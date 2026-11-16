// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package api_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rocareer/rolling-memory/internal/api"
	"github.com/rocareer/rolling-memory/internal/thinclient"
)

func TestSecurityScopeRejectedBeforeImportAndGraphRecall(t *testing.T) {
	memory := newMemory(t)
	other := "mem:other"
	path := writeTranscript(t, "scope.txt", "scope witness\n")
	var before int
	if err := memory.Store.DB().QueryRow(`SELECT count(*) FROM trace`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := memory.ImportFile(path, &other, nil, "shared", false); err == nil {
		t.Fatal("cross-scope import accepted")
	}
	var after int
	if err := memory.Store.DB().QueryRow(`SELECT count(*) FROM trace`).Scan(&after); err != nil || after != before {
		t.Fatalf("rejected import changed traces: before=%d after=%d err=%v", before, after, err)
	}
	if result, err := memory.Recall(api.RecallRequest{Cue: "scope witness", Scope: &other}); err == nil || result != nil {
		t.Fatalf("cross-scope graph recall = %#v, %v", result, err)
	}
}

func TestSecurityPrivateImportNotMirroredIntoVault(t *testing.T) {
	memory := newMemory(t)
	path := writeTranscript(t, "private.txt", "private imported witness\n")
	if _, err := memory.ImportFile(path, nil, nil, "private", false); err != nil {
		t.Fatal(err)
	}
	var traces, docs int
	if err := memory.Store.DB().QueryRow(`SELECT count(*) FROM trace WHERE audience = 'private'`).Scan(&traces); err != nil {
		t.Fatal(err)
	}
	if err := memory.Store.DB().QueryRow(`SELECT count(*) FROM thin_vault_docs`).Scan(&docs); err != nil {
		t.Fatal(err)
	}
	if traces != 1 || docs != 0 {
		t.Fatalf("private trace preservation/mirror = %d/%d", traces, docs)
	}
}

func TestSecurityWholeTurnPointerIsNotDelivered(t *testing.T) {
	memory := newMemory(t)
	client, err := memory.ThinClient()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "source#metadata.jsonl")
	body := `{"type":"user","message":{"role":"user","content":"quartz whole turn"}}` + "\n" + `{"type":"assistant","message":{"role":"assistant","content":"whole turn secret"}}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	ref := "@ref:jsonl:" + path + ":0"
	text, err := thinclient.ResolveEpisodicPointerLive(ref)
	if err != nil {
		t.Fatal(err)
	}
	packet, _ := client.Vault.PackBlindPacket("ep_whole", text, testNow)
	if err := client.Vault.SaveLocalEpisodicPointerIndexed("ep_whole", testNow, ref, "", text, packet, thinclient.CloudIngestResponse{Layer: "episodic"}); err != nil {
		t.Fatal(err)
	}
	thinclient.PurgeLegacyTables(memory.Store.DB())
	for _, mode := range []string{"full", "probe", "recent"} {
		result, err := memory.Recall(api.RecallRequest{Cue: "quartz whole turn", Mode: mode, NoCrystallize: true})
		if err != nil {
			t.Fatal(err)
		}
		payload, err := json.Marshal(result.ToDict())
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(payload), "whole turn secret") || strings.Contains(string(payload), path) || strings.Contains(string(payload), "@ref:") {
			t.Errorf("%s delivered whole-turn source: %s", mode, payload)
		}
	}
}

func TestSecurityLineageDoesNotAuthorizeAdjacentHit(t *testing.T) {
	memory := newMemory(t)
	client, err := memory.ThinClient()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "sensitive.jsonl")
	body := `{"type":"user","message":{"role":"user","content":"quartz nebula evidence"}}` + "\n" + `{"type":"assistant","message":{"role":"assistant","content":"unrelated confidential sibling"}}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	turn := "@ref:jsonl:" + path + ":0"
	for _, row := range []struct{ id, ref, text string }{
		{"ep_quartz", turn + "#0", "[user] quartz nebula evidence"},
		{"ep_secret", turn + "#1", "[assistant] unrelated confidential sibling"},
	} {
		packet, _ := client.Vault.PackBlindPacket(row.id, row.text, testNow)
		if err := client.Vault.SaveLocalEpisodicPointerIndexed(row.id, testNow, row.ref, turn, row.text, packet, thinclient.CloudIngestResponse{Layer: "episodic"}); err != nil {
			t.Fatal(err)
		}
	}
	thinclient.PurgeLegacyTables(memory.Store.DB())
	result, err := memory.Recall(api.RecallRequest{Cue: "quartz nebula evidence", NoCrystallize: true})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(result.ToDict())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), "confidential sibling") || strings.Contains(string(payload), path) || strings.Contains(string(payload), "@ref:") {
		t.Fatalf("unselected sibling or source pointer delivered: %s", payload)
	}
	if !strings.Contains(string(payload), "quartz nebula evidence") {
		t.Fatalf("selected evidence missing: %s", payload)
	}
}
