// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package eval

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// fixtureUpstreamATM 生成一套最小可解析的上游文件（键 = 上游 data/ 相对路径）。
func fixtureUpstreamATM(t *testing.T) map[string][]byte {
	t.Helper()
	marshal := func(v any) []byte {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal fixture: %v", err)
		}
		return raw
	}
	return map[string][]byte{
		"data/raw_memory/email/emails.json": marshal([]any{
			map[string]any{"id": "email1", "timestamp": "2022-05-01 10:00:00", "subject": "Trip plan", "short_summary": "plan", "detail": "we met in Cambridge"},
			map[string]any{"id": "email2", "timestamp": "2022-05-02 09:00:00", "subject": "Receipt", "short_summary": "receipt", "detail": "lunch with Alex"},
		}),
		"data/processed_memory/image_batch_results.json": marshal([]any{
			map[string]any{"image_path": "/raw/20220430_132212.jpg", "timestamp": "2022-04-30 13:22:12", "short_caption": "street", "caption": "a street corner", "ocr_text": "CAFE", "tags": []string{"street"}, "location_name": "Cambridge", "city": "Cambridge"},
		}),
		"data/processed_memory/video_batch_results.json": marshal([]any{
			map[string]any{"video_path": "/raw/20220501_101010.mp4", "timestamp": "2022-05-01 10:10:10", "short_caption": "park", "caption": "a walk in the park", "tags": []string{"park"}},
		}),
		"data/atm-bench/atm-bench.json": marshal([]any{
			map[string]any{"id": "q1", "question": "Where did we meet?", "answer": "Cambridge", "qtype": "factual", "evidence_ids": []string{"email1"}},
			map[string]any{"id": "q2", "question": "When was the receipt?", "answer": "May 2", "qtype": "temporal", "evidence_ids": []string{"email2"}},
			map[string]any{"id": "q3", "question": "What did I eat?", "answer": "lunch", "qtype": "factual", "evidence_ids": []string{"email2"}},
		}),
		"data/atm-bench/atm-bench-hard.json": marshal([]any{
			map[string]any{"id": "h1", "question": "Hard one?", "answer": "yes", "qtype": "multi-hop", "evidence_ids": []string{"email1", "email2"}},
		}),
	}
}

func stageLoaderLayout(t *testing.T, dir string, files map[string][]byte) {
	t.Helper()
	for rel, data := range files {
		local := strings.TrimPrefix(rel, "data/")
		path := filepath.Join(dir, filepath.FromSlash(local))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
}

func fixtureFileTable(files map[string][]byte) []upstreamFile {
	table := make([]upstreamFile, 0, len(files))
	for rel, data := range files {
		sum := sha256.Sum256(data)
		table = append(table, upstreamFile{
			Upstream: rel,
			Local:    strings.TrimPrefix(rel, "data/"),
			Size:     int64(len(data)),
			SHA256:   hex.EncodeToString(sum[:]),
		})
	}
	return table
}

// expectedPinsFromDirectExport 从同一份 fixture 直接跑一次官方导出，得到期望 pin。
func expectedPinsFromDirectExport(t *testing.T, files map[string][]byte) map[string]string {
	t.Helper()
	rawDir := t.TempDir()
	stageLoaderLayout(t, rawDir, files)
	outDir := t.TempDir()
	manifest, err := ExportBundledATMDataset(rawDir, outDir)
	if err != nil {
		t.Fatalf("direct export: %v", err)
	}
	corpusSHA, tierSHAs, err := manifestDigests(manifest)
	if err != nil {
		t.Fatalf("manifest digests: %v", err)
	}
	expected := map[string]string{"corpus.jsonl.gz": corpusSHA}
	for rel, sha := range tierSHAs {
		expected[rel] = sha
	}
	return expected
}

func serveUpstreamATM(t *testing.T, files map[string][]byte) (*httptest.Server, *int64) {
	t.Helper()
	var hits int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		prefix := "/datasets/" + ATMUpstreamDatasetID + "/resolve/"
		if !strings.HasPrefix(r.URL.Path, prefix) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		rest := strings.TrimPrefix(r.URL.Path, prefix)
		if idx := strings.Index(rest, "/"); idx >= 0 {
			rest = rest[idx+1:]
		}
		data, ok := files[rest]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write(data)
	}))
	t.Cleanup(server.Close)
	return server, &hits
}

func TestFetchATMUpstreamLicenseGate(t *testing.T) {
	files := fixtureUpstreamATM(t)
	server, hits := serveUpstreamATM(t, files)
	report, err := FetchATMUpstream(context.Background(), FetchATMOptions{
		TargetDir:     t.TempDir(),
		BaseURL:       server.URL,
		HTTPClient:    server.Client(),
		files:         fixtureFileTable(files),
		expected:      expectedPinsFromDirectExport(t, files),
		AcceptLicense: false,
	})
	if !errors.Is(err, ErrUpstreamLicenseNotAccepted) {
		t.Fatalf("want ErrUpstreamLicenseNotAccepted, got %v (report=%v)", err, report)
	}
	if got := atomic.LoadInt64(hits); got != 0 {
		t.Fatalf("license gate must block all requests, got %d", got)
	}
}

func TestFetchATMUpstreamRebuildVerifyAndCache(t *testing.T) {
	files := fixtureUpstreamATM(t)
	server, hits := serveUpstreamATM(t, files)
	target := t.TempDir()
	expected := expectedPinsFromDirectExport(t, files)
	opts := FetchATMOptions{
		TargetDir:     target,
		BaseURL:       server.URL,
		HTTPClient:    server.Client(),
		AcceptLicense: true,
		files:         fixtureFileTable(files),
		expected:      expected,
	}
	report, err := FetchATMUpstream(context.Background(), opts)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if !report.Rebuilt || !report.PinsVerified {
		t.Fatalf("rebuild/pins not verified: %+v", report)
	}
	if got := atomic.LoadInt64(hits); got != int64(len(files)) {
		t.Fatalf("want %d downloads, got %d", len(files), got)
	}
	for _, rel := range []string{"corpus.jsonl.gz", "MANIFEST.json", "tiers/questions-1013.json", "tiers/questions-hard.json", "raw_memory/email/emails.json"} {
		if _, err := os.Stat(filepath.Join(target, filepath.FromSlash(rel))); err != nil {
			t.Fatalf("missing %s: %v", rel, err)
		}
	}
	if report.CorpusSHA256 != expected["corpus.jsonl.gz"] {
		t.Fatalf("corpus pin mismatch: %s", report.CorpusSHA256)
	}
	provRaw, err := os.ReadFile(report.ProvenancePath)
	if err != nil {
		t.Fatalf("provenance: %v", err)
	}
	var prov map[string]any
	if err := json.Unmarshal(provRaw, &prov); err != nil {
		t.Fatalf("provenance json: %v", err)
	}
	rebuild, _ := prov["rebuild"].(map[string]any)
	if verified, _ := rebuild["pins_verified"].(bool); !verified {
		t.Fatalf("provenance pins_verified not true: %v", rebuild)
	}

	// 第二次：全部命中本地缓存，不再发起请求。
	report2, err := FetchATMUpstream(context.Background(), opts)
	if err != nil {
		t.Fatalf("second fetch: %v", err)
	}
	if got := atomic.LoadInt64(hits); got != int64(len(files)) {
		t.Fatalf("cached run must not re-download, hits=%d", got)
	}
	for _, f := range report2.Files {
		if f.Downloaded {
			t.Fatalf("cached run reported download for %s", f.Local)
		}
	}
}

func TestFetchATMUpstreamRejectsTamperedFile(t *testing.T) {
	files := fixtureUpstreamATM(t)
	tampered := map[string][]byte{}
	for k, v := range files {
		tampered[k] = v
	}
	corrupt := append([]byte(nil), files["data/atm-bench/atm-bench.json"]...)
	corrupt[0] = '['
	tampered["data/atm-bench/atm-bench.json"] = corrupt[:len(corrupt)/2]
	server, _ := serveUpstreamATM(t, tampered)
	target := t.TempDir()
	_, err := FetchATMUpstream(context.Background(), FetchATMOptions{
		TargetDir:     target,
		BaseURL:       server.URL,
		HTTPClient:    server.Client(),
		AcceptLicense: true,
		files:         fixtureFileTable(files), // 期望哈希来自完好文件
		expected:      expectedPinsFromDirectExport(t, files),
	})
	if err == nil || !strings.Contains(err.Error(), "不符") {
		t.Fatalf("want mismatch error, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(target, "corpus.jsonl.gz")); statErr == nil {
		t.Fatal("tampered fetch must not install rebuilt corpus")
	}
	entries, _ := os.ReadDir(filepath.Join(target, "atm-bench"))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".part") {
			t.Fatalf("partial file left behind: %s", e.Name())
		}
	}
}

func TestResolveATMFetchTargetExplicit(t *testing.T) {
	got := ResolveATMFetchTarget(" /tmp/evaldata-x ")
	if got != filepath.Join("/tmp/evaldata-x", "atmbench") {
		t.Fatalf("explicit target = %q", got)
	}
	// 默认目标必须可推导（本机存在家目录）：以 `<...>/atmbench` 结尾。
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "xdg"))
	t.Setenv("ROLLING_MEMORY_ATM_DATA", "")
	if def := ResolveATMFetchTarget(""); def != "" && !strings.HasSuffix(def, "atmbench") {
		t.Fatalf("default target = %q", def)
	}
}

func TestFetchATMUpstreamKeepsMatchingManifest(t *testing.T) {
	files := fixtureUpstreamATM(t)
	server, _ := serveUpstreamATM(t, files)
	expected := expectedPinsFromDirectExport(t, files)
	target := t.TempDir()
	stageLoaderLayout(t, target, files)

	writeManifest := func(corpusSHA string, withMarker bool) []byte {
		tiers := map[string]any{}
		order := map[string]string{
			"tiers/questions-1013.json": "1013",
			"tiers/questions-hard.json": "hard",
		}
		for rel, cat := range order {
			tiers[cat] = map[string]any{"file": rel, "sha256": expected[rel]}
		}
		manifest := map[string]any{
			"version":  "1",
			"protocol": "atmbench-official-v1",
			"corpus":   map[string]any{"path": "corpus.jsonl.gz", "sha256": corpusSHA, "rows": 5},
			"tiers":    tiers,
		}
		if withMarker {
			manifest["legacy_marker"] = true
		}
		raw, err := json.MarshalIndent(manifest, "", "  ")
		if err != nil {
			t.Fatalf("marshal manifest: %v", err)
		}
		raw = append(raw, '\n')
		if err := os.WriteFile(filepath.Join(target, "MANIFEST.json"), raw, 0o644); err != nil {
			t.Fatalf("write manifest: %v", err)
		}
		return raw
	}

	// 既有清单与重建产物同 pin：保留原文件（含旧 schema 的标记字段）。
	kept := writeManifest(expected["corpus.jsonl.gz"], true)
	opts := FetchATMOptions{
		TargetDir:     target,
		BaseURL:       server.URL,
		HTTPClient:    server.Client(),
		AcceptLicense: true,
		files:         fixtureFileTable(files),
		expected:      expected,
	}
	if _, err := FetchATMUpstream(context.Background(), opts); err != nil {
		t.Fatalf("fetch with matching manifest: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(target, "MANIFEST.json"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if string(got) != string(kept) {
		t.Fatalf("matching manifest must be preserved, got %s", string(got))
	}

	// 既有清单 pin 不符：替换为新重建生成的清单。
	writeManifest("deadbeef", false)
	if _, err := FetchATMUpstream(context.Background(), opts); err != nil {
		t.Fatalf("fetch with stale manifest: %v", err)
	}
	got, err = os.ReadFile(filepath.Join(target, "MANIFEST.json"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if strings.Contains(string(got), "deadbeef") {
		t.Fatalf("stale manifest must be replaced, got %s", string(got))
	}
	var replaced map[string]any
	if err := json.Unmarshal(got, &replaced); err != nil {
		t.Fatalf("replaced manifest json: %v", err)
	}
	if _, ok := replaced["official_corpus_pins_verified"]; !ok {
		t.Fatalf("replaced manifest should be the freshly generated one: %v", replaced)
	}
}
