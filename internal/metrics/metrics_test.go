package metrics

import (
	"testing"
	"time"
)

func TestSnapshotTracksCountsBytesAndPercentiles(t *testing.T) {
	c := New()
	for i := 1; i <= 100; i++ {
		c.ObserveRead(time.Duration(i)*time.Millisecond, 10)
	}
	c.ObserveWrite(2*time.Second, 2048)

	s := c.Snapshot()
	if s.Reads.Count != 100 || s.Reads.Bytes != 1000 || s.Reads.Samples != 100 {
		t.Fatalf("reads: %+v", s.Reads)
	}
	if s.Reads.P50 != 50*time.Millisecond || s.Reads.P95 != 95*time.Millisecond {
		t.Fatalf("read percentiles: p50=%s p95=%s", s.Reads.P50, s.Reads.P95)
	}
	if s.Writes.Count != 1 || s.Writes.Bytes != 2048 || s.Writes.P95 != 2*time.Second {
		t.Fatalf("writes: %+v", s.Writes)
	}
}

func TestSnapshotBoundsLatencySamples(t *testing.T) {
	c := New()
	for i := 0; i < sampleLimit+10; i++ {
		c.ObserveRead(time.Duration(i)*time.Millisecond, 0)
	}
	if got := c.Snapshot().Reads.Samples; got != sampleLimit {
		t.Fatalf("sample count %d, want %d", got, sampleLimit)
	}
}
