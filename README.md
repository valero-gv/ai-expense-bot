# AI Expense Bot

A small Go service for tracking family expenses through natural-language Telegram messages. An LLM selects structured tools; the server validates and executes them against PostgreSQL. A local dashboard manages every family member's expenses through a REST API.

Built as a take-home assignment and a backend engineering example. [English specification](docs/task.en.md) · [Unmodified original](docs/original-task.md) · [Design decisions](docs/decisions.md) · [API](docs/api.md) · [Verification record](docs/verification.md).

## Stack and architecture

Go 1.26.9, PostgreSQL 16, `net/http`, `pgx`, and an embedded HTML/CSS/JavaScript dashboard. The frontend has no build step or third-party JavaScript dependencies.

```text
Telegram long polling -> bounded LLM/tool loop -> user-scoped transaction -> PostgreSQL
Browser                -> local REST API      -> administrative repository -> PostgreSQL
```

The bot supports creating, listing, totaling, updating and deleting expenses. English is the default language for the interface and server-generated replies. Russian input remains supported and has explicit multilingual regression coverage. Stored comments and names are user data and are not translated. Existing category keys remain unchanged; their display labels are English.

## Quick start

Requires Docker with Compose v2:

```sh
cp .env.example .env
docker compose up --build
```

Open <http://localhost:8080>. Without credentials, the dashboard and API work and Telegram polling is disabled. To enable the bot, set `TELEGRAM_BOT_TOKEN`, `LLM_API_KEY`, `LLM_BASE_URL` and `LLM_MODEL` in your local `.env`, then recreate the app with `docker compose up --build`. The provider must support OpenAI-compatible Chat Completions with strict function tools. Availability and model quality depend on the provider.

Example messages:

```text
coffee 350
the latest expense was 450, not 350
taxi 1200 yesterday
how much did I spend on food this month?
show expenses for this week
delete taxi
```

An ambiguous update or deletion asks for an explicit ID, such as `delete #12`. One expense can be changed or deleted per message. Amounts are in RUB. `APP_TIMEZONE` controls calendar dates; it defaults to `Europe/Moscow` to preserve the original behavior. `LLM_TIMEOUT` bounds an entire bot turn (default 30s, configurable from 1s to 2m).

**Local administrative access only.** The dashboard intentionally has no authentication, as required by the assignment. Anyone with access to the local port can read and change every user's expenses. The port binds to loopback, and PostgreSQL has no published port. Do not expose the app through a public port or tunnel without adding authentication and authorization. Host/origin checks reduce browser risks; they are not authentication.

Keep credentials in `.env`; it is excluded from Git and the Docker build context. Do not share `docker compose config` output with real credentials. `docker compose down` preserves the database volume; adding `-v` deletes that volume.

## Tests

The default suite uses scripted HTTP model responses and real PostgreSQL for integration tests. It does not need Telegram or LLM credentials.

```sh
# Isolated Compose project; explicitly ignores the local .env.
docker compose --env-file /dev/null -p expensebot-tests --profile test run --build --rm tests
```

This runs `go vet` and `go test -race` against a separate PostgreSQL service. Database tests fail instead of skipping when `TEST_REQUIRE_DATABASE=1` is set; the Compose test service and CI both set it.

For local Go development, use Go 1.26.9 and a C compiler for the race detector:

```sh
gofmt -l cmd internal web/embed.go
go vet ./...
TEST_REQUIRE_DATABASE=1 TEST_DATABASE_URL='postgres://user:password@localhost:5432/expensebot_test?sslmode=disable' RUN_LIVE_LLM=0 go test -race -count=1 -timeout=5m ./...
node --check web/app.js
node --test web/app.test.js
python3 scripts/check_cyrillic.py
```

Each database test creates and drops its own schema. Without `TEST_DATABASE_URL`, local database tests explicitly report `SKIP`; that is not a successful integration run. The external-provider test is intentionally skipped unless separately opted into. [Verification](docs/verification.md) distinguishes current automated checks, historical live tests and checks not performed.

The GitHub Actions workflow configures formatting, `go vet`, race-enabled tests with a PostgreSQL 16 service, dashboard tests and the localization audit. It requires no personal secrets. API smoke testing is available through `python3 scripts/smoke_api.py` against a disposable local instance.

## Engineering decisions

- **Exact money:** integer minor units in Go, `NUMERIC(11,2)` in PostgreSQL and decimal strings in JSON. Floats and implicit rounding are rejected; database aggregates include every matching row, independently of pagination.
- **Ownership at the boundary:** the owner comes from the trusted Telegram sender, not model arguments. Every bot query uses an immutable owner scope; administrative access is a separate repository path.
- **Atomic turns and idempotency:** expense changes, conversation history and the saved reply commit together. A per-user PostgreSQL advisory lock serializes bot turns; duplicate update IDs return the committed reply. Repeated mutation tools within one turn reuse the result. A new Telegram message is a new intent, even if its text is identical.
- **Concurrency:** a list's count and rows share one SQL statement snapshot. Scoped reads lock mutation targets before a bot builds a partial correction. Administrative `PUT` remains a full replacement; independent administrative edits use last-writer-wins semantics.
- **Bounded work:** model iterations, tools, history, payloads and deadlines are limited. API database work has a 10s context budget, readiness checks 2s. Shutdown cancels polling, drains HTTP requests for up to 10s and closes overdue connections to cancel request contexts before releasing resources.
- **Reliability and diagnostics:** transient Telegram failures retry; permanently undeliverable messages do not block later updates. Structured logs expose safe error categories and HTTP status codes, without tokens, provider bodies or user messages. `/healthz` checks HTTP/database readiness, not external Telegram or model availability.

## Trade-offs and limits

A bot turn holds a database connection and transaction during model requests. This keeps rollback and replay behavior simple, at the cost of lock duration and throughput. Polling is sequential and intended for one bot instance; a permanently failing transient operation can still delay later messages. Delivery can repeat after an uncertain network result, while the saved expense mutation is deduplicated.

The project has been exercised as a local demonstration. Load capacity and production availability have not been measured. Administrative POST requests have no idempotency key; stored history and processed update IDs have no retention policy; the dashboard refreshes on navigation or user action.

Useful next steps would be authenticated family access, explicit edit-conflict handling, retention, operational metrics and durable delivery handling. A deployment's measured needs should guide any additional infrastructure.

## AI assistance

AI assistants were used for design, implementation, tests, documentation and independent review. The verification record documents automated checks and historical manual Telegram tests. See [localization exceptions](docs/localization.md) for the preserved original source, category compatibility and multilingual tests.
