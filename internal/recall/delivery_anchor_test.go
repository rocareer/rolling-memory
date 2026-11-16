// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package recall

import (
	"strings"
	"testing"

	"github.com/rocareer/rolling-memory/internal/store"
)

// TestDeliveryAnchorDoseFlagsBadSpans verifies that a mutated span is annotated
// (not silently dropped), counted once by the dose, and a healthy node stays unmarked.
func TestDeliveryAnchorDoseFlagsBadSpans(t *testing.T) {
	badEntry := &bandCandidate{candidate: store.Candidate{Data: store.CandidateRow{
		Assertion: store.Assertion{
			NodeID:          "a_bad",
			AnchorTraceHash: strings.Repeat("h", 8),
			AnchorStart:     0,
			AnchorEnd:       99,
		},
		TraceText: "abcd",
	}}}
	bad := nodeView(badEntry, nil)
	if bad["anchor_ok"] != false {
		t.Fatalf("bad node view = %#v", bad)
	}
	if !strings.Contains(nodeLine(bad), "锚异常") {
		t.Fatalf("node line = %q", nodeLine(bad))
	}
	checked, failed := deliveryAnchorDose([]map[string]any{{"nodes": []map[string]any{bad}}})
	if checked != 1 || failed != 1 {
		t.Fatalf("dose = (%d, %d), want (1, 1)", checked, failed)
	}

	goodEntry := &bandCandidate{candidate: store.Candidate{Data: store.CandidateRow{
		Assertion: store.Assertion{
			NodeID:          "a_good",
			AnchorTraceHash: strings.Repeat("h", 8),
			AnchorStart:     0,
			AnchorEnd:       4,
		},
		TraceText: "abcd",
	}}}
	good := nodeView(goodEntry, nil)
	if _, ok := good["anchor_ok"]; ok {
		t.Fatalf("healthy node view = %#v", good)
	}
	checked, failed = deliveryAnchorDose([]map[string]any{{"nodes": []map[string]any{good}}})
	if checked != 1 || failed != 0 {
		t.Fatalf("dose = (%d, %d), want (1, 0)", checked, failed)
	}
}
