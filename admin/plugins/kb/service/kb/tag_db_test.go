// plugin: kb v1.0.0
package kb

// 标签核心连库用例：
//   - 绑定幂等（ON CONFLICT 无重复行；真变才落审计）；
//   - 跨库标签引用拒绝（库作用域标签不可用于其它库；平台标签跨库可用；禁用/不存在标签拒；
//     对象侧无权 = ErrDenied 同型）；
//   - 重切后切片级标签重放（命中重挂 / 失配丢弃 + 审计；文档级 link 不受影响；purge 清对象侧关联）；
//   - 候选未确认不落 link（kb:tag 缺省关 + 注入候选源开档：候选只回显、词表与关联都不写）。
//
// 门控：PLUGIN_KB_DB=1（同 //；未设时 DB 用例一律 SKIP——不得当作通过）。
// 跑法：PLUGIN_KB_DB=1 go test -count=1 -timeout=120s ./plugins/kb/service/kb/...
import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/ai-go-hub/ai-go-admin/plugins/kb/model"
	"github.com/ai-go-hub/ai-go-admin/plugins/kb/service/kb/aiport"
	"github.com/ai-go-hub/ai-go-admin/plugins/kb/service/kb/chunk"
	"github.com/ai-go-hub/ai-go-admin/plugins/kb/service/kb/ingest"
	kbTag "github.com/ai-go-hub/ai-go-admin/plugins/kb/service/kb/tag"

	"gorm.io/gorm"
)

// ==================== 标签词表夹具（kbFixture 清理面不含标签四表——本夹具自清） ====================

type tagRowsFixture struct {
	t   *testing.T
	ctx context.Context
	db  *gorm.DB

	groupIDs []uint
	tagIDs   []uint
}

// newTagRows 建标签组（scope/library 由调用方给出；平台组 libraryID=0）+ 可选标签；
// 清理注册在 kbFixture 之后（LIFO：本夹具先清，先 link 后 tag 后 group）。
func newTagRows(t *testing.T, f *kbFixture, scope string, libraryID uint, groupName string) *tagRowsFixture {
	t.Helper()
	g := &tagRowsFixture{t: t, ctx: f.ctx, db: f.db}
	grp := &model.KBTagGroup{Scope: scope, LibraryID: libraryID, Name: groupName}
	if err := gorm.G[model.KBTagGroup](f.db).Create(g.ctx, grp); err != nil {
		t.Fatalf("建标签组失败: %v", err)
	}
	g.groupIDs = append(g.groupIDs, grp.ID)
	t.Cleanup(g.cleanup)
	return g
}

// tag 建标签（status：0=禁用，1=启用）
func (g *tagRowsFixture) tag(name string, status uint8) *model.KBTag {
	g.t.Helper()
	row := &model.KBTag{GroupID: g.groupIDs[0], Name: name, Status: &status}
	if err := gorm.G[model.KBTag](g.db).Create(g.ctx, row); err != nil {
		g.t.Fatalf("建标签失败: %v", err)
	}
	g.tagIDs = append(g.tagIDs, row.ID)
	return row
}

// cleanup 硬删本夹具的关联/标签/组（关联按 tag_id 面清——link 行 id 不入登记：随标签一并清）
func (g *tagRowsFixture) cleanup() {
	if len(g.tagIDs) > 0 {
		if err := g.db.Exec("DELETE FROM kb_tag_link WHERE tag_id IN ?", g.tagIDs).Error; err != nil {
			g.t.Logf("清理标签关联失败（请手工核）: %v", err)
		}
	}
	if len(g.tagIDs) > 0 {
		if err := g.db.Exec("DELETE FROM kb_tag WHERE id IN ?", g.tagIDs).Error; err != nil {
			g.t.Logf("清理标签失败（请手工核）: %v", err)
		}
	}
	if len(g.groupIDs) > 0 {
		if err := g.db.Exec("DELETE FROM kb_tag_group WHERE id IN ?", g.groupIDs).Error; err != nil {
			g.t.Logf("清理标签组失败（请手工核）: %v", err)
		}
	}
}

// linkCount 某标签的活跃关联行数
func (g *tagRowsFixture) linkCount(tagID uint) int64 {
	g.t.Helper()
	var n int64
	if err := g.db.Model(&model.KBTagLink{}).Where("tag_id = ?", tagID).Count(&n).Error; err != nil {
		g.t.Fatalf("统计标签关联失败: %v", err)
	}
	return n
}

// linkObjects 某标签的关联对象 id 集（object_type 过滤）
func (g *tagRowsFixture) linkObjects(tagID uint, objectType string) []uint {
	g.t.Helper()
	ids := make([]uint, 0, 2)
	if err := g.db.Model(&model.KBTagLink{}).
		Where("tag_id = ? AND object_type = ?", tagID, objectType).
		Order("id ASC").Pluck("object_id", &ids).Error; err != nil {
		g.t.Fatalf("读标签关联对象失败: %v", err)
	}
	return ids
}

// ==================== ① 绑定幂等 + 解绑 + 对象标签读取 ====================

// TestTagBindIdempotentDB 绑定幂等（ON CONFLICT 无重复行）+ 重复请求不重复留痕 +
// 解绑硬删 + tag_remove 审计 + ListObjectTags 读取面。
func TestTagBindIdempotentDB(t *testing.T) {
	f := newKBFixture(t)
	ctx := f.ctx
	admin := Actor{Kind: model.KBActorKindAdmin, ID: f.adminID}

	lib := f.library(model.KBLibraryOwnerKindSystem, 0, "kb-tag-lib-"+f.suffix, 1)
	doc := f.document(lib.ID, "kb-tag-doc-"+f.suffix)

	grp := newTagRows(t, f, model.KBTagScopePlatform, 0, "kb-tag-grp-"+f.suffix)
	tagA := grp.tag("kb-tag-tag-a-"+f.suffix, 1)
	tagB := grp.tag("kb-tag-tag-b-"+f.suffix, 1)

	// ① 首绑：去重后 2 条新行（重复 id 折叠）
	res, err := f.svc.BindTags(ctx, admin, model.KBTagObjectDocument, doc.ID, []uint{tagA.ID, tagB.ID, tagA.ID})
	if err != nil {
		t.Fatalf("绑定失败: %v", err)
	}
	if res.Bound != 2 || res.Skipped != 0 || res.LibraryID != lib.ID || res.DocumentID != doc.ID {
		t.Fatalf("首绑回执不符: %+v", res)
	}
	if n := grp.linkCount(tagA.ID) + grp.linkCount(tagB.ID); n != 2 {
		t.Fatalf("首绑行数不符: %d", n)
	}

	// ② 重绑同集：幂等（ON CONFLICT DO NOTHING ⇒ 无重复行、零新行、零重复留痕）
	res2, err := f.svc.BindTags(ctx, admin, model.KBTagObjectDocument, doc.ID, []uint{tagA.ID, tagB.ID})
	if err != nil {
		t.Fatalf("重绑失败: %v", err)
	}
	if res2.Bound != 0 || res2.Skipped != 2 {
		t.Fatalf("重绑应全跳过: %+v", res2)
	}
	if n := grp.linkCount(tagA.ID) + grp.linkCount(tagB.ID); n != 2 {
		t.Fatalf("幂等破坏（出现重复行）: %d", n)
	}
	if n := f.auditCount(lib.ID, model.KBAuditActionTagAdd, model.KBActorKindAdmin, f.adminID); n != 1 {
		t.Fatalf("tag_add 审计应只 1 行（真变才写）: %d", n)
	}

	// ③ 对象标签读取面（读谓词门；按 link 序）
	tags, err := f.svc.ListObjectTags(ctx, mustAdminView(t, f), model.KBTagObjectDocument, doc.ID)
	if err != nil {
		t.Fatalf("读对象标签失败: %v", err)
	}
	if len(tags) != 2 || tags[0].TagName == "" || tags[0].Scope != model.KBTagScopePlatform {
		t.Fatalf("对象标签读取不符: %+v", tags)
	}

	// ④ 解绑：硬删行 + tag_remove 审计；重复解绑无真变、不重复留痕
	res3, err := f.svc.UnbindTags(ctx, admin, model.KBTagObjectDocument, doc.ID, []uint{tagA.ID})
	if err != nil {
		t.Fatalf("解绑失败: %v", err)
	}
	if res3.Removed != 1 || grp.linkCount(tagA.ID) != 0 {
		t.Fatalf("解绑未硬删行: %+v", res3)
	}
	if n := f.auditCount(lib.ID, model.KBAuditActionTagRemove, model.KBActorKindAdmin, f.adminID); n != 1 {
		t.Fatalf("tag_remove 审计应 1 行: %d", n)
	}
	res4, err := f.svc.UnbindTags(ctx, admin, model.KBTagObjectDocument, doc.ID, []uint{tagA.ID})
	if err != nil {
		t.Fatalf("重复解绑失败: %v", err)
	}
	if res4.Removed != 0 {
		t.Fatalf("重复解绑应零行: %+v", res4)
	}
	if n := f.auditCount(lib.ID, model.KBAuditActionTagRemove, model.KBActorKindAdmin, f.adminID); n != 1 {
		t.Fatalf("重复解绑不应新留痕: %d", n)
	}

	// ⑤ 入参门：对象类型闭集 / id 必填 / 空标签集
	if _, err := f.svc.BindTags(ctx, admin, "library", doc.ID, []uint{tagB.ID}); err == nil {
		t.Fatal("未登记对象类型应拒")
	}
	if _, err := f.svc.BindTags(ctx, admin, model.KBTagObjectDocument, 0, []uint{tagB.ID}); err == nil {
		t.Fatal("对象 id=0 应拒")
	}
	if _, err := f.svc.BindTags(ctx, admin, model.KBTagObjectDocument, doc.ID, nil); err == nil {
		t.Fatal("空标签集应拒")
	}
}

// mustAdminView admin 轴读视图（用例自持合成 id；节点授权是 HTTP 侧前置）
func mustAdminView(t *testing.T, f *kbFixture) *ReadView {
	t.Helper()
	view, err := f.svc.AdminView(f.ctx, f.adminID)
	if err != nil {
		t.Fatalf("admin 视图解析失败: %v", err)
	}
	return view
}

// ==================== ② 跨库标签引用拒绝 ====================

// TestTagCrossLibraryRejectDB 词表作用域执法：库作用域标签只可用于本库对象（跨库引用响亮拒）；
// 平台标签跨库可用；禁用/不存在标签拒；对象侧无权 = ErrDenied 同型（fail-closed）。
func TestTagCrossLibraryRejectDB(t *testing.T) {
	f := newKBFixture(t)
	ctx := f.ctx
	admin := Actor{Kind: model.KBActorKindAdmin, ID: f.adminID}

	uOwner := f.user("kb-tag-owner-"+f.suffix, "enable", 0)
	uOther := f.user("kb-tag-other-"+f.suffix, "enable", 0)
	libA := f.library(model.KBLibraryOwnerKindUser, uOwner.ID, "kb-tag-libA-"+f.suffix, 1)
	libB := f.library(model.KBLibraryOwnerKindUser, uOwner.ID, "kb-tag-libB-"+f.suffix, 1)
	docB := f.document(libB.ID, "kb-tag-docB-"+f.suffix)

	grpA := newTagRows(t, f, model.KBTagScopeLibrary, libA.ID, "kb-tag-grpA-"+f.suffix)
	tagA := grpA.tag("kb-tag-tagA-"+f.suffix, 1)
	grpB := newTagRows(t, f, model.KBTagScopeLibrary, libB.ID, "kb-tag-grpB-"+f.suffix)
	tagB := grpB.tag("kb-tag-tagB-"+f.suffix, 1)
	tagOff := grpB.tag("kb-tag-tagB-off-"+f.suffix, 0)
	grpP := newTagRows(t, f, model.KBTagScopePlatform, 0, "kb-tag-grpP-"+f.suffix)
	tagP := grpP.tag("kb-tag-tagP-"+f.suffix, 1)

	// ① 跨库引用：libA 的库标签挂到 libB 的文档 ⇒ 响亮拒（校验错，不是 ErrDenied 族），零行零审计
	_, err := f.svc.BindTags(ctx, admin, model.KBTagObjectDocument, docB.ID, []uint{tagA.ID})
	if err == nil {
		t.Fatal("跨库标签引用应拒")
	}
	if errors.Is(err, ErrDenied) {
		t.Fatalf("跨库引用是校验错，不应并入 ErrDenied 族: %v", err)
	}
	if !strings.Contains(err.Error(), "跨库") {
		t.Fatalf("拒绝原因应点名跨库: %v", err)
	}
	if grpA.linkCount(tagA.ID) != 0 {
		t.Fatal("跨库引用拒绝后不得落行")
	}
	if n := f.auditCount(libB.ID, model.KBAuditActionTagAdd, "", 0); n != 0 {
		t.Fatalf("拒绝路径不得留 tag_add 审计: %d", n)
	}

	// ② 本库标签 + 平台标签：可用
	if _, err := f.svc.BindTags(ctx, admin, model.KBTagObjectDocument, docB.ID, []uint{tagB.ID, tagP.ID}); err != nil {
		t.Fatalf("本库/平台标签应可绑: %v", err)
	}
	if grpB.linkCount(tagB.ID) != 1 || grpP.linkCount(tagP.ID) != 1 {
		t.Fatal("绑定未落行")
	}

	// ③ 禁用标签 / 不存在标签：拒
	if _, err := f.svc.BindTags(ctx, admin, model.KBTagObjectDocument, docB.ID, []uint{tagOff.ID}); err == nil {
		t.Fatal("禁用标签应拒")
	}
	if _, err := f.svc.BindTags(ctx, admin, model.KBTagObjectDocument, docB.ID, []uint{987654321}); err == nil {
		t.Fatal("不存在标签应拒")
	}

	// ④ 对象侧无权（user 轴无 grant）⇒ ErrDenied 同型；不存在的对象亦然
	_, err = f.svc.BindTags(ctx, Actor{Kind: model.KBActorKindUser, ID: uOther.ID},
		model.KBTagObjectDocument, docB.ID, []uint{tagP.ID})
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("无写权对象应同型拒: %v", err)
	}
	if _, err := f.svc.BindTags(ctx, Actor{Kind: model.KBActorKindUser, ID: uOther.ID},
		model.KBTagObjectDocument, 987654321, []uint{tagP.ID}); !errors.Is(err, ErrDenied) {
		t.Fatalf("不存在对象应同型拒: %v", err)
	}
	// 属主（user 轴 owner）可绑自库对象
	if _, err := f.svc.BindTags(ctx, Actor{Kind: model.KBActorKindUser, ID: uOwner.ID},
		model.KBTagObjectDocument, docB.ID, []uint{tagP.ID}); err != nil {
		t.Fatalf("属主应可绑: %v", err)
	}
	// actor_id=0 短路拒（入口守卫）
	if _, err := f.svc.BindTags(ctx, Actor{Kind: model.KBActorKindAdmin, ID: 0},
		model.KBTagObjectDocument, docB.ID, []uint{tagP.ID}); !errors.Is(err, ErrInvalidActor) {
		t.Fatalf("actor_id=0 应短路拒: %v", err)
	}
}

// ==================== ③ 重切后切片级标签重放 ====================

// TestChunkTagReplayDB 换出 × 标签重放（重放规则）：
//   - 切片级 link 随旧 build 删除后按 (document_id, text_hash) 重放：命中 ⇒ 重挂到新 chunk（换 id）；
//   - 失配 ⇒ 丢弃 + 审计（tag_remove，detail.event=replay_drop）；
//   - 文档级 link 不受重切影响；
//   - 导入审计带重放读数；purge 清对象侧全部关联（document + chunk 两路）。
//
// 序列（关键：重放键的「命中面」必须在换出前可构造——旧行 text_hash 取新 build 切片行之一）：
// 首轮全链 ready → 换档写新 build 切片（不换出）→ 造 2 行合成旧块（一命中/一失配）并挂切片级标签
// → 再跑全链换出（重放发生）→ 断言。
func TestChunkTagReplayDB(t *testing.T) {
	f := newPipelineFixture(t, pipelineFixtureMd)
	ctx := f.ctx
	admin := Actor{Kind: model.KBActorKindAdmin, ID: f.adminID}

	// ① 首轮全链（kb 包 TestMain 关嵌入 ⇒ 只切不嵌）→ ready + active_build = build1
	if _, err := f.svc.RunIngest(ctx, ingest.IngestParams{DocumentID: f.docID}, nil); err != nil {
		t.Fatalf("首轮导入失败: %v", err)
	}
	doc1 := f.reloadDoc(t)
	oldBuild := strings.TrimSpace(doc1.ActiveBuild)
	if doc1.BuildState != model.KBDocumentBuildReady || oldBuild == "" {
		t.Fatalf("首轮应 ready 且换出: state=%s build=%q", doc1.BuildState, oldBuild)
	}

	// ② 换档（新 build 切片行先落位，不换出）——取新 build 首块 text_hash 作「命中面」重放键
	if err := f.db.Model(&model.KBDocument{}).Where("id = ?", doc1.ID).
		Update("chunk_config", `{"target":128,"overlap":16}`).Error; err != nil {
		t.Fatalf("写文档级配置失败: %v", err)
	}
	plan2, _, _ := f.runToChunks(t, f.reloadDoc(t))
	if plan2.BuildHash == oldBuild {
		t.Fatal("换档后 build_hash 应变化")
	}
	newChunks := f.chunkRows(t, plan2.BuildHash)
	if len(newChunks) == 0 {
		t.Fatal("新 build 无切片行")
	}
	hitHash := newChunks[0].TextHash

	// ③ 合成 2 行旧块：一命中（text_hash = 新 build 某块）/ 一失配（凭空 hash）——都随换出被删
	missHash := chunk.TextHash("tagRes-miss-" + f.suffix)
	hitChunkID := f.insertSynthChunk(t, oldBuild, 900, "合成旧块-命中-"+f.suffix, hitHash)
	missChunkID := f.insertSynthChunk(t, oldBuild, 901, "合成旧块-失配-"+f.suffix, missHash)

	// ④ 挂标：切片级（命中面 + 失配面）+ 文档级
	grp := newTagRows(t, f.kbFixture, model.KBTagScopePlatform, 0, "kb-tag-replay-grp-"+f.suffix)
	tagRelink := grp.tag("kb-tag-relink-"+f.suffix, 1)
	tagDrop := grp.tag("kb-tag-drop-"+f.suffix, 1)
	tagDocLevel := grp.tag("kb-tag-doclevel-"+f.suffix, 1)
	if _, err := f.svc.BindTags(ctx, admin, model.KBTagObjectChunk, hitChunkID, []uint{tagRelink.ID}); err != nil {
		t.Fatalf("绑切片级标签（命中面）失败: %v", err)
	}
	if _, err := f.svc.BindTags(ctx, admin, model.KBTagObjectChunk, missChunkID, []uint{tagDrop.ID}); err != nil {
		t.Fatalf("绑切片级标签（失配面）失败: %v", err)
	}
	if _, err := f.svc.BindTags(ctx, admin, model.KBTagObjectDocument, f.docID, []uint{tagDocLevel.ID}); err != nil {
		t.Fatalf("绑文档级标签失败: %v", err)
	}
	var oldLinkIDs []uint
	if err := f.db.Model(&model.KBTagLink{}).Where("tag_id = ? AND object_type = ?", tagRelink.ID, model.KBTagObjectChunk).
		Pluck("id", &oldLinkIDs).Error; err != nil || len(oldLinkIDs) != 1 {
		t.Fatalf("读旧关联失败: %v（%v）", err, oldLinkIDs)
	}

	// ⑤ 再跑全链：同档 ⇒ 零新块，触发换出（active_build: build1 → build2），重放随换出事务发生
	res, err := f.svc.RunIngest(ctx, ingest.IngestParams{DocumentID: f.docID}, nil)
	if err != nil {
		t.Fatalf("换出重跑失败: %v", err)
	}
	if res.State != "ready" || strings.TrimSpace(res.BuildHash) != plan2.BuildHash {
		t.Fatalf("换出回执不符: state=%s build=%s", res.State, res.BuildHash)
	}
	doc2 := f.reloadDoc(t)
	if strings.TrimSpace(doc2.ActiveBuild) != plan2.BuildHash {
		t.Fatalf("active_build 未换出: %q", doc2.ActiveBuild)
	}

	// ⑥ 命中 ⇒ 重挂：关联行指向新 build 的同 text_hash 切片；旧行（id）已删
	objs := grp.linkObjects(tagRelink.ID, model.KBTagObjectChunk)
	if len(objs) != 1 {
		t.Fatalf("命中面应恰 1 行关联: %v", objs)
	}
	if objs[0] == hitChunkID {
		t.Fatalf("重挂未换 id（仍指旧块）: %d", objs[0])
	}
	var target model.KBChunk
	if err := f.db.Where("id = ?", objs[0]).First(&target).Error; err != nil {
		t.Fatalf("读重挂目标切片失败: %v", err)
	}
	if target.DocumentID != f.docID || target.BuildHash != plan2.BuildHash || target.TextHash != hitHash {
		t.Fatalf("重挂目标不符 (document_id, text_hash) 口径: %+v", target)
	}
	var oldLeft int64
	if err := f.db.Model(&model.KBTagLink{}).Where("id IN ?", oldLinkIDs).Count(&oldLeft).Error; err != nil || oldLeft != 0 {
		t.Fatalf("旧关联行应删（重挂=删旧行+插新行）: %d err=%v", oldLeft, err)
	}

	// ⑦ 失配 ⇒ 丢弃 + 审计（tag_remove；detail.event=replay_drop）
	if n := grp.linkCount(tagDrop.ID); n != 0 {
		t.Fatalf("失配关联应丢弃: %d", n)
	}
	var drops int64
	if err := f.db.Model(&model.KBAuditLog{}).
		Where("document_id = ? AND action = ?", f.docID, model.KBAuditActionTagRemove).Count(&drops).Error; err != nil {
		t.Fatalf("统计丢弃审计失败: %v", err)
	}
	if drops != 1 {
		t.Fatalf("失配丢弃应落 1 行审计: %d", drops)
	}
	var detail string
	if err := f.db.Raw("SELECT detail::text FROM kb_audit_log WHERE document_id = ? AND action = ? LIMIT 1",
		f.docID, model.KBAuditActionTagRemove).Scan(&detail).Error; err != nil {
		t.Fatalf("读丢弃审计明细失败: %v", err)
	}
	if !strings.Contains(detail, "replay_drop") || !strings.Contains(detail, missHash) {
		t.Fatalf("丢弃审计明细应带 replay_drop 与 text_hash: %s", detail)
	}

	// ⑧ 文档级 link 不受重切影响（同 id、同对象）；合成旧块已随换出删除
	docLevel := grp.linkObjects(tagDocLevel.ID, model.KBTagObjectDocument)
	if len(docLevel) != 1 || docLevel[0] != f.docID {
		t.Fatalf("文档级关联不应受重切影响: %v", docLevel)
	}
	var synthLeft int64
	if err := f.db.Model(&model.KBChunk{}).Where("id IN ?", []uint{hitChunkID, missChunkID}).Count(&synthLeft).Error; err != nil || synthLeft != 0 {
		t.Fatalf("合成旧块应随换出删除: %d err=%v", synthLeft, err)
	}

	// ⑨ 导入审计带重放读数
	var importDetail string
	if err := f.db.Raw("SELECT detail::text FROM kb_audit_log WHERE document_id = ? AND action = ? ORDER BY id DESC LIMIT 1",
		f.docID, model.KBAuditActionImport).Scan(&importDetail).Error; err != nil {
		t.Fatalf("读导入审计失败: %v", err)
	}
	if !strings.Contains(importDetail, "tag_relinked") || !strings.Contains(importDetail, "tag_dropped") {
		t.Fatalf("导入审计应带重放读数: %s", importDetail)
	}

	// ⑩ purge 清对象侧关联（document + chunk 两路）：软删 → 清理 → 关联空集
	if err := f.svc.DeleteDocument(ctx, admin, f.docID); err != nil {
		t.Fatalf("软删文档失败: %v", err)
	}
	if err := f.svc.RunPurge(ctx, 0, f.docID); err != nil {
		t.Fatalf("清理失败: %v", err)
	}
	for _, id := range []uint{tagRelink.ID, tagDrop.ID, tagDocLevel.ID} {
		if n := grp.linkCount(id); n != 0 {
			t.Fatalf("清理后标签 %d 仍有 %d 行关联", id, n)
		}
	}
}

// insertSynthChunk 插入合成切片行（文档的「旧 build」面：换档后人工造失配/命中锚；返回 id）
func (f *pipelineFixture) insertSynthChunk(t *testing.T, build string, seq int, text, textHash string) uint {
	t.Helper()
	var id int64
	if err := f.db.Raw("INSERT INTO kb_chunk (library_id, document_id, build_hash, config_hash, chunker,"+
		" chunker_version, seq, locator, char_start, char_end, text, text_hash, created_at)"+
		" VALUES (?,?,?,?,?,?,?,?,?,?,?,?,NOW()) RETURNING id",
		f.libID, f.docID, build, strings.Repeat("c", 40), "fixed", "v1", seq,
		fmt.Sprintf("kb:doc:%d#p%d", f.docID, seq), 0, len([]rune(text)), text, textHash,
	).Scan(&id).Error; err != nil {
		t.Fatalf("插入合成切片失败: %v", err)
	}
	return uint(id)
}

// ==================== ④ 候选未确认不落 link ====================

// TestTagCandidatesNoLinkDB 词表污染防护（候选不落 kb_tag_link、词表零新增）：
//   - 缺省关档：作业收工 state=disabled，不调模型、不产候选、零写；
//   - 注入候选源开档：候选照出（含命中词表与未入词表两类），但 **kb_tag_link 零行、词表零新增**——
//     「人工确认后落标」的确认面 = 绑定服务（未确认候选绝不入过滤面）。
func TestTagCandidatesNoLinkDB(t *testing.T) {
	f := newPipelineFixture(t, pipelineFixtureMd)
	ctx := f.ctx

	if _, err := f.svc.RunIngest(ctx, ingest.IngestParams{DocumentID: f.docID}, nil); err != nil {
		t.Fatalf("导入失败: %v", err)
	}
	if got := f.reloadDoc(t); got.BuildState != model.KBDocumentBuildReady {
		t.Fatalf("文档应 ready: %s", got.BuildState)
	}

	grp := newTagRows(t, f.kbFixture, model.KBTagScopePlatform, 0, "kb-tag-cand-grp-"+f.suffix)
	vocabTag := grp.tag("候选命中-"+f.suffix, 1)

	// ① 缺省关档（enabled nil ⇒ 缺省读 kb.tag.model_enabled，测试环境未配 = 缺省 false）
	svcOff := kbTag.NewServiceWith(f.db, nil, nil)
	resOff, err := svcOff.RunCandidates(ctx, f.docID)
	if err != nil {
		t.Fatalf("关档运行失败: %v", err)
	}
	if resOff.State != "disabled" || len(resOff.Candidates) != 0 {
		t.Fatalf("关档回执不符: %+v", resOff)
	}

	// ② 开档 + 注入候选源（假候选：一条命中词表 / 一条未入词表）
	gen := func(context.Context, aiport.CallMeta, string) ([]kbTag.Candidate, string, error) {
		return []kbTag.Candidate{
			{Tag: "候选命中-" + f.suffix, Confidence: 0.9},
			{Tag: "候选未入词表-" + f.suffix, Confidence: 0.5},
		}, "fake-model", nil
	}
	svcOn := kbTag.NewServiceWith(f.db, gen, func() bool { return true })
	res, err := svcOn.RunCandidates(ctx, f.docID)
	if err != nil {
		t.Fatalf("开档运行失败: %v", err)
	}
	if res.State != "candidates" || len(res.Candidates) != 2 || res.Model != "fake-model" {
		t.Fatalf("候选回执不符: %+v", res)
	}
	hit, miss := 0, 0
	for _, c := range res.Candidates {
		if c.InVocabulary {
			hit++
			if c.TagID != vocabTag.ID {
				t.Fatalf("命中项应带词表 id=%d: %+v", vocabTag.ID, c)
			}
		} else {
			miss++
		}
	}
	if hit != 1 || miss != 1 {
		t.Fatalf("词表命中面不符（hit=%d miss=%d）: %+v", hit, miss, res.Candidates)
	}
	if res.Chars == 0 || res.Vocabulary < 1 {
		t.Fatalf("回执缺材料/词表读数: %+v", res)
	}

	// ③ 关键词表污染防护：候选零落标（kb_tag_link 零行、词表零新增）
	if n := grp.linkCount(vocabTag.ID); n != 0 {
		t.Fatalf("未确认候选不得落 link: %d", n)
	}
	var allLinks int64
	if err := f.db.Model(&model.KBTagLink{}).Count(&allLinks).Error; err != nil {
		t.Fatalf("统计关联失败: %v", err)
	}
	if allLinks != 0 {
		t.Fatalf("候选作业不应有任何 link 行: %d", allLinks)
	}
	var tagTotal int64
	if err := f.db.Model(&model.KBTag{}).Where("group_id = ?", grp.groupIDs[0]).Count(&tagTotal).Error; err != nil {
		t.Fatalf("统计词表失败: %v", err)
	}
	if tagTotal != 1 {
		t.Fatalf("候选不得写词表（应仍 1 条）: %d", tagTotal)
	}
}
