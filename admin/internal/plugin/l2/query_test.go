package l2

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/ai-go-hub/ai-go-admin/internal/plugin/manifest"
	"github.com/ai-go-hub/ai-go-admin/internal/service"

	"gorm.io/gorm"
)

// query_test.go：SQL 构造的四类注入负例（字段名 / 排序 / 操作符 / 表名）、词表一致性、
// 前缀匹配语义、分页上限、以及GORM 前缀探针（断言 db.Table() 的实际 SQL 文本）。

// demoSpec 用例样板：drag_sort 的实体，含文本列（可模糊搜索）、数值列、二值列
func demoSpec(t *testing.T) *Spec {
	t.Helper()
	return mustParse(t, "demo", "items", manifest.Entity{
		Entity:   "items",
		TitleZh:  "条目",
		TitleEn:  "Items",
		DragSort: true,
		Fields: []manifest.Field{
			textField("title", "标题", func(field *manifest.Field) { field.Sort = true }),
			{Name: "amount", Type: "int", TitleZh: "数量", TitleEn: "Amount", List: true, Form: "input-number",
				Search: &manifest.Search{Op: "egt"}},
			{Name: "status", Type: "bool", TitleZh: "状态", TitleEn: "Status", List: true, Form: "switch",
				Search: &manifest.Search{Op: "eq"}},
		},
	})
}

// TestInjectFieldNameRejected 一类：字段名（不在条件白名单 / 含注入载荷）
func TestInjectFieldNameRejected(t *testing.T) {
	engine, _ := dryRunEngine(t, "")
	spec := demoSpec(t)

	for _, field := range []string{
		"nonexistent",
		`title" OR 1=1 --`,
		"title; DROP TABLE admin_plugins",
		"deleted_at",
		"id",
	} {
		_, _, err := engine.whereClause(spec, []service.WhereGroup{{Wheres: []service.Where{
			{Field: field, Operator: "eq", Value: "x"},
		}}})
		if err == nil {
			t.Fatalf("字段 %q 不在条件白名单内，必须拒绝（且绝不进入标识符位置）", field)
		}
	}
}

// TestInjectSortRejected 二类：排序（白名单外拒绝，不静默跳过）
func TestInjectSortRejected(t *testing.T) {
	engine, _ := dryRunEngine(t, "")
	spec := demoSpec(t)

	for _, field := range []string{
		"nonexistent",
		"amount", // 未声明 sort:true
		`"id" DESC; DROP TABLE admin_plugins --`,
		"id) --",
	} {
		if _, err := engine.buildOrder(spec, field, "desc"); err == nil {
			t.Fatalf("排序字段 %q 不在白名单内，必须拒绝", field)
		}
	}

	order, err := engine.buildOrder(spec, "title", "desc")
	if err != nil {
		t.Fatalf("白名单内的排序不应报错: %v", err)
	}
	if order != `"title" DESC, "id" DESC` {
		t.Fatalf("排序片段应为「用户字段 + 主键托底」，实际 %q", order)
	}
	order, err = engine.buildOrder(spec, "", "")
	if err != nil {
		t.Fatalf("无排序字段时应只留主键托底: %v", err)
	}
	if order != `"id" DESC` {
		t.Fatalf("无排序字段时只剩主键托底，实际 %q", order)
	}
}

// TestInjectOperatorRejected 三类：操作符（词表外一律拒绝；含 gte / like 小写）
func TestInjectOperatorRejected(t *testing.T) {
	engine, _ := dryRunEngine(t, "")
	spec := demoSpec(t)

	for _, op := range []string{"gte", "like", "ild", "ILIKE ", "BETWEEEN"} {
		_, _, err := engine.whereClause(spec, []service.WhereGroup{{Wheres: []service.Where{
			{Field: "title", Operator: op, Value: "x"},
		}}})
		if err == nil {
			t.Fatalf("操作符 %q 不在词表内，必须拒绝", op)
		}
	}

	// IN 在词表内：数组形态可用（值为参数，不拼接）
	condition, args, err := engine.whereClause(spec, []service.WhereGroup{{Wheres: []service.Where{
		{Field: "title", Operator: "IN", Value: []any{"a", "b"}},
	}}})
	if err != nil {
		t.Fatalf("词表内的 IN 应可用: %v", err)
	}
	// 软删过滤是 `IS NULL`（无占位符）⇒ 参数只有 IN 的两个值
	if !strings.Contains(condition, "IN (?, ?)") || len(args) != 2 {
		t.Fatalf("IN 应展开为位置占位符，实际条件 %q 参数 %v", condition, args)
	}
}

// TestInjectTableNameRejected 四类：表名（元数据里的物理名与引擎推导名都过字符集白名单）
func TestInjectTableNameRejected(t *testing.T) {
	engine, _ := dryRunEngine(t, "")
	spec := demoSpec(t)

	for _, table := range []string{
		`plugin_demo_items"; DROP TABLE admin_plugins --`,
		"plugin_demo_items OR 1=1",
		`plugin_demo_items"`,
		"",
	} {
		poisoned := *spec
		poisoned.PhysicalTable = table
		if _, err := engine.quoteTable(table); err == nil && table != "" {
			t.Fatalf("表名 %q 不在白名单内，必须拒绝", table)
		}
		if _, err := engine.tableName(&poisoned); err == nil && table != "" {
			t.Fatalf("表名 %q 不在白名单内，必须拒绝", table)
		}
	}
	// 空表名回退到引擎占位公式（合法）
	fallback := *spec
	fallback.PhysicalTable = ""
	name, err := engine.tableName(&fallback)
	if err != nil || name != expectTable("demo", "items") {
		t.Fatalf("空表名应回退为 <前缀>plugin_<名>_<实体>，实际 %q err=%v", name, err)
	}
}

// TestValuesAreParameterized 值一律参数化：注入载荷只出现在参数里，不进 SQL 文本
func TestValuesAreParameterized(t *testing.T) {
	engine, _ := dryRunEngine(t, "")
	spec := demoSpec(t)
	payload := `x'; DROP TABLE admin_plugins; --`

	condition, args, err := engine.whereClause(spec, []service.WhereGroup{{Wheres: []service.Where{
		{Field: "title", Operator: "eq", Value: payload},
	}}})
	if err != nil {
		t.Fatalf("合法条件不应报错: %v", err)
	}
	if strings.Contains(condition, payload) || strings.Contains(condition, "DROP TABLE") {
		t.Fatalf("值不得进入 SQL 文本，实际条件 %q", condition)
	}
	if len(args) != 1 || args[0] != payload {
		t.Fatalf("值应以参数形态传递，实际参数 %v", args)
	}

	// 渲染成完整 SQL：载荷进 Vars，SQL 文本里只有 $1
	sql, vars := dryRunQuery(t, engine.DB(), func(tx *gorm.DB) *gorm.DB {
		return tx.Table(spec.PhysicalTableName()).Select([]string{`"id"`}).Where(condition, args...)
	})
	if strings.Contains(sql, "DROP TABLE") {
		t.Fatalf("SQL 文本里不得出现注入载荷: %s", sql)
	}
	if !strings.Contains(sql, "$1") {
		t.Fatalf("值应为位置占位符（$1），实际 SQL: %s", sql)
	}
	if len(vars) != 1 || vars[0] != payload {
		t.Fatalf("参数应原样传入驱动，实际 %v", vars)
	}
}

// TestFuzzyPrefixSemantics 模糊搜索 = 前缀匹配；声明 trgm 才前后通配（显式规定）
func TestFuzzyPrefixSemantics(t *testing.T) {
	engine, _ := dryRunEngine(t, "")

	plain := mustParse(t, "demo", "items", manifest.Entity{
		Entity:  "items",
		TitleZh: "条目",
		TitleEn: "Items",
		Fields:  []manifest.Field{textField("title", "标题")},
	})
	_, args, err := engine.whereClause(plain, []service.WhereGroup{{Wheres: []service.Where{
		{Field: "title", Operator: "ILIKE", Value: "  kw  "},
	}}})
	if err != nil {
		t.Fatalf("ILIKE 不应报错: %v", err)
	}
	if args[0] != "kw%" {
		t.Fatalf("默认应是前缀匹配（kw%%），实际 %v", args[0])
	}

	trgm := mustParse(t, "demo", "items", manifest.Entity{
		Entity:  "items",
		TitleZh: "条目",
		TitleEn: "Items",
		Fields:  []manifest.Field{textField("title", "标题", func(field *manifest.Field) { field.Trgm = true })},
	})
	_, args, err = engine.whereClause(trgm, []service.WhereGroup{{Wheres: []service.Where{
		{Field: "title", Operator: "NOT ILIKE", Value: "kw"},
	}}})
	if err != nil {
		t.Fatalf("NOT ILIKE 不应报错: %v", err)
	}
	if args[0] != "%kw%" {
		t.Fatalf("声明 trgm 的字段应前后通配，实际 %v", args[0])
	}

	// NULL / NOT NULL 不需要值（词表内的这两项合法且不消费参数）
	condition, args, err := engine.whereClause(plain, []service.WhereGroup{{Wheres: []service.Where{
		{Field: "title", Operator: "NULL"},
	}}})
	if err != nil {
		t.Fatalf("NULL 操作符应合法: %v", err)
	}
	if !strings.Contains(condition, `"title" IS NULL`) || len(args) != 0 {
		t.Fatalf("NULL 应渲染为 IS NULL 且无参数，实际 %q / %v", condition, args)
	}
}

// TestListSQLShapeAndPaginationCap 列表 SQL 形状：显式列（不 SELECT *）、软删过滤、排序托底、分页上限 1000
func TestListSQLShapeAndPaginationCap(t *testing.T) {
	engine, capture := dryRunEngine(t, "")
	spec := demoSpec(t)
	handler := entityFor(engine, spec)

	c, recorder := testContext("POST", "/admin/plugin/demo/items/list", `{"page":1,"limit":5000,"sort":"title","order":"desc"}`, nil)
	handler.List(c)
	payload := decodeResponse(t, recorder)
	if payload.Code != 0 {
		t.Fatalf("列表应成功（DryRun 下返回空集），实际 code=%d message=%s", payload.Code, payload.Message)
	}
	if _, ok := payload.Data["meta"]; !ok {
		t.Fatalf("L2 列表响应必须带元数据，实际 %v", payload.Data)
	}
	for _, key := range []string{"list", "total", "has_more"} {
		if _, ok := payload.Data[key]; !ok {
			t.Fatalf("列表响应缺 %s: %v", key, payload.Data)
		}
	}

	sqls := capture.all()
	var listSQL, countSQL string
	for _, sql := range sqls {
		if strings.Contains(sql, "SELECT count(*)") {
			countSQL = sql
			continue
		}
		if strings.Contains(sql, "FROM ") {
			listSQL = sql
		}
	}
	if listSQL == "" || countSQL == "" {
		t.Fatalf("应渲染列表与计数两条 SQL，实际 %v", sqls)
	}
	for _, want := range []string{
		`SELECT "id","title","amount","status","weigh","created_at"`,
		`FROM ` + quoted(expectTable("demo", "items")),
		`"deleted_at" IS NULL`,
		`ORDER BY "title" DESC, "id" DESC`,
		"LIMIT $1", // 分页走参数（取值断言见下）
	} {
		if !strings.Contains(listSQL, want) {
			t.Fatalf("列表 SQL 缺 %q\n实际: %s", want, listSQL)
		}
	}
	if strings.Contains(listSQL, "SELECT *") {
		t.Fatalf("列表不得 SELECT *（SQL 纪律）：%s", listSQL)
	}

	// 分页上限 1000（硬条款 ③：复用 service.BuildPaginateScope / maxPageLimit）：
	// 值以参数形态下发，故从 Statement.Vars 读（limit 是 int，与常量直接可比）
	_, vars := dryRunQuery(t, engine.DB(), func(tx *gorm.DB) *gorm.DB {
		return tx.Table(spec.PhysicalTableName()).Select([]string{`"id"`}).Scopes(paginateScope(1, 5000))
	})
	if len(vars) != 1 || vars[0] != maxPageLimit {
		t.Fatalf("limit=5000 应收敛为 maxPageLimit（%d），实际 Vars=%v", maxPageLimit, vars)
	}
	// handler 路径的收敛（handler.go:162 用它计算 LIMIT/OFFSET 与 has_more）：同一个包内常量，
	// 保证「列表实际下发到 SQL 的 limit」与上一条断言（共享 scope）停在同一条线上
	if page, limit := normalizePaginate(1, 5000); page != 1 || limit != maxPageLimit {
		t.Fatalf("handler 路径的 limit=5000 应收敛为 page=%d limit=%d，实际 page=%d limit=%d",
			1, maxPageLimit, page, limit)
	}
}

// TestTablePrefixProbe探针：断言 db.Table() 的实际 SQL 文本。
// 结论（本项目源码核对 + 本探针）：GORM 的 NamingStrategy.TablePrefix 只在 model 解析期生效，
// Table(字符串) 按原样引用 ⇒ 前缀必须由引擎自己拼（本引擎用元数据里存的物理名）。
func TestTablePrefixProbe(t *testing.T) {
	db, _ := dryRunDB(t, "pre_")
	sql, _ := dryRunQuery(t, db, func(tx *gorm.DB) *gorm.DB {
		return tx.Table("plugin_demo_items").Select([]string{`"id"`}).Where(`"deleted_at" IS NULL`)
	})
	if !strings.Contains(sql, `FROM "plugin_demo_items"`) {
		t.Fatalf("db.Table() 应按原样引用表名，实际 SQL: %s", sql)
	}
	if strings.Contains(sql, "pre_plugin_demo_items") {
		t.Fatalf("db.Table() 不应吃 NamingStrategy.TablePrefix（否则引擎会双前缀），实际 SQL: %s", sql)
	}

	// 对照组：由 model 解析出的表名**会**吃前缀（证明差异来自 Table() 的原样引用，而不是前缀没配上）
	type tablePrefixProbeUser struct{}
	{
		rows := make([]map[string]any, 0, 1)
		tx := db.Session(&gorm.Session{DryRun: true}).Model(&tablePrefixProbeUser{}).Find(&rows)
		if tx.Error != nil {
			t.Fatalf("渲染失败: %v", tx.Error)
		}
		if !strings.Contains(tx.Statement.SQL.String(), `"pre_table_prefix_probe_users"`) {
			t.Fatalf("model 解析出的表名应带 NamingStrategy 前缀，实际: %s", tx.Statement.SQL.String())
		}
	}
}

// TestOperatorVocabularyParityWithHost 词表 = 宿主 GetOperatorByAlias 的映射域（16 项一个不少）
func TestOperatorVocabularyParityWithHost(t *testing.T) {
	for _, op := range manifest.SearchOperators {
		if service.GetOperatorByAlias(op) == "" && op != "" {
			t.Fatalf("操作符 %q 在宿主映射表里没有对应 SQL 运算符：词表与宿主已漂移", op)
		}
	}
	// 词表外的写法宿主会返回空串（条件被静默丢弃）——这正是 L2 必须拒装的原因
	for _, op := range []string{"gte", "like", "ild"} {
		if service.GetOperatorByAlias(op) != "" {
			t.Fatalf("探针失效：宿主对 %q 竟有映射", op)
		}
	}
}

// TestOperatorVocabularyMatchesFrontendUnion 词表 = web/types/table.d.ts 的 OperatorStr 原文（16 项逐字一致）
func TestOperatorVocabularyMatchesFrontendUnion(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Skip("无法定位测试文件路径")
	}
	path := filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "web", "types", "table.d.ts")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("读不到前端类型定义（%s），跳过：%v", path, err)
	}
	block := regexp.MustCompile(`(?s)type OperatorStr\s*=(.*?)\n\s*\n`).FindStringSubmatch(string(raw))
	if block == nil {
		t.Fatalf("未能在 %s 里定位 OperatorStr 联合类型", path)
	}
	literals := regexp.MustCompile(`'([^']*)'`).FindAllStringSubmatch(block[1], -1)
	frontend := make([]string, 0, len(literals))
	for _, match := range literals {
		frontend = append(frontend, match[1])
	}
	if len(frontend) == 0 {
		t.Fatalf("OperatorStr 里没解析出任何字面量：%s", block[1])
	}
	if !slices.Equal(frontend, manifest.SearchOperators) {
		t.Fatalf("前端 OperatorStr 与 manifest.SearchOperators 不一致：\n前端: %v\n后端: %v", frontend, manifest.SearchOperators)
	}
}

// TestInValuesBounded IN/NOT IN 元素数有界（SQL 纪律：请求路径上的条件不得无界增长）
func TestInValuesBounded(t *testing.T) {
	engine, _ := dryRunEngine(t, "")
	spec := demoSpec(t)
	values := make([]any, maxInValues+1)
	for i := range values {
		values[i] = i
	}
	_, _, err := engine.whereClause(spec, []service.WhereGroup{{Wheres: []service.Where{
		{Field: "amount", Operator: "IN", Value: values},
	}}})
	if err == nil {
		t.Fatalf("IN 元素数超过 %d 必须拒绝", maxInValues)
	}
}

// TestBetweenAcceptsCommaAndArray BETWEEN 两种值形态都可用（宿主口径是逗号字符串）
func TestBetweenAcceptsCommaAndArray(t *testing.T) {
	engine, _ := dryRunEngine(t, "")
	spec := demoSpec(t)

	condition, args, err := engine.whereClause(spec, []service.WhereGroup{{Wheres: []service.Where{
		{Field: "amount", Operator: "BETWEEN", Value: "1, 9"},
	}}})
	if err != nil {
		t.Fatalf("逗号字符串形态应可用: %v", err)
	}
	// 软删过滤是 `IS NULL`（无占位符）⇒ 参数只有上下界两个
	if !strings.Contains(condition, `"amount" BETWEEN ? AND ?`) || len(args) != 2 {
		t.Fatalf("BETWEEN 应渲染两个占位符，实际 %q / %v", condition, args)
	}
	if args[0].(int64) != 1 || args[1].(int64) != 9 {
		t.Fatalf("逗号字符串形态应被收敛为整数参数，实际 %v", args)
	}

	_, args, err = engine.whereClause(spec, []service.WhereGroup{{Wheres: []service.Where{
		{Field: "amount", Operator: "BETWEEN", Value: []any{1, 9}},
	}}})
	if err != nil {
		t.Fatalf("数组形态应可用: %v", err)
	}
	if len(args) != 2 || args[0].(int64) != 1 || args[1].(int64) != 9 {
		t.Fatalf("数组形态应被收敛为整数参数，实际 %v", args)
	}
}

// TestWhereGroupOrSemantics 组内 OR / 组间 AND（与宿主 BuildWhereScopes 同语义）
func TestWhereGroupOrSemantics(t *testing.T) {
	engine, _ := dryRunEngine(t, "")
	spec := demoSpec(t)

	condition, _, err := engine.whereClause(spec, []service.WhereGroup{
		{Or: true, Wheres: []service.Where{
			{Field: "title", Operator: "ILIKE", Value: "a"},
			{Field: "amount", Operator: "gt", Value: 1},
		}},
		{Wheres: []service.Where{{Field: "status", Operator: "eq", Value: 1}}},
	})
	if err != nil {
		t.Fatalf("合法条件组不应报错: %v", err)
	}
	want := `"deleted_at" IS NULL AND ("title" ILIKE ? OR "amount" > ?) AND ("status" = ?)`
	if condition != want {
		t.Fatalf("条件片段形状不符：\n实际: %s\n期望: %s", condition, want)
	}
}

// TestNilValueRejected 条件值缺失（null）应给可读错误，而不是渲染出永不成立的条件
func TestNilValueRejected(t *testing.T) {
	engine, _ := dryRunEngine(t, "")
	spec := demoSpec(t)
	_, _, err := engine.whereClause(spec, []service.WhereGroup{{Wheres: []service.Where{
		{Field: "title", Operator: "eq", Value: nil},
	}}})
	if err == nil {
		t.Fatal("eq 的值为 null 应报错（判定为空请用 NULL 操作符）")
	}
}

// TestQuoteRejectsNonIdentifier 引用入口的负例（列名闭集）
func TestQuoteRejectsNonIdentifier(t *testing.T) {
	engine, _ := dryRunEngine(t, "")
	for _, name := range []string{"Id", "1id", "id;", `id"`, "", "id-name", "id name", "id)"} {
		if _, err := engine.quote(name); err == nil {
			t.Fatalf("标识符 %q 不在白名单内，必须拒绝", name)
		}
	}
	quoted, err := engine.quote("created_at")
	if err != nil || quoted != `"created_at"` {
		t.Fatalf("合法标识符应被引号包裹，实际 %q err=%v", quoted, err)
	}
}

// TestSoftDeleteConditionAndNilEngine 引用与条件构造不依赖可用的连接；nil 引擎给可读错误
func TestSoftDeleteConditionAndNilEngine(t *testing.T) {
	engine, _ := dryRunEngine(t, "")
	condition, err := engine.softDeleteCondition()
	if err != nil {
		t.Fatalf("软删条件构造失败: %v", err)
	}
	if condition != `"deleted_at" IS NULL` {
		t.Fatalf("软删条件形状不符：%s", condition)
	}
	nilEngine := New(nil)
	if _, err := nilEngine.quote("id"); err == nil {
		t.Fatal("未接入数据库时应返回可读错误（不 panic）")
	}
}
