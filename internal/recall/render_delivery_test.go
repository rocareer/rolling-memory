// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package recall

import (
	"strings"
	"testing"
)

// TestRenderDeliveryBandHeaders pins the delivery headers: internal ladder
// addresses never surface, the retired "带·" taxonomy is not replayed to the
// reader, the space label (or the node's workspace locus) takes over, and
// legacy/noetic titles keep their own label with the space label in the parens.
func TestRenderDeliveryBandHeaders(t *testing.T) {
	result := &Result{
		Scope: "mem:test",
		Bands: []map[string]any{
			{
				"kind":        "cell_band",
				"title":       "带·C6:L3:B001/C6:L2:B116/C6:L1:B977#00",
				"space_label": "design/xxx.md#L117-118",
				"nodes":       []map[string]any{{"content": "内容甲", "confidence": 0.89, "status": "active"}},
			},
			{
				"kind":  "cell_band",
				"title": "带·C6:L3:B001/C6:L2:B050/C6:L1:B460#00",
				"nodes": []map[string]any{{"content": "内容乙", "locus": "Rolling-Memory", "confidence": 0.7, "status": "active"}},
			},
			{
				"kind":  "cell_band",
				"title": "带·ast:default:inc:10#42",
				"nodes": []map[string]any{{"content": "内容丙", "confidence": 0.5, "status": "active"}},
			},
			{
				"kind":        "subject",
				"title":       "带·我",
				"time_label":  "2026-Q1",
				"space_label": "project × workspace",
				"nodes":       []map[string]any{{"content": "内容丁", "confidence": 0.8, "status": "active"}},
			},
			{
				// Source-built thin title already carries the space label, and a background
				// chunk whose content starts with the same `[space_label]` line must not
				// repeat the bracketed label twice on the rendered line.
				"kind":        "cell_band",
				"title":       "带·design/yyy.md#L9-10",
				"space_label": "design/yyy.md#L9-10",
				"nodes":       []map[string]any{{"content": "[design/yyy.md#L9-10]\n内容戊", "confidence": 0.6, "status": "active"}},
			},
		},
		Meta: map[string]any{"elapsed_ms": 1},
		Gaps: map[string]int{},
	}
	text := renderRecall(result, 100000)
	for _, want := range []string{
		"  - [design/xxx.md#L117-118] 内容甲",
		"  - [Rolling-Memory] 内容乙",
		"  - [记忆] 内容丙",
		"我（2026-Q1 · project × workspace）",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("render missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "带·") || strings.Contains(text, "记忆带") || strings.Contains(text, "bands=") {
		t.Fatalf("retired band taxonomy leaked into the delivery rendering:\n%s", text)
	}
	if strings.Contains(text, "C6:") || strings.Contains(text, "ast:") {
		t.Fatalf("internal address leaked into the delivery rendering:\n%s", text)
	}
	if got := strings.Count(text, "design/yyy.md#L9-10"); got != 1 {
		t.Fatalf("source label repeated %d times in the header:\n%s", got, text)
	}
}

func TestRenderDeliveryGapsAndTruncation(t *testing.T) {
	result := &Result{
		Scope: "mem:test",
		Gaps:  map[string]int{"index_incomplete": 2, "source_unavailable": 1},
		Meta:  map[string]any{"truncated": map[string]any{"raw": true}},
	}
	rendered := result.Render(10000)
	for _, want := range []string{"情景索引不完整 2", "来源不可回源 1", "预算裁剪"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("missing %q: %s", want, rendered)
		}
	}
}

// TestRenderDeliveryNodeLines pins the node suffix: status only when it is not
// the default, reason/relative kept, opaque anchors dropped, and content verbatim.
func TestRenderDeliveryNodeLines(t *testing.T) {
	line := nodeLine(map[string]any{
		"content":    `if a && b: out << "<T> & 'x'"`,
		"time":       "2026-03-02 12:00",
		"confidence": 0.871,
		"status":     "superseded",
		"reason":     "被取代",
		"anchor":     "trace:abcd#0-1",
	})
	for _, want := range []string{"superseded", "被取代", "2026-03-02 12:00"} {
		if !strings.Contains(line, want) {
			t.Fatalf("node line missing %q: %q", want, line)
		}
	}
	// 即便节点带着强度数字（0.871），渲染也不得输出：它是本组内相对强度，不是可信度。
	if strings.Contains(line, "conf") || strings.Contains(line, "0.87") {
		t.Fatalf("strength number must not be rendered: %q", line)
	}
	if strings.Contains(line, "锚 trace:") || strings.Contains(line, "· active") {
		t.Fatalf("opaque anchor or default status leaked: %q", line)
	}
	if !strings.Contains(line, `a && b: out << "<T> & 'x'"`) {
		t.Fatalf("content must be delivered verbatim: %q", line)
	}
	active := nodeLine(map[string]any{"content": "内容", "confidence": 0.5, "status": "active"})
	if strings.Contains(active, "active") {
		t.Fatalf("default status must be dropped: %q", active)
	}
}

func TestRenderDeliveryMultilineCellBandAndCompactSingleSegmentHeader(t *testing.T) {
	result := &Result{
		Scope: "mem:test",
		Bands: []map[string]any{
			{
				"kind":        "cell_band",
				"title":       "带·Rolling-Memory",
				"space_label": "Rolling-Memory",
				"nodes": []map[string]any{{
					"time":    "2026-11-11 19:00:00 +08:00",
					"content": "首行摘要结论。\n| 列1 | 列2 |\n| --- | --- |\n| 值A | 值B |",
					"status":  "active",
				}},
			},
			{
				"kind":       "episodic_turn_band",
				"title":      "轮·ep_single",
				"time_label": "2026-11-11 19:02:07 +08:00",
				"nodes": []map[string]any{{
					"id":             "ep_single",
					"locus":          "Rolling-Memory",
					"content":        "[assistant] 单段命中输出。",
					"segment_window": []int{28, 28},
					"turn_segments":  1,
				}},
			},
		},
		Meta: map[string]any{"elapsed_ms": 5},
		Gaps: map[string]int{},
	}
	rendered := renderRecall(result, 10000)
	for _, want := range []string{
		"  - [Rolling-Memory] 2026-11-11 19:00:00 +08:00 首行摘要结论。\n    | 列1 | 列2 |\n    | --- | --- |\n    | 值A | 值B |",
		"轮·ep_single（2026-11-11 19:02:07 +08:00 · Rolling-Memory · 段 28）",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered output missing %q:\n%s", want, rendered)
		}
	}
	if strings.Contains(rendered, "段 28–28 · 1 段") {
		t.Fatalf("single-segment turn header should not repeat range and count:\n%s", rendered)
	}
}

func TestRenderDeliveryNoeticBandTurnSubBulletsAndCompactProbeSummary(t *testing.T) {
	longMultiline := "[assistant] 第一行核心结论。\n- 子列表项甲\n- 子列表项乙\n" + strings.Repeat("补充细节说明文字。", 30)
	result := &Result{
		Scope: "mem:test",
		Bands: []map[string]any{
			{
				"kind":       "episodic_turn_band",
				"title":      "轮·ep_turn1",
				"time_label": "2026-11-12 10:00:00 +08:00",
				"nodes": []map[string]any{{
					"id":             "ep_turn1",
					"locus":          "Rolling-Memory",
					"content":        longMultiline,
					"segment_window": []int{1, 1},
					"turn_segments":  1,
				}},
			},
			{
				"kind":        "cell_band",
				"title":       "带·Rolling-Memory",
				"space_label": "Rolling-Memory",
				"nodes": []map[string]any{{
					"id":      "ep_cell1",
					"time":    "2026-11-12 09:00:00 +08:00",
					"content": "[assistant] 独立记忆条目。",
					"status":  "active",
				}},
			},
			{
				"kind":       "noetic_band",
				"title":      "带·recall format",
				"time_label": "2026-11-12 08:00:00 +08:00",
				"nodes": []map[string]any{{
					"id":      "nt_1",
					"time":    "2026-11-12 08:00:00 +08:00",
					"content": "Cue: recall format\nCited units: ep_turn1, ep_cell1",
					"status":  "active",
				}},
			},
		},
		Meta: map[string]any{"elapsed_ms": 3},
		Gaps: map[string]int{},
	}
	rendered := renderRecall(result, 10000)
	for _, want := range []string{
		"  [assistant] 第一行核心结论。\n    - 子列表项甲\n    - 子列表项乙",
		"  - [Rolling-Memory] 2026-11-12 09:00:00 +08:00 [assistant] 独立记忆条目。",
		"  - [noetic] 2026-11-12 08:00:00 +08:00 Cue: recall format\n    Cited units: ep_turn1, ep_cell1",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered output missing %q:\n%s", want, rendered)
		}
	}
	probe := result.ToProbe()
	hits, _ := probe["hits"].([]any)
	if len(hits) == 0 {
		t.Fatalf("expected probe hits, got %#v", probe)
	}
	firstHit := hits[0].(map[string]any)
	summary, _ := firstHit["summary"].(string)
	if strings.Contains(summary, "\n") || len([]rune(summary)) > 161 {
		t.Fatalf("probe summary must be single-line and <=160 runes (+ellipsis), got len=%d %q", len([]rune(summary)), summary)
	}
}
