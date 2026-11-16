// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package recall_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/rocareer/rolling-memory/internal/api"
	"github.com/rocareer/rolling-memory/internal/clock"
	"github.com/rocareer/rolling-memory/internal/config"
	"github.com/rocareer/rolling-memory/internal/ids"
	"github.com/rocareer/rolling-memory/internal/recall"
	"github.com/rocareer/rolling-memory/internal/store"
)

const testNow = int64(1793102400)

func TestMain(m *testing.M) {
	previous := time.Local
	time.Local = time.UTC
	code := m.Run()
	time.Local = previous
	os.Exit(code)
}

func strPtr(value string) *string { return &value }

func boolPtr(value bool) *bool { return &value }

func intPtr(value int) *int { return &value }

func newMemory(t *testing.T) *api.Memory {
	t.Helper()
	restore := clock.SetForTest(time.Unix(testNow, 0), 1_000_000_000)
	t.Cleanup(restore)
	directory := t.TempDir()
	dbPath := filepath.Join(directory, "memory.db")
	configPath := filepath.Join(directory, "config.toml")
	mode := "local"
	scope := "mem:test"
	reader := "deliverable"
	cfg, err := config.Load(config.LoadOptions{
		Path: &configPath, DB: &dbPath, Mode: &mode, Scope: &scope, Reader: &reader,
		Env: map[string]string{},
	})
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	cfg.Log = filepath.Join(directory, "state", "rolling-memory.log")
	memory, err := api.New(cfg)
	if err != nil {
		t.Fatalf("api: %v", err)
	}
	t.Cleanup(func() { memory.Close() })
	return memory
}

func writeFact(t *testing.T, memory *api.Memory, content, s, x string, o any, extra map[string]any) map[string]any {
	t.Helper()
	assertion := map[string]any{
		"s": map[string]any{"label": s},
		"x": x,
		"o": o,
	}
	for key, value := range extra {
		assertion[key] = value
	}
	receipt, err := memory.Remember(api.RememberRequest{
		Content: strPtr(content), AnchorText: strPtr(content), Assertion: assertion,
		Author: "user", AutoSync: boolPtr(false),
	})
	if err != nil {
		t.Fatalf("remember %q: %v", content, err)
	}
	return receipt
}

func seedGraph(t *testing.T, memory *api.Memory) {
	t.Helper()
	writeFact(t, memory, "我养了一只猫，叫小白。", "我", "养", map[string]any{"label": "小白"},
		map[string]any{"t_valid_from": "2026-03-02", "world": "actual", "carrier": "real", "locus": "北京"})
	writeFact(t, memory, "我喜欢喝咖啡。", "我", "喜欢", map[string]any{"label": "咖啡"},
		map[string]any{"t_valid_from": "2026-04-01", "world": "actual", "carrier": "real"})
	writeFact(t, memory, "我搬到北京。", "我", "住在", map[string]any{"label": "北京"},
		map[string]any{"t_valid_from": "2026-06-01", "world": "actual", "carrier": "real"})
	writeFact(t, memory, "我今年 30 岁。", "我", "年龄", map[string]any{"literal": "30"},
		map[string]any{"t_valid_from": "2026-01-01", "world": "actual", "carrier": "real"})
	writeFact(t, memory, "小白是白色的。", "小白", "是", map[string]any{"label": "白色"},
		map[string]any{"world": "actual", "carrier": "real"})
}

func recallCue(t *testing.T, memory *api.Memory, cue string, includeRaw bool) *recall.Result {
	t.Helper()
	result, err := memory.Recall(api.RecallRequest{
		Cue: cue, IncludeRaw: boolPtr(includeRaw), RawLimit: 5, Depth: 2,
	})
	if err != nil {
		t.Fatalf("recall %q: %v", cue, err)
	}
	return result
}

func TestProbeEvidenceAndHintResolution(t *testing.T) {
	memory := newMemory(t)
	seedGraph(t, memory)
	writeFact(t, memory, "路边有人在唱歌。", "路人", "在", map[string]any{"label": "路边"},
		map[string]any{"confidence": 0.35})

	settled := recallCue(t, memory, "我", false)
	settledEvidence := map[string]string{}
	for _, hitAny := range settled.ToProbe()["hits"].([]any) {
		hit := hitAny.(map[string]any)
		settledEvidence[hit["summary"].(string)] = hit["evidence"].(string)
	}
	if settledEvidence["我 —养→ 小白"] != "fact" {
		t.Fatalf("settled hit evidence = %v (all: %v)", settledEvidence["我 —养→ 小白"], settledEvidence)
	}

	mention := recallCue(t, memory, "路人", false)
	mentionEvidence := map[string]string{}
	for _, hitAny := range mention.ToProbe()["hits"].([]any) {
		hit := hitAny.(map[string]any)
		mentionEvidence[hit["summary"].(string)] = hit["evidence"].(string)
	}
	if mentionEvidence["路人 —在→ 路边"] != "mention_only" {
		t.Fatalf("mention-floor hit evidence = %v (all: %v)", mentionEvidence["路人 —在→ 路边"], mentionEvidence)
	}

	// The public s/x/o request slots were removed with the structured memory assertion contract; the
	// hint channel is internal-only, so these calls go through recall.Options.
	resolved, err := recall.Recall(memory.Store, memory.Config, recall.Options{
		Cue: "小白", IncludeRaw: boolPtr(false), Structured: map[string]string{"s": "我"},
	})
	if err != nil {
		t.Fatalf("recall with hint: %v", err)
	}
	hints := resolved.Meta["hints"].(map[string]any)
	if hints["s"] != "resolved" {
		t.Fatalf("resolved hints = %#v", hints)
	}

	missing, err := recall.Recall(memory.Store, memory.Config, recall.Options{
		Cue: "小白", IncludeRaw: boolPtr(false),
		Structured: map[string]string{"s": "不存在的实体", "x": "养"},
	})
	if err != nil {
		t.Fatalf("recall with missing hint: %v", err)
	}
	missingHints := missing.Meta["hints"].(map[string]any)
	if missingHints["s"] != "unresolved" || missingHints["x"] != "resolved" {
		t.Fatalf("missing hints = %#v", missingHints)
	}
	if warning, ok := missingHints["warning"].(string); !ok || warning == "" {
		t.Fatalf("missing hints warning = %#v", missingHints)
	}
}

func TestBandsChainsAndLabels(t *testing.T) {
	memory := newMemory(t)
	seedGraph(t, memory)
	result := recallCue(t, memory, "我 小白", false)
	if result.Absent {
		t.Fatal("expected a non-absent result")
	}
	titles := []string{}
	chainTypes := map[string]struct{}{}
	for _, band := range result.Bands {
		titles = append(titles, band["title"].(string))
		for _, chain := range band["chains"].([]any) {
			chainTypes[chain.(map[string]any)["type"].(string)] = struct{}{}
		}
	}
	foundTitles := map[string]bool{}
	for _, title := range titles {
		foundTitles[title] = true
	}
	if !foundTitles["带·我"] || !foundTitles["带·小白"] {
		t.Fatalf("titles = %#v", titles)
	}
	if _, ok := chainTypes["SO"]; !ok {
		t.Fatalf("chain types = %#v", chainTypes)
	}
	if _, ok := chainTypes["SX"]; !ok {
		t.Fatalf("chain types = %#v", chainTypes)
	}
	rendered := result.Render(8000)
	for _, fragment := range []string{"我 —养→ 小白", "2026-03-02", "real × actual"} {
		if !strings.Contains(rendered, fragment) {
			t.Fatalf("render missing %q:\n%s", fragment, rendered)
		}
	}
	// 交付渲染不带强度数字（本组内相对强度不是可信度，逐条印会被当权威读）。
	if strings.Contains(rendered, "conf ") {
		t.Fatalf("delivery rendering must not carry a strength number:\n%s", rendered)
	}
	if strings.Contains(rendered, "锚 trace:") {
		t.Fatalf("opaque anchor leaked into the delivery rendering:\n%s", rendered)
	}
	meta := result.Meta
	if meta["anchors_checked"].(int) == 0 || meta["anchor_failed"].(int) != 0 {
		t.Fatalf("anchor dose = %#v", meta)
	}
}

func TestDeterminism(t *testing.T) {
	memory := newMemory(t)
	seedGraph(t, memory)
	first := recallCue(t, memory, "我", false)
	second := recallCue(t, memory, "我", false)
	if !reflect.DeepEqual(first.Bands, second.Bands) {
		t.Fatal("bands changed between calls")
	}
	if !reflect.DeepEqual(first.Gaps, second.Gaps) || first.Absent != second.Absent {
		t.Fatal("gaps/absent changed between calls")
	}
}

func TestAbsentIsFirstClass(t *testing.T) {
	memory := newMemory(t)
	result := recallCue(t, memory, "zzzqqqxxx", false)
	if !result.Absent || len(result.Bands) != 0 {
		t.Fatalf("result = %#v", result.ToDict())
	}
	if !strings.Contains(result.Render(8000), "不持有") {
		t.Fatal("absent render missing the not-held line")
	}
	if result.ToProbe()["absent"] != true {
		t.Fatalf("probe = %#v", result.ToProbe())
	}
}

func TestRawFallback(t *testing.T) {
	memory := newMemory(t)
	if _, _, err := memory.Store.AddTrace("mem:test", "项目使用 SQLite 和 FTS5 做本地检索。",
		store.TraceOptions{Session: "raw", Author: "user"}); err != nil {
		t.Fatalf("trace: %v", err)
	}
	result := recallCue(t, memory, "FTS5", true)
	if len(result.RawHits) == 0 {
		t.Fatal("expected raw hits")
	}
	if !strings.Contains(result.Render(8000), "SQLite") {
		t.Fatal("render missing the raw evidence")
	}
}

func TestPrivateRawIsNotDelivered(t *testing.T) {
	memory := newMemory(t)
	if _, _, err := memory.Store.AddTrace("mem:test", "绝密线索 XYZ 只给 owner 看。",
		store.TraceOptions{Session: "private", Author: "user", Audience: "private"}); err != nil {
		t.Fatalf("trace: %v", err)
	}
	deliverable := recallCue(t, memory, "绝密线索 XYZ", true)
	if !deliverable.Absent {
		t.Fatalf("deliverable = %#v", deliverable.ToDict())
	}
	if len(deliverable.RawHits) != 0 {
		t.Fatalf("deliverable raw hits = %#v", deliverable.RawHits)
	}
	owner, err := memory.Recall(api.RecallRequest{
		Cue: "绝密线索 XYZ", Reader: strPtr("owner"), IncludeRaw: boolPtr(true),
	})
	if err != nil {
		t.Fatalf("owner recall: %v", err)
	}
	if owner.Absent {
		t.Fatalf("owner = %#v", owner.ToDict())
	}
	if len(owner.RawHits) == 0 {
		t.Fatal("owner should see the private raw hit")
	}
}

func TestCapsAndProbe(t *testing.T) {
	memory := newMemory(t)
	for _, object := range []string{"咖啡", "茶", "牛奶", "果汁", "汽水"} {
		writeFact(t, memory, "我喜欢"+object+"。", "我", "喜欢", map[string]any{"label": object}, nil)
	}
	result, err := memory.Recall(api.RecallRequest{
		Cue: "我 喜欢", IncludeRaw: boolPtr(false),
		CapsBands: intPtr(1), CapsNodes: intPtr(2),
	})
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if len(result.Bands) > 1 {
		t.Fatalf("bands = %d", len(result.Bands))
	}
	if nodes := result.Bands[0]["nodes"].([]map[string]any); len(nodes) > 2 {
		t.Fatalf("nodes = %d", len(nodes))
	}
	probe := result.ToProbe()
	hits := probe["hits"].([]any)
	if len(hits) == 0 || len(hits) > 12 {
		t.Fatalf("probe hits = %#v", hits)
	}
	hit := hits[0].(map[string]any)
	for _, key := range []string{"id", "summary", "date", "status", "anchor", "confidence"} {
		if _, ok := hit[key]; !ok {
			t.Fatalf("probe hit missing %q: %#v", key, hit)
		}
	}
}

func TestDateWindowSupplement(t *testing.T) {
	memory := newMemory(t)
	occurred := int64(1689292800) // 2023-07-14
	if _, _, err := memory.Store.AddTrace("mem:test",
		"zebra migration observation in the field notes.",
		store.TraceOptions{Session: "date-window", Author: "user", OccurredAt: &occurred}); err != nil {
		t.Fatalf("trace: %v", err)
	}
	result := recallCue(t, memory, "zebra migration July 2023", false)
	window := result.Meta["date_window"].(map[string]any)
	if window["derived"] != true {
		t.Fatalf("date_window = %#v", window)
	}
	if window["start"] != int64(1688169600) || window["end"] != int64(1690848000) {
		t.Fatalf("date_window = %#v", window)
	}
	if len(result.RawHits) == 0 {
		t.Fatal("expected the boosted raw hit")
	}
}

func TestSessionFilter(t *testing.T) {
	memory := newMemory(t)
	for _, item := range []struct{ session, content, object string }{
		{"s1", "老张养了一只猫。", "猫"},
		{"s2", "老张养了一只狗。", "狗"},
	} {
		if _, err := memory.Remember(api.RememberRequest{
			Content: strPtr(item.content), AnchorText: strPtr(item.content),
			Assertion: map[string]any{
				"s": map[string]any{"label": "老张"}, "x": "养",
				"o": map[string]any{"label": item.object},
			},
			Session: item.session, Author: "user", AutoSync: boolPtr(false),
		}); err != nil {
			t.Fatalf("remember: %v", err)
		}
	}
	session := "s1"
	if result, err := memory.Recall(api.RecallRequest{Cue: "老张", Session: &session, IncludeRaw: boolPtr(false)}); err == nil || result != nil {
		t.Fatalf("graph facade must reject unsupported session filtering: %#v, %v", result, err)
	}
	result, err := recall.Recall(memory.Store, memory.Config, recall.Options{
		Cue: "老张", Scope: "mem:test", Reader: "deliverable", Session: &session, IncludeRaw: boolPtr(false),
	})
	if err != nil {
		t.Fatalf("graph candidate recall: %v", err)
	}
	if result.Meta["session"] != "s1" {
		t.Fatalf("session meta = %#v", result.Meta["session"])
	}
	rendered := result.Render(8000)
	if !strings.Contains(rendered, "猫") || strings.Contains(rendered, "狗") {
		t.Fatalf("session filter leaked:\n%s", rendered)
	}
}

func TestCorruptSpanIsExcludedAndCounted(t *testing.T) {
	memory := newMemory(t)
	writeFact(t, memory, "老张养了一只猫。", "老张", "养", map[string]any{"label": "猫"}, nil)
	if _, err := memory.Store.DB().Exec(
		"UPDATE assertion SET anchor_end = anchor_end + 999"); err != nil {
		t.Fatalf("corrupt: %v", err)
	}
	result := recallCue(t, memory, "老张 猫", false)
	if result.Gaps["bad_anchor"] < 1 {
		t.Fatalf("gaps = %#v", result.Gaps)
	}
	if result.Meta["anchors_checked"].(int) != 0 || result.Meta["anchor_failed"].(int) != 0 {
		t.Fatalf("meta = %#v", result.Meta)
	}
}

func TestDeliveryAnchorAnnotatesInsteadOfDropping(t *testing.T) {
	// A node that reaches delivery with an impossible span is annotated, not
	// silently dropped; rendering surfaces the anomaly.
	result := &recall.Result{
		Scope: "mem:test",
		Bands: []map[string]any{{
			"title": "带·老张", "time_label": "2026-10-27", "space_label": "空间未知",
			"chains": []any{},
			"nodes": []map[string]any{{
				"id": "a_bad", "content": "老张 —养→ 猫", "time": "2026-10-27",
				"anchor": "trace:bad", "confidence": 0.5, "status": "active",
				"anchor_ok": false,
			}},
		}},
		Gaps: map[string]int{},
		Meta: map[string]any{"elapsed_ms": 0, "truncated": map[string]any{"chars": false}},
	}
	rendered := result.Render(8000)
	if !strings.Contains(rendered, "锚异常") {
		t.Fatalf("render missing the anchor anomaly marker:\n%s", rendered)
	}
}

func TestOffsetPagination(t *testing.T) {
	memory := newMemory(t)
	seedGraph(t, memory)
	first := recallCue(t, memory, "我 小白", false)
	second, err := memory.Recall(api.RecallRequest{
		Cue: "我 小白", IncludeRaw: boolPtr(false), Offset: 1,
	})
	if err != nil {
		t.Fatalf("recall offset: %v", err)
	}
	if len(second.Bands) == 0 || len(first.Bands) < 2 {
		t.Fatalf("bands = %d / %d", len(first.Bands), len(second.Bands))
	}
	if second.Bands[0]["id"] == first.Bands[0]["id"] {
		t.Fatalf("offset did not advance: %#v", second.Bands[0]["id"])
	}
}

// TestAsOfHidesLaterAssertions verifies temporal as-of filtering on candidate assertions.
func TestAsOfHidesLaterAssertions(t *testing.T) {
	memory := newMemory(t)
	writeFact(t, memory, "我搬到北京。", "我", "住在", map[string]any{"label": "北京"},
		map[string]any{"t_valid_from": "2026-06-01"})
	if result, err := memory.Recall(api.RecallRequest{Cue: "北京", AsOf: "2026-03-01", IncludeRaw: boolPtr(false)}); err == nil || result != nil {
		t.Fatalf("graph facade must reject unsupported asof filtering: %#v, %v", result, err)
	}
	before, err := recall.Recall(memory.Store, memory.Config, recall.Options{
		Cue: "北京", Scope: "mem:test", Reader: "deliverable", AsOf: ids.ParseTime("2026-03-01", ids.ParseTimeOptions{}), IncludeRaw: boolPtr(false),
	})
	if err != nil {
		t.Fatalf("graph candidate recall before: %v", err)
	}
	if !before.Absent {
		t.Fatalf("asof before = %#v", before.ToDict())
	}
	after, err := recall.Recall(memory.Store, memory.Config, recall.Options{
		Cue: "北京", Scope: "mem:test", Reader: "deliverable", AsOf: ids.ParseTime("2027-01-01", ids.ParseTimeOptions{}), IncludeRaw: boolPtr(false),
	})
	if err != nil {
		t.Fatalf("graph candidate recall after: %v", err)
	}
	if after.Absent {
		t.Fatalf("asof after = %#v", after.ToDict())
	}
}

// TestSameAsClosureExpandsSeeds verifies seed expansion across active same-as edges.
func TestSameAsClosureExpandsSeeds(t *testing.T) {
	memory := newMemory(t)
	left, err := memory.Store.EnsureNode("mem:test", "referent", "老张", store.NodeOptions{})
	if err != nil {
		t.Fatalf("ensure left: %v", err)
	}
	right, err := memory.Store.EnsureNode("mem:test", "referent", "张先生", store.NodeOptions{})
	if err != nil {
		t.Fatalf("ensure right: %v", err)
	}
	// A same-as edge inserted directly mirrors the row a read-side merge leaves.
	if _, err := memory.Store.DB().Exec(
		`INSERT INTO meta_edge(scope, meta_kind, lhs_id, rhs_id, status, evidence_anchor, created_at, created_by)
		 VALUES ('mem:test', 'same-as', ?, ?, 'active', '', 0, 'test')`, left, right); err != nil {
		t.Fatalf("insert same-as: %v", err)
	}
	writeFact(t, memory, "老张养了一只猫。", "老张", "养", map[string]any{"label": "猫"}, nil)
	result := recallCue(t, memory, "张先生", false)
	if result.Absent {
		t.Fatalf("closure recall = %#v", result.ToDict())
	}
	if !strings.Contains(result.Render(8000), "猫") {
		t.Fatalf("closure render missing the expanded seed:\n%s", result.Render(8000))
	}
}

// TestBareAssertionIsNominatedButNotDelivered verifies that bare assertions are excluded from delivery and counted in gaps.
func TestBareAssertionIsNominatedButNotDelivered(t *testing.T) {
	memory := newMemory(t)
	writeFact(t, memory, "老张养了一只猫。", "老张", "养", map[string]any{"label": "猫"},
		map[string]any{"bare": true})
	result := recallCue(t, memory, "老张 猫", false)
	if !result.Absent {
		t.Fatalf("bare recall = %#v", result.ToDict())
	}
	if result.Gaps["bare_excluded"] != 1 {
		t.Fatalf("gaps = %#v", result.Gaps)
	}
}

// TestConflictsSurfaceOnTheReadSide verifies that conflicting assertions surface in recall gaps and check output.
func TestConflictsSurfaceOnTheReadSide(t *testing.T) {
	memory := newMemory(t)
	writeFact(t, memory, "老张养了一只猫。", "老张", "养", map[string]any{"label": "猫"}, nil)
	second := writeFact(t, memory, "老张养了一只狗。", "老张", "养", map[string]any{"label": "狗"}, nil)
	if second["conflicts"] != 1 {
		t.Fatalf("conflicts = %#v", second["conflicts"])
	}
	result := recallCue(t, memory, "老张 养", false)
	if result.Gaps["conflicts"] != 1 {
		t.Fatalf("gaps = %#v", result.Gaps)
	}
	rendered := result.Render(8000)
	if !strings.Contains(rendered, "猫") || !strings.Contains(rendered, "狗") {
		t.Fatalf("conflict render hides one side:\n%s", rendered)
	}
	report, err := memory.Check()
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if report["ok"] != true || report["conflicts_proposed"] != int64(1) {
		t.Fatalf("check = %#v", report)
	}
}

// TestPrivateAssertionIsNotDelivered verifies audience filtering between deliverable and owner readers.
func TestPrivateAssertionIsNotDelivered(t *testing.T) {
	memory := newMemory(t)
	receipt, err := memory.Remember(api.RememberRequest{
		Content:    strPtr("绝密信息：老张的暗号是蓝鸟。"),
		AnchorText: strPtr("老张的暗号是蓝鸟"),
		Assertion: map[string]any{
			"s": map[string]any{"label": "老张"}, "x": "暗号",
			"o": map[string]any{"literal": "蓝鸟"}, "warrant": "experiential",
		},
		Audience: "private", Author: "user", AutoSync: boolPtr(false),
	})
	if err != nil {
		t.Fatalf("remember: %v", err)
	}
	if len(receipt["written"].([]any)) != 1 {
		t.Fatalf("receipt = %#v", receipt)
	}
	if !recallCue(t, memory, "绝密 暗号", false).Absent {
		t.Fatal("deliverable reader must not see a private assertion")
	}
	owner, err := memory.Recall(api.RecallRequest{
		Cue: "绝密 暗号", Reader: strPtr("owner"), IncludeRaw: boolPtr(false),
	})
	if err != nil {
		t.Fatalf("owner recall: %v", err)
	}
	if owner.Absent || !strings.Contains(owner.Render(8000), "蓝鸟") {
		t.Fatalf("owner recall = %#v", owner.ToDict())
	}
}

// TestMonthOnlyDateWindowSupplementsRaw verifies month-only date window derivation and raw-hit supplementation.
func TestMonthOnlyDateWindowSupplementsRaw(t *testing.T) {
	memory := newMemory(t)
	occurred := int64(1689292800) // 2023-07-14
	if _, _, err := memory.Store.AddTrace("mem:test",
		"zebra migration observation in the field notes.",
		store.TraceOptions{Session: "month-window", Author: "user", OccurredAt: &occurred}); err != nil {
		t.Fatalf("trace: %v", err)
	}
	result, err := memory.Recall(api.RecallRequest{
		Cue: "zebra migration in July", IncludeRaw: boolPtr(false), RawLimit: 5,
	})
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if len(result.RawHits) == 0 {
		t.Fatal("expected the month-boosted raw hit")
	}
	window := result.Meta["date_window"].(map[string]any)
	if window["label"] != "month:07" || window["derived"] != true {
		t.Fatalf("date_window = %#v", window)
	}
}

// TestImplausibleYearWindowIsRejected verifies that out-of-corpus year windows are rejected.
func TestImplausibleYearWindowIsRejected(t *testing.T) {
	memory := newMemory(t)
	occurred := int64(1689292800) // 2023-07-14
	if _, _, err := memory.Store.AddTrace("mem:test",
		"zebra migration observation in the field notes.",
		store.TraceOptions{Session: "year-window", Author: "user", OccurredAt: &occurred}); err != nil {
		t.Fatalf("trace: %v", err)
	}
	result, err := memory.Recall(api.RecallRequest{
		Cue: "Cyberpunk 2077 zebra migration", IncludeRaw: boolPtr(false),
	})
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	window := result.Meta["date_window"].(map[string]any)
	if window["rejected"] != "outside_corpus" {
		t.Fatalf("date_window = %#v", window)
	}
}

// TestExplicitRawSupplementSurvivesDenseGraph verifies explicit raw-hit supplementation alongside dense graph results.
func TestExplicitRawSupplementSurvivesDenseGraph(t *testing.T) {
	memory := newMemory(t)
	writeFact(t, memory, "老张养了一只猫。", "老张", "养", map[string]any{"label": "猫"}, nil)
	writeFact(t, memory, "老张喜欢咖啡。", "老张", "喜欢", map[string]any{"label": "咖啡"}, nil)
	if _, _, err := memory.Store.AddTrace("mem:test", "unique zebra observation in the field notes.",
		store.TraceOptions{Session: "raw", Author: "user"}); err != nil {
		t.Fatalf("trace: %v", err)
	}
	result, err := memory.Recall(api.RecallRequest{
		Cue: "unique zebra", IncludeRaw: boolPtr(true), RawLimit: 3,
	})
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if len(result.RawHits) == 0 {
		t.Fatal("expected the explicit raw supplement")
	}
	truncated := result.Meta["truncated"].(map[string]any)
	if truncated["chars"] != false {
		t.Fatalf("truncated = %#v", truncated)
	}
}

// TestSessionFilterLimitsRawHits verifies session-scoped raw-hit filtering.
func TestSessionFilterLimitsRawHits(t *testing.T) {
	memory := newMemory(t)
	for _, item := range []struct{ session, text string }{
		{"s1", "unique zebra in session one."},
		{"s2", "unique zebra in session two."},
	} {
		if _, _, err := memory.Store.AddTrace("mem:test", item.text,
			store.TraceOptions{Session: item.session, Author: "user"}); err != nil {
			t.Fatalf("trace: %v", err)
		}
	}
	session := "s1"
	if result, err := memory.Recall(api.RecallRequest{Cue: "unique zebra", Session: &session, IncludeRaw: boolPtr(true), RawLimit: 5}); err == nil || result != nil {
		t.Fatalf("graph facade must reject unsupported session filtering: %#v, %v", result, err)
	}
	result, err := recall.Recall(memory.Store, memory.Config, recall.Options{
		Cue: "unique zebra", Scope: "mem:test", Reader: "deliverable", Session: &session, IncludeRaw: boolPtr(true), RawLimit: 5,
	})
	if err != nil {
		t.Fatalf("graph raw recall: %v", err)
	}
	if len(result.RawHits) == 0 {
		t.Fatal("expected raw hits")
	}
	for _, hit := range result.RawHits {
		if hit["session"] != "s1" {
			t.Fatalf("raw hit leaked another session: %#v", hit)
		}
	}
	if result.Meta["session"] != "s1" {
		t.Fatalf("session meta = %#v", result.Meta["session"])
	}
}

// TestDoseReportMatchesDeliveredNodes verifies that dose accounting matches the set of delivered node IDs.
func TestDoseReportMatchesDeliveredNodes(t *testing.T) {
	memory := newMemory(t)
	writeFact(t, memory, "老张养了一只猫。", "老张", "养", map[string]any{"label": "猫"}, nil)
	writeFact(t, memory, "老张住在北京。", "老张", "住在", map[string]any{"label": "北京"}, nil)
	result := recallCue(t, memory, "老张", false)
	delivered := map[string]struct{}{}
	for _, band := range result.Bands {
		nodes, _ := band["nodes"].([]map[string]any)
		for _, node := range nodes {
			delivered[node["id"].(string)] = struct{}{}
		}
	}
	if len(delivered) == 0 {
		t.Fatal("expected delivered nodes")
	}
	if result.Meta["anchors_checked"] != len(delivered) {
		t.Fatalf("anchors_checked = %v, delivered = %d", result.Meta["anchors_checked"], len(delivered))
	}
	if result.Meta["anchor_failed"] != 0 {
		t.Fatalf("anchor_failed = %#v", result.Meta["anchor_failed"])
	}
}

func TestResultJSONShape(t *testing.T) {
	memory := newMemory(t)
	seedGraph(t, memory)
	result := recallCue(t, memory, "我", false)
	raw, err := json.Marshal(result.ToDict())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	decoded := map[string]any{}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"scope", "bands", "raw_hits", "absent", "gaps", "meta"} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("result missing %q", key)
		}
	}
	gaps := decoded["gaps"].(map[string]any)
	for _, value := range gaps {
		if value == float64(0) {
			t.Fatalf("zero gap leaked: %#v", gaps)
		}
	}
}

func TestGraphLexicalSynergyAndChainRowCapSelection(t *testing.T) {
	memory := newMemory(t)
	writeFact(t, memory, "Caroline moved from Sweden 4 years ago.", "Caroline", "states", map[string]any{"literal": "moved from Sweden"}, map[string]any{"occurred_at": int64(1700000100)})
	for i := 0; i < 20; i++ {
		num := string(rune('a' + i))
		text := "Caroline had a casual chat " + num + " today."
		writeFact(t, memory, text, "Caroline", "states", map[string]any{"literal": "casual chat " + num}, map[string]any{"occurred_at": int64(1700001000 + i)})
	}
	res, err := memory.Recall(api.RecallRequest{
		Cue:       "Where did Caroline move from?",
		CapsBands: intPtr(4),
		CapsNodes: intPtr(30),
	})
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	rendered := res.Render(8000)
	if !strings.Contains(rendered, "Sweden") {
		t.Fatalf("expected early high-relevance chain row 'moved from Sweden' to survive chain row cap, got:\n%s", rendered)
	}
}

func TestEffectiveShannonBitsAdversarialGate(t *testing.T) {
	// Adversarial V1: Pure punctuation / whitespace must score 0.0 bits (never reaching 5.6b or 8.0b).
	for _, noise := range []string{"", "        ", "--------", "........", "?!?!?!?!?!?!"} {
		if got := recall.EffectiveShannonBits(noise); got != 0 {
			t.Fatalf("EffectiveShannonBits(%q) = %.2f, want 0", noise, got)
		}
	}
	// Adversarial V2 & Second-Order Affix-Padded Periodic Floods:
	// Both period-1 ("aaaaaaaaaa"), period-2 ("abababab..."), period-4 ("a1b2a1b2..."),
	// Han stutter ("哈哈哈哈"), and affix-padded bypasses ("xababababababababab", "哈哈哈哈哈哈嘿")
	// must decay below 0.70 * AtomicConceptEntropyBits (5.6b).
	for _, flood := range []string{
		"aaaaaaaaaa",
		"aAaAaAaAaA",
		"1aaaaaaaaaa2",
		"abababababababababababababababababab",
		"abababababababababababababababababababababababababababababababababababababababab",
		"xababababababababab",
		"a1b2a1b2a1b2a1b2a1b2",
		"哈哈哈哈",
		"哈哈哈哈哈哈嘿",
		"a\u200ba\u200ba\u200ba\u200ba\u200ba\u200ba\u200ba\u200ba\u200ba\u200b",
		"a" + strings.Repeat("\u0301", 30),
	} {
		if got := recall.EffectiveShannonBits(flood); got >= 0.70*8.0 {
			t.Fatalf("EffectiveShannonBits(%q) = %.2f, want < 5.60", flood, got)
		}
	}
	if got := recall.EffectiveShannonBits("banana"); got != 12.0 {
		t.Fatalf("EffectiveShannonBits(\"banana\") = %.2f, want 12.0", got)
	}
	// Valid concepts ("mutex", "SQL", "记忆图谱") preserve their full information mass.
	if got := recall.EffectiveShannonBits("mutex"); got != 10.0 {
		t.Fatalf("EffectiveShannonBits(\"mutex\") = %.2f, want 10.0", got)
	}
	if got := recall.EffectiveShannonBits("SQL"); got != 6.0 {
		t.Fatalf("EffectiveShannonBits(\"SQL\") = %.2f, want 6.0", got)
	}
	if got := recall.EffectiveShannonBits("记忆图谱"); got != 40.0 {
		t.Fatalf("EffectiveShannonBits(\"记忆图谱\") = %.2f, want 40.0", got)
	}
	cands := recall.PreindexSubjectCandidates([]recall.SubjectCandidate{
		{Label: "数据库配置", Body: "SQLite WAL mutex"},
	})
	if hits := recall.ProbeSubjectsBySpecificity(cands, "--------", "", 0, 4); len(hits) != 0 {
		t.Fatalf("expected pure punctuation prompt to awaken 0 subjects, got %v", hits)
	}
	if hits := recall.ProbeSubjectsBySpecificity(cands, "aaaaaaaaaa", "", 0, 4); len(hits) != 0 {
		t.Fatalf("expected stutter prompt to awaken 0 subjects, got %v", hits)
	}
}

func TestUserScopeRecallDoesNotLeakInternalTraceTextBloat(t *testing.T) {
	mem := newMemory(t)
	writeFact(t, mem, "老张配置了 1024 维向量索引。"+strings.Repeat("背景长段落内容", 200),
		"老张", "配置了", map[string]any{"literal": "1024 维向量索引"}, nil)
	res := recallCue(t, mem, "老张", false)
	if len(res.Bands) == 0 {
		t.Fatalf("expected non-empty bands")
	}
	nodes, _ := res.Bands[0]["nodes"].([]map[string]any)
	if len(nodes) == 0 {
		t.Fatalf("expected non-empty nodes in band[0]")
	}
	if _, leaked := nodes[0]["_trace_text"]; leaked {
		t.Fatalf("user scope leaked internal _trace_text bloat into delivered band node")
	}
}
