//go:build e2e

package e2e_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nimbit-platform/nx-cache/internal/auth"
	"github.com/nimbit-platform/nx-cache/internal/cleanup"
	"github.com/nimbit-platform/nx-cache/internal/config"
	"github.com/nimbit-platform/nx-cache/internal/httpserver"
	"github.com/nimbit-platform/nx-cache/internal/storage"
	"github.com/nimbit-platform/nx-cache/internal/store"
	"github.com/nimbit-platform/nx-cache/internal/web"
)

// These tests exercise the Nx OpenAPI cache protocol against RustFS.
// An Nx workspace is not required: the CLI only PUT/GETs /v1/cache/{hash}
// with a bearer token and an octet-stream body (a tar archive).

func rustfsConfig(t *testing.T) (storage.S3Config, bool) {
	t.Helper()
	endpoint := os.Getenv("S3_ENDPOINT_URL")
	if endpoint == "" {
		endpoint = "http://127.0.0.1:9000"
	}
	key := os.Getenv("AWS_ACCESS_KEY_ID")
	if key == "" {
		key = "nxcache"
	}
	secret := os.Getenv("AWS_SECRET_ACCESS_KEY")
	if secret == "" {
		secret = "nxcache-e2e-secret-key"
	}
	bucket := os.Getenv("S3_BUCKET_NAME")
	if bucket == "" {
		bucket = "nx-cache-e2e"
	}
	cfg := storage.S3Config{
		Region:          "us-east-1",
		AccessKeyID:     key,
		SecretAccessKey: secret,
		Bucket:          bucket,
		Endpoint:        endpoint,
		Prefix:          fmt.Sprintf("e2e/%d/", time.Now().UnixNano()),
		ForcePathStyle:  true,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	backend, err := storage.NewS3(ctx, cfg)
	if err != nil {
		t.Logf("s3 client: %v", err)
		return cfg, false
	}
	if err := backend.EnsureBucket(ctx); err != nil {
		t.Logf("rustfs not reachable at %s: %v", endpoint, err)
		return cfg, false
	}
	return cfg, true
}

func startE2E(t *testing.T) string {
	t.Helper()
	cfg, ok := rustfsConfig(t)
	if !ok {
		if os.Getenv("E2E_REQUIRE_RUSTFS") != "" {
			t.Fatal("RustFS is required but not reachable; start `docker compose up -d rustfs` or set S3_ENDPOINT_URL")
		}
		t.Skip("RustFS is not running; start `docker compose up -d rustfs` or set S3_ENDPOINT_URL")
	}
	ctx := context.Background()
	backend, err := storage.NewS3(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := backend.EnsureBucket(ctx); err != nil {
		t.Fatal(err)
	}
	catalog := store.NewObject(backend, store.ObjectOptions{FlushInterval: 50 * time.Millisecond})
	if err := catalog.Load(ctx); err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	catalog.Start(runCtx)
	t.Cleanup(func() {
		cancel()
		_ = catalog.Close()
	})

	cleaner := &cleanup.Cleaner{Backend: backend, Store: catalog, TTL: 5 * 24 * time.Hour, Interval: time.Hour}
	staticFS, err := web.Static()
	if err != nil {
		t.Fatal(err)
	}
	srv := &httpserver.Server{
		Cfg: config.Config{
			AccessToken:    "e2e-token",
			ReadToken:      "e2e-read",
			UIUsername:     "admin",
			UIPassword:     "adminpass",
			CacheTTL:       5 * 24 * time.Hour,
			CleanupOnSave:  false,
			MaxUploadBytes: 10 << 20,
			CatalogBackend: "s3",
			CatalogFlush:   50 * time.Millisecond,
		},
		Backend: backend,
		Store:   catalog,
		Cleaner: cleaner,
		Sessions: &auth.Sessions{
			Secret:   []byte("e2e-session-secret"),
			Username: "admin",
		},
		Static: staticFS,
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts.URL
}

func nxClient() *http.Client {
	return &http.Client{Timeout: 15 * time.Second}
}

func nxPut(t *testing.T, client *http.Client, base, hash, token string, body []byte) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, base+"/v1/cache/"+hash, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/octet-stream")
	req.ContentLength = int64(len(body))
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestE2ENxProtocolAgainstRustFS(t *testing.T) {
	base := startE2E(t)
	client := nxClient()
	hash := fmt.Sprintf("nx-%d", time.Now().UnixNano())
	payload := []byte("nx-tar-archive-bytes")

	req, _ := http.NewRequest(http.MethodPut, base+"/v1/cache/"+hash, bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/octet-stream")
	req.ContentLength = int64(len(payload))
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("unauthenticated put: %d", resp.StatusCode)
	}

	resp = nxPut(t, client, base, hash, "e2e-read", payload)
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("read token write: %d", resp.StatusCode)
	}

	resp = nxPut(t, client, base, hash, "e2e-token", payload)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("put: %d %s", resp.StatusCode, body)
	}

	resp = nxPut(t, client, base, hash, "e2e-token", payload)
	resp.Body.Close()
	if resp.StatusCode != 409 {
		t.Fatalf("expected 409, got %d", resp.StatusCode)
	}

	req, _ = http.NewRequest(http.MethodGet, base+"/v1/cache/"+hash, nil)
	req.Header.Set("Authorization", "Bearer e2e-read")
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !bytes.Equal(got, payload) {
		t.Fatalf("get: %d %s", resp.StatusCode, got)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/octet-stream" {
		t.Fatalf("content-type %s", ct)
	}

	req, _ = http.NewRequest(http.MethodHead, base+"/v1/cache/"+hash, nil)
	req.Header.Set("Authorization", "Bearer e2e-token")
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("head: %d", resp.StatusCode)
	}

	req, _ = http.NewRequest(http.MethodGet, base+"/v1/cache/missing-"+hash, nil)
	req.Header.Set("Authorization", "Bearer e2e-token")
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatalf("miss: %d", resp.StatusCode)
	}
}

func TestE2EDashboardAgainstRustFS(t *testing.T) {
	base := startE2E(t)
	client := nxClient()
	hash := fmt.Sprintf("ui-%d", time.Now().UnixNano())
	resp := nxPut(t, client, base, hash, "e2e-token", []byte("artifact-body"))
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("put: %d", resp.StatusCode)
	}

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	browser := &http.Client{Jar: jar, Timeout: 15 * time.Second}
	form := url.Values{"username": {"admin"}, "password": {"adminpass"}}
	resp, err = browser.PostForm(base+"/login", form)
	if err != nil {
		t.Fatal(err)
	}
	pageBytes, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("dashboard after login: %d", resp.StatusCode)
	}
	page := string(pageBytes)
	if !strings.Contains(page, "Remote cache") || !strings.Contains(page, hash) {
		t.Fatalf("dashboard missing artifact: %s", page[:min(400, len(page))])
	}
}
