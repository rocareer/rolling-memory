// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package recall

import (
	"strings"
	"testing"
)

// TestRenderRecallOmitsEmptyLabelsAndUnknownTime pins the delivery rendering:
// bands/chains with empty time/space labels carry no empty parens, and nodes
// without a resolved time carry no "unknown" timestamp token.
func TestRenderRecallOmitsEmptyLabelsAndUnknownTime(t *testing.T) {
	result := &Result{
		Scope: "mem:test",
		Bands: []map[string]any{
			{
				"title":       "无标签主题",
				"time_label":  "",
				"space_label": "",
				"chains": []any{
					map[string]any{
						"type":       "read",
						"label":      "链",
						"time_label": "",
						"nodes": []map[string]any{
							{"content": "Q: 问题\nA: 答案", "time": "unknown", "anchor": "trace:abcd#0-1", "confidence": 0.9, "status": "active"},
							{"content": "老张 养 小白", "time": "2026-11-06 12:00", "anchor": "trace:abcd#1-2", "confidence": 0.8, "status": "active"},
						},
					},
				},
			},
			{
				"title":       "部分标签主题",
				"time_label":  "2026-11-06",
				"space_label": "",
				"nodes": []map[string]any{
					{"content": "老张 住在 北京", "time": "2026-11-06 13:00", "anchor": "trace:abcd#2-3", "confidence": 0.7, "status": "active"},
				},
			},
		},
		Meta: map[string]any{"elapsed_ms": 1},
		Gaps: map[string]int{},
	}
	text := renderRecall(result, 100000)
	if strings.Contains(text, "（ · ）") || strings.Contains(text, "（）") {
		t.Fatalf("empty labels rendered as empty parens:\n%s", text)
	}
	if strings.Contains(text, "- unknown Q:") || strings.Contains(text, "- unknown ") {
		t.Fatalf("unknown time token leaked into rendering:\n%s", text)
	}
	if !strings.Contains(text, "无标签主题\n") {
		t.Fatalf("band header lost its title:\n%s", text)
	}
	if !strings.Contains(text, "部分标签主题（2026-11-06）") {
		t.Fatalf("single non-empty label must render without a stray separator:\n%s", text)
	}
	if !strings.Contains(text, "  [read] 链\n") {
		t.Fatalf("chain header must drop its empty parens:\n%s", text)
	}
	if !strings.Contains(text, "- 2026-11-06 12:00 老张 养 小白") {
		t.Fatalf("timed node lost its timestamp:\n%s", text)
	}
}
