// plugin: rolling-memory v0.0.58
// eval_export.go —— 评测运行「复现包」导出端点（/eval/export）。
//
// 面向两类读者一次成包：
//   - ATM-Bench 官方 / 外部复核者：自包含 Markdown 报告（官方 gold-set-mean 指标表、
//     与封印基线的配对差值、复现命令、语料 SHA-256 Pin、环境指纹）；
//   - 机器消费方：同内容的结构化 JSON（run 记录 + 基线 + 逐题明细），可直接入
//     paper/reproduce 或外部审计管线。
//
// 只读端点：仅读测评任务表与 manifest，绝不触发任何评测阶段。
package rollingmemory

import (
	"fmt"
	"net/http"
	"os"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/ai-go-hub/ai-go-admin/internal/kit/httpx"
	"github.com/ai-go-hub/ai-go-admin/internal/version"
	"github.com/gin-gonic/gin"
)

type evalExportRequest struct {
	Suite  string `json:"suite"`
	RunID  string `json:"run_id"`
	Format string `json:"format"` // markdown（默认）| json
}

// evalExportHandler 组装一次运行的复现包。run_id 为空时取该套件最近一次实测运行。
func evalExportHandler(c *gin.Context) {
	var req evalExportRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Fail(c, httpx.WithMessage("参数错误: "+err.Error()))
		return
	}
	suite := normalizeSuite(req.Suite)
	history := loadRunHistoryBySuite(suite)
	var run *EvalRunRecord
	wantID := strings.TrimSpace(req.RunID)
	for i := range history {
		if (wantID == "" && !history[i].IsBaseline) || history[i].ID == wantID {
			run = &history[i]
			break
		}
	}
	if run == nil {
		httpx.Fail(c, httpx.WithCode(http.StatusNotFound), httpx.WithMessage("该套件尚无可导出的实测运行（run_id="+wantID+"）"))
		return
	}

	baseline := matchExportBaseline(suite, run.Tier)
	home, _ := os.UserHomeDir()
	// run.Arm 是落档展示串（含 k= 尾注），非规范臂枚举：按子串判定脉冲模式取对应隔离库。
	pulseMode := EvalPulseModePulseCipher
	armLower := strings.ToLower(run.Arm)
	switch {
	case strings.Contains(armLower, "local-plain") || strings.Contains(armLower, "local_plain"):
		pulseMode = EvalPulseModeLocalPlain
	case strings.Contains(armLower, "pulse-plain") || strings.Contains(armLower, "pulse_plain"):
		pulseMode = EvalPulseModePulsePlain
	}
	storePath := resolveEvalDBPathForSuiteAndMode(home, suite, pulseMode)
	corpusSHA := manifestCorpusDigest(storePath)
	var manifest EvalCloudManifest
	if m, err := readCloudEvalManifest(storePath, suite); err == nil {
		manifest = m
	}
	items, _ := loadEvalTaskItems(run.ID)

	payload := gin.H{
		"generated_at": time.Now().Format(time.RFC3339),
		"suite":        suite,
		"run":          run,
		"baseline":     baseline,
		"delta":        exportDeltaRows(run, baseline),
		"environment": gin.H{
			"software_version": version.Get().Version,
			"go_version":       runtime.Version(),
			"os_arch":          runtime.GOOS + "/" + runtime.GOARCH,
			"corpus_sha256":    corpusSHA,
			"scope":            manifest.Scope,
			"documents":        manifest.Documents,
			"transport":        manifest.Transport,
		},
		"items": items,
	}
	if strings.EqualFold(strings.TrimSpace(req.Format), "json") {
		httpx.Success(c, httpx.WithData(payload))
		return
	}
	payload["markdown"] = renderEvalReproMarkdown(suite, run, baseline, manifest, corpusSHA, len(items))
	httpx.Success(c, httpx.WithData(payload))
}

// matchExportBaseline 取同套件同题集档位的封印基线（我方实测行优先），供配对差值。
func matchExportBaseline(suite, tier string) *EvalRunRecord {
	var pool []EvalRunRecord
	if suite == SuiteATM {
		pool = atmReferenceBaselines()
	} else {
		pool = referenceBaselines()
	}
	for i := range pool {
		if pool[i].Tier == tier && strings.Contains(pool[i].ID, "-rm") {
			return &pool[i]
		}
	}
	for i := range pool {
		if pool[i].Tier == tier {
			return &pool[i]
		}
	}
	return nil
}

type exportDeltaRow struct {
	Metric   string  `json:"metric"`
	Run      float64 `json:"run"`
	Baseline float64 `json:"baseline"`
	DeltaPP  float64 `json:"delta_pp"` // 百分点差（run - baseline）×100
}

// exportDeltaRows 产出官方指标的配对差值行（基线缺位时 baseline/delta 置 0 并由调用方注记）。
func exportDeltaRows(run, baseline *EvalRunRecord) []exportDeltaRow {
	get := func(r *EvalRunRecord, pick func(*EvalRunRecord) float64) float64 {
		if r == nil {
			return 0
		}
		return pick(r)
	}
	metrics := []struct {
		name string
		pick func(*EvalRunRecord) float64
	}{
		{"R@10 (gold-set-mean)", func(r *EvalRunRecord) float64 { return r.R10 }},
		{"R@20", func(r *EvalRunRecord) float64 { return r.R20 }},
		{"R@50", func(r *EvalRunRecord) float64 { return r.R50 }},
		{"Any-Hit@10", func(r *EvalRunRecord) float64 { return r.AnyHit10 }},
		{"All-Hit@10", func(r *EvalRunRecord) float64 { return r.AllHit10 }},
		{"Hybrid QS / Judge", func(r *EvalRunRecord) float64 { return r.JudgeRate }},
	}
	rows := make([]exportDeltaRow, 0, len(metrics))
	for _, m := range metrics {
		rv, bv := get(run, m.pick), get(baseline, m.pick)
		rows = append(rows, exportDeltaRow{
			Metric: m.name, Run: roundFloat(rv, 4), Baseline: roundFloat(bv, 4),
			DeltaPP: roundFloat((rv-bv)*100, 2),
		})
	}
	return rows
}

// renderEvalReproMarkdown 生成自包含复现报告（ATM 官方评测器口径）。
func renderEvalReproMarkdown(suite string, run, baseline *EvalRunRecord, manifest EvalCloudManifest, corpusSHA string, itemCount int) string {
	var b strings.Builder
	suiteName := "LoCoMo"
	if suite == SuiteATM {
		suiteName = "ATM-Bench"
	}
	fmt.Fprintf(&b, "# %s Reproducibility Report · %s\n\n", suiteName, run.ID)
	fmt.Fprintf(&b, "- Generated: %s · Software v%s · %s/%s · Go %s\n", time.Now().Format("2006-01-02 15:04:05"), version.Get().Version, runtime.GOOS, runtime.GOARCH, runtime.Version())
	fmt.Fprintf(&b, "- Run: **%s** · Tier `%s` · Arm `%s` · TopK %d · %d scored questions · %.1fs end-to-end\n", run.Label, run.Tier, run.Arm, run.TopK, run.ScoredQuestions, run.ElapsedSeconds)
	fmt.Fprintf(&b, "- Corpus: scope `%s` · %d documents · SHA-256 `%s`\n\n", manifest.Scope, manifest.Documents, corpusSHA)

	b.WriteString("## Official Metrics (gold-set-mean) vs Sealed Baseline\n\n")
	b.WriteString("| Metric | This Run | Baseline | Δ (pp) |\n|---|---:|---:|---:|\n")
	for _, row := range exportDeltaRows(run, baseline) {
		mark := ""
		if row.DeltaPP > 0 {
			mark = " ↑"
		} else if row.DeltaPP < 0 {
			mark = " ↓"
		}
		fmt.Fprintf(&b, "| %s | %.2f%% | %.2f%% | %+.2f%s |\n", row.Metric, row.Run*100, row.Baseline*100, row.DeltaPP, mark)
	}
	if baseline != nil {
		fmt.Fprintf(&b, "\nBaseline: `%s`（%s）\n", baseline.ID, baseline.Label)
	} else {
		b.WriteString("\nBaseline: none for this tier (delta column reads as absolute).\n")
	}

	b.WriteString("\n## Performance Telemetry\n\n")
	fmt.Fprintf(&b, "- Retrieval latency: mean %.1f ms · P95 %.1f ms\n", run.AvgRecallMS, run.P95RecallMS)
	if run.IngestMS > 0 {
		fmt.Fprintf(&b, "- Corpus ingest + index build: %.1f s\n", float64(run.IngestMS)/1000)
	}
	fmt.Fprintf(&b, "- Delivered payload: %.1f items / %d tokens per question (tokenz-r)\n", run.AvgDeliveredItems, run.AvgDeliveredTokens)

	if len(run.BucketCounts) > 0 {
		b.WriteString("\n## Failure Bucket Attribution\n\n")
		keys := make([]string, 0, len(run.BucketCounts))
		for k := range run.BucketCounts {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, "- %s: %d\n", k, run.BucketCounts[k])
		}
	}

	b.WriteString("\n## Reproduce\n\n")
	fmt.Fprintf(&b, "```bash\n%s\n```\n", run.ReproCLI)
	fmt.Fprintf(&b, "\nPer-question details: %d items archived in the admin ledger (`/eval/run` mode=detail · run_id=%s).\n", itemCount, run.ID)
	fmt.Fprintf(&b, "\nScoring formula (official gold-set-mean): R@k = mean over questions of |set(GT) ∩ set(Top-k)| / |set(GT)|.\n")
	return b.String()
}
