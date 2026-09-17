package cleanup

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/nimbit-platform/nx-cache/internal/storage"
	"github.com/nimbit-platform/nx-cache/internal/store"
)

type Cleaner struct {
	Backend  storage.Backend
	Store    *store.DB
	TTL      time.Duration
	Interval time.Duration
	Log      *slog.Logger

	mu      sync.Mutex
	lastRun time.Time
	minGap  time.Duration
}

func (c *Cleaner) logger() *slog.Logger {
	if c.Log != nil {
		return c.Log
	}
	return slog.Default()
}

func (c *Cleaner) Start(ctx context.Context) {
	interval := c.Interval
	if interval <= 0 {
		interval = time.Hour
	}
	if c.minGap == 0 {
		c.minGap = time.Minute
	}
	t := time.NewTicker(interval)
	go func() {
		defer t.Stop()
		if _, err := c.Run(ctx); err != nil && ctx.Err() == nil {
			c.logger().Error("cache cleanup failed", "err", err)
		}
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if _, err := c.Run(ctx); err != nil && ctx.Err() == nil {
					c.logger().Error("cache cleanup failed", "err", err)
				}
			}
		}
	}()
}

func (c *Cleaner) MaybeRun(ctx context.Context) {
	c.mu.Lock()
	gap := c.minGap
	if gap == 0 {
		gap = time.Minute
	}
	if time.Since(c.lastRun) < gap {
		c.mu.Unlock()
		return
	}
	c.mu.Unlock()
	go func() {
		if _, err := c.Run(context.WithoutCancel(ctx)); err != nil {
			c.logger().Error("opportunistic cache cleanup failed", "err", err)
		}
	}()
}

func (c *Cleaner) Run(ctx context.Context) (int, error) {
	c.mu.Lock()
	c.lastRun = time.Now()
	c.mu.Unlock()

	cutoff := time.Now().Add(-c.TTL)
	seen := map[string]struct{}{}
	var hashes []string

	entries, err := c.Store.ListOlderThan(ctx, cutoff)
	if err != nil {
		return 0, err
	}
	for _, e := range entries {
		if _, ok := seen[e.Hash]; ok {
			continue
		}
		seen[e.Hash] = struct{}{}
		hashes = append(hashes, e.Hash)
	}

	objects, err := c.Backend.List(ctx)
	if err != nil {
		return 0, err
	}
	for _, obj := range objects {
		if obj.LastModified.IsZero() || !obj.LastModified.Before(cutoff) {
			continue
		}
		if _, ok := seen[obj.Hash]; ok {
			continue
		}
		seen[obj.Hash] = struct{}{}
		hashes = append(hashes, obj.Hash)
	}

	if len(hashes) == 0 {
		return 0, nil
	}
	if err := c.Backend.Delete(ctx, hashes); err != nil {
		return 0, err
	}
	if err := c.Store.Delete(ctx, hashes); err != nil {
		return 0, err
	}
	c.logger().Info("removed expired cache artifacts", "count", len(hashes), "ttl", c.TTL.String())
	return len(hashes), nil
}
