// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package facade_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rocareer/rolling-memory/internal/memory/facade"
	"github.com/rocareer/rolling-memory/internal/memory/localvault"
)

func TestSecurityScopeRejectedBeforeImportAndGraphRecall(t *testing.T) {
	memory := newMemory(t)
	if _, err := memory.LocalVault(); err != nil {
		t.Fatal(err)
	}
	other := "mem:other"
	path := writeTranscript(t, "scope.txt", "scope witness\n")
	var before int
	if err := memory.Store.DB().QueryRow(`SELECT count(*) FROM rm_docs`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := memory.ImportFile(path, &other, nil, "shared", false); err == nil {
		t.Fatal("cross-scope import accepted")
	}
	var after int
	if err := memory.Store.DB().QueryRow(`SELECT count(*) FROM rm_docs`).Scan(&after); err != nil || after != before {
		t.Fatalf("rejected import changed docs: before=%d after=%d err=%v", before, after, err)
	}
	if result, err := memory.Recall(facade.RecallRequest{Cue: "scope witness", Scope: &other}); err == nil || result != nil {
		t.Fatalf("cross-scope recall = %#v, %v", result, err)
	}
}

func TestSecurityRestrictedFileImportFailsBeforeWrites(t *testing.T) {
	memory := newMemory(t)
	if _, err := memory.LocalVault(); err != nil {
		t.Fatal(err)
	}
	path := writeTranscript(t, "private.txt", "private imported witness\n")
	jsonlPath := writeTranscript(t, "private.jsonl",
		`{"type":"user","message":{"role":"user","content":"private imported witness"}}`+"\n"+
			`{"type":"assistant","message":{"role":"assistant","stop_reason":"end_turn","content":"noted"}}`+"\n")
	for _, audience := range []string{"private", " PRIVATE ", "group", " GROUP ", "unknown", " ", "SHARED"} {
		result, err := memory.ImportFile(path, nil, nil, audience, false)
		if err == nil || result != nil || !strings.Contains(err.Error(), "background transcript scanning cannot enforce file privacy") {
			t.Fatalf("private import = %#v, %v", result, err)
		}
	}
	var docs int
	if err := memory.Store.DB().QueryRow(`SELECT COUNT(*) FROM rm_docs`).Scan(&docs); err != nil {
		t.Fatal(err)
	}
	if docs != 0 {
		t.Fatalf("private import wrote data: docs=%d", docs)
	}
	if _, err := memory.ImportFile(jsonlPath, nil, nil, "shared", false); err != nil {
		t.Fatal(err)
	}
	var beforeDocs int
	if err := memory.Store.DB().QueryRow(`SELECT COUNT(*) FROM rm_docs`).Scan(&beforeDocs); err != nil {
		t.Fatal(err)
	}
	if _, err := memory.ImportFile(jsonlPath, nil, nil, "private", false); err == nil {
		t.Fatal("private import accepted an existing shared source")
	}
	var afterDocs int
	if err := memory.Store.DB().QueryRow(`SELECT COUNT(*) FROM rm_docs`).Scan(&afterDocs); err != nil || beforeDocs != afterDocs || beforeDocs != 2 {
		t.Fatalf("private rejection altered existing data: before=%d after=%d err=%v", beforeDocs, afterDocs, err)
	}
}

func TestSecurityWholeTurnPointerIsNotDelivered(t *testing.T) {
	memory := newMemory(t)
	client, err := memory.LocalVault()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "source#metadata.jsonl")
	body := `{"type":"user","message":{"role":"user","content":"quartz whole turn"}}` + "\n" + `{"type":"assistant","message":{"role":"assistant","content":"whole turn secret"}}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	ref := "@ref:jsonl:" + path + ":0"
	text, err := localvault.ResolveEpisodicPointerLive(ref)
	if err != nil {
		t.Fatal(err)
	}
	packet, _ := client.Vault.PackBlindPacket("ep_whole", text, testNow)
	if err := client.Vault.SaveLocalEpisodicPointerIndexed("ep_whole", testNow, ref, "", text, packet, localvault.IngestResponse{Layer: "episodic"}); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"full", "probe", "recent"} {
		result, err := memory.Recall(facade.RecallRequest{Cue: "quartz whole turn", Mode: mode, NoCrystallize: true})
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

func TestSecurityAlternateSourceRequiresProvenance(t *testing.T) {
	memory := newMemory(t)
	client, err := memory.LocalVault()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	primary := filepath.Join(dir, "primary.jsonl")
	alternate := filepath.Join(dir, "alternate.jsonl")
	body := `{"type":"user","message":{"role":"user","content":"quartz provenance witness"}}` + "\n"
	for _, path := range []string{primary, alternate} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	primaryTurn := "@ref:jsonl:" + primary + ":0"
	alternateTurn := "@ref:jsonl:" + alternate + ":0"
	text := "[user] quartz provenance witness"
	const docID = "ep_provenance"
	packet, _ := client.Vault.PackBlindPacket(docID, text, testNow)
	client.SetIdentity("primary-assistant", "primary-session", "primary-locus")
	if err := client.Vault.SaveLocalEpisodicPointerIndexed(docID, testNow, primaryTurn+"#0", primaryTurn, text, packet, localvault.IngestResponse{Layer: localvault.LayerEpisodic}); err != nil {
		t.Fatal(err)
	}
	client.SetIdentity("alternate-assistant", "alternate-session", "alternate-locus")
	if err := client.Vault.SaveLocalEpisodicPointerIndexed(docID, testNow+100, alternateTurn+"#0", alternateTurn, text, packet, localvault.IngestResponse{Layer: localvault.LayerEpisodic}); err != nil {
		t.Fatal(err)
	}
	req := facade.RecallRequest{Cue: "quartz provenance witness", NoCrystallize: true}
	result, err := memory.Recall(req)
	if err != nil || !strings.Contains(result.Render(10000), text) {
		t.Fatalf("primary source control: %+v %v", result, err)
	}
	if err := os.Remove(primary); err != nil {
		t.Fatal(err)
	}
	if got, actual, err := client.Vault.LoadVerifiedEpisodicPointerSource(docID, primaryTurn+"#0"); err != nil || got != text || actual != alternateTurn+"#0" {
		t.Fatalf("alternate-source control: %q %q %v", got, actual, err)
	}
	result, err = memory.Recall(req)
	if err != nil || len(result.Bands) != 0 || len(result.RawHits) != 0 || result.Gaps["source_unavailable"] != 1 || result.Absent {
		t.Fatalf("unattributed alternate delivered: %+v %v", result, err)
	}
	if err := os.WriteFile(primary, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err = memory.Recall(req)
	if err != nil || !strings.Contains(result.Render(10000), text) || result.Gaps["source_unavailable"] != 0 {
		t.Fatalf("restored primary source control: %+v %v", result, err)
	}
}

func TestSecuritySameTurnRelocationUsesActualSegment(t *testing.T) {
	memory := newMemory(t)
	client, err := memory.LocalVault()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "turn.jsonl")
	original := `{"type":"user","message":{"role":"user","content":"quartz relocation witness"}}` + "\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	turn := "@ref:jsonl:" + path + ":0"
	text := "[user] quartz relocation witness"
	const docID = "ep_relocation"
	packet, _ := client.Vault.PackBlindPacket(docID, text, testNow)
	if err := client.Vault.SaveLocalEpisodicPointerIndexed(docID, testNow, turn+"#0", turn, text, packet, localvault.IngestResponse{Layer: localvault.LayerEpisodic}); err != nil {
		t.Fatal(err)
	}
	changed := `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"unindexed secret"},{"type":"text","text":"quartz relocation witness"}]}}` + "\n"
	if err := os.WriteFile(path, []byte(changed), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, actual, err := client.Vault.LoadVerifiedEpisodicPointerSource(docID, turn+"#0"); err != nil || got != text || actual != turn+"#1" {
		t.Fatalf("same-turn relocation control: %q %q %v", got, actual, err)
	}
	result, err := memory.Recall(facade.RecallRequest{Cue: "quartz relocation witness", NoCrystallize: true})
	if err != nil || len(result.Bands) != 1 || result.Gaps["source_unavailable"] != 0 {
		t.Fatalf("relocated recall: %+v %v", result, err)
	}
	nodes := result.Bands[0]["nodes"].([]map[string]any)
	window, _ := nodes[0]["segment_window"].([]int)
	if len(window) != 2 || window[0] != 1 || window[1] != 1 || nodes[0]["content"] != text {
		t.Fatalf("wrong source segment delivered: %+v", nodes[0])
	}
	if payload, err := json.Marshal(result.ToDict()); err != nil || strings.Contains(string(payload), "unindexed secret") || strings.Contains(string(payload), path) {
		t.Fatalf("unverified segment or pointer leaked: %s %v", payload, err)
	}
}

func TestSecurityLineageDoesNotAuthorizeAdjacentHit(t *testing.T) {
	memory := newMemory(t)
	client, err := memory.LocalVault()
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
		if err := client.Vault.SaveLocalEpisodicPointerIndexed(row.id, testNow, row.ref, turn, row.text, packet, localvault.IngestResponse{Layer: "episodic"}); err != nil {
			t.Fatal(err)
		}
	}
	result, err := memory.Recall(facade.RecallRequest{Cue: "quartz nebula evidence", NoCrystallize: true})
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
	if !strings.Contains(string(payload), "quartz nebula evidence") || strings.Contains(string(payload), "ep_secret") {
		t.Fatalf("selected evidence missing or unrelated sibling became addressable: %s", payload)
	}
}
