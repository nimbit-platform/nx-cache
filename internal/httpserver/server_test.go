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
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader("username=admin&password=secret"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("good login: %d %s", rec.Code, rec.Body.String())
	}
	cookie := rec.Result().Cookies()
	if len(cookie) == 0 {
		t.Fatal("expected session cookie")
	}
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(cookie[0])
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("dashboard: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Remote cache") {
		t.Fatalf("dashboard missing title: %s", rec.Body.String()[:min(200, rec.Body.Len())])
	}
}
