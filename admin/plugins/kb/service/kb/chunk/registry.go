// plugin: kb v1.0.0
// Package chunk 知识库切片体系。
//
// 单点集（本包即施工落点，禁第二处口径）：
//   - 策略注册表：name + version 成对（照闭集做法，不落 DDL）；加策略 = 改闭集 + 配对评测 + 版本 +1。
//   - 配置模型：三层浅覆盖 global（config/kb.yaml + 代码缺省）▷ library（kb_library.chunk_config）
//     ▷ document（kb_document.chunk_config）；**未知键响亮拒绝**、值域校验单点（C3）。
//   - 锚契约：块锚 = <doc_locator>#pN（锚语法同构）+ 可复算定位（char 区间 [start,end) +
//     text_hash + chunker_version）；**禁只存切片序号**（换尺寸档会把旧锚整批打错）。
//   - 派生文本口径：ChunkTextOf（标题路径前缀等）——tsv 生成与嵌入文本同源。
//   - 构建身份：build_hash = sha1(内容身份 | config_hash | chunker_version | extractor@version)（C2）。
//
// 管道侧用法（ingest / 检索探针照抄）：
//
//	cfg, err := chunk.Global()                                  // kb.chunk 段 ∪ 代码缺省（已校验）
//	cfg, err = chunk.EffectiveOn(cfg, lib.ChunkConfig, doc.ChunkConfig) // 三层浅覆盖（nil/空层忽略）
//	name := cfg.Strategy                                        // 空 ⇒ 由格式路由决定
//	if name == "" { name, err = chunk.RouteFormat(format) }
//	ch, err := chunk.Get(name)                                  // 未登记 ⇒ 响亮报错
//	chunks, err := ch.Chunk(ctx, chunk.Input{Text: extract, Locator: docLocator, Config: cfg, ...})
//	cvh, err := chunk.StrategyVersion(name, agentIdentity)      // semantic 须带嵌入 agent 身份（C4）
//	build, err := chunk.BuildHash(contentSHA256, cfg.Hash(), cvh, "kb-text@1")
//	chunks, err = chunk.Finalize(docLocator, chunks)             // seq/locator/text_hash 规整（回接返回值）
//	err = chunk.VerifyVerbatim(extract, chunks)                   // 锚可复算 + 逐字一致（写库前自检）
//
// 尺寸档规范：缺省 512/64；改档必须与融合档同批配对读数，禁单改。
package chunk

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/ai-go-hub/ai-go-admin/plugins/kb/kbcfg"
)

// ── 产出形态与输入 ────────────────────────────────────────────────────────────

// Chunk 切片产出行（kb_chunk 落库前内存形态）。
//
// 定位真值 = CharStart/CharEnd（**半开区间 [start,end)，相对提取文本的 rune 下标**）+ TextHash +
// ChunkerVersion（由调用方落列）；Seq/Locator 由 Finalize 规整。Text 为 verbatim 原文
// （派生文本经 ChunkTextOf 单点生成，不落本字段）。
type Chunk struct {
	Seq         int        `json:"seq"`
	Locator     string     `json:"locator"`
	CharStart   int        `json:"char_start"`
	CharEnd     int        `json:"char_end"`
	HeadingPath []string   `json:"heading_path,omitempty"`
	TableMeta   *TableMeta `json:"table_meta,omitempty"`
	PageFrom    int        `json:"page_from,omitempty"`
	PageTo      int        `json:"page_to,omitempty"`
	TimeFrom    int64      `json:"time_from,omitempty"`
	TimeTo      int64      `json:"time_to,omitempty"`
	Text        string     `json:"text"`
	TextHash    string     `json:"text_hash"`
}

// TableMeta 表格元信息（table 策略产出；落 kb_chunk.table_meta）。
// RowFrom/RowTo = 数据行序号（1 起，不计表头行）；Header = 表头字段（header_repeat 时进派生文本）。
type TableMeta struct {
	Header  []string `json:"header,omitempty"`
	RowFrom int      `json:"row_from"`
	RowTo   int      `json:"row_to"`
	Columns int      `json:"columns,omitempty"`
}

// Embedder 句向量入口（semantic 策略用）：生产 = 经 ai.RunEmbeddings 的注入体
// （Source="kb"、agent=kb-embed；批内条数由调用方按其配置控制）；测试可注入假体。
type Embedder func(ctx context.Context, texts []string) ([][]float32, error)

// Input 切片输入（reader 归一后的 verbatim 文本 + 文档锚前缀）。
type Input struct {
	// Text 提取文本（verbatim；编码/换行归一由 reader 负责）
	Text string
	// Locator 文档 locator（锚前缀 = <doc_locator>#pN 的父单元；空 ⇒ Finalize 报错）
	Locator string
	// Config 有效配置（调用方经 Global/EffectiveOn 单点计算后传入）
	Config Config
	// Embedder 语义档句向量入口（semantic 必需；其余策略忽略）
	Embedder Embedder
	// AgentIdentity 语义档嵌入 agent 身份（形如 "kb-embed@3"；semantic 必需，其余策略忽略）
	AgentIdentity string
}

// ── 策略注册表（代码闭集；加策略 = 改本表 + 配对评测 + 版本 +1）────────────────

// Strategy 策略声明（name + version 成对，照闭集口径）
type Strategy struct {
	Name    string  // 策略名（闭集键；kb_chunk.chunker）
	Version string  // 契约版本（口径升级 ⇒ 升版本 + 重跑派生投影；kb_chunk.chunker_version）
	Note    string  // 说明（判据/默认档）
	chunker Chunker // 实现（无状态；配置经 Input 传入）
}

// Chunker 切片器接口（实现均无状态：配置与文本经 Input 传入）
type Chunker interface {
	Name() string
	Version() string
	// Chunk 产出块序列（未规整：Seq/Locator/TextHash 由 Finalize 填）。
	// 输入零文本（空/空白）⇒ 响亮报错（零文本禁静默入库）。
	Chunk(ctx context.Context, in Input) ([]Chunk, error)
}

// builtinStrategies 策略闭集（对齐表；**每个值必须有消费者**）。
//
// 表 8 策略的本项目落地状态（加未登记项 = 改本闭集 + 配对评测 + 版本 +1）：
//   - fixed / heading / table / code / semantic：登记在本包。
//   - dialog / page：登记在本包（会话转写 / 扫描件图片；dialog.go / page.go）。
//   - html：不单独登记——reader 剥脚本/样式后文本走 heading（「剥脚本/样式后同 heading」）。
var builtinStrategies = []Strategy{
	{Name: "fixed", Version: "v1", Note: "无结构纯文本兜底（target/overlap/hard_cap/tail_merge）", chunker: fixedChunker{}},
	{Name: "heading", Version: "v1", Note: "md/html/docx/pdf 文本层标题层级（levels/target/heading_prefix）", chunker: headingChunker{}},
	{Name: "table", Version: "v1", Note: "csv/表结构块（rows_per_chunk/header_repeat/min_rows）", chunker: tableChunker{}},
	{Name: "code", Version: "v1", Note: "代码扩展名（max/overlap；缩进/花括号启发式，不引 AST）", chunker: codeChunker{}},
	{Name: "semantic", Version: "v1", Note: "无显式结构长文（window/分位 threshold/min/max；缺省关，C4）", chunker: semanticChunker{}},
	{Name: "dialog", Version: "v1", Note: "会话/转写（轮=单元、多轮合并、禁跨会话合并、秒级时间戳）", chunker: dialogChunker{}},
	{Name: "page", Version: "v1", Note: "扫描件/图片（\\f 分页、page_as_chunk、cross_page_merge=false）", chunker: pageChunker{}},
}

// Strategies 策略清单（副本；排序稳定：登记序 =表序）
func Strategies() []Strategy {
	return append([]Strategy(nil), builtinStrategies...)
}

// Get 按名取切片器（未登记 ⇒ 响亮报错；禁静默兜底——加策略须改闭集）
func Get(name string) (Chunker, error) {
	n := strings.TrimSpace(name)
	for _, s := range builtinStrategies {
		if s.Name == n {
			return s.chunker, nil
		}
	}
	return nil, fmt.Errorf("kb 切片策略未登记: %q（闭集：%s）", n, strings.Join(strategyNames(), " / "))
}

// ValidStrategy 策略名闭集校验
func ValidStrategy(name string) bool {
	n := strings.TrimSpace(name)
	for _, s := range builtinStrategies {
		if s.Name == n {
			return true
		}
	}
	return false
}

// strategyNames 闭集名清单（报错/展示用）
func strategyNames() []string {
	out := make([]string, 0, len(builtinStrategies))
	for _, s := range builtinStrategies {
		out = append(out, s.Name)
	}
	return out
}

// StrategyVersion 构建身份里的 chunker_version 槽（C2/C4）。
//
//   - 一般策略：登记版本（如 "v1"）。
//   - semantic：额外要求把嵌入 agent 身份并入（C4「agent 版本入锚」）——agentIdentity 形如
//     "kb-embed@3"（名称@版本），产出 "v1+kb-embed@3"；**空 ⇒ 报错**（缺身份不得进锚：
//     否则换 agent / 换版本不换 build ⇒ 该重切的不重切）。
func StrategyVersion(strategy, agentIdentity string) (string, error) {
	n := strings.TrimSpace(strategy)
	var found *Strategy
	for i := range builtinStrategies {
		if builtinStrategies[i].Name == n {
			found = &builtinStrategies[i]
			break
		}
	}
	if found == nil {
		return "", fmt.Errorf("kb 切片策略未登记: %q（闭集：%s）", n, strings.Join(strategyNames(), " / "))
	}
	if n != "semantic" {
		return found.Version, nil
	}
	ident := strings.TrimSpace(agentIdentity)
	if ident == "" {
		return "", errors.New("kb semantic 切片缺嵌入 agent 身份（agentIdentity 形如 kb-embed@3；C4 agent 版本入锚）")
	}
	return found.Version + "+" + ident, nil
}

// ── 格式 → 策略默认路由─────────────────────────────────────────────────

// formatRoutes 格式 → 策略默认路由（键 = 小写格式/扩展名/类级格式，无点；表）。
// 代码扩展名统一走 code；html 剥标签后走 heading（reader 侧归一）；
// 类级键（image/audio/video 为 reader.DetectFormat 的类级返回值）与常见扩展名同名同路由。
var formatRoutes = map[string]string{
	"md": "heading", "markdown": "heading", "html": "heading", "htm": "heading", "docx": "heading",
	"pdf": "heading", // pdf 文本层 → heading（无标题即单节，等价 fixed 兜底行为；「有文本层→heading+fixed 兜底」）
	"txt": "fixed", "text": "fixed",
	"csv": "table", "tsv": "table",
	"go": "code", "py": "code", "js": "code", "ts": "code", "tsx": "code", "jsx": "code",
	"java": "code", "kt": "code", "rs": "code", "c": "code", "h": "code", "cc": "code", "cpp": "code",
	"hpp": "code", "cs": "code", "rb": "code", "php": "code", "swift": "code", "sh": "code",
	"sql": "code", "json": "code", "yaml": "code", "yml": "code", "toml": "code",
	// 图片/扫描件 → page；音频/视频（ASR 转写）→ dialog（表逐格式路线）
	"image": "page", "png": "page", "jpg": "page", "jpeg": "page", "gif": "page", "webp": "page", "bmp": "page", "tif": "page", "tiff": "page",
	"audio": "dialog", "mp3": "dialog", "wav": "dialog", "m4a": "dialog", "aac": "dialog", "flac": "dialog", "ogg": "dialog",
	"video": "dialog", "mp4": "dialog", "mov": "dialog", "mkv": "dialog", "webm": "dialog", "avi": "dialog",
}

// RouteFormat 格式 → 策略默认路由：入参 = 小写格式/扩展名/类级格式（带不带点均可）。
// 未登记格式 ⇒ 响亮报错（禁静默兜底——加路由须改本表）。
func RouteFormat(format string) (string, error) {
	f := strings.ToLower(strings.TrimSpace(format))
	f = strings.TrimPrefix(f, ".")
	if f == "" {
		return "", errors.New("kb 格式路由：格式为空")
	}
	if name, ok := formatRoutes[f]; ok {
		return name, nil
	}
	return "", fmt.Errorf("kb 格式路由未登记: %q（禁静默兜底；登记面见 formatRoutes）", format)
}

// RouteTXT txt 细分路由（txt→fixed；检出标题模式可升 heading——前 200 行内有标题行即升档）
func RouteTXT(text string) string {
	rs := []rune(text)
	lines := scanLines(rs)
	limit := len(lines)
	if limit > txtRouteScanLines {
		limit = txtRouteScanLines
	}
	for i := 0; i < limit; i++ {
		if _, level, _ := headingAt(rs, lines, i); level > 0 {
			return "heading"
		}
	}
	return "fixed"
}

// txtRouteScanLines txt 升档探测行数上限（防长文全量扫描）
const txtRouteScanLines = 200

// ── 锚契约──────────────────────────────────────────────────────────────

// Anchor 块锚：<doc_locator>#pN（N 从 1 起；锚语法与既有实现同构，
// 可反解析回父单元）。doc_locator 空 ⇒ 返回空串（Finalize 会响亮报错）。
func Anchor(docLocator string, seq int) string {
	d := strings.TrimSpace(docLocator)
	if d == "" || seq < 1 {
		return ""
	}
	return d + "#p" + strconv.Itoa(seq)
}

// ParseAnchor 解析块锚 <doc_locator>#pN：返回父单元 locator 与块号（>=1）。
// 非法（无 #p、块号非正整数、父单元空）⇒ ok=false（锚语法与判据同构）。
func ParseAnchor(locator string) (base string, seq int, ok bool) {
	l := strings.TrimSpace(locator)
	i := strings.LastIndex(l, "#p")
	if i <= 0 {
		return "", 0, false
	}
	n, err := strconv.Atoi(l[i+2:])
	if err != nil || n < 1 {
		return "", 0, false
	}
	return l[:i], n, true
}

// Finalize 产出规整单点（**返回剔除空白块后的切片，调用方须回接返回值**）：seq = 1..N、
// locator = Anchor(docLocator, seq)、text_hash = sha1(Text)（verbatim 正文口径，不含派生前缀）。
//
// 空白块剔除（Text 全空白 —— 不写库不占 seq）；剔除后为空 ⇒ 响亮报错（零文本禁静默入库）；
// docLocator 空 ⇒ 响亮报错（锚必须指向父单元，禁只存切片序号）。
func Finalize(docLocator string, chunks []Chunk) ([]Chunk, error) {
	if strings.TrimSpace(docLocator) == "" {
		return nil, errors.New("kb 切片锚缺父单元 locator（<doc_locator>#pN 的 doc_locator 不得为空）")
	}
	kept := make([]Chunk, 0, len(chunks))
	for _, c := range chunks {
		if strings.TrimSpace(c.Text) == "" {
			continue
		}
		kept = append(kept, c)
	}
	if len(kept) == 0 {
		return nil, errors.New("kb 切片零文本（切片结果为空；零文本禁静默入库——判据/失败兜底）")
	}
	for i := range kept {
		kept[i].Seq = i + 1
		kept[i].Locator = Anchor(docLocator, i+1)
		kept[i].TextHash = TextHash(kept[i].Text)
	}
	return kept, nil
}

// VerifyWithin 锚可复算校验（门禁口径）：逐块校验 char 区间合法（0 ≤ start ≤ end ≤ runeLen）
// 与 text_hash 复算一致。冒烟/用例的「锚可复算（char 区间 + text_hash 复算一致）」即本函数。
func VerifyWithin(extract string, chunks []Chunk) error {
	n := len([]rune(extract))
	for _, c := range chunks {
		if c.CharStart < 0 || c.CharStart > c.CharEnd || c.CharEnd > n {
			return fmt.Errorf("kb 切片锚非法：块 seq=%d 区间 [%d,%d) 越界（提取文本 %d rune）", c.Seq, c.CharStart, c.CharEnd, n)
		}
		if got := TextHash(c.Text); got != c.TextHash {
			return fmt.Errorf("kb 切片锚不可复算：块 seq=%d text_hash=%s 复算=%s", c.Seq, c.TextHash, got)
		}
	}
	return nil
}

// VerifyVerbatim 在 VerifyWithin 之上加逐字校验：Text 必须与提取文本的 [start,end) rune 切片一致
// （锚指向 verbatim 提取文本）。各策略（fixed/heading/table/code/semantic）均满足；
// 会改写文本的形态（如 dialog 的 speaker_prefix）只能用 VerifyWithin。
func VerifyVerbatim(extract string, chunks []Chunk) error {
	if err := VerifyWithin(extract, chunks); err != nil {
		return err
	}
	rs := []rune(extract)
	for _, c := range chunks {
		if got := string(rs[c.CharStart:c.CharEnd]); got != c.Text {
			return fmt.Errorf("kb 切片非逐字：块 seq=%d 的 Text 与提取文本 [%d,%d) 不一致", c.Seq, c.CharStart, c.CharEnd)
		}
	}
	return nil
}

// ── 派生文本口径单点────────────────────────────────────────────────────

// ChunkTextOf 派生文本单点：tsv 生成、trgm 表达式面、嵌入文本全部从它派生（写读同源，禁第二处口径）。
//
// 组成（按序）：① 标题路径前缀（heading_path 非空 ∧ heading_prefix=true）② 表头（table_meta.Header
// 非空 ∧ header_repeat=true）③ verbatim 正文。kb_chunk.text 列仍存 verbatim 正文（锚真源不掺前缀）。
func ChunkTextOf(c Chunk, cfg Config) string {
	var b strings.Builder
	if cfg.HeadingPrefix && len(c.HeadingPath) > 0 {
		b.WriteString(strings.Join(c.HeadingPath, " > "))
		b.WriteString("\n")
	}
	if cfg.HeaderRepeat && c.TableMeta != nil && len(c.TableMeta.Header) > 0 {
		b.WriteString(strings.Join(c.TableMeta.Header, ","))
		b.WriteString("\n")
	}
	b.WriteString(c.Text)
	return b.String()
}

// RowTextOf 行级派生文本复算单点（kb_chunk 行四列 → ChunkTextOf 口径；**复算面与写侧同源，禁第二处**）：
//   - text = verbatim 正文（kb_chunk.text，锚真源）；
//   - headingPath / tableMeta = jsonb 列原文（NULL/空 = 缺席；JSON 非法 ⇒ 响亮报错——不静默丢前缀）；
//   - configSnapshot = kb_chunk.config_snapshot（NULL/空 = 代码缺省档；非法 ⇒ 响亮报错——配置未知
//     不得静默复算，口径可能整体漂移）。
//
// 消费者：embed 包嵌入文本派生（embedTextOf 委托本函数）、aigo kb reindex 存量回填（match_text 列）。
// 写侧（ingest.insertChunks）用内存态 ChunkTextOf，与本函数同一单点函数，口径恒同。
func RowTextOf(text string, headingPath, tableMeta, configSnapshot *string) (string, error) {
	c := Chunk{Text: text}
	if headingPath != nil && strings.TrimSpace(*headingPath) != "" {
		if err := json.Unmarshal([]byte(*headingPath), &c.HeadingPath); err != nil {
			return "", fmt.Errorf("kb: 切片标题路径解析失败: %w", err)
		}
	}
	if tableMeta != nil && strings.TrimSpace(*tableMeta) != "" {
		var meta TableMeta
		if err := json.Unmarshal([]byte(*tableMeta), &meta); err != nil {
			return "", fmt.Errorf("kb: 切片表格元信息解析失败: %w", err)
		}
		c.TableMeta = &meta
	}
	cfg := Default()
	if configSnapshot != nil && strings.TrimSpace(*configSnapshot) != "" {
		parsed, err := EffectiveOn(cfg, []byte(*configSnapshot))
		if err != nil {
			return "", fmt.Errorf("kb: 切片配置快照非法（派生文本口径未知）: %w", err)
		}
		cfg = parsed
	}
	return ChunkTextOf(c, cfg), nil
}

// ── 哈希口径 ─────────────────────────────────────────────────────────────────

// TextHash 切片正文 sha1（= kb_chunk.text_hash；口径 = verbatim 正文，不含派生前缀）
func TextHash(text string) string { return sha1Hex([]byte(text)) }

// ContentSHA256 原文内容身份 sha256（file 来源 = kb_blob.sha256 复算；text 来源 = sha256(原文字节)）
func ContentSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// BuildHash 构建身份（C2）：sha1(内容身份 | config_hash | chunker_version | extractor@version)。
//
// 四项任一为空 ⇒ 响亮报错（缺分量不得进 build：漏项会让不同口径共用同一 build）。
// 重切触发判定 = 本值变化（只比 chunk-affecting 键；嵌入模型/标签/可见性不进本函数）。
func BuildHash(contentIdentity, configHash, chunkerVersion, extractorAtVersion string) (string, error) {
	parts := []string{
		strings.TrimSpace(contentIdentity),
		strings.TrimSpace(configHash),
		strings.TrimSpace(chunkerVersion),
		strings.TrimSpace(extractorAtVersion),
	}
	names := []string{"内容身份", "config_hash", "chunker_version", "extractor@version"}
	for i, p := range parts {
		if p == "" {
			return "", fmt.Errorf("kb build_hash 缺分量：%s", names[i])
		}
	}
	return sha1Hex([]byte(strings.Join(parts, "|"))), nil
}

// sha1Hex 十六进制小写 sha1（内容寻址口径单点）
func sha1Hex(data []byte) string {
	sum := sha1.Sum(data)
	return hex.EncodeToString(sum[:])
}

// ── 配置模型（C3：三层浅覆盖 + 未知键拒绝 + 值域校验）────────────────────────

// 配置值域（写侧校验单点；「值域校验在写侧（如 target∈[128,4096]）」）
const (
	MinTarget       = 128  // target 下限
	MaxTarget       = 4096 // target 上限
	MinHardCap      = 128  // hard_cap 下限（另须 ≥ target）
	MaxHardCap      = 8192 // hard_cap 上限
	MinSemanticSize = 16   // semantic min 下限
	MaxSemanticSize = 8192 // semantic max 上限
	MaxRowsPerChunk = 1000 // rows_per_chunk 上限
	MinSemanticQ    = 0.0  // threshold 开区间下界（不含）
	MaxSemanticQ    = 1.0  // threshold 上界（含）
	MinSemanticWin  = 1    // window 下限
	MaxSemanticWin  = 16   // window 上限
	MinChunkLevel   = 1    // levels 下界
	MaxChunkLevel   = 6    // levels 上界
)

// configKeys 配置键闭集（未知键 ⇒ 响亮拒绝；加键 = 改本闭集 + 校验分支）
var configKeys = map[string]bool{
	"strategy": true, "target": true, "overlap": true, "hard_cap": true, "tail_merge": true,
	"heading_prefix": true, "levels": true,
	"window": true, "threshold": true, "min": true, "max": true,
	"rows_per_chunk": true, "header_repeat": true, "min_rows": true,
}

// Config 有效切片配置（resolved 形态：解析后所有键都有值；规范 JSON 即 config_snapshot 载体）。
//
// 闭集**全部为 chunk-affecting 键**（重切触发条件所指）；嵌入模型/标签/可见性不进本配置
// （其变更只触发重嵌/重建索引，不触发重切）。
type Config struct {
	Strategy      string  `json:"strategy"`       // 空 = 由格式路由决定（RouteFormat）
	Target        int     `json:"target"`         // 目标块长（rune）
	Overlap       int     `json:"overlap"`        // 超帽切分重叠（rune；仅超帽切分启用）
	HardCap       int     `json:"hard_cap"`       // 硬帽（rune）
	TailMerge     int     `json:"tail_merge"`     // 尾块合并阈值（rune；< target/4 缺省；0=不合并）
	HeadingPrefix bool    `json:"heading_prefix"` // 标题路径前置进派生文本
	Levels        []int   `json:"levels"`         // heading 生效层级（1..6 子集，升序去重）
	Window        int     `json:"window"`         // semantic 句窗
	Threshold     float64 `json:"threshold"`      // semantic 相似度**分位**阈值（0,1]
	Min           int     `json:"min"`            // semantic 最小块长（rune）
	Max           int     `json:"max"`            // semantic/code 最大块长（rune）
	RowsPerChunk  int     `json:"rows_per_chunk"` // table 每块数据行数
	HeaderRepeat  bool    `json:"header_repeat"`  // table 表头重复（进派生文本）
	MinRows       int     `json:"min_rows"`       // table 尾块最小数据行数

	// tailMergeSet 解析期标记（不落 JSON/不进哈希）：tail_merge 是否被任一层显式给出。
	// 未显式 ⇒ 后续层叠加时仍按最终 target/4 派生（EffectiveOn 会把派生值从基里摘掉）。
	tailMergeSet bool
}

// Default 代码缺省档（C1 继承：512/64）。TailMerge=0 表示**未显式设置** ⇒ 解析时按 target/4 派生
// （「tail_merge=target/4」）；显式 0（用户层 JSON 写 0）= 不合并尾块。
func Default() Config {
	return Config{
		Target:        defaultTarget,
		Overlap:       defaultOverlap,
		HardCap:       defaultHardCap,
		HeadingPrefix: true,
		Levels:        []int{1, 2, 3, 4, 5, 6},
		Window:        defaultWindow,
		Threshold:     defaultThreshold,
		Min:           defaultSemanticMin,
		Max:           defaultMax,
		RowsPerChunk:  defaultRowsPerChunk,
		HeaderRepeat:  true,
		MinRows:       defaultMinRows,
	}
}

// 缺省档常量（表「参数（默认档）」；改档必须与融合档同批配对读数）
const (
	defaultTarget       = 512
	defaultOverlap      = 64
	defaultHardCap      = 1024
	defaultWindow       = 3
	defaultThreshold    = 0.25 // 分位阈值缺省档（只钉「分位」，取值待读数；语义档缺省关）
	defaultSemanticMin  = 128
	defaultMax          = 1024
	defaultRowsPerChunk = 50
	defaultMinRows      = 3
)

// Global 全局层解析（C3）：插件配置的 kb.chunk 段（内嵌缺省 ∪ configs 覆盖，可空）∪ 代码缺省
// → 校验后的有效配置。未知键/值域外 ⇒ 响亮报错（防配置漂移）。
func Global() (Config, error) {
	raw := kbcfg.Get().Chunk
	if len(raw) == 0 {
		return Default(), nil
	}
	layer, err := json.Marshal(raw)
	if err != nil {
		return Config{}, fmt.Errorf("kb: 全局切片配置序列化失败: %w", err)
	}
	return Effective(layer)
}

// Effective 三层浅覆盖（C3）：以代码缺省为底，按 global ▷ library ▷ document 顺序叠加层。
// 纯函数（不读配置不查库）；nil/空白层忽略；未知键与值域外一律响亮报错。单点合并，禁第二处实现。
func Effective(layers ...[]byte) (Config, error) { return EffectiveOn(Default(), layers...) }

// EffectiveOn 在已解析的基（如 Global() 结果）上叠加 library/document 层（同 Effective 语义）。
func EffectiveOn(base Config, layers ...[]byte) (Config, error) {
	merged := map[string]any{}
	if err := layerMapInto(base, merged); err != nil {
		return Config{}, err
	}
	for i, raw := range layers {
		m, err := parseLayer(raw)
		if err != nil {
			return Config{}, fmt.Errorf("第 %d 层切片配置非法: %w", i+1, err)
		}
		for k, v := range m {
			merged[k] = v
		}
	}
	return decodeConfig(merged)
}

// ValidateLayer 写侧单层校验（kb_library.chunk_config / kb_document.chunk_config 入库前调用）：
// 合法 JSON 对象 + 键闭集（未知键拒绝）+ 逐键类型/值域。跨键约束（如 overlap<target）在
// EffectiveOn 全量解析时校验——单层可能只给部分键。
func ValidateLayer(raw []byte) error {
	_, err := parseLayer(raw)
	return err
}

// Canonical 规范 JSON（config_snapshot 载体）：字段序固定、Levels 升序去重 ⇒ 复算稳定。
func (c Config) Canonical() string {
	b, err := json.Marshal(c)
	if err != nil { // Config 全为标量/整型切片，Marshal 不失败（防御分支）
		return ""
	}
	return string(b)
}

// Hash 有效配置 sha1（= config_hash；可自 config_snapshot 复算一致）
func (c Config) Hash() string { return sha1Hex([]byte(c.Canonical())) }

// AffectsChunk 重切触发比对（「只比 chunk-affecting 键」）：本闭集全部为 chunk-affecting 键，
// 故判据 = 规范形态不等。调用方亦可直接比 build_hash（含本值）——两者等价。
func (c Config) AffectsChunk(other Config) bool { return c.Canonical() != other.Canonical() }

// layerMapInto 把已解析配置摊平回层 map（供 EffectiveOn 以「缺省为底」起步）。
// tail_merge 未显式设置（tailMergeSet=false）时不落 map ⇒ 保留「按最终 target/4 派生」语义。
func layerMapInto(cfg Config, out map[string]any) error {
	raw, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("kb: 基配置序列化失败: %w", err)
	}
	m, err := parseLayer(raw)
	if err != nil {
		return fmt.Errorf("kb: 基配置非法: %w", err)
	}
	if !cfg.tailMergeSet {
		delete(m, "tail_merge")
	}
	for k, v := range m {
		out[k] = v
	}
	return nil
}

// parseLayer 单层解析：空白 = 空层；否则须为 JSON 对象（键闭集 + 逐键值域校验）。
// 只校验不改写（值保持 JSON 解码形态，统一交由 decodeConfig 收口——避免两处归一化口径）。
func parseLayer(raw []byte) (map[string]any, error) {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return map[string]any{}, nil
	}
	var m map[string]any
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("不是合法 JSON 对象: %w", err)
	}
	for k, v := range m {
		if !configKeys[k] {
			return nil, fmt.Errorf("未知配置键: %q（闭集：%s）", k, strings.Join(configKeyNames(), " / "))
		}
		if _, err := normalizeConfigValue(k, v); err != nil {
			return nil, err
		}
	}
	return m, nil
}

// configKeyNames 键闭集清单（报错/展示用）
func configKeyNames() []string {
	out := make([]string, 0, len(configKeys))
	for k := range configKeys {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// decodeConfig 合并 map → Config（键全部校验后；跨键约束在此收口）
func decodeConfig(m map[string]any) (Config, error) {
	cfg := Config{}
	var err error

	if cfg.Strategy, err = stringValue(m, "strategy"); err != nil {
		return Config{}, err
	}
	if cfg.Strategy != "" && !ValidStrategy(cfg.Strategy) {
		return Config{}, fmt.Errorf("strategy 未登记: %q（闭集：%s）", cfg.Strategy, strings.Join(strategyNames(), " / "))
	}
	if cfg.Target, err = intValue(m, "target"); err != nil {
		return Config{}, err
	}
	if cfg.Overlap, err = intValue(m, "overlap"); err != nil {
		return Config{}, err
	}
	if cfg.HardCap, err = intValue(m, "hard_cap"); err != nil {
		return Config{}, err
	}
	_, tailMergeSet := m["tail_merge"]
	if tailMergeSet {
		if cfg.TailMerge, err = intValue(m, "tail_merge"); err != nil {
			return Config{}, err
		}
	}
	if cfg.HeadingPrefix, err = boolValue(m, "heading_prefix"); err != nil {
		return Config{}, err
	}
	if cfg.Levels, err = levelsValue(m, "levels"); err != nil {
		return Config{}, err
	}
	if cfg.Window, err = intValue(m, "window"); err != nil {
		return Config{}, err
	}
	if cfg.Threshold, err = floatValue(m, "threshold"); err != nil {
		return Config{}, err
	}
	if cfg.Min, err = intValue(m, "min"); err != nil {
		return Config{}, err
	}
	if cfg.Max, err = intValue(m, "max"); err != nil {
		return Config{}, err
	}
	if cfg.RowsPerChunk, err = intValue(m, "rows_per_chunk"); err != nil {
		return Config{}, err
	}
	if cfg.HeaderRepeat, err = boolValue(m, "header_repeat"); err != nil {
		return Config{}, err
	}
	if cfg.MinRows, err = intValue(m, "min_rows"); err != nil {
		return Config{}, err
	}

	// tail_merge 派生：任一层未显式给 ⇒ target/4（「tail_merge=target/4」，按**最终** target 派生）
	cfg.tailMergeSet = tailMergeSet
	if !tailMergeSet {
		cfg.TailMerge = cfg.Target / 4
	}

	// 跨键约束
	if cfg.Overlap >= cfg.Target {
		return Config{}, fmt.Errorf("overlap(%d) 必须小于 target(%d)", cfg.Overlap, cfg.Target)
	}
	if cfg.HardCap < cfg.Target {
		return Config{}, fmt.Errorf("hard_cap(%d) 必须不小于 target(%d)", cfg.HardCap, cfg.Target)
	}
	if cfg.TailMerge > cfg.HardCap {
		return Config{}, fmt.Errorf("tail_merge(%d) 不得大于 hard_cap(%d)", cfg.TailMerge, cfg.HardCap)
	}
	if cfg.Min > cfg.Max {
		return Config{}, fmt.Errorf("min(%d) 不得大于 max(%d)", cfg.Min, cfg.Max)
	}
	if cfg.MinRows > cfg.RowsPerChunk {
		return Config{}, fmt.Errorf("min_rows(%d) 不得大于 rows_per_chunk(%d)", cfg.MinRows, cfg.RowsPerChunk)
	}
	return cfg, nil
}

// normalizeConfigValue 单键归一与值域校验（parseLayer 与写侧校验同用）
func normalizeConfigValue(key string, v any) (any, error) {
	one := map[string]any{key: v}
	switch key {
	case "strategy":
		s, err := stringValue(one, key)
		if err != nil {
			return nil, err
		}
		if s != "" && !ValidStrategy(s) {
			return nil, fmt.Errorf("strategy 未登记: %q（闭集：%s）", s, strings.Join(strategyNames(), " / "))
		}
		return s, nil
	case "target":
		return intValue(one, key, rangeCheck{MinTarget, MaxTarget, "target"})
	case "overlap":
		return intValue(one, key, rangeCheck{0, MaxTarget, "overlap"})
	case "hard_cap":
		return intValue(one, key, rangeCheck{MinHardCap, MaxHardCap, "hard_cap"})
	case "tail_merge":
		return intValue(one, key, rangeCheck{0, MaxHardCap, "tail_merge"})
	case "heading_prefix", "header_repeat":
		return boolValue(one, key)
	case "levels":
		return levelsValue(one, key)
	case "window":
		return intValue(one, key, rangeCheck{MinSemanticWin, MaxSemanticWin, "window"})
	case "threshold":
		f, err := floatValue(one, key)
		if err != nil {
			return nil, err
		}
		if f <= MinSemanticQ || f > MaxSemanticQ {
			return nil, fmt.Errorf("threshold 值域: (0,1]，实际 %v", f)
		}
		return f, nil
	case "min":
		return intValue(one, key, rangeCheck{MinSemanticSize, MaxSemanticSize, "min"})
	case "max":
		return intValue(one, key, rangeCheck{MinSemanticSize, MaxSemanticSize, "max"})
	case "rows_per_chunk":
		return intValue(one, key, rangeCheck{1, MaxRowsPerChunk, "rows_per_chunk"})
	case "min_rows":
		return intValue(one, key, rangeCheck{1, MaxRowsPerChunk, "min_rows"})
	default:
		return nil, fmt.Errorf("未知配置键: %q", key)
	}
}

// rangeCheck 整数键值域约束
type rangeCheck struct {
	min, max int
	name     string
}

// intValue 整数取值（JSON number 须为整值；yaml 侧经 map 已是数字）
func intValue(m map[string]any, key string, rc ...rangeCheck) (int, error) {
	raw, ok := m[key]
	if !ok {
		return 0, fmt.Errorf("缺配置键: %s", key)
	}
	var n int
	switch v := raw.(type) {
	case json.Number:
		i, err := v.Int64()
		if err != nil {
			return 0, fmt.Errorf("%s 须为整数，实际 %v", key, raw)
		}
		n = int(i)
	case float64:
		if v != float64(int64(v)) {
			return 0, fmt.Errorf("%s 须为整数，实际 %v", key, raw)
		}
		n = int(v)
	case int:
		n = v
	case int64:
		n = int(v)
	default:
		return 0, fmt.Errorf("%s 须为整数，实际 %T", key, raw)
	}
	if len(rc) > 0 {
		c := rc[0]
		if n < c.min || n > c.max {
			return 0, fmt.Errorf("%s 值域 [%d,%d]，实际 %d", c.name, c.min, c.max, n)
		}
	}
	return n, nil
}

// floatValue 浮点取值（整数亦接受）
func floatValue(m map[string]any, key string) (float64, error) {
	raw, ok := m[key]
	if !ok {
		return 0, fmt.Errorf("缺配置键: %s", key)
	}
	switch v := raw.(type) {
	case json.Number:
		f, err := v.Float64()
		if err != nil {
			return 0, fmt.Errorf("%s 须为数字，实际 %v", key, raw)
		}
		return f, nil
	case float64:
		return v, nil
	case int:
		return float64(v), nil
	case int64:
		return float64(v), nil
	default:
		return 0, fmt.Errorf("%s 须为数字，实际 %T", key, raw)
	}
}

// boolValue 布尔取值
func boolValue(m map[string]any, key string) (bool, error) {
	raw, ok := m[key]
	if !ok {
		return false, fmt.Errorf("缺配置键: %s", key)
	}
	b, ok := raw.(bool)
	if !ok {
		return false, fmt.Errorf("%s 须为布尔，实际 %T", key, raw)
	}
	return b, nil
}

// stringValue 字符串取值
func stringValue(m map[string]any, key string) (string, error) {
	raw, ok := m[key]
	if !ok {
		return "", fmt.Errorf("缺配置键: %s", key)
	}
	s, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("%s 须为字符串，实际 %T", key, raw)
	}
	return s, nil
}

// levelsValue heading 层级取值（数组；升序去重；元素 ∈ [1,6]）
func levelsValue(m map[string]any, key string) ([]int, error) {
	raw, ok := m[key]
	if !ok {
		return nil, fmt.Errorf("缺配置键: %s", key)
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("%s 须为整数数组，实际 %T", key, raw)
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("%s 不得为空数组", key)
	}
	seen := map[int]bool{}
	out := make([]int, 0, len(items))
	for _, item := range items {
		n, err := intValue(map[string]any{"v": item}, "v")
		if err != nil {
			return nil, fmt.Errorf("%s 元素须为整数: %v", key, item)
		}
		if n < MinChunkLevel || n > MaxChunkLevel {
			return nil, fmt.Errorf("%s 元素值域 [%d,%d]，实际 %d", key, MinChunkLevel, MaxChunkLevel, n)
		}
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	sort.Ints(out)
	return out, nil
}
