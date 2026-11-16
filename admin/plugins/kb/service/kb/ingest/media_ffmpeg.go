// plugin: kb v1.0.0
// 媒体工具包装（ffmpeg 音轨提取/关键帧抽取、pdftoppm 光栅化）。
//
// 实跑型（非脚手架）：单测在工具存在时真实执行（lavfi 合成素材 → 抽取 → 校验），
// 工具缺失则 t.Skip（环境如实，不伪造产物）；工具二进制路径由插件配置 media_ffmpeg_bin 给出。
// 生产链路的**可达性**由渠道决定（见 media.go）：工具就位而渠道缺失 ⇒ 链停在
// ocr/asr/caption_channel_unavailable；工具缺失 ⇒ *_missing 原因码。两组原因码分列，便于运维定位。
//
// 口径：
//   - 输入统走临时文件（ffmpeg 对不可 seek 的管道输入兼容性差；一次性目录随用随清）；
//   - 单次执行预算 = MediaBudget（工作量自适应：基数 + 媒体分钟/页数 × 比例，配置源 kb.media.timeout_*；
//     原为固定 180s 硬编码，长媒体/多页 pdf 会「每次必超 ⇒ 永久失败」）；
//     超时=响亮失败（文案带生效预算；作业退避重试可重入——产物键幂等由上层承担）；
//   - 抽帧硬帽 MaxMediaFrames（防超长视频写爆磁盘）；帧时间戳按均匀步长近似（i*step；
//     精确取法待读数需要时接 ffprobe——不引入无消费者的依赖）。
package ingest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ai-go-hub/ai-go-admin/plugins/kb/kbcfg"
)

const (
	// DefaultMediaBaseTimeout 单次外部媒体工具执行基数（config kb.media.timeout_seconds；含进程启动/固定开销）
	DefaultMediaBaseTimeout = 180 * time.Second
	// DefaultMediaPerMinute 每媒体分钟追加额度（config kb.media.timeout_per_media_minute_seconds；音轨/抽帧）
	DefaultMediaPerMinute = 30 * time.Second
	// DefaultMediaPerPDFPage pdf 光栅化每页追加额度（config kb.media.timeout_per_page_seconds）
	DefaultMediaPerPDFPage = 2 * time.Second
	// MaxMediaFrames 抽帧硬帽（帧数×单帧字节 ≈ 临时目录与后续 caption 成本上限）
	MaxMediaFrames = 5000
	// rasterizeDPI pdf 光栅化分辨率（OCR 精度/体积权衡；读数后可调）
	rasterizeDPI = 150
	// maxRasterPages 光栅化硬帽（护栏；业务上限 = kb.limits.max_pages，链内在光栅化前先行检查）
	maxRasterPages = 1000
)

// MediaBudget 单次媒体工具执行预算（工作量自适应）：预算 = 基数 + 工作量 × 比例。
// 工作量上限由 kb.limits（max_minutes/max_pages）约束 ⇒ 预算有界；未知工作量按来源上限取（见 mediaWorkload）。
type MediaBudget struct {
	base      time.Duration
	perMinute time.Duration
	perPage   time.Duration
}

// MediaBudgets 预算解析单点（插件配置 kb.media.timeout_*；非正 ⇒ 代码缺省）
func MediaBudgets() MediaBudget {
	b := MediaBudget{base: DefaultMediaBaseTimeout, perMinute: DefaultMediaPerMinute, perPage: DefaultMediaPerPDFPage}
	m := kbcfg.Get().Media
	if m.TimeoutSeconds > 0 {
		b.base = time.Duration(m.TimeoutSeconds) * time.Second
	}
	if m.TimeoutPerMediaMinute > 0 {
		b.perMinute = time.Duration(m.TimeoutPerMediaMinute) * time.Second
	}
	if m.TimeoutPerPDFPage > 0 {
		b.perPage = time.Duration(m.TimeoutPerPDFPage) * time.Second
	}
	return b
}

// mediaBudgetResolver 预算解析注入点（测试可替换；生产 = MediaBudgets）
var mediaBudgetResolver = MediaBudgets

// Base 基数（未知工作量/固定开销档；探测与无工作量参数步骤用）
func (b MediaBudget) Base() time.Duration { return b.fallback().base }

// ForMinutes 按媒体时长（分钟）取预算
func (b MediaBudget) ForMinutes(minutes float64) time.Duration {
	b = b.fallback()
	if minutes < 0 {
		minutes = 0
	}
	return b.base + time.Duration(minutes*float64(b.perMinute))
}

// ForPages 按 pdf 页数取预算（光栅化）
func (b MediaBudget) ForPages(pages int) time.Duration {
	b = b.fallback()
	if pages < 0 {
		pages = 0
	}
	return b.base + time.Duration(pages)*b.perPage
}

// fallback 零值（未解析）防御：回退缩略预算（调用方漏给预算时行为 = 旧基数档，不静默无界）
func (b MediaBudget) fallback() MediaBudget {
	if b.base <= 0 {
		b.base = DefaultMediaBaseTimeout
	}
	if b.perMinute < 0 {
		b.perMinute = 0
	}
	if b.perPage < 0 {
		b.perPage = 0
	}
	return b
}

// toolBudget 归一单步预算（<=0 ⇒ 基数档）
func toolBudget(budget time.Duration) time.Duration {
	if budget > 0 {
		return budget
	}
	return mediaBudgetResolver().Base()
}

// ExtractAudioTrack 音视频 → 16k 单声道 WAV（ASR 输入规范形态；budget = 本步执行预算，<=0 取基数）
func ExtractAudioTrack(ctx context.Context, bin string, input []byte, budget time.Duration) ([]byte, error) {
	dir, err := mediaTempDir()
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	in := filepath.Join(dir, "in.bin")
	if err := os.WriteFile(in, input, 0o600); err != nil {
		return nil, fmt.Errorf("kb: 媒体临时输入写失败: %w", err)
	}
	out := filepath.Join(dir, "out.wav")
	if _, err := runMediaTool(ctx, bin, budget, "-nostdin", "-y", "-i", in, "-vn", "-ac", "1", "-ar", "16000", "-f", "wav", out); err != nil {
		return nil, err
	}
	wav, err := os.ReadFile(out)
	if err != nil {
		return nil, fmt.Errorf("kb: 音轨读取失败: %w", err)
	}
	if len(wav) == 0 {
		return nil, errors.New("kb: 音轨提取为空（输入无音频流？）")
	}
	return wav, nil
}

// ExtractKeyframes 关键帧序列（每 stepSeconds 秒一帧 JPEG；返回帧字节与近似时间戳（秒））
func ExtractKeyframes(ctx context.Context, bin string, input []byte, stepSeconds int, budget time.Duration) ([][]byte, []int, error) {
	step := stepSeconds
	if step <= 0 {
		step = DefaultFrameSeconds
	}
	dir, err := mediaTempDir()
	if err != nil {
		return nil, nil, err
	}
	defer os.RemoveAll(dir)
	in := filepath.Join(dir, "in.bin")
	if err := os.WriteFile(in, input, 0o600); err != nil {
		return nil, nil, fmt.Errorf("kb: 媒体临时输入写失败: %w", err)
	}
	if _, err := runMediaTool(ctx, bin, budget, "-nostdin", "-y", "-i", in,
		"-vf", fmt.Sprintf("fps=1/%d", step), "-frames:v", fmt.Sprint(MaxMediaFrames+1), "-q:v", "3",
		filepath.Join(dir, "f_%05d.jpg")); err != nil {
		return nil, nil, err
	}
	files, err := filepath.Glob(filepath.Join(dir, "f_*.jpg"))
	if err != nil {
		return nil, nil, fmt.Errorf("kb: 帧文件枚举失败: %w", err)
	}
	sort.Strings(files)
	if len(files) > MaxMediaFrames {
		return nil, nil, fmt.Errorf("kb: 抽帧超硬帽（%d > %d；长视频请先切片或调大步长）", len(files), MaxMediaFrames)
	}
	if len(files) == 0 {
		return nil, nil, errors.New("kb: 抽帧输出 0 帧（视频无可解码画面？）")
	}
	frames := make([][]byte, 0, len(files))
	times := make([]int, 0, len(files))
	for i, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, nil, fmt.Errorf("kb: 帧读取失败（%s）: %w", filepath.Base(f), err)
		}
		frames = append(frames, b)
		times = append(times, i*step) // 近似的均匀步长（见文件头注）
	}
	return frames, times, nil
}

// RasterizePDFPages pdf → 页图（PNG；rasterizeDPI）。页数硬帽 maxRasterPages——
// 业务上限（kb.limits.max_pages）应由调用方在**光栅化前**先行检查（烧钱步骤前置原则）；
// budget 由调用方按页数取（MediaBudget.ForPages）。
func RasterizePDFPages(ctx context.Context, bin string, input []byte, budget time.Duration) ([][]byte, error) {
	dir, err := mediaTempDir()
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	in := filepath.Join(dir, "in.pdf")
	if err := os.WriteFile(in, input, 0o600); err != nil {
		return nil, fmt.Errorf("kb: 媒体临时输入写失败: %w", err)
	}
	prefix := filepath.Join(dir, "page")
	if _, err := runMediaTool(ctx, bin, budget, "-png", "-r", fmt.Sprint(rasterizeDPI), in, prefix); err != nil {
		return nil, err
	}
	files, err := filepath.Glob(prefix + "*.png")
	if err != nil {
		return nil, fmt.Errorf("kb: 页图枚举失败: %w", err)
	}
	sort.Strings(files)
	if len(files) > maxRasterPages {
		return nil, fmt.Errorf("kb: pdf 光栅化超硬帽（%d > %d 页）", len(files), maxRasterPages)
	}
	pages := make([][]byte, 0, len(files))
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, fmt.Errorf("kb: 页图读取失败（%s）: %w", filepath.Base(f), err)
		}
		pages = append(pages, b)
	}
	return pages, nil
}

// mediaTempDir 一次性媒体工作目录（随用随清）
func mediaTempDir() (string, error) {
	dir, err := os.MkdirTemp("", "kb-media-*")
	if err != nil {
		return "", fmt.Errorf("kb: 创建媒体临时目录失败: %w", err)
	}
	return dir, nil
}

// ProbeMediaDuration 读容器时长（分钟；`ffmpeg -i` 的 stderr `Duration: HH:MM:SS.xx` 行解析——与转码
// 同一二进制，不引入 ffprobe 新依赖）。无输出目标 ⇒ 退出码非 0 是预期形态（独立执行，不套 runMediaTool
// 的「非零即错」）；解析不到/超时 = 响亮报错（拒猜时长，交由链口径处置）。
// budget = 本步预算（探测只读容器头，调用方传基数档即可；<=0 取基数）。
// 用途：音视频的 max_minutes 来源闸**前置**（先于转码/ASR 烧钱步骤；pdf 页数闸同款）。
func ProbeMediaDuration(ctx context.Context, bin string, input []byte, budget time.Duration) (float64, error) {
	dir, err := mediaTempDir()
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(dir)
	in := filepath.Join(dir, "in.bin")
	if err := os.WriteFile(in, input, 0o600); err != nil {
		return 0, fmt.Errorf("kb: 媒体临时输入写失败: %w", err)
	}
	name := strings.TrimSpace(bin)
	if name == "" {
		name = DefaultFFmpegBin
	}
	budget = toolBudget(budget)
	cctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	cmd := exec.CommandContext(cctx, name, "-nostdin", "-hide_banner", "-i", in)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	_ = cmd.Run()
	if errors.Is(cctx.Err(), context.DeadlineExceeded) {
		return 0, fmt.Errorf("kb: %s 时长探测超时（%s）", filepath.Base(name), budget)
	}
	minutes, ok := parseFFmpegDuration(stderr.String())
	if !ok {
		return 0, fmt.Errorf("kb: %s 输出未含可解析的 Duration 行（拒猜时长）: %s",
			filepath.Base(name), tailRunes(stderr.String(), 200))
	}
	return minutes, nil
}

// parseFFmpegDuration 解析 `Duration: HH:MM:SS.xx` 行（返回分钟；N/A / 缺行 ⇒ 未命中）。
func parseFFmpegDuration(stderrText string) (float64, bool) {
	for _, line := range strings.Split(stderrText, "\n") {
		i := strings.Index(line, "Duration:")
		if i < 0 {
			continue
		}
		fields := strings.Fields(strings.TrimSpace(line[i+len("Duration:"):]))
		if len(fields) == 0 {
			continue
		}
		parts := strings.Split(strings.TrimSuffix(fields[0], ","), ":")
		if len(parts) != 3 {
			continue
		}
		h, e1 := strconv.ParseFloat(parts[0], 64)
		m, e2 := strconv.ParseFloat(parts[1], 64)
		s, e3 := strconv.ParseFloat(parts[2], 64)
		if e1 != nil || e2 != nil || e3 != nil {
			continue
		}
		return (h*3600 + m*60 + s) / 60, true
	}
	return 0, false
}

// runMediaTool 执行外部媒体工具（超时=响亮失败；stderr 末段进错误文案供诊断）；budget = 本步执行预算
func runMediaTool(ctx context.Context, bin string, budget time.Duration, args ...string) ([]byte, error) {
	name := strings.TrimSpace(bin)
	if name == "" {
		name = DefaultFFmpegBin
	}
	budget = toolBudget(budget)
	cctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	cmd := exec.CommandContext(cctx, name, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if errors.Is(cctx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("kb: %s 执行超时（%s）", filepath.Base(name), budget)
		}
		return nil, fmt.Errorf("kb: %s 执行失败: %v；stderr 末段: %s", filepath.Base(name), err, tailRunes(stderr.String(), 300))
	}
	return out, nil
}

// tailRunes 取字符串末段（rune 口径；stderr 诊断截断）
func tailRunes(s string, n int) string {
	s = strings.TrimSpace(s)
	rs := []rune(s)
	if len(rs) <= n {
		return s
	}
	return "…" + string(rs[len(rs)-n:])
}
