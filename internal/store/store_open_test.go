// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package store

import (
	"path/filepath"
	"testing"

	"github.com/rocareer/rolling-memory/internal/db"
)

func TestOpenCreatesSchemaAndFTS(t *testing.T) {
	store := newTestStore(t, testScope)
	if !store.fts {
		t.Fatal("fts = false on a fresh database")
	}
	var tables int
	if err := store.conn.QueryRow(
		"SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name IN ('node_fts', 'trace_fts')",
	).Scan(&tables); err != nil {
		t.Fatalf("fts tables: %v", err)
	}
	if tables != 2 {
		t.Fatalf("fts tables = %d", tables)
	}
	status, err := store.Status(testScope)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status["schema_version"] != int64(1) {
		t.Fatalf("schema_version = %v", status["schema_version"])
	}
}

func TestOpenRejectsNewerSchema(t *testing.T) {
	freezeClock(t)
	directory := t.TempDir()
	dbPath := filepath.Join(directory, "memory.db")
	connection, err := db.Connect(dbPath, db.LocalProfile())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if _, err := connection.Exec("PRAGMA user_version = 99"); err != nil {
		connection.Close()
		t.Fatalf("set user_version: %v", err)
	}
	connection.Close()
	cfg := loadTestConfig(t, testScope, dbPath)
	if _, err := Open(cfg); err == nil {
		t.Fatal("expected newer-schema error")
	}
}

func TestOpenUSBProfileAppliesPragmas(t *testing.T) {
	freezeClock(t)
	directory := t.TempDir()
	dbPath := filepath.Join(directory, "memory.db")
	cfg := loadTestConfig(t, testScope, dbPath)
	cfg.Storage.Profile = "usb"
	store, err := Open(cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()
	var synchronous int
	if err := store.conn.QueryRow("PRAGMA synchronous").Scan(&synchronous); err != nil {
		t.Fatalf("synchronous: %v", err)
	}
	if synchronous != 2 {
		t.Fatalf("synchronous = %d, want 2 (FULL)", synchronous)
	}
	var busyTimeout int
	if err := store.conn.QueryRow("PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		t.Fatalf("busy_timeout: %v", err)
	}
	if busyTimeout != 15000 {
		t.Fatalf("busy_timeout = %d, want 15000", busyTimeout)
	}
}

func TestOpenAutoProfileUsesLocal(t *testing.T) {
	freezeClock(t)
	directory := t.TempDir()
	dbPath := filepath.Join(directory, "memory.db")
	cfg := loadTestConfig(t, testScope, dbPath)
	cfg.Storage.Profile = "auto"
	store, err := Open(cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()
	if store.profile.Name != "local" {
		t.Fatalf("profile = %q, want local", store.profile.Name)
	}
}
