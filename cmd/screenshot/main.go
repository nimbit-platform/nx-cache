package main

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/nimbit-platform/nx-cache/internal/auth"
	"github.com/nimbit-platform/nx-cache/internal/cleanup"
	"github.com/nimbit-platform/nx-cache/internal/config"
	"github.com/nimbit-platform/nx-cache/internal/httpserver"
	"github.com/nimbit-platform/nx-cache/internal/nxartifact"
	"github.com/nimbit-platform/nx-cache/internal/storage"
	"github.com/nimbit-platform/nx-cache/internal/store"
	"github.com/nimbit-platform/nx-cache/internal/web"
)

func main() {
	base := os.Getenv("SCREENSHOT_BASE_URL")
	user := env("UI_USERNAME", "admin")
	pass := env("UI_PASSWORD", "admin")
	out := env("SCREENSHOT_DIR", "docs/screenshots")
	if err := os.MkdirAll(out, 0o755); err != nil {
		log.Fatal(err)
	}

	if base == "" {
		url, cleanup, err := startEmbedded(user, pass)
		if err != nil {
			log.Fatal(err)
		}
		defer cleanup()
		base = url
		log.Println("embedded dashboard at", base)
	}

	allocOpts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(chromePath()),
		chromedp.Flag("headless", true),
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.Flag("hide-scrollbars", true),
		chromedp.WindowSize(1440, 900),
	)
	alloc, cancel := chromedp.NewExecAllocator(context.Background(), allocOpts...)
	defer cancel()
	ctx, cancel := chromedp.NewContext(alloc)
	defer cancel()
	ctx, cancel = context.WithTimeout(ctx, 45*time.Second)
	defer cancel()

	var loginPNG, dashPNG []byte
	if err := chromedp.Run(ctx,
		chromedp.Navigate(base+"/login"),
		chromedp.WaitVisible(`input[name="username"]`, chromedp.ByQuery),
		chromedp.Sleep(500*time.Millisecond),
		chromedp.FullScreenshot(&loginPNG, 100),
		chromedp.SendKeys(`#username`, user, chromedp.ByID),
		chromedp.SendKeys(`#password`, pass, chromedp.ByID),
		chromedp.Click(`button[type="submit"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`h1`, chromedp.ByQuery),
		chromedp.Sleep(700*time.Millisecond),
		chromedp.FullScreenshot(&dashPNG, 100),
	); err != nil {
		log.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(out, "login.png"), loginPNG, 0o644); err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "dashboard.png"), dashPNG, 0o644); err != nil {
		log.Fatal(err)
	}
	fmt.Println("wrote", filepath.Join(out, "login.png"))
	fmt.Println("wrote", filepath.Join(out, "dashboard.png"))
}

func startEmbedded(user, pass string) (string, func(), error) {
	mem := storage.NewMemory()
	catalog := store.NewObject(mem)
	staticFS, err := web.Static()
	if err != nil {
		return "", nil, err
	}
	srv := &httpserver.Server{
		Cfg: config.Config{
			AccessToken:    "dev-token",
			UIUsername:     user,
			UIPassword:     pass,
			CacheTTL:       5 * 24 * time.Hour,
			CleanupOnSave:  false,
			MaxUploadBytes: 10 << 20,
			CatalogBackend: "s3",
		},
		Backend: mem,
		Store:   catalog,
		Cleaner: &cleanup.Cleaner{Backend: mem, Store: catalog, TTL: 5 * 24 * time.Hour, Interval: time.Hour},
		Sessions: &auth.Sessions{
			Secret:   []byte("screenshot-session-secret"),
			Username: user,
		},
		Static: staticFS,
	}
	ts := httptest.NewServer(srv.Handler())

	seeds := []struct {
		hash     string
		terminal string
		files    map[string][]byte
		hits     int
	}{
		{
			hash:     "a1b2c3d4e5f6a7b8c9d0",
			terminal: "> nx run web:build:production\ncompiled successfully\n",
			files:    map[string][]byte{"outputs/apps/web/dist/main.js": []byte("console.log(1)")},
			hits:     4,
		},
		{
			hash:     "9f8e7d6c5b4a3210fedc",
			terminal: "> nx run api:test\nTests: 12 passed\n",
			files:    map[string][]byte{"outputs/coverage/api/lcov.info": []byte("TN:")},
			hits:     2,
		},
		{
			hash:     "11223344556677889900",
			terminal: "> nx run api:lint\n",
			files:    map[string][]byte{"outputs/libs/api/.eslintcache": []byte("{}")},
			hits:     1,
		},
	}
	client := ts.Client()
	for _, s := range seeds {
		body, err := nxartifact.Pack(s.terminal, 0, s.files)
		if err != nil {
			ts.Close()
			return "", nil, err
		}
		req, err := http.NewRequest(http.MethodPut, ts.URL+"/v1/cache/"+s.hash, bytes.NewReader(body))
		if err != nil {
			ts.Close()
			return "", nil, err
		}
		req.Header.Set("Authorization", "Bearer dev-token")
		req.Header.Set("Content-Type", "application/octet-stream")
		req.ContentLength = int64(len(body))
		resp, err := client.Do(req)
		if err != nil {
			ts.Close()
			return "", nil, err
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			ts.Close()
			return "", nil, fmt.Errorf("seed put %s: %d", s.hash, resp.StatusCode)
		}
		for i := 0; i < s.hits; i++ {
			req, _ = http.NewRequest(http.MethodGet, ts.URL+"/v1/cache/"+s.hash, nil)
			req.Header.Set("Authorization", "Bearer dev-token")
			resp, err = client.Do(req)
			if err != nil {
				ts.Close()
				return "", nil, err
			}
			resp.Body.Close()
		}
	}
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/v1/cache/missing-task-hash", nil)
	req.Header.Set("Authorization", "Bearer dev-token")
	resp, err := client.Do(req)
	if err != nil {
		ts.Close()
		return "", nil, err
	}
	resp.Body.Close()
	return ts.URL, ts.Close, nil
}

func chromePath() string {
	if p := os.Getenv("CHROME_PATH"); p != "" {
		return p
	}
	for _, name := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser"} {
		if abs, err := exec.LookPath(name); err == nil {
			return abs
		}
	}
	return "google-chrome"
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
