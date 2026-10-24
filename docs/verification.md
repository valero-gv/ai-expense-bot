# Verification record

## Local verification: 23 October 2026

The English revision preserves the original assignment, routes, expense fields and persisted category keys. The original task and migration `001_init.sql` remain byte-for-byte unchanged. No Git history was rewritten. The checks below were completed locally before publication.

No personal `.env` was read, and no real Telegram or LLM account was connected during this revision. Local test data is disposable and separate from the previously running demonstration.

### Backend review and regression coverage

The review examined money parsing/serialization, SQL aggregates, transaction boundaries, update replay, concurrent access, tenant scope, deadlines, cancellation and process shutdown. Fixes and regressions cover:

- A list's count and rows now use one PostgreSQL statement snapshot. A real-database interleaving test reproduces the former count/row mismatch.
- Scoped target reads lock the row before a bot builds a correction. Query selectors re-read the locked target and check its comment again. Administrative whole-record replacements remain last-writer-wins.
- Replayed older Telegram updates no longer overwrite a more recent sender label.
- PostgreSQL-incompatible NUL characters are rejected in comments and queries at validation boundaries.
- Permanent Telegram delivery failures no longer starve later updates; transient transport, 429 and 5xx failures still retry.
- HTTP API operations have a 10s context deadline and health checks 2s; earlier client deadlines and cancellation are preserved.
- Shutdown cancels and joins polling, drains HTTP requests, and closes overdue connections. HTTP startup failure also cancels/joins polling before the database is closed.
- Selector validation, Unicode character limits, late cancellation and provider-error sanitization have additional regression coverage.

### Commands and results

The following checks were actually run on macOS arm64 with Go 1.26.9 and a separate PostgreSQL 17.7 cluster. `TEST_DATABASE_URL` pointed to that disposable cluster, `TEST_REQUIRE_DATABASE=1` and `RUN_LIVE_LLM=0` were set. No existing application or database was used.

| Check | Result |
|---|---|
| `gofmt -l cmd internal web/embed.go` | PASS: no unformatted Go files |
| `go vet ./...` | PASS |
| `go test -race -count=1 -timeout=5m -json ./...` | PASS: eight Go test packages; 191 passing test/subtest events, no database test skipped |
| Required-DB guard with `TEST_DATABASE_URL` unset | PASS: both database suites fail explicitly instead of skipping |
| `go build -o artifacts/validation/expensebot ./cmd/server` with `CGO_ENABLED=0` | PASS; the binary served the isolated interface check |
| `node --check web/app.js` | PASS |
| `node --test web/app.test.js` | PASS: 7 tests |
| `python3 scripts/check_cyrillic.py` | PASS: 82 matching lines, each with a documented exception |
| `BASE_URL=http://127.0.0.1:18080 python3 scripts/smoke_api.py` | PASS: health, categories, exact 0.10 + 0.20 total, CRUD, filters, sorting and validation |
| Browser against the separate app on port 18080 | PASS: English labels, seven category labels, amount validation, create/edit confirmations, delete dialog/cancel, filters/reset, empty states, `lang=en`; no JavaScript errors |
| Workflow YAML, documentation links, JSON examples and Python syntax | PASS: local validation only |
| Original specification and applied migration hashes | PASS: unchanged |
| Public source credential-pattern scan and `git diff --check` | PASS; `.env` excluded from all reads |

The only skipped Go **test** was `TestLiveLLMExpensePhrases`, intentionally disabled. The embedded `web` Go package has no Go test files; its JavaScript tests ran separately. These local results are separate from GitHub Actions results.

Regression tests also ran independently per subsystem. The browser exercised disposable data in a dedicated UI database; its test expense was cleaned up afterward. A local screenshot is saved under ignored `artifacts/validation/english-panel.png`.

### Environment and limits

Docker was installed but its daemon was stopped. It was not started because previously configured containers could automatically reconnect to real accounts. Consequently, this revision's Docker image and Compose launch were not rerun. An installed PostgreSQL 17.7 server and the Go 1.26.9 toolchain are used locally instead. CI is configured for PostgreSQL 16, matching Compose.

At the time of the local checks on 23 October, the GitHub Actions workflow had not run because publication was not yet authorized. Published revisions have their own results in the [CI run history](https://github.com/valero-gv/ai-expense-bot/actions/workflows/ci.yml). The external-provider test is intentionally skipped. English prompt quality against a live provider and delivery from two real Telegram accounts were not tested in this revision.

## Historical evidence: 16 October 2026

The earlier Russian-language build was exercised with a real Groq `openai/gpt-oss-20b` endpoint and PostgreSQL. All six assignment scenarios passed in an isolated schema: create coffee, correct its amount, create yesterday's taxi, aggregate monthly food spending, list the week and delete the taxi. That run made 12 model requests and included pauses for provider quotas. It does **not** establish live-provider behavior of the revised English prompt.

A user also manually verified the actual Telegram bot: greeting, coffee creation, correction from 350 to 450, deletion, today's list and an exact total of 1,500 RUB for two remaining expenses. Database, REST and dashboard reads confirmed those outcomes. A two-account Telegram test was not performed; automated owner-isolation tests used independent identities.

The earlier build passed Docker startup, Go vet/race tests with PostgreSQL, browser CRUD/filter/mobile checks, exact-money API smoke tests and a vulnerability scan after dependency updates. These are historical results, not claims that every check was repeated for the current revision.
