// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package harness

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
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
	if content := readFile(t, shim); !strings.Contains(content, `exec `+shellDoubleQuote(executablePath())+` "$@"`) {
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
		t.Fatalf("updated action = %#v", action)
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
	initial := `{"hooks": {"enabled": true, "events": {"PreToolUse": [{"hooks": [{"type": "command", "command": "guard.sh"}]}]}}}`
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
	if _, found := objectAt(t, readJSONObject(t, fx.zcodeConfig()), "hooks")["enabled"]; found {
		t.Fatal("install must not add a global hooks switch")
	}
	assertNoFailures(t, Uninstall(zcode, options))
	want := "{\n  \"mcp\": {\n    \"servers\": {}\n  },\n  \"hooks\": {\n    \"events\": {}\n  }\n}\n"
	if got := readFile(t, fx.zcodeConfig()); got != want {
		t.Fatalf("post-uninstall zcode config mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestHooksSkippedWithoutSurface(t *testing.T) {
	fx := newFixture(t)
	report := Install(fx.registry(), fx.optionsWith(func(options *InstallOptions) { options.Hooks = true }))
	action := firstAction(t, report, "codebuddy", "hooks")
	if action.Status != StatusSkipped {
		t.Fatalf("status = %q", action.Status)
	}
}

// TestCodexHooksRoundtrip pins the Codex hook surface: the optional wiring
// writes the Claude-shaped command hook into ~/.codex/hooks.json (a
// first-hand-verified read point; the very first run additionally requires the
// user's review/trust inside Codex), stays idempotent, reports OK on check,
// and uninstall removes only our group.
func TestCodexHooksRoundtrip(t *testing.T) {
	fx := newFixture(t)
	options := fx.optionsWith(func(options *InstallOptions) { options.Hooks = true })
	codex, err := SelectHarnesses(fx.registry(), []string{"codex"}, true)
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	assertNoFailures(t, Install(codex, options))
	path := filepath.Join(fx.home, ".codex", "hooks.json")
	events := objectAt(t, readJSONObject(t, path), "hooks")
	groups, ok := events["UserPromptSubmit"].([]any)
	if !ok || len(groups) != 1 {
		t.Fatalf("codex hooks = %#v", events["UserPromptSubmit"])
	}
	if got, want := groupCommand(t, groups[0]), shlexQuote(options.ResolvedBinary())+" hook recall --json"; got != want {
		t.Fatalf("codex hook command = %q, want %q", got, want)
	}
	if action := firstAction(t, Install(codex, options), "codex", "hooks"); action.Status != StatusUnchanged {
		t.Fatalf("reinstall = %#v", action)
	}
	if action := firstAction(t, Check(codex, options, false), "codex", "hooks"); action.Status != StatusOK {
		t.Fatalf("check = %#v", action)
	}
	assertNoFailures(t, Uninstall(codex, options))
	if events := objectAt(t, readJSONObject(t, path), "hooks"); func() bool {
		_, found := events["UserPromptSubmit"]
		return found
	}() {
		t.Fatalf("UserPromptSubmit not removed: %#v", events)
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
	if !strings.Contains(text, "name: rm-test") || !strings.Contains(text, "# Rolling Memory 协作") {
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

func TestConfigSafetyTOMLSpans(t *testing.T) {
	original := "[before]\nx=1\n[mcp_servers.rmem]\ncommand = 'rmem'\nargs = ['mcp']\n[after]\nx=2\n"
	spans, err := tomlTableSpans(original, []string{"mcp_servers", "rmem"})
	if err != nil {
		t.Fatal(err)
	}
	remaining := tomlWithoutSpans(original, spans)
	if strings.Contains(remaining, "[mcp_servers.rmem]") || strings.Contains(remaining, "command = 'rmem'") {
		t.Fatalf("spans = %#v remaining = %q", spans, remaining)
	}
}

func TestConfigSafetyTOMLSeparatedTables(t *testing.T) {
	fx := newFixture(t)
	t.Setenv("HOME", fx.home)
	options := fx.options()
	codex := harnessIndex(fx.registry())["codex"]
	original := "[mcp_servers.rmem] # keep header comment\ncommand = '" + options.ResolvedBinary() + "'\nargs = ['mcp']\n" +
		"[other]\ntext = '''\n[not.a.table]\n'''\n" +
		"[mcp_servers.rmem.env]\nROLLING_MEMORY_DB = 'old'\n" +
		"[[profiles]]\nname = 'stay'\n"
	if err := os.WriteFile(codex.MCPPath, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	report := newReport("uninstall", options)
	uninstallMCPToml(codex, options, codex.MCPPath, report)
	if action := firstAction(t, report, "codex", "mcp"); action.Status != StatusRemoved {
		t.Fatalf("status = %#v", action)
	}
	got := readFile(t, codex.MCPPath)
	if !strings.Contains(got, "# keep header comment") || !strings.Contains(got, "[other]\ntext = '''\n[not.a.table]\n'''\n") || !strings.Contains(got, "[[profiles]]\nname = 'stay'\n") {
		t.Fatalf("unrelated bytes changed: %q", got)
	}
}

func TestConfigSafetyTOML(t *testing.T) {
	cases := []struct {
		name     string
		original func(InstallOptions) string
		status   string
	}{
		{"array-table", func(o InstallOptions) string {
			return "[mcp_servers.rmem]\ncommand = '" + o.ResolvedBinary() + "'\nargs = ['mcp']\n[[profiles]]\nname = 'keep'\n"
		}, StatusRemoved},
		{"foreign-comment", func(o InstallOptions) string {
			return "[mcp_servers.rmem]\ncommand = '/usr/bin/other'\nargs = ['mcp']\n# " + o.ResolvedBinary() + "\n"
		}, StatusConflict},
		{"invalid-source", func(o InstallOptions) string {
			return "[mcp_servers.rmem]\ncommand = '" + o.ResolvedBinary() + "'\nargs = ['mcp']\ninvalid = [\n"
		}, StatusError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := newFixture(t)
			t.Setenv("HOME", fx.home)
			options := fx.options()
			codex := harnessIndex(fx.registry())["codex"]
			original := tc.original(options)
			if err := os.WriteFile(codex.MCPPath, []byte(original), 0o644); err != nil {
				t.Fatal(err)
			}
			report := newReport("uninstall", options)
			uninstallMCPToml(codex, options, codex.MCPPath, report)
			if action := firstAction(t, report, "codex", "mcp"); action.Status != tc.status {
				t.Fatalf("status = %#v", action)
			}
			got := readFile(t, codex.MCPPath)
			if tc.status != StatusRemoved && got != original {
				t.Fatalf("protected config changed: %q", got)
			}
			if tc.name == "array-table" && !strings.Contains(got, "[[profiles]]\nname = 'keep'") {
				t.Fatalf("array table lost: %q", got)
			}
		})
	}

	t.Run("dotted-name", func(t *testing.T) {
		fx := newFixture(t)
		t.Setenv("HOME", fx.home)
		options := fx.options()
		options.Name = "team.memory"
		codex := harnessIndex(fx.registry())["codex"]
		original := "[mcp_servers.team.other]\ncommand = 'keep'\n"
		if err := os.WriteFile(codex.MCPPath, []byte(original), 0o644); err != nil {
			t.Fatal(err)
		}
		report := newReport("install", options)
		installMCPToml(codex, options, codex.MCPPath, report)
		if action := firstAction(t, report, "codex", "mcp"); action.Status != StatusInstalled {
			t.Fatalf("status = %#v", action)
		}
		parsed, err := parseTOML(readFile(t, codex.MCPPath))
		if err != nil {
			t.Fatal(err)
		}
		servers := parsed["mcp_servers"].(map[string]any)
		if _, ok := servers["team.memory"]; !ok {
			t.Fatalf("dotted name is not a single key: %#v", servers)
		}
		if servers["team"].(map[string]any)["other"].(map[string]any)["command"] != "keep" {
			t.Fatalf("other server changed: %#v", servers)
		}
	})
}

func TestConfigSafetyHooks(t *testing.T) {
	fx := newFixture(t)
	t.Setenv("HOME", fx.home)
	options := fx.options()
	zcode := harnessIndex(fx.registry())["zcode"]
	path := *zcode.HooksPath
	command := hookCommands(options)[0].Command
	original := `{"hooks":{"enabled":false,"events":{"UserPromptSubmit":[{"matcher":"keep","hooks":[{"type":"command","command":"guard.sh","extra":"preserve"},{"type":"command","command":` + strconv.Quote(command) + `}] }]}}}`
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	before := objectAt(t, readJSONObject(t, path), "hooks", "events")["UserPromptSubmit"].([]any)[0].(map[string]any)
	guard := before["hooks"].([]any)[0]
	report := newReport("uninstall", options)
	uninstallHooks(zcode, options, report)
	if action := firstAction(t, report, "zcode", "hooks"); action.Status != StatusRemoved {
		t.Fatalf("status = %#v", action)
	}
	hooks := objectAt(t, readJSONObject(t, path), "hooks")
	if hooks["enabled"] != false {
		t.Fatalf("global switch changed: %#v", hooks)
	}
	groups, ok := objectAt(t, hooks, "events")["UserPromptSubmit"].([]any)
	if !ok || len(groups) != 1 {
		t.Fatalf("mixed group lost: %#v", groups)
	}
	group := groups[0].(map[string]any)
	if group["matcher"] != "keep" || !reflect.DeepEqual(group["hooks"], []any{guard}) {
		t.Fatalf("foreign hook changed: %#v", group)
	}

	if err := os.WriteFile(path, []byte(`{"hooks":{"enabled":false,"events":{}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	report = newReport("install", options)
	installHooks(zcode, options, report)
	if objectAt(t, readJSONObject(t, path), "hooks")["enabled"] != false {
		t.Fatal("install enabled a disabled global switch")
	}
}

func TestConfigSafetyHookDisabledAndMalformed(t *testing.T) {
	fx := newFixture(t)
	t.Setenv("HOME", fx.home)
	options := fx.options()
	zcode := harnessIndex(fx.registry())["zcode"]
	path := *zcode.HooksPath
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, original := range []string{
		`{"hooks":false}`,
		`{"hooks":{"enabled":"invalid","events":{}}}`,
		`{"hooks":{"events":{"UserPromptSubmit":[{"hooks":false}]}}}`,
	} {
		if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
			t.Fatal(err)
		}
		report := newReport("install", options)
		installHooks(zcode, options, report)
		if action := firstAction(t, report, "zcode", "hooks"); action.Status != StatusConflict {
			t.Fatalf("invalid config accepted: %#v", action)
		}
		if got := readFile(t, path); got != original {
			t.Fatalf("invalid config changed: %q", got)
		}
	}
	original := `{"hooks":{"events":{"UserPromptSubmit":[{"hooks":[{"command":"guard.sh"}]}]}}}`
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	report := newReport("install", options)
	installHooks(zcode, options, report)
	assertNoFailures(t, report)
	if _, found := objectAt(t, readJSONObject(t, path), "hooks")["enabled"]; found {
		t.Fatal("install enabled unrelated hooks by adding the global switch")
	}
}

func TestConfigSafetyHookInstances(t *testing.T) {
	fx := newFixture(t)
	t.Setenv("HOME", fx.home)
	first := fx.options()
	second := first
	first.Binary = filepath.Join(fx.home, "one", "rolling-memory")
	second.Binary = filepath.Join(fx.home, "two", "rolling-memory")
	zcode := harnessIndex(fx.registry())["zcode"]
	for _, options := range []InstallOptions{first, second} {
		report := newReport("install", options)
		installHooks(zcode, options, report)
		assertNoFailures(t, report)
	}
	report := newReport("uninstall", first)
	uninstallHooks(zcode, first, report)
	assertNoFailures(t, report)
	groups := objectAt(t, readJSONObject(t, *zcode.HooksPath), "hooks", "events")["UserPromptSubmit"].([]any)
	if len(groups) != 1 || groupCommand(t, groups[0]) != hookCommands(second)[0].Command {
		t.Fatalf("other instance changed: %#v", groups)
	}
}

func TestConfigSafetyProjectHookIsolation(t *testing.T) {
	fx := newFixture(t)
	t.Setenv("HOME", fx.home)
	options := fx.options()
	options.Hooks = true
	zcode := harnessIndex(fx.registry())["zcode"]
	assertNoFailures(t, Install([]Harness{zcode}, options))
	global := readFile(t, *zcode.HooksPath)
	project := options
	project.Project = true
	assertNoFailures(t, Install([]Harness{zcode}, project))
	assertNoFailures(t, Uninstall([]Harness{zcode}, project))
	if got := readFile(t, *zcode.HooksPath); got != global {
		t.Fatalf("project operations changed user hooks:\n%s", got)
	}
}

func TestConfigSafetyDSHMarkers(t *testing.T) {
	fx := newFixture(t)
	t.Setenv("HOME", fx.home)
	options := fx.options()
	dsh := harnessIndex(fx.registry())["dsh"]
	path, _ := mcpTargetPath(dsh, options)
	begin, end := dshMarkers(options.Name)
	for _, original := range []string{
		end + "\n" + begin + "\n",
		begin + "\ncommand: '/usr/bin/other'\n# " + options.ResolvedBinary() + "\n" + end + "\n",
	} {
		if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
			t.Fatal(err)
		}
		report := newReport("uninstall", options)
		uninstallMCPDsh(dsh, options, path, report)
		if action := firstAction(t, report, "dsh", "mcp"); action.Status != StatusConflict {
			t.Fatalf("status = %#v", action)
		}
		if got := readFile(t, path); got != original {
			t.Fatalf("foreign or malformed block changed: %q", got)
		}
	}
}

func TestConfigSafetyShimAndBinaryFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell probe")
	}
	fx := newFixture(t)
	t.Setenv("HOME", fx.home)
	marker := filepath.Join(fx.home, "injected")
	binary := filepath.Join(fx.home, "$(touch${IFS}"+marker+")")
	shim := filepath.Join(fx.home, "probe.sh")
	if err := os.WriteFile(shim, []byte(shimTextFor("darwin", binary)), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = exec.Command("/bin/sh", shim).Run()
	if fileExists(marker) {
		t.Fatal("shell expanded the binary path")
	}
	portableRoot := filepath.Join(fx.home, "'$(touch${IFS}"+marker+")")
	portableBinary := filepath.Join(portableRoot, "bin", "rm")
	if err := os.WriteFile(shim, []byte(PortableShimTextFor("darwin", portableBinary, portableRoot)), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = exec.Command("/bin/sh", shim).Run()
	if fileExists(marker) {
		t.Fatal("portable shell expanded a path")
	}

	options := fx.options()
	blocked := DefaultShimPath(fx.home)
	if err := os.MkdirAll(filepath.Dir(blocked), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(blocked, []byte("foreign"), 0o755); err != nil {
		t.Fatal(err)
	}
	report := Install([]Harness{harnessIndex(fx.registry())["zcode"]}, options)
	if len(report.Failures()) == 0 {
		t.Fatal("foreign shim should fail")
	}
	if fileExists(fx.zcodeConfig()) {
		t.Fatal("MCP config written despite failed launcher")
	}
}

func TestConfigSafetyDSHBundleSelection(t *testing.T) {
	for _, tc := range []struct {
		name     string
		bundles  string
		selected bool
	}{
		{"selected", `["@rocareer/dsh-rolling-memory"]`, true},
		{"not-selected", `["@rocareer/other"]`, false},
		{"similar-name", `["@another/dsh-rolling-memory"]`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fx := newFixture(t)
			t.Setenv("HOME", fx.home)
			options := fx.options()
			options.Name = DefaultName
			options.Hooks = true
			dsh := harnessIndex(fx.registry())["dsh"]
			path, _ := mcpTargetPath(dsh, options)
			manifest := `{"dependencies":{"@rocareer/dsh-rolling-memory":"file:bundle.tgz"},"dsh":{"profile":{"bundles":` + tc.bundles + `}}}`
			if err := os.WriteFile(filepath.Join(filepath.Dir(path), "package.json"), []byte(manifest), 0o644); err != nil {
				t.Fatal(err)
			}
			for attempt := 0; attempt < 2; attempt++ {
				report := Install([]Harness{dsh}, options)
				action := firstAction(t, report, "dsh", "mcp")
				if tc.selected && action.Status != StatusSkipped {
					t.Fatalf("selected bundle accepted duplicate: %#v", action)
				}
				if !tc.selected && action.Status != StatusInstalled && action.Status != StatusUnchanged {
					t.Fatalf("unselected bundle blocked installation: %#v", action)
				}
			}
			if tc.selected && fileExists(path) {
				t.Fatal("selected bundle caused a manual marker to be created")
			}
		})
	}
}

func TestConfigSafetyDSHForeignServer(t *testing.T) {
	fx := newFixture(t)
	t.Setenv("HOME", fx.home)
	options := fx.options()
	options.Name = DefaultName
	dsh := harnessIndex(fx.registry())["dsh"]
	path, _ := mcpTargetPath(dsh, options)
	original := "- insert:\n    - id: external\n      config:\n        serverName: rolling-memory\n        command: other\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	report := Install([]Harness{dsh}, options)
	if action := firstAction(t, report, "dsh", "mcp"); action.Status != StatusConflict {
		t.Fatalf("foreign server accepted: %#v", action)
	}
	if got := readFile(t, path); got != original {
		t.Fatalf("foreign entry changed: %q", got)
	}
}

func TestConfigSafetyDSHBundleExistingMarker(t *testing.T) {
	fx := newFixture(t)
	t.Setenv("HOME", fx.home)
	options := fx.options()
	options.Name = DefaultName
	options.Hooks = true
	dsh := harnessIndex(fx.registry())["dsh"]
	path, _ := mcpTargetPath(dsh, options)
	assertNoFailures(t, Install([]Harness{dsh}, options))
	original := readFile(t, path)
	manifest := `{"dsh":{"profile":{"bundles":["@rocareer/dsh-rolling-memory"]}}}`
	if err := os.WriteFile(filepath.Join(filepath.Dir(path), "package.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		report := Install([]Harness{dsh}, options)
		if action := firstAction(t, report, "dsh", "mcp"); action.Status != StatusConflict {
			t.Fatalf("existing duplicate status = %#v", action)
		}
		if got := readFile(t, path); got != original {
			t.Fatalf("existing marker changed: %q", got)
		}
	}
}

func TestConfigSafetyDSHProfile(t *testing.T) {
	fx := newFixture(t)
	t.Setenv("HOME", fx.home)
	dsh := harnessIndex(fx.registry())["dsh"]
	outside := filepath.Join(fx.home, "victim", "cordis.patch.yml")
	if err := os.MkdirAll(filepath.Dir(outside), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte("untouched\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, profile := range []string{"../../victim", ".", "..", "/tmp/victim", `..\..\victim`, `C:\victim`, "dir/name"} {
		options := fx.options()
		options.DSHProfile = profile
		report := Install([]Harness{dsh}, options)
		if len(report.Failures()) == 0 {
			t.Fatalf("accepted profile %q", profile)
		}
		if got := readFile(t, outside); got != "untouched\n" {
			t.Fatalf("profile %q changed outside file: %q", profile, got)
		}
	}
}

func TestConfigSafetyDSHSymlinks(t *testing.T) {
	fx := newFixture(t)
	t.Setenv("HOME", fx.home)
	dsh := harnessIndex(fx.registry())["dsh"]
	options := fx.options()
	outside := filepath.Join(fx.home, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(outside, "cordis.patch.yml")
	if err := os.WriteFile(victim, []byte("untouched\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	profile := filepath.Join(*dsh.ProfilesDir, "linked")
	if err := os.Symlink(outside, profile); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	options.DSHProfile = "linked"
	report := Install([]Harness{dsh}, options)
	if action := firstAction(t, report, "dsh", "mcp"); action.Status != StatusConflict {
		t.Fatalf("profile symlink status = %#v", action)
	}
	if got := readFile(t, victim); got != "untouched\n" {
		t.Fatalf("symlink target changed: %q", got)
	}
	options.DSHProfile = "web"
	patch := filepath.Join(*dsh.ProfilesDir, "web", "cordis.patch.yml")
	if err := os.Symlink(victim, patch); err != nil {
		t.Fatal(err)
	}
	report = Install([]Harness{dsh}, options)
	if action := firstAction(t, report, "dsh", "mcp"); action.Status != StatusConflict {
		t.Fatalf("patch symlink status = %#v", action)
	}
	if got := readFile(t, victim); got != "untouched\n" {
		t.Fatalf("patch target changed: %q", got)
	}
}

func TestRedTeamDSHBundleCheck(t *testing.T) {
	fx := newFixture(t)
	t.Setenv("HOME", fx.home)
	options := fx.options()
	options.Name = DefaultName
	dsh := harnessIndex(fx.registry())["dsh"]
	path, _ := mcpTargetPath(dsh, options)
	manifest := `{"dsh":{"profile":{"bundles":["@rocareer/dsh-rolling-memory"]}}}`
	if err := os.WriteFile(filepath.Join(filepath.Dir(path), "package.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, withFile := range []bool{false, true} {
		if withFile {
			if err := os.WriteFile(path, []byte("# empty\n[]\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		report := newReport("check", options)
		checkMCP(dsh, options, report)
		if action := firstAction(t, report, "dsh", "mcp"); action.Status != StatusSkipped || !strings.Contains(action.Detail, "unverified") {
			t.Fatalf("selected bundle check = %#v", action)
		}
	}
}

func TestRedTeamDSHTrustedRootSymlink(t *testing.T) {
	fx := newFixture(t)
	t.Setenv("HOME", fx.home)
	root := filepath.Join(fx.home, "dsh-root-link")
	if err := os.Symlink(fx.dsh, root); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	dsh := harnessIndex(BuildHarnesses(fx.home, root))["dsh"]
	options := fx.options()
	report := newReport("install", options)
	installMCP(dsh, options, report)
	if action := firstAction(t, report, "dsh", "mcp"); action.Status != StatusInstalled {
		t.Fatalf("trusted root symlink rejected: %#v", action)
	}
}

func TestRedTeamJSONUserFields(t *testing.T) {
	fx := newFixture(t)
	t.Setenv("HOME", fx.home)
	options := fx.options()
	zcode := harnessIndex(fx.registry())["zcode"]
	path := zcode.MCPPath
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	original := `{"mcp":{"servers":{"rmem":{"type":"stdio","command":` + strconv.Quote(options.ResolvedBinary()) + `,"args":["mcp"],"disabled":true,"env":{"USER_KEY":"keep"},"custom":{"keep":1}}}}}`
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	report := newReport("install", options)
	installMCPJSON(zcode, options, path, report)
	if action := firstAction(t, report, "zcode", "mcp"); action.Status != StatusConflict {
		t.Fatalf("custom entry accepted: %#v", action)
	}
	if got := readFile(t, path); got != original {
		t.Fatalf("custom entry overwritten: %q", got)
	}
}

func TestRedTeamTOMLMultilineComment(t *testing.T) {
	fx := newFixture(t)
	t.Setenv("HOME", fx.home)
	options := fx.options()
	codex := harnessIndex(fx.registry())["codex"]
	original := "[mcp_servers.rmem]\ncommand = '" + options.ResolvedBinary() + "'\nargs = [\n  'mcp', # keep inline comment\n]\n[other]\nx = 1\n"
	if err := os.WriteFile(codex.MCPPath, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	report := newReport("uninstall", options)
	uninstallMCPToml(codex, options, codex.MCPPath, report)
	if action := firstAction(t, report, "codex", "mcp"); action.Status != StatusRemoved {
		t.Fatalf("status = %#v", action)
	}
	if got := readFile(t, codex.MCPPath); !strings.Contains(got, "# keep inline comment") || !strings.Contains(got, "[other]\nx = 1") {
		t.Fatalf("comments or unrelated fields lost: %q", got)
	}
}

func TestRedTeamSharedHookNames(t *testing.T) {
	fx := newFixture(t)
	t.Setenv("HOME", fx.home)
	first := fx.options()
	first.Hooks = true
	first.Name = "first"
	second := first
	second.Name = "second"
	zcode := harnessIndex(fx.registry())["zcode"]
	assertNoFailures(t, Install([]Harness{zcode}, first))
	assertNoFailures(t, Install([]Harness{zcode}, second))
	assertNoFailures(t, Uninstall([]Harness{zcode}, first))
	data := readJSONObject(t, *zcode.HooksPath)
	groups, ok := objectAt(t, data, "hooks", "events")["UserPromptSubmit"].([]any)
	if !ok || len(groups) != 1 || groupCommand(t, groups[0]) != hookCommands(second)[0].Command {
		t.Fatalf("remaining instance lost shared hook: %#v", groups)
	}
	assertNoFailures(t, Uninstall([]Harness{zcode}, second))
	data = readJSONObject(t, *zcode.HooksPath)
	if _, exists := objectAt(t, data, "hooks", "events")["UserPromptSubmit"]; exists {
		t.Fatal("last instance did not remove shared hook")
	}
}

func TestRedTeamReinstallPreservesSettings(t *testing.T) {
	for _, kind := range []string{"json", "toml"} {
		t.Run(kind, func(t *testing.T) {
			fx := newFixture(t)
			t.Setenv("HOME", fx.home)
			options := fx.options()
			options.DB = "/old/vault.db"
			options.Scope = "old-scope"
			index := harnessIndex(fx.registry())
			harness := index["codex"]
			if kind == "json" {
				harness = index["zcode"]
			}
			report := newReport("install", options)
			installMCP(harness, options, report)
			assertNoFailures(t, report)
			if kind == "toml" {
				original := readFile(t, harness.MCPPath)
				original = strings.Replace(original, "startup_timeout_sec = 30", "startup_timeout_sec = 123", 1)
				if err := os.WriteFile(harness.MCPPath, []byte(original), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			options.DB, options.Scope = "", ""
			report = newReport("install", options)
			installMCP(harness, options, report)
			assertNoFailures(t, report)
			text := readFile(t, harness.MCPPath)
			if !strings.Contains(text, "/old/vault.db") || !strings.Contains(text, "old-scope") || (kind == "toml" && !strings.Contains(text, "startup_timeout_sec = 123")) {
				t.Fatalf("reinstall discarded settings: %q", text)
			}
			options.DB = "/new/vault.db"
			report = newReport("install", options)
			installMCP(harness, options, report)
			assertNoFailures(t, report)
			text = readFile(t, harness.MCPPath)
			if !strings.Contains(text, "/new/vault.db") || !strings.Contains(text, "old-scope") || (kind == "toml" && !strings.Contains(text, "startup_timeout_sec = 123")) {
				t.Fatalf("explicit update lost settings: %q", text)
			}
		})
	}
}

func TestRedTeamPluginDeclarationUnverified(t *testing.T) {
	fx := newFixture(t)
	t.Setenv("HOME", fx.home)
	options := fx.options()
	options.Name = DefaultName
	options.SkillName = DefaultName
	zcode := harnessIndex(fx.registry())["zcode"]
	pluginDir := filepath.Join(fx.home, ".zcode", "cli", "plugins", "cache", "market", DefaultName, "1.0")
	manifestPath := filepath.Join(pluginDir, ".zcode-plugin", "plugin.json")
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"mcpServers":{"rolling-memory":{"command":"/not/installed","args":["mcp"]}}}`
	if err := os.WriteFile(manifestPath, []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(zcode.MCPPath), 0o755); err != nil {
		t.Fatal(err)
	}
	config := `{"plugins":{"enabledPlugins":{"rolling-memory@market":true}}}`
	if err := os.WriteFile(zcode.MCPPath, []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	report := newReport("install", options)
	installMCPJSON(zcode, options, zcode.MCPPath, report)
	if action := firstAction(t, report, "zcode", "mcp"); action.Status != StatusConflict {
		t.Fatalf("unverified plugin accepted: %#v", action)
	}
	if got := readFile(t, zcode.MCPPath); got != config {
		t.Fatalf("plugin config changed: %q", got)
	}
}

func TestRedTeamFailedMCPUninstallRetainsHooks(t *testing.T) {
	for _, dryRun := range []bool{false, true} {
		t.Run(strconv.FormatBool(dryRun), func(t *testing.T) {
			fx := newFixture(t)
			t.Setenv("HOME", fx.home)
			options := fx.options()
			options.Project = true
			options.Hooks = true
			claude := harnessIndex(fx.registry())["claude-code"]
			assertNoFailures(t, Install([]Harness{claude}, options))
			mcpPath, _ := mcpTargetPath(claude, options)
			data := readJSONObject(t, mcpPath)
			objectAt(t, data, "mcpServers", options.Name)["disabled"] = true
			encoded, err := json.Marshal(data)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(mcpPath, encoded, 0o644); err != nil {
				t.Fatal(err)
			}
			hooksPath, _ := hooksTargetPath(claude, options)
			before := readFile(t, hooksPath)
			options.DryRun = dryRun
			report := Uninstall([]Harness{claude}, options)
			if firstAction(t, report, "claude-code", "mcp").Status != StatusConflict {
				t.Fatalf("modified MCP entry was not protected: %#v", report.Actions)
			}
			if got := readFile(t, hooksPath); got != before {
				t.Fatalf("dependent hook changed: %q", got)
			}
		})
	}
}

func TestRedTeamUninstallDryRunSharedHook(t *testing.T) {
	fx := newFixture(t)
	t.Setenv("HOME", fx.home)
	options := fx.options()
	options.Hooks = true
	zcode := harnessIndex(fx.registry())["zcode"]
	assertNoFailures(t, Install([]Harness{zcode}, options))
	options.DryRun = true
	report := Uninstall([]Harness{zcode}, options)
	if action := firstAction(t, report, "zcode", "hooks"); action.Status != StatusPlanned {
		t.Fatalf("last user's hook not planned for removal: %#v", action)
	}
}

func TestRedTeamShimSymlinks(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(strconv.FormatBool(existing), func(t *testing.T) {
			fx := newFixture(t)
			t.Setenv("HOME", fx.home)
			options := fx.options()
			path := DefaultShimPath(fx.home)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(fx.home, "outside")
			if existing {
				if err := os.WriteFile(target, []byte("untouched"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink(target, path); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			report := newReport("install", options)
			ensureBinary(options, report)
			if action := firstAction(t, report, "*", "binary"); action.Status != StatusConflict {
				t.Fatalf("symlink accepted: %#v", action)
			}
			if existing && readFile(t, target) != "untouched" || !existing && fileExists(target) {
				t.Fatal("symlink target changed")
			}
		})
	}
}

func TestRedTeamProjectLanguagePreference(t *testing.T) {
	for _, tc := range []struct {
		name    string
		project bool
		lang    string
		writes  bool
	}{
		{"user-explicit", false, "zh-CN", true},
		{"user-default", false, "", false},
		{"project-explicit", true, "zh-CN", false},
		{"project-default", true, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fx := newFixture(t)
			t.Setenv("HOME", fx.home)
			options := fx.options()
			options.Project, options.Lang = tc.project, tc.lang
			assertNoFailures(t, Install([]Harness{harnessIndex(fx.registry())["zcode"]}, options))
			if got := fileExists(UILangStatePath(fx.home)); got != tc.writes {
				t.Fatalf("language preference exists = %v, want %v", got, tc.writes)
			}
		})
	}
}

func TestRedTeamProjectUninstallShortcut(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("desktop shortcut probe uses temporary HOME")
	}
	fx := newFixture(t)
	t.Setenv("HOME", fx.home)
	options := fx.options()
	spec := desktopShortcutSpec(runtime.GOOS, fx.home, options.ResolvedBinary())
	if err := os.MkdirAll(filepath.Dir(spec.Path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(spec.Path, []byte(spec.Content), 0o755); err != nil {
		t.Fatal(err)
	}
	options.Project = true
	Uninstall([]Harness{harnessIndex(fx.registry())["zcode"]}, options)
	if got := readFile(t, spec.Path); got != spec.Content {
		t.Fatalf("project uninstall changed user shortcut: %q", got)
	}
}

func TestRedTeamClaudeDisableAllHooks(t *testing.T) {
	for _, tc := range []struct {
		name     string
		value    string
		conflict bool
	}{
		{"disabled", `true`, true},
		{"enabled", `false`, false},
		{"invalid", `"yes"`, true},
		{"missing", ``, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fx := newFixture(t)
			t.Setenv("HOME", fx.home)
			options := fx.options()
			claude := harnessIndex(fx.registry())["claude-code"]
			path := *claude.HooksPath
			field := ""
			if tc.value != "" {
				field = `"disableAllHooks":` + tc.value + `,`
			}
			original := "{" + field + `"hooks":{}}`
			if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
				t.Fatal(err)
			}
			report := newReport("install", options)
			installHooks(claude, options, report)
			action := firstAction(t, report, "claude-code", "hooks")
			if tc.conflict {
				if action.Status != StatusConflict || readFile(t, path) != original {
					t.Fatalf("disabled setting changed or accepted: %#v", action)
				}
			} else if action.Status != StatusInstalled {
				t.Fatalf("enabled setting rejected: %#v", action)
			}
			check := newReport("check", options)
			checkHooks(claude, options, check)
			if tc.conflict && firstAction(t, check, "claude-code", "hooks").Status != StatusConflict {
				t.Fatalf("check accepted disabled setting: %#v", check.Actions)
			}
		})
	}
}

func TestRedTeamClaudeProjectDisableAllHooks(t *testing.T) {
	fx := newFixture(t)
	t.Setenv("HOME", fx.home)
	options := fx.options()
	options.Project = true
	claude := harnessIndex(fx.registry())["claude-code"]
	user := `{"disableAllHooks":true}`
	if err := os.WriteFile(*claude.HooksPath, []byte(user), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"install", "check"} {
		report := newReport(mode, options)
		if mode == "install" {
			installHooks(claude, options, report)
		} else {
			checkHooks(claude, options, report)
		}
		if action := firstAction(t, report, "claude-code", "hooks"); action.Status != StatusConflict {
			t.Fatalf("%s ignored disabled user hooks: %#v", mode, action)
		}
		if got := readFile(t, *claude.HooksPath); got != user {
			t.Fatalf("%s changed user settings: %q", mode, got)
		}
	}
}

func TestRedTeamHookDuplicatesAndSwitch(t *testing.T) {
	for _, tc := range []struct {
		name     string
		enabled  string
		groups   string
		conflict bool
	}{
		{"disabled", `false`, `[{"hooks":[%s]}]`, true},
		{"duplicate-groups", `true`, `[{"hooks":[%s]},{"hooks":[%s]}]`, true},
		{"valid-and-disabled", `true`, `[{"hooks":[%s,{"type":"command","enabled":false,"command":COMMAND}]}]`, true},
		{"duplicate-mixed-group", `true`, `[{"hooks":[{"type":"command","command":"guard.sh"},%s,%s]}]`, true},
		{"single-mixed-group", `true`, `[{"hooks":[{"type":"command","command":"guard.sh"},%s]}]`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fx := newFixture(t)
			t.Setenv("HOME", fx.home)
			options := fx.options()
			zcode := harnessIndex(fx.registry())["zcode"]
			path := *zcode.HooksPath
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			hook := `{"type":"command","command":` + strconv.Quote(hookCommands(options)[0].Command) + `}`
			groups := strings.ReplaceAll(tc.groups, "%s", hook)
			groups = strings.ReplaceAll(groups, "COMMAND", strconv.Quote(hookCommands(options)[0].Command))
			original := `{"hooks":{"enabled":` + tc.enabled + `,"events":{"UserPromptSubmit":` + groups + `}}}`
			if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
				t.Fatal(err)
			}
			for _, mode := range []string{"install", "check"} {
				report := newReport(mode, options)
				if mode == "install" {
					installHooks(zcode, options, report)
				} else {
					checkHooks(zcode, options, report)
				}
				action := firstAction(t, report, "zcode", "hooks")
				if tc.conflict && action.Status != StatusConflict {
					t.Fatalf("%s accepted unsafe hooks: %#v", mode, action)
				}
				if !tc.conflict && action.Status != StatusUnchanged && action.Status != StatusOK {
					t.Fatalf("%s rejected single mixed hook: %#v", mode, action)
				}
				if got := readFile(t, path); got != original {
					t.Fatalf("%s changed hook config: %q", mode, got)
				}
			}
		})
	}
}

func TestRedTeamPluginHookSwitch(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(strconv.FormatBool(enabled), func(t *testing.T) {
			fx := newFixture(t)
			t.Setenv("HOME", fx.home)
			options := fx.options()
			options.Name = DefaultName
			options.SkillName = DefaultName
			zcode := harnessIndex(fx.registry())["zcode"]
			setupZCodePluginCache(t, fx, "0.7.4")
			path := *zcode.HooksPath
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			original := `{"plugins":{"enabledPlugins":{"rolling-memory@marketplace":true}},"hooks":{"enabled":` + strconv.FormatBool(enabled) + `,"events":{}}}`
			if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
				t.Fatal(err)
			}
			for _, mode := range []string{"install", "check"} {
				report := newReport(mode, options)
				if mode == "install" {
					installHooks(zcode, options, report)
				} else {
					checkHooks(zcode, options, report)
				}
				action := firstAction(t, report, "zcode", "hooks")
				if !enabled && action.Status != StatusConflict {
					t.Fatalf("%s accepted disabled plugin hooks: %#v", mode, action)
				}
				if enabled && action.Status != StatusUnchanged && action.Status != StatusOK {
					t.Fatalf("%s rejected enabled plugin declaration: %#v", mode, action)
				}
				if got := readFile(t, path); got != original {
					t.Fatalf("%s changed config: %q", mode, got)
				}
			}
			if enabled {
				command := strconv.Quote(hookCommands(options)[0].Command)
				mixed := `{"plugins":{"enabledPlugins":{"rolling-memory@marketplace":true}},"hooks":{"enabled":true,"events":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"guard.sh"},{"type":"command","command":` + command + `}]}]}}}`
				if err := os.WriteFile(path, []byte(mixed), 0o644); err != nil {
					t.Fatal(err)
				}
				for _, mode := range []string{"install", "check"} {
					report := newReport(mode, options)
					if mode == "install" {
						installHooks(zcode, options, report)
					} else {
						checkHooks(zcode, options, report)
					}
					if action := firstAction(t, report, "zcode", "hooks"); action.Status != StatusConflict {
						t.Fatalf("%s accepted plugin and mixed global hook: %#v", mode, action)
					}
					if got := readFile(t, path); got != mixed {
						t.Fatalf("%s changed mixed hook group: %q", mode, got)
					}
				}
			}
		})
	}
}

func TestRedTeamHookUnavailable(t *testing.T) {
	for _, tc := range []struct {
		name  string
		group string
	}{
		{"disabled", `{"hooks":[{"type":"command","enabled":false,"command":%s}]}`},
		{"wrong-type", `{"hooks":[{"type":"prompt","command":%s}]}`},
		{"matcher", `{"matcher":"only-some-events","hooks":[{"type":"command","command":%s}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fx := newFixture(t)
			t.Setenv("HOME", fx.home)
			options := fx.options()
			zcode := harnessIndex(fx.registry())["zcode"]
			path := *zcode.HooksPath
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			group := fmt.Sprintf(tc.group, strconv.Quote(hookCommands(options)[0].Command))
			original := `{"hooks":{"enabled":true,"events":{"UserPromptSubmit":[` + group + `]}}}`
			if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
				t.Fatal(err)
			}
			for _, mode := range []string{"install", "check"} {
				report := newReport(mode, options)
				if mode == "install" {
					installHooks(zcode, options, report)
				} else {
					checkHooks(zcode, options, report)
				}
				if action := firstAction(t, report, "zcode", "hooks"); action.Status != StatusConflict {
					t.Fatalf("%s accepted unavailable hook: %#v", mode, action)
				}
				if got := readFile(t, path); got != original {
					t.Fatalf("%s changed user hook: %q", mode, got)
				}
			}
		})
	}
}

func TestRedTeamDSHYAML(t *testing.T) {
	for _, tc := range []struct {
		name     string
		original string
		conflict bool
	}{
		{"empty-sequence", "# initialized profile\n[]\n", false},
		{"foreign-space", "- insert:\n    - id: other\n      config:\n        serverName : rolling-memory\n", true},
		{"unrelated", "- insert:\n    - id: other\n      config:\n        serverName: different\n        note: rolling-memory\n", false},
		{"tagged-js", "- insert:\n    - id: other\n      config:\n        serverName: different\n        expression: !!js process.env.VALUE\n", false},
		{"dynamic-server", "- insert:\n    - id: other\n      config:\n        serverName: !!js process.env.VALUE\n", true},
		{"alias-server", "- insert:\n    - id: other\n      config:\n        serverName: &name different\n        other: *name\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fx := newFixture(t)
			t.Setenv("HOME", fx.home)
			options := fx.options()
			options.Name = DefaultName
			dsh := harnessIndex(fx.registry())["dsh"]
			path, _ := mcpTargetPath(dsh, options)
			if err := os.WriteFile(path, []byte(tc.original), 0o644); err != nil {
				t.Fatal(err)
			}
			report := newReport("install", options)
			installMCPDsh(dsh, options, path, report)
			action := firstAction(t, report, "dsh", "mcp")
			got := readFile(t, path)
			if tc.conflict {
				if action.Status != StatusConflict || got != tc.original {
					t.Fatalf("foreign entry not protected: %#v %q", action, got)
				}
				return
			}
			if action.Status != StatusInstalled || !strings.Contains(got, "# >>> rolling-memory") {
				t.Fatalf("valid profile not installed: %#v %q", action, got)
			}
			if tc.name == "empty-sequence" && strings.Contains(got, "[]\n\n# >>>") {
				t.Fatalf("empty sequence followed by another document: %q", got)
			}
			if tc.name == "tagged-js" && !strings.Contains(got, "expression: !!js process.env.VALUE") {
				t.Fatalf("tagged expression changed: %q", got)
			}
		})
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
