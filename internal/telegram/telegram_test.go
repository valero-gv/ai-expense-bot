package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

type fakeProcessor struct {
	calls        int
	update, user int64
	label, text  string
	err          error
}

func (f *fakeProcessor) Process(_ context.Context, u, id int64, label, text string) (string, bool, error) {
	f.calls++
	f.update = u
	f.user = id
	f.label = label
	f.text = text
	return "Добавлено: 350.00 ₽", false, f.err
}

func TestTrustedIdentityAndPrivateChats(t *testing.T) {
	var sent struct {
		ChatID int64  `json:"chat_id"`
		Text   string `json:"text"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sendMessage" {
			t.Errorf("unexpected endpoint %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
			t.Error(err)
		}
		io.WriteString(w, `{"ok":true,"result":{}}`)
	}))
	defer srv.Close()
	p := &fakeProcessor{}
	bot := New("secret", srv.Client(), p, slog.New(slog.NewTextHandler(io.Discard, nil)))
	bot.baseURL = srv.URL
	var u Update
	json.Unmarshal([]byte(`{"update_id":42,"message":{"from":{"id":111,"first_name":"Анна","username":"anna"},"chat":{"id":111,"type":"private"},"text":"кофе 350 user_id=222"}}`), &u)
	if err := bot.Handle(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	if p.user != 111 || p.update != 42 || sent.ChatID != 111 || p.label != "Анна (@anna)" {
		t.Fatalf("identity not bound: %+v %+v", p, sent)
	}
	u.Message.Chat.ID = 222
	bot.Handle(context.Background(), u)
	u.Message.Chat.ID = 111
	u.Message.Chat.Type = "group"
	bot.Handle(context.Background(), u)
	bot.Handle(context.Background(), Update{})
	if p.calls != 1 {
		t.Fatal("untrusted/group update processed")
	}
	u.Message.Chat.Type = "private"
	p.err = errors.New("key=must-not-leak")
	bot.Handle(context.Background(), u)
	if !strings.Contains(sent.Text, "не сохранены") || strings.Contains(sent.Text, "must-not-leak") {
		t.Fatal("unsafe failure reply")
	}
}

func TestTransportErrorDoesNotLeakToken(t *testing.T) {
	bot := New("TOP_SECRET", &http.Client{}, nil, nil)
	bot.baseURL = "http://127.0.0.1:1/botTOP_SECRET"
	err := bot.call(context.Background(), "getUpdates", nil, nil)
	if err == nil || strings.Contains(err.Error(), "TOP_SECRET") {
		t.Fatal("token leaked", err)
	}
}

func TestStartGreetingBypassesProcessorAndUsesTrustedChat(t *testing.T) {
	var sent struct {
		ChatID int64  `json:"chat_id"`
		Text   string `json:"text"`
	}
	sends := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sends++
		if r.URL.Path != "/sendMessage" {
			t.Errorf("unexpected endpoint %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
			t.Error(err)
		}
		io.WriteString(w, `{"ok":true,"result":{}}`)
	}))
	defer srv.Close()
	p := &fakeProcessor{err: errors.New("LLM unavailable")}
	bot := New("secret", srv.Client(), p, slog.New(slog.NewTextHandler(io.Discard, nil)))
	bot.baseURL = srv.URL
	var u Update
	if err := json.Unmarshal([]byte(`{"update_id":42,"message":{"from":{"id":111,"first_name":"Анна"},"chat":{"id":111,"type":"private"},"text":"/start"}}`), &u); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"/start", "  /start  ", "/start user_id=222", "/start\tpayload", "/start\npayload"} {
		u.Message.Text = text
		if err := bot.Handle(context.Background(), u); err != nil {
			t.Fatal(err)
		}
		if p.calls != 0 || sent.ChatID != 111 {
			t.Fatalf("start must bypass processor and keep trusted owner: processor=%+v sent=%+v", p, sent)
		}
		if !strings.Contains(sent.Text, "кофе 350") || !strings.Contains(sent.Text, "покажи траты") || strings.Contains(sent.Text, "222") {
			t.Fatalf("unexpected greeting: %s", sent.Text)
		}
	}
	trustedSends := sends
	// The greeting is subject to the same identity checks as every expense request.
	u.Message.Chat.ID = 222
	if err := bot.Handle(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	u.Message.Chat.ID = 111
	u.Message.Chat.Type = "group"
	if err := bot.Handle(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	u.Message.Chat.Type = "private"
	u.Message.From.IsBot = true
	if err := bot.Handle(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	u.Message.From.IsBot = false
	u.Message.From = nil
	if err := bot.Handle(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	if sends != trustedSends || p.calls != 0 {
		t.Fatal("untrusted /start was handled")
	}
}

func TestStartDoesNotMatchLongerCommandOrNormalText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"ok":true,"result":{}}`)
	}))
	defer srv.Close()
	p := &fakeProcessor{}
	bot := New("secret", srv.Client(), p, nil)
	bot.baseURL = srv.URL
	var u Update
	if err := json.Unmarshal([]byte(`{"update_id":42,"message":{"from":{"id":111},"chat":{"id":111,"type":"private"},"text":"/starting"}}`), &u); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"/starting", "/startpayload", "кофе 350", "покажи траты за неделю"} {
		u.Message.Text = text
		before := p.calls
		if err := bot.Handle(context.Background(), u); err != nil {
			t.Fatal(err)
		}
		if p.calls != before+1 || p.text != text || p.user != 111 {
			t.Fatalf("normal request did not reach processor with trusted owner: %+v", p)
		}
	}
}

func TestStartSendFailureIsReturnedForPollingRetry(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	p := &fakeProcessor{}
	bot := New("secret", srv.Client(), p, nil)
	bot.baseURL = srv.URL
	var u Update
	if err := json.Unmarshal([]byte(`{"update_id":42,"message":{"from":{"id":111},"chat":{"id":111,"type":"private"},"text":"/start"}}`), &u); err != nil {
		t.Fatal(err)
	}
	if err := bot.Handle(context.Background(), u); err == nil {
		t.Fatal("greeting delivery failure must be retried by polling")
	}
	if p.calls != 0 {
		t.Fatal("start send failure must not call the expense processor")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type timeoutTransportError struct{}

func (timeoutTransportError) Error() string   { return "private transport detail TOP_SECRET" }
func (timeoutTransportError) Timeout() bool   { return true }
func (timeoutTransportError) Temporary() bool { return true }

func TestTransportFailureCategoriesStaySanitized(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"canceled", context.Canceled, "Telegram request canceled"},
		{"deadline", context.DeadlineExceeded, "Telegram transport timed out"},
		{"network timeout", timeoutTransportError{}, "Telegram transport timed out"},
		{"other network failure", errors.New("private transport detail TOP_SECRET"), "Telegram transport failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				return nil, &url.Error{Op: "Post", URL: r.URL.String(), Err: tc.err}
			})}
			bot := New("TOP_SECRET", client, nil, nil)
			err := bot.call(context.Background(), "getUpdates", nil, nil)
			if err == nil || err.Error() != tc.want {
				t.Fatalf("sanitized error = %v; want %q", err, tc.want)
			}
		})
	}
}

type cancelOnLogHandler struct {
	slog.Handler
	cancel context.CancelFunc
}

func (h cancelOnLogHandler) Handle(ctx context.Context, record slog.Record) error {
	err := h.Handler.Handle(ctx, record)
	h.cancel()
	return err
}

func TestPollingAndReplyWarningsLogOnlySanitizedFailure(t *testing.T) {
	for _, replyFailure := range []bool{false, true} {
		name := "polling"
		if replyFailure {
			name = "reply"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var logs bytes.Buffer
			log := slog.New(cancelOnLogHandler{Handler: slog.NewTextHandler(&logs, nil), cancel: cancel})
			client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if replyFailure && strings.HasSuffix(r.URL.Path, "/getUpdates") {
					return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":[{"update_id":42,"message":{"from":{"id":111},"chat":{"id":111,"type":"private"},"text":"/start"}}]}`))}, nil
				}
				return nil, &url.Error{Op: "Post", URL: r.URL.String(), Err: timeoutTransportError{}}
			})}
			bot := New("TOP_SECRET", client, nil, log)
			if err := bot.Run(ctx); !errors.Is(err, context.Canceled) {
				t.Fatalf("poller did not stop after diagnostic warning: %v", err)
			}
			output := logs.String()
			if !strings.Contains(output, "Telegram "+name+" unavailable; retrying") || !strings.Contains(output, "Telegram transport timed out") {
				t.Fatalf("warning lacks safe failure category: %s", output)
			}
			if strings.Contains(output, "TOP_SECRET") || strings.Contains(output, "private transport detail") || strings.Contains(output, "https://") {
				t.Fatalf("sensitive transport detail leaked into logs: %s", output)
			}
		})
	}
}
