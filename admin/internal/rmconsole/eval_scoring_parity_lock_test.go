// plugin: rolling-memory · 判分对等回归锁
//
// 目的：把「面板判分 = CLI 官方语义」锁进机器回归——任何把 list_recall 二值化进 QS、
// number 多重集弱校验、判词宽松放行、f1 换成 Jaccard、取整换回 math.Round 的提交，
// 在本包测试当场变红。
//
// 覆盖维度：F1 词元口径（冠词/连续音节文字切分）、ATM 判词严格闭集、
// judge_unparsed/judge_error 分列、逐行四位取整后再聚合，全部与 CLI 逐条同尺。
package rollingmemory

import "testing"

func TestATMPanelCLIScoringParityAuditLock(t *testing.T) {
	// 1) number 题型多重集：多报额外数字必须拒绝（对齐 CLI internal/eval/atmbench.go::ComputeATMNumberEM）
	numberVectors := []struct {
		gt, pred string
		want     bool
	}{
		{"6 nights", "6 nights in 2024", true}, // 官方 RE_NUMBER 不把裸 4 位年份当作数字
		{"6 nights in 2024", "You stayed 6 nights during 2024.", true},
		{"6 nights", "4 nights", false},
		{"Unknown", "No evidnece to determine", true},
		{"Paris", "She visited Paris in May.", true},
		{"Paris", "She visited Lyon in May.", false},
	}
	for _, c := range numberVectors {
		if ok, _ := computeATMNumberEMAdmin(c.gt, c.pred); ok != c.want {
			t.Errorf("number-EM(%q, %q) = %v, want %v", c.gt, c.pred, ok, c.want)
		}
	}

	// 2) ATM 聚合必须走连续混合 QS（list_recall 连续 Jaccard）；0.5 二值只服务五桶归因。
	items := []EvalQuestionResultItem{
		{Category: "1", JudgeVerdict: "correct", QSScore: 0.667, QSScored: true},
		{Category: "1", JudgeVerdict: "wrong", QSScore: 0.25, QSScored: true},
		{Category: "3", JudgeVerdict: "correct", QSScore: 1.0, QSScored: true},
	}
	_, _, _, rate, _, _, _, _, _, _, _ := recomputeLiveAggregates(items, SuiteATM)
	if rate != 0.639 {
		t.Fatalf("ATM 连续 QS 聚合 = %v, want 0.639（二值化回归？）", rate)
	}

	// 3) LoCoMo 保持判对率语义（无 QS 字段时回落二值计数）。
	locomo := []EvalQuestionResultItem{{JudgeVerdict: "correct"}, {JudgeVerdict: "wrong"}}
	_, _, _, locomoRate, _, _, _, _, _, _, _ := recomputeLiveAggregates(locomo, SuiteLoCoMo)
	if locomoRate != 0.5 {
		t.Fatalf("LoCoMo 判对率 = %v, want 0.5", locomoRate)
	}

	// 4) 冻结判词解析严格闭集（对齐 CLI internal/eval/frozen.go）：
	//    非 JSON 子串、小写 label 一律 JUDGE_UNPARSED；合法闭集与「围栏内含合法 JSON」照常解析。
	if v, _, err := parseFrozenJudgeOutput("The answer is CORRECT."); err == nil || v != "unparsed" {
		t.Fatalf("frozen judge 放行了非 JSON 子串: v=%q err=%v", v, err)
	}
	if v, _, err := parseFrozenJudgeOutput(`{"reasoning":"r","label":"correct"}`); err == nil || v != "unparsed" {
		t.Fatalf("frozen judge 放行了小写 label: v=%q err=%v", v, err)
	}
	if v, _, err := parseFrozenJudgeOutput(`{"reasoning":"r","label":"CORRECT"}`); err != nil || v != "correct" {
		t.Fatalf("frozen judge 拒收了合法闭集: v=%q err=%v", v, err)
	}
	if v, _, err := parseFrozenJudgeOutput("```json\n{\"reasoning\":\"r\",\"label\":\"WRONG\"}\n```"); err != nil || v != "wrong" {
		t.Fatalf("frozen judge 拒收了围栏内合法 JSON: v=%q err=%v", v, err)
	}

	// 5) ATM 混合 QS 判官 = 官方 parse_judge_response（JSON 优先 + 宽松回退；无 unparsed）。
	atmJudgeVectors := []struct {
		raw  string
		want string
	}{
		{`{"explanation":"ok","accuracy":"true"}`, "correct"},
		{`{"explanation":"ok","accuracy":true}`, "correct"},
		{`{"explanation":"ok","accuracy":"TRUE"}`, "correct"},
		{"```json\n{\"explanation\":\"x\",\"accuracy\":\"false\"}\n```", "wrong"},
		{`{"explanation":"no","accuracy":false}`, "wrong"},
		{`{"explanation":"x","accuracy":"maybe"}`, "wrong"},
		{"accuracy: true please", "correct"},
		{`{"explanation":"oops","accuracy": true`, "correct"},
		{"", "wrong"},
		{"The answer is correct.", "correct"},
	}
	for _, c := range atmJudgeVectors {
		got, _, err := parseATMJudgeOutputAdmin(c.raw)
		if err != nil {
			t.Fatalf("ATM judge(%q) must not return an error under official parser: %v", c.raw, err)
		}
		if got != c.want {
			t.Fatalf("ATM judge(%q) = %q, want %q", c.raw, got, c.want)
		}
	}

	// 6) F1 词元口径 = CLI internal/eval/scoring.go::TokenF1（字符串字面量为 CLI 实测读数）：
	//    冠词丢失、连续音节文字按单字切分、标点丢弃、多重集 F1。
	f1Vectors := []struct {
		gold, pred string
		wantF1     float64
		wantExact  bool
	}{
		{"6 nights", "6 nights in 2024", 0.6667, false},
		{"6 nights in 2024", "You stayed 6 nights during 2024.", 0.6, false},
		{"a red apple", "red apple", 1.0, true}, // 旧 extractAlphaNumTokens 会保留冠词 ⇒ 0.8
		{"the cat sat", "cat sat on the mat", 0.6667, false},
		{"すし屋", "すし", 0.8, false}, // 早期实现视かな为整词 ⇒ 0.6667
		{"東京タワーの高さ", "東京タワーの高さは333m", 0.8889, false},
		{"2 apples and 3 pears", "3 pears, 2 apples", 0.8889, false},
		{"", "", 0, false},
		{"Paris", "", 0, false},
		{"Unknown", "No evidnece to determine", 0, false},
	}
	for _, c := range f1Vectors {
		gotF1, gotExact := computeTokenF1(c.pred, c.gold)
		if gotF1 != c.wantF1 || gotExact != c.wantExact {
			t.Errorf("TokenF1(gold=%q, pred=%q) = (%v,%v), want (%v,%v)", c.gold, c.pred, gotF1, gotExact, c.wantF1, c.wantExact)
		}
	}
	// LoCoMo 的 exact 无 ATM 那条非空前置（CLI mergeLoCoMoGrade 口径）：双方空串亦判 exact。
	if exact := normalizeAnswerAdmin("") == normalizeAnswerAdmin(""); !exact {
		t.Fatalf("LoCoMo exact 口径（空串全等）未对齐 CLI")
	}

	// 7) 取整 = CLI internal/eval/aggregate.go::PyRound（十进制字符串格式化）。0.00035 处
	//    math.Round(v*1e4)/1e4 给出 0.0004 而 PyRound 给出 0.0003（200k 值扫描共 12 处分歧，
	//    全部落在 f1/qs 同量级），任一处即破「四位全等」判据。
	roundVectors := []struct {
		val    float64
		digits int
		want   float64
	}{
		{0.12345, 4, 0.1235},
		{0.12335, 4, 0.1234},
		{0.00035, 4, 0.0003}, // math.Round 路径 = 0.0004 ⇒ 判别性向量
		{0.045, 2, 0.04},     // math.Round 路径 = 0.05
		{0.6825, 4, 0.6825},
		{2.675, 2, 2.67},
		{2.5, 0, 2}, // math.Round 路径 = 3（半数不进位）
		{0.00005, 4, 0.0001},
		{1.00005, 4, 1.0001},
		{0.55, 4, 0.55},
	}
	for _, c := range roundVectors {
		if got := roundFloat(c.val, c.digits); got != c.want {
			t.Errorf("roundFloat(%v, %d) = %v, want %v (PyRound 同尺)", c.val, c.digits, got, c.want)
		}
	}

	// 8) 聚合逐行取整后再求均值（CLI 从 results-*.jsonl 复算一行一值）：
	//    raw 5e-5 与 0.0 两行 ⇒ 逐行取整 = (0.0001+0.0)/2 = 0.0001，直接用 raw 求和则得 0.0。
	tinyItems := []EvalQuestionResultItem{
		{Category: "1", TokenF1: 0.00005, QSScore: 0.00005, QSScored: true, JudgeVerdict: "correct"},
		{Category: "1", TokenF1: 0, JudgeVerdict: "wrong"},
	}
	_, _, f1Avg, _, _, _, _, _, _, _, _ := recomputeLiveAggregates(tinyItems, SuiteATM)
	if f1Avg != 0.0001 {
		t.Fatalf("逐行取整后 f1 均值 = %v, want 0.0001（聚合取整口径回归？）", f1Avg)
	}

	// 9) judge_unparsed 与 judge_error 分列（CLI summary 同名同义），二者均不进 scored 分母。
	mixed := []EvalQuestionResultItem{
		{Category: "1", JudgeVerdict: "correct"},
		{Category: "1", JudgeVerdict: "unparsed"},
		{Category: "1", JudgeVerdict: "error"},
		{Category: "1", JudgeVerdict: "wrong"},
	}
	_, _, _, mixedRate, mixedCorrect, mixedScored, mixedWrong, mixedUnparsed, mixedError, _, _ := recomputeLiveAggregates(mixed, SuiteLoCoMo)
	if mixedCorrect != 1 || mixedWrong != 1 || mixedScored != 2 || mixedUnparsed != 1 || mixedError != 1 {
		t.Fatalf("判词计数分列不符: correct=%d wrong=%d scored=%d unparsed=%d error=%d",
			mixedCorrect, mixedWrong, mixedScored, mixedUnparsed, mixedError)
	}
	if mixedRate != 0.5 {
		t.Fatalf("judge_rate = %v, want 0.5（unparsed/error 必须剔除出分母）", mixedRate)
	}
}
