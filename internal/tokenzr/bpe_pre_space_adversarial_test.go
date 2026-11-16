// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package tokenzr

import (
	"math"
	"testing"
)

// TestTokenPriorBitsPreSpaceWordBoundaryContract pins the physical BPE contract:
// standalone space-delimited words use their leading-space tokenization, while CJK uses
// bare tokenization. Taking min(bare, spaced) would incorrectly price word-internal fragments.
func TestTokenPriorBitsPreSpaceWordBoundaryContract(t *testing.T) {
	ctr, err := Default()
	if err != nil {
		t.Fatalf("Default: %v", err)
	}
	bits := func(s string) float64 {
		total := 0.0
		for _, id := range ctr.codec.bpePiece(s) {
			total += math.Log2(float64(id) + 2)
		}
		return total
	}
	for _, w := range []string{"ice", "art", "red", "the"} {
		got := TokenPriorBits(w)
		want := bits(" " + w)
		if math.Abs(got-want) > 1e-12 {
			t.Fatalf("TokenPriorBits(%q)=%.12f want standalone spaced encoding %.12f (bare fragment=%.12f)", w, got, want, bits(w))
		}
	}
	for _, w := range []string{"中", "食べる"} {
		got := TokenPriorBits(w)
		want := bits(w)
		if math.Abs(got-want) > 1e-12 {
			t.Fatalf("TokenPriorBits(%q)=%.12f want bare CJK encoding %.12f", w, got, want)
		}
	}
}

func TestSplitSubwordConstituentsBPEBinaryTree(t *testing.T) {
	leaves, phrases := SplitSubwordConstituents("不知道怎么切这个系统字段")
	foundZhiDao := false
	for _, lf := range leaves {
		if lf == "知道" {
			foundZhiDao = true
		}
	}
	if !foundZhiDao {
		t.Fatalf("expected SplitSubwordConstituents to unwrap BPE constituent '知道', got leaves=%v phrases=%v", leaves, phrases)
	}
}

func BenchmarkTokenPriorBits_InVocabAndOOV(b *testing.B) {
	_, _ = Default()
	words := []string{"memory", "the", "sweden", "epinephrine", "地貌形态", "chronotrigger99"}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = TokenPriorBits(words[i%len(words)])
	}
}

func BenchmarkBPEInflectionValence_ZeroAllocLUT(b *testing.B) {
	_, _ = Default()
	words := []string{"work", "move", "the", "where", "study", "hotel"}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = BPEInflectionValence(words[i%len(words)])
	}
}

func BenchmarkIsBPEProperNounPrior_ZeroAllocLUT(b *testing.B) {
	_, _ = Default()
	words := []string{"caroline", "sweden", "where", "memory", "paris", "with"}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = IsBPEProperNounPrior(words[i%len(words)])
	}
}

func BenchmarkAtomInitialEnergyBits_ZeroAlloc(b *testing.B) {
	_, _ = Default()
	words := []string{"caroline", "sweden", "the", "memory", "拓扑结构", "的"}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = AtomInitialEnergyBits(words[i%len(words)])
	}
}

func BenchmarkSplitSubwordConstituents_PrecomputedStrings(b *testing.B) {
	_, _ = Default()
	sample := "他在哈尔滨研究数据库和莫斯科历史，不知道怎么切这个系统字段"
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = SplitSubwordConstituents(sample)
	}
}
