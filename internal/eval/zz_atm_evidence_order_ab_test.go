// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

// zz_atm_evidence_order_ab_test.go —— ATM 本地明文臂的证据顺序单变量 A/B。
//
// 用法：
//
//	RUN_ATM_EVIDENCE_ORDER_AB=1 \
//	ROLLING_MEMORY_EVAL_BASE_URL=<OpenAI-compatible endpoint> \
//	ROLLING_MEMORY_EVAL_API_KEY=<secret> \
//	  go test ./internal/eval -run TestZZATMEvidenceOrderAB -count=1 -v -timeout 6h
//
// 每题只检索一次；retrieval 保持原 top-10 顺序，chronological 按 OccurredAt 稳定排序。
// 两臂共用同一证据集合、全文、官方 payload、答器与判官配置，不重检索、不按题型分支。
package eval

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rocareer/rolling-memory/internal/config"
	"github.com/rocareer/rolling-memory/internal/thinclient"
	"github.com/rocareer/rolling-memory/internal/tokenzr"
	ramesdk "github.com/rocareer/rolling-memory/sdk/go"
	_ "modernc.org/sqlite"
)

const (
	atmOrderRetrievalArm     = 0
	atmOrderChronologicalArm = 1
	atmOrderCacheVersion     = "atm-evidence-order-cache-v1"
)

var atmOrderArmNames = [2]string{"retrieval", "chronological"}

type atmOrderCacheRecord struct {
	Key  string `json:"key"`
	Text string `json:"text"`
}

// atmOrderTextCache 只保存成功非空原文；内存索引与 JSONL 追加由同一把锁保护。
type atmOrderTextCache struct {
	path   string
	mu     sync.Mutex
	values map[string]string
}

func newATMOrderTextCache(path string) (*atmOrderTextCache, error) {
	cache := &atmOrderTextCache{path: path, values: map[string]string{}}
	handle, err := os.Open(path)
	if os.IsNotExist(err) {
		return cache, nil
	}
	if err != nil {
		return nil, err
	}
	defer handle.Close()
	decoder := json.NewDecoder(handle)
	for {
		var record atmOrderCacheRecord
		if err := decoder.Decode(&record); err == io.EOF {
			break
		} else if err != nil {
			return nil, fmt.Errorf("decode order cache %s: %w", path, err)
		}
		if record.Key == "" || strings.TrimSpace(record.Text) == "" {
			return nil, fmt.Errorf("decode order cache %s: empty key or text", path)
		}
		cache.values[record.Key] = record.Text
	}
	return cache, nil
}

func (c *atmOrderTextCache) Get(key string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	text, ok := c.values[key]
	return text, ok
}

func (c *atmOrderTextCache) Put(key, text string) error {
	if key == "" || strings.TrimSpace(text) == "" {
		return fmt.Errorf("order cache refuses empty key or text")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if previous, ok := c.values[key]; ok {
		if previous != text {
			return fmt.Errorf("order cache key collision")
		}
		return nil
	}
	handle, err := os.OpenFile(c.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(atmOrderCacheRecord{Key: key, Text: text})
	if err == nil {
		_, err = handle.Write(append(encoded, '\n'))
	}
	if err == nil {
		err = handle.Sync()
	}
	if closeErr := handle.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	c.values[key] = text
	return nil
}

func atmOrderEffectiveEffort(options ChatOptions) string {
	if !options.Thinking {
		return "none"
	}
	if effort := strings.TrimSpace(options.ReasoningEffort); effort != "" {
		return effort
	}
	return "high"
}

// atmOrderCacheKey 不含 API key；端点只以摘要进入键，完整 prompt 决定响应内容身份。
func atmOrderCacheKey(role, tier, questionID, endpoint, model string, options ChatOptions, system, user string) string {
	endpointDigest := sha256.Sum256([]byte(endpoint))
	identity := struct {
		Version        string  `json:"version"`
		Role           string  `json:"role"`
		Tier           string  `json:"tier"`
		QuestionID     string  `json:"question_id"`
		EndpointSHA    string  `json:"endpoint_sha256"`
		Model          string  `json:"model"`
		Temperature    float64 `json:"temperature"`
		MaxTokens      int     `json:"max_tokens"`
		Thinking       bool    `json:"thinking"`
		ThinkingEffort string  `json:"reasoning_effort"`
		System         string  `json:"system"`
		User           string  `json:"user"`
	}{
		Version: atmOrderCacheVersion, Role: role, Tier: tier, QuestionID: questionID,
		EndpointSHA: hex.EncodeToString(endpointDigest[:]), Model: model,
		Temperature: options.Temperature, MaxTokens: options.MaxTokens,
		Thinking: options.Thinking, ThinkingEffort: atmOrderEffectiveEffort(options),
		System: system, User: user,
	}
	encoded, _ := json.Marshal(identity)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func atmOrderChronologicalIDs(source []string, byID map[string]*ATMSGMItem) ([]string, error) {
	type reference struct {
		id         string
		occurredAt int64
	}
	references := make([]reference, 0, len(source))
	for _, id := range source {
		item := byID[id]
		if item == nil {
			return nil, fmt.Errorf("missing ATM evidence %q", id)
		}
		if item.OccurredAt <= 0 {
			return nil, fmt.Errorf("ATM evidence %q has unknown OccurredAt", id)
		}
		references = append(references, reference{id: id, occurredAt: item.OccurredAt})
	}
	// source 原本就是检索名次；稳定排序让同时间戳证据保持原名次。
	sort.SliceStable(references, func(i, j int) bool {
		return references[i].occurredAt < references[j].occurredAt
	})
	ordered := make([]string, len(references))
	for i := range references {
		ordered[i] = references[i].id
	}
	return ordered, nil
}

func atmOrderSameIDSet(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	leftCopy := append([]string(nil), left...)
	rightCopy := append([]string(nil), right...)
	sort.Strings(leftCopy)
	sort.Strings(rightCopy)
	for i := range leftCopy {
		if leftCopy[i] != rightCopy[i] {
			return false
		}
	}
	return true
}

func atmOrderEvidenceSetSHA(ids []string) string {
	ordered := append([]string(nil), ids...)
	sort.Strings(ordered)
	encoded, _ := json.Marshal(ordered)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func atmOrderTextSHA(text string) string {
	digest := sha256.Sum256([]byte(text))
	return hex.EncodeToString(digest[:])
}

func TestATMEvidenceOrderChronologicalIDs(t *testing.T) {
	byID := map[string]*ATMSGMItem{
		"a": {ItemID: "a", OccurredAt: 300},
		"b": {ItemID: "b", OccurredAt: 100},
		"c": {ItemID: "c", OccurredAt: 100},
		"d": {ItemID: "d", OccurredAt: 200},
	}
	source := []string{"a", "b", "c", "d"}
	ordered, err := atmOrderChronologicalIDs(source, byID)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"b", "c", "d", "a"}
	for i := range want {
		if ordered[i] != want[i] {
			t.Fatalf("ordered = %v, want %v", ordered, want)
		}
	}
	if source[0] != "a" || source[1] != "b" {
		t.Fatalf("chronological sort mutated source: %v", source)
	}
	if _, err := atmOrderChronologicalIDs([]string{"missing"}, byID); err == nil {
		t.Fatal("missing evidence must fail closed")
	}
	byID["undated"] = &ATMSGMItem{ItemID: "undated"}
	if _, err := atmOrderChronologicalIDs([]string{"undated"}, byID); err == nil {
		t.Fatal("unknown OccurredAt must fail closed")
	}
	if atmOrderSameIDSet([]string{"a", "a", "b"}, []string{"a", "b", "b"}) {
		t.Fatal("ID multiset comparison ignored duplicate counts")
	}
}

func TestATMEvidenceOrderPayloadOnlyChangesOrder(t *testing.T) {
	byID := map[string]*ATMSGMItem{
		"a": {ItemID: "a", FullText: "alpha body", OccurredAt: 300},
		"b": {ItemID: "b", FullText: "beta body", OccurredAt: 100},
		"c": {ItemID: "c", FullText: "gamma body", OccurredAt: 200},
	}
	retrieval := []string{"a", "b", "c"}
	chronological, err := atmOrderChronologicalIDs(retrieval, byID)
	if err != nil {
		t.Fatal(err)
	}
	retrievalPayload, _ := FormatATMEvidencePayload("question", retrieval, byID, 10)
	chronologicalPayload, _ := FormatATMEvidencePayload("question", chronological, byID, 10)
	if retrievalPayload == chronologicalPayload {
		t.Fatal("fixture did not exercise an order change")
	}
	for _, item := range byID {
		if strings.Count(retrievalPayload, item.FullText) != 1 || strings.Count(chronologicalPayload, item.FullText) != 1 {
			t.Fatalf("evidence %s was lost or duplicated", item.ItemID)
		}
	}
	if atmOrderEvidenceSetSHA(retrieval) != atmOrderEvidenceSetSHA(chronological) {
		t.Fatal("order changed the evidence-set digest")
	}
}

func TestATMEvidenceOrderCacheIdentityAndReuse(t *testing.T) {
	options := ChatOptions{Temperature: 0, MaxTokens: 0, Thinking: true}
	key := atmOrderCacheKey("answer", "hard", "q1", "https://example.invalid/v1", "model-a", options, "system", "payload-a")
	same := atmOrderCacheKey("answer", "hard", "q1", "https://example.invalid/v1", "model-a", options, "system", "payload-a")
	if key != same {
		t.Fatal("identical request identity produced different cache keys")
	}
	otherModel := atmOrderCacheKey("answer", "hard", "q1", "https://example.invalid/v1", "model-b", options, "system", "payload-a")
	otherPayload := atmOrderCacheKey("answer", "hard", "q1", "https://example.invalid/v1", "model-a", options, "system", "payload-b")
	if key == otherModel || key == otherPayload {
		t.Fatal("cache key does not bind model and exact payload")
	}
	cache, err := newATMOrderTextCache(filepath.Join(t.TempDir(), "answer-cache.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := cache.Put(key, "answer"); err != nil {
		t.Fatal(err)
	}
	if err := cache.Put(key, "different"); err == nil {
		t.Fatal("cache collision was not rejected")
	}
	if err := cache.Put("empty", ""); err == nil {
		t.Fatal("empty answer was cached")
	}
	reloaded, err := newATMOrderTextCache(cache.path)
	if err != nil {
		t.Fatal(err)
	}
	if text, ok := reloaded.Get(key); !ok || text != "answer" {
		t.Fatalf("cache reload = %q, %v", text, ok)
	}
}

type atmOrderPrepared struct {
	qa             ATMQAItem
	sourceIDs      []string
	armIDs         [2][]string
	payloads       [2]string
	formatted      [2][]string
	occurredAt     [2][]int64
	evidenceSetSHA string
	recall         map[string]float64
	recallMS       float64
	anyHit10       bool
	allHit10       bool
	orderChanged   bool
}

func atmOrderOccurrences(ids []string, byID map[string]*ATMSGMItem) []int64 {
	out := make([]int64, len(ids))
	for i, id := range ids {
		out[i] = byID[id].OccurredAt
	}
	return out
}

func atmOrderAnswerAndJudge(
	prepared *atmOrderPrepared,
	arm int,
	tier string,
	answerClient, judgeClient *ChatClient,
	answerOptions, judgeOptions ChatOptions,
	answerCache, judgeCache *atmOrderTextCache,
) map[string]any {
	q := prepared.qa
	payload := prepared.payloads[arm]
	answerKey := atmOrderCacheKey("answer", tier, q.ID, answerClient.URL(), answerClient.Model(), answerOptions, ATMSystemPrompt, payload)
	answerStarted := time.Now()
	prediction, answerCacheHit := answerCache.Get(answerKey)
	answerErr := ""
	cacheErr := ""
	if !answerCacheHit {
		answer, err := answerClient.CompleteWithRetry(ATMSystemPrompt, payload)
		if err != nil {
			answerErr = err.Error()
		} else {
			prediction = strings.TrimSpace(answer)
			if err := answerCache.Put(answerKey, prediction); err != nil {
				cacheErr = "answer: " + err.Error()
			}
		}
	}
	answerMS := float64(time.Since(answerStarted).Microseconds()) / 1000.0

	qsScore := 0.0
	qsScored := false
	var judgeCorrect any
	judgeReason := ""
	judgeRaw := ""
	judgeKey := ""
	judgeCacheHit := false
	judgeMS := 0.0
	unscoredReason := ""
	if answerErr != "" {
		unscoredReason = "ANSWER_UNAVAILABLE: " + answerErr
		judgeReason = unscoredReason
	} else {
		switch q.QType {
		case "list_recall":
			qsScore = ComputeATMListJaccard(q.Answer, prediction)
			qsScored = true
			judgeCorrect = qsScore >= 0.5
			judgeReason = fmt.Sprintf("ATM official list_recall Jaccard = %.3f", qsScore)
		case "number":
			correct, reason := ComputeATMNumberEMWithQuestion(q.Answer, prediction, q.Question)
			if correct {
				qsScore = 1.0
			}
			qsScored = true
			judgeCorrect = correct
			judgeReason = reason
		default:
			judgePrompt := BuildATMJudgePrompt(q.Question, q.Answer, prediction)
			judgeKey = atmOrderCacheKey("judge", tier, q.ID, judgeClient.URL(), judgeClient.Model(), judgeOptions, "", judgePrompt)
			judgeStarted := time.Now()
			judgeRaw, judgeCacheHit = judgeCache.Get(judgeKey)
			if !judgeCacheHit {
				raw, err := judgeClient.CompleteWithRetry("", judgePrompt)
				if err != nil {
					unscoredReason = "JUDGE_UNAVAILABLE: " + err.Error()
					judgeReason = unscoredReason
				} else {
					judgeRaw = raw
					if err := judgeCache.Put(judgeKey, judgeRaw); err != nil {
						if cacheErr == "" {
							cacheErr = "judge: " + err.Error()
						} else {
							cacheErr += "; judge: " + err.Error()
						}
					}
				}
			}
			judgeMS = float64(time.Since(judgeStarted).Microseconds()) / 1000.0
			if unscoredReason == "" {
				correct, explanation, err := ParseATMJudgeResponse(judgeRaw)
				if err != nil {
					unscoredReason = "JUDGE_UNPARSED: " + err.Error()
					judgeReason = unscoredReason
				} else {
					judgeCorrect = correct
					if correct {
						qsScore = 1.0
					}
					qsScored = true
					judgeReason = explanation
				}
			}
		}
	}
	verdict := ""
	if correct, ok := judgeCorrect.(bool); ok {
		if correct {
			verdict = "correct"
		} else {
			verdict = "wrong"
		}
	}
	contextText := strings.Join(prepared.formatted[arm], "\n")
	f1 := TokenF1(prediction, q.Answer)
	exact := NormalizeAnswer(prediction) != "" && NormalizeAnswer(prediction) == NormalizeAnswer(q.Answer)
	return map[string]any{
		"id": q.ID, "sample_id": q.QType, "qtype": q.QType,
		"question": q.Question, "gold": q.Answer, "evidence_texts": q.EvidenceIDs,
		"arm":                   atmOrderArmNames[arm],
		"source_retrieval_ids":  prepared.sourceIDs,
		"retrieval_ids":         prepared.armIDs[arm],
		"delivered_occurred_at": prepared.occurredAt[arm],
		"evidence_set_sha256":   prepared.evidenceSetSHA,
		"payload_sha256":        atmOrderTextSHA(payload),
		"order_changed":         prepared.orderChanged,
		"retrieval_recall":      prepared.recall,
		"evidence_recall":       PyRound(prepared.recall["R@10"], 4),
		"evidence_hit":          prepared.anyHit10,
		"recall_at_10_any_hit":  prepared.anyHit10,
		"recall_at_10_gt":       prepared.allHit10,
		"prediction":            prediction, "f1": PyRound(f1, 4), "exact": exact,
		"judge": verdict, "judge_correct": judgeCorrect, "judge_reason": judgeReason,
		"judge_raw": judgeRaw, "qs_score": PyRound(qsScore, 4), "qs_scored": qsScored,
		"answer_error": answerErr, "unscored_reason": unscoredReason, "cache_error": cacheErr,
		"answer_cache_key": answerKey, "judge_cache_key": judgeKey,
		"answer_cache_hit": answerCacheHit, "judge_cache_hit": judgeCacheHit,
		"formatted_items":        prepared.formatted[arm],
		"context_chars":          len(contextText),
		"delivery_tokenzr":       tokenzr.Count(contextText),
		"answer_payload_tokenzr": tokenzr.Count(payload),
		"items_delivered":        len(prepared.armIDs[arm]),
		"recall_ms":              PyRound(prepared.recallMS, 3),
		"answer_ms":              PyRound(answerMS, 3), "judge_ms": PyRound(judgeMS, 3),
		"answer_model": answerClient.Model(), "judge_model": judgeClient.Model(),
		"answer_max_tokens": answerOptions.MaxTokens, "judge_max_tokens": judgeOptions.MaxTokens,
		"answer_thinking": answerOptions.Thinking, "judge_thinking": judgeOptions.Thinking,
		"answer_reasoning_effort": atmOrderEffectiveEffort(answerOptions),
		"judge_reasoning_effort":  atmOrderEffectiveEffort(judgeOptions),
		"judge_star":              judgeClient.Model() != "gpt-5-mini",
		"engine_transport":        "local.sqlite-fts5-bm25",
		"engine_scope":            "local-eval-temp-vault",
		"engine_upstream":         "local://sqlite-fts5-bm25",
		"pulse_mode":              "local_plain", "local_fallback": false, "cloud_hits": 0,
		"corpus_sha256":                 PinATMCorpusSHA256,
		"official_corpus_pins_verified": true,
		"scoring_protocol":              ATMOfficialScoringProtocol,
		"scoring_version":               ATMOfficialScoringVersion,
		"scoring_fixture_sha256":        PinATMOfficialScoringFixtureSHA256,
	}
}

func atmOrderSummary(arm string, rows []map[string]any) map[string]any {
	summary := map[string]any{"arm": arm, "n": len(rows)}
	if len(rows) == 0 {
		return summary
	}
	qsSum, qsScoredSum, f1Sum := 0.0, 0.0, 0.0
	r10Sum, anyHitSum, allHitSum := 0.0, 0.0, 0.0
	tokenTotal, payloadTokenTotal, contextTotal, itemsTotal := 0, 0, 0, 0
	recallMSSum, answerMSSum := 0.0, 0.0
	scored, unscored, empty, orderChanged := 0, 0, 0, 0
	answerCacheHits, judgeCacheHits, cacheErrors := 0, 0, 0
	type qtypeAccumulator struct {
		n, scored int
		qsSum     float64
	}
	qtypes := map[string]*qtypeAccumulator{}
	for _, row := range rows {
		qs := rowFloat(row, "qs_score")
		qsSum += qs
		f1Sum += rowFloat(row, "f1")
		r10Sum += row["retrieval_recall"].(map[string]float64)["R@10"]
		if truthy(row["recall_at_10_any_hit"]) {
			anyHitSum++
		}
		if truthy(row["recall_at_10_gt"]) {
			allHitSum++
		}
		if truthy(row["qs_scored"]) {
			scored++
			qsScoredSum += qs
		} else {
			unscored++
		}
		if strings.TrimSpace(pyStr(row["prediction"])) == "" {
			empty++
		}
		if truthy(row["order_changed"]) {
			orderChanged++
		}
		if truthy(row["answer_cache_hit"]) {
			answerCacheHits++
		}
		if truthy(row["judge_cache_hit"]) {
			judgeCacheHits++
		}
		if pyStr(row["cache_error"]) != "" {
			cacheErrors++
		}
		tokenTotal += int(rowFloat(row, "delivery_tokenzr"))
		payloadTokenTotal += int(rowFloat(row, "answer_payload_tokenzr"))
		contextTotal += int(rowFloat(row, "context_chars"))
		itemsTotal += int(rowFloat(row, "items_delivered"))
		recallMSSum += rowFloat(row, "recall_ms")
		answerMSSum += rowFloat(row, "answer_ms")
		qtype := pyStr(row["qtype"])
		accumulator := qtypes[qtype]
		if accumulator == nil {
			accumulator = &qtypeAccumulator{}
			qtypes[qtype] = accumulator
		}
		accumulator.n++
		accumulator.qsSum += qs
		if truthy(row["qs_scored"]) {
			accumulator.scored++
		}
	}
	n := float64(len(rows))
	summary["qs_score_all_denominator"] = PyRound(qsSum/n, 6)
	if scored > 0 {
		summary["qs_score_scored_only"] = PyRound(qsScoredSum/float64(scored), 6)
	}
	summary["scored_count"] = scored
	summary["unscored_count"] = unscored
	summary["empty_answer_count"] = empty
	summary["f1"] = PyRound(f1Sum/n, 6)
	summary["recall_at_10_any_hit_rate"] = PyRound(anyHitSum/n, 6)
	summary["recall_at_10_gold_mean_rate"] = PyRound(r10Sum/n, 6)
	summary["recall_at_10_gt_rate"] = PyRound(allHitSum/n, 6)
	summary["delivery_tokenzr_total"] = tokenTotal
	summary["delivery_tokenzr_mean"] = PyRound(float64(tokenTotal)/n, 2)
	summary["answer_payload_tokenzr_total"] = payloadTokenTotal
	summary["answer_payload_tokenzr_mean"] = PyRound(float64(payloadTokenTotal)/n, 2)
	summary["context_chars_mean"] = PyRound(float64(contextTotal)/n, 2)
	summary["items_delivered_mean"] = PyRound(float64(itemsTotal)/n, 2)
	summary["recall_ms_mean"] = PyRound(recallMSSum/n, 3)
	summary["answer_ms_mean_including_cache"] = PyRound(answerMSSum/n, 3)
	summary["answer_cache_hits"] = answerCacheHits
	summary["judge_cache_hits"] = judgeCacheHits
	summary["cache_error_count"] = cacheErrors
	summary["order_changed_count"] = orderChanged
	byQType := map[string]any{}
	for qtype, accumulator := range qtypes {
		block := map[string]any{
			"n":                        accumulator.n,
			"scored_count":             accumulator.scored,
			"qs_score_all_denominator": PyRound(accumulator.qsSum/float64(accumulator.n), 6),
		}
		if accumulator.scored > 0 {
			block["qs_score_scored_only"] = PyRound(accumulator.qsSum/float64(accumulator.scored), 6)
		}
		byQType[qtype] = block
	}
	summary["by_qtype"] = byQType
	return summary
}

func atmOrderPairedSummary(retrievalRows, chronologicalRows []map[string]any) map[string]any {
	paired := 0
	retrievalSum, chronologicalSum := 0.0, 0.0
	improved, worsened, predictionChanged := 0, 0, 0
	for i := range retrievalRows {
		left, right := retrievalRows[i], chronologicalRows[i]
		if truthy(left["qs_scored"]) && truthy(right["qs_scored"]) {
			paired++
			leftQS, rightQS := rowFloat(left, "qs_score"), rowFloat(right, "qs_score")
			retrievalSum += leftQS
			chronologicalSum += rightQS
			if rightQS > leftQS {
				improved++
			} else if rightQS < leftQS {
				worsened++
			}
		}
		if pyStr(left["prediction"]) != pyStr(right["prediction"]) {
			predictionChanged++
		}
	}
	result := map[string]any{
		"paired_scored_count":      paired,
		"prediction_changed_count": predictionChanged,
		"paired_improved_count":    improved,
		"paired_worsened_count":    worsened,
	}
	if paired > 0 {
		result["paired_qs_retrieval"] = PyRound(retrievalSum/float64(paired), 6)
		result["paired_qs_chronological"] = PyRound(chronologicalSum/float64(paired), 6)
		result["paired_qs_delta"] = PyRound((chronologicalSum-retrievalSum)/float64(paired), 6)
	}
	return result
}

func TestZZATMEvidenceOrderAB(t *testing.T) {
	if os.Getenv("RUN_ATM_EVIDENCE_ORDER_AB") != "1" {
		t.Skip("set RUN_ATM_EVIDENCE_ORDER_AB=1 to run the real evidence-order A/B")
	}
	if err := VerifyATMOfficialProtocol(); err != nil {
		t.Fatalf("VerifyATMOfficialProtocol: %v", err)
	}
	var cfg *config.Config
	var err error
	if path := strings.TrimSpace(os.Getenv("ATM_ORDER_CONFIG")); path != "" {
		cfg, err = config.Load(config.LoadOptions{Path: &path, Env: map[string]string{}})
	} else {
		cfg = testConfig(t)
	}
	if err != nil {
		t.Fatalf("load order experiment config: %v", err)
	}
	baseURL := strings.TrimSpace(os.Getenv("ROLLING_MEMORY_EVAL_BASE_URL"))
	if baseURL == "" {
		baseURL = strings.TrimSpace(cfg.Eval.BaseURL)
	}
	apiKey := strings.TrimSpace(os.Getenv("ROLLING_MEMORY_EVAL_API_KEY"))
	if apiKey == "" {
		apiKey, _ = cfg.EvalKey()
	}
	if baseURL == "" || apiKey == "" {
		t.Skip("requires ROLLING_MEMORY_EVAL_BASE_URL/API_KEY, or ATM_ORDER_CONFIG with [eval] credentials")
	}
	baseModel := strings.TrimSpace(cfg.Eval.Model)
	if baseModel == "" {
		baseModel = "deepseek-v4.1-flash"
	}
	answerModel := strings.TrimSpace(os.Getenv("ATM_ORDER_ANSWER_MODEL"))
	if answerModel == "" {
		answerModel = baseModel
	}
	judgeModel := strings.TrimSpace(os.Getenv("ATM_ORDER_JUDGE_MODEL"))
	if judgeModel == "" {
		judgeModel = baseModel
	}
	// 与本地基线 ChatOptions 未显式传 effort 时的有效值一致：high。
	// 不继承全局配置缺省 low；实验档只能由 ATM_ORDER_REASONING_EFFORT 显式覆盖。
	answerEffort := strings.TrimSpace(os.Getenv("ATM_ORDER_REASONING_EFFORT"))
	if answerEffort == "" {
		answerEffort = "high"
	}
	// cfg.Mode=cloud 只允许模型 egress；记忆检索始终是本地下文 Vault，零云端记忆请求。
	cfg.Mode = "cloud"
	answerOptions := ChatOptions{
		BaseURL: baseURL, Model: answerModel, APIKey: apiKey,
		Temperature: 0, MaxTokens: 0, TimeoutSec: 90, Thinking: true, ReasoningEffort: answerEffort,
	}
	judgeOptions := ChatOptions{
		BaseURL: baseURL, Model: judgeModel, APIKey: apiKey,
		Temperature: 0, MaxTokens: 4096, TimeoutSec: 90, Thinking: false,
	}
	answerClient, err := NewChatClient(cfg, answerOptions)
	if err != nil {
		t.Fatalf("answer client: %v", err)
	}
	judgeClient, err := NewChatClient(cfg, judgeOptions)
	if err != nil {
		t.Fatalf("judge client: %v", err)
	}
	workers := 24 // 与本地基线相同；两臂合计仍最多 24 个并发模型调用。
	if raw := strings.TrimSpace(os.Getenv("ATM_ORDER_WORKERS")); raw != "" {
		workers, err = strconv.Atoi(raw)
		if err != nil || workers < 1 {
			t.Fatalf("ATM_ORDER_WORKERS must be a positive integer")
		}
	}
	outDir := strings.TrimSpace(os.Getenv("ATM_ORDER_OUT_DIR"))
	if outDir == "" {
		outDir = "/tmp/atm-evidence-order-ab"
	}
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		t.Fatal(err)
	}
	repoEvalData, _ := filepath.Abs(filepath.Join("..", "..", DataDirName))
	atmDir, err := ResolveATMDataDir(filepath.Join(repoEvalData, "atmbench"))
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyATMCorpusPins(atmDir); err != nil {
		t.Fatal(err)
	}
	sgmItems, byID, _, err := LoadATMSGMItems(atmDir, ATMScope, 0)
	if err != nil {
		t.Fatal(err)
	}
	atmDB, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "atm_order.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer atmDB.Close()
	_, _ = atmDB.Exec(`PRAGMA synchronous = OFF; PRAGMA journal_mode = MEMORY;`)
	atmVault, err := thinclient.NewVault(atmDB, []byte("atm-order-eval-key"))
	if err != nil {
		t.Fatal(err)
	}
	atmVault.SetCanonicalHarmonics(ramesdk.CanonicalHarmonicOptions{EnableStructuredDates: true})
	atmVault.SetSegmenterOptions(ramesdk.SegmenterPackOptions{
		SentenceAtomPacker: packLocalOfflineShannonSentence,
		HarmonicOverlay:    true,
	})
	tx, err := atmDB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	stmtDoc, _ := tx.Prepare(`INSERT OR REPLACE INTO thin_vault_docs(doc_id, layer, occurred_at, raw_text, sentences_json, address_uri, coord64, waveform_bar, doc_macro_wave, kept_indices_json, assistant_id, incarnation_id, locus) VALUES (?, 'episodic', ?, ?, '[]', '', 0, '', '', '[]', 'default', '', '')`)
	stmtMap, _ := tx.Prepare(`INSERT OR REPLACE INTO thin_vault_fts_map(rowid, doc_id) VALUES (?, ?)`)
	stmtFTS, _ := tx.Prepare(`INSERT INTO thin_vault_docs_fts(rowid, raw_text) VALUES (?, ?)`)
	for idx, item := range sgmItems {
		rowID := int64(idx + 1)
		if _, err := stmtDoc.Exec(item.ItemID, item.OccurredAt, item.FullText); err != nil {
			t.Fatal(err)
		}
		if _, err := stmtMap.Exec(rowID, item.ItemID); err != nil {
			t.Fatal(err)
		}
		if _, err := stmtFTS.Exec(rowID, item.FullText); err != nil {
			t.Fatal(err)
		}
	}
	_ = stmtDoc.Close()
	_ = stmtMap.Close()
	_ = stmtFTS.Close()
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := atmVault.SearchClientSDKDefault("warmup", 10); err != nil {
		t.Fatal(err)
	}
	tiers := []string{TierATMHard, TierATM1013}
	if raw := strings.TrimSpace(os.Getenv("ATM_ORDER_TIERS")); raw != "" {
		tiers = strings.Split(raw, ",")
	}
	limit := 0
	if raw := strings.TrimSpace(os.Getenv("ATM_ORDER_LIMIT")); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 {
			t.Fatalf("ATM_ORDER_LIMIT must be a positive integer")
		}
	}
	for _, tier := range tiers {
		tier = strings.TrimSpace(tier)
		if tier != TierATMHard && tier != TierATM1013 {
			t.Fatalf("unsupported ATM tier %q", tier)
		}
		t.Run(tier, func(t *testing.T) {
			started := time.Now().UTC()
			qas, err := LoadATMQAItems(atmDir, tier, 0, limit)
			if err != nil {
				t.Fatal(err)
			}
			answerCache, err := newATMOrderTextCache(filepath.Join(outDir, "cache-"+tier+"-answer.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			judgeCache, err := newATMOrderTextCache(filepath.Join(outDir, "cache-"+tier+"-judge.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			prepared := make([]atmOrderPrepared, len(qas))
			for idx, q := range qas {
				retrievalStarted := time.Now()
				hits, err := atmVault.SearchClientSDKDefault(q.Question, 50)
				if err != nil {
					t.Fatalf("%s recall: %v", q.ID, err)
				}
				recallMS := float64(time.Since(retrievalStarted).Microseconds()) / 1000.0
				sourceIDs := make([]string, 0, len(hits))
				seen := map[string]bool{}
				for _, hit := range hits {
					if byID[hit.UnitID] == nil {
						t.Fatalf("%s returned foreign evidence %q", q.ID, hit.UnitID)
					}
					if seen[hit.UnitID] {
						t.Fatalf("%s returned duplicate evidence %q", q.ID, hit.UnitID)
					}
					seen[hit.UnitID] = true
					sourceIDs = append(sourceIDs, hit.UnitID)
				}
				topN := 10
				if len(sourceIDs) < topN {
					topN = len(sourceIDs)
				}
				retrievalIDs := append([]string(nil), sourceIDs[:topN]...)
				chronologicalIDs, err := atmOrderChronologicalIDs(retrievalIDs, byID)
				if err != nil {
					t.Fatalf("%s chronological order: %v", q.ID, err)
				}
				if !atmOrderSameIDSet(retrievalIDs, chronologicalIDs) {
					t.Fatalf("%s evidence-set mismatch", q.ID)
				}
				item := atmOrderPrepared{
					qa: q, sourceIDs: sourceIDs,
					armIDs:         [2][]string{retrievalIDs, chronologicalIDs},
					evidenceSetSHA: atmOrderEvidenceSetSHA(retrievalIDs),
					recall:         ComputeATMRecallMap(q.EvidenceIDs, sourceIDs), recallMS: recallMS,
				}
				item.occurredAt = [2][]int64{atmOrderOccurrences(retrievalIDs, byID), atmOrderOccurrences(chronologicalIDs, byID)}
				item.payloads[atmOrderRetrievalArm], item.formatted[atmOrderRetrievalArm] = FormatATMEvidencePayload(q.Question, retrievalIDs, byID, 10)
				item.payloads[atmOrderChronologicalArm], item.formatted[atmOrderChronologicalArm] = FormatATMEvidencePayload(q.Question, chronologicalIDs, byID, 10)
				item.orderChanged = item.payloads[0] != item.payloads[1]
				gold := map[string]bool{}
				for _, id := range q.EvidenceIDs {
					gold[id] = true
				}
				goldInTop10 := 0
				for _, id := range retrievalIDs {
					if gold[id] {
						goldInTop10++
					}
				}
				item.anyHit10 = goldInTop10 > 0
				item.allHit10 = len(gold) > 0 && goldInTop10 == len(gold)
				prepared[idx] = item
			}
			pairRows := ParallelMap(len(prepared), workers, func(idx int) [2]map[string]any {
				var rows [2]map[string]any
				// 奇偶题交换调用先后，降低提供方时间漂移系统偏向某一臂的可能。
				for step := 0; step < 2; step++ {
					arm := (idx + step) % 2
					rows[arm] = atmOrderAnswerAndJudge(&prepared[idx], arm, tier, answerClient, judgeClient, answerOptions, judgeOptions, answerCache, judgeCache)
				}
				return rows
			})
			rowsByArm := [2][]map[string]any{make([]map[string]any, len(prepared)), make([]map[string]any, len(prepared))}
			for idx, pair := range pairRows {
				for arm := 0; arm < 2; arm++ {
					rowsByArm[arm][idx] = pair[arm]
					if pair[arm]["evidence_set_sha256"] != prepared[idx].evidenceSetSHA {
						t.Fatalf("%s arm %s changed evidence set", prepared[idx].qa.ID, atmOrderArmNames[arm])
					}
				}
			}
			for arm := 0; arm < 2; arm++ {
				path := filepath.Join(outDir, "results-atm-"+tier+"-evidence-order-"+atmOrderArmNames[arm]+".jsonl")
				if err := writeResults(path, rowsByArm[arm]); err != nil {
					t.Fatal(err)
				}
			}
			retrievalSummary := atmOrderSummary("retrieval", rowsByArm[0])
			chronologicalSummary := atmOrderSummary("chronological", rowsByArm[1])
			for _, key := range []string{"recall_at_10_any_hit_rate", "recall_at_10_gold_mean_rate", "recall_at_10_gt_rate"} {
				if retrievalSummary[key] != chronologicalSummary[key] {
					t.Fatalf("order-only invariant %s changed: %v vs %v", key, retrievalSummary[key], chronologicalSummary[key])
				}
			}
			summary := map[string]any{
				"experiment": "atm-local-plain-evidence-order-ab",
				"tier":       tier, "questions": len(qas),
				"started_at":   started.Format(time.RFC3339Nano),
				"completed_at": time.Now().UTC().Format(time.RFC3339Nano),
				"answer_model": answerModel, "judge_model": judgeModel,
				"answer_reasoning_effort": atmOrderEffectiveEffort(answerOptions),
				"judge_reasoning_effort":  atmOrderEffectiveEffort(judgeOptions),
				"answer_max_tokens":       answerOptions.MaxTokens, "judge_max_tokens": judgeOptions.MaxTokens,
				"judge_star": judgeModel != "gpt-5-mini",
				"arms":       map[string]any{"retrieval": retrievalSummary, "chronological": chronologicalSummary},
				"paired":     atmOrderPairedSummary(rowsByArm[0], rowsByArm[1]),
				"pulse_mode": "local_plain", "engine_transport": "local.sqlite-fts5-bm25",
				"local_fallback": false, "cloud_hits": 0,
				"corpus_sha256": PinATMCorpusSHA256, "official_corpus_pins_verified": true,
				"scoring_protocol":       ATMOfficialScoringProtocol,
				"scoring_fixture_sha256": PinATMOfficialScoringFixtureSHA256,
			}
			if err := writeJSON(filepath.Join(outDir, "summary-atm-"+tier+"-evidence-order-ab.json"), summary); err != nil {
				t.Fatal(err)
			}
			t.Logf("tier=%s retrieval_qs=%v chronological_qs=%v paired=%v h@10=%v r@10=%v out=%s",
				tier, retrievalSummary["qs_score_all_denominator"], chronologicalSummary["qs_score_all_denominator"],
				summary["paired"], retrievalSummary["recall_at_10_any_hit_rate"], retrievalSummary["recall_at_10_gold_mean_rate"], outDir)
		})
	}
}
