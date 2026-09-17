package cleanup

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nimbit-platform/nx-cache/internal/storage"
	"github.com/nimbit-platform/nx-cache/internal/store"
)

func TestRunDeletesOlderThanTTL(t *testing.T) {
	mem := storage.NewMemory()
	cat := store.NewObject(mem)
	ctx := context.Background()
	now := time.Now()
	if err := mem.Put(ctx, "old", strings.NewReader("old"), 3); err != nil {
		t.Fatal(err)
	}
	if err := mem.Put(ctx, "new", strings.NewReader("new"), 3); err != nil {
		t.Fatal(err)
	}
	if err := cat.Seed(store.Entry{Hash: "old", Size: 3, CreatedAt: now.Add(-48 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := cat.Seed(store.Entry{Hash: "new", Size: 3, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	c := &Cleaner{Backend: mem, Store: cat, TTL: 24 * time.Hour}
	n, err := c.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("deleted %d", n)
	}
	if ok, _ := mem.Exists(ctx, "old"); ok {
		t.Fatal("old should be gone")
	}
	if ok, _ := mem.Exists(ctx, "new"); !ok {
		t.Fatal("new should remain")
	}
}

func TestRunEmpty(t *testing.T) {
	mem := storage.NewMemory()
	cat := store.NewObject(mem)
	c := &Cleaner{Backend: mem, Store: cat, TTL: time.Hour}
	n, err := c.Run(context.Background())
	if err != nil || n != 0 {
		t.Fatalf("n=%d err=%v", n, err)
	}
}

func TestMaybeRunDoesNotOverlap(t *testing.T) {
	mem := storage.NewMemory()
	cat := store.NewObject(mem)
	var mu sync.Mutex
	running := 0
	max := 0
	slow := &slowBackend{Memory: mem, onList: func() {
		mu.Lock()
		running++
		if running > max {
			max = running
		}
		mu.Unlock()
		time.Sleep(40 * time.Millisecond)
		mu.Lock()
		running--
		mu.Unlock()
	}}
	c := &Cleaner{Backend: slow, Store: cat, TTL: time.Hour, minGap: time.Minute}
	ctx := context.Background()
	c.MaybeRun(ctx)
	c.MaybeRun(ctx)
	c.MaybeRun(ctx)
	time.Sleep(120 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if max > 1 {
		t.Fatalf("overlapping cleanups: max %d", max)
	}
}

type slowBackend struct {
	*storage.Memory
	onList func()
}

func (s *slowBackend) List(ctx context.Context) ([]storage.Object, error) {
	if s.onList != nil {
		s.onList()
	}
	return s.Memory.List(ctx)
}

func TestRunUsesCreateTimeNotAccess(t *testing.T) {
	mem := storage.NewMemory()
	cat := store.NewObject(mem)
	ctx := context.Background()
	now := time.Now()
	if err := mem.Put(ctx, "stale", strings.NewReader("xx"), 2); err != nil {
		t.Fatal(err)
	}
	if err := cat.Seed(store.Entry{
		Hash:           "stale",
		Size:           2,
		CreatedAt:      now.Add(-48 * time.Hour),
		LastAccessedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	c := &Cleaner{Backend: mem, Store: cat, TTL: 24 * time.Hour}
	n, err := c.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("create-time TTL should still expire recently accessed artifacts, deleted %d", n)
	}
}
