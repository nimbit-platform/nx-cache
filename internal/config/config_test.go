package config

import (
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	t.Setenv("NX_CACHE_ACCESS_TOKEN", "tok")
	t.Setenv("UI_PASSWORD", "pw")
	t.Setenv("S3_BUCKET_NAME", "bucket")
	t.Setenv("AWS_ACCESS_KEY_ID", "")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "")
	t.Setenv("S3_ENDPOINT_URL", "")
	t.Setenv("NX_SELF_HOSTED_REMOTE_CACHE_ACCESS_TOKEN", "")
	t.Setenv("STORAGE_BACKEND", "")
	t.Setenv("CACHE_TTL", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CacheTTL != 5*24*time.Hour {
		t.Fatalf("ttl %s", cfg.CacheTTL)
	}
	if cfg.StorageBackend != "s3" {
		t.Fatalf("backend %s", cfg.StorageBackend)
	}
	if cfg.Port != "8080" {
		t.Fatalf("port %s", cfg.Port)
	}
}

func TestLoadMemorySkipsBucket(t *testing.T) {
	t.Setenv("NX_CACHE_ACCESS_TOKEN", "tok")
	t.Setenv("UI_PASSWORD", "pw")
	t.Setenv("STORAGE_BACKEND", "memory")
	t.Setenv("S3_BUCKET_NAME", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.StorageBackend != "memory" {
		t.Fatalf("%s", cfg.StorageBackend)
	}
}
