// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/rocareer/rolling-memory/internal/api"
	"github.com/rocareer/rolling-memory/internal/config"
	"github.com/rocareer/rolling-memory/internal/harness"
)

func (a *application) runInitPortable(rawDir string) int {
	targetRoot := expandUser(strings.TrimSpace(rawDir))
	if abs, err := filepath.Abs(targetRoot); err == nil {
		targetRoot = abs
	}

	binDir := filepath.Join(targetRoot, "bin")
	cfgDir := filepath.Join(targetRoot, "config")
	dataDir := filepath.Join(targetRoot, "data")
	stateDir := filepath.Join(targetRoot, "state")
	skillDir := filepath.Join(targetRoot, "skills", "rolling-memory")

	for _, dir := range []string{targetRoot, binDir, cfgDir, dataDir, stateDir, skillDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return a.fail(err)
		}
	}
	_ = os.Chmod(dataDir, 0o700)
	_ = os.Chmod(stateDir, 0o700)

	markerPath := filepath.Join(targetRoot, ".portable")
	if _, err := os.Stat(markerPath); os.IsNotExist(err) {
		if err := os.WriteFile(markerPath, []byte("rolling-memory portable payload\n"), 0o644); err != nil {
			return a.fail(err)
		}
	}

	binName := "rolling-memory"
	if runtime.GOOS == "windows" {
		binName = "rolling-memory.exe"
	}
	dstBin := filepath.Join(binDir, binName)
	if srcBin, err := os.Executable(); err == nil && srcBin != "" {
		if resolved, evalErr := filepath.EvalSymlinks(srcBin); evalErr == nil && resolved != "" {
			srcBin = resolved
		}
		if err := copyFileIfDifferent(srcBin, dstBin, 0o755); err != nil {
			return a.fail(err)
		}
	}

	dstSkill := filepath.Join(skillDir, "SKILL.md")
	srcSkill := harness.DefaultSkillFile()
	skillCopied := false
	if info, err := os.Stat(srcSkill); err == nil && !info.IsDir() {
		if err := copyFileIfDifferent(srcSkill, dstSkill, 0o644); err != nil {
			return a.fail(err)
		}
		skillCopied = true
	}

	cfgPath := filepath.Join(cfgDir, "config.toml")
	if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
		if err := os.WriteFile(cfgPath, []byte(config.PortableDefaultConfigText()), 0o600); err != nil {
			return a.fail(err)
		}
		_ = os.Chmod(cfgPath, 0o600)
	}
	if scope, ok := a.args.LookupString("scope"); ok && scope != "" {
		if _, err := config.SetConfigValue(cfgPath, "core.scope", scope); err != nil {
			return a.fail(err)
		}
	}

	if err := writePortableHelperScripts(targetRoot); err != nil {
		return a.fail(err)
	}

	dbPath := filepath.Join(dataDir, "memory.db")
	if explicitDB := a.args.StringPtr("db"); explicitDB != nil && *explicitDB != "" {
		dbPath = expandUser(*explicitDB)
		if !filepath.IsAbs(dbPath) {
			dbPath = filepath.Join(targetRoot, dbPath)
		}
	}
	logPath := filepath.Join(stateDir, "rolling-memory.log")

	cfg, err := config.Load(config.LoadOptions{
		Path:  &cfgPath,
		DB:    &dbPath,
		Mode:  a.args.StringPtr("mode"),
		Scope: a.args.StringPtr("scope"),
		Env: map[string]string{
			"ROLLING_MEMORY_PORTABLE": targetRoot,
			"ROLLING_MEMORY_LOG":      logPath,
		},
	})
	if err != nil {
		return a.fail(err)
	}
	cfg.Storage.Profile = "usb"
	if err := cfg.Ensure(); err != nil {
		return a.fail(err)
	}

	memory, err := api.New(cfg)
	if err != nil {
		return a.fail(err)
	}
	status, statusErr := memory.Status(cfg.Scope)
	_, ejectErr := memory.Eject()
	closeErr := memory.Close()
	if statusErr != nil {
		return a.fail(statusErr)
	}
	if ejectErr != nil {
		return a.fail(ejectErr)
	}
	if closeErr != nil {
		return a.fail(closeErr)
	}

	result := map[string]any{
		"ok":             true,
		"portable_root":  targetRoot,
		"marker":         markerPath,
		"binary":         dstBin,
		"skill":          dstSkill,
		"skill_copied":   skillCopied,
		"db":             cfg.DB,
		"config":         cfg.Path,
		"profile":        cfg.Storage.Profile,
		"scope":          cfg.Scope,
		"mode":           cfg.Mode,
		"schema_version": status["schema_version"],
	}
	if err := a.emit(result, func() { a.printInit(result) }); err != nil {
		return a.fail(err)
	}
	return 0
}

func copyFileIfDifferent(src, dst string, perm os.FileMode) error {
	srcInfo, err := os.Stat(src)
	if err != nil {
		return err
	}
	if dstInfo, err := os.Stat(dst); err == nil && os.SameFile(srcInfo, dstInfo) {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	tmp := fmt.Sprintf("%s.tmp.%d", dst, os.Getpid())
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	_ = os.Chmod(tmp, perm)
	return os.Rename(tmp, dst)
}

func writePortableHelperScripts(root string) error {
	setupSh := strings.Join([]string{
		"#!/bin/sh",
		"# Rolling-Memory portable USB setup (macOS / Linux).",
		"set -eu",
		"ROOT=\"$(cd \"$(dirname \"$0\")\" && pwd)\"",
		"export ROLLING_MEMORY_PORTABLE=\"$ROOT\"",
		"os_name=\"$(uname -s 2>/dev/null | tr '[:upper:]' '[:lower:]')\"",
		"arch_name=\"$(uname -m 2>/dev/null | tr '[:upper:]' '[:lower:]')\"",
		"case \"$arch_name\" in x86_64|amd64) arch_name=\"amd64\" ;; arm64|aarch64) arch_name=\"arm64\" ;; esac",
		"BIN=\"$ROOT/bin/rolling-memory-${os_name}-${arch_name}\"",
		"if [ ! -x \"$BIN\" ]; then BIN=\"$ROOT/bin/rolling-memory\"; fi",
		"exec \"$BIN\" install --with-hooks \"$@\"",
		"",
	}, "\n")
	ejectSh := strings.Join([]string{
		"#!/bin/sh",
		"# Rolling-Memory portable USB safe eject: flush & truncate SQLite WAL before unmounting.",
		"set -eu",
		"ROOT=\"$(cd \"$(dirname \"$0\")\" && pwd)\"",
		"export ROLLING_MEMORY_PORTABLE=\"$ROOT\"",
		"os_name=\"$(uname -s 2>/dev/null | tr '[:upper:]' '[:lower:]')\"",
		"arch_name=\"$(uname -m 2>/dev/null | tr '[:upper:]' '[:lower:]')\"",
		"case \"$arch_name\" in x86_64|amd64) arch_name=\"amd64\" ;; arm64|aarch64) arch_name=\"arm64\" ;; esac",
		"BIN=\"$ROOT/bin/rolling-memory-${os_name}-${arch_name}\"",
		"if [ ! -x \"$BIN\" ]; then BIN=\"$ROOT/bin/rolling-memory\"; fi",
		"exec \"$BIN\" eject \"$@\"",
		"",
	}, "\n")
	setupCmd := strings.Join([]string{
		"@echo off",
		"rem Rolling-Memory portable USB setup (Windows).",
		"set \"ROOT=%~dp0\"",
		"if \"%ROOT:~-1%\"==\"\\\" set \"ROOT=%ROOT:~0,-1%\"",
		"set \"ROLLING_MEMORY_PORTABLE=%ROOT%\"",
		"if exist \"%ROOT%\\bin\\rolling-memory-windows-amd64.exe\" (",
		"  \"%ROOT%\\bin\\rolling-memory-windows-amd64.exe\" install --with-hooks %*",
		"  exit /b %ERRORLEVEL%",
		")",
		"\"%ROOT%\\bin\\rolling-memory.exe\" install --with-hooks %*",
		"",
	}, "\r\n")
	ejectCmd := strings.Join([]string{
		"@echo off",
		"rem Rolling-Memory portable USB safe eject (Windows).",
		"set \"ROOT=%~dp0\"",
		"if \"%ROOT:~-1%\"==\"\\\" set \"ROOT=%ROOT:~0,-1%\"",
		"set \"ROLLING_MEMORY_PORTABLE=%ROOT%\"",
		"if exist \"%ROOT%\\bin\\rolling-memory-windows-amd64.exe\" (",
		"  \"%ROOT%\\bin\\rolling-memory-windows-amd64.exe\" eject %*",
		"  exit /b %ERRORLEVEL%",
		")",
		"\"%ROOT%\\bin\\rolling-memory.exe\" eject %*",
		"",
	}, "\r\n")

	scripts := []struct {
		name string
		body string
		perm os.FileMode
	}{
		{"setup.sh", setupSh, 0o755},
		{"setup.command", setupSh, 0o755},
		{"eject.sh", ejectSh, 0o755},
		{"eject.command", ejectSh, 0o755},
		{"setup.cmd", setupCmd, 0o644},
		{"eject.cmd", ejectCmd, 0o644},
	}
	for _, item := range scripts {
		target := filepath.Join(root, item.name)
		if err := os.WriteFile(target, []byte(item.body), item.perm); err != nil {
			return err
		}
		_ = os.Chmod(target, item.perm)
	}
	return nil
}
