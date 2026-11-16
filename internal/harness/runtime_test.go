// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package harness

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// Process-level runtime checks (shim runs the CLI, MCP handshake,
// `install --list` / `--dry-run` / `--check`).
//
// Note: these checks must build the real
// cmd/rolling-memory binary and drive it as a child process. The `go test`
// binary can never act as the launcher -- os.Executable() inside the test
// process points at the test executable, not at the CLI. The install-handshake
// check uses the same shape: temporary HOME, `--skill-file` at the
// repo skill, then `install --check` with the launcher on the shim.

var (
	buildBinaryOnce sync.Once
	builtBinaryPath string
	builtBinaryDir  string
	buildBinaryErr  error
)

func TestMain(m *testing.M) {
	code := m.Run()
	if builtBinaryDir != "" {
		_ = os.RemoveAll(builtBinaryDir)
	}
	os.Exit(code)
}

// realBinary builds ./cmd/rolling-memory once per test run and returns its path.
// The build lands in <dir>/bin/rolling-memory with the shipped skill mirrored to
// <dir>/skills/rolling-memory/SKILL.md, the layout DefaultSkillFile resolves
// against (internal/harness/harness.go:300), so installs run without
// --skill-file exactly like the standard reference.
func realBinary(t *testing.T) string {
	t.Helper()
	buildBinaryOnce.Do(func() {
		directory, err := os.MkdirTemp("", "rm-runtime.")
		if err != nil {
			buildBinaryErr = fmt.Errorf("temp dir: %w", err)
			return
		}
		builtBinaryDir = directory
		name := "rolling-memory"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		output := filepath.Join(directory, "bin", name)
		command := exec.Command("go", "build", "-o", output, "github.com/rocareer/rolling-memory/cmd/rolling-memory")
		if data, err := command.CombinedOutput(); err != nil {
			buildBinaryErr = fmt.Errorf("build cmd/rolling-memory: %w\n%s", err, data)
			return
		}
		skill, err := os.ReadFile(filepath.Join("..", "..", "skills", "rolling-memory", "SKILL.md"))
		if err != nil {
			buildBinaryErr = fmt.Errorf("read repo skill: %w", err)
			return
		}
		target := filepath.Join(directory, "skills", "rolling-memory", "SKILL.md")
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			buildBinaryErr = fmt.Errorf("mkdir skill dir: %w", err)
			return
		}
		if err := os.WriteFile(target, skill, 0o644); err != nil {
			buildBinaryErr = fmt.Errorf("mirror skill: %w", err)
			return
		}
		refDir := filepath.Join(directory, "skills", "rolling-memory", "references")
		_ = os.MkdirAll(refDir, 0o755)
		for _, langCode := range []string{"en", "zh-CN"} {
			if refRaw, rErr := os.ReadFile(filepath.Join("..", "..", "skills", "rolling-memory", "references", "SKILL."+langCode+".md")); rErr == nil {
				_ = os.WriteFile(filepath.Join(refDir, "SKILL."+langCode+".md"), refRaw, 0o644)
			}
		}
		builtBinaryPath = output
	})
	if buildBinaryErr != nil {
		t.Fatalf("%v", buildBinaryErr)
	}
	return builtBinaryPath
}

// runtimeFixture is a synthetic HOME with all five harness directories, mirror
// of the standard Fixture, plus the child-process environment for the real binary.
type runtimeFixture struct {
	home string
	env  []string
}

func newRuntimeFixture(t *testing.T) *runtimeFixture {
	t.Helper()
	home := filepath.Join(t.TempDir(), "home")
	for _, path := range []string{
		filepath.Join(home, ".zcode"),
		filepath.Join(home, ".claude"),
		filepath.Join(home, ".codex", "skills"),
		filepath.Join(home, ".dsh", "profiles", "web"),
		filepath.Join(home, ".codebuddy"),
		filepath.Join(home, "project"),
	} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
	}
	return &runtimeFixture{home: home, env: childEnv(home)}
}

// childEnv builds the isolated subprocess environment: inherit the shell
// environment, drop every ROLLING_MEMORY_* override and the LLM key variables,
// then pin HOME/XDG/DSH under the synthetic home so the real core and the user
// configuration are never touched.
func childEnv(home string) []string {
	dropped := func(key string) bool {
		upper := strings.ToUpper(key)
		switch {
		case strings.HasPrefix(upper, "ROLLING_MEMORY_"):
			return true
		case upper == "DSH_HOME", upper == "OPENAI_API_KEY", upper == "OLLAMA_API_KEY":
			return true
		}
		return false
	}
	env := make([]string, 0, len(os.Environ())+6)
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if dropped(key) {
			continue
		}
		env = append(env, entry)
	}
	return append(env,
		"HOME="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, "xdg-config"),
		"XDG_DATA_HOME="+filepath.Join(home, "xdg-data"),
		"XDG_STATE_HOME="+filepath.Join(home, "xdg-state"),
		"DSH_HOME="+filepath.Join(home, ".dsh"),
	)
}

// runRealBinary executes the built CLI as a child process.
func runRealBinary(t *testing.T, fx *runtimeFixture, dir string, args ...string) (int, string, string) {
	t.Helper()
	command := exec.Command(realBinary(t), args...)
	command.Dir = dir
	command.Env = fx.env
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("run %v: %v", args, err)
		}
		code = exitErr.ExitCode()
	}
	return code, stdout.String(), stderr.String()
}

// repoSkillFile is the shipped skills/rolling-memory/SKILL.md: the package dir
// is internal/harness, so the repo root is two levels up.
func repoSkillFile(t *testing.T) string {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("..", "..", "skills", "rolling-memory", "SKILL.md"))
	if err != nil {
		t.Fatalf("repo skill path: %v", err)
	}
	if !fileExists(path) {
		t.Fatalf("repo skill file missing: %s", path)
	}
	return path
}

// TestRealBinaryHandshake builds the binary, uses it as the launcher, and
// requires the initialize + tools/list answers. The handshake must run on its
// own temporary database even when the calling process carries
// ROLLING_MEMORY_* overrides.
func TestRealBinaryHandshake(t *testing.T) {
	fx := newRuntimeFixture(t)
	options := DefaultInstallOptions(fx.home, filepath.Join(fx.home, "project"))
	options.Binary = realBinary(t)
	options.Scope = "mem:test"

	sentinel := filepath.Join(t.TempDir(), "sentinel.db")
	t.Setenv("ROLLING_MEMORY_DB", sentinel)
	t.Setenv("ROLLING_MEMORY_SCOPE", "mem:sentinel")

	result := Handshake(options, 60*time.Second)
	if result["ok"] != true {
		t.Fatalf("handshake = %#v", result)
	}
	tools, ok := result["tools"].([]any)
	if !ok || len(tools) != 1 || tools[0] != "recall" {
		t.Fatalf("tools = %#v", result["tools"])
	}
	server, ok := result["server"].(map[string]any)
	if !ok || server["name"] != "rolling-memory" {
		t.Fatalf("server = %#v", result["server"])
	}
	if _, exists := result["error"]; exists {
		t.Fatalf("unexpected error: %v", result["error"])
	}
	if fileExists(sentinel) {
		t.Fatalf("handshake opened the inherited ROLLING_MEMORY_DB: %s", sentinel)
	}
}

// TestCLIInstallListDryRunAndCheck verifies `install --list`, `install --dry-run`,
// and `install --check` against the built CLI binary.
func TestCLIInstallListDryRunAndCheck(t *testing.T) {
	fx := newRuntimeFixture(t)
	work := t.TempDir()

	code, stdout, stderr := runRealBinary(t, fx, work, "install", "--list", "--json")
	if code != 0 {
		t.Fatalf("install --list exit %d: %s", code, stderr)
	}
	rows := []map[string]any{}
	if err := json.Unmarshal([]byte(stdout), &rows); err != nil {
		t.Fatalf("install --list output: %v\n%s", err, stdout)
	}
	wantDetected := map[string]bool{
		"zcode": true, "claude-code": true, "codex": true, "dsh": true, "codebuddy": true,
		"cursor": false, "windsurf": false, "cline": false, "workbuddy": false, "doubao": false,
	}
	if len(rows) != len(wantDetected) {
		t.Fatalf("install --list rows = %d, want %d", len(rows), len(wantDetected))
	}
	for _, row := range rows {
		id, ok := row["id"].(string)
		want, exists := wantDetected[id]
		if !ok || !exists {
			t.Fatalf("unexpected or duplicate harness: %#v", row)
		}
		if row["detected"] != want {
			t.Fatalf("%s detected = %v, want %t", id, row["detected"], want)
		}
		delete(wantDetected, id)
	}
	if len(wantDetected) != 0 {
		t.Fatalf("missing harnesses: %#v", wantDetected)
	}

	code, stdout, stderr = runRealBinary(t, fx, work,
		"install", "zcode", "--name", "rmem", "--dry-run", "--json")
	if code != 0 {
		t.Fatalf("dry-run exit %d: %s", code, stderr)
	}
	payload := map[string]any{}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("dry-run output: %v\n%s", err, stdout)
	}
	if payload["dry_run"] != true {
		t.Fatalf("dry-run payload = %#v", payload)
	}
	if fileExists(filepath.Join(fx.home, ".zcode", "cli", "config.json")) {
		t.Fatalf("dry-run wrote the zcode config")
	}
	if fileExists(filepath.Join(fx.home, ".zcode", "skills", "rmem")) {
		t.Fatalf("dry-run wrote the skill")
	}

	// Real install, then the live check. The launcher resolves to the shim the
	// install wrote, so this exercises shim -> real binary -> `mcp`.
	code, stdout, stderr = runRealBinary(t, fx, work, "install", "zcode", "--name", "rmem", "--json")
	if code != 0 {
		t.Fatalf("install exit %d: %s%s", code, stdout, stderr)
	}

	code, stdout, stderr = runRealBinary(t, fx, work, "install", "--check", "--name", "rmem", "--json")
	if code != 0 {
		t.Fatalf("install --check exit %d: %s%s", code, stdout, stderr)
	}
	checked := map[string]any{}
	if err := json.Unmarshal([]byte(stdout), &checked); err != nil {
		t.Fatalf("install --check output: %v\n%s", err, stdout)
	}
	// The check embeds the whole handshake result under "server":
	// {"ok", "server": serverInfo, "tools"}.
	server, ok := checked["server"].(map[string]any)
	if !ok || server["ok"] != true {
		t.Fatalf("check server = %#v (payload %s)", checked["server"], stdout)
	}
	tools, _ := server["tools"].([]any)
	if len(tools) != 1 || tools[0] != "recall" {
		t.Fatalf("check tools = %#v", server["tools"])
	}
	info, _ := server["server"].(map[string]any)
	if name, _ := info["name"].(string); name != "rolling-memory" {
		t.Fatalf("check server name = %#v", server["server"])
	}
	actions, _ := checked["actions"].([]any)
	found := false
	for _, item := range actions {
		action, _ := item.(map[string]any)
		if action["harness"] == "zcode" && action["component"] == "mcp" {
			found = true
		}
	}
	if !found {
		t.Fatalf("install --check has no zcode/mcp action: %s", stdout)
	}
}

// TestShimRunsCLI verifies that the generated shim executes the real CLI.
func TestShimRunsCLI(t *testing.T) {
	fx := newRuntimeFixture(t)
	work := t.TempDir()
	code, stdout, stderr := runRealBinary(t, fx, work, "install", "zcode", "--name", "rmem", "--json")
	if code != 0 {
		t.Fatalf("install exit %d: %s%s", code, stdout, stderr)
	}

	installed := filepath.Join(fx.home, ".zcode", "skills", "rmem", "SKILL.md")
	if !strings.Contains(readFile(t, installed), "name: rmem") ||
		!strings.Contains(readFile(t, installed), "Rolling-Memory") {
		t.Fatalf("installed skill is not the shipped SKILL.md renamed:\n%s", readFile(t, installed))
	}

	shim := filepath.Join(fx.home, "xdg-data", "rolling-memory", "bin", ShimName)
	if runtime.GOOS == "windows" {
		shim = filepath.Join(fx.home, "xdg-data", "rolling-memory", "bin", windowsShimName)
	}
	if !fileExists(shim) {
		t.Fatalf("shim missing: %s", shim)
	}
	if content := readFile(t, shim); !strings.Contains(content, realBinary(t)) {
		t.Fatalf("shim does not exec the built binary:\n%s", content)
	}

	command := exec.Command(shim, "--version")
	command.Dir = work
	command.Env = fx.env
	var shimOut, shimErr bytes.Buffer
	command.Stdout = &shimOut
	command.Stderr = &shimErr
	if err := command.Run(); err != nil {
		t.Fatalf("run shim: %v (stderr %s)", err, shimErr.String())
	}
	if !strings.Contains(shimOut.String(), "rolling-memory") {
		t.Fatalf("shim stdout = %q", shimOut.String())
	}
}

// TestCLIInstallRejectsBadName verifies that `install` rejects an invalid server name.
func TestCLIInstallRejectsBadName(t *testing.T) {
	fx := newRuntimeFixture(t)
	code, _, stderr := runRealBinary(t, fx, t.TempDir(), "install", "zcode", "--name", "bad name")
	if code != 1 {
		t.Fatalf("invalid name exit = %d, want 1", code)
	}
	if !strings.Contains(stderr, "invalid") {
		t.Fatalf("stderr = %q", stderr)
	}
}
