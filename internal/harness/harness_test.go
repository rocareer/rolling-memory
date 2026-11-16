// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package harness

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rocareer/rolling-memory/internal/clock"
)

// fixture is a synthetic HOME with all five harness directories present. It
// never touches the real user configuration.
type fixture struct {
	t       *testing.T
	home    string
	dsh     string
	project string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	base := t.TempDir()
	home := filepath.Join(base, "home")
	fx := &fixture{
		t:       t,
		home:    home,
		dsh:     filepath.Join(home, ".dsh"),
		project: filepath.Join(home, "project"),
	}
	for _, path := range []string{
		filepath.Join(home, ".zcode"),
		filepath.Join(home, ".claude"),
		filepath.Join(home, ".codex", "skills"),
		filepath.Join(fx.dsh, "profiles", "web"),
		filepath.Join(home, ".codebuddy"),
		fx.project,
	} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
	}
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "xdg-data"))
	t.Setenv("DSH_HOME", "")
	return fx
}

func (fx *fixture) registry() []Harness {
	return BuildHarnesses(fx.home, fx.dsh)
}

func (fx *fixture) writeSkillSource(content string) string {
	fx.t.Helper()
	path := filepath.Join(fx.home, "SKILL.md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		fx.t.Fatalf("write skill source: %v", err)
	}
	return path
}

const syntheticSkill = "---\nname: rolling-memory\ndescription: synthetic test skill\n---\n\n# body\n"

func (fx *fixture) options() InstallOptions {
	skill := fx.writeSkillSource(syntheticSkill)
	options := DefaultInstallOptions(fx.home, fx.project)
	options.Name = "rmem"
	options.SkillName = "rmem"
	options.SkillFile = &skill
	return options
}

func (fx *fixture) optionsWith(mutate func(*InstallOptions)) InstallOptions {
	fx.t.Helper()
	options := fx.options()
	mutate(&options)
	return options
}

func (fx *fixture) zcodeConfig() string {
	return filepath.Join(fx.home, ".zcode", "cli", "config.json")
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}

func readJSONObject(t *testing.T, path string) map[string]any {
	t.Helper()
	var data map[string]any
	if err := json.Unmarshal([]byte(readFile(t, path)), &data); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return data
}

func objectAt(t *testing.T, data any, keys ...string) map[string]any {
	t.Helper()
	node := data
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

func actionsFor(report *Report, harnessID, component string) []Action {
	result := make([]Action, 0)
	for _, action := range report.Actions {
		if action.Harness == harnessID && action.Component == component {
			result = append(result, action)
		}
	}
	return result
}

func firstAction(t *testing.T, report *Report, harnessID, component string) Action {
	t.Helper()
	actions := actionsFor(report, harnessID, component)
	if len(actions) == 0 {
		t.Fatalf("no %s/%s action in %#v", harnessID, component, report.Actions)
	}
	return actions[0]
}

func assertNoFailures(t *testing.T, report *Report) {
	t.Helper()
	if failures := report.Failures(); len(failures) != 0 {
		t.Fatalf("unexpected failures: %#v\nreport: %#v", failures, report.Actions)
	}
}

func TestBuildHarnessesRegistry(t *testing.T) {
	fx := newFixture(t)
	registry := fx.registry()
	ids := make([]string, 0, len(registry))
	for _, harness := range registry {
		ids = append(ids, harness.ID)
	}
	want := []string{"zcode", "claude-code", "codex", "dsh", "codebuddy"}
	if strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Fatalf("ids = %v, want %v", ids, want)
	}
	index := harnessIndex(registry)
	zcode := index["zcode"]
	if got, want := zcode.MCPPath, filepath.Join(fx.home, ".zcode", "cli", "config.json"); got != want {
		t.Fatalf("zcode mcp path = %q, want %q", got, want)
	}
	if strings.Join(zcode.MCPKeys, ".") != "mcp.servers" {
		t.Fatalf("zcode mcp keys = %v", zcode.MCPKeys)
	}
	if strings.Join(zcode.HooksKeys, ".") != "hooks.events" || strings.Join(zcode.HooksEnabledKeys, ".") != "hooks.enabled" {
		t.Fatalf("zcode hook keys = %v / %v", zcode.HooksKeys, zcode.HooksEnabledKeys)
	}
	if index["codex"].MCPKind != "toml" || index["dsh"].MCPKind != "dsh" {
		t.Fatalf("mcp kinds = %q / %q", index["codex"].MCPKind, index["dsh"].MCPKind)
	}
	if got := index["dsh"].Detect; got != fx.dsh {
		t.Fatalf("dsh detect = %q, want %q", got, fx.dsh)
	}
	if index["codebuddy"].InstructionsUser != nil {
		t.Fatalf("codebuddy should have no user instruction file")
	}
}

func TestBuildHarnessesDSHEnv(t *testing.T) {
	home := t.TempDir()
	t.Setenv("DSH_HOME", "/tmp/rm-dsh-home")
	registry := BuildHarnesses(home, "")
	for _, harness := range registry {
		if harness.ID == "dsh" {
			if harness.Detect != "/tmp/rm-dsh-home" {
				t.Fatalf("dsh detect = %q", harness.Detect)
			}
			return
		}
	}
	t.Fatal("dsh not in registry")
}

func TestWithProjectDefaults(t *testing.T) {
	base := t.TempDir()
	options := DefaultInstallOptions(base, filepath.Join(base, "My Project"))
	options.Project = true
	resolved := WithProjectDefaults(options)
	if want := filepath.Join(resolved.Cwd, ".rolling-memory", "memory.db"); resolved.DB != want {
		t.Fatalf("db = %q, want %q", resolved.DB, want)
	}
	if resolved.Scope != "project:My-Project" {
		t.Fatalf("scope = %q", resolved.Scope)
	}

	cjk := DefaultInstallOptions(base, filepath.Join(base, "项目"))
	cjk.Project = true
	if got := WithProjectDefaults(cjk).Scope; got != "project:项目" {
		t.Fatalf("cjk scope = %q", got)
	}

	empty := DefaultInstallOptions(base, filepath.Join(base, "---"))
	empty.Project = true
	if got := WithProjectDefaults(empty).Scope; got != "project:project" {
		t.Fatalf("empty slug = %q", got)
	}

	explicit := DefaultInstallOptions(base, filepath.Join(base, "x"))
	explicit.Project = true
	explicit.DB = "/tmp/x.db"
	explicit.Scope = "mem:task:keep"
	resolved = WithProjectDefaults(explicit)
	if resolved.DB != "/tmp/x.db" || resolved.Scope != "mem:task:keep" {
		t.Fatalf("explicit values overwritten: %#v", resolved)
	}

	plain := DefaultInstallOptions(base, "/tmp/x")
	if got := WithProjectDefaults(plain); got.Project || got.DB != "" || got.Scope != "" {
		t.Fatalf("non-project defaults changed: %#v", got)
	}
}

func TestValidateInstallOptions(t *testing.T) {
	options := DefaultInstallOptions("/tmp/home", "/tmp/work")
	if err := ValidateInstallOptions(options); err != nil {
		t.Fatalf("default options rejected: %v", err)
	}
	options.Name = "bad name"
	err := ValidateInstallOptions(options)
	if err == nil || !strings.Contains(err.Error(), "invalid server name 'bad name'") {
		t.Fatalf("expected invalid server name error, got %v", err)
	}
	options = DefaultInstallOptions("/tmp/home", "/tmp/work")
	options.SkillName = "bad/name"
	err = ValidateInstallOptions(options)
	if err == nil || !strings.Contains(err.Error(), "invalid skill name") {
		t.Fatalf("expected invalid skill name error, got %v", err)
	}
	options.Name = "a"
	options.SkillName = "B-2_ok"
	if err := ValidateInstallOptions(options); err != nil {
		t.Fatalf("valid names rejected: %v", err)
	}
}

func TestSelectHarnesses(t *testing.T) {
	fx := newFixture(t)
	registry := fx.registry()
	selected, err := SelectHarnesses(registry, []string{"zcode"}, true)
	if err != nil || len(selected) != 1 || selected[0].ID != "zcode" {
		t.Fatalf("select zcode = %#v, %v", selected, err)
	}
	if _, err := SelectHarnesses(registry, []string{"nope"}, true); err == nil ||
		!strings.Contains(err.Error(), "unknown harness 'nope'; known: zcode, claude-code, codex, dsh, codebuddy") {
		t.Fatalf("unknown harness error = %v", err)
	}
	detected, err := SelectHarnesses(registry, nil, true)
	if err != nil || len(detected) != 5 {
		t.Fatalf("detected = %d, %v", len(detected), err)
	}
	if err := os.RemoveAll(filepath.Join(fx.home, ".claude")); err != nil {
		t.Fatalf("remove .claude: %v", err)
	}
	detected, err = SelectHarnesses(registry, nil, true)
	if err != nil || len(detected) != 4 {
		t.Fatalf("detected after removal = %d, %v", len(detected), err)
	}
	all, err := SelectHarnesses(registry, nil, false)
	if err != nil || len(all) != 5 {
		t.Fatalf("all = %d, %v", len(all), err)
	}
}

func TestDescribeHarnesses(t *testing.T) {
	fx := newFixture(t)
	rows := DescribeHarnesses(fx.registry(), fx.options())
	if len(rows) != 5 {
		t.Fatalf("rows = %d", len(rows))
	}
	zcode := rows[0]
	if zcode["id"] != "zcode" || zcode["detected"] != true {
		t.Fatalf("zcode row = %#v", zcode)
	}
	if want := filepath.Join(fx.home, ".zcode", "cli", "config.json"); zcode["mcp"] != want {
		t.Fatalf("zcode mcp = %v, want %s", zcode["mcp"], want)
	}
	codebuddy := rows[4]
	if codebuddy["instructions"] != "-" {
		t.Fatalf("codebuddy user instructions = %v", codebuddy["instructions"])
	}
	projectOptions := fx.options()
	projectOptions.Project = true
	projectRows := DescribeHarnesses(fx.registry(), projectOptions)
	if want := filepath.Join(fx.project, ".zcode", "skills"); projectRows[0]["skill"] != want {
		t.Fatalf("project zcode skill = %v, want %s", projectRows[0]["skill"], want)
	}
	if want := filepath.Join(fx.project, "CODEBUDDY.md"); projectRows[4]["instructions"] != want {
		t.Fatalf("project codebuddy instructions = %v, want %s", projectRows[4]["instructions"], want)
	}
}

func TestCLISkillNameFallback(t *testing.T) {
	fx := newFixture(t)
	skill := fx.writeSkillSource(syntheticSkill)
	options := WithProjectDefaults(InstallOptions{
		Home:      fx.home,
		Cwd:       fx.project,
		Name:      "rmem",
		SkillFile: &skill,
	})
	if options.SkillName != "" {
		t.Fatalf("WithProjectDefaults must leave SkillName for `--skill-name or --name`")
	}
	options.SkillName = options.Name
	zcode, err := SelectHarnesses(fx.registry(), []string{"zcode"}, true)
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	assertNoFailures(t, Install(zcode, options))
	if !fileExists(filepath.Join(fx.home, ".zcode", "skills", "rmem", "SKILL.md")) {
		t.Fatalf("skill was not installed under --name")
	}
	defaults := InstallOptions{Home: fx.home, Cwd: fx.project, SkillFile: &skill}
	if got := defaults.normalized().SkillName; got != DefaultName {
		t.Fatalf("harness default skill name = %q", got)
	}
}

func TestBackupFile(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "config.json")
	if err := os.WriteFile(path, []byte("{\"a\":1}"), 0o640); err != nil {
		t.Fatalf("write: %v", err)
	}
	backup, err := backupFile(path)
	if err != nil {
		t.Fatalf("backup: %v", err)
	}
	base := filepath.Base(backup)
	if !strings.HasPrefix(base, "config.json.rolling-memory.") || !strings.HasSuffix(base, ".bak") {
		t.Fatalf("backup name = %q", base)
	}
	if len(strings.TrimSuffix(strings.TrimPrefix(base, "config.json.rolling-memory."), ".bak")) != 14 {
		t.Fatalf("backup timestamp is not 14 digits: %q", base)
	}
	if content := readFile(t, backup); content != "{\"a\":1}" {
		t.Fatalf("backup content = %q", content)
	}
	info, err := os.Stat(backup)
	if err != nil {
		t.Fatalf("stat backup: %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o640 {
		t.Fatalf("backup mode = %v", info.Mode().Perm())
	}
}

func TestBackupFilePreservesRepeatedWrites(t *testing.T) {
	when := time.Date(2026, time.November, 11, 12, 0, 0, 0, time.UTC)
	restore := clock.SetForTest(when, 0)
	defer restore()

	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte("original"), 0o640); err != nil {
		t.Fatalf("write: %v", err)
	}
	legacy := path + ".rolling-memory." + when.Format("20060102150405") + ".bak"
	if err := os.WriteFile(legacy, []byte("existing backup"), 0o600); err != nil {
		t.Fatalf("write existing backup: %v", err)
	}
	first, err := writeText(path, "first update")
	if err != nil {
		t.Fatalf("first write: %v", err)
	}
	second, err := writeText(path, "second update")
	if err != nil {
		t.Fatalf("second write: %v", err)
	}
	if first == second || first == legacy || second == legacy {
		t.Fatalf("backup paths are not distinct: %q %q %q", legacy, first, second)
	}
	for backup, want := range map[string]string{legacy: "existing backup", first: "original", second: "first update"} {
		if got := readFile(t, backup); got != want {
			t.Fatalf("backup %s = %q, want %q", backup, got, want)
		}
	}
	if got := readFile(t, path); got != "second update" {
		t.Fatalf("config = %q", got)
	}
}

func TestBackupFileConcurrent(t *testing.T) {
	when := time.Date(2026, time.November, 11, 12, 0, 0, 0, time.UTC)
	restore := clock.SetForTest(when, 0)
	defer restore()

	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte("original"), 0o640); err != nil {
		t.Fatalf("write: %v", err)
	}
	const count = 32
	type result struct {
		path string
		err  error
	}
	results := make(chan result, count)
	start := make(chan struct{})
	var workers sync.WaitGroup
	for i := 0; i < count; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			backup, err := backupFile(path)
			results <- result{backup, err}
		}()
	}
	close(start)
	workers.Wait()
	close(results)
	seen := make(map[string]bool)
	for result := range results {
		if result.err != nil {
			t.Fatalf("backup: %v", result.err)
		}
		if seen[result.path] {
			t.Fatalf("duplicate backup path: %s", result.path)
		}
		seen[result.path] = true
		if got := readFile(t, result.path); got != "original" {
			t.Fatalf("backup %s = %q", result.path, got)
		}
	}
	if len(seen) != count {
		t.Fatalf("backup count = %d, want %d", len(seen), count)
	}
}

func TestSkillText(t *testing.T) {
	fx := newFixture(t)
	options := fx.options()
	text, err := skillText(options)
	if err != nil {
		t.Fatalf("skillText: %v", err)
	}
	if !strings.Contains(text, "name: rmem") {
		t.Fatalf("skill text did not replace the name:\n%s", text)
	}
	if !strings.HasSuffix(text, "\n") || strings.HasSuffix(text, "\n\n") {
		t.Fatalf("skill text must end with exactly one newline: %q", text[len(text)-4:])
	}
	if !strings.Contains(text, "description: synthetic test skill") {
		t.Fatalf("skill text lost the description")
	}

	missing := filepath.Join(t.TempDir(), "missing.md")
	options.SkillFile = &missing
	if _, err := skillText(options); err == nil || !strings.Contains(err.Error(), "skill source not found") {
		t.Fatalf("missing skill error = %v", err)
	}
}

func TestPySplitLines(t *testing.T) {
	cases := []struct {
		text string
		want []string
	}{
		{"", nil},
		{"a\nb\n", []string{"a", "b"}},
		{"a\r\nb\rc", []string{"a", "b", "c"}},
		{"a\u2028b", []string{"a", "b"}},
	}
	for _, testCase := range cases {
		got := pySplitLines(testCase.text)
		if strings.Join(got, "|") != strings.Join(testCase.want, "|") {
			t.Fatalf("pySplitLines(%q) = %#v, want %#v", testCase.text, got, testCase.want)
		}
	}
}

// TestResolvedBinaryExpandsUserPaths pins the written-launcher contract: host
// runtimes do not expand ~ or $HOME, so ResolvedBinary must resolve those
// prefixes while leaving bare command names (PATH lookups) untouched.
func TestResolvedBinaryExpandsUserPaths(t *testing.T) {
	home := t.TempDir()
	want := filepath.Join(home, "bin", "rmem")
	for _, binary := range []string{"~/bin/rmem", "${HOME}/bin/rmem", "$HOME/bin/rmem"} {
		if got := (InstallOptions{Home: home, Binary: binary}).ResolvedBinary(); got != want {
			t.Fatalf("ResolvedBinary(%q) = %q, want %q", binary, got, want)
		}
	}
	if got := (InstallOptions{Home: home, Binary: "~"}).ResolvedBinary(); got != home {
		t.Fatalf("ResolvedBinary(~) = %q, want %q", got, home)
	}
	for _, binary := range []string{"rolling-memory", "/usr/local/bin/rolling-memory"} {
		if got := (InstallOptions{Home: home, Binary: binary}).ResolvedBinary(); got != binary {
			t.Fatalf("ResolvedBinary(%q) rewrote to %q", binary, got)
		}
	}
}
