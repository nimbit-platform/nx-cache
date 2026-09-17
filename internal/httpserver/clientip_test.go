package httpserver

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nimbit-platform/nx-cache/internal/config"
)

func TestClientIPIgnoresLeftmostXFFFromUntrustedPeer(t *testing.T) {
	trusted, err := config.ParseIPNets("10.0.0.1/32")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/login", nil)
	req.RemoteAddr = "203.0.113.9:9"
	req.Header.Set("X-Forwarded-For", "10.1.2.3")
	if got := clientIP(req, true, trusted); got != "203.0.113.9" {
		t.Fatalf("spoofed XFF must not win: %s", got)
	}
	if got := clientIP(req, false, trusted); got != "203.0.113.9" {
		t.Fatalf("untrusted mode: %s", got)
	}
}

func TestClientIPWalksXFFFromTheRight(t *testing.T) {
	trusted, err := config.ParseIPNets("10.0.0.0/8")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.0.0.1:443"
	req.Header.Set("X-Forwarded-For", "10.1.2.3, 203.0.113.9")
	if got := clientIP(req, true, trusted); got != "203.0.113.9" {
		t.Fatalf("expected real client, got %s", got)
	}

	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	if got := clientIP(req, true, trusted); got != "203.0.113.9" {
		t.Fatalf("replaced XFF: %s", got)
	}

	req.Header.Del("X-Forwarded-For")
	req.Header.Set("X-Real-IP", "198.51.100.4")
	if got := clientIP(req, true, trusted); got != "198.51.100.4" {
		t.Fatalf("X-Real-IP behind trusted proxy: %s", got)
	}

	req.RemoteAddr = "203.0.113.9:9"
	if got := clientIP(req, true, trusted); got != "203.0.113.9" {
		t.Fatalf("X-Real-IP from untrusted peer: %s", got)
	}
}

func TestForwardedAllowListProbe(t *testing.T) {
	s, _, _, _ := testServer(t)
	allow, err := config.ParseIPNets("10.0.0.0/8")
	if err != nil {
		t.Fatal(err)
	}
	trusted, err := config.ParseIPNets("192.0.2.1/32")
	if err != nil {
		t.Fatal(err)
	}
	s.Cfg.AllowNets = allow
	s.Cfg.TrustForwardedIP = true
	s.Cfg.TrustedProxyNets = trusted
	h := s.Handler()

	hit := func(remote, xff string) int {
		req := httptest.NewRequest(http.MethodGet, "/login", nil)
		req.RemoteAddr = remote
		if xff != "" {
			req.Header.Set("X-Forwarded-For", xff)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	if code := hit("203.0.113.9:9", ""); code != http.StatusForbidden {
		t.Fatalf("no XFF: %d", code)
	}
	if code := hit("203.0.113.9:9", "10.1.2.3"); code != http.StatusForbidden {
		t.Fatalf("spoofed leftmost XFF: %d", code)
	}
	if code := hit("192.0.2.1:9", "10.1.2.3"); code != http.StatusOK {
		t.Fatalf("trusted proxy + allowed client: %d", code)
	}
	if code := hit("192.0.2.1:9", "203.0.113.9"); code != http.StatusForbidden {
		t.Fatalf("trusted proxy + denied client: %d", code)
	}
}

func TestRateLimitNotBypassedByXFF(t *testing.T) {
	s, _, _, _ := testServer(t)
	s.Cfg.RateLimitRPS = 1
	s.Cfg.RateLimitBurst = 1
	s.Cfg.TrustForwardedIP = true
	trusted, err := config.ParseIPNets("10.0.0.1/32")
	if err != nil {
		t.Fatal(err)
	}
	s.Cfg.TrustedProxyNets = trusted
	h := s.Handler()
	hit := func(xff string) int {
		req := httptest.NewRequest(http.MethodGet, "/login", nil)
		req.RemoteAddr = "203.0.113.9:9"
		req.Header.Set("X-Forwarded-For", xff)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := hit("10.0.0.2"); code != http.StatusOK {
		t.Fatalf("first: %d", code)
	}
	if code := hit("10.0.0.3"); code != http.StatusTooManyRequests {
		t.Fatalf("spoofed XFF must share the real-IP bucket: %d", code)
	}
}
