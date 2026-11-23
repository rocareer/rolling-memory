// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package localvault

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	core "github.com/rocareer/rolling-memory/internal/core"
)

func TestPublishedSnapshotIsAsync(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", vaultTestDSN(filepath.Join(t.TempDir(), "async.sqlite")))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	v, err := NewVault(db, []byte("async-key"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(v.CloseRuntimeSnapshots)
	for i := 0; i < 60; i++ {
		text := fmt.Sprintf("unit %d records the alpha beta ledger and unique%03d.", i, i)
		if err := v.SaveLocalPlaintext(fmt.Sprintf("doc-%03d", i), 1700000000+int64(i)*3600, text, nil, IngestResponse{Layer: "episodic"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := v.RebuildCorpusSnapshot(ctx); err != nil {
		t.Fatal(err)
	}
	v.SetBuildAllowed(false)
	if err := v.SaveLocalPlaintext("doc-060", 1700002000, "fresh unit sixty records the alpha ledger and unique060.", nil, IngestResponse{Layer: "episodic"}); err != nil {
		t.Fatal(err)
	}

	before, ok := v.loadPublishedSnapshot(ctx)
	if !ok || before == nil {
		t.Fatal("published generation not loaded")
	}
	assertSearchVisibility(t, v, "unique000", "doc-000", true)
	assertSearchVisibility(t, v, "unique060", "doc-060", false)

	afterWrite, ok := v.loadPublishedSnapshot(ctx)
	if !ok || afterWrite != before {
		t.Fatal("an unpublished write rebuilt the published snapshot")
	}
	if _, err := v.RebuildCorpusSnapshot(ctx); err != nil {
		t.Fatal(err)
	}
	assertSearchVisibility(t, v, "unique060", "doc-060", true)
	afterPublish, ok := v.loadPublishedSnapshot(ctx)
	if !ok || afterPublish == before {
		t.Fatal("a published generation was not picked up")
	}
	if before.coreEngine.HasMmapSegment() {
		t.Fatal("loading a newly published generation must unmap the retired generation")
	}
	if !afterPublish.coreEngine.HasMmapSegment() {
		t.Fatal("the active published generation must retain its mmap")
	}

	if err := v.SaveLocalPlaintext("doc-000", 1700003000, "rewritten unit zero records zqxwvuqwerty and the harbour ledger.", nil, IngestResponse{Layer: "episodic"}); err != nil {
		t.Fatal(err)
	}
	assertSearchVisibility(t, v, "zqxwvuqwerty", "doc-000", false)
	if _, err := v.RebuildCorpusSnapshot(ctx); err != nil {
		t.Fatal(err)
	}
	assertSearchVisibility(t, v, "zqxwvuqwerty", "doc-000", true)
}

func openPublishedLifecycleSnapshot(t *testing.T, name string, hash uint64) *vaultCorpusSnapshot {
	t.Helper()
	docs := []core.Doc{
		{
			UnitID:     name + "-a",
			OccurredAt: 100,
			Terms:      map[uint64]core.Term{hash: {Hash: hash, Group: hash, Bits: 40, Role: core.RolePrimary}},
			Counts:     map[uint64]int{hash: 1},
			Positions:  map[uint64][]core.TermPosition{hash: {{Sentence: 0, Slot: 1}}},
		},
		{
			UnitID:     name + "-b",
			OccurredAt: 200,
			Terms: map[uint64]core.Term{
				hash:     {Hash: hash, Group: hash, Bits: 40, Role: core.RolePrimary},
				hash + 1: {Hash: hash + 1, Group: hash + 1, Bits: 44, Role: core.RolePrimary},
			},
			Counts: map[uint64]int{hash: 1, hash + 1: 1},
			Positions: map[uint64][]core.TermPosition{
				hash:     {{Sentence: 0, Slot: 1}},
				hash + 1: {{Sentence: 0, Slot: 2}},
			},
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
	return &vaultCorpusSnapshot{coreEngine: disk, ownsCoreEngine: true}
}

func searchPublishedLifecycleSnapshot(snap *vaultCorpusSnapshot, hash uint64) bool {
	if snap == nil || snap.coreEngine == nil {
		return false
	}
	worker := snap.coreEngine.CloneForQuery()
	hits := worker.SearchWithPositions(
		map[uint64]core.Term{hash: {Hash: hash, Group: hash, Bits: 40, Role: core.RolePrimary}},
		map[uint64][]core.TermPosition{hash: {{Sentence: 0, Slot: 1}}},
		5,
	)
	return len(hits) > 0
}

func TestPublishedSnapshotReplacementClosesRetiredMmap(t *testing.T) {
	v := &Vault{slotCacheMu: &sync.RWMutex{}, snapRef: newVaultSnapshotRef()}
	first := openPublishedLifecycleSnapshot(t, "first", 11)
	second := openPublishedLifecycleSnapshot(t, "second", 21)

	if !v.installPublishedSnapshot(first, "slot-a") {
		t.Fatal("install first published snapshot")
	}
	if !searchPublishedLifecycleSnapshot(first, 11) {
		t.Fatal("first published snapshot must be searchable before replacement")
	}
	if !v.installPublishedSnapshot(second, "slot-b") {
		t.Fatal("install second published snapshot")
	}
	if first.coreEngine.HasMmapSegment() {
		t.Fatal("replaced generation without readers must release its mmap immediately")
	}
	if !second.coreEngine.HasMmapSegment() || !searchPublishedLifecycleSnapshot(second, 21) {
		t.Fatal("replacement generation must remain mapped and searchable")
	}

	v.CloseRuntimeSnapshots()
	if second.coreEngine.HasMmapSegment() {
		t.Fatal("closing the runtime must release the active published mmap")
	}
	late := openPublishedLifecycleSnapshot(t, "late", 31)
	if v.installPublishedSnapshot(late, "slot-c") {
		t.Fatal("a closed runtime must reject a late published generation")
	}
	late.retire()
	if late.coreEngine.HasMmapSegment() {
		t.Fatal("rejected late generation must remain independently releasable")
	}
}

func TestPublishedSnapshotReplacementWaitsForReaders(t *testing.T) {
	v := &Vault{slotCacheMu: &sync.RWMutex{}, snapRef: newVaultSnapshotRef()}
	first := openPublishedLifecycleSnapshot(t, "leased", 41)
	second := openPublishedLifecycleSnapshot(t, "next", 51)
	if !v.installPublishedSnapshot(first, "slot-a") {
		t.Fatal("install leased published snapshot")
	}

	const readerCount = 16
	var acquired sync.WaitGroup
	var readers sync.WaitGroup
	acquired.Add(readerCount)
	readers.Add(readerCount)
	releaseReaders := make(chan struct{})
	errCh := make(chan error, readerCount)
	for range readerCount {
		go func() {
			defer readers.Done()
			release, ok := first.acquireReader()
			acquired.Done()
			if !ok {
				errCh <- fmt.Errorf("reader could not lease active generation")
				return
			}
			<-releaseReaders
			if !searchPublishedLifecycleSnapshot(first, 41) {
				errCh <- fmt.Errorf("leased generation became unreadable before release")
			}
			release()
		}()
	}
	acquired.Wait()

	if !v.installPublishedSnapshot(second, "slot-b") {
		t.Fatal("install replacement published snapshot")
	}
	if !first.coreEngine.HasMmapSegment() {
		t.Fatal("replacement must not unmap a generation with active readers")
	}
	close(releaseReaders)
	readers.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
	if first.coreEngine.HasMmapSegment() {
		t.Fatal("final reader release must unmap the retired generation")
	}
	if !second.coreEngine.HasMmapSegment() {
		t.Fatal("active replacement generation must remain mapped")
	}
	v.CloseRuntimeSnapshots()
}
