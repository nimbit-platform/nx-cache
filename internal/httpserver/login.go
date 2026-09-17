package httpserver

import (
	"context"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

type loginGate struct {
	mu       sync.Mutex
	byIP     map[string]*loginAttempt
	maxFails int
	lockFor  time.Duration
}

type loginAttempt struct {
	fails int
	until time.Time
}

func newLoginGate(maxFails int, lockFor time.Duration) *loginGate {
	if maxFails <= 0 {
		maxFails = 5
	}
	if lockFor <= 0 {
		lockFor = 30 * time.Second
	}
	return &loginGate{byIP: map[string]*loginAttempt{}, maxFails: maxFails, lockFor: lockFor}
}

func (g *loginGate) allow(ip string) bool {
	if g == nil {
		return true
	}
	now := time.Now()
	g.mu.Lock()
	defer g.mu.Unlock()
	st := g.byIP[ip]
	if st == nil {
		return true
	}
	if now.After(st.until) {
		return true
	}
	return st.fails < g.maxFails
}

func (g *loginGate) success(ip string) {
	if g == nil {
		return
	}
	g.mu.Lock()
	delete(g.byIP, ip)
	g.mu.Unlock()
}

func (g *loginGate) failure(ip string) {
	if g == nil {
		return
	}
	now := time.Now()
	g.mu.Lock()
	defer g.mu.Unlock()
	st := g.byIP[ip]
	if st == nil {
		st = &loginAttempt{}
		g.byIP[ip] = st
	}
	if now.After(st.until) && st.fails >= g.maxFails {
		st.fails = 0
	}
	st.fails++
	if st.fails >= g.maxFails {
		st.until = now.Add(g.lockFor)
	}
}

func clientIP(r *http.Request, trustForwarded bool) string {
	if trustForwarded {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			return strings.TrimSpace(strings.Split(xff, ",")[0])
		}
		if xr := strings.TrimSpace(r.Header.Get("X-Real-IP")); xr != "" {
			return xr
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self'; img-src 'self' data:; frame-ancestors 'none'")
		if r.TLS != nil {
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}

func requestTimeout(d time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPut || strings.HasPrefix(r.URL.Path, "/v1/cache/") {
				next.ServeHTTP(w, r)
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), d)
			defer cancel()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
