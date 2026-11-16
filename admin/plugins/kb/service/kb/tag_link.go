// plugin: kb v1.0.0
// 知识库标签绑定面：
// kb_tag_link 的**手写**绑定/解绑/读取（关联表无独立单表语义，故不走 CRUD 生成器）。
//
// 硬条款：
//   - **硬删**：解除即删行（无软删列）；留痕走 kb_audit_log（tag_add/tag_remove）与业务**同事务**；
//   - **幂等**：绑定 = 多值 INSERT ... ON CONFLICT DO NOTHING（uk(tag_id,object_type,object_id)）；
//     重复绑定不产重复行、重复请求不重复留痕（真变才写审计）；
//   - **对象级校验**：一切读写先经对象 → 库谓词（文档侧单点 documentAccessCond；禁按裸 id 取数）；
//     对象面的门 = **写谓词**（owner ∪ write 授权 ∪ 后台轴；写权不扩管理权，管理权留在库元数据/授权面）；
//   - **跨库引用拒绝**：库作用域标签只可用于本库对象（服务层校验，不建 FK——T2）；平台标签
//     （library_id=0）跨库可用；禁用/已删标签不可新绑定（既有 link 保留）；
//   - 对象锚定 = (document|chunk) + 对象 id（「锚定对象」口径）；chunk 对象经其所属文档过谓词。
//
// 拒绝语义：对象不存在/无权 = ErrDenied（「不存在」与「无权」同型，HTTP 层 404）；标签侧的校验失败
// （不存在/禁用/跨库）是**校验错**，响亮回显原因（不是隔离拒绝，不并入 ErrDenied 族）。
package kb

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ai-go-hub/ai-go-admin/plugins/kb/model"
	kbTag "github.com/ai-go-hub/ai-go-admin/plugins/kb/service/kb/tag"

	"gorm.io/gorm"
)

// maxTagBatch 单次绑定/解绑的标签数上限（防无界多值 INSERT；超限响亮拒绝）
const maxTagBatch = 200

// tagObjectTypes 锚定对象类型闭集（kb_tag_link.object_type；加值零迁移——改此表即改契约）
var tagObjectTypes = map[string]bool{
	model.KBTagObjectDocument: true,
	model.KBTagObjectChunk:    true,
}

// TagLinkResult 绑定/解绑回执
type TagLinkResult struct {
	ObjectType string `json:"object_type"`
	ObjectID   uint   `json:"object_id"`
	LibraryID  uint   `json:"library_id"`
	DocumentID uint   `json:"document_id"` // chunk 对象的所属文档（document 对象 = 自身）
	Bound      int64  `json:"bound"`       // 新落行数（幂等命中不计）
	Skipped    int    `json:"skipped"`     // 幂等命中数（已在位）
	Removed    int64  `json:"removed"`     // 解绑删除行数
}

// ObjectTag 对象已挂标签行（详情面读取；LEFT JOIN 保留词表已删的悬挂 link——如实回显不静默吞）
type ObjectTag struct {
	LinkID    uint       `json:"link_id"`
	TagID     uint       `json:"tag_id"`
	TagName   string     `json:"tag_name"`
	TagStatus uint8      `json:"tag_status"`
	GroupID   uint       `json:"group_id"`
	GroupName string     `json:"group_name"`
	Scope     string     `json:"scope"`
	LibraryID uint       `json:"library_id"` // 标签所属库（平台组=0）
	CreatedAt *time.Time `json:"created_at"`
}

// tagTarget 锚定对象的归属解析结果（对象级校验通过后产出）
type tagTarget struct {
	LibraryID  uint
	DocumentID uint
}

// BindTags 批量绑定标签到对象（幂等；同事务审计 tag_add）。
//
// 步骤：① 读视图（两轴分派）→ ② 对象过**写谓词**（不存在/无权同型 ErrDenied）→ ③ 词表引用校验
// （不存在/禁用/跨库引用一律响亮拒）→ ④ ON CONFLICT DO NOTHING 落行 → ⑤ 真有新行才落审计。
func (s *Service) BindTags(ctx context.Context, actor Actor, objectType string, objectID uint, tagIDs []uint) (*TagLinkResult, error) {
	view, ids, err := s.prepareTagLinkWrite(ctx, actor, objectType, objectID, tagIDs)
	if err != nil {
		return nil, err
	}

	res := &TagLinkResult{ObjectType: objectType, ObjectID: objectID}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		target, err := s.tagTargetRow(ctx, tx, view, objectType, objectID, model.KBGrantPermWrite)
		if err != nil {
			return err
		}
		res.LibraryID, res.DocumentID = target.LibraryID, target.DocumentID

		refs, err := kbTag.LoadTagRefsByIDs(ctx, tx, ids)
		if err != nil {
			return err
		}
		for _, id := range ids {
			ref, ok := refs[id]
			if !ok {
				return fmt.Errorf("kb: 标签不存在或已删: %d", id)
			}
			if ref.Status != 1 {
				return fmt.Errorf("kb: 标签已停用，不可新绑定: %d", id)
			}
			if ref.Scope == model.KBTagScopeLibrary && ref.LibraryID != target.LibraryID {
				return fmt.Errorf("kb: 标签 %d 属库 %d，不可跨库引用（目标库 %d）", id, ref.LibraryID, target.LibraryID)
			}
			if ref.Scope != model.KBTagScopePlatform && ref.Scope != model.KBTagScopeLibrary {
				return fmt.Errorf("kb: 标签作用域未登记: %s（标签 %d）", ref.Scope, id)
			}
		}

		bound, err := insertTagLinks(ctx, tx, ids, objectType, objectID, actor.ID)
		if err != nil {
			return err
		}
		res.Bound = bound
		res.Skipped = len(ids) - int(bound)
		if bound == 0 {
			return nil // 幂等命中：不重复留痕（免噪音；先例 UpdateLibrary 真变才写）
		}
		return writeAudit(ctx, tx, AuditEntry{
			LibraryID:  target.LibraryID,
			DocumentID: target.DocumentID,
			Actor:      actor,
			Action:     model.KBAuditActionTagAdd,
			Detail: map[string]any{
				"object_type": objectType, "object_id": objectID,
				"tag_ids": ids, "bound": bound, "skipped": res.Skipped,
			},
		})
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

// UnbindTags 从对象解绑标签（硬删行；同事务审计 tag_remove）。
func (s *Service) UnbindTags(ctx context.Context, actor Actor, objectType string, objectID uint, tagIDs []uint) (*TagLinkResult, error) {
	view, ids, err := s.prepareTagLinkWrite(ctx, actor, objectType, objectID, tagIDs)
	if err != nil {
		return nil, err
	}

	res := &TagLinkResult{ObjectType: objectType, ObjectID: objectID}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		target, err := s.tagTargetRow(ctx, tx, view, objectType, objectID, model.KBGrantPermWrite)
		if err != nil {
			return err
		}
		res.LibraryID, res.DocumentID = target.LibraryID, target.DocumentID

		del := tx.WithContext(ctx).Exec("DELETE FROM "+tableNameOf(tx, model.KBTagLink{})+
			" WHERE object_type = ? AND object_id = ? AND tag_id IN ?",
			objectType, objectID, int64Slice(ids))
		if del.Error != nil {
			return fmt.Errorf("kb: 解绑标签失败: %w", del.Error)
		}
		res.Removed = del.RowsAffected
		if res.Removed == 0 {
			return nil // 无真变：不落审计
		}
		return writeAudit(ctx, tx, AuditEntry{
			LibraryID:  target.LibraryID,
			DocumentID: target.DocumentID,
			Actor:      actor,
			Action:     model.KBAuditActionTagRemove,
			Detail: map[string]any{
				"object_type": objectType, "object_id": objectID,
				"tag_ids": ids, "removed": res.Removed,
			},
		})
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

// ListObjectTags 对象已挂标签（读谓词门；详情面读取）。
func (s *Service) ListObjectTags(ctx context.Context, v *ReadView, objectType string, objectID uint) ([]ObjectTag, error) {
	if v == nil {
		return nil, ErrDenied
	}
	if _, err := s.normalizeTagLinkInputs(objectType, objectID, nil); err != nil {
		return nil, err
	}
	if _, err := s.tagTargetRow(ctx, s.db, v, objectType, objectID, model.KBGrantPermRead); err != nil {
		return nil, err
	}
	sqlText := "SELECT l.id AS link_id, l.tag_id AS tag_id, t.name AS tag_name, t.status AS tag_status," +
		" g.id AS group_id, g.name AS group_name, g.scope AS scope, g.library_id AS library_id, l.created_at AS created_at" +
		" FROM " + tableNameOf(s.db, model.KBTagLink{}) + " l" +
		" LEFT JOIN " + tableNameOf(s.db, model.KBTag{}) + " t ON t.id = l.tag_id" +
		" LEFT JOIN " + tableNameOf(s.db, model.KBTagGroup{}) + " g ON g.id = t.group_id" +
		" WHERE l.object_type = ? AND l.object_id = ? ORDER BY l.id ASC LIMIT ?"
	rows := make([]ObjectTag, 0, 16)
	if err := s.db.WithContext(ctx).Raw(sqlText, objectType, objectID, MaxHardLimit).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("kb: 读取对象标签失败: %w", err)
	}
	return rows, nil
}

// prepareTagLinkWrite 写面公共前置：读视图 + 入参归一（对象类型闭集、标签集去重 + 非空 + 批量上限）。
func (s *Service) prepareTagLinkWrite(ctx context.Context, actor Actor, objectType string, objectID uint, tagIDs []uint) (*ReadView, []uint, error) {
	view, err := s.BuildReadView(ctx, actor.Kind, actor.ID)
	if err != nil {
		return nil, nil, err
	}
	ids, err := s.normalizeTagLinkInputs(objectType, objectID, tagIDs)
	if err != nil {
		return nil, nil, err
	}
	if len(ids) == 0 {
		return nil, nil, errors.New("kb: 标签集为空（tag_ids 必填）")
	}
	if len(ids) > maxTagBatch {
		return nil, nil, fmt.Errorf("kb: 单次标签数超上限（%d > %d）", len(ids), maxTagBatch)
	}
	return view, ids, nil
}

// normalizeTagLinkInputs 入参归一（对象类型闭集 + 对象 id 必填 + 标签集去重；读面可空、写面的非空
// 与批量上限在 prepareTagLinkWrite 裁决——读面（对象详情）不需要标签集）。
func (s *Service) normalizeTagLinkInputs(objectType string, objectID uint, tagIDs []uint) ([]uint, error) {
	if !tagObjectTypes[objectType] {
		return nil, fmt.Errorf("kb: 标签锚定对象类型未登记: %q（闭集 document|chunk）", objectType)
	}
	if objectID == 0 {
		return nil, errors.New("kb: 标签锚定对象 id 必填（>0）")
	}
	return dedupeUints(tagIDs), nil
}

// tagTargetRow 对象级校验单点：对象 → 归属（库/文档）+ 谓词门（read|write 两轴同源——
// 文档侧单点 documentAccessCond；chunk 对象经其所属文档过谓词，禁按裸 id 放行）。
// 不存在/无权一律 ErrDenied（同型）。
func (s *Service) tagTargetRow(ctx context.Context, db *gorm.DB, v *ReadView, objectType string, objectID uint, perm string) (*tagTarget, error) {
	if v == nil || objectID == 0 {
		return nil, ErrDenied
	}
	cond, args := s.documentAccessCond(v, perm)
	switch objectType {
	case model.KBTagObjectDocument:
		args = append([]any{objectID}, args...)
		row, err := gorm.G[model.KBDocument](db).
			Select("id, library_id").
			Where(s.tDocument+".id = ? AND "+cond, args...).First(ctx)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrDenied
		}
		if err != nil {
			return nil, err
		}
		return &tagTarget{LibraryID: row.LibraryID, DocumentID: row.ID}, nil
	case model.KBTagObjectChunk:
		chunk, err := gorm.G[model.KBChunk](db).
			Select("id, library_id, document_id").
			Where("id = ?", objectID).First(ctx)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrDenied
		}
		if err != nil {
			return nil, err
		}
		args = append([]any{chunk.DocumentID}, args...)
		n, err := gorm.G[model.KBDocument](db).
			Where(s.tDocument+".id = ? AND "+cond, args...).Count(ctx, "id")
		if err != nil {
			return nil, err
		}
		if n == 0 {
			return nil, ErrDenied // 文档不可读写（含已删/禁用库/未授权）：与不存在同型
		}
		return &tagTarget{LibraryID: chunk.LibraryID, DocumentID: chunk.DocumentID}, nil
	}
	return nil, fmt.Errorf("kb: 标签锚定对象类型未登记: %q", objectType)
}

// insertTagLinks 幂等落行（多值 INSERT ... ON CONFLICT DO NOTHING；uk(tag_id,object_type,object_id)）。
// 返回新落行数（冲突行不计）——调用方据此判定「真变」与审计义务。
func insertTagLinks(ctx context.Context, tx *gorm.DB, tagIDs []uint, objectType string, objectID, operatorID uint) (int64, error) {
	if len(tagIDs) == 0 {
		return 0, nil
	}
	var b strings.Builder
	b.WriteString("INSERT INTO " + tableNameOf(tx, model.KBTagLink{}) +
		" (tag_id, object_type, object_id, operator_id, created_at) VALUES ")
	args := make([]any, 0, len(tagIDs)*4)
	for i, id := range tagIDs {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString("(?,?,?,?,NOW())")
		args = append(args, id, objectType, objectID, operatorID)
	}
	b.WriteString(" ON CONFLICT DO NOTHING")
	exec := tx.WithContext(ctx).Exec(b.String(), args...)
	if exec.Error != nil {
		return 0, fmt.Errorf("kb: 绑定标签失败: %w", exec.Error)
	}
	return exec.RowsAffected, nil
}

// purgeTagLinks 清对象侧标签关联（清理面单点；**必须在本批对象的切片行删除之前调用**——
// chunk 关联经 kb_chunk 反查；库级清理传入库内全部文档 id，含软删文档）。
// 口径（I6 清理清单）：document 关联按文档 id 直删；chunk 关联按「该文档切片 id 集」删。
func purgeTagLinks(ctx context.Context, tx *gorm.DB, documentIDs []uint) error {
	if len(documentIDs) == 0 {
		return nil
	}
	chunkIDs := "SELECT id FROM " + tableNameOf(tx, model.KBChunk{}) + " WHERE document_id IN ?"
	sqlText := "DELETE FROM " + tableNameOf(tx, model.KBTagLink{}) +
		" WHERE (object_type = ? AND object_id IN (" + chunkIDs + ")) OR (object_type = ? AND object_id IN ?)"
	if err := tx.WithContext(ctx).Exec(sqlText,
		model.KBTagObjectChunk, documentIDs, model.KBTagObjectDocument, documentIDs).Error; err != nil {
		return fmt.Errorf("kb: 清理标签关联失败: %w", err)
	}
	return nil
}

// dedupeUints 去重（保序；nil/空 = 空集）
func dedupeUints(ids []uint) []uint {
	seen := make(map[uint]struct{}, len(ids))
	out := make([]uint, 0, len(ids))
	for _, id := range ids {
		if id == 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// int64Slice uint 集 → int64 参数（kb_* 主键为 bigint；uint 直传有驱动面风险，统一转换单点）
func int64Slice(ids []uint) []int64 {
	out := make([]int64, len(ids))
	for i, v := range ids {
		out[i] = int64(v)
	}
	return out
}
