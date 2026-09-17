package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestMemoryPutGetDeleteList(t *testing.T) {
	m := NewMemory()
	ctx := context.Background()
	if err := m.Delete(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if err := m.Put(ctx, "h1", strings.NewReader("abc"), 3); err != nil {
		t.Fatal(err)
	}
	if err := m.Put(ctx, "h1", strings.NewReader("abc"), 3); !errors.Is(err, ErrExists) {
		t.Fatalf("overwrite: %v", err)
	}
	ok, err := m.Exists(ctx, "h1")
	if err != nil || !ok {
		t.Fatalf("exists: %v %v", ok, err)
	}
	body, size, err := m.Get(ctx, "h1")
	if err != nil || size != 3 {
		t.Fatal(err, size)
	}
	got := new(bytes.Buffer)
	_, _ = got.ReadFrom(body)
	_ = body.Close()
	if got.String() != "abc" {
		t.Fatalf("got %q", got.String())
	}

	hashes := []string{"h1"}
	for i := 0; i < 1004; i++ {
		h := fmt.Sprintf("n%d", i)
		if err := m.Put(ctx, h, strings.NewReader("z"), 1); err != nil {
			t.Fatal(err)
		}
		hashes = append(hashes, h)
	}
	if err := m.Delete(ctx, hashes); err != nil {
		t.Fatal(err)
	}
	list, err := m.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("want empty after batch delete, got %d", len(list))
	}
}

func TestIsNotFoundRejectsSubstring(t *testing.T) {
	if isNotFound(errors.New("upstream 404 from proxy for bucket-404")) {
		t.Fatal("error text containing 404 must not be treated as a cache miss")
	}
	if isNotFound(errors.New("NotFound in the middle of a timeout")) {
		t.Fatal("substring NotFound must not be treated as a cache miss")
	}
	if isPreconditionFailed(errors.New("something else")) {
		t.Fatal("unrelated error")
	}
}
