package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type Entry struct {
	Hash           string    `json:"hash"`
	Size           int64     `json:"size"`
	CreatedAt      time.Time `json:"created_at"`
	LastAccessedAt time.Time `json:"last_accessed_at"`
	Hits           int64     `json:"hits"`
}

type DayStat struct {
	Day    string
	Hits   int64
	Misses int64
	Stores int64
}

type Stats struct {
	Entries   int64
	TotalSize int64
	Hits      int64
	Misses    int64
	Stores    int64
	Days      []DayStat
}

func (s Stats) HitRate() float64 {
	total := s.Hits + s.Misses
	if total == 0 {
		return 0
	}
	return float64(s.Hits) / float64(total) * 100
}

// Store is the artifact catalog and hit/miss counters.
// The default implementation keeps this in the S3 bucket; SQLite is optional.
type Store interface {
	UpsertEntry(ctx context.Context, hash string, size int64, at time.Time) error
	RecordHit(ctx context.Context, hash string, at time.Time) error
	RecordMiss(ctx context.Context, at time.Time) error
	List(ctx context.Context, query string, limit, offset int) ([]Entry, int, error)
	ListOlderThan(ctx context.Context, cutoff time.Time) ([]Entry, error)
	Delete(ctx context.Context, hashes []string) error
	Stats(ctx context.Context) (Stats, error)
	EnsureEntry(ctx context.Context, hash string, size int64, at time.Time) error
	Seed(e Entry) error
	Close() error
}

type DB struct {
	sql *sql.DB
}

func Open(path string) (*DB, error) {
	dsn := path
	if path == ":memory:" || path == "" {
		var b [8]byte
		_, _ = rand.Read(b[:])
		dsn = "file:nxcache-" + hex.EncodeToString(b[:]) + "?mode=memory&cache=shared"
	} else {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil && filepath.Dir(path) != "." {
			return nil, fmt.Errorf("create sqlite dir: %w", err)
		}
		dsn = "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)"
	}
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxOpenConns(1)
	db := &DB{sql: sqlDB}
	if err := db.migrate(); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	return db, nil
}

func (d *DB) Close() error { return d.sql.Close() }

func (d *DB) migrate() error {
	_, err := d.sql.Exec(`
CREATE TABLE IF NOT EXISTS cache_entries (
  hash TEXT PRIMARY KEY,
  size INTEGER NOT NULL,
  created_at INTEGER NOT NULL,
  last_accessed_at INTEGER NOT NULL,
  hits INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_cache_created_at ON cache_entries(created_at);
CREATE TABLE IF NOT EXISTS counters (
  key TEXT PRIMARY KEY,
  value INTEGER NOT NULL DEFAULT 0
);
INSERT OR IGNORE INTO counters(key, value) VALUES ('hits', 0), ('misses', 0), ('stores', 0);
CREATE TABLE IF NOT EXISTS daily_stats (
  day TEXT PRIMARY KEY,
  hits INTEGER NOT NULL DEFAULT 0,
  misses INTEGER NOT NULL DEFAULT 0,
  stores INTEGER NOT NULL DEFAULT 0
);
`)
	return err
}

func (d *DB) UpsertEntry(ctx context.Context, hash string, size int64, at time.Time) error {
	unix := at.Unix()
	_, err := d.sql.ExecContext(ctx, `
INSERT INTO cache_entries(hash, size, created_at, last_accessed_at, hits)
VALUES (?, ?, ?, ?, 0)
ON CONFLICT(hash) DO UPDATE SET size = excluded.size
`, hash, size, unix, unix)
	if err != nil {
		return err
	}
	return d.bump(ctx, "stores", at)
}

func (d *DB) RecordHit(ctx context.Context, hash string, at time.Time) error {
	_, err := d.sql.ExecContext(ctx, `
UPDATE cache_entries SET hits = hits + 1, last_accessed_at = ? WHERE hash = ?
`, at.Unix(), hash)
	if err != nil {
		return err
	}
	return d.bump(ctx, "hits", at)
}

func (d *DB) RecordMiss(ctx context.Context, at time.Time) error {
	return d.bump(ctx, "misses", at)
}

func (d *DB) bump(ctx context.Context, key string, at time.Time) error {
	if _, err := d.sql.ExecContext(ctx, `UPDATE counters SET value = value + 1 WHERE key = ?`, key); err != nil {
		return err
	}
	day := at.UTC().Format("2006-01-02")
	var q string
	switch key {
	case "hits":
		q = `INSERT INTO daily_stats(day, hits, misses, stores) VALUES (?, 1, 0, 0)
ON CONFLICT(day) DO UPDATE SET hits = hits + 1`
	case "misses":
		q = `INSERT INTO daily_stats(day, hits, misses, stores) VALUES (?, 0, 1, 0)
ON CONFLICT(day) DO UPDATE SET misses = misses + 1`
	case "stores":
		q = `INSERT INTO daily_stats(day, hits, misses, stores) VALUES (?, 0, 0, 1)
ON CONFLICT(day) DO UPDATE SET stores = stores + 1`
	default:
		return fmt.Errorf("unknown counter %s", key)
	}
	_, err := d.sql.ExecContext(ctx, q, day)
	return err
}

func (d *DB) List(ctx context.Context, query string, limit, offset int) ([]Entry, int, error) {
	if limit <= 0 {
		limit = 25
	}
	args := []any{}
	where := ""
	if q := strings.TrimSpace(query); q != "" {
		where = "WHERE hash LIKE ?"
		args = append(args, "%"+q+"%")
	}
	var total int
	countArgs := append([]any{}, args...)
	if err := d.sql.QueryRowContext(ctx, "SELECT COUNT(*) FROM cache_entries "+where, countArgs...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, limit, offset)
	rows, err := d.sql.QueryContext(ctx, `
SELECT hash, size, created_at, last_accessed_at, hits
FROM cache_entries `+where+`
ORDER BY created_at DESC
LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var entries []Entry
	for rows.Next() {
		var e Entry
		var created, accessed int64
		if err := rows.Scan(&e.Hash, &e.Size, &created, &accessed, &e.Hits); err != nil {
			return nil, 0, err
		}
		e.CreatedAt = time.Unix(created, 0).UTC()
		e.LastAccessedAt = time.Unix(accessed, 0).UTC()
		entries = append(entries, e)
	}
	return entries, total, rows.Err()
}

func (d *DB) ListOlderThan(ctx context.Context, cutoff time.Time) ([]Entry, error) {
	rows, err := d.sql.QueryContext(ctx, `
SELECT hash, size, created_at, last_accessed_at, hits
FROM cache_entries WHERE created_at < ?
`, cutoff.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []Entry
	for rows.Next() {
		var e Entry
		var created, accessed int64
		if err := rows.Scan(&e.Hash, &e.Size, &created, &accessed, &e.Hits); err != nil {
			return nil, err
		}
		e.CreatedAt = time.Unix(created, 0).UTC()
		e.LastAccessedAt = time.Unix(accessed, 0).UTC()
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

func (d *DB) Delete(ctx context.Context, hashes []string) error {
	if len(hashes) == 0 {
		return nil
	}
	placeholders := make([]string, len(hashes))
	args := make([]any, len(hashes))
	for i, h := range hashes {
		placeholders[i] = "?"
		args[i] = h
	}
	_, err := d.sql.ExecContext(ctx, "DELETE FROM cache_entries WHERE hash IN ("+strings.Join(placeholders, ",")+")", args...)
	return err
}

func (d *DB) Stats(ctx context.Context) (Stats, error) {
	var s Stats
	err := d.sql.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(size), 0) FROM cache_entries`).Scan(&s.Entries, &s.TotalSize)
	if err != nil {
		return Stats{}, err
	}
	if err := d.sql.QueryRowContext(ctx, `SELECT value FROM counters WHERE key = 'hits'`).Scan(&s.Hits); err != nil {
		return Stats{}, err
	}
	if err := d.sql.QueryRowContext(ctx, `SELECT value FROM counters WHERE key = 'misses'`).Scan(&s.Misses); err != nil {
		return Stats{}, err
	}
	if err := d.sql.QueryRowContext(ctx, `SELECT value FROM counters WHERE key = 'stores'`).Scan(&s.Stores); err != nil {
		return Stats{}, err
	}
	since := time.Now().UTC().AddDate(0, 0, -6).Format("2006-01-02")
	rows, err := d.sql.QueryContext(ctx, `
SELECT day, hits, misses, stores FROM daily_stats WHERE day >= ? ORDER BY day ASC
`, since)
	if err != nil {
		return Stats{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var day DayStat
		if err := rows.Scan(&day.Day, &day.Hits, &day.Misses, &day.Stores); err != nil {
			return Stats{}, err
		}
		s.Days = append(s.Days, day)
	}
	return s, rows.Err()
}

func (d *DB) EnsureEntry(ctx context.Context, hash string, size int64, at time.Time) error {
	unix := at.Unix()
	_, err := d.sql.ExecContext(ctx, `
INSERT OR IGNORE INTO cache_entries(hash, size, created_at, last_accessed_at, hits)
VALUES (?, ?, ?, ?, 0)
`, hash, size, unix, unix)
	return err
}

func (d *DB) Seed(e Entry) error {
	_, err := d.sql.Exec(`
INSERT INTO cache_entries(hash, size, created_at, last_accessed_at, hits)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(hash) DO UPDATE SET
  size = excluded.size,
  created_at = excluded.created_at,
  last_accessed_at = excluded.last_accessed_at,
  hits = excluded.hits
`, e.Hash, e.Size, e.CreatedAt.Unix(), e.LastAccessedAt.Unix(), e.Hits)
	return err
}
