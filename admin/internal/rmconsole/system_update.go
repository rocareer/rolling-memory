package rollingmemory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/ai-go-hub/ai-go-admin/internal/kit/httpx"
	"github.com/gin-gonic/gin"
)

const defaultConsoleUpdateReleaseBase = "https://github.com/rocareer/" + Name + "/releases/latest/download"

type systemUpdateRequest struct {
	ReleaseBase string `json:"release_base"`
	Force       bool   `json:"force"`
	WithConsole *bool  `json:"with_console"`
	NoInstall   bool   `json:"no_install"`
}

func resolveConsoleReleaseBase(override string) string {
	cand := strings.TrimSpace(override)
	if cand == "" {
		if env, ok := os.LookupEnv("ROLLING_MEMORY_RELEASE_BASE"); ok {
			cand = strings.TrimSpace(env)
			if cand == "" {
				return ""
			}
		} else {
			cand = defaultConsoleUpdateReleaseBase
		}
	}
	switch strings.ToLower(cand) {
	case "", "off", "none", "0":
		return ""
	}
	return cand
}

func validateReleaseBaseURL(raw string) error {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	if strings.HasPrefix(trimmed, "-") {
		return errors.New("invalid release_base: must not start with '-'")
	}
	low := strings.ToLower(trimmed)
	if low == "off" || low == "none" || low == "0" {
		return nil
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Scheme == "" {
		return errors.New("invalid release_base: malformed URL")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "https":
		if strings.TrimSpace(parsed.Hostname()) == "" {
			return errors.New("invalid release_base: https:// URL requires a hostname")
		}
		return nil
	case "http":
		if isStrictLoopbackHost(parsed.Hostname()) {
			return nil
		}
		return errors.New("invalid release_base: http:// requires a loopback host; use https:// for remote hosts")
	default:
		return errors.New("invalid release_base: must use https:// (or http:// on loopback)")
	}
}

func isStrictLoopbackHost(host string) bool {
	h := strings.TrimSpace(strings.TrimSuffix(host, "."))
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// resolveRealCLIBinary returns the underlying compiled rolling-memory binary
// rather than the .sh launcher shim when calculating local SHA-256 digests.
func resolveRealCLIBinary(home string) string {
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	installBin := filepath.Join(home, ".local", "share", "rolling-memory", "bin", "rolling-memory"+ext)
	if info, err := os.Stat(installBin); err == nil && info.Mode().IsRegular() {
		return installBin
	}
	bin := resolveBinary(home)
	if strings.HasSuffix(bin, ".sh") {
		trimmed := strings.TrimSuffix(bin, ".sh") + ext
		if info, err := os.Stat(trimmed); err == nil && info.Mode().IsRegular() {
			return trimmed
		}
	}
	if resolved, err := filepath.EvalSymlinks(bin); err == nil && resolved != "" {
		if info, sErr := os.Stat(resolved); sErr == nil && info.Mode().IsRegular() {
			return resolved
		}
	}
	return bin
}

func consoleInstallPaths(home string) (adminBin, webDist string) {
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	base := filepath.Join(home, ".local", "share", "rolling-memory", "admin")
	if xdg := strings.TrimSpace(os.Getenv("XDG_DATA_HOME")); xdg != "" {
		base = filepath.Join(xdg, "rolling-memory", "admin")
	}
	adminBin = filepath.Join(base, "bin", "rolling-admin"+ext)
	if _, err := os.Stat(adminBin); err != nil {
		altBin := filepath.Join(base, "rolling-admin"+ext)
		if info, aErr := os.Stat(altBin); aErr == nil && info.Mode().IsRegular() {
			adminBin = altBin
		}
	}
	webDist = filepath.Join(base, "web-dist")
	return adminBin, webDist
}

type fileSHA256CacheEntry struct {
	size    int64
	mtimeNS int64
	shaHex  string
}

var (
	fileSHA256Mu    sync.Mutex
	fileSHA256Cache = map[string]fileSHA256CacheEntry{}
)

func computeFileSHA256Hex(path string) string {
	clean := filepath.Clean(strings.TrimSpace(path))
	if clean == "" || clean == "." {
		return ""
	}
	info, err := os.Stat(clean)
	if err != nil || !info.Mode().IsRegular() {
		return ""
	}
	sz := info.Size()
	mt := info.ModTime().UnixNano()
	fileSHA256Mu.Lock()
	if cached, ok := fileSHA256Cache[clean]; ok && cached.size == sz && cached.mtimeNS == mt && cached.shaHex != "" {
		fileSHA256Mu.Unlock()
		return cached.shaHex
	}
	fileSHA256Mu.Unlock()

	f, err := os.Open(clean)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	digest := hex.EncodeToString(h.Sum(nil))
	fileSHA256Mu.Lock()
	if len(fileSHA256Cache) > 64 {
		fileSHA256Cache = map[string]fileSHA256CacheEntry{}
	}
	fileSHA256Cache[clean] = fileSHA256CacheEntry{size: sz, mtimeNS: mt, shaHex: digest}
	fileSHA256Mu.Unlock()
	return digest
}

func readMarkerFileSHA256(dir string) string {
	raw, err := os.ReadFile(filepath.Join(dir, ".sha256"))
	if err != nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(string(raw)))
}

// systemVersionHandler returns local version and SHA-256 digest telemetry with
// zero external network requests.
func systemVersionHandler(c *gin.Context) {
	home, _ := os.UserHomeDir()
	cliBin := resolveRealCLIBinary(home)
	adminBin, webDist := consoleInstallPaths(home)
	adminSHA := computeFileSHA256Hex(adminBin)
	if adminSHA == "" {
		if self, err := os.Executable(); err == nil {
			adminSHA = computeFileSHA256Hex(self)
			adminBin = self
		}
	}
	webSHA := readMarkerFileSHA256(webDist)
	_, adminStatErr := os.Stat(adminBin)
	_, webStatErr := os.Stat(filepath.Join(webDist, "index.html"))
	releaseBase := resolveConsoleReleaseBase("")

	httpx.Success(c, httpx.WithData(gin.H{
		"ok":                      true,
		"current_version":         liveSoftwareVersion(),
		"core_version":            CoreVersion,
		"core_codename":           CoreCodename,
		"os":                      runtime.GOOS,
		"arch":                    runtime.GOARCH,
		"release_base":            releaseBase,
		"release_channel_enabled": releaseBase != "",
		"cli": gin.H{
			"path":         cliBin,
			"local_sha256": computeFileSHA256Hex(cliBin),
		},
		"console": gin.H{
			"installed":       adminStatErr == nil || webStatErr == nil,
			"admin_path":      adminBin,
			"admin_local_sha": adminSHA,
			"web_dist_path":   webDist,
			"web_local_sha":   webSHA,
		},
	}))
}

// systemUpdateCheckHandler runs `rolling-memory update --check --with-console --json`
// on explicit user request to compare local digests against the release channel's SHA256SUMS.
func systemUpdateCheckHandler(c *gin.Context) {
	var req systemUpdateRequest
	_ = c.ShouldBindJSON(&req)
	if err := validateReleaseBaseURL(req.ReleaseBase); err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	home, _ := os.UserHomeDir()
	bin := resolveRealCLIBinary(home)
	if bin == "" {
		httpx.Fail(c, httpx.WithMessage("rolling-memory CLI binary not found"))
		return
	}

	args := []string{"update", "--check", "--with-console", "--json"}
	if strings.TrimSpace(req.ReleaseBase) != "" {
		args = append(args, "--release-base", strings.TrimSpace(req.ReleaseBase))
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 25*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	out, err := cmd.CombinedOutput()
	payload := parseCLIJSONOutput(out)
	if err != nil {
		msg := extractCLIErrorMessage(payload, out, err)
		httpx.Fail(c, httpx.WithMessage(msg))
		return
	}
	if payload == nil {
		httpx.Fail(c, httpx.WithMessage("unexpected empty JSON output from rolling-memory update --check"))
		return
	}
	httpx.Success(c, httpx.WithData(payload))
}

// systemUpdateApplyHandler executes an in-place SHA-256 verified self-update via
// `rolling-memory update --with-console --defer-console-restart --json`.
func systemUpdateApplyHandler(c *gin.Context) {
	var req systemUpdateRequest
	_ = c.ShouldBindJSON(&req)
	if err := validateReleaseBaseURL(req.ReleaseBase); err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	home, _ := os.UserHomeDir()
	bin := resolveRealCLIBinary(home)
	if bin == "" {
		httpx.Fail(c, httpx.WithMessage("rolling-memory CLI binary not found"))
		return
	}

	withConsole := true
	if req.WithConsole != nil {
		withConsole = *req.WithConsole
	}
	args := []string{"update", "--defer-console-restart", "--json"}
	if withConsole {
		args = append(args, "--with-console")
	} else {
		args = append(args, "--no-console")
	}
	if req.Force {
		args = append(args, "--force")
	}
	if req.NoInstall {
		args = append(args, "--no-install")
	}
	if strings.TrimSpace(req.ReleaseBase) != "" {
		args = append(args, "--release-base", strings.TrimSpace(req.ReleaseBase))
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 180*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	out, err := cmd.CombinedOutput()
	payload := parseCLIJSONOutput(out)
	if err != nil {
		msg := extractCLIErrorMessage(payload, out, err)
		httpx.Fail(c, httpx.WithMessage(msg))
		return
	}
	if payload == nil {
		httpx.Fail(c, httpx.WithMessage("unexpected empty JSON output from rolling-memory update"))
		return
	}

	// If the console binary or web assets were updated while the console was
	// running, schedule an asynchronous console restart after the HTTP response
	// has been flushed to the browser.
	if deferred, _ := payload["console_restart_deferred"].(bool); deferred {
		updatedBin := resolveRealCLIBinary(home)
		go func(cliBinary string) {
			time.Sleep(600 * time.Millisecond)
			if cliBinary == "" {
				return
			}
			_ = exec.Command(cliBinary, "panel", "stop").Run()
			_ = exec.Command(cliBinary, "panel", "start", "--no-open").Start()
		}(updatedBin)
	}

	httpx.Success(c, httpx.WithData(payload))
}

func parseCLIJSONOutput(raw []byte) map[string]any {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return nil
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(trimmed), &payload); err == nil {
		return payload
	}
	// Fallback: locate the last JSON object line in case hooks or warnings printed to stderr.
	lines := strings.Split(trimmed, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if strings.HasPrefix(line, "{") && strings.HasSuffix(line, "}") {
			if err := json.Unmarshal([]byte(line), &payload); err == nil {
				return payload
			}
		}
	}
	return nil
}

func extractCLIErrorMessage(payload map[string]any, raw []byte, runErr error) string {
	if payload != nil {
		if errStr, ok := payload["error"].(string); ok && strings.TrimSpace(errStr) != "" {
			return strings.TrimSpace(errStr)
		}
	}
	text := strings.TrimSpace(string(raw))
	if text != "" {
		return text
	}
	if runErr != nil {
		return fmt.Sprintf("update command failed: %v", runErr)
	}
	return "update command failed"
}
