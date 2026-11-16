// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package api_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rocareer/rolling-memory/internal/api"
	"github.com/rocareer/rolling-memory/internal/thinclient"
)

// A turn band must show what the user asked even when retrieval only matched a later
// assistant segment: the turn's own stored segment `#0` (the opening user message) is
// hydrated from the verified index and prepended ahead of the delivered hit window.
func TestLocalRecallEpisodicTurnKeepsUserPromptOutsideHitWindow(t *testing.T) {
	memory := newMemory(t)
	client, err := memory.ThinClient()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "turn.jsonl")
	lines := strings.Join([]string{
		`{"type":"user","message":{"role":"user","content":"请只作答：用户首段标记 zebra-prompt"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":"quartz answer beta evidence 已按问题收口"}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	turnRef := "@ref:jsonl:" + path + ":0"
	segments := []struct {
		id   string
		ptr  string
		text string
	}{
		{"ep_turn_prompt", turnRef + "#0", "[user] 请只作答：用户首段标记 zebra-prompt"},
		{"ep_turn_answer", turnRef + "#1", "[assistant] quartz answer beta evidence 已按问题收口"},
	}
	for index, segment := range segments {
		packet, _ := client.Vault.PackBlindPacket(segment.id, segment.text, testNow+int64(index))
		if err := client.Vault.SaveLocalEpisodicPointerIndexed(segment.id, testNow+int64(index), segment.ptr, turnRef, segment.text, packet, thinclient.CloudIngestResponse{Layer: "episodic"}); err != nil {
			t.Fatalf("save %s: %v", segment.id, err)
		}
	}
	thinclient.PurgeLegacyTables(memory.Store.DB())

	// The cue matches only the assistant segment; the prompt unit must stay outside the
	// hit window and still be delivered as the turn's provenance head.
	result, err := memory.Recall(api.RecallRequest{Cue: "quartz answer beta evidence", NoCrystallize: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Bands) == 0 {
		t.Fatalf("expected a turn band, got none (raw=%d)", len(result.RawHits))
	}
	nodes, _ := result.Bands[0]["nodes"].([]map[string]any)
	if len(nodes) == 0 {
		t.Fatalf("band without nodes: %+v", result.Bands[0])
	}
	content, _ := nodes[0]["content"].(string)
	if !strings.Contains(content, "zebra-prompt") {
		t.Fatalf("delivered band must carry the user prompt, got: %s", content)
	}
	window, _ := nodes[0]["segment_window"].([]int)
	if len(window) != 2 || window[0] == 0 {
		t.Fatalf("prompt must come from the turn head, not the hit window: %v", nodes[0]["segment_window"])
	}
	if rendered := result.Render(6000); !strings.Contains(rendered, "[user]") {
		t.Fatalf("rendered recall must show the user line: %s", rendered)
	}
}
