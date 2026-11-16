// plugin: kb v1.0.0
package kb

// 规则打标求值链连库用例（规则打标口径：命中落文档级 link + 事件审计）：
//   - 导入批次内求值：RunIngest 的换出事务内命中 → **文档级** link + tag_add（event=rule）审计，
//     rule_ids 按 (priority ASC, id ASC) 序；
//   - 批次内幂等（重跑不重复、同结果）：应用面重跑零新行、零重复留痕（ON CONFLICT DO NOTHING +
//     真变才写审计）；同 build 重跑导入（零变更路径）link/审计不变；
//   - 优先级与 enabled：排序由 (priority,id) 决定；enabled=0 不参与（装载 SQL 过滤）；
//   - 跨库规则隔离：库 B 的规则不作用于库 A 文档；库 A 规则引用库 B 标签在装载期被跳过（不落行）；
//   - 四类 match_type 端到端接线（path_glob / filename_regex / suffix / meta_eq 正例 + 反例不命中）。
//
// 门控：PLUGIN_KB_DB=1（同 //；未设时 DB 用例一律 SKIP——不得当作通过）。
// 跑法：PLUGIN_KB_DB=1 go test -count=1 -timeout=120s ./plugins/kb/service/kb/...
import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ai-go-hub/ai-go-admin/plugins/kb/model"
	"github.com/ai-go-hub/ai-go-admin/plugins/kb/service/kb/ingest"

	"gorm.io/gorm"
)

// ==================== 规则夹具（kbFixture 清理面不含 kb_tag_rule——本夹具自清） ====================

type ruleRowsFixture struct {
	t   *testing.T
	ctx context.Context
	db  *gorm.DB

	ids []uint
}

// newRuleRows 规则夹具（自清：硬删本夹具建的规则行；link 行随 newTagRows 的 tag_id 面清）
func newRuleRows(t *testing.T, f *kbFixture) *ruleRowsFixture {
	t.Helper()
	r := &ruleRowsFixture{t: t, ctx: f.ctx, db: f.db}
	t.Cleanup(r.cleanup)
	return r
}

// cleanup 硬删本夹具的规则行（规则无 link 面；引用校验由求值/管理面各自裁决）
func (r *ruleRowsFixture) cleanup() {
	if len(r.ids) == 0 {
		return
	}
	if err := r.db.Exec("DELETE FROM kb_tag_rule WHERE id IN ?", r.ids).Error; err != nil {
		r.t.Logf("清理标签规则失败（请手工核）: %v", err)
	}
}

// rule 建规则（enabled：0=停用，1=启用；priority 小者先评）
func (r *ruleRowsFixture) rule(libraryID uint, name, matchType, pattern string, tagID uint, priority int64, enabled uint8) *model.KBTagRule {
	r.t.Helper()
	row := &model.KBTagRule{
		LibraryID: libraryID, Name: name, MatchType: matchType, Pattern: pattern,
		TagID: tagID, Priority: priority, Enabled: &enabled,
	}
	if err := gorm.G[model.KBTagRule](r.db).Create(r.ctx, row); err != nil {
		r.t.Fatalf("建标签规则失败: %v", err)
	}
	r.ids = append(r.ids, row.ID)
	return row
}

// applyInTx 直接调应用面（事务内；不经导入链——跨库/幂等断言的探针）
func (f *pipelineFixture) applyInTx(t *testing.T, doc *model.KBDocument) (int64, []string) {
	t.Helper()
	var bound int64
	var notes []string
	err := f.db.WithContext(f.ctx).Transaction(func(tx *gorm.DB) error {
		b, n, aerr := f.svc.applyRuleTags(f.ctx, tx, doc)
		bound, notes = b, n
		return aerr
	})
	if err != nil {
		t.Fatalf("规则打标应用失败: %v", err)
	}
	return bound, notes
}

// ruleAuditDetails 本库文档的 tag_add 审计明细（id 升序；解析 JSON）
func (f *pipelineFixture) ruleAuditDetails(t *testing.T, libraryID, documentID uint) []map[string]any {
	t.Helper()
	var rows []model.KBAuditLog
	if err := f.db.Model(&model.KBAuditLog{}).
		Where("library_id = ? AND document_id = ? AND action = ?", libraryID, documentID, model.KBAuditActionTagAdd).
		Order("id ASC").Find(&rows).Error; err != nil {
		t.Fatalf("读审计失败: %v", err)
	}
	out := make([]map[string]any, 0, len(rows))
	for i := range rows {
		if rows[i].ActorKind != model.KBActorKindSystem {
			t.Fatalf("规则打标审计主体应为 system: %+v", rows[i])
		}
		detail := map[string]any{}
		if rows[i].Detail != nil {
			if err := json.Unmarshal([]byte(*rows[i].Detail), &detail); err != nil {
				t.Fatalf("审计明细解析失败: %v", err)
			}
		}
		out = append(out, detail)
	}
	return out
}

// warnContains 回执告警含子串（跳过项回显断言）
func warnContains(notes []string, sub string) bool {
	for _, n := range notes {
		if strings.Contains(n, sub) {
			return true
		}
	}
	return false
}

// ==================== 求值链端到端 ====================

// TestRuleEvaluationDB 导入批次内求值：命中落文档级 link + event=rule 审计（priority 序）、
// 反例/enabled=0/停用标签/缺失标签/跨库标签一律零行，跳过项回显告警；应用面重跑与同 build 重跑
// 均幂等（零新行、零重复留痕）；库 B 规则不作用于库 A 文档。
func TestRuleEvaluationDB(t *testing.T) {
	f := newPipelineFixture(t, pipelineFixtureMd) // 文档：source_ref=fixture.md, mime=text/markdown, source_kind=file
	ctx := f.ctx
	libA, docA := f.libID, f.docID

	// 库 B（跨库隔离面）+ 库 B 的库作用域组/标签/规则
	libB := f.library(model.KBLibraryOwnerKindSystem, 0, "kb-rule-libb-"+f.suffix, 1)
	docB := f.document(libB.ID, "kb-rule-docb-"+f.suffix)
	if err := f.db.Model(&model.KBDocument{}).Where("id = ?", docB.ID).
		Update("source_ref", "b-fixture.md").Error; err != nil {
		t.Fatalf("写库 B 文档来源失败: %v", err)
	}
	freshB, err := gorm.G[model.KBDocument](f.db).Where("id = ?", docB.ID).First(ctx)
	if err != nil {
		t.Fatalf("读回库 B 文档失败: %v", err)
	}
	docB = &freshB
	grpB := newTagRows(t, f.kbFixture, model.KBTagScopeLibrary, libB.ID, "kb-rule-grpb-"+f.suffix)
	tagB := grpB.tag("kb-rule-tagb-"+f.suffix, 1)

	// 库 A 的平台组 + 标签（命中四枚 + 反例/停用/enabled=0 目标）
	grpA := newTagRows(t, f.kbFixture, model.KBTagScopePlatform, 0, "kb-rule-grpa-"+f.suffix)
	tagGlob := grpA.tag("kb-rule-glob-"+f.suffix, 1)
	tagRegex := grpA.tag("kb-rule-regex-"+f.suffix, 1)
	tagSuffix := grpA.tag("kb-rule-suffix-"+f.suffix, 1)
	tagMeta := grpA.tag("kb-rule-meta-"+f.suffix, 1)
	tagNoMatch := grpA.tag("kb-rule-nomatch-"+f.suffix, 1) // 反例（不命中）
	tagOff := grpA.tag("kb-rule-off-"+f.suffix, 0)         // 停用标签（规则引用 → 装载期跳过）
	tagRuleOff := grpA.tag("kb-rule-roff-"+f.suffix, 1)    // enabled=0 规则的目标（须零行）
	missTagID := tagNoMatch.ID + 100000                    // 不存在的标签（直连造行面：管理面校验之外）

	rules := newRuleRows(t, f.kbFixture)
	rGlob := rules.rule(libA, "glob-md", model.KBTagRuleMatchPathGlob, "*.md", tagGlob.ID, 10, 1)
	rRegex := rules.rule(libA, "regex-fixture", model.KBTagRuleMatchFilenameRegex, "^fixture", tagRegex.ID, 20, 1)
	rSuffix := rules.rule(libA, "suffix-md", model.KBTagRuleMatchSuffix, "md", tagSuffix.ID, 30, 1)
	rMeta := rules.rule(libA, "meta-mime", model.KBTagRuleMatchMetaEq, "mime=text/markdown", tagMeta.ID, 40, 1)
	rules.rule(libA, "suffix-pdf-nomatch", model.KBTagRuleMatchSuffix, "pdf", tagNoMatch.ID, 50, 1)
	rules.rule(libA, "disabled-rule", model.KBTagRuleMatchSuffix, "md", tagRuleOff.ID, 60, 0)
	rules.rule(libA, "tag-off", model.KBTagRuleMatchSuffix, "md", tagOff.ID, 70, 1)
	rules.rule(libA, "tag-miss", model.KBTagRuleMatchSuffix, "md", missTagID, 80, 1)
	rules.rule(libA, "tag-cross-lib", model.KBTagRuleMatchSuffix, "md", tagB.ID, 90, 1) // 库 B 标签
	rules.rule(libB.ID, "b-suffix-md", model.KBTagRuleMatchSuffix, "md", tagB.ID, 10, 1)

	// ① 导入：换出事务内求值 → 4 条命中落文档级 link
	res, err := f.svc.RunIngest(ctx, ingest.IngestParams{DocumentID: docA}, nil)
	if err != nil {
		t.Fatalf("导入失败: %v", err)
	}
	if res.State != "ready" || res.RuleTags != 4 {
		t.Fatalf("导入回执不符: state=%s rule_tags=%d", res.State, res.RuleTags)
	}
	for _, tag := range []*model.KBTag{tagGlob, tagRegex, tagSuffix, tagMeta} {
		if n := grpA.linkCount(tag.ID); n != 1 {
			t.Fatalf("命中标签 %d 应恰 1 条 link: %d", tag.ID, n)
		}
		if objs := grpA.linkObjects(tag.ID, model.KBTagObjectDocument); len(objs) != 1 || objs[0] != docA {
			t.Fatalf("规则 link 应锚文档 %d（文档级）: %+v", docA, objs)
		}
	}
	// 反例 / enabled=0 / 停用标签 / 缺失标签 / 跨库标签：零行
	if n := grpA.linkCount(tagNoMatch.ID); n != 0 {
		t.Fatalf("反例（suffix pdf）不应命中: %d", n)
	}
	if n := grpA.linkCount(tagRuleOff.ID); n != 0 {
		t.Fatalf("enabled=0 规则不应参与求值: %d", n)
	}
	if n := grpA.linkCount(tagOff.ID); n != 0 {
		t.Fatalf("停用标签不应被规则落行: %d", n)
	}
	if n := grpB.linkCount(tagB.ID); n != 0 {
		t.Fatalf("库 B 标签不应被库 A 文档命中: %d", n)
	}

	// ② 审计：恰 1 行 tag_add（system）+ event=rule + rule_ids 按 (priority,id) 序 + 跳过计数
	details := f.ruleAuditDetails(t, libA, docA)
	if len(details) != 1 {
		t.Fatalf("规则打标应恰 1 行审计（真变才写）: %d", len(details))
	}
	if details[0]["event"] != "rule" || details[0]["object_type"] != model.KBTagObjectDocument {
		t.Fatalf("审计明细口径不符: %+v", details[0])
	}
	wantRules := []any{float64(rGlob.ID), float64(rRegex.ID), float64(rSuffix.ID), float64(rMeta.ID)}
	gotRules, _ := details[0]["rule_ids"].([]any)
	if len(gotRules) != len(wantRules) {
		t.Fatalf("审计 rule_ids 条数不符: %+v", details[0]["rule_ids"])
	}
	for i := range wantRules {
		if gotRules[i] != wantRules[i] {
			t.Fatalf("审计 rule_ids 应按 (priority,id) 序: got=%+v", gotRules)
		}
	}
	if details[0]["bound"] != float64(4) || details[0]["skipped_rules"] != float64(3) {
		t.Fatalf("审计读数不符: %+v", details[0])
	}

	// ③ 跳过项回显（响亮：停用 / 不存在 / 跨库三条）
	if !warnContains(res.Warnings, "已停用") || !warnContains(res.Warnings, "跨库引用") ||
		!warnContains(res.Warnings, "不存在或已删") {
		t.Fatalf("跳过项应回显告警: %+v", res.Warnings)
	}

	// ④ 应用面重跑（同规则集）：零新行、零重复留痕（ON CONFLICT 幂等）
	bound, notes := f.applyInTx(t, f.reloadDoc(t))
	if bound != 0 {
		t.Fatalf("重跑应幂等命中（零新行）: %d", bound)
	}
	if len(notes) != 3 {
		t.Fatalf("重跑跳过项应稳定 3 条: %+v", notes)
	}
	if n := f.auditCount(libA, model.KBAuditActionTagAdd, model.KBActorKindSystem, 0); n != 1 {
		t.Fatalf("重跑不得重复留痕: %d", n)
	}

	// ⑤ 同 build 重跑导入（零变更路径）：link / 审计不变（重跑同结果）
	res2, err := f.svc.RunIngest(ctx, ingest.IngestParams{DocumentID: docA}, nil)
	if err != nil {
		t.Fatalf("同 build 重跑失败: %v", err)
	}
	if res2.State != "ready" || res2.ChunkRows != 0 {
		t.Fatalf("重跑应零变更 ready: state=%s rows=%d", res2.State, res2.ChunkRows)
	}
	if res2.RuleTags != 0 {
		t.Fatalf("零变更重跑应零新行（幂等）: %d", res2.RuleTags)
	}
	for _, tag := range []*model.KBTag{tagGlob, tagRegex, tagSuffix, tagMeta} {
		if n := grpA.linkCount(tag.ID); n != 1 {
			t.Fatalf("重跑后 link 行数应不变: tag=%d n=%d", tag.ID, n)
		}
	}
	if len(f.ruleAuditDetails(t, libA, docA)) != 1 {
		t.Fatal("重跑不得新增规则审计行")
	}

	// ⑥ 新增规则后再重跑（零变更路径的收敛面）：只补新增 1 条，既有行不重复、不重复留痕
	tagLate := grpA.tag("kb-rule-late-"+f.suffix, 1)
	rules.rule(libA, "late-suffix-md", model.KBTagRuleMatchSuffix, "md", tagLate.ID, 100, 1)
	res3, err := f.svc.RunIngest(ctx, ingest.IngestParams{DocumentID: docA}, nil)
	if err != nil {
		t.Fatalf("新增规则后重跑失败: %v", err)
	}
	if res3.State != "ready" || res3.RuleTags != 1 {
		t.Fatalf("重跑应只补新增规则 1 条: state=%s rule_tags=%d", res3.State, res3.RuleTags)
	}
	if n := grpA.linkCount(tagLate.ID); n != 1 {
		t.Fatalf("新增规则应落 1 条 link: %d", n)
	}
	for _, tag := range []*model.KBTag{tagGlob, tagRegex, tagSuffix, tagMeta} {
		if n := grpA.linkCount(tag.ID); n != 1 {
			t.Fatalf("既有命中不得重复落行: tag=%d n=%d", tag.ID, n)
		}
	}
	if got := f.ruleAuditDetails(t, libA, docA); len(got) != 2 {
		t.Fatalf("收敛应恰再留痕 1 行: %d", len(got))
	}

	// ⑦ 跨库规则隔离：库 B 文档只命中库 B 规则；库 B 规则永不作用于库 A 文档
	if b, _ := f.applyInTx(t, docB); b != 1 {
		t.Fatalf("库 B 文档应命中库 B 规则 1 条: %d", b)
	}
	if n := grpB.linkCount(tagB.ID); n != 1 {
		t.Fatalf("库 B 标签应恰 1 条 link: %d", n)
	}
	if objs := grpB.linkObjects(tagB.ID, model.KBTagObjectDocument); len(objs) != 1 || objs[0] != docB.ID {
		t.Fatalf("库 B link 应锚库 B 文档: %+v", objs)
	}
	// 库 A 文档复跑仍零新行（库 B 规则与跨库标签规则均零命中）
	if b, _ := f.applyInTx(t, f.reloadDoc(t)); b != 0 {
		t.Fatalf("库 A 文档复跑不应有新行（跨库规则不得落行）: %d", b)
	}
	if n := grpB.linkCount(tagB.ID); n != 1 {
		t.Fatalf("库 A 复跑不得动库 B 关联: %d", n)
	}
}
