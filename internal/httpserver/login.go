package httpserver

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"
)

const loginGateMaxMap = 10_000

type loginGate struct {
	mu       sync.Mutex
	byIP     map[string]*loginAttempt
	maxFails int
	lockFor  time.Duration
	maxMap   int
}

type loginAttempt struct {
	fails int
	until time.Time
	seen  time.Time
}

func newLoginGate(maxFails int, lockFor time.Duration) *loginGate {
	if maxFails <= 0 {
		maxFails = 5
	}
	if lockFor <= 0 {
		lockFor = 30 * time.Second
	}
	return &loginGate{byIP: map[string]*loginAttempt{}, maxFails: maxFails, lockFor: lockFor, maxMap: loginGateMaxMap}
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
	st.seen = now
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
	limit := g.maxMap
	if limit < 1 {
		limit = loginGateMaxMap
	}
	if len(g.byIP) >= limit {
		g.gcLocked(now)
	}
	st := g.byIP[ip]
	if st == nil {
		if len(g.byIP) >= limit {
			g.gcLocked(now)
			if len(g.byIP) >= limit {
				g.dropLocked(limit / 2)
			}
		}
		st = &loginAttempt{}
		g.byIP[ip] = st
	}
	if now.After(st.until) && st.fails >= g.maxFails {
		st.fails = 0
	}
	st.fails++
	st.seen = now
	if st.fails >= g.maxFails {
		st.until = now.Add(g.lockFor)
	}
}

func (g *loginGate) gcLocked(now time.Time) {
	idle := g.lockFor
	if idle < time.Minute {
		idle = time.Minute
	}
	for ip, st := range g.byIP {
		if now.After(st.until) && now.Sub(st.seen) > idle {
			delete(g.byIP, ip)
		}
	}
}

func (g *loginGate) dropLocked(keep int) {
	for ip := range g.byIP {
		if len(g.byIP) <= keep {
			return
		}
		delete(g.byIP, ip)
	}
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
			timeout := d
			if strings.HasPrefix(r.URL.Path, "/ui/") {
				timeout = 60 * time.Second
			}
			ctx, cancel := context.WithTimeout(r.Context(), timeout)
			defer cancel()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
