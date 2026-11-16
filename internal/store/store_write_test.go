// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package store

import (
	"encoding/json"
	"testing"
)

const testScope = "mem:test"

func TestStructuredWriteAndIdempotency(t *testing.T) {
	store := newTestStore(t, testScope)
	receipt := addFact(t, store, testScope, "老张养了一只猫，叫小白。", "老张", "养", "小白", "老张养了一只猫", factOptions{})
	if receipt["action"] != "written" {
		t.Fatalf("action = %v, want written", receipt["action"])
	}
	if countRows(t, store, "assertion") != 1 || countRows(t, store, "trace") != 1 {
		t.Fatalf("assertion/trace counts = %d/%d", countRows(t, store, "assertion"), countRows(t, store, "trace"))
	}

	again := addFact(t, store, testScope, "老张养了一只猫，叫小白。", "老张", "养", "小白", "老张养了一只猫", factOptions{})
	if again["action"] != "skipped" || again["reason"] != "idempotent" {
		t.Fatalf("second write = %v, want skipped/idempotent", again)
	}
	if again["id"] != receipt["id"] {
		t.Fatalf("second id = %v, want %v", again["id"], receipt["id"])
	}
	if countRows(t, store, "assertion") != 1 {
		t.Fatalf("assertion count = %d, want 1", countRows(t, store, "assertion"))
	}
}

func TestCoverageAndTimeBackfill(t *testing.T) {
	store := newTestStore(t, testScope)
	first := addFact(t, store, testScope, "老张养了一只猫，叫小白。", "老张", "养", "小白", "老张养了一只猫", factOptions{})
	if first["action"] != "written" {
		t.Fatalf("first action = %v", first["action"])
	}
	second := addFact(t, store, testScope, "老张养了一只猫", "老张", "养", "小白", "老张养了一只猫", factOptions{TValidFrom: 1772409600})
	if second["action"] != "supplemented" {
		t.Fatalf("second action = %v, want supplemented", second["action"])
	}
	if second["id"] != first["id"] {
		t.Fatalf("supplement id = %v, want %v", second["id"], first["id"])
	}
	row, err := queryOneMap(store.conn,
		"SELECT t_valid_from, temporal_class FROM assertion WHERE node_id = ?", first["id"])
	if err != nil {
		t.Fatalf("read assertion: %v", err)
	}
	if mapInt64(row, "t_valid_from") != 1772409600 || mapString(row, "temporal_class") != "past" {
		t.Fatalf("backfill row = %v", row)
	}
	if countRows(t, store, "assertion") != 1 {
		t.Fatalf("assertion count = %d, want 1", countRows(t, store, "assertion"))
	}
	note := queryText(t, store,
		"SELECT note FROM event WHERE op = 'assert.time_backfill' ORDER BY id DESC LIMIT 1")
	if note != "single-direction backfill 0 -> value" {
		t.Fatalf("backfill note = %q", note)
	}
}

func TestAnchorGateRejectsUnpinnedEvidence(t *testing.T) {
	store := newTestStore(t, testScope)
	subject, _ := store.EnsureNode(testScope, "referent", "老张", NodeOptions{})
	relation, _ := store.EnsureNode(testScope, "relation_type", "养", NodeOptions{})
	object, _ := store.EnsureNode(testScope, "referent", "狗", NodeOptions{})
	trace, _, err := store.AddTrace(testScope, "老张养了一只猫。", TraceOptions{Author: "user"})
	if err != nil {
		t.Fatalf("add trace: %v", err)
	}
	base := AppendAssertionRequest{
		Scope:           testScope,
		RelID:           relation,
		AnchorTraceHash: trace,
		SubjectID:       &subject,
		ObjectID:        &object,
		Warrant:         "referential",
		Audience:        "shared",
		Confidence:      0.8,
		Status:          "active",
	}
	for name, mutate := range map[string]func(*AppendAssertionRequest){
		"empty span":       func(r *AppendAssertionRequest) { r.AnchorStart, r.AnchorEnd = 0, 0 },
		"exceeds text":     func(r *AppendAssertionRequest) { r.AnchorStart, r.AnchorEnd = 0, 99 },
		"negative start":   func(r *AppendAssertionRequest) { r.AnchorStart, r.AnchorEnd = -1, 2 },
		"missing trace":    func(r *AppendAssertionRequest) { r.AnchorTraceHash = "missing" },
		"missing source":   func(r *AppendAssertionRequest) { r.SourceTraceHash = strPtr("missing") },
		"invalid audience": func(r *AppendAssertionRequest) { r.Audience = "world" },
		"invalid warrant":  func(r *AppendAssertionRequest) { r.Warrant = "guess" },
		"invalid status":   func(r *AppendAssertionRequest) { r.Status = "gone" },
		"no subject":       func(r *AppendAssertionRequest) { r.SubjectID = nil },
		"no object":        func(r *AppendAssertionRequest) { r.ObjectID = nil },
	} {
		t.Run(name, func(t *testing.T) {
			request := base
			mutate(&request)
			if _, err := store.AppendAssertion(request); err == nil {
				t.Fatalf("expected error")
			}
		})
	}
	if countRows(t, store, "assertion") != 0 {
		t.Fatalf("assertion count = %d, want 0", countRows(t, store, "assertion"))
	}
}

func TestBlankAnchorSpanRejected(t *testing.T) {
	store := newTestStore(t, testScope)
	subject, _ := store.EnsureNode(testScope, "referent", "老张", NodeOptions{})
	relation, _ := store.EnsureNode(testScope, "relation_type", "养", NodeOptions{})
	object, _ := store.EnsureNode(testScope, "referent", "猫", NodeOptions{})
	trace, _, _ := store.AddTrace(testScope, "a  b", TraceOptions{Author: "user"})
	_, err := store.AppendAssertion(AppendAssertionRequest{
		Scope:           testScope,
		RelID:           relation,
		AnchorTraceHash: trace,
		AnchorStart:     1,
		AnchorEnd:       2,
		SubjectID:       &subject,
		ObjectID:        &object,
		Confidence:      0.8,
	})
	if err == nil {
		t.Fatal("expected empty anchor span error")
	}
}

func TestConflictIsProposedAndResolvable(t *testing.T) {
	store := newTestStore(t, testScope)
	first := addFact(t, store, testScope, "老张养了一只猫。", "老张", "养", "猫", "老张养了一只猫", factOptions{})
	second := addFact(t, store, testScope, "老张养了一只狗。", "老张", "养", "狗", "老张养了一只狗", factOptions{})
	if second["conflicts"] != 1 {
		t.Fatalf("conflicts = %v, want 1", second["conflicts"])
	}
	conflicts, err := store.ListConflicts(testScope, 20)
	if err != nil {
		t.Fatalf("list conflicts: %v", err)
	}
	if len(conflicts) != 1 || conflicts[0]["edge_id"] == nil {
		t.Fatalf("conflicts = %v", conflicts)
	}
	count, err := store.ConflictCount([]string{first["id"].(string), second["id"].(string)})
	if err != nil || count != 1 {
		t.Fatalf("conflict count = %d, err = %v", count, err)
	}

	resolved, err := store.ResolveConflict(second["id"].(string), first["id"].(string), "dog wins", false, "user")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if resolved["loser_status"] != "superseded" {
		t.Fatalf("loser_status = %v", resolved["loser_status"])
	}
	status := queryText(t, store, "SELECT status FROM assertion WHERE node_id = ?", first["id"])
	if status != "superseded" {
		t.Fatalf("loser status = %q", status)
	}
	conflicts, err = store.ListConflicts(testScope, 20)
	if err != nil || len(conflicts) != 0 {
		t.Fatalf("conflicts after resolve = %v, err = %v", conflicts, err)
	}
	detail, err := store.Why(first["id"].(string), 20)
	if err != nil {
		t.Fatalf("why: %v", err)
	}
	if detail["status"] != "superseded" {
		t.Fatalf("why status = %v", detail["status"])
	}
	if len(detail["superseded_by"].([]map[string]any)) != 1 {
		t.Fatalf("superseded_by = %v", detail["superseded_by"])
	}
}

// A retraction takes its proposed conflicts with it: the edge must not stay in
// the governance ledger pointing at a retracted row.
func TestRetractRevertsProposedConflict(t *testing.T) {
	store := newTestStore(t, testScope)
	first := addFact(t, store, testScope, "老张养了一只猫。", "老张", "养", "猫", "老张养了一只猫", factOptions{})
	second := addFact(t, store, testScope, "老张养了一只狗。", "老张", "养", "狗", "老张养了一只狗", factOptions{})
	if second["conflicts"] != 1 {
		t.Fatalf("conflicts = %v, want 1", second["conflicts"])
	}
	if _, err := store.RetractAssertion(first["id"].(string), "noise cleanup", "user"); err != nil {
		t.Fatalf("retract: %v", err)
	}
	conflicts, err := store.ListConflicts(testScope, 20)
	if err != nil || len(conflicts) != 0 {
		t.Fatalf("conflicts after retract = %v, err = %v", conflicts, err)
	}
	status := queryText(t, store,
		"SELECT status FROM meta_edge WHERE meta_kind = 'conflicts-with' ORDER BY id DESC LIMIT 1")
	if status != "reverted" {
		t.Fatalf("edge status = %q, want reverted", status)
	}
}

func TestPrivateAudienceNarrowingAndReadVisibility(t *testing.T) {
	store := newTestStore(t, testScope)
	receipt := addFact(t, store, testScope, "绝密信息：老张的暗号是蓝鸟。", "老张", "暗号", "蓝鸟", "老张的暗号是蓝鸟", factOptions{
		Audience: "private",
		Warrant:  "experiential",
	})
	if receipt["audience"] != "private" || receipt["audience_narrowed"] != false {
		t.Fatalf("receipt = %v", receipt)
	}
	subject, _, _ := store.NodeByLabel(testScope, "老张", "")
	deliverable, gaps, err := store.Candidates([]string{subject.ID}, CandidateOptions{
		Scope: testScope, Reader: "deliverable",
	})
	if err != nil {
		t.Fatalf("deliverable candidates: %v", err)
	}
	if len(deliverable) != 0 || gaps["audience_excluded"] != 1 {
		t.Fatalf("deliverable = %d gaps=%v", len(deliverable), gaps)
	}
	owner, _, err := store.Candidates([]string{subject.ID}, CandidateOptions{
		Scope: testScope, Reader: "owner",
	})
	if err != nil {
		t.Fatalf("owner candidates: %v", err)
	}
	if len(owner) != 1 || owner[0].Data.Audience != "private" {
		t.Fatalf("owner candidates = %v", owner)
	}
}

func TestBareConflictNotRecorded(t *testing.T) {
	store := newTestStore(t, testScope)
	first := addFact(t, store, testScope, "老张养了一只猫。", "老张", "养", "猫", "老张养了一只猫", factOptions{})
	second := addFact(t, store, testScope, "老张养了一只狗。", "老张", "养", "狗", "老张养了一只狗", factOptions{Bare: true})
	if second["conflicts"] != 0 {
		t.Fatalf("bare conflicts = %v, want 0", second["conflicts"])
	}
	if countRows(t, store, "meta_edge") != 0 {
		t.Fatalf("meta_edge count = %d", countRows(t, store, "meta_edge"))
	}
	if first["action"] != "written" {
		t.Fatalf("first action = %v", first["action"])
	}
}

// TestBareAssertionIsNominated verifies that the store keeps bare assertions
// nominated while delivery excludes them and records gaps.bare_excluded.
func TestBareAssertionIsNominated(t *testing.T) {
	store := newTestStore(t, testScope)
	receipt := addFact(t, store, testScope, "老张养了一只猫。", "老张", "养", "猫", "老张养了一只猫", factOptions{Bare: true})
	if receipt["action"] != "written" {
		t.Fatalf("bare action = %v, want written", receipt["action"])
	}
	subject, ok, err := store.NodeByLabel(testScope, "老张", "")
	if err != nil || !ok {
		t.Fatalf("subject lookup = %v ok=%v err=%v", subject, ok, err)
	}
	candidates, _, err := store.Candidates([]string{subject.ID}, CandidateOptions{
		Scope: testScope, Reader: "deliverable",
	})
	if err != nil {
		t.Fatalf("candidates: %v", err)
	}
	if len(candidates) != 1 || !candidates[0].Data.Bare {
		t.Fatalf("bare candidate was not nominated: %#v", candidates)
	}
	if candidates[0].Data.NodeID != receipt["id"].(string) {
		t.Fatalf("candidate node = %v, want %v", candidates[0].Data.NodeID, receipt["id"])
	}
}

func TestEnsureNodeAliasesAndFTS(t *testing.T) {
	store := newTestStore(t, testScope)
	nodeID, err := store.EnsureNode(testScope, "referent", "老张", NodeOptions{})
	if err != nil {
		t.Fatalf("ensure node: %v", err)
	}
	sameID, err := store.EnsureNode(testScope, "referent", "  老张。 ", NodeOptions{})
	if err != nil || sameID != nodeID {
		t.Fatalf("normalized ensure = %q, err=%v", sameID, err)
	}
	inserted, err := store.AddAlias(nodeID, "张先生", "manual")
	if err != nil || !inserted {
		t.Fatalf("add alias = %v, err=%v", inserted, err)
	}
	again, err := store.AddAlias(nodeID, "张先生", "manual")
	if err != nil || again {
		t.Fatalf("duplicate alias = %v, err=%v", again, err)
	}
	found, ok, err := store.NodeByLabel(testScope, "张先生", "")
	if err != nil || !ok || found.ID != nodeID {
		t.Fatalf("alias lookup = %v ok=%v err=%v", found, ok, err)
	}
	aliases, err := store.Aliases(nodeID)
	if err != nil || len(aliases) != 1 || aliases[0] != "张先生" {
		t.Fatalf("aliases = %v, err=%v", aliases, err)
	}
	seeds, legs, err := store.SearchSeeds("张先生", SeedOptions{Scope: testScope, Reader: "deliverable"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(seeds) == 0 || legs["alias"] == 0 {
		t.Fatalf("seeds = %v legs=%v", seeds, legs)
	}
}

func TestClosureAndCanonicalMap(t *testing.T) {
	store := newTestStore(t, testScope)
	keep, _ := store.EnsureNode(testScope, "referent", "老张", NodeOptions{})
	drop, _ := store.EnsureNode(testScope, "referent", "张先生", NodeOptions{})
	if _, err := store.MergeNodes(keep, drop, "user"); err != nil {
		t.Fatalf("merge: %v", err)
	}
	closure, err := store.Closure(testScope, []string{drop}, []string{"same-as"}, 128)
	if err != nil || len(closure) != 2 || closure[0] != drop || closure[1] != keep {
		t.Fatalf("closure = %v, err=%v", closure, err)
	}
	canonical, err := store.CanonicalMap(testScope, []string{keep, drop})
	if err != nil {
		t.Fatalf("canonical: %v", err)
	}
	if canonical[drop] != keep || canonical[keep] != keep {
		t.Fatalf("canonical = %v", canonical)
	}
	if _, err := store.UnmergeNodes(keep, drop, "user"); err != nil {
		t.Fatalf("unmerge: %v", err)
	}
	closure, _ = store.Closure(testScope, []string{drop}, []string{"same-as"}, 128)
	if len(closure) != 1 || closure[0] != drop {
		t.Fatalf("closure after unmerge = %v", closure)
	}
}

func TestMetaEdgesSubtypeAndInverse(t *testing.T) {
	store := newTestStore(t, testScope)
	relation, _ := store.EnsureNode(testScope, "relation_type", "养", NodeOptions{})
	broader, _ := store.EnsureNode(testScope, "relation_type", "拥有", NodeOptions{})
	added, err := store.AddMetaEdge(testScope, "subtype-of", relation, broader, "", "user")
	if err != nil || added["inserted"] != true {
		t.Fatalf("add meta = %v err=%v", added, err)
	}
	again, err := store.AddMetaEdge(testScope, "subtype-of", relation, broader, "", "user")
	if err != nil || again["inserted"] != false {
		t.Fatalf("duplicate meta = %v err=%v", again, err)
	}
	closure, err := store.Closure(testScope, []string{relation}, []string{"subtype-of"}, 128)
	if err != nil || len(closure) != 2 {
		t.Fatalf("subtype closure = %v err=%v", closure, err)
	}
	removed, err := store.RemoveMetaEdge(testScope, "subtype-of", relation, broader, "user")
	if err != nil || removed["reverted"] != int64(1) {
		t.Fatalf("remove meta = %v err=%v", removed, err)
	}
	if _, err := store.RemoveMetaEdge(testScope, "subtype-of", relation, broader, "user"); err == nil {
		t.Fatal("expected missing edge error")
	}
	if _, err := store.AddMetaEdge(testScope, "same-as", relation, broader, "", "user"); err == nil {
		t.Fatal("expected meta kind error")
	}
}

func TestRewindSessionResetsCursorWithoutDeleting(t *testing.T) {
	store := newTestStore(t, testScope)
	addFact(t, store, testScope, "老张养了一只猫。", "老张", "养", "猫", "老张养了一只猫", factOptions{Session: "s1"})
	traceHash, _, _ := store.AddTrace(testScope, "老张养了一只狗。", TraceOptions{Session: "s1", Turn: 1, Author: "user"})
	if err := store.MarkProcessed(traceHash); err != nil {
		t.Fatalf("mark: %v", err)
	}
	if err := store.UpdateSessionCursor(testScope, "s1", 1); err != nil {
		t.Fatalf("cursor: %v", err)
	}
	if _, err := store.RewindSession(testScope, "s1", "  ", "user"); err == nil {
		t.Fatal("expected reason error")
	}
	rewind, err := store.RewindSession(testScope, "s1", "gold fix", "user")
	if err != nil {
		t.Fatalf("rewind: %v", err)
	}
	if rewind["reset_traces"] != int64(1) {
		t.Fatalf("reset_traces = %v", rewind["reset_traces"])
	}
	if countRows(t, store, "assertion") != 1 {
		t.Fatalf("assertion count = %d", countRows(t, store, "assertion"))
	}
	cursor, err := queryOneMap(store.conn,
		"SELECT turn_done FROM session_cursor WHERE scope = ? AND session = ?", testScope, "s1")
	if err != nil || mapInt64(cursor, "turn_done") != -1 {
		t.Fatalf("cursor = %v err=%v", cursor, err)
	}
	note := queryText(t, store,
		"SELECT note FROM event WHERE op = 'replay.rewind' ORDER BY id DESC LIMIT 1")
	if note != "gold fix" {
		t.Fatalf("rewind note = %q", note)
	}
	if _, err := store.RewindSession(testScope, "", "reason", "user"); err == nil {
		t.Fatal("expected session error")
	}
}

func TestExportImportRoundtrip(t *testing.T) {
	source := newTestStore(t, testScope)
	addFact(t, source, testScope, "老张养了一只猫。", "老张", "养", "猫", "老张养了一只猫", factOptions{TValidFrom: 1772409600})
	payload, err := source.ExportScope(testScope)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if len(payload["assertions"].([]map[string]any)) != 1 {
		t.Fatalf("exported assertions = %v", payload["assertions"])
	}

	target := newTestStore(t, testScope)
	rawPayload, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal export: %v", err)
	}
	decoded := map[string]any{}
	if err := json.Unmarshal(rawPayload, &decoded); err != nil {
		t.Fatalf("decode export: %v", err)
	}
	inserted, err := target.ImportScope(testScope, decoded)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if inserted["assertions"] != 1 || inserted["traces"] != 1 {
		t.Fatalf("inserted = %v", inserted)
	}
	if countRows(t, target, "assertion") != 1 {
		t.Fatalf("target assertion count = %d", countRows(t, target, "assertion"))
	}
	again, err := target.ImportScope(testScope, decoded)
	if err != nil {
		t.Fatalf("second import: %v", err)
	}
	if again["assertions"] != 0 || again["traces"] != 0 {
		t.Fatalf("second inserted = %v", again)
	}
	seeds, _, err := target.SearchSeeds("猫", SeedOptions{Scope: testScope, Reader: "deliverable"})
	if err != nil || len(seeds) == 0 {
		t.Fatalf("FTS after import seeds=%v err=%v", seeds, err)
	}
}

func TestCheckDetectsBrokenAnchor(t *testing.T) {
	store := newTestStore(t, testScope)
	receipt := addFact(t, store, testScope, "老张养了一只猫。", "老张", "养", "猫", "老张养了一只猫", factOptions{})
	report, err := store.Check()
	if err != nil || report["ok"] != true {
		t.Fatalf("check = %v err=%v", report, err)
	}
	if _, err := store.conn.Exec(
		"UPDATE assertion SET anchor_end = anchor_end + 999 WHERE node_id = ?", receipt["id"]); err != nil {
		t.Fatalf("break anchor: %v", err)
	}
	report, err = store.Check()
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if report["ok"] != false {
		t.Fatalf("check ok = %v", report["ok"])
	}
	found := false
	for _, issue := range report["issues"].([]map[string]any) {
		if mapString(issue, "code") == "anchor_span" {
			found = true
		}
	}
	if !found {
		t.Fatalf("issues = %v", report["issues"])
	}
}

func TestStatusCounts(t *testing.T) {
	store := newTestStore(t, testScope)
	addFact(t, store, testScope, "老张养了一只猫。", "老张", "养", "猫", "老张养了一只猫", factOptions{})
	status, err := store.Status(testScope)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status["scope"] != testScope || status["active_assertions"] != int64(1) {
		t.Fatalf("status = %v", status)
	}
	if status["schema_version"] != int64(1) {
		t.Fatalf("schema_version = %v", status["schema_version"])
	}
	counts := status["counts"].(map[string]int)
	if counts["assertion"] != 1 || counts["schema_version"] != 1 {
		t.Fatalf("counts = %v", counts)
	}
	if _, ok := status["embedding"]; ok {
		t.Fatalf("legacy embedding field must be removed from Status(): %v", status["embedding"])
	}
}

func TestEventPayloads(t *testing.T) {
	store := newTestStore(t, testScope)
	addFact(t, store, testScope, "老张养了一只猫。", "老张", "养", "猫", "老张养了一只猫", factOptions{})
	rows, err := queryMaps(store.conn, "SELECT op, refs_json FROM event ORDER BY id")
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	seen := map[string]map[string]any{}
	nodeLabels := []string{}
	for _, row := range rows {
		refs := decodeJSONMap(t, mapString(row, "refs_json"))
		seen[mapString(row, "op")] = refs
		if mapString(row, "op") == "node.add" {
			nodeLabels = append(nodeLabels, mapString(refs, "label"))
		}
	}
	if seen["trace.add"]["session"] != "default" {
		t.Fatalf("trace.add refs = %v", seen["trace.add"])
	}
	if !containsValue(nodeLabels, "老张") {
		t.Fatalf("node.add labels = %v", nodeLabels)
	}
	if seen["assert.add"]["hash"] == nil {
		t.Fatalf("assert.add refs = %v", seen["assert.add"])
	}
}

// Literals keep the written form: ice stores object_literal verbatim and folds
// only for keys/comparison (lower(literal)); replays stay idempotent.
func TestLiteralKeepsWrittenForm(t *testing.T) {
	store := newTestStore(t, testScope)
	subject, _ := store.EnsureNode(testScope, "referent", "本机记忆核", NodeOptions{})
	relation, _ := store.EnsureNode(testScope, "relation_type", "二进制为", NodeOptions{})
	literal := "Mach-O 64-bit arm64"
	trace, _, _ := store.AddTrace(testScope, literal+"。", TraceOptions{Author: "user"})
	request := AppendAssertionRequest{
		Scope:           testScope,
		RelID:           relation,
		SubjectID:       &subject,
		ObjectLiteral:   &literal,
		Warrant:         "experiential",
		AnchorTraceHash: trace,
		AnchorStart:     0,
		AnchorEnd:       len([]rune(literal)),
		Confidence:      0.9,
	}
	receipt, err := store.AppendAssertion(request)
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	row, err := queryOneMap(store.conn,
		"SELECT object_literal FROM assertion WHERE node_id = ?", receipt["id"])
	if err != nil {
		t.Fatalf("read assertion: %v", err)
	}
	if mapString(row, "object_literal") != literal {
		t.Fatalf("stored literal = %q, want %q", mapString(row, "object_literal"), literal)
	}

	lowered := "mach-o 64-bit arm64"
	request.ObjectLiteral = &lowered
	again, err := store.AppendAssertion(request)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if again["action"] != "skipped" || again["reason"] != "idempotent" {
		t.Fatalf("replay = %v, want skipped/idempotent", again)
	}
	if countRows(t, store, "assertion") != 1 {
		t.Fatalf("assertion count = %d, want 1", countRows(t, store, "assertion"))
	}

	// Trailing 「）。」 and full-width punctuation must survive as written: the
	// pre-fix NormalizeLabel path folded （→( ；→; and ate the closing bracket
	// (live-graph regression: stored literals came back as unclosed
	// half-sentences).
	punctuated := "复制器材（dry-run 缺省；域集 = 全部；逐域对差 covered 判据）。"
	punctuatedTrace, _, err := store.AddTrace(testScope, punctuated, TraceOptions{Author: "user"})
	if err != nil {
		t.Fatalf("add punctuated trace: %v", err)
	}
	relation2, _ := store.EnsureNode(testScope, "relation_type", "说明", NodeOptions{})
	request2 := request
	request2.RelID = relation2
	request2.ObjectLiteral = &punctuated
	request2.AnchorTraceHash = punctuatedTrace
	request2.AnchorStart = 0
	request2.AnchorEnd = len([]rune(punctuated))
	receipt2, err := store.AppendAssertion(request2)
	if err != nil {
		t.Fatalf("append punctuated: %v", err)
	}
	row2, err := queryOneMap(store.conn,
		"SELECT object_literal FROM assertion WHERE node_id = ?", receipt2["id"])
	if err != nil {
		t.Fatalf("read punctuated: %v", err)
	}
	if mapString(row2, "object_literal") != punctuated {
		t.Fatalf("stored punctuated literal = %q, want %q", mapString(row2, "object_literal"), punctuated)
	}
}
