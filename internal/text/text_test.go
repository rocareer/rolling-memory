// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package text

import (
	"math"
	"strings"
	"testing"
)

// TestShannonPrimitivesContract pins the canonical Shannon-bit values of the
// single primitive home. memcore now delegates here, so these numbers are the
// repo-wide contract for recall ranking, hook gating, and eval scoring.
func TestShannonPrimitivesContract(t *testing.T) {
	if got := RuneShannonBits('a'); got != 2.0 {
		t.Fatalf("RuneShannonBits('a') = %v, want 2.0", got)
	}
	if got := RuneShannonBits('猫'); got != 10.0 {
		t.Fatalf("RuneShannonBits('猫') = %v, want 10.0 (Han)", got)
	}
	if got := SliceShannonBits([]rune("abc")); got != 6.0 {
		t.Fatalf("SliceShannonBits(abc) = %v, want 6.0", got)
	}
	if got := StringShannonBits("猫"); got != 10.0 {
		t.Fatalf("StringShannonBits(猫) = %v, want 10.0", got)
	}
	// Non-carrier strings contribute zero bits (punctuation-only, no alnum).
	if got := StringShannonBits("----"); got != 0.0 {
		t.Fatalf("StringShannonBits(----) = %v, want 0.0", got)
	}
	// Stutter flood is damped below the atomic concept floor.
	if got := StringShannonBits("aaaaaaaaaa"); got >= AtomicConceptEntropyBits {
		t.Fatalf("StringShannonBits(aaaaaaaaaa) = %v, want < %v", got, AtomicConceptEntropyBits)
	}
	if got := StringShannonBits(""); got != 0.0 {
		t.Fatalf("StringShannonBits(empty) = %v, want 0.0", got)
	}
}

// TestScriptClassifiers locks the language-agnostic script classification used by
// ids/token gating: continuous scripts are never stem-truncated.
func TestScriptClassifiers(t *testing.T) {
	if !IsPureHan("猫") || IsPureHan("cat") || IsPureHan("") {
		t.Fatal("IsPureHan contract broken")
	}
	if !IsPureCJK("食べ") || IsPureCJK("cat") {
		t.Fatal("IsPureCJK contract broken")
	}
	if !IsContinuousSyllabicOrIdeographic('한') || !IsContinuousSyllabicOrIdeographic('猫') {
		t.Fatal("IsContinuousSyllabicOrIdeographic must accept Hangul/Han")
	}
	if IsContinuousSyllabicOrIdeographic('a') {
		t.Fatal("latin letters are not continuous-script carriers")
	}
	// Continuous scripts pass through untouched; spaced scripts take a
	// Shannon-proportional prefix (never a hardcoded suffix rule).
	if got := StemToken("北京大学"); got != "北京大学" {
		t.Fatalf("StemToken(CJK) = %q, want unchanged", got)
	}
	stem := StemToken("running")
	if stem == "" || !strings.HasPrefix("running", stem) || len(stem) >= len("running") {
		t.Fatalf("StemToken(running) = %q, want a proper Shannon-bits prefix", stem)
	}
	root := MorphRootPrefix("running")
	if root == "" || !strings.HasPrefix("running", root) || len(root) > len(stem) {
		t.Fatalf("MorphRootPrefix(running) = %q, want prefix of the stem", root)
	}
}

// TestTokenizeAndSelectionGates covers the permissive tokenizer's invariants:
// empty input yields nothing, real text yields gain-ordered tokens, and the
// top-k selector never truncates below the budget.
func TestTokenizeAndSelectionGates(t *testing.T) {
	if got := Tokenize(""); got != nil {
		t.Fatalf("Tokenize(empty) = %v, want nil", got)
	}
	tokens := Tokenize("猫咪 喜欢 鱼 Resolution@1024")
	if len(tokens) == 0 {
		t.Fatal("Tokenize must keep permissive candidates")
	}
	short := []string{"a", "b", "c"}
	if got := SelectTopInformationTokens(short, 5); len(got) != 3 {
		t.Fatalf("SelectTopInformationTokens passthrough = %v", got)
	}
	if kept := SelectTopInformationTokens(short, 2); len(kept) != 2 {
		t.Fatalf("SelectTopInformationTokens top-2 = %v", kept)
	}
}

// TestCounter pins tokenz-r counting.
func TestCounter(t *testing.T) {
	if CountTokens("") != 0 {
		t.Fatal("CountTokens(empty) must be 0")
	}
	if CountTokens("Rolling-Memory 通用记忆系统") <= 0 {
		t.Fatal("CountTokens must count via tokenz-r")
	}
}

// TestApostropheAndRoleHelpers pins the dialog-normalization helpers.
func TestApostropheAndRoleHelpers(t *testing.T) {
	if got := CanonicalizeApostrophe("it’s `fine`"); got != "it's 'fine'" {
		t.Fatalf("CanonicalizeApostrophe = %q", got)
	}
	if !IsRolePlaceholder("assistant") || !IsRolePlaceholder("USER") || IsRolePlaceholder("Alice") {
		t.Fatal("IsRolePlaceholder contract broken")
	}
}

// TestSpatialAndVectorPrimitives covers the hierarchical path kernel and the
// feature-hash vector used by the recall gates.
func TestSpatialAndVectorPrimitives(t *testing.T) {
	if got := NormalizeHierarchicalPath("a//b/../c"); got != "a/c" {
		t.Fatalf("NormalizeHierarchicalPath = %q, want a/c", got)
	}
	if got := HierarchicalLCPScore("org/repo/pkg", "org/repo/api"); math.Abs(got-4.0/6.0) > 1e-9 {
		t.Fatalf("HierarchicalLCPScore = %v, want 4/6", got)
	}
	vec := FeatureHashVec("hello world", DefaultHashDim)
	if len(vec) != DefaultHashDim {
		t.Fatalf("FeatureHashVec dim = %d, want %d", len(vec), DefaultHashDim)
	}
	if got := DotUnit(vec, vec); math.Abs(got-1.0) > 1e-6 {
		t.Fatalf("DotUnit(v,v) = %v, want 1.0", got)
	}
	empty := PresenceCoordinates{}
	if m := SpacetimeResonanceMultiplier(empty, "", "", ""); m != 1.0 {
		t.Fatalf("empty presence multiplier = %v, want 1.0", m)
	}
	full := PresenceCoordinates{World: "actual", Carrier: "real", Locus: "org/repo", Incarnation: "coding/zcode"}
	if m := SpacetimeResonanceMultiplier(full, "org/repo", "real", "coding/zcode"); m <= 1.0 {
		t.Fatalf("matching presence multiplier = %v, want > 1.0", m)
	}
}
