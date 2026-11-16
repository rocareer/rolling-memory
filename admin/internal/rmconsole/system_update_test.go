package rollingmemory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSystemVersionAndUpdateHandlers(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell stub test runs on unix")
	}
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("XDG_DATA_HOME", filepath.Join(tmpHome, ".local", "share"))

	binDir := filepath.Join(tmpHome, ".local", "share", "rolling-memory", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	fakeCLI := filepath.Join(binDir, "rolling-memory")
	script := `#!/bin/sh
if [ "$1" = "update" ]; then
  for arg in "$@"; do
    if [ "$arg" = "--check" ]; then
      echo '{"ok":true,"action":"check","current_version":"0.0.1","latest_version":"0.0.2","update_available":true,"cli":{"asset":"rolling-memory-darwin-arm64","update_available":true}}'
      exit 0
    fi
  done
  echo '{"ok":true,"action":"update","updated":true,"components":["rolling-memory-darwin-arm64","web-dist.tar.gz"],"previous_version":"0.0.1","latest_version":"0.0.2","console_restart_deferred":false}'
  exit 0
fi
exit 1
`
	if err := os.WriteFile(fakeCLI, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ROLLING_MEMORY_BIN", fakeCLI)

	// 1. /system/version returns local version and SHA-256 without network access.
	verRaw := callPluginHandler(t, systemVersionHandler, "/admin/plugin/rolling-memory/system/version", `{}`)
	var verResp struct {
		Code int            `json:"code"`
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(verRaw), &verResp); err != nil {
		t.Fatalf("unmarshal version response: %v (%s)", err, verRaw)
	}
	if verResp.Code != 0 || verResp.Data["ok"] != true {
		t.Fatalf("unexpected version response: %+v", verResp)
	}
	cliInfo, _ := verResp.Data["cli"].(map[string]any)
	if cliSHA, _ := cliInfo["local_sha256"].(string); len(cliSHA) != 64 {
		t.Fatalf("expected 64-char cli local_sha256, got %q", cliSHA)
	}

	// 2. Reject invalid release_base schemes, non-loopback HTTP, or flag injection.
	for _, badBase := range []string{"--bad-flag", "http://evil.example.com/releases", "http://127.0.0.1.evil.com/releases", "file:///tmp/evil"} {
		badRaw := callPluginHandler(t, systemUpdateCheckHandler, "/admin/plugin/rolling-memory/system/update/check", `{"release_base":"`+badBase+`"}`)
		if !strings.Contains(badRaw, "invalid release_base") {
			t.Fatalf("expected invalid release_base rejection for %q, got %s", badBase, badRaw)
		}
	}

	// 3. /system/update/check returns parsed check payload.
	chkRaw := callPluginHandler(t, systemUpdateCheckHandler, "/admin/plugin/rolling-memory/system/update/check", `{"release_base":"https://example.com/releases"}`)
	var chkResp struct {
		Code int            `json:"code"`
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(chkRaw), &chkResp); err != nil {
		t.Fatalf("unmarshal check response: %v (%s)", err, chkRaw)
	}
	if chkResp.Code != 0 || chkResp.Data["update_available"] != true || chkResp.Data["latest_version"] != "0.0.2" {
		t.Fatalf("unexpected check response: %+v", chkResp)
	}

	// 4. /system/update/apply runs update with --defer-console-restart and returns updated components.
	appRaw := callPluginHandler(t, systemUpdateApplyHandler, "/admin/plugin/rolling-memory/system/update/apply", `{"release_base":"https://example.com/releases","force":true}`)
	var appResp struct {
		Code int            `json:"code"`
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(appRaw), &appResp); err != nil {
		t.Fatalf("unmarshal apply response: %v (%s)", err, appRaw)
	}
	if appResp.Code != 0 || appResp.Data["updated"] != true || appResp.Data["latest_version"] != "0.0.2" {
		t.Fatalf("unexpected apply response: %+v", appResp)
	}
}
