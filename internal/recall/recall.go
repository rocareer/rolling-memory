// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

// Package recall implements read-side assembly: seeds -> graph walk ->
// adjudication -> bands/chains.
//
// The read path is zero-LLM by default and deterministic for the same
// database, scope, cue, as-of, reader and caps. absent is a first-class state:
// an empty result means "this local graph does not hold it", not an error.
package recall

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/rocareer/rolling-memory/internal/clock"
	"github.com/rocareer/rolling-memory/internal/config"
	"github.com/rocareer/rolling-memory/internal/ids"
	"github.com/rocareer/rolling-memory/internal/memcore"
	"github.com/rocareer/rolling-memory/internal/store"
)

var gapLabels = map[string]string{
	"no_time":             "缺时间",
	"bad_anchor":          "锚不可回源",
	"source_unavailable":  "来源不可回源",
	"index_incomplete":    "情景索引不完整",
	"bare_excluded":       "仅提名（bare）",
	"state_excluded":      "状态排除",
	"audience_excluded":   "受众排除",
	"conflicts":           "冲突待裁定",
	"unresolved_mentions": "未决提及",
	"budget_exceeded":     "预算截止",
	"empty_graph":         "图面为空",
}

var gapOrder = []string{
	"no_time", "bad_anchor", "source_unavailable", "index_incomplete", "bare_excluded", "state_excluded",
	"audience_excluded", "conflicts", "unresolved_mentions",
	"budget_exceeded", "empty_graph",
}

// Read-side closure walk: same-as folds identity, subtype-of / inverse-of widen
// nomination (read-chain closure).
var readClosureKinds = []string{"same-as", "subtype-of", "part-of", "inverse-of", "same_as", "subtype_of", "part_of", "inverse_of"}

var relativePatterns = compileRelativePatterns()

// Options carries recall keyword arguments.
type Options struct {
	Cue             string
	Scope           string
	Reader          string
	Mode            string
	AsOf            int64
	WindowStart     int64
	WindowEnd       int64
	CapsBands       *int
	CapsNodes       *int
	CapsChars       *int
	CapsChains      *int
	CapsRows        *int
	Offset          int
	Depth           int
	IncludeRaw      *bool
	BudgetMS        *int
	RawLimit        int
	Session         *string
	Structured      map[string]string
	DeepMeta        map[string]any
	TimeRef         int64
	TokenCorrect    *bool
	AliasExpand     *bool
	RelativeTag     *bool
	MentionBackfill *bool
	PresenceLocus   string
	Presence        *memcore.PresenceCoordinates
	ReaderSubject   string
	SearchEngine    string
	ResultUnit      string
	Rank            *bool
	AttachFiles     *bool
	VectorWarning   any
}

// Result is the assembled recall result.
type Result struct {
	Scope   string
	Bands   []map[string]any
	RawHits []map[string]any
	Gaps    map[string]int
	Absent  bool
	Meta    map[string]any
}

// ToDict renders the full result shape.
func (r *Result) ToDict() map[string]any {
	gaps := map[string]any{}
	for key, value := range r.Gaps {
		if value != 0 {
			gaps[key] = value
		}
	}
	return map[string]any{
		"scope":    r.Scope,
		"bands":    r.Bands,
		"raw_hits": r.RawHits,
		"absent":   r.Absent,
		"gaps":     gaps,
		"meta":     r.Meta,
	}
}

// ToProbe renders the compact probe shape (hits capped at 12).
func (r *Result) ToProbe() map[string]any {
	hits := []any{}
	seen := map[string]struct{}{}
	for _, band := range r.Bands {
		nodes, _ := band["nodes"].([]map[string]any)
		for _, node := range nodes {
			nodeID := asString(node["id"])
			if _, ok := seen[nodeID]; ok {
				continue
			}
			seen[nodeID] = struct{}{}
			hit := map[string]any{
				"id":      nodeID,
				"summary": ids.Truncate(strings.TrimSpace(plainLine(node["content"])), 160, "…"),
				"date":    defaultAny(node["time"], "unknown"),
				"status":  defaultAny(node["status"], "active"),
				"anchor":  defaultAny(node["anchor"], ""),
			}
			// 两条血统的同名字段含义不同，必须分开，且不得把「本组内相对强度」当定性判据：
			//   - 本地断言图血统：节点 confidence 是抽取时写入的置信度（rule-v1 对仅提及片段写 0.35），
			//     可以在收据上附 fact / mention_only 的定性标注（mentionOnlyConfidence）。
			//   - thin-vault 血统：只有 relative_strength（榜首恒为 1.00 的本组内相对强度），
			//     它不含「是否已成事实」的信息，故只原样透传，不据此下任何判断。
			if writerConfidence, ok := node["confidence"]; ok {
				hit["confidence"] = writerConfidence
				hit["evidence"] = probeEvidence(writerConfidence)
			}
			if relStrength, ok := node["relative_strength"]; ok {
				hit["relative_strength"] = relStrength
			}
			if anchorOK, ok := node["anchor_ok"].(bool); ok && !anchorOK {
				hit["anchor_ok"] = false
			}
			hits = append(hits, hit)
			if len(hits) >= 12 {
				break
			}
		}
		if len(hits) >= 12 {
			break
		}
	}
	gaps := map[string]any{}
	for key, value := range r.Gaps {
		if value != 0 {
			gaps[key] = value
		}
	}
	return map[string]any{
		"scope":  r.Scope,
		"absent": r.Absent,
		"hits":   hits,
		"gaps":   gaps,
		"meta":   r.Meta,
	}
}

// mentionOnlyConfidence is the rule extractor's write floor (rule-v1 writes
// 0.35): probe hits at or below it are mention-backed fragments, not settled
// facts. The distinction is spelled out instead of inferred from a number (L6).
const mentionOnlyConfidence = 0.4

// probeEvidence labels one probe hit as "fact" or "mention_only".
func probeEvidence(confidence any) string {
	if asFloat(confidence, 1.0) <= mentionOnlyConfidence {
		return "mention_only"
	}
	return "fact"
}

// Render renders the recall block, truncating at maxChars.
func (r *Result) Render(maxChars int) string {
	return renderRecall(r, maxChars)
}

// Recall assembles bands/chains, raw fallback and gap counters.
func Recall(memory *store.Store, cfg *config.Config, opts Options) (*Result, error) {
	started := clock.MonotonicMS()
	startedSeconds := started / 1000.0
	scope := opts.Scope
	if scope == "" {
		scope = cfg.Scope
	}
	capsBands := pickCap(opts.CapsBands, cfg.Recall.Bands)
	capsNodes := pickCap(opts.CapsNodes, cfg.Recall.Nodes)
	capsChars := pickCap(opts.CapsChars, cfg.Recall.Chars)
	capsChains := pickCap(opts.CapsChains, cfg.Recall.Chains)
	if capsChains <= 0 {
		capsChains = 8
	}
	capsRows := pickCap(opts.CapsRows, cfg.Recall.Rows)
	if capsRows <= 0 {
		capsRows = 12
	}
	budget := cfg.Recall.BudgetMS
	if opts.BudgetMS != nil {
		budget = *opts.BudgetMS
	}
	// Compute the recall deadline in seconds from the millisecond budget (with a
	// 50ms floor).
	deadline := startedSeconds + mathMax(0.05, float64(budget)/1000.0)
	tokenCorrect := cfg.Recall.TokenCorrect
	if opts.TokenCorrect != nil {
		tokenCorrect = *opts.TokenCorrect
	}
	aliasExpand := cfg.Recall.AliasExpand
	if opts.AliasExpand != nil {
		aliasExpand = *opts.AliasExpand
	}
	relativeTag := cfg.Recall.RelativeTag
	if opts.RelativeTag != nil {
		relativeTag = *opts.RelativeTag
	}
	mentionBackfill := cfg.Recall.MentionBackfill
	if opts.MentionBackfill != nil {
		mentionBackfill = *opts.MentionBackfill
	}
	// The raw-leg failure note is per read chain (the store is long-lived), so
	// reset it before the legs run.
	memory.ClearRawError()

	hintParts := []string{}
	for _, key := range []string{"s", "x", "o"} {
		if value := strings.TrimSpace(opts.Structured[key]); value != "" {
			hintParts = append(hintParts, value)
		}
	}
	parts := append([]string{}, hintParts...)
	if cue := strings.TrimSpace(opts.Cue); cue != "" {
		parts = append(parts, cue)
	}
	query := strings.TrimSpace(strings.Join(parts, " "))
	depth := opts.Depth
	if depth == 0 {
		depth = 2
	}
	if query == "" {
		return &Result{
			Scope:   scope,
			Bands:   []map[string]any{},
			RawHits: []map[string]any{},
			Gaps:    map[string]int{"empty_graph": 1},
			Absent:  true,
			Meta: map[string]any{
				"reader":     opts.Reader,
				"mode":       opts.Mode,
				"elapsed_ms": 0,
				"caps":       map[string]any{"bands": capsBands, "nodes": capsNodes, "chars": capsChars},
				"offset":     opts.Offset,
				"depth":      depth,
				"legs":       map[string]int{},
				"seeds":      0,
				"candidates": 0,
				"truncated":  map[string]any{"bands": false, "nodes": false, "chars": false},
			},
		}, nil
	}

	var cacheKey string
	if DecisionCacheOn() && !strings.HasPrefix(scope, "mem:golden:") {
		cacheKey = DecisionCacheKey(cfg, scope, query, opts)
		if cached, ok := DecisionCacheGet(cacheKey); ok {
			cached.Render(capsChars)
			return cached, nil
		}
	}

	windowStart, windowEnd := opts.WindowStart, opts.WindowEnd
	window := map[string]any{"start": int64(0), "end": int64(0), "label": "", "derived": false}
	var boostWindow *store.Window
	boostMonth := 0
	if opts.WindowStart == 0 && opts.WindowEnd == 0 {
		derivedStart, derivedEnd, derivedLabel := ids.ParseQueryWindow(query)
		if derivedStart != 0 && derivedEnd != 0 {
			boostWindow = &store.Window{Start: derivedStart, End: derivedEnd, Label: derivedLabel}
			window = map[string]any{
				"start": derivedStart, "end": derivedEnd, "label": derivedLabel, "derived": true,
			}
		} else if month := ids.ParseQueryMonth(query); month != 0 {
			boostMonth = month
			window = map[string]any{
				"start": int64(0), "end": int64(0),
				"label": fmt.Sprintf("month:%02d", month), "derived": true, "month": month,
			}
		}
	} else {
		window = map[string]any{
			"start": opts.WindowStart, "end": opts.WindowEnd, "label": "", "derived": false,
		}
	}

	if opts.TimeRef != 0 && opts.WindowStart == 0 && opts.WindowEnd == 0 &&
		boostWindow == nil && boostMonth == 0 {
		relativePhrase := findRelativePhrase(query, opts.TimeRef)
		resolvedRelative := int64(0)
		if relativePhrase != "" {
			ref := opts.TimeRef
			resolvedRelative = ids.ParseTime(relativePhrase, ids.ParseTimeOptions{TimeRef: &ref, Default: 0})
		}
		if relativePhrase != "" && resolvedRelative != 0 {
			relStart, relEnd := relativeSpan(relativePhrase, resolvedRelative)
			boostWindow = &store.Window{Start: relStart, End: relEnd, Label: "relative:" + relativePhrase}
			window = map[string]any{
				"start": int64(0), "end": int64(0), "label": "relative:" + relativePhrase,
				"derived": true, "relative": true, "time_ref": opts.TimeRef,
			}
		}
	}

	if boostWindow != nil {
		minTS, maxTS, err := memory.TimeRange(scope)
		if err != nil {
			return nil, err
		}
		if minTS != 0 && (boostWindow.End < minTS-366*86400 || boostWindow.Start > maxTS+366*86400) {
			boostWindow = nil
			window = map[string]any{
				"start": int64(0), "end": int64(0), "label": "", "derived": false,
				"rejected": "outside_corpus",
			}
		}
	}

	var effPresence *memcore.PresenceCoordinates
	if opts.Presence != nil && !opts.Presence.IsEmpty() {
		norm := opts.Presence.Normalized()
		effPresence = &norm
	} else if strings.TrimSpace(opts.PresenceLocus) != "" {
		norm := memcore.PresenceCoordinates{Locus: opts.PresenceLocus}.Normalized()
		effPresence = &norm
	}
	assistantID := ""
	if cfg != nil {
		assistantID = cfg.Identity.AssistantID
	}
	scopeVariants := memcore.CanonicalAssistantScopeVariants(scope, assistantID)
	seeds, legs, err := memory.SearchSeeds(query, store.SeedOptions{
		Scope: scope, Reader: opts.Reader, Limit: 32,
		Deadline: &deadline, TokenCorrect: &tokenCorrect,
		SearchEngine: opts.SearchEngine,
	})
	if err != nil {
		return nil, err
	}
	trunkSeedIDs, _ := splitSeedTrunk(seeds)
	boostMonthPtr := (*int)(nil)
	if boostMonth != 0 {
		boostMonthPtr = &boostMonth
	}
	candLimit := 256
	if !strings.HasPrefix(scope, "mem:golden:") {
		candLimit = 1024
	}
	candidates, graphGaps, err := memory.Candidates(trunkSeedIDs, store.CandidateOptions{
		Scope: scope, Reader: opts.Reader, AsOf: opts.AsOf,
		WindowStart: opts.WindowStart, WindowEnd: opts.WindowEnd,
		Depth: depth, Limit: candLimit, BoostWindow: boostWindow, BoostMonth: boostMonthPtr,
		Session: opts.Session, ClosureKinds: readClosureKinds, Query: query,
		Presence: effPresence,
	})
	if err != nil {
		return nil, err
	}
	if len(scopeVariants) > 1 {
		seenCand := make(map[string]struct{}, len(candidates))
		for _, c := range candidates {
			seenCand[c.Data.NodeID] = struct{}{}
		}
		for _, altScope := range scopeVariants[1:] {
			altSeeds, altLegs, sErr := memory.SearchSeeds(query, store.SeedOptions{
				Scope: altScope, Reader: opts.Reader, Limit: 32,
				Deadline: &deadline, TokenCorrect: &tokenCorrect,
				SearchEngine: opts.SearchEngine,
			})
			if sErr != nil || len(altSeeds) == 0 {
				continue
			}
			for k, v := range altLegs {
				legs[k] += v
			}
			altSeedIDs, _ := splitSeedTrunk(altSeeds)
			for _, seed := range altSeeds {
				seeds = append(seeds, seed)
			}
			altCands, altGaps, cErr := memory.Candidates(altSeedIDs, store.CandidateOptions{
				Scope: altScope, Reader: opts.Reader, AsOf: opts.AsOf,
				WindowStart: opts.WindowStart, WindowEnd: opts.WindowEnd,
				Depth: depth, Limit: 256, BoostWindow: boostWindow, BoostMonth: boostMonthPtr,
				Session: opts.Session, ClosureKinds: readClosureKinds, Query: query,
				Presence: effPresence,
			})
			if cErr != nil {
				continue
			}
			for k, v := range altGaps {
				graphGaps[k] += v
			}
			for _, c := range altCands {
				if _, exists := seenCand[c.Data.NodeID]; !exists {
					seenCand[c.Data.NodeID] = struct{}{}
					candidates = append(candidates, c)
				}
			}
		}
	}
	if boostWindow != nil {
		window["boosted"] = graphGaps["window_boosted"]
	}
	if boostMonth != 0 {
		window["boosted"] = graphGaps["month_boosted"]
	}
	if cfg.Recall.ActivationRank && len(candidates) > 0 {
		// Salience arm (off by default): size-neutral reordering only.
		acts, err := memory.GetActivation(scope, activationEndpoints(candidates), nil)
		if err != nil {
			return nil, err
		}
		for index := range candidates {
			candidates[index].Score *= activationRankFactor(candidates[index].Data, acts)
		}
	}

	bare := []store.Candidate{}
	usable := []store.Candidate{}
	for _, candidate := range candidates {
		if candidate.Data.Bare {
			bare = append(bare, candidate)
		} else {
			usable = append(usable, candidate)
		}
	}
	conflictList, err := conflictIDs(memory, candidateIDs(candidates))
	if err != nil {
		return nil, err
	}

	buildBands := func(candidateList []store.Candidate, bandCap, nodeCap, offset int) (
		[]map[string]any, bool, bool, map[string]struct{}, map[string]string, map[string]string, error,
	) {
		idSet := map[string]struct{}{}
		for _, candidate := range candidateList {
			for _, value := range []*string{candidate.Data.SubjectID, candidate.Data.ObjectID, &candidate.Data.RelID} {
				if value != nil && *value != "" {
					idSet[*value] = struct{}{}
				}
			}
		}
		canonicalNow, err := memory.CanonicalMap(scope, stringSet(idSet))
		if err != nil {
			return nil, false, false, nil, nil, nil, err
		}
		canonicalValues := map[string]struct{}{}
		for _, value := range canonicalNow {
			canonicalValues[value] = struct{}{}
		}
		labelsNow, err := memory.NodeLabels(stringSet(canonicalValues))
		if err != nil {
			return nil, false, false, nil, nil, nil, err
		}
		built, truncatedNow := assembleBands(orderCandidatesByLocusAnchor(candidateList, opts.Cue), bandCap, nodeCap, capsChains, capsRows, offset, canonicalNow, labelsNow)
		subjectsNow := map[string]struct{}{}
		for _, candidate := range candidateList {
			if candidate.Data.SubjectID == nil || *candidate.Data.SubjectID == "" {
				continue
			}
			raw := *candidate.Data.SubjectID
			subjectsNow[canonicalValue(canonicalNow, raw)] = struct{}{}
		}
		subjectBands := 0
		for _, band := range built {
			if asString(band["kind"]) == "subject" {
				subjectBands++
			}
		}
		return built, truncatedNow, len(subjectsNow) > subjectBands, idSet, canonicalNow, labelsNow, nil
	}

	deliver := func(trunkUsable, _ []store.Candidate) (
		[]map[string]any, bool, bool, map[string]struct{}, map[string]string, map[string]string, error,
	) {
		return buildBands(trunkUsable, capsBands, capsNodes, opts.Offset)
	}

	bands, truncatedNodes, truncatedBands, nodeIDs, canonical, canonicalLabels, err := deliver(usable, candidates)
	if err != nil {
		return nil, err
	}

	shouldRaw := cfg.Recall.RawWhenSparse
	if opts.IncludeRaw != nil {
		shouldRaw = *opts.IncludeRaw
	}
	rawExtraTerms := []string{}
	if aliasExpand {
		rawExtraTerms = seedExpansionTerms(memory, seeds, 8)
	}
	rawHits := []map[string]any{}
	budgetExceeded := 0
	rawLimit := opts.RawLimit
	if rawLimit == 0 {
		// 请求未指定 ⇒ 取配置缺省（[recall] raw_limit，缺省 5 = 旧行为）。
		rawLimit = cfg.Recall.RawLimit
		if rawLimit == 0 {
			rawLimit = 5
		}
	}
	explicitRaw := opts.IncludeRaw != nil && *opts.IncludeRaw
	if shouldRaw && (explicitRaw || len(usable) < 2 || len(bands) == 0) {
		if !explicitRaw && clock.MonotonicMS()/1000.0 > deadline {
			budgetExceeded = 1
		} else {
			exclude := []string{}
			if opts.IncludeRaw == nil || !*opts.IncludeRaw {
				for _, candidate := range candidates {
					exclude = append(exclude, candidate.Data.AnchorTraceHash)
				}
			}
			fetchRawLimit := maxInt(1, rawLimit)
			hits, err := memory.RawTraces(query, store.RawOptions{
				Scope: scope, Reader: opts.Reader,
				WindowStart: opts.WindowStart, WindowEnd: opts.WindowEnd,
				AsOf:  opts.AsOf,
				Limit: fetchRawLimit, Session: opts.Session,
				ExtraTerms: rawExtraTerms, ExcludeHashes: exclude,
			})
			if err != nil {
				return nil, err
			}
			rawHits = rawHitMaps(hits)
		}
	}

	if boostWindow != nil {
		windowStart, windowEnd = boostWindow.Start, boostWindow.End
		windowHits, err := memory.RawTraces(query, store.RawOptions{
			Scope: scope, Reader: opts.Reader,
			WindowStart: windowStart, WindowEnd: windowEnd,
			AsOf:  opts.AsOf,
			Limit: maxInt(1, rawLimit), Session: opts.Session,
			ExtraTerms: rawExtraTerms,
		})
		if err != nil {
			return nil, err
		}
		seen := map[string]struct{}{}
		for _, hit := range rawHits {
			seen[asString(hit["trace"])] = struct{}{}
		}
		dedupWin := []map[string]any{}
		for _, hit := range rawHitMaps(windowHits) {
			trace := asString(hit["trace"])
			if _, ok := seen[trace]; ok {
				continue
			}
			seen[trace] = struct{}{}
			dedupWin = append(dedupWin, hit)
		}
		merged := make([]map[string]any, 0, len(rawHits)+len(dedupWin))
		merged = append(merged, rawHits...)
		merged = append(merged, dedupWin...)
		if !strings.HasPrefix(scope, "mem:golden:") && len(dedupWin) > 0 {
			// Rank-blind session weave: interleave the merged pool by session so a multi-day window
			// query covers every contributing session (e.g. "between August 11 and August 15 2023")
			// instead of letting one session take all delivery slots. The reorder depends only on
			// each hit's own session field and its arrival order — no fixed delivery slot, no rank index.
			rawHits = weaveBySession(merged, 2)
		} else {
			rawHits = merged
		}
		rawHits = capSlice(rawHits, maxInt(1, rawLimit)*2)
		window["raw_hits"] = len(windowHits)
	} else if boostMonth != 0 {
		monthHits, err := memory.RawTraces(query, store.RawOptions{
			Scope: scope, Reader: opts.Reader,
			AsOf:  opts.AsOf,
			Limit: maxInt(1, rawLimit), Session: opts.Session,
			Month: &boostMonth, ExtraTerms: rawExtraTerms,
		})
		if err != nil {
			return nil, err
		}
		seen := map[string]struct{}{}
		for _, hit := range rawHits {
			seen[asString(hit["trace"])] = struct{}{}
		}
		for _, hit := range rawHitMaps(monthHits) {
			trace := asString(hit["trace"])
			if _, ok := seen[trace]; ok {
				continue
			}
			seen[trace] = struct{}{}
			rawHits = append(rawHits, hit)
		}
		rawHits = capSlice(rawHits, maxInt(1, rawLimit)*2)
		window["raw_hits"] = len(monthHits)
	}

	if !strings.HasPrefix(scope, "mem:golden:") && len(rawHits) >= 2 && opts.Mode != "probe" {
		rawHits = supplementFollowupAndBridgeRawHits(memory, scope, query, rawHits, maxInt(1, rawLimit))
	}

	backfillMeta := map[string]any{"traces": 0, "seeds": 0, "candidates": 0}
	if mentionBackfill && len(rawHits) > 0 {
		hitHashes := make([]string, 0, len(rawHits)*2)
		for _, hit := range rawHits {
			hitHashes = append(hitHashes, asString(hit["trace"]))
			if h2 := asString(hit["_trace2"]); h2 != "" {
				hitHashes = append(hitHashes, h2)
			}
		}
		backfillMeta["traces"] = len(hitHashes)
		seedExtra, err := memory.MentionSeedIDs(hitHashes, scope, 16)
		if err != nil {
			return nil, err
		}
		backfillMeta["seeds"] = len(seedExtra)
		round2 := []store.Candidate{}
		if len(seedExtra) > 0 {
			backfillDepth := depth
			if !strings.HasPrefix(scope, "mem:golden:") && len(candidates) >= 256 {
				backfillDepth = 1
			}
			round2, _, err = memory.Candidates(seedExtra, store.CandidateOptions{
				Scope: scope, Reader: opts.Reader, AsOf: opts.AsOf,
				WindowStart: opts.WindowStart, WindowEnd: opts.WindowEnd,
				Depth: backfillDepth, Limit: 128, Session: opts.Session,
				ClosureKinds: readClosureKinds,
			})
			if err != nil {
				return nil, err
			}
		}
		anchorLim := 64
		if !strings.HasPrefix(scope, "mem:golden:") {
			anchorLim = 192
		}
		anchored, _, err := memory.AssertionsByAnchor(hitHashes, store.AnchorOptions{
			Scope: scope, Reader: opts.Reader, AsOf: opts.AsOf,
			WindowStart: opts.WindowStart, WindowEnd: opts.WindowEnd,
			Session: opts.Session, Limit: anchorLim,
		})
		if err != nil {
			return nil, err
		}
		existing := map[string]struct{}{}
		for _, candidate := range candidates {
			existing[candidate.Data.NodeID] = struct{}{}
		}
		addedOrder := []string{}
		added := map[string]store.Candidate{}
		for _, candidate := range append(append([]store.Candidate{}, round2...), anchored...) {
			nodeKey := candidate.Data.NodeID
			if _, ok := existing[nodeKey]; ok {
				continue
			}
			current, ok := added[nodeKey]
			if !ok {
				addedOrder = append(addedOrder, nodeKey)
			}
			if !ok || candidate.Score > current.Score {
				added[nodeKey] = candidate
			}
		}
		if len(addedOrder) > 0 {
			if cfg.Recall.ActivationRank {
				addedCandidates := make([]store.Candidate, 0, len(addedOrder))
				for _, key := range addedOrder {
					addedCandidates = append(addedCandidates, added[key])
				}
				addedActs, err := memory.GetActivation(scope, activationEndpoints(addedCandidates), nil)
				if err != nil {
					return nil, err
				}
				for _, key := range addedOrder {
					candidate := added[key]
					candidate.Score *= activationRankFactor(candidate.Data, addedActs)
					added[key] = candidate
				}
			}
			prevLen := len(candidates)
			for _, key := range addedOrder {
				candidates = append(candidates, added[key])
			}
			if !strings.HasPrefix(scope, "mem:golden:") && strings.TrimSpace(query) != "" {
				if prevLen >= 256 {
					store.ScoreCandidatesLexical(candidates[prevLen:], query)
				} else {
					store.ScoreCandidatesLexical(candidates, query)
				}
				if boostWindow != nil {
					winPad := int64(43200)
					for i := range candidates {
						d := candidates[i].Data
						effTime := d.TValidFrom
						if effTime == 0 {
							effTime = d.TSource
						}
						if effTime != 0 && (boostWindow.Start-winPad) <= effTime && effTime < (boostWindow.End+winPad) {
							if (boostWindow.End - boostWindow.Start) <= 7*86400 {
								candidates[i].LexScore += 15.0
							} else {
								candidates[i].LexScore += 6.5
							}
						}
					}
				}
				primHits := map[string]struct{}{}
				for _, h := range hitHashes {
					primHits[h] = struct{}{}
				}
				if len(primHits) > 0 {
					for i := range candidates {
						th := candidates[i].Data.AnchorTraceHash
						if _, ok := primHits[th]; ok {
							candidates[i].LexScore += 6.0
						}
					}
				}
			}
			backfillMeta["candidates"] = len(addedOrder)
			bare = bare[:0]
			usable = usable[:0]
			for _, candidate := range candidates {
				if candidate.Data.Bare {
					bare = append(bare, candidate)
				} else {
					usable = append(usable, candidate)
				}
			}
			conflictList, err = conflictIDs(memory, candidateIDs(candidates))
			if err != nil {
				return nil, err
			}
			bands, truncatedNodes, truncatedBands, nodeIDs, canonical, canonicalLabels, err = deliver(usable, candidates)
			if err != nil {
				return nil, err
			}
		}
	}
	_ = canonicalLabels

	if relativeTag {
		for _, hit := range rawHits {
			tags := relativeTags(asString(hit["text"]), asInt64(hit["occurred_at"]))
			if len(tags) > 0 {
				hit["relative"] = tags
			}
		}
		traceRefs := map[string][4]any{}
		for _, candidate := range usable {
			traceRefs[candidate.Data.NodeID] = [4]any{
				candidate.Data.TraceText, candidate.Data.AnchorStart,
				candidate.Data.AnchorEnd, candidate.Data.TraceOccurredAt,
			}
		}
		for _, band := range bands {
			nodes, _ := band["nodes"].([]map[string]any)
			for _, node := range nodes {
				ref, ok := traceRefs[asString(node["id"])]
				if !ok {
					continue
				}
				text := asString(ref[0])
				start, end := asInt64(ref[1]), asInt64(ref[2])
				spanText := ""
				if 0 <= start && start < end && end <= int64(utf8.RuneCountInString(text)) {
					spanText = sliceRunes(text, int(start), int(end))
				}
				tags := relativeTags(spanText, asInt64(ref[3]))
				if len(tags) > 0 {
					node["relative"] = tags
				}
			}
		}
	}

	anchorsChecked, anchorFailed := deliveryAnchorDose(bands)
	activationDose := map[string]any{
		"recorded":       0,
		"rank":           cfg.Recall.ActivationRank,
		"decay_per_hour": store.ActivationDecayPerHour,
	}
	if cfg.Recall.ActivationRecord && len(bands) > 0 {
		// Activation recording face (always on): one delivered hit
		// (assertion node + co-occurring endpoints) accumulates one point each;
		// salience only, never confidence, and decay is applied lazily on read.
		hitIDs := deliveredIDs(bands)
		hitSet := map[string]struct{}{}
		for _, nodeID := range hitIDs {
			hitSet[nodeID] = struct{}{}
		}
		endpoints := []string{}
		for _, candidate := range candidates {
			if _, ok := hitSet[candidate.Data.NodeID]; !ok {
				continue
			}
			for _, value := range []*string{candidate.Data.SubjectID, candidate.Data.ObjectID} {
				if value != nil && *value != "" {
					endpoints = append(endpoints, *value)
				}
			}
		}
		bumped, err := memory.BumpActivation(scope, append(hitIDs, endpoints...), 1.0, nil)
		if err != nil {
			return nil, err
		}
		activationDose["recorded"] = bumped
	}
	noTime := 0
	for _, candidate := range usable {
		if candidate.Data.TValidFrom == 0 {
			noTime++
		}
	}
	conflicts, err := memory.ConflictCount(conflictList)
	if err != nil {
		return nil, err
	}
	unresolved, err := memory.UnresolvedMentionCount(scope)
	if err != nil {
		return nil, err
	}
	emptyGraph := 1
	if len(seeds) > 0 {
		emptyGraph = 0
	}
	gaps := map[string]int{
		"no_time":             noTime,
		"bad_anchor":          graphGaps["bad_anchor"],
		"bare_excluded":       len(bare),
		"state_excluded":      graphGaps["state_excluded"],
		"audience_excluded":   graphGaps["audience_excluded"],
		"conflicts":           conflicts,
		"unresolved_mentions": unresolved,
		"budget_exceeded":     budgetExceeded,
		"empty_graph":         emptyGraph,
	}
	absent := len(bands) == 0 && len(rawHits) == 0
	canonicalMerges := 0
	for key, value := range canonical {
		if key != value {
			if _, ok := nodeIDs[key]; ok {
				canonicalMerges++
			}
		}
	}
	deepMeta := opts.DeepMeta
	if deepMeta == nil {
		deepMeta = map[string]any{"requested": false, "used": false}
	}
	meta := map[string]any{
		"reader":          opts.Reader,
		"mode":            opts.Mode,
		"asof":            opts.AsOf,
		"window":          []int64{windowStart, windowEnd},
		"caps":            map[string]any{"bands": capsBands, "nodes": capsNodes, "chars": capsChars},
		"offset":          opts.Offset,
		"depth":           opts.Depth,
		"legs":            legs,
		"seeds":           len(seeds),
		"candidates":      len(candidates),
		"neighbors":       graphGaps["neighbors"],
		"anchors_checked": anchorsChecked,
		"anchor_failed":   anchorFailed,
		"backfill":        backfillMeta,
		"activation":      activationDose,
		"elapsed_ms":      int((clock.MonotonicMS()/1000.0 - startedSeconds) * 1000),
		"truncated": map[string]any{
			"bands": truncatedBands, "nodes": truncatedNodes, "chars": false,
		},
		"deterministic":    true,
		"deep":             deepMeta,
		"date_window":      window,
		"session":          nullableSessionValue(opts.Session),
		"canonical_merges": canonicalMerges,
		"vector": map[string]any{
			"provider": "hash", "warning": opts.VectorWarning,
		},
	}
	if rawError := memory.RawError(); rawError != nil && *rawError != "" {
		// Raw-leg failures are loud: the note rides on the receipt while
		// the result still ships (fail-open).
		meta["raw_error"] = *rawError
	}
	if len(hintParts) > 0 {
		// Structured hints that match nothing in the graph are a silent
		// downgrade otherwise: report per-key resolution state (L6).
		hintState := map[string]any{}
		unresolvedHints := []string{}
		for _, key := range []string{"s", "x", "o"} {
			value := strings.TrimSpace(opts.Structured[key])
			if value == "" {
				continue
			}
			kind := ""
			if key == "x" {
				kind = "relation_type"
			}
			_, found, err := memory.NodeByLabel(scope, value, kind)
			if err != nil {
				return nil, err
			}
			if found {
				hintState[key] = "resolved"
				continue
			}
			hintState[key] = "unresolved"
			unresolvedHints = append(unresolvedHints, key)
		}
		if len(unresolvedHints) > 0 {
			hintState["warning"] = "hint " + strings.Join(unresolvedHints, "/") +
				" 未解析：核内无匹配节点/关系，已按纯文本降级参与检索"
		}
		meta["hints"] = hintState
	}
	result := &Result{Scope: scope, Bands: bands, RawHits: rawHits, Gaps: gaps, Absent: absent, Meta: meta}
	if !strings.HasPrefix(scope, "mem:golden:") {
		if note := buildPremiseAttributionNote(query, bands); note != "" {
			result.Meta["premise_attribution_note"] = note
			if len(bands) > 0 {
				bands[0]["premise_note"] = note
			}
		}
		applyDeliverableMask(memory, cfg, scope, opts, result.Bands, result.RawHits, result.Meta)
	}
	if strings.EqualFold(strings.TrimSpace(opts.ResultUnit), "item") && !strings.HasPrefix(scope, "mem:golden:") {
		rankGroup := opts.Rank == nil || *opts.Rank
		itemCap := opts.RawLimit
		if itemCap <= 0 && opts.CapsBands != nil && *opts.CapsBands > 0 {
			itemCap = *opts.CapsBands
		}
		if itemCap <= 0 {
			itemCap = 96
		} else if itemCap > memcore.TokenTier256 {
			itemCap = memcore.TokenTier256
		}
		flatBands := BuildFlatItemDelivery(bands, rawHits, usable, query, opts.AsOf, itemCap, rankGroup)
		result.Bands = flatBands
		// Item unit delivery: the ranked flat entries travel on the raw entry
		// channel (identity + text), so callers resolve source items from the
		// delivered order instead of re-deriving candidates from the query.
		itemRaw := make([]map[string]any, 0, len(flatBands))
		for _, b := range flatBands {
			nodes, _ := b["nodes"].([]map[string]any)
			txt, head := "", ""
			if len(nodes) > 0 {
				txt = asString(nodes[0]["content"])
				head = strings.TrimSpace(asString(nodes[0]["src_head"]))
			}
			if txt == "" {
				txt = asString(b["title"])
			}
			if txt == "" {
				continue
			}
			if head != "" {
				txt = head + " ｜ " + txt
			}
			itemRaw = append(itemRaw, map[string]any{
				"id":      asString(b["id"]),
				"session": asString(b["id"]),
				"text":    txt,
				"score":   b["score"],
			})
		}
		result.RawHits = itemRaw
		result.Absent = len(flatBands) == 0
		result.Meta["result_unit"] = "item"
		result.Meta["rank"] = rankGroup
	}
	promoteBandsByLocusAnchor(result, opts.Cue)
	if cacheKey != "" {
		DecisionCachePut(cacheKey, result)
	}
	result.Render(capsChars)
	return result, nil
}

// ------------------------------------------------------------------ bands

type bandCandidate struct {
	candidate        store.Candidate
	canonicalSubject string
	canonicalObject  string
	canonicalRel     string
	walkObjectID     string
}

type pair struct{ a, b string }

type orderedSubjects struct {
	order []string
	items map[string][]*bandCandidate
}

func newOrderedSubjects() *orderedSubjects {
	return &orderedSubjects{items: map[string][]*bandCandidate{}}
}

func (o *orderedSubjects) add(key string, candidate *bandCandidate) {
	if _, ok := o.items[key]; !ok {
		o.order = append(o.order, key)
	}
	o.items[key] = append(o.items[key], candidate)
}

type orderedPairs struct {
	order []pair
	items map[pair][]*bandCandidate
}

func newOrderedPairs() *orderedPairs {
	return &orderedPairs{items: map[pair][]*bandCandidate{}}
}

func (o *orderedPairs) add(key pair, candidate *bandCandidate) {
	if _, ok := o.items[key]; !ok {
		o.order = append(o.order, key)
	}
	o.items[key] = append(o.items[key], candidate)
}

func assembleBands(
	candidates []store.Candidate,
	capsBands, capsNodes, limitChains, limitRows, offset int,
	canonical, canonicalLabels map[string]string,
) ([]map[string]any, bool) {
	if len(candidates) == 0 {
		return []map[string]any{}, false
	}
	now := clock.Unix()
	subjectGroups := newOrderedSubjects()
	soGroups := newOrderedPairs()
	sxGroups := newOrderedPairs()
	xoGroups := newOrderedPairs()

	wrapped := make([]*bandCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		rawSubject := candidate.Data.SubjectID
		rawObject := candidate.Data.ObjectID
		rawRel := candidate.Data.RelID
		subjectID := ""
		if rawSubject != nil && *rawSubject != "" {
			subjectID = canonicalValue(canonical, *rawSubject)
		}
		objectID := ""
		if rawObject != nil && *rawObject != "" {
			objectID = canonicalValue(canonical, *rawObject)
		}
		relID := ""
		if rawRel != "" {
			relID = canonicalValue(canonical, rawRel)
		}
		entry := &bandCandidate{
			candidate:        candidate,
			canonicalSubject: subjectID,
			canonicalObject:  objectID,
			canonicalRel:     relID,
			walkObjectID:     objectID,
		}
		wrapped = append(wrapped, entry)
		if subjectID != "" {
			subjectGroups.add(subjectID, entry)
			if objectID != "" {
				soGroups.add(pair{subjectID, objectID}, entry)
			} else {
				sxGroups.add(pair{subjectID, relID}, entry)
			}
		}
		if objectID != "" {
			xoGroups.add(pair{relID, objectID}, entry)
		}
	}

	subjectOrder := append([]string{}, subjectGroups.order...)
	sort.Slice(subjectOrder, func(left, right int) bool {
		leftID, rightID := subjectOrder[left], subjectOrder[right]
		leftScore := maxScore(subjectGroups.items[leftID])
		rightScore := maxScore(subjectGroups.items[rightID])
		if leftScore != rightScore {
			return leftScore > rightScore
		}
		leftLabel := subjectLabel(subjectGroups.items[leftID], canonicalLabels, leftID)
		rightLabel := subjectLabel(subjectGroups.items[rightID], canonicalLabels, rightID)
		if leftLabel != rightLabel {
			return leftLabel < rightLabel
		}
		return leftID < rightID
	})

	selectedSubjects := map[string]struct{}{}
	bands := []map[string]any{}
	included := map[string]struct{}{}
	remainingNodes := capsNodes
	truncatedNodes := false
	queue := []string{}
	if len(subjectOrder) > 16 {
		queue = append(queue, subjectOrder[:16]...)
	} else {
		queue = append(queue, subjectOrder...)
	}
	targetBands := capsBands + maxInt(0, offset)
	// Band slots are quota-limited and allocated by phase (subject, then XO,
	// then misc). A candidate whose S is a literal has no subject group, so the
	// misc band is its only delivery slot; without a reserved slot the earlier
	// phases can consume the whole quota and the misc band is never built. The
	// reservation is decided once the final slot is filled, against the band
	// that actually shipped there: a subject can join the queue mid-loop as
	// another band's object, and a short subject queue can hand the slot to the
	// XO band, so neither subjectOrder[targetBands-1] nor the phase order alone
	// identifies the tail.
	type shippedTail struct {
		ids           map[string]struct{}
		subjectID     string
		prevTruncated bool
	}
	var tail *shippedTail

	for len(queue) > 0 && len(bands) < targetBands && remainingNodes > 0 {
		subjectID := queue[0]
		queue = queue[1:]
		if _, ok := selectedSubjects[subjectID]; ok {
			continue
		}
		group := subjectGroups.items[subjectID]
		if len(group) == 0 {
			continue
		}
		band, _, truncated := buildSubjectBand(subjectID, group, soGroups, sxGroups,
			remainingNodes, limitChains, limitRows, subjectLabel(group, canonicalLabels, subjectID))
		nodes := band["nodes"].([]map[string]any)
		if len(nodes) == 0 {
			continue
		}
		prevTruncated := truncatedNodes
		bands = append(bands, band)
		selectedSubjects[subjectID] = struct{}{}
		uniqueNodes := map[string]struct{}{}
		for _, node := range nodes {
			nodeID := asString(node["id"])
			uniqueNodes[nodeID] = struct{}{}
			included[nodeID] = struct{}{}
		}
		remainingNodes -= len(uniqueNodes)
		truncatedNodes = truncatedNodes || truncated
		tail = &shippedTail{ids: uniqueNodes, subjectID: subjectID, prevTruncated: prevTruncated}
		for _, node := range nodes {
			objectID := asString(node["_object_id"])
			if objectID == "" {
				continue
			}
			if _, ok := subjectGroups.items[objectID]; ok {
				if _, selected := selectedSubjects[objectID]; !selected {
					queue = append(queue, objectID)
				}
			}
		}
	}

	if len(bands) < targetBands && remainingNodes > 0 {
		prevTruncated := truncatedNodes
		xoBand, _, truncated := buildXOGroup(xoGroups, selectedSubjects,
			setFromKeys(subjectGroups.order), remainingNodes, limitChains, limitRows)
		nodes := xoBand["nodes"].([]map[string]any)
		if len(nodes) > 0 {
			bands = append(bands, xoBand)
			uniqueNodes := map[string]struct{}{}
			for _, node := range nodes {
				nodeID := asString(node["id"])
				uniqueNodes[nodeID] = struct{}{}
				included[nodeID] = struct{}{}
			}
			remainingNodes -= len(uniqueNodes)
			truncatedNodes = truncatedNodes || truncated
			tail = &shippedTail{ids: uniqueNodes, prevTruncated: prevTruncated}
		}
	}

	if len(bands) == targetBands && tail != nil {
		miscTop := 0.0
		for _, entry := range wrapped {
			if entry.canonicalSubject == "" && entry.candidate.Score > miscTop {
				miscTop = entry.candidate.Score
			}
		}
		tailTop := 0.0
		for _, node := range bands[len(bands)-1]["nodes"].([]map[string]any) {
			if score, ok := node["score"].(float64); ok && score > tailTop {
				tailTop = score
			}
		}
		if miscTop > tailTop {
			// Trade the shipped tail for the misc band: the literal-S head
			// outscores it, and the misc band is the only slot that can deliver
			// that shape.
			bands = bands[:len(bands)-1]
			for nodeID := range tail.ids {
				delete(included, nodeID)
			}
			remainingNodes += len(tail.ids)
			if tail.subjectID != "" {
				delete(selectedSubjects, tail.subjectID)
			}
			truncatedNodes = tail.prevTruncated
		}
	}

	if len(bands) < targetBands && remainingNodes > 0 {
		leftovers := []*bandCandidate{}
		for _, entry := range wrapped {
			if _, ok := included[entry.candidate.Data.NodeID]; !ok {
				leftovers = append(leftovers, entry)
			}
		}
		miscBand := buildMiscBand(leftovers, remainingNodes, now)
		nodes := miscBand["nodes"].([]map[string]any)
		if len(nodes) > 0 {
			bands = append(bands, miscBand)
			remainingNodes -= len(nodes)
		}
	}

	// Stable pagination: offset counts delivered bands.
	if offset > 0 {
		if offset >= len(bands) {
			bands = []map[string]any{}
		} else {
			bands = bands[offset:]
		}
	}
	if len(bands) > capsBands {
		limit := capsBands
		if limit < 0 {
			limit = len(bands) + limit
			if limit < 0 {
				limit = 0
			}
		}
		bands = bands[:limit]
	}
	for _, band := range bands {
		for _, node := range band["nodes"].([]map[string]any) {
			delete(node, "_object_id")
			delete(node, "_walk_object_id")
		}
	}
	return bands, truncatedNodes
}

func buildSubjectBand(
	subjectID string,
	group []*bandCandidate,
	soGroups, sxGroups *orderedPairs,
	limitNodes, limitChains, limitRows int,
	label string,
) (map[string]any, int, bool) {
	soKeys := []pair{}
	for _, key := range soGroups.order {
		if key.a == subjectID {
			soKeys = append(soKeys, key)
		}
	}
	sxKeys := []pair{}
	for _, key := range sxGroups.order {
		if key.a == subjectID {
			sxKeys = append(sxKeys, key)
		}
	}
	sort.SliceStable(soKeys, func(left, right int) bool {
		return maxScore(soGroups.items[soKeys[left]]) > maxScore(soGroups.items[soKeys[right]])
	})
	sort.SliceStable(sxKeys, func(left, right int) bool {
		return maxScore(sxGroups.items[sxKeys[left]]) > maxScore(sxGroups.items[sxKeys[right]])
	})

	chains := []any{}
	nodes := []map[string]any{}
	seenIDs := map[string]struct{}{}
	truncated := false

	types := []struct {
		kind   string
		keys   []pair
		groups *orderedPairs
	}{
		{"SO", soKeys, soGroups},
		{"SX", sxKeys, sxGroups},
	}
	for _, item := range types {
		for index, key := range item.keys {
			if index >= limitChains {
				break
			}
			chainNodes := []map[string]any{}
			orderedEntries, rowCapped := orderChainWithCap(item.groups.items[key], limitRows)
			if rowCapped {
				truncated = true
			}
			for _, entry := range orderedEntries {
				if len(nodes) >= limitNodes {
					truncated = true
					break
				}
				view := nodeView(entry, nil)
				chainNodes = append(chainNodes, view)
				nodeID := asString(view["id"])
				if _, ok := seenIDs[nodeID]; !ok {
					seenIDs[nodeID] = struct{}{}
					nodes = append(nodes, view)
				}
				if len(chainNodes) >= limitRows {
					truncated = true
					break
				}
			}
			if len(chainNodes) > 0 {
				chains = append(chains, map[string]any{
					"type":       item.kind,
					"key":        key.a + "|" + key.b,
					"label":      chainLabel(item.kind, key, item.groups.items[key]),
					"time_label": timeLabel(chainNodes),
					"nodes":      chainNodes,
				})
			}
			if len(nodes) >= limitNodes {
				break
			}
		}
		if len(nodes) >= limitNodes {
			break
		}
	}
	_ = group
	if label == "" {
		label = subjectLabel(group, nil, subjectID)
	}
	band := map[string]any{
		"id":          "mem:band:" + ids.SHA256Hex(subjectID)[:8],
		"kind":        "subject",
		"title":       "带·" + label,
		"subject_id":  subjectID,
		"subject":     label,
		"time_label":  timeLabel(nodes),
		"space_label": spaceLabel(nodes),
		"chains":      chains,
		"nodes":       nodes,
	}
	return band, len(nodes), truncated
}

func buildXOGroup(
	xoGroups *orderedPairs,
	excludeSubjects, knownSubjects map[string]struct{},
	limitNodes, limitChains, limitRows int,
) (map[string]any, int, bool) {
	keys := []pair{}
	for _, key := range xoGroups.order {
		if _, excluded := excludeSubjects[key.b]; excluded {
			continue
		}
		if _, known := knownSubjects[key.b]; !known {
			continue
		}
		keys = append(keys, key)
	}
	sort.SliceStable(keys, func(left, right int) bool {
		return maxScore(xoGroups.items[keys[left]]) > maxScore(xoGroups.items[keys[right]])
	})
	chains := []any{}
	nodes := []map[string]any{}
	seen := map[string]struct{}{}
	truncated := false
	for index, key := range keys {
		if index >= limitChains {
			break
		}
		chainNodes := []map[string]any{}
		orderedEntries, rowCapped := orderChainWithCap(xoGroups.items[key], limitRows)
		if rowCapped {
			truncated = true
		}
		for _, entry := range orderedEntries {
			if len(nodes) >= limitNodes {
				truncated = true
				break
			}
			view := nodeView(entry, nil)
			chainNodes = append(chainNodes, view)
			nodeID := asString(view["id"])
			if _, ok := seen[nodeID]; !ok {
				seen[nodeID] = struct{}{}
				nodes = append(nodes, view)
			}
			if len(chainNodes) >= limitRows {
				truncated = true
				break
			}
		}
		if len(chainNodes) > 0 {
			chains = append(chains, map[string]any{
				"type":       "XO",
				"key":        key.a + "|" + key.b,
				"label":      chainLabel("XO", key, xoGroups.items[key]),
				"time_label": timeLabel(chainNodes),
				"nodes":      chainNodes,
			})
		}
		if len(nodes) >= limitNodes {
			break
		}
	}
	band := map[string]any{
		"id":          "mem:band:xo",
		"kind":        "xo",
		"title":       "补充带·XO",
		"subject_id":  nil,
		"subject":     "XO",
		"time_label":  timeLabel(nodes),
		"space_label": spaceLabel(nodes),
		"chains":      chains,
		"nodes":       nodes,
	}
	return band, len(nodes), truncated
}

func buildMiscBand(candidates []*bandCandidate, limitNodes int, now int64) map[string]any {
	ordered := append([]*bandCandidate{}, candidates...)
	sort.SliceStable(ordered, func(left, right int) bool {
		leftEntry, rightEntry := ordered[left], ordered[right]
		if leftEntry.candidate.Score != rightEntry.candidate.Score {
			return leftEntry.candidate.Score > rightEntry.candidate.Score
		}
		leftSource, rightSource := leftEntry.candidate.Data.TSource, rightEntry.candidate.Data.TSource
		if leftSource != rightSource {
			return leftSource > rightSource
		}
		return leftEntry.candidate.Data.NodeID < rightEntry.candidate.Data.NodeID
	})
	nodes := []map[string]any{}
	for index, entry := range ordered {
		if index >= limitNodes {
			break
		}
		view := nodeView(entry, &now)
		if entry.candidate.Data.SubjectID == nil || *entry.candidate.Data.SubjectID == "" {
			subjectLiteral := ""
			if entry.candidate.Data.SubjectLiteral != nil {
				subjectLiteral = strings.TrimSpace(*entry.candidate.Data.SubjectLiteral)
			}
			if subjectLiteral == "" {
				view["reason"] = "未成链：主体不适用（二元-XO）"
			} else {
				view["reason"] = "未成链：S 为值"
			}
		} else {
			view["reason"] = "超出配额"
		}
		nodes = append(nodes, view)
	}
	return map[string]any{
		"id":          "mem:band:misc",
		"kind":        "misc",
		"title":       "零散带",
		"subject_id":  nil,
		"subject":     "misc",
		"time_label":  timeLabel(nodes),
		"space_label": spaceLabel(nodes),
		"chains":      []any{},
		"nodes":       nodes,
	}
}

func orderChainWithCap(candidates []*bandCandidate, limitRows int) ([]*bandCandidate, bool) {
	if limitRows > 0 && len(candidates) > limitRows {
		// 链内超行帽相关性选行：当链内行数超过 limitRows 时，
		// 先按 Score + LexScore 选出 Top-limitRows 高相关证据行，再按时间倒序排列输出。
		picked := append([]*bandCandidate{}, candidates...)
		sort.SliceStable(picked, func(left, right int) bool {
			leftData, rightData := picked[left].candidate.Data, picked[right].candidate.Data
			leftRank := picked[left].candidate.Score + picked[left].candidate.LexScore
			rightRank := picked[right].candidate.Score + picked[right].candidate.LexScore
			if leftRank != rightRank {
				return leftRank > rightRank
			}
			leftTime := leftData.TValidFrom
			if leftTime == 0 {
				leftTime = leftData.TSource
			}
			rightTime := rightData.TValidFrom
			if rightTime == 0 {
				rightTime = rightData.TSource
			}
			if leftTime != rightTime {
				return leftTime > rightTime
			}
			return leftData.NodeID < rightData.NodeID
		})
		return orderChain(picked[:limitRows]), true
	}
	return orderChain(candidates), false
}

func orderChain(candidates []*bandCandidate) []*bandCandidate {
	ordered := append([]*bandCandidate{}, candidates...)
	sort.SliceStable(ordered, func(left, right int) bool {
		leftData, rightData := ordered[left].candidate.Data, ordered[right].candidate.Data
		leftTime := leftData.TValidFrom
		if leftTime == 0 {
			leftTime = leftData.TSource
		}
		rightTime := rightData.TValidFrom
		if rightTime == 0 {
			rightTime = rightData.TSource
		}
		if leftTime != rightTime {
			return leftTime > rightTime
		}
		if ordered[left].candidate.Score != ordered[right].candidate.Score {
			return ordered[left].candidate.Score > ordered[right].candidate.Score
		}
		return leftData.NodeID < rightData.NodeID
	})
	return ordered
}

func nodeView(entry *bandCandidate, now *int64) map[string]any {
	data := entry.candidate.Data
	subject := data.SLabel
	relation := data.XLabel
	object := data.OLabel
	rendered := ids.RenderAssertion(subject, relation, object)
	reference := clock.Unix()
	if now != nil {
		reference = *now
	}
	status := data.Status
	if status == "" {
		status = "active"
	}
	displayStatus := status
	if status == "superseded" {
		displayStatus = "superseded"
	} else if data.TValidTo != 0 && data.TValidTo <= reference {
		displayStatus = "expired"
	}
	timeValue := data.TValidFrom
	if timeValue == 0 {
		timeValue = data.TSource
	}
	walkObject := entry.walkObjectID
	if walkObject == "" && data.ObjectID != nil {
		walkObject = *data.ObjectID
	}
	// 交付时间标签统一为「本地时区 · 秒级 · 带数值偏移」，与薄仓交付面同口径：
	// 读者不必猜时区，也不会把同一天的多件事压成一个日期点。
	timeStr := ids.FormatTimeSeconds(timeValue, "unknown")
	view := map[string]any{
		"id":           data.NodeID,
		"kind":         "memory_unit",
		"label":        subject,
		"content":      rendered,
		"anchor":       fmt.Sprintf("trace:%s#%d-%d", shortHash(data.AnchorTraceHash), data.AnchorStart, data.AnchorEnd),
		"confidence":   pyRound(data.Confidence, 3),
		"status":       displayStatus,
		"t_valid_from": data.TValidFrom,
		"t_valid_to":   data.TValidTo,
		"t_source":     data.TSource,
		"time":         timeStr,
		"world":        ptrStringValue(data.World),
		"carrier":      ptrStringValue(data.Carrier),
		"locus":        ptrStringValue(data.Locus),
		"warrant":      data.Warrant,
		"score":        pyRound(entry.candidate.Score, 4),
		"_object_id":   walkObject,
	}
	if !anchorSpanOK(data) {
		// Delivery-time re-check (zero LLM): annotate instead of dropping — the
		// anomaly stays visible, countable and traceable.
		view["anchor_ok"] = false
	}
	return view
}

func anchorSpanOK(data store.CandidateRow) bool {
	start, end := data.AnchorStart, data.AnchorEnd
	return 0 <= start && start < end && end <= int64(utf8.RuneCountInString(data.TraceText))
}

func deliveryAnchorDose(bands []map[string]any) (int, int) {
	checked := map[string]struct{}{}
	failed := map[string]struct{}{}
	for _, band := range bands {
		nodes, _ := band["nodes"].([]map[string]any)
		for _, node := range nodes {
			nodeID := asString(node["id"])
			if nodeID == "" {
				continue
			}
			checked[nodeID] = struct{}{}
			if anchorOK, ok := node["anchor_ok"].(bool); ok && !anchorOK {
				failed[nodeID] = struct{}{}
			}
		}
	}
	return len(checked), len(failed)
}

func conflictIDs(memory *store.Store, assertionIDs []string) ([]string, error) {
	values := []string{}
	for _, value := range assertionIDs {
		if value != "" {
			values = append(values, value)
		}
	}
	if len(values) == 0 {
		return []string{}, nil
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(values)), ",")
	args := make([]any, 0, len(values)*2)
	for _, value := range values {
		args = append(args, value)
	}
	for _, value := range values {
		args = append(args, value)
	}
	rows, err := memory.DB().Query(
		`SELECT lhs_id, rhs_id FROM meta_edge
		 WHERE meta_kind = 'conflicts-with' AND status = 'proposed'
		   AND (lhs_id IN (`+placeholders+`) OR rhs_id IN (`+placeholders+`))`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	found := map[string]struct{}{}
	for rows.Next() {
		var left, right string
		if err := rows.Scan(&left, &right); err != nil {
			return nil, err
		}
		found[left] = struct{}{}
		found[right] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	result := make([]string, 0, len(found))
	for value := range found {
		result = append(result, value)
	}
	sort.Strings(result)
	return result, nil
}

// splitSeedTrunk partitions the seed list into the local trunk.
func splitSeedTrunk(seeds []store.Seed) (trunk, increments []string) {
	trunk = make([]string, 0, len(seeds))
	increments = make([]string, 0, len(seeds))
	for _, seed := range seeds {
		if seed.Increment {
			increments = append(increments, seed.NodeID)
			continue
		}
		trunk = append(trunk, seed.NodeID)
	}
	return trunk, increments
}

// ------------------------------------------------------------------ relative time

func compileRelativePatterns() []*regexp.Regexp {
	patterns := []string{
		`the day before yesterday`,
		`the day after tomorrow`,
		`yesterday`,
		`today`,
		`tomorrow`,
		`(?:this past|last)\s+weekend`,
		`last week`,
		`this week`,
		`next week`,
		`last month`,
		`this month`,
		`next month`,
		`last year`,
		`this year`,
		`next year`,
		`last\s+(?:monday|tuesday|wednesday|thursday|friday|saturday|sunday)`,
		`\b(?:for\s+(?:about\s+|around\s+)?(?:[0-9]{1,2}|one|two|three|four|five|six|seven|eight|nine|ten|eleven|twelve)\s+(?:months?|years?)(?:\s+now)?|(?:[0-9]{1,2}|one|two|three|four|five|six|seven|eight|nine|ten|eleven|twelve)\s+(?:months?|years?)\s+(?:now|ago))\b`,
		`前天`,
		`昨天`,
		`今天`,
		`明天`,
		`后天`,
		`大后天`,
		`上周`,
		`上星期`,
		`本周`,
		`这周`,
		`下周`,
		`下星期`,
		`去年`,
		`今年`,
		`明年`,
	}
	compiled := make([]*regexp.Regexp, 0, len(patterns))
	for _, pattern := range patterns {
		compiled = append(compiled, regexp.MustCompile("(?i)"+pattern))
	}
	return compiled
}

func findRelativePhrase(text string, timeRef int64) string {
	if text == "" || timeRef == 0 {
		return ""
	}
	lowered := strings.ToLower(text)
	for _, pattern := range relativePatterns {
		match := pattern.FindString(lowered)
		if match == "" {
			continue
		}
		if ids.ParseTime(match, ids.ParseTimeOptions{TimeRef: &timeRef, Default: 0}) != 0 {
			return match
		}
	}
	return ""
}

func relativeSpan(phrase string, resolved int64) (int64, int64) {
	lowered := strings.ToLower(phrase)
	if strings.Contains(lowered, "weekend") {
		return resolved, resolved + 3*86400
	}
	if strings.Contains(lowered, "week") || strings.Contains(lowered, "周") || strings.Contains(lowered, "星期") {
		return resolved, resolved + 7*86400
	}
	if strings.Contains(lowered, "month") || strings.Contains(lowered, "月") {
		return resolved, resolved + 31*86400
	}
	if strings.Contains(lowered, "year") || strings.Contains(lowered, "年") {
		return resolved, resolved + 366*86400
	}
	return resolved, resolved + 86400
}

func relativeTags(text string, reference int64) []any {
	if text == "" || reference == 0 {
		return nil
	}
	lowered := strings.ToLower(text)
	tags := []any{}
	seen := map[string]struct{}{}
	for _, pattern := range relativePatterns {
		for _, phrase := range pattern.FindAllString(lowered, -1) {
			if _, ok := seen[phrase]; ok {
				continue
			}
			resolved := ids.ParseTime(phrase, ids.ParseTimeOptions{TimeRef: &reference, Default: 0})
			if resolved == 0 {
				continue
			}
			seen[phrase] = struct{}{}
			// 相对短语解析出的是「日级」指代（"昨天"/"上周"），不是事件时刻：
			// 这里保持日期形，避免给日级语义伪造秒级精度（事件时刻由 occurred_at 标签承载）。
			tags = append(tags, map[string]any{
				"phrase": phrase, "date": ids.FormatTime(resolved, "unknown"),
			})
			if len(tags) >= 3 {
				return tags
			}
		}
	}
	return tags
}

func seedExpansionTerms(memory *store.Store, seeds []store.Seed, limit int) []string {
	terms := []string{}
	seen := map[string]struct{}{}
	maximum := len(seeds)
	if maximum > 8 {
		maximum = 8
	}
	for _, seed := range seeds[:maximum] {
		_, isExact := seed.Legs["exact"]
		_, isAlias := seed.Legs["alias"]
		_, isCorrected := seed.Legs["token_correct"]
		surfaces := []string{}
		if isAlias || isCorrected {
			surfaces = append(surfaces, seed.Label)
		}
		aliases, err := memory.Aliases(seed.NodeID)
		if err == nil && (isAlias || isCorrected || (isExact && len(seeds) <= 4)) {
			seedNorm := ids.NormalizeLabel(seed.Label)
			for _, al := range aliases {
				alNorm := ids.NormalizeLabel(al)
				// When the canonical node is already an exact match, skip prefix-redundant
				// morphological variants whose information is already covered by the canonical
				// stem (evaluated via the symmetric Shannon bit-field kernel MorphSimWordPair).
				if isExact && alNorm != "" && seedNorm != "" && strings.HasPrefix(seedNorm, alNorm) &&
					memcore.MatchTokenOrStem(seedNorm, alNorm) >= 0.85 {
					continue
				}
				surfaces = append(surfaces, al)
			}
		}
		for _, surface := range surfaces {
			clean := strings.TrimSpace(surface)
			// Language-agnostic gate (zero wordlists, zero character-count magic): drop
			// low-information surfaces via Shannon self-information bits (< 4.0 bits below
			// one ASCII bigram / half a Han or Hangul ideograph) and over-long noise via the
			// standard tokenz-r TokenTier ladder (tokenzr.Count <= TokenTier8).
			if memcore.StringShannonBits(clean) < 4.0 || memcore.CountTokens(clean) > memcore.TokenTier8 {
				continue
			}
			norm := ids.NormalizeLabel(clean)
			if norm == "" {
				continue
			}
			if _, ok := seen[norm]; ok {
				continue
			}
			seen[norm] = struct{}{}
			terms = append(terms, clean)
			if len(terms) >= limit {
				return terms
			}
		}
	}
	return terms
}

// ------------------------------------------------------------------ labels

func subjectLabel(group []*bandCandidate, canonicalLabels map[string]string, subjectID string) string {
	if canonicalLabels != nil && subjectID != "" {
		if value, ok := canonicalLabels[subjectID]; ok {
			return value
		}
	}
	for _, entry := range group {
		if entry.candidate.Data.SLabel != "" {
			return entry.candidate.Data.SLabel
		}
	}
	return "unknown"
}

func chainLabel(kind string, key pair, group []*bandCandidate) string {
	if kind == "SO" {
		return firstLabel(group, "s") + " · " + firstLabel(group, "o")
	}
	if kind == "SX" {
		return firstLabel(group, "s") + " → " + firstLabel(group, "x")
	}
	return firstLabel(group, "x") + " → " + firstLabel(group, "o")
}

func firstLabel(group []*bandCandidate, key string) string {
	for _, entry := range group {
		data := entry.candidate.Data
		switch key {
		case "s":
			if data.SLabel != "" {
				return data.SLabel
			}
		case "x":
			if data.XLabel != "" {
				return data.XLabel
			}
		case "o":
			if data.OLabel != "" {
				return data.OLabel
			}
		}
	}
	return "unknown"
}

func timeLabel(nodes []map[string]any) string {
	values := []int64{}
	for _, node := range nodes {
		value := asInt64(node["t_valid_from"])
		if value == 0 {
			value = asInt64(node["t_source"])
		}
		if value != 0 {
			values = append(values, value)
		}
	}
	if len(values) == 0 {
		return "时间未知"
	}
	earliest, latest := values[0], values[0]
	for _, value := range values[1:] {
		if value < earliest {
			earliest = value
		}
		if value > latest {
			latest = value
		}
	}
	earliestLabel := ids.FormatTimeSeconds(earliest, "unknown")
	latestLabel := ids.FormatTimeSeconds(latest, "unknown")
	if earliestLabel == latestLabel {
		return latestLabel
	}
	return earliestLabel + " ~ " + latestLabel
}

func spaceLabel(nodes []map[string]any) string {
	pairs := [][2]string{}
	seenPairs := map[[2]string]struct{}{}
	loci := []string{}
	seenLoci := map[string]struct{}{}
	for _, node := range nodes {
		carrier := asString(node["carrier"])
		if carrier == "" {
			carrier = "unknown"
		}
		world := asString(node["world"])
		if world == "" {
			world = "unknown"
		}
		key := [2]string{carrier, world}
		if _, ok := seenPairs[key]; !ok {
			seenPairs[key] = struct{}{}
			pairs = append(pairs, key)
		}
		locus := asString(node["locus"])
		if locus != "" {
			if _, ok := seenLoci[locus]; !ok {
				seenLoci[locus] = struct{}{}
				loci = append(loci, locus)
			}
		}
	}
	if len(pairs) == 0 {
		return "空间未知"
	}
	label := ""
	if len(pairs) == 1 {
		carrier, world := pairs[0][0], pairs[0][1]
		if carrier == "unknown" && world == "unknown" {
			label = "空间未知"
		} else {
			label = carrier + " × " + world
		}
	} else {
		label = "mixed"
	}
	if len(loci) == 1 {
		label += " @ " + loci[0]
	} else if len(loci) > 1 {
		if common := commonHierarchicalLocusPrefix(loci); common != "" {
			label += " @ " + common
		}
	}
	return label
}

func commonHierarchicalLocusPrefix(loci []string) string {
	if len(loci) == 0 || !strings.Contains(loci[0], "/") {
		return ""
	}
	common := memcore.SplitHierarchicalSegments(loci[0])
	if len(common) == 0 {
		return ""
	}
	for _, loc := range loci[1:] {
		if !strings.Contains(loc, "/") {
			return ""
		}
		segs := memcore.SplitHierarchicalSegments(loc)
		k := 0
		for k < len(common) && k < len(segs) && strings.EqualFold(common[k], segs[k]) {
			k++
		}
		if k == 0 {
			return ""
		}
		common = common[:k]
	}
	return strings.Join(common, "/")
}

// --------------------------------------------------------------- rendering

func renderRecall(result *Result, maxChars int) string {
	gapCount := 0
	for _, value := range result.Gaps {
		if value != 0 {
			gapCount++
		}
	}
	// The block always carries the current local time (second precision, explicit
	// zone): a reader must be able to place a recalled moment against "now" without
	// guessing today's date or the zone the labels live in.
	attrs := fmt.Sprintf("scope=%q now=%q results=%q raw=%q absent=%q gaps=%q ms=%q",
		result.Scope, ids.FormatTimeSeconds(clock.Unix(), ""),
		strconv.Itoa(len(result.Bands)), strconv.Itoa(len(result.RawHits)),
		strconv.FormatBool(result.Absent), strconv.Itoa(gapCount),
		asString(defaultAny(result.Meta["elapsed_ms"], 0)))
	opening := "<rolling-memory-recall " + attrs + ">"
	closing := "</rolling-memory-recall>"
	lines := []string{}
	for _, band := range result.Bands {
		kind := asString(band["kind"])
		if kind == "episodic_turn_band" {
			lines = append(lines, episodicTurnBandLines(band)...)
			continue
		}
		if kind == "cell_band" || kind == "noetic_band" {
			lines = append(lines, cellBandItemLines(band)...)
			continue
		}
		head, parenParts := bandHead(band)
		lines = append(lines, head+labelParen(parenParts...))
		chains, _ := band["chains"].([]any)
		for _, chainValue := range chains {
			chain := chainValue.(map[string]any)
			lines = append(lines, "  ["+plainLine(chain["type"])+"] "+plainLine(chain["label"])+labelParen(chain["time_label"]))
			nodes, _ := chain["nodes"].([]map[string]any)
			for _, node := range nodes {
				lines = append(lines, nodeLine(node))
			}
		}
		if chains == nil || len(chains) == 0 {
			nodes, _ := band["nodes"].([]map[string]any)
			for _, node := range nodes {
				lines = append(lines, nodeLine(node))
			}
		}
	}
	if len(result.RawHits) > 0 {
		undelivered := make([]map[string]any, 0, len(result.RawHits))
		for _, hit := range result.RawHits {
			// A hit already delivered as a band must not be printed twice: the thin
			// path marks those, so the block stays one readable stream.
			if value, ok := hit["in_band"].(bool); ok && value {
				continue
			}
			undelivered = append(undelivered, hit)
		}
		if len(undelivered) > 0 {
			lines = append(lines, "原文命中（未入图）:")
		}
		for _, hit := range undelivered {
			suffix := ""
			if tags, ok := hit["relative"].([]any); ok && len(tags) > 0 {
				dates := []string{}
				for index, tagValue := range tags {
					if index >= 2 {
						break
					}
					tag := tagValue.(map[string]any)
					dates = append(dates, asString(tag["date"]))
				}
				suffix = "（相对时点 " + strings.Join(dates, "、") + "）"
			}
			timePrefix := ""
			if occ := asInt64(hit["occurred_at"]); occ > 0 {
				if formatted := ids.FormatTimeSeconds(occ, ""); formatted != "" {
					timePrefix = formatted + " "
				}
			}
			lines = append(lines, "  - ["+plainLine(hit["session"])+":"+plainLine(hit["turn"])+"] "+
				timePrefix+plainLine(hit["text"])+suffix)
		}
	}
	activeGaps := []string{}
	for _, key := range gapOrder {
		value := result.Gaps[key]
		if value == 0 {
			continue
		}
		if label, ok := gapLabels[key]; ok {
			activeGaps = append(activeGaps, label+" "+strconv.Itoa(value))
		}
	}
	if len(activeGaps) > 0 {
		lines = append([]string{"缺口: " + strings.Join(activeGaps, " · ")}, lines...)
	}
	if result.Absent {
		lines = append(lines, "不持有：本地图中没有与该线索相关的记忆；可换更具体的名称，或继续对话，系统会自动记录。")
	}
	if truncated, ok := result.Meta["truncated"].(map[string]any); ok {
		if value, ok := truncated["chars"].(bool); ok && value {
			lines = append(lines, "（已截断：受交付预算限制）")
		}
		bandsTrimmed, _ := truncated["bands"].(bool)
		nodesTrimmed, _ := truncated["nodes"].(bool)
		rawTrimmed, _ := truncated["raw"].(bool)
		if bandsTrimmed || nodesTrimmed || rawTrimmed {
			lines = append([]string{"（已按预算裁剪：部分结果未显示，可用 caps / offset 调整）"}, lines...)
		}
	}

	body := strings.Join(lines, "\n")
	budget := maxChars - utf8.RuneCountInString(opening) - utf8.RuneCountInString(closing) - 2
	if budget < 200 {
		budget = 200
	}
	if utf8.RuneCountInString(body) > budget {
		trimmed := prefixRunes(body, budget)
		// Cut on a line boundary so the block never ends inside a speaker line.
		if boundary := strings.LastIndex(trimmed, "\n"); boundary > 0 {
			trimmed = trimmed[:boundary]
		}
		body = strings.TrimRightFunc(trimmed, unicode.IsSpace) + "\n…（已截断）"
		if truncated, ok := result.Meta["truncated"].(map[string]any); ok {
			truncated["chars"] = true
		}
	}
	if body != "" {
		return opening + "\n" + body + "\n" + closing
	}
	return opening + "\n" + closing
}

// cellBandItemLines renders a flat source item without the retired grouping
// header. One memory is one line; the source label is carried inline so repeated
// workspace names do not become repeated section titles.
func cellBandItemLines(band map[string]any) []string {
	nodes, _ := band["nodes"].([]map[string]any)
	if len(nodes) == 0 {
		return nil
	}
	source := strings.TrimSpace(asString(band["space_label"]))
	if source == "" && asString(band["kind"]) == "noetic_band" {
		source = "noetic"
	}
	if source == "" {
		source = bandLocus(band)
	}
	if source == "" {
		title := displayMemoryTitle(asString(band["title"]))
		if title != "" && !isAddressTitle(title) {
			source = title
		}
	}
	if source == "" {
		source = "记忆"
	}
	source = plainLine(source)
	lines := make([]string, 0, len(nodes))
	for _, node := range nodes {
		rawContent := strings.TrimSpace(asString(node["content"]))
		contentLines := nonEmptyTrimmedLines(rawContent)
		if source != "" && len(contentLines) > 0 {
			tag := "[" + source + "]"
			if contentLines[0] == tag && len(contentLines) > 1 {
				contentLines = contentLines[1:]
			} else if strings.HasPrefix(contentLines[0], tag+" ") {
				if rest := strings.TrimSpace(strings.TrimPrefix(contentLines[0], tag)); rest != "" {
					contentLines[0] = rest
				}
			}
		}
		prefix := "  - "
		if source != "" {
			prefix += "[" + source + "] "
		}
		if len(contentLines) <= 1 {
			singleNode := node
			if len(contentLines) == 1 && contentLines[0] != rawContent {
				singleNode = make(map[string]any, len(node))
				for k, v := range node {
					singleNode[k] = v
				}
				singleNode["content"] = contentLines[0]
			}
			line := strings.TrimPrefix(nodeLine(singleNode), "    - ")
			lines = append(lines, prefix+line)
			continue
		}
		firstNode := make(map[string]any, len(node))
		for k, v := range node {
			firstNode[k] = v
		}
		firstNode["content"] = contentLines[0]
		firstLine := strings.TrimPrefix(nodeLine(firstNode), "    - ")
		lines = append(lines, prefix+firstLine)
		for _, extra := range contentLines[1:] {
			lines = append(lines, "    "+plainLine(extra))
		}
	}
	return lines
}

func nonEmptyTrimmedLines(text string) []string {
	if !strings.Contains(text, "\n") {
		if trimmed := strings.TrimSpace(text); trimmed != "" {
			return []string{trimmed}
		}
		return nil
	}
	raw := strings.Split(text, "\n")
	out := make([]string, 0, len(raw))
	for _, line := range raw {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// episodicTurnBandLines renders one assembled conversation turn. The segments keep
// their own lines and speaker order; only the header carries provenance, because a
// per-line metadata suffix would flatten the conversation back into the unreadable
// wall the assembly just removed. The header deliberately carries no strength
// number: the caller reads the ordered content, not a scalar that impersonates
// authority (see relativeStrength in the thin client).
func episodicTurnBandLines(band map[string]any) []string {
	title := plainLine(band["title"])
	nodes, _ := band["nodes"].([]map[string]any)
	if len(nodes) == 0 {
		return []string{title}
	}
	node := nodes[0]
	rendered := asString(node["content"])
	parts := make([]string, 0, 5)
	timeText := strings.TrimSpace(asString(band["time_label"]))
	if timeText == "" || timeText == "unknown" || timeText == "时间未知" {
		timeText = strings.TrimSpace(asString(node["time"]))
	}
	if (timeText == "" || timeText == "unknown" || timeText == "时间未知") && asInt64(node["occurred_at"]) > 0 {
		timeText = ids.FormatTimeSeconds(asInt64(node["occurred_at"]), "")
	}
	if timeText != "" && timeText != "unknown" && timeText != "时间未知" {
		parts = append(parts, plainLine(timeText))
	}
	if locus := strings.TrimSpace(asString(node["locus"])); locus != "" {
		parts = append(parts, plainLine(locus))
	}
	segments := int(asFloat(node["turn_segments"], 0))
	if window, ok := node["segment_window"].([]int); ok && len(window) == 2 {
		if window[0] == window[1] && segments <= 1 {
			parts = append(parts, fmt.Sprintf("段 %d", window[0]))
		} else {
			parts = append(parts, fmt.Sprintf("段 %d–%d", window[0], window[1]))
			if segments > 0 {
				parts = append(parts, strconv.Itoa(segments)+" 段")
			}
		}
	} else if segments > 0 {
		parts = append(parts, strconv.Itoa(segments)+" 段")
	}
	header := title
	if len(parts) > 0 {
		header += "（" + strings.Join(parts, " · ") + "）"
	}
	lines := []string{header}
	rawLines := strings.Split(rendered, "\n")
	multiLineTurn := len(rawLines) > 1
	for idx, line := range rawLines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		isSpeakerLine := strings.HasPrefix(trimmed, "[user]") ||
			strings.HasPrefix(trimmed, "[assistant]") ||
			strings.HasPrefix(trimmed, "[agent]") ||
			strings.HasPrefix(trimmed, "[tool]")
		// Keep speaker lead lines at 2-space indent ("  [assistant] ...") and indent
		// subordinate body lines (lists, tables, headings) inside a multi-line turn at
		// 4 spaces ("    - ...") so nested bullets never collide visually with top-level
		// "  - [Repo] ..." cell_band items.
		if isSpeakerLine || (!multiLineTurn && idx == 0) {
			lines = append(lines, "  "+plainLine(trimmed))
		} else {
			lines = append(lines, "    "+plainLine(trimmed))
		}
	}
	return lines
}

// bandHead builds the agent-facing header for one band: the visible label plus
// the parenthetical labels that are not already part of it. Thin-client cell
// bands carry an internal ladder address as their title ("带·C6:…#00"), which
// means nothing to a reader; for those the header falls back to the space label
// (file coordinates), the node's workspace locus, or a neutral label — and the
// space label is then not repeated in the parens.
func bandHead(band map[string]any) (string, []any) {
	title := strings.TrimSpace(asString(band["title"]))
	space := strings.TrimSpace(asString(band["space_label"]))
	if !isAddressTitle(title) {
		// A source-built title (thin bands prefer the file label) must not repeat
		// the space label in the parens.
		display := displayMemoryTitle(title)
		if space != "" && strings.Contains(display, space) {
			return plainLine(display), []any{band["time_label"]}
		}
		return plainLine(display), []any{band["time_label"], band["space_label"]}
	}
	if space != "" {
		return plainLine(space), []any{band["time_label"]}
	}
	if locus := bandLocus(band); locus != "" {
		return plainLine(locus), []any{band["time_label"]}
	}
	return "记忆", []any{band["time_label"]}
}

// displayMemoryTitle removes the retired subject-band prefix from reader-facing
// output. The internal compatibility shape may still carry it, but the delivered
// text never re-introduces the old taxonomy.
func displayMemoryTitle(title string) string {
	title = strings.TrimSpace(title)
	title = strings.TrimPrefix(title, "带·")
	title = strings.TrimPrefix(title, "记忆带·")
	if title == "记忆带" {
		return "记忆"
	}
	return title
}

// isAddressTitle reports whether a band title is only an internal addressing
// coordinate (ladder "C6:…" cells or "ast:…" incarnations) with no reader value.
func isAddressTitle(title string) bool {
	body := displayMemoryTitle(title)
	if body == "" {
		return false
	}
	return strings.HasPrefix(body, "C6:") || strings.HasPrefix(body, "ast:") ||
		strings.Contains(body, "/C6:") || strings.Contains(body, "/ast:")
}

// bandLocus returns the first workspace locus carried by a band's nodes, walking
// chains when the band has no flat node list.
func bandLocus(band map[string]any) string {
	if nodes, ok := band["nodes"].([]map[string]any); ok {
		for _, node := range nodes {
			if locus := strings.TrimSpace(asString(node["locus"])); locus != "" {
				return locus
			}
		}
	}
	chains, _ := band["chains"].([]any)
	for _, chainValue := range chains {
		chain, ok := chainValue.(map[string]any)
		if !ok {
			continue
		}
		nodes, _ := chain["nodes"].([]map[string]any)
		for _, node := range nodes {
			if locus := strings.TrimSpace(asString(node["locus"])); locus != "" {
				return locus
			}
		}
	}
	return ""
}

func nodeLine(node map[string]any) string {
	parts := []string{}
	if status := strings.TrimSpace(asString(node["status"])); status != "" && status != "active" {
		parts = append(parts, plainLine(status))
	}
	if anchorOK, ok := node["anchor_ok"].(bool); ok && !anchorOK {
		parts = append(parts, "锚异常")
	}
	if value := strings.TrimSpace(asString(node["reason"])); value != "" {
		parts = append(parts, plainLine(value))
	}
	if tags, ok := node["relative"].([]any); ok && len(tags) > 0 {
		dates := []string{}
		for index, tagValue := range tags {
			if index >= 2 {
				break
			}
			tag := tagValue.(map[string]any)
			dates = append(dates, asString(tag["date"]))
		}
		parts = append(parts, "相对时点 "+strings.Join(dates, "、"))
	}
	prefix := ""
	if text := strings.TrimSpace(asString(node["time"])); text != "" && text != "unknown" {
		prefix = plainLine(text) + " "
	}
	line := "    - " + prefix + plainLine(node["content"])
	if len(parts) > 0 {
		line += "（" + strings.Join(parts, " · ") + "）"
	}
	return line
}

// labelParen renders the "（time · space）" suffix with empty parts collapsed;
// it returns "" when every part is empty so headers never carry empty parens.
func labelParen(parts ...any) string {
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		text := strings.TrimSpace(asString(part))
		if text == "" || text == "unknown" {
			continue
		}
		values = append(values, plainLine(text))
	}
	if len(values) == 0 {
		return ""
	}
	return "（" + strings.Join(values, " · ") + "）"
}

// plainLine flattens one content line for the delivery block: newlines become
// spaces so a node never breaks the line structure. Content is delivered as
// plain text — no HTML entity escaping, because the block is read by an agent
// (and often quoted back into code) rather than parsed as markup.
func plainLine(value any) string {
	text := strings.ReplaceAll(asString(value), "\n", " ")
	return strings.ReplaceAll(text, "\r", " ")
}

// ------------------------------------------------------------------ helpers

func pickCap(value *int, fallback int) int {
	if value == nil || *value == 0 {
		return fallback
	}
	return *value
}

func canonicalValue(mapping map[string]string, key string) string {
	if value, ok := mapping[key]; ok {
		return value
	}
	return key
}

func candidateIDs(candidates []store.Candidate) []string {
	values := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		values = append(values, candidate.Data.NodeID)
	}
	return values
}

func maxScore(group []*bandCandidate) float64 {
	maximum := 0.0
	for _, entry := range group {
		sc := entry.candidate.Score
		if !strings.HasPrefix(entry.candidate.Data.Scope, "mem:golden:") && entry.candidate.LexScore > 0 {
			sc += entry.candidate.LexScore
		}
		if sc > maximum {
			maximum = sc
		}
	}
	return maximum
}

func setFromKeys(values []string) map[string]struct{} {
	result := map[string]struct{}{}
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

func stringSet(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	return result
}

func rawHitMaps(hits []store.RawHit) []map[string]any {
	result := make([]map[string]any, 0, len(hits))
	for _, hit := range hits {
		result = append(result, map[string]any{
			"trace": hit.Trace, "session": hit.Session, "turn": hit.Turn,
			"author": hit.Author, "occurred_at": hit.OccurredAt, "text": hit.Text,
		})
	}
	return result
}

// weaveBySession interleaves a raw-hit pool by session: one pass per session (in first-appearance
// order) for up to maxPerSession rounds, then the remainder in original order. It exists so a
// multi-day window query covers every contributing session instead of letting one session take all
// delivery slots, and is rank-blind by construction — it reads only each hit's own "session" field
// and its arrival order, never a delivery slot or a rank index.
func weaveBySession(hits []map[string]any, maxPerSession int) []map[string]any {
	if len(hits) == 0 || maxPerSession < 1 {
		return hits
	}
	bySession := make(map[string][]int, len(hits))
	order := make([]string, 0, len(hits))
	for i, hit := range hits {
		s := asString(hit["session"])
		if _, ok := bySession[s]; !ok {
			order = append(order, s)
		}
		bySession[s] = append(bySession[s], i)
	}
	out := make([]map[string]any, 0, len(hits))
	taken := make([]bool, len(hits))
	for round := 0; round < maxPerSession; round++ {
		for _, s := range order {
			idx := bySession[s]
			if round < len(idx) {
				out = append(out, hits[idx[round]])
				taken[idx[round]] = true
			}
		}
	}
	for i, hit := range hits {
		if !taken[i] {
			out = append(out, hit)
		}
	}
	return out
}

func capSlice[T any](values []T, limit int) []T {
	if len(values) > limit {
		return values[:limit]
	}
	return values
}

func ptrStringValue(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func shortHash(value string) string {
	if len(value) <= 8 {
		return value
	}
	return value[:8]
}

func pyRound(value float64, digits int) float64 {
	text := strconv.FormatFloat(value, 'f', digits, 64)
	parsed, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return value
	}
	return parsed
}

func prefixRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

func sliceRunes(value string, start, end int) string {
	runes := []rune(value)
	if start < 0 || end < start || start >= len(runes) {
		return ""
	}
	if end > len(runes) {
		end = len(runes)
	}
	return string(runes[start:end])
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}

// deliveredIDs lists the ids of every delivered node in first-seen order
// (standard builds a set here; the read-side walk order is the deterministic
// stand-in for its unordered iteration).
func deliveredIDs(bands []map[string]any) []string {
	result := []string{}
	seen := map[string]struct{}{}
	for _, band := range bands {
		nodes, _ := band["nodes"].([]map[string]any)
		for _, node := range nodes {
			nodeID := asString(node["id"])
			if nodeID == "" {
				continue
			}
			if _, ok := seen[nodeID]; ok {
				continue
			}
			seen[nodeID] = struct{}{}
			result = append(result, nodeID)
		}
	}
	return result
}

// activationEndpoints lists the S/O endpoint ids of the candidates.
func activationEndpoints(candidates []store.Candidate) []string {
	values := []string{}
	for _, candidate := range candidates {
		for _, value := range []*string{candidate.Data.SubjectID, candidate.Data.ObjectID} {
			if value != nil && *value != "" {
				values = append(values, *value)
			}
		}
	}
	return values
}

// activationRankFactor is the salience factor:
// ×(1 + 0.02·ln(1+act)), capped at ×1.25, size-neutral (reorder only).
func activationRankFactor(data store.CandidateRow, acts map[string]float64) float64 {
	act := 0.0
	for _, value := range []*string{data.SubjectID, data.ObjectID} {
		if value == nil || *value == "" {
			continue
		}
		if current := acts[*value]; current > act {
			act = current
		}
	}
	if act <= 0 {
		return 1.0
	}
	return mathMin(1.25, 1.0+0.02*math.Log1p(act))
}

func mathMax(left, right float64) float64 {
	if left > right {
		return left
	}
	return right
}

func mathMin(left, right float64) float64 {
	if left < right {
		return left
	}
	return right
}

func nullableSessionValue(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func asString(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case bool:
		if typed {
			return "true"
		}
		return "false"
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case float64:
		return strconv.FormatFloat(typed, 'g', -1, 64)
	}
	return fmt.Sprint(value)
}

func asInt64(value any) int64 {
	switch typed := value.(type) {
	case int:
		return int64(typed)
	case int64:
		return typed
	case float64:
		return int64(typed)
	case string:
		parsed, err := strconv.ParseInt(typed, 10, 64)
		if err == nil {
			return parsed
		}
	}
	return 0
}

func asFloat(value any, fallback float64) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case float32:
		return float64(typed)
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	case string:
		parsed, err := strconv.ParseFloat(typed, 64)
		if err == nil {
			return parsed
		}
	}
	return fallback
}

func defaultAny(value any, fallback any) any {
	if value == nil {
		return fallback
	}
	return value
}
