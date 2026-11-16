package rollingmemory

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestATMAnswerDeliveryWindowStaysOfficialTen(t *testing.T) {
	if atmAnswerDeliveryK != 10 {
		t.Fatalf("ATM answer delivery window = %d, want official retrieve_k=10", atmAnswerDeliveryK)
	}
	for _, arm := range []string{EvalArmSDKLocalPlain, EvalArmSDKPulsePlain, EvalArmSDKPulseCipher} {
		if got := atmDeliveryWindow(arm, 50); got != 10 {
			t.Fatalf("arm %s delivery window = %d, want 10", arm, got)
		}
	}
	if got := atmDeliveryWindow(EvalArmOracle, 50); got != 50 {
		t.Fatalf("oracle arm delivery window = %d, want rawLimit=50 (gold ceiling)", got)
	}
}

// TestATMPanelDeliveryLocksOfficialTop10 用 12 条召回桩覆盖真实执行路径：
// 检索深度 12、答题交付 10；第 11 位的金标只能算「检索到未交付」，不得计命中。
func TestATMPanelDeliveryLocksOfficialTop10(t *testing.T) {
	const scope = "u10-mem-eval-atmbench-local-plain-testgen"
	storePath := filepath.Join(t.TempDir(), "eval-atm-local-plain.json")
	writeCloudTestManifest(t, storePath, SuiteATM, scope, func(m map[string]any) {
		m["transport"] = evalLocalPlainTransport
		m["endpoint"] = "local://sqlite-fts5-bm25"
		m["pulse_mode"] = EvalPulseModeLocalPlain
	})

	units := make(map[string]atmEvalUnit, 12)
	rawHits := make([]any, 0, 12)
	for i := 1; i <= 12; i++ {
		id := fmt.Sprintf("u%02d", i)
		units[id] = atmEvalUnit{ID: id, Text: "body-" + id}
		rawHits = append(rawHits, map[string]any{"unit_id": id, "session": id, "trace": id, "text": "body-" + id})
	}
	payload, err := json.Marshal(map[string]any{
		"meta": map[string]any{
			"engine_transport": evalLocalPlainTransport,
			"pulse_mode":       EvalPulseModeLocalPlain,
			"engine_scope":     scope,
			"engine_upstream":  "local://sqlite-fts5-bm25",
			"tenant_id":        "user",
			"user_id":          "10",
			"local_fallback":   false,
			"cloud_hits":       len(rawHits),
		},
		"raw_hits": rawHits,
	})
	if err != nil {
		t.Fatal(err)
	}
	installCloudTestCommand(t, func(context.Context, string, ...string) ([]byte, error) {
		return payload, nil
	})

	run := func(gold string) EvalQuestionResultItem {
		t.Helper()
		item, _, err := executeSingleATMQuestionEval(context.Background(), "fake-bin", storePath,
			EvalQuestionItem{ID: "q-" + gold, Category: "1", Question: "List the ids.", Answer: gold, EvidenceTexts: []string{gold}},
			0, EvalArmSDKLocalPlain, 12, false, AIModelConfig{}, units)
		if err != nil {
			t.Fatalf("executeSingleATMQuestionEval(%s): %v", gold, err)
		}
		return item
	}

	delivered := run("u03")
	if delivered.DeliveredItems != 10 || len(delivered.FormattedItems) != 10 || len(delivered.RawItems) != 10 {
		t.Fatalf("delivery window = items:%d formatted:%d raw:%d, want 10/10/10",
			delivered.DeliveredItems, len(delivered.FormattedItems), len(delivered.RawItems))
	}
	if delivered.RawHits != 12 || len(delivered.RetrievalUnitIDs) != 12 {
		t.Fatalf("retrieval depth = raw:%d ids:%d, want 12/12", delivered.RawHits, len(delivered.RetrievalUnitIDs))
	}
	if !delivered.EvidenceHit || delivered.BestEvidenceRecall != 1.0 {
		t.Fatalf("top-10 gold must hit: evidence_hit=%v recall=%v", delivered.EvidenceHit, delivered.BestEvidenceRecall)
	}

	rank11 := run("u11")
	if len(rank11.FormattedItems) != 10 || strings.Contains(strings.Join(rank11.FormattedItems, "\n"), "body-u11") {
		t.Fatal("rank-11 evidence must not enter the answer payload")
	}
	if rank11.EvidenceHit || rank11.AnswerInContext || rank11.BestEvidenceRecall != 0 {
		t.Fatalf("rank-11 gold must not count as delivered hit: %+v", rank11)
	}
	if len(rank11.EvidenceMatches) != 1 || rank11.EvidenceMatches[0].Hit || rank11.EvidenceMatches[0].TokenRecall != 0.5 {
		t.Fatalf("rank-11 match = %+v, want retrieved-not-delivered (hit=false, token_recall=0.5)", rank11.EvidenceMatches)
	}
	if strings.Contains(rank11.ContextPreview, "body-u11") {
		t.Fatal("rank-11 body leaked into answer context preview")
	}
}
