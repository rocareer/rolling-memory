// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package api_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/rocareer/rolling-memory/internal/api"
	"github.com/rocareer/rolling-memory/internal/config"
	"github.com/rocareer/rolling-memory/internal/ids"
	"github.com/rocareer/rolling-memory/internal/thinclient"
)

func TestVaultScopePreflightBeforeOpen(t *testing.T) {
	for _, mode := range []string{"local", "cloud"} {
		for _, table := range []string{"trace", "noetic", "thin_vault_docs", "unexpected_user_data", "foreign_fts_data"} {
			t.Run(mode+"/"+table, func(t *testing.T) {
				dir := t.TempDir()
				path := filepath.Join(dir, "memory?#.db")
				setup, err := sql.Open("sqlite", ids.SQLiteFileURI(filepath.Join(dir, "fixture.db")))
				if err != nil {
					t.Fatal(err)
				}
				if _, err := setup.Exec(`CREATE TABLE "` + table + `" (value TEXT); INSERT INTO "` + table + `" VALUES ('private witness')`); err != nil {
					t.Fatal(err)
				}
				if err := setup.Close(); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(filepath.Join(dir, "fixture.db"), path); err != nil {
					t.Fatal(err)
				}
				before, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				scope := "mem:test"
				configPath := filepath.Join(dir, "config.toml")
				cfg, err := config.Load(config.LoadOptions{Path: &configPath, DB: &path, Mode: &mode, Scope: &scope, Env: map[string]string{}})
				if err != nil {
					t.Fatal(err)
				}
				cfg.Log = filepath.Join(dir, "state", "test.log")
				memory, err := api.New(cfg)
				if err == nil {
					memory.Close()
					t.Fatal("unbound populated database accepted")
				}
				after, err := os.ReadFile(path)
				if err != nil || string(after) != string(before) {
					t.Fatalf("database changed on rejected open: %v", err)
				}
				verify, err := sql.Open("sqlite", ids.SQLiteFileURI(path)+"?mode=ro")
				if err != nil {
					t.Fatal(err)
				}
				defer verify.Close()
				var value string
				if err := verify.QueryRow(`SELECT value FROM "` + table + `"`).Scan(&value); err != nil || value != "private witness" {
					t.Fatalf("original row changed: %q, %v", value, err)
				}
			})
		}
	}
}

func TestVaultScopePreflightAcceptsMatchingAndRejectsOtherScope(t *testing.T) {
	memory := newMemory(t)
	client, err := memory.ThinClient()
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Vault.SaveLocalPlaintext("scope-witness", testNow, "private witness", nil, thinclient.CloudIngestResponse{Layer: "episodic"}); err != nil {
		t.Fatal(err)
	}
	cfg := *memory.Config
	for _, mode := range []string{"local", "cloud"} {
		cfg.Mode = mode
		matching, err := api.New(&cfg)
		if err != nil {
			t.Fatalf("matching %s: %v", mode, err)
		}
		if _, err := matching.ThinClient(); err != nil {
			matching.Close()
			t.Fatalf("matching thinclient %s: %v", mode, err)
		}
		matching.Close()
		other := cfg
		other.Scope = "mem:other"
		if opened, err := api.New(&other); err == nil {
			opened.Close()
			t.Fatalf("mismatched %s scope accepted", mode)
		}
	}
}
