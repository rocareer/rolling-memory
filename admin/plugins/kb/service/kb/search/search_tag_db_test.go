// plugin: kb v1.0.0
package search

// 标签过滤下推连库用例（T5）：可读域内按标签收窄召回——切片级 link 命中其自身块、
// 文档级 link 命中文档全部块、多标签 = OR、无关/不存在标签 = 空集；并钉「两段子查询进同一条件」
// 的 SQL 形态（先于 LIMIT 截断，禁应用层后过滤）。
//
// 门控：PLUGIN_KB_DB=1（同前门禁；未设时 SKIP）。跑法：PLUGIN_KB_DB=1 go test -count=1 -timeout=120s ./plugins/kb/service/kb/search/...

import (
	"fmt"
	"strings"
	"testing"

	"github.com/ai-go-hub/ai-go-admin/plugins/kb/model"
	repoKB "github.com/ai-go-hub/ai-go-admin/plugins/kb/repository/kb"
	svcKB "github.com/ai-go-hub/ai-go-admin/plugins/kb/service/kb"

	"gorm.io/gorm"
)

// tagRows 标签造数（raw；searchFixture 清理面不含标签四表——本夹具自清：link → tag → group）
type tagRows struct {
	t  *testing.T
	db *gorm.DB

	groupID uint
	tagIDs  []uint
}

func newTagRows(t *testing.T, f *searchFixture, groupName string) *tagRows {
	t.Helper()
	g := &tagRows{t: t, db: f.db}
	var id int64
	if err := f.db.Raw("INSERT INTO kb_tag_group (scope, library_id, name, weigh, created_at, updated_at)"+
		" VALUES (?, 0, ?, 0, NOW(), NOW()) RETURNING id", model.KBTagScopePlatform, groupName).Scan(&id).Error; err != nil {
		t.Fatalf("建标签组失败: %v", err)
	}
	g.groupID = uint(id)
	t.Cleanup(g.cleanup)
	return g
}

func (g *tagRows) tag(name string) uint {
	g.t.Helper()
	var id int64
	if err := g.db.Raw("INSERT INTO kb_tag (group_id, name, weigh, status, created_at, updated_at)"+
		" VALUES (?, ?, 0, 1, NOW(), NOW()) RETURNING id", g.groupID, name).Scan(&id).Error; err != nil {
		g.t.Fatalf("建标签失败: %v", err)
	}
	g.tagIDs = append(g.tagIDs, uint(id))
	return uint(id)
}

// link 直落关联行（绑定面另有单测；本用例只造过滤下推的输入面）
func (g *tagRows) link(tagID, objectID uint, objectType string) {
	g.t.Helper()
	if err := g.db.Exec("INSERT INTO kb_tag_link (tag_id, object_type, object_id, operator_id, created_at)"+
		" VALUES (?,?,?,0,NOW())", tagID, objectType, objectID).Error; err != nil {
		g.t.Fatalf("建标签关联失败: %v", err)
	}
}

func (g *tagRows) cleanup() {
	if len(g.tagIDs) > 0 {
		if err := g.db.Exec("DELETE FROM kb_tag_link WHERE tag_id IN ?", g.tagIDs).Error; err != nil {
			g.t.Logf("清理标签关联失败（请手工核）: %v", err)
		}
		if err := g.db.Exec("DELETE FROM kb_tag WHERE id IN ?", g.tagIDs).Error; err != nil {
			g.t.Logf("清理标签失败（请手工核）: %v", err)
		}
	}
	if g.groupID != 0 {
		if err := g.db.Exec("DELETE FROM kb_tag_group WHERE id = ?", g.groupID).Error; err != nil {
			g.t.Logf("清理标签组失败（请手工核）: %v", err)
		}
	}
}

// TestSearchTagFilterDB 标签过滤下推端到端（chunk 级 / document 级 / OR / 无关与不存在 / SQL 形态）。
func TestSearchTagFilterDB(t *testing.T) {
	f := newSearchFixture(t)
	token := "kbtagsearch" + f.suffix
	lib := f.library(model.KBLibraryOwnerKindSystem, 0, "kb-tagsearch-lib-"+f.suffix, 1)
	_, chunkA := f.readyDoc(lib.ID, "kb-tagsearch-docA-"+f.suffix, token)
	docB, chunkB := f.readyDoc(lib.ID, "kb-tagsearch-docB-"+f.suffix, token)

	rows := newTagRows(t, f, "kb-tagsearch-grp-"+f.suffix)
	tagChunk := rows.tag("kb-tagsearch-chunk-" + f.suffix) // 切片级：挂 chunkA
	tagDoc := rows.tag("kb-tagsearch-doc-" + f.suffix)     // 文档级：挂 docB（应命中其全部块）
	tagOther := rows.tag("kb-tagsearch-other-" + f.suffix) // 无关标签：无任何关联
	rows.link(tagChunk, chunkA.ID, model.KBTagObjectChunk)
	rows.link(tagDoc, docB.ID, model.KBTagObjectDocument)

	core := svcKB.NewService()
	cache := newQueryVecCache()
	cache.loaded = true
	capture := &sqlCaptureLogger{}
	session := f.db.Session(&gorm.Session{Logger: capture})
	svc := NewServiceWith(session, core, repoKB.NewStore().WithDB(session), cache)
	view, err := core.AdminView(f.ctx, f.adminID)
	if err != nil {
		t.Fatalf("admin 视图解析失败: %v", err)
	}

	search := func(tagIDs []uint) *Result {
		t.Helper()
		res, err := svc.Search(f.ctx, view, Options{Query: token, Mode: ModeLexical, TopK: 20, TagIDs: tagIDs})
		if err != nil {
			t.Fatalf("检索失败（tags=%v）: %v", tagIDs, err)
		}
		return res
	}

	// ① 基线：不过滤 → 两块都在（同一 token 命中两文档）
	base := itemChunkIDs(search(nil))
	if !base[chunkA.ID] || !base[chunkB.ID] {
		t.Fatalf("基线应命中两块: %v", base)
	}

	// ② 切片级标签：只留 chunkA（chunkB 的文档未挂该标签）
	onlyChunk := itemChunkIDs(search([]uint{tagChunk}))
	if !onlyChunk[chunkA.ID] || onlyChunk[chunkB.ID] {
		t.Fatalf("切片级标签过滤不符: %v", onlyChunk)
	}

	// ③ 文档级标签：命中 docB 的全部块（chunkB），不含 chunkA
	onlyDoc := itemChunkIDs(search([]uint{tagDoc}))
	if !onlyDoc[chunkB.ID] || onlyDoc[chunkA.ID] {
		t.Fatalf("文档级标签过滤不符（应命中其全部块）: %v", onlyDoc)
	}

	// ④ 多标签 = OR：两块都留
	both := itemChunkIDs(search([]uint{tagChunk, tagDoc}))
	if !both[chunkA.ID] || !both[chunkB.ID] {
		t.Fatalf("多标签应为 OR 语义: %v", both)
	}

	// ⑤ 无关标签 / 不存在标签：空集（不是错误）
	for _, ids := range [][]uint{{tagOther}, {987654321}} {
		if got := itemChunkIDs(search(ids)); len(got) != 0 {
			t.Fatalf("无关联标签应得空集（tags=%v）: %v", ids, got)
		}
	}

	// ⑤′ tags[] 回填（item.tags[]）：命中行标签名 = 切片级 link（chunkA 挂切片标签名）∪
	// 所属文档级 link（docB 挂文档标签名 ⇒ chunkB 行带上该名）
	nameChunk := "kb-tagsearch-chunk-" + f.suffix
	nameDoc := "kb-tagsearch-doc-" + f.suffix
	namesOf := map[uint][]string{}
	for _, it := range search(nil).Items {
		namesOf[it.ChunkID] = it.Tags
	}
	if got := namesOf[chunkA.ID]; len(got) != 1 || got[0] != nameChunk {
		t.Fatalf("chunkA 的 tags[] 应为 [%s]: %v", nameChunk, got)
	}
	if got := namesOf[chunkB.ID]; len(got) != 1 || got[0] != nameDoc {
		t.Fatalf("chunkB 的 tags[] 应为 [%s]（文档级 link 回填到其行）: %v", nameDoc, got)
	}

	// ⑥ SQL 形态：两段标签子查询拼进同一条件，且先于 LIMIT 截断（禁后过滤）
	sqls := capture.all()
	joined := strings.Join(sqls, "\n")
	if !strings.Contains(joined, "l1.object_type") || !strings.Contains(joined, "c.document_id IN (") {
		t.Fatalf("腿 SQL 未见标签两路子查询: %s", joined)
	}
	for i, sql := range sqls {
		tagIdx := strings.Index(sql, "l1.tag_id IN (")
		limitIdx := strings.Index(sql, "LIMIT")
		if tagIdx >= 0 && limitIdx >= 0 && tagIdx > limitIdx {
			t.Fatalf("第 %d 条腿 SQL 标签过滤未先于 LIMIT: %s", i, sql)
		}
	}
	t.Logf("标签过滤下推 SQL 捕获 %d 条（含两路子查询）", len(sqls))
	_ = fmt.Sprintf

	// ⑦ trgm 腿 × 标签（CJK 查询启用该腿）：全部腿带标签参数执行不降级 + 过滤生效
	cjkRes, err := svc.Search(f.ctx, view, Options{Query: "冒烟检索", Mode: ModeLexical, TopK: 20, TagIDs: []uint{tagChunk}})
	if err != nil {
		t.Fatalf("CJK 检索失败: %v", err)
	}
	if len(cjkRes.Degraded) != 0 {
		t.Fatalf("trgm 腿带标签下推不应降级: %v", cjkRes.Degraded)
	}
	legsSeen := map[string]bool{}
	for _, st := range cjkRes.Legs {
		legsSeen[st.Name] = true
	}
	if !legsSeen[LegTrgm] {
		t.Fatalf("CJK 查询应含 trgm 腿: %+v", cjkRes.Legs)
	}
	cjkHits := itemChunkIDs(cjkRes)
	if !cjkHits[chunkA.ID] || cjkHits[chunkB.ID] {
		t.Fatalf("trgm 腿标签过滤不符: %v", cjkHits)
	}
	// 向量腿 × 标签由 repository/kb 的腿级用例覆盖（测试二进制不载 config/*.yaml ⇒
	// 服务层嵌入档恒关，向量腿在此不可直接驱动；腿 SQL 与参数序在仓储层真连库验证）。
}
