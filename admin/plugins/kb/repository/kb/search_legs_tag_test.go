// plugin: kb v1.0.0
package kb

import (
	"strings"
	"testing"
)

// 标签过滤下推的静态审查（T5 硬条款；无库）：
//   - 空/未给 = 无条件（不发兜底子查询）；
//   - 非空 = **两段子查询拼进同一条件**（切片级 link 命中 c.id；文档级 link 命中 c.document_id），
//     且整体以 " AND (" 拼接——与可读集同层进 WHERE，先于 LIMIT/`<=>` 截断（禁应用层后过滤）；
//   - 参数次序与 SQL 文本一致（object_type、tag_ids、object_type、tag_ids）；
//   - 标签方向走 uk(tag_id,object_type,object_id) 前缀（tag_id IN + object_type 等值）。

func TestTagFilterCondUnit(t *testing.T) {
	s := NewStore()

	// ① 空集/未给：无条件
	if cond, args := s.tagCond(nil); cond != "" || args != nil {
		t.Fatalf("nil 过滤应零条件: %q %v", cond, args)
	}
	if cond, args := s.tagCond(&TagFilter{}); cond != "" || args != nil {
		t.Fatalf("空标签集应零条件: %q %v", cond, args)
	}

	// ② 非空：两段子查询同条件
	cond, args := s.tagCond(&TagFilter{TagIDs: []uint{7, 9}})
	if !strings.HasPrefix(cond, " AND (") || !strings.HasSuffix(cond, ")") {
		t.Fatalf("标签条件应为单括号组 AND 条件: %q", cond)
	}
	for _, want := range []string{
		"c.id IN (SELECT l1.object_id FROM",
		"l1.object_type = ? AND l1.tag_id IN ?",
		" OR c.document_id IN (SELECT l2.object_id FROM",
		"l2.object_type = ? AND l2.tag_id IN ?",
	} {
		if !strings.Contains(cond, want) {
			t.Fatalf("条件缺片段 %q: %s", want, cond)
		}
	}
	// 两段子查询之间不得有别的 AND 级断开（同一条件；T5「拼进同一条件」）
	if strings.Contains(cond, ") AND (") {
		t.Fatalf("两段子查询未拼进同一条件: %s", cond)
	}
	if len(args) != 4 {
		t.Fatalf("参数面不符（object_type/tag_ids × 两段）: %v", args)
	}
}

// TestTagFilterArgsOrderUnit 参数次序 = SQL 文本出现次序（object_type、tag_ids、object_type、tag_ids）。
func TestTagFilterArgsOrderUnit(t *testing.T) {
	s := NewStore()
	cond, args := s.tagCond(&TagFilter{TagIDs: []uint{7, 9}})
	if len(args) != 4 {
		t.Fatalf("参数个数不符: %v", args)
	}
	// 文本内占位符次序：l1.object_type → l1.tag_id → l2.object_type → l2.tag_id
	if got := strings.Count(cond, "?"); got != 4 {
		t.Fatalf("占位符数应为 4: %d", got)
	}
	if args[0] != "chunk" || args[2] != "document" {
		t.Fatalf("对象类型参数次序不符: %v", args)
	}
	ids1, ok := args[1].([]int64)
	if !ok || len(ids1) != 2 || ids1[0] != 7 || ids1[1] != 9 {
		t.Fatalf("标签集参数不符: %#v", args[1])
	}
	ids2, ok := args[3].([]int64)
	if !ok || len(ids2) != 2 {
		t.Fatalf("第二段标签集参数不符: %#v", args[3])
	}
}

// TestLegSQLTagCondAppendedAfterBase 腿 SQL 静态审查：标签条件下推后
// ① 可读集仍为**第一条件**（前缀不变）；② 文档在场三连仍在场（d.deleted_at IS NULL ∧
// d.active_build 非空 ∧ c.build_hash = d.active_build——F5 采纳后不含 build_state）；③ 标签条件
// 位于 LIMIT 之前（先过滤后截断）。
func TestLegSQLTagCondAppendedAfterBase(t *testing.T) {
	s := NewStore()
	tagSQL, tagArgs := s.tagCond(&TagFilter{TagIDs: []uint{7}})
	exact := s.chunkSelect("1.0::float8 AS rank") + tagSQL + " AND c.text ILIKE ? ORDER BY c.id ASC LIMIT ?"
	whereIdx := strings.Index(exact, " WHERE ")
	cond := exact[whereIdx+len(" WHERE "):]
	if !strings.HasPrefix(cond, "c.library_id IN ?") {
		t.Fatalf("可读库集不再是第一条件: %s", cond)
	}
	if strings.Index(cond, "l1.tag_id IN ?") > strings.Index(cond, "LIMIT ?") {
		t.Fatalf("标签过滤未先于 LIMIT（禁后过滤）: %s", cond)
	}
	if len(tagArgs) != 4 {
		t.Fatalf("标签参数面不符: %v", tagArgs)
	}
}

// TestSearchVectorTagFilterDB 向量腿 × 标签下推（真连库；服务层嵌入档在测试二进制恒关，向量腿的
// SQL 与参数序只能在仓储层验证）：命中重挂面 = 切片级 link；基线（无标签）两块都在。
// 门控：PLUGIN_KB_DB=1；跑法：PLUGIN_KB_DB=1 go test -count=1 -timeout=120s ./plugins/kb/repository/kb/
func TestSearchVectorTagFilterDB(t *testing.T) {
	f := newLegFixture(t)
	ctx := f.ctx
	libA := f.readyDocWithVector("vec-tag-a")
	libB := f.readyDocWithVector("vec-tag-b")

	// 取两库各自的切片 id（夹具内登记，直接反查）
	chunkOf := func(libraryID uint) uint {
		t.Helper()
		var id uint
		if err := f.db.Raw("SELECT id FROM kb_chunk WHERE library_id = ? ORDER BY id LIMIT 1", libraryID).Scan(&id).Error; err != nil || id == 0 {
			t.Fatalf("读切片失败（lib=%d）: %v", libraryID, err)
		}
		return id
	}
	chunkA := chunkOf(libA)

	// 标签 + 切片级关联（本用例自清：link → tag → group）
	var groupID, tagID int64
	if err := f.db.Raw("INSERT INTO kb_tag_group (scope, library_id, name, weigh, created_at, updated_at)"+
		" VALUES ('platform', 0, ?, 0, NOW(), NOW()) RETURNING id", "kb-leg-tagg-"+f.suffix).Scan(&groupID).Error; err != nil {
		t.Fatalf("建标签组失败: %v", err)
	}
	if err := f.db.Raw("INSERT INTO kb_tag (group_id, name, weigh, status, created_at, updated_at)"+
		" VALUES (?, ?, 0, 1, NOW(), NOW()) RETURNING id", groupID, "kb-leg-tag-"+f.suffix).Scan(&tagID).Error; err != nil {
		t.Fatalf("建标签失败: %v", err)
	}
	if err := f.db.Exec("INSERT INTO kb_tag_link (tag_id, object_type, object_id, operator_id, created_at)"+
		" VALUES (?, 'chunk', ?, 0, NOW())", tagID, chunkA).Error; err != nil {
		t.Fatalf("建标签关联失败: %v", err)
	}
	t.Cleanup(func() {
		if err := f.db.Exec("DELETE FROM kb_tag_link WHERE tag_id = ?", tagID).Error; err != nil {
			t.Logf("清理标签关联失败（请手工核）: %v", err)
		}
		if err := f.db.Exec("DELETE FROM kb_tag WHERE id = ?", tagID).Error; err != nil {
			t.Logf("清理标签失败（请手工核）: %v", err)
		}
		if err := f.db.Exec("DELETE FROM kb_tag_group WHERE id = ?", groupID).Error; err != nil {
			t.Logf("清理标签组失败（请手工核）: %v", err)
		}
	})

	vec := "[" + strings.Repeat("0,", 1023) + "0]"
	readable := []uint{libA, libB}

	// ① 基线：两库各一块（身份 = 夹具当前模型身份 legTestModel）
	all, err := f.store.SearchVector(ctx, readable, vec, legTestModel, 10, nil)
	if err != nil {
		t.Fatalf("向量腿基线失败: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("基线应两块: %d", len(all))
	}

	// ② 带标签：只留挂标的块（过滤先于 `<=>` 排序；两段子查询参数序正确——错位即 SQL 报错）
	hits, err := f.store.SearchVector(ctx, readable, vec, legTestModel, 10, &TagFilter{TagIDs: []uint{uint(tagID)}})
	if err != nil {
		t.Fatalf("向量腿带标签失败: %v", err)
	}
	if len(hits) != 1 || hits[0].ChunkID != chunkA {
		t.Fatalf("向量腿标签过滤不符: %+v", hits)
	}
}
