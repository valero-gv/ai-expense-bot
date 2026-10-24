package domain

import (
	"context"
	"errors"
	"time"
)

var ErrNotFound = errors.New("expense not found")
var ErrInvalid = errors.New("invalid input")

// Category keys are persisted values from the original assignment. Keep them
// stable when changing the language used by the interface.
const (
	Food          = "еда"
	Transport     = "транспорт"
	Housing       = "жильё"
	Entertainment = "развлечения"
	Health        = "здоровье"
	Shopping      = "покупки"
	Other         = "другое"
)

var Categories = []string{Food, Transport, Housing, Entertainment, Health, Shopping, Other}

// CategoryLabel translates a stored key for display and preserves unknown keys.
func CategoryLabel(key string) string {
	switch key {
	case Food:
		return "Food"
	case Transport:
		return "Transport"
	case Housing:
		return "Housing"
	case Entertainment:
		return "Entertainment"
	case Health:
		return "Health"
	case Shopping:
		return "Shopping"
	case Other:
		return "Other"
	default:
		return key
	}
}

type Expense struct {
	ID        int64     `json:"id"`
	UserID    int64     `json:"user_id"`
	Amount    Money     `json:"amount"`
	Category  string    `json:"category"`
	SpentAt   string    `json:"spent_at"`
	Comment   string    `json:"comment"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Input is a full expense replacement. Bot-scoped repositories always bind UserID on the server.
type Input struct {
	UserID   int64  `json:"user_id"`
	Amount   Money  `json:"amount"`
	Category string `json:"category"`
	SpentAt  string `json:"spent_at"`
	Comment  string `json:"comment"`
}

type Filter struct {
	UserID                    *int64
	From, To, Category, Query string
	Page, PageSize            int
}

type Page struct {
	Items    []Expense `json:"items"`
	Total    int64     `json:"total"`
	Page     int       `json:"page"`
	PageSize int       `json:"page_size"`
}

type Summary struct {
	Amount Money `json:"amount"`
	Count  int64 `json:"count"`
}

type User struct {
	ID    int64  `json:"id"`
	Label string `json:"label"`
}

type Expenses interface {
	List(context.Context, Filter) (Page, error)
	Summary(context.Context, Filter) (Summary, error)
	Get(context.Context, int64) (Expense, error)
	Last(context.Context) (Expense, error)
	Create(context.Context, Input) (Expense, error)
	Update(context.Context, int64, Input) (Expense, error)
	Delete(context.Context, int64) error
}

// Session binds every operation to one user and one transaction. Get and Last
// lock returned expenses until the turn commits, protecting bot corrections
// from overwriting a concurrent admin edit.
type Session interface {
	Expenses
	// History is per Telegram user and is saved atomically with the update and its expenses.
	History() []byte
	SaveHistory([]byte) error
}

type UpdateFunc func(context.Context, Session) (string, error)

type BotStore interface {
	// Runs the whole agent turn in one transaction, serialized per user. Duplicates return saved reply.
	ProcessUpdate(ctx context.Context, updateID, userID int64, label string, fn UpdateFunc) (reply string, duplicate bool, err error)
}

type AdminStore interface {
	Expenses
	Users(context.Context) ([]User, error)
	Ping(context.Context) error
}
