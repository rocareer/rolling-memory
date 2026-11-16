// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package harness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallMCPOnlyWritesOnlyMCP(t *testing.T) {
	fx := newFixture(t)
	options := fx.optionsWith(func(options *InstallOptions) {
		options.MCPOnly = true
		options.Hooks = true
		options.Instructions = true
	})
	report := Install(fx.registry(), options)
	assertNoFailures(t, report)

	skill := filepath.Join(fx.home, ".zcode", "skills", "rmem")
	if _, err := os.Stat(skill); !os.IsNotExist(err) {
		t.Fatalf("--mcp-only must not create a skill dir: %s", skill)
	}
	instructions := filepath.Join(fx.home, ".zcode", "AGENTS.md")
	if fileExists(instructions) {
		t.Fatalf("--mcp-only must not write instructions: %s", instructions)
	}

	zcode := readFile(t, fx.zcodeConfig())
	if !strings.Contains(zcode, "rmem") {
		t.Fatalf("--mcp-only did not register the MCP server:\n%s", zcode)
	}
	if strings.Contains(zcode, `"hooks"`) {
		t.Fatalf("--mcp-only must not write hooks:\n%s", zcode)
	}

	installed := InstalledHarnesses(fx.registry(), options)
	if len(installed) == 0 {
		t.Fatalf("--mcp-only installed harness not visible")
	}
	check := Check(fx.registry(), options, false)
	assertNoFailures(t, check)

	uninstall := Uninstall(fx.registry(), options)
	assertNoFailures(t, uninstall)
	if after := readFile(t, fx.zcodeConfig()); strings.Contains(after, "rmem") {
		t.Fatalf("--mcp-only uninstall left the MCP entry:\n%s", after)
	}
}
