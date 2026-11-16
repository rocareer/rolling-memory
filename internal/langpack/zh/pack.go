// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

// Package zh provides the opt-in Chinese companion word segmenter (128k BPE phrase/leaf
// constituents overlaid onto sliding 2-gram and unigram harmonics) and pulse packer.
package zh

import (
	"strings"
	"unicode"

	"github.com/rocareer/rolling-memory/internal/langpack/multi"
	"github.com/rocareer/rolling-memory/internal/tokenzr"
	ramesdk "github.com/rocareer/rolling-memory/sdk/go"
)

// SegmentSentence segments Chinese text by combining 128k BPE multi-character phrases
// and constituent leaves (`tokenzr.SplitSubwordConstituents`) with the canonical sliding
// 2-gram + unigram stream (`ramesdk.SplitWords`), capturing 2/3/4-character compound words
// while preserving 100% partial-match recall.
func SegmentSentence(sentence string) []string {
	if strings.TrimSpace(sentence) == "" {
		return nil
	}
	leaves, phrases := tokenzr.SplitSubwordConstituents(sentence)
	base := ramesdk.SplitWords(sentence)
	out := make([]string, 0, len(phrases)+len(leaves)+len(base))
	seenPhrase := make(map[string]bool, len(phrases)+len(leaves))

	addToken := func(tok string) {
		t := strings.TrimSpace(tok)
		if t == "" {
			return
		}
		var cw uint32
		hasValid := false
		for _, r := range t {
			if unicode.IsLetter(r) || unicode.IsNumber(r) {
				hasValid = true
			}
			cw += uint32(ramesdk.CarrierUnits(r))
		}
		// Multi-character Chinese BPE words (cw >= 200) or alphanumeric words
		if !hasValid || cw < 40 {
			return
		}
		low := strings.ToLower(t)
		if !seenPhrase[low] {
			seenPhrase[low] = true
			out = append(out, t)
		}
	}

	for _, ph := range phrases {
		addToken(ph)
	}
	for _, lf := range leaves {
		addToken(lf)
	}
	out = append(out, base...)
	return out
}

// PackSentence applies the Chinese companion segmenter, Common Core + Chinese unit harmonics, and BPE harmonic packer.
func PackSentence(enc *ramesdk.BlindEncoder, sentence string, sentIdx int) []ramesdk.BlindTokenAtom {
	return multi.PackSentenceWithUnitLookup(enc, sentence, sentIdx, SegmentSentence, false, multi.LookupChineseUnitHarmonics)
}
