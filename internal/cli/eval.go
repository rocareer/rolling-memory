// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/rocareer/rolling-memory/internal/config"
	"github.com/rocareer/rolling-memory/internal/eval"
)

// runEval 跑随包评测（`rmem eval`）。
//
// 三条纪律：
//  1. **评测引擎**：非 Oracle 评测只经公共 SDK 与本地明文金库（或远端 /open/memory/v1），禁止本地断言图与静默回退；
//  2. **专属隔离**：明文写入独立 JSON Vault，云端写入租户专属评测 scope，绝不碰用户默认记忆空间；
//  3. **冻结协议**：答题 / 判官使用随包冻结提示词协议（sha1 钉在 internal/eval/frozen.go），不可配置。
func (a *application) runEval() int {
	cfg, err := config.Load(config.LoadOptions{
		Path: a.args.StringPtr("config"),
	})
	if err != nil {
		return a.fail(err)
	}
	stateDir := config.DefaultStateDir()
	outDir := a.args.String("out_dir")
	if outDir == "" {
		outDir = filepath.Join(stateDir, "eval", "out")
	}
	// 子命令分发（只读台账）：`jobs` / `status` 只读 out-dir 已落盘产物（jobs/<id>/job.json 与
	// summary-*.json），绝不触发任何评测阶段——此前任意 stage 名称都会静默落入全量管线，
	// `rmem eval jobs` 实际会跑完整评测（本批修复的 CLI 契约缺口）。
	if sub := a.args.Subcommand(); sub == "jobs" || sub == "status" {
		jobs, err := eval.ListJobs(outDir)
		if err != nil {
			return a.fail(err)
		}
		jobArg := strings.TrimSpace(a.args.String("job"))
		if sub == "status" {
			selected := eval.SelectJob(jobs, jobArg)
			if selected == nil {
				return a.fail(fmt.Errorf("no evaluation job found under %s (job=%q)", outDir, jobArg))
			}
			result := map[string]any{"ok": true, "out_dir": outDir, "job": selected}
			if err := a.emit(result, func() {
				a.printf("job %s · suite=%s tier=%s state=%s · updated %s\n",
					selected.JobID, selected.Suite, selected.Tier, selected.State, selected.UpdatedAt)
			}); err != nil {
				return a.fail(err)
			}
			return 0
		}
		filtered := eval.FilterJobs(jobs, jobArg)
		result := map[string]any{"ok": true, "out_dir": outDir, "count": len(filtered), "jobs": filtered}
		if err := a.emit(result, func() {
			if len(filtered) == 0 {
				a.printf("no evaluation jobs recorded under %s\n", outDir)
				return
			}
			for _, job := range filtered {
				a.printf("%-40s state=%-10s suite=%-7s tier=%-6s updated=%s\n",
					job.JobID, job.State, job.Suite, job.Tier, job.UpdatedAt)
			}
		}); err != nil {
			return a.fail(err)
		}
		return 0
	}
	suite := firstNonEmptyString(a.args.String("suite"), eval.SuiteLoCoMo)
	// `--fetch atm` 取数并本地重建后继续本次评测；`--fetch-only` 只取数不跑评测。
	if fetchTarget := strings.TrimSpace(a.args.String("fetch")); fetchTarget != "" {
		if strings.EqualFold(fetchTarget, "atm") && !a.args.provided["suite"] {
			suite = eval.SuiteATM
		}
		report, view, err := a.doEvalFetch()
		if err != nil {
			return a.fail(err)
		}
		if a.args.Flag("fetch_only") {
			if err := a.emit(report, func() { a.printFetchSummary(view) }); err != nil {
				return a.fail(err)
			}
			return 0
		}
	} else if a.args.Flag("fetch_only") {
		return a.fail(errors.New("--fetch-only 需要同时指定 --fetch atm"))
	}
	pulseMode := eval.NormalizePulseMode(firstNonEmptyString(a.args.String("pulse_mode"), os.Getenv("ROLLING_MEMORY_EVAL_PULSE_MODE"), eval.PulseModeLocalPlain))
	storePath := a.args.String("eval_store")
	if storePath == "" {
		baseName := "eval-locomo"
		if suite == eval.SuiteATM {
			baseName = "eval-atm"
		}
		switch pulseMode {
		case eval.PulseModePulsePlain:
			baseName += "-pulse-plain"
		case eval.PulseModeLocalPlain:
			baseName += "-local-plain"
		}
		storePath = filepath.Join(stateDir, "eval", baseName+".json")
	}
	tier := firstNonEmptyString(a.args.String("tier"), eval.Tier1540)
	if suite == eval.SuiteATM && !a.args.provided["tier"] {
		tier = eval.TierATMHard
	}
	rawLimit := a.args.Int("raw_limit")
	if !a.args.provided["raw_limit"] || rawLimit <= 0 {
		if suite == eval.SuiteATM {
			rawLimit = 50
		} else {
			// LoCoMo 默认交付窗口 = 0–96 items（本地渲染上限 24576 字符 ≈ TokenTier8192）：
			// 96 条交付窗口是当前实测证据命中 100% 的档位，低于它会截断多跳题的相关轮次。
			rawLimit = 96
		}
	}
	contextChars := intOr(a.args.Int("context_chars"), cfg.Recall.Chars)
	if !a.args.provided["context_chars"] && suite != eval.SuiteATM && contextChars < 24576 {
		contextChars = 24576
	}
	opts := eval.Options{
		Suite:           suite,
		DataDir:         a.args.String("eval_data"),
		Tier:            tier,
		Limit:           intOr(a.args.Int("limit"), 0),
		Offset:          a.args.Int("offset"),
		StorePath:       storePath,
		OutDir:          outDir,
		Rebuild:         a.args.Flag("rebuild"),
		ReusePrepare:    a.args.Flag("reuse_prepare"),
		VerifyPrepare:   a.args.Flag("verify_prepare"),
		Reader:          cfg.Reader,
		Oracle:          a.args.Flag("oracle"),
		Answerer:        firstNonEmptyString(a.args.String("answerer"), cfg.Eval.Answerer),
		Judge:           firstNonEmptyString(a.args.String("judge"), cfg.Eval.Judge),
		Model:           firstNonEmptyString(a.args.String("eval_model"), cfg.Eval.Model),
		BaseURL:         firstNonEmptyString(a.args.String("eval_base_url"), cfg.Eval.BaseURL),
		Temperature:     cfg.Eval.AnswerTemp,
		JudgeTemp:       cfg.Eval.JudgeTemp,
		Thinking:        cfg.Eval.Thinking,
		ReasoningEffort: cfg.Eval.ReasoningEffort,
		MaxTokens:       intOr(a.args.Int("max_tokens"), cfg.Eval.MaxTokens),
		TimeoutSec:      intOr(a.args.Int("timeout"), int(cfg.Eval.TimeoutSec)),
		Workers:         intOr(a.args.Int("workers"), cfg.Eval.Workers),
		ContextChars:    contextChars,
		RawLimit:        rawLimit,
		CorpusLimit:     intOr(a.args.Int("corpus_limit"), 0),
		ProgressEach:    intOr(a.args.Int("progress_every"), 20),
		DenoiseRatio:    a.args.Float("denoise_ratio"),
		NoVerify:        a.args.Flag("no_verify"),
		PulseTokenizer:  firstNonEmptyString(a.args.String("pulse_tokenizer"), eval.PulseTokenizerFTS5BM25),
		PulseMode:       pulseMode,
		Log: evalLogWriter(a.args.Flag("json"), a.printf, func(format string, args ...any) {
			fmt.Fprintf(os.Stderr, format+"\n", args...)
		}),
	}
	if opts.Answerer == "llm" || opts.Judge == "llm" {
		key, err := cfg.EvalKey()
		if err != nil {
			return a.fail(err)
		}
		opts.APIKey = key
	}
	summary, err := eval.Run(cfg, opts)
	if err != nil {
		return a.fail(err)
	}
	if err := a.emit(summary, func() { a.printEvalSummary(opts, summary) }); err != nil {
		return a.fail(err)
	}
	return 0
}

func (a *application) printEvalSummary(opts eval.Options, summary map[string]any) {
	a.printf("评测完成：suite=%s tier=%s 题数=%v 臂=%v\n", firstNonEmptyString(opts.Suite, eval.SuiteLoCoMo), opts.Tier, summary["n"], summary["arm"])
	for _, key := range []string{
		"r_at_10", "r_at_25", "r_at_50", "qs_score",
		// 分母必须成对出现：qs_score 是已评分题分母（历史口径），未判/失败题会把该值
		// 拉离全量分母；两个读数一起打印，防止只引用头条分数造成选择性偏高分。
		"qs_score_all_denominator", "scored_count", "unscored_count",
		"evidence_hit", "answer_in_context", "f1", "judge_rate", "judge_correct", "judge_scored",
		"judge_unparsed", "avg_context_chars", "elapsed_seconds",
	} {
		if value, ok := summary[key]; ok {
			a.printf("  %-20s %v\n", key, value)
		}
	}
	a.printf("  %-20s %v\n", "transport", summary["transport"])
	a.printf("  %-20s %v\n", "scope", summary["scope"])
	a.printf("结果目录：%s\n", opts.OutDir)
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// evalLogWriter 保证 --json 时 stdout 只包含机器可解析的 JSON：
// 进度/诊断日志改走 stderr。rmem eval --json 的调用方（Admin 的 verify-prepare 回执解析）
// 直接 Unmarshal stdout，任何 "[eval] ..." 前缀都会让整份回执解析失败。
func evalLogWriter(jsonMode bool, stdout, stderr func(string, ...any)) func(string, ...any) {
	if !jsonMode {
		return func(format string, args ...any) { stdout(format+"\n", args...) }
	}
	return func(format string, args ...any) { stderr(format+"\n", args...) }
}

func intOr(value, fallback int) int {
	if value != 0 {
		return value
	}
	return fallback
}
