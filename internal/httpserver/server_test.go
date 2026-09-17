package httpserver

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nimbit-platform/nx-cache/internal/auth"
	"github.com/nimbit-platform/nx-cache/internal/cleanup"
	"github.com/nimbit-platform/nx-cache/internal/config"
	"github.com/nimbit-platform/nx-cache/internal/storage"
	"github.com/nimbit-platform/nx-cache/internal/store"
)

func testServer(t *testing.T) (*Server, http.Handler, *storage.Memory, store.Store) {
	t.Helper()
	mem := storage.NewMemory()
	catalog := store.NewObject(mem)
	cfg := config.Config{
		AccessToken:    "write-token",
		ReadToken:      "read-token",
		UIUsername:     "admin",
		UIPassword:     "secret",
		CacheTTL:       5 * 24 * time.Hour,
		CleanupOnSave:  true,
		MaxUploadBytes: 10 << 20,
		CatalogBackend: "s3",
	}
	cleaner := &cleanup.Cleaner{Backend: mem, Store: catalog, TTL: cfg.CacheTTL, Interval: time.Hour}
	s := &Server{
		Cfg:     cfg,
		Backend: mem,
		Store:   catalog,
		Cleaner: cleaner,
		Sessions: &auth.Sessions{
			Secret:   []byte("session-secret"),
			Username: "admin",
		},
	}
	return s, s.Handler(), mem, catalog
}

func do(h http.Handler, method, path, token string, body []byte, headers map[string]string) *httptest.ResponseRecorder {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rdr)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestHealth(t *testing.T) {
	_, h, _, _ := testServer(t)
	rec := do(h, http.MethodGet, "/health", "", nil, nil)
	if rec.Code != 200 || rec.Body.String() != "OK" {
		t.Fatalf("health: %d %s", rec.Code, rec.Body.String())
	}
}

func TestPutGetAuthAndHits(t *testing.T) {
	_, h, _, db := testServer(t)
	payload := []byte("nx-artifact")

	rec := do(h, http.MethodPut, "/v1/cache/abc123", "", payload, map[string]string{"Content-Length": "11"})
	if rec.Code != 401 {
		t.Fatalf("expected 401, got %d", rec.Code)
	}

	rec = do(h, http.MethodPut, "/v1/cache/abc123", "read-token", payload, map[string]string{"Content-Length": "11"})
	if rec.Code != 403 {
		t.Fatalf("expected 403 for read token write, got %d %s", rec.Code, rec.Body.String())
	}

	rec = do(h, http.MethodPut, "/v1/cache/abc123", "write-token", payload, nil)
	if rec.Code != 411 {
		t.Fatalf("expected 411, got %d", rec.Code)
	}

	rec = do(h, http.MethodPut, "/v1/cache/abc123", "write-token", payload, map[string]string{"Content-Length": "11"})
	if rec.Code != 200 {
		t.Fatalf("put: %d %s", rec.Code, rec.Body.String())
	}

	rec = do(h, http.MethodPut, "/v1/cache/abc123", "write-token", payload, map[string]string{"Content-Length": "11"})
	if rec.Code != 409 {
		t.Fatalf("expected 409, got %d", rec.Code)
	}

	rec = do(h, http.MethodGet, "/v1/cache/missing", "write-token", nil, nil)
	if rec.Code != 404 {
		t.Fatalf("expected 404, got %d", rec.Code)
	}

	rec = do(h, http.MethodGet, "/v1/cache/abc123", "read-token", nil, nil)
	if rec.Code != 200 || rec.Body.String() != string(payload) {
		t.Fatalf("get: %d %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/octet-stream" {
		t.Fatalf("content-type %s", ct)
	}

	stats, err := db.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Hits != 1 || stats.Misses != 1 || stats.Stores != 1 || stats.Entries != 1 {
		t.Fatalf("stats %+v", stats)
	}
}

func TestIncompletePut(t *testing.T) {
	_, h, mem, _ := testServer(t)
	rec := do(h, http.MethodPut, "/v1/cache/short", "write-token", []byte("nope"), map[string]string{"Content-Length": "100"})
	if rec.Code != 400 {
		t.Fatalf("expected 400, got %d %s", rec.Code, rec.Body.String())
	}
	ok, _ := mem.Exists(context.Background(), "short")
	if ok {
		t.Fatal("incomplete upload should not remain")
	}
}

func TestCleanupOlderThanTTL(t *testing.T) {
	s, _, mem, db := testServer(t)
	ctx := context.Background()
	if err := mem.Put(ctx, "old", bytes.NewReader([]byte("old")), 3); err != nil {
		t.Fatal(err)
	}
	if err := mem.Put(ctx, "new", bytes.NewReader([]byte("new")), 3); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := db.Seed(store.Entry{Hash: "old", Size: 3, CreatedAt: now.Add(-6 * 24 * time.Hour), LastAccessedAt: now.Add(-6 * 24 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := db.Seed(store.Entry{Hash: "new", Size: 3, CreatedAt: now, LastAccessedAt: now}); err != nil {
		t.Fatal(err)
	}
	n, err := s.Cleaner.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("deleted %d, want 1", n)
	}
	if ok, _ := mem.Exists(ctx, "old"); ok {
		t.Fatal("old artifact should be gone")
	}
	if ok, _ := mem.Exists(ctx, "new"); !ok {
		t.Fatal("new artifact should remain")
	}
}

func loginCookie(t *testing.T, h http.Handler) *http.Cookie {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader("username=admin&password=secret"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("login: %d %s", rec.Code, rec.Body.String())
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == "nx_cache_session" {
			return c
		}
	}
	t.Fatal("expected session cookie")
	return nil
}

func TestUIRequiresLogin(t *testing.T) {
	_, h, _, _ := testServer(t)
	rec := do(h, http.MethodGet, "/", "", nil, nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect, got %d", rec.Code)
	}
	rec = do(h, http.MethodPost, "/login", "", []byte("username=admin&password=wrong"), map[string]string{
		"Content-Type": "application/x-www-form-urlencoded",
	})
	if rec.Code != 401 {
		t.Fatalf("bad login: %d", rec.Code)
	}
	cookie := loginCookie(t, h)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("dashboard: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Remote cache") {
		t.Fatalf("dashboard missing title: %s", rec.Body.String()[:min(200, rec.Body.Len())])
	}
}

func TestHeadInvalidHashAndTooLarge(t *testing.T) {
	_, h, _, _ := testServer(t)
	payload := []byte("nx-artifact")

	rec := do(h, http.MethodHead, "/v1/cache/abc123", "write-token", nil, nil)
	if rec.Code != 404 {
		t.Fatalf("head miss: %d", rec.Code)
	}

	rec = do(h, http.MethodPut, "/v1/cache/abc123", "write-token", payload, map[string]string{"Content-Length": "11"})
	if rec.Code != 200 {
		t.Fatalf("put: %d %s", rec.Code, rec.Body.String())
	}

	rec = do(h, http.MethodHead, "/v1/cache/abc123", "read-token", nil, nil)
	if rec.Code != 200 {
		t.Fatalf("head hit: %d", rec.Code)
	}

	rec = do(h, http.MethodPut, "/v1/cache/not%20ok", "write-token", payload, map[string]string{"Content-Length": "11"})
	if rec.Code != 400 {
		t.Fatalf("invalid hash: %d %s", rec.Code, rec.Body.String())
	}

	rec = do(h, http.MethodGet, "/v1/cache/not%20ok", "write-token", nil, nil)
	if rec.Code != 404 {
		t.Fatalf("invalid hash get: %d", rec.Code)
	}

	s, _, _, _ := testServer(t)
	s.Cfg.MaxUploadBytes = 4
	h = s.Handler()
	rec = do(h, http.MethodPut, "/v1/cache/tiny", "write-token", []byte("xxxxx"), map[string]string{"Content-Length": "5"})
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("too large: %d", rec.Code)
	}
}

func TestDashboardListsArtifactsAndHTMX(t *testing.T) {
	_, h, _, _ := testServer(t)
	payload := []byte("nx-artifact")
	rec := do(h, http.MethodPut, "/v1/cache/lib-build-1", "write-token", payload, map[string]string{"Content-Length": "11"})
	if rec.Code != 200 {
		t.Fatalf("put: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(h, http.MethodGet, "/v1/cache/lib-build-1", "read-token", nil, nil)
	if rec.Code != 200 {
		t.Fatalf("get: %d", rec.Code)
	}
	rec = do(h, http.MethodGet, "/v1/cache/missing-x", "read-token", nil, nil)
	if rec.Code != 404 {
		t.Fatalf("miss: %d", rec.Code)
	}

	rec = do(h, http.MethodGet, "/ui/entries", "", nil, nil)
	if rec.Code != 401 {
		t.Fatalf("unauth entries: %d", rec.Code)
	}
	if rec.Header().Get("HX-Redirect") != "/login" {
		t.Fatalf("HX-Redirect=%s", rec.Header().Get("HX-Redirect"))
	}

	cookie := loginCookie(t, h)

	req := httptest.NewRequest(http.MethodGet, "/login", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("logged-in /login should redirect, got %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, "lib-build-1") {
		t.Fatalf("dashboard: %d %s", rec.Code, body[:min(400, len(body))])
	}
	if !strings.Contains(body, "Hits / misses") || !strings.Contains(body, "Cached artifacts") {
		t.Fatalf("dashboard missing stats: %s", body[:min(400, len(body))])
	}

	req = httptest.NewRequest(http.MethodGet, "/ui/entries?q=lib", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "lib-build-1") {
		t.Fatalf("entries: %d %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/ui/entries?q=zzz-none", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "No cache artifacts yet") {
		t.Fatalf("empty filter: %d %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/ui/stats", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "Artifacts") {
		t.Fatalf("stats: %d %s", rec.Code, rec.Body.String()[:min(300, rec.Body.Len())])
	}

	req = httptest.NewRequest(http.MethodPost, "/ui/cleanup", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("cleanup: %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "expired") {
		t.Fatalf("cleanup location %s", loc)
	}

	req = httptest.NewRequest(http.MethodPost, "/logout", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("logout: %d", rec.Code)
	}
	cleared := false
	for _, c := range rec.Result().Cookies() {
		if c.Name == "nx_cache_session" && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Fatal("expected session cookie to be cleared")
	}
}
