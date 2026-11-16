// plugin: kb v1.0.0
package ingest

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// 格式判型与提取身份的用例（原 pdf 提取用例随「v1 裁剪 PDF」删改：本插件不含 PDF 解析依赖）。
//
// 本文件保留**与 PDF 无关**的两组契约用例（媒体判型 / 提取器身份）与公共夹具 fixtureBytes
//（media_test.go 的光栅化用例也用它），并把 PDF 的期望从「提取成功」改成「提取响亮失败」——
// 判型仍认 PDF（错误文案才能说清「是这个格式但本插件不支持」）。

func fixtureBytes(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("fixture 读取失败（%s）: %v", name, err)
	}
	return b
}

func TestDetectFormatMediaFormats(t *testing.T) {
	cases := []struct {
		name, mime string
		want       string
	}{
		{"a.pdf", "", FormatPDF},
		{"a.PDF", "application/pdf", FormatPDF},
		{"a.png", "", FormatImage},
		{"无扩展名", "image/jpeg", FormatImage},
		{"a.mp3", "", FormatAudio},
		{"a.wav", "audio/wav", FormatAudio},
		{"a.mp4", "", FormatVideo},
		{"a.mov", "video/quicktime", FormatVideo},
	}
	for _, c := range cases {
		got, err := DetectFormat(c.name, c.mime, nil)
		if err != nil || got != c.want {
			t.Fatalf("DetectFormat(%q,%q)=%q,%v 期望 %q", c.name, c.mime, got, err, c.want)
		}
	}
	// 魔数嗅探（无扩展名无 MIME）
	if got, err := DetectFormat("blob", "", []byte("%PDF-1.7\nrest")); err != nil || got != FormatPDF {
		t.Fatalf("pdf 魔数嗅探: %q %v", got, err)
	}
	if got, err := DetectFormat("blob", "", append([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, make([]byte, 8)...)); err != nil || got != FormatImage {
		t.Fatalf("png 魔数嗅探: %q %v", got, err)
	}
	if _, err := DetectFormat("blob", "", []byte("plain words")); err == nil {
		t.Fatal("未登记内容应响亮报错")
	}
}

// TestExtractPDFLoudlyUnsupported v1 裁剪契约：PDF 仍被判型/仍有提取器身份，但**提取响亮失败**
// （不伪造产物；调用方的既有错误路径会把作业落 failed_stage='extract'）。
func TestExtractPDFLoudlyUnsupported(t *testing.T) {
	data := fixtureBytes(t, "fixture-text-1p.pdf")
	format, err := DetectFormat("fixture-text-1p.pdf", "application/pdf", data)
	if err != nil || format != FormatPDF {
		t.Fatalf("PDF 判型应保留（得到 %q, %v）", format, err)
	}
	_, err = ReadFormatCtx(context.Background(), FormatPDF, data)
	if err == nil {
		t.Fatal("PDF 提取必须响亮失败（v1 不支持 PDF 文本层）")
	}
	if !errors.Is(err, ErrUnsupportedFormat) {
		t.Fatalf("失败原因应为 ErrUnsupportedFormat，实际 %v", err)
	}
}

func TestExtractorIDForMediaFormats(t *testing.T) {
	cases := map[string]string{
		FormatPDF:   "kb-pdf@v1",
		FormatImage: "kb-image@v1",
		FormatAudio: "kb-audio@v1",
		FormatVideo: "kb-video@v1",
	}
	for format, want := range cases {
		got, err := ExtractorIDFor(format)
		if err != nil || got != want {
			t.Fatalf("ExtractorIDFor(%q)=%q,%v 期望 %q", format, got, err, want)
		}
	}
}
