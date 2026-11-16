// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package store

import (
	"testing"
)

func TestSubtypeOfClosureNominatesNeighbourRelations(t *testing.T) {
	store := newTestStore(t, testScope)
	addFact(t, store, testScope, "老张养了一只猫。", "老张", "养", "猫", "老张养了一只猫", factOptions{})
	relation, ok, _ := store.NodeByLabel(testScope, "养", "relation_type")
	if !ok {
		t.Fatal("missing relation 养")
	}
	broader, err := store.EnsureNode(testScope, "relation_type", "拥有", NodeOptions{})
	if err != nil {
		t.Fatalf("ensure broader: %v", err)
	}
	before, err := store.Closure(testScope, []string{broader}, []string{"subtype-of"}, 128)
	if err != nil || len(before) != 1 {
		t.Fatalf("closure before = %v err=%v", before, err)
	}
	if _, err := store.AddMetaEdge(testScope, "subtype-of", relation.ID, broader, "", "user"); err != nil {
		t.Fatalf("add subtype: %v", err)
	}
	after, err := store.Closure(testScope, []string{broader}, []string{"subtype-of"}, 128)
	if err != nil {
		t.Fatalf("closure after: %v", err)
	}
	if len(after) != 2 || after[0] != broader || after[1] != relation.ID {
		t.Fatalf("closure after = %v", after)
	}
}

func TestInverseOfClosureFindsTheInverseReading(t *testing.T) {
	store := newTestStore(t, testScope)
	addFact(t, store, testScope, "老张照顾了猫。", "老张", "照顾", "猫", "老张照顾了猫", factOptions{})
	relation, _, _ := store.NodeByLabel(testScope, "照顾", "relation_type")
	inverse, err := store.EnsureNode(testScope, "relation_type", "看护", NodeOptions{})
	if err != nil {
		t.Fatalf("ensure inverse: %v", err)
	}
	if _, err := store.AddMetaEdge(testScope, "inverse-of", inverse, relation.ID, "", "user"); err != nil {
		t.Fatalf("add inverse: %v", err)
	}
	closure, err := store.Closure(testScope, []string{inverse}, []string{"inverse-of"}, 128)
	if err != nil {
		t.Fatalf("closure: %v", err)
	}
	if len(closure) != 2 || closure[1] != relation.ID {
		t.Fatalf("closure = %v", closure)
	}
}

func TestClosureHonoursKindsAndLimit(t *testing.T) {
	store := newTestStore(t, testScope)
	first, _ := store.EnsureNode(testScope, "relation_type", "甲", NodeOptions{})
	second, _ := store.EnsureNode(testScope, "relation_type", "乙", NodeOptions{})
	third, _ := store.EnsureNode(testScope, "relation_type", "丙", NodeOptions{})
	for _, pair := range [][2]string{{first, second}, {second, third}} {
		if _, err := store.AddMetaEdge(testScope, "subtype-of", pair[0], pair[1], "", "user"); err != nil {
			t.Fatalf("add edge: %v", err)
		}
	}
	limited, err := store.Closure(testScope, []string{first}, []string{"subtype-of"}, 2)
	if err != nil || len(limited) != 2 {
		t.Fatalf("limited closure = %v err=%v", limited, err)
	}
	none, err := store.Closure(testScope, []string{first}, []string{"same-as"}, 128)
	if err != nil || len(none) != 1 || none[0] != first {
		t.Fatalf("same-as closure = %v err=%v", none, err)
	}
	empty, err := store.Closure(testScope, []string{first}, []string{"subtype-of"}, -1)
	if err != nil || len(empty) != 0 {
		t.Fatalf("zero-limit closure = %v err=%v", empty, err)
	}
}

func TestCanonicalRepresentativePrefersActive(t *testing.T) {
	store := newTestStore(t, testScope)
	keep, _ := store.EnsureNode(testScope, "referent", "老张", NodeOptions{})
	drop, _ := store.EnsureNode(testScope, "referent", "张先生", NodeOptions{})
	if _, err := store.PromoteNode(drop, "user"); err != nil {
		t.Fatalf("promote: %v", err)
	}
	if _, err := store.conn.Exec(
		`INSERT INTO meta_edge(scope, meta_kind, lhs_id, rhs_id, status, evidence_anchor, created_at, created_by)
		 VALUES (?, 'same-as', ?, ?, 'active', '', ?, 'user')`,
		testScope, keep, drop, testNow,
	); err != nil {
		t.Fatalf("insert same-as: %v", err)
	}
	canonical, err := store.CanonicalMap(testScope, []string{keep, drop})
	if err != nil {
		t.Fatalf("canonical: %v", err)
	}
	if canonical[keep] != drop || canonical[drop] != drop {
		t.Fatalf("canonical = %v, want active %s", canonical, drop)
	}
}

func TestCheckDetectsSameAsCycle(t *testing.T) {
	store := newTestStore(t, testScope)
	node, _ := store.EnsureNode(testScope, "referent", "老张", NodeOptions{})
	if _, err := store.conn.Exec(
		`INSERT INTO meta_edge(scope, meta_kind, lhs_id, rhs_id, status, evidence_anchor, created_at, created_by)
		 VALUES (?, 'same-as', ?, ?, 'active', '', ?, 'user')`,
		testScope, node, node, testNow,
	); err != nil {
		t.Fatalf("insert self edge: %v", err)
	}
	report, err := store.Check()
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if report["ok"] != false {
		t.Fatalf("check ok = %v", report["ok"])
	}
	found := false
	for _, issue := range report["issues"].([]map[string]any) {
		if mapString(issue, "code") == "same_as_cycle" {
			found = true
		}
	}
	if !found {
		t.Fatalf("issues = %v", report["issues"])
	}
}

// 单条 same-as 边是等价合并（合法）；只有两端已在同一等价类（真环）才判失败。
// 引擎 check ⑦ 用并查集同判据：无向回边判定会把任意单边误报成环（g9 语料 4 条单边曾被
// 本地误报 4 处 same_as_cycle）。
func TestCheckSameAsSingleEdgeIsNotACycle(t *testing.T) {
	store := newTestStore(t, testScope)
	alice, _ := store.EnsureNode(testScope, "referent", "Alice", NodeOptions{})
	ally, _ := store.EnsureNode(testScope, "referent", "Ally", NodeOptions{})
	bob, _ := store.EnsureNode(testScope, "referent", "Bob", NodeOptions{})
	insert := func(lhs, rhs string) {
		if _, err := store.conn.Exec(
			`INSERT INTO meta_edge(scope, meta_kind, lhs_id, rhs_id, status, evidence_anchor, created_at, created_by)
			 VALUES (?, 'same-as', ?, ?, 'active', '', ?, 'user')`,
			testScope, lhs, rhs, testNow,
		); err != nil {
			t.Fatalf("insert same-as %s->%s: %v", lhs, rhs, err)
		}
	}
	insert(alice, ally)
	report, err := store.Check()
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	for _, issue := range report["issues"].([]map[string]any) {
		if mapString(issue, "code") == "same_as_cycle" {
			t.Fatalf("single same-as edge falsely reported as cycle: %v", issue)
		}
	}
	// 三角形（Alice~Ally 已同类，再加 Ally~Bob、Bob~Alice 后 Alice~Bob 成环）判失败。
	insert(ally, bob)
	insert(bob, alice)
	report, err = store.Check()
	if err != nil {
		t.Fatalf("check after triangle: %v", err)
	}
	if report["ok"] != false {
		t.Fatalf("triangle check ok = %v", report["ok"])
	}
	found := false
	for _, issue := range report["issues"].([]map[string]any) {
		if mapString(issue, "code") == "same_as_cycle" {
			found = true
		}
	}
	if !found {
		t.Fatalf("triangle issues = %v", report["issues"])
	}
}

func TestResolveSingleHopReadOnly(t *testing.T) {
	s := newTestStore(t, testScope)
	addFact(t, s, testScope, "Alice (aka Ally) works at Acme and visited Berlin.", "Alice", "works at", "Acme", "Alice (aka Ally) works at Acme", factOptions{})
	alice, ok, err := s.NodeByLabel(testScope, "Alice", "referent")
	if err != nil || !ok {
		t.Fatalf("NodeByLabel Alice: ok=%v err=%v", ok, err)
	}
	if _, err := s.AddAlias(alice.ID, "Ally", "user"); err != nil {
		t.Fatalf("AddAlias: %v", err)
	}

	aliasRes, err := s.Resolve(testScope, "alias", "Ally", 10)
	if err != nil || aliasRes["count"] != 1 {
		t.Fatalf("Resolve(alias) = %#v, err=%v", aliasRes, err)
	}
	rosterRes, err := s.Resolve(testScope, "roster", "Alice", 10)
	if err != nil || rosterRes["count"] == 0 {
		t.Fatalf("Resolve(roster) = %#v, err=%v", rosterRes, err)
	}
	nbRes, err := s.Resolve(testScope, "neighbor", "Alice", 10)
	if err != nil || nbRes["count"] != 1 {
		t.Fatalf("Resolve(neighbor) = %#v, err=%v", nbRes, err)
	}
	histRes, err := s.Resolve(testScope, "history", "Alice", 10)
	if err != nil || histRes["count"] != 1 {
		t.Fatalf("Resolve(history) = %#v, err=%v", histRes, err)
	}
	if _, err := s.Resolve(testScope, "invalid_op", "Alice", 10); err == nil {
		t.Fatalf("expected error on invalid resolve op")
	}
}

func TestSearchEngineModeGating(t *testing.T) {
	s := newTestStore(t, testScope)
	s.Config.Recall.Vector = false
	if _, _, err := s.SearchSeeds("Alice", SeedOptions{Scope: testScope, SearchEngine: "vector"}); err == nil {
		t.Fatalf("expected loud error when search_engine=vector and recall.vector=false")
	}
	if _, _, err := s.SearchSeeds("Alice", SeedOptions{Scope: testScope, SearchEngine: "invalid"}); err == nil {
		t.Fatalf("expected error on invalid search_engine value")
	}
	if _, _, err := s.SearchSeeds("Alice", SeedOptions{Scope: testScope, SearchEngine: "fts"}); err != nil {
		t.Fatalf("unexpected error on search_engine=fts: %v", err)
	}
}

func TestSameTurnMultiValueConflictExemptionAndAuditQuality(t *testing.T) {
	liveScope := "mem:live:u19"
	s := newTestStore(t, liveScope)
	text := "Bob speaks French and Spanish."
	res1 := addFact(t, s, liveScope, text, "Bob", "speaks", "French", "Bob speaks French", factOptions{})
	res2 := addFact(t, s, liveScope, text, "Bob", "speaks", "Spanish", "Spanish", factOptions{})
	if mapInt64(res2, "conflicts") != 0 {
		t.Fatalf("expected 0 conflicts for same-turn parallel assertions, got %v", res2["conflicts"])
	}

	// Cross-turn distinct object on a functional predicate still records a conflict
	_ = addFact(t, s, liveScope, "Bob lives in Rome.", "Bob", "lives in", "Rome", "Bob lives in Rome", factOptions{})
	resCross := addFact(t, s, liveScope, "Bob lives in Paris.", "Bob", "lives in", "Paris", "Bob lives in Paris", factOptions{})
	if mapInt64(resCross, "conflicts") == 0 {
		t.Fatalf("expected cross-turn conflict on functional predicate, got %v", resCross)
	}

	// Update loop_state via governed path to verify AuditArtifactQuality loop_states tally
	_, _ = s.conn.Exec(`UPDATE assertion SET loop_state = 'open' WHERE node_id = ?`, mapString(res1, "id"))
	_, _ = s.conn.Exec(`UPDATE assertion SET loop_state = 'fulfilled' WHERE node_id = ?`, mapString(res2, "id"))

	audit, err := s.AuditArtifactQuality(liveScope)
	if err != nil || audit["ok"] != true {
		t.Fatalf("AuditArtifactQuality: %#v, err=%v", audit, err)
	}
	ls, _ := audit["loop_states"].(map[string]int)
	if ls["open"] != 1 || ls["fulfilled"] != 1 {
		t.Fatalf("expected loop_states open=1 fulfilled=1, got %#v", ls)
	}
}
