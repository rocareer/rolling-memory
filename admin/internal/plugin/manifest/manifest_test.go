package manifest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-go-hub/ai-go-admin/internal/plugin/host"
)

// validYAML 一份最小合法清单（code 形态；用于在用例里定点改一行验单条规则）
const validYAML = `
name: demo
title: 示例插件
version: 1.0.0
kind: code
host_api: ">=1 <2"
`

// mixedYAML 含菜单/实体/前端/配置的合法清单（mixed 形态）
const mixedYAML = `
name: demo
title: 示例插件
version: 1.0.0
kind: mixed
host_api: ">=1 <2"
framework_req: ">=0.1 <1"
menus:
  - title: 示例
    type: dir
    name: plugin/demo
    path: plugin/demo
  - title: 条目
    type: menu
    name: plugin/demo/items
    path: plugin/demo/items
    component: /src/views/admin/plugin/render/index.vue
    pid: plugin/demo
entities:
  - entity: items
    title_zh: 条目
    title_en: Items
    drag_sort: true
    fields:
      - name: title
        type: string
        length: 128
      - name: status
        type: int
web:
  views: [web/views]
  lang: [web/lang/zh-cn.yaml]
configs:
  - name: retry_limit
    title: 重试上限
    type: number
    value: "3"
`

func TestParseValidCode(t *testing.T) {
	t.Parallel()
	m, warnings, err := Parse([]byte(validYAML), Options{})
	if err != nil {
		t.Fatalf("合法清单应通过: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("无 framework_req 时不应有告警: %v", warnings)
	}
	if m.Name != "demo" || m.Kind != "code" || m.Version != "1.0.0" || m.HostAPI != ">=1 <2" {
		t.Fatalf("解析结果不符: %+v", m)
	}
}

func TestParseValidMixed(t *testing.T) {
	t.Parallel()
	m, _, err := Parse([]byte(mixedYAML), Options{Prefix: "", HostVersion: "0.6.0"})
	if err != nil {
		t.Fatalf("合法混合清单应通过: %v", err)
	}
	if len(m.Menus) != 2 || len(m.Entities) != 1 || m.Web == nil || len(m.Configs) != 1 {
		t.Fatalf("嵌套段解析不符: %+v", m)
	}
	if !m.Entities[0].DragSort || m.Entities[0].Fields[1].Name != "status" {
		t.Fatalf("实体段解析不符: %+v", m.Entities[0])
	}
}

func TestParseRejectsUnknownKeys(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"顶层未知键":      strings.Replace(validYAML, "kind: code", "kind: code\nwhatever: 1", 1),
		"菜单未知键":      strings.Replace(mixedYAML, "    path: plugin/demo\n", "    path: plugin/demo\n    bogus: x\n", 1),
		"实体未知键":      strings.Replace(mixedYAML, "    drag_sort: true\n", "    drag_sort: true\n    ddl: engine\n", 1),
		"字段未知键":      strings.Replace(mixedYAML, "        type: string\n", "        type: string\n        widght: 1\n", 1),
		"缺 host_api": strings.Replace(validYAML, "host_api: \">=1 <2\"\n", "", 1),
	}
	for name, yaml := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, _, err := Parse([]byte(yaml), Options{}); err == nil {
				t.Fatal("应被拒装")
			}
		})
	}
}

func TestParseNameRules(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		sub  string
	}{
		{"含下划线", "demo_plugin"},
		{"首字符非字母", "1demo"},
		{"大写", "Demo"},
		{"单字符", "d"},
		{"超长 33", "d" + strings.Repeat("a", 32)},
		{"保留字 render", "render"},
		{"保留字 list", "list"},
		{"保留字 enable", "enable"},
		// read 是宿主管理面节点 plugin/read 的末段（分组 dir 规则名 plugin/<名> 会与它逐字相等）
		{"保留字 read", "read"},
		{"保留字 disable", "disable"},
		{"保留字 get", "get"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			yaml := strings.Replace(validYAML, "name: demo", "name: "+c.sub, 1)
			if _, _, err := Parse([]byte(yaml), Options{}); err == nil {
				t.Fatalf("name %q 应被拒装", c.sub)
			}
		})
	}
	// 边界合法：2 字符与 32 字符、含连字符
	for _, ok := range []string{"ab", "d" + strings.Repeat("a", 31), "my-plugin-2"} {
		t.Run("合法 "+ok, func(t *testing.T) {
			t.Parallel()
			yaml := strings.Replace(validYAML, "name: demo", "name: "+ok, 1)
			if _, _, err := Parse([]byte(yaml), Options{}); err != nil {
				t.Fatalf("name %q 应通过: %v", ok, err)
			}
		})
	}
}

func TestParseHostAPI(t *testing.T) {
	t.Parallel()
	// 与宿主**当前**机制接口版本（host.APIVersion）有关的用例一律由它派生，不写死版本号：
	// 裸版本 = 精确相等（host/version.go 口径），写死 "1.0.0": true 会随 APIVersion 递增静默失真
	// （1.1.0 递增后本用例就这样红过一次；「严格大于/小于当前版本」两个域与取值无关，恒为不相容）。
	cases := map[string]bool{ // key = host_api，value = 是否应通过
		">=1 <2":               true,
		">=1.0.0":              true,
		host.APIVersion:        true, // 裸版本精确命中当前机制接口版本
		">=" + host.APIVersion: true,
		">" + host.APIVersion:  false, // 严格大于当前版本 ⇒ 必不相容
		"<" + host.APIVersion:  false, // 严格小于 ⇒ 同理
		">=2 <3":               false, // 主版本 2 的域与当前宿主不相容
		"^1":                   false, // v1 不支持插入语义（拒解析）
		">=1 || <0.5":          false,
		"":                     false,
	}
	for domain, want := range cases {
		t.Run("host_api="+domain, func(t *testing.T) {
			t.Parallel()
			yaml := strings.Replace(validYAML, "host_api: \">=1 <2\"", "host_api: \""+domain+"\"", 1)
			_, _, err := Parse([]byte(yaml), Options{})
			if want && err != nil {
				t.Fatalf("host_api %q 应通过: %v", domain, err)
			}
			if !want && err == nil {
				t.Fatalf("host_api %q 应被拒装", domain)
			}
		})
	}
}

func TestFrameworkReqOnlyWarns(t *testing.T) {
	t.Parallel()
	// 与宿主版本不相容 ⇒ 只告警不拒装（VERSION 属「各仓保留」）
	yaml := validYAML + "framework_req: \">=9.0\"\n"
	m, warnings, err := Parse([]byte(yaml), Options{HostVersion: "0.6.0"})
	if err != nil {
		t.Fatalf("framework_req 不相容不应拒装: %v", err)
	}
	if m.FrameworkReq != ">=9.0" || len(warnings) != 1 || !strings.Contains(warnings[0], "不拒装") {
		t.Fatalf("应给出提示告警: %v", warnings)
	}

	// 域语法非法 ⇒ 拒装（解析失败属清单错误）
	if _, _, err := Parse([]byte(validYAML+"framework_req: \"~~1\"\n"), Options{HostVersion: "0.6.0"}); err == nil {
		t.Fatal("非法 framework_req 域应被拒装")
	}
}

func TestMenuNamePrefixAndType(t *testing.T) {
	t.Parallel()
	cases := map[string]bool{
		"无前缀":         false,
		"前缀不符":        false,
		"节点类型非法":      false,
		"菜单缺组件":       false,
		"pid 悬空":      false,
		"菜单名重复":       false,
		"分组 dir 恰为组名": true, //：分组 dir 的 name 就是 plugin/<名>
		"menu 无尾斜杠":   false,
		"合法（补全）":      true,
	}
	for name, want := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var menuBlock string
			switch name {
			case "无前缀":
				menuBlock = "\nmenus:\n  - {title: X, type: dir, name: demo}\n"
			case "前缀不符":
				menuBlock = "\nmenus:\n  - {title: X, type: dir, name: plugin/other}\n"
			case "节点类型非法":
				menuBlock = "\nmenus:\n  - {title: X, type: page, name: plugin/demo}\n"
			case "菜单缺组件":
				menuBlock = "\nmenus:\n  - {title: X, type: menu, name: plugin/demo/items}\n"
			case "pid 悬空":
				menuBlock = "\nmenus:\n  - {title: X, type: menu, name: plugin/demo/items, component: /x.vue, pid: plugin/demo/absent}\n"
			case "菜单名重复":
				menuBlock = "\nmenus:\n  - {title: X, type: dir, name: plugin/demo}\n  - {title: Y, type: dir, name: plugin/demo}\n"
			case "分组 dir 恰为组名":
				menuBlock = "\nmenus:\n  - {title: X, type: dir, name: plugin/demo, path: plugin/demo}\n"
			case "menu 无尾斜杠":
				menuBlock = "\nmenus:\n  - {title: X, type: menu, name: plugin/demo, component: /src/views/admin/plugin/demo/index.vue}\n"
			case "合法（补全）":
				menuBlock = "\nmenus:\n  - {title: X, type: dir, name: plugin/demo}\n  - {title: Y, type: node, name: plugin/demo/items/read, pid: plugin/demo}\n"
			}
			_, _, err := Parse([]byte(validYAML+menuBlock), Options{})
			if want && err != nil {
				t.Fatalf("应通过: %v", err)
			}
			if !want && err == nil {
				t.Fatal("应被拒装")
			}
		})
	}
}

func TestEntitiesRules(t *testing.T) {
	t.Parallel()
	// 注意：validYAML 已含 `kind: code`，这里必须替换而不是追加（重复键会被严格解码器拒掉）
	mixedDoc := strings.Replace(validYAML, "kind: code", "kind: mixed", 1)
	entityYAML := func(body string) string {
		return mixedDoc + "entities:\n" + body
	}
	cases := map[string]bool{
		"code 形态带实体":   false,
		"实体名非法":        false,
		"实体名重复":        false,
		"缺双语标题":        false,
		"ddl_owner 非法": false,
		"字段为空":         false,
		"字段名非法":        false,
		"字段名重复":        false,
		"字段名命中保留列":     false,
		"不拖排的 weigh 列": true,
	}
	bodies := map[string]string{
		"实体名非法":        "  - {entity: Items, title_zh: 条目, title_en: Items, fields: [{name: title, type: string}]}\n",
		"实体名重复":        "  - {entity: items, title_zh: 条目, title_en: Items, fields: [{name: title, type: string}]}\n  - {entity: items, title_zh: 条目, title_en: Items, fields: [{name: title, type: string}]}\n",
		"缺双语标题":        "  - {entity: items, title_zh: 条目, fields: [{name: title, type: string}]}\n",
		"ddl_owner 非法": "  - {entity: items, title_zh: 条目, title_en: Items, ddl_owner: self, fields: [{name: title, type: string}]}\n",
		"字段为空":         "  - {entity: items, title_zh: 条目, title_en: Items, fields: []}\n",
		"字段名非法":        "  - {entity: items, title_zh: 条目, title_en: Items, fields: [{name: Bad, type: string}]}\n",
		"字段名重复":        "  - {entity: items, title_zh: 条目, title_en: Items, fields: [{name: title, type: string}, {name: title, type: int}]}\n",
		"字段名命中保留列":     "  - {entity: items, title_zh: 条目, title_en: Items, fields: [{name: deleted_at, type: datetime}]}\n",
		"不拖排的 weigh 列": "  - {entity: items, title_zh: 条目, title_en: Items, fields: [{name: weigh, type: int}]}\n",
	}
	for name, want := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var doc string
			if name == "code 形态带实体" {
				doc = validYAML + "entities:\n" + bodies["字段名命中保留列"]
			} else {
				doc = entityYAML(bodies[name])
			}
			_, _, err := Parse([]byte(doc), Options{})
			if want && err != nil {
				t.Fatalf("应通过: %v", err)
			}
			if !want && err == nil {
				t.Fatal("应被拒装")
			}
		})
	}

	// drag_sort 时 weigh 是引擎附加列 ⇒ 作者声明它应被拒
	dragDoc := mixedDoc + "entities:\n" +
		"  - {entity: items, title_zh: 条目, title_en: Items, drag_sort: true, fields: [{name: weigh, type: int}]}\n"
	if _, _, err := Parse([]byte(dragDoc), Options{}); err == nil {
		t.Fatal("drag_sort 时声明的 weigh 列应被拒装")
	}
}

func TestLengthBudget(t *testing.T) {
	t.Parallel()
	// 同 TestEntitiesRules：kind 必须替换（重复键会被严格解码器拒掉）
	mixedDoc := strings.Replace(validYAML, "kind: code", "kind: mixed", 1)
	entity := func(entityLen int) string {
		return mixedDoc + "entities:\n  - {entity: " + strings.Repeat("e", entityLen) +
			", title_zh: 条目, title_en: Items, fields: [{name: title, type: string}]}\n"
	}
	entityWithOwner := func(entityLen int, owner string) string {
		return mixedDoc + "entities:\n  - {entity: " + strings.Repeat("e", entityLen) +
			", title_zh: 条目, title_en: Items, ddl_owner: " + owner + ", fields: [{name: title, type: string}]}\n"
	}
	// 名 demo（4）+ 实体 38 = 42 ⇒ 空前缀、engine 下恰好命中上限（42 = 引擎索引名**截断兜底形态**的
	// 可用长度：前缀 + 21 + 名 + 实体 ≤ 63；自然名形态的区间由兜底覆盖）
	if _, _, err := Parse([]byte(entity(38)), Options{}); err != nil {
		t.Fatalf("联合长度 42（engine）应通过: %v", err)
	}
	if _, _, err := Parse([]byte(entity(39)), Options{}); err == nil {
		t.Fatal("联合长度 43（engine）应被拒装（空前缀上限 42）")
	}
	// ddl_owner=migrations：引擎不发索引名 ⇒ 只剩节点名约束（49）
	if _, _, err := Parse([]byte(entityWithOwner(45, "migrations")), Options{}); err != nil {
		t.Fatalf("联合长度 49（ddl_owner=migrations）应通过: %v", err)
	}
	if _, _, err := Parse([]byte(entityWithOwner(46, "migrations")), Options{}); err == nil {
		t.Fatal("联合长度 50（ddl_owner=migrations）应被拒装（节点名上限 49）")
	}
	// 前缀 8 字节 ⇒ engine 上限 34
	if _, _, err := Parse([]byte(entity(30)), Options{Prefix: "prefix8_"}); err != nil {
		t.Fatalf("前缀 8 时联合长度 34 应通过: %v", err)
	}
	if _, _, err := Parse([]byte(entity(31)), Options{Prefix: "prefix8_"}); err == nil {
		t.Fatal("前缀 8 时联合长度 35 应被拒装")
	}
	// 上限函数：节点名（varchar(64) ⇒ 49）与引擎索引名（PG 标识符 63 字节 ⇒ 42−len(前缀)）的更紧者；
	// ddl_owner=migrations 的表只剩前者（引擎不发索引名）
	if got := NameEntityBudget("", "engine"); got != 42 {
		t.Errorf("空前缀（engine）预算 = %d, want 42", got)
	}
	if got := NameEntityBudget("", ""); got != 42 {
		t.Errorf("ddl_owner 缺省应等同 engine，实际 %d", got)
	}
	if got := NameEntityBudget("pfx_", "engine"); got != 38 {
		t.Errorf("前缀 4 字节（engine）预算 = %d, want 38", got)
	}
	if got := NameEntityBudget("", "migrations"); got != 49 {
		t.Errorf("ddl_owner=migrations 预算 = %d, want 49（引擎不发索引名）", got)
	}
	if got := NameEntityBudget(strings.Repeat("p", 42), "engine"); got != 0 {
		t.Errorf("前缀 42 字节（engine）预算 = %d, want 0", got)
	}
	if NameEntityBudget(strings.Repeat("p", 43), "engine") >= 0 {
		t.Error("前缀 43 字节（engine）时预算应为负（无法容纳任何实体）")
	}
	if engineIndexBudgetBase > nodeBudget {
		t.Error("引擎索引名约束应不大于节点名约束（取更紧者口径）")
	}
}

func TestRequiresFormat(t *testing.T) {
	t.Parallel()
	if _, _, err := Parse([]byte(validYAML+"requires: \"base>=1 <2, util@>=1.0.0\"\n"), Options{}); err == nil {
		t.Fatal("requires 项缺少 @ 应被拒装")
	}
	if _, _, err := Parse([]byte(validYAML+"requires: \"base@>=1 <2\"\n"), Options{}); err != nil {
		t.Fatalf("合法依赖应通过: %v", err)
	}
	if _, _, err := Parse([]byte(validYAML+"requires: \"render@>=1\"\n"), Options{}); err == nil {
		t.Fatal("依赖项命中宿主保留字应被拒装")
	}
}

func TestWebAndConfigsPaths(t *testing.T) {
	t.Parallel()
	// 绝对路径与 .. 一律拒（与落位防护同口径）
	if _, _, err := Parse([]byte(validYAML+"web:\n  views: [/etc]\n"), Options{}); err == nil {
		t.Fatal("绝对路径应被拒装")
	}
	if _, _, err := Parse([]byte(validYAML+"web:\n  lang: [../outside.yaml]\n"), Options{}); err == nil {
		t.Fatal("含 .. 的路径应被拒装")
	}
	if _, _, err := Parse([]byte(validYAML+"configs:\n  - {name: Retry, title: X}\n"), Options{}); err == nil {
		t.Fatal("非法配置键名应被拒装")
	}
	if _, _, err := Parse([]byte(validYAML+"configs:\n  - {name: retry, title: X}\n  - {name: retry, title: Y}\n"), Options{}); err == nil {
		t.Fatal("重复配置键应被拒装")
	}
}

func TestLoadChecksDirAndDeclaredPaths(t *testing.T) {
	t.Parallel()
	root := t.TempDir()

	// 目录名与 name 不符 ⇒ 拒
	bad := filepath.Join(root, "other")
	if err := os.MkdirAll(bad, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bad, FileName), []byte(validYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Load(bad, Options{}); err == nil {
		t.Fatal("目录名 != name 应被拒装")
	}

	// 目录名相符但 web 源路径不存在 ⇒ 拒
	good := filepath.Join(root, "demo")
	if err := os.MkdirAll(good, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(good, FileName), []byte(validYAML+"web:\n  views: [web/views]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Load(good, Options{}); err == nil {
		t.Fatal("web 源路径不存在应被拒装")
	}

	// 补齐迁移目录与 web 源后通过
	if err := os.MkdirAll(filepath.Join(good, "migrations"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(good, "web", "views"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Load(good, Options{}); err != nil {
		t.Fatalf("目录齐备后应通过: %v", err)
	}
}

func TestFieldValueDomains(t *testing.T) {
	t.Parallel()
	mixedDoc := strings.Replace(validYAML, "kind: code", "kind: mixed", 1)
	build := func(fieldYAML string) string {
		return mixedDoc + "entities:\n  - entity: items\n    title_zh: 条目\n    title_en: Items\n    fields:\n      - " + fieldYAML + "\n"
	}

	// 词表规模锚定（宿主词表 16 项原文；改动必须同步 web/types/table.d.ts 与 service/base.go）
	if len(SearchOperators) != 16 {
		t.Fatalf("搜索操作符词表应为 16 项，实际 %d：%v", len(SearchOperators), SearchOperators)
	}
	if len(FieldTypes) != 9 || len(FieldForms) != 8 {
		t.Fatalf("type/form 闭集规模不符：type=%d form=%d", len(FieldTypes), len(FieldForms))
	}

	accept := map[string]string{
		"string+input":         "{name: title, type: string, form: input}",
		"text+textarea":        "{name: body, type: text, form: textarea}",
		"int+input-number":     "{name: age, type: int, form: input-number}",
		"bigint+input-number":  "{name: total, type: bigint, form: input-number}",
		"decimal+input-number": "{name: price, type: decimal, form: input-number}",
		"bool+switch":          "{name: enabled, type: bool, form: switch}",
		"date+date":            "{name: day, type: date, form: date}",
		"datetime+datetime":    "{name: at, type: datetime, form: datetime}",
		"枚举+select+数值取值":       "{name: status, type: enum, form: select, dict: [{value: 1, label_zh: 启用, label_en: Enabled}]}",
		"枚举+radio+字符串取值":       "{name: state, type: enum, form: radio, dict: [{value: draft, label_zh: 草稿, label_en: Draft}]}",
		"数值枚举+select+dict":     "{name: level, type: int, form: select, dict: [{value: 1, label_zh: 高, label_en: High}]}",
		"二值状态+switch+dict":     "{name: status, type: bool, form: switch, dict: [{value: 0, label_zh: 停用, label_en: Disabled}, {value: 1, label_zh: 启用, label_en: Enabled}]}",
		"示例形态+input+dict":      "{name: title, type: string, form: input, dict: [{value: 1, label_zh: 启用, label_en: Enabled}]}",
		"最简字段（无 form/search）":  "{name: note, type: string}",
	}
	for name, fieldYAML := range accept {
		t.Run("通过/"+name, func(t *testing.T) {
			t.Parallel()
			if _, _, err := Parse([]byte(build(fieldYAML)), Options{}); err != nil {
				t.Fatalf("应通过: %v", err)
			}
		})
	}

	reject := map[string]string{
		"type 不在闭集":                        "{name: title, type: strng}",
		"form 不在闭集":                        "{name: title, type: string, form: inputt}",
		"string 配 textarea":                "{name: title, type: string, form: textarea}",
		"bool 配 input":                     "{name: on, type: bool, form: input}",
		"datetime 配 date":                  "{name: at, type: datetime, form: date}",
		"操作符 gte（不在词表）":                    "{name: title, type: string, search: {op: gte}}",
		"操作符 like（小写）":                     "{name: title, type: string, search: {op: like}}",
		"操作符为空":                            "{name: title, type: string, search: {op: ''}}",
		"enum 缺 dict":                      "{name: status, type: enum, form: select}",
		"enum 配 input":                     "{name: status, type: enum, form: input, dict: [{value: 1, label_zh: 启用, label_en: Enabled}]}",
		"enum 配 switch":                    "{name: status, type: enum, form: switch, dict: [{value: 1, label_zh: 启用, label_en: Enabled}]}",
		"enum 缺 label_en（dict 缺 label_en）": "{name: status, type: enum, form: select, dict: [{value: 1, label_zh: 启用}]}",
		"dict 缺 value":                     "{name: status, type: enum, form: select, dict: [{label_zh: 启用, label_en: Enabled}]}",
		"string 配 select+dict":             "{name: status, type: string, form: select, dict: [{value: 1, label_zh: 启用, label_en: Enabled}]}",
	}
	for name, fieldYAML := range reject {
		t.Run("拒装/"+name, func(t *testing.T) {
			t.Parallel()
			if _, _, err := Parse([]byte(build(fieldYAML)), Options{}); err == nil {
				t.Fatal("应被拒装")
			}
		})
	}

	// 16 项词表原文逐项可用（含带空格的 NOT ILIKE / NOT NULL）
	for _, op := range SearchOperators {
		t.Run("操作符合法/"+op, func(t *testing.T) {
			t.Parallel()
			fieldYAML := "{name: title, type: string, search: {op: '" + op + "'}}"
			if _, _, err := Parse([]byte(build(fieldYAML)), Options{}); err != nil {
				t.Fatalf("词表内操作符 %q 应通过: %v", op, err)
			}
		})
	}
}

// adoptYAML 含接管段（adopt）的合法清单：through=3，逐版本覆盖（对象探针 + kind: none 免探）
const adoptYAML = `
name: demo
title: 示例插件
version: 1.0.0
kind: code
host_api: ">=1 <2"
adopt:
  through: 3
  probes:
    - version: 1
      kind: table
      name: eval_sets
    - version: 2
      kind: none
    - version: 3
      kind: column
      table: eval_runs
      name: title
`

// TestParseAdoptValid 合法 adopt 段（键集闭集内新增键；结构闭集通过）
func TestParseAdoptValid(t *testing.T) {
	t.Parallel()
	m, _, err := Parse([]byte(adoptYAML), Options{Prefix: "t_"})
	if err != nil {
		t.Fatalf("合法 adopt 段应通过: %v", err)
	}
	if m.Adopt == nil || m.Adopt.Through != 3 || len(m.Adopt.Probes) != 3 {
		t.Fatalf("adopt 段解析不符: %+v", m.Adopt)
	}
	if m.Adopt.Probes[2].Table != "eval_runs" || m.Adopt.Probes[2].Name != "title" {
		t.Fatalf("column 探针解析不符: %+v", m.Adopt.Probes[2])
	}
}

// TestParseAdoptRejected adopt 段的非法形态一律拒装（闭集与值域；与 Go 校验逐条对齐）
func TestParseAdoptRejected(t *testing.T) {
	t.Parallel()
	base := strings.Replace(adoptYAML, `adopt:
  through: 3
  probes:`, "adopt:\n  through: 3\n  probes:", 1) // 原样（锚点：便于逐条替换）
	cases := map[string]string{
		"段内未知键":              "adopt:\n  through: 3\n  adopt_from: 2\n  probes:\n    - version: 1\n      kind: table\n      name: eval_sets\n",
		"探针未知键":              "adopt:\n  through: 1\n  probes:\n    - version: 1\n      kind: table\n      name: eval_sets\n      note: x\n",
		"through 缺省（0）":      "adopt:\n  probes:\n    - version: 1\n      kind: table\n      name: eval_sets\n",
		"through 负值":         "adopt:\n  through: -1\n  probes:\n    - version: 1\n      kind: table\n      name: eval_sets\n",
		"kind 不在闭集":          "adopt:\n  through: 1\n  probes:\n    - version: 1\n      kind: trigger\n      name: eval_sets\n",
		"version 越上界":        "adopt:\n  through: 1\n  probes:\n    - version: 2\n      kind: table\n      name: eval_sets\n",
		"version 为 0":        "adopt:\n  through: 1\n  probes:\n    - version: 0\n      kind: table\n      name: eval_sets\n",
		"覆盖漏版本":              "adopt:\n  through: 3\n  probes:\n    - version: 1\n      kind: table\n      name: eval_sets\n    - version: 3\n      kind: table\n      name: eval_runs\n",
		"全是 kind: none":      "adopt:\n  through: 2\n  probes:\n    - version: 1\n      kind: none\n    - version: 2\n      kind: none\n",
		"none 带 name":        "adopt:\n  through: 1\n  probes:\n    - version: 1\n      kind: none\n      name: eval_sets\n",
		"table 带 table":      "adopt:\n  through: 1\n  probes:\n    - version: 1\n      kind: table\n      table: eval_runs\n      name: eval_sets\n",
		"column 缺 table":     "adopt:\n  through: 1\n  probes:\n    - version: 1\n      kind: column\n      name: title\n",
		"constraint 缺 table": "adopt:\n  through: 1\n  probes:\n    - version: 1\n      kind: constraint\n      name: ck_eval_runs_state\n",
		"name 大写":            "adopt:\n  through: 1\n  probes:\n    - version: 1\n      kind: table\n      name: Eval_Sets\n",
		"name 首字符为数字":        "adopt:\n  through: 1\n  probes:\n    - version: 1\n      kind: table\n      name: 1_sets\n",
		"name 带连字符":          "adopt:\n  through: 1\n  probes:\n    - version: 1\n      kind: table\n      name: eval-sets\n",
		"name 超长 64":         "adopt:\n  through: 1\n  probes:\n    - version: 1\n      kind: table\n      name: " + strings.Repeat("a", 64) + "\n",
		"重复探针":               "adopt:\n  through: 1\n  probes:\n    - version: 1\n      kind: table\n      name: eval_sets\n    - version: 1\n      kind: table\n      name: eval_sets\n",
	}
	for name, yaml := range cases {
		t.Run("拒装/"+name, func(t *testing.T) {
			t.Parallel()
			if _, _, err := Parse([]byte(adoptYAML[:0]+strings.Replace(adoptYAML, "adopt:\n  through: 3\n  probes:\n    - version: 1\n      kind: table\n      name: eval_sets\n    - version: 2\n      kind: none\n    - version: 3\n      kind: column\n      table: eval_runs\n      name: title\n", strings.TrimSuffix(yaml, "\n"), 1)), Options{}); err == nil {
				t.Fatal("应被拒装")
			}
		})
	}
	_ = base
}

// TestParseNoAdoptUnchanged 未声明 adopt ⇒ 行为与既往一致（本机制不介入）
func TestParseNoAdoptUnchanged(t *testing.T) {
	t.Parallel()
	m, _, err := Parse([]byte(validYAML), Options{})
	if err != nil {
		t.Fatalf("无 adopt 段应通过: %v", err)
	}
	if m.Adopt != nil {
		t.Fatalf("无 adopt 段时 Adopt 应为 nil，实际 %+v", m.Adopt)
	}
}
