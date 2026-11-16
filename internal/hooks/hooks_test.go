// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rocareer/rolling-memory/internal/api"
	"github.com/rocareer/rolling-memory/internal/clock"
	"github.com/rocareer/rolling-memory/internal/config"
	"github.com/rocareer/rolling-memory/internal/episodicunit"
	"github.com/rocareer/rolling-memory/internal/ids"
	"github.com/rocareer/rolling-memory/internal/recall"
	"github.com/rocareer/rolling-memory/internal/store"
	"github.com/rocareer/rolling-memory/internal/thinclient"
	"github.com/rocareer/rolling-memory/internal/tokenzr"
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

func openMemory(t *testing.T) *api.Memory {
	t.Helper()
	memory, err := api.New(loadConfig(t))
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
	if _, err := memory.Remember(api.RememberRequest{
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
	if !strings.Contains(context, "[Rolling-Memory 4轮节律锚点]") {
		t.Fatalf("cadence reminder missing:\n%s", context)
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
	if _, err := mem.Store.DB().Exec(`PRAGMA foreign_keys = OFF; DROP TABLE IF EXISTS assertion; DROP TABLE IF EXISTS trace; DROP TABLE IF EXISTS node; PRAGMA foreign_keys = ON;`); err != nil {
		t.Fatalf("drop legacy tables: %v", err)
	}
	if mem.Store.HasLegacyGraph() {
		t.Fatal("expected HasLegacyGraph() == false after dropping assertion/trace/node")
	}
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

func TestStartBootContext(t *testing.T) {
	env := newTestEnv(t)
	seedCat(t)
	if _, err := config.WriteDefault(env.configPath); err != nil {
		t.Fatalf("write default: %v", err)
	}
	if _, err := config.SetConfigValue(env.configPath, "hooks.boot_context", "true"); err != nil {
		t.Fatalf("set boot_context: %v", err)
	}
	cfg := loadConfig(t)
	memory := openMemory(t)
	status, err := memory.Status(cfg.Scope)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	recent, err := memory.Recent(nil, nil, 3)
	if err != nil {
		t.Fatalf("recent: %v", err)
	}
	expected := fmt.Sprintf(
		"Rolling-Memory (local): %d active memory units · %d pending traces · %d conflicts · scope mem:hooks",
		status["active_assertions"], status["pending_traces"], status["conflicts_proposed"],
	)
	if status["active_assertions"] != int64(1) {
		t.Fatalf("seed should leave one active assertion: %#v", status["active_assertions"])
	}
	rendered := []string{}
	for _, item := range recent {
		rendered = append(rendered, item["content"].(string))
	}
	if len(rendered) > 0 {
		expected += "\n最近: " + strings.Join(rendered, "；")
	}

	code, stdout, stderr := runHookMain(t, []string{"start", "--json"}, `{"session_id":"s1"}`)
	if code != 0 {
		t.Fatalf("start exit = %d, stderr = %s", code, stderr)
	}
	decoded := map[string]any{}
	if err := json.Unmarshal([]byte(stdout), &decoded); err != nil {
		t.Fatalf("start stdout is not JSON: %v\n%s", err, stdout)
	}
	hook := decoded["hookSpecificOutput"].(map[string]any)
	if hook["hookEventName"] != "SessionStart" {
		t.Fatalf("hookEventName = %#v", hook["hookEventName"])
	}
	if hook["additionalContext"] != expected {
		t.Fatalf("boot context mismatch:\n got: %q\nwant: %q", hook["additionalContext"], expected)
	}

	// Without --json the same text is printed raw.
	code, stdout, _ = runHookMain(t, []string{"start"}, `{"session_id":"s1"}`)
	if code != 0 || strings.HasPrefix(stdout, "{") {
		t.Fatalf("text start: exit=%d stdout=%q", code, stdout)
	}
	if strings.TrimSuffix(stdout, "\n") != expected {
		t.Fatalf("text boot context = %q", stdout)
	}
}

func TestCloseReplaysPendingTracesAndStaysSilent(t *testing.T) {
	env := newTestEnv(t)
	memory := openMemory(t)
	if _, _, err := memory.Store.AddTrace("mem:hooks", "用户喜欢喝咖啡。",
		store.TraceOptions{Session: "s1", Author: "user"}); err != nil {
		t.Fatalf("trace: %v", err)
	}
	code, stdout, stderr := runHookMain(t, []string{"close", "--json"}, `{"session_id":"s1"}`)
	if code != 0 {
		t.Fatalf("close exit = %d, stderr = %s", code, stderr)
	}
	if stdout != "" {
		t.Fatalf("close must not print: %q", stdout)
	}
	lines := logLines(t, env.log)
	line := lines[len(lines)-1]
	if line["event"] != "close" || line["imported"].(float64) != 0 || line["messages"].(float64) != 0 {
		t.Fatalf("close log line = %#v", line)
	}
	if line["replayed"].(float64) != 1 {
		t.Fatalf("close should replay one pending trace: %#v", line)
	}
	var pending int64
	if err := memory.Store.DB().QueryRow(
		"SELECT count(*) FROM trace WHERE scope = ? AND processed = 0", "mem:hooks").Scan(&pending); err != nil {
		t.Fatalf("pending: %v", err)
	}
	if pending != 0 {
		t.Fatalf("pending traces after close = %d", pending)
	}
}

// TestCloseUploadsCurrentTurnToEpisodicLayer pins the thin-brain Stop contract: the
// closed turn (the prompt that opened it plus the answer that closed it) is ingested
// into `/open/memory/v1/ingest` with `layer="episodic"` in real time, the local vault
// keeps the plaintext, and the upload stays content-addressed so a repeated Stop can
// never fork a duplicate unit.
func TestCloseUploadsCurrentTurnToEpisodicLayer(t *testing.T) {
	env := newTestEnv(t)
	var mu sync.Mutex
	var packets []thinclient.BlindPulsePacket
	var wireBodies []string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/open/memory/v1/ingest" {
			http.Error(writer, "unexpected path "+request.URL.Path, http.StatusNotFound)
			return
		}
		raw, _ := io.ReadAll(request.Body)
		var pkt thinclient.BlindPulsePacket
		if err := json.Unmarshal(raw, &pkt); err != nil {
			http.Error(writer, err.Error(), http.StatusBadRequest)
			return
		}
		if _, _, err := thinclient.DecodePulsePayloadB64(pkt.AtomsB64); err != nil {
			http.Error(writer, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		packets = append(packets, pkt)
		wireBodies = append(wireBodies, string(raw))
		epoch := len(packets)
		mu.Unlock()
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(thinclient.CloudIngestResponse{
			UnitID: pkt.UnitID, Layer: pkt.Layer, Epoch: epoch,
			AddressURI:  "C6:L3:B001/C6:L2:B001/C6:L1:B001#00",
			WaveformBar: "▂▇▂▂▇█▁▆▆█▁█", KeptSentenceIndices: []int{0, 1},
		})
	}))
	t.Cleanup(server.Close)
	t.Setenv("ROLLING_MEMORY_MODE", "cloud")
	t.Setenv("ROLLING_MEMORY_BRAIN_URL", server.URL+"/open/memory/v1")
	// A data-plane credential is required for the HTTP transport to be selected;
	// the fake server never verifies it (without one the client silently falls
	// back to the local vault transport).
	t.Setenv("ROLLING_MEMORY_BRAIN_KEY", "mem_test_fake_key")

	path := writeTranscript(t, filepath.Join(env.base, "s1.jsonl"))
	payload := fmt.Sprintf(`{"session_id":"s1","transcript_path":%q}`, path)
	code, stdout, stderr := runHookMain(t, []string{"close"}, payload)
	if code != 0 || stdout != "" {
		t.Fatalf("thin close: exit=%d stdout=%q stderr=%s", code, stdout, stderr)
	}

	mu.Lock()
	if len(packets) != 2 {
		mu.Unlock()
		if raw, err := os.ReadFile(env.log); err == nil {
			t.Logf("hook log: %s", strings.TrimSpace(string(raw)))
		}
		t.Fatalf("expected two segment ingests (user + assistant), got %d", len(packets))
	}
	first, second := packets[0], packets[1]
	wires := append([]string(nil), wireBodies...)
	mu.Unlock()
	for _, pulse := range []thinclient.BlindPulsePacket{first, second} {
		if pulse.Layer != thinclient.LayerEpisodic {
			t.Fatalf("ingest layer = %q", pulse.Layer)
		}
		if !strings.HasPrefix(pulse.UnitID, "ep_") {
			t.Fatalf("ingest pulse unit id = %q", pulse.UnitID)
		}
		atoms, offsets, err := thinclient.DecodePulsePayloadB64(pulse.AtomsB64)
		if err != nil {
			t.Fatalf("DecodePulsePayloadB64: %v", err)
		}
		if len(atoms) == 0 || len(offsets) == 0 {
			t.Fatalf("ingest pulse carried no atoms/offsets: atoms=%d offsets=%d", len(atoms), len(offsets))
		}
	}
	if first.UnitID == second.UnitID {
		t.Fatalf("user and assistant segments must be distinct units, both = %q", first.UnitID)
	}
	for _, wire := range wires {
		for _, plaintext := range []string{"我喜欢猫", "记住了"} {
			if strings.Contains(wire, plaintext) {
				t.Fatalf("plaintext %q left the vault on the wire: %s", plaintext, wire)
			}
		}
	}

	cfg := loadConfig(t)
	connection, err := store.Open(cfg)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer connection.Close()
	rows, err := connection.DB().Query(`SELECT doc_id, layer, raw_text FROM thin_vault_docs WHERE layer = 'episodic' ORDER BY doc_id`)
	if err != nil {
		t.Fatalf("query vault: %v", err)
	}
	var docIDs []string
	var pointers []string
	for rows.Next() {
		var docID, layer, text string
		if rows.Scan(&docID, &layer, &text) != nil {
			continue
		}
		if layer != "episodic" {
			t.Fatalf("vault layer = %q", layer)
		}
		docIDs = append(docIDs, docID)
		pointers = append(pointers, text)
	}
	rows.Close()
	if len(docIDs) != 2 {
		t.Fatalf("expected two segment units in the vault, got %d (%v)", len(docIDs), docIDs)
	}
	for _, pointer := range pointers {
		// Zero-copy: each unit stores only its segment pointer.
		if !strings.HasPrefix(pointer, "@ref:jsonl:") || !strings.Contains(pointer, "#") {
			t.Fatalf("segment unit must keep a #<k> pointer, got %q", pointer)
		}
	}
	var lineage int
	if err := connection.DB().QueryRow(`SELECT count(*) FROM thin_vault_episodic_lineage`).Scan(&lineage); err != nil {
		t.Fatalf("lineage table: %v", err)
	}
	if lineage != 2 {
		t.Fatalf("expected lineage rows for both segments, got %d", lineage)
	}
	var legacyTableCount int64
	if err := connection.DB().QueryRow(
		`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name IN ('trace', 'node', 'assertion', 'vector', 'node_vector')`,
	).Scan(&legacyTableCount); err != nil {
		t.Fatalf("check legacy tables: %v", err)
	}
	if legacyTableCount != 5 {
		t.Fatalf("expected all 5 legacy tables to be preserved, found %d", legacyTableCount)
	}

	lines := logLines(t, env.log)
	line := lines[len(lines)-1]
	if line["event"] != "close" || line["messages"].(float64) != 2 || line["imported"].(float64) != 2 {
		t.Fatalf("thin close log line = %#v", line)
	}
	if line["uploaded"].(float64) != 2 || line["units"].(float64) != 2 {
		t.Fatalf("thin close upload receipt = %#v", line)
	}

	// A repeated Stop re-reads the same closed turn; content addressing keeps it two units.
	code, stdout, stderr = runHookMain(t, []string{"close"}, payload)
	if code != 0 || stdout != "" {
		t.Fatalf("repeat thin close: exit=%d stdout=%q stderr=%s", code, stdout, stderr)
	}
	mu.Lock()
	defer mu.Unlock()
	seen := map[string]bool{}
	for _, packet := range packets {
		seen[packet.UnitID] = true
	}
	if len(packets) != 4 || len(seen) != 2 {
		t.Fatalf("repeated Stop must reuse the content-addressed units, got %+v", packets)
	}
}

// TestCloseStripsThinkingAndToolEnvelopes pins the L2 pulse denoise contract: the
// closed turn uploaded as `episodic` carries what the speakers said, never the
// `[thinking]` / `[tool_use …]` / `[tool_result]` envelopes nor injected context.
func TestCloseStripsThinkingAndToolEnvelopes(t *testing.T) {
	env := newTestEnv(t)
	var mu sync.Mutex
	ingests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/open/memory/v1/ingest" {
			http.Error(writer, "unexpected path "+request.URL.Path, http.StatusNotFound)
			return
		}
		var pkt thinclient.BlindPulsePacket
		if err := json.NewDecoder(request.Body).Decode(&pkt); err != nil {
			http.Error(writer, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		ingests++
		mu.Unlock()
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(thinclient.CloudIngestResponse{
			UnitID: pkt.UnitID, Layer: pkt.Layer, Epoch: 1,
			AddressURI: "C6:L3:B001/C6:L2:B001/C6:L1:B001#00", WaveformBar: "▂▇▂",
			KeptSentenceIndices: []int{0},
		})
	}))
	t.Cleanup(server.Close)
	t.Setenv("ROLLING_MEMORY_MODE", "cloud")
	t.Setenv("ROLLING_MEMORY_BRAIN_URL", server.URL+"/open/memory/v1")
	t.Setenv("ROLLING_MEMORY_BRAIN_KEY", "mem_test_fake_key")

	path := filepath.Join(env.base, "noisy.jsonl")
	content := strings.Join([]string{
		`{"type":"user","sessionId":"s1","timestamp":"2026-10-20T10:00:00",` +
			`"message":{"role":"user","content":[{"type":"text","text":"<system-reminder>injected banner</system-reminder>请把缓存按 docID 失效。"}]}}`,
		`{"type":"assistant","sessionId":"s1","timestamp":"2026-10-20T10:00:10",` +
			`"message":{"role":"assistant","content":[` +
			`{"type":"thinking","thinking":"internal monologue"},` +
			`{"type":"text","text":"先确认缓存失效断言。\n[thinking] internal monologue A\n[tool_use Bash] {\"cmd\":\"go test ./...\"}\n[tool_result] ok  github.com/rocareer/rolling-memory/internal/ids\n\n结论：缓存按 docID 失效。"},` +
			`{"type":"tool_use","name":"Bash","input":{"cmd":"go test ./..."}}]}}`,
		"",
	}, "\n")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("transcript: %v", err)
	}
	payload := fmt.Sprintf(`{"session_id":"s1","transcript_path":%q}`, path)
	code, stdout, stderr := runHookMain(t, []string{"close"}, payload)
	if code != 0 || stdout != "" {
		t.Fatalf("thin close: exit=%d stdout=%q stderr=%s", code, stdout, stderr)
	}
	mu.Lock()
	got := ingests
	mu.Unlock()
	if got != 2 {
		t.Fatalf("expected two segment ingests (user + assistant), got %d", got)
	}

	cfg := loadConfig(t)
	connection, err := store.Open(cfg)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer connection.Close()
	rows, err := connection.DB().Query(
		`SELECT raw_text FROM thin_vault_docs WHERE layer = 'episodic' ORDER BY doc_id`,
	)
	if err != nil {
		t.Fatalf("query episodic rows: %v", err)
	}
	var pointers []string
	for rows.Next() {
		var pointer string
		if rows.Scan(&pointer) == nil {
			pointers = append(pointers, pointer)
		}
	}
	rows.Close()
	if len(pointers) != 2 {
		t.Fatalf("expected two pointer units, got %v", pointers)
	}
	var storedText strings.Builder
	for _, pointer := range pointers {
		live, err := thinclient.ResolveEpisodicPointerLive(pointer)
		if err != nil {
			t.Fatalf("resolve %q: %v", pointer, err)
		}
		storedText.WriteString(live)
		storedText.WriteString("\n")
	}
	for _, speech := range []string{"[user] 请把缓存按 docID 失效。", "[assistant] 先确认缓存失效断言。", "结论：缓存按 docID 失效。"} {
		if !strings.Contains(storedText.String(), speech) {
			t.Fatalf("episodic pulse lost speech %q: %q", speech, storedText.String())
		}
	}
	for _, noise := range []string{"[thinking]", "[tool_use", "[tool_result]", "internal monologue", "system-reminder"} {
		if strings.Contains(storedText.String(), noise) {
			t.Fatalf("episodic pulse kept noise %q: %q", noise, storedText.String())
		}
	}
}

// TestCurrentTurnUnitsAreSegments pins the unit contract: the closed turn becomes
// one unit per speech segment (never one blob), the user instruction survives even
// next to a 100k-token work log, and an oversized segment is chunked at sentence
// boundaries instead of being truncated.
func TestCurrentTurnUnitsAreSegments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "turn.jsonl")
	workLog := strings.Repeat("正在定位失败断言并复跑全部包。", 2400)
	content := strings.Join([]string{
		`{"type":"user","sessionId":"s1","timestamp":"2026-10-20T10:00:00","message":{"role":"user","content":"继续执行你之前没有执行完的任务 中断的任务"}}`,
		`{"type":"assistant","sessionId":"s1","timestamp":"2026-10-20T10:00:10","message":{"role":"assistant","content":[` +
			`{"type":"text","text":"先确认断言。"},` +
			`{"type":"text","text":"` + workLog + `"}]}}`,
		"",
	}, "\n")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write turn: %v", err)
	}
	units, _ := currentTurnUnits("mem:test", "s1", map[string]any{"transcript_path": path})
	if len(units) < 3 {
		t.Fatalf("expected the user segment plus chunked work-log segments, got %d", len(units))
	}
	if !strings.HasPrefix(units[0].Text, "[user] 继续执行你之前没有执行完的任务") {
		t.Fatalf("user instruction must be its own unit, got %q", units[0].Text)
	}
	if units[0].RefPointer != "@ref:jsonl:"+path+":0#0" {
		t.Fatalf("user segment pointer = %q", units[0].RefPointer)
	}
	if units[1].Text != "[assistant] 先确认断言。" {
		t.Fatalf("second segment = %q", units[1].Text)
	}
	// The oversized work log is split at sentence boundaries into zero-copy chunks:
	// every chunk stays under the rung and carries `#<segment>.<chunk>`, so delivery
	// resolves it live from the same transcript instead of storing plaintext.
	chunked := 0
	for index, unit := range units[2:] {
		if tokenzr.Count(unit.Text) > episodicunit.TokenBudget {
			t.Fatalf("chunk wider than the rung: %d tokens", tokenzr.Count(unit.Text))
		}
		want := fmt.Sprintf("@ref:jsonl:%s:0#2.%d", path, index)
		if unit.RefPointer != want {
			t.Fatalf("chunk %d pointer = %q, want %q", index, unit.RefPointer, want)
		}
		if unit.SegmentIndex != 2 || unit.ChunkIndex != index {
			t.Fatalf("chunk %d coords = (%d,%d), want (2,%d)", index, unit.SegmentIndex, unit.ChunkIndex, index)
		}
		chunked++
	}
	if chunked == 0 {
		t.Fatalf("expected the oversized segment to be chunked")
	}
	if units[0].TurnRef == "" || units[1].TurnRef == "" {
		t.Fatalf("every unit must carry its turn provenance: %+v", units)
	}
}

// TestLiveUpstreamStopHookEpisodicUpload_UserID10 drives the real Stop hook against the live
// Autonoetic Engine (`/open/memory/v1`, user_id=10): the closed turn is ingested as `episodic` in
// real time and the next blind query returns that unit with the plaintext resolved from the local
// vault. Skips when the local engine is not reachable.
func TestLiveUpstreamStopHookEpisodicUpload_UserID10(t *testing.T) {
	baseURL := thinclient.DefaultUpstreamBaseURL
	health, err := http.Get(baseURL + "/health")
	if err != nil {
		t.Skipf("live engine %s not reachable (%v); skipping live Stop hook test", baseURL, err)
	}
	_ = health.Body.Close()
	if health.StatusCode != http.StatusOK {
		t.Skipf("live engine %s returned %d; skipping live Stop hook test", baseURL, health.StatusCode)
	}

	env := newTestEnv(t)
	marker := "LIVE-STOP-" + ids.SHA256Hex(fmt.Sprintf("%d", time.Now().UnixNano()))[:10]
	// The live Stop hook needs a real data-plane credential from the host
	// environment; the environment carries the signed key the CLI itself uses.
	liveKey := strings.TrimSpace(os.Getenv("ROLLING_MEMORY_BRAIN_KEY"))
	if liveKey == "" {
		t.Skip("ROLLING_MEMORY_BRAIN_KEY is not set; skipping live Stop hook test")
	}
	t.Setenv("ROLLING_MEMORY_MODE", "cloud")
	t.Setenv("ROLLING_MEMORY_BRAIN_URL", baseURL)
	t.Setenv("ROLLING_MEMORY_USER_ID", thinclient.DefaultUpstreamDevUserID)
	t.Setenv("ROLLING_MEMORY_BRAIN_KEY", liveKey)
	t.Setenv("ROLLING_MEMORY_SCOPE", "mem:live-hooks")

	path := filepath.Join(env.base, "live-stop.jsonl")
	transcriptText := strings.Join([]string{
		fmt.Sprintf(`{"type":"user","timestamp":"2026-11-06T03:00:00","message":{"role":"user","content":[{"type":"text","text":"%s 洛灵自知记忆引擎 Stop 钩子需要把当前轮会话实时上传为 episodic 内容。"}]}}`, marker),
		`{"type":"assistant","timestamp":"2026-11-06T03:00:20","message":{"role":"assistant","content":[{"type":"text","text":"已确认：Stop 钩子调用 /open/memory/v1/ingest，layer=episodic，明文锁本地金库。"}]}}`,
		"",
	}, "\n")
	if err := os.WriteFile(path, []byte(transcriptText), 0o644); err != nil {
		t.Fatalf("transcript: %v", err)
	}
	payload := fmt.Sprintf(`{"session_id":"live-stop","transcript_path":%q}`, path)
	code, stdout, stderr := runHookMain(t, []string{"close"}, payload)
	if code != 0 || stdout != "" {
		t.Fatalf("live close: exit=%d stdout=%q stderr=%s", code, stdout, stderr)
	}
	lines := logLines(t, env.log)
	line := lines[len(lines)-1]
	unit, _ := line["unit"].(string)
	if line["uploaded"].(float64) != 1 || !strings.HasPrefix(unit, "ep_") || line["address"] == "" {
		t.Fatalf("live Stop receipt = %#v", line)
	}

	memory := openMemory(t)
	client, err := memory.ThinClient()
	if err != nil {
		t.Fatalf("thin client: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	hits, err := client.Recall(ctx, marker+" 洛灵自知记忆引擎 Stop 钩子 episodic 会话", 5)
	if err != nil {
		t.Fatalf("live blind recall: %v", err)
	}
	for _, hit := range hits {
		if hit.UnitID == unit && strings.Contains(hit.RawPlaintext, marker) {
			return
		}
	}
	t.Fatalf("live engine did not return the uploaded unit %s; hits = %+v", unit, hits)
}

// Missing transcript keeps the hook fail-open -- exit 0, nothing on stdout.
func TestCloseMissingTranscriptFailsOpen(t *testing.T) {
	env := newTestEnv(t)
	payload := fmt.Sprintf(`{"session_id":"x","transcript_path":%q}`, filepath.Join(env.base, "nope.jsonl"))
	code, stdout, stderr := runHookMain(t, []string{"close"}, payload)
	if code != 0 || stdout != "" {
		t.Fatalf("missing transcript close: exit=%d stdout=%q stderr=%s", code, stdout, stderr)
	}
}

func TestCloseCheckpointsUsbProfile(t *testing.T) {
	env := newTestEnv(t)
	if _, err := config.WriteDefault(env.configPath); err != nil {
		t.Fatalf("write default: %v", err)
	}
	if _, err := config.SetConfigValue(env.configPath, "storage.profile", "usb"); err != nil {
		t.Fatalf("set profile: %v", err)
	}
	seeder := openMemory(t)
	if _, _, err := seeder.Store.AddTrace("mem:hooks", "用户搬到北京。",
		store.TraceOptions{Session: "s1", Author: "user"}); err != nil {
		t.Fatalf("trace: %v", err)
	}
	// The hook must be the last connection for the stop checkpoint to truncate.
	if err := seeder.Close(); err != nil {
		t.Fatalf("close seeder: %v", err)
	}
	code, stdout, stderr := runHookMain(t, []string{"close"}, `{"session_id":"s1"}`)
	if code != 0 || stdout != "" {
		t.Fatalf("usb close: exit=%d stdout=%q stderr=%s", code, stdout, stderr)
	}
	// The stop checkpoint runs inside store.Close; the last connection is gone,
	// so no WAL side files may survive.
	for _, suffix := range []string{"-wal", "-shm"} {
		if info, err := os.Stat(env.db + suffix); err == nil && info.Size() > 0 {
			t.Fatalf("%s not checkpointed: %d bytes", suffix, info.Size())
		}
	}
	cfg := loadConfig(t)
	connection, err := store.Open(cfg)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer connection.Close()
	var integrity string
	if err := connection.DB().QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil {
		t.Fatalf("integrity: %v", err)
	}
	if integrity != "ok" {
		t.Fatalf("integrity = %q", integrity)
	}
}

// writeTranscript writes the two synthetic turns of one coding session.
func writeTranscript(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	content := strings.Join([]string{
		`{"type":"user","sessionId":"s1","timestamp":"2026-10-20T10:00:00",` +
			`"message":{"role":"user","content":[{"type":"text","text":"我喜欢猫。我的猫叫小白。"}]}}`,
		`{"type":"assistant","sessionId":"s1","timestamp":"2026-10-20T10:00:10",` +
			`"message":{"role":"assistant","content":[{"type":"text","text":"记住了。"}]}}`,
		"",
	}, "\n")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("transcript: %v", err)
	}
	return path
}

func TestImportTurnsMovesTheCursor(t *testing.T) {
	env := newTestEnv(t)
	path := writeTranscript(t, filepath.Join(env.base, "s1.jsonl"))
	memory := openMemory(t)

	// A relative path resolves against payload cwd; the session falls back to
	// the transcript file stem.
	receipt, err := importTurns(memory, map[string]any{
		"transcript_path": "s1.jsonl", "cwd": env.base,
	})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if receipt["messages"] != 2 || receipt["inserted"] != 2 || receipt["session"] != "s1" {
		t.Fatalf("receipt = %#v", receipt)
	}
	var traces int
	if err := memory.Store.DB().QueryRow(
		"SELECT count(*) FROM trace WHERE scope = ? AND session = ?", "mem:hooks", "s1").Scan(&traces); err != nil {
		t.Fatalf("count traces: %v", err)
	}
	if traces != 2 {
		t.Fatalf("imported traces = %d", traces)
	}

	// The cursor is honoured, so a second run imports nothing new.
	again, err := importTurns(memory, map[string]any{"transcript_path": path})
	if err != nil {
		t.Fatalf("second import: %v", err)
	}
	if again["messages"] != 0 || again["inserted"] != 0 || again["offset"] != 2 {
		t.Fatalf("second receipt = %#v", again)
	}

	// An explicit session id wins over the file stem and has its own cursor.
	explicit, err := importTurns(memory, map[string]any{
		"transcriptPath": path, "sessionId": "explicit",
	})
	if err != nil {
		t.Fatalf("explicit import: %v", err)
	}
	if explicit["session"] != "explicit" || explicit["messages"] != 2 {
		t.Fatalf("explicit receipt = %#v", explicit)
	}

	// Missing files keep the standard receipt shape.
	missing, err := importTurns(memory, map[string]any{
		"transcript_path": filepath.Join(env.base, "nope.jsonl"),
	})
	if err != nil {
		t.Fatalf("missing import: %v", err)
	}
	if missing["messages"] != 0 || missing["inserted"] != 0 || missing["missing"] == nil {
		t.Fatalf("missing receipt = %#v", missing)
	}

	// No transcript path at all is a no-op with the same minimal shape.
	none, err := importTurns(memory, map[string]any{})
	if err != nil {
		t.Fatalf("pathless import: %v", err)
	}
	if none["messages"] != 0 || none["inserted"] != 0 {
		t.Fatalf("pathless receipt = %#v", none)
	}
}

// TestCloseImportsAndRecallHookStaysSilent: the close event replays imported
// turns, while the recall event never runs automatic recall — even on a prompt
// the imported transcript would answer, it only stays silent (retrieval is
// agent-driven through the MCP tool).
func TestCloseImportsAndRecallHookStaysSilent(t *testing.T) {
	env := newTestEnv(t)
	path := writeTranscript(t, filepath.Join(env.base, "s1.jsonl"))
	payload := fmt.Sprintf(`{"session_id":"s1","transcript_path":%q}`, path)
	code, stdout, stderr := runHookMain(t, []string{"close"}, payload)
	if code != 0 || stdout != "" {
		t.Fatalf("close: exit=%d stdout=%q stderr=%s", code, stdout, stderr)
	}
	lines := logLines(t, env.log)
	line := lines[len(lines)-1]
	if line["imported"].(float64) != 2 || line["messages"].(float64) != 2 {
		t.Fatalf("close import receipt = %#v", line)
	}
	if line["replayed"].(float64) != 2 {
		t.Fatalf("close replay receipt = %#v", line)
	}

	memory := openMemory(t)
	status, err := memory.Status("mem:hooks")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	counts, _ := status["counts"].(map[string]int)
	if counts["trace"] < 2 {
		t.Fatalf("trace count = %#v", status["counts"])
	}

	code, stdout, stderr = runHookMain(t, []string{"recall", "--json"},
		`{"session_id":"s1","prompt":"我的猫叫什么？"}`)
	if code != 0 || strings.TrimSpace(stdout) != "" {
		t.Fatalf("recall hook must stay silent: exit=%d stdout=%q stderr=%s", code, stdout, stderr)
	}
	code, stdout, _ = runHookMain(t, []string{"recall", "--json"},
		`{"session_id":"s1","prompt":"我的猫叫什么？"}`)
	if code != 0 || strings.TrimSpace(stdout) != "" {
		t.Fatalf("repeat prompt must stay silent: exit=%d stdout=%q", code, stdout)
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

// TestRecallHookAutoSyncsMountedBackgroundWithCooldown 锁住增量冷却契约：
// UserPromptSubmit (recall) 钩子在有挂载 L1 Background 目录且距上次扫描 >= 60s 时
// 自动增量触发 SyncBackgroundDirs，而在 60s 冷却期内不重复扫描。
func TestRecallHookAutoSyncsMountedBackgroundWithCooldown(t *testing.T) {
	env := newTestEnv(t)
	var mu sync.Mutex
	var bgIngests []thinclient.BlindPulsePacket
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/open/memory/v1/ingest":
			raw, _ := io.ReadAll(request.Body)
			var pkt thinclient.BlindPulsePacket
			_ = json.Unmarshal(raw, &pkt)
			mu.Lock()
			if pkt.Layer == thinclient.LayerBackground {
				bgIngests = append(bgIngests, pkt)
			}
			epoch := len(bgIngests)
			mu.Unlock()
			_ = json.NewEncoder(writer).Encode(thinclient.CloudIngestResponse{
				UnitID: pkt.UnitID, Layer: pkt.Layer, Epoch: epoch,
				AddressURI: "C6:L1:B001#00", WaveformBar: "▂▇█", KeptSentenceIndices: []int{0},
			})
		case "/open/memory/v1/query":
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"epistemic_state": "non_existent",
				"hits":            []any{},
			})
		case "/open/memory/v1/purge":
			_ = json.NewEncoder(writer).Encode(map[string]any{"success": true})
		default:
			http.Error(writer, "unexpected path "+request.URL.Path, http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	t.Setenv("ROLLING_MEMORY_MODE", "cloud")
	t.Setenv("ROLLING_MEMORY_BRAIN_URL", server.URL+"/open/memory/v1")
	t.Setenv("ROLLING_MEMORY_BRAIN_KEY", "mem_test_fake_key")

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
	tc, err := mem.ThinClient()
	if err != nil {
		t.Fatalf("ThinClient: %v", err)
	}
	if _, err := tc.AddBackgroundDir(bgDir); err != nil {
		t.Fatalf("AddBackgroundDir: %v", err)
	}
	if _, err := tc.SyncBackgroundDirs(context.Background()); err != nil {
		t.Fatalf("initial SyncBackgroundDirs: %v", err)
	}
	mu.Lock()
	initialIngests := len(bgIngests)
	mu.Unlock()
	if initialIngests == 0 {
		t.Fatal("expected initial AddBackgroundDir to ingest at least 1 background chunk")
	}

	// 2. 修改挂载目录下的文件（大小和 mtime 改变）。
	if err := os.WriteFile(notePath, []byte("更新规格说明 v2：新增 UserPromptSubmit 60s 冷却自动增量同步机制。\n"), 0o644); err != nil {
		t.Fatalf("write v2: %v", err)
	}
	futureTime := time.Now().Add(5 * time.Second)
	_ = os.Chtimes(notePath, futureTime, futureTime)

	// 3. 冷却期内（距上次 10s < 60s）：触发 recall 钩子不应扫描或上行。
	nowTS := ids.UTCNow()
	_, err = mem.Store.DB().Exec(`INSERT OR REPLACE INTO thin_vault_meta(k, v) VALUES (?, ?)`,
		backgroundHookMetaKey, []byte(fmt.Sprintf("%d", nowTS-10)))
	if err != nil {
		t.Fatalf("seed cooldown meta: %v", err)
	}
	_ = mem.Close()

	code, _, stderr := runHookMain(t, []string{"recall", "--json"}, `{"session_id":"s-bg","prompt":"检查规格"}`)
	if code != 0 {
		t.Fatalf("recall within cooldown failed: code=%d stderr=%s", code, stderr)
	}
	mu.Lock()
	afterCooldownIngests := len(bgIngests)
	mu.Unlock()
	if afterCooldownIngests != initialIngests {
		t.Fatalf("cooldown violated: ingests grew from %d to %d inside 60s window", initialIngests, afterCooldownIngests)
	}

	// 4. 冷却期已过（距上次 65s >= 60s）：触发 recall 钩子必须自动增量同步 v2 文件。
	mem2 := openMemory(t)
	_, err = mem2.Store.DB().Exec(`INSERT OR REPLACE INTO thin_vault_meta(k, v) VALUES (?, ?)`,
		backgroundHookMetaKey, []byte(fmt.Sprintf("%d", ids.UTCNow()-65)))
	if err != nil {
		t.Fatalf("seed expired cooldown meta: %v", err)
	}
	_ = mem2.Close()

	code, _, stderr = runHookMain(t, []string{"recall", "--json"}, `{"session_id":"s-bg","prompt":"再次检查规格"}`)
	if code != 0 {
		t.Fatalf("recall after cooldown failed: code=%d stderr=%s", code, stderr)
	}
	mu.Lock()
	finalIngests := len(bgIngests)
	mu.Unlock()
	if finalIngests <= initialIngests {
		t.Fatalf("expected recall hook after cooldown to auto-sync modified L1 file: initial=%d final=%d", initialIngests, finalIngests)
	}

	// 验证本地 `thin_vault_bg_chunks` 已更新为最新上行的 unit_id。
	mu.Lock()
	latestUnitID := bgIngests[len(bgIngests)-1].UnitID
	mu.Unlock()
	mem3 := openMemory(t)
	var chunkCount int
	if err := mem3.Store.DB().QueryRow(
		`SELECT COUNT(*) FROM thin_vault_bg_chunks WHERE unit_id = ?`, latestUnitID,
	).Scan(&chunkCount); err != nil {
		t.Fatalf("query thin_vault_bg_chunks: %v", err)
	}
	if chunkCount != 1 {
		t.Fatalf("expected updated unit %s in thin_vault_bg_chunks, got %d", latestUnitID, chunkCount)
	}
	var noeticCount int
	if err := mem3.Store.DB().QueryRow(
		`SELECT COUNT(*) FROM thin_vault_noetic`,
	).Scan(&noeticCount); err != nil {
		t.Fatalf("query thin_vault_noetic: %v", err)
	}
	if noeticCount != 0 {
		t.Fatalf("RAME recall hook must never crystallize user prompts into thin_vault_noetic, got %d", noeticCount)
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

func TestPyJoinAndStem(t *testing.T) {
	joins := []struct{ base, path, want string }{
		{"/a/b", "c.jsonl", "/a/b/c.jsonl"},
		{"/a/b/", "c.jsonl", "/a/b/c.jsonl"},
		{"/a/b", "./c.jsonl", "/a/b/c.jsonl"},
		{"/a/b", "../c.jsonl", "/a/b/../c.jsonl"},
		{"/a", "c//d.jsonl", "/a/c/d.jsonl"},
		{"a", "/abs.jsonl", "/abs.jsonl"},
	}
	for _, item := range joins {
		if got := pyJoin(item.base, item.path); got != item.want {
			t.Fatalf("pyJoin(%q, %q) = %q, want %q", item.base, item.path, got, item.want)
		}
	}
	stems := map[string]string{
		"s1.jsonl": "s1", ".bashrc": ".bashrc", "a.tar.gz": "a.tar",
		"a.": "a.", "noext": "noext",
	}
	for path, want := range stems {
		if got := pathStem(path); got != want {
			t.Fatalf("pathStem(%q) = %q, want %q", path, got, want)
		}
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

func TestSituationalBootAndSingleBigramCollisionSilence(t *testing.T) {
	env := newTestEnv(t)
	wsDir := filepath.Join(env.base, "Rolling-Memory")
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		t.Fatalf("mkdir ws: %v", err)
	}
	mem := openMemory(t)
	now := testNow - 30
	_, _, _ = mem.Store.AddTrace("mem:hooks", "请优化 hooks.go 的三维时空态势唤醒机制", store.TraceOptions{
		Session: "prev-sess", Turn: 1, Kind: "message", Author: "user", Audience: "shared",
		OccurredAt: &now, Meta: map[string]any{"locus": "rocareer/Rolling-Memory"},
	})
	nowReply := testNow - 20
	_, _, _ = mem.Store.AddTrace("mem:hooks", "已完成 internal/hooks/hooks.go 三维态势唤醒改造", store.TraceOptions{
		Session: "prev-sess", Turn: 2, Kind: "message", Author: "assistant", Audience: "shared",
		OccurredAt: &nowReply, Meta: map[string]any{"locus": "rocareer/Rolling-Memory"},
	})
	if _, err := mem.Remember(api.RememberRequest{
		Content:    strPtr("增量批实施卡首版落于 docs/zh-CN/design.md。"),
		AnchorText: strPtr("增量批实施卡首版落于 docs/zh-CN/design.md。"),
		Assertion: map[string]any{
			"s": map[string]any{"label": "增量批实施卡首版"},
			"x": "落于",
			"o": map[string]any{"literal": "docs/zh-CN/design.md"},
		},
		Author: "user", AutoSync: boolPtr(false),
	}); err != nil {
		t.Fatalf("remember card: %v", err)
	}

	// 1. SessionStart in workspace must include [刚干了什么 · 时序尾] and [记忆代理四问闭环]
	if _, err := config.WriteDefault(env.configPath); err != nil {
		t.Fatalf("write default: %v", err)
	}
	if _, err := config.SetConfigValue(env.configPath, "hooks.boot_context", "true"); err != nil {
		t.Fatalf("set boot_context: %v", err)
	}
	startPayload, _ := json.Marshal(map[string]any{
		"session_id": "new-sess-1",
		"cwd":        wsDir,
	})
	code, startOut, _ := runHookMain(t, []string{"start"}, string(startPayload))
	if code != 0 || !strings.Contains(startOut, "[刚干了什么 · 历史会话留痕") || !strings.Contains(startOut, "[记忆代理四问闭环与防幻觉准则]") || !strings.Contains(startOut, "历史记忆≠本轮已做") {
		t.Fatalf("unexpected SessionStart situational boot output: %q", startOut)
	}

	// 2. Accidental single-bigram collision ("全部同意 推进实施" sharing only 2-char "实施" with "增量批实施卡首版")
	// MUST be silenced by pure mathematical sequence coverage (0 word lists). The hook no longer
	// recalls, so these assertions pin the retrieval + specificity gate the agent-driven path uses.
	if kept := hookGateRecall(t, mem, wsDir, "new-sess-1", "全部同意 推进实施"); len(kept) != 0 {
		t.Fatalf("expected single-bigram collision prompt to be silenced, got: %s", bandsText(kept))
	}

	// 3. Specific query with contiguous sequence match ("增量批实施卡首版落于哪里") MUST surface its band.
	keptCard := hookGateRecall(t, mem, wsDir, "new-sess-1", "增量批实施卡首版落于哪里")
	if !strings.Contains(bandsText(keptCard), "增量批实施卡首版") {
		t.Fatalf("expected specific entity prompt to recall its band, got: %s", bandsText(keptCard))
	}

	// 4. Scattered 2-char domain words ("开源", "记忆", "插件") in a long architectural prompt
	// MUST NOT trigger a false-positive recall of an unrelated long mission band that merely
	// shares a single 2-char word in its title and scattered 2-char words in its body.
	if _, err := mem.Remember(api.RememberRequest{
		Content:    strPtr("Rolling-Memory 开源工作使命、三仓拓扑与零泄漏纪律：工作使命登记于本地 AGENTS.md，世界开源的记忆系统与全宿主插件。"),
		AnchorText: strPtr("工作使命登记于本地 AGENTS.md，世界开源的记忆系统与全宿主插件。"),
		Assertion: map[string]any{
			"s": map[string]any{"label": "Rolling-Memory 开源工作使命、三仓拓扑与零泄漏纪律"},
			"x": "裁定为",
			"o": map[string]any{"literal": "工作使命登记于本地 AGENTS.md，世界开源的记忆系统与全宿主插件。"},
		},
		Author: "user", AutoSync: boolPtr(false),
	}); err != nil {
		t.Fatalf("remember mission: %v", err)
	}
	genericPayload := "对照 同业 各种 开源记忆插件 思考 我们的机制 是否 健壮合理"
	if kept := hookGateRecall(t, mem, wsDir, "new-sess-2", genericPayload); len(kept) != 0 {
		t.Fatalf("expected scattered 2-char domain words in long prompt to be silenced, got: %s", bandsText(kept))
	}

	// 5. Specificity gate test: 2-letter ASCII syllable fragments ("ac", "ti", "ct" from "artifact")
	// MUST NOT trigger false recalls on unrelated camelCase test identifiers.
	if kept := recall.FilterBandsBySpecificity("artifact file db", "artifact file db", "Rolling-Memory", []map[string]any{
		{
			"title": "带·TestProactiveLiveBridgeDB",
			"nodes": []map[string]any{{"id": "a_1", "s": "TestProactiveLiveBridgeDB", "x": "验证", "o": "桥接测试", "status": "active"}},
		},
	}); len(kept) != 0 {
		t.Fatalf("expected 0 bands on 2-letter ASCII syllable fragments, got %d", len(kept))
	}
	if got := stripDegenerateTail("再看 stopword_filter.go！]]]]]]]]]]]]]]]]]]]]]]]]]]"); got != "再看 stopword_filter.go！" {
		t.Fatalf("stripDegenerateTail = %q", got)
	}

	// 6. Pre-retrieval subject probe + compact technical acronym ("w3:roi") must recall
	// "Rolling-Memory 独立 ROI 监控屏", while a >=6-hanzi title ("机械核零 LLM 读链") sharing only
	// a single 3-letter word ("llm") with zero ideographic overlap must stay silent.
	if _, err := mem.Remember(api.RememberRequest{
		Content:    strPtr("Rolling-Memory 独立 ROI 监控屏落位于 admin/web/src/views/admin/dashboard/roi.vue。"),
		AnchorText: strPtr("Rolling-Memory 独立 ROI 监控屏落位于 admin/web/src/views/admin/dashboard/roi.vue。"),
		Assertion: map[string]any{
			"s": map[string]any{"label": "Rolling-Memory 独立 ROI 监控屏"},
			"x": "落位于",
			"o": map[string]any{"literal": "admin/web/src/views/admin/dashboard/roi.vue"},
		},
		Author: "user", AutoSync: boolPtr(false),
	}); err != nil {
		t.Fatalf("remember roi: %v", err)
	}
	if _, err := mem.Remember(api.RememberRequest{
		Content:    strPtr("机械核零 LLM 读链定义为种子门到图走与结构排序。"),
		AnchorText: strPtr("机械核零 LLM 读链定义为种子门到图走与结构排序。"),
		Assertion: map[string]any{
			"s": map[string]any{"label": "机械核零 LLM 读链"},
			"x": "定义为",
			"o": map[string]any{"literal": "种子门到图走与结构排序"},
		},
		Author: "user", AutoSync: boolPtr(false),
	}); err != nil {
		t.Fatalf("remember llm: %v", err)
	}
	keptROI := hookGateRecall(t, mem, wsDir, "new-sess-3", "检查 ROI 监控 记录 还是有应该检索的 被静默了")
	if !strings.Contains(bandsText(keptROI), "admin/web/src/views/admin/dashboard/roi.vue") {
		t.Fatalf("expected compact technical w3:roi subject to be recalled, got: %s", bandsText(keptROI))
	}
	keptLLM := hookGateRecall(t, mem, wsDir, "new-sess-3", "就是 可以随仓发布吗? 修改配置文件(各家key 和 自己的 llm key等) 直接复现?")
	if strings.Contains(bandsText(keptLLM), "机械核零 LLM 读链") {
		t.Fatalf("expected >=6-hanzi title with lone w3:llm match and zero hanzi overlap to be silenced, got: %s", bandsText(keptLLM))
	}

	// 7. StripInjected must strip <untrusted_gen_ui_state> blocks so widget paths never
	// pollute search cues or trigger false file-path recalls.
	genUIPrompt := "open-webui的默认字体试一下\n<untrusted_gen_ui_state>\n[{\"path\":\"/Users/test/.zcode/v2/visualizations/abc.html\"}]\n</untrusted_gen_ui_state>"
	if stripped := ids.StripInjected(genUIPrompt); strings.Contains(stripped, "untrusted_gen_ui_state") || strings.Contains(stripped, "visualizations") {
		t.Fatalf("expected StripInjected to remove <untrusted_gen_ui_state>, got: %q", stripped)
	}
	if paths := extractStructuralPathsFromText("访问 http://localhost:8008/ 首页", 4); len(paths) != 0 {
		t.Fatalf("expected root localhost URL without SPA route to yield 0 file paths, got: %v", paths)
	}
	if !isEphemeralProbeSession("probe-boot-readonly") || isEphemeralProbeSession("session_493f1424b7") {
		t.Fatalf("unexpected isEphemeralProbeSession classification")
	}

	// 8. 4-turn Cadence Reminder & Attribution Line Stripping:
	// Turns 1..3 on an unindexed topic stay 0-token silent; Turn 4 emits the lightweight
	// cadence anchor (logged as injected=0, cadence=1). And stripMemoryAttributionLine
	// removes the opening "Rolling-Memory 记忆" sentence before persisting to trace.
	for turnIdx := 1; turnIdx <= 4; turnIdx++ {
		cadPayload, _ := json.Marshal(map[string]any{
			"session_id": "session_cadence_live_1",
			"cwd":        wsDir,
			"prompt":     fmt.Sprintf("继续推进第 %d 步独立样式微调", turnIdx),
		})
		code, cadOut, _ := runHookMain(t, []string{"recall"}, string(cadPayload))
		if code != 0 {
			t.Fatalf("cadence turn %d exit=%d", turnIdx, code)
		}
		if turnIdx < 4 && strings.TrimSpace(cadOut) != "" {
			t.Fatalf("expected turn %d (<4) to stay silent, got: %q", turnIdx, cadOut)
		}
		if turnIdx == 4 && !strings.Contains(cadOut, "[Rolling-Memory 4轮节律锚点]") {
			t.Fatalf("expected turn 4 to emit 4-turn cadence reminder, got: %q", cadOut)
		}
	}
	rawReply := "技能判定：无（豁免）\n\n💡 本轮经 Rolling-Memory 记忆直达 internal/hooks/hooks.go，省去盲搜。\n\n已完成修改并通过测试。"
	cleanedReply := stripMemoryAttributionLine(rawReply)
	if strings.Contains(cleanedReply, "Rolling-Memory 记忆") || !strings.Contains(cleanedReply, "已完成修改并通过测试。") {
		t.Fatalf("stripMemoryAttributionLine failed: %q", cleanedReply)
	}
}

// hookGateRecall replays the retrieval the UserPromptSubmit hook used to run
// before the reminder-only ruling — recall as the deliverable reader, then the
// deterministic specificity gate — so retrieval-quality assertions keep guarding
// the agent-driven path without the hook performing any recall itself.
func hookGateRecall(t *testing.T, mem *api.Memory, wsDir, session, prompt string) []map[string]any {
	t.Helper()
	payload := map[string]any{"session_id": session, "cwd": wsDir}
	reader := "deliverable"
	res, err := mem.Recall(api.RecallRequest{
		Cue: prompt, Reader: &reader, Presence: workspacePresence(payload), NoCrystallize: true,
	})
	if err != nil {
		t.Fatalf("recall(%q): %v", prompt, err)
	}
	return recall.FilterBandsBySpecificity(prompt, prompt, workspaceProject(payload), res.Bands)
}

// bandsText flattens a band list into one searchable string (titles + rendered nodes).
func bandsText(bands []map[string]any) string {
	var builder strings.Builder
	for _, band := range bands {
		builder.WriteString(fmt.Sprint(band["title"]))
		builder.WriteString(" ")
		nodes, _ := band["nodes"].([]map[string]any)
		for _, node := range nodes {
			builder.WriteString(fmt.Sprint(node["content"]))
			builder.WriteString(" ")
		}
	}
	return builder.String()
}

func TestBootContextUsesThinVaultAtlasAndEpisodicTail(t *testing.T) {
	env := newTestEnv(t)
	wsDir := filepath.Join(env.base, "Rolling-Memory")
	if err := os.MkdirAll(filepath.Join(wsDir, "internal", "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	targetFile := filepath.Join(wsDir, "internal", "hooks", "hooks.go")
	if err := os.WriteFile(targetFile, []byte("package hooks\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mem := openMemory(t)
	tc, err := mem.ThinClient()
	if err != nil {
		t.Fatal(err)
	}
	tc.SetIdentity("zcode", "sess_boot_1", "Rolling-Memory")
	if err := tc.Vault.SaveLocalPlaintext("ep_boot_u", 100, "[user] 检查 SessionStart 唤醒路标", []string{"[user] 检查 SessionStart 唤醒路标"}, thinclient.CloudIngestResponse{}); err != nil {
		t.Fatal(err)
	}
	if err := tc.Vault.SaveLocalPlaintext("ep_boot_a", 101, "[assistant] 已修复 internal/hooks/hooks.go 的薄库回退。", []string{"[assistant] 已修复 internal/hooks/hooks.go 的薄库回退。"}, thinclient.CloudIngestResponse{}); err != nil {
		t.Fatal(err)
	}
	if err := tc.RememberNoetic("nt_boot_1", "SessionStart 唤醒路标入口", []string{"ep_boot_a"}, []string{"internal/hooks/hooks.go"}, "入口在 internal/hooks/hooks.go", 102); err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{"cwd": wsDir, "session_id": "sess_boot_2"}
	atlas := queryDiskVerifiedWorkspaceAtlas(mem, "mem:hooks", "Rolling-Memory", payload, 5)
	if !strings.Contains(atlas, "internal/hooks/hooks.go") {
		t.Fatalf("expected thin_vault_noetic verified path in atlas, got %q", atlas)
	}
	tail := queryWorkspaceEpisodicTail(mem, "mem:hooks", "Rolling-Memory", "sess_boot_2")
	if !strings.Contains(tail, "检查 SessionStart 唤醒路标") || !strings.Contains(tail, "已修复 internal/hooks/hooks.go") {
		t.Fatalf("expected thin_vault_docs episodic tail, got %q", tail)
	}
}
