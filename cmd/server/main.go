package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/nimbit-platform/nx-cache/internal/auth"
	"github.com/nimbit-platform/nx-cache/internal/cleanup"
	"github.com/nimbit-platform/nx-cache/internal/config"
	"github.com/nimbit-platform/nx-cache/internal/httpserver"
	"github.com/nimbit-platform/nx-cache/internal/storage"
	"github.com/nimbit-platform/nx-cache/internal/store"
	"github.com/nimbit-platform/nx-cache/internal/web"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(runHealthcheck())
	}

	cfg, err := config.Load()
	if err != nil {
		slog.Error("invalid configuration", "err", err)
		os.Exit(1)
	}

	level := slog.LevelInfo
	switch strings.ToLower(cfg.LogLevel) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var backend storage.Backend
	if cfg.StorageBackend == "memory" {
		backend = storage.NewMemory()
		log.Warn("using in-memory storage; artifacts will not persist")
	} else {
		s3, err := storage.NewS3(ctx, storage.S3Config{
			Region:          cfg.AWSRegion,
			AccessKeyID:     cfg.AWSAccessKeyID,
			SecretAccessKey: cfg.AWSSecretAccessKey,
			Bucket:          cfg.S3Bucket,
			Endpoint:        cfg.S3Endpoint,
			Prefix:          cfg.S3Prefix,
			ForcePathStyle:  cfg.S3ForcePathStyle,
		})
		if err != nil {
			log.Error("s3 client", "err", err)
			os.Exit(1)
		}
		if cfg.CreateBucket {
			var last error
			for i := 0; i < 12; i++ {
				last = s3.EnsureBucket(ctx)
				if last == nil {
					break
				}
				log.Warn("waiting for bucket", "err", last)
				time.Sleep(time.Second)
			}
			if last != nil {
				log.Error("ensure bucket", "err", last)
				os.Exit(1)
			}
		}
		backend = s3
	}

	var catalog store.Store
	switch cfg.CatalogBackend {
	case "sqlite":
		db, err := store.Open(cfg.SQLitePath)
		if err != nil {
			log.Error("sqlite", "err", err)
			os.Exit(1)
		}
		catalog = db
		log.Info("catalog backend", "type", "sqlite", "path", cfg.SQLitePath)
	default:
		obj := store.NewObject(backend, store.ObjectOptions{
			FlushInterval: cfg.CatalogFlush,
			Log:           log,
		})
		if err := obj.Load(ctx); err != nil {
			log.Error("could not load catalog snapshot from s3", "err", err)
			os.Exit(1)
		}
		obj.Start(ctx)
		catalog = obj
		log.Info("catalog backend", "type", "s3", "flush", cfg.CatalogFlush.String())
		log.Warn("S3 catalog is single-replica; run only one process per bucket prefix")
	}
	defer func() {
		if err := catalog.Close(); err != nil {
			log.Error("catalog close", "err", err)
		}
	}()

	cleaner := &cleanup.Cleaner{
		Backend:  backend,
		Store:    catalog,
		TTL:      cfg.CacheTTL,
		Interval: cfg.CleanupInterval,
		Log:      log,
	}
	cleaner.Start(ctx)

	go func() {
		recCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
		defer cancel()
		objects, err := backend.List(recCtx)
		if err != nil {
			log.Warn("could not list existing cache objects", "err", err)
			return
		}
		for _, obj := range objects {
			at := obj.LastModified
			if at.IsZero() {
				at = time.Now()
			}
			if err := catalog.EnsureEntry(recCtx, obj.Hash, obj.Size, at); err != nil {
				log.Warn("reconcile cache entry", "hash", obj.Hash, "err", err)
			}
		}
		log.Info("reconciled cache catalog", "objects", len(objects))
	}()

	staticFS, err := web.Static()
	if err != nil {
		log.Error("static assets", "err", err)
		os.Exit(1)
	}

	srv := &httpserver.Server{
		Cfg:     cfg,
		Backend: backend,
		Store:   catalog,
		Cleaner: cleaner,
		Sessions: &auth.Sessions{
			Secret:   []byte(cfg.SessionSecret),
			Username: cfg.UIUsername,
			Secure:   cfg.SessionSecure,
		},
		Log:    log,
		Static: staticFS,
	}

	if cfg.SessionSecretRandom {
		log.Warn("SESSION_SECRET was unset; generated an ephemeral secret (sessions will not survive restart)")
	}

	httpSrv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	go func() {
		log.Info("nx cache listening",
			"addr", httpSrv.Addr,
			"bucket", cfg.S3Bucket,
			"ttl", cfg.CacheTTL.String(),
		)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("server", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutdownCtx)
}
