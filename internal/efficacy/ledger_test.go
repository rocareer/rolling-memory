// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package efficacy_test

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/rocareer/rolling-memory/internal/efficacy"
)

func observedEvent(id string) efficacy.LedgerEvent {
	text := []byte("example")
	hash := sha256.Sum256(text)
	size := int64(len(text))
	return efficacy.LedgerEvent{
		EventID: id, ExecutionID: "execution", Session: "session", Scope: "scope",
		Hook: "recall", Action: "prepared", OutputOutcome: "not_applicable", ObservedAt: 123, Observation: "observed",
		TextSHA256: hex.EncodeToString(hash[:]), TextBytes: &size,
	}
}

func TestLedgerAppendRestartAndCorrections(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.sqlite")
	ledger, err := efficacy.OpenLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	original := observedEvent("one")
	if err := ledger.Append(original); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Append(original); err != nil {
		t.Fatalf("duplicate: %v", err)
	}
	conflict := original
	conflict.ObservedAt++
	if err := ledger.Append(conflict); err == nil {
		t.Fatal("conflicting duplicate accepted")
	}
	unknown := original
	unknown.EventID = "two"
	unknown.Action = "output_attempt"
	unknown.OutputOutcome = "unknown"
	unknown.Observation = "unknown"
	unknown.TextSHA256, unknown.TextBytes = "", nil
	if err := ledger.Append(unknown); err != nil {
		t.Fatal(err)
	}
	correction := observedEvent("three")
	correction.Action, correction.TargetEventID = "correction", "one"
	if err := ledger.Append(correction); err != nil {
		t.Fatal(err)
	}
	retraction := unknown
	retraction.EventID, retraction.Action, retraction.TargetEventID = "four", "retraction", "one"
	retraction.OutputOutcome = "not_applicable"
	if err := ledger.Append(retraction); err != nil {
		t.Fatal(err)
	}
	bad := correction
	bad.EventID, bad.Scope = "wrong", "other"
	if err := ledger.Append(bad); err == nil {
		t.Fatal("cross-scope correction accepted")
	}
	bad = correction
	bad.EventID, bad.Harness = "wrong-harness", "another"
	if err := ledger.Append(bad); err == nil {
		t.Fatal("cross-harness correction accepted")
	}
	bad = correction
	bad.EventID, bad.Hook = "wrong-hook", "close"
	if err := ledger.Append(bad); err == nil {
		t.Fatal("cross-hook correction accepted")
	}
	bad = correction
	bad.EventID, bad.OutputOutcome = "wrong-outcome", "success"
	if err := ledger.Append(bad); err == nil {
		t.Fatal("correction outcome incompatible with original action accepted")
	}
	if err := ledger.Close(); err != nil {
		t.Fatal(err)
	}
	ledger, err = efficacy.OpenLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	first, err := ledger.Events(0, 2)
	if err != nil || len(first) != 2 || first[0].EventID != "one" || first[1].EventID != "two" || first[1].TextBytes != nil {
		t.Fatalf("first page: %#v, %v", first, err)
	}
	last, err := ledger.Events(first[1].Sequence, 10)
	if err != nil || len(last) != 2 || last[0].EventID != "three" || last[1].EventID != "four" {
		t.Fatalf("last page: %#v, %v", last, err)
	}
}

func TestLedgerValidation(t *testing.T) {
	ledger, err := efficacy.OpenLedger(filepath.Join(t.TempDir(), "ledger.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	for _, mutate := range []func(*efficacy.LedgerEvent){
		func(e *efficacy.LedgerEvent) { e.EventID = "" },
		func(e *efficacy.LedgerEvent) { e.ExecutionID = "" },
		func(e *efficacy.LedgerEvent) { e.Action = "delivered" },
		func(e *efficacy.LedgerEvent) { e.Observation = "unknown" },
		func(e *efficacy.LedgerEvent) { e.TextSHA256 = "not-a-hash" },
		func(e *efficacy.LedgerEvent) { e.TextBytes = nil },
		func(e *efficacy.LedgerEvent) { e.Action = "correction" },
		func(e *efficacy.LedgerEvent) {
			e.Action = "retraction"
			e.TargetEventID = "missing"
			e.TextSHA256 = ""
			e.TextBytes = nil
		},
	} {
		event := observedEvent("invalid")
		mutate(&event)
		if err := ledger.Append(event); err == nil {
			t.Fatalf("invalid event accepted: %#v", event)
		}
	}
	if _, err := ledger.Events(-1, 1); err == nil {
		t.Fatal("invalid cursor accepted")
	}
}

func TestLedgerDecisionAndUnknownIdentity(t *testing.T) {
	ledger, err := efficacy.OpenLedger(filepath.Join(t.TempDir(), "ledger.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	for _, outcome := range []string{"output", "empty", "error", "unknown"} {
		event := observedEvent(outcome)
		event.Session, event.Scope, event.Harness = "", "", ""
		event.Action, event.OutputOutcome = "decision", outcome
		event.Observation, event.TextSHA256, event.TextBytes = "unknown", "", nil
		if err := ledger.Append(event); err != nil {
			t.Fatalf("decision %s: %v", outcome, err)
		}
	}
	entries, err := ledger.Events(0, 10)
	if err != nil || len(entries) != 4 || entries[0].Scope != "" || entries[0].Observation != "unknown" {
		t.Fatalf("unknown events: %#v, %v", entries, err)
	}
}

func TestLedgerPathAndLockBudget(t *testing.T) {
	memoryPath := filepath.Join(t.TempDir(), "memory.sqlite")
	path, err := efficacy.DefaultLedgerPath(memoryPath)
	if err != nil || path != memoryPath+".efficacy.sqlite" {
		t.Fatalf("ledger path = %q, %v", path, err)
	}
	for _, bad := range []string{"", ":memory:", "file:test", "a?mode=memory"} {
		if _, err := efficacy.DefaultLedgerPath(bad); err == nil {
			t.Fatalf("invalid memory path accepted: %q", bad)
		}
	}
	ledger, err := efficacy.OpenLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	connection, err := sql.Open("sqlite", path+"?_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	tx, err := connection.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	start := time.Now()
	if err := ledger.Append(observedEvent("locked")); err == nil {
		t.Fatal("write under lock succeeded")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("locked append took %v", elapsed)
	}
}

func TestLedgerConcurrentWriters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.sqlite")
	first, err := efficacy.OpenLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := efficacy.OpenLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	var wg sync.WaitGroup
	errs := make(chan error, 32)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ledger := first
			if i%2 == 0 {
				ledger = second
			}
			if err := ledger.Append(observedEvent(fmt.Sprintf("event-%d", i%16))); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	entries, err := first.Events(0, 100)
	if err != nil || len(entries) != 16 {
		t.Fatalf("entries = %d, err = %v", len(entries), err)
	}
}

func TestLedgerIndexesOnExistingVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.sqlite")
	ledger, err := efficacy.OpenLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	ledger.Close()
	connection, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if _, err := connection.Exec(`DROP INDEX efficacy_event_scope_harness; DROP INDEX efficacy_event_target`); err != nil {
		t.Fatal(err)
	}
	ledger, err = efficacy.OpenLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	ledger.Close()
	for _, name := range []string{"efficacy_event_scope_harness", "efficacy_event_target"} {
		var count int
		if err := connection.QueryRow("SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name = ?", name).Scan(&count); err != nil || count != 1 {
			t.Fatalf("index %s: count=%d err=%v", name, count, err)
		}
	}
}

func TestLedgerVersionGuard(t *testing.T) {
	for _, version := range []int{0, 2} {
		path := filepath.Join(t.TempDir(), "ledger.sqlite")
		connection, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := connection.Exec(fmt.Sprintf("CREATE TABLE unrelated (value TEXT); PRAGMA user_version = %d", version)); err != nil {
			t.Fatal(err)
		}
		connection.Close()
		if ledger, err := efficacy.OpenLedger(path); err == nil {
			ledger.Close()
			t.Fatalf("version %d accepted", version)
		}
	}
}
