// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package eval

import (
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/rocareer/rolling-memory/internal/config"
)

// TestATMRecallMetricContract distinguishes gold-set coverage from binary Any-Hit.
func TestATMRecallMetricContract(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("ROLLING_MEMORY_EVAL_PULSE_MODE", "")
	t.Setenv("ROLLING_MEMORY_BRAIN_URL", "")
	dataDir := filepath.Join(root, "data")
	if err := os.MkdirAll(filepath.Join(dataDir, "tiers"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "MANIFEST.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	corpus, err := os.Create(filepath.Join(dataDir, "corpus.jsonl.gz"))
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(corpus)
	if err := json.NewEncoder(gz).Encode(ATMSGMItem{
		ItemID: "email202401010001", Modality: "email", Timestamp: "2024-01-01",
		Summary: "Distinctive lighthouse photograph",
	}); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := corpus.Close(); err != nil {
		t.Fatal(err)
	}
	qas := []ATMQAItem{{
		ID: "q1", Question: "Distinctive lighthouse photograph", QType: "open_end",
		EvidenceIDs: []string{"email202401010001", "email202401010002"},
	}}
	qaJSON, err := json.Marshal(qas)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "tiers", "questions-hard.json"), qaJSON, 0o600); err != nil {
		t.Fatal(err)
	}

	outDir := filepath.Join(root, "out")
	summary, err := RunATM(&config.Config{Mode: "local"}, Options{
		Suite: SuiteATM, DataDir: dataDir, Tier: TierATMHard, NoVerify: true,
		PulseMode: PulseModeLocalPlain, StorePath: filepath.Join(root, "vault.json"),
		OutDir: outDir, Answerer: "none", Judge: "off", Workers: 1,
	})
	if err != nil {
		t.Fatalf("RunATM: %v", err)
	}
	if got := summary["recall_metric"]; got != "gold-set-mean" {
		t.Fatalf("recall_metric = %v, want gold-set-mean", got)
	}
	if got := summary["r_at_10"]; got != 0.5 {
		t.Fatalf("summary R@10 = %v, want 0.5", got)
	}
	if got := summary["any_hit_10"]; got != 1.0 {
		t.Fatalf("summary any_hit_10 = %v, want 1.0", got)
	}
	if got := summary["all_hit_10"]; got != 0.0 {
		t.Fatalf("summary all_hit_10 = %v, want 0.0", got)
	}
	files, err := filepath.Glob(filepath.Join(outDir, "results-*.jsonl"))
	if err != nil || len(files) != 1 {
		t.Fatalf("results files = %v, err = %v", files, err)
	}
	result, err := os.Open(files[0])
	if err != nil {
		t.Fatal(err)
	}
	defer result.Close()
	var row struct {
		RetrievalIDs    []string           `json:"retrieval_ids"`
		RetrievalRecall map[string]float64 `json:"retrieval_recall"`
		EvidenceHit     bool               `json:"evidence_hit"`
		AllHit10        bool               `json:"all_hit_10"`
	}
	if err := json.NewDecoder(result).Decode(&row); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if len(row.RetrievalIDs) != 1 || row.RetrievalIDs[0] != qas[0].EvidenceIDs[0] {
		t.Fatalf("retrieval IDs = %v, want the first gold ID only", row.RetrievalIDs)
	}
	if got := row.RetrievalRecall["R@10"]; got != 0.5 || !row.EvidenceHit || row.AllHit10 {
		t.Fatalf("R@10 = %v, Any-Hit = %v, All-Hit = %v, want 0.5, true, false", got, row.EvidenceHit, row.AllHit10)
	}
}

func TestComputeATMRecallMap_SetDeduplicationAndOfficialFormula(t *testing.T) {
	gtIDs := []string{"g1", "g2", "g3", "g4", "g1", ""}
	// Even if a buggy caller repeats "g1" 10 times in retIDs, official set intersection
	// |set(GT) ∩ set(Top-k)| / |set(GT)| must count "g1" at most once (1/4 = 0.25).
	retDup := []string{"g1", "g1", "g1", "g1", "g1", "g1", "g1", "g1", "g1", "g1"}
	rec := ComputeATMRecallMap(gtIDs, retDup)
	if got := rec["R@10"]; got != 0.25 {
		t.Fatalf("ComputeATMRecallMap with duplicate retIDs: R@10 = %v, want 0.25", got)
	}
	if got := rec["R@1"]; got != 0.25 {
		t.Fatalf("ComputeATMRecallMap R@1 = %v, want 0.25", got)
	}

	retFull := []string{"x1", "g2", "x2", "g4", "g1", "x3", "g3"}
	recFull := ComputeATMRecallMap(gtIDs, retFull)
	if got := recFull["R@5"]; got != 0.75 {
		t.Fatalf("R@5 = %v, want 0.75 (3 of 4 in top-5)", got)
	}
	if got := recFull["R@10"]; got != 1.0 {
		t.Fatalf("R@10 = %v, want 1.0 (all 4 in top-10)", got)
	}
}

func TestComputeATMOfficialCompositeBps_PureOfficialR10Only(t *testing.T) {
	// 0.70 * 0.7717 + 0.30 * 0.4612 = 0.54019 + 0.13836 = 0.67855 -> 6785.5 bps
	got := ComputeATMOfficialCompositeBps(0.7717, 0.4612)
	if got != 6785.5 {
		t.Fatalf("ComputeATMOfficialCompositeBps(0.7717, 0.4612) = %v, want 6785.5", got)
	}
}
