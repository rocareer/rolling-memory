// plugin: kb v1.0.0
// 知识库导入管道 · 切分执行与预算预检。
//
// 分工（禁第二处口径）：
//   - 策略注册表 / 三层配置合并 / 未知键拒绝 / 切分算法 / 锚与派生文本 / build_hash = chunk 包
//     （service/kb/chunk；按该包「管道侧用法」逐字照抄）；
//   - 文档锚前缀（kb:doc:<id>）、切片落库与幂等重跑、换出、成本估算与预算闸 = 本包（本文件）。
//
// 幂等链（层层可复算，禁靠「跑没跑过」判断）：
//
//	blob = sha256（kb_blob 主键，取用时复算校验）
//	提取物 = uk(document_id, extractor, version)（重切不重解；state.go）
//	切片   = uk(document_id, build_hash, seq)（本文件：同 build 完整在位即跳过，残缺则整体重写）
//	向量   = uk(chunk_id, model, text_hash)（embed 包：ON CONFLICT DO NOTHING）
//
// 换出语义（C2）：新 build 的全部切片与向量就绪后，单事务内「active_build 原子换出 + 删旧 build
// 切片行/向量行」；换出前旧 build 保持可服务（嵌入失败 ⇒ 旧 build 不受影响）。
package ingest

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ai-go-hub/ai-go-admin/plugins/kb/model"
	"github.com/ai-go-hub/ai-go-admin/plugins/kb/service/kb/aiport"
	"github.com/ai-go-hub/ai-go-admin/plugins/kb/service/kb/chunk"

	"github.com/ai-go-hub/ai-go-admin/plugins/kb/kbcfg"
	"gorm.io/gorm"
)

// ==================== 计划解析（chunk 包三层配置 + 策略路由 + build 身份） ====================

// Plan 一次切分的完整计划（有效配置 + 身份要素；build 身份四元组之一）
type Plan struct {
	Config         chunk.Config // 有效配置（global ▷ library ▷ document 浅覆盖后）
	Strategy       string       // 生效策略名（配置指定或格式路由）
	ChunkerVersion string       // chunker_version（进 build_hash 与锚复算）
	BuildHash      string       // build_hash（chunk.BuildHash 产出）
	DocLocator     string       // 文档锚前缀（kb:doc:<id>；块锚 = <doc_locator>#pN）
	ExtractorID    string       // extractor@version（build 身份组成项）
	ContentSHA256  string       // 原文内容身份（build 身份组成项；kb_blob.sha256）
	AgentIdentity  string       // semantic 档嵌入 agent 身份（kb-embed@<version>；其余策略空）
}

// ResolvePlan 解析一次切分的完整计划：三层配置 → 策略路由 → chunker_version → build_hash。
// 未知键/值域外/格式未登记/semantic 缺 agent 身份 ⇒ 一律响亮报错（禁静默兜底）。
func ResolvePlan(ctx context.Context, db *gorm.DB, doc *model.KBDocument, format, extractText, contentSHA256, extractorID string) (Plan, error) {
	if db == nil || doc == nil || doc.ID == 0 {
		return Plan{}, errors.New("kb: 切片计划参数非法")
	}
	if strings.TrimSpace(extractorID) == "" || strings.TrimSpace(contentSHA256) == "" {
		return Plan{}, errors.New("kb: 切片计划缺身份要素（extractor@version / 内容 sha256）")
	}
	// 三层浅覆盖：global（config/kb.yaml kb.chunk 段 ∪ 代码缺省）▷ library ▷ document
	cfg, err := chunk.Global()
	if err != nil {
		return Plan{}, fmt.Errorf("kb: 全局切片配置解析失败: %w", err)
	}
	libCfg, err := libraryChunkConfig(ctx, db, doc.LibraryID)
	if err != nil {
		return Plan{}, err
	}
	layers := make([][]byte, 0, 2)
	if libCfg != nil {
		layers = append(layers, []byte(*libCfg))
	}
	if doc.ChunkConfig != nil && strings.TrimSpace(*doc.ChunkConfig) != "" {
		layers = append(layers, []byte(*doc.ChunkConfig))
	}
	cfg, err = chunk.EffectiveOn(cfg, layers...)
	if err != nil {
		return Plan{}, fmt.Errorf("kb: 切片配置合并失败（库/文档层）: %w", err)
	}

	// 策略路由：配置显式指定优先；空 ⇒ 格式路由（txt 检出标题模式升 heading）
	strategy := strings.TrimSpace(cfg.Strategy)
	if strategy == "" {
		if strings.EqualFold(format, FormatTxt) {
			strategy = chunk.RouteTXT(extractText)
		} else {
			strategy, err = chunk.RouteFormat(format)
			if err != nil {
				return Plan{}, err
			}
		}
		cfg.Strategy = strategy // 生效策略进规范形态 ⇒ 进 config_snapshot 与 config_hash
	}

	// semantic 档：嵌入锚身份入锚（C4）——身份缺失响亮失败
	agentIdentity := ""
	if strategy == "semantic" {
		agentIdentity, err = anchorIdentityOf(ctx)
		if err != nil {
			return Plan{}, err
		}
	}
	chunkerVersion, err := chunk.StrategyVersion(strategy, agentIdentity)
	if err != nil {
		return Plan{}, err
	}
	buildHash, err := chunk.BuildHash(contentSHA256, cfg.Hash(), chunkerVersion, extractorID)
	if err != nil {
		return Plan{}, err
	}
	return Plan{
		Config: cfg, Strategy: strategy, ChunkerVersion: chunkerVersion,
		BuildHash: buildHash, DocLocator: DocLocator(doc.ID), ExtractorID: extractorID,
		ContentSHA256: contentSHA256, AgentIdentity: agentIdentity,
	}, nil
}

// libraryChunkConfig 库级切片配置（NULL = 未配置；库不存在 = nil）
func libraryChunkConfig(ctx context.Context, db *gorm.DB, libraryID uint) (*string, error) {
	row, err := gorm.G[model.KBLibrary](db).Select("chunk_config").Where("id = ?", libraryID).First(ctx)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("kb: 读库级切片配置失败: %w", err)
	}
	return row.ChunkConfig, nil
}

// DocLocator 文档级锚前缀（可反解析回文档：kb:doc:<id>；块锚 = DocLocator#p<seq>，seq 从 1 起）。
func DocLocator(documentID uint) string {
	return "kb:doc:" + uitoa(documentID)
}

// defaultEmbedAgent 嵌入调用档名缺省（产品适配器映射到其 ai agent，如 kb-embed；框架默认实现忽略）。
// 与 embed 包 DefaultAgent 同值（改叙事前先读两处引用）。
const defaultEmbedAgent = "kb-embed"

// anchorIdentityOf 语义切分锚身份（经 aiport 端口；semantic 档入 chunker_version）。
// Provider 未注册/身份不可用（模型未配置）⇒ 响亮报错（禁以空身份入锚）。
func anchorIdentityOf(ctx context.Context) (string, error) {
	provider, err := aiport.CurrentEmbedder()
	if err != nil {
		return "", err
	}
	identity, err := provider.AnchorIdentity(ctx)
	if err != nil {
		return "", fmt.Errorf("kb: semantic 档嵌入锚身份解析失败: %w", err)
	}
	if strings.TrimSpace(identity) == "" {
		return "", errors.New("kb: semantic 档嵌入锚身份为空（Provider 未就绪）")
	}
	return identity, nil
}

// kbLibraryScope 库维 scope（`kb:library:<id>`；0 = 不设 scope）——出站脱敏域声明用
// （与 embed 包同款各自持有，避跨模块耦合）。
func kbLibraryScope(libraryID uint) string {
	if libraryID == 0 {
		return ""
	}
	return fmt.Sprintf("kb:library:%d", libraryID)
}

// SplitDocument 切分提取文本（chunk 包『管道侧用法』照抄）：
// Get(name) → Chunker.Chunk → Finalize（seq/locator/text_hash 规整）→ VerifyVerbatim（锚可复算+逐字）。
func SplitDocument(ctx context.Context, doc *model.KBDocument, plan Plan, extractText string) ([]chunk.Chunk, error) {
	if doc == nil {
		return nil, errors.New("kb: 切分参数非法")
	}
	chunker, err := chunk.Get(plan.Strategy)
	if err != nil {
		return nil, err
	}
	in := chunk.Input{
		Text:          extractText,
		Locator:       plan.DocLocator,
		Config:        plan.Config,
		AgentIdentity: plan.AgentIdentity,
	}
	if plan.Strategy == "semantic" {
		in.Embedder = semanticEmbedder(kbLibraryScope(doc.LibraryID))
	}
	chunks, err := chunker.Chunk(ctx, in)
	if err != nil {
		return nil, fmt.Errorf("kb: 切分失败（策略 %s）: %w", plan.Strategy, err)
	}
	chunks, err = chunk.Finalize(plan.DocLocator, chunks) // 空白块剔除 + seq/locator/text_hash 规整
	if err != nil {
		return nil, err
	}
	if err := chunk.VerifyVerbatim(extractText, chunks); err != nil {
		return nil, err // 写库前自检：锚不可复算/非逐字一律不落库
	}
	return chunks, nil
}

// semanticEmbedder 句向量入口（semantic 档；经 aiport 端口）。
// 调用元数据：Face=kb.ingest（产品适配器据此注入出站声明）、scope=kb:library:<id>、Kind=sentence。
func semanticEmbedder(scope string) chunk.Embedder {
	return func(ctx context.Context, texts []string) ([][]float32, error) {
		if len(texts) == 0 {
			return nil, nil
		}
		provider, err := aiport.CurrentEmbedder()
		if err != nil {
			return nil, err
		}
		res, err := provider.Embed(ctx, aiport.CallMeta{
			Face: "kb.ingest", Scope: scope, Kind: "sentence",
			Agent: defaultEmbedAgent, Source: "kb",
		}, texts)
		if err != nil {
			return nil, err
		}
		if len(res.Vectors) != len(texts) {
			return nil, fmt.Errorf("kb: 句向量条数不符（%d != %d）", len(res.Vectors), len(texts))
		}
		return res.Vectors, nil
	}
}

// ==================== 切片落库（同 build 幂等） ====================

// EnsureChunks 保证 (document_id, build_hash) 的切片完整在位：
//   - 完整在位（行数 == len(chunks)）⇒ 直接返回（**不重写**：chunk.id 稳定 ⇒ 向量行不失效）；
//   - 缺失/残缺（上次崩溃）⇒ 单事务删该 build 既有行（含其向量行）后整体写入。
//
// 返回写入数（0 = 复用既有行）。匹配面文本（match_text）写侧单点在此（chunk.ChunkTextOf 派生文本）；
// tsv 为 DB 生成列（恒自 match_text 派生——migrations/1_core.up.sql 的 match_text 派生列），写侧不再写该列（含 FTS 配置入参已撤）。
func EnsureChunks(ctx context.Context, db *gorm.DB, doc *model.KBDocument, plan Plan, chunks []chunk.Chunk, budget BudgetLimits) (int, error) {
	if db == nil || doc == nil || doc.ID == 0 || plan.BuildHash == "" {
		return 0, errors.New("kb: 落切片参数非法")
	}
	if len(chunks) == 0 {
		return 0, errors.New("kb: 切片结果为空（响亮失败）")
	}

	existing, err := countChunksForBuild(ctx, db, doc.ID, plan.BuildHash)
	if err != nil {
		return 0, err
	}
	if existing == int64(len(chunks)) {
		return 0, nil // 完整在位：同 build 重跑行数不变（幂等）
	}

	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if existing > 0 {
			// 残缺残留：先删向量（chunk_id 外键面）再删切片行
			if err := deleteVectorsForBuild(ctx, tx, doc.ID, plan.BuildHash); err != nil {
				return err
			}
			if _, err := gorm.G[map[string]any](tx).Table(tableName(tx, model.KBChunk{})).
				Where("document_id = ? AND build_hash = ?", doc.ID, plan.BuildHash).
				Delete(ctx); err != nil {
				return fmt.Errorf("kb: 清残缺切片失败: %w", err)
			}
		}
		return insertChunks(ctx, tx, doc, plan, chunks, budget)
	})
	if err != nil {
		return 0, err
	}
	return len(chunks), nil
}

// countChunksForBuild 该 build 的切片行数
func countChunksForBuild(ctx context.Context, db *gorm.DB, documentID uint, buildHash string) (int64, error) {
	n, err := gorm.G[model.KBChunk](db).
		Where("document_id = ? AND build_hash = ?", documentID, buildHash).
		Count(ctx, "id")
	if err != nil {
		return 0, fmt.Errorf("kb: 统计切片失败: %w", err)
	}
	return n, nil
}

// deleteVectorsForBuild 删该 build 切片行的向量行（重写/换出前；chunk_id 属该 build 集）
func deleteVectorsForBuild(ctx context.Context, tx *gorm.DB, documentID uint, buildHash string) error {
	sql := "DELETE FROM " + tableName(tx, ChunkVectorRow{}) + " WHERE chunk_id IN (SELECT id FROM " +
		tableName(tx, model.KBChunk{}) + " WHERE document_id = ? AND build_hash = ?)"
	if err := tx.WithContext(ctx).Exec(sql, documentID, buildHash).Error; err != nil {
		return fmt.Errorf("kb: 清切片向量失败: %w", err)
	}
	return nil
}

// insertChunks 批量写入切片行（多值 INSERT 分批；值全参数化、标识符常量）。
// 匹配面文本列 match_text = chunk.ChunkTextOf 派生文本（文本口径单点；与嵌入文本同源）；
// verbatim 正文进 text 列（锚/逐字核对真源不变）；tsv 为 DB 生成列（自 match_text 派生，写侧不写）。
func insertChunks(ctx context.Context, tx *gorm.DB, doc *model.KBDocument, plan Plan, chunks []chunk.Chunk, budget BudgetLimits) error {
	table := tableName(tx, model.KBChunk{})
	const cols = 22
	const batch = 200
	snapshot := snapshotOrNil(plan.Config.Canonical())
	for start := 0; start < len(chunks); start += batch {
		end := start + batch
		if end > len(chunks) {
			end = len(chunks)
		}
		part := chunks[start:end]

		var b strings.Builder
		b.WriteString("INSERT INTO " + table + " (library_id, document_id, build_hash, config_hash, chunker," +
			" chunker_version, seq, locator, char_start, char_end, heading_path, page_from, page_to," +
			" time_from, time_to, table_meta, text, text_hash, token_count, config_snapshot, match_text, created_at) VALUES ")
		args := make([]any, 0, len(part)*cols)
		for i := range part {
			c := part[i]
			if i > 0 {
				b.WriteString(",")
			}
			b.WriteString("(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,NOW())")
			cjk, other := CountWordsLike(c.Text)
			// 匹配面文本 = ChunkTextOf 派生文本（单点：与嵌入文本同源；标题路径/表头重复词面经
			// exact/trgm/tsv 三面可达）。原仅 tsv 从派生文本生成（exact/trgm
			// 仍匹配 verbatim text），本批落 match_text 列并三面同切（migrations/1_core.up.sql 的 match_text 派生列）。
			// 存量行 match_text 为空串 = 未回填（aigo kb reindex 幂等复跑）。
			args = append(args,
				doc.LibraryID, doc.ID, plan.BuildHash, plan.Config.Hash(), plan.Strategy,
				plan.ChunkerVersion, c.Seq, c.Locator, c.CharStart, c.CharEnd,
				jsonOrNil(headingPathJSON(c.HeadingPath)), c.PageFrom, c.PageTo,
				c.TimeFrom, c.TimeTo, jsonOrNil(tableMetaJSON(c.TableMeta)), c.Text,
				c.TextHash, int64(EstimateTokens(cjk, other, budget)), snapshot, chunk.ChunkTextOf(c, plan.Config),
			)
		}
		if err := tx.WithContext(ctx).Exec(b.String(), args...).Error; err != nil {
			return fmt.Errorf("kb: 写入切片失败: %w", err)
		}
	}
	return nil
}

// headingPathJSON 标题路径 JSON（空 = nil ⇒ NULL 列）
func headingPathJSON(path []string) any {
	if len(path) == 0 {
		return nil
	}
	return path
}

// tableMetaJSON 表格元信息 JSON（空 = nil ⇒ NULL 列）
func tableMetaJSON(meta *chunk.TableMeta) any {
	if meta == nil {
		return nil
	}
	return meta
}

// jsonOrNil map/slice → *string（jsonb 列；nil 值保持 NULL）
func jsonOrNil(v any) *string {
	if v == nil {
		return nil
	}
	raw, err := marshalJSON(v)
	if err != nil {
		return nil
	}
	return &raw
}

// snapshotOrNil 配置快照（空串 = NULL）
func snapshotOrNil(s string) *string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return &s
}

// ==================== 换出（active_build 原子换出 + 删旧 build） ====================

// ErrBuildSuperseded 换出被取代（并发换出已删本 build 的切片行）：换出必须被拒——防「ready 但
// active_build 指向零行」的静默坏态（检索面 `c.build_hash = d.active_build` 恒不命中且不自愈）。
// 调用方按此语义收口：终态收工，**禁**退避重跑（重跑会重切重嵌重建行，把并发换出的结果翻回去且重复计费）。
var ErrBuildSuperseded = errors.New("kb: 本 build 切片行已不在位（已被并发换出删除），拒绝换出")

// SwitchBuild 换出：单事务内落 ready（active_build = buildHash）+ 删**其它** build 的切片行与向量行。
// 前置：新 build 的向量已全部就绪（调用方保证——嵌入失败不得换出）。
// beforeCommit 非 nil 时在换出完成、事务提交前调用（调用方在同一事务内落审计等；失败整事务回滚）。
// 返回删除的旧切片行数。
//
// 并发安全（已整改）：同一文档两个 build 在途时（kb:ingest×kb:rebuild 窗口/跨实例，
// 键内 build 不构成单飞），迟到者的删除条件 `build_hash <> ?` 会删光已换出的对方行，而自己的行
// 已被对方换出时删掉 ⇒ 末态 ready + active_build 指向零行。收口=「按文档串行化 + 换出前校验自方行在位」：
// MarkReady 的 UPDATE 取 kb_document 行锁 ⇒ 并发换出在**此事务内排队**；排队后（对方已提交）读到
// 本 build 行数 0 ⇒ 返回 ErrBuildSuperseded（整事务回滚，含 MarkReady）——校验必须在 MarkReady
// **之后**执行，否则读到的是对方删除前的快照，收不住。
func SwitchBuild(ctx context.Context, db *gorm.DB, documentID uint, buildHash string, beforeCommit func(ctx context.Context, tx *gorm.DB) error) (int64, error) {
	if db == nil || documentID == 0 || buildHash == "" {
		return 0, errors.New("kb: 换出参数非法")
	}
	var removed int64
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := MarkReady(ctx, tx, documentID, buildHash); err != nil {
			return err
		}
		inPlace, err := gorm.G[model.KBChunk](tx).
			Where("document_id = ? AND build_hash = ?", documentID, buildHash).
			Count(ctx, "id")
		if err != nil {
			return fmt.Errorf("kb: 换出前校验切片行失败: %w", err)
		}
		if inPlace == 0 {
			return fmt.Errorf("%w（document=%d build=%s）", ErrBuildSuperseded, documentID, buildHash)
		}
		sql := "DELETE FROM " + tableName(tx, ChunkVectorRow{}) + " WHERE chunk_id IN (SELECT id FROM " +
			tableName(tx, model.KBChunk{}) + " WHERE document_id = ? AND build_hash <> ?)"
		if err := tx.WithContext(ctx).Exec(sql, documentID, buildHash).Error; err != nil {
			return fmt.Errorf("kb: 清旧 build 向量失败: %w", err)
		}
		affected, err := gorm.G[map[string]any](tx).Table(tableName(tx, model.KBChunk{})).
			Where("document_id = ? AND build_hash <> ?", documentID, buildHash).
			Delete(ctx)
		if err != nil {
			return fmt.Errorf("kb: 删旧 build 切片失败: %w", err)
		}
		removed = int64(affected)
		if beforeCommit != nil {
			return beforeCommit(ctx, tx)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return removed, nil
}

// ActiveBuildOf 读文档当前生效 build（换出判定/巡检用；未产出 = ""）。
func ActiveBuildOf(ctx context.Context, db *gorm.DB, documentID uint) (string, error) {
	row, err := gorm.G[model.KBDocument](db).Select("active_build").Where("id = ?", documentID).First(ctx)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("kb: 读 active_build 失败: %w", err)
	}
	return row.ActiveBuild, nil
}

// ==================== 成本系数表与预算预检 ====================

// 成本系数缺省（config/kb.yaml kb.budget.* 未配时的代码缺省；0/负 = 取缺省）
const (
	// DefaultCJKTokenPerChar CJK 字符 → token 系数（初值；核对 = 模型端口侧调用日志回算，超差 20% 回查）
	DefaultCJKTokenPerChar = 0.6
	// DefaultNonCJKTokenPerChar 非 CJK 字符 → token 系数
	DefaultNonCJKTokenPerChar = 0.25
	// DefaultEmbedPricePerMillionTokens 嵌入单价（CNY/百万输入 token；快照价：0.125）
	DefaultEmbedPricePerMillionTokens = 0.125
)

// BudgetLimits 单作业预算（0 = 不设限；系数为 0/负取缺省；媒体单价 0 = 未配价按 0 计）。
type BudgetLimits struct {
	MaxDocsPerJob      int     // 单作业文档数上限
	MaxChunksPerJob    int     // 单作业切片数上限
	MaxCharsPerJob     int64   // 单作业字符预算（rune）
	MaxEstTokensPerJob float64 // 单作业 token 估算预算
	CJKTokenPerChar    float64 // 系数：CJK token/字
	NonCJKTokenPerChar float64 // 系数：非 CJK token/字符
	PricePerMillion    float64 // 嵌入单价（CNY/百万 token；仅作事后对账参考）
	//  媒体计价（OCR 页数×元/页、ASR 分钟×元/分钟；快照进 kb_cost_log 行的 unit_price）
	OCRPricePerPageCNY   float64 // OCR 单价（元/页；0=未配价）
	ASRPricePerMinuteCNY float64 // ASR 单价（元/分钟；0=未配价）
}

// WithDefaults 系数缺省回落（纯函数）
func (l BudgetLimits) WithDefaults() BudgetLimits {
	if l.CJKTokenPerChar <= 0 {
		l.CJKTokenPerChar = DefaultCJKTokenPerChar
	}
	if l.NonCJKTokenPerChar <= 0 {
		l.NonCJKTokenPerChar = DefaultNonCJKTokenPerChar
	}
	if l.PricePerMillion <= 0 {
		l.PricePerMillion = DefaultEmbedPricePerMillionTokens
	}
	return l
}

// EstimateTokens 成本估算（估算值必须标注「估算」）：CJK×系数 + 非 CJK×系数。
func EstimateTokens(cjk, other int, l BudgetLimits) float64 {
	l = l.WithDefaults()
	return float64(cjk)*l.CJKTokenPerChar + float64(other)*l.NonCJKTokenPerChar
}

// EstimateCostCNY 费用估算（估算；对账口径 = 模型端口侧调用日志的费用快照）。
func EstimateCostCNY(tokens float64, l BudgetLimits) float64 {
	l = l.WithDefaults()
	return tokens / 1e6 * l.PricePerMillion
}

// EstimateMediaCostCNY 媒体计价估算（OCR 页数×元/页 + ASR 分钟×元/分钟）。
// 单价 0（未配价）按 0 计——估算值如实标注，**禁虚构价格**（台账侧同步标 detail.price_unconfigured）。
func EstimateMediaCostCNY(pages int, minutes float64, l BudgetLimits) float64 {
	if pages < 0 {
		pages = 0
	}
	if minutes < 0 {
		minutes = 0
	}
	return float64(pages)*l.OCRPricePerPageCNY + minutes*l.ASRPricePerMinuteCNY
}

// BudgetInput 预检输入（作业入口按「本次将处理的量」填写；估算口径 = 本文件成本系数表）。
type BudgetInput struct {
	Docs    int     // 本次作业文档数（kb:ingest = 1；kb:rebuild = 本窗/全量按调用方口径）
	Chunks  int     // 本次作业切片数（重切为切分后实测；嵌入为待嵌块数）
	Chars   int64   // 本次作业字符数（rune）
	CJK     int     // CJK 字符数
	Other   int     // 非 CJK 字符数
	Pages   int     // 媒体量：页数（pdf/图片 OCR）
	Minutes float64 // 媒体量：分钟数（音视频 ASR）
}

// BudgetSuspended 预算/来源超限（固定文案；「挂起待批」载体——调用方落 failed_stage='budget' 并保留原文/产物）。
type BudgetSuspended struct {
	What      string  // 触发的维度（docs / chunks / chars / est_tokens / pages / minutes）
	Value     float64 // 实测/估算值
	Limit     float64 // 上限
	EstTokens float64 // token 估算（估算值）
	EstCost   float64 // 费用估算（CNY；估算值；含媒体计价子读数）
	MediaCost float64 // 媒体计价子读数（CNY；估算值；0=无从媒体量或未配价）
}

// Error 错误文案（进 failed_reason 与作业结果；不含用户输入回显）
func (e *BudgetSuspended) Error() string {
	if e == nil {
		return ""
	}
	base := fmt.Sprintf("kb: 预算超限待批（%s %.2f > 上限 %.2f；估算 %.0f token ≈ %.4f 元",
		e.What, e.Value, e.Limit, e.EstTokens, e.EstCost)
	if e.MediaCost > 0 {
		base += fmt.Sprintf("，其中媒体计价 ≈ %.4f 元", e.MediaCost)
	}
	return base + "）"
}

// CheckBudget 预算预检（作业入口；超限返回 *BudgetSuspended，永不虚构通过：估算口径如实标注）。
// 成本预检起 EstCost/MediaCost 并入媒体计价（pages×OCR单价 + minutes×ASR单价）。
func CheckBudget(in BudgetInput, l BudgetLimits) (*BudgetSuspended, error) {
	l = l.WithDefaults()
	est := EstimateTokens(in.CJK, in.Other, l)
	media := EstimateMediaCostCNY(in.Pages, in.Minutes, l)
	cost := EstimateCostCNY(est, l) + media
	sus := func(what string, v, lim float64) *BudgetSuspended {
		return &BudgetSuspended{What: what, Value: v, Limit: lim, EstTokens: est, EstCost: cost, MediaCost: media}
	}
	switch {
	case l.MaxDocsPerJob > 0 && in.Docs > l.MaxDocsPerJob:
		return sus("docs", float64(in.Docs), float64(l.MaxDocsPerJob)), nil
	case l.MaxChunksPerJob > 0 && in.Chunks > l.MaxChunksPerJob:
		return sus("chunks", float64(in.Chunks), float64(l.MaxChunksPerJob)), nil
	case l.MaxCharsPerJob > 0 && in.Chars > l.MaxCharsPerJob:
		return sus("chars", float64(in.Chars), float64(l.MaxCharsPerJob)), nil
	case l.MaxEstTokensPerJob > 0 && est > l.MaxEstTokensPerJob:
		return sus("est_tokens", est, l.MaxEstTokensPerJob), nil
	default:
		return nil, nil
	}
}

// ==================== 来源上限（失败兜底：max_pages / max_minutes） ====================

// 来源上限缺省（超限挂起待批）。文本类格式 pages=1/minutes=0（恒过）；多模态
// （pdf 页数 / 音视频分钟数）由各处理阶段回填实测读数后调 Check（同一挂起语义，缺 config 键随插件配置）。
const (
	// DefaultMaxPages 单文档页数上限
	DefaultMaxPages = 500
	// DefaultMaxMinutes 单文档时长上限（分钟）
	DefaultMaxMinutes = 120
)

// SourceLimits 单文档来源上限（0 = 取缺省）
type SourceLimits struct {
	MaxPages   int
	MaxMinutes float64
}

// WithDefaults 缺省回落（纯函数）
func (l SourceLimits) WithDefaults() SourceLimits {
	if l.MaxPages <= 0 {
		l.MaxPages = DefaultMaxPages
	}
	if l.MaxMinutes <= 0 {
		l.MaxMinutes = DefaultMaxMinutes
	}
	return l
}

// Check 超限判定（超限返回 *BudgetSuspended——同「挂起待批」语义与落点）。
func (l SourceLimits) Check(pages int, minutes float64) *BudgetSuspended {
	l = l.WithDefaults()
	if pages > l.MaxPages {
		return &BudgetSuspended{What: "pages", Value: float64(pages), Limit: float64(l.MaxPages)}
	}
	if minutes > l.MaxMinutes {
		return &BudgetSuspended{What: "minutes", Value: minutes, Limit: l.MaxMinutes}
	}
	return nil
}

// SourceLimitsOf 来源上限（插件配置 kb.limits.*；0/未配取代码缺省 500/120——**闸始终武装**，
// 与 kb.budget 四上限「0=不设限」语义不同）。
func SourceLimitsOf() SourceLimits {
	lim := kbcfg.Get().Limits
	return SourceLimits{MaxPages: lim.MaxPages, MaxMinutes: lim.MaxMinutes}
}

// SourceSuspendedError 来源超限（「挂起待批」）——媒体链的**前置抛点**：无文本层 pdf 在
// 光栅化/OCR（烧钱步骤）之前按页数检查；job 层 errors.As 识别后与 post-extract 检查同走
// suspendForBudget（failed_stage='budget'，产物保留，批准 = 复投）。不实现 error 链包装是故意的：
// 唯一消费者是 job 层的类型分支（哨兵文案不参与对外契约）。
type SourceSuspendedError struct {
	Suspended *BudgetSuspended
}

// Error 文案（同 BudgetSuspended；进 failed_reason）
func (e *SourceSuspendedError) Error() string {
	if e == nil || e.Suspended == nil {
		return "kb: 来源超限挂起待批"
	}
	return e.Suspended.Error()
}

// FTSConfigFixed tsv 生成配置单点（migrations/1_core.up.sql 的 match_text 派生列：tsv 已切换为 **DB 生成列**
// `to_tsvector('simple'::regconfig, match_text)` ⇒ 生成侧配置自 DDL 冻结为 simple；本常量即其
// 代码面同源声明。改此值 = 新迁移重建生成列（禁单侧改：写读同源由 DDL 保证，禁第二处口径）。
const FTSConfigFixed = "simple"

// ResolveFTSConfig tsv **查询**配置解析单点（收窄后）：空 / simple ⇒ simple；
// 其余值（含原 english 档）⇒ **响亮拒绝**——生成列已把 tsv 口径冻结为 simple（见 FTSConfigFixed），
// 查询侧再取别值会与生成列错位（静默失召回）；错位不许静默，故拒绝。原闭集 simple|english 的
// english 档随生成列切换收窄（如需 english 须新迁移重建生成列）。
func ResolveFTSConfig(name string) (string, error) {
	v := strings.ToLower(strings.TrimSpace(name))
	if v == "" || v == FTSConfigFixed {
		return FTSConfigFixed, nil
	}
	return "", fmt.Errorf("kb: fts 配置 %q 不受支持（tsv 生成列冻结为 %s——migrations/1_core.up.sql 的 match_text 派生列；如需 %q 须新迁移重建生成列）",
		name, FTSConfigFixed, v)
}

// uitoa uint → 十进制字符串（无 fmt 分配的小工具）
func uitoa(v uint) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}
