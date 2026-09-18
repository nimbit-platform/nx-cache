package longpoll

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestHubInitialStateAndNotify(t *testing.T) {
	hub := NewHub()
	defer hub.Close()

	s1 := hub.Snapshot()
	if s1.Version != 1 {
		t.Fatalf("expected initial version 1, got %d", s1.Version)
	}
	if s1.LastModified.IsZero() {
		t.Fatalf("expected non-zero LastModified")
	}
	if s1.ETag == "" {
		t.Fatalf("expected non-empty ETag")
	}

	hub.Notify()
	s2 := hub.Snapshot()
	if s2.Version != 2 {
		t.Fatalf("expected version 2, got %d", s2.Version)
	}
	if s2.LastModified.Before(s1.LastModified) {
		t.Fatalf("expected LastModified not to move backwards, got s1=%v, s2=%v", s1.LastModified, s2.LastModified)
	}
	if s2.ETag == s1.ETag {
		t.Fatalf("expected ETag to change after notify")
	}
}

func TestHubNotifyDoesNotFutureDate(t *testing.T) {
	hub := NewHub()
	for range 10 {
		hub.Notify()
	}

	if state := hub.Snapshot(); state.LastModified.After(time.Now().UTC().Truncate(time.Second)) {
		t.Fatalf("expected LastModified not to be in the future, got %v", state.LastModified)
	}
}

func TestHubIsFresh(t *testing.T) {
	hub := NewHub()
	defer hub.Close()
	state := hub.Snapshot()

	// 1. No headers -> not fresh
	req := httptest.NewRequest(http.MethodGet, "/ui/stats", nil)
	if hub.IsFresh(req, state) {
		t.Fatalf("expected false when no cache headers are present")
	}

	// 2. Matching ETag -> fresh
	req = httptest.NewRequest(http.MethodGet, "/ui/stats", nil)
	req.Header.Set("If-None-Match", state.ETag)
	if !hub.IsFresh(req, state) {
		t.Fatalf("expected true when ETag matches")
	}

	// 3. Matching weak ETag -> fresh
	req = httptest.NewRequest(http.MethodGet, "/ui/stats", nil)
	req.Header.Set("If-None-Match", "W/"+state.ETag)
	if !hub.IsFresh(req, state) {
		t.Fatalf("expected true when weak ETag matches")
	}

	// 4. Wildcard ETag -> fresh
	req = httptest.NewRequest(http.MethodGet, "/ui/stats", nil)
	req.Header.Set("If-None-Match", "*")
	if !hub.IsFresh(req, state) {
		t.Fatalf("expected true for wildcard ETag")
	}

	// 5. Stale ETag -> not fresh
	req = httptest.NewRequest(http.MethodGet, "/ui/stats", nil)
	req.Header.Set("If-None-Match", `"999-0"`)
	if hub.IsFresh(req, state) {
		t.Fatalf("expected false when ETag is stale")
	}

	// 6. Matching If-Modified-Since -> fresh
	req = httptest.NewRequest(http.MethodGet, "/ui/stats", nil)
	req.Header.Set("If-Modified-Since", state.LastModified.Format(http.TimeFormat))
	if !hub.IsFresh(req, state) {
		t.Fatalf("expected true when IMS matches")
	}

	// 7. Future If-Modified-Since -> fresh (not modified since that date)
	future := state.LastModified.Add(10 * time.Minute)
	req = httptest.NewRequest(http.MethodGet, "/ui/stats", nil)
	req.Header.Set("If-Modified-Since", future.Format(http.TimeFormat))
	if !hub.IsFresh(req, state) {
		t.Fatalf("expected true when IMS is in the future")
	}

	// 8. Stale If-Modified-Since -> not fresh
	past := state.LastModified.Add(-10 * time.Minute)
	req = httptest.NewRequest(http.MethodGet, "/ui/stats", nil)
	req.Header.Set("If-Modified-Since", past.Format(http.TimeFormat))
	if hub.IsFresh(req, state) {
		t.Fatalf("expected false when IMS is in the past")
	}

	// 9. Invalid date IMS -> not fresh
	req = httptest.NewRequest(http.MethodGet, "/ui/stats", nil)
	req.Header.Set("If-Modified-Since", "not-a-valid-date")
	if hub.IsFresh(req, state) {
		t.Fatalf("expected false when IMS date is invalid")
	}
}

func TestWaitOrModifiedImmediateWhenMissingOrStale(t *testing.T) {
	hub := NewHub()
	defer hub.Close()

	// Missing cache header resolves immediately
	req := httptest.NewRequest(http.MethodGet, "/ui/stats", nil)
	start := time.Now()
	mod, state, err := hub.WaitOrModified(req, 2*time.Second)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !mod {
		t.Fatalf("expected modified=true for cold request")
	}
	if time.Since(start) > 200*time.Millisecond {
		t.Fatalf("expected immediate return, took %v", time.Since(start))
	}
	if state.Version != 1 {
		t.Fatalf("expected version 1, got %d", state.Version)
	}

	// Stale cache header resolves immediately
	req = httptest.NewRequest(http.MethodGet, "/ui/stats", nil)
	req.Header.Set("If-None-Match", `"0-0"`)
	start = time.Now()
	mod, _, err = hub.WaitOrModified(req, 2*time.Second)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !mod {
		t.Fatalf("expected modified=true for stale cache header")
	}
	if time.Since(start) > 200*time.Millisecond {
		t.Fatalf("expected immediate return, took %v", time.Since(start))
	}
}

func TestWaitOrModifiedBlocksUntilNotify(t *testing.T) {
	hub := NewHub()
	defer hub.Close()
	state := hub.Snapshot()

	req := httptest.NewRequest(http.MethodGet, "/ui/stats", nil)
	req.Header.Set("If-None-Match", state.ETag)

	start := time.Now()
	var (
		mod      bool
		newState State
		err      error
	)

	done := make(chan struct{})
	go func() {
		mod, newState, err = hub.WaitOrModified(req, 5*time.Second)
		close(done)
	}()

	// Wait until subscriber is registered
	for i := 0; i < 50; i++ {
		if hub.SubscriberCount() > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if hub.SubscriberCount() == 0 {
		t.Fatalf("subscriber was not registered")
	}

	// Trigger notification
	time.Sleep(50 * time.Millisecond)
	hub.Notify()

	<-done

	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !mod {
		t.Fatalf("expected modified=true after notify")
	}
	if newState.Version != state.Version+1 {
		t.Fatalf("expected version %d, got %d", state.Version+1, newState.Version)
	}
	elapsed := time.Since(start)
	if elapsed < 40*time.Millisecond || elapsed > 2*time.Second {
		t.Fatalf("expected quick wake-up upon notify, elapsed %v", elapsed)
	}

	// Subscriber should be cleaned up
	if hub.SubscriberCount() != 0 {
		t.Fatalf("expected 0 subscribers after completion, got %d", hub.SubscriberCount())
	}
}

func TestWaitOrModifiedBlocksUntilTimeout(t *testing.T) {
	hub := NewHub()
	defer hub.Close()
	state := hub.Snapshot()

	req := httptest.NewRequest(http.MethodGet, "/ui/stats", nil)
	req.Header.Set("If-None-Match", state.ETag)

	start := time.Now()
	timeout := 100 * time.Millisecond
	mod, returnedState, err := hub.WaitOrModified(req, timeout)

	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if mod {
		t.Fatalf("expected modified=false on timeout")
	}
	if returnedState.Version != state.Version {
		t.Fatalf("expected state unchanged")
	}
	if time.Since(start) < timeout {
		t.Fatalf("expected to wait at least %v, took %v", timeout, time.Since(start))
	}
	if hub.SubscriberCount() != 0 {
		t.Fatalf("expected 0 subscribers after timeout, got %d", hub.SubscriberCount())
	}
}

func TestWaitOrModifiedContextCancellation(t *testing.T) {
	hub := NewHub()
	defer hub.Close()
	state := hub.Snapshot()

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/ui/stats", nil).WithContext(ctx)
	req.Header.Set("If-None-Match", state.ETag)

	done := make(chan struct{})
	var (
		mod bool
		err error
	)
	go func() {
		mod, _, err = hub.WaitOrModified(req, 5*time.Second)
		close(done)
	}()

	for i := 0; i < 50; i++ {
		if hub.SubscriberCount() > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	cancel()
	<-done

	if err != context.Canceled {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if mod {
		t.Fatalf("expected modified=false on cancellation")
	}
	if hub.SubscriberCount() != 0 {
		t.Fatalf("expected 0 subscribers after cancel, got %d", hub.SubscriberCount())
	}
}

func TestConcurrentSubscribersAndBroadcast(t *testing.T) {
	hub := NewHub()
	defer hub.Close()
	state := hub.Snapshot()

	const numSubscribers = 20
	var wg sync.WaitGroup
	wg.Add(numSubscribers)

	results := make([]bool, numSubscribers)

	for i := 0; i < numSubscribers; i++ {
		go func(idx int) {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodGet, "/ui/stats", nil)
			req.Header.Set("If-None-Match", state.ETag)
			mod, _, err := hub.WaitOrModified(req, 5*time.Second)
			if err != nil {
				t.Errorf("subscriber %d error: %v", idx, err)
			}
			results[idx] = mod
		}(i)
	}

	// Wait until all subscribers are active
	for i := 0; i < 100; i++ {
		if hub.SubscriberCount() == numSubscribers {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if hub.SubscriberCount() != numSubscribers {
		t.Fatalf("expected %d subscribers, got %d", numSubscribers, hub.SubscriberCount())
	}

	hub.Notify()
	wg.Wait()

	for idx, mod := range results {
		if !mod {
			t.Errorf("subscriber %d did not receive modified=true", idx)
		}
	}
	if hub.SubscriberCount() != 0 {
		t.Fatalf("expected 0 subscribers after all resolved, got %d", hub.SubscriberCount())
	}
}

func TestSetCacheHeaders(t *testing.T) {
	rec := httptest.NewRecorder()
	state := State{
		Version:      5,
		LastModified: time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC),
		ETag:         `"5-1726653600"`,
	}
	SetCacheHeaders(rec, state)

	if rec.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("expected Cache-Control: no-cache, got %s", rec.Header().Get("Cache-Control"))
	}
	if rec.Header().Get("ETag") != state.ETag {
		t.Fatalf("expected ETag: %s, got %s", state.ETag, rec.Header().Get("ETag"))
	}
	expectedLM := state.LastModified.Format(http.TimeFormat)
	if rec.Header().Get("Last-Modified") != expectedLM {
		t.Fatalf("expected Last-Modified: %s, got %s", expectedLM, rec.Header().Get("Last-Modified"))
	}
}
