package config

import (
	"net"
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
	t.Setenv("SESSION_SECRET", "")
	t.Setenv("SESSION_SECURE", "")
	t.Setenv("TRUST_FORWARDED_IP", "")
	t.Setenv("ALLOW_IPS", "")
	t.Setenv("RATE_LIMIT_RPS", "")
	t.Setenv("RATE_LIMIT_BURST", "")
	t.Setenv("LOG_FORMAT", "")
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
	if !cfg.SessionSecure {
		t.Fatal("SESSION_SECURE should default to true")
	}
	if cfg.SessionSecret == "" || cfg.SessionSecret == "tok:pw" || !cfg.SessionSecretRandom {
		t.Fatalf("expected ephemeral session secret, got %q", cfg.SessionSecret)
	}
	if cfg.LogFormat != "text" {
		t.Fatalf("log format %s", cfg.LogFormat)
	}
	if cfg.LogLevel != "info" {
		t.Fatalf("log level %s", cfg.LogLevel)
	}
	if cfg.TrustForwardedIP {
		t.Fatal("must not trust forwarded IP by default")
	}
	if len(cfg.AllowNets) != 0 || cfg.RateLimitRPS != 0 {
		t.Fatal("access controls should be off by default")
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

func TestLoadInvalidLogLevel(t *testing.T) {
	t.Setenv("NX_CACHE_ACCESS_TOKEN", "tok")
	t.Setenv("UI_PASSWORD", "pw")
	t.Setenv("S3_BUCKET_NAME", "bucket")
	t.Setenv("LOG_LEVEL", "verbose")
	if _, err := Load(); err == nil {
		t.Fatal("expected invalid LOG_LEVEL to fail")
	}
	t.Setenv("LOG_LEVEL", "DEBUG")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LogLevel != "debug" {
		t.Fatalf("level %s", cfg.LogLevel)
	}
}

func TestLoadInvalidDuration(t *testing.T) {
	t.Setenv("NX_CACHE_ACCESS_TOKEN", "tok")
	t.Setenv("UI_PASSWORD", "pw")
	t.Setenv("S3_BUCKET_NAME", "bucket")
	t.Setenv("CACHE_TTL", "120hh")
	if _, err := Load(); err == nil {
		t.Fatal("expected invalid CACHE_TTL to fail")
	}
}

func TestParseIPNets(t *testing.T) {
	nets, err := ParseIPNets("10.0.0.0/8, 192.168.1.4, 2001:db8::/32")
	if err != nil || len(nets) != 3 {
		t.Fatalf("%v %v", nets, err)
	}
	if !nets[0].Contains(net.ParseIP("10.9.8.7")) || nets[0].Contains(net.ParseIP("11.0.0.1")) {
		t.Fatalf("cidr %s", nets[0])
	}
	if !nets[1].Contains(net.ParseIP("192.168.1.4")) || nets[1].Contains(net.ParseIP("192.168.1.5")) {
		t.Fatalf("host %s", nets[1])
	}
	if _, err := ParseIPNets("not-an-ip"); err == nil {
		t.Fatal("expected invalid IP")
	}
}

func TestLoadAllowIPsAndRateLimit(t *testing.T) {
	t.Setenv("NX_CACHE_ACCESS_TOKEN", "tok")
	t.Setenv("UI_PASSWORD", "pw")
	t.Setenv("S3_BUCKET_NAME", "bucket")
	t.Setenv("ALLOW_IPS", "10.0.0.0/8")
	t.Setenv("RATE_LIMIT_RPS", "12.5")
	t.Setenv("RATE_LIMIT_BURST", "30")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.AllowNets) != 1 || cfg.RateLimitRPS != 12.5 || cfg.RateLimitBurst != 30 {
		t.Fatalf("%+v", cfg)
	}
}
