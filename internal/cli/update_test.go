// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func sha256HexBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func buildTestWebDistTarGz(t *testing.T, indexHTML string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	data := []byte(indexHTML)
	if err := tw.WriteHeader(&tar.Header{
		Name: "index.html",
		Mode: 0o644,
		Size: int64(len(data)),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestParseSHA256SumsAndVersionExtraction(t *testing.T) {
	cliAsset := platformCLIAssetName()
	adminAsset := platformAdminAssetName()
	archiveName := fmt.Sprintf("rolling-memory-0.2.0-%s-%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	sha1 := strings.Repeat("a", 64)
	sha2 := strings.Repeat("b", 64)
	sha3 := strings.Repeat("c", 64)
	sha4 := strings.Repeat("d", 64)
	manifest := strings.Join([]string{
		fmt.Sprintf("%s  %s", sha1, archiveName),
		fmt.Sprintf("%s  *%s", sha2, cliAsset),
		fmt.Sprintf("%s  %s", sha3, adminAsset),
		fmt.Sprintf("%s  web-dist.tar.gz", sha4),
	}, "\n")

	sums, ver := parseSHA256Sums(manifest)
	if ver != "0.2.0" {
		t.Fatalf("detected version = %q, want 0.2.0", ver)
	}
	if sums[cliAsset] != sha2 {
		t.Fatalf("cli sha = %q, want %q", sums[cliAsset], sha2)
	}
	if sums[adminAsset] != sha3 {
		t.Fatalf("admin sha = %q, want %q", sums[adminAsset], sha3)
	}
	if sums["web-dist.tar.gz"] != sha4 {
		t.Fatalf("web-dist sha = %q, want %q", sums["web-dist.tar.gz"], sha4)
	}
}

func TestUpdateCheckAndSelfUpdateLifecycle(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)
	t.Setenv("XDG_DATA_HOME", filepath.Join(tempHome, ".local", "share"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tempHome, ".config"))
	installDir := filepath.Join(tempHome, ".local", "share", "rolling-memory", "bin")
	linkDir := filepath.Join(tempHome, ".local", "bin")
	mockApp := filepath.Join(tempHome, "Applications", "Rolling-Memory.app")
	t.Setenv("ROLLING_MEMORY_INSTALL_DIR", installDir)
	t.Setenv("ROLLING_MEMORY_LINK_DIR", linkDir)
	t.Setenv("ROLLING_MEMORY_DESKTOP_APP", mockApp)

	cliAsset := platformCLIAssetName()
	adminAsset := platformAdminAssetName()
	archiveName := fmt.Sprintf("rolling-memory-0.0.2-%s-%s.tar.gz", runtime.GOOS, runtime.GOARCH)

	oldCLIBin := []byte("#!/bin/sh\necho old-cli\n")
	newCLIBin := []byte("#!/bin/sh\necho new-cli-v0.0.2\n")
	newAdminBin := []byte("#!/bin/sh\necho new-admin-v0.0.2\n")
	newWebTar := buildTestWebDistTarGz(t, "<!doctype html><title>console v0.0.2</title>")

	if err := os.MkdirAll(installDir, 0o755); err != nil {
		t.Fatal(err)
	}
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	targetBin := filepath.Join(installDir, "rolling-memory"+ext)
	if err := os.WriteFile(targetBin, oldCLIBin, 0o755); err != nil {
		t.Fatal(err)
	}

	manifest := strings.Join([]string{
		fmt.Sprintf("%s  %s", sha256HexBytes([]byte("archive")), archiveName),
		fmt.Sprintf("%s  %s", sha256HexBytes(newCLIBin), cliAsset),
		fmt.Sprintf("%s  %s", sha256HexBytes(newAdminBin), adminAsset),
		fmt.Sprintf("%s  web-dist.tar.gz", sha256HexBytes(newWebTar)),
	}, "\n")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/SHA256SUMS":
			_, _ = w.Write([]byte(manifest))
		case "/" + cliAsset:
			_, _ = w.Write(newCLIBin)
		case "/" + adminAsset:
			_, _ = w.Write(newAdminBin)
		case "/web-dist.tar.gz":
			_, _ = w.Write(newWebTar)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	t.Setenv("ROLLING_MEMORY_RELEASE_BASE", server.URL)

	runCLI := func(args ...string) (int, map[string]any) {
		t.Helper()
		var out, errBuf bytes.Buffer
		code := Main(args, strings.NewReader(""), &out, &errBuf)
		var payload map[string]any
		if out.Len() > 0 {
			if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
				t.Fatalf("unmarshal %q: %v (stderr=%s)", out.String(), err, errBuf.String())
			}
		}
		return code, payload
	}

	// 1. Check detects update available without modifying targetBin.
	code, checkRes := runCLI("update", "--check", "--json")
	if code != 0 {
		t.Fatalf("update --check exit=%d, res=%v", code, checkRes)
	}
	if checkRes["update_available"] != true || checkRes["latest_version"] != "0.0.2" {
		t.Fatalf("unexpected check result: %v", checkRes)
	}
	if got, _ := os.ReadFile(targetBin); !bytes.Equal(got, oldCLIBin) {
		t.Fatalf("check must not modify binary")
	}

	// 2. Perform update with --with-console and --no-install (with a mock desktop .app bundle).
	mockPayload := filepath.Join(mockApp, "Contents", "Resources", "payload")
	if err := os.MkdirAll(mockPayload, 0o755); err != nil {
		t.Fatal(err)
	}
	mockPlist := filepath.Join(mockApp, "Contents", "Info.plist")
	initialPlist := "<plist><dict>\n\t<key>CFBundleShortVersionString</key>\n\t<string>0.0.1</string>\n\t<key>CFBundleVersion</key>\n\t<string>0.0.1</string>\n</dict></plist>\n"
	if err := os.WriteFile(mockPlist, []byte(initialPlist), 0o644); err != nil {
		t.Fatal(err)
	}

	code, updRes := runCLI("update", "--with-console", "--no-install", "--json")
	if code != 0 {
		t.Fatalf("update exit=%d, res=%v", code, updRes)
	}
	if updRes["updated"] != true {
		t.Fatalf("expected updated=true, got %v", updRes)
	}
	if got, _ := os.ReadFile(targetBin); !bytes.Equal(got, newCLIBin) {
		t.Fatalf("binary was not updated: %q", string(got))
	}
	if gotVer, _ := os.ReadFile(filepath.Join(mockPayload, "VERSION")); strings.TrimSpace(string(gotVer)) != "0.0.2" {
		t.Fatalf("desktop app payload VERSION not synced: got %q", string(gotVer))
	}
	if gotPlist, _ := os.ReadFile(mockPlist); !strings.Contains(string(gotPlist), "<string>0.0.2</string>") {
		t.Fatalf("desktop app Info.plist version not synced: got %q", string(gotPlist))
	}
	if synced, ok := updRes["desktop_bundles_synced"].([]any); !ok || len(synced) != 1 || synced[0] != mockApp {
		t.Fatalf("desktop_bundles_synced = %v, want only [%q]", updRes["desktop_bundles_synced"], mockApp)
	}
	_, cBin, cWebDist, _, _, _, _ := consoleLayout()
	if got, _ := os.ReadFile(cBin); !bytes.Equal(got, newAdminBin) {
		t.Fatalf("console binary was not updated: %q", string(got))
	}
	if readWebDistSHA256(cWebDist) != sha256HexBytes(newWebTar) {
		t.Fatalf("web-dist .sha256 marker mismatch: got %q, want %q", readWebDistSHA256(cWebDist), sha256HexBytes(newWebTar))
	}

	// 3. Re-running `upgrade` (alias) when digests match is a fast no-op.
	code, noopRes := runCLI("upgrade", "--no-install", "--json")
	if code != 0 {
		t.Fatalf("upgrade noop exit=%d, res=%v", code, noopRes)
	}
	if noopRes["updated"] != false {
		t.Fatalf("expected updated=false on second run, got %v", noopRes)
	}

	// 4. Explicit --release-base flag overrides ROLLING_MEMORY_RELEASE_BASE=off and --force + --defer-console-restart works.
	t.Setenv("ROLLING_MEMORY_RELEASE_BASE", "off")
	code, forceRes := runCLI("update", "--release-base", server.URL, "--force", "--with-console", "--defer-console-restart", "--no-install", "--json")
	if code != 0 {
		t.Fatalf("update --release-base --force exit=%d, res=%v", code, forceRes)
	}
	if forceRes["updated"] != true || forceRes["release_base"] != server.URL {
		t.Fatalf("expected updated=true with release_base=%s, got %v", server.URL, forceRes)
	}
}

func TestConsoleReleaseMirrorFallbackPolicy(t *testing.T) {
	t.Setenv("ROLLING_MEMORY_RELEASE_BASE", "")
	t.Setenv("ROLLING_MEMORY_RELEASE_MIRROR", "https://mirror.example.com/download")
	if got := consoleReleaseMirror(""); got != "https://mirror.example.com/download" {
		t.Fatalf("expected mirror URL when base is not pinned, got %q", got)
	}
	if got := consoleReleaseMirror("https://explicit.example.com/download"); got != "" {
		t.Fatalf("explicit flag must disable mirror fallback, got %q", got)
	}
	t.Setenv("ROLLING_MEMORY_RELEASE_BASE", "https://explicit-env.example.com/download")
	if got := consoleReleaseMirror(""); got != "" {
		t.Fatalf("explicit env must disable mirror fallback, got %q", got)
	}
}
