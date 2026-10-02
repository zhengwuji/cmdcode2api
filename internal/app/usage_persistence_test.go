package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// TestMarkDirtyDoesNotWrite pins the debounce contract: the request path only
// sets a flag, so a burst of requests cannot each trigger a full rewrite.
func TestMarkDirtyDoesNotWrite(t *testing.T) {
	redirectUsageFile(t)
	u := &UsageTracker{}

	u.Recorder("acct-1", "key-1").Record(100, 20, 0, 0)
	if _, err := os.Stat(usageFile); !os.IsNotExist(err) {
		t.Fatalf("Record wrote the usage file directly (stat err = %v)", err)
	}

	if err := u.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if _, err := os.Stat(usageFile); err != nil {
		t.Fatalf("Flush did not write the usage file: %v", err)
	}
}

// TestFlushSkipsCleanTracker keeps a no-op flush from rewriting the file.
func TestFlushSkipsCleanTracker(t *testing.T) {
	redirectUsageFile(t)
	u := &UsageTracker{}
	u.Recorder("acct-1", "").Record(1, 1, 0, 0)
	if err := u.Flush(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(usageFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := u.Flush(); err != nil {
		t.Fatal(err)
	}
	again, err := os.Stat(usageFile)
	if err != nil {
		t.Fatal(err)
	}
	if !again.ModTime().Equal(info.ModTime()) {
		t.Fatalf("a clean Flush rewrote the file (%v -> %v)", info.ModTime(), again.ModTime())
	}
}

// TestRunPersistenceFlushesDirtyState covers the background writer actually
// running: mark dirty, and the ticker must land the file without an explicit
// Flush.
func TestRunPersistenceFlushesDirtyState(t *testing.T) {
	redirectUsageFile(t)
	u := &UsageTracker{}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		u.RunPersistence(ctx)
		close(done)
	}()

	u.Recorder("acct-1", "").Record(500, 100, 0, 0)

	deadline := time.Now().Add(3 * usageFlushInterval)
	for {
		if _, err := os.Stat(usageFile); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("RunPersistence never flushed the dirty tracker")
		}
		time.Sleep(20 * time.Millisecond)
	}

	reloaded := loadUsage()
	if reloaded.TotalRequests.Load() != 1 || reloaded.PromptTokens.Load() != 500 {
		t.Fatalf("persisted totals = %d/%d, want 1/500",
			reloaded.TotalRequests.Load(), reloaded.PromptTokens.Load())
	}
	if got := reloaded.AccountUsage("acct-1"); got.Requests != 1 || got.PromptTokens != 500 {
		t.Fatalf("persisted account counters = %+v", got)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("RunPersistence did not return after cancel")
	}
}

// TestLoadUsageBacksUpCorruptFile makes sure a damaged usage.json is preserved
// instead of being silently replaced by an empty tracker.
func TestLoadUsageBacksUpCorruptFile(t *testing.T) {
	redirectUsageFile(t)
	if err := os.WriteFile(usageFile, []byte(`{"total_requests": `), 0600); err != nil {
		t.Fatal(err)
	}

	u := loadUsage()
	if u.TotalRequests.Load() != 0 {
		t.Fatalf("corrupt file produced counters: %d", u.TotalRequests.Load())
	}
	if _, err := os.Stat(usageFile); !os.IsNotExist(err) {
		t.Fatalf("corrupt file was not moved aside (stat err = %v)", err)
	}
	backup, err := os.ReadFile(usageFile + ".bak")
	if err != nil {
		t.Fatalf("backup missing: %v", err)
	}
	if string(backup) != `{"total_requests": ` {
		t.Fatalf("backup content = %q", backup)
	}
}

// TestLoadUsageRoundTripsQuotasAndKeys keeps the persisted shape stable across
// a save/load cycle.
func TestLoadUsageRoundTripsQuotasAndKeys(t *testing.T) {
	redirectUsageFile(t)
	u := &UsageTracker{}
	u.Recorder("acct-1", "key-1").Record(10, 5, 2, 1)
	u.SetQuota("acct-1", &QuotaSnapshot{MonthlyCredits: floatPtr(12.5)})
	if err := u.Save(); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(usageFile)
	if err != nil {
		t.Fatal(err)
	}
	var shape map[string]any
	if err := json.Unmarshal(raw, &shape); err != nil {
		t.Fatalf("persisted usage is not valid JSON: %v", err)
	}
	if shape["total_requests"] != float64(1) {
		t.Fatalf("total_requests = %v", shape["total_requests"])
	}
	if _, ok := shape["quotas"]; !ok {
		t.Fatalf("quotas missing from %s", raw)
	}

	reloaded := loadUsage()
	if reloaded.ClientKeyUsage("key-1").Requests != 1 {
		t.Fatalf("client key counters lost: %+v", reloaded.ClientKeyUsage("key-1"))
	}
	if q := reloaded.Quota("acct-1"); q == nil || q.MonthlyCredits == nil || *q.MonthlyCredits != 12.5 {
		t.Fatalf("quota lost: %+v", q)
	}
}

// TestSaveUsesRestrictivePermissions documents that usage.json is operator data.
func TestSaveUsesRestrictivePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permission bits are not meaningful on Windows")
	}
	redirectUsageFile(t)
	u := &UsageTracker{}
	u.Recorder("acct-1", "").Record(1, 1, 0, 0)
	if err := u.Save(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(usageFile)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Fatalf("usage.json mode = %04o, want 0600", perm)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(usageFile), "usage.json.tmp")); !os.IsNotExist(err) {
		t.Fatalf("temp file was left behind (stat err = %v)", err)
	}
}
