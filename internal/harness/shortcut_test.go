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

// TestDesktopShortcutSpec pins the platform shapes: the launcher always runs
// `<binary> panel`, so macOS gets a double-clickable .command, Linux a desktop
// entry, and Windows a .lnk created through PowerShell.
func TestDesktopShortcutSpec(t *testing.T) {
	home := "/home/user"
	binary := "/opt/rolling-memory/bin/rolling-memory.sh"

	mac := desktopShortcutSpec("darwin", home, binary)
	if mac.Path != filepath.Join(home, "Desktop", "Rolling-Memory Console.command") {
		t.Fatalf("darwin path = %s", mac.Path)
	}
	if !strings.HasPrefix(mac.Content, "#!/bin/sh") || !strings.Contains(mac.Content, binary+" panel") {
		t.Fatalf("darwin content = %q", mac.Content)
	}
	if !mac.Executable {
		t.Fatal("darwin launcher must be executable")
	}

	linux := desktopShortcutSpec("linux", home, binary)
	if !strings.HasSuffix(linux.Path, "rolling-memory-console.desktop") {
		t.Fatalf("linux path = %s", linux.Path)
	}
	if !strings.Contains(linux.Content, "Type=Application") || !strings.Contains(linux.Content, "Exec="+binary+" panel") {
		t.Fatalf("linux content = %q", linux.Content)
	}

	windows := desktopShortcutSpec("windows", `C:\Users\user`, `C:\Users\user\AppData\Local\rolling-memory\bin\rolling-memory.cmd`)
	if len(windows.Command) == 0 || windows.Command[0] != "powershell" {
		t.Fatalf("windows command = %#v", windows.Command)
	}
	script := strings.Join(windows.Command, " ")
	for _, want := range []string{"GetFolderPath('Desktop')", ".lnk", "rolling-memory.cmd", "'panel'"} {
		if !strings.Contains(script, want) {
			t.Fatalf("windows script misses %q: %s", want, script)
		}
	}
	// Paths with spaces must survive quoting in the POSIX launchers.
	spaced := desktopShortcutSpec("darwin", "/home/user", "/Users/My User/bin/rolling-memory.sh")
	if !strings.Contains(spaced.Content, `'/Users/My User/bin/rolling-memory.sh' panel`) {
		t.Fatalf("darwin quoting = %q", spaced.Content)
	}
}

// TestDesktopShortcutLifecycle drives install/refresh/conflict/uninstall on the
// running platform (the Darwin and Linux shapes share the file path logic and
// the Darwin shape is exercised on the development host).
func TestDesktopShortcutLifecycle(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("windows shortcuts are created through PowerShell; covered by the spec test")
	}
	home := t.TempDir()
	options := InstallOptions{Home: home, Name: DefaultName, SkillName: DefaultName}
	report := newReport("install", options)

	installDesktopShortcut(options, report)
	if len(report.Failures()) > 0 {
		t.Fatalf("install failed: %#v", report.Failures())
	}
	spec := desktopShortcutSpec(runtime.GOOS, home, options.ResolvedBinary())
	info, err := os.Stat(spec.Path)
	if err != nil {
		t.Fatalf("shortcut missing after install: %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o100 == 0 {
		t.Fatalf("shortcut not executable: %v", info.Mode())
	}

	report = newReport("install", options)
	installDesktopShortcut(options, report)
	sawUnchanged := false
	for _, action := range report.Actions {
		if action.Component == "shortcut" && action.Status == StatusUnchanged {
			sawUnchanged = true
		}
	}
	if !sawUnchanged {
		t.Fatalf("re-install must report unchanged: %#v", report.Actions)
	}

	// A foreign file at the same name is left alone.
	if err := os.WriteFile(spec.Path, []byte("#!/bin/sh\necho not ours\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	report = newReport("install", options)
	installDesktopShortcut(options, report)
	sawConflict := false
	for _, action := range report.Actions {
		if action.Component == "shortcut" && action.Status == StatusConflict {
			sawConflict = true
		}
	}
	if !sawConflict {
		t.Fatalf("foreign desktop entry must conflict: %#v", report.Actions)
	}

	// Restore ours, then uninstall removes it and a second uninstall reports
	// it as not present.
	report = newReport("install", options)
	spec.Content = strings.Replace(spec.Content, DesktopShortcutName, DesktopShortcutName, 1)
	if err := writeDesktopShortcut(spec); err != nil {
		t.Fatal(err)
	}
	report = newReport("uninstall", options)
	uninstallDesktopShortcut(options, report)
	if _, err := os.Stat(spec.Path); !os.IsNotExist(err) {
		t.Fatalf("shortcut survived uninstall: %v", err)
	}
	report = newReport("uninstall", options)
	uninstallDesktopShortcut(options, report)
	sawSkipped := false
	for _, action := range report.Actions {
		if action.Component == "shortcut" && action.Status == StatusSkipped {
			sawSkipped = true
		}
	}
	if !sawSkipped {
		t.Fatalf("second uninstall must report skipped: %#v", report.Actions)
	}
}
