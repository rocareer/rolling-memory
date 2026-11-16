// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

// zz_atm_tokenstats_test.go —— ATM 评测统计器（本地复算：交付量/延迟/官方召回口径）。
//
// 逐题复算 tokenz-r（唯一合法分词与 Token 计数标准）交付量、字符数、交付条数与检索延迟，
// 并按官方规则独立复算 ATM R@10/R@25/R@50/Hit@10（与 internal/eval.ComputeATMRecallMap 同规则）。
//
// 用法：
//
//	ATM_STATS_RESULTS=<results.jsonl> ATM_STATS_OUT=<stats.json> \
//	  [ATM_STATS_TOKENS_OUT=<index->tokens.json>] \
//	  go test ./internal/eval -run TestZZATMTokenStats -count=1 -v
//
// 输出 stats.json：{n, tokens{mean,total,median,p95,max}, chars{...}, items{mean,max},
// recall_ms{mean,median,p95,max,distinct_rounded}, judge{correct,wrong,abstained,scored},
// qs{scored,mean}, evidence_hit{rate,count,full_rate}, buckets{}, by_category{},
// atm_recall{R@1,R@10,R@25,R@50,Hit@10,hit_at_10_count}}。
package eval

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/rocareer/rolling-memory/internal/tokenzr"
)

func atmStatSort(xs []float64) []float64 {
	out := append([]float64(nil), xs...)
	sort.Float64s(out)
	return out
}

func atmStatMean(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := 0.0
	for _, x := range xs {
		s += x
	}
	return s / float64(len(xs))
}

func atmStatMedian(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := atmStatSort(xs)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

// atmStatNearestRank 最近秩百分位（与统计惯例一致：取 ceil(p*n)-1）。
func atmStatNearestRank(xs []float64, p float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := atmStatSort(xs)
	idx := int(math.Ceil(p*float64(len(s)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(s) {
		idx = len(s) - 1
	}
	return s[idx]
}

func atmStatMax(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	m := xs[0]
	for _, x := range xs {
		if x > m {
			m = x
		}
	}
	return m
}

func TestZZATMTokenStats(t *testing.T) {
	resultsPath := strings.TrimSpace(os.Getenv("ATM_STATS_RESULTS"))
	if resultsPath == "" {
		t.Skip("set ATM_STATS_RESULTS=<results.jsonl>")
	}
	f, err := os.Open(resultsPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<26)

	var tokens, chars, items, recallMS []float64
	tokenMap := map[int]int{}
	judgeCounts := map[string]int{}
	buckets := map[string]int{}
	byCatHit, byCatN := map[string]int{}, map[string]int{}
	byCatJudgeOk, byCatJudgeN := map[string]int{}, map[string]int{}
	qsScored, evHits := 0, 0
	qsSum := 0.0
	// ATM 官方口径 R@k 复算（与 ComputeATMRecallMap 同规则：集合交 / 金标数）。
	atmKs := []int{1, 10, 25, 50}
	atmSum := map[int]float64{}
	hitAt10 := 0
	rowIdx := 0
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var row map[string]any
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatalf("parse row: %v", err)
		}
		rowIdx++
		var formatted []string
		if arr, ok := row["formatted_items"].([]any); ok {
			for _, it := range arr {
				if s, ok := it.(string); ok {
					formatted = append(formatted, s)
				}
			}
		}
		text := strings.Join(formatted, "\n")
		tok := tokenzr.Count(text)
		tokenMap[rowIdx] = tok
		tokens = append(tokens, float64(tok))
		chars = append(chars, float64(len([]rune(text))))
		items = append(items, float64(len(formatted)))
		recallSec, _ := row["recall_seconds"].(float64)
		recallMS = append(recallMS, recallSec*1000)

		judge, _ := row["judge"].(string)
		judgeCounts[judge]++
		if judge == "correct" || judge == "wrong" {
			byCatJudgeN[fmt.Sprint(row["category"])]++
			if judge == "correct" {
				byCatJudgeOk[fmt.Sprint(row["category"])]++
			}
		}
		bucket, _ := row["failure_bucket"].(string)
		buckets[bucket]++
		cat := fmt.Sprint(row["category"])
		byCatN[cat]++
		if evHit, _ := row["evidence_hit"].(bool); evHit {
			evHits++
			byCatHit[cat]++
		}
		if qsc, _ := row["qs_scored"].(bool); qsc {
			qsScored++
			qs, _ := row["qs_score"].(float64)
			qsSum += qs
		}
		// R@k 复算：ATM 行用 retrieval_ids，LoCoMo 行用 retrieval_unit_ids。
		var retIDs, gtIDs []string
		if arr, ok := row["retrieval_ids"].([]any); ok {
			for _, it := range arr {
				if s, ok := it.(string); ok {
					retIDs = append(retIDs, s)
				}
			}
		}
		if arr, ok := row["retrieval_unit_ids"].([]any); ok {
			for _, it := range arr {
				if s, ok := it.(string); ok {
					retIDs = append(retIDs, s)
				}
			}
		}
		if arr, ok := row["evidence_texts"].([]any); ok {
			for _, it := range arr {
				if s, ok := it.(string); ok {
					gtIDs = append(gtIDs, s)
				}
			}
		}
		if len(gtIDs) > 0 {
			gtSet := map[string]bool{}
			for _, id := range gtIDs {
				gtSet[id] = true
			}
			for _, k := range atmKs {
				lim := k
				if len(retIDs) < lim {
					lim = len(retIDs)
				}
				hits := 0
				for i := 0; i < lim; i++ {
					if gtSet[retIDs[i]] {
						hits++
					}
				}
				atmSum[k] += float64(hits) / float64(len(gtIDs))
			}
			// Hit@10：R@10 > 0（ATM 官方口径 Hit 判定）。
			lim := 10
			if len(retIDs) < lim {
				lim = len(retIDs)
			}
			for i := 0; i < lim; i++ {
				if gtSet[retIDs[i]] {
					hitAt10++
					break
				}
			}
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if rowIdx == 0 {
		t.Fatal("no rows")
	}
	n := float64(rowIdx)

	stats := map[string]any{
		"n": rowIdx,
		"tokens": map[string]any{"mean": atmStatMean(tokens), "total": atmStatMean(tokens) * n,
			"median": atmStatMedian(tokens), "p95": atmStatNearestRank(tokens, 0.95), "max": atmStatMax(tokens)},
		"chars": map[string]any{"mean": atmStatMean(chars), "median": atmStatMedian(chars),
			"p95": atmStatNearestRank(chars, 0.95), "max": atmStatMax(chars)},
		"items": map[string]any{"mean": atmStatMean(items), "max": atmStatMax(items)},
		"recall_ms": map[string]any{"mean": atmStatMean(recallMS), "median": atmStatMedian(recallMS),
			"p95": atmStatNearestRank(recallMS, 0.95), "max": atmStatMax(recallMS)},
		"judge":           judgeCounts,
		"buckets":         buckets,
		"evidence_hit":    map[string]any{"count": evHits, "rate": float64(evHits) / n},
		"qs":              map[string]any{"scored": qsScored, "mean": qsSum / math.Max(1, float64(qsScored))},
		"by_category_hit": byCatHit, "by_category_n": byCatN,
		"by_category_judge_ok": byCatJudgeOk, "by_category_judge_n": byCatJudgeN,
		"atm_recall": map[string]any{
			"R@1": atmSum[1] / n, "R@10": atmSum[10] / n, "R@25": atmSum[25] / n,
			"R@50": atmSum[50] / n, "Hit@10": float64(hitAt10) / n, "Hit@10_count": hitAt10,
		},
	}
	// recall_ms 恒定值指纹：分桶到 100ms，检查是否被单一值支配（防「超时截断伪装值」）。
	rounded := map[int]int{}
	for _, ms := range recallMS {
		rounded[int(ms/100)]++
	}
	maxBucket, maxCount := 0, 0
	for b, c := range rounded {
		if c > maxCount {
			maxBucket, maxCount = b, c
		}
	}
	stats["recall_ms_dominant_bucket"] = map[string]any{"ms_rounded_100": maxBucket * 100, "count": maxCount, "share": float64(maxCount) / n}

	outPath := strings.TrimSpace(os.Getenv("ATM_STATS_OUT"))
	if outPath != "" {
		raw, _ := json.MarshalIndent(stats, "", "  ")
		if err := os.WriteFile(outPath, raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if tokOut := strings.TrimSpace(os.Getenv("ATM_STATS_TOKENS_OUT")); tokOut != "" {
		raw, _ := json.Marshal(tokenMap)
		if err := os.WriteFile(tokOut, raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("n=%d tokens/mean=%.0f chars/mean=%.0f items/mean=%.1f recall_ms/mean=%.0f median=%.0f R@10=%.4f Hit@10=%.4f judge=%v buckets=%v",
		rowIdx, atmStatMean(tokens), atmStatMean(chars), atmStatMean(items),
		atmStatMean(recallMS), atmStatMedian(recallMS), atmSum[10]/n, float64(hitAt10)/n, judgeCounts, buckets)
}
