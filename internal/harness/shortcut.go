// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package harness

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// DesktopShortcutName is the label users see on their desktop.
const DesktopShortcutName = "Rolling-Memory Console"

// shortcutSpec describes one platform's desktop launcher. The spec is a pure
// value so every platform's shape is unit-testable from any host platform.
type shortcutSpec struct {
	// Path is the created file (on Windows the returned path is indicative:
	// PowerShell resolves the real, possibly redirected, Desktop folder).
	Path string
	// Content is the file body on darwin/linux.
	Content string
	// Executable mirrors the +x bit the POSIX desktops expect.
	Executable bool
	// Command is the creation command on Windows.
	Command []string
}

// desktopShortcutSpec builds the launcher for one platform. The shortcut runs
// the same entry hosts use (`<binary> panel`), so a double-click prepares and
// opens the local web console.
func desktopShortcutSpec(goos, home, binary string) shortcutSpec {
	switch goos {
	case "windows":
		script := fmt.Sprintf(
			"$desktop=[Environment]::GetFolderPath('Desktop');"+
				"$path=Join-Path $desktop '%s.lnk';"+
				"$shell=New-Object -ComObject WScript.Shell;"+
				"$link=$shell.CreateShortcut($path);"+
				"$link.TargetPath='%s';"+
				"$link.Arguments='panel';"+
				"$link.Description='Open the local Rolling-Memory web console';"+
				"$link.Save();Write-Output $path",
			psSingleQuote(DesktopShortcutName),
			psSingleQuote(binary),
		)
		return shortcutSpec{
			Path:    filepath.Join(home, "Desktop", DesktopShortcutName+".lnk"),
			Command: []string{"powershell", "-NoProfile", "-NonInteractive", "-Command", script},
		}
	case "darwin":
		content := fmt.Sprintf(
			"#!/bin/sh\n# %s — opens the local web console (same entry as `rolling-memory panel`).\nexec %s panel\n",
			DesktopShortcutName, shlexQuote(binary),
		)
		return shortcutSpec{
			Path:       filepath.Join(home, "Desktop", DesktopShortcutName+".command"),
			Content:    content,
			Executable: true,
		}
	default:
		content := fmt.Sprintf(
			"[Desktop Entry]\nType=Application\nName=%s\nComment=Open the local Rolling-Memory web console\nExec=%s panel\nTerminal=true\n",
			DesktopShortcutName, desktopExecQuote(binary),
		)
		return shortcutSpec{
			Path:       filepath.Join(home, "Desktop", "rolling-memory-console.desktop"),
			Content:    content,
			Executable: true,
		}
	}
}

// psSingleQuote escapes one PowerShell single-quoted literal.
func psSingleQuote(value string) string {
	return strings.ReplaceAll(value, "'", "''")
}

// desktopExecQuote quotes one Exec= argument per the desktop entry spec.
func desktopExecQuote(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "`", "\\`", `$`, `\$`)
	if !strings.ContainsAny(value, " \t\"'\\`$") {
		return value
	}
	return `"` + replacer.Replace(value) + `"`
}

// installDesktopShortcut writes the platform launcher when the caller asked for
// it (--desktop-shortcut). Existing launchers of ours are refreshed; foreign
// files are never touched.
func installDesktopShortcut(options InstallOptions, report *Report) {
	spec := desktopShortcutSpec(runtime.GOOS, options.Home, options.ResolvedBinary())
	if runtime.GOOS == "windows" {
		if options.DryRun {
			report.Add("*", "shortcut", spec.Path, StatusPlanned, "")
			return
		}
		output, err := exec.Command(spec.Command[0], spec.Command[1:]...).CombinedOutput()
		if err != nil {
			report.Add("*", "shortcut", spec.Path, StatusError, strings.TrimSpace(string(output))+" "+err.Error())
			return
		}
		path := strings.TrimSpace(string(output))
		if path == "" {
			path = spec.Path
		}
		report.Add("*", "shortcut", path, StatusInstalled, "desktop launcher created")
		return
	}
	current, readErr := os.ReadFile(spec.Path)
	if readErr == nil {
		if string(current) == spec.Content {
			report.Add("*", "shortcut", spec.Path, StatusUnchanged, "")
			return
		}
		if !strings.Contains(string(current), DesktopShortcutName) {
			report.Add("*", "shortcut", spec.Path, StatusConflict, "desktop entry is not ours; not touching it")
			return
		}
		if options.DryRun {
			report.Add("*", "shortcut", spec.Path, StatusPlanned, "")
			return
		}
		if err := writeDesktopShortcut(spec); err != nil {
			report.Add("*", "shortcut", spec.Path, StatusError, err.Error())
			return
		}
		report.Add("*", "shortcut", spec.Path, StatusUpdated, "")
		return
	}
	if readErr != nil && !os.IsNotExist(readErr) {
		report.Add("*", "shortcut", spec.Path, StatusError, "cannot read: "+readErr.Error())
		return
	}
	if options.DryRun {
		report.Add("*", "shortcut", spec.Path, StatusPlanned, "")
		return
	}
	if err := writeDesktopShortcut(spec); err != nil {
		report.Add("*", "shortcut", spec.Path, StatusError, err.Error())
		return
	}
	report.Add("*", "shortcut", spec.Path, StatusInstalled, "desktop launcher created")
}

func writeDesktopShortcut(spec shortcutSpec) error {
	if err := os.MkdirAll(filepath.Dir(spec.Path), 0o755); err != nil {
		return err
	}
	if _, err := writeText(spec.Path, spec.Content); err != nil {
		return err
	}
	if spec.Executable {
		if err := os.Chmod(spec.Path, 0o755); err != nil {
			return err
		}
	}
	return nil
}

// uninstallDesktopShortcut removes a desktop launcher this tool created; a
// foreign file at the same name is reported and left alone. It runs on every
// uninstall (like the hook cleanup) so a stale launcher never outlives the
// installation.
func uninstallDesktopShortcut(options InstallOptions, report *Report) {
	spec := desktopShortcutSpec(runtime.GOOS, options.Home, options.ResolvedBinary())
	if runtime.GOOS == "windows" {
		if options.DryRun {
			if _, err := os.Stat(spec.Path); err == nil {
				report.Add("*", "shortcut", spec.Path, StatusPlanned, "")
			} else {
				report.Add("*", "shortcut", spec.Path, StatusSkipped, "not present")
			}
			return
		}
		// The launcher on Windows is a .lnk whose name is ours alone, so
		// existence is the ownership test. Resolve Desktop via GetFolderPath
		// (matching installDesktopShortcut) so redirected OneDrive desktops
		// and paths containing spaces work without $args[0] token splitting.
		_ = os.Remove(spec.Path)
		script := fmt.Sprintf(
			"$desktop=[Environment]::GetFolderPath('Desktop');"+
				"$path=Join-Path $desktop '%s.lnk';"+
				"if(Test-Path -LiteralPath $path){Remove-Item -LiteralPath $path -Force;Write-Output 'removed'}else{Write-Output 'missing'}",
			psSingleQuote(DesktopShortcutName),
		)
		output, err := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script).CombinedOutput()
		if err != nil {
			report.Add("*", "shortcut", spec.Path, StatusError, strings.TrimSpace(string(output))+" "+err.Error())
			return
		}
		if strings.Contains(string(output), "removed") {
			report.Add("*", "shortcut", spec.Path, StatusRemoved, "")
			return
		}
		report.Add("*", "shortcut", spec.Path, StatusSkipped, "not present")
		return
	}
	current, err := os.ReadFile(spec.Path)
	if err != nil {
		report.Add("*", "shortcut", spec.Path, StatusSkipped, "not present")
		return
	}
	if !strings.Contains(string(current), DesktopShortcutName) {
		report.Add("*", "shortcut", spec.Path, StatusConflict, "desktop entry is not ours; not touching it")
		return
	}
	if options.DryRun {
		report.Add("*", "shortcut", spec.Path, StatusPlanned, "")
		return
	}
	if err := os.Remove(spec.Path); err != nil {
		report.Add("*", "shortcut", spec.Path, StatusError, "cannot remove: "+err.Error())
		return
	}
	report.Add("*", "shortcut", spec.Path, StatusRemoved, "")
}
