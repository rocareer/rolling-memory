// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package mcp

import (
	"strings"
	"testing"

	"github.com/rocareer/rolling-memory/internal/episodicunit"
	"github.com/rocareer/rolling-memory/internal/memory/localvault"
)

func TestReadConversationSourceWithoutSearch(t *testing.T) {
	memory := newVaultMemory(t)
	client, err := memory.LocalVault()
	if err != nil {
		t.Fatal(err)
	}
	body := "[assistant] Preserve this first line.\n\n  保留缩进与换行。"
	id := episodicunit.DocID(memory.Config.Scope, body)
	foreign := episodicunit.DocID("mem:other", body)
	changed := episodicunit.DocID(memory.Config.Scope, "[assistant] Original source")
	for _, key := range []string{id, foreign, changed} {
		if err := client.Vault.SaveLocalPlaintext(key, testNow, body, nil, localvault.IngestResponse{Layer: localvault.LayerEpisodic}); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name      string
		args      map[string]any
		wantError bool
	}{
		{"source", map[string]any{"unit_id": id}, false},
		{"different scope", map[string]any{"unit_id": foreign}, true},
		{"different content", map[string]any{"unit_id": changed}, true},
		{"unknown ID", map[string]any{"unit_id": "ep_" + strings.Repeat("0", 24)}, true},
		{"raw pointer", map[string]any{"unit_id": "@ref:jsonl:/tmp/private.jsonl:0#0"}, true},
		{"mixed search", map[string]any{"unit_id": id, "cue": "query"}, true},
		{"scope override", map[string]any{"unit_id": id, "scope": "mem:other"}, true},
		{"synthesized ID", map[string]any{"unit_id": "nt_" + strings.Repeat("0", 24)}, true},
		{"null ID", map[string]any{"unit_id": nil}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := runSession(t, memory, toolCallMessage(1, "recall", tc.args))[0]
			if isError(t, got) != tc.wantError {
				t.Fatalf("response: %+v", got)
			}
			text := textOf(t, got)
			if tc.wantError {
				if strings.Contains(text, body) || strings.Contains(text, "private.jsonl") {
					t.Fatalf("error disclosed source data: %q", text)
				}
			} else if _, gotBody, ok := strings.Cut(text, "\n\n"); !ok || gotBody != body {
				t.Fatalf("source changed: %q", text)
			}
		})
	}
	var count int
	if err := memory.Store.DB().QueryRow("SELECT count(*) FROM rm_noetic").Scan(&count); err != nil || count != 0 {
		t.Fatalf("source reads changed the recall trace count: %d (%v)", count, err)
	}
}
