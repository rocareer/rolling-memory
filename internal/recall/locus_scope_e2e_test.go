// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package recall_test

import (
	"testing"

	"github.com/rocareer/rolling-memory/internal/api"
	"github.com/rocareer/rolling-memory/internal/recall"
)

// TestRecallLocusAnchorPromotesSameWorkspaceDelivery pins the end-to-end wiring of the
// retrieval-scope anchor: with `[<workspace>]` in the cue, delivery must lead with a band
// whose node is stamped to that workspace, in both directions; without an anchor the
// delivery meta carries no anchor note (fail-open).
func TestRecallLocusAnchorPromotesSameWorkspaceDelivery(t *testing.T) {
	memory := newMemory(t)
	writeFact(t, memory, "首次进入控制台卡住要强刷新才能进入的守卫记录。", "当前工作区问题", "记录",
		map[string]any{"label": "守卫"}, map[string]any{"locus": "Rolling-Memory"})
	writeFact(t, memory, "首次进入控制台卡住要强刷新才能进入的审计报告。", "他仓问题", "记录",
		map[string]any{"label": "守卫"}, map[string]any{"locus": "Other-Workspace"})

	leadLocus := func(result *recall.Result) string {
		if len(result.Bands) == 0 {
			return ""
		}
		nodes, _ := result.Bands[0]["nodes"].([]map[string]any)
		if len(nodes) == 0 {
			return ""
		}
		locus, _ := nodes[0]["locus"].(string)
		return locus
	}

	for _, anchor := range []string{"Rolling-Memory", "Other-Workspace"} {
		result, err := memory.Recall(api.RecallRequest{Cue: "[" + anchor + "] 首次进入控制台卡住的守卫记录"})
		if err != nil {
			t.Fatalf("recall with anchor %q: %v", anchor, err)
		}
		if len(result.Bands) < 2 {
			t.Fatalf("anchor %q delivered %d bands, want both workspaces", anchor, len(result.Bands))
		}
		if got := leadLocus(result); got != anchor {
			t.Fatalf("anchor %q leads with locus %q, want %q", anchor, got, anchor)
		}
	}

	result, err := memory.Recall(api.RecallRequest{Cue: "首次进入控制台卡住的守卫记录"})
	if err != nil {
		t.Fatalf("recall without anchor: %v", err)
	}
	if _, ok := result.Meta["locus_anchor"]; ok {
		t.Fatalf("meta locus_anchor set without anchor: %#v", result.Meta["locus_anchor"])
	}
}
