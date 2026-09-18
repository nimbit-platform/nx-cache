package httpserver

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nimbit-platform/nx-cache/internal/auth"
	"github.com/nimbit-platform/nx-cache/internal/cleanup"
	"github.com/nimbit-platform/nx-cache/internal/config"
	"github.com/nimbit-platform/nx-cache/internal/longpoll"
	"github.com/nimbit-platform/nx-cache/internal/nxartifact"
	"github.com/nimbit-platform/nx-cache/internal/storage"
	"github.com/nimbit-platform/nx-cache/internal/store"
)

func testServer(t *testing.T) (*Server, http.Handler, *storage.Memory, store.Store) {
	t.Helper()
	mem := storage.NewMemory()
	hub := longpoll.NewHub()
	catalog := store.NewNotifying(store.NewObject(mem), hub)
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
		Hub: hub,
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

func TestRequestLogUsesSlog(t *testing.T) {
	s, _, _, _ := testServer(t)
	var buf bytes.Buffer
	s.Log = slog.New(slog.NewJSONHandler(&buf, nil))
	h := s.Handler()
	rec := do(h, http.MethodGet, "/login", "", nil, nil)
	if rec.Code != 200 {
		t.Fatalf("login page: %d", rec.Code)
	}
	out := buf.String()
	if !strings.Contains(out, `"msg":"http"`) || !strings.Contains(out, `"path":"/login"`) || !strings.Contains(out, `"status":200`) {
		t.Fatalf("request log: %s", out)
	}
	buf.Reset()
	rec = do(h, http.MethodGet, "/health", "", nil, nil)
	if rec.Code != 200 {
		t.Fatal(rec.Code)
	}
	if strings.Contains(buf.String(), `"msg":"http"`) {
		t.Fatalf("health should be debug-only, got %s", buf.String())
	}

	nets, err := config.ParseIPNets("10.0.0.0/8")
	if err != nil {
		t.Fatal(err)
	}
	s.Cfg.AllowNets = nets
	h = s.Handler()
	buf.Reset()
	req := httptest.NewRequest(http.MethodGet, "/login", nil)
	req.RemoteAddr = "8.8.8.8:9"
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("denied: %d", rec.Code)
	}
	out = buf.String()
	if !strings.Contains(out, `"msg":"http"`) || !strings.Contains(out, `"status":403`) {
		t.Fatalf("allow list should be in access log: %s", out)
	}
}

func TestRecovererLogsPanic(t *testing.T) {
	s, _, _, _ := testServer(t)
	var buf bytes.Buffer
	s.Log = slog.New(slog.NewJSONHandler(&buf, nil))
	h := s.requestLog(s.recoverer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	})))
	req := httptest.NewRequest(http.MethodGet, "/panic", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d", rec.Code)
	}
	out := buf.String()
	if !strings.Contains(out, `"msg":"panic"`) || !strings.Contains(out, "boom") {
		t.Fatalf("panic log: %s", out)
	}
	if !strings.Contains(out, `"msg":"http"`) || !strings.Contains(out, `"status":500`) {
		t.Fatalf("access log after panic: %s", out)
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

func TestLoginRateLimit(t *testing.T) {
	s, _, _, _ := testServer(t)
	s.logins = newLoginGate(3, time.Hour)
	h := s.Handler()
	for i := 0; i < 3; i++ {
		rec := do(h, http.MethodPost, "/login", "", []byte("username=admin&password=wrong"), map[string]string{
			"Content-Type": "application/x-www-form-urlencoded",
		})
		if rec.Code != 401 {
			t.Fatalf("attempt %d: %d", i, rec.Code)
		}
	}
	rec := do(h, http.MethodPost, "/login", "", []byte("username=admin&password=wrong"), map[string]string{
		"Content-Type": "application/x-www-form-urlencoded",
	})
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", rec.Code)
	}
}

func TestLoginGateEvictsIdleEntries(t *testing.T) {
	g := newLoginGate(5, time.Second)
	g.maxMap = 50
	now := time.Now()
	g.mu.Lock()
	for i := 0; i < 80; i++ {
		ip := net.IPv4(10, byte(i/256), byte(i%256), 1).String()
		g.byIP[ip] = &loginAttempt{fails: 1, seen: now.Add(-time.Hour), until: now.Add(-time.Minute)}
	}
	if len(g.byIP) != 80 {
		t.Fatalf("seed %d", len(g.byIP))
	}
	g.gcLocked(now)
	n := len(g.byIP)
	g.mu.Unlock()
	if n != 0 {
		t.Fatalf("gc left %d entries", n)
	}

	for i := 0; i < 80; i++ {
		g.failure("203.0.113." + strconv.Itoa(i%200+1))
	}
	g.mu.Lock()
	n = len(g.byIP)
	g.mu.Unlock()
	if n > g.maxMap {
		t.Fatalf("map grew to %d, cap %d", n, g.maxMap)
	}
}

func TestIPAllowList(t *testing.T) {
	s, _, _, _ := testServer(t)
	nets, err := config.ParseIPNets("10.0.0.0/8")
	if err != nil {
		t.Fatal(err)
	}
	s.Cfg.AllowNets = nets
	h := s.Handler()

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.RemoteAddr = "8.8.8.8:9"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("health must stay open: %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/login", nil)
	req.RemoteAddr = "10.1.2.3:9"
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("allowed: %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/login", nil)
	req.RemoteAddr = "8.8.8.8:9"
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("denied: %d", rec.Code)
	}
}

func TestIPRateLimit(t *testing.T) {
	s, _, _, _ := testServer(t)
	s.Cfg.RateLimitRPS = 1
	s.Cfg.RateLimitBurst = 2
	h := s.Handler()
	reqFor := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/login", nil)
		req.RemoteAddr = "192.0.2.10:9"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	if rec := reqFor(); rec.Code != 200 {
		t.Fatalf("1: %d", rec.Code)
	}
	if rec := reqFor(); rec.Code != 200 {
		t.Fatalf("2: %d", rec.Code)
	}
	rec := reqFor()
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", rec.Code)
	}
	if rec.Header().Get("Retry-After") != "1" {
		t.Fatalf("Retry-After=%s", rec.Header().Get("Retry-After"))
	}
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.RemoteAddr = "192.0.2.10:9"
	health := httptest.NewRecorder()
	h.ServeHTTP(health, req)
	if health.Code != 200 {
		t.Fatalf("health must skip rate limit: %d", health.Code)
	}
}

func TestSecurityHeaders(t *testing.T) {
	_, h, _, _ := testServer(t)
	rec := do(h, http.MethodGet, "/health", "", nil, nil)
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("headers %v", rec.Header())
	}
	if rec.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatal(rec.Header().Get("X-Frame-Options"))
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

func TestDashboardShowsTaskFromNxTar(t *testing.T) {
	_, h, _, _ := testServer(t)
	payload, err := nxartifact.Pack("> nx run web:build:production\ncompiled\n", 0, map[string][]byte{
		"outputs/apps/web/dist/main.js": []byte("ok"),
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := do(h, http.MethodPut, "/v1/cache/taskhash1", "write-token", payload, map[string]string{
		"Content-Length": strconv.Itoa(len(payload)),
	})
	if rec.Code != 200 {
		t.Fatalf("put: %d %s", rec.Code, rec.Body.String())
	}

	lintTar, err := nxartifact.Pack("> nx run api:lint\n", 0, map[string][]byte{"outputs/libs/api/.eslintcache": []byte("{}")})
	if err != nil {
		t.Fatal(err)
	}
	rec = do(h, http.MethodPut, "/v1/cache/taskhash2", "write-token", lintTar, map[string]string{
		"Content-Length": strconv.Itoa(len(lintTar)),
	})
	if rec.Code != 200 {
		t.Fatalf("put lint: %d", rec.Code)
	}

	cookie := loginCookie(t, h)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	body := rec.Body.String()
	if rec.Code != 200 {
		t.Fatalf("dashboard: %d", rec.Code)
	}
	for _, want := range []string{"web:build:production", "api:lint", "build", "lint", "By target"} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in dashboard: %s", want, body[:min(800, len(body))])
		}
	}

	req = httptest.NewRequest(http.MethodGet, "/ui/entries?q=lint", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	got := rec.Body.String()
	if !strings.Contains(got, "api:lint") || strings.Contains(got, "web:build") {
		t.Fatalf("filter lint: %s", got)
	}
}

func TestLongPollingColdRequestHeaders(t *testing.T) {
	_, h, _, _ := testServer(t)
	cookie := loginCookie(t, h)

	for _, endpoint := range []string{"/ui/stats", "/ui/entries"} {
		req := httptest.NewRequest(http.MethodGet, endpoint, nil)
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("%s: expected 200, got %d", endpoint, rec.Code)
		}
		if rec.Header().Get("Cache-Control") != "no-cache" {
			t.Fatalf("%s: expected Cache-Control: no-cache, got %s", endpoint, rec.Header().Get("Cache-Control"))
		}
		if rec.Header().Get("ETag") == "" {
			t.Fatalf("%s: expected non-empty ETag", endpoint)
		}
		if rec.Header().Get("Last-Modified") == "" {
			t.Fatalf("%s: expected non-empty Last-Modified", endpoint)
		}
	}
}

func TestLongPollingStandingRequestResolvesOnUpload(t *testing.T) {
	s, h, _, _ := testServer(t)
	cookie := loginCookie(t, h)

	// 1. Initial cold request to get ETag
	req := httptest.NewRequest(http.MethodGet, "/ui/stats", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("initial stats: %d", rec.Code)
	}
	etag1 := rec.Header().Get("ETag")
	if etag1 == "" {
		t.Fatal("empty initial ETag")
	}

	// 2. Second request with matching ETag should block
	standingDone := make(chan struct{})
	standingRec := httptest.NewRecorder()
	go func() {
		standingReq := httptest.NewRequest(http.MethodGet, "/ui/stats", nil)
		standingReq.AddCookie(cookie)
		standingReq.Header.Set("If-None-Match", etag1)
		h.ServeHTTP(standingRec, standingReq)
		close(standingDone)
	}()

	// Wait for the long poll to register as a standing subscriber
	for i := 0; i < 50; i++ {
		if s.Hub.SubscriberCount() > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if s.Hub.SubscriberCount() == 0 {
		t.Fatal("expected standing subscriber to be registered")
	}

	// 3. Perform a cache upload to trigger notification
	tarPayload, err := nxartifact.Pack("> nx run web:build\n", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	putRec := do(h, http.MethodPut, "/v1/cache/longpollhash1", "write-token", tarPayload, map[string]string{
		"Content-Length": strconv.Itoa(len(tarPayload)),
	})
	if putRec.Code != http.StatusOK {
		t.Fatalf("put: %d %s", putRec.Code, putRec.Body.String())
	}

	// 4. Standing request should unblock immediately
	select {
	case <-standingDone:
	case <-time.After(3 * time.Second):
		t.Fatal("standing poll request did not resolve in time after cache upload")
	}

	if standingRec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK after update, got %d", standingRec.Code)
	}
	etag2 := standingRec.Header().Get("ETag")
	if etag2 == etag1 {
		t.Fatalf("expected new ETag after update, got identical %s", etag2)
	}
	if !strings.Contains(standingRec.Body.String(), "Artifacts") {
		t.Fatalf("expected body with stats, got %s", standingRec.Body.String())
	}
}

func TestLongPollingStandingRequestResolvesOnHitAndMiss(t *testing.T) {
	s, h, _, _ := testServer(t)
	cookie := loginCookie(t, h)

	// Seed an artifact
	tarPayload, err := nxartifact.Pack("> nx run app:build\n", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	putRec := do(h, http.MethodPut, "/v1/cache/hashhit1", "write-token", tarPayload, map[string]string{
		"Content-Length": strconv.Itoa(len(tarPayload)),
	})
	if putRec.Code != http.StatusOK {
		t.Fatalf("put: %d", putRec.Code)
	}

	// Get current ETag
	req := httptest.NewRequest(http.MethodGet, "/ui/stats", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	etag := rec.Header().Get("ETag")

	// 1. Standing poll unblocks on Hit
	done := make(chan struct{})
	standingRec := httptest.NewRecorder()
	go func() {
		pollReq := httptest.NewRequest(http.MethodGet, "/ui/stats", nil)
		pollReq.AddCookie(cookie)
		pollReq.Header.Set("If-None-Match", etag)
		h.ServeHTTP(standingRec, pollReq)
		close(done)
	}()

	for i := 0; i < 50; i++ {
		if s.Hub.SubscriberCount() > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Trigger cache hit
	hitRec := do(h, http.MethodGet, "/v1/cache/hashhit1", "read-token", nil, nil)
	if hitRec.Code != http.StatusOK {
		t.Fatalf("get hit: %d", hitRec.Code)
	}

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("standing poll request did not resolve after cache hit")
	}
	if standingRec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK after hit, got %d", standingRec.Code)
	}

	etagAfterHit := standingRec.Header().Get("ETag")

	// 2. Standing poll unblocks on Miss
	doneMiss := make(chan struct{})
	standingRecMiss := httptest.NewRecorder()
	go func() {
		pollReq := httptest.NewRequest(http.MethodGet, "/ui/stats", nil)
		pollReq.AddCookie(cookie)
		pollReq.Header.Set("If-None-Match", etagAfterHit)
		h.ServeHTTP(standingRecMiss, pollReq)
		close(doneMiss)
	}()

	for i := 0; i < 50; i++ {
		if s.Hub.SubscriberCount() > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Trigger cache miss
	missRec := do(h, http.MethodGet, "/v1/cache/missinghashxyz", "read-token", nil, nil)
	if missRec.Code != http.StatusNotFound {
		t.Fatalf("get miss: %d", missRec.Code)
	}

	select {
	case <-doneMiss:
	case <-time.After(3 * time.Second):
		t.Fatal("standing poll request did not resolve after cache miss")
	}
	if standingRecMiss.Code != http.StatusOK {
		t.Fatalf("expected 200 OK after miss, got %d", standingRecMiss.Code)
	}
}

func TestLongPollingTimeoutReturns304(t *testing.T) {
	s, h, _, _ := testServer(t)
	s.PollTimeout = 50 * time.Millisecond
	cookie := loginCookie(t, h)

	// Initial cold request
	req := httptest.NewRequest(http.MethodGet, "/ui/stats", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	etag := rec.Header().Get("ETag")

	// Second request with same ETag times out to 304
	req2 := httptest.NewRequest(http.MethodGet, "/ui/stats", nil)
	req2.AddCookie(cookie)
	req2.Header.Set("If-None-Match", etag)
	rec2 := httptest.NewRecorder()
	start := time.Now()
	h.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusNotModified {
		t.Fatalf("expected 304 Not Modified, got %d", rec2.Code)
	}
	if rec2.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("expected Cache-Control: no-cache, got %s", rec2.Header().Get("Cache-Control"))
	}
	if rec2.Header().Get("ETag") != etag {
		t.Fatalf("expected matching ETag on 304, got %s", rec2.Header().Get("ETag"))
	}
	if rec2.Body.Len() > 0 {
		t.Fatalf("expected empty body on 304, got %q", rec2.Body.String())
	}
	if time.Since(start) < 40*time.Millisecond {
		t.Fatalf("expected to wait for PollTimeout, elapsed %v", time.Since(start))
	}
}

func TestLongPollingMultipleSubscribers(t *testing.T) {
	s, h, _, _ := testServer(t)
	cookie := loginCookie(t, h)

	// Get initial ETag
	req := httptest.NewRequest(http.MethodGet, "/ui/stats", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	etag := rec.Header().Get("ETag")

	const numSubscribers = 5
	var wg sync.WaitGroup
	wg.Add(numSubscribers)
	recorders := make([]*httptest.ResponseRecorder, numSubscribers)

	for i := 0; i < numSubscribers; i++ {
		recorders[i] = httptest.NewRecorder()
		go func(idx int) {
			defer wg.Done()
			r := httptest.NewRequest(http.MethodGet, "/ui/stats", nil)
			r.AddCookie(cookie)
			r.Header.Set("If-None-Match", etag)
			h.ServeHTTP(recorders[idx], r)
		}(i)
	}

	for i := 0; i < 50; i++ {
		if s.Hub.SubscriberCount() == numSubscribers {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if s.Hub.SubscriberCount() != numSubscribers {
		t.Fatalf("expected %d subscribers, got %d", numSubscribers, s.Hub.SubscriberCount())
	}

	// Trigger update
	tarPayload, err := nxartifact.Pack("> nx run api:test\n", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	do(h, http.MethodPut, "/v1/cache/multisubh1", "write-token", tarPayload, map[string]string{
		"Content-Length": strconv.Itoa(len(tarPayload)),
	})

	wg.Wait()

	for idx, r := range recorders {
		if r.Code != http.StatusOK {
			t.Errorf("subscriber %d got status %d", idx, r.Code)
		}
		if r.Header().Get("ETag") == etag {
			t.Errorf("subscriber %d got old ETag", idx)
		}
	}
}

func TestLongPollingIfModifiedSince(t *testing.T) {
	s, h, _, _ := testServer(t)
	cookie := loginCookie(t, h)

	// Initial request
	req := httptest.NewRequest(http.MethodGet, "/ui/stats", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	lm := rec.Header().Get("Last-Modified")

	done := make(chan struct{})
	standingRec := httptest.NewRecorder()
	go func() {
		pollReq := httptest.NewRequest(http.MethodGet, "/ui/stats", nil)
		pollReq.AddCookie(cookie)
		pollReq.Header.Set("If-Modified-Since", lm)
		h.ServeHTTP(standingRec, pollReq)
		close(done)
	}()

	for i := 0; i < 50; i++ {
		if s.Hub.SubscriberCount() > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Trigger cleanup to notify
	cleanRec := httptest.NewRequest(http.MethodPost, "/ui/cleanup", nil)
	cleanRec.AddCookie(cookie)
	cleanResp := httptest.NewRecorder()
	h.ServeHTTP(cleanResp, cleanRec)

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("standing poll request with IMS did not resolve after cleanup")
	}
	if standingRec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK after cleanup, got %d", standingRec.Code)
	}
}

func TestLongPollingEntriesEndpoint(t *testing.T) {
	s, h, _, _ := testServer(t)
	cookie := loginCookie(t, h)

	// 1. Initial cold request to /ui/entries
	req := httptest.NewRequest(http.MethodGet, "/ui/entries", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("initial entries: %d", rec.Code)
	}
	etag := rec.Header().Get("ETag")

	// 2. Second request with matching ETag blocks
	done := make(chan struct{})
	standingRec := httptest.NewRecorder()
	go func() {
		standingReq := httptest.NewRequest(http.MethodGet, "/ui/entries", nil)
		standingReq.AddCookie(cookie)
		standingReq.Header.Set("If-None-Match", etag)
		h.ServeHTTP(standingRec, standingReq)
		close(done)
	}()

	for i := 0; i < 50; i++ {
		if s.Hub.SubscriberCount() > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	// 3. Upload new entry
	tarPayload, err := nxartifact.Pack("> nx run web:lint\n", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	do(h, http.MethodPut, "/v1/cache/entriesh1", "write-token", tarPayload, map[string]string{
		"Content-Length": strconv.Itoa(len(tarPayload)),
	})

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("standing poll on /ui/entries did not resolve after upload")
	}

	if standingRec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for /ui/entries, got %d", standingRec.Code)
	}
	if !strings.Contains(standingRec.Body.String(), "entriesh1") {
		t.Fatalf("expected body to contain uploaded hash, got %s", standingRec.Body.String())
	}
}
