// plugin: kb v1.0.0
package kb

import (
	"strings"
	"testing"
)

// TestLegSQLFirstCondition 腿 SQL 静态审查（无库）：每条腿**第一条件 = 可读库集下推**
// （`c.library_id IN ?`，语义即 library_id = ANY(:readable)；T5 硬条款；写法注见 chunkBaseCond）；
// 文档在场条件（未删 ∧ d.active_build <> ” ∧ c.build_hash = d.active_build；**不再含 build_state='ready'**
// ——口径裁决）；显式列（禁 SELECT *）。
func TestLegSQLFirstCondition(t *testing.T) {
	s := NewStore()

	// chunk 系腿（exact/fts/trgm/chunk-by-id 共用骨架）
	sqlText := s.chunkSelect("1.0::float8 AS rank")
	if !strings.HasPrefix(sqlText, "SELECT ") {
		t.Fatalf("腿 SQL 不以 SELECT 起头：%s", sqlText)
	}
	if strings.Contains(sqlText, "SELECT *") {
		t.Fatalf("腿 SQL 出现 SELECT *（禁）")
	}
	whereIdx := strings.Index(sqlText, " WHERE ")
	if whereIdx < 0 {
		t.Fatalf("腿 SQL 无 WHERE：%s", sqlText)
	}
	cond := sqlText[whereIdx+len(" WHERE "):]
	const anyCond = "c.library_id IN ?"
	if !strings.HasPrefix(cond, anyCond) {
		t.Fatalf("第一条件不是可读库集下推（library_id IN ?）：%s", cond)
	}
	for _, want := range []string{"d.deleted_at IS NULL", "d.active_build <> ''", "c.build_hash = d.active_build"} {
		if !strings.Contains(cond, want) {
			t.Fatalf("腿 SQL 缺文档在场条件 %q：%s", want, cond)
		}
	}
	// F5 采纳：ready 依赖已去掉（重建/换窗期旧完成 build 仍可召回——回归即召回空洞复现）
	if strings.Contains(cond, "build_state") {
		t.Fatalf("腿 SQL 仍含 build_state 条件（F5 已裁决去掉 ready）：%s", cond)
	}
	// 可读集下推必须先于文档在场条件（第一条件语义）
	if strings.Index(cond, anyCond) > strings.Index(cond, "d.deleted_at IS NULL") {
		t.Fatalf("可读集条件未在最前：%s", cond)
	}

	// 向量腿（别名 v；同款文档在场条件 + 当前模型身份过滤）
	if !strings.HasPrefix(vectorBaseCond, "v.library_id IN ?") {
		t.Fatalf("向量腿第一条件不是 library_id 下推：%s", vectorBaseCond)
	}
	for _, want := range []string{"d.deleted_at IS NULL", "d.active_build <> ''", "c.build_hash = d.active_build", "v.model = ?"} {
		if !strings.Contains(vectorBaseCond, want) {
			t.Fatalf("向量腿缺条件 %q：%s", want, vectorBaseCond)
		}
	}
	if strings.Contains(vectorBaseCond, "build_state") {
		t.Fatalf("向量腿仍含 build_state 条件（F5 已裁决去掉 ready）：%s", vectorBaseCond)
	}
	// 模型身份占位符在场（参数序 = 文本序：library_id IN ? → v.model = ?）
	if n := strings.Count(vectorBaseCond, "?"); n != 2 {
		t.Fatalf("向量腿基条件占位符数 = %d（期望 2：readable、model）：%s", n, vectorBaseCond)
	}

	// 扫描帽（v1 精确扫描）：窗帽 ≤ ScanCap()（配置化：kb.search.scan_cap，
	// 缺省回落 ScanCapDefault=5000 先例值——帽值语义不变，仅改可配）
	if ScanCapDefault != 5000 {
		t.Fatalf("扫描帽缺省值不得偏离先例（5000）：%d", ScanCapDefault)
	}
	if ScanCap() < 1 {
		t.Fatalf("扫描帽非法（须为正）：%d", ScanCap())
	}
	if scanLimit(0) != ScanCap() || scanLimit(999999) != ScanCap() || scanLimit(8) != 8 {
		t.Fatalf("scanLimit 帽规整不合规：0→%d 999999→%d 8→%d", scanLimit(0), scanLimit(999999), scanLimit(8))
	}
}

// TestChunkHitColumnsExplicit 显式列面（禁 SELECT *；text 入选为 snippet 真源）
func TestChunkHitColumnsExplicit(t *testing.T) {
	for _, want := range []string{"c.id AS chunk_id", "c.text", "c.text_hash", "d.title AS title", "d.source_ref"} {
		if !strings.Contains(chunkHitColumns, want) {
			t.Fatalf("显式列缺 %q：%s", want, chunkHitColumns)
		}
	}
	if strings.Contains(chunkHitColumns, "*") {
		t.Fatalf("列面含通配：%s", chunkHitColumns)
	}
}
