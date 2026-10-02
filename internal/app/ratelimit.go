package app

import (
	"container/list"
	"sync"
	"time"
)

// Rate limiting constants.
//
//   - admin*: consecutive failed admin-password attempts per IP before the
//     source is locked out (brute-force protection on /admin/*).
//   - clientKeyFailLimit/Window: the same protection for the local Bearer keys
//     on /v1/* and /usage, so an attacker cannot grind the key space.
const (
	adminFailLimit   = 5
	adminFailWindow  = 10 * time.Minute
	adminLockoutTime = 15 * time.Minute
	// adminPruneThreshold bounds the fail map: forged client IPs can rotate
	// per request, so entries must not accumulate without limit.
	adminPruneThreshold = 1024

	clientKeyFailLimit   = 20
	clientKeyFailWindow  = 5 * time.Minute
	clientKeyLockoutTime = 10 * time.Minute
)

// ipRateLimiter tracks failed attempts per client IP. After failLimit failures
// inside failWindow, the IP is locked out for lockoutTime. A successful
// authentication clears the record.
//
// The fail map is paired with a recency list so the capacity eviction is O(1)
// instead of scanning every entry for the oldest window.
type ipRateLimiter struct {
	mu        sync.Mutex
	fails     map[string]*failRecord
	recency   *list.List // front = most recently touched IP
	lastSweep time.Time

	failLimit   int
	failWindow  time.Duration
	lockoutTime time.Duration
}

type failRecord struct {
	count       int
	windowStart time.Time
	lockedUntil time.Time
	// elem is the entry in ipRateLimiter.recency holding this IP.
	elem *list.Element
}

func newIPRateLimiter() *ipRateLimiter {
	return newIPRateLimiterWith(adminFailLimit, adminFailWindow, adminLockoutTime)
}

func newIPRateLimiterWith(failLimit int, failWindow, lockoutTime time.Duration) *ipRateLimiter {
	return &ipRateLimiter{
		fails:       make(map[string]*failRecord),
		recency:     list.New(),
		failLimit:   failLimit,
		failWindow:  failWindow,
		lockoutTime: lockoutTime,
	}
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
		l.removeLocked(ip, rec)
		return true, 0
	}
	if now.Sub(rec.windowStart) > l.failWindow {
		l.removeLocked(ip, rec)
		return true, 0
	}
	l.touchLocked(rec)
	return true, 0
}

// Fail records a failed attempt, arming the lockout once the limit is hit.
func (l *ipRateLimiter) Fail(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	l.pruneLocked(now)
	rec, ok := l.fails[ip]
	if !ok || now.Sub(rec.windowStart) > l.failWindow {
		if ok {
			l.removeLocked(ip, rec)
		}
		if len(l.fails) >= adminPruneThreshold {
			l.evictOldestLocked()
		}
		rec = &failRecord{windowStart: now}
		rec.elem = l.recency.PushFront(ip)
		l.fails[ip] = rec
	}
	rec.count++
	if rec.count >= l.failLimit {
		rec.lockedUntil = now.Add(l.lockoutTime)
	}
}

// Reset clears the IP's record after a successful authentication.
func (l *ipRateLimiter) Reset(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if rec, ok := l.fails[ip]; ok {
		l.removeLocked(ip, rec)
	}
}

func (l *ipRateLimiter) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.fails)
}

// pruneLocked deletes expired records so untouched IPs do not linger forever.
// It runs at most once per failWindow, or immediately once the map has grown
// past adminPruneThreshold.
func (l *ipRateLimiter) pruneLocked(now time.Time) {
	if len(l.fails) == 0 {
		return
	}
	if now.Sub(l.lastSweep) < l.failWindow && len(l.fails) < adminPruneThreshold {
		return
	}
	l.lastSweep = now
	for ip, rec := range l.fails {
		if l.expiredLocked(rec, now) {
			l.removeLocked(ip, rec)
		}
	}
}

func (l *ipRateLimiter) expiredLocked(rec *failRecord, now time.Time) bool {
	if !rec.lockedUntil.IsZero() {
		return now.After(rec.lockedUntil)
	}
	return now.Sub(rec.windowStart) > l.failWindow
}

// removeLocked drops a record together with its recency entry.
func (l *ipRateLimiter) removeLocked(ip string, rec *failRecord) {
	delete(l.fails, ip)
	if rec != nil && rec.elem != nil {
		l.recency.Remove(rec.elem)
		rec.elem = nil
	}
}

// touchLocked marks the record as most recently used, keeping the eviction
// order meaningful for repeat offenders.
func (l *ipRateLimiter) touchLocked(rec *failRecord) {
	if rec.elem != nil {
		l.recency.MoveToFront(rec.elem)
	}
}

// evictOldestLocked drops the least recently touched record, keeping the map
// bounded when pruneLocked cannot keep up with unique forged IPs.
func (l *ipRateLimiter) evictOldestLocked() {
	for {
		back := l.recency.Back()
		if back == nil {
			break
		}
		ip := back.Value.(string)
		l.recency.Remove(back)
		if rec, ok := l.fails[ip]; ok && rec.elem == back {
			delete(l.fails, ip)
			return
		}
	}
	// Records injected without a recency entry (tests, future callers) have no
	// list position; fall back to a linear scan so the map still stays bounded.
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
