// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package localvault

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// TestWarmPublishedGenerationOpensAheadOfQuery pins the background-warm
// contract: a freshly published generation is opened by the warmer (not by a
// query), and the warmer reports no work once that generation is resident.
func TestWarmPublishedGenerationOpensAheadOfQuery(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", vaultTestDSN(filepath.Join(t.TempDir(), "warm.db")))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	v, err := NewVault(db, []byte("warm-generation-key"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(v.CloseRuntimeSnapshots)
	for i := 0; i < 48; i++ {
		text := fmt.Sprintf("warm unit %d records the alpha beta ledger and unique%03d.", i, i)
		if err := v.SaveLocalPlaintext(fmt.Sprintf("doc-%03d", i), 1700000000+int64(i)*3600, text, nil, IngestResponse{Layer: "episodic"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := v.RebuildCorpusSnapshot(ctx); err != nil {
		t.Fatal(err)
	}
	v.SetBuildAllowed(false)

	firstSlot, ok := v.PublishedSlot()
	if !ok || firstSlot == "" {
		t.Fatal("a published generation must report its slot")
	}
	slot, changed, err := v.WarmPublishedGeneration(ctx)
	if err != nil {
		t.Fatalf("warm: %v", err)
	}
	if !changed || slot != firstSlot {
		t.Fatalf("first warm = (slot=%q changed=%v), want (%q true)", slot, changed, firstSlot)
	}
	v.slotCacheMu.RLock()
	resident := v.snapRef.publishedSnap
	v.slotCacheMu.RUnlock()
	if resident == nil || resident.coreEngine == nil || !resident.coreEngine.HasMmapSegment() {
		t.Fatal("warming must leave the generation mapped before any query")
	}
	if _, changed, err = v.WarmPublishedGeneration(ctx); err != nil || changed {
		t.Fatalf("second warm of the same generation must be a no-op (changed=%v err=%v)", changed, err)
	}

	// A newer publication moves CURRENT: the warmer follows it and the previous
	// generation retires instead of accumulating.
	if err := v.SaveLocalPlaintext("doc-048", 1700002000, "fresh warm unit forty eight records the alpha ledger and unique048.", nil, IngestResponse{Layer: "episodic"}); err != nil {
		t.Fatal(err)
	}
	if _, err := v.RebuildCorpusSnapshot(ctx); err != nil {
		t.Fatal(err)
	}
	nextSlot, ok := v.PublishedSlot()
	if !ok || nextSlot == firstSlot {
		t.Fatalf("publishing must advance CURRENT (was %q, now %q)", firstSlot, nextSlot)
	}
	if slot, changed, err = v.WarmPublishedGeneration(ctx); err != nil || !changed || slot != nextSlot {
		t.Fatalf("warm after publication = (slot=%q changed=%v err=%v), want (%q true)", slot, changed, err, nextSlot)
	}
	if resident.coreEngine != nil && resident.coreEngine.HasMmapSegment() {
		t.Fatal("warming the next generation must retire the previous mmap")
	}
}
