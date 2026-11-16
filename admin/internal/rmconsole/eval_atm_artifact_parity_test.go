// plugin: rolling-memory · ATM 归档产物读路径判分对等（实际 JSONL 复算）
//
// 目的：锁住「面板从 CLI 归档 results-*.jsonl 复算的读数 = CLI 自己记录的 summary 口径」。
// 此前 loadATMResultsJSONL 漏映射 `qs_score`/`qs_scored`（⇒ ATM judge_rate 退化为二值
// correct/scored，实数据 0.1613 vs 官方 QS 0.187），且把 `AnswerInContext` 误映射为
// `full_evidence_hit`（实数据 0.871 vs CLI summary 的 0.0323，而列头写的是「上下文含答案率」）。
package rollingmemory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeATMArtifactForTest 在临时 HOME 下按 loadATMResultsJSONL 的既有候选路径写入归档产物。
func writeATMArtifactForTest(t *testing.T, rows []string) string {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, ".local", "state", "rolling-memory", "eval", "out-sync1104-qs-max-hard")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir artifact dir: %v", err)
	}
	artifact := filepath.Join(dir, "results-atm-hard-raw-llm.jsonl")
	if err := os.WriteFile(artifact, []byte(strings.Join(rows, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	return home
}

func TestATMArchivedArtifactRecomputeParity(t *testing.T) {
	rows := []string{
		// list_recall：f1=TokenF1 0.5455 与 qs_score=Jaccard 0.375 必须分别落位；
		// answer_in_context=false 而 full_evidence_hit=true ⇒ AIC 列必须读前者。
		`{"id":"q1","category":"1","sample_id":"list_recall","question":"q","gold":"g","f1":0.5455,` +
			`"exact":false,"judge":"wrong","judge_reason":"ATM official list_recall Jaccard = 0.375",` +
			`"qs_score":0.375,"qs_scored":true,"evidence_hit":true,"answer_in_context":false,` +
			`"full_evidence_hit":true,"answer_token_recall":0.5,"context_chars":100,"bands":1,"nodes":2,"raw_hits":3}`,
		`{"id":"q2","category":"3","sample_id":"open_end","question":"q","gold":"g","f1":0.0,` +
			`"exact":false,"judge":"correct","judge_reason":"ok","qs_score":1.0,"qs_scored":true,` +
			`"evidence_hit":true,"answer_in_context":true,"full_evidence_hit":true,"context_chars":120}`,
		// 判词无法解析：既不进 binary 分母，也不进 QS 人口。
		`{"id":"q3","category":"3","sample_id":"open_end","question":"q","gold":"g","f1":0.0,` +
			`"exact":false,"judge":"unparsed","judge_reason":"JUDGE_UNPARSED","qs_scored":false,` +
			`"evidence_hit":false,"answer_in_context":false,"full_evidence_hit":false}`,
	}
	home := writeATMArtifactForTest(t, rows)
	items := loadATMResultsJSONL(home, "atm-baseline-rm-hard-max")
	if len(items) != 3 {
		t.Fatalf("loadATMResultsJSONL 载入 %d 行，want 3", len(items))
	}

	// 1) 字段逐项落位（同名同义）。
	if items[0].TokenF1 != 0.5455 || items[0].QSScore != 0.375 || !items[0].QSScored {
		t.Fatalf("list_recall 行映射不符: f1=%v qs=%v scored=%v（want 0.5455 / 0.375 / true）",
			items[0].TokenF1, items[0].QSScore, items[0].QSScored)
	}
	if items[0].AnswerInContext {
		t.Fatalf("AIC 必须读 answer_in_context(false)，被误映射为 full_evidence_hit(true)")
	}
	if items[1].AnswerInContext != true || items[2].QSScored {
		t.Fatalf("AIC/QS 人口映射不符: aic1=%v scored3=%v", items[1].AnswerInContext, items[2].QSScored)
	}

	// 2) 聚合读数 = CLI summary 口径：ATM judge_rate 取 QS 均值（(0.375+1.0)/2 = 0.6875），
	//    而非二值 correct/scored（1/2 = 0.5）；unparsed 剔出两者；AIC 率 = 1/3。
	_, aicRate, _, jdgRate, _, _, _, unparsed, _, _, _ := recomputeLiveAggregates(items, SuiteATM)
	if jdgRate != 0.6875 {
		t.Fatalf("ATM judge_rate = %v, want 0.6875（官方 QS 均值；0.5 即二值化退化）", jdgRate)
	}
	if aicRate != 0.3333 {
		t.Fatalf("AIC 率 = %v, want 0.3333（读 answer_in_context 口径）", aicRate)
	}
	if unparsed != 1 {
		t.Fatalf("judge_unparsed = %d, want 1", unparsed)
	}
}
