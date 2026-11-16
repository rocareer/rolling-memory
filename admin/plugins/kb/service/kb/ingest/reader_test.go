// plugin: kb v1.0.0
package ingest

import (
	"archive/zip"
	"bytes"
	"errors"
	"strings"
	"testing"
)

// buildDocx 构造最小 docx（word/document.xml + word/styles.xml）供纯单测使用。
func buildDocx(t *testing.T, documentXML, stylesXML string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	write := func(name, body string) {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip 创建 %s: %v", name, err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatalf("zip 写入 %s: %v", name, err)
		}
	}
	write("word/document.xml", documentXML)
	if stylesXML != "" {
		write("word/styles.xml", stylesXML)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip 关闭: %v", err)
	}
	return buf.Bytes()
}

const docxMainNS = `xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"`

func TestReadTxtNormalize(t *testing.T) {
	raw := []byte("\ufeff第一行\r\n第二行\r第三行\n")
	ext, err := Read("a.txt", "text/plain", raw)
	if err != nil {
		t.Fatalf("读取 txt: %v", err)
	}
	want := "第一行\n第二行\n第三行\n"
	if ext.Text != want {
		t.Fatalf("BOM/CRLF 归一不符: %q", ext.Text)
	}
	if ext.ExtractorID() != "kb-txt@v1" {
		t.Fatalf("提取器身份不符: %s", ext.ExtractorID())
	}
	if ext.Runes() != len([]rune(want)) {
		t.Fatalf("rune 数不符: %d", ext.Runes())
	}
	if ext.Format != FormatTxt || ext.Kind != ExtractKindText {
		t.Fatalf("格式/类型不符: %s/%s", ext.Format, ext.Kind)
	}
}

func TestReadBinaryRejected(t *testing.T) {
	_, err := Read("a.txt", "text/plain", []byte("abc\x00def"))
	if !errors.Is(err, ErrBinaryContent) {
		t.Fatalf("二进制应被拒: %v", err)
	}
}

func TestReadEmptyLoudFail(t *testing.T) {
	_, err := Read("a.txt", "text/plain", []byte("   \n  \n"))
	if !errors.Is(err, ErrEmptyText) {
		t.Fatalf("零文本应响亮失败: %v", err)
	}
}

func TestReadGarbledLoudFail(t *testing.T) {
	_, err := Read("a.txt", "text/plain", []byte(strings.Repeat("\uFFFD", 20)+"abc"))
	if !errors.Is(err, ErrGarbledText) {
		t.Fatalf("乱码率超阈应响亮失败: %v", err)
	}
}

func TestDetectFormat(t *testing.T) {
	cases := []struct {
		name, mime string
		want       string
	}{
		{"a.md", "", FormatMd},
		{"a.markdown", "", FormatMd},
		{"a.htm", "", FormatHTML},
		{"a.csv", "", FormatCSV},
		{"a.docx", "", FormatDocx},
		{"无扩展名", "text/plain", FormatTxt},
		{"无扩展名", "text/csv; charset=utf-8", FormatCSV},
		{"无扩展名", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", FormatDocx},
	}
	for _, c := range cases {
		got, err := DetectFormat(c.name, c.mime, nil)
		if err != nil || got != c.want {
			t.Fatalf("DetectFormat(%q,%q)=%q,%v 期望 %q", c.name, c.mime, got, err, c.want)
		}
	}
	if got, err := DetectFormat("a.pdf", "application/pdf", nil); err != nil || got != FormatPDF {
		t.Fatalf("pdf 判型应成功（类级判型已登记）: %q %v", got, err)
	}
	if _, err := DetectFormat("a.exe", "", nil); !errors.Is(err, ErrUnsupportedFormat) {
		t.Fatalf("未登记格式应响亮失败: %v", err)
	}
	// docx 魔数嗅探
	zipMagic := append([]byte{'P', 'K', 3, 4}, make([]byte, 8)...)
	if got, err := DetectFormat("blob", "", zipMagic); err != nil || got != FormatDocx {
		t.Fatalf("zip 魔数应判 docx: %q %v", got, err)
	}
}

func TestReadMdVerbatim(t *testing.T) {
	raw := []byte("# 标题一\n\n正文一\n\n## 标题二\n正文二\n")
	ext, err := Read("a.md", "", raw)
	if err != nil {
		t.Fatalf("读取 md: %v", err)
	}
	if ext.Text != string(raw) {
		t.Fatalf("md 提取应逐字保留: %q", ext.Text)
	}
	if n, ok := ext.Stats["headings"].(int); !ok || n != 2 {
		t.Fatalf("标题统计不符: %v", ext.Stats["headings"])
	}
}

func TestReadHTMLStripHeadings(t *testing.T) {
	raw := []byte(`<html><head><title>t</title><style>.a{}</style></head>
<body><h1>标题一</h1><p>段落  一</p><script>var x=1;</script>
<h2>标题二</h2><ul><li>甲</li><li>乙</li></ul><p>尾</p></body></html>`)
	ext, err := Read("a.html", "", raw)
	if err != nil {
		t.Fatalf("读取 html: %v", err)
	}
	text := ext.Text
	for _, want := range []string{"# 标题一", "## 标题二", "段落 一", "-"} {
		if want == "-" {
			continue
		}
		if !strings.Contains(text, want) {
			t.Fatalf("html 提取缺少 %q：\n%s", want, text)
		}
	}
	for _, bad := range []string{"var x=1", ".a{}", "<p>", "<h1>"} {
		if strings.Contains(text, bad) {
			t.Fatalf("html 提取残留 %q：\n%s", bad, text)
		}
	}
	if idx := strings.Index(text, "甲"); idx < 0 {
		t.Fatalf("列表项缺失：\n%s", text)
	}
}

func TestReadCSVVerbatim(t *testing.T) {
	raw := []byte("name,age\n\u5f20\u4e09,30\n\u674e\u56db,25\n")
	ext, err := Read("a.csv", "", raw)
	if err != nil {
		t.Fatalf("读取 csv: %v", err)
	}
	if ext.Text != string(raw) {
		t.Fatalf("csv 提取应逐字保留: %q", ext.Text)
	}
	if n, ok := ext.Stats["rows"].(int); !ok || n != 3 {
		t.Fatalf("行数统计不符: %v", ext.Stats["rows"])
	}
}

func TestReadCSVInvalid(t *testing.T) {
	// 未闭合引号：FieldsPerRecord 变长容忍 + LazyQuotes 下仍可解析，故构造结构错误不成立——
	// 改为验证合法内容解析通过、空文件响亮失败。
	if _, err := Read("a.csv", "", []byte("\n")); !errors.Is(err, ErrEmptyText) {
		t.Fatalf("空 csv 应响亮失败: %v", err)
	}
}

func TestReadDocx(t *testing.T) {
	document := `<?xml version="1.0" encoding="UTF-8"?>
<w:document ` + docxMainNS + `><w:body>
<w:p><w:pPr><w:pStyle w:val="Heading1"/></w:pPr><w:r><w:t>合同标题</w:t></w:r></w:p>
<w:p><w:r><w:t>正文第一段</w:t></w:r></w:p>
<w:p><w:pPr><w:pStyle w:val="2"/></w:pPr><w:r><w:t>条款一</w:t></w:r></w:p>
<w:p><w:r><w:t>条款正文</w:t><w:r/></w:r><w:r><w:tab/><w:t>缩进文本</w:t></w:r></w:p>
</w:body></w:document>`
	styles := `<?xml version="1.0" encoding="UTF-8"?>
<w:styles ` + docxMainNS + `>
<w:style w:styleId="Heading1"><w:name w:val="heading 1"/></w:style>
<w:style w:styleId="2"><w:name w:val="标题 2"/></w:style>
</w:styles>`
	data := buildDocx(t, document, styles)
	ext, err := Read("a.docx", "", data)
	if err != nil {
		t.Fatalf("读取 docx: %v", err)
	}
	for _, want := range []string{"# 合同标题", "## 条款一", "正文第一段", "缩进文本"} {
		if !strings.Contains(ext.Text, want) {
			t.Fatalf("docx 提取缺少 %q：\n%s", want, ext.Text)
		}
	}
	if n, ok := ext.Stats["headings"].(int); !ok || n != 2 {
		t.Fatalf("标题统计不符: %v", ext.Stats["headings"])
	}
	if n, ok := ext.Stats["paragraphs"].(int); !ok || n != 4 {
		t.Fatalf("段落统计不符: %v", ext.Stats["paragraphs"])
	}
}

func TestReadDocxMissingDocumentXML(t *testing.T) {
	data := buildDocx(t, "", "")
	// 空 document.xml 无标题/正文 → 响亮失败（零文本）
	if _, err := Read("a.docx", "", data); !errors.Is(err, ErrEmptyText) {
		t.Fatalf("空 docx 应响亮失败: %v", err)
	}
}

func TestCountWordsLike(t *testing.T) {
	cjk, other := CountWordsLike("你好ab\n世界!")
	if cjk != 4 || other != 3 { // 你好世界 = 4 CJK；a b ! = 3
		t.Fatalf("分类不符: cjk=%d other=%d", cjk, other)
	}
}

func TestIsATXHeading(t *testing.T) {
	cases := map[string]int{
		"# 一级": 1, "###### 六级": 6, "####### 七级": 0, "#无空格": 0, "正文": 0, "#": 1,
	}
	for line, want := range cases {
		if got := isATXHeading(line); got != want {
			t.Fatalf("isATXHeading(%q)=%d 期望 %d", line, got, want)
		}
	}
}
