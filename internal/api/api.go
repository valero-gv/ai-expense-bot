// Package api exposes the local family administration REST API and embedded panel.
package api

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"

	"expensebot/internal/domain"
)

const maxBodyBytes = 16 * 1024

type server struct {
	store domain.AdminStore
}

// New serves the unauthenticated administrator panel. It deliberately accepts
// only localhost/loopback hosts: publishing this API requires authentication.
func New(store domain.AdminStore, webFS fs.FS) http.Handler {
	s := &server{store: store}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.health)
	mux.HandleFunc("/api/categories", s.categories)
	mux.HandleFunc("/api/users", s.users)
	mux.HandleFunc("/api/summary", s.summary)
	mux.HandleFunc("/api/expenses", s.expenses)
	mux.HandleFunc("/api/expenses/{id}", s.expense)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "Endpoint not found")
	})
	mux.HandleFunc("/api", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "Endpoint not found")
	})
	mux.Handle("/", staticFiles(webFS))
	return secure(mux)
}

func (s *server) health(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodGet) {
		return
	}
	if err := s.store.Ping(r.Context()); err != nil {
		writeError(w, http.StatusServiceUnavailable, "internal_error", "Database unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *server) categories(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodGet) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": domain.Categories})
}

func (s *server) users(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodGet) {
		return
	}
	items, err := s.store.Users(r.Context())
	if err != nil {
		storeError(w, err)
		return
	}
	if items == nil {
		items = []domain.User{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *server) summary(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodGet) {
		return
	}
	filter, err := requestFilter(r)
	if err != nil {
		invalid(w, err.Error())
		return
	}
	result, err := s.store.Summary(r.Context(), filter)
	if err != nil {
		storeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *server) expenses(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		filter, err := requestFilter(r)
		if err != nil {
			invalid(w, err.Error())
			return
		}
		result, err := s.store.List(r.Context(), filter)
		if err != nil {
			storeError(w, err)
			return
		}
		if result.Items == nil {
			result.Items = []domain.Expense{}
		}
		writeJSON(w, http.StatusOK, result)
	case http.MethodPost:
		input, ok := decodeInput(w, r)
		if !ok {
			return
		}
		result, err := s.store.Create(r.Context(), input)
		if err != nil {
			storeError(w, err)
			return
		}
		w.Header().Set("Location", "/api/expenses/"+strconv.FormatInt(result.ID, 10))
		writeJSON(w, http.StatusCreated, result)
	default:
		method(w, r, http.MethodGet, http.MethodPost)
	}
}

func (s *server) expense(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPut && r.Method != http.MethodDelete {
		method(w, r, http.MethodGet, http.MethodPut, http.MethodDelete)
		return
	}
	id, err := positiveID(r.PathValue("id"))
	if err != nil {
		invalid(w, "Expense id must be a positive integer")
		return
	}
	switch r.Method {
	case http.MethodGet:
		result, err := s.store.Get(r.Context(), id)
		if err != nil {
			storeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	case http.MethodPut:
		input, ok := decodeInput(w, r)
		if !ok {
			return
		}
		result, err := s.store.Update(r.Context(), id, input)
		if err != nil {
			storeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	case http.MethodDelete:
		if err := s.store.Delete(r.Context(), id); err != nil {
			storeError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func requestFilter(r *http.Request) (domain.Filter, error) {
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return domain.Filter{}, errors.New("Invalid URL query")
	}
	return parseFilter(values)
}

func parseFilter(values url.Values) (domain.Filter, error) {
	filter := domain.Filter{Page: 1, PageSize: 20}
	for key, entries := range values {
		switch key {
		case "user_id", "from", "to", "category", "q", "page", "page_size":
		default:
			return filter, errors.New("Unknown query parameter: " + key)
		}
		if len(entries) != 1 || entries[0] == "" {
			return filter, errors.New("Query parameters must have one nonempty value")
		}
	}
	if raw, ok := values["user_id"]; ok {
		id, err := positiveID(raw[0])
		if err != nil {
			return filter, errors.New("user_id must be a positive integer")
		}
		filter.UserID = &id
	}
	for key, dest := range map[string]*int{"page": &filter.Page, "page_size": &filter.PageSize} {
		if raw, ok := values[key]; ok {
			n, err := positiveID(raw[0])
			if err != nil || n > int64(int(^uint(0)>>1)) {
				return filter, errors.New(key + " must be a positive integer")
			}
			*dest = int(n)
		}
	}
	filter.From = values.Get("from")
	filter.To = values.Get("to")
	filter.Category = values.Get("category")
	filter.Query = values.Get("q")
	if err := domain.ValidateFilter(filter); err != nil {
		return filter, err
	}
	return filter, nil
}

func positiveID(raw string) (int64, error) {
	if raw == "" {
		return 0, errors.New("empty integer")
	}
	for _, c := range raw {
		if c < '0' || c > '9' {
			return 0, errors.New("invalid integer")
		}
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, errors.New("integer must be positive and in int64 range")
	}
	return id, nil
}

func decodeInput(w http.ResponseWriter, r *http.Request) (domain.Input, bool) {
	var input domain.Input
	contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "invalid_request", "Content-Type must be application/json")
		return input, false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		decodeError(w, err)
		return input, false
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		if err == nil {
			invalid(w, "Body must contain one JSON object")
		} else {
			decodeError(w, err)
		}
		return input, false
	}
	if err := domain.ValidateInput(input); err != nil {
		invalid(w, err.Error())
		return input, false
	}
	return input, true
}

func decodeError(w http.ResponseWriter, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeError(w, http.StatusRequestEntityTooLarge, "invalid_request", "Request body is too large")
		return
	}
	invalid(w, "Invalid expense JSON: "+err.Error())
}

func method(w http.ResponseWriter, r *http.Request, methods ...string) bool {
	for _, allowed := range methods {
		if r.Method == allowed {
			return true
		}
	}
	w.Header().Set("Allow", strings.Join(methods, ", "))
	writeError(w, http.StatusMethodNotAllowed, "invalid_request", "Method not allowed")
	return false
}

func storeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "Expense not found")
	case errors.Is(err, domain.ErrInvalid):
		invalid(w, err.Error())
	default:
		// Database and driver errors may contain query or connection details.
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to complete request")
	}
}

func invalid(w http.ResponseWriter, message string) {
	writeError(w, http.StatusBadRequest, "invalid_request", message)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		if !localHost(r.Host) {
			writeError(w, http.StatusForbidden, "invalid_request", "Only localhost access is allowed")
			return
		}
		if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
			writeError(w, http.StatusForbidden, "invalid_request", "Cross-origin access is not allowed")
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			parsed, err := url.Parse(origin)
			scheme := "http"
			if r.TLS != nil {
				scheme = "https"
			}
			if err != nil || parsed.Scheme != scheme || !strings.EqualFold(parsed.Host, r.Host) || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
				writeError(w, http.StatusForbidden, "invalid_request", "Cross-origin access is not allowed")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func localHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	// IPv6 without an explicit port still uses brackets in the Host header.
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func staticFiles(files fs.FS) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !method(w, r, http.MethodGet, http.MethodHead) {
			return
		}
		if files == nil {
			http.NotFound(w, r)
			return
		}
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name == "." || name == "" {
			name = "index.html"
		}
		if !fs.ValidPath(name) {
			http.NotFound(w, r)
			return
		}
		info, err := fs.Stat(files, name)
		if err != nil || info.IsDir() {
			http.NotFound(w, r)
			return
		}
		// Avoid FileServer's /index.html redirect for the root document.
		if name == "index.html" && r.URL.Path == "/" {
			data, err := fs.ReadFile(files, name)
			if err != nil {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache")
			if r.Method == http.MethodGet {
				_, _ = w.Write(data)
			}
			return
		}
		http.FileServer(http.FS(files)).ServeHTTP(w, r)
	})
}
