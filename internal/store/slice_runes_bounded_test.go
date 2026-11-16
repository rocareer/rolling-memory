// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package store

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestSliceRunesBoundedMatchesRuneSlicing pins the exact semantics of the bounded rune-range
// extractor against the `[]rune(text)[start:end]` idiom it replaced inside the candidate
// scoring hot path (anchored trace texts reach hundreds of KB, so the full rune copy had to go).
func TestSliceRunesBoundedMatchesRuneSlicing(t *testing.T) {
	cases := []struct {
		name       string
		value      string
		start, end int
	}{
		{name: "ascii inner span", value: "abcdef", start: 1, end: 3},
		{name: "ascii full span", value: "abcdef", start: 0, end: 6},
		{name: "ascii tail span", value: "abcdef", start: 4, end: 6},
		{name: "strict end beyond rune count", value: "abcdef", start: 0, end: 7},
		{name: "start beyond rune count", value: "abcdef", start: 6, end: 7},
		{name: "empty span", value: "abcdef", start: 3, end: 3},
		{name: "reversed span", value: "abcdef", start: 4, end: 2},
		{name: "negative start", value: "abcdef", start: -1, end: 3},
		{name: "empty value", value: "", start: 0, end: 1},
		{name: "cjk multi-byte", value: "老张养了一只猫。", start: 2, end: 5},
		{name: "cjk full", value: "老张养了一只猫。", start: 0, end: 8},
		{name: "mixed emoji astral", value: "a😀b猫c", start: 1, end: 4},
		{name: "mixed astral tail", value: "a😀b猫c", start: 4, end: 5},
		{name: "astral beyond end", value: "a😀b猫c", start: 4, end: 9},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want := ""
			runes := []rune(tc.value)
			if tc.start >= 0 && tc.end <= len(runes) && tc.end > tc.start {
				want = string(runes[tc.start:tc.end])
			}
			got := sliceRunesBounded(tc.value, tc.start, tc.end)
			if got != want {
				t.Fatalf("sliceRunesBounded(%q, %d, %d) = %q, want %q (inline idiom)", tc.value, tc.start, tc.end, got, want)
			}
			if !strings.Contains(tc.value, got) && got != "" {
				t.Fatalf("extracted span %q is not a substring of %q", got, tc.value)
			}
		})
	}
}

// TestSliceRunesBoundedLargeTextIsBounded guards the deliberate divergence from sliceRunes:
// an out-of-range end must yield "" (strict inline semantics) rather than clamping, and the
// extractor must not copy the whole text into a rune slice on the hot recall path.
func TestSliceRunesBoundedLargeTextIsBounded(t *testing.T) {
	big := strings.Repeat("甲", 200000) + "tail-anchor" + strings.Repeat("乙", 200000)
	start := 200000
	end := start + utf8.RuneCountInString("tail-anchor")
	got := sliceRunesBounded(big, start, end)
	if got != "tail-anchor" {
		t.Fatalf("anchor span = %q, want %q", got, "tail-anchor")
	}
	if clamped := sliceRunes(big, start, len(big)*4); clamped == "" {
		t.Fatalf("sliceRunes clamps while sliceRunesBounded must stay strict; fixture no longer distinguishes the two")
	}
	if strict := sliceRunesBounded(big, start, len(big)*4); strict != "" {
		t.Fatalf("out-of-range end must stay strict (empty), got %q", strict)
	}
}
