// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package eval

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLocalIndexSurfaceSelection(t *testing.T) {
	cases := []struct {
		tokenizer string
		want      string
	}{
		{PulseTokenizerFTS5BM25, LocalIndexSurfaceProduct},
		{PulseTokenizerStandard, LocalIndexSurfaceEval},
		{PulseTokenizerFTS5Trigram, LocalIndexSurfaceEval},
	}
	for _, tc := range cases {
		engine := &CloudEngine{PulseMode: PulseModeLocalPlain, PulseTokenizer: tc.tokenizer}
		if got := engine.localIndexSurface(); got != tc.want {
			t.Fatalf("tokenizer %q surface = %q, want %q", tc.tokenizer, got, tc.want)
		}
	}
}

func TestProductSurfaceMetadata(t *testing.T) {
	t.Setenv("ROLLING_MEMORY_LANGPACK", "")
	product := &CloudEngine{PulseMode: PulseModeLocalPlain, PulseTokenizer: PulseTokenizerFTS5BM25}
	meta := product.Metadata()
	if meta["local_index_surface"] != "product-maxcap" ||
		meta["local_recall_surface"] != "thinclient.recall-with-options" ||
		meta["product_path_equivalent"] != true {
		t.Fatalf("product metadata = %+v", meta)
	}
	standard := &CloudEngine{PulseMode: PulseModeLocalPlain, PulseTokenizer: PulseTokenizerStandard}
	standardMeta := standard.Metadata()
	if standardMeta["product_path_equivalent"] != false ||
		standardMeta["local_recall_surface"] != "vault.search-client-sdk-default" {
		t.Fatalf("standard ablation metadata = %+v", standardMeta)
	}
}

func writeLocalPlainStoreFixture(t *testing.T, docs map[string]map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(docs)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "eval-atm-local-plain.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestProductSurfaceRecallsWithVaultReuse(t *testing.T) {
	storePath := writeLocalPlainStoreFixture(t, map[string]map[string]any{
		"d1": {"doc_id": "d1", "occurred_at": int64(100), "raw_text": "alpha beta gamma shared body"},
		"d2": {"doc_id": "d2", "occurred_at": int64(200), "raw_text": "delta epsilon zeta shared body"},
		"d3": {"doc_id": "d3", "occurred_at": int64(300), "raw_text": "eta theta iota shared body"},
	})
	engine := &CloudEngine{
		StorePath: storePath, PulseMode: PulseModeLocalPlain,
		PulseTokenizer: PulseTokenizerFTS5BM25, UserID: "10",
	}
	vault, err := engine.ensureLocalPlainVault()
	if err != nil || vault == nil {
		t.Fatalf("ensureLocalPlainVault: vault=%v err=%v", vault, err)
	}
	hits, err := engine.recallLocalProduct(context.Background(), vault, "alpha beta", 3)
	if err != nil {
		t.Fatalf("recallLocalProduct: %v", err)
	}
	if len(hits) == 0 || hits[0].UnitID != "d1" {
		t.Fatalf("product recall top hit = %+v, want d1", hits)
	}

	metaPath := storePath + ".local-vault.sqlite.meta.json"
	metaRaw, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatalf("vault meta missing: %v", err)
	}
	var meta localVaultMeta
	if err := json.Unmarshal(metaRaw, &meta); err != nil {
		t.Fatal(err)
	}
	if meta.IndexSurface != LocalIndexSurfaceProduct || meta.Documents != 3 || meta.Langpack == "" ||
		meta.Ingestion != "product-client" {
		t.Fatalf("vault meta = %+v, want product/3/langpack/product-client", meta)
	}
	// 产品摄入必须写入句子帧；裸插入（sentences_json='[]'）会丢产品句窗能力。
	sidecar, err := sql.Open("sqlite", storePath+".local-vault.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	defer sidecar.Close()
	var withSentences int
	if err := sidecar.QueryRow(`SELECT COUNT(*) FROM thin_vault_docs WHERE sentences_json != '[]'`).Scan(&withSentences); err != nil {
		t.Fatal(err)
	}
	if withSentences != 3 {
		t.Fatalf("product ingest wrote %d/3 sentence frames", withSentences)
	}
	before, err := os.Stat(metaPath)
	if err != nil {
		t.Fatal(err)
	}

	// 第二个引擎（模拟 Admin 逐题进程）必须复用派生金库，而不是重灌。
	other := &CloudEngine{
		StorePath: storePath, PulseMode: PulseModeLocalPlain,
		PulseTokenizer: PulseTokenizerFTS5BM25, UserID: "10",
	}
	if _, err := other.ensureLocalPlainVault(); err != nil {
		t.Fatalf("second ensureLocalPlainVault: %v", err)
	}
	after, err := os.Stat(metaPath)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Fatal("vault meta rewritten: second engine rebuilt instead of reusing the derived vault")
	}
}
