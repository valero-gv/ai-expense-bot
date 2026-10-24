#!/usr/bin/env python3
"""Audit Cyrillic exceptions without reading .env, build output or credentials."""

import hashlib
from pathlib import Path
import re
import sys

ROOT = Path(__file__).resolve().parent.parent
CYRILLIC = re.compile(r"[\u0400-\u04ff]")
LEGACY_CATEGORIES = (
    "\u0435\u0434\u0430", "\u0442\u0440\u0430\u043d\u0441\u043f\u043e\u0440\u0442",
    "\u0436\u0438\u043b\u044c\u0451", "\u0440\u0430\u0437\u0432\u043b\u0435\u0447\u0435\u043d\u0438\u044f",
    "\u0437\u0434\u043e\u0440\u043e\u0432\u044c\u0435", "\u043f\u043e\u043a\u0443\u043f\u043a\u0438",
    "\u0434\u0440\u0443\u0433\u043e\u0435",
)
CATEGORY_LITERAL = re.compile(r"([\"'`])(?:category=)?(?:" + "|".join(map(re.escape, LEGACY_CATEGORIES)) + r")\1")
CATEGORY_FILES = {
    "internal/domain/domain.go", "web/app.js", "web/app.test.js",
    "scripts/smoke_api.py", "docs/api.md",
}
HISTORICAL = {
    "docs/original-task.md": ("b20533e6c2e15b400658903f31697fae69c22bd1628d480a4c42a980bb95eab3", "unmodified historical specification"),
    "internal/store/migrations/001_init.sql": ("8c5b256779f402b747f11270948f7ebbaedd7feddeef2d58557b67070b1c9136", "immutable migration with persisted category keys"),
}
MULTILINGUAL_TESTS = {
    "internal/agent/engine_test.go": {"TestRussianInputWithEnglishResponses", "TestMultilingualQueryUsesCharacterLimit"},
    "internal/telegram/telegram_test.go": {"TestRussianMessagesRemainSupported"},
}


def sources():
    for name in ("README.md", ".env.example", ".gitignore", ".dockerignore", "compose.yaml", "Dockerfile", "go.mod", "go.sum"):
        yield ROOT / name
    for directory in ("cmd", "internal", "web", "scripts", "docs", ".github"):
        for path in sorted((ROOT / directory).rglob("*")):
            if path.is_file() and not path.is_symlink() and "__pycache__" not in path.parts:
                if path.suffix in {".go", ".md", ".py", ".js", ".html", ".css", ".json", ".sql", ".yaml", ".yml"}:
                    yield path


def main():
    failures = []
    exceptions = []
    for filename, (digest, reason) in HISTORICAL.items():
        if hashlib.sha256((ROOT / filename).read_bytes()).hexdigest() != digest:
            failures.append(f"{filename}: historical source changed ({reason})")
    for path in sources():
        name = path.relative_to(ROOT).as_posix()
        function = ""
        js_test = ""
        for number, line in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
            match = re.match(r"func\s+(?:\([^)]*\)\s*)?(\w+)(?:\[.*\])?\(", line)
            if match:
                function = match[1]
            match = re.match(r"test\('([^']+)'", line)
            if match:
                js_test = match[1]
            if not CYRILLIC.search(line):
                continue
            reason = ""
            if name in HISTORICAL:
                reason = HISTORICAL[name][1]
            elif name in CATEGORY_FILES and not CYRILLIC.search(CATEGORY_LITERAL.sub("", line)):
                reason = "quoted persisted category key; English display label is separate"
            elif function in MULTILINGUAL_TESTS.get(name, set()):
                reason = f"explicit multilingual input regression: {function}"
            elif name == "web/app.test.js" and js_test.startswith("multilingual user text"):
                reason = "explicit multilingual user-content regression"
            elif name == "internal/agent/engine.go":
                if line.startswith("var explicitID =") and not CYRILLIC.search(line.replace("\u0438\u0434", "")):
                    reason = "Russian ID abbreviation recognition"
                elif function == "latestReference" and not CYRILLIC.search(line.replace("\u043f\u043e\u0441\u043b\u0435\u0434\u043d", "")):
                    reason = "Russian latest-expense recognition"
            location = f"{name}:{number}"
            if reason:
                exceptions.append(f"ALLOW {location}: {reason}")
            else:
                failures.append(f"UNEXPLAINED {location}")
    print("\n".join(exceptions))
    if failures:
        print("\n".join(failures), file=sys.stderr)
        return 1
    print(f"PASS: {len(exceptions)} Cyrillic lines, every exception explained; no .env read")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
