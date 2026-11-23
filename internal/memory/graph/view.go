// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package graph

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	core "github.com/rocareer/rolling-memory/internal/core"
	"github.com/rocareer/rolling-memory/internal/graphmodel"
	"github.com/rocareer/rolling-memory/internal/memory/localvault"
	plain "github.com/rocareer/rolling-memory/sdk/go/pulse/plain"
)

const (
	// maxGraphLimit bounds one rendered graph view. The local daemon serves
	// /v1/graph over an 8 MiB HTTP body budget, so a view larger than this would
	// fail the verified relay before it reached the console. The full unit total
	// is still reported through stats.event_total and truncated.nodes.
	maxGraphLimit       = 4096
	defaultGraphLimit   = 256
	maxViewUnits        = 512
	maxGraphTextRunes   = 200000
	maxGraphSpanRunes   = 1200
	maxPhraseCandidates = 24
	maxWordsHard        = 512
	maxPhrasesHard      = 512
	maxHydrateBudget    = 20 * time.Second
)

// View materializes one graph view from the cached engine snapshot.
func (b *Builder) View(ctx context.Context, req graphmodel.ViewRequest) (*graphmodel.Response, error) {
	started := time.Now()
	snap, err := b.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	// Hold a render lease on the cached engine for the whole projection: the
	// builder may retire this snapshot as soon as a newer generation lands,
	// and the backing mmap must outlive every active render.
	_, releaseEngine, _ := snap.Engine()
	defer releaseEngine()
	op := strings.ToLower(strings.TrimSpace(req.Op))
	cacheable := op == "" || op == "view"
	cacheKey := ""
	if cacheable {
		cacheKey = viewCacheKey(snap, req)
		b.viewMu.Lock()
		if b.viewResp != nil && b.viewKey == cacheKey && time.Since(b.viewAt) < 30*time.Second {
			cached := *b.viewResp
			b.viewMu.Unlock()
			return &cached, nil
		}
		b.viewMu.Unlock()
	}
	limit := normalizeLimit(req.Limit)
	layers := normalizeLayerSet(req.Layers)
	clusterTypes := normalizeClusterTypeSet(req.ClusterTypes)
	var selected []*Unit
	var totalFiltered int
	if strings.EqualFold(strings.TrimSpace(req.Op), "expand") && len(req.SeedKeys) > 0 {
		selected, totalFiltered = selectSeedUnits(snap, req, limit, layers)
	} else {
		selected, totalFiltered = selectUnits(snap, limit, req.Keyword, layers)
	}

	state := &viewState{
		builder:        b,
		snap:           snap,
		limit:          limit,
		layers:         layers,
		clusterTypes:   clusterTypes,
		selected:       selected,
		totalFiltered:  totalFiltered,
		labels:         map[uint64]string{},
		groupSurface:   map[uint64]map[string]int{},
		termMass:       map[uint64]float64{},
		wordAgg:        map[uint64]*wordAgg{},
		phraseAgg:      map[phraseKey]*phraseAgg{},
		backgroundDF:   backgroundDisplayLimit(snap),
		wordBudget:     clampInt(limit, 64, maxWordsHard),
		phraseBudget:   clampInt(limit/2, 32, maxPhrasesHard),
		wordsPerUnit:   clampInt(limit/64, 2, 8),
		phrasesPerUnit: clampInt(limit/128, 1, 4),
	}
	state.hydrate(ctx)
	state.buildLabels()
	state.buildUnitPayloads()
	nodes, edges, kindCounts, clusterTypeCounts := state.assemble()

	scope := strings.TrimSpace(req.Scope)
	if scope == "" {
		scope = "mem:self"
	}
	stats := state.stats(nodes, edges, kindCounts, clusterTypeCounts)
	stats.DisplayFilteredWords = state.filteredWords
	stats.DisplayFilteredPhrases = state.filteredPhrases
	stats.BuildMS = snap.BuildMS + time.Since(started).Milliseconds()
	resp := &graphmodel.Response{
		GraphSchemaVersion: graphmodel.GraphSchemaVersion,
		Source:             "local-vault",
		ScopeKey:           scope,
		Scopes: []graphmodel.ScopeItem{{
			Source:    "local-vault",
			ScopeKey:  scope,
			Events:    len(snap.Units),
			Links:     len(edges),
			Materials: len(nodes),
		}},
		Nodes:     nodes,
		Edges:     edges,
		Relations: relationOptions(edges),
		Stats:     stats,
		Truncated: graphmodel.Truncated{
			Nodes: len(selected) < totalFiltered,
			Edges: len(state.wordAgg) > state.wordBudget || len(state.phraseAgg) > state.phraseBudget,
		},
	}
	if cacheable {
		b.viewMu.Lock()
		b.viewKey = cacheKey
		b.viewAt = time.Now()
		b.viewResp = resp
		b.viewMu.Unlock()
	}
	return resp, nil
}

type viewState struct {
	builder       *Builder
	snap          *Snapshot
	limit         int
	layers        map[string]bool
	clusterTypes  map[string]bool
	selected      []*Unit
	totalFiltered int

	units []*hydratedUnit

	labels       map[uint64]string
	groupSurface map[uint64]map[string]int
	termMass     map[uint64]float64

	wordAgg   map[uint64]*wordAgg
	phraseAgg map[phraseKey]*phraseAgg

	// Display projection: background word families and phrases with a
	// background endpoint are recall keys, not consumer-facing labels. The
	// aggregates above stay complete; only the rendered selection is filtered.
	backgroundDF    int
	filteredWords   int
	filteredPhrases int

	wordBudget     int
	phraseBudget   int
	wordsPerUnit   int
	phrasesPerUnit int
}

type hydratedUnit struct {
	unit     *Unit
	summary  string
	span     string
	phrases  []rawPhrase
	surfaces map[uint64]map[string]int
}

type rawPhrase struct {
	a           uint64
	b           uint64
	df          int
	dfA         int
	dfB         int
	containment float64
	lift        float64
	joint       float64
	binding     float64
}

type wordAgg struct {
	group    uint64
	weight   float64
	df       int
	mass     float64
	unitSpan int
	units    map[string]float64
	layers   map[string]int
}

type phraseKey struct {
	a uint64
	b uint64
}

type phraseAgg struct {
	a           uint64
	b           uint64
	df          int
	dfA         int
	dfB         int
	containment float64
	lift        float64
	joint       float64
	binding     float64
	unitSpan    int
	units       map[string]float64
	layers      map[string]int
}

func (s *viewState) hydrate(ctx context.Context) {
	hydrateBudget := len(s.selected)
	if hydrateBudget > maxViewUnits {
		hydrateBudget = maxViewUnits
	}
	started := time.Now()
	for i, u := range s.selected {
		if err := ctx.Err(); err != nil {
			break
		}
		var vu *hydratedUnit
		if i < hydrateBudget {
			vu = s.builder.cachedHydratedUnit(s.snap, u)
		}
		if vu == nil {
			vu = &hydratedUnit{
				unit:     u,
				summary:  u.Summary,
				span:     u.Span,
				surfaces: map[uint64]map[string]int{},
			}
			if i < hydrateBudget && time.Since(started) <= maxHydrateBudget {
				raw, _, _, err := s.builder.vault.LoadLocalDocument(u.Row.UnitID)
				if err == nil && strings.TrimSpace(raw) != "" {
					text := raw
					if len([]rune(text)) > maxGraphTextRunes {
						text = string([]rune(text)[:maxGraphTextRunes])
					}
					if summary := firstSentence(text, 54); summary != "" {
						vu.summary = summary
					}
					vu.span = clipText(text, maxGraphSpanRunes)
					extractLexical(s.builder.vault, s.snap, vu, text)
					s.builder.storeHydratedUnit(s.snap, vu)
				}
			}
		}
		s.mergeHydrated(vu)
	}
}

// mergeHydrated folds one hydrated unit's lexical projection into the view
// aggregates. The cached payload itself stays immutable across views.
func (s *viewState) mergeHydrated(vu *hydratedUnit) {
	if vu == nil {
		return
	}
	for group, counts := range vu.surfaces {
		dst := s.groupSurface[group]
		if dst == nil {
			dst = make(map[string]int, len(counts))
			s.groupSurface[group] = dst
		}
		for surface, count := range counts {
			dst[surface] += count
		}
	}
	s.units = append(s.units, vu)
}

// cachedHydratedUnit returns a copy of one cached unit projection, rebinding
// the snapshot-local *Unit pointer. Snapshots are immutable, so a cached
// projection stays valid until the builder's snapshot key changes.
func (b *Builder) cachedHydratedUnit(snap *Snapshot, u *Unit) *hydratedUnit {
	if b == nil || snap == nil || u == nil {
		return nil
	}
	b.lexMu.Lock()
	defer b.lexMu.Unlock()
	if b.lexKey != snap.Key {
		b.lexKey = snap.Key
		b.lexCache = make(map[string]*hydratedUnit, hydratedCacheLimit)
		b.lexOrder = nil
		return nil
	}
	cached := b.lexCache[u.Row.UnitID]
	if cached == nil {
		return nil
	}
	clone := *cached
	clone.unit = u
	return &clone
}

// storeHydratedUnit publishes one freshly hydrated projection into the
// per-snapshot cache with insertion-order eviction.
func (b *Builder) storeHydratedUnit(snap *Snapshot, vu *hydratedUnit) {
	if b == nil || snap == nil || vu == nil || vu.unit == nil {
		return
	}
	b.lexMu.Lock()
	defer b.lexMu.Unlock()
	if b.lexKey != snap.Key {
		b.lexKey = snap.Key
		b.lexCache = make(map[string]*hydratedUnit, hydratedCacheLimit)
		b.lexOrder = nil
	}
	id := vu.unit.Row.UnitID
	if _, exists := b.lexCache[id]; exists {
		return
	}
	if len(b.lexOrder) >= hydratedCacheLimit {
		oldest := b.lexOrder[0]
		b.lexOrder = b.lexOrder[1:]
		delete(b.lexCache, oldest)
	}
	b.lexCache[id] = vu
	b.lexOrder = append(b.lexOrder, id)
}

func extractLexical(vault *localvault.Vault, snap *Snapshot, vu *hydratedUnit, text string) {
	if snap == nil || snap.engine == nil || vu == nil || vu.unit == nil {
		return
	}
	pkt, _ := vault.PackBlindPacket(vu.unit.Row.UnitID, text, vu.unit.Row.OccurredAt)
	overlapPairs := hanRunWindowPairs(text)
	surfaces := make([]string, 0, 64)
	for _, sentence := range plain.SplitSentences(text) {
		for _, word := range plain.SplitWords(sentence) {
			surface := canonicalPhraseSurface(word)
			if surface == "" {
				continue
			}
			surfaces = append(surfaces, surface)
		}
	}
	overlapSurfaces := make([]string, 0, len(overlapPairs)*2)
	for _, pair := range overlapPairs {
		overlapSurfaces = append(overlapSurfaces, canonicalPhraseSurface(pair[0]), canonicalPhraseSurface(pair[1]))
	}
	hashes := vault.SurfaceHashes(append(surfaces, overlapSurfaces...))
	for _, surface := range surfaces {
		hash := hashes[surface]
		if hash == 0 || snap.engine.Index.TermDF(hash) <= 0 {
			continue
		}
		group := termGroupOf(snap, hash)
		if group == 0 {
			continue
		}
		counts := vu.surfaces[group]
		if counts == nil {
			counts = map[string]int{}
			vu.surfaces[group] = counts
		}
		counts[surface]++
	}
	// Adjacent sliding windows of one Han run overlap by construction, so their
	// co-occurrence is forced by the text and carries no association evidence.
	// They stay in the retrieval index (recall keys), but not in the phrase
	// projection: one character run projected twice is one observation.
	overlap := make(map[phraseKey]struct{}, len(overlapPairs))
	for _, pair := range overlapPairs {
		a := hashes[canonicalPhraseSurface(pair[0])]
		b := hashes[canonicalPhraseSurface(pair[1])]
		if a == 0 || b == 0 {
			continue
		}
		overlap[orderPhraseKey(a, b)] = struct{}{}
	}
	surfaceByHash := make(map[uint64]string, len(hashes))
	for surface, hash := range hashes {
		if hash != 0 {
			surfaceByHash[hash] = surface
		}
	}
	if pkt.SentenceFrames == nil {
		return
	}
	frames := make([][]plain.PulseSlot, 0, len(pkt.SentenceFrames))
	for _, frame := range pkt.SentenceFrames {
		slots := make([]plain.PulseSlot, 0, len(frame))
		for _, term := range frame {
			slots = append(slots, plain.PulseSlot{H: term.H, RawBitsX10: term.RawBitsX10, Flags: term.ClassFlags})
		}
		if len(slots) > 0 {
			frames = append(frames, slots)
		}
	}
	dyads := plain.ExtractPulsePrimaryDyads(frames)
	seen := map[phraseKey]struct{}{}
	kept := 0
	addPhrase := func(left, right uint64) {
		if kept >= maxPhraseCandidates {
			return
		}
		if left == 0 || right == 0 || left == right {
			return
		}
		key := orderPhraseKey(left, right)
		if _, skip := overlap[key]; skip {
			return
		}
		if decomposedHanUnigrams(surfaceByHash, hashes, snap.engine.Index, key.a, key.b) {
			return
		}
		if _, dup := seen[key]; dup {
			return
		}
		profile, ok := snap.engine.PhraseEnergyVector(key.a, key.b)
		if !ok || profile.BindingEnergyNats <= 0 {
			return
		}
		a, b := profile.TermA, profile.TermB
		if a == 0 || b == 0 || a == b {
			return
		}
		final := phraseKey{a: a, b: b}
		if _, dup := seen[final]; dup {
			return
		}
		seen[final] = struct{}{}
		dfA, dfB, containment, lift := phraseEvidence(snap.engine.Index, profile)
		vu.phrases = append(vu.phrases, rawPhrase{
			a:           a,
			b:           b,
			df:          profile.PhraseDF,
			dfA:         dfA,
			dfB:         dfB,
			containment: containment,
			lift:        lift,
			joint:       profile.JointSurprisalNats,
			binding:     profile.BindingEnergyNats,
		})
		kept++
	}
	for _, dyad := range dyads {
		addPhrase(dyad.Left, dyad.Right)
	}
	// The dual track emits every bigram before every unigram, so adjacent
	// primaries are always overlapping windows (dropped above). Recover the
	// disjoint tilings of each Han run instead: windows at stride two cover the
	// run with distinct text material, which is the only same-run pairing that
	// is not a tautology.
	for _, run := range hanRuns(text) {
		runes := []rune(run)
		for parity := 0; parity < 2; parity++ {
			for k := parity; k+4 <= len(runes); k += 2 {
				addPhrase(
					hashes[canonicalPhraseSurface(string(runes[k:k+2]))],
					hashes[canonicalPhraseSurface(string(runes[k+2:k+4]))],
				)
			}
		}
	}
}

// canonicalPhraseSurface mirrors the encoder's surface canonicalization so a
// projected run window hashes to the same term the index stored.
func canonicalPhraseSurface(surface string) string {
	return strings.ToLower(strings.TrimSpace(plain.CanonicalizeApostrophe(surface)))
}

// hanRunWindowPairs returns every pair of adjacent two-character sliding
// windows of the same Han run. Each pair shares one character, so it is one
// text run seen twice rather than two independent observations.
func hanRunWindowPairs(text string) [][2]string {
	var pairs [][2]string
	for _, run := range hanRuns(text) {
		runes := []rune(run)
		for k := 0; k+3 <= len(runes); k++ {
			pairs = append(pairs, [2]string{string(runes[k : k+2]), string(runes[k+1 : k+3])})
		}
	}
	return pairs
}

func hanRuns(text string) []string {
	var runs []string
	start := -1
	for i, r := range text {
		if unicode.Is(unicode.Han, r) {
			if start < 0 {
				start = i
			}
			continue
		}
		if start >= 0 {
			runs = append(runs, text[start:i])
			start = -1
		}
	}
	if start >= 0 {
		runs = append(runs, text[start:])
	}
	return runs
}

func orderPhraseKey(a, b uint64) phraseKey {
	if a > b {
		a, b = b, a
	}
	return phraseKey{a: a, b: b}
}

// phraseEvidence pairs the phrase document frequency with the two member
// document frequencies: containment measures how much of the rarer member's
// life is covered by the pair, lift measures the pair against the independence
// expectation dfA*dfB/N.
func phraseEvidence(idx *core.Index, profile core.PhraseEnergyProfile) (dfA, dfB int, containment, lift float64) {
	if idx == nil {
		return 0, 0, 0, 0
	}
	dfA = idx.TermDF(profile.TermA)
	dfB = idx.TermDF(profile.TermB)
	minDF := dfA
	if dfB < minDF {
		minDF = dfB
	}
	if minDF > 0 {
		containment = float64(profile.PhraseDF) / float64(minDF)
	}
	if dfA > 0 && dfB > 0 && idx.N > 0 {
		lift = float64(profile.PhraseDF) * float64(idx.N) / (float64(dfA) * float64(dfB))
	}
	return dfA, dfB, containment, lift
}

// decomposedHanUnigrams reports whether two single Han characters are the
// unigram track of one longer indexed term (e.g. 暴 + 露 behind 暴露). The
// maximality principle keeps the longer term and drops the decomposition from
// the phrase projection; the short tokens remain retrieval keys.
func decomposedHanUnigrams(surfaceByHash map[uint64]string, hashes map[string]uint64, idx *core.Index, a, b uint64) bool {
	if idx == nil {
		return false
	}
	sa, sb := surfaceByHash[a], surfaceByHash[b]
	ra, rb := []rune(sa), []rune(sb)
	if len(ra) != 1 || len(rb) != 1 || !unicode.Is(unicode.Han, ra[0]) || !unicode.Is(unicode.Han, rb[0]) {
		return false
	}
	joined := hashes[sa+sb]
	return joined != 0 && idx.TermDF(joined) > 0
}

func (s *viewState) termGroup(term uint64) uint64 {
	if s.snap != nil {
		if group, ok := s.snap.TermGroup[term]; ok && group != 0 {
			return group
		}
	}
	return term
}

func (s *viewState) termMassOf(group uint64) float64 {
	if mass, ok := s.termMass[group]; ok {
		return mass
	}
	mass := 0.0
	if s.snap.engine != nil && s.snap.engine.Index != nil {
		if m, ok := s.snap.engine.Index.TermMass(group); ok {
			mass = m
		}
	}
	s.termMass[group] = mass
	return mass
}

func (s *viewState) buildLabels() {
	for group, surfaces := range s.groupSurface {
		best := ""
		bestCount := 0
		for surface, count := range surfaces {
			if count > bestCount || (count == bestCount && (best == "" || surface < best)) {
				best = surface
				bestCount = count
			}
		}
		if best != "" {
			s.labels[group] = best
		}
	}
}

func (s *viewState) buildUnitPayloads() {
	for _, vu := range s.units {
		if vu == nil || vu.unit == nil {
			continue
		}
		unitID := vu.unit.Row.UnitID
		layer := vu.unit.Row.Layer

		type groupWeight struct {
			group  uint64
			weight float64
		}
		groups := make([]groupWeight, 0, len(vu.unit.GroupWeights))
		for group, weight := range vu.unit.GroupWeights {
			if group == 0 || weight <= 0 {
				continue
			}
			groups = append(groups, groupWeight{group: group, weight: weight})
		}
		sort.Slice(groups, func(i, j int) bool {
			if groups[i].weight != groups[j].weight {
				return groups[i].weight > groups[j].weight
			}
			return groups[i].group < groups[j].group
		})
		if len(groups) > s.wordsPerUnit {
			groups = groups[:s.wordsPerUnit]
		}
		for _, gw := range groups {
			agg := s.wordAgg[gw.group]
			if agg == nil {
				agg = &wordAgg{
					group:  gw.group,
					df:     termDF(s.snap, gw.group),
					mass:   s.termMassOf(gw.group),
					units:  map[string]float64{},
					layers: map[string]int{},
				}
				s.wordAgg[gw.group] = agg
			}
			agg.weight += gw.weight
			agg.unitSpan++
			agg.units[unitID] += gw.weight
			agg.layers[layer]++
		}

		phrases := append([]rawPhrase(nil), vu.phrases...)
		sort.Slice(phrases, func(i, j int) bool {
			if phrases[i].binding != phrases[j].binding {
				return phrases[i].binding > phrases[j].binding
			}
			if phrases[i].a != phrases[j].a {
				return phrases[i].a < phrases[j].a
			}
			return phrases[i].b < phrases[j].b
		})
		if len(phrases) > s.phrasesPerUnit {
			phrases = phrases[:s.phrasesPerUnit]
		}
		for _, raw := range phrases {
			key := phraseKey{a: raw.a, b: raw.b}
			agg := s.phraseAgg[key]
			if agg == nil {
				agg = &phraseAgg{
					a:           raw.a,
					b:           raw.b,
					df:          raw.df,
					dfA:         raw.dfA,
					dfB:         raw.dfB,
					containment: raw.containment,
					lift:        raw.lift,
					joint:       raw.joint,
					units:       map[string]float64{},
					layers:      map[string]int{},
				}
				s.phraseAgg[key] = agg
			}
			if agg.joint == 0 {
				agg.joint = raw.joint
			}
			agg.binding += raw.binding
			agg.unitSpan++
			agg.units[unitID] += raw.binding
			agg.layers[layer]++
		}
	}
}

func (s *viewState) assemble() ([]graphmodel.Node, []graphmodel.Edge, graphmodel.KindCounts, graphmodel.ClusterTypeCounts) {
	nodesByKey := map[string]*graphmodel.Node{}
	edges := make([]graphmodel.Edge, 0, 1024)
	kindCounts := graphmodel.KindCounts{}
	clusterTypeCounts := graphmodel.ClusterTypeCounts{}

	addNode := func(node graphmodel.Node) {
		if _, exists := nodesByKey[node.NodeKey]; exists {
			return
		}
		nodesByKey[node.NodeKey] = &node
	}
	addEdge := func(fromKey, toKey, relation, layer, warrant string, weight float64) {
		if fromKey == "" || toKey == "" || fromKey == toKey {
			return
		}
		edges = append(edges, graphmodel.Edge{
			FromKey:    fromKey,
			ToKey:      toKey,
			Relation:   relation,
			Layer:      layer,
			Warrant:    warrant,
			Certainty:  "confirmed (structural)",
			Confidence: 1,
			Status:     "active",
			Weight:     weight,
		})
	}

	unitPayloads := map[string]*hydratedUnit{}
	for _, vu := range s.units {
		if vu == nil || vu.unit == nil {
			continue
		}
		unitPayloads[vu.unit.Row.UnitID] = vu
		kind := graphmodel.KindUnit
		if vu.unit.Free {
			kind = graphmodel.KindFree
			kindCounts.FreeUnits++
		} else {
			kindCounts.Units++
		}
		node := graphmodel.Node{
			NodeKey:           "unit:" + vu.unit.Row.UnitID,
			Summary:           vu.summary,
			Label:             vu.summary,
			Kind:              kind,
			Layer:             vu.unit.Row.Layer,
			Free:              vu.unit.Free,
			PrimaryClusterKey: vu.unit.PrimaryCluster,
			Certainty:         "confirmed (structural)",
			Status:            "active",
			Confidence:        1,
			ObservedAt:        formatObservedAt(vu.unit.Row.OccurredAt),
			Anchor:            vu.unit.Anchor,
			Span:              vu.span,
			Weight:            1,
		}
		if vu.unit.HasEngine {
			node.EnergyNats = vu.unit.DominantWeight
		}
		addNode(node)
	}

	// Containers, memberships and citation edges.
	for _, vu := range s.units {
		if vu == nil || vu.unit == nil {
			continue
		}
		unitKey := "unit:" + vu.unit.Row.UnitID
		memberships := membershipKeys(s.snap, vu.unit)
		for _, clusterKey := range memberships {
			cluster := s.snap.ClusterByKey[clusterKey]
			if cluster == nil || !s.includeClusterType(cluster.Type) {
				continue
			}
			warrant := "secondary"
			if clusterKey == vu.unit.PrimaryCluster {
				warrant = "primary"
			}
			addEdge(unitKey, clusterKey, graphmodel.RelationMemberOf, vu.unit.Row.Layer, warrant, 1)
		}
		if vu.unit.Row.Source == graphmodel.SourceNoetic {
			for _, cited := range s.snap.Citations[vu.unit.Row.UnitID] {
				cited = strings.TrimSpace(cited)
				if cited == "" {
					continue
				}
				citedKey := "unit:" + cited
				if _, ok := unitPayloads[cited]; !ok {
					continue
				}
				addEdge(unitKey, citedKey, graphmodel.RelationCites, localvault.LayerNoetic, "citation", 1)
			}
		}
	}

	// Cluster nodes, only when at least one selected member belongs to them.
	clusterWords := map[string]map[uint64]float64{}
	clusterPhrases := map[string]map[phraseKey]float64{}
	for _, vu := range s.units {
		if vu == nil || vu.unit == nil {
			continue
		}
		for _, clusterKey := range membershipKeys(s.snap, vu.unit) {
			if clusterWords[clusterKey] == nil {
				clusterWords[clusterKey] = map[uint64]float64{}
			}
			for group, weight := range vu.unit.GroupWeights {
				clusterWords[clusterKey][group] += weight
			}
			if clusterPhrases[clusterKey] == nil {
				clusterPhrases[clusterKey] = map[phraseKey]float64{}
			}
			for _, raw := range vu.phrases {
				clusterPhrases[clusterKey][phraseKey{a: raw.a, b: raw.b}] += raw.binding
			}
		}
	}
	for clusterKey, cluster := range s.snap.ClusterByKey {
		if !s.includeClusterType(cluster.Type) {
			continue
		}
		if _, visible := clusterWords[clusterKey]; !visible {
			continue
		}
		node := graphmodel.Node{
			NodeKey:     clusterKey,
			Summary:     cluster.Summary,
			Label:       cluster.Label,
			Kind:        graphmodel.KindCluster,
			Layer:       cluster.Layer,
			ClusterType: cluster.Type,
			MemberCount: cluster.MemberCount,
			EnergyNats:  cluster.EnergyNats,
			Certainty:   "confirmed (structural)",
			Status:      "active",
			Confidence:  1,
			Anchor:      cluster.Anchor,
			Span:        cluster.Span,
			Weight:      float64(cluster.MemberCount),
			LayerMix:    cluster.LayerMix,
		}
		if cluster.Type == graphmodel.ClusterEntity {
			if label := s.labels[cluster.Group]; label != "" {
				node.Label = label
				node.Summary = fmt.Sprintf("实体簇 · %s · %d 个单元", label, cluster.MemberCount)
			}
		}
		node.Words = s.topClusterWords(clusterWords[clusterKey], 8)
		node.Phrases = s.topClusterPhrases(clusterPhrases[clusterKey], 4)
		addNode(node)
		clusterTypeCounts.Episode += boolInt(cluster.Type == graphmodel.ClusterEpisode)
		clusterTypeCounts.Entity += boolInt(cluster.Type == graphmodel.ClusterEntity)
		clusterTypeCounts.Source += boolInt(cluster.Type == graphmodel.ClusterSource)
		kindCounts.Clusters++
	}

	// co_cites: cluster-level resonance from one noetic unit's cited evidence.
	coCiteCounts := map[[2]string]float64{}
	for _, vu := range s.units {
		if vu == nil || vu.unit == nil || vu.unit.Row.Source != graphmodel.SourceNoetic {
			continue
		}
		clusters := make([]string, 0, 8)
		seenClusters := map[string]struct{}{}
		for _, cited := range s.snap.Citations[vu.unit.Row.UnitID] {
			target := s.snap.UnitByID[strings.TrimSpace(cited)]
			if target == nil || target.PrimaryCluster == "" {
				continue
			}
			if _, ok := nodesByKey[target.PrimaryCluster]; !ok {
				continue
			}
			if _, dup := seenClusters[target.PrimaryCluster]; dup {
				continue
			}
			seenClusters[target.PrimaryCluster] = struct{}{}
			clusters = append(clusters, target.PrimaryCluster)
			if len(clusters) >= 16 {
				break
			}
		}
		sort.Strings(clusters)
		for i := 0; i < len(clusters); i++ {
			for j := i + 1; j < len(clusters); j++ {
				coCiteCounts[[2]string{clusters[i], clusters[j]}]++
			}
		}
	}
	for pair, count := range coCiteCounts {
		addEdge(pair[0], pair[1], graphmodel.RelationCoCites, localvault.LayerEpisodic, "co-citation", count)
		addEdge(pair[1], pair[0], graphmodel.RelationCoCites, localvault.LayerEpisodic, "co-citation", count)
	}

	// Word nodes: phrase endpoints first so phrase_has_word always resolves.
	phraseKeys := s.selectPhrases()
	wordKeys := s.selectWords(phraseKeys)
	for _, group := range wordKeys {
		agg := s.wordAgg[group]
		if agg == nil {
			agg = &wordAgg{group: group, df: termDF(s.snap, group), mass: s.termMassOf(group), units: map[string]float64{}, layers: map[string]int{}}
		}
		label := s.labelForGroup(group)
		node := graphmodel.Node{
			NodeKey:    "word:" + hex16(group),
			Summary:    label,
			Label:      label,
			Kind:       graphmodel.KindWord,
			Layer:      "mixed",
			Certainty:  "confirmed (structural)",
			Status:     "active",
			Confidence: 1,
			Anchor:     "word://" + hex16(group),
			Span:       fmt.Sprintf("df=%d mass=%.2f", agg.df, agg.mass),
			Weight:     agg.weight,
			EnergyNats: agg.mass,
			LayerMix:   agg.layers,
			Words: []graphmodel.WordParticle{{
				Token:    label,
				GroupHex: hex16(group),
				DF:       agg.df,
				MassNats: agg.mass,
				Weight:   agg.weight,
				UnitSpan: agg.unitSpan,
			}},
		}
		addNode(node)
		for unitID, weight := range agg.units {
			unitKey := "unit:" + unitID
			if _, ok := nodesByKey[unitKey]; ok {
				addEdge(node.NodeKey, unitKey, graphmodel.RelationWordOf, agentLayer(s.snap, unitID), "word-family", weight)
			}
		}
		kindCounts.Words++
	}

	// Phrase nodes.
	for _, key := range phraseKeys {
		agg := s.phraseAgg[key]
		if agg == nil {
			continue
		}
		left := s.labelForGroup(s.termGroup(key.a))
		right := s.labelForGroup(s.termGroup(key.b))
		label := left + " · " + right
		nodeKey := "phrase:" + hex16(key.a) + ":" + hex16(key.b)
		node := graphmodel.Node{
			NodeKey:    nodeKey,
			Summary:    label,
			Label:      label,
			Kind:       graphmodel.KindPhrase,
			Layer:      "mixed",
			Certainty:  "confirmed (structural)",
			Status:     "active",
			Confidence: 1,
			Anchor:     "phrase://" + hex16(key.a) + ":" + hex16(key.b),
			Span:       fmt.Sprintf("df=%d dfA=%d dfB=%d contain=%.2f lift=%.2f J=%.2f binding=%.2f", agg.df, agg.dfA, agg.dfB, agg.containment, agg.lift, agg.joint, agg.binding),
			Weight:     agg.binding,
			EnergyNats: agg.binding,
			LayerMix:   agg.layers,
			Phrases: []graphmodel.PhraseFragment{{
				Left:               left,
				Right:              right,
				DF:                 agg.df,
				LeftDF:             agg.dfA,
				RightDF:            agg.dfB,
				Containment:        agg.containment,
				Lift:               agg.lift,
				JointSurprisalNats: agg.joint,
				BindingEnergyNats:  agg.binding,
				UnitSpan:           agg.unitSpan,
			}},
		}
		addNode(node)
		for unitID, weight := range agg.units {
			unitKey := "unit:" + unitID
			if _, ok := nodesByKey[unitKey]; ok {
				addEdge(nodeKey, unitKey, graphmodel.RelationPhraseOf, agentLayer(s.snap, unitID), "phrase", weight)
			}
		}
		if _, ok := nodesByKey["word:"+hex16(s.termGroup(key.a))]; ok {
			addEdge(nodeKey, "word:"+hex16(s.termGroup(key.a)), graphmodel.RelationPhraseHasWord, "mixed", "phrase-component", agg.binding)
		}
		if _, ok := nodesByKey["word:"+hex16(s.termGroup(key.b))]; ok {
			addEdge(nodeKey, "word:"+hex16(s.termGroup(key.b)), graphmodel.RelationPhraseHasWord, "mixed", "phrase-component", agg.binding)
		}
		kindCounts.Phrases++
	}

	// Deterministic IDs and degrees.
	nodeKeys := make([]string, 0, len(nodesByKey))
	for key := range nodesByKey {
		nodeKeys = append(nodeKeys, key)
	}
	sort.Strings(nodeKeys)
	idByKey := make(map[string]int, len(nodeKeys))
	used := make(map[int]struct{}, len(nodeKeys))
	for _, key := range nodeKeys {
		id := deterministicIntID(key)
		for {
			if _, taken := used[id]; !taken {
				break
			}
			id++
			if id <= 0 {
				id = 1
			}
		}
		used[id] = struct{}{}
		idByKey[key] = id
		nodesByKey[key].ID = id
		nodesByKey[key].NodeKey = key
	}
	degree := map[string]int{}
	nodes := make([]graphmodel.Node, 0, len(nodeKeys))
	for _, key := range nodeKeys {
		nodes = append(nodes, *nodesByKey[key])
	}
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].FromKey != edges[j].FromKey {
			return edges[i].FromKey < edges[j].FromKey
		}
		if edges[i].Relation != edges[j].Relation {
			return edges[i].Relation < edges[j].Relation
		}
		return edges[i].ToKey < edges[j].ToKey
	})
	deduped := edges[:0]
	seenEdge := map[string]struct{}{}
	for _, e := range edges {
		key := e.FromKey + "|" + e.Relation + "|" + e.ToKey
		if _, dup := seenEdge[key]; dup {
			continue
		}
		seenEdge[key] = struct{}{}
		deduped = append(deduped, e)
	}
	edges = deduped
	for i := range edges {
		edges[i].ID = i + 1
		edges[i].From = idByKey[edges[i].FromKey]
		edges[i].To = idByKey[edges[i].ToKey]
		edges[i].Assertion = edges[i].FromKey + "|" + edges[i].Relation + "|" + edges[i].ToKey
		from := nodesByKey[edges[i].FromKey]
		to := nodesByKey[edges[i].ToKey]
		if from != nil {
			edges[i].FromLabel = from.Summary
		}
		if to != nil {
			edges[i].ToLabel = to.Summary
		}
		degree[edges[i].FromKey]++
		degree[edges[i].ToKey]++
	}
	for i := range nodes {
		nodes[i].Degree = degree[nodes[i].NodeKey]
	}
	return nodes, edges, kindCounts, clusterTypeCounts
}

func (s *viewState) includeClusterType(clusterType string) bool {
	if len(s.clusterTypes) == 0 {
		return true
	}
	return s.clusterTypes[clusterType]
}

func (s *viewState) selectPhrases() []phraseKey {
	keys := make([]phraseKey, 0, len(s.phraseAgg))
	for key := range s.phraseAgg {
		if !s.displayablePhrase(key) {
			continue
		}
		keys = append(keys, key)
	}
	s.filteredPhrases = len(s.phraseAgg) - len(keys)
	sort.Slice(keys, func(i, j int) bool {
		ai, aj := s.phraseAgg[keys[i]], s.phraseAgg[keys[j]]
		if ai.binding != aj.binding {
			return ai.binding > aj.binding
		}
		if keys[i].a != keys[j].a {
			return keys[i].a < keys[j].a
		}
		return keys[i].b < keys[j].b
	})
	if len(keys) > s.phraseBudget {
		keys = keys[:s.phraseBudget]
	}
	return keys
}

func (s *viewState) selectWords(phraseKeys []phraseKey) []uint64 {
	endpoint := map[uint64]struct{}{}
	for _, key := range phraseKeys {
		if group := s.termGroup(key.a); group != 0 && s.displayableWord(group) {
			endpoint[group] = struct{}{}
		}
		if group := s.termGroup(key.b); group != 0 && s.displayableWord(group) {
			endpoint[group] = struct{}{}
		}
	}
	keys := make([]uint64, 0, len(s.wordAgg))
	for group := range s.wordAgg {
		if !s.displayableWord(group) {
			continue
		}
		keys = append(keys, group)
	}
	s.filteredWords = len(s.wordAgg) - len(keys)
	sort.Slice(keys, func(i, j int) bool {
		ai, aj := s.wordAgg[keys[i]], s.wordAgg[keys[j]]
		if ai.weight != aj.weight {
			return ai.weight > aj.weight
		}
		return keys[i] < keys[j]
	})
	selected := make([]uint64, 0, s.wordBudget)
	seen := map[uint64]struct{}{}
	for group := range endpoint {
		if len(selected) >= s.wordBudget {
			break
		}
		if _, dup := seen[group]; dup {
			continue
		}
		seen[group] = struct{}{}
		selected = append(selected, group)
	}
	for _, group := range keys {
		if len(selected) >= s.wordBudget {
			break
		}
		if _, dup := seen[group]; dup {
			continue
		}
		seen[group] = struct{}{}
		selected = append(selected, group)
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i] < selected[j] })
	return selected
}

func (s *viewState) topClusterWords(weights map[uint64]float64, limit int) []graphmodel.WordParticle {
	type item struct {
		group  uint64
		weight float64
	}
	items := make([]item, 0, len(weights))
	for group, weight := range weights {
		if group == 0 || weight <= 0 || !s.displayableWord(group) {
			continue
		}
		items = append(items, item{group: group, weight: weight})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].weight != items[j].weight {
			return items[i].weight > items[j].weight
		}
		return items[i].group < items[j].group
	})
	if len(items) > limit {
		items = items[:limit]
	}
	out := make([]graphmodel.WordParticle, 0, len(items))
	for _, it := range items {
		out = append(out, graphmodel.WordParticle{
			Token:    s.labelForGroup(it.group),
			GroupHex: hex16(it.group),
			DF:       termDF(s.snap, it.group),
			MassNats: s.termMassOf(it.group),
			Weight:   it.weight,
		})
	}
	return out
}

func (s *viewState) topClusterPhrases(weights map[phraseKey]float64, limit int) []graphmodel.PhraseFragment {
	type item struct {
		key     phraseKey
		binding float64
	}
	items := make([]item, 0, len(weights))
	for key, binding := range weights {
		if binding <= 0 || !s.displayablePhrase(key) {
			continue
		}
		items = append(items, item{key: key, binding: binding})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].binding != items[j].binding {
			return items[i].binding > items[j].binding
		}
		if items[i].key.a != items[j].key.a {
			return items[i].key.a < items[j].key.a
		}
		return items[i].key.b < items[j].key.b
	})
	if len(items) > limit {
		items = items[:limit]
	}
	out := make([]graphmodel.PhraseFragment, 0, len(items))
	for _, it := range items {
		agg := s.phraseAgg[it.key]
		df, dfA, dfB, joint, containment, lift := 0, 0, 0, 0.0, 0.0, 0.0
		if agg != nil {
			df, dfA, dfB, joint = agg.df, agg.dfA, agg.dfB, agg.joint
			containment, lift = agg.containment, agg.lift
		}
		out = append(out, graphmodel.PhraseFragment{
			Left:               s.labelForGroup(s.termGroup(it.key.a)),
			Right:              s.labelForGroup(s.termGroup(it.key.b)),
			DF:                 df,
			LeftDF:             dfA,
			RightDF:            dfB,
			Containment:        containment,
			Lift:               lift,
			JointSurprisalNats: joint,
			BindingEnergyNats:  it.binding,
		})
	}
	return out
}

// backgroundDisplayLimit returns the corpus background document-frequency
// ceiling. Zero disables the display projection (no index available).
func backgroundDisplayLimit(snap *Snapshot) int {
	if snap == nil || snap.engine == nil || snap.engine.Index == nil {
		return 0
	}
	return snap.engine.Index.HighDFThreshold()
}

// displayableWord reports whether a word family is a consumer-facing label:
// it needs a primary surface and must not be corpus background.
func (s *viewState) displayableWord(group uint64) bool {
	if group == 0 || s.labels[group] == "" {
		return false
	}
	if s.backgroundDF > 0 && termDF(s.snap, group) > s.backgroundDF {
		return false
	}
	return true
}

func (s *viewState) displayablePhrase(key phraseKey) bool {
	return s.displayableWord(s.termGroup(key.a)) && s.displayableWord(s.termGroup(key.b))
}

func (s *viewState) labelForGroup(group uint64) string {
	if label := s.labels[group]; label != "" {
		return label
	}
	h := hex16(group)
	if len(h) > 8 {
		h = h[:8]
	}
	return "词族 #" + h
}

func (s *viewState) stats(nodes []graphmodel.Node, edges []graphmodel.Edge, kinds graphmodel.KindCounts, clusters graphmodel.ClusterTypeCounts) graphmodel.Stats {
	layerCounts := map[string]int{}
	unitNodes, unitDegreeSum, isolated, multi := 0, 0, 0, 0
	labeledWords, totalWords := 0, 0
	for i := range nodes {
		node := nodes[i]
		switch node.Kind {
		case graphmodel.KindUnit, graphmodel.KindFree:
			unitNodes++
			layerCounts[node.Layer]++
			unitDegreeSum += node.Degree
			if node.Degree == 0 {
				isolated++
			}
			if node.Degree > 1 {
				multi++
			}
		case graphmodel.KindWord:
			totalWords++
			if !strings.HasPrefix(node.Label, "词族 #") {
				labeledWords++
			}
		}
	}
	avgDegree := 0.0
	multiRatio := 0.0
	if unitNodes > 0 {
		avgDegree = float64(unitDegreeSum) / float64(unitNodes)
		multiRatio = float64(multi) / float64(unitNodes)
	}
	relationReuse := 0.0
	if rels := relationOptions(edges); len(rels) > 0 {
		relationReuse = float64(len(edges)) / float64(len(rels))
	}
	labelCoverage := 0.0
	if totalWords > 0 {
		labelCoverage = float64(labeledWords) * 100 / float64(totalWords)
	}
	return graphmodel.Stats{
		EventTotal:           len(s.snap.Units),
		LinkTotal:            len(edges),
		NodeShown:            len(nodes),
		EdgeShown:            len(edges),
		IsolatedShown:        isolated,
		AvgEntityDegree:      round2(avgDegree),
		MultiEdgeEntityRatio: round2(multiRatio),
		AvgRelationReuse:     round2(relationReuse),
		ObjectNodeRatio:      100,
		LayerNodeCounts:      layerCounts,
		WordTotal:            kinds.Words,
		PhraseTotal:          kinds.Phrases,
		ClusterTotal:         kinds.Clusters,
		FreeUnitTotal:        kinds.FreeUnits,
		KindCounts:           kinds,
		ClusterTypeCounts:    clusters,
		LabelCoveragePct:     round2(labelCoverage),
	}
}

func relationOptions(edges []graphmodel.Edge) []graphmodel.RelationOption {
	counts := map[string]int{}
	for _, edge := range edges {
		counts[edge.Relation]++
	}
	out := make([]graphmodel.RelationOption, 0, len(counts))
	for relation, count := range counts {
		out = append(out, graphmodel.RelationOption{
			Relation: relation,
			Count:    count,
			Classes:  relationClasses(relation),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Relation < out[j].Relation
	})
	return out
}

func relationClasses(relation string) []graphmodel.RelationClass {
	switch relation {
	case graphmodel.RelationMemberOf, graphmodel.RelationWordOf, graphmodel.RelationPhraseOf, graphmodel.RelationPhraseHasWord:
		return []graphmodel.RelationClass{{Key: "structure", Display: "结构"}}
	case graphmodel.RelationCites:
		return []graphmodel.RelationClass{{Key: "evidence", Display: "证据"}}
	case graphmodel.RelationCoCites:
		return []graphmodel.RelationClass{{Key: "resonance", Display: "共振"}}
	default:
		return []graphmodel.RelationClass{{Key: "relation", Display: "关系"}}
	}
}

func membershipKeys(snap *Snapshot, u *Unit) []string {
	if u == nil {
		return nil
	}
	out := make([]string, 0, 3)
	if ep := snap.Episodes[u.EpisodeID]; u.EpisodeID >= 0 && ep != nil && ep.MemberCount > 1 {
		out = append(out, ep.Key)
	}
	if u.SourceCluster != "" {
		out = append(out, u.SourceCluster)
	}
	if u.EntityClusterKey != "" {
		out = append(out, u.EntityClusterKey)
	}
	return out
}

func selectUnits(snap *Snapshot, limit int, keyword string, layers map[string]bool) ([]*Unit, int) {
	keyword = strings.ToLower(strings.TrimSpace(keyword))
	filtered := make([]*Unit, 0, len(snap.Units))
	for _, u := range snap.Units {
		if len(layers) > 0 && !layers[u.Row.Layer] {
			continue
		}
		if keyword != "" && !unitMatchesKeyword(u, keyword) {
			continue
		}
		filtered = append(filtered, u)
	}
	total := len(filtered)
	if keyword != "" {
		selected := append([]*Unit(nil), filtered...)
		sortUnits(selected)
		seen := make(map[string]struct{}, len(selected))
		for _, u := range selected {
			seen[u.Row.UnitID] = struct{}{}
		}
		// Keep one-hop cluster context for keyword hits.
		for _, u := range append([]*Unit(nil), selected...) {
			cluster := snap.ClusterByKey[u.PrimaryCluster]
			if cluster == nil {
				continue
			}
			for _, memberID := range cluster.MemberIDs {
				if len(selected) >= limit {
					break
				}
				if _, dup := seen[memberID]; dup {
					continue
				}
				member := snap.UnitByID[memberID]
				if member == nil || (len(layers) > 0 && !layers[member.Row.Layer]) {
					continue
				}
				seen[memberID] = struct{}{}
				selected = append(selected, member)
			}
			if len(selected) >= limit {
				break
			}
		}
		if len(selected) > limit {
			selected = selected[:limit]
		}
		return selected, total
	}

	buckets := map[string][]*Unit{
		localvault.LayerNoetic:     {},
		localvault.LayerBackground: {},
		localvault.LayerEpisodic:   {},
	}
	for _, u := range filtered {
		buckets[u.Row.Layer] = append(buckets[u.Row.Layer], u)
	}
	for layer := range buckets {
		sortUnits(buckets[layer])
	}
	selected := make([]*Unit, 0, limit)
	cursor := map[string]int{}
	order := []struct {
		layer string
		take  int
	}{
		{localvault.LayerNoetic, 1},
		{localvault.LayerBackground, 2},
		{localvault.LayerEpisodic, 2},
	}
	for len(selected) < limit {
		progressed := false
		for _, spec := range order {
			bucket := buckets[spec.layer]
			for n := 0; n < spec.take && len(selected) < limit; n++ {
				idx := cursor[spec.layer]
				if idx >= len(bucket) {
					break
				}
				selected = append(selected, bucket[idx])
				cursor[spec.layer] = idx + 1
				progressed = true
			}
		}
		if !progressed {
			break
		}
	}
	return selected, total
}

func selectSeedUnits(snap *Snapshot, req graphmodel.ViewRequest, limit int, layers map[string]bool) ([]*Unit, int) {
	selected := map[string]*Unit{}
	add := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" {
			return
		}
		u := snap.UnitByID[id]
		if u == nil {
			return
		}
		if len(layers) > 0 && !layers[u.Row.Layer] {
			return
		}
		selected[id] = u
	}
	for _, raw := range req.SeedKeys {
		key := strings.TrimSpace(raw)
		switch {
		case strings.HasPrefix(key, "unit:"):
			add(strings.TrimPrefix(key, "unit:"))
		case strings.HasPrefix(key, "cluster:"):
			if cluster := snap.ClusterByKey[key]; cluster != nil {
				for _, id := range cluster.MemberIDs {
					add(id)
				}
			}
		case strings.HasPrefix(key, "word:"):
			group, err := strconv.ParseUint(strings.TrimPrefix(key, "word:"), 16, 64)
			if err == nil {
				for _, u := range snap.Units {
					if u.GroupWeights[group] > 0 {
						add(u.Row.UnitID)
					}
				}
			}
		case strings.HasPrefix(key, "phrase:"):
			parts := strings.Split(strings.TrimPrefix(key, "phrase:"), ":")
			if len(parts) != 2 {
				continue
			}
			a, errA := strconv.ParseUint(parts[0], 16, 64)
			b, errB := strconv.ParseUint(parts[1], 16, 64)
			if errA != nil || errB != nil {
				continue
			}
			ga := termGroupOf(snap, a)
			gb := termGroupOf(snap, b)
			for _, u := range snap.Units {
				if u.GroupWeights[ga] > 0 && u.GroupWeights[gb] > 0 {
					add(u.Row.UnitID)
				}
			}
		}
	}

	rounds := req.Depth
	if rounds <= 0 {
		rounds = 1
	}
	if rounds > 3 {
		rounds = 3
	}
	frontier := make([]string, 0, len(selected))
	for id := range selected {
		frontier = append(frontier, id)
	}
	for round := 0; round < rounds; round++ {
		next := make([]string, 0, len(frontier))
		for _, id := range frontier {
			u := snap.UnitByID[id]
			if u == nil {
				continue
			}
			if cluster := snap.ClusterByKey[u.PrimaryCluster]; cluster != nil {
				for _, memberID := range cluster.MemberIDs {
					if _, seen := selected[memberID]; !seen {
						add(memberID)
						if _, now := selected[memberID]; now {
							next = append(next, memberID)
						}
					}
				}
			}
			for _, cited := range snap.Citations[id] {
				cited = strings.TrimSpace(cited)
				if _, seen := selected[cited]; seen {
					continue
				}
				add(cited)
				if _, now := selected[cited]; now {
					next = append(next, cited)
				}
			}
			for _, citing := range snap.CitedBy[id] {
				if _, seen := selected[citing]; seen {
					continue
				}
				add(citing)
				if _, now := selected[citing]; now {
					next = append(next, citing)
				}
			}
		}
		frontier = next
		if len(frontier) == 0 {
			break
		}
	}

	out := make([]*Unit, 0, len(selected))
	for _, u := range selected {
		out = append(out, u)
	}
	sortUnits(out)
	total := len(out)
	if limit < len(out) {
		out = out[:limit]
	}
	return out, total
}

func termGroupOf(snap *Snapshot, term uint64) uint64 {
	if snap != nil {
		if group, ok := snap.TermGroup[term]; ok && group != 0 {
			return group
		}
	}
	return term
}

func sortUnits(units []*Unit) {
	sort.SliceStable(units, func(i, j int) bool {
		if units[i].Row.OccurredAt != units[j].Row.OccurredAt {
			return units[i].Row.OccurredAt > units[j].Row.OccurredAt
		}
		return units[i].Row.UnitID < units[j].Row.UnitID
	})
}

func unitMatchesKeyword(u *Unit, keyword string) bool {
	if u == nil {
		return false
	}
	fields := []string{u.Row.UnitID, u.Summary, u.Row.RelPath, u.Row.Anchor, u.Row.QueryText, u.Row.AnswerText, u.Row.RefPointer, u.Row.AddressURI, u.Row.Locus}
	for _, field := range fields {
		if strings.Contains(strings.ToLower(field), keyword) {
			return true
		}
	}
	return false
}

func normalizeLimit(limit int) int {
	if limit < 0 {
		return maxGraphLimit
	}
	if limit == 0 {
		return defaultGraphLimit
	}
	if limit > maxGraphLimit {
		return maxGraphLimit
	}
	return limit
}

func normalizeLayerSet(layers []string) map[string]bool {
	if len(layers) == 0 {
		return nil
	}
	out := map[string]bool{}
	for _, layer := range layers {
		switch strings.ToLower(strings.TrimSpace(layer)) {
		case localvault.LayerBackground, "bg":
			out[localvault.LayerBackground] = true
		case localvault.LayerNoetic, "qa", "insight":
			out[localvault.LayerNoetic] = true
		case localvault.LayerEpisodic:
			out[localvault.LayerEpisodic] = true
		}
	}
	return out
}

func normalizeClusterTypeSet(types []string) map[string]bool {
	if len(types) == 0 {
		return nil
	}
	out := map[string]bool{}
	for _, clusterType := range types {
		switch strings.ToLower(strings.TrimSpace(clusterType)) {
		case graphmodel.ClusterEpisode:
			out[graphmodel.ClusterEpisode] = true
		case graphmodel.ClusterEntity:
			out[graphmodel.ClusterEntity] = true
		case graphmodel.ClusterSource:
			out[graphmodel.ClusterSource] = true
		}
	}
	return out
}

func firstSentence(text string, maxRunes int) string {
	for _, sentence := range plain.SplitSentences(text) {
		sentence = strings.TrimSpace(sentence)
		if sentence == "" {
			continue
		}
		return clipText(sentence, maxRunes)
	}
	return ""
}

func agentLayer(snap *Snapshot, unitID string) string {
	if u := snap.UnitByID[unitID]; u != nil {
		return u.Row.Layer
	}
	return localvault.LayerEpisodic
}

func termDF(snap *Snapshot, group uint64) int {
	if snap == nil || snap.engine == nil || snap.engine.Index == nil {
		return 0
	}
	return snap.engine.Index.TermDF(group)
}

func deterministicIntID(key string) int {
	var h uint32 = 2166136261
	for i := 0; i < len(key); i++ {
		h ^= uint32(key[i])
		h *= 16777619
	}
	id := int(h & 0x7fffffff)
	if id == 0 {
		id = 1
	}
	return id
}

func formatObservedAt(ts int64) string {
	if ts <= 0 {
		return ""
	}
	return time.Unix(ts, 0).UTC().Format("2006-01-02 15:04:05")
}

func round2(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return math.Round(v*100) / 100
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func viewCacheKey(snap *Snapshot, req graphmodel.ViewRequest) string {
	key := snap.Key
	if snap.engine != nil && snap.engine.Index != nil {
		key += fmt.Sprintf("|gen=%d", snap.engine.Index.StateGeneration())
	}
	return fmt.Sprintf("%s|op=%s|limit=%d|kw=%s|layers=%s|ctypes=%s|seeds=%s|depth=%d",
		key, strings.TrimSpace(req.Op), req.Limit, strings.TrimSpace(req.Keyword),
		strings.Join(req.Layers, ","), strings.Join(req.ClusterTypes, ","),
		strings.Join(req.SeedKeys, ","), req.Depth)
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
