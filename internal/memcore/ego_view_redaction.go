// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package memcore

import (
	"fmt"
	"sort"
	"strings"
)

// CanonicalSelfDesignator is the sole online-writer storage-core designator for the Assistant's own identity (origin="self").
// Explicit persona names are bound via aliases and projected on egress; governed display representatives use append-only merges.
const CanonicalSelfDesignator = "self"

// FallbackAssistantDesignator is the consumer-facing role label used when no unique persona name is known.
const FallbackAssistantDesignator = "assistant"

// EgressViewMode specifies how identity and roster labels are projected at the consumer boundary.
type EgressViewMode string

const (
	// EgressViewNeutral projects the canonical self token to a uniquely identified name or "assistant";
	// original roster labels and verbatim evidence stay untouched (default for eval/harness).
	EgressViewNeutral EgressViewMode = "neutral"
	// EgressViewPersona projects the canonical "self" token to the active persona name or "assistant".
	EgressViewPersona EgressViewMode = "persona"
	// EgressViewRedacted projects "self" to a generic role label ("assistant" or custom) and masks roster entities + aliases into deterministic pseudonyms ("Person_1", "Person_2", ...).
	EgressViewRedacted EgressViewMode = "redacted"
)

// EgressViewPolicy defines the consumer-side projection and de-identification parameters.
type EgressViewPolicy struct {
	Mode            EgressViewMode
	SelfDisplayName string              // Target display name for "self" (e.g. "湖伴" in EgressViewPersona, or "assistant" in EgressViewRedacted)
	SelfHistory     []string            // Historical or active persona names bound to "self" (used for evidence-text de-identification or normalization)
	RosterNodeIDs   map[uint64]bool     // Node IDs with origin="roster" (eligible for pseudonym masking in EgressViewRedacted)
	NodeAliases     map[uint64][]string // All known aliases per node ID (from memory_alias), enabling synchronized masking across structured bands and raw [E#k] slices
}

// IsSelfDesignator reports whether label matches the canonical self token, base self role words,
// or any configured active/historical persona name. Pure equality check, zero semantic guessing.
func IsSelfDesignator(label string, personaNames ...string) bool {
	trimmed := strings.TrimSpace(label)
	if trimmed == "" {
		return false
	}
	switch strings.ToLower(trimmed) {
	case CanonicalSelfDesignator, FallbackAssistantDesignator, "助手":
		return true
	}
	for _, nm := range personaNames {
		p := strings.TrimSpace(nm)
		if p != "" && strings.EqualFold(trimmed, p) {
			return true
		}
	}
	return false
}

// RewriteQuerySelfDesignators bridges user queries that mention an active or historical persona name
// (e.g. "贾维斯" or "湖伴") to the storage-core canonical token "self", ensuring O(1) recall reachability
// across arbitrary assistant renames without mutating the underlying graph.
func RewriteQuerySelfDesignators(query string, personaNames ...string) string {
	trimmedQ := strings.TrimSpace(query)
	if trimmedQ == "" || len(personaNames) == 0 {
		return query
	}
	var pairs []string
	seen := map[string]bool{}
	cands := make([]string, 0, len(personaNames))
	for _, nm := range personaNames {
		p := strings.TrimSpace(nm)
		if p == "" || strings.EqualFold(p, CanonicalSelfDesignator) {
			continue
		}
		low := strings.ToLower(p)
		if !seen[low] {
			seen[low] = true
			cands = append(cands, p)
		}
	}
	sortByShannonMassDesc(cands)
	lowQ := strings.ToLower(trimmedQ)
	for _, p := range cands {
		lowP := strings.ToLower(p)
		if strings.Contains(trimmedQ, p) {
			pairs = append(pairs, p, CanonicalSelfDesignator)
		} else if strings.Contains(lowQ, lowP) {
			pairs = append(pairs, lowP, CanonicalSelfDesignator)
		}
	}
	if len(pairs) == 0 {
		return query
	}
	res := strings.NewReplacer(pairs...).Replace(trimmedQ)
	if res == trimmedQ {
		res = strings.NewReplacer(pairs...).Replace(lowQ)
	}
	return res
}

// ProjectNodeLabelsView performs an O(1) consumer-side projection on the node label dictionary
// and returns both the projected label map and a synchronized text-replacement glossary for [E#k] evidence slices.
//
// Invariants:
//  1. Storage-core immutability: the input labels map is never mutated in place; a projected copy is returned.
//  2. Co-referential consistency: in EgressViewRedacted, all roster nodes are sorted by node ID and assigned
//     deterministic pseudonyms ("Person_1", "Person_2", ...), and all of a node's aliases map to the exact same pseudonym.
//  3. Persona projection: in EgressViewPersona, any self-designator node ("self" or legacy persona name) is projected
//     to SelfDisplayName when non-empty.
func ProjectNodeLabelsView(labels map[uint64]string, policy EgressViewPolicy) (projected map[uint64]string, glossary map[string]string) {
	projected = make(map[uint64]string, len(labels))
	glossary = make(map[string]string)
	for id, lb := range labels {
		projected[id] = lb
	}

	allPersonas := make([]string, 0, len(policy.SelfHistory)+1)
	if policy.SelfDisplayName != "" {
		allPersonas = append(allPersonas, policy.SelfDisplayName)
	}
	allPersonas = append(allPersonas, policy.SelfHistory...)

	targetSelf := strings.TrimSpace(policy.SelfDisplayName)
	if targetSelf == "" || strings.EqualFold(targetSelf, CanonicalSelfDesignator) {
		targetSelf = FallbackAssistantDesignator
	}

	// 1. Project Assistant self nodes and register self-history glossary entries.
	for id, lb := range projected {
		if IsSelfDesignator(lb, allPersonas...) {
			projected[id] = targetSelf
			if trimmed := strings.TrimSpace(lb); trimmed != "" && !strings.EqualFold(trimmed, targetSelf) {
				glossary[trimmed] = targetSelf
			}
			for _, al := range policy.NodeAliases[id] {
				if trimmedAl := strings.TrimSpace(al); trimmedAl != "" && !strings.EqualFold(trimmedAl, targetSelf) {
					glossary[trimmedAl] = targetSelf
				}
			}
		}
	}
	for _, nm := range allPersonas {
		if trimmed := strings.TrimSpace(nm); trimmed != "" && !strings.EqualFold(trimmed, targetSelf) {
			glossary[trimmed] = targetSelf
		}
	}
	if policy.Mode == EgressViewPersona && targetSelf != CanonicalSelfDesignator {
		glossary[CanonicalSelfDesignator] = targetSelf
	}
	// Neutral delivery projects structured labels only. Raw [E#k] evidence remains verbatim.
	if policy.Mode == EgressViewNeutral {
		return projected, map[string]string{}
	}

	// 2. In EgressViewRedacted, assign deterministic pseudonyms to roster entities and their alias sets.
	if policy.Mode == EgressViewRedacted && len(policy.RosterNodeIDs) > 0 {
		rosterIDs := make([]uint64, 0, len(policy.RosterNodeIDs))
		for id, isRoster := range policy.RosterNodeIDs {
			if !isRoster || id == 0 {
				continue
			}
			if IsSelfDesignator(projected[id], allPersonas...) {
				continue
			}
			rosterIDs = append(rosterIDs, id)
		}
		sort.Slice(rosterIDs, func(i, j int) bool { return rosterIDs[i] < rosterIDs[j] })
		for idx, id := range rosterIDs {
			mask := fmt.Sprintf("Person_%d", idx+1)
			orig := strings.TrimSpace(projected[id])
			projected[id] = mask
			if orig != "" && !strings.EqualFold(orig, mask) {
				glossary[orig] = mask
			}
			for _, al := range policy.NodeAliases[id] {
				if trimmedAl := strings.TrimSpace(al); trimmedAl != "" && !strings.EqualFold(trimmedAl, mask) {
					glossary[trimmedAl] = mask
				}
			}
		}
	}

	return projected, glossary
}

// RedactEvidenceTextWithGlossary applies deterministic multi-pattern replacement to raw evidence slice text
// ([E#k]) using the glossary produced by ProjectNodeLabelsView.
// Patterns are ordered by Shannon self-information mass descending so compound full names ("Sarah Garcia")
// always match before shorter given names or nicknames ("Sarah").
func RedactEvidenceTextWithGlossary(text string, glossary map[string]string) string {
	if text == "" || len(glossary) == 0 {
		return text
	}
	keys := make([]string, 0, len(glossary))
	for src, dst := range glossary {
		s := strings.TrimSpace(src)
		if s == "" || s == dst || s == CanonicalSelfDesignator {
			continue
		}
		keys = append(keys, s)
	}
	if len(keys) == 0 {
		return text
	}
	sortByShannonMassDesc(keys)
	pairs := make([]string, 0, len(keys)*2)
	for _, k := range keys {
		pairs = append(pairs, k, glossary[k])
	}
	return strings.NewReplacer(pairs...).Replace(text)
}

// AssemblePersonaViewText replaces canonical "self" structural tokens in rendered memory text
// with the consumer scene's active persona name (e.g. "湖伴" or "小舟" in 1v1 companion dialog).
// Only structural slot boundaries (band headers, assertion slots, speaker tags, evidence headers)
// are replaced so ordinary English words containing "self" (e.g. "himself", "self-transform") are never touched.
func AssemblePersonaViewText(text, personaName string) string {
	name := strings.TrimSpace(personaName)
	if name == "" || strings.EqualFold(name, CanonicalSelfDesignator) {
		name = FallbackAssistantDesignator
	}
	if text == "" {
		return text
	}
	zhBandPrefix := LabelFor("zh", LabelBandSubjectPrefix)
	enBandPrefix := LabelFor("en", LabelBandSubjectPrefix)
	replacer := strings.NewReplacer(
		zhBandPrefix+"self】", zhBandPrefix+name+"】",
		enBandPrefix+"self】", enBandPrefix+name+"】",
		"【Memory Band · self】", "【Memory Band · "+name+"】",
		"｜ self ", "｜ "+name+" ",
		"| self ", "| "+name+" ",
		"· self ", "· "+name+" ",
		"(self→", "("+name+"→",
		"→self)", "→"+name+")",
		"讲述人(self)", "讲述人("+name+")",
		"speaker:self", "speaker:"+name,
	)
	return replacer.Replace(text)
}

func sortByShannonMassDesc(items []string) {
	sort.SliceStable(items, func(i, j int) bool {
		bi := StringShannonBits(items[i])
		bj := StringShannonBits(items[j])
		if bi != bj {
			return bi > bj
		}
		return items[i] > items[j]
	})
}
