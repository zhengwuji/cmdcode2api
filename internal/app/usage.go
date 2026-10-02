package app

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// usageRecorder is what the stream handlers consume so they can record usage
// globally, per upstream account, per client key, or both.
type usageRecorder interface {
	Record(prompt, completion, cacheRead, cacheWrite int)
}

type UsageTracker struct {
	TotalRequests    atomic.Int64 `json:"total_requests"`
	PromptTokens     atomic.Int64 `json:"prompt_tokens"`
	CompletionTokens atomic.Int64 `json:"completion_tokens"`
	CacheReadTokens  atomic.Int64 `json:"cache_read_tokens"`
	CacheWriteTokens atomic.Int64 `json:"cache_write_tokens"`
	saveMu           sync.Mutex

	// Persistence is debounced: mutating paths call markDirty, and
	// RunPersistence coalesces every change inside one flush interval into a
	// single write. Flush forces a write on shutdown.
	dirty atomic.Bool

	// Both counter maps are guarded by accMu. Account and client-key
	// counters are independent dimensions: an account aggregates across all
	// client keys and vice versa.
	accMu      sync.Mutex
	accounts   map[string]*UsageCounters
	clientKeys map[string]*UsageCounters
	// quotas caches the latest fetched quota snapshot per account ID; the
	// entries are replaced wholesale and never mutated in place.
	quotas map[string]*QuotaSnapshot
}

type UsageCounters struct {
	Requests         atomic.Int64
	PromptTokens     atomic.Int64
	CompletionTokens atomic.Int64
	CacheReadTokens  atomic.Int64
	CacheWriteTokens atomic.Int64
}

func (c *UsageCounters) add(prompt, completion, cacheRead, cacheWrite int) {
	c.Requests.Add(1)
	c.PromptTokens.Add(int64(prompt))
	c.CompletionTokens.Add(int64(completion))
	if cacheRead > 0 {
		c.CacheReadTokens.Add(int64(cacheRead))
	}
	if cacheWrite > 0 {
		c.CacheWriteTokens.Add(int64(cacheWrite))
	}
}

func (c *UsageCounters) restore(entry UsageSnapshotEntry) {
	c.Requests.Store(entry.Requests)
	c.PromptTokens.Store(entry.PromptTokens)
	c.CompletionTokens.Store(entry.CompletionTokens)
	c.CacheReadTokens.Store(entry.CacheReadTokens)
	c.CacheWriteTokens.Store(entry.CacheWriteTokens)
}

func (c *UsageCounters) snapshot() UsageSnapshotEntry {
	return UsageSnapshotEntry{
		Requests:         c.Requests.Load(),
		PromptTokens:     c.PromptTokens.Load(),
		CompletionTokens: c.CompletionTokens.Load(),
		CacheReadTokens:  c.CacheReadTokens.Load(),
		CacheWriteTokens: c.CacheWriteTokens.Load(),
	}
}

func (u *UsageTracker) Record(prompt, completion, cacheRead, cacheWrite int) {
	u.TotalRequests.Add(1)
	u.PromptTokens.Add(int64(prompt))
	u.CompletionTokens.Add(int64(completion))
	if cacheRead > 0 {
		u.CacheReadTokens.Add(int64(cacheRead))
	}
	if cacheWrite > 0 {
		u.CacheWriteTokens.Add(int64(cacheWrite))
	}
	u.markDirty()
}

// ForAccount returns a recorder that mirrors usage into the per-account
// counters. A nil account records globally only.
func (u *UsageTracker) ForAccount(a *Account) usageRecorder {
	if a == nil {
		return u
	}
	return u.Recorder(a.ID(), "")
}

// RecorderFor is a nil-safe Recorder wrapper taking the served account.
func (u *UsageTracker) RecorderFor(a *Account, clientKeyID string) usageRecorder {
	if a == nil {
		return u.Recorder("", clientKeyID)
	}
	return u.Recorder(a.ID(), clientKeyID)
}

// Recorder returns a recorder that mirrors usage into the per-account and
// per-client-key counters for every non-empty ID.
func (u *UsageTracker) Recorder(accountID, clientKeyID string) usageRecorder {
	if accountID == "" && clientKeyID == "" {
		return u
	}
	return &mirrorUsageRecorder{tracker: u, accountID: accountID, clientKeyID: clientKeyID}
}

type mirrorUsageRecorder struct {
	tracker     *UsageTracker
	accountID   string
	clientKeyID string
}

func (r *mirrorUsageRecorder) Record(prompt, completion, cacheRead, cacheWrite int) {
	r.tracker.Record(prompt, completion, cacheRead, cacheWrite)
	if c := r.tracker.accountCounter(r.accountID); c != nil {
		c.add(prompt, completion, cacheRead, cacheWrite)
	}
	if c := r.tracker.clientKeyCounter(r.clientKeyID); c != nil {
		c.add(prompt, completion, cacheRead, cacheWrite)
	}
}

// counter returns the counter for a non-empty id, creating it on first use.
func (u *UsageTracker) counter(m *map[string]*UsageCounters, id string) *UsageCounters {
	if id == "" {
		return nil
	}
	u.accMu.Lock()
	defer u.accMu.Unlock()
	u.ensureMapsLocked()
	target := *m
	if target == nil {
		target = make(map[string]*UsageCounters)
		*m = target
	}
	c, ok := target[id]
	if !ok {
		c = &UsageCounters{}
		target[id] = c
	}
	return c
}

func (u *UsageTracker) accountCounter(id string) *UsageCounters {
	return u.counter(&u.accounts, id)
}

func (u *UsageTracker) clientKeyCounter(id string) *UsageCounters {
	return u.counter(&u.clientKeys, id)
}

func (u *UsageTracker) ensureMapsLocked() {
	if u.accounts == nil {
		u.accounts = make(map[string]*UsageCounters)
	}
	if u.clientKeys == nil {
		u.clientKeys = make(map[string]*UsageCounters)
	}
	if u.quotas == nil {
		u.quotas = make(map[string]*QuotaSnapshot)
	}
}

// countersFor returns the counters for id from the map selected by m. m is a
// pointer so the field is dereferenced only under accMu: reading u.accounts /
// u.clientKeys at the call site would race with ensureMapsLocked, which can
// initialize them from a background goroutine.
func (u *UsageTracker) countersFor(m *map[string]*UsageCounters, id string) UsageSnapshotEntry {
	u.accMu.Lock()
	c := (*m)[id]
	u.accMu.Unlock()
	if c == nil {
		return UsageSnapshotEntry{}
	}
	return c.snapshot()
}

// AccountUsage returns a snapshot of one account's durable counters.
func (u *UsageTracker) AccountUsage(id string) UsageSnapshotEntry {
	return u.countersFor(&u.accounts, id)
}

// ClientKeyUsage returns a snapshot of one client key's durable counters.
func (u *UsageTracker) ClientKeyUsage(id string) UsageSnapshotEntry {
	return u.countersFor(&u.clientKeys, id)
}

// DropAccount forgets a removed account's counters and quota snapshot so they
// stop persisting.
func (u *UsageTracker) DropAccount(id string) {
	u.accMu.Lock()
	delete(u.accounts, id)
	delete(u.quotas, id)
	u.accMu.Unlock()
	u.markDirty()
}

// Quota returns the cached quota snapshot for an account, or nil. Stored
// snapshots are immutable; callers must not mutate the returned value.
func (u *UsageTracker) Quota(id string) *QuotaSnapshot {
	u.accMu.Lock()
	defer u.accMu.Unlock()
	return u.quotas[id]
}

// SetQuota stores an account's latest quota snapshot.
func (u *UsageTracker) SetQuota(id string, snap *QuotaSnapshot) {
	if id == "" || snap == nil {
		return
	}
	u.accMu.Lock()
	u.ensureMapsLocked()
	u.quotas[id] = snap
	u.accMu.Unlock()
	u.markDirty()
}

// DropQuota forgets an account's quota snapshot, used when its key changes
// because the snapshot belongs to the old credential.
func (u *UsageTracker) DropQuota(id string) {
	u.accMu.Lock()
	delete(u.quotas, id)
	u.accMu.Unlock()
	u.markDirty()
}

// quotaSnapshot copies the quota map for persistence.
func (u *UsageTracker) quotaSnapshot() map[string]*QuotaSnapshot {
	u.accMu.Lock()
	defer u.accMu.Unlock()
	if len(u.quotas) == 0 {
		return nil
	}
	out := make(map[string]*QuotaSnapshot, len(u.quotas))
	for id, q := range u.quotas {
		out[id] = q
	}
	return out
}

// MoveAccount migrates counters when an account's key (and therefore ID)
// changes. Counters are merged if the target already has any.
func (u *UsageTracker) MoveAccount(oldID, newID string) {
	if oldID == "" || newID == "" || oldID == newID {
		return
	}
	u.accMu.Lock()
	u.ensureMapsLocked()
	old := u.accounts[oldID]
	if old == nil {
		u.accMu.Unlock()
		return
	}
	delete(u.accounts, oldID)
	target := u.accounts[newID]
	if target == nil {
		u.accounts[newID] = old
	} else {
		target.Requests.Add(old.Requests.Load())
		target.PromptTokens.Add(old.PromptTokens.Load())
		target.CompletionTokens.Add(old.CompletionTokens.Load())
		target.CacheReadTokens.Add(old.CacheReadTokens.Load())
		target.CacheWriteTokens.Add(old.CacheWriteTokens.Load())
	}
	u.accMu.Unlock()
	u.markDirty()
}

// DropClientKey forgets a removed client key's counters.
func (u *UsageTracker) DropClientKey(id string) {
	u.accMu.Lock()
	delete(u.clientKeys, id)
	u.accMu.Unlock()
	u.markDirty()
}

func (u *UsageTracker) Snapshot() UsageSnapshot {
	snap := UsageSnapshot{
		TotalRequests:    u.TotalRequests.Load(),
		PromptTokens:     u.PromptTokens.Load(),
		CompletionTokens: u.CompletionTokens.Load(),
		CacheReadTokens:  u.CacheReadTokens.Load(),
		CacheWriteTokens: u.CacheWriteTokens.Load(),
	}
	u.accMu.Lock()
	defer u.accMu.Unlock()
	if len(u.accounts) > 0 {
		snap.Accounts = make(map[string]UsageSnapshotEntry, len(u.accounts))
		for id, c := range u.accounts {
			snap.Accounts[id] = c.snapshot()
		}
	}
	if len(u.clientKeys) > 0 {
		snap.ClientKeys = make(map[string]UsageSnapshotEntry, len(u.clientKeys))
		for id, c := range u.clientKeys {
			snap.ClientKeys[id] = c.snapshot()
		}
	}
	return snap
}

type UsageSnapshot struct {
	TotalRequests    int64 `json:"total_requests"`
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	CacheReadTokens  int64 `json:"cache_read_tokens"`
	CacheWriteTokens int64 `json:"cache_write_tokens"`

	Accounts   map[string]UsageSnapshotEntry `json:"accounts,omitempty"`
	ClientKeys map[string]UsageSnapshotEntry `json:"client_keys,omitempty"`
}

type UsageSnapshotEntry struct {
	Requests         int64 `json:"requests"`
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	CacheReadTokens  int64 `json:"cache_read_tokens"`
	CacheWriteTokens int64 `json:"cache_write_tokens"`
}

// persistedUsage is the on-disk shape of usage.json: the public usage
// snapshot plus per-account quota snapshots, which stay out of UsageSnapshot
// so they never reach the unauthenticated /usage endpoint.
type persistedUsage struct {
	UsageSnapshot
	Quotas map[string]*QuotaSnapshot `json:"quotas,omitempty"`
}

// TotalTokens returns prompt + completion (not counting cache separately)
func (s UsageSnapshot) TotalTokens() int64 {
	return s.PromptTokens + s.CompletionTokens
}

// ====== persistence ======

// usageFile is a var so tests can redirect persistence to a temp dir.
var usageFile = "usage.json"

// usageFlushInterval bounds how long a usage mutation may stay unpersisted.
// The write path used to run synchronously on every chat request and on every
// quota refresh; coalescing them keeps a busy gateway from rewriting the whole
// file (and burning fsyncs) once per request.
const usageFlushInterval = time.Second

// markDirty records that the tracker has unsaved changes. It never blocks the
// request path.
func (u *UsageTracker) markDirty() {
	u.dirty.Store(true)
}

// RunPersistence flushes dirty state at most once per usageFlushInterval until
// ctx is cancelled, then flushes once more. Callers should still call Flush
// explicitly on shutdown so the final state cannot be lost.
func (u *UsageTracker) RunPersistence(ctx context.Context) {
	ticker := time.NewTicker(usageFlushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			u.flushIfDirty()
		}
	}
}

func (u *UsageTracker) flushIfDirty() {
	if !u.dirty.Load() {
		return
	}
	if err := u.Flush(); err != nil {
		log.Printf("[WARN] save usage failed: %v", err)
	}
}

// Flush writes the current snapshot to disk if anything changed since the last
// successful write. It is safe to call concurrently.
func (u *UsageTracker) Flush() error {
	if !u.dirty.Swap(false) {
		return nil
	}
	if err := u.save(); err != nil {
		// Keep the flag set so the next flush retries instead of silently
		// dropping the update.
		u.dirty.Store(true)
		return err
	}
	return nil
}

// Save is the unconditional write used by tests and callers that need the file
// to exist right now.
func (u *UsageTracker) Save() error {
	u.dirty.Store(false)
	return u.save()
}

func loadUsage() *UsageTracker {
	u := &UsageTracker{}
	data, err := os.ReadFile(usageFile)
	if err != nil {
		return u
	}
	var snap persistedUsage
	if err := json.Unmarshal(data, &snap); err != nil {
		// Never start from a clean slate on top of a corrupt file: keep a copy
		// so the operator can recover the history by hand.
		backup := usageFile + ".bak"
		if renameErr := os.Rename(usageFile, backup); renameErr != nil {
			log.Printf("[WARN] usage file is corrupt (%v) and could not be backed up: %v", err, renameErr)
		} else {
			log.Printf("[WARN] usage file is corrupt (%v); moved it to %s and starting fresh", err, backup)
		}
		return u
	}
	u.TotalRequests.Store(snap.TotalRequests)
	u.PromptTokens.Store(snap.PromptTokens)
	u.CompletionTokens.Store(snap.CompletionTokens)
	u.CacheReadTokens.Store(snap.CacheReadTokens)
	u.CacheWriteTokens.Store(snap.CacheWriteTokens)

	u.accMu.Lock()
	u.ensureMapsLocked()
	for id, entry := range snap.Accounts {
		c := &UsageCounters{}
		c.restore(entry)
		u.accounts[id] = c
	}
	for id, entry := range snap.ClientKeys {
		c := &UsageCounters{}
		c.restore(entry)
		u.clientKeys[id] = c
	}
	for id, q := range snap.Quotas {
		u.quotas[id] = q
	}
	u.accMu.Unlock()
	return u
}

func (u *UsageTracker) save() error {
	u.saveMu.Lock()
	defer u.saveMu.Unlock()

	snap := persistedUsage{UsageSnapshot: u.Snapshot(), Quotas: u.quotaSnapshot()}
	// Compact, not indented: this file is machine state, and pretty-printing
	// it roughly doubles the bytes written on every flush.
	data, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	tmp := usageFile + ".tmp"
	// 0600: the file carries per-account and per-client-key usage, which is
	// operator data rather than world-readable state.
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, usageFile)
}
