// plugin: rolling-memory v0.0.58
package rollingmemory

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

// TestROIFactsDailySeriesCarryAdoption verifies the 14-day series exposes the
// post-hoc adoption audit per ingest day: only active crystallizations count,
// settled outcomes split by tool/context adoption, pending stays out of the rate,
// and buckets from earlier days aggregate independently.
func TestROIFactsDailySeriesCarryAdoption(t *testing.T) {
	home, path := adoptionTestHome(t)
	memDB, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	// Anchor fixtures at local noon so "today"-relative offsets stay inside
	// today's bucket even when the suite runs just after midnight.
	nowT := time.Now()
	nowT = time.Date(nowT.Year(), nowT.Month(), nowT.Day(), 12, 0, 0, 0, time.Local)
	nowUnix := nowT.Unix()
	yNoon := nowT.AddDate(0, 0, -1)
	yNoon = time.Date(yNoon.Year(), yNoon.Month(), yNoon.Day(), 12, 0, 0, 0, time.Local)
	yDate := yNoon.Format("2006-01-02")
	if _, err := memDB.Exec(`
		CREATE TABLE db_meta (key TEXT PRIMARY KEY, value TEXT);
		CREATE TABLE rm_units (
			doc_id TEXT PRIMARY KEY, layer TEXT, occurred_at INTEGER, raw_text TEXT,
			sentences_json TEXT, address_uri TEXT, waveform_bar TEXT, kept_indices_json TEXT, locus TEXT
		);
		CREATE TABLE rm_noetic (
			noetic_id TEXT PRIMARY KEY, occurred_at INTEGER, status TEXT,
			query_text TEXT, answer_text TEXT, cited_unit_ids_json TEXT,
			cited_sentences_json TEXT, address_uri TEXT, waveform_bar TEXT,
			assistant_id TEXT, incarnation_id TEXT
		);
		INSERT INTO db_meta VALUES ('created_at', '1793350000'), ('engine', 'rolling-memory');
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := memDB.Exec(`
		INSERT INTO rm_units VALUES
			('ep_src_1', 'episodic', ?, 'Fix located in admin/internal/rmconsole/roi_facts.go and ComputeDBFingerprint.', '[]', 'C6:L3:B001#01', '', '[]', 'Rolling-Memory'),
			('ep_src_2', 'episodic', ?, 'Base zero-shot score is 0.362 on ModernBERT-large.', '[]', 'C6:L3:B002#01', '', '[]', 'Rolling-Memory');
	`, nowUnix-200, nowUnix-190); err != nil {
		t.Fatal(err)
	}
	if _, err := memDB.Exec(`
		INSERT INTO rm_noetic VALUES
			('nt_tool_1', ?, 'active', 'ComputeDBFingerprint bug', 'Check admin/internal/rmconsole/roi_facts.go and ComputeDBFingerprint', '["ep_src_1"]', '["admin/internal/rmconsole/roi_facts.go"]', 'C6:L3:B001#05', '', 'default', 'inc1'),
			('nt_ctx_2', ?, 'active', 'laya CPU performance', 'Base zero-shot score is 0.362 on ModernBERT-large', '["ep_src_2"]', '["ModernBERT-large 0.362"]', 'C6:L3:B002#05', '', 'default', 'inc1'),
			('nt_unadopted_3', ?, 'active', 'unrelated query token_xyz', 'Check internal/store/unused_file.go', '[]', '["internal/store/unused_file.go"]', 'C6:L3:B003#01', '', 'default', 'inc1'),
			('nt_pending_4', ?, 'active', 'latest query', 'Check cmd/rmem/main.go', '[]', '["cmd/rmem/main.go"]', 'C6:L3:B004#01', '', 'default', 'inc1'),
			('nt_stale_5', ?, 'stale', 'retired query', 'Retired answer', '[]', '["retired/anchor.go"]', 'C6:L3:B005#01', '', 'default', 'inc1'),
			('nt_yesterday_6', ?, 'active', 'yesterday query', 'yesterday answer', '[]', '["yesterday/anchor.go"]', 'C6:L3:B006#01', '', 'default', 'inc1');
	`, nowUnix-100, nowUnix-60, nowUnix-30, nowUnix-5, nowUnix-110, yNoon.Unix()); err != nil {
		t.Fatal(err)
	}
	_ = memDB.Close()

	lines := adoptionRecallLines("call_tool_1", "ComputeDBFingerprint bug",
		"<rolling-memory-recall scope=\"mem:self\" results=\"1\" absent=\"false\">\nFix located in admin/internal/rmconsole/roi_facts.go and ComputeDBFingerprint.\n</rolling-memory-recall>", nowUnix-99)
	lines = append(lines, adoptionToolUseLine("call_tool_1_read", "Read",
		`{"file_path":"/workspace/Rolling-Memory/admin/internal/rmconsole/roi_facts.go"}`, nowUnix-96))
	lines = append(lines, adoptionRecallLines("call_ctx_2", "laya CPU performance",
		"Base zero-shot score is 0.362 on ModernBERT-large.", nowUnix-59)...)
	lines = append(lines, adoptionAssistantTextLine(
		"Because laya uses ModernBERT-large with 0.362 base score, we should use intersection auditing.", nowUnix-56))
	lines = append(lines, adoptionRecallLines("call_unadopted_3", "unrelated query token_xyz",
		"Check internal/store/unused_file.go.", nowUnix-29)...)
	lines = append(lines, adoptionAssistantTextLine("Mentioning only echoed token_xyz in reply.", nowUnix-26))
	lines = append(lines, adoptionRecallLines("call_pending_4", "latest query",
		"Check cmd/rmem/main.go.", nowUnix-4)...)
	writeAdoptionJSONL(t, home, lines)

	got := queryROIFacts(context.Background(), path, roiFactsRequest{Scope: "mem:self"})
	if len(got.DailySeries) != 14 {
		t.Fatalf("expected 14 daily buckets, got %d", len(got.DailySeries))
	}
	var totAdopted, totTool, totCtx, totUnadopted, totPending, totNoetic int64
	for _, b := range got.DailySeries {
		totAdopted += b.AdoptedCount
		totTool += b.AdoptedToolCount
		totCtx += b.AdoptedContextCount
		totUnadopted += b.UnadoptedCount
		totPending += b.PendingCount
		totNoetic += b.NoeticCount
		audited := b.AdoptedCount + b.UnadoptedCount
		if audited > 0 {
			if want := roundFloat(float64(b.AdoptedCount)*100.0/float64(audited), 1); b.AdoptionRatePct != want {
				t.Fatalf("bucket %s adoption_rate_pct=%.1f, want %.1f", b.Date, b.AdoptionRatePct, want)
			}
		} else if b.AdoptionRatePct != 0 {
			t.Fatalf("bucket %s adoption_rate_pct=%.1f without audited recalls, want 0", b.Date, b.AdoptionRatePct)
		}
		if b.Date == yDate {
			if b.NoeticCount != 1 || b.AdoptedCount != 0 || b.PendingCount+b.UnadoptedCount != 1 {
				t.Fatalf("yesterday bucket must isolate its own settled outcome: %+v", b)
			}
		}
	}
	if totAdopted != 2 || totTool != 1 || totCtx != 1 {
		t.Fatalf("adopted totals tool=%d ctx=%d (want 1/1), got %+v", totTool, totCtx, got.DailySeries)
	}
	if totUnadopted+totPending != 3 {
		t.Fatalf("settled+pending totals mismatch: unadopted=%d pending=%d", totUnadopted, totPending)
	}
	if totNoetic != 5 {
		t.Fatalf("noetic totals must count active crystallizations only (stale excluded), got %d", totNoetic)
	}
}
