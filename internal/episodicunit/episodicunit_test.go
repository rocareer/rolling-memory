// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package episodicunit

import (
	"fmt"
	"strings"
	"testing"

	"github.com/rocareer/rolling-memory/internal/episodic"
	"github.com/rocareer/rolling-memory/internal/tokenzr"
)

// TestSegmentUnitsKeepsOneUnitPerSegment pins the core rule: one speech segment is
// one unit (never a merged blob), the pointer carries `#<k>`, and the docID is
// content-addressed so identical speech collapses.
func TestSegmentUnitsKeepsOneUnitPerSegment(t *testing.T) {
	segment := episodic.Segment{Role: "assistant", Text: "结论：缓存按 docID 失效。", Index: 3}
	units := SegmentUnits("mem:test", "@ref:zcode:s1:m1", segment, 1793102400, true)
	if len(units) != 1 {
		t.Fatalf("expected one unit, got %+v", units)
	}
	unit := units[0]
	if unit.Text != "[assistant] 结论：缓存按 docID 失效。" {
		t.Fatalf("unit text = %q", unit.Text)
	}
	if unit.RefPointer != "@ref:zcode:s1:m1#3" {
		t.Fatalf("unit pointer = %q", unit.RefPointer)
	}
	if unit.TurnRef != "@ref:zcode:s1:m1" {
		t.Fatalf("turn provenance = %q", unit.TurnRef)
	}
	if unit.DocID != DocID("mem:test", unit.Text) || !strings.HasPrefix(unit.DocID, "ep_") {
		t.Fatalf("docID = %q", unit.DocID)
	}
	// Identical speech collapses onto the same content-addressed unit.
	again := SegmentUnits("mem:test", "@ref:zcode:s9:m9", segment, 1793102500, true)
	if again[0].DocID != unit.DocID {
		t.Fatalf("content addressing broken: %q != %q", again[0].DocID, unit.DocID)
	}
	if again[0].TurnRef == unit.TurnRef {
		t.Fatalf("provenance must follow the source turn, both = %q", again[0].TurnRef)
	}
}

// TestChunkSplitsOnlyAtSentenceBoundaries pins the width contract: an oversized
// segment becomes several units under the rung, no unit is sliced mid-sentence,
// and a sentence that alone exceeds the rung is kept whole.
func TestChunkSplitsOnlyAtSentenceBoundaries(t *testing.T) {
	sentence := "先确认缓存失效断言并复跑全部包，确认没有回归之后再汇报结论。"
	text := strings.Repeat(sentence, 60)
	chunks := Chunk(text, 128)
	if len(chunks) < 2 {
		t.Fatalf("expected the oversized text to be chunked, got %d chunks", len(chunks))
	}
	for _, chunk := range chunks {
		if tokenzr.Count(chunk) > 128 {
			if strings.Count(chunk, "。") < 2 {
				continue // single oversized sentence is allowed to stay whole
			}
			t.Fatalf("chunk %d tokens exceeds the rung: %.80q", tokenzr.Count(chunk), chunk)
		}
		if !strings.HasSuffix(strings.TrimSpace(chunk), "。") && chunk != chunks[len(chunks)-1] {
			t.Fatalf("chunk does not end at a sentence boundary: %.80q", chunk)
		}
	}
	joined := strings.Join(chunks, "")
	if strings.ReplaceAll(joined, " ", "") != strings.ReplaceAll(text, " ", "") {
		t.Fatalf("chunking must not drop or rewrite speech")
	}
}

// TestChunkKeepsSmallUnitIntact pins the fast path: a unit that already fits is
// returned verbatim as a single chunk.
func TestChunkKeepsSmallUnitIntact(t *testing.T) {
	text := "[user] 把缓存按 docID 失效。"
	if got := Chunk(text, TokenBudget); len(got) != 1 || got[0] != text {
		t.Fatalf("small unit must stay intact, got %+v", got)
	}
	if got := Chunk("", TokenBudget); got != nil {
		t.Fatalf("empty text must yield no chunks, got %+v", got)
	}
}

// TestSegmentUnitsPointsEveryChunkOfALongSegment pins the zero-copy contract for
// oversized segments: every emitted chunk carries `@ref:...#<k>.<c>`, so the unit
// resolves live from the same deterministic re-split instead of storing plaintext.
func TestSegmentUnitsPointsEveryChunkOfALongSegment(t *testing.T) {
	sentence := "先确认缓存失效断言并复跑全部包，确认没有回归之后再汇报结论。"
	segment := episodic.Segment{Role: "assistant", Text: strings.Repeat(sentence, 500), Index: 7}
	units := SegmentUnits("mem:test", "@ref:zcode:s1:m1", segment, 1793102400, true)
	if len(units) < 2 {
		t.Fatalf("expected a long segment to split, got %d unit(s)", len(units))
	}
	for index, unit := range units {
		want := fmt.Sprintf("@ref:zcode:s1:m1#7.%d", index)
		if unit.RefPointer != want {
			t.Fatalf("unit %d pointer = %q, want %q", index, unit.RefPointer, want)
		}
		if unit.SegmentIndex != 7 || unit.ChunkIndex != index {
			t.Fatalf("unit %d coords = (%d,%d), want (7,%d)", index, unit.SegmentIndex, unit.ChunkIndex, index)
		}
	}
	// A segment whose turn cannot be addressed still carries no pointer: its text
	// has to be stored locally (no source to resolve live).
	local := SegmentUnits("mem:test", "", segment, 1793102400, true)
	for _, unit := range local {
		if unit.RefPointer != "" {
			t.Fatalf("unaddressable segment must not carry a pointer, got %q", unit.RefPointer)
		}
	}
}
