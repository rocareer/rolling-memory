package eval

import (
	"path/filepath"
	"testing"
)

func TestATMProtocolPinsAndScoring(t *testing.T) {
	if err := VerifyATMProtocol(); err != nil {
		t.Fatalf("VerifyATMProtocol failed: %v", err)
	}

	// 1. list_recall Jaccard scoring (extracting media/email IDs from prose or CSV)
	j1 := ComputeATMListJaccard("20240501_120000, email0042", "Based on your records: 20240501_120000 and EMAIL0042.")
	if j1 != 1.0 {
		t.Fatalf("expected Jaccard 1.0, got %.4f", j1)
	}
	j2 := ComputeATMListJaccard("20240501_120000, email0042", "20240501_120000, email9999")
	if j2 < 0.33 || j2 > 0.34 {
		t.Fatalf("expected Jaccard ~0.3333, got %.4f", j2)
	}

	// 2. number deterministic EM & abstention
	okAbst, _ := ComputeATMNumberEM("Unknown", "Insufficient information to determine")
	if !okAbst {
		t.Fatalf("expected abstention match")
	}
	okNum, _ := ComputeATMNumberEM("6 nights in 2024", "You stayed 6 nights during 2024.")
	if !okNum {
		t.Fatalf("expected numeric multiset match")
	}
	badNum, _ := ComputeATMNumberEM("6 nights", "4 nights")
	if badNum {
		t.Fatalf("expected numeric mismatch")
	}

	// 3. Judge JSON parser
	acc, exp, err := ParseATMJudgeResponse(`{"explanation":"Matches all ground truth cities.","accuracy":"true"}`)
	if err != nil || !acc || exp == "" {
		t.Fatalf("ParseATMJudgeResponse failed: acc=%v exp=%q err=%v", acc, exp, err)
	}
	acc2, _, err2 := ParseATMJudgeResponse(`{"explanation":"Missing dates.","accuracy":false}`)
	if err2 != nil || acc2 {
		t.Fatalf("ParseATMJudgeResponse false failed: acc=%v err=%v", acc2, err2)
	}
}

// TestATMJudgeOfficialFallbackParity 锁住官方 parse_judge_response 语义：
// JSON 载荷优先；JSON 失败或缺少 accuracy 时按官方宽松回退（"accuracy": true/false
// 子串，再按 accurate/correct/matches/true/yes 关键词）。官方没有 JUDGE_UNPARSED。
func TestATMJudgeOfficialFallbackParity(t *testing.T) {
	vectors := []struct {
		raw     string
		wantAcc bool
	}{
		{`{"explanation":"ok","accuracy":"true"}`, true},
		{`{"explanation":"ok","accuracy":true}`, true},
		{`{"explanation":"ok","accuracy":"TRUE"}`, true},
		{"```json\n{\"explanation\":\"x\",\"accuracy\":\"false\"}\n```", false},
		{`{"explanation":"no","accuracy":false}`, false},
		{`{"explanation":"x","accuracy":"maybe"}`, false},
		{"accuracy: true please", true},
		{`{"explanation":"oops","accuracy": true`, true},
		{"", false},
		{"The answer is correct.", true},
	}
	for _, c := range vectors {
		acc, _, err := ParseATMJudgeResponse(c.raw)
		if err != nil {
			t.Fatalf("official judge parser must not return an error for %q: %v", c.raw, err)
		}
		if acc != c.wantAcc {
			t.Fatalf("ParseATMJudgeResponse(%q) = %v, want %v", c.raw, acc, c.wantAcc)
		}
	}
}

// TestTokenF1ParityLiteralsWithPanel 锁住 F1/exact 口径字面量：同一张表在面板侧
// eval_scoring_parity_lock_test.go 逐条同断言（冠词丢弃、连续音节文字按单字切分、多重集 F1）。
func TestTokenF1ParityLiteralsWithPanel(t *testing.T) {
	f1Vectors := []struct {
		gold, pred string
		wantF1     float64
		wantExact  bool
	}{
		{"6 nights", "6 nights in 2024", 0.6667, false},
		{"6 nights in 2024", "You stayed 6 nights during 2024.", 0.6, false},
		{"a red apple", "red apple", 1.0, true},
		{"the cat sat", "cat sat on the mat", 0.6667, false},
		{"すし屋", "すし", 0.8, false},
		{"東京タワーの高さ", "東京タワーの高さは333m", 0.8889, false},
		{"2 apples and 3 pears", "3 pears, 2 apples", 0.8889, false},
		{"", "", 0, false},
		{"Paris", "", 0, false},
		{"Unknown", "No evidnece to determine", 0, false},
	}
	for _, c := range f1Vectors {
		if got := PyRound(TokenF1(c.pred, c.gold), 4); got != c.wantF1 {
			t.Errorf("TokenF1(gold=%q, pred=%q) = %v, want %v", c.gold, c.pred, got, c.wantF1)
		}
		exact := NormalizeAnswer(c.pred) != "" && NormalizeAnswer(c.pred) == NormalizeAnswer(c.gold)
		if exact != c.wantExact {
			t.Errorf("ATM exact(gold=%q, pred=%q) = %v, want %v", c.gold, c.pred, exact, c.wantExact)
		}
	}
	// LoCoMo 口径（mergeLoCoMoGrade）无非空前置：空串与空串判 exact。
	if exact := NormalizeAnswer("") == NormalizeAnswer(""); !exact {
		t.Fatalf("LoCoMo exact 口径（空串全等）被改")
	}
}

// TestAggregateRoundsPerRowLikePanel 锁住聚合口径：CLI 从 results-*.jsonl 复算读的是
// 行内已 PyRound 的值，面板 recomputeLiveAggregates 逐行取整后求均值必须给出同一读数。
func TestAggregateRoundsPerRowLikePanel(t *testing.T) {
	rows := []map[string]any{
		{"category": "1", "f1": PyRound(0.00005, 4), "judge": "correct"},
		{"category": "1", "f1": PyRound(0.0, 4), "judge": "wrong"},
	}
	if got := Aggregate(rows, "atm-sdk-pulse-cipher-llm"); got["f1"] != 0.0001 {
		t.Fatalf("Aggregate f1 = %v, want 0.0001（逐行取整口径）", got["f1"])
	}
}

func TestBundledATMDatasetAndCloudSDKRecall(t *testing.T) {
	atmDir, err := ResolveATMDataDir("")
	if err != nil {
		t.Skipf("bundled ATM dataset not found: %v", err)
	}
	items, byID, byHash, err := LoadATMSGMItems(atmDir, ATMScope, 25)
	if err != nil {
		t.Fatalf("LoadATMSGMItems failed: %v", err)
	}
	if len(items) != 25 || len(byID) != 25 || len(byHash) != 25 {
		t.Fatalf("expected 25 items, got items=%d byID=%d byHash=%d", len(items), len(byID), len(byHash))
	}

	hardQAs, err := LoadATMQAItems(atmDir, TierATMHard, 0, 5)
	if err != nil {
		t.Fatalf("LoadATMQAItems hard failed: %v", err)
	}
	if len(hardQAs) != 5 {
		t.Fatalf("expected 5 hard QAs, got %d", len(hardQAs))
	}

	server := newCloudTestServer(t)
	server.queryHitIDs = []string{items[0].ItemID, items[1].ItemID}
	engine := newTestCloudEngine(t, server, SuiteATM)
	outDir := filepath.Join(t.TempDir(), "out")
	summary, err := Run(engine.cfg, Options{
		Suite: SuiteATM, DataDir: atmDir, Tier: TierATMHard,
		PulseMode: PulseModePulseCipher,
		Limit:     2, CorpusLimit: 30, StorePath: engine.StorePath, OutDir: outDir,
		Answerer: "none", Judge: "off", Workers: 1,
	})
	if err != nil {
		t.Fatalf("Run(SuiteATM) failed: %v", err)
	}
	if summary["suite"] != SuiteATM || summary["n"] != 2 || summary["transport"] != CloudTransport {
		t.Fatalf("unexpected ATM cloud summary: %v", summary)
	}
	// scope = 每次新评测自动分配的独立世代 scope（<user>-mem-eval-atmbench-<世代号>）。
	scope, ok := summary["scope"].(string)
	if !ok {
		t.Fatalf("ATM summary scope = %v", summary["scope"])
	}
	if suite, generation, scopeOK := SplitEvalScope(scope); !scopeOK || suite != SuiteATM || generation == "" {
		t.Fatalf("ATM summary scope 不是本套件的评测世代 scope: %q", scope)
	}
	if summary["local_fallback"] != false {
		t.Fatalf("ATM isolation metadata missing: %v", summary)
	}
	if summary["avg_raw"] != 2.0 {
		t.Fatalf("ATM cloud hits must feed aggregate avg_raw: %v", summary["avg_raw"])
	}
}

func TestATMEvidencePromptPinAndPayload(t *testing.T) {
	if got := Sha1Hex(ATMEvidencePromptTPL); got != PinATMEvidencePromptSHA1 {
		t.Fatalf("ATM answer user template sha1 = %s, want %s", got, PinATMEvidencePromptSHA1)
	}
	user, lines := FormatATMEvidencePayload("Q?", []string{"u1"}, map[string]*ATMSGMItem{
		"u1": &ATMSGMItem{ItemID: "u1", FullText: "hello"},
	}, 10)
	want := "Question: Q?\n\nEvidence:\nEvidence 1:\nhello\n\nProvide the answer based solely on the evidence."
	if user != want {
		t.Fatalf("official answer payload mismatch:\n got %q\nwant %q", user, want)
	}
	if len(lines) != 1 {
		t.Fatalf("formatted evidence lines = %d, want 1", len(lines))
	}
}
