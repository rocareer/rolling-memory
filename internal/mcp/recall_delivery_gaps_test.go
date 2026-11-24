// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rocareer/rolling-memory/internal/memory/localvault"
)

func TestLocalRecallMCPTextReportsIndexAndSourceGaps(t *testing.T) {
	for _, tc := range []struct {
		name     string
		mutation string
		remove   bool
		want     string
	}{
		{name: "missing index", mutation: `DELETE FROM rm_episodic_index`, want: "情景索引不完整 1"},
		{name: "missing source", remove: true, want: "来源不可回源 1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ROLLING_MEMORY_BRAIN_URL", "")
			memory := newVaultMemory(t)
			client, err := memory.LocalVault()
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "source.jsonl")
			if err := os.WriteFile(path, []byte(`{"type":"user","message":{"role":"user","content":"quartz nebula evidence"}}`+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			text := "[user] quartz nebula evidence"
			packet, _ := client.Vault.PackBlindPacket("ep_gap", text, testNow)
			if err := client.Vault.SaveLocalEpisodicPointerIndexed("ep_gap", testNow, "@ref:jsonl:"+path+":0#0", "", text, packet, localvault.IngestResponse{Layer: localvault.LayerEpisodic}); err != nil {
				t.Fatal(err)
			}
			if tc.mutation != "" {
				if _, err := memory.Store.DB().Exec(tc.mutation); err != nil {
					t.Fatal(err)
				}
			}
			if tc.remove {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
			responses := runSession(t, memory, toolCallMessage(1, "recall", map[string]any{"cue": "quartz nebula evidence", "mode": "full"}))
			if len(responses) != 1 || isError(t, responses[0]) {
				t.Fatalf("unexpected response: %#v", responses)
			}
			rendered := textOf(t, responses[0])
			if !strings.Contains(rendered, tc.want) || !strings.Contains(rendered, `absent="false"`) {
				t.Fatalf("missing gap or incorrect absence: %s", rendered)
			}
			if strings.Contains(rendered, path) || strings.Contains(rendered, "@ref:") {
				t.Fatalf("source pointer leaked: %s", rendered)
			}
		})
	}
}

func TestLocalRecallMCPTextReportsCharacterTruncation(t *testing.T) {
	t.Setenv("ROLLING_MEMORY_BRAIN_URL", "")
	memory := newVaultMemory(t)
	client, err := memory.LocalVault()
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"chars-a", "chars-b", "chars-c"} {
		if err := client.Vault.SaveLocalPlaintext(id, testNow, "quartz evidence "+id+" "+strings.Repeat("sample ", 100), nil, localvault.IngestResponse{Layer: localvault.LayerEpisodic}); err != nil {
			t.Fatal(err)
		}
	}
	memory.Config.Recall.Chars = 200
	responses := runSession(t, memory, toolCallMessage(1, "recall", map[string]any{"cue": "quartz evidence", "mode": "full"}))
	if len(responses) != 1 || isError(t, responses[0]) {
		t.Fatalf("unexpected response: %#v", responses)
	}
	rendered := textOf(t, responses[0])
	if !strings.Contains(rendered, "已截断") {
		t.Fatalf("missing character truncation notice: %s", rendered)
	}
}

func TestLocalRecallMCPTextReportsPagedResults(t *testing.T) {
	t.Setenv("ROLLING_MEMORY_BRAIN_URL", "")
	memory := newVaultMemory(t)
	client, err := memory.LocalVault()
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"caps-a", "caps-b", "caps-c"} {
		if err := client.Vault.SaveLocalPlaintext(id, testNow, "quartz evidence "+id, nil, localvault.IngestResponse{Layer: localvault.LayerEpisodic}); err != nil {
			t.Fatal(err)
		}
	}
	responses := runSession(t, memory, toolCallMessage(1, "recall", map[string]any{"cue": "quartz evidence", "offset": 1}))
	if len(responses) != 1 || isError(t, responses[0]) {
		t.Fatalf("unexpected response: %#v", responses)
	}
	rendered := textOf(t, responses[0])
	if !strings.Contains(rendered, "部分结果未显示") || !strings.Contains(rendered, "offset") || strings.Contains(rendered, "caps /") {
		t.Fatalf("incorrect pagination notice: %s", rendered)
	}
}
