// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package e2e_test

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
	"testing"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// binaryPath is the acceptance binary built once in TestMain.
var binaryPath string

func TestInstallDefaultExtendedHarnesses(t *testing.T) {
	sb := newSandbox(t)
	for _, dir := range []string{".cursor", filepath.Join(".codeium", "windsurf"), ".cline"} {
		if err := os.MkdirAll(filepath.Join(sb.home, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}

	listed := sb.run("install", "--list", "--json")
	assertExit(t, listed, 0, "install --list")
	var rows []map[string]any
	if err := json.Unmarshal([]byte(listed.stdout), &rows); err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, row := range rows {
		id, _ := row["id"].(string)
		if row["detected"] == true {
			found[id] = true
		}
	}
	for _, id := range []string{"cursor", "windsurf", "cline"} {
		if !found[id] {
			t.Fatalf("detected %s missing from list: %s", id, listed.stdout)
		}
	}

	installed := sb.run("install", "--binary", binaryPath, "--json")
	assertExit(t, installed, 0, "default install")
	installReport := jsonObject(t, installed.stdout)
	for _, id := range []string{"cursor", "windsurf", "cline"} {
		if status := actionStatus(t, installReport, id, "skill"); status != "installed" {
			t.Fatalf("%s skill = %q", id, status)
		}
		if status := actionStatus(t, installReport, id, "mcp"); status != "installed" {
			t.Fatalf("%s mcp = %q", id, status)
		}
	}

	checked := sb.run("install", "--check", "--binary", binaryPath, "--json")
	assertExit(t, checked, 0, "default install --check")
	checkReport := jsonObject(t, checked.stdout)
	for _, id := range []string{"cursor", "windsurf", "cline"} {
		if status := actionStatus(t, checkReport, id, "mcp"); status != "ok" {
			t.Fatalf("%s check = %q", id, status)
		}
	}
}

// TestMain builds the real binary: every face under test (MCP stdio, install
// handshake, hooks) must run a real executable, not the go test binary.
// The build lands in a temporary directory that is removed
// again when the suite ends.
func TestMain(m *testing.M) {
	previous := time.Local
	time.Local = time.UTC

	root, err := repoRootDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e: repo root: %v\n", err)
		os.Exit(1)
	}
	tempDir, err := os.MkdirTemp("", "rm-e2e.")
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e: temp dir: %v\n", err)
		os.Exit(1)
	}
	if err := buildAcceptanceBinary(root, tempDir); err != nil {
		fmt.Fprintf(os.Stderr, "e2e: %v\n", err)
		os.RemoveAll(tempDir)
		os.Exit(1)
	}

	code := m.Run()
	time.Local = previous
	os.RemoveAll(tempDir)
	os.Exit(code)
}

func buildAcceptanceBinary(root, dir string) error {
	binaryPath = filepath.Join(dir, "rolling-memory")
	if runtime.GOOS == "windows" {
		binaryPath += ".exe"
	}
	build := exec.Command("go", "build", "-trimpath", "-o", binaryPath, "./cmd/rolling-memory")
	build.Dir = root
	build.Stdout = os.Stdout
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		return fmt.Errorf("build acceptance binary: %w", err)
	}
	return nil
}

// repoRootDir is the module root; the tests run with internal/e2e as their
// working directory.
func repoRootDir() (string, error) {
	return filepath.Abs(filepath.Join("..", ".."))
}

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := repoRootDir()
	if err != nil {
		t.Fatalf("repo root: %v", err)
	}
	return root
}

// result is one finished child process.
type result struct {
	code   int
	stdout string
	stderr string
}

// sandbox is one throwaway environment: private HOME, XDG roots, database,
// config and log. Children get exactly this environment and never inherit the
// host one, so nothing can reach the live memory core or the host configs.
type sandbox struct {
	t      *testing.T
	base   string
	home   string
	db     string
	config string
	log    string
}

func newSandbox(t *testing.T) *sandbox {
	t.Helper()
	base := t.TempDir()
	sb := &sandbox{
		t:      t,
		base:   base,
		home:   filepath.Join(base, "home"),
		db:     filepath.Join(base, "memory.db"),
		config: filepath.Join(base, "config.toml"),
		log:    filepath.Join(base, "state", "rolling-memory.log"),
	}
	for _, dir := range []string{sb.home, filepath.Join(base, "tmp")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("sandbox dir %s: %v", dir, err)
		}
	}
	return sb
}

// env is the complete child environment: the sandbox variables plus PATH (the
// launcher lookup) and TZ, never the host environment.
func (s *sandbox) env() []string {
	return []string{
		"HOME=" + s.home,
		"PATH=" + os.Getenv("PATH"),
		"TMPDIR=" + filepath.Join(s.base, "tmp"),
		"TZ=UTC",
		"XDG_CONFIG_HOME=" + filepath.Join(s.base, "xdg-config"),
		"XDG_DATA_HOME=" + filepath.Join(s.base, "xdg-data"),
		"XDG_STATE_HOME=" + filepath.Join(s.base, "xdg-state"),
		"DSH_HOME=" + filepath.Join(s.base, "dsh"),
		"ROLLING_MEMORY_CONFIG=" + s.config,
		"ROLLING_MEMORY_DB=" + s.db,
		"ROLLING_MEMORY_LOG=" + s.log,
		"ROLLING_MEMORY_SCOPE=mem:e2e",
		"ROLLING_MEMORY_MODE=local",
	}
}

// skillFile is the shipped SKILL.md that the install smoke registers.
func (s *sandbox) skillFile() string {
	return filepath.Join(repoRoot(s.t), "skills", "rolling-memory", "SKILL.md")
}

func (s *sandbox) run(args ...string) result {
	s.t.Helper()
	return s.runInput("", args...)
}

// runInput runs the acceptance binary with the sandbox environment and feeds
// input on stdin.
func (s *sandbox) runInput(input string, args ...string) result {
	s.t.Helper()
	command := exec.Command(binaryPath, args...)
	command.Dir = s.base
	command.Env = s.env()
	command.Stdin = strings.NewReader(input)
	return runCommand(s.t, command)
}

// runCommandLine runs one command line recorded by `install` (a hook entry),
// the way the harness would: split on whitespace, same sandbox, same stdin.
func (s *sandbox) runCommandLine(line, input string) result {
	s.t.Helper()
	fields := strings.Fields(line)
	if len(fields) == 0 {
		s.t.Fatalf("empty command line")
	}
	if fields[0] != binaryPath {
		s.t.Fatalf("command %q does not reference the acceptance binary %q", line, binaryPath)
	}
	return s.runInput(input, fields[1:]...)
}

// runCommand runs one prepared command and captures its exit code and streams.
func runCommand(t *testing.T, command *exec.Cmd) result {
	t.Helper()
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("run %s: %v", strings.Join(command.Args, " "), err)
		}
		code = exitErr.ExitCode()
	}
	return result{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

func assertExit(t *testing.T, got result, want int, what string) {
	t.Helper()
	if got.code != want {
		t.Fatalf("%s exit = %d, want %d\nstdout: %s\nstderr: %s", what, got.code, want, got.stdout, got.stderr)
	}
}

// assertNoFailures rejects the conflict / error / missing statuses of one
// install / check report.
func assertNoFailures(t *testing.T, report map[string]any) {
	t.Helper()
	for _, item := range arrayField(t, report, "actions") {
		action, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("action is not an object: %#v", item)
		}
		switch action["status"] {
		case "conflict", "error", "missing":
			t.Fatalf("report action failed: %#v", action)
		}
	}
}

// actionStatus finds one report action by harness and component.
func actionStatus(t *testing.T, report map[string]any, harnessID, component string) string {
	t.Helper()
	for _, item := range arrayField(t, report, "actions") {
		action, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("action is not an object: %#v", item)
		}
		if action["harness"] == harnessID && action["component"] == component {
			return stringField(t, action, "status")
		}
	}
	t.Fatalf("no %s/%s action in %#v", harnessID, component, report["actions"])
	return ""
}

// jsonLines splits non-empty lines, the way the stdio MCP server writes them.
func jsonLines(text string) []string {
	lines := []string{}
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			lines = append(lines, trimmed)
		}
	}
	return lines
}

func jsonObject(t *testing.T, text string) map[string]any {
	t.Helper()
	value := map[string]any{}
	if err := json.Unmarshal([]byte(text), &value); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, text)
	}
	return value
}

// objectAt walks nested JSON objects, failing the test when the path is absent.
func objectAt(t *testing.T, value any, keys ...string) map[string]any {
	t.Helper()
	node := value
	for _, key := range keys {
		object, ok := node.(map[string]any)
		if !ok {
			t.Fatalf("path %v: %q is not an object in %#v", keys, key, node)
		}
		node = object[key]
	}
	object, ok := node.(map[string]any)
	if !ok {
		t.Fatalf("path %v: value is not an object: %#v", keys, node)
	}
	return object
}

func arrayField(t *testing.T, value map[string]any, key string) []any {
	t.Helper()
	items, ok := value[key].([]any)
	if !ok {
		t.Fatalf("%q is not an array: %#v", key, value[key])
	}
	return items
}

func stringField(t *testing.T, value map[string]any, key string) string {
	t.Helper()
	text, ok := value[key].(string)
	if !ok {
		t.Fatalf("%q is not a string: %#v", key, value[key])
	}
	return text
}

func numberField(t *testing.T, value map[string]any, key string) float64 {
	t.Helper()
	switch typed := value[key].(type) {
	case float64:
		return typed
	case int64:
		return float64(typed)
	case int:
		return float64(typed)
	case json.Number:
		if parsed, err := typed.Float64(); err == nil {
			return parsed
		}
	}
	t.Fatalf("%q is not a number: %#v", key, value[key])
	return 0
}

// numberOr reads one optional numeric field, the shape logHook writes with
// per-event keys.
func numberOr(t *testing.T, value map[string]any, key string, fallback float64) float64 {
	t.Helper()
	if _, present := value[key]; !present {
		return fallback
	}
	return numberField(t, value, key)
}

// parseTOML decodes the codex config.toml the install smoke writes.
func parseTOML(t *testing.T, text string) map[string]any {
	t.Helper()
	data := map[string]any{}
	if err := toml.Unmarshal([]byte(text), &data); err != nil {
		t.Fatalf("invalid TOML: %v\n%s", err, text)
	}
	return data
}

// jsonPayload marshals one synthetic hook payload.
func jsonPayload(t *testing.T, payload map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("payload: %v", err)
	}
	return string(raw)
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}

func writeFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
