// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

// Verification of as-of raw legs, pre-write existing[] echo, activation ledger (store face),
// closure caps, t_source same-source, and loud raw errors.
package store

import (
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/rocareer/rolling-memory/internal/clock"
)

func TestClosureCapsHopsAndFanout(t *testing.T) {
	store := newTestStore(t, testScope)
	first, _ := store.EnsureNode(testScope, "referent", "甲", NodeOptions{})
	second, _ := store.EnsureNode(testScope, "referent", "乙", NodeOptions{})
	third, _ := store.EnsureNode(testScope, "referent", "丙", NodeOptions{})
	fourth, _ := store.EnsureNode(testScope, "referent", "丁", NodeOptions{})
	// same-as edges are written directly (like the oracle test): the public
	// AddMetaEdge only accepts subtype-of / inverse-of.
	for _, pair := range [][2]string{{first, second}, {second, third}, {third, fourth}} {
		if _, err := store.conn.Exec(
			`INSERT INTO meta_edge(scope, meta_kind, lhs_id, rhs_id, status, created_at)
			 VALUES (?, 'same-as', ?, ?, 'active', ?)`,
			testScope, pair[0], pair[1], clock.Unix(),
		); err != nil {
			t.Fatalf("add same-as: %v", err)
		}
	}
	closure, err := store.Closure(testScope, []string{first}, []string{"same-as"}, 128)
	if err != nil {
		t.Fatalf("closure: %v", err)
	}
	if !containsValue(closure, first) || !containsValue(closure, third) {
		t.Fatalf("closure = %v, want 甲 and 丙 reachable", closure)
	}
	if containsValue(closure, fourth) {
		t.Fatalf("closure = %v, want 丁 (3 hops) pruned by the hop cap", closure)
	}

	hub, _ := store.EnsureNode(testScope, "referent", "枢纽", NodeOptions{})
	for index := 0; index < 40; index++ {
		leaf, err := store.EnsureNode(testScope, "referent", "叶子"+strconv.Itoa(index), NodeOptions{})
		if err != nil {
			t.Fatalf("ensure leaf: %v", err)
		}
		if _, err := store.conn.Exec(
			`INSERT INTO meta_edge(scope, meta_kind, lhs_id, rhs_id, status, created_at)
			 VALUES (?, 'same-as', ?, ?, 'active', ?)`,
			testScope, hub, leaf, clock.Unix(),
		); err != nil {
			t.Fatalf("add leaf edge: %v", err)
		}
	}
	wide, err := store.Closure(testScope, []string{hub}, []string{"same-as"}, 128)
	if err != nil {
		t.Fatalf("wide closure: %v", err)
	}
	if len(wide) != 1+closureFanout {
		t.Fatalf("wide closure = %d, want %d (hub + fanout cap)", len(wide), 1+closureFanout)
	}
}

func TestTSourceFollowsSourceTrace(t *testing.T) {
	store := newTestStore(t, testScope)
	anchorTime := int64(1000)
	sourceTime := int64(2000)
	anchorHash, _, err := store.AddTrace(testScope, "锚句子在这里。", TraceOptions{
		Session: "s1", Turn: 1, OccurredAt: &anchorTime,
	})
	if err != nil {
		t.Fatalf("anchor trace: %v", err)
	}
	sourceHash, _, err := store.AddTrace(testScope, "来源句子在这里。", TraceOptions{
		Session: "s1", Turn: 2, OccurredAt: &sourceTime,
	})
	if err != nil {
		t.Fatalf("source trace: %v", err)
	}
	subject, _ := store.EnsureNode(testScope, "referent", "老张", NodeOptions{})
	relation, _ := store.EnsureNode(testScope, "relation_type", "养", NodeOptions{})
	object, _ := store.EnsureNode(testScope, "referent", "猫", NodeOptions{})
	result, err := store.AppendAssertion(AppendAssertionRequest{
		Scope:           testScope,
		RelID:           relation,
		SubjectID:       &subject,
		ObjectID:        &object,
		AnchorTraceHash: anchorHash,
		AnchorStart:     0,
		AnchorEnd:       4,
		SourceTraceHash: &sourceHash,
	})
	if err != nil {
		t.Fatalf("append assertion: %v", err)
	}
	row, err := queryOneMap(store.conn, "SELECT t_source FROM assertion WHERE node_id = ?", result["id"])
	if err != nil {
		t.Fatalf("select t_source: %v", err)
	}
	if mapInt64(row, "t_source") != sourceTime {
		t.Fatalf("t_source = %d, want %d (source trace, not the anchor trace)",
			mapInt64(row, "t_source"), sourceTime)
	}
}

func TestAppendAssertionEchoesExistingActiveValues(t *testing.T) {
	store := newTestStore(t, testScope)
	first := addFact(t, store, testScope, "老张养了一只猫。", "老张", "养", "猫", "老张养了一只猫", factOptions{})
	if pairs, ok := first["existing"].([]map[string]any); !ok || len(pairs) != 0 {
		t.Fatalf("first existing = %#v, want an empty list", first["existing"])
	}
	second := addFact(t, store, testScope, "老张养了一只狗。", "老张", "养", "狗", "老张养了一只狗", factOptions{})
	pairs, ok := second["existing"].([]map[string]any)
	if !ok || len(pairs) != 1 {
		t.Fatalf("second existing = %#v, want one echo", second["existing"])
	}
	if mapString(pairs[0], "content") == "" {
		t.Fatalf("echo content = %#v", pairs[0])
	}
	if pairs[0]["id"] != first["id"] {
		t.Fatalf("echo id = %v, want the first write id %v", pairs[0]["id"], first["id"])
	}
}

func TestActivationDecaysHourly(t *testing.T) {
	store := newTestStore(t, testScope)
	node, err := store.EnsureNode(testScope, "referent", "老张", NodeOptions{})
	if err != nil {
		t.Fatalf("ensure node: %v", err)
	}
	reference := clock.Unix()
	if _, err := store.conn.Exec(
		`INSERT INTO activation(node_id, scope, score, updated_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT(node_id, scope) DO UPDATE SET
		   score = excluded.score, updated_at = excluded.updated_at`,
		node, testScope, 10.0, reference-7200,
	); err != nil {
		t.Fatalf("seed activation: %v", err)
	}
	acts, err := store.GetActivation(testScope, []string{node}, &reference)
	if err != nil {
		t.Fatalf("get activation: %v", err)
	}
	want := 10.0 * math.Pow(ActivationDecayPerHour, 2)
	if got := acts[node]; math.Abs(got-want) > 5e-4 {
		t.Fatalf("decayed activation = %v, want %v", got, want)
	}
}

func TestBumpActivationAccumulatesDecayedScore(t *testing.T) {
	store := newTestStore(t, testScope)
	node, err := store.EnsureNode(testScope, "referent", "老张", NodeOptions{})
	if err != nil {
		t.Fatalf("ensure node: %v", err)
	}
	start := clock.Unix()
	if count, err := store.BumpActivation(testScope, []string{node, node}, 2.0, &start); err != nil || count != 1 {
		t.Fatalf("first bump = %d err=%v, want 1 (deduplicated)", count, err)
	}
	after := start + 3600
	if count, err := store.BumpActivation(testScope, []string{node}, 3.0, &after); err != nil || count != 1 {
		t.Fatalf("second bump = %d err=%v", count, err)
	}
	acts, err := store.GetActivation(testScope, []string{node}, &after)
	if err != nil {
		t.Fatalf("get activation: %v", err)
	}
	want := 2.0*math.Pow(ActivationDecayPerHour, 1.0) + 3.0
	if got := acts[node]; math.Abs(got-want) > 1e-9 {
		t.Fatalf("accumulated activation = %v, want %v", got, want)
	}
}

func TestRawTracesRespectsAsOf(t *testing.T) {
	store := newTestStore(t, testScope)
	now := clock.Unix()
	future := now + 3600
	if _, _, err := store.AddTrace(testScope, "旧桥边有一只猫。", TraceOptions{
		Session: "s1", Turn: 1, RecordedAt: &future,
	}); err != nil {
		t.Fatalf("add trace: %v", err)
	}
	visible, err := store.RawTraces("旧桥边", RawOptions{
		Scope: testScope, Reader: "deliverable", Limit: 4,
	})
	if err != nil || len(visible) != 1 {
		t.Fatalf("raw hits = %d err=%v, want the trace without as-of", len(visible), err)
	}
	past, err := store.RawTraces("旧桥边", RawOptions{
		Scope: testScope, Reader: "deliverable", Limit: 4, AsOf: now,
	})
	if err != nil {
		t.Fatalf("as-of raw hits: %v", err)
	}
	if len(past) != 0 {
		t.Fatalf("as-of raw hits = %d, want 0 (recorded after as-of)", len(past))
	}
}

func TestRawFTSFailureIsLoudButFailOpen(t *testing.T) {
	store := newTestStore(t, testScope)
	if _, _, err := store.AddTrace(testScope, "旧桥边有一只猫。", TraceOptions{Session: "s1", Turn: 1}); err != nil {
		t.Fatalf("add trace: %v", err)
	}
	if !store.fts {
		t.Skip("fixture database has no FTS tables")
	}
	if _, err := store.conn.Exec("DROP TABLE trace_fts"); err != nil {
		t.Fatalf("drop trace_fts: %v", err)
	}
	hits, err := store.RawTraces("旧桥边", RawOptions{
		Scope: testScope, Reader: "deliverable", Limit: 4,
	})
	if err != nil {
		t.Fatalf("raw traces: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("hits = %d, want 1 (LIKE fallback still delivers)", len(hits))
	}
	rawError := store.RawError()
	if rawError == nil {
		t.Fatal("raw_error = nil, want a failure note")
	}
	if !strings.HasPrefix(*rawError, "trace_fts failed") {
		t.Fatalf("raw_error = %q, want the trace_fts failure note", *rawError)
	}
	store.ClearRawError()
	if store.RawError() != nil {
		t.Fatalf("raw_error = %v after reset, want nil", *store.RawError())
	}
}
