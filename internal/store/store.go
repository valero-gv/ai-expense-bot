package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"expensebot/internal/domain"
)

type queryer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// repository carries an immutable scope. Bot sessions bind every query to this
// scope, even when the agent provides another owner in an input or filter.
type repository struct {
	db     queryer
	userID *int64
}

type Store struct {
	*repository
	db *sql.DB
}

var _ domain.AdminStore = (*Store)(nil)
var _ domain.BotStore = (*Store)(nil)
var _ domain.Session = (*session)(nil)

func New(db *sql.DB) *Store {
	return &Store{repository: &repository{db: db}, db: db}
}

func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

func (s *Store) Users(ctx context.Context) ([]domain.User, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id, label FROM users ORDER BY lower(label), id")
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()
	users := make([]domain.User, 0)
	for rows.Next() {
		var user domain.User
		if err = rows.Scan(&user.ID, &user.Label); err != nil {
			return nil, fmt.Errorf("scan user: %w", err)
		}
		users = append(users, user)
	}
	return users, rows.Err()
}

const columns = "id, user_id, amount, category, to_char(spent_at, 'YYYY-MM-DD'), comment, created_at, updated_at"

type scanner interface{ Scan(...any) error }

func scanExpense(row scanner) (domain.Expense, error) {
	var expense domain.Expense
	err := row.Scan(&expense.ID, &expense.UserID, &expense.Amount, &expense.Category, &expense.SpentAt, &expense.Comment, &expense.CreatedAt, &expense.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Expense{}, domain.ErrNotFound
	}
	return expense, err
}

func (r *repository) where(filter domain.Filter) (string, []any, error) {
	if r.userID != nil {
		filter.UserID = r.userID
	}
	if err := domain.ValidateFilter(filter); err != nil {
		return "", nil, err
	}
	clauses := make([]string, 0, 5)
	args := make([]any, 0, 5)
	add := func(clause string, value any) {
		args = append(args, value)
		clauses = append(clauses, fmt.Sprintf(clause, len(args)))
	}
	if filter.UserID != nil {
		add("user_id = $%d", *filter.UserID)
	}
	if filter.From != "" {
		add("spent_at >= $%d::date", filter.From)
	}
	if filter.To != "" {
		add("spent_at <= $%d::date", filter.To)
	}
	if filter.Category != "" {
		add("category = $%d", filter.Category)
	}
	if filter.Query != "" {
		add(`comment ILIKE $%d ESCAPE E'\\'`, "%"+escapeLike(filter.Query)+"%")
	}
	if len(clauses) == 0 {
		return "", args, nil
	}
	return " WHERE " + strings.Join(clauses, " AND "), args, nil
}

func escapeLike(text string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(text)
}

func (r *repository) List(ctx context.Context, filter domain.Filter) (domain.Page, error) {
	where, args, err := r.where(filter)
	if err != nil {
		return domain.Page{}, err
	}
	if filter.Page == 0 {
		filter.Page = 1
	}
	if filter.PageSize == 0 {
		filter.PageSize = 20
	}
	page := domain.Page{Items: make([]domain.Expense, 0), Page: filter.Page, PageSize: filter.PageSize}
	args = append(args, filter.PageSize, int64(filter.Page-1)*int64(filter.PageSize))
	// Count and rows share one PostgreSQL statement snapshot. The outer join
	// retains the total even when the requested page contains no expenses.
	// A zero ID marks that empty-page row; persisted expense IDs are positive.
	query := fmt.Sprintf(`WITH filtered AS (SELECT * FROM expenses%s),
		paged AS (SELECT * FROM filtered ORDER BY spent_at DESC, id DESC LIMIT $%d OFFSET $%d)
		SELECT totals.total, COALESCE(p.id, 0), COALESCE(p.user_id, 0),
			COALESCE(p.amount, 0), COALESCE(p.category, ''),
			COALESCE(to_char(p.spent_at, 'YYYY-MM-DD'), ''), COALESCE(p.comment, ''),
			COALESCE(p.created_at, 'epoch'::timestamptz), COALESCE(p.updated_at, 'epoch'::timestamptz)
		FROM (SELECT COUNT(*) AS total FROM filtered) totals
		LEFT JOIN paged p ON true
		ORDER BY p.spent_at DESC, p.id DESC`, where, len(args)-1, len(args))
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return domain.Page{}, fmt.Errorf("list expenses: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var expense domain.Expense
		scanErr := rows.Scan(&page.Total, &expense.ID, &expense.UserID, &expense.Amount, &expense.Category, &expense.SpentAt, &expense.Comment, &expense.CreatedAt, &expense.UpdatedAt)
		if scanErr != nil {
			return domain.Page{}, fmt.Errorf("scan expense: %w", scanErr)
		}
		if expense.ID != 0 {
			page.Items = append(page.Items, expense)
		}
	}
	if err = rows.Err(); err != nil {
		return domain.Page{}, fmt.Errorf("read expenses: %w", err)
	}
	return page, nil
}

func (r *repository) Summary(ctx context.Context, filter domain.Filter) (domain.Summary, error) {
	where, args, err := r.where(filter)
	if err != nil {
		return domain.Summary{}, err
	}
	var summary domain.Summary
	err = r.db.QueryRowContext(ctx, "SELECT COALESCE(SUM(amount), 0), COUNT(*) FROM expenses"+where, args...).Scan(&summary.Amount, &summary.Count)
	if err != nil {
		return domain.Summary{}, fmt.Errorf("summarize expenses: %w", err)
	}
	return summary, nil
}

func (r *repository) Get(ctx context.Context, id int64) (domain.Expense, error) {
	if id <= 0 {
		return domain.Expense{}, domain.ErrNotFound
	}
	query, args := r.idQuery("SELECT "+columns+" FROM expenses", id)
	if r.userID != nil {
		query += " FOR UPDATE"
	}
	expense, err := scanExpense(r.db.QueryRowContext(ctx, query, args...))
	if err != nil {
		return domain.Expense{}, fmt.Errorf("get expense: %w", err)
	}
	return expense, nil
}

func (r *repository) Last(ctx context.Context) (domain.Expense, error) {
	query := "SELECT " + columns + " FROM expenses"
	var args []any
	if r.userID != nil {
		query += " WHERE user_id = $1"
		args = append(args, *r.userID)
	}
	query += " ORDER BY created_at DESC, id DESC LIMIT 1"
	if r.userID != nil {
		query += " FOR UPDATE"
	}
	expense, err := scanExpense(r.db.QueryRowContext(ctx, query, args...))
	if err != nil {
		return domain.Expense{}, fmt.Errorf("last expense: %w", err)
	}
	return expense, nil
}

func (r *repository) idQuery(query string, id int64) (string, []any) {
	args := []any{id}
	query += " WHERE id = $1"
	if r.userID != nil {
		query += " AND user_id = $2"
		args = append(args, *r.userID)
	}
	return query, args
}

func (r *repository) bindInput(input domain.Input) (domain.Input, error) {
	if r.userID != nil {
		input.UserID = *r.userID
	}
	return input, domain.ValidateInput(input)
}

func (r *repository) ensureOwner(ctx context.Context, userID int64) error {
	_, err := r.db.ExecContext(ctx, "INSERT INTO users(id, label) VALUES($1, $2) ON CONFLICT(id) DO NOTHING", userID, "Telegram "+strconv.FormatInt(userID, 10))
	return err
}

// Create and Update on the admin store include new-owner creation atomically.
func (s *Store) Create(ctx context.Context, input domain.Input) (domain.Expense, error) {
	if err := domain.ValidateInput(input); err != nil {
		return domain.Expense{}, err
	}
	return s.write(ctx, func(r *repository) (domain.Expense, error) { return r.Create(ctx, input) })
}

func (s *Store) Update(ctx context.Context, id int64, input domain.Input) (domain.Expense, error) {
	if err := domain.ValidateInput(input); err != nil {
		return domain.Expense{}, err
	}
	return s.write(ctx, func(r *repository) (domain.Expense, error) { return r.Update(ctx, id, input) })
}

func (s *Store) write(ctx context.Context, fn func(*repository) (domain.Expense, error)) (domain.Expense, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Expense{}, fmt.Errorf("begin expense write: %w", err)
	}
	defer tx.Rollback()
	expense, err := fn(&repository{db: tx})
	if err != nil {
		return domain.Expense{}, err
	}
	if err = tx.Commit(); err != nil {
		return domain.Expense{}, fmt.Errorf("commit expense write: %w", err)
	}
	return expense, nil
}

func (r *repository) Create(ctx context.Context, input domain.Input) (domain.Expense, error) {
	input, err := r.bindInput(input)
	if err != nil {
		return domain.Expense{}, err
	}
	if err = r.ensureOwner(ctx, input.UserID); err != nil {
		return domain.Expense{}, fmt.Errorf("create expense owner: %w", err)
	}
	expense, err := scanExpense(r.db.QueryRowContext(ctx,
		"INSERT INTO expenses(user_id, amount, category, spent_at, comment) VALUES($1, $2, $3, $4::date, $5) RETURNING "+columns,
		input.UserID, input.Amount, input.Category, input.SpentAt, input.Comment))
	if err != nil {
		return domain.Expense{}, fmt.Errorf("create expense: %w", err)
	}
	return expense, nil
}

func (r *repository) Update(ctx context.Context, id int64, input domain.Input) (domain.Expense, error) {
	if id <= 0 {
		return domain.Expense{}, domain.ErrNotFound
	}
	input, err := r.bindInput(input)
	if err != nil {
		return domain.Expense{}, err
	}
	if err = r.ensureOwner(ctx, input.UserID); err != nil {
		return domain.Expense{}, fmt.Errorf("update expense owner: %w", err)
	}
	args := []any{input.UserID, input.Amount, input.Category, input.SpentAt, input.Comment, id}
	query := "UPDATE expenses SET user_id = $1, amount = $2, category = $3, spent_at = $4::date, comment = $5, updated_at = clock_timestamp() WHERE id = $6"
	if r.userID != nil {
		query += " AND user_id = $7"
		args = append(args, *r.userID)
	}
	expense, err := scanExpense(r.db.QueryRowContext(ctx, query+" RETURNING "+columns, args...))
	if err != nil {
		return domain.Expense{}, fmt.Errorf("update expense: %w", err)
	}
	return expense, nil
}

func (r *repository) Delete(ctx context.Context, id int64) error {
	if id <= 0 {
		return domain.ErrNotFound
	}
	query, args := r.idQuery("DELETE FROM expenses", id)
	result, err := r.db.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("delete expense: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete expense result: %w", err)
	}
	if count == 0 {
		return domain.ErrNotFound
	}
	return nil
}

type session struct {
	*repository
	history []byte
	dirty   bool
}

func (s *session) History() []byte { return append([]byte(nil), s.history...) }
func (s *session) SaveHistory(history []byte) error {
	s.history = append([]byte{}, history...)
	s.dirty = true
	return nil
}

func (s *Store) ProcessUpdate(ctx context.Context, updateID, userID int64, label string, callback domain.UpdateFunc) (string, bool, error) {
	if err := domain.ValidateFilter(domain.Filter{UserID: &userID}); err != nil {
		return "", false, err
	}
	if updateID < 0 || callback == nil {
		return "", false, fmt.Errorf("%w: invalid Telegram update", domain.ErrInvalid)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", false, fmt.Errorf("begin Telegram update: %w", err)
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1)", userID); err != nil {
		return "", false, fmt.Errorf("lock Telegram user: %w", err)
	}
	var reply string
	err = tx.QueryRowContext(ctx, "SELECT reply FROM processed_updates WHERE user_id = $1 AND update_id = $2", userID, updateID).Scan(&reply)
	if err == nil {
		if err = tx.Commit(); err != nil {
			return "", false, fmt.Errorf("commit duplicate update: %w", err)
		}
		return reply, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", false, fmt.Errorf("find processed update: %w", err)
	}
	if label == "" {
		_, err = tx.ExecContext(ctx, "INSERT INTO users(id, label) VALUES($1, $2) ON CONFLICT(id) DO NOTHING", userID, "Telegram "+strconv.FormatInt(userID, 10))
	} else {
		_, err = tx.ExecContext(ctx, "INSERT INTO users(id, label) VALUES($1, $2) ON CONFLICT(id) DO UPDATE SET label = EXCLUDED.label", userID, label)
	}
	if err != nil {
		return "", false, fmt.Errorf("save Telegram user: %w", err)
	}
	turn := &session{repository: &repository{db: tx, userID: &userID}}
	err = tx.QueryRowContext(ctx, "SELECT payload FROM conversation_history WHERE user_id = $1", userID).Scan(&turn.history)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", false, fmt.Errorf("read conversation: %w", err)
	}
	reply, err = callback(ctx, turn)
	if err != nil {
		return "", false, fmt.Errorf("process Telegram update: %w", err)
	}
	if turn.dirty {
		if _, err = tx.ExecContext(ctx, "INSERT INTO conversation_history(user_id, payload) VALUES($1, $2) ON CONFLICT(user_id) DO UPDATE SET payload = EXCLUDED.payload", userID, turn.history); err != nil {
			return "", false, fmt.Errorf("save conversation: %w", err)
		}
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO processed_updates(user_id, update_id, reply) VALUES($1, $2, $3)", userID, updateID, reply); err != nil {
		return "", false, fmt.Errorf("save processed update: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return "", false, fmt.Errorf("commit Telegram update: %w", err)
	}
	return reply, false, nil
}
