// plugin: kb v1.0.0
// 知识库重切 × 标签重放（重切与标签的重放规则）。
//
// 规则（本稿新增，02×03 接缝；本文件即实现落点）：
//   - **文档级 link 不受重切影响**（object_id = kb_document.id 恒稳，重切只动派生面）；
//   - **切片级 link 随旧 build 删除**：换出前捕获（link → 旧 chunk 的 (document_id, text_hash)），
//     换出事务内按 **(document_id, text_hash)** 重放——命中 ⇒ 重挂（object_id 换新 chunk）；
//     不命中 ⇒ 丢弃并**落审计**（手工块标签锚失效即弃，不做模糊匹配）。
//
// 与换出流程的衔接（两半，缺一不可）：
//  1. captureChunkTagReplay：RunIngest 在 ingest.SwitchBuild **之前**调用（此刻旧 build 切片行仍在位，
//     link → text_hash 可解出；换出事务内旧行已删，读不到）。只读，失败即响亮失败（禁静默跳过——
//     跳过会让旧 link 静默悬空、重放规则被绕过）。
//  2. applyChunkTagReplay：随 SwitchBuild 的 beforeCommit 回调在**换出事务内**执行（与 active_build
//     换出、旧行删除同事务）：重挂与审计要么全成、要么全回滚，不留半程态。
//
// 幂等与并发：重挂 = 多值 INSERT ... ON CONFLICT DO NOTHING（uk(tag_id,object_type,object_id)）+
// 带 object_id 守卫的删旧行（VALUES JOIN）——重复重放/与绑定面并发都不产重复行、不误删他行。
//
// 遗留登记（如实）：系统面孤儿 link（object_id 指向已不存在的 chunk——早于本批的悬空行、或非换出路径
// 的删除）不在本重放面：捕获以「活切片行」为左表，孤儿无 document_id 可归，交由 kb:purge 清理面兜底。
package kb

import (
	"context"
	"fmt"
	"strings"

	"github.com/ai-go-hub/ai-go-admin/plugins/kb/model"

	"gorm.io/gorm"
)

// chunkTagReplayEntry 重放计划行（换出前捕获：link → 旧 chunk 的复算键）
type chunkTagReplayEntry struct {
	LinkID     uint   // kb_tag_link.id（换出事务内据此删旧行）
	TagID      uint   // 标签 id（重挂落到新 chunk 上）
	TextHash   string // 旧 chunk 的 text_hash（重放键的第二半：(document_id, text_hash)）
	OldChunkID uint   // 旧 chunk id（删旧行的守卫列）
}

// captureChunkTagReplay 捕获本文档的切片级标签重放计划（只读；换出前调用）。
// 空集 = 该文档无切片级 link（首轮导入/仅文档级标签）——合法，不视为错误。
func (s *Service) captureChunkTagReplay(ctx context.Context, documentID uint) ([]chunkTagReplayEntry, error) {
	if documentID == 0 {
		return nil, fmt.Errorf("kb: 标签重放捕获缺文档 id")
	}
	// 以切片行为左表（走 uk_kb_chunk(document_id,…) 前缀），逐行探 kb_tag_link
	// 的 idx(object_type,object_id,tag_id)——不是「全表扫 link 再回表」。
	sqlText := "SELECT l.id AS link_id, l.tag_id AS tag_id, c.text_hash AS text_hash, c.id AS old_chunk_id" +
		" FROM " + tableNameOf(s.db, model.KBChunk{}) + " c" +
		" JOIN " + tableNameOf(s.db, model.KBTagLink{}) + " l ON l.object_type = ? AND l.object_id = c.id" +
		" WHERE c.document_id = ? ORDER BY l.id ASC"
	rows := make([]chunkTagReplayEntry, 0, 8)
	if err := s.db.WithContext(ctx).Raw(sqlText, model.KBTagObjectChunk, documentID).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("kb: 捕获切片标签重放计划失败: %w", err)
	}
	return rows, nil
}

// applyChunkTagReplay 在换出事务内执行重放（旧 build 行刚被删；新 build 行在位）。
// 返回 (重挂行数, 丢弃行数)；丢弃逐条落审计（tag_remove + detail.event=replay_drop，携带 library_id）。
func (s *Service) applyChunkTagReplay(ctx context.Context, tx *gorm.DB, documentID, libraryID uint, buildHash string, plan []chunkTagReplayEntry) (int64, int64, error) {
	if len(plan) == 0 {
		return 0, 0, nil
	}
	if tx == nil || documentID == 0 || strings.TrimSpace(buildHash) == "" {
		return 0, 0, fmt.Errorf("kb: 标签重放参数非法")
	}

	// ① 新 build 的 text_hash → chunk id 映射（换出后旧行已删，此映射即「命中」的全部候选）
	type hashRow struct {
		ID       uint   `gorm:"column:id"`
		TextHash string `gorm:"column:text_hash"`
	}
	rows := make([]hashRow, 0, len(plan))
	// ORDER BY seq,id：同 build 内同文本多块（重复段落/模板句）时「取首个」的确定性由 SQL 显式钉死
	//（已整改：原查询无排序——PG 扫描序不构成保证，注释自称的确定性此前不成立）。
	if err := tx.WithContext(ctx).Raw("SELECT id, text_hash FROM "+tableNameOf(tx, model.KBChunk{})+
		" WHERE document_id = ? AND build_hash = ? ORDER BY seq ASC, id ASC", documentID, buildHash).Scan(&rows).Error; err != nil {
		return 0, 0, fmt.Errorf("kb: 读取新 build 切片失败: %w", err)
	}
	byHash := make(map[string]uint, len(rows))
	for i := range rows {
		if _, ok := byHash[rows[i].TextHash]; !ok {
			byHash[rows[i].TextHash] = rows[i].ID // 同 build 内同文本多块：取 seq 最小者（ORDER BY seq,id 已钉）
		}
	}

	// ② 分桶：命中重挂（新 id ≠ 旧 id）/ 同 id 免动 / 失配
	type relink struct{ tagID, newID uint }
	relinks := make([]relink, 0, len(plan))
	oldIDs := make([]uint, 0, len(plan))       // 待删旧行（重挂 + 失配）
	oldObjectIDs := make([]uint, 0, len(plan)) // 与之配对的守卫列（VALUES JOIN 用）
	dropped := make([]chunkTagReplayEntry, 0)
	for _, e := range plan {
		newID, ok := byHash[e.TextHash]
		if ok && newID == e.OldChunkID {
			continue // 已在目标行（重复重放/并发重放）：免动
		}
		oldIDs = append(oldIDs, e.LinkID)
		oldObjectIDs = append(oldObjectIDs, e.OldChunkID)
		if ok {
			relinks = append(relinks, relink{tagID: e.TagID, newID: newID})
			continue
		}
		dropped = append(dropped, e)
	}

	// ③ 重挂：多值 INSERT ... ON CONFLICT DO NOTHING（operator_id=0：system 作业面；轴归属见导入审计行）
	var relinked int64
	if len(relinks) > 0 {
		var b strings.Builder
		b.WriteString("INSERT INTO " + tableNameOf(tx, model.KBTagLink{}) +
			" (tag_id, object_type, object_id, operator_id, created_at) VALUES ")
		args := make([]any, 0, len(relinks)*4)
		for i, r := range relinks {
			if i > 0 {
				b.WriteString(",")
			}
			b.WriteString("(?,?,?,0,NOW())")
			args = append(args, r.tagID, model.KBTagObjectChunk, r.newID)
		}
		b.WriteString(" ON CONFLICT DO NOTHING")
		exec := tx.WithContext(ctx).Exec(b.String(), args...)
		if exec.Error != nil {
			return 0, 0, fmt.Errorf("kb: 重挂标签失败: %w", exec.Error)
		}
		relinked = exec.RowsAffected
	}

	// ④ 删旧行（带 object_id 守卫：只删「仍指向旧 chunk」的行，防误删并发改动的行）
	if len(oldIDs) > 0 {
		var b strings.Builder
		b.WriteString("DELETE FROM " + tableNameOf(tx, model.KBTagLink{}) + " l USING (VALUES ")
		args := make([]any, 0, len(oldIDs)*2)
		for i := range oldIDs {
			if i > 0 {
				b.WriteString(",")
			}
			b.WriteString("(?::bigint, ?::bigint)")
			args = append(args, oldIDs[i], oldObjectIDs[i])
		}
		b.WriteString(") AS v(id, object_id) WHERE l.id = v.id AND l.object_id = v.object_id")
		if err := tx.WithContext(ctx).Exec(b.String(), args...).Error; err != nil {
			return 0, 0, fmt.Errorf("kb: 删旧标签关联失败: %w", err)
		}
	}

	// ⑤ 失配丢弃：逐条落审计（tag_remove + event=replay_drop；与业务同事务）
	for _, e := range dropped {
		if err := writeAudit(ctx, tx, AuditEntry{
			LibraryID:  libraryID,
			DocumentID: documentID,
			Actor:      systemActor,
			Action:     model.KBAuditActionTagRemove,
			Detail: map[string]any{
				"event": "replay_drop", "reason": "text_hash 失配（不做模糊匹配）",
				"object_type": model.KBTagObjectChunk, "tag_id": e.TagID,
				"old_chunk_id": e.OldChunkID, "text_hash": e.TextHash, "build_hash": buildHash,
			},
		}); err != nil {
			return 0, 0, err
		}
	}
	return relinked, int64(len(dropped)), nil
}
