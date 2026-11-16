// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package harness

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Check verifies the static registration (skill, MCP entry, hooks, instruction
// block) and, when live, a real MCP handshake on a temporary database.
func Check(harnesses []Harness, options InstallOptions, live bool) *Report {
	report := newReport("check", options)
	if err := ValidateInstallOptions(report.Options); err != nil {
		report.Add("*", "binary", "-", StatusError, err.Error())
		return report
	}
	if live {
		server := Handshake(report.Options, 30*time.Second)
		report.Extra["server"] = server
		if server["ok"] != true {
			detail, _ := server["error"].(string)
			report.Add("*", "server", report.Options.ResolvedBinary(), StatusError, detail)
		}
	}
	for _, harness := range harnesses {
		if !report.Options.MCPOnly {
			checkSkill(harness, report.Options, report)
		}
		checkMCP(harness, report.Options, report)
		if !report.Options.MCPOnly {
			checkHooks(harness, report.Options, report)
			checkInstructions(harness, report.Options, report)
		}
	}
	return report
}

// SelectHarnesses resolves explicit ids (unknown ids are an error) or filters
// the registry by detection.
func SelectHarnesses(registry []Harness, names []string, detectedOnly bool) ([]Harness, error) {
	if len(names) > 0 {
		index := harnessIndex(registry)
		var extIndex map[string]Harness
		if len(registry) > 0 {
			home := filepath.Dir(registry[0].Detect)
			extIndex = harnessIndex(BuildExtendedHarnesses(home))
		}
		for _, name := range names {
			if _, ok := index[name]; ok {
				continue
			}
			if extIndex != nil {
				if _, ok := extIndex[name]; ok {
					continue
				}
			}
			known := make([]string, 0, len(registry))
			for _, harness := range registry {
				known = append(known, harness.ID)
			}
			return nil, fmt.Errorf("unknown harness %s; known: %s", pyRepr(name), strings.Join(known, ", "))
		}
		selected := make([]Harness, 0, len(names))
		for _, name := range names {
			if h, ok := index[name]; ok {
				selected = append(selected, h)
			} else if extIndex != nil {
				if h, ok := extIndex[name]; ok {
					selected = append(selected, h)
				}
			}
		}
		return selected, nil
	}
	if detectedOnly {
		selected := make([]Harness, 0, len(registry))
		for _, harness := range registry {
			if fileExists(harness.Detect) {
				selected = append(selected, harness)
			}
		}
		return selected, nil
	}
	return append([]Harness(nil), registry...), nil
}

// InstalledHarnesses returns the harnesses where an install of options.Name is
// visible (skill file present, or the MCP config mentions the name).
func InstalledHarnesses(harnesses []Harness, options InstallOptions) []Harness {
	options = options.normalized()
	result := make([]Harness, 0, len(harnesses))
	for _, harness := range harnesses {
		found := false
		if !options.MCPOnly {
			for _, target := range skillTargets(harness, options) {
				if fileExists(target) {
					found = true
					break
				}
			}
		}
		if !found {
			for _, path := range mcpTargetPaths(harness, options) {
				if !fileExists(path) {
					continue
				}
				text := ""
				if raw, err := os.ReadFile(path); err == nil {
					text = strings.ToValidUTF8(string(raw), "\uFFFD")
				}
				if strings.Contains(text, options.Name) {
					found = true
					break
				}
			}
		}
		if !found && !options.MCPOnly {
			_, found = activeNativePlugin(harness, options)
		}
		if found {
			result = append(result, harness)
		}
	}
	return result
}

// DescribeHarnesses returns the --list rows.
func DescribeHarnesses(harnesses []Harness, options InstallOptions) []map[string]any {
	options = options.normalized()
	rows := make([]map[string]any, 0, len(harnesses))
	for _, harness := range harnesses {
		skill := "-"
		if options.Project && harness.SkillProject != "" {
			skill = filepath.Join(options.Cwd, filepath.FromSlash(harness.SkillProject))
		} else if harness.SkillUser != nil {
			skill = *harness.SkillUser
		}
		mcp := "-"
		if path, ok := mcpTargetPath(harness, options); ok {
			mcp = path
		}
		hooks := "-"
		if harness.HooksKind != "" && harness.HooksPath != nil {
			hooks = *harness.HooksPath
		}
		instructions := "-"
		if options.Project && harness.InstructionsProject != "" {
			instructions = filepath.Join(options.Cwd, filepath.FromSlash(harness.InstructionsProject))
		} else if harness.InstructionsUser != nil {
			instructions = *harness.InstructionsUser
		}
		rows = append(rows, map[string]any{
			"id":           harness.ID,
			"label":        harness.Label,
			"detected":     fileExists(harness.Detect),
			"detect":       harness.Detect,
			"skill":        skill,
			"mcp":          mcp,
			"hooks":        hooks,
			"instructions": instructions,
			"notes":        harness.Notes,
		})
	}
	return rows
}
