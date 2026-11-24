// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package rollingmemory

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// adoptionTestHome returns an isolated HOME plus the canonical product vault
// path below it, so transcript discovery is scoped to the fixture.
func adoptionTestHome(t *testing.T) (string, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".local", "share", "rolling-memory", "memory.db")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir vault dir: %v", err)
	}
	return home, path
}

// openAdoptionFixtureDB creates the minimal vault surface used by the adoption
// audit. The fixture does not model any host's private session store.
func openAdoptionFixtureDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open fixture vault: %v", err)
	}
	if _, err := db.Exec(`
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
		_ = db.Close()
		t.Fatalf("create fixture vault: %v", err)
	}
	return db
}

func insertAdoptionNoetic(t *testing.T, db *sql.DB, id string, occurredAt int64, cue, answer, citedIDs, citedSentences, assistantID string) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO rm_noetic VALUES (?, ?, 'active', ?, ?, ?, ?, ?, '', ?, 'inc1')
	`, id, occurredAt, cue, answer, citedIDs, citedSentences, "C6:L1:LOCAL#"+id, assistantID); err != nil {
		t.Fatalf("insert noetic %s: %v", id, err)
	}
}

func insertAdoptionUnit(t *testing.T, db *sql.DB, id string, occurredAt int64, text string) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO rm_units VALUES (?, 'episodic', ?, ?, '[]', '', '', '[]', 'Rolling-Memory')
	`, id, occurredAt, text); err != nil {
		t.Fatalf("insert unit %s: %v", id, err)
	}
}

func adoptionOutcomeByID(result roiFactsResponse, id string) roiNoeticEvent {
	for _, ev := range result.NoeticStream {
		if ev.NoeticID == id {
			return ev
		}
	}
	return roiNoeticEvent{}
}

func adoptionJSONLTS(unixSec int64) string {
	return time.Unix(unixSec, 0).UTC().Format(time.RFC3339Nano)
}

func adoptionRecallLines(callID, cue, output string, atUnix int64) []string {
	return []string{
		fmt.Sprintf(`{"timestamp":%q,"message":{"role":"assistant","content":[{"type":"tool_use","id":%q,"name":"mcp__rolling-memory__recall","input":{"cue":%q}}]}}`,
			adoptionJSONLTS(atUnix), callID, cue),
		fmt.Sprintf(`{"timestamp":%q,"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":%q,"content":%q}]}}`,
			adoptionJSONLTS(atUnix+1), callID, output),
	}
}

func adoptionToolUseLine(callID, name, inputJSON string, atUnix int64) string {
	return fmt.Sprintf(`{"timestamp":%q,"message":{"role":"assistant","content":[{"type":"tool_use","id":%q,"name":%q,"input":%s}]}}`,
		adoptionJSONLTS(atUnix), callID, name, inputJSON)
}

func adoptionToolResultLine(callID, output string, atUnix int64) string {
	return fmt.Sprintf(`{"timestamp":%q,"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":%q,"content":%q}]}}`,
		adoptionJSONLTS(atUnix), callID, output)
}

func adoptionErrorToolResultLine(callID, output string, atUnix int64) string {
	return fmt.Sprintf(`{"timestamp":%q,"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":%q,"content":%q,"is_error":true}]}}`,
		adoptionJSONLTS(atUnix), callID, output)
}

func adoptionAssistantBlocksLine(blocksJSON string, atUnix int64) string {
	return fmt.Sprintf(`{"timestamp":%q,"message":{"role":"assistant","content":%s}}`,
		adoptionJSONLTS(atUnix), blocksJSON)
}

func adoptionAssistantTextLine(text string, atUnix int64) string {
	return fmt.Sprintf(`{"timestamp":%q,"message":{"role":"assistant","content":[{"type":"text","text":%q}]}}`,
		adoptionJSONLTS(atUnix), text)
}

func writeAdoptionJSONL(t *testing.T, home string, lines []string) string {
	t.Helper()
	return writeAdoptionJSONLAt(t, home, "adoption-fixture", "session", lines)
}

func writeAdoptionJSONLAt(t *testing.T, home, project, session string, lines []string) string {
	t.Helper()
	return writeAdoptionJSONLInDir(t, filepath.Join(home, ".claude", "projects", project), session, lines)
}

func writeAdoptionJSONLInDir(t *testing.T, dir, session string, lines []string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir transcript dir: %v", err)
	}
	path := filepath.Join(dir, session+".jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	return path
}

// TestAdoptionJSONLRequiresExactCompleteCueAndRealOutput pins the pairing gate:
// a transcript whose cue is only a superstring of the Noetic cue must not be
// paired, and the unpaired recall must not fall back to vault fragments.
func TestAdoptionJSONLRequiresExactCompleteCueAndRealOutput(t *testing.T) {
	home, path := adoptionTestHome(t)
	now := time.Now().Unix()
	db := openAdoptionFixtureDB(t, path)
	insertAdoptionNoetic(t, db, "nt_exact", now-1200, "alpha complete cue", "crystallized summary", "[]", "[]", "default")
	insertAdoptionUnit(t, db, "ep_tool_fragment", now-1195, `[tool] {"file_path":"/repo/internal/foo.go"}`)
	insertAdoptionUnit(t, db, "ep_reply_fragment", now-1194, "[assistant] The answer is in /repo/internal/foo.go.")
	_ = db.Close()

	lines := adoptionRecallLines("call_superset", "alpha complete cue extra", "deliverable in /repo/internal/foo.go", now-1199)
	lines = append(lines, adoptionToolUseLine("call_read", "Read", `{"file_path":"/repo/internal/foo.go"}`, now-1196))
	writeAdoptionJSONL(t, home, lines)

	got := queryROIFacts(context.Background(), path, roiFactsRequest{Scope: "mem:self"})
	ev := adoptionOutcomeByID(got, "nt_exact")
	if ev.AdoptionOutcome != "pending" || got.Vault.AuditedRecalls != 0 || got.Vault.AdoptedRecalls != 0 || got.Vault.AdoptionRatePct != 0 {
		t.Fatalf("unpaired recall must stay pending without vault-fragment evidence, got outcome=%q summary=%+v", ev.AdoptionOutcome, got.Vault)
	}
}

// TestAdoptionJSONLAmbiguousFullCueStaysPending pins the no-cross-talk rule:
// two sessions recording the same complete cue in the same time window are
// ambiguous, so neither window may be guessed into the event.
func TestAdoptionJSONLAmbiguousFullCueStaysPending(t *testing.T) {
	home, path := adoptionTestHome(t)
	now := time.Now().Unix()
	db := openAdoptionFixtureDB(t, path)
	insertAdoptionNoetic(t, db, "nt_ambiguous", now-1200, "same complete cue", "crystallized summary", "[]", "[]", "default")
	_ = db.Close()

	linesA := adoptionRecallLines("call_a", "same complete cue", "session A deliverable /repo/a/only.go", now-1199)
	linesA = append(linesA, adoptionToolUseLine("call_a_read", "Read", `{"file_path":"/repo/a/only.go"}`, now-1196))
	linesB := adoptionRecallLines("call_b", "same complete cue", "session B deliverable /repo/b/only.go", now-1199)
	linesB = append(linesB, adoptionToolUseLine("call_b_read", "Read", `{"file_path":"/repo/b/only.go"}`, now-1196))
	writeAdoptionJSONLAt(t, home, "proj-a", "session-a", linesA)
	writeAdoptionJSONLAt(t, home, "proj-b", "session-b", linesB)

	got := queryROIFacts(context.Background(), path, roiFactsRequest{Scope: "mem:self"})
	ev := adoptionOutcomeByID(got, "nt_ambiguous")
	if ev.AdoptionOutcome != "pending" || got.Vault.AuditedRecalls != 0 {
		t.Fatalf("ambiguous full-cue windows must not be cross-matched, got outcome=%q summary=%+v", ev.AdoptionOutcome, got.Vault)
	}
}

// TestAdoptionJSONLExcludesReasoningAndToolReturns pins the evidence boundary:
// only subsequent real tool parameters and assistant body count; reasoning
// traces and tool returns never do.
func TestAdoptionJSONLExcludesReasoningAndToolReturns(t *testing.T) {
	home, path := adoptionTestHome(t)
	now := time.Now().Unix()
	db := openAdoptionFixtureDB(t, path)
	insertAdoptionNoetic(t, db, "nt_exclusions", now-1200, "strict exclusion cue", "crystallized summary", "[]", "[]", "default")
	_ = db.Close()

	anchor := "/repo/vaulted/keep.go"
	lines := adoptionRecallLines("call_exclusions", "strict exclusion cue", "real deliverable "+anchor, now-1199)
	lines = append(lines, adoptionToolUseLine("call_unrelated", "Read", `{"file_path":"/repo/unrelated.txt"}`, now-1196))
	lines = append(lines, adoptionToolResultLine("call_unrelated", "tool return body mentions "+anchor, now-1195))
	blocks := fmt.Sprintf(`[{"type":"reasoning","text":"reasoning mentions %s"},{"type":"thinking","text":"thinking mentions %s"},{"type":"text","text":"I will inspect the unrelated file."}]`, anchor, anchor)
	lines = append(lines, adoptionAssistantBlocksLine(blocks, now-1194))
	writeAdoptionJSONL(t, home, lines)

	got := queryROIFacts(context.Background(), path, roiFactsRequest{Scope: "mem:self"})
	ev := adoptionOutcomeByID(got, "nt_exclusions")
	if ev.AdoptionOutcome != "unadopted" || len(ev.MatchedAnchors) != 0 {
		t.Fatalf("reasoning/tool returns must not count as adoption, got %+v", ev)
	}
}

// TestAdoptionJSONLRealDeliveryBeatsCrystallizedAnchor pins delivery priority:
// anchors present only in crystallized text are not evidence when the paired
// transcript recorded a different real recall output.
func TestAdoptionJSONLRealDeliveryBeatsCrystallizedAnchor(t *testing.T) {
	home, path := adoptionTestHome(t)
	now := time.Now().Unix()
	db := openAdoptionFixtureDB(t, path)
	insertAdoptionNoetic(t, db, "nt_real_delivery", now-1200, "real delivery priority cue",
		"Crystallized answer mentions /repo/phantom.go",
		`["ep_crystal"]`, `["/repo/phantom.go"]`, "default")
	_ = db.Close()

	lines := adoptionRecallLines("call_real", "real delivery priority cue", "Real answer mentions /repo/actual.go", now-1199)
	lines = append(lines, adoptionToolUseLine("call_phantom_read", "Read", `{"file_path":"/repo/phantom.go"}`, now-1196))
	writeAdoptionJSONL(t, home, lines)

	got := queryROIFacts(context.Background(), path, roiFactsRequest{Scope: "mem:self"})
	ev := adoptionOutcomeByID(got, "nt_real_delivery")
	if ev.AdoptionOutcome != "unadopted" || len(ev.MatchedAnchors) != 0 {
		t.Fatalf("crystallized-only anchors must not be charged against the real delivery, got %+v", ev)
	}
}

// TestAdoptionJSONLNextRecallClosesWindow pins the same-session window boundary:
// evidence after the next recall belongs to the next recall, not the previous one.
func TestAdoptionJSONLNextRecallClosesWindow(t *testing.T) {
	home, path := adoptionTestHome(t)
	now := time.Now().Unix()
	db := openAdoptionFixtureDB(t, path)
	insertAdoptionNoetic(t, db, "nt_first", now-1200, "first recall cue", "first summary", "[]", "[]", "default")
	insertAdoptionNoetic(t, db, "nt_second", now-1180, "second recall cue", "second summary", "[]", "[]", "default")
	_ = db.Close()

	lines := adoptionRecallLines("call_first", "first recall cue", "first deliverable /repo/first.go", now-1199)
	lines = append(lines, adoptionToolUseLine("call_unrelated_before", "Read", `{"file_path":"/repo/unrelated.txt"}`, now-1195))
	lines = append(lines, adoptionRecallLines("call_second", "second recall cue", "second deliverable /repo/second.go", now-1190)...)
	lines = append(lines, adoptionToolUseLine("call_first_read", "Read", `{"file_path":"/repo/first.go"}`, now-1189))
	lines = append(lines, adoptionAssistantTextLine("The second answer is /repo/second.go.", now-1188))
	writeAdoptionJSONL(t, home, lines)

	got := queryROIFacts(context.Background(), path, roiFactsRequest{Scope: "mem:self"})
	first := adoptionOutcomeByID(got, "nt_first")
	second := adoptionOutcomeByID(got, "nt_second")
	if first.AdoptionOutcome != "unadopted" {
		t.Fatalf("first recall must not consume evidence after the next recall, got %+v", first)
	}
	if second.AdoptionOutcome != "adopted_context" || len(second.MatchedAnchors) == 0 {
		t.Fatalf("second recall should use its own follow-up assistant body, got %+v", second)
	}
}

// TestAdoptionJSONLIncompleteWindowDoesNotSettleUnused pins the observation
// floor: while a recall's ten-minute follow-up window is still open, a missing
// hit is pending, not unadopted.
func TestAdoptionJSONLIncompleteWindowDoesNotSettleUnused(t *testing.T) {
	home, path := adoptionTestHome(t)
	now := time.Now().Unix()
	db := openAdoptionFixtureDB(t, path)
	insertAdoptionNoetic(t, db, "nt_open_window", now-30, "open window cue", "summary", "[]", "[]", "default")
	_ = db.Close()

	lines := adoptionRecallLines("call_open", "open window cue", "deliverable /repo/open/keep.go", now-29)
	lines = append(lines, adoptionToolUseLine("call_unrelated", "Read", `{"file_path":"/repo/unrelated.txt"}`, now-26))
	writeAdoptionJSONL(t, home, lines)

	got := queryROIFacts(context.Background(), path, roiFactsRequest{Scope: "mem:self"})
	ev := adoptionOutcomeByID(got, "nt_open_window")
	if ev.AdoptionOutcome != "pending" || got.Vault.AuditedRecalls != 0 {
		t.Fatalf("an open follow-up window must not settle as unused, got outcome=%q summary=%+v", ev.AdoptionOutcome, got.Vault)
	}
}

// TestAdoptionJSONLZCodeTranscriptPathStillSupported pins the original host's
// authorized JSONL adapter path: when the host writes a supported transcript,
// its session window is used and the host id comes from the path. Host-private
// SQLite is never consulted.
func TestAdoptionJSONLZCodeTranscriptPathStillSupported(t *testing.T) {
	home, path := adoptionTestHome(t)
	now := time.Now().Unix()
	db := openAdoptionFixtureDB(t, path)
	insertAdoptionNoetic(t, db, "nt_zcode", now-1200, "zcode transcript cue", "summary", "[]", "[]", "default")
	_ = db.Close()

	lines := adoptionRecallLines("call_zcode", "zcode transcript cue", "deliverable /repo/zcode/only.go", now-1199)
	lines = append(lines, adoptionToolUseLine("call_zcode_read", "Read", `{"file_path":"/repo/zcode/only.go"}`, now-1196))
	writeAdoptionJSONLInDir(t, filepath.Join(home, ".zcode", "projects", "proj-z", "sessions"), "sess-z", lines)

	got := queryROIFacts(context.Background(), path, roiFactsRequest{Scope: "mem:self"})
	ev := adoptionOutcomeByID(got, "nt_zcode")
	if ev.AdoptionOutcome != "adopted_tool" || len(ev.MatchedAnchors) == 0 || ev.AssistantID != "zcode" {
		t.Fatalf("zcode JSONL transcript must be supported through the generic adapter, got %+v", ev)
	}
}

// TestAdoptionJSONLPreReceiptSiblingEvidenceDoesNotCount pins observation order:
// a sibling tool parameter or assistant text emitted before the recall's tool
// receipt is not post-recall evidence.
func TestAdoptionJSONLPreReceiptSiblingEvidenceDoesNotCount(t *testing.T) {
	home, path := adoptionTestHome(t)
	now := time.Now().Unix()
	db := openAdoptionFixtureDB(t, path)
	insertAdoptionNoetic(t, db, "nt_order", now-1200, "receipt order cue", "summary", "[]", "[]", "default")
	_ = db.Close()

	blocks := `[{"type":"tool_use","id":"call_order","name":"mcp__rolling-memory__recall","input":{"cue":"receipt order cue"}},` +
		`{"type":"tool_use","id":"call_pre","name":"Read","input":{"file_path":"/repo/pre/only.go"}},` +
		`{"type":"text","text":"pre-receipt body mentions /repo/pre/only.go"}]`
	lines := []string{adoptionAssistantBlocksLine(blocks, now-1199)}
	lines = append(lines, adoptionToolResultLine("call_order", "real deliverable /repo/pre/only.go", now-1198))
	lines = append(lines, adoptionToolUseLine("call_post", "Read", `{"file_path":"/repo/post/unrelated.txt"}`, now-1197))
	writeAdoptionJSONL(t, home, lines)

	got := queryROIFacts(context.Background(), path, roiFactsRequest{Scope: "mem:self"})
	ev := adoptionOutcomeByID(got, "nt_order")
	if ev.AdoptionOutcome != "unadopted" || len(ev.MatchedAnchors) != 0 {
		t.Fatalf("pre-receipt sibling evidence must not be adopted, got %+v", ev)
	}
}

// TestAdoptionJSONLErrorReceiptStaysPending pins that an errored recall result
// is not a confirmed delivery and cannot be used to claim adoption.
func TestAdoptionJSONLErrorReceiptStaysPending(t *testing.T) {
	home, path := adoptionTestHome(t)
	now := time.Now().Unix()
	db := openAdoptionFixtureDB(t, path)
	insertAdoptionNoetic(t, db, "nt_error", now-1200, "error receipt cue", "summary", "[]", "[]", "default")
	_ = db.Close()

	lines := []string{
		fmt.Sprintf(`{"timestamp":%q,"message":{"role":"assistant","content":[{"type":"tool_use","id":"call_error","name":"mcp__rolling-memory__recall","input":{"cue":"error receipt cue"}}]}}`,
			adoptionJSONLTS(now-1199)),
	}
	lines = append(lines, adoptionErrorToolResultLine("call_error", "error mentions /repo/error/only.go", now-1198))
	lines = append(lines, adoptionToolUseLine("call_error_read", "Read", `{"file_path":"/repo/error/only.go"}`, now-1197))
	writeAdoptionJSONL(t, home, lines)

	got := queryROIFacts(context.Background(), path, roiFactsRequest{Scope: "mem:self"})
	ev := adoptionOutcomeByID(got, "nt_error")
	if ev.AdoptionOutcome != "pending" || got.Vault.AuditedRecalls != 0 {
		t.Fatalf("an error receipt must stay pending, got outcome=%q summary=%+v", ev.AdoptionOutcome, got.Vault)
	}
}

// TestAdoptionJSONLCueCaseIsNotFolded pins that original cue casing is part of
// the identity; a differently-cased cue cannot claim the call.
func TestAdoptionJSONLCueCaseIsNotFolded(t *testing.T) {
	home, path := adoptionTestHome(t)
	now := time.Now().Unix()
	db := openAdoptionFixtureDB(t, path)
	insertAdoptionNoetic(t, db, "nt_case", now-1200, "Case Cue Alpha", "summary", "[]", "[]", "default")
	_ = db.Close()

	lines := adoptionRecallLines("call_case", "case cue alpha", "deliverable /repo/case/only.go", now-1199)
	lines = append(lines, adoptionToolUseLine("call_case_read", "Read", `{"file_path":"/repo/case/only.go"}`, now-1196))
	writeAdoptionJSONL(t, home, lines)

	got := queryROIFacts(context.Background(), path, roiFactsRequest{Scope: "mem:self"})
	ev := adoptionOutcomeByID(got, "nt_case")
	if ev.AdoptionOutcome != "pending" || got.Vault.AuditedRecalls != 0 {
		t.Fatalf("differently-cased cue must not be folded into a match, got outcome=%q summary=%+v", ev.AdoptionOutcome, got.Vault)
	}
}

// TestAdoptionJSONLMissingTimestampsStayPending pins that transcript lines
// without real timestamps cannot claim a call, even when their file mtime
// happens to sit near the event time.
func TestAdoptionJSONLMissingTimestampsStayPending(t *testing.T) {
	home, path := adoptionTestHome(t)
	now := time.Now().Unix()
	db := openAdoptionFixtureDB(t, path)
	insertAdoptionNoetic(t, db, "nt_no_ts", now, "no timestamp cue", "summary", "[]", "[]", "default")
	_ = db.Close()

	lines := []string{
		`{"message":{"role":"assistant","content":[{"type":"tool_use","id":"call_no_ts","name":"mcp__rolling-memory__recall","input":{"cue":"no timestamp cue"}}]}}`,
		`{"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_no_ts","content":"deliverable /repo/no-ts/only.go"}]}}`,
		`{"message":{"role":"assistant","content":[{"type":"tool_use","id":"call_no_ts_read","name":"Read","input":{"file_path":"/repo/no-ts/only.go"}}]}}`,
	}
	writeAdoptionJSONL(t, home, lines)

	got := queryROIFacts(context.Background(), path, roiFactsRequest{Scope: "mem:self"})
	ev := adoptionOutcomeByID(got, "nt_no_ts")
	if ev.AdoptionOutcome != "pending" || got.Vault.AuditedRecalls != 0 {
		t.Fatalf("missing timestamps must not claim a call, got outcome=%q summary=%+v", ev.AdoptionOutcome, got.Vault)
	}
}

// TestAdoptionJSONLSharedUniqueCandidateStaysPending pins bidirectional
// uniqueness: when two events would claim the same single window, neither wins.
func TestAdoptionJSONLSharedUniqueCandidateStaysPending(t *testing.T) {
	home, path := adoptionTestHome(t)
	now := time.Now().Unix()
	db := openAdoptionFixtureDB(t, path)
	insertAdoptionNoetic(t, db, "nt_shared_a", now-1200, "shared candidate cue", "summary", "[]", "[]", "default")
	insertAdoptionNoetic(t, db, "nt_shared_b", now-1200, "shared candidate cue", "summary", "[]", "[]", "default")
	_ = db.Close()

	lines := adoptionRecallLines("call_shared", "shared candidate cue", "deliverable /repo/shared/only.go", now-1199)
	lines = append(lines, adoptionToolUseLine("call_shared_read", "Read", `{"file_path":"/repo/shared/only.go"}`, now-1196))
	writeAdoptionJSONL(t, home, lines)

	got := queryROIFacts(context.Background(), path, roiFactsRequest{Scope: "mem:self"})
	for _, id := range []string{"nt_shared_a", "nt_shared_b"} {
		ev := adoptionOutcomeByID(got, id)
		if ev.AdoptionOutcome != "pending" {
			t.Fatalf("%s must stay pending on a shared candidate, got %+v", id, ev)
		}
	}
	if got.Vault.AuditedRecalls != 0 {
		t.Fatalf("shared candidate must not be audited, got %+v", got.Vault)
	}
}

func TestAdoptionJSONLWindowOwnershipAcrossEvents(t *testing.T) {
	for _, scenario := range []string{"overlapping windows", "outside batch", "unrelated older event"} {
		t.Run(scenario, func(t *testing.T) {
			home, path := adoptionTestHome(t)
			db := openAdoptionFixtureDB(t, path)
			defer db.Close()
			at := time.Now().Unix() - 120
			const cue = "handover decision"
			event := roiNoeticEvent{NoeticID: "nt_first", OccurredAt: at, QueryText: cue, Status: "active"}
			stream := []roiNoeticEvent{event}
			lines := adoptionRecallLines("call_first", cue, "source /repo/amber.go", at)
			lines = append(lines, adoptionToolUseLine("read_first", "Read", `{"file_path":"/repo/amber.go"}`, at+2))
			writeAdoptionJSONL(t, home, lines)
			other := event
			other.NoeticID = "nt_other"
			want := "pending"
			switch scenario {
			case "overlapping windows":
				other.OccurredAt += 20
				writeAdoptionJSONLAt(t, home, "other", "session-b", adoptionRecallLines("call_second", cue, "source /repo/blue.go", at+40))
			case "outside batch":
				for len(stream) < adoptionAuditBatchLimit {
					id := fmt.Sprintf("nt_filler_%d", len(stream))
					stream = append(stream, roiNoeticEvent{NoeticID: id, OccurredAt: at, QueryText: id, Status: "active"})
				}
			case "unrelated older event":
				other.OccurredAt -= 60
				want = "adopted_tool"
			}
			stream = append(stream, other)
			auditNoeticStreamAdoption(context.Background(), db, home, path, stream)
			if got := stream[0].AdoptionOutcome; got != want {
				t.Fatalf("first recall = %q, want %q", got, want)
			}
		})
	}
}

func TestSelectAdoptionAuditEventsPrioritizesSettleable(t *testing.T) {
	old := roiNoeticEvent{NoeticID: "nt_old", OccurredAt: 100}
	recent := make([]roiNoeticEvent, 120)
	for i := range recent {
		recent[i] = roiNoeticEvent{NoeticID: fmt.Sprintf("nt_recent_%03d", i), OccurredAt: 200}
	}
	got := selectAdoptionAuditEvents([]roiNoeticEvent{old}, recent, adoptionAuditBatchLimit)
	if len(got) != adoptionAuditBatchLimit || got[0].NoeticID != "nt_old" {
		t.Fatalf("settleable event must lead the batch and cap at %d, got len=%d first=%q", adoptionAuditBatchLimit, len(got), got[0].NoeticID)
	}
}

// TestAdoptionUnpairedRecallIgnoresVaultFragments is the ZCode-shaped
// counterexample: no generic JSONL session/call window means the recall is
// unobserved (pending), never unadopted from arbitrary vault rows.
func TestAdoptionUnpairedRecallIgnoresVaultFragments(t *testing.T) {
	_, path := adoptionTestHome(t)
	now := time.Now().Unix()
	db := openAdoptionFixtureDB(t, path)
	insertAdoptionNoetic(t, db, "nt_unobserved", now-1200, "vault-only cue", "summary", "[]", "[]", "default")
	insertAdoptionUnit(t, db, "ep_vault_tool", now-1195, `[tool] {"file_path":"/repo/vaulted.go"}`)
	insertAdoptionUnit(t, db, "ep_vault_reply", now-1194, "[assistant] The fix is /repo/vaulted.go.")
	_ = db.Close()

	got := queryROIFacts(context.Background(), path, roiFactsRequest{Scope: "mem:self"})
	ev := adoptionOutcomeByID(got, "nt_unobserved")
	if ev.AdoptionOutcome != "pending" || got.Vault.AuditedRecalls != 0 || got.Vault.AdoptedRecalls != 0 {
		t.Fatalf("unpaired recall must remain unobserved, got outcome=%q summary=%+v", ev.AdoptionOutcome, got.Vault)
	}
}
