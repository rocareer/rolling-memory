// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package thinclient

import (
	"math"
	"sort"
)

// selectKernelSmoothedHits performs Contraharmonic-Bounded Temporal Kernel Smoothing over the
// candidate pool and returns the topK items:
//   - Let C_W = (∑ W_i^2) / (∑ W_i) be the endogenous contraharmonic mean of candidate weights.
//   - Every high-confidence hit in the superlevel set { u : W(u) >= C_W } is strictly order-invariant
//     (pure temporal proximity never reorders hits above C_W).
//   - For each anchor a with W(a) > C_W on a multi-hit day, its endogenous temporal scale tau_a is the
//     median of |t_v - t_a| across same-day peers v != a.
//   - A lower-weight same-day companion turn u (W(u) < C_W) receives geometric-contraharmonic kernel
//     support capped at C_W:
//     W_tilde(u) = max(W(u), min(C_W, max_{a} sqrt(W(a) * C_W) * exp(-|t_u - t_a| / tau_a))).
func selectKernelSmoothedHits(hits []CloudQueryHit, topK int) []CloudQueryHit {
	n := len(hits)
	if topK <= 0 || n <= topK {
		return hits
	}

	var maxW, sumW, sumWSq float64
	for _, h := range hits {
		if h.FinalWeight > 0 {
			w := h.FinalWeight
			sumW += w
			sumWSq += w * w
			if w > maxW {
				maxW = w
			}
		}
	}
	if maxW <= 0 || sumW <= 0 {
		return hits[:topK]
	}

	contraharmonicMean := sumWSq / sumW
	anchorFloor := contraharmonicMean
	if eFloor := maxW / math.E; eFloor > anchorFloor {
		anchorFloor = eFloor
	}

	dayGroups := make(map[int64][]int, 32)
	for i, h := range hits {
		if h.OccurredAt <= 0 {
			continue
		}
		d := h.OccurredAt / 86400
		dayGroups[d] = append(dayGroups[d], i)
	}

	type anchorKernel struct {
		idx int
		tau float64
	}
	dayAnchors := make(map[int64][]anchorKernel, len(dayGroups))
	for d, idxs := range dayGroups {
		if len(idxs) < 2 {
			continue
		}
		for _, aIdx := range idxs {
			if hits[aIdx].FinalWeight <= anchorFloor {
				continue
			}
			djs := make([]int64, 0, len(idxs)-1)
			for _, vIdx := range idxs {
				if vIdx == aIdx {
					continue
				}
				dt := hits[vIdx].OccurredAt - hits[aIdx].OccurredAt
				if dt < 0 {
					dt = -dt
				}
				if dt > 0 {
					djs = append(djs, dt)
				}
			}
			tau := 1.0
			if len(djs) > 0 {
				sort.Slice(djs, func(i, j int) bool { return djs[i] < djs[j] })
				tau = float64(djs[len(djs)/2])
				if tau < 1.0 {
					tau = 1.0
				}
			}
			dayAnchors[d] = append(dayAnchors[d], anchorKernel{idx: aIdx, tau: tau})
		}
	}

	if len(dayAnchors) == 0 {
		return hits[:topK]
	}

	newHits := make([]CloudQueryHit, n)
	copy(newHits, hits)
	anyAdjusted := false
	for i, u := range newHits {
		if u.FinalWeight >= contraharmonicMean || u.OccurredAt <= 0 {
			continue
		}
		d := u.OccurredAt / 86400
		anchors := dayAnchors[d]
		if len(anchors) == 0 {
			continue
		}
		bestCond := u.FinalWeight
		for _, ak := range anchors {
			a := hits[ak.idx]
			if a.FinalWeight <= u.FinalWeight {
				continue
			}
			dt := u.OccurredAt - a.OccurredAt
			if dt < 0 {
				dt = -dt
			}
			cond := math.Sqrt(a.FinalWeight*contraharmonicMean) * math.Exp(-float64(dt)/(ak.tau+1e-9))
			if cond > contraharmonicMean {
				cond = contraharmonicMean
			}
			if cond > bestCond {
				bestCond = cond
			}
		}
		if bestCond > u.FinalWeight {
			newHits[i].FinalWeight = bestCond
			anyAdjusted = true
		}
	}

	if anyAdjusted {
		sort.SliceStable(newHits, func(i, j int) bool {
			if newHits[i].FinalWeight != newHits[j].FinalWeight {
				return newHits[i].FinalWeight > newHits[j].FinalWeight
			}
			return newHits[i].UnitID < newHits[j].UnitID
		})
	}
	return newHits[:topK]
}

// selectAdaptiveDelivery wraps selectKernelSmoothedHits for LocalResolvedHit slices.
func selectAdaptiveDelivery(hits []LocalResolvedHit, deliver int) []LocalResolvedHit {
	if deliver <= 0 || len(hits) <= deliver {
		return hits
	}
	cloud := make([]CloudQueryHit, len(hits))
	byID := make(map[string]LocalResolvedHit, len(hits))
	for i := range hits {
		cloud[i] = hits[i].CloudQueryHit
		byID[hits[i].UnitID] = hits[i]
	}
	selected := selectKernelSmoothedHits(cloud, deliver)
	out := make([]LocalResolvedHit, len(selected))
	for i := range selected {
		out[i] = byID[selected[i].UnitID]
	}
	return out
}
