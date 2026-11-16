// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

// Package multi provides the opt-in composite lookup that resolves the Universal Common Core (common)
// and the per-language unit/statistical views for the client-side `langpack` plugin.
//
// Delegates to the canonical client SDK (`ramesdk.LookupUnitDimensionHarmonicPair`) so that
// all 4 language SDKs and the Go `langpack` companion suite share a single source of truth.
package multi

import (
	"strings"

	"github.com/rocareer/rolling-memory/internal/langpack/common"
	ramesdk "github.com/rocareer/rolling-memory/sdk/go"
)

// UnitHarmonicLookup is a per-language unit/statistical harmonic resolver.
type UnitHarmonicLookup func(low string) (canonicalUnit string, dimensionTag string)

// LookupEnglishUnitHarmonics resolves English-specific unit words and quantitative cues.
// The per-language names below stay the opt-in activation surface of each language pack while
// the lexicons themselves live in the single canonical SDK table.
func LookupEnglishUnitHarmonics(low string) (string, string) {
	return ramesdk.LookupUnitDimensionHarmonicPair(low)
}

// LookupChineseUnitHarmonics resolves Chinese-specific measurement and statistical unit words.
// Beyond the shared SDK table it restores the single ambiguous plain-word unit (元) that the
// canonical table deliberately excludes as a cross-language noise guard (the Common Core must
// not tag it), keeping the Chinese pack's original coverage.
func LookupChineseUnitHarmonics(low string) (string, string) {
	if unit, dim := ramesdk.LookupUnitDimensionHarmonicPair(low); unit != "" || dim != "" {
		return unit, dim
	}
	if strings.TrimSpace(low) == "元" {
		return "@unit:curr:cny", "@dim:money"
	}
	return "", ""
}

// LookupJapaneseKoreanGermanUnitHarmonics resolves JA/KO/DE unit surface forms.
func LookupJapaneseKoreanGermanUnitHarmonics(low string) (string, string) {
	return ramesdk.LookupUnitDimensionHarmonicPair(low)
}

// LookupSEAsianUnitHarmonics resolves Thai / Lao / Khmer unit surface forms.
func LookupSEAsianUnitHarmonics(low string) (string, string) {
	return ramesdk.LookupUnitDimensionHarmonicPair(low)
}

// ExtractSEAsianUnitWords scans unspaced SEA text for companion unit words (longest-first).
func ExtractSEAsianUnitWords(sentence string) []string {
	return ramesdk.ExtractSEAsianUnitWords(sentence)
}

// LookupUnitAndStatHarmonics chains Common Core + per-language companion unit views. Every view
// resolves through the same canonical SDK source, so the chain is a single shared pass; the
// explicit per-language links are kept because a language pack may carry a narrow residual that
// the shared table deliberately excludes (e.g. the Chinese 元 currency guard).
func LookupUnitAndStatHarmonics(low string) (canonicalUnit string, dimensionTag string) {
	if u, d := common.LookupCommonUnitAndStatHarmonics(low); u != "" || d != "" {
		return u, d
	}
	if u, d := LookupEnglishUnitHarmonics(low); u != "" || d != "" {
		return u, d
	}
	if u, d := LookupChineseUnitHarmonics(low); u != "" || d != "" {
		return u, d
	}
	if u, d := LookupJapaneseKoreanGermanUnitHarmonics(low); u != "" || d != "" {
		return u, d
	}
	return LookupSEAsianUnitHarmonics(low)
}

// ExtractInlineSymbolUnits delegates to the canonical SDK inline symbol scanner.
func ExtractInlineSymbolUnits(sentence string) []string {
	return ramesdk.ExtractInlineSymbolUnits(sentence)
}
