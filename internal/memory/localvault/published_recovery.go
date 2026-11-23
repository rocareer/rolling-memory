// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package localvault

import (
	"database/sql"
	"errors"
	"strings"

	core "github.com/rocareer/rolling-memory/internal/core"
)

// snapshotRejectedSlotKey records the CURRENT slot whose segment this build
// could not open (for example a format bump). Presence-only readiness probes
// would otherwise keep reporting the unreadable generation as ready and the
// background builder would never republish it.
const snapshotRejectedSlotKey = "snapshot_rejected_slot"

func snapshotSlotToken(slot string) string {
	slot = strings.TrimSpace(slot)
	if slot == "" {
		return "legacy"
	}
	return slot
}

// snapshotRejectedSlotFromDB reads the rejected-slot marker without opening a
// vault; status surfaces use it for a side-effect-free readiness probe.
func snapshotRejectedSlotFromDB(db *sql.DB) string {
	if db == nil {
		return ""
	}
	var raw []byte
	if err := db.QueryRow(`SELECT v FROM rm_meta WHERE k = ?`, snapshotRejectedSlotKey).Scan(&raw); err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

// markRejectedSnapshotSlot records that the generation in slot cannot be
// opened by this build. A later publication has a different slot name, so the
// marker stops applying as soon as a new generation lands.
func (v *Vault) markRejectedSnapshotSlot(slot string) {
	if v == nil || v.db == nil {
		return
	}
	if _, err := v.db.Exec(`CREATE TABLE IF NOT EXISTS rm_meta (k TEXT PRIMARY KEY, v BLOB NOT NULL)`); err != nil {
		return
	}
	_, _ = v.db.Exec(`INSERT INTO rm_meta(k, v) VALUES (?, ?) ON CONFLICT(k) DO UPDATE SET v = excluded.v`,
		snapshotRejectedSlotKey, []byte(snapshotSlotToken(slot)))
}

// clearRejectedSnapshotSlot forgets the marker after a successful publish.
func (v *Vault) clearRejectedSnapshotSlot() {
	if v == nil || v.db == nil {
		return
	}
	_, _ = v.db.Exec(`DELETE FROM rm_meta WHERE k = ?`, snapshotRejectedSlotKey)
}

// snapshotGenerationRejected reports whether the generation in slot is the one
// this build already failed to open.
func (v *Vault) snapshotGenerationRejected(slot string) bool {
	if v == nil {
		return false
	}
	return snapshotSlotRejected(v.db, slot)
}

// snapshotSlotRejected reports whether slot is the recorded unreadable
// generation. A nil database simply has no marker.
func snapshotSlotRejected(db *sql.DB, slot string) bool {
	rejected := snapshotRejectedSlotFromDB(db)
	return rejected != "" && rejected == snapshotSlotToken(slot)
}

// PublishedSnapshotUsable reports whether the active generation exists and has
// not been recorded as unreadable by this build. It reads the marker through
// the supplied connection, so status probes do not need to open a vault.
func PublishedSnapshotUsable(db *sql.DB, dbPath string) bool {
	if strings.TrimSpace(dbPath) == "" {
		return false
	}
	_, _, _, _, slot, ok := resolvePublishedSnapshotPaths(dbPath)
	if !ok {
		return false
	}
	return !snapshotSlotRejected(db, slot)
}

// snapshotOpenFailureIsFormat reports whether a failed segment open is a
// persistent format/checksum rejection rather than a transient file error.
func snapshotOpenFailureIsFormat(err error) bool {
	return errors.Is(err, core.ErrInvalidZCSR) || errors.Is(err, core.ErrCorruptZCSR)
}
