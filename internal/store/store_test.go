package store

import (
	"context"
	"errors"
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
	if err := db.UpsertEntry(ctx, "aaa", 10, now, TaskInfo{}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertEntry(ctx, "bbb", 20, now, TaskInfo{}); err != nil {
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
	if err := db.UpsertEntry(ctx, "ccc", 5, now, TaskInfo{Project: "web", Target: "lint", Kind: "lint"}); err != nil {
		t.Fatal(err)
	}
	linted, n, err := db.List(ctx, "lint", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 || linted[0].Label() != "web:lint" {
		t.Fatalf("task filter %d %+v", n, linted)
	}
	entries, total, err := db.List(ctx, "aa", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(entries) != 1 || entries[0].Hash != "aaa" || entries[0].Hits != 1 {
		t.Fatalf("%d %+v", total, entries)
	}
	if err := db.UpdateTaskInfo(ctx, "aaa", TaskInfo{Project: "app", Target: "tsc", Kind: "typecheck"}); err != nil {
		t.Fatal(err)
	}
	updated, _, err := db.List(ctx, "", 10, 0)
	var task Entry
	for _, entry := range updated {
		if entry.Hash == "aaa" {
			task = entry
		}
	}
	if err != nil || task.Label() != "app:tsc" || task.Kind != "typecheck" {
		t.Fatalf("updated task: %v %+v", err, updated)
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
	if err := cat.UpsertEntry(ctx, "aaa", 10, now, TaskInfo{}); err != nil {
		t.Fatal(err)
	}
	if err := cat.UpsertEntry(ctx, "bbb", 20, now, TaskInfo{}); err != nil {
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

func TestObjectCatalogFlush(t *testing.T) {
	mem := storage.NewMemory()
	ctx := context.Background()
	now := time.Now()
	if err := mem.Put(ctx, "aaa", strings.NewReader("helloworld"), 10); err != nil {
		t.Fatal(err)
	}
	cat := NewObject(mem)
	if err := cat.UpsertEntry(ctx, "aaa", 10, now, TaskInfo{}); err != nil {
		t.Fatal(err)
	}
	if err := cat.RecordHit(ctx, "aaa", now); err != nil {
		t.Fatal(err)
	}
	if _, err := mem.GetMeta(ctx, catalogMetaKey); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("expected no snapshot before flush, got %v", err)
	}
	if err := cat.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	loaded := NewObject(mem)
	if err := loaded.Load(ctx); err != nil {
		t.Fatal(err)
	}
	stats, err := loaded.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Hits != 1 || stats.Stores != 1 || stats.Entries != 1 {
		t.Fatalf("reloaded %+v", stats)
	}
}

func TestObjectCatalogFlushInterval(t *testing.T) {
	mem := storage.NewMemory()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cat := NewObject(mem, ObjectOptions{FlushInterval: 20 * time.Millisecond})
	cat.Start(ctx)
	if err := mem.Put(ctx, "zzz", strings.NewReader("payloadxx"), 9); err != nil {
		t.Fatal(err)
	}
	if err := cat.UpsertEntry(ctx, "zzz", 9, time.Now(), TaskInfo{}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		_, err := mem.GetMeta(ctx, catalogMetaKey)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("catalog was not flushed on interval")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := cat.Close(); err != nil {
		t.Fatal(err)
	}
}

type errMeta struct {
	storage.Backend
	getErr error
	lists  int
}

func (e *errMeta) GetMeta(ctx context.Context, key string) ([]byte, error) {
	if e.getErr != nil {
		return nil, e.getErr
	}
	return e.Backend.GetMeta(ctx, key)
}

func (e *errMeta) List(ctx context.Context) ([]storage.Object, error) {
	e.lists++
	return e.Backend.List(ctx)
}

func TestObjectFlushBlockedAfterLoadError(t *testing.T) {
	mem := storage.NewMemory()
	ctx := context.Background()
	backend := &errMeta{Backend: mem, getErr: errors.New("s3 timeout")}
	cat := NewObject(backend)
	if err := cat.Load(ctx); err == nil {
		t.Fatal("expected load error")
	}
	if err := cat.UpsertEntry(ctx, "aaa", 3, time.Now(), TaskInfo{}); err != nil {
		t.Fatal(err)
	}
	if err := cat.Flush(ctx); !errors.Is(err, ErrCatalogLoadFailed) {
		t.Fatalf("flush: %v", err)
	}
	if _, err := mem.GetMeta(ctx, catalogMetaKey); !errors.Is(err, storage.ErrNotFound) {
		t.Fatal("must not overwrite a snapshot after a failed load")
	}
}

func TestObjectListUsesMemoryNotBucketList(t *testing.T) {
	mem := storage.NewMemory()
	ctx := context.Background()
	backend := &errMeta{Backend: mem}
	cat := NewObject(backend)
	if err := mem.Put(ctx, "only-in-s3", strings.NewReader("hello"), 5); err != nil {
		t.Fatal(err)
	}
	if err := cat.UpsertEntry(ctx, "in-catalog", 4, time.Now(), TaskInfo{}); err != nil {
		t.Fatal(err)
	}
	entries, total, err := cat.List(ctx, "", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || entries[0].Hash != "in-catalog" {
		t.Fatalf("%d %+v", total, entries)
	}
	if backend.lists != 0 {
		t.Fatalf("dashboard list must not List the bucket, got %d", backend.lists)
	}
}
