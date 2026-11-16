// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package harness

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// groupCommand returns the command of the first hook in a hook group object.
func groupCommand(t *testing.T, group any) string {
	t.Helper()
	object, ok := group.(map[string]any)
	if !ok {
		t.Fatalf("hook group is not an object: %#v", group)
	}
	hooks, ok := object["hooks"].([]any)
	if !ok || len(hooks) == 0 {
		t.Fatalf("hook group has no hooks: %#v", group)
	}
	hook, ok := hooks[0].(map[string]any)
	if !ok {
		t.Fatalf("hook is not an object: %#v", hooks[0])
	}
	command, _ := hook["command"].(string)
	return command
}

func TestInstallWritesAllSurfaces(t *testing.T) {
	fx := newFixture(t)
	report := Install(fx.registry(), fx.options())
	assertNoFailures(t, report)

	shim := DefaultShimPath(fx.home)
	info, err := os.Stat(shim)
	if err != nil {
		t.Fatalf("shim missing: %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("shim is not executable: %v", info.Mode())
	}
	if content := readFile(t, shim); !strings.Contains(content, `exec "`+executablePath()+`" "$@"`) {
		t.Fatalf("shim does not exec the Go binary:\n%s", content)
	}

	skillRoots := map[string]string{
		"zcode":       filepath.Join(fx.home, ".zcode", "skills"),
		"claude-code": filepath.Join(fx.home, ".claude", "skills"),
		"codex":       filepath.Join(fx.home, ".codex", "skills"),
		"dsh":         filepath.Join(fx.dsh, "skills"),
		"codebuddy":   filepath.Join(fx.home, ".codebuddy", "skills"),
	}
	for harnessID, root := range skillRoots {
		skill := filepath.Join(root, "rmem", "SKILL.md")
		lines := pySplitLines(readFile(t, skill))
		if len(lines) > 4 {
			lines = lines[:4]
		}
		head := strings.Join(lines, "\n")
		if !strings.Contains(head, "name: rmem") || !strings.Contains(head, "description:") {
			t.Fatalf("%s skill head = %q", harnessID, head)
		}
	}

	zcode := readJSONObject(t, fx.zcodeConfig())
	entry := objectAt(t, zcode, "mcp", "servers", "rmem")
	if entry["command"] != shim || entry["type"] != "stdio" {
		t.Fatalf("zcode entry = %#v", entry)
	}
	if args, ok := entry["args"].([]any); !ok || len(args) != 1 || args[0] != "mcp" {
		t.Fatalf("zcode args = %#v", entry["args"])
	}

	claude := readJSONObject(t, filepath.Join(fx.home, ".claude.json"))
	if command := objectAt(t, claude, "mcpServers", "rmem")["command"]; command != shim {
		t.Fatalf("claude command = %v", command)
	}

	codex, err := parseTOML(readFile(t, filepath.Join(fx.home, ".codex", "config.toml")))
	if err != nil {
		t.Fatalf("parse codex toml: %v", err)
	}
	codexEntry := codex["mcp_servers"].(map[string]any)["rmem"].(map[string]any)
	if codexEntry["command"] != shim {
		t.Fatalf("codex command = %v", codexEntry["command"])
	}
	if codexEntry["startup_timeout_sec"] != int64(30) {
		t.Fatalf("codex startup_timeout_sec = %#v", codexEntry["startup_timeout_sec"])
	}

	patch := readFile(t, filepath.Join(fx.dsh, "profiles", "web", "cordis.patch.yml"))
	for _, want := range []string{"- id: rmem-mcp", "serverName: rmem", "'@deepseek-ai/dsh-mcp-client'"} {
		if !strings.Contains(patch, want) {
			t.Fatalf("dsh patch missing %q:\n%s", want, patch)
		}
	}

	codebuddy := readJSONObject(t, filepath.Join(fx.home, ".codebuddy", "mcp.json"))
	if _, ok := objectAt(t, codebuddy, "mcpServers")["rmem"]; !ok {
		t.Fatalf("codebuddy entry missing")
	}
}

func TestInstallDefaultNameKeepsSkillText(t *testing.T) {
	fx := newFixture(t)
	options := fx.options()
	options.Name = DefaultName
	options.SkillName = DefaultName
	assertNoFailures(t, Install(fx.registry(), options))
	installed := filepath.Join(fx.home, ".zcode", "skills", "rolling-memory", "SKILL.md")
	text, err := skillText(options)
	if err != nil {
		t.Fatalf("skillText: %v", err)
	}
	if readFile(t, installed) != text {
		t.Fatalf("installed skill differs from skillText")
	}
}

func TestInstallEnvOptions(t *testing.T) {
	fx := newFixture(t)
	options := fx.optionsWith(func(options *InstallOptions) {
		options.Scope = "mem:task:demo"
		options.DB = "/tmp/demo/memory.db"
	})
	report := Install(fx.registry(), options)
	assertNoFailures(t, report)

	entry := objectAt(t, readJSONObject(t, fx.zcodeConfig()), "mcp", "servers", "rmem")
	env := objectAt(t, entry, "env")
	if env["ROLLING_MEMORY_DB"] != "/tmp/demo/memory.db" || env["ROLLING_MEMORY_SCOPE"] != "mem:task:demo" {
		t.Fatalf("zcode env = %#v", env)
	}
	codex, err := parseTOML(readFile(t, filepath.Join(fx.home, ".codex", "config.toml")))
	if err != nil {
		t.Fatalf("parse codex toml: %v", err)
	}
	codexEntry := codex["mcp_servers"].(map[string]any)["rmem"].(map[string]any)
	codexEnv := codexEntry["env"].(map[string]any)
	if codexEnv["ROLLING_MEMORY_SCOPE"] != "mem:task:demo" {
		t.Fatalf("codex env = %#v", codexEnv)
	}

	dshAction := firstAction(t, report, "dsh", "mcp")
	if !strings.Contains(dshAction.Detail, "cannot carry env") {
		t.Fatalf("dsh detail = %q", dshAction.Detail)
	}
}

func TestInstallIdempotent(t *testing.T) {
	fx := newFixture(t)
	registry := fx.registry()
	options := fx.options()
	assertNoFailures(t, Install(registry, options))
	second := Install(registry, options)
	for _, action := range second.Actions {
		if action.Status != StatusUnchanged && action.Status != StatusSkipped {
			t.Fatalf("second install action = %#v", action)
		}
	}
}

func TestForeignEntryConflictsThenForce(t *testing.T) {
	fx := newFixture(t)
	config := fx.zcodeConfig()
	if err := os.MkdirAll(filepath.Dir(config), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	foreign := `{"mcp": {"servers": {"rmem": {"command": "/usr/bin/other", "args": ["mcp"]}, "keep": {"command": "x"}}}, "other": true}`
	if err := os.WriteFile(config, []byte(foreign), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	registry := fx.registry()
	report := Install(registry, fx.options())
	action := firstAction(t, report, "zcode", "mcp")
	if action.Status != StatusConflict {
		t.Fatalf("status = %q", action.Status)
	}
	data := readJSONObject(t, config)
	if objectAt(t, data, "mcp", "servers", "rmem")["command"] != "/usr/bin/other" || data["other"] != true {
		t.Fatalf("foreign config changed: %#v", data)
	}

	forced := Install(registry, fx.optionsWith(func(options *InstallOptions) { options.Force = true }))
	if action := firstAction(t, forced, "zcode", "mcp"); action.Status != StatusUpdated {
		t.Fatalf("forced status = %q", action.Status)
	}
	data = readJSONObject(t, config)
	if objectAt(t, data, "mcp", "servers", "rmem")["command"] != DefaultShimPath(fx.home) {
		t.Fatalf("forced entry = %#v", objectAt(t, data, "mcp", "servers", "rmem"))
	}
	if objectAt(t, data, "mcp", "servers", "keep")["command"] != "x" || data["other"] != true {
		t.Fatalf("forced install changed other keys: %#v", data)
	}
	backups, err := filepath.Glob(filepath.Join(filepath.Dir(config), "config.json.rolling-memory.*.bak"))
	if err != nil || len(backups) == 0 {
		t.Fatalf("expected a backup, got %v (%v)", backups, err)
	}
}

func TestJSONMergePreservesOtherContentAndOrder(t *testing.T) {
	fx := newFixture(t)
	path := filepath.Join(fx.home, ".claude.json")
	if err := os.WriteFile(path, []byte(`{
  "mcpServers": {
    "hulianui": {
      "command": "/bin/bash"
    }
  },
  "theme": "dark"
}
`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	assertNoFailures(t, Install(fx.registry(), fx.options()))
	data := readJSONObject(t, path)
	if data["theme"] != "dark" {
		t.Fatalf("theme lost: %#v", data)
	}
	if objectAt(t, data, "mcpServers", "hulianui")["command"] != "/bin/bash" {
		t.Fatalf("foreign server lost: %#v", data)
	}
	if args, ok := objectAt(t, data, "mcpServers", "rmem")["args"].([]any); !ok || args[0] != "mcp" {
		t.Fatalf("rmem entry = %#v", objectAt(t, data, "mcpServers", "rmem"))
	}
	raw := readFile(t, path)
	if strings.Index(raw, `"theme"`) < strings.Index(raw, `"mcpServers"`) {
		t.Fatalf("key order not preserved:\n%s", raw)
	}
	if !strings.HasSuffix(raw, "\n") || !strings.Contains(raw, "\n  \"theme\": \"dark\"") {
		t.Fatalf("JSON indentation/newline differs:\n%s", raw)
	}
}

func TestTOMLSectionSurgery(t *testing.T) {
	fx := newFixture(t)
	config := filepath.Join(fx.home, ".codex", "config.toml")
	original := "# my comment\n" +
		"model = \"gpt\"\n" +
		"\n" +
		"[mcp_servers.existing]\n" +
		"command = \"node\"\n" +
		"\n" +
		"[projects.\"/x\"]\n" +
		"trust_level = \"trusted\"\n"
	if err := os.WriteFile(config, []byte(original), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	registry := fx.registry()
	report := Install(registry, fx.options())
	if action := firstAction(t, report, "codex", "mcp"); action.Status != StatusInstalled {
		t.Fatalf("status = %q", action.Status)
	}
	text := readFile(t, config)
	for _, want := range []string{"# my comment", "[mcp_servers.existing]", "command = \"node\"", "trust_level = \"trusted\""} {
		if !strings.Contains(text, want) {
			t.Fatalf("TOML lost %q:\n%s", want, text)
		}
	}
	if got := strings.Count(text, "[mcp_servers.rmem]"); got != 1 {
		t.Fatalf("[mcp_servers.rmem] appears %d times:\n%s", got, text)
	}
	parsed, err := parseTOML(text)
	if err != nil {
		t.Fatalf("parse TOML: %v\n%s", err, text)
	}
	if parsed["model"] != "gpt" {
		t.Fatalf("model = %v", parsed["model"])
	}
	existing := parsed["mcp_servers"].(map[string]any)["existing"].(map[string]any)
	if existing["command"] != "node" {
		t.Fatalf("existing = %#v", existing)
	}
	projects := parsed["projects"].(map[string]any)["/x"].(map[string]any)
	if projects["trust_level"] != "trusted" {
		t.Fatalf("projects = %#v", projects)
	}

	again := Install(registry, fx.options())
	if action := firstAction(t, again, "codex", "mcp"); action.Status != StatusUnchanged {
		t.Fatalf("second status = %q", action.Status)
	}

	updated := Install(registry, fx.optionsWith(func(options *InstallOptions) { options.DB = "/tmp/n.db" }))
	if action := firstAction(t, updated, "codex", "mcp"); action.Status != StatusUpdated {
		t.Fatalf("updated status = %q", action.Status)
	}
	parsed, err = parseTOML(readFile(t, config))
	if err != nil {
		t.Fatalf("parse updated TOML: %v", err)
	}
	entry := parsed["mcp_servers"].(map[string]any)["rmem"].(map[string]any)
	if entry["env"].(map[string]any)["ROLLING_MEMORY_DB"] != "/tmp/n.db" {
		t.Fatalf("updated env = %#v", entry)
	}
	if parsed["mcp_servers"].(map[string]any)["existing"].(map[string]any)["command"] != "node" {
		t.Fatalf("existing entry lost after update")
	}
}

func TestTOMLConflictOnForeignBlock(t *testing.T) {
	fx := newFixture(t)
	config := filepath.Join(fx.home, ".codex", "config.toml")
	foreign := "[mcp_servers.rmem]\ncommand = \"/usr/bin/other\"\nargs = [\"mcp\"]\n"
	if err := os.WriteFile(config, []byte(foreign), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	report := Install(fx.registry(), fx.options())
	if action := firstAction(t, report, "codex", "mcp"); action.Status != StatusConflict {
		t.Fatalf("status = %q", action.Status)
	}
	if text := readFile(t, config); !strings.Contains(text, "/usr/bin/other") {
		t.Fatalf("foreign block changed:\n%s", text)
	}
}

func TestHooksWiredAndRemoved(t *testing.T) {
	fx := newFixture(t)
	config := fx.zcodeConfig()
	if err := os.MkdirAll(filepath.Dir(config), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	initial := `{"hooks": {"enabled": false, "events": {"PreToolUse": [{"hooks": [{"type": "command", "command": "guard.sh"}]}]}}}`
	if err := os.WriteFile(config, []byte(initial), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	registry := fx.registry()
	options := fx.optionsWith(func(options *InstallOptions) { options.Hooks = true })
	assertNoFailures(t, Install(registry, options))

	zcode := readJSONObject(t, config)
	if objectAt(t, zcode, "hooks")["enabled"] != true {
		t.Fatalf("hooks.enabled = %#v", objectAt(t, zcode, "hooks")["enabled"])
	}
	events := objectAt(t, zcode, "hooks", "events")
	command := groupCommand(t, events["UserPromptSubmit"].([]any)[0])
	if !strings.Contains(command, "hook recall --json") || !strings.Contains(command, DefaultShimPath(fx.home)) {
		t.Fatalf("hook command = %q", command)
	}
	if got := groupCommand(t, events["PreToolUse"].([]any)[0]); got != "guard.sh" {
		t.Fatalf("PreToolUse lost: %q", got)
	}
	claude := readJSONObject(t, filepath.Join(fx.home, ".claude", "settings.json"))
	if _, ok := objectAt(t, claude, "hooks")["UserPromptSubmit"]; !ok {
		t.Fatalf("claude hooks = %#v", claude["hooks"])
	}

	removed := Uninstall(registry, options)
	assertNoFailures(t, removed)
	zcode = readJSONObject(t, config)
	events = objectAt(t, zcode, "hooks", "events")
	if _, ok := events["UserPromptSubmit"]; ok {
		t.Fatalf("UserPromptSubmit not removed")
	}
	if got := groupCommand(t, events["PreToolUse"].([]any)[0]); got != "guard.sh" {
		t.Fatalf("PreToolUse not preserved: %q", got)
	}
}

func TestHooksFreshConfigKeyOrder(t *testing.T) {
	fx := newFixture(t)
	options := fx.optionsWith(func(options *InstallOptions) { options.Hooks = true })
	zcode, err := SelectHarnesses(fx.registry(), []string{"zcode"}, true)
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	assertNoFailures(t, Install(zcode, options))
	text := readFile(t, fx.zcodeConfig())
	if strings.Index(text, `"events"`) > strings.Index(text, `"enabled"`) {
		t.Fatalf("events must be created before enabled:\n%s", text)
	}
	assertNoFailures(t, Uninstall(zcode, options))
	want := "{\n  \"mcp\": {\n    \"servers\": {}\n  },\n  \"hooks\": {\n    \"events\": {},\n    \"enabled\": true\n  }\n}\n"
	if got := readFile(t, fx.zcodeConfig()); got != want {
		t.Fatalf("post-uninstall zcode config mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestHooksSkippedWithoutSurface(t *testing.T) {
	fx := newFixture(t)
	report := Install(fx.registry(), fx.optionsWith(func(options *InstallOptions) { options.Hooks = true }))
	action := firstAction(t, report, "codex", "hooks")
	if action.Status != StatusSkipped {
		t.Fatalf("status = %q", action.Status)
	}
}

func TestInstructionsBlockRoundtrip(t *testing.T) {
	fx := newFixture(t)
	agents := filepath.Join(fx.home, ".zcode", "AGENTS.md")
	if err := os.WriteFile(agents, []byte("# My rules\n\nkeep me\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	registry := fx.registry()
	options := fx.optionsWith(func(options *InstallOptions) { options.Instructions = true })
	assertNoFailures(t, Install(registry, options))
	text := readFile(t, agents)
	if !strings.Contains(text, InstructionBegin) || !strings.Contains(text, "rmem") {
		t.Fatalf("instruction block missing:\n%s", text)
	}
	if !strings.HasPrefix(text, "# My rules") {
		t.Fatalf("instruction file prefix changed:\n%s", text)
	}

	again := Install(registry, options)
	if action := firstAction(t, again, "zcode", "instructions"); action.Status != StatusUnchanged {
		t.Fatalf("second status = %q", action.Status)
	}

	assertNoFailures(t, Uninstall(registry, options))
	text = readFile(t, agents)
	if strings.Contains(text, InstructionBegin) {
		t.Fatalf("instruction block not removed:\n%s", text)
	}
	if !strings.Contains(text, "keep me") {
		t.Fatalf("user content lost:\n%s", text)
	}
}

func TestInstructionHalfMarkerConflict(t *testing.T) {
	fx := newFixture(t)
	agents := filepath.Join(fx.home, ".zcode", "AGENTS.md")
	if err := os.WriteFile(agents, []byte("# rules\n"+InstructionBegin+"\norphan\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	report := Install(fx.registry(), fx.optionsWith(func(options *InstallOptions) { options.Instructions = true }))
	if action := firstAction(t, report, "zcode", "instructions"); action.Status != StatusConflict {
		t.Fatalf("status = %q", action.Status)
	}
}

func TestUninstallRemovesOnlyOurs(t *testing.T) {
	fx := newFixture(t)
	claudePath := filepath.Join(fx.home, ".claude.json")
	if err := os.WriteFile(claudePath, []byte(`{"mcpServers": {"hulianui": {"command": "/bin/bash"}}}`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	registry := fx.registry()
	options := fx.options()
	assertNoFailures(t, Install(registry, options))
	report := Uninstall(registry, options)
	assertNoFailures(t, report)

	if fileExists(filepath.Join(fx.home, ".zcode", "skills", "rmem")) {
		t.Fatalf("zcode skill dir still present")
	}
	if fileExists(filepath.Join(fx.dsh, "skills", "rmem")) {
		t.Fatalf("dsh skill dir still present")
	}
	zcode := readJSONObject(t, fx.zcodeConfig())
	if _, ok := objectAt(t, zcode, "mcp", "servers")["rmem"]; ok {
		t.Fatalf("zcode entry still present")
	}
	claude := readJSONObject(t, claudePath)
	if _, ok := objectAt(t, claude, "mcpServers")["rmem"]; ok {
		t.Fatalf("claude entry still present")
	}
	if objectAt(t, claude, "mcpServers", "hulianui")["command"] != "/bin/bash" {
		t.Fatalf("foreign claude entry changed")
	}
	codex, err := parseTOML(readFile(t, filepath.Join(fx.home, ".codex", "config.toml")))
	if err != nil {
		t.Fatalf("parse codex: %v", err)
	}
	if servers, ok := codex["mcp_servers"].(map[string]any); ok {
		if _, ok := servers["rmem"]; ok {
			t.Fatalf("codex entry still present")
		}
	}
	patch := readFile(t, filepath.Join(fx.dsh, "profiles", "web", "cordis.patch.yml"))
	if strings.Contains(patch, "rmem") {
		t.Fatalf("dsh patch still has our block:\n%s", patch)
	}

	second := Uninstall(registry, options)
	for _, action := range second.Actions {
		if action.Status != StatusSkipped && action.Status != StatusConflict {
			t.Fatalf("second uninstall action = %#v", action)
		}
	}
}

func TestCheckPassesAfterInstall(t *testing.T) {
	fx := newFixture(t)
	registry := fx.registry()
	options := fx.options()
	assertNoFailures(t, Install(registry, options))
	report := Check(registry, options, false)
	assertNoFailures(t, report)
	if action := firstAction(t, report, "zcode", "skill"); action.Status != StatusOK {
		t.Fatalf("zcode skill status = %q", action.Status)
	}
	for _, action := range actionsFor(report, "zcode", "hooks") {
		if action.Status != StatusSkipped {
			t.Fatalf("hook check = %#v", action)
		}
	}
	if _, ok := report.Extra["server"]; ok {
		t.Fatalf("live=false must not run the handshake")
	}
}

func TestCheckDetectsMissing(t *testing.T) {
	fx := newFixture(t)
	report := Check(fx.registry(), fx.options(), false)
	if len(report.Failures()) == 0 {
		t.Fatalf("expected failures for a fresh home")
	}
	if action := firstAction(t, report, "zcode", "skill"); action.Status != StatusMissing {
		t.Fatalf("zcode skill status = %q", action.Status)
	}
}

func TestCheckReportsActualHookWiring(t *testing.T) {
	fx := newFixture(t)
	registry := fx.registry()
	options := fx.optionsWith(func(options *InstallOptions) { options.Hooks = true })
	assertNoFailures(t, Install(registry, options))

	// The check reports the wiring on disk; the --with-hooks flag on the check
	// run itself must not change what is found.
	for _, opts := range []InstallOptions{options, fx.options()} {
		report := Check(registry, opts, false)
		if action := firstAction(t, report, "zcode", "hooks"); action.Status != StatusOK {
			t.Fatalf("wired hooks check (Hooks=%v) = %#v", opts.Hooks, action)
		}
	}

	assertNoFailures(t, Uninstall(registry, options))
	report := Check(registry, fx.options(), false)
	if action := firstAction(t, report, "zcode", "hooks"); action.Status != StatusSkipped {
		t.Fatalf("unwired hooks check = %#v", action)
	}
}

func TestInstalledHarnessesFilter(t *testing.T) {
	fx := newFixture(t)
	registry := fx.registry()
	options := fx.options()
	zcode, err := SelectHarnesses(registry, []string{"zcode"}, true)
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	assertNoFailures(t, Install(zcode, options))
	installed := InstalledHarnesses(registry, options)
	if len(installed) != 1 || installed[0].ID != "zcode" {
		t.Fatalf("installed = %#v", installed)
	}
}

func TestProjectScope(t *testing.T) {
	fx := newFixture(t)
	registry := fx.registry()
	options := WithProjectDefaults(fx.optionsWith(func(options *InstallOptions) { options.Project = true }))
	assertNoFailures(t, Install(registry, options))

	for _, path := range []string{
		filepath.Join(fx.project, ".zcode", "config.json"),
		filepath.Join(fx.project, ".zcode", "skills", "rmem", "SKILL.md"),
		filepath.Join(fx.project, ".mcp.json"),
		filepath.Join(fx.project, ".claude", "skills", "rmem", "SKILL.md"),
	} {
		if !fileExists(path) {
			t.Fatalf("missing project artifact %s", path)
		}
	}
	entry := objectAt(t, readJSONObject(t, filepath.Join(fx.project, ".mcp.json")), "mcpServers", "rmem")
	env := objectAt(t, entry, "env")
	if _, ok := env["ROLLING_MEMORY_DB"]; !ok {
		t.Fatalf("project env = %#v", env)
	}
	scope, _ := env["ROLLING_MEMORY_SCOPE"].(string)
	if !strings.HasPrefix(scope, "project:") {
		t.Fatalf("project scope = %q", scope)
	}
}

func TestDryRunInstall(t *testing.T) {
	fx := newFixture(t)
	report := Install(fx.registry(), fx.optionsWith(func(options *InstallOptions) { options.DryRun = true }))
	assertNoFailures(t, report)
	if !report.ToDict()["dry_run"].(bool) {
		t.Fatalf("report dry_run = %#v", report.ToDict()["dry_run"])
	}
	if fileExists(fx.zcodeConfig()) {
		t.Fatalf("dry-run wrote the zcode config")
	}
	if fileExists(filepath.Join(fx.home, ".zcode", "skills", "rmem")) {
		t.Fatalf("dry-run wrote the skill")
	}
	if fileExists(DefaultShimPath(fx.home)) {
		t.Fatalf("dry-run wrote the shim")
	}
	if action := firstAction(t, report, "zcode", "mcp"); action.Status != StatusPlanned {
		t.Fatalf("mcp status = %q", action.Status)
	}
}

func TestCustomBinarySkipsShim(t *testing.T) {
	fx := newFixture(t)
	options := fx.optionsWith(func(options *InstallOptions) { options.Binary = "/usr/local/bin/rmem" })
	report := Install(fx.registry(), options)
	binaryActions := actionsFor(report, "*", "binary")
	if len(binaryActions) != 1 || binaryActions[0].Status != StatusSkipped {
		t.Fatalf("binary actions = %#v", binaryActions)
	}
	if fileExists(DefaultShimPath(fx.home)) {
		t.Fatalf("custom --binary must not write the shim")
	}
	entry := objectAt(t, readJSONObject(t, fx.zcodeConfig()), "mcp", "servers", "rmem")
	if entry["command"] != "/usr/local/bin/rmem" {
		t.Fatalf("entry command = %v", entry["command"])
	}
}

// TestInstallAndUninstallClaudeEntries verifies that installing for Claude Code
// modifies only the Claude surfaces and installs the shipped SKILL.md.
func TestInstallAndUninstallClaudeEntries(t *testing.T) {
	fx := newFixture(t)
	claudeConfig := filepath.Join(fx.home, ".claude.json")
	if err := os.WriteFile(claudeConfig, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write claude config: %v", err)
	}
	skill := repoSkillFile(t)
	options := DefaultInstallOptions(fx.home, fx.home)
	options.Name = "rm-test"
	options.SkillName = "rm-test"
	options.SkillFile = &skill

	claude, err := SelectHarnesses(fx.registry(), []string{"claude-code"}, true)
	if err != nil {
		t.Fatalf("select claude-code: %v", err)
	}

	assertNoFailures(t, Install(claude, options))
	servers := objectAt(t, readJSONObject(t, claudeConfig), "mcpServers")
	if _, ok := servers["rm-test"]; !ok {
		t.Fatalf("claude mcpServers = %#v", servers)
	}
	target := filepath.Join(fx.home, ".claude", "skills", "rm-test", "SKILL.md")
	text := readFile(t, target)
	if !strings.Contains(text, "name: rm-test") || !strings.Contains(text, "Rolling-Memory") {
		t.Fatalf("installed skill is not the shipped SKILL.md renamed:\n%s", text)
	}

	assertNoFailures(t, Install(claude, options))

	assertNoFailures(t, Uninstall(claude, options))
	data := readJSONObject(t, claudeConfig)
	if remaining, ok := data["mcpServers"].(map[string]any); ok {
		if _, still := remaining["rm-test"]; still {
			t.Fatalf("claude entry survived uninstall: %#v", data)
		}
	}
	if fileExists(target) {
		t.Fatalf("skill file survived uninstall: %s", target)
	}
}

func TestInstallInvalidNameReport(t *testing.T) {
	fx := newFixture(t)
	failed := Install(fx.registry(), fx.optionsWith(func(options *InstallOptions) { options.Name = "bad name" }))
	if len(failed.Failures()) == 0 {
		t.Fatalf("invalid name must fail")
	}
	if !strings.Contains(failed.Failures()[0].Detail, "invalid server name") {
		t.Fatalf("detail = %q", failed.Failures()[0].Detail)
	}
}
