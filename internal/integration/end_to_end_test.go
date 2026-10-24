// These tests use the real PostgreSQL repository, HTTP LLM client and REST handler.
// Only the paid external model is replaced by a scripted HTTP server.
package integration_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"expensebot/internal/agent"
	"expensebot/internal/api"
	"expensebot/internal/domain"
	"expensebot/internal/store"
	"expensebot/web"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

func database(t *testing.T) (*store.Store, *sql.DB) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		if os.Getenv("TEST_REQUIRE_DATABASE") == "1" {
			t.Fatal("TEST_DATABASE_URL must be set when TEST_REQUIRE_DATABASE=1")
		}
		t.Skip("TEST_DATABASE_URL unset: real PostgreSQL integration")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	startupCtx, cancelStartup := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelStartup()
	admin := stdlib.OpenDB(*cfg)
	t.Cleanup(func() { admin.Close() })
	schema := fmt.Sprintf("expensebot_e2e_%d_%d", os.Getpid(), time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.ExecContext(startupCtx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancelCleanup()
		if _, err := admin.ExecContext(cleanupCtx, "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error(err)
		}
	})
	cfg.RuntimeParams["search_path"] = schema
	db := stdlib.OpenDB(*cfg)
	t.Cleanup(func() { db.Close() })
	if err = store.Migrate(startupCtx, db); err != nil {
		t.Fatal(err)
	}
	return store.New(db), db
}

type scriptedLLM struct {
	mu        sync.Mutex
	responses []any
	calls     int
}

func (s *scriptedLLM) enqueue(responses ...any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.responses = append(s.responses, responses...)
}
func (s *scriptedLLM) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.calls++
	var response any
	if len(s.responses) > 0 {
		response = s.responses[0]
		s.responses = s.responses[1:]
	}
	s.mu.Unlock()
	if r.URL.Path != "/v1/chat/completions" {
		http.Error(w, "wrong path", 400)
		return
	}
	if status, ok := response.(int); ok {
		http.Error(w, "scripted failure", status)
		return
	}
	if delay, ok := response.(time.Duration); ok {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(delay):
		}
		response = stop()
	}
	if response == nil {
		http.Error(w, "unexpected model request", 500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}
func tool(id, name, args string) agent.ToolCall {
	return agent.ToolCall{ID: id, Type: "function", Function: agent.FunctionCall{Name: name, Arguments: args}}
}
func calls(tc ...agent.ToolCall) any {
	return map[string]any{"choices": []any{map[string]any{"message": agent.Message{Role: "assistant", ToolCalls: tc}, "finish_reason": "tool_calls"}}}
}
func stop() any {
	return map[string]any{"choices": []any{map[string]any{"message": agent.Message{Role: "assistant", Content: "Done"}, "finish_reason": "stop"}}}
}
func createArgs(amount, comment string) string {
	return fmt.Sprintf(`{"amount":%q,"category":%q,"spent_at":"2026-10-16","comment":%q}`, amount, domain.Food, comment)
}

func TestRealAgentHTTPDatabaseAndAdminAPI(t *testing.T) {
	repo, db := database(t)
	ctx := context.Background()
	script := &scriptedLLM{}
	model := httptest.NewServer(script)
	defer model.Close()
	client := agent.NewClient(model.URL+"/v1", "test-model", "fake-not-a-secret", model.Client())
	loc, _ := time.LoadLocation("Europe/Moscow")
	engine := agent.NewEngine(repo, client, agent.Config{Location: loc, Now: func() time.Time { return time.Date(2026, 10, 16, 12, 0, 0, 0, loc) }, Timeout: 2 * time.Second})
	script.enqueue(calls(tool("a", "create_expense", createArgs("350", "coffee")), tool("b", "create_expense", createArgs("350.00", "coffee"))), stop())
	reply, dup, err := engine.Process(ctx, 9001, 111, "Anna", "coffee 350")
	if err != nil || dup || !strings.Contains(reply, "350.00") {
		t.Fatalf("create %q %v %v", reply, dup, err)
	}
	page, err := repo.List(ctx, domain.Filter{})
	if err != nil || page.Total != 1 || page.Items[0].UserID != 111 {
		t.Fatalf("scoping/dedup %+v %v", page, err)
	}
	first := page.Items[0]
	_, dup, err = engine.Process(ctx, 9001, 111, "Anna", "coffee 350")
	if err != nil || !dup {
		t.Fatal("duplicate update", dup, err)
	}
	script.mu.Lock()
	n := script.calls
	script.mu.Unlock()
	if n != 2 {
		t.Fatal("duplicate hit LLM", n)
	}
	script.enqueue(calls(tool("c", "create_expense", createArgs("0.10", "Boris's coffee"))), stop())
	if _, _, err = engine.Process(ctx, 9002, 222, "Boris", "coffee 0,10"); err != nil {
		t.Fatal(err)
	}
	page, _ = repo.List(ctx, domain.Filter{UserID: ptr(222)})
	foreign := page.Items[0]
	script.enqueue(calls(tool("d", "update_expense", `{"selector":{"id":null,"query":null,"latest":true},"amount":"450","category":null,"spent_at":null,"comment":null}`)), stop())
	if _, _, err = engine.Process(ctx, 9003, 111, "Anna", "the latest expense was 450, not 350"); err != nil {
		t.Fatal(err)
	}
	x, _ := repo.Get(ctx, first.ID)
	y, _ := repo.Get(ctx, foreign.ID)
	if x.Amount.String() != "450.00" || y.Amount.String() != "0.10" {
		t.Fatal("latest mixed users", x, y)
	}
	script.enqueue(calls(tool("e", "get_expense", fmt.Sprintf(`{"id":%d}`, foreign.ID))), stop())
	reply, _, err = engine.Process(ctx, 9004, 111, "Anna", fmt.Sprintf("show another user's expense #%d", foreign.ID))
	if err != nil || !strings.Contains(reply, "Expense not found among your expenses.") {
		t.Fatal("foreign expense exposed", reply, err)
	}
	// If the provider fails AFTER the SQL write, the entire turn must roll back.
	script.enqueue(calls(tool("f", "create_expense", createArgs("12.34", "must roll back"))), 500)
	if _, _, err = engine.Process(ctx, 9005, 111, "Anna", "coffee 12.34"); err == nil {
		t.Fatal("expected provider error")
	}
	page, _ = repo.List(ctx, domain.Filter{Query: "must roll back"})
	if page.Total != 0 {
		t.Fatal("LLM error committed mutation")
	}
	script.enqueue(calls(tool("g", "create_expense", createArgs("12.34", "retry once"))), stop())
	if _, dup, err = engine.Process(ctx, 9005, 111, "Anna", "coffee 12.34"); err != nil || dup {
		t.Fatal("retry failed", dup, err)
	}
	var processed int
	if err = db.QueryRow("SELECT COUNT(*) FROM processed_updates WHERE update_id=9005").Scan(&processed); err != nil || processed != 1 {
		t.Fatal("retry update log", processed, err)
	}
	// REST reaches the same rows and exact SQL aggregate through the real handler.
	server := httptest.NewServer(api.New(repo, web.Files))
	defer server.Close()
	resp, err := http.Get(server.URL + "/api/summary?user_id=111&from=2026-10-01&to=2026-10-31&category=" + url.QueryEscape(domain.Food))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var sum domain.Summary
	if err = json.NewDecoder(resp.Body).Decode(&sum); err != nil || resp.StatusCode != 200 || sum.Amount.String() != "462.34" || sum.Count != 2 {
		t.Fatal("REST exact aggregate", sum, resp.StatusCode, err)
	}
}

func ptr(id int64) *int64 { return &id }

func TestRealAgentTimeoutRollsBackSQL(t *testing.T) {
	repo, _ := database(t)
	script := &scriptedLLM{}
	model := httptest.NewServer(script)
	defer model.Close()
	client := agent.NewClient(model.URL+"/v1", "test", "", model.Client())
	engine := agent.NewEngine(repo, client, agent.Config{Timeout: 100 * time.Millisecond})
	script.enqueue(calls(tool("a", "create_expense", createArgs("99.99", "timeout"))), time.Second)
	if _, _, err := engine.Process(context.Background(), 7001, 333, "Kate", "coffee 99.99"); err == nil {
		t.Fatal("timeout should fail")
	}
	page, err := repo.List(context.Background(), domain.Filter{})
	if err != nil || page.Total != 0 {
		t.Fatal("timeout committed SQL", page, err)
	}
}

func TestRealAPICRUD(t *testing.T) {
	repo, _ := database(t)
	srv := httptest.NewServer(api.New(repo, web.Files))
	defer srv.Close()
	request := func(method, path, body string) (int, []byte) {
		t.Helper()
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, b
	}
	status, b := request("POST", "/api/expenses", fmt.Sprintf(`{"user_id":444,"amount":"0.10","category":%q,"spent_at":"2026-10-16","comment":"API"}`, domain.Food))
	if status != 201 {
		t.Fatalf("create %d %s", status, b)
	}
	var x domain.Expense
	json.Unmarshal(b, &x)
	status, b = request("PUT", fmt.Sprintf("/api/expenses/%d", x.ID), fmt.Sprintf(`{"user_id":555,"amount":"0.20","category":%q,"spent_at":"2026-10-15","comment":"edited"}`, domain.Transport))
	if status != 200 {
		t.Fatalf("update %d %s", status, b)
	}
	status, b = request("GET", "/api/expenses?user_id=555&from=2026-10-15&to=2026-10-15&category="+url.QueryEscape(domain.Transport), "")
	var page domain.Page
	json.Unmarshal(b, &page)
	if status != 200 || page.Total != 1 || page.Items[0].Amount.String() != "0.20" {
		t.Fatalf("filters %d %s", status, b)
	}
	status, b = request("DELETE", fmt.Sprintf("/api/expenses/%d", x.ID), "")
	if status != 204 {
		t.Fatalf("delete %d %s", status, b)
	}
	status, b = request("GET", fmt.Sprintf("/api/expenses/%d", x.ID), "")
	if status != 404 {
		t.Fatalf("deleted get %d %s", status, b)
	}
}
