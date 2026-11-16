// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package thinclient

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ============================================================================
// Multi-Directory Background Mount, Recursive Nested Traversal & Live On-Demand Reader (`bgdirs.go`)
//
// Core Capabilities:
//   1. Arbitrary Multiple Directories & Deep Nested Subdirectories:
//      - Users can mount any number of local directories (`AddBackgroundDir`).
//      - `filepath.WalkDir` recursively traverses arbitrarily deep nested subdirectories
//        (`dir/sub1/sub2/sub3/doc.docx`), preserving the nested relative path in the anchor.
//      - Overlapping parent/child mounted directories (e.g., mounting both `/work` and `/work/sub/docs`)
//        are automatically deduplicated by canonical physical file path (`abs_path`) so a file inside
//        a nested mount is never ingested twice!
//   2. Source-in-Place Live On-Demand Reading (Zero Full-Text Duplication Bloat):
//      - The client does NOT duplicate gigabytes of raw files into SQLite `thin_vault_docs`.
//      - Instead, `thin_vault_bg_files` & `thin_vault_bg_chunks` store only lightweight file metadata
//        (`abs_path`, `mtime_ns`, `file_size`, `content_sha256`, `chunk_index`, `kept_indices_json`).
//      - When `LoadLocalDocument(unitID)` is called upon retrieval hit, the client reads the chunk
//        LIVE on-demand from the original file on disk (`ReadChunkFromLocalFileLive`) and applies
//        the cloud-distilled `kept_sentence_indices`!
//   3. Millisecond Delta Sync (`SyncBackgroundDirs`):
//      - Unchanged files (`mtime_ns` & `size` match) are skipped in O(1) via `os.Stat` with zero file I/O.
//      - Modified files re-chunk, re-ingest `{h, b, f, layer:"background"}` pulses, and cascade `stale`
//        to any local `noetic` records citing the old chunks (`InvalidateLocalNoeticByCitedDoc`).
//      - Deleted files automatically purge their obsolete chunk `UnitID`s.
// ============================================================================

// BackgroundDirMount represents a mounted local directory in `thin_vault_bg_dirs`.
type BackgroundDirMount struct {
	DirID     string `json:"dir_id"`
	RootPath  string `json:"root_path"`
	AddedAt   int64  `json:"added_at"`
	FileCount int    `json:"file_count,omitempty"`
	// Notice carries a one-shot add-time advisory (directory already mounted, or covered by /
	// containing another mount) for the caller to surface to the user. It is never persisted
	// and stays empty on listed mounts.
	Notice string `json:"notice,omitempty"`
}

// BackgroundSkippedFile records one supported file that this pass could not read or extract
// (content exceeds the file-family size cap, holds no extractable text, or is unreadable).
// A per-file failure never aborts a mount sync: the file stays out of the mount index this
// pass — any rows an earlier pass indexed for it are purged through the same purge as a
// deleted file — while every other file still converges. `Reason` is a human-readable
// diagnostic such as "text file exceeds 4194304 bytes cap" or "no extractable text".
type BackgroundSkippedFile struct {
	AbsPath string `json:"abs_path"`
	RelPath string `json:"rel_path,omitempty"`
	Reason  string `json:"reason"`
}

// BackgroundSyncReport summarizes a delta synchronization pass across all mounted directories.
type BackgroundSyncReport struct {
	MountedDirs          int                     `json:"mounted_dirs"`
	ScannedFiles         int                     `json:"scanned_files"`
	UnchangedFiles       int                     `json:"unchanged_files"`
	IngestedFiles        int                     `json:"ingested_files"`
	IngestedChunks       int                     `json:"ingested_chunks"`
	DeletedFiles         int                     `json:"deleted_files"`
	DeletedChunks        int                     `json:"deleted_chunks"`
	SkippedFiles         []BackgroundSkippedFile `json:"skipped_files,omitempty"`
	UnreadableDirs       []string                `json:"unreadable_dirs,omitempty"`
	InvalidatedNoeticIDs []string                `json:"invalidated_noetic_ids,omitempty"`
}

// backgroundSyncWorkers bounds how many files upload their changed chunks concurrently during a
// mount sync. The first mount of a directory (~26 files / ~1,006 chunks) is dominated by the
// per-chunk `POST /ingest` latency, so a bounded pool cuts the wall clock roughly by the pool
// width, while the width stays well below the account-level QPS budget (target: 1,000 QPS;
// the engine's default subject bucket is 100 RPS, so four loopback round-trips keep a healthy
// margin). One worker drives one file with that file's chunks uploaded sequentially, so at most
// `backgroundSyncWorkers` blind pulses are ever in flight.
//
// Failure-path bound (the delivery contract of the pool): the failing file is discovered by its
// worker while up to `backgroundSyncWorkers-1` other files are already in flight. Those uploads
// drain rather than abort — aborting them would turn genuine transport results into bogus
// `context canceled` errors — and stage 3 still aborts at the first failure in walk order, so no
// failing-suffix file is ever committed locally. A failed sync can therefore leave up to
// `backgroundSyncWorkers-1` files uploaded-but-uncommitted in the cloud; their units are
// content-addressed (the same text re-ingests to the same `ep_<sha256>` unit), so the next
// successful sync converges them and the extension costs bandwidth on the failure path only.
const backgroundSyncWorkers = 4

// backgroundChunkerGeneration names the chunking rule that produced a stored inventory. Chunk
// boundaries follow the sizing rule, not the file alone, so the identity of that rule is stored
// per file and compared on the stat fast path: rows written by an older rule are re-chunked
// instead of trusted. Change this string in the same commit as any chunking rule change (budget
// unit, budget value, packing rule) so existing inventories converge on the next sync.
const backgroundChunkerGeneration = "tokenz-r/1024"

// backgroundTrackedFile is one `thin_vault_bg_files` row loaded as the previous sync's baseline for
// the stat fast path, the content-SHA comparison and per-chunk delta detection.
type backgroundTrackedFile struct {
	absPath           string
	dirID             string
	relPath           string
	mtimeNS           int64
	fileSize          int64
	contentSHA256     string
	chunkerGeneration string
	unitIDs           []string
	chunkHashes       []string
}

// backgroundSyncUpload is one file staged by the scan phase of a mount sync: `filepath.WalkDir`
// records the stat-level facts without any network I/O, the bounded pool fills in the
// read/chunk/pack/upload results, and the serial commit phase persists them in walk order.
type backgroundSyncUpload struct {
	absPath    string
	dirID      string
	relPath    string
	mtimeNS    int64
	fileSize   int64
	occurredAt int64
	prev       backgroundTrackedFile
	hadPrev    bool

	contentsUnchanged bool
	contentSHA256     string
	chunks            []DocumentChunk
	newUnitIDs        []string
	chunkHashes       []string
	changed           []bool
	packets           []BlindPulsePacket
	responses         []CloudIngestResponse
	changedChunks     int
	skipReason        string // non-empty: per-file skip (unreadable, oversized, no extractable text)
	err               error
}

func (v *Vault) ensureBackgroundTables() error {
	if v == nil || v.db == nil {
		return errors.New("thinclient: nil Vault db")
	}
	var readyCnt int
	if err := v.db.QueryRow(`
		SELECT
			COALESCE(SUM(CASE WHEN type = 'table' AND name = 'thin_vault_bg_dirs' THEN 1 ELSE 0 END), 0) +
			COALESCE(SUM(CASE WHEN type = 'table' AND name = 'thin_vault_bg_files' AND sql LIKE '%chunk_hashes_json%' AND sql LIKE '%chunker_generation%' THEN 1 ELSE 0 END), 0) +
			COALESCE(SUM(CASE WHEN type = 'table' AND name = 'thin_vault_bg_chunks' AND sql LIKE '%inserted_at%' THEN 1 ELSE 0 END), 0) +
			COALESCE(SUM(CASE WHEN type = 'index' AND name IN (
				'idx_thin_vault_bg_chunks_path',
				'idx_thin_vault_bg_chunks_occurred_at',
				'idx_thin_vault_bg_chunks_inserted_at',
				'idx_thin_vault_bg_chunks_meta'
			) THEN 1 ELSE 0 END), 0)
		FROM sqlite_master
		WHERE name IN (
			'thin_vault_bg_dirs', 'thin_vault_bg_files', 'thin_vault_bg_chunks',
			'idx_thin_vault_bg_chunks_path', 'idx_thin_vault_bg_chunks_occurred_at',
			'idx_thin_vault_bg_chunks_inserted_at', 'idx_thin_vault_bg_chunks_meta'
		)
	`).Scan(&readyCnt); err == nil && readyCnt == 7 {
		return nil
	}
	ddl := `
	CREATE TABLE IF NOT EXISTS thin_vault_bg_dirs (
		dir_id TEXT PRIMARY KEY,
		root_path TEXT NOT NULL UNIQUE,
		added_at INTEGER NOT NULL DEFAULT 0
	);
	CREATE TABLE IF NOT EXISTS thin_vault_bg_files (
		abs_path TEXT PRIMARY KEY,
		dir_id TEXT NOT NULL,
		rel_path TEXT NOT NULL,
		mtime_ns INTEGER NOT NULL DEFAULT 0,
		file_size INTEGER NOT NULL DEFAULT 0,
		content_sha256 TEXT NOT NULL DEFAULT '',
		chunk_count INTEGER NOT NULL DEFAULT 0,
		unit_ids_json TEXT NOT NULL DEFAULT '[]',
		chunk_hashes_json TEXT NOT NULL DEFAULT '[]',
		chunker_generation TEXT NOT NULL DEFAULT ''
	);
	CREATE TABLE IF NOT EXISTS thin_vault_bg_chunks (
		unit_id TEXT PRIMARY KEY,
		abs_path TEXT NOT NULL,
		rel_path TEXT NOT NULL,
		chunk_index INTEGER NOT NULL DEFAULT 0,
		anchor TEXT NOT NULL DEFAULT '',
		occurred_at INTEGER NOT NULL DEFAULT 0,
		inserted_at INTEGER NOT NULL DEFAULT 0,
		address_uri TEXT NOT NULL DEFAULT '',
		coord64 INTEGER NOT NULL DEFAULT 0,
		waveform_bar TEXT NOT NULL DEFAULT '',
		kept_indices_json TEXT NOT NULL DEFAULT '[]',
		slots_json TEXT NOT NULL DEFAULT '[]'
	);
		CREATE INDEX IF NOT EXISTS idx_thin_vault_bg_chunks_path ON thin_vault_bg_chunks(abs_path);
		CREATE INDEX IF NOT EXISTS idx_thin_vault_bg_chunks_occurred_at ON thin_vault_bg_chunks(occurred_at DESC, unit_id ASC);
		CREATE INDEX IF NOT EXISTS idx_thin_vault_bg_chunks_inserted_at ON thin_vault_bg_chunks(inserted_at DESC);
		CREATE INDEX IF NOT EXISTS idx_thin_vault_bg_chunks_meta ON thin_vault_bg_chunks(occurred_at DESC, unit_id ASC, rel_path, anchor, abs_path, address_uri);`
	if _, err := v.db.Exec(ddl); err != nil {
		return err
	}
	// Existing vaults predate per-chunk hashes: add the column once, then the next
	// sync backfills it (old rows read as [] → every chunk re-ingests one final time).
	if !v.hasBackgroundColumn("thin_vault_bg_files", "chunk_hashes_json") {
		if _, err := v.db.Exec(`ALTER TABLE thin_vault_bg_files ADD COLUMN chunk_hashes_json TEXT NOT NULL DEFAULT '[]'`); err != nil {
			return err
		}
	}
	// Rows from an older (or pre-generation) chunking rule carry '' here, so the next sync
	// re-chunks them under the current rule instead of trusting a stale boundary set.
	if !v.hasBackgroundColumn("thin_vault_bg_files", "chunker_generation") {
		if _, err := v.db.Exec(`ALTER TABLE thin_vault_bg_files ADD COLUMN chunker_generation TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	if !v.hasBackgroundColumn("thin_vault_bg_chunks", "inserted_at") {
		if _, err := v.db.Exec(`ALTER TABLE thin_vault_bg_chunks ADD COLUMN inserted_at INTEGER NOT NULL DEFAULT 0`); err != nil {
			return err
		}
		_, _ = v.db.Exec(`
			UPDATE thin_vault_bg_chunks
			SET inserted_at = COALESCE(
				(SELECT MIN(c2.occurred_at) FROM thin_vault_bg_chunks c2 WHERE c2.abs_path = thin_vault_bg_chunks.abs_path AND c2.occurred_at > 0),
				occurred_at
			)
			WHERE inserted_at = 0 AND occurred_at > 0
		`)
	}
	return nil
}

// hasBackgroundColumn reports whether a background table already carries a column.
// The table name is a package constant, never user input.
func (v *Vault) hasBackgroundColumn(table, column string) bool {
	rows, err := v.db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull, pk int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &colType, &notNull, &defaultValue, &pk); err != nil {
			return false
		}
		if name == column {
			return true
		}
	}
	return false
}

func canonicalDirPath(dirPath string) (string, error) {
	trimmed := strings.TrimSpace(dirPath)
	if trimmed == "" {
		return "", errors.New("thinclient: empty directory path")
	}
	abs, err := filepath.Abs(trimmed)
	if err != nil {
		return "", err
	}
	if resolved, lErr := filepath.EvalSymlinks(abs); lErr == nil {
		abs = resolved
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("thinclient: path is not a directory: %s", abs)
	}
	return filepath.Clean(abs), nil
}

func computeDirID(cleanRoot string) string {
	sum := sha256.Sum256([]byte(cleanRoot))
	return "d_" + hex.EncodeToString(sum[:4])
}

func computeChunkUnitID(absFilePath string, chunkIdx int) string {
	sum := sha256.Sum256([]byte(absFilePath))
	return fmt.Sprintf("bg:%s#%d", hex.EncodeToString(sum[:6]), chunkIdx)
}

// backgroundChunkHash fingerprints one extracted chunk's text so a file edit only
// re-ingests the chunks that actually changed.
func backgroundChunkHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// AddBackgroundDir registers a local directory (and all of its nested subdirectories) as a `background`
// memory source. Multiple directories—including nested parent/child directories—can be added safely.
//
// A file's identity is its canonical physical path, so mounting is idempotent at that level: a
// directory that is already mounted—including the same physical directory reached through a
// case-variant path on a case-insensitive filesystem—resolves to the existing mount instead of
// creating a second one that would re-index every file under a duplicate path. Overlapping
// parent/child mounts stay allowed (the deeper mount owns its subtree during syncs); the returned
// mount carries a `Notice` explaining either situation.
func (v *Vault) AddBackgroundDir(dirPath string) (BackgroundDirMount, error) {
	if err := v.ensureBackgroundTables(); err != nil {
		return BackgroundDirMount{}, err
	}
	cleanRoot, err := canonicalDirPath(dirPath)
	if err != nil {
		return BackgroundDirMount{}, err
	}
	mounts, err := v.ListBackgroundDirs()
	if err != nil {
		return BackgroundDirMount{}, err
	}
	// Physical-identity check: os.SameFile catches the same directory reached through a
	// different canonical string (case-variant on a case-insensitive filesystem, hardlinked
	// alias), which pure string comparison of paths can never see.
	if newInfo, statErr := os.Stat(cleanRoot); statErr == nil {
		for _, m := range mounts {
			existingInfo, err := os.Stat(m.RootPath)
			if err != nil {
				continue
			}
			if os.SameFile(existingInfo, newInfo) {
				m.Notice = fmt.Sprintf("directory is already mounted as %s (same physical directory)", m.RootPath)
				return m, nil
			}
		}
	}
	dirID, err := v.resolveOrAllocateDirID(cleanRoot)
	if err != nil {
		return BackgroundDirMount{}, err
	}
	now := time.Now().Unix()
	if _, err = v.db.Exec(`
		INSERT OR IGNORE INTO thin_vault_bg_dirs(dir_id, root_path, added_at)
		VALUES (?, ?, ?)`,
		dirID, cleanRoot, now,
	); err != nil {
		return BackgroundDirMount{}, err
	}
	mount := BackgroundDirMount{
		DirID:    dirID,
		RootPath: cleanRoot,
		AddedAt:  now,
	}
	for _, m := range mounts {
		if m.RootPath != cleanRoot {
			continue
		}
		// The row already existed (INSERT OR IGNORE was a no-op): report the stored mount.
		mount.AddedAt = m.AddedAt
		mount.Notice = fmt.Sprintf("directory is already mounted (%s)", cleanRoot)
		return mount, nil
	}
	for _, m := range mounts {
		switch {
		case backgroundPathCovered(m.RootPath, cleanRoot):
			// The new mount is a subdirectory of an existing mount: both stay, and every file
			// under the deeper root binds to the more specific mount on the next sync.
			mount.Notice = fmt.Sprintf("directory is covered by the existing mount %s; its files will bind to this more specific mount", m.RootPath)
		case backgroundPathCovered(cleanRoot, m.RootPath):
			mount.Notice = fmt.Sprintf("directory contains the existing mount %s; files inside it stay bound to the more specific mount", m.RootPath)
		default:
			continue
		}
		return mount, nil
	}
	return mount, nil
}

// backgroundPathCovered reports whether absPath lies inside root (or equals it), treating the
// filesystem root correctly.
func backgroundPathCovered(root, absPath string) bool {
	if absPath == root {
		return true
	}
	prefix := root
	if !strings.HasSuffix(prefix, string(filepath.Separator)) {
		prefix += string(filepath.Separator)
	}
	return strings.HasPrefix(absPath, prefix)
}

// resolveOrAllocateDirID returns the existing dir_id for cleanRoot if already mounted,
// or allocates computeDirID(cleanRoot), extending the SHA-256 prefix width if another
// distinct root_path ever collides on the 32-bit default prefix.
func (v *Vault) resolveOrAllocateDirID(cleanRoot string) (string, error) {
	var existingID string
	if err := v.db.QueryRow(`SELECT dir_id FROM thin_vault_bg_dirs WHERE root_path = ?`, cleanRoot).Scan(&existingID); err == nil && existingID != "" {
		return existingID, nil
	}
	sum := sha256.Sum256([]byte(cleanRoot))
	for bytesLen := 4; bytesLen <= 16; bytesLen += 2 {
		cand := "d_" + hex.EncodeToString(sum[:bytesLen])
		var otherRoot string
		err := v.db.QueryRow(`SELECT root_path FROM thin_vault_bg_dirs WHERE dir_id = ?`, cand).Scan(&otherRoot)
		if errors.Is(err, sql.ErrNoRows) || otherRoot == cleanRoot {
			return cand, nil
		}
		if err != nil {
			return "", err
		}
	}
	return "d_" + hex.EncodeToString(sum[:16]), nil
}

type backgroundDirRemovedFile struct {
	absPath string
	unitIDs []string
}

type backgroundDirRemoval struct {
	dirID    string
	rootPath string
	files    []backgroundDirRemovedFile
	unitIDs  []string
}

func (v *Vault) loadBackgroundDirRemoval(dirPath string) (backgroundDirRemoval, error) {
	if err := v.ensureBackgroundTables(); err != nil {
		return backgroundDirRemoval{}, err
	}
	cleanRoot, err := canonicalDirPath(dirPath)
	if err != nil {
		// Allow removing a directory even if it was already deleted from disk.
		cleanRoot = filepath.Clean(strings.TrimSpace(dirPath))
		if abs, absErr := filepath.Abs(cleanRoot); absErr == nil {
			cleanRoot = filepath.Clean(abs)
		}
	}
	dirID := computeDirID(cleanRoot)
	var storedDirID string
	if err := v.db.QueryRow(`SELECT dir_id FROM thin_vault_bg_dirs WHERE root_path = ?`, cleanRoot).Scan(&storedDirID); err == nil && storedDirID != "" {
		dirID = storedDirID
	} else {
		var ownerRoot string
		if err := v.db.QueryRow(`SELECT root_path FROM thin_vault_bg_dirs WHERE dir_id = ?`, dirID).Scan(&ownerRoot); err == nil && ownerRoot != "" && ownerRoot != cleanRoot {
			// The 32-bit default ID belongs to a different mounted directory; never purge its files.
			return backgroundDirRemoval{dirID: "", rootPath: cleanRoot}, nil
		}
	}
	removal := backgroundDirRemoval{
		dirID:    dirID,
		rootPath: cleanRoot,
	}

	rows, err := v.db.Query(`SELECT abs_path, unit_ids_json FROM thin_vault_bg_files WHERE dir_id = ? ORDER BY abs_path`, removal.dirID)
	if err != nil {
		return backgroundDirRemoval{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var absPath, uidsJSON string
		if err := rows.Scan(&absPath, &uidsJSON); err != nil {
			return backgroundDirRemoval{}, err
		}
		var unitIDs []string
		if err := json.Unmarshal([]byte(uidsJSON), &unitIDs); err != nil {
			return backgroundDirRemoval{}, fmt.Errorf("thinclient: decode background units for %s: %w", absPath, err)
		}
		removal.files = append(removal.files, backgroundDirRemovedFile{absPath: absPath, unitIDs: unitIDs})
		removal.unitIDs = append(removal.unitIDs, unitIDs...)
	}
	if err := rows.Err(); err != nil {
		return backgroundDirRemoval{}, err
	}
	if err := rows.Close(); err != nil {
		return backgroundDirRemoval{}, err
	}
	return removal, nil
}

// backgroundDirRebind records one tracked file that survives its mount's removal because a
// remaining mount still physically covers it: only the attribution moves (dir_id and display
// rel_path point at the deepest covering mount); chunks, unit IDs, cloud receipts and Noetic
// citations all stay untouched.
type backgroundDirRebind struct {
	absPath string
	dirID   string
	relPath string
}

// backgroundDirPurge is one tracked file that no remaining mount covers: it is removed exactly
// like a deleted file (cloud units first, then local rows and the Noetic stale cascade).
type backgroundDirPurge struct {
	absPath string
	unitIDs []string
}

// backgroundDirRemovalPlan captures the full effect of unmounting one directory against the
// CURRENT mount set: the files re-bound to remaining covering mounts, and the files (plus their
// unit IDs) that no remaining mount covers and which are therefore purged from both planes.
// A file is kept whenever any remaining mount still covers its canonical physical path,
// regardless of which mount happened to own the row at last sync.
type backgroundDirRemovalPlan struct {
	removal   backgroundDirRemoval
	remaining []BackgroundDirMount
	rebinds   []backgroundDirRebind
	purges    []backgroundDirPurge
}

// purgeUnitIDs flattens every UnitID that must be deleted from the cloud.
func (plan backgroundDirRemovalPlan) purgeUnitIDs() []string {
	out := make([]string, 0, len(plan.purges))
	for _, purge := range plan.purges {
		out = append(out, purge.unitIDs...)
	}
	return out
}

// deepestCoveringMount returns the remaining mount with the longest root that physically covers
// absPath, mirroring the sync-side rule that the most specific mount owns its subtree.
func deepestCoveringMount(mounts []BackgroundDirMount, absPath string) (BackgroundDirMount, bool) {
	best := BackgroundDirMount{}
	found := false
	for _, m := range mounts {
		if !backgroundPathCovered(m.RootPath, absPath) {
			continue
		}
		if !found || len(m.RootPath) > len(best.RootPath) {
			best, found = m, true
		}
	}
	return best, found
}

// backgroundRelPath mirrors the sync-side display path: relative to the owning mount root, with
// a base-name fallback for files whose canonical path left the root through a symlink.
func backgroundRelPath(root, absPath string) string {
	relPath, err := filepath.Rel(root, absPath)
	if err != nil || strings.HasPrefix(relPath, "..") {
		relPath = filepath.Base(absPath)
	}
	return filepath.ToSlash(relPath)
}

// planBackgroundDirRemoval resolves the removal against the current mount set before anything is
// written: covered files become rebinds, uncovered files become purges. Only files no mount
// covers anymore enter the purge set, so removing an overlapping mount never purges (and never
// stales citations for) files that another mount keeps tracking.
func (v *Vault) planBackgroundDirRemoval(dirPath string) (backgroundDirRemovalPlan, error) {
	removal, err := v.loadBackgroundDirRemoval(dirPath)
	if err != nil {
		return backgroundDirRemovalPlan{}, err
	}
	plan := backgroundDirRemovalPlan{removal: removal}
	rows, err := v.db.Query(`SELECT dir_id, root_path FROM thin_vault_bg_dirs WHERE root_path != ? ORDER BY dir_id ASC`, removal.rootPath)
	if err != nil {
		return backgroundDirRemovalPlan{}, err
	}
	for rows.Next() {
		var m BackgroundDirMount
		if err := rows.Scan(&m.DirID, &m.RootPath); err != nil {
			_ = rows.Close()
			return backgroundDirRemovalPlan{}, err
		}
		plan.remaining = append(plan.remaining, m)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return backgroundDirRemovalPlan{}, err
	}
	if err := rows.Close(); err != nil {
		return backgroundDirRemovalPlan{}, err
	}
	for _, file := range removal.files {
		if covering, ok := deepestCoveringMount(plan.remaining, file.absPath); ok {
			plan.rebinds = append(plan.rebinds, backgroundDirRebind{
				absPath: file.absPath,
				dirID:   covering.DirID,
				relPath: backgroundRelPath(covering.RootPath, file.absPath),
			})
			continue
		}
		plan.purges = append(plan.purges, backgroundDirPurge{absPath: file.absPath, unitIDs: file.unitIDs})
	}
	return plan, nil
}

// rebindBackgroundFileRowsTx moves one tracked file's attribution to a remaining covering mount
// inside the caller's transaction; chunks, units and citations stay untouched.
func rebindBackgroundFileRowsTx(tx *sql.Tx, rebind backgroundDirRebind) error {
	if _, err := tx.Exec(`UPDATE thin_vault_bg_files SET dir_id = ?, rel_path = ? WHERE abs_path = ?`, rebind.dirID, rebind.relPath, rebind.absPath); err != nil {
		return fmt.Errorf("thinclient: rebind background file %s: %w", rebind.absPath, err)
	}
	if _, err := tx.Exec(`UPDATE thin_vault_bg_chunks SET rel_path = ? WHERE abs_path = ?`, rebind.relPath, rebind.absPath); err != nil {
		return fmt.Errorf("thinclient: rebind background chunks for %s: %w", rebind.absPath, err)
	}
	return nil
}

// rebindBackgroundFile applies one rebind in its own bounded transaction.
func (v *Vault) rebindBackgroundFile(rebind backgroundDirRebind) error {
	tx, err := v.db.Begin()
	if err != nil {
		return fmt.Errorf("thinclient: begin background file rebind %s: %w", rebind.absPath, err)
	}
	if err := rebindBackgroundFileRowsTx(tx, rebind); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("thinclient: commit background file rebind %s: %w", rebind.absPath, err)
	}
	return nil
}

// Purge transactions are bounded twice: backgroundPurgeBatchBudget caps how long one
// transaction may accumulate work and backgroundPurgeBatchUnits caps its row/WAL footprint.
// The per-statement FTS5 delete cost is fat-tailed (segment maintenance can cost hundreds of
// milliseconds inside a single remove), and a mount can hold files with tens of thousands of
// chunks, so neither one file nor a fixed unit count bounds a transaction's duration. The
// budget keeps every purge transaction a small fraction of the busy timeout concurrent
// writers wait on, while staying comfortably above the observed single-statement tail so
// batches do not degenerate to one unit per transaction. backgroundPurgeYield pauses between
// consecutive transactions: back-to-back commits leave only sub-millisecond gaps that a
// waiting writer's busy handler usually misses, letting coarse retry intervals starve it for
// seconds, while a couple of milliseconds (several times the handler's early 1-2ms step)
// always gives every waiter a catchable window.
const (
	backgroundPurgeBatchUnits  = 256
	backgroundPurgeBatchBudget = 50 * time.Millisecond
	backgroundPurgeYield       = 2 * time.Millisecond
)

// RemoveBackgroundDir unmounts a local directory and removes its tracked file/chunk metadata from the local Vault,
// returning all background `UnitID`s that no remaining mount covers so the caller can purge them. Files still
// physically covered by another mounted directory are re-attributed to the deepest covering mount instead — their
// units, cloud receipts and Noetic citations stay intact. It is a local operation; callers that also purge cloud
// units must do so before calling this method.
// Every file is purged or re-bound in bounded batches and the mount row goes last, so the write lock is never
// held for a whole file or mount and any failure leaves a resumable state behind.
func (v *Vault) RemoveBackgroundDir(dirPath string) ([]string, error) {
	plan, err := v.planBackgroundDirRemoval(dirPath)
	if err != nil {
		return nil, err
	}
	return v.applyBackgroundDirRemoval(plan)
}

// applyBackgroundDirRemoval writes a prepared plan into the local Vault: every uncovered file is
// purged through the same bounded batches as a deleted file, every covered file is re-bound in
// its own bounded transaction, and the mount row goes last (with a coverage-aware sweep of any
// file rows a concurrent walk re-committed after their pass), so a failure at any point leaves a
// resumable state behind. It returns the purged UnitIDs the caller must still delete from the cloud.
func (v *Vault) applyBackgroundDirRemoval(plan backgroundDirRemovalPlan) ([]string, error) {
	// One citation index for the whole mount: rescanning the Noetic table per removed unit made
	// the purge quadratic in the mount size.
	citations, err := loadLocalNoeticCitationIndex(v.db)
	if err != nil {
		return nil, err
	}
	if v.hasFTS5 && len(plan.purgeUnitIDs()) > backgroundPurgeBatchUnits {
		_, _ = v.db.Exec(`INSERT INTO thin_vault_docs_fts(thin_vault_docs_fts, rank) VALUES('automerge', 0)`)
		_, _ = v.db.Exec(`INSERT INTO thin_vault_docs_fts(thin_vault_docs_fts, rank) VALUES('crisismerge', 2000)`)
		defer func() {
			_, _ = v.db.Exec(`INSERT INTO thin_vault_docs_fts(thin_vault_docs_fts, rank) VALUES('automerge', 4)`)
			_, _ = v.db.Exec(`INSERT INTO thin_vault_docs_fts(thin_vault_docs_fts, rank) VALUES('crisismerge', 16)`)
		}()
	}
	for _, purge := range plan.purges {
		if err := v.purgeBackgroundFileBatched(purge.absPath, purge.unitIDs, citations); err != nil {
			return nil, err
		}
	}
	for _, rebind := range plan.rebinds {
		if err := v.rebindBackgroundFile(rebind); err != nil {
			return nil, err
		}
		time.Sleep(backgroundPurgeYield)
	}
	// Sweep file rows a concurrent walk re-committed after their pass: a residual file still
	// covered by a remaining mount is re-bound, the rest are purged, so the mount never
	// disappears while rows of it remain behind and covered files are never swept by accident.
	for {
		absPath, unitIDs, found, err := v.nextResidualBackgroundFile(plan.removal.dirID)
		if err != nil {
			return nil, err
		}
		if !found {
			break
		}
		if covering, ok := deepestCoveringMount(plan.remaining, absPath); ok {
			if err := v.rebindBackgroundFile(backgroundDirRebind{
				absPath: absPath,
				dirID:   covering.DirID,
				relPath: backgroundRelPath(covering.RootPath, absPath),
			}); err != nil {
				return nil, err
			}
			continue
		}
		if err := v.purgeBackgroundFileBatched(absPath, unitIDs, citations); err != nil {
			return nil, err
		}
	}
	// The mount row is deleted last, in its own bounded commit, so the removal stays resumable:
	// a failure above keeps the mount and the remaining retry metadata intact.
	tx, err := v.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("thinclient: begin background dir removal transaction %s: %w", plan.removal.rootPath, err)
	}
	if _, err := tx.Exec(`DELETE FROM thin_vault_bg_dirs WHERE dir_id = ? OR root_path = ?`, plan.removal.dirID, plan.removal.rootPath); err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("thinclient: delete background mount %s: %w", plan.removal.rootPath, err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("thinclient: commit background dir removal %s: %w", plan.removal.rootPath, err)
	}
	v.InvalidateRuntimeCaches()
	return plan.purgeUnitIDs(), nil
}

// purgeBackgroundFileBatched removes one tracked file in time-and-unit bounded batches; every
// batch deletes that slice's chunk rows, FTS rows and citing-Noetic staleness together, and the
// file row goes in the last batch, so a failure anywhere leaves a re-enumerable file behind
// instead of orphaned index rows; every step is idempotent for the retry.
func (v *Vault) purgeBackgroundFileBatched(absPath string, unitIDs []string, citations *localNoeticCitationIndex) error {
	_, err := v.purgeBackgroundFileBatchedLoop(context.Background(), absPath, unitIDs, citations)
	return err
}

func (v *Vault) purgeBackgroundFileBatchedWithStaled(ctx context.Context, absPath string, unitIDs []string, citations *localNoeticCitationIndex) (map[string]bool, error) {
	if v.hasFTS5 && len(unitIDs) > backgroundPurgeBatchUnits {
		_, _ = v.db.Exec(`INSERT INTO thin_vault_docs_fts(thin_vault_docs_fts, rank) VALUES('automerge', 0)`)
		_, _ = v.db.Exec(`INSERT INTO thin_vault_docs_fts(thin_vault_docs_fts, rank) VALUES('crisismerge', 2000)`)
		defer func() {
			_, _ = v.db.Exec(`INSERT INTO thin_vault_docs_fts(thin_vault_docs_fts, rank) VALUES('automerge', 4)`)
			_, _ = v.db.Exec(`INSERT INTO thin_vault_docs_fts(thin_vault_docs_fts, rank) VALUES('crisismerge', 16)`)
		}()
	}
	return v.purgeBackgroundFileBatchedLoop(ctx, absPath, unitIDs, citations)
}

func (v *Vault) purgeBackgroundFileBatchedLoop(ctx context.Context, absPath string, unitIDs []string, citations *localNoeticCitationIndex) (map[string]bool, error) {
	staled := make(map[string]bool)
	start := 0
	for {
		nextStart, lastBatch, err := v.purgeBackgroundFileBatch(ctx, absPath, unitIDs, start, citations, staled)
		if err != nil {
			return nil, err
		}
		time.Sleep(backgroundPurgeYield)
		if lastBatch {
			v.InvalidateRuntimeCaches()
			return staled, nil
		}
		start = nextStart
	}
}

// purgeBackgroundFileBatch runs one bounded batch: it deletes up to backgroundPurgeBatchUnits
// chunk rows and FTS rows using set-based IN(...) statements (avoiding 4*N individual SQL
// compilations and FTS5 cursor opens per batch), cascades Noetic citation staleness, and
// deletes the file row itself when this batch reaches the end of unitIDs.
func (v *Vault) purgeBackgroundFileBatch(ctx context.Context, absPath string, unitIDs []string, start int, citations *localNoeticCitationIndex, staled map[string]bool) (int, bool, error) {
	tx, err := v.db.BeginTx(ctx, nil)
	if err != nil {
		return start, false, fmt.Errorf("thinclient: begin background file purge %s: %w", absPath, err)
	}
	rollback := func(err error) (int, bool, error) {
		_ = tx.Rollback()
		return start, false, err
	}
	end := start + backgroundPurgeBatchUnits
	if end > len(unitIDs) {
		end = len(unitIDs)
	}
	batch := unitIDs[start:end]
	if len(batch) > 0 {
		args := make([]any, len(batch))
		for i, unitID := range batch {
			args[i] = unitID
		}
		placeholders := strings.Repeat("?,", len(batch))
		placeholders = placeholders[:len(placeholders)-1]
		if _, err := tx.Exec(`DELETE FROM thin_vault_bg_chunks WHERE unit_id IN (`+placeholders+`)`, args...); err != nil {
			return rollback(fmt.Errorf("thinclient: delete background chunks for %s: %w", absPath, err))
		}
		if v.hasFTS5 {
			if err := ftsDeleteRows(tx, batch); err != nil {
				return rollback(fmt.Errorf("thinclient: delete FTS rows for %s: %w", absPath, err))
			}
		}
		if citations != nil {
			for _, unitID := range batch {
				if _, err := citations.invalidateCited(tx, unitID, staled); err != nil {
					return rollback(fmt.Errorf("thinclient: invalidate Noetic citations for %s: %w", unitID, err))
				}
			}
		}
	}
	lastBatch := end >= len(unitIDs)
	if lastBatch {
		if _, err := tx.Exec(`DELETE FROM thin_vault_bg_chunks WHERE abs_path = ?`, absPath); err != nil {
			return rollback(fmt.Errorf("thinclient: delete background chunks for %s: %w", absPath, err))
		}
		if _, err := tx.Exec(`DELETE FROM thin_vault_bg_files WHERE abs_path = ?`, absPath); err != nil {
			return rollback(fmt.Errorf("thinclient: delete background file metadata %s: %w", absPath, err))
		}
	}
	if err := tx.Commit(); err != nil {
		return start, false, fmt.Errorf("thinclient: commit background file purge %s: %w", absPath, err)
	}
	return end, lastBatch, nil
}

// nextResidualBackgroundFile returns one file row still bound to dirID that the per-file pass
// did not purge (a concurrent walk re-committed it), or found == false when none remain.
func (v *Vault) nextResidualBackgroundFile(dirID string) (absPath string, unitIDs []string, found bool, err error) {
	var unitIDsJSON string
	queryErr := v.db.QueryRow(`SELECT abs_path, unit_ids_json FROM thin_vault_bg_files WHERE dir_id = ? ORDER BY abs_path LIMIT 1`, dirID).Scan(&absPath, &unitIDsJSON)
	if errors.Is(queryErr, sql.ErrNoRows) {
		return "", nil, false, nil
	}
	if queryErr != nil {
		return "", nil, false, queryErr
	}
	if err := json.Unmarshal([]byte(unitIDsJSON), &unitIDs); err != nil {
		return "", nil, false, fmt.Errorf("thinclient: decode background units for %s: %w", absPath, err)
	}
	return absPath, unitIDs, true, nil
}

// ListBackgroundDirs returns all currently mounted local background directories in deterministic order.
func (v *Vault) ListBackgroundDirs() ([]BackgroundDirMount, error) {
	if err := v.ensureBackgroundTables(); err != nil {
		return nil, err
	}
	rows, err := v.db.Query(`SELECT dir_id, root_path, added_at FROM thin_vault_bg_dirs ORDER BY added_at ASC, dir_id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var mounts []BackgroundDirMount
	for rows.Next() {
		var m BackgroundDirMount
		if err := rows.Scan(&m.DirID, &m.RootPath, &m.AddedAt); err != nil {
			return nil, err
		}
		mounts = append(mounts, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for i := range mounts {
		if err := v.db.QueryRow(`SELECT COUNT(*) FROM thin_vault_bg_files WHERE dir_id = ?`, mounts[i].DirID).Scan(&mounts[i].FileCount); err != nil {
			return nil, err
		}
	}
	return mounts, nil
}

type bgChunkMetaEntry struct {
	absPath  string
	relPath  string
	anchor   string
	chunkIdx int
	keptIdx  []int
}

var (
	bgFileChunksMu    sync.RWMutex
	bgFileChunksCache = make(map[string][]DocumentChunk, 64)
	bgChunkMetaMu     sync.RWMutex
	bgChunkMetaCache  = make(map[string]bgChunkMetaEntry, 512)
)

func clearBackgroundChunkCaches() {
	bgFileChunksMu.Lock()
	bgFileChunksCache = make(map[string][]DocumentChunk, 64)
	bgFileChunksMu.Unlock()

	bgChunkMetaMu.Lock()
	bgChunkMetaCache = make(map[string]bgChunkMetaEntry, 512)
	bgChunkMetaMu.Unlock()
}

func (v *Vault) lookupBackgroundChunkMeta(unitID string) (bgChunkMetaEntry, error) {
	var cacheKey string
	if v.slotCacheMu != nil && v.snapRef != nil {
		v.slotCacheMu.RLock()
		snap := v.snapRef.snap
		if snap != nil && len(snap.docs) >= 2048 {
			cacheKey = fmt.Sprintf("%d:%d:%d:%d:%s", snap.bgCnt, snap.bgMaxRowID, snap.bgOccSum, snap.bgSlotsLen, unitID)
		}
		v.slotCacheMu.RUnlock()
	}
	if cacheKey != "" {
		bgChunkMetaMu.RLock()
		if entry, ok := bgChunkMetaCache[cacheKey]; ok {
			bgChunkMetaMu.RUnlock()
			return entry, nil
		}
		bgChunkMetaMu.RUnlock()
	}
	var absPath, relPath, anchor, keptJSON string
	var chunkIdx int
	err := v.db.QueryRow(`
		SELECT abs_path, rel_path, anchor, chunk_index, kept_indices_json
		FROM thin_vault_bg_chunks WHERE unit_id = ?`, unitID).
		Scan(&absPath, &relPath, &anchor, &chunkIdx, &keptJSON)
	if err != nil {
		return bgChunkMetaEntry{}, err
	}
	var keptIdx []int
	_ = json.Unmarshal([]byte(keptJSON), &keptIdx)
	entry := bgChunkMetaEntry{
		absPath:  absPath,
		relPath:  relPath,
		anchor:   anchor,
		chunkIdx: chunkIdx,
		keptIdx:  keptIdx,
	}
	if cacheKey != "" {
		bgChunkMetaMu.Lock()
		if len(bgChunkMetaCache) >= 4096 {
			bgChunkMetaCache = make(map[string]bgChunkMetaEntry, 512)
		}
		bgChunkMetaCache[cacheKey] = entry
		bgChunkMetaMu.Unlock()
	}
	return entry, nil
}

// loadBackgroundChunkLive reads the chunk LIVE on-demand from the original file on disk using `abs_path` and `chunk_index`,
// and reconstructs the distilled sentences using the stored `kept_indices_json`.
func (v *Vault) loadBackgroundChunkLive(unitID string) (rawText string, distilledText string, keptSents []string, err error) {
	if v == nil || v.db == nil {
		return "", "", nil, errors.New("thinclient: nil Vault db")
	}
	meta, err := v.lookupBackgroundChunkMeta(unitID)
	if err != nil {
		return "", "", nil, err
	}
	absPath, relPath, chunkIdx, keptIdx := meta.absPath, meta.relPath, meta.chunkIdx, meta.keptIdx
	st, statErr := os.Stat(absPath)
	if statErr != nil {
		return "", "", nil, statErr
	}
	fileKey := fmt.Sprintf("%s\x00%d\x00%d", absPath, st.Size(), st.ModTime().UnixNano())
	bgFileChunksMu.RLock()
	chunks, cached := bgFileChunksCache[fileKey]
	bgFileChunksMu.RUnlock()
	if !cached {
		chunks, err = ReadAndChunkLocalFile(absPath, relPath)
		if err != nil {
			return "", "", nil, err
		}
		bgFileChunksMu.Lock()
		if len(bgFileChunksCache) >= 128 {
			bgFileChunksCache = make(map[string][]DocumentChunk, 64)
		}
		bgFileChunksCache[fileKey] = chunks
		bgFileChunksMu.Unlock()
	}
	if chunkIdx < 0 || chunkIdx >= len(chunks) {
		return "", "", nil, fmt.Errorf("thinclient: chunk index %d out of range for %s (total=%d)", chunkIdx, absPath, len(chunks))
	}
	ch := chunks[chunkIdx]
	distilled, kept := ReconstructDistilled(ch.Text, ch.Sentences, keptIdx)
	return ch.Text, distilled, kept, nil
}

// BackgroundChunkLocation returns the relative path, absolute path, and line-range anchor
// (e.g. "L12-40") for a mounted L1 background chunk unit, enabling recall bands to expose
// clickable file:line coordinates directly to the Harness Agent.
func (v *Vault) BackgroundChunkLocation(unitID string) (relPath, absPath, anchor string, ok bool) {
	if v == nil || v.db == nil || !strings.HasPrefix(strings.TrimSpace(unitID), "bg:") {
		return "", "", "", false
	}
	meta, err := v.lookupBackgroundChunkMeta(strings.TrimSpace(unitID))
	if err != nil || strings.TrimSpace(meta.relPath) == "" {
		return "", "", "", false
	}
	return strings.TrimSpace(meta.relPath), strings.TrimSpace(meta.absPath), strings.TrimSpace(meta.anchor), true
}

// AddBackgroundDir mounts a local directory on the Client's Vault.
func (c *Client) AddBackgroundDir(dirPath string) (BackgroundDirMount, error) {
	if c.Vault == nil {
		return BackgroundDirMount{}, errors.New("thinclient: AddBackgroundDir requires a local SQLite Vault")
	}
	return c.Vault.AddBackgroundDir(dirPath)
}

func (c *Client) deleteBackgroundUnits(ctx context.Context, unitIDs []string) error {
	if len(unitIDs) == 0 {
		return nil
	}
	if c.Transport == nil {
		return errors.New("thinclient: background unit purge requires a Transport with DeleteUnits")
	}
	deleter, ok := c.Transport.(interface {
		DeleteUnits(context.Context, []string) error
	})
	if !ok {
		return errors.New("thinclient: background unit purge requires a Transport with DeleteUnits")
	}
	if err := deleter.DeleteUnits(ctx, unitIDs); err != nil {
		return fmt.Errorf("thinclient: purge background units: %w", err)
	}
	return nil
}

// BackgroundDirRemovalReport summarizes one unmount: the UnitIDs purged from both planes, and how
// many tracked files survived the unmount by re-binding to a remaining covering mount.
type BackgroundDirRemovalReport struct {
	RootPath       string   `json:"root_path"`
	RemovedUnitIDs []string `json:"removed_unit_ids,omitempty"`
	ReboundFiles   int      `json:"rebound_files"`
}

// RemoveBackgroundDir purges a mounted directory's cloud chunk units before unmounting its local metadata.
// Only units that no remaining mount covers are purged: files still covered by another mount are re-attributed
// to the deepest covering mount and stay in the cloud. A failed purge leaves the mount and all retry metadata intact.
func (c *Client) RemoveBackgroundDir(ctx context.Context, dirPath string) ([]string, error) {
	report, err := c.RemoveBackgroundDirReport(ctx, dirPath)
	if err != nil {
		return nil, err
	}
	return report.RemovedUnitIDs, nil
}

// RemoveBackgroundDirReport is RemoveBackgroundDir with the full removal report, letting callers
// surface files that survived the unmount because another mount still tracks them.
func (c *Client) RemoveBackgroundDirReport(ctx context.Context, dirPath string) (BackgroundDirRemovalReport, error) {
	if c.Vault == nil {
		return BackgroundDirRemovalReport{}, errors.New("thinclient: RemoveBackgroundDir requires a local SQLite Vault")
	}
	plan, err := c.Vault.planBackgroundDirRemoval(dirPath)
	if err != nil {
		return BackgroundDirRemovalReport{}, err
	}
	if err := c.deleteBackgroundUnits(ctx, plan.purgeUnitIDs()); err != nil {
		return BackgroundDirRemovalReport{}, err
	}
	removedIDs, err := c.Vault.applyBackgroundDirRemoval(plan)
	if err != nil {
		return BackgroundDirRemovalReport{}, err
	}
	return BackgroundDirRemovalReport{
		RootPath:       plan.removal.rootPath,
		RemovedUnitIDs: removedIDs,
		ReboundFiles:   len(plan.rebinds),
	}, nil
}

// uploadBackgroundFile drains the network half of one staged file: read and chunk it, diff every
// chunk against the previous sync, blind-pack and upload the changed chunks. Chunk order is
// sequential, so a file's first failing chunk is its reported error. No local write happens here:
// the commit phase owns every transaction.
//
// A file that cannot be read or chunked at all (size cap, unreadable, no extractable text) is
// recorded as a per-file skip and the mount sync carries on: the walk staged it, but nothing
// about one unreadable file says the rest of the tree is untrustworthy, so it must never abort
// the whole mount. Only transport errors keep the fail-fast contract of the pool.
func (c *Client) uploadBackgroundFile(ctx context.Context, up *backgroundSyncUpload) error {
	chunks, err := ReadAndChunkLocalFile(up.absPath, up.relPath)
	if err != nil {
		var sizeErr *FileSizeLimitError
		if errors.As(err, &sizeErr) {
			up.skipReason = fmt.Sprintf("%s file exceeds %d bytes cap", sizeErr.Family, sizeErr.Cap)
		} else {
			up.skipReason = err.Error()
		}
		return nil
	}
	if len(chunks) == 0 {
		up.skipReason = "no extractable text"
		return nil
	}

	// Compute content SHA-256 over extracted chunks to detect no-op touches.
	h := sha256.New()
	for _, chunk := range chunks {
		_, _ = h.Write([]byte(chunk.Text))
		_, _ = h.Write([]byte{0})
	}
	up.chunks = chunks
	up.contentSHA256 = hex.EncodeToString(h.Sum(nil))
	if up.hadPrev && up.prev.contentSHA256 == up.contentSHA256 && len(up.prev.unitIDs) == len(chunks) {
		up.contentsUnchanged = true
		return nil
	}

	// Per-chunk delta: a file-level edit no longer re-sends every chunk. Chunk i is
	// re-ingested only when its text hash changed (or it is new / from a legacy row
	// without hashes). Unit IDs stay path+index based, so unchanged cloud units and
	// their Noetic citations remain valid.
	up.newUnitIDs = make([]string, len(chunks))
	up.chunkHashes = make([]string, len(chunks))
	up.packets = make([]BlindPulsePacket, len(chunks))
	up.responses = make([]CloudIngestResponse, len(chunks))
	up.changed = make([]bool, len(chunks))
	for i, chunk := range chunks {
		unitID := computeChunkUnitID(up.absPath, chunk.ChunkIndex)
		up.newUnitIDs[i] = unitID
		up.chunkHashes[i] = backgroundChunkHash(chunk.Text)
		if up.hadPrev && i < len(up.prev.unitIDs) && i < len(up.prev.chunkHashes) &&
			up.prev.unitIDs[i] == unitID && up.prev.chunkHashes[i] == up.chunkHashes[i] {
			continue
		}
		up.changed[i] = true
		up.changedChunks++
		pkt, _ := c.Vault.PackBlindPacket(unitID, chunk.Text, up.occurredAt)
		pkt.Layer = LayerBackground
		fileKey := strings.TrimSpace(chunk.FileURL)
		if fileKey == "" {
			fileKey = up.relPath
		}
		if fileKey == "" {
			fileKey = up.absPath
		}
		if fileKey != "" {
			pkt.DocHash = c.Vault.KeyedHash64(fileKey)
		}
		if anchor := strings.TrimSpace(chunk.Anchor); anchor != "" {
			pkt.BlockHash = c.Vault.KeyedHash64(fileKey + "#" + anchor)
		} else {
			pkt.BlockHash = c.Vault.KeyedHash64(unitID)
		}
		pkt.LineStart = chunk.LineStart
		pkt.LineEnd = chunk.LineEnd
		if pkt.LineStart <= 0 {
			pkt.LineStart = 1
		}
		if pkt.LineEnd < pkt.LineStart {
			pkt.LineEnd = pkt.LineStart
		}
		resp, err := c.Transport.IngestBlindPulse(ctx, pkt)
		if err != nil {
			return fmt.Errorf("thinclient: ingest background chunk %s: %w", unitID, err)
		}
		up.packets[i] = pkt
		up.responses[i] = resp
	}
	return nil
}

// commitBackgroundFile serializes one staged file's local bookkeeping after its uploads drained:
// purge obsolete trailing cloud units before deleting their local metadata, refresh a metadata-only
// touch, then write the chunk/FTS/file rows in bounded transactions while cascading `stale` to every
// Noetic record citing a changed chunk. Normal files (<= backgroundPurgeBatchUnits chunks within
// backgroundPurgeBatchBudget) commit in one transaction; giant multi-thousand-chunk files split
// across bounded batches and write the `thin_vault_bg_files` row in the final batch so the SQLite
// write lock is never held for a whole monster file and interrupted passes remain idempotent.
func (c *Client) commitBackgroundFile(ctx context.Context, up *backgroundSyncUpload, getCitations func() (*localNoeticCitationIndex, error)) (map[string]bool, int, error) {
	affectedNoetic := make(map[string]bool)
	if up.contentsUnchanged {
		if _, err := c.Vault.db.Exec(`UPDATE thin_vault_bg_files SET mtime_ns = ?, file_size = ?, dir_id = ?, rel_path = ?, chunker_generation = ? WHERE abs_path = ?`,
			up.mtimeNS, up.fileSize, up.dirID, up.relPath, backgroundChunkerGeneration, up.absPath); err != nil {
			return nil, 0, fmt.Errorf("thinclient: update unchanged background content %s: %w", up.absPath, err)
		}
		return affectedNoetic, 0, nil
	}

	// Purge obsolete trailing cloud units before deleting their local metadata.
	if up.hadPrev && len(up.prev.unitIDs) > len(up.chunks) {
		if err := c.deleteBackgroundUnits(ctx, up.prev.unitIDs[len(up.chunks):]); err != nil {
			return nil, 0, err
		}
	}

	fileInsertedAt := up.occurredAt
	if up.hadPrev {
		var existingMin int64
		_ = c.Vault.db.QueryRow(`SELECT COALESCE(MIN(CASE WHEN inserted_at > 0 THEN inserted_at WHEN occurred_at > 0 THEN occurred_at END), 0) FROM thin_vault_bg_chunks WHERE abs_path = ?`, up.absPath).Scan(&existingMin)
		if existingMin > 0 && existingMin < fileInsertedAt {
			fileInsertedAt = existingMin
		}
	}
	uidsJSON, err := json.Marshal(up.newUnitIDs)
	if err != nil {
		return nil, 0, fmt.Errorf("thinclient: encode background units for %s: %w", up.absPath, err)
	}
	hashesJSON, err := json.Marshal(up.chunkHashes)
	if err != nil {
		return nil, 0, fmt.Errorf("thinclient: encode background chunk hashes for %s: %w", up.absPath, err)
	}

	var citations *localNoeticCitationIndex
	if up.hadPrev {
		needCitationScan := false
		for i := range up.prev.unitIDs {
			if i >= len(up.changed) || up.changed[i] {
				needCitationScan = true
				break
			}
		}
		if needCitationScan && getCitations != nil {
			citations, err = getCitations()
			if err != nil {
				return nil, 0, fmt.Errorf("thinclient: load Noetic citation index for %s: %w", up.absPath, err)
			}
		}
	}

	var trailingUnits []string
	if up.hadPrev && len(up.prev.unitIDs) > len(up.chunks) {
		trailingUnits = up.prev.unitIDs[len(up.chunks):]
	}
	if c.Vault.hasFTS5 && up.changedChunks+len(trailingUnits) > backgroundPurgeBatchUnits {
		_, _ = c.Vault.db.Exec(`INSERT INTO thin_vault_docs_fts(thin_vault_docs_fts, rank) VALUES('automerge', 0)`)
		_, _ = c.Vault.db.Exec(`INSERT INTO thin_vault_docs_fts(thin_vault_docs_fts, rank) VALUES('crisismerge', 2000)`)
		defer func() {
			_, _ = c.Vault.db.Exec(`INSERT INTO thin_vault_docs_fts(thin_vault_docs_fts, rank) VALUES('automerge', 4)`)
			_, _ = c.Vault.db.Exec(`INSERT INTO thin_vault_docs_fts(thin_vault_docs_fts, rank) VALUES('crisismerge', 16)`)
		}()
	}

	chunkIdx := 0
	trailIdx := 0
	for {
		tx, err := c.Vault.db.BeginTx(ctx, nil)
		if err != nil {
			return nil, 0, fmt.Errorf("thinclient: begin background file transaction %s: %w", up.absPath, err)
		}
		rollback := func(err error) (map[string]bool, int, error) {
			_ = tx.Rollback()
			return nil, 0, err
		}
		batchStart := time.Now()
		ops := 0
		for chunkIdx < len(up.chunks) && ops < backgroundPurgeBatchUnits {
			i := chunkIdx
			chunk := up.chunks[i]
			unitID := up.newUnitIDs[i]
			if !up.changed[i] {
				// Reused chunk: keep its cloud receipt and slots; only refresh the
				// display path if the file moved between mounts.
				if up.hadPrev && up.prev.relPath != up.relPath {
					if _, err := tx.Exec(`UPDATE thin_vault_bg_chunks SET rel_path = ? WHERE unit_id = ?`, up.relPath, unitID); err != nil {
						return rollback(fmt.Errorf("thinclient: update reused chunk path %s: %w", unitID, err))
					}
					ops++
				}
				chunkIdx++
				if ops > 0 && (chunkIdx < len(up.chunks) || trailIdx < len(trailingUnits)) && time.Since(batchStart) >= backgroundPurgeBatchBudget {
					break
				}
				continue
			}
			resp := up.responses[i]
			keptJSON, err := json.Marshal(resp.KeptSentenceIndices)
			if err != nil {
				return rollback(fmt.Errorf("thinclient: encode kept indices for %s: %w", unitID, err))
			}
			slotsBlob := encodeAtomSlots(up.packets[i].Slots)
			if _, err := tx.Exec(`
					INSERT INTO thin_vault_bg_chunks(
						unit_id, abs_path, rel_path, chunk_index, anchor, occurred_at, inserted_at, address_uri, coord64, waveform_bar, kept_indices_json, slots_json
					) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
					ON CONFLICT(unit_id) DO UPDATE SET
						abs_path = excluded.abs_path,
						rel_path = excluded.rel_path,
						chunk_index = excluded.chunk_index,
						anchor = excluded.anchor,
						occurred_at = excluded.occurred_at,
						inserted_at = CASE WHEN thin_vault_bg_chunks.inserted_at > 0 THEN thin_vault_bg_chunks.inserted_at ELSE excluded.inserted_at END,
						address_uri = excluded.address_uri,
						coord64 = excluded.coord64,
						waveform_bar = excluded.waveform_bar,
						kept_indices_json = excluded.kept_indices_json,
						slots_json = excluded.slots_json`,
				unitID, up.absPath, up.relPath, chunk.ChunkIndex, chunk.Anchor, up.occurredAt, fileInsertedAt,
				resp.AddressURI, int64(resp.AddressCoord64), resp.WaveformBar, string(keptJSON), slotsBlob,
			); err != nil {
				return rollback(fmt.Errorf("thinclient: save background chunk %s: %w", unitID, err))
			}
			if c.Vault.hasFTS5 {
				if err := ftsReplaceRow(tx, unitID, chunk.Text); err != nil {
					return rollback(fmt.Errorf("thinclient: index FTS row for %s: %w", unitID, err))
				}
			}
			chunkIdx++
			ops++
			if (chunkIdx < len(up.chunks) || trailIdx < len(trailingUnits)) && time.Since(batchStart) >= backgroundPurgeBatchBudget {
				break
			}
		}

		if chunkIdx >= len(up.chunks) && trailIdx < len(trailingUnits) {
			endTrail := trailIdx + (backgroundPurgeBatchUnits - ops)
			if endTrail <= trailIdx {
				endTrail = trailIdx + 1
			}
			if endTrail > len(trailingUnits) {
				endTrail = len(trailingUnits)
			}
			trailSlice := trailingUnits[trailIdx:endTrail]
			args := make([]any, len(trailSlice))
			for i, unitID := range trailSlice {
				args[i] = unitID
			}
			placeholders := strings.Repeat("?,", len(trailSlice))
			placeholders = placeholders[:len(placeholders)-1]
			if _, err := tx.Exec(`DELETE FROM thin_vault_bg_chunks WHERE unit_id IN (`+placeholders+`)`, args...); err != nil {
				return rollback(fmt.Errorf("thinclient: delete trailing background chunks for %s: %w", up.absPath, err))
			}
			if c.Vault.hasFTS5 {
				if err := ftsDeleteRows(tx, trailSlice); err != nil {
					return rollback(fmt.Errorf("thinclient: delete trailing FTS rows for %s: %w", up.absPath, err))
				}
			}
			trailIdx = endTrail
		}

		lastBatch := chunkIdx >= len(up.chunks) && trailIdx >= len(trailingUnits)
		if lastBatch {
			if _, err := tx.Exec(`
				INSERT OR REPLACE INTO thin_vault_bg_files(
					abs_path, dir_id, rel_path, mtime_ns, file_size, content_sha256, chunk_count, unit_ids_json, chunk_hashes_json, chunker_generation
				) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				up.absPath, up.dirID, up.relPath, up.mtimeNS, up.fileSize, up.contentSHA256, len(up.newUnitIDs), string(uidsJSON), string(hashesJSON), backgroundChunkerGeneration,
			); err != nil {
				return rollback(fmt.Errorf("thinclient: save synced background file %s: %w", up.absPath, err))
			}
			if citations != nil {
				for i, oldUnitID := range up.prev.unitIDs {
					// Only changed/removed chunks invalidate downstream Noetic citations;
					// untouched chunks keep their provenance edges.
					if i < len(up.changed) && !up.changed[i] {
						continue
					}
					if _, err := citations.invalidateCited(tx, oldUnitID, affectedNoetic); err != nil {
						return rollback(fmt.Errorf("thinclient: invalidate Noetic citations for %s: %w", oldUnitID, err))
					}
				}
			}
		}
		if err := tx.Commit(); err != nil {
			return nil, 0, fmt.Errorf("thinclient: commit background file %s: %w", up.absPath, err)
		}
		if lastBatch {
			c.Vault.ClearDeletedUnits(up.newUnitIDs...)
			c.Vault.InvalidateRuntimeCaches()
			break
		}
		time.Sleep(backgroundPurgeYield)
	}
	return affectedNoetic, up.changedChunks, nil
}

// markBackgroundFailure lowers the shared stop index to the earliest staged file that failed, so
// the pool stops dispatching work for the files that follow it while every earlier file still runs
// to completion (matching the previous single-threaded walk, which had already committed them).
func markBackgroundFailure(stopIndex *atomic.Int64, at int) {
	for {
		seen := stopIndex.Load()
		if int64(at) >= seen || stopIndex.CompareAndSwap(seen, int64(at)) {
			return
		}
	}
}

// rollbackPostFailureUploads purges the units of files that finished uploading after the first
// failure. The pool cannot know the failure in advance, so files behind it may already have
// reached the cloud while their local metadata is never written; purging them here restores the
// strict serial invariant "nothing after the first failure reaches the cloud" instead of leaving
// an orphan tail that only a later retry would converge. Idempotent by construction: those units
// are content-addressed and a retry re-ingests the same ids.
func (c *Client) rollbackPostFailureUploads(ctx context.Context, tail []backgroundSyncUpload) error {
	orphans := make([]string, 0, len(tail))
	for i := range tail {
		if tail[i].err != nil {
			continue
		}
		orphans = append(orphans, tail[i].newUnitIDs...)
	}
	if len(orphans) == 0 {
		return nil
	}
	return c.deleteBackgroundUnits(ctx, orphans)
}

// purgeTrackedBackgroundFile removes one previously indexed file from both planes: its cloud
// chunk units are purged first (so a failed purge keeps every retry row intact), then its local
// file/chunk/FTS rows and the `stale` cascade into every Noetic record citing those chunks are
// written in bounded batches. It returns the invalidated Noetic IDs and is shared by the
// deletion phase and the skipped-file path so both leave the index in exactly the same state.
func (c *Client) purgeTrackedBackgroundFile(ctx context.Context, absPath string, prev backgroundTrackedFile, getCitations func() (*localNoeticCitationIndex, error)) (map[string]bool, error) {
	if err := c.deleteBackgroundUnits(ctx, prev.unitIDs); err != nil {
		return nil, fmt.Errorf("thinclient: purge background file %s: %w", absPath, err)
	}
	var citations *localNoeticCitationIndex
	if len(prev.unitIDs) > 0 && getCitations != nil {
		var err error
		citations, err = getCitations()
		if err != nil {
			return nil, fmt.Errorf("thinclient: load Noetic citation index for %s: %w", absPath, err)
		}
	}
	return c.Vault.purgeBackgroundFileBatchedWithStaled(ctx, absPath, prev.unitIDs, citations)
}

// pathUnderAnyDir reports whether absPath lies inside one of dirs (or equals it).
func pathUnderAnyDir(absPath string, dirs []string) bool {
	for _, dir := range dirs {
		if absPath == dir || strings.HasPrefix(absPath, dir+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// loadBackgroundIgnoreRules reads optional ignore patterns from `<rootPath>/.rolling-memory-ignore`.
// Blank lines and `#` comment lines are ignored. Patterns use slash-separated relative paths or
// base-name globs (e.g. `dev/evidence/`, `docs/reports/evidence/`, `evaldata/`, `*.jsonl`).
func loadBackgroundIgnoreRules(rootPath string) []string {
	raw, err := os.ReadFile(filepath.Join(rootPath, ".rolling-memory-ignore"))
	if err != nil {
		return nil
	}
	var rules []string
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		rules = append(rules, filepath.ToSlash(trimmed))
	}
	return rules
}

// shouldIgnoreBackgroundPath reports whether a slash-normalized relative path inside a mounted
// directory matches any rule from `.rolling-memory-ignore`.
func shouldIgnoreBackgroundPath(relSlash string, name string, isDir bool, rules []string) bool {
	if len(rules) == 0 {
		return false
	}
	cleanRel := strings.TrimPrefix(strings.TrimSuffix(filepath.ToSlash(relSlash), "/"), "./")
	if cleanRel == "" || cleanRel == "." {
		return false
	}
	for _, rule := range rules {
		dirOnly := strings.HasSuffix(rule, "/")
		pat := strings.TrimPrefix(strings.TrimSuffix(rule, "/"), "./")
		if pat == "" {
			continue
		}
		if strings.Contains(pat, "/") {
			if cleanRel == pat || strings.HasPrefix(cleanRel, pat+"/") {
				if !dirOnly || isDir || strings.HasPrefix(cleanRel, pat+"/") {
					return true
				}
			}
			if matched, err := filepath.Match(pat, cleanRel); err == nil && matched {
				if !dirOnly || isDir {
					return true
				}
			}
		} else {
			if (!dirOnly || isDir) && strings.EqualFold(name, pat) {
				return true
			}
			if (!dirOnly || isDir) && func() bool {
				ok, err := filepath.Match(pat, name)
				return err == nil && ok
			}() {
				return true
			}
		}
	}
	return false
}

// SyncBackgroundDirs recursively scans all mounted local directories (including arbitrarily deep nested
// subdirectories and overlapping parent/child mounts), performs `os.Stat(mtime_ns, size)` delta detection,
// streams changed/new files through the native multi-format reader (`.docx`, `.xlsx`, `.pptx`, `.pdf`,
// `.md`, `.txt`, `.json`, `.csv`, code), ingests `{h, b, f, layer:"background"}` blind pulses, and cascades
// `stale` invalidation to local `noetic` records whenever an underlying file changes or is deleted.
// Changed chunks upload through a bounded worker pool while every local write stays on the serial,
// single-writer commit path.
func (c *Client) SyncBackgroundDirs(ctx context.Context) (report BackgroundSyncReport, retErr error) {
	invalidatedSet := make(map[string]bool)
	defer func() {
		report.InvalidatedNoeticIDs = report.InvalidatedNoeticIDs[:0]
		for noeticID := range invalidatedSet {
			report.InvalidatedNoeticIDs = append(report.InvalidatedNoeticIDs, noeticID)
		}
		sort.Strings(report.InvalidatedNoeticIDs)
	}()

	if c.Vault == nil {
		return BackgroundSyncReport{}, errors.New("thinclient: SyncBackgroundDirs requires a local SQLite Vault")
	}
	if c.Transport == nil {
		return BackgroundSyncReport{}, errors.New("thinclient: SyncBackgroundDirs requires a non-nil Transport")
	}
	mounts, err := c.Vault.ListBackgroundDirs()
	if err != nil {
		return BackgroundSyncReport{}, err
	}
	report = BackgroundSyncReport{
		MountedDirs: len(mounts),
	}
	if len(mounts) == 0 {
		return report, nil
	}

	// Sort mounts by longest (most specific) RootPath first so that if both `/work` and `/work/sub`
	// are mounted, files under `/work/sub` bind to the most specific mount `/work/sub` and are never
	// processed twice when walking `/work`!
	sortedMounts := append([]BackgroundDirMount(nil), mounts...)
	sort.Slice(sortedMounts, func(i, j int) bool {
		if len(sortedMounts[i].RootPath) != len(sortedMounts[j].RootPath) {
			return len(sortedMounts[i].RootPath) > len(sortedMounts[j].RootPath)
		}
		return sortedMounts[i].DirID < sortedMounts[j].DirID
	})

	// Load existing tracked files from `thin_vault_bg_files`.
	existingFiles := make(map[string]backgroundTrackedFile)
	rows, err := c.Vault.db.Query(`SELECT abs_path, dir_id, rel_path, mtime_ns, file_size, content_sha256, unit_ids_json, chunk_hashes_json, chunker_generation FROM thin_vault_bg_files`)
	if err != nil {
		return report, err
	}
	for rows.Next() {
		var tf backgroundTrackedFile
		var uidsJSON, hashesJSON string
		if err := rows.Scan(&tf.absPath, &tf.dirID, &tf.relPath, &tf.mtimeNS, &tf.fileSize, &tf.contentSHA256, &uidsJSON, &hashesJSON, &tf.chunkerGeneration); err != nil {
			_ = rows.Close()
			return report, err
		}
		if err := json.Unmarshal([]byte(uidsJSON), &tf.unitIDs); err != nil {
			_ = rows.Close()
			return report, fmt.Errorf("thinclient: decode background units for %s: %w", tf.absPath, err)
		}
		if strings.TrimSpace(hashesJSON) != "" {
			if err := json.Unmarshal([]byte(hashesJSON), &tf.chunkHashes); err != nil {
				_ = rows.Close()
				return report, fmt.Errorf("thinclient: decode background chunk hashes for %s: %w", tf.absPath, err)
			}
		}
		existingFiles[tf.absPath] = tf
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return report, err
	}
	if err := rows.Close(); err != nil {
		return report, err
	}

	seenAbsPaths := make(map[string]bool)
	deletedBGUnits := c.Vault.DeletedUnitSet("background")
	var cachedCitations *localNoeticCitationIndex
	getCitations := func() (*localNoeticCitationIndex, error) {
		if cachedCitations != nil {
			return cachedCitations, nil
		}
		idx, err := loadLocalNoeticCitationIndex(c.Vault.db)
		if err != nil {
			return nil, err
		}
		cachedCitations = idx
		return cachedCitations, nil
	}
	for _, m := range sortedMounts {
		mountInfo, err := os.Stat(m.RootPath)
		if err != nil {
			return report, fmt.Errorf("thinclient: stat background mount %s: %w", m.RootPath, err)
		}
		if !mountInfo.IsDir() {
			return report, fmt.Errorf("thinclient: background mount is not a directory: %s", m.RootPath)
		}

		// Stage 1: the walk only scans and stages. It keeps the O(1) stat fast path (unchanged
		// files never leave this phase) and records every other file for the upload pool; no
		// network or file read happens inside the callback.
		ignoreRules := loadBackgroundIgnoreRules(m.RootPath)
		uploads := make([]backgroundSyncUpload, 0, 16)
		walkErr := filepath.WalkDir(m.RootPath, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				if path == m.RootPath {
					// The mount root itself cannot be scanned: no deletion evidence exists for
					// this pass, so it stays a mount-level failure.
					return walkErr
				}
				// A nested directory that cannot be enumerated (permissions, I/O) keeps the rest
				// of the mount syncing. Its files are simply invisible this pass, which the
				// deletion phase below must not mistake for deletion.
				report.UnreadableDirs = append(report.UnreadableDirs, filepath.Clean(path))
				return nil
			}
			var walkRelSlash string
			if path != m.RootPath {
				if rel, rErr := filepath.Rel(m.RootPath, path); rErr == nil && !strings.HasPrefix(rel, "..") {
					walkRelSlash = filepath.ToSlash(rel)
				}
			}
			if d.IsDir() {
				if path != m.RootPath && (ShouldSkipBackgroundDir(d.Name()) || shouldIgnoreBackgroundPath(walkRelSlash, d.Name(), true, ignoreRules)) {
					return filepath.SkipDir
				}
				return nil
			}
			if shouldIgnoreBackgroundPath(walkRelSlash, d.Name(), false, ignoreRules) {
				return nil
			}

			absFile := filepath.Clean(path)
			if resolved, err := filepath.EvalSymlinks(absFile); err == nil {
				absFile = filepath.Clean(resolved)
			}
			if seenAbsPaths[absFile] {
				// Already visited via a more specific nested mount.
				return nil
			}
			if !IsSupportedBackgroundFile(absFile) {
				return nil
			}
			info, err := os.Stat(absFile)
			if err != nil {
				// The entry vanished between the directory read and this stat (or its symlink
				// dangles): it is absent from this pass, exactly like a deleted file, and the
				// deletion phase purges any stale rows. One unreadable entry never fails the mount.
				return nil
			}
			if info.IsDir() {
				return nil
			}
			seenAbsPaths[absFile] = true
			report.ScannedFiles++

			mtimeNS := info.ModTime().UnixNano()
			fileSize := info.Size()
			relPath, err := filepath.Rel(m.RootPath, path)
			if err != nil {
				return fmt.Errorf("thinclient: resolve background relative path for %s: %w", absFile, err)
			}
			if strings.HasPrefix(relPath, "..") {
				relPath = filepath.Base(absFile)
			}
			relPath = filepath.ToSlash(relPath)

			// Fast O(1) stat check: if mtime_ns and file_size are identical, skip reading the file.
			// The stored chunker generation must match as well, so a chunking-rule change re-chunks
			// every row regardless of the file's own stat. When a user explicitly deleted all chunks
			// of an unchanged file (`len(prev.unitIDs) == 0` while `deletedBGUnits[computeChunkUnitID(absFile, 0)]`
			// is recorded), keep skipping until the file itself is edited on disk.
			if prev, exists := existingFiles[absFile]; exists && prev.mtimeNS == mtimeNS && prev.fileSize == fileSize &&
				(len(prev.unitIDs) > 0 || deletedBGUnits[computeChunkUnitID(absFile, 0)]) &&
				prev.chunkerGeneration == backgroundChunkerGeneration {
				if prev.dirID != m.DirID || prev.relPath != relPath {
					if _, err := c.Vault.db.Exec(`UPDATE thin_vault_bg_files SET dir_id = ?, rel_path = ? WHERE abs_path = ?`, m.DirID, relPath, absFile); err != nil {
						return fmt.Errorf("thinclient: update unchanged background file %s: %w", absFile, err)
					}
				}
				report.UnchangedFiles++
				return nil
			}

			prev, hadPrev := existingFiles[absFile]
			uploads = append(uploads, backgroundSyncUpload{
				absPath:    absFile,
				dirID:      m.DirID,
				relPath:    relPath,
				mtimeNS:    mtimeNS,
				fileSize:   fileSize,
				occurredAt: info.ModTime().Unix(),
				prev:       prev,
				hadPrev:    hadPrev,
			})
			return nil
		})

		// Stage 2: upload the staged files through a bounded worker pool — one file per worker,
		// that file's chunks uploaded sequentially inside the worker, so at most `workers` blind
		// pulses are in flight. The first failure stops dispatch for the files that follow it
		// (`markBackgroundFailure`); requests already in flight drain instead of being aborted
		// mid-request, which keeps every error a genuine transport result instead of a bogus
		// `context canceled`.
		if len(uploads) > 0 {
			workers := backgroundSyncWorkers
			if len(uploads) < workers {
				workers = len(uploads)
			}
			jobs := make(chan int, len(uploads))
			for i := range uploads {
				jobs <- i
			}
			close(jobs)
			var stopIndex atomic.Int64
			stopIndex.Store(int64(len(uploads)))
			var wg sync.WaitGroup
			for w := 0; w < workers; w++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for idx := range jobs {
						if int64(idx) > stopIndex.Load() {
							continue
						}
						if err := c.uploadBackgroundFile(ctx, &uploads[idx]); err != nil {
							uploads[idx].err = err
							markBackgroundFailure(&stopIndex, idx)
						}
					}
				}()
			}
			wg.Wait()
		}

		// Stage 3: commit the staged files serially in walk order. This is the old single-threaded
		// body (trailing purge → transaction → Noetic cascade → counters) unchanged, which keeps
		// the single-writer transaction path and the first-failure abort semantics intact. Both
		// abort sites roll the uploaded-but-uncommitted tail back out of the cloud first, so a
		// failed sync leaves exactly the serial invariant behind: nothing after the first failure.
		for i := range uploads {
			up := &uploads[i]
			if up.err != nil {
				if rollbackErr := c.rollbackPostFailureUploads(ctx, uploads[i+1:]); rollbackErr != nil {
					return report, fmt.Errorf("thinclient: walk background mount %s: %w (post-failure upload rollback failed: %v)", m.RootPath, up.err, rollbackErr)
				}
				return report, fmt.Errorf("thinclient: walk background mount %s: %w", m.RootPath, up.err)
			}
			if up.skipReason != "" {
				// Skipped files are excluded from the mount index. If an earlier pass indexed this
				// path, its rows are purged now (the same purge as a deleted file) so recall never
				// serves content the current pass could not verify.
				if up.hadPrev {
					affectedNoetic, err := c.purgeTrackedBackgroundFile(ctx, up.absPath, up.prev, getCitations)
					if err != nil {
						return report, fmt.Errorf("thinclient: walk background mount %s: %w", m.RootPath, err)
					}
					for noeticID := range affectedNoetic {
						invalidatedSet[noeticID] = true
					}
				}
				report.SkippedFiles = append(report.SkippedFiles, BackgroundSkippedFile{
					AbsPath: up.absPath,
					RelPath: up.relPath,
					Reason:  up.skipReason,
				})
				continue
			}
			affectedNoetic, changedChunks, err := c.commitBackgroundFile(ctx, up, getCitations)
			if err != nil {
				if rollbackErr := c.rollbackPostFailureUploads(ctx, uploads[i+1:]); rollbackErr != nil {
					return report, fmt.Errorf("thinclient: walk background mount %s: %w (post-failure upload rollback failed: %v)", m.RootPath, err, rollbackErr)
				}
				return report, fmt.Errorf("thinclient: walk background mount %s: %w", m.RootPath, err)
			}
			for noeticID := range affectedNoetic {
				invalidatedSet[noeticID] = true
			}
			if changedChunks == 0 {
				report.UnchangedFiles++
			} else {
				report.IngestedFiles++
			}
			report.IngestedChunks += changedChunks
		}

		// A scan error still surfaces after the files staged before it committed (the walk stopped
		// there, so nothing past it was ever staged), and it still keeps the deletion phase below
		// from running on an incomplete scan.
		if walkErr != nil {
			return report, fmt.Errorf("thinclient: walk background mount %s: %w", m.RootPath, walkErr)
		}
	}

	// A mount-root scan error returns above, so deletion never runs on a scan that failed
	// wholesale. Nested directories that could not be read are recorded in report.UnreadableDirs:
	// their files were not enumerable this pass, so absence there is not deletion evidence and
	// the tracked rows stay for the next pass that can actually read them.
	deletedPaths := make([]string, 0)
	for absPath := range existingFiles {
		if seenAbsPaths[absPath] || pathUnderAnyDir(absPath, report.UnreadableDirs) {
			continue
		}
		deletedPaths = append(deletedPaths, absPath)
	}
	sort.Strings(deletedPaths)
	for _, absPath := range deletedPaths {
		prev := existingFiles[absPath]
		affectedForFile, err := c.purgeTrackedBackgroundFile(ctx, absPath, prev, getCitations)
		if err != nil {
			return report, err
		}
		for noeticID := range affectedForFile {
			invalidatedSet[noeticID] = true
		}
		report.DeletedFiles++
		report.DeletedChunks += len(prev.unitIDs)
	}
	return report, nil
}
