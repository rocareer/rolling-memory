// plugin: rolling-memory v0.0.58
package rollingmemory

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func roiTestDB(t *testing.T, path string, version int) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+".efficacy.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE efficacy_event (
		seq INTEGER PRIMARY KEY AUTOINCREMENT, event_id TEXT NOT NULL UNIQUE,
		execution_id TEXT NOT NULL, session TEXT NOT NULL, scope TEXT NOT NULL,
		harness TEXT NOT NULL, hook TEXT NOT NULL, output_outcome TEXT NOT NULL,
		action TEXT NOT NULL, target_event_id TEXT NOT NULL, observed_at INTEGER NOT NULL,
		observation TEXT NOT NULL, text_sha256 TEXT NOT NULL, text_bytes INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if version == 1 {
		if _, err := db.Exec(`PRAGMA user_version = 1`); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func roiTestInsert(t *testing.T, db *sql.DB, id, scope, harness, action, outcome, target, observation string, bytes any) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO efficacy_event (event_id, execution_id, session, scope, harness, hook, output_outcome, action, target_event_id, observed_at, observation, text_sha256, text_bytes) VALUES (?, 'execution-secret', 'session-secret', ?, ?, 'hook', ?, ?, ?, 1234567890, ?, 'hash-secret', ?)`, id, scope, harness, outcome, action, target, observation, bytes)
	if err != nil {
		t.Fatal(err)
	}
}

func TestROIFactsIsolationAndRevisions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.db")
	db := roiTestDB(t, path, 1)
	roiTestInsert(t, db, "decision", "one", "host-a", "decision", "output", "", "observed", 50)
	roiTestInsert(t, db, "attempt", "one", "host-a", "output_attempt", "success", "", "observed", 50)
	roiTestInsert(t, db, "correct", "one", "host-a", "correction", "short_write", "attempt", "observed", 12)
	roiTestInsert(t, db, "prepared", "one", "host-a", "prepared", "not_applicable", "", "observed", 50)
	roiTestInsert(t, db, "remove", "one", "host-a", "retraction", "not_applicable", "prepared", "unknown", nil)
	roiTestInsert(t, db, "unknown", "one", "host-a", "decision", "unknown", "", "unknown", nil)
	roiTestInsert(t, db, "other-host", "one", "host-b", "decision", "empty", "", "unknown", nil)
	roiTestInsert(t, db, "other-scope", "two", "host-a", "decision", "empty", "", "unknown", nil)
	roiTestInsert(t, db, "unassigned", "", "host-a", "decision", "empty", "", "unknown", nil)
	got := queryROIFacts(context.Background(), path, roiFactsRequest{Scope: "one", Harness: "host-a", Limit: 2})
	if got.Status != "available" || got.Counts == nil || got.Counts.Total != 3 || got.Counts.Actions["decision"] != 2 || got.Counts.Actions["output_attempt"] != 1 || got.Counts.OutputOutcomes["short_write"] != 1 || got.Counts.KnownTextBytes != 62 || got.Counts.UnknownByteEvents != 1 || len(got.RecentEvents) != 2 {
		t.Fatalf("unexpected facts: %+v counts=%+v", got, got.Counts)
	}
	all := queryROIFacts(context.Background(), path, roiFactsRequest{Scope: "one"})
	if all.Counts == nil || all.Counts.Total != 4 {
		t.Fatalf("all harnesses: %+v", all)
	}
	for _, req := range []roiFactsRequest{{Scope: "two", Harness: "host-b"}, {Scope: "missing"}, {Scope: "ALL"}} {
		if got := queryROIFacts(context.Background(), path, req); got.Status != "unknown" || got.Counts != nil {
			t.Fatalf("expected unknown for %+v: %+v", req, got)
		}
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"session-secret", "execution-secret", "hash-secret"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("private data exposed: %s", raw)
		}
	}
}

func TestROIFactsCorrectionRestoresAndWrongIdentityIgnored(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.db")
	db := roiTestDB(t, path, 1)
	roiTestInsert(t, db, "original", "one", "host-a", "output_attempt", "success", "", "observed", 8)
	roiTestInsert(t, db, "remove", "one", "host-a", "retraction", "not_applicable", "original", "unknown", nil)
	roiTestInsert(t, db, "restore", "one", "host-a", "correction", "failure", "original", "observed", 3)
	roiTestInsert(t, db, "wrong-host", "one", "host-b", "retraction", "not_applicable", "original", "unknown", nil)
	if _, err := db.Exec(`INSERT INTO efficacy_event (event_id, execution_id, session, scope, harness, hook, output_outcome, action, target_event_id, observed_at, observation, text_sha256) VALUES ('wrong-hook','execution-secret','session-secret','one','host-a','other','not_applicable','retraction','original',123,'unknown','')`); err != nil {
		t.Fatal(err)
	}
	got := queryROIFacts(context.Background(), path, roiFactsRequest{Scope: "one", Harness: "host-a"})
	if got.Counts == nil || got.Counts.Total != 1 || got.Counts.OutputOutcomes["failure"] != 1 || got.Counts.KnownTextBytes != 3 {
		t.Fatalf("wrong revision handling: %+v counts=%+v", got, got.Counts)
	}
}

func TestROIFactsMissingAndVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.db")
	got := queryROIFacts(context.Background(), path, roiFactsRequest{Scope: "one"})
	if got.Status != "unknown" || got.Reason != "ledger_missing" {
		t.Fatalf("missing: %+v", got)
	}
	if _, err := os.Stat(path + ".efficacy.sqlite"); !os.IsNotExist(err) {
		t.Fatal("reader created ledger")
	}
	db := roiTestDB(t, path, 0)
	got = queryROIFacts(context.Background(), path, roiFactsRequest{Scope: "one"})
	if got.Status != "unknown" || got.Reason != "schema_version" {
		t.Fatalf("unversioned: %+v", got)
	}
	if _, err := db.Exec(`PRAGMA user_version = 2`); err != nil {
		t.Fatal(err)
	}
	got = queryROIFacts(context.Background(), path, roiFactsRequest{Scope: "one"})
	if got.Status != "unknown" || got.Reason != "schema_version" {
		t.Fatalf("newer version: %+v", got)
	}
}

func TestROIFactsHTTP(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.db")
	db := roiTestDB(t, path, 1)
	roiTestInsert(t, db, "one", "mem:self", "host", "output_attempt", "success", "", "observed", 4)
	if err := os.WriteFile(path, []byte("old database"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("replacement database"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ROLLING_MEMORY_DB", path)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/facts", roiFactsHandler)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/facts", strings.NewReader(`{"scope":"mem:self","harness":"host"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	var response struct {
		Code int              `json:"code"`
		Data roiFactsResponse `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || response.Code != 0 || response.Data.Status != "unknown" || response.Data.Reason != "ledger_identity_unverified" || response.Data.Counts != nil || len(response.Data.RecentEvents) != 0 || response.Data.Coverage != "unknown" || response.Data.IdentityStatus != "unknown" {
		t.Fatalf("response %s: %v", w.Body.String(), err)
	}
}

func TestROIFactsVerifiedIdentityHTTP(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.db")
	memDB, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := memDB.Exec(`
		CREATE TABLE db_meta (key TEXT PRIMARY KEY, value TEXT);
		CREATE TABLE thin_vault_docs (doc_id TEXT PRIMARY KEY, occurred_at INTEGER);
		INSERT INTO db_meta VALUES ('created_at', '1793350000'), ('engine', 'rolling-memory');
		INSERT INTO thin_vault_docs VALUES ('doc_root_1', 1793350001);
	`); err != nil {
		t.Fatal(err)
	}
	_ = memDB.Close()

	fp := computeAdminDBFingerprint(path, "mem:self")
	if fp == "" {
		t.Fatal("expected non-empty DB fingerprint")
	}
	db := roiTestDB(t, path, 1)
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS efficacy_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL); INSERT INTO efficacy_meta VALUES ('db_fingerprint', ?);`, fp); err != nil {
		t.Fatal(err)
	}
	roiTestInsert(t, db, "one", "mem:self", "zcode", "output_attempt", "success", "", "observed", 128)

	t.Setenv("ROLLING_MEMORY_DB", path)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/facts", roiFactsHandler)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/facts", strings.NewReader(`{"scope":"mem:self","harness":"zcode"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	var response struct {
		Code int              `json:"code"`
		Data roiFactsResponse `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || response.Code != 0 || response.Data.Status != "available" || response.Data.IdentityStatus != "verified" || response.Data.Counts == nil || response.Data.Counts.Total != 1 {
		t.Fatalf("verified response %s: %v", w.Body.String(), err)
	}
}

func TestROIFactsRecallWindowsAndVault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.db")
	memDB, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := memDB.Exec(`
		CREATE TABLE db_meta (key TEXT PRIMARY KEY, value TEXT);
		CREATE TABLE thin_vault_docs (doc_id TEXT PRIMARY KEY, occurred_at INTEGER, raw_text TEXT);
		CREATE TABLE thin_vault_noetic (noetic_id TEXT PRIMARY KEY, status TEXT, cited_unit_ids_json TEXT);
		CREATE TABLE injection_ledger (id INTEGER PRIMARY KEY AUTOINCREMENT, created_at INTEGER);
		INSERT INTO db_meta VALUES ('created_at', '1793350000'), ('engine', 'rolling-memory');
		INSERT INTO thin_vault_docs VALUES ('doc_root_1', 1793350001, 'real text');
		INSERT INTO thin_vault_docs VALUES ('doc_ref_1', 1793350002, '@ref:zcode:sess:msg');
		INSERT INTO thin_vault_docs VALUES ('doc_ref_2', 1793350003, '@ref:zcode:sess:msg2');
		INSERT INTO thin_vault_noetic VALUES ('no_1', 'active', '["unit:1"]');
		INSERT INTO thin_vault_noetic VALUES ('no_2', 'active', '[]');
		INSERT INTO thin_vault_noetic VALUES ('no_3', 'retracted', '["unit:2"]');
		INSERT INTO injection_ledger (created_at) VALUES (1793350004);
	`); err != nil {
		t.Fatal(err)
	}
	_ = memDB.Close()

	fp := computeAdminDBFingerprint(path, "mem:self")
	db := roiTestDB(t, path, 1)
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS efficacy_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL); INSERT INTO efficacy_meta VALUES ('db_fingerprint', ?);`, fp); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	insert := func(id, action, outcome string, bytes any, at time.Time) {
		t.Helper()
		if _, err := db.Exec(`INSERT INTO efficacy_event (event_id, execution_id, session, scope, harness, hook, output_outcome, action, target_event_id, observed_at, observation, text_sha256, text_bytes) VALUES (?, 'exec', 'session', 'mem:self', 'zcode', 'mcp_recall:full', ?, ?, '', ?, 'observed', 'hash', ?)`, id, outcome, action, at.UnixNano(), bytes); err != nil {
			t.Fatal(err)
		}
	}
	insert("stale", "recall", "hits", 64, now.Add(-40*24*time.Hour))
	insert("older", "recall", "empty", 0, now.Add(-26*time.Hour))
	insert("recent", "recall", "hits", 120, now.Add(-10*time.Minute))

	got := queryROIFacts(context.Background(), path, roiFactsRequest{Scope: "mem:self"})
	if got.Status != "available" || got.IdentityStatus != "verified" || got.Counts == nil {
		t.Fatalf("facts unavailable: %+v", got)
	}
	if got.Counts.Total != 3 || got.Counts.RecallOutcomes["hits"] != 2 || got.Counts.RecallOutcomes["empty"] != 1 || got.Counts.KnownTextBytes != 184 {
		t.Fatalf("recall counts: %+v", got.Counts)
	}
	windows := map[string]roiFactWindow{}
	for _, window := range got.Windows {
		windows[window.Key] = window
	}
	if windows["1h"].Total != 1 || windows["1h"].RecallOutcomes["hits"] != 1 || windows["1h"].KnownTextBytes != 120 {
		t.Fatalf("1h window: %+v", windows["1h"])
	}
	if windows["24h"].Total != 1 || windows["7d"].Total != 2 || windows["30d"].Total != 2 {
		t.Fatalf("rolling windows: %+v", windows)
	}
	if got.Vault == nil || got.Vault.SchemaEra != "thin_vault" || got.Vault.EpisodicDocs != 3 || got.Vault.EpisodicPointers != 2 || got.Vault.EpisodicText != 1 || got.Vault.NoeticUnits != 2 || got.Vault.NoeticWithCitations != 1 || got.Vault.InjectionLedgerRows != 1 {
		t.Fatalf("vault facts: %+v", got.Vault)
	}
	if len(got.RecentEvents) != 3 || got.RecentEvents[0].Action != "recall" || got.RecentEvents[0].TextBytes == nil || *got.RecentEvents[0].TextBytes != 120 {
		t.Fatalf("recent events: %+v", got.RecentEvents)
	}
	if got.RecentEvents[0].ObservedAt != now.Add(-10*time.Minute).Unix() {
		t.Fatalf("observed_at must be reported in seconds: %d", got.RecentEvents[0].ObservedAt)
	}
}

func TestROIEngineeringMetricsReuseAndLocusMatrix(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.db")
	memDB, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	nowUnix := time.Now().Unix()
	if _, err := memDB.Exec(`
		CREATE TABLE db_meta (key TEXT PRIMARY KEY, value TEXT);
		CREATE TABLE thin_vault_docs (
			doc_id TEXT PRIMARY KEY, layer TEXT, occurred_at INTEGER, raw_text TEXT,
			sentences_json TEXT, address_uri TEXT, waveform_bar TEXT, kept_indices_json TEXT, locus TEXT
		);
		CREATE TABLE thin_vault_noetic (
			noetic_id TEXT PRIMARY KEY, occurred_at INTEGER, status TEXT,
			query_text TEXT, answer_text TEXT, cited_unit_ids_json TEXT,
			cited_sentences_json TEXT, address_uri TEXT, waveform_bar TEXT,
			assistant_id TEXT, incarnation_id TEXT
		);
		INSERT INTO db_meta VALUES ('created_at', '1793350000'), ('engine', 'rolling-memory');
		INSERT INTO thin_vault_docs VALUES ('ep_hot_1', 'episodic', ?, '@ref:zcode:s1:m1', '[]', 'C6:L3:B001#01', '', '[]', 'Rolling-Memory');
		INSERT INTO thin_vault_docs VALUES ('ep_hot_2', 'episodic', ?, 'inline sentence one. inline sentence two.', '["inline sentence one.","inline sentence two."]', 'C6:L3:B002#01', '', '[0]', 'workspace-b');
		INSERT INTO thin_vault_noetic VALUES ('nt_1', ?, 'active', 'query alpha', 'ans alpha', '["ep_hot_1","ep_hot_2"]', '["distilled alpha"]', 'C6:L3:B001#05', '', 'zcode', 'inc1');
		INSERT INTO thin_vault_noetic VALUES ('nt_2', ?, 'active', 'query beta', 'ans beta', '["ep_hot_1"]', '["distilled beta"]', 'C6:L3:B001#06', '', 'zcode', 'inc1');
	`, nowUnix-60, nowUnix-120, nowUnix-30, nowUnix-10); err != nil {
		t.Fatal(err)
	}
	_ = memDB.Close()

	got := queryROIFacts(context.Background(), path, roiFactsRequest{Scope: "mem:self"})
	if got.Vault == nil {
		t.Fatalf("expected vault bundle even when sidecar ledger is absent, got nil")
	}
	if got.Vault.TotalCitationLinks != 3 || got.Vault.UniqueCitedUnits != 2 || got.Vault.ReusedUnitsCount != 1 || got.Vault.MaxReuseCount != 2 || got.Vault.AvgReuseMultiplier != 1.5 {
		t.Fatalf("unexpected reuse metrics: %+v", got.Vault)
	}
	if len(got.TopCitedUnits) != 2 || got.TopCitedUnits[0].UnitID != "ep_hot_1" || got.TopCitedUnits[0].CitationCount != 2 || len(got.TopCitedUnits[0].RecentQueries) != 2 {
		t.Fatalf("unexpected top cited units: %+v", got.TopCitedUnits)
	}
	if len(got.LocusMatrix) != 2 || got.LocusMatrix[0].Locus != "Rolling-Memory" || got.LocusMatrix[0].CitationHits != 2 || got.LocusMatrix[0].Status != "hot" {
		t.Fatalf("unexpected locus matrix: %+v", got.LocusMatrix)
	}
	if len(got.NoeticStream) != 2 || len(got.DailySeries) != 14 || len(got.VaultWindows) != 5 {
		t.Fatalf("unexpected streams/windows: noetic=%d daily=%d vaultWindows=%d", len(got.NoeticStream), len(got.DailySeries), len(got.VaultWindows))
	}
}

func TestROIAdoptionIntersectionAudit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.db")
	memDB, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	nowUnix := time.Now().Unix()
	if _, err := memDB.Exec(`
		CREATE TABLE db_meta (key TEXT PRIMARY KEY, value TEXT);
		CREATE TABLE thin_vault_docs (
			doc_id TEXT PRIMARY KEY, layer TEXT, occurred_at INTEGER, raw_text TEXT,
			sentences_json TEXT, address_uri TEXT, waveform_bar TEXT, kept_indices_json TEXT, locus TEXT
		);
		CREATE TABLE thin_vault_noetic (
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
		INSERT INTO thin_vault_docs VALUES
			('ep_src_1', 'episodic', ?, 'Fix located in admin/internal/rmconsole/roi_facts.go and ComputeDBFingerprint.', '[]', 'C6:L3:B001#01', '', '[]', 'Rolling-Memory'),
			('ep_src_2', 'episodic', ?, 'Base zero-shot score is 0.362 on ModernBERT-large.', '[]', 'C6:L3:B002#01', '', '[]', 'Rolling-Memory'),
				('ep_follow_tool', 'episodic', ?, '[tool] {"file_path":"/workspace/Rolling-Memory/admin/internal/rmconsole/roi_facts.go"}', '[]', 'C6:L3:B001#06', '', '[]', 'Rolling-Memory'),
			('ep_follow_reply', 'episodic', ?, '[assistant] Because laya uses ModernBERT-large with 0.362 base score, we should use intersection auditing.\n[Rolling-Memory 复盘] 调用: 1 次', '[]', 'C6:L3:B002#06', '', '[]', 'Rolling-Memory'),
			('ep_follow_unadopted', 'episodic', ?, '[assistant] Mentioning only echoed token_xyz in reply.\n[Rolling-Memory 复盘] internal/store/unused_file.go', '[]', 'C6:L3:B003#02', '', '[]', 'Rolling-Memory');
	`, nowUnix-200, nowUnix-190, nowUnix-95, nowUnix-55, nowUnix-25); err != nil {
		t.Fatal(err)
	}
	if _, err := memDB.Exec(`
		INSERT INTO thin_vault_noetic VALUES
			('nt_tool_1', ?, 'active', 'ComputeDBFingerprint bug', 'Check admin/internal/rmconsole/roi_facts.go and ComputeDBFingerprint', '["ep_src_1"]', '["admin/internal/rmconsole/roi_facts.go"]', 'C6:L3:B001#05', '', 'zcode', 'inc1'),
			('nt_ctx_2', ?, 'active', 'laya CPU performance', 'Base zero-shot score is 0.362 on ModernBERT-large', '["ep_src_2"]', '["ModernBERT-large 0.362"]', 'C6:L3:B002#05', '', 'zcode', 'inc1'),
			('nt_unadopted_3', ?, 'active', 'unrelated query token_xyz', 'Check internal/store/unused_file.go', '[]', '["internal/store/unused_file.go"]', 'C6:L3:B003#01', '', 'zcode', 'inc1'),
			('nt_pending_4', ?, 'active', 'latest query', 'Check cmd/rmem/main.go', '[]', '["cmd/rmem/main.go"]', 'C6:L3:B004#01', '', 'zcode', 'inc1');
	`, nowUnix-100, nowUnix-60, nowUnix-30, nowUnix-5); err != nil {
		t.Fatal(err)
	}
	_ = memDB.Close()

	got := queryROIFacts(context.Background(), path, roiFactsRequest{Scope: "mem:self"})
	if got.Vault == nil {
		t.Fatalf("expected non-nil vault")
	}
	if got.Vault.AuditedRecalls != 3 || got.Vault.AdoptedRecalls != 2 || got.Vault.AdoptedToolCount != 1 || got.Vault.AdoptedContextCount != 1 {
		t.Fatalf("unexpected adoption counts: audited=%d adopted=%d tool=%d ctx=%d rate=%.1f%% stream=%+v",
			got.Vault.AuditedRecalls, got.Vault.AdoptedRecalls, got.Vault.AdoptedToolCount, got.Vault.AdoptedContextCount, got.Vault.AdoptionRatePct, got.NoeticStream)
	}
	if got.Vault.AdoptionRatePct != 66.7 {
		t.Fatalf("expected adoption_rate_pct=66.7, got %.1f", got.Vault.AdoptionRatePct)
	}
	byID := map[string]roiNoeticEvent{}
	for _, ev := range got.NoeticStream {
		byID[ev.NoeticID] = ev
	}
	if byID["nt_tool_1"].AdoptionOutcome != "adopted_tool" || len(byID["nt_tool_1"].MatchedAnchors) == 0 || byID["nt_tool_1"].EchoExcludedCount < 1 {
		t.Fatalf("nt_tool_1 expected adopted_tool with echo exclusion, got %+v", byID["nt_tool_1"])
	}
	if byID["nt_ctx_2"].AdoptionOutcome != "adopted_context" || len(byID["nt_ctx_2"].MatchedAnchors) == 0 {
		t.Fatalf("nt_ctx_2 expected adopted_context, got %+v", byID["nt_ctx_2"])
	}
	if byID["nt_unadopted_3"].AdoptionOutcome != "unadopted" {
		t.Fatalf("nt_unadopted_3 expected unadopted (review footer must be stripped and query echo must be excluded), got %+v", byID["nt_unadopted_3"])
	}
	if byID["nt_pending_4"].AdoptionOutcome != "pending" {
		t.Fatalf("nt_pending_4 expected pending, got %+v", byID["nt_pending_4"])
	}
}

func TestROIAdoptionMultiHarnessJSONL(t *testing.T) {
	tmpHome := t.TempDir()
	path := filepath.Join(tmpHome, ".local", "share", "rolling-memory", "memory.db")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}

	// 1. Claude Code JSONL transcript (`tool_use` + `tool_result` + subsequent `Read` tool_use)
	claudeDir := filepath.Join(tmpHome, ".claude", "projects", "proj-a")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	claudeFile := filepath.Join(claudeDir, "sess_claude_1.jsonl")
	claudeJSONL := strings.Join([]string{
		`{"timestamp":"2026-11-12T01:00:10.000Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"call_cc_1","name":"mcp__rolling-memory__recall","input":{"cue":"claude config bug"}}]}}`,
		`{"timestamp":"2026-11-12T01:00:11.000Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_cc_1","content":"<rolling-memory-recall scope=\"mem:self\" results=\"1\" absent=\"false\">\nFix is in internal/harness/claude_wire.go and VerifyClaudeHook.\n</rolling-memory-recall>"}]}}`,
		`{"timestamp":"2026-11-12T01:00:15.000Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"call_cc_2","name":"Read","input":{"file_path":"/repo/internal/harness/claude_wire.go"}}]}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(claudeFile, []byte(claudeJSONL), 0o600); err != nil {
		t.Fatal(err)
	}

	// 2. Codex JSONL rollout transcript registered via ~/.codex/state_5.sqlite (`threads.rollout_path`)
	codexCustomDir := filepath.Join(tmpHome, "custom-codex-rollouts")
	if err := os.MkdirAll(codexCustomDir, 0o755); err != nil {
		t.Fatal(err)
	}
	codexFile := filepath.Join(codexCustomDir, "rollout_codex_1.jsonl")
	codexJSONL := strings.Join([]string{
		`{"timestamp":"2026-11-12T01:01:10.000Z","type":"response_item","payload":{"type":"function_call","name":"recall","namespace":"mcp__rolling_memory","call_id":"call_cx_1","arguments":"{\"cue\":\"codex rollout schema\"}"}}`,
		`{"timestamp":"2026-11-12T01:01:11.000Z","type":"response_item","payload":{"type":"function_call_output","call_id":"call_cx_1","output":[{"type":"input_text","text":"<rolling-memory-recall scope=\"mem:self\" results=\"1\" absent=\"false\">\nCheck internal/cli/episodic_scan_source.go and discoverEpisodicScanSources.\n</rolling-memory-recall>"}]}}`,
		`{"timestamp":"2026-11-12T01:01:14.000Z","type":"response_item","payload":{"type":"function_call","name":"exec_command","call_id":"call_cx_2","arguments":"{\"cmd\":\"sed -n '1,80p' internal/cli/episodic_scan_source.go\"}"}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(codexFile, []byte(codexJSONL), 0o600); err != nil {
		t.Fatal(err)
	}
	codexStateDir := filepath.Join(tmpHome, ".codex")
	if err := os.MkdirAll(codexStateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	codexStateDB, err := sql.Open("sqlite", "file:"+filepath.Join(codexStateDir, "state_5.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := codexStateDB.Exec(`
		CREATE TABLE threads (id TEXT PRIMARY KEY, rollout_path TEXT, updated_at INTEGER);
		INSERT INTO threads VALUES ('th_cx_1', ?, 1794445275);
	`, codexFile); err != nil {
		t.Fatal(err)
	}
	_ = codexStateDB.Close()

	// 3. WorkBuddy JSONL transcript (`DeferredToolCall` wrapping `mcp__rolling-memory__recall` + assistant reply citing novel symbol)
	wbDir := filepath.Join(tmpHome, ".workbuddy", "projects", "proj-wb")
	if err := os.MkdirAll(wbDir, 0o755); err != nil {
		t.Fatal(err)
	}
	wbFile := filepath.Join(wbDir, "sess_wb_1.jsonl")
	wbJSONL := strings.Join([]string{
		`{"timestamp":"2026-11-12T01:02:10.000Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"call_wb_1","name":"DeferredToolCall","input":{"name":"mcp__rolling-memory__recall","input":{"cue":"workbuddy mcp contract"}}}]}}`,
		`{"timestamp":"2026-11-12T01:02:11.000Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_wb_1","content":"<rolling-memory-recall scope=\"mem:self\" results=\"1\" absent=\"false\">\nUse activeNativePlugin and bin/rolling-memory-mcp.js.\n</rolling-memory-recall>"}]}}`,
		`{"timestamp":"2026-11-12T01:02:16.000Z","message":{"role":"assistant","content":[{"type":"text","text":"We should wire activeNativePlugin with bin/rolling-memory-mcp.js to avoid duplicate registration."}]}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(wbFile, []byte(wbJSONL), 0o600); err != nil {
		t.Fatal(err)
	}

	memDB, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := memDB.Exec(`
		CREATE TABLE db_meta (key TEXT PRIMARY KEY, value TEXT);
		CREATE TABLE thin_vault_docs (
			doc_id TEXT PRIMARY KEY, layer TEXT, occurred_at INTEGER, raw_text TEXT,
			sentences_json TEXT, address_uri TEXT, waveform_bar TEXT, kept_indices_json TEXT, locus TEXT
		);
		CREATE TABLE thin_vault_noetic (
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
		INSERT INTO thin_vault_docs VALUES
			('ep_cc_1', 'episodic', 1794445200, ?, '[]', 'C6:L1:LOCAL#1', '', '[]', 'Rolling-Memory'),
			('ep_cx_1', 'episodic', 1794445260, ?, '[]', 'C6:L1:LOCAL#2', '', '[]', 'Rolling-Memory'),
			('ep_wb_1', 'episodic', 1794445320, ?, '[]', 'C6:L1:LOCAL#3', '', '[]', 'Rolling-Memory');
	`, "@ref:jsonl:"+claudeFile+":0", "@ref:jsonl:"+codexFile+":0", "@ref:jsonl:"+wbFile+":0"); err != nil {
		t.Fatal(err)
	}
	if _, err := memDB.Exec(`
		INSERT INTO thin_vault_noetic VALUES
			('nt_claude', 1794445211, 'active', 'claude config bug', 'summary', '["ep_cc_1"]', '[]', 'C6:L1:LOCAL#10', '', 'default', 'inc1'),
			('nt_codex', 1794445271, 'active', 'codex rollout schema', 'summary', '["ep_cx_1"]', '[]', 'C6:L1:LOCAL#11', '', 'default', 'inc1'),
			('nt_workbuddy', 1794445331, 'active', 'workbuddy mcp contract', 'summary', '["ep_wb_1"]', '[]', 'C6:L1:LOCAL#12', '', 'default', 'inc1');
	`); err != nil {
		t.Fatal(err)
	}
	_ = memDB.Close()

	// Verify ledger sync imports all 3 non-ZCode harnesses even when ZCode DB does not exist.
	syncProductionRecallLedger(tmpHome, path)
	got := queryROIFacts(context.Background(), path, roiFactsRequest{Scope: "mem:self"})
	if got.Status != "available" || got.Counts == nil || got.Counts.RecallOutcomes["hits"] != 3 {
		t.Fatalf("expected 3 synced JSONL recall events in ledger, got status=%s counts=%+v", got.Status, got.Counts)
	}
	if got.Vault == nil || got.Vault.AuditedRecalls != 3 || got.Vault.AdoptedRecalls != 3 || got.Vault.AdoptedToolCount != 2 || got.Vault.AdoptedContextCount != 1 || got.Vault.AdoptionRatePct != 100.0 {
		t.Fatalf("unexpected multi-harness adoption summary: %+v", got.Vault)
	}
	byID := map[string]roiNoeticEvent{}
	for _, ev := range got.NoeticStream {
		byID[ev.NoeticID] = ev
	}
	if byID["nt_claude"].AdoptionOutcome != "adopted_tool" || byID["nt_claude"].AssistantID != "claude-code" || len(byID["nt_claude"].MatchedAnchors) == 0 {
		t.Fatalf("expected nt_claude adopted_tool with assistant_id=claude-code, got %+v", byID["nt_claude"])
	}
	if byID["nt_codex"].AdoptionOutcome != "adopted_tool" || byID["nt_codex"].AssistantID != "codex" || len(byID["nt_codex"].MatchedAnchors) == 0 {
		t.Fatalf("expected nt_codex adopted_tool with assistant_id=codex, got %+v", byID["nt_codex"])
	}
	if byID["nt_workbuddy"].AdoptionOutcome != "adopted_context" || byID["nt_workbuddy"].AssistantID != "workbuddy" || len(byID["nt_workbuddy"].MatchedAnchors) == 0 {
		t.Fatalf("expected nt_workbuddy adopted_context with assistant_id=workbuddy, got %+v", byID["nt_workbuddy"])
	}
	if len(got.TopCitedUnits) != 3 || got.TopCitedUnits[0].AdoptedCount != 1 || len(got.TopCitedUnits[0].Citations) == 0 || got.TopCitedUnits[0].Citations[0].AdoptionOutcome == "" {
		t.Fatalf("expected TopCitedUnits to carry AdoptedCount and citation adoption provenance, got %+v", got.TopCitedUnits)
	}
	rows := []ThreeLayerRow{{ID: "nt_claude", Layer: "noetic"}}
	enrichThreeLayerRowsWithAdoption(path, rows)
	if rows[0].AdoptionOutcome != "adopted_tool" || rows[0].AssistantID != "claude-code" || len(rows[0].MatchedAnchors) == 0 {
		t.Fatalf("expected enrichThreeLayerRowsWithAdoption to populate noetic row, got %+v", rows[0])
	}
}

func TestROIFactsDeduplicatesTwinRecallLedgerRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.db")
	memDB, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := memDB.Exec(`
			CREATE TABLE db_meta (key TEXT PRIMARY KEY, value TEXT);
			CREATE TABLE thin_vault_docs (doc_id TEXT PRIMARY KEY, occurred_at INTEGER, raw_text TEXT);
			CREATE TABLE thin_vault_noetic (noetic_id TEXT PRIMARY KEY, status TEXT, cited_unit_ids_json TEXT);
			INSERT INTO db_meta VALUES ('created_at', '1793350000'), ('engine', 'rolling-memory');
		`); err != nil {
		t.Fatal(err)
	}
	_ = memDB.Close()

	fp := computeAdminDBFingerprint(path, "mem:self")
	db := roiTestDB(t, path, 1)
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS efficacy_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL); INSERT INTO efficacy_meta VALUES ('db_fingerprint', ?);`, fp); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	twinTime := now.Add(-5 * time.Minute).UnixNano()
	soloBackfillTime := now.Add(-20 * time.Minute).UnixNano()

	// 1. Twin pair: direct daemon row (mcp_recall:full) + host backfill row (mcp_recall:tool) within 500ms
	if _, err := db.Exec(`
			INSERT INTO efficacy_event (event_id, execution_id, session, scope, harness, hook, output_outcome, action, target_event_id, observed_at, observation, text_sha256, text_bytes)
			VALUES
				('direct_1', 'exec_d1', 'sess_1', 'mem:self', 'zcode', 'mcp_recall:full', 'hits', 'recall', '', ?, 'observed', 'h1', 4096),
				('zcode_call_1', 'exec_t1', 'sess_1', 'mem:self', 'zcode', 'mcp_recall:tool', 'hits', 'recall', '', ?, 'observed', 'h1', 4096),
				('zcode_call_legacy', 'exec_t2', 'sess_1', 'mem:self', 'zcode', 'mcp_recall:tool', 'hits', 'recall', '', ?, 'observed', 'h2', 2048);
		`, twinTime, twinTime+500_000_000, soloBackfillTime); err != nil {
		t.Fatal(err)
	}

	got := queryROIFacts(context.Background(), path, roiFactsRequest{Scope: "mem:self"})
	if got.Status != "available" || got.Counts == nil {
		t.Fatalf("facts unavailable: %+v", got)
	}
	// Must count the twin pair once (4096 bytes) + the solo backfill row once (2048 bytes) = 2 calls, 6144 bytes total.
	if got.Counts.Total != 2 || got.Counts.RecallOutcomes["hits"] != 2 || got.Counts.KnownTextBytes != 6144 {
		t.Fatalf("expected twin deduplication (2 calls, 6144 bytes), got %+v", got.Counts)
	}
}

// TestROIFactsWindowsUseIngestTime 钉住「今日/窗口新增」的首次入库时间口径：内容时间
// （occurred_at）旧、首次入库（inserted_at）新的行计入近期窗口；内容时间新、入库旧的行
// 不计入——与「记忆吞吐趋势」图的 inserted_at 口径一致，杜绝编辑旧文件虚增近期新增。
func TestROIFactsWindowsUseIngestTime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.db")
	memDB, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	nowUnix := now.Unix()
	if _, err := memDB.Exec(`
		CREATE TABLE db_meta (key TEXT PRIMARY KEY, value TEXT);
		CREATE TABLE thin_vault_docs (
			doc_id TEXT PRIMARY KEY, layer TEXT, occurred_at INTEGER, inserted_at INTEGER, raw_text TEXT,
			sentences_json TEXT, address_uri TEXT, waveform_bar TEXT, kept_indices_json TEXT, locus TEXT
		);
		CREATE TABLE thin_vault_noetic (
			noetic_id TEXT PRIMARY KEY, occurred_at INTEGER, inserted_at INTEGER, status TEXT,
			query_text TEXT, answer_text TEXT, cited_unit_ids_json TEXT,
			cited_sentences_json TEXT, address_uri TEXT, waveform_bar TEXT,
			assistant_id TEXT, incarnation_id TEXT
		);
		INSERT INTO db_meta VALUES ('created_at', '1793350000'), ('engine', 'rolling-memory');
		INSERT INTO thin_vault_docs VALUES
			('ep_old_content_fresh_ingest', 'episodic', ?, ?, 'older corpus text', '[]', 'C6:L1:LOCAL#01', '', '[]', 'Rolling-Memory'),
			('ep_fresh_content_old_ingest', 'episodic', ?, ?, 'fresh corpus text', '[]', 'C6:L1:LOCAL#02', '', '[]', 'Rolling-Memory'),
			('ep_old_content_fresh_ingest_2', 'episodic', ?, ?, 'older corpus text two', '[]', 'C6:L1:LOCAL#03', '', '[]', 'Rolling-Memory');
		INSERT INTO thin_vault_noetic VALUES
			('nt_old_content_fresh_ingest', ?, ?, 'active', 'older query', 'answer', '["ep_old_content_fresh_ingest"]', '[]', 'C6:L1:LOCAL#04', '', 'zcode', 'inc1'),
			('nt_fresh_content_old_ingest', ?, ?, 'active', 'fresh query', 'answer', '["ep_fresh_content_old_ingest"]', '[]', 'C6:L1:LOCAL#05', '', 'zcode', 'inc1'),
			('nt_old_content_fresh_ingest_2', ?, ?, 'active', 'older query two', 'answer', '["ep_old_content_fresh_ingest"]', '[]', 'C6:L1:LOCAL#06', '', 'zcode', 'inc1');
	`,
		nowUnix-3*86400, nowUnix-120, // 内容 3 天前，入库 2 分钟前
		nowUnix-120, nowUnix-3*86400, // 内容 2 分钟前，入库 3 天前
		nowUnix-3*86400, nowUnix-240, // 内容 3 天前，入库 4 分钟前
		nowUnix-3*86400, nowUnix-120,
		nowUnix-120, nowUnix-3*86400,
		nowUnix-3*86400, nowUnix-240,
	); err != nil {
		t.Fatal(err)
	}
	_ = memDB.Close()

	got := queryROIFacts(context.Background(), path, roiFactsRequest{Scope: "mem:self"})
	if got.Vault == nil {
		t.Fatalf("expected vault bundle")
	}
	windows := map[string]roiFactWindow{}
	for _, window := range got.VaultWindows {
		windows[window.Key] = window
	}
	if windows["1h"].EpisodicAdded != 2 || windows["1h"].NoeticCrystallized != 2 {
		t.Fatalf("1h window must follow first-ingest time: ep=%d noetic=%d, want 2/2（按 occurred_at 只会是 1/1）",
			windows["1h"].EpisodicAdded, windows["1h"].NoeticCrystallized)
	}
	if windows["24h"].EpisodicAdded != 2 || windows["24h"].NoeticCrystallized != 2 {
		t.Fatalf("24h window must follow first-ingest time: %+v", windows["24h"])
	}
	if windows["all"].EpisodicAdded != 3 || windows["all"].NoeticCrystallized != 3 {
		t.Fatalf("all window must keep every row: %+v", windows["all"])
	}

	// 今日计数按同一首次入库口径，逐行镜像生产谓词（避免测试恰逢午夜跨日时误判）。
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).Unix()
	var wantToday int64
	for _, ingest := range []int64{nowUnix - 120, nowUnix - 240} {
		if ingest >= todayStart {
			wantToday++
		}
	}
	if got.Vault.TodayEpisodic != wantToday || got.Vault.TodayNoetic != wantToday {
		t.Fatalf("today counters must follow first-ingest time: ep=%d noetic=%d, want %d",
			got.Vault.TodayEpisodic, got.Vault.TodayNoetic, wantToday)
	}
	if got.Vault.TodayCitations != wantToday {
		t.Fatalf("today citations must follow first-ingest time: got %d, want %d", got.Vault.TodayCitations, wantToday)
	}
}

// TestROIFactsLayerAttribution 钉住 ROI 卡的层归属与控制台统一口径（三层浏览 / facets /
// 趋势图）一致：docs 表中 layer=='background' 的行计入 L1（并入背景计数），L2 只计情景文档，
// 避免演示用背景文档被记到 L2 而与三层浏览页同名异值。
func TestROIFactsLayerAttribution(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.db")
	memDB, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	nowUnix := now.Unix()
	if _, err := memDB.Exec(`
		CREATE TABLE db_meta (key TEXT PRIMARY KEY, value TEXT);
		CREATE TABLE thin_vault_docs (
			doc_id TEXT PRIMARY KEY, layer TEXT, occurred_at INTEGER, inserted_at INTEGER, raw_text TEXT,
			sentences_json TEXT, address_uri TEXT, waveform_bar TEXT, kept_indices_json TEXT, locus TEXT
		);
		CREATE TABLE thin_vault_noetic (
			noetic_id TEXT PRIMARY KEY, occurred_at INTEGER, inserted_at INTEGER, status TEXT,
			query_text TEXT, answer_text TEXT, cited_unit_ids_json TEXT,
			cited_sentences_json TEXT, address_uri TEXT, waveform_bar TEXT,
			assistant_id TEXT, incarnation_id TEXT
		);
		CREATE TABLE thin_vault_bg_chunks (
			unit_id TEXT PRIMARY KEY, abs_path TEXT, rel_path TEXT, chunk_index INTEGER DEFAULT 0,
			anchor TEXT DEFAULT '', occurred_at INTEGER DEFAULT 0, inserted_at INTEGER DEFAULT 0,
			address_uri TEXT DEFAULT '', coord64 INTEGER DEFAULT 0, kept_indices_json TEXT DEFAULT '[]', slots_json TEXT DEFAULT '[]'
		);
		INSERT INTO db_meta VALUES ('created_at', '1793350000'), ('engine', 'rolling-memory');
		INSERT INTO thin_vault_docs VALUES ('ep_1', 'episodic', ?, ?, '@ref:zcode:sess:msg', '[]', 'C6:L1:LOCAL#01', '', '[]', 'Rolling-Memory');
		INSERT INTO thin_vault_docs VALUES ('ep_2', 'episodic', ?, ?, 'inline text', '[]', 'C6:L1:LOCAL#02', '', '[]', 'Rolling-Memory');
		INSERT INTO thin_vault_docs VALUES ('U10-JSON-DOC-1', 'background', ?, ?, 'demo background doc', '[]', 'C7:L1:LOCAL#01', '', '[]', 'Rolling-Memory');
		INSERT INTO thin_vault_bg_chunks(unit_id, abs_path, rel_path, occurred_at, inserted_at) VALUES
			('bg:aa#1', '/repo/a.md', 'a.md', ?, ?), ('bg:aa#2', '/repo/a.md', 'a.md', ?, ?);
		INSERT INTO thin_vault_noetic VALUES ('nt_1', ?, ?, 'active', 'q', 'a', '["ep_1"]', '[]', 'C6:L1:LOCAL#04', '', 'zcode', 'inc1');
	`,
		nowUnix-3*86400, nowUnix-120,
		nowUnix-3*86400, nowUnix-180,
		nowUnix-3*86400, nowUnix-240,
		nowUnix-3*86400, nowUnix-120,
		nowUnix-3*86400, nowUnix-120,
		nowUnix-3*86400, nowUnix-150,
	); err != nil {
		t.Fatal(err)
	}
	_ = memDB.Close()

	bundle, _ := computeROIFactsVaultBundle(path)
	v := bundle.vault
	if v == nil {
		t.Fatal("expected vault bundle")
	}
	if v.EpisodicDocs != 2 || v.EpisodicPointers != 1 || v.EpisodicText != 1 {
		t.Fatalf("L2 只计情景文档：docs=%d pointers=%d text=%d, want 2/1/1", v.EpisodicDocs, v.EpisodicPointers, v.EpisodicText)
	}
	if v.EpisodicCells != 1 {
		t.Fatalf("L2 记忆地址只计情景文档（背景文档地址不计入）：cells=%d, want 1", v.EpisodicCells)
	}
	if v.BackgroundChunks != 3 || v.TodayBackground != 3 {
		t.Fatalf("L1 = 背景分块 2 + 背景文档 1：chunks=%d today=%d, want 3/3", v.BackgroundChunks, v.TodayBackground)
	}
	if v.TodayEpisodic != 2 {
		t.Fatalf("今日 L2 = 2（背景文档不计入）：%d", v.TodayEpisodic)
	}
	if v.TotalUnits != 2+3+1 {
		t.Fatalf("总单元保持 情景+背景+结晶 = 6（层归属不改变总量）：%d", v.TotalUnits)
	}
	wins := map[string]roiFactWindow{}
	for _, w := range bundle.vaultWindows {
		wins[w.Key] = w
	}
	if wins["1h"].EpisodicAdded != 2 || wins["1h"].BackgroundAdded != 3 {
		t.Fatalf("1h 窗口层归属：ep=%d bg=%d, want 2/3", wins["1h"].EpisodicAdded, wins["1h"].BackgroundAdded)
	}
	if wins["all"].EpisodicAdded != 2 || wins["all"].BackgroundAdded != 3 {
		t.Fatalf("all 窗口层归属：ep=%d bg=%d, want 2/3", wins["all"].EpisodicAdded, wins["all"].BackgroundAdded)
	}
}
