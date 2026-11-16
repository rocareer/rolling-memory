// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package thinclient

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func TestCloneWithSharedSlotCacheSeparatesIdentity(t *testing.T) {
	db, err := sql.Open("sqlite", "file:slot-cache-clone?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	base, err := NewVault(db, []byte("slot-cache-secret"))
	if err != nil {
		t.Fatalf("NewVault: %v", err)
	}
	base.slotCache["DOC-SHARED"] = vaultCachedDocSlots{rawText: "shared blind atoms"}

	first := base.CloneWithSharedSlotCache()
	second := base.CloneWithSharedSlotCache()
	if first == second || first == base {
		t.Fatalf("clones must have independent Vault values")
	}
	first.SetIdentity(VaultIdentity{AssistantID: "assistant-a", IncarnationID: "inc-a", Locus: "locus-a"})
	second.SetIdentity(VaultIdentity{AssistantID: "assistant-b", IncarnationID: "inc-b", Locus: "locus-b"})
	if got := first.Identity(); got.IncarnationID != "inc-a" || got.Locus != "locus-a" {
		t.Fatalf("first identity = %+v", got)
	}
	if got := second.Identity(); got.IncarnationID != "inc-b" || got.Locus != "locus-b" {
		t.Fatalf("second identity = %+v", got)
	}
	if got := base.Identity(); got != (VaultIdentity{}) {
		t.Fatalf("template identity must stay empty, got %+v", got)
	}
	if got := second.slotCache["DOC-SHARED"].rawText; got != "shared blind atoms" {
		t.Fatalf("shared cache entry = %q", got)
	}
	second.slotCache["DOC-SECOND"] = vaultCachedDocSlots{rawText: "visible through shared cache"}
	if _, ok := first.slotCache["DOC-SECOND"]; !ok {
		t.Fatalf("cache write on second clone not visible on first")
	}
}

func TestCorpusSnapshotInvalidatesOnEpisodicPointerAndDirectSQLUpdate(t *testing.T) {
	db, err := sql.Open("sqlite", "file:slot-cache-invalidation?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	v, err := NewVault(db, []byte("slot-cache-invalidation-secret"))
	if err != nil {
		t.Fatalf("NewVault: %v", err)
	}
	if err := v.SaveLocalPlaintext("DOC-1", 1700000000, "Cornwall coastal cliff hiking trail", []string{"Cornwall coastal cliff hiking trail"}, CloudIngestResponse{Layer: "episodic"}); err != nil {
		t.Fatalf("SaveLocalPlaintext: %v", err)
	}
	hits, err := v.SearchClientSDKDefault("Cornwall hiking", 5)
	if err != nil || len(hits) != 1 || hits[0].UnitID != "DOC-1" {
		t.Fatalf("expected DOC-1 hit before pointer migration, got %+v (err=%v)", hits, err)
	}
	// Migrating DOC-1 to a zero-copy @ref: pointer must invalidate both slotCache and corpusSnap.
	if err := v.SaveLocalEpisodicPointer("DOC-1", 1700000000, "@ref:jsonl:/tmp/nonexistent.jsonl:0#0", "@ref:jsonl:/tmp/nonexistent.jsonl:0", CloudIngestResponse{Layer: "episodic"}); err != nil {
		t.Fatalf("SaveLocalEpisodicPointer: %v", err)
	}
	hitsAfterPtr, err := v.SearchClientSDKDefault("Cornwall hiking", 5)
	if err != nil || len(hitsAfterPtr) != 0 {
		t.Fatalf("expected 0 hits after @ref: pointer migration, got %+v (err=%v)", hitsAfterPtr, err)
	}
	// Direct SQL insert on the underlying connection must also invalidate corpusSnap via total_changes().
	if _, err := db.Exec(`INSERT INTO thin_vault_docs(doc_id, layer, occurred_at, raw_text, sentences_json) VALUES ('DOC-2', 'episodic', 1700000100, 'Kyoto bamboo forest shrine', '[]')`); err != nil {
		t.Fatalf("direct SQL insert: %v", err)
	}
	hitsAfterSQL, err := v.SearchClientSDKDefault("Kyoto shrine", 5)
	if err != nil || len(hitsAfterSQL) != 1 || hitsAfterSQL[0].UnitID != "DOC-2" {
		t.Fatalf("expected DOC-2 hit after direct SQL insert, got %+v (err=%v)", hitsAfterSQL, err)
	}
}

func TestCorpusSnapshotInvalidatesOnInPlaceBackgroundChunkUpdate(t *testing.T) {
	db, err := sql.Open("sqlite", "file:bg-chunk-inplace?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	v, err := NewVault(db, []byte("bg-chunk-inplace-secret"))
	if err != nil {
		t.Fatalf("NewVault: %v", err)
	}
	if err := v.ensureBackgroundTables(); err != nil {
		t.Fatalf("ensureBackgroundTables: %v", err)
	}
	pkt1, _ := v.PackBlindPacket("bg:doc1#0", "Porto Douro riverside vintage tram", 1700000000)
	blob1 := encodeAtomSlots(pkt1.Slots)
	if _, err := db.Exec(`INSERT INTO thin_vault_bg_chunks(unit_id, abs_path, rel_path, chunk_index, anchor, occurred_at, inserted_at, address_uri, coord64, waveform_bar, kept_indices_json, slots_json) VALUES ('bg:doc1#0', '/tmp/doc1.md', 'doc1.md', 0, 'L1-5', 1700000000, 1700000000, 'C6:L1:LOCAL#bg:doc1#0', 1, '█', '[]', ?)`, blob1); err != nil {
		t.Fatalf("insert bg chunk: %v", err)
	}
	hits1, err := v.SearchClientSDKDefault("Porto vintage tram", 5)
	if err != nil || len(hits1) != 1 || hits1[0].UnitID != "bg:doc1#0" {
		t.Fatalf("expected bg:doc1#0 hit before edit, got %+v (err=%v)", hits1, err)
	}

	// Simulate an in-place edit where chunk count and rowid remain identical (`ON CONFLICT(unit_id) DO UPDATE`).
	pkt2, _ := v.PackBlindPacket("bg:doc1#0", "Glasgow Kelvingrove organ recital hall", 1700000200)
	blob2 := encodeAtomSlots(pkt2.Slots)
	if _, err := db.Exec(`INSERT INTO thin_vault_bg_chunks(unit_id, abs_path, rel_path, chunk_index, anchor, occurred_at, inserted_at, address_uri, coord64, waveform_bar, kept_indices_json, slots_json) VALUES ('bg:doc1#0', '/tmp/doc1.md', 'doc1.md', 0, 'L1-5', 1700000200, 1700000000, 'C6:L1:LOCAL#bg:doc1#0', 1, '█', '[]', ?) ON CONFLICT(unit_id) DO UPDATE SET occurred_at = excluded.occurred_at, slots_json = excluded.slots_json`, blob2); err != nil {
		t.Fatalf("update bg chunk in place: %v", err)
	}

	hitsOld, err := v.SearchClientSDKDefault("Porto vintage tram", 5)
	if err != nil || len(hitsOld) != 0 {
		t.Fatalf("expected 0 hits for replaced text after in-place bg chunk update, got %+v (err=%v)", hitsOld, err)
	}
	hitsNew, err := v.SearchClientSDKDefault("Glasgow Kelvingrove recital", 5)
	if err != nil || len(hitsNew) != 1 || hitsNew[0].UnitID != "bg:doc1#0" {
		t.Fatalf("expected bg:doc1#0 hit for updated text after in-place bg chunk update, got %+v (err=%v)", hitsNew, err)
	}
}
