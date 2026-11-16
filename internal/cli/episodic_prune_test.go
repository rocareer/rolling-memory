// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rocareer/rolling-memory/internal/api"
	"github.com/rocareer/rolling-memory/internal/config"
	"github.com/rocareer/rolling-memory/internal/thinclient"
)

// TestEpisodicPruneRetiresAbsentUnitsOnly pins the bounded prune contract: only a
// unit whose content a successfully enumerated turn no longer yields is removed;
// units still present in the turn (relocatable) and units whose source is
// unavailable are kept, and every removal lands in the receipt file.
func TestEpisodicPruneRetiresAbsentUnitsOnly(t *testing.T) {
	home := t.TempDir()
	// Receipts land under the state dir: keep the test out of the live one.
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	source := filepath.Join(home, "transcript.jsonl")
	original := `{"type":"user","message":{"role":"user","content":"开始。"}}` + "\n" +
		`{"type":"assistant","message":{"role":"assistant","content":"回答一。"}}` + "\n" +
		`{"type":"assistant","message":{"role":"assistant","content":"回答二。"}}` + "\n"
	if err := os.WriteFile(source, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	goneSource := filepath.Join(home, "gone.jsonl")
	if err := os.WriteFile(goneSource, []byte(`{"type":"user","message":{"role":"user","content":"稍后删除。"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	memory, err := api.New(&config.Config{DB: filepath.Join(home, "memory.db"), Scope: "mem:prune", Mode: "local"})
	if err != nil {
		t.Fatal(err)
	}
	defer memory.Close()
	tc, err := memory.ThinClient()
	if err != nil || tc == nil || tc.Vault == nil {
		t.Fatalf("thin client: %v", err)
	}
	turnRef := "@ref:jsonl:" + source + ":0"
	type unit struct{ id, ref, text string }
	units := []unit{
		{"kept", turnRef + "#2", ""},
		{"dropped", turnRef + "#1", ""},
		{"missing", "@ref:jsonl:" + goneSource + ":0#0", ""},
	}
	for index := range units {
		text, rErr := thinclient.ResolveEpisodicPointerLive(units[index].ref)
		if rErr != nil || strings.TrimSpace(text) == "" {
			t.Fatalf("resolve %s: %q %v", units[index].ref, text, rErr)
		}
		units[index].text = text
		if _, rErr := tc.RememberEpisodicByRef(context.Background(), units[index].id, units[index].ref, turnRef, text, 1); rErr != nil {
			t.Fatal(rErr)
		}
	}
	if err := tc.RememberNoetic("nt_alive", "回答二查询", []string{"kept"}, []string{"回答二。"}, "回答二。", 1); err != nil {
		t.Fatal(err)
	}
	if err := tc.RememberNoetic("nt_orphan", "已删旁白查询", []string{"dropped"}, []string{"回答一。"}, "回答一。", 1); err != nil {
		t.Fatal(err)
	}
	if err := tc.RememberNoetic("nt_probe", "daemon warmup 0100", []string{"kept"}, []string{"回答二。"}, "回答二。", 1); err != nil {
		t.Fatal(err)
	}
	// The turn re-enumerates without 回答一 (an older filter imported it) and the
	// second source disappears entirely.
	if err := os.WriteFile(source, []byte(`{"type":"user","message":{"role":"user","content":"开始。"}}`+"\n"+
		`{"type":"assistant","message":{"role":"assistant","content":"回答二。"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(goneSource); err != nil {
		t.Fatal(err)
	}

	dry, err := pruneEpisodicUnits(context.Background(), memory, 100, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if dry.Mode != "dry_run" || dry.Scanned != 3 || dry.Absent != 1 || dry.Relocatable != 1 || dry.Unreadable != 1 || dry.Removed != 0 {
		t.Fatalf("dry run = %+v", dry)
	}
	if len(dry.Candidates) != 1 || !strings.HasPrefix(dry.Candidates[0], "dropped\t") {
		t.Fatalf("candidates = %+v", dry.Candidates)
	}
	var count int
	if err := memory.Store.DB().QueryRow(`SELECT count(*) FROM thin_vault_docs WHERE doc_id='dropped'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("dry run must not delete: count=%d err=%v", count, err)
	}
	if _, err := pruneEpisodicUnits(context.Background(), memory, 100, true, false); err == nil {
		t.Fatal("apply without --yes must fail")
	}
	applied, err := pruneEpisodicUnits(context.Background(), memory, 100, true, true)
	if err != nil {
		t.Fatal(err)
	}
	if applied.Mode != "apply" || applied.Removed != 1 || applied.Kept != 0 || applied.NoeticRemoved != 2 {
		t.Fatalf("apply = %+v", applied)
	}
	if err := memory.Store.DB().QueryRow(`SELECT count(*) FROM thin_vault_noetic WHERE noetic_id='nt_alive'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("alive noetic row must be kept: count=%d err=%v", count, err)
	}
	if err := memory.Store.DB().QueryRow(`SELECT count(*) FROM thin_vault_noetic WHERE noetic_id IN ('nt_orphan','nt_probe')`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("orphan/probe noetic rows survived: count=%d err=%v", count, err)
	}
	if err := memory.Store.DB().QueryRow(`SELECT count(*) FROM thin_vault_docs WHERE doc_id='dropped'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("dropped row survived: count=%d err=%v", count, err)
	}
	if err := memory.Store.DB().QueryRow(`SELECT count(*) FROM thin_vault_episodic_index WHERE doc_id='dropped'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("dropped index survived: count=%d err=%v", count, err)
	}
	if err := memory.Store.DB().QueryRow(`SELECT count(*) FROM thin_vault_episodic_lineage WHERE doc_id='dropped'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("dropped lineage survived: count=%d err=%v", count, err)
	}
	for _, id := range []string{"kept", "missing"} {
		if err := memory.Store.DB().QueryRow(`SELECT count(*) FROM thin_vault_docs WHERE doc_id=?`, id).Scan(&count); err != nil || count != 1 {
			t.Fatalf("%s must be kept: count=%d err=%v", id, count, err)
		}
	}
	var healedPtr string
	if err := memory.Store.DB().QueryRow(`SELECT raw_text FROM thin_vault_docs WHERE doc_id='kept'`).Scan(&healedPtr); err != nil || healedPtr != turnRef+"#1" {
		t.Fatalf("relocatable unit was not healed in-place: got %q err=%v", healedPtr, err)
	}
	receipt, err := os.ReadFile(applied.Receipt)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(receipt), `"doc_id":"dropped"`) || !strings.Contains(string(receipt), turnRef+"#1") {
		t.Fatalf("receipt = %s", receipt)
	}

	// Second pass with includeUnreadable retires the gone transcript pointer while
	// keeping the healed unit verified.
	withUnreadable, err := pruneEpisodicUnits(context.Background(), memory, 100, true, true, true)
	if err != nil {
		t.Fatal(err)
	}
	if withUnreadable.Verified != 1 || withUnreadable.Unreadable != 1 || withUnreadable.Removed != 1 {
		t.Fatalf("include-unreadable apply = %+v", withUnreadable)
	}
	if err := memory.Store.DB().QueryRow(`SELECT count(*) FROM thin_vault_docs WHERE doc_id='missing'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("missing row survived include-unreadable: count=%d err=%v", count, err)
	}
}
