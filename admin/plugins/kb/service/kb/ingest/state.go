// plugin: kb v1.0.0
// 知识库导入管道 · 状态机与库级水位（C6）。
//
// 载体三层（禁靠「跑没跑过」判断）：
//  1. 文档状态列 kb_document.build_state（registered → extracting → extracted → chunking →
//     chunked → embedding → ready；旁路 failed(stage,reason) / canceled）——每阶段开工前落状态，
//     崩溃重跑按状态与产物续跑（提取物/切片/向量各自有幂等键，层层可复算）；
//  2. 库级水位 kb_produce_state.last_document_id（「≤ 该 id 的全部候选文档已终态」的连续前缀；
//     逐窗追平、无失败窗后 GREATEST 单调推进；失败窗不堵尾——先例：状态推进表 + 逐窗追平）；
//  3. jobs 幂等键单飞（kb:ingest:<document_id>:<build_hash> / kb:rebuild:<library_id>:<build_hash>）。
//
// 行类型说明（意图偏离、需知悉）：kb_blob / kb_blob_ref / kb_extract / kb_produce_state 四表的
// Go 行类型落在本包（提取物/状态行类型不落宿主 model 包；migrations/1_core.up.sql 的列面即本包
// Row 类型的唯一对齐源）。若后续把行类型上收 internal/model，本包仅需换类型别名。
//
// 本文件把「状态机 + 水位」收敛为单点：调用方（kb:ingest / kb:rebuild 处理器）不得另写 UPDATE
// 状态语句（防两处口径：一处漏清 failed_* / 一处漏 cancel 检查）。
package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ai-go-hub/ai-go-admin/plugins/kb/model"

	"gorm.io/gorm"
)

// 作业 kind 闭集（jobs.Register 名；幂等键口径队列行）
const (
	// JobKindIngest 单文档全链（提取 → 切分 → 嵌入）；键 kb:ingest:<document_id>:<build_hash>
	JobKindIngest = "kb:ingest"
	// JobKindRebuild 整库重切/重嵌（窗口续跑，水位驱动）；键 kb:rebuild:<library_id>:<build_hash>
	JobKindRebuild = "kb:rebuild"
	// JobKindPurge 清理（幂等可重入）；键 kb:<library_id> / kb:doc:<document_id>（入队钩子口径）
	JobKindPurge = "kb:purge"
)

// rebuild 模式闭集（kb:rebuild.params.mode）
const (
	// RebuildModeRechunk 重切（含重嵌）：全库逐窗追平
	RebuildModeRechunk = "rechunk"
	// RebuildModeReembed 只重嵌（切分产物不动；嵌入模型/文本口径变更时用）
	RebuildModeReembed = "reembed"
)

// 队列缺省窗口（作业面批量读取上限：NormalizeHardLimit 为绝对硬线，此处是业务缺省）
const (
	// DefaultIngestWindow 单文档链路的向量批窗口
	DefaultIngestWindow = 256
	// DefaultRebuildWindow kb:rebuild 单窗文档数（逐窗追平；窗口满继续下一窗）
	DefaultRebuildWindow = 20
	// rebuild 追平窗口上限（防单作业无界循环：一窗追平 50 窗后收工，剩余留给下次投递）
	MaxRebuildWindows = 50
)

// IngestParams kb:ingest 入参（document_id 必填；build_hash 为空 = 处理器自行复算）
type IngestParams struct {
	DocumentID uint   `json:"document_id"`
	BuildHash  string `json:"build_hash,omitempty"`
	Reason     string `json:"reason,omitempty"` // 投递原因（admin_retry/import/...；仅日志与审计明细）
}

// RebuildParams kb:rebuild 入参（library_id 必填；mode 空 = rechunk）
type RebuildParams struct {
	LibraryID uint   `json:"library_id"`
	Mode      string `json:"mode,omitempty"`
	Window    int    `json:"window,omitempty"`
	Reset     bool   `json:"reset,omitempty"` // true = 全量档：自水位 0 起扫（水位追平后「改档 → 整库重切」的可达路径；水位行只前进不被改写）
	Reason    string `json:"reason,omitempty"`
}

// ==================== 行类型（migrations/1_core.up.sql 列面对齐；前缀由 GORM NamingStrategy 注入） ====================

// BlobRow kb_blob 原始件（私有内容寻址；sha256 主键 = 去重身份）
type BlobRow struct {
	SHA256    string     `gorm:"column:sha256;primarykey"`
	MIME      string     `gorm:"column:mime"`
	Size      int64      `gorm:"column:size"`
	Bytes     []byte     `gorm:"column:bytes"`
	CreatedAt *time.Time `gorm:"column:created_at"`
}

// TableName 表名单点（前缀由 NamingStrategy 注入）
func (BlobRow) TableName() string { return "kb_blob" }

// BlobRefRow kb_blob_ref 文档↔原始件引用登记（共享件引用计数面）
type BlobRefRow struct {
	ID         uint       `gorm:"column:id;primarykey"`
	LibraryID  uint       `gorm:"column:library_id"`
	DocumentID uint       `gorm:"column:document_id"`
	SHA256     string     `gorm:"column:sha256"`
	CreatedAt  *time.Time `gorm:"column:created_at"`
}

// TableName 表名单点
func (BlobRefRow) TableName() string { return "kb_blob_ref" }

// ExtractRow kb_extract 提取物（重切不重解的真源档）
type ExtractRow struct {
	ID         uint       `gorm:"column:id;primarykey"`
	DocumentID uint       `gorm:"column:document_id"`
	Kind       string     `gorm:"column:kind"`
	Extractor  string     `gorm:"column:extractor"`
	Version    string     `gorm:"column:version"`
	Text       string     `gorm:"column:text"`
	Stats      *string    `gorm:"column:stats"`
	CreatedAt  *time.Time `gorm:"column:created_at"`
}

// TableName 表名单点
func (ExtractRow) TableName() string { return "kb_extract" }

// ChunkVectorRow kb_chunk_vectors 向量行（halfvec(1024) 单空间；embed 包写入/本包清理共用）
type ChunkVectorRow struct {
	ID             uint       `gorm:"column:id;primarykey"`
	ChunkID        uint       `gorm:"column:chunk_id"`
	LibraryID      uint       `gorm:"column:library_id"`
	Model          string     `gorm:"column:model"`
	TextHash       string     `gorm:"column:text_hash"`
	Embedding      string     `gorm:"column:embedding"` // halfvec 文本形式（Go 侧不经手向量运算）
	EmbeddingSpace string     `gorm:"column:embedding_space"`
	CreatedAt      *time.Time `gorm:"column:created_at"`
}

// TableName 表名单点
func (ChunkVectorRow) TableName() string { return "kb_chunk_vectors" }

// ProduceState kb_produce_state 库级水位（PK=library_id；单调）
type ProduceState struct {
	LibraryID      uint       `gorm:"column:library_id;primarykey"`
	LastDocumentID uint       `gorm:"column:last_document_id"`
	UpdatedAt      *time.Time `gorm:"column:updated_at"`
}

// TableName 表名单点
func (ProduceState) TableName() string { return "kb_produce_state" }

// ==================== 原始件读取（内容寻址 + 完整性校验） ====================

// LoadBlobForDocument 取文档的原始件（经 kb_blob_ref 登记面；未登记/悬空 = 响亮失败）。
func LoadBlobForDocument(ctx context.Context, db *gorm.DB, documentID uint) (*BlobRow, error) {
	if db == nil || documentID == 0 {
		return nil, errors.New("kb: 取原始件参数非法")
	}
	ref, err := gorm.G[BlobRefRow](db).Where("document_id = ?", documentID).First(ctx)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("kb: 文档 %d 未登记原始件（kb_blob_ref 无行）", documentID)
	}
	if err != nil {
		return nil, fmt.Errorf("kb: 读取原始件登记失败: %w", err)
	}
	blob, err := gorm.G[BlobRow](db).Where("sha256 = ?", ref.SHA256).First(ctx)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("kb: 原始件 %s 不在场（登记悬空）", ref.SHA256)
	}
	if err != nil {
		return nil, fmt.Errorf("kb: 读取原始件失败: %w", err)
	}
	return &blob, nil
}

// ==================== 提取物（重切不重解） ====================

// LoadExtract 取既有提取物（不存在返回 nil, nil——调用方据此决定是否重新提取）。
func LoadExtract(ctx context.Context, db *gorm.DB, documentID uint, extractor, version string) (*ExtractRow, error) {
	row, err := gorm.G[ExtractRow](db).
		Where("document_id = ? AND extractor = ? AND version = ?", documentID, extractor, version).
		First(ctx)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("kb: 读取提取物失败: %w", err)
	}
	return &row, nil
}

// SaveExtract 落提取物（幂等：uk(document_id,extractor,version) 冲突即回读既有行——
// 并发/重跑不覆盖已冻结的 verbatim 文本，锚坐标系不被改写）。
func SaveExtract(ctx context.Context, db *gorm.DB, ext *Extract, documentID uint) (*ExtractRow, error) {
	if ext == nil || documentID == 0 {
		return nil, errors.New("kb: 落提取物参数非法")
	}
	stats, err := marshalStats(ext.Stats)
	if err != nil {
		return nil, err
	}
	row := ExtractRow{
		DocumentID: documentID,
		Kind:       ext.Kind,
		Extractor:  ext.Extractor,
		Version:    ext.Version,
		Text:       ext.Text,
		Stats:      stats,
	}
	err = gorm.G[ExtractRow](db).Create(ctx, &row)
	if err != nil {
		if !isUniqueViolation(err) {
			return nil, fmt.Errorf("kb: 落提取物失败: %w", err)
		}
		// 冲突：既有行即真源（禁改写）；回读返回
		existing, rerr := LoadExtract(ctx, db, documentID, ext.Extractor, ext.Version)
		if rerr != nil {
			return nil, rerr
		}
		if existing == nil {
			return nil, fmt.Errorf("kb: 提取物冲突后回读为空（document=%d）", documentID)
		}
		return existing, nil
	}
	return &row, nil
}

func marshalStats(stats map[string]any) (*string, error) {
	if len(stats) == 0 {
		return nil, nil
	}
	raw, err := marshalJSON(stats)
	if err != nil {
		return nil, fmt.Errorf("kb: 提取读数序列化失败: %w", err)
	}
	return &raw, nil
}

// ==================== 文档状态机（单点：一切状态写入经此） ====================

// TransitionState 状态 CAS 迁移（WHERE build_state IN from；from 空 = 不限——重跑宽容）。
// 返回 false = 行已被并发改走（调用方按最新状态决策，不视为错误）。
func TransitionState(ctx context.Context, db *gorm.DB, documentID uint, from []string, to string) (bool, error) {
	if db == nil || documentID == 0 || strings.TrimSpace(to) == "" {
		return false, errors.New("kb: 状态迁移参数非法")
	}
	q := gorm.G[map[string]any](db).Table("kb_document").
		Where("id = ? AND deleted_at IS NULL", documentID)
	if len(from) > 0 {
		q = q.Where("build_state IN ?", from)
	}
	affected, err := q.Updates(ctx, map[string]any{
		"build_state": to,
		"updated_at":  gorm.Expr("NOW()"),
	})
	if err != nil {
		return false, fmt.Errorf("kb: 状态迁移失败(%s→%s): %w", strings.Join(from, "|"), to, err)
	}
	return affected > 0, nil
}

// MarkFailed 落失败旁路（stage/reason 响亮留痕；保留 blob 与既有产物——重试从失败阶段重入）。
func MarkFailed(ctx context.Context, db *gorm.DB, documentID uint, stage, reason string) error {
	if db == nil || documentID == 0 {
		return errors.New("kb: 落失败状态参数非法")
	}
	_, err := gorm.G[map[string]any](db).Table("kb_document").
		Where("id = ?", documentID).
		Updates(ctx, map[string]any{
			"build_state":   model.KBDocumentBuildFailed,
			"failed_stage":  truncateRunes(stage, 32),
			"failed_reason": truncateRunes(reason, 512),
			"updated_at":    gorm.Expr("NOW()"),
		})
	if err != nil {
		return fmt.Errorf("kb: 落失败状态错误: %w", err)
	}
	return nil
}

// MarkBudgetSuspended 预算/来源超限挂起待批（C8）：落 failed_stage='budget' + 原因。
// 口径（如实）：挂起**保留在途态**——本链的调用点在切分前后（文档已过
// extracting/extracted，可能已到 chunking），故 build_state 不回写、也不停置「未开工」；
// 「未开工」不可达（切分前已有状态迁移），后台列表以 failed_stage='budget' + 原因码识别挂起
// （中间态 + 原因码即挂起标识；批准 = 复投作业，整链按产物幂等续跑）。
func MarkBudgetSuspended(ctx context.Context, db *gorm.DB, documentID uint, reason string) error {
	if db == nil || documentID == 0 {
		return errors.New("kb: 落预算挂起状态参数非法")
	}
	_, err := gorm.G[map[string]any](db).Table("kb_document").
		Where("id = ? AND deleted_at IS NULL", documentID).
		Updates(ctx, map[string]any{
			"failed_stage":  "budget",
			"failed_reason": truncateRunes(reason, 512),
			"updated_at":    gorm.Expr("NOW()"),
		})
	if err != nil {
		return fmt.Errorf("kb: 落预算挂起状态错误: %w", err)
	}
	return nil
}

// MarkReady 就绪终态（active_build 换出 + failed_* 清空；原子换出点——旧 build 的行由调用方在换出后删）。
func MarkReady(ctx context.Context, db *gorm.DB, documentID uint, buildHash string) error {
	if db == nil || documentID == 0 || buildHash == "" {
		return errors.New("kb: 落就绪状态参数非法")
	}
	_, err := gorm.G[map[string]any](db).Table("kb_document").
		Where("id = ? AND deleted_at IS NULL", documentID).
		Updates(ctx, map[string]any{
			"build_state":   model.KBDocumentBuildReady,
			"active_build":  buildHash,
			"failed_stage":  "",
			"failed_reason": "",
			"updated_at":    gorm.Expr("NOW()"),
		})
	if err != nil {
		return fmt.Errorf("kb: 落就绪状态错误: %w", err)
	}
	return nil
}

// ClearFailed 重入清尾（retry 路径：failed → 起始态；不动 active_build/产物）。
func ClearFailed(ctx context.Context, db *gorm.DB, documentID uint) error {
	_, err := gorm.G[map[string]any](db).Table("kb_document").
		Where("id = ? AND deleted_at IS NULL AND build_state = ?", documentID, model.KBDocumentBuildFailed).
		Updates(ctx, map[string]any{
			"build_state":   model.KBDocumentBuildRegistered,
			"failed_stage":  "",
			"failed_reason": "",
			"updated_at":    gorm.Expr("NOW()"),
		})
	if err != nil {
		return fmt.Errorf("kb: 清失败状态错误: %w", err)
	}
	return nil
}

// Canceled 取消判定（运行中取消 = 业务标志列；jobs Cancel 仅限 pending——）。
func Canceled(doc *model.KBDocument) bool {
	if doc == nil || doc.CancelRequested == nil {
		return false
	}
	return *doc.CancelRequested == 1
}

// MarkCanceled 落取消终态（只对非终态行生效）。
func MarkCanceled(ctx context.Context, db *gorm.DB, documentID uint) error {
	_, err := gorm.G[map[string]any](db).Table("kb_document").
		Where("id = ? AND build_state NOT IN ?", documentID,
			[]string{model.KBDocumentBuildReady, model.KBDocumentBuildCanceled}).
		Updates(ctx, map[string]any{
			"build_state":  model.KBDocumentBuildCanceled,
			"updated_at":   gorm.Expr("NOW()"),
			"failed_stage": "canceled",
		})
	if err != nil {
		return fmt.Errorf("kb: 落取消状态错误: %w", err)
	}
	return nil
}

// ==================== 库级水位 ====================

// LoadWatermark 读库级水位（无行 = 0，未产）。
func LoadWatermark(ctx context.Context, db *gorm.DB, libraryID uint) (uint, error) {
	row, err := gorm.G[ProduceState](db).Where("library_id = ?", libraryID).First(ctx)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("kb: 读水位失败: %w", err)
	}
	return row.LastDocumentID, nil
}

// AdvanceWatermark 推进水位（GREATEST 单调不回退；仅「无失败窗且游标未高位跳跃」时由调用方调用）。
func AdvanceWatermark(ctx context.Context, db *gorm.DB, libraryID, lastDocumentID uint) error {
	if db == nil || libraryID == 0 {
		return errors.New("kb: 推进水位参数非法")
	}
	err := db.WithContext(ctx).Exec(
		"INSERT INTO "+tableName(db, ProduceState{})+" (library_id, last_document_id, updated_at) VALUES (?, ?, NOW())"+
			" ON CONFLICT (library_id) DO UPDATE SET last_document_id = GREATEST("+
			tableName(db, ProduceState{})+".last_document_id, EXCLUDED.last_document_id), updated_at = NOW()",
		libraryID, lastDocumentID).Error
	if err != nil {
		return fmt.Errorf("kb: 推进水位失败: %w", err)
	}
	return nil
}

// ==================== 小工具 ====================

// tableName 表名单点（与模型 TableName() 同源；raw SQL 禁裸写表名——照既有 tableOf）
func tableName(db *gorm.DB, m any) string {
	stmt := &gorm.Statement{DB: db}
	if err := stmt.Parse(m); err != nil || stmt.Schema == nil || stmt.Schema.Table == "" {
		if t, ok := m.(interface{ TableName() string }); ok {
			return t.TableName()
		}
		return "kb_produce_state"
	}
	return stmt.Schema.Table
}

// marshalJSON JSON 序列化（单点；错误由调用方包上下文）
func marshalJSON(v any) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// truncateRunes 截断（按 rune；列宽帽内留余量）
func truncateRunes(s string, n int) string {
	rs := []rune(strings.TrimSpace(s))
	if len(rs) <= n {
		return string(rs)
	}
	return string(rs[:n])
}

// isUniqueViolation 唯一键冲突判定（GORM 未开 TranslateError ⇒ 按 SQLSTATE/文案判；照既有先例）
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "23505") || strings.Contains(msg, "duplicate key")
}
