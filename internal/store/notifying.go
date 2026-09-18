package store

import (
	"context"
	"time"
)

// Notifier receives notifications when store data changes.
type Notifier interface {
	Notify()
}

// NotifyingStore wraps any Store implementation and automatically calls
// Notifier.Notify on any mutation that succeeds.
type NotifyingStore struct {
	Store
	notifier Notifier
}

// NewNotifying wraps s so that any successful mutation triggers n.Notify.
// If n is nil, no notifications are emitted.
func NewNotifying(s Store, n Notifier) *NotifyingStore {
	return &NotifyingStore{
		Store:    s,
		notifier: n,
	}
}

func (n *NotifyingStore) notify() {
	if n.notifier != nil {
		n.notifier.Notify()
	}
}

func (n *NotifyingStore) UpsertEntry(ctx context.Context, hash string, size int64, at time.Time, info TaskInfo) error {
	err := n.Store.UpsertEntry(ctx, hash, size, at, info)
	if err == nil {
		n.notify()
	}
	return err
}

func (n *NotifyingStore) RecordHit(ctx context.Context, hash string, at time.Time) error {
	err := n.Store.RecordHit(ctx, hash, at)
	if err == nil {
		n.notify()
	}
	return err
}

func (n *NotifyingStore) RecordMiss(ctx context.Context, at time.Time) error {
	err := n.Store.RecordMiss(ctx, at)
	if err == nil {
		n.notify()
	}
	return err
}

func (n *NotifyingStore) Delete(ctx context.Context, hashes []string) error {
	err := n.Store.Delete(ctx, hashes)
	if err == nil && len(hashes) > 0 {
		n.notify()
	}
	return err
}

func (n *NotifyingStore) EnsureEntry(ctx context.Context, hash string, size int64, at time.Time) error {
	err := n.Store.EnsureEntry(ctx, hash, size, at)
	if err == nil {
		n.notify()
	}
	return err
}

func (n *NotifyingStore) Seed(e Entry) error {
	err := n.Store.Seed(e)
	if err == nil {
		n.notify()
	}
	return err
}
