// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package cluster

import (
	"math"
	"sort"
	"time"
)

// HitClusterGroup is the read-only delivery view of one cognitive cluster:
// hits sharing a SpacetimeCluster-adjacent origin are grouped for downstream
// LLM consumption without touching hit order, energy, or membership.
type HitClusterGroup struct {
	GroupID         int      `json:"group_id"`
	Day             string   `json:"day,omitempty"`
	UnitIDs         []string `json:"unit_ids"`
	PeakUnitID      string   `json:"peak_unit_id"`
	JointEnergyNats float64  `json:"joint_energy_nats"`
}

// GroupHitsByCluster reorganizes an already-ranked hit list into cognitive
// cluster groups (protocol P-NCD-1: the view is derived strictly from the hits
// slice plus immutable index metadata, so the hits themselves stay bit-for-bit
// untouched).
//
// Grouping key, in priority order:
//  1. the unit's endogenous episodic component (gap-split partition, F-33):
//     time-shift invariant, so a midnight-spanning event stays one group
//     instead of inheriting the UTC calendar-day phase cut;
//  2. units without any clock fall back to their SpacetimeCluster bucket id;
//  3. units unknown to the index form singleton groups (fail-open: a delivery
//     view must never drop a hit).
//
// Day is a display-only calendar annotation (UTC day of the group's
// best-ranked timed member); it never participates in the grouping key.
// Groups are ordered by their best (lowest-index) hit, so the first group
// always contains the top-1 hit; UnitIDs inside a group preserve hit order.
// JointEnergyNats sums member energies as a display aid only (no dedup claim,
// no ranking consequence).
func (e *Engine) GroupHitsByCluster(hits []Hit) []HitClusterGroup {
	if e == nil || e.Index == nil || len(hits) == 0 {
		return nil
	}
	idx := e.Index
	st := e.ensureClusterState()

	type groupKey struct {
		ep     int32
		bucket int32
	}
	keyOf := func(unitID string) (groupKey, int64) {
		u, ok := idx.LookupUnit(unitID)
		if !ok {
			return groupKey{ep: -1, bucket: -1}, 0
		}
		var ts int64
		if int(u) < len(st.refTimes) {
			ts = st.refTimes[u]
		}
		if int(u) < len(st.episodeOf) {
			if ep := st.episodeOf[u]; ep >= 0 {
				return groupKey{ep: ep, bucket: -1}, ts
			}
		}
		if int(u) < len(st.clusters) {
			return groupKey{ep: -1, bucket: st.clusters[u]}, ts
		}
		return groupKey{ep: -1, bucket: -1}, ts
	}

	order := make([]groupKey, 0, 8)
	members := make(map[groupKey][]int, 8)
	dayTS := make(map[groupKey]int64, 8)
	for i, h := range hits {
		k, ts := keyOf(h.UnitID)
		if k.ep == -1 && k.bucket == -1 {
			// Unknown unit: singleton group keyed by its hit position.
			k = groupKey{ep: -1, bucket: int32(-2 - i)}
		}
		if _, seen := members[k]; !seen {
			order = append(order, k)
		}
		members[k] = append(members[k], i)
		if ts > 0 && dayTS[k] == 0 {
			dayTS[k] = ts
		}
	}

	out := make([]HitClusterGroup, 0, len(order))
	for gi, k := range order {
		idxs := members[k]
		g := HitClusterGroup{
			GroupID: gi + 1,
			UnitIDs: make([]string, 0, len(idxs)),
		}
		if ts := dayTS[k]; ts > 0 {
			g.Day = time.Unix(ts, 0).UTC().Format("2006-01-02")
		}
		peak := math.Inf(-1)
		for _, hi := range idxs {
			h := hits[hi]
			g.UnitIDs = append(g.UnitIDs, h.UnitID)
			g.JointEnergyNats += h.Energy
			if h.Energy > peak {
				peak = h.Energy
				g.PeakUnitID = h.UnitID
			}
		}
		out = append(out, g)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].GroupID < out[j].GroupID })
	return out
}
