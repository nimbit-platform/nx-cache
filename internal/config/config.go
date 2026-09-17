package config

import (
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

	UIUsername    string
	UIPassword    string
	SessionSecret string

	SQLitePath string

	StorageBackend string

	CacheTTL        time.Duration
	CleanupInterval time.Duration
	CleanupOnSave   bool
	MaxUploadBytes  int64
}

func Load() (Config, error) {
	cfg := Config{
		Port:               env("PORT", "8080"),
		LogLevel:           env("LOG_LEVEL", "info"),
		AWSRegion:          env("AWS_REGION", "us-east-1"),
		AWSAccessKeyID:     os.Getenv("AWS_ACCESS_KEY_ID"),
		AWSSecretAccessKey: os.Getenv("AWS_SECRET_ACCESS_KEY"),
		S3Bucket:           env("S3_BUCKET_NAME", "nx-cache"),
		S3Endpoint:         os.Getenv("S3_ENDPOINT_URL"),
		S3Prefix:           env("S3_PREFIX", "nx-cache/"),
		S3ForcePathStyle:   envBool("S3_FORCE_PATH_STYLE", os.Getenv("S3_ENDPOINT_URL") != ""),
		CreateBucket:       envBool("S3_CREATE_BUCKET", false),
		AccessToken:        env("NX_CACHE_ACCESS_TOKEN", os.Getenv("NX_SELF_HOSTED_REMOTE_CACHE_ACCESS_TOKEN")),
		ReadToken:          os.Getenv("NX_CACHE_READ_TOKEN"),
		UIUsername:         env("UI_USERNAME", "admin"),
		UIPassword:         os.Getenv("UI_PASSWORD"),
		SessionSecret:      os.Getenv("SESSION_SECRET"),
		SQLitePath:         env("SQLITE_PATH", "data/nx-cache.db"),
		StorageBackend:     strings.ToLower(env("STORAGE_BACKEND", "s3")),
		CacheTTL:           envDuration("CACHE_TTL", 5*24*time.Hour),
		CleanupInterval:    envDuration("CLEANUP_INTERVAL", time.Hour),
		CleanupOnSave:      envBool("CLEANUP_ON_SAVE", true),
		MaxUploadBytes:     envInt64("MAX_UPLOAD_BYTES", 2*1024*1024*1024),
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
	if cfg.UIPassword == "" {
		return Config{}, fmt.Errorf("UI_PASSWORD is required")
	}
	if cfg.SessionSecret == "" {
		cfg.SessionSecret = cfg.AccessToken + ":" + cfg.UIPassword
	}
	if cfg.CacheTTL <= 0 {
		return Config{}, fmt.Errorf("CACHE_TTL must be positive")
	}
	return cfg, nil
}

func env(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return fallback
	}
	return b
}

func envDuration(key string, fallback time.Duration) time.Duration {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return fallback
	}
	return d
}

func envInt64(key string, fallback int64) int64 {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return fallback
	}
	return n
}
