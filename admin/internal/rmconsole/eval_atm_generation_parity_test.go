// plugin: rolling-memory · ATM 生成面判分对等回归
//
// 目的：把「面板生成路径的 f1 列 = TokenF1（不被 Jaccard 覆写）、ATM 判词严格闭集」
// 钉成行为断言——generate 路径此前把 list_recall 的 TokenF1 覆写成 Jaccard、将 number
// 命中覆写成 1.0，与 CLI 行内 f1 同名异义。桩服务器同时覆盖 answerer 与 judge 两条调用。
package rollingmemory

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newStubChatServer 返回一个 OpenAI 兼容桩：判官提示（含 accuracy 模板）一律回非 JSON 文本，
// 其余（作答）回 answerText。
func newStubChatServer(t *testing.T, answerText string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		userPrompt := ""
		for _, m := range payload.Messages {
			if m.Role == "user" {
				userPrompt = m.Content
			}
		}
		content := answerText
		if strings.Contains(userPrompt, "accuracy") {
			content = "The prediction looks correct to me." // 非 JSON ⇒ 严格闭集必须记 unparsed
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": content}}},
		})
	}))
	t.Cleanup(server.Close)
	return server
}

func TestATMPanelGenerationKeepsTokenF1AndStrictJudge(t *testing.T) {
	gold := "20240501_120000, email0042"
	answer := "Based on your records: 20240501_120000 and EMAIL0042."
	stub := newStubChatServer(t, answer)
	aiCfg := AIModelConfig{
		BaseURL: stub.URL, APIKey: "stub-key",
		AnswerModel: "stub-answer", JudgeModel: "stub-judge",
		MaxTokens: 64, TimeoutSec: 10,
	}
	atmUnits := map[string]atmEvalUnit{"u1": {Text: "20240501_120000, email0042 listed in the inbox."}}

	// 1) list_recall（cat 1）：QS 记 Jaccard=1.0，f1 列必须保持 TokenF1（=0.5455），不得被覆写。
	item, _, err := executeSingleATMQuestionEval(context.Background(), "", t.TempDir(),
		EvalQuestionItem{ID: "q1", Category: "1", Question: "List the ids.", Answer: gold, EvidenceTexts: []string{"u1"}},
		0, EvalArmOracle, 50, true, aiCfg, atmUnits)
	if err != nil {
		t.Fatalf("executeSingleATMQuestionEval(list_recall) failed: %v", err)
	}
	if item.JudgeVerdict != "correct" || item.QSScore != 1.0 || !item.QSScored {
		t.Fatalf("list_recall QS 契约不符: verdict=%q qs=%v scored=%v", item.JudgeVerdict, item.QSScore, item.QSScored)
	}
	wantF1, _ := computeTokenF1(answer, gold)
	if wantF1 != 0.5455 {
		t.Fatalf("前置字面量漂移：computeTokenF1 = %v, want 0.5455", wantF1)
	}
	if item.TokenF1 != wantF1 {
		t.Fatalf("list_recall f1 列 = %v, want TokenF1 %v（被 Jaccard 覆写回归？）", item.TokenF1, wantF1)
	}
	if item.TokenF1 == item.QSScore {
		t.Fatalf("f1 与 qs_score 同值（%v）：Jaccard 覆写 TokenF1 的旧行为回归", item.TokenF1)
	}

	// 2) open_end（cat 3）：官方 parse_judge_response 的宽松回退会解析关键词；
	//    官方没有 JUDGE_UNPARSED 语义，该行正常进入 QS 人口。
	item2, _, err := executeSingleATMQuestionEval(context.Background(), "", t.TempDir(),
		EvalQuestionItem{ID: "q2", Category: "3", Question: "Why?", Answer: "Because it rained.", EvidenceTexts: []string{"u1"}},
		1, EvalArmOracle, 50, true, aiCfg, atmUnits)
	if err != nil {
		t.Fatalf("executeSingleATMQuestionEval(open_end) failed: %v", err)
	}
	if item2.JudgeVerdict != "correct" {
		t.Fatalf("open_end 官方宽松回退判词 = %q, want correct", item2.JudgeVerdict)
	}
	if !item2.QSScored {
		t.Fatalf("官方回退判词必须计入 QS 人口: qs=%v scored=%v", item2.QSScore, item2.QSScored)
	}
	_, _, _, _, _, scored, _, unparsed, _, _, _ := recomputeLiveAggregates([]EvalQuestionResultItem{item, item2}, SuiteATM)
	if scored != 2 || unparsed != 0 {
		t.Fatalf("聚合人口不符: scored=%d unparsed=%d（want 2/0）", scored, unparsed)
	}
}
