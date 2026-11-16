// plugin: kb v1.0.0
// 知识库导入管道 · 格式提取层（文本类格式：txt / md / html / csv / docx；多模态：pdf / 图片 / 音频 / 视频）。
//
// 契约：
//   - 提取物（kb_extract）= **重切不重解**的独立真源档：uk(document_id, extractor, version)；
//     extractor@version 是 build_hash 的组成项（提取器实现变更必须 +1，否则重切复算会漂移）。
//   - 锚指向 verbatim 提取文本：切片的 char_start/char_end 是**提取文本的 rune 偏移**，
//     text_hash = sha1(提取文本[rune 区间])。故提取一旦落库即冻结；本层只做「读取 + 归一」。
//   - 文本类格式里 txt/md/csv 的提取文本 = 原文归一（去 BOM、CRLF/CR→LF）后**逐字**；
//     html/docx 无纯文本真源，提取为「归一文本表示」——块级结构换行，标题合成 ATX（#）行，
//     供 heading 策略按行解析（docx 标题样式 → `#`~`######` 前缀；html h1~h6 同理）。
//   - 多模态（本层分派，链体在 media.go / media_ffmpeg.go）：
//     图片 → OCR 链；音频 → ASR 链；视频 → ASR + 关键帧 caption 链。渠道/驱动缺失 ⇒ fail-closed
//     响亮失败（MediaUnavailableError：稳定原因码；extract_failed），禁伪造产物（本项目当前无
//     OCR/ASR 驱动登记——环境阻断如实悬置，接线 = 实现驱动 + Register* 一次调用）。
//   - 响亮失败：空文本 / 二进制（NUL）/ 乱码率（U+FFFD 占比）超阈一律报错，禁静默入库（失败兜底）。
//
// 本层不触碰 DB 与 chunk 包：提取=纯函数（输入字节 → 提取物）；**记账**（kb_cost_log，非 ai 直连
// 的 OCR/ASR/caption 成本）与提取物同事务落库，由 cost.go 承担、job 侧调用（禁无账开销）。
package ingest

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/csv"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/html"
)

// 提取物闭集（kb_extract.kind；文本层 + 多模态三型——migrations/1_core.up.sql 注释同口径）
const (
	ExtractKindText    = "text"    // 文本层（txt/md/html/csv/docx；**PDF 文本层 v1 未实现**，见 ReadFormat 分派）
	ExtractKindOCR     = "ocr"     // 光学识别（图片 / pdf 扫描件）
	ExtractKindASR     = "asr"     // 语音转写（音频 / 视频音轨）
	ExtractKindCaption = "caption" // 图像描述（视频关键帧；v1 并入视频提取文本，帧行标注）
)

// 源格式闭集（kb_document.source_kind=file 时的文件格式路由键）：
//   - 文本类五格式（归一文本提取）；
//   - 类级四格式（pdf/image/audio/video——细分扩展名在 DetectFormat 内归一为类级；
//     类级进 build_hash 的 extractor 身份，扩展名不入身份，同类格式口径一致）。
//
// **v1 裁剪**：PDF 文本层提取**未实现**（本插件不含 PDF 解析依赖；加依赖会改动宿主的 go.mod/go.sum，
// 那属宿主文件改动面）。PDF 仍被判型为 `FormatPDF`，提取时响亮失败并落 `failed_stage='extract'`；
// 解锁条件 = 为宿主 go.mod 增加一行依赖后补回 PDF 提取器与用例。
const (
	FormatTxt   = "txt"
	FormatMd    = "md"
	FormatHTML  = "html"
	FormatCSV   = "csv"
	FormatDocx  = "docx"
	FormatPDF   = "pdf"
	FormatImage = "image"
	FormatAudio = "audio"
	FormatVideo = "video"
)

// 提取器标识与版本（extractor@version 进 build_hash；实现变更必须 +1——重切触发条件）
const (
	// ExtractorVersion 提取器版本（本批全部提取器的公共版本；单提取器变更时按格式分版本）
	ExtractorVersion = "v1"

	extractorTxt  = "kb-txt"
	extractorMd   = "kb-md"
	extractorHTML = "kb-html"
	extractorCSV  = "kb-csv"
	extractorDocx = "kb-docx"
	// extractorPDF pdf 文本层提取器名（**身份保留**：v1 不支持 PDF 文本层提取，本常量只为
	// ExtractorIDFor 的 FormatPDF 分支与成本记账的提取器身份完备而存在——判型阶段 pdf 仍被判为
	// FormatPDF，提取则在 ReadFormat 里响亮失败，见该分派处的说明）
	extractorPDF = "kb-pdf"
)

// 质量阈值与来源上限（失败兜底：空文本/乱码率超阈响亮失败）
const (
	// MaxSourceBytes 单文档来源字节上限（文本类格式内存提取的安全帽；超限响亮失败留 blob）
	MaxSourceBytes = 64 << 20
	// MaxReplacementRatio 乱码率阈值（U+FFFD 占比；超阈判乱码，响亮失败）
	MaxReplacementRatio = 0.05
	// maxDocxEntryBytes docx 单条目解压上限（zip 炸弹防御）
	maxDocxEntryBytes = 64 << 20
)

// 提取层哨兵错误（errors.Is 判定；上层据此落 failed(stage='extract', reason)）
var (
	// ErrEmptyText 零文本：禁静默入库（「响亮失败」）
	ErrEmptyText = errors.New("kb: 提取文本为空（零文本禁静默入库）")
	// ErrBinaryContent 二进制内容（NUL 字节；文本类格式不接受）
	ErrBinaryContent = errors.New("kb: 内容为二进制（含 NUL 字节）")
	// ErrGarbledText 乱码率超阈（U+FFFD 占比 > MaxReplacementRatio）
	ErrGarbledText = errors.New("kb: 乱码率超阈")
	// ErrUnsupportedFormat 格式不在提取闭集（文本类 + 多模态四类；其余响亮失败）
	ErrUnsupportedFormat = errors.New("kb: 格式不在提取闭集")
	// ErrSourceTooLarge 来源超上限（保留 blob，后台按需提高上限或分片）
	ErrSourceTooLarge = errors.New("kb: 来源超上限")
)

// Extract 一次提取产物（kb_extract 行的领域视图；重切不重解的载体）
type Extract struct {
	Kind      string         // 提取物类型（ExtractKindText）
	Extractor string         // 提取器名（kb-txt/kb-md/kb-html/kb-csv/kb-docx）
	Version   string         // 提取器版本（v1）
	Format    string         // 源格式闭集（txt/md/html/csv/docx）
	MIME      string         // 提取文本 MIME
	Text      string         // 提取文本（归一后；锚与 char 区间的坐标系）
	Stats     map[string]any // 统计（rune 数/行数/乱码率/结构读数）
}

// ExtractorID 提取器身份（extractor@version；build_hash 组成项与 kb_extract uk 的第二、三列）
func (e *Extract) ExtractorID() string {
	if e == nil {
		return ""
	}
	return e.Extractor + "@" + e.Version
}

// Runes 提取文本 rune 长度（char_start/char_end 的坐标系）
func (e *Extract) Runes() int {
	if e == nil {
		return 0
	}
	return utf8.RuneCountInString(e.Text)
}

// ReplacementRatio 乱码率（U+FFFD 数 / rune 总数；空文本 = 0）
func (e *Extract) ReplacementRatio() float64 {
	if e == nil || e.Text == "" {
		return 0
	}
	n := strings.Count(e.Text, "\uFFFD")
	return float64(n) / float64(utf8.RuneCountInString(e.Text))
}

// ExtractorIDFor 格式 → 提取器身份（extractor@version；build 身份组成项 + kb_extract uk）。
// 未登记格式 ⇒ 响亮报错（与 ReadFormat 同闭集）。
func ExtractorIDFor(format string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case FormatTxt:
		return extractorTxt + "@" + ExtractorVersion, nil
	case FormatMd:
		return extractorMd + "@" + ExtractorVersion, nil
	case FormatHTML:
		return extractorHTML + "@" + ExtractorVersion, nil
	case FormatCSV:
		return extractorCSV + "@" + ExtractorVersion, nil
	case FormatDocx:
		return extractorDocx + "@" + ExtractorVersion, nil
	case FormatPDF:
		return extractorPDF + "@" + ExtractorVersion, nil
	case FormatImage:
		return extractorImage + "@" + ExtractorVersion, nil
	case FormatAudio:
		return extractorAudio + "@" + ExtractorVersion, nil
	case FormatVideo:
		return extractorVideo + "@" + ExtractorVersion, nil
	default:
		return "", fmt.Errorf("%w：format=%q", ErrUnsupportedFormat, format)
	}
}

// Read 按名字与 MIME 判型后提取（管道入口；name 直接取 kb_document.source_ref 或标题）。
func Read(name, mime string, data []byte) (*Extract, error) {
	format, err := DetectFormat(name, mime, data)
	if err != nil {
		return nil, err
	}
	return ReadFormat(format, data)
}

// DetectFormat 格式判型（闭集；扩展名优先，MIME 次之，魔数嗅探兜底，未命中报错——响亮失败不猜）。
// 类级格式：pdf / image / audio / video（细分扩展名归一到类级；类级即 extractor 身份键）。
func DetectFormat(name, mime string, data []byte) (string, error) {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(strings.TrimSpace(name)), "."))
	switch ext {
	case FormatTxt, "text":
		return FormatTxt, nil
	case FormatMd, "markdown":
		return FormatMd, nil
	case FormatHTML, "htm":
		return FormatHTML, nil
	case FormatCSV:
		return FormatCSV, nil
	case FormatDocx:
		return FormatDocx, nil
	case FormatPDF:
		return FormatPDF, nil
	}
	if imageExts[ext] {
		return FormatImage, nil
	}
	if audioExts[ext] {
		return FormatAudio, nil
	}
	if videoExts[ext] {
		return FormatVideo, nil
	}
	switch m := strings.ToLower(strings.TrimSpace(strings.Split(mime, ";")[0])); {
	case m == "text/plain":
		return FormatTxt, nil
	case m == "text/markdown":
		return FormatMd, nil
	case m == "text/html", m == "application/xhtml+xml":
		return FormatHTML, nil
	case m == "text/csv":
		return FormatCSV, nil
	case m == "application/vnd.openxmlformats-officedocument.wordprocessingml.document":
		return FormatDocx, nil
	case m == "application/pdf":
		return FormatPDF, nil
	case strings.HasPrefix(m, "image/"):
		return FormatImage, nil
	case strings.HasPrefix(m, "audio/"):
		return FormatAudio, nil
	case strings.HasPrefix(m, "video/"):
		return FormatVideo, nil
	}
	// 内容嗅探：docx = zip 魔数（PK\x03\x04）；多模态四型魔数（pdf/png/jpeg/mp3/RIFF-WAVE/mp4-系列）
	if len(data) >= 4 && bytes.Equal(data[:4], []byte{'P', 'K', 3, 4}) {
		return FormatDocx, nil
	}
	switch {
	case len(data) >= 5 && string(data[:5]) == "%PDF-":
		return FormatPDF, nil
	case len(data) >= 8 && bytes.Equal(data[:8], []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}):
		return FormatImage, nil
	case len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF:
		return FormatImage, nil
	case len(data) >= 3 && string(data[:3]) == "ID3":
		return FormatAudio, nil
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WAVE":
		return FormatAudio, nil
	case len(data) >= 12 && string(data[4:8]) == "ftyp":
		return FormatVideo, nil
	}
	return "", fmt.Errorf("%w：name=%q mime=%q", ErrUnsupportedFormat, name, mime)
}

// imageExts/audioExts/videoExts 类级格式的扩展名集（小写、无点；未列出的走 MIME/魔数）
var (
	imageExts = map[string]bool{"png": true, "jpg": true, "jpeg": true, "gif": true, "webp": true, "bmp": true, "tif": true, "tiff": true}
	audioExts = map[string]bool{"mp3": true, "wav": true, "m4a": true, "aac": true, "flac": true, "ogg": true}
	videoExts = map[string]bool{"mp4": true, "mov": true, "mkv": true, "webm": true, "avi": true}
)

// ReadFormat 读提取（无 ctx 形态：文本类格式纯本地；媒体链应经 ReadFormatCtx 传入作业 ctx）。
func ReadFormat(format string, data []byte) (*Extract, error) {
	return ReadFormatCtx(context.Background(), format, data)
}

// ReadFormatCtx 闭集内显式分派提取（ctx 供媒体链的云调用/外部工具取消）。
func ReadFormatCtx(ctx context.Context, format string, data []byte) (*Extract, error) {
	if len(data) > MaxSourceBytes {
		return nil, fmt.Errorf("%w：%d 字节 > %d", ErrSourceTooLarge, len(data), MaxSourceBytes)
	}
	format = strings.ToLower(strings.TrimSpace(format))
	switch format {
	case FormatTxt, FormatMd, FormatHTML, FormatCSV:
		// 文本类格式：NUL 判二进制（docx 是 zip 容器，含 NUL 属正常）
		if bytes.IndexByte(data, 0) >= 0 {
			return nil, ErrBinaryContent
		}
	}
	var (
		ext *Extract
		err error
	)
	switch format {
	case FormatTxt:
		ext, err = extractTxt(data)
	case FormatMd:
		ext, err = extractMd(data)
	case FormatHTML:
		ext, err = extractHTML(data)
	case FormatCSV:
		ext, err = extractCSV(data)
	case FormatDocx:
		ext, err = extractDocx(data)
	case FormatPDF:
		// v1 未实现 PDF 文本层提取：本插件不含 PDF 解析依赖（不动宿主 go.mod 的唯一解法），
		// 故此处**响亮失败**——判型仍保留 FormatPDF（错误文案才能说清「是这个格式但本插件不支持」），
		// 调用方的既有错误路径会把作业落 failed_stage='extract'，不伪造产物。
		return nil, fmt.Errorf("%w：v1 未支持 PDF 文本层提取（本插件不含 PDF 解析依赖，内置提取器未启用）", ErrUnsupportedFormat)
	case FormatImage:
		ext, err = extractImageOCR(ctx, data)
	case FormatAudio:
		ext, err = extractAudioASR(ctx, data)
	case FormatVideo:
		ext, err = extractVideoAV(ctx, data)
	default:
		return nil, fmt.Errorf("%w：format=%q", ErrUnsupportedFormat, format)
	}
	if err != nil {
		return nil, err
	}
	if err := CheckExtract(ext); err != nil {
		return nil, err
	}
	return ext, nil
}

// CheckExtract 提取质量门（零文本 / 乱码率）。OCR/ASR/caption 产物同走本门（空转写禁静默入库）；
// 媒体链的能力缺失（渠道/工具未就位）在链内先于本门响亮失败（MediaUnavailableError，见 media.go）。
func CheckExtract(e *Extract) error {
	if e == nil || strings.TrimSpace(e.Text) == "" {
		return ErrEmptyText
	}
	if r := e.ReplacementRatio(); r > MaxReplacementRatio {
		return fmt.Errorf("%w：U+FFFD 占比 %.3f > %.3f", ErrGarbledText, r, MaxReplacementRatio)
	}
	return nil
}

// NormalizeText 文本归一单点：去 UTF-8 BOM、CRLF/CR → LF（不做 trim——逐字纪律）。
func NormalizeText(s string) string {
	s = strings.TrimPrefix(s, "\ufeff")
	if strings.ContainsAny(s, "\r") {
		s = strings.ReplaceAll(s, "\r\n", "\n")
		s = strings.ReplaceAll(s, "\r", "\n")
	}
	return s
}

// statsOf 提取统计（rune 数/行数/标题数/乱码率——成本估算与质量读数共用口径）
func statsOf(text string) map[string]any {
	lines := 1
	if text != "" {
		lines = strings.Count(text, "\n") + 1
	}
	h := 0
	for _, ln := range strings.Split(text, "\n") {
		if isATXHeading(ln) > 0 {
			h++
		}
	}
	fffd := strings.Count(text, "\uFFFD")
	runes := utf8.RuneCountInString(text)
	ratio := 0.0
	if runes > 0 {
		ratio = float64(fffd) / float64(runes)
	}
	return map[string]any{
		"runes":       runes,
		"lines":       lines,
		"headings":    h,
		"replacement": fffd,
		"fffd_ratio":  ratio,
	}
}

// isATXHeading 行是否 ATX 标题（`#`~`######` + 空格/行尾）；返回层级 1..6，非标题 0。
func isATXHeading(line string) int {
	line = strings.TrimLeft(line, " ")
	n := 0
	for n < len(line) && line[n] == '#' {
		n++
	}
	if n < 1 || n > 6 {
		return 0
	}
	if n == len(line) {
		return n
	}
	if line[n] != ' ' && line[n] != '\t' {
		return 0
	}
	return n
}

// ---------- txt ----------

func extractTxt(data []byte) (*Extract, error) {
	text := NormalizeText(string(data))
	return &Extract{
		Kind: ExtractKindText, Extractor: extractorTxt, Version: ExtractorVersion,
		Format: FormatTxt, MIME: "text/plain; charset=utf-8",
		Text: text, Stats: statsOf(text),
	}, nil
}

// ---------- md ----------

func extractMd(data []byte) (*Extract, error) {
	// md 原文即文本（标题由 `#` 语法自带）——逐字保留，heading 策略按行解析
	text := NormalizeText(string(data))
	return &Extract{
		Kind: ExtractKindText, Extractor: extractorMd, Version: ExtractorVersion,
		Format: FormatMd, MIME: "text/markdown; charset=utf-8",
		Text: text, Stats: statsOf(text),
	}, nil
}

// ---------- csv ----------

// extractCSV csv 提取 = 原文归一后逐字（表结构解析归 table 策略：重切时 repr 保持一致，
// 偏移相对提取文本；解析失败（不合法 CSV）时报错——响亮失败而非静默降级）。
func extractCSV(data []byte) (*Extract, error) {
	text := NormalizeText(string(data))
	if err := validateCSV(text); err != nil {
		return nil, err
	}
	stats := statsOf(text)
	if rows, err := countCSVRows(text); err == nil {
		stats["rows"] = rows
	}
	return &Extract{
		Kind: ExtractKindText, Extractor: extractorCSV, Version: ExtractorVersion,
		Format: FormatCSV, MIME: "text/csv; charset=utf-8",
		Text: text, Stats: stats,
	}, nil
}

// validateCSV 校验提取文本可按标准 csv 解析（逗号分隔；FieldsPerRecord 变长容忍）。
func validateCSV(text string) error {
	r := csv.NewReader(strings.NewReader(text))
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	for {
		_, err := r.Read()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("kb: csv 解析失败: %w", err)
		}
	}
}

// countCSVRows 统计 csv 数据行数（不含空尾行）
func countCSVRows(text string) (int, error) {
	r := csv.NewReader(strings.NewReader(text))
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	n := 0
	for {
		rec, err := r.Read()
		if err == io.EOF {
			return n, nil
		}
		if err != nil {
			return 0, err
		}
		if len(rec) == 1 && rec[0] == "" {
			continue // 空行不计
		}
		n++
	}
}

// ---------- html ----------

// htmlBlockTags 块级标签（结束后换行；行内标签不换行）
var htmlBlockTags = map[string]bool{
	"address": true, "article": true, "aside": true, "blockquote": true, "dd": true,
	"div": true, "dl": true, "dt": true, "fieldset": true, "figcaption": true,
	"figure": true, "footer": true, "form": true, "h1": true, "h2": true, "h3": true,
	"h4": true, "h5": true, "h6": true, "header": true, "hr": true, "li": true,
	"main": true, "nav": true, "ol": true, "p": true, "pre": true, "section": true,
	"table": true, "tbody": true, "td": true, "tfoot": true, "th": true, "thead": true,
	"tr": true, "ul": true,
}

// htmlSkipTags 整棵跳过（脚本/样式/元信息/模板）
var htmlSkipTags = map[string]bool{
	"script": true, "style": true, "head": true, "template": true, "noscript": true,
	"iframe": true, "svg": true, "math": true, "title": true, "meta": true, "link": true,
}

// extractHTML html 提取（x/net/html 解析 → 归一文本表示）：
// 块级标签换行；h1~h6 合成 ATX 标题行（heading 策略同 md 口径）；script/style/head 子树剥除。
func extractHTML(data []byte) (*Extract, error) {
	root, err := html.Parse(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("kb: html 解析失败: %w", err)
	}
	var b strings.Builder
	var walk func(n *html.Node, inHeading int)
	walk = func(n *html.Node, inHeading int) {
		switch n.Type {
		case html.TextNode:
			text := collapseSpace(n.Data)
			if text == "" {
				return
			}
			b.WriteString(text)
		case html.ElementNode:
			name := strings.ToLower(n.Data)
			if htmlSkipTags[name] {
				return
			}
			if htmlLineBreak(name) {
				b.WriteString("\n")
			}
			heading := inHeading
			if lv := headingLevel(name); lv > 0 {
				b.WriteString("\n")
				b.WriteString(strings.Repeat("#", lv))
				b.WriteString(" ")
				heading = lv
			}
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				walk(c, heading)
			}
			if htmlBlockTags[name] || heading > 0 {
				b.WriteString("\n")
			}
		default:
			// 文档节点/注释/Doctype：直通子节点（不产出文本）
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				walk(c, inHeading)
			}
		}
	}
	walk(root, 0)

	text := tidyLines(b.String())
	stats := statsOf(text)
	stats["runes"] = utf8.RuneCountInString(text)
	return &Extract{
		Kind: ExtractKindText, Extractor: extractorHTML, Version: ExtractorVersion,
		Format: FormatHTML, MIME: "text/plain; charset=utf-8",
		Text: text, Stats: stats,
	}, nil
}

// headingLevel h1~h6 → 1..6；其余 0
func headingLevel(name string) int {
	if len(name) == 2 && name[0] == 'h' && name[1] >= '1' && name[1] <= '6' {
		return int(name[1] - '0')
	}
	return 0
}

// htmlLineBreak 是否显式换行标签（br/hr）
func htmlLineBreak(name string) bool {
	return name == "br" || name == "hr"
}

// collapseSpace 行内空白折叠（HTML 空白无语义）；NBSP 归一为空格
func collapseSpace(s string) string {
	s = strings.ReplaceAll(s, "\u00a0", " ")
	return spaceRe.ReplaceAllString(s, " ")
}

var spaceRe = regexp.MustCompile(`[ \t\f\v]+`)

// tidyLines 行级归一：行尾去空白、折叠连续空行为单空行、去首尾空行。
func tidyLines(s string) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	blank := 0
	for _, ln := range lines {
		ln = strings.TrimRight(ln, " \t")
		if ln == "" {
			blank++
			if blank > 1 || len(out) == 0 {
				continue
			}
			out = append(out, "")
			continue
		}
		blank = 0
		out = append(out, ln)
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	if len(out) == 0 {
		return ""
	}
	return strings.Join(out, "\n") + "\n"
}

// ---------- docx ----------

// docxParagraph docx 段落中间态（样式 → 标题层级 + 文本）
type docxParagraph struct {
	style string
	level int // 标题层级 1..6；0 = 正文
	text  string
}

// extractDocx docx 提取（archive/zip + encoding/xml 自解析，零重依赖）：
// 逐段提取；标题样式（styles.xml 的 Heading N / 标题 N / styleId N）合成 ATX 前缀行。
func extractDocx(data []byte) (*Extract, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("kb: docx 解包失败: %w", err)
	}
	docXML, err := readZipEntry(zr, "word/document.xml", maxDocxEntryBytes)
	if err != nil {
		return nil, err
	}
	stylesXML, _ := readZipEntry(zr, "word/styles.xml", maxDocxEntryBytes) // 可选
	styleLevels := parseDocxStyles(stylesXML)

	paras, err := parseDocxBody(docXML)
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	headings, tables := 0, 0
	for _, p := range paras {
		text := strings.TrimRight(p.text, " \t")
		if text == "" {
			b.WriteString("\n")
			continue
		}
		level := p.level
		if level == 0 && p.style != "" {
			level = styleLevels[strings.ToLower(p.style)]
		}
		if level > 0 {
			headings++
			b.WriteString(strings.Repeat("#", level))
			b.WriteString(" ")
		}
		b.WriteString(text)
		b.WriteString("\n")
	}
	tables = strings.Count(string(docXML), "<w:tbl>") + strings.Count(string(docXML), "<w:tbl ")
	text := tidyLines(b.String())
	stats := statsOf(text)
	stats["paragraphs"] = len(paras)
	stats["headings"] = headings
	stats["tables"] = tables
	return &Extract{
		Kind: ExtractKindText, Extractor: extractorDocx, Version: ExtractorVersion,
		Format: FormatDocx, MIME: "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		Text: text, Stats: stats,
	}, nil
}

// readZipEntry 读取 zip 条目（带上限；缺失报错，可选条目由调用方忽略错误）
func readZipEntry(zr *zip.Reader, name string, max int64) ([]byte, error) {
	for _, f := range zr.File {
		if f.Name != name {
			continue
		}
		if int64(f.UncompressedSize64) > max {
			return nil, fmt.Errorf("%w：docx 条目 %s 解压 %d 字节 > %d", ErrSourceTooLarge, name, f.UncompressedSize64, max)
		}
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("kb: docx 读取 %s 失败: %w", name, err)
		}
		defer rc.Close()
		buf, err := io.ReadAll(io.LimitReader(rc, max+1))
		if err != nil {
			return nil, fmt.Errorf("kb: docx 读取 %s 失败: %w", name, err)
		}
		if int64(len(buf)) > max {
			return nil, fmt.Errorf("%w：docx 条目 %s 超上限", ErrSourceTooLarge, name)
		}
		return buf, nil
	}
	return nil, fmt.Errorf("kb: docx 缺少条目 %s", name)
}

// docxStyleRegex 标题样式名匹配（Heading 1 / heading1 / 标题 1）
var docxStyleRegex = regexp.MustCompile(`(?i)^(?:heading|标题)[\s_-]*([1-6])$`)

// parseDocxStyles 解析 styles.xml：styleId → 标题层级（name 匹配标题文法，或 styleId 为 1..6 数字）。
func parseDocxStyles(data []byte) map[string]int {
	out := map[string]int{}
	if len(data) == 0 {
		return out
	}
	dec := xml.NewDecoder(bytes.NewReader(data))
	var (
		curID   string
		curName string
	)
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			if ee, ok := tok.(xml.EndElement); ok && ee.Name.Local == "style" {
				if lv := docxStyleLevel(curID, curName); lv > 0 {
					out[strings.ToLower(curID)] = lv
				}
				curID, curName = "", ""
			}
			continue
		}
		switch se.Name.Local {
		case "style":
			curID, curName = "", ""
			for _, a := range se.Attr {
				if a.Name.Local == "styleId" {
					curID = a.Value
				}
			}
		case "name":
			for _, a := range se.Attr {
				if a.Name.Local == "val" {
					curName = a.Value
				}
			}
		}
	}
	return out
}

// docxStyleLevel 标题层级判定：name 命中标题文法，或 styleId 为 1..6 纯数字（中文 Word 常见）。
func docxStyleLevel(styleID, name string) int {
	if m := docxStyleRegex.FindStringSubmatch(strings.TrimSpace(name)); m != nil {
		if lv, err := strconv.Atoi(m[1]); err == nil {
			return lv
		}
	}
	if id := strings.TrimSpace(styleID); len(id) == 1 && id[0] >= '1' && id[0] <= '6' {
		return int(id[0] - '0')
	}
	return 0
}

// parseDocxBody 解析 document.xml 正文：w:p 段落（w:pStyle → 样式；w:t 文本；w:tab/w:br 控制符）。
func parseDocxBody(data []byte) ([]docxParagraph, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	var (
		paras   []docxParagraph
		cur     *docxParagraph
		inText  bool
		lastEnd bool
	)
	flush := func() {
		if cur != nil {
			paras = append(paras, *cur)
		}
		cur = nil
	}
	for {
		tok, err := dec.Token()
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("kb: docx 正文解析失败: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "p":
				if cur == nil {
					cur = &docxParagraph{}
				}
			case "pStyle":
				if cur != nil && cur.style == "" {
					for _, a := range t.Attr {
						if a.Name.Local == "val" {
							cur.style = a.Value
						}
					}
				}
			case "t":
				inText = true
				lastEnd = false
			case "tab":
				if cur != nil && inText {
					cur.text += "\t"
				}
			case "br":
				if cur != nil && inText {
					cur.text += "\n"
				}
			}
		case xml.CharData:
			if inText && cur != nil {
				cur.text += string(t)
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inText = false
			case "p":
				flush()
				lastEnd = true
			}
		}
	}
	if !lastEnd {
		flush()
	}
	return paras, nil
}

// CountWordsLike 行内字符分类（成本系数用：CJK 与非 CJK 字符数——估算 token 的输入）。
// 口径：CJK ≈ 0.6 token/字、非 CJK ≈ 1/4 字符；本函数只做分类，不做系数。
func CountWordsLike(text string) (cjk, other int) {
	for _, r := range text {
		switch {
		case unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) ||
			unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hangul, r):
			cjk++
		case r == '\n' || r == '\r':
			// 换行不计入 token 估算字符
		default:
			other++
		}
	}
	return cjk, other
}
