// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package hooks

import (
	"database/sql"
	"testing"

	"github.com/rocareer/rolling-memory/internal/thinclient"
)

func TestHistoricalTablesSurviveStartupAndHooks(t *testing.T) {
	newTestEnv(t)
	memory := openMemory(t)
	connection := memory.Store.DB()
	for _, statement := range []string{
		`INSERT INTO node(id, scope, kind, label, norm_label, status, audience, created_at) VALUES ('historic', 'mem:hooks', 'referent', 'old', 'old', 'active', 'private', 1)`,
		`CREATE TABLE history_guard (payload TEXT)`,
		`INSERT INTO history_guard VALUES ('original')`,
	} {
		if _, err := connection.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	check := func(db *sql.DB) {
		t.Helper()
		var audience, payload string
		if err := db.QueryRow(`SELECT audience FROM node WHERE id = 'historic'`).Scan(&audience); err != nil || audience != "private" {
			t.Fatalf("historic node: audience=%q err=%v", audience, err)
		}
		if err := db.QueryRow(`SELECT payload FROM history_guard`).Scan(&payload); err != nil || payload != "original" {
			t.Fatalf("historical payload=%q err=%v", payload, err)
		}
	}
	for i := 0; i < 2; i++ {
		thinclient.PurgeLegacyTables(connection)
		if _, err := memory.ThinClient(); err != nil {
			t.Fatal(err)
		}
		if _, err := runHook(loadConfig(t), "start", map[string]any{}, Options{}); err != nil {
			t.Fatal(err)
		}
		if _, err := runHook(loadConfig(t), "close", map[string]any{}, Options{}); err != nil {
			t.Fatal(err)
		}
		check(connection)
	}
}
