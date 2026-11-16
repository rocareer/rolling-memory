package l2

import (
	"strings"
	"testing"

	"github.com/ai-go-hub/ai-go-admin/internal/plugin/manifest"
)

// ddl_test.go：DDL 生成（CREATE/ALTER/COMMENT/索引一次到位）、索引名长度预算与截断兜底、
// ddl_owner=migrations 的空计划、DROP 文本（全部 DryRun：只渲染不连库）。
// 自省基线与真实库的执行在 db_test.go（PLUGIN_DB=1）。

// TestColumnDefinitionByType 9 种类型 → PG 列定义（含必填/默认）
func TestColumnDefinitionByType(t *testing.T) {
	engine, _ := dryRunEngine(t, "")

	cases := []struct {
		name  string
		field manifest.Field
		want  string
	}{
		{"string", manifest.Field{Name: "title", Type: "string", Length: 128, Required: true, Default: ""},
			`"title" VARCHAR(128) NOT NULL DEFAULT ''`},
		{"string 缺省长度", manifest.Field{Name: "title", Type: "string"}, `"title" VARCHAR(255)`},
		{"text", manifest.Field{Name: "body", Type: "text"}, `"body" TEXT`},
		{"int", manifest.Field{Name: "amount", Type: "int", Required: true}, `"amount" INTEGER NOT NULL DEFAULT 0`},
		{"bigint", manifest.Field{Name: "hits", Type: "bigint"}, `"hits" BIGINT`},
		{"decimal", manifest.Field{Name: "price", Type: "decimal", Default: 1.5}, `"price" NUMERIC(12,2) DEFAULT 1.5`},
		{"bool", manifest.Field{Name: "status", Type: "bool", Required: true, Default: true}, `"status" SMALLINT NOT NULL DEFAULT 1`},
		{"date", manifest.Field{Name: "day", Type: "date"}, `"day" DATE`},
		{"datetime", manifest.Field{Name: "at", Type: "datetime"}, `"at" TIMESTAMPTZ`},
		{"enum", manifest.Field{Name: "level", Type: "enum", Length: 16, Default: "low"}, `"level" VARCHAR(16) DEFAULT 'low'`},
	}
	for _, item := range cases {
		got, err := engine.columnDefinition(item.field)
		if err != nil {
			t.Fatalf("%s: %v", item.name, err)
		}
		if got != item.want {
			t.Fatalf("%s 列定义不符：\n实际 %s\n期望 %s", item.name, got, item.want)
		}
	}
}

// TestDefaultLiteralEscapesSingleQuote 默认值里的单引号必须翻倍（DDL 无法参数化，字面量必须转义）
func TestDefaultLiteralEscapesSingleQuote(t *testing.T) {
	engine, _ := dryRunEngine(t, "")
	definition, err := engine.columnDefinition(manifest.Field{Name: "title", Type: "string", Default: "it's"})
	if err != nil {
		t.Fatalf("列定义失败: %v", err)
	}
	if !strings.Contains(definition, `DEFAULT 'it''s'`) {
		t.Fatalf("单引号应翻倍转义，实际 %s", definition)
	}
	if _, err := engine.columnDefinition(manifest.Field{Name: "price", Type: "decimal", Default: "1;DROP TABLE x"}); err == nil {
		t.Fatal("非数值的 decimal 默认值必须拒绝（字面量注入面）")
	}
}

// TestIndexNameBudgetAndTruncation命名与截断兜底：自然名 → 8 位 sha256 截断名 → 超限报错
func TestIndexNameBudgetAndTruncation(t *testing.T) {
	natural, err := indexName("", "demo", "items", "idx", "weigh")
	if err != nil || natural != "idx_plugin_demo_items_weigh" {
		t.Fatalf("自然索引名不符：%s err=%v", natural, err)
	}

	// 长名（len(名)+len(实体)=42）：自然形态 65 字节 > 63 ⇒ 退化为「截断 + 8 位 sha256」；
	// 截断形态 = 11("idx_plugin_") + 42 + 2(分隔) + 8 = 63，刚好放得下
	plugin := strings.Repeat("p", 22)
	entity := strings.Repeat("e", 20)
	truncated, err := indexName("", plugin, entity, "idx", ColumnDeletedAt)
	if err != nil {
		t.Fatalf("截断形态应可生成: %v", err)
	}
	if len(truncated) != pgIdentifierMaxBytes {
		t.Fatalf("截断形态应恰好用满 %d 字节，实际 %d（%s）", pgIdentifierMaxBytes, len(truncated), truncated)
	}
	if !strings.HasPrefix(truncated, "idx_plugin_"+plugin+"_"+entity+"_") {
		t.Fatalf("截断形态形状不符：%s", truncated)
	}
	again, _ := indexName("", plugin, entity, "idx", ColumnDeletedAt)
	if again != truncated {
		t.Fatalf("截断名必须是确定性函数：%s vs %s", truncated, again)
	}
	// 不同后缀的截断名必须不同（否则索引名碰撞）：note_trgm 的自然形态 64 字节也超限 ⇒ 同样走截断
	otherSuffix, err := indexName("", plugin, entity, "idx", "note_trgm")
	if err != nil {
		t.Fatalf("另一后缀的截断形态应可生成: %v", err)
	}
	if otherSuffix == truncated {
		t.Fatal("不同后缀的截断名不得相同（否则索引名碰撞）")
	}

	// 极端：再长一字节，连截断形态都放不下 ⇒ fail-closed（绝不让 PG 静默截断标识符）
	if _, err := indexName("", strings.Repeat("p", 23), entity, "idx", ColumnDeletedAt); err == nil {
		t.Fatal("前缀+名+实体吃掉全部余量时必须报错")
	}
}

// TestIndexStatementsShape 索引一次到位：软删部分唯一、trgm GIN、weigh、deleted_at
func TestIndexStatementsShape(t *testing.T) {
	engine, _ := dryRunEngine(t, "")
	spec := mustParse(t, "demo", "items", manifest.Entity{
		Entity:   "items",
		TitleZh:  "条目",
		TitleEn:  "Items",
		DragSort: true,
		Fields: []manifest.Field{
			textField("code", "编码", func(field *manifest.Field) {
				field.Unique = true
				field.Search = nil
				field.QuickSearch = false
				field.Length = 32
			}),
			textField("tag", "标签", func(field *manifest.Field) { field.Index = true }),
			textField("note", "备注", func(field *manifest.Field) { field.Trgm = true }),
		},
	})

	statements, err := engine.indexStatements(spec, spec.PhysicalTableName())
	if err != nil {
		t.Fatalf("索引生成失败: %v", err)
	}
	joined := make([]string, 0, len(statements))
	for _, statement := range statements {
		joined = append(joined, statement.Text)
	}
	all := strings.Join(joined, "\n")

	table := quoted(expectTable("demo", "items"))
	prefix := prefixOf()
	wants := []string{
		`CREATE UNIQUE INDEX IF NOT EXISTS ` + quoted(prefix+"uk_plugin_demo_items_code") + ` ON ` + table + ` ("code") WHERE "deleted_at" IS NULL`,
		`CREATE INDEX IF NOT EXISTS ` + quoted(prefix+"idx_plugin_demo_items_tag") + ` ON ` + table + ` ("tag")`,
		`CREATE INDEX IF NOT EXISTS ` + quoted(prefix+"idx_plugin_demo_items_note_trgm") + ` ON ` + table + ` USING GIN ("note" gin_trgm_ops)`,
		`CREATE INDEX IF NOT EXISTS ` + quoted(prefix+"idx_plugin_demo_items_weigh") + ` ON ` + table + ` ("weigh")`,
		`CREATE INDEX IF NOT EXISTS ` + quoted(prefix+"idx_plugin_demo_items_deleted_at") + ` ON ` + table + ` ("deleted_at")`,
	}
	for _, want := range wants {
		if !strings.Contains(all, want) {
			t.Fatalf("索引语句缺失：\n%s\n实际:\n%s", want, all)
		}
	}
	if strings.Contains(all, "CONCURRENTLY") {
		t.Fatal("引擎生成的索引不得含 CONCURRENTLY（迁移机制的整文件隐式事务里必报错）")
	}
}

// TestCreatePlanSQLShape 建表计划：显式列 + 引擎列 + 索引 + 注释（含 dict 的值→文案对照）
func TestCreatePlanSQLShape(t *testing.T) {
	engine, _ := dryRunEngine(t, "")
	spec := mustParse(t, "demo", "items", manifest.Entity{
		Entity:   "items",
		TitleZh:  "条目",
		TitleEn:  "Items",
		DragSort: true,
		Fields: []manifest.Field{
			textField("title", "标题", func(field *manifest.Field) { field.Required = true; field.Length = 128; field.Default = "" }),
			{Name: "status", Type: "bool", TitleZh: "状态", TitleEn: "Status", Required: true, List: true, Form: "switch",
				Dict: []manifest.Dict{{Value: 0, LabelZh: "停用", LabelEn: "Disabled"}, {Value: 1, LabelZh: "启用", LabelEn: "Enabled"}}},
		},
	})

	plan, err := engine.createPlan(spec, &Plan{Plugin: spec.Plugin, Entity: spec.Name, Table: spec.PhysicalTableName()})
	if err != nil {
		t.Fatalf("建表计划生成失败: %v", err)
	}
	if len(plan.Statements) == 0 || plan.Statements[0].Kind != KindCreateTable {
		t.Fatalf("首条语句应是 CREATE TABLE，实际 %+v", plan.Statements)
	}
	create := plan.Statements[0].Text
	for _, want := range []string{
		`CREATE TABLE IF NOT EXISTS ` + quoted(expectTable("demo", "items")),
		`"id" BIGSERIAL PRIMARY KEY`,
		`"title" VARCHAR(128) NOT NULL DEFAULT ''`,
		`"status" SMALLINT NOT NULL DEFAULT 0`,
		`"weigh" INTEGER NOT NULL DEFAULT 0`,
		`"deleted_at" TIMESTAMPTZ`,
		`"created_at" TIMESTAMPTZ NOT NULL DEFAULT now()`,
		`"updated_at" TIMESTAMPTZ NOT NULL DEFAULT now()`,
	} {
		if !strings.Contains(create, want) {
			t.Fatalf("建表语句缺 %q：\n%s", want, create)
		}
	}

	all := make([]string, 0, len(plan.Statements))
	for _, statement := range plan.Statements {
		all = append(all, statement.Text)
	}
	joined := strings.Join(all, "\n")
	if !strings.Contains(joined, `COMMENT ON TABLE `+quoted(expectTable("demo", "items"))+` IS '插件 demo 的 L2 实体 items`) {
		t.Fatalf("表注释应带归属信息（审计留痕）：\n%s", joined)
	}
	if !strings.Contains(joined, `COMMENT ON COLUMN `+quoted(expectTable("demo", "items"))+`."status" IS '状态:0=停用,1=启用'`) {
		t.Fatalf("状态列注释应带 dict 对照（与宿主状态列口径一致）：\n%s", joined)
	}
	if !strings.Contains(joined, `COMMENT ON COLUMN `+quoted(expectTable("demo", "items"))+`."deleted_at" IS '软删时间`) {
		t.Fatalf("软删列注释应说明语义：\n%s", joined)
	}
}

// TestBuildPlanMigrationsOwnerEmpty ddl_owner=migrations ⇒ 引擎不做任何结构变更
func TestBuildPlanMigrationsOwnerEmpty(t *testing.T) {
	engine, _ := dryRunEngine(t, "")
	spec := mustParse(t, "demo", "items", manifest.Entity{
		Entity:   "items",
		TitleZh:  "条目",
		TitleEn:  "Items",
		DdlOwner: "migrations",
		Fields:   []manifest.Field{textField("title", "标题")},
	})

	plan, err := engine.BuildPlan(t.Context(), spec)
	if err != nil {
		t.Fatalf("migrations 归属的计划生成不应报错: %v", err)
	}
	if len(plan.Statements) != 0 {
		t.Fatalf("migrations 归属时计划必须为空，实际 %+v", plan.Statements)
	}
	if len(plan.Warnings) == 0 || !strings.Contains(strings.Join(plan.Warnings, " "), "migrations") {
		t.Fatalf("应留下「迁移自管」的说明，实际 %v", plan.Warnings)
	}
	if plan.DDLOwner != "migrations" {
		t.Fatalf("计划应记录 ddl_owner，实际 %s", plan.DDLOwner)
	}
}

// TestEngineOwnedPlanDryRunBuildPlan 表缺失路径（DryRun 下自省无行 ⇒ 视为新表）走建表计划
func TestEngineOwnedPlanDryRunBuildPlan(t *testing.T) {
	engine, _ := dryRunEngine(t, "")
	spec := mustParse(t, "demo", "items", manifest.Entity{
		Entity:  "items",
		TitleZh: "条目",
		TitleEn: "Items",
		Fields:  []manifest.Field{textField("title", "标题")},
	})
	plan, err := engine.BuildPlan(t.Context(), spec)
	if err != nil {
		t.Fatalf("建表计划生成失败: %v", err)
	}
	if plan.Existing {
		t.Fatal("DryRun 下自省无行，应视为新表")
	}
	if len(plan.Statements) == 0 || plan.Statements[0].Kind != KindCreateTable {
		t.Fatalf("应产出建表语句，实际 %+v", plan.Statements)
	}
}

// TestDropTableSQLText --purge 的删表语句（引用标识符；表名先过白名单）
func TestDropTableSQLText(t *testing.T) {
	engine, capture := dryRunEngine(t, "")
	spec := mustParse(t, "demo", "items", manifest.Entity{
		Entity:  "items",
		TitleZh: "条目",
		TitleEn: "Items",
		Fields:  []manifest.Field{textField("title", "标题")},
	})
	if err := engine.DropTable(t.Context(), spec); err != nil {
		t.Fatalf("DryRun 下删表语句应可渲染: %v", err)
	}

	sqls := capture.all()
	if len(sqls) == 0 || !strings.Contains(sqls[len(sqls)-1], `DROP TABLE IF EXISTS `+quoted(expectTable("demo", "items"))) {
		t.Fatalf("删表语句形状不符，实际 %v", sqls)
	}

	poisoned := *spec
	poisoned.PhysicalTable = `plugin_demo_items"; DROP TABLE x --`
	if err := engine.DropTable(t.Context(), &poisoned); err == nil {
		t.Fatal("非法表名必须拒绝")
	}
}

// TestDropTableRefusesMigrationsOwner 纵深守卫：ddl_owner=migrations 的表由插件迁移自管，
// DropTable 自己就该拒绝（不依赖 cli 卸载编排的那道检查），且拒绝发生在渲染 DROP 语句之前。
func TestDropTableRefusesMigrationsOwner(t *testing.T) {
	engine, capture := dryRunEngine(t, "")
	spec := mustParse(t, "demo", "items", manifest.Entity{
		Entity:   "items",
		TitleZh:  "条目",
		TitleEn:  "Items",
		DdlOwner: "migrations",
		Fields:   []manifest.Field{textField("title", "标题")},
	})
	err := engine.DropTable(t.Context(), spec)
	if err == nil || !strings.Contains(err.Error(), "ddl_owner=migrations") {
		t.Fatalf("migrations 自管的表必须被 DropTable 拒绝并点名 ddl_owner，实际 %v", err)
	}
	if sqls := capture.all(); len(sqls) != 0 {
		t.Fatalf("拒绝时不得渲染任何 DROP 语句，实际 %v", sqls)
	}
}

// TestApplyPlanGuard 逐条执行、失败即停（DryRun 下 Exec 不执行，故用 nil 引擎验证守护分支）；
// 以及**空计划（nil）**这一调用契约违例：必须返回可读错误而不是空指针 panic
func TestApplyPlanGuard(t *testing.T) {
	nilEngine := New(nil)
	if _, err := nilEngine.BuildPlan(t.Context(), &Spec{Plugin: "demo", Name: "items"}); err == nil {
		t.Fatal("未接入数据库时 BuildPlan 应返回可读错误")
	}
	if _, err := nilEngine.ApplyPlan(t.Context(), &Plan{}); err == nil {
		t.Fatal("未接入数据库时 ApplyPlan 应返回可读错误")
	}

	dryEngine, _ := dryRunEngine(t, "")
	if _, err := dryEngine.ApplyPlan(t.Context(), nil); err == nil {
		t.Fatal("nil 计划应返回可读错误（调用契约违例），而不是空指针 panic")
	} else if !strings.Contains(err.Error(), "结构计划为空") {
		t.Fatalf("nil 计划的错误应点名「结构计划为空」，实际 %v", err)
	}
}

// upgradeFixture 既有表差异用例的公共夹具：spec（title + remark）与自省基线（含/不含遗留列）
func upgradeFixture(t *testing.T) (*Engine, *Spec) {
	t.Helper()
	engine, _ := dryRunEngine(t, "")
	spec := mustParse(t, "demo", "items", manifest.Entity{
		Entity:  "items",
		TitleZh: "条目",
		TitleEn: "Items",
		Fields: []manifest.Field{
			{Name: "title", Type: "string", TitleZh: "标题", TitleEn: "Title", Length: 128, Required: true, Default: "", List: true, Form: "input"},
			{Name: "remark", Type: "string", TitleZh: "备注", TitleEn: "Remark", Length: 64, Form: "input"},
		},
	})
	return engine, spec
}

// upgradeState 自省基线：id/title(128)/时间列；legacy 非空时额外带一列「表里有而 spec 未声明」的遗留列
func upgradeState(legacy bool) *schemaState {
	state := &schemaState{
		existing: true,
		columns: map[string]columnState{
			"id":         {Name: "id", DataType: "bigint"},
			"title":      {Name: "title", DataType: "character varying", MaxLength: ptrInt64(128), Nullable: "NO"},
			"created_at": {Name: "created_at", DataType: "timestamp with time zone"},
			"updated_at": {Name: "updated_at", DataType: "timestamp with time zone"},
			"deleted_at": {Name: "deleted_at", DataType: "timestamp with time zone"},
		},
		order:   []string{"id", "title", "created_at", "updated_at", "deleted_at"},
		indexes: map[string]struct{}{},
	}
	if legacy {
		state.columns["legacy"] = columnState{Name: "legacy", DataType: "integer"}
		state.order = append(state.order, "legacy")
	}
	return state
}

// newPlanFor 造一张空白计划（upgradePlan 的 table 前置）
func newPlanFor(spec *Spec) *Plan {
	return &Plan{Plugin: spec.Plugin, Entity: spec.Name, Table: spec.PhysicalTableName()}
}

// TestUpgradePlanRules 既有表的差异规则（用假自省结果驱动，不连库，允许/拒绝清单）：
// 缺列 ⇒ ADD COLUMN；「旧列消失 + 新列出现」⇒ 疑似改列名拒绝；类型/长度变更（两个方向）⇒ 拒绝；
// spec 未声明 default 的新列 ⇒ ADD COLUMN 后跟随 DROP DEFAULT（放宽类变更）；未声明列 ⇒ 保留只报告。
func TestUpgradePlanRules(t *testing.T) {
	engine, spec := upgradeFixture(t)
	table := quoted(expectTable("demo", "items"))

	// ① 缺 remark 列、无遗留列：ADD COLUMN + 该列注释（remark 非必填且未声明 default ⇒ 不带默认、无收尾）
	plan, err := engine.upgradePlan(spec, newPlanFor(spec), upgradeState(false))
	if err != nil {
		t.Fatalf("加列路径不应报错: %v", err)
	}
	joined := joinStatements(plan)
	if !strings.Contains(joined, `ALTER TABLE `+table+` ADD COLUMN IF NOT EXISTS "remark" VARCHAR(64)`) {
		t.Fatalf("应产出加列语句：\n%s", joined)
	}
	if !strings.Contains(joined, `COMMENT ON COLUMN `+table+`."remark"`) {
		t.Fatalf("新列应同时补注释：\n%s", joined)
	}
	if strings.Contains(joined, "DROP") {
		t.Fatalf("非必填新列不带默认 ⇒ 不应有 DROP DEFAULT：\n%s", joined)
	}
	if len(plan.KeptColumns) != 0 {
		t.Fatalf("无遗留列时保留清单应为空，实际 %v", plan.KeptColumns)
	}

	// ② 「旧列消失 + 新列出现」⇒ 点名疑似改列名并拒绝（改列名拒绝；两侧名字都要在理由里）
	renamed, err := engine.upgradePlan(spec, newPlanFor(spec), upgradeState(true))
	if err == nil {
		t.Fatalf("遗留列与新列并存应判为疑似改列名并拒绝（计划：\n%s）", joinStatements(renamed))
	}
	msg := err.Error()
	if !strings.Contains(msg, "疑似改列名") || !strings.Contains(msg, "legacy") || !strings.Contains(msg, "remark") {
		t.Fatalf("拒绝理由应点名疑似改名（两侧列名都要在），实际 %v", err)
	}
	if !strings.Contains(strings.Join(renamed.KeptColumns, ","), "legacy") {
		t.Fatalf("被拒的计划仍应带上保留列读数（供人定位），实际 %v", renamed.KeptColumns)
	}

	// ③ 纯遗留列（无新增列）⇒ 只保留 + 报告，不拒绝（保守口径：绝不自动 DROP 列）
	keepSpec := mustParse(t, "demo", "items", manifest.Entity{
		Entity:  "items",
		TitleZh: "条目",
		TitleEn: "Items",
		Fields: []manifest.Field{
			{Name: "title", Type: "string", TitleZh: "标题", TitleEn: "Title", Length: 128, Required: true, Default: "", List: true, Form: "input"},
		},
	})
	plan, err = engine.upgradePlan(keepSpec, newPlanFor(keepSpec), upgradeState(true))
	if err != nil {
		t.Fatalf("只有遗留列时不应报错: %v", err)
	}
	if len(plan.KeptColumns) != 1 || plan.KeptColumns[0] != "legacy" {
		t.Fatalf("表里存在而 spec 未声明的列应进保留清单，实际 %v", plan.KeptColumns)
	}
	if !strings.Contains(strings.Join(plan.Warnings, " "), "legacy") {
		t.Fatalf("保留列应在报告里点名，实际 %v", plan.Warnings)
	}
	if strings.Contains(joinStatements(plan), "DROP") {
		t.Fatalf("绝不对遗留列发 DROP（只报告）：\n%s", joinStatements(plan))
	}

	// ④ 类型不符 ⇒ 拒绝并提示改用迁移文件
	stateType := upgradeState(false)
	stateType.columns["title"] = columnState{Name: "title", DataType: "text", Nullable: "NO"}
	if _, err = engine.upgradePlan(spec, newPlanFor(spec), stateType); err == nil {
		t.Fatal("改类型必须拒绝")
	} else if !strings.Contains(err.Error(), "迁移文件") {
		t.Fatalf("拒绝理由应指向迁移文件，实际 %v", err)
	}

	// ⑤ varchar 长度：放大与收窄都拒绝（长度变更在 DDL 层是 ALTER COLUMN TYPE，归入改类型拒绝；
	// 引擎对既有列只发 ADD COLUMN 与 DROP DEFAULT）
	for _, length := range []int64{32, 255} {
		state := upgradeState(false)
		state.columns["title"] = columnState{Name: "title", DataType: "character varying", MaxLength: ptrInt64(length), Nullable: "NO"}
		plan, err := engine.upgradePlan(spec, newPlanFor(spec), state)
		if err == nil {
			t.Fatalf("varchar 长度由 %d 变更必须拒绝（计划：\n%s）", length, joinStatements(plan))
		}
		if !strings.Contains(err.Error(), "迁移文件") {
			t.Fatalf("长度变更的拒绝理由应指向迁移文件，实际 %v", err)
		}
		if strings.Contains(joinStatements(plan), "ALTER COLUMN") {
			t.Fatalf("长度变更不得落到 ALTER COLUMN TYPE 执行路径（已按设计回退为拒绝）：\n%s", joinStatements(plan))
		}
	}
	if _, err := engine.upgradePlan(spec, newPlanFor(spec), upgradeState(false)); err != nil {
		// 长度相等（spec 128 = 表 128）⇒ 不报错：确认上面的拒绝来自长度差异而不是固定报错
		t.Fatalf("长度一致时不应报错: %v", err)
	}
}

// TestUpgradePlanAddColumnDropDefault 放宽类变更（升级示例两条语句）：
// spec 未声明 default 的**必填**新列 ⇒ ADD COLUMN 带类型零值默认（非空表落 NOT NULL 的前提），
// 随后 DROP DEFAULT 收尾（表结构对齐 spec 目标态，不触碰既有数据）；显式声明 default 的字段不受影响。
func TestUpgradePlanAddColumnDropDefault(t *testing.T) {
	engine, _ := dryRunEngine(t, "")
	spec := mustParse(t, "demo", "items", manifest.Entity{
		Entity:  "items",
		TitleZh: "条目",
		TitleEn: "Items",
		Fields: []manifest.Field{
			{Name: "title", Type: "string", TitleZh: "标题", TitleEn: "Title", Length: 128, Required: true, Default: "", List: true, Form: "input"},
			{Name: "note", Type: "string", TitleZh: "说明", TitleEn: "Note", Length: 64, Required: true, Form: "input"},
			{Name: "code", Type: "string", TitleZh: "编码", TitleEn: "Code", Length: 32, Required: true, Default: "", Form: "input"},
		},
	})
	state := upgradeState(false)
	plan, err := engine.upgradePlan(spec, newPlanFor(spec), state)
	if err != nil {
		t.Fatalf("加列路径不应报错: %v", err)
	}
	table := quoted(expectTable("demo", "items"))
	joined := joinStatements(plan)
	if !strings.Contains(joined, `ADD COLUMN IF NOT EXISTS "note" VARCHAR(64) NOT NULL DEFAULT ''`) {
		t.Fatalf("必填新列应带类型零值默认：\n%s", joined)
	}
	if !strings.Contains(joined, `ALTER TABLE `+table+` ALTER COLUMN "note" DROP DEFAULT`) {
		t.Fatalf("spec 未声明 default ⇒ 应跟随一条 DROP DEFAULT（放宽类变更）：\n%s", joined)
	}
	if !strings.Contains(strings.Join(plan.Warnings, " "), "DROP DEFAULT") {
		t.Fatalf("DROP DEFAULT 应在报告里说明依据，实际 %v", plan.Warnings)
	}
	// 显式声明 default 的字段：ADD COLUMN 的默认是目标态的一部分，不得被 DROP 掉
	if strings.Contains(joined, `ALTER COLUMN "code" DROP DEFAULT`) {
		t.Fatalf("显式声明 default 的字段不得被 DROP DEFAULT：\n%s", joined)
	}
	if !strings.Contains(joined, `ADD COLUMN IF NOT EXISTS "code"`) {
		t.Fatalf("缺列的字段仍应产出 ADD COLUMN：\n%s", joined)
	}
}

// TestUpgradePlanIndexReverse 索引面：已有索引不重复产出 + 反向对齐（「允许 加/删索引」）
func TestUpgradePlanIndexReverse(t *testing.T) {
	engine, spec := upgradeFixture(t)

	// ① 已存在的索引不重复产出（自省里有 ⇒ 计划里没有）
	state := upgradeState(false)
	state.indexes[prefixOf()+"idx_plugin_demo_items_deleted_at"] = struct{}{}
	plan, err := engine.upgradePlan(spec, newPlanFor(spec), state)
	if err != nil {
		t.Fatalf("重复应用不应报错: %v", err)
	}
	if strings.Contains(joinStatements(plan), "deleted_at") {
		t.Fatalf("索引已存在时计划里不应再出现该索引：\n%s", joinStatements(plan))
	}

	// ② 反向对齐：表上残留的**引擎命名**索引而已不在 spec 内 ⇒ DROP INDEX；
	// 非引擎命名的索引（手写 / 插件迁移创建 / 主键）一律不动（不猜归属）
	state.indexes[prefixOf()+"idx_plugin_demo_items_title"] = struct{}{}
	state.indexes[prefixOf()+"uk_plugin_demo_items_title"] = struct{}{}
	state.indexes["manual_index_on_items"] = struct{}{}
	state.indexes["plugin_demo_items_pkey"] = struct{}{}
	plan, err = engine.upgradePlan(spec, newPlanFor(spec), state)
	if err != nil {
		t.Fatalf("删索引路径不应报错: %v", err)
	}
	joined := joinStatements(plan)
	for _, want := range []string{
		`DROP INDEX IF EXISTS ` + quoted(prefixOf()+"idx_plugin_demo_items_title"),
		`DROP INDEX IF EXISTS ` + quoted(prefixOf()+"uk_plugin_demo_items_title"),
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("spec 不再声明的引擎命名索引应产出 %q：\n%s", want, joined)
		}
	}
	if !strings.Contains(joined, `DROP INDEX IF EXISTS `) {
		t.Fatalf("引擎命名索引的反向对齐应产出 DROP INDEX：\n%s", joined)
	}
	for _, kept := range []string{"manual_index_on_items", "plugin_demo_items_pkey"} {
		if strings.Contains(joined, kept) {
			t.Fatalf("非引擎命名的索引不得被自动删除（%s）：\n%s", kept, joined)
		}
	}
	if !strings.Contains(strings.Join(plan.Warnings, " "), "已不在 spec 的声明内") {
		t.Fatalf("删索引应在报告里说明依据，实际 %v", plan.Warnings)
	}
	// 计划必须确定性（索引名来自 map，产出前排序）
	again, err := engine.upgradePlan(spec, newPlanFor(spec), state)
	if err != nil {
		t.Fatalf("重复生成计划不应报错: %v", err)
	}
	if joinStatements(again) != joined {
		t.Fatalf("同一输入的计划必须逐字一致（确定性）：\n%s\n---\n%s", joined, joinStatements(again))
	}
}

// TestIntrospectSQLUsesParamTable 自省语句的表名走参数（不拼进 SQL 文本）
func TestIntrospectSQLUsesParamTable(t *testing.T) {
	engine, capture := dryRunEngine(t, "")
	const probeTable = "l2_introspect_probe"
	if _, err := engine.introspect(t.Context(), probeTable); err != nil {
		t.Fatalf("DryRun 自省不应报错: %v", err)
	}
	sqls := capture.all()
	if len(sqls) == 0 {
		t.Fatal("自省应渲染查询")
	}
	joined := strings.Join(sqls, "\n")
	for _, want := range []string{
		"information_schema.columns",
		"pg_indexes",
		"table_name = $1",
		"tablename = $1",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("自省语句应含 %q（值走位置占位符）：\n%s", want, joined)
		}
	}
	if strings.Contains(joined, probeTable) {
		t.Fatalf("表名应走参数而不是拼进 SQL 文本：\n%s", joined)
	}
}

func ptrInt64(value int64) *int64 { return &value }

func joinStatements(plan *Plan) string {
	texts := make([]string, 0, len(plan.Statements))
	for _, statement := range plan.Statements {
		texts = append(texts, statement.Text)
	}
	return strings.Join(texts, "\n")
}
