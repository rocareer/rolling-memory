// plugin: kb v1.0.0
// 知识库作业接线 · kb:purge 清理（处理器 + 键解析 + 库/文档尾巴清理与硬删面；/ 入队钩子接线）。
//
// 从 job.go 拆出（文件规模纪律：按职责拆分而非按行数硬切）；共享装配面见 job.go。
package kb

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/ai-go-hub/ai-go-admin/plugins/kb/model"
	"github.com/ai-go-hub/ai-go-admin/plugins/kb/service/jobs"
	"github.com/ai-go-hub/ai-go-admin/plugins/kb/service/kb/ingest"

	"gorm.io/gorm"
)

// ==================== kb:purge 处理器 ====================

// purgeHandler kb:purge 处理器（幂等可重入；键 kb:<library_id> / kb:doc:<document_id>）
type purgeHandler struct{}

// Run 执行清理（按键解析目标；父对象必须已软删——防误清活数据）
func (purgeHandler) Run(ctx context.Context, j *jobs.Job) error {
	libID, docID, err := parsePurgeKey(j.Key)
	if err != nil {
		return err
	}
	svc := NewService()
	return svc.RunPurge(ctx, libID, docID)
}

// parsePurgeKey 解析清理键（kb:<library_id> 库级 / kb:doc:<document_id> 文档级）
func parsePurgeKey(key string) (libraryID, documentID uint, err error) {
	k := strings.TrimSpace(key)
	switch {
	case strings.HasPrefix(k, "kb:doc:"):
		id, perr := strconv.ParseUint(strings.TrimPrefix(k, "kb:doc:"), 10, 64)
		if perr != nil || id == 0 {
			return 0, 0, fmt.Errorf("kb: 清理键非法: %q", key)
		}
		return 0, uint(id), nil
	case strings.HasPrefix(k, "kb:"):
		id, perr := strconv.ParseUint(strings.TrimPrefix(k, "kb:"), 10, 64)
		if perr != nil || id == 0 {
			return 0, 0, fmt.Errorf("kb: 清理键非法: %q", key)
		}
		return uint(id), 0, nil
	default:
		return 0, 0, fmt.Errorf("kb: 清理键未登记前缀: %q", key)
	}
}

// RunPurge 清理入口（文档级 / 库级；幂等可重入）。
func (s *Service) RunPurge(ctx context.Context, libraryID, documentID uint) error {
	switch {
	case documentID != 0 && libraryID == 0:
		return s.purgeDocument(ctx, documentID)
	case libraryID != 0 && documentID == 0:
		return s.purgeLibrary(ctx, libraryID)
	default:
		return errors.New("kb: 清理参数非法（二选一：library_id / document_id）")
	}
}

// purgeDocument 删文档后的尾巴清理：切片 → 向量 → 原始件登记（附孤儿 blob）→ 文档行（硬删）。
// 守卫：文档必须已软删或已不存在（防误清活数据——活文档的清理只有删除动作本身会产生）。
func (s *Service) purgeDocument(ctx context.Context, documentID uint) error {
	row, err := s.scopedDocument(ctx, documentID)
	if err != nil {
		return err
	}
	if row == nil {
		return nil // 已被清理（幂等重入）
	}
	if !softDeleted(row.DeletedAt) {
		return fmt.Errorf("kb: 文档 %d 未软删，拒绝清理", documentID)
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 标签关联先于切片清（I6 清理清单：标签关联随对象；purgeTagLinks 的 chunk 关联经
		// kb_chunk 反查——必须在切片行删除**之前**调用）
		if err := purgeTagLinks(ctx, tx, []uint{documentID}); err != nil {
			return err
		}
		if err := purgeChunksOfDocuments(ctx, tx, []uint{documentID}); err != nil {
			return err
		}
		// 提取物随文档清理（I6 清单的补充：提取文本是文档内容的派生真源，
		// 留行 = 删除后内容仍在库内——按「删文档」语义一并清）
		if err := hardDelete(ctx, tx, "kb_extract", "document_id = ?", documentID); err != nil {
			return err
		}
		if err := purgeBlobRefs(ctx, tx, "document_id = ?", documentID); err != nil {
			return err
		}
		if err := hardDelete(ctx, tx, "kb_document", "id = ?", documentID); err != nil {
			return err
		}
		return writeAudit(ctx, tx, AuditEntry{
			LibraryID: row.LibraryID, DocumentID: documentID, Actor: systemActor, Action: model.KBAuditActionPurge,
			Detail: map[string]any{"scope": "document", "title": row.Title},
		})
	})
}

// purgeLibrary 删库后的尾巴清理：文档（含切片/向量/原始件登记）→ 授权 → 水位 → 库行（硬删）。
// 守卫：库必须已软删或已不存在；审计行只增不改不删（不清）。
func (s *Service) purgeLibrary(ctx context.Context, libraryID uint) error {
	lib, err := s.scopedLibrary(ctx, libraryID)
	if err != nil {
		return err
	}
	if lib != nil && !softDeleted(lib.DeletedAt) {
		return fmt.Errorf("kb: 库 %d 未软删，拒绝清理", libraryID)
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		docIDs, err := documentIDsOfLibrary(ctx, tx, libraryID)
		if err != nil {
			return err
		}
		// 标签关联清在切片行之前（同 purgeDocument：chunk 关联经 kb_chunk 反查；I6 清理清单含标签关联）
		if err := purgeTagLinks(ctx, tx, docIDs); err != nil {
			return err
		}
		if err := purgeChunksOfDocuments(ctx, tx, docIDs); err != nil {
			return err
		}
		if err := hardDelete(ctx, tx, "kb_extract", "document_id IN ?", docIDs); err != nil {
			return err
		}
		if err := purgeBlobRefs(ctx, tx, "library_id = ?", libraryID); err != nil {
			return err
		}
		if err := hardDelete(ctx, tx, "kb_grant", "library_id = ?", libraryID); err != nil {
			return err
		}
		if err := hardDelete(ctx, tx, "kb_produce_state", "library_id = ?", libraryID); err != nil {
			return err
		}
		if err := hardDelete(ctx, tx, "kb_document", "library_id = ?", libraryID); err != nil {
			return err
		}
		if err := hardDelete(ctx, tx, "kb_library", "id = ?", libraryID); err != nil {
			return err
		}
		return writeAudit(ctx, tx, AuditEntry{
			LibraryID: libraryID, Actor: systemActor, Action: model.KBAuditActionPurge,
			Detail: map[string]any{"scope": "library", "documents": len(docIDs)},
		})
	})
}

// purgeChunksOfDocuments 清文档的切片与向量（先向量后切片：chunk_id 引用面）
func purgeChunksOfDocuments(ctx context.Context, tx *gorm.DB, documentIDs []uint) error {
	if len(documentIDs) == 0 {
		return nil
	}
	sql := "DELETE FROM " + tableNameOf(tx, ingest.ChunkVectorRow{}) + " WHERE chunk_id IN (SELECT id FROM " +
		tableNameOf(tx, model.KBChunk{}) + " WHERE document_id IN ?)"
	if err := tx.WithContext(ctx).Exec(sql, documentIDs).Error; err != nil {
		return fmt.Errorf("kb: 清理向量失败: %w", err)
	}
	if err := hardDelete(ctx, tx, "kb_chunk", "document_id IN ?", documentIDs); err != nil {
		return err
	}
	return nil
}

// purgeBlobRefs 清原始件登记，并回收不再被引用的 blob 行（内容寻址共享件：引用计数面 = kb_blob_ref）
func purgeBlobRefs(ctx context.Context, tx *gorm.DB, where string, args ...any) error {
	shaCol := "sha256"
	rows, err := gorm.G[ingest.BlobRefRow](tx).Select(shaCol).Where(where, args...).Find(ctx)
	if err != nil {
		return fmt.Errorf("kb: 读原始件登记失败: %w", err)
	}
	shaSet := make([]string, 0, len(rows))
	for i := range rows {
		if rows[i].SHA256 != "" {
			shaSet = append(shaSet, rows[i].SHA256)
		}
	}
	if err := hardDelete(ctx, tx, "kb_blob_ref", where, args...); err != nil {
		return err
	}
	if len(shaSet) == 0 {
		return nil
	}
	orphan := "DELETE FROM " + tableNameOf(tx, ingest.BlobRow{}) +
		" WHERE sha256 IN ? AND NOT EXISTS (SELECT 1 FROM " + tableNameOf(tx, ingest.BlobRefRow{}) + " r WHERE r.sha256 = " +
		tableNameOf(tx, ingest.BlobRow{}) + ".sha256)"
	if err := tx.WithContext(ctx).Exec(orphan, shaSet).Error; err != nil {
		return fmt.Errorf("kb: 回收孤儿原始件失败: %w", err)
	}
	return nil
}

// documentIDsOfLibrary 库内全部文档 id（**含软删**——清理面必须覆盖软删行；泛型 API 无 Unscoped
// ⇒ 传统 API 单点。软删文档同样有切片/向量/提取物尾巴要清）
func documentIDsOfLibrary(ctx context.Context, tx *gorm.DB, libraryID uint) ([]uint, error) {
	var ids []uint
	if err := tx.WithContext(ctx).Unscoped().Model(&model.KBDocument{}).
		Where("library_id = ?", libraryID).
		Pluck("id", &ids).Error; err != nil {
		return nil, fmt.Errorf("kb: 读库内文档失败: %w", err)
	}
	return ids, nil
}

// hardDelete 硬删（raw SQL；软删模型经 Unscoped 语义——清理面必须真删）
func hardDelete(ctx context.Context, tx *gorm.DB, table, where string, args ...any) error {
	if err := tx.WithContext(ctx).Exec("DELETE FROM "+table+" WHERE "+where, args...).Error; err != nil {
		return fmt.Errorf("kb: 硬删 %s 失败: %w", table, err)
	}
	return nil
}

// scopedDocument 读文档（含软删；不存在 = nil。泛型 API 无 Unscoped ⇒ 传统 API 单点）。
// 列面 = documentDetailColumns + **deleted_at**：清理守卫按 deleted_at 放行，不选该列则软删行读出的
// DeletedAt 恒为 nil ⇒ 守卫永远拒清（实测：kb:purge 恒 failed「未软删」）。
func (s *Service) scopedDocument(ctx context.Context, documentID uint) (*model.KBDocument, error) {
	var row model.KBDocument
	err := s.db.WithContext(ctx).Unscoped().
		Select(documentDetailColumns+", deleted_at").Where("id = ?", documentID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("kb: 读文档（含软删）失败: %w", err)
	}
	return &row, nil
}

// softDeleted 软删判定（**唯一口径**：nil 指针与无效值都算未软删——bpchar/NULL 扫描语义两可时均安全）。
func softDeleted(dt *gorm.DeletedAt) bool { return dt != nil && dt.Valid }

// scopedLibrary 读库（含软删；不存在 = nil）。列面同 scopedDocument（deleted_at 必须入选）。
func (s *Service) scopedLibrary(ctx context.Context, libraryID uint) (*model.KBLibrary, error) {
	var row model.KBLibrary
	err := s.db.WithContext(ctx).Unscoped().
		Select(libraryDetailColumns+", deleted_at").Where("id = ?", libraryID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("kb: 读库（含软删）失败: %w", err)
	}
	return &row, nil
}
