// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package recall

import (
	"strings"
	"testing"
)

// TestRenderEpisodicTurnBandKeepsConversationShape pins the delivery format of an
// assembled turn: one header carrying provenance, then every speaker segment on its
// own line. The per-line metadata suffix used for graph nodes ("锚 … · conf …")
// would flatten the conversation back into a wall, so it must not appear here.
func TestRenderEpisodicTurnBandKeepsConversationShape(t *testing.T) {
	result := &Result{
		Scope: "mem:test",
		Bands: []map[string]any{{
			"id":      "@ref:jsonl:/tmp/a.jsonl:0",
			"kind":    "episodic_turn_band",
			"title":   "轮·0",
			"subject": "@ref:jsonl:/tmp/a.jsonl:0",
			"score":   12.5,
			"nodes": []map[string]any{{
				"id":             "ep_abc",
				"anchor":         "ep_abc",
				"content":        "[user] 请检查缓存失效。\n[assistant] 先确认断言。\n[assistant] 结论：缓存按 docID 失效。",
				"confidence":     0.94,
				"segment_window": []int{0, 2},
				"turn_segments":  3,
			}},
		}},
		// The same content is delivered by the band; the raw section must stay silent.
		RawHits: []map[string]any{{
			"session": "@ref:jsonl:/tmp/a.jsonl:0", "turn": 1,
			"text": "[user] 请检查缓存失效。", "score": 12.5, "in_band": true,
		}},
	}
	out := result.Render(4000)
	lines := strings.Split(out, "\n")
	if !strings.Contains(lines[1], "轮·0（段 0–2 · 3 段）") {
		t.Fatalf("turn header = %q", lines[1])
	}
	// 头部不带强度数字：该数字是「本组内相对强度」（榜首恒为 1.00），放进逐条渲染会被读成权威。
	if strings.Contains(lines[1], "conf") {
		t.Fatalf("turn header must not carry a strength number: %q", lines[1])
	}
	want := []string{
		"  [user] 请检查缓存失效。",
		"  [assistant] 先确认断言。",
		"  [assistant] 结论：缓存按 docID 失效。",
	}
	for index, expected := range want {
		if lines[2+index] != expected {
			t.Fatalf("segment line %d = %q, want %q", index, lines[2+index], expected)
		}
	}
	if strings.Contains(out, "锚 ep_abc") {
		t.Fatalf("turn band must not carry the per-line anchor suffix: %s", out)
	}
	if strings.Contains(out, "原文命中") {
		t.Fatalf("a hit already delivered as a band must not be repeated: %s", out)
	}
}

// TestRenderUndeliveredRawHitsStayVisible pins the other side of the contract: a raw
// hit that no band delivered is still printed, so nothing silently disappears.
func TestRenderUndeliveredRawHitsStayVisible(t *testing.T) {
	result := &Result{
		Scope: "mem:test",
		RawHits: []map[string]any{{
			"session": "s1", "turn": 2, "occurred_at": int64(1793966400), "text": "未入图的原文命中。", "score": 1.0,
		}},
	}
	out := result.Render(2000)
	if !strings.Contains(out, "原文命中（未入图）:") || !strings.Contains(out, "未入图的原文命中。") {
		t.Fatalf("undelivered raw hit vanished: %s", out)
	}
	if !strings.Contains(out, "[s1:2] 2026-") {
		t.Fatalf("expected date prefix on undelivered raw hit with occurred_at > 0, got: %s", out)
	}
}

// TestRenderEpisodicTurnBandCarriesDateFirst pins the provenance header order when
// the source turn has a timestamp: the local date comes first inside the parens
// ("轮·0（2026-11-11 · Rolling-Memory · 段 0–2 · 3 段）") and is never fabricated when 0.
func TestRenderEpisodicTurnBandCarriesDateFirst(t *testing.T) {
	result := &Result{
		Scope: "mem:test",
		Bands: []map[string]any{{
			"id":         "ep_abc",
			"kind":       "episodic_turn_band",
			"title":      "轮·ep_abc",
			"subject":    "ep_abc",
			"time_label": "2026-11-11",
			"score":      12.5,
			"nodes": []map[string]any{{
				"id":             "ep_abc",
				"locus":          "Rolling-Memory",
				"time":           "2026-11-11",
				"content":        "[user] 我们什么时候讨论的？\n[assistant] 2026-11-11。",
				"segment_window": []int{0, 1},
				"turn_segments":  2,
			}},
		}},
	}
	out := result.Render(2000)
	lines := strings.Split(out, "\n")
	wantHeader := "轮·ep_abc（2026-11-11 · Rolling-Memory · 段 0–1 · 2 段）"
	if lines[1] != wantHeader {
		t.Fatalf("turn header = %q, want %q", lines[1], wantHeader)
	}
}

// TestRenderTruncatesOnLineBoundary pins the budget cut: when the block outgrows the
// char budget it must stop between speaker lines, never inside one.
func TestRenderTruncatesOnLineBoundary(t *testing.T) {
	long := strings.Repeat("这一句会占用较多交付预算。", 20)
	result := &Result{
		Scope: "mem:test",
		Bands: []map[string]any{
			{
				"title": "轮·0", "kind": "episodic_turn_band",
				"nodes": []map[string]any{{
					"content":        "[user] " + long + "\n[assistant] " + long + "\n[assistant] 结论。",
					"confidence":     0.9,
					"segment_window": []int{0, 2},
					"turn_segments":  3,
				}},
			},
			{
				"title": "轮·1", "kind": "episodic_turn_band",
				"nodes": []map[string]any{{
					"content":        "[user] 后续轮次不应出现在截断块里。",
					"confidence":     0.5,
					"segment_window": []int{0, 0},
					"turn_segments":  1,
				}},
			},
		},
	}
	out := result.Render(260)
	if !strings.Contains(out, "…（已截断）") {
		t.Fatalf("expected a truncation marker: %q", out)
	}
	body := out[strings.Index(out, ">")+1:]
	body = body[:strings.LastIndex(body, "</rolling-memory-recall>")]
	for _, line := range strings.Split(strings.TrimSpace(body), "\n") {
		if strings.HasPrefix(line, "  [") && !strings.HasSuffix(line, "。") && !strings.HasSuffix(line, "）") {
			t.Fatalf("truncation cut inside a speaker line: %q (block %q)", line, out)
		}
	}
}
