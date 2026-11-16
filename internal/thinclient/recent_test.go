// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package thinclient_test

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/rocareer/rolling-memory/internal/thinclient"
)

// TestRecentDocsTimeOrderAcrossLayers pins the recent-mode primitive: entries
// come back newest-first across document rows and active Noetic records, with
// the limit applied after the merge, and no semantic matching involved.
func TestRecentDocsTimeOrderAcrossLayers(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "recent.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	vault, err := thinclient.NewVault(db, []byte("recent-primitive-secret-2026"))
	if err != nil {
		t.Fatalf("NewVault: %v", err)
	}

	base := time.Date(2026, 11, 7, 9, 0, 0, 0, time.UTC).Unix()
	for index, doc := range []struct {
		id    string
		text  string
		at    int64
		layer string
	}{
		{"ep_old", "旧一轮对话", base - 300, "episodic"},
		{"bg_new", "最新挂载目录变更", base - 60, "background"},
		{"ep_mid", "中间一轮对话", base - 120, "episodic"},
	} {
		if err := vault.SaveLocalPlaintext(doc.id, doc.at, doc.text, []string{doc.text}, thinclient.CloudIngestResponse{}); err != nil {
			t.Fatalf("save doc %d: %v", index, err)
		}
		if _, err := db.Exec(`UPDATE thin_vault_docs SET layer = ? WHERE doc_id = ?`, doc.layer, doc.id); err != nil {
			t.Fatalf("stamp layer %d: %v", index, err)
		}
	}
	if err := vault.SaveLocalNoetic(thinclient.LocalNoeticRecord{
		NoeticID:   "nt_newest",
		OccurredAt: base - 10,
		Status:     "active",
		QueryText:  "最近改了什么",
		AnswerText: "刚更新了最近的记忆原语",
	}); err != nil {
		t.Fatalf("save noetic: %v", err)
	}
	if err := vault.SaveLocalNoetic(thinclient.LocalNoeticRecord{
		NoeticID:   "nt_inactive",
		OccurredAt: base,
		Status:     "retired",
		QueryText:  "已停用的问题",
		AnswerText: "不应出现",
	}); err != nil {
		t.Fatalf("save inactive noetic: %v", err)
	}

	recent, err := vault.RecentDocs(10)
	if err != nil {
		t.Fatalf("RecentDocs: %v", err)
	}
	order := make([]string, 0, len(recent))
	for _, entry := range recent {
		order = append(order, entry.ID)
	}
	want := []string{"nt_newest", "bg_new", "ep_mid", "ep_old"}
	if len(order) != len(want) {
		t.Fatalf("recent = %v, want %v", order, want)
	}
	for index := range want {
		if order[index] != want[index] {
			t.Fatalf("recent order = %v, want %v", order, want)
		}
	}
	if recent[0].Layer != "noetic" {
		t.Fatalf("noetic layer label = %q", recent[0].Layer)
	}
	if recent[1].Layer != "background" || recent[2].Layer != "episodic" {
		t.Fatalf("doc layer labels = %q %q", recent[1].Layer, recent[2].Layer)
	}

	limited, err := vault.RecentDocs(2)
	if err != nil {
		t.Fatalf("RecentDocs(2): %v", err)
	}
	if len(limited) != 2 || limited[0].ID != "nt_newest" || limited[1].ID != "bg_new" {
		t.Fatalf("limited recent = %#v", limited)
	}
}
