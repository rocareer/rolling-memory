// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package atmdata

import "sort"

// RecallAt 是官方 gold-set-mean 口径的单题 Recall@k：
// |set(GT) ∩ set(Top-k)| / |set(GT)|，分子分母两侧均先按集合去重。
func RecallAt(evidenceIDs []string, topIDs []string, k int) float64 {
	gt := dedupSorted(evidenceIDs)
	if len(gt) == 0 || k <= 0 || len(topIDs) == 0 {
		return 0
	}
	top := dedupTopK(topIDs, k)
	matched := 0
	for _, id := range top {
		if positionOf(gt, id) >= 0 {
			matched++
		}
	}
	return float64(matched) / float64(len(gt))
}

// AnyHitAt 是辅助二值口径：Top-k 内是否至少命中一枚金标。
func AnyHitAt(evidenceIDs []string, topIDs []string, k int) bool {
	return RecallAt(evidenceIDs, topIDs, k) > 0
}

// AllHitAt 是集合完备度口径：Top-k 是否覆盖全部金标。
func AllHitAt(evidenceIDs []string, topIDs []string, k int) bool {
	gt := dedupSorted(evidenceIDs)
	if len(gt) == 0 {
		return false
	}
	return RecallAt(evidenceIDs, topIDs, k) == 1
}

func dedupTopK(ids []string, k int) []string {
	if len(ids) == 0 || k <= 0 {
		return nil
	}
	limit := k
	if limit > len(ids) {
		limit = len(ids)
	}
	return dedupSorted(ids[:limit])
}

func dedupSorted(ids []string) []string {
	if len(ids) == 0 {
		return nil
	}
	out := append([]string(nil), ids...)
	sort.Strings(out)
	write := 0
	for read := 0; read < len(out); read++ {
		if read > 0 && out[read] == out[read-1] {
			continue
		}
		out[write] = out[read]
		write++
	}
	return out[:write]
}

func positionOf(sorted []string, id string) int {
	at := sort.SearchStrings(sorted, id)
	if at < len(sorted) && sorted[at] == id {
		return at
	}
	return -1
}
