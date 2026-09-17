package httpserver

import (
	"net"
	"net/http"
	"sync"
	"time"
)

type ipLimiter struct {
	rps   float64
	burst float64
	mu    sync.Mutex
	byIP  map[string]*ipBucket
}

type ipBucket struct {
	tokens float64
	last   time.Time
}

func newIPLimiter(rps float64, burst int) *ipLimiter {
	if rps <= 0 {
		return nil
	}
	if burst < 1 {
		burst = int(rps)
		if burst < 1 {
			burst = 1
		}
	}
	return &ipLimiter{rps: rps, burst: float64(burst), byIP: map[string]*ipBucket{}}
}

func (l *ipLimiter) allow(ip string) bool {
	if l == nil {
		return true
	}
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.byIP[ip]
	if b == nil {
		b = &ipBucket{tokens: l.burst, last: now}
		l.byIP[ip] = b
	}
	elapsed := now.Sub(b.last).Seconds()
	if elapsed > 0 {
		b.tokens += elapsed * l.rps
		if b.tokens > l.burst {
			b.tokens = l.burst
		}
		b.last = now
	}
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	if len(l.byIP) > 10_000 {
		l.gcLocked(now)
	}
	return true
}

func (l *ipLimiter) gcLocked(now time.Time) {
	for ip, b := range l.byIP {
		if now.Sub(b.last) > 2*time.Minute {
			delete(l.byIP, ip)
		}
	}
}

func ipAllowed(nets []*net.IPNet, ipStr string) bool {
	if len(nets) == 0 {
		return true
	}
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return false
	}
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

func (s *Server) allowlist(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" || len(s.Cfg.AllowNets) == 0 {
			next.ServeHTTP(w, r)
			return
		}
		ip := clientIP(r, s.Cfg.TrustForwardedIP)
		if !ipAllowed(s.Cfg.AllowNets, ip) {
			s.logger().Warn("ip not allowed", "ip", ip, "path", r.URL.Path)
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) rateLimit(lim *ipLimiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if lim == nil || r.URL.Path == "/health" {
				next.ServeHTTP(w, r)
				return
			}
			ip := clientIP(r, s.Cfg.TrustForwardedIP)
			if !lim.allow(ip) {
				s.logger().Warn("rate limited", "ip", ip, "path", r.URL.Path)
				w.Header().Set("Retry-After", "1")
				http.Error(w, "Too many requests", http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
