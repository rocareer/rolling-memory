// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package codec

import (
	"math"
	"strings"

	"github.com/rocareer/rolling-memory/internal/zhao/rm/shaping"
)

// CanonicalPhraseJoint is the language-agnostic canonical single-space phrase delimiter.
const CanonicalPhraseJoint = shaping.CanonicalPhraseJoint

// CanonicalPhrase joins term faces with the canonical single-space joint.
func CanonicalPhrase(parts ...string) string {
	return shaping.CanonicalPhrase(parts...)
}

// SpaceElision is the result of one O(K) single-pass space-elision scan.
type SpaceElision = shaping.SpaceElision

// FuseSpaceElision repairs space faults of one sentence with a single pass and zero backtracking.
func FuseSpaceElision(sentence string) SpaceElision {
	return shaping.FuseSpaceElision(sentence)
}

// SpaceFaultFusion is the keyed projection of the space-fault-tolerant plugin
// over a text: the engine term layer performs the O(K) adjacent space-elision
// scan (src/zhao/engine/term/space_tolerance_plugin.go) and this view maps the
// candidates into the same keyed identity domain as primary terms
// (zero prior energy, flat-wave candidates).
type SpaceFaultFusion struct {
	// Tokens holds the keyed identities of the seamless fused tokens
	// e = t_i + t_{i+1} (bare faces, no whitespace).
	Tokens map[uint64]Term
	// Counts aligns with Tokens: flat-wave occurrence counts.
	Counts map[uint64]int
	// PhraseFaces lists the canonical single-space faces of the adjacent fused
	// phrases (first-occurrence order, deduplicated).
	PhraseFaces []string
	// Phrases holds the keyed identities of the canonical phrase faces; phrase
	// faces always contain the canonical joint, so they never collide with the
	// whitespace-free term faces.
	Phrases map[uint64]Term
	// PhraseCounts aligns with Phrases.
	PhraseCounts map[uint64]int
}

// MergeInto folds the fusion candidates into a flat-wave atom table and,
// when counts is non-nil, into the aligned count table. It is the single merge
// point shared by query-side repair (Engine.Query probes the clean dictionary
// with repaired tokens) and by callers that deliberately opt into symmetric
// indexing of the dual track. Identity collisions with existing atoms keep the
// existing identity and add the flat-wave count.
func (f SpaceFaultFusion) MergeInto(atoms map[uint64]Term, counts map[uint64]int) {
	if atoms == nil {
		return
	}
	for hash, atom := range f.Tokens {
		if _, exists := atoms[hash]; !exists {
			atoms[hash] = atom
		}
		if counts != nil {
			counts[hash] += f.Counts[hash]
		}
	}
	for hash, atom := range f.Phrases {
		if _, exists := atoms[hash]; !exists {
			atoms[hash] = atom
		}
		if counts != nil {
			counts[hash] += f.PhraseCounts[hash]
		}
	}
}

// ExtractSpaceFaultFusion scans the whole text sentence by sentence and merges
// every candidate. All candidates are kept as flat wave: real words
// ("山东", "an", "apple") and cross-boundary pseudo-fusions ("napp") enter the
// table alike; dynamic energy resolution is owned by decode-time corpus
// statistics, where pseudo-fusions without corpus matches collapse to zero.
// Fused phrases stay sentence-local. Zero vocabulary lookup, zero BPE prior
// calls; cost is O(K) in the number of base blocks K, at most K-1 fused tokens.
func (a *TermExtractor) ExtractSpaceFaultFusion(text string) SpaceFaultFusion {
	fusion := SpaceFaultFusion{
		Tokens:       make(map[uint64]Term, 64),
		Counts:       make(map[uint64]int, 64),
		Phrases:      make(map[uint64]Term, 16),
		PhraseCounts: make(map[uint64]int, 16),
	}
	seenFace := make(map[string]bool, 16)
	for _, sent := range splitSentences(CanonicalizeApostrophe(text)) {
		plan := FuseSpaceElision(sent)
		for _, face := range plan.Tokens {
			low := strings.ToLower(strings.TrimSpace(face))
			if low == "" {
				continue
			}
			hash := a.enc.KeyedHash64(low)
			if hash == 0 {
				continue
			}
			if _, ok := fusion.Tokens[hash]; !ok {
				fusion.Tokens[hash] = Term{Hash: hash}
			}
			fusion.Counts[hash]++
		}
		for _, face := range plan.Phrases {
			low := strings.ToLower(strings.TrimSpace(face))
			if low == "" {
				continue
			}
			hash := a.enc.KeyedHash64(low)
			if hash == 0 {
				continue
			}
			if _, ok := fusion.Phrases[hash]; !ok {
				fusion.Phrases[hash] = Term{Hash: hash}
			}
			fusion.PhraseCounts[hash]++
			if !seenFace[low] {
				seenFace[low] = true
				fusion.PhraseFaces = append(fusion.PhraseFaces, low)
			}
		}
	}
	return fusion
}

// mergeQuerySpaceFaultFusion folds space-elision candidates into qTerms under
// the corpus null-hypothesis geometric-mean cohesion law:
//
//	df(L+R) * e^2 >= sqrt(df(L) * df(R))
//
// When a word was genuinely split by an accidental space (e.g. "app le", "山 东"),
// at least one fragment is absent or rare in the clean corpus while the fused
// word is attested, so the inequality holds; when two standalone corpus words
// (e.g. "of a", "and in", "in St", "I had") are adjacent in normal prose,
// sqrt(df(L)*df(R)) >> df(L+R)*e^2 and the pseudo-fusion is suppressed without
// any word list or character-length threshold.
func (a *TermExtractor) mergeQuerySpaceFaultFusionSents(idx *Index, canonical string, sents []string, qTerms map[uint64]Term) {
	if qTerms == nil {
		return
	}
	if idx == nil {
		a.ExtractSpaceFaultFusion(canonical).MergeInto(qTerms, nil)
		return
	}
	const e2 = math.E * math.E
	for _, sent := range sents {
		plan := FuseSpaceElision(sent)
		blocks, separated := plan.Blocks, plan.Separated
		for boundary := 0; boundary+1 < len(blocks); boundary++ {
			if !separated[boundary+1] {
				continue
			}
			left := strings.TrimFunc(blocks[boundary], func(r rune) bool { return !isLetterOrNumber(r) })
			right := strings.TrimFunc(blocks[boundary+1], func(r rune) bool { return !isLetterOrNumber(r) })
			if left == "" || right == "" {
				continue
			}
			script := shaping.SpaceElisionBlockScript(left)
			if script == shaping.SpaceElisionScriptNone || script != shaping.SpaceElisionBlockScript(right) {
				continue
			}
			lLow := strings.ToLower(left)
			rLow := strings.ToLower(right)
			fLow := lLow + rLow
			hF := a.enc.KeyedHash64(fLow)
			if hF == 0 {
				continue
			}
			if _, exists := qTerms[hF]; exists {
				continue
			}
			dfF := idx.TermDF(hF)
			if dfF == 0 {
				continue
			}
			hL := a.enc.KeyedHash64(lLow)
			hR := a.enc.KeyedHash64(rLow)
			dfL, dfR := idx.TermDF(hL), idx.TermDF(hR)
			if float64(dfF)*e2 < math.Sqrt(float64(dfL)*float64(dfR)) {
				continue
			}
			atom := Term{Hash: hF}
			if dfL > 0 && dfR > 0 {
				atom.Group = hL
				atom.Flags = FlagIsHarmonicCompanion
			}
			qTerms[hF] = atom
		}
		for _, face := range plan.Phrases {
			low := strings.ToLower(strings.TrimSpace(face))
			if low == "" {
				continue
			}
			if hP := a.enc.KeyedHash64(low); hP != 0 {
				if _, exists := qTerms[hP]; !exists && idx.TermDF(hP) > 0 {
					qTerms[hP] = Term{Hash: hP}
				}
			}
		}
	}
}
