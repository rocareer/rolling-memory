// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

// Package ko provides the opt-in Korean companion word segmenter (Hangul Eojeol +
// 128k BPE content-stem extraction stripping agglutinative Josa particles) and pulse packer.
package ko

import (
	"strings"
	"unicode"

	"github.com/rocareer/rolling-memory/internal/langpack/multi"
	"github.com/rocareer/rolling-memory/internal/tokenzr"
	ramesdk "github.com/rocareer/rolling-memory/sdk/go"
)

// SegmentSentence segments Korean text into surface Eojeol words (`ramesdk.SplitWords`)
// plus 128k BPE content stems (`tokenzr.SplitSubwords`), ensuring that inflected Hangul
// tokens with attached particles (e.g., 은/는/이/가/을/를/에서) emit both the full Eojeol
// and the bare stem.
func SegmentSentence(sentence string) []string {
	if strings.TrimSpace(sentence) == "" {
		return nil
	}
	base := ramesdk.SplitWords(sentence)
	bpeSubs := tokenzr.SplitSubwords(sentence)

	out := make([]string, 0, len(base)+len(bpeSubs))
	out = append(out, base...)
	seen := make(map[string]bool, len(base)+len(bpeSubs))
	for _, w := range base {
		seen[strings.ToLower(strings.TrimSpace(w))] = true
	}
	for _, sub := range bpeSubs {
		t := strings.TrimSpace(sub)
		if t == "" {
			continue
		}
		var cw uint32
		hasLetter := false
		for _, r := range t {
			if unicode.IsLetter(r) || unicode.IsNumber(r) {
				hasLetter = true
			}
			cw += uint32(ramesdk.CarrierUnits(r))
		}
		if !hasLetter || cw < 70 {
			continue
		}
		low := strings.ToLower(t)
		if !seen[low] {
			seen[low] = true
			out = append(out, t)
		}
	}
	return out
}

// PackSentence applies the Korean companion segmenter and BPE harmonic packer.
func PackSentence(enc *ramesdk.BlindEncoder, sentence string, sentIdx int) []ramesdk.BlindTokenAtom {
	return multi.PackSentenceWithSegmenter(enc, sentence, sentIdx, SegmentSentence, false)
}
