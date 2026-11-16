// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package store

import (
	"context"
	"strings"
	"testing"

	"github.com/rocareer/rolling-memory/internal/memcore"
)

// End-to-end scenario simulation over the real write/read kernel:
// memcore.Engine.RememberBatch (graph encoding) -> SQLite persistence -> memcore.ScoreAndAssembleBands
// (lexical/morphological scoring, prospective-loop boost, band assembly and rendering).
//
// The four scenarios mirror the long-running-agent memory requirements:
//  1. continuing a long task across context compression (goal / steps / completed vs open);
//  2. reusing project material long-term with versioned decisions and source locations;
//  3. handing work between parent and child agents;
//  4. recovering an operation with an external side effect (receipt + pending verification).
//
// Remarks are kept neutral on purpose: this tree ships publicly.

const (
	simDay = int64(86400)
	simT0  = int64(1760000000)
)

type simTurn struct {
	session string
	turn    int64
	at      int64
	author  string
	text    string
	items   []map[string]any
}

type simRow struct {
	NodeID        string
	SubjectID     string
	ObjectID      string
	ObjectLiteral string
	ObjectGap     string
	Warrant       string
	Status        string
	LoopState     string
	Locus         string
	TEvent        int64
	TSource       int64
	TRecord       int64
	TValidTo      int64
	AnchorHash    string
	AnchorStart   int
	AnchorEnd     int
	SourceHash    string
	Audience      string
	SName         string
	XName         string
	OName         string
	TraceText     string
}

func (r simRow) point() string {
	obj := r.OName
	if obj == "" {
		obj = r.ObjectLiteral
	}
	return strings.TrimSpace(r.SName + " " + r.XName + " " + obj)
}

func simRemember(t *testing.T, eng *memcore.Engine, scope string, tn simTurn) *memcore.RememberBatchResult {
	t.Helper()
	trace := memcore.Trace{
		Scope: scope, Session: tn.session, Turn: tn.turn, Kind: "unit",
		Author: tn.author, Audience: "shared", Text: tn.text,
		OccurredAt: tn.at, RecordedAt: tn.at,
	}
	res, err := eng.RememberBatch(context.Background(), trace, tn.items)
	if err != nil {
		t.Fatalf("remember batch (%s): %v", tn.session, err)
	}
	if len(res.Rejected) > 0 {
		t.Fatalf("remember batch (%s) rejected: %v", tn.session, res.Rejected)
	}
	return res
}

func simLoad(t *testing.T, st *Store, scope string) []simRow {
	t.Helper()
	raw, err := queryMaps(st.conn,
		`SELECT a.node_id, coalesce(a.subject_id,'') AS subject_id, coalesce(a.object_id,'') AS object_id,
		        coalesce(a.object_literal,'') AS object_literal, coalesce(e.object_gap,'') AS object_gap,
		        a.warrant, a.status, coalesce(a.loop_state,'') AS loop_state, coalesce(a.locus,'') AS locus,
		        coalesce(e.t_event,0) AS t_event, a.t_source, a.t_record, a.t_valid_to,
		        a.anchor_trace_hash, a.anchor_start, a.anchor_end,
		        coalesce(a.source_trace_hash,'') AS source_trace_hash, a.audience,
		        coalesce(ns.label,'') AS s_name, nx.label AS x_name, coalesce(no.label,'') AS o_name,
		        coalesce(t.text,'') AS trace_text
		 FROM assertion a
		 LEFT JOIN assertion_ext e ON e.node_id = a.node_id
		 LEFT JOIN node ns ON ns.id = a.subject_id
		 JOIN node nx ON nx.id = a.rel_id
		 LEFT JOIN node no ON no.id = a.object_id
		 LEFT JOIN trace t ON t.hash = a.anchor_trace_hash
		 WHERE a.scope = ?
		 ORDER BY a.t_source ASC, a.node_id ASC`, scope)
	if err != nil {
		t.Fatalf("load assertions: %v", err)
	}
	rows := make([]simRow, 0, len(raw))
	for _, m := range raw {
		rows = append(rows, simRow{
			NodeID:        mapString(m, "node_id"),
			SubjectID:     mapString(m, "subject_id"),
			ObjectID:      mapString(m, "object_id"),
			ObjectLiteral: mapString(m, "object_literal"),
			ObjectGap:     mapString(m, "object_gap"),
			Warrant:       mapString(m, "warrant"),
			Status:        mapString(m, "status"),
			LoopState:     mapString(m, "loop_state"),
			Locus:         mapString(m, "locus"),
			TEvent:        mapInt64(m, "t_event"),
			TSource:       mapInt64(m, "t_source"),
			TRecord:       mapInt64(m, "t_record"),
			TValidTo:      mapInt64(m, "t_valid_to"),
			AnchorHash:    mapString(m, "anchor_trace_hash"),
			AnchorStart:   int(mapInt64(m, "anchor_start")),
			AnchorEnd:     int(mapInt64(m, "anchor_end")),
			SourceHash:    mapString(m, "source_trace_hash"),
			Audience:      mapString(m, "audience"),
			SName:         mapString(m, "s_name"),
			XName:         mapString(m, "x_name"),
			OName:         mapString(m, "o_name"),
			TraceText:     mapString(m, "trace_text"),
		})
	}
	return rows
}

func simFind(t *testing.T, rows []simRow, sName, xName, oName string) simRow {
	t.Helper()
	for _, r := range rows {
		if r.SName == sName && r.XName == xName && r.OName == oName {
			return r
		}
	}
	points := make([]string, 0, len(rows))
	for _, r := range rows {
		points = append(points, r.point())
	}
	t.Fatalf("assertion not found: %s %s %s (have: %v)", sName, xName, oName, points)
	return simRow{}
}

func simItems(rows []simRow) []memcore.ScoredAssertion {
	out := make([]memcore.ScoredAssertion, 0, len(rows))
	for _, r := range rows {
		snippet := ""
		if r.TraceText != "" && r.AnchorEnd > r.AnchorStart && r.AnchorStart >= 0 {
			snippet = strings.TrimSpace(sliceRunes(r.TraceText, r.AnchorStart, r.AnchorEnd))
		}
		out = append(out, memcore.ScoredAssertion{
			Assertion: memcore.Assertion{
				NodeID: r.NodeID, Scope: "", SubjectID: r.SubjectID, RelID: "",
				ObjectID: r.ObjectID, ObjectLiteral: r.ObjectLiteral, ObjectGap: r.ObjectGap,
				Warrant: r.Warrant, Status: r.Status, LoopState: r.LoopState,
				TEvent: r.TEvent, TSource: r.TSource, TRecord: r.TRecord,
				AnchorTraceHash: r.AnchorHash, AnchorStart: r.AnchorStart, AnchorEnd: r.AnchorEnd,
			},
			SubjectName: r.SName, RelName: r.XName, ObjectName: r.OName, Snippet: snippet,
		})
	}
	return out
}

func simRender(query string, rows []simRow) (string, []memcore.RecallBand) {
	bands := memcore.ScoreAndAssembleBands(query, simItems(rows), 12)
	var sb strings.Builder
	for _, b := range bands {
		sb.WriteString(b.Rendered)
		sb.WriteString("\n")
	}
	return sb.String(), bands
}

// Scenario 1: a long task survives context compression; completed work is closed automatically
// once it is realized, while the still-open goal keeps surfacing.
func TestScenarioLongTaskAcrossCompression(t *testing.T) {
	scope := "mem:test:sim:long-task"
	st := newTestStore(t, scope)
	eng := memcore.NewEngine(st.MemcoreBackend())
	restore := memcore.SetNowUnixForTest(func() int64 { return simT0 + 4*simDay })
	t.Cleanup(restore)

	simRemember(t, eng, scope, simTurn{
		session: "sess-plan", turn: 1, at: simT0, author: "owner",
		text: "I plan to finish the MemoBot architecture report this week. Current step is collecting competitor material.",
		items: []map[string]any{
			{"subject": "湖伴", "rel": "计划完成", "object": "MemoBot架构方案", "warrant": "performative",
				"time_expr": "+5d", "anchor": "I plan to finish the MemoBot architecture report this week"},
			{"subject": "湖伴", "rel": "当前步骤", "object": "收集竞品资料", "warrant": "performative",
				"time_expr": "+2d", "anchor": "Current step is collecting competitor material"},
		},
	})
	simRemember(t, eng, scope, simTurn{
		session: "sess-plan", turn: 2, at: simT0 + 3*simDay, author: "owner",
		text: "The competitor material is fully collected now; moving to draft writing.",
		items: []map[string]any{
			{"subject": "湖伴", "rel": "当前步骤", "object": "撰写方案初稿", "warrant": "performative",
				"time_expr": "+4d", "closes_prior": true,
				"anchor": "The competitor material is fully collected now; moving to draft writing"},
		},
	})

	rows := simLoad(t, st, scope)
	goal := simFind(t, rows, "湖伴", "计划完成", "MemoBot架构方案")
	stepPlan := simFind(t, rows, "湖伴", "当前步骤", "收集竞品资料")
	stepDone := simFind(t, rows, "湖伴", "当前步骤", "撰写方案初稿")

	if goal.LoopState != memcore.LoopStateOpen {
		t.Fatalf("goal should stay open, got %q", goal.LoopState)
	}
	if stepPlan.LoopState != memcore.LoopStateFulfilled {
		t.Fatalf("explicitly closed prior step should be fulfilled, got %q", stepPlan.LoopState)
	}
	if stepDone.LoopState != memcore.LoopStateOpen {
		t.Fatalf("new active step should be open, got %q", stepDone.LoopState)
	}
	t.Logf("EV[S1] goal(open)=%s · prior-step(fulfilled)=%s · next-step(open)=%s",
		goal.point(), stepPlan.point(), stepDone.point())

	// Re-query after "compression": the open goal and next step must be delivered, prior step marked fulfilled.
	rendered, bands := simRender("MemoBot architecture report competitor material draft writing", rows)
	if len(bands) == 0 {
		t.Fatalf("expected at least one band after compression")
	}
	if !strings.Contains(rendered, "计划完成") {
		t.Fatalf("open goal dropped from delivery: %s", rendered)
	}
	if !strings.Contains(rendered, "[已兑现]") {
		t.Fatalf("fulfilled prior step should be rendered with the closed-loop tag: %s", rendered)
	}
	t.Logf("EV[S1] rendered=%s", strings.ReplaceAll(strings.TrimSpace(rendered), "\n", " ⏎ "))

	// Prospective-loop boost inside the real scoring pipeline: the same row scores higher while open.
	base := simItems([]simRow{goal})[0]
	closed := base
	closed.Assertion.LoopState = memcore.LoopStateFulfilled
	openBands := memcore.ScoreAndAssembleBands("MemoBot architecture report", []memcore.ScoredAssertion{base}, 12)
	closedBands := memcore.ScoreAndAssembleBands("MemoBot architecture report", []memcore.ScoredAssertion{closed}, 12)
	if len(openBands) == 0 || len(closedBands) == 0 {
		t.Fatalf("scoring produced no bands")
	}
	ratio := openBands[0].Score / closedBands[0].Score
	if ratio < 1.10 {
		t.Fatalf("expected prospective-loop boost > 10%%, got %.3f", ratio)
	}
	t.Logf("EV[S1] boost ratio open/closed = %.3f", ratio)
}

// Scenario 2: project material is reused long-term; a newer decision supersedes the old one
// without rewriting history, and the source location survives.
func TestScenarioMaterialReuseAndVersioning(t *testing.T) {
	scope := "mem:test:sim:material"
	st := newTestStore(t, scope)
	eng := memcore.NewEngine(st.MemcoreBackend())
	restore := memcore.SetNowUnixForTest(func() int64 { return simT0 + 30*simDay })
	t.Cleanup(restore)

	simRemember(t, eng, scope, simTurn{
		session: "sess-brand", turn: 1, at: simT0, author: "owner",
		text: "Brand A main color is deep blue. Style guide v2, page 4.",
		items: []map[string]any{
			{"subject": "品牌A", "rel": "主色为", "object": "深海蓝", "warrant": "referential",
				"anchor": "Brand A main color is deep blue", "locus": "brand/A/style-guide-v2"},
		},
	})
	simRemember(t, eng, scope, simTurn{
		session: "sess-brand", turn: 2, at: simT0 + 20*simDay, author: "owner",
		text: "Update: Brand A main color changed to quantum purple. Style guide v3, page 2.",
		items: []map[string]any{
			{"subject": "品牌A", "rel": "主色为", "object": "量子紫", "warrant": "referential",
				"anchor": "changed to quantum purple", "locus": "brand/A/style-guide-v3",
				"closes_prior": true},
		},
	})

	rows := simLoad(t, st, scope)
	v1 := simFind(t, rows, "品牌A", "主色为", "深海蓝")
	v2 := simFind(t, rows, "品牌A", "主色为", "量子紫")
	if v1.Status != "superseded" || v1.TValidTo <= 0 {
		t.Fatalf("old decision should be superseded with a validity end, got status=%q valid_to=%d", v1.Status, v1.TValidTo)
	}
	if v2.Status != "active" {
		t.Fatalf("new decision should be active, got %q", v2.Status)
	}
	if v1.Locus != "brand/A/style-guide-v2" || v2.Locus != "brand/A/style-guide-v3" {
		t.Fatalf("source locations lost: v1=%q v2=%q", v1.Locus, v2.Locus)
	}
	// History is preserved verbatim: both rows keep their own anchored source text.
	if !strings.Contains(v1.TraceText, "deep blue") || !strings.Contains(v2.TraceText, "quantum purple") {
		t.Fatalf("anchored sources not preserved: v1=%q v2=%q", v1.TraceText, v2.TraceText)
	}
	t.Logf("EV[S2] v1 status=%s valid_to=%d locus=%s | v2 status=%s locus=%s",
		v1.Status, v1.TValidTo, v1.Locus, v2.Status, v2.Locus)

	// Reuse: querying the material returns the current value and keeps the retired one visible.
	rendered, _ := simRender("brand main color quantum purple deep blue", rows)
	if !strings.Contains(rendered, "量子紫") || !strings.Contains(rendered, "深海蓝") {
		t.Fatalf("material reuse should surface both current and historical values: %s", rendered)
	}
	t.Logf("EV[S2] rendered=%s", strings.ReplaceAll(strings.TrimSpace(rendered), "\n", " ⏎ "))
}

// Scenario 3: a parent agent delegates; the child agent delivers; the delegation closes itself
// and unresolved questions stay marked as open gaps.
func TestScenarioAgentHandoff(t *testing.T) {
	scope := "mem:test:sim:handoff"
	st := newTestStore(t, scope)
	eng := memcore.NewEngine(st.MemcoreBackend())
	restore := memcore.SetNowUnixForTest(func() int64 { return simT0 + 2*simDay })
	t.Cleanup(restore)

	simRemember(t, eng, scope, simTurn{
		session: "sess-parent", turn: 1, at: simT0, author: "owner",
		text: "I delegate the GEO draft to the retriever agent, workspace team/work/geo.",
		items: []map[string]any{
			{"subject": "湖伴", "rel": "子任务状态", "object": "待检索分身交付GEO初稿", "warrant": "performative",
				"anchor": "I delegate the GEO draft to the retriever agent", "locus": "team/work/geo"},
		},
	})
	simRemember(t, eng, scope, simTurn{
		session: "sess-child", turn: 1, at: simT0 + 7200, author: "owner",
		text: "The GEO draft is delivered and stored under team/work/geo.",
		items: []map[string]any{
			{"subject": "湖伴", "rel": "子任务状态", "object": "已交付GEO初稿", "warrant": "referential",
				"closes_prior": true, "anchor": "The GEO draft is delivered", "locus": "team/work/geo"},
		},
	})
	simRemember(t, eng, scope, simTurn{
		session: "sess-child", turn: 2, at: simT0 + 7300, author: "owner",
		text: "The client contact person is still unclear.",
		items: []map[string]any{
			{"subject": "湖伴", "rel": "待确认", "object_gap": "referent_unresolvable", "warrant": "performative",
				"anchor": "client contact person is still unclear"},
		},
	})

	rows := simLoad(t, st, scope)
	delegation := simFind(t, rows, "湖伴", "子任务状态", "待检索分身交付GEO初稿")
	delivery := simFind(t, rows, "湖伴", "子任务状态", "已交付GEO初稿")
	if delegation.LoopState != memcore.LoopStateFulfilled {
		t.Fatalf("child delivery with closes_prior should close the parent delegation, got %q", delegation.LoopState)
	}
	if delivery.Locus != "team/work/geo" || delegation.Locus != "team/work/geo" {
		t.Fatalf("shared workspace location lost: parent=%q child=%q", delegation.Locus, delivery.Locus)
	}
	var unresolved simRow
	for _, r := range rows {
		if r.ObjectGap != "" {
			unresolved = r
		}
	}
	if unresolved.NodeID == "" {
		t.Fatalf("expected one unresolved-object assertion, rows=%v", rows)
	}
	if unresolved.LoopState != memcore.LoopStateOpen {
		t.Fatalf("unresolved follow-up should stay open, got %q", unresolved.LoopState)
	}
	t.Logf("EV[S3] delegation loop=%s locus=%s | delivery locus=%s | unresolved gap=%s loop=%s",
		delegation.LoopState, delegation.Locus, delivery.Locus, unresolved.ObjectGap, unresolved.LoopState)

	rendered, _ := simRender("GEO draft delivered workspace", rows)
	if !strings.Contains(rendered, "GEO初稿") {
		t.Fatalf("handoff artifacts missing from delivery: %s", rendered)
	}
	t.Logf("EV[S3] rendered=%s", strings.ReplaceAll(strings.TrimSpace(rendered), "\n", " ⏎ "))
}

// Scenario 4: an operation with an external side effect keeps a pending-verification loop, then
// closes it once a real receipt arrives. Replaying the same receipt is idempotent.
func TestScenarioExternalSideEffectRecovery(t *testing.T) {
	scope := "mem:test:sim:side-effect"
	st := newTestStore(t, scope)
	eng := memcore.NewEngine(st.MemcoreBackend())
	restore := memcore.SetNowUnixForTest(func() int64 { return simT0 + 2*simDay })
	t.Cleanup(restore)

	simRemember(t, eng, scope, simTurn{
		session: "sess-quote", turn: 1, at: simT0, author: "owner",
		text: "Quote submitted to client A, awaiting platform confirmation. Ref Q-2026-31.",
		items: []map[string]any{
			{"subject": "报价单Q-2026-31", "rel": "核验状态", "object": "待客户A确认", "warrant": "performative",
				"anchor": "Quote submitted to client A, awaiting platform confirmation", "locus": "task/quote/Q-2026-31"},
		},
	})
	before := simFind(t, simLoad(t, st, scope), "报价单Q-2026-31", "核验状态", "待客户A确认")
	if before.LoopState != memcore.LoopStateOpen {
		t.Fatalf("submitted quote should stay pending verification (open), got %q", before.LoopState)
	}

	// Falsification guard: an unrelated experiential encounter with the same entity must NOT
	// falsely close an open commitment without explicit closes_prior!
	simRemember(t, eng, scope, simTurn{
		session: "sess-quote", turn: 2, at: simT0 + 3600, author: "owner",
		text: "Met client A in the elevator this morning.",
		items: []map[string]any{
			{"subject": "报价单Q-2026-31", "rel": "关联方偶遇", "object": "待客户A确认", "warrant": "experiential",
				"anchor": "Met client A in the elevator"},
		},
	})
	stillOpen := simFind(t, simLoad(t, st, scope), "报价单Q-2026-31", "核验状态", "待客户A确认")
	if stillOpen.LoopState != memcore.LoopStateOpen {
		t.Fatalf("unrelated experiential assertion on same (S,O) must NOT auto-close open commitment, got %q", stillOpen.LoopState)
	}

	receiptTurn := simTurn{
		session: "sess-quote", turn: 3, at: simT0 + simDay, author: "owner",
		text: "Receipt Q-2026-31 received from client A, quote accepted.",
		items: []map[string]any{
			{"subject": "报价单Q-2026-31", "rel": "核验状态", "object": "已收客户A确认回执", "warrant": "experiential",
				"closes_prior": true, "anchor": "Receipt Q-2026-31 received from client A", "locus": "task/quote/Q-2026-31"},
		},
	}
	simRemember(t, eng, scope, receiptTurn)

	rows := simLoad(t, st, scope)
	closed := simFind(t, rows, "报价单Q-2026-31", "核验状态", "待客户A确认")
	receipt := simFind(t, rows, "报价单Q-2026-31", "核验状态", "已收客户A确认回执")
	if closed.LoopState != memcore.LoopStateFulfilled {
		t.Fatalf("real receipt should close the pending loop, got %q", closed.LoopState)
	}
	if receipt.Status != "active" || receipt.Warrant != "experiential" {
		t.Fatalf("receipt row should be an active experiential fact, got status=%q warrant=%q", receipt.Status, receipt.Warrant)
	}
	t.Logf("EV[S4] quote loop=%s | receipt status=%s warrant=%s locus=%s",
		closed.LoopState, receipt.Status, receipt.Warrant, receipt.Locus)

	rendered, _ := simRender("quote client A receipt", rows)
	if !strings.Contains(rendered, "[已兑现]") {
		t.Fatalf("closed pending loop should render the closed-loop tag: %s", rendered)
	}
	t.Logf("EV[S4] rendered=%s", strings.ReplaceAll(strings.TrimSpace(rendered), "\n", " ⏎ "))

	// Replaying the same receipt turn must not duplicate rows (idempotent batch write).
	beforeCount := len(rows)
	simRemember(t, eng, scope, receiptTurn)
	afterCount := len(simLoad(t, st, scope))
	if afterCount != beforeCount {
		t.Fatalf("replaying the receipt duplicated rows: %d -> %d", beforeCount, afterCount)
	}
	t.Logf("EV[S4] idempotent replay: assertions %d -> %d", beforeCount, afterCount)
}

// A caller-supplied anchor that is not a verbatim span of the source trace must be rejected,
// never silently widened to the whole trace.
func TestScenarioAnchorFailClosed(t *testing.T) {
	scope := "mem:test:sim:anchor"
	st := newTestStore(t, scope)
	eng := memcore.NewEngine(st.MemcoreBackend())

	trace := memcore.Trace{
		Scope: scope, Session: "sess-anchor", Turn: 1, Kind: "unit",
		Author: "owner", Audience: "shared", Text: "The draft was reviewed yesterday.",
		OccurredAt: simT0, RecordedAt: simT0,
	}
	res, err := eng.RememberBatch(context.Background(), trace, []map[string]any{
		{"subject": "湖伴", "rel": "审阅了", "object": "初稿", "warrant": "referential",
			"anchor": "this phrase is absent from the source"},
	})
	if err != nil {
		t.Fatalf("remember batch: %v", err)
	}
	if len(res.Rejected) == 0 {
		t.Fatalf("expected the non-verbatim anchor to be rejected, written=%d", len(res.Written))
	}
	if rows := simLoad(t, st, scope); len(rows) != 0 {
		t.Fatalf("rejected batch must not persist assertions, got %d rows", len(rows))
	}
	t.Logf("EV[S6] rejected=%v assertions=0", res.Rejected)
}

// A stale open loop that is never mentioned again must decay back to the neutral level, so that
// abandoned intentions do not dominate delivery months later.
func TestScenarioStaleOpenLoopDecays(t *testing.T) {
	scope := "mem:test:sim:stale"
	st := newTestStore(t, scope)
	eng := memcore.NewEngine(st.MemcoreBackend())

	simRemember(t, eng, scope, simTurn{
		session: "sess-old", turn: 1, at: simT0, author: "owner",
		text: "I plan to reorganize the archive folder next week.",
		items: []map[string]any{
			{"subject": "湖伴", "rel": "计划整理", "object": "归档目录", "warrant": "performative",
				"time_expr": "+7d", "anchor": "I plan to reorganize the archive folder next week"},
		},
	})
	row := simFind(t, simLoad(t, st, scope), "湖伴", "计划整理", "归档目录")

	// Fresh window: still boosted.
	freshRestore := memcore.SetNowUnixForTest(func() int64 { return simT0 + 2*simDay })
	freshBands := memcore.ScoreAndAssembleBands("archive folder reorganize", simItems([]simRow{row}), 12)
	freshRestore()

	// 120 days without any mention: boost must be gone.
	staleRestore := memcore.SetNowUnixForTest(func() int64 { return simT0 + 120*simDay })
	staleBands := memcore.ScoreAndAssembleBands("archive folder reorganize", simItems([]simRow{row}), 12)
	staleRestore()

	if len(freshBands) == 0 || len(staleBands) == 0 {
		t.Fatalf("scoring produced no bands")
	}
	if staleBands[0].Score >= freshBands[0].Score {
		t.Fatalf("stale open loop should score lower than a fresh one: fresh=%.3f stale=%.3f",
			freshBands[0].Score, staleBands[0].Score)
	}
	t.Logf("EV[S5] fresh=%.3f stale(120d)=%.3f ratio=%.3f",
		freshBands[0].Score, staleBands[0].Score, staleBands[0].Score/freshBands[0].Score)
	if ratio := staleBands[0].Score / freshBands[0].Score; ratio < 0.74 || ratio > 0.92 {
		t.Fatalf("expected stale decay back to neutral (0.74..0.92 of fresh), got %.3f", ratio)
	}
}
