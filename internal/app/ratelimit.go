package app

import (
	"sync"
	"time"
)

// adminAuthRateLimit caps consecutive failed admin-password attempts per IP
// before the source is locked out (brute-force protection).
const (
	adminFailLimit   = 5
	adminFailWindow  = 10 * time.Minute
	adminLockoutTime = 15 * time.Minute
	// adminPruneThreshold bounds the fail map: forged client IPs can rotate
	// per request, so entries must not accumulate without limit.
	adminPruneThreshold = 1024
)

// ipRateLimiter tracks failed attempts per client IP. After adminFailLimit
// failures inside adminFailWindow, the IP is locked out for
// adminLockoutTime. A successful authentication clears the record.
type ipRateLimiter struct {
	mu        sync.Mutex
	fails     map[string]*failRecord
	lastSweep time.Time
}

type failRecord struct {
	count       int
	windowStart time.Time
	lockedUntil time.Time
}

func newIPRateLimiter() *ipRateLimiter {
	return &ipRateLimiter{fails: make(map[string]*failRecord)}
}

// Allow reports whether the IP may attempt authentication now, and how long
// until the lockout expires when it may not.
func (l *ipRateLimiter) Allow(ip string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	l.pruneLocked(now)
	rec, ok := l.fails[ip]
	if !ok {
		return true, 0
	}
	if !rec.lockedUntil.IsZero() {
		if now.Before(rec.lockedUntil) {
			return false, time.Until(rec.lockedUntil)
		}
		delete(l.fails, ip)
		return true, 0
	}
	if now.Sub(rec.windowStart) > adminFailWindow {
		delete(l.fails, ip)
	}
	return true, 0
}

// Fail records a failed attempt, arming the lockout once the limit is hit.
func (l *ipRateLimiter) Fail(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	l.pruneLocked(now)
	rec, ok := l.fails[ip]
	if !ok || now.Sub(rec.windowStart) > adminFailWindow {
		if !ok && len(l.fails) >= adminPruneThreshold {
			l.evictOldestLocked()
		}
		rec = &failRecord{windowStart: now}
		l.fails[ip] = rec
	}
	rec.count++
	if rec.count >= adminFailLimit {
		rec.lockedUntil = now.Add(adminLockoutTime)
	}
}

// Reset clears the IP's record after a successful authentication.
func (l *ipRateLimiter) Reset(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.fails, ip)
}

func (l *ipRateLimiter) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.fails)
}

// pruneLocked deletes expired records so untouched IPs do not linger forever.
// It runs at most once per adminFailWindow, or immediately once the map has
// grown past adminPruneThreshold.
func (l *ipRateLimiter) pruneLocked(now time.Time) {
	if len(l.fails) == 0 {
		return
	}
	if now.Sub(l.lastSweep) < adminFailWindow && len(l.fails) < adminPruneThreshold {
		return
	}
	l.lastSweep = now
	for ip, rec := range l.fails {
		if l.expiredLocked(rec, now) {
			delete(l.fails, ip)
		}
	}
}

func (l *ipRateLimiter) expiredLocked(rec *failRecord, now time.Time) bool {
	if !rec.lockedUntil.IsZero() {
		return now.After(rec.lockedUntil)
	}
	return now.Sub(rec.windowStart) > adminFailWindow
}

// evictOldestLocked drops the record with the oldest fail window, keeping the
// map bounded when pruneLocked cannot keep up with unique forged IPs.
func (l *ipRateLimiter) evictOldestLocked() {
	var oldestIP string
	var oldest time.Time
	for ip, rec := range l.fails {
		if oldestIP == "" || rec.windowStart.Before(oldest) {
			oldestIP, oldest = ip, rec.windowStart
		}
	}
	if oldestIP != "" {
		delete(l.fails, oldestIP)
	}
}
