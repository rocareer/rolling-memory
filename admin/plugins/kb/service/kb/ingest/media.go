// plugin: kb v1.0.0
// 媒体链：图片/扫描件 OCR、音频 ASR、视频 ASR+关键帧 caption 的渠道能力探测、
// 驱动登记面与 fail-closed 链体。
//
// 契约：
//   - 图片（image）→ OCR 链（按页计价，kb_cost_log kind=ocr）；pdf 无文本层 → 光栅化 + 同 OCR 链；
//   - 音频（audio）→ ASR 链（按分钟计价，kind=asr）；
//   - 视频（video）→ 音轨 ASR（转写，dialog 语法）+ 关键帧 caption（帧描述，以 `[ts] frame: ...`
//     行并入转写文本——天然兼容 dialog 语法，`frame` 作 speaker）→ 单提取物（kind=asr）；
//   - **fail-closed（本文件的骨架即交付）**：任一环缺失（渠道未开 / 驱动未登记 / ffmpeg 或
//     pdftoppm 不在 PATH）⇒ MediaUnavailableError（稳定原因码：extract_failed: <code>）——
//     作业落 failed_stage='extract' + 原因码，保留 blob，渠道就绪后复投重试；**禁伪造产物**。
//     本项目当前**无 OCR/ASR/caption 驱动登记**（无厂商凭据；DB 渠道核验受连库失败阻断，如实上报），
//     故媒体链在生产即停在「渠道不可用」——这不是跳过施工，而是把「不可用」做成显式、可审计、可复位的事实。
//   - 接线一次调用：实现 OCRPages/ASRTranscribe/CaptionFrames → RegisterOCRDriver/RegisterASRDriver/
//     RegisterCaptionDriver（进程内登记，照 ai 面 RegisterDriver 先例）；配置 kb.media.<ch>.enabled +
//     provider 指向该驱动名即通。驱动**不摸 DB**：记账行由本包构造（cost.go）、job 侧与提取物同事务落
//     （禁无账开销）。
//   - 记账读数进 stats：ocr→{pages, ocr_pages, provider, confidence}；asr→{minutes, provider}；
//     视频帧 caption 计数→{frames}（无媒体读数=无渠道产物=本函数不产任何记账依据）。
package ingest

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/ai-go-hub/ai-go-admin/plugins/kb/kbcfg"
)

const (
	// extractorImage 图片 OCR 提取器名（build 身份）
	extractorImage = "kb-image"
	// extractorAudio 音频 ASR 提取器名
	extractorAudio = "kb-audio"
	// extractorVideo 视频提取器名（ASR 转写 + 关键帧 caption 融合文本）
	extractorVideo = "kb-video"
)

// 原因码（稳定字符串；进 failed_reason，后台/巡检/Smoke 据它识别「环境阻断」与真失败）
const (
	ReasonOCRUnavailable     = "ocr_channel_unavailable"
	ReasonASRUnavailable     = "asr_channel_unavailable"
	ReasonCaptionUnavailable = "caption_channel_unavailable"
	ReasonFFmpegMissing      = "ffmpeg_missing"
	ReasonRasterizerMissing  = "pdf_rasterizer_missing"
)

// MediaUnavailableError 媒体链能力不可用（fail-closed 响亮失败；errors.As 可判）
type MediaUnavailableError struct {
	Code   string // 原因码闭集（Reason* 常量）
	Detail string // 人读细节（渠道名/工具名；不含用户输入回显）
}

// Error 错误文案（含稳定原因码；进 failed_reason——512 rune 帽内）
func (e *MediaUnavailableError) Error() string {
	msg := "kb: 媒体能力不可用（extract_failed: " + e.Code + "）"
	if strings.TrimSpace(e.Detail) != "" {
		msg += "；" + e.Detail
	}
	return msg
}

// ReasonCodeOf 提取错误 → 原因码（非 MediaUnavailableError = ""；巡检/冒烟判「阻断 vs 真失败」）
func ReasonCodeOf(err error) string {
	var m *MediaUnavailableError
	if errors.As(err, &m) {
		return m.Code
	}
	return ""
}

// MediaChannelCaps 单渠道能力（配置侧读数）
type MediaChannelCaps struct {
	Enabled  bool   // kb.media.<ch>.enabled
	Provider string // kb.media.<ch>.provider（驱动登记名）
}

// MediaCaps 媒体链能力快照（探测读数；config 未初始化 = 渠道全关（fail-closed），ffmpeg=未探测）
type MediaCaps struct {
	FFmpegBin       string
	FFmpegAvailable bool
	OCR             MediaChannelCaps
	ASR             MediaChannelCaps
	Caption         MediaChannelCaps
	FrameSeconds    int
}

// 缺省（config/kb.yaml kb.media.* 为覆盖源）
const (
	// DefaultFFmpegBin 抽帧/音轨工具（PATH 查找）
	DefaultFFmpegBin = "ffmpeg"
	// DefaultFrameSeconds 关键帧步长（秒；R12 视频收益未验证，读数前不翻缺省）
	DefaultFrameSeconds = 30
)

// mediaCapsResolver 能力解析单点（测试可注入假体；生产 = resolveMediaCaps）
var mediaCapsResolver = resolveMediaCaps

// sourceLimitsResolver 来源上限解析单点（测试注入点；生产 = SourceLimitsOf——config kb.limits.*）。
// 链内前置检查点：无文本层的媒体来源在光栅化/OCR/ASR（烧钱步骤）之前先查页数/时长上限
// （超限挂起待批）。
var sourceLimitsResolver = SourceLimitsOf

// MediaCapsOf 媒体链能力快照（探测单点）
func MediaCapsOf() MediaCaps { return mediaCapsResolver() }

// resolveMediaCaps 读插件配置组装能力快照（驱动登记表为空 ⇒ 渠道全关 fail-closed，不猜；
// ffmpeg 探测与配置解耦——纯本地读数照常探测）
func resolveMediaCaps() MediaCaps {
	caps := MediaCaps{FFmpegBin: DefaultFFmpegBin, FrameSeconds: DefaultFrameSeconds}
	m := kbcfg.Get().Media
	if bin := strings.TrimSpace(m.FFmpegBin); bin != "" {
		caps.FFmpegBin = bin
	}
	if m.FrameSeconds > 0 {
		caps.FrameSeconds = m.FrameSeconds
	}
	caps.OCR = MediaChannelCaps{Enabled: m.OCR.Enabled, Provider: strings.TrimSpace(m.OCR.Provider)}
	caps.ASR = MediaChannelCaps{Enabled: m.ASR.Enabled, Provider: strings.TrimSpace(m.ASR.Provider)}
	caps.Caption = MediaChannelCaps{Enabled: m.Caption.Enabled, Provider: strings.TrimSpace(m.Caption.Provider)}
	caps.FFmpegAvailable = commandAvailable(caps.FFmpegBin)
	return caps
}

// commandAvailable 外部工具可用性（PATH 查找）
func commandAvailable(bin string) bool {
	if strings.TrimSpace(bin) == "" {
		return false
	}
	_, err := exec.LookPath(bin)
	return err == nil
}

// rasterizerBin pdf 光栅化工具（固定名；poppler 套件，与 pdftotext 同包）
const rasterizerBin = "pdftoppm"

// ==================== 驱动登记面（照 ai 面 RegisterDriver 先例；当前为空） ====================

// OCRPage OCR 单页读数
type OCRPage struct {
	Text       string  // 页文本（verbatim；空页由质量门/融合层处理）
	Confidence float64 // 置信度读数（0 = 驱动未报告；逐页落 stats 聚合）
}

// OCRDriver 云 OCR 驱动（图片 / pdf 扫描件页图；实现方自理 HTTP/凭据/重试）
type OCRDriver interface {
	Name() string
	OCRPages(ctx context.Context, pages [][]byte) ([]OCRPage, error)
}

// ASRDriver 云 ASR 驱动（音频 16k 单声道 WAV；输出 = **dialog 语法**转写文本：每轮
// `[HH:MM:SS] speaker: text`（或 [MM:SS]/[SS]），无前缀行 = 上一轮续行——dialog 策略的输入契约）
type ASRDriver interface {
	Name() string
	ASRTranscribe(ctx context.Context, audioWAV []byte) (*ASRResult, error)
}

// ASRResult ASR 转写读数
type ASRResult struct {
	Text    string  // 转写文本（dialog 语法）
	Minutes float64 // 音频时长（分钟；Stats 读数与 max_minutes 闸输入）
}

// CaptionDriver 关键帧 caption 驱动（VLM/OCR；输出逐帧描述）
type CaptionDriver interface {
	Name() string
	CaptionFrames(ctx context.Context, frames [][]byte) ([]string, error)
}

var (
	ocrDrivers     = map[string]OCRDriver{}
	asrDrivers     = map[string]ASRDriver{}
	captionDrivers = map[string]CaptionDriver{}
)

// RegisterOCRDriver 登记 OCR 驱动（重名 panic——响亮失败，照 jobs.Register 口径）
func RegisterOCRDriver(d OCRDriver) {
	if d == nil || strings.TrimSpace(d.Name()) == "" {
		panic("kb: 登记 OCR 驱动非法（空名/空实现）")
	}
	if _, dup := ocrDrivers[d.Name()]; dup {
		panic("kb: OCR 驱动重复登记: " + d.Name())
	}
	ocrDrivers[d.Name()] = d
}

// RegisterASRDriver 登记 ASR 驱动
func RegisterASRDriver(d ASRDriver) {
	if d == nil || strings.TrimSpace(d.Name()) == "" {
		panic("kb: 登记 ASR 驱动非法（空名/空实现）")
	}
	if _, dup := asrDrivers[d.Name()]; dup {
		panic("kb: ASR 驱动重复登记: " + d.Name())
	}
	asrDrivers[d.Name()] = d
}

// RegisterCaptionDriver 登记 caption 驱动
func RegisterCaptionDriver(d CaptionDriver) {
	if d == nil || strings.TrimSpace(d.Name()) == "" {
		panic("kb: 登记 caption 驱动非法（空名/空实现）")
	}
	if _, dup := captionDrivers[d.Name()]; dup {
		panic("kb: caption 驱动重复登记: " + d.Name())
	}
	captionDrivers[d.Name()] = d
}

// ocrDriverOf OCR 链可用性检查单点（未开渠道 / 未登记驱动 ⇒ 响亮失败；当前环境停在此处）
func ocrDriverOf(caps MediaCaps) (OCRDriver, error) {
	if !caps.OCR.Enabled {
		return nil, &MediaUnavailableError{Code: ReasonOCRUnavailable, Detail: "kb.media.ocr.enabled=false（渠道未配置；接线=实现驱动 + RegisterOCRDriver）"}
	}
	d, ok := ocrDrivers[caps.OCR.Provider]
	if !ok {
		return nil, &MediaUnavailableError{Code: ReasonOCRUnavailable, Detail: fmt.Sprintf("OCR 驱动未登记: provider=%q（登记面为空——无厂商凭据）", caps.OCR.Provider)}
	}
	return d, nil
}

// asrDriverOf ASR 链可用性检查单点
func asrDriverOf(caps MediaCaps) (ASRDriver, error) {
	if !caps.ASR.Enabled {
		return nil, &MediaUnavailableError{Code: ReasonASRUnavailable, Detail: "kb.media.asr.enabled=false（渠道未配置；接线=实现驱动 + RegisterASRDriver）"}
	}
	d, ok := asrDrivers[caps.ASR.Provider]
	if !ok {
		return nil, &MediaUnavailableError{Code: ReasonASRUnavailable, Detail: fmt.Sprintf("ASR 驱动未登记: provider=%q（登记面为空——无厂商凭据）", caps.ASR.Provider)}
	}
	return d, nil
}

// captionDriverOf caption 链可用性检查单点
func captionDriverOf(caps MediaCaps) (CaptionDriver, error) {
	if !caps.Caption.Enabled {
		return nil, &MediaUnavailableError{Code: ReasonCaptionUnavailable, Detail: "kb.media.caption.enabled=false（渠道未配置；接线=实现驱动 + RegisterCaptionDriver）"}
	}
	d, ok := captionDrivers[caps.Caption.Provider]
	if !ok {
		return nil, &MediaUnavailableError{Code: ReasonCaptionUnavailable, Detail: fmt.Sprintf("caption 驱动未登记: provider=%q（登记面为空——无厂商凭据）", caps.Caption.Provider)}
	}
	return d, nil
}

// ==================== 链体 ====================

// extractImageOCR 图片 → OCR 链（单页）
func extractImageOCR(ctx context.Context, data []byte) (*Extract, error) {
	caps := MediaCapsOf()
	drv, err := ocrDriverOf(caps)
	if err != nil {
		return nil, err
	}
	res, err := drv.OCRPages(ctx, [][]byte{data})
	if err != nil {
		return nil, fmt.Errorf("kb: 图片 OCR 失败: %w", err)
	}
	text, stats := joinOCRResults(res)
	stats["pages"] = 1
	stats["ocr_pages"] = 1
	stats["provider"] = drv.Name()
	return &Extract{
		Kind: ExtractKindOCR, Extractor: extractorImage, Version: ExtractorVersion,
		Format: FormatImage, MIME: "text/plain; charset=utf-8",
		Text: text, Stats: stats,
	}, nil
}

// mediaWorkload 音视频链内前置：探测时长（顺带产出本链执行预算的工作量口径）+ 时长闸。
// （超限挂起待批；**先于转码/ASR 烧钱步骤**——已整改：原实现等 ASR 返回 minutes 才判，
// 超长音视频先全额转写才被挂起，闸挡不住最大开销；页数闸（图片/扫描件光栅化之前）同款口径。）
//
// 返回 = 预算用工作量（分钟）：
//   - 探测成功 ⇒ 实际时长（超限则挂起，error = *SourceSuspendedError）；
//   - 探测失败 ⇒ **不阻断**（护栏位尽力而为；ASR 之后的统一来源闸仍是兜底），工作量按来源上限
//     （kb.limits.max_minutes，最坏允许值）取——避免未知时长落回基数档而被固定常量永久截断
//     （原 mediaExecTimeout 固定 180s 即此类）。
func mediaWorkload(ctx context.Context, bin string, data []byte) (float64, error) {
	limits := sourceLimitsResolver().WithDefaults()
	minutes, err := ProbeMediaDuration(ctx, bin, data, mediaBudgetResolver().Base())
	if err != nil {
		return limits.MaxMinutes, nil
	}
	if susp := sourceLimitsResolver().Check(0, minutes); susp != nil {
		return 0, &SourceSuspendedError{Suspended: susp}
	}
	return minutes, nil
}

// extractAudioASR 音频 → ASR 链（转写，dialog 语法）
func extractAudioASR(ctx context.Context, data []byte) (*Extract, error) {
	caps := MediaCapsOf()
	drv, err := asrDriverOf(caps)
	if err != nil {
		return nil, err
	}
	if !caps.FFmpegAvailable {
		return nil, &MediaUnavailableError{
			Code:   ReasonFFmpegMissing,
			Detail: fmt.Sprintf("ffmpeg 不在 PATH（kb.media.ffmpeg_bin=%q）；音频需先转码为 16k 单声道 WAV", caps.FFmpegBin),
		}
	}
	minutes, err := mediaWorkload(ctx, caps.FFmpegBin, data)
	if err != nil {
		return nil, err // 时长超限：挂起待批，先于转码/ASR（付费步骤）
	}
	wav, err := ExtractAudioTrack(ctx, caps.FFmpegBin, data, mediaBudgetResolver().ForMinutes(minutes))
	if err != nil {
		return nil, err
	}
	res, err := drv.ASRTranscribe(ctx, wav)
	if err != nil {
		return nil, fmt.Errorf("kb: 音频 ASR 失败: %w", err)
	}
	if res == nil {
		return nil, errors.New("kb: ASR 驱动返回空结果（响亮失败）")
	}
	text := normalizeTranscript(res.Text)
	stats := statsOf(text)
	stats["minutes"] = res.Minutes
	stats["provider"] = drv.Name()
	return &Extract{
		Kind: ExtractKindASR, Extractor: extractorAudio, Version: ExtractorVersion,
		Format: FormatAudio, MIME: "text/plain; charset=utf-8",
		Text: text, Stats: stats,
	}, nil
}

// extractVideoAV 视频 → 音轨 ASR + 关键帧 caption 链（两者齐备才产出——禁未完成件入库）
//
// 融合文本形态（单提取物单策略的承载方式）：转写（dialog 语法）在前，帧 caption 以
// `[<ts>] frame: <描述>` 行按时间序并入（frame 作 speaker——天然是合法 dialog 轮，见 dialog.go）。
func extractVideoAV(ctx context.Context, data []byte) (*Extract, error) {
	caps := MediaCapsOf()
	asr, err := asrDriverOf(caps) // ① 主内容渠道
	if err != nil {
		return nil, err
	}
	capDrv, err := captionDriverOf(caps) // ② 帧 caption 渠道（视频契约要求两者齐备）
	if err != nil {
		return nil, err
	}
	if !caps.FFmpegAvailable { // ③ 本地工具（音轨 + 抽帧）
		return nil, &MediaUnavailableError{
			Code:   ReasonFFmpegMissing,
			Detail: fmt.Sprintf("ffmpeg 不在 PATH（kb.media.ffmpeg_bin=%q）", caps.FFmpegBin),
		}
	}
	minutes, err := mediaWorkload(ctx, caps.FFmpegBin, data)
	if err != nil {
		return nil, err // 时长超限：挂起待批，先于转码/ASR/抽帧（付费/重活步骤）
	}
	budget := mediaBudgetResolver().ForMinutes(minutes)
	wav, err := ExtractAudioTrack(ctx, caps.FFmpegBin, data, budget)
	if err != nil {
		return nil, err
	}
	res, err := asr.ASRTranscribe(ctx, wav)
	if err != nil {
		return nil, fmt.Errorf("kb: 视频音轨 ASR 失败: %w", err)
	}
	if res == nil {
		return nil, errors.New("kb: ASR 驱动返回空结果（响亮失败）")
	}
	frames, times, err := ExtractKeyframes(ctx, caps.FFmpegBin, data, caps.FrameSeconds, budget)
	if err != nil {
		return nil, err
	}
	captions, err := capDrv.CaptionFrames(ctx, frames)
	if err != nil {
		return nil, fmt.Errorf("kb: 关键帧 caption 失败（%d 帧）: %w", len(frames), err)
	}
	text := mergeVideoText(normalizeTranscript(res.Text), times, captions)
	stats := statsOf(text)
	stats["minutes"] = res.Minutes
	stats["frames"] = len(frames)
	stats["provider"] = asr.Name() + "+" + capDrv.Name()
	return &Extract{
		Kind: ExtractKindASR, Extractor: extractorVideo, Version: ExtractorVersion,
		Format: FormatVideo, MIME: "text/plain; charset=utf-8",
		Text: text, Stats: stats,
	}, nil
}

// normalizeTranscript 转写文本归一（换行归一 + 尾部补换行；不做内容改写）
func normalizeTranscript(s string) string {
	s = NormalizeText(s)
	if s != "" && !strings.HasSuffix(s, "\n") {
		s += "\n"
	}
	return s
}

// mergeVideoText 转写 + 帧 caption 融合（caption 行 = `[HH:MM:SS] frame: <描述>`；空描述/空帧剔除）
func mergeVideoText(transcript string, frameTimes []int, captions []string) string {
	var b strings.Builder
	b.WriteString(transcript)
	if len(transcript) > 0 && !strings.HasSuffix(transcript, "\n\n") {
		b.WriteString("\n")
	}
	for i, cap := range captions {
		desc := strings.TrimSpace(NormalizeText(cap))
		if desc == "" {
			continue
		}
		ts := 0
		if i < len(frameTimes) {
			ts = frameTimes[i]
		}
		fmt.Fprintf(&b, "[%s] frame: %s\n", formatClock(ts), strings.ReplaceAll(desc, "\n", " "))
	}
	return b.String()
}

// formatClock 秒 → HH:MM:SS（dialog 时间戳契约形态）
func formatClock(sec int) string {
	if sec < 0 {
		sec = 0
	}
	return fmt.Sprintf("%02d:%02d:%02d", sec/3600, (sec%3600)/60, sec%60)
}

// joinOCRResults OCR 逐页结果 → 文本 + 读数（页间 "\n\n"；置信度取均值）
func joinOCRResults(res []OCRPage) (string, map[string]any) {
	parts := make([]string, 0, len(res))
	confSum, confN := 0.0, 0
	for _, p := range res {
		t := strings.Trim(tidyLines(NormalizeText(p.Text)), "\n")
		if strings.TrimSpace(t) != "" {
			parts = append(parts, t)
		}
		if p.Confidence > 0 {
			confSum += p.Confidence
			confN++
		}
	}
	text := ""
	if len(parts) > 0 {
		text = strings.Join(parts, "\n\n") + "\n"
	}
	stats := statsOf(text)
	if confN > 0 {
		stats["confidence"] = confSum / float64(confN)
	}
	return text, stats
}
