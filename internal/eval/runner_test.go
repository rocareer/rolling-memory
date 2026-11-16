// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package eval

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/rocareer/rolling-memory/internal/ids"
)

// buildTinyData 造一个最小 evaldata（1 档 2 题 + 3 条语料），用于跑通 runner。
func buildTinyData(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "tiers"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	corpus := `{"author":"A","id":"11","locator":"s1#0","occurred_at":"1700000000","session":"s1","text":"I moved from Sweden last year."}
{"author":"B","id":"13","locator":"s1#1","occurred_at":"1700000060","session":"s1","text":"That sounds like a big move!"}
{"author":"A","id":"17","locator":"s2#0","occurred_at":"1700100000","session":"s2","text":"My cat is called Momo."}
`
	if err := os.WriteFile(filepath.Join(dir, "corpus.jsonl"), []byte(corpus), 0o644); err != nil {
		t.Fatalf("write corpus: %v", err)
	}
	questions := map[string]any{
		"_meta": map[string]any{"tier": "1540"},
		"questions": []any{
			map[string]any{
				"id": "s1#0", "sample_id": "s1", "category": "1",
				"question": "Where did I move from?", "answer": "Sweden",
				"evidence_texts": []any{"I moved from Sweden last year."},
			},
			map[string]any{
				"id": "s1#1", "sample_id": "s1", "category": "4",
				"question": "What is my cat called?", "answer": "Momo",
				"evidence_texts": []any{"My cat is called Momo."},
			},
		},
	}
	encoded, err := json.MarshalIndent(questions, "", " ")
	if err != nil {
		t.Fatalf("marshal questions: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tiers", "questions-1540.json"), encoded, 0o644); err != nil {
		t.Fatalf("write questions: %v", err)
	}
	manifest := Manifest{
		Version: "1", Protocol: FrozenProtocolText,
		Corpus: FileEntry{Path: "corpus.jsonl", SHA256: fileSHA256(t, filepath.Join(dir, "corpus.jsonl")), Rows: 3},
		Tiers: map[string]TierEntry{
			"1540": {File: filepath.Join("tiers", "questions-1540.json"),
				SHA256: fileSHA256(t, filepath.Join(dir, "tiers", "questions-1540.json")), Count: 2},
		},
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "MANIFEST.json"), raw, 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	return dir
}

func fileSHA256(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return sha256Hex(raw)
}

// TestRunCloudSDK 跑通：清单核验 → 云端摄入 → 云端检索 → 来源留痕 → 汇总。
func TestRunCloudSDK(t *testing.T) {
	dataDir := buildTinyData(t)
	server := newCloudTestServer(t)
	engine := newTestCloudEngine(t, server, SuiteLoCoMo)
	outDir := t.TempDir()
	summary, err := Run(engine.cfg, Options{
		DataDir: dataDir, Tier: "1540", StorePath: engine.StorePath, OutDir: outDir,
		PulseMode: PulseModePulseCipher,
		Reader:    "deliverable", Answerer: "none", Judge: "off",
		Workers: 2, ContextChars: 6000, RawLimit: 5,
		ProgressEach: 0, Log: func(string, ...any) {},
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if summary["n"] != 2 || summary["transport"] != CloudTransport {
		t.Fatalf("cloud summary provenance = %v", summary)
	}
	// scope = 每次新评测自动分配的独立世代 scope（<user>-mem-eval-<suite>-<世代号>）。
	scope, ok := summary["scope"].(string)
	if !ok {
		t.Fatalf("summary scope = %v", summary["scope"])
	}
	if suite, generation, scopeOK := SplitEvalScope(scope); !scopeOK || suite != SuiteLoCoMo || generation == "" {
		t.Fatalf("summary scope 不是本套件的评测世代 scope: %q", scope)
	}
	if summary["local_fallback"] != false || summary["evidence_hit"] != 1.0 {
		t.Fatalf("cloud summary metrics = %v", summary)
	}
	engineMeta := summary["engine"].(map[string]any)
	if engineMeta["endpoint"] != server.URL+"/open/memory/v1" || engineMeta["scope"] != scope {
		t.Fatalf("engine metadata = %v", engineMeta)
	}
	manifest, err := ReadCloudManifest(engine.StorePath)
	if err != nil || manifest.Documents != 3 || manifest.CorpusSHA256 == "" || manifest.CorpusSHA256 == "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Fatalf("cloud manifest = %+v err=%v", manifest, err)
	}
	raw, err := os.ReadFile(filepath.Join(outDir, "results-sdk-pulse-cipher-none.jsonl"))
	if err != nil || !strings.Contains(string(raw), `"engine_transport":"ramesdk.pulse_cipher.open-memory-v1"`) || !strings.Contains(string(raw), `"retrieval_unit_ids"`) {
		t.Fatalf("result provenance missing: %s err=%v", raw, err)
	}

	server.mu.Lock()
	ingestCalls := len(server.ingests)
	server.mu.Unlock()
	reused, err := Run(engine.cfg, Options{
		DataDir: dataDir, Tier: "1540", StorePath: engine.StorePath, OutDir: t.TempDir(),
		PulseMode: PulseModePulseCipher,
		Reader:    "deliverable", Answerer: "none", Judge: "off", ReusePrepare: true,
		Workers: 2, ContextChars: 6000, RawLimit: 5, ProgressEach: 0, Log: func(string, ...any) {},
	})
	if err != nil {
		t.Fatalf("reuse prepare: %v", err)
	}
	if reused["ingest"].(map[string]any)["reused"] != true || reused["ingest"].(map[string]any)["inserted"] != 0 {
		t.Fatalf("identical corpus was not reused with provenance: %v", reused["ingest"])
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	if len(server.ingests) != ingestCalls {
		t.Fatalf("reuse triggered another ingest: before=%d after=%d", ingestCalls, len(server.ingests))
	}
}

// TestRunRejectsTamperedData sha256 不符必须响亮失败（冻结输入的可审计性）。
func TestRunRejectsTamperedData(t *testing.T) {
	dataDir := buildTinyData(t)
	path := filepath.Join(dataDir, "tiers", "questions-1540.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0o644); err != nil {
		t.Fatalf("tamper: %v", err)
	}
	cfg := testConfig(t)
	if _, err := Run(cfg, Options{
		DataDir: dataDir, Tier: "1540", StorePath: filepath.Join(t.TempDir(), "vault.json"), OutDir: t.TempDir(),
		Reader: "deliverable", Answerer: "none", Judge: "off", Workers: 1,
	}); err == nil {
		t.Fatal("tampered data must fail verification")
	}
}

// TestTierResolution 档位解析与缺档报错。
func TestTierResolution(t *testing.T) {
	manifest := Manifest{Tiers: map[string]TierEntry{"1540": {File: "tiers/questions-1540.json", Count: 1540}}}
	entry, err := manifest.Tier("")
	if err != nil || entry.Count != 1540 {
		t.Fatalf("default tier = %+v err=%v", entry, err)
	}
	if _, err := manifest.Tier("200"); err == nil {
		t.Fatal("retired convenience tier must not resolve")
	}
	if err := CheckLoCoMoTier("100"); err == nil {
		t.Fatal("retired tier 100 must be rejected before running")
	}
	if err := CheckLoCoMoTier("1540"); err != nil {
		t.Fatalf("official tier must pass: %v", err)
	}
	if _, err := ResolveDataDir(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("missing data dir must fail")
	}
}

// TestClassifyFailureBucket verifies the deterministic 4-bucket failure taxonomy.
func TestClassifyFailureBucket(t *testing.T) {
	if got := ClassifyFailureBucket(true, false, 0.8, 0, 0, ""); got != "HIT" {
		t.Fatalf("0-LLM evidence hit got %q, want HIT", got)
	}
	if got := ClassifyFailureBucket(false, false, 0.4, 0, 0, ""); got != "RANK_CUTOFF" {
		t.Fatalf("0-LLM partial recall got %q, want RANK_CUTOFF", got)
	}
	if got := ClassifyFailureBucket(false, false, 0.0, 0, 0, ""); got != "RECALL_MISS" {
		t.Fatalf("0-LLM zero recall got %q, want RECALL_MISS", got)
	}
	if got := ClassifyFailureBucket(true, true, 0.9, 0.65, 0.9, "wrong"); got != "JUDGE_AMBIGUITY" {
		t.Fatalf("high-F1 judge wrong got %q, want JUDGE_AMBIGUITY", got)
	}
	if got := ClassifyFailureBucket(true, false, 0.8, 0.1, 0.2, "wrong"); got != "READER_GAP" {
		t.Fatalf("evidence in context but low F1 wrong got %q, want READER_GAP", got)
	}
}

// TestFullTokenRecallIgnoresQueryCap 验证无损双轨量尺：TokenRecall 两侧都走
// ids.Tokenize（带 unique[:48] Query 上限，长交付上下文只剩头部 48 词元），
// FullTokenRecall / BestFullEvidenceRecall 走无上限的完整词元集合，只差「上限」一个变量。
func TestFullTokenRecallIgnoresQueryCap(t *testing.T) {
	// 头部 queryCapBound 个词元 + 其后的尾部词元：尾部命中恰好落在旧轨的截断之外。
	total := queryCapBound + 12
	terms := make([]string, 0, total)
	for index := 0; index < total; index++ {
		terms = append(terms, "tok"+strconv.Itoa(1000+index))
	}
	haystack := strings.Join(terms, " ")
	if capped := ids.Tokenize(haystack); len(capped) != queryCapBound {
		t.Fatalf("cap premise: ids.Tokenize(haystack) = %d terms, want %d", len(capped), queryCapBound)
	}
	if full := fullTermSet(haystack); len(full) != total {
		t.Fatalf("cap-free term set = %d terms, want %d", len(full), total)
	}
	tail := strings.Join(terms[queryCapBound:], " ")
	if got := TokenRecall(tail, haystack); got != 0.0 {
		t.Fatalf("capped rail: TokenRecall(tail, haystack) = %v, want 0.0", got)
	}
	if got := FullTokenRecall(tail, haystack); got != 1.0 {
		t.Fatalf("cap-free rail: FullTokenRecall(tail, haystack) = %v, want 1.0", got)
	}
	if got := BestEvidenceRecall([]string{tail}, haystack); got != 0.0 {
		t.Fatalf("capped rail: BestEvidenceRecall = %v, want 0.0", got)
	}
	if got := BestFullEvidenceRecall([]string{tail}, haystack); got != 1.0 {
		t.Fatalf("cap-free rail: BestFullEvidenceRecall = %v, want 1.0", got)
	}
	if got := FullTokenRecall(terms[queryCapBound]+" zzz998877", haystack); got != 0.5 {
		t.Fatalf("partial coverage = %v, want 0.5", got)
	}
	// 非拉丁语料同样量得准：ids.Tokenize 覆盖 CJK/kana/hangul，而 NormalizeAnswer 只留
	// [a-z0-9]（纯 CJK 会被清空），故本轨复用 ids.Tokenize 语义而非 AnswerTokens。
	if got := FullTokenRecall("你好世界", "前缀 你好世界 后缀"); got != 1.0 {
		t.Fatalf("CJK coverage = %v, want 1.0", got)
	}
}
