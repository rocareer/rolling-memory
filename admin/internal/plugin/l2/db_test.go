package l2

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-go-hub/ai-go-admin/internal/infra/config"
	"github.com/ai-go-hub/ai-go-admin/internal/infra/database"
	"github.com/ai-go-hub/ai-go-admin/internal/plugin/manifest"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// DB 门控用例（默认 skip，需显式环境变量；无 PLUGIN_DB 时一律不连库）：
//
//	PLUGIN_DB=1 go test -count=1 -timeout=120s ./internal/plugin/l2/...
//
// 自带建表与清理：只碰 <前缀>plugin_l2dbtest_* 自建表（用例组结束即 DROP），不依赖插件元数据表
// （元数据通过 injected resolver 提供；Resolve 的库级断言在表缺失时自行跳过）。

const dbTestPlugin = "l2dbtest"

var errNoDBConfig = errors.New("仓库无 config/*.yaml")

var (
	dbOnce sync.Once
	dbConn *gorm.DB
	dbErr  error
)

// dbSetup 连真实库（配置从仓库根磁盘注入，与运行时同源）
func dbSetup(t *testing.T) *gorm.DB {
	t.Helper()
	if os.Getenv("PLUGIN_DB") != "1" {
		t.Skip("需 PLUGIN_DB=1（连真实库；本用例组自带建表与清理）")
	}
	dbOnce.Do(func() {
		if err := chdirRepoRoot(); err != nil {
			dbErr = err
			return
		}
		if err := config.Init(); err != nil {
			dbErr = fmt.Errorf("初始化配置: %w", err)
			return
		}
		if config.Get().Database.Write.Host == "" {
			dbErr = errNoDBConfig
			return
		}
		if err := database.Init(); err != nil {
			dbErr = fmt.Errorf("初始化数据库: %w", err)
			return
		}
		dbConn = database.DB()
	})
	if errors.Is(dbErr, errNoDBConfig) {
		t.Skip("仓库无 config/*.yaml，跳过 DB 集成测试")
	}
	if dbErr != nil {
		t.Fatalf("初始化测试数据库: %v", dbErr)
	}
	return dbConn
}

// chdirRepoRoot 切到仓库根（配置与迁移文件都在根目录相对路径下）
func chdirRepoRoot() error {
	dir, err := os.Getwd()
	if err != nil {
		return err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return os.Chdir(dir)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return errors.New("未找到 go.mod（仓库根）")
		}
		dir = parent
	}
}

// dropTestTable 清理测试表（含索引：表删了索引随之删）
func dropTestTable(t *testing.T, db *gorm.DB, table string) {
	t.Helper()
	if err := db.Exec("DROP TABLE IF EXISTS " + quoteForDrop(table) + " CASCADE").Error; err != nil {
		t.Fatalf("清理测试表 %s 失败: %v", table, err)
	}
}

// quoteForDrop 测试清理专用引用（表名来自本文件的常量 + 配置前缀）
func quoteForDrop(table string) string {
	return `"` + strings.ReplaceAll(table, `"`, `""`) + `"`
}

// tableColumns 实际列名（自省口径与引擎一致）
func tableColumns(t *testing.T, db *gorm.DB, table string) map[string]struct{} {
	t.Helper()
	rows := make([]map[string]any, 0, 8)
	if err := db.Raw(`SELECT column_name FROM information_schema.columns
WHERE table_schema = current_schema() AND table_name = ?`, table).Find(&rows).Error; err != nil {
		t.Fatalf("读取列名失败: %v", err)
	}
	columns := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		columns[fmt.Sprint(row["column_name"])] = struct{}{}
	}
	return columns
}

// tableExists 测试判定（与引擎同口径：pg_class + current_schema）
func tableExists(t *testing.T, db *gorm.DB, table string) bool {
	t.Helper()
	var rows []map[string]any
	if err := db.Raw(`SELECT t.relname FROM pg_catalog.pg_class t
JOIN pg_catalog.pg_namespace n ON n.oid = t.relnamespace
WHERE n.nspname = current_schema() AND t.relkind = 'r' AND t.relname = ?`, table).Find(&rows).Error; err != nil {
		t.Fatalf("检查表 %s 失败: %v", table, err)
	}
	return len(rows) > 0
}

// indexNames 表上的索引名
func indexNames(t *testing.T, db *gorm.DB, table string) map[string]struct{} {
	t.Helper()
	rows := make([]map[string]any, 0, 8)
	if err := db.Raw("SELECT indexname FROM pg_indexes WHERE schemaname = current_schema() AND tablename = ?", table).Find(&rows).Error; err != nil {
		t.Fatalf("读取索引失败: %v", err)
	}
	names := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		names[fmt.Sprint(row["indexname"])] = struct{}{}
	}
	return names
}

// dbTestSpec 用例实体：文本列 + 二值列 + decimal + 唯一列 + 拖排序（覆盖各 DDL 分支）。
// extra 里的字段与基础字段同名时**替换**基础字段（升级用例要改类型/长度，不能出现重复声明）
func dbTestSpec(t *testing.T, extra ...manifest.Field) *Spec {
	t.Helper()
	fields := []manifest.Field{
		textField("title", "标题", func(field *manifest.Field) {
			field.Required = true
			field.Length = 64
			field.Default = ""
			field.Sort = true
		}),
		{Name: "status", Type: "bool", TitleZh: "状态", TitleEn: "Status", Required: true, List: true, Form: "switch",
			Search: &manifest.Search{Op: "eq"},
			Dict:   []manifest.Dict{{Value: 0, LabelZh: "停用", LabelEn: "Disabled"}, {Value: 1, LabelZh: "启用", LabelEn: "Enabled"}}},
		{Name: "amount", Type: "int", TitleZh: "数量", TitleEn: "Amount", List: true, Form: "input-number",
			Search: &manifest.Search{Op: "egt"}},
	}
	for _, field := range extra {
		replaced := false
		for i := range fields {
			if fields[i].Name == field.Name {
				fields[i] = field
				replaced = true
				break
			}
		}
		if !replaced {
			fields = append(fields, field)
		}
	}
	return mustParse(t, dbTestPlugin, "items", manifest.Entity{
		Entity:   "items",
		TitleZh:  "条目",
		TitleEn:  "Items",
		DragSort: true,
		Fields:   fields,
	})
}

// TestDatabaseDDLLifecycle 建表 → 幂等 → 加列 → 拒绝项 → 保留列（演进规则在真实库上成立）
func TestDatabaseDDLLifecycle(t *testing.T) {
	db := dbSetup(t)
	engine := New(db)
	ctx := context.Background()
	spec := dbTestSpec(t)
	table := spec.PhysicalTableName()
	dropTestTable(t, db, table)
	t.Cleanup(func() { dropTestTable(t, db, table) })

	// ① 装（新实体）：建表 + 索引 + 注释，逐条回报
	plan, err := engine.BuildPlan(ctx, spec)
	if err != nil {
		t.Fatalf("建表计划失败: %v", err)
	}
	if plan.Existing || len(plan.Statements) == 0 {
		t.Fatalf("新实体的计划应为建表，实际 existing=%v 语句 %d 条", plan.Existing, len(plan.Statements))
	}
	results, err := engine.ApplyPlan(ctx, plan)
	if err != nil {
		t.Fatalf("执行建表失败: %v（已执行 %d 条）", err, len(results))
	}
	if len(results) != len(plan.Statements) {
		t.Fatalf("应逐条回报全部语句，实际 %d/%d", len(results), len(plan.Statements))
	}
	if !tableExists(t, db, table) {
		t.Fatalf("建表后表 %s 应存在", table)
	}
	columns := tableColumns(t, db, table)
	for _, want := range []string{"id", "title", "status", "amount", "weigh", "deleted_at", "created_at", "updated_at"} {
		if _, ok := columns[want]; !ok {
			t.Fatalf("列 %s 应存在，实际 %v", want, columns)
		}
	}
	indexes := indexNames(t, db, table)
	for _, want := range []string{
		"idx_plugin_" + dbTestPlugin + "_items_weigh",
		"idx_plugin_" + dbTestPlugin + "_items_deleted_at",
	} {
		if _, ok := indexes[want]; !ok {
			t.Fatalf("索引 %s 应一次到位，实际 %v", want, indexes)
		}
	}

	// ② 复装/升级的基线是表结构自省：再跑一次 ⇒ 无语句（幂等）
	again, err := engine.BuildPlan(ctx, spec)
	if err != nil {
		t.Fatalf("重复应用不应报错: %v", err)
	}
	if !again.Existing || len(again.Statements) != 0 {
		t.Fatalf("表结构已齐备时计划应为空，实际 existing=%v 语句 %+v", again.Existing, again.Statements)
	}

	// ③ 升级：加一列（允许的唯一列级变更）
	upgraded := dbTestSpec(t, manifest.Field{
		Name: "remark", Type: "string", TitleZh: "备注", TitleEn: "Remark", Length: 32, Form: "input",
	})
	planAdd, err := engine.BuildPlan(ctx, upgraded)
	if err != nil {
		t.Fatalf("加列计划失败: %v", err)
	}
	if !strings.Contains(joinStatements(planAdd), `ADD COLUMN IF NOT EXISTS "remark" VARCHAR(32)`) {
		t.Fatalf("应产出加列语句，实际:\n%s", joinStatements(planAdd))
	}
	if _, err := engine.ApplyPlan(ctx, planAdd); err != nil {
		t.Fatalf("执行加列失败: %v", err)
	}
	if _, ok := tableColumns(t, db, table)["remark"]; !ok {
		t.Fatal("加列后 remark 列应存在")
	}

	// ④ 改类型 ⇒ 拒绝（提示改用迁移文件）
	typeChanged := dbTestSpec(t, manifest.Field{
		Name: "title", Type: "text", TitleZh: "标题", TitleEn: "Title", Form: "textarea",
	})
	if _, err := engine.BuildPlan(ctx, typeChanged); err == nil {
		t.Fatal("改类型必须拒绝")
	} else if !strings.Contains(err.Error(), "迁移文件") {
		t.Fatalf("拒绝理由应指向迁移文件，实际 %v", err)
	}

	// ⑤ 长度变更（放大与收窄）⇒ 一律拒绝（「改类型拒绝」：长度变更在 DDL 层就是 ALTER COLUMN TYPE；
	// 引擎对既有列只发 ADD COLUMN 与 DROP DEFAULT，不得再落到 ALTER COLUMN TYPE 执行路径）
	for _, length := range []int{128, 8} {
		changed := dbTestSpec(t, manifest.Field{
			Name: "title", Type: "string", TitleZh: "标题", TitleEn: "Title", Length: length, Required: true, Default: "", Form: "input",
		})
		if _, err := engine.BuildPlan(ctx, changed); err == nil {
			t.Fatalf("varchar 长度 %d（表上 64）必须拒绝（改类型/改长度需迁移文件）", length)
		}
	}

	// ⑥ 表里存在而 spec 未声明的列 ⇒ 保留 + 报告，绝不自动 DROP；且不得发散列/属性的语句
	if err := db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN IF NOT EXISTS \"legacy\" integer", quoteForDrop(table))).Error; err != nil {
		t.Fatalf("造遗留列失败: %v", err)
	}
	planKeep, err := engine.BuildPlan(ctx, spec)
	if err != nil {
		t.Fatalf("含遗留列时计划不应报错: %v", err)
	}
	if !slices.Contains(planKeep.KeptColumns, "legacy") {
		t.Fatalf("遗留列应进保留清单，实际 %v", planKeep.KeptColumns)
	}
	// 本夹具有遗留列（legacy）+ remark 也是「表有 spec 无」⇒ 保留清单含两列；spec 声明的列都在表上
	// ⇒ 没有「新列出现」，不触发疑似改列名的拒绝
	if strings.Contains(joinStatements(planKeep), "ALTER TABLE") {
		t.Fatalf("遗留列只报告不改动（既不加列也不 DROP）：\n%s", joinStatements(planKeep))
	}

	// ⑦ --purge 的删表路径
	if err := engine.DropTable(ctx, spec); err != nil {
		t.Fatalf("删表失败: %v", err)
	}
	if tableExists(t, db, table) {
		t.Fatal("删表后表不应存在")
	}
}

// TestDatabaseTrgmExtensionBranch trgm 的两个分支：扩展不可用 ⇒ 拒装；可用 ⇒ GIN 索引建成
func TestDatabaseTrgmExtensionBranch(t *testing.T) {
	db := dbSetup(t)
	engine := New(db)
	ctx := context.Background()

	var extensions []map[string]any
	if err := db.Raw("SELECT extname FROM pg_extension WHERE extname = 'pg_trgm'").Find(&extensions).Error; err != nil {
		t.Fatalf("检查扩展失败: %v", err)
	}

	spec := mustParse(t, dbTestPlugin, "tags", manifest.Entity{
		Entity:  "tags",
		TitleZh: "标签",
		TitleEn: "Tags",
		Fields: []manifest.Field{textField("name", "名称", func(field *manifest.Field) {
			field.Trgm = true
			field.Length = 64
		})},
	})
	table := spec.PhysicalTableName()
	dropTestTable(t, db, table)
	t.Cleanup(func() { dropTestTable(t, db, table) })

	plan, err := engine.BuildPlan(ctx, spec)
	if len(extensions) == 0 {
		if err == nil {
			t.Fatal("扩展不可用时声明 trgm 的实体必须拒装")
		}
		if !strings.Contains(err.Error(), "pg_trgm") {
			t.Fatalf("拒绝理由应点名 pg_trgm，实际 %v", err)
		}
		return
	}

	if err != nil {
		t.Fatalf("扩展可用时不应报错: %v", err)
	}
	if !strings.Contains(joinStatements(plan), "gin_trgm_ops") {
		t.Fatalf("应产出 pg_trgm GIN 索引，实际:\n%s", joinStatements(plan))
	}
	if _, err := engine.ApplyPlan(ctx, plan); err != nil {
		t.Fatalf("执行 trgm 建表失败: %v", err)
	}
}

// TestDatabaseCRUDAndSort 六动作在真实库上的行为：新增/读取/更新/软删/列表/拖动排序
func TestDatabaseCRUDAndSort(t *testing.T) {
	db := dbSetup(t)
	ctx := context.Background()
	spec := dbTestSpec(t)
	table := spec.PhysicalTableName()
	dropTestTable(t, db, table)
	t.Cleanup(func() { dropTestTable(t, db, table) })

	engine := New(db)
	if _, err := engine.ApplyPlan(ctx, mustPlan(t, engine, spec)); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	handler := entityFor(engine, spec)

	// ① 新增三行（必填校验 + 类型收敛 + 默认 weigh）
	for i, title := range []string{"第一条", "第二条", "第三条"} {
		body := fmt.Sprintf(`{"title":%q,"status":1,"amount":%d}`, title, i+1)
		c, recorder := testContext(http.MethodPost, "/x", body, nil)
		handler.Create(c)
		if payload := decodeResponse(t, recorder); payload.Code != 0 {
			t.Fatalf("新增失败: %s", payload.Message)
		}
	}

	// ② 列表（默认排序：主键托底；软删过滤）
	c, recorder := testContext(http.MethodPost, "/x", `{"page":1,"limit":10}`, nil)
	handler.List(c)
	payload := decodeResponse(t, recorder)
	if payload.Code != 0 {
		t.Fatalf("列表失败: %s", payload.Message)
	}
	list, _ := payload.Data["list"].([]any)
	if len(list) != 3 {
		t.Fatalf("应有 3 行，实际 %d", len(list))
	}
	first, _ := list[0].(map[string]any)
	if first["title"] != "第三条" {
		t.Fatalf("默认排序应为主键 DESC 托底（最新在前），首行实际 %v", first["title"])
	}
	meta, _ := payload.Data["meta"].(map[string]any)
	if meta["title"] != "条目" {
		t.Fatalf("元数据应带实体标题，实际 %v", meta)
	}
	columns, _ := meta["columns"].([]any)
	if len(columns) == 0 {
		t.Fatalf("元数据应带列定义，实际 %v", meta)
	}
	if _, ok := meta["dicts"].(map[string]any)["status"]; !ok {
		t.Fatalf("元数据应带 dict（状态列渲染依据），实际 %v", meta["dicts"])
	}

	// ③ 条件查询（前缀匹配）+ 排序走白名单列（title 声明了 sort:true；amount 未声明 ⇒ 排序会拒）
	c, recorder = testContext(http.MethodPost, "/x",
		`{"page":1,"limit":10,"sort":"title","order":"asc","wheres":[{"wheres":[{"field":"title","operator":"ILIKE","value":"第二"}],"or":false}]}`, nil)
	handler.List(c)
	payload = decodeResponse(t, recorder)
	if payload.Code != 0 {
		t.Fatalf("条件查询失败: %s", payload.Message)
	}
	list, _ = payload.Data["list"].([]any)
	if len(list) != 1 {
		t.Fatalf("前缀匹配应命中 1 行，实际 %d", len(list))
	}

	// ④ 单条 + 更新 + 必填校验
	id := latestID(t, db, table)
	c, recorder = testContext(http.MethodGet, "/x", "", gin.Params{{Key: "pk", Value: fmt.Sprint(id)}})
	handler.Get(c)
	if payload = decodeResponse(t, recorder); payload.Code != 0 {
		t.Fatalf("单条读取失败: %s", payload.Message)
	}
	if row, _ := payload.Data["row"].(map[string]any); row["title"] != "第三条" {
		t.Fatalf("单条内容不符: %v", payload.Data["row"])
	}

	c, recorder = testContext(http.MethodPost, "/x", `{"title":"改过"}`, gin.Params{{Key: "pk", Value: fmt.Sprint(id)}})
	handler.Update(c)
	if payload = decodeResponse(t, recorder); payload.Code != 0 {
		t.Fatalf("更新失败: %s", payload.Message)
	}
	c, recorder = testContext(http.MethodPost, "/x", `{"title":""}`, gin.Params{{Key: "pk", Value: fmt.Sprint(id)}})
	handler.Update(c)
	if payload = decodeResponse(t, recorder); payload.Code == 0 {
		t.Fatal("必填字段置空必须拒绝")
	}

	// ⑤ 排序：把最后一行拖到最前（weigh 递增，asc 向上拖）
	ids := allIDs(t, db, table)
	if len(ids) != 3 {
		t.Fatalf("应有 3 行，实际 %v", ids)
	}
	for i, rowID := range ids {
		if err := db.Exec(fmt.Sprintf("UPDATE %s SET %s = ? WHERE %s = ?", quoteForDrop(table), `"weigh"`, `"id"`), i+1, rowID).Error; err != nil {
			t.Fatalf("设置 weigh 失败: %v", err)
		}
	}
	moveID := ids[2]
	targetID := ids[0]
	// 契约对齐宿主 handler.SortRequest 与前端（web/src/hooks/useTableManager.ts:466-468）：
	// move/target 是**字符串**形态的主键
	body := fmt.Sprintf(`{"move":"%d","target":"%d","direction":"up","weigh":1,"sort":"weigh","order":"asc"}`, moveID, targetID)
	c, recorder = testContext(http.MethodPost, "/x", body, nil)
	handler.Sort(c)
	if payload = decodeResponse(t, recorder); payload.Code != 0 {
		t.Fatalf("拖动排序失败: %s", payload.Message)
	}
	c, recorder = testContext(http.MethodPost, "/x", `{"page":1,"limit":10,"sort":"weigh","order":"asc"}`, nil)
	handler.List(c)
	payload = decodeResponse(t, recorder)
	list, _ = payload.Data["list"].([]any)
	ordered := make([]int64, 0, len(list))
	for _, item := range list {
		row, _ := item.(map[string]any)
		ordered = append(ordered, int64FromAny(t, row["id"]))
	}
	if len(ordered) != 3 || ordered[0] != moveID || ordered[1] != targetID {
		t.Fatalf("拖动排序结果不符：期望 %v 打头，实际 %v", []int64{moveID, targetID}, ordered)
	}

	// ⑥ 软删：列表与单条都看不到，但行还在（deleted_at 非空）
	c, recorder = testContext(http.MethodPost, "/x", fmt.Sprintf(`{"pks":["%d"]}`, moveID), nil)
	handler.Delete(c)
	if payload = decodeResponse(t, recorder); payload.Code != 0 {
		t.Fatalf("软删失败: %s", payload.Message)
	}
	c, recorder = testContext(http.MethodPost, "/x", `{"page":1,"limit":10}`, nil)
	handler.List(c)
	list, _ = decodeResponse(t, recorder).Data["list"].([]any)
	if len(list) != 2 {
		t.Fatalf("软删后列表应剩 2 行，实际 %d", len(list))
	}
	c, recorder = testContext(http.MethodGet, "/x", "", gin.Params{{Key: "pk", Value: fmt.Sprint(moveID)}})
	handler.Get(c)
	if payload = decodeResponse(t, recorder); payload.Code == 0 {
		t.Fatal("软删行不得被单条读取返回")
	}
	var deleted []map[string]any
	if err := db.Raw(fmt.Sprintf(`SELECT "deleted_at" FROM %s WHERE "id" = ?`, quoteForDrop(table)), moveID).Find(&deleted).Error; err != nil {
		t.Fatalf("读取软删列失败: %v", err)
	}
	if len(deleted) != 1 || deleted[0]["deleted_at"] == nil {
		t.Fatalf("软删行必须留在表里且 deleted_at 非空，实际 %v", deleted)
	}

	// ⑦ 关闭拖动的实体不接 /sort 的逻辑在 router_test.go；此处补一条：非法主键与条件拒绝
	c, recorder = testContext(http.MethodPost, "/x", `{"pks":["abc"]}`, nil)
	handler.Delete(c)
	if payload = decodeResponse(t, recorder); payload.Code == 0 {
		t.Fatal("非法主键必须拒绝")
	}
	c, recorder = testContext(http.MethodPost, "/x", `{"page":1,"limit":10,"sort":"nonexistent"}`, nil)
	handler.List(c)
	if payload = decodeResponse(t, recorder); payload.Code == 0 {
		t.Fatal("白名单外的排序字段必须拒绝")
	}
}

// TestDatabaseResolveMetadata Resolve 的库级断言（需要 admin_plugin_entities：框架数据库迁移；
// 缺失时跳过并给出提示——本用例组不替代 `aigo migrate up`）
func TestDatabaseResolveMetadata(t *testing.T) {
	db := dbSetup(t)
	ctx := context.Background()

	if !tableExists(t, db, config.Get().Database.Prefix+"admin_plugin_entities") {
		t.Skip("插件元数据表不存在（插件机制框架迁移未应用）：Resolve 的库级断言跳过")
	}

	row := entityRow(t, dbTestPlugin, "resolveprobe", manifest.Entity{
		Entity:  "resolveprobe",
		TitleZh: "解析探针",
		TitleEn: "Resolve Probe",
		Fields:  []manifest.Field{textField("title", "标题")},
	})
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("写入元数据行失败: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Exec("DELETE FROM "+quoteForDrop(config.Get().Database.Prefix+"admin_plugin_entities")+
			" WHERE plugin = ? AND name = ?", dbTestPlugin, "resolveprobe").Error; err != nil {
			t.Logf("清理元数据行失败: %v", err)
		}
		SetMetadataTTL(0)
	})

	SetMetadataTTL(time.Millisecond) // 缓存 1ms：避免与清理互相等待
	engine := New(db)
	spec, err := engine.Resolve(ctx, dbTestPlugin, "resolveprobe")
	if err != nil {
		t.Fatalf("解析元数据失败: %v", err)
	}
	if spec.Name != "resolveprobe" || spec.Entity.TitleZh != "解析探针" {
		t.Fatalf("解析结果不符: %+v", spec)
	}
	if spec.PhysicalTableName() != PhysicalEntityTable(prefixOf(), dbTestPlugin, "resolveprobe") {
		t.Fatalf("物理表名应与占位公式一致，实际 %s", spec.PhysicalTableName())
	}

	if _, err := engine.Resolve(ctx, dbTestPlugin, "missing"); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("不存在的实体应返回 ErrNotInstalled，实际 %v", err)
	}
}

// mustPlan 建表计划（DryRun 之外的最常用前置）
func mustPlan(t *testing.T, engine *Engine, spec *Spec) *Plan {
	t.Helper()
	plan, err := engine.BuildPlan(context.Background(), spec)
	if err != nil {
		t.Fatalf("生成结构计划失败: %v", err)
	}
	return plan
}

// latestID 表内最新一行的主键
func latestID(t *testing.T, db *gorm.DB, table string) int64 {
	t.Helper()
	rows := make([]map[string]any, 0, 1)
	if err := db.Table(table).Select([]string{`"id"`}).Order(`"id" DESC`).Find(&rows).Error; err != nil {
		t.Fatalf("读取最新主键失败: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("表内没有任何行")
	}
	return int64FromAny(t, rows[0]["id"])
}

// allIDs 表内全部主键（升序）
func allIDs(t *testing.T, db *gorm.DB, table string) []int64 {
	t.Helper()
	rows := make([]map[string]any, 0, 8)
	if err := db.Table(table).Select([]string{`"id"`}).Order(`"id" ASC`).Find(&rows).Error; err != nil {
		t.Fatalf("读取主键失败: %v", err)
	}
	ids := make([]int64, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, int64FromAny(t, row["id"]))
	}
	return ids
}

// int64FromAny 驱动返回的整数（pgx 对 int2/int4/int8 分别给 int16/int32/int64）
func int64FromAny(t *testing.T, value any) int64 {
	t.Helper()
	number, err := toInt64(value)
	if err != nil {
		t.Fatalf("值 %v 不是整数: %v", value, err)
	}
	return number
}
