// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

// Package sea provides the opt-in Southeast Asian unspaced-script companion word segmenter
// (Thai `th`, Lao `lo`, Khmer `km`) and pulse packer, decomposing unspaced abugida runs into
// 128k BPE lexical words andorthographic syllable clusters to prevent runaway glued carriers.
package sea

import (
	"strings"
	"unicode"

	"github.com/rocareer/rolling-memory/internal/langpack/multi"
	"github.com/rocareer/rolling-memory/internal/tokenzr"
	ramesdk "github.com/rocareer/rolling-memory/sdk/go"
)

func isSEAsianRune(r rune) bool {
	return (r >= 0x0E00 && r <= 0x0EFF) || (r >= 0x1780 && r <= 0x17FF)
}

func isPreposedVowelSEA(r rune) bool {
	return (r >= 0x0E40 && r <= 0x0E44) || (r >= 0x0EC0 && r <= 0x0EC4) || (r >= 0x17C1 && r <= 0x17C5)
}

// splitSEASyllableClusters segments a contiguous Thai/Lao/Khmer run into orthographic
// syllable clusters (keeping preposed vowels and combining tone/vowel marks attached to their
// base consonant) and emits overlapping 2-cluster bigrams when BPE leaves an unsegmented run.
func splitSEASyllableClusters(run string) []string {
	var clusters []string
	var cur strings.Builder
	baseSeen := false
	for _, r := range run {
		if isPreposedVowelSEA(r) {
			if baseSeen && cur.Len() > 0 {
				clusters = append(clusters, cur.String())
				cur.Reset()
				baseSeen = false
			}
			cur.WriteRune(r)
			continue
		}
		if unicode.IsMark(r) || (r >= 0x0E30 && r <= 0x0E3A) || (r >= 0x0E47 && r <= 0x0E4E) ||
			(r >= 0x0EB0 && r <= 0x0EBD) || (r >= 0x0EC8 && r <= 0x0ECD) || (r >= 0x17B6 && r <= 0x17D3) {
			cur.WriteRune(r)
			continue
		}
		if baseSeen && cur.Len() > 0 {
			clusters = append(clusters, cur.String())
			cur.Reset()
		}
		cur.WriteRune(r)
		baseSeen = true
	}
	if cur.Len() > 0 {
		clusters = append(clusters, cur.String())
	}
	if len(clusters) < 2 {
		return clusters
	}
	out := make([]string, 0, len(clusters)*2)
	for i := 0; i+1 < len(clusters); i++ {
		out = append(out, clusters[i]+clusters[i+1])
	}
	return out
}

// SegmentSentence segments Thai/Lao/Khmer and mixed text into 128k BPE subwords and
// syllable-cluster bigrams, eliminating `COARSE_UNSEGMENTED_CARRIER` on unspaced SEA scripts.
func SegmentSentence(sentence string) []string {
	if strings.TrimSpace(sentence) == "" {
		return nil
	}
	bpeSubs := tokenzr.SplitSubwords(sentence)
	base := ramesdk.SplitWords(sentence)

	out := make([]string, 0, len(bpeSubs)+len(base)*2)
	seen := make(map[string]bool, len(bpeSubs)+len(base))
	addTok := func(tok string) {
		t := strings.TrimSpace(tok)
		if t == "" {
			return
		}
		var cw uint32
		hasAlnum := false
		for _, r := range t {
			if unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.IsMark(r) {
				hasAlnum = true
			}
			cw += uint32(ramesdk.CarrierUnits(r))
		}
		if !hasAlnum || cw < 40 {
			return
		}
		low := strings.ToLower(t)
		if !seen[low] {
			seen[low] = true
			out = append(out, t)
		}
	}

	for _, sub := range bpeSubs {
		var subW uint32
		hasSEA := false
		for _, r := range sub {
			if isSEAsianRune(r) {
				hasSEA = true
			}
			subW += uint32(ramesdk.CarrierUnits(r))
		}
		if hasSEA && subW > 280 {
			for _, cl := range splitSEASyllableClusters(sub) {
				addTok(cl)
			}
		} else {
			addTok(sub)
		}
	}
	for _, w := range base {
		hasSEA := false
		var cw uint32
		for _, r := range w {
			if isSEAsianRune(r) {
				hasSEA = true
			}
			cw += uint32(ramesdk.CarrierUnits(r))
		}
		if hasSEA && cw > 240 {
			for _, cl := range splitSEASyllableClusters(w) {
				addTok(cl)
			}
		} else {
			addTok(w)
		}
	}
	for _, unitWord := range multi.ExtractSEAsianUnitWords(sentence) {
		addTok(unitWord)
	}
	return out
}

// PackSentence applies the SEA companion segmenter and BPE harmonic packer.
func PackSentence(enc *ramesdk.BlindEncoder, sentence string, sentIdx int) []ramesdk.BlindTokenAtom {
	return multi.PackSentenceWithSegmenter(enc, sentence, sentIdx, SegmentSentence, false)
}
