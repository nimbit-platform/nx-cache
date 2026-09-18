package store

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nimbit-platform/nx-cache/internal/storage"
)

const catalogMetaKey = "catalog.json"

var ErrCatalogLoadFailed = errors.New("catalog snapshot was not loaded; refusing to flush")

type objectStats struct {
	Hits   int64               `json:"hits"`
	Misses int64               `json:"misses"`
	Stores int64               `json:"stores"`
	Days   map[string]*DayStat `json:"days"`
}

type snapshot struct {
	Stats   objectStats      `json:"stats"`
	Entries map[string]Entry `json:"entries"`
}

// ObjectOptions controls how the S3 catalog is cached and flushed.
type ObjectOptions struct {
	FlushInterval time.Duration
	Log           *slog.Logger
}

// Object keeps the catalog in memory and flushes a single JSON snapshot to S3.
type Object struct {
	backend    storage.Backend
	flushEvery time.Duration
	log        *slog.Logger
	mu         sync.Mutex
	flushMu    sync.Mutex
	entries    map[string]Entry
	stats      objectStats
	dirty      bool
	loadFailed bool
	wg         sync.WaitGroup
}

func NewObject(backend storage.Backend, opts ...ObjectOptions) *Object {
	var o ObjectOptions
	if len(opts) > 0 {
		o = opts[0]
	}
	log := o.Log
	if log == nil {
		log = slog.Default()
	}
	return &Object{
		backend:    backend,
		flushEvery: o.FlushInterval,
		log:        log,
		entries:    map[string]Entry{},
		stats:      objectStats{Days: map[string]*DayStat{}},
	}
}

func (o *Object) Load(ctx context.Context) error {
	data, err := o.backend.GetMeta(ctx, catalogMetaKey)
	if errors.Is(err, storage.ErrNotFound) {
		o.mu.Lock()
		o.loadFailed = false
		o.mu.Unlock()
		return nil
	}
	if err != nil {
		o.mu.Lock()
		o.loadFailed = true
		o.mu.Unlock()
		return err
	}
	var snap snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		o.mu.Lock()
		o.loadFailed = true
		o.mu.Unlock()
		return err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if snap.Entries != nil {
		o.entries = snap.Entries
	}
	o.stats = snap.Stats
	if o.stats.Days == nil {
		o.stats.Days = map[string]*DayStat{}
	}
	o.dirty = false
	o.loadFailed = false
	return nil
}

func (o *Object) Start(ctx context.Context) {
	if o.flushEvery <= 0 {
		return
	}
	o.wg.Add(1)
	go func() {
		defer o.wg.Done()
		t := time.NewTicker(o.flushEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := o.Flush(context.WithoutCancel(ctx)); err != nil {
					o.log.Error("catalog flush failed", "err", err)
					continue
				}
				o.log.Debug("catalog flushed")
			}
		}
	}()
}

func (o *Object) Close() error {
	o.wg.Wait()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return o.Flush(ctx)
}

func (o *Object) Flush(ctx context.Context) error {
	o.flushMu.Lock()
	defer o.flushMu.Unlock()

	o.mu.Lock()
	if o.loadFailed {
		o.mu.Unlock()
		return ErrCatalogLoadFailed
	}
	if !o.dirty {
		o.mu.Unlock()
		return nil
	}
	snap := o.cloneLocked()
	o.dirty = false
	o.mu.Unlock()

	data, err := json.Marshal(snap)
	if err != nil {
		o.markDirty()
		return err
	}
	if err := o.backend.PutMeta(ctx, catalogMetaKey, data); err != nil {
		o.markDirty()
		return err
	}
	return nil
}

func (o *Object) markDirty() {
	o.mu.Lock()
	o.dirty = true
	o.mu.Unlock()
}

func (o *Object) cloneLocked() snapshot {
	entries := make(map[string]Entry, len(o.entries))
	for k, v := range o.entries {
		entries[k] = v
	}
	days := make(map[string]*DayStat, len(o.stats.Days))
	for k, v := range o.stats.Days {
		if v == nil {
			continue
		}
		cp := *v
		days[k] = &cp
	}
	return snapshot{
		Stats: objectStats{
			Hits:   o.stats.Hits,
			Misses: o.stats.Misses,
			Stores: o.stats.Stores,
			Days:   days,
		},
		Entries: entries,
	}
}

func (o *Object) UpsertEntry(_ context.Context, hash string, size int64, at time.Time, info TaskInfo) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	e := o.entries[hash]
	if e.Hash == "" {
		e = Entry{Hash: hash, CreatedAt: at, LastAccessedAt: at}
	}
	e.Size = size
	if e.CreatedAt.IsZero() {
		e.CreatedAt = at
	}
	if info.Project != "" {
		e.Project = info.Project
	}
	if info.Target != "" {
		e.Target = info.Target
	}
	if info.Config != "" {
		e.Config = info.Config
	}
	if info.Kind != "" {
		e.Kind = info.Kind
	}
	o.entries[hash] = e
	o.bumpLocked("stores", at)
	o.dirty = true
	return nil
}

func (o *Object) RecordHit(_ context.Context, hash string, at time.Time) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	e := o.entries[hash]
	if e.Hash == "" {
		e = Entry{Hash: hash, CreatedAt: at}
	}
	e.Hits++
	e.LastAccessedAt = at
	o.entries[hash] = e
	o.bumpLocked("hits", at)
	o.dirty = true
	return nil
}

func (o *Object) RecordMiss(_ context.Context, at time.Time) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.bumpLocked("misses", at)
	o.dirty = true
	return nil
}

func (o *Object) List(ctx context.Context, query string, limit, offset int) ([]Entry, int, error) {
	entries, err := o.allEntries(ctx)
	if err != nil {
		return nil, 0, err
	}
	q := strings.ToLower(strings.TrimSpace(query))
	if q != "" {
		filtered := make([]Entry, 0, len(entries))
		for _, e := range entries {
			if e.MatchesQuery(q) {
				filtered = append(filtered, e)
			}
		}
		entries = filtered
	}
	total := len(entries)
	if limit <= 0 {
		limit = 25
	}
	if offset > total {
		offset = total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	return entries[offset:end], total, nil
}

func (o *Object) ListOlderThan(ctx context.Context, cutoff time.Time) ([]Entry, error) {
	entries, err := o.allEntries(ctx)
	if err != nil {
		return nil, err
	}
	var old []Entry
	for _, e := range entries {
		if e.CreatedAt.Before(cutoff) {
			old = append(old, e)
		}
	}
	return old, nil
}

func (o *Object) Delete(_ context.Context, hashes []string) error {
	if len(hashes) == 0 {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, h := range hashes {
		delete(o.entries, h)
	}
	o.dirty = true
	return nil
}

func (o *Object) Stats(ctx context.Context) (Stats, error) {
	entries, err := o.allEntries(ctx)
	if err != nil {
		return Stats{}, err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	s := Stats{Entries: int64(len(entries)), Hits: o.stats.Hits, Misses: o.stats.Misses, Stores: o.stats.Stores}
	counts := map[string]int64{}
	for _, e := range entries {
		s.TotalSize += e.Size
		kind := e.Kind
		if kind == "" {
			kind = "unknown"
		}
		counts[kind]++
	}
	s.ByKind = kindCountsFromMap(counts)
	since := time.Now().UTC().AddDate(0, 0, -6).Format("2006-01-02")
	for day, st := range o.stats.Days {
		if day >= since && st != nil {
			s.Days = append(s.Days, DayStat{Day: day, Hits: st.Hits, Misses: st.Misses, Stores: st.Stores})
		}
	}
	sort.Slice(s.Days, func(i, j int) bool { return s.Days[i].Day < s.Days[j].Day })
	return s, nil
}

func (o *Object) EnsureEntry(_ context.Context, hash string, size int64, at time.Time) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if e, ok := o.entries[hash]; ok {
		if e.Size == 0 && size > 0 {
			e.Size = size
			o.entries[hash] = e
			o.dirty = true
		}
		return nil
	}
	o.entries[hash] = Entry{Hash: hash, Size: size, CreatedAt: at, LastAccessedAt: at}
	o.dirty = true
	return nil
}

func (o *Object) UpdateTaskInfo(_ context.Context, hash string, info TaskInfo) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	e, ok := o.entries[hash]
	if !ok {
		return nil
	}
	if info.Project != "" {
		e.Project = info.Project
	}
	if info.Target != "" {
		e.Target = info.Target
	}
	if info.Config != "" {
		e.Config = info.Config
	}
	if info.Kind != "" {
		e.Kind = info.Kind
	}
	o.entries[hash] = e
	o.dirty = true
	return nil
}

func (o *Object) Seed(e Entry) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.entries[e.Hash] = e
	o.dirty = true
	return nil
}

func (o *Object) allEntries(context.Context) ([]Entry, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	entries := make([]Entry, 0, len(o.entries))
	for _, e := range o.entries {
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].CreatedAt.After(entries[j].CreatedAt) })
	return entries, nil
}

func (o *Object) bumpLocked(key string, at time.Time) {
	switch key {
	case "hits":
		o.stats.Hits++
	case "misses":
		o.stats.Misses++
	case "stores":
		o.stats.Stores++
	}
	day := at.UTC().Format("2006-01-02")
	if o.stats.Days[day] == nil {
		o.stats.Days[day] = &DayStat{Day: day}
	}
	switch key {
	case "hits":
		o.stats.Days[day].Hits++
	case "misses":
		o.stats.Days[day].Misses++
	case "stores":
		o.stats.Days[day].Stores++
	}
}
