package httpserver

import (
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/nimbit-platform/nx-cache/internal/reqlog"
)

func (s *Server) logger() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

func (s *Server) requestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		log := s.logger().With(
			"request_id", middleware.GetReqID(r.Context()),
			"ip", clientIP(r, s.Cfg.TrustForwardedIP),
			"method", r.Method,
			"path", r.URL.Path,
		)
		next.ServeHTTP(ww, r.WithContext(reqlog.With(r.Context(), log)))

		status := ww.Status()
		if status == 0 {
			status = http.StatusOK
		}
		attrs := []any{"status", status, "bytes", ww.BytesWritten(), "ms", time.Since(start).Milliseconds()}
		if r.URL.Path == "/health" || strings.HasPrefix(r.URL.Path, "/static/") {
			log.Debug("http", attrs...)
			return
		}
		switch {
		case status >= 500:
			log.Error("http", attrs...)
		case status >= 400:
			log.Warn("http", attrs...)
		default:
			log.Info("http", attrs...)
		}
	})
}
