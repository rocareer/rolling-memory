// plugin: rolling-memory v0.0.58
package rollingmemory

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func efficacyTestBinary(t *testing.T, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rolling-memory")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatalf("write binary: %v", err)
	}
	return path
}

func efficacyWait(t *testing.T, key efficacyKey, status string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		result := collectEfficacySnapshot(key.home, key.dbPath, key.scope, key.harness, key.limit, false)
		if result["status"] == status {
			return result
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("snapshot did not reach %s for %#v", status, key)
	return nil
}

func efficacyReset(t *testing.T) {
	t.Helper()
	waitDeadline := time.Now().Add(5 * time.Second)
	for {
		efficacyCacheMu.Lock()
		if efficacyRunning == 0 {
			efficacyCache = map[efficacyKey]*efficacyCacheEntry{}
			efficacyCacheVersion = 0
			efficacyCacheMu.Unlock()
			return
		}
		efficacyCacheMu.Unlock()
		if !time.Now().Before(waitDeadline) {
			t.Fatal("previous efficacy tasks still running")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestEfficacySnapshotReadDoesNotRefresh(t *testing.T) {
	efficacyReset(t)
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	if err := os.WriteFile(calls, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	bin := efficacyTestBinary(t, fmt.Sprintf("printf 'call\\n' >> %q\nprintf '{\"evaluated_at\":\"2026-10-31 12:00:00\",\"total_sessions\":5}'", calls))
	t.Setenv("ROLLING_MEMORY_BIN", bin)
	key := efficacyKey{home: dir, dbPath: "db", scope: "scope", limit: 20}
	assertCalls := func(want int) {
		t.Helper()
		efficacyCacheMu.Lock()
		running := efficacyRunning
		efficacyCacheMu.Unlock()
		data, err := os.ReadFile(calls)
		if err != nil || running != 0 || strings.Count(string(data), "call") != want {
			t.Fatalf("calls = %q, running = %d, err = %v, want %d calls and no running tasks", data, running, err, want)
		}
	}
	for i := 0; i < 100; i++ {
		got := collectEfficacySnapshot(key.home, key.dbPath, key.scope, key.harness, key.limit, false)
		if got["status"] != "pending" || got["evaluated_at"] != "" {
			t.Fatalf("cold read = %#v", got)
		}
	}
	assertCalls(0)
	collectEfficacySnapshot(key.home, key.dbPath, key.scope, key.harness, key.limit, true)
	efficacyWait(t, key, "ready")
	assertCalls(1)
	for i := 0; i < 100; i++ {
		got := collectEfficacySnapshot(key.home, key.dbPath, key.scope, key.harness, key.limit, false)
		if got["status"] != "ready" || got["total_sessions"] != float64(5) {
			t.Fatalf("cached read = %#v", got)
		}
	}
	assertCalls(1)
	efficacyCacheMu.Lock()
	efficacyCache[key].at = time.Now().Add(-efficacyRefreshInterval - time.Second)
	efficacyCacheMu.Unlock()
	for i := 0; i < 100; i++ {
		got := collectEfficacySnapshot(key.home, key.dbPath, key.scope, key.harness, key.limit, false)
		if got["status"] != "stale" || got["total_sessions"] != float64(5) {
			t.Fatalf("expired read = %#v", got)
		}
	}
	assertCalls(1)
	collectEfficacySnapshot(key.home, key.dbPath, key.scope, key.harness, key.limit, true)
	efficacyWait(t, key, "ready")
	assertCalls(2)
}

func TestEfficacySnapshotSlowSingleFlightAndLimit(t *testing.T) {
	efficacyReset(t)
	dir := t.TempDir()
	logPath := filepath.Join(dir, "calls")
	gate := filepath.Join(dir, "gate")
	bin := efficacyTestBinary(t, fmt.Sprintf("printf 'call\\n' >> %q\nwhile [ ! -f %q ]; do sleep 0.05; done\nprintf '{\"evaluated_at\":\"2026-10-31 12:00:00\",\"total_sessions\":7}'", logPath, gate))
	t.Setenv("ROLLING_MEMORY_BIN", bin)
	key := efficacyKey{home: dir, dbPath: "db", scope: "scope", limit: 20}
	start := time.Now()
	result := collectEfficacySnapshot(key.home, key.dbPath, key.scope, key.harness, key.limit, true)
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("slow snapshot blocked for %v", elapsed)
	}
	if result["status"] != "pending" || result["evaluated_at"] != "" || result["sessions"] == nil {
		t.Fatalf("unexpected pending snapshot: %#v", result)
	}
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			collectEfficacySnapshot(key.home, key.dbPath, key.scope, key.harness, key.limit, true)
		}()
	}
	wg.Wait()
	other := efficacyKey{home: dir, dbPath: "db2", scope: "scope", limit: 20}
	third := efficacyKey{home: dir, dbPath: "db3", scope: "scope", limit: 20}
	collectEfficacySnapshot(other.home, other.dbPath, other.scope, "", 20, true)
	collectEfficacySnapshot(third.home, third.dbPath, third.scope, "", 20, true)
	efficacyCacheMu.Lock()
	if efficacyRunning != efficacyMaxConcurrent || efficacyCache[third] != nil {
		t.Errorf("running = %d, third entry = %#v", efficacyRunning, efficacyCache[third])
	}
	efficacyCacheMu.Unlock()
	if err := os.WriteFile(gate, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	got := efficacyWait(t, key, "ready")
	efficacyWait(t, other, "ready")
	if got["evaluated_at"] != "2026-10-31 12:00:00" || got["total_sessions"] != float64(7) {
		t.Fatalf("unexpected ready snapshot: %#v", got)
	}
	calls, err := os.ReadFile(logPath)
	if err != nil || strings.Count(string(calls), "call") != 2 {
		t.Fatalf("calls = %q, err = %v, want 2", calls, err)
	}
	collectEfficacySnapshot(third.home, third.dbPath, third.scope, third.harness, third.limit, true)
	if got := efficacyWait(t, third, "ready"); got["total_sessions"] != float64(7) {
		t.Fatalf("third key did not start after capacity became available: %#v", got)
	}
}

func TestEfficacySnapshotKeyIsolation(t *testing.T) {
	efficacyReset(t)
	bin := efficacyTestBinary(t, `printf '{"evaluated_at":"2026-10-31 12:00:00","total_sessions":3}'`)
	t.Setenv("ROLLING_MEMORY_BIN", bin)
	base := efficacyKey{home: "home", dbPath: "db", scope: "scope", harness: "harness", limit: 20}
	keys := []efficacyKey{
		base,
		{home: "other", dbPath: "db", scope: "scope", harness: "harness", limit: 20},
		{home: "home", dbPath: "other", scope: "scope", harness: "harness", limit: 20},
		{home: "home", dbPath: "db", scope: "other", harness: "harness", limit: 20},
		{home: "home", dbPath: "db", scope: "scope", harness: "other", limit: 20},
		{home: "home", dbPath: "db", scope: "scope", harness: "harness", limit: 21},
	}
	for _, key := range keys {
		initial := collectEfficacySnapshot(key.home, key.dbPath, key.scope, key.harness, key.limit, false)
		if initial["status"] != "pending" || initial["evaluated_at"] != "" {
			t.Fatalf("new key returned another snapshot: %#v", initial)
		}
		collectEfficacySnapshot(key.home, key.dbPath, key.scope, key.harness, key.limit, true)
		efficacyWait(t, key, "ready")
	}
	efficacyCacheMu.Lock()
	if len(efficacyCache) != len(keys) {
		t.Errorf("cache entries = %d, want %d", len(efficacyCache), len(keys))
	}
	efficacyCacheMu.Unlock()
}

func TestEfficacySnapshotFailureBackoffAndStale(t *testing.T) {
	efficacyReset(t)
	dir := t.TempDir()
	failure := filepath.Join(dir, "failure")
	calls := filepath.Join(dir, "calls")
	bin := efficacyTestBinary(t, fmt.Sprintf("printf 'call\\n' >> %q\nif [ -f %q ]; then printf 'secret output' >&2; exit 1; fi\nprintf '{\"evaluated_at\":\"2026-10-31 12:00:00\",\"total_sessions\":9}'", calls, failure))
	t.Setenv("ROLLING_MEMORY_BIN", bin)
	key := efficacyKey{home: dir, dbPath: "db", scope: "scope", limit: 20}
	collectEfficacySnapshot(key.home, key.dbPath, key.scope, key.harness, key.limit, true)
	efficacyWait(t, key, "ready")
	if err := os.WriteFile(failure, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	collectEfficacySnapshot(key.home, key.dbPath, key.scope, "", key.limit, true)
	deadline := time.Now().Add(5 * time.Second)
	for {
		efficacyCacheMu.Lock()
		failed := efficacyCache[key].err != ""
		efficacyCacheMu.Unlock()
		if failed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("refresh did not fail")
		}
		time.Sleep(10 * time.Millisecond)
	}
	stale := collectEfficacySnapshot(key.home, key.dbPath, key.scope, "", key.limit, true)
	if stale["status"] != "stale" || stale["evaluated_at"] != "2026-10-31 12:00:00" || stale["total_sessions"] != float64(9) || stale["error"] != "efficacy evaluation failed; retry later" || stale["retry_after_seconds"] == nil {
		t.Fatalf("unexpected stale snapshot: %#v", stale)
	}
	other := efficacyKey{home: dir, dbPath: "different", scope: "scope", limit: 20}
	collectEfficacySnapshot(other.home, other.dbPath, other.scope, other.harness, other.limit, true)
	failed := efficacyWait(t, other, "error")
	if failed["evaluated_at"] != "" || failed["total_sessions"] != 0 || strings.Contains(fmt.Sprint(failed), "secret") {
		t.Fatalf("unexpected failed snapshot: %#v", failed)
	}
	for i := 0; i < 30; i++ {
		collectEfficacySnapshot(key.home, key.dbPath, key.scope, "", key.limit, true)
		collectEfficacySnapshot(other.home, other.dbPath, other.scope, "", other.limit, true)
	}
	data, err := os.ReadFile(calls)
	if err != nil || strings.Count(string(data), "call") != 3 {
		t.Fatalf("backoff calls = %q, err = %v, want 3", data, err)
	}
	if err := os.Remove(failure); err != nil {
		t.Fatal(err)
	}
	efficacyCacheMu.Lock()
	efficacyCache[other].retryAt = time.Now().Add(-time.Second)
	efficacyCacheMu.Unlock()
	for i := 0; i < 30; i++ {
		if got := collectEfficacySnapshot(other.home, other.dbPath, other.scope, other.harness, other.limit, false); got["status"] != "error" {
			t.Fatalf("poll after backoff = %#v", got)
		}
	}
	data, err = os.ReadFile(calls)
	if err != nil || strings.Count(string(data), "call") != 3 {
		t.Fatalf("poll after backoff calls = %q, err = %v, want 3", data, err)
	}
	collectEfficacySnapshot(other.home, other.dbPath, other.scope, other.harness, other.limit, true)
	recovered := efficacyWait(t, other, "ready")
	if recovered["error"] != nil || recovered["evaluated_at"] != "2026-10-31 12:00:00" {
		t.Fatalf("unexpected recovered snapshot: %#v", recovered)
	}
}

func TestEfficacySnapshotInvalidOutputAndCapacity(t *testing.T) {
	efficacyReset(t)
	bin := efficacyTestBinary(t, `printf 'secret invalid json'`)
	t.Setenv("ROLLING_MEMORY_BIN", bin)
	key := efficacyKey{home: t.TempDir(), dbPath: "db", scope: "scope", limit: 20}
	collectEfficacySnapshot(key.home, key.dbPath, key.scope, key.harness, key.limit, true)
	failed := efficacyWait(t, key, "error")
	if failed["error"] != "efficacy evaluation returned invalid data; retry later" || strings.Contains(fmt.Sprint(failed), "secret") {
		t.Fatalf("invalid output leaked: %#v", failed)
	}
	for _, payload := range []string{`{}`, `{"evaluated_at":""}`, `{"evaluated_at":"not-a-date"}`} {
		badBin := efficacyTestBinary(t, "printf '%s' '"+payload+"'")
		t.Setenv("ROLLING_MEMORY_BIN", badBin)
		if _, errText := runEfficacySnapshot(key, key.scope, time.Second); errText != "efficacy evaluation returned invalid data" {
			t.Fatalf("invalid evaluated_at accepted: %s", payload)
		}
	}
	efficacyCacheMu.Lock()
	for i := 0; i < efficacyMaxEntries-1; i++ {
		efficacyCache[efficacyKey{dbPath: fmt.Sprint(i)}] = &efficacyCacheEntry{lastAccess: time.Now()}
	}
	efficacyCacheMu.Unlock()
	blocked := collectEfficacySnapshot(key.home, "other", key.scope, "", key.limit, false)
	if blocked["status"] != "pending" {
		t.Fatalf("capacity snapshot = %#v", blocked)
	}
	efficacyCacheMu.Lock()
	if len(efficacyCache) != efficacyMaxEntries || efficacyCache[key] == nil {
		t.Errorf("capacity lost failed entry or exceeded limit: %d", len(efficacyCache))
	}
	efficacyCacheMu.Unlock()
}

func TestEfficacyCommandTimeout(t *testing.T) {
	bin := efficacyTestBinary(t, "exec sleep 10")
	t.Setenv("ROLLING_MEMORY_BIN", bin)
	start := time.Now()
	result, errText := runEfficacySnapshot(efficacyKey{home: t.TempDir(), dbPath: "db", limit: 20}, "scope", 100*time.Millisecond)
	if result != nil || errText != "efficacy evaluation timed out" {
		t.Fatalf("timeout result = %#v, error = %q", result, errText)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("timeout took %v", elapsed)
	}
}
