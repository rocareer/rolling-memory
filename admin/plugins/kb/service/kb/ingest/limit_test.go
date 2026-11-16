// plugin: kb v1.0.0
package ingest

import (
	"context"
	"errors"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// 来源上限与预算闸计价扩展。

func TestSourceLimitsDefaultsAndCheck(t *testing.T) {
	l := SourceLimits{}.WithDefaults()
	if l.MaxPages != DefaultMaxPages || l.MaxMinutes != DefaultMaxMinutes {
		t.Fatalf("缺省应 %d 页 / %v 分钟，实际 %+v", DefaultMaxPages, DefaultMaxMinutes, l)
	}
	// 显式值不被覆盖
	l = SourceLimits{MaxPages: 2, MaxMinutes: 0.5}.WithDefaults()
	if l.MaxPages != 2 || l.MaxMinutes != 0.5 {
		t.Fatalf("显式上限被误覆盖: %+v", l)
	}
	if s := l.Check(2, 0.5); s != nil {
		t.Fatalf("未超限不应挂起: %+v", s)
	}
	if s := l.Check(3, 0); s == nil || s.What != "pages" {
		t.Fatalf("超页数应挂起 pages: %+v", s)
	}
	if s := l.Check(1, 0.6); s == nil || s.What != "minutes" {
		t.Fatalf("超时长应挂起 minutes: %+v", s)
	}
	// 缺省档边界（500/120 恒过）
	if s := (SourceLimits{}).Check(500, 120); s != nil {
		t.Fatalf("缺省档 500 页 / 120 分钟应放行: %+v", s)
	}
	if s := (SourceLimits{}).Check(501, 0); s == nil || s.What != "pages" {
		t.Fatalf("501 页应挂起: %+v", s)
	}
}

func TestSourceSuspendedErrorCarriesSuspended(t *testing.T) {
	se := &SourceSuspendedError{Suspended: &BudgetSuspended{What: "pages", Value: 501, Limit: 500}}
	if !strings.Contains(se.Error(), "超限待批") || !strings.Contains(se.Error(), "pages") {
		t.Fatalf("挂起文案不符: %s", se.Error())
	}
	var target *SourceSuspendedError
	if !errors.As(error(se), &target) || target.Suspended.What != "pages" {
		t.Fatal("SourceSuspendedError 应可 As 判")
	}
}

// TestParseFFmpegDurationUnit Duration 行解析（纯单元；音视频前置闸的读数面）。
func TestParseFFmpegDurationUnit(t *testing.T) {
	cases := []struct {
		line string
		want float64
		ok   bool
	}{
		{"  Duration: 00:00:02.00, start: 0.000000, bitrate: 256 kb/s", 2.0 / 60, true},
		{"  Duration: 00:02:05.50, start: 0.0, bitrate: 64 kb/s", 125.5 / 60, true},
		{"  Duration: 01:00:00.00, start: 0.0, bitrate: 1 kb/s", 60, true},
		{"  Duration: N/A, start: 0.000000, bitrate: N/A", 0, false},
		{"Stream mapping: no duration here", 0, false},
	}
	for _, c := range cases {
		got, ok := parseFFmpegDuration(c.line)
		if ok != c.ok || (ok && (got < c.want-1e-6 || got > c.want+1e-6)) {
			t.Fatalf("Duration 解析不符（%q）: got=%v ok=%v want=%v wantOK=%v", c.line, got, ok, c.want, c.ok)
		}
	}
}

// countingASRDriver 计数用 ASR 驱动（闸前置断言）
type countingASRDriver struct{}

var countingASRDriverCalls int64 // 包内原子自增面（单测串行；atomic 包非必需）

func (countingASRDriver) Name() string { return "fake-asr-counting" }

func (countingASRDriver) ASRTranscribe(_ context.Context, _ []byte) (*ASRResult, error) {
	countingASRDriverCalls++
	return &ASRResult{Text: "[00:01] 甲: 计数用例\n", Minutes: 0.05}, nil
}

// TestAudioDurationGateBeforeASR 音视频时长闸**先于 ASR 付费步骤**（已整改：原实现等 ASR
// 返回 minutes 才判）。断言：超限（注入 0.01 分钟上限）⇒ SourceSuspendedError 且 ASR 驱动**零调用**；
// 未超限 ⇒ 链走到 ASR（计数驱动被调）。依赖真实 ffmpeg（合成 2s wav；工具缺失 ⇒ 如实跳过）。
func TestAudioDurationGateBeforeASR(t *testing.T) {
	if !commandAvailable(DefaultFFmpegBin) {
		t.Skip("ffmpeg 不在 PATH（环境如实）：本用例需真实容器读数")
	}
	dir := t.TempDir()
	wav := filepath.Join(dir, "fixture.wav")
	if out, err := exec.Command(DefaultFFmpegBin, "-nostdin", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=2", "-ac", "1", "-ar", "16000", wav).CombinedOutput(); err != nil {
		t.Skipf("ffmpeg 合成 wav 失败（环境如实）: %v %s", err, string(out))
	}
	data, err := os.ReadFile(wav)
	if err != nil {
		t.Fatalf("读 wav 失败: %v", err)
	}

	oldCaps := mediaCapsResolver
	mediaCapsResolver = func() MediaCaps {
		return MediaCaps{ASR: MediaChannelCaps{Enabled: true, Provider: "fake-asr-counting"}, FFmpegAvailable: true, FFmpegBin: DefaultFFmpegBin}
	}
	defer func() { mediaCapsResolver = oldCaps }()
	RegisterASRDriver(countingASRDriver{})
	defer delete(asrDrivers, "fake-asr-counting")

	oldLimits := sourceLimitsResolver
	sourceLimitsResolver = func() SourceLimits { return SourceLimits{MaxPages: 500, MaxMinutes: 0.01} }
	defer func() { sourceLimitsResolver = oldLimits }()

	before := countingASRDriverCalls
	if _, err := ReadFormatCtx(context.Background(), FormatAudio, data); err == nil {
		t.Fatal("超时长应挂起（SourceSuspendedError）")
	} else {
		var se *SourceSuspendedError
		if !errors.As(err, &se) {
			t.Fatalf("应为 SourceSuspendedError（挂起待批），实际: %v", err)
		}
	}
	if got := countingASRDriverCalls; got != before {
		t.Fatalf("闸必须在 ASR 之前：ASR 调用次数 %d → %d", before, got)
	}

	sourceLimitsResolver = func() SourceLimits { return SourceLimits{} } // 缺省档（120 分钟）放行
	if _, err := ReadFormatCtx(context.Background(), FormatAudio, data); err != nil {
		t.Fatalf("未超限应放行到 ASR: %v", err)
	}
	if got := countingASRDriverCalls; got != before+1 {
		t.Fatalf("未超限时 ASR 应被调用一次：%d → %d", before, got)
	}
}

func TestEstimateMediaCostCNY(t *testing.T) {
	l := BudgetLimits{OCRPricePerPageCNY: 0.01, ASRPricePerMinuteCNY: 0.5}
	if got := EstimateMediaCostCNY(3, 2, l); math.Abs(got-1.03) > 1e-9 {
		t.Fatalf("媒体计价应 3×0.01+2×0.5=1.03，实际 %v", got)
	}
	// 未配价按 0 计（不虚构价格）
	if got := EstimateMediaCostCNY(10, 5, BudgetLimits{}); got != 0 {
		t.Fatalf("未配价应 0，实际 %v", got)
	}
	// 负值收敛 0
	if got := EstimateMediaCostCNY(-1, -1, l); got != 0 {
		t.Fatalf("负量应 0，实际 %v", got)
	}
}

func TestCheckBudgetIncludesMediaCost(t *testing.T) {
	l := BudgetLimits{MaxChunksPerJob: 1, OCRPricePerPageCNY: 0.1, ASRPricePerMinuteCNY: 1}
	in := BudgetInput{Docs: 1, Chunks: 2, Chars: 100, CJK: 100, Pages: 5, Minutes: 2}
	s, err := CheckBudget(in, l)
	if err != nil || s == nil || s.What != "chunks" {
		t.Fatalf("应挂起 chunks: %v %v", s, err)
	}
	// 媒体计价入读数：5×0.1+2×1 = 2.5
	if math.Abs(s.MediaCost-2.5) > 1e-9 {
		t.Fatalf("MediaCost 应 2.5，实际 %v", s.MediaCost)
	}
	if !strings.Contains(s.Error(), "媒体计价") {
		t.Fatalf("挂起文案应带媒体计价子读数: %s", s.Error())
	}
	// 无媒体量 ⇒ MediaCost=0 且文案不带子读数
	noMedia, _ := CheckBudget(BudgetInput{Docs: 1, Chunks: 2, CJK: 10}, BudgetLimits{MaxChunksPerJob: 1})
	if noMedia == nil || noMedia.MediaCost != 0 || strings.Contains(noMedia.Error(), "媒体计价") {
		t.Fatalf("无媒体量不应带媒体读数: %+v", noMedia)
	}
}
