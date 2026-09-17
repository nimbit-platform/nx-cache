package httpserver

import (
	"net"
	"net/http"
	"strings"
)

func (s *Server) clientIP(r *http.Request) string {
	return clientIP(r, s.Cfg.TrustForwardedIP, s.Cfg.TrustedProxyNets)
}

// clientIP returns the TCP peer, or the first hop that is not a trusted proxy
// when walking X-Forwarded-For from the right. Client-supplied leftmost XFF
// values are ignored unless every hop to the right is in trusted.
func clientIP(r *http.Request, trustForwarded bool, trusted []*net.IPNet) string {
	peer := remoteIP(r.RemoteAddr)
	if !trustForwarded || len(trusted) == 0 {
		return peer
	}
	hops := forwardedChain(r, peer)
	for i := len(hops) - 1; i >= 0; i-- {
		if !ipAllowed(trusted, hops[i]) {
			return hops[i]
		}
	}
	return hops[0]
}

func forwardedChain(r *http.Request, peer string) []string {
	var hops []string
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		for _, part := range strings.Split(xff, ",") {
			if ip := normalizeIP(part); ip != "" {
				hops = append(hops, ip)
			}
		}
	} else if ip := normalizeIP(r.Header.Get("X-Real-IP")); ip != "" {
		hops = append(hops, ip)
	}
	if peer != "" {
		hops = append(hops, peer)
	}
	if len(hops) == 0 {
		return []string{r.RemoteAddr}
	}
	return hops
}

func remoteIP(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return normalizeIP(addr)
	}
	return normalizeIP(host)
}

func normalizeIP(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "[")
	s = strings.TrimSuffix(s, "]")
	ip := net.ParseIP(s)
	if ip == nil {
		return ""
	}
	return ip.String()
}
