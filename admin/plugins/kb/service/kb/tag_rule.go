// plugin: kb v1.0.0
// 规则打标应用面（打标方式：
// 规则 = 导入/重导入批次内求值，重跑同结果、幂等）。
//
// 三面分工（写面单点；求值引擎在 service/kb/tag/rule.go——**只读**）：
//   - 求值 = kbTag.EvaluateForLibrary（装载 kb_tag_rule 按 enabled/priority → 纯函数匹配）；
//   - 落行 = insertTagLinks（绑定面**同一单点**：多值 INSERT … ON CONFLICT DO NOTHING ⇒ 重跑幂等）；
//   - 留痕 = writeAudit（tag_add + detail.event='rule'；**真变才写**，先例 BindTags「真变才留痕」）。
//
// 事务：主链调用点 = RunIngest 的换出事务内（标签与换出要么全成、要么全回滚，不留半程态）；
// 零变更重跑路径无在途事务 ⇒ 走 applyRuleTagsStandalone 的独立事务形态（同一语义）。
//
// 作用域 = **文档级** link（object_type=document）：匹配主体是文档属性（路径/文件名/后缀/元信息），
// 逐切片重复同一判定无增益；文档级 link 不受重切影响（：仅切片级 link 随旧 build 删除并按
// text_hash 重放）⇒ 重切批次内的「重放」= 重跑求值的幂等空操作。规则删除/停用后的既有 link
// **不回撤**（投影重建，登记为待实施）——本面只增不删。
//
// 跳过项（标签停用/已删/跨库、匹配式非法等）经 notes 由调用方落作业 warnings（响亮回显，不静默）；
// 装载读失败 = 响亮失败（整事务回滚——规则面读不出来时不静默放行半套打标）。
package kb

import (
	"context"
	"errors"

	"github.com/ai-go-hub/ai-go-admin/plugins/kb/model"
	kbTag "github.com/ai-go-hub/ai-go-admin/plugins/kb/service/kb/tag"

	"gorm.io/gorm"
)

// applyRuleTags 导入批次内的规则打标（须在导入完成的同一事务内调用；tx 由调用方给出）。
// 返回 (新落 link 行数, 跳过告警, error)。
func (s *Service) applyRuleTags(ctx context.Context, tx *gorm.DB, doc *model.KBDocument) (int64, []string, error) {
	if tx == nil || doc == nil || doc.ID == 0 || doc.LibraryID == 0 {
		return 0, nil, errors.New("kb: 规则打标参数非法（缺事务/文档/库）")
	}
	res, err := kbTag.EvaluateForLibrary(ctx, tx, doc.LibraryID, kbTag.Subject{
		Title:      doc.Title,
		Path:       doc.SourceRef,
		MIME:       doc.Mime,
		SourceKind: doc.SourceKind,
	})
	if err != nil {
		return 0, nil, err
	}
	notes := res.SkipNotes()
	if len(res.TagIDs) == 0 {
		return 0, notes, nil
	}
	bound, err := insertTagLinks(ctx, tx, res.TagIDs, model.KBTagObjectDocument, doc.ID, 0)
	if err != nil {
		return 0, nil, err
	}
	if bound == 0 {
		return 0, notes, nil // 幂等命中（重复导入/手工先绑）：不产重复行、不重复留痕
	}
	ruleIDs := make([]uint, 0, len(res.Hits))
	for i := range res.Hits {
		ruleIDs = append(ruleIDs, res.Hits[i].RuleID)
	}
	if err := writeAudit(ctx, tx, AuditEntry{
		LibraryID:  doc.LibraryID,
		DocumentID: doc.ID,
		Actor:      systemActor,
		Action:     model.KBAuditActionTagAdd,
		Detail: map[string]any{
			"event": "rule", "object_type": model.KBTagObjectDocument, "object_id": doc.ID,
			"rule_ids": ruleIDs, "tag_ids": res.TagIDs, "bound": bound,
			"skipped_rules": len(res.Skipped),
		},
	}); err != nil {
		return 0, nil, err
	}
	return bound, notes, nil
}

// applyRuleTagsStandalone 独立事务形态（零变更重跑路径：该路径无在途事务；失败 = 响亮失败）。
// 回执读数（RuleTags + 跳过告警）在提交成功后写回作业结果，调用点保持最小。
func (s *Service) applyRuleTagsStandalone(ctx context.Context, doc *model.KBDocument, res *IngestResult) error {
	if res == nil {
		return errors.New("kb: 规则打标回执载体缺失")
	}
	var applied int64
	var notes []string
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var aerr error
		applied, notes, aerr = s.applyRuleTags(ctx, tx, doc)
		return aerr
	})
	if err != nil {
		return err
	}
	res.RuleTags = applied
	res.Warnings = append(res.Warnings, notes...)
	return nil
}
