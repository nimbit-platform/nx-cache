package store

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nimbit-platform/nx-cache/internal/storage"
)

type mockNotifier struct {
	count atomic.Int64
}

func (m *mockNotifier) Notify() {
	m.count.Add(1)
}

func TestNotifyingStore(t *testing.T) {
	mem := storage.NewMemory()
	base := NewObject(mem)
	n := &mockNotifier{}
	s := NewNotifying(base, n)

	ctx := context.Background()

	// 1. UpsertEntry
	if err := s.UpsertEntry(ctx, "hash1", 100, time.Now(), TaskInfo{Project: "proj", Target: "build"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if n.count.Load() != 1 {
		t.Fatalf("expected 1 notification after upsert, got %d", n.count.Load())
	}

	// 2. RecordHit
	if err := s.RecordHit(ctx, "hash1", time.Now()); err != nil {
		t.Fatalf("hit: %v", err)
	}
	if n.count.Load() != 2 {
		t.Fatalf("expected 2 notifications after hit, got %d", n.count.Load())
	}

	// 3. RecordMiss
	if err := s.RecordMiss(ctx, time.Now()); err != nil {
		t.Fatalf("miss: %v", err)
	}
	if n.count.Load() != 3 {
		t.Fatalf("expected 3 notifications after miss, got %d", n.count.Load())
	}

	// 4. EnsureEntry
	if err := s.EnsureEntry(ctx, "hash2", 200, time.Now()); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if n.count.Load() != 4 {
		t.Fatalf("expected 4 notifications after ensure, got %d", n.count.Load())
	}

	// 5. Delete empty -> should not notify
	if err := s.Delete(ctx, nil); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if n.count.Load() != 4 {
		t.Fatalf("expected 4 notifications after empty delete, got %d", n.count.Load())
	}

	// 6. Delete with items -> should notify
	if err := s.Delete(ctx, []string{"hash1"}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if n.count.Load() != 5 {
		t.Fatalf("expected 5 notifications after delete, got %d", n.count.Load())
	}

	// 7. Seed
	if err := s.Seed(Entry{Hash: "hash3", Size: 300}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if n.count.Load() != 6 {
		t.Fatalf("expected 6 notifications after seed, got %d", n.count.Load())
	}

	// 8. Read operations (List, Stats) -> should not notify
	_, _, err := s.List(ctx, "", 10, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	_, err = s.Stats(ctx)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if n.count.Load() != 6 {
		t.Fatalf("expected count 6 after reads, got %d", n.count.Load())
	}
}
