// plugin: rolling-memory v0.0.58
package rollingmemory

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func fakeBackgroundPickerLookPath(available map[string]string) backgroundPickerLookPath {
	return func(name string) (string, error) {
		if path, ok := available[name]; ok {
			return path, nil
		}
		return "", errors.New("not found: " + name)
	}
}

func TestNativeBackgroundPickerCommandPlatforms(t *testing.T) {
	t.Run("macOS uses the native AppleScript folder panel", func(t *testing.T) {
		command, err := nativeBackgroundPickerCommand("darwin", fakeBackgroundPickerLookPath(map[string]string{
			"osascript": "/usr/bin/osascript",
		}), func(string) string { return "" })
		if err != nil {
			t.Fatalf("nativeBackgroundPickerCommand: %v", err)
		}
		args := strings.Join(command.args, "\n")
		if command.name != "/usr/bin/osascript" || !strings.Contains(args, "choose folder") || !strings.Contains(args, "on error number -128") {
			t.Fatalf("unexpected macOS picker command: %+v", command)
		}
	})

	t.Run("Windows uses an STA WinForms folder dialog", func(t *testing.T) {
		command, err := nativeBackgroundPickerCommand("windows", fakeBackgroundPickerLookPath(map[string]string{
			"powershell.exe": `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`,
		}), func(string) string { return "" })
		if err != nil {
			t.Fatalf("nativeBackgroundPickerCommand: %v", err)
		}
		args := strings.Join(command.args, "\n")
		if !strings.Contains(args, "-STA") || !strings.Contains(args, "System.Windows.Forms.FolderBrowserDialog") || !strings.Contains(args, "UserProfile") {
			t.Fatalf("unexpected Windows picker command: %+v", command)
		}
	})

	t.Run("Windows falls back to PowerShell Core", func(t *testing.T) {
		command, err := nativeBackgroundPickerCommand("windows", fakeBackgroundPickerLookPath(map[string]string{
			"pwsh": "/usr/local/bin/pwsh",
		}), func(string) string { return "" })
		if err != nil || command.name != "/usr/local/bin/pwsh" {
			t.Fatalf("PowerShell Core fallback: command=%+v err=%v", command, err)
		}
	})

	t.Run("Linux defaults to Zenity", func(t *testing.T) {
		command, err := nativeBackgroundPickerCommand("linux", fakeBackgroundPickerLookPath(map[string]string{
			"zenity":  "/usr/bin/zenity",
			"kdialog": "/usr/bin/kdialog",
		}), func(name string) string {
			if name == "DISPLAY" {
				return ":0"
			}
			if name == "HOME" {
				return "/home/test"
			}
			return ""
		})
		if err != nil {
			t.Fatalf("nativeBackgroundPickerCommand: %v", err)
		}
		if command.name != "/usr/bin/zenity" || !strings.Contains(strings.Join(command.args, " "), "--directory") {
			t.Fatalf("unexpected Zenity command: %+v", command)
		}
	})

	t.Run("Linux KDE prefers KDialog", func(t *testing.T) {
		command, err := nativeBackgroundPickerCommand("linux", fakeBackgroundPickerLookPath(map[string]string{
			"zenity":  "/usr/bin/zenity",
			"kdialog": "/usr/bin/kdialog",
		}), func(name string) string {
			switch name {
			case "WAYLAND_DISPLAY":
				return "wayland-0"
			case "XDG_CURRENT_DESKTOP":
				return "KDE"
			case "HOME":
				return "/home/test"
			default:
				return ""
			}
		})
		if err != nil {
			t.Fatalf("nativeBackgroundPickerCommand: %v", err)
		}
		if command.name != "/usr/bin/kdialog" || !strings.Contains(strings.Join(command.args, " "), "--getexistingdirectory") {
			t.Fatalf("unexpected KDialog command: %+v", command)
		}
	})

	t.Run("Linux headless and unsupported systems fail to manual fallback", func(t *testing.T) {
		lookPath := fakeBackgroundPickerLookPath(map[string]string{"zenity": "/usr/bin/zenity"})
		if _, err := nativeBackgroundPickerCommand("linux", lookPath, func(string) string { return "" }); err == nil {
			t.Fatal("expected a headless Linux picker error")
		}
		if _, err := nativeBackgroundPickerCommand("plan9", lookPath, func(string) string { return "" }); err == nil {
			t.Fatal("expected an unsupported OS picker error")
		}
	})
}

func TestRunNativeBackgroundDirectoryPickerResults(t *testing.T) {
	root := t.TempDir()
	lookPath := fakeBackgroundPickerLookPath(map[string]string{
		"osascript": "/usr/bin/osascript",
		"zenity":    "/usr/bin/zenity",
	})
	getenv := func(name string) string {
		if name == "DISPLAY" {
			return ":0"
		}
		return ""
	}

	t.Run("selected path is canonicalized", func(t *testing.T) {
		selection := runNativeBackgroundDirectoryPicker(context.Background(), "darwin", lookPath, getenv,
			func(context.Context, backgroundPickerCommand) (backgroundPickerCommandResult, error) {
				return backgroundPickerCommandResult{stdout: root + "\n"}, nil
			})
		wantRoot, err := filepath.EvalSymlinks(root)
		if err != nil {
			t.Fatalf("resolve test root: %v", err)
		}
		if selection.Status != "selected" || selection.Path != wantRoot {
			t.Fatalf("unexpected selected result: %+v", selection)
		}
	})

	t.Run("empty native output means cancelled", func(t *testing.T) {
		selection := runNativeBackgroundDirectoryPicker(context.Background(), "darwin", lookPath, getenv,
			func(context.Context, backgroundPickerCommand) (backgroundPickerCommandResult, error) {
				return backgroundPickerCommandResult{}, nil
			})
		if selection.Status != "cancelled" {
			t.Fatalf("unexpected cancellation result: %+v", selection)
		}
	})

	t.Run("Linux exit one without stderr means cancelled", func(t *testing.T) {
		selection := runNativeBackgroundDirectoryPicker(context.Background(), "linux", lookPath, getenv,
			func(context.Context, backgroundPickerCommand) (backgroundPickerCommandResult, error) {
				return backgroundPickerCommandResult{exitCode: 1}, errors.New("exit status 1")
			})
		if selection.Status != "cancelled" {
			t.Fatalf("unexpected Linux cancellation result: %+v", selection)
		}
	})

	t.Run("desktop failure preserves diagnostic detail", func(t *testing.T) {
		selection := runNativeBackgroundDirectoryPicker(context.Background(), "darwin", lookPath, getenv,
			func(context.Context, backgroundPickerCommand) (backgroundPickerCommandResult, error) {
				return backgroundPickerCommandResult{stderr: "no window server", exitCode: 1}, errors.New("exit status 1")
			})
		if selection.Status != "unavailable" || selection.Detail != "no window server" {
			t.Fatalf("unexpected failure result: %+v", selection)
		}
	})

	t.Run("missing selected directory falls back", func(t *testing.T) {
		selection := runNativeBackgroundDirectoryPicker(context.Background(), "darwin", lookPath, getenv,
			func(context.Context, backgroundPickerCommand) (backgroundPickerCommandResult, error) {
				return backgroundPickerCommandResult{stdout: filepath.Join(root, "missing")}, nil
			})
		if selection.Status != "unavailable" || !strings.Contains(selection.Detail, "不是目录") {
			t.Fatalf("unexpected invalid directory result: %+v", selection)
		}
	})
}

func TestBackgroundDirectoryPickerBusyAndHandler(t *testing.T) {
	backgroundDirectoryPickerLock <- struct{}{}
	selection := pickNativeBackgroundDirectory(context.Background())
	<-backgroundDirectoryPickerLock
	if selection.Status != "busy" {
		t.Fatalf("expected busy result, got %+v", selection)
	}

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/admin/plugin/rolling-memory/background/select", nil)
	handleBackgroundDirectorySelect(c, func(context.Context) backgroundDirectorySelection {
		return backgroundDirectorySelection{Status: "selected", Path: "/tmp/background"}
	})
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"status":"selected"`) || !strings.Contains(recorder.Body.String(), `"path":"/tmp/background"`) {
		t.Fatalf("unexpected picker response: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestCanonicalBackgroundDirPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	nested := filepath.Join(home, "nested")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	wantNested, err := filepath.EvalSymlinks(nested)
	if err != nil {
		t.Fatalf("resolve nested path: %v", err)
	}
	resolved, err := canonicalBackgroundDirPath("~/nested")
	if err != nil || resolved != wantNested {
		t.Fatalf("canonicalBackgroundDirPath: path=%q err=%v", resolved, err)
	}
	filePath := filepath.Join(home, "file.txt")
	if err := os.WriteFile(filePath, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if _, err := canonicalBackgroundDirPath(filePath); err == nil {
		t.Fatal("expected a regular file to be rejected")
	}
}
