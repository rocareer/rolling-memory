// plugin: rolling-memory v0.0.58
package rollingmemory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestItemForLiveStreamStripsUnitIDs(t *testing.T) {
	item := EvalQuestionResultItem{
		ID:               "q1",
		EngineTransport:  evalCloudTransport,
		CloudHits:        2,
		RetrievalUnitIDs: []string{"u1", "u2"},
	}
	live := itemForLiveStream(item)
	if len(live.RetrievalUnitIDs) != 0 {
		t.Fatalf("live item must not carry retrieval_unit_ids, got %v", live.RetrievalUnitIDs)
	}
	if live.EngineTransport != evalCloudTransport || live.CloudHits != 2 {
		t.Fatalf("live item must keep scalar provenance, got transport=%q hits=%d", live.EngineTransport, live.CloudHits)
	}
	if len(item.RetrievalUnitIDs) != 2 {
		t.Fatalf("source item must not be mutated, got %v", item.RetrievalUnitIDs)
	}
}

func TestApplyRecallProvenancePersistsEngineEvidence(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "eval-locomo.json")
	manifest := `{"transport":"ramesdk.pulse_cipher.open-memory-v1","endpoint":"http://127.0.0.1:18081/open/memory/v1","tenant_id":"user","user_id":"10","scope":"u10-mem-eval-locomo","suite":"locomo","corpus_sha256":"` + strings.Repeat("a", 64) + `","documents":2,"local_fallback":false}`
	if err := os.WriteFile(storePath+".manifest.json", []byte(manifest), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	recallRaw := map[string]any{
		"meta": map[string]any{
			"engine_transport": evalCloudTransport,
			"engine_scope":     EvalScope,
			"engine_upstream":  "http://127.0.0.1:18081/open/memory/v1",
			"local_fallback":   false,
			"cloud_hits":       float64(2),
		},
	}
	var item EvalQuestionResultItem
	applyRecallProvenance(&item, recallRaw, []string{"u1", "u2"}, storePath)
	if item.EngineTransport != evalCloudTransport || item.EngineScope != EvalScope {
		t.Fatalf("transport/scope not persisted: %+v", item)
	}
	if item.EngineUpstream == "" || item.CloudHits != 2 {
		t.Fatalf("upstream/cloud_hits not persisted: %+v", item)
	}
	if item.LocalFallback == nil || *item.LocalFallback {
		t.Fatalf("local_fallback must persist explicit false, got %v", item.LocalFallback)
	}
	if len(item.RetrievalUnitIDs) != 2 || item.RetrievalUnitIDs[0] != "u1" {
		t.Fatalf("retrieval_unit_ids not persisted: %v", item.RetrievalUnitIDs)
	}
	if item.CorpusSHA256 != strings.Repeat("a", 64) {
		t.Fatalf("corpus digest not persisted: %q", item.CorpusSHA256)
	}
}

func TestApplyRecallProvenanceWithoutManifestKeepsDigestEmpty(t *testing.T) {
	var item EvalQuestionResultItem
	applyRecallProvenance(&item, map[string]any{"meta": map[string]any{"cloud_hits": float64(1)}}, nil, filepath.Join(t.TempDir(), "missing.json"))
	if item.CorpusSHA256 != "" {
		t.Fatalf("missing manifest must not invent a digest, got %q", item.CorpusSHA256)
	}
	if item.CloudHits != 1 {
		t.Fatalf("cloud_hits not persisted: %d", item.CloudHits)
	}
}

func TestMarkOracleProvenance(t *testing.T) {
	var item EvalQuestionResultItem
	markOracleProvenance(&item, ATMEvalScope, 3, filepath.Join(t.TempDir(), "missing.json"))
	if item.EngineTransport != "oracle-gold" || item.EngineScope != ATMEvalScope || item.EngineUpstream != "oracle-gold" {
		t.Fatalf("oracle provenance not marked: %+v", item)
	}
	if item.LocalFallback == nil || *item.LocalFallback {
		t.Fatalf("oracle local_fallback must be explicit false")
	}
	if item.CloudHits != 3 {
		t.Fatalf("oracle hits = %d, want 3", item.CloudHits)
	}
}

func TestIsReferenceBaselineID(t *testing.T) {
	if !isReferenceBaselineID("baseline-rm-20261116") {
		t.Fatalf("baseline-rm-20261116 must be recognized as a sealed baseline")
	}
	if !isReferenceBaselineID("atm-baseline-rm-hard-wave-cell") {
		t.Fatalf("ATM baseline must be recognized as a sealed baseline")
	}
	if isReferenceBaselineID("run-20261106-120000") {
		t.Fatalf("recorded run must not be treated as a baseline")
	}
	if isReferenceBaselineID("") {
		t.Fatalf("empty id must not be treated as a baseline")
	}
}
