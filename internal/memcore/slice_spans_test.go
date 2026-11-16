// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package memcore

import (
	"strings"
	"testing"
)

// TestSplitSentenceSpans_VerbatimLadderAndCoverage 验证读侧候选窗的四个不变量：
// 逐字（每窗都是原文连续子串）、有序不重叠、Token 计数自洽、单窗 ≤ 目标阶梯。
func TestSplitSentenceSpans_VerbatimLadderAndCoverage(t *testing.T) {
	cases := []string{
		"Maria moved to Lisbon in 2019. She opened a ceramics studio there. Two years later the studio burned down.",
		"张伟在杭州开了一家咖啡馆。他每天六点起床磨豆子。第三年他把店搬到了西湖边。",
		"first line without terminator\nsecond line\nthird line",
	}
	for _, text := range cases {
		spans := SplitSentenceSpans(text, TokenTier256)
		if len(spans) == 0 {
			t.Fatalf("expected spans for %q", text)
		}
		lastEnd := 0
		for _, sp := range spans {
			if sp.Start < 0 || sp.End > len(text) || sp.Start >= sp.End {
				t.Fatalf("bad span %+v for %q", sp, text)
			}
			if sp.Start < lastEnd {
				t.Fatalf("span out of order / overlapping: %+v (lastEnd=%d)", sp, lastEnd)
			}
			lastEnd = sp.End
			sub := text[sp.Start:sp.End]
			if strings.TrimSpace(sub) == "" {
				t.Fatalf("blank span %+v", sp)
			}
			if !strings.Contains(text, sub) {
				t.Fatalf("span is not a verbatim substring: %q", sub)
			}
			if got := CountTokens(sub); got != sp.Tokens {
				t.Fatalf("token count mismatch: span=%d recomputed=%d text=%q", sp.Tokens, got, sub)
			}
			if sp.Tokens > TokenTier256 {
				t.Fatalf("span exceeds target tier %d: %+v", TokenTier256, sp)
			}
		}
	}
}

// TestSplitSentenceSpans_OversizedSentenceHardSplit 无句界超长文本必须按阶梯硬切，
// 且每片仍是逐字子串（防止「切出非原文」）。
func TestSplitSentenceSpans_OversizedSentenceHardSplit(t *testing.T) {
	long := strings.Repeat("alpha beta gamma delta ", 200)
	spans := SplitSentenceSpans(long, TokenTier256)
	if len(spans) < 2 {
		t.Fatalf("expected oversized text to be split, got %d span(s)", len(spans))
	}
	for _, sp := range spans {
		if sp.Tokens > TokenTier256 {
			t.Fatalf("span exceeds target tier: %+v", sp)
		}
		if !strings.Contains(long, long[sp.Start:sp.End]) {
			t.Fatalf("span is not a verbatim substring: %+v", sp)
		}
	}
}

// TestSplitSentenceSpans_EmptyAndDefaultTier 空文本返回 nil；预算 ≤0 时落到 TokenTier256 阶梯。
func TestSplitSentenceSpans_EmptyAndDefaultTier(t *testing.T) {
	if got := SplitSentenceSpans("", 0); got != nil {
		t.Fatalf("expected nil for empty text, got %+v", got)
	}
	if got := SplitSentenceSpans("   \n\t ", 0); got != nil {
		t.Fatalf("expected nil for whitespace text, got %+v", got)
	}
	long := strings.Repeat("alpha beta gamma delta ", 200)
	for _, sp := range SplitSentenceSpans(long, 0) {
		if sp.Tokens > TokenTier256 {
			t.Fatalf("default tier must be TokenTier256, got span %+v", sp)
		}
	}
}
