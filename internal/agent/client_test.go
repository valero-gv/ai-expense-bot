package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClientOpenAIWireContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer local-test-key" {
			t.Errorf("bad request method/path/auth")
		}
		var payload struct {
			Model    string    `json:"model"`
			Messages []Message `json:"messages"`
			Tools    []Tool    `json:"tools"`
			Parallel bool      `json:"parallel_tool_calls"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if payload.Model != "test-model" || payload.Parallel || len(payload.Tools) != 7 || len(payload.Messages) != 2 {
			t.Errorf("bad request payload %+v", payload)
		}
		for _, tool := range payload.Tools {
			if !tool.Function.Strict || tool.Function.Parameters["additionalProperties"] != false {
				t.Error("non-strict tool")
			}
			props := tool.Function.Parameters["properties"].(map[string]any)
			if _, ok := props["user_id"]; ok {
				t.Error("owner ID exposed in tools")
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":"call-1","type":"function","function":{"name":"latest_expense","arguments":"{\"count\":1}"}}]}}]}`)
	}))
	defer server.Close()
	c := NewClient(server.URL+"/v1", "test-model", "local-test-key", server.Client())
	m, err := c.Complete(context.Background(), []Message{{Role: "system", Content: "s"}, {Role: "user", Content: "u"}}, toolDefinitions())
	if err != nil || len(m.ToolCalls) != 1 || m.ToolCalls[0].ID != "call-1" {
		t.Fatal(m, err)
	}
}

func TestClientErrorsAndCancellation(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"provider rejected", `{"error":{"message":"private provider detail"}}`, 429},
		{"invalid JSON", `not JSON`, 200},
		{"no choices", `{"choices":[]}`, 200},
		{"multiple choices", `{"choices":[{},{}]}`, 200},
		{"truncated completion", `{"choices":[{"finish_reason":"length","message":{"role":"assistant","content":"partial"}}]}`, 200},
		{"wrong role", `{"choices":[{"finish_reason":"stop","message":{"role":"system","content":"wrong"}}]}`, 200},
		{"oversized body", strings.Repeat("x", (1<<20)+1), 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer s.Close()
			c := NewClient(s.URL, "model", "key", s.Client())
			_, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "u"}}, toolDefinitions())
			if err == nil || strings.Contains(err.Error(), "private provider detail") {
				t.Fatal(err)
			}
		})
	}
	t.Run("deadline", func(t *testing.T) {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(75 * time.Millisecond)
			_, _ = io.WriteString(w, `{}`)
		}))
		defer s.Close()
		c := NewClient(s.URL, "model", "key", s.Client())
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		defer cancel()
		_, err := c.Complete(ctx, []Message{{Role: "user", Content: "u"}}, toolDefinitions())
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		}
	})
	t.Run("invalid config", func(t *testing.T) {
		for _, base := range []string{"ftp://localhost/v1", "http://user:secret@localhost/v1", "://bad"} {
			c := NewClient(base, "model", "key", nil)
			_, err := c.Complete(context.Background(), nil, nil)
			if err == nil {
				t.Fatalf("accepted %s", base)
			}
		}
	})
}

func TestClientFailureAfterMutationRollsBack(t *testing.T) {
	requests := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 1 {
			message := toolMessage(call("create", "create_expense", coffee))
			response := map[string]any{"choices": []any{map[string]any{"finish_reason": "tool_calls", "message": message}}}
			_ = json.NewEncoder(w).Encode(response)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = fmt.Fprint(w, `{"error":{"message":"try again"}}`)
	}))
	defer s.Close()
	f := newStore()
	e := NewEngine(f, NewClient(s.URL, "test-model", "test-key", s.Client()), config())
	_, _, err := e.Process(context.Background(), 1, 101, "Анна", "кофе 350")
	if err == nil || requests != 2 || len(f.rows) != 0 || len(f.processed) != 0 {
		t.Fatal(err, requests, f.rows)
	}
}
