// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package api_test

import (
	"strings"
	"testing"

	"github.com/rocareer/rolling-memory/internal/api"
	"github.com/rocareer/rolling-memory/internal/llm"
)

// fakeHintsLLM is the read-side test double for query hints:
// one query rewrite, zero write-side extraction.
type fakeHintsLLM struct {
	hints map[string]string
	calls int
}

func (f *fakeHintsLLM) Extract(text, speaker string, timeRef int64) (llm.Extraction, error) {
	return llm.Extraction{}, nil
}

func (f *fakeHintsLLM) Name() string { return "fake:query" }

func (f *fakeHintsLLM) QueryHints(cue string) (map[string]string, error) {
	f.calls++
	cloned := map[string]string{}
	for key, value := range f.hints {
		cloned[key] = value
	}
	return cloned, nil
}

// TestDeepUsesOneQueryRewriteToFillHints verifies that deep recall performs
// a single query-hint rewrite and surfaces the matched assertion.
func TestDeepUsesOneQueryRewriteToFillHints(t *testing.T) {
	memory := newMemory(t)
	memory.Config.Recall.Vector = false
	rememberOne(t, memory, "老张养了一只猫。", "老张", "养", map[string]any{"label": "猫"})
	fake := &fakeHintsLLM{hints: map[string]string{"s": "老张", "x": "养", "o": "猫"}}
	memory.LLM = fake

	mechanical, err := memory.Recall(api.RecallRequest{
		Cue: "那只宠物是谁在照顾", IncludeRaw: boolPtr(false),
	})
	if err != nil {
		t.Fatalf("mechanical recall: %v", err)
	}
	if !mechanical.Absent {
		t.Fatalf("mechanical recall = %#v", mechanical.ToDict())
	}
	if fake.calls != 0 {
		t.Fatalf("mechanical recall called the LLM %d times", fake.calls)
	}
	if mechanical.Meta["deep"].(map[string]any)["requested"] != false {
		t.Fatalf("deep meta = %#v", mechanical.Meta["deep"])
	}

	deep, err := memory.Recall(api.RecallRequest{
		Cue: "那只宠物是谁在照顾", IncludeRaw: boolPtr(false), Deep: true,
	})
	if err != nil {
		t.Fatalf("deep recall: %v", err)
	}
	if deep.Absent {
		t.Fatalf("deep recall = %#v", deep.ToDict())
	}
	if fake.calls != 1 {
		t.Fatalf("deep recall called the LLM %d times", fake.calls)
	}
	deepMeta := deep.Meta["deep"].(map[string]any)
	if deepMeta["used"] != true || deepMeta["hints"].(map[string]string)["s"] != "老张" {
		t.Fatalf("deep meta = %#v", deepMeta)
	}
	if !strings.Contains(deep.Render(8000), "老张 —养→ 猫") {
		t.Fatalf("render = %s", deep.Render(8000))
	}
}

// TestDeepWithoutLLMIsDegradedNotError verifies that requesting deep recall
// without a configured LLM degrades cleanly instead of failing.
func TestDeepWithoutLLMIsDegradedNotError(t *testing.T) {
	memory := newMemory(t)
	memory.Config.Recall.Vector = false
	memory.LLM = nil
	result, err := memory.Recall(api.RecallRequest{
		Cue: "zzzqqqxxx", IncludeRaw: boolPtr(false), Deep: true,
	})
	if err != nil {
		t.Fatalf("deep recall: %v", err)
	}
	if !result.Absent {
		t.Fatalf("result = %#v", result.ToDict())
	}
	deepMeta := result.Meta["deep"].(map[string]any)
	if deepMeta["requested"] != true || deepMeta["used"] != false {
		t.Fatalf("deep meta = %#v", deepMeta)
	}
	if _, ok := deepMeta["error"]; !ok {
		t.Fatalf("deep meta = %#v", deepMeta)
	}
}
