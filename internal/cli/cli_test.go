// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package cli_test

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/rocareer/rolling-memory/internal/cli"
	"github.com/rocareer/rolling-memory/internal/version"

	_ "modernc.org/sqlite"
)

func TestMain(m *testing.M) {
	previous := time.Local
	time.Local = time.UTC
	// Host identity variables are cleared for the whole test binary: the
	// developer's terminal may itself run inside a coding harness, and an
	// inherited marker would fabricate an active host in every snapshot a test
	// takes. Individual cases set the markers they mean to exercise.
	for _, key := range []string{
		"ZCODE_PROJECT_DIR", "ZCODE_SESSION_ID", "ZCODE_WORKSPACE", "TERM_PROGRAM",
		"CLAUDE_PROJECT_DIR", "CLAUDE_CODE_ENTRYPOINT", "CLAUDECODE",
		"CODEX_HOME", "CURSOR_TRACE_ID", "WINDSURF_SESSION",
		"DSH_SESSION_ID", "DSH_SHELL", "ROLLING_MEMORY_HARNESS",
	} {
		_ = os.Unsetenv(key)
	}
	code := m.Run()
	time.Local = previous
	os.Exit(code)
}

// cliEnv isolates HOME/XDG/ROLLING_MEMORY_* so no test touches the real core.
type cliEnv struct {
	base string
}

func newEnv(t *testing.T) *cliEnv {
	t.Helper()
	base := t.TempDir()
	home := filepath.Join(base, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatalf("home: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(base, "xdg-config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(base, "xdg-data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(base, "xdg-state"))
	t.Setenv("DSH_HOME", filepath.Join(base, "dsh"))
	t.Setenv("ROLLING_MEMORY_CONFIG", filepath.Join(base, "config.toml"))
	t.Setenv("ROLLING_MEMORY_DB", filepath.Join(base, "memory.db"))
	t.Setenv("ROLLING_MEMORY_SCOPE", "mem:test")
	t.Setenv("ROLLING_MEMORY_MODE", "local")
	t.Setenv("ROLLING_MEMORY_LOG", filepath.Join(base, "state", "rolling-memory.log"))
	t.Setenv("ROLLING_MEMORY_READER", "")
	t.Setenv("ROLLING_MEMORY_LLM_PROVIDER", "")
	t.Setenv("ROLLING_MEMORY_LLM_API_KEY_ENV", "")
	t.Setenv("ROLLING_MEMORY_EMBEDDING_PROVIDER", "")
	t.Setenv("ROLLING_MEMORY_SYNC_BASE_URL", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("OLLAMA_API_KEY", "")
	// 宿主身份环境变量清空：开发者的终端可能正运行在 ZCode / Claude Code 等宿主里，
	// 若不清空，这些变量会泄漏进每一条 `sync status` 快照并凭空点亮一个「活跃宿主」。
	t.Setenv("ZCODE_PROJECT_DIR", "")
	t.Setenv("CLAUDE_PROJECT_DIR", "")
	t.Setenv("CODEX_HOME", "")
	t.Setenv("DSH_SESSION_ID", "")
	t.Setenv("DSH_SHELL", "")
	t.Setenv("ROLLING_MEMORY_HARNESS", "")
	t.Setenv("ZCODE_SESSION_ID", "")
	t.Setenv("ZCODE_WORKSPACE", "")
	t.Setenv("TERM_PROGRAM", "")
	t.Setenv("CLAUDE_CODE_ENTRYPOINT", "")
	t.Setenv("CLAUDECODE", "")
	t.Setenv("CURSOR_TRACE_ID", "")
	t.Setenv("WINDSURF_SESSION", "")
	return &cliEnv{base: base}
}

type cliResult struct {
	code   int
	stdout string
	stderr string
}

func run(t *testing.T, args ...string) cliResult {
	t.Helper()
	return runInput(t, "", args...)
}

func runInput(t *testing.T, input string, args ...string) cliResult {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := cli.Main(args, strings.NewReader(input), &stdout, &stderr)
	return cliResult{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

func jsonObject(t *testing.T, text string) map[string]any {
	t.Helper()
	value := map[string]any{}
	if err := json.Unmarshal([]byte(text), &value); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, text)
	}
	return value
}

func jsonArray(t *testing.T, text string) []any {
	t.Helper()
	value := []any{}
	if err := json.Unmarshal([]byte(text), &value); err != nil {
		t.Fatalf("invalid JSON array: %v\n%s", err, text)
	}
	return value
}

// seedTranscript 以现役写侧（`import` 转录 JSONL）播种测试库。
// `remember` 子命令已随本地断言图停用，断言图在无写侧后不再由 CLI 生成。
func seedTranscript(t *testing.T, lines ...string) {
	t.Helper()
	if len(lines) == 0 {
		lines = []string{
			`{"type":"user","sessionId":"cm1","timestamp":"2026-03-02T08:00:00Z","message":{"role":"user","content":"老张养了一只猫，叫小白。"}}`,
			`{"type":"assistant","sessionId":"cm1","timestamp":"2026-03-02T08:01:00Z","message":{"role":"assistant","content":[{"type":"text","text":"记住了。"}]}}`,
		}
	}
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	result := run(t, "import", path, "--session", "cm1", "--json")
	if result.code != 0 {
		t.Fatalf("import exit %d: %s%s", result.code, result.stdout, result.stderr)
	}
}

func TestDBFlagPreservesExistingParent(t *testing.T) {
	env := newEnv(t)
	directory := filepath.Join(env.base, "shared")
	if err := os.Mkdir(directory, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Chmod(directory, 0o755); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	path := filepath.Join(directory, "memory.db")
	result := run(t, "init", "--db", path, "--json")
	if result.code != 0 {
		t.Fatalf("init exit %d: %s%s", result.code, result.stdout, result.stderr)
	}
	for _, check := range []struct {
		path string
		mode os.FileMode
	}{{directory, 0o755}, {path, 0o600}} {
		info, err := os.Stat(check.path)
		if err != nil {
			t.Fatalf("stat %q: %v", check.path, err)
		}
		if info.Mode().Perm() != check.mode {
			t.Errorf("%q mode = %o, want %o", check.path, info.Mode().Perm(), check.mode)
		}
	}
}

func TestEndToEnd(t *testing.T) {
	newEnv(t)
	initResult := run(t, "init", "--json")
	if initResult.code != 0 {
		t.Fatalf("init exit %d: %s", initResult.code, initResult.stderr)
	}
	if ok, _ := jsonObject(t, initResult.stdout)["ok"].(bool); !ok {
		t.Fatalf("init = %s", initResult.stdout)
	}

	seedTranscript(t)

	// 本地无云引擎（薄客户端）⇒ recall 恒为 absent + empty_graph；此处只锁形态与其它本地面。
	recalled := run(t, "recall", "--q", "老张 小白", "--json")
	if recalled.code != 0 {
		t.Fatalf("recall exit %d: %s", recalled.code, recalled.stderr)
	}
	result := jsonObject(t, recalled.stdout)
	if _, ok := result["absent"]; !ok {
		t.Fatalf("recall misses absent: %s", recalled.stdout)
	}

	status := run(t, "status", "--json")
	if status.code != 0 {
		t.Fatalf("status exit %d: %s", status.code, status.stderr)
	}
	if active := jsonObject(t, status.stdout)["active_assertions"]; active != float64(0) {
		t.Fatalf("active_assertions = %#v (want 0：写侧停用后 CLI 不再生成断言)", active)
	}

	recent := run(t, "recent", "--json")
	if recent.code != 0 {
		t.Fatalf("recent exit %d: %s", recent.code, recent.stderr)
	}
	jsonArray(t, recent.stdout)

	check := run(t, "check", "--json")
	if check.code != 0 {
		t.Fatalf("check exit %d: %s", check.code, check.stderr)
	}
	if ok, _ := jsonObject(t, check.stdout)["ok"].(bool); !ok {
		t.Fatalf("check = %s", check.stdout)
	}

	doctor := run(t, "doctor", "--json")
	if doctor.code != 0 {
		t.Fatalf("doctor exit %d: %s", doctor.code, doctor.stderr)
	}
	diagnostics := jsonObject(t, doctor.stdout)
	if diagnostics["version"] != version.Version {
		t.Fatalf("doctor version = %#v, want %q", diagnostics["version"], version.Version)
	}
	integrity, _ := diagnostics["integrity"].(map[string]any)
	if ok, _ := integrity["ok"].(bool); !ok {
		t.Fatalf("doctor integrity = %#v", integrity)
	}

	humanDoctor := run(t, "doctor")
	if !strings.Contains(humanDoctor.stdout, "rolling-memory "+version.Version+" · go ") {
		t.Fatalf("doctor human line = %q", humanDoctor.stdout)
	}
	if !strings.Contains(humanDoctor.stdout, "integrity: ok") {
		t.Fatalf("doctor human integrity = %q", humanDoctor.stdout)
	}
}

func TestConfigAndMode(t *testing.T) {
	env := newEnv(t)
	shown := run(t, "config", "show", "--json")
	if shown.code != 0 {
		t.Fatalf("config show exit %d: %s", shown.code, shown.stderr)
	}
	data := jsonObject(t, shown.stdout)
	if data["mode"] != "local" {
		t.Fatalf("mode = %#v", data["mode"])
	}
	llm, _ := data["llm"].(map[string]any)
	key, _ := llm["key"].(map[string]any)
	if _, ok := key["configured"]; !ok {
		t.Fatalf("llm.key = %#v", llm["key"])
	}

	// config show prints JSON even without --json.
	plain := run(t, "config", "show")
	if plain.code != 0 || !strings.HasPrefix(strings.TrimSpace(plain.stdout), "{") {
		t.Fatalf("config show human = %d %q", plain.code, plain.stdout)
	}

	switched := run(t, "mode", "cloud")
	if switched.code != 0 || !strings.Contains(switched.stdout, "cloud") {
		t.Fatalf("mode cloud = %d %q %s", switched.code, switched.stdout, switched.stderr)
	}
	switchedBack := run(t, "mode", "local")
	if switchedBack.code != 0 || !strings.Contains(switchedBack.stdout, "local") {
		t.Fatalf("mode local = %d %q", switchedBack.code, switchedBack.stdout)
	}

	path := run(t, "config", "path")
	if path.code != 0 || strings.TrimSpace(path.stdout) != filepath.Join(env.base, "config.toml") {
		t.Fatalf("config path = %d %q", path.code, path.stdout)
	}
	set := run(t, "config", "set", "recall.bands", "6")
	if set.code != 0 || !strings.Contains(set.stdout, "updated ") {
		t.Fatalf("config set = %d %q %s", set.code, set.stdout, set.stderr)
	}
	afterSet := jsonObject(t, run(t, "config", "show").stdout)
	recall, _ := afterSet["recall"].(map[string]any)
	if recall["bands"] != float64(6) {
		t.Fatalf("recall.bands = %#v", recall["bands"])
	}
}

func TestExportUnavailableAfterImportedEvents(t *testing.T) {
	env := newEnv(t)
	seedTranscript(t)

	exportPath := filepath.Join(env.base, "export.json")
	exported := run(t, "export", "--out", exportPath)
	if exported.code != 1 || exported.stdout != "" || !strings.Contains(exported.stderr, "not a complete backup") {
		t.Fatalf("export = %d %q %q", exported.code, exported.stdout, exported.stderr)
	}
	if _, err := os.Stat(exportPath); !os.IsNotExist(err) {
		t.Fatalf("export output exists or cannot be checked: %v", err)
	}
	db, err := sql.Open("sqlite", filepath.Join(env.base, "memory.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	var events int
	if err := db.QueryRow("SELECT count(*) FROM event").Scan(&events); err != nil || events != 2 {
		t.Fatalf("events = %d, err = %v; want 2 imported events", events, err)
	}
}

func TestErrorsAreJSONWhenRequested(t *testing.T) {
	newEnv(t)
	result := run(t, "recall", "--q", "x", "--scope", "bad scope", "--json")
	if result.code != 1 {
		t.Fatalf("exit = %d, want 1", result.code)
	}
	data := jsonObject(t, result.stdout)
	if ok, _ := data["ok"].(bool); ok {
		t.Fatalf("ok = %#v", data["ok"])
	}
	if message, _ := data["error"].(string); !strings.Contains(message, "scope") {
		t.Fatalf("error = %#v", data["error"])
	}

	// Without --json the same failure goes to stderr.
	plain := run(t, "recall", "--q", "x", "--scope", "bad scope")
	if plain.code != 1 || !strings.HasPrefix(plain.stderr, "error: ") || plain.stdout != "" {
		t.Fatalf("plain error = %d %q %q", plain.code, plain.stdout, plain.stderr)
	}
}

func TestImportHistoricalJSONExport(t *testing.T) {
	env := newEnv(t)
	exportPath := writeFile(t, filepath.Join(env.base, "historical-export.json"), `{
		"format":"rolling-memory-export", "version":1, "scope":"mem:test",
		"traces":[
			{"hash":"historical-1", "scope":"mem:test", "text":"first", "occurred_at":1, "recorded_at":1},
			{"hash":"historical-2", "scope":"mem:test", "text":"second", "occurred_at":2, "recorded_at":2}
		],
		"events":[
			{"id":1, "scope":"mem:test", "op":"trace.add", "created_at":1},
			{"id":2, "scope":"mem:test", "op":"trace.add", "created_at":2}
		],
		"nodes":[], "aliases":[], "assertions":[], "mentions":[], "meta_edges":[]
	}`)

	imported := run(t, "import", exportPath, "--json")
	if imported.code != 0 {
		t.Fatalf("import exit %d: %s%s", imported.code, imported.stdout, imported.stderr)
	}
	payload := jsonObject(t, imported.stdout)
	inserted, _ := payload["inserted"].(map[string]any)
	if inserted["events"] != float64(2) || inserted["traces"] != float64(2) {
		t.Fatalf("inserted = %#v（want events/traces = 2）", payload["inserted"])
	}
	// 本地无云引擎：recall 不为 absent 的前提是云端命中，此处只锁形态。
	recalled := run(t, "recall", "--q", "老张 猫", "--no-raw", "--json")
	if recalled.code != 0 {
		t.Fatalf("recall exit %d: %s", recalled.code, recalled.stderr)
	}
	if _, ok := jsonObject(t, recalled.stdout)["absent"]; !ok {
		t.Fatalf("recall misses absent: %s", recalled.stdout)
	}
}

// TestRecallNoCrystallizeIsReadOnly pins the read-only recall used by debug consoles:
// `--no-crystallize` must deliver the same evidence without recording the query as an
// L3 noetic entry, while a default recall on the same cue still crystallizes.
func TestRecallNoCrystallizeIsReadOnly(t *testing.T) {
	env := newEnv(t)
	seedTranscript(t)
	noeticRows := func() int {
		t.Helper()
		db, err := sql.Open("sqlite", filepath.Join(env.base, "memory.db"))
		if err != nil {
			t.Fatalf("open db: %v", err)
		}
		defer db.Close()
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM thin_vault_noetic`).Scan(&count); err != nil {
			t.Fatalf("count noetic: %v", err)
		}
		return count
	}

	readOnly := run(t, "recall", "--q", "老张 猫", "--no-crystallize", "--json")
	if readOnly.code != 0 {
		t.Fatalf("read-only recall exit %d: %s", readOnly.code, readOnly.stderr)
	}
	if absent, _ := jsonObject(t, readOnly.stdout)["absent"].(bool); absent {
		t.Fatalf("read-only recall found no evidence: %s", readOnly.stdout)
	}
	if got := noeticRows(); got != 0 {
		t.Fatalf("--no-crystallize wrote %d noetic rows, want 0", got)
	}

	if result := run(t, "recall", "--q", "老张 猫", "--json"); result.code != 0 {
		t.Fatalf("default recall exit %d: %s", result.code, result.stderr)
	}
	if got := noeticRows(); got == 0 {
		t.Fatal("default recall with evidence must still crystallize an L3 noetic entry")
	}
}

// TestGovernanceCommandsSmokeOverImportedStore 冒烟：写侧停用后 CLI 不再生成断言图，
// TestRetiredAssertionGraphCommandsRejected 锁住契约：早期本地断言图读写与治理命令
// （remember/replay/nodes/why/changed/portrait/meta/govern/forget/conflicts/resolve/alias/promote/merge/unmerge）
// 已全数从 CLI 入口停用（一律返回 exit 2 invalid choice），仅保留现役薄客户端命令闭集。
func TestRetiredAssertionGraphCommandsRejected(t *testing.T) {
	newEnv(t)
	seedTranscript(t)

	retired := []string{
		"remember", "replay", "nodes", "why", "changed", "portrait",
		"meta", "govern", "forget", "conflicts", "resolve", "alias",
		"promote", "merge", "unmerge",
	}
	for _, cmd := range retired {
		res := run(t, cmd)
		if res.code != 2 || !strings.Contains(res.stderr, "invalid choice: '"+cmd+"'") {
			t.Fatalf("retired assertion-graph command %q must exit 2 invalid choice, got code=%d stderr=%q", cmd, res.code, res.stderr)
		}
	}

	recent := run(t, "recent", "--json")
	if recent.code != 0 {
		t.Fatalf("recent exit %d: %s", recent.code, recent.stderr)
	}
	jsonArray(t, recent.stdout)
}

func TestRecallProbeAndCueValidation(t *testing.T) {
	newEnv(t)
	seedTranscript(t)

	probe := run(t, "recall", "--q", "老张", "--result-mode", "probe")
	if probe.code != 0 {
		t.Fatalf("probe exit %d: %s", probe.code, probe.stderr)
	}
	payload := jsonObject(t, probe.stdout)
	for _, key := range []string{"scope", "absent", "hits", "gaps", "meta"} {
		if _, ok := payload[key]; !ok {
			t.Fatalf("probe misses %q: %s", key, probe.stdout)
		}
	}

	missingCue := run(t, "recall", "--json")
	if missingCue.code != 1 || !strings.Contains(jsonObject(t, missingCue.stdout)["error"].(string), "requires --q") {
		t.Fatalf("missing cue = %d %s", missingCue.code, missingCue.stdout)
	}
}

func TestInitProjectAndWriteConfig(t *testing.T) {
	env := newEnv(t)
	t.Chdir(env.base)
	result := run(t, "init", "--project", "--write-config", "--json")
	if result.code != 0 {
		t.Fatalf("init project exit %d: %s%s", result.code, result.stdout, result.stderr)
	}
	payload := jsonObject(t, result.stdout)
	wantDB := filepath.Join(env.base, ".rolling-memory", "memory.db")
	if payload["db"] != wantDB {
		t.Fatalf("db = %#v, want %q", payload["db"], wantDB)
	}
	configText := readFile(t, filepath.Join(env.base, "config.toml"))
	if !strings.Contains(configText, "[core]") || !strings.Contains(configText, wantDB) {
		t.Fatalf("config file = %s", configText)
	}
}

func TestVersionHelpAndUsageErrors(t *testing.T) {
	newEnv(t)
	verRun := run(t, "--version")
	if verRun.code != 0 || verRun.stdout != "rolling-memory "+version.Version+"\n" {
		t.Fatalf("--version = %d %q", verRun.code, verRun.stdout)
	}
	help := run(t, "--help")
	if help.code != 0 || !strings.Contains(help.stdout, "usage: rolling-memory") {
		t.Fatalf("--help = %d %q", help.code, help.stdout)
	}
	subHelp := run(t, "import", "-h")
	if subHelp.code != 0 || !strings.Contains(subHelp.stdout, "import") {
		t.Fatalf("import -h = %d %q", subHelp.code, subHelp.stdout)
	}

	cases := [][]string{
		{},
		{"nonsense"},
		{"why"},
		{"nodes", "--limit", "3"},
		{"mode", "bogus"},
		{"recall", "--q", "x", "--result-mode", "bogus"},
		{"config"},
		{"recall", "--q"},
		{"meta", "n_a", "n_b"},
		{"install", "--unknown-flag"},
	}
	for _, args := range cases {
		result := run(t, args...)
		if result.code != 2 {
			t.Fatalf("%v exit = %d, want 2 (stderr %q)", args, result.code, result.stderr)
		}
		if !strings.Contains(result.stderr, "rolling-memory: error:") {
			t.Fatalf("%v stderr = %q", args, result.stderr)
		}
	}
}

func TestMCPServerHandshake(t *testing.T) {
	newEnv(t)
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
	}, "\n") + "\n"
	result := runInput(t, input, "mcp")
	if result.code != 0 {
		t.Fatalf("mcp exit %d: %s", result.code, result.stderr)
	}
	lines := strings.Split(strings.TrimSpace(result.stdout), "\n")
	if len(lines) != 2 {
		t.Fatalf("mcp wrote %d lines: %s", len(lines), result.stdout)
	}
	var seen []string
	for _, line := range lines {
		message := jsonObject(t, line)
		resultValue, _ := message["result"].(map[string]any)
		if tools, ok := resultValue["tools"].([]any); ok {
			for _, tool := range tools {
				if item, ok := tool.(map[string]any); ok {
					name, _ := item["name"].(string)
					seen = append(seen, name)
				}
			}
		}
	}
	if strings.Join(seen, ",") != "recall" {
		t.Fatalf("tools = %v（单工具面：回忆即记忆）", seen)
	}
}

func TestHookFailOpen(t *testing.T) {
	newEnv(t)
	result := runInput(t, `{"prompt":"老张 猫","session_id":"smoke"}`, "hook", "recall", "--json")
	if result.code != 0 {
		t.Fatalf("hook exit %d: %s%s", result.code, result.stdout, result.stderr)
	}
	empty := runInput(t, "{}", "hook", "recall", "--json")
	if empty.code != 0 || strings.TrimSpace(empty.stdout) != "" {
		t.Fatalf("empty hook = %d %q", empty.code, empty.stdout)
	}
}

func TestInstallList(t *testing.T) {
	newEnv(t)
	result := run(t, "install", "--list", "--json")
	if result.code != 0 {
		t.Fatalf("install --list exit %d: %s", result.code, result.stderr)
	}
	rows := jsonArray(t, result.stdout)
	if len(rows) == 0 {
		t.Fatal("no harness rows")
	}
	for _, row := range rows {
		item, _ := row.(map[string]any)
		if item["id"] == nil || item["mcp"] == nil || item["skill"] == nil {
			t.Fatalf("row = %#v", item)
		}
	}
}

func TestReindexAndSyncGap(t *testing.T) {
	newEnv(t)
	seedTranscript(t)
	reindex := run(t, "reindex", "--json")
	if reindex.code != 0 {
		t.Fatalf("reindex exit %d: %s", reindex.code, reindex.stderr)
	}
	payload := jsonObject(t, reindex.stdout)
	if _, ok := payload["fts"]; !ok {
		t.Fatalf("reindex = %s", reindex.stdout)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}

func asInt64(value any) int64 {
	number, _ := value.(float64)
	return int64(number)
}

func TestCheckExitsOneOnError(t *testing.T) {
	env := newEnv(t)
	seedTranscript(t)

	// 注入可被 check 检出的不一致：悬挂断言行（测试自身连接的 SQLite 默认 FKs OFF，
	// 故可写入不存在 node/trace 的引用；check 的 foreign_key_check + assertion_node 会判 error）。
	db, err := sql.Open("sqlite", filepath.Join(env.base, "memory.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO assertion
		(node_id, scope, subject_literal, rel_id, object_literal, bare, t_valid_from, t_valid_to,
		 t_record, t_source, anchor_trace_hash, anchor_start, anchor_end, source_trace_hash,
		 audience, confidence, status, hash, created_at)
		VALUES ('n_orphan','mem:test','subj','n_rel','obj',0,0,0,0,0,'h',0,1,'h2','shared',0.9,'active','deadbeef',0)`); err != nil {
		db.Close()
		t.Fatalf("insert dangling assertion: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}

	result := run(t, "check", "--json")
	if result.code != 1 {
		t.Fatalf("check exit = %d, want 1 (%s%s)", result.code, result.stdout, result.stderr)
	}
	payload := jsonObject(t, result.stdout)
	if ok, _ := payload["ok"].(bool); ok {
		t.Fatalf("check ok = true: %s", result.stdout)
	}
}

func TestExportWithoutOutIsUnavailable(t *testing.T) {
	newEnv(t)
	seedTranscript(t)
	result := run(t, "export")
	if result.code != 1 || result.stdout != "" || !strings.Contains(result.stderr, "not a complete backup") {
		t.Fatalf("export = %d %q %q", result.code, result.stdout, result.stderr)
	}
	result = run(t, "export", "--json")
	if result.code != 1 || result.stderr != "" {
		t.Fatalf("export --json = %d %q %q", result.code, result.stdout, result.stderr)
	}
	payload := jsonObject(t, result.stdout)
	message, _ := payload["error"].(string)
	if payload["ok"] != false || !strings.Contains(message, "not a complete backup") {
		t.Fatalf("export --json payload = %#v", payload)
	}
}

func TestInstallNoTarget(t *testing.T) {
	newEnv(t)
	result := run(t, "install")
	if result.code != 1 {
		t.Fatalf("install exit = %d, want 1", result.code)
	}
	if !strings.Contains(result.stderr, "no harness detected") ||
		!strings.Contains(result.stderr, "rolling-memory install --list") {
		t.Fatalf("install stderr = %q", result.stderr)
	}

	unknown := run(t, "install", "bogus", "--json")
	if unknown.code != 1 {
		t.Fatalf("install bogus exit = %d", unknown.code)
	}
	message, _ := jsonObject(t, unknown.stdout)["error"].(string)
	if !strings.Contains(message, "unknown harness 'bogus'") {
		t.Fatalf("install bogus error = %#v", message)
	}
}

type epipeWriter struct{}

func (epipeWriter) Write([]byte) (int, error) { return 0, syscall.EPIPE }

func TestBrokenPipeExitsZero(t *testing.T) {
	newEnv(t)
	var stderr bytes.Buffer
	code := cli.Main([]string{"recall", "--q", "x", "--json"}, strings.NewReader(""), epipeWriter{}, &stderr)
	if code != 0 {
		t.Fatalf("broken pipe exit = %d, want 0 (stderr %q)", code, stderr.String())
	}
}

func TestEjectAndUISnapshotAndPortableMode(t *testing.T) {
	portableRoot := t.TempDir()
	t.Setenv("ROLLING_MEMORY_PORTABLE", portableRoot)
	t.Setenv("ROLLING_MEMORY_CONFIG", "")
	t.Setenv("ROLLING_MEMORY_DB", "")

	seedTranscript(t)

	ejectRes := run(t, "eject", "--json")
	if ejectRes.code != 0 {
		t.Fatalf("eject exit %d: %s", ejectRes.code, ejectRes.stderr)
	}
	ejectObj := jsonObject(t, ejectRes.stdout)
	if ok, _ := ejectObj["ok"].(bool); !ok {
		t.Fatalf("eject ok = %v", ejectObj)
	}
	if ejectObj["profile"] != "usb" {
		t.Fatalf("expected portable profile=usb, got %v", ejectObj["profile"])
	}
	if ejectObj["checkpoint"] != "TRUNCATE" {
		t.Fatalf("expected checkpoint=TRUNCATE, got %v", ejectObj["checkpoint"])
	}

	bundleDir := filepath.Join(t.TempDir(), "usb-rm")
	initRes := run(t, "init", "--portable", bundleDir, "--json")
	if initRes.code != 0 {
		t.Fatalf("init --portable exit %d: %s", initRes.code, initRes.stderr)
	}
	initObj := jsonObject(t, initRes.stdout)
	if ok, _ := initObj["ok"].(bool); !ok {
		t.Fatalf("init --portable ok = %v", initObj)
	}
	if initObj["profile"] != "usb" {
		t.Fatalf("init --portable profile = %v, want usb", initObj["profile"])
	}
	for _, rel := range []string{
		".portable",
		filepath.Join("config", "config.toml"),
		filepath.Join("data", "memory.db"),
		"setup.sh",
		"setup.command",
		"setup.cmd",
		"eject.sh",
		"eject.command",
		"eject.cmd",
	} {
		if _, err := os.Stat(filepath.Join(bundleDir, rel)); err != nil {
			t.Fatalf("portable bundle missing %s: %v", rel, err)
		}
	}
}
