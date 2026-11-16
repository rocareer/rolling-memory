package thinclient

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// TestRealVaultWriteThenQueryLatency is a manual latency probe against a real
// vault copy (set RM_LATENCY_DB to its path; skipped otherwise). It reports the
// write-then-query recall latency under (a) the generation-checked snapshot
// path and (b) a forced snap=nil discard emulating the previous behaviour.
func TestRealVaultWriteThenQueryLatency(t *testing.T) {
	dbPath := os.Getenv("RM_LATENCY_DB")
	if dbPath == "" {
		t.Skip("set RM_LATENCY_DB to a real vault copy")
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	v, err := NewVault(db, nil)
	if err != nil {
		t.Fatalf("NewVault: %v", err)
	}

	t0 := time.Now()
	snap, err := v.loadCorpusSnapshot(context.Background())
	if err != nil {
		t.Fatalf("cold snapshot: %v", err)
	}
	t.Logf("cold snapshot: %v (docs=%d)", time.Since(t0), len(snap.docs))

	query := "性能 优化 检索 延迟"
	warm := func(label string) {
		ts := time.Now()
		hits, _ := v.SearchClientSDKDefault(query, 20)
		t.Logf("%-28s %v (%d hits)", label, time.Since(ts), len(hits))
	}
	warm("steady query (pre-write)")

	saveOne := func(i int) {
		text := fmt.Sprintf("Latency probe entry %d: measuring write-then-query recall path.", i)
		id := fmt.Sprintf("latency_probe_%d_%d", time.Now().UnixNano(), i)
		if err := v.SaveLocalPlaintext(id, time.Now().Unix(), text, SplitSentences(text), CloudIngestResponse{AddressURI: "mem://" + id}); err != nil {
			t.Fatalf("save: %v", err)
		}
	}

	// (a) New path: write bumps the generation; foreground query re-verifies
	// fingerprints and reuses component caches.
	for i := 0; i < 3; i++ {
		saveOne(i)
		ts := time.Now()
		hits, _ := v.SearchClientSDKDefault(query, 20)
		t.Logf("write->query (gen path) #%d:  %v (%d hits)", i, time.Since(ts), len(hits))
	}

	// (b) Old behaviour emulation: discard the snapshot entirely.
	for i := 0; i < 2; i++ {
		saveOne(10 + i)
		v.slotCacheMu.Lock()
		v.snapRef.snap = nil
		v.slotCacheMu.Unlock()
		ts := time.Now()
		hits, _ := v.SearchClientSDKDefault(query, 20)
		t.Logf("write->query (nil path) #%d:  %v (%d hits)", i, time.Since(ts), len(hits))
	}

	// (c) Plan-B payoff: write, let the debounced background refresh finish,
	// then query — the foreground should hit a warm, re-verified snapshot.
	saveOne(20)
	time.Sleep(600 * time.Millisecond)
	warm("query after bg refresh")

	// (d) Dominant real-world write: an @ref: episodic pointer (inline docs
	// unchanged), which should keep the inline component cache valid.
	ptr := fmt.Sprintf("@ref:zcode:probe:%d#1", time.Now().UnixNano())
	id := fmt.Sprintf("latency_ptr_%d", time.Now().UnixNano())
	if err := v.SaveLocalEpisodicPointer(id, time.Now().Unix(), ptr, "probe-turn", CloudIngestResponse{AddressURI: "mem://" + id}); err != nil {
		t.Fatalf("pointer save: %v", err)
	}
	ts := time.Now()
	hits, _ := v.SearchClientSDKDefault(query, 20)
	t.Logf("ptr-write->query (gen path):  %v (%d hits)", time.Since(ts), len(hits))
}
