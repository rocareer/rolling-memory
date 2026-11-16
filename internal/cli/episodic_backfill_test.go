// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package cli

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/rocareer/rolling-memory/internal/api"
	"github.com/rocareer/rolling-memory/internal/config"
	"github.com/rocareer/rolling-memory/internal/episodicunit"
	"github.com/rocareer/rolling-memory/internal/thinclient"
)

func TestAutoWatchLeaseOtherProcess(t *testing.T) {
	if path := os.Getenv("RM_TEST_LEASE_DB"); path != "" {
		db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)")
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		if lease, ok := acquireAutoWatchLease(db, func() {}); ok {
			releaseAutoWatchLease(db, lease)
			t.Fatal("child acquired an active lease")
		}
		return
	}
	path := filepath.Join(t.TempDir(), "lease.sqlite")
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	lease, ok := acquireAutoWatchLease(db, func() {})
	if !ok {
		t.Fatal("parent could not acquire lease")
	}
	defer releaseAutoWatchLease(db, lease)
	cmd := exec.Command(os.Args[0], "-test.run=^TestAutoWatchLeaseOtherProcess$")
	cmd.Env = append(os.Environ(), "RM_TEST_LEASE_DB="+path)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child process: %v\n%s", err, output)
	}
}

func TestAutoWatchLeaseConcurrentAndFencedRelease(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "lease.sqlite")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE thin_vault_meta (k TEXT PRIMARY KEY, v BLOB NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	const workers = 24
	start := make(chan struct{})
	leases := make(chan *autoWatchLease, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			lease, ok := acquireAutoWatchLease(db, func() {})
			if ok {
				leases <- lease
			}
		}()
	}
	close(start)
	wg.Wait()
	close(leases)
	var winner *autoWatchLease
	count := 0
	for lease := range leases {
		winner = lease
		count++
	}
	if count != 1 {
		t.Fatalf("concurrent leases=%d, want 1", count)
	}
	if _, ok := acquireAutoWatchLease(db, func() {}); ok {
		t.Fatal("same-process lease acquired twice")
	}
	if _, err := db.Exec(`UPDATE thin_vault_meta SET v = 'other:9999999999:token' WHERE k = 'autowatch_lease'`); err != nil {
		t.Fatal(err)
	}
	releaseAutoWatchLease(db, winner)
	var value string
	if err := db.QueryRow(`SELECT v FROM thin_vault_meta WHERE k = 'autowatch_lease'`).Scan(&value); err != nil || value != "other:9999999999:token" {
		t.Fatalf("stale release modified current lease: %q %v", value, err)
	}
	if _, err := db.Exec(`UPDATE thin_vault_meta SET v = 'corrupt' WHERE k = 'autowatch_lease'`); err != nil {
		t.Fatal(err)
	}
	if lease, ok := acquireAutoWatchLease(db, func() {}); ok {
		releaseAutoWatchLease(db, lease)
		t.Fatal("corrupt lease was overwritten")
	}
	if _, err := db.Exec(`UPDATE thin_vault_meta SET v = '123:1' WHERE k = 'autowatch_lease'`); err != nil {
		t.Fatal(err)
	}
	lease, ok := acquireAutoWatchLease(db, func() {})
	if !ok {
		t.Fatal("expired lease could not be acquired")
	}
	releaseAutoWatchLease(db, lease)
}

func TestEpisodicCheckpointConcurrentWriters(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "checkpoint.sqlite")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE thin_vault_meta (k TEXT PRIMARY KEY, v BLOB NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	for _, exists := range []bool{false, true} {
		key := fmt.Sprintf("checkpoint-%t", exists)
		if exists {
			if _, err := db.Exec(`INSERT INTO thin_vault_meta(k, v) VALUES (?, ?)`, key, "old"); err != nil {
				t.Fatal(err)
			}
		}
		start := make(chan struct{})
		results := make(chan error, 2)
		var wg sync.WaitGroup
		for _, next := range []string{"", "advanced"} {
			wg.Add(1)
			go func(next string) {
				defer wg.Done()
				<-start
				results <- saveEpisodicCheckpoint(db, key, []byte("old"), exists, []byte(next))
			}(next)
		}
		close(start)
		wg.Wait()
		close(results)
		successes := 0
		for err := range results {
			if err == nil {
				successes++
			} else if !strings.Contains(err.Error(), "checkpoint changed") {
				t.Fatal(err)
			}
		}
		if successes != 1 {
			t.Fatalf("exists=%v: %d writers succeeded", exists, successes)
		}
	}
}

func TestEpisodicBackfillTickResumesAndRetries(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{DB: filepath.Join(dir, "memory.db"), Scope: "mem:backfill", Mode: "local"}
	memory, err := api.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer memory.Close()
	tc, err := memory.ThinClient()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "source.jsonl")
	for i := 0; i < reconcileDefaultLimit+1; i++ {
		text := fmt.Sprintf("[user] historical entry %d", i)
		id := episodicunit.DocID(cfg.Scope, text)
		ref := fmt.Sprintf("@ref:jsonl:%s:%d#0", path, i)
		if err := tc.Vault.SaveLocalEpisodicPointer(id, 1, ref, "", thinclient.CloudIngestResponse{}); err != nil {
			t.Fatal(err)
		}
	}
	first, err := runEpisodicIndexBackfillTick(context.Background(), memory)
	if err != nil || first.Scanned != reconcileDefaultLimit || first.Unavailable != reconcileDefaultLimit {
		t.Fatalf("first tick: %+v %v", first, err)
	}
	second, err := runEpisodicIndexBackfillTick(context.Background(), memory)
	if err != nil || second.Scanned != 1 || second.Unavailable != 1 || second.Cursor <= first.Cursor {
		t.Fatalf("resume: %+v %v", second, err)
	}
	third, err := runEpisodicIndexBackfillTick(context.Background(), memory)
	if err != nil || third.Scanned != reconcileDefaultLimit || third.Cursor != first.Cursor {
		t.Fatalf("cycle retry: %+v %v", third, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("source modified: %v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := runEpisodicIndexBackfillTick(cancelled, memory); err == nil {
		t.Fatal("cancelled tick succeeded")
	}
}

func TestEpisodicBackfillTickStopsOnIndexWriteError(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{DB: filepath.Join(dir, "memory.db"), Scope: "mem:backfill", Mode: "local"}
	memory, err := api.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer memory.Close()
	tc, err := memory.ThinClient()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "source.jsonl")
	var lines []string
	var ids []string
	for i := 0; i < 2; i++ {
		text := fmt.Sprintf("historical entry %d", i)
		lines = append(lines, fmt.Sprintf(`{"type":"user","message":{"role":"user","content":%q}}`, text))
		id := episodicunit.DocID(cfg.Scope, "[user] "+text)
		ids = append(ids, id)
		ref := fmt.Sprintf("@ref:jsonl:%s:%d#0", path, i)
		if err := tc.Vault.SaveLocalEpisodicPointer(id, 1, ref, "", thinclient.CloudIngestResponse{}); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sort.Strings(ids)
	db := memory.Store.DB()
	if _, err := db.Exec(`CREATE TRIGGER backfill_test_failure BEFORE INSERT ON thin_vault_episodic_index WHEN NEW.doc_id = '` + ids[0] + `' BEGIN SELECT RAISE(ABORT, 'write failed'); END`); err != nil {
		t.Fatal(err)
	}
	page, err := runEpisodicIndexBackfillTick(context.Background(), memory)
	if err == nil || page.Scanned != 0 || page.Cursor != "" {
		t.Fatalf("expected fail-closed checkpoint: %+v %v", page, err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM thin_vault_episodic_index`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("unexpected index writes: %d %v", count, err)
	}
	if _, err := db.Exec(`DROP TRIGGER backfill_test_failure`); err != nil {
		t.Fatal(err)
	}
	page, err = runEpisodicIndexBackfillTick(context.Background(), memory)
	if err != nil || page.Scanned != 2 || page.Indexed != 2 {
		t.Fatalf("retry after repair: %+v %v", page, err)
	}
}

func TestEpisodicSyncPauseStepResumeAndProgress(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	cfg := &config.Config{DB: filepath.Join(dir, "memory.db"), Scope: "mem:pause-test", Mode: "local"}
	memory, err := api.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer memory.Close()
	tc, err := memory.ThinClient()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "session.jsonl")
	body := `{"type":"user","message":{"role":"user","content":"pause resume witness"}}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	id := episodicunit.DocID(cfg.Scope, "[user] pause resume witness")
	ref := "@ref:jsonl:" + path + ":0#0"
	if err := tc.Vault.SaveLocalEpisodicPointer(id, 1, ref, "", thinclient.CloudIngestResponse{}); err != nil {
		t.Fatal(err)
	}
	db := memory.Store.DB()
	if err := SetEpisodicSyncPaused(db, true); err != nil {
		t.Fatal(err)
	}
	if !IsEpisodicSyncPaused(db, cfg.Path) {
		t.Fatal("expected paused=true")
	}
	if ran, _, _ := RunAutoWatchTick(context.Background(), memory, 0, 0); ran {
		t.Fatal("paused auto-watch tick should not run")
	}
	st, err := inspectEnvHealth(context.Background(), memory, "", false)
	if err != nil || st.EpisodicIndex == nil {
		t.Fatalf("inspectEnvHealth: %+v %v", st, err)
	}
	if !st.EpisodicIndex.Paused || st.EpisodicIndex.TotalDocs != 1 || st.EpisodicIndex.Compatible != 0 || st.EpisodicIndex.Missing != 1 {
		t.Fatalf("unexpected paused status: %+v", st.EpisodicIndex)
	}
	rep, err := runEpisodicIndexBackfillTick(context.Background(), memory)
	if err != nil || rep.Indexed != 1 {
		t.Fatalf("step backfill while paused: %+v %v", rep, err)
	}
	if err := SetEpisodicSyncPaused(db, false); err != nil {
		t.Fatal(err)
	}
	st, err = inspectEnvHealth(context.Background(), memory, "", false)
	if err != nil || st.EpisodicIndex == nil {
		t.Fatalf("inspectEnvHealth after resume: %+v %v", st, err)
	}
	if st.EpisodicIndex.Paused || st.EpisodicIndex.Compatible != 1 || st.EpisodicIndex.CoveragePct != 100.0 {
		t.Fatalf("unexpected resumed status: %+v", st.EpisodicIndex)
	}
}
