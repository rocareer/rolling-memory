// plugin: kb v1.0.0
package ingest

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// 媒体工具执行预算（工作量自适应）专测：原为固定 180s 硬编码 ⇒
// 长音视频/多页 pdf「每次必超、永久失败」（作业重试无益）。
// 现行口径：预算 = 基数（kb.media.timeout_seconds，缺省 180s）+ 工作量 × 比例
// （媒体按分钟 30s / pdf 按页 2s）；工作量上限由 kb.limits（120 分钟 / 500 页）约束 ⇒ 预算有界。

// TestMediaBudgetDefaultsAndScaling 缺省档（单测环境无嵌入配置 ⇒ 代码缺省）与随量放大
func TestMediaBudgetDefaultsAndScaling(t *testing.T) {
	b := mediaBudgetResolver()
	if got := b.Base(); got != DefaultMediaBaseTimeout {
		t.Fatalf("基数缺省应为 %s，实际 %s", DefaultMediaBaseTimeout, got)
	}
	if got := b.ForMinutes(0); got != DefaultMediaBaseTimeout {
		t.Fatalf("0 分钟应落基数档: %s", got)
	}
	if got := b.ForMinutes(10); got != DefaultMediaBaseTimeout+10*DefaultMediaPerMinute {
		t.Fatalf("10 分钟预算错误: %s", got)
	}
	// 上限档 = kb.limits 缺省 120 分钟（未知工作量的取值口径）
	if got, want := b.ForMinutes(DefaultMaxMinutes), DefaultMediaBaseTimeout+time.Duration(DefaultMaxMinutes)*DefaultMediaPerMinute; got != want {
		t.Fatalf("上限档（%v 分钟）预算错误: got=%s want=%s", float64(DefaultMaxMinutes), got, want)
	}
	if got := b.ForPages(0); got != DefaultMediaBaseTimeout {
		t.Fatalf("0 页应落基数档: %s", got)
	}
	if got := b.ForPages(500); got != DefaultMediaBaseTimeout+500*DefaultMediaPerPDFPage {
		t.Fatalf("500 页预算错误: %s", got)
	}
	// 负工作量/页数按 0 计（防御，不产生负预算）
	if got := b.ForMinutes(-5); got != DefaultMediaBaseTimeout {
		t.Fatalf("负分钟应落基数档: %s", got)
	}
	if got := b.ForPages(-1); got != DefaultMediaBaseTimeout {
		t.Fatalf("负页数应落基数档: %s", got)
	}
}

// TestMediaBudgetPinnedAgainstHardCap 核心断言：长媒体/多页不再被固定常量截断
// （旧 180s 硬限对 120 分钟媒体/500 页 pdf 必超；新预算须显著大于基数）
func TestMediaBudgetPinnedAgainstHardCap(t *testing.T) {
	b := mediaBudgetResolver()
	long := b.ForMinutes(DefaultMaxMinutes) // 120 分钟媒体（来源上限内的最长档）
	if long <= DefaultMediaBaseTimeout {
		t.Fatalf("长媒体预算未随量放大（仍被基数档封顶）: %s", long)
	}
	many := b.ForPages(DefaultMaxPages) // 500 页 pdf（来源上限内的最大档）
	if many <= DefaultMediaBaseTimeout {
		t.Fatalf("多页 pdf 预算未随页数放大（仍被基数档封顶）: %s", many)
	}
}

// TestRunMediaToolBudgetEnforced 预算真实生效：预算 60ms + 长睡进程 ⇒ 超时报错且文案含生效预算
func TestRunMediaToolBudgetEnforced(t *testing.T) {
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("环境无 sleep（环境如实跳过）")
	}
	start := time.Now()
	_, err := runMediaTool(context.Background(), "sleep", 60*time.Millisecond, "5")
	if err == nil {
		t.Fatal("超预算应响亮失败")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("预算未生效（耗时 %s）", elapsed)
	}
	if !strings.Contains(err.Error(), "执行超时") || !strings.Contains(err.Error(), "60ms") {
		t.Fatalf("错误文案应含生效预算: %v", err)
	}
}

// TestRunMediaToolZeroBudgetFallsBackToBase 预算缺省档（0）⇒ 回退基数（不静默无界）
func TestRunMediaToolZeroBudgetFallsBackToBase(t *testing.T) {
	if toolBudget(0) != mediaBudgetResolver().Base() {
		t.Fatalf("0 预算应回退基数档: %s", toolBudget(0))
	}
	if toolBudget(-time.Second) != mediaBudgetResolver().Base() {
		t.Fatalf("负预算应回退基数档: %s", toolBudget(-time.Second))
	}
	explicit := 42 * time.Second
	if toolBudget(explicit) != explicit {
		t.Fatalf("显式预算被改写: %s", toolBudget(explicit))
	}
}
