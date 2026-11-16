// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, content string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// TestSyncWatchOnceCommand 锁住 `sync watch --once` 走 RunAutoWatchTick（不落入已停用 push/pull 分支）。
func TestSyncWatchOnceCommand(t *testing.T) {
	newEnv(t)
	seedTranscript(t)

	res := run(t, "sync", "watch", "--once", "--json")
	if res.code != 0 {
		t.Fatalf("sync watch --once exit %d: %s%s", res.code, res.stdout, res.stderr)
	}
	payload := jsonObject(t, res.stdout)
	if payload["ok"] != true || payload["once"] != true {
		t.Fatalf("sync watch --once = %s", res.stdout)
	}
	for _, key := range []string{"ran", "background_chunks", "episodic_pushed"} {
		if _, ok := payload[key]; !ok {
			t.Fatalf("sync watch --once misses %q: %s", key, res.stdout)
		}
	}

	human := run(t, "sync", "watch", "--once")
	if human.code != 0 || !strings.HasPrefix(human.stdout, "sync watch (once): ") {
		t.Fatalf("human sync watch --once = %d %q %q", human.code, human.stdout, human.stderr)
	}

	retired := run(t, "sync", "push")
	if retired.code != 2 || !strings.Contains(retired.stderr, "invalid choice: 'push'") {
		t.Fatalf("retired sync push = %d %q", retired.code, retired.stderr)
	}
}

// TestImportTranscriptCommand verifies the `import` CLI subcommand on a JSONL transcript.
func TestImportTranscriptCommand(t *testing.T) {
	env := newEnv(t)
	transcript := writeFile(t, filepath.Join(env.base, "chat.jsonl"), strings.Join([]string{
		`{"type":"user","message":{"role":"user","content":"老张养了一只猫。"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"好的，记下了。"}]}}`,
		`{"type":"user","isMeta":true,"message":{"role":"user","content":"meta row must be skipped"}}`,
	}, "\n")+"\n")

	imported := run(t, "import", transcript, "--json")
	if imported.code != 0 {
		t.Fatalf("import exit %d: %s%s", imported.code, imported.stdout, imported.stderr)
	}
	payload := jsonObject(t, imported.stdout)
	for _, key := range []string{"ok", "path", "session", "messages", "inserted", "offset", "total_lines", "size"} {
		if _, ok := payload[key]; !ok {
			t.Fatalf("import misses %q: %s", key, imported.stdout)
		}
	}
	if payload["messages"] != float64(2) || payload["inserted"] != float64(2) {
		t.Fatalf("import = %s", imported.stdout)
	}
	if payload["session"] != "chat" {
		t.Fatalf("session = %#v", payload["session"])
	}
	if _, ok := payload["replay"]; ok {
		t.Fatalf("replay without --replay: %s", imported.stdout)
	}

	second := writeFile(t, filepath.Join(env.base, "notes.txt"), "老张养了一只猫。\n")
	replayed := run(t, "import", second, "--replay", "--json")
	if replayed.code != 0 {
		t.Fatalf("replay import exit %d: %s%s", replayed.code, replayed.stdout, replayed.stderr)
	}
	replayPayload := jsonObject(t, replayed.stdout)
	replay, _ := replayPayload["replay"].(map[string]any)
	if replay == nil {
		t.Fatalf("replay receipt missing: %s", replayed.stdout)
	}
	for _, key := range []string{"ok", "scope", "processed", "written", "skipped", "anchor_failed", "resolve_failed", "llm_calls", "warnings"} {
		if _, ok := replay[key]; !ok {
			t.Fatalf("replay misses %q: %s", key, replayed.stdout)
		}
	}
	if replay["processed"] != float64(1) {
		t.Fatalf("replay = %s", replayed.stdout)
	}

	// Human output goes through _print_import / _print_replay.
	human := run(t, "import", second, "--replay")
	if human.code != 0 {
		t.Fatalf("human import exit %d: %s", human.code, human.stderr)
	}
	if !strings.HasPrefix(human.stdout, "import: 1 messages · 0 new traces · session notes · offset 1/1\n") {
		t.Fatalf("human import = %q", human.stdout)
	}
	if !strings.Contains(human.stdout, "\nreplay: ") {
		t.Fatalf("human import misses the replay block: %q", human.stdout)
	}
}

func TestExportRejectsBeforeOpeningDatabase(t *testing.T) {
	env := newEnv(t)
	dbPath := filepath.Join(env.base, "untouched.db")
	outPath := filepath.Join(env.base, "existing.json")
	writeFile(t, dbPath, "database sentinel")
	writeFile(t, outPath, "output sentinel")
	for _, args := range [][]string{
		{"export", "--db", dbPath, "--out", outPath},
		{"export", "--db", dbPath, "--out", dbPath, "--json"},
		{"export", "--db", dbPath, "--json"},
	} {
		result := run(t, args...)
		if result.code != 1 || !strings.Contains(result.stderr+result.stdout, "not a complete backup") {
			t.Fatalf("export = %d %q %q", result.code, result.stdout, result.stderr)
		}
	}
	for path, want := range map[string]string{dbPath: "database sentinel", outPath: "output sentinel"} {
		raw, err := os.ReadFile(path)
		if err != nil || string(raw) != want {
			t.Fatalf("%s changed: %q, %v", path, raw, err)
		}
	}
}

// TestImportJSONExportHumanLine pins the receipt branch of _print_import
// : the export path prints per-table counts, not the
// transcript summary.
func TestImportJSONExportHumanLine(t *testing.T) {
	env := newEnv(t)
	exportPath := writeFile(t, filepath.Join(env.base, "export.json"),
		`{"format":"rolling-memory-export","version":1,"scope":"mem:test","traces":[],"nodes":[],"aliases":[],"assertions":[],"mentions":[],"meta_edges":[],"events":[]}`)

	newEnv(t)
	imported := run(t, "import", exportPath)
	if imported.code != 0 {
		t.Fatalf("import exit %d: %s", imported.code, imported.stderr)
	}
	if !strings.HasPrefix(imported.stdout, "import (json export): scope mem:test  inserted {") {
		t.Fatalf("import human line = %q", imported.stdout)
	}
	for _, key := range []string{"traces", "nodes", "aliases", "assertions", "mentions", "meta_edges", "events"} {
		if !strings.Contains(imported.stdout, "'"+key+"': ") {
			t.Fatalf("import counts miss %q: %q", key, imported.stdout)
		}
	}
}
