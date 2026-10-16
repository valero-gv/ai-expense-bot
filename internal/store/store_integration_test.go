package store_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"reflect"
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

// Every test creates its own schema and drops only that schema. The configured
// database can therefore be reused safely; no tables in public are touched.
func testStore(t *testing.T) (*store.Store, *sql.DB) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is unset: PostgreSQL integration test")
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	admin := stdlib.OpenDB(*config)
	t.Cleanup(func() { admin.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err = admin.PingContext(ctx); err != nil {
		t.Fatalf("connect TEST_DATABASE_URL: %v", err)
	}
	schema := fmt.Sprintf("expensebot_test_%d_%d", os.Getpid(), time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.ExecContext(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := admin.ExecContext(ctx, "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Errorf("drop test schema: %v", err)
		}
	})
	config.RuntimeParams["search_path"] = schema
	db := stdlib.OpenDB(*config)
	db.SetMaxOpenConns(12)
	t.Cleanup(func() { db.Close() })
	if err = store.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	return store.New(db), db
}

func input(userID int64, cents domain.Money, category, date, comment string) domain.Input {
	return domain.Input{UserID: userID, Amount: cents, Category: category, SpentAt: date, Comment: comment}
}

func create(t *testing.T, s *store.Store, in domain.Input) domain.Expense {
	t.Helper()
	e, err := s.Create(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestMigrationRepeatedAndExactSchema(t *testing.T) {
	_, db := testStore(t)
	ctx := context.Background()
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("second migration: %v", err)
	}
	var count int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations").Scan(&count); err != nil || count != 1 {
		t.Fatalf("migration tracking count=%d err=%v", count, err)
	}
	rows, err := db.QueryContext(ctx, `SELECT column_name FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'expenses' ORDER BY ordinal_position`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := []string{"id", "user_id", "amount", "category", "spent_at", "comment", "created_at", "updated_at"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("expense columns=%v, want %v", names, want)
	}
	var numericType string
	var precision, scale int
	if err := db.QueryRowContext(ctx, `SELECT data_type, numeric_precision, numeric_scale FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'expenses' AND column_name = 'amount'`).Scan(&numericType, &precision, &scale); err != nil {
		t.Fatal(err)
	}
	if numericType != "numeric" || precision != 11 || scale != 2 {
		t.Fatalf("money SQL type=%s(%d,%d)", numericType, precision, scale)
	}
	if _, err := db.ExecContext(ctx, "UPDATE schema_migrations SET checksum = 'changed'"); err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(ctx, db); err == nil || !strings.Contains(err.Error(), "has changed") {
		t.Fatalf("modified migration should be rejected: %v", err)
	}
}

func TestBotScopeCoversEveryOperationAndAdminCanManageAllUsers(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	first := create(t, s, input(111, 35000, "еда", "2026-10-16", "кофе"))
	foreign := create(t, s, input(222, 120000, "транспорт", "2026-10-16", "такси"))
	otherID := int64(222)
	_, _, err := s.ProcessUpdate(ctx, 10, 111, "Анна", func(ctx context.Context, turn domain.Session) (string, error) {
		if _, err := turn.Get(ctx, foreign.ID); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("foreign Get: %v", err)
		}
		if _, err := turn.Update(ctx, foreign.ID, input(222, 1, "другое", "2026-10-16", "чужая")); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("foreign Update: %v", err)
		}
		if err := turn.Delete(ctx, foreign.ID); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("foreign Delete: %v", err)
		}
		page, err := turn.List(ctx, domain.Filter{UserID: &otherID})
		if err != nil {
			return "", err
		}
		if page.Total != 1 || len(page.Items) != 1 || page.Items[0].ID != first.ID {
			t.Errorf("scoped List leaked or lost rows: %+v", page)
		}
		summary, err := turn.Summary(ctx, domain.Filter{UserID: &otherID})
		if err != nil {
			return "", err
		}
		if summary.Count != 1 || summary.Amount != 35000 {
			t.Errorf("scoped Summary=%+v", summary)
		}
		last, err := turn.Last(ctx)
		if err != nil {
			return "", err
		}
		if last.ID != first.ID {
			t.Errorf("scoped Last=%d, want %d", last.ID, first.ID)
		}
		created, err := turn.Create(ctx, input(222, 45000, "еда", "2026-10-15", "server-scoped"))
		if err != nil {
			return "", err
		}
		if created.UserID != 111 {
			t.Errorf("create allowed owner %d", created.UserID)
		}
		updated, err := turn.Update(ctx, first.ID, input(222, 45000, "еда", "2026-10-16", "исправлено"))
		if err != nil {
			return "", err
		}
		if updated.UserID != 111 {
			t.Errorf("update allowed owner %d", updated.UserID)
		}
		return "готово", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.Get(ctx, foreign.ID); err != nil || got.Amount != foreign.Amount || got.UserID != 222 {
		t.Fatalf("foreign expense changed: %+v %v", got, err)
	}
	page, err := s.List(ctx, domain.Filter{})
	if err != nil || page.Total != 3 {
		t.Fatalf("admin List=%+v err=%v", page, err)
	}
	all, err := s.Summary(ctx, domain.Filter{})
	if err != nil || all.Count != 3 || all.Amount != 210000 {
		t.Fatalf("admin Summary=%+v err=%v", all, err)
	}
	reassigned, err := s.Update(ctx, foreign.ID, input(333, 10000, "другое", "2026-10-01", "admin reassigned"))
	if err != nil || reassigned.UserID != 333 {
		t.Fatalf("admin Update=%+v err=%v", reassigned, err)
	}
	if err = s.Delete(ctx, foreign.ID); err != nil {
		t.Fatalf("admin Delete: %v", err)
	}
	if _, err = s.Get(ctx, foreign.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("deleted Get: %v", err)
	}
	users, err := s.Users(ctx)
	if err != nil {
		t.Fatal(err)
	}
	labels := map[int64]string{}
	for _, user := range users {
		labels[user.ID] = user.Label
	}
	if labels[111] != "Анна" || labels[222] == "" || labels[333] == "" {
		t.Fatalf("user labels=%v", labels)
	}
	_, _, err = s.ProcessUpdate(ctx, 11, 111, "", func(ctx context.Context, turn domain.Session) (string, error) { return "ok", nil })
	if err != nil {
		t.Fatal(err)
	}
	users, err = s.Users(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, user := range users {
		if user.ID == 111 && user.Label != "Анна" {
			t.Fatalf("empty label replaced name: %+v", user)
		}
	}
}

func TestUpdateIdempotencyHistoryIsolationAndCompleteRollback(t *testing.T) {
	s, db := testStore(t)
	ctx := context.Background()
	var first domain.Expense
	_, duplicate, err := s.ProcessUpdate(ctx, 1, 101, "Первый", func(ctx context.Context, turn domain.Session) (string, error) {
		var err error
		first, err = turn.Create(ctx, input(999, 10, "еда", "2026-10-16", "original"))
		if err != nil {
			return "", err
		}
		if err := turn.SaveHistory([]byte("original history")); err != nil {
			return "", err
		}
		return "saved answer", nil
	})
	if err != nil || duplicate {
		t.Fatalf("first update duplicate=%v err=%v", duplicate, err)
	}
	answer, duplicate, err := s.ProcessUpdate(ctx, 1, 101, "", func(context.Context, domain.Session) (string, error) {
		t.Error("duplicate callback was called")
		return "wrong", nil
	})
	if err != nil || !duplicate || answer != "saved answer" {
		t.Fatalf("duplicate answer=%q duplicate=%v err=%v", answer, duplicate, err)
	}
	_, duplicate, err = s.ProcessUpdate(ctx, 1, 202, "Второй", func(ctx context.Context, turn domain.Session) (string, error) {
		if len(turn.History()) != 0 {
			t.Errorf("history leaked: %q", turn.History())
		}
		return "second user's answer", turn.SaveHistory([]byte("second history"))
	})
	if err != nil || duplicate {
		t.Fatalf("user/update pair confused: duplicate=%v err=%v", duplicate, err)
	}
	failure := errors.New("LLM failed after expense tools")
	_, _, err = s.ProcessUpdate(ctx, 2, 101, "Changed label", func(ctx context.Context, turn domain.Session) (string, error) {
		if string(turn.History()) != "original history" {
			t.Errorf("history=%q", turn.History())
		}
		copy := turn.History()
		copy[0] = 'X'
		if string(turn.History()) != "original history" {
			t.Error("History exposed mutable backing array")
		}
		if _, err := turn.Create(ctx, input(101, 20, "еда", "2026-10-16", "must roll back")); err != nil {
			return "", err
		}
		if _, err := turn.Update(ctx, first.ID, input(101, 99, "другое", "2026-10-15", "must roll back")); err != nil {
			return "", err
		}
		if err := turn.Delete(ctx, first.ID); err != nil {
			return "", err
		}
		if err := turn.SaveHistory([]byte("must roll back")); err != nil {
			return "", err
		}
		return "", failure
	})
	if !errors.Is(err, failure) {
		t.Fatalf("callback error lost: %v", err)
	}
	got, err := s.Get(ctx, first.ID)
	if err != nil || got.Amount != 10 || got.Comment != "original" {
		t.Fatalf("expense rollback failed: %+v %v", got, err)
	}
	page, err := s.List(ctx, domain.Filter{})
	if err != nil || page.Total != 1 {
		t.Fatalf("created row survived rollback: %+v %v", page, err)
	}
	var processed int
	if err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM processed_updates WHERE user_id = 101 AND update_id = 2").Scan(&processed); err != nil || processed != 0 {
		t.Fatalf("failed update persisted: count=%d err=%v", processed, err)
	}
	answer, duplicate, err = s.ProcessUpdate(ctx, 2, 101, "", func(ctx context.Context, turn domain.Session) (string, error) {
		if string(turn.History()) != "original history" {
			t.Errorf("history survived rollback: %q", turn.History())
		}
		_, err := turn.Create(ctx, input(101, 20, "еда", "2026-10-16", "retry once"))
		if err != nil {
			return "", err
		}
		return "retry succeeded", turn.SaveHistory([]byte("retry history"))
	})
	if err != nil || duplicate || answer != "retry succeeded" {
		t.Fatalf("retry: %q %v %v", answer, duplicate, err)
	}
	_, _, err = s.ProcessUpdate(ctx, 2, 202, "", func(ctx context.Context, turn domain.Session) (string, error) {
		if string(turn.History()) != "second history" {
			t.Errorf("second history changed: %q", turn.History())
		}
		return "ok", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	users, err := s.Users(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, user := range users {
		if user.ID == 101 && user.Label != "Первый" {
			t.Fatalf("user label survived rollback: %+v", user)
		}
	}
}

func TestFiltersPaginationStableLastAndExactAggregates(t *testing.T) {
	s, db := testStore(t)
	ctx := context.Background()
	first := create(t, s, input(11, 10, "еда", "2026-10-01", `Кофе 100%_ок\чек`))
	second := create(t, s, input(11, 20, "еда", "2026-10-01", `Кофе 100ABCокXчек`))
	third := create(t, s, input(22, 12345, "транспорт", "2026-10-03", "такси"))
	fourth := create(t, s, input(11, 99999999999, "покупки", "2026-09-01", "большая покупка"))
	fifth := create(t, s, input(11, 99999999999, "покупки", "2026-09-01", "ещё покупка"))
	owner := int64(11)
	filter := domain.Filter{UserID: &owner, From: "2026-10-01", To: "2026-10-01", Category: "еда"}
	page, err := s.List(ctx, filter)
	if err != nil || page.Total != 2 || len(page.Items) != 2 || page.Items[0].ID != second.ID || page.Items[1].ID != first.ID {
		t.Fatalf("inclusive filters/sorting: %+v %v", page, err)
	}
	summary, err := s.Summary(ctx, filter)
	if err != nil || summary.Amount != 30 || summary.Count != 2 {
		t.Fatalf("0.10+0.20 not exact: %+v %v", summary, err)
	}
	filter.Query = `100%_ок\чек`
	page, err = s.List(ctx, filter)
	if err != nil || page.Total != 1 || page.Items[0].ID != first.ID {
		t.Fatalf("LIKE wildcards/backslash treated as pattern: %+v %v", page, err)
	}
	filter.Query = "100"
	page, err = s.List(ctx, filter)
	if err != nil || page.Total != 2 {
		t.Fatalf("comment substring: %+v %v", page, err)
	}
	page, err = s.List(ctx, domain.Filter{Page: 2, PageSize: 2})
	if err != nil || page.Total != 5 || len(page.Items) != 2 || page.Items[0].ID != first.ID || page.Items[1].ID != fifth.ID {
		t.Fatalf("pagination: %+v %v", page, err)
	}
	page, err = s.List(ctx, domain.Filter{Page: 99, PageSize: 2})
	if err != nil || page.Total != 5 || len(page.Items) != 0 || page.Items == nil {
		t.Fatalf("empty page should retain total/non-nil array: %+v %v", page, err)
	}
	summary, err = s.Summary(ctx, domain.Filter{UserID: &owner, Category: "покупки"})
	if err != nil || summary.Amount != 199999999998 || summary.Count != 2 {
		t.Fatalf("aggregate larger than per-expense bound: %+v %v", summary, err)
	}
	if _, err = db.ExecContext(ctx, "UPDATE expenses SET created_at = '2026-01-01T00:00:00Z'"); err != nil {
		t.Fatal(err)
	}
	last, err := s.Last(ctx)
	if err != nil || last.ID != fifth.ID {
		t.Fatalf("created_at tie requires id order: %+v %v", last, err)
	}
	if _, err = s.Update(ctx, first.ID, input(11, 11, "еда", "2026-10-16", "updated but not latest created")); err != nil {
		t.Fatal(err)
	}
	last, err = s.Last(ctx)
	if err != nil || last.ID != fifth.ID {
		t.Fatalf("last wrongly uses update/spending time: %+v %v", last, err)
	}
	if third.ID == fourth.ID {
		t.Fatal("expense IDs are not unique")
	}
	for _, invalid := range []domain.Filter{{From: "2026-02-30"}, {From: "2026-10-02", To: "2026-10-01"}, {Category: "invalid"}, {Page: -1}} {
		if _, err := s.List(ctx, invalid); !errors.Is(err, domain.ErrInvalid) {
			t.Errorf("invalid filter accepted %+v: %v", invalid, err)
		}
	}
}

func TestConcurrentUpdatesSerializePerUserAndDeduplicate(t *testing.T) {
	s, _ := testStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		_, _, err := s.ProcessUpdate(ctx, 1, 1001, "one", func(ctx context.Context, turn domain.Session) (string, error) {
			close(firstEntered)
			select {
			case <-releaseFirst:
			case <-ctx.Done():
				return "", ctx.Err()
			}
			return "first", turn.SaveHistory([]byte("first finished"))
		})
		firstDone <- err
	}()
	select {
	case <-firstEntered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	secondEntered := make(chan struct{})
	secondDone := make(chan error, 1)
	go func() {
		_, _, err := s.ProcessUpdate(ctx, 2, 1001, "one", func(ctx context.Context, turn domain.Session) (string, error) {
			close(secondEntered)
			if string(turn.History()) != "first finished" {
				return "", fmt.Errorf("stale conversation: %q", turn.History())
			}
			return "second", nil
		})
		secondDone <- err
	}()
	// Another user can finish while user 1001's callback (including the LLM) is open.
	_, _, err := s.ProcessUpdate(ctx, 1, 1002, "two", func(context.Context, domain.Session) (string, error) { return "independent", nil })
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-secondEntered:
		t.Fatal("same user's second callback entered before first transaction committed")
	case <-time.After(100 * time.Millisecond):
	}
	close(releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if err := <-secondDone; err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int64
	var duplicates atomic.Int64
	errorsCh := make(chan error, 8)
	var workers sync.WaitGroup
	for i := 0; i < 8; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			reply, duplicate, err := s.ProcessUpdate(ctx, 55, 1003, "three", func(ctx context.Context, turn domain.Session) (string, error) {
				calls.Add(1)
				_, err := turn.Create(ctx, input(1003, 10, "еда", "2026-10-16", "exactly once"))
				return "same answer", err
			})
			if duplicate {
				duplicates.Add(1)
			}
			if err == nil && reply != "same answer" {
				err = fmt.Errorf("reply=%q", reply)
			}
			errorsCh <- err
		}()
	}
	workers.Wait()
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Error(err)
		}
	}
	if calls.Load() != 1 || duplicates.Load() != 7 {
		t.Fatalf("callbacks=%d duplicates=%d", calls.Load(), duplicates.Load())
	}
	owner := int64(1003)
	page, err := s.List(ctx, domain.Filter{UserID: &owner})
	if err != nil || page.Total != 1 {
		t.Fatalf("concurrent duplicate expense: %+v %v", page, err)
	}
}
