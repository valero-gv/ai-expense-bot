# REST API

Базовый URL: `http://localhost:8080`. Авторизации нет: это административный API всех пользователей. Допускаются только localhost/loopback Host. POST/PUT требуют `Content-Type: application/json`; Origin, если передан, должен совпадать с origin сервера. CORS не открыт.

## Расход и ввод

```json
{
  "id": 12,
  "user_id": 123456789,
  "amount": "350.00",
  "category": "еда",
  "spent_at": "2026-10-16",
  "comment": "кофе",
  "created_at": "2026-10-16T08:00:00Z",
  "updated_at": "2026-10-16T08:00:00Z"
}
```

POST/PUT принимают только `user_id`, `amount`, `category`, `spent_at`, `comment`. Comment необязателен; пустая строка означает отсутствие комментария. PUT полностью заменяет поля ввода и может изменить владельца; id/created_at/updated_at назначает сервер. Неизвестные поля и лишний JSON отклоняются. Тело до 16 KiB. `amount` — строка с 0–2 десятичными знаками (точка или запятая), диапазон 0.01–999999999.99 RUB. JSON-числа, отрицательные суммы, exponent и округление из более точного числа не поддерживаются. Comment до 1000 символов. User ID — положительное целое до 2^52−1.

Категории строго: `еда`, `транспорт`, `жильё`, `развлечения`, `здоровье`, `покупки`, `другое`. Даты `YYYY-MM-DD`, реальные календарные даты. Время — RFC3339 с offset.

## Endpoints

| Метод | Путь | Успешный ответ |
|---|---|---|
| GET | `/healthz` | 200 `{"status":"ok"}`; 503 если БД недоступна |
| GET | `/api/categories` | 200 `{"items":["еда", ...]}` |
| GET | `/api/users` | 200 `{"items":[{"id":123,"label":"Анна (@anna)"}]}` |
| GET | `/api/expenses` | 200 Page |
| GET | `/api/summary` | 200 `{"amount":"700.00","count":2}` |
| GET | `/api/expenses/{id}` | 200 Expense |
| POST | `/api/expenses` | 201 Expense + Location |
| PUT | `/api/expenses/{id}` | 200 Expense |
| DELETE | `/api/expenses/{id}` | 204, пустое тело |

Page:

```json
{"items":[],"total":0,"page":1,"page_size":20}
```

Список и summary принимают одинаковые фильтры: `user_id`, `from`, `to`, `category`, `q` (буквальная подстрока комментария, без учёта регистра). `from`/`to` включены, можно указать один край. Нельзя from > to. Список сортируется по `spent_at DESC, id DESC`. `page` по умолчанию 1, `page_size` по умолчанию 20, максимум 100; предельная page 1 000 000. Summary игнорирует пагинацию при подсчёте. Неизвестные и повторяющиеся query-параметры отклоняются. При page за пределами выборки items пустой, total остаётся общим.

## Ошибки

```json
{"error":{"code":"invalid_request","message":"..."}}
```

| HTTP | code | Причина |
|---|---|---|
| 400 | `invalid_request` | Некорректные ID, поля, JSON, деньги, фильтры, категория, даты |
| 403 | `invalid_request` | Недопустимый Host/Origin/Fetch-Metadata |
| 404 | `not_found` | Расход или endpoint не найден |
| 405 | `invalid_request` | Неподдерживаемый HTTP-метод; Allow указан |
| 413 | `invalid_request` | Тело больше лимита |
| 415 | `invalid_request` | POST/PUT без application/json |
| 500 | `internal_error` | Ошибка хранилища, детали SQL не раскрываются |
| 503 | `internal_error` | БД недоступна при healthcheck |

## Примеры

```sh
curl -i http://localhost:8080/healthz

curl -i http://localhost:8080/api/expenses \
  -H 'Content-Type: application/json' \
  -d '{"user_id":123456789,"amount":"350.00","category":"еда","spent_at":"2026-10-16","comment":"кофе"}'

curl -G http://localhost:8080/api/expenses \
  --data-urlencode 'user_id=123456789' \
  --data-urlencode 'from=2026-10-01' \
  --data-urlencode 'to=2026-10-31' \
  --data-urlencode 'category=еда' \
  --data-urlencode 'page=1' --data-urlencode 'page_size=20'

curl -G http://localhost:8080/api/summary \
  --data-urlencode 'user_id=123456789' --data-urlencode 'category=еда'

# Замените 12 реальным id из ответа создания:
curl -X PUT http://localhost:8080/api/expenses/12 \
  -H 'Content-Type: application/json' \
  -d '{"user_id":123456789,"amount":"450.00","category":"еда","spent_at":"2026-10-16","comment":"кофе"}'

curl -i -X DELETE http://localhost:8080/api/expenses/12
```

Ручной POST в панели/API может создать пользователя до первого сообщения боту: его подпись будет `Telegram <ID>`. Имя обновится после настоящего Telegram update. Новое имя не принимается произвольным полем расхода.
