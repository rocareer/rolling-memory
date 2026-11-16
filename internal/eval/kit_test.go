// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package eval

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeAnswer(t *testing.T) {
	cases := map[string]string{
		"The Cat, sat!":       "cat sat",
		"an apple and a pear": "apple and pear",
		"No record.":          "no record",
		"北京（海淀区）":             "北 京 海 淀 区",
		"":                    "",
	}
	for input, expected := range cases {
		if got := NormalizeAnswer(input); got != expected {
			t.Fatalf("NormalizeAnswer(%q) = %q, want %q", input, got, expected)
		}
	}
	if NormalizeAnswer("北京") == NormalizeAnswer("上海") {
		t.Fatalf("distinct CJK answers must not collapse to identical empty strings")
	}
}

func TestTokenF1(t *testing.T) {
	cases := []struct {
		prediction string
		gold       string
		expected   float64
	}{
		{"the cat", "cat", 1.0},
		{"cat", "dog", 0.0},
		{"北京", "上海", 0.0},
		{"北京", "北京", 1.0},
		{"", "cat", 0.0},
		{"cat cat", "cat", 2 * 0.5 * 1.0 / 1.5},
	}
	for _, test := range cases {
		if got := TokenF1(test.prediction, test.gold); got != test.expected {
			t.Fatalf("TokenF1(%q, %q) = %v, want %v", test.prediction, test.gold, got, test.expected)
		}
	}
}

func TestTokenRecallAndBestEvidence(t *testing.T) {
	if got := TokenRecall("cat", "the cat sat"); got != 1.0 {
		t.Fatalf("TokenRecall = %v, want 1", got)
	}
	if got := TokenRecall("cat, dog", "the cat sat"); got != 0.5 {
		t.Fatalf("TokenRecall = %v, want 0.5", got)
	}
	if got := TokenRecall("", "anything"); got != 0.0 {
		t.Fatalf("empty needle = %v, want 0", got)
	}
	if got := BestEvidenceRecall(nil, "context"); got != 0.0 {
		t.Fatalf("empty evidence = %v, want 0", got)
	}
	evidence := []string{"nothing here", "the cat sat"}
	if got := BestEvidenceRecall(evidence, "yesterday the cat sat down"); got != 1.0 {
		t.Fatalf("best evidence recall = %v, want 1", got)
	}
}

func TestAggregate(t *testing.T) {
	rows := []map[string]any{
		{
			"category": "multi-hop", "f1": 1.0, "exact": true, "evidence_hit": true,
			"answer_in_context": true, "judge": "correct", "prediction": "cat",
			"context_chars": 10, "bands": 1, "nodes": 2, "raw": 0,
			"extracted_assertions": 3, "extract_parse_ok": true,
		},
		{
			"category": "multi-hop", "f1": 0.0, "exact": false, "evidence_hit": false,
			"answer_in_context": false, "judge": "wrong", "prediction": "no record",
			"context_chars": 20, "bands": 0, "nodes": 0, "raw": 1,
			"extracted_assertions": 0, "extract_parse_ok": false,
		},
		{
			"category": "temporal", "f1": 0.5, "exact": false, "evidence_hit": true,
			"answer_in_context": true, "judge": nil, "prediction": "2019",
			"context_chars": 30, "bands": 2, "nodes": 4, "raw": 0,
			"extracted_assertions": 1, "extract_parse_ok": true,
		},
	}
	summary := Aggregate(rows, "raw-none")
	if summary["arm"] != "raw-none" || summary["n"] != 3 {
		t.Fatalf("summary = %v", summary)
	}
	if summary["f1"] != 0.5 {
		t.Fatalf("f1 = %v", summary["f1"])
	}
	if summary["exact"] != 0.3333 || summary["evidence_hit"] != 0.6667 ||
		summary["answer_rate"] != 0.6667 || summary["extract_parse_rate"] != 0.6667 {
		t.Fatalf("rates = %v", summary)
	}
	if summary["judge_scored"] != 2 || summary["judge_correct"] != 0.5 || summary["judge_wrong"] != 0.5 {
		t.Fatalf("judge = %v", summary)
	}
	if summary["f1_when_evidence_hit"] != 0.75 || summary["f1_when_evidence_miss"] != 0.0 {
		t.Fatalf("conditional f1 = %v / %v", summary["f1_when_evidence_hit"], summary["f1_when_evidence_miss"])
	}
	if summary["avg_context_chars"] != 20.0 || summary["avg_bands"] != 1.0 ||
		summary["avg_nodes"] != 2.0 || summary["avg_raw"] != 0.3 || summary["avg_extracted"] != 1.33 {
		t.Fatalf("averages = %v", summary)
	}
	categories := summary["by_category"].(map[string]any)
	multi := categories["multi-hop"].(map[string]any)
	if multi["n"] != 2 || multi["f1"] != 0.5 || multi["exact"] != 0.5 ||
		multi["evidence_hit"] != 0.5 || multi["judge_correct"] != 0.5 {
		t.Fatalf("multi-hop = %v", multi)
	}
	temporal := categories["temporal"].(map[string]any)
	if temporal["n"] != 1 || temporal["f1"] != 0.5 || temporal["judge_correct"] != nil {
		t.Fatalf("temporal = %v", temporal)
	}
}

func TestAggregateWithoutJudge(t *testing.T) {
	rows := []map[string]any{{"category": nil, "f1": 1.0, "prediction": "x"}}
	summary := Aggregate(rows, "oracle-none")
	if summary["judge_correct"] != nil || summary["judge_partial"] != nil || summary["judge_wrong"] != nil {
		t.Fatalf("judge fields = %v", summary)
	}
	if _, ok := summary["by_category"].(map[string]any)["?"]; !ok {
		t.Fatalf("missing '?' bucket: %v", summary["by_category"])
	}
}

func TestWriteMarkdown(t *testing.T) {
	summary := Aggregate([]map[string]any{
		{"category": "temporal", "f1": 1.0, "exact": true, "evidence_hit": true, "judge": "correct"},
	}, "raw-none")
	path := filepath.Join(t.TempDir(), "summary.md")
	if err := WriteMarkdown(summary, path); err != nil {
		t.Fatalf("WriteMarkdown: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	text := string(raw)
	if !strings.HasPrefix(text, "# Rolling-Memory Cloud SDK Evaluation — raw-none") {
		t.Fatalf("heading = %q", text)
	}
	if !strings.Contains(text, "| n | 1 |") || !strings.Contains(text, "| temporal | 1 | 1 |") {
		t.Fatalf("markdown = %q", text)
	}
	if !strings.Contains(text, "| judge_wrong | None |") && !strings.Contains(text, "| judge_wrong | 0 |") {
		t.Fatalf("judge_wrong row = %q", text)
	}
}

func TestParallelMapKeepsOrder(t *testing.T) {
	values := ParallelMap(10, 3, func(index int) int { return index * index })
	for index, value := range values {
		if value != index*index {
			t.Fatalf("values[%d] = %d", index, value)
		}
	}
}
