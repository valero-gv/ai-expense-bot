package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"expensebot/internal/domain"
)

const validBody = `{"user_id":42,"amount":"350.00","category":"` + domain.Food + `","spent_at":"2026-10-16","comment":"coffee"}`

type fakeStore struct {
	items  map[int64]domain.Expense
	filter domain.Filter
	calls  int
	err    error
	users  []domain.User
}

func (s *fakeStore) List(_ context.Context, f domain.Filter) (domain.Page, error) {
	s.calls++
	s.filter = f
	p := domain.Page{Page: f.Page, PageSize: f.PageSize}
	for _, expense := range s.items {
		p.Items = append(p.Items, expense)
	}
	p.Total = int64(len(p.Items))
	return p, s.err
}
func (s *fakeStore) Summary(_ context.Context, f domain.Filter) (domain.Summary, error) {
	s.calls++
	s.filter = f
	return domain.Summary{Count: int64(len(s.items))}, s.err
}
func (s *fakeStore) Get(_ context.Context, id int64) (domain.Expense, error) {
	s.calls++
	if s.err != nil {
		return domain.Expense{}, s.err
	}
	item, ok := s.items[id]
	if !ok {
		return item, domain.ErrNotFound
	}
	return item, nil
}
func (s *fakeStore) Last(ctx context.Context) (domain.Expense, error) { return s.Get(ctx, 1) }
func (s *fakeStore) Create(_ context.Context, in domain.Input) (domain.Expense, error) {
	s.calls++
	if s.err != nil {
		return domain.Expense{}, s.err
	}
	if s.items == nil {
		s.items = make(map[int64]domain.Expense)
	}
	item := fromInput(1, in)
	s.items[item.ID] = item
	return item, nil
}
func (s *fakeStore) Update(_ context.Context, id int64, in domain.Input) (domain.Expense, error) {
	s.calls++
	if s.err != nil {
		return domain.Expense{}, s.err
	}
	if _, ok := s.items[id]; !ok {
		return domain.Expense{}, domain.ErrNotFound
	}
	item := fromInput(id, in)
	s.items[id] = item
	return item, nil
}
func (s *fakeStore) Delete(_ context.Context, id int64) error {
	s.calls++
	if s.err != nil {
		return s.err
	}
	if _, ok := s.items[id]; !ok {
		return domain.ErrNotFound
	}
	delete(s.items, id)
	return nil
}
func (s *fakeStore) Users(context.Context) ([]domain.User, error) {
	s.calls++
	return s.users, s.err
}
func (s *fakeStore) Ping(context.Context) error { s.calls++; return s.err }
func fromInput(id int64, in domain.Input) domain.Expense {
	return domain.Expense{ID: id, UserID: in.UserID, Amount: in.Amount, Category: in.Category, SpentAt: in.SpentAt, Comment: in.Comment}
}

func request(h http.Handler, method, target, body string, headers ...map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://localhost:8080"+target, strings.NewReader(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	for _, values := range headers {
		for k, v := range values {
			r.Header.Set(k, v)
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func assertStatus(t *testing.T, w *httptest.ResponseRecorder, want int) {
	t.Helper()
	if w.Code != want {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, want, w.Body.String())
	}
}

func assertCode(t *testing.T, w *httptest.ResponseRecorder, want string) {
	t.Helper()
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.Error.Code != want {
		t.Fatalf("error code = %q, want %q; JSON error: %v; body: %s", body.Error.Code, want, err, w.Body.String())
	}
}

func TestCRUDAndOwnerReplacement(t *testing.T) {
	s := &fakeStore{}
	h := New(s, nil)
	w := request(h, http.MethodPost, "/api/expenses", validBody)
	assertStatus(t, w, http.StatusCreated)
	if w.Header().Get("Location") != "/api/expenses/1" {
		t.Fatalf("missing created expense Location: %v", w.Header())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["amount"] != "350.00" || got["user_id"] != float64(42) {
		t.Fatalf("unexpected expense: %#v", got)
	}
	w = request(h, http.MethodGet, "/api/expenses/1", "")
	assertStatus(t, w, http.StatusOK)
	updated := strings.ReplaceAll(strings.ReplaceAll(validBody, `"user_id":42`, `"user_id":99`), `"350.00"`, `"450.00"`)
	w = request(h, http.MethodPut, "/api/expenses/1", updated)
	assertStatus(t, w, http.StatusOK)
	if s.items[1].UserID != 99 {
		t.Fatal("administrator must be able to change expense owner")
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["amount"] != "450.00" {
		t.Fatalf("updated amount = %#v", got["amount"])
	}
	w = request(h, http.MethodDelete, "/api/expenses/1", "")
	assertStatus(t, w, http.StatusNoContent)
	if w.Body.Len() != 0 {
		t.Fatal("204 response has a body")
	}
	w = request(h, http.MethodGet, "/api/expenses/1", "")
	assertStatus(t, w, http.StatusNotFound)
	assertCode(t, w, "not_found")
}

func TestInvalidJSONNeverReachesStore(t *testing.T) {
	tests := []struct {
		name, body string
		status     int
	}{
		{"empty", "", 400},
		{"malformed", `{`, 400},
		{"unknown field", strings.TrimSuffix(validBody, "}") + `,"admin":true}`, 400},
		{"trailing object", validBody + ` {}`, 400},
		{"trailing garbage", validBody + ` !`, 400},
		{"JSON null", `null`, 400},
		{"JSON array", `[]`, 400},
		{"numeric amount", strings.ReplaceAll(validBody, `"350.00"`, `350.00`), 400},
		{"negative amount", strings.ReplaceAll(validBody, `"350.00"`, `"-1.00"`), 400},
		{"zero owner", strings.ReplaceAll(validBody, `"user_id":42`, `"user_id":0`), 400},
		{"fractional owner", strings.ReplaceAll(validBody, `"user_id":42`, `"user_id":42.5`), 400},
		{"invalid category", strings.ReplaceAll(validBody, `"`+domain.Food+`"`, `"invalid"`), 400},
		{"impossible date", strings.ReplaceAll(validBody, `2026-10-16`, `2026-02-30`), 400},
		{"oversized", strings.ReplaceAll(validBody, `coffee`, strings.Repeat("a", maxBodyBytes)), 413},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for _, method := range []string{http.MethodPost, http.MethodPut} {
				s := &fakeStore{}
				target := "/api/expenses"
				if method == http.MethodPut {
					target += "/1"
				}
				w := request(New(s, nil), method, target, tc.body, map[string]string{"Content-Type": "application/json"})
				assertStatus(t, w, tc.status)
				assertCode(t, w, "invalid_request")
				if s.calls != 0 {
					t.Fatalf("invalid input reached store: %d calls", s.calls)
				}
			}
		})
	}
}

func TestFilterParsingAndEmptyLists(t *testing.T) {
	s := &fakeStore{}
	h := New(s, nil)
	w := request(h, http.MethodGet, "/api/expenses", "")
	assertStatus(t, w, 200)
	if strings.Contains(w.Body.String(), `"items":null`) || !strings.Contains(w.Body.String(), `"items":[]`) {
		t.Fatalf("empty list must be JSON array: %s", w.Body.String())
	}
	if s.filter.Page != 1 || s.filter.PageSize != 20 {
		t.Fatalf("defaults: %#v", s.filter)
	}
	query := url.Values{"user_id": {"42"}, "from": {"2026-10-01"}, "to": {"2026-10-16"}, "category": {domain.Food}, "q": {"coffee with milk"}, "page": {"2"}, "page_size": {"50"}}.Encode()
	for _, endpoint := range []string{"/api/expenses", "/api/summary"} {
		w = request(h, http.MethodGet, endpoint+"?"+query, "")
		assertStatus(t, w, 200)
		f := s.filter
		if f.UserID == nil || *f.UserID != 42 || f.From != "2026-10-01" || f.To != "2026-10-16" || f.Category != domain.Food || f.Query != "coffee with milk" || f.Page != 2 || f.PageSize != 50 {
			t.Fatalf("filter not forwarded: %#v", f)
		}
	}
	w = request(h, http.MethodGet, "/api/users", "")
	assertStatus(t, w, 200)
	if strings.TrimSpace(w.Body.String()) != `{"items":[]}` {
		t.Fatalf("empty users: %s", w.Body.String())
	}
	s.users = []domain.User{{ID: 42, Label: "Anna"}}
	w = request(h, http.MethodGet, "/api/users", "")
	assertStatus(t, w, 200)
	if !strings.Contains(w.Body.String(), `"label":"Anna"`) {
		t.Fatalf("user label lost: %s", w.Body.String())
	}
	w = request(h, http.MethodGet, "/api/categories", "")
	assertStatus(t, w, 200)
	var result struct {
		Items []string `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || len(result.Items) != 7 {
		t.Fatalf("categories: %s, %v", w.Body.String(), err)
	}
	for i, category := range domain.Categories {
		if result.Items[i] != category {
			t.Fatalf("stored category key changed: got %q, want %q", result.Items[i], category)
		}
	}
}

type cancelAwareStore struct {
	*fakeStore
	entered chan context.Context
}

func (s *cancelAwareStore) List(ctx context.Context, _ domain.Filter) (domain.Page, error) {
	s.entered <- ctx
	<-ctx.Done()
	return domain.Page{}, ctx.Err()
}

func (s *cancelAwareStore) Create(ctx context.Context, _ domain.Input) (domain.Expense, error) {
	s.entered <- ctx
	<-ctx.Done()
	return domain.Expense{}, ctx.Err()
}

func TestRequestCancellationReachesStore(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		t.Run(method, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			store := &cancelAwareStore{fakeStore: &fakeStore{}, entered: make(chan context.Context, 1)}
			body := ""
			if method == http.MethodPost {
				body = validBody
			}
			r := httptest.NewRequest(method, "http://localhost:8080/api/expenses", strings.NewReader(body)).WithContext(ctx)
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			done := make(chan struct{})
			go func() {
				New(store, nil).ServeHTTP(w, r)
				close(done)
			}()
			select {
			case received := <-store.entered:
				if deadline, ok := received.Deadline(); !ok || time.Until(deadline) > requestTimeout {
					t.Fatal("store did not receive the bounded request context")
				}
			case <-time.After(time.Second):
				t.Fatal("store was not called")
			}
			cancel()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("canceled request left the handler blocked")
			}
			assertStatus(t, w, http.StatusInternalServerError)
			assertCode(t, w, "internal_error")
			if strings.Contains(w.Body.String(), "context canceled") {
				t.Fatal("internal cancellation details leaked")
			}
		})
	}
}

type deadlineStore struct {
	*fakeStore
	inspect func(context.Context) error
}

func (s *deadlineStore) List(ctx context.Context, _ domain.Filter) (domain.Page, error) {
	return domain.Page{}, s.inspect(ctx)
}

func (s *deadlineStore) Ping(ctx context.Context) error { return s.inspect(ctx) }

func TestDatabaseOperationsHaveServerDeadlines(t *testing.T) {
	for _, tc := range []struct {
		path   string
		budget time.Duration
	}{
		{"/api/expenses", 10 * time.Second},
		{"/healthz", 2 * time.Second},
	} {
		t.Run(tc.path, func(t *testing.T) {
			started := time.Now()
			called := false
			store := &deadlineStore{fakeStore: &fakeStore{}, inspect: func(ctx context.Context) error {
				called = true
				deadline, ok := ctx.Deadline()
				if !ok || deadline.After(time.Now().Add(tc.budget)) || deadline.Before(started.Add(tc.budget-time.Second)) {
					t.Errorf("database operation has unexpected deadline: %v, present=%t", deadline, ok)
				}
				return fmt.Errorf("private database detail: %w", context.DeadlineExceeded)
			}}
			w := request(New(store, nil), http.MethodGet, tc.path, "")
			assertStatus(t, w, http.StatusServiceUnavailable)
			assertCode(t, w, "internal_error")
			if !called || !strings.Contains(w.Body.String(), `"message":"Request timed out"`) || strings.Contains(w.Body.String(), "private") {
				t.Fatalf("unsafe or missing timeout response: %s", w.Body.String())
			}
		})
	}
}

func TestEarlierRequestDeadlineIsPreserved(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	parentDeadline, _ := ctx.Deadline()
	store := &deadlineStore{fakeStore: &fakeStore{}, inspect: func(received context.Context) error {
		deadline, ok := received.Deadline()
		if !ok || !deadline.Equal(parentDeadline) {
			t.Errorf("shorter parent deadline was replaced: %v", deadline)
		}
		<-received.Done()
		return received.Err()
	}}
	r := httptest.NewRequest(http.MethodGet, "http://localhost:8080/api/expenses", nil).WithContext(ctx)
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { New(store, nil).ServeHTTP(w, r); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("request ignored its earlier deadline")
	}
	assertStatus(t, w, http.StatusServiceUnavailable)
	assertCode(t, w, "internal_error")
}

func TestInvalidFiltersAndIDsNeverReachStore(t *testing.T) {
	queries := []string{"user_id=0", "user_id=-1", "user_id=9223372036854775808", "user_id=1.5", "user_id=+42", "user_id=42&user_id=43", "user_id=", "page=0", "page=-1", "page=abc", "page_size=101", "page_size=0", "page_size=1.5", "from=2026-02-30", "from=2026-10-17&to=2026-10-01", "category=invalid", "unknown=x", "from=%zz", "from=2026-10-01;user_id=42"}
	for _, query := range queries {
		t.Run(query, func(t *testing.T) {
			for _, endpoint := range []string{"/api/expenses", "/api/summary"} {
				s := &fakeStore{}
				w := request(New(s, nil), http.MethodGet, endpoint+"?"+query, "")
				assertStatus(t, w, 400)
				assertCode(t, w, "invalid_request")
				if s.calls != 0 {
					t.Fatal("invalid filter reached store")
				}
			}
		})
	}
	for _, id := range []string{"0", "-1", "abc", "1.2", "+1", "9223372036854775808"} {
		for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
			s := &fakeStore{}
			w := request(New(s, nil), method, "/api/expenses/"+id, validBody)
			assertStatus(t, w, 400)
			if s.calls != 0 {
				t.Fatalf("invalid ID %s reached store", id)
			}
		}
	}
}

func TestErrorMappingAndHealth(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{fmt.Errorf("wrapped: %w", domain.ErrNotFound), 404, "not_found"},
		{fmt.Errorf("wrapped: %w", domain.ErrInvalid), 400, "invalid_request"},
		{errors.New("postgres password=secret SQL detail"), 500, "internal_error"},
	} {
		s := &fakeStore{err: tc.err}
		h := New(s, nil)
		for _, route := range []struct{ method, target, body string }{
			{http.MethodGet, "/api/expenses/1", ""},
			{http.MethodGet, "/api/expenses", ""},
			{http.MethodGet, "/api/summary", ""},
			{http.MethodGet, "/api/users", ""},
			{http.MethodPost, "/api/expenses", validBody},
			{http.MethodPut, "/api/expenses/1", validBody},
			{http.MethodDelete, "/api/expenses/1", ""},
		} {
			w := request(h, route.method, route.target, route.body)
			assertStatus(t, w, tc.status)
			assertCode(t, w, tc.code)
			if strings.Contains(w.Body.String(), "secret") {
				t.Fatal("database details leaked")
			}
		}
	}
	s := &fakeStore{}
	h := New(s, nil)
	w := request(h, http.MethodGet, "/healthz", "")
	assertStatus(t, w, 200)
	if strings.TrimSpace(w.Body.String()) != `{"status":"ok"}` {
		t.Fatalf("health: %s", w.Body.String())
	}
	s.err = errors.New("connection failed")
	w = request(h, http.MethodGet, "/healthz", "")
	assertStatus(t, w, 503)
	assertCode(t, w, "internal_error")
}

func TestBrowserAndNetworkBoundaries(t *testing.T) {
	for _, headers := range []map[string]string{
		{"Origin": "https://evil.example"},
		{"Origin": "null"},
		{"Origin": "http://localhost:9090"},
		{"Origin": "https://localhost:8080"},
		{"Origin": "http://localhost:8080/"},
		{"Sec-Fetch-Site": "cross-site"},
		{"Sec-Fetch-Site": "same-site"},
	} {
		s := &fakeStore{}
		w := request(New(s, nil), http.MethodPost, "/api/expenses", validBody, headers)
		assertStatus(t, w, 403)
		if s.calls != 0 {
			t.Fatalf("cross-origin request reached store: %v", headers)
		}
		if w.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Fatal("API must not grant CORS")
		}
	}
	for _, headers := range []map[string]string{
		{}, {"Origin": "http://localhost:8080", "Sec-Fetch-Site": "same-origin"}, {"Sec-Fetch-Site": "none"},
	} {
		w := request(New(&fakeStore{}, nil), http.MethodPost, "/api/expenses", validBody, headers)
		assertStatus(t, w, 201)
	}
	for _, host := range []string{"evil.example", "evil.example:8080", "192.168.1.1:8080", "localhost.evil.example:8080"} {
		s := &fakeStore{}
		r := httptest.NewRequest(http.MethodGet, "http://"+host+"/api/expenses", nil)
		w := httptest.NewRecorder()
		New(s, nil).ServeHTTP(w, r)
		assertStatus(t, w, 403)
		if s.calls != 0 {
			t.Fatalf("nonlocal Host %s reached store", host)
		}
	}
	for _, host := range []string{"localhost:8080", "127.0.0.1:8080", "[::1]:8080", "[::1]"} {
		r := httptest.NewRequest(http.MethodGet, "http://"+host+"/api/expenses", nil)
		w := httptest.NewRecorder()
		New(&fakeStore{}, nil).ServeHTTP(w, r)
		assertStatus(t, w, 200)
	}
	for _, contentType := range []string{"", "text/plain", "application/x-www-form-urlencoded"} {
		s := &fakeStore{}
		w := request(New(s, nil), http.MethodPost, "/api/expenses", validBody, map[string]string{"Content-Type": contentType})
		assertStatus(t, w, 415)
		if s.calls != 0 {
			t.Fatal("invalid content type reached store")
		}
	}
	w := request(New(&fakeStore{}, nil), http.MethodOptions, "/api/expenses", "", map[string]string{"Origin": "https://evil.example", "Access-Control-Request-Method": "POST"})
	assertStatus(t, w, 403)
}

func TestStaticFilesSecurityHeadersAndUnknownRoutes(t *testing.T) {
	files := fstest.MapFS{"index.html": {Data: []byte("<html>Panel</html>")}, "app.js": {Data: []byte("console.log('panel')")}, "nested/hidden.txt": {Data: []byte("hidden")}}
	h := New(&fakeStore{}, files)
	w := request(h, http.MethodGet, "/", "")
	assertStatus(t, w, 200)
	if w.Body.String() != "<html>Panel</html>" {
		t.Fatalf("root: %s", w.Body.String())
	}
	for key, want := range map[string]string{"X-Content-Type-Options": "nosniff", "X-Frame-Options": "DENY", "Referrer-Policy": "no-referrer", "Cross-Origin-Resource-Policy": "same-origin"} {
		if w.Header().Get(key) != want {
			t.Errorf("%s = %q", key, w.Header().Get(key))
		}
	}
	if !strings.Contains(w.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Fatal("missing CSP")
	}
	w = request(h, http.MethodHead, "/", "")
	assertStatus(t, w, 200)
	if w.Body.Len() != 0 {
		t.Fatal("HEAD has a body")
	}
	w = request(h, http.MethodGet, "/app.js", "")
	assertStatus(t, w, 200)
	for _, target := range []string{"/missing", "/nested/", "/go.mod"} {
		assertStatus(t, request(h, http.MethodGet, target, ""), 404)
	}
	w = request(h, http.MethodGet, "/api/unknown", "")
	assertStatus(t, w, 404)
	assertCode(t, w, "not_found")
	w = request(h, http.MethodPatch, "/api/expenses/1", "")
	assertStatus(t, w, 405)
	assertCode(t, w, "invalid_request")
	if w.Header().Get("Allow") != "GET, PUT, DELETE" {
		t.Fatal("missing Allow header")
	}
}
