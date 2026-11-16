// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

// Package ja provides the opt-in Japanese companion word segmenter (Kanji / Katakana /
// Hiragana script-phase transitions + 128k BPE morpheme segmentation) and pulse packer.
package ja

import (
	"strings"
	"unicode"

	"github.com/rocareer/rolling-memory/internal/langpack/multi"
	"github.com/rocareer/rolling-memory/internal/tokenzr"
	ramesdk "github.com/rocareer/rolling-memory/sdk/go"
)

func scriptKindJA(r rune) int {
	switch {
	case (r >= 0x4E00 && r <= 0x9FFF) || (r >= 0x3400 && r <= 0x4DBF) || (r >= 0xF900 && r <= 0xFAFF):
		return 1 // Kanji (Han)
	case (r >= 0x30A0 && r <= 0x30FF) || (r >= 0xFF66 && r <= 0xFF9F) || r == 0x30FC:
		return 2 // Katakana
	case r >= 0x3040 && r <= 0x309F:
		return 3 // Hiragana
	case unicode.IsNumber(r):
		return 4
	case unicode.IsLetter(r):
		return 5
	default:
		return 0
	}
}

// SegmentSentence segments Japanese sentences by separating Kanji stems, Katakana loanword
// compounds, and Hiragana inflectional runs along Unicode script-phase boundaries, enriched
// with 128k BPE morphemes (`tokenzr.SplitSubwords`) and canonical fallback tokens.
func SegmentSentence(sentence string) []string {
	if strings.TrimSpace(sentence) == "" {
		return nil
	}
	var phaseRuns []string
	var cur strings.Builder
	prevKind := 0
	for _, r := range sentence {
		k := scriptKindJA(r)
		if k == 0 {
			if cur.Len() > 0 {
				phaseRuns = append(phaseRuns, cur.String())
				cur.Reset()
			}
			prevKind = 0
			continue
		}
		if prevKind != 0 && k != prevKind && cur.Len() > 0 {
			phaseRuns = append(phaseRuns, cur.String())
			cur.Reset()
		}
		cur.WriteRune(r)
		prevKind = k
	}
	if cur.Len() > 0 {
		phaseRuns = append(phaseRuns, cur.String())
	}

	bpeSubs := tokenzr.SplitSubwords(sentence)
	base := ramesdk.SplitWords(sentence)

	out := make([]string, 0, len(phaseRuns)+len(bpeSubs)+len(base))
	seen := make(map[string]bool, len(phaseRuns)+len(bpeSubs))
	addUnique := func(tok string) {
		t := strings.TrimSpace(tok)
		if t == "" {
			return
		}
		var cw uint32
		for _, r := range t {
			cw += uint32(ramesdk.CarrierUnits(r))
		}
		if cw < 42 {
			return
		}
		low := strings.ToLower(t)
		if !seen[low] {
			seen[low] = true
			out = append(out, t)
		}
	}

	for _, pr := range phaseRuns {
		addUnique(pr)
	}
	for _, sub := range bpeSubs {
		addUnique(sub)
	}
	out = append(out, base...)
	return out
}

// PackSentence applies the Japanese companion segmenter and BPE harmonic packer.
func PackSentence(enc *ramesdk.BlindEncoder, sentence string, sentIdx int) []ramesdk.BlindTokenAtom {
	return multi.PackSentenceWithSegmenter(enc, sentence, sentIdx, SegmentSentence, false)
}
