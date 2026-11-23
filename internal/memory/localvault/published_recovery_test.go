// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package localvault

import (
	"context"
	"encoding/binary"
	"hash/crc32"
	"os"
	"testing"
)

func demotePublishedZCSRToPreviousVersion(t *testing.T, path string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) < 184 {
		t.Fatalf("short zcsr: %d bytes", len(raw))
	}
	// Previous release used segment version 7. Rewrite the version and refresh
	// the header CRC so the reader reaches the version gate, not a checksum
	// failure.
	binary.LittleEndian.PutUint16(raw[4:6], 7)
	binary.LittleEndian.PutUint32(raw[176:180], crc32.Checksum(raw[0:176], crc32.MakeTable(crc32.Castagnoli)))
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestRejectedPublishedGenerationIsNotReady pins upgrade recovery: a segment
// this build cannot open must stop reporting the vault as snapshot-ready, so
// the background builder republishes instead of leaving recall in warming.
func TestRejectedPublishedGenerationIsNotReady(t *testing.T) {
	ctx := context.Background()
	v := openSnapshotEngineTestVault(t, "rejected-generation.sqlite")

	saveSnapshotEngineDocs(t, v, 0, 8)
	if _, err := v.RebuildCorpusSnapshot(ctx); err != nil {
		t.Fatalf("publish: %v", err)
	}
	v.SetBuildAllowed(false)

	csrPath, _, _, _, slot, ok := resolvePublishedSnapshotPaths(v.mainDBPath())
	if !ok || slot == "" {
		t.Fatal("no published slot resolved")
	}
	demotePublishedZCSRToPreviousVersion(t, csrPath)

	if snap, loaded := v.loadPublishedSnapshot(ctx); loaded || snap != nil {
		t.Fatal("a previous-version generation must not load")
	}
	if !v.snapshotGenerationRejected(slot) {
		t.Fatal("the unreadable generation was not recorded")
	}
	if v.PublishedSnapshotReady() {
		t.Fatal("an unreadable generation must not keep readiness true")
	}

	// A successful publication is a new slot and must clear the marker.
	if _, err := v.RebuildCorpusSnapshot(ctx); err != nil {
		t.Fatalf("republish after rejection: %v", err)
	}
	if !v.PublishedSnapshotReady() {
		t.Fatal("a fresh publication must restore readiness")
	}
	if snap, loaded := v.loadPublishedSnapshot(ctx); !loaded || snap == nil {
		t.Fatal("the fresh publication must load")
	}
}
