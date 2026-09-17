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
	t.Setenv("CATALOG_BACKEND", "")
	t.Setenv("SQLITE_PATH", "")
	t.Setenv("CATALOG_FLUSH_INTERVAL", "")
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
	if cfg.CatalogBackend != "s3" {
		t.Fatalf("catalog %s", cfg.CatalogBackend)
	}
	if cfg.CatalogFlush != 30*time.Second {
		t.Fatalf("flush %s", cfg.CatalogFlush)
	}
	if cfg.SQLitePath != "" {
		t.Fatalf("sqlite should be off by default, got %s", cfg.SQLitePath)
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

func TestLoadSQLiteCatalog(t *testing.T) {
	t.Setenv("NX_CACHE_ACCESS_TOKEN", "tok")
	t.Setenv("UI_PASSWORD", "pw")
	t.Setenv("S3_BUCKET_NAME", "bucket")
	t.Setenv("CATALOG_BACKEND", "sqlite")
	t.Setenv("SQLITE_PATH", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CatalogBackend != "sqlite" || cfg.SQLitePath != "data/nx-cache.db" {
		t.Fatalf("catalog=%s path=%s", cfg.CatalogBackend, cfg.SQLitePath)
	}
}
