// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package mcp

import (
	"testing"

	"github.com/rocareer/rolling-memory/internal/memory/recall"
)

func TestRecallOutcomeUsesDeliveredEvidence(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result recall.Result
		want   string
	}{
		{name: "not ready", result: recall.Result{Gaps: map[string]int{"snapshot_unavailable": 1}}, want: "unknown"},
		{name: "absent", result: recall.Result{Absent: true}, want: "empty"},
		{name: "hit", result: recall.Result{Bands: []map[string]any{{"nodes": []map[string]any{{"id": "u1"}}}}}, want: "hits"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for mode, receipt := range map[string]any{"full": tc.result.ToDict(), "probe": tc.result.ToProbe()} {
				if got := recallOutcome(receipt); got != tc.want {
					t.Errorf("%s outcome = %q, want %q", mode, got, tc.want)
				}
			}
		})
	}
}
