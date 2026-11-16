package l2

import (
	"strings"
	"testing"

	"github.com/ai-go-hub/ai-go-admin/internal/plugin/host"
	"github.com/ai-go-hub/ai-go-admin/internal/plugin/manifest"

	"gorm.io/datatypes"
)

// spec_test.go：spec 解析与引擎增补规则的负例/正例（不连库）。
// 覆盖面：词表负例（gte / like 小写拒装；NOT ILIKE / NULL 合法）、保留列名、trgm 与类型、
// 模糊操作符与非文本类型、物理表名注入、闭集外的键、标识符长度预算。

// TestParseRejectsOutsideOperatorVocabulary 词表负例（宿主 16 项词表原文之外一律拒装）
func TestParseRejectsOutsideOperatorVocabulary(t *testing.T) {
	for _, op := range []string{"gte", "like", "ild", "ILIKE ", " NOT NULL", "==", "IN ("} {
		spec := manifest.Entity{
			Entity:  "items",
			TitleZh: "条目",
			TitleEn: "Items",
			Fields: []manifest.Field{
				textField("title", "标题", func(field *manifest.Field) {
					field.Search = &manifest.Search{Op: op}
				}),
			},
		}
		if _, err := Parse(entityRow(t, "demo", "items", spec)); err == nil {
			t.Fatalf("操作符 %q 不在闭集内，必须拒装", op)
		} else if !strings.Contains(err.Error(), "词表") {
			t.Fatalf("操作符 %q 的拒绝理由应点名词表，实际: %v", op, err)
		}
	}
}

// TestParseAcceptsEveryVocabularyWord 词表正例：16 项原文全部可装载（按类型选相容的字段类型）
func TestParseAcceptsEveryVocabularyWord(t *testing.T) {
	fieldType := func(op string) string {
		switch op {
		case "LIKE", "NOT LIKE", "ILIKE", "NOT ILIKE":
			return "string"
		case "BETWEEN", "NOT BETWEEN", "gt", "egt", "lt", "elt":
			return "int"
		default:
			return "string"
		}
	}
	if len(manifest.SearchOperators) != 16 {
		t.Fatalf("宿主词表应有 16 项，实际 %d 项", len(manifest.SearchOperators))
	}
	for _, op := range manifest.SearchOperators {
		field := manifest.Field{
			Name:    "probe",
			Type:    fieldType(op),
			TitleZh: "探针",
			TitleEn: "probe",
			List:    true,
			Search:  &manifest.Search{Op: op},
		}
		if field.Type == "string" {
			field.Length = 64
			field.Form = "input"
		} else {
			field.Form = "input-number"
		}
		spec := manifest.Entity{Entity: "probe", TitleZh: "探针", TitleEn: "Probe", Fields: []manifest.Field{field}}
		if _, err := Parse(entityRow(t, "demo", "probe", spec)); err != nil {
			t.Fatalf("操作符 %q（%s 字段）应可装载，实际失败: %v", op, field.Type, err)
		}
	}
}

// TestParseRejectsFuzzyOperatorOnNumeric 词表内但类型不相容：模糊类只用于文本（否则 PG 运行期报错）
func TestParseRejectsFuzzyOperatorOnNumeric(t *testing.T) {
	for _, op := range []string{"LIKE", "NOT LIKE", "ILIKE", "NOT ILIKE"} {
		spec := manifest.Entity{
			Entity:  "items",
			TitleZh: "条目",
			TitleEn: "Items",
			Fields: []manifest.Field{{
				Name: "amount", Type: "int", TitleZh: "数量", TitleEn: "Amount",
				List: true, Form: "input-number", Search: &manifest.Search{Op: op},
			}},
		}
		if _, err := Parse(entityRow(t, "demo", "items", spec)); err == nil {
			t.Fatalf("int 字段声明 %s 必须拒装（PG 会直接报错）", op)
		}
	}
}

// TestParseRejectsCompareOperatorOnBool 二值列只接 eq/ne/IN/NULL 类，不接范围与比较
func TestParseRejectsCompareOperatorOnBool(t *testing.T) {
	for _, op := range []string{"gt", "BETWEEN"} {
		spec := manifest.Entity{
			Entity:  "items",
			TitleZh: "条目",
			TitleEn: "Items",
			Fields: []manifest.Field{{
				Name: "status", Type: "bool", TitleZh: "状态", TitleEn: "Status",
				List: true, Form: "switch", Search: &manifest.Search{Op: op},
			}},
		}
		if _, err := Parse(entityRow(t, "demo", "items", spec)); err == nil {
			t.Fatalf("bool 字段声明 %s 必须拒装", op)
		}
	}
}

// TestParseRejectsReservedColumns 引擎自管列不得被 spec 字段占用
func TestParseRejectsReservedColumns(t *testing.T) {
	for _, name := range []string{"id", "created_at", "updated_at", "deleted_at"} {
		spec := manifest.Entity{
			Entity:  "items",
			TitleZh: "条目",
			TitleEn: "Items",
			Fields:  []manifest.Field{textField(name, "占位")},
		}
		if _, err := Parse(entityRow(t, "demo", "items", spec)); err == nil {
			t.Fatalf("字段名 %q 是引擎保留列名，必须拒装", name)
		}
	}
}

// TestParseWeighReservedOnlyWhenDragSort 未开 drag_sort 时 weigh 是普通业务列（与 manifest 同口径）
func TestParseWeighReservedOnlyWhenDragSort(t *testing.T) {
	withoutDragSort := manifest.Entity{
		Entity:  "items",
		TitleZh: "条目",
		TitleEn: "Items",
		Fields:  []manifest.Field{textField("weigh", "权重")},
	}
	if _, err := Parse(entityRow(t, "demo", "items", withoutDragSort)); err != nil {
		t.Fatalf("未开 drag_sort 时 weigh 应可作业务列，实际: %v", err)
	}

	withDragSort := withoutDragSort
	withDragSort.DragSort = true
	if _, err := Parse(entityRow(t, "demo", "items", withDragSort)); err == nil {
		t.Fatal("drag_sort=true 时 weigh 由引擎附加，spec 字段不得占用")
	}
}

// TestParseRejectsTrgmOnNonText trgm 只许文本列（引擎建 pg_trgm GIN 索引）
func TestParseRejectsTrgmOnNonText(t *testing.T) {
	spec := manifest.Entity{
		Entity:  "items",
		TitleZh: "条目",
		TitleEn: "Items",
		Fields: []manifest.Field{{
			Name: "amount", Type: "int", TitleZh: "数量", TitleEn: "Amount",
			List: true, Form: "input-number", Trgm: true,
		}},
	}
	if _, err := Parse(entityRow(t, "demo", "items", spec)); err == nil {
		t.Fatal("int 字段声明 trgm 必须拒装")
	}
}

// TestParseRejectsPoisonedPhysicalTable 表名注入负例（硬条款 ①：标识符闭集）
func TestParseRejectsPoisonedPhysicalTable(t *testing.T) {
	spec := manifest.Entity{
		Entity:  "items",
		TitleZh: "条目",
		TitleEn: "Items",
		Fields:  []manifest.Field{textField("title", "标题")},
	}
	for _, table := range []string{
		`plugin_demo_items"; DROP TABLE admin_plugins; --`,
		"plugin_demo_items\nDELETE FROM x",
		`plugin_demo_items"`,
		"plugin demo items",
	} {
		row := entityRow(t, "demo", "items", spec)
		row.PhysicalTable = table
		if _, err := Parse(row); err == nil {
			t.Fatalf("物理表名 %q 含注入面，必须拒装", table)
		}
	}

	// 合法形态（空白符在两侧时按 trim 后判定）仍旧放行
	row := entityRow(t, "demo", "items", spec)
	row.PhysicalTable = " " + PhysicalEntityTable(prefixOf(), "demo", "items") + " "
	if _, err := Parse(row); err != nil {
		t.Fatalf("与命名公式一致的表名不应被拒: %v", err)
	}
}

// TestParseRejectsPhysicalTableMismatch 物理表名一致性（「table_name 显式存物理名」）：
// 非空就必须等于 <前缀>plugin_<名>_<实体>；不一致 ⇒ 拒载并点名两侧（旧前缀 / 手工改写元数据的典型现场）。
func TestParseRejectsPhysicalTableMismatch(t *testing.T) {
	row := entityRow(t, "demo", "items", manifest.Entity{
		Entity:  "items",
		TitleZh: "条目",
		TitleEn: "Items",
		Fields:  []manifest.Field{textField("title", "标题")},
	})
	row.PhysicalTable = "oldprefix_plugin_demo_items"
	_, err := Parse(row)
	if err == nil {
		t.Fatal("物理表名与命名公式不一致必须拒载（否则会静默读写错表）")
	}
	msg := err.Error()
	for _, want := range []string{"oldprefix_plugin_demo_items", PhysicalEntityTable(prefixOf(), "demo", "items")} {
		if !strings.Contains(msg, want) {
			t.Fatalf("拒绝理由应点名实际表名与公式名（缺 %q）: %v", want, err)
		}
	}
	// 同一条判定在预检/安装期走的是 host.ValidateSpec（本包注册的增补校验器）——两处必须同结论
	if err := host.ValidateSpec(row); err == nil {
		t.Fatal("host.ValidateSpec（启动预检/安装期写库的入口）必须同样拒掉表名不一致的行")
	}
}

// TestEngineReusesManifestFieldValidation 引擎链复用 manifest.ValidateField：
// 字段值域闭集（form↔type 映射、enum 必须有 dict）在**运行期装载**这一侧也必须成立——
// 元数据可被手工改写（不经清单校验），引擎不能只在安装期把关。
func TestEngineReusesManifestFieldValidation(t *testing.T) {
	base := func(field manifest.Field) manifest.Entity {
		return manifest.Entity{Entity: "items", TitleZh: "条目", TitleEn: "Items", Fields: []manifest.Field{field}}
	}
	// 负例：enum 必须有 dict、form 与 type 不匹配、词表外操作符——三条都在引擎链上拒
	rejects := []struct {
		what  string
		field manifest.Field
	}{
		{"enum 缺 dict", manifest.Field{Name: "status", Type: "enum", TitleZh: "状态", TitleEn: "Status", Form: "select"}},
		{"bool 配 input（form↔type 映射）", manifest.Field{Name: "on", Type: "bool", TitleZh: "开", TitleEn: "On", Form: "input"}},
		{"string 配 number 控件", manifest.Field{Name: "title", Type: "string", TitleZh: "标题", TitleEn: "Title", Form: "input-number"}},
	}
	for _, c := range rejects {
		if _, err := Parse(entityRow(t, "demo", "items", base(c.field))); err == nil {
			t.Fatalf("%s：引擎装载链必须拒（复用 manifest.ValidateField）", c.what)
		}
	}

	// 正例：bool + switch + dict（列注释口径）与示例形态（input + dict）都放行
	accepts := []manifest.Field{
		{Name: "status", Type: "bool", TitleZh: "状态", TitleEn: "Status", Form: "switch",
			Dict: []manifest.Dict{{Value: 0, LabelZh: "停用", LabelEn: "Disabled"}, {Value: 1, LabelZh: "启用", LabelEn: "Enabled"}}},
		{Name: "title", Type: "string", TitleZh: "标题", TitleEn: "Title", Length: 64, Form: "input",
			Dict: []manifest.Dict{{Value: 1, LabelZh: "启用", LabelEn: "Enabled"}}},
	}
	for _, field := range accepts {
		if _, err := Parse(entityRow(t, "demo", "items", base(field))); err != nil {
			t.Fatalf("字段 %s（%s/%s + dict）应可装载: %v", field.Name, field.Type, field.Form, err)
		}
	}
}

// TestParseRejectsUnknownSpecKey 键集闭集（多余键一律拒装）由宿主校验器把关，引擎复用同一条判定
func TestParseRejectsUnknownSpecKey(t *testing.T) {
	row := entityRow(t, "demo", "items", manifest.Entity{
		Entity:  "items",
		TitleZh: "条目",
		TitleEn: "Items",
		Fields:  []manifest.Field{textField("title", "标题")},
	})
	row.Spec = datatypes.JSON(`{"entity":"items","title_zh":"条目","title_en":"Items","fields":[{"name":"title","type":"string"}],"extra_key":true}`)
	if _, err := Parse(row); err == nil {
		t.Fatal("出现闭集外的键必须拒装")
	}
}

// TestParseRejectsSpecEntityMismatch spec.entity 与实体行 name 必须逐字一致
func TestParseRejectsSpecEntityMismatch(t *testing.T) {
	row := entityRow(t, "demo", "items", manifest.Entity{
		Entity:  "others",
		TitleZh: "条目",
		TitleEn: "Items",
		Fields:  []manifest.Field{textField("title", "标题")},
	})
	if _, err := Parse(row); err == nil {
		t.Fatal("spec.entity 与实体行 name 不一致必须拒装")
	}
}

// TestParseRejectsLengthBudgetOverflow 标识符长度预算（节点名 ≤ 49 与引擎索引名 42−前缀 的更紧者）
func TestParseRejectsLengthBudgetOverflow(t *testing.T) {
	plugin := strings.Repeat("a", 30)
	entity := strings.Repeat("b", 24) // 30+24=54 > 42（前缀为空时 engine 取 42）
	spec := manifest.Entity{
		Entity:  entity,
		TitleZh: "条目",
		TitleEn: "Items",
		Fields:  []manifest.Field{textField("title", "标题")},
	}
	if _, err := Parse(entityRow(t, plugin, entity, spec)); err == nil {
		t.Fatal("名+实体超出长度预算必须拒装")
	}
}

// TestParseLengthBudgetByDDLOwner 预算按 ddl_owner 分流：engine 的表引擎要发索引名 ⇒
// 42 − len(前缀)（截断兜底形态 前缀+21+名+实体 ≤ 63）；migrations 的表结构由插件迁移自管 ⇒
// 引擎不发索引名，只剩节点名约束 49。43..46 区间**只在 migrations 下**可装载（engine 下会建不出表）。
func TestParseLengthBudgetByDDLOwner(t *testing.T) {
	const plugin = "demo" // 4 字节
	entity := strings.Repeat("e", 39)
	base := manifest.Entity{
		Entity:  entity, // 4 + 39 = 43
		TitleZh: "条目",
		TitleEn: "Items",
		Fields:  []manifest.Field{textField("title", "标题")},
	}
	if _, err := Parse(entityRow(t, plugin, entity, base)); err == nil {
		t.Fatal("联合长度 43 在 ddl_owner=engine（缺省）下必须拒装（引擎索引名放不下）")
	}

	owned := base
	owned.DdlOwner = "migrations"
	if _, err := Parse(entityRow(t, plugin, entity, owned)); err != nil {
		t.Fatalf("联合长度 43 在 ddl_owner=migrations 下应可装载（引擎不发索引名）: %v", err)
	}

	tooLong := base
	tooLong.DdlOwner = "migrations"
	tooLong.Entity = strings.Repeat("e", 46) // 4 + 46 = 50 > 49（节点名约束，与归属无关）
	if _, err := Parse(entityRow(t, plugin, tooLong.Entity, tooLong)); err == nil {
		t.Fatal("联合长度 50 在 migrations 下也必须拒装（节点名受 varchar(64) 约束）")
	}
}

// TestSpecWhitelistsAndColumns 排序白名单 / 条件白名单 / 列表列（显式列，不 SELECT *）
func TestSpecWhitelistsAndColumns(t *testing.T) {
	spec := mustParse(t, "demo", "items", manifest.Entity{
		Entity:   "items",
		TitleZh:  "条目",
		TitleEn:  "Items",
		DragSort: true,
		Fields: []manifest.Field{
			textField("title", "标题", func(field *manifest.Field) { field.Sort = true }),
			textField("body", "正文", func(field *manifest.Field) {
				field.Type = "text"
				field.Form = "textarea"
				field.Search = nil
				field.QuickSearch = false
				field.List = false // text 是大字段：不进列表列（本用例验证「未声明 list 的字段不出列」）
			}),
			{Name: "amount", Type: "int", TitleZh: "数量", TitleEn: "Amount", Form: "input-number"},
		},
	})

	if !spec.Sortable("title") {
		t.Error("声明 sort:true 的字段应在排序白名单内")
	}
	if spec.Sortable("amount") || spec.Sortable("nonexistent") {
		t.Error("未声明 sort 的字段与不存在的字段不得进排序白名单")
	}
	if !spec.Sortable(ColumnID) || !spec.Sortable(ColumnCreatedAt) || !spec.Sortable(ColumnWeigh) {
		t.Error("引擎列 id/created_at（+drag_sort 的 weigh）应在白名单内")
	}

	if _, ok := spec.Filterable("title"); !ok {
		t.Error("声明 search 的字段应可作条件")
	}
	if _, ok := spec.Filterable("amount"); ok {
		t.Error("未声明 search/quickSearch 的字段不得作条件")
	}

	columns := strings.Join(spec.ListColumns(), ",")
	if !strings.HasPrefix(columns, ColumnID+",") || !strings.Contains(columns, "title") {
		t.Errorf("列表列应含 id 与声明 list 的字段，实际 %s", columns)
	}
	if strings.Contains(columns, "body") {
		t.Errorf("未声明 list 时 text 字段不进列表列（大字段），实际 %s", columns)
	}
	if !strings.Contains(columns, ColumnWeigh) || !strings.Contains(columns, ColumnCreatedAt) {
		t.Errorf("drag_sort 的实体列表列应含 weigh 与 created_at，实际 %s", columns)
	}
	if strings.Contains(columns, "*") {
		t.Errorf("列表列必须显式列出，不得出现 *（实际 %s）", columns)
	}
}

// TestSpecListColumnsFallback 未声明 list 时回退为「除 text 外的全部字段」
func TestSpecListColumnsFallback(t *testing.T) {
	spec := mustParse(t, "demo", "items", manifest.Entity{
		Entity:  "items",
		TitleZh: "条目",
		TitleEn: "Items",
		Fields: []manifest.Field{
			{Name: "title", Type: "string", TitleZh: "标题", TitleEn: "Title", Length: 64, Form: "input"},
			{Name: "body", Type: "text", TitleZh: "正文", TitleEn: "Body", Form: "textarea"},
		},
	})
	columns := strings.Join(spec.ListColumns(), ",")
	if !strings.Contains(columns, "title") || strings.Contains(columns, "body") {
		t.Fatalf("回退口径应为「除 text 外全部字段」，实际 %s", columns)
	}
}

// TestBuildMetaEngineColumnLabels 引擎列标签随 lang：引擎列不在 spec 里、没有双语元数据，
// 标签在 BuildMeta 里按 lang 取值；未知/空 lang 回退中文（与字段文案同一口径）。
func TestBuildMetaEngineColumnLabels(t *testing.T) {
	spec := mustParse(t, "demo", "items", manifest.Entity{
		Entity:   "items",
		TitleZh:  "条目",
		TitleEn:  "Items",
		DragSort: true,
		Fields:   []manifest.Field{textField("title", "标题")},
	})

	labels := func(meta Meta) map[string]string {
		t.Helper()
		out := make(map[string]string, len(meta.Columns))
		for _, column := range meta.Columns {
			out[column.Prop] = column.Label
		}
		return out
	}

	zh := labels(BuildMeta(spec, "zh-cn"))
	if zh[ColumnID] != "ID" || zh[ColumnWeigh] != "排序权重" || zh[ColumnCreatedAt] != "创建时间" {
		t.Fatalf("中文 locale 的引擎列标签不符: %v", zh)
	}
	en := labels(BuildMeta(spec, "en-US"))
	if en[ColumnID] != "ID" || en[ColumnWeigh] != "Sort Weight" || en[ColumnCreatedAt] != "Created At" {
		t.Fatalf("英文 locale 的引擎列标签不符: %v", en)
	}
	fallback := labels(BuildMeta(spec, ""))
	if fallback[ColumnWeigh] != "排序权重" || fallback[ColumnCreatedAt] != "创建时间" {
		t.Fatalf("未带 lang 时应回退中文标签: %v", fallback)
	}
	if fallback["title"] != "标题" {
		t.Fatalf("字段标签也应回退中文: %v", fallback)
	}
}

// TestValueColumnsExcludeEngineColumns 可写列只有 spec 字段（引擎列不可由请求体写入）
func TestValueColumnsExcludeEngineColumns(t *testing.T) {
	spec := mustParse(t, "demo", "items", manifest.Entity{
		Entity:  "items",
		TitleZh: "条目",
		TitleEn: "Items",
		Fields:  []manifest.Field{textField("title", "标题")},
	})
	joined := strings.Join(spec.ValueColumns(), ",")
	if joined != "title" {
		t.Fatalf("可写列应只有 spec 字段，实际 %s", joined)
	}
}

// TestCollectValuesRejectsEmptyRequired / null / 超长（写入前的校验口径）
func TestCollectValuesValidation(t *testing.T) {
	spec := mustParse(t, "demo", "items", manifest.Entity{
		Entity:  "items",
		TitleZh: "条目",
		TitleEn: "Items",
		Fields: []manifest.Field{
			textField("title", "标题", func(field *manifest.Field) {
				field.Required = true
				field.Length = 8
			}),
			{Name: "remark", Type: "string", TitleZh: "备注", TitleEn: "Remark", Length: 32, Form: "input"},
		},
	})

	if _, _, err := spec.collectValues(map[string]any{}, true); err == nil {
		t.Error("create 时缺失必填字段必须报错")
	}
	if _, _, err := spec.collectValues(map[string]any{"title": ""}, true); err == nil {
		t.Error("create 时必填字段为空必须报错")
	}
	if _, _, err := spec.collectValues(map[string]any{"title": "123456789"}, true); err == nil {
		t.Error("超长字段必须报错")
	}
	columns, args, err := spec.collectValues(map[string]any{"title": "ok", "deleted_at": "2020-01-01", "id": 9}, true)
	if err != nil {
		t.Fatalf("合法请求体不应报错: %v", err)
	}
	if strings.Join(columns, ",") != "title" || len(args) != 1 {
		t.Fatalf("引擎列与未知键必须被忽略，实际列 %v 参数 %v", columns, args)
	}
	if _, _, err := spec.collectValues(map[string]any{"title": "ok"}, false); err != nil {
		t.Fatalf("update 只处理出现的字段，不应报错: %v", err)
	}
}
