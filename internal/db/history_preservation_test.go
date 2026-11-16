// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package db

import (
	"path/filepath"
	"testing"
)

func TestHistoricalOrphanTablesSurviveRepeatedMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.db")
	connection, err := Connect(path, LocalProfile())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	for _, table := range []string{"assertion_ext", "participant", "restate", "node_ext", "node_vector", "episode_member", "episode", "memory_hot", "noetic"} {
		execSQL(t, connection, "CREATE TABLE "+table+" (payload TEXT)")
		execSQL(t, connection, "INSERT INTO "+table+" VALUES ('historical')")
	}
	for i := 0; i < 2; i++ {
		if err := EnsureMemcoreSchema(connection); err != nil {
			t.Fatal(err)
		}
		for _, table := range []string{"assertion_ext", "participant", "restate", "node_ext", "node_vector", "episode_member", "episode", "memory_hot", "noetic"} {
			if got := queryText(t, connection, "SELECT payload FROM "+table); got != "historical" {
				t.Fatalf("%s payload = %q", table, got)
			}
		}
	}
}

func TestHistoricalMetaEdgeSurvivesRepeatedMigration(t *testing.T) {
	connection, _ := connectMigrated(t, LocalProfile())
	execSQL(t, connection, `DROP TABLE meta_edge`)
	ddl := `CREATE TABLE meta_edge (id INTEGER PRIMARY KEY, meta_kind TEXT CHECK(meta_kind IN ('same-as')), payload TEXT)`
	execSQL(t, connection, ddl)
	execSQL(t, connection, `INSERT INTO meta_edge VALUES (1, 'same-as', 'historical')`)
	execSQL(t, connection, `CREATE TABLE meta_edge_v2 (payload TEXT)`)
	execSQL(t, connection, `INSERT INTO meta_edge_v2 VALUES ('existing v2')`)
	for i := 0; i < 2; i++ {
		if _, err := Migrate(connection); err != nil {
			t.Fatal(err)
		}
		if got := queryText(t, connection, `SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'meta_edge'`); got != ddl {
			t.Fatalf("meta_edge schema changed: %s", got)
		}
		if got := queryText(t, connection, `SELECT payload FROM meta_edge WHERE id = 1`); got != "historical" {
			t.Fatalf("meta_edge payload = %q", got)
		}
		if got := queryText(t, connection, `SELECT payload FROM meta_edge_v2`); got != "existing v2" {
			t.Fatalf("meta_edge_v2 payload = %q", got)
		}
	}
}
