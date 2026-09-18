package metrics

import (
	"sort"
	"sync"
	"time"
)

// ponytail: keep a bounded in-memory window; use a real histogram if cardinality or accuracy needs grow.
const sampleLimit = 512

type OperationStats struct {
	Count   int64
	Bytes   int64
	Samples int
	P50     time.Duration
	P95     time.Duration
}

type Snapshot struct {
	Reads  OperationStats
	Writes OperationStats
}

type Collector struct {
	mu     sync.Mutex
	reads  operation
	writes operation
}

type operation struct {
	count   int64
	bytes   int64
	samples []time.Duration
}

func New() *Collector { return &Collector{} }

func (c *Collector) ObserveRead(duration time.Duration, bytes int64) {
	c.observe(&c.reads, duration, bytes)
}

func (c *Collector) ObserveWrite(duration time.Duration, bytes int64) {
	c.observe(&c.writes, duration, bytes)
}

func (c *Collector) observe(op *operation, duration time.Duration, bytes int64) {
	if c == nil {
		return
	}
	if duration < 0 {
		duration = 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	op.count++
	if bytes > 0 {
		op.bytes += bytes
	}
	if len(op.samples) == sampleLimit {
		copy(op.samples, op.samples[1:])
		op.samples = op.samples[:sampleLimit-1]
	}
	op.samples = append(op.samples, duration)
}

func (c *Collector) Snapshot() Snapshot {
	if c == nil {
		return Snapshot{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return Snapshot{
		Reads:  summarize(c.reads),
		Writes: summarize(c.writes),
	}
}

func summarize(op operation) OperationStats {
	samples := append([]time.Duration(nil), op.samples...)
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	return OperationStats{
		Count:   op.count,
		Bytes:   op.bytes,
		Samples: len(samples),
		P50:     percentile(samples, 50),
		P95:     percentile(samples, 95),
	}
}

func percentile(samples []time.Duration, percentile int) time.Duration {
	if len(samples) == 0 {
		return 0
	}
	index := (len(samples) - 1) * percentile / 100
	return samples[index]
}
