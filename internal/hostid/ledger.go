// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package hostid

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	// LedgerFileName keeps the historical path while the document schema
	// moves to per-session entries. Readers that only understand the old
	// single-carrier shape see no sessions and report no live host, which is
	// the honest result for a stale document.
	LedgerFileName = "active_workspace.json"
	LedgerVersion  = 2

	ModeProcess   = "process"
	ModeHeartbeat = "heartbeat"
)

const (
	// ProcessLease covers one heartbeat interval plus a short grace window.
	// The sidecar renews it every thirty seconds while its process is alive.
	ProcessLease = 90 * time.Second
	// HeartbeatLease covers a hook-written prompt without implying that the
	// hook process is still running.
	HeartbeatLease = 2 * time.Minute
	// RecentTTL bounds how long a stale session stays in the document.
	RecentTTL = 24 * time.Hour
)

// errLedgerLockTimeout reports that the cross-process ledger lock stayed held
// through the whole retry budget. The mutation is refused instead of running
// unsynchronized, which would let two writers overwrite each other's session.
var errLedgerLockTimeout = errors.New("hostid: attachment ledger lock timeout")

// Attachment is one session-scoped presence record.
type Attachment struct {
	Host       string `json:"host"`
	Label      string `json:"label"`
	SessionID  string `json:"session_id,omitempty"`
	Role       string `json:"role,omitempty"`
	PID        int    `json:"pid,omitempty"`
	CWD        string `json:"cwd,omitempty"`
	Locus      string `json:"locus,omitempty"`
	Evidence   string `json:"evidence,omitempty"`
	Mode       string `json:"mode,omitempty"`
	StartedAt  int64  `json:"started_at,omitempty"`
	LastSeenAt int64  `json:"last_seen_at"`
	LeaseUntil int64  `json:"lease_until"`
}

// Ledger is the per-session attachment document.
type Ledger struct {
	Version  int                   `json:"version"`
	Sessions map[string]Attachment `json:"sessions"`
}

// LedgerPath returns the document path under stateDir.
func LedgerPath(stateDir string) string {
	return filepath.Join(stateDir, LedgerFileName)
}

// Key identifies one session record. A session-less writer falls back to its
// PID so two concurrent hooks cannot overwrite each other.
func Key(a Attachment) string {
	host := strings.TrimSpace(a.Host)
	if host == "" {
		host = string(Unknown)
	}
	if session := strings.TrimSpace(a.SessionID); session != "" {
		return host + ":" + session
	}
	if a.PID > 0 {
		return fmt.Sprintf("%s:pid:%d", host, a.PID)
	}
	return host + ":default"
}

// LoadLedger reads the document. Missing, unreadable, or legacy documents
// yield an empty v2 ledger rather than an error: presence is advisory state
// and must never break a recall.
func LoadLedger(stateDir string) Ledger {
	if strings.TrimSpace(stateDir) == "" {
		return emptyLedger()
	}
	return loadLedgerFile(LedgerPath(stateDir))
}

func emptyLedger() Ledger {
	return Ledger{Version: LedgerVersion, Sessions: map[string]Attachment{}}
}

func loadLedgerFile(path string) Ledger {
	raw, err := os.ReadFile(path)
	if err != nil {
		return emptyLedger()
	}
	var ledger Ledger
	if json.Unmarshal(raw, &ledger) != nil {
		return emptyLedger()
	}
	if ledger.Sessions == nil {
		ledger.Sessions = map[string]Attachment{}
	}
	ledger.Version = LedgerVersion
	return ledger
}

// RecordAt upserts one session attachment and renews its lease. The write is
// serialized with the same lock-file plus atomic-rename protocol the previous
// single-carrier document used.
func RecordAt(stateDir string, attachment Attachment, now int64) error {
	if strings.TrimSpace(stateDir) == "" {
		return nil
	}
	if attachment.Mode == "" {
		attachment.Mode = ModeProcess
	}
	if attachment.Host == "" {
		attachment.Host = string(Unknown)
	}
	if attachment.Label == "" {
		attachment.Label = Label(Normalize(attachment.Host))
	}
	if attachment.LastSeenAt == 0 {
		attachment.LastSeenAt = now
	}
	key := Key(attachment)
	return mutateLedger(stateDir, func(ledger *Ledger) {
		if previous, ok := ledger.Sessions[key]; ok && attachment.StartedAt == 0 {
			attachment.StartedAt = previous.StartedAt
		}
		if attachment.StartedAt == 0 {
			attachment.StartedAt = attachment.LastSeenAt
		}
		attachment.LeaseUntil = now + int64(leaseDuration(attachment.Mode).Seconds())
		ledger.Sessions[key] = attachment
		ledger.prune(now)
	})
}

// Touch renews a session's lease without creating a new record.
func Touch(stateDir, key string, now int64) error {
	if strings.TrimSpace(stateDir) == "" || strings.TrimSpace(key) == "" {
		return nil
	}
	return mutateLedger(stateDir, func(ledger *Ledger) {
		attachment, ok := ledger.Sessions[key]
		if !ok {
			return
		}
		attachment.LastSeenAt = now
		attachment.LeaseUntil = now + int64(leaseDuration(attachment.Mode).Seconds())
		ledger.Sessions[key] = attachment
		ledger.prune(now)
	})
}

// LiveAttachments returns lease-valid records, newest activity first.
func LiveAttachments(ledger Ledger, now int64) []Attachment {
	out := make([]Attachment, 0, len(ledger.Sessions))
	for _, attachment := range ledger.Sessions {
		if attachment.LeaseUntil >= now {
			out = append(out, attachment)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].LastSeenAt != out[j].LastSeenAt {
			return out[i].LastSeenAt > out[j].LastSeenAt
		}
		return out[i].StartedAt > out[j].StartedAt
	})
	return out
}

// LatestLive returns the most recently active live attachment.
func LatestLive(ledger Ledger, now int64) (Attachment, bool) {
	live := LiveAttachments(ledger, now)
	if len(live) == 0 {
		return Attachment{}, false
	}
	return live[0], true
}

func leaseDuration(mode string) time.Duration {
	if mode == ModeHeartbeat {
		return HeartbeatLease
	}
	return ProcessLease
}

func (ledger *Ledger) prune(now int64) {
	cutoff := now - int64(RecentTTL.Seconds())
	for key, attachment := range ledger.Sessions {
		if attachment.LastSeenAt < cutoff {
			delete(ledger.Sessions, key)
		}
	}
}

func mutateLedger(stateDir string, mutate func(*Ledger)) error {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}
	statePath := LedgerPath(stateDir)
	lockPath := statePath + ".lock"
	locked := false
	for attempt := 0; attempt < 8; attempt++ {
		if info, err := os.Stat(lockPath); err == nil && time.Since(info.ModTime()) > 2*time.Second {
			_ = os.Remove(lockPath)
		}
		lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_ = lock.Close()
			defer os.Remove(lockPath)
			locked = true
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if !locked {
		return errLedgerLockTimeout
	}
	ledger := loadLedgerFile(statePath)
	mutate(&ledger)
	ledger.Version = LedgerVersion
	if ledger.Sessions == nil {
		ledger.Sessions = map[string]Attachment{}
	}
	data, err := json.Marshal(ledger)
	if err != nil {
		return err
	}
	tmpPath := fmt.Sprintf("%s.tmp.%d.%d", statePath, os.Getpid(), time.Now().UnixNano())
	if err := os.WriteFile(tmpPath, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, statePath); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}
