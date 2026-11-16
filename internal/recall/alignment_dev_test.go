// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

// Verification of as-of raw legs, activation ledger + ranking arm (read-chain face),
// and loud raw errors.
package recall_test

import (
	"math"
	"strings"
	"testing"

	"github.com/rocareer/rolling-memory/internal/api"
	"github.com/rocareer/rolling-memory/internal/clock"
	"github.com/rocareer/rolling-memory/internal/recall"
	"github.com/rocareer/rolling-memory/internal/store"
)

func TestRawLegRespectsAsOf(t *testing.T) {
	memory := newMemory(t)
	future := clock.Unix() + 3600
	if _, _, err := memory.Store.AddTrace("mem:test", "旧桥边有一只猫。", store.TraceOptions{
		Session: "s1", Turn: 1, RecordedAt: &future,
	}); err != nil {
		t.Fatalf("add trace: %v", err)
	}
	visible := recallCue(t, memory, "旧桥", true)
	if !anyRawHitContains(visible, "旧桥") {
		t.Fatalf("raw hits = %#v, want the recorded trace", visible.RawHits)
	}
	if result, err := memory.Recall(api.RecallRequest{Cue: "旧桥", IncludeRaw: boolPtr(true), RawLimit: 4, AsOf: clock.Unix()}); err == nil || result != nil {
		t.Fatalf("graph facade must reject unsupported asof filtering: %#v, %v", result, err)
	}
	past, err := recall.Recall(memory.Store, memory.Config, recall.Options{
		Cue: "旧桥", Scope: "mem:test", Reader: "deliverable", IncludeRaw: boolPtr(true), RawLimit: 4, AsOf: clock.Unix(),
	})
	if err != nil {
		t.Fatalf("graph raw recall: %v", err)
	}
	if anyRawHitContains(past, "旧桥") {
		t.Fatalf("as-of raw hits = %#v, want none (recorded after as-of)", past.RawHits)
	}
}

func TestRecallRecordsActivationForDeliveredNodes(t *testing.T) {
	memory := newMemory(t)
	writeFact(t, memory, "老张养了一只猫。", "老张", "养", map[string]any{"label": "猫"}, nil)
	result := recallCue(t, memory, "老张", false)
	dose, ok := result.Meta["activation"].(map[string]any)
	if !ok {
		t.Fatalf("meta.activation = %#v, want a dose map", result.Meta["activation"])
	}
	if recorded, _ := dose["recorded"].(int); recorded <= 0 {
		t.Fatalf("activation.recorded = %#v, want > 0", dose["recorded"])
	}
	if rank := dose["rank"]; rank != false {
		t.Fatalf("activation.rank = %v, want false by default", rank)
	}
	var rows int64
	if err := memory.Store.DB().QueryRow("SELECT count(*) FROM activation").Scan(&rows); err != nil {
		t.Fatalf("count activation: %v", err)
	}
	if rows <= 0 {
		t.Fatalf("activation rows = %d, want > 0", rows)
	}
}

func TestActivationRankArmReordersOnlyWhenEnabled(t *testing.T) {
	memory := newMemory(t)
	writeFact(t, memory, "老张养了一只猫。", "老张", "养", map[string]any{"label": "猫"}, nil)
	writeFact(t, memory, "老李养了一只狗。", "老李", "养", map[string]any{"label": "狗"}, nil)
	subject, found, err := memory.Store.NodeByLabel("mem:test", "老李", "")
	if err != nil || !found {
		t.Fatalf("node_by_label(老李) found=%v err=%v", found, err)
	}
	if _, err := memory.Store.BumpActivation("mem:test", []string{subject.ID}, 50.0, nil); err != nil {
		t.Fatalf("bump activation: %v", err)
	}
	off := recallCue(t, memory, "养", false)
	if title := bandTitle(off.Bands, 0); !strings.Contains(title, "老张") {
		t.Fatalf("off band[0] = %q, want 老张 first", title)
	}
	memory.Config.Recall.ActivationRank = true
	on := recallCue(t, memory, "养", false)
	if title := bandTitle(on.Bands, 0); !strings.Contains(title, "老李") {
		t.Fatalf("on band[0] = %q, want 老李 first", title)
	}
	dose, _ := on.Meta["activation"].(map[string]any)
	if dose == nil || dose["rank"] != true {
		t.Fatalf("activation dose = %#v, want rank=true", on.Meta["activation"])
	}
	// Salience only, never confidence.
	delivered := bandNodeID(on.Bands, 0)
	if delivered == "" {
		t.Fatalf("band[0] = %#v, want a delivered node", on.Bands[0])
	}
	var confidence float64
	if err := memory.Store.DB().QueryRow(
		"SELECT confidence FROM assertion WHERE node_id = ?", delivered,
	).Scan(&confidence); err != nil {
		t.Fatalf("select confidence: %v", err)
	}
	if math.Abs(confidence-0.8) > 1e-3 {
		t.Fatalf("confidence = %v, want 0.8 (activation never writes confidence)", confidence)
	}
}

func TestRawErrorIsLoudButFailOpen(t *testing.T) {
	memory := newMemory(t)
	if _, _, err := memory.Store.AddTrace("mem:test", "旧桥边有一只猫。", store.TraceOptions{
		Session: "s1", Turn: 1,
	}); err != nil {
		t.Fatalf("add trace: %v", err)
	}
	if _, err := memory.Store.DB().Exec("DROP TABLE trace_fts"); err != nil {
		t.Fatalf("drop trace_fts: %v", err)
	}
	result, err := memory.Recall(api.RecallRequest{
		Cue: "旧桥边", IncludeRaw: boolPtr(true), RawLimit: 4,
	})
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	rawError, ok := result.Meta["raw_error"].(string)
	if !ok {
		t.Fatalf("meta.raw_error = %#v, want the failure note", result.Meta["raw_error"])
	}
	if !strings.Contains(rawError, "trace_fts failed") {
		t.Fatalf("raw_error = %q, want the trace_fts failure note", rawError)
	}
	if !anyRawHitContains(result, "旧桥") {
		t.Fatalf("raw hits = %#v, want the LIKE fallback delivery", result.RawHits)
	}
}

// bandTitle returns the title of one band (empty when the band is missing).
func bandTitle(bands []map[string]any, index int) string {
	if index >= len(bands) {
		return ""
	}
	title, _ := bands[index]["title"].(string)
	return title
}

// bandNodeID returns the first delivered node id of one band.
func bandNodeID(bands []map[string]any, index int) string {
	if index >= len(bands) {
		return ""
	}
	nodes, _ := bands[index]["nodes"].([]map[string]any)
	if len(nodes) == 0 {
		return ""
	}
	value, _ := nodes[0]["id"].(string)
	return value
}
