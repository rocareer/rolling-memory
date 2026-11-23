// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package hostid

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDetectOpenCodeFromSessionMarker(t *testing.T) {
	identity := DetectWith(map[string]string{
		"OPENCODE_SESSION_ID": "ses_123",
		"OPENCODE":            "1",
	}, nil)
	if identity.ID != OpenCode || identity.SessionID != "ses_123" {
		t.Fatalf("identity = %+v, want opencode/ses_123", identity)
	}
	if identity.Evidence != "env:OPENCODE_SESSION_ID" || !identity.Strong {
		t.Fatalf("evidence = %q strong=%v", identity.Evidence, identity.Strong)
	}
}

func TestDetectOpenCodeFromAgentMarker(t *testing.T) {
	identity := DetectWith(map[string]string{"AI_AGENT": "opencode"}, nil)
	if identity.ID != OpenCode || !identity.Strong {
		t.Fatalf("identity = %+v, want strong opencode", identity)
	}
}

func TestDetectRejectsPlainWorkspaceExports(t *testing.T) {
	for _, env := range []map[string]string{
		{"ZCODE_PROJECT_DIR": "/Users/albert/Rolling-Memory"},
		{"CODEX_HOME": "/Users/albert/.codex"},
		{"CLAUDE_PROJECT_DIR": "/Users/albert/Rolling-Memory"},
		{"TERM_PROGRAM": "zcode"},
	} {
		if identity := DetectWith(env, nil); identity.ID != Unknown {
			t.Fatalf("env %v resolved to %q, want unknown", env, identity.ID)
		}
	}
}

func TestDetectUsesAncestryWhenMarkersConflict(t *testing.T) {
	// An OpenCode session started from a ZCode terminal inherits the ZCode
	// session marker. The nearest host in the parent chain is OpenCode.
	identity := DetectWith(
		map[string]string{"ZCODE_SESSION_ID": "z1", "OPENCODE_SESSION_ID": "o1"},
		[]Process{{PID: 10, PPID: 1, Command: "/Applications/OpenCode.app/Contents/MacOS/OpenCode"}, {PID: 1, PPID: 0, Command: "/sbin/launchd"}},
	)
	if identity.ID != OpenCode {
		t.Fatalf("identity = %+v, want opencode from ancestry", identity)
	}
	if identity.Evidence != "process:opencode" {
		t.Fatalf("evidence = %q, want process:opencode", identity.Evidence)
	}
}

func TestDetectExplicitOverride(t *testing.T) {
	identity := DetectWith(map[string]string{"ROLLING_MEMORY_HARNESS": "my-agent"}, nil)
	if identity.ID != "my-agent" || identity.Label != "my-agent" || !identity.Strong {
		t.Fatalf("identity = %+v", identity)
	}
}

func TestEveryRegistryHostHasLabel(t *testing.T) {
	for _, id := range RegistryIDs() {
		if label := Label(id); label == "" || label == string(id) {
			t.Fatalf("host %q has no display label", id)
		}
	}
}

func TestLedgerRecordsLiveSessions(t *testing.T) {
	dir := t.TempDir()
	now := int64(1_000_000)
	if err := RecordAt(dir, Attachment{
		Host: string(OpenCode), Label: "OpenCode", SessionID: "ses_1",
		PID: os.Getpid(), Mode: ModeProcess, LastSeenAt: now,
	}, now); err != nil {
		t.Fatal(err)
	}
	ledger := LoadLedger(dir)
	live := LiveAttachments(ledger, now)
	if len(live) != 1 || live[0].Host != "opencode" || live[0].Label != "OpenCode" {
		t.Fatalf("live = %+v", live)
	}
	if live[0].LeaseUntil <= now {
		t.Fatalf("lease = %d, want > %d", live[0].LeaseUntil, now)
	}
	if _, ok := LatestLive(ledger, now+int64(ProcessLease.Seconds())+1); ok {
		t.Fatal("expired lease must not be live")
	}
}

func TestLedgerUpsertKeepsStartedAt(t *testing.T) {
	dir := t.TempDir()
	first := int64(1_000_000)
	second := first + 30
	attachment := Attachment{Host: "zcode", SessionID: "z1", Mode: ModeProcess}
	if err := RecordAt(dir, attachment, first); err != nil {
		t.Fatal(err)
	}
	if err := RecordAt(dir, attachment, second); err != nil {
		t.Fatal(err)
	}
	live := LiveAttachments(LoadLedger(dir), second)
	if len(live) != 1 || live[0].StartedAt != first || live[0].LastSeenAt != second {
		t.Fatalf("live = %+v, want started=%d last=%d", live, first, second)
	}
}

func TestLedgerLegacyDocumentReportedAsNoLiveHost(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, LedgerFileName), []byte(`{"carrier":"harness:zcode","locus":"TT","updated_at":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if live := LiveAttachments(LoadLedger(dir), 2); len(live) != 0 {
		t.Fatalf("legacy document produced live sessions: %+v", live)
	}
}

func TestDetectCLIOnlyEnvironmentReturnsCLI(t *testing.T) {
	identity := DetectWith(map[string]string{"AI_AGENT": "cli"}, nil)
	if identity.ID != CLI || !identity.Strong {
		t.Fatalf("identity = %+v, want strong cli", identity)
	}
	if identity.Label != "CLI" {
		t.Fatalf("label = %q, want CLI", identity.Label)
	}
}

func TestDetectCLIWithRegistryHostPrefersRegistryHost(t *testing.T) {
	identity := DetectWith(map[string]string{
		"AI_AGENT":            "cli",
		"OPENCODE_SESSION_ID": "ses_1",
	}, nil)
	if identity.ID != OpenCode || identity.SessionID != "ses_1" {
		t.Fatalf("identity = %+v, want opencode/ses_1", identity)
	}
}

func TestRecordAtRefusesHeldLock(t *testing.T) {
	dir := t.TempDir()
	lockPath := LedgerPath(dir) + ".lock"
	if err := os.WriteFile(lockPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	err := RecordAt(dir, Attachment{Host: "cli", SessionID: "s1", LastSeenAt: 1000}, 1000)
	if !errors.Is(err, errLedgerLockTimeout) {
		t.Fatalf("RecordAt error = %v, want errLedgerLockTimeout", err)
	}
	if _, statErr := os.Stat(LedgerPath(dir)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("ledger was written without the lock: %v", statErr)
	}
}

func TestRecordAtReclaimsStaleLock(t *testing.T) {
	dir := t.TempDir()
	lockPath := LedgerPath(dir) + ".lock"
	if err := os.WriteFile(lockPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	stale := time.Now().Add(-3 * time.Second)
	if err := os.Chtimes(lockPath, stale, stale); err != nil {
		t.Fatal(err)
	}
	if err := RecordAt(dir, Attachment{Host: "cli", SessionID: "s1", LastSeenAt: 1000}, 1000); err != nil {
		t.Fatalf("RecordAt with stale lock: %v", err)
	}
	if _, statErr := os.Stat(LedgerPath(dir)); statErr != nil {
		t.Fatalf("ledger missing after stale-lock reclaim: %v", statErr)
	}
}

func TestLedgerPrunesOldSessions(t *testing.T) {
	dir := t.TempDir()
	now := int64(2_000_000)
	if err := RecordAt(dir, Attachment{Host: "codex", SessionID: "c1", Mode: ModeProcess}, now); err != nil {
		t.Fatal(err)
	}
	later := now + int64(RecentTTL.Seconds()) + 1
	if err := RecordAt(dir, Attachment{Host: "zcode", SessionID: "z1", Mode: ModeProcess}, later); err != nil {
		t.Fatal(err)
	}
	ledger := LoadLedger(dir)
	if _, ok := ledger.Sessions["codex:c1"]; ok {
		t.Fatal("expired session was not pruned")
	}
	if _, ok := ledger.Sessions["zcode:z1"]; !ok {
		t.Fatal("fresh session was pruned")
	}
}
