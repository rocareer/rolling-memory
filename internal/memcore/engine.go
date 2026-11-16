// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package memcore

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// Engine is the unified, database-agnostic memory kernel that executes both
// write-side structured memory assertion graph encoding (with inline AST sub_assertion/@k/#id resolution)
// and read-side deterministic recall (lexical/morphological IDF scoring, 0–96 band
// monotonic convex marginal budget pruning, and single-row self-covering rendering)
// on top of any StoreBackend (PostgreSQL or SQLite).
type Engine struct {
	Backend StoreBackend
}

// NewEngine constructs a unified memory kernel bound to backend.
func NewEngine(backend StoreBackend) *Engine {
	return &Engine{Backend: backend}
}

// WriteInput represents a single structured assertion write request in a batch.
type WriteInput struct {
	Subject       string         `json:"subject"`
	SubjectAbsent bool           `json:"subject_absent"`
	SubjectGap    string         `json:"subject_gap"`
	Rel           string         `json:"rel"`
	Object        string         `json:"object"`
	ObjectLiteral string         `json:"object_literal"`
	ObjectGap     string         `json:"object_gap"`
	Holder        string         `json:"holder"`
	HolderGap     string         `json:"holder_gap"`
	Anchor        string         `json:"anchor"`
	Warrant       string         `json:"warrant"`
	Audience      string         `json:"audience"`
	World         string         `json:"world"`
	Carrier       string         `json:"carrier"`
	Locus         string         `json:"locus"`
	TimeExpr      string         `json:"time_expr"`
	ClosesPrior   bool           `json:"closes_prior"`
	Confidence    float64        `json:"confidence"`
	SubAssertion  map[string]any `json:"sub_assertion,omitempty"`
}

// RememberBatchResult holds the outcome of a batch write on Engine.
type RememberBatchResult struct {
	TraceHash string      `json:"trace_hash"`
	Written   []Assertion `json:"written"`
	Rejected  []string    `json:"rejected,omitempty"`
}

// RememberBatch persists a source trace and a batch of structured assertions
// (supporting nested sub_assertion AST expansion, intra-batch @k references, and #<node_id> references).
func (e *Engine) RememberBatch(ctx context.Context, trace Trace, rawBatch []map[string]any) (*RememberBatchResult, error) {
	if e == nil || e.Backend == nil {
		return nil, fmt.Errorf("memcore: engine backend is nil")
	}
	scope := strings.TrimSpace(trace.Scope)
	if scope == "" {
		return nil, fmt.Errorf("memcore: scope is required")
	}
	if trace.Audience == "" {
		trace.Audience = "shared"
	}
	savedTrace, _, err := e.Backend.AppendTrace(ctx, trace)
	if err != nil {
		return nil, err
	}
	expanded := ExpandInlineSubAssertions(rawBatch)
	batchIDs := make([]string, len(expanded))
	res := &RememberBatchResult{
		TraceHash: savedTrace.Hash,
		Written:   make([]Assertion, 0, len(expanded)),
	}
	now := trace.OccurredAt
	if now <= 0 {
		now = 1
	}

	resolveSlotNode := func(raw string, kind string, world string) (string, string, error) {
		val := strings.TrimSpace(raw)
		if val == "" {
			return "", "", nil
		}
		if strings.HasPrefix(val, "#") {
			refID := strings.TrimSpace(strings.TrimPrefix(val, "#"))
			if refID != "" {
				return refID, refID, nil
			}
		}
		if kind == "referent" {
			if found, ok, err := e.Backend.FindNodeByName(ctx, scope, val); err == nil && ok && found != nil {
				return found.ID, found.Label, nil
			}
		}
		n, _, err := e.Backend.EnsureNode(ctx, scope, kind, val, trace.Audience, world, now)
		if err != nil || n == nil {
			return "", "", err
		}
		return n.ID, n.Label, nil
	}

	executed := make([]bool, len(expanded))
	hasUnmetAtRef := func(selfIdx int, item map[string]any) bool {
		for _, slot := range []string{"subject", "s", "object", "o"} {
			v := strings.TrimSpace(asMapString(item[slot]))
			if !strings.HasPrefix(v, "@") {
				continue
			}
			if k, kerr := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(v, "@"))); kerr == nil && k >= 1 && k <= len(expanded) && k-1 != selfIdx {
				if !executed[k-1] && strings.TrimSpace(batchIDs[k-1]) == "" {
					return true
				}
			}
		}
		return false
	}

	execItem := func(i int, item map[string]any) error {
		executed[i] = true
		if item == nil {
			return nil
		}
		subj := firstNonEmptyMapStr(item, "subject", "s")
		rel := firstNonEmptyMapStr(item, "rel", "x")
		obj := firstNonEmptyMapStr(item, "object", "o")
		objLit := firstNonEmptyMapStr(item, "object_literal", "o_literal")
		anchor := firstNonEmptyMapStr(item, "anchor", "anchor_text")
		if anchor == "" {
			anchor = trace.Text
		}
		if strings.HasPrefix(subj, "@") {
			subj = ResolveBatchAtRefString(subj, expanded, batchIDs, i, anchor)
			if strings.HasPrefix(subj, "@") && !strings.Contains(trace.Text, subj) {
				res.Rejected = append(res.Rejected, fmt.Sprintf("unresolved @k reference in subject: %s", subj))
				return nil
			}
		}
		if strings.HasPrefix(obj, "@") {
			obj = ResolveBatchAtRefString(obj, expanded, batchIDs, i, anchor)
			if strings.HasPrefix(obj, "@") && !strings.Contains(trace.Text, obj) {
				res.Rejected = append(res.Rejected, fmt.Sprintf("unresolved @k reference in object: %s", obj))
				return nil
			}
		}
		subjAbsent, _ := item["subject_absent"].(bool)
		subjGap := firstNonEmptyMapStr(item, "subject_gap")
		objGap := firstNonEmptyMapStr(item, "object_gap")
		holder := firstNonEmptyMapStr(item, "holder")
		if holder == "" {
			holder = trace.Author
		}
		holderGap := firstNonEmptyMapStr(item, "holder_gap")
		namedHolder := strings.TrimSpace(holder)
		if IsRolePlaceholder(namedHolder) {
			namedHolder = ""
			if holder != "" {
				holder = ""
				if holderGap == "" {
					holderGap = "absent_in_source"
				}
			}
		}
		if subj == "" && !subjAbsent && subjGap == "" && namedHolder != "" {
			subj = namedHolder
		}
		if reason := ValidateUnboundAndDualForm(subj, 0, subjAbsent, subjGap, rel, obj, objLit, 0, objGap, holder, holderGap); reason != "" {
			res.Rejected = append(res.Rejected, reason)
			return nil
		}
		world := firstNonEmptyMapStr(item, "world")
		if world == "" {
			world = "actual"
		}
		carrier := firstNonEmptyMapStr(item, "carrier")
		if carrier == "" {
			carrier = "virtual"
		}
		// Slot creation order is strictly s -> x -> o.
		sID, sLabel, err := resolveSlotNode(subj, "referent", world)
		if err != nil {
			return err
		}
		xID, xLabel, err := resolveSlotNode(rel, "relation_type", world)
		if err != nil {
			return err
		}
		oID, oLabel, err := resolveSlotNode(obj, "referent", world)
		if err != nil {
			return err
		}
		hID, _, _ := resolveSlotNode(holder, "referent", world)

		// Anchor is a hard requirement: a caller-supplied anchor must be a verbatim span of the
		// source trace. An anchor that cannot be found is rejected instead of silently widening to
		// the whole trace (which would let an unanchored claim masquerade as evidence).
		aStart, aEnd := 0, len(trace.Text)
		if supplied := strings.TrimSpace(anchor); supplied != "" {
			idx := strings.Index(trace.Text, supplied)
			if idx < 0 {
				res.Rejected = append(res.Rejected, "anchor is not a verbatim span of the source trace")
				return nil
			}
			aStart = idx
			aEnd = aStart + len(supplied)
		}
		timeExpr := firstNonEmptyMapStr(item, "time_expr")
		anchorSent := ExtractAnchorSentence(trace.Text, aStart, aEnd)
		tEvent, evSrc := ResolveAssertionTime(timeExpr, anchor, anchorSent, trace.OccurredAt)

		warrant := firstNonEmptyMapStr(item, "warrant")
		if warrant == "" {
			warrant = "referential"
		}
		tc := "unknown"
		if tEvent > 0 {
			tc = "dated"
		}
		a := Assertion{
			Scope:           scope,
			SubjectID:       sID,
			SubjectAbsent:   subjAbsent,
			SubjectGap:      NormalizeGap(subjGap),
			RelID:           xID,
			ObjectID:        oID,
			ObjectLiteral:   objLit,
			ObjectGap:       NormalizeGap(objGap),
			HolderID:        hID,
			HolderGap:       NormalizeGap(holderGap),
			Warrant:         warrant,
			Confidence:      0.9,
			TemporalClass:   tc,
			TSource:         trace.OccurredAt,
			TValidFrom:      ResolveEffectiveValidFrom(0, tEvent, trace.OccurredAt, warrant),
			TEvent:          tEvent,
			EventSrc:        evSrc,
			TRecord:         now,
			World:           world,
			Carrier:         carrier,
			Locus:           firstNonEmptyMapStr(item, "locus"),
			Audience:        trace.Audience,
			Status:          "active",
			LoopState:       InitialLoopState(warrant, firstNonEmptyMapStr(item, "loop_state")),
			AnchorTraceHash: savedTrace.Hash,
			AnchorStart:     aStart,
			AnchorEnd:       aEnd,
			SourceTraceHash: savedTrace.Hash,
			CreatedAt:       now,
		}
		labelParts := []string{}
		if sLabel != "" {
			labelParts = append(labelParts, sLabel)
		}
		if xLabel != "" {
			labelParts = append(labelParts, xLabel)
		}
		if oLabel != "" {
			labelParts = append(labelParts, oLabel)
		} else if objLit != "" {
			labelParts = append(labelParts, objLit)
		}
		savedAssert, _, err := e.Backend.AppendAssertion(ctx, a, strings.Join(labelParts, " "))
		if err != nil {
			return err
		}
		if closesPrior, _ := item["closes_prior"].(bool); closesPrior && savedAssert != nil && savedAssert.NodeID != "" {
			vt := tEvent
			if vt <= 0 {
				vt = now
			}
			_, _ = e.Backend.ClosePriorAssertions(ctx, scope, savedAssert.NodeID, sID, xID, oID, objLit, vt, now)
		}
		batchIDs[i] = savedAssert.NodeID
		res.Written = append(res.Written, *savedAssert)
		return nil
	}

	for pass := 0; pass < len(expanded); pass++ {
		progressed := false
		for i, item := range expanded {
			if executed[i] {
				continue
			}
			if hasUnmetAtRef(i, item) {
				continue
			}
			if err := execItem(i, item); err != nil {
				return nil, err
			}
			progressed = true
		}
		if !progressed {
			break
		}
	}
	for i, item := range expanded {
		if executed[i] {
			continue
		}
		if err := execItem(i, item); err != nil {
			return nil, err
		}
	}
	return res, nil
}

// ScoredAssertion pairs an Assertion with its resolved labels, snippet, and 0-LLM lexical/morphological score.
type ScoredAssertion struct {
	Assertion   Assertion
	SubjectName string
	RelName     string
	ObjectName  string
	Snippet     string
	Score       float64
	NonSubj     bool
	DirectXO    bool
}

// RecallBand represents one assembled subject band ready for rendering or structured delivery.
type RecallBand struct {
	SubjectID   string            `json:"subject_id"`
	SubjectName string            `json:"subject_name"`
	Score       float64           `json:"score"`
	Rows        []ScoredAssertion `json:"rows"`
	Rendered    string            `json:"rendered"`
}

// ScoreAndAssembleBands runs the 0-LLM deterministic recall pipeline over candidate assertions:
//  1. Monotone IDF + YASS morphological / stem / compound matching across S, X, O, and verbatim snippet;
//  2. Intra-turn sibling score inheritance;
//  3. Subject band grouping and 0–96 band monotonic convex marginal signal budget pruning;
//  4. Single-row self-covering snippet elision (StmtCoversSnippet) and band text rendering.
func ScoreAndAssembleBands(query string, items []ScoredAssertion, maxRowsPerBand int) []RecallBand {
	if len(items) == 0 {
		return nil
	}
	if maxRowsPerBand <= 0 {
		maxRowsPerBand = 12
	}
	rawToks := Tokenize(query)
	tokens := make([]string, 0, len(rawToks))
	stemOf := make(map[string]string, len(rawToks))
	df := make(map[string]int, len(rawToks))
	for _, tok := range rawToks {
		t := strings.ToLower(strings.TrimSpace(tok))
		if t == "" {
			continue
		}
		if _, exists := df[t]; !exists {
			df[t] = 0
			stemOf[t] = StemToken(t)
			tokens = append(tokens, t)
		}
	}

	type cachedItemFields struct {
		sub, rel, obj, snip string
	}
	fieldsCache := make([]cachedItemFields, len(items))
	slotMatchCache := make(map[[2]string]float64, len(items)*len(tokens))
	evalField := func(hay, tok, st string) float64 {
		if hay == "" || tok == "" {
			return 0
		}
		if len(hay) <= 1024 {
			k := [2]string{hay, tok}
			if v, ok := slotMatchCache[k]; ok {
				return v
			}
			v := MatchTokenOrStemWithStem(hay, tok, st, false)
			slotMatchCache[k] = v
			return v
		}
		return MatchTokenOrStemWithStem(hay, tok, st, false)
	}

	for i := range items {
		sub := strings.ToLower(items[i].SubjectName)
		rel := strings.ToLower(items[i].RelName)
		obj := strings.ToLower(items[i].ObjectName)
		if obj == "" {
			obj = strings.ToLower(items[i].Assertion.ObjectLiteral)
		}
		snip := strings.ToLower(items[i].Snippet)
		fieldsCache[i] = cachedItemFields{sub: sub, rel: rel, obj: obj, snip: snip}
		for _, tok := range tokens {
			st := stemOf[tok]
			if evalField(sub, tok, st) > 0 ||
				evalField(rel, tok, st) > 0 ||
				evalField(obj, tok, st) > 0 ||
				evalField(snip, tok, st) > 0 {
				df[tok]++
			}
		}
	}

	n := float64(len(items))
	idfW := make(map[string]float64, len(tokens))
	for _, tok := range tokens {
		c := df[tok]
		if c <= 0 {
			idfW[tok] = 1.0 + math.Log((n+1.0)/2.0)
		} else {
			idfW[tok] = 1.0 + math.Log((n+1.0)/(float64(c)+1.0))
		}
	}

	// Zero-wall-clock determinism: derive the reference timestamp endogenously from the candidate
	// set's latest source/record timestamp unless a test explicitly injected a reference clock.
	var refNow int64
	if injected, ok := InjectedNowUnix(); ok {
		refNow = injected
	} else {
		for i := range items {
			if items[i].Assertion.TSource > refNow {
				refNow = items[i].Assertion.TSource
			}
			if items[i].Assertion.TRecord > refNow {
				refNow = items[i].Assertion.TRecord
			}
		}
	}

	var baseScoreSum float64
	var baseScoredCount int
	queryTerminalIDs := make(map[uint64]bool, len(items))
	nodeDeg := make(map[uint64]int, len(items)*2)

	for i := range items {
		cf := fieldsCache[i]
		sub, rel, obj, snip := cf.sub, cf.rel, cf.obj, cf.snip
		sc := 0.0
		matched := 0
		nonSubj := false
		directXO := false
		subHit := false
		objHit := false
		for _, tok := range tokens {
			st := stemOf[tok]
			w := idfW[tok]
			hit := false
			if f := evalField(sub, tok, st); f > 0 {
				sc += 2.0 * w * f
				hit = true
				subHit = true
			}
			if f := evalField(obj, tok, st); f > 0 {
				sc += 2.5 * w * f
				hit = true
				nonSubj = true
				directXO = true
				objHit = true
			}
			if f := evalField(rel, tok, st); f > 0 {
				sc += 1.8 * w * f
				hit = true
				nonSubj = true
				directXO = true
			}
			if f := evalField(snip, tok, st); f > 0 {
				sc += 1.2 * w * f
				hit = true
				nonSubj = true
			}
			if hit {
				matched++
			}
		}
		if matched >= 2 && nonSubj {
			dm := float64(matched - 1)
			sc += dm * dm * 2.5
		}
		if sc > 0 {
			sc *= ZeigarnikLoopBoost(items[i].Assertion.LoopState, items[i].Assertion.Status, items[i].Assertion.TEvent, refNow, items[i].Assertion.TSource)
			baseScoreSum += sc
			baseScoredCount++
		}
		items[i].Score = sc
		items[i].NonSubj = nonSubj
		items[i].DirectXO = directXO

		sid := HashStringIDToUint64(items[i].Assertion.SubjectID)
		oid := HashStringIDToUint64(items[i].Assertion.ObjectID)
		if sid > 0 {
			nodeDeg[sid]++
			if subHit {
				queryTerminalIDs[sid] = true
			}
		}
		if oid > 0 {
			nodeDeg[oid]++
			if objHit {
				queryTerminalIDs[oid] = true
			}
		}
	}

	_ = queryTerminalIDs
	_ = nodeDeg
	_ = baseScoreSum
	_ = baseScoredCount
	_ = refNow

	bySubj := make(map[string]*RecallBand)
	order := []string{}
	for _, it := range items {
		key := it.Assertion.SubjectID
		if key == "" {
			key = "_xo_"
		}
		b, ok := bySubj[key]
		if !ok {
			b = &RecallBand{
				SubjectID:   key,
				SubjectName: it.SubjectName,
			}
			bySubj[key] = b
			order = append(order, key)
		}
		b.Rows = append(b.Rows, it)
		if it.Score > b.Score {
			b.Score = it.Score
		}
	}

	rawBands := make([]RecallBand, 0, len(order))
	for _, k := range order {
		b := bySubj[k]
		sort.SliceStable(b.Rows, func(i, j int) bool {
			if b.Rows[i].Score != b.Rows[j].Score {
				return b.Rows[i].Score > b.Rows[j].Score
			}
			return b.Rows[i].Assertion.TSource > b.Rows[j].Assertion.TSource
		})
		rawBands = append(rawBands, *b)
	}
	sort.SliceStable(rawBands, func(i, j int) bool {
		return rawBands[i].Score > rawBands[j].Score
	})

	reachObj := map[string]bool{}
	reachTrace := map[string]bool{}
	if len(rawBands) > 0 {
		for _, r := range rawBands[0].Rows {
			if r.Assertion.ObjectID != "" {
				reachObj[r.Assertion.ObjectID] = true
			}
			if r.Assertion.AnchorTraceHash != "" {
				reachTrace[r.Assertion.AnchorTraceHash] = true
			}
		}
	}

	out := make([]RecallBand, 0, len(rawBands))
	connAccepted, unconnAccepted := 0, 0
	for bi, b := range rawBands {
		if len(out) >= MaxPrimaryBands {
			break
		}
		maxNS := 0.0
		isTraceConn := false
		for _, r := range b.Rows {
			if r.NonSubj && r.Score > maxNS {
				maxNS = r.Score
			}
			if r.Assertion.AnchorTraceHash != "" && reachTrace[r.Assertion.AnchorTraceHash] {
				isTraceConn = true
			}
		}
		isDirectConn := reachObj[b.SubjectID]
		pass, incC, incU := MonotonicConvexBandPass(bi, len(rawBands), len(b.Rows), connAccepted, unconnAccepted, isDirectConn, isTraceConn, true, maxNS)
		if !pass {
			continue
		}
		if incC {
			connAccepted++
		}
		if incU {
			unconnAccepted++
		}
		if len(b.Rows) > maxRowsPerBand {
			b.Rows = b.Rows[:maxRowsPerBand]
		}
		var sb strings.Builder
		title := b.SubjectName
		if title == "" {
			title = "客观事件"
		}
		sb.WriteString("带·" + title)
		for _, r := range b.Rows {
			objText := r.ObjectName
			if objText == "" {
				objText = r.Assertion.ObjectLiteral
			}
			stmt := strings.TrimSpace(r.SubjectName + " —" + r.RelName + "→ " + objText)
			dateTag := LabelFor("zh", LabelTimeUnknown)
			if r.Assertion.TEvent > 0 {
				dateTag = DateUTC(r.Assertion.TEvent)
			} else if r.Assertion.TSource > 0 {
				dateTag = DateUTC(r.Assertion.TSource)
			}
			line := fmt.Sprintf("\n  · %s ｜ %s", dateTag, stmt)
			snip := StripMultimodalLongParen(r.Snippet, 8, 480)
			if snip != "" && !StmtCoversSnippet(stmt, snip) {
				line += fmt.Sprintf("（「%s」）", snip)
			}
			if lTag := FormatLoopStateTag(r.Assertion.LoopState, false); lTag != "" {
				line += " ｜ " + lTag
			}
			// Non-active rows are historical versions: keep them visible but marked, so a reader
			// never mistakes a retired value for the current one.
			if st := strings.TrimSpace(r.Assertion.Status); st != "" && st != "active" {
				line += " ｜ [" + st + "]"
			}
			sb.WriteString(line)
		}
		b.Rendered = sb.String()
		out = append(out, b)
	}
	return out
}

func firstNonEmptyMapStr(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok && v != nil {
			if s := strings.TrimSpace(asMapString(v)); s != "" {
				return s
			}
		}
	}
	return ""
}
