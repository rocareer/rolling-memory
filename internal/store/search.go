// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package store

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/rocareer/rolling-memory/internal/ids"
	"github.com/rocareer/rolling-memory/internal/memcore"
)

type nodeCore struct {
	ID       string
	Kind     string
	Status   string
	Audience string
	Label    string
}

func coreFromNode(node Node) nodeCore {
	return nodeCore{
		ID:       node.ID,
		Kind:     node.Kind,
		Status:   node.Status,
		Audience: node.Audience,
		Label:    node.Label,
	}
}

func coreFromMap(row map[string]any) nodeCore {
	return nodeCore{
		ID:       mapString(row, "id"),
		Kind:     mapString(row, "kind"),
		Status:   mapString(row, "status"),
		Audience: mapString(row, "audience"),
		Label:    mapString(row, "label"),
	}
}

// SearchSeeds runs the exact/alias/substring/lexical/vector seed legs.
func (s *Store) SearchSeeds(cue string, o SeedOptions) ([]Seed, map[string]int, error) {
	if o.Limit == 0 {
		o.Limit = 32
	}
	if o.Limit < 0 {
		o.Limit = 0
	}
	engineMode := strings.ToLower(strings.TrimSpace(o.SearchEngine))
	if engineMode == "" && s.Config != nil {
		engineMode = strings.ToLower(strings.TrimSpace(s.Config.Recall.SearchEngine))
	}
	if engineMode == "" {
		engineMode = "hybrid"
	}
	switch engineMode {
	case "fts", "vector", "hybrid":
	default:
		return nil, nil, fmt.Errorf("search_engine must be one of: fts, vector, hybrid (got %q)", engineMode)
	}
	if engineMode == "vector" && (s.Config == nil || !s.Config.Recall.Vector) {
		return nil, nil, fmt.Errorf("search_engine=vector requires [recall] vector = true")
	}
	lexicalEnabled := engineMode != "vector"
	terms := ids.Tokenize(cue)
	if !strings.HasPrefix(o.Scope, "mem:golden:") {
		allTerms := ids.TokenizeAll(cue)
		if filtered := ids.FilterQueryTerms(allTerms); len(filtered) > 0 {
			terms = filtered
		} else {
			terms = allTerms
		}
		if len(terms) > memcore.TokenTier64 {
			terms = terms[:memcore.TokenTier64]
		}
	}
	scores := map[string]*Seed{}
	legs := map[string]int{
		"exact": 0, "alias": 0, "lexical": 0,
		"substring": 0, "vector": 0, "token_correct": 0,
	}

	add := func(node nodeCore, score float64, leg string) {
		if node.Kind == "assertion" || (node.Status != "provisional" && node.Status != "active") {
			return
		}
		if !ids.AudienceVisible(o.Reader, node.Audience) {
			return
		}
		if current, ok := scores[node.ID]; ok {
			if score > current.Score {
				current.Score = score
			}
			current.Legs[leg] = struct{}{}
			return
		}
		scores[node.ID] = &Seed{
			NodeID: node.ID,
			Label:  node.Label,
			Score:  score,
			Legs:   map[string]struct{}{leg: {}},
		}
	}

	whole := pyStrip(cue)
	if lexicalEnabled && whole != "" {
		exact, found, err := s.NodeByLabel(o.Scope, whole, "")
		if err != nil {
			return nil, nil, err
		}
		if found {
			add(coreFromNode(exact), 1.0, "exact")
			legs["exact"]++
		}
	}

	runTerm := func(term string) (int, error) {
		hits := 0
		isGolden := strings.HasPrefix(o.Scope, "mem:golden:")
		termBits := memcore.StringShannonBits(term)
		infoTieBreak := 0.0
		if !isGolden {
			infoTieBreak = math.Min(0.04, termBits/1000.0)
		}
		exact, found, err := s.NodeByLabel(o.Scope, term, "")
		if err != nil {
			return hits, err
		}
		if found {
			add(coreFromNode(exact), 0.9+infoTieBreak, "exact")
			legs["exact"]++
			hits++
		}
		rows, err := queryMaps(s.conn,
			`SELECT DISTINCT n.* FROM node_alias a CROSS JOIN node n ON n.id = a.node_id
			 WHERE a.norm_alias = ? AND n.scope = ? AND n.status IN ('provisional','active')`,
			ids.NormalizeLabel(term), o.Scope)
		if err != nil {
			return hits, err
		}
		for _, row := range rows {
			add(coreFromMap(row), 0.85+infoTieBreak, "alias")
			legs["alias"]++
			hits++
		}
		minSubBits := 4.0
		if ids.HasCJK(term) {
			minSubBits = 10.5
		}
		if (isGolden && termBits >= minSubBits) || (!isGolden && !memcore.IsShortOrDigitsToken(term) && termBits >= minSubBits) {
			termNorm := ids.NormalizeLabel(term)
			var rows []map[string]any
			var err error
			usedFTS := false
			if !isGolden && s.fts && !ids.HasCJK(termNorm) && termBits >= 6.0 && termNorm != "" {
				rows, err = queryMaps(s.conn,
					`SELECT n.* FROM (SELECT node_id, bm25(node_fts) AS r FROM node_fts WHERE node_fts MATCH ? ORDER BY r LIMIT 96) f
						 JOIN node n ON n.id = f.node_id
						 WHERE n.scope = ? AND n.status IN ('provisional','active') AND n.kind != 'assertion'
						 LIMIT 24`,
					ftsMatch([]string{termNorm}), o.Scope)
				if err == nil {
					usedFTS = true
				}
			}
			if !usedFTS {
				pattern := "%" + ids.EscapeLike(termNorm) + "%"
				rows, err = queryMaps(s.conn,
					`SELECT n.* FROM node n
							 WHERE n.scope = ? AND n.status IN ('provisional','active') AND n.kind != 'assertion'
							   AND (n.norm_label LIKE ? ESCAPE '\'
							        OR EXISTS (SELECT 1 FROM node_alias a WHERE a.node_id = n.id
							                   AND a.norm_alias LIKE ? ESCAPE '\'))
							 LIMIT 24`,
					o.Scope, pattern, pattern)
			}
			if err != nil {
				return hits, err
			}
			boost := 0.6 + math.Min(0.15, float64(runeCount(term))/40.0)
			if !isGolden {
				boost = 0.6 + math.Min(0.15, termBits/80.0)
			}
			for _, row := range rows {
				subBoost := boost
				if !isGolden && termNorm != "" {
					rowNorm := mapString(row, "norm_label")
					if strings.HasPrefix(rowNorm, termNorm+"'s ") || strings.HasPrefix(rowNorm, termNorm+"s' ") {
						subBoost = 0.89
					}
				}
				add(coreFromMap(row), subBoost, "substring")
				legs["substring"]++
				hits++
			}
		}
		return hits, nil
	}

	zeroTerms := []string{}
	if lexicalEnabled {
		termLimit := 24
		if len(terms) < termLimit {
			termLimit = len(terms)
		}
		for _, term := range terms[:termLimit] {
			hits, err := runTerm(term)
			if err != nil {
				return nil, nil, err
			}
			if hits == 0 && memcore.StringShannonBits(term) >= 6.0 {
				zeroTerms = append(zeroTerms, term)
			}
		}
	}

	tokenCorrect := s.Config.Recall.TokenCorrect
	if o.TokenCorrect != nil {
		tokenCorrect = *o.TokenCorrect
	}
	if lexicalEnabled && tokenCorrect && len(zeroTerms) > 0 {
		probe := zeroTerms
		if len(probe) > 4 {
			probe = probe[:4]
		}
		corrections, err := s.tokenCorrections(o.Scope, probe, 2)
		if err != nil {
			return nil, nil, err
		}
		for _, correction := range corrections {
			hits, err := runTerm(correction)
			if err != nil {
				return nil, nil, err
			}
			if hits > 0 {
				legs["token_correct"]++
			}
		}
	}

	if lexicalEnabled && s.fts {
		ftsTerms := []string{}
		for _, term := range terms {
			if runeCount(term) >= 3 {
				ftsTerms = append(ftsTerms, term)
			}
			if len(ftsTerms) == 8 {
				break
			}
		}
		if len(ftsTerms) > 0 {
			match := ftsMatch(ftsTerms)
			rows, err := queryMaps(s.conn,
				`SELECT n.*, f.bm25_score
				 FROM (SELECT node_id, bm25(node_fts) AS bm25_score FROM node_fts
				       WHERE node_fts MATCH ?) f
				 JOIN node n ON n.id = f.node_id
				 WHERE n.scope = ?
				   AND n.status IN ('provisional','active') AND n.kind != 'assertion'
				 ORDER BY f.bm25_score LIMIT 32`,
				match, o.Scope)
			if err != nil {
				rows = nil
			}
			for _, row := range rows {
				// The aliased column carries the raw bm25() score (a continuous negative-valued
				// magnitude), never a discrete rank index: the lexical leg contributes a continuous
				// score bonus, so no reciprocal-rank term enters the fusion.
				bm25 := absFloat(mapFloat(row, "bm25_score"))
				add(coreFromMap(row), 0.55+0.3*(1.0/(1.0+bm25)), "lexical")
				legs["lexical"]++
			}
		}
	}

	trunk := make([]Seed, 0, len(scores))
	for _, seed := range scores {
		trunk = append(trunk, *seed)
	}
	sortSeeds(trunk)
	if o.Limit < len(trunk) {
		trunk = trunk[:o.Limit]
	}
	return trunk, legs, nil
}

// sortSeeds orders a seed slice by score desc, then label, then node id (the
// read path is deterministic for the same database, cue and caps).
func sortSeeds(seeds []Seed) {
	sort.Slice(seeds, func(left, right int) bool {
		if seeds[left].Score != seeds[right].Score {
			return seeds[left].Score > seeds[right].Score
		}
		if seeds[left].Label != seeds[right].Label {
			return seeds[left].Label < seeds[right].Label
		}
		return seeds[left].NodeID < seeds[right].NodeID
	})
}

// tokenCorrections builds mechanical variants for zero-hit query tokens.
//
// For every zero-hit token: adjacent transposition / single-char deletion,
// prefilter candidate labels by the first character, then keep word-level
// matches: exact variant hit or SequenceMatcher >= 0.45. Zero LLM; never
// invents a label.
func (s *Store) tokenCorrections(scope string, terms []string, limit int) ([]string, error) {
	if limit == 0 {
		limit = 2
	}
	type correction struct {
		ratio float64
		label string
	}
	corrections := []correction{}
	seen := map[string]struct{}{}
	for _, term := range terms {
		variants := map[string]struct{}{}
		runes := []rune(term)
		for index := 0; index+1 < len(runes); index++ {
			swapped := append([]rune{}, runes...)
			swapped[index], swapped[index+1] = swapped[index+1], swapped[index]
			variants[string(swapped)] = struct{}{}
		}
		for index := range runes {
			deleted := append([]rune{}, runes[:index]...)
			deleted = append(deleted, runes[index+1:]...)
			variants[string(deleted)] = struct{}{}
		}
		first := ""
		if len(runes) > 0 {
			first = string(runes[0])
		}
		rows, err := queryMaps(s.conn,
			`SELECT label, norm_label FROM node
			 WHERE scope = ? AND status IN ('provisional','active') AND kind != 'assertion'
			   AND substr(norm_label, 1, 1) = ?
			 LIMIT 400`,
			scope, first)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			norm := mapString(row, "norm_label")
			if norm == "" || norm == term {
				continue
			}
			if _, ok := seen[norm]; ok {
				continue
			}
			ratio := 0.0
			if _, ok := variants[norm]; ok {
				ratio = 1.0
			} else {
				ratio = sequenceRatio([]rune(term), []rune(norm), true)
				if ratio < 0.45 {
					continue
				}
			}
			seen[norm] = struct{}{}
			corrections = append(corrections, correction{ratio: ratio, label: mapString(row, "label")})
		}
	}
	sort.SliceStable(corrections, func(left, right int) bool {
		if corrections[left].ratio != corrections[right].ratio {
			return corrections[left].ratio > corrections[right].ratio
		}
		return corrections[left].label < corrections[right].label
	})
	result := []string{}
	for _, item := range corrections {
		result = append(result, item.label)
		if len(result) == limit {
			break
		}
	}
	return result, nil
}

// ftsMatch quotes every term; embedded quotes are doubled.
func ftsMatch(terms []string) string {
	quoted := make([]string, 0, len(terms))
	for _, term := range terms {
		quoted = append(quoted, `"`+strings.ReplaceAll(term, `"`, `""`)+`"`)
	}
	return strings.Join(quoted, " OR ")
}
