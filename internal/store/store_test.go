package store

import (
	"context"
	"testing"
	"time"
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
