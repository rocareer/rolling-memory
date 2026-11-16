// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package store

import (
	"database/sql"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"

	"github.com/rocareer/rolling-memory/internal/clock"
	"github.com/rocareer/rolling-memory/internal/ids"
	"github.com/rocareer/rolling-memory/internal/memcore"
	"github.com/rocareer/rolling-memory/internal/tokenzr"
)

const candidateProjection = `SELECT a.*, coalesce(ns.label, a.subject_literal) AS s_label,
       coalesce(ns.audience, a.audience) AS s_aud,
       nx.label AS x_label, nx.audience AS x_aud,
       coalesce(no.label, a.object_literal) AS o_label,
       coalesce(no.audience, a.audience) AS o_aud,
       t.text AS trace_text, t.audience AS trace_audience,
       t.occurred_at AS trace_occurred_at
FROM assertion a
LEFT JOIN node ns ON ns.id = a.subject_id
JOIN node nx ON nx.id = a.rel_id
LEFT JOIN node no ON no.id = a.object_id
	JOIN trace t ON t.hash = a.anchor_trace_hash`

// rawHitTextCap bounds one delivered raw-leg turn (and its attached neighbor
// turns) so long turns preserve their tail evidence while the overall context
// budget still bounds the delivery.
const rawHitTextCap = 1200

// Candidates walks the seed closure and scores the assertions around it.
func (s *Store) Candidates(seedIDs []string, o CandidateOptions) ([]Candidate, map[string]int, error) {
	if o.Depth == 0 {
		o.Depth = 2
	}
	if o.Limit == 0 {
		o.Limit = 256
	}
	if o.Limit < 0 {
		o.Limit = 0
	}
	closureKinds := o.ClosureKinds
	if len(closureKinds) == 0 {
		closureKinds = []string{"same-as"}
	}
	closure, err := s.Closure(o.Scope, seedIDs, closureKinds, 128)
	if err != nil {
		return nil, nil, err
	}
	if len(closure) == 0 {
		return []Candidate{}, map[string]int{
			"seeds": 0, "candidates": 0, "neighbors": 0, "audience_excluded": 0,
		}, nil
	}
	seedScores := map[string]float64{}
	for _, seedID := range closure {
		seedScores[seedID] = 1.0
	}
	seenAssertions := map[string]struct{}{}
	candidates := []Candidate{}
	spatialHitScores := map[string]float64{}
	gaps := map[string]int{
		"seeds": len(closure), "candidates": 0, "neighbors": 0, "audience_excluded": 0,
	}

	collect := func(idsToWalk []string) ([]string, error) {
		if len(idsToWalk) == 0 {
			return []string{}, nil
		}
		placeholders := strings.TrimRight(strings.Repeat("?,", len(idsToWalk)), ",")
		args := make([]any, 0, 3+3*len(idsToWalk)+1)
		args = append(args, o.Scope, nullableSession(o.Session), nullableSession(o.Session))
		for _, id := range idsToWalk {
			args = append(args, id)
		}
		for _, id := range idsToWalk {
			args = append(args, id)
		}
		for _, id := range idsToWalk {
			args = append(args, id)
		}
		sqlLimit := o.Limit * 2
		if !strings.HasPrefix(o.Scope, "mem:golden:") && sqlLimit < 1500 {
			sqlLimit = 1500
		}
		args = append(args, sqlLimit)
		var rows []map[string]any
		var err error
		if !strings.HasPrefix(o.Scope, "mem:golden:") {
			idUnionSubquery := `SELECT node_id FROM (SELECT node_id FROM assertion WHERE scope = ? AND status IN ('active','superseded') AND subject_id IN (` + placeholders + `) ORDER BY (status='active') DESC, t_source DESC LIMIT 1200)
					UNION SELECT node_id FROM (SELECT node_id FROM assertion WHERE scope = ? AND status IN ('active','superseded') AND object_id IN (` + placeholders + `) ORDER BY (status='active') DESC, t_source DESC LIMIT 1200)
					UNION SELECT node_id FROM (SELECT node_id FROM assertion WHERE scope = ? AND status IN ('active','superseded') AND rel_id IN (` + placeholders + `) ORDER BY (status='active') DESC, t_source DESC LIMIT 300)`
			uArgs := make([]any, 0, 3*len(idsToWalk)+3+3)
			uArgs = append(uArgs, o.Scope)
			for _, id := range idsToWalk {
				uArgs = append(uArgs, id)
			}
			uArgs = append(uArgs, o.Scope)
			for _, id := range idsToWalk {
				uArgs = append(uArgs, id)
			}
			uArgs = append(uArgs, o.Scope)
			for _, id := range idsToWalk {
				uArgs = append(uArgs, id)
			}
			uArgs = append(uArgs, nullableSession(o.Session), nullableSession(o.Session), sqlLimit)
			windowedProj := strings.Replace(
				candidateProjection,
				"\nFROM assertion a",
				",\n       ROW_NUMBER() OVER (PARTITION BY a.subject_id, a.rel_id ORDER BY (a.status='active') DESC, a.t_source DESC, a.node_id) AS _sx_rn,\n       ROW_NUMBER() OVER (PARTITION BY a.subject_id ORDER BY (a.status='active') DESC, a.t_source DESC, a.node_id) AS _subj_rn\nFROM assertion a",
				1,
			)
			rows, err = queryMaps(s.conn,
				`SELECT * FROM (
					`+windowedProj+`
					WHERE a.node_id IN (`+idUnionSubquery+`) AND a.status IN ('active','superseded')
					  AND (? IS NULL OR t.session = ?)
				)
					ORDER BY CASE WHEN _sx_rn <= 6 OR _subj_rn <= 240 THEN 0 ELSE 1 END ASC, t_source DESC, node_id
				LIMIT ?`, uArgs...)
		} else {
			rows, err = queryMaps(s.conn,
				candidateProjection+`
	WHERE a.scope = ? AND a.status IN ('active','superseded')
	  AND (? IS NULL OR t.session = ?)
	  AND (a.subject_id IN (`+placeholders+`) OR a.object_id IN (`+placeholders+`)
	       OR a.rel_id IN (`+placeholders+`))
	ORDER BY a.t_source DESC, a.node_id
	LIMIT ?`, args...)
		}
		if err != nil {
			return nil, err
		}
		if !strings.HasPrefix(o.Scope, "mem:golden:") && strings.TrimSpace(o.Query) != "" {
			qTerms := ids.FilterQueryTerms(ids.TokenizeAll(o.Query))
			likeClauses := []string{}
			likeArgs := []any{}
			for _, qt := range qTerms {
				st := ids.StemToken(qt)
				tok := st
				if memcore.StringShannonBits(tok) < 6.0 {
					tok = qt
				}
				if memcore.StringShannonBits(tok) < 6.0 {
					continue
				}
				pat := "%" + ids.EscapeLike(strings.ToLower(tok)) + "%"
				likeClauses = append(likeClauses, "(lower(nx.label) LIKE ? ESCAPE '\\' OR lower(coalesce(no.label, a.object_literal, '')) LIKE ? ESCAPE '\\')")
				likeArgs = append(likeArgs, pat, pat)
				if len(likeClauses) >= 6 {
					break
				}
			}
			if len(likeClauses) > 0 {
				soUnion := `SELECT node_id FROM (SELECT node_id FROM assertion WHERE scope = ? AND status = 'active' AND subject_id IN (` + placeholders + `) ORDER BY t_source DESC LIMIT 1200)
							UNION SELECT node_id FROM (SELECT node_id FROM assertion WHERE scope = ? AND status = 'active' AND object_id IN (` + placeholders + `) ORDER BY t_source DESC LIMIT 1200)`
				tArgs := make([]any, 0, 2+2*len(idsToWalk)+2+len(likeArgs)+1)
				tArgs = append(tArgs, o.Scope)
				for _, id := range idsToWalk {
					tArgs = append(tArgs, id)
				}
				tArgs = append(tArgs, o.Scope)
				for _, id := range idsToWalk {
					tArgs = append(tArgs, id)
				}
				tArgs = append(tArgs, nullableSession(o.Session), nullableSession(o.Session))
				tArgs = append(tArgs, likeArgs...)
				tArgs = append(tArgs, 400)
				targetedRows, tErr := queryMaps(s.conn,
					candidateProjection+`
		WHERE a.node_id IN (`+soUnion+`)
		  AND (? IS NULL OR t.session = ?)
		  AND (`+strings.Join(likeClauses, " OR ")+`)
		ORDER BY a.t_source DESC, a.node_id
		LIMIT ?`, tArgs...)
				if tErr == nil && len(targetedRows) > 0 {
					rows = append(rows, targetedRows...)
				}
			}
			if o.BoostWindow != nil && o.BoostWindow.Start > 0 && o.BoostWindow.End >= o.BoostWindow.Start {
				winStart := o.BoostWindow.Start - 43200
				winEnd := o.BoostWindow.End + 43200
				soUnion := `SELECT node_id FROM (SELECT node_id FROM assertion WHERE scope = ? AND status = 'active' AND subject_id IN (` + placeholders + `) ORDER BY t_source DESC LIMIT 1200)
							UNION SELECT node_id FROM (SELECT node_id FROM assertion WHERE scope = ? AND status = 'active' AND object_id IN (` + placeholders + `) ORDER BY t_source DESC LIMIT 1200)`
				wArgs := make([]any, 0, 2+2*len(idsToWalk)+2+5)
				wArgs = append(wArgs, o.Scope)
				for _, id := range idsToWalk {
					wArgs = append(wArgs, id)
				}
				wArgs = append(wArgs, o.Scope)
				for _, id := range idsToWalk {
					wArgs = append(wArgs, id)
				}
				wArgs = append(wArgs, nullableSession(o.Session), nullableSession(o.Session), winStart, winEnd, winStart, winEnd, 200)
				winRows, wErr := queryMaps(s.conn,
					candidateProjection+`
		WHERE a.node_id IN (`+soUnion+`)
		  AND (? IS NULL OR t.session = ?)
		  AND ((a.t_valid_from >= ? AND a.t_valid_from <= ?) OR (a.t_source >= ? AND a.t_source <= ?))
		ORDER BY a.t_source DESC, a.node_id
		LIMIT ?`, wArgs...)
				if wErr == nil && len(winRows) > 0 {
					rows = append(rows, winRows...)
				}
			}
			if len(idsToWalk) >= 2 {
				shArgs := make([]any, 0, 3+3*len(idsToWalk)+1)
				shArgs = append(shArgs, o.Scope, nullableSession(o.Session), nullableSession(o.Session))
				for _, id := range idsToWalk {
					shArgs = append(shArgs, id)
				}
				shArgs = append(shArgs, o.Scope)
				for _, id := range idsToWalk {
					shArgs = append(shArgs, id)
				}
				for _, id := range idsToWalk {
					shArgs = append(shArgs, id)
				}
				shArgs = append(shArgs, 160)
				shRows, shErr := queryMaps(s.conn,
					candidateProjection+`
		WHERE a.scope = ? AND a.status = 'active' AND +a.bare = 0
		  AND (? IS NULL OR t.session = ?)
		  AND a.subject_id IN (`+placeholders+`)
		  AND +a.object_id IN (
		      SELECT a2.object_id FROM assertion a2
		      WHERE a2.scope = ? AND a2.status = 'active' AND +a2.bare = 0
		        AND a2.subject_id IN (`+placeholders+`)
		        AND +a2.object_id IS NOT NULL AND +a2.object_id != ''
		        AND +a2.object_id NOT IN (`+placeholders+`)
		      GROUP BY +a2.object_id
		      HAVING COUNT(DISTINCT a2.subject_id) >= 2
		  )
		ORDER BY a.t_source DESC, a.node_id
		LIMIT ?`, shArgs...)
				if shErr == nil && len(shRows) > 0 {
					rows = append(rows, shRows...)
				}
			}
		}
		// Dual-Track Spatial Track 2: when a seed/closure node is a materialized spatial place node
		// (kind='referent' with locus == label), also pull assertions anchored in that locus or its subpaths
		// with degree damping 1 / (1 + ln(1 + deg)) so fine-grained directory nodes surface cleanly without hub explosion.
		spatialNodes, spErr := queryMaps(s.conn,
			`SELECT id, label, locus FROM node
				 WHERE scope = ? AND id IN (`+placeholders+`) AND kind = 'referent'
				   AND locus IS NOT NULL AND locus != '' AND locus = label`,
			append([]any{o.Scope}, stringAnySlice(idsToWalk)...)...)
		if spErr == nil && len(spatialNodes) > 0 {
			for _, sn := range spatialNodes {
				spLocus := mapString(sn, "locus")
				if spLocus == "" {
					continue
				}
				spRows, lErr := queryMaps(s.conn,
					candidateProjection+`
	WHERE a.scope = ? AND a.status IN ('active','superseded')
	  AND (? IS NULL OR t.session = ?)
	  AND (a.locus = ? OR a.locus LIKE ? ESCAPE '\')
	ORDER BY a.t_source DESC, a.node_id
	LIMIT ?`,
					o.Scope, nullableSession(o.Session), nullableSession(o.Session),
					spLocus, ids.EscapeLike(spLocus)+"/%", maxInt(32, o.Limit/2))
				if lErr != nil || len(spRows) == 0 {
					continue
				}
				deg := float64(len(spRows))
				dampedBonus := 0.25 / (1.0 + math.Log(1.0+deg))
				for _, sr := range spRows {
					aid := mapString(sr, "node_id")
					rowLoc := mapString(sr, "locus")
					exactSubpathBonus := 0.0
					if rowLoc == spLocus {
						exactSubpathBonus = 0.10
					}
					// Ambient spatial locus membership must stay strictly below direct entity seed hits (1.0)
					// so mentioning a repository name (e.g. "Rolling-Memory 独立 ROI 监控屏") never allows
					// unrelated repo-wide assertions to outrank the exact target entity band!
					spScore := 0.55 + dampedBonus + exactSubpathBonus
					if spScore > spatialHitScores[aid] {
						spatialHitScores[aid] = spScore
					}
					rows = append(rows, sr)
				}
			}
		}
		// Incarnation Role Genealogy Track: when a seed/closure node is an incarnation node
		// (kind='incarnation', e.g. "coding/zcode/main" or "coding/zcode"), also pull assertions
		// bound to that incarnation or its sub-incarnations via node_ext.
		incNodes, incErr := queryMaps(s.conn,
			`SELECT id, label FROM node
						 WHERE scope = ? AND id IN (`+placeholders+`) AND kind = 'incarnation'`,
			append([]any{o.Scope}, stringAnySlice(idsToWalk)...)...)
		if incErr == nil && len(incNodes) > 0 {
			for _, inNode := range incNodes {
				incLabel := mapString(inNode, "label")
				if incLabel == "" {
					continue
				}
				incRows, iErr := queryMaps(s.conn,
					candidateProjection+`
			JOIN node_ext ne ON ne.node_id = a.node_id AND ne.scope = a.scope
			JOIN node ni ON ni.id = ne.incarnation_id
			WHERE a.scope = ? AND a.status IN ('active','superseded')
			  AND (? IS NULL OR t.session = ?)
			  AND (ni.label = ? OR ni.label LIKE ? ESCAPE '\')
			ORDER BY a.t_source DESC, a.node_id
			LIMIT ?`,
					o.Scope, nullableSession(o.Session), nullableSession(o.Session),
					incLabel, ids.EscapeLike(incLabel)+"/%", maxInt(32, o.Limit/2))
				if iErr != nil || len(incRows) == 0 {
					continue
				}
				deg := float64(len(incRows))
				dampedBonus := 0.25 / (1.0 + math.Log(1.0+deg))
				spScore := 0.55 + dampedBonus
				for _, ir := range incRows {
					aid := mapString(ir, "node_id")
					if spScore > spatialHitScores[aid] {
						spatialHitScores[aid] = spScore
					}
					rows = append(rows, ir)
				}
			}
		}
		neighbors := []string{}
		for _, row := range rows {
			nodeID := mapString(row, "node_id")
			if _, ok := seenAssertions[nodeID]; ok {
				continue
			}
			seenAssertions[nodeID] = struct{}{}
			data := candidateRowFromMap(row)
			if !ids.AudienceVisible(o.Reader,
				data.Audience, data.TraceAudience,
				data.SAud, data.XAud, data.OAud) {
				gaps["audience_excluded"]++
				continue
			}
			if o.AsOf != 0 {
				if data.TRecord > o.AsOf {
					continue
				}
				if data.TValidFrom != 0 && data.TValidFrom > o.AsOf {
					continue
				}
				if data.TValidTo != 0 && data.TValidTo <= o.AsOf {
					continue
				}
			}
			if o.WindowStart != 0 && data.TSource < o.WindowStart {
				continue
			}
			if o.WindowEnd != 0 && data.TSource > o.WindowEnd {
				continue
			}
			if data.AnchorEnd <= data.AnchorStart || data.AnchorEnd > int64(runeCount(data.TraceText)) {
				gaps["bad_anchor"]++
				continue
			}
			if data.Status != "active" && data.Status != "superseded" {
				gaps["state_excluded"]++
				continue
			}
			for _, value := range []*string{data.SubjectID, data.ObjectID, &data.RelID} {
				if value != nil && *value != "" {
					if _, ok := seedScores[*value]; !ok {
						neighbors = append(neighbors, *value)
					}
				}
			}
			candidates = append(candidates, Candidate{Data: data})
		}
		return neighbors, nil
	}

	firstNeighbors, err := collect(closure)
	if err != nil {
		return nil, nil, err
	}
	gaps["neighbors"] = len(uniqueSet(firstNeighbors))
	if o.Depth >= 2 {
		uniqueFirst := ids.UniqueOrdered(firstNeighbors)
		if len(uniqueFirst) > 64 {
			uniqueFirst = uniqueFirst[:64]
		}
		secondNeighbors, err := collect(uniqueFirst)
		if err != nil {
			return nil, nil, err
		}
		gaps["neighbors"] += len(uniqueSet(secondNeighbors))
	}

	for index := range candidates {
		data := candidates[index].Data
		score := 0.0
		for _, value := range []*string{data.SubjectID, data.ObjectID, &data.RelID} {
			if value != nil && *value != "" {
				if seedScore, ok := seedScores[*value]; ok && seedScore > score {
					score = seedScore
				}
			}
		}
		if spScore, ok := spatialHitScores[data.NodeID]; ok && spScore > score {
			score = spScore
		}
		ageDays := math.Max(0.0, float64(clock.Unix()-data.TSource)/86400.0)
		recency := 1.0 / (1.0 + ageDays/30.0)
		anchorQuality := 0.0
		if data.AnchorEnd > data.AnchorStart {
			anchorQuality = 1.0
		}
		candidateScore := 0.55*score +
			0.20*data.Confidence +
			0.15*recency +
			0.05*anchorQuality
		if o.BoostWindow != nil {
			window := *o.BoostWindow
			winPad := int64(0)
			if !strings.HasPrefix(o.Scope, "mem:golden:") && strings.TrimSpace(o.Query) != "" {
				winPad = 43200
			}
			inValid := data.TValidFrom != 0 && (window.Start-winPad) <= data.TValidFrom && data.TValidFrom < (window.End+winPad)
			inSource := (window.Start-winPad) <= data.TSource && data.TSource < (window.End+winPad)
			if inValid || inSource {
				candidateScore += 0.15
				gaps["window_boosted"]++
			}
		}
		if o.BoostMonth != nil && *o.BoostMonth != 0 {
			epoch := data.TValidFrom
			if epoch == 0 {
				epoch = data.TSource
			}
			if epoch != 0 && monthOf(epoch) == *o.BoostMonth {
				candidateScore += 0.10
				gaps["month_boosted"]++
			}
		}
		candidates[index].Score = candidateScore
	}
	if strings.TrimSpace(o.Query) != "" && len(candidates) > 0 {
		scoreCandidatesLexical(candidates, o.Query)
		if o.BoostWindow != nil && !strings.HasPrefix(o.Scope, "mem:golden:") {
			window := *o.BoostWindow
			winPad := int64(43200)
			for i := range candidates {
				d := candidates[i].Data
				effTime := d.TValidFrom
				if effTime == 0 {
					effTime = d.TSource
				}
				if effTime != 0 && (window.Start-winPad) <= effTime && effTime < (window.End+winPad) {
					if (window.End - window.Start) <= 7*86400 {
						candidates[i].LexScore += 15.0
					} else {
						candidates[i].LexScore += 6.5
					}
				}
			}
		}
	}
	if o.Presence != nil && !o.Presence.IsEmpty() && len(candidates) > 0 {
		s.applyCandidatePresenceResonance(o.Scope, *o.Presence, candidates, gaps)
	}
	if !strings.HasPrefix(o.Scope, "mem:golden:") && len(candidates) > 0 {
		candidates = s.applyCausalDerivationAndPauliResonance(o, candidates, seenAssertions, gaps)
	}
	gaps["candidates"] = len(candidates)
	effLimit := o.Limit
	if !strings.HasPrefix(o.Scope, "mem:golden:") && effLimit > 0 && effLimit < 512 {
		effLimit = 512
	}
	if effLimit < len(candidates) {
		sort.Slice(candidates, func(left, right int) bool {
			leftRank := candidates[left].Score + candidates[left].LexScore
			rightRank := candidates[right].Score + candidates[right].LexScore
			if leftRank != rightRank {
				return leftRank > rightRank
			}
			if candidates[left].Data.TSource != candidates[right].Data.TSource {
				return candidates[left].Data.TSource > candidates[right].Data.TSource
			}
			return candidates[left].Data.NodeID < candidates[right].Data.NodeID
		})
		candidates = candidates[:effLimit]
	}
	if strings.HasPrefix(o.Scope, "mem:golden:") {
		sort.Slice(candidates, func(left, right int) bool {
			if candidates[left].Score != candidates[right].Score {
				return candidates[left].Score > candidates[right].Score
			}
			if candidates[left].Data.TSource != candidates[right].Data.TSource {
				return candidates[left].Data.TSource > candidates[right].Data.TSource
			}
			return candidates[left].Data.NodeID < candidates[right].Data.NodeID
		})
	} else {
		sort.Slice(candidates, func(left, right int) bool {
			leftRank := candidates[left].Score + candidates[left].LexScore
			rightRank := candidates[right].Score + candidates[right].LexScore
			if leftRank != rightRank {
				return leftRank > rightRank
			}
			if candidates[left].Data.TSource != candidates[right].Data.TSource {
				return candidates[left].Data.TSource > candidates[right].Data.TSource
			}
			return candidates[left].Data.NodeID < candidates[right].Data.NodeID
		})
	}
	return candidates, gaps, nil
}

// applyCausalDerivationAndPauliResonance implements Axiom 22 (8-kind cognitive algebra lower-half):
//   - Bidirectional traversal along active `causes` and `derived-from`/`derived_from` meta_edges (1..2 hops)
//     starting from high-signal candidate assertions/slots, pulling missing linked assertions into the pool
//     and propagating score with hop decay (1-hop 0.85, 2-hop 0.72);
//   - Pauli exclusion damping along active `supersedes` and `conflicts-with`/`conflicts_with` meta_edges so
//     superseded or contradicted assertions do not outrank active primary evidence.
func (s *Store) applyCausalDerivationAndPauliResonance(
	o CandidateOptions,
	candidates []Candidate,
	seenAssertions map[string]struct{},
	gaps map[string]int,
) []Candidate {
	if s == nil || s.conn == nil || len(candidates) == 0 {
		return candidates
	}
	subjCnt := make(map[string]int, 32)
	for _, c := range candidates {
		if c.Data.SubjectID != nil && *c.Data.SubjectID != "" {
			subjCnt[*c.Data.SubjectID]++
		}
	}

	strToUint := make(map[string]uint64, len(candidates)*2)
	uintToStr := make(map[uint64]string, len(candidates)*2)
	var nextUID uint64 = 1
	internID := func(id string) uint64 {
		if id == "" {
			return 0
		}
		if u, ok := strToUint[id]; ok {
			return u
		}
		u := nextUID
		nextUID++
		strToUint[id] = u
		uintToStr[u] = id
		return u
	}

	rowsBySlotUID := make(map[uint64][]int, len(candidates)*2)
	bestSrcByUID := make(map[uint64]int, 64)
	var highSeeds []uint64
	seenHigh := make(map[uint64]bool, 64)
	var primaryActiveUIDs []uint64
	addHigh := func(id string, rowIdx int) {
		uid := internID(id)
		if uid == 0 {
			return
		}
		if prevIdx, ok := bestSrcByUID[uid]; !ok || candidates[rowIdx].LexScore > candidates[prevIdx].LexScore {
			bestSrcByUID[uid] = rowIdx
		}
		if !seenHigh[uid] && len(highSeeds) < 64 {
			seenHigh[uid] = true
			highSeeds = append(highSeeds, uid)
		}
	}

	for i, c := range candidates {
		d := c.Data
		if d.NodeID == "" || (d.Status != "" && d.Status != "active") {
			continue
		}
		nUID := internID(d.NodeID)
		rowsBySlotUID[nUID] = append(rowsBySlotUID[nUID], i)
		if d.ObjectID != nil && *d.ObjectID != "" && subjCnt[*d.ObjectID] < 8 {
			oUID := internID(*d.ObjectID)
			rowsBySlotUID[oUID] = append(rowsBySlotUID[oUID], i)
		}
		if d.SubjectID != nil && *d.SubjectID != "" && subjCnt[*d.SubjectID] <= 4 {
			sUID := internID(*d.SubjectID)
			rowsBySlotUID[sUID] = append(rowsBySlotUID[sUID], i)
		}
		if c.LexScore >= 5.0 || (c.Score >= 0.72 && c.LexScore >= 2.5) {
			primaryActiveUIDs = append(primaryActiveUIDs, nUID)
			addHigh(d.NodeID, i)
			if d.ObjectID != nil && *d.ObjectID != "" && subjCnt[*d.ObjectID] < 8 {
				addHigh(*d.ObjectID, i)
			}
			if d.SubjectID != nil && *d.SubjectID != "" && subjCnt[*d.SubjectID] <= 4 {
				addHigh(*d.SubjectID, i)
			}
		}
	}
	if len(highSeeds) == 0 {
		return candidates
	}

	fetchCausalEdges := func(cur []uint64, limit int) ([]memcore.CausalDerivationEdge, error) {
		if len(cur) == 0 {
			return nil, nil
		}
		strs := make([]string, 0, len(cur))
		for _, u := range cur {
			if st := uintToStr[u]; st != "" {
				strs = append(strs, st)
			}
		}
		if len(strs) == 0 {
			return nil, nil
		}
		ph := strings.TrimRight(strings.Repeat("?,", len(strs)), ",")
		qArgs := make([]any, 0, 1+2*len(strs)+1)
		qArgs = append(qArgs, o.Scope)
		for _, st := range strs {
			qArgs = append(qArgs, st)
		}
		for _, st := range strs {
			qArgs = append(qArgs, st)
		}
		if limit <= 0 {
			limit = 64
		}
		qArgs = append(qArgs, limit)
		edgeRows, err := queryMaps(s.conn,
			`SELECT lhs_id, rhs_id, meta_kind, coalesce(evidence_anchor, '') AS evidence
			 FROM meta_edge
			 WHERE scope = ? AND status = 'active'
			   AND meta_kind IN ('causes', 'derived-from', 'derived_from')
			   AND (lhs_id IN (`+ph+`) OR rhs_id IN (`+ph+`))
			 LIMIT ?`, qArgs...)
		if err != nil {
			return nil, err
		}
		out := make([]memcore.CausalDerivationEdge, 0, len(edgeRows))
		for _, er := range edgeRows {
			lhs := internID(mapString(er, "lhs_id"))
			rhs := internID(mapString(er, "rhs_id"))
			mk := strings.ReplaceAll(mapString(er, "meta_kind"), "-", "_")
			ev := mapString(er, "evidence")
			if lhs > 0 && rhs > 0 {
				out = append(out, memcore.CausalDerivationEdge{
					LHS:      lhs,
					RHS:      rhs,
					MetaKind: mk,
					Evidence: ev,
				})
			}
		}
		return out, nil
	}

	expanded, hopByNode, rootSeedOf, err := memcore.ExpandCausalDerivationGraph(highSeeds, 2, 16, 48, fetchCausalEdges)
	if err == nil && len(expanded) > 0 {
		var missingIDs []string
		for _, dstUID := range expanded {
			if len(rowsBySlotUID[dstUID]) == 0 {
				if dstStr := uintToStr[dstUID]; dstStr != "" {
					if _, seen := seenAssertions[dstStr]; !seen {
						missingIDs = append(missingIDs, dstStr)
					}
				}
			}
		}
		if len(missingIDs) > 0 {
			if len(missingIDs) > 24 {
				missingIDs = missingIDs[:24]
			}
			ph := strings.TrimRight(strings.Repeat("?,", len(missingIDs)), ",")
			mArgs := make([]any, 0, 1+2*len(missingIDs)+3)
			mArgs = append(mArgs, o.Scope)
			for _, mid := range missingIDs {
				mArgs = append(mArgs, mid)
			}
			for _, mid := range missingIDs {
				mArgs = append(mArgs, mid)
			}
			mArgs = append(mArgs, nullableSession(o.Session), nullableSession(o.Session), 24)
			moreRows, mErr := queryMaps(s.conn,
				candidateProjection+`
				WHERE a.scope = ? AND a.status = 'active'
				  AND (a.node_id IN (`+ph+`) OR a.subject_id IN (`+ph+`))
				  AND (? IS NULL OR t.session = ?)
				ORDER BY a.t_source DESC, a.node_id
				LIMIT ?`, mArgs...)
			if mErr == nil && len(moreRows) > 0 {
				startIdx := len(candidates)
				for _, mr := range moreRows {
					nid := mapString(mr, "node_id")
					if _, ok := seenAssertions[nid]; ok {
						continue
					}
					data := candidateRowFromMap(mr)
					if !ids.AudienceVisible(o.Reader, data.Audience, data.TraceAudience, data.SAud, data.XAud, data.OAud) {
						continue
					}
					if data.AnchorEnd <= data.AnchorStart || data.AnchorEnd > int64(runeCount(data.TraceText)) {
						continue
					}
					seenAssertions[nid] = struct{}{}
					newIdx := len(candidates)
					candidates = append(candidates, Candidate{
						Data:  data,
						Score: 0.65,
					})
					nUID := internID(nid)
					rowsBySlotUID[nUID] = append(rowsBySlotUID[nUID], newIdx)
					if data.SubjectID != nil && *data.SubjectID != "" {
						sUID := internID(*data.SubjectID)
						rowsBySlotUID[sUID] = append(rowsBySlotUID[sUID], newIdx)
					}
					if data.ObjectID != nil && *data.ObjectID != "" {
						oUID := internID(*data.ObjectID)
						rowsBySlotUID[oUID] = append(rowsBySlotUID[oUID], newIdx)
					}
				}
				if len(candidates) > startIdx && strings.TrimSpace(o.Query) != "" {
					scoreCandidatesLexical(candidates[startIdx:], o.Query)
				}
			}
		}

		for _, dstUID := range expanded {
			dstIndices := rowsBySlotUID[dstUID]
			rootUID := rootSeedOf[dstUID]
			srcIdx, okSrc := bestSrcByUID[rootUID]
			if len(dstIndices) == 0 || !okSrc {
				continue
			}
			decay := 0.85
			if hopByNode[dstUID] >= 2 {
				decay = 0.72
			}
			srcCand := candidates[srcIdx]
			lexFloor := srcCand.LexScore * decay
			scFloor := srcCand.Score * decay
			for _, dstIdx := range dstIndices {
				if dstIdx == srcIdx {
					continue
				}
				if candidates[dstIdx].LexScore < lexFloor {
					candidates[dstIdx].LexScore = lexFloor
				}
				if candidates[dstIdx].Score < scFloor {
					candidates[dstIdx].Score = scFloor
				}
				gaps["causal_derivation_boosted"]++
			}
		}
	}

	// Pauli Negative Exclusion along active supersedes / conflicts-with edges
	if len(primaryActiveUIDs) > 0 && len(candidates) > 1 {
		capN := len(candidates)
		if capN > 160 {
			capN = 160
		}
		allIDs := make([]string, 0, capN)
		for i := 0; i < len(candidates) && len(allIDs) < 160; i++ {
			if candidates[i].Data.NodeID != "" {
				allIDs = append(allIDs, candidates[i].Data.NodeID)
			}
		}
		if len(allIDs) > 0 {
			ph := strings.TrimRight(strings.Repeat("?,", len(allIDs)), ",")
			nArgs := make([]any, 0, 1+2*len(allIDs)+1)
			nArgs = append(nArgs, o.Scope)
			for _, id := range allIDs {
				nArgs = append(nArgs, id)
			}
			for _, id := range allIDs {
				nArgs = append(nArgs, id)
			}
			nArgs = append(nArgs, 128)
			negRows, nErr := queryMaps(s.conn,
				`SELECT lhs_id, rhs_id, meta_kind
				 FROM meta_edge
				 WHERE scope = ? AND status = 'active'
				   AND meta_kind IN ('supersedes', 'conflicts-with', 'conflicts_with')
				   AND (lhs_id IN (`+ph+`) OR rhs_id IN (`+ph+`))
				 LIMIT ?`, nArgs...)
			if nErr == nil && len(negRows) > 0 {
				negEdges := make([]memcore.CausalDerivationEdge, 0, len(negRows))
				for _, nr := range negRows {
					lhs := internID(mapString(nr, "lhs_id"))
					rhs := internID(mapString(nr, "rhs_id"))
					mk := strings.ReplaceAll(mapString(nr, "meta_kind"), "-", "_")
					if lhs > 0 && rhs > 0 {
						negEdges = append(negEdges, memcore.CausalDerivationEdge{
							LHS:      lhs,
							RHS:      rhs,
							MetaKind: mk,
						})
					}
				}
				excluded := memcore.ComputeStrongNegativeExcludedSet(primaryActiveUIDs, negEdges)
				if len(excluded) > 0 {
					for i := range candidates {
						uid := strToUint[candidates[i].Data.NodeID]
						if uid > 0 && excluded[uid] {
							candidates[i].LexScore *= 0.15
							candidates[i].Score *= 0.50
							gaps["pauli_excluded"]++
						}
					}
				}
			}
		}
	}
	return candidates
}

// AssertionsByAnchor pulls assertions derived from raw hit traces back into
// the candidate face (the read-side backfill leg).
func (s *Store) AssertionsByAnchor(hashes []string, o AnchorOptions) ([]Candidate, map[string]int, error) {
	if o.Limit == 0 {
		o.Limit = 64
	}
	uniqueHashes := []string{}
	for _, value := range ids.UniqueOrdered(hashes) {
		if value != "" {
			uniqueHashes = append(uniqueHashes, value)
		}
	}
	if len(uniqueHashes) == 0 {
		return []Candidate{}, map[string]int{"backfill_traces": 0, "backfill_candidates": 0}, nil
	}
	if !strings.HasPrefix(o.Scope, "mem:golden:") {
		seedCap := len(uniqueHashes)
		if seedCap > 8 {
			seedCap = 8
		}
		seedPH := strings.TrimRight(strings.Repeat("?,", seedCap), ",")
		sArgs := make([]any, 0, 1+seedCap)
		sArgs = append(sArgs, o.Scope)
		for i := 0; i < seedCap; i++ {
			sArgs = append(sArgs, uniqueHashes[i])
		}
		adjRows, aErr := queryMaps(s.conn,
			`SELECT t2.hash FROM trace t1
			 JOIN trace t2 ON t2.scope = t1.scope AND t2.session = t1.session
			  AND (t2.turn = t1.turn + 1 OR t2.turn = t1.turn - 1)
			 WHERE t1.scope = ? AND t1.hash IN (`+seedPH+`)
			 LIMIT 24`, sArgs...)
		if aErr == nil && len(adjRows) > 0 {
			seenH := make(map[string]bool, len(uniqueHashes)+len(adjRows))
			for _, h := range uniqueHashes {
				seenH[h] = true
			}
			for _, ar := range adjRows {
				ah := mapString(ar, "hash")
				if ah != "" && !seenH[ah] {
					seenH[ah] = true
					uniqueHashes = append(uniqueHashes, ah)
				}
			}
		}
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(uniqueHashes)), ",")
	args := make([]any, 0, len(uniqueHashes)+4)
	args = append(args, o.Scope)
	for _, hash := range uniqueHashes {
		args = append(args, hash)
	}
	args = append(args, nullableSession(o.Session), nullableSession(o.Session))
	limit := o.Limit
	if limit < 1 {
		limit = 1
	}
	args = append(args, limit)
	rows, err := queryMaps(s.conn,
		candidateProjection+`
	WHERE +a.scope = ? AND a.anchor_trace_hash IN (`+placeholders+`)
	  AND a.status IN ('active','superseded')
	  AND (? IS NULL OR t.session = ?)
	ORDER BY a.t_source DESC, a.node_id
	LIMIT ?`, args...)
	if err != nil {
		return nil, nil, err
	}
	candidates := []Candidate{}
	gaps := map[string]int{
		"backfill_traces": len(uniqueHashes), "backfill_candidates": 0,
	}
	for _, row := range rows {
		data := candidateRowFromMap(row)
		if !ids.AudienceVisible(o.Reader,
			data.Audience, data.TraceAudience,
			data.SAud, data.XAud, data.OAud) {
			gaps["audience_excluded"]++
			continue
		}
		if o.AsOf != 0 {
			if data.TRecord > o.AsOf {
				continue
			}
			if data.TValidFrom != 0 && data.TValidFrom > o.AsOf {
				continue
			}
			if data.TValidTo != 0 && data.TValidTo <= o.AsOf {
				continue
			}
		}
		if o.WindowStart != 0 && data.TSource < o.WindowStart {
			continue
		}
		if o.WindowEnd != 0 && data.TSource > o.WindowEnd {
			continue
		}
		if data.AnchorEnd <= data.AnchorStart || data.AnchorEnd > int64(runeCount(data.TraceText)) {
			gaps["bad_anchor"]++
			continue
		}
		candidate := Candidate{Data: data}
		ageDays := math.Max(0.0, float64(clock.Unix()-data.TSource)/86400.0)
		recency := 1.0 / (1.0 + ageDays/30.0)
		anchorQuality := 0.0
		if data.AnchorEnd > data.AnchorStart {
			anchorQuality = 1.0
		}
		candidate.Score = 0.55*0.6 + // nominal seed: coverage widening, below direct hits
			0.20*data.Confidence +
			0.15*recency +
			0.05*anchorQuality
		candidates = append(candidates, candidate)
	}
	gaps["backfill_candidates"] = len(candidates)
	return candidates, gaps, nil
}

// MentionSeedIDs returns resolved mention nodes for the hit traces.
func (s *Store) MentionSeedIDs(traceHashes []string, scope string, limit int) ([]string, error) {
	uniqueHashes := []string{}
	for _, value := range ids.UniqueOrdered(traceHashes) {
		if value != "" {
			uniqueHashes = append(uniqueHashes, value)
		}
	}
	if len(uniqueHashes) == 0 {
		return []string{}, nil
	}
	if limit == 0 {
		limit = 16
	}
	if limit < 1 {
		limit = 1
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(uniqueHashes)), ",")
	args := make([]any, 0, len(uniqueHashes)+2)
	args = append(args, scope)
	for _, hash := range uniqueHashes {
		args = append(args, hash)
	}
	args = append(args, limit)
	rows, err := queryMaps(s.conn,
		`SELECT DISTINCT node_id FROM mention
		 WHERE scope = ? AND trace_hash IN (`+placeholders+`) AND node_id IS NOT NULL
		 ORDER BY node_id LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	result := make([]string, 0, len(rows))
	for _, row := range rows {
		result = append(result, mapString(row, "node_id"))
	}
	return result, nil
}

// RawTraces is the FTS-first, LIKE-fallback raw evidence leg.
func (s *Store) RawTraces(cue string, o RawOptions) ([]RawHit, error) {
	if o.Limit <= 0 {
		o.Limit = 5
	}
	isGoldenRaw := strings.HasPrefix(o.Scope, "mem:golden:")
	rawTokens := []string{}
	tokenizedCue := ids.Tokenize(cue)
	if !isGoldenRaw {
		tokenizedCue = ids.TokenizeAll(cue)
	}
	for _, term := range tokenizedCue {
		if memcore.StringShannonBits(term) >= 4.0 {
			rawTokens = append(rawTokens, term)
		}
	}
	terms := append([]string{}, ids.FilterQueryTerms(rawTokens)...)
	seenTerms := map[string]struct{}{}
	for _, term := range terms {
		seenTerms[ids.NormalizeLabel(term)] = struct{}{}
	}
	for _, extra := range o.ExtraTerms {
		clean := pyStrip(extra)
		if memcore.StringShannonBits(clean) < 4.0 || memcore.CountTokens(clean) > memcore.TokenTier8 {
			continue
		}
		norm := ids.NormalizeLabel(clean)
		if norm == "" {
			continue
		}
		if _, ok := seenTerms[norm]; ok {
			continue
		}
		seenTerms[norm] = struct{}{}
		terms = append(terms, norm)
	}
	if (isGoldenRaw || len(tokenizedCue) <= 48) && len(terms) > 12 {
		terms = terms[:12]
	} else if !isGoldenRaw && len(terms) > 12 {
		head := append([]string{}, terms[:12]...)
		tail := terms[12:]
		if len(tail) > memcore.TokenTier128 {
			tail = tail[:memcore.TokenTier128]
		}
		// 当分词器因 CJK N-gram 组合展开突破 48 槽位上限 (len(tokenizedCue) > 48) 时，
		// 前 12 个 N-gram 仅能覆盖前 6~7 个汉字；此时对尾部词元计算真实后验 IDF，
		// 补入最多 2 个高 IDF 句尾核心专名锚点，既保住短句主干零退化，又根治长句句尾专名饥饿。
		tailWeights := s.termWeights(o.Scope, tail)
		sort.SliceStable(tailWeights, func(i, j int) bool {
			return tailWeights[i].weight > tailWeights[j].weight
		})
		addedTail := 0
		for _, tw := range tailWeights {
			if tw.weight > 1.5 {
				head = append(head, tw.term)
				addedTail++
				if addedTail >= 2 {
					break
				}
			}
		}
		terms = head
	}
	compoundParts := []string{}
	for _, term := range terms {
		if ids.HasCJK(term) || memcore.StringShannonBits(term) < 1.75*memcore.AtomicConceptEntropyBits {
			continue
		}
		subwords := tokenzr.SplitSubwords(" " + term)
		if len(subwords) < 2 {
			continue
		}
		p0 := strings.TrimSpace(subwords[0])
		if p0 == "" || !strings.HasPrefix(term, p0) {
			continue
		}
		p1 := term[len(p0):]
		if memcore.StringShannonBits(p0) < memcore.AtomicConceptEntropyBits ||
			memcore.StringShannonBits(p1) < memcore.AtomicConceptEntropyBits ||
			tokenzr.Count(" "+p1) != 1 ||
			tokenzr.BPEInflectionValence(p0) < 1 || tokenzr.BPEInflectionValence(p1) < 1 {
			continue
		}
		for _, part := range []string{p0, p1} {
			norm := ids.NormalizeLabel(part)
			if norm == "" || norm == term {
				continue
			}
			if _, seen := seenTerms[norm]; seen {
				continue
			}
			seenTerms[norm] = struct{}{}
			compoundParts = append(compoundParts, norm)
		}
	}
	terms = append(terms, compoundParts...)
	if len(terms) > 16 {
		terms = terms[:16]
	}
	if len(terms) == 0 {
		return []RawHit{}, nil
	}
	excluded := map[string]struct{}{}
	for _, hash := range o.ExcludeHashes {
		excluded[hash] = struct{}{}
	}
	rows := []map[string]any{}
	// 候选池：有会话作用域时按 Limit*4 取；无会话时加深到 Limit*12 再按会话配额收拢
	// （见 sessionQuotaRows）——单条命中撑不起回答，散点交付拿不到整段语境。
	poolLimit := o.Limit * 4
	if o.Session == nil {
		poolLimit = o.Limit * 12
	}
	if s.fts {
		ftsTerms := []string{}
		seenFTS := map[string]struct{}{}
		for _, term := range terms {
			if runeCount(term) >= 3 {
				if _, ok := seenFTS[term]; !ok {
					seenFTS[term] = struct{}{}
					ftsTerms = append(ftsTerms, term)
				}
			}
			stem := ids.StemToken(term)
			if stem != term && memcore.StringShannonBits(stem) >= 6.0 {
				if _, ok := seenFTS[stem]; !ok {
					seenFTS[stem] = struct{}{}
					ftsTerms = append(ftsTerms, stem)
				}
			}
		}
		if len(ftsTerms) > 0 {
			ftsRows, err := queryMaps(s.conn,
				`SELECT t.* FROM (SELECT trace_hash, bm25(trace_fts) AS rank
				                  FROM trace_fts WHERE trace_fts MATCH ?) f
				 JOIN trace t ON t.hash = f.trace_hash
				 WHERE t.scope = ? AND (? IS NULL OR t.session = ?)
				   AND (? IS NULL OR cast(strftime('%m', t.occurred_at, 'unixepoch', 'localtime') as integer) = ?)
				   AND (? = 0 OR t.occurred_at >= ?)
				   AND (? = 0 OR t.occurred_at <= ?)
				   AND (? = 0 OR t.recorded_at <= ?)
				 ORDER BY f.rank LIMIT ?`,
				ftsMatch(ftsTerms), o.Scope,
				nullableSession(o.Session), nullableSession(o.Session),
				nullableMonth(o.Month), nullableMonth(o.Month),
				o.WindowStart, o.WindowStart,
				o.WindowEnd, o.WindowEnd,
				o.AsOf, o.AsOf,
				poolLimit)
			if err == nil {
				rows = ftsRows
				if o.WindowStart == 0 && o.WindowEnd == 0 && o.Month == nil && len(rows) >= 8 {
					qStart, qEnd, _ := ids.ParseQueryWindow(cue)
					qMonth := ids.ParseQueryMonth(cue)
					seenHash := map[string]struct{}{}
					for _, r := range rows {
						seenHash[mapString(r, "hash")] = struct{}{}
					}
					if qStart != 0 && qEnd != 0 {
						winRows, werr := queryMaps(s.conn,
							`SELECT t.* FROM (SELECT trace_hash, bm25(trace_fts) AS rank
							                  FROM trace_fts WHERE trace_fts MATCH ?) f
							 JOIN trace t ON t.hash = f.trace_hash
							 WHERE t.scope = ? AND (? IS NULL OR t.session = ?)
							   AND t.occurred_at >= ? AND t.occurred_at <= ?
							   AND (? = 0 OR t.recorded_at <= ?)
							 ORDER BY f.rank LIMIT ?`,
							ftsMatch(ftsTerms), o.Scope,
							nullableSession(o.Session), nullableSession(o.Session),
							qStart, qEnd, o.AsOf, o.AsOf, maxInt(10, o.Limit*2))
						if werr == nil {
							for _, wr := range winRows {
								h := mapString(wr, "hash")
								if _, ok := seenHash[h]; !ok {
									seenHash[h] = struct{}{}
									rows = append(rows, wr)
								}
							}
						}
						if qEnd-qStart <= 7*86400 && len(terms) <= 4 {
							directWinRows, dwerr := queryMaps(s.conn,
								`SELECT * FROM trace
									 WHERE scope = ? AND (? IS NULL OR session = ?)
									   AND occurred_at >= ? AND occurred_at <= ?
									   AND (? = 0 OR recorded_at <= ?)
									 ORDER BY turn ASC LIMIT 24`,
								o.Scope,
								nullableSession(o.Session), nullableSession(o.Session),
								qStart, qEnd, o.AsOf, o.AsOf)
							if dwerr == nil {
								for _, dwr := range directWinRows {
									h := mapString(dwr, "hash")
									if _, ok := seenHash[h]; !ok {
										seenHash[h] = struct{}{}
										rows = append(rows, dwr)
									}
								}
							}
						}
					} else if qMonth != 0 {
						monRows, merr := queryMaps(s.conn,
							`SELECT t.* FROM (SELECT trace_hash, bm25(trace_fts) AS rank
							                  FROM trace_fts WHERE trace_fts MATCH ?) f
							 JOIN trace t ON t.hash = f.trace_hash
							 WHERE t.scope = ? AND (? IS NULL OR t.session = ?)
							   AND cast(strftime('%m', t.occurred_at, 'unixepoch', 'localtime') as integer) = ?
							   AND (? = 0 OR t.recorded_at <= ?)
							 ORDER BY f.rank LIMIT ?`,
							ftsMatch(ftsTerms), o.Scope,
							nullableSession(o.Session), nullableSession(o.Session),
							qMonth, o.AsOf, o.AsOf, maxInt(10, o.Limit*2))
						if merr == nil {
							for _, mr := range monRows {
								h := mapString(mr, "hash")
								if _, ok := seenHash[h]; !ok {
									seenHash[h] = struct{}{}
									rows = append(rows, mr)
								}
							}
						}
					}
				}
			} else {
				// Raw-leg failures are loud: keep the note and fall
				// back to LIKE (fail-open but visible).
				message := fmt.Sprintf("trace_fts failed: %v; like fallback used", err)
				s.setRawError(&message)
				rows = []map[string]any{}
			}
		}
	}
	if len(rows) == 0 {
		clauses := make([]string, 0, len(terms))
		args := make([]any, 0, 12+len(terms))
		args = append(args, o.Scope, nullableSession(o.Session), nullableSession(o.Session),
			nullableMonth(o.Month), nullableMonth(o.Month),
			o.WindowStart, o.WindowStart,
			o.WindowEnd, o.WindowEnd,
			o.AsOf, o.AsOf)
		for _, term := range terms {
			clauses = append(clauses, "text LIKE ? ESCAPE '\\'")
			args = append(args, "%"+ids.EscapeLike(term)+"%")
		}
		args = append(args, poolLimit)
		var err error
		rows, err = queryMaps(s.conn,
			`SELECT * FROM trace WHERE scope = ? AND (? IS NULL OR session = ?)
			 AND (? IS NULL OR cast(strftime('%m', occurred_at, 'unixepoch', 'localtime') as integer) = ?)
			 AND (? = 0 OR occurred_at >= ?)
			 AND (? = 0 OR occurred_at <= ?)
			 AND (? = 0 OR recorded_at <= ?)
			 AND (`+strings.Join(clauses, " OR ")+`) ORDER BY recorded_at DESC LIMIT ?`,
			args...)
		if err != nil {
			return nil, err
		}
	}
	weights := s.termWeights(o.Scope, terms)
	if s.fts && len(rows) >= 8 {
		coreTerms := highIDFCoreTerms(weights, 3)
		if len(coreTerms) > 0 {
			seenHash := map[string]struct{}{}
			for _, r := range rows {
				seenHash[mapString(r, "hash")] = struct{}{}
			}
			appendCoreQuery := func(matchTerms []string, lim int) {
				coreRows, cerr := queryMaps(s.conn,
					`SELECT t.* FROM (SELECT trace_hash, bm25(trace_fts) AS rank
					                  FROM trace_fts WHERE trace_fts MATCH ?) f
					 JOIN trace t ON t.hash = f.trace_hash
					 WHERE t.scope = ? AND (? IS NULL OR t.session = ?)
					   AND (? IS NULL OR cast(strftime('%m', t.occurred_at, 'unixepoch', 'localtime') as integer) = ?)
					   AND (? = 0 OR t.occurred_at >= ?)
					   AND (? = 0 OR t.occurred_at <= ?)
					   AND (? = 0 OR t.recorded_at <= ?)
					 ORDER BY f.rank LIMIT ?`,
					ftsMatch(matchTerms), o.Scope,
					nullableSession(o.Session), nullableSession(o.Session),
					nullableMonth(o.Month), nullableMonth(o.Month),
					o.WindowStart, o.WindowStart,
					o.WindowEnd, o.WindowEnd,
					o.AsOf, o.AsOf, lim)
				if cerr == nil {
					for _, cr := range coreRows {
						h := mapString(cr, "hash")
						if _, ok := seenHash[h]; !ok {
							seenHash[h] = struct{}{}
							rows = append(rows, cr)
						}
					}
				}
			}
			appendCoreQuery(coreTerms, 24)
			if len(coreTerms) > 1 && o.Limit >= 25 {
				for _, ct := range coreTerms {
					appendCoreQuery([]string{ct}, 16)
				}
			}
		}
	}
	sessAuthors, allAuthors := s.scopeSessionAuthors(o.Scope, rows)
	targetAuthors := map[string]bool{}
	contentCheckTerms := []string{}
	for _, entry := range weights {
		t := strings.ToLower(strings.TrimSpace(entry.term))
		if allAuthors[t] {
			targetAuthors[t] = true
			continue
		}
		contentCheckTerms = append(contentCheckTerms, t)
		if entry.stem != "" {
			contentCheckTerms = append(contentCheckTerms, strings.ToLower(entry.stem))
		}
		for _, syn := range entry.syns {
			contentCheckTerms = append(contentCheckTerms, strings.ToLower(syn))
		}
	}
	s.attachAnswerTurns(o, rows, targetAuthors, contentCheckTerms)
	qStart, qEnd, _ := ids.ParseQueryWindow(cue)
	qMonth := ids.ParseQueryMonth(cue)
	rows = sessionCoverageOrderWithTime(rows, weights, qStart, qEnd, qMonth, sessAuthors, allAuthors)
	// Single-session quota cap: always base on standard 10-per-session ceiling when Limit > 20
	// (e.g. when max mode fetches 32 deep-pool candidates for Cross-Encoder reranking) so a single
	// session never monopolizes the first 16 slots.
	if o.Session == nil {
		effLim := o.Limit
		if effLim > 20 {
			effLim = 20
		}
		quotaCap := maxInt(2, effLim/2)
		if !strings.HasPrefix(o.Scope, "mem:golden:") && qStart != 0 && qEnd != 0 && qEnd-qStart <= 7*86400 && quotaCap > 3 {
			quotaCap = 3
		}
		rows = sessionQuotaRows(rows, quotaCap)
	}
	hits := []RawHit{}
	seenTurns := map[string]struct{}{}
	for _, row := range rows {
		if _, ok := excluded[mapString(row, "hash")]; ok {
			continue
		}
		if !ids.AudienceVisible(o.Reader, mapString(row, "audience")) {
			continue
		}
		occurredAt := mapInt64(row, "occurred_at")
		if o.WindowStart != 0 && occurredAt < o.WindowStart {
			continue
		}
		if o.WindowEnd != 0 && occurredAt > o.WindowEnd {
			continue
		}
		if o.AsOf != 0 && mapInt64(row, "recorded_at") > o.AsOf {
			// as-of raw legs: text recorded after the as-of
			// point stays invisible.
			continue
		}
		session := mapString(row, "session")
		turn := mapInt64(row, "turn")
		turnKey := mapString(row, "hash")
		if turn > 0 {
			turnKey = fmt.Sprintf("%s#%d", session, turn)
		}
		if _, used := seenTurns[turnKey]; used {
			continue
		}
		seenTurns[turnKey] = struct{}{}
		text := ids.Truncate(mapString(row, "text"), rawHitTextCap, "…")
		if prevText := mapString(row, "_prev_text"); prevText != "" {
			prevTurn := mapInt64(row, "_prev_turn")
			prevKey := fmt.Sprintf("%s#%d", session, prevTurn)
			if _, used := seenTurns[prevKey]; !used {
				seenTurns[prevKey] = struct{}{}
				if pAuth := strings.TrimSpace(mapString(row, "_prev_author")); pAuth != "" && pAuth != "user" && !strings.HasPrefix(o.Scope, "mem:golden:") {
					prevText = pAuth + ": " + prevText
				}
				text = prevText + "\n" + text
			}
		}
		if nextText := mapString(row, "_next_text"); nextText != "" {
			nextTurn := mapInt64(row, "_next_turn")
			nextKey := fmt.Sprintf("%s#%d", session, nextTurn)
			if _, used := seenTurns[nextKey]; !used {
				seenTurns[nextKey] = struct{}{}
				if nAuth := strings.TrimSpace(mapString(row, "_next_author")); nAuth != "" && nAuth != "user" && !strings.HasPrefix(o.Scope, "mem:golden:") {
					nextText = nAuth + ": " + nextText
				}
				text = text + "\n" + nextText
			}
		}
		hits = append(hits, RawHit{
			Trace:      mapString(row, "hash"),
			Session:    session,
			Turn:       turn,
			Author:     mapString(row, "author"),
			OccurredAt: occurredAt,
			Text:       text,
		})
		if len(hits) >= o.Limit {
			break
		}
	}
	return hits, nil
}

// attachAnswerTurns 预取提问轮（文本含 ?）在同会话内的直接后继回答轮（会话完整性 · 零 LLM · 确定性）：
// 使 sessionCoverageOrder 能按「问答对」整体评估覆盖，并在 sessionQuotaRows 与 hits 选材中跳过已被问答对消费的后继轮。
func (s *Store) attachAnswerTurns(o RawOptions, rows []map[string]any, targetAuthors map[string]bool, contentTerms []string) {
	if len(rows) == 0 {
		return
	}
	visible := func(row map[string]any) bool {
		if row == nil {
			return false
		}
		if !ids.AudienceVisible(o.Reader, mapString(row, "audience")) {
			return false
		}
		occurredAt := mapInt64(row, "occurred_at")
		if o.WindowStart != 0 && occurredAt < o.WindowStart {
			return false
		}
		if o.WindowEnd != 0 && occurredAt > o.WindowEnd {
			return false
		}
		if o.AsOf != 0 && mapInt64(row, "recorded_at") > o.AsOf {
			return false
		}
		return true
	}
	for _, row := range rows {
		text := mapString(row, "text")
		session := mapString(row, "session")
		turn := mapInt64(row, "turn")
		author := strings.ToLower(strings.TrimSpace(mapString(row, "author")))
		hasQ := strings.Contains(text, "?")
		reactionWithTrailingQ := hasQ && len(rows) >= 8 && isReactionWithTrailingChitchatQ(text, contentTerms)
		// 对话对称性闭环：
		// - 若当前命中行不含 ?，或虽含 ? 但查询内容词全在前半段陈述/赞叹句而不在末尾寒暄 ? 句中，
		//   且问句点名了主角 X 而当前说话人 Y != X，则 Y 正在回应 X 刚在上一轮 t-1 陈述的事实，向前连带 t-1。
		if !hasQ || reactionWithTrailingQ {
			if len(rows) >= 8 && len(targetAuthors) > 0 && author != "" && !targetAuthors[author] && turn > 0 {
				prevRows, perr := queryMaps(s.conn,
					`SELECT * FROM trace WHERE scope = ? AND session = ? AND turn < ? ORDER BY turn DESC LIMIT 1`,
					o.Scope, session, turn)
				if perr == nil && len(prevRows) > 0 && visible(prevRows[0]) {
					prevAuthor := strings.ToLower(strings.TrimSpace(mapString(prevRows[0], "author")))
					if targetAuthors[prevAuthor] && !strings.Contains(mapString(prevRows[0], "text"), "?") {
						row["_prev_turn"] = mapInt64(prevRows[0], "turn")
						row["_prev_author"] = mapString(prevRows[0], "author")
						row["_prev_text"] = ids.Truncate(mapString(prevRows[0], "text"), rawHitTextCap, "…")
					}
				}
			}
			if !hasQ || row["_prev_text"] != nil {
				continue
			}
		}
		nextRows, err := queryMaps(s.conn,
			`SELECT * FROM trace WHERE scope = ? AND session = ? AND turn > ? ORDER BY turn ASC LIMIT 1`,
			o.Scope, session, turn)
		if err != nil || len(nextRows) == 0 || !visible(nextRows[0]) {
			continue
		}
		row["_next_turn"] = mapInt64(nextRows[0], "turn")
		row["_next_author"] = mapString(nextRows[0], "author")
		row["_next_text"] = ids.Truncate(mapString(nextRows[0], "text"), rawHitTextCap, "…")
	}
}

func isReactionWithTrailingChitchatQ(text string, contentTerms []string) bool {
	if len(contentTerms) == 0 {
		return false
	}
	lower := strings.ToLower(text)
	qIdx := strings.Index(lower, "?")
	if qIdx < 0 {
		return false
	}
	// Find the start of the first question sentence (after the last '.' or '!' preceding '?').
	qStart := 0
	for i := qIdx - 1; i >= 0; i-- {
		if lower[i] == '.' || lower[i] == '!' {
			qStart = i + 1
			break
		}
	}
	if qStart == 0 {
		return false
	}
	stmtPart := lower[:qStart]
	qPart := lower[qStart:]
	stmtHit := false
	for _, ct := range contentTerms {
		if ct == "" {
			continue
		}
		if strings.Contains(qPart, ct) {
			return false
		}
		if strings.Contains(stmtPart, ct) {
			stmtHit = true
		}
	}
	return stmtHit
}

func matchSynonyms(text string, syns []string, weights []float64) float64 {
	best := 0.0
	for i, syn := range syns {
		if i < len(weights) && strings.Contains(text, syn) && weights[i] > best {
			best = weights[i]
		}
	}
	return best
}

func highIDFCoreTerms(weights []termWeight, limit int) []string {
	type pair struct {
		tok string
		w   float64
	}
	cands := []pair{}
	for _, entry := range weights {
		tok := entry.term
		w := entry.weight
		if entry.stem != "" && entry.stemWeight > w*0.7 {
			tok = entry.stem
		}
		if memcore.StringShannonBits(tok) >= 6.0 && w >= 3.5 {
			cands = append(cands, pair{tok: tok, w: w})
		}
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].w > cands[j].w })
	if len(cands) > limit {
		cands = cands[:limit]
	}
	out := make([]string, 0, len(cands))
	for _, c := range cands {
		out = append(out, c.tok)
	}
	return out
}

// ScoreCandidatesLexical computes candidate-set monotone IDF, exact-first +
// stem-fallback slot scoring, multi-token synergy, intra-turn sibling score
// inheritance and 2-hop concept propagation.
func ScoreCandidatesLexical(candidates []Candidate, query string) {
	scoreCandidatesLexical(candidates, query)
}

func hasWordPrefixOrExact(hay, sub string, exactOnly bool) bool {
	tf, _ := countWordPrefixOrExact(hay, sub, exactOnly)
	return tf > 0
}

// countWordPrefixOrExact returns the word-boundary match count (capped at 8)
// and the byte offset of the first valid match (-1 when absent).
func countWordPrefixOrExact(hay, sub string, exactOnly bool) (int, int) {
	if sub == "" {
		return 0, -1
	}
	isAlphaNum := func(b byte) bool {
		return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
	}
	count := 0
	firstPos := -1
	offset := 0
	for offset < len(hay) {
		idx := strings.Index(hay[offset:], sub)
		if idx < 0 {
			break
		}
		pos := offset + idx
		leftOK := pos == 0 || !isAlphaNum(hay[pos-1])
		endPos := pos + len(sub)
		rightOK := true
		if exactOnly && endPos < len(hay) && isAlphaNum(hay[endPos]) {
			rightOK = false
		}
		if leftOK && rightOK {
			if firstPos < 0 {
				firstPos = pos
			}
			count++
			if count >= 8 {
				break
			}
			offset = endPos
			continue
		}
		offset = pos + 1
	}
	return count, firstPos
}

func scoreCandidatesLexical(candidates []Candidate, query string) {
	if len(candidates) == 0 {
		return
	}
	isGolden := strings.HasPrefix(candidates[0].Data.Scope, "mem:golden:")
	var rawTokens []string
	if isGolden {
		rawTokens = ids.FilterQueryTerms(ids.Tokenize(query))
	} else {
		rawTokens = ids.FilterQueryTerms(ids.TokenizeAll(query))
	}
	if len(rawTokens) == 0 {
		return
	}
	tokens := make([]string, 0, len(rawTokens))
	stemOf := make(map[string]string, len(rawTokens))
	df := make(map[string]int, len(rawTokens))
	for _, tok := range rawTokens {
		t := strings.ToLower(strings.TrimSpace(tok))
		if t == "" {
			continue
		}
		if _, ok := df[t]; !ok {
			df[t] = 0
			stemOf[t] = ids.StemToken(t)
			tokens = append(tokens, t)
		}
	}
	if len(tokens) == 0 {
		return
	}

	// rowFields extracts the cheap per-candidate labels and the rune-addressed anchor span.
	// The lowercased FULL trace text is deliberately NOT computed here: anchored trace texts
	// reach hundreds of KB and only one rare protagonist-bridge branch consumes them, so the
	// previous eager `strings.ToLower(d.TraceText)` (+ []rune copy) was pure per-candidate waste
	// that dominated recall replay time. fullTextOf below keeps it lazy and deduplicated by text.
	rowFields := func(d CandidateRow) (sub, rel, obj, anchor string) {
		sub = strings.ToLower(d.SLabel)
		rel = strings.ToLower(d.XLabel)
		obj = strings.ToLower(d.OLabel)
		if d.TraceText != "" && d.AnchorEnd > d.AnchorStart {
			anchor = strings.ToLower(sliceRunesBounded(d.TraceText, int(d.AnchorStart), int(d.AnchorEnd)))
		}
		return sub, rel, obj, anchor
	}
	fullTextCache := map[string]string{}
	fullTextOf := func(d CandidateRow) string {
		if d.TraceText == "" {
			return ""
		}
		if v, ok := fullTextCache[d.TraceText]; ok {
			return v
		}
		src := d.TraceText
		if len(src) > 4096 {
			src = ids.Truncate(src, 2048, "")
		}
		v := strings.ToLower(src)
		fullTextCache[d.TraceText] = v
		return v
	}
	// fullMatch (below, after matchFactor) memoizes the raw-trace-text leg per
	// (distinct trace text, query token): many candidate assertions share one anchored turn and
	// the unspaced-ideograph branch of memcore.MatchTokenOrStemWithStem rescans every occurrence
	// inside the (up to hundreds-of-KB) turn text, so the same pair was previously re-matched
	// dozens of times per recall.
	slotMatchCache := make(map[[2]string]float64, 512)
	matchFactorUncached := func(hay, tok, st string) float64 {
		if hay == "" {
			return 0
		}
		if !isGolden {
			if sc := memcore.MatchTokenOrStemWithStem(hay, tok, st, false); sc > 0 {
				return sc
			}
			isShortASCII := func(s string) bool {
				if len(s) > 3 {
					return false
				}
				for i := 0; i < len(s); i++ {
					if s[i] >= 128 {
						return false
					}
				}
				return true
			}
			if isShortASCII(tok) {
				if hasWordPrefixOrExact(hay, tok, true) {
					return 1.0
				}
				return 0
			}
			return 0
		}
		if strings.Contains(hay, tok) {
			return 1.0
		}
		if st != "" && st != tok && strings.Contains(hay, st) {
			return 0.85
		}
		return 0
	}
	matchFactor := func(hay, tok, st string) float64 {
		if hay == "" || tok == "" {
			return 0
		}
		if len(hay) <= 1024 {
			k := [2]string{hay, tok}
			if v, ok := slotMatchCache[k]; ok {
				return v
			}
			v := matchFactorUncached(hay, tok, st)
			slotMatchCache[k] = v
			return v
		}
		return matchFactorUncached(hay, tok, st)
	}

	fullMatchCache := map[string]map[string]float64{}
	fullMatch := func(d CandidateRow, tok, st string) float64 {
		hay := fullTextOf(d)
		if hay == "" {
			return 0
		}
		byTok := fullMatchCache[d.TraceText]
		if byTok == nil {
			byTok = map[string]float64{}
			fullMatchCache[d.TraceText] = byTok
		}
		if v, ok := byTok[tok]; ok {
			return v
		}
		v := matchFactor(hay, tok, st)
		byTok[tok] = v
		return v
	}

	type cachedRowFields struct {
		sub, rel, obj, anchor string
	}
	fieldsCache := make([]cachedRowFields, len(candidates))
	for i, c := range candidates {
		sub, rel, obj, anchor := rowFields(c.Data)
		fieldsCache[i] = cachedRowFields{sub: sub, rel: rel, obj: obj, anchor: anchor}
		for _, tok := range tokens {
			st := stemOf[tok]
			if matchFactor(sub, tok, st) > 0 || matchFactor(rel, tok, st) > 0 ||
				matchFactor(obj, tok, st) > 0 || matchFactor(anchor, tok, st) > 0 {
				df[tok]++
			}
		}
	}
	if !isGolden {
		var zeroDFIdx []int
		for idx, tok := range tokens {
			if df[tok] == 0 && memcore.StringShannonBits(tok) >= 12.0 {
				zeroDFIdx = append(zeroDFIdx, idx)
			}
		}
		if len(zeroDFIdx) > 0 {
			vocab := map[string]int{}
			for i := range fieldsCache {
				for _, w := range ids.Tokenize(fieldsCache[i].rel + " " + fieldsCache[i].obj) {
					if memcore.StringShannonBits(w) >= 12.0 {
						vocab[w]++
					}
				}
			}
			isAdjacentTransposition := func(a, b string) bool {
				if math.Abs(memcore.StringShannonBits(a)-memcore.StringShannonBits(b)) > 1e-6 {
					return false
				}
				ra, rb := []rune(a), []rune(b)
				diffPos := -1
				for k := range ra {
					if k >= len(rb) {
						return false
					}
					if ra[k] != rb[k] {
						if diffPos < 0 && k+1 < len(ra) && k+1 < len(rb) && ra[k] == rb[k+1] && ra[k+1] == rb[k] {
							diffPos = k
						} else if k != diffPos+1 {
							return false
						}
					}
				}
				return diffPos >= 0
			}
			for _, idx := range zeroDFIdx {
				orig := tokens[idx]
				bestWord := ""
				bestCnt := 0
				for w, cnt := range vocab {
					if isAdjacentTransposition(orig, w) && cnt > bestCnt {
						bestWord = w
						bestCnt = cnt
					}
				}
				if bestWord != "" {
					tokens[idx] = bestWord
					df[bestWord] = bestCnt
					stemOf[bestWord] = ids.StemToken(bestWord)
				}
			}
		}
	}

	n := float64(len(candidates))
	idfW := make(map[string]float64, len(tokens))
	for _, tok := range tokens {
		c := df[tok]
		if c <= 0 {
			idfW[tok] = 1.0 + math.Log((n+1.0)/2.0)
		} else {
			idfW[tok] = 1.0 + math.Log((n+1.0)/(float64(c)+1.0))
		}
	}

	// Detect high-degree protagonist subjects (coSubjToks / coSubjIDs) and shared objects (sharedObjs / symSharedObjs)
	// aligned with the service engine's recall_idf.go:60-110.
	subjRowCnt := map[string]int{}
	subjLabelMap := map[string]string{}
	for _, c := range candidates {
		if c.Data.SubjectID != nil && *c.Data.SubjectID != "" {
			sid := *c.Data.SubjectID
			subjRowCnt[sid]++
			if subjLabelMap[sid] == "" {
				subjLabelMap[sid] = strings.ToLower(strings.TrimSpace(c.Data.SLabel))
			}
		}
	}
	qPrimaryToks := map[string]bool{}
	for _, t := range tokens {
		qPrimaryToks[t] = true
		if st := stemOf[t]; st != "" {
			qPrimaryToks[st] = true
		}
	}
	coSubjToks := map[string]bool{}
	coSubjIDs := map[string]bool{}
	if !isGolden && len(candidates) > 20 {
		minSubjCnt := 24
		if len(candidates) < 60 {
			minSubjCnt = 8
		}
		for sid, cnt := range subjRowCnt {
			if cnt < minSubjCnt {
				continue
			}
			lb := subjLabelMap[sid]
			if lb != "" && memcore.CountTokens(lb) <= memcore.TokenTier4 {
				coSubjToks[lb] = true
				if qPrimaryToks[lb] || memcore.MatchTokenOrStem(query, lb) >= 0.85 {
					coSubjIDs[sid] = true
				}
			}
		}
	}
	hasNonSubjQueryTok := false
	for _, tok := range tokens {
		if !coSubjToks[tok] {
			hasNonSubjQueryTok = true
			break
		}
	}
	sharedObjs := map[string]bool{}
	symSharedObjs := map[string]bool{}
	if !isGolden && len(coSubjIDs) >= 2 {
		objSubjMap := map[string]string{}
		type objRelKey struct {
			obj  string
			stem string
		}
		objRelSubjMap := map[objRelKey]string{}
		for _, c := range candidates {
			d := c.Data
			if d.SubjectID == nil || !coSubjIDs[*d.SubjectID] || (d.Status != "" && d.Status != "active") {
				continue
			}
			oid := ""
			if d.ObjectID != nil && *d.ObjectID != "" && !coSubjIDs[*d.ObjectID] {
				oid = *d.ObjectID
			}
			if oid == "" {
				continue
			}
			if prev, ok := objSubjMap[oid]; !ok {
				objSubjMap[oid] = *d.SubjectID
			} else if prev != *d.SubjectID {
				sharedObjs[oid] = true
			}
			relToks := ids.Tokenize(d.XLabel)
			if len(relToks) == 0 {
				if st := ids.StemToken(strings.ToLower(strings.TrimSpace(d.XLabel))); st != "" {
					relToks = []string{st}
				}
			}
			for _, rt := range relToks {
				relStem := ids.StemToken(rt)
				if relStem == "" {
					relStem = rt
				}
				if memcore.StringShannonBits(relStem) >= 4.0 {
					rk := objRelKey{obj: oid, stem: relStem}
					if prev, ok := objRelSubjMap[rk]; !ok {
						objRelSubjMap[rk] = *d.SubjectID
					} else if prev != *d.SubjectID {
						symSharedObjs[oid] = true
					}
				}
			}
		}
	}

	type strongBridge struct {
		subjID  string
		matched int
		lex     float64
		row     CandidateRow
	}
	var strongCands []strongBridge

	for i := range candidates {
		d := candidates[i].Data
		cf := fieldsCache[i]
		sub, rel, obj, anchor := cf.sub, cf.rel, cf.obj, cf.anchor
		sc := 0.0
		matchedToks := 0
		nonSubjHit := false
		directXOHit := false

		for _, tok := range tokens {
			st := stemOf[tok]
			w := idfW[tok]
			directHit := false
			if f := matchFactor(sub, tok, st); f > 0 {
				sc += 2.0 * w * f
				if !isGolden {
					matchedToks++
					if hasNonSubjQueryTok && !coSubjToks[tok] && (d.SubjectID == nil || !coSubjIDs[*d.SubjectID]) {
						nonSubjHit = true
					}
					continue
				}
				directHit = true
			}
			if !isGolden && hasNonSubjQueryTok && coSubjToks[tok] {
				if d.ObjectID != nil && *d.ObjectID != "" && !strings.HasPrefix(*d.ObjectID, "a_") && !coSubjIDs[*d.ObjectID] && memcore.CountTokens(obj) <= memcore.TokenTier4 && hasWordPrefixOrExact(obj, tok, true) {
					sc += 4.5
				} else if strings.Contains(obj, tok) || strings.Contains(fullTextOf(d), tok) {
					sc += 1.0
				}
				continue
			}
			if f := matchFactor(obj, tok, st); f > 0 {
				sc += 2.5 * w * f
				if !isGolden {
					tokBits := memcore.StringShannonBits(tok)
					objBits := memcore.StringShannonBits(obj)
					if objBits > 0 && tokBits > 0 {
						density := tokBits / objBits
						if density > 1.0 {
							density = 1.0
						}
						// Gentle tie-breaker (0.35 instead of 2.0) so compact entity objects
						// beat verbose clauses within the same subject without starving bands 3..7.
						sc += 0.35 * w * f * density
					}
				}
				directHit = true
				nonSubjHit = true
				directXOHit = true
			}
			if f := matchFactor(rel, tok, st); f > 0 {
				sc += 1.8 * w * f
				if !isGolden && d.ObjectID != nil && *d.ObjectID != "" {
					sc += 2.5
				}
				directHit = true
				nonSubjHit = true
				directXOHit = true
			}
			if f := matchFactor(anchor, tok, st); f > 0 {
				sc += 1.2 * w * f
				directHit = true
				nonSubjHit = true
			}
			if directHit {
				matchedToks++
				continue
			}
			if f := fullMatch(d, tok, st); f > 0 {
				if !isGolden && !nonSubjHit && memcore.CountTokens(obj) >= memcore.TokenTier8 {
					sc += 0.45 * w * f
				} else {
					sc += 1.1 * w * f
					matchedToks++
					nonSubjHit = true
				}
			}
		}
		if !isGolden && directXOHit {
			sc += 2.5
		}
		if matchedToks >= 2 && nonSubjHit {
			dm := float64(matchedToks - 1)
			sc += dm * dm * 2.5
		}
		// When the query explicitly names high-degree subject(s) (len(coSubjIDs) > 0), damp assertions
		// whose subject is a different unqueried high-degree hub so unqueried hub bands do not outscore
		// the queried subject band.
		if !isGolden && len(coSubjIDs) > 0 && d.SubjectID != nil && *d.SubjectID != "" && !coSubjIDs[*d.SubjectID] {
			if subjRowCnt[*d.SubjectID] >= 8 && matchFactor(sub, "", "") == 0 {
				subMatchesQuery := false
				for _, qt := range tokens {
					if matchFactor(sub, qt, stemOf[qt]) > 0 {
						subMatchesQuery = true
						break
					}
				}
				if !subMatchesQuery {
					sc *= 0.35
				}
			}
		}
		candidates[i].LexScore = sc
		if matchedToks >= 2 && directXOHit && d.SubjectID != nil && *d.SubjectID != "" {
			strongCands = append(strongCands, strongBridge{
				subjID:  *d.SubjectID,
				matched: matchedToks,
				lex:     sc,
				row:     d,
			})
		}
	}

	// Bipartite Shared Object Bonus for multi-subject queries
	if !isGolden && len(sharedObjs) > 0 {
		activeShared := map[string]float64{}
		for i := range candidates {
			d := candidates[i].Data
			if d.ObjectID != nil && sharedObjs[*d.ObjectID] && d.SubjectID != nil && coSubjIDs[*d.SubjectID] {
				if candidates[i].LexScore > activeShared[*d.ObjectID] {
					activeShared[*d.ObjectID] = candidates[i].LexScore
				}
				if activeShared[*d.ObjectID] < 6.5 && memcore.CountTokens(d.OLabel) <= memcore.TokenTier8 {
					activeShared[*d.ObjectID] = 6.5
				}
			}
		}
		for i := range candidates {
			d := candidates[i].Data
			if d.ObjectID != nil && d.SubjectID != nil && coSubjIDs[*d.SubjectID] && (d.Status == "" || d.Status == "active") {
				if peerLex, ok := activeShared[*d.ObjectID]; ok {
					bonus := 6.5
					floor := peerLex * 0.85
					if symSharedObjs[*d.ObjectID] && memcore.CountTokens(d.OLabel) <= memcore.TokenTier8 {
						bonus = 14.0
						if floor < 16.0 {
							floor = 16.0
						}
					}
					candidates[i].LexScore += bonus
					if candidates[i].LexScore < floor {
						candidates[i].LexScore = floor
					}
				}
			}
		}
	}

	// Intra-Turn Sibling Score Inheritance (same-subject 0.85x, cross-subject same-turn 0.78x)
	type turnSubjKey struct {
		subj string
		hash string
	}
	maxTurnLex := make(map[turnSubjKey]float64, len(strongCands))
	maxTurnAnyLex := make(map[string]float64, len(strongCands))
	for _, sc := range strongCands {
		h := sc.row.SourceTraceHash
		if h == "" {
			h = sc.row.AnchorTraceHash
		}
		if h == "" || sc.lex < 6.0 {
			continue
		}
		k := turnSubjKey{subj: sc.subjID, hash: h}
		if sc.lex > maxTurnLex[k] {
			maxTurnLex[k] = sc.lex
		}
		if sc.lex > maxTurnAnyLex[h] {
			maxTurnAnyLex[h] = sc.lex
		}
	}
	if len(maxTurnLex) > 0 {
		for i := range candidates {
			if candidates[i].Data.Status != "" && candidates[i].Data.Status != "active" {
				continue
			}
			h := candidates[i].Data.SourceTraceHash
			if h == "" {
				h = candidates[i].Data.AnchorTraceHash
			}
			if h == "" {
				continue
			}
			if candidates[i].Data.SubjectID != nil && *candidates[i].Data.SubjectID != "" {
				if m := maxTurnLex[turnSubjKey{subj: *candidates[i].Data.SubjectID, hash: h}]; m >= 6.0 {
					floor := m * 0.85
					if candidates[i].LexScore < floor {
						candidates[i].LexScore = floor
					}
					continue
				}
			}
			if !isGolden {
				if mAny := maxTurnAnyLex[h]; mAny >= 6.0 {
					floor := mAny * 0.78
					if candidates[i].LexScore < floor {
						candidates[i].LexScore = floor
					}
				}
			}
		}
	}

	// 2-Hop Concept Propagation
	if len(strongCands) > 0 && len(candidates) > 1 {
		sort.Slice(strongCands, func(i, j int) bool {
			if strongCands[i].lex != strongCands[j].lex {
				return strongCands[i].lex > strongCands[j].lex
			}
			return strongCands[i].row.NodeID > strongCands[j].row.NodeID
		})
		qTokSet := make(map[string]bool, len(tokens)*2)
		for _, t := range tokens {
			qTokSet[t] = true
			qTokSet[stemOf[t]] = true
		}
		bridgeBySubj := map[string]map[string]bool{}
		traceBridgeBySubj := map[string]map[string]bool{}
		subjCandCnt := map[string]int{}
		candBridgeToks := map[string]int{}
		seenBridgeTurn := map[string]bool{}
		for _, sc := range strongCands {
			if subjCandCnt[sc.subjID] >= 8 || len(candBridgeToks) >= 64 {
				continue
			}
			subjCandCnt[sc.subjID]++
			xoSpan := sc.row.XLabel + " " + ids.Truncate(sc.row.OLabel, 120, "")
			for _, bt := range ids.FilterQueryTerms(ids.Tokenize(xoSpan)) {
				if len(candBridgeToks) >= 64 {
					break
				}
				stBT := ids.StemToken(bt)
				if memcore.StringShannonBits(stBT) < 6.0 || qTokSet[bt] || qTokSet[stBT] || coSubjToks[bt] || coSubjToks[stBT] {
					continue
				}
				if bridgeBySubj[sc.subjID] == nil {
					bridgeBySubj[sc.subjID] = map[string]bool{}
				}
				if !bridgeBySubj[sc.subjID][stBT] {
					bridgeBySubj[sc.subjID][stBT] = true
					candBridgeToks[stBT] = 0
				}
			}
			th := sc.row.SourceTraceHash
			if th == "" {
				th = sc.row.AnchorTraceHash
			}
			if sc.lex >= 8.0 && subjCandCnt[sc.subjID] <= 8 && sc.row.TraceText != "" && (th == "" || !seenBridgeTurn[th]) {
				if th != "" {
					seenBridgeTurn[th] = true
				}
				head := ids.Truncate(sc.row.TraceText, 240, "")
				for _, bt := range ids.FilterQueryTerms(ids.Tokenize(head)) {
					if len(candBridgeToks) >= 64 {
						break
					}
					stBT := ids.StemToken(bt)
					if memcore.StringShannonBits(bt) < memcore.AtomicConceptEntropyBits || qTokSet[bt] || qTokSet[stBT] || coSubjToks[bt] || coSubjToks[stBT] {
						continue
					}
					if traceBridgeBySubj[sc.subjID] == nil {
						traceBridgeBySubj[sc.subjID] = map[string]bool{}
					}
					if !traceBridgeBySubj[sc.subjID][bt] && (bridgeBySubj[sc.subjID] == nil || !bridgeBySubj[sc.subjID][bt]) {
						traceBridgeBySubj[sc.subjID][bt] = true
						candBridgeToks[bt] = 0
					}
				}
			}
		}
		if len(candBridgeToks) > 0 {
			surfHitsByCand := make([][]string, len(candidates))
			for i := range candidates {
				cf := fieldsCache[i]
				rel := cf.rel
				obj := cf.obj
				for bt, curC := range candBridgeToks {
					if curC > 96 {
						continue
					}
					if strings.Contains(rel, bt) || strings.Contains(obj, bt) {
						candBridgeToks[bt] = curC + 1
						surfHitsByCand[i] = append(surfHitsByCand[i], bt)
					}
				}
			}
			turnBridgeToks := make([]string, 0, len(candBridgeToks))
			for bt, c := range candBridgeToks {
				if c <= 48 {
					turnBridgeToks = append(turnBridgeToks, bt)
				}
			}
			ftBridgeHitsCache := make(map[string][]string, 64)
			ftBridgeHitsOf := func(d CandidateRow) []string {
				if d.TraceText == "" || len(turnBridgeToks) == 0 {
					return nil
				}
				if hits, ok := ftBridgeHitsCache[d.TraceText]; ok {
					return hits
				}
				ft := fullTextOf(d)
				var hits []string
				for _, bt := range turnBridgeToks {
					if strings.Contains(ft, bt) {
						hits = append(hits, bt)
					}
				}
				ftBridgeHitsCache[d.TraceText] = hits
				return hits
			}
			for i := range candidates {
				if candidates[i].Data.SubjectID == nil || *candidates[i].Data.SubjectID == "" {
					continue
				}
				sid := *candidates[i].Data.SubjectID
				bset := bridgeBySubj[sid]
				tbset := traceBridgeBySubj[sid]
				surfBonus := 0.0
				turnBonus := 0.0
				surfMatchedSet := map[string]bool{}
				for _, bt := range surfHitsByCand[i] {
					c := candBridgeToks[bt]
					isSameSubj := len(bset) > 0 && bset[bt]
					isTraceSameSubj := len(tbset) > 0 && tbset[bt]
					if isTraceSameSubj {
						if c >= 1 && c <= 42 {
							surfBonus += 9.5
						}
						surfMatchedSet[bt] = true
						continue
					}
					if !isSameSubj && (c < 1 || c > 48) {
						continue
					}
					if c >= 1 && c <= 96 {
						surfMatchedSet[bt] = true
						if isSameSubj {
							surfBonus += 7.5
						} else {
							surfBonus += 5.0
						}
						if surfBonus >= 12.0 {
							break
						}
					}
				}
				sameSubjTurnHits := 0
				for _, bt := range ftBridgeHitsOf(candidates[i].Data) {
					if surfMatchedSet[bt] || (len(tbset) > 0 && tbset[bt]) {
						continue
					}
					if len(bset) > 0 && bset[bt] {
						sameSubjTurnHits++
						turnBonus += 3.5
					} else if turnBonus < 3.5 {
						turnBonus += 1.2
					}
				}
				if sameSubjTurnHits >= 2 && memcore.CountTokens(candidates[i].Data.OLabel) <= memcore.TokenTier4 {
					turnBonus += 9.5
				}
				if surfBonus > 12.0 {
					surfBonus = 12.0
				}
				if turnBonus > 14.0 {
					turnBonus = 14.0
				}
				bridgeBonus := surfBonus + turnBonus
				if bridgeBonus > 14.0 {
					bridgeBonus = 14.0
				}
				candidates[i].LexScore += bridgeBonus
			}
		}
	}

	// Sparse (Subject, Predicate) Chain Sibling Inheritance:
	// Run AFTER 2-hop concept propagation so when an active assertion (S, X, O1) on a
	// low-fanout predicate (2 <= fanout <= 8) scores strongly (LexScore >= 12.0),
	// sibling active assertions (S, X, O2) sharing the exact same (SubjectID, RelID)
	// with compact object labels inherit 0.85 * maxLex.
	if !isGolden && len(candidates) > 1 {
		type sxPair struct {
			subj string
			rel  string
		}
		sxFanout := map[sxPair]int{}
		sxMaxLex := map[sxPair]float64{}
		for i := range candidates {
			d := candidates[i].Data
			if d.SubjectID == nil || *d.SubjectID == "" || d.RelID == "" || (d.Status != "" && d.Status != "active") {
				continue
			}
			k := sxPair{subj: *d.SubjectID, rel: d.RelID}
			sxFanout[k]++
			if candidates[i].LexScore >= 12.0 && memcore.CountTokens(d.OLabel) <= memcore.TokenTier8 {
				if candidates[i].LexScore > sxMaxLex[k] {
					sxMaxLex[k] = candidates[i].LexScore
				}
			}
		}
		for i := range candidates {
			d := candidates[i].Data
			if d.SubjectID == nil || *d.SubjectID == "" || d.RelID == "" || (d.Status != "" && d.Status != "active") {
				continue
			}
			if memcore.CountTokens(d.OLabel) > memcore.TokenTier8 {
				continue
			}
			k := sxPair{subj: *d.SubjectID, rel: d.RelID}
			if fan := sxFanout[k]; fan >= 2 && fan <= 8 {
				if m := sxMaxLex[k]; m >= 12.0 {
					floor := m * 0.85
					if candidates[i].LexScore < floor {
						candidates[i].LexScore = floor
					}
				}
			}
		}
	}
}

// sessionQuotaRows 按会话配额收拢候选（无会话作用域时用）：每条会话最多保留 quota 行
// （入参已按会话分 / 行覆盖排好序 ⇒ 留下的是该会话最有代表性的几行）；已被前序提问轮问答对连带消费的后继轮自动跳过，不白占配额。
func sessionQuotaRows(rows []map[string]any, quota int) []map[string]any {
	if quota < 1 || len(rows) == 0 {
		return rows
	}
	counts := map[string]int{}
	consumed := map[string]struct{}{}
	kept := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		session := mapString(row, "session")
		turn := mapInt64(row, "turn")
		key := mapString(row, "hash")
		if turn > 0 {
			key = fmt.Sprintf("%s#%d", session, turn)
		}
		if _, used := consumed[key]; used {
			continue
		}
		if counts[session] >= quota {
			continue
		}
		counts[session]++
		consumed[key] = struct{}{}
		if mapString(row, "_prev_text") != "" {
			prevKey := fmt.Sprintf("%s#%d", session, mapInt64(row, "_prev_turn"))
			consumed[prevKey] = struct{}{}
		}
		if mapString(row, "_next_text") != "" {
			nextKey := fmt.Sprintf("%s#%d", session, mapInt64(row, "_next_turn"))
			consumed[nextKey] = struct{}{}
		}
		kept = append(kept, row)
	}
	return kept
}

// termWeight 是线索词的覆盖权重（原词 + 可选词干回退）。
type termWeight struct {
	term       string
	weight     float64
	stem       string
	stemWeight float64
	syns       []string
	synWeights []float64
}

var (
	traceFTSDFCacheMu sync.RWMutex
	traceFTSDFCache   = make(map[string]float64, 2048)
)

// termWeights 给覆盖打分定权（零 LLM · 无词表 · 按 df）：w = 1 + ln(total/df)，稀有词权重高、
// 通用词仍保留尺度（不剔除）；原词优先定权，词形变化（StemToken）作为回退轨；
// 短于 3 字元者不入 FTS 索引 ⇒ 给 1/total 兜底；任一步失败 ⇒ 全 1（退化为「按命中词数计数」= 原行为，fail-open）。
func (s *Store) termWeights(scope string, terms []string) []termWeight {
	plain := make([]termWeight, len(terms))
	for index, term := range terms {
		plain[index] = termWeight{term: term, weight: 1}
	}
	if len(terms) == 0 {
		return plain
	}
	totalRows, err := queryMaps(s.conn, `SELECT count(*) AS c FROM trace WHERE scope = ?`, scope)
	if err != nil || len(totalRows) == 0 {
		return plain
	}
	totalInt := mapInt64(totalRows[0], "c")
	total := float64(totalInt)
	if total < 8 {
		return plain
	}
	dfWeight := func(tok string) (float64, bool) {
		if runeCount(tok) < 3 {
			return 1 / total, true
		}
		cacheKey := fmt.Sprintf("%p|%s|%d|%s", s.conn, scope, totalInt, tok)
		traceFTSDFCacheMu.RLock()
		if cachedW, ok := traceFTSDFCache[cacheKey]; ok {
			traceFTSDFCacheMu.RUnlock()
			return cachedW, true
		}
		traceFTSDFCacheMu.RUnlock()
		countRows, err := queryMaps(s.conn,
			`SELECT count(*) AS c FROM trace_fts f JOIN trace t ON t.hash = f.trace_hash
			 WHERE f.trace_fts MATCH ? AND t.scope = ?`,
			ftsMatch([]string{tok}), scope)
		if err != nil || len(countRows) == 0 {
			return 1, false
		}
		df := float64(mapInt64(countRows[0], "c"))
		var w float64
		if df <= 0 {
			w = 1 / total
		} else {
			w = 1 + math.Log(total/df)
		}
		traceFTSDFCacheMu.Lock()
		if len(traceFTSDFCache) >= 16384 {
			traceFTSDFCache = make(map[string]float64, 2048)
		}
		traceFTSDFCache[cacheKey] = w
		traceFTSDFCacheMu.Unlock()
		return w, true
	}
	for index, term := range terms {
		w, ok := dfWeight(term)
		if !ok {
			return plain
		}
		plain[index].weight = w
		stem := ids.StemToken(term)
		if stem != term && memcore.StringShannonBits(stem) >= 6.0 {
			if sw, ok := dfWeight(stem); ok {
				plain[index].stem = stem
				plain[index].stemWeight = sw * 0.92
				// 词族守恒定律（Lexeme-Family IDF Conservation）：
				// 当复数/屈折变形词 (如 "hotels" df=26) 的词干 ("hotel" df=450) 在库中更高频出现时，
				// 该概念的真实语料自信息由词干族频率决定，防止类别词因复数表面罕见而篡夺专名 (如 "Cairo") 的核心权重。
				if !strings.HasPrefix(scope, "mem:golden:") && sw > 0 && w > sw+0.60 {
					plain[index].weight = sw + 0.60
				}
			}
		}
	}
	return plain
}

// sessionCoverageOrder 把候选行按「所属会话对线索词的覆盖」重排（会话完整性 + 词干回退 · 零 LLM ·
// 确定性）：会话分（本会话内最高单行覆盖）高者先、会话块保持连续；同会话内按该行覆盖降序，
// 平局与原序一致（稳定排序 ⇒ 覆盖齐平时是恒等变换）。覆盖 = 命中线索词的权重之和（原词优先，词干回退）。
func (s *Store) scopeSessionAuthors(scope string, rows []map[string]any) (map[string]map[string]bool, map[string]bool) {
	sessionAuthors := map[string]map[string]bool{}
	allAuthors := map[string]bool{}
	add := func(session, rawAuthor string) {
		if sessionAuthors[session] == nil {
			sessionAuthors[session] = map[string]bool{}
		}
		author := strings.ToLower(strings.TrimSpace(rawAuthor))
		if author != "" && author != "user" && author != "assistant" {
			sessionAuthors[session][author] = true
			allAuthors[author] = true
		}
	}
	for _, row := range rows {
		add(mapString(row, "session"), mapString(row, "author"))
	}
	if len(rows) >= 8 {
		dbRows, err := queryMaps(s.conn,
			`SELECT DISTINCT session, author FROM trace WHERE scope = ? AND author NOT IN ('', 'user', 'assistant')`,
			scope)
		if err == nil {
			for _, r := range dbRows {
				add(mapString(r, "session"), mapString(r, "author"))
			}
		}
	}
	return sessionAuthors, allAuthors
}

func sessionCoverageOrder(rows []map[string]any, weights []termWeight) []map[string]any {
	return sessionCoverageOrderWithTime(rows, weights, 0, 0, 0, nil, nil)
}

func sessionCoverageOrderWithTime(rows []map[string]any, weights []termWeight, qStart, qEnd int64, qMonth int, scopeAuthors map[string]map[string]bool, allScopeAuthors map[string]bool) []map[string]any {
	if len(rows) < 2 || len(weights) == 0 {
		return rows
	}
	sessionAuthors := map[string]map[string]bool{}
	allAuthors := map[string]bool{}
	for _, row := range rows {
		session := mapString(row, "session")
		if sessionAuthors[session] == nil {
			sessionAuthors[session] = map[string]bool{}
		}
		author := strings.ToLower(strings.TrimSpace(mapString(row, "author")))
		if author != "" && author != "user" && author != "assistant" {
			sessionAuthors[session][author] = true
			allAuthors[author] = true
		}
	}
	if scopeAuthors == nil {
		scopeAuthors = sessionAuthors
	}
	if allScopeAuthors == nil {
		allScopeAuthors = allAuthors
	}
	isTemporalToken := func(tok string) bool {
		if ids.ParseQueryMonth(tok) != 0 {
			return true
		}
		if len(tok) == 4 && (strings.HasPrefix(tok, "19") || strings.HasPrefix(tok, "20")) {
			return true
		}
		if qStart != 0 && qEnd != 0 && qEnd-qStart <= 7*86400 && len(tok) <= 2 && tok[0] >= '0' && tok[0] <= '9' {
			return true
		}
		return false
	}
	hasNonAuthorContentToken := false
	for _, entry := range weights {
		t := strings.ToLower(entry.term)
		if !allScopeAuthors[t] && !isTemporalToken(t) {
			hasNonAuthorContentToken = true
			break
		}
	}
	cover := make([]float64, len(rows))
	rowTerms := make([][]float64, len(rows))
	firstSeen := map[string]int{}
	scores := map[string]float64{}
	bestIdx := map[string]int{}
	sessionTermBest := map[string][]float64{}
	for index, row := range rows {
		session := mapString(row, "session")
		if _, ok := firstSeen[session]; !ok {
			firstSeen[session] = index
			bestIdx[session] = index
			sessionTermBest[session] = make([]float64, len(weights))
		}
		author := strings.ToLower(strings.TrimSpace(mapString(row, "author")))
		text := strings.ToLower(mapString(row, "text"))
		nextText := strings.ToLower(strings.TrimSpace(
			mapString(row, "_prev_text") + " " +
				mapString(row, "_next_text")))
		occurredAt := mapInt64(row, "occurred_at")
		inQueryTime := false
		isDayWindow := false
		isMonthWindow := false
		if occurredAt > 0 && len(rows) >= 8 {
			if qStart != 0 && qEnd != 0 {
				inQueryTime = occurredAt >= qStart && occurredAt <= qEnd
				isDayWindow = qEnd-qStart <= 7*86400
				isMonthWindow = !isDayWindow && qEnd-qStart <= 35*86400
			} else if qMonth != 0 {
				inQueryTime = monthOf(occurredAt) == qMonth
				isMonthWindow = true
			}
		}
		value := 0.0
		contentHit := false
		authorHit := false
		highIDFHits := 0
		highIDFSum := 0.0
		termBest := sessionTermBest[session]
		termsHit := make([]float64, len(weights))
		for ti, entry := range weights {
			needle := strings.ToLower(entry.term)
			w := 0.0
			if allScopeAuthors[needle] {
				// 说话人角色词：所属会话参与者含该角色即获角色归属分，消除「台词里喊对方名字比本人说 I 分高」的失衡。
				if scopeAuthors[session][needle] || hasWordPrefixOrExact(text, needle, true) {
					w = entry.weight * 0.75
					if author == needle || hasWordPrefixOrExact(text, needle, true) {
						w = entry.weight * 0.85
						authorHit = true
					}
				}
			} else if tf, firstPos := countWordPrefixOrExact(text, needle, true); tf > 0 {
				w = entry.weight
				if len(rows) >= 16 && len(allScopeAuthors) == 0 {
					// 1. Sublinear BM25-style TF saturation: multiple occurrences across header/body/tags reinforce relevance
					if tf >= 2 {
						tfBoost := 1.0 + 0.14*math.Log(float64(tf))
						if tfBoost > 1.28 {
							tfBoost = 1.28
						}
						w *= tfBoost
					}
					// 2. Closet / Header-Zone density resonance: hits in the leading 16*tau_atom (128-bit) structural summary zone
					// avoid dilution by multi-kilobyte trailing boilerplate while keeping short media items on equal footing (0 byte magic numbers).
					if firstPos >= 0 && memcore.StringShannonBits(text[:firstPos]) < 16.0*memcore.AtomicConceptEntropyBits {
						w *= 1.18
					}
				}
				contentHit = true
			} else if entry.stem != "" {
				if tf, firstPos := countWordPrefixOrExact(text, strings.ToLower(entry.stem), false); tf > 0 {
					w = entry.stemWeight
					if len(rows) >= 16 && len(allScopeAuthors) == 0 && firstPos >= 0 && memcore.StringShannonBits(text[:firstPos]) < 16.0*memcore.AtomicConceptEntropyBits {
						w *= 1.12
					}
					contentHit = true
				}
			} else if memcore.StringShannonBits(needle) >= memcore.AtomicConceptEntropyBits {
				if tf, _ := countWordPrefixOrExact(text, needle, false); tf > 0 && memcore.MatchTokenOrStem(text, needle) >= 0.85 {
					w = entry.weight * 0.88
					contentHit = true
				}
			}
			if w == 0 {
				if synW := matchSynonyms(text, entry.syns, entry.synWeights); synW > 0 {
					w = synW
					contentHit = true
				} else if nextText != "" && hasWordPrefixOrExact(nextText, needle, true) {
					w = entry.weight * 0.85
					contentHit = true
				} else if nextText != "" && entry.stem != "" && hasWordPrefixOrExact(nextText, strings.ToLower(entry.stem), false) {
					w = entry.stemWeight * 0.85
					contentHit = true
				} else if nextText != "" {
					if synW := matchSynonyms(nextText, entry.syns, entry.synWeights); synW > 0 {
						w = synW * 0.85
						contentHit = true
					} else if inQueryTime && isTemporalToken(needle) {
						baseW := entry.weight
						if isDayWindow && baseW < 3.0 {
							baseW = 3.0
						}
						w = baseW * 0.90
					}
				} else if inQueryTime && isTemporalToken(needle) {
					baseW := entry.weight
					if isDayWindow && baseW < 3.0 {
						baseW = 3.0
					}
					w = baseW * 0.90
				}
			}
			if w >= 3.5 && !allScopeAuthors[needle] {
				highIDFHits++
				highIDFSum += w
			}
			termsHit[ti] = w
			value += w
			if w > termBest[ti] {
				termBest[ti] = w
			}
		}
		// 多核心稀有词共现共振（High-IDF Co-Occurrence Synergy）：
		// 当单条记录同时精确命中 >=2 个高 IDF 稀有实词 (w >= 3.5) 时，联合自信息产生超线性协同加成，
		// 防止命中多个低信息量常见词的长文压过命中双核心专名的精确记录。
		if highIDFHits >= 2 && len(allScopeAuthors) == 0 {
			value += 0.35 * highIDFSum
		}
		if inQueryTime && (contentHit || (!hasNonAuthorContentToken && authorHit)) {
			if isDayWindow {
				value += 5.0
			} else if isMonthWindow {
				value += 2.5
			} else {
				value += 1.8
			}
		}
		rowTerms[index] = termsHit
		cover[index] = value
		if value > scores[session] {
			scores[session] = value
			bestIdx[session] = index
		}
	}
	// 邻轮拓扑承接分（零文本膨胀）+ 会话内次行边际覆盖加分（大库生效）
	if len(rows) >= 8 {
		adjBonus := make([]float64, len(rows))
		for i, rowI := range rows {
			sessI := mapString(rowI, "session")
			turnI := mapInt64(rowI, "turn")
			timeI := mapInt64(rowI, "occurred_at")
			if turnI <= 0 {
				continue
			}
			bestBridge := make([]float64, len(weights))
			for j, rowJ := range rows {
				if i == j || mapString(rowJ, "session") != sessI {
					continue
				}
				turnJ := mapInt64(rowJ, "turn")
				timeJ := mapInt64(rowJ, "occurred_at")
				diff := turnI - turnJ
				if diff < 0 {
					diff = -diff
				}
				if turnJ <= 0 || diff == 0 || diff > 8 {
					continue
				}
				if timeI > 0 && timeJ > 0 {
					dt := timeI - timeJ
					if dt < 0 {
						dt = -dt
					}
					if dt > 1 {
						continue
					}
				}
				for ti, entry := range weights {
					if allAuthors[strings.ToLower(entry.term)] {
						continue
					}
					if rowTerms[i][ti] == 0 && rowTerms[j][ti] > bestBridge[ti] {
						bestBridge[ti] = rowTerms[j][ti]
					}
				}
			}
			sumB := 0.0
			for _, bw := range bestBridge {
				sumB += bw
			}
			adjBonus[i] = 0.45 * sumB
		}
		for i := range rows {
			if adjBonus[i] > 0 {
				cover[i] += adjBonus[i]
				sess := mapString(rows[i], "session")
				if cover[i] > scores[sess] {
					scores[sess] = cover[i]
					bestIdx[sess] = i
				}
			}
		}
		for index, row := range rows {
			session := mapString(row, "session")
			top := bestIdx[session]
			if index == top {
				continue
			}
			topTerms := rowTerms[top]
			marginal := 0.0
			for ti, w := range rowTerms[index] {
				if w > topTerms[ti] {
					marginal += w - topTerms[ti]
				}
			}
			adj := 0.0
			topTurn := mapInt64(rows[top], "turn")
			curTurn := mapInt64(row, "turn")
			if topTurn > 0 && curTurn > topTurn && curTurn-topTurn <= 12 {
				adj = 1.5
			}
			if marginal > 0 || adj > 0 {
				promoted := cover[index] + 0.60*marginal + adj
				if promoted >= scores[session] {
					// 次行保持在会话最高行之后，不篡夺第 1 行位置
					promoted = math.Nextafter(scores[session], -math.MaxFloat64)
				}
				if promoted > cover[index] {
					cover[index] = promoted
				}
			}
		}
	}
	queryHasAuthor := false
	for _, entry := range weights {
		if allScopeAuthors[strings.ToLower(entry.term)] {
			queryHasAuthor = true
			break
		}
	}
	for session, peak := range scores {
		unionSum := 0.0
		for _, w := range sessionTermBest[session] {
			unionSum += w
		}
		if unionSum > peak {
			scores[session] = peak + 0.35*(unionSum-peak)
		}
		if queryHasAuthor && len(rows) >= 8 {
			hasTargetAuthor := false
			for ti, entry := range weights {
				needle := strings.ToLower(entry.term)
				if allScopeAuthors[needle] && (scopeAuthors[session][needle] || sessionTermBest[session][ti] > 0) {
					hasTargetAuthor = true
					break
				}
			}
			if !hasTargetAuthor {
				// 主角约束守恒：问句点名了对话角色，而本会话完全不含该角色 ⇒ 强降权外人会话
				scores[session] *= 0.20
			}
		}
	}
	effectiveSessionScore := make([]float64, len(rows))
	for i, row := range rows {
		sess := mapString(row, "session")
		if len(rows) >= 8 && i != bestIdx[sess] && cover[i] < 0.55*scores[sess] {
			// 会话内低分凑数次行不借首行之光插队在其他会话的高分首行之前
			effectiveSessionScore[i] = cover[i]
		} else {
			effectiveSessionScore[i] = scores[sess]
		}
	}
	order := make([]int, len(rows))
	for index := range order {
		order[index] = index
	}
	sort.SliceStable(order, func(left, right int) bool {
		li, ri := order[left], order[right]
		if effectiveSessionScore[li] != effectiveSessionScore[ri] {
			return effectiveSessionScore[li] > effectiveSessionScore[ri]
		}
		leftSession := mapString(rows[li], "session")
		rightSession := mapString(rows[ri], "session")
		if leftSession != rightSession && firstSeen[leftSession] != firstSeen[rightSession] {
			return firstSeen[leftSession] < firstSeen[rightSession]
		}
		return cover[li] > cover[ri]
	})
	ordered := make([]map[string]any, 0, len(rows))
	for _, index := range order {
		ordered = append(ordered, rows[index])
	}
	return ordered
}

// ConflictCount counts proposed conflict edges touching the assertions.
func (s *Store) ConflictCount(assertionIDs []string) (int, error) {
	values := []string{}
	for _, value := range assertionIDs {
		if value != "" {
			values = append(values, value)
		}
	}
	if len(values) == 0 {
		return 0, nil
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(values)), ",")
	args := make([]any, 0, 2*len(values))
	for _, value := range values {
		args = append(args, value)
	}
	for _, value := range values {
		args = append(args, value)
	}
	count, err := queryCount(s.conn,
		`SELECT count(*) FROM meta_edge
		 WHERE scope IS NOT NULL AND meta_kind = 'conflicts-with' AND status = 'proposed'
		   AND (lhs_id IN (`+placeholders+`) OR rhs_id IN (`+placeholders+`))`, args...)
	return int(count), err
}

// UnresolvedMentionCount counts mentions without a resolved node.
func (s *Store) UnresolvedMentionCount(scope string) (int, error) {
	count, err := queryCount(s.conn,
		"SELECT count(*) FROM mention WHERE scope = ? AND node_id IS NULL", scope)
	return int(count), err
}

// TimeRange returns the min/max trace occurrence time in the scope.
func (s *Store) TimeRange(scope string) (int64, int64, error) {
	var low, high sql.NullInt64
	if err := s.conn.QueryRow(
		"SELECT min(occurred_at), max(occurred_at) FROM trace WHERE scope = ?", scope,
	).Scan(&low, &high); err != nil {
		return 0, 0, err
	}
	if !low.Valid {
		return 0, 0, nil
	}
	return low.Int64, high.Int64, nil
}

func nullableSession(session *string) any {
	if session == nil {
		return nil
	}
	return *session
}

func nullableMonth(month *int) any {
	if month == nil {
		return nil
	}
	return *month
}

func uniqueSet(values []string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}
