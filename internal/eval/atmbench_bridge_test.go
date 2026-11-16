package eval

import (
	"testing"
	"time"
)

func testATMBridgeItem(id, text string, ts int64) *ATMSGMItem {
	return &ATMSGMItem{ItemID: id, FullText: text, OccurredAt: ts, CorpusIdx: 0}
}

// TestATMSessionIDCoarsensToEpisode 锁定「一条记忆 = 一个 session」不会回归。
// 远端网关用 locator 前缀作 episode 键（ExtractSessionKeyFromLocator），
// 所以 session 粒度直接等于记忆结构里的事件粒度：同一天同一地点的多条记忆必须同键。
func TestATMSessionIDCoarsensToEpisode(t *testing.T) {
	day1 := time.Date(2024, 10, 1, 17, 10, 53, 0, time.UTC)
	day2 := time.Date(2024, 10, 2, 9, 0, 0, 0, time.UTC)
	mk := func(id, modality, loc, city string, ts time.Time) *ATMSGMItem {
		return &ATMSGMItem{ItemID: id, Modality: modality, Location: loc, City: city, OccurredAt: ts.Unix()}
	}

	milanoA := mk("20241001_171053", "image", "Allianz MiCo, Viale Eginardo, Milan", "Milan, Italy", day1)
	milanoB := mk("20241001_181259", "image", "Allianz MiCo, Viale Eginardo, Milan", "Milan, Italy", day1.Add(62*time.Minute))
	milanoNextDay := mk("20241002_120000", "image", "Allianz MiCo, Viale Eginardo, Milan", "Milan, Italy", day2)
	otherCity := mk("20241001_200000", "image", "Via Roma, Turin", "Turin, Italy", day1)
	mailA := mk("email202410010001", "email", "", "", day1)
	mailB := mk("email202410010002", "email", "", "", day1.Add(2*time.Hour))
	mailNextDay := mk("email202410020001", "email", "", "", day2)

	if got, want := atmSessionID(milanoA), atmSessionID(milanoB); got != want {
		t.Fatalf("same day + same place must share a session: %q vs %q", got, want)
	}
	if atmSessionID(milanoA) == atmSessionID(milanoNextDay) {
		t.Fatal("different calendar day must start a new session")
	}
	if atmSessionID(milanoA) == atmSessionID(otherCity) {
		t.Fatal("different place must start a new session")
	}
	if got, want := atmSessionID(mailA), atmSessionID(mailB); got != want {
		t.Fatalf("same-day mail must share a session: %q vs %q", got, want)
	}
	if atmSessionID(mailA) == atmSessionID(mailNextDay) {
		t.Fatal("different-day mail must start a new session")
	}
	if atmSessionID(mailA) == atmSessionID(milanoA) {
		t.Fatal("mail and media must not share a session namespace")
	}
	if atmSessionID(milanoA) != atmSessionID(milanoA) {
		t.Fatal("session id must be deterministic")
	}
	if atmSessionID(mk("20241001_000001", "image", "", "", day1)) == atmSessionID(milanoA) {
		t.Fatal("location-less media must not join a placed session")
	}
}

func TestRecallATMQuestionCloudItemUnitRanking(t *testing.T) {
	server := newCloudTestServer(t)
	engine := newTestCloudEngine(t, server, SuiteATM)
	items := []*ATMSGMItem{
		testATMBridgeItem("20240715_125140", "ID: 20240715_125140\nCaption: British Longhair feline resting on sofa", 1721047900),
		testATMBridgeItem("email202304120007", "ID: email202304120007\nSubject: Vet consultation for Grace (British Longhair)", 1681300000),
	}
	if _, _, err := IngestATMCorpus(engine, items, false, false, nil); err != nil {
		t.Fatalf("IngestATMCorpus: %v", err)
	}
	byID := map[string]*ATMSGMItem{items[0].ItemID: items[0], items[1].ItemID: items[1]}
	server.queryHitIDs = []string{items[1].ItemID, items[0].ItemID}
	ids, hits, err := RecallATMQuestion(engine, byID, "Find photos of my pet Grace", 10)
	if err != nil {
		t.Fatalf("RecallATMQuestion: %v", err)
	}
	if len(ids) != 2 || ids[0] != items[1].ItemID || len(hits) != 2 {
		t.Fatalf("cloud ranking was not preserved: ids=%v hits=%v", ids, hits)
	}
	server.queryHitIDs = []string{"foreign-unit"}
	if _, _, err := RecallATMQuestion(engine, byID, "Find photos of my pet Grace", 10); err == nil {
		t.Fatal("foreign cloud unit must fail closed instead of entering ATM scoring")
	}
}

// TestApplyATMUnscoredAccounting 答不上 ≠ 答错：失败题移出 qs 分母，同时必须给出全量分母读数。
func TestApplyATMUnscoredAccounting(t *testing.T) {
	results := []map[string]any{
		{"id": "q1", "qs_scored": true, "qs_score": 1.0},
		{"id": "q2", "qs_scored": true, "qs_score": 0.0},
		{"id": "q3", "qs_scored": false, "qs_score": 0.0, "unscored_reason": "ANSWER_UNAVAILABLE: 模型回执正文为空"},
		{"id": "q4", "qs_scored": true, "qs_score": 1.0},
	}
	summary := map[string]any{}
	applyATMUnscoredAccounting(summary, results, 2.0, 3)
	if summary["scored_count"] != 3 || summary["unscored_count"] != 1 {
		t.Fatalf("scored/unscored = %v/%v, want 3/1", summary["scored_count"], summary["unscored_count"])
	}
	if got := summary["qs_score_scored_only"].(float64); got != PyRound(2.0/3.0, 4) {
		t.Fatalf("qs_score_scored_only = %v", got)
	}
	if got := summary["qs_score_all_denominator"].(float64); got != PyRound(2.0/4.0, 4) {
		t.Fatalf("qs_score_all_denominator = %v", got)
	}
	// 全量不可用（全题 unscored）时不得产出 scored 分母读数，但全量分母仍须为 0。
	empty := map[string]any{}
	applyATMUnscoredAccounting(empty, results[:1], 0, 0)
	if _, ok := empty["qs_score_scored_only"]; ok {
		t.Fatal("zero scored rows must not report a scored-only QS")
	}
	if got := empty["qs_score_all_denominator"].(float64); got != 0 {
		t.Fatalf("qs_score_all_denominator = %v", got)
	}
}
