// plugin: rolling-memory v0.0.58
package rollingmemory

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/ai-go-hub/ai-go-admin/internal/kit/httpx"
	"github.com/gin-gonic/gin"
)

const backgroundDirectoryPickerTimeout = 5 * time.Minute

var backgroundDirectoryPickerLock = make(chan struct{}, 1)

type backgroundDirectorySelection struct {
	Status string `json:"status"`
	Path   string `json:"path,omitempty"`
	Detail string `json:"detail,omitempty"`
}

type backgroundPickerCommand struct {
	name            string
	args            []string
	cancelExitCodes []int
}

type backgroundPickerCommandResult struct {
	stdout   string
	stderr   string
	exitCode int
}

type backgroundPickerLookPath func(string) (string, error)
type backgroundPickerGetenv func(string) string
type backgroundPickerRunner func(context.Context, backgroundPickerCommand) (backgroundPickerCommandResult, error)
type backgroundDirectoryPicker func(context.Context) backgroundDirectorySelection

// backgroundSelectHandler 在 Admin 进程所在机器上打开操作系统原生目录选择器。
func backgroundSelectHandler(c *gin.Context) {
	handleBackgroundDirectorySelect(c, pickNativeBackgroundDirectory)
}

func handleBackgroundDirectorySelect(c *gin.Context, picker backgroundDirectoryPicker) {
	httpx.Success(c, httpx.WithData(picker(c.Request.Context())))
}

func pickNativeBackgroundDirectory(ctx context.Context) backgroundDirectorySelection {
	select {
	case backgroundDirectoryPickerLock <- struct{}{}:
		defer func() { <-backgroundDirectoryPickerLock }()
	default:
		return backgroundDirectorySelection{Status: "busy"}
	}

	pickerCtx, cancel := context.WithTimeout(ctx, backgroundDirectoryPickerTimeout)
	defer cancel()
	return runNativeBackgroundDirectoryPicker(pickerCtx, runtime.GOOS, exec.LookPath, os.Getenv, runBackgroundPickerCommand)
}

func runNativeBackgroundDirectoryPicker(
	ctx context.Context,
	goos string,
	lookPath backgroundPickerLookPath,
	getenv backgroundPickerGetenv,
	run backgroundPickerRunner,
) backgroundDirectorySelection {
	command, err := nativeBackgroundPickerCommand(goos, lookPath, getenv)
	if err != nil {
		return backgroundDirectorySelection{Status: "unavailable", Detail: err.Error()}
	}
	result, err := run(ctx, command)
	if err != nil {
		if ctx.Err() != nil {
			return backgroundDirectorySelection{Status: "unavailable", Detail: ctx.Err().Error()}
		}
		for _, code := range command.cancelExitCodes {
			if result.exitCode == code && strings.TrimSpace(result.stderr) == "" {
				return backgroundDirectorySelection{Status: "cancelled"}
			}
		}
		detail := strings.TrimSpace(result.stderr)
		if detail == "" {
			detail = err.Error()
		}
		return backgroundDirectorySelection{Status: "unavailable", Detail: detail}
	}

	rawPath := strings.TrimRight(result.stdout, "\r\n")
	if strings.TrimSpace(rawPath) == "" {
		return backgroundDirectorySelection{Status: "cancelled"}
	}
	cleanRoot, err := canonicalBackgroundDirPath(rawPath)
	if err != nil {
		return backgroundDirectorySelection{Status: "unavailable", Detail: err.Error()}
	}
	return backgroundDirectorySelection{Status: "selected", Path: cleanRoot}
}

func nativeBackgroundPickerCommand(goos string, lookPath backgroundPickerLookPath, getenv backgroundPickerGetenv) (backgroundPickerCommand, error) {
	switch goos {
	case "darwin":
		bin, err := lookPath("osascript")
		if err != nil {
			return backgroundPickerCommand{}, fmt.Errorf("未找到 macOS 系统目录选择器 osascript: %w", err)
		}
		const script = `try
    return POSIX path of (choose folder with prompt "Select a folder for Rolling-Memory Background" default location (path to home folder))
on error number -128
    return ""
end try`
		return backgroundPickerCommand{name: bin, args: []string{"-e", script}}, nil
	case "windows":
		bin, err := lookPath("powershell.exe")
		if err != nil {
			bin, err = lookPath("pwsh")
			if err != nil {
				return backgroundPickerCommand{}, errors.New("未找到 Windows PowerShell 或 PowerShell，无法打开系统目录选择器")
			}
		}
		const script = `$ErrorActionPreference = 'Stop'
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
try {
    Add-Type -AssemblyName System.Windows.Forms
    $dialog = New-Object System.Windows.Forms.FolderBrowserDialog
    $dialog.Description = 'Select a folder for Rolling-Memory Background'
    $dialog.ShowNewFolderButton = $false
    $dialog.SelectedPath = [Environment]::GetFolderPath('UserProfile')
    try {
        $result = $dialog.ShowDialog()
        if ($result -eq [System.Windows.Forms.DialogResult]::OK) {
            [Console]::Out.Write($dialog.SelectedPath)
        }
    } finally {
        $dialog.Dispose()
    }
} catch {
    [Console]::Error.Write($_.Exception.Message)
    exit 1
}`
		return backgroundPickerCommand{name: bin, args: []string{"-NoLogo", "-NoProfile", "-STA", "-Command", script}}, nil
	case "linux":
		if strings.TrimSpace(getenv("DISPLAY")) == "" && strings.TrimSpace(getenv("WAYLAND_DISPLAY")) == "" {
			return backgroundPickerCommand{}, errors.New("Admin 进程没有可用的 Linux 图形会话（DISPLAY / WAYLAND_DISPLAY）")
		}
		desktop := strings.ToLower(getenv("XDG_CURRENT_DESKTOP") + " " + getenv("XDG_SESSION_DESKTOP"))
		candidates := []string{"zenity", "kdialog"}
		if getenv("KDE_SESSION_VERSION") != "" || strings.Contains(desktop, "kde") {
			candidates = []string{"kdialog", "zenity"}
		}
		for _, candidate := range candidates {
			bin, err := lookPath(candidate)
			if err != nil {
				continue
			}
			startDir := strings.TrimSpace(getenv("HOME"))
			if startDir == "" {
				startDir = "."
			}
			if candidate == "kdialog" {
				return backgroundPickerCommand{
					name:            bin,
					args:            []string{"--getexistingdirectory", startDir, "--title", "Select a folder for Rolling-Memory Background"},
					cancelExitCodes: []int{1},
				}, nil
			}
			return backgroundPickerCommand{
				name:            bin,
				args:            []string{"--file-selection", "--directory", "--title=Select a folder for Rolling-Memory Background", "--filename=" + strings.TrimRight(startDir, "/") + "/"},
				cancelExitCodes: []int{1},
			}, nil
		}
		return backgroundPickerCommand{}, errors.New("未找到 Linux 系统目录选择器，请安装 Zenity 或 KDialog")
	default:
		return backgroundPickerCommand{}, fmt.Errorf("当前操作系统 %s 暂不支持原生目录选择，请手动输入目录路径", goos)
	}
}

func runBackgroundPickerCommand(ctx context.Context, command backgroundPickerCommand) (backgroundPickerCommandResult, error) {
	cmd := exec.CommandContext(ctx, command.name, command.args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.Output()
	result := backgroundPickerCommandResult{stdout: string(stdout), stderr: stderr.String(), exitCode: 0}
	if err != nil {
		result.exitCode = -1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			result.exitCode = exitErr.ExitCode()
		}
	}
	return result, err
}

func canonicalBackgroundDirPath(rawPath string) (string, error) {
	rawPath = strings.TrimSpace(rawPath)
	if strings.HasPrefix(rawPath, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			rawPath = filepath.Join(home, rawPath[2:])
		}
	}
	abs, err := filepath.Abs(rawPath)
	if err != nil {
		return "", fmt.Errorf("解析目录路径失败: %w", err)
	}
	if resolved, resolveErr := filepath.EvalSymlinks(abs); resolveErr == nil {
		abs = resolved
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("指定路径不存在或不是目录: %s", abs)
	}
	return filepath.Clean(abs), nil
}
