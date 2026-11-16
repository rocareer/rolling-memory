// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package cli

import (
	"context"
	"crypto/rand"
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/rocareer/rolling-memory/internal/api"
	"github.com/rocareer/rolling-memory/internal/config"
	"github.com/rocareer/rolling-memory/internal/harness"
)

const (
	// defaultAutoWatchTick is the base cadence for checking local changes.
	defaultAutoWatchTick = 10 * time.Second
	// bgSettleWindow waits until a modified file's mtime has settled for at least
	// 3 seconds before chunking and syncing, preventing mid-save or git-checkout storms.
	bgSettleWindow = 3 * time.Second
	// episodicQuiescenceWindow waits until the active harness transcript/DB has been
	// quiet for at least 12 seconds so in-flight streaming turns are never cut in half.
	episodicQuiescenceWindow = 12 * time.Second
	// autoWatchLeaseSeconds bounds the single-flight SQLite lease across multiple IDE windows.
	autoWatchLeaseSeconds = 25
)

// IsAutoSyncEnabled reports whether automatic background/episodic monitoring is enabled
// ("auto" mode = true, "manual" mode = false). Default is true unless explicitly disabled
// via ROLLING_MEMORY_AUTO_SYNC or config.toml (`sync.auto_watch = false`).
func IsAutoSyncEnabled(cfgPath string) bool {
	if envVal := strings.ToLower(strings.TrimSpace(os.Getenv("ROLLING_MEMORY_AUTO_SYNC"))); envVal != "" {
		switch envVal {
		case "0", "false", "off", "no", "manual":
			return false
		case "1", "true", "on", "yes", "auto":
			return true
		}
	}
	if strings.TrimSpace(cfgPath) == "" {
		cfgPath = config.DefaultConfigPath()
	}
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		return true
	}
	section := ""
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			section = strings.TrimSpace(trimmed[1 : len(trimmed)-1])
			continue
		}
		parts := strings.SplitN(trimmed, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.ToLower(strings.Trim(strings.TrimSpace(parts[1]), `"'`))
		switch section + "." + key {
		case "sync.auto_watch", "hooks.auto_sync", "cloud.sync.auto_watch":
			if val == "false" || val == "0" || val == "manual" {
				return false
			}
			if val == "true" || val == "1" || val == "auto" {
				return true
			}
		}
	}
	return true
}

// SyncModeString returns "auto" when automatic monitoring is enabled, or "manual" otherwise.
func SyncModeString(cfgPath string) string {
	if IsAutoSyncEnabled(cfgPath) {
		return "auto"
	}
	return "manual"
}

const episodicSyncPausedMetaKey = "episodic_sync_paused"

// IsEpisodicSyncPaused reports whether background episodic scanning/backfilling is paused
// either via the SQLite runtime flag (`episodic_sync_paused = '1'`) or config (`auto_sync = false`).
func IsEpisodicSyncPaused(db *sql.DB, cfgPath string) bool {
	if !IsAutoSyncEnabled(cfgPath) {
		return true
	}
	if db == nil {
		return false
	}
	var raw string
	if err := db.QueryRow(`SELECT CAST(v AS TEXT) FROM thin_vault_meta WHERE k = ?`, episodicSyncPausedMetaKey).Scan(&raw); err == nil {
		trimmed := strings.ToLower(strings.TrimSpace(raw))
		return trimmed == "1" || trimmed == "true" || trimmed == "paused"
	}
	return false
}

// SetEpisodicSyncPaused toggles the runtime pause flag in `thin_vault_meta`.
// When pausing, it also revokes any active autowatch lease so an in-flight pass stops within 500ms.
func SetEpisodicSyncPaused(db *sql.DB, paused bool) error {
	if db == nil {
		return fmt.Errorf("database is required")
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS thin_vault_meta (k TEXT PRIMARY KEY, v BLOB NOT NULL)`); err != nil {
		return err
	}
	val := "0"
	if paused {
		val = "1"
	}
	if _, err := db.Exec(`INSERT INTO thin_vault_meta(k, v) VALUES (?, ?) ON CONFLICT(k) DO UPDATE SET v = excluded.v`, episodicSyncPausedMetaKey, val); err != nil {
		return err
	}
	if paused {
		_, _ = db.Exec(`DELETE FROM thin_vault_meta WHERE k = 'autowatch_lease'`)
	}
	return nil
}

// RunAutoWatchTick executes one single-flight pass of the auto-watcher if enabled.
// It returns true if a pass ran, along with counts of synced background chunks and episodic turns.
func RunAutoWatchTick(ctx context.Context, memory *api.Memory, settleBG, quiesceEpi time.Duration) (ran bool, bgChunks int, epiPushed int) {
	if memory == nil || memory.Store == nil || memory.Store.DB() == nil {
		return false, 0, 0
	}
	if api.ShouldYieldToForegroundRecall() {
		return false, 0, 0
	}
	cfgPath := ""
	if memory.Config != nil {
		cfgPath = memory.Config.Path
	}
	db := memory.Store.DB()
	if IsEpisodicSyncPaused(db, cfgPath) {
		return false, 0, 0
	}
	passCtx, cancel := context.WithCancel(ctx)
	api.RegisterBackgroundWorkCancel(cancel)
	defer func() {
		api.RegisterBackgroundWorkCancel(nil)
		cancel()
	}()
	lease, ok := acquireAutoWatchLease(db, cancel)
	if !ok {
		return false, 0, 0
	}
	defer releaseAutoWatchLease(db, lease)
	ctx = passCtx

	if _, err := runEpisodicIndexBackfillTick(ctx, memory); err != nil {
		log.Printf("episodic index backfill: %v", err)
	}

	// 1. L1 Background: only sync when mounted dirs have dirty/new/deleted files whose
	// mtime has settled past settleBG.
	if hasSettledDirtyBackgroundState(db, settleBG) {
		if tc, err := memory.ThinClient(); err == nil && tc != nil && tc.Transport != nil {
			if rep, sErr := tc.SyncBackgroundDirs(ctx); sErr == nil {
				bgChunks += rep.IngestedChunks
			}
		}
	}

	// 2. L2 Episodic: reconcile whenever a harness transcript store actually changed
	// since the last successful pass. A shared WAL is rewritten every second while
	// any harness session is active, so "wait for 12s of silence" never fires in a
	// live multi-session workspace; completion is instead decided per turn inside
	// the scanner, which keeps a still-streaming tail out of the engine while every
	// finished turn flows in. The pass is bounded so consecutive ticks never overlap:
	// local pointer import is unbudgeted (pure SQLite write), only network pulses stop
	// at the deadline.
	home, _ := os.UserHomeDir()
	if home != "" {
		passCtx, cancelPass := context.WithTimeout(ctx, autoWatchReconcileBudget)
		if err := runEpisodicSourceScanTick(passCtx, memory, home); err != nil {
			log.Printf("episodic source scan: %v", err)
		}
		cancelPass()
	}
	if home != "" && harnessTranscriptChangedSince(db, home) {
		passCtx, cancelPass := context.WithTimeout(ctx, autoWatchReconcileBudget)
		if rep, rErr := executeReconcileSync(passCtx, memory, "episodic", "", 15); rErr == nil {
			epiPushed += rep.EpisodicPushedToRemote
			markHarnessTranscriptSynced(db, home)
		}
		cancelPass()
	}
	return true, bgChunks, epiPushed
}

// autoWatchReconcileBudget keeps one background pass inside the auto-watch tick
// (defaultAutoWatchTick) so a slow engine cannot stack overlapping passes on the
// single SQLite writer.
const autoWatchReconcileBudget = 8 * time.Second

// RunAutoWatchLoop runs the periodic background/episodic watcher until ctx is cancelled.
func RunAutoWatchLoop(ctx context.Context, memory *api.Memory, interval time.Duration) {
	if interval <= 0 {
		interval = defaultAutoWatchTick
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			RunAutoWatchTick(ctx, memory, bgSettleWindow, episodicQuiescenceWindow)
		}
	}
}

// backgroundMCPAutoWatch runs alongside `rolling-memory mcp` (stdio or HTTP), checking
// `IsAutoSyncEnabled` on every tick and keeping one dedicated background handle while
// auto-sync is active so it neither re-runs schema probes every 10s nor shares the
// foreground recall connection pool.
func (a *application) backgroundMCPAutoWatch(ctx context.Context) {
	ticker := time.NewTicker(defaultAutoWatchTick)
	defer ticker.Stop()
	var watchMem *api.Memory
	defer func() {
		if watchMem != nil {
			_ = watchMem.Close()
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if api.ShouldYieldToForegroundRecall() {
				continue
			}
			cfgPath := strings.TrimSpace(a.args.String("config"))
			if !IsAutoSyncEnabled(cfgPath) {
				if watchMem != nil {
					_ = watchMem.Close()
					watchMem = nil
				}
				continue
			}
			if watchMem == nil {
				mem, err := a.openMemory()
				if err != nil || mem == nil {
					continue
				}
				watchMem = mem
			}
			RunAutoWatchTick(ctx, watchMem, bgSettleWindow, episodicQuiescenceWindow)
		}
	}
}

// hasSettledDirtyBackgroundState checks whether any tracked background file was modified/deleted
// (and settled past settleWindow) or if a mounted directory has a different file count.
func hasSettledDirtyBackgroundState(db *sql.DB, settleWindow time.Duration) bool {
	var dirCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM thin_vault_bg_dirs`).Scan(&dirCount); err != nil || dirCount == 0 {
		return false
	}
	now := time.Now()
	rows, err := db.Query(`SELECT abs_path, mtime_ns, file_size FROM thin_vault_bg_files`)
	if err != nil {
		return false
	}
	tracked := make(map[string]struct{}, 64)
	dirtySettled := false
	for rows.Next() {
		var p string
		var mtimeNS, fsize int64
		if rows.Scan(&p, &mtimeNS, &fsize) != nil {
			continue
		}
		tracked[p] = struct{}{}
		info, sErr := os.Stat(p)
		if sErr != nil {
			// File was deleted on disk.
			dirtySettled = true
			continue
		}
		if info.ModTime().UnixNano() != mtimeNS || info.Size() != fsize {
			if settleWindow <= 0 || now.Sub(info.ModTime()) >= settleWindow {
				dirtySettled = true
			}
		}
	}
	rows.Close()
	if dirtySettled {
		return true
	}

	// Also check if any new supported file was added under mounted directories.
	dirRows, err := db.Query(`SELECT root_path FROM thin_vault_bg_dirs`)
	if err != nil {
		return false
	}
	var roots []string
	for dirRows.Next() {
		var r string
		if dirRows.Scan(&r) == nil && strings.TrimSpace(r) != "" {
			roots = append(roots, r)
		}
	}
	dirRows.Close()

	newSettled := false
	for _, root := range roots {
		_ = filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil
			}
			if d.IsDir() {
				name := d.Name()
				if path != root && (strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor" || name == "dist" || name == "target") {
					return filepath.SkipDir
				}
				return nil
			}
			clean := filepath.Clean(path)
			if _, exists := tracked[clean]; exists {
				return nil
			}
			info, iErr := d.Info()
			if iErr == nil && (settleWindow <= 0 || now.Sub(info.ModTime()) >= settleWindow) {
				newSettled = true
				return filepath.SkipAll
			}
			return nil
		})
		if newSettled {
			return true
		}
	}
	return false
}

// harnessEpisodeSyncMetaKey remembers the newest harness transcript store mtime
// that a successful episodic reconcile has already absorbed.
const harnessEpisodeSyncMetaKey = "harness_episodic_synced_mtime"

// harnessTranscriptPatterns returns the transcript globs of every harness this
// build can import turns from. The list resolves through the harness adapter, so
// a change detector and the session scanner can never read different files, and
// a host that is inventoried but not yet importable (its transcripts are
// compressed or use an unread schema) contributes no change evidence: it cannot
// make a reconcile pass do anything, while its constant writes would otherwise
// keep the auto-sync gate permanently dirty.
func harnessTranscriptPatterns(home string) []string {
	var patterns []string
	for _, id := range []string{"zcode", "claude-code", "codex", "dsh", "codebuddy", "workbuddy"} {
		if !harnessTurnsScannable(id) {
			continue
		}
		patterns = append(patterns, harness.TranscriptGlobs(home, id)...)
	}
	return patterns
}

// latestHarnessTranscriptMTime returns the newest modification timestamp (Unix seconds)
// across all harness transcript stores, top-level session directories, and discovered
// JSONL transcript files. Inspecting discovered transcript files in addition to top-level
// directories closes the POSIX directory-mtime blind spot where appending to an existing
// nested JSONL file does not update its grandparent directory's mtime.
func latestHarnessTranscriptMTime(home string) int64 {
	latest := int64(0)
	for _, rel := range []string{
		filepath.Join(home, ".zcode", "cli", "db", "db.sqlite"),
		filepath.Join(home, ".zcode", "cli", "db", "db.sqlite-wal"),
		filepath.Join(home, ".zcode", "projects"),
		filepath.Join(home, ".claude", "projects"),
		filepath.Join(home, ".codex", "sessions"),
		filepath.Join(home, ".codex", "archived_sessions"),
		filepath.Join(harness.ResolveDSHHome(home), "sessions"),
		filepath.Join(home, ".codebuddy", "projects"),
		filepath.Join(home, ".workbuddy", "projects"),
	} {
		if st, err := os.Stat(rel); err == nil && st.ModTime().Unix() > latest {
			latest = st.ModTime().Unix()
		}
	}
	for _, pattern := range harnessTranscriptPatterns(home) {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			continue
		}
		for _, path := range matches {
			if st, sErr := os.Stat(path); sErr == nil && st.ModTime().Unix() > latest {
				latest = st.ModTime().Unix()
			}
		}
	}
	return latest
}

// harnessTranscriptChangedSince reports whether a harness transcript store has been
// touched since the last successful episodic reconcile. It is a pure change detector
// (no silence requirement), so live workspaces keep syncing instead of waiting for a
// quiescent window that never arrives while several sessions share one WAL.
func harnessTranscriptChangedSince(db *sql.DB, home string) bool {
	latest := latestHarnessTranscriptMTime(home)
	if latest == 0 {
		return false
	}
	var raw []byte
	if err := db.QueryRow(`SELECT v FROM thin_vault_meta WHERE k = ?`, harnessEpisodeSyncMetaKey).Scan(&raw); err != nil {
		return true
	}
	last, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil {
		return true
	}
	return latest > last
}

// markHarnessTranscriptSynced records the current transcript store mtime as absorbed.
func markHarnessTranscriptSynced(db *sql.DB, home string) {
	latest := latestHarnessTranscriptMTime(home)
	if latest == 0 {
		return
	}
	_, _ = db.Exec(`INSERT OR REPLACE INTO thin_vault_meta(k, v) VALUES (?, ?)`,
		harnessEpisodeSyncMetaKey, []byte(strconv.FormatInt(latest, 10)))
}

type autoWatchLease struct {
	value string
	done  chan struct{}
	stop  chan struct{}
}

func acquireAutoWatchLease(db *sql.DB, cancel context.CancelFunc) (*autoWatchLease, bool) {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS thin_vault_meta (k TEXT PRIMARY KEY, v BLOB NOT NULL)`); err != nil {
		return nil, false
	}
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return nil, false
	}
	lease := &autoWatchLease{value: fmt.Sprintf("%d:%d:%x", os.Getpid(), time.Now().Unix()+autoWatchLeaseSeconds, token), done: make(chan struct{}), stop: make(chan struct{})}
	result, err := db.Exec(`INSERT INTO thin_vault_meta(k, v) VALUES ('autowatch_lease', ?) ON CONFLICT(k) DO UPDATE SET v = excluded.v WHERE instr(CAST(thin_vault_meta.v AS TEXT), ':') > 1 AND substr(CAST(thin_vault_meta.v AS TEXT), instr(CAST(thin_vault_meta.v AS TEXT), ':') + 1, 1) BETWEEN '0' AND '9' AND CAST(substr(CAST(thin_vault_meta.v AS TEXT), instr(CAST(thin_vault_meta.v AS TEXT), ':') + 1) AS INTEGER) < ?`, lease.value, time.Now().Unix())
	if err != nil {
		return nil, false
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return nil, false
	}
	go func() {
		defer close(lease.done)
		ticker := time.NewTicker(time.Duration(autoWatchLeaseSeconds) * time.Second / 3)
		defer ticker.Stop()
		pauseTicker := time.NewTicker(500 * time.Millisecond)
		defer pauseTicker.Stop()
		for {
			select {
			case <-lease.stop:
				return
			case <-pauseTicker.C:
				var pausedVal, curLease string
				if qErr := db.QueryRow(`SELECT COALESCE((SELECT CAST(v AS TEXT) FROM thin_vault_meta WHERE k = ?), '0'), COALESCE((SELECT CAST(v AS TEXT) FROM thin_vault_meta WHERE k = 'autowatch_lease'), '')`, episodicSyncPausedMetaKey).Scan(&pausedVal, &curLease); qErr == nil {
					if pausedVal == "1" || pausedVal == "true" || curLease != lease.value {
						cancel()
						return
					}
				}
			case <-ticker.C:
				next := fmt.Sprintf("%d:%d:%x", os.Getpid(), time.Now().Unix()+autoWatchLeaseSeconds, token)
				result, err := db.Exec(`UPDATE thin_vault_meta SET v = ? WHERE k = 'autowatch_lease' AND v = ?`, next, lease.value)
				if err != nil {
					cancel()
					return
				}
				if n, err := result.RowsAffected(); err != nil || n != 1 {
					cancel()
					return
				}
				lease.value = next
			}
		}
	}()
	return lease, true
}

func releaseAutoWatchLease(db *sql.DB, lease *autoWatchLease) {
	close(lease.stop)
	<-lease.done
	_, _ = db.Exec(`DELETE FROM thin_vault_meta WHERE k = 'autowatch_lease' AND v = ?`, lease.value)
}
