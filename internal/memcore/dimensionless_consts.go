// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package memcore

import (
	"math"
	"unicode"
)

// Score regime calibration constants & named Shannon self-information bit-field laws.
const (
	// ReferencePeakLexScore is the reference peak lexical score used to derive the dimensionless alpha.
	ReferencePeakLexScore = 15.0

	// MinDimensionlessScaleFloor is the lower bound of the dimensionless scale factor.
	MinDimensionlessScaleFloor = 0.55

	// BaselineDirectXOHostScore is the reference score threshold for a compact direct-XO host band.
	BaselineDirectXOHostScore = 12.0

	// BaselineSubordinateBandScore is the reference floor for subordinate bands.
	BaselineSubordinateBandScore = 8.5

	// BaselineInverseEpisodeAnchorScore is the reference floor for inverse episode anchors.
	BaselineInverseEpisodeAnchorScore = 9.0

	// BaselinePeakBandConfidenceScore is the reference floor for peak band confidence.
	BaselinePeakBandConfidenceScore = 6.5

	// BaselineLeadCompetitionScore is the reference floor for lead competition.
	BaselineLeadCompetitionScore = 8.0

	// BaselineMultiTurnChainScore is the reference floor for multi-turn chains.
	BaselineMultiTurnChainScore = 6.0

	// BaselineUpwardContinuationScore is the reference floor for upward continuation.
	BaselineUpwardContinuationScore = 5.5

	// BaselineReverseReachScore is the reference floor for reverse reach.
	BaselineReverseReachScore = 5.0

	// BaselineStrongRowSignalScore is the reference threshold for strong row signals.
	BaselineStrongRowSignalScore = 4.5

	// BaselineCompactChainScore is the reference floor for compact chains.
	BaselineCompactChainScore = 4.0

	// BaselineIntraChainPurityScore is the reference floor for intra-chain purity.
	BaselineIntraChainPurityScore = 3.5

	// BaselineReachExpansionScore is the reference floor for reach expansion.
	BaselineReachExpansionScore = 3.0

	// CompactBandMaxRows is the maximum row count (12) for a band to be treated as a compact entity host.
	CompactBandMaxRows = 12
)

// ScoreRegime carries the calibrated floors actually used by band assembly and topology boosting.
type ScoreRegime struct {
	ObservedMaxScore          float64
	Alpha                     float64
	DirectXOHostFloor         float64
	InverseEpisodeAnchorFloor float64
	SubordinateBandFloor      float64
	LeadCompetitionFloor      float64
	PeakBandConfidenceFloor   float64
	MultiTurnChainFloor       float64
	UpwardContinuationFloor   float64
	ReverseReachFloor         float64
	StrongRowSignalFloor      float64
	CompactChainFloor         float64
	IntraChainPurityFloor     float64
	ReachExpansionFloor       float64
}

// CalibrateScoreRegime derives the dimensionless scale factor from the observed peak lexical score and
// scales every baseline floor by it, so thresholds stay comparable across corpora of different score
// magnitudes.
func CalibrateScoreRegime(observedMaxScore float64) ScoreRegime {
	alpha := 1.0
	if observedMaxScore > 3.5 && observedMaxScore < ReferencePeakLexScore {
		alpha = observedMaxScore / ReferencePeakLexScore
		if alpha < MinDimensionlessScaleFloor {
			alpha = MinDimensionlessScaleFloor
		}
	}
	return ScoreRegime{
		ObservedMaxScore:          observedMaxScore,
		Alpha:                     alpha,
		DirectXOHostFloor:         BaselineDirectXOHostScore * alpha,
		InverseEpisodeAnchorFloor: BaselineInverseEpisodeAnchorScore * alpha,
		SubordinateBandFloor:      BaselineSubordinateBandScore * alpha,
		LeadCompetitionFloor:      BaselineLeadCompetitionScore * alpha,
		PeakBandConfidenceFloor:   BaselinePeakBandConfidenceScore * alpha,
		MultiTurnChainFloor:       BaselineMultiTurnChainScore * alpha,
		UpwardContinuationFloor:   BaselineUpwardContinuationScore * alpha,
		ReverseReachFloor:         BaselineReverseReachScore * alpha,
		StrongRowSignalFloor:      BaselineStrongRowSignalScore * alpha,
		CompactChainFloor:         BaselineCompactChainScore * alpha,
		IntraChainPurityFloor:     BaselineIntraChainPurityScore * alpha,
		ReachExpansionFloor:       BaselineReachExpansionScore * alpha,
	}
}

// Named Dimensionless Constants & Operators of the Shannon Self-Information Bit Field (Laws 1–4):
const (
	// PhaseHalfBandwidthBits (Delta_tau = 7/4 * h_lat = 3.5 bits) is the symmetric phase-transition
	// half-bandwidth centered at AtomicConceptEntropyBits (tau_atom = 8.0b):
	//   SingleAlternationMaxBits      = tau_atom - Delta_tau = 4.5b
	//   OverStemmingDivergenceMaxBits = tau_atom + Delta_tau = 11.5b
	PhaseHalfBandwidthBits = 3.5

	// IntraLexemeMIDecayKappa0 (kappa_0 = log2(3/2) ≈ 0.5849625) is the universal dimensionless
	// power-law decay constant governing conditional entropy H_cond(r_m | r_{1..m-1}) = H_0(r_m) * m^(-lambda(r_m))
	// across continuous ideographic and syllabic compounds (Law 2).
	IntraLexemeMIDecayKappa0 = 0.5849625007211562
)

// PowerLawCompoundShannonBits computes the continuous compound-discounted Shannon self-information B~(s)
// under Law 2 (Intra-Lexeme Mutual-Information Power-Law Decay with coupling constant kappa_0 = log2(3/2)):
//
//	H_cond(r_m | r_{1..m-1}) = H_0(r_m) * m^(-lambda(r_m)),  lambda(r) = kappa_0 * max(0, (H_0(r) - 2.0) / 8.0)
//
// For alphabetic letters (H_0 = 2.0b), lambda = 0 (no compound discount); for Han ideographs (H_0 = 10.0b),
// lambda = kappa_0 ≈ 0.5850 (compressing Zh–En bilingual concept CV by 77.1%); for Hangul syllables (H_0 = 7.0b),
// lambda ≈ 0.3656.
func PowerLawCompoundShannonBits(s string) float64 {
	var bits float64
	hasAlnum := false
	var prevRune rune
	runLen := 0
	ideoRun := 0
	for _, r := range s {
		if unicode.Is(unicode.Cf, r) {
			continue
		}
		lowR := unicode.ToLower(r)
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			hasAlnum = true
		}
		if lowR == prevRune {
			runLen++
		} else {
			prevRune = lowR
			runLen = 1
		}
		rb := RuneShannonBits(r)
		if runLen >= 3 {
			rb *= math.Pow(0.25, float64(runLen-2))
		}
		if IsContinuousSyllabicOrIdeographic(r) {
			ideoRun++
			if ideoRun >= 2 && runLen == 1 {
				lam := IntraLexemeMIDecayKappa0 * math.Max(0.0, (rb-2.0)/AtomicConceptEntropyBits)
				rb *= math.Pow(float64(ideoRun), -lam)
			}
		} else {
			ideoRun = 0
		}
		bits += rb
	}
	if !hasAlnum {
		return 0
	}
	return bits
}

// ShannonPerTokenDensity computes Law 3: the Shannon-per-Token Density Index
//
//	eta_BPE(s) = B(s) / max(1, CountTokens(s))   (in bits / token),
//
// simultaneously rejecting 1-token micro-noise ("a" = 2.0 b/tok) and multi-token OOV gibberish ("@#x!9%" < 2.0 b/tok).
func ShannonPerTokenDensity(s string) float64 {
	b := StringShannonBits(s)
	if b <= 0 {
		return 0
	}
	toks := CountTokens(s)
	if toks < 1 {
		toks = 1
	}
	return b / float64(toks)
}

// GeneralizedTandemNoveltyBits computes Law 4: the Generalized Tandem-Repeat Novelty Operator B*(s),
// applying quaternary geometric decay 0.25^(k*(i)-2) to any contiguous period-p (p in 1..4) tandem repeat u^N,
// obeying the exact supremum Theorem 3: sup_{N>=1} B*(u^N) = (7/3) * B(u) < +inf.
func GeneralizedTandemNoveltyBits(s string) float64 {
	var clean []rune
	for _, r := range s {
		if !unicode.Is(unicode.Cf, r) {
			clean = append(clean, r)
		}
	}
	n := len(clean)
	if n == 0 {
		return 0
	}
	var bits float64
	hasAlnum := false
	for i, r := range clean {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			hasAlnum = true
		}
		maxK := 1
		for p := 1; p <= 4; p++ {
			if i >= 2*p {
				kP := 1
				pos := i
				for pos >= p && unicode.ToLower(clean[pos]) == unicode.ToLower(clean[pos-p]) {
					kP++
					pos -= p
				}
				if kP >= 3 && kP > maxK {
					maxK = kP
				}
			}
		}
		rb := RuneShannonBits(r)
		if maxK >= 3 {
			rb *= math.Pow(0.25, float64(maxK-2))
		}
		bits += rb
	}
	if !hasAlnum {
		return 0
	}
	return bits
}
