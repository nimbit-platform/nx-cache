package longpoll

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// State captures a snapshot of the Hub's version and timestamp.
type State struct {
	Version      uint64
	LastModified time.Time
	ETag         string
}

// Hub tracks dynamic application state updates and coordinates long-polling
// requests across goroutines using Go channels.
type Hub struct {
	mu          sync.RWMutex
	version     uint64
	updatedAt   time.Time
	subscribers map[chan struct{}]struct{}
	closed      bool
}

// NewHub initializes and returns a new Hub with an initial version and timestamp.
func NewHub() *Hub {
	now := time.Now().UTC().Truncate(time.Second)
	return &Hub{
		version:     1,
		updatedAt:   now,
		subscribers: make(map[chan struct{}]struct{}),
	}
}

// Notify increments the internal version, updates the last modified timestamp
// monotonically, and broadcasts a notification to all standing long-poll requests
// via their subscriber channels.
func (h *Hub) Notify() {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.closed {
		return
	}

	h.version++
	now := time.Now().UTC().Truncate(time.Second)
	if now.After(h.updatedAt) {
		h.updatedAt = now
	}

	for ch := range h.subscribers {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// Snapshot returns the current State under a read lock.
func (h *Hub) Snapshot() State {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.snapshotLocked()
}

func (h *Hub) snapshotLocked() State {
	return State{
		Version:      h.version,
		LastModified: h.updatedAt,
		ETag:         fmt.Sprintf(`"%d-%d"`, h.version, h.updatedAt.Unix()),
	}
}

// IsFresh evaluates whether the incoming HTTP request's cache validation headers
// (If-None-Match or If-Modified-Since) indicate that the client has the current state.
// If the client sent no cache headers, it is not fresh.
func (h *Hub) IsFresh(r *http.Request, state State) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.isFreshLocked(r, state)
}

func (h *Hub) isFreshLocked(r *http.Request, state State) bool {
	inm := r.Header.Get("If-None-Match")
	ims := r.Header.Get("If-Modified-Since")

	// If no conditional headers were sent, client has no cached representation.
	if inm == "" && ims == "" {
		return false
	}

	// RFC 9110: If-None-Match takes precedence when present.
	if inm != "" {
		return etagMatches(inm, state.ETag)
	}

	// Validate If-Modified-Since
	imsTime, err := http.ParseTime(ims)
	if err != nil {
		return false
	}

	// If the server resource was not modified after IMS, it is fresh.
	return !state.LastModified.After(imsTime)
}

// WaitOrModified checks if the client's request is already stale or missing cache headers.
// If stale or missing, it resolves immediately with (true, state, nil).
// If the client's cached state matches the current internal state, it blocks for up to
// timeout waiting for a change notification via a Go channel.
//
// If a notification arrives, it returns (true, newState, nil).
// If timeout expires with no change, it returns (false, state, nil).
// If the request context is canceled, it returns (false, state, ctx.Err()).
func (h *Hub) WaitOrModified(r *http.Request, timeout time.Duration) (bool, State, error) {
	h.mu.Lock()
	state := h.snapshotLocked()
	fresh := h.isFreshLocked(r, state)
	if !fresh {
		h.mu.Unlock()
		return true, state, nil
	}

	ch := make(chan struct{}, 1)
	h.subscribers[ch] = struct{}{}
	h.mu.Unlock()

	defer func() {
		h.mu.Lock()
		delete(h.subscribers, ch)
		h.mu.Unlock()
	}()

	effectiveTimeout := timeout
	if deadline, ok := r.Context().Deadline(); ok {
		remaining := time.Until(deadline) - 100*time.Millisecond
		if remaining > 0 && remaining < effectiveTimeout {
			effectiveTimeout = remaining
		}
	}

	timer := time.NewTimer(effectiveTimeout)
	defer timer.Stop()

	select {
	case <-ch:
		h.mu.RLock()
		newState := h.snapshotLocked()
		h.mu.RUnlock()
		return true, newState, nil

	case <-timer.C:
		return false, state, nil

	case <-r.Context().Done():
		return false, state, r.Context().Err()
	}
}

// Close closes the Hub and unblocks any standing subscriber channels.
func (h *Hub) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil
	}
	h.closed = true
	for ch := range h.subscribers {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	return nil
}

// SubscriberCount returns the current number of standing subscribers (useful for testing/metrics).
func (h *Hub) SubscriberCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.subscribers)
}

// SetCacheHeaders sets standard browser cache headers for conditional revalidation:
// Cache-Control: no-cache (instructs browser to revalidate before each reuse)
// ETag: matching current representation state
// Last-Modified: HTTP-date format
func SetCacheHeaders(w http.ResponseWriter, state State) {
	header := w.Header()
	header.Set("Cache-Control", "no-cache")
	header.Set("ETag", state.ETag)
	header.Set("Last-Modified", state.LastModified.Format(http.TimeFormat))
}

// etagMatches performs RFC-compliant weak and strong ETag comparison.
func etagMatches(clientHeader, serverETag string) bool {
	clientHeader = strings.TrimSpace(clientHeader)
	if clientHeader == "*" {
		return true
	}
	serverTrimmed := strings.TrimPrefix(strings.TrimSpace(serverETag), "W/")
	for _, part := range strings.Split(clientHeader, ",") {
		part = strings.TrimSpace(part)
		partTrimmed := strings.TrimPrefix(part, "W/")
		if partTrimmed == serverTrimmed {
			return true
		}
	}
	return false
}
