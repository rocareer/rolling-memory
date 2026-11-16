// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package harness

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/rocareer/rolling-memory/internal/ids"
)

func setupZCodePluginCache(t *testing.T, fx *fixture, version string) string {
	t.Helper()
	pluginDir := filepath.Join(fx.home, ".zcode", "cli", "plugins", "cache", "marketplace", DefaultName, version)
	if _, err := writeText(filepath.Join(pluginDir, ".zcode-plugin", "plugin.json"), `{
  "name": "rolling-memory",
  "version": "`+version+`",
  "skills": "./skills",
  "hooks": "./hooks/hooks.json",
  "mcpServers": {
    "rolling-memory": {
      "type": "stdio",
      "command": "/bin/bash",
      "args": ["${CLAUDE_PLUGIN_ROOT}/bin/rolling-memory-mcp"]
    }
  }
}`); err != nil {
		t.Fatalf("writeText plugin.json: %v", err)
	}
	if _, err := writeText(filepath.Join(pluginDir, "skills", DefaultName, "SKILL.md"), "---\nname: rolling-memory\ndescription: plugin skill\n---\n"); err != nil {
		t.Fatalf("writeText SKILL.md: %v", err)
	}
	if _, err := writeText(filepath.Join(pluginDir, "hooks", "hooks.json"), `{"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"${CLAUDE_PLUGIN_ROOT}/bin/rolling-memory-hook recall"}]}]}}`); err != nil {
		t.Fatalf("writeText hooks.json: %v", err)
	}
	if _, err := writeText(filepath.Join(pluginDir, "bin", "rolling-memory-hook"), "#!/bin/sh\n"); err != nil {
		t.Fatal(err)
	}
	return pluginDir
}

func TestPluginHooksRequireCommand(t *testing.T) {
	tests := []struct {
		name string
		data string
		want bool
	}{
		{"empty", `{"hooks":{}}`, false},
		{"empty event", `{"hooks":{"UserPromptSubmit":[]}}`, false},
		{"unrelated command", `{"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"other"}]}]}}`, false},
		{"unrelated suffix", `{"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"${CLAUDE_PLUGIN_ROOT}/bin/rolling-memory-hook recall-extra"}]}]}}`, false},
		{"valid", `{"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"${CLAUDE_PLUGIN_ROOT}/bin/rolling-memory-hook recall"}]}]}}`, true},
		{"valid json envelope", `{"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"${CLAUDE_PLUGIN_ROOT}/bin/rolling-memory-hook recall --json"}]}]}}`, true},
		{"json suffix on unrelated sub", `{"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"${CLAUDE_PLUGIN_ROOT}/bin/rolling-memory-hook start --json"}]}]}}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "hooks.json")
			if _, err := writeText(path, tt.data); err != nil {
				t.Fatal(err)
			}
			if _, err := writeText(filepath.Join(filepath.Dir(path), "bin", "rolling-memory-hook"), "#!/bin/sh\n"); err != nil {
				t.Fatal(err)
			}
			if got := pluginHasHooks(path, filepath.Dir(path)); got != tt.want {
				t.Fatalf("pluginHasHooks() = %v, want %v", got, tt.want)
			}
			if tt.want {
				if err := os.Remove(filepath.Join(filepath.Dir(path), "bin", "rolling-memory-hook")); err != nil {
					t.Fatal(err)
				}
				if pluginHasHooks(path, filepath.Dir(path)) {
					t.Fatal("missing launcher must not be considered a usable hook")
				}
			}
		})
	}
}

func TestZCodePluginDeduplicatesGlobalInstallAndCheckRecognizesSinglePath(t *testing.T) {
	fx := newFixture(t)
	options := fx.options()
	options.Name = DefaultName
	options.SkillName = DefaultName
	options.Hooks = true

	// 1. First install via CLI (no plugin enabled yet) -> writes global skill, MCP and hooks.
	zcodeOnly, err := SelectHarnesses(fx.registry(), []string{"zcode"}, false)
	if err != nil {
		t.Fatalf("SelectHarnesses: %v", err)
	}
	assertNoFailures(t, Install(zcodeOnly, options))

	// 2. Now enable the self-contained ZCode plugin while the global wiring is still on disk.
	setupZCodePluginCache(t, fx, "0.7.4")
	data, err := loadJSONFile(fx.zcodeConfig())
	if err != nil {
		t.Fatalf("loadJSONFile: %v", err)
	}
	data, _ = jsonTableWith(data, []string{"plugins", "enabledPlugins"}, true, func(current ids.OrderedMap) ids.OrderedMap {
		return objectSet(current, "rolling-memory@marketplace", true)
	})
	rawCfg, err := dumpJSON(data)
	if err != nil {
		t.Fatalf("dumpJSON: %v", err)
	}
	if _, err := writeText(fx.zcodeConfig(), rawCfg); err != nil {
		t.Fatalf("writeText config.json: %v", err)
	}

	// 3. Check must detect the duplicate wiring across skill, mcp, and hooks.
	checkDup := Check(zcodeOnly, options, false)
	if len(checkDup.Failures()) != 3 {
		t.Fatalf("expected 3 duplicate conflicts (skill, mcp, hooks), got %d: %#v", len(checkDup.Failures()), checkDup.Failures())
	}
	for _, action := range checkDup.Failures() {
		if action.Status != StatusConflict || !strings.Contains(action.Detail, "duplicate") {
			t.Fatalf("unexpected conflict action: %#v", action)
		}
	}

	// 4. Running Install now automatically deduplicates global skill, MCP and hooks.
	dedupReport := Install(zcodeOnly, options)
	assertNoFailures(t, dedupReport)
	removedCount := 0
	for _, action := range dedupReport.Actions {
		if action.Harness == "zcode" && action.Status == StatusRemoved && strings.Contains(action.Detail, "deduped; provided by plugin rolling-memory@marketplace") {
			removedCount++
		}
	}
	if removedCount != 3 {
		t.Fatalf("expected 3 removed duplicate surfaces, got %d: %#v", removedCount, dedupReport.Actions)
	}

	// 5. Re-running Check now passes cleanly (0 failures) and attributes skill/mcp/hooks to the plugin.
	checkClean := Check(zcodeOnly, options, false)
	assertNoFailures(t, checkClean)
	for _, action := range checkClean.Actions {
		if action.Component == "instructions" {
			continue
		}
		if action.Status != StatusOK || !strings.Contains(action.Detail, "plugin rolling-memory@marketplace") {
			t.Fatalf("expected OK via plugin for %s, got %#v", action.Component, action)
		}
	}
}

func TestEmptyPluginHooksKeepGlobalHook(t *testing.T) {
	fx := newFixture(t)
	options := fx.options()
	options.Hooks = true
	zcodeOnly, err := SelectHarnesses(fx.registry(), []string{"zcode"}, false)
	if err != nil {
		t.Fatal(err)
	}
	assertNoFailures(t, Install(zcodeOnly, options))
	pluginDir := setupZCodePluginCache(t, fx, "0.7.4")
	if _, err := writeText(filepath.Join(pluginDir, "hooks", "hooks.json"), `{"hooks":{}}`); err != nil {
		t.Fatal(err)
	}
	data, err := loadJSONFile(fx.zcodeConfig())
	if err != nil {
		t.Fatal(err)
	}
	data, _ = jsonTableWith(data, []string{"plugins", "enabledPlugins"}, true, func(current ids.OrderedMap) ids.OrderedMap {
		return objectSet(current, "rolling-memory@marketplace", true)
	})
	config, err := dumpJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writeText(fx.zcodeConfig(), config); err != nil {
		t.Fatal(err)
	}
	assertNoFailures(t, Install(zcodeOnly, options))
	if !zcodeHasGlobalHooks(zcodeOnly[0], options) {
		t.Fatal("empty plugin hooks removed the existing global hook")
	}
	assertNoFailures(t, Check(zcodeOnly, options, false))
}

func TestClaudePluginDeduplicatesGlobalInstallAndCheck(t *testing.T) {
	fx := newFixture(t)
	options := fx.options()
	options.Name = DefaultName
	options.SkillName = DefaultName
	options.Hooks = true

	claudeOnly, err := SelectHarnesses(fx.registry(), []string{"claude-code"}, false)
	if err != nil {
		t.Fatalf("SelectHarnesses: %v", err)
	}
	assertNoFailures(t, Install(claudeOnly, options))

	pluginDir := filepath.Join(fx.home, ".claude", "plugins", "cache", "marketplace", DefaultName, "0.7.4")
	if _, err := writeText(filepath.Join(pluginDir, ".claude-plugin", "plugin.json"), `{
  "name": "rolling-memory",
  "version": "0.7.4",
  "skills": "./skills",
  "hooks": "./hooks/hooks.json",
  "mcpServers": {
    "rolling-memory": {
      "type": "stdio",
      "command": "/bin/bash",
      "args": ["${CLAUDE_PLUGIN_ROOT}/bin/rolling-memory-mcp"]
    }
  }
}`); err != nil {
		t.Fatalf("writeText plugin.json: %v", err)
	}
	if _, err := writeText(filepath.Join(pluginDir, "skills", DefaultName, "SKILL.md"), "---\nname: rolling-memory\ndescription: plugin skill\n---\n"); err != nil {
		t.Fatalf("writeText SKILL.md: %v", err)
	}
	if _, err := writeText(filepath.Join(pluginDir, "hooks", "hooks.json"), `{"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"${CLAUDE_PLUGIN_ROOT}/bin/rolling-memory-hook recall"}]}]}}`); err != nil {
		t.Fatalf("writeText hooks.json: %v", err)
	}

	if _, err := writeText(filepath.Join(pluginDir, "bin", "rolling-memory-hook"), "#!/bin/sh\n"); err != nil {
		t.Fatal(err)
	}

	settingsPath := filepath.Join(fx.home, ".claude", "settings.json")
	data, err := loadJSONFile(settingsPath)
	if err != nil {
		t.Fatalf("loadJSONFile: %v", err)
	}
	data, _ = jsonTableWith(data, []string{"enabledPlugins"}, true, func(current ids.OrderedMap) ids.OrderedMap {
		return objectSet(current, "rolling-memory@marketplace", true)
	})
	rawCfg, err := dumpJSON(data)
	if err != nil {
		t.Fatalf("dumpJSON: %v", err)
	}
	if _, err := writeText(settingsPath, rawCfg); err != nil {
		t.Fatalf("writeText settings.json: %v", err)
	}

	dedupReport := Install(claudeOnly, options)
	assertNoFailures(t, dedupReport)
	checkClean := Check(claudeOnly, options, false)
	assertNoFailures(t, checkClean)
}

func TestExtendedHarnessesCursorWindsurfCline(t *testing.T) {
	fx := newFixture(t)
	options := fx.options()
	ext, err := SelectHarnesses(fx.registry(), []string{"cursor", "windsurf", "cline"}, false)
	if err != nil {
		t.Fatalf("SelectHarnesses extended: %v", err)
	}
	if len(ext) != 3 {
		t.Fatalf("expected 3 extended harnesses, got %d", len(ext))
	}
	assertNoFailures(t, Install(ext, options))
	assertNoFailures(t, Check(ext, options, false))
	assertNoFailures(t, Uninstall(ext, options))
}

func TestExtendedHarnessesWorkBuddyAndDoubao(t *testing.T) {
	fx := newFixture(t)
	options := fx.options()
	ext, err := SelectHarnesses(fx.registry(), []string{"workbuddy", "doubao"}, false)
	if err != nil {
		t.Fatalf("SelectHarnesses extended: %v", err)
	}
	if len(ext) != 2 {
		t.Fatalf("expected 2 extended harnesses, got %d", len(ext))
	}
	report := Install(ext, options)
	assertNoFailures(t, report)

	// WorkBuddy: the host's user config is ~/.workbuddy/mcp.json (WorkBuddy's
	// own connector docs correct the earlier dotted reading); the dotted
	// ~/.workbuddy/.mcp.json is kept as the legacy carrier for host builds that
	// already read it. The runtime never expands shell variables, so the written
	// command must be an absolute, already-resolved path.
	wbConfig := filepath.Join(fx.home, ".workbuddy", "mcp.json")
	wbLegacyConfig := filepath.Join(fx.home, ".workbuddy", ".mcp.json")
	wbSkill := filepath.Join(fx.home, ".workbuddy", "skills", options.SkillName, "SKILL.md")
	if !fileExists(wbSkill) {
		t.Fatalf("workbuddy skill not written: %s", wbSkill)
	}
	for _, target := range []string{wbConfig, wbLegacyConfig} {
		data, err := loadJSONFile(target)
		if err != nil {
			t.Fatalf("load workbuddy mcp config %s: %v", target, err)
		}
		entry, found := jsonLookup(data, []string{"mcpServers", options.Name})
		if !found {
			t.Fatalf("workbuddy mcp entry missing in %s: %#v", target, data)
		}
		command := ""
		if object, ok := asObject(entry); ok {
			if value, ok := objectGet(object, "command"); ok {
				command, _ = value.(string)
			}
		}
		if !filepath.IsAbs(command) || strings.Contains(command, "${") || strings.Contains(command, "~") {
			t.Fatalf("workbuddy command %q is not an absolute resolved path", command)
		}
	}

	// Doubao: dual skill roots, and the MCP surface reports a truthful skip.
	doubaoSkills := []string{
		filepath.Join(fx.home, "Doubao", "skills", options.SkillName, "SKILL.md"),
		filepath.Join(fx.home, ".agents", "skills", options.SkillName, "SKILL.md"),
	}
	for _, target := range doubaoSkills {
		if !fileExists(target) {
			t.Fatalf("doubao skill root not written: %s", target)
		}
	}
	sawSkip := false
	for _, action := range report.Actions {
		if action.Harness == "doubao" && action.Component == "mcp" {
			if action.Status != StatusSkipped || !strings.Contains(action.Detail, "address-style connector") {
				t.Fatalf("doubao mcp action = %#v", action)
			}
			sawSkip = true
		}
	}
	if !sawSkip {
		t.Fatalf("doubao mcp action missing from install report: %#v", report.Actions)
	}

	assertNoFailures(t, Check(ext, options, false))
	assertNoFailures(t, Uninstall(ext, options))
	// Skill files are removed; the workbuddy config file stays on disk (it
	// belongs to the host) but must no longer carry our entry.
	for _, target := range append([]string{wbSkill}, doubaoSkills...) {
		if fileExists(target) {
			t.Fatalf("uninstall left %s", target)
		}
	}
	for _, target := range []string{wbConfig, wbLegacyConfig} {
		if remaining, err := loadJSONFile(target); err == nil {
			if _, found := jsonLookup(remaining, []string{"mcpServers", options.Name}); found {
				t.Fatalf("uninstall left the workbuddy mcp entry in %s: %#v", target, remaining)
			}
		}
	}
}

// TestWorkBuddyWritesHostReadPath pins the WorkBuddy write point to the user
// config path the host's own connector documentation names (mcp.json, no dot)
// and keeps the dotted legacy carrier alongside it.
func TestWorkBuddyWritesHostReadPath(t *testing.T) {
	fx := newFixture(t)
	options := fx.options()
	workbuddy, err := SelectHarnesses(fx.registry(), []string{"workbuddy"}, false)
	if err != nil {
		t.Fatalf("SelectHarnesses: %v", err)
	}
	if len(workbuddy) != 1 {
		t.Fatalf("expected 1 workbuddy harness, got %d", len(workbuddy))
	}
	harness := workbuddy[0]
	wantPrimary := filepath.Join(fx.home, ".workbuddy", "mcp.json")
	legacyPath := filepath.Join(fx.home, ".workbuddy", ".mcp.json")
	if harness.MCPPath != wantPrimary {
		t.Fatalf("workbuddy MCPPath = %q, want %q", harness.MCPPath, wantPrimary)
	}
	if got, ok := mcpTargetPath(harness, options); !ok || got != wantPrimary {
		t.Fatalf("mcpTargetPath = %q (ok=%v), want %q", got, ok, wantPrimary)
	}
	paths := mcpTargetPaths(harness, options)
	if len(paths) != 2 || paths[0] != wantPrimary || paths[1] != legacyPath {
		t.Fatalf("mcpTargetPaths = %#v", paths)
	}
	assertNoFailures(t, Install(workbuddy, options))
	for _, target := range paths {
		data, err := loadJSONFile(target)
		if err != nil {
			t.Fatalf("host read path not written %s: %v", target, err)
		}
		if _, found := jsonLookup(data, []string{"mcpServers", options.Name}); !found {
			t.Fatalf("no mcp entry in %s: %#v", target, data)
		}
	}
	assertNoFailures(t, Uninstall(workbuddy, options))
}

// TestWorkBuddyNeverClobbersHostEntries pins the second nail: install and
// uninstall merge into the WorkBuddy files and remove only entries recognised
// as ours, so whatever the host itself keeps there (e.g. its connector-proxy
// placeholder) is preserved.
func TestWorkBuddyNeverClobbersHostEntries(t *testing.T) {
	fx := newFixture(t)
	options := fx.options()
	workbuddy, err := SelectHarnesses(fx.registry(), []string{"workbuddy"}, false)
	if err != nil {
		t.Fatalf("SelectHarnesses: %v", err)
	}
	primaryPath := filepath.Join(fx.home, ".workbuddy", "mcp.json")
	legacyPath := filepath.Join(fx.home, ".workbuddy", ".mcp.json")
	hostEntries := map[string]string{
		primaryPath: `{"mcpServers":{"connector-proxy":{"type":"stdio","command":"/host/connector-proxy"}}}`,
		legacyPath:  `{"connector-proxy":{"type":"stdio","command":"/host/connector-proxy"}}`,
	}
	for path, text := range hostEntries {
		if _, err := writeText(path, text); err != nil {
			t.Fatalf("seed host entry %s: %v", path, err)
		}
	}
	assertNoFailures(t, Install(workbuddy, options))
	for path := range hostEntries {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if !strings.Contains(string(raw), "connector-proxy") {
			t.Fatalf("install clobbered the host entry in %s: %s", path, raw)
		} // The legacy file is the host's own: its root-level connector table must
		// survive the merge while our entry lands under mcpServers.
		if path == legacyPath {
			data, err := loadJSONFile(path)
			if err != nil {
				t.Fatalf("parse %s: %v", path, err)
			}
			hostEntry, found := jsonLookup(data, []string{"connector-proxy", "command"})
			if !found || pyString(hostEntry) != "/host/connector-proxy" {
				t.Fatalf("host connector row lost in %s: %#v", path, data)
			}
			if _, found := jsonLookup(data, []string{"mcpServers", options.Name}); !found {
				t.Fatalf("our entry missing from %s: %#v", path, data)
			}
		}
	}
	assertNoFailures(t, Uninstall(workbuddy, options))
	for path := range hostEntries {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read after uninstall %s: %v", path, err)
		}
		if !strings.Contains(string(raw), "connector-proxy") {
			t.Fatalf("uninstall clobbered the host entry in %s: %s", path, raw)
		}
	}
}

// TestWorkBuddyPluginDeduplicatesGlobalInstall mirrors the ZCode/Claude dedup
// contract for WorkBuddy: once the native plugin is enabled (settings.json +
// plugins/cache manifest + .mcp.json + skills), a CLI install must fold the
// user-scope skill and MCP entry into the plugin's single path.
func TestWorkBuddyPluginDeduplicatesGlobalInstall(t *testing.T) {
	fx := newFixture(t)
	options := fx.options()
	options.Name = DefaultName
	options.SkillName = DefaultName

	workbuddy, err := SelectHarnesses(fx.registry(), []string{"workbuddy"}, false)
	if err != nil {
		t.Fatalf("SelectHarnesses: %v", err)
	}
	assertNoFailures(t, Install(workbuddy, options))

	pluginDir := filepath.Join(fx.home, ".workbuddy", "plugins", "cache", "marketplace", DefaultName, "0.7.4")
	if _, err := writeText(filepath.Join(pluginDir, ".codebuddy-plugin", "plugin.json"), `{
  "name": "rolling-memory",
  "version": "0.7.4",
  "skills": ["./skills/rolling-memory"]
}`); err != nil {
		t.Fatalf("writeText plugin.json: %v", err)
	}
	if _, err := writeText(filepath.Join(pluginDir, "skills", DefaultName, "SKILL.md"), "---\nname: rolling-memory\ndescription: plugin skill\n---\n"); err != nil {
		t.Fatalf("writeText SKILL.md: %v", err)
	}
	if _, err := writeText(filepath.Join(pluginDir, ".mcp.json"), `{"mcpServers":{"rolling-memory":{"type":"stdio","command":"/bin/sh","args":["-c","true"]}}}`); err != nil {
		t.Fatalf("writeText .mcp.json: %v", err)
	}
	if _, err := writeText(filepath.Join(fx.home, ".workbuddy", "settings.json"), `{"enabledPlugins":{"rolling-memory@marketplace":true}}`); err != nil {
		t.Fatalf("writeText settings.json: %v", err)
	}

	dedupReport := Install(workbuddy, options)
	assertNoFailures(t, dedupReport)
	removed := map[string]bool{}
	for _, action := range dedupReport.Actions {
		if action.Harness == "workbuddy" && action.Status == StatusRemoved && strings.Contains(action.Detail, "deduped; provided by plugin rolling-memory@marketplace") {
			removed[action.Component] = true
		}
	}
	if !removed["skill"] || !removed["mcp"] {
		t.Fatalf("workbuddy dedup must fold skill and mcp into the plugin, got %#v", dedupReport.Actions)
	}
	assertNoFailures(t, Check(workbuddy, options, false))
}

// TestWorkBuddyPluginDedupFromMarketplaceDir covers the directory-marketplace
// shape: no plugins/cache copy exists, the plugin is enabled in settings.json
// and the bundle lives inside plugins/marketplaces/<name>/.
func TestWorkBuddyPluginDedupFromMarketplaceDir(t *testing.T) {
	fx := newFixture(t)
	options := fx.options()
	options.Name = DefaultName
	options.SkillName = DefaultName

	workbuddy, err := SelectHarnesses(fx.registry(), []string{"workbuddy"}, false)
	if err != nil {
		t.Fatalf("SelectHarnesses: %v", err)
	}
	assertNoFailures(t, Install(workbuddy, options))

	marketDir := filepath.Join(fx.home, ".workbuddy", "plugins", "marketplaces", "rolling-memory-plugins")
	if _, err := writeText(filepath.Join(marketDir, ".codebuddy-plugin", "marketplace.json"), `{
  "name": "rolling-memory-plugins",
  "plugins": [
    {"name": "rolling-memory", "source": "."}
  ]
}`); err != nil {
		t.Fatalf("writeText marketplace.json: %v", err)
	}
	if _, err := writeText(filepath.Join(marketDir, ".codebuddy-plugin", "plugin.json"), `{
  "name": "rolling-memory",
  "version": "0.7.4",
  "skills": ["./skills/rolling-memory"]
}`); err != nil {
		t.Fatalf("writeText plugin.json: %v", err)
	}
	if _, err := writeText(filepath.Join(marketDir, "skills", DefaultName, "SKILL.md"), "---\nname: rolling-memory\ndescription: plugin skill\n---\n"); err != nil {
		t.Fatalf("writeText SKILL.md: %v", err)
	}
	if _, err := writeText(filepath.Join(marketDir, ".mcp.json"), `{"mcpServers":{"rolling-memory":{"type":"stdio","command":"node","args":["-e","0"]}}}`); err != nil {
		t.Fatalf("writeText .mcp.json: %v", err)
	}
	if _, err := writeText(filepath.Join(fx.home, ".workbuddy", "settings.json"), `{"enabledPlugins":{"rolling-memory@rolling-memory-plugins":true}}`); err != nil {
		t.Fatalf("writeText settings.json: %v", err)
	}

	dedupReport := Install(workbuddy, options)
	assertNoFailures(t, dedupReport)
	removed := map[string]bool{}
	for _, action := range dedupReport.Actions {
		if action.Harness == "workbuddy" && action.Status == StatusRemoved && strings.Contains(action.Detail, "deduped; provided by plugin rolling-memory@rolling-memory-plugins") {
			removed[action.Component] = true
		}
	}
	if !removed["skill"] || !removed["mcp"] {
		t.Fatalf("marketplace-dir dedup must fold skill and mcp into the plugin, got %#v", dedupReport.Actions)
	}
}

// TestRepositoryPluginBundleHooksContract pins the shipped bundle: the repo's
// hooks/hooks.json must satisfy pluginHasHooks, so the native plugin provides
// the prompt hook on every host (and CLI-wired hooks are deduplicated).
func TestRepositoryPluginBundleHooksContract(t *testing.T) {
	repoRoot := filepath.Join("..", "..")
	hookPath := filepath.Join(repoRoot, "hooks", "hooks.json")
	if !fileExists(hookPath) {
		t.Fatalf("bundle hooks manifest missing: %s", hookPath)
	}
	if !fileExists(filepath.Join(repoRoot, "bin", "rolling-memory-hook")) {
		t.Fatal("bundle hook launcher missing: bin/rolling-memory-hook")
	}
	if !pluginHasHooks(hookPath, repoRoot) {
		t.Fatalf("pluginHasHooks must recognise the shipped hooks manifest at %s", hookPath)
	}
}

// TestRepositoryPluginBundleMCPContract pins the shipped MCP wiring: the bundle
// .mcp.json must not depend on host variable expansion (the CodeBuddy-family
// main process expands nothing), must run through the host-managed Node
// runtime, and its inline locator must hand off to the bundled JS launcher.
func TestRepositoryPluginBundleMCPContract(t *testing.T) {
	repoRoot := filepath.Join("..", "..")
	launcher := filepath.Join(repoRoot, "bin", "rolling-memory-mcp.js")
	if !fileExists(launcher) {
		t.Fatalf("bundle node launcher missing: %s", launcher)
	}
	data, err := loadJSONFile(filepath.Join(repoRoot, ".mcp.json"))
	if err != nil {
		t.Fatalf("load bundle .mcp.json: %v", err)
	}
	entry, found := jsonLookup(data, []string{"mcpServers", DefaultName})
	if !found {
		t.Fatalf("bundle .mcp.json misses the %s server entry: %#v", DefaultName, data)
	}
	object, ok := asObject(entry)
	if !ok {
		t.Fatalf("bundle server entry is not an object: %#v", entry)
	}
	command, _ := objectGet(object, "command")
	if command != "node" {
		t.Fatalf("bundle command = %#v, want node (host-managed runtime)", command)
	}
	if runtimeValue, ok := objectGet(object, "runtime"); !ok {
		t.Fatal("bundle entry must request the host-managed node runtime")
	} else if runtimeObject, isObject := asObject(runtimeValue); !isObject {
		t.Fatalf("bundle runtime field is not an object: %#v", runtimeValue)
	} else if runtimeType, _ := objectGet(runtimeObject, "type"); runtimeType != "node" {
		t.Fatalf("bundle runtime.type = %#v, want node", runtimeType)
	}
	argsValue, _ := objectGet(object, "args")
	args, ok := asArray(argsValue)
	if !ok || len(args) != 2 {
		t.Fatalf("bundle args = %#v, want exactly [-e, <inline locator>]", argsValue)
	}
	if first, _ := args[0].(string); first != "-e" {
		t.Fatalf("bundle args[0] = %#v, want -e", args[0])
	}
	script, _ := args[1].(string)
	if strings.Contains(script, "${") {
		t.Fatalf("bundle inline locator must not depend on host variable expansion: %s", script)
	}
	if !strings.Contains(script, "rolling-memory-mcp.js") {
		t.Fatalf("bundle inline locator must hand off to the bundled node launcher: %s", script)
	}
}

// TestNodeLauncherFollowsRedirectsAndVerifiesSHA256 exercises
// bin/rolling-memory-mcp.js against a local HTTP server that returns 302
// redirects (matching GitHub Releases -> objects.githubusercontent.com) for
// both SHA256SUMS and the platform binary asset.
func TestNodeLauncherFollowsRedirectsAndVerifiesSHA256(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	repoRoot := filepath.Join("..", "..")
	launcher, err := filepath.Abs(filepath.Join(repoRoot, "bin", "rolling-memory-mcp.js"))
	if err != nil {
		t.Fatalf("abs launcher: %v", err)
	}

	var dummyPayload []byte
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell payload test runs on unix platforms")
	} else {
		dummyPayload = []byte("#!/bin/sh\nprintf 'REDIRECT_BOOTSTRAP_OK:%s\\n' \"$1\"\nexit 0\n")
	}
	sum := sha256.Sum256(dummyPayload)
	sumHex := hex.EncodeToString(sum[:])

	goos := runtime.GOOS
	if goos != "darwin" && goos != "freebsd" {
		goos = "linux"
	}
	assetName := fmt.Sprintf("rolling-memory-%s-%s", goos, runtime.GOARCH)
	sumsContent := fmt.Sprintf("%s  %s\n", sumHex, assetName)

	mux := http.NewServeMux()
	mux.HandleFunc("/releases/latest/download/SHA256SUMS", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/releases/download/v0.0.1/SHA256SUMS", http.StatusFound)
	})
	mux.HandleFunc("/releases/download/v0.0.1/SHA256SUMS", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/cdn/SHA256SUMS", http.StatusTemporaryRedirect)
	})
	mux.HandleFunc("/cdn/SHA256SUMS", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sumsContent))
	})
	mux.HandleFunc("/releases/latest/download/"+assetName, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/cdn/"+assetName, http.StatusFound)
	})
	mux.HandleFunc("/cdn/"+assetName, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(dummyPayload)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	tmpHome := t.TempDir()
	cmd := exec.Command("node", launcher)
	cmd.Env = []string{
		"HOME=" + tmpHome,
		"XDG_DATA_HOME=" + filepath.Join(tmpHome, "xdg"),
		"PATH=/usr/bin:/bin",
		"ROLLING_MEMORY_RELEASE_BASE=" + srv.URL + "/releases/latest/download",
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("node launcher failed: %v\noutput:\n%s", err, string(out))
	}
	if !strings.Contains(string(out), "REDIRECT_BOOTSTRAP_OK:mcp") {
		t.Fatalf("expected REDIRECT_BOOTSTRAP_OK:mcp in output, got:\n%s", string(out))
	}
}

// TestInstallScriptNonDevEnvironmentContract verifies install.sh in an isolated
// environment with NO go and NO git on PATH:
//  1. Verified prebuilt install + automatic idempotent ~/.zshrc PATH persistence
//     + clean exit 0 when no harness is detected yet;
//  2. Re-running does not duplicate the PATH marker block;
//  3. Tampered binary SHA256 is rejected fail-closed and never written to disk;
//  4. Unreachable release channel with no git/go prints a clear end-user
//     diagnostic instead of a misleading toolchain error.
func TestInstallScriptNonDevEnvironmentContract(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("install.sh contract test runs on POSIX platforms")
	}
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	installSh := filepath.Join(repoRoot, "install.sh")

	// Build a minimal isolated PATH directory without `go` or `git`.
	sysBin := filepath.Join(t.TempDir(), "sysbin")
	if err := os.MkdirAll(sysBin, 0o755); err != nil {
		t.Fatal(err)
	}
	tools := []string{
		"bash", "sh", "uname", "tr", "mkdir", "chmod", "mv", "rm", "ln",
		"cat", "grep", "awk", "mktemp", "dirname", "basename", "curl", "shasum", "sha256sum",
	}
	for _, name := range tools {
		if resolved, lookErr := exec.LookPath(name); lookErr == nil {
			_ = os.Symlink(resolved, filepath.Join(sysBin, name))
		}
	}

	assetName := fmt.Sprintf("rolling-memory-%s-%s", runtime.GOOS, runtime.GOARCH)
	validBody := []byte("#!/bin/sh\nif [ \"$1\" = \"init\" ]; then exit 0; fi\nif [ \"$1\" = \"install\" ]; then echo \"no harness detected\" >&2; exit 1; fi\nexit 0\n")
	validSum := sha256.Sum256(validBody)
	validHex := hex.EncodeToString(validSum[:])

	releaseDir := filepath.Join(t.TempDir(), "release")
	if err := os.MkdirAll(releaseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(releaseDir, assetName), validBody, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(releaseDir, "SHA256SUMS"), []byte(fmt.Sprintf("%s  %s\n", validHex, assetName)), 0o644); err != nil {
		t.Fatal(err)
	}

	// Copy install.sh into an isolated directory (not inside the repo checkout)
	// so it behaves exactly like `curl ... | bash` on a user machine.
	standaloneInstallSh := filepath.Join(t.TempDir(), "install.sh")
	rawInstall, err := os.ReadFile(installSh)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(standaloneInstallSh, rawInstall, 0o755); err != nil {
		t.Fatal(err)
	}

	home1 := filepath.Join(t.TempDir(), "home1")
	if err := os.MkdirAll(home1, 0o755); err != nil {
		t.Fatal(err)
	}
	env1 := []string{
		"HOME=" + home1,
		"SHELL=/bin/zsh",
		"PATH=" + sysBin,
		"ROLLING_MEMORY_RELEASE_BASE=file://" + releaseDir,
	}

	// Phase 1 & 2: Valid install + idempotent PATH block in ~/.zshrc
	for run := 1; run <= 2; run++ {
		cmd := exec.Command(filepath.Join(sysBin, "bash"), standaloneInstallSh)
		cmd.Env = env1
		out, runErr := cmd.CombinedOutput()
		if runErr != nil {
			t.Fatalf("run %d failed: %v\n%s", run, runErr, string(out))
		}
	}
	zshrcBytes, err := os.ReadFile(filepath.Join(home1, ".zshrc"))
	if err != nil {
		t.Fatalf("expected ~/.zshrc to be created with PATH entry: %v", err)
	}
	if got := strings.Count(string(zshrcBytes), "# >>> rolling-memory PATH >>>"); got != 1 {
		t.Fatalf("expected exactly 1 PATH block in ~/.zshrc after 2 runs, got %d:\n%s", got, string(zshrcBytes))
	}

	// Phase 3: Tampered binary rejected fail-closed
	tamperedDir := filepath.Join(t.TempDir(), "tampered")
	if err := os.MkdirAll(tamperedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tamperedDir, assetName), append(validBody, []byte("# tampered\n")...), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tamperedDir, "SHA256SUMS"), []byte(fmt.Sprintf("%s  %s\n", validHex, assetName)), 0o644); err != nil {
		t.Fatal(err)
	}
	home3 := filepath.Join(t.TempDir(), "home3")
	if err := os.MkdirAll(home3, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd3 := exec.Command(filepath.Join(sysBin, "bash"), standaloneInstallSh)
	cmd3.Env = []string{
		"HOME=" + home3,
		"SHELL=/bin/zsh",
		"PATH=" + sysBin,
		"ROLLING_MEMORY_RELEASE_BASE=file://" + tamperedDir,
	}
	out3, err3 := cmd3.CombinedOutput()
	if err3 == nil {
		t.Fatalf("expected tampered binary to fail, but succeeded:\n%s", string(out3))
	}
	if !strings.Contains(string(out3), "SHA256 mismatch") {
		t.Fatalf("expected SHA256 mismatch error, got:\n%s", string(out3))
	}
	if fileExists(filepath.Join(home3, ".local", "share", "rolling-memory", "bin", "rolling-memory")) {
		t.Fatal("tampered binary must not be installed to disk")
	}

	// Phase 4: Unreachable channel + no Go/Git prints friendly non-dev guidance
	home4 := filepath.Join(t.TempDir(), "home4")
	if err := os.MkdirAll(home4, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd4 := exec.Command(filepath.Join(sysBin, "bash"), standaloneInstallSh)
	cmd4.Env = []string{
		"HOME=" + home4,
		"SHELL=/bin/zsh",
		"PATH=" + sysBin,
		"ROLLING_MEMORY_RELEASE_BASE=off",
		"ROLLING_MEMORY_RELEASE_MIRROR=off",
	}
	out4, err4 := cmd4.CombinedOutput()
	if err4 == nil {
		t.Fatalf("expected offline no-toolchain run to fail, got:\n%s", string(out4))
	}
	if !strings.Contains(string(out4), "普通用户（免装 Git/Go）") {
		t.Fatalf("expected end-user friendly guidance in output, got:\n%s", string(out4))
	}
}
