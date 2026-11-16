// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package golden

// Surface scenario replay.
//
// The helpers below rebuild a synthetic HOME/XDG sandbox, drive the
// Go CLI/MCP/hook faces in process (so the frozen clock seam applies), and
// compare the normalized streams with the sealed expectations. Install
// additionally runs the real launcher binary at <CASE>/bin/rolling-memory for
// the live MCP handshake.

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/rocareer/rolling-memory/internal/cli"
	"github.com/rocareer/rolling-memory/internal/ids"
)

// updateGolden turns the surface comparators into recorders: each comparison
// writes the normalized actual stream back to the sealed expectation instead of
// failing. It is captured at package load because the replay sandbox clears the
// process environment (withProcessEnv), so a lazy os.Getenv inside the
// comparators would always read "".
var updateGolden = os.Getenv("UPDATE_GOLDEN") == "1"

// writeExpected replaces one sealed expectation, lifting the read-only bit
// first when the corpus pins the file 0444.
func writeExpected(path string, raw []byte) error {
	if err := os.Chmod(path, 0o644); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.WriteFile(path, raw, 0o644)
}

// surfaceCase is one invocation (or direct SQL write) of a surface scenario.
type surfaceCase struct {
	ID     string   `json:"id"`
	Kind   string   `json:"kind"`
	Argv   []string `json:"argv"`
	Note   string   `json:"note"`
	SQL    string   `json:"sql"`
	Params []any    `json:"params"`
}

// surfaceSpec is cases.json / case.json, shared by cli, hooks and install.
type surfaceSpec struct {
	Scenario     string            `json:"scenario"`
	Scope        string            `json:"scope"`
	Runner       string            `json:"runner"`
	Env          map[string]string `json:"env"`
	Config       map[string]any    `json:"config"`
	Cases        []surfaceCase     `json:"cases"`
	Inputs       []string          `json:"inputs"`
	HomeTemplate string            `json:"home_template"`
	Binary       string            `json:"binary"`
}

// surfaceMCPMeta is meta.json of the MCP scenarios.
type surfaceMCPMeta struct {
	Scenario string         `json:"scenario"`
	Scope    string         `json:"scope"`
	Config   map[string]any `json:"config"`
}

// surfaceSandbox is one synthetic scenario tree:
//
//	<workRoot>/cases/<scenario>       <CASE>
//	<workRoot>/cases/<scenario>/home  <HOME>
//	.../config.toml, .../memory.db, .../state/rolling-memory.log, .../out
type surfaceSandbox struct {
	workRoot   string
	goldenRoot string
	caseDir    string
	home       string
	configPath string
	dbPath     string
	logPath    string
	outDir     string
	tmpDir     string
	hostPath   string
	normalizer *Normalizer
}

func newSurfaceSandbox(workRoot, repoRoot, kind, scenario string) (*surfaceSandbox, error) {
	goldenRoot, err := filepath.Abs(GoldenRoot())
	if err != nil {
		return nil, err
	}
	caseDir := filepath.Join(workRoot, "cases", scenario)
	sandbox := &surfaceSandbox{
		workRoot:   workRoot,
		goldenRoot: goldenRoot,
		caseDir:    caseDir,
		home:       filepath.Join(caseDir, "home"),
		configPath: filepath.Join(caseDir, "config.toml"),
		dbPath:     filepath.Join(caseDir, "memory.db"),
		logPath:    filepath.Join(caseDir, "state", "rolling-memory.log"),
		outDir:     filepath.Join(caseDir, "out"),
		tmpDir:     filepath.Join(workRoot, "tmp"),
		hostPath:   os.Getenv("PATH"),
	}
	if sandbox.hostPath == "" {
		sandbox.hostPath = "/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"
	}
	for _, dir := range []string{caseDir, sandbox.home, sandbox.outDir, sandbox.tmpDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	sandbox.normalizer = &Normalizer{
		Root:       workRoot,
		Case:       caseDir,
		Home:       sandbox.home,
		Tmp:        sandbox.tmpDir,
		Repo:       repoRoot,
		Out:        sandbox.outDir,
		ConfigPath: sandbox.configPath,
		DBPath:     sandbox.dbPath,
		LogPath:    sandbox.logPath,
	}
	if err := sandbox.copyInputs(kind, scenario); err != nil {
		return nil, err
	}
	return sandbox, nil
}

// copyInputs copies <golden>/<kind>/<scenario>/input to <CASE>/input when the
// scenario has one (cli and hooks).
func (s *surfaceSandbox) copyInputs(kind, scenario string) error {
	source := filepath.Join(s.goldenRoot, kind, scenario, "input")
	info, err := os.Stat(source)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if !info.IsDir() {
		return nil
	}
	return copyTree(source, filepath.Join(s.caseDir, "input"))
}

// copyTree copies a template tree and makes every file writable (the sealed
// corpus is 0444; README §Surface scenarios asks the runner to copy).
func copyTree(source, target string) error {
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		destination := filepath.Join(target, relative)
		if entry.IsDir() {
			return os.MkdirAll(destination, 0o755)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			return err
		}
		return os.WriteFile(destination, raw, 0o644)
	})
}

// expand substitutes the README §Surface tokens in a case template.
func (s *surfaceSandbox) expand(text string) string {
	for _, pair := range []struct{ token, value string }{
		{"<TMP>", s.workRoot},
		{"<CASE>", s.caseDir},
		{"<HOME>", s.home},
		{"<CFG>", s.configPath},
		{"<DB>", s.dbPath},
		{"<LOG>", s.logPath},
		{"<OUT>", s.outDir},
		{"<NOW>", strconv.FormatInt(FixedNow, 10)},
	} {
		text = strings.ReplaceAll(text, pair.token, pair.value)
	}
	return text
}

// defaultEnv is the sandbox environment of the MCP scenarios (their meta.json
// carries no env map).
func (s *surfaceSandbox) defaultEnv(scope string) map[string]string {
	return map[string]string{
		"HOME":                  s.home,
		"TMPDIR":                s.tmpDir,
		"XDG_CONFIG_HOME":       filepath.Join(s.home, ".config"),
		"XDG_DATA_HOME":         filepath.Join(s.home, ".local", "share"),
		"XDG_STATE_HOME":        filepath.Join(s.home, ".local", "state"),
		"ROLLING_MEMORY_CONFIG": s.configPath,
		"ROLLING_MEMORY_DB":     s.dbPath,
		"ROLLING_MEMORY_LOG":    s.logPath,
		"ROLLING_MEMORY_MODE":   "local",
		"ROLLING_MEMORY_SCOPE":  scope,
	}
}

// env merges the case environment over the base §6.2 environment. Case values
// are token-expanded first.
func (s *surfaceSandbox) env(raw map[string]string, scope string) map[string]string {
	if raw == nil {
		raw = s.defaultEnv(scope)
	}
	values := map[string]string{
		"PATH":   s.hostPath,
		"TZ":     "UTC",
		"LC_ALL": "C",
		"LANG":   "C",
	}
	for name, value := range raw {
		values[name] = s.expand(value)
	}
	return values
}

// withProcessEnv replaces the process environment for one scenario with the
// declared sandbox values and restores the host environment afterwards.
// Package tests run sequentially, so the process-wide switch is safe.
func withProcessEnv(values map[string]string, fn func() error) error {
	previous := os.Environ()
	os.Clearenv()
	for name, value := range values {
		_ = os.Setenv(name, value)
	}
	defer func() {
		os.Clearenv()
		for _, entry := range previous {
			index := strings.IndexByte(entry, '=')
			if index < 0 {
				continue
			}
			_ = os.Setenv(entry[:index], entry[index+1:])
		}
	}()
	return fn()
}

// writeSurfaceConfig writes the scenario's dotted config map as TOML.
func writeSurfaceConfig(path string, values map[string]any) error {
	root := []string{}
	sections := map[string][]string{}
	for dotted, value := range values {
		key := dotted
		section := ""
		if index := strings.LastIndex(dotted, "."); index >= 0 {
			section, key = dotted[:index], dotted[index+1:]
		}
		line := key + " = " + surfaceTOMLLiteral(value)
		if section == "" {
			root = append(root, line)
			continue
		}
		sections[section] = append(sections[section], line)
	}
	sort.Strings(root)
	names := make([]string, 0, len(sections))
	for name := range sections {
		names = append(names, name)
	}
	sort.Strings(names)
	var builder strings.Builder
	for _, line := range root {
		builder.WriteString(line)
		builder.WriteString("\n")
	}
	for _, name := range names {
		lines := sections[name]
		sort.Strings(lines)
		builder.WriteString("[" + name + "]\n")
		for _, line := range lines {
			builder.WriteString(line)
			builder.WriteString("\n")
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(builder.String()), 0o600)
}

// surfaceTOMLLiteral renders one config value; the corpus stores booleans and
// integers as strings, so parse them back before quoting.
func surfaceTOMLLiteral(value any) string {
	switch typed := value.(type) {
	case bool:
		if typed {
			return "true"
		}
		return "false"
	case json.Number:
		return typed.String()
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case float64:
		return strconv.FormatFloat(typed, 'g', -1, 64)
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "true" || trimmed == "false" {
			return trimmed
		}
		if _, err := strconv.ParseInt(trimmed, 10, 64); err == nil {
			return trimmed
		}
		if _, err := strconv.ParseFloat(trimmed, 64); err == nil {
			return trimmed
		}
		return strconv.Quote(typed)
	}
	return strconv.Quote(fmt.Sprint(value))
}

// surfaceRun is one finished in-process CLI invocation.
type surfaceRun struct {
	code   int
	stdout string
	stderr string
}

func (s *surfaceSandbox) run(argv []string, stdin string) surfaceRun {
	var stdout, stderr bytes.Buffer
	code := cli.Main(argv, strings.NewReader(stdin), &stdout, &stderr)
	return surfaceRun{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

// expandArgv substitutes tokens in every argument.
func (s *surfaceSandbox) expandArgv(argv []string) []string {
	result := make([]string, 0, len(argv))
	for _, value := range argv {
		result = append(result, s.expand(value))
	}
	return result
}

// expectation returns one sealed expectation path.
func (s *surfaceSandbox) expectation(kind, scenario, name string) string {
	return filepath.Join(s.goldenRoot, kind, scenario, "expected", name)
}

// caseFile returns one sealed scenario file (cases.json, meta.json...).
func (s *surfaceSandbox) caseFile(kind, scenario, name string) string {
	return filepath.Join(s.goldenRoot, kind, scenario, name)
}

// loadSurfaceJSON decodes one sealed JSON file with numbers preserved.
func loadSurfaceJSON(path string, target any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// compareExit checks one expected exit file.
func compareExit(actual int, wantPath string) error {
	if updateGolden {
		return writeExpected(wantPath, []byte(strconv.Itoa(actual)+"\n"))
	}
	raw, err := os.ReadFile(wantPath)
	if err != nil {
		return err
	}
	want := strings.TrimSpace(string(raw))
	if strconv.Itoa(actual) != want {
		return fmt.Errorf("exit = %d, want %s", actual, want)
	}
	return nil
}

// compareText normalizes one text stream and compares it byte for byte.
func (s *surfaceSandbox) compareText(actual, wantPath string) error {
	normalized := s.normalizer.Text(actual)
	if updateGolden {
		return writeExpected(wantPath, []byte(normalized))
	}
	raw, err := os.ReadFile(wantPath)
	if err != nil {
		return err
	}
	if normalized != string(raw) {
		return fmt.Errorf("text mismatch\n%s", diffReport(normalized, string(raw)))
	}
	return nil
}

// compareJSON normalizes one JSON stream and compares canonical encodings.
func (s *surfaceSandbox) compareJSON(actual []byte, wantPath string) error {
	value, err := decodeJSON(actual)
	if err != nil {
		return fmt.Errorf("decode output: %w", err)
	}
	normalized, err := s.normalizer.Value(value, "")
	if err != nil {
		return err
	}
	canonical, err := CanonicalJSON(normalized)
	if err != nil {
		return err
	}
	if updateGolden {
		return writeExpected(wantPath, []byte(canonical))
	}
	raw, err := os.ReadFile(wantPath)
	if err != nil {
		return err
	}
	if canonical != string(raw) {
		return fmt.Errorf("JSON mismatch\n%s", diffReport(canonical, string(raw)))
	}
	return nil
}

// mcpNormalizedValue decodes and normalizes one MCP response for comparison.
// Both the recorded expectation and the Go response pass through this
// identically: the normalizer rewrites volatile fields (paths, fixed clock),
// and the embedded probe document of recall is canonicalized as a JSON value
// so key-order differences inside that string cannot mask value differences
// (full-mode recall text is not JSON and stays byte-compared). Differences
// between the frozen expectations and the Go implementation are closed
// by name in deviations.go; this function never mutates
// either side.
func (s *surfaceSandbox) mcpNormalizedValue(line string) (any, error) {
	value, err := decodeJSON([]byte(line))
	if err != nil {
		return nil, err
	}
	if err := s.normalizeProbeText(value); err != nil {
		return nil, err
	}
	return s.normalizer.Value(value, "")
}

// normalizeProbeText canonicalizes an embedded probe JSON document (the probe
// text of recall), so ordering differences inside that string do not mask value
// differences. Full-mode recall text is not JSON and stays byte-compared.
func (s *surfaceSandbox) normalizeProbeText(value any) error {
	root, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	result, ok := root["result"].(map[string]any)
	if !ok {
		return nil
	}
	content, ok := result["content"].([]any)
	if !ok {
		return nil
	}
	for _, item := range content {
		block, ok := item.(map[string]any)
		if !ok {
			continue
		}
		text, ok := block["text"].(string)
		if !ok || !strings.HasPrefix(strings.TrimSpace(text), "{") {
			continue
		}
		parsed, err := decodeJSON([]byte(text))
		if err != nil {
			continue
		}
		probe, ok := parsed.(map[string]any)
		if !ok {
			continue
		}
		if _, isProbe := probe["hits"]; !isProbe {
			continue
		}
		normalized, err := s.normalizer.Value(probe, "")
		if err != nil {
			return err
		}
		canonical, err := ids.PyJSONDumpIndent(normalized, false, true, 2)
		if err != nil {
			return err
		}
		block["text"] = canonical
	}
	return nil
}

// splitNonEmpty splits newline-delimited text, dropping blank lines.
func splitNonEmpty(text string) []string {
	lines := []string{}
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			lines = append(lines, trimmed)
		}
	}
	return lines
}

// ReplayCLISurface replays cli/<scenario>/cases.json.
func ReplayCLISurface(workRoot, repoRoot, scenario string) error {
	sandbox, err := newSurfaceSandbox(workRoot, repoRoot, "cli", scenario)
	if err != nil {
		return err
	}
	spec := &surfaceSpec{}
	if err := loadSurfaceJSON(sandbox.caseFile("cli", scenario, "cases.json"), spec); err != nil {
		return err
	}
	return withWorkingDir(sandbox.caseDir, func() error {
		return withProcessEnv(sandbox.env(spec.Env, spec.Scope), func() error {
			for _, testCase := range spec.Cases {
				if testCase.Kind == "sql" {
					if err := replaySurfaceSQL(sandbox, scenario, testCase); err != nil {
						return fmt.Errorf("%s: %w", testCase.ID, err)
					}
					continue
				}
				run := sandbox.run(sandbox.expandArgv(testCase.Argv), "")
				if err := compareExit(run.code, sandbox.expectation("cli", scenario, testCase.ID+".exit.txt")); err != nil {
					return fmt.Errorf("%s: %w", testCase.ID, err)
				}
				if err := sandbox.compareText(run.stderr, sandbox.expectation("cli", scenario, testCase.ID+".stderr.txt")); err != nil {
					return fmt.Errorf("%s stderr: %w", testCase.ID, err)
				}
				jsonPath := sandbox.expectation("cli", scenario, testCase.ID+".stdout.json")
				if fileExists(jsonPath) {
					if err := sandbox.compareJSON([]byte(run.stdout), jsonPath); err != nil {
						return fmt.Errorf("%s stdout: %w", testCase.ID, err)
					}
					continue
				}
				if err := sandbox.compareText(run.stdout, sandbox.expectation("cli", scenario, testCase.ID+".stdout.txt")); err != nil {
					return fmt.Errorf("%s stdout: %w", testCase.ID, err)
				}
			}
			return nil
		})
	})
}

// replaySurfaceSQL applies one direct database write and checks rowcount.
func replaySurfaceSQL(sandbox *surfaceSandbox, scenario string, testCase surfaceCase) error {
	connection, err := sql.Open("sqlite", sandbox.dbPath)
	if err != nil {
		return err
	}
	defer connection.Close()
	params := make([]any, 0, len(testCase.Params))
	for _, value := range testCase.Params {
		params = append(params, surfaceSQLValue(value))
	}
	result, err := connection.Exec(testCase.SQL, params...)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	return sandbox.compareJSON([]byte(fmt.Sprintf(`{"rowcount": %d}`, rows)),
		sandbox.expectation("cli", scenario, testCase.ID+".json"))
}

// surfaceSQLValue converts a JSON literal to a database/sql driver value.
func surfaceSQLValue(value any) any {
	switch typed := value.(type) {
	case json.Number:
		if strings.ContainsAny(typed.String(), ".eE") {
			number, err := typed.Float64()
			if err == nil {
				return number
			}
			return typed.String()
		}
		number, err := typed.Int64()
		if err == nil {
			return number
		}
		return typed.String()
	}
	return value
}

// ReplayMCPSurface replays mcp/<scenario>/transcript.jsonl against the stdio
// server and compares every response line with expected.jsonl.
func ReplayMCPSurface(workRoot, repoRoot, scenario string) error {
	sandbox, err := newSurfaceSandbox(workRoot, repoRoot, "mcp", scenario)
	if err != nil {
		return err
	}
	meta := &surfaceMCPMeta{}
	if err := loadSurfaceJSON(sandbox.caseFile("mcp", scenario, "meta.json"), meta); err != nil {
		return err
	}
	if err := writeSurfaceConfig(sandbox.configPath, meta.Config); err != nil {
		return err
	}
	transcript, err := os.ReadFile(sandbox.caseFile("mcp", scenario, "transcript.jsonl"))
	if err != nil {
		return err
	}
	var stdout, stderr bytes.Buffer
	scope := meta.Scope
	if scope == "" {
		scope = "mem:golden:" + scenario
	}
	err = withWorkingDir(sandbox.caseDir, func() error {
		return withProcessEnv(sandbox.env(nil, scope), func() error {
			code := cli.Main([]string{"mcp"}, bytes.NewReader(transcript), &stdout, &stderr)
			if code != 0 {
				return fmt.Errorf("mcp exit = %d (stderr: %s)", code, stderr.String())
			}
			if strings.TrimSpace(stderr.String()) != "" {
				return fmt.Errorf("mcp wrote to stderr: %s", stderr.String())
			}
			return nil
		})
	})
	if err != nil {
		return err
	}
	actualLines := splitNonEmpty(stdout.String())
	if updateGolden {
		// Record the normalized actual responses; serverInfo.version is pinned to
		// 0.0.1 by the normalizer, so the recording compares equal on replay.
		recorded := make([]string, 0, len(actualLines))
		for index, line := range actualLines {
			value, err := sandbox.mcpNormalizedValue(line)
			if err != nil {
				return fmt.Errorf("response %d: %w", index+1, err)
			}
			encoded, err := ids.PyJSONDump(value, false, true, false)
			if err != nil {
				return fmt.Errorf("encode response %d: %w", index+1, err)
			}
			recorded = append(recorded, encoded)
		}
		return writeExpected(sandbox.caseFile("mcp", scenario, "expected.jsonl"),
			[]byte(strings.Join(recorded, "\n")+"\n"))
	}
	expected, err := os.ReadFile(sandbox.caseFile("mcp", scenario, "expected.jsonl"))
	if err != nil {
		return err
	}
	expectedLines := splitNonEmpty(string(expected))
	if len(actualLines) != len(expectedLines) {
		return fmt.Errorf("response count = %d, want %d\n%s", len(actualLines), len(expectedLines), diffReport(stdout.String(), string(expected)))
	}
	for index, line := range actualLines {
		got, err := sandbox.mcpNormalizedValue(line)
		if err != nil {
			return fmt.Errorf("response %d: %w", index+1, err)
		}
		want, err := sandbox.mcpNormalizedValue(expectedLines[index])
		if err != nil {
			return fmt.Errorf("expected response %d: %w", index+1, err)
		}
		if err := compareGoldenValues(scenario, want, got); err != nil {
			return fmt.Errorf("response %d mismatch: %w\n%s",
				index+1, err, diffReport(goldenDump(got), goldenDump(want)))
		}
	}
	return nil
}

// goldenDump renders a normalized value for mismatch reports; it never fails
// the comparison itself.
func goldenDump(value any) string {
	text, err := ids.PyJSONDump(value, false, true, false)
	if err != nil {
		return fmt.Sprintf("<undumpable: %v>", err)
	}
	return text
}

// ReplayHooksSurface replays hooks/<scenario>/cases.json. The cases are
// cumulative: 010 imports the transcript, 020 boots on that state, 030-060 read
// it, 070 fails open and 080 closes a missing transcript.
func ReplayHooksSurface(workRoot, repoRoot, scenario string) error {
	sandbox, err := newSurfaceSandbox(workRoot, repoRoot, "hooks", scenario)
	if err != nil {
		return err
	}
	spec := &surfaceSpec{}
	if err := loadSurfaceJSON(sandbox.caseFile("hooks", scenario, "cases.json"), spec); err != nil {
		return err
	}
	if err := writeSurfaceConfig(sandbox.configPath, spec.Config); err != nil {
		return err
	}
	fallbackPath := filepath.Join(sandbox.expand(spec.Env["XDG_STATE_HOME"]), "rolling-memory", "hook.log")
	return withWorkingDir(sandbox.caseDir, func() error {
		return withProcessEnv(sandbox.env(spec.Env, spec.Scope), func() error {
			for _, testCase := range spec.Cases {
				_ = os.Remove(sandbox.logPath)
				_ = os.Remove(fallbackPath)
				stdin, err := readSurfaceStdin(sandbox, scenario, testCase.ID)
				if err != nil {
					return fmt.Errorf("%s: %w", testCase.ID, err)
				}
				run := sandbox.run(sandbox.expandArgv(testCase.Argv), stdin)
				if err := compareExit(run.code, filepath.Join(sandbox.caseFile("hooks", scenario, testCase.ID), "expected_exit.txt")); err != nil {
					return fmt.Errorf("%s: %w", testCase.ID, err)
				}
				if err := sandbox.compareText(run.stdout,
					filepath.Join(sandbox.caseFile("hooks", scenario, testCase.ID), "expected_stdout.txt")); err != nil {
					return fmt.Errorf("%s stdout: %w", testCase.ID, err)
				}
				if err := sandbox.compareText(run.stderr,
					filepath.Join(sandbox.caseFile("hooks", scenario, testCase.ID), "expected_stderr.txt")); err != nil {
					return fmt.Errorf("%s stderr: %w", testCase.ID, err)
				}
				if err := compareHookLog(sandbox, sandbox.logPath,
					filepath.Join(sandbox.caseFile("hooks", scenario, testCase.ID), "expected_log.jsonl")); err != nil {
					return fmt.Errorf("%s log: %w", testCase.ID, err)
				}
				if err := compareHookFallbackLog(sandbox, fallbackPath,
					filepath.Join(sandbox.caseFile("hooks", scenario, testCase.ID), "expected_hook_log.txt")); err != nil {
					return fmt.Errorf("%s fallback log: %w", testCase.ID, err)
				}
			}
			return nil
		})
	})
}

// readSurfaceStdin reads and token-expands one hook payload template.
func readSurfaceStdin(sandbox *surfaceSandbox, scenario, caseID string) (string, error) {
	for _, name := range []string{"stdin.json", "stdin.txt"} {
		raw, err := os.ReadFile(filepath.Join(sandbox.caseFile("hooks", scenario, caseID), name))
		if err == nil {
			return sandbox.expand(string(raw)), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
	}
	return "", fmt.Errorf("no stdin template")
}

// compareHookLog compares the main hook log line by line; ts becomes <NOW>
// and ms 0 (§6.4). An absent expectation means the case must not log.
func compareHookLog(sandbox *surfaceSandbox, actualPath, wantPath string) error {
	actual, err := readSurfaceLines(actualPath)
	if err != nil {
		return err
	}
	if updateGolden {
		lines := make([]string, 0, len(actual))
		for index := range actual {
			line, err := sandbox.normalizeHookLogLine(actual[index])
			if err != nil {
				return fmt.Errorf("line %d: %w", index+1, err)
			}
			lines = append(lines, line)
		}
		if len(lines) == 0 {
			return nil
		}
		return writeExpected(wantPath, []byte(strings.Join(lines, "\n")+"\n"))
	}
	raw, err := os.ReadFile(wantPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if len(actual) != 0 {
				return fmt.Errorf("unexpected log lines: %v", actual)
			}
			return nil
		}
		return err
	}
	want := splitNonEmpty(string(raw))
	if len(actual) != len(want) {
		return fmt.Errorf("log line count = %d, want %d", len(actual), len(want))
	}
	for index := range actual {
		gotLine, err := sandbox.normalizeHookLogLine(actual[index])
		if err != nil {
			return fmt.Errorf("line %d: %w", index+1, err)
		}
		wantLine, err := sandbox.normalizeHookLogLine(want[index])
		if err != nil {
			return fmt.Errorf("expected line %d: %w", index+1, err)
		}
		if gotLine != wantLine {
			return fmt.Errorf("line %d mismatch\n%s", index+1, diffReport(gotLine, wantLine))
		}
	}
	return nil
}

// normalizeHookLogLine rewrites the volatile hook log fields.
func (s *surfaceSandbox) normalizeHookLogLine(line string) (string, error) {
	value, err := decodeJSON([]byte(line))
	if err != nil {
		return "", err
	}
	row, ok := value.(map[string]any)
	if !ok {
		return "", fmt.Errorf("log line is not an object")
	}
	row["ts"] = "<NOW>"
	if _, present := row["ms"]; present {
		row["ms"] = json.Number("0")
	}
	normalized, err := s.normalizer.Value(row, "")
	if err != nil {
		return "", err
	}
	return ids.PyJSONDump(normalized, false, true, false)
}

// compareHookFallbackLog compares default_state_dir()/hook.log; its lines start
// with the frozen epoch.
func compareHookFallbackLog(sandbox *surfaceSandbox, actualPath, wantPath string) error {
	raw, err := os.ReadFile(wantPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if _, statErr := os.Stat(actualPath); statErr == nil {
				return fmt.Errorf("unexpected fallback log: %s", actualPath)
			}
			return nil
		}
		return err
	}
	actual, err := os.ReadFile(actualPath)
	if err != nil {
		return err
	}
	normalized := hookFallbackStampRE.ReplaceAllString(string(actual), "<NOW> ")
	normalized = sandbox.normalizer.Text(normalized)
	if normalized != string(raw) {
		return fmt.Errorf("fallback log mismatch\n%s", diffReport(normalized, string(raw)))
	}
	return nil
}

var hookFallbackStampRE = regexp.MustCompile(`^\d+ `)

// readSurfaceLines reads a newline-delimited file; a missing file is empty.
func readSurfaceLines(path string) ([]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	return splitNonEmpty(string(raw)), nil
}

// ReplayInstallSurface replays install/<scenario>/case.json. binaryPath is the
// freshly built launcher, copied to <CASE>/bin/rolling-memory.
func ReplayInstallSurface(workRoot, repoRoot, scenario, binaryPath string) error {
	sandbox, err := newSurfaceSandbox(workRoot, repoRoot, "install", scenario)
	if err != nil {
		return err
	}
	spec := &surfaceSpec{}
	if err := loadSurfaceJSON(sandbox.caseFile("install", scenario, "case.json"), spec); err != nil {
		return err
	}
	if spec.HomeTemplate != "" {
		if err := copyTree(filepath.Join(sandbox.goldenRoot, spec.HomeTemplate), sandbox.home); err != nil {
			return err
		}
	}
	// DefaultSkillFile resolves next to the process executable, which is the
	// go test binary here; the oracle's in-process cli.main used the package
	// skill directly. The runner supplies the checkout's skill source like it
	// supplies the launcher binary (the flag is not echoed in any report).
	skillSource := filepath.Join(repoRoot, "skills", "rolling-memory", "SKILL.md")
	if err := installSurfaceBinary(sandbox, spec.Binary, binaryPath); err != nil {
		return err
	}
	return withWorkingDir(sandbox.caseDir, func() error {
		return withProcessEnv(sandbox.env(spec.Env, spec.Scope), func() error {
			for _, testCase := range spec.Cases {
				argv := sandbox.expandArgv(testCase.Argv)
				argv = append(argv, "--skill-file", skillSource)
				run := sandbox.run(argv, "")
				if err := compareExit(run.code,
					sandbox.expectation("install", scenario, testCase.ID+".exit.txt")); err != nil {
					return fmt.Errorf("%s: %w", testCase.ID, err)
				}
				if err := sandbox.compareText(run.stderr,
					sandbox.expectation("install", scenario, testCase.ID+".stderr.txt")); err != nil {
					return fmt.Errorf("%s stderr: %w", testCase.ID, err)
				}
				if err := sandbox.compareJSON([]byte(run.stdout),
					sandbox.expectation("install", scenario, testCase.ID+".report.json")); err != nil {
					return fmt.Errorf("%s report: %w", testCase.ID, err)
				}
				switch testCase.ID {
				case "050-force":
					if err := sandbox.compareTree(scenario,
						sandbox.expectation("install", scenario, "tree-after-force.json"),
						"state after 050-force"); err != nil {
						return err
					}
				case "070-uninstall":
					if err := sandbox.compareTree(scenario,
						sandbox.expectation("install", scenario, "tree.json"),
						"state after 070-uninstall"); err != nil {
						return err
					}
				}
			}
			return nil
		})
	})
}

// installSurfaceBinary copies the built launcher to the case path.
func installSurfaceBinary(sandbox *surfaceSandbox, token, source string) error {
	if token == "" {
		token = "<CASE>/bin/rolling-memory"
	}
	target := sandbox.expand(token)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	raw, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	return os.WriteFile(target, raw, 0o755)
}

var backupFileNameRE = regexp.MustCompile(`\.rolling-memory\.\d+\.bak$`)

// compareTree dumps <HOME> (files plus backup names) and compares it with the
// sealed tree expectation. Backups are listed separately and sorted as raw
// strings; regular files keep the walk order.
func (s *surfaceSandbox) compareTree(scenario, wantPath, stage string) error {
	files := []any{}
	backups := []string{}
	err := filepath.WalkDir(s.home, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if backupFileNameRE.MatchString(entry.Name()) {
			backups = append(backups, s.normalizer.Text(path))
			return nil
		}
		executable := info.Mode()&0o111 != 0
		if isSkillFile(path) {
			files = append(files, map[string]any{
				"executable": false,
				"kind":       "skill",
				"note":       "copied from the repo skill source; content asserted by the install report",
				"path":       s.normalizer.Text(path),
			})
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files = append(files, map[string]any{
			"content":    s.normalizer.Text(string(raw)),
			"executable": executable,
			"kind":       "text",
			"path":       s.normalizer.Text(path),
		})
		return nil
	})
	if err != nil {
		return err
	}
	sort.Strings(backups)
	backupItems := make([]any, 0, len(backups))
	for _, backup := range backups {
		backupItems = append(backupItems, backup)
	}
	produced := map[string]any{
		"backups":  backupItems,
		"files":    files,
		"note":     stage + "; the launcher binary at <CASE>/bin/rolling-memory is provided by the runner",
		"root":     s.normalizer.Text(s.caseDir),
		"scenario": scenario,
	}
	canonical, err := CanonicalJSON(produced)
	if err != nil {
		return err
	}
	if updateGolden {
		return writeExpected(wantPath, []byte(canonical))
	}
	raw, err := os.ReadFile(wantPath)
	if err != nil {
		return err
	}
	if canonical != string(raw) {
		return fmt.Errorf("%s tree mismatch\n%s", stage, diffReport(canonical, string(raw)))
	}
	return nil
}

// isSkillFile matches the copied skill payload (kind=skill in the tree dump).
func isSkillFile(path string) bool {
	return filepath.Base(path) == "SKILL.md" &&
		strings.Contains(filepath.ToSlash(path), "/skills/rolling-memory/")
}

// fileExists reports whether path is a regular file.
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}
