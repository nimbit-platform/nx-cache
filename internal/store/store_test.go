package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nimbit-platform/nx-cache/internal/storage"
)

func TestStatsAndList(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	now := time.Now()
	if err := db.UpsertEntry(ctx, "aaa", 10, now); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertEntry(ctx, "bbb", 20, now); err != nil {
		t.Fatal(err)
	}
	if err := db.RecordHit(ctx, "aaa", now); err != nil {
		t.Fatal(err)
	}
	if err := db.RecordMiss(ctx, now); err != nil {
		t.Fatal(err)
	}
	stats, err := db.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Entries != 2 || stats.TotalSize != 30 || stats.Hits != 1 || stats.Misses != 1 || stats.Stores != 2 {
		t.Fatalf("%+v", stats)
	}
	if stats.HitRate() < 49 || stats.HitRate() > 51 {
		t.Fatalf("hit rate %f", stats.HitRate())
	}
	entries, total, err := db.List(ctx, "aa", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(entries) != 1 || entries[0].Hash != "aaa" || entries[0].Hits != 1 {
		t.Fatalf("%d %+v", total, entries)
	}
}

func TestObjectCatalog(t *testing.T) {
	mem := storage.NewMemory()
	cat := NewObject(mem)
	ctx := context.Background()
	now := time.Now()
	if err := mem.Put(ctx, "aaa", strings.NewReader("helloworld"), 10); err != nil {
		t.Fatal(err)
	}
	if err := mem.Put(ctx, "bbb", strings.NewReader("abcdefghijklmnopqrst"), 20); err != nil {
		t.Fatal(err)
	}
	if err := cat.UpsertEntry(ctx, "aaa", 10, now); err != nil {
		t.Fatal(err)
	}
	if err := cat.UpsertEntry(ctx, "bbb", 20, now); err != nil {
		t.Fatal(err)
	}
	if err := cat.RecordHit(ctx, "aaa", now); err != nil {
		t.Fatal(err)
	}
	if err := cat.RecordMiss(ctx, now); err != nil {
		t.Fatal(err)
	}
	stats, err := cat.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Entries != 2 || stats.TotalSize != 30 || stats.Hits != 1 || stats.Misses != 1 || stats.Stores != 2 {
		t.Fatalf("%+v", stats)
	}
	entries, total, err := cat.List(ctx, "aa", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(entries) != 1 || entries[0].Hash != "aaa" || entries[0].Hits != 1 {
		t.Fatalf("%d %+v", total, entries)
	}
}
