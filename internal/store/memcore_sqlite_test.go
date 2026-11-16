// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package store

import (
	"path/filepath"
	"testing"

	"github.com/rocareer/rolling-memory/internal/config"
	"github.com/rocareer/rolling-memory/internal/memcore"
)

func TestSQLiteMemcoreBackendCompliance(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "memory.db")
	cfgPath := filepath.Join(dir, "config.toml")
	mode := "local"
	scope := "mem:test:compliance"
	reader := "deliverable"
	cfg, err := config.Load(config.LoadOptions{
		Path:   &cfgPath,
		DB:     &dbPath,
		Mode:   &mode,
		Scope:  &scope,
		Reader: &reader,
		Env:    map[string]string{},
	})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	cfg.Log = filepath.Join(dir, "rolling-memory.log")
	st, err := Open(cfg)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	memcore.RunStoreBackendComplianceSuite(t, st.MemcoreBackend())

	// Verify PartTextsOfBlockParents links block trace parts to unit trace hash via parent_locator (e2aef8546)
	occ1, occ2, occ3 := int64(1700000000), int64(1700000001), int64(1700000002)
	unitHash, _, err := st.AddTrace(scope, "unit question", TraceOptions{
		Session:    "sess_bp",
		Turn:       1,
		Kind:       "message",
		Author:     "user",
		Audience:   "shared",
		OccurredAt: &occ1,
		Meta:       map[string]any{"kind": "unit"},
	})
	if err != nil {
		t.Fatalf("AddTrace unit: %v", err)
	}
	_, _, err = st.AddTrace(scope, "def _check_required_columns(self): pass", TraceOptions{
		Session:    "sess_bp",
		Turn:       2,
		Kind:       "message",
		Author:     "assistant",
		Audience:   "shared",
		OccurredAt: &occ2,
		Meta: map[string]any{
			"kind":           "block",
			"parent_locator": "sess_bp#1",
			"part_kind":      "file",
			"parser":         "virtual",
		},
	})
	if err != nil {
		t.Fatalf("AddTrace block: %v", err)
	}
	_, _, err = st.AddTrace(scope, "orphan thinking block", TraceOptions{
		Session:    "sess_bp",
		Turn:       3,
		Kind:       "message",
		Author:     "assistant",
		Audience:   "shared",
		OccurredAt: &occ3,
		Meta:       map[string]any{"kind": "block"},
	})
	if err != nil {
		t.Fatalf("AddTrace orphan block: %v", err)
	}
	gotParts, err := st.PartTextsOfBlockParents(scope, []string{unitHash})
	if err != nil {
		t.Fatalf("PartTextsOfBlockParents: %v", err)
	}
	if len(gotParts[unitHash]) != 1 || gotParts[unitHash][0].Text != "def _check_required_columns(self): pass" {
		t.Fatalf("expected 1 block part mapped to unitHash, got %+v", gotParts[unitHash])
	}
}
