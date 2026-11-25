// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package cli

import (
	"context"
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

	"github.com/rocareer/rolling-memory/internal/config"
	"github.com/rocareer/rolling-memory/internal/mcp"
	"github.com/rocareer/rolling-memory/internal/updatechannel"
	"github.com/rocareer/rolling-memory/internal/updatecheck"
	"github.com/rocareer/rolling-memory/internal/updatesource"
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
	autoRun := a.args.Flag("auto")
	force := a.args.Flag("force")
	tagsOnly := a.args.Flag("tags_only")
	quiet := a.args.Flag("quiet")
	notify := a.args.Flag("notify")
	withConsole := a.args.Flag("with_console")
	noConsole := a.args.Flag("no_console")
	noInstall := a.args.Flag("no_install")
	deferConsoleRestart := a.args.Flag("defer_console_restart")

	if schedule := strings.TrimSpace(a.args.String("schedule")); schedule != "" {
		return a.runUpdateSchedule(schedule)
	}

	flagBase := a.args.String("release_base")
	if source := strings.TrimSpace(a.args.String("source")); source != "" {
		normalized, err := updatesource.Normalize(source)
		if err != nil {
			return a.fail(err)
		}
		if strings.TrimSpace(flagBase) == "" {
			flagBase = updatesource.ReleaseBase(normalized)
		}
	}
	channel := updatecheck.ResolveChannel(a.args.String("channel"))
	base := resolveReleaseBase(flagBase)
	autoMode := ""
	if autoRun && !checkOnly && !tagsOnly {
		if !updatecheck.AutoCheckEnabled() {
			autoMode = updatechannel.AutoOff
		} else {
			autoMode = resolveAutoMode()
		}
		if autoMode == updatechannel.AutoOff {
			result := map[string]any{
				"ok": true, "action": "auto", "auto": autoMode, "updated": false,
				"skipped": "automatic updates are off",
			}
			human := func() { a.printf("automatic updates are off\n") }
			if quiet {
				return 0
			}
			return emitOrFail(a, result, human)
		}
	}
	if base == "" && channel != "dev" {
		return a.fail(fmt.Errorf("release channel is disabled (ROLLING_MEMORY_RELEASE_BASE is off); unset it or pass --release-base <url> to update"))
	}

	checked, checkErr := updatecheck.Check(context.Background(), updatecheck.Options{
		ReleaseBase: base,
		Channel:     channel,
		TagsOnly:    tagsOnly,
		Timeout:     15 * time.Second,
	})
	if checkErr != nil && checked.LatestTag == "" {
		if mirror := consoleReleaseMirror(flagBase); mirror != "" && mirror != base {
			if retry, retryErr := updatecheck.Check(context.Background(), updatecheck.Options{
				ReleaseBase: mirror,
				Channel:     channel,
				TagsOnly:    tagsOnly,
				Timeout:     15 * time.Second,
			}); retryErr == nil || retry.LatestTag != "" {
				base = mirror
				checked = retry
				checkErr = retryErr
			}
		}
		if checkErr != nil && checked.LatestTag == "" {
			return a.fail(fmt.Errorf("check latest version from %s: %w", base, checkErr))
		}
	}

	// A Gitee tag can exist before its release artifacts are attached. When
	// the selected mirror has no installable manifest, try the official
	// GitHub release for the same channel; a reachable fallback is better than
	// telling the user to rebuild from source.
	assetFallback := ""
	if !tagsOnly && channel != "dev" && !checked.Installable && updatesource.IsGiteeBase(base) {
		githubBase := updatesource.ReleaseBase(updatesource.GitHub)
		if retry, retryErr := updatecheck.Check(context.Background(), updatecheck.Options{
			ReleaseBase: githubBase,
			Channel:     channel,
			Timeout:     15 * time.Second,
		}); retryErr == nil && retry.Installable {
			base = githubBase
			checked = retry
			checkErr = nil
			assetFallback = updatesource.GitHub
		}
	}

	remoteVersion := checked.LatestTag
	assetBase := checked.AssetBase
	if strings.TrimSpace(assetBase) == "" {
		assetBase = base
	}
	cliAsset := platformCLIAssetName()
	wantCLISHA := checked.Sums[cliAsset]

	targetBin, linkPaths := resolveUpdateTargetPaths()
	localCLISHA := fileSHA256(targetBin)
	if localCLISHA == "" {
		if self, err := os.Executable(); err == nil {
			localCLISHA = fileSHA256(self)
		}
	}

	cRoot, cBin, cWebDist, cAPIPID, cWebPID, cAPILog, cWebLog := consoleLayout()
	consoleInstalled := regularFileExists(cBin) || regularFileExists(filepath.Join(cWebDist, "index.html"))
	checkConsole := !tagsOnly && !noConsole && (consoleInstalled || withConsole)

	adminAsset := platformAdminAssetName()
	wantAdminSHA := checked.Sums[adminAsset]
	wantWebSHA := checked.Sums["web-dist.tar.gz"]
	localAdminSHA := fileSHA256(cBin)
	localWebSHA := readWebDistSHA256(cWebDist)

	cliNeedsUpdate := false
	adminNeedsUpdate := false
	webNeedsUpdate := false
	if !checked.DowngradeAvailable {
		cliNeedsUpdate = wantCLISHA != "" && (force || localCLISHA == "" || localCLISHA != wantCLISHA)
		if checkConsole {
			if wantAdminSHA != "" {
				adminNeedsUpdate = force || localAdminSHA == "" || localAdminSHA != wantAdminSHA
			}
			if wantWebSHA != "" {
				webNeedsUpdate = force || !regularFileExists(filepath.Join(cWebDist, "index.html")) || localWebSHA != wantWebSHA
			}
		}
	}
	anyUpdate := checked.UpdateAvailable
	if !tagsOnly && !checked.DowngradeAvailable {
		anyUpdate = anyUpdate || cliNeedsUpdate || adminNeedsUpdate || webNeedsUpdate
	}

	latestLabel := remoteVersion
	if checked.LatestCommitShort != "" {
		latestLabel = "main@" + checked.LatestCommitShort
	}
	if checkOnly {
		result := map[string]any{
			"ok":                    true,
			"action":                "check",
			"current_version":       version.Version,
			"channel":               channel,
			"latest_tag":            remoteVersion,
			"latest_release":        checked.LatestRelease,
			"latest_commit":         checked.LatestCommit,
			"latest_commit_short":   checked.LatestCommitShort,
			"installed_commit":      checked.InstalledCommit,
			"release_base":          base,
			"update_source":         sourceFromBase(base),
			"asset_fallback":        assetFallback,
			"update_available":      anyUpdate,
			"downgrade_available":   checked.DowngradeAvailable,
			"assets_available":      checked.AssetsAvailable,
			"assets_checked":        checked.AssetsChecked,
			"installable":           checked.Installable && !checked.DowngradeAvailable,
			"needs_source_baseline": checked.NeedsSourceBaseline,
			"install_method":        checked.InstallMethod,
			"source_update":         sourceUpdateHint(checked.InstallMethod, remoteVersion, channel, base),
			"checked_at":            checked.CheckedAt,
			"source":                checked.Source,
			"tags_only":             tagsOnly,
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
		if checked.Error != "" {
			result["error"] = checked.Error
		}
		human := func() {
			if tagsOnly {
				if checked.UpdateAvailable {
					a.printf("update available · version %s -> %s\n", version.Version, latestLabel)
				} else if checked.DowngradeAvailable {
					a.printf("local version %s is newer than channel %s\n", version.Version, remoteVersion)
				} else {
					a.printf("up to date · version %s\n", latestLabel)
				}
				return
			}
			if checked.NeedsSourceBaseline {
				a.printf("dev channel baseline needed · current %s\n", version.Version)
				if hint := sourceUpdateHint(checked.InstallMethod, remoteVersion, channel, base); hint != "" {
					a.printf("  %s\n", hint)
				}
				return
			}
			if checked.DowngradeAvailable {
				a.printf("local version %s is newer than channel %s (use --force to downgrade)\n", version.Version, remoteVersion)
				return
			}
			if !anyUpdate {
				a.printf("up to date · version %s (%s)\n", version.Version, shortSHA(wantCLISHA))
				return
			}
			a.printf("update available · version %s -> %s\n", version.Version, latestLabel)
			if !checked.Installable {
				a.printf("  release assets are unavailable for %s; source installs must update from the tag\n", cliAsset)
				if hint := sourceUpdateHint(checked.InstallMethod, remoteVersion, channel, base); hint != "" {
					a.printf("  %s\n", hint)
				}
			}
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
		if notify && checked.UpdateAvailable && !checked.DowngradeAvailable {
			_ = updatecheck.NotifyDesktop(checked.State)
		}
		if quiet {
			if ok, _ := result["ok"].(bool); ok {
				return 0
			}
			return 1
		}
		return emitOrFail(a, result, human)
	}

	// Only one process may replace the installed artifacts at a time. The
	// scheduled policy and a manual click can race; the loser exits before
	// touching the console or the binary.
	unlock, lockErr := acquireUpdateLock()
	if lockErr != nil {
		if autoMode != "" {
			result := map[string]any{
				"ok": true, "action": "auto", "auto": autoMode, "updated": false,
				"skipped": lockErr.Error(),
			}
			if quiet {
				return 0
			}
			return emitOrFail(a, result, func() { a.printf("skipped: %s\n", lockErr) })
		}
		return a.fail(lockErr)
	}
	defer unlock()

	if autoMode != "" {
		// A background run never fails just because the channel has no
		// installable artifact: it records the state and leaves a notification
		// (or the console entry) to the user.
		if !checked.Installable || checked.DowngradeAvailable {
			notified := checked.UpdateAvailable && !checked.DowngradeAvailable
			if notified {
				_ = updatecheck.NotifyDesktop(checked.State)
			}
			result := map[string]any{
				"ok":                  true,
				"action":              "auto",
				"auto":                autoMode,
				"updated":             false,
				"update_available":    checked.UpdateAvailable,
				"installable":         checked.Installable,
				"downgrade_available": checked.DowngradeAvailable,
				"notified":            notified,
				"latest_tag":          remoteVersion,
				"latest_commit_short": checked.LatestCommitShort,
				"install_method":      checked.InstallMethod,
			}
			human := func() {
				switch {
				case checked.DowngradeAvailable:
					a.printf("local version is newer than channel %s\n", remoteVersion)
				case checked.UpdateAvailable && !checked.Installable:
					a.printf("update available · version %s -> %s (no installable artifacts; source update required)\n", version.Version, latestLabel)
				default:
					a.printf("up to date · version %s\n", version.Version)
				}
			}
			if quiet {
				return 0
			}
			return emitOrFail(a, result, human)
		}
		if autoMode == updatechannel.AutoNotify {
			notified := checked.UpdateAvailable && !checked.DowngradeAvailable
			if notified {
				_ = updatecheck.NotifyDesktop(checked.State)
			}
			result := map[string]any{
				"ok":               true,
				"action":           "auto",
				"auto":             autoMode,
				"updated":          false,
				"update_available": checked.UpdateAvailable,
				"installable":      checked.Installable,
				"notified":         notified,
				"latest_tag":       remoteVersion,
			}
			human := func() {
				if notified {
					a.printf("update available · version %s -> %s (notification sent)\n", version.Version, latestLabel)
					return
				}
				a.printf("up to date · version %s\n", version.Version)
			}
			if quiet {
				return 0
			}
			return emitOrFail(a, result, human)
		}
		// autoMode == apply falls through to the verified install below.
	}

	if channel == "dev" {
		hint := sourceUpdateHint(checked.InstallMethod, remoteVersion, channel, base)
		if hint == "" {
			hint = "set ROLLING_MEMORY_SOURCE_REF=main and run install.sh"
		}
		return a.fail(fmt.Errorf("dev channel tracks main and requires a source rebuild; %s", hint))
	}
	if checked.DowngradeAvailable && !force {
		return a.fail(fmt.Errorf("refusing to downgrade from %s to %s; pass --force to replace the local build", version.Version, remoteVersion))
	}
	if wantCLISHA == "" {
		return a.fail(fmt.Errorf("release manifest at %s has no entry for %s; this version has no installable artifacts", base, cliAsset))
	}

	updatedComponents := []string{}
	daemonRefreshed := false

	if cliNeedsUpdate {
		if err := os.MkdirAll(filepath.Dir(targetBin), 0o755); err != nil {
			return a.fail(fmt.Errorf("create binary directory: %w", err))
		}
		backupInstalledFile(targetBin, targetBin+".prev")
		if err := downloadAssetWithExpectedSHA(assetBase, cliAsset, wantCLISHA, targetBin); err != nil {
			return a.fail(fmt.Errorf("update %s: %w", cliAsset, err))
		}
		ensureBinaryLinks(targetBin, linkPaths)
		daemonRefreshed = mcp.ShutdownUnixDaemon("") == nil
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
		// Download and verify before stopping a healthy console. A failed
		// download must leave the user with a working console, not a stopped
		// one and a half-written asset directory.
		if adminNeedsUpdate {
			if err := os.MkdirAll(filepath.Dir(cBin), 0o755); err != nil {
				return a.fail(err)
			}
			backupInstalledFile(cBin, cBin+".prev")
			if err := downloadAssetWithExpectedSHA(assetBase, adminAsset, wantAdminSHA, cBin); err != nil {
				return a.fail(fmt.Errorf("update %s: %w", adminAsset, err))
			}
			updatedComponents = append(updatedComponents, adminAsset)
		}
		if webNeedsUpdate {
			if err := downloadAndExtractWebDist(assetBase, cRoot, cWebDist, wantWebSHA); err != nil {
				return a.fail(fmt.Errorf("update web-dist.tar.gz: %w", err))
			}
			updatedComponents = append(updatedComponents, "web-dist.tar.gz")
		}
		if wasRunning && !deferConsoleRestart {
			_ = stopPIDFile(layout.apiPID)
			_ = stopPIDFile(layout.webPID)
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
		cmd := exec.Command(targetBin, "install")
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
		"latest_tag":               remoteVersion,
		"release_base":             base,
		"asset_fallback":           assetFallback,
		"binary":                   targetBin,
		"cli_sha256":               wantCLISHA,
		"install_method":           checked.InstallMethod,
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
			a.printf("  harness integrations refreshed (`install`; hooks unchanged)\n")
		}
		if consoleRestarted {
			a.printf("  web console restarted (backend :9006 + web frontend :8008)\n")
		}
	}
	return emitOrFail(a, result, human)
}

// attachUpdateHint adds the cached update summary to read-only status receipts
// without performing a network request.
func attachUpdateHint(result map[string]any) map[string]any {
	if hint := updatecheck.Hint(); hint != nil {
		result["update"] = hint
	}
	return result
}

// acquireUpdateLock serializes artifact replacement across a manual click and
// the scheduled policy. A lock whose owner is gone is reclaimed so a crash or
// an interrupted console restart does not block updates for thirty minutes.
func acquireUpdateLock() (func(), error) {
	path := filepath.Join(filepath.Dir(updatecheck.StatePath()), "update.lock")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	handle, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if updateLockAbandoned(path) {
			_ = os.Remove(path)
			handle, err = os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		}
		if err != nil {
			return nil, fmt.Errorf("another update is already running")
		}
	}
	_, _ = fmt.Fprintf(handle, "%d\n", os.Getpid())
	_ = handle.Close()
	return func() { _ = os.Remove(path) }, nil
}

// updateLockAbandoned reports whether an existing update.lock no longer has a
// live owner and can be reclaimed. A lock older than thirty minutes is always
// reclaimable. A younger lock is reclaimable only when it records a positive
// PID that no longer refers to a running process; an unreadable or empty lock
// is treated as live so we never race a writer that has not yet recorded its
// PID.
func updateLockAbandoned(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		// The lock vanished between the failed exclusive create and this check.
		return true
	}
	if time.Since(info.ModTime()) > 30*time.Minute {
		return true
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 {
		return false
	}
	return !processAlive(pid)
}

// resolveAutoMode reads the saved background policy. An unreadable or invalid
// config falls back to the safe default (notify, never install silently).
func resolveAutoMode() string {
	if cfg, err := config.Load(config.LoadOptions{}); err == nil {
		if mode, err := updatechannel.NormalizeAuto(cfg.Update.Auto); err == nil {
			return mode
		}
	}
	return updatechannel.AutoDefault
}

func sourceFromBase(base string) string {
	if updatesource.IsGiteeBase(base) {
		return updatesource.Gitee
	}
	return updatesource.GitHub
}

func sourceUpdateHint(method, version, channel, base string) string {
	version = strings.TrimSpace(version)
	if version == "" {
		return ""
	}
	gitee := updatesource.IsGiteeBase(base)
	ref := "v" + version
	repoURL := updatesource.RepoURL(updatesource.GitHub)
	if gitee {
		repoURL = updatesource.RepoURL(updatesource.Gitee)
	}
	repoEnv := "ROLLING_MEMORY_REPO_URL=" + repoURL + " "
	if gitee {
		if strings.EqualFold(strings.TrimSpace(channel), "dev") {
			if runtime.GOOS == "windows" {
				return fmt.Sprintf("source update: $env:ROLLING_MEMORY_REPO_URL='%s'; powershell -ExecutionPolicy Bypass -File install.ps1 --ref main", repoURL)
			}
			return "source update: " + repoEnv + "ROLLING_MEMORY_SOURCE_REF=main bash install.sh"
		}
		if runtime.GOOS == "windows" {
			return fmt.Sprintf("source update: $env:ROLLING_MEMORY_REPO_URL='%s'; powershell -ExecutionPolicy Bypass -File install.ps1 --ref %s", repoURL, ref)
		}
		return fmt.Sprintf("source update: %sROLLING_MEMORY_SOURCE_REF=%s bash install.sh", repoEnv, ref)
	}
	if strings.EqualFold(strings.TrimSpace(channel), "dev") {
		if runtime.GOOS == "windows" {
			return "source update: powershell -ExecutionPolicy Bypass -File install.ps1 --ref main"
		}
		return "source update: ROLLING_MEMORY_SOURCE_REF=main bash install.sh"
	}
	switch strings.ToLower(strings.TrimSpace(method)) {
	case "source", "local":
		if runtime.GOOS == "windows" {
			return fmt.Sprintf("source update: powershell -ExecutionPolicy Bypass -File install.ps1 --ref %s", ref)
		}
		return fmt.Sprintf("source update: %sROLLING_MEMORY_SOURCE_REF=%s bash install.sh", repoEnv, ref)
	case "go-install":
		if gitee {
			return fmt.Sprintf("source update: ROLLING_MEMORY_REPO_URL=%s ROLLING_MEMORY_SOURCE_REF=%s bash install.sh", updatesource.RepoURL(updatesource.Gitee), ref)
		}
		return fmt.Sprintf("go install github.com/rocareer/rolling-memory/cmd/rolling-memory@%s", ref)
	default:
		return ""
	}
}

func platformCLIAssetName() string {
	return updatecheck.PlatformCLIAssetName()
}

func platformAdminAssetName() string {
	return updatecheck.PlatformAdminAssetName()
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
	return updatecheck.FetchManifest(context.Background(), base, timeout)
}

// parseSHA256Sums is the package-local test seam for the shared manifest parser.
func parseSHA256Sums(content string) (map[string]string, string) {
	return updatecheck.ParseSHA256Sums(content)
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

// backupInstalledFile keeps one previous copy before a verified replacement so
// the console restart path can roll back a backend that fails to start.
func backupInstalledFile(source, backup string) {
	if !regularFileExists(source) {
		return
	}
	input, err := os.Open(source)
	if err != nil {
		return
	}
	defer input.Close()
	_ = os.Remove(backup)
	output, err := os.OpenFile(backup, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return
	}
	if _, err := io.Copy(output, input); err != nil {
		_ = output.Close()
		_ = os.Remove(backup)
		return
	}
	_ = output.Close()
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
