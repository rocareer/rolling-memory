// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package api_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/rocareer/rolling-memory/internal/api"
	"github.com/rocareer/rolling-memory/internal/store"
)

func TestImportMirrorsAllEligiblePendingTracesWithoutConsumingRestrictedRows(t *testing.T) {
	memory := newMemory(t)
	beforeImport := testNow - 1
	for i := 0; i < 140; i++ {
		audience := "shared"
		if i%2 == 0 {
			audience = "private"
		}
		if _, inserted, err := memory.Store.AddTrace("mem:test", fmt.Sprintf("backlog witness %d", i), store.TraceOptions{Session: "backlog", Turn: i, Audience: audience, RecordedAt: &beforeImport}); err != nil || !inserted {
			t.Fatalf("add trace %d: inserted=%v err=%v", i, inserted, err)
		}
	}
	if _, err := memory.Store.DB().Exec(`PRAGMA ignore_check_constraints = ON`); err != nil {
		t.Fatal(err)
	}
	if _, err := memory.Store.DB().Exec(`UPDATE trace SET audience = 'unknown' WHERE session = 'backlog' AND turn = 0`); err != nil {
		t.Fatal(err)
	}
	if _, err := memory.Store.DB().Exec(`PRAGMA ignore_check_constraints = OFF`); err != nil {
		t.Fatal(err)
	}
	path := writeTranscript(t, "new-shared.txt", "new shared witness\n")
	if _, err := memory.ImportFile(path, nil, nil, "shared", false); err != nil {
		t.Fatal(err)
	}
	var docs, restrictedDocs, restrictedPending int
	if err := memory.Store.DB().QueryRow(`SELECT count(*), count(*) FILTER (WHERE raw_text IN (SELECT text FROM trace WHERE audience NOT IN ('shared','public'))) FROM thin_vault_docs`).Scan(&docs, &restrictedDocs); err != nil {
		t.Fatal(err)
	}
	if err := memory.Store.DB().QueryRow(`SELECT count(*) FROM trace WHERE audience NOT IN ('shared','public') AND processed = 0`).Scan(&restrictedPending); err != nil {
		t.Fatal(err)
	}
	if docs != 71 || restrictedDocs != 0 || restrictedPending != 70 {
		t.Fatalf("docs=%d restricted docs=%d restricted pending=%d", docs, restrictedDocs, restrictedPending)
	}
	if _, err := memory.ImportFile(path, nil, nil, "shared", false); err != nil {
		t.Fatal(err)
	}
	if err := memory.Store.DB().QueryRow(`SELECT count(*) FROM thin_vault_docs`).Scan(&docs); err != nil || docs != 71 {
		t.Fatalf("repeat docs=%d err=%v", docs, err)
	}
}

func TestImportSharedTraceSurvivesPrivatePendingBacklogAndReplay(t *testing.T) {
	memory := newMemory(t)
	beforeImport := testNow - 1
	for i := 0; i < 70; i++ {
		_, inserted, err := memory.Store.AddTrace("mem:test", fmt.Sprintf("private backlog item %d", i), store.TraceOptions{Session: "backlog", Turn: i + 1, Audience: "private", RecordedAt: &beforeImport})
		if err != nil || !inserted {
			t.Fatalf("add private trace %d: inserted=%v err=%v", i, inserted, err)
		}
	}
	path := writeTranscript(t, "shared-import.txt", "zirconium telescope unique evidence\n")
	if _, err := memory.ImportFile(path, nil, nil, "shared", true); err != nil {
		t.Fatal(err)
	}
	var sharedDocs, privateDocs int
	if err := memory.Store.DB().QueryRow(`SELECT count(*) FROM thin_vault_docs WHERE raw_text LIKE '%zirconium telescope unique evidence%'`).Scan(&sharedDocs); err != nil {
		t.Fatal(err)
	}
	if err := memory.Store.DB().QueryRow(`SELECT count(*) FROM thin_vault_docs WHERE raw_text LIKE '%private backlog item%'`).Scan(&privateDocs); err != nil {
		t.Fatal(err)
	}
	if privateDocs != 0 {
		t.Fatalf("private backlog mirrored: %d", privateDocs)
	}
	if sharedDocs != 1 {
		t.Fatalf("new shared trace missing from vault after replay: %d", sharedDocs)
	}
	result, err := memory.Recall(api.RecallRequest{Cue: "zirconium telescope unique evidence", NoCrystallize: true})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, hit := range result.RawHits {
		if text, _ := hit["text"].(string); strings.Contains(text, "zirconium telescope unique evidence") {
			found = true
		}
	}
	if !found {
		t.Fatalf("new shared trace not retrievable after replay: %#v", result.ToDict())
	}
}
