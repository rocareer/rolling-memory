// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package localvault

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

	core "github.com/rocareer/rolling-memory/internal/core"
	_ "modernc.org/sqlite"
)

func openSnapshotEngineTestVault(t *testing.T, name string) *Vault {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), name)
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	v, err := NewVault(db, []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(v.CloseRuntimeSnapshots)
	return v
}

func saveSnapshotEngineDocs(t *testing.T, v *Vault, from, to int) {
	t.Helper()
	for i := from; i < to; i++ {
		text := fmt.Sprintf("baseline unit %d records the alpha beta ledger and unique%03d.", i, i)
		if err := v.SaveLocalPlaintext(fmt.Sprintf("doc-%03d", i), 1700000000+int64(i)*3600, text, nil, IngestResponse{Layer: "episodic"}); err != nil {
			t.Fatal(err)
		}
	}
}

// TestReplacedBaselineEngineUnmapsAfterSnapshotRetirement pins the baseline
// ownership contract: replacing the shared .zcsr handle and retiring the
// previous corpus snapshot must unmap the old baseline instead of leaking one
// mapping per publication.
func TestReplacedBaselineEngineUnmapsAfterSnapshotRetirement(t *testing.T) {
	ctx := context.Background()
	v := openSnapshotEngineTestVault(t, "baseline-swap.sqlite")

	saveSnapshotEngineDocs(t, v, 0, 8)
	if _, err := v.RebuildCorpusSnapshot(ctx); err != nil {
		t.Fatalf("first publish: %v", err)
	}
	if err := v.WarmupCorpusSnapshot(ctx); err != nil {
		t.Fatalf("first warmup: %v", err)
	}
	v.slotCacheMu.RLock()
	first := v.snapRef.csrEngine
	v.slotCacheMu.RUnlock()
	if first == nil || !first.HasMmapSegment() {
		t.Fatalf("first baseline is not mmap-backed: %v", first)
	}

	saveSnapshotEngineDocs(t, v, 8, 16)
	if _, err := v.RebuildCorpusSnapshot(ctx); err != nil {
		t.Fatalf("second publish: %v", err)
	}
	if err := v.WarmupCorpusSnapshot(ctx); err != nil {
		t.Fatalf("second warmup: %v", err)
	}
	v.slotCacheMu.RLock()
	second := v.snapRef.csrEngine
	v.slotCacheMu.RUnlock()
	if second == nil {
		t.Fatal("second baseline is missing")
	}
	if second == first {
		t.Fatal("the baseline handle was not replaced across generations")
	}
	if first.HasMmapSegment() {
		t.Fatal("the replaced baseline is still mapped after its snapshot was retired")
	}
}

// TestCloseRuntimeSnapshotsUnmapsBaselineEngine pins shutdown behaviour: the
// non-published baseline handle must unmap with the runtime snapshots too.
func TestCloseRuntimeSnapshotsUnmapsBaselineEngine(t *testing.T) {
	ctx := context.Background()
	v := openSnapshotEngineTestVault(t, "baseline-close.sqlite")

	saveSnapshotEngineDocs(t, v, 0, 8)
	if _, err := v.RebuildCorpusSnapshot(ctx); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if err := v.WarmupCorpusSnapshot(ctx); err != nil {
		t.Fatalf("warmup: %v", err)
	}
	v.slotCacheMu.RLock()
	baseline := v.snapRef.csrEngine
	v.slotCacheMu.RUnlock()
	if baseline == nil || !baseline.HasMmapSegment() {
		t.Fatalf("baseline is not mmap-backed: %v", baseline)
	}

	v.CloseRuntimeSnapshots()
	if baseline.HasMmapSegment() {
		t.Fatal("CloseRuntimeSnapshots left the baseline engine mapped")
	}
	if snap, err := v.loadCorpusSnapshot(ctx); err != nil || snap != nil {
		t.Fatal("a closed runtime must not load another baseline snapshot")
	}
}

// TestCachedBaselineHoldSurvivesCacheDrop pins the bridge between opening or
// reusing a baseline and attaching it to a snapshot: clearing the cache in
// between must not unmap an engine a builder is still projecting from.
func TestCachedBaselineHoldSurvivesCacheDrop(t *testing.T) {
	ctx := context.Background()
	v := openSnapshotEngineTestVault(t, "baseline-hold.sqlite")

	saveSnapshotEngineDocs(t, v, 0, 8)
	if _, err := v.RebuildCorpusSnapshot(ctx); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if err := v.WarmupCorpusSnapshot(ctx); err != nil {
		t.Fatalf("warmup: %v", err)
	}

	eng, _, hold, ok := v.acquireCachedCSREngine(v.mainDBPath())
	if !ok || eng == nil || !eng.HasMmapSegment() {
		t.Fatalf("baseline handle: ok=%v eng=%v", ok, eng)
	}
	released := false
	release := func() {
		if !released {
			released = true
			hold()
		}
	}
	defer release()

	v.clearSlotCache()
	if !eng.HasMmapSegment() {
		t.Fatal("clearing the cache unmapped an engine a builder still holds")
	}
	release()
	if eng.HasMmapSegment() {
		t.Fatal("the final hold release must unmap the baseline")
	}
}

// TestEmptyCorpusEarlyReturnReleasesLease pins the lease contract for the
// empty-corpus early returns: every consumer that leases a snapshot must
// return the lease even when it cannot project anything.
func TestEmptyCorpusEarlyReturnReleasesLease(t *testing.T) {
	ctx := context.Background()
	v := openSnapshotEngineTestVault(t, "empty-lease.sqlite")

	if err := v.WithCoreEngine(ctx, func(*core.Engine) error { return nil }); err == nil {
		t.Fatal("an empty corpus must reject the engine callback")
	}
	if _, _, ok := v.ArchiveVoidProbe("probe"); ok {
		t.Fatal("an empty corpus must not produce void certificates")
	}
	transport := NewLocalVaultTransport(v)
	if _, err := transport.QueryBlindManifold(ctx, BlindPulsePacket{}, 5); err != nil {
		t.Fatalf("blind manifold on an empty vault: %v", err)
	}

	v.slotCacheMu.RLock()
	snap := v.snapRef.snap
	v.slotCacheMu.RUnlock()
	if snap == nil {
		t.Fatal("expected a cached empty snapshot")
	}
	snap.lifecycleMu.Lock()
	readers := snap.readers
	snap.lifecycleMu.Unlock()
	if readers != 0 {
		t.Fatalf("empty-corpus early returns leaked %d reader lease(s)", readers)
	}
}
