// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package memcore

import (
	"strings"
	"testing"
)

// Identifier tokens embed four-digit segments that are not time expressions
// ("Q-2026-31", "SKU2026A", "id_2026_01"). The bare-year guard must keep those out of the
// time window set while ordinary prose years still parse.
func TestBareYearInsideIdentifierIsNotTime(t *testing.T) {
	for _, q := range []string{
		"Receipt Q-2026-31 received from client A",
		"order SKU2026A shipped",
		"ticket id_2026_01 closed",
		"case #2026-114 resolved",
		"[tool_result] 1976     def _create_formsets(self, request, obj, change):",
		"options.go:1984 passes mutated object",
		"1984:    return formsets, inline_instances",
		"image resolution 1920×1080 uploaded",
	} {
		if wins := ParseTimeWindows(q); len(wins) != 0 {
			t.Fatalf("%q should not yield a time window, got %+v", q, wins)
		}
	}

	for _, q := range []string{
		"in 2026",
		"happened in 2026.",
		"2026年总结",
		"在2026年做完了",
		"during 2022-2023",
		"in mid-2023",
		"late-2022 trip",
	} {
		if wins := ParseTimeWindows(q); len(wins) == 0 {
			t.Fatalf("%q should still yield a year window", q)
		}
	}
	// Verify year range "2022-2023" yields both 2022 and 2023 windows
	if wins := ParseTimeWindows("events during 2022-2023"); len(wins) < 2 {
		t.Fatalf("expected '2022-2023' to yield 2 year windows, got %+v", wins)
	}
}

// Rendering must distinguish historical versions from the current value instead of relying on
// the caller to remember that a row was superseded.
func TestEngineRenderMarksInactiveStatus(t *testing.T) {
	items := []ScoredAssertion{
		{
			Assertion: Assertion{
				NodeID: "a_1", SubjectID: "s_1", RelID: "x_1", ObjectID: "o_1",
				Warrant: "referential", Status: "superseded", TSource: 1760000000,
			},
			SubjectName: "品牌A", RelName: "主色为", ObjectName: "深海蓝",
			Snippet: "main color is deep blue",
		},
		{
			Assertion: Assertion{
				NodeID: "a_2", SubjectID: "s_1", RelID: "x_2", ObjectID: "o_2",
				Warrant: "performative", Status: "active", LoopState: LoopStateFulfilled, TSource: 1760000000,
			},
			SubjectName: "品牌A", RelName: "主色为", ObjectName: "量子紫",
			Snippet: "changed to quantum purple",
		},
	}
	bands := ScoreAndAssembleBands("brand main color", items, 12)
	if len(bands) == 0 {
		t.Fatalf("expected a band")
	}
	rendered := bands[0].Rendered
	if !strings.Contains(rendered, "[superseded]") {
		t.Fatalf("historical row should carry its status tag: %s", rendered)
	}
	if !strings.Contains(rendered, "[已兑现]") {
		t.Fatalf("closed prospective row should carry the loop tag: %s", rendered)
	}
	if strings.Contains(rendered, "量子紫 ｜ [superseded]") {
		t.Fatalf("active row must not be marked superseded: %s", rendered)
	}
}
