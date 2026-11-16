// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

// Package en provides the opt-in English companion word segmenter (standard SplitWords + 128k BPE)
// and full harmonic pulse packer (Shannon/BPE prior + stem/morph + unit/stat + ISO 3166 geo + calendar).
package en

import (
	"github.com/rocareer/rolling-memory/internal/langpack/multi"
	ramesdk "github.com/rocareer/rolling-memory/sdk/go"
)

// SegmentSentence segments English text combining canonical surface words (`ramesdk.SplitWords`)
// and 128k BPE subword constituents (`multi.SegmentSentence`).
func SegmentSentence(sentence string) []string {
	return multi.SegmentSentence(sentence)
}

// PackSentence applies English BPE/Shannon/lexeme harmonics, Common Core + English unit harmonics,
// ISO 3166 L0-L3 geographic harmonics, and ISO-to-month calendar alignment.
func PackSentence(enc *ramesdk.BlindEncoder, sentence string, sentIdx int) []ramesdk.BlindTokenAtom {
	return multi.PackSentenceWithUnitLookup(enc, sentence, sentIdx, SegmentSentence, true, multi.LookupUnitAndStatHarmonics)
}
