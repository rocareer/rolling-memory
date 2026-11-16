// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestConsoleReleaseBaseSemantics pins the release-channel switch shared with
// the shell launchers: unset means the official channel, an explicit empty
// value (or off/none/0) disables fetching.
func TestConsoleReleaseBaseSemantics(t *testing.T) {
	const official = "https://github.com/rocareer/rolling-memory/releases/latest/download"

	os.Unsetenv("ROLLING_MEMORY_RELEASE_BASE")
	t.Cleanup(func() { os.Unsetenv("ROLLING_MEMORY_RELEASE_BASE") })
	if got := consoleReleaseBase(); got != official {
		t.Fatalf("unset base = %q, want the official channel", got)
	}

	for _, value := range []string{"", "off", "none", "0"} {
		t.Setenv("ROLLING_MEMORY_RELEASE_BASE", value)
		if got := consoleReleaseBase(); got != "" {
			t.Fatalf("base %q = %q, want disabled", value, got)
		}
	}

	t.Setenv("ROLLING_MEMORY_RELEASE_BASE", "https://mirror.example.com/assets")
	if got := consoleReleaseBase(); got != "https://mirror.example.com/assets" {
		t.Fatalf("mirror base = %q", got)
	}
}

// TestDownloadVerifiedAsset covers the SHA-256 gate: a matching payload is
// installed, a mismatching payload is refused without touching the target, and
// a SHA256SUMS file without the entry is refused.
func TestDownloadVerifiedAsset(t *testing.T) {
	payload := []byte("console-binary-bytes")
	digest := sha256.Sum256(payload)
	sums := hex.EncodeToString(digest[:]) + "  rolling-admin-test-arch\n"

	serve := func(challenge string) *httptest.Server {
		mux := http.NewServeMux()
		mux.HandleFunc("/SHA256SUMS", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(challenge))
		})
		mux.HandleFunc("/rolling-admin-test-arch", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write(payload)
		})
		return httptest.NewServer(mux)
	}

	target := filepath.Join(t.TempDir(), "rolling-admin")

	good := serve(sums)
	defer good.Close()
	if err := downloadVerifiedAsset(good.URL, "rolling-admin-test-arch", target); err != nil {
		t.Fatalf("verified download failed: %v", err)
	}
	installed, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(installed, payload) {
		t.Fatalf("installed payload mismatch: %v", err)
	}

	mismatch := serve(hex.EncodeToString(make([]byte, 32)) + "  rolling-admin-test-arch\n")
	defer mismatch.Close()
	if err := downloadVerifiedAsset(mismatch.URL, "rolling-admin-test-arch", target); err == nil {
		t.Fatal("mismatching payload must be refused")
	}

	missing := serve("deadbeef  other-asset\n")
	defer missing.Close()
	if err := downloadVerifiedAsset(missing.URL, "rolling-admin-test-arch", target); err == nil {
		t.Fatal("SHA256SUMS without the asset entry must be refused")
	}
}

// TestExtractTarGzSkipsTraversal pins the archive unpack: regular files land in
// the destination, directory entries are created, and entries escaping the
// destination are skipped.
func TestExtractTarGzSkipsTraversal(t *testing.T) {
	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	tarWriter := tar.NewWriter(gzipWriter)
	writeFile := func(name string, content string) {
		t.Helper()
		if err := tarWriter.WriteHeader(&tar.Header{
			Name: name, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tarWriter.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarWriter.WriteHeader(&tar.Header{Name: "assets/", Mode: 0o755, Typeflag: tar.TypeDir}); err != nil {
		t.Fatal(err)
	}
	writeFile("index.html", "<html>console</html>")
	writeFile("assets/app.js", "console.log(1)")
	writeFile("../escape.txt", "must not land outside")
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "web-dist.tar.gz")
	if err := os.WriteFile(archive, buffer.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	destination := t.TempDir()
	if err := extractTarGz(archive, destination); err != nil {
		t.Fatalf("extract: %v", err)
	}
	if !regularFileExists(filepath.Join(destination, "index.html")) {
		t.Fatal("index.html missing after extract")
	}
	if !regularFileExists(filepath.Join(destination, "assets", "app.js")) {
		t.Fatal("nested asset missing after extract")
	}
	if regularFileExists(filepath.Join(filepath.Dir(destination), "escape.txt")) {
		t.Fatal("path traversal entry must be skipped")
	}
}

// TestReadPIDFile pins the pid bookkeeping: plain numbers parse, garbage and
// missing files report zero.
func TestReadPIDFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rolling-admin.pid")
	if got := readPIDFile(path); got != 0 {
		t.Fatalf("missing pid file = %d, want 0", got)
	}
	if err := os.WriteFile(path, []byte("12345\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := readPIDFile(path); got != 12345 {
		t.Fatalf("pid = %d, want 12345", got)
	}
	if err := os.WriteFile(path, []byte("not-a-pid"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := readPIDFile(path); got != 0 {
		t.Fatalf("garbage pid = %d, want 0", got)
	}
}

// TestWebAssetsHandler pins the static frontend server used by `panel start`:
// files pass through, unknown paths fall back to index.html, API prefixes stay
// 404, and non-GET methods are refused.
func TestWebAssetsHandler(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"index.html":    "<html>console</html>",
		"assets/app.js": "console.log(1)",
	} {
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(name)), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	handler := webAssetsHandler(dir)

	request := func(method, requestPath string) *httptest.ResponseRecorder {
		t.Helper()
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(method, requestPath, nil))
		return recorder
	}
	if got := request(http.MethodGet, "/"); got.Code != http.StatusOK || !strings.Contains(got.Body.String(), "console") {
		t.Fatalf("GET / = %d %q", got.Code, got.Body.String())
	}
	if got := request(http.MethodGet, "/assets/app.js"); got.Code != http.StatusOK || !strings.Contains(got.Body.String(), "console.log") {
		t.Fatalf("GET asset = %d %q", got.Code, got.Body.String())
	}
	if got := request(http.MethodGet, "/#/admin"); got.Code != http.StatusOK || !strings.Contains(got.Body.String(), "console") {
		t.Fatalf("hash route must fall back to index: %d", got.Code)
	}
	if got := request(http.MethodGet, "/admin/login-config"); got.Code != http.StatusNotFound {
		t.Fatalf("API prefix must stay 404 on the web server: %d", got.Code)
	}
	if got := request(http.MethodPost, "/"); got.Code != http.StatusNotFound {
		t.Fatalf("POST must be refused: %d", got.Code)
	}
}

// TestPanelPortResolution pins the port single-source contract shared with
// bin/rolling-memory-panel: flag > RM_PANEL_* env > documented default
// (backend 9006, web 8008).
func TestPanelPortResolution(t *testing.T) {
	os.Unsetenv("RM_PANEL_API_PORT")
	os.Unsetenv("RM_PANEL_WEB_PORT")
	t.Cleanup(func() {
		os.Unsetenv("RM_PANEL_API_PORT")
		os.Unsetenv("RM_PANEL_WEB_PORT")
	})
	if got := panelPort(nil, "RM_PANEL_API_PORT", 9006); got != 9006 {
		t.Fatalf("default api port = %d", got)
	}
	t.Setenv("RM_PANEL_API_PORT", "9106")
	if got := panelPort(nil, "RM_PANEL_API_PORT", 9006); got != 9106 {
		t.Fatalf("env api port = %d", got)
	}
	explicit := 9206
	if got := panelPort(&explicit, "RM_PANEL_API_PORT", 9006); got != 9206 {
		t.Fatalf("flag must win: %d", got)
	}
	if got := panelPort(nil, "RM_PANEL_WEB_PORT", 8008); got != 8008 {
		t.Fatalf("default web port = %d", got)
	}
}
