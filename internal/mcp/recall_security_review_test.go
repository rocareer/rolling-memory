// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rocareer/rolling-memory/internal/thinclient"
)

// TestRecallDeliverySecurity exercises the real MCP dispatch and verifies that
// source references and content without indexed identity never cross its boundary.
func TestRecallDeliverySecurity(t *testing.T) {
	for _, live := range []bool{false, true} {
		for _, scenario := range []string{"available", "unavailable", "unindexed-neighbor", "changed-source"} {
			for _, mode := range []string{"full", "probe", "recent"} {
				name := mode + "/" + scenario
				if live {
					name = "live/" + name
				} else {
					name = "structured/" + name
				}
				t.Run(name, func(t *testing.T) {
					t.Setenv("ROLLING_MEMORY_BRAIN_URL", "")
					var lazy *LazyMemory
					memory := newMemory(t)
					if live {
						lazy = NewLazyMemory()
						lazy.memory = memory
					}
					client, err := memory.ThinClient()
					if err != nil {
						t.Fatal(err)
					}
					path := filepath.Join(t.TempDir(), "sensitive-source.jsonl")
					source := `{"type":"user","message":{"role":"user","content":"quartz nebula evidence"}}` + "\n"
					if scenario == "unindexed-neighbor" {
						source += `{"type":"assistant","message":{"role":"assistant","content":"unindexed sibling secret"}}` + "\n"
					}
					if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
						t.Fatal(err)
					}
					text := "[user] quartz nebula evidence"
					packet, _ := client.Vault.PackBlindPacket("ep_security", text, testNow)
					if err := client.Vault.SaveLocalEpisodicPointerIndexed("ep_security", testNow, "@ref:jsonl:"+path+":0#0", "", text, packet, thinclient.CloudIngestResponse{Layer: thinclient.LayerEpisodic}); err != nil {
						t.Fatal(err)
					}
					if scenario == "unavailable" {
						if err := os.Remove(path); err != nil {
							t.Fatal(err)
						}
					}
					if scenario == "changed-source" {
						if err := os.WriteFile(path, []byte(`{"type":"user","message":{"role":"user","content":"tampered source secret"}}`+"\n"), 0o600); err != nil {
							t.Fatal(err)
						}
					}
					thinclient.PurgeLegacyTables(memory.Store.DB())
					request := toolCallMessage(1, "recall", map[string]any{"cue": "quartz nebula evidence", "mode": mode, "reader": "deliverable"})
					var response any
					if live {
						response = handle(request, nil, lazy, nil)
					} else {
						responses := runSession(t, memory, request)
						if len(responses) != 1 {
							t.Fatalf("unexpected responses: %#v", responses)
						}
						response = responses[0]
					}
					if isError(t, response) {
						t.Fatalf("recall failed: %#v", response)
					}
					payload, err := json.Marshal(response)
					if err != nil {
						t.Fatal(err)
					}
					if strings.Contains(string(payload), path) || strings.Contains(string(payload), "@ref:") {
						t.Errorf("source pointer crossed MCP boundary: %s", payload)
					}
					if strings.Contains(string(payload), "unindexed sibling secret") || strings.Contains(string(payload), "tampered source secret") {
						t.Errorf("unauthorized source crossed MCP boundary: %s", payload)
					}
					if scenario != "unavailable" && scenario != "changed-source" && !strings.Contains(string(payload), "quartz nebula evidence") {
						t.Errorf("verified evidence missing: %s", payload)
					}
					_, structured := resultOf(t, response)["structuredContent"]
					if structured != (!live || mode == "probe") {
						t.Errorf("structuredContent present = %t", structured)
					}
				})
			}
		}
	}
}

func TestRecallDeliveryReportsUpstreamTextTruncation(t *testing.T) {
	t.Setenv("ROLLING_MEMORY_BRAIN_URL", "")
	memory := newMemory(t)
	client, err := memory.ThinClient()
	if err != nil {
		t.Fatal(err)
	}
	text := "quartz nebula evidence " + strings.Repeat("filler ", 1000)
	if err := client.Vault.SaveLocalPlaintext("ep_long", testNow, text, nil, thinclient.CloudIngestResponse{Layer: thinclient.LayerEpisodic}); err != nil {
		t.Fatal(err)
	}
	thinclient.PurgeLegacyTables(memory.Store.DB())
	responses := runSession(t, memory, toolCallMessage(1, "recall", map[string]any{"cue": "quartz nebula evidence", "mode": "full", "caps": map[string]any{"chars": 1000}}))
	if len(responses) != 1 || isError(t, responses[0]) {
		t.Fatalf("unexpected response: %#v", responses)
	}
	rendered := textOf(t, responses[0])
	if !strings.Contains(rendered, "quartz nebula evidence") {
		t.Fatalf("missing evidence: %s", rendered)
	}
	if !strings.Contains(rendered, "已截断") && !strings.Contains(rendered, "预算裁剪") && !strings.Contains(rendered, "预算截止") {
		t.Fatalf("missing upstream content truncation notice: %s", rendered)
	}
}
