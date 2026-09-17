package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is assembled from environment variables at process start.
type Config struct {
	Port     string
	LogLevel string

	AWSRegion          string
	AWSAccessKeyID     string
	AWSSecretAccessKey string
	S3Bucket           string
	S3Endpoint         string
	S3Prefix           string
	S3ForcePathStyle   bool
	CreateBucket       bool

	AccessToken string
	ReadToken   string

	UIUsername          string
	UIPassword          string
	SessionSecret       string
	SessionSecretRandom bool
	SessionSecure       bool
	TrustForwardedIP    bool

	SQLitePath string

	StorageBackend string
	CatalogBackend string
	CatalogFlush   time.Duration

	CacheTTL        time.Duration
	CleanupInterval time.Duration
	CleanupOnSave   bool
	MaxUploadBytes  int64
}

func Load() (Config, error) {
	forcePath, err := envBool("S3_FORCE_PATH_STYLE", os.Getenv("S3_ENDPOINT_URL") != "")
	if err != nil {
		return Config{}, err
	}
	createBucket, err := envBool("S3_CREATE_BUCKET", false)
	if err != nil {
		return Config{}, err
	}
	flush, err := envDuration("CATALOG_FLUSH_INTERVAL", 30*time.Second)
	if err != nil {
		return Config{}, err
	}
	ttl, err := envDuration("CACHE_TTL", 5*24*time.Hour)
	if err != nil {
		return Config{}, err
	}
	cleanupEvery, err := envDuration("CLEANUP_INTERVAL", time.Hour)
	if err != nil {
		return Config{}, err
	}
	cleanupOnSave, err := envBool("CLEANUP_ON_SAVE", true)
	if err != nil {
		return Config{}, err
	}
	maxUpload, err := envInt64("MAX_UPLOAD_BYTES", 2*1024*1024*1024)
	if err != nil {
		return Config{}, err
	}
	sessionSecure, err := envBool("SESSION_SECURE", true)
	if err != nil {
		return Config{}, err
	}
	trustFwd, err := envBool("TRUST_FORWARDED_IP", false)
	if err != nil {
		return Config{}, err
	}

	cfg := Config{
		Port:               env("PORT", "8080"),
		LogLevel:           env("LOG_LEVEL", "info"),
		AWSRegion:          env("AWS_REGION", "us-east-1"),
		AWSAccessKeyID:     os.Getenv("AWS_ACCESS_KEY_ID"),
		AWSSecretAccessKey: os.Getenv("AWS_SECRET_ACCESS_KEY"),
		S3Bucket:           env("S3_BUCKET_NAME", "nx-cache"),
		S3Endpoint:         os.Getenv("S3_ENDPOINT_URL"),
		S3Prefix:           env("S3_PREFIX", "nx-cache/"),
		S3ForcePathStyle:   forcePath,
		CreateBucket:       createBucket,
		AccessToken:        env("NX_CACHE_ACCESS_TOKEN", os.Getenv("NX_SELF_HOSTED_REMOTE_CACHE_ACCESS_TOKEN")),
		ReadToken:          os.Getenv("NX_CACHE_READ_TOKEN"),
		UIUsername:         env("UI_USERNAME", "admin"),
		UIPassword:         os.Getenv("UI_PASSWORD"),
		SessionSecret:      os.Getenv("SESSION_SECRET"),
		SessionSecure:      sessionSecure,
		TrustForwardedIP:   trustFwd,
		SQLitePath:         os.Getenv("SQLITE_PATH"),
		StorageBackend:     strings.ToLower(env("STORAGE_BACKEND", "s3")),
		CatalogBackend:     strings.ToLower(env("CATALOG_BACKEND", "s3")),
		CatalogFlush:       flush,
		CacheTTL:           ttl,
		CleanupInterval:    cleanupEvery,
		CleanupOnSave:      cleanupOnSave,
		MaxUploadBytes:     maxUpload,
	}

	if !strings.HasSuffix(cfg.S3Prefix, "/") && cfg.S3Prefix != "" {
		cfg.S3Prefix += "/"
	}
	if cfg.AccessToken == "" {
		return Config{}, fmt.Errorf("NX_CACHE_ACCESS_TOKEN is required")
	}
	if cfg.StorageBackend != "s3" && cfg.StorageBackend != "memory" {
		return Config{}, fmt.Errorf("STORAGE_BACKEND must be s3 or memory")
	}
	if cfg.StorageBackend == "s3" && cfg.S3Bucket == "" {
		return Config{}, fmt.Errorf("S3_BUCKET_NAME is required")
	}
	if cfg.CatalogBackend == "" {
		cfg.CatalogBackend = "s3"
	}
	if cfg.CatalogBackend != "s3" && cfg.CatalogBackend != "sqlite" {
		return Config{}, fmt.Errorf("CATALOG_BACKEND must be s3 or sqlite")
	}
	if cfg.CatalogBackend == "sqlite" && strings.TrimSpace(cfg.SQLitePath) == "" {
		cfg.SQLitePath = "data/nx-cache.db"
	}
	if cfg.UIPassword == "" {
		return Config{}, fmt.Errorf("UI_PASSWORD is required")
	}
	if strings.TrimSpace(cfg.SessionSecret) == "" {
		buf := make([]byte, 32)
		if _, err := rand.Read(buf); err != nil {
			return Config{}, fmt.Errorf("SESSION_SECRET: %w", err)
		}
		cfg.SessionSecret = hex.EncodeToString(buf)
		cfg.SessionSecretRandom = true
	}
	if cfg.CacheTTL <= 0 {
		return Config{}, fmt.Errorf("CACHE_TTL must be positive")
	}
	if cfg.CatalogFlush < 0 {
		return Config{}, fmt.Errorf("CATALOG_FLUSH_INTERVAL cannot be negative")
	}
	return cfg, nil
}

func env(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func envBool(key string, fallback bool) (bool, error) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("%s is invalid boolean %q", key, v)
	}
	return b, nil
}

func envDuration(key string, fallback time.Duration) (time.Duration, error) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s is invalid duration %q", key, v)
	}
	return d, nil
}

func envInt64(key string, fallback int64) (int64, error) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s is invalid integer %q", key, v)
	}
	return n, nil
}
