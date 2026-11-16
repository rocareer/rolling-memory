// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package store

import "testing"

// The thin-vault status branch must report the configured mode rather than a constant:
// a local-mode database is loopback-only, and a cloud-mode one is remote-allowed.
func TestThinVaultStatusReportsConfiguredMode(t *testing.T) {
	store := newTestStore(t, testScope)
	if _, err := store.conn.Exec(`DROP TABLE assertion`); err != nil {
		t.Fatalf("drop legacy assertion table: %v", err)
	}

	store.Config.Mode = "local"
	status, err := store.Status(testScope)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status["mode"] != "local" {
		t.Fatalf("mode = %v, want local", status["mode"])
	}
	if status["egress"] != "loopback-only" {
		t.Fatalf("egress = %v, want loopback-only", status["egress"])
	}

	store.Config.Mode = "cloud"
	status, err = store.Status(testScope)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status["mode"] != "cloud" {
		t.Fatalf("mode = %v, want cloud", status["mode"])
	}
	if status["egress"] != "remote-allowed" {
		t.Fatalf("egress = %v, want remote-allowed", status["egress"])
	}
}
