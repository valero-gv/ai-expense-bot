# REST API

Base URL: `http://localhost:8080`. This is an administrative API for all family members, without authentication. Only localhost and loopback `Host` values are accepted. POST and PUT require `Content-Type: application/json`. If supplied, `Origin` must match the server origin. Cross-origin access is not enabled.

## Expense and input

```json
{
  "id": 12,
  "user_id": 123456789,
  "amount": "350.00",
  "category": "\u0435\u0434\u0430",
  "spent_at": "2026-10-16",
  "comment": "coffee",
  "created_at": "2026-10-16T08:00:00Z",
  "updated_at": "2026-10-16T08:00:00Z"
}
```

POST and PUT accept only `user_id`, `amount`, `category`, `spent_at` and `comment`. Comment is optional; an empty string means no comment. PUT replaces all input fields and may change the owner. The server controls `id`, `created_at` and `updated_at`. Unknown fields and trailing JSON are rejected. The body limit is 16 KiB.

- `user_id`: a positive integer up to `4503599627370495` (`2^52 − 1`).
- `amount`: a JSON string containing decimal digits and zero to two fractional digits, with a dot or comma separator. Range: `0.01`–`999999999.99` RUB. JSON numbers, negative values, exponent notation and excess precision are rejected. No rounding occurs; responses always contain a dot and two decimal places.
- `spent_at`: a real calendar date in `YYYY-MM-DD`, from `0001-01-01` through `9999-12-31`.
- `comment`: valid Unicode text, at most 1,000 characters; NUL is rejected. User text is preserved in its original language.
- Timestamps: RFC3339 with a timezone offset, including `Z` for UTC.

### Category compatibility

The Russian category keys below are stable stored and wire values from the original assignment. They are an intentional exception to the English interface and documentation. English labels are for display; they are **not** accepted as API category values. JSON Unicode escapes and the corresponding literal characters are equivalent. The escaped value in the examples is the Food key.

| English display label | Stored/API key |
|---|---|
| Food | `еда` |
| Transport | `транспорт` |
| Housing | `жильё` |
| Entertainment | `развлечения` |
| Health | `здоровье` |
| Shopping | `покупки` |
| Other | `другое` |

## Endpoints

| Method | Path | Successful response |
|---|---|---|
| GET | `/healthz` | 200 `{"status":"ok"}` |
| GET | `/api/categories` | 200 `{"items":["\u0435\u0434\u0430", ...]}`; the seven stored keys |
| GET | `/api/users` | 200 `{"items":[{"id":123,"label":"Anna (@anna)"}]}` |
| GET | `/api/expenses` | 200 Page |
| GET | `/api/summary` | 200 `{"amount":"700.00","count":2}` |
| GET | `/api/expenses/{id}` | 200 Expense |
| POST | `/api/expenses` | 201 Expense, with a `Location` header |
| PUT | `/api/expenses/{id}` | 200 Expense |
| DELETE | `/api/expenses/{id}` | 204, no body |

Page format:

```json
{"items":[],"total":0,"page":1,"page_size":20}
```

### Filters and pagination

List and summary accept the same filters:

| Parameter | Meaning |
|---|---|
| `user_id` | One family member's Telegram ID |
| `from`, `to` | Inclusive date boundaries; either may be omitted; `from` must not exceed `to` |
| `category` | One stored category key from the table above |
| `q` | Case-insensitive literal substring of the comment, up to 200 Unicode characters; `%` and `_` are literal characters, NUL is rejected |
| `page` | Positive page number, default 1, maximum 1,000,000 |
| `page_size` | Positive number of rows per page, default 20, maximum 100 |

Unknown, repeated and explicitly empty query parameters are rejected. Omit unused parameters. Rows are ordered by `spent_at DESC, id DESC`. A page beyond the end returns an empty `items` array while retaining the full matching `total`.

The list's count and rows share a single PostgreSQL statement snapshot. Summary calculates the exact sum and count over all matching rows, ignoring pagination. Summary is a separate read, so concurrent writes can change its result between requests.

### Deadlines and concurrent writes

API operations have a 10-second server context deadline; health checks have a 2-second deadline. These budgets cover database execution, connection-pool waits and lock waits. Earlier caller deadlines and client cancellation propagate to the store. A deadline error returns HTTP 503 with code `internal_error` and the safe message `Request timed out`; database details are not exposed. A disconnected client may no longer receive a response.

Administrative PUT is a full replacement without a version check: concurrent edits use last-writer-wins behavior. POST has no idempotency key. The bot uses separate owner-scoped transactions and row locks for its corrections; those protections do not turn administrative PUT into a partial update.

## Errors

```json
{"error":{"code":"invalid_request","message":"..."}}
```

| HTTP | Code | Meaning |
|---|---|---|
| 400 | `invalid_request` | Invalid ID, input, JSON, amount, filter, category or date |
| 403 | `invalid_request` | Rejected Host, Origin or Fetch Metadata |
| 404 | `not_found` | Expense or endpoint not found |
| 405 | `invalid_request` | Unsupported method; `Allow` lists supported methods |
| 413 | `invalid_request` | Request body exceeds the limit |
| 415 | `invalid_request` | POST/PUT without `application/json` |
| 500 | `internal_error` | Store failure; SQL and connection details are hidden |
| 503 | `internal_error` | Operation deadline exceeded, or database unavailable during a health check |

## Examples

The literal category in query examples is the legacy Food wire key, not an untranslated UI label.

```sh
curl -i http://localhost:8080/healthz

curl -i http://localhost:8080/api/expenses \
  -H 'Content-Type: application/json' \
  -d '{"user_id":123456789,"amount":"350.00","category":"\u0435\u0434\u0430","spent_at":"2026-10-16","comment":"coffee"}'

curl -G http://localhost:8080/api/expenses \
  --data-urlencode 'user_id=123456789' \
  --data-urlencode 'from=2026-10-01' \
  --data-urlencode 'to=2026-10-31' \
  --data-urlencode 'category=еда' \
  --data-urlencode 'page=1' --data-urlencode 'page_size=20'

curl -G http://localhost:8080/api/summary \
  --data-urlencode 'user_id=123456789' --data-urlencode 'category=еда'

# Replace 12 with the ID returned when creating the expense:
curl -X PUT http://localhost:8080/api/expenses/12 \
  -H 'Content-Type: application/json' \
  -d '{"user_id":123456789,"amount":"450.00","category":"\u0435\u0434\u0430","spent_at":"2026-10-16","comment":"coffee"}'

curl -i -X DELETE http://localhost:8080/api/expenses/12
```

Creating an expense through the panel or API may register a user before their first bot message. Their initial label is `Telegram <ID>` and is refreshed by a real Telegram update. Expense input does not accept an arbitrary display-name field. A user remains in the family list after all their expenses are deleted.
