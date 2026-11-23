// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package mcp

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/rocareer/rolling-memory/internal/memory/localvault"
	_ "modernc.org/sqlite"
)

// TestWarmPublishedGenerationsFollowsPublications proves the daemon warmer
// opens a newly published generation without any query arriving first.
func TestWarmPublishedGenerationsFollowsPublications(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "warm-loop.sqlite")
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	vault, err := localvault.NewVault(db, []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(vault.CloseRuntimeSnapshots)
	for i := 0; i < 32; i++ {
		text := fmt.Sprintf("warmer unit %d records the alpha beta ledger and unique%03d.", i, i)
		if saveErr := vault.SaveLocalPlaintext(fmt.Sprintf("doc-%03d", i), 1700000000+int64(i)*3600, text, nil, localvault.IngestResponse{Layer: "episodic"}); saveErr != nil {
			t.Fatal(saveErr)
		}
	}
	if _, rebuildErr := vault.RebuildCorpusSnapshot(context.Background()); rebuildErr != nil {
		t.Fatal(rebuildErr)
	}
	vault.SetBuildAllowed(false)
	if vault.PublishedGenerationResident() {
		t.Fatal("test setup must start from an unopened generation")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go warmPublishedGenerations(ctx, vault, 10*time.Millisecond)

	deadline := time.Now().Add(5 * time.Second)
	for !vault.PublishedGenerationResident() {
		if time.Now().After(deadline) {
			t.Fatal("the warmer did not open the published generation")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
