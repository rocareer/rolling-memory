// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package memcore

import (
	"math"
	"sort"
	"strings"
	"unicode"

	"github.com/rocareer/rolling-memory/internal/tokenzr"
)

// OrderedStemSeq extracts the ordered sequence of high-gain lexical stems (>= 4.0 Shannon bits) from label
// using tokenz-r 128k BPE gain (IsHighGainToken) and Shannon-bit proportional prefix stemming
// (zero stopword tables, zero language-specific suffix rules, zero character-count thresholds).
func OrderedStemSeq(label string) []string {
	toks := Tokenize(CanonicalizeApostrophe(label))
	if len(toks) == 0 {
		return nil
	}
	out := make([]string, 0, len(toks))
	for _, t := range toks {
		if !IsHighGainToken(t) {
			continue
		}
		st := StemToken(t)
		if st == "" {
			st = t
		}
		if StringShannonBits(st) >= 4.0 {
			out = append(out, st)
		}
	}
	return out
}

// CanonicalRelationQuotientKey computes a 100% language-agnostic, zero-wordlist canonical stem key
// for a relation predicate label using its content-stem sequence (or proportional Shannon-bit stem
// when all tokens are closed-class function words). This projects inflected or particle-decorated
// predicates ("works at", "worked at", "working at") onto the same morphological quotient key ("work")
// while preserving the verbatim surface label on every assertion row.
func CanonicalRelationQuotientKey(relLabel string) string {
	s := strings.ToLower(strings.TrimSpace(CanonicalizeApostrophe(relLabel)))
	if s == "" {
		return ""
	}
	if stems := OrderedStemSeq(s); len(stems) > 0 {
		return strings.Join(stems, " ")
	}
	raw := strings.FieldsFunc(s, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
	if len(raw) == 0 {
		return s
	}
	parts := make([]string, 0, len(raw))
	for _, w := range raw {
		st := StemToken(w)
		if st == "" {
			st = w
		}
		parts = append(parts, st)
	}
	return strings.Join(parts, " ")
}

// AreRelationsMorphologicallyEquivalent determines whether two relation predicate labels belong
// to the same morphological/structural quotient class using pure Shannon Self-Information Bit-Field
// similarity (zero wordlists, zero language-specific suffix rules):
//  1. Exact case-insensitive or CanonicalRelationQuotientKey equality;
//  2. Single-stem predicates whose Shannon Bit-Field similarity MorphSimWordPair >= 0.85
//     (and shared root entropy >= AtomicConceptEntropyBits = 8.0 bits);
//  3. Multi-stem predicates where all content stems align in order with MorphSimWordPair >= 0.85.
func AreRelationsMorphologicallyEquivalent(relA, relB string) bool {
	a := strings.ToLower(strings.TrimSpace(CanonicalizeApostrophe(relA)))
	b := strings.ToLower(strings.TrimSpace(CanonicalizeApostrophe(relB)))
	if a == "" || b == "" {
		return false
	}
	if a == b {
		return true
	}
	// 纯 CJK 关系词无词头屈折变化：若左首字不同（LCPRunes == 0，如「信任」vs「不再信任」、「启用」vs「未启用」），
	// 必含不同前缀修饰或否定极性，严禁因低增益前缀被滤除而误判为形态等价（治 TestWriteSideLLMExplicitOnlyBoundaryAdversarialDB）。
	if IsPureCJK(a) && IsPureCJK(b) && LCPRunes([]rune(a), []rune(b)) == 0 {
		return false
	}
	if !strings.Contains(a, " ") && !strings.Contains(b, " ") {
		if ra, rb := []rune(a), []rune(b); SliceShannonBits(ra) >= AtomicConceptEntropyBits &&
			SliceShannonBits(rb) >= AtomicConceptEntropyBits &&
			SliceShannonBits(ra[:LCPRunes(ra, rb)]) >= AtomicConceptEntropyBits &&
			(MorphSimWordPair(ra, []rune(StemToken(a)), rb) >= 0.85 ||
				MorphSimWordPair(rb, []rune(StemToken(b)), ra) >= 0.85) {
			return true
		}
	}
	// Compare high-gain content tokens via MorphSimWordPair (with left-anchored bLCP >= AtomicConceptEntropyBits)
	// rather than pre-truncated 4-letter StemToken prefixes, preventing 5-letter homograph stem collisions
	// ("reach"<->"react", "peach"<->"peace", "child"<->"chill", "story"<->"store"<->"storm", "train"<->"trail",
	// "plant"<->"plane", "think"<->"thing", "class"<->"clash").
	tokA := highGainTokens(a)
	tokB := highGainTokens(b)
	if len(tokA) == 0 && len(tokB) == 0 {
		ka := CanonicalRelationQuotientKey(a)
		kb := CanonicalRelationQuotientKey(b)
		return ka != "" && ka == kb
	}
	if len(tokA) == 0 || len(tokA) != len(tokB) {
		return false
	}
	// 防低增益反义/方位/介词差异被 highGainTokens 剥离后误判为形态等价谓词（如 "worked with" vs "worked without"、"跳进" vs "跳出"）：
	// 当原串存在低增益修饰词元（rawToks > highGainTokens）时，要求两侧的非高增益残余词元集合对称相等（除非仅相差单个 <=4.0b 的屈折/完成体语法助词如 "了/过/to/at/in" 且另一侧无对立介词）。
	rawA := Tokenize(a)
	rawB := Tokenize(b)
	if len(rawA) > len(tokA) && len(rawB) > len(tokB) {
		lowSetA := map[string]bool{}
		for _, t := range rawA {
			if !IsHighGainToken(t) {
				lowSetA[t] = true
			}
		}
		for _, t := range rawB {
			if !IsHighGainToken(t) && !lowSetA[t] {
				return false
			}
		}
	}
	for i := range tokA {
		if tokA[i] == tokB[i] {
			continue
		}
		ra, rb := []rune(tokA[i]), []rune(tokB[i])
		bA, bB := SliceShannonBits(ra), SliceShannonBits(rb)
		if bA < AtomicConceptEntropyBits || bB < AtomicConceptEntropyBits ||
			SliceShannonBits(ra[:LCPRunes(ra, rb)]) < AtomicConceptEntropyBits {
			return false
		}
		stA := StemToken(tokA[i])
		stB := StemToken(tokB[i])
		siblingInflection := stA != "" && stA == stB && math.Abs(math.Min(bA, bB)-1.25*AtomicConceptEntropyBits) < 1e-6 &&
			math.Abs(math.Max(bA, bB)-1.50*AtomicConceptEntropyBits) < 1e-6
		if !siblingInflection &&
			MorphSimWordPair(ra, []rune(stA), rb) < 0.85 &&
			MorphSimWordPair(rb, []rune(stB), ra) < 0.85 {
			return false
		}
	}
	return true
}

func highGainTokens(s string) []string {
	raw := Tokenize(s)
	out := make([]string, 0, len(raw))
	for _, t := range raw {
		if IsHighGainToken(t) {
			out = append(out, t)
		}
	}
	return out
}

// LabelsShareContentStemOrSubstring determines whether two labels (such as a unary state predicate
// "上大学" / "went to university" and a specific object entity "清华大学" / "Stanford University")
// share a content concept kernel carrying >= AtomicConceptEntropyBits (8.0 Shannon bits)
// using pure Shannon Self-Information Bit-Field algebra (zero wordlists, zero character-count thresholds, 100% language-agnostic).
func LabelsShareContentStemOrSubstring(labelA, labelB string) bool {
	a := strings.ToLower(strings.TrimSpace(CanonicalizeApostrophe(labelA)))
	b := strings.ToLower(strings.TrimSpace(CanonicalizeApostrophe(labelB)))
	if a == "" || b == "" {
		return false
	}
	if a == b {
		return true
	}
	tokA := Tokenize(a)
	tokB := Tokenize(b)
	type bSpec struct {
		tok   string
		runes []rune
		bits  float64
		ideo  bool
		stem  []rune
	}
	var specsB []bSpec
	for _, tb := range tokB {
		bBits := StringShannonBits(tb)
		if bBits < 6.0 {
			continue
		}
		specsB = append(specsB, bSpec{
			tok:   tb,
			runes: []rune(tb),
			bits:  bBits,
			ideo:  HasIdeographRune(tb),
			stem:  []rune(StemToken(tb)),
		})
	}
	for _, ta := range tokA {
		aBits := StringShannonBits(ta)
		if aBits < 6.0 {
			continue
		}
		ra := []rune(ta)
		aIdeo := HasIdeographRune(ta)
		var stARunes []rune
		for i := range specsB {
			sb := &specsB[i]
			tb := sb.tok
			isIdeo := aIdeo || sb.ideo
			if ta == tb || (isIdeo && (strings.Contains(ta, tb) || strings.Contains(tb, ta))) {
				sharedBits := aBits
				if sb.bits < aBits {
					sharedBits = sb.bits
				}
				if sharedBits >= AtomicConceptEntropyBits {
					return true
				}
			}
			if aBits >= AtomicConceptEntropyBits && sb.bits >= AtomicConceptEntropyBits &&
				SliceShannonBits(ra[:LCPRunes(ra, sb.runes)]) >= AtomicConceptEntropyBits {
				if stARunes == nil {
					stARunes = []rune(StemToken(ta))
				}
				if MorphSimWordPair(ra, stARunes, sb.runes) >= 0.85 ||
					MorphSimWordPair(sb.runes, sb.stem, ra) >= 0.85 {
					return true
				}
			}
		}
	}
	// Continuous ideographic/syllabic script Shannon-bit kernel overlap (>= AtomicConceptEntropyBits = 8.0 bits, zero character counting):
	var ideosA []rune
	for _, r := range []rune(a) {
		if IsContinuousSyllabicOrIdeographic(r) {
			ideosA = append(ideosA, r)
		}
	}
	if SliceShannonBits(ideosA) >= AtomicConceptEntropyBits && strings.IndexFunc(b, IsContinuousSyllabicOrIdeographic) >= 0 {
		for i := range ideosA {
			var accBits float64
			for j := i; j < len(ideosA); j++ {
				accBits += RuneShannonBits(ideosA[j])
				if accBits >= AtomicConceptEntropyBits {
					sub := string(ideosA[i : j+1])
					if len(Tokenize(sub)) > 0 && strings.Contains(b, sub) {
						return true
					}
					break
				}
			}
		}
	}
	return false
}

// ContentInformationPurity computes the language-agnostic Shannon-BPE information gain ratio
// of a node label using tokenz-r 128k BPE subword priors and StringShannonBits (zero stopword tables):
//   - contentBits: sum of Shannon bits over high-gain lexical segments/subwords (IsHighGainToken);
//   - rawBits: sum of Shannon bits over all lexical segments;
//   - purity: contentBits / rawBits in [0, 1].
//
// Core invariant ("代码可以放宽但不能误杀 · 香农与 BPE 只负责增益，不负责正确和确定"):
// Used strictly as a continuous gain / priority metric, never as a binary stopword kill switch.
func ContentInformationPurity(label string) (contentBits, rawBits, purity float64) {
	s := strings.TrimSpace(CanonicalizeApostrophe(label))
	if s == "" {
		return 0, 0, 0
	}
	segs := strings.FieldsFunc(s, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r) && r != '\'' && r != '-'
	})
	for _, seg := range segs {
		seg = strings.Trim(seg, "'-")
		if seg == "" {
			continue
		}
		b := StringShannonBits(seg)
		rawBits += b
		if idx := strings.IndexByte(seg, '\''); idx > 0 {
			base := seg[:idx]
			bBase := StringShannonBits(base)
			if IsHighGainToken(strings.ToLower(base)) {
				contentBits += bBase
			}
			continue
		}
		lo := strings.ToLower(seg)
		if IsPureCJK(lo) {
			subs := tokenzr.SplitSubwords(lo)
			for _, sub := range subs {
				if IsPronounSurface(sub) {
					continue
				}
				bSub := StringShannonBits(sub)
				if bSub >= 2.0*AtomicConceptEntropyBits {
					if IsHighGainToken(sub) {
						allFuncRunes := len(subs) > 1
						if allFuncRunes {
							for _, r := range sub {
								if !IsPronounSurface(string(r)) {
									allFuncRunes = false
									break
								}
							}
						}
						if !allFuncRunes {
							contentBits += bSub
						}
					}
				} else if bSub >= AtomicConceptEntropyBits && tokenzr.TokenPriorBits(sub) >= 9.96 {
					contentBits += bSub
				}
			}
			continue
		}
		if IsHighGainToken(lo) {
			contentBits += b
		}
	}
	if rawBits <= 0 {
		return 0, 0, 0
	}
	if contentBits > rawBits {
		contentBits = rawBits
	}
	return contentBits, rawBits, contentBits / rawBits
}

func hasNonStopwordUpperToken(label string) bool {
	for _, w := range strings.Fields(label) {
		w = strings.Trim(w, "\"'“”‘’.,!?;:()[]{}")
		rs := []rune(w)
		if len(rs) == 0 || !unicode.IsUpper(rs[0]) {
			continue
		}
		lo := strings.ToLower(CanonicalizeApostrophe(w))
		if !IsHighGainToken(lo) {
			continue
		}
		return true
	}
	return false
}

// IsColloquialFragmentLabel reports whether label is a function-word-wrapped phrase/clause
// using pure Shannon self-information conservation (zero wordlists, zero suffix rules, zero word-count rules):
//   - Excludes single tokens without spaces, technical identifiers, and proper-capitalized named entities;
//   - Returns true iff rawBits > contentBits (purity < 1.0, i.e., non-zero closed-class function-word shell entropy)
//     or contentBits < AtomicConceptEntropyBits.
//
// In graph topology, multi-edge concept hubs ("the script", "sense of purpose") have degree >= 2 and are preserved
// as intermediate Hasse hubs, whereas degree-1 pendant leaves with IsColloquialFragmentLabel == true are anchored
// to their co-occurring assertion host ("colloquial-host").
func IsColloquialFragmentLabel(label string) bool {
	s := strings.TrimSpace(CanonicalizeApostrophe(label))
	if s == "" || !strings.Contains(s, " ") || strings.ContainsAny(s, "/_():[]{}<>+#0123456789") {
		return false
	}
	if hasNonStopwordUpperToken(s) {
		return false
	}
	contentBits, rawBits, purity := ContentInformationPurity(s)
	if rawBits <= 0 {
		return false
	}
	stems := OrderedStemSeq(s)
	if len(stems) == 0 || contentBits < AtomicConceptEntropyBits {
		return true
	}
	return purity < 0.999
}

// SubphraseNodeInput represents a referent (or slot-occupying proposition) node candidate
// for Hasse multi-level sub-phrase/entity hub construction.
type SubphraseNodeInput struct {
	ID         uint64
	Label      string
	Stems      []string
	FieldCount int
	IsPure     bool
	IsPoss     bool
	IsSlotProp bool // true when kind=='assertion' occupying a subject_id/object_id slot (acts only as ChildID hub, never ParentID)
}

// SubphraseLinkOutput represents a directed Hasse edge (ChildID --subtype_of--> ParentID) with its formal evidence tag.
type SubphraseLinkOutput struct {
	ChildID  uint64
	ParentID uint64
	Evidence string // "stem-suffix", "stem-prefix", "stem-subseq", or "stem-entity"
}

// registerSingleStemBitFieldKeys indexes a 1-stem concept into m using its proportional Shannon-bit stem,
// its second-order relaxed morphological root prefix MorphRootPrefix (>= AtomicConceptEntropyBits = 8.0 bits),
// and an upward +2.0-bit Shannon prefix step on rawWord when rawWord carries >= 4.0 bits of inflected suffix
// beyond stem (bridging cross-boundary morphological inflections on a pure Shannon self-information grid,
// with zero language-specific suffix stripping and zero character-count thresholds).
func registerSingleStemBitFieldKeys(m map[string]uint64, id uint64, rawWord, stem string) {
	if id == 0 || stem == "" {
		return
	}
	if m[stem] == 0 {
		m[stem] = id
	}
	if root := MorphRootPrefix(rawWord); root != "" && root != stem && StringShannonBits(root) >= AtomicConceptEntropyBits {
		if m[root] == 0 {
			m[root] = id
		}
	}
	loRaw := strings.ToLower(strings.TrimSpace(CanonicalizeApostrophe(rawWord)))
	stemBits := StringShannonBits(stem)
	rawBits := StringShannonBits(loRaw)
	if stemBits >= AtomicConceptEntropyBits && rawBits >= stemBits+4.0 {
		targetBits := stemBits + 2.0
		acc := 0.0
		var b strings.Builder
		for _, r := range loRaw {
			if !unicode.IsLetter(r) && !unicode.IsNumber(r) {
				break
			}
			b.WriteRune(r)
			acc += StringShannonBits(string(r))
			if acc >= targetBits {
				cand := b.String()
				if cand != stem && cand != loRaw && m[cand] == 0 {
					m[cand] = id
				}
				break
			}
		}
	}
}

func stemLookupInMap(m map[string]uint64, stem string) uint64 {
	if id := m[stem]; id > 0 {
		return id
	}
	if root := MorphRootPrefix(stem); root != "" && root != stem && StringShannonBits(root) >= AtomicConceptEntropyBits {
		return m[root]
	}
	return 0
}

// isThinShellCompoundConcept reports whether a multi-stem phrase has high Shannon information purity
// (shellBits <= 8.0 bits, no clause punctuation) and starts/ends with content tokens rather than stopwords.
func isThinShellCompoundConcept(label string, stems []string) bool {
	m := len(stems)
	if m < 2 || m > 4 {
		return false
	}
	s := strings.TrimSpace(CanonicalizeApostrophe(label))
	if strings.ContainsAny(s, ",!?;:\n\r'") {
		return false
	}
	contentBits, rawBits, _ := ContentInformationPurity(s)
	if rawBits <= contentBits || (rawBits-contentBits) > 8.0 {
		return false
	}
	toks := highGainTokens(s)
	if len(toks) < 2 {
		return false
	}
	lo := strings.ToLower(s)
	return strings.HasPrefix(lo, toks[0]) && strings.HasSuffix(lo, toks[len(toks)-1])
}

func hasRepeatedIdeographRune(s string) bool {
	if !IsPureCJK(s) {
		return false
	}
	var prev rune
	for _, r := range s {
		if r == prev {
			return true
		}
		prev = r
	}
	return false
}

// BuildHasseSubphraseLinks constructs a multi-level Hasse diagram (transitive reduction of contiguous stem sub-intervals)
// over referent and slot-occupying proposition nodes using pure Shannon self-information entropy and lattice topology
// (zero wordlists, zero language-specific suffix rules, zero character/word-count truncation).
func BuildHasseSubphraseLinks(nodes []SubphraseNodeInput, authorStemSet map[string]bool) []SubphraseLinkOutput {
	if len(nodes) < 2 {
		return nil
	}
	sortedNodes := make([]SubphraseNodeInput, len(nodes))
	copy(sortedNodes, nodes)
	sort.Slice(sortedNodes, func(i, j int) bool { return sortedNodes[i].ID < sortedNodes[j].ID })
	nodes = sortedNodes

	pureConceptByStemKey := make(map[string]uint64, len(nodes)*2)
	properIndividualNode := make(map[uint64]bool, len(nodes)/2)
	entityConceptByStemKey := make(map[string]uint64, len(nodes)/2)
	properWordByNodeID := make(map[uint64]string, len(nodes)/4)
	rawSingleWordByID := make(map[uint64]string, len(nodes))
	lookupVerified := func(m map[string]uint64, sk, childLabel string) uint64 {
		cid := m[sk]
		if cid == 0 {
			cid = stemLookupInMap(m, sk)
		}
		if cid > 0 {
			if rawW := rawSingleWordByID[cid]; rawW != "" && rawW != sk {
				loChild := strings.ToLower(childLabel)
				if !strings.Contains(loChild, rawW) {
					rRaw := []rune(rawW)
					stRaw := []rune(StemToken(rawW))
					morphOK := false
					for _, cw := range Tokenize(loChild) {
						rcw := []rune(cw)
						if MorphSimWordPair(rRaw, stRaw, rcw) >= 0.85 ||
							MorphSimWordPair(rcw, []rune(StemToken(cw)), rRaw) >= 0.85 {
							morphOK = true
							break
						}
					}
					if !morphOK {
						return 0
					}
				}
			}
		}
		return cid
	}

	// Pass 1: Index zero-shell pure concepts (IsPure == true).
	for _, n := range nodes {
		if n.IsSlotProp {
			continue
		}
		m := len(n.Stems)
		if m == 0 || m > 5 || !n.IsPure {
			continue
		}
		if m == 1 {
			lb := strings.TrimSpace(n.Label)
			loLb := strings.ToLower(strings.Trim(CanonicalizeApostrophe(lb), "\"'.,;:()[]{}"))
			rawSingleWordByID[n.ID] = loLb
			isAuthor := authorStemSet[n.Stems[0]] || authorStemSet[loLb]
			if hasProperCapitalWord([]string{lb}) {
				properIndividualNode[n.ID] = true
				properWordByNodeID[n.ID] = loLb
			} else if hasRepeatedIdeographRune(lb) || isAuthor {
				properIndividualNode[n.ID] = true
			}
			registerSingleStemBitFieldKeys(entityConceptByStemKey, n.ID, lb, n.Stems[0])
			if !isAuthor {
				registerSingleStemBitFieldKeys(pureConceptByStemKey, n.ID, lb, n.Stems[0])
			}
			continue
		}
		key := strings.Join(n.Stems, " ")
		loLb := strings.ToLower(strings.TrimSpace(n.Label))
		if pureConceptByStemKey[key] == 0 && !authorStemSet[key] && !authorStemSet[loLb] {
			pureConceptByStemKey[key] = n.ID
		}
	}

	// Pass 2a: Fallback anchors for thin-shell concepts (shellBits <= 8.0 bits) when no bare pure concept exists.
	colloquialConceptByStemKey := make(map[string]uint64, len(nodes)/2)
	for _, n := range nodes {
		if n.IsSlotProp || n.IsPoss || n.IsPure {
			continue
		}
		m := len(n.Stems)
		if m == 1 && !strings.ContainsAny(n.Label, ",!?;:'") {
			sk := n.Stems[0]
			if StringShannonBits(sk) >= AtomicConceptEntropyBits && !authorStemSet[sk] && stemLookupInMap(pureConceptByStemKey, sk) == 0 {
				contentBits, rawBits, _ := ContentInformationPurity(n.Label)
				toks := highGainTokens(n.Label)
				if rawBits > contentBits && (rawBits-contentBits) <= 8.0 && len(toks) == 1 &&
					strings.HasSuffix(strings.ToLower(strings.TrimSpace(n.Label)), toks[0]) {
					registerSingleStemBitFieldKeys(colloquialConceptByStemKey, n.ID, toks[0], sk)
				}
			}
			continue
		}
		if m >= 2 && m <= 4 && isThinShellCompoundConcept(n.Label, n.Stems) {
			key := strings.Join(n.Stems, " ")
			if pureConceptByStemKey[key] == 0 && colloquialConceptByStemKey[key] == 0 {
				colloquialConceptByStemKey[key] = n.ID
			}
		}
	}

	// Pass 2b: Shared head-concept cluster representatives for 2-stem pure concepts when no single-stem anchor exists.
	tailClusterRepByStem := make(map[string]uint64, len(nodes)/2)
	for _, n := range nodes {
		if n.IsSlotProp || n.IsPoss {
			continue
		}
		if len(n.Stems) == 2 && n.IsPure && !hasNonStopwordUpperToken(n.Label) {
			tailSk := n.Stems[1]
			if StringShannonBits(tailSk) >= AtomicConceptEntropyBits && !authorStemSet[tailSk] &&
				stemLookupInMap(pureConceptByStemKey, tailSk) == 0 && stemLookupInMap(colloquialConceptByStemKey, tailSk) == 0 {
				toks := Tokenize(n.Label)
				rawTail := tailSk
				if len(toks) > 0 {
					rawTail = toks[len(toks)-1]
				}
				registerSingleStemBitFieldKeys(tailClusterRepByStem, n.ID, rawTail, tailSk)
			}
		}
	}

	out := make([]SubphraseLinkOutput, 0, len(nodes))
	var singleWrapperLinks []SubphraseLinkOutput
	var entityAndClusterLinks []SubphraseLinkOutput

	for _, n := range nodes {
		stems := n.Stems
		m := len(stems)
		if m == 0 || m > 24 {
			continue
		}
		// Law 1: Single-stem shell-wrapped phrases (m == 1 && !IsPure).
		if m == 1 {
			if !n.IsPure && !strings.ContainsAny(n.Label, ",!?;:") {
				sk := stems[0]
				if stemLookupInMap(pureConceptByStemKey, sk) == n.ID || stemLookupInMap(colloquialConceptByStemKey, sk) == n.ID {
					continue
				}
				linkedSingle := false
				if StringShannonBits(sk) >= AtomicConceptEntropyBits && !authorStemSet[sk] {
					if cid := stemLookupInMap(pureConceptByStemKey, sk); cid > 0 && cid != n.ID && !properIndividualNode[cid] {
						singleWrapperLinks = append(singleWrapperLinks, SubphraseLinkOutput{
							ChildID:  n.ID,
							ParentID: cid,
							Evidence: "stem-subseq",
						})
						linkedSingle = true
					} else if cid := stemLookupInMap(colloquialConceptByStemKey, sk); cid > 0 && cid != n.ID {
						entityAndClusterLinks = append(entityAndClusterLinks, SubphraseLinkOutput{
							ChildID:  n.ID,
							ParentID: cid,
							Evidence: "stem-entity",
						})
						linkedSingle = true
					} else if cid := stemLookupInMap(tailClusterRepByStem, sk); cid > 0 && cid != n.ID {
						entityAndClusterLinks = append(entityAndClusterLinks, SubphraseLinkOutput{
							ChildID:  n.ID,
							ParentID: cid,
							Evidence: "stem-entity",
						})
						linkedSingle = true
					}
				}
				if !linkedSingle {
					if eid := stemLookupInMap(entityConceptByStemKey, sk); eid > 0 && eid != n.ID {
						if !properIndividualNode[eid] || hasNonStopwordUpperToken(n.Label) {
							entityAndClusterLinks = append(entityAndClusterLinks, SubphraseLinkOutput{
								ChildID:  n.ID,
								ParentID: eid,
								Evidence: "stem-entity",
							})
						}
					}
				}
			}
			continue
		}

		linked := map[uint64]bool{n.ID: true}
		coveredPos := make([]bool, m)
		_, rawBits, purity := ContentInformationPurity(n.Label)

		// 1. Right-anchored head suffix (Hasse immediate suffix parent: longest k < m first, marks [m-k, m) covered).
		if rawBits <= 256.0 || n.IsSlotProp {
			maxK := min(5, m-1)
			for k := maxK; k >= 1; k-- {
				sufKey := strings.Join(stems[m-k:], " ")
				if k == 1 && authorStemSet[sufKey] && !n.IsPoss {
					continue
				}
				parentID := pureConceptByStemKey[sufKey]
				if k == 1 {
					parentID = lookupVerified(pureConceptByStemKey, sufKey, n.Label)
				}
				if parentID > 0 && !linked[parentID] {
					if n.IsPoss && k == 1 && properIndividualNode[parentID] {
						continue
					}
					linked[parentID] = true
					for q := m - k; q < m; q++ {
						coveredPos[q] = true
					}
					out = append(out, SubphraseLinkOutput{
						ChildID:  n.ID,
						ParentID: parentID,
						Evidence: "stem-suffix",
					})
					break
				}
			}
		}

		// 2. Left-anchored possessor / modifier prefix for compact high-purity or possessive phrases (marks [0, k) covered).
		if !n.IsSlotProp && rawBits <= 48.0 && !strings.ContainsAny(n.Label, ",!?;:") && (n.IsPoss || n.IsPure) {
			for k := min(4, m-1); k >= 1; k-- {
				prefKey := strings.Join(stems[:k], " ")
				ownerID := pureConceptByStemKey[prefKey]
				if k == 1 {
					ownerID = lookupVerified(pureConceptByStemKey, prefKey, n.Label)
				}
				if ownerID > 0 && !linked[ownerID] {
					if authorStemSet[prefKey] || (k == 1 && properIndividualNode[ownerID] && !n.IsPoss && !n.IsPure) {
						continue
					}
					linked[ownerID] = true
					for q := 0; q < k; q++ {
						coveredPos[q] = true
					}
					out = append(out, SubphraseLinkOutput{
						ChildID:  n.ID,
						ParentID: ownerID,
						Evidence: "stem-prefix",
					})
					break
				}
			}
		}

		// 3. Hasse transitive reduction over contiguous internal sub-phrases and concepts (longest k first;
		//    any sub-interval completely covered by a strictly longer linked interval is skipped).
		const maxSubseqPerSentence = 4
		subseqAdded := 0
		for k := min(4, m-1); k >= 1 && subseqAdded < maxSubseqPerSentence; k-- {
			for p := 0; p <= m-k && subseqAdded < maxSubseqPerSentence; p++ {
				hasUncovered := false
				for q := p; q < p+k; q++ {
					if !coveredPos[q] {
						hasUncovered = true
						break
					}
				}
				if !hasUncovered {
					continue
				}
				subKey := strings.Join(stems[p:p+k], " ")
				if authorStemSet[subKey] || (k == 1 && StringShannonBits(subKey) < AtomicConceptEntropyBits) {
					continue
				}
				conceptID := pureConceptByStemKey[subKey]
				if k == 1 {
					conceptID = lookupVerified(pureConceptByStemKey, subKey, n.Label)
				}
				if conceptID > 0 && !linked[conceptID] {
					if k == 1 && properIndividualNode[conceptID] && (n.IsPoss || purity < 0.75) {
						continue
					}
					linked[conceptID] = true
					for q := p; q < p+k; q++ {
						coveredPos[q] = true
					}
					out = append(out, SubphraseLinkOutput{
						ChildID:  n.ID,
						ParentID: conceptID,
						Evidence: "stem-subseq",
					})
					subseqAdded++
				}
			}
		}

		// 4. Unlinked Phrase & Embedded Entity Fallback ("stem-entity"):
		if len(linked) == 1 || hasNonStopwordUpperToken(n.Label) {
			if len(linked) == 1 {
				for k := min(4, m-1); k >= 1; k-- {
					sufKey := strings.Join(stems[m-k:], " ")
					cid := colloquialConceptByStemKey[sufKey]
					if cid == 0 && k == 1 {
						cid = stemLookupInMap(colloquialConceptByStemKey, sufKey)
					}
					if cid > 0 && !linked[cid] {
						linked[cid] = true
						for q := m - k; q < m; q++ {
							coveredPos[q] = true
						}
						entityAndClusterLinks = append(entityAndClusterLinks, SubphraseLinkOutput{
							ChildID:  n.ID,
							ParentID: cid,
							Evidence: "stem-entity",
						})
						break
					}
				}
			}
			capStemSet := make(map[string]bool, 4)
			for _, f := range strings.Fields(n.Label) {
				fClean := strings.Trim(CanonicalizeApostrophe(f), "\"'.,!?;:()[]{}")
				if hasProperCapitalWord([]string{fClean}) {
					for _, tok := range Tokenize(fClean) {
						capStemSet[tok] = true
						capStemSet[StemToken(tok)] = true
					}
				}
			}
			for p := 0; p < m; p++ {
				if coveredPos[p] {
					continue
				}
				sk := stems[p]
				if len(linked) == 1 && !authorStemSet[sk] && StringShannonBits(sk) >= AtomicConceptEntropyBits {
					if cid := stemLookupInMap(colloquialConceptByStemKey, sk); cid > 0 && !linked[cid] {
						linked[cid] = true
						coveredPos[p] = true
						entityAndClusterLinks = append(entityAndClusterLinks, SubphraseLinkOutput{
							ChildID:  n.ID,
							ParentID: cid,
							Evidence: "stem-entity",
						})
						continue
					}
				}
				if eid := lookupVerified(entityConceptByStemKey, sk, n.Label); eid > 0 && !linked[eid] {
					if properIndividualNode[eid] {
						pw := properWordByNodeID[eid]
						if !capStemSet[sk] && (pw == "" || !capStemSet[pw]) {
							continue
						}
						if n.IsPoss && p == m-1 {
							continue
						}
					} else if len(linked) > 1 {
						continue
					}
					linked[eid] = true
					coveredPos[p] = true
					entityAndClusterLinks = append(entityAndClusterLinks, SubphraseLinkOutput{
						ChildID:  n.ID,
						ParentID: eid,
						Evidence: "stem-entity",
					})
				}
			}
			if len(linked) == 1 && rawBits <= 72.0 {
				tailSk := stems[m-1]
				if cid := stemLookupInMap(tailClusterRepByStem, tailSk); cid > 0 && !linked[cid] {
					linked[cid] = true
					entityAndClusterLinks = append(entityAndClusterLinks, SubphraseLinkOutput{
						ChildID:  n.ID,
						ParentID: cid,
						Evidence: "stem-entity",
					})
				}
			}
		}
	}
	out = append(out, singleWrapperLinks...)
	out = append(out, entityAndClusterLinks...)

	// Strict DAG acyclicity guard: never emit a directed edge (u -> v) if v can already reach u.
	adj := make(map[uint64][]uint64, len(out))
	canReach := func(src, dst uint64) bool {
		if src == dst {
			return true
		}
		q := []uint64{src}
		vis := map[uint64]bool{src: true}
		for len(q) > 0 {
			curr := q[0]
			q = q[1:]
			for _, nxt := range adj[curr] {
				if nxt == dst {
					return true
				}
				if !vis[nxt] {
					vis[nxt] = true
					q = append(q, nxt)
				}
			}
		}
		return false
	}
	dagOut := make([]SubphraseLinkOutput, 0, len(out))
	seenEdge := make(map[[2]uint64]bool, len(out))
	for _, lk := range out {
		if lk.ChildID == 0 || lk.ParentID == 0 || lk.ChildID == lk.ParentID {
			continue
		}
		pair := [2]uint64{lk.ChildID, lk.ParentID}
		if seenEdge[pair] || canReach(lk.ParentID, lk.ChildID) {
			continue
		}
		seenEdge[pair] = true
		adj[lk.ChildID] = append(adj[lk.ChildID], lk.ParentID)
		dagOut = append(dagOut, lk)
	}
	return dagOut
}
