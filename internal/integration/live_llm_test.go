package integration_test

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"expensebot/internal/agent"
	"expensebot/internal/config"
	"expensebot/internal/domain"
)

// TestLiveLLMExpensePhrases is deliberately opt-in: it makes real, potentially
// billable provider requests. database(t) creates and drops a separate schema;
// these synthetic identities never touch the application's Telegram data.
// Environment values and provider request bodies must never be logged here.
// RUN_LIVE_LLM=1 enables it; LIVE_LLM_CASE_LIMIT=1 permits a first-case smoke test.
// LIVE_LLM_TURN_PAUSE defaults to 60s, outside the Engine deadline, for pacing.
func TestLiveLLMExpensePhrases(t *testing.T) {
	if os.Getenv("RUN_LIVE_LLM") != "1" {
		t.Skip("RUN_LIVE_LLM=1 required for external LLM integration")
	}
	cfg, err := liveLLMConfig(os.Getenv)
	if err != nil {
		t.Fatal("invalid live LLM configuration:", err)
	}
	pause := 60 * time.Second
	if raw := os.Getenv("LIVE_LLM_TURN_PAUSE"); raw != "" {
		var err error
		pause, err = time.ParseDuration(raw)
		if err != nil || pause < 0 || pause > 5*time.Minute {
			t.Fatal("LIVE_LLM_TURN_PAUSE must be a duration between 0s and 5m")
		}
	}
	limit := 6
	if raw := os.Getenv("LIVE_LLM_CASE_LIMIT"); raw != "" {
		var err error
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 6 {
			t.Fatal("LIVE_LLM_CASE_LIMIT must be an integer from 1 through 6")
		}
	}

	repo, _ := database(t)
	ctx := context.Background()
	const userID int64 = 4_000_000_000_001
	const foreignID int64 = 4_000_000_000_002
	foreign, err := repo.Create(ctx, domain.Input{UserID: foreignID, Amount: 9_876_543, Category: domain.Food, SpentAt: "2026-10-16", Comment: "isolated ownership sentinel"})
	if err != nil {
		t.Fatal("create isolated ownership sentinel:", err)
	}
	loc, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		t.Fatal(err)
	}
	client := agent.NewClient(cfg.LLMBaseURL, cfg.LLMModel, cfg.LLMAPIKey, &http.Client{Timeout: cfg.LLMTimeout})
	trace := &liveTraceLLM{client: client, t: t}
	engine := agent.NewEngine(repo, trace, agent.Config{Location: loc, Now: func() time.Time {
		return time.Date(2026, 10, 16, 12, 0, 0, 0, loc)
	}, Timeout: cfg.LLMTimeout})
	var coffeeID, taxiID int64
	type liveCase struct {
		name, phrase string
		wantCount    int64
		check        func(*testing.T, domain.Page, string)
	}
	cases := []liveCase{
		{"coffee", "coffee 350", 1, func(t *testing.T, page domain.Page, reply string) {
			x := page.Items[0]
			coffeeID = x.ID
			if x.Amount != 35000 || x.Category != domain.Food || x.SpentAt != "2026-10-16" || !strings.Contains(strings.ToLower(x.Comment), "coffee") || !strings.Contains(reply, "350.00") {
				t.Fatalf("coffee facts do not match: expense=%+v reply=%q", x, reply)
			}
		}},
		{"correction", "the latest expense was 450, not 350", 1, func(t *testing.T, page domain.Page, reply string) {
			x := page.Items[0]
			if x.ID != coffeeID || x.Amount != 45000 || x.Category != domain.Food || x.SpentAt != "2026-10-16" || !strings.Contains(reply, "450.00") {
				t.Fatalf("latest correction facts do not match: expense=%+v reply=%q", x, reply)
			}
		}},
		{"taxi_yesterday", "taxi 1200 yesterday", 2, func(t *testing.T, page domain.Page, reply string) {
			for _, x := range page.Items {
				if x.ID == coffeeID {
					continue
				}
				taxiID = x.ID
				if x.Amount != 120000 || x.Category != domain.Transport || x.SpentAt != "2026-10-15" || !strings.Contains(strings.ToLower(x.Comment), "taxi") || !strings.Contains(reply, "1200.00") {
					t.Fatalf("taxi facts do not match: expense=%+v reply=%q", x, reply)
				}
			}
			if taxiID == 0 {
				t.Fatal("taxi was not created")
			}
		}},
		{"monthly_food_sum", "how much did I spend on food this month", 2, func(t *testing.T, page domain.Page, reply string) {
			if !strings.Contains(reply, "Total: 450.00 RUB. Expenses: 1.") {
				t.Fatalf("expected database-backed food total 450.00 for one owner: %q", reply)
			}
		}},
		{"week_list", "show my expenses this week", 2, func(t *testing.T, page domain.Page, reply string) {
			for _, id := range []int64{coffeeID, taxiID} {
				if !strings.Contains(reply, fmt.Sprintf("#%d:", id)) {
					t.Fatalf("week list omits owned expense #%d: %q", id, reply)
				}
			}
			if !strings.Contains(reply, "450.00") || !strings.Contains(reply, "1200.00") {
				t.Fatalf("week list has incorrect amounts: %q", reply)
			}
		}},
		{"delete_taxi", "delete taxi", 1, func(t *testing.T, page domain.Page, reply string) {
			if page.Items[0].ID != coffeeID || page.Items[0].Amount != 45000 || !strings.Contains(reply, fmt.Sprintf("Deleted #%d:", taxiID)) {
				t.Fatalf("taxi deletion facts do not match: page=%+v reply=%q", page, reply)
			}
		}},
	}
	for i, tc := range cases[:limit] {
		if i > 0 && pause > 0 {
			t.Logf("Pacing next external LLM turn for %s", pause)
			time.Sleep(pause)
		}
		if !t.Run(tc.name, func(t *testing.T) {
			trace.t = t
			t.Logf("Live phrase: %q", tc.phrase)
			reply, duplicate, err := engine.Process(ctx, 8_000_000_000+int64(i), userID, "Live isolated test", tc.phrase)
			if err != nil {
				t.Fatal("live agent turn failed:", err)
			}
			t.Logf("Verified server reply: %s", reply)
			if duplicate || reply == "" || strings.Contains(reply, foreign.Comment) || strings.Contains(reply, foreign.Amount.String()) {
				t.Fatalf("duplicate, empty reply or ownership leak: duplicate=%v reply=%q", duplicate, reply)
			}
			page, err := repo.List(ctx, domain.Filter{UserID: ptr(userID)})
			if err != nil || page.Total != tc.wantCount || int64(len(page.Items)) != tc.wantCount {
				t.Fatalf("unexpected owned expense state: count=%d want=%d err=%v", page.Total, tc.wantCount, err)
			}
			for _, x := range page.Items {
				if x.UserID != userID {
					t.Fatal("repository scope leaked another synthetic owner")
				}
			}
			tc.check(t, page, reply)
			unchanged, err := repo.Get(ctx, foreign.ID)
			if err != nil || unchanged != foreign {
				t.Fatal("another owner's isolated expense changed")
			}
		}) {
			return // Later phrases depend on the preceding verified state.
		}
	}
}

// Reuse production URL/HTTPS and timeout checks before creating a client or
// touching PostgreSQL. A missing setting after explicit opt-in is a test failure.
// The live harness never uses production DATABASE_URL or Telegram credentials.
func liveLLMConfig(get func(string) string) (config.Config, error) {
	for _, name := range []string{"TEST_DATABASE_URL", "LLM_API_KEY", "LLM_BASE_URL", "LLM_MODEL"} {
		if strings.TrimSpace(get(name)) == "" {
			return config.Config{}, fmt.Errorf("%s required for external LLM integration", name)
		}
	}
	return config.LoadWith(func(name string) string {
		switch name {
		case "DATABASE_URL":
			return get("TEST_DATABASE_URL")
		case "TELEGRAM_BOT_TOKEN":
			return ""
		default:
			return get(name)
		}
	})
}

func TestLiveLLMConfigValidation(t *testing.T) {
	base := map[string]string{"TEST_DATABASE_URL": "postgres://isolated-test", "LLM_API_KEY": "secret-value", "LLM_BASE_URL": "https://provider.example/v1", "LLM_MODEL": "test-model", "LLM_TIMEOUT": "60s"}
	for _, tc := range []struct{ name, key, value string }{
		{"missing database", "TEST_DATABASE_URL", ""},
		{"missing API key", "LLM_API_KEY", ""},
		{"missing base URL", "LLM_BASE_URL", ""},
		{"missing model", "LLM_MODEL", ""},
		{"blank model", "LLM_MODEL", "  "},
		{"insecure remote URL", "LLM_BASE_URL", "http://provider.example/v1"},
		{"credentials URL", "LLM_BASE_URL", "https://user:secret-value@provider.example/v1"},
		{"short timeout", "LLM_TIMEOUT", "0s"},
		{"long timeout", "LLM_TIMEOUT", "3m"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := liveLLMConfig(func(name string) string {
				if name == tc.key {
					return tc.value
				}
				return base[name]
			})
			if err == nil {
				t.Fatal("invalid explicit live configuration was accepted")
			}
			if strings.Contains(err.Error(), "secret-value") {
				t.Fatal("configuration error leaked a secret")
			}
		})
	}
	cfg, err := liveLLMConfig(func(name string) string {
		if name == "DATABASE_URL" || name == "TELEGRAM_BOT_TOKEN" {
			t.Fatalf("read production-only setting %s", name)
		}
		return base[name]
	})
	if err != nil || cfg.DatabaseURL != base["TEST_DATABASE_URL"] || cfg.TelegramToken != "" || cfg.LLMTimeout != 60*time.Second {
		t.Fatal("live configuration mapping failed")
	}
}

// Trace only timings, sanitized client errors and known tool names. The full
// request, arguments, messages, model responses and environment stay private.
type liveTraceLLM struct {
	client agent.LLM
	t      *testing.T
	calls  int
}

func (l *liveTraceLLM) Complete(ctx context.Context, messages []agent.Message, tools []agent.Tool) (agent.Message, error) {
	l.calls++
	call := l.calls
	l.t.Logf("Live LLM request %d started", call)
	start := time.Now()
	message, err := l.client.Complete(ctx, messages, tools)
	if err != nil {
		l.t.Logf("Live LLM request %d failed after %s: %v", call, time.Since(start).Round(time.Millisecond), err)
		return message, err
	}
	known := map[string]bool{"create_expense": true, "update_expense": true, "delete_expense": true, "list_expenses": true, "sum_expenses": true, "get_expense": true, "latest_expense": true}
	names := make([]string, 0, len(message.ToolCalls))
	for _, tool := range message.ToolCalls {
		name := tool.Function.Name
		if !known[name] {
			name = "unknown"
		}
		names = append(names, name)
	}
	l.t.Logf("Live LLM request %d completed after %s; tool names: %v", call, time.Since(start).Round(time.Millisecond), names)
	return message, nil
}
