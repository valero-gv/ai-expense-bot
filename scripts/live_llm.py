#!/usr/bin/env python3
"""Explicitly run the real-provider integration test without exposing its keys."""

import argparse
from decimal import Decimal
import os
from pathlib import Path
import re
import signal
import subprocess
import sys


ROOT = Path(__file__).resolve().parent.parent
LLM_NAMES = ("LLM_API_KEY", "LLM_BASE_URL", "LLM_MODEL")
CONFIG_NAMES = LLM_NAMES + ("LLM_TIMEOUT",)
SECRET_NAMES = ("LLM_API_KEY", "TELEGRAM_BOT_TOKEN")
RUN_NAMES = ("RUN_LIVE_LLM", "LIVE_LLM_CASE_LIMIT", "LIVE_LLM_TURN_PAUSE")
DURATION_PART = re.compile(r"(\d+(?:\.\d*)?|\.\d+)(ns|us|µs|μs|ms|s|m|h)")
DURATION_UNITS = {
    "ns": 1, "us": 1000, "µs": 1000, "μs": 1000, "ms": 1_000_000,
    "s": 1_000_000_000, "m": 60_000_000_000, "h": 3_600_000_000_000,
}


class ConfigError(Exception):
    """Messages must describe names/locations only, never configuration values."""


def duration(value, minimum, maximum, message):
    """Validate Go duration syntax and bounds without printing its input."""
    raw = value.removeprefix("+")
    total = Decimal(0)
    position = 0
    if raw != "0":
        for part in DURATION_PART.finditer(raw):
            if part.start() != position:
                raise ValueError(message)
            total += Decimal(part[1]) * DURATION_UNITS[part[2]]
            position = part.end()
        if position != len(raw) or position == 0:
            raise ValueError(message)
    if not minimum <= total <= maximum:
        raise ValueError(message)
    return value


def turn_pause(value):
    try:
        return duration(value, 0, 300_000_000_000, "duration must be between 0s and 5m")
    except ValueError as error:
        raise argparse.ArgumentTypeError(str(error)) from None


def dotenv(path):
    """Read simple NAME=value lines literally; never evaluate shell expressions."""
    try:
        contents = path.read_text(encoding="utf-8-sig")
    except FileNotFoundError:
        return {}
    except (OSError, UnicodeError):
        raise ConfigError("Cannot read the repository .env file") from None
    result = {}
    for number, line in enumerate(contents.splitlines(), 1):
        line = line.strip()
        if not line or line.startswith("#"):
            continue
        if line.startswith("export "):
            line = line[7:].lstrip()
        match = re.fullmatch(r"([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(.*)", line)
        if not match:
            raise ConfigError(f".env line {number}: expected NAME=value")
        name, value = match.groups()
        if value.startswith("'"):
            quoted = re.fullmatch(r"'([^']*)'\s*(?:#.*)?", value)
            if not quoted:
                raise ConfigError(f".env line {number}: invalid quoted value for {name}")
            value = quoted[1]
        elif value.startswith('"'):
            quoted = re.fullmatch(r'"((?:[^"\\]|\\["\\])*)"\s*(?:#.*)?', value)
            if not quoted:
                raise ConfigError(f".env line {number}: invalid quoted value for {name}")
            value = re.sub(r'\\(["\\])', r'\1', quoted[1])
        else:
            value = re.split(r"\s+#", value, maxsplit=1)[0].rstrip()
        if name in CONFIG_NAMES or name in SECRET_NAMES:
            result[name] = value
    return result


def arguments():
    parser = argparse.ArgumentParser(
        description=(
            "Opt in to real LLM provider requests for TestLiveLLMExpensePhrases. "
            "The test creates and drops an isolated PostgreSQL schema. "
            "Normal test runs skip this live test."
        ),
        epilog=(
            "Reads LLM_API_KEY, LLM_BASE_URL and LLM_MODEL from your environment "
            "or the repository .env (environment wins). Optional LLM_TIMEOUT "
            "defaults to 30s and must be between 1s and 2m. The 60s pause helps stay "
            "within free-provider quotas; provider requests can still be billable. "
            "The test has a 45m total timeout to cover the maximum supported pauses. "
            "No Telegram token is passed to the tests container."
        ),
    )
    parser.add_argument("--case-limit", type=int, choices=range(1, 7), default=6,
                        help="number of consecutive live cases (default: 6)")
    parser.add_argument("--turn-pause", type=turn_pause, default="60s",
                        help="pause between turns, Go duration 0s..5m (default: 60s)")
    parser.add_argument("--no-build", action="store_true",
                        help="reuse the existing tests image instead of building it")
    return parser.parse_args()


def main():
    args = arguments()  # --help exits before any secret-bearing file is read.
    try:
        values = dotenv(ROOT / ".env")
        config = {name: os.environ.get(name, values.get(name, "")) for name in LLM_NAMES}
        missing = [name for name, value in config.items() if not value.strip()]
        if missing:
            raise ConfigError("Missing configuration: " + ", ".join(missing))
        config["LLM_TIMEOUT"] = os.environ.get("LLM_TIMEOUT", values.get("LLM_TIMEOUT", "")) or "30s"
        invalid = [name for name, value in config.items() if "\n" in value or "\r" in value or "\0" in value]
        if invalid:
            raise ConfigError("Configuration must use single-line values: " + ", ".join(invalid))
        try:
            duration(config["LLM_TIMEOUT"], 1_000_000_000, 120_000_000_000,
                     "LLM_TIMEOUT must be a duration between 1s and 2m")
        except ValueError as error:
            raise ConfigError(str(error)) from None
    except ConfigError as error:
        print(str(error), file=sys.stderr)
        return 2

    secrets = set()
    for source in (os.environ, values):
        for name in SECRET_NAMES:
            value = source.get(name, "")
            if value:
                secrets.add(value)
                secrets.update(part for part in value.splitlines() if part)
    secrets = sorted(secrets, key=len, reverse=True)

    child_env = os.environ.copy()  # Preserve DOCKER_CONFIG/DOCKER_HOST and normal Docker settings.
    child_env.pop("TELEGRAM_BOT_TOKEN", None)
    child_env.update(config)
    child_env.update({
        "RUN_LIVE_LLM": "1",
        "LIVE_LLM_CASE_LIMIT": str(args.case_limit),
        "LIVE_LLM_TURN_PAUSE": args.turn_pause,
    })
    command = ["docker", "compose", "--profile", "test", "run"]
    if not args.no_build:
        command.append("--build")
    command.append("--rm")
    for name in CONFIG_NAMES + RUN_NAMES:
        command.extend(("-e", name))  # Bare names: no credentials in process arguments.
    command.extend((
        "tests", "go", "test", "-v", "-count=1", "-timeout=45m", "-run",
        "^TestLiveLLMExpensePhrases$", "./internal/integration",
    ))

    print(f"Running {args.case_limit} live LLM cases with a {args.turn_pause} pause; using an isolated database schema.", flush=True)
    try:
        process = subprocess.Popen(
            command, cwd=ROOT, env=child_env, stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT, text=True, encoding="utf-8", errors="replace", bufsize=1,
        )
    except OSError:
        print("Cannot start Docker Compose; check the Docker installation and daemon.", file=sys.stderr)
        return 2
    try:
        for line in process.stdout:
            for secret in secrets:
                line = line.replace(secret, "[REDACTED]")
            print(line, end="", flush=True)
        status = process.wait()
        return status if status >= 0 else 128 - status
    except KeyboardInterrupt:
        process.send_signal(signal.SIGINT)
        try:
            process.wait(timeout=10)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait()
        return 130
    except (OSError, UnicodeError):
        process.terminate()
        process.wait()
        print("Unable to read Docker Compose output.", file=sys.stderr)
        return 2


if __name__ == "__main__":
    sys.exit(main())
