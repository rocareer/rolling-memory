// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package episodic

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func openZCodeFixture(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "zcode.db"))
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	// The turn-prefix cache keys on PRAGMA data_version, which is per connection;
	// it stays off on a pooled handle. One connection is the shape the vault's read
	// handle uses, so the fixture exercises the cached path.
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	for _, ddl := range []string{
		`CREATE TABLE message (id TEXT PRIMARY KEY, session_id TEXT, sequence INTEGER, time_created INTEGER, data TEXT)`,
		`CREATE TABLE part (id TEXT PRIMARY KEY, message_id TEXT, session_id TEXT, sequence INTEGER, data TEXT)`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatalf("ddl: %v", err)
		}
	}
	insertRaw := func(msgID string, seq int, ts int64, data string) {
		if _, err := db.Exec(`INSERT INTO message(id, session_id, sequence, time_created, data) VALUES (?, 's1', ?, ?, ?)`,
			msgID, seq, ts, data); err != nil {
			t.Fatalf("insert message: %v", err)
		}
	}
	insert := func(msgID string, seq int, ts int64, role string) {
		insertRaw(msgID, seq, ts, `{"role":"`+role+`"}`)
	}
	partAt := func(msgID string, seq int, payload string) {
		if _, err := db.Exec(`INSERT INTO part(id, message_id, session_id, sequence, data) VALUES (?, ?, 's1', ?, ?)`,
			msgID+"-p"+string(rune('a'+seq)), msgID, seq, payload); err != nil {
			t.Fatalf("insert part: %v", err)
		}
	}
	insert("m1", 1, 1793102400, "user")
	partAt("m1", 1, `{"type":"text","text":"<system-reminder>hidden banner</system-reminder>请把缓存按 docID 失效。"}`)
	insertRaw("m2", 2, 1793102410, `{"role":"assistant","finish":"tool-calls"}`)
	partAt("m2", 1, `{"type":"reasoning","text":"internal monologue"}`)
	partAt("m2", 2, `{"type":"text","text":"先确认缓存失效断言。"}`)
	partAt("m2", 3, `{"type":"tool","text":"{\"cmd\":\"go test ./...\"}"}`)
	partAt("m2", 4, `{"type":"text","text":"[thinking] internal scratch\n\n结论：缓存按 docID 失效。"}`)
	partAt("m2", 5, `{"type":"text","text":"harness generated summary","synthetic":1}`)
	partAt("m2", 6, `{"reasoning":"internal monologue","type":"text","text":"双字段零件仍是语音。"}`)
	insertRaw("m2b", 3, 1793102412, `{"role":"assistant","finish":"other"}`)
	partAt("m2b", 1, `{"type":"text","text":"Thinking Process: 继续排查缓存。"}`)
	partAt("m2b", 2, `{"type":"tool","text":"{\"cmd\":\"rg cache\"}"}`)
	insertRaw("m2c", 4, 1793102414, `{"role":"assistant","finish":"stop"}`)
	partAt("m2c", 1, `{"type":"text","text":"缓存按 docID 失效。"}`)
	partAt("m2c", 2, `{"reasoning":"internal monologue","type":"text","text":"双字段零件仍是语音。"}`)
	partAt("m2c", 3, `{"type":"text","text":"<system-reminder>hidden banner</system-reminder>确认按 docID 失效。"}`)
	insertRaw("m2d", 5, 1793102416, `{"role":"assistant","semantics":{"kind":"assistant_response","uiVisibility":"hidden"}}`)
	partAt("m2d", 1, `{"type":"text","text":"隐藏旁白。"}`)
	insert("m3", 8, 1793102420, "user")
	partAt("m3", 1, `{"type":"text","text":"好，继续。"}`)
	insert("m4", 9, 1793102430, "assistant")
	partAt("m4", 1, `{"type":"text","text":"已继续。"}`)
	return db
}

// TestZCodeSegmentsAgreeAcrossPaths pins the contract that makes `#<k>` pointers
// safe: the whole-session scanner, the live source scan and the single-turn
// resolver enumerate exactly the same segments in the same order — every site
// appends the canonical `ZCodeTextPartFilter`.
func TestZCodeSegmentsAgreeAcrossPaths(t *testing.T) {
	db := openZCodeFixture(t)
	turns, err := ZCodeSessionTurns(db, "s1")
	if err != nil {
		t.Fatalf("session turns: %v", err)
	}
	if len(turns) != 2 {
		t.Fatalf("expected 2 closed turns, got %d (%+v)", len(turns), turns)
	}
	var visited []Turn
	if err := VisitZCodeTurnsFrom(context.Background(), db, "s1", "", func(turn Turn, closed bool) error {
		visited = append(visited, turn)
		return nil
	}); err != nil {
		t.Fatalf("visit turns: %v", err)
	}
	if len(visited) != len(turns) {
		t.Fatalf("source scan enumerated %d turns, scanner %d", len(visited), len(turns))
	}
	for index, turn := range turns {
		if visited[index].UserMsgID != turn.UserMsgID {
			t.Fatalf("turn %d: source scan=%s scanner=%s", index, visited[index].UserMsgID, turn.UserMsgID)
		}
		resolved, err := ZCodeTurnSegments(db, "s1", turn.UserMsgID)
		if err != nil {
			t.Fatalf("turn %s segments: %v", turn.UserMsgID, err)
		}
		compare := func(name string, other []Segment) {
			t.Helper()
			if len(turn.Segments) != len(other) {
				t.Fatalf("turn %s: scanner=%d %s=%d", turn.UserMsgID, len(turn.Segments), name, len(other))
			}
			for index := range turn.Segments {
				if turn.Segments[index] != other[index] {
					t.Fatalf("turn %s segment %d diverges: scanner=%+v %s=%+v",
						turn.UserMsgID, index, turn.Segments[index], name, other[index])
				}
			}
		}
		compare("source_scan", visited[index].Segments)
		compare("resolver", resolved)
	}
}

// TestZCodeSegmentsAreSpeechOnly pins the part and message filters plus the
// injected-envelope strip: reasoning/tool/synthetic parts never become segments, a
// tool step's text never becomes speech (whatever its finish shape or the JSON head
// of the part), a harness-hidden message never becomes speech, and only the answer
// step's text is conversation.
func TestZCodeSegmentsAreSpeechOnly(t *testing.T) {
	db := openZCodeFixture(t)
	turns, err := ZCodeSessionTurns(db, "s1")
	if err != nil {
		t.Fatalf("session turns: %v", err)
	}
	first := turns[0]
	if len(first.Segments) != 4 {
		t.Fatalf("expected 4 speech segments, got %+v", first.Segments)
	}
	wants := []struct {
		role string
		text string
	}{
		{RoleUser, "请把缓存按 docID 失效。"},
		{RoleAssistant, "缓存按 docID 失效。"},
		{RoleAssistant, "双字段零件仍是语音。"},
		{RoleAssistant, "确认按 docID 失效。"},
	}
	for index, want := range wants {
		got := first.Segments[index]
		if got.Role != want.role || got.Text != want.text || got.Index != index {
			t.Fatalf("segment %d = %+v, want %+v", index, got, want)
		}
	}
	for _, segment := range first.Segments {
		for _, noise := range []string{
			"system-reminder", "internal monologue", "internal scratch", "harness generated summary",
			"先确认缓存失效断言", "隐藏旁白", "Thinking Process",
		} {
			if strings.Contains(segment.Text, noise) {
				t.Fatalf("segment kept noise %q: %q", noise, segment.Text)
			}
		}
	}
}

func writeJSONL(t *testing.T, lines []string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session.jsonl")
	content := strings.Join(append(lines, ""), "\n")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write jsonl: %v", err)
	}
	return path
}

// TestJSONLEnumeration pins the JSONL shapes (string content, block lists, Codex
// payload envelopes, meta rows) and that per-turn resolution matches the scan.
func TestJSONLEnumeration(t *testing.T) {
	path := writeJSONL(t, []string{
		`{"type":"user","timestamp":"2026-10-20T10:00:00Z","message":{"role":"user","content":"第一句，用户的话。"}}`,
		`{"type":"assistant","timestamp":"2026-10-20T10:00:10Z","message":{"role":"assistant","content":[` +
			`{"type":"thinking","thinking":"internal monologue"},` +
			`{"type":"text","text":"先取证。"},` +
			`{"type":"tool_use","name":"Bash","input":{"cmd":"ls"}}]}}`,
		`{"type":"user","timestamp":"2026-10-20T10:01:00Z","message":{"role":"user","content":[{"type":"text","text":"继续。"}]}}`,
		`{"type":"assistant","timestamp":"2026-10-20T10:01:10Z","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"已完成。"}]}}`,
		`{"isMeta":true,"type":"user","message":{"role":"user","content":"meta row"}}`,
		`{"type":"user","timestamp":"2026-10-20T10:02:00Z","message":{"role":"user","content":"# AGENTS.md instructions for /repo"}}`,
	})
	turns, err := JSONLTurns(path)
	if err != nil {
		t.Fatalf("jsonl turns: %v", err)
	}
	if len(turns) != 2 {
		t.Fatalf("expected 2 turns, got %d (%+v)", len(turns), turns)
	}
	if got := turns[0].Segments; len(got) != 1 || got[0].Text != "第一句，用户的话。" {
		t.Fatalf("turn 0 segments = %+v", got)
	}
	if turns[1].Segments[1].Text != "已完成。" {
		t.Fatalf("turn 1 payload envelope parse = %+v", turns[1].Segments)
	}
	for index := range turns {
		resolved, err := JSONLTurnSegments(path, index)
		if err != nil {
			t.Fatalf("jsonl turn %d resolve: %v", index, err)
		}
		if len(resolved) != len(turns[index].Segments) {
			t.Fatalf("turn %d scanned=%d resolved=%d", index, len(turns[index].Segments), len(resolved))
		}
	}
}

// TestJSONLNarrationStepExcluded 锁定与 ZCode 工具步同义的 JSONL 过程旁白过滤：
// 1. Claude Code / WorkBuddy / CodeBuddy：assistant 步以 `stop_reason:"tool_use"` 收尾时其文本是过程旁白；
// 2. Codex / DSH：assistant `response_item` 后紧跟 `function_call` / `custom_tool_call` 时其文本是过程旁白；
// 只有回答步才是对话，且三条枚举路径（全文件扫描 / 流式扫描 / 逐轮解析）必须一致。
func TestJSONLNarrationStepExcluded(t *testing.T) {
	path := writeJSONL(t, []string{
		`{"type":"user","timestamp":"2026-10-20T10:00:00Z","message":{"role":"user","content":"修复缓存失效。"}}`,
		`{"type":"assistant","timestamp":"2026-10-20T10:00:10Z","message":{"role":"assistant","stop_reason":"tool_use","content":[{"type":"text","text":"我先看缓存失效逻辑。"}]}}`,
		`{"type":"assistant","timestamp":"2026-10-20T10:00:20Z","message":{"role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"缓存按 docID 失效。"}]}}`,
		`{"type":"response_item","timestamp":"2026-10-20T10:01:00Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"Codex 继续。"}]}}`,
		`{"type":"response_item","timestamp":"2026-10-20T10:01:05Z","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Now I will inspect the file:"}]}}`,
		`{"type":"response_item","timestamp":"2026-10-20T10:01:06Z","payload":{"type":"function_call","name":"shell","arguments":"{\"cmd\":\"ls\"}"}}`,
		`{"type":"response_item","timestamp":"2026-10-20T10:01:08Z","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Applying patch now:"}]}}`,
		`{"type":"response_item","timestamp":"2026-10-20T10:01:09Z","payload":{"type":"custom_tool_call","name":"apply_patch"}}`,
		`{"type":"response_item","timestamp":"2026-10-20T10:01:15Z","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Codex 最终回答。"}]}}`,
	})
	turns, err := JSONLTurns(path)
	if err != nil {
		t.Fatalf("jsonl turns: %v", err)
	}
	if len(turns) != 2 {
		t.Fatalf("expected 2 turns, got %+v", turns)
	}
	if got := turns[0].Segments; len(got) != 2 || got[0].Text != "修复缓存失效。" || got[1].Text != "缓存按 docID 失效。" {
		t.Fatalf("turn 0 narration leaked: %+v", got)
	}
	if got := turns[1].Segments; len(got) != 2 || got[0].Text != "Codex 继续。" || got[1].Text != "Codex 最终回答。" {
		t.Fatalf("turn 1 codex narration leaked: %+v", got)
	}
	var visited []JSONLTurn
	if err := VisitJSONLTurns(context.Background(), path, func(turn JSONLTurn, _ bool) error {
		visited = append(visited, turn)
		return nil
	}); err != nil {
		t.Fatalf("visit jsonl: %v", err)
	}
	if len(visited) != len(turns) {
		t.Fatalf("visited=%d turns=%d", len(visited), len(turns))
	}
	for idx := range turns {
		resolved, err := JSONLTurnSegments(path, idx)
		if err != nil {
			t.Fatalf("jsonl resolve %d: %v", idx, err)
		}
		if len(resolved) != len(turns[idx].Segments) || len(visited[idx].Segments) != len(turns[idx].Segments) {
			t.Fatalf("turn %d length mismatch: scan=%+v visit=%+v resolve=%+v", idx, turns[idx].Segments, visited[idx].Segments, resolved)
		}
		for sIdx := range resolved {
			if resolved[sIdx] != turns[idx].Segments[sIdx] || visited[idx].Segments[sIdx] != turns[idx].Segments[sIdx] {
				t.Fatalf("turn %d seg %d mismatch: scan=%+v visit=%+v resolve=%+v",
					idx, sIdx, turns[idx].Segments[sIdx], visited[idx].Segments[sIdx], resolved[sIdx])
			}
		}
	}
}

// TestJSONLMillisecondTimestampNormalized 锁定 WorkBuddy/CodeBuddy 毫秒级 Unix 时间戳归一化：
// `timestamp: 1794407222466`（>1e11）必须归一为秒级 `1794407222`，否则会话尾轮 `UTCNow()-OccurredAt`
// 恒为负数而永不判为 completed，导致最新一轮对话无法入库。
func TestJSONLMillisecondTimestampNormalized(t *testing.T) {
	path := writeJSONL(t, []string{
		`{"id":"m1","timestamp":1794407222466,"type":"message","role":"user","content":[{"type":"input_text","text":"WorkBuddy 同步测试。"}]}`,
		`{"id":"m2","timestamp":1794407225890,"type":"message","role":"assistant","content":[{"type":"output_text","text":"已归一化毫秒时间戳。"}]}`,
	})
	turns, err := JSONLTurns(path)
	if err != nil {
		t.Fatalf("jsonl turns: %v", err)
	}
	if len(turns) != 1 || len(turns[0].Segments) != 2 {
		t.Fatalf("expected 1 turn with 2 segments, got %+v", turns)
	}
	if turns[0].OccurredAt != 1794407225 {
		t.Fatalf("OccurredAt = %d, want 1794407225 (seconds)", turns[0].OccurredAt)
	}
	if turns[0].Segments[0].OccurredAt != 1794407222 || turns[0].Segments[1].OccurredAt != 1794407225 {
		t.Fatalf("segment timestamps not normalized to seconds: %+v", turns[0].Segments)
	}
}

func TestJSONLDSHProvenanceAndTools(t *testing.T) {
	path := writeJSONL(t, []string{
		`{"type":"session","version":4,"cwd":"/fixture"}`,
		`{"type":"turn/start","data":{"turn":1}}`,
		`{"type":"user/message","time":1794407222466,"data":{"role":"user","source":{"kind":"user"},"content":[{"type":"text","text":"Question"}]}}`,
		`{"type":"user/message","data":{"role":"user","source":{"kind":"agent-instructions"},"content":[{"type":"text","text":"Injected context"}]}}`,
		`{"type":"user/message","data":{"role":"user","content":[{"type":"text","text":"Unknown provenance"}]}}`,
		`{"type":"assistant/message","time":1794407223000,"data":{"message":{"role":"assistant","content":[{"type":"text","text":"Narration"},{"type":"tool-call","name":"shell","arguments":"secret"}]}}}`,
		`{"type":"tool/result","data":{"message":{"role":"user","content":[{"type":"text","text":"Tool output"}]}}}`,
		`{"type":"assistant/chunk","data":{"chunk":{"type":"text-delta","text":"duplicate"}}}`,
		`{"type":"assistant/message","time":1794407225890,"data":{"message":{"role":"assistant","content":[{"type":"reasoning","text":"hidden"},{"type":"text","text":"Answer"}]}}}`,
		`{"type":"turn/end","data":{"turn":1,"reason":{"kind":"completed"}}}`,
	})
	turns, err := JSONLTurns(path)
	if err != nil || len(turns) != 1 || len(turns[0].Segments) != 2 {
		t.Fatalf("turns=%+v err=%v", turns, err)
	}
	if turns[0].Segments[0].Text != "Question" || turns[0].Segments[1].Text != "Answer" || turns[0].OccurredAt != 1794407225 {
		t.Fatalf("turn = %+v", turns[0])
	}
	var visited []JSONLTurn
	var closed []bool
	if err := VisitJSONLTurns(context.Background(), path, func(turn JSONLTurn, complete bool) error {
		visited = append(visited, turn)
		closed = append(closed, complete)
		return nil
	}); err != nil || len(visited) != 1 || !closed[0] {
		t.Fatalf("visited=%+v closed=%v err=%v", visited, closed, err)
	}
	resolved, err := JSONLTurnSegments(path, 0)
	if err != nil || !reflect.DeepEqual(resolved, turns[0].Segments) || !reflect.DeepEqual(visited[0], turns[0]) {
		t.Fatalf("resolved=%+v visited=%+v turns=%+v err=%v", resolved, visited, turns, err)
	}
}

func TestFilteredToolNarrationChangesSegmentOrdinal(t *testing.T) {
	path := writeJSONL(t, []string{
		`{"type":"user","message":{"role":"user","content":"question"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":"old narration"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","name":"shell"}]}}`,
		`{"type":"assistant","message":{"role":"assistant","stop_reason":"end_turn","content":"final answer"}}`,
	})
	turns, err := JSONLTurns(path)
	if err != nil || len(turns) != 1 || len(turns[0].Segments) != 2 || turns[0].Segments[1].Text != "final answer" {
		t.Fatalf("turns=%+v err=%v", turns, err)
	}
	segments, err := JSONLTurnSegments(path, 0)
	if err != nil || !reflect.DeepEqual(segments, turns[0].Segments) {
		t.Fatalf("segments=%+v err=%v", segments, err)
	}
}

func TestJSONLCompletionEvidence(t *testing.T) {
	cases := []struct {
		name   string
		rows   []string
		closed bool
	}{
		{"unmarked", nil, false},
		{"claude completed", []string{`{"type":"assistant","message":{"role":"assistant","stop_reason":"end_turn","content":"answer"}}`}, true},
		{"codex completed", []string{`{"type":"event_msg","payload":{"type":"task_complete"}}`}, true},
		{"codex failed", []string{`{"type":"event_msg","payload":{"type":"task_complete","error":{"message":"failed"}}}`}, false},
		{"dsh completed", []string{`{"type":"turn/end","data":{"reason":{"kind":"completed"}}}`}, true},
		{"dsh aborted", []string{`{"type":"turn/end","data":{"reason":{"kind":"aborted"}}}`}, false},
		{"tool after completion", []string{`{"type":"event_msg","payload":{"type":"task_complete"}}`, `{"type":"response_item","payload":{"type":"function_call"}}`}, false},
		{"assistant after completion", []string{`{"type":"event_msg","payload":{"type":"task_complete"}}`, `{"role":"assistant","content":"still working"}`}, false},
		{"reasoning after completion", []string{`{"type":"event_msg","payload":{"type":"task_complete"}}`, `{"type":"assistant/message","data":{"message":{"role":"assistant","content":[{"type":"reasoning","text":"working"}]}}}`}, false},
		{"aborted after completion", []string{`{"type":"turn/end","data":{"reason":{"kind":"completed"}}}`, `{"type":"turn/end","data":{"reason":{"kind":"aborted"}}}`}, false},
		{"codex failure after completion", []string{`{"type":"event_msg","payload":{"type":"task_complete"}}`, `{"type":"event_msg","payload":{"type":"task_complete","error":{"message":"failed"}}}`}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeJSONL(t, append([]string{`{"role":"user","content":"question"}`}, tc.rows...))
			var states []bool
			if err := VisitJSONLTurns(context.Background(), path, func(_ JSONLTurn, complete bool) error {
				states = append(states, complete)
				return nil
			}); err != nil || len(states) != 1 || states[0] != tc.closed {
				t.Fatalf("states=%v want=%v err=%v", states, tc.closed, err)
			}
		})
	}
}

// TestZCodeTurnCacheServesEarlyPrefixAndInvalidatesOnAppend pins the resolver
// cache contract: an early-stopped prefix serves later `#0`-style lookups, a
// partial prefix never answers a whole-turn request, and a new session message
// invalidates the identity so a stale prefix cannot outlive its transcript.
func TestZCodeTurnCacheServesEarlyPrefixAndInvalidatesOnAppend(t *testing.T) {
	db := openZCodeFixture(t)
	hit0 := zcodeTurnCacheHitCount()

	first, firstComplete, err := ZCodeTurnSegmentsUpTo(db, "s1", "m1", 1)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	if len(first) != 1 || firstComplete {
		t.Fatalf("first prefix = %d complete=%v, want 1/false", len(first), firstComplete)
	}
	if got := zcodeTurnCacheHitCount(); got != hit0 {
		t.Fatalf("first call hit the cache: hits=%d want=%d", got, hit0)
	}

	second, secondComplete, err := ZCodeTurnSegmentsUpTo(db, "s1", "m1", 1)
	if err != nil || !reflect.DeepEqual(second, first) || secondComplete != firstComplete {
		t.Fatalf("second = %+v/%v err=%v, want cache hit of %+v/%v", second, secondComplete, err, first, firstComplete)
	}
	if got := zcodeTurnCacheHitCount(); got != hit0+1 {
		t.Fatalf("second call missed the cache: hits=%d want=%d", got, hit0+1)
	}

	full, fullComplete, err := ZCodeTurnSegmentsUpTo(db, "s1", "m1", 0)
	if err != nil {
		t.Fatalf("full: %v", err)
	}
	if !fullComplete || len(full) <= len(first) {
		t.Fatalf("full = %d complete=%v, want more than prefix and complete", len(full), fullComplete)
	}
	if got := zcodeTurnCacheHitCount(); got != hit0+1 {
		t.Fatalf("partial prefix answered a full-turn request: hits=%d want=%d", got, hit0+1)
	}

	fullAgain, fullAgainComplete, err := ZCodeTurnSegmentsUpTo(db, "s1", "m1", 1)
	if err != nil || !fullAgainComplete || len(fullAgain) != len(full) {
		t.Fatalf("complete entry did not serve the prefix shape: %d/%v err=%v", len(fullAgain), fullAgainComplete, err)
	}
	if got := zcodeTurnCacheHitCount(); got != hit0+2 {
		t.Fatalf("complete entry missed the cache: hits=%d want=%d", got, hit0+2)
	}

	if _, err := db.Exec(`INSERT INTO message(id, session_id, sequence, time_created, data) VALUES ('m5', 's1', 10, 1793102440, '{"role":"assistant"}')`); err != nil {
		t.Fatalf("append: %v", err)
	}
	if _, _, err := ZCodeTurnSegmentsUpTo(db, "s1", "m1", 1); err != nil {
		t.Fatalf("after append: %v", err)
	}
	if got := zcodeTurnCacheHitCount(); got != hit0+2 {
		t.Fatalf("stale prefix survived a new message: hits=%d want=%d", got, hit0+2)
	}
}

// TestZCodeTurnCacheStaysOffOnPooledHandles pins the cache contract that a
// CI-only failure exposed: PRAGMA data_version counts the commits each
// connection observed, so two pooled connections report different counts for the
// same file state and a fresh connection restarts below a count an older one
// already published. Keyed on a pooled handle, the second connection inherited a
// pre-edit prefix and hydration reported the old text as verified.
func TestZCodeTurnCacheStaysOffOnPooledHandles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "zcode-pooled.db")
	writer, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("seed handle: %v", err)
	}
	for _, stmt := range []string{
		`CREATE TABLE message (id TEXT PRIMARY KEY, session_id TEXT, sequence INTEGER, time_created INTEGER, data TEXT)`,
		`CREATE TABLE part (message_id TEXT, sequence INTEGER, data TEXT)`,
		`INSERT INTO message (id, session_id, sequence, time_created, data) VALUES ('u1', 's1', 1, 1700000000, '{"role":"user"}')`,
		`INSERT INTO part (message_id, sequence, data) VALUES ('u1', 0, '{"type":"text","text":"fixture question"}')`,
		`INSERT INTO message (id, session_id, sequence, time_created, data) VALUES ('a1', 's1', 2, 1700000001, '{"role":"assistant"}')`,
		`INSERT INTO part (message_id, sequence, data) VALUES ('a1', 0, '{"type":"text","text":"fixture reply"}')`,
	} {
		if _, err := writer.Exec(stmt); err != nil {
			t.Fatalf("seed (%s): %v", stmt, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close seed handle: %v", err)
	}

	reader, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatalf("reader handle: %v", err)
	}
	defer reader.Close()
	reader.SetMaxOpenConns(2)
	reader.SetMaxIdleConns(2)

	warm, _, err := ZCodeTurnSegmentsUpTo(reader, "s1", "u1", 3)
	if err != nil || len(warm) == 0 || warm[0].Text != "fixture question" {
		t.Fatalf("warm read = %+v err=%v", warm, err)
	}
	hits := zcodeTurnCacheHitCount()

	edit, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("edit handle: %v", err)
	}
	if _, err := edit.Exec(`UPDATE part SET data = '{"type":"text","text":"changed primary"}' WHERE message_id = 'u1'`); err != nil {
		t.Fatalf("edit source: %v", err)
	}
	if err := edit.Close(); err != nil {
		t.Fatalf("close edit handle: %v", err)
	}

	// Hold the connection that served the warm read so the post-edit read has to
	// take the pool's second connection.
	pin, err := reader.Query(`SELECT 1 FROM part`)
	if err != nil {
		t.Fatalf("pin connection: %v", err)
	}
	defer pin.Close()
	if !pin.Next() {
		t.Fatalf("pin connection produced no row")
	}

	after, _, err := ZCodeTurnSegmentsUpTo(reader, "s1", "u1", 3)
	if err != nil {
		t.Fatalf("post-edit read: %v", err)
	}
	if len(after) == 0 || after[0].Text != "changed primary" {
		t.Fatalf("pooled handle served the pre-edit segment: %+v", after)
	}
	if got := zcodeTurnCacheHitCount(); got != hits {
		t.Fatalf("pooled handle used the turn cache: hits=%d want=%d", got, hits)
	}
}
