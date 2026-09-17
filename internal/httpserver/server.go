package httpserver

import (
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/a-h/templ"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/nimbit-platform/nx-cache/internal/auth"
	"github.com/nimbit-platform/nx-cache/internal/cacheapi"
	"github.com/nimbit-platform/nx-cache/internal/cleanup"
	"github.com/nimbit-platform/nx-cache/internal/config"
	"github.com/nimbit-platform/nx-cache/internal/storage"
	"github.com/nimbit-platform/nx-cache/internal/store"
	"github.com/nimbit-platform/nx-cache/internal/web"
)

const pageSize = 25

type Server struct {
	Cfg      config.Config
	Backend  storage.Backend
	Store    store.Store
	Cleaner  *cleanup.Cleaner
	Sessions *auth.Sessions
	Log      *slog.Logger
	Static   fs.FS
	logins   *loginGate
}

func (s *Server) Handler() http.Handler {
	cache := &cacheapi.Handler{
		Backend:       s.Backend,
		Store:         s.Store,
		Cleaner:       s.Cleaner,
		CleanupOnSave: s.Cfg.CleanupOnSave,
		MaxUpload:     s.Cfg.MaxUploadBytes,
	}

	if s.logins == nil {
		s.logins = newLoginGate(5, 30*time.Second)
	}

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	if s.Cfg.TrustForwardedIP {
		r.Use(middleware.RealIP)
	}
	r.Use(middleware.Recoverer)
	r.Use(securityHeaders)
	r.Use(requestTimeout(30 * time.Second))
	r.Use(middleware.Logger)

	r.Get("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("OK"))
	})

	if s.Static != nil {
		fileServer := http.FileServer(http.FS(s.Static))
		r.Handle("/static/*", http.StripPrefix("/static/", fileServer))
	}

	r.Group(func(r chi.Router) {
		r.Use(s.bearer)
		r.Put("/v1/cache/{hash}", cache.Put)
		r.Get("/v1/cache/{hash}", cache.Get)
		r.Head("/v1/cache/{hash}", cache.Head)
	})

	r.Get("/login", s.loginGet)
	r.Post("/login", s.loginPost)
	r.Group(func(r chi.Router) {
		r.Use(s.requireSession)
		r.Get("/", s.dashboard)
		r.Get("/ui/entries", s.entries)
		r.Get("/ui/stats", s.stats)
		r.Post("/ui/cleanup", s.cleanupNow)
		r.Post("/logout", s.logout)
	})
	return r
}

func (s *Server) bearer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		write := r.Method == http.MethodPut || r.Method == http.MethodDelete
		ok, status, msg := auth.BearerOK(r.Header.Get("Authorization"), s.Cfg.AccessToken, s.Cfg.ReadToken, write)
		if !ok {
			http.Error(w, msg, status)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) requireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := s.Sessions.UsernameFromRequest(r); !ok {
			if strings.HasPrefix(r.URL.Path, "/ui/") {
				w.Header().Set("HX-Redirect", "/login")
				http.Error(w, "authentication required", http.StatusUnauthorized)
				return
			}
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) loginGet(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.Sessions.UsernameFromRequest(r); ok {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	s.render(w, r, http.StatusOK, web.LoginPage(web.LoginData{}))
}

func (s *Server) loginPost(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r, s.Cfg.TrustForwardedIP)
	if s.logins != nil && !s.logins.allow(ip) {
		s.render(w, r, http.StatusTooManyRequests, web.LoginPage(web.LoginData{Error: "Too many attempts, try again later"}))
		return
	}
	if err := r.ParseForm(); err != nil {
		s.render(w, r, http.StatusBadRequest, web.LoginPage(web.LoginData{Error: "Invalid form"}))
		return
	}
	user := r.FormValue("username")
	pass := r.FormValue("password")
	if user != s.Cfg.UIUsername || !auth.CheckPassword(pass, s.Cfg.UIPassword) {
		if s.logins != nil {
			s.logins.failure(ip)
		}
		s.render(w, r, http.StatusUnauthorized, web.LoginPage(web.LoginData{
			Username: user,
			Error:    "Invalid username or password",
		}))
		return
	}
	if s.logins != nil {
		s.logins.success(ip)
	}
	s.Sessions.SetCookie(w, user)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	s.Sessions.ClearCookie(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	data, err := s.dashboardData(r)
	if err != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	s.render(w, r, http.StatusOK, web.DashboardPage(data))
}

func (s *Server) entries(w http.ResponseWriter, r *http.Request) {
	data, err := s.dashboardData(r)
	if err != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	s.render(w, r, http.StatusOK, web.EntriesSection(data))
}

func (s *Server) stats(w http.ResponseWriter, r *http.Request) {
	data, err := s.dashboardData(r)
	if err != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	s.render(w, r, http.StatusOK, web.StatsSection(data))
}

func (s *Server) cleanupNow(w http.ResponseWriter, r *http.Request) {
	n, err := s.Cleaner.Run(r.Context())
	if err != nil {
		http.Error(w, "cleanup failed", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/?"+url.Values{"msg": {strconv.Itoa(n) + " expired artifacts removed"}}.Encode(), http.StatusSeeOther)
}

func (s *Server) dashboardData(r *http.Request) (web.DashboardData, error) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	entries, total, err := s.Store.List(r.Context(), q, pageSize, (page-1)*pageSize)
	if err != nil {
		return web.DashboardData{}, err
	}
	stats, err := s.Store.Stats(r.Context())
	if err != nil {
		return web.DashboardData{}, err
	}
	user, _ := s.Sessions.UsernameFromRequest(r)
	msg := r.URL.Query().Get("msg")
	return web.NewDashboard(user, s.Cfg.CacheTTL.String(), q, msg, page, pageSize, stats, entries, total), nil
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, status int, c templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := c.Render(r.Context(), w); err != nil && s.Log != nil {
		s.Log.Error("render template", "err", err)
	}
}
