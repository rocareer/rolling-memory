// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package store

import (
	"fmt"
	"strings"
	"testing"
)

func TestPendingTracesAndWatermarks(t *testing.T) {
	store := newTestStore(t, testScope)
	hashA, _, _ := store.AddTrace(testScope, "老张养了一只猫。", TraceOptions{Session: "s1", Turn: 1, Author: "user"})
	hashB, _, _ := store.AddTrace(testScope, "老张养了一只狗。", TraceOptions{Session: "s2", Turn: 0, Author: "user"})
	pending, err := store.PendingTraces(testScope, 60, nil)
	if err != nil || len(pending) != 2 {
		t.Fatalf("pending = %v err=%v", pending, err)
	}
	if pending[0].Hash != hashB || pending[1].Hash != hashA {
		t.Fatalf("pending order = %v", pending)
	}
	session := "s1"
	onlyS1, err := store.PendingTraces(testScope, 60, &session)
	if err != nil || len(onlyS1) != 1 || onlyS1[0].Hash != hashA {
		t.Fatalf("session pending = %v err=%v", onlyS1, err)
	}
	if err := store.MarkProcessed(hashA); err != nil {
		t.Fatalf("mark: %v", err)
	}
	pending, err = store.PendingTraces(testScope, 60, nil)
	if err != nil || len(pending) != 1 || pending[0].Hash != hashB {
		t.Fatalf("pending after mark = %v err=%v", pending, err)
	}

	if err := store.UpdateReplayState(testScope, 10, hashA); err != nil {
		t.Fatalf("replay 10: %v", err)
	}
	if err := store.UpdateReplayState(testScope, 5, hashB); err != nil {
		t.Fatalf("replay 5: %v", err)
	}
	row, err := queryOneMap(store.conn,
		"SELECT produced_through, last_trace_hash FROM replay_state WHERE scope = ?", testScope)
	if err != nil {
		t.Fatalf("read replay: %v", err)
	}
	if mapInt64(row, "produced_through") != 10 || mapString(row, "last_trace_hash") != hashA {
		t.Fatalf("replay row = %v", row)
	}
	if err := store.UpdateReplayState(testScope, 20, hashB); err != nil {
		t.Fatalf("replay 20: %v", err)
	}
	row, _ = queryOneMap(store.conn,
		"SELECT produced_through, last_trace_hash FROM replay_state WHERE scope = ?", testScope)
	if mapInt64(row, "produced_through") != 20 || mapString(row, "last_trace_hash") != hashB {
		t.Fatalf("replay row = %v", row)
	}

	if err := store.UpdateSessionCursor(testScope, "s1", 3); err != nil {
		t.Fatalf("cursor 3: %v", err)
	}
	if err := store.UpdateSessionCursor(testScope, "s1", 1); err != nil {
		t.Fatalf("cursor 1: %v", err)
	}
	cursor, err := queryOneMap(store.conn,
		"SELECT turn_done FROM session_cursor WHERE scope = ? AND session = ?", testScope, "s1")
	if err != nil || mapInt64(cursor, "turn_done") != 3 {
		t.Fatalf("cursor = %v err=%v", cursor, err)
	}
	if err := store.UpdateSessionCursor(testScope, "s1", 5); err != nil {
		t.Fatalf("cursor 5: %v", err)
	}
	cursor, _ = queryOneMap(store.conn,
		"SELECT turn_done FROM session_cursor WHERE scope = ? AND session = ?", testScope, "s1")
	if mapInt64(cursor, "turn_done") != 5 {
		t.Fatalf("cursor = %v", cursor)
	}
}

func TestPendingVaultTracesSnapshotAndAudience(t *testing.T) {
	store := newTestStore(t, testScope)
	rows, err := store.PendingVaultTraces(testScope, nil)
	if err != nil || len(rows) != 0 {
		t.Fatalf("empty snapshot = %v err=%v", rows, err)
	}
	var privateHash, unknownHash string
	for i := 0; i < 150; i++ {
		audience := "shared"
		if i%3 == 0 {
			audience = "private"
		} else if i%3 == 1 {
			audience = "public"
		}
		hash, inserted, err := store.AddTrace(testScope, fmt.Sprintf("trace %d", i), TraceOptions{Session: "snapshot", Turn: i, Audience: audience})
		if err != nil || !inserted {
			t.Fatalf("add trace %d: inserted=%v err=%v", i, inserted, err)
		}
		if i == 0 {
			privateHash = hash
		}
		if i == 3 {
			unknownHash = hash
		}
	}
	if _, err := store.conn.Exec("PRAGMA ignore_check_constraints = ON"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.conn.Exec("UPDATE trace SET audience = 'unknown' WHERE hash = ?", unknownHash); err != nil {
		t.Fatal(err)
	}
	if _, err := store.conn.Exec("PRAGMA ignore_check_constraints = OFF"); err != nil {
		t.Fatal(err)
	}
	rows, err = store.PendingVaultTraces(testScope, nil)
	if err != nil || len(rows) != 100 {
		t.Fatalf("snapshot count = %d err=%v", len(rows), err)
	}
	for _, row := range rows {
		if row.Audience != "shared" && row.Audience != "public" {
			t.Fatalf("unexpected audience: %q", row.Audience)
		}
	}
	if _, inserted, err := store.AddTrace(testScope, "later trace", TraceOptions{Session: "later", Audience: "shared"}); err != nil || !inserted {
		t.Fatalf("add later trace: inserted=%v err=%v", inserted, err)
	}
	if _, inserted, err := store.AddTrace("mem:other", "other scope trace", TraceOptions{Session: "later", Audience: "shared"}); err != nil || !inserted {
		t.Fatalf("add other scope trace: inserted=%v err=%v", inserted, err)
	}
	if len(rows) != 100 {
		t.Fatalf("snapshot changed after insert: %d", len(rows))
	}
	later := "later"
	laterRows, err := store.PendingVaultTraces(testScope, &later)
	if err != nil || len(laterRows) != 1 {
		t.Fatalf("later session = %v err=%v", laterRows, err)
	}
	allRows, err := store.PendingVaultTraces(testScope, nil)
	if err != nil || len(allRows) != 101 {
		t.Fatalf("next snapshot = %d err=%v", len(allRows), err)
	}
	if err := store.MarkProcessed(laterRows[0].Hash); err != nil {
		t.Fatal(err)
	}
	laterRows, err = store.PendingVaultTraces(testScope, &later)
	if err != nil || len(laterRows) != 0 {
		t.Fatalf("processed session = %v err=%v", laterRows, err)
	}
	for _, hash := range []string{privateHash, unknownHash} {
		tr, found, err := store.Trace(hash)
		if err != nil || !found || tr.Processed != 0 {
			t.Fatalf("restricted trace %s changed: %#v found=%v err=%v", hash, tr, found, err)
		}
	}
	if _, err := store.conn.Exec("ALTER TABLE trace RENAME TO trace_unavailable"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PendingVaultTraces(testScope, nil); err == nil {
		t.Fatal("missing trace table should return an error")
	}
}

func TestAddTraceNormalization(t *testing.T) {
	store := newTestStore(t, testScope)
	if _, _, err := store.AddTrace(testScope, "   ", TraceOptions{Author: "user"}); err == nil {
		t.Fatal("expected empty text error")
	}
	if _, _, err := store.AddTrace(testScope, "x", TraceOptions{Author: "user", Audience: "world"}); err == nil {
		t.Fatal("expected audience error")
	}
	first, _, err := store.AddTrace(testScope, "老张\x00养猫", TraceOptions{Author: "user", Turn: "2"})
	if err != nil {
		t.Fatalf("add trace: %v", err)
	}
	second, _, err := store.AddTrace(testScope, "老张养猫", TraceOptions{Author: "user", Turn: 2})
	if err != nil {
		t.Fatalf("add normalized trace: %v", err)
	}
	if first == second {
		t.Fatalf("NUL-cleaned text should differ from space-collapsed text")
	}
	trace, ok, err := store.Trace(first)
	if err != nil || !ok {
		t.Fatalf("trace = %v ok=%v err=%v", trace, ok, err)
	}
	if trace.Text != "老张 养猫" || trace.Turn != 2 || trace.Session != "default" || trace.Kind != "message" {
		t.Fatalf("trace row = %v", trace)
	}
	if trace.MetaJSON != "{}" {
		t.Fatalf("meta_json = %q", trace.MetaJSON)
	}
	invalidTurn, _, err := store.AddTrace(testScope, "另一个句子。", TraceOptions{Author: "user", Turn: "not-a-number"})
	if err != nil {
		t.Fatalf("add invalid turn: %v", err)
	}
	trace, _, _ = store.Trace(invalidTurn)
	if trace.Turn != 0 {
		t.Fatalf("invalid turn = %d, want 0", trace.Turn)
	}
	meta, _, err := store.AddTrace(testScope, "带元数据的句子。", TraceOptions{
		Author: "user", Meta: map[string]any{"channel": "测试", "n": 2},
	})
	if err != nil {
		t.Fatalf("add meta trace: %v", err)
	}
	trace, _, _ = store.Trace(meta)
	if !strings.Contains(trace.MetaJSON, "测试") || !strings.Contains(trace.MetaJSON, `"n": 2`) {
		t.Fatalf("meta_json = %q", trace.MetaJSON)
	}
}

func TestEnsureNodeBackfillAndRevival(t *testing.T) {
	store := newTestStore(t, testScope)
	world := "w1"
	nodeID, err := store.EnsureNode(testScope, "referent", "老张", NodeOptions{World: &world})
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	world2 := "w2"
	carrier := "c1"
	locus := "l1"
	sameID, err := store.EnsureNode(testScope, "referent", "老张", NodeOptions{
		World: &world2, Carrier: &carrier, Locus: &locus,
	})
	if err != nil || sameID != nodeID {
		t.Fatalf("second ensure = %q err=%v", sameID, err)
	}
	node, _, _ := store.Node(nodeID)
	if node.World == nil || *node.World != "w1" {
		t.Fatalf("world overwritten: %v", node.World)
	}
	if node.Carrier == nil || *node.Carrier != "c1" || node.Locus == nil || *node.Locus != "l1" {
		t.Fatalf("carrier/locus not backfilled: %v/%v", node.Carrier, node.Locus)
	}

	drop, _ := store.EnsureNode(testScope, "referent", "张先生", NodeOptions{})
	if _, err := store.MergeNodes(nodeID, drop, "user"); err != nil {
		t.Fatalf("merge: %v", err)
	}
	// Merge writes the dropped label as an alias, so label lookup resolves to
	// the kept node rather than reviving the merged one.
	viaAlias, err := store.EnsureNode(testScope, "referent", "张先生", NodeOptions{})
	if err != nil || viaAlias != nodeID {
		t.Fatalf("alias resolve = %q err=%v", viaAlias, err)
	}

	retired, _ := store.EnsureNode(testScope, "referent", "临时", NodeOptions{})
	if _, err := store.conn.Exec(
		"UPDATE node SET status = 'merged', retired_at = ? WHERE id = ?", testNow, retired,
	); err != nil {
		t.Fatalf("retire: %v", err)
	}
	revived, err := store.EnsureNode(testScope, "referent", "临时", NodeOptions{})
	if err != nil || revived != retired {
		t.Fatalf("revive = %q err=%v", revived, err)
	}
	row, _, _ := store.Node(retired)
	if row.Status != "provisional" || row.RetiredAt != nil {
		t.Fatalf("revived node = %v", row)
	}
	if _, err := store.EnsureNode(testScope, "nonsense", "x", NodeOptions{}); err == nil {
		t.Fatal("expected invalid kind error")
	}
	if _, err := store.EnsureNode(testScope, "referent", "  ", NodeOptions{}); err == nil {
		t.Fatal("expected empty label error")
	}
}

func TestRetractAssertionIsAuditedOnce(t *testing.T) {
	store := newTestStore(t, testScope)
	receipt := addFact(t, store, testScope, "老张养了一只猫。", "老张", "养", "猫", "老张养了一只猫", factOptions{})
	summary, err := store.RetractAssertion(receipt["id"].(string), "no longer true", "user")
	if err != nil {
		t.Fatalf("retract: %v", err)
	}
	if summary["status"] != "retracted" {
		t.Fatalf("summary = %v", summary)
	}
	before := countRows(t, store, "event")
	if _, err := store.RetractAssertion(receipt["id"].(string), "again", "user"); err != nil {
		t.Fatalf("second retract: %v", err)
	}
	if countRows(t, store, "event") != before {
		t.Fatalf("second retract wrote another event")
	}
	if _, err := store.RetractAssertion("a_missing", "", "user"); err == nil {
		t.Fatal("expected missing assertion error")
	}
	note := queryText(t, store,
		"SELECT note FROM event WHERE op = 'assert.retract' ORDER BY id DESC LIMIT 1")
	if note != "no longer true" {
		t.Fatalf("retract note = %q", note)
	}
}

func TestStatusDefaultsToConfigScope(t *testing.T) {
	store := newTestStore(t, testScope)
	status, err := store.Status("")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status["scope"] != testScope {
		t.Fatalf("scope = %v", status["scope"])
	}
}
