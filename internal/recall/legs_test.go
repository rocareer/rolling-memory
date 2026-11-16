// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package recall_test

import (
	"strings"
	"testing"

	"github.com/rocareer/rolling-memory/internal/api"
	"github.com/rocareer/rolling-memory/internal/ids"
	"github.com/rocareer/rolling-memory/internal/recall"
	"github.com/rocareer/rolling-memory/internal/store"
)

func deliveredNodes(result *recall.Result) []map[string]any {
	nodes := []map[string]any{}
	for _, band := range result.Bands {
		group, _ := band["nodes"].([]map[string]any)
		nodes = append(nodes, group...)
	}
	return nodes
}

func anyRawHitContains(result *recall.Result, needle string) bool {
	for _, hit := range result.RawHits {
		if text, ok := hit["text"].(string); ok && strings.Contains(text, needle) {
			return true
		}
	}
	return false
}

// TestSubtypeOfClosureNominatesNeighbourRelations verifies subtype-of closure
// expansion during recall.
func TestSubtypeOfClosureNominatesNeighbourRelations(t *testing.T) {
	memory := newMemory(t)
	writeFact(t, memory, "老张养了一只猫。", "老张", "养", map[string]any{"label": "猫"}, nil)
	if !recallCue(t, memory, "拥有", false).Absent {
		t.Fatal("broader relation should not match before the meta edge")
	}
	relation, ok, err := memory.Store.NodeByLabel("mem:test", "养", "relation_type")
	if err != nil || !ok {
		t.Fatalf("relation lookup = %v ok=%v err=%v", relation, ok, err)
	}
	broader, err := memory.Store.EnsureNode("mem:test", "relation_type", "拥有", store.NodeOptions{})
	if err != nil {
		t.Fatalf("ensure broader: %v", err)
	}
	if _, err := memory.Store.AddMetaEdge("mem:test", "subtype-of", relation.ID, broader, "", "user"); err != nil {
		t.Fatalf("meta edge: %v", err)
	}
	hit := recallCue(t, memory, "拥有", false)
	if !strings.Contains(hit.Render(8000), "猫") {
		t.Fatalf("render = %s", hit.Render(8000))
	}
}

// TestInverseOfClosureFindsTheInverseReading verifies inverse-of relation
// closure during recall.
func TestInverseOfClosureFindsTheInverseReading(t *testing.T) {
	memory := newMemory(t)
	writeFact(t, memory, "老张照顾了猫。", "老张", "照顾", map[string]any{"label": "猫"}, nil)
	memory.Config.Recall.Vector = false
	if !recallCue(t, memory, "看护", false).Absent {
		t.Fatal("inverse relation should not match before the meta edge")
	}
	relation, ok, err := memory.Store.NodeByLabel("mem:test", "照顾", "relation_type")
	if err != nil || !ok {
		t.Fatalf("relation lookup = %v ok=%v err=%v", relation, ok, err)
	}
	inverse, err := memory.Store.EnsureNode("mem:test", "relation_type", "看护", store.NodeOptions{})
	if err != nil {
		t.Fatalf("ensure inverse: %v", err)
	}
	if _, err := memory.Store.AddMetaEdge("mem:test", "inverse-of", inverse, relation.ID, "", "user"); err != nil {
		t.Fatalf("meta edge: %v", err)
	}
	hit := recallCue(t, memory, "看护", false)
	if !strings.Contains(hit.Render(8000), "猫") {
		t.Fatalf("render = %s", hit.Render(8000))
	}
}

// TestTokenCorrectionRecoversATypo verifies typo recovery when token correction is enabled.
func TestTokenCorrectionRecoversATypo(t *testing.T) {
	memory := newMemory(t)
	memory.Config.Recall.Vector = false
	writeFact(t, memory, "老张赢得了 tournament。", "老张", "赢得",
		map[string]any{"label": "tournament"}, nil)
	on, err := memory.Recall(api.RecallRequest{
		Cue: "torunament", IncludeRaw: boolPtr(false), TokenCorrect: boolPtr(true),
	})
	if err != nil {
		t.Fatalf("recall on: %v", err)
	}
	legs := on.Meta["legs"].(map[string]int)
	if legs["token_correct"] < 1 {
		t.Fatalf("legs = %#v", legs)
	}
	if !strings.Contains(on.Render(8000), "tournament") {
		t.Fatalf("render = %s", on.Render(8000))
	}
	off, err := memory.Recall(api.RecallRequest{
		Cue: "torunament", IncludeRaw: boolPtr(false), TokenCorrect: boolPtr(false),
	})
	if err != nil {
		t.Fatalf("recall off: %v", err)
	}
	offLegs := off.Meta["legs"].(map[string]int)
	if offLegs["token_correct"] != 0 {
		t.Fatalf("correction leg ran while disabled: %#v", offLegs)
	}
	if !off.Absent {
		t.Fatalf("expected absent when token_correct is off: %#v", off.ToDict())
	}
}

// TestRawLegExpandsWithSeedAliases verifies raw-hit expansion via seed aliases.
func TestRawLegExpandsWithSeedAliases(t *testing.T) {
	memory := newMemory(t)
	writeFact(t, memory, "老张养了一只猫。", "老张", "养", map[string]any{"label": "猫"}, nil)
	node, ok, err := memory.Store.NodeByLabel("mem:test", "老张", "")
	if err != nil || !ok {
		t.Fatalf("node lookup = %v ok=%v err=%v", node, ok, err)
	}
	if _, err := memory.Store.AddAlias(node.ID, "张先生", "manual"); err != nil {
		t.Fatalf("alias: %v", err)
	}
	if _, _, err := memory.Store.AddTrace("mem:test", "张先生今天出门了。",
		store.TraceOptions{Session: "s2", Turn: 1, Author: "user"}); err != nil {
		t.Fatalf("trace: %v", err)
	}
	on, err := memory.Recall(api.RecallRequest{
		Cue: "老张", IncludeRaw: boolPtr(true), RawLimit: 4, AliasExpand: boolPtr(true),
	})
	if err != nil {
		t.Fatalf("recall on: %v", err)
	}
	if !anyRawHitContains(on, "张先生") {
		t.Fatalf("alias-expanded raw hits = %#v", on.RawHits)
	}
	off, err := memory.Recall(api.RecallRequest{
		Cue: "老张", IncludeRaw: boolPtr(true), RawLimit: 4, AliasExpand: boolPtr(false),
	})
	if err != nil {
		t.Fatalf("recall off: %v", err)
	}
	if anyRawHitContains(off, "张先生") {
		t.Fatalf("alias expansion leaked with the flag off: %#v", off.RawHits)
	}
}

// TestRelativeTagAnnotatesAnchorWithTheRowClock verifies relative time tag annotation on delivered nodes.
func TestRelativeTagAnnotatesAnchorWithTheRowClock(t *testing.T) {
	memory := newMemory(t)
	reference := testNow // 2026-10-27T12:00:00Z
	if _, err := memory.Remember(api.RememberRequest{
		Content:    strPtr("老张昨天拜访了医生。"),
		AnchorText: strPtr("老张昨天拜访了医生。"),
		Assertion: map[string]any{
			"s": map[string]any{"label": "老张"}, "x": "拜访", "o": map[string]any{"label": "医生"},
		},
		OccurredAt: reference, Author: "user", AutoSync: boolPtr(false),
	}); err != nil {
		t.Fatalf("remember: %v", err)
	}
	on, err := memory.Recall(api.RecallRequest{
		Cue: "老张", IncludeRaw: boolPtr(false), RelativeTag: boolPtr(true),
	})
	if err != nil {
		t.Fatalf("recall on: %v", err)
	}
	nodes := deliveredNodes(on)
	if len(nodes) == 0 {
		t.Fatalf("on = %#v", on.ToDict())
	}
	relative, ok := nodes[0]["relative"].([]any)
	if !ok || len(relative) == 0 {
		t.Fatalf("relative = %#v", nodes[0]["relative"])
	}
	if item := relative[0].(map[string]any); item["date"] != "2026-10-26" {
		t.Fatalf("relative = %#v", relative)
	}
	if !strings.Contains(on.Render(8000), "相对时点") {
		t.Fatalf("render = %s", on.Render(8000))
	}
	off, err := memory.Recall(api.RecallRequest{
		Cue: "老张", IncludeRaw: boolPtr(false), RelativeTag: boolPtr(false),
	})
	if err != nil {
		t.Fatalf("recall off: %v", err)
	}
	nodesOff := deliveredNodes(off)
	if len(nodesOff) == 0 {
		t.Fatalf("off = %#v", off.ToDict())
	}
	if _, ok := nodesOff[0]["relative"]; ok {
		t.Fatalf("relative tag leaked with the flag off: %#v", nodesOff[0])
	}
}

// TestTimeRefResolvesRelativeCueIntoaSoftWindow verifies relative cue resolution into a date window.
func TestTimeRefResolvesRelativeCueIntoaSoftWindow(t *testing.T) {
	memory := newMemory(t)
	reference := testNow // 2026-10-27T12:00:00Z
	target := ids.ParseTime("昨天", ids.ParseTimeOptions{TimeRef: &reference, Default: 0})
	if target == 0 {
		t.Fatal("parse_time('昨天') returned 0")
	}
	if _, err := memory.Remember(api.RememberRequest{
		Content:    strPtr("老张开了项目会。"),
		AnchorText: strPtr("老张开了项目会。"),
		Assertion: map[string]any{
			"s": map[string]any{"label": "老张"}, "x": "开", "o": map[string]any{"label": "项目会"},
		},
		OccurredAt: target + 3600, Author: "user", AutoSync: boolPtr(false),
	}); err != nil {
		t.Fatalf("remember: %v", err)
	}
	hit, err := memory.Recall(api.RecallRequest{
		Cue: "昨天 老张", IncludeRaw: boolPtr(false), TimeRef: reference,
	})
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	window := hit.Meta["date_window"].(map[string]any)
	if window["derived"] != true {
		t.Fatalf("date_window = %#v", window)
	}
	label, _ := window["label"].(string)
	if !strings.HasPrefix(label, "relative:") {
		t.Fatalf("date_window = %#v", window)
	}
	if boosted, ok := window["boosted"].(int); !ok || boosted < 1 {
		t.Fatalf("date_window = %#v", window)
	}
	plain, err := memory.Recall(api.RecallRequest{Cue: "昨天 老张", IncludeRaw: boolPtr(false)})
	if err != nil {
		t.Fatalf("plain recall: %v", err)
	}
	if plain.Meta["date_window"].(map[string]any)["derived"] != false {
		t.Fatalf("plain date_window = %#v", plain.Meta["date_window"])
	}
}

// TestRawHitsPullTheirDerivedAssertionsBack verifies mention backfill from raw hits to derived assertions.
func TestRawHitsPullTheirDerivedAssertionsBack(t *testing.T) {
	memory := newMemory(t)
	writeFact(t, memory, "老张在旧桥边看见了那只猫。", "老张", "看见", map[string]any{"label": "猫"}, nil)
	result, err := memory.Recall(api.RecallRequest{
		Cue: "旧桥", IncludeRaw: boolPtr(true), RawLimit: 4,
	})
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if len(result.Bands) == 0 {
		t.Fatalf("backfilled bands = %#v", result.ToDict())
	}
	if !strings.Contains(result.Render(8000), "看见") {
		t.Fatalf("render = %s", result.Render(8000))
	}
	backfill := result.Meta["backfill"].(map[string]any)
	if backfill["candidates"].(int) < 1 {
		t.Fatalf("backfill = %#v", backfill)
	}
}

// TestBackfillCanBeDisabled verifies that disabling MentionBackfill suppresses derived assertion backfill.
func TestBackfillCanBeDisabled(t *testing.T) {
	memory := newMemory(t)
	writeFact(t, memory, "老张在旧桥边看见了那只猫。", "老张", "看见", map[string]any{"label": "猫"}, nil)
	result, err := memory.Recall(api.RecallRequest{
		Cue: "旧桥", IncludeRaw: boolPtr(true), RawLimit: 4, MentionBackfill: boolPtr(false),
	})
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if len(result.Bands) != 0 {
		t.Fatalf("bands = %d", len(result.Bands))
	}
	backfill := result.Meta["backfill"].(map[string]any)
	if backfill["candidates"].(int) != 0 {
		t.Fatalf("backfill = %#v", backfill)
	}
}
