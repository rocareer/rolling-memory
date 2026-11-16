// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package shaping

import "testing"

func TestShapingPrimitives(t *testing.T) {
	if got := CanonicalizeApostrophe("fancett’s"); got != "fancett's" {
		t.Fatalf("CanonicalizeApostrophe = %q", got)
	}
	if r2, r1 := CollapseElongatedRuns("coooool"); r2 != "cool" || r1 != "col" {
		t.Fatalf("CollapseElongatedRuns(coooool) = (%q, %q)", r2, r1)
	}
	if got := CollapsePeriodicReduplication("hahahaha"); got != "haha" {
		t.Fatalf("CollapsePeriodicReduplication = %q", got)
	}
	if got := FoldLatinDiacritics("zürich"); got != "zurich" {
		t.Fatalf("FoldLatinDiacritics = %q", got)
	}
	if got := FoldLatinDiacritics("phở hà nội șț"); got != "pho ha noi st" {
		t.Fatalf("FoldLatinDiacritics(extended Latin/Vietnamese) = %q, want pho ha noi st", got)
	}
	if got := SplitAlphaLeadingNumericTail("day2"); got != "day" {
		t.Fatalf("SplitAlphaLeadingNumericTail(day2) = %q", got)
	}
	if dates := ExtractStructuredDateHarmonics("Visited on 2023-05-01."); len(dates) != 1 || dates[0] != "2023-05-01" {
		t.Fatalf("ExtractStructuredDateHarmonics = %v", dates)
	}
	if bogus := ExtractStructuredDateHarmonics("Resolution 1920x10x12 hash 2023a05a01"); len(bogus) != 0 {
		t.Fatalf("ExtractStructuredDateHarmonics must reject non-date separators: got %v", bogus)
	}
	if plan := FuseSpaceElision("山 东 今 天"); len(plan.Tokens) == 0 {
		t.Fatalf("FuseSpaceElision returned empty tokens")
	}
}
