// plugin: kb v1.0.0
// 知识库文档面服务（对象级校验 +手写核心树最小面）：
// 文档列表 / 详情 / 软删。文档是库的子资源——一切读写先经**库侧读/写谓词**（EXISTS 单点），
// 禁按裸 id 取数；删文档 = 软删即刻 + 同事务审计 + kb:purge 键 kb:doc:<document_id>（接线）。
package kb

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/ai-go-hub/ai-go-admin/internal/service"
	"github.com/ai-go-hub/ai-go-admin/plugins/kb/model"
	"github.com/ai-go-hub/ai-go-admin/plugins/kb/service/kb/ingest"

	"gorm.io/gorm"
)

// documentListColumns 文档列表显式列（禁 SELECT *；source_ref/chunk_config 大字段不进列表）
const documentListColumns = "id, library_id, title, source_kind, mime, size, build_state, active_build, " +
	"failed_stage, failed_reason, cancel_requested, created_by_kind, created_by_id, updated_at, created_at"

// documentDetailColumns 文档详情列（显式；含 source_ref/chunk_config，供详情面装配）
const documentDetailColumns = documentListColumns + ", source_ref, content_hash, chunk_config"

// documentSortFields 文档列表可排序列（数值/时间列闭集；禁 text/jsonb 大字段排序）
var documentSortFields = map[string]bool{
	"id": true, "size": true, "updated_at": true, "created_at": true,
}

// documentBuildStates 文档构建状态闭集（过滤入参白名单；状态机）
var documentBuildStates = map[string]bool{
	model.KBDocumentBuildRegistered: true,
	model.KBDocumentBuildExtracting: true,
	model.KBDocumentBuildExtracted:  true,
	model.KBDocumentBuildChunking:   true,
	model.KBDocumentBuildChunked:    true,
	model.KBDocumentBuildEmbedding:  true,
	model.KBDocumentBuildReady:      true,
	model.KBDocumentBuildFailed:     true,
	model.KBDocumentBuildCanceled:   true,
}

// DocumentListOptions 文档列表选项（浏览面上限 MaxBrowseLimit）
type DocumentListOptions struct {
	Page       int
	Limit      int
	SortField  string
	SortOrder  string
	BuildState string // 闭集；空=不过滤
	Keyword    string // title ILIKE（trgm 表达式 GIN 支撑：idx_kb_document_title_trgm）
}

// ListDocuments 库内文档列表（先逐资源过库读视图，再按 library_id 收窄；Count 与 List 同一构造器）。
func (s *Service) ListDocuments(ctx context.Context, v *ReadView, libraryID uint, opts DocumentListOptions) ([]model.KBDocument, int64, error) {
	if err := s.RequireRead(ctx, v, libraryID); err != nil {
		return nil, 0, err
	}

	docs := s.tDocument
	cond := docs + ".library_id = ?"
	args := []any{libraryID}
	q := gorm.G[model.KBDocument](s.db).Where(cond, args...)
	cq := gorm.G[model.KBDocument](s.db).Where(cond, args...)

	if state := strings.TrimSpace(opts.BuildState); state != "" && documentBuildStates[state] {
		q = q.Where("build_state = ?", state) // 走 idx_kb_document_library_build_state
		cq = cq.Where("build_state = ?", state)
	}
	if kw := strings.TrimSpace(opts.Keyword); kw != "" {
		like := "%" + escapeLike(kw) + "%"
		q = q.Where("title ILIKE ?", like)
		cq = cq.Where("title ILIKE ?", like)
	}

	total, err := cq.Count(ctx, "id")
	if err != nil {
		return nil, 0, err
	}

	field, order := normalizeSortList(opts.SortField, opts.SortOrder, documentSortFields)
	scopes := s.documentBase.BuildScopes(service.Options{
		SortField: field,
		SortOrder: order,
		Page:      opts.Page,
		Limit:     NormalizeBrowseLimit(opts.Limit),
	})
	rows, err := q.Select(documentListColumns).Scopes(scopes...).Find(ctx)
	if err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// documentAccessCond 文档侧**单点谓词**（读/写共用；文档归属经 library_id 收窄到库谓词——
// 外层 EXISTS 命中文档主键，内层 EXISTS 命中 uk_kb_grant_library_subject_perm 前缀）。
// admin 轴：文档未删 ∧ 父库未删（删库即刻对**一切面**不可见——含后台文档面；节点授权不等于绕过软删）。
func (s *Service) documentAccessCond(v *ReadView, perm string) (string, []any) {
	docs, lib := s.tDocument, s.tLibrary
	if v.IsAdmin() {
		return docs + ".deleted_at IS NULL AND EXISTS (SELECT 1 FROM " + lib +
			" WHERE " + lib + ".id = " + docs + ".library_id AND " + lib + ".deleted_at IS NULL)", nil
	}
	libCond, args := s.libraryAccessCond(v, perm)
	return docs + ".deleted_at IS NULL AND EXISTS (SELECT 1 FROM " + lib +
		" WHERE " + lib + ".id = " + docs + ".library_id AND " + libCond + ")", args
}

// GetDocument 文档详情（文档 → 库谓词单查询；不存在/无权同型 ErrDenied）。
func (s *Service) GetDocument(ctx context.Context, v *ReadView, documentID uint) (*model.KBDocument, error) {
	if v == nil || documentID == 0 {
		return nil, ErrDenied
	}
	cond, args := s.documentAccessCond(v, model.KBGrantPermRead)
	args = append([]any{documentID}, args...)
	row, err := gorm.G[model.KBDocument](s.db).
		Where(s.tDocument+".id = ? AND "+cond, args...).
		Select(documentDetailColumns).
		First(ctx)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrDenied
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// DeleteDocument 删文档（**写谓词**门；软删即刻 + 同事务审计 delete + 清理入队。
// 键 kb:doc:<document_id>（I6）；清块/向量/标签关联由作业执行——禁请求路径同步大删）。
func (s *Service) DeleteDocument(ctx context.Context, actor Actor, documentID uint) error {
	view, err := s.BuildReadView(ctx, actor.Kind, actor.ID)
	if err != nil {
		return err
	}
	if documentID == 0 {
		return ErrDenied
	}

	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		cond, args := s.documentAccessCond(view, model.KBGrantPermWrite)
		args = append([]any{documentID}, args...)
		row, err := gorm.G[model.KBDocument](tx).
			Where(s.tDocument+".id = ? AND "+cond, args...).
			Select(documentDetailColumns).
			First(ctx)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrDenied // 不存在/无权同型
		}
		if err != nil {
			return err
		}
		if _, err := gorm.G[model.KBDocument](tx).Where("id = ?", row.ID).Delete(ctx); err != nil {
			return err
		}
		return writeAudit(ctx, tx, AuditEntry{
			LibraryID:  row.LibraryID,
			DocumentID: row.ID,
			Actor:      actor,
			Action:     model.KBAuditActionDelete,
			Detail:     map[string]any{"title": row.Title, "build_state": row.BuildState, "content_hash": row.ContentHash},
		})
	})
	if err != nil {
		return err
	}

	enqueuePurge(ctx, "kb:doc:"+strconv.FormatUint(uint64(documentID), 10))
	return nil
}

// escapeLike 转义 LIKE/ILIKE 通配符（\ % _；照 webui note 先例——多转义反斜杠为无害收紧）
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// ==================== 文档动作面（重建 / 取消 / 失败列表） ====================
//
// 三轴约束不变（一切读写先经库谓词单点；禁按裸 id 取数）：
//   - 重建（文档级 / 库级）门 = **写谓词**（owner ∪ write 授权 ∪ 后台轴；写权不扩管理权）；
//   - 取消 = 置业务标志 cancel_requested=1（：jobs Cancel 仅限 pending，运行中取消由管道在阶段
//     边界检查本标志并落 canceled 终态），标志写入与审计（update）同事务；
//   - 失败列表 = build_state='failed' 的可读域清单（单点 documentAccessCond；库收窄可选；浏览面上限）。
//
// 取消否决与重建的关系（如实登记）：cancel_requested=1 是对该文档后续构建的**持续否决**（管道入口与
// 阶段边界检查）；产品面无「清除否决」独立入口 ⇒ 显式**文档级重建**承担复位职责（否则取消一次后
// 重建恒被立即否决 = 假动作）。库级重建不复位：被否决文档按 canceled 终态跳过、不堵水位（见 RunRebuild）。

// DocumentBuildResult 重建投递回执（文档级 = kb:ingest 单键；库级 = kb:rebuild 协调行）
type DocumentBuildResult struct {
	Scope         string `json:"scope"` // document=单文档键；library=库级协调行
	DocumentID    uint   `json:"document_id,omitempty"`
	LibraryID     uint   `json:"library_id,omitempty"`
	Mode          string `json:"mode,omitempty"` // 库级：rechunk|reembed
	JobID         uint64 `json:"job_id"`
	JobKey        string `json:"job_key"`
	Created       bool   `json:"created"`        // false = 幂等命中（同键活跃作业已在飞，未重复投递）
	CancelCleared bool   `json:"cancel_cleared"` // 文档级：本次是否复位取消否决（1→0）
}

// writableDocumentRow 文档**写门**取行（单点：写谓词 EXISTS + 显式列；不存在/无权同型 ErrDenied）
func (s *Service) writableDocumentRow(ctx context.Context, db *gorm.DB, v *ReadView, documentID uint) (*model.KBDocument, error) {
	if v == nil || documentID == 0 {
		return nil, ErrDenied
	}
	cond, args := s.documentAccessCond(v, model.KBGrantPermWrite)
	args = append([]any{documentID}, args...)
	row, err := gorm.G[model.KBDocument](db).
		Where(s.tDocument+".id = ? AND "+cond, args...).
		Select(documentDetailColumns).
		First(ctx)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrDenied
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// RebuildDocument 文档级重建（写谓词门 → 复位取消否决 → 投递 kb:ingest 单键）。
//
// 投递本身不是内容变更、不另写审计（内容变更由作业侧留痕：换出时 system 审计 import）；
// 复位否决（1→0）是数据变更 ⇒ 与审计同事务写入（真变才写）。
func (s *Service) RebuildDocument(ctx context.Context, actor Actor, documentID uint) (*DocumentBuildResult, error) {
	view, err := s.BuildReadView(ctx, actor.Kind, actor.ID)
	if err != nil {
		return nil, err
	}
	row, err := s.writableDocumentRow(ctx, s.db, view, documentID)
	if err != nil {
		return nil, err
	}

	cleared := false
	if ingest.Canceled(row) {
		err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			res := tx.WithContext(ctx).Exec("UPDATE "+s.tDocument+
				" SET cancel_requested = 0, updated_at = NOW()"+
				" WHERE id = ? AND deleted_at IS NULL AND cancel_requested = 1", row.ID)
			if res.Error != nil {
				return fmt.Errorf("kb: 复位取消标志失败: %w", res.Error)
			}
			if res.RowsAffected == 0 {
				return nil // 并发已复位：不重复留痕
			}
			cleared = true
			return writeAudit(ctx, tx, AuditEntry{
				LibraryID: row.LibraryID, DocumentID: row.ID, Actor: actor, Action: model.KBAuditActionUpdate,
				Detail: map[string]any{"event": "cancel_cleared", "build_state": row.BuildState},
			})
		})
		if err != nil {
			return nil, err
		}
	}

	job, created, err := s.EnqueueIngestDocument(ctx, row.ID)
	if err != nil {
		return nil, err
	}
	return &DocumentBuildResult{
		Scope: "document", DocumentID: row.ID, LibraryID: row.LibraryID,
		JobID: job.ID, JobKey: job.Key, Created: created, CancelCleared: cleared,
	}, nil
}

// RebuildLibrary 库级重建（写谓词门 + 目标库在场校验 → 投递 kb:rebuild 协调行）。
// mode 缺省 rechunk；闭集校验在 EnqueueRebuildLibrary 单点（rechunk|reembed）。协调行由作业侧按水位
// 逐窗展开为逐文档导入（reset 全量档不在本面开放——登记）。
// 用库谓词**取行**（而非 RequireWrite 的 admin 恒真分支）：admin 轴同样要求目标库在场且未删——防把
// 作业投给悬空库（否则形态是「接口报成功、作业必失败」的假成功）。
func (s *Service) RebuildLibrary(ctx context.Context, actor Actor, libraryID uint, mode string) (*DocumentBuildResult, error) {
	view, err := s.BuildReadView(ctx, actor.Kind, actor.ID)
	if err != nil {
		return nil, err
	}
	if libraryID == 0 {
		return nil, ErrDenied
	}
	cond, args := s.libraryAccessCond(view, model.KBGrantPermWrite)
	args = append([]any{libraryID}, args...)
	if _, err := gorm.G[model.KBLibrary](s.db).
		Select("id").
		Where(s.tLibrary+".id = ? AND "+cond, args...).
		First(ctx); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrDenied // 不存在/无权同型
		}
		return nil, err
	}
	job, created, err := s.EnqueueRebuildLibrary(ctx, libraryID, mode, "")
	if err != nil {
		return nil, err
	}
	m := strings.TrimSpace(mode)
	if m == "" {
		m = ingest.RebuildModeRechunk
	}
	return &DocumentBuildResult{
		Scope: "library", LibraryID: libraryID, Mode: m,
		JobID: job.ID, JobKey: job.Key, Created: created,
	}, nil
}

// DocumentCancelResult 取消回执
type DocumentCancelResult struct {
	DocumentID       uint   `json:"document_id"`
	LibraryID        uint   `json:"library_id"`
	BuildState       string `json:"build_state"`       // 当前状态（运行中 → 阶段边界收口为 canceled）
	CancelRequested  bool   `json:"cancel_requested"`  // 置位后恒 true
	AlreadyRequested bool   `json:"already_requested"` // 幂等命中：此前已请求（未重复写、未重复留痕）
}

// CancelDocument 取消文档构建（写谓词门；置 cancel_requested=1，同事务审计 update）。
//
// 语义：标志 = 对后续构建的**持续否决**——运行中管道在阶段边界检查并落 canceled 终态；待跑
// （pending）作业被领取后同样在入口被否决（stopIfCanceled）——故不使用 jobs Cancel（其仅限 pending，
// 且不做业务状态收口）。复位仅由显式文档级重建承担（RebuildDocument）；置位幂等（已在位不重写）。
func (s *Service) CancelDocument(ctx context.Context, actor Actor, documentID uint) (*DocumentCancelResult, error) {
	view, err := s.BuildReadView(ctx, actor.Kind, actor.ID)
	if err != nil {
		return nil, err
	}
	if documentID == 0 {
		return nil, ErrDenied
	}

	out := &DocumentCancelResult{}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		cond, args := s.documentAccessCond(view, model.KBGrantPermWrite)
		args = append([]any{documentID}, args...)
		row, err := gorm.G[model.KBDocument](tx).
			Where(s.tDocument+".id = ? AND "+cond, args...).
			Select(documentDetailColumns).
			First(ctx)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrDenied // 不存在/无权同型
		}
		if err != nil {
			return err
		}
		out.DocumentID, out.LibraryID, out.BuildState = row.ID, row.LibraryID, row.BuildState
		if ingest.Canceled(&row) {
			out.CancelRequested, out.AlreadyRequested = true, true
			return nil // 幂等：不重写、不重复留痕
		}
		if _, err := gorm.G[map[string]any](tx).Table(s.tDocument).
			Where("id = ? AND deleted_at IS NULL", row.ID).
			Updates(ctx, map[string]any{
				"cancel_requested": 1,
				"updated_at":       gorm.Expr("NOW()"),
			}); err != nil {
			return err
		}
		out.CancelRequested = true
		return writeAudit(ctx, tx, AuditEntry{
			LibraryID: row.LibraryID, DocumentID: row.ID, Actor: actor, Action: model.KBAuditActionUpdate,
			Detail: map[string]any{"event": "cancel_requested", "build_state": row.BuildState},
		})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// DocumentFailedOptions 失败列表选项（build_state='failed'；库收窄可选——0=全可读域运维视图）
type DocumentFailedOptions struct {
	Page      int
	Limit     int
	SortField string
	SortOrder string
	Keyword   string // title ILIKE（trgm 表达式 GIN 支撑：idx_kb_document_title_trgm）
}

// ListFailedDocuments 失败文档列表（build_state='failed'；读谓词单点 + Count/List 同构造器 + 分页上限）。
//
// 收窄面：libraryID（0=全可读域跨库运维视图；>0 走 idx_kb_document_library_build_state）、keyword（标题快搜）。
// 挂起待批（failed_stage='budget'，build_state 保留在途态）**不在本表**——以 failed_stage='budget'
// 识别，属独立处置面（未随本批开放）。
// 性能四问：① 跨库面 build_state 无单列索引 ⇒ kb_document 过滤扫描（非增长表；量级=存量文档数），
// 带库收窄走复合索引；② 扫描量级 = 库/全库文档数；③ 返回上限 = MaxBrowseLimit（100）；
// ④ 收敛 = 库收窄 + 分页；存量抬升再议索引（届时随迁移补，不在本批）。
func (s *Service) ListFailedDocuments(ctx context.Context, v *ReadView, libraryID uint, opts DocumentFailedOptions) ([]model.KBDocument, int64, error) {
	if v == nil {
		return nil, 0, ErrDenied
	}
	cond, args := s.documentAccessCond(v, model.KBGrantPermRead)
	q := gorm.G[model.KBDocument](s.db).Where(cond, args...).
		Where(s.tDocument+".build_state = ?", model.KBDocumentBuildFailed)
	cq := gorm.G[model.KBDocument](s.db).Where(cond, args...).
		Where(s.tDocument+".build_state = ?", model.KBDocumentBuildFailed)
	if libraryID > 0 {
		q = q.Where(s.tDocument+".library_id = ?", libraryID)
		cq = cq.Where(s.tDocument+".library_id = ?", libraryID)
	}
	if kw := strings.TrimSpace(opts.Keyword); kw != "" {
		like := "%" + escapeLike(kw) + "%"
		q = q.Where(s.tDocument+".title ILIKE ?", like)
		cq = cq.Where(s.tDocument+".title ILIKE ?", like)
	}

	total, err := cq.Count(ctx, "id")
	if err != nil {
		return nil, 0, err
	}

	field, order := normalizeSortList(opts.SortField, opts.SortOrder, documentSortFields)
	scopes := s.documentBase.BuildScopes(service.Options{
		SortField: field,
		SortOrder: order,
		Page:      opts.Page,
		Limit:     NormalizeBrowseLimit(opts.Limit),
	})
	rows, err := q.Select(documentListColumns).Scopes(scopes...).Find(ctx)
	if err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}
