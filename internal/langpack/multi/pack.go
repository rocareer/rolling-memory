// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

// Package multi implements the shared opt-in BPE and lexeme-harmonic pulse packer
// and multilingual companion word segmenter.
package multi

import (
	"math"
	"strings"
	"time"
	"unicode"

	"github.com/rocareer/rolling-memory/internal/langpack/common"
	"github.com/rocareer/rolling-memory/internal/text"
	"github.com/rocareer/rolling-memory/internal/tokenzr"
	ramesdk "github.com/rocareer/rolling-memory/sdk/go"
)

// CalendarBeaconWidthX10 marks normalized ISO calendar-day atoms.
const CalendarBeaconWidthX10 uint16 = 588

func isWordConnInMulti(r rune) bool { return r == '-' || r == '_' || r == '.' }

func splitCompoundSubwordsInMulti(tok string) []string {
	var parts []string
	var cur strings.Builder
	var prevR rune
	hasPrev := false
	for _, r := range tok {
		if isWordConnInMulti(r) {
			if cur.Len() > 0 {
				parts = append(parts, cur.String())
				cur.Reset()
			}
			hasPrev = false
			continue
		}
		if hasPrev && cur.Len() > 0 && ((unicode.IsLower(prevR) && unicode.IsUpper(r)) || (unicode.IsNumber(prevR) != unicode.IsNumber(r))) {
			parts = append(parts, cur.String())
			cur.Reset()
		}
		cur.WriteRune(r)
		prevR, hasPrev = r, true
	}
	if cur.Len() > 0 {
		parts = append(parts, cur.String())
	}
	if len(parts) < 2 {
		return nil
	}
	return parts
}

func isBlindNumericOnlyTokenInMulti(tok string) bool {
	hasDigit := false
	for _, r := range tok {
		switch {
		case unicode.IsNumber(r):
			hasDigit = true
		case isWordConnInMulti(r):
		default:
			return false
		}
	}
	return hasDigit
}

func isBlindLetterOnlyTokenInMulti(tok string) bool {
	hasLetter := false
	for _, r := range tok {
		switch {
		case unicode.IsNumber(r):
			return false
		case unicode.IsLetter(r) || unicode.IsMark(r):
			hasLetter = true
		}
	}
	return hasLetter
}

func isBlindDigitLedAlnumTokenInMulti(tok string) bool {
	for _, r := range tok {
		if !unicode.IsNumber(r) {
			return false
		}
		return !isBlindNumericOnlyTokenInMulti(tok)
	}
	return false
}

func computeSentenceFormFlagMarks(line string, words []string) []uint8 {
	if len(words) == 0 {
		return nil
	}
	marks := make([]uint8, len(words))
	for _, r := range line {
		if !unicode.IsSpace(r) {
			if unicode.Is(unicode.Ps, r) || unicode.Is(unicode.Pe, r) || unicode.Is(unicode.Pi, r) || unicode.Is(unicode.Pf, r) {
				marks[0] |= ramesdk.FlagHasBoundaryQuote
			}
			break
		}
	}
	trimmed := strings.TrimSpace(line)
	if trimmed != "" {
		nHash := 0
		for _, r := range trimmed {
			if r != '#' {
				break
			}
			nHash++
			if nHash > 6 {
				nHash = 0
				break
			}
		}
		isFocal := nHash > 0 && (trimmed[nHash:] == "" || !strings.HasPrefix(trimmed[nHash:], "#"))
		if !isFocal {
			colon, colonLen := -1, 0
			for idx, r := range trimmed {
				if r == ':' || r == '：' {
					colon, colonLen = idx, len(string(r))
					break
				}
			}
			if colon > 0 {
				validPrefix := true
				for _, r := range trimmed[:colon] {
					if unicode.IsSpace(r) || r == '\n' || r == '\r' || r == '.' || r == '。' || r == '！' || r == '？' || r == '；' || r == ';' || r == '!' || r == '?' {
						validPrefix = false
						break
					}
				}
				if validPrefix {
					rest := strings.TrimLeftFunc(trimmed[colon+colonLen:], unicode.IsSpace)
					hasDigit := false
					for _, r := range rest {
						if unicode.IsNumber(r) {
							hasDigit = true
						} else if !isWordConnInMulti(r) {
							break
						}
					}
					isFocal = hasDigit
				}
			}
		}
		if isFocal {
			marks[0] |= ramesdk.FlagIsFocalHeader
		}
	}
	parents := make([]int, len(words))
	for i := range parents {
		parents[i] = -1
	}
	for i := 0; i < len(words); i++ {
		if parents[i] >= 0 {
			continue
		}
		subs := splitCompoundSubwordsInMulti(words[i])
		if len(subs) < 2 {
			continue
		}
		for k := 1; k <= len(subs) && i+k < len(words); k++ {
			if words[i+k] != subs[k-1] {
				break
			}
			parents[i+k] = i
		}
	}
	runes := make([]rune, 0, len(line))
	offsets := make([]int, 0, len(line))
	for i, r := range line {
		runes = append(runes, r)
		offsets = append(offsets, i)
	}
	var carrierStarts []int
	for i := 0; i < len(runes); {
		if !unicode.IsNumber(runes[i]) && !isWordConnInMulti(runes[i]) {
			i++
			continue
		}
		hasDigit := false
		j := i
		for j < len(runes) {
			if unicode.IsNumber(runes[j]) {
				hasDigit = true
				j++
				continue
			}
			if isWordConnInMulti(runes[j]) {
				j++
				continue
			}
			break
		}
		if hasDigit {
			k := j
			for k < len(runes) && unicode.IsSpace(runes[k]) {
				k++
			}
			if k < len(runes) && (unicode.IsLetter(runes[k]) || unicode.IsMark(runes[k])) {
				carrierStarts = append(carrierStarts, offsets[k])
			}
		}
		i = j
	}
	for i := 1; i < len(words); i++ {
		if !isBlindNumericOnlyTokenInMulti(words[i-1]) || !isBlindLetterOnlyTokenInMulti(words[i]) {
			continue
		}
		carrierOK := true
		if cp := parents[i]; cp >= 0 {
			carrierOK = cp == parents[i-1] && isBlindDigitLedAlnumTokenInMulti(words[cp])
		} else if ap := parents[i-1]; ap >= 0 {
			carrierOK = isBlindNumericOnlyTokenInMulti(words[ap])
		}
		if !carrierOK {
			continue
		}
		adj := false
		for _, s := range carrierStarts {
			if s <= len(line) && strings.HasPrefix(line[s:], words[i]) {
				adj = true
				break
			}
		}
		if adj {
			marks[i] |= ramesdk.FlagIsQuantityOrUnit
		}
	}
	return marks
}

func scriptKindJAInMulti(r rune) int {
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

// SegmentSentence segments a multilingual sentence by combining the universal language-agnostic
// surface tokens (`ramesdk.SplitWords`) with Japanese script-phase boundaries (Kanji/Katakana/Hiragana)
// and 128k BPE subword constituents (`tokenzr.SplitSubwords` and `tokenzr.SplitSubwordConstituents`),
// decomposing unspaced runs (CJK / Thai / Lao / Khmer) and long compounds without dropping any canonical surface token.
func SegmentSentence(sentence string) []string {
	if strings.TrimSpace(sentence) == "" {
		return nil
	}
	baseWords := ramesdk.SplitWords(sentence)
	leaves, phrases := tokenzr.SplitSubwordConstituents(sentence)
	subwords := tokenzr.SplitSubwords(sentence)

	var phaseRuns []string
	var cur strings.Builder
	prevKind := 0
	for _, r := range sentence {
		k := scriptKindJAInMulti(r)
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

	out := make([]string, 0, len(baseWords)+len(phaseRuns)+len(phrases)+len(leaves))
	seen := make(map[string]bool, len(baseWords)*2)
	appendClean := func(tok string, allowRepeat bool) {
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
		if !hasAlnum || cw < 20 {
			return
		}
		low := strings.ToLower(t)
		if !allowRepeat && seen[low] {
			return
		}
		seen[low] = true
		out = append(out, t)
	}

	for _, w := range baseWords {
		appendClean(w, true)
	}
	for _, pr := range phaseRuns {
		appendClean(pr, false)
	}
	for _, ph := range phrases {
		appendClean(ph, false)
	}
	for _, lf := range leaves {
		appendClean(lf, false)
	}
	for _, sw := range subwords {
		appendClean(sw, false)
	}
	return out
}

// PackSentence applies BPE/Shannon/lexeme harmonics, unit/geo harmonic tables, and ISO date alignment.
func PackSentence(enc *ramesdk.BlindEncoder, sentence string, sentIdx int) []ramesdk.BlindTokenAtom {
	return PackSentenceWithSegmenter(enc, sentence, sentIdx, SegmentSentence, true)
}

// PackSentenceWithEnglishDates additionally aligns ISO dates with English month names.
func PackSentenceWithEnglishDates(enc *ramesdk.BlindEncoder, sentence string, sentIdx int) []ramesdk.BlindTokenAtom {
	return PackSentenceWithSegmenter(enc, sentence, sentIdx, ramesdk.SplitWords, true)
}

// PackSentenceWithSegmenter packs a sentence using a language-companion word segmenter,
// 128k BPE prior/inflection harmonics, and structured ISO date beacons.
func PackSentenceWithSegmenter(
	enc *ramesdk.BlindEncoder,
	sentence string,
	sentIdx int,
	seg ramesdk.WordSegmenter,
	englishDates bool,
) []ramesdk.BlindTokenAtom {
	return PackSentenceWithUnitLookup(enc, sentence, sentIdx, seg, englishDates, LookupUnitAndStatHarmonics)
}

// PackSentenceWithUnitLookup chains the Universal Common Core (`common.LookupCommonUnitAndStatHarmonics`)
// with a language-specific `langUnitLookup` (if provided).
func PackSentenceWithUnitLookup(
	enc *ramesdk.BlindEncoder,
	sentence string,
	sentIdx int,
	seg ramesdk.WordSegmenter,
	englishDates bool,
	langUnitLookup UnitHarmonicLookup,
) []ramesdk.BlindTokenAtom {
	canonWords := ramesdk.SplitWords(sentence)
	canonMarks := computeSentenceFormFlagMarks(sentence, canonWords)
	var baseWords []string
	if seg != nil {
		baseWords = seg(sentence)
	}
	if len(baseWords) == 0 {
		baseWords = canonWords
	}
	if len(baseWords) == 0 {
		return enc.PackSentenceAtoms(sentence)
	}
	type packedWordEntry struct {
		text        string
		isSentFirst bool
		formMark    uint8
	}
	entries := make([]packedWordEntry, 0, len(baseWords)+4)
	var beacons []ramesdk.BlindTokenAtom
	seenBeacon := make(map[uint64]bool, 4)
	addDateBeacon := func(iso string) {
		h := enc.KeyedHash64("cal:" + iso)
		if !seenBeacon[h] {
			seenBeacon[h] = true
			beacons = append(beacons, ramesdk.BlindTokenAtom{
				H:          h,
				RawBitsX10: CalendarBeaconWidthX10,
				ClassFlags: ramesdk.FlagHasDigit | ramesdk.FlagHasConn,
			})
		}
		isoH := enc.KeyedHash64(iso)
		if !seenBeacon[isoH] {
			seenBeacon[isoH] = true
			beacons = append(beacons, ramesdk.BlindTokenAtom{
				H:          isoH,
				RawBitsX10: 284,
				ClassFlags: ramesdk.FlagHasDigit | ramesdk.FlagHasConn,
			})
		}
	}

	for _, iso := range ramesdk.ExtractStructuredDateHarmonics(sentence) {
		addDateBeacon(iso)
		if englishDates {
			if parsed, err := time.Parse("2006-01-02", iso); err == nil {
				entries = append(entries,
					packedWordEntry{text: parsed.Format("January")},
					packedWordEntry{text: parsed.Format("Jan")},
					packedWordEntry{text: parsed.Format("2006")},
				)
			}
		} else if len(iso) >= 4 {
			entries = append(entries, packedWordEntry{text: iso[:4]})
		}
	}

	canonCursor := 0
	for baseIdx, word := range baseWords {
		var fm uint8
		if canonCursor < len(canonWords) && canonWords[canonCursor] == word {
			fm = canonMarks[canonCursor]
			canonCursor++
		}
		entries = append(entries, packedWordEntry{
			text:        word,
			isSentFirst: baseIdx == 0,
			formMark:    fm,
		})
		trimmed := strings.TrimSpace(word)
		if strings.Count(trimmed, "-") == 2 {
			if parsed, err := time.Parse("2006-01-02", trimmed); err == nil {
				addDateBeacon(trimmed)
				if englishDates {
					entries = append(entries,
						packedWordEntry{text: parsed.Format("January")},
						packedWordEntry{text: parsed.Format("Jan")},
						packedWordEntry{text: parsed.Format("2006")},
					)
				}
			}
		}
	}
	for _, symUnit := range ExtractInlineSymbolUnits(sentence) {
		entries = append(entries, packedWordEntry{
			text:     symUnit,
			formMark: ramesdk.FlagIsQuantityOrUnit,
		})
	}
	for _, seaUnit := range ExtractSEAsianUnitWords(sentence) {
		entries = append(entries, packedWordEntry{text: seaUnit})
	}
	if englishDates {
		for wi := 0; wi+2 < len(baseWords); wi++ {
			w0 := strings.TrimSpace(baseWords[wi])
			w1 := strings.TrimSpace(baseWords[wi+1])
			w2 := strings.TrimSpace(baseWords[wi+2])
			cand3 := w0 + " " + w1 + " " + w2
			for _, layout := range []string{"January 2 2006", "Jan 2 2006", "2 January 2006", "2 Jan 2006"} {
				if parsed, err := time.Parse(layout, cand3); err == nil && parsed.Year() >= 1990 && parsed.Year() <= 2100 {
					addDateBeacon(parsed.Format("2006-01-02"))
					break
				}
			}
		}
	}

	headerBoost := 1.0
	if sentIdx <= 4 {
		headerBoost = 1.15
	}
	atoms := append(make([]ramesdk.BlindTokenAtom, 0, len(entries)*2+len(beacons)), beacons...)
	for _, entry := range entries {
		trimmed := strings.TrimSpace(entry.text)
		low := strings.ToLower(trimmed)
		if low == "" {
			continue
		}
		var flags uint8
		var rawWidth uint32
		hasLower := false
		for idx, r := range trimmed {
			rawWidth += uint32(ramesdk.CarrierUnits(r))
			if unicode.IsUpper(r) {
				if !entry.isSentFirst || idx > 0 {
					flags |= ramesdk.FlagHasUpper
				}
				if idx > 0 && hasLower {
					flags |= ramesdk.FlagHasCamel
				}
			} else if unicode.IsLower(r) {
				hasLower = true
			} else if unicode.IsDigit(r) {
				flags |= ramesdk.FlagHasDigit
			} else if r == '-' || r == '_' || r == '.' {
				flags |= ramesdk.FlagHasConn
			}
		}
		// SplitWords already emits the component words for non-numeric compounds.
		if flags&ramesdk.FlagHasConn != 0 && flags&ramesdk.FlagHasDigit == 0 {
			continue
		}

		isDigit := flags&ramesdk.FlagHasDigit != 0
		isCJK := text.IsPureCJK(low)
		isProper := flags&(ramesdk.FlagHasUpper|ramesdk.FlagHasCamel) != 0 || tokenzr.IsBPEProperNounPrior(trimmed)
		prior := tokenzr.TokenPriorBits(low)
		valence := tokenzr.BPEInflectionValence(low)
		stem := text.StemToken(low)
		root := text.MorphRootPrefix(low)
		if stem != "" && stem != low {
			if stemPrior := tokenzr.TokenPriorBits(stem); stemPrior > prior {
				prior = 0.5*prior + 0.5*stemPrior
			}
			if stemValence := tokenzr.BPEInflectionValence(stem); stemValence > valence {
				valence = stemValence
			}
		}
		effectiveBits := prior + 1.2*float64(valence)
		highGain := text.IsHighGainToken(low) || isProper || isDigit || isCJK

		var width uint16
		switch {
		case !highGain && !isCJK && rawWidth <= 60:
			width = 20
		case !highGain && !isCJK:
			width = 24
		case !isProper && !isDigit && !isCJK && prior < 12.65:
			scaled := int((prior - 9.0) * 12.0)
			if scaled < 26 {
				scaled = 26
			} else if scaled > 52 {
				scaled = 52
			}
			width = uint16(scaled)
		default:
			excess := math.Max(1.2, effectiveBits-9.2)
			scaled := int(26.0 * math.Pow(excess, 1.32) * headerBoost)
			if int(rawWidth) > scaled {
				scaled = int(rawWidth)
			}
			if isProper || isDigit {
				flags |= ramesdk.FlagHasUpper
				scaled = int(float64(scaled) * 1.28)
			}
			maxNonGlued := 300
			if int(rawWidth) > maxNonGlued {
				maxNonGlued = int(rawWidth)
			}
			if scaled > maxNonGlued {
				scaled = maxNonGlued
			}
			if scaled < 76 {
				scaled = 76
			} else if scaled > 640 {
				scaled = 640
			}
			width = uint16(scaled)
		}
		width = (width + 1) &^ 1
		if width < 20 {
			width = 20
		}
		var unitTag, dimTag string
		// Single-letter Latin tokens ("s" from "it's", "m" from "I'm", "t" from "don't") only
		// resolve to SI unit harmonics when preceded by a numeric anchor (FlagIsQuantityOrUnit).
		// Only pure measurement/currency/statistical units (@unit:*) replace (stem, root) in the
		// odd-width harmonic slots; coarse continent/category tags (@geo:cont:*, @venue:*, @obj:*)
		// must not overwrite high-specificity lexical stems/roots or create continent-wide false hubs.
		if isCJK || rawWidth > 20 || (entry.formMark&ramesdk.FlagIsQuantityOrUnit) != 0 {
			unitTag, dimTag = common.LookupCommonUnitAndStatHarmonics(low)
			if unitTag == "" && dimTag == "" && langUnitLookup != nil {
				unitTag, dimTag = langUnitLookup(low)
			}
			if !strings.HasPrefix(unitTag, "@unit:") && !strings.HasPrefix(unitTag, "@geo:cc:") {
				unitTag, dimTag = "", ""
			}
		}
		if (unitTag != "" || dimTag != "") && width < 28 {
			width = 28
		}
		primaryFlags := (flags &^ ramesdk.FlagHasConn) & 0x0F
		if isDigit && flags&ramesdk.FlagHasConn != 0 {
			primaryFlags = flags & 0x0F
		}
		primaryFlags |= entry.formMark
		atoms = append(atoms, ramesdk.BlindTokenAtom{
			H:          enc.KeyedHash64(low),
			RawBitsX10: width,
			ClassFlags: primaryFlags,
		})

		harmonicFlags := ((flags &^ ramesdk.FlagHasDigit) | ramesdk.FlagHasConn) & 0x0F
		harmonicFlags |= 1 << 4 // FlagIsHarmonicCompanion (8-bit F bit 4 explicit harmonic-companion marker)
		isCanonicalUnitOrGeo := strings.HasPrefix(unitTag, "@unit:") || strings.HasPrefix(unitTag, "@geo:")
		if isCanonicalUnitOrGeo && (unitTag != "" || dimTag != "") {
			if unitTag != "" {
				atoms = append(atoms, ramesdk.BlindTokenAtom{
					H:          enc.KeyedHash64(unitTag),
					RawBitsX10: width - 1,
					ClassFlags: harmonicFlags,
				})
			}
			if dimTag != "" && dimTag != unitTag {
				atoms = append(atoms, ramesdk.BlindTokenAtom{
					H:          enc.KeyedHash64(dimTag),
					RawBitsX10: width - 3,
					ClassFlags: harmonicFlags,
				})
			}
		} else if !isDigit && !isCJK && width >= 26 {
			if stem != "" && stem != low {
				atoms = append(atoms, ramesdk.BlindTokenAtom{
					H:          enc.KeyedHash64(stem),
					RawBitsX10: width - 1,
					ClassFlags: harmonicFlags,
				})
			} else if unitTag != "" || len(atoms) == len(beacons)+1 {
				atoms = append(atoms, ramesdk.BlindTokenAtom{
					H:          enc.KeyedHash64(low),
					RawBitsX10: width - 1,
					ClassFlags: harmonicFlags,
				})
			}
			if root != "" && root != low && root != stem {
				atoms = append(atoms, ramesdk.BlindTokenAtom{
					H:          enc.KeyedHash64(root),
					RawBitsX10: width - 3,
					ClassFlags: harmonicFlags,
				})
			} else if unitTag != "" {
				atoms = append(atoms, ramesdk.BlindTokenAtom{
					H:          enc.KeyedHash64(unitTag),
					RawBitsX10: width - 3,
					ClassFlags: harmonicFlags,
				})
			}
		} else if unitTag != "" || dimTag != "" {
			if unitTag != "" {
				atoms = append(atoms, ramesdk.BlindTokenAtom{
					H:          enc.KeyedHash64(unitTag),
					RawBitsX10: width - 1,
					ClassFlags: harmonicFlags,
				})
			}
			if dimTag != "" && dimTag != unitTag {
				atoms = append(atoms, ramesdk.BlindTokenAtom{
					H:          enc.KeyedHash64(dimTag),
					RawBitsX10: width - 3,
					ClassFlags: harmonicFlags,
				})
			}
		}
	}
	if len(atoms) == 0 {
		return enc.PackSentenceAtoms(sentence)
	}
	return atoms
}
