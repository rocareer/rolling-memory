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

// fixtureLoCoMoUpstream 构造两份会话：cat1-4 保留、cat5 丢弃；两份会话共享同一时间戳
// （复刻上游 conv-43/conv-49 的同时间会话）。
func fixtureLoCoMoUpstream(t *testing.T) []byte {
	t.Helper()
	conversations := []map[string]any{
		{
			"sample_id": "conv-a",
			"conversation": map[string]any{
				"session_1_date_time": "1:56 pm on 8 May, 2023",
				"session_1": []map[string]any{
					{"speaker": "Caroline", "dia_id": "D1:1", "text": "Hey Mel! Good to see you!"},
					{"speaker": "Melanie", "dia_id": "D1:2", "text": "Caroline! I joined an LGBTQ support group."},
				},
			},
			"qa": []map[string]any{
				{"question": "When did Caroline go to the group?", "answer": "7 May 2023", "evidence": []string{"D1:2"}, "category": 2},
				{"question": "Unanswerable twist?", "answer": "n/a", "evidence": []string{}, "category": 5},
				{"question": "Who greeted first?", "answer": "Caroline", "evidence": []string{"D1:1"}, "category": 1},
			},
		},
		{
			"sample_id": "conv-b",
			"conversation": map[string]any{
				"session_1_date_time": "1:56 pm on 8 May, 2023", // 与 conv-a 同时间戳
				"session_1": []map[string]any{
					{"speaker": "Nate", "dia_id": "D1:1", "text": "Woah Joanna, that's incredible!"},
				},
			},
			"qa": []map[string]any{
				{"question": "What surprised Nate?", "answer": "Joanna's progress", "evidence": []string{"D1:1"}, "category": 4},
			},
		},
	}
	raw, err := json.Marshal(conversations)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	return raw
}

func TestBuildLoCoMoOfficialBundleDeterministic(t *testing.T) {
	upstream := filepath.Join(t.TempDir(), "locomo10.json")
	if err := os.WriteFile(upstream, fixtureLoCoMoUpstream(t), 0o644); err != nil {
		t.Fatalf("write upstream: %v", err)
	}
	buildOnce := func(dir string) map[string][]byte {
		if _, err := BuildLoCoMoOfficialBundle(upstream, dir); err != nil {
			t.Fatalf("build: %v", err)
		}
		files := map[string][]byte{}
		for _, rel := range []string{"corpus.jsonl", "tiers/questions-1540.json", "MANIFEST.json"} {
			raw, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
			if err != nil {
				t.Fatalf("read %s: %v", rel, err)
			}
			files[rel] = raw
		}
		return files
	}
	first := buildOnce(t.TempDir())
	second := buildOnce(t.TempDir())
	for rel, raw := range first {
		if string(raw) != string(second[rel]) {
			t.Fatalf("%s is not deterministic across builds", rel)
		}
	}

	// 语料：每个会话一轮一行；同时间戳会话按出现顺序加 #0/#1 后缀。
	lines := strings.Split(strings.TrimSpace(string(first["corpus.jsonl"])), "\n")
	if len(lines) != 3 {
		t.Fatalf("corpus rows = %d, want 3", len(lines))
	}
	var sessions []string
	for _, line := range lines {
		var row locomoCorpusRow
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatalf("corpus row: %v", err)
		}
		sessions = append(sessions, row.Session)
		if row.ID == "" || row.Locator == "" || row.OccurredAt == "" {
			t.Fatalf("row missing identity fields: %+v", row)
		}
	}
	if sessions[0] != sessions[1] {
		t.Fatalf("same-session rows must share a session key: %v", sessions)
	}
	if sessions[0] == sessions[2] || !strings.Contains(sessions[2], "#1") {
		t.Fatalf("duplicate datetime must yield a #1-suffixed session key: %v", sessions)
	}

	// 题集：仅保留 cat1-4，证据解析为文本；ID 为过滤后连续编号。
	var doc struct {
		Meta      map[string]any           `json:"_meta"`
		Questions []locomoOfficialQuestion `json:"questions"`
	}
	if err := json.Unmarshal(first["tiers/questions-1540.json"], &doc); err != nil {
		t.Fatalf("questions doc: %v", err)
	}
	if len(doc.Questions) != 3 {
		t.Fatalf("kept questions = %d, want 3 (cat5 dropped)", len(doc.Questions))
	}
	if got := doc.Questions[0].ID; got != "conv-a#0" {
		t.Fatalf("first kept question id = %q", got)
	}
	if got := doc.Questions[2].ID; got != "conv-b#0" {
		t.Fatalf("conv-b kept question id = %q", got)
	}
	if len(doc.Questions[0].EvidenceTexts) != 1 || !strings.Contains(doc.Questions[0].EvidenceTexts[0], "support group") {
		t.Fatalf("evidence text not resolved: %+v", doc.Questions[0])
	}
	if got := doc.Questions[0].EvidenceMessages[0].Speaker; got != "Melanie" {
		t.Fatalf("evidence speaker = %q", got)
	}

	// 清单只含官方 1540 档。
	var manifest struct {
		Tiers map[string]any `json:"tiers"`
	}
	if err := json.Unmarshal(first["MANIFEST.json"], &manifest); err != nil {
		t.Fatalf("manifest: %v", err)
	}
	if len(manifest.Tiers) != 1 {
		t.Fatalf("manifest tiers = %v, want only 1540", manifest.Tiers)
	}
	if _, ok := manifest.Tiers["1540"]; !ok {
		t.Fatalf("manifest missing 1540 tier: %v", manifest.Tiers)
	}
}

func TestFetchLoCoMoUpstreamLicenseGateAndDownload(t *testing.T) {
	fixture := fixtureLoCoMoUpstream(t)
	var hits int64
	sum := sha256.Sum256(fixture)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		if !strings.HasSuffix(r.URL.Path, "/data/locomo10.json") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write(fixture)
	}))
	t.Cleanup(server.Close)

	// 未接受许可：零请求拒绝。
	target := t.TempDir()
	_, err := FetchLoCoMoUpstream(context.Background(), FetchLoCoMoOptions{
		TargetDir:  target,
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		fileSize:   int64(len(fixture)),
		fileSHA:    hex.EncodeToString(sum[:]),
	})
	if !errors.Is(err, ErrUpstreamLicenseNotAccepted) {
		t.Fatalf("want license gate error, got %v", err)
	}
	if got := atomic.LoadInt64(&hits); got != 0 {
		t.Fatalf("license gate must block requests, got %d", got)
	}

	// 接受许可：下载 + 重建 + 自洽清单；再次运行命中缓存并保留清单。
	opts := FetchLoCoMoOptions{
		TargetDir:     target,
		BaseURL:       server.URL,
		HTTPClient:    server.Client(),
		AcceptLicense: true,
		fileSize:      int64(len(fixture)),
		fileSHA:       hex.EncodeToString(sum[:]),
	}
	report, err := FetchLoCoMoUpstream(context.Background(), opts)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if !report.Rebuilt || report.CorpusSHA256 == "" || len(report.TierSHA256) != 1 {
		t.Fatalf("unexpected report: %+v", report)
	}
	for _, rel := range []string{"locomo10.json", "corpus.jsonl", "tiers/questions-1540.json", "MANIFEST.json", "UPSTREAM-PROVENANCE.json"} {
		if _, err := os.Stat(filepath.Join(target, filepath.FromSlash(rel))); err != nil {
			t.Fatalf("missing %s: %v", rel, err)
		}
	}
	manifestBefore, err := os.ReadFile(filepath.Join(target, "MANIFEST.json"))
	if err != nil {
		t.Fatalf("manifest read: %v", err)
	}
	if _, err := FetchLoCoMoUpstream(context.Background(), opts); err != nil {
		t.Fatalf("second fetch: %v", err)
	}
	if got := atomic.LoadInt64(&hits); got != 1 {
		t.Fatalf("cached run must not re-download, hits=%d", got)
	}
	manifestAfter, err := os.ReadFile(filepath.Join(target, "MANIFEST.json"))
	if err != nil {
		t.Fatalf("manifest reread: %v", err)
	}
	if string(manifestBefore) != string(manifestAfter) {
		t.Fatal("matching manifest must be preserved across fetches")
	}
}
