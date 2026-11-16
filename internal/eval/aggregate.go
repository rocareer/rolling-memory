// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package eval

import (
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/rocareer/rolling-memory/internal/ids"
)

// Mean returns the arithmetic mean of values, or 0.0 for empty input.
func Mean(values []float64) float64 {
	return ids.Mean(values)
}

// PyRound mirrors round(value, digits) (binary-float half-even formatting).
func PyRound(value float64, digits int) float64 {
	text := strconv.FormatFloat(value, 'f', digits, 64)
	parsed, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return value
	}
	return parsed
}

type categoryBucket struct {
	n            int
	f1           []float64
	exact        int
	evidenceHit  int
	judgeCorrect int
	judgeScored  int
}

// Aggregate builds the summary block.
func Aggregate(rows []map[string]any, arm string) map[string]any {
	rate := func(key string) float64 {
		values := make([]float64, 0, len(rows))
		for _, row := range rows {
			if truthy(row[key]) {
				values = append(values, 1.0)
			} else {
				values = append(values, 0.0)
			}
		}
		return ids.Mean(values)
	}
	judged := []map[string]any{}
	for _, row := range rows {
		if truthy(row["judge"]) {
			judged = append(judged, row)
		}
	}
	buckets := map[string]*categoryBucket{}
	for _, row := range rows {
		category := pyStr(firstTruthy(row["category"], "?"))
		bucket := buckets[category]
		if bucket == nil {
			bucket = &categoryBucket{}
			buckets[category] = bucket
		}
		bucket.n++
		bucket.f1 = append(bucket.f1, rowFloat(row, "f1"))
		if truthy(row["exact"]) {
			bucket.exact++
		}
		if truthy(row["evidence_hit"]) {
			bucket.evidenceHit++
		}
		if truthy(row["judge"]) {
			bucket.judgeScored++
			if row["judge"] == "correct" {
				bucket.judgeCorrect++
			}
		}
	}
	byCategory := map[string]any{}
	for category, bucket := range buckets {
		var judgeCorrect any
		if bucket.judgeScored > 0 {
			judgeCorrect = PyRound(float64(bucket.judgeCorrect)/float64(bucket.judgeScored), 4)
		}
		exact := 0.0
		evidenceHit := 0.0
		if bucket.n > 0 {
			exact = PyRound(float64(bucket.exact)/float64(bucket.n), 4)
			evidenceHit = PyRound(float64(bucket.evidenceHit)/float64(bucket.n), 4)
		}
		byCategory[category] = map[string]any{
			"n":             bucket.n,
			"f1":            PyRound(ids.Mean(bucket.f1), 4),
			"exact":         exact,
			"evidence_hit":  evidenceHit,
			"judge_correct": judgeCorrect,
		}
	}

	f1Values := make([]float64, 0, len(rows))
	contextChars := make([]float64, 0, len(rows))
	bandValues := make([]float64, 0, len(rows))
	nodeValues := make([]float64, 0, len(rows))
	rawValues := make([]float64, 0, len(rows))
	extractedValues := make([]float64, 0, len(rows))
	answerRate := make([]float64, 0, len(rows))
	parseRate := make([]float64, 0, len(rows))
	hitF1 := []float64{}
	missF1 := []float64{}
	failureBuckets := map[string]int{
		"HIT":             0,
		"RECALL_MISS":     0,
		"RANK_CUTOFF":     0,
		"READER_GAP":      0,
		"JUDGE_AMBIGUITY": 0,
	}
	for _, row := range rows {
		bkt := pyStr(row["failure_bucket"])
		if bkt == "" {
			evHit := truthy(row["full_evidence_hit"]) || truthy(row["evidence_hit"])
			evRec := rowFloat(row, "full_evidence_recall")
			if evRec == 0 {
				evRec = rowFloat(row, "best_evidence_recall")
			}
			goldRec := rowFloat(row, "answer_token_recall")
			if goldRec == 0 {
				goldRec = rowFloat(row, "gold_recall")
			}
			bkt = ClassifyFailureBucket(
				evHit,
				truthy(row["answer_in_context"]),
				evRec,
				rowFloat(row, "f1"),
				goldRec,
				pyStr(row["judge"]),
			)
		}
		failureBuckets[bkt]++
		f1Values = append(f1Values, rowFloat(row, "f1"))
		contextChars = append(contextChars, rowFloat(row, "context_chars"))
		bandValues = append(bandValues, rowFloat(row, "bands"))
		nodeValues = append(nodeValues, rowFloat(row, "nodes"))
		rawValues = append(rawValues, rowFloat(row, "raw"))
		extractedValues = append(extractedValues, rowFloat(row, "extracted_assertions"))
		if strings.ToLower(pyStrip(pyStr(firstTruthy(row["prediction"], "")))) == "no record" {
			answerRate = append(answerRate, 0.0)
		} else {
			answerRate = append(answerRate, 1.0)
		}
		if truthy(row["extract_parse_ok"]) {
			parseRate = append(parseRate, 1.0)
		} else {
			parseRate = append(parseRate, 0.0)
		}
		if truthy(row["evidence_hit"]) {
			hitF1 = append(hitF1, rowFloat(row, "f1"))
		} else {
			missF1 = append(missF1, rowFloat(row, "f1"))
		}
	}

	var judgeCorrect, judgePartial, judgeWrong, f1Hit, f1Miss any
	if len(judged) > 0 {
		correct := 0.0
		partial := 0.0
		wrong := 0.0
		for _, row := range judged {
			switch row["judge"] {
			case "correct":
				correct++
			case "partial":
				partial++
			case "wrong":
				wrong++
			}
		}
		count := float64(len(judged))
		judgeCorrect = PyRound(correct/count, 4)
		judgePartial = PyRound(partial/count, 4)
		judgeWrong = PyRound(wrong/count, 4)
	}
	if len(hitF1) > 0 {
		f1Hit = PyRound(ids.Mean(hitF1), 4)
	}
	if len(missF1) > 0 {
		f1Miss = PyRound(ids.Mean(missF1), 4)
	}

	return map[string]any{
		"arm":                   arm,
		"n":                     len(rows),
		"f1":                    PyRound(ids.Mean(f1Values), 4),
		"exact":                 PyRound(rate("exact"), 4),
		"evidence_hit":          PyRound(rate("evidence_hit"), 4),
		"full_evidence_hit":     PyRound(rate("full_evidence_hit"), 4),
		"answer_in_context":     PyRound(rate("answer_in_context"), 4),
		"answer_rate":           PyRound(ids.Mean(answerRate), 4),
		"judge_correct":         judgeCorrect,
		"judge_partial":         judgePartial,
		"judge_wrong":           judgeWrong,
		"judge_scored":          len(judged),
		"f1_when_evidence_hit":  f1Hit,
		"f1_when_evidence_miss": f1Miss,
		"avg_context_chars":     PyRound(ids.Mean(contextChars), 1),
		"avg_bands":             PyRound(ids.Mean(bandValues), 1),
		"avg_nodes":             PyRound(ids.Mean(nodeValues), 1),
		"avg_raw":               PyRound(ids.Mean(rawValues), 1),
		"avg_extracted":         PyRound(ids.Mean(extractedValues), 2),
		"extract_parse_rate":    PyRound(ids.Mean(parseRate), 4),
		"failure_buckets":       failureBuckets,
		"by_category":           byCategory,
	}
}

// WriteMarkdown renders the human report.
func WriteMarkdown(summary map[string]any, path string) error {
	lines := []string{
		"# Rolling-Memory Cloud SDK Evaluation — " + pyStr(summary["arm"]),
		"",
		"| metric | value |",
		"| --- | --- |",
	}
	for _, key := range []string{
		"n", "f1", "exact", "evidence_hit", "answer_in_context", "answer_rate",
		"judge_correct", "judge_partial", "judge_wrong",
		"f1_when_evidence_hit", "f1_when_evidence_miss",
		"avg_context_chars", "avg_bands", "avg_nodes", "avg_raw", "avg_extracted",
		"extract_parse_rate",
	} {
		lines = append(lines, "| "+key+" | "+fmtValue(summary[key])+" |")
	}
	lines = append(lines, "", "## by category", "",
		"| category | n | f1 | exact | evidence_hit | judge_correct |",
		"| --- | --- | --- | --- | --- | --- |")
	buckets, _ := summary["by_category"].(map[string]any)
	categories := []string{}
	for category := range buckets {
		categories = append(categories, category)
	}
	sort.Strings(categories)
	for _, category := range categories {
		bucket, _ := buckets[category].(map[string]any)
		if bucket == nil {
			continue
		}
		lines = append(lines, "| "+category+" | "+fmtValue(bucket["n"])+" | "+fmtValue(bucket["f1"])+
			" | "+fmtValue(bucket["exact"])+" | "+fmtValue(bucket["evidence_hit"])+
			" | "+fmtValue(bucket["judge_correct"])+" |")
	}
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
}

func fmtValue(v any) string {
	return pyStr(v)
}

func rowFloat(row map[string]any, key string) float64 {
	switch typed := row[key].(type) {
	case float64:
		return typed
	case float32:
		return float64(typed)
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	}
	return 0.0
}
