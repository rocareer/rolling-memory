// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package store

import (
	"testing"
)

func TestHierarchicalLCPScoreAndDualTrackLocus(t *testing.T) {
	sameDir := HierarchicalLCPScore("rocareer/rolling-memory/internal/store", "rocareer/rolling-memory/internal/store")
	sameRepo := HierarchicalLCPScore("rocareer/rolling-memory/internal/store", "rocareer/rolling-memory/internal/hooks")
	sameOrg := HierarchicalLCPScore("rocareer/rolling-memory/internal/store", "rocareer/other-project/internal/hooks")
	disjoint := HierarchicalLCPScore("rocareer/rolling-memory/internal/store", "中国/浙江/杭州/西湖")
	if sameDir != 1.0 {
		t.Fatalf("expected sameDir=1.0, got %.4f", sameDir)
	}
	if !(sameDir > sameRepo && sameRepo > sameOrg && sameOrg > disjoint && disjoint == 0.0) {
		t.Fatalf("monotonicity violated: sameDir=%.3f sameRepo=%.3f sameOrg=%.3f disjoint=%.3f",
			sameDir, sameRepo, sameOrg, disjoint)
	}

	st := newTestStore(t, "mem:self")

	leafID, nodeIDs, err := st.EnsureDualTrackLocus("mem:self", "rocareer/rolling-memory/internal/hooks", "shared")
	if err != nil {
		t.Fatalf("EnsureDualTrackLocus: %v", err)
	}
	if leafID == "" || len(nodeIDs) != 4 {
		t.Fatalf("expected 4 spatial nodes and non-empty leafID, got leafID=%q nodeIDs=%v", leafID, nodeIDs)
	}
	// Short alias "hooks" should resolve directly to the leaf spatial node.
	foundNode, ok, err := st.NodeByLabel("mem:self", "hooks", "referent")
	if err != nil || !ok || foundNode.ID != leafID {
		t.Fatalf("expected short alias 'hooks' to resolve to %q, got ok=%v node=%+v err=%v", leafID, ok, foundNode, err)
	}

	incLeaf, err := st.EnsureIncarnationHierarchy("mem:self", "coding/zcode/main", "shared")
	if err != nil || incLeaf == "" {
		t.Fatalf("EnsureIncarnationHierarchy: %v, leaf=%q", err, incLeaf)
	}
	incRoot, ok, err := st.NodeByLabel("mem:self", "coding", "incarnation")
	if err != nil || !ok || incRoot.ID == "" {
		t.Fatalf("expected root incarnation node 'coding' to exist, ok=%v err=%v", ok, err)
	}
	closure, err := st.Closure("mem:self", []string{incRoot.ID}, []string{"subtype-of"}, 16)
	if err != nil || len(closure) < 3 {
		t.Fatalf("expected subtype-of closure from 'coding' to include 3 hierarchy nodes, got %v (err=%v)", closure, err)
	}

	// Verify SearchSeeds substring leg excludes pure 2-digit number tokens
	_, err = st.EnsureNode("mem:self", "referent", "Room 1828 Server", NodeOptions{Audience: "shared", Status: "active"})
	if err != nil {
		t.Fatalf("EnsureNode Room 1828 Server: %v", err)
	}
	_, legs, err := st.SearchSeeds("18", SeedOptions{Scope: "mem:self", Reader: "owner", Limit: 16})
	if err != nil {
		t.Fatalf("SearchSeeds('18'): %v", err)
	}
	if legs["substring"] != 0 {
		t.Fatalf("expected pure 2-digit token '18' to be excluded from unanchored substring scan, got legs=%v", legs)
	}
}

func TestSplitInlineCategoryNamesAndMultiValueBinding(t *testing.T) {
	cats := SplitInlineCategoryNames(`["TCP/IP", "network / protocol", "电影、斯皮尔伯格作品", "user"]`)
	if len(cats) != 5 || cats[0] != "TCP/IP" || cats[1] != "network" || cats[2] != "protocol" || cats[3] != "电影" || cats[4] != "斯皮尔伯格作品" {
		t.Fatalf("unexpected SplitInlineCategoryNames: %#v", cats)
	}

	st := newTestStore(t, "mem:self")
	entID, err := st.EnsureNode("mem:self", "referent", "Jurassic Park", NodeOptions{Audience: "shared", Status: "active"})
	if err != nil || entID == "" {
		t.Fatalf("EnsureNode Jurassic Park: %v", err)
	}
	// Self-reflexive category ("Jurassic Park") must be filtered out; "movie" and "Sci-Fi" must be materialized as subtype-of edges.
	if err := st.BindInlineConceptCategory("mem:self", entID, "Jurassic Park", []string{"Jurassic Park", "movie", "Sci-Fi"}, "shared"); err != nil {
		t.Fatalf("BindInlineConceptCategory: %v", err)
	}
	closure, err := st.Closure("mem:self", []string{entID}, []string{"subtype-of"}, 16)
	if err != nil || len(closure) != 3 {
		t.Fatalf("expected 3 nodes in subtype-of closure (entity + 2 categories), got %v err=%v", closure, err)
	}
}

func TestMergeAndReconcileCrossNodeAliasConflictGuard(t *testing.T) {
	st := newTestStore(t, "mem:self")
	aliceID, _ := st.EnsureNode("mem:self", "referent", "Alice", NodeOptions{Audience: "shared", Status: "active"})
	bobID, _ := st.EnsureNode("mem:self", "referent", "Bob", NodeOptions{Audience: "shared", Status: "active"})
	bobbyID, _ := st.EnsureNode("mem:self", "referent", "Bobby", NodeOptions{Audience: "shared", Status: "active"})

	// Bind "shared_nick" to Alice
	_, _ = st.AddAlias(aliceID, "shared_nick", "manual")
	// Bind "shared_nick" and "unique_bobby" to Bobby
	_, _ = st.AddAlias(bobbyID, "shared_nick", "manual")
	_, _ = st.AddAlias(bobbyID, "unique_bobby", "manual")

	// Merge Bobby into Bob: "unique_bobby" must migrate to Bob, while "shared_nick" (already bound to live Alice) must NOT migrate to Bob
	if _, err := st.MergeNodes(bobID, bobbyID, "user"); err != nil {
		t.Fatalf("MergeNodes: %v", err)
	}
	bobAliases, err := st.Aliases(bobID)
	if err != nil {
		t.Fatalf("Aliases(bob): %v", err)
	}
	hasUnique := false
	for _, al := range bobAliases {
		if al == "shared_nick" {
			t.Fatalf("cross-node conflicting alias 'shared_nick' (already bound to live Alice) must not be copied to Bob: %v", bobAliases)
		}
		if al == "unique_bobby" {
			hasUnique = true
		}
	}
	if !hasUnique {
		t.Fatalf("expected non-conflicting alias 'unique_bobby' to migrate to Bob, got %v", bobAliases)
	}
}

func TestEightKindClosedSetMetaEdgesAndPartOfClosure(t *testing.T) {
	st := newTestStore(t, "mem:self")
	aID, _ := st.EnsureNode("mem:self", "referent", "EngineBlock", NodeOptions{Audience: "shared", Status: "active"})
	bID, _ := st.EnsureNode("mem:self", "referent", "Vehicle", NodeOptions{Audience: "shared", Status: "active"})
	cID, _ := st.EnsureNode("mem:self", "assertion", "a_cause_1", NodeOptions{Audience: "shared", Status: "active"})
	dID, _ := st.EnsureNode("mem:self", "assertion", "a_effect_2", NodeOptions{Audience: "shared", Status: "active"})

	if err := st.EnsureExplicitMetaEdge("mem:self", "part_of", aID, bID, "meronymy"); err != nil {
		t.Fatalf("EnsureExplicitMetaEdge(part_of): %v", err)
	}
	if err := st.EnsureExplicitMetaEdge("mem:self", "causes", cID, dID, "causal"); err != nil {
		t.Fatalf("EnsureExplicitMetaEdge(causes): %v", err)
	}
	if err := st.EnsureExplicitMetaEdge("mem:self", "derived_from", dID, cID, "epistemic"); err != nil {
		t.Fatalf("EnsureExplicitMetaEdge(derived_from): %v", err)
	}
	// Adversarial: unknown meta_kind or self-loop must be rejected silently without writing invalid rows
	_ = st.EnsureExplicitMetaEdge("mem:self", "invented_kind", aID, bID, "bad")
	_ = st.EnsureExplicitMetaEdge("mem:self", "part_of", aID, aID, "self-loop")

	closure, err := st.Closure("mem:self", []string{aID}, []string{"part-of"}, 16)
	if err != nil || len(closure) != 2 {
		t.Fatalf("expected part-of closure to contain [EngineBlock, Vehicle], got %v err=%v", closure, err)
	}
}

func TestReconcileAliasDuplicateNodesNullEndpointsAndSelfLoopGuard(t *testing.T) {
	st := newTestStore(t, "mem:self")
	keepID, _ := st.EnsureNode("mem:self", "referent", "CanonicalAlice", NodeOptions{Audience: "shared", Status: "active"})
	dupID, _ := st.EnsureNode("mem:self", "referent", "DuplicateAlice", NodeOptions{Audience: "shared", Status: "active"})
	otherHolderID, _ := st.EnsureNode("mem:self", "referent", "HolderBob", NodeOptions{Audience: "shared", Status: "active"})
	predID, _ := st.EnsureNode("mem:self", "relation_type", "likes", NodeOptions{Audience: "shared", Status: "active"})

	if _, err := st.conn.Exec(`
		INSERT INTO trace(hash, scope, session, turn, kind, author, audience, text, occurred_at, recorded_at)
		VALUES ('tr_1', 'mem:self', 's1', 1, 'message', 'user', 'shared', 'Alice likes tea', 1, 1)`); err != nil {
		t.Fatalf("insert trace: %v", err)
	}
	aLitNode, _ := st.EnsureNode("mem:self", "assertion", "a_lit_1", NodeOptions{Audience: "shared", Status: "active"})
	aTernaryNode, _ := st.EnsureNode("mem:self", "assertion", "a_ternary_1", NodeOptions{Audience: "shared", Status: "active"})

	// 1. Literal-object assertion (object_id IS NULL, holder_id IS NULL): subject_id must migrate from dupID to keepID.
	if _, err := st.conn.Exec(`
		INSERT INTO assertion(node_id, hash, scope, subject_id, rel_id, object_id, object_literal, holder_id,
		  t_record, t_source, temporal_class, warrant, status, anchor_trace_hash, anchor_start, anchor_end, source_trace_hash, audience, created_at)
		VALUES (?, 'h_lit_1', 'mem:self', ?, ?, NULL, 'green tea', NULL,
		  1, 1, 'unknown', 'referential', 'active', 'tr_1', 0, 5, 'tr_1', 'shared', 1)`,
		aLitNode, dupID, predID,
	); err != nil {
		t.Fatalf("insert literal assertion: %v", err)
	}
	// 2. Ternary assertion where subject_id = dupID, object_id = keepID, holder_id = otherHolderID:
	// migrating subject_id to keepID would create a subject_id == object_id self-loop, so the guard must block it.
	if _, err := st.conn.Exec(`
		INSERT INTO assertion(node_id, hash, scope, subject_id, rel_id, object_id, object_literal, holder_id,
		  t_record, t_source, temporal_class, warrant, status, anchor_trace_hash, anchor_start, anchor_end, source_trace_hash, audience, created_at)
		VALUES (?, 'h_ter_1', 'mem:self', ?, ?, ?, NULL, ?,
		  1, 1, 'unknown', 'referential', 'active', 'tr_1', 0, 5, 'tr_1', 'shared', 1)`,
		aTernaryNode, dupID, predID, keepID, otherHolderID,
	); err != nil {
		t.Fatalf("insert ternary assertion: %v", err)
	}

	merged, err := st.ReconcileAliasDuplicateNodes("mem:self", keepID, "DuplicateAlice", "test")
	if err != nil || merged != 1 {
		t.Fatalf("ReconcileAliasDuplicateNodes: merged=%d err=%v", merged, err)
	}

	var litSubj, terSubj string
	if err := st.conn.QueryRow(`SELECT subject_id FROM assertion WHERE node_id = ?`, aLitNode).Scan(&litSubj); err != nil {
		t.Fatalf("query literal assertion: %v", err)
	}
	if litSubj != keepID {
		t.Fatalf("literal-object assertion subject_id was not migrated: got %q, want %q", litSubj, keepID)
	}
	if err := st.conn.QueryRow(`SELECT subject_id FROM assertion WHERE node_id = ?`, aTernaryNode).Scan(&terSubj); err != nil {
		t.Fatalf("query ternary assertion: %v", err)
	}
	if terSubj == keepID {
		t.Fatalf("self-loop guard failed: ternary assertion collapsed into subject_id == object_id (%q)", terSubj)
	}

	// BindAssertionParticipants must persist rows into the canonical `participant` table.
	if err := st.BindAssertionParticipants("mem:self", aLitNode, "shared", []string{"Carol", "Dave"}); err != nil {
		t.Fatalf("BindAssertionParticipants: %v", err)
	}
	var partCount int
	if err := st.conn.QueryRow(`SELECT COUNT(*) FROM participant WHERE assertion_node_id = ?`, aLitNode).Scan(&partCount); err != nil {
		t.Fatalf("count participant rows: %v", err)
	}
	if partCount != 2 {
		t.Fatalf("expected 2 participant rows, got %d", partCount)
	}
}
