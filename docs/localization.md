# Language and compatibility

English is the default for documentation, dashboard labels, validation errors, server-generated bot replies, ordinary test fixtures and demonstration messages. Currency remains RUB and configured calendar behavior is unchanged.

The model receives the user's original text, including Russian. The English system prompt explicitly allows multilingual input and returns English replies. User-supplied comments and sender names remain data: the application does not translate or rewrite them.

## Cyrillic exceptions

Run `python3 scripts/check_cyrillic.py`. It scans explicit source/documentation roots, reports every matching line with a reason, and fails on unexplained Cyrillic. It never scans `.env`, Git objects or ignored build output. It also checks SHA-256 hashes for the historical specification and existing migration.

| Location | Reason |
|---|---|
| `docs/original-task.md` | Unmodified historical assignment; the English translation is `task.en.md`. |
| `internal/store/migrations/001_init.sql` | Existing database CHECK values. Editing an applied migration would break checksum verification and compatibility. |
| `internal/domain/domain.go` | Seven existing persisted category constants. `CategoryLabel` supplies English labels without a data migration. |
| `web/app.js` | Category label/icon mappings and default form values keep the REST keys unchanged. Labels shown to the user are English. |
| `web/app.test.js` | Category round-trip fixtures verify existing wire values. A separately named multilingual test verifies preservation of Russian user content. |
| `scripts/smoke_api.py` | Explicitly named legacy category constants exercise the actual API contract. Demo comments are English. |
| `docs/api.md` | Any quoted category keys are wire values, not untranslated interface labels. |
| `internal/agent/engine.go` | Russian ID abbreviations and the latest-expense stem preserve existing selector recognition. |
| `internal/agent/engine_test.go` | Named Russian-input and Unicode-length regressions cover all six original scenarios, selectors and character limits. |
| `internal/telegram/telegram_test.go` | Named Russian-input regression preserves message text and sender labels at the transport boundary. |

Stored comments, history and existing user labels can contain any supported language. Seeing original user content in a record is expected; English defaults apply to application-authored text. No stored category, API route, JSON field, migration or existing record was renamed for presentation.
