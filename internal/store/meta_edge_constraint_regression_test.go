// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package store

import (
	"strings"
	"testing"
)

func TestMetaEdgeUnsupportedKindsReturnConstraintError(t *testing.T) {
	st := newTestStore(t, "mem:self")
	left, err := st.EnsureNode("mem:self", "referent", "left", NodeOptions{Audience: "shared", Status: "active"})
	if err != nil {
		t.Fatal(err)
	}
	right, err := st.EnsureNode("mem:self", "referent", "right", NodeOptions{Audience: "shared", Status: "active"})
	if err != nil {
		t.Fatal(err)
	}
	// Simulate an unmigrated custom/legacy meta_edge table whose CHECK constraint
	// only permits the original 6 kinds (guarded by an existing meta_edge_v2 sentinel).
	if _, err := st.DB().Exec(`DROP TABLE meta_edge; CREATE TABLE meta_edge (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		scope TEXT NOT NULL,
		meta_kind TEXT NOT NULL CHECK (meta_kind IN ('same-as', 'conflicts-with', 'subtype-of', 'inverse-of', 'supersedes', 'supplement')),
		lhs_id TEXT NOT NULL,
		rhs_id TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'proposed', 'reverted')),
		evidence_anchor TEXT,
		created_at INTEGER NOT NULL,
		created_by TEXT NOT NULL DEFAULT 'system',
		revert_of INTEGER,
		UNIQUE (scope, meta_kind, lhs_id, rhs_id, status)
	); CREATE TABLE meta_edge_v2 (sentinel TEXT)`); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"part_of", "causes", "derived_from"} {
		err := st.EnsureExplicitMetaEdge("mem:self", kind, left, right, "witness")
		if err == nil || !strings.Contains(err.Error(), "CHECK constraint failed") {
			t.Fatalf("%s: got %v, want CHECK constraint error", kind, err)
		}
	}
	for i := 0; i < 2; i++ {
		if err := st.EnsureExplicitMetaEdge("mem:self", "subtype_of", left, right, "witness"); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := st.DB().QueryRow(`SELECT count(*) FROM meta_edge`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("edge count = %d, err = %v", count, err)
	}
}

func TestImportUnsupportedMetaEdgeRollsBack(t *testing.T) {
	st := newTestStore(t, "mem:self")
	left, err := st.EnsureNode("mem:self", "referent", "left", NodeOptions{Audience: "shared", Status: "active"})
	if err != nil {
		t.Fatal(err)
	}
	right, err := st.EnsureNode("mem:self", "referent", "right", NodeOptions{Audience: "shared", Status: "active"})
	if err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{
		"traces":     []map[string]any{{"hash": "synthetic", "scope": "mem:self", "session": "import", "turn": 1, "kind": "message", "audience": "shared", "text": "witness", "occurred_at": 1, "recorded_at": 1}},
		"meta_edges": []map[string]any{{"scope": "mem:self", "meta_kind": "unsupported-kind", "lhs_id": left, "rhs_id": right, "status": "active", "created_at": 1, "created_by": "system"}},
	}
	_, err = st.ImportScope("mem:self", payload)
	if err == nil || !strings.Contains(err.Error(), "CHECK constraint failed") {
		t.Fatalf("import error = %v, want CHECK constraint error", err)
	}
	var count int
	if err := st.DB().QueryRow(`SELECT count(*) FROM trace WHERE hash = 'synthetic'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rolled-back trace count = %d, err = %v", count, err)
	}
}
