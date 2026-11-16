// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package store

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/rocareer/rolling-memory/internal/clock"
	"github.com/rocareer/rolling-memory/internal/config"
)

const testNow = int64(1793102400) // 2026-10-27T12:00:00Z

func TestMain(m *testing.M) {
	previous := time.Local
	time.Local = time.UTC
	code := m.Run()
	time.Local = previous
	os.Exit(code)
}

func freezeClock(t *testing.T) {
	t.Helper()
	restore := clock.SetForTest(time.Unix(testNow, 0), 1_000_000)
	t.Cleanup(restore)
}

func newTestStore(t *testing.T, scope string) *Store {
	t.Helper()
	freezeClock(t)
	directory := t.TempDir()
	return openStoreAt(t, scope, filepath.Join(directory, "memory.db"))
}

func openStoreAt(t *testing.T, scope, dbPath string) *Store {
	t.Helper()
	cfg := loadTestConfig(t, scope, dbPath)
	store, err := Open(cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func loadTestConfig(t *testing.T, scope, dbPath string) *config.Config {
	t.Helper()
	directory := filepath.Dir(dbPath)
	configPath := filepath.Join(directory, "config.toml")
	mode := "local"
	reader := "deliverable"
	cfg, err := config.Load(config.LoadOptions{
		Path:   &configPath,
		DB:     &dbPath,
		Mode:   &mode,
		Scope:  &scope,
		Reader: &reader,
		Env:    map[string]string{},
	})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	cfg.Log = filepath.Join(directory, "state", "rolling-memory.log")
	return cfg
}

func strPtr(value string) *string { return &value }

func boolPtr(value bool) *bool { return &value }

func countRows(t *testing.T, store *Store, table string) int64 {
	t.Helper()
	count, err := queryCount(store.conn, "SELECT count(*) FROM "+table)
	if err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return count
}

type factOptions struct {
	TValidFrom int64
	TSource    int64
	Warrant    string
	Audience   string
	Bare       bool
	Holder     string
	Session    string
}

func addFact(t *testing.T, store *Store, scope, text, subject, relation, object, anchor string, o factOptions) map[string]any {
	t.Helper()
	subjectID, err := store.EnsureNode(scope, "referent", subject, NodeOptions{})
	if err != nil {
		t.Fatalf("ensure subject: %v", err)
	}
	relationID, err := store.EnsureNode(scope, "relation_type", relation, NodeOptions{})
	if err != nil {
		t.Fatalf("ensure relation: %v", err)
	}
	objectID, err := store.EnsureNode(scope, "referent", object, NodeOptions{})
	if err != nil {
		t.Fatalf("ensure object: %v", err)
	}
	var holderID *string
	if o.Holder != "" {
		value, err := store.EnsureNode(scope, "referent", o.Holder, NodeOptions{})
		if err != nil {
			t.Fatalf("ensure holder: %v", err)
		}
		holderID = &value
	}
	session := o.Session
	if session == "" {
		session = "default"
	}
	traceHash, _, err := store.AddTrace(scope, text, TraceOptions{
		Session:  session,
		Turn:     0,
		Kind:     "message",
		Author:   "user",
		Audience: "shared",
	})
	if err != nil {
		t.Fatalf("add trace: %v", err)
	}
	byteIndex := strings.Index(text, anchor)
	if byteIndex < 0 {
		t.Fatalf("anchor %q is not found in %q", anchor, text)
	}
	anchorStart := utf8.RuneCountInString(text[:byteIndex])
	anchorEnd := anchorStart + utf8.RuneCountInString(anchor)
	warrant := o.Warrant
	if warrant == "" {
		warrant = "referential"
	}
	audience := o.Audience
	if audience == "" {
		audience = "shared"
	}
	request := AppendAssertionRequest{
		Scope:           scope,
		RelID:           relationID,
		AnchorTraceHash: traceHash,
		AnchorStart:     anchorStart,
		AnchorEnd:       anchorEnd,
		SubjectID:       &subjectID,
		ObjectID:        &objectID,
		Warrant:         warrant,
		Audience:        audience,
		Confidence:      0.8,
		TValidFrom:      o.TValidFrom,
		HolderID:        holderID,
		Bare:            o.Bare,
		Status:          "active",
		CreatedBy:       "user",
	}
	if o.TSource != 0 {
		request.TSource = &o.TSource
	}
	receipt, err := store.AppendAssertion(request)
	if err != nil {
		t.Fatalf("append assertion: %v", err)
	}
	return receipt
}

func decodeJSONMap(t *testing.T, value string) map[string]any {
	t.Helper()
	result := map[string]any{}
	if err := json.Unmarshal([]byte(value), &result); err != nil {
		t.Fatalf("decode %s: %v", value, err)
	}
	return result
}

func queryText(t *testing.T, store *Store, query string, args ...any) string {
	t.Helper()
	var value sql.NullString
	if err := store.conn.QueryRow(query, args...).Scan(&value); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return value.String
}
