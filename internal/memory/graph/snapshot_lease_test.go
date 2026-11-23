// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package graph

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	core "github.com/rocareer/rolling-memory/internal/core"
	"github.com/rocareer/rolling-memory/internal/memory/localvault"
	_ "modernc.org/sqlite"
)

// leaseTestEngine writes one .zcsr generation and returns the mmap-backed
// engine, so lease tests can observe real unmap behaviour through
// HasMmapSegment. The cleanup Close is a no-op after the lifecycle already
// closed the engine (mmapUnmap is cleared by the first Close).
func leaseTestEngine(t *testing.T, name string) *core.Engine {
	t.Helper()
	docs := []core.Doc{
		{
			UnitID:     name + "-a",
			OccurredAt: 100,
			Terms:      map[uint64]core.Term{11: {Hash: 11, Group: 11, Bits: 40, Role: core.RolePrimary}},
			Counts:     map[uint64]int{11: 1},
			Positions:  map[uint64][]core.TermPosition{11: {{Sentence: 0, Slot: 1}}},
		},
		{
			UnitID:     name + "-b",
			OccurredAt: 200,
			Terms:      map[uint64]core.Term{12: {Hash: 12, Group: 12, Bits: 44, Role: core.RolePrimary}},
			Counts:     map[uint64]int{12: 1},
			Positions:  map[uint64][]core.TermPosition{12: {{Sentence: 0, Slot: 1}}},
		},
	}
	memory := core.NewEngine(core.BuildIndex(docs))
	memory.MaterializeClusterState()
	path := filepath.Join(t.TempDir(), name+".zcsr")
	if err := core.WriteDiskClusterCSR(path, memory); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	disk, err := core.OpenDiskClusterEngine(path)
	if err != nil {
		t.Fatalf("open %s: %v", name, err)
	}
	t.Cleanup(func() { _ = disk.Close() })
	if !disk.HasMmapSegment() {
		t.Fatalf("%s must own an mmap segment", name)
	}
	return disk
}

// TestSnapshotRetireWaitsForEngineLeases pins the render-lease contract: a
// retired snapshot keeps its backing mmap until the last active Engine lease
// is returned, and it never hands out new leases afterwards.
func TestSnapshotRetireWaitsForEngineLeases(t *testing.T) {
	disk := leaseTestEngine(t, "lease")
	snap := &Snapshot{engine: disk, engRelease: func() { _ = disk.Close() }}

	eng, release, ok := snap.Engine()
	if !ok || eng != disk {
		t.Fatal("an active snapshot must lease its engine")
	}
	snap.retire()
	if !disk.HasMmapSegment() {
		t.Fatal("retirement must not unmap while a render lease is active")
	}
	release()
	if disk.HasMmapSegment() {
		t.Fatal("the final lease return must release the retired engine")
	}
	if _, _, ok := snap.Engine(); ok {
		t.Fatal("a retired snapshot must not hand out new engine leases")
	}
}

// TestBuilderInvalidationReleasesPublishedGeneration runs the full lease chain
// against a published generation: snapshot replacement retires the old engine
// immediately (no active renders), and builder invalidation retires the active
// one.
func TestBuilderInvalidationReleasesPublishedGeneration(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "graph-lease.sqlite")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	vault, err := localvault.NewVault(db, []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 40; i++ {
		text := fmt.Sprintf("lease unit %d records the alpha beta ledger and unique%03d.", i, i)
		if err := vault.SaveLocalPlaintext(fmt.Sprintf("doc-%03d", i), 1700000000+int64(i)*3600, text, nil, localvault.IngestResponse{Layer: "episodic"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := vault.RebuildCorpusSnapshot(ctx); err != nil {
		t.Fatal(err)
	}
	vault.SetBuildAllowed(false)

	builder := NewBuilder(vault)
	first, err := builder.Snapshot(ctx)
	if err != nil || first.engine == nil {
		t.Fatalf("published snapshot: %v", err)
	}
	firstEng := first.engine
	if !firstEng.HasMmapSegment() {
		t.Fatal("the published generation must own its mmap")
	}

	// Publish a second generation and defeat the reuse window so Snapshot
	// rebuilds and replaces the cached snapshot.
	if err := vault.SaveLocalPlaintext("doc-040", 1700002000, "fresh lease unit forty records the alpha ledger and unique040.", nil, localvault.IngestResponse{Layer: "episodic"}); err != nil {
		t.Fatal(err)
	}
	if _, err := vault.RebuildCorpusSnapshot(ctx); err != nil {
		t.Fatal(err)
	}
	first.BuiltAt = time.Now().Add(-snapshotReuseWindow)
	second, err := builder.Snapshot(ctx)
	if err != nil || second == nil || second == first {
		t.Fatal("a rebuilt generation must replace the cached snapshot")
	}
	if firstEng.HasMmapSegment() {
		t.Fatal("a replaced snapshot without active renders must release its mmap")
	}

	builder.Invalidate()
	if _, _, ok := second.Engine(); ok {
		t.Fatal("an invalidated snapshot must not hand out new engine leases")
	}
	if second.engine == nil || !second.engine.HasMmapSegment() {
		t.Fatal("the active published generation stays vault-owned after the graph drops its lease")
	}
	// The vault owns the final unmap: shutdown retires the active generation
	// once every consumer lease (the graph's included) is back.
	vault.CloseRuntimeSnapshots()
	if second.engine.HasMmapSegment() {
		t.Fatal("vault shutdown must release the generation after the graph lease is returned")
	}
}
