// plugin: kb v1.0.0
// Package embed 知识库向量生产（kb:ingest / kb:rebuild 的嵌入阶段）。
//
// 契约：
//   - 一切模型步骤经 aiport 端口唯一入口（aiport.Embedder；框架默认实现 = openai-compatible 直连，
//     下游部署注册自有 Provider 接管模型运行时——禁绕过 Provider 直连、禁第二处模型口径）：
//     调用档名经 CallMeta.Agent 透传（产品适配器映射到其 ai agent；框架默认实现忽略）；
//   - Source="kb" ⇒ 批处理优先级档与调用日志由产品适配器映射；框架默认实现只读观测；
//   - 嵌入文本口径 = chunk.ChunkTextOf 单点（含 heading_prefix；本包从 kb_chunk 行 +
//     config_snapshot 重建该口径）——口径保持逐字，改口径 = 重产向量行（不触发重切）；
//   - **向量身份 = 嵌入 Provider 的模型标识**（Identity()；框架默认实现 = kb.embed.model，
//     产品 Provider 可解析其运行时模型定义）：向量行 model 列、已嵌集合判据与检索腿 `v.model`
//     过滤同源（ModelIdentity）；换模型 ⇒ 新行换身份、旧身份行按不匹配自然排除——
//     重嵌入口 = `aigo kb reembed`（ReembedAll）或 kb:rebuild(reembed) 档；
//   - 幂等 = uk(chunk_id, model, text_hash)，落行 ON CONFLICT DO NOTHING（先例：产物键去重）；
//     扫描前先查已嵌集合（不重复计费），失败按批计数不中断（批级失败进 Errors，由调用方决定是否响亮失败）；
//     身份解析失败/批间身份漂移一律响亮失败（禁以未知/漂移身份落行）。
//
// 本包不注册 jobs（kind 接线在 service/kb/job.go），只提供可重入的阶段函数：
// 重试/租约回收会重跑本函数，幂等由「已嵌集合 + ON CONFLICT」承担。
package embed

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/ai-go-hub/ai-go-admin/internal/infra/database"
	"github.com/ai-go-hub/ai-go-admin/plugins/kb/model"
	"github.com/ai-go-hub/ai-go-admin/plugins/kb/service/kb/aiport"
	"github.com/ai-go-hub/ai-go-admin/plugins/kb/service/kb/chunk"
	"github.com/ai-go-hub/ai-go-admin/plugins/kb/service/kb/ingest"

	"gorm.io/gorm"
)

// 缺省参数（config/kb.yaml kb.embed.* 为运行期接线的覆盖源）
const (
	// DefaultAgent 嵌入调用档名（产品适配器映射到其 ai agent，如 kb-embed；框架默认实现忽略）
	DefaultAgent = "kb-embed"
	// DefaultBatch 单次嵌入调用文本条数（先例：批 16）
	DefaultBatch = 16
	// MaxBatch 单批硬上限（先例：64）
	MaxBatch = 64
	// DefaultMaxChars 单条嵌入文本字符帽（超帽截断并记读数）
	DefaultMaxChars = 2000
	// Source 调用来源（产品侧落调用日志的 agent_source；优先级=批处理档）
	Source = "kb"
	// scanWindow 单窗扫描切片行数（keyset 分页；作业面批量读取）
	scanWindow = 256
	// modelIdentityTimeout 模型身份解析内部超时（ModelIdentity 无 ctx 面；脱钩短上限）
	modelIdentityTimeout = 5 * time.Second
)

// Options 嵌入阶段参数（零值 = 缺省）
type Options struct {
	Agent    string // 嵌入 agent（缺省 kb-embed）
	Batch    int    // 批大小（缺省 16，上限 64）
	MaxChars int    // 单条字符帽（缺省 2000；<0 = 不截断）
	Budget   ingest.BudgetLimits
	// Progress 进度回调（done/total；作业侧可映射 j.Progress 借力续租；nil = 不上报）
	Progress func(done, total int64)
}

// Stats 嵌入阶段读数（回执/结果 JSON）
type Stats struct {
	Scanned    int      `json:"scanned"`      // 扫描到的切片行
	Embedded   int      `json:"embedded"`     // 新落向量行数
	Skipped    int      `json:"skipped"`      // 已有向量行跳过数（含截断后哈希命中）
	Truncated  int      `json:"truncated"`    // 截断条数（max_chars 帽）
	Failed     int      `json:"failed"`       // 失败条数（批失败按批计）
	Chars      int64    `json:"chars"`        // 嵌入文本字符数（rune；成本估算输入）
	CJK        int      `json:"cjk"`          // CJK 字符数（系数表）
	Other      int      `json:"other"`        // 非 CJK 字符数
	EstTokens  float64  `json:"est_tokens"`   // token 估算（估算值）
	EstCostCNY float64  `json:"est_cost_cny"` // 费用估算（估算值；对账口径 = 模型端口侧调用日志）
	Dim        int      `json:"dim"`          // 向量维度（首次成功批读数）
	Model      string   `json:"model"`        // 上游模型名（agent 定义回显）
	Channel    string   `json:"channel"`      // 渠道读数（账单核对）
	Errors     []string `json:"errors,omitempty"`
}

// Ok 是否无失败（调用方据此响亮失败：Failed > 0 ⇒ 返回错误让 jobs 退避重试）
func (s Stats) Ok() bool { return s.Failed == 0 }

// ==================== 向量身份（model 列 = 嵌入 Provider 的模型标识，非调用档名） ====================

// ModelIdentity 当前嵌入模型身份（**Provider 解析的模型标识**）——向量行 model 列、检索腿 `v.model` 过滤与
// CLI（aigo kb reembed）回显共用单点：框架默认实现 = kb.embed.model；产品 Provider（适配器）可解析
// 其运行时模型定义（如 agent 的 model），换模型后旧身份行按不匹配自然排除。
//
// 签名取 (no ctx, string)：调用面 = 检索腿每查一次 + CLI 回显（进程无持久身份状态，免缓存陈旧）；
// 解析失败返回 "" 并落日志——调用方必须把 "" 当「身份不可用」响亮处理（禁以空身份落行/发查询）。
func ModelIdentity() string {
	ctx, cancel := context.WithTimeout(context.Background(), modelIdentityTimeout)
	defer cancel()
	identity, err := currentIdentity(ctx)
	if err != nil {
		log.Printf("[kb] 嵌入模型身份解析失败: %v", err)
		return ""
	}
	return identity
}

// currentIdentity 经端口解析当前嵌入模型身份（写入路径与 ModelIdentity 共用单点）。
// Provider 未注册/身份不可用（模型未配置）⇒ 错误（fail-closed：身份未知一律不写向量行——
// 写入面拿不到 identity 场景由调用方响亮失败，禁回落档名）。
func currentIdentity(ctx context.Context) (string, error) {
	provider, err := aiport.CurrentEmbedder()
	if err != nil {
		return "", err
	}
	identity, err := provider.Identity(ctx)
	if err != nil {
		return "", fmt.Errorf("kb: 嵌入模型身份解析失败: %w", err)
	}
	identity = strings.TrimSpace(identity)
	if identity == "" {
		return "", errors.New("kb: 嵌入模型身份为空（Provider Identity 未就绪，向量身份不可用）")
	}
	return identity, nil
}

// ==================== 阶段入口 ====================

// ==================== 调用元数据（产品适配器据此补治理语义：egress face/scope、agent 绑定） ====================

// kbEmbedScope 库维 scope（`kb:library:<id>`；0 = 不设 scope）。
func kbEmbedScope(libraryID uint) string {
	if libraryID == 0 {
		return ""
	}
	return "kb:library:" + strconv.FormatUint(uint64(libraryID), 10)
}

// kbEmbedDocumentLibraryID 文档 → 库属 PK 点查（EmbedDocumentBuild 的 scope 解析）。
// 查失败 = 缺省 scope（仍在日志登记）——单文档入口的库属拿不到时，调用面照常（scope 只影响产品侧治理面）。
func kbEmbedDocumentLibraryID(ctx context.Context, db *gorm.DB, documentID uint) uint {
	row, err := gorm.G[model.KBDocument](db).Select("library_id").Where("id = ?", documentID).First(ctx)
	if err != nil {
		log.Printf("[kb] kb.embed 文档库属解析失败（doc=%d；本批缺省 scope 照常嵌入）：%v", documentID, err)
		return 0
	}
	return row.LibraryID
}

// EmbedDocumentBuild 嵌入某文档某 build 的全部待嵌块（kb:ingest 阶段调用；可重入）。
func EmbedDocumentBuild(ctx context.Context, db *gorm.DB, documentID uint, buildHash string, opts Options) (Stats, error) {
	if db == nil || documentID == 0 || strings.TrimSpace(buildHash) == "" {
		return Stats{}, errors.New("kb: 嵌入参数非法（document_id/build_hash 必填）")
	}
	meta := aiport.CallMeta{
		Face: "kb.embed", Scope: kbEmbedScope(kbEmbedDocumentLibraryID(ctx, db, documentID)),
		Kind: "document", Agent: opts.Agent, Source: Source,
	}
	return embedScan(ctx, db, opts, meta, "document_id = ? AND build_hash = ?", documentID, buildHash)
}

// EmbedLibraryPending 嵌入某库全部待嵌块（kb:rebuild reembed 模式；可重入，逐窗追平到扫不满一窗）。
// 作用域收窄到「各文档当前生效 build」的切片行（换出前的旧/中间态 build 不重嵌——它们是待删投影）。
func EmbedLibraryPending(ctx context.Context, db *gorm.DB, libraryID uint, opts Options) (Stats, error) {
	if db == nil || libraryID == 0 {
		return Stats{}, errors.New("kb: 嵌入参数非法（library_id 必填）")
	}
	meta := aiport.CallMeta{
		Face: "kb.embed", Scope: kbEmbedScope(libraryID),
		Kind: "document", Agent: opts.Agent, Source: Source,
	}
	chunkTable := tableName(db, model.KBChunk{})
	docTable := tableName(db, model.KBDocument{})
	cond := "library_id = ? AND EXISTS (SELECT 1 FROM " + docTable + " d WHERE d.id = " + chunkTable +
		".document_id AND d.active_build = " + chunkTable + ".build_hash AND d.deleted_at IS NULL)"
	return embedScan(ctx, db, opts, meta, cond, libraryID)
}

// ReembedAll 全库重嵌（运维入口 `aigo kb reembed` 调用）：按**当前模型身份**把全部未删库的
// 「当前生效 build」切片追平嵌入。幂等：同身份已嵌行（uk 三元组命中）跳过；历史身份行不参与去重
// ⇒ 换模型后首跑即整库重产（旧身份行留档、不再被检索腿召回；清理留待运维按容量处置）。
// 返回本次新落向量行数；逐库串行（库内逐窗），任一步失败即响亮返回（已计入的读数如实带回，不吞半程）。
//
// 参数档**由调用方给出且必须与作业面同源**（config kb.embed.*；作业面映射 =
// service/kb/job_support.go·embedOptions）——已整改：原取代码缺省（帽 2000），
// 与作业面实际配置不一致时，嵌入文本按不同帽截断、text_hash 不同 ⇒ 同一 (chunk, model) 落出
// 第二条向量行（uk 三元组不拦），而向量腿不过滤 text_hash ⇒ 同一切片重复占窗（RRF 只按 chunk_id
// 去重）。换 max_chars 档后旧 text_hash 行需清理或按容量留档，否则新旧行并存。
// Options.Budget 零值 = 代码缺省（仅影响估算读数，CLI 面不消费）。
// 界：库级粒度重嵌走 kb:rebuild(reembed) 档；未删库含 status=0（禁用停写
// 不停管——重开放后向量即就绪，成本一库嵌入量）。
func ReembedAll(ctx context.Context, opts Options) (int, error) {
	db := database.DB()
	if db == nil {
		return 0, errors.New("kb: 全库重嵌失败（DB 未初始化）")
	}
	// 入口早失败：身份不可用不逐库空跑（每库内 embedScan 还会再解析，此处先响亮一次）
	if _, err := currentIdentity(ctx); err != nil {
		return 0, err
	}
	libIDs := make([]uint, 0, 16)
	// 未删库清单（model.KBLibrary 带 DeletedAt ⇒ 软删由 GORM 作用域排除）
	if err := gorm.G[model.KBLibrary](db).Select("id").Order("id ASC").Scan(ctx, &libIDs); err != nil {
		return 0, fmt.Errorf("kb: 读库清单失败: %w", err)
	}
	total := 0
	for _, id := range libIDs {
		stats, err := EmbedLibraryPending(ctx, db, id, opts)
		if err != nil {
			return total, fmt.Errorf("kb: 库 %d 重嵌失败: %w", id, err)
		}
		total += stats.Embedded
		if !stats.Ok() {
			return total, fmt.Errorf("kb: 库 %d 重嵌失败 %d 条: %v", id, stats.Failed, stats.Errors)
		}
		log.Printf("[kb] 库 %d 重嵌：新落 %d 行、跳过 %d 行（累计 %d）", id, stats.Embedded, stats.Skipped, total)
	}
	return total, nil
}

// ==================== 向量身份巡检（运维登记：非当前身份行点名；只读、可复跑） ====================

// 向量身份点名结论闭集（aigo kb check 向量段输出档；命名照巡检既有 kind 风格）
const (
	IdentityKindOK             = "ok"              // 无非当前身份向量行
	IdentityKindArchived       = "archived"        // 非当前身份行留档（当前身份行在场；不构成待处置）
	IdentityKindPendingReembed = "pending_reembed" // 当前身份零行且存在非当前身份行（疑身份切换后未重产；待处置）
)

// IdentityFinding 单库向量身份点名结论
type IdentityFinding struct {
	LibraryID     uint     `json:"library_id"`
	Rows          int64    `json:"rows"`           // 该库向量行总数
	RowsCurrent   int64    `json:"rows_current"`   // 当前身份行数
	RowsForeign   int64    `json:"rows_foreign"`   // 非当前身份行数
	ForeignModels []string `json:"foreign_models"` // 非当前身份的模型名（按行数降序去重展示）
	Kind          string   `json:"kind"`           // ok|archived|pending_reembed
	Hint          string   `json:"hint"`
}

// IdentityReport 向量身份巡检报告（aigo kb check 向量段的数据面）
type IdentityReport struct {
	Identity    string            `json:"identity"`                 // 当前身份（解析失败 = 空串，原因见 identity_error）
	IdentityErr string            `json:"identity_error,omitempty"` // 身份不可用原因（fail-closed 口径同 ModelIdentity）
	Libraries   []IdentityFinding `json:"libraries"`
	// VectorRowsExist 未删库是否存在向量行（身份不可用时的待处置语境判据：有行 ⇒ 即使嵌入档已关也计待处置）
	VectorRowsExist bool `json:"vector_rows_exist"`
}

// Issues 待处置条数（pending_reembed 库数；身份不可用按部署语境计）：
// 身份不可用 ∧ 嵌入档关（kb.embed.enabled=false）∧ 库内无向量行 ⇒ 合法部署（未接线/停用），只提示不计；
// 否则（嵌入开而身份不可用，或库内已有向量行）⇒ 计 1 条——检索面身份过滤与重嵌入口均依赖该身份。
func (r *IdentityReport) Issues(embedEnabled bool) int {
	if r.IdentityErr != "" {
		if !embedEnabled && !r.VectorRowsExist {
			return 0
		}
		return 1
	}
	n := 0
	for i := range r.Libraries {
		if r.Libraries[i].Kind == IdentityKindPendingReembed {
			n++
		}
	}
	return n
}

// CheckVectorIdentities 向量身份点名（aigo kb check 向量段的只读数据面；可复跑、无副作用）：
// 按 (库, model) 聚合 kb_chunk_vectors 行，点名「存在非当前身份向量行」与「当前身份零行」的库——
// 身份切换（后台改 kb-embed 的 model）后未重产的形态（升级部署须一次性 aigo kb reembed）。
//
// 口径（如实）：
//   - 作用域 = 未删库（与 ReembedAll 同域；软删库的行待 kb:purge 清理，不属重嵌面）；
//   - 只按向量行聚合，不逐切片判 text_hash 覆盖（「某切片是否仍缺行」是重嵌作业自身的判据，
//     本巡检给的是可复跑的身份面点名）；分类 = 两态——当前身份行在场（archived）与否（pending_reembed）；
//   - 身份不可用（agent 未定义/未启用/model 空）= 不分类、带回原因，并如实带回「库内是否已有向量行」
//     （聚合先行、不依赖身份）——是否计待处置按 Issues 的部署语境判据（嵌入关 ∧ 无行 = 合法部署只提示）。
func CheckVectorIdentities(ctx context.Context, db *gorm.DB) (*IdentityReport, error) {
	if db == nil {
		return nil, errors.New("kb: 向量身份巡检失败（DB 未初始化）")
	}
	report := &IdentityReport{Libraries: []IdentityFinding{}}

	// 聚合先行（不依赖身份解析）——身份不可用时仍须自带「库内是否已有向量行」判据
	type identityRow struct {
		LibraryID uint   `gorm:"column:library_id"`
		Model     string `gorm:"column:model"`
		N         int64  `gorm:"column:n"`
	}
	sqlText := "SELECT v.library_id AS library_id, v.model AS model, COUNT(*) AS n FROM " +
		tableName(db, ingest.ChunkVectorRow{}) + " v JOIN " + tableName(db, model.KBLibrary{}) +
		" l ON l.id = v.library_id AND l.deleted_at IS NULL" +
		" GROUP BY v.library_id, v.model ORDER BY v.library_id ASC, n DESC"
	rows := make([]identityRow, 0, 16)
	if err := db.WithContext(ctx).Raw(sqlText).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("kb: 向量身份聚合失败: %w", err)
	}
	report.VectorRowsExist = len(rows) > 0

	identity, err := currentIdentity(ctx)
	if err != nil {
		report.IdentityErr = err.Error()
		return report, nil
	}
	report.Identity = identity

	type acc struct {
		rows, current, foreign int64
		models                 []string
	}
	byLib := map[uint]*acc{}
	order := make([]uint, 0, len(rows))
	for i := range rows {
		a := byLib[rows[i].LibraryID]
		if a == nil {
			a = &acc{}
			byLib[rows[i].LibraryID] = a
			order = append(order, rows[i].LibraryID)
		}
		a.rows += rows[i].N
		if rows[i].Model == identity {
			a.current += rows[i].N
			continue
		}
		a.foreign += rows[i].N
		a.models = append(a.models, rows[i].Model)
	}
	for _, id := range order {
		a := byLib[id]
		f := IdentityFinding{
			LibraryID: id, Rows: a.rows, RowsCurrent: a.current, RowsForeign: a.foreign,
			ForeignModels: a.models,
		}
		if f.ForeignModels == nil {
			f.ForeignModels = []string{}
		}
		switch {
		case a.foreign == 0:
			f.Kind = IdentityKindOK
			f.Hint = "正常；无非当前身份向量行"
		case a.current > 0:
			f.Kind = IdentityKindArchived
			f.Hint = fmt.Sprintf("非当前身份向量行留档 %d 行（model=%s）：不计费、不被召回、不参与去重；"+
				"自动清理未建（容量由运维处置），不构成待处置", a.foreign, strings.Join(a.models, ","))
		default:
			f.Kind = IdentityKindPendingReembed
			f.Hint = fmt.Sprintf("当前身份（%s）零行、存在非当前身份向量行（model=%s）——疑身份切换后未重产："+
				"aigo kb reembed 重产（幂等、可重入）", identity, strings.Join(a.models, ","))
		}
		report.Libraries = append(report.Libraries, f)
	}
	return report, nil
}

// embedScan 待嵌扫描主环（keyset 分页；入口解析一次模型身份，其后查已嵌集合/落行两处共用同一身份；
// 失败按批计数不中断）。
func embedScan(ctx context.Context, db *gorm.DB, opts Options, meta aiport.CallMeta, where string, args ...any) (Stats, error) {
	opts = normalizeOptions(opts)
	meta = meta.Normalize()
	meta.Agent = opts.Agent
	provider, err := aiport.CurrentEmbedder()
	if err != nil {
		return Stats{}, err // fail-closed：Provider 未注册不得落行
	}
	identity, err := provider.Identity(ctx)
	if err != nil {
		return Stats{}, fmt.Errorf("kb: 嵌入模型身份解析失败: %w", err)
	}
	identity = strings.TrimSpace(identity)
	if identity == "" {
		return Stats{}, errors.New("kb: 嵌入模型身份为空（Provider Identity 未就绪）")
	}
	var stat Stats
	cursor := uint(0)
	for {
		rows, err := scanChunks(ctx, db, cursor, where, args...)
		if err != nil {
			return stat, err
		}
		if len(rows) == 0 {
			return stat, nil
		}
		cursor = rows[len(rows)-1].ID
		if err := embedWindow(ctx, db, rows, opts, provider, meta, identity, &stat); err != nil {
			return stat, err
		}
		stat.Scanned += len(rows)
		if opts.Progress != nil {
			opts.Progress(int64(stat.Scanned), int64(stat.Scanned))
		}
		if len(rows) < scanWindow {
			return stat, nil
		}
	}
}

// chunkRow 扫描行（嵌入派生所需列：正文 + 标题路径 + 表格元信息 + 配置快照）
type chunkRow struct {
	ID             uint    `gorm:"column:id"`
	LibraryID      uint    `gorm:"column:library_id"`
	Text           string  `gorm:"column:text"`
	HeadingPath    *string `gorm:"column:heading_path"`
	TableMeta      *string `gorm:"column:table_meta"`
	ConfigSnapshot *string `gorm:"column:config_snapshot"`
}

// scanChunks 单窗扫描（keyset：WHERE ... AND id > cursor ORDER BY id LIMIT scanWindow）
func scanChunks(ctx context.Context, db *gorm.DB, cursor uint, where string, args ...any) ([]chunkRow, error) {
	rows, err := gorm.G[model.KBChunk](db).
		Select("id, library_id, text, heading_path, table_meta, config_snapshot").
		Where(where+" AND id > ?", append(args, cursor)...).
		Order("id ASC").Limit(scanWindow).Find(ctx)
	if err != nil {
		return nil, fmt.Errorf("kb: 扫描待嵌切片失败: %w", err)
	}
	out := make([]chunkRow, 0, len(rows))
	for i := range rows {
		r := rows[i]
		out = append(out, chunkRow{
			ID: r.ID, LibraryID: r.LibraryID, Text: r.Text,
			HeadingPath: r.HeadingPath, TableMeta: r.TableMeta, ConfigSnapshot: r.ConfigSnapshot,
		})
	}
	return out, nil
}

// pending 待嵌单元（嵌入文本 + 其哈希）
type pending struct {
	ChunkID   uint
	LibraryID uint
	Text      string // 嵌入文本（ChunkTextOf 口径；已截断）
	Hash      string // 嵌入文本 sha1（向量行 uk 第三列）
}

// embedWindow 单窗嵌入：派生文本 → 查已嵌集合（同身份）→ 过滤 → 分批调用 → 落行。
func embedWindow(ctx context.Context, db *gorm.DB, rows []chunkRow, opts Options, provider aiport.Embedder, meta aiport.CallMeta, identity string, stat *Stats) error {
	items := make([]pending, 0, len(rows))
	for i := range rows {
		text, err := embedTextOf(rows[i])
		if err != nil {
			stat.Failed++
			addErr(stat, err.Error())
			continue
		}
		if n := countRunes(text); opts.MaxChars > 0 && n > opts.MaxChars {
			text = truncateRunes(text, opts.MaxChars)
			stat.Truncated++
		}
		if strings.TrimSpace(text) == "" {
			stat.Failed++
			addErr(stat, fmt.Sprintf("切片 %d 嵌入文本为空（ChunkTextOf 派生为空）", rows[i].ID))
			continue
		}
		items = append(items, pending{ChunkID: rows[i].ID, LibraryID: rows[i].LibraryID, Text: text, Hash: chunk.TextHash(text)})
	}

	existing, err := existingVectors(ctx, db, items, identity)
	if err != nil {
		return err
	}
	filtered := items[:0]
	for _, it := range items {
		if existing[vectorKey(it.ChunkID, it.Hash)] {
			stat.Skipped++
			continue
		}
		filtered = append(filtered, it)
	}
	items = filtered

	// 成本/字数读数只统计**本次发往上游**的条数（已嵌集合命中者不重复计费——对账口径与
	// 上游调用日志一致；否则纯重跑（新落 0 行）也会报出非零费用估算）。
	for i := range items {
		stat.Chars += int64(countRunes(items[i].Text))
		cjk, other := ingest.CountWordsLike(items[i].Text)
		stat.CJK += cjk
		stat.Other += other
	}

	for start := 0; start < len(items); start += opts.Batch {
		end := start + opts.Batch
		if end > len(items) {
			end = len(items)
		}
		batch := items[start:end]
		texts := make([]string, 0, len(batch))
		for _, it := range batch {
			texts = append(texts, it.Text)
		}
		res, err := provider.Embed(ctx, meta, texts)
		if err != nil {
			stat.Failed += len(batch)
			addErr(stat, fmt.Sprintf("批 %d 嵌入失败: %v", start/opts.Batch, err))
			continue
		}
		if len(res.Vectors) != len(batch) {
			stat.Failed += len(batch)
			addErr(stat, fmt.Sprintf("批 %d 向量数不符（%d != %d）", start/opts.Batch, len(res.Vectors), len(batch)))
			continue
		}
		// 批间身份漂移（外部改动模型定义与调用并发）：拒绝落行（否则整批行被贴上旧身份，
		// 检索腿按新身份过滤会整体漏召回且无痕迹）——计入失败，由调用方响亮收口。
		if got := strings.TrimSpace(res.ModelIdentity); got != identity {
			stat.Failed += len(batch)
			addErr(stat, fmt.Sprintf("批 %d 模型身份漂移（作业身份=%s 调用回显=%s）：拒绝落行", start/opts.Batch, identity, got))
			continue
		}
		dim := 0
		if len(res.Vectors) > 0 {
			dim = len(res.Vectors[0])
		}
		if dim == 0 {
			stat.Failed += len(batch)
			addErr(stat, fmt.Sprintf("批 %d 空向量", start/opts.Batch))
			continue
		}
		if stat.Dim == 0 {
			stat.Dim = dim
		}
		if stat.Model == "" {
			stat.Model = strings.TrimSpace(res.Model)
		}
		stat.Channel = res.Channel
		if err := insertVectors(ctx, db, batch, res.Vectors, dim, identity); err != nil {
			return err
		}
		stat.Embedded += len(batch)
	}
	l := opts.Budget.WithDefaults()
	stat.EstTokens = ingest.EstimateTokens(stat.CJK, stat.Other, l)
	stat.EstCostCNY = ingest.EstimateCostCNY(stat.EstTokens, l)
	return nil
}

// embedTextOf 嵌入文本派生（委托 **chunk.RowTextOf 行级复算单点**；口径见该函数注释）：
// 从 kb_chunk 行重建 chunk.Chunk + 有效配置（config_snapshot 复算；缺失回退代码缺省档、非法响亮报错）。
// 原行级复算逻辑上移 chunk 包（与 aigo kb reindex 回填同源），本函数收薄为委托。
func embedTextOf(row chunkRow) (string, error) {
	text, err := chunk.RowTextOf(row.Text, row.HeadingPath, row.TableMeta, row.ConfigSnapshot)
	if err != nil {
		return "", fmt.Errorf("kb: 切片 %d 派生文本复算失败: %w", row.ID, err)
	}
	return text, nil
}

// existingVectors 已嵌集合（(chunk_id, model=身份, text_hash) 命中集；一次查询，不重复计费。
// 身份 = 已解析模型标识 ⇒ 换模型后同文本不算已嵌（重嵌=换行，旧身份行不拦住新行）。
func existingVectors(ctx context.Context, db *gorm.DB, items []pending, identity string) (map[string]bool, error) {
	out := map[string]bool{}
	if len(items) == 0 {
		return out, nil
	}
	ids := make([]uint, 0, len(items))
	for _, it := range items {
		ids = append(ids, it.ChunkID)
	}
	rows, err := gorm.G[ingest.ChunkVectorRow](db).
		Select("chunk_id, text_hash").
		Where("model = ? AND chunk_id IN ?", identity, ids).
		Find(ctx)
	if err != nil {
		return nil, fmt.Errorf("kb: 查询已嵌集合失败: %w", err)
	}
	for i := range rows {
		out[vectorKey(rows[i].ChunkID, rows[i].TextHash)] = true
	}
	return out, nil
}

// vectorKey 已嵌集合键（chunk_id + 嵌入文本哈希）
func vectorKey(chunkID uint, hash string) string {
	return strconv.FormatUint(uint64(chunkID), 10) + ":" + hash
}

// insertVectors 批量落向量行（多值 INSERT；ON CONFLICT DO NOTHING 幂等；embedding 走 pgvector 文本形态绑定；
// model 列 = 模型身份（非 agent 名；列注释「嵌入模型调用名（换模型=新行）」照此对齐））
func insertVectors(ctx context.Context, db *gorm.DB, batch []pending, vectors [][]float32, dim int, identity string) error {
	var b strings.Builder
	b.WriteString("INSERT INTO " + tableName(db, ingest.ChunkVectorRow{}) +
		" (chunk_id, library_id, model, text_hash, embedding, embedding_space, created_at) VALUES ")
	args := make([]any, 0, len(batch)*6)
	for i := range batch {
		if len(vectors[i]) != dim {
			return fmt.Errorf("kb: 向量维度不一致（chunk %d: %d != %d）", batch[i].ChunkID, len(vectors[i]), dim)
		}
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString("(?,?,?,?,?::halfvec,'',NOW())")
		args = append(args, batch[i].ChunkID, batch[i].LibraryID, identity, batch[i].Hash, formatEmbedding(vectors[i]))
	}
	b.WriteString(" ON CONFLICT DO NOTHING")
	if err := db.WithContext(ctx).Exec(b.String(), args...).Error; err != nil {
		return fmt.Errorf("kb: 落向量行失败: %w", err)
	}
	return nil
}

// formatEmbedding float32 向量 → pgvector 文本形态 [a,b,c]（口径与向量文本形态同构：
// 'f' 格式、-1 精度、float32 位宽——两模块各自持有以避跨模块耦合，格式逐字一致）
func formatEmbedding(v []float32) string {
	var b strings.Builder
	b.Grow(len(v) * 8)
	b.WriteByte('[')
	for i, x := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(x), 'f', -1, 32))
	}
	b.WriteByte(']')
	return b.String()
}

// normalizeOptions 参数规整（批大小收敛 [1, MaxBatch]；agent 缺省）
func normalizeOptions(opts Options) Options {
	if strings.TrimSpace(opts.Agent) == "" {
		opts.Agent = DefaultAgent
	}
	if opts.Batch <= 0 {
		opts.Batch = DefaultBatch
	}
	if opts.Batch > MaxBatch {
		opts.Batch = MaxBatch
	}
	if opts.MaxChars == 0 {
		opts.MaxChars = DefaultMaxChars
	}
	if opts.MaxChars < 0 {
		opts.MaxChars = 0
	}
	return opts
}

// addErr 错误摘要收集（≤10 条，防结果膨胀）
func addErr(stat *Stats, msg string) {
	if len(stat.Errors) < 10 {
		stat.Errors = append(stat.Errors, msg)
	}
}

// countRunes rune 计数（本地小工具；与 ingest 包同口径但避免为计数反向依赖）
func countRunes(s string) int {
	n := 0
	for range s {
		n++
	}
	return n
}

// truncateRunes 按 rune 截断（截断读数由调用方计入 Truncated）
func truncateRunes(s string, n int) string {
	if n <= 0 {
		return s
	}
	rs := []rune(s)
	if len(rs) <= n {
		return s
	}
	return string(rs[:n])
}

// tableName 表名单点（与模型 TableName() 同源；raw SQL 禁裸写表名）
func tableName(db *gorm.DB, m any) string {
	stmt := &gorm.Statement{DB: db}
	if err := stmt.Parse(m); err != nil || stmt.Schema == nil || stmt.Schema.Table == "" {
		if t, ok := m.(interface{ TableName() string }); ok {
			return t.TableName()
		}
		return "kb_chunk_vectors"
	}
	return stmt.Schema.Table
}
