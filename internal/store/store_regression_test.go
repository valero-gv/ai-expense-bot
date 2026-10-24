package store_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"expensebot/internal/domain"
	"expensebot/internal/store"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// Run a mutation on another connection after PostgreSQL has produced a list
// query's result. With separate COUNT and SELECT statements, that mutation lands
// between the two snapshots and makes total disagree with the returned rows.
type listQueryTracer struct {
	once  sync.Once
	after func()
}

type listQueryContextKey struct{}

func (tracer *listQueryTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	isList := strings.HasPrefix(data.SQL, "SELECT COUNT(*) FROM expenses") || strings.HasPrefix(data.SQL, "WITH filtered AS")
	return context.WithValue(ctx, listQueryContextKey{}, isList)
}

func (tracer *listQueryTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	if isList, _ := ctx.Value(listQueryContextKey{}).(bool); isList && data.Err == nil {
		tracer.once.Do(tracer.after)
	}
}

func TestListCountAndRowsShareSnapshotDuringConcurrentInsert(t *testing.T) {
	s, db := testStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	first := create(t, s, input(11, 10, domain.Food, "2026-10-16", "first coffee"))
	var schema string
	if err := db.QueryRowContext(ctx, "SELECT current_schema()").Scan(&schema); err != nil {
		t.Fatal(err)
	}
	config, err := pgx.ParseConfig(os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	mutationResult := make(chan error, 1)
	config.RuntimeParams["search_path"] = schema
	config.Tracer = &listQueryTracer{after: func() {
		finished := make(chan error, 1)
		go func() {
			_, err := s.Create(ctx, input(11, 20, domain.Food, "2026-10-16", "concurrent coffee"))
			finished <- err
		}()
		mutationResult <- <-finished
	}}
	tracedDB := stdlib.OpenDB(*config)
	t.Cleanup(func() { tracedDB.Close() })
	page, err := store.New(tracedDB).List(ctx, domain.Filter{PageSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-mutationResult:
		if err != nil {
			t.Fatalf("concurrent insert: %v", err)
		}
	default:
		t.Fatal("the concurrent insert did not run during List")
	}
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].ID != first.ID {
		t.Fatalf("count and items came from different snapshots: %+v", page)
	}
	current, err := s.List(ctx, domain.Filter{})
	if err != nil || current.Total != 2 {
		t.Fatalf("concurrent insert was not committed: %+v, %v", current, err)
	}
}

func TestDuplicateOldUpdatePreservesLatestUserLabel(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	for _, update := range []struct {
		id    int64
		label string
	}{{1, "Old name"}, {2, "New name"}} {
		_, _, err := s.ProcessUpdate(ctx, update.id, 123, update.label, func(context.Context, domain.Session) (string, error) {
			return "saved reply", nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	reply, duplicate, err := s.ProcessUpdate(ctx, 1, 123, "Old name", func(context.Context, domain.Session) (string, error) {
		return "", errors.New("duplicate callback must not run")
	})
	if err != nil || !duplicate || reply != "saved reply" {
		t.Fatalf("duplicate result: reply=%q duplicate=%v err=%v", reply, duplicate, err)
	}
	users, err := s.Users(ctx)
	if err != nil || len(users) != 1 || users[0].Label != "New name" {
		t.Fatalf("duplicate restored a stale user label: %+v, %v", users, err)
	}
}

func TestCanceledUpdateRollsBackExpensesHistoryAndReceipt(t *testing.T) {
	s, _ := testStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reply, duplicate, err := s.ProcessUpdate(ctx, 1, 123, "Canceled user", func(ctx context.Context, turn domain.Session) (string, error) {
		if _, err := turn.Create(ctx, input(123, 10, domain.Food, "2026-10-16", "must roll back")); err != nil {
			return "", err
		}
		if err := turn.SaveHistory([]byte("must roll back")); err != nil {
			return "", err
		}
		cancel()
		return "must not be saved", nil
	})
	if err == nil || duplicate || reply != "" {
		t.Fatalf("canceled update returned success: reply=%q duplicate=%v err=%v", reply, duplicate, err)
	}
	ctx = context.Background()
	page, err := s.List(ctx, domain.Filter{})
	if err != nil || page.Total != 0 || len(page.Items) != 0 {
		t.Fatalf("canceled expense survived: %+v, %v", page, err)
	}
	users, err := s.Users(ctx)
	if err != nil || len(users) != 0 {
		t.Fatalf("canceled owner survived: %+v, %v", users, err)
	}
	reply, duplicate, err = s.ProcessUpdate(ctx, 1, 123, "Retry user", func(ctx context.Context, turn domain.Session) (string, error) {
		if len(turn.History()) != 0 {
			t.Errorf("canceled history survived: %q", turn.History())
		}
		if _, err := turn.Create(ctx, input(123, 20, domain.Food, "2026-10-16", "successful retry")); err != nil {
			return "", err
		}
		return "retry saved", nil
	})
	if err != nil || duplicate || reply != "retry saved" {
		t.Fatalf("canceled receipt prevented retry: reply=%q duplicate=%v err=%v", reply, duplicate, err)
	}
}

func TestWaitingForUserLockHonorsCancellation(t *testing.T) {
	s, _ := testStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, _, err := s.ProcessUpdate(ctx, 1, 123, "User", func(ctx context.Context, turn domain.Session) (string, error) {
			close(entered)
			select {
			case <-release:
				return "first saved", turn.SaveHistory([]byte("first history"))
			case <-ctx.Done():
				return "", ctx.Err()
			}
		})
		done <- err
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	waitCtx, waitCancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer waitCancel()
	var called atomic.Bool
	_, _, err := s.ProcessUpdate(waitCtx, 2, 123, "User", func(context.Context, domain.Session) (string, error) {
		called.Store(true)
		return "must not run", nil
	})
	if err == nil || called.Load() || waitCtx.Err() == nil {
		t.Fatalf("lock wait did not honor cancellation: callback=%v err=%v context=%v", called.Load(), err, waitCtx.Err())
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	_, duplicate, err := s.ProcessUpdate(ctx, 2, 123, "User", func(ctx context.Context, turn domain.Session) (string, error) {
		if string(turn.History()) != "first history" {
			t.Errorf("retry did not see the committed history: %q", turn.History())
		}
		return "retry saved", nil
	})
	if err != nil || duplicate {
		t.Fatalf("canceled lock wait could not retry: duplicate=%v err=%v", duplicate, err)
	}
}

func TestAllStoredCategoryKeysRemainAcceptedByMigration(t *testing.T) {
	s, _ := testStore(t)
	for _, key := range domain.Categories {
		created := create(t, s, input(123, 1, key, "2026-10-16", domain.CategoryLabel(key)))
		if created.Category != key {
			t.Errorf("stored category was changed: %q -> %q", key, created.Category)
		}
	}
}

func TestScopedExpenseReadLocksOutConcurrentAdminEdit(t *testing.T) {
	for _, method := range []string{"Get", "Last"} {
		t.Run(method, func(t *testing.T) {
			s, db := testStore(t)
			ctx := context.Background()
			original := create(t, s, input(123, 35000, domain.Food, "2026-10-16", "original comment"))
			_, _, err := s.ProcessUpdate(ctx, 1, 123, "User", func(ctx context.Context, turn domain.Session) (string, error) {
				var expense domain.Expense
				var err error
				if method == "Get" {
					expense, err = turn.Get(ctx, original.ID)
				} else {
					expense, err = turn.Last(ctx)
				}
				if err != nil {
					return "", err
				}
				adminCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
				defer cancel()
				_, err = db.ExecContext(adminCtx, "UPDATE expenses SET comment = 'concurrent edit' WHERE id = $1", original.ID)
				if err == nil || adminCtx.Err() == nil {
					return "", fmt.Errorf("admin edit did not wait for the scoped %s lock: %v", method, err)
				}
				_, err = turn.Update(ctx, expense.ID, input(expense.UserID, 45000, expense.Category, expense.SpentAt, expense.Comment))
				return "corrected", err
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = db.ExecContext(ctx, "UPDATE expenses SET comment = 'later admin edit' WHERE id = $1", original.ID); err != nil {
				t.Fatalf("admin edit did not resume after the turn committed: %v", err)
			}
			expense, err := s.Get(ctx, original.ID)
			if err != nil || expense.Amount != 45000 || expense.Comment != "later admin edit" {
				t.Fatalf("bot correction and subsequent admin edit did not survive: %+v, %v", expense, err)
			}
		})
	}
}
