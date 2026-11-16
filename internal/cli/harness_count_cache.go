// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package cli

// Harness turn-count cache.
//
// `sync status` used to rescan every harness transcript (five harnesses, up to 2000
// turns each, JSON-decoding tens of thousands of rows) on every call, and a reconcile
// pass did it twice more. The counts only change when the harness store changes or
// when the local vault grows, so they are memoised in `thin_vault_meta` under a
// fingerprint of both. A cache hit costs one small row read instead of a full scan;
// any doubt (missing store, unreadable cache, fingerprint drift) falls back to the
// scan, so a stale cache can never be reported as truth.

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rocareer/rolling-memory/internal/harness"
)

const (
	harnessCountCachePrefix = "harness_count_cache:"
	// harnessCountCacheTTL bounds how stale a memoised count may be. A live harness
	// store rewrites its WAL every second, so a fingerprint-only cache can never hit
	// while the user is working; the TTL is what makes a repeated `sync status` (a
	// panel poll, a second command in the same minute) cheap. Counts change only when
	// a turn is finished, so one minute of staleness is far below the noise floor of
	// a "backlog size" reading, and any local import still invalidates it at once
	// through the vault-unit component of the key.
	harnessCountCacheTTL = 300
)

type harnessCountCache struct {
	Fingerprint  int64 `json:"fp"`
	VaultUnits   int   `json:"vault"`
	ComputedAt   int64 `json:"computed_at"`
	Sessions     int   `json:"sessions"`
	LocalTurns   int   `json:"local_turns"`
	Synced       int   `json:"synced"`
	Unsynced     int   `json:"unsynced"`
	LastActiveAt int64 `json:"last_active_at"`
}

// harnessStoreFingerprint summarises "has this harness store changed" without
// reading any content: the newest mtime across the store database, its WAL, and the
// transcript files the scanner would read.
func harnessStoreFingerprint(home, harnessID string) int64 {
	patterns := harness.TranscriptGlobs(home, harnessID)
	if harnessID == "zcode" {
		// ZCode keeps interactive sessions in SQLite; its transcript files alone
		// would miss a turn written straight to the database.
		patterns = append([]string{
			filepath.Join(home, ".zcode", "cli", "db", "db.sqlite"),
			filepath.Join(home, ".zcode", "cli", "db", "db.sqlite-wal"),
		}, patterns...)
	}
	if len(patterns) == 0 {
		return 0
	}
	newest := int64(0)
	for _, pattern := range patterns {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return 0
		}
		for _, path := range matches {
			info, statErr := os.Stat(path)
			if statErr != nil || info.IsDir() {
				continue
			}
			if stamp := info.ModTime().UnixNano(); stamp > newest {
				newest = stamp
			}
		}
	}
	return newest
}

func loadHarnessCountCache(db *sql.DB, harnessID, home string, vaultUnits int) (harnessCountCache, bool) {
	if db == nil || vaultUnits <= 0 {
		return harnessCountCache{}, false
	}
	var raw []byte
	if err := db.QueryRow(`SELECT v FROM thin_vault_meta WHERE k = ?`, harnessCountCachePrefix+harnessID).Scan(&raw); err != nil {
		return harnessCountCache{}, false
	}
	var cached harnessCountCache
	if err := json.Unmarshal(raw, &cached); err != nil {
		return harnessCountCache{}, false
	}
	// The scan classifies every turn it returns as synced or unsynced, so the cached
	// turn total must equal their sum; a row written before that total was memoised
	// fails this identity and is recomputed instead of surfacing as zero turns.
	if cached.LocalTurns != cached.Synced+cached.Unsynced {
		return harnessCountCache{}, false
	}
	withinTTL := time.Now().Unix()-cached.ComputedAt < harnessCountCacheTTL
	if cached.VaultUnits != vaultUnits {
		// Within the TTL window, if the local episodic vault grew by at most the
		// previously unsynced backlog (e.g. background auto-sync or an explicit
		// reconcile imported pending turns), fold the delta in place instead of
		// rescanning the multi-GB harness WAL. Any shrink or growth beyond the
		// known backlog falls through to a full scan.
		if withinTTL && vaultUnits > cached.VaultUnits && vaultUnits-cached.VaultUnits <= cached.Unsynced {
			delta := vaultUnits - cached.VaultUnits
			cached.Synced += delta
			cached.Unsynced -= delta
			cached.VaultUnits = vaultUnits
			if rawUpdated, mErr := json.Marshal(cached); mErr == nil {
				_, _ = db.Exec(`INSERT OR REPLACE INTO thin_vault_meta(k, v) VALUES (?, ?)`,
					harnessCountCachePrefix+harnessID, []byte(strings.TrimSpace(string(rawUpdated))))
			}
			return cached, true
		}
		return harnessCountCache{}, false
	}
	if withinTTL {
		return cached, true
	}
	fingerprint := harnessStoreFingerprint(home, harnessID)
	if fingerprint == 0 || cached.Fingerprint != fingerprint {
		return harnessCountCache{}, false
	}
	return cached, true
}

func storeHarnessCountCache(db *sql.DB, harnessID, home string, vaultUnits, sessions, synced, unsynced, localTurns int, lastActiveAt int64) {
	if db == nil {
		return
	}
	fingerprint := harnessStoreFingerprint(home, harnessID)
	if fingerprint == 0 {
		return
	}
	raw, err := json.Marshal(harnessCountCache{
		Fingerprint: fingerprint, VaultUnits: vaultUnits, ComputedAt: time.Now().Unix(),
		Sessions: sessions, LocalTurns: localTurns, Synced: synced, Unsynced: unsynced, LastActiveAt: lastActiveAt,
	})
	if err != nil {
		return
	}
	_, _ = db.Exec(`INSERT OR REPLACE INTO thin_vault_meta(k, v) VALUES (?, ?)`,
		harnessCountCachePrefix+harnessID, []byte(strings.TrimSpace(string(raw))))
}
