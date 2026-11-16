// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package cli

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/rocareer/rolling-memory/internal/config"
)

// The panel command is the one-command entry to the local web console. The
// console keeps the canonical two-port topology: the admin backend (API) on
// 127.0.0.1:9006 and the web frontend on 127.0.0.1:8008. The backend port
// mirrors admin/config/config.yaml (server.port, 9006 by default) and the web
// port mirrors admin/web/.env (VITE_PORT, 8008 by default); RM_PANEL_API_PORT /
// RM_PANEL_WEB_PORT override them, exactly like bin/rolling-memory-panel.
//
// On first use the command fetches the console server and its prebuilt web
// assets (SHA-256 verified) into the data dir, starts both processes detached
// (the web side is this same binary re-executed in --serve-web mode, so no
// node/python is required), then opens http://localhost:8008/#/admin.
const (
	defaultConsoleAPIPort = 9006
	defaultConsoleWebPort = 8008
)

// consoleLayout returns the on-disk locations used by the panel command.
func consoleLayout() (root, binPath, webDist, apiPID, webPID, apiLog, webLog string) {
	base := filepath.Dir(config.DefaultDBPath()) // <data>/rolling-memory
	root = filepath.Join(base, "admin")
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	binPath = filepath.Join(root, "bin", "rolling-admin"+ext)
	webDist = filepath.Join(root, "web-dist")
	apiPID = filepath.Join(root, "rolling-admin.pid")
	webPID = filepath.Join(root, "rolling-admin-web.pid")
	apiLog = filepath.Join(root, "rolling-admin.log")
	webLog = filepath.Join(root, "rolling-admin-web.log")
	return root, binPath, webDist, apiPID, webPID, apiLog, webLog
}

// consoleReleaseBase resolves the release channel used to fetch console
// artifacts; unset means the official channel, an explicitly empty value (or
// off/none/0) disables fetching, mirroring the shell launchers.
func consoleReleaseBase() string {
	base, configured := os.LookupEnv("ROLLING_MEMORY_RELEASE_BASE")
	if !configured {
		base = "https://github.com/rocareer/rolling-memory/releases/latest/download"
	}
	switch strings.ToLower(strings.TrimSpace(base)) {
	case "", "off", "none", "0":
		return ""
	}
	return strings.TrimSpace(base)
}

// consoleReleaseMirror returns the fallback release mirror when the caller did
// not explicitly pin ROLLING_MEMORY_RELEASE_BASE or --release-base. There is no
// default mirror: GitHub is the single public source, so the fallback applies
// only when ROLLING_MEMORY_RELEASE_MIRROR is explicitly injected.
func consoleReleaseMirror(explicitFlag string) string {
	if strings.TrimSpace(explicitFlag) != "" {
		return ""
	}
	if val, configured := os.LookupEnv("ROLLING_MEMORY_RELEASE_BASE"); configured && strings.TrimSpace(val) != "" {
		return ""
	}
	mirror := os.Getenv("ROLLING_MEMORY_RELEASE_MIRROR")
	switch strings.ToLower(strings.TrimSpace(mirror)) {
	case "", "off", "none", "0":
		return ""
	}
	return strings.TrimSpace(mirror)
}

// resolveReleaseBase returns an explicit --release-base flag value when
// provided (honoring off/none/0 to disable), or falls back to consoleReleaseBase().
func resolveReleaseBase(flagValue string) string {
	trimmed := strings.TrimSpace(flagValue)
	if trimmed != "" {
		switch strings.ToLower(trimmed) {
		case "off", "none", "0":
			return ""
		}
		return trimmed
	}
	return consoleReleaseBase()
}

// regularFileExists reports a plain file (not a directory) at path.
func regularFileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func (a *application) runPanel() int {
	if webDir := strings.TrimSpace(a.args.String("serve_web")); webDir != "" {
		return a.runPanelServeWeb(webDir)
	}
	verbs := a.args.List("verbs")
	verb := "start"
	if len(verbs) > 0 {
		verb = strings.ToLower(strings.TrimSpace(verbs[0]))
	}
	root, binPath, webDist, apiPID, webPID, apiLog, webLog := consoleLayout()
	apiPort := panelPort(a.args.IntPtr("api_port"), "RM_PANEL_API_PORT", defaultConsoleAPIPort)
	webPort := panelPort(a.args.IntPtr("web_port"), "RM_PANEL_WEB_PORT", defaultConsoleWebPort)
	layout := panelLayout{
		root: root, binPath: binPath, webDist: webDist,
		apiPID: apiPID, webPID: webPID, apiLog: apiLog, webLog: webLog,
		apiPort: apiPort, webPort: webPort,
	}
	switch verb {
	case "start":
		return a.runPanelStart(layout)
	case "stop":
		return a.runPanelStop(layout)
	case "status":
		return a.runPanelStatus(layout)
	default:
		return a.fail(fmt.Errorf("unknown panel action %q; use start, stop or status", verb))
	}
}

// panelLayout bundles the resolved locations and ports of one console run.
type panelLayout struct {
	root    string
	binPath string
	webDist string
	apiPID  string
	webPID  string
	apiLog  string
	webLog  string
	apiPort int
	webPort int
}

func (l panelLayout) apiURL() string { return fmt.Sprintf("http://127.0.0.1:%d", l.apiPort) }
func (l panelLayout) webURL() string { return fmt.Sprintf("http://127.0.0.1:%d", l.webPort) }

// panelPort resolves one console port: explicit flag, then the RM_PANEL_* env
// override, then the documented default.
func panelPort(flag *int, envName string, fallback int) int {
	if flag != nil && *flag != 0 {
		return *flag
	}
	if env := strings.TrimSpace(os.Getenv(envName)); env != "" {
		if parsed, err := strconv.Atoi(env); err == nil && parsed != 0 {
			return parsed
		}
	}
	return fallback
}

func (a *application) runPanelStart(layout panelLayout) int {
	if flagBase := strings.TrimSpace(a.args.String("release_base")); flagBase != "" {
		_ = os.Setenv("ROLLING_MEMORY_RELEASE_BASE", flagBase)
	}
	downloaded := []string{}
	if a.args.Flag("update") {
		if err := updateConsoleAssetsIfChanged(layout, &downloaded); err != nil {
			return a.fail(err)
		}
	}
	consoleBin, err := ensureConsoleBinary(layout.root, layout.binPath, &downloaded)
	if err != nil {
		return a.fail(err)
	}
	if err := ensureConsoleWebDist(layout.root, layout.webDist, &downloaded); err != nil {
		return a.fail(err)
	}

	apiStarted, err := startConsoleAPI(layout, consoleBin)
	if err != nil {
		return a.fail(err)
	}
	webStarted, err := startConsoleWeb(layout)
	if err != nil {
		return a.fail(err)
	}
	if !waitHTTPReady(layout.apiURL()+"/admin/login-config", 30*time.Second) {
		return a.fail(fmt.Errorf("console API did not become ready at %s; see %s", layout.apiURL(), layout.apiLog))
	}
	if !waitHTTPReady(layout.webURL()+"/", 15*time.Second) {
		return a.fail(fmt.Errorf("console web frontend did not become ready at %s; see %s", layout.webURL(), layout.webLog))
	}

	consoleURL := layout.webURL() + "/#/admin"
	opened := false
	if !a.args.Flag("no_open") {
		opened = openBrowser(consoleURL)
	}
	human := func() {
		if apiStarted || webStarted {
			fmt.Printf("console started · %s\n", consoleURL)
		} else {
			fmt.Printf("console already running · %s\n", consoleURL)
		}
		fmt.Printf("  backend API: %s\n  web frontend: %s\n", layout.apiURL(), layout.webURL())
		if opened {
			fmt.Println("browser opened · sign in with the local account shown on the login page")
		} else if !a.args.Flag("no_open") {
			fmt.Println("open the address above in a browser to sign in")
		}
		fmt.Printf("  console dir: %s\n  stop with: rolling-memory panel stop\n", layout.root)
	}
	apiPID := resolvePanelPID(layout.apiPID, layout.apiPort, true)
	webPID := resolvePanelPID(layout.webPID, layout.webPort, true)
	result := map[string]any{
		"ok":          true,
		"action":      "start",
		"url":         consoleURL,
		"api_url":     layout.apiURL(),
		"web_url":     layout.webURL(),
		"api_pid":     apiPID,
		"web_pid":     webPID,
		"started":     apiStarted || webStarted,
		"browser":     opened,
		"console_dir": layout.root,
		"binary":      consoleBin,
		"web_dist":    layout.webDist,
		"downloaded":  downloaded,
	}
	return emitOrFail(a, result, human)
}

// startConsoleAPI launches `rolling-admin serve` detached unless it is already
// answering on the API port.
func startConsoleAPI(layout panelLayout, consoleBin string) (bool, error) {
	if consoleReady(layout.apiURL()) {
		return false, nil
	}
	if pid := readPIDFile(layout.apiPID); pid != 0 && !processAlive(pid) {
		_ = os.Remove(layout.apiPID)
	}
	logHandle, err := os.OpenFile(layout.apiLog, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return false, fmt.Errorf("open console log: %w", err)
	}
	defer logHandle.Close()
	command := exec.Command(consoleBin, "serve")
	command.Dir = layout.root
	command.Stdout = logHandle
	command.Stderr = logHandle
	command.Env = append(os.Environ(), fmt.Sprintf("SERVER_PORT=%d", layout.apiPort))
	if err := command.Start(); err != nil {
		return false, fmt.Errorf("start console backend: %w", err)
	}
	pid := command.Process.Pid
	_ = command.Process.Release()
	if err := os.WriteFile(layout.apiPID, []byte(strconv.Itoa(pid)+"\n"), 0o644); err != nil {
		return false, fmt.Errorf("write console pid: %w", err)
	}
	return true, nil
}

// startConsoleWeb launches the web frontend server detached unless the web port
// is already answering. The server is this binary in --serve-web mode, so it
// needs no node or python on the machine.
func startConsoleWeb(layout panelLayout) (bool, error) {
	if webAssetsReady(layout.webURL()) {
		return false, nil
	}
	if pid := readPIDFile(layout.webPID); pid != 0 && !processAlive(pid) {
		_ = os.Remove(layout.webPID)
	}
	self, err := os.Executable()
	if err != nil {
		return false, fmt.Errorf("resolve self for the web server: %w", err)
	}
	logHandle, err := os.OpenFile(layout.webLog, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return false, fmt.Errorf("open web log: %w", err)
	}
	defer logHandle.Close()
	command := exec.Command(self, "panel", "--serve-web", layout.webDist, "--web-port", strconv.Itoa(layout.webPort))
	command.Dir = layout.root
	command.Stdout = logHandle
	command.Stderr = logHandle
	if err := command.Start(); err != nil {
		return false, fmt.Errorf("start console web frontend: %w", err)
	}
	pid := command.Process.Pid
	_ = command.Process.Release()
	if err := os.WriteFile(layout.webPID, []byte(strconv.Itoa(pid)+"\n"), 0o644); err != nil {
		return false, fmt.Errorf("write web pid: %w", err)
	}
	return true, nil
}

// runPanelServeWeb is the static frontend server used by `panel start`; it
// serves the web assets and falls back to index.html for unknown paths (the
// console frontend is hash-routed).
func (a *application) runPanelServeWeb(dir string) int {
	port := panelPort(a.args.IntPtr("web_port"), "RM_PANEL_WEB_PORT", defaultConsoleWebPort)
	if !regularFileExists(filepath.Join(dir, "index.html")) {
		return a.fail(fmt.Errorf("web assets not found: %s", dir))
	}
	address := fmt.Sprintf("127.0.0.1:%d", port)
	// Same hardening as the console backend: a slow-header peer must not park a
	// goroutine and a socket forever on this static file server.
	server := &http.Server{
		Addr:              address,
		Handler:           webAssetsHandler(dir),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	fmt.Fprintf(os.Stderr, "[rm-panel] serving console web assets from %s on http://%s\n", dir, address)
	if err := server.ListenAndServe(); err != nil {
		return a.fail(err)
	}
	return 0
}

// webAssetsHandler serves one web dist directory: files pass through, unknown
// paths fall back to index.html, and the API prefixes are never shadowed.
func webAssetsHandler(dir string) http.Handler {
	fileServer := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.NotFound(w, r)
			return
		}
		requestPath := r.URL.Path
		for _, prefix := range []string{"/admin", "/static", "/uploads", "/api"} {
			if requestPath == prefix || strings.HasPrefix(requestPath, prefix+"/") {
				http.NotFound(w, r)
				return
			}
		}
		if requestPath != "/" && requestPath != "/index.html" {
			cleaned := strings.TrimPrefix(path.Clean("/"+requestPath), "/")
			if regularFileExists(filepath.Join(dir, filepath.FromSlash(cleaned))) {
				fileServer.ServeHTTP(w, r)
				return
			}
		}
		// SPA entry HTML must never be served from a stale browser cache after
		// `panel --update` or a web-dist rebuild, otherwise the tab keeps loading
		// old hashed JS chunks.
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		r.URL.Path = "/"
		fileServer.ServeHTTP(w, r)
	})
}

func (a *application) runPanelStop(layout panelLayout) int {
	_ = resolvePanelPID(layout.apiPID, layout.apiPort, consoleReady(layout.apiURL()))
	_ = resolvePanelPID(layout.webPID, layout.webPort, webAssetsReady(layout.webURL()))
	stoppedAPI := stopPIDFile(layout.apiPID)
	stoppedWeb := stopPIDFile(layout.webPID)
	human := func() {
		if stoppedAPI || stoppedWeb {
			fmt.Printf("console stopped (backend=%t, web=%t)\n", stoppedAPI, stoppedWeb)
		} else {
			fmt.Println("console is not running")
		}
	}
	result := map[string]any{
		"ok": true, "action": "stop",
		"stopped": stoppedAPI || stoppedWeb, "stopped_api": stoppedAPI, "stopped_web": stoppedWeb,
		"api_url": layout.apiURL(), "web_url": layout.webURL(),
	}
	return emitOrFail(a, result, human)
}

// stopPIDFile terminates one recorded process and removes its pid file. The
// recorded pid is only signalled while the process still looks like one of our
// own, so a stale pid file whose number was reused by an unrelated program can
// never make `panel stop` (or `update`) kill an innocent process.
func stopPIDFile(pidFile string) bool {
	pid := readPIDFile(pidFile)
	stopped := false
	if pid != 0 && processAlive(pid) {
		if !processLooksLikeConsole(pid) {
			fmt.Fprintf(os.Stderr, "refusing to stop pid %d: the process no longer looks like a Rolling-Memory console\n", pid)
			_ = os.Remove(pidFile)
			return false
		}
		if process, err := os.FindProcess(pid); err == nil {
			_ = process.Signal(os.Interrupt)
			if !waitProcessExit(pid, 5*time.Second) {
				_ = process.Kill()
				_ = waitProcessExit(pid, 3*time.Second)
			}
			stopped = true
		}
	}
	_ = os.Remove(pidFile)
	return stopped
}

// processLooksLikeConsole reports whether pid's command line still belongs to a
// Rolling-Memory console process. It is a name check against the recorded pid
// only; unverifiable pids are refused (never kill what we cannot identify).
func processLooksLikeConsole(pid int) bool {
	if runtime.GOOS == "windows" {
		out, err := exec.Command("tasklist", "/FI", fmt.Sprintf("PID eq %d", pid), "/FO", "CSV", "/NH").Output()
		if err != nil {
			return false
		}
		name := strings.ToLower(string(out))
		return strings.Contains(name, "rolling-admin") || strings.Contains(name, "rolling-memory")
	}
	psBin, err := exec.LookPath("ps")
	if err != nil {
		return false
	}
	out, err := exec.Command(psBin, "-p", strconv.Itoa(pid), "-o", "command=").Output()
	if err != nil {
		return false
	}
	command := strings.ToLower(strings.TrimSpace(string(out)))
	if command == "" {
		return false
	}
	return strings.Contains(command, "rolling-admin") || strings.Contains(command, "rolling-memory")
}

func (a *application) runPanelStatus(layout panelLayout) int {
	apiRunning := consoleReady(layout.apiURL())
	webRunning := webAssetsReady(layout.webURL())
	running := apiRunning && webRunning
	human := func() {
		if running {
			fmt.Printf("running · %s (backend %s · web %s)\n", layout.webURL()+"/#/admin", layout.apiURL(), layout.webURL())
			return
		}
		fmt.Printf("stopped (backend=%t, web=%t)\n", apiRunning, webRunning)
	}
	result := map[string]any{
		"ok":       running,
		"action":   "status",
		"running":  running,
		"api":      apiRunning,
		"web":      webRunning,
		"api_pid":  resolvePanelPID(layout.apiPID, layout.apiPort, apiRunning),
		"web_pid":  resolvePanelPID(layout.webPID, layout.webPort, webRunning),
		"url":      layout.webURL() + "/#/admin",
		"api_url":  layout.apiURL(),
		"web_url":  layout.webURL(),
		"binary":   layout.binPath,
		"web_dist": layout.webDist,
	}
	return emitOrFail(a, result, human)
}

func emitOrFail(a *application, result map[string]any, human func()) int {
	if err := a.emit(result, human); err != nil {
		return a.fail(err)
	}
	if ok, _ := result["ok"].(bool); !ok {
		return 1
	}
	return 0
}

// ensureConsoleBinary resolves the console server: an explicit override, the
// installed layout, a source checkout with a Go toolchain, then the release
// channel (SHA-256 verified).
func ensureConsoleBinary(root, binPath string, downloaded *[]string) (string, error) {
	if override := strings.TrimSpace(os.Getenv("ROLLING_MEMORY_ADMIN_BIN")); override != "" {
		if info, err := os.Stat(override); err == nil && info.Mode().IsRegular() {
			return override, nil
		}
		return "", fmt.Errorf("ROLLING_MEMORY_ADMIN_BIN does not name an executable file: %s", override)
	}
	if info, err := os.Stat(binPath); err == nil && info.Mode().IsRegular() {
		stripDarwinQuarantine(binPath, false)
		return binPath, nil
	}
	if built := buildConsoleFromSource(root, binPath); built != "" {
		return built, nil
	}
	base := consoleReleaseBase()
	if base == "" {
		return "", fmt.Errorf("console binary not found at %s and the release channel is disabled", binPath)
	}
	if err := os.MkdirAll(filepath.Dir(binPath), 0o755); err != nil {
		return "", err
	}
	asset := "rolling-admin-" + runtime.GOOS + "-" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		asset += ".exe"
	}
	if err := downloadVerifiedAsset(base, asset, binPath); err != nil {
		if mirror := consoleReleaseMirror(""); mirror != "" && mirror != base {
			if mErr := downloadVerifiedAsset(mirror, asset, binPath); mErr == nil {
				*downloaded = append(*downloaded, asset)
				return binPath, nil
			}
		}
		return "", fmt.Errorf("console binary not found and download failed: %w", err)
	}
	*downloaded = append(*downloaded, asset)
	return binPath, nil
}

// candidateRepoRoots returns potential repository roots ordered by specificity:
// current working directory (+ parent), directory of the resolved executable
// (+ parent), and common home checkouts (~/Rolling-Memory, ~/rolling-memory).
func candidateRepoRoots() []string {
	var roots []string
	seen := map[string]bool{}
	add := func(p string) {
		if strings.TrimSpace(p) == "" {
			return
		}
		clean := filepath.Clean(p)
		if !seen[clean] {
			seen[clean] = true
			roots = append(roots, clean)
		}
	}
	if cwd, err := os.Getwd(); err == nil {
		add(cwd)
		add(filepath.Join(cwd, ".."))
	}
	if exe, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exe)
		add(exeDir)
		add(filepath.Dir(exeDir))
		if realExe, lErr := filepath.EvalSymlinks(exe); lErr == nil && realExe != exe {
			realDir := filepath.Dir(realExe)
			add(realDir)
			add(filepath.Dir(realDir))
		}
	}
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		add(filepath.Join(home, "Rolling-Memory"))
		add(filepath.Join(home, "rolling-memory"))
	}
	return roots
}

// buildConsoleFromSource compiles the console from a source checkout when the
// current directory, executable location, or home checkout carries the admin module and Go.
func buildConsoleFromSource(root, binPath string) string {
	sourceRoot := ""
	for _, candidate := range candidateRepoRoots() {
		if info, err := os.Stat(filepath.Join(candidate, "admin", "cmd")); err == nil && info.IsDir() {
			sourceRoot = filepath.Join(candidate, "admin")
			break
		}
		if info, err := os.Stat(filepath.Join(candidate, "cmd")); err == nil && info.IsDir() && regularFileExists(filepath.Join(candidate, "go.mod")) {
			sourceRoot = candidate
			break
		}
	}
	if sourceRoot == "" {
		return ""
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		return ""
	}
	if err := os.MkdirAll(filepath.Dir(binPath), 0o755); err != nil {
		return ""
	}
	command := exec.Command(goBin, "build", "-o", binPath, "./cmd")
	command.Dir = sourceRoot
	command.Env = append(os.Environ(), "CGO_ENABLED=0")
	if err := command.Run(); err != nil {
		return ""
	}
	return binPath
}

// buildConsoleWebFromSource builds admin/web from a local source checkout when
// package.json is present and npm is available on PATH.
func buildConsoleWebFromSource() string {
	webRoot := ""
	for _, repoRoot := range candidateRepoRoots() {
		for _, candidate := range []string{
			filepath.Join(repoRoot, "admin", "web"),
			filepath.Join(repoRoot, "web"),
		} {
			if regularFileExists(filepath.Join(candidate, "package.json")) && regularFileExists(filepath.Join(candidate, "vite.config.ts")) {
				webRoot = candidate
				break
			}
		}
		if webRoot != "" {
			break
		}
	}
	if webRoot == "" {
		return ""
	}
	npmBin, err := exec.LookPath("npm")
	if err != nil {
		return ""
	}
	if info, statErr := os.Stat(filepath.Join(webRoot, "node_modules")); statErr != nil || !info.IsDir() {
		installCmd := exec.Command(npmBin, "install")
		installCmd.Dir = webRoot
		if err := installCmd.Run(); err != nil {
			return ""
		}
	}
	buildCmd := exec.Command(npmBin, "run", "build")
	buildCmd.Dir = webRoot
	if err := buildCmd.Run(); err != nil {
		return ""
	}
	distDir := filepath.Join(webRoot, "dist")
	if regularFileExists(filepath.Join(distDir, "index.html")) {
		return distDir
	}
	return ""
}

// ensureConsoleWebDist resolves the console frontend: an explicit override, the
// installed layout, a source checkout's built dist, then the release channel.
func ensureConsoleWebDist(root, webDist string, downloaded *[]string) error {
	if override := strings.TrimSpace(os.Getenv("ROLLING_MEMORY_ADMIN_WEB_DIST")); override != "" {
		if regularFileExists(filepath.Join(override, "index.html")) {
			return nil
		}
		return fmt.Errorf("ROLLING_MEMORY_ADMIN_WEB_DIST has no index.html: %s", override)
	}
	if regularFileExists(filepath.Join(webDist, "index.html")) {
		return nil
	}
	for _, repoRoot := range candidateRepoRoots() {
		for _, candidate := range []string{
			filepath.Join(repoRoot, "admin", "web", "dist"), // repo root
			filepath.Join(repoRoot, "web", "dist"),          // admin module root
		} {
			if regularFileExists(filepath.Join(candidate, "index.html")) {
				if err := copyTree(candidate, webDist); err != nil {
					return err
				}
				return nil
			}
		}
	}
	if builtDist := buildConsoleWebFromSource(); builtDist != "" {
		if err := copyTree(builtDist, webDist); err != nil {
			return err
		}
		return nil
	}
	base := consoleReleaseBase()
	if base == "" {
		return fmt.Errorf("console web assets not found at %s and the release channel is disabled", webDist)
	}
	if err := downloadAndExtractWebDist(base, root, webDist, ""); err != nil {
		if mirror := consoleReleaseMirror(""); mirror != "" && mirror != base {
			if mErr := downloadAndExtractWebDist(mirror, root, webDist, ""); mErr == nil {
				*downloaded = append(*downloaded, "web-dist.tar.gz")
				return nil
			}
		}
		return fmt.Errorf("console web assets not found and download failed: %w", err)
	}
	*downloaded = append(*downloaded, "web-dist.tar.gz")
	return nil
}

// downloadAndExtractWebDist downloads web-dist.tar.gz, verifies its SHA-256
// against SHA256SUMS (or wantSHA when already resolved), extracts it cleanly
// into webDist, and records the archive's SHA-256 in webDist/.sha256 so subsequent
// `rolling-memory update` calls can detect stale web assets in O(1).
func downloadAndExtractWebDist(base, root, webDist, wantSHA string) error {
	if err := os.MkdirAll(filepath.Dir(webDist), 0o755); err != nil {
		return err
	}
	archive := filepath.Join(root, "web-dist.tar.gz")
	if wantSHA != "" {
		if err := downloadAssetWithExpectedSHA(base, "web-dist.tar.gz", wantSHA, archive); err != nil {
			return err
		}
	} else {
		if err := downloadVerifiedAsset(base, "web-dist.tar.gz", archive); err != nil {
			return err
		}
		wantSHA = fileSHA256(archive)
	}
	// The archive carries the dist root at its top level. It is extracted into a
	// staging tree first and swapped in only once it is complete, so a failed
	// extraction (disk full, truncated archive) can never destroy the working
	// console frontend.
	staging := fmt.Sprintf("%s.staging.%d", webDist, os.Getpid())
	_ = os.RemoveAll(staging)
	if err := os.MkdirAll(staging, 0o755); err != nil {
		return err
	}
	if err := extractTarGz(archive, staging); err != nil {
		_ = os.RemoveAll(staging)
		return err
	}
	if !regularFileExists(filepath.Join(staging, "index.html")) {
		_ = os.RemoveAll(staging)
		return fmt.Errorf("downloaded web assets are incomplete: %s", webDist)
	}
	oldDist := ""
	if info, err := os.Stat(webDist); err == nil && info.IsDir() {
		oldDist = fmt.Sprintf("%s.old.%d", webDist, os.Getpid())
		_ = os.RemoveAll(oldDist)
		if err := os.Rename(webDist, oldDist); err != nil {
			_ = os.RemoveAll(staging)
			return err
		}
	}
	if err := os.Rename(staging, webDist); err != nil {
		if oldDist != "" {
			_ = os.Rename(oldDist, webDist)
		}
		_ = os.RemoveAll(staging)
		return err
	}
	if oldDist != "" {
		_ = os.RemoveAll(oldDist)
	}
	_ = os.Remove(archive)
	writeWebDistSHA256(webDist, wantSHA)
	return nil
}

// updateConsoleAssetsIfChanged checks SHA256SUMS on the release channel when
// `rolling-memory panel --update` is invoked, replaces any outdated console
// binary or web assets, and stops running processes so they restart cleanly
// with the updated artifacts.
func updateConsoleAssetsIfChanged(layout panelLayout, downloaded *[]string) error {
	base := consoleReleaseBase()
	if base == "" {
		return fmt.Errorf("release channel is disabled (ROLLING_MEMORY_RELEASE_BASE is off)")
	}
	sums, _, err := fetchReleaseManifest(base, 15*time.Second)
	if err != nil {
		if mirror := consoleReleaseMirror(""); mirror != "" && mirror != base {
			if mSums, _, mErr := fetchReleaseManifest(mirror, 15*time.Second); mErr == nil {
				base = mirror
				sums = mSums
				err = nil
			}
		}
		if err != nil {
			return fmt.Errorf("fetch release manifest: %w", err)
		}
	}
	adminAsset := platformAdminAssetName()
	wantAdmin := sums[adminAsset]
	wantWeb := sums["web-dist.tar.gz"]

	adminChanged := wantAdmin != "" && fileSHA256(layout.binPath) != wantAdmin
	webChanged := wantWeb != "" && (!regularFileExists(filepath.Join(layout.webDist, "index.html")) || readWebDistSHA256(layout.webDist) != wantWeb)
	if !adminChanged && !webChanged {
		return nil
	}
	if adminChanged {
		_ = stopPIDFile(layout.apiPID)
		if err := os.MkdirAll(filepath.Dir(layout.binPath), 0o755); err != nil {
			return err
		}
		if err := downloadAssetWithExpectedSHA(base, adminAsset, wantAdmin, layout.binPath); err != nil {
			return err
		}
		*downloaded = append(*downloaded, adminAsset)
	}
	if webChanged {
		_ = stopPIDFile(layout.webPID)
		if err := downloadAndExtractWebDist(base, layout.root, layout.webDist, wantWeb); err != nil {
			return err
		}
		*downloaded = append(*downloaded, "web-dist.tar.gz")
	}
	return nil
}

// downloadVerifiedAsset fetches one release asset and installs it only after it
// matches the SHA256SUMS entry served next to it.
func downloadVerifiedAsset(base, asset, destination string) error {
	base = strings.TrimSuffix(base, "/")
	sums, err := httpGet(base + "/SHA256SUMS")
	if err != nil {
		return fmt.Errorf("fetch SHA256SUMS: %w", err)
	}
	want := ""
	for _, line := range strings.Split(string(sums), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && (fields[1] == asset || fields[1] == "*"+asset) {
			want = fields[0]
			break
		}
	}
	if want == "" {
		return fmt.Errorf("SHA256SUMS has no entry for %s", asset)
	}
	payload, err := httpGet(base + "/" + asset)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(payload)
	if got := hex.EncodeToString(digest[:]); got != want {
		return fmt.Errorf("SHA256 mismatch for %s (want %s, got %s)", asset, want, got)
	}
	return atomicWriteExecutable(destination, payload)
}

func httpGet(url string) ([]byte, error) {
	client := &http.Client{Timeout: 60 * time.Second}
	response, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d for %s", response.StatusCode, url)
	}
	return io.ReadAll(io.LimitReader(response.Body, 256<<20))
}

func extractTarGz(archive, destination string) error {
	handle, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer handle.Close()
	gzipReader, err := gzip.NewReader(handle)
	if err != nil {
		return err
	}
	defer gzipReader.Close()
	reader := tar.NewReader(gzipReader)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		cleaned := filepath.Clean(header.Name)
		if cleaned == "." || strings.HasPrefix(cleaned, "..") || filepath.IsAbs(cleaned) {
			continue
		}
		target := filepath.Join(destination, cleaned)
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			mode := os.FileMode(header.Mode & 0o777)
			if mode == 0 {
				mode = 0o644
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, io.LimitReader(reader, 256<<20)); err != nil {
				out.Close()
				return err
			}
			out.Close()
		}
	}
}

func copyTree(source, destination string) error {
	return filepath.Walk(source, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		payload, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, payload, info.Mode().Perm())
	})
}

func readPIDFile(path string) int {
	payload, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(payload)))
	if err != nil || pid <= 0 {
		return 0
	}
	return pid
}

// resolvePanelPID returns the live PID recorded in pidFile, or — when the HTTP
// probe confirmed the console port is serving but the pid file is missing/stale
// (e.g. started from source or another launcher) — discovers the listener PID
// on POSIX via lsof so `panel status` and `panel stop` stay consistent.
func resolvePanelPID(pidFile string, port int, verifiedRunning bool) int {
	if pid := readPIDFile(pidFile); pid > 0 && processAlive(pid) {
		return pid
	}
	if !verifiedRunning || port <= 0 || runtime.GOOS == "windows" {
		return 0
	}
	lsofBin, err := exec.LookPath("lsof")
	if err != nil {
		return 0
	}
	out, err := exec.Command(lsofBin, "-nP", fmt.Sprintf("-tiTCP:%d", port), "-sTCP:LISTEN").Output()
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if pid, pErr := strconv.Atoi(strings.TrimSpace(line)); pErr == nil && pid > 0 && processAlive(pid) {
			_ = os.MkdirAll(filepath.Dir(pidFile), 0o755)
			_ = os.WriteFile(pidFile, []byte(strconv.Itoa(pid)+"\n"), 0o644)
			return pid
		}
	}
	return 0
}

func processAlive(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	if signalErr := process.Signal(syscall.Signal(0)); signalErr != nil {
		if runtime.GOOS == "windows" {
			// Windows does not implement signal 0; a found handle is the best
			// liveness signal we have (Kill still works from that handle).
			return true
		}
		return false
	}
	return true
}

func waitProcessExit(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return !processAlive(pid)
}

// consoleReady probes the backend API.
func consoleReady(apiURL string) bool {
	return httpStatusOK(apiURL + "/admin/login-config")
}

// webAssetsReady probes the frontend: the web server answers the SPA root.
func webAssetsReady(webURL string) bool {
	client := &http.Client{Timeout: 2 * time.Second}
	response, err := client.Get(webURL + "/")
	if err != nil {
		return false
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
	return response.StatusCode == http.StatusOK
}

func httpStatusOK(url string) bool {
	client := &http.Client{Timeout: 2 * time.Second}
	response, err := client.Get(url)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
	return response.StatusCode == http.StatusOK
}

func waitHTTPReady(url string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if httpStatusOK(url) {
			return true
		}
		time.Sleep(300 * time.Millisecond)
	}
	return httpStatusOK(url)
}

// openBrowser opens the console URL with the platform opener; failure is a
// soft outcome (the address is still printed).
func openBrowser(url string) bool {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		command = exec.Command("open", url)
	case "windows":
		command = exec.Command("cmd", "/c", "start", "", url)
	default:
		command = exec.Command("xdg-open", url)
	}
	if err := command.Start(); err != nil {
		return false
	}
	_ = command.Process.Release()
	return true
}
