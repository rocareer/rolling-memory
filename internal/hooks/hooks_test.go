// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rocareer/rolling-memory/internal/clock"
	"github.com/rocareer/rolling-memory/internal/config"
	"github.com/rocareer/rolling-memory/internal/memory/facade"
)

const testNow = int64(1793102400)

func TestMain(m *testing.M) {
	// UTC stays for the whole process: restoring time.Local after m.Run would
	// write a global while goroutines outliving their test (vault snapshot
	// refreshes) may still read it, and the process exits immediately after.
	time.Local = time.UTC
	code := m.Run()
	os.Exit(code)
}

func strPtr(value string) *string { return &value }

func boolPtr(value bool) *bool { return &value }

// testEnv isolates every path the hook touches: HOME/XDG for the defaults and
// explicit ROLLING_MEMORY_* overrides so no live memory core is ever opened.
type testEnv struct {
	base       string
	db         string
	log        string
	configPath string
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	restore := clock.SetForTest(time.Unix(testNow, 0), 1_000_000_000)
	t.Cleanup(restore)
	base := t.TempDir()
	home := filepath.Join(base, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatalf("home: %v", err)
	}
	env := &testEnv{
		base:       base,
		db:         filepath.Join(base, "memory.db"),
		log:        filepath.Join(base, "state", "rolling-memory.log"),
		configPath: filepath.Join(base, "config.toml"),
	}
	variables := map[string]string{
		"HOME":                              home,
		"XDG_CONFIG_HOME":                   filepath.Join(home, "config"),
		"XDG_DATA_HOME":                     filepath.Join(home, "data"),
		"XDG_STATE_HOME":                    filepath.Join(home, "state"),
		"ROLLING_MEMORY_CONFIG":             env.configPath,
		"ROLLING_MEMORY_DB":                 env.db,
		"ROLLING_MEMORY_LOG":                env.log,
		"ROLLING_MEMORY_SCOPE":              "mem:hooks",
		"ROLLING_MEMORY_MODE":               "local",
		"ROLLING_MEMORY_READER":             "deliverable",
		"ROLLING_MEMORY_LLM_PROVIDER":       "",
		"ROLLING_MEMORY_LLM_BASE_URL":       "",
		"ROLLING_MEMORY_LLM_API_KEY_ENV":    "",
		"ROLLING_MEMORY_EMBEDDING_PROVIDER": "",
		"ROLLING_MEMORY_SYNC_BASE_URL":      "",
		"OPENAI_API_KEY":                    "",
		"ROLLING_MEMORY_CLOUD_KEY":          "",
	}
	for key, value := range variables {
		t.Setenv(key, value)
	}
	return env
}

func loadConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.Load(config.LoadOptions{})
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	return cfg
}

func openMemory(t *testing.T) *facade.Memory {
	t.Helper()
	memory, err := facade.New(loadConfig(t))
	if err != nil {
		t.Fatalf("api: %v", err)
	}
	t.Cleanup(func() { memory.Close() })
	return memory
}

// seedCat writes one synthetic structured fact so recall has something to cite.
func seedCat(t *testing.T) {
	t.Helper()
	memory := openMemory(t)
	if _, err := memory.Remember(facade.RememberRequest{
		Content:    strPtr("我养了一只猫，叫小白。"),
		AnchorText: strPtr("我养了一只猫，叫小白。"),
		Assertion: map[string]any{
			"s": map[string]any{"label": "我"},
			"x": "养",
			"o": map[string]any{"label": "小白"},
		},
		Author: "user", AutoSync: boolPtr(false),
	}); err != nil {
		t.Fatalf("remember: %v", err)
	}
}

func runHookMain(t *testing.T, args []string, input string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := HookMain(args, strings.NewReader(input), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func logLines(t *testing.T, path string) []map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log %s: %v", path, err)
	}
	lines := []map[string]any{}
	for _, text := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(text) == "" {
			continue
		}
		parsed := map[string]any{}
		if err := json.Unmarshal([]byte(text), &parsed); err != nil {
			t.Fatalf("log line is not JSON: %q", text)
		}
		lines = append(lines, parsed)
	}
	return lines
}

func TestLocalHookNeverContactsBrain(t *testing.T) {
	env := newTestEnv(t)
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "unexpected request", http.StatusInternalServerError)
	}))
	defer server.Close()
	t.Setenv("ROLLING_MEMORY_BRAIN_URL", server.URL+"/open/memory/v1")
	t.Setenv("ROLLING_MEMORY_BRAIN_KEY", "test-key")
	payload := fmt.Sprintf(`{"session_id":"local-sentinel","cwd":%q,"prompt":"local sentinel"}`, env.base)
	code, stdout, stderr := runHookMain(t, []string{"recall", "--json"}, payload)
	if code != 0 {
		t.Fatalf("hook exit=%d stdout=%q stderr=%s", code, stdout, stderr)
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("local hook made %d remote requests", got)
	}
}

// TestRecallHookNeverInjectsAndCadenceReminds verifies the hook contract: the
// UserPromptSubmit hook never runs automatic recall and never injects recalled
// material; it only captures the prompt locally and, on the four-turn cadence in
// a workspace session, emits the one-line reminder (as the JSON envelope).
func TestRecallHookNeverInjectsAndCadenceReminds(t *testing.T) {
	env := newTestEnv(t)
	seedCat(t)
	wsDir := filepath.Join(env.base, "Rolling-Memory")
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		t.Fatalf("mkdir ws: %v", err)
	}
	prompt := func(turn int) string {
		return fmt.Sprintf("继续推进第 %d 步：我喜欢猫，我的猫叫小白。", turn)
	}
	payload := func(turn int) string {
		raw, _ := json.Marshal(map[string]any{
			"session_id": "s1",
			"cwd":        wsDir,
			"prompt":     prompt(turn),
		})
		return string(raw)
	}

	for turn := 1; turn <= 3; turn++ {
		code, stdout, stderr := runHookMain(t, []string{"recall", "--json"}, payload(turn))
		if code != 0 || strings.TrimSpace(stdout) != "" {
			t.Fatalf("turn %d must stay silent: exit=%d stdout=%q stderr=%s", turn, code, stdout, stderr)
		}
	}
	// Turn 4 hits the four-turn cadence: a static reminder, never the seeded
	// memory itself.
	code, stdout, stderr := runHookMain(t, []string{"recall", "--json"}, payload(4))
	if code != 0 {
		t.Fatalf("cadence turn exit = %d, stderr = %s", code, stderr)
	}
	if !strings.HasSuffix(stdout, "\n") {
		t.Fatalf("output must end with a newline: %q", stdout)
	}
	// Canonical JSON separators (", ", ": ") and key order.
	prefix := `{"hookSpecificOutput": {"hookEventName": "UserPromptSubmit", "additionalContext": "`
	if !strings.HasPrefix(stdout, prefix) {
		t.Fatalf("JSON envelope drifted from expected shape: %q", stdout)
	}
	decoded := map[string]any{}
	if err := json.Unmarshal([]byte(stdout), &decoded); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, stdout)
	}
	hook, ok := decoded["hookSpecificOutput"].(map[string]any)
	if !ok {
		t.Fatalf("missing hookSpecificOutput: %#v", decoded)
	}
	if hook["hookEventName"] != "UserPromptSubmit" {
		t.Fatalf("hookEventName = %#v", hook["hookEventName"])
	}
	context, _ := hook["additionalContext"].(string)
	for _, marker := range []string{"[Rolling-Memory 4轮节律锚点]", "已知文件或报错行就直接核验", "过去的决定、试过的办法"} {
		if !strings.Contains(context, marker) {
			t.Fatalf("cadence reminder missing %q:\n%s", marker, context)
		}
	}
	if strings.Contains(context, "小白") || strings.Contains(context, "<rolling-memory-recall") {
		t.Fatalf("hook must never inject recalled material:\n%s", context)
	}

	lines := logLines(t, env.log)
	if len(lines) != 4 {
		t.Fatalf("log lines = %#v", lines)
	}
	for index, line := range lines {
		if line["event"] != "recall" || line["injected"].(float64) != 0 {
			t.Fatalf("recall log line %d = %#v", index, line)
		}
	}
	if cadence, ok := lines[3]["cadence"].(float64); !ok || cadence != 1 {
		t.Fatalf("cadence turn must be logged with cadence=1: %#v", lines[3])
	}
}

func TestRecallHookCadenceRemindsOnVaultOnlyDB(t *testing.T) {
	env := newTestEnv(t)
	wsDir := filepath.Join(env.base, "Rolling-Memory")
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		t.Fatalf("mkdir ws: %v", err)
	}
	mem := openMemory(t)
	mem.Close()

	payload := func(turn int) string {
		raw, _ := json.Marshal(map[string]any{
			"session_id": "s-vault-only",
			"cwd":        wsDir,
			"prompt":     fmt.Sprintf("继续推进第 %d 步", turn),
		})
		return string(raw)
	}
	for turn := 1; turn <= 3; turn++ {
		code, stdout, stderr := runHookMain(t, []string{"recall", "--json"}, payload(turn))
		if code != 0 || strings.TrimSpace(stdout) != "" {
			t.Fatalf("turn %d must stay silent on vault-only DB: exit=%d stdout=%q stderr=%s", turn, code, stdout, stderr)
		}
	}
	code, stdout, stderr := runHookMain(t, []string{"recall", "--json"}, payload(4))
	if code != 0 || !strings.Contains(stdout, "[Rolling-Memory 4轮节律锚点]") {
		t.Fatalf("turn 4 must emit cadence reminder on vault-only DB: exit=%d stdout=%q stderr=%s", code, stdout, stderr)
	}
}

func TestRecallEmptyAndAbsentAreSilent(t *testing.T) {
	env := newTestEnv(t)
	code, stdout, _ := runHookMain(t, []string{"recall", "--json"}, "{}")
	if code != 0 || stdout != "" {
		t.Fatalf("empty prompt: exit=%d stdout=%q", code, stdout)
	}
	if _, err := os.Stat(env.log); !os.IsNotExist(err) {
		t.Fatalf("silent empty prompt must not log, stat err = %v", err)
	}
	code, stdout, _ = runHookMain(t, []string{"recall", "--json"},
		`{"prompt":"zzzqqqxxx","session_id":"s1"}`)
	if code != 0 || stdout != "" {
		t.Fatalf("absent recall: exit=%d stdout=%q", code, stdout)
	}
	lines := logLines(t, env.log)
	if len(lines) != 1 || lines[0]["absent"].(float64) != 1 || lines[0]["injected"].(float64) != 0 {
		t.Fatalf("absent log line = %#v", lines)
	}
}

func TestBudgetFlag(t *testing.T) {
	newTestEnv(t)
	seedCat(t)
	// The flag stays accepted on the hook surface; retrieval is agent-driven, so
	// the hook itself stays silent either way.
	code, stdout, stderr := runHookMain(t,
		[]string{"recall", "--json", "--budget-ms", " 5000 "}, `{"prompt":"小白"}`)
	if code != 0 || strings.TrimSpace(stdout) != "" {
		t.Fatalf("budget flag: exit=%d stdout=%q stderr=%s", code, stdout, stderr)
	}
	// A trailing flag without a value falls back to the config budget.
	code, stdout, _ = runHookMain(t, []string{"recall", "--json", "--budget-ms"},
		`{"session_id":"s2","prompt":"小白"}`)
	if code != 0 || strings.TrimSpace(stdout) != "" {
		t.Fatalf("valueless budget flag: exit=%d stdout=%q", code, stdout)
	}
	// standard raises ValueError before its try block, so the CLI exits 1.
	code, stdout, stderr = runHookMain(t, []string{"recall", "--budget-ms", "5x"},
		`{"session_id":"s3","prompt":"小白"}`)
	if code != 1 {
		t.Fatalf("bad budget exit = %d", code)
	}
	if stdout != "" || !strings.Contains(stderr, "error: invalid literal for int() with base 10: '5x'") {
		t.Fatalf("bad budget stderr = %q stdout = %q", stderr, stdout)
	}
	// With --json cli.main prints the error object on stdout instead.
	code, stdout, stderr = runHookMain(t, []string{"recall", "--json", "--budget-ms", "5x"},
		`{"prompt":"小白"}`)
	if code != 1 || stderr != "" {
		t.Fatalf("json bad budget: exit=%d stderr=%q", code, stderr)
	}
	want := `{"ok": false, "error": "invalid literal for int() with base 10: '5x'"}` + "\n"
	if stdout != want {
		t.Fatalf("json bad budget stdout = %q, want %q", stdout, want)
	}
}

func TestBudgetFlagAcceptsPythonIntForms(t *testing.T) {
	cases := map[string]int{
		"42": 42, " 42 ": 42, "+42": 42, "-7": -7, "1_000": 1000, "007": 7,
	}
	for raw, want := range cases {
		got, err := pyInt(raw)
		if err != nil || got != want {
			t.Fatalf("pyInt(%q) = %d, %v; want %d", raw, got, err, want)
		}
	}
	for _, raw := range []string{"", " ", "1.5", "_1", "1_", "1__0", "0x10", "abc"} {
		if _, err := pyInt(raw); err == nil {
			t.Fatalf("pyInt(%q) unexpectedly succeeded", raw)
		}
	}
}

func TestHookMainFailOpenOnBadInput(t *testing.T) {
	newTestEnv(t)
	code, stdout, _ := runHookMain(t, []string{"recall", "--json"}, "{")
	if code != 0 || stdout != "" {
		t.Fatalf("invalid JSON: exit=%d stdout=%q", code, stdout)
	}
	fallback := filepath.Join(config.DefaultStateDir(), "hook.log")
	raw, err := os.ReadFile(fallback)
	if err != nil {
		t.Fatalf("fallback log: %v", err)
	}
	if !strings.Contains(string(raw), "hook input error: invalid JSON") {
		t.Fatalf("fallback log = %q", raw)
	}
	// The fail-open log carries json.JSONDecodeError.msg, not the Go decoder
	// error ("unexpected EOF"); golden 230/070 pins the wording.
	if !strings.Contains(string(raw), "invalid JSON: Expecting property name enclosed in double quotes") {
		t.Fatalf("fallback log wording = %q", raw)
	}
	if !strings.HasPrefix(string(raw), fmt.Sprintf("%d ", testNow)) {
		t.Fatalf("fallback line must start with the epoch: %q", raw)
	}
}

func TestHookMainInputTooLarge(t *testing.T) {
	newTestEnv(t)
	oversized := strings.Repeat("a", maxHookInput+1)
	code, stdout, _ := runHookMain(t, []string{"recall", "--json"}, oversized)
	if code != 0 || stdout != "" {
		t.Fatalf("oversize input: exit=%d stdout=%q", code, stdout)
	}
	raw, err := os.ReadFile(filepath.Join(config.DefaultStateDir(), "hook.log"))
	if err != nil {
		t.Fatalf("fallback log: %v", err)
	}
	if !strings.Contains(string(raw), "hook input too large") {
		t.Fatalf("fallback log = %q", raw)
	}
}

func TestHookLogCarriesScopeForLiveScopes(t *testing.T) {
	env := newTestEnv(t)
	cfg := loadConfig(t)
	cfg.Scope = "mem:self"
	logHook(cfg, "recall", map[string]any{"status": "absent"})
	raw, err := os.ReadFile(env.log)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !strings.Contains(string(raw), `"scope": "mem:self"`) {
		t.Fatalf("expected live scope in hook log, got %s", string(raw))
	}
}

func TestHookMainNonObjectPayloadIsEmpty(t *testing.T) {
	newTestEnv(t)
	code, stdout, _ := runHookMain(t, []string{"recall", "--json"}, "[1, 2, 3]")
	if code != 0 || stdout != "" {
		t.Fatalf("list payload: exit=%d stdout=%q", code, stdout)
	}
}

func TestHookMainFailOpenWhenStoreCannotOpen(t *testing.T) {
	env := newTestEnv(t)
	blocker := filepath.Join(env.base, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("blocker: %v", err)
	}
	t.Setenv("ROLLING_MEMORY_DB", filepath.Join(blocker, "memory.db"))
	code, stdout, _ := runHookMain(t, []string{"recall", "--json"}, `{"prompt":"小白"}`)
	if code != 0 || stdout != "" {
		t.Fatalf("unopenable store: exit=%d stdout=%q", code, stdout)
	}
	raw, err := os.ReadFile(filepath.Join(config.DefaultStateDir(), "hook.log"))
	if err != nil || len(raw) == 0 {
		t.Fatalf("fallback log missing: %v", err)
	}
}

func TestHookMainUnknownEventFailsOpen(t *testing.T) {
	newTestEnv(t)
	code, stdout, _ := runHookMain(t, []string{"bogus"}, "{}")
	if code != 0 || stdout != "" {
		t.Fatalf("unknown event: exit=%d stdout=%q", code, stdout)
	}
}

func TestLogLineIsSortedAndRedacted(t *testing.T) {
	newTestEnv(t)
	cfg := loadConfig(t)
	logHook(cfg, "recall", map[string]any{
		"injected": 1, "bands": 2, "raw": 0, "ms": 7, "chars": 40,
		"note": "sk-secretsecret123",
	})
	raw, err := os.ReadFile(cfg.Log)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	text := strings.TrimSuffix(string(raw), "\n")
	previous := -1
	for _, key := range []string{"bands", "chars", "event", "injected", "ms", "note", "raw", "ts"} {
		index := strings.Index(text, `"`+key+`":`)
		if index < 0 || index < previous {
			t.Fatalf("key %q out of order in %q", key, text)
		}
		previous = index
	}
	if strings.Contains(text, "secretsecret123") || !strings.Contains(text, `"note": "***"`) {
		t.Fatalf("log line was not redacted: %q", text)
	}
	if !strings.Contains(text, fmt.Sprintf(`"ts": %d`, testNow)) {
		t.Fatalf("ts = %q", text)
	}
}

func TestHookLogPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file permissions and umask are not available")
	}
	if os.Getenv("ROLLING_MEMORY_TEST_LOG_PERMISSIONS") != "1" {
		for _, mask := range []string{"000", "022", "077"} {
			t.Run("umask_"+mask, func(t *testing.T) {
				command := exec.Command("sh", "-c", "umask \"$1\"; exec \"$2\" -test.run '^TestHookLogPermissions$'", "sh", mask, os.Args[0])
				command.Env = append(os.Environ(), "ROLLING_MEMORY_TEST_LOG_PERMISSIONS=1")
				if output, err := command.CombinedOutput(); err != nil {
					t.Fatalf("permission subprocess: %v\n%s", err, output)
				}
			})
		}
		return
	}

	for _, fallback := range []bool{false, true} {
		name := "main"
		if fallback {
			name = "fallback"
		}
		t.Run(name, func(t *testing.T) {
			env := newTestEnv(t)
			path := env.log
			if fallback {
				path = filepath.Join(config.DefaultStateDir(), "hook.log")
			}
			directory := filepath.Dir(path)
			if err := os.MkdirAll(directory, 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			if err := os.Chmod(directory, 0o755); err != nil {
				t.Fatalf("chmod directory: %v", err)
			}
			cfg := &config.Config{Log: path}
			write := func() {
				if fallback {
					fallbackLog("test message")
				} else {
					logHook(cfg, "recall", map[string]any{"injected": 0})
				}
			}
			checkMode := func(path string, want os.FileMode) {
				t.Helper()
				info, err := os.Stat(path)
				if err != nil {
					t.Fatalf("stat %q: %v", path, err)
				}
				if info.Mode().Perm() != want {
					t.Errorf("%q mode = %o, want %o", path, info.Mode().Perm(), want)
				}
			}

			write()
			checkMode(path, 0o600)
			checkMode(directory, 0o755)
			if err := os.Chmod(path, 0o644); err != nil {
				t.Fatalf("chmod log: %v", err)
			}
			write()
			checkMode(path, 0o600)
			if err := os.WriteFile(path, bytes.Repeat([]byte("x"), maxLogBytes+1), 0o600); err != nil {
				t.Fatalf("seed oversized log: %v", err)
			}
			if err := os.Chmod(path, 0o644); err != nil {
				t.Fatalf("chmod oversized log: %v", err)
			}
			write()
			checkMode(path, 0o600)
			checkMode(path+".1", 0o600)
			checkMode(directory, 0o755)
			rotated, err := os.Stat(path + ".1")
			if err != nil || rotated.Size() != int64(maxLogBytes+1) {
				t.Fatalf("rotated log = %v, %v", rotated, err)
			}
		})
	}
}

func TestLogRotation(t *testing.T) {
	newTestEnv(t)
	cfg := loadConfig(t)
	if err := os.MkdirAll(filepath.Dir(cfg.Log), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	oversized := bytes.Repeat([]byte("x"), maxLogBytes+1)
	if err := os.WriteFile(cfg.Log, oversized, 0o644); err != nil {
		t.Fatalf("seed log: %v", err)
	}
	logHook(cfg, "recall", map[string]any{"injected": 0, "absent": 1})
	rotated, err := os.Stat(cfg.Log + ".1")
	if err != nil {
		t.Fatalf("rotated log: %v", err)
	}
	if rotated.Size() != int64(maxLogBytes+1) {
		t.Fatalf("rotated size = %d", rotated.Size())
	}
	lines := logLines(t, cfg.Log)
	if len(lines) != 1 || lines[0]["event"] != "recall" {
		t.Fatalf("fresh log lines = %#v", lines)
	}
}

// TestRecallHookDoesNotScanMountedBackground locks the prompt-hook boundary:
// the hook captures the prompt and injects the recall nudge, while background
// scanning belongs to the ingest worker. A changed mounted file must stay
// untouched in the vault no matter how long the hook has been idle.
func TestRecallHookDoesNotScanMountedBackground(t *testing.T) {
	env := newTestEnv(t)

	// 1. 挂载一个含初始文件的 L1 Background 目录。
	bgDir := filepath.Join(env.base, "mounted-docs")
	if err := os.MkdirAll(bgDir, 0o755); err != nil {
		t.Fatalf("mkdir bgDir: %v", err)
	}
	notePath := filepath.Join(bgDir, "spec.md")
	if err := os.WriteFile(notePath, []byte("初始规格说明 v1：系统采用盲化脉冲协议。\n"), 0o644); err != nil {
		t.Fatalf("write v1: %v", err)
	}
	mem := openMemory(t)
	tc, err := mem.LocalVault()
	if err != nil {
		t.Fatalf("LocalVault: %v", err)
	}
	if _, err := tc.AddBackgroundDir(bgDir); err != nil {
		t.Fatalf("AddBackgroundDir: %v", err)
	}
	if _, err := tc.SyncBackgroundDirs(context.Background()); err != nil {
		t.Fatalf("initial SyncBackgroundDirs: %v", err)
	}
	var initialSHA string
	if err := mem.Store.DB().QueryRow(`SELECT content_sha256 FROM rm_bg_files LIMIT 1`).Scan(&initialSHA); err != nil || initialSHA == "" {
		t.Fatalf("expected initial AddBackgroundDir to ingest v1 background file, got %q err=%v", initialSHA, err)
	}
	_ = mem.Close()

	// 2. 修改挂载目录下的文件（大小和 mtime 改变）。
	if err := os.WriteFile(notePath, []byte("更新规格说明 v2：后台扫描改由独立 worker 承担。\n"), 0o644); err != nil {
		t.Fatalf("write v2: %v", err)
	}
	futureTime := time.Now().Add(5 * time.Second)
	_ = os.Chtimes(notePath, futureTime, futureTime)

	// 3. 钩子执行后（无论距上次多久）都不得扫描或更新 L1 Background。
	for round := 0; round < 2; round++ {
		code, _, stderr := runHookMain(t, []string{"recall", "--json"}, `{"session_id":"s-bg","prompt":"检查规格"}`)
		if code != 0 {
			t.Fatalf("recall hook round %d failed: code=%d stderr=%s", round, code, stderr)
		}
	}

	memCheck := openMemory(t)
	defer memCheck.Close()
	var updatedSHA string
	if err := memCheck.Store.DB().QueryRow(`SELECT content_sha256 FROM rm_bg_files LIMIT 1`).Scan(&updatedSHA); err != nil {
		t.Fatalf("query rm_bg_files: %v", err)
	}
	if updatedSHA != initialSHA {
		t.Fatalf("prompt hook scanned background files: sha256 changed from %q to %q", initialSHA, updatedSHA)
	}
	var noeticCount int
	if err := memCheck.Store.DB().QueryRow(
		`SELECT COUNT(*) FROM rm_noetic`,
	).Scan(&noeticCount); err != nil {
		t.Fatalf("query rm_noetic: %v", err)
	}
	if noeticCount != 0 {
		t.Fatalf("recall hook must never crystallize user prompts into rm_noetic, got %d", noeticCount)
	}
}

func TestHookStats(t *testing.T) {
	newTestEnv(t)
	cfg := loadConfig(t)
	stats, err := HookStats(cfg)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if stats["exists"] != false || stats["lines"] != 0 {
		t.Fatalf("empty stats = %#v", stats)
	}
	logHook(cfg, "recall", map[string]any{"injected": 1})
	stats, err = HookStats(cfg)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if stats["exists"] != true || stats["lines"] != 1 {
		t.Fatalf("stats = %#v", stats)
	}
	last, ok := stats["last"].(map[string]any)
	if !ok || last["event"] != "recall" {
		t.Fatalf("last = %#v", stats["last"])
	}
	if stats["size"].(int64) <= 0 {
		t.Fatalf("size = %#v", stats["size"])
	}
}

func TestPyStrMatchesPythonShape(t *testing.T) {
	cases := []struct {
		value any
		want  string
	}{
		{nil, "None"},
		{"", ""},
		{true, "True"},
		{false, "False"},
		{json.Number("42"), "42"},
		{json.Number("1.5"), "1.5"},
		{float64(1000), "1000.0"},
		{3, "3"},
	}
	for _, item := range cases {
		if got := pyStr(item.value); got != item.want {
			t.Fatalf("pyStr(%#v) = %q, want %q", item.value, got, item.want)
		}
	}
}
