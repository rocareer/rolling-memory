// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package text

import (
	"math"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/rocareer/rolling-memory/internal/tokenzr"
)

// Tokenization and information-gain ranking invariants:
//  1. Zero static stopword lists: tokenization never drops tokens via hardcoded stopword or closed-class tables;
//  2. Permissive candidate retention: lexical operators preserve ambiguous homographs, proper names, acronyms, and short content words without destructive pruning;
//  3. Semantic neutrality: relation and coreference semantics belong to the language model; lexical operators provide structural and statistical support only;
//  4. Continuous Shannon + BPE gain: StringShannonBits and TokenInformationMass serve solely as continuous ranking and top-k budget weights (SelectTopInformationTokens), never as boolean cutoff gates.

// MaxQueryTokens is the hard cap on tokens per query (compacted by TokenInformationMass gain).
const MaxQueryTokens = 24

// IsContinuousSyllabicOrIdeographic reports whether r belongs to a script without word spaces.
func IsContinuousSyllabicOrIdeographic(r rune) bool {
	return r == 'ー' ||
		unicode.Is(unicode.Han, r) ||
		unicode.Is(unicode.Hiragana, r) ||
		unicode.Is(unicode.Katakana, r) ||
		unicode.Is(unicode.Hangul, r) ||
		unicode.Is(unicode.Thai, r) ||
		unicode.Is(unicode.Lao, r) ||
		unicode.Is(unicode.Khmer, r) ||
		unicode.Is(unicode.Myanmar, r) ||
		unicode.Is(unicode.Tibetan, r)
}

// IsPureCJK reports whether s consists solely of Han/Hiragana/Katakana characters.
func IsPureCJK(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r != 'ー' && !unicode.Is(unicode.Han, r) && !unicode.Is(unicode.Hiragana, r) && !unicode.Is(unicode.Katakana, r) {
			return false
		}
	}
	return true
}

// TokenInformationMass computes the composite Shannon + BPE prior + morphological + proper-noun information gain of a token.
// Note: Used strictly for gain ranking and budget prioritization ("只负责增益，不负责一刀切生死过滤").
func TokenInformationMass(tk string) float64 {
	mass := 0.5*StringShannonBits(tk) + 0.5*tokenzr.EffectiveLexicalBits(tk)
	if tokenzr.IsBPEProperNounPrior(tk) {
		mass += 2.5
	}
	return mass
}

// IsHighGainToken reports whether tk belongs to the Primary Gain Tier for ordering priority
// (never used to delete tokens; low-gain tokens remain in the Secondary Residual Tier).
func IsHighGainToken(tk string) bool {
	if tk == "" {
		return false
	}
	if r0, w0 := utf8.DecodeRuneInString(tk); w0 > 0 {
		if unicode.IsDigit(r0) {
			return true
		}
		if (r0 == '#' || unicode.Is(unicode.Sc, r0)) && len(tk) > w0 {
			if r1, _ := utf8.DecodeRuneInString(tk[w0:]); unicode.IsDigit(r1) {
				return true
			}
		}
	}
	if IsPureCJK(tk) {
		if StringShannonBits(tk) < 2.0*AtomicConceptEntropyBits {
			return tokenzr.TokenPriorBits(tk) >= 11.20
		}
		return tokenzr.TokenPriorBits(tk) >= 12.25
	}
	// Colloquial expressive elongation ("sooooo", "ummmmm"), syllabic laughter ("haha", "hahaha"),
	// and auxiliary negation truncation stems ("didn", "wasn") are evaluated on their canonical form
	// and never hijack the Primary Gain Tier.
	if collapsed := tokenzr.CollapseMarkovElongatedWord(tk); collapsed != "" && collapsed != tk {
		if StringShannonBits(collapsed) > 0.50*AtomicConceptEntropyBits || tokenzr.BPEInflectionValence(collapsed) >= 1 {
			return IsHighGainToken(collapsed)
		}
		return false
	}
	if tokenzr.IsBPEContractedAuxStem(tk) || tokenzr.IsBPEUninflectedTandemReduplication(tk) {
		return false
	}
	if tokenzr.IsBPEProperNounPrior(tk) {
		return true
	}
	b := StringShannonBits(tk)
	pr := tokenzr.TokenPriorBits(tk)
	val := tokenzr.BPEInflectionValence(tk)
	if b == 4.0 {
		return val >= 2 && pr >= 9.60
	}
	if b <= 6.0 && pr < 10.30 {
		return false
	}
	if (val >= 2 && pr >= 9.75) || (val >= 1 && pr >= 10.30) {
		return true
	}
	return pr >= 11.20 || (pr >= 11.00 && TokenInformationMass(tk) >= 10.30)
}

// StemToken computes the language-agnostic Shannon-bit proportional prefix stem.
func StemToken(tok string) string {
	t := strings.ToLower(strings.TrimSpace(tok))
	if t == "" {
		return t
	}
	var totalBits float64
	for _, r := range t {
		if !unicode.IsLetter(r) || IsContinuousSyllabicOrIdeographic(r) {
			return t
		}
		totalBits += RuneShannonBits(r)
	}
	if totalBits < 1.25*AtomicConceptEntropyBits {
		return t
	}
	targetBits := math.Floor((totalBits*2.0+2.0)/6.0) * 2.0
	if targetBits < AtomicConceptEntropyBits {
		targetBits = AtomicConceptEntropyBits
	} else if targetBits > 2.25*AtomicConceptEntropyBits {
		targetBits = 2.25 * AtomicConceptEntropyBits
	}
	var acc float64
	for idx, r := range t {
		acc += RuneShannonBits(r)
		if acc >= targetBits {
			return t[:idx+utf8.RuneLen(r)]
		}
	}
	return t
}

// MorphRootPrefix computes the second-order relaxed morphological root prefix (retaining >= AtomicConceptEntropyBits = 8.0 bits)
// by stepping down one trailing symbol's Shannon self-information mass (zero character-count checks).
func MorphRootPrefix(tok string) string {
	st := StemToken(tok)
	for _, r := range st {
		if !unicode.IsLetter(r) || IsContinuousSyllabicOrIdeographic(r) {
			return st
		}
	}
	bSt := StringShannonBits(st)
	if bSt > AtomicConceptEntropyBits {
		lastR, _ := utf8.DecodeLastRuneInString(st)
		target := bSt - RuneShannonBits(lastR)
		if target >= AtomicConceptEntropyBits {
			var acc float64
			for idx, r := range st {
				acc += RuneShannonBits(r)
				if acc >= target {
					return st[:idx+utf8.RuneLen(r)]
				}
			}
		}
	}
	return st
}

// Tokenize performs permissive, zero-stopword-table Unicode + tokenz-r 128k BPE tokenization
// and orders tokens by Shannon-BPE information gain (Primary Gain Tier first, Secondary Residual Tier second,
// compacted to MaxQueryTokens via SelectTopInformationTokens).
//
// Core invariant ("代码可以放宽但不能误杀 · 香农只负责增益"):
//   - Zero static stopword lists or word-level kill switches;
//   - All multi-character words/numbers (>= 4.0b Latin/Digit, >= 16.0b Han, >= 8.0b Kana) are preserved;
//   - High-gain tokens lead the slice so downstream top-K seed windows prioritize high-information terms without ever wiping low-mass idioms/homographs.
func Tokenize(q string) []string {
	q = CanonicalizeApostrophe(q)
	if strings.TrimSpace(q) == "" {
		return nil
	}
	seen := make(map[string]bool, 24)
	rawOut := make([]string, 0, 16)
	minCJKTokenBits := func(s string) float64 {
		if IsPureHan(s) {
			return 2.0 * AtomicConceptEntropyBits
		}
		return AtomicConceptEntropyBits
	}
	addClean := func(t string) {
		t = strings.ToLower(strings.TrimSpace(t))
		bT := StringShannonBits(t)
		if bT < 0.50*AtomicConceptEntropyBits || seen[t] || len(rawOut) >= 256 {
			return
		}
		if IsPureCJK(t) && bT < minCJKTokenBits(t) {
			return
		}
		seen[t] = true
		rawOut = append(rawOut, t)
	}
	if IsAnonymousStrangerLabel(q) {
		addClean(strings.ToLower(strings.TrimSpace(q)))
		return rawOut
	}

	const (
		kindNone     = 0
		kindLatin    = 1
		kindDigit    = 2
		kindCJK      = 3
		kindHangul   = 4
		kindCyrillic = 5
		kindGreek    = 6
		kindArabic   = 7
		kindHebrew   = 8
		kindBrahmic  = 9
		kindMark     = -1
	)
	classify := func(r rune) int {
		if r == '\u00ad' || unicode.Is(unicode.Cf, r) {
			return kindMark
		}
		if unicode.IsMark(r) {
			return kindMark
		}
		switch {
		case IsContinuousSyllabicOrIdeographic(r) && !unicode.Is(unicode.Hangul, r):
			return kindCJK
		case unicode.IsDigit(r):
			return kindDigit
		case unicode.Is(unicode.Hangul, r):
			return kindHangul
		case unicode.Is(unicode.Cyrillic, r):
			return kindCyrillic
		case unicode.Is(unicode.Greek, r):
			return kindGreek
		case unicode.Is(unicode.Arabic, r):
			return kindArabic
		case unicode.Is(unicode.Hebrew, r):
			return kindHebrew
		case unicode.Is(unicode.Latin, r):
			return kindLatin
		case unicode.IsLetter(r):
			return kindBrahmic
		default:
			return kindNone
		}
	}

	flushLatin := func(seg []rune) {
		if collapsed := tokenzr.CollapseMarkovElongatedWord(string(seg)); collapsed != "" {
			addClean(collapsed)
		}
		// 保留：低比特段抑制是**候选预算/噪声控制**（非语义裁决）；直接删除会令 150KB 单行切分退化为 3.2s
		// 且单字母 MCQ 选项被误判为说话人（实测）。转 Top-K-by-mass 预算属独立改造，见 V22 基线清单。
		if SliceShannonBits(seg) < 0.50*AtomicConceptEntropyBits {
			return
		}
		full := string(seg)
		addClean(full)
		start := 0
		for i := 1; i < len(seg); i++ {
			prev, cur, next := seg[i-1], seg[i], rune(0)
			if i+1 < len(seg) {
				next = seg[i+1]
			}
			if unicode.IsLower(prev) && unicode.IsUpper(cur) {
				if i > start {
					addClean(string(seg[start:i]))
				}
				start = i
			} else if unicode.IsUpper(prev) && unicode.IsUpper(cur) && unicode.IsLower(next) {
				if SliceShannonBits(seg[start:i]) >= 0.50*AtomicConceptEntropyBits {
					addClean(string(seg[start:i]))
					start = i
				} else if i+1 <= len(seg) {
					addClean(string(seg[start : i+1]))
				}
			}
		}
		if start > 0 && len(seg) > start {
			addClean(string(seg[start:]))
			upperEnd := start
			for upperEnd < len(seg) && unicode.IsUpper(seg[upperEnd]) {
				upperEnd++
			}
			if upperEnd > start && upperEnd < len(seg) && SliceShannonBits(seg[start:upperEnd]) >= 0.50*AtomicConceptEntropyBits {
				addClean(string(seg[start:upperEnd]))
			}
		}
	}

	flushCJK := func(seg []rune) {
		// 保留：整段抑制同属候选预算/噪声控制（见 flushLatin 注）；删除即触发分片成本与误判回归。
		bSeg := SliceShannonBits(seg)
		if bSeg < AtomicConceptEntropyBits {
			return
		}
		hasKana := false
		for _, r := range seg {
			if unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r) {
				hasKana = true
				break
			}
		}
		if hasKana {
			n := len(seg)
			hasKanjiOrKata := false
			for i := 0; i < n; {
				r := seg[i]
				switch {
				case unicode.Is(unicode.Katakana, r) || r == 'ー':
					hasKanjiOrKata = true
					j := i + 1
					for j < n && (unicode.Is(unicode.Katakana, seg[j]) || seg[j] == 'ー') {
						j++
					}
					if SliceShannonBits(seg[i:j]) >= AtomicConceptEntropyBits {
						addClean(string(seg[i:j]))
					}
					i = j
				case unicode.Is(unicode.Han, r):
					hasKanjiOrKata = true
					j := i + 1
					for j < n && unicode.Is(unicode.Han, seg[j]) {
						j++
					}
					hanRun := seg[i:j]
					bHan := SliceShannonBits(hanRun)
					if bHan >= 2.0*AtomicConceptEntropyBits {
						if bHan <= 5.0*AtomicConceptEntropyBits {
							addClean(string(hanRun))
						}
						if bHan > 3.75*AtomicConceptEntropyBits {
							hLeaves, hPhrases := tokenzr.SplitSubwordConstituents(string(hanRun))
							for _, ph := range hPhrases {
								addClean(ph)
							}
							for _, sub := range tokenzr.BPEContainedSubblocks(string(hanRun)) {
								addClean(sub)
							}
							for hi, lf := range hLeaves {
								if lf == " " {
									continue
								}
								bLf := StringShannonBits(lf)
								if bLf >= 2.0*AtomicConceptEntropyBits {
									addClean(lf)
								} else if bLf >= AtomicConceptEntropyBits && hi+1 < len(hLeaves) && hLeaves[hi+1] != " " {
									nxt := hLeaves[hi+1]
									bNxt := StringShannonBits(nxt)
									if bNxt >= 2.0*AtomicConceptEntropyBits && bLf+bNxt <= 3.0*AtomicConceptEntropyBits {
										addClean(lf + nxt)
									}
								}
							}
						}
						for k := 0; k+2 <= len(hanRun); k++ {
							addClean(string(hanRun[k : k+2]))
						}
					} else if bHan >= AtomicConceptEntropyBits && j < n && unicode.Is(unicode.Hiragana, seg[j]) {
						k := j
						for k < n && unicode.Is(unicode.Hiragana, seg[k]) && SliceShannonBits(seg[j:k]) < 1.5*AtomicConceptEntropyBits {
							k++
						}
						if k > j {
							addClean(string(seg[i:k]))
							if SliceShannonBits(seg[j:k]) >= AtomicConceptEntropyBits {
								addClean(string(seg[i : j+1]))
							}
						}
					}
					i = j
				default:
					i++
				}
			}
			if !hasKanjiOrKata && bSeg >= 1.5*AtomicConceptEntropyBits {
				addClean(string(seg))
				for _, sp := range tokenzr.SplitSubwords(string(seg)) {
					if StringShannonBits(sp) >= 1.5*AtomicConceptEntropyBits {
						addClean(sp)
					}
				}
			}
			return
		}
		full := string(seg)
		if bSeg <= 2.5*AtomicConceptEntropyBits {
			addClean(full)
			return
		}
		if bSeg <= 3.75*AtomicConceptEntropyBits {
			addClean(full)
			for rem := full; rem != ""; {
				_, w0 := utf8.DecodeRuneInString(rem)
				if w0 <= 0 || w0 >= len(rem) {
					break
				}
				_, w1 := utf8.DecodeRuneInString(rem[w0:])
				if w1 <= 0 {
					break
				}
				addClean(rem[:w0+w1])
				rem = rem[w0:]
			}
			return
		}
		leaves, phrases := tokenzr.SplitSubwordConstituents(full)
		for _, ph := range phrases {
			addClean(ph)
		}
		for i, lf := range leaves {
			if lf == " " {
				continue
			}
			bLf := StringShannonBits(lf)
			if bLf >= minCJKTokenBits(lf) && bLf <= 5.0*AtomicConceptEntropyBits {
				addClean(lf)
				if bLf >= 3.0*AtomicConceptEntropyBits {
					for rem := lf; rem != ""; {
						_, w0 := utf8.DecodeRuneInString(rem)
						if w0 <= 0 || w0 >= len(rem) {
							break
						}
						_, w1 := utf8.DecodeRuneInString(rem[w0:])
						if w1 <= 0 {
							break
						}
						addClean(rem[:w0+w1])
						rem = rem[w0:]
					}
				}
				continue
			}
			// Adjacent 1-character Han leaf bigram synthesis (never crosses BPE >=2-char constituents or super-hub boundaries)
			if bLf >= AtomicConceptEntropyBits && bLf < 2.0*AtomicConceptEntropyBits && i+1 < len(leaves) && leaves[i+1] != " " {
				nxt := leaves[i+1]
				bNxt := StringShannonBits(nxt)
				if bNxt >= AtomicConceptEntropyBits && bNxt < 2.0*AtomicConceptEntropyBits {
					addClean(lf + nxt)
				} else if bNxt >= 2.0*AtomicConceptEntropyBits && i == 0 {
					r0, w0 := utf8.DecodeRuneInString(nxt)
					if w0 > 0 && unicode.Is(unicode.Han, r0) {
						addClean(lf + nxt[:w0])
					}
				}
			}
		}
	}

	var alnumRun []rune
	flushAlnumRun := func() {
		if len(alnumRun) == 0 {
			return
		}
		hasLat, hasDig := false, false
		for _, r := range alnumRun {
			if unicode.IsDigit(r) {
				hasDig = true
			} else if unicode.IsLetter(r) && !IsContinuousSyllabicOrIdeographic(r) {
				hasLat = true
			}
		}
		if hasLat && hasDig && SliceShannonBits(alnumRun) <= 6.0*AtomicConceptEntropyBits {
			addClean(string(alnumRun))
			if len(alnumRun) > 2 && alnumRun[0] == '0' && (alnumRun[1] == 'x' || alnumRun[1] == 'X') &&
				SliceShannonBits(alnumRun[2:]) >= 0.50*AtomicConceptEntropyBits {
				addClean(string(alnumRun[2:]))
			}
			start := 0
			seenDig := false
			for i := 0; i < len(alnumRun); i++ {
				if unicode.IsDigit(alnumRun[i]) {
					seenDig = true
				}
				if i > start && seenDig && unicode.IsUpper(alnumRun[i]) &&
					(unicode.IsLower(alnumRun[i-1]) || unicode.IsDigit(alnumRun[i-1])) {
					if SliceShannonBits(alnumRun[start:i]) >= 0.50*AtomicConceptEntropyBits {
						addClean(string(alnumRun[start:i]))
					}
					start = i
					seenDig = false
				}
			}
			if start > 0 && start < len(alnumRun) && SliceShannonBits(alnumRun[start:]) >= 0.50*AtomicConceptEntropyBits {
				addClean(string(alnumRun[start:]))
			}
		}
		alnumRun = alnumRun[:0]
	}

	var curSeg []rune
	var prevTightAlnum []rune
	var prevTightHan []rune
	curKind := kindNone
	flushSeg := func(nextKind int) {
		if len(curSeg) == 0 {
			return
		}
		switch curKind {
		case kindLatin, kindHangul, kindCyrillic, kindGreek, kindArabic, kindHebrew, kindBrahmic:
			flushLatin(curSeg)
			if curKind == kindLatin && nextKind != kindDigit && len(prevTightHan) > 0 {
				bLat := SliceShannonBits(curSeg)
				if bLat >= 0.25*AtomicConceptEntropyBits && bLat < 0.50*AtomicConceptEntropyBits {
					if nextKind == kindNone {
						addClean(string(prevTightHan[len(prevTightHan)-1:]) + string(curSeg))
						bPrevHan := SliceShannonBits(prevTightHan)
						if bPrevHan >= 2.0*AtomicConceptEntropyBits && bPrevHan <= 3.75*AtomicConceptEntropyBits {
							addClean(string(prevTightHan) + string(curSeg))
						}
					}
					if len(prevTightHan) >= 3 {
						tail3 := string(prevTightHan[len(prevTightHan)-3:])
						if tokenzr.IsBPESingleToken(tail3) {
							addClean(tail3 + string(curSeg))
						}
					}
				}
			}
			prevTightHan = prevTightHan[:0]
		case kindDigit:
			if SliceShannonBits(curSeg) > 2.0 {
				addClean(string(curSeg))
			}
			prevTightHan = prevTightHan[:0]
		case kindCJK:
			if len(prevTightAlnum) > 0 && unicode.Is(unicode.Han, curSeg[0]) {
				bPrev := SliceShannonBits(prevTightAlnum)
				bHan := SliceShannonBits(curSeg)
				if bPrev >= 0.25*AtomicConceptEntropyBits && bPrev <= AtomicConceptEntropyBits {
					addClean(string(prevTightAlnum) + string(curSeg[:1]))
					if bPrev < 0.50*AtomicConceptEntropyBits && bHan >= 2.0*AtomicConceptEntropyBits && unicode.Is(unicode.Han, curSeg[1]) {
						addClean(string(prevTightAlnum) + string(curSeg[:2]))
					}
				}
			}
			prevTightAlnum = prevTightAlnum[:0]
			if unicode.Is(unicode.Han, curSeg[len(curSeg)-1]) {
				prevTightHan = append(prevTightHan[:0], curSeg...)
			} else {
				prevTightHan = prevTightHan[:0]
			}
			flushCJK(curSeg)
		}
		curSeg = curSeg[:0]
	}

	for _, r := range q {
		k := classify(r)
		if k == kindMark {
			if curKind != kindNone && len(curSeg) > 0 {
				curSeg = append(curSeg, r)
				if len(alnumRun) > 0 {
					alnumRun = append(alnumRun, r)
				}
			}
			continue
		}
		if k == kindLatin || k == kindDigit {
			alnumRun = append(alnumRun, r)
		} else {
			if k == kindCJK && len(alnumRun) > 0 {
				prevTightAlnum = append(prevTightAlnum[:0], alnumRun...)
			}
			flushAlnumRun()
		}
		if k == kindNone {
			flushSeg(kindNone)
			prevTightAlnum = prevTightAlnum[:0]
			prevTightHan = prevTightHan[:0]
			curKind = kindNone
			continue
		}
		if k != curKind {
			flushSeg(k)
			if k != kindCJK {
				prevTightAlnum = prevTightAlnum[:0]
			}
			curKind = k
		}
		curSeg = append(curSeg, r)
	}
	flushSeg(kindNone)
	flushAlnumRun()

	// Preserve joined forms for apostrophe-embedded names ("Xi'an" -> "xian", "O'Neal" -> "oneal", "L'Hôpital" -> "lhôpital"),
	// hyphenated compounds ("well-being" -> "wellbeing", "state-of-the-art" -> "stateoftheart", "UTF-8" -> "utf8", "SHA-256" -> "sha256"),
	// and snake_case code identifiers ("max_query_tokens" -> "max_query_tokens" + "maxquerytokens", "x86_64" -> "x86_64" + "x8664"),
	// governed purely by Shannon bit capacity (zero character/byte length magic caps).
	maxCompoundBits := math.Ceil(math.E*math.E) * AtomicConceptEntropyBits
	for _, span := range strings.FieldsFunc(q, func(r rune) bool {
		return !(unicode.IsLetter(r) && !IsContinuousSyllabicOrIdeographic(r)) && !unicode.IsDigit(r) && r != '\'' && r != '-' && r != '_' && r != '.'
	}) {
		span = strings.Trim(span, "'-_.")
		hasApostrophe := strings.ContainsRune(span, '\'')
		hasHyphen := strings.ContainsRune(span, '-')
		hasUnderscore := strings.ContainsRune(span, '_')
		hasDot := strings.ContainsRune(span, '.')
		if hasDot {
			if hasHyphen && !hasApostrophe && !hasUnderscore {
				if bSpan := StringShannonBits(span); bSpan >= AtomicConceptEntropyBits && bSpan <= maxCompoundBits {
					addClean(strings.ToLower(span))
					if hIdx := strings.IndexByte(span, '-'); hIdx > 0 && strings.ContainsRune(span[:hIdx], '.') {
						addClean(strings.ToLower(span[:hIdx]))
					}
				}
			} else if !hasHyphen && !hasApostrophe && !hasUnderscore {
				parts := strings.Split(span, ".")
				if len(parts) >= 2 && len(parts) <= int(math.Ceil(math.E*math.E)) {
					allLetters := true
					hasSingleLetter := false
					var joined strings.Builder
					for _, p := range parts {
						if p == "" {
							allLetters = false
							break
						}
						for _, pr := range p {
							if !unicode.IsLetter(pr) || IsContinuousSyllabicOrIdeographic(pr) {
								allLetters = false
								break
							}
						}
						bPart := StringShannonBits(p)
						if !(bPart >= 0.25*AtomicConceptEntropyBits && bPart <= 0.75*AtomicConceptEntropyBits) {
							allLetters = false
							break
						}
						if !(bPart >= 0.50*AtomicConceptEntropyBits) {
							hasSingleLetter = true
						}
						joined.WriteString(strings.ToLower(p))
					}
					if allLetters && hasSingleLetter {
						bJ := StringShannonBits(joined.String())
						if bJ >= 0.50*AtomicConceptEntropyBits && bJ <= maxCompoundBits {
							addClean(joined.String())
						}
					}
				}
			}
			continue
		}
		if hasApostrophe && !hasHyphen && !hasUnderscore {
			parts := strings.Split(span, "'")
			if len(parts) >= 2 && StringShannonBits(parts[0]) >= 2.0 && StringShannonBits(parts[len(parts)-1]) >= 0.50*AtomicConceptEntropyBits {
				// Skip grammatical clitic contractions ("we've" through "you're") whose joined form would
				// fabricate an OOV pseudo-name, while preserving apostrophe-embedded proper names
				// ("Xi'an" -> "xian", "O'Neal" -> "oneal", "L'Hôpital" -> "lhôpital").
				if len(parts) == 2 && tokenzr.IsBPECliticTail(parts[1]) && parts[1] == strings.ToLower(parts[1]) {
					continue
				}
				var joined strings.Builder
				for _, p := range parts {
					joined.WriteString(strings.ToLower(p))
				}
				if bJoined := StringShannonBits(joined.String()); bJoined >= AtomicConceptEntropyBits && bJoined <= maxCompoundBits {
					addClean(joined.String())
				}
			}
		} else if (hasHyphen || hasUnderscore) && !hasApostrophe {
			parts := strings.FieldsFunc(span, func(r rune) bool {
				return r == '-' || r == '_'
			})
			hasLetter := false
			for _, r := range span {
				if unicode.IsLetter(r) {
					hasLetter = true
					break
				}
			}
			if hasLetter && len(parts) >= 2 && len(parts) <= int(math.Ceil(math.E*math.E)) {
				var joined strings.Builder
				for _, p := range parts {
					joined.WriteString(strings.ToLower(p))
				}
				bJoined := StringShannonBits(joined.String())
				if bJoined >= AtomicConceptEntropyBits && bJoined <= maxCompoundBits {
					if hasUnderscore && !hasHyphen {
						addClean(strings.ToLower(span))
					}
					addClean(joined.String())
				}
			}
		}
	}

	// Rejoin OCR line-break hyphenated words (e.g. "dis-\nassembled" -> "disassembled", "accom-\r\nmodation" -> "accommodation")
	if strings.ContainsRune(q, '-') && (strings.ContainsRune(q, '\n') || strings.ContainsRune(q, '\r')) {
		qRunes := []rune(q)
		for i := 1; i+2 < len(qRunes); i++ {
			if qRunes[i] == '-' && (qRunes[i+1] == '\n' || qRunes[i+1] == '\r') && unicode.IsLower(qRunes[i-1]) {
				rStart := i + 1
				for rStart < len(qRunes) && (qRunes[rStart] == '\n' || qRunes[rStart] == '\r' || qRunes[rStart] == ' ' || qRunes[rStart] == '\t') {
					rStart++
				}
				if rStart < len(qRunes) && unicode.IsLower(qRunes[rStart]) {
					lStart := i - 1
					for lStart > 0 && unicode.IsLetter(qRunes[lStart-1]) && !IsContinuousSyllabicOrIdeographic(qRunes[lStart-1]) {
						lStart--
					}
					rEnd := rStart + 1
					for rEnd < len(qRunes) && unicode.IsLetter(qRunes[rEnd]) && !IsContinuousSyllabicOrIdeographic(qRunes[rEnd]) {
						rEnd++
					}
					rejoined := strings.ToLower(string(qRunes[lStart:i]) + string(qRunes[rStart:rEnd]))
					if bRej := StringShannonBits(rejoined); bRej >= AtomicConceptEntropyBits && bRej <= maxCompoundBits {
						addClean(rejoined)
					}
				}
			}
		}
	}

	// Gain-based ordering ("香农与 BPE 只负责增益，不负责一刀切生死"):
	// Primary Gain Tier tokens lead in their original relative order; Secondary Residual Tier tokens follow.
	if len(rawOut) <= MaxQueryTokens {
		primary := make([]string, 0, len(rawOut))
		secondary := make([]string, 0, len(rawOut))
		for _, tk := range rawOut {
			if IsHighGainToken(tk) {
				primary = append(primary, tk)
			} else {
				secondary = append(secondary, tk)
			}
		}
		return append(primary, secondary...)
	}
	// Long query (> MaxQueryTokens) compaction: preserve the tail question window (tailN = 3/4 of MaxQueryTokens),
	// rescue high-gain mid-section anchors by TokenInformationMass, and fill remaining head slots by gain.
	out := rawOut
	tailN := (MaxQueryTokens * 3) / 4
	headN := MaxQueryTokens - tailN
	mid := out[headN : len(out)-tailN]
	midMass := make([]float64, len(mid))
	for i, tk := range mid {
		midMass[i] = TokenInformationMass(tk)
	}
	maxRescue := 2
	lowHeadCnt := 0
	for _, htk := range out[:headN] {
		if TokenInformationMass(htk) < 1.5*AtomicConceptEntropyBits {
			lowHeadCnt++
		}
	}
	if lowHeadCnt > 0 && len(mid) >= 3 {
		extra := min(2, lowHeadCnt)
		maxRescue = 2 + extra
	}
	var midRescue []string
	usedMid := make([]bool, len(mid))
	for len(midRescue) < maxRescue {
		minMass := 1.5 * AtomicConceptEntropyBits
		if len(midRescue) >= 2 {
			minMass = 2.0 * AtomicConceptEntropyBits
		}
		bestIdx := -1
		bestBits := minMass
		for i, b := range midMass {
			if usedMid[i] {
				continue
			}
			if b >= bestBits && (bestIdx < 0 || b > bestBits) {
				bestIdx = i
				bestBits = b
			}
		}
		if bestIdx < 0 {
			break
		}
		usedMid[bestIdx] = true
		midRescue = append(midRescue, mid[bestIdx])
	}
	extraRescued := max(0, len(midRescue)-2)
	actualHeadN := headN - extraRescued
	actualTailN := MaxQueryTokens - actualHeadN - len(midRescue)
	compacted := make([]string, 0, MaxQueryTokens)
	compacted = append(compacted, out[len(out)-actualTailN:]...)
	compacted = append(compacted, midRescue...)
	compacted = append(compacted, SelectTopInformationTokens(out[:headN], actualHeadN)...)
	return compacted
}

// AtomInitialEnergyNats computes the intrinsic initial prior energy E_0(a) (in Nats) of an atom
// determined jointly by its Shannon Unicode script capacity and its 128k BPE Pre-Space Zipfian prior:
//
//	E_0(a) = ln(2) * ( (1 - 1/e)*EffectiveLexicalBits(a) + (1/e)*StringShannonBits(a) + Δ_rigid(a) )
//
// In N=1 cold-start states where empirical frequency distributions are flat, E_0(a) serves as the
// universal ignition prior (0-Gate continuous gain). As the manifold grows (N -> inf), E_0(a) is
// smoothly conjugated with online manifold field potential U_field(a) via Bayesian weight α_N = N/(N+e^2).
func AtomInitialEnergyNats(tk string) float64 {
	s := strings.TrimSpace(tk)
	if s == "" {
		return 0
	}
	bChar := StringShannonBits(s)
	if bChar <= 0 {
		return 0
	}
	bBPE := tokenzr.EffectiveLexicalBits(s)
	invE := 1.0 / math.E
	bits := bChar
	if bBPE > 0 {
		bits = (1.0-invE)*bBPE + invE*bChar
	}
	if tokenzr.IsBPEProperNounPrior(s) {
		bits += math.E
	}
	return bits * math.Ln2
}

// PureShannonStructuralMass computes the 100% BPE-free structural self-information mass (in bits)
// using only Unicode script Shannon capacity, proportional stem differential, and rigid character form flags.
func PureShannonStructuralMass(tk string) float64 {
	s := strings.TrimSpace(tk)
	if s == "" {
		return 0
	}
	bChar := StringShannonBits(s)
	if bChar <= 0 {
		return 0
	}
	hasRigid := false
	hasLower := false
	for i, r := range s {
		if unicode.IsDigit(r) || r == '-' || r == '_' {
			hasRigid = true
		} else if unicode.IsUpper(r) {
			if i > 0 && hasLower {
				hasRigid = true
			}
			hasRigid = true
		} else if unicode.IsLower(r) {
			hasLower = true
		}
	}
	invE := 1.0 / math.E
	mass := bChar
	if st := StemToken(s); st != "" && st != strings.ToLower(s) {
		bSt := StringShannonBits(st)
		if bChar > bSt {
			mass += invE * (bChar - bSt)
		}
	}
	if hasRigid {
		mass *= 1.0 + invE
	}
	return mass
}

// IsPureShannonHighGainToken reports whether tk belongs to the Primary Gain Tier using 100% BPE-free
// pure Shannon structural capacity (used strictly for ordering priority, never for deleting tokens).
func IsPureShannonHighGainToken(tk string) bool {
	s := strings.TrimSpace(tk)
	if s == "" {
		return false
	}
	if d := strings.TrimPrefix(s, "#"); d != "" && d[0] >= '0' && d[0] <= '9' {
		return true
	}
	for _, r := range s {
		if unicode.IsDigit(r) || unicode.IsUpper(r) {
			return true
		}
	}
	if IsPureCJK(s) {
		return StringShannonBits(s) >= 2.0*AtomicConceptEntropyBits
	}
	return PureShannonStructuralMass(s) >= 1.25*AtomicConceptEntropyBits
}

// OnlineDFConjugateBits computes the lightweight Bayesian cold-to-warm conjugate self-information (in bits)
// driven solely by online document frequency (df, totalDocs) and literal Shannon capacity bChar (zero heavy manifold operators).
func OnlineDFConjugateBits(bChar, coldBits float64, df, totalDocs int) float64 {
	if coldBits <= 0 {
		coldBits = bChar
	}
	if totalDocs <= 0 || df <= 0 {
		return coldBits
	}
	nFloat := float64(totalDocs)
	dFloat := float64(df)
	if dFloat > nFloat {
		dFloat = nFloat
	}
	eSq := math.E * math.E
	invE := 1.0 / math.E
	alphaN := nFloat / (nFloat + eSq)

	empBits := math.Log2((nFloat + 1.0) / (dFloat + 0.5))
	if empBits < 0 {
		empBits = 0
	}
	baseCarrier := math.Pow(math.Max(1.0, bChar), invE) * math.E
	warmBits := baseCarrier + 0.5*math.E*empBits

	priorWeight := 1.0 - alphaN
	if dFloat >= 2.0 && coldBits > warmBits {
		diff := coldBits - warmBits
		conflictZ2 := (dFloat - 1.0) * diff * diff
		priorWeight = priorWeight / (1.0 + conflictZ2)
	}
	return priorWeight*coldBits + (1.0-priorWeight)*warmBits
}
