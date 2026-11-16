// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package api_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rocareer/rolling-memory/internal/api"
	"github.com/rocareer/rolling-memory/internal/config"
)

// TestPerfProbeLocalRecall is a manual profiling probe (skipped unless RM_PERF_DB is
// set) that measures the local-mode recall path and its two legs separately.
func TestPerfProbeLocalRecall(t *testing.T) {
	dbPath := os.Getenv("RM_PERF_DB")
	if dbPath == "" {
		t.Skip("RM_PERF_DB not set")
	}
	directory := t.TempDir()
	configPath := filepath.Join(directory, "config.toml")
	mode, scope, reader := "local", "mem:self", "deliverable"
	cfg, err := config.Load(config.LoadOptions{
		Path: &configPath, DB: &dbPath, Mode: &mode, Scope: &scope, Reader: &reader,
		Env: map[string]string{},
	})
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	cfg.Log = filepath.Join(directory, "rolling-memory.log")
	memory, err := api.New(cfg)
	if err != nil {
		t.Fatalf("api: %v", err)
	}
	t.Cleanup(func() { memory.Close() })
	client, err := memory.ThinClient()
	if err != nil {
		t.Fatalf("thinclient: %v", err)
	}
	const cue = "ATM 官方 Recall@10 口径 local_plain"
	for i := 0; i < 5; i++ {
		// Recall first: this is the cold, one-shot face a CLI invocation sees.
		start := time.Now()
		result, rErr := memory.Recall(api.RecallRequest{Cue: cue})
		fullDur := time.Since(start)

		start = time.Now()
		noetic, nErr := client.RecallNoetic(cue, 20)
		noeticDur := time.Since(start)

		start = time.Now()
		hits, sErr := client.Vault.SearchClientSDKDefault(cue, 20)
		searchDur := time.Since(start)

		bands, raw := 0, 0
		if result != nil {
			bands, raw = len(result.Bands), len(result.RawHits)
		}
		t.Logf("turn=%d recall bands=%d raw=%d err=%v %s | noetic=%d/%v %s | search=%d/%v %s",
			i, bands, raw, rErr, fullDur, len(noetic), nErr, noeticDur, len(hits), sErr, searchDur)
	}
}
