// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/rocareer/rolling-memory/internal/eval"
)

// fetchSummaryView 是 ATM/LoCoMo 两次取数回执的共同视图（供终端摘要打印）。
type fetchSummaryView struct {
	Dataset        string
	Target         string
	Revision       string
	License        string
	Files          []eval.FetchATMFileReport
	CorpusSHA256   string
	TierSHA256     map[string]string
	ProvenancePath string
}

// doEvalFetch 执行 `rmem eval --fetch <atm|locomo>`：
// 上游下载 → 本地重建 pinned/官方形态 → 摘要校验 → 就位。
//
// 许可确认：下载前必须显式给出 --accept-upstream-license（确定性确认，不做交互探测）；
// 缺该旗标时打印上游数据许可与取得方式，并以错误提示补上旗标。
func (a *application) doEvalFetch() (any, *fetchSummaryView, error) {
	fetch := strings.ToLower(strings.TrimSpace(a.args.String("fetch")))
	log := evalLogWriter(a.args.Flag("json"), a.printf, func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, format+"\n", args...)
	})
	accept := a.args.Flag("accept_upstream_license")
	revision := a.args.String("fetch_revision")
	switch fetch {
	case "atm":
		target := eval.ResolveATMFetchTarget(a.args.String("eval_data"))
		if target == "" {
			return nil, nil, errors.New("无法确定取数目录：请用 --eval-data <dir> 指定，或设置 ROLLING_MEMORY_ATM_DATA")
		}
		report, err := eval.FetchATMUpstream(context.Background(), eval.FetchATMOptions{
			TargetDir:     target,
			Revision:      revision,
			AcceptLicense: accept,
			Log:           log,
		})
		if errors.Is(err, eval.ErrUpstreamLicenseNotAccepted) {
			log("%s", eval.UpstreamATMNotice())
			return nil, nil, fmt.Errorf("%w：请加 --accept-upstream-license 表示已阅读并接受上游数据许可", err)
		}
		if err != nil {
			return nil, nil, err
		}
		return report, &fetchSummaryView{
			Dataset:        report.DatasetID + " @ " + shortDigest(report.Revision),
			Target:         report.TargetDir,
			Revision:       report.Revision,
			License:        report.License,
			Files:          report.Files,
			CorpusSHA256:   report.CorpusSHA256,
			TierSHA256:     report.TierSHA256,
			ProvenancePath: report.ProvenancePath,
		}, nil
	case "locomo":
		target := eval.ResolveLoCoMoFetchTarget(a.args.String("eval_data"))
		if target == "" {
			return nil, nil, errors.New("无法确定取数目录：请用 --eval-data <dir> 指定，或设置 ROLLING_MEMORY_EVAL_DATA")
		}
		report, err := eval.FetchLoCoMoUpstream(context.Background(), eval.FetchLoCoMoOptions{
			TargetDir:     target,
			Revision:      revision,
			AcceptLicense: accept,
			Log:           log,
		})
		if errors.Is(err, eval.ErrUpstreamLicenseNotAccepted) {
			log("%s", eval.UpstreamLoCoMoNotice())
			return nil, nil, fmt.Errorf("%w：请加 --accept-upstream-license 表示已阅读并接受上游数据许可", err)
		}
		if err != nil {
			return nil, nil, err
		}
		return report, &fetchSummaryView{
			Dataset:        report.Repo + " @ " + shortDigest(report.Revision),
			Target:         report.TargetDir,
			Revision:       report.Revision,
			License:        report.License,
			Files:          report.Files,
			CorpusSHA256:   report.CorpusSHA256,
			TierSHA256:     report.TierSHA256,
			ProvenancePath: report.ProvenancePath,
		}, nil
	default:
		return nil, nil, fmt.Errorf("未知的 --fetch 目标 %q（支持：atm、locomo）", a.args.String("fetch"))
	}
}

func (a *application) printFetchSummary(view *fetchSummaryView) {
	a.printf("上游语料就绪：%s\n", view.Target)
	a.printf("  数据集：%s\n", view.Dataset)
	a.printf("  数据许可：%s\n", view.License)
	downloaded := 0
	for _, f := range view.Files {
		if f.Downloaded {
			downloaded++
		}
	}
	a.printf("  文件：%d 个（本次下载 %d 个，其余为本地已有且哈希一致）\n", len(view.Files), downloaded)
	a.printf("  重建语料 corpus=%s\n", shortDigest(view.CorpusSHA256))
	keys := make([]string, 0, len(view.TierSHA256))
	for rel := range view.TierSHA256 {
		keys = append(keys, rel)
	}
	sort.Strings(keys)
	for _, rel := range keys {
		a.printf("           %s=%s\n", rel, shortDigest(view.TierSHA256[rel]))
	}
	a.printf("  来源记录：%s\n", view.ProvenancePath)
}

func shortDigest(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= 12 {
		return value
	}
	return value[:12]
}
