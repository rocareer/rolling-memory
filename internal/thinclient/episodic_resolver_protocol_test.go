// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package thinclient

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rocareer/rolling-memory/internal/episodic"
	"github.com/rocareer/rolling-memory/internal/episodicunit"
)

func TestJSONLStreamResolverPointerProtocol(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session:多语种.jsonl")
	long := strings.Repeat("世界。مرحبا. English! ", 5000)
	data := `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"你好"},{"type":"input_text","text":"مرحبا"}]}}` + "\n" +
		`{"payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"` + long + `"},{"type":"text","text":"日本語 Ελληνικά"}]}}` + "\n" +
		`{"type":"user","message":{"role":"user","content":"next"}}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	turns, err := episodic.JSONLTurns(path)
	if err != nil {
		t.Fatal(err)
	}
	chunks := 0
	for _, turn := range turns {
		prefix := fmt.Sprintf("@ref:jsonl:%s:%d", path, turn.Index)
		for _, segment := range turn.Segments {
			units := episodicunit.SegmentUnits("mem:test", prefix, segment, 1, true)
			if len(units) > 1 {
				chunks += len(units)
			}
			for _, unit := range units {
				got, err := ResolveEpisodicPointerLive(unit.RefPointer)
				if err != nil || got != unit.Text {
					t.Fatalf("pointer %s disagrees: got=%q want=%q err=%v", unit.RefPointer, got, unit.Text, err)
				}
			}
		}
	}
	if chunks < 2 {
		t.Fatalf("expected chunked pointer coverage, got %d chunks", chunks)
	}
}
