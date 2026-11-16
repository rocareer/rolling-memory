// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package harness

// Install writes the skill, the MCP registration and, when requested, the hook
// events and the instruction block for every selected harness.
//
// The standard reference raises on an invalid server/skill name; Go reports it as
// an error action (the closed status set) so callers still get a report and the
// CLI can surface the message. Call ValidateInstallOptions first for the
// original stderr contract.
func Install(harnesses []Harness, options InstallOptions) *Report {
	report := newReport("install", options)
	if err := ValidateInstallOptions(report.Options); err != nil {
		report.Add("*", "binary", "-", StatusError, err.Error())
		return report
	}
	ensureBinary(report.Options, report)
	if report.Options.Lang != "" && !report.Options.MCPOnly {
		adminLang := AdminLangFor(report.Options.Lang)
		if !report.Options.DryRun {
			if written, err := WriteAdminDefaultLang(report.Options.Home, report.Options.Lang); err == nil && written != "" {
				adminLang = written
			}
		}
		if adminLang != "" {
			report.Extra["lang"] = report.Options.Lang
			report.Extra["default_lang"] = adminLang
		}
	}
	for _, harness := range harnesses {
		if !report.Options.MCPOnly {
			installSkill(harness, report.Options, report)
		}
		installMCP(harness, report.Options, report)
		if report.Options.Hooks && !report.Options.MCPOnly {
			installHooks(harness, report.Options, report)
		}
		if report.Options.Instructions && !report.Options.MCPOnly {
			installInstructions(harness, report.Options, report)
		}
	}
	if report.Options.DesktopShortcut && !report.Options.MCPOnly {
		installDesktopShortcut(report.Options, report)
	}
	return report
}

// Uninstall removes exactly what install wrote: matched entries, marker blocks
// and skill files whose frontmatter carries our name. Hooks and instruction
// blocks are removed whenever they are ours — not only when the caller repeats
// `--with-hooks` / `--with-instructions` — because a leftover hook re-runs
// `install --with-hooks` on the next prompt and would silently undo the
// uninstall; `--mcp-only` remains the narrowing knob for the MCP entry alone.
// The shim is left alone.
func Uninstall(harnesses []Harness, options InstallOptions) *Report {
	report := newReport("uninstall", options)
	if err := ValidateInstallOptions(report.Options); err != nil {
		report.Add("*", "binary", "-", StatusError, err.Error())
		return report
	}
	for _, harness := range harnesses {
		if !report.Options.MCPOnly {
			uninstallSkill(harness, report.Options, report)
		}
		uninstallMCP(harness, report.Options, report)
		if !report.Options.MCPOnly {
			uninstallHooks(harness, report.Options, report)
			uninstallInstructions(harness, report.Options, report)
		}
	}
	if !report.Options.MCPOnly {
		// Like the hook cleanup, a stale desktop launcher never outlives the
		// installation; ours is removed whether or not the flag was repeated.
		uninstallDesktopShortcut(report.Options, report)
	}
	return report
}

func newReport(mode string, options InstallOptions) *Report {
	options = options.normalized()
	return &Report{Mode: mode, Options: options, Extra: map[string]any{}}
}
