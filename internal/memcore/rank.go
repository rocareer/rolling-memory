// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package memcore

import (
	"math"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/rocareer/rolling-memory/internal/text"
	"github.com/rocareer/rolling-memory/internal/tokenzr"
)

// PhraseBonus computes the sentence-level phrase match bonus (+10 per matched phrase of >= 16.0 Shannon bits,
// unifying >=8-letter alphabetic phrases and >=2-character CJK phrases on the same entropy scale).
func PhraseBonus(hay string, phrases []string) float64 {
	if len(phrases) == 0 || hay == "" {
		return 0
	}
	b := 0.0
	for _, ph := range phrases {
		if StringShannonBits(ph) >= 16.0 && strings.Contains(hay, ph) {
			b += 10
		}
	}
	return b
}

// IsShortOrDigitsToken reports whether s is an all-digit token or a non-ideographic short token (<=2 runes).
func IsShortOrDigitsToken(s string) bool {
	if s == "" {
		return false
	}
	allDigits := true
	hasIdeograph := false
	runeCnt := 0
	for _, r := range s {
		runeCnt++
		if r >= 0x3040 && (unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r)) {
			hasIdeograph = true
		}
		if r < '0' || r > '9' {
			if r < 0x80 || !unicode.IsDigit(r) {
				allDigits = false
			}
		}
	}
	if hasIdeograph {
		return false
	}
	return allDigits || runeCnt <= 2
}

// HasIdeographRune reports whether s contains any Han/Hiragana/Katakana rune.
func HasIdeographRune(s string) bool {
	for _, r := range s {
		if r >= 0x3040 && (unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r)) {
			return true
		}
	}
	return false
}

func runeBoundaryScriptKind(r rune) int {
	if unicode.IsMark(r) {
		return -1
	}
	switch {
	case IsContinuousSyllabicOrIdeographic(r) && !unicode.Is(unicode.Hangul, r):
		return 3
	case unicode.IsNumber(r):
		return 2
	case unicode.Is(unicode.Hangul, r):
		return 4
	case unicode.Is(unicode.Cyrillic, r):
		return 5
	case unicode.Is(unicode.Greek, r):
		return 6
	case unicode.Is(unicode.Arabic, r):
		return 7
	case unicode.Is(unicode.Hebrew, r):
		return 8
	case unicode.Is(unicode.Latin, r):
		return 1
	case unicode.IsLetter(r):
		return 9
	default:
		return 0
	}
}

// ContainsWordBounded checks whether tok appears in hay with non-alphanumeric or orthogonal script/digit phase boundaries on both ends.
func ContainsWordBounded(hay, tok string) bool {
	if hay == "" || tok == "" {
		return false
	}
	firstR, _ := utf8.DecodeRuneInString(tok)
	firstBaseK := runeBoundaryScriptKind(firstR)
	lastBaseK := firstBaseK
	for rem := tok; rem != ""; {
		r, w := utf8.DecodeLastRuneInString(rem)
		if w <= 0 {
			break
		}
		if k := runeBoundaryScriptKind(r); k >= 0 {
			lastBaseK = k
			break
		}
		rem = rem[:len(rem)-w]
	}
	tokMultiSymbol := StringShannonBits(tok) >= 0.50*AtomicConceptEntropyBits
	off := 0
	for {
		idx := strings.Index(hay[off:], tok)
		if idx < 0 {
			break
		}
		pos := off + idx
		end := pos + len(tok)
		leftOK := true
		if pos > 0 {
			if firstBaseK < 0 {
				leftOK = false
			} else {
				prevBaseK := 0
				for rem := hay[:pos]; rem != ""; {
					pr, pw := utf8.DecodeLastRuneInString(rem)
					if pw <= 0 {
						break
					}
					pk := runeBoundaryScriptKind(pr)
					if pk >= 0 {
						prevBaseK = pk
						break
					}
					rem = rem[:len(rem)-pw]
				}
				switch {
				case prevBaseK == 0:
					leftOK = true
				case prevBaseK == firstBaseK:
					leftOK = false
				case firstBaseK == 2:
					leftOK = true
				case prevBaseK == 2:
					leftOK = tokMultiSymbol
				default:
					leftOK = true
				}
			}
		}
		rightOK := true
		if end < len(hay) {
			nextR, _ := utf8.DecodeRuneInString(hay[end:])
			nextK := runeBoundaryScriptKind(nextR)
			switch {
			case nextK < 0:
				rightOK = false
			case nextK == 0:
				rightOK = true
			case nextK == lastBaseK:
				rightOK = false
			case lastBaseK == 2:
				rightOK = true
			case nextK == 2:
				rightOK = tokMultiSymbol
			default:
				rightOK = true
			}
		}
		if leftOK && rightOK {
			return true
		}
		_, step := utf8.DecodeRuneInString(hay[pos:])
		if step < 1 {
			step = 1
		}
		off = pos + step
	}
	if firstBaseK == 1 && lastBaseK == 1 && tokMultiSymbol &&
		!strings.ContainsRune(tok, '.') && strings.ContainsRune(hay, '.') {
		for i := 0; i < len(hay); i++ {
			if hay[i] != tok[0] {
				continue
			}
			if i > 0 {
				pr, _ := utf8.DecodeLastRuneInString(hay[:i])
				if runeBoundaryScriptKind(pr) != 0 {
					continue
				}
			}
			hi, ti, dots := i, 0, 0
			for hi < len(hay) && ti < len(tok) {
				if hay[hi] == tok[ti] {
					hi++
					ti++
				} else if hay[hi] == '.' && ti > 0 && hi+1 < len(hay) && hay[hi+1] == tok[ti] {
					dots++
					hi++
				} else {
					break
				}
			}
			if ti == len(tok) && dots > 0 {
				rightOK := true
				if hi < len(hay) {
					nr, _ := utf8.DecodeRuneInString(hay[hi:])
					if runeBoundaryScriptKind(nr) != 0 {
						rightOK = false
					}
				}
				if rightOK {
					return true
				}
			}
		}
	}
	return false
}

// LCPRunes returns the longest common prefix length of two rune slices.
func LCPRunes(a, b []rune) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	i := 0
	for i < n && a[i] == b[i] {
		i++
	}
	return i
}

// CharBigramDice computes the symmetric character 2-gram multiset Dice similarity 2*|A∩B|/(|A|+|B|).
func CharBigramDice(a, b []rune) float64 {
	na, nb := len(a)-1, len(b)-1
	if na <= 0 || nb <= 0 {
		return 0
	}
	if string(a) > string(b) {
		a, b = b, a
		na, nb = nb, na
	}
	used := make([]bool, nb)
	inter := 0
	for i := 0; i < na; i++ {
		a0, a1 := a[i], a[i+1]
		for j := 0; j < nb; j++ {
			if !used[j] && b[j] == a0 && b[j+1] == a1 {
				used[j] = true
				inter++
				break
			}
		}
	}
	return float64(2*inter) / float64(na+nb)
}

// AtomicConceptEntropyBits is the universal language-agnostic minimum Shannon self-information (in bits)
// required for a shared subword/morpheme core to anchor an independent lexical concept without exact word-boundary equality.
//   - 1 Han ideograph = 10.0 bits (>= 8.0 bits)
//   - 1 Japanese Kanji + 1 Kana ("食べ") = 14.2 bits (>= 8.0 bits)
//   - 2 Hangul syllables ("공부") = 14.0 bits (>= 8.0 bits)
//   - 4..5 alphabetic letters = 8.0..10.0 bits (>= 8.0 bits), whereas 2..3 letters ("in", "un", "art", "cat", "car") = 4.0..6.0 bits (< 8.0 bits).
// ---- Shannon / rune 原语：唯一正本在 internal/text（薄转发，杜绝双副本漂移）----
//
// 以下常量与函数的权威实现位于 internal/text，memcore 仅做 1:1 转发。

const (
	AtomicConceptEntropyBits      = text.AtomicConceptEntropyBits
	SingleAlternationMaxBits      = text.SingleAlternationMaxBits
	OverStemmingDivergenceMaxBits = text.OverStemmingDivergenceMaxBits
)

// RuneShannonBits returns the intrinsic prior Shannon information content H_0(r) (delegates to internal/text).
func RuneShannonBits(r rune) float64 { return text.RuneShannonBits(r) }

// SliceShannonBits returns the total prior Shannon self-information of a rune slice (delegates to internal/text).
func SliceShannonBits(rs []rune) float64 { return text.SliceShannonBits(rs) }

// StringShannonBits returns the total prior Shannon self-information of a string (delegates to internal/text).
func StringShannonBits(s string) float64 { return text.StringShannonBits(s) }

// IsPureHan reports whether s consists solely of Han ideographs (delegates to internal/text).
func IsPureHan(s string) bool { return text.IsPureHan(s) }

// LCSuffixRunes returns the longest common suffix length of two rune slices.
func LCSuffixRunes(a, b []rune) int {
	na, nb := len(a), len(b)
	n := na
	if nb < n {
		n = nb
	}
	i := 0
	for i < n && a[na-1-i] == b[nb-1-i] {
		i++
	}
	return i
}

// suffixesShareStemRune reports whether the post-LCP suffixes of a and b share a rune
// that also occurs inside the shared stem prefix a[:lcpLen] (e.g. "spiel-en" <-> "spiel-te" sharing 'e').
func suffixesShareStemRune(a, b []rune, lcpLen int) bool {
	if lcpLen <= 0 || lcpLen >= len(a) || lcpLen >= len(b) {
		return false
	}
	for _, r := range a[lcpLen:] {
		inB := false
		for _, rb := range b[lcpLen:] {
			if r == rb {
				inB = true
				break
			}
		}
		if !inB {
			continue
		}
		for _, rs := range a[:lcpLen] {
			if r == rs {
				return true
			}
		}
	}
	return false
}

// MorphSimWordPair computes the universal Zero-Rune-Count morphological similarity kernel
// in Shannon Self-Information Bits (Plan 1 × Plan 3: Subword Information Retention Field).
// All internal bit thresholds are homogeneous multiples of AtomicConceptEntropyBits (tau_atom = 8.0b),
// SingleAlternationMaxBits (tau_alt = 4.5b), and OverStemmingDivergenceMaxBits (tau_div = 11.5b).
func MorphSimWordPair(qRunes, stRunes, wRunes []rune) float64 {
	if len(qRunes) == 0 || len(wRunes) == 0 {
		return 0
	}
	lcpLen := LCPRunes(qRunes, wRunes)
	if len(qRunes) == len(wRunes) && lcpLen == len(qRunes) {
		return 1.0
	}
	const tauAtom = AtomicConceptEntropyBits
	const tauAlt = SingleAlternationMaxBits
	const tauDiv = OverStemmingDivergenceMaxBits

	bQ := SliceShannonBits(qRunes)
	bW := SliceShannonBits(wRunes)
	bMin, bMax := math.Min(bQ, bW), math.Max(bQ, bW)
	if bMin < 0.75*tauAtom {
		return 0
	}
	bLCP := SliceShannonBits(qRunes[:lcpLen])
	bResMin := bMin - bLCP
	bResMax := bMax - bLCP
	if bMin < tauAtom {
		if bResMin == 0 && bMax >= 1.5*tauAtom && bMax <= 1.75*tauAtom && tokenzr.IsTerminalInflectedExtension(qRunes, wRunes) {
			return 0.85
		}
		return 0
	}
	_ = stRunes

	sufLen := LCSuffixRunes(qRunes, wRunes)
	minLen := min(len(qRunes), len(wRunes))
	nonOverlapSufLen := sufLen
	if lcpLen+nonOverlapSufLen > minLen {
		nonOverlapSufLen = minLen - lcpLen
	}
	var bSufNonOverlap float64
	if nonOverlapSufLen > 0 {
		bSufNonOverlap = SliceShannonBits(qRunes[len(qRunes)-nonOverlapSufLen:])
	}
	bAnchored := bLCP + bSufNonOverlap
	bGapMin := bMin - bAnchored
	bGapMax := bMax - bAnchored

	// Channel 3a: Dual-Boundary Circumfix/Infix & Orthographic Gemination Field (zero character-length counting)
	// Requires bLCP >= tauAtom (8.0b) and bAnchored >= 1.5*tauAtom (12.0b), admitting either:
	//   (i) Single-symbol geminate doubling on a >=1.75*tauAtom (14.0b) anchored frame where bGapMin == 0 and
	//       bGapMax <= 0.25*tauAtom ("accomodate"<->"accommodate", while blocking 12.0b "carton"<->"cartoon"), OR
	//   (ii) Equal-mass single-symbol internal inflection with single-symbol coda on a >=1.5*tauAtom (12.0b) anchored frame
	//        where bGapMin == H_0(infix) and bSufNonOverlap == H_0(coda) ("공부하다"<->"공부했다", "organize"<->"organise", "analyze"<->"analyse").
	if bLCP >= tauAtom && bSufNonOverlap >= 0.25*tauAtom && bAnchored >= 1.5*tauAtom &&
		(bAnchored/bMax) >= 0.75 && bGapMax <= (tauAtom-0.0625*tauAtom) {
		if math.Abs(bGapMin) < 1e-6 && bAnchored >= 1.75*tauAtom && bGapMax > 0 && bGapMax <= 0.25*tauAtom {
			longer := wRunes
			if bQ > bW {
				longer = qRunes
			}
			if lcpLen > 0 && lcpLen < len(longer) && longer[lcpLen] == longer[lcpLen-1] &&
				math.Abs(bGapMax-RuneShannonBits(longer[lcpLen])) < 1e-6 {
				return 0.85
			}
		} else if math.Abs(bMax-bMin) < 1e-6 && bAnchored >= 1.75*tauAtom && bGapMin > 0 &&
			lcpLen < len(qRunes) && math.Abs(bGapMin-RuneShannonBits(qRunes[lcpLen])) < 1e-6 &&
			math.Abs(bSufNonOverlap-RuneShannonBits(qRunes[len(qRunes)-1])) < 1e-6 {
			return 0.85
		}
	}

	// Guard 0: Over-Stemming Information Divergence Guard (zero hardcoded magic exemptions)
	if bLCP >= tauAtom && bResMax >= tauDiv && (bLCP/bMax) < 0.60 {
		return 0
	}

	// Channel 1: Left-Anchored Shannon Information Retention Field (zero character-length counting)
	// Strictly enforces:
	//  - Pure-Han Compound Root Floor: pure Han pairs require bResMin == 0 and bLCP >= 2.0*tauAtom (16.0b, >=2 ideographs),
	//    preventing 1-character Han words ("马", "火", "手") from matching 2-character compounds ("马桶", "火车", "手机");
	//  - Symmetric Stem Pivot bPivotMax from bMax: prevents 4-letter (8.0b) roots from matching >=7-letter (14.0b+) words;
	//  - Single-Coda Elision Law: when a 0.25*tauAtom (2.0b) silent coda elides from the shorter root ("dance"->"dancing"),
	//    the inflected form must add >= 0.50*tauAtom (4.0b) net bit mass (blocking "status"<->"stature" and "adawe"<->"adawn").
	isPureHanPair := IsPureHan(string(qRunes)) && IsPureHan(string(wRunes))
	if bMin == tauAtom && bLCP == 0.75*tauAtom && bResMin == 0.25*tauAtom &&
		bMax >= 1.25*tauAtom && bMax <= 1.75*tauAtom && !isPureHanPair &&
		!tokenzr.HasCompetingBPESubroot(qRunes, wRunes, lcpLen) &&
		tokenzr.IsTerminalInflectedExtension(qRunes, wRunes) {
		return 0.85
	}
	if bLCP >= tauAtom && bResMin <= tauAlt && (!isPureHanPair || (bResMin == 0 && bLCP >= 2.0*tauAtom)) {
		if !(math.Abs(bMax-bMin) < 1e-6 && bSufNonOverlap > 0) &&
			!(bResMin > 0 && bResMin <= 0.25*tauAtom && (bMax-bMin) < 0.50*tauAtom) &&
			!(bResMin > 0 && bLCP == tauAtom && tokenzr.HasCompetingBPESubroot(qRunes, wRunes, lcpLen)) {
			if bMin == tauAtom && bLCP == tauAtom && bResMin == 0 && bMax <= 1.75*tauAtom &&
				!tokenzr.HasCompetingBPESubroot(qRunes, wRunes, lcpLen) &&
				tokenzr.IsTerminalInflectedExtension(qRunes, wRunes) {
				return 0.85
			}
			bPivotMax := math.Max(tauAtom, math.Min(2.25*tauAtom, math.Floor((bMax*2.0+2.0)/6.0)*2.0))
			if bMax < 1.25*tauAtom {
				bPivotMax = bMax
			}
			if bLCP >= bPivotMax && bResMin <= 0.25*tauAtom {
				if (math.Abs(bMin-bPivotMax) < 0.1 && bPivotMax < bMax &&
					bResMin == 0 && bMax <= 1.5*tauAtom && tokenzr.IsTerminalInflectedExtension(qRunes, wRunes)) ||
					(bMin >= 1.25*tauAtom && bMax >= 1.5*tauAtom && (bLCP/bMax) >= 0.50) {
					return 0.85
				}
			}
			singleSymbolCodaSwap := math.Abs(bMax-bMin) < 1e-6 && bLCP >= 1.5*tauAtom &&
				lcpLen < len(qRunes) && lcpLen < len(wRunes) &&
				math.Abs(bResMin-RuneShannonBits(qRunes[lcpLen])) < 1e-6 &&
				math.Abs(bResMax-RuneShannonBits(wRunes[lcpLen])) < 1e-6
			if bMin >= 1.25*tauAtom && (bLCP/bMax) >= 0.45 {
				if bResMin == 0 ||
					(bResMin <= 0.25*tauAtom && (bLCP >= 1.25*tauAtom || bMax <= 1.75*tauAtom)) ||
					singleSymbolCodaSwap ||
					(bLCP >= 1.5*tauAtom && (bMax-bMin) >= 0.50*tauAtom && (bLCP/bMax) >= 0.60) ||
					(bLCP == 1.25*tauAtom && bMin == 1.75*tauAtom && bMax <= 2.0*tauAtom &&
						(bMax > bMin || suffixesShareStemRune(qRunes, wRunes, lcpLen))) {
					return 0.85
				}
			}
		}
	}

	// Channel 2: Right-Anchored MDL Prefix-Root Information Field (LCP == 0 prefixed derivations)
	if bMin >= 1.25*tauAtom && bLCP == 0 {
		bSuf := SliceShannonBits(qRunes[len(qRunes)-sufLen:])
		bPfx := bMax - bMin
		if math.Abs(bSuf-bMin) < 0.1 && bPfx >= 0.50*tauAtom && bPfx <= 0.75*tauAtom {
			return 0.85
		}
		tryPrefixPeel := func(prefixed, other []rune) bool {
			var pfxBits float64
			bOther := SliceShannonBits(other)
			for idx := 0; idx < len(prefixed)-1; idx++ {
				pfxBits += RuneShannonBits(prefixed[idx])
				if pfxBits < 0.50*tauAtom {
					continue
				}
				if pfxBits > 0.8125*tauAtom {
					break
				}
				peeled := prefixed[idx+1:]
				bPeeled := SliceShannonBits(peeled)
				if bPeeled < 1.25*tauAtom {
					break
				}
				subLCP := LCPRunes(other, peeled)
				bSubLCP := SliceShannonBits(other[:subLCP])
				bSubMin := math.Min(bOther, bPeeled)
				bSubMax := math.Max(bOther, bPeeled)
				bSubResMin := bSubMin - bSubLCP
				allowedResMin := 0.0
				if bSubMin <= 1.25*tauAtom && (bSubMax-bSubMin) >= 0.50*tauAtom {
					allowedResMin = 0.25 * tauAtom
				}
				if bSubLCP >= tauAtom && bSubResMin <= allowedResMin &&
					(bSubLCP >= 1.25*tauAtom || bSubResMin > 0) &&
					(bSubLCP/bSubMax) >= 0.55 && (bSubMax-bSubLCP) <= 0.8125*tauAtom {
					return true
				}
			}
			return false
		}
		if tryPrefixPeel(wRunes, qRunes) || tryPrefixPeel(qRunes, wRunes) {
			return 0.85
		}
	}

	return 0
}

// MatchTokenOrStemWithStem performs exact (1.0), unspaced compound (0.90), and
// Shannon Bit-Field morphological/stem (0.85) matching.
func MatchTokenOrStemWithStem(hay, tok, st string, legacyStem bool) float64 {
	if hay == "" || tok == "" {
		return 0
	}
	if legacyStem {
		if IsShortOrDigitsToken(tok) {
			if ContainsWordBounded(hay, tok) {
				return 1.0
			}
			return 0
		}
		if strings.Contains(hay, tok) {
			return 1.0
		}
		if st != "" && st != tok {
			if IsShortOrDigitsToken(st) {
				if ContainsWordBounded(hay, st) {
					return 0.85
				}
			} else if strings.Contains(hay, st) {
				return 0.85
			}
		}
		if StringShannonBits(tok) >= 6.5 && strings.Contains(hay, " ") && strings.Contains(strings.ReplaceAll(hay, " ", ""), tok) {
			return 0.90
		}
		return 0
	}
	spec := loadOrCompileTokenSpec(tok, st)
	if spec.tokShort {
		if ContainsWordBounded(hay, tok) {
			return 1.0
		}
		return 0
	}
	if spec.hasIdeo {
		if strings.Contains(hay, tok) {
			return 1.0
		}
		if spec.pureHan {
			return 0
		}
		// Unspaced mixed Ideograph+Syllabic token (e.g. Japanese Kanji+Kana verb/adjective inflection "食べる" in "食堂で美味しい寿司を食べた"):
		// Scan all occurrences of the high-entropy ideographic onset inside `hay` and evaluate the Shannon Bit-Field kernel.
		qRunes := spec.qRunes
		bestIdeo := 0.0
		rem := hay
		for rem != "" {
			idx := strings.IndexRune(rem, qRunes[0])
			if idx < 0 {
				break
			}
			tailRunes := []rune(rem[idx:])
			if len(tailRunes) > len(qRunes) {
				tailRunes = tailRunes[:len(qRunes)]
			}
			if sim := MorphSimWordPair(qRunes, nil, tailRunes); sim > bestIdeo {
				bestIdeo = sim
			}
			_, w := utf8.DecodeRuneInString(rem[idx:])
			if w <= 0 {
				break
			}
			rem = rem[idx+w:]
		}
		return bestIdeo
	}
	if ContainsWordBounded(hay, tok) {
		return 1.0
	}
	if spec.stShort {
		if ContainsWordBounded(hay, st) {
			return 0.85
		}
	}
	bQ := spec.bQ
	if bQ < 0.50*AtomicConceptEntropyBits {
		return 0
	}
	onsetEnd := spec.onsetEnd
	hasOnset := onsetEnd > 0 && strings.Contains(hay, spec.onsetStr)
	hasCoda := spec.hasCodaCand && strings.Contains(hay, spec.codaStr)
	if !hasOnset && !hasCoda {
		return 0
	}
	qRunes := spec.qRunes
	stRunes := spec.stRunes
	best := 0.0
	var wordBuf [48]rune
	var prevWord [48]rune
	wLen, prevLen := 0, 0
	flushWord := func() {
		cur := wordBuf[:wLen]
		if len(cur) > 0 && unicode.IsNumber(cur[0]) && len(qRunes) > 0 && !unicode.IsNumber(qRunes[0]) {
			dEnd := 0
			for dEnd < len(cur) && unicode.IsNumber(cur[dEnd]) {
				dEnd++
			}
			if dEnd < len(cur) {
				tailLetters := cur[dEnd:]
				if SliceShannonBits(tailLetters) >= 0.50*AtomicConceptEntropyBits {
					if sc := MorphSimWordPair(qRunes, stRunes, tailLetters); sc > best {
						best = sc
					}
				}
			}
		}
		leftMatch := hasOnset && LCPRunes(qRunes, cur) >= onsetEnd
		var bCur float64
		if leftMatch {
			bCur = SliceShannonBits(cur)
			if bCur >= 0.50*AtomicConceptEntropyBits {
				if sc := MorphSimWordPair(qRunes, stRunes, cur); sc > best {
					best = sc
				}
			}
		} else if hasCoda && bQ >= 1.25*AtomicConceptEntropyBits {
			sufLen := LCSuffixRunes(qRunes, cur)
			if sufLen > 0 && SliceShannonBits(cur[len(cur)-sufLen:]) >= 0.50*AtomicConceptEntropyBits {
				bCur = SliceShannonBits(cur)
				if bCur >= 1.25*AtomicConceptEntropyBits {
					if sc := MorphSimWordPair(qRunes, stRunes, cur); sc > best {
						best = sc
					}
				}
			}
		}
		if best < 0.90 && bQ >= AtomicConceptEntropyBits && prevLen > 0 &&
			LCPRunes(qRunes, prevWord[:prevLen]) >= onsetEnd {
			if bCur == 0 {
				bCur = SliceShannonBits(cur)
			}
			bPrev := SliceShannonBits(prevWord[:prevLen])
			curValid := bCur >= 0.50*AtomicConceptEntropyBits || (len(cur) > 0 && unicode.IsDigit(cur[0]) && bCur >= 3.0)
			if bPrev >= 0.50*AtomicConceptEntropyBits && curValid {
				combLen := prevLen + wLen
				if combLen <= len(prevWord)*2 {
					var comb [96]rune
					copy(comb[:prevLen], prevWord[:prevLen])
					copy(comb[prevLen:combLen], cur)
					bComb := SliceShannonBits(comb[:combLen])
					if math.Abs(bComb-bQ) <= 0.25*AtomicConceptEntropyBits {
						w1, w2 := string(prevWord[:prevLen]), string(cur)
						if len(Tokenize(w1)) > 0 && len(Tokenize(w2)) > 0 {
							if math.Abs(bComb-bQ) < 1e-6 && LCPRunes(qRunes, comb[:combLen]) == combLen {
								best = 0.90
							} else if MorphSimWordPair(qRunes, stRunes, comb[:combLen]) >= 0.85 {
								best = 0.90
							}
						}
					}
				}
			}
		}
		if leftMatch && bCur >= 0.50*AtomicConceptEntropyBits && wLen <= len(prevWord) {
			copy(prevWord[:wLen], cur)
			prevLen = wLen
		} else {
			prevLen = 0
		}
		wLen = 0
	}
	curKind := 0
	for _, r := range hay {
		k := runeBoundaryScriptKind(r)
		if k < 0 {
			if wLen > 0 && wLen < len(wordBuf) {
				wordBuf[wLen] = r
				wLen++
			}
			continue
		}
		if k > 0 {
			isAlnumPair := (curKind == 1 || curKind == 2) && (k == 1 || k == 2)
			if wLen > 0 && curKind > 0 && k != curKind && !isAlnumPair {
				flushWord()
				if best >= 1.0 {
					return best
				}
			}
			curKind = k
			if wLen < len(wordBuf) {
				wordBuf[wLen] = r
				wLen++
			}
		} else if wLen > 0 {
			flushWord()
			curKind = 0
			if best >= 1.0 {
				return best
			}
		}
	}
	if wLen > 0 {
		flushWord()
	}
	return best
}

// MatchTokenOrStem is the convenient 2-arg entry point using StemToken(tok) and default morphological mode.
func MatchTokenOrStem(lowerText, tok string) float64 {
	t := strings.ToLower(strings.TrimSpace(tok))
	return MatchTokenOrStemWithStem(lowerText, t, StemToken(t), LegacyStemMode.Load())
}

// CleanWords extracts lowercased word tokens (SliceShannonBits >= 0.50*AtomicConceptEntropyBits) from text for lexical matching.
func CleanWords(text string) []string {
	var out []string
	var cur []rune
	flush := func() {
		if SliceShannonBits(cur) >= 0.50*AtomicConceptEntropyBits {
			out = append(out, strings.ToLower(string(cur)))
		}
		cur = cur[:0]
	}
	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r) {
			cur = append(cur, r)
		} else {
			flush()
		}
	}
	flush()
	return out
}
