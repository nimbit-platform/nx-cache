package store

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nimbit-platform/nx-cache/internal/storage"
)

const (
	statsMetaKey = "stats.json"
)

func entryMetaKey(hash string) string { return "entries/" + hash + ".json" }

type objectStats struct {
	Hits   int64               `json:"hits"`
	Misses int64               `json:"misses"`
	Stores int64               `json:"stores"`
	Days   map[string]*DayStat `json:"days"`
}

// Object keeps the catalog and counters next to artifacts (S3 or memory).
type Object struct {
	backend storage.Backend
	mu      sync.Mutex
}

func NewObject(backend storage.Backend) *Object {
	return &Object{backend: backend}
}

func (o *Object) Close() error { return nil }

func (o *Object) UpsertEntry(ctx context.Context, hash string, size int64, at time.Time) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	e := Entry{Hash: hash, Size: size, CreatedAt: at, LastAccessedAt: at}
	if existing, err := o.loadEntry(ctx, hash); err == nil {
		e.CreatedAt = existing.CreatedAt
		e.Hits = existing.Hits
		if existing.LastAccessedAt.After(e.LastAccessedAt) {
			e.LastAccessedAt = existing.LastAccessedAt
		}
	}
	if err := o.saveEntry(ctx, e); err != nil {
		return err
	}
	return o.bumpLocked(ctx, "stores", at)
}

func (o *Object) RecordHit(ctx context.Context, hash string, at time.Time) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	e, err := o.loadEntry(ctx, hash)
	if err != nil {
		if !errors.Is(err, storage.ErrNotFound) {
			return err
		}
		e = Entry{Hash: hash, CreatedAt: at}
	}
	e.Hits++
	e.LastAccessedAt = at
	if err := o.saveEntry(ctx, e); err != nil {
		return err
	}
	return o.bumpLocked(ctx, "hits", at)
}

func (o *Object) RecordMiss(ctx context.Context, at time.Time) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.bumpLocked(ctx, "misses", at)
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
			if strings.Contains(strings.ToLower(e.Hash), q) {
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

func (o *Object) Delete(ctx context.Context, hashes []string) error {
	if len(hashes) == 0 {
		return nil
	}
	keys := make([]string, 0, len(hashes))
	for _, h := range hashes {
		keys = append(keys, entryMetaKey(h))
	}
	return o.backend.DeleteMeta(ctx, keys)
}

func (o *Object) Stats(ctx context.Context) (Stats, error) {
	entries, err := o.allEntries(ctx)
	if err != nil {
		return Stats{}, err
	}
	s := Stats{Entries: int64(len(entries))}
	for _, e := range entries {
		s.TotalSize += e.Size
	}
	raw, err := o.loadStats(ctx)
	if err != nil {
		return Stats{}, err
	}
	s.Hits, s.Misses, s.Stores = raw.Hits, raw.Misses, raw.Stores
	since := time.Now().UTC().AddDate(0, 0, -6).Format("2006-01-02")
	for day, st := range raw.Days {
		if day >= since && st != nil {
			s.Days = append(s.Days, DayStat{Day: day, Hits: st.Hits, Misses: st.Misses, Stores: st.Stores})
		}
	}
	sort.Slice(s.Days, func(i, j int) bool { return s.Days[i].Day < s.Days[j].Day })
	return s, nil
}

func (o *Object) EnsureEntry(ctx context.Context, hash string, size int64, at time.Time) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if _, err := o.loadEntry(ctx, hash); err == nil {
		return nil
	} else if !errors.Is(err, storage.ErrNotFound) {
		return err
	}
	return o.saveEntry(ctx, Entry{Hash: hash, Size: size, CreatedAt: at, LastAccessedAt: at})
}

func (o *Object) Seed(e Entry) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.saveEntry(context.Background(), e)
}

func (o *Object) allEntries(ctx context.Context) ([]Entry, error) {
	objects, err := o.backend.List(ctx)
	if err != nil {
		return nil, err
	}
	entries := make([]Entry, 0, len(objects))
	for _, obj := range objects {
		e := Entry{Hash: obj.Hash, Size: obj.Size, CreatedAt: obj.LastModified, LastAccessedAt: obj.LastModified}
		if meta, err := o.loadEntry(ctx, obj.Hash); err == nil {
			if !meta.CreatedAt.IsZero() {
				e.CreatedAt = meta.CreatedAt
			}
			if !meta.LastAccessedAt.IsZero() {
				e.LastAccessedAt = meta.LastAccessedAt
			}
			e.Hits = meta.Hits
			if meta.Size > 0 {
				e.Size = meta.Size
			}
		} else if !errors.Is(err, storage.ErrNotFound) {
			return nil, err
		}
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].CreatedAt.After(entries[j].CreatedAt) })
	return entries, nil
}

func (o *Object) loadEntry(ctx context.Context, hash string) (Entry, error) {
	data, err := o.backend.GetMeta(ctx, entryMetaKey(hash))
	if err != nil {
		return Entry{}, err
	}
	var e Entry
	if err := json.Unmarshal(data, &e); err != nil {
		return Entry{}, err
	}
	return e, nil
}

func (o *Object) saveEntry(ctx context.Context, e Entry) error {
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	return o.backend.PutMeta(ctx, entryMetaKey(e.Hash), data)
}

func (o *Object) loadStats(ctx context.Context) (objectStats, error) {
	data, err := o.backend.GetMeta(ctx, statsMetaKey)
	if errors.Is(err, storage.ErrNotFound) {
		return objectStats{Days: map[string]*DayStat{}}, nil
	}
	if err != nil {
		return objectStats{}, err
	}
	var s objectStats
	if err := json.Unmarshal(data, &s); err != nil {
		return objectStats{}, err
	}
	if s.Days == nil {
		s.Days = map[string]*DayStat{}
	}
	return s, nil
}

func (o *Object) bumpLocked(ctx context.Context, key string, at time.Time) error {
	s, err := o.loadStats(ctx)
	if err != nil {
		return err
	}
	switch key {
	case "hits":
		s.Hits++
	case "misses":
		s.Misses++
	case "stores":
		s.Stores++
	}
	day := at.UTC().Format("2006-01-02")
	if s.Days[day] == nil {
		s.Days[day] = &DayStat{Day: day}
	}
	switch key {
	case "hits":
		s.Days[day].Hits++
	case "misses":
		s.Days[day].Misses++
	case "stores":
		s.Days[day].Stores++
	}
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return o.backend.PutMeta(ctx, statsMetaKey, data)
}
