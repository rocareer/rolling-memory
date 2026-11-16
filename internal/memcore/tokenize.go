// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package memcore

import (
	"sync/atomic"

	"github.com/rocareer/rolling-memory/internal/text"
)

// LegacyStemMode toggles legacy stem truncation for A/B falsification tests (default false).
// 本包仅保留该开关（rank.go::MatchTokenOrStem 读取）；分词 / 茎干实现唯一正本在 internal/text。
var LegacyStemMode atomic.Bool

// MaxQueryTokens is the hard cap on tokens per query (delegates to internal/text).
const MaxQueryTokens = text.MaxQueryTokens

// IsContinuousSyllabicOrIdeographic reports whether r belongs to a script without word spaces (delegates to internal/text).
func IsContinuousSyllabicOrIdeographic(r rune) bool {
	return text.IsContinuousSyllabicOrIdeographic(r)
}

// IsPureCJK reports whether s consists solely of Han/Hiragana/Katakana characters (delegates to internal/text).
func IsPureCJK(s string) bool { return text.IsPureCJK(s) }

// TokenInformationMass computes the composite Shannon + BPE prior information gain (delegates to internal/text).
func TokenInformationMass(tk string) float64 { return text.TokenInformationMass(tk) }

// IsHighGainToken reports whether tk belongs to the Primary Gain Tier (delegates to internal/text).
func IsHighGainToken(tk string) bool { return text.IsHighGainToken(tk) }

// StemToken computes the language-agnostic Shannon-bit proportional prefix stem (delegates to internal/text).
func StemToken(tok string) string { return text.StemToken(tok) }

// MorphRootPrefix computes the second-order relaxed morphological root prefix (delegates to internal/text).
func MorphRootPrefix(tok string) string { return text.MorphRootPrefix(tok) }

// Tokenize performs permissive zero-stopword-table Unicode + tokenz-r BPE tokenization (delegates to internal/text).
func Tokenize(q string) []string { return text.Tokenize(q) }

// AtomInitialEnergyNats computes the intrinsic initial prior energy E_0(a) (in Nats) of an atom
// determined jointly by its Shannon Unicode script capacity and its 128k BPE Pre-Space Zipfian prior (delegates to internal/text).
func AtomInitialEnergyNats(tk string) float64 { return text.AtomInitialEnergyNats(tk) }

// PureShannonStructuralMass computes 100% BPE-free structural self-information mass (delegates to internal/text).
func PureShannonStructuralMass(tk string) float64 { return text.PureShannonStructuralMass(tk) }

// IsPureShannonHighGainToken reports Primary Gain Tier membership without BPE priors (delegates to internal/text).
func IsPureShannonHighGainToken(tk string) bool { return text.IsPureShannonHighGainToken(tk) }

// OnlineDFConjugateBits computes Bayesian cold-to-warm online DF conjugate self-information bits (delegates to internal/text).
func OnlineDFConjugateBits(bChar, coldBits float64, df, totalDocs int) float64 {
	return text.OnlineDFConjugateBits(bChar, coldBits, df, totalDocs)
}
