package l2

// 本文件是 P6 验收的**L2 引擎级驱动与断言**（验收 A/B 与反向路径三条用例共用；用例本身在
// demo_e2e_seq_db_test.go，公共基建在 demo_e2e_db_test.go）。拆成第三个文件是为了守住
// 「代码文件 ≤ 1024 行」的文件规模纪律（校验脚本）。

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/ai-go-hub/ai-go-admin/internal/plugin/host"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// demoSpecFromMetadata 从**已安装的元数据行**装载运行期 spec（生产同一条校验链：Parse）
func demoSpecFromMetadata(t *testing.T, db *gorm.DB, name string) *Spec {
	t.Helper()
	row, err := gorm.G[host.Entity](db).Where("plugin = ? AND name = ?", name, demoEntity).First(context.Background())
	if err != nil {
		t.Fatalf("读取实体元数据失败: %v", err)
	}
	spec, err := Parse(row)
	if err != nil {
		t.Fatalf("装载实体 spec 失败: %v", err)
	}
	return spec
}

// demoAssertInstallBuiltStructure 安装链 ④b 的建表断言（F1：DDL 由安装编排放行，用例不再驱动引擎）：
// 元数据行在（④）→ 表在（④b）→ 以**表结构自省**复算应判定结构齐备（0 条语句，复装/升级的基线口径）。
func demoAssertInstallBuiltStructure(t *testing.T, db *gorm.DB, engine *Engine, spec *Spec) {
	t.Helper()
	if !spec.EngineOwnsDDL() {
		t.Fatalf("实体 %s/%s 的 ddl_owner=%s：引擎不做 DDL（应由插件迁移承担）", spec.Plugin, spec.Name, spec.DDLOwner())
	}
	plan, err := engine.BuildPlan(context.Background(), spec)
	if err != nil {
		t.Fatalf("复算结构计划失败: %v", err)
	}
	if !plan.Existing || len(plan.Statements) != 0 {
		t.Fatalf("安装编排的 ④b 已按 spec 建表 ⇒ 自省基线应判定结构齐备（0 条语句），实际 existing=%v:\n%s",
			plan.Existing, joinStatements(plan))
	}
	if !tableExists(t, db, plan.Table) {
		t.Fatalf("安装的 ④b 应建出实体表 %s", plan.Table)
	}
}

// demoExerciseEngineCRUD 引擎级六动作（handler.go 行 /；不经 HTTP）：
// 新增 3 行 → 列表（meta / 前缀搜索 / 白名单排序）→ 单条 → 更新（必填置空被拒）→ 拖动排序 → 软删。
func demoExerciseEngineCRUD(t *testing.T, engine *Engine, spec *Spec) {
	t.Helper()
	db := engine.DB()
	table := spec.PhysicalTableName()
	handler := entityFor(engine, spec)

	// ① 新增三行（code 唯一列取不同值，避开软删部分唯一索引）
	for i, code := range []string{"d-001", "d-002", "d-003"} {
		body := fmt.Sprintf(`{"title":"条目 %d","status":1,"code":%q}`, i+1, code)
		c, recorder := testContext(http.MethodPost, "/x", body, nil)
		handler.Create(c)
		if payload := decodeResponse(t, recorder); payload.Code != 0 {
			t.Fatalf("新增失败: %s", payload.Message)
		}
	}

	// ② 列表：分页 + 一次请求带元数据
	c, recorder := testContext(http.MethodPost, "/x", `{"page":1,"limit":10}`, nil)
	handler.List(c)
	payload := decodeResponse(t, recorder)
	if payload.Code != 0 {
		t.Fatalf("列表失败: %s", payload.Message)
	}
	if list, _ := payload.Data["list"].([]any); len(list) != 3 {
		t.Fatalf("应有 3 行，实际 %v", payload.Data["list"])
	}
	meta, _ := payload.Data["meta"].(map[string]any)
	if meta == nil || meta["title"] != "条目" {
		t.Fatalf("列表应随带实体标题元数据（双语元数据随响应下发），实际 %v", payload.Data["meta"])
	}
	if columns, _ := meta["columns"].([]any); len(columns) == 0 {
		t.Fatalf("元数据应带列定义，实际 %v", meta)
	}

	// ③ 前缀模糊搜索（L2 内 LIKE/ILIKE 是前缀匹配）+ 白名单列排序
	c, recorder = testContext(http.MethodPost, "/x",
		`{"page":1,"limit":10,"sort":"title","order":"asc","wheres":[{"wheres":[{"field":"title","operator":"ILIKE","value":"条目 2"}],"or":false}]}`, nil)
	handler.List(c)
	payload = decodeResponse(t, recorder)
	if payload.Code != 0 {
		t.Fatalf("条件查询失败: %s", payload.Message)
	}
	if list, _ := payload.Data["list"].([]any); len(list) != 1 {
		t.Fatalf("前缀匹配应命中 1 行，实际 %v", payload.Data["list"])
	}

	// ④ 单条 + 更新 + 必填校验
	id := latestID(t, db, table)
	c, recorder = testContext(http.MethodGet, "/x", "", gin.Params{{Key: "pk", Value: fmt.Sprint(id)}})
	handler.Get(c)
	if payload = decodeResponse(t, recorder); payload.Code != 0 {
		t.Fatalf("单条读取失败: %s", payload.Message)
	}
	c, recorder = testContext(http.MethodPost, "/x", `{"title":"改过的标题"}`, gin.Params{{Key: "pk", Value: fmt.Sprint(id)}})
	handler.Update(c)
	if payload = decodeResponse(t, recorder); payload.Code != 0 {
		t.Fatalf("更新失败: %s", payload.Message)
	}
	c, recorder = testContext(http.MethodPost, "/x", `{"title":""}`, gin.Params{{Key: "pk", Value: fmt.Sprint(id)}})
	handler.Update(c)
	if payload = decodeResponse(t, recorder); payload.Code == 0 {
		t.Error("必填字段置空必须被拒（required 校验在写入期仍生效）")
	}

	// ⑤ 拖动排序（drag_sort: true ⇒ 引擎附加 weigh 列并注册 /sort）
	ids := allIDs(t, db, table)
	if len(ids) != 3 {
		t.Fatalf("应有 3 行，实际 %v", ids)
	}
	for i, rowID := range ids {
		if err := db.Exec(fmt.Sprintf("UPDATE %s SET %s = ? WHERE %s = ?", quoteForDrop(table), `"weigh"`, `"id"`), i+1, rowID).Error; err != nil {
			t.Fatalf("设置 weigh 失败: %v", err)
		}
	}
	moveID, targetID := ids[2], ids[0]
	body := fmt.Sprintf(`{"move":"%d","target":"%d","direction":"up","weigh":1,"sort":"weigh","order":"asc"}`, moveID, targetID)
	c, recorder = testContext(http.MethodPost, "/x", body, nil)
	handler.Sort(c)
	if payload = decodeResponse(t, recorder); payload.Code != 0 {
		t.Fatalf("拖动排序失败: %s", payload.Message)
	}

	// ⑥ 软删（写 deleted_at；界面与引擎都不提供硬删）
	c, recorder = testContext(http.MethodPost, "/x", fmt.Sprintf(`{"pks":["%d"]}`, moveID), nil)
	handler.Delete(c)
	if payload = decodeResponse(t, recorder); payload.Code != 0 {
		t.Fatalf("软删失败: %s", payload.Message)
	}
	c, recorder = testContext(http.MethodPost, "/x", `{"page":1,"limit":10}`, nil)
	handler.List(c)
	if list, _ := decodeResponse(t, recorder).Data["list"].([]any); len(list) != 2 {
		t.Fatalf("软删后列表应剩 2 行，实际 %v", list)
	}
	rows := make([]map[string]any, 0, 1)
	if err := db.Raw(fmt.Sprintf(`SELECT "deleted_at" FROM %s WHERE "id" = ?`, quoteForDrop(table)), moveID).Find(&rows).Error; err != nil {
		t.Fatalf("读取软删列失败: %v", err)
	}
	if len(rows) != 1 || rows[0]["deleted_at"] == nil {
		t.Fatalf("软删行必须留在表里且 deleted_at 非空，实际 %v", rows)
	}
}

// demoUint8Ptr uint8 指针（角色组 Status 用）
func demoUint8Ptr(value uint8) *uint8 { return &value }
