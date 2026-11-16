// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package store

import "testing"

func TestRevivalPreservesHistoricalAudience(t *testing.T) {
	store := newTestStore(t, testScope)
	id, err := store.EnsureNode(testScope, "referent", "historical", NodeOptions{Audience: "private"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.conn.Exec(`UPDATE node SET status = 'merged', retired_at = ? WHERE id = ?`, testNow, id); err != nil {
		t.Fatal(err)
	}
	got, err := store.EnsureNode(testScope, "referent", "historical", NodeOptions{Audience: "shared"})
	if err != nil || got != id {
		t.Fatalf("revive = %q err=%v", got, err)
	}
	var audience string
	if err := store.conn.QueryRow(`SELECT audience FROM node WHERE id = ?`, id).Scan(&audience); err != nil || audience != "private" {
		t.Fatalf("audience = %q err=%v", audience, err)
	}
}
