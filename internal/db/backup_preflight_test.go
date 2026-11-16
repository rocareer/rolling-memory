// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package db

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBackupPreflightReportsExternalReferencesWithoutWriting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "memory?#.db")
	connection, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: path}).String())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	for _, statement := range []string{
		`CREATE TABLE thin_vault_docs (doc_id TEXT, raw_text TEXT)`,
		`CREATE TABLE thin_vault_bg_files (abs_path TEXT)`,
		`CREATE TABLE thin_vault_bg_chunks (unit_id TEXT, abs_path TEXT)`,
		`CREATE TABLE thin_vault_bg_dirs (dir_id TEXT, root_path TEXT)`,
		`PRAGMA journal_mode = WAL`,
	} {
		if _, err := connection.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	present := filepath.Join(dir, "source.jsonl")
	if err := os.WriteFile(present, []byte("user original"), 0o600); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "missing.jsonl")
	for _, row := range [][2]string{
		{"present", "@ref:jsonl:" + present + ":0#0"},
		{"missing", "@ref:jsonl:" + missing + ":0#1"},
		{"zcode", "@ref:zcode:s:m#0"},
		{"unknown", "@ref:other:opaque"},
		{"invalid", "@ref:jsonl:bad"},
		{"ordinary", "private text must not be returned"},
	} {
		if _, err := connection.Exec(`INSERT INTO thin_vault_docs VALUES (?, ?)`, row[0], row[1]); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := connection.Exec(`INSERT INTO thin_vault_bg_files VALUES (?)`, missing); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Exec(`INSERT INTO thin_vault_bg_chunks VALUES (?, ?)`, "chunk", present); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Exec(`INSERT INTO thin_vault_bg_dirs VALUES (?, ?)`, "mount", dir); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(present)
	if err != nil {
		t.Fatal(err)
	}
	report, err := BackupPreflight(context.Background(), path, filepath.Join(dir, "missing-zcode.db"))
	if err != nil {
		t.Fatal(err)
	}
	if report.Complete || report.SelfContained || len(report.References) != 8 {
		t.Fatalf("report = %+v", report)
	}
	want := map[string]string{
		"present": "path_present_unverified", "missing": "missing", "zcode": "missing",
		"unknown": "unsupported", "invalid": "invalid", missing: "missing",
		"chunk": "path_present_unverified", "mount": "path_present_unverified",
	}
	for _, ref := range report.References {
		if ref.Status != want[ref.ID] {
			t.Errorf("reference %+v, want %q", ref, want[ref.ID])
		}
		if strings.Contains(ref.Reference, "user original") || strings.Contains(ref.Reference, "private text") {
			t.Errorf("preflight disclosed content: %+v", ref)
		}
	}
	after, err := os.ReadFile(present)
	if err != nil || string(before) != string(after) {
		t.Fatalf("source changed: %q, %v", after, err)
	}
}

func TestBackupPreflightRejectsMissingOrInvalidDatabase(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing", "memory.db")
	if _, err := BackupPreflight(context.Background(), missing, ""); err == nil {
		t.Fatal("missing database accepted")
	}
	if _, err := os.Stat(filepath.Dir(missing)); !os.IsNotExist(err) {
		t.Fatalf("preflight created parent directory: %v", err)
	}
	invalid := filepath.Join(dir, "invalid.db")
	if err := os.WriteFile(invalid, []byte("not a database"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := BackupPreflight(context.Background(), invalid, ""); err == nil {
		t.Fatal("invalid database accepted")
	}
	if data, err := os.ReadFile(invalid); err != nil || string(data) != "not a database" {
		t.Fatalf("invalid database changed: %q, %v", data, err)
	}
}

func TestBackupPreflightAbsentTablesNotClaimedComplete(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.db")
	connection, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Exec(`CREATE TABLE unrelated (id INTEGER)`); err != nil {
		t.Fatal(err)
	}
	connection.Close()
	report, err := BackupPreflight(context.Background(), path, "")
	if err != nil {
		t.Fatal(err)
	}
	if report.Complete || report.SelfContained || len(report.References) != 0 || len(report.Warnings) < 7 {
		t.Fatalf("report = %+v", report)
	}
}
