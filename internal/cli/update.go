// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/rocareer/rolling-memory/internal/mcp"
	"github.com/rocareer/rolling-memory/internal/version"
)

// The update command (aliased as `upgrade`) provides a friendly, Git-free
// self-update mechanism for end users. It fetches SHA256SUMS from the release
// channel, compares the SHA-256 digest of the installed CLI binary (and, when
// present or requested, the local web console backend + frontend assets),
// downloads and verifies any changed artifacts, atomically swaps them in place,
// refreshes host harness integrations, and restarts the console if it was
// running.
const webDistSHA256Marker = ".sha256"

func (a *application) runUpdate() int {
	checkOnly := a.args.Flag("check")
	force := a.args.Flag("force")
	withConsole := a.args.Flag("with_console")
	noConsole := a.args.Flag("no_console")
	noInstall := a.args.Flag("no_install")
	deferConsoleRestart := a.args.Flag("defer_console_restart")

	flagBase := a.args.String("release_base")
	base := resolveReleaseBase(flagBase)
	if base == "" {
		return a.fail(fmt.Errorf("release channel is disabled (ROLLING_MEMORY_RELEASE_BASE is off); unset it or pass --release-base <url> to update"))
	}

	sumsMap, remoteVersion, err := fetchReleaseManifest(base, 15*time.Second)
	if err != nil {
		if mirror := consoleReleaseMirror(flagBase); mirror != "" && mirror != base {
			if mSums, mVer, mErr := fetchReleaseManifest(mirror, 15*time.Second); mErr == nil {
				base = mirror
				sumsMap = mSums
				remoteVersion = mVer
				err = nil
			}
		}
		if err != nil {
			return a.fail(fmt.Errorf("fetch release manifest from %s: %w", base, err))
		}
	}

	cliAsset := platformCLIAssetName()
	wantCLISHA := sumsMap[cliAsset]
	if wantCLISHA == "" {
		return a.fail(fmt.Errorf("release manifest at %s has no entry for %s", base, cliAsset))
	}

	targetBin, linkPaths := resolveUpdateTargetPaths()
	localCLISHA := fileSHA256(targetBin)
	if localCLISHA == "" {
		if self, err := os.Executable(); err == nil {
			localCLISHA = fileSHA256(self)
		}
	}
	cliNeedsUpdate := force || localCLISHA == "" || localCLISHA != wantCLISHA

	// Console assets are checked if the console is already installed on disk or
	// if --with-console was explicitly requested (unless --no-console is set).
	cRoot, cBin, cWebDist, cAPIPID, cWebPID, cAPILog, cWebLog := consoleLayout()
	consoleInstalled := regularFileExists(cBin) || regularFileExists(filepath.Join(cWebDist, "index.html"))
	checkConsole := !noConsole && (consoleInstalled || withConsole)

	adminAsset := platformAdminAssetName()
	wantAdminSHA := sumsMap[adminAsset]
	wantWebSHA := sumsMap["web-dist.tar.gz"]
	localAdminSHA := fileSHA256(cBin)
	localWebSHA := readWebDistSHA256(cWebDist)

	adminNeedsUpdate := false
	webNeedsUpdate := false
	if checkConsole {
		if wantAdminSHA != "" {
			adminNeedsUpdate = force || localAdminSHA == "" || localAdminSHA != wantAdminSHA
		}
		if wantWebSHA != "" {
			webNeedsUpdate = force || !regularFileExists(filepath.Join(cWebDist, "index.html")) || localWebSHA != wantWebSHA
		}
	}

	anyUpdate := cliNeedsUpdate || adminNeedsUpdate || webNeedsUpdate

	if checkOnly {
		result := map[string]any{
			"ok":               true,
			"action":           "check",
			"current_version":  version.Version,
			"latest_version":   remoteVersion,
			"release_base":     base,
			"update_available": anyUpdate,
			"cli": map[string]any{
				"asset":            cliAsset,
				"path":             targetBin,
				"local_sha256":     localCLISHA,
				"remote_sha256":    wantCLISHA,
				"update_available": cliNeedsUpdate,
			},
			"console": map[string]any{
				"installed":        consoleInstalled,
				"checked":          checkConsole,
				"admin_asset":      adminAsset,
				"admin_local_sha":  localAdminSHA,
				"admin_remote_sha": wantAdminSHA,
				"admin_update":     adminNeedsUpdate,
				"web_local_sha":    localWebSHA,
				"web_remote_sha":   wantWebSHA,
				"web_update":       webNeedsUpdate,
			},
		}
		human := func() {
			verLabel := version.Version
			if remoteVersion != "" && remoteVersion != version.Version {
				verLabel = fmt.Sprintf("%s -> %s", version.Version, remoteVersion)
			}
			if !anyUpdate {
				a.printf("up to date · version %s (%s)\n", verLabel, shortSHA(wantCLISHA))
				return
			}
			a.printf("update available · version %s\n", verLabel)
			if cliNeedsUpdate {
				a.printf("  cli      %s: %s -> %s\n", cliAsset, shortSHA(localCLISHA), shortSHA(wantCLISHA))
			} else {
				a.printf("  cli      %s: up to date (%s)\n", cliAsset, shortSHA(localCLISHA))
			}
			if checkConsole {
				if adminNeedsUpdate {
					a.printf("  console  %s: %s -> %s\n", adminAsset, shortSHA(localAdminSHA), shortSHA(wantAdminSHA))
				}
				if webNeedsUpdate {
					a.printf("  console  web-dist.tar.gz: %s -> %s\n", shortSHA(localWebSHA), shortSHA(wantWebSHA))
				}
			}
			a.printf("run `rolling-memory update` to install\n")
		}
		return emitOrFail(a, result, human)
	}

	updatedComponents := []string{}
	daemonRefreshed := false

	if cliNeedsUpdate {
		if err := os.MkdirAll(filepath.Dir(targetBin), 0o755); err != nil {
			return a.fail(fmt.Errorf("create binary directory: %w", err))
		}
		if err := downloadAssetWithExpectedSHA(base, cliAsset, wantCLISHA, targetBin); err != nil {
			return a.fail(fmt.Errorf("update %s: %w", cliAsset, err))
		}
		ensureBinaryLinks(targetBin, linkPaths)
		mcp.ShutdownUnixDaemon("")
		daemonRefreshed = true
		updatedComponents = append(updatedComponents, cliAsset)
	}

	consoleRestarted := false
	consoleRestartDeferred := false
	if checkConsole && (adminNeedsUpdate || webNeedsUpdate) {
		apiPort := panelPort(nil, "RM_PANEL_API_PORT", defaultConsoleAPIPort)
		webPort := panelPort(nil, "RM_PANEL_WEB_PORT", defaultConsoleWebPort)
		layout := panelLayout{
			root: cRoot, binPath: cBin, webDist: cWebDist,
			apiPID: cAPIPID, webPID: cWebPID, apiLog: cAPILog, webLog: cWebLog,
			apiPort: apiPort, webPort: webPort,
		}
		wasRunning := consoleReady(layout.apiURL()) || webAssetsReady(layout.webURL()) ||
			readPIDFile(layout.apiPID) != 0 || readPIDFile(layout.webPID) != 0
		if wasRunning && !deferConsoleRestart {
			_ = stopPIDFile(layout.apiPID)
			_ = stopPIDFile(layout.webPID)
		}
		if adminNeedsUpdate {
			if err := os.MkdirAll(filepath.Dir(cBin), 0o755); err != nil {
				return a.fail(err)
			}
			if err := downloadAssetWithExpectedSHA(base, adminAsset, wantAdminSHA, cBin); err != nil {
				return a.fail(fmt.Errorf("update %s: %w", adminAsset, err))
			}
			updatedComponents = append(updatedComponents, adminAsset)
		}
		if webNeedsUpdate {
			if err := downloadAndExtractWebDist(base, cRoot, cWebDist, wantWebSHA); err != nil {
				return a.fail(fmt.Errorf("update web-dist.tar.gz: %w", err))
			}
			updatedComponents = append(updatedComponents, "web-dist.tar.gz")
		}
		if wasRunning {
			if deferConsoleRestart {
				consoleRestartDeferred = true
			} else if _, err := startConsoleAPI(layout, cBin); err == nil {
				_, _ = startConsoleWeb(layout)
				_ = waitHTTPReady(layout.apiURL()+"/admin/login-config", 15*time.Second)
				_ = waitHTTPReady(layout.webURL()+"/", 10*time.Second)
				consoleRestarted = true
			}
		}
	}

	harnessRefreshed := false
	if cliNeedsUpdate && !noInstall {
		cmd := exec.Command(targetBin, "install", "--with-hooks")
		if err := cmd.Run(); err == nil {
			harnessRefreshed = true
		}
	}

	desktopBundlesSynced := syncDesktopAppBundles(targetBin, cBin, wantWebSHA, remoteVersion, len(updatedComponents) > 0)

	result := map[string]any{
		"ok":                       true,
		"action":                   "update",
		"updated":                  len(updatedComponents) > 0,
		"components":               updatedComponents,
		"previous_version":         version.Version,
		"latest_version":           remoteVersion,
		"release_base":             base,
		"binary":                   targetBin,
		"cli_sha256":               wantCLISHA,
		"daemon_refreshed":         daemonRefreshed,
		"harness_refreshed":        harnessRefreshed,
		"console_restarted":        consoleRestarted,
		"console_restart_deferred": consoleRestartDeferred,
		"desktop_bundles_synced":   desktopBundlesSynced,
	}
	human := func() {
		if len(updatedComponents) == 0 {
			a.printf("already up to date · version %s (%s)\n", version.Version, shortSHA(wantCLISHA))
			return
		}
		verLabel := version.Version
		if remoteVersion != "" {
			verLabel = remoteVersion
		}
		a.printf("updated · version %s (%s)\n", verLabel, strings.Join(updatedComponents, ", "))
		a.printf("  binary: %s (%s)\n", targetBin, shortSHA(wantCLISHA))
		if harnessRefreshed {
			a.printf("  harness integrations refreshed (`install --with-hooks`)\n")
		}
		if consoleRestarted {
			a.printf("  web console restarted (backend :9006 + web frontend :8008)\n")
		}
	}
	return emitOrFail(a, result, human)
}

func platformCLIAssetName() string {
	asset := "rolling-memory-" + runtime.GOOS + "-" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		asset += ".exe"
	}
	return asset
}

func platformAdminAssetName() string {
	asset := "rolling-admin-" + runtime.GOOS + "-" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		asset += ".exe"
	}
	return asset
}

// resolveUpdateTargetPaths returns the canonical binary path to update and any
// symlink locations (`~/.local/bin/rolling-memory`, `rmem`) to keep aligned.
func resolveUpdateTargetPaths() (string, []string) {
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	home, _ := os.UserHomeDir()
	installDir := strings.TrimSpace(os.Getenv("ROLLING_MEMORY_INSTALL_DIR"))
	if installDir == "" {
		if runtime.GOOS == "windows" {
			if localAppData := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); localAppData != "" {
				installDir = filepath.Join(localAppData, "rolling-memory", "bin")
			}
		}
		if installDir == "" && home != "" {
			installDir = filepath.Join(home, ".local", "share", "rolling-memory", "bin")
		}
	}
	targetBin := filepath.Join(installDir, "rolling-memory"+ext)
	if !regularFileExists(targetBin) {
		if self, err := os.Executable(); err == nil {
			if resolved, err := filepath.EvalSymlinks(self); err == nil && regularFileExists(resolved) {
				if installDir == "" {
					targetBin = resolved
				}
			}
		}
	}
	linkDir := strings.TrimSpace(os.Getenv("ROLLING_MEMORY_LINK_DIR"))
	if linkDir == "" && home != "" && runtime.GOOS != "windows" {
		linkDir = filepath.Join(home, ".local", "bin")
	}
	var links []string
	if linkDir != "" && runtime.GOOS != "windows" {
		links = []string{
			filepath.Join(linkDir, "rolling-memory"),
			filepath.Join(linkDir, "rmem"),
		}
	}
	return targetBin, links
}

func ensureBinaryLinks(targetBin string, linkPaths []string) {
	for _, link := range linkPaths {
		if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
			continue
		}
		_ = os.Remove(link)
		_ = os.Symlink(targetBin, link)
	}
}

// fetchReleaseManifest downloads SHA256SUMS (and extracts the version from
// archive filenames like `rolling-memory-<ver>-<os>-<arch>.tar.gz` or an
// optional `VERSION` file).
func fetchReleaseManifest(base string, timeout time.Duration) (map[string]string, string, error) {
	base = strings.TrimSuffix(base, "/")
	raw, err := httpGetWithTimeout(base+"/SHA256SUMS", timeout)
	if err != nil {
		return nil, "", err
	}
	sums, detectedVersion := parseSHA256Sums(string(raw))
	if len(sums) == 0 {
		return nil, "", fmt.Errorf("empty or invalid SHA256SUMS")
	}
	if detectedVersion == "" {
		if verBytes, err := httpGetWithTimeout(base+"/VERSION", 3*time.Second); err == nil {
			detectedVersion = strings.TrimSpace(string(verBytes))
		}
	}
	return sums, detectedVersion, nil
}

// parseSHA256Sums parses a standard SHA256SUMS file into an asset->sha256 map
// and extracts the release version if a versioned archive name is present.
func parseSHA256Sums(content string) (map[string]string, string) {
	sums := make(map[string]string)
	detectedVersion := ""
	suffix := "-" + runtime.GOOS + "-" + runtime.GOARCH + ".tar.gz"
	for _, line := range strings.Split(content, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		sha := strings.ToLower(strings.TrimSpace(fields[0]))
		name := strings.TrimPrefix(strings.TrimSpace(fields[1]), "*")
		if len(sha) != 64 {
			continue
		}
		sums[name] = sha
		if strings.HasPrefix(name, "rolling-memory-") && strings.HasSuffix(name, suffix) {
			ver := strings.TrimSuffix(strings.TrimPrefix(name, "rolling-memory-"), suffix)
			if ver != "" && ver != runtime.GOOS {
				detectedVersion = ver
			}
		}
	}
	return sums, detectedVersion
}

func httpGetWithTimeout(url string, timeout time.Duration) ([]byte, error) {
	client := &http.Client{Timeout: timeout}
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

// downloadAssetWithExpectedSHA downloads one asset, verifies its SHA-256 against
// wantSHA, and atomically replaces destination (handling Windows self-replace).
func downloadAssetWithExpectedSHA(base, asset, wantSHA, destination string) error {
	base = strings.TrimSuffix(base, "/")
	payload, err := httpGetWithTimeout(base+"/"+asset, 120*time.Second)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(payload)
	if got := hex.EncodeToString(digest[:]); !strings.EqualFold(got, wantSHA) {
		return fmt.Errorf("SHA256 mismatch for %s (want %s, got %s)", asset, wantSHA, got)
	}
	return atomicWriteExecutable(destination, payload)
}

func atomicWriteExecutable(destination string, payload []byte) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	temporary := destination + ".dl." + strconv.Itoa(os.Getpid())
	handle, err := os.OpenFile(temporary, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := handle.Write(payload); err != nil {
		handle.Close()
		_ = os.Remove(temporary)
		return err
	}
	if err := handle.Sync(); err != nil {
		handle.Close()
		_ = os.Remove(temporary)
		return err
	}
	if err := handle.Close(); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	// Windows cannot replace a running executable in place, so the old binary is
	// moved aside first; it is kept until the new payload has landed because the
	// swap must never leave the user with no binary at all.
	oldPath := ""
	if runtime.GOOS == "windows" && regularFileExists(destination) {
		oldPath = destination + ".old"
		_ = os.Remove(oldPath)
		if err := os.Rename(destination, oldPath); err != nil {
			_ = os.Remove(temporary)
			return fmt.Errorf("cannot release %s for replacement: %w", filepath.Base(destination), err)
		}
	}
	if err := os.Rename(temporary, destination); err != nil {
		_ = os.Remove(temporary)
		if oldPath != "" {
			if restoreErr := os.Rename(oldPath, destination); restoreErr != nil {
				return fmt.Errorf("%w (the previous binary was kept at %s)", err, oldPath)
			}
		}
		return err
	}
	if oldPath != "" {
		_ = os.Remove(oldPath)
	}
	stripDarwinQuarantine(destination, false)
	return nil
}

// stripDarwinQuarantine removes the macOS Gatekeeper com.apple.quarantine
// extended attribute from a downloaded binary or .app bundle so macOS does not
// block execution with "Apple could not verify Rolling-Memory is free of malware".
func stripDarwinQuarantine(target string, recursive bool) {
	if runtime.GOOS != "darwin" || strings.TrimSpace(target) == "" {
		return
	}
	xattrBin, err := exec.LookPath("xattr")
	if err != nil {
		return
	}
	flag := "-d"
	if recursive {
		flag = "-dr"
	}
	_ = exec.Command(xattrBin, flag, "com.apple.quarantine", target).Run()
}

// resignDarwinAppBundle refreshes the ad-hoc signature of a macOS .app bundle
// after its embedded payload binaries or Info.plist have been updated in place,
// preventing macOS AMFI / Gatekeeper from rejecting the modified bundle with
// "invalid Info.plist (plist or signature have been modified)".
func resignDarwinAppBundle(appDir string) {
	if runtime.GOOS != "darwin" || strings.TrimSpace(appDir) == "" {
		return
	}
	stripDarwinQuarantine(appDir, true)
	codesignBin, err := exec.LookPath("codesign")
	if err != nil {
		return
	}
	_ = exec.Command(codesignBin, "--force", "--deep", "--sign", "-", appDir).Run()
}

// syncDesktopAppBundles keeps any installed macOS Rolling-Memory.app bundle in
// sync after an online `rolling-memory update` so subsequent launches of the
// desktop app never revert the updated CLI/console binaries or show a stale
// version in the menu bar. It also strips com.apple.quarantine and refreshes
// the bundle's ad-hoc code signature whenever bundle contents change.
func syncDesktopAppBundles(cliBin, adminBin, webSHA, newVersion string, allowPayloadSync bool) []string {
	var candidates []string
	if envApp := strings.TrimSpace(os.Getenv("ROLLING_MEMORY_DESKTOP_APP")); envApp != "" {
		if strings.EqualFold(envApp, "off") || strings.EqualFold(envApp, "none") {
			return nil
		}
		candidates = append(candidates, envApp)
	} else if runtime.GOOS == "darwin" {
		candidates = append(candidates, "/Applications/Rolling-Memory.app")
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			candidates = append(candidates, filepath.Join(home, "Applications", "Rolling-Memory.app"))
		}
	}
	seen := map[string]bool{}
	var synced []string
	for _, appDir := range candidates {
		clean := filepath.Clean(appDir)
		if clean == "" || seen[clean] {
			continue
		}
		seen[clean] = true
		payloadDir := filepath.Join(clean, "Contents", "Resources", "payload")
		info, err := os.Stat(payloadDir)
		if err != nil || !info.IsDir() {
			continue
		}
		stripDarwinQuarantine(clean, true)
		if !allowPayloadSync {
			continue
		}
		updatedAny := false
		if regularFileExists(cliBin) {
			if raw, err := os.ReadFile(cliBin); err == nil {
				if atomicWriteExecutable(filepath.Join(payloadDir, "rolling-memory"), raw) == nil {
					updatedAny = true
				}
			}
		}
		if regularFileExists(adminBin) {
			if raw, err := os.ReadFile(adminBin); err == nil {
				if atomicWriteExecutable(filepath.Join(payloadDir, "rolling-admin"), raw) == nil {
					updatedAny = true
				}
			}
		}
		if webSHA != "" {
			_ = os.WriteFile(filepath.Join(payloadDir, "web-dist.sha256"), []byte(webSHA+"\n"), 0o644)
		}
		if newVersion != "" {
			_ = os.WriteFile(filepath.Join(payloadDir, "VERSION"), []byte(newVersion+"\n"), 0o644)
			plistPath := filepath.Join(clean, "Contents", "Info.plist")
			if rawPlist, err := os.ReadFile(plistPath); err == nil {
				updatedPlist := updatePlistVersionStrings(string(rawPlist), newVersion)
				if updatedPlist != string(rawPlist) {
					_ = os.WriteFile(plistPath, []byte(updatedPlist), 0o644)
					updatedAny = true
				}
			}
		}
		if updatedAny {
			resignDarwinAppBundle(clean)
			synced = append(synced, clean)
		}
	}
	return synced
}

func updatePlistVersionStrings(plistContent, newVersion string) string {
	lines := strings.Split(plistContent, "\n")
	for i := 0; i+1 < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "<key>CFBundleShortVersionString</key>" || trimmed == "<key>CFBundleVersion</key>" {
			indentIdx := strings.Index(lines[i+1], "<string>")
			if indentIdx >= 0 {
				prefix := lines[i+1][:indentIdx]
				lines[i+1] = prefix + "<string>" + newVersion + "</string>"
			}
		}
	}
	return strings.Join(lines, "\n")
}

func fileSHA256(path string) string {
	handle, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer handle.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, handle); err != nil {
		return ""
	}
	return hex.EncodeToString(hasher.Sum(nil))
}

func readWebDistSHA256(webDist string) string {
	data, err := os.ReadFile(filepath.Join(webDist, webDistSHA256Marker))
	if err != nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(string(data)))
}

func writeWebDistSHA256(webDist, sha string) {
	if sha == "" {
		return
	}
	_ = os.WriteFile(filepath.Join(webDist, webDistSHA256Marker), []byte(strings.ToLower(strings.TrimSpace(sha))+"\n"), 0o644)
}

func shortSHA(sha string) string {
	if sha == "" {
		return "none"
	}
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
