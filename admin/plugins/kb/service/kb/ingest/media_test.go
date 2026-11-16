// plugin: kb v1.0.0
package ingest

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// 媒体链：能力探测 / fail-closed 原因码 / 驱动登记面 / 外部工具实跑（工具在则真跑，缺则如实跳过）。

func TestMediaCapsClosedByDefault(t *testing.T) {
	// ingest 包单测不初始化 config（无 config.Init）⇒ 渠道全关（fail-closed）、ffmpeg=未探测
	caps := MediaCapsOf()
	if caps.OCR.Enabled || caps.ASR.Enabled || caps.Caption.Enabled {
		t.Fatalf("config 未初始化时渠道必须全关（fail-closed）: %+v", caps)
	}
	if caps.FFmpegBin != DefaultFFmpegBin || caps.FrameSeconds != DefaultFrameSeconds {
		t.Fatalf("缺省档不符: %+v", caps)
	}
	if caps.FFmpegAvailable != commandAvailable(DefaultFFmpegBin) {
		t.Fatalf("ffmpeg 探测读数应与 LookPath 一致（环境如实）: %v", caps.FFmpegAvailable)
	}
}

func TestMediaChainsBlockedWithReasonCodes(t *testing.T) {
	// 渠道缺失 ⇒ 各链在最早处响亮失败 + 稳定原因码（禁伪造产物）
	cases := []struct {
		format string
		want   string
	}{
		{FormatImage, ReasonOCRUnavailable},
		{FormatAudio, ReasonASRUnavailable},
		{FormatVideo, ReasonASRUnavailable}, // 视频先查 ASR 主渠道
	}
	for _, c := range cases {
		_, err := ReadFormatCtx(context.Background(), c.format, []byte("not-a-real-payload"))
		if err == nil {
			t.Fatalf("%s 链在渠道缺失时必须失败", c.format)
		}
		if code := ReasonCodeOf(err); code != c.want {
			t.Fatalf("%s 原因码应 %s，实际 %q（err=%v）", c.format, c.want, code, err)
		}
	}
}

func TestReasonCodeOfNonMediaError(t *testing.T) {
	if code := ReasonCodeOf(ErrEmptyText); code != "" {
		t.Fatalf("非媒体错误原因码应空，实际 %q", code)
	}
	if code := ReasonCodeOf(nil); code != "" {
		t.Fatalf("nil 错误原因码应空，实际 %q", code)
	}
}

// fakeOCRDriver 测试驱动（登记/链路/记账构造的可通性验证——生产登记表为空，见 media.go 头注）
type fakeOCRDriver struct{}

func (fakeOCRDriver) Name() string { return "fake-ocr" }

func (fakeOCRDriver) OCRPages(_ context.Context, pages [][]byte) ([]OCRPage, error) {
	out := make([]OCRPage, 0, len(pages))
	for range pages {
		out = append(out, OCRPage{Text: "识别文本示例\n", Confidence: 0.9})
	}
	return out, nil
}

func TestImageChainWithRegisteredFakeDriver(t *testing.T) {
	oldResolver := mediaCapsResolver
	mediaCapsResolver = func() MediaCaps {
		return MediaCaps{OCR: MediaChannelCaps{Enabled: true, Provider: "fake-ocr"}}
	}
	defer func() { mediaCapsResolver = oldResolver }()
	RegisterOCRDriver(fakeOCRDriver{})
	defer delete(ocrDrivers, "fake-ocr")

	ext, err := ReadFormatCtx(context.Background(), FormatImage, []byte("img"))
	if err != nil {
		t.Fatalf("有渠道+驱动时图片链应通: %v", err)
	}
	if ext.Kind != ExtractKindOCR || ext.Extractor != extractorImage {
		t.Fatalf("身份不符: kind=%s extractor=%s", ext.Kind, ext.Extractor)
	}
	if StatFloat(ext.Stats, "ocr_pages") != 1 || StatFloat(ext.Stats, "confidence") <= 0 {
		t.Fatalf("读数不符: %+v", ext.Stats)
	}
	// 记账构造：ocr 产物 ⇒ 1 条 page 计价条目（单价未配 ⇒ price_unconfigured）
	entries := MediaCostEntries(ext, 1, 2)
	if len(entries) != 1 || entries[0].Kind != CostKindOCR || entries[0].Unit != CostUnitPage {
		t.Fatalf("记账条目不符: %+v", entries)
	}
	if v, ok := entries[0].Detail["price_unconfigured"].(bool); !ok || !v {
		t.Fatalf("未配价应标注 price_unconfigured: %+v", entries[0].Detail)
	}
}

// fakeASRDriver 测试 ASR 驱动
type fakeASRDriver struct{}

func (fakeASRDriver) Name() string { return "fake-asr" }

func (fakeASRDriver) ASRTranscribe(_ context.Context, _ []byte) (*ASRResult, error) {
	return &ASRResult{Text: "[00:01] 甲: 转写测试\n", Minutes: 0.1}, nil
}

func TestVideoChainRequiresCaptionChannel(t *testing.T) {
	// ASR 可用（驱动已登记）但 caption 不可用 ⇒ 视频链仍在 caption 处 fail-closed（禁未完成件）
	oldResolver := mediaCapsResolver
	mediaCapsResolver = func() MediaCaps {
		return MediaCaps{ASR: MediaChannelCaps{Enabled: true, Provider: "fake-asr"}}
	}
	defer func() { mediaCapsResolver = oldResolver }()
	RegisterASRDriver(fakeASRDriver{})
	defer delete(asrDrivers, "fake-asr")

	_, err := ReadFormatCtx(context.Background(), FormatVideo, []byte("vid"))
	if ReasonCodeOf(err) != ReasonCaptionUnavailable {
		t.Fatalf("caption 渠道缺失应 %s，实际 %v", ReasonCaptionUnavailable, err)
	}
}

// ---------- 外部工具实跑（工具不在场 ⇒ t.Skip，环境如实） ----------

func synthVideoMP4(t *testing.T) []byte {
	t.Helper()
	if !commandAvailable("ffmpeg") {
		t.Skip("ffmpeg 不在 PATH（环境如实跳过实跑用例）")
	}
	out := filepath.Join(t.TempDir(), "synth.mp4")
	cmd := exec.Command("ffmpeg", "-nostdin", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc=duration=2:size=160x120:rate=5",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=2",
		"-shortest", "-pix_fmt", "yuv420p", out)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("ffmpeg 合成素材失败（环境如实跳过）: %v；%s", err, tailRunes(string(b), 200))
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("合成素材读取失败: %v", err)
	}
	return data
}

func TestFFmpegExtractAudioTrackAndKeyframes(t *testing.T) {
	video := synthVideoMP4(t)

	wav, err := ExtractAudioTrack(context.Background(), DefaultFFmpegBin, video, 0)
	if err != nil {
		t.Fatalf("音轨提取失败: %v", err)
	}
	if len(wav) < 1000 {
		t.Fatalf("音轨字节过小: %d", len(wav))
	}
	if string(wav[:4]) != "RIFF" {
		t.Fatalf("音轨应为 WAV（RIFF 头），实际 %q", wav[:4])
	}

	frames, times, err := ExtractKeyframes(context.Background(), DefaultFFmpegBin, video, 1, 0)
	if err != nil {
		t.Fatalf("抽帧失败: %v", err)
	}
	if len(frames) == 0 || len(frames) != len(times) {
		t.Fatalf("帧数/时间戳数不符: %d/%d", len(frames), len(times))
	}
	if times[0] != 0 || (len(times) > 1 && times[1] != 1) {
		t.Fatalf("时间戳应按步长递增: %v", times)
	}
	if len(frames[0]) == 0 {
		t.Fatal("首帧为空")
	}
}

func TestRasterizePDFPagesFixture(t *testing.T) {
	if !commandAvailable(rasterizerBin) {
		t.Skipf("%s 不在 PATH（环境如实跳过实跑用例）", rasterizerBin)
	}
	pages, err := RasterizePDFPages(context.Background(), rasterizerBin, fixtureBytes(t, "fixture-text-1p.pdf"), 0)
	if err != nil {
		t.Fatalf("光栅化失败: %v", err)
	}
	if len(pages) != 1 || len(pages[0]) == 0 {
		t.Fatalf("应输出 1 页非空图，实际 %d 页", len(pages))
	}
}

func TestRunMediaToolMissingBinary(t *testing.T) {
	_, err := runMediaTool(context.Background(), "kb-definitely-missing-tool", 0, "--version")
	if err == nil {
		t.Fatal("缺工具应响亮失败")
	}
}
