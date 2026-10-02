package app

import (
	"testing"
	"time"
)

// benchPool builds a pool of n enabled accounts for the hot-path benchmarks.
func benchPool(b *testing.B, n int) *AccountPool {
	b.Helper()
	pool := NewAccountPool(nil)
	for i := 0; i < n; i++ {
		if _, err := pool.Add("acct", "cc-bench-key-"+string(rune('a'+i)), true); err != nil {
			b.Fatal(err)
		}
	}
	return pool
}

// BenchmarkAccountPoolAcquire covers the per-request account selection. The
// pool lock is held read-only and each candidate is inspected once, so this
// should stay flat as the pool grows.
func BenchmarkAccountPoolAcquire(b *testing.B) {
	for _, size := range []int{1, 4, 16} {
		b.Run(sizeName(size), func(b *testing.B) {
			pool := benchPool(b, size)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if pool.Acquire() == nil {
					b.Fatal("no account acquired")
				}
			}
		})
	}
}

// BenchmarkUsageRecord covers the per-request usage accounting, including the
// mirror into the per-account and per-client-key counters and the dirty flag.
func BenchmarkUsageRecord(b *testing.B) {
	u := &UsageTracker{}
	rec := u.Recorder("acct-1", "key-1")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rec.Record(1200, 400, 100, 0)
	}
}

// BenchmarkUsageSnapshot measures the dashboard read path: it copies every
// per-account and per-client-key counter under one lock.
func BenchmarkUsageSnapshot(b *testing.B) {
	u := &UsageTracker{}
	for i := 0; i < 16; i++ {
		u.Recorder("acct-"+string(rune('a'+i)), "").Record(1000, 500, 0, 0)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if snap := u.Snapshot(); len(snap.Accounts) != 16 {
			b.Fatalf("accounts = %d", len(snap.Accounts))
		}
	}
}

// BenchmarkEventNormalizerText covers the SSE hot path for a plain text stream.
func BenchmarkEventNormalizerText(b *testing.B) {
	n := newCCEventNormalizer()
	ev := CCStreamEvent{Type: "text-delta", Text: "the quick brown fox "}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := n.Consume(ev); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkEarliestRateLimitWait covers the "all accounts busy" path, which
// scans every account to find the soonest cooldown expiry.
func BenchmarkEarliestRateLimitWait(b *testing.B) {
	pool := benchPool(b, 16)
	now := time.Now()
	for _, a := range pool.List() {
		a.RecordFailure(&upstreamAPIError{Status: 429, RetryAfter: "60"})
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if wait := pool.EarliestRateLimitWait(now); wait <= 0 {
			b.Fatal("expected a positive wait")
		}
	}
}

func sizeName(n int) string {
	if n == 1 {
		return "1account"
	}
	return itoaBench(n) + "accounts"
}

func itoaBench(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [8]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
