// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package updatecheck

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	// ScheduleInterval is the cadence of the optional OS-level update check.
	ScheduleInterval = 6 * time.Hour
	scheduleLabel    = "org.rocareer.rolling-memory.update"
	scheduleTaskName = "Rolling Memory Update Check"
)

// ScheduleStatus describes the optional OS-level scheduled check.
type ScheduleStatus struct {
	Installed bool   `json:"installed"`
	Platform  string `json:"platform"`
	Kind      string `json:"kind"`
	Path      string `json:"path,omitempty"`
	Detail    string `json:"detail,omitempty"`
}

func scheduleDryRun() bool {
	if IsTestBinary() {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("ROLLING_MEMORY_SCHEDULE_DRY_RUN"))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func scheduleBaseDir() (string, string, error) {
	if override := strings.TrimSpace(os.Getenv("ROLLING_MEMORY_SCHEDULE_DIR")); override != "" {
		switch runtime.GOOS {
		case "darwin":
			return override, "launchd", nil
		case "linux":
			return override, "systemd", nil
		case "windows":
			return override, "schtasks", nil
		default:
			return "", "", fmt.Errorf("scheduled update checks are not supported on %s", runtime.GOOS)
		}
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", "", errors.New("cannot resolve user home directory")
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "LaunchAgents"), "launchd", nil
	case "linux":
		if xdg := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); xdg != "" {
			return filepath.Join(xdg, "systemd", "user"), "systemd", nil
		}
		return filepath.Join(home, ".config", "systemd", "user"), "systemd", nil
	case "windows":
		return "", "schtasks", nil
	default:
		return "", "", fmt.Errorf("scheduled update checks are not supported on %s", runtime.GOOS)
	}
}

func scheduleLogPath() string {
	return filepath.Join(filepath.Dir(StatePath()), "update-schedule.log")
}

// InstallSchedule creates a user-level periodic run of the saved update.auto
// policy. The command decides whether to only refresh the shared cache, notify,
// or install a verified update; "off" is a no-op.
func InstallSchedule(binary, releaseBase string) (ScheduleStatus, error) {
	binary, err := scheduleBinary(binary)
	if err != nil {
		return ScheduleStatus{}, err
	}
	_ = os.MkdirAll(filepath.Dir(scheduleLogPath()), 0o755)
	dir, kind, err := scheduleBaseDir()
	if err != nil {
		return ScheduleStatus{}, err
	}
	status := ScheduleStatus{Platform: runtime.GOOS, Kind: kind}
	switch kind {
	case "launchd":
		path := filepath.Join(dir, scheduleLabel+".plist")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return status, err
		}
		plist := launchdPlist(binary, releaseBase)
		status.Path = path
		status.Installed = true
		// An identical re-install (the common case: channel/source/auto changes
		// all resolve to the same plist) must not unload the job. `unload -w`
		// kills an in-flight `update --auto` run and leaves its lock behind, so
		// reload only when the definition changed or the job is not yet loaded.
		if !scheduleDryRun() && launchdJobLoaded(scheduleLabel) && fileContentEquals(path, plist) {
			status.Detail = "launchd job unchanged and loaded"
			return status, nil
		}
		if err := os.WriteFile(path, plist, 0o644); err != nil {
			return status, err
		}
		if scheduleDryRun() {
			status.Detail = "dry run: launchd job written"
			return status, nil
		}
		_ = exec.Command("launchctl", "unload", "-w", path).Run()
		if output, err := exec.Command("launchctl", "load", "-w", path).CombinedOutput(); err != nil {
			return status, fmt.Errorf("launchctl load %s: %w (%s)", path, err, strings.TrimSpace(string(output)))
		}
		status.Detail = "launchd job loaded"
		return status, nil
	case "systemd":
		servicePath := filepath.Join(dir, "rolling-memory-update.service")
		timerPath := filepath.Join(dir, "rolling-memory-update.timer")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return status, err
		}
		if err := os.WriteFile(servicePath, systemdService(binary, releaseBase), 0o644); err != nil {
			return status, err
		}
		if err := os.WriteFile(timerPath, systemdTimer(), 0o644); err != nil {
			return status, err
		}
		status.Path = timerPath
		status.Installed = true
		if scheduleDryRun() {
			status.Detail = "dry run: systemd timer written"
			return status, nil
		}
		if output, err := exec.Command("systemctl", "--user", "daemon-reload").CombinedOutput(); err != nil {
			return status, fmt.Errorf("systemctl --user daemon-reload: %w (%s)", err, strings.TrimSpace(string(output)))
		}
		if output, err := exec.Command("systemctl", "--user", "enable", "--now", "rolling-memory-update.timer").CombinedOutput(); err != nil {
			return status, fmt.Errorf("systemctl --user enable: %w (%s)", err, strings.TrimSpace(string(output)))
		}
		status.Detail = "systemd timer enabled"
		return status, nil
	case "schtasks":
		status.Path = scheduleTaskName
		status.Installed = true
		if scheduleDryRun() {
			status.Detail = "dry run: scheduled task planned"
			return status, nil
		}
		taskCommand := fmt.Sprintf("\"%s\" update --auto --quiet%s", binary, windowsReleaseArg(releaseBase))
		if output, err := exec.Command("schtasks", "/Create", "/F", "/SC", "HOURLY", "/MO", "6",
			"/TN", scheduleTaskName, "/TR", taskCommand).CombinedOutput(); err != nil {
			return status, fmt.Errorf("schtasks /Create: %w (%s)", err, strings.TrimSpace(string(output)))
		}
		status.Detail = "Windows scheduled task created"
		return status, nil
	}
	return status, fmt.Errorf("unsupported scheduler kind %q", kind)
}

// RemoveSchedule removes the user-level periodic check.
func RemoveSchedule() (ScheduleStatus, error) {
	dir, kind, err := scheduleBaseDir()
	if err != nil {
		return ScheduleStatus{}, err
	}
	status := ScheduleStatus{Platform: runtime.GOOS, Kind: kind}
	switch kind {
	case "launchd":
		path := filepath.Join(dir, scheduleLabel+".plist")
		status.Path = path
		status.Installed = false
		if scheduleDryRun() {
			_ = os.Remove(path)
			status.Detail = "dry run: launchd job removed"
			return status, nil
		}
		_ = exec.Command("launchctl", "unload", "-w", path).Run()
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return status, err
		}
		status.Detail = "launchd job removed"
		return status, nil
	case "systemd":
		timerPath := filepath.Join(dir, "rolling-memory-update.timer")
		servicePath := filepath.Join(dir, "rolling-memory-update.service")
		status.Path = timerPath
		status.Installed = false
		if scheduleDryRun() {
			_ = os.Remove(timerPath)
			_ = os.Remove(servicePath)
			status.Detail = "dry run: systemd timer removed"
			return status, nil
		}
		_, _ = exec.Command("systemctl", "--user", "disable", "--now", "rolling-memory-update.timer").CombinedOutput()
		_ = os.Remove(timerPath)
		_ = os.Remove(servicePath)
		_, _ = exec.Command("systemctl", "--user", "daemon-reload").CombinedOutput()
		status.Detail = "systemd timer removed"
		return status, nil
	case "schtasks":
		status.Path = scheduleTaskName
		status.Installed = false
		if scheduleDryRun() {
			status.Detail = "dry run: scheduled task removal planned"
			return status, nil
		}
		if output, err := exec.Command("schtasks", "/Delete", "/F", "/TN", scheduleTaskName).CombinedOutput(); err != nil {
			if strings.Contains(strings.ToLower(string(output)), "cannot find") {
				status.Detail = "scheduled task was not installed"
				return status, nil
			}
			return status, fmt.Errorf("schtasks /Delete: %w (%s)", err, strings.TrimSpace(string(output)))
		}
		status.Detail = "Windows scheduled task removed"
		return status, nil
	}
	return status, fmt.Errorf("unsupported scheduler kind %q", kind)
}

// ScheduleStatusInfo reports whether the periodic check is installed.
func ScheduleStatusInfo() (ScheduleStatus, error) {
	dir, kind, err := scheduleBaseDir()
	if err != nil {
		return ScheduleStatus{}, err
	}
	status := ScheduleStatus{Platform: runtime.GOOS, Kind: kind}
	switch kind {
	case "launchd":
		status.Path = filepath.Join(dir, scheduleLabel+".plist")
		_, err := os.Stat(status.Path)
		status.Installed = err == nil
		if status.Installed {
			status.Detail = "launchd job installed"
		} else {
			status.Detail = "launchd job not installed"
		}
	case "systemd":
		status.Path = filepath.Join(dir, "rolling-memory-update.timer")
		_, err := os.Stat(status.Path)
		status.Installed = err == nil
		if status.Installed {
			status.Detail = "systemd timer installed"
		} else {
			status.Detail = "systemd timer not installed"
		}
	case "schtasks":
		status.Path = scheduleTaskName
		if scheduleDryRun() {
			status.Detail = "dry run: task status not queried"
			return status, nil
		}
		output, err := exec.Command("schtasks", "/Query", "/TN", scheduleTaskName).CombinedOutput()
		status.Installed = err == nil
		if status.Installed {
			status.Detail = strings.TrimSpace(string(output))
		} else {
			status.Detail = "scheduled task not installed"
		}
	}
	return status, nil
}

func scheduleBinary(binary string) (string, error) {
	trimmed := strings.TrimSpace(binary)
	if trimmed == "" {
		executable, err := os.Executable()
		if err != nil {
			return "", err
		}
		trimmed = executable
	}
	absolute, err := filepath.Abs(trimmed)
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(absolute); err == nil && resolved != "" {
		absolute = resolved
	}
	if strings.Contains(absolute, string(filepath.Separator)+"go-build") {
		return "", errors.New("run the schedule command from an installed rolling-memory binary, not `go run`")
	}
	info, err := os.Stat(absolute)
	if err != nil || info.IsDir() {
		return "", fmt.Errorf("update-check binary is not executable: %s", absolute)
	}
	return absolute, nil
}

// launchdJobLoaded reports whether the launchd job is currently loaded. A
// lookup error is treated as "not loaded" so the caller falls back to a normal
// unload/load cycle.
func launchdJobLoaded(label string) bool {
	if scheduleDryRun() {
		return false
	}
	return exec.Command("launchctl", "list", label).Run() == nil
}

// fileContentEquals reports whether the file at path already carries exactly
// content. A missing file is "not equal" so the caller writes it fresh.
func fileContentEquals(path string, content []byte) bool {
	existing, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return bytes.Equal(existing, content)
}

func launchdPlist(binary, releaseBase string) []byte {
	var escapedBinary bytes.Buffer
	_ = xml.EscapeText(&escapedBinary, []byte(binary))
	var escapedLog bytes.Buffer
	_ = xml.EscapeText(&escapedLog, []byte(scheduleLogPath()))
	releaseArgs := ""
	if strings.TrimSpace(releaseBase) != "" {
		var escapedBase bytes.Buffer
		_ = xml.EscapeText(&escapedBase, []byte(strings.TrimSpace(releaseBase)))
		releaseArgs = fmt.Sprintf("    <string>--release-base</string>\n    <string>%s</string>\n", escapedBase.String())
	}
	return []byte(fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key>
  <array>
    <string>%s</string>
    <string>update</string>
    <string>--auto</string>
    <string>--quiet</string>
%s  </array>
  <key>StartInterval</key><integer>%d</integer>
  <key>RunAtLoad</key><true/>
  <key>StandardOutPath</key><string>%s</string>
  <key>StandardErrorPath</key><string>%s</string>
</dict>
</plist>
`, scheduleLabel, escapedBinary.String(), releaseArgs, int(ScheduleInterval.Seconds()), escapedLog.String(), escapedLog.String()))
}

func systemdService(binary, releaseBase string) []byte {
	return []byte(fmt.Sprintf(`[Unit]
Description=Rolling Memory update check

[Service]
Type=oneshot
ExecStart=%s update --auto --quiet%s
`, systemdQuote(binary), systemdArg(releaseBase)))
}

func systemdTimer() []byte {
	return []byte(`[Unit]
Description=Periodic Rolling Memory update check

[Timer]
OnBootSec=5min
OnUnitActiveSec=6h

[Install]
WantedBy=timers.target
`)
}

func systemdArg(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	return " --release-base " + systemdQuote(value)
}

func windowsReleaseArg(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	return fmt.Sprintf(" --release-base \"%s\"", strings.ReplaceAll(value, "\"", "\\\""))
}

func systemdQuote(value string) string {
	if !strings.ContainsAny(value, " \t\"'\\") {
		return value
	}
	replacer := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + replacer.Replace(value) + `"`
}
