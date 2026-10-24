# Take-home assignment: AI expense-tracking bot

English translation of the [original Russian assignment](original-task.md). The original remains unchanged.

Build a service for tracking family expenses. A family member sends the Telegram bot a message such as “coffee 350”, and the expense is saved: the bot identifies the amount, category and date automatically.

The service has two parts:

- **Telegram bot with an AI agent.** Understands ordinary text to add, view, update and delete expenses. Each family member can access only their own expenses.
- **Web dashboard for the head of the family.** Shows every family member's expenses and allows them to be edited. Authentication is not required.

## Data model

Expense entity:

| Field | Meaning |
|---|---|
| `id` | Expense identifier |
| `user_id` | Owner's Telegram ID |
| `amount` | Amount |
| `category` | Category: Food, Transport, Housing, Entertainment, Health, Shopping or Other |
| `spent_at` | Expense date |
| `comment` | Optional comment |
| `created_at`, `updated_at` | Creation and update times |

## Telegram bot

The bot provides CRUD operations on the user's expenses through natural language, without buttons or commands:

- Add: “coffee 350”, “taxi 1200 yesterday”.
- View: “how much did I spend on food this month”, “show my expenses for the week”.
- Update: “the last one was 450, not 350”.
- Delete: “delete taxi”.

Other users' expenses are inaccessible through the bot.

## Web dashboard

CRUD for all family members' expenses: a list filtered by family member, period and category, plus creation, editing and deletion. The dashboard communicates with the backend through a REST API.

## Requirements

- Backend in Go; any frontend stack.
- The application and all dependencies start through a single `docker compose` invocation.
- LLM access is configurable.

We recommend writing the code with AI assistants: we also assess how effectively you use them in your work.

Everything else is at your discretion.

Deadline: five working days from receiving the assignment.

---

Implementation compatibility note, outside the translated assignment: the English category names above translate the original labels. The existing application retains the original Russian category keys in storage and its API so existing data remains compatible. See the [API category mapping](api.md#category-compatibility). This note describes the implementation and does not change the original task.
