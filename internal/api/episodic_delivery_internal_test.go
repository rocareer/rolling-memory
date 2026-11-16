// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package api

import (
	"strings"
	"testing"
)

// TestRenderEpisodicWindow pins the window contract: the hit segment is always
// delivered, neighbours extend the window in reading order while the char budget
// allows, and the window never spans segments outside the turn.
func TestRenderEpisodicWindow(t *testing.T) {
	segments := []string{
		"[user] 请检查缓存失效。",
		"[assistant] 先确认断言。",
		"[assistant] 结论：缓存按 docID 失效。",
		"[assistant] 复跑全部包全绿。",
	}
	text, low, high := renderEpisodicWindow(segments, 2, 200)
	if low != 0 || high != 3 {
		t.Fatalf("expected the whole small turn, got window [%d,%d] text=%q", low, high, text)
	}
	if strings.Count(text, "\n") != 3 {
		t.Fatalf("window must join segments in reading order: %q", text)
	}
	if !strings.Contains(text, "[user] 请检查缓存失效。") || !strings.Contains(text, "结论：缓存按 docID 失效。") {
		t.Fatalf("window lost a speaker line: %q", text)
	}

	// A tight budget keeps the hit segment alone instead of slicing it.
	text, low, high = renderEpisodicWindow(segments, 2, 1)
	if low != 2 || high != 2 || text != segments[2] {
		t.Fatalf("tight budget must keep the hit segment whole, got [%d,%d] %q", low, high, text)
	}

	// Out-of-range hit indices clamp instead of panicking (and still window around 3).
	if text, low, high = renderEpisodicWindow(segments, 99, 100); high != 3 || low > 3 || !strings.Contains(text, segments[3]) {
		t.Fatalf("clamp failed: [%d,%d] %q", low, high, text)
	}
	if text, _, _ = renderEpisodicWindow(nil, 0, 100); text != "" {
		t.Fatalf("empty turn must render empty, got %q", text)
	}
}

// TestSplitTurnRefSegment pins pointer parsing: `#<k>` is the segment index, and a
// bare turn reference keeps segment 0.
func TestSplitTurnRefSegment(t *testing.T) {
	cases := []struct {
		pointer string
		ref     string
		index   int
	}{
		{"@ref:zcode:s1:m1#3", "@ref:zcode:s1:m1", 3},
		{"@ref:jsonl:/tmp/a.jsonl:2#0", "@ref:jsonl:/tmp/a.jsonl:2", 0},
		{"@ref:jsonl:/tmp/a.jsonl:2", "@ref:jsonl:/tmp/a.jsonl:2", 0},
		{"@ref:jsonl:/tmp/a#b.jsonl:2#7", "@ref:jsonl:/tmp/a#b.jsonl:2", 7},
		{"", "", 0},
	}
	for _, tc := range cases {
		ref, index := splitTurnRefSegment(tc.pointer)
		if ref != tc.ref || index != tc.index {
			t.Errorf("splitTurnRefSegment(%q) = (%q,%d), want (%q,%d)", tc.pointer, ref, index, tc.ref, tc.index)
		}
	}
}

// TestTurnWindowBudget pins the derived budget: it shrinks as more turns compete,
// and a zero total budget never invents one.
func TestTurnWindowBudget(t *testing.T) {
	if got := turnWindowBudget(0, 3); got != 0 {
		t.Fatalf("zero budget must stay zero, got %d", got)
	}
	if got := turnWindowBudget(900, 1); got != 900 {
		t.Fatalf("single turn keeps the whole budget, got %d", got)
	}
	if got := turnWindowBudget(900, 3); got != 300 {
		t.Fatalf("three turns split the budget, got %d", got)
	}
}

// TestBestTurnSegmentIndexSelectsQueryMatchingAssistantSegment proves that when a
// legacy turn-level pointer lacks a `#<k>` suffix, focal segment selection picks the
// assistant segment matching the query rather than defaulting to `[user] 继续` (0).
func TestBestTurnSegmentIndexSelectsQueryMatchingAssistantSegment(t *testing.T) {
	segments := []string{
		"[user] 那就修好网络瓶颈",
		"[assistant] 先检查端口监听状态。",
		"[assistant] 根因定位：cmd/brain-server/main.go 的 WriteTimeout 从 120s 提升至 600s。",
	}
	if got := bestTurnSegmentIndex("@ref:zcode:s1:m1", segments, "WriteTimeout 600s", 0); got != 2 {
		t.Fatalf("bare turn pointer focal segment = %d, want 2", got)
	}
	if got := bestTurnSegmentIndex("@ref:zcode:s1:m1#1", segments, "WriteTimeout 600s", 1); got != 1 {
		t.Fatalf("explicit #1 segment pointer focal segment = %d, want 1", got)
	}
}
