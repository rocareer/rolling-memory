// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/rocareer/rolling-memory/internal/harness"
)

// runInstall handles the `install` subcommand. The registry lives in internal/harness;
// the CLI owns option resolution and rendering.
func (a *application) runInstall() int {
	home, _ := os.UserHomeDir()
	cwd, _ := os.Getwd()
	options := harness.WithProjectDefaults(harness.InstallOptions{
		Home:         home,
		Cwd:          cwd,
		Name:         a.args.String("name"),
		SkillName:    a.args.String("skill_name"),
		Binary:       a.args.String("binary"),
		SkillFile:    skillFileOption(a.args.StringPtr("skill_file")),
		Lang:         a.args.String("lang"),
		Scope:        a.args.String("scope"),
		DB:           a.args.String("db"),
		Project:      a.args.Flag("project"),
		Hooks:        a.args.Flag("with_hooks"),
		Instructions: a.args.Flag("with_instructions"),
		MCPOnly:      a.args.Flag("mcp_only"),
		Force:        a.args.Flag("force"),
		DryRun:       a.args.Flag("dry_run"),
		// A desktop launcher for the web console (--desktop-shortcut).
		DesktopShortcut: a.args.Flag("desktop_shortcut"),
		DSHProfile:      a.args.String("profile"),
	})
	if options.SkillName == "" {
		options.SkillName = options.Name
	}
	registry := append(harness.BuildHarnesses(home, ""), harness.BuildExtendedHarnesses(home)...)
	if a.args.Flag("list") {
		rows := harness.DescribeHarnesses(registry, options)
		if a.args.Flag("json") {
			if err := a.printJSON(rows); err != nil {
				return a.fail(err)
			}
			return 0
		}
		a.printHarnessTable(rows)
		return 0
	}
	names := a.args.List("harnesses")
	targets, err := harness.SelectHarnesses(registry, names, true)
	if err != nil {
		return a.fail(err)
	}
	if a.args.Flag("check") && len(names) == 0 {
		targets = harness.InstalledHarnesses(targets, options)
	}
	if len(targets) == 0 {
		message := "no harness detected (or none matching); "
		if a.args.Flag("check") {
			message = fmt.Sprintf("no installation of '%s' found for --check; ", options.Name)
		}
		a.errorf("%srun `rolling-memory install --list` for ids\n", message)
		return 1
	}
	if err := harness.ValidateInstallOptions(options); err != nil {
		return a.fail(err)
	}
	var report *harness.Report
	switch {
	case a.args.Flag("check"):
		report = harness.Check(targets, options, true)
	case a.args.Flag("uninstall"):
		report = harness.Uninstall(targets, options)
	default:
		report = harness.Install(targets, options)
	}
	if a.args.Flag("json") {
		if err := a.printJSON(report.ToDict()); err != nil {
			return a.fail(err)
		}
	} else {
		a.printInstallReport(report)
	}
	if len(report.Failures()) > 0 {
		return 1
	}
	return 0
}

func skillFileOption(value *string) *string {
	if value == nil || *value == "" {
		return nil
	}
	expanded := expandUser(*value)
	return &expanded
}

func (a *application) printHarnessTable(rows []map[string]any) {
	a.printf("%-12s %-9s %-44s %s\n", "harness", "detected", "mcp registration", "skill root")
	for _, row := range rows {
		detected := "no"
		if truthy(row["detected"]) {
			detected = "yes"
		}
		a.printf("%-12s %-9s %-44s %s\n",
			pyStr(row["id"]), detected, pyStr(row["mcp"]), pyStr(row["skill"]))
		if notes := pyStr(row["notes"]); notes != "" {
			a.printf("%-12s %s\n", "", notes)
		}
	}
}

func (a *application) printInstallReport(report *harness.Report) {
	mode := report.Mode
	if report.Options.DryRun {
		mode += " (dry-run)"
	}
	a.printf("%s · name=%s · binary=%s\n", mode, report.Options.Name, report.Options.ResolvedBinary())
	if adminLang, ok := report.Extra["default_lang"].(string); ok && adminLang != "" {
		a.printf("  language     ok · skill=%s · admin_default=%s\n", report.Options.Lang, adminLang)
	}
	if server, ok := report.Extra["server"].(map[string]any); ok {
		if truthy(server["ok"]) {
			info := childMap(server, "server")
			tools := strings.Join(stringList(server["tools"]), ", ")
			a.printf("  server       ok · %s %s · tools: %s\n",
				pyStr(info["name"]), pyStr(info["version"]), tools)
		} else {
			a.printf("  server       FAIL · %s\n", pyStr(server["error"]))
		}
	}
	order := make([]string, 0, len(report.Actions))
	counts := map[string]int{}
	for _, action := range report.Actions {
		detail := ""
		if action.Detail != "" {
			detail = " · " + action.Detail
		}
		label := "[" + action.Harness + "] " + action.Component
		a.printf("  %-22s %-10s %s%s\n", label, action.Status, action.Path, detail)
		if _, seen := counts[action.Status]; !seen {
			order = append(order, action.Status)
		}
		counts[action.Status]++
	}
	parts := make([]string, 0, len(order))
	for _, status := range order {
		parts = append(parts, fmt.Sprintf("%s %d", status, counts[status]))
	}
	if len(parts) > 0 {
		a.printf("  %s\n", strings.Join(parts, " · "))
	}
	for _, action := range report.Failures() {
		fmt.Fprintf(a.stderr, "  ! %s: %s %s — %s\n",
			action.Harness, action.Component, action.Status, action.Detail)
	}
}
