// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package memcore

import (
	"math"
	"testing"
)

// TestShannonTokenOverlapSimEntropyWeighting verifies the pure mathematical properties of
// ShannonTokenOverlapSim across Latin and continuous CJK scripts:
//  1. High-entropy proper nouns / identifiers strictly outweigh low-entropy common words
//     when both match 1 out of 2 query tokens (eliminating uniform 1/N ties);
//  2. A full query-token match yields exactly 1.0.
func TestShannonTokenOverlapSimEntropyWeighting(t *testing.T) {
	qLatin := []string{"is", "hydrocortisone"}
	simCommon := ShannonTokenOverlapSim("it is here", qLatin)
	simRare := ShannonTokenOverlapSim("apply hydrocortisone cream", qLatin)
	if !(simRare > simCommon) {
		t.Fatalf("high-entropy Latin token must score strictly higher than common token: rare=%.4f common=%.4f", simRare, simCommon)
	}
	if math.Abs(ShannonTokenOverlapSim("it is hydrocortisone", qLatin)-1.0) > 1e-6 {
		t.Fatalf("full token match must yield 1.0, got %.6f", ShannonTokenOverlapSim("it is hydrocortisone", qLatin))
	}

	qCJK := []string{"的", "阿司匹林肠溶片"}
	simCJKCommon := ShannonTokenOverlapSim("这是我的书", qCJK)
	simCJKRare := ShannonTokenOverlapSim("每日服用阿司匹林肠溶片", qCJK)
	if !(simCJKRare > simCJKCommon) {
		t.Fatalf("high-entropy CJK token must score strictly higher than single particle: rare=%.4f common=%.4f", simCJKRare, simCJKCommon)
	}
}
