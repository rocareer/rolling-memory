// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package eval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rocareer/rolling-memory/internal/config"
	"github.com/rocareer/rolling-memory/internal/ids"
	ramesdk "github.com/rocareer/rolling-memory/sdk/go"
)

// Options 是一次评测运行的完整参数（命令行 flag 覆盖 [eval] 段）。
type Options struct {
	Suite           string // locomo | atm
	DataDir         string
	Tier            string
	Limit           int
	Offset          int
	StorePath       string
	OutDir          string
	Rebuild         bool
	ReusePrepare    bool
	VerifyPrepare   bool
	Scope           string
	Reader          string
	Oracle          bool
	Answerer        string // none|llm
	Judge           string // off|llm
	Model           string
	BaseURL         string
	APIKey          string
	Temperature     float64
	JudgeTemp       float64
	MaxTokens       int
	TimeoutSec      int
	Thinking        bool
	ReasoningEffort string
	Workers         int
	ContextChars    int
	RawLimit        int
	CorpusLimit     int
	ProgressEach    int
	NoVerify        bool
	// DenoiseRatio 是分形干句降噪比例（--denoise-ratio：0.0=零损，0.25=安全，0.50=推荐，0.75=激进）。
	DenoiseRatio float64
	// PulseTokenizer 选择盲态脉冲切词臂：standard（默认，SDK 原生表面词 + 子词/谐波切词）
	// 或 fts5-bm25 / fts5-trigram（对照臂）。Cloud 三臂都只消费云端结果，不融合本地检索。
	PulseTokenizer string
	// PulseMode 选择评测脉冲运行模式（三种独立注册的评测模式，各自独立 scope 与 store，互不清库）：
	//   - "pulse_cipher"（默认）：脉冲密文云端检索
	//   - "pulse_plain"：脉冲明文云端检索（独立 scope *-mem-eval-<suite>-pulse-plain[-<世代>]）
	//   - "local_plain"：本地明文离线检索（独立 scope *-mem-eval-<suite>-local-plain[-<世代>]）
	PulseMode string
	Log       func(format string, args ...any)
}

// Run 跑一次评测并返回汇总报告（同时把 results/summary 落盘）。
func Run(cfg *config.Config, opts Options) (map[string]any, error) {
	if opts.VerifyPrepare && (opts.Oracle || opts.Rebuild) {
		return nil, errors.New("--verify-prepare is read-only and cannot be combined with --oracle or --rebuild")
	}
	suite := strings.ToLower(strings.TrimSpace(opts.Suite))
	tierLow := strings.ToLower(strings.TrimSpace(opts.Tier))
	if suite == SuiteATM || tierLow == TierATMHard || tierLow == TierATM1013 {
		return RunATM(cfg, opts)
	}
	logf := opts.Log
	if logf == nil {
		logf = func(string, ...any) {}
	}
	started := time.Now()
	if opts.Answerer == "llm" || opts.Judge == "llm" {
		if err := VerifyFrozenProtocol(); err != nil {
			return nil, err
		}
	}
	dataDir, err := ResolveDataDir(opts.DataDir)
	if err != nil {
		return nil, err
	}
	manifest, err := LoadManifest(dataDir)
	if err != nil {
		return nil, err
	}
	if err := CheckLoCoMoTier(opts.Tier); err != nil {
		return nil, err
	}
	tier, err := manifest.Tier(opts.Tier)
	if err != nil {
		return nil, err
	}
	questionsPath := filepath.Join(dataDir, tier.File)
	corpusPath := filepath.Join(dataDir, manifest.Corpus.Path)
	if !opts.NoVerify {
		if err := VerifyFile(questionsPath, tier.SHA256); err != nil {
			return nil, err
		}
		if err := VerifyFile(corpusPath, manifest.Corpus.SHA256); err != nil {
			return nil, err
		}
	}
	questions, err := LoadQuestions(questionsPath, opts.Offset, opts.Limit)
	if err != nil {
		return nil, err
	}
	opts.Suite = SuiteLoCoMo
	opts.PulseMode = NormalizePulseMode(opts.PulseMode)
	opts.Scope = ScopeForSuiteAndMode(SuiteLoCoMo, cloudUserID(), opts.PulseMode)
	if opts.RawLimit <= 0 {
		opts.RawLimit = 96
	}
	if opts.ContextChars <= 0 {
		opts.ContextChars = 24576
	}
	arm := BuildArm(opts)
	logf("[eval] arm=%s pulse_mode=%s tier=%s questions=%d transport=%s oracle=%v answerer=%s judge=%s scope=%s",
		arm, opts.PulseMode, opts.Tier, len(questions), TransportForPulseMode(opts.PulseMode), opts.Oracle, answererName(opts), judgeName(opts), opts.Scope)

	answerer, judge, err := buildProtocols(cfg, opts)
	if err != nil {
		return nil, err
	}

	ingestMeta := map[string]any{"corpus_rows": 0, "inserted": 0}
	var engine *CloudEngine
	if !opts.Oracle {
		engine, err = NewCloudEngine(cfg, SuiteLoCoMo, expandUser(opts.StorePath), WithPulseTokenizer(opts.PulseTokenizer), WithPulseMode(opts.PulseMode))
		if err != nil {
			return nil, err
		}
		opts.Scope = engine.Scope
		corpus, loadErr := LoadCorpus(corpusPath, opts.CorpusLimit)
		if loadErr != nil {
			return nil, loadErr
		}
		documents := make([]CloudDocument, 0, len(corpus))
		// 同一会话内的轮次按出现顺序累加物理行号：相邻轮次行跨度紧邻（gap=1），
		// 引擎的四维空间连续行共振即可在召回时补全上下文轮次。
		sessionLineCursor := make(map[string]int, 64)
		for _, row := range corpus {
			text := pyStrip(pyStr(firstTruthy(row["text"], "")))
			if text == "" {
				continue
			}
			author := pyStrip(pyStr(firstTruthy(row["author"], firstTruthy(row["speaker"], ""))))
			locator := pyStr(firstTruthy(row["locator"], ""))
			session := pyStr(firstTruthy(row["session"], firstTruthy(strings.Split(locator, "#")[0], "default")))
			turn := firstTruthy(row["id"], 0)
			occurred, convErr := pyInt(firstTruthy(row["occurred_at"], 0))
			if convErr != nil {
				return nil, convErr
			}
			docID := "locomo-" + ids.SHA256Hex(engine.Scope + "\x00" + session + "\x00" + pyStr(turn))[:24]
			lineCount := strings.Count(text, "\n") + 1
			if lineCount < 1 {
				lineCount = 1
			}
			lineStart := sessionLineCursor[session] + 1
			lineEnd := lineStart + lineCount - 1
			sessionLineCursor[session] = lineEnd
			blockID := locator
			if blockID == "" {
				blockID = session + "#" + pyStr(turn)
			}
			// 评测原始对话统一作为 L2 episodic 写入；评测模块只使用引擎的三个记忆层，
			// 题目/证据等业务标注全部由评测模块自持，不进入引擎。
			documents = append(documents, CloudDocument{
				ID:         docID,
				Text:       text,
				Author:     author,
				OccurredAt: occurred,
				NarratedAt: occurred,
				Layer:      ramesdk.LayerEpisodic,
				DocKey:     session,
				FileURL:    session,
				BlockKey:   blockID,
				BlockID:    blockID,
				LineStart:  lineStart,
				LineEnd:    lineEnd,
				SubUnits:   locomoTurnSubUnits(docID, text, row, lineStart, lineEnd),
			})
		}
		if opts.VerifyPrepare {
			verified, verifyErr := engine.VerifyPrepared(context.Background(), documents)
			if verifyErr != nil {
				return nil, verifyErr
			}
			opts.Scope = engine.Scope
			verification := map[string]any{
				"verified": true, "transport": TransportForPulseMode(opts.PulseMode), "scope": engine.Scope,
				"local_fallback": false, "engine": engine.Metadata(),
				"ingest": map[string]any{"manifest": verified, "reused": true, "inserted": 0},
			}
			if err := os.MkdirAll(opts.OutDir, 0o755); err != nil {
				return nil, err
			}
			if err := writeJSON(filepath.Join(opts.OutDir, "verification-locomo.json"), verification); err != nil {
				return nil, err
			}
			return verification, nil
		}
		prepared, ingestErr := engine.Prepare(context.Background(), documents, opts.Rebuild, opts.ReusePrepare, opts.Workers, logf)
		if ingestErr != nil {
			return nil, ingestErr
		}
		defer engine.CloseGeneration(context.Background(), logf)
		// Prepare 可能已把引擎绑定到本次评测自己的世代 scope：provenance 一律以绑定后的真值为准。
		opts.Scope = engine.Scope
		inserted := len(documents)
		if prepared.Reused {
			inserted = 0
		}
		ingestMeta = map[string]any{
			"corpus_rows": len(corpus), "inserted": inserted, "reused": prepared.Reused,
			"corpus_sha256": prepared.CorpusSHA256, "manifest": prepared,
			"generation": prepared.Generation,
			// purged_units = 本次「重算记忆」在重灌前清空的旧记忆条数（rebuild 口径下的 scope 级清库回执）。
			"purged_units": prepared.PurgedUnits,
			// retired_scopes = 本次回收的过期世代回执（独立世代 scope，回收不阻塞本次运行）。
			"retired_scopes": prepared.RetiredScopes,
		}
	}

	results, err := runQuestions(engine, opts, questions, answerer, judge, logf)
	if err != nil {
		return nil, err
	}
	summary := Aggregate(results, arm)
	summary["ingest"] = ingestMeta
	summary["elapsed_seconds"] = PyRound(time.Since(started).Seconds(), 1)
	summary["oracle"] = opts.Oracle
	if opts.Oracle {
		summary["mode"] = "oracle"
		summary["pulse_mode"] = "oracle"
		summary["engine"] = map[string]any{"transport": "oracle-gold", "scope": opts.Scope, "local_fallback": false}
	} else {
		summary["mode"] = ArmModeForPulseMode(opts.PulseMode)
		summary["pulse_mode"] = NormalizePulseMode(opts.PulseMode)
		summary["engine"] = engine.Metadata()
	}
	summary["transport"] = summary["engine"].(map[string]any)["transport"]
	summary["scope"] = opts.Scope
	summary["local_fallback"] = false
	summary["caps"] = map[string]any{
		"chars": opts.ContextChars, "top_k": opts.RawLimit,
	}
	summary["data"] = map[string]any{
		"dir": dataDir, "tier": opts.Tier,
		"questions_file": tier.File, "questions_sha256": tier.SHA256,
		"questions_count": tier.Count, "scored_questions": len(questions),
		"corpus_file": manifest.Corpus.Path, "corpus_sha256": manifest.Corpus.SHA256,
		"corpus_rows": manifest.Corpus.Rows,
		"protocol":    manifest.Protocol,
	}
	summary["answerer"] = answerer.Describe()
	judgeMeta := map[string]any{"protocol": "off", "model": nil}
	judgeStats := judgeTotals(results)
	if judge != nil {
		judgeMeta = judge.Describe()
	}
	summary["judge"] = judgeMeta
	for key, value := range judgeStats {
		summary[key] = value
	}
	summary["model"] = answerer.Describe()["model"]
	summary["judge_model"] = judgeMeta["model"]
	if err := os.MkdirAll(opts.OutDir, 0o755); err != nil {
		return nil, err
	}
	if err := writeResults(filepath.Join(opts.OutDir, "results-"+arm+".jsonl"), results); err != nil {
		return nil, err
	}
	if err := writeJSON(filepath.Join(opts.OutDir, "summary-"+arm+".json"), summary); err != nil {
		return nil, err
	}
	if err := WriteMarkdown(summary, filepath.Join(opts.OutDir, "summary-"+arm+".md")); err != nil {
		return nil, err
	}
	return summary, nil
}

// locomoTurnSubUnits 把一轮对话中附带的多模态图像描述/观察信息组装成高分辨率块：
//   - 语料显式给出的结构化字段（image caption / observation / attachment 等解析件）各成一块；
//   - 正文内以方括号包裹的观察片段（如 `[shares a photo ...]`）按出现顺序各成一块。
//
// 纯形式切分：固定字段名 + 方括号边界，无语义词表、无字符数门控。
// 这些块与轮次同属一个父单元句帧，不另发子 UnitID（/units 与 corpus 仍一一相等）。
func locomoTurnSubUnits(docID, text string, row map[string]any, lineStart, lineEnd int) []ramesdk.SubUnit {
	var subs []ramesdk.SubUnit
	add := func(kind, raw string) {
		body := strings.TrimSpace(raw)
		if body == "" {
			return
		}
		id := fmt.Sprintf("%s#%s:%d", docID, kind, len(subs))
		subs = append(subs, ramesdk.SubUnit{
			Kind:      kind,
			ID:        id,
			BlockID:   id,
			Text:      body,
			FileParse: body,
			LineStart: lineStart,
			LineEnd:   lineEnd,
		})
	}
	for _, key := range []string{"image_caption", "blip_caption", "observation", "attachment", "file_parse"} {
		if row == nil {
			break
		}
		add(key, pyStr(firstTruthy(row[key], "")))
	}
	rest := text
	for {
		open := strings.IndexByte(rest, '[')
		if open < 0 {
			break
		}
		shut := strings.IndexByte(rest[open+1:], ']')
		if shut < 0 {
			break
		}
		add("observation", rest[open+1:open+1+shut])
		rest = rest[open+1+shut+1:]
	}
	return subs
}

type contextRow struct {
	question map[string]any
	context  string
	items    []Item
	counts   map[string]int
	seconds  float64
	unitIDs  []string
	err      error
}

func runQuestions(engine *CloudEngine, opts Options,
	questions []map[string]any, answerer Answerer, judge Judge,
	logf func(string, ...any)) ([]map[string]any, error) {
	results := make([]map[string]any, 0, len(questions))
	workers := maxInt(1, opts.Workers)
	for start := 0; start < len(questions); start += workers {
		end := start + workers
		if end > len(questions) {
			end = len(questions)
		}
		batch := questions[start:end]
		contexts := ParallelMap(len(batch), workers, func(index int) contextRow {
			return buildContext(opts, engine, batch[index], time.Now())
		})
		for _, row := range contexts {
			if row.err != nil {
				return nil, row.err
			}
		}
		predictions := make([]string, len(contexts))
		answerSeconds := make([]float64, len(contexts))
		answerErrors := make([]string, len(contexts))
		if _, isNone := answerer.(noneAnswerer); !isNone {
			answers := ParallelMap(len(contexts), workers, func(index int) string {
				begin := time.Now()
				answer, err := answerer.Answer(questionText(contexts[index].question), contexts[index].items)
				answerSeconds[index] = time.Since(begin).Seconds()
				if err != nil {
					answerErrors[index] = err.Error()
					logf("[eval] answer error (%v): %v", contexts[index].question["id"], err)
					return ""
				}
				return answer
			})
			copy(predictions, answers)
		}
		verdicts := make([]any, len(contexts))
		reasons := make([]any, len(contexts))
		judgeSeconds := make([]float64, len(contexts))
		if judge != nil {
			_ = ParallelMap(len(contexts), workers, func(index int) string {
				row := contexts[index]
				answer := predictions[index]
				if answerErrors[index] != "" {
					verdicts[index] = "error"
					reasons[index] = answerErrors[index]
					return ""
				}
				if answer == "" {
					verdicts[index] = "wrong"
					reasons[index] = "empty answer"
					return ""
				}
				begin := time.Now()
				verdict, err := judge.Judge(questionText(row.question), goldAnswer(row.question), answer)
				judgeSeconds[index] = time.Since(begin).Seconds()
				if err != nil {
					verdicts[index] = "unparsed"
					reasons[index] = err.Error()
					return ""
				}
				if verdict.Correct {
					verdicts[index] = "correct"
				} else {
					verdicts[index] = "wrong"
				}
				reasons[index] = verdict.Reason
				return ""
			})
		}
		for index, row := range contexts {
			gold := goldAnswer(row.question)
			evidence := evidenceTexts(row.question)
			f1 := TokenF1(predictions[index], gold)
			exact := NormalizeAnswer(predictions[index]) == NormalizeAnswer(gold)
			evidenceRecall := BestEvidenceRecall(evidence, row.context)
			evidenceHit := opts.Oracle || evidenceRecall >= 0.6
			goldRecall := TokenRecall(gold, row.context)
			answerInContext := goldRecall >= 0.8
			fullEvidenceRecall := BestFullEvidenceRecall(evidence, row.context)
			fullEvidenceHit := opts.Oracle || fullEvidenceRecall >= 0.6
			fullGoldRecall := FullTokenRecall(gold, row.context)
			failureBucket := ClassifyFailureBucket(
				fullEvidenceHit, answerInContext || fullGoldRecall >= 0.8, fullEvidenceRecall, f1, fullGoldRecall, pyStr(verdicts[index]),
			)
			transport := TransportForPulseMode(opts.PulseMode)
			if opts.Oracle {
				transport = "oracle-gold"
			}
			results = append(results, map[string]any{
				"id": row.question["id"], "sample_id": row.question["sample_id"],
				"category": row.question["category"], "question": row.question["question"],
				"gold": gold, "prediction": predictions[index], "context": row.context,
				"f1": PyRound(f1, 4), "exact": exact,
				"evidence_hit": evidenceHit, "full_evidence_hit": fullEvidenceHit,
				"answer_in_context":    answerInContext,
				"best_evidence_recall": PyRound(evidenceRecall, 4),
				"full_evidence_recall": PyRound(fullEvidenceRecall, 4),
				"answer_token_recall":  PyRound(fullGoldRecall, 4),
				"failure_bucket":       failureBucket, "judge": verdicts[index], "judge_reason": reasons[index],
				"context_chars": row.counts["context_chars"], "bands": 0, "nodes": 0,
				"raw": row.counts["raw"], "cloud_hits": row.counts["cloud_hits"],
				"retrieval_unit_ids": row.unitIDs, "engine_transport": transport,
				"engine_scope": opts.Scope, "local_fallback": false,
				"recall_seconds": PyRound(row.seconds, 4),
				"answer_seconds": PyRound(answerSeconds[index], 4),
				"judge_seconds":  PyRound(judgeSeconds[index], 4),
			})
		}
		done := start + len(batch)
		if opts.ProgressEach > 0 && (done%opts.ProgressEach < workers || done == len(questions)) {
			// 进度行走 logf（`--json` 时由 CLI 重定向到 stderr）：`--json` 的 stdout 必须是纯 JSON，
			// 否则调用方（Admin 的 verify/prepare 回执解析）会把进度行读成解析失败。
			logf("%s", progressLine(done, len(questions), results))
		}
	}
	return results, nil
}

func buildContext(opts Options, engine *CloudEngine, question map[string]any, started time.Time) contextRow {
	row := contextRow{question: question, counts: map[string]int{}}
	if opts.Oracle {
		evidence := evidenceTexts(question)
		row.context = TruncateRunes(joinLines(evidence), opts.ContextChars)
		row.items = Itemize(row.context, 0)
		row.counts = map[string]int{"raw": len(evidence), "context_chars": len([]rune(row.context))}
		row.seconds = time.Since(started).Seconds()
		return row
	}
	if engine == nil {
		row.err = errors.New("cloud evaluation requires an initialized SDK engine")
		return row
	}
	hits, err := engine.Recall(context.Background(), questionText(question), opts.RawLimit)
	if err != nil {
		row.err = err
		return row
	}
	lines := make([]string, 0, len(hits))
	row.unitIDs = make([]string, 0, len(hits))
	for _, hit := range hits {
		text := hit.DistilledPlaintext
		if strings.TrimSpace(text) == "" {
			text = hit.RawPlaintext
		}
		lines = append(lines, text)
		row.unitIDs = append(row.unitIDs, hit.UnitID)
	}
	row.context = TruncateRunes(joinLines(lines), opts.ContextChars)
	row.items = Itemize(row.context, 0)
	row.counts = map[string]int{
		"raw": len(hits), "cloud_hits": len(hits), "context_chars": len([]rune(row.context)),
	}
	row.seconds = time.Since(started).Seconds()
	return row
}

func buildProtocols(cfg *config.Config, opts Options) (Answerer, Judge, error) {
	var ansClient, jdgClient *ChatClient
	if opts.Answerer == "llm" || opts.Judge == "llm" {
		// Allow the evaluation grader harness itself to reach the configured [eval] endpoint
		// even when the memory engine under test is running in "zero" (0-egress) mode.
		evalCfg := *cfg
		if opts.BaseURL != "" || cfg.Eval.BaseURL != "" {
			evalCfg.Mode = "cloud"
		}
		var err error
		if opts.Answerer == "llm" {
			ansClient, err = NewChatClient(&evalCfg, ChatOptions{
				BaseURL: opts.BaseURL, Model: opts.Model, APIKey: opts.APIKey,
				Temperature: opts.Temperature, MaxTokens: opts.MaxTokens, TimeoutSec: opts.TimeoutSec,
				Thinking: opts.Thinking, ReasoningEffort: opts.ReasoningEffort,
			})
			if err != nil {
				return nil, nil, err
			}
		}
		if opts.Judge == "llm" {
			jdgClient, err = NewChatClient(&evalCfg, ChatOptions{
				BaseURL: opts.BaseURL, Model: opts.Model, APIKey: opts.APIKey,
				Temperature: opts.JudgeTemp, MaxTokens: opts.MaxTokens, TimeoutSec: opts.TimeoutSec,
			})
			if err != nil {
				return nil, nil, err
			}
		}
	}
	answerer, err := NewAnswerer(cfg, opts.Answerer, ansClient)
	if err != nil {
		return nil, nil, err
	}
	judge, err := NewJudge(cfg, opts.Judge, jdgClient)
	if err != nil {
		return nil, nil, err
	}
	return answerer, judge, nil
}

func judgeTotals(rows []map[string]any) map[string]any {
	correct, wrong, unparsed, errored := 0, 0, 0, 0
	for _, row := range rows {
		switch Str(row["judge"]) {
		case "correct":
			correct++
		case "wrong":
			wrong++
		case "unparsed":
			unparsed++
		case "error":
			// 答题侧传输/协议失败：与 JUDGE_UNPARSED 一样剔除出 scored（协议口径）。
			errored++
		}
	}
	scored := correct + wrong
	rate := 0.0
	if scored > 0 {
		rate = float64(correct) / float64(scored)
	}
	return map[string]any{
		"judge_correct":  correct,
		"judge_wrong":    wrong,
		"judge_unparsed": unparsed,
		"judge_error":    errored,
		"judge_scored":   scored,
		"judge_rate":     PyRound(rate, 4),
	}
}

func progressLine(done, total int, results []map[string]any) string {
	f1Values := make([]float64, 0, len(results))
	evidenceValues := make([]float64, 0, len(results))
	correct := 0
	scored := 0
	for _, row := range results {
		f1Values = append(f1Values, rowValue(row, "f1"))
		if Truthy(row["evidence_hit"]) {
			evidenceValues = append(evidenceValues, 1.0)
		} else {
			evidenceValues = append(evidenceValues, 0.0)
		}
		switch Str(row["judge"]) {
		case "correct":
			correct++
			scored++
		case "wrong":
			scored++
		}
	}
	line := fmt.Sprintf("[eval] %d/%d f1=%.3f ev=%.3f", done, total, Mean(f1Values), Mean(evidenceValues))
	if scored > 0 {
		line += fmt.Sprintf(" judge=%d/%d", correct, scored)
	}
	return line
}

// ArmModeForPulseMode 将脉冲模式映射为唯一合法检索臂标签。
func ArmModeForPulseMode(pulseMode string) string {
	switch NormalizePulseMode(pulseMode) {
	case PulseModePulsePlain:
		return "sdk-pulse-plain"
	case PulseModeLocalPlain:
		return "sdk-local-plain"
	default:
		return "sdk-pulse-cipher"
	}
}

// BuildArm 给一次运行起个可读的臂名（写盘文件名与报告用）。
func BuildArm(opts Options) string {
	arm := ArmModeForPulseMode(opts.PulseMode)
	if opts.Oracle {
		arm = "oracle"
	}
	answerer := answererName(opts)
	judge := judgeName(opts)
	name := arm + "-" + answerer
	if judge != "off" {
		name += "-" + judge
	}
	return name
}

func answererName(opts Options) string {
	if opts.Answerer == "llm" {
		return "llm"
	}
	return "none"
}

func judgeName(opts Options) string {
	if opts.Judge == "llm" {
		return "judge"
	}
	return "off"
}

// VerifyFile 校验随包数据件的 sha256（冻结输入的可审计性）。
func VerifyFile(path, want string) error {
	if want == "" {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if got := sha256Hex(raw); got != want {
		return fmt.Errorf("%s sha256 不符：manifest %s，实际 %s（数据件被改动；确认后可用 --no-verify 复核）",
			filepath.Base(path), want, got)
	}
	return nil
}

// LoadQuestions 读题集件并按 offset/limit 切片（standard 切片边界语义）。
func LoadQuestions(path string, offset, limit int) ([]map[string]any, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	document := map[string]any{}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&document); err != nil {
		return nil, err
	}
	items, ok := document["questions"].([]any)
	if !ok {
		return nil, fmt.Errorf("%s 没有 questions 列表", path)
	}
	questions := []map[string]any{}
	for _, item := range items {
		if row, ok := item.(map[string]any); ok {
			questions = append(questions, row)
		}
	}
	start := sliceStart(offset, len(questions))
	stop := sliceStop(start, limit, len(questions))
	if limit == 0 {
		// limit 0 = 整档全量（负值仍按 standard 切片语义从尾部截）。
		stop = len(questions)
	}
	return questions[start:stop], nil
}

func writeResults(path string, rows []map[string]any) error {
	handle, err := os.Create(path)
	if err != nil {
		return err
	}
	defer handle.Close()
	writer := json.NewEncoder(handle)
	writer.SetEscapeHTML(false)
	for _, row := range rows {
		encoded, err := json.Marshal(row)
		if err != nil {
			return err
		}
		if _, err := handle.Write(append(encoded, '\n')); err != nil {
			return err
		}
	}
	return handle.Sync()
}

func writeJSON(path string, value any) error {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	return os.WriteFile(path, encoded, 0o644)
}
