// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package store

import (
	"testing"
)

func TestHealthQualityProbes(t *testing.T) {
	store := newTestStore(t, testScope)
	addFact(t, store, testScope, "老张是农民。", "老张", "是", "农民", "老张是农民", factOptions{Holder: "我"})
	subject, err := store.EnsureNode(testScope, "referent", "老张", NodeOptions{})
	if err != nil {
		t.Fatalf("ensure subject: %v", err)
	}
	relation, err := store.EnsureNode(testScope, "relation_type", "说明", NodeOptions{})
	if err != nil {
		t.Fatalf("ensure relation: %v", err)
	}
	unclosed := "复制器材（dry-run 缺省；域集 = 全部"
	traceHash, _, err := store.AddTrace(testScope, unclosed+"）。", TraceOptions{Author: "user"})
	if err != nil {
		t.Fatalf("add trace: %v", err)
	}
	if _, err := store.AppendAssertion(AppendAssertionRequest{
		Scope:           testScope,
		RelID:           relation,
		SubjectID:       &subject,
		ObjectLiteral:   &unclosed,
		Warrant:         "experiential",
		AnchorTraceHash: traceHash,
		AnchorStart:     0,
		AnchorEnd:       len([]rune(unclosed)),
		Confidence:      0.35,
	}); err != nil {
		t.Fatalf("append unclosed: %v", err)
	}

	health, err := store.Health(testScope, 0)
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	quality, ok := health["quality"].(map[string]any)
	if !ok {
		t.Fatalf("health has no quality block: %v", health)
	}
	if quality["unclosed_literals"] != 1 || quality["copula_relations"] != 1 || quality["low_confidence"] != 1 {
		t.Fatalf("quality = %v", quality)
	}

	// Valid list numbering ("1) ... 2) ...") and half-open intervals ("(1024,4096]") must NOT be flagged as unclosed_literals
	validListAndInterval := "规则：1) 单轮 ∈(1024,4096] tokens 整轮不切；2) 区间 [0,1) 正常闭合。"
	th2, _, err := store.AddTrace(testScope, validListAndInterval, TraceOptions{Author: "user"})
	if err != nil {
		t.Fatalf("add trace 2: %v", err)
	}
	rel2, err := store.EnsureNode(testScope, "relation_type", "核心机制为", NodeOptions{})
	if err != nil {
		t.Fatalf("ensure rel 2: %v", err)
	}
	if _, err := store.AppendAssertion(AppendAssertionRequest{
		Scope:           testScope,
		RelID:           rel2,
		SubjectID:       &subject,
		ObjectLiteral:   &validListAndInterval,
		Warrant:         "referential",
		AnchorTraceHash: th2,
		AnchorStart:     0,
		AnchorEnd:       len([]rune(validListAndInterval)),
		Confidence:      0.95,
	}); err != nil {
		t.Fatalf("append valid list/interval: %v", err)
	}
	healthAfter, err := store.Health(testScope, 0)
	if err != nil {
		t.Fatalf("health after: %v", err)
	}
	qAfter := healthAfter["quality"].(map[string]any)
	if qAfter["unclosed_literals"] != 1 {
		t.Fatalf("expected valid list numbering and half-open intervals not to increment unclosed_literals, got %v", qAfter)
	}
}

func TestHealthCountsGapsConvergenceConflictsAndCoordinates(t *testing.T) {
	store := newTestStore(t, testScope)
	addFact(t, store, testScope, "老张养了一只猫。", "老张", "养", "猫", "老张养了一只猫", factOptions{
		TValidFrom: 1772409600, Holder: "我",
	})
	addFact(t, store, testScope, "老张养了一只狗。", "老张", "养", "狗", "老张养了一只狗", factOptions{Holder: "我"})
	traceHash := queryText(t, store, "SELECT hash FROM trace LIMIT 1")
	if err := store.AddMention(testScope, traceHash, "某人", 0, 2, nil); err != nil {
		t.Fatalf("add mention: %v", err)
	}
	temporary, err := store.EnsureNode(testScope, "referent", "临时节点", NodeOptions{})
	if err != nil {
		t.Fatalf("ensure temp: %v", err)
	}
	if _, err := store.PromoteNode(temporary, "user"); err != nil {
		t.Fatalf("promote: %v", err)
	}
	if _, err := store.Event(testScope, "assert.rejected", map[string]any{
		"anchor_failed": 1, "resolve_failed": 0,
	}, "", "user"); err != nil {
		t.Fatalf("rejected event: %v", err)
	}

	health, err := store.Health(testScope, 0)
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	if health["ok"] != true || health["scope"] != testScope {
		t.Fatalf("health header = %v", health)
	}
	gaps := health["gaps"].(map[string]any)
	if gaps["mentions"] != int64(1) || gaps["unresolved_mentions"] != 1 {
		t.Fatalf("gaps = %v", gaps)
	}
	if gaps["unresolved_ratio"] != 1.0 {
		t.Fatalf("unresolved_ratio = %v", gaps["unresolved_ratio"])
	}
	if gaps["rejected_events"] != 1 || gaps["rejected_anchor_failed"] != int64(1) || gaps["rejected_resolve_failed"] != int64(0) {
		t.Fatalf("rejected = %v", gaps)
	}
	convergence := health["convergence"].(map[string]any)
	if convergence["created_nodes"] != 6 || convergence["converged_nodes"] != 1 {
		t.Fatalf("convergence = %v", convergence)
	}
	if convergence["convergence_rate"] != 0.1667 || convergence["promotes"] != 1 {
		t.Fatalf("convergence = %v", convergence)
	}
	conflicts := health["conflicts"].(map[string]any)
	if conflicts["proposed"] != int64(1) || conflicts["oldest_age_days"] != 0.0 {
		t.Fatalf("conflicts = %v", conflicts)
	}
	coordinates := health["coordinates"].(map[string]any)
	if coordinates["active_assertions"] != 2 || coordinates["unknown_time"] != 1 ||
		coordinates["known_time_ratio"] != 0.5 {
		t.Fatalf("coordinates = %v", coordinates)
	}
	volume := health["volume"].(map[string]any)
	counts := volume["counts"].(map[string]int)
	if counts["trace"] != 2 || counts["assertion"] != 2 {
		t.Fatalf("volume counts = %v", counts)
	}
	created := volume["created_in_window"].(map[string]int)
	if created["trace"] != 2 || created["assertion"] != 2 || created["mention"] != 1 {
		t.Fatalf("created_in_window = %v", created)
	}

	windowed, err := store.Health(testScope, testNow+3600)
	if err != nil {
		t.Fatalf("windowed health: %v", err)
	}
	if windowed["gaps"].(map[string]any)["rejected_events"] != 0 {
		t.Fatalf("windowed rejected = %v", windowed["gaps"])
	}
	if windowed["convergence"].(map[string]any)["created_nodes"] != 0 {
		t.Fatalf("windowed created = %v", windowed["convergence"])
	}
	if windowed["volume"].(map[string]any)["created_in_window"].(map[string]int)["trace"] != 0 {
		t.Fatalf("windowed volume = %v", windowed["volume"])
	}
	if windowed["conflicts"].(map[string]any)["proposed"] != int64(0) {
		t.Fatalf("windowed conflicts = %v", windowed["conflicts"])
	}
}

func TestWhyAssertionExpandsEvidenceChain(t *testing.T) {
	store := newTestStore(t, testScope)
	receipt := addFact(t, store, testScope, "老张养了一只猫。", "老张", "养", "猫", "老张养了一只猫", factOptions{
		TValidFrom: 1772409600, Warrant: "experiential", Holder: "我",
	})
	second := addFact(t, store, testScope, "老张养了一只狗。", "老张", "养", "狗", "老张养了一只狗", factOptions{
		Holder: "我", Warrant: "experiential",
	})
	if second["conflicts"] != 1 {
		t.Fatalf("dog receipt = %v", second)
	}
	detail, err := store.Why(receipt["id"].(string), 20)
	if err != nil {
		t.Fatalf("why: %v", err)
	}
	if detail["kind"] != "assertion" || detail["warrant"] != "experiential" {
		t.Fatalf("detail = %v", detail)
	}
	if detail["anchor_text"] != "老张养了一只猫" {
		t.Fatalf("anchor_text = %q", detail["anchor_text"])
	}
	span := detail["anchor_span"].([]int)
	if span[0] != 0 || span[1] != 7 {
		t.Fatalf("anchor_span = %v", span)
	}
	if detail["temporal_class"] != "past" || detail["confidence"] != 0.8 {
		t.Fatalf("detail = %v", detail)
	}
	trace := detail["trace"].(map[string]any)
	if trace["session"] != "default" || trace["author"] != "user" {
		t.Fatalf("trace = %v", trace)
	}
	if detail["created_by"] != nil {
		t.Fatalf("created_by = %v", detail["created_by"])
	}
	holder := detail["holder"].(map[string]any)
	if holder["label"] != "我" || holder["id"] == nil {
		t.Fatalf("holder = %v", holder)
	}
	conflicts := detail["conflicts"].([]map[string]any)
	if len(conflicts) != 1 || conflicts[0]["status"] != "proposed" {
		t.Fatalf("conflicts = %v", conflicts)
	}

	if _, err := store.Why("n_missing", 20); err == nil {
		t.Fatal("expected unknown target error")
	}
}

func TestWhyNodeListsAssertionsAndSameAs(t *testing.T) {
	store := newTestStore(t, testScope)
	keep, _ := store.EnsureNode(testScope, "referent", "老张", NodeOptions{})
	drop, _ := store.EnsureNode(testScope, "referent", "张先生", NodeOptions{})
	addFact(t, store, testScope, "老张养了一只猫。", "老张", "养", "猫", "老张养了一只猫", factOptions{})
	addFact(t, store, testScope, "张先生住在北京。", "张先生", "住在", "北京", "张先生住在北京", factOptions{})
	if _, err := store.MergeNodes(keep, drop, "user"); err != nil {
		t.Fatalf("merge: %v", err)
	}
	detail, err := store.Why(keep, 20)
	if err != nil {
		t.Fatalf("why keep: %v", err)
	}
	if detail["kind"] != "node" {
		t.Fatalf("kind = %v", detail["kind"])
	}
	sameAs := detail["same_as"].(map[string]any)
	mergedFrom := sameAs["merged_from"].([]string)
	if len(mergedFrom) != 1 || mergedFrom[0] != drop {
		t.Fatalf("merged_from = %v", mergedFrom)
	}
	if len(detail["assertions"].([]map[string]any)) != 2 {
		t.Fatalf("assertions = %v", detail["assertions"])
	}
	merged, err := store.Why(drop, 20)
	if err != nil {
		t.Fatalf("why drop: %v", err)
	}
	if merged["node"].(map[string]any)["status"] != "merged" {
		t.Fatalf("merged node = %v", merged["node"])
	}
	if merged["same_as"].(map[string]any)["merges_to"] != keep {
		t.Fatalf("merges_to = %v", merged["same_as"])
	}
}

func TestChangedReturnsVersionSequence(t *testing.T) {
	store := newTestStore(t, testScope)
	first := addFact(t, store, testScope, "老张住在北京。", "老张", "住在", "北京", "老张住在北京", factOptions{TValidFrom: 1772409600})
	second := addFact(t, store, testScope, "老张搬到了上海。", "老张", "搬到", "上海", "老张搬到了上海", factOptions{TValidFrom: 1780272000})
	node, ok, _ := store.NodeByLabel(testScope, "老张", "")
	if !ok {
		t.Fatal("missing node")
	}
	changed, err := store.Changed(node.ID, 50)
	if err != nil {
		t.Fatalf("changed: %v", err)
	}
	if changed["count"] != 2 {
		t.Fatalf("count = %v", changed["count"])
	}
	sequence := changed["sequence"].([]map[string]any)
	if sequence[0]["id"] != first["id"] || sequence[1]["id"] != second["id"] {
		t.Fatalf("sequence ids = %v/%v", sequence[0]["id"], sequence[1]["id"])
	}
	if _, err := store.Changed("n_missing", 50); err == nil {
		t.Fatal("expected unknown node error")
	}
}

func TestChangedReasonsSupersedeAndConflicts(t *testing.T) {
	store := newTestStore(t, testScope)
	first := addFact(t, store, testScope, "老张养了一只猫。", "老张", "养", "猫", "老张养了一只猫", factOptions{})
	second := addFact(t, store, testScope, "老张养了一只狗。", "老张", "养", "狗", "老张养了一只狗", factOptions{})
	node, _, _ := store.NodeByLabel(testScope, "老张", "")
	if _, err := store.ResolveConflict(second["id"].(string), first["id"].(string), "", false, "user"); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	changed, err := store.Changed(node.ID, 50)
	if err != nil {
		t.Fatalf("changed: %v", err)
	}
	if changed["node"].(map[string]any)["label"] != "老张" {
		t.Fatalf("node = %v", changed["node"])
	}
	sequence := changed["sequence"].([]map[string]any)
	reason := ""
	for _, item := range sequence {
		if item["id"] == first["id"] {
			reason, _ = item["reason"].(string)
			if item["status"] != "superseded" {
				t.Fatalf("loser status = %v", item["status"])
			}
		}
		if item["id"] == second["id"] && item["reason"] != "" {
			t.Fatalf("winner reason = %v", item["reason"])
		}
	}
	if reason != "superseded_by "+second["id"].(string)+" (老张 —养→ 狗)" {
		t.Fatalf("reason = %q", reason)
	}
}

// TestWhyReportsSupersedesAndConflicts verifies that Why reports active
// supersedes and conflicts edges alongside the superseded status.
func TestWhyReportsSupersedesAndConflicts(t *testing.T) {
	store := newTestStore(t, testScope)
	first := addFact(t, store, testScope, "老张养了一只猫。", "老张", "养", "猫", "老张养了一只猫", factOptions{})
	second := addFact(t, store, testScope, "老张养了一只狗。", "老张", "养", "狗", "老张养了一只狗", factOptions{})
	if second["conflicts"] != 1 {
		t.Fatalf("dog receipt = %v", second)
	}
	if _, err := store.conn.Exec(
		`INSERT INTO meta_edge(scope, meta_kind, lhs_id, rhs_id, status, evidence_anchor, created_at, created_by)
		 VALUES (?, 'supersedes', ?, ?, 'active', '', ?, 'user')`,
		testScope, second["id"].(string), first["id"].(string), testNow); err != nil {
		t.Fatalf("insert supersedes: %v", err)
	}
	if _, err := store.conn.Exec(
		"UPDATE assertion SET status = 'superseded' WHERE node_id = ?", first["id"]); err != nil {
		t.Fatalf("mark superseded: %v", err)
	}

	old, err := store.Why(first["id"].(string), 20)
	if err != nil {
		t.Fatalf("why old: %v", err)
	}
	if old["status"] != "superseded" {
		t.Fatalf("old status = %v", old["status"])
	}
	supersededBy := old["superseded_by"].([]map[string]any)
	if len(supersededBy) != 1 || supersededBy[0]["id"] != second["id"] {
		t.Fatalf("superseded_by = %v", supersededBy)
	}

	new, err := store.Why(second["id"].(string), 20)
	if err != nil {
		t.Fatalf("why new: %v", err)
	}
	supersedes := new["supersedes"].([]map[string]any)
	if len(supersedes) != 1 || supersedes[0]["id"] != first["id"] {
		t.Fatalf("supersedes = %v", supersedes)
	}
	conflicts := new["conflicts"].([]map[string]any)
	if len(conflicts) < 1 || conflicts[0]["status"] != "proposed" {
		t.Fatalf("conflicts = %v", conflicts)
	}
}

func TestPortraitReportsSpanAndGaps(t *testing.T) {
	store := newTestStore(t, testScope)
	addFact(t, store, testScope, "老张住在北京。", "老张", "住在", "北京", "老张住在北京", factOptions{TValidFrom: 1772409600})
	addFact(t, store, testScope, "老张养了一只猫。", "老张", "养", "猫", "老张养了一只猫", factOptions{})
	node, _, _ := store.NodeByLabel(testScope, "老张", "")
	shot, err := store.Portrait(node.ID, 50)
	if err != nil {
		t.Fatalf("portrait: %v", err)
	}
	if shot["kind"] != "portrait" {
		t.Fatalf("kind = %v", shot["kind"])
	}
	span := shot["span"].(map[string]any)
	if span["label"] != "2026-03-02 ~ 2026-10-27" {
		t.Fatalf("span = %v", span)
	}
	gaps := shot["gaps"].(map[string]any)
	if gaps["no_time"] != 1 || gaps["assertions"] != 2 {
		t.Fatalf("gaps = %v", gaps)
	}
	if _, err := store.Portrait("n_missing", 50); err == nil {
		t.Fatal("expected unknown node error")
	}
	assertionID := queryText(t, store, "SELECT node_id FROM assertion LIMIT 1")
	if _, err := store.Portrait(assertionID, 50); err == nil {
		t.Fatal("expected assertion portrait error")
	}
}

func TestGovernanceReportDetectsAliasesSimilarLabelsAndStaleVectors(t *testing.T) {
	store := newTestStore(t, testScope)
	alice, _ := store.EnsureNode(testScope, "referent", "Alice", NodeOptions{})
	alicia, _ := store.EnsureNode(testScope, "referent", "Alicia", NodeOptions{})
	if _, err := store.AddAlias(alice, "al", "manual"); err != nil {
		t.Fatalf("alias alice: %v", err)
	}
	if _, err := store.AddAlias(alicia, "al", "manual"); err != nil {
		t.Fatalf("alias alicia: %v", err)
	}
	addFact(t, store, testScope, "Alice养了一只猫。", "Alice", "养", "猫", "Alice养了一只猫", factOptions{})
	addFact(t, store, testScope, "Alicia养了一只狗。", "Alicia", "养", "狗", "Alicia养了一只狗", factOptions{})
	ghost, _ := store.EnsureNode(testScope, "referent", "ghost", NodeOptions{})
	if _, err := store.conn.Exec("INSERT INTO vector(owner_kind, owner_hash, model, dim, vec, updated_at) VALUES ('node', ?, 'hash', 4, X'00', 1)", ghost); err != nil {
		t.Fatalf("ghost vectors: %v", err)
	}
	if _, err := store.conn.Exec("DELETE FROM node WHERE id = ?", ghost); err != nil {
		t.Fatalf("delete ghost: %v", err)
	}

	report, err := store.GovernanceReport(testScope, 20)
	if err != nil {
		t.Fatalf("governance: %v", err)
	}
	collisions := report["alias_collisions"].([]map[string]any)
	if len(collisions) != 1 || collisions[0]["alias"] != "al" {
		t.Fatalf("alias_collisions = %v", collisions)
	}
	nodes := collisions[0]["nodes"].([]map[string]any)
	if len(nodes) != 2 || mapString(nodes[0], "id") > mapString(nodes[1], "id") {
		t.Fatalf("collision nodes = %v", nodes)
	}
	similar := report["similar_labels"].([]map[string]any)
	if len(similar) != 1 || similar[0]["ratio"] != 0.727 {
		t.Fatalf("similar_labels = %v", similar)
	}
	if report["stale_vectors"] != int64(1) {
		t.Fatalf("stale_vectors = %v", report["stale_vectors"])
	}
	counts := report["counts"].(map[string]int64)
	if counts["provisional"] != 5 {
		t.Fatalf("counts = %v", counts)
	}
	provisional := report["provisional"].([]map[string]any)
	if len(provisional) == 0 || mapString(provisional[0], "label") != "养" {
		t.Fatalf("provisional = %v", provisional)
	}
}

func TestCandidatesPreservesSupersededRowsInNonGoldenScope(t *testing.T) {
	st := newTestStore(t, "mem:self")
	first := addFact(t, st, "mem:self", "帮助中心路由定为 views/admin/help/index.vue。", "帮助中心路由", "定为", "views/admin/help/index.vue", "views/admin/help/index.vue", factOptions{})
	second := addFact(t, st, "mem:self", "帮助中心路由已迁移至 views/admin/help/v2.vue。", "帮助中心路由", "定为", "views/admin/help/v2.vue", "views/admin/help/v2.vue", factOptions{})
	if _, err := st.conn.Exec("UPDATE assertion SET status = 'superseded' WHERE node_id = ?", first["id"]); err != nil {
		t.Fatalf("mark superseded: %v", err)
	}
	subjNode, ok, err := st.NodeByLabel("mem:self", "帮助中心路由", "")
	if err != nil || !ok {
		t.Fatalf("find subject node: ok=%v err=%v", ok, err)
	}
	cands, _, err := st.Candidates([]string{subjNode.ID}, CandidateOptions{
		Scope:  "mem:self",
		Reader: "owner",
		Depth:  1,
		Limit:  64,
	})
	if err != nil {
		t.Fatalf("Candidates: %v", err)
	}
	seenFirst, seenSecond := false, false
	for _, c := range cands {
		if c.Data.NodeID == first["id"].(string) {
			seenFirst = true
		}
		if c.Data.NodeID == second["id"].(string) {
			seenSecond = true
		}
	}
	if !seenFirst || !seenSecond {
		t.Fatalf("expected non-golden Candidates union subquery to preserve both active and superseded rows, got first=%v second=%v (len=%d)", seenFirst, seenSecond, len(cands))
	}
}
