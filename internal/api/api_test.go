// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package api_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rocareer/rolling-memory/internal/api"
	"github.com/rocareer/rolling-memory/internal/clock"
	"github.com/rocareer/rolling-memory/internal/config"
	"github.com/rocareer/rolling-memory/internal/thinclient"
	"github.com/rocareer/rolling-memory/internal/version"
)

const testNow = int64(1793102400)

func TestMain(m *testing.M) {
	previous := time.Local
	time.Local = time.UTC
	code := m.Run()
	time.Local = previous
	os.Exit(code)
}

func strPtr(value string) *string { return &value }

func boolPtr(value bool) *bool { return &value }

func newMemory(t *testing.T) *api.Memory {
	t.Helper()
	restore := clock.SetForTest(time.Unix(testNow, 0), 1_000_000_000)
	t.Cleanup(restore)
	directory := t.TempDir()
	dbPath := filepath.Join(directory, "memory.db")
	configPath := filepath.Join(directory, "config.toml")
	mode := "local"
	scope := "mem:test"
	reader := "deliverable"
	cfg, err := config.Load(config.LoadOptions{
		Path: &configPath, DB: &dbPath, Mode: &mode, Scope: &scope, Reader: &reader,
		Env: map[string]string{},
	})
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	cfg.Log = filepath.Join(directory, "state", "rolling-memory.log")
	memory, err := api.New(cfg)
	if err != nil {
		t.Fatalf("api: %v", err)
	}
	t.Cleanup(func() { memory.Close() })
	return memory
}

func fact(s, x string, o any) map[string]any {
	return map[string]any{
		"s": map[string]any{"label": s},
		"x": x,
		"o": o,
	}
}

func rememberOne(t *testing.T, memory *api.Memory, content, s, x string, o any) map[string]any {
	t.Helper()
	receipt, err := memory.Remember(api.RememberRequest{
		Content: strPtr(content), AnchorText: strPtr(content), Assertion: fact(s, x, o),
		Author: "user", AutoSync: boolPtr(false),
	})
	if err != nil {
		t.Fatalf("remember: %v", err)
	}
	return receipt
}

func TestStatusAndDoctor(t *testing.T) {
	memory := newMemory(t)
	status, err := memory.Status("mem:test")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status["version"] != version.Version {
		t.Fatalf("version = %#v", status["version"])
	}
	if _, ok := status["config"].(map[string]any); !ok {
		t.Fatalf("config = %#v", status["config"])
	}
	if _, ok := status["llm_warning"]; !ok {
		t.Fatal("status missing llm_warning")
	}
	if _, ok := status["vector_warning"]; !ok {
		t.Fatal("status missing vector_warning")
	}
	doctor, err := memory.Doctor()
	if err != nil {
		t.Fatalf("doctor: %v", err)
	}
	checks := doctor["checks"].([]any)
	names := []string{}
	for _, check := range checks {
		names = append(names, check.(map[string]any)["name"].(string))
	}
	want := []string{"db_exists", "db_writable", "fts5", "json1", "config", "llm", "embedding", "sync"}
	if len(names) != len(want) {
		t.Fatalf("checks = %#v", names)
	}
	for index := range want {
		if names[index] != want[index] {
			t.Fatalf("checks = %#v", names)
		}
	}
	if doctor["python"] != "" {
		t.Fatalf("python placeholder = %#v", doctor["python"])
	}
}

func TestExportRejectsIncompleteBackupWithoutWriting(t *testing.T) {
	memory := newMemory(t)
	dir := t.TempDir()
	existing := filepath.Join(dir, "existing.json")
	if err := os.WriteFile(existing, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "missing", "backup.json")
	for _, path := range []*string{nil, &existing, &missing, &memory.Config.DB} {
		if payload, err := memory.Export(path, nil); err == nil || payload != nil || !strings.Contains(err.Error(), "not a complete backup") {
			t.Fatalf("export = %#v, %v", payload, err)
		}
	}
	raw, err := os.ReadFile(existing)
	if err != nil || string(raw) != "original" {
		t.Fatalf("existing file changed: %q, %v", raw, err)
	}
	if _, err := os.Stat(filepath.Dir(missing)); !os.IsNotExist(err) {
		t.Fatalf("unexpected output directory: %v", err)
	}
	if err := memory.Store.DB().Ping(); err != nil {
		t.Fatalf("database changed: %v", err)
	}
}

func TestExportImportRoundTrip(t *testing.T) {
	memory := newMemory(t)
	rememberOne(t, memory, "老张养了一只猫。", "老张", "养", map[string]any{"label": "猫"})
	payload, err := memory.Store.ExportScope("mem:test")
	if err != nil {
		t.Fatalf("legacy export fixture: %v", err)
	}
	payload["format"] = version.ExportFormat
	payload["scope"] = "mem:test"
	if payload["format"] != version.ExportFormat || payload["scope"] != "mem:test" {
		t.Fatalf("payload = %#v", payload)
	}
	for _, key := range []string{"traces", "nodes", "aliases", "assertions", "mentions", "meta_edges", "events"} {
		if _, ok := payload[key]; !ok {
			t.Fatalf("payload missing %q", key)
		}
	}
	fresh := newMemory(t)
	imported, err := fresh.ImportData(payload, nil)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	inserted := imported["inserted"].(map[string]int)
	if inserted["traces"] != 1 || inserted["assertions"] != 1 {
		t.Fatalf("inserted = %#v", inserted)
	}
	again, err := fresh.ImportData(payload, nil)
	if err != nil {
		t.Fatalf("second import: %v", err)
	}
	for table, count := range again["inserted"].(map[string]int) {
		if count != 0 {
			t.Fatalf("%s re-imported %d rows", table, count)
		}
	}
	status, err := fresh.Status("mem:test")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status["active_assertions"] != int64(1) {
		t.Fatalf("active = %#v", status["active_assertions"])
	}
	if _, err := fresh.ImportData(map[string]any{"format": "other"}, nil); err == nil ||
		err.Error() != "not a rolling-memory export" {
		t.Fatalf("format error = %v", err)
	}
}

func TestRecentOrdersAndFilters(t *testing.T) {
	memory := newMemory(t)
	old, err := memory.Remember(api.RememberRequest{
		Content: strPtr("老张养了一只猫。"), AnchorText: strPtr("老张养了一只猫"),
		Assertion:  fact("老张", "养", map[string]any{"label": "猫"}),
		OccurredAt: int64(1793000000), Author: "user", AutoSync: boolPtr(false),
	})
	if err != nil {
		t.Fatalf("old: %v", err)
	}
	newer, err := memory.Remember(api.RememberRequest{
		Content: strPtr("老张住在北京。"), AnchorText: strPtr("老张住在北京"),
		Assertion:  fact("老张", "住在", map[string]any{"label": "北京"}),
		OccurredAt: int64(1793100000), Author: "user", AutoSync: boolPtr(false),
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	oldID := old["written"].([]any)[0].(map[string]any)["id"]
	newID := newer["written"].([]any)[0].(map[string]any)["id"]
	rows, err := memory.Recent(nil, nil, 10)
	if err != nil {
		t.Fatalf("recent: %v", err)
	}
	if len(rows) != 2 || rows[0]["id"] != newID || rows[1]["id"] != oldID {
		t.Fatalf("recent = %#v", rows)
	}
	if _, err := memory.Remember(api.RememberRequest{
		Content: strPtr("老张的暗号是蓝鸟。"), AnchorText: strPtr("老张的暗号是蓝鸟"),
		Assertion: fact("老张", "暗号", map[string]any{"literal": "蓝鸟"}),
		Audience:  "private", Author: "user", AutoSync: boolPtr(false),
	}); err != nil {
		t.Fatalf("private: %v", err)
	}
	deliverable, err := memory.Recent(nil, nil, 10)
	if err != nil {
		t.Fatalf("recent deliverable: %v", err)
	}
	for _, row := range deliverable {
		if row["audience"] == "private" {
			t.Fatalf("deliverable recent leaked private: %#v", row)
		}
	}
	owner, err := memory.Recent(nil, strPtr("owner"), 10)
	if err != nil {
		t.Fatalf("recent owner: %v", err)
	}
	if len(owner) != len(deliverable)+1 {
		t.Fatalf("owner=%d deliverable=%d", len(owner), len(deliverable))
	}
}

func TestExportFileAndImportFile(t *testing.T) {
	memory := newMemory(t)
	rememberOne(t, memory, "老张养了一只猫。", "老张", "养", map[string]any{"label": "猫"})
	path := filepath.Join(t.TempDir(), "export.json")
	payload, err := memory.Store.ExportScope("mem:test")
	if err != nil {
		t.Fatalf("legacy export fixture: %v", err)
	}
	payload["format"] = version.ExportFormat
	payload["scope"] = "mem:test"
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	fresh := newMemory(t)
	imported, err := fresh.ImportFile(path, nil, nil, "shared", false)
	if err != nil {
		t.Fatalf("import file: %v", err)
	}
	inserted := imported["inserted"].(map[string]int)
	if inserted["traces"] != 1 || inserted["assertions"] != 1 {
		t.Fatalf("inserted = %#v", inserted)
	}
	status, err := fresh.Status("mem:test")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status["active_assertions"] != int64(1) {
		t.Fatalf("active = %#v", status["active_assertions"])
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	memory := newMemory(t)
	if err := memory.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := memory.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
}

func TestReindexAndStatus(t *testing.T) {
	memory := newMemory(t)
	rememberOne(t, memory, "老张养了一只猫。", "老张", "养", map[string]any{"label": "猫"})
	reindexed, err := memory.Reindex()
	if err != nil {
		t.Fatalf("reindex: %v", err)
	}
	if reindexed["ok"] != true {
		t.Fatalf("reindex = %#v", reindexed)
	}
	fts := reindexed["fts"].(map[string]int)
	if fts["nodes"] < 2 {
		t.Fatalf("fts = %#v", fts)
	}
	status, err := memory.Status("mem:test")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status["active_assertions"] != int64(1) {
		t.Fatalf("status = %#v", status)
	}
	if status["mode"] != "local" {
		t.Fatalf("mode = %#v", status["mode"])
	}
	if status["schema_version"] != int64(1) {
		t.Fatalf("schema_version = %#v", status["schema_version"])
	}
}

// TestLocalImportMirrorsTracesIntoVaultRecall locks the local-mode bridge:
// transcript import must populate the thin plaintext vault so `local` recall is
// not an empty assertion graph. The cloud path is untouched.
func TestLocalImportMirrorsTracesIntoVaultRecall(t *testing.T) {
	memory := newMemory(t)
	scope := "mem:test"
	session := "transcript"
	path := filepath.Join("..", "..", "testdata", "golden", "cli", "208-cli-matrix", "input", "transcript.jsonl")
	if _, err := memory.ImportFile(path, &scope, &session, "shared", false); err != nil {
		t.Fatalf("import: %v", err)
	}
	reader := "owner"
	result, err := memory.Recall(api.RecallRequest{Cue: "老张搬到哪里了？", Reader: &reader})
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if len(result.Bands) == 0 {
		t.Fatalf("expected local vault recall bands, got %+v", result)
	}
	found := false
	for _, hit := range result.RawHits {
		if text, _ := hit["text"].(string); strings.Contains(text, "上海") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected a raw hit containing 上海, got %+v", result.RawHits)
	}
	recent, err := memory.Recent(&scope, &reader, 10)
	if err != nil {
		t.Fatalf("recent: %v", err)
	}
	if len(recent) == 0 {
		t.Fatalf("expected recent vault items after import")
	}
}

// TestLocalModeNeverUsesCloudTransport locks the no-egress invariant: stored
// credentials (BRAIN_KEY) must not override `mode=local` when no explicit
// BRAIN_URL is set. Explicit BRAIN_URL precedence is covered by
// TestThinClientBrainURLEnvPrecedence.
func TestLocalModeNeverUsesCloudTransport(t *testing.T) {
	t.Setenv("ROLLING_MEMORY_BRAIN_URL", "")
	t.Setenv("ROLLING_MEMORY_BRAIN_KEY", "mem2.test.local-only")
	memory := newMemory(t)
	tc, err := memory.ThinClient()
	if err != nil {
		t.Fatalf("thin client: %v", err)
	}
	if _, ok := tc.Transport.(*thinclient.LocalVaultTransport); !ok {
		t.Fatalf("local mode must use LocalVaultTransport, got %T", tc.Transport)
	}
}
