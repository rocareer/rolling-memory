package manifest

import (
	"fmt"
	"slices"
	"strings"
)

// 字段级值域闭集（**唯一定义处**）。
//
// 为什么要在这里定义：清单（plugin.yaml）是声明式实体的作者入口，安装准入必须能拒掉「非法值域」的清单
// （：「词表外的写法（如 gte、like 小写）一律拒装」——宿主侧遇到未知操作符返回空串、条件被
// 静默丢弃，不能靠它兜底）。L2 引擎的 spec 校验器（P3，经 host.SetSpecValidator 同时服务安装期与
// 启动预检）应**复用**本包的集合与 ValidateField，而不是另写一套——两处各写一套必然漂移。
var (
	// FieldTypes 字段类型闭集（9 种）
	FieldTypes = []string{"string", "text", "int", "bigint", "decimal", "bool", "date", "datetime", "enum"}
	// FieldForms 表单控件闭集（8 种）
	FieldForms = []string{"input", "textarea", "input-number", "switch", "date", "datetime", "select", "radio"}
	// SearchOperators 搜索操作符闭集 = **宿主词表 16 项原文**（web/types/table.d.ts 的 OperatorStr，
	// 与 internal/service/base.go 的 GetOperatorByAlias 同一套）；大小写敏感——
	// `gte`（不在词表）、`like`（小写）一律拒装。
	SearchOperators = []string{
		"eq", "ne", "gt", "egt", "lt", "elt", "LIKE", "NOT LIKE", "ILIKE", "NOT ILIKE",
		"IN", "NOT IN", "BETWEEN", "NOT BETWEEN", "NULL", "NOT NULL",
	}
)

// formsByType 控件映射闭集（「控件映射（闭集，页面按 type/form 分派）」）：
// 类型 → 允许的表单控件。映射表只列 string+input 与 text+textarea，故 string 只接 input
// （长文本请声明 `type: text`，其控件才是 textarea）；数值型的 select/radio 用于带 dict 的数值枚举
// （如 status 0/1/2）；字符串枚举请声明 `type: enum`（配 dict，页面按枚举渲染）。
var formsByType = map[string][]string{
	"string":   {"input"},
	"text":     {"textarea"},
	"int":      {"input-number", "select", "radio"},
	"bigint":   {"input-number", "select", "radio"},
	"decimal":  {"input-number", "select", "radio"},
	"bool":     {"switch"},
	"date":     {"date"},
	"datetime": {"datetime"},
	"enum":     {"select", "radio"},
}

// dictForms 由 dict 驱动的控件（字典只服务选择类控件）
var dictForms = []string{"select", "radio"}

// fuzzyOperators 前后通配类操作符（L2 引擎在 v1 内按前缀匹配，trgm 才开前后通配；此处只作词表分类）
var fuzzyOperators = []string{"LIKE", "NOT LIKE", "ILIKE", "NOT ILIKE"}

// ValidateField 校验单个字段的值域闭集：type ∈ 9 / form ∈ 8 且与 type 匹配（控件映射）、
// search.op ∈ 16 项词表原文、dict 形状（非空、双语文案、value 非空）与 enum 的字典要求。
// 错误信息可直接展示给插件作者；L2 的 spec 校验器（P3）应复用本函数。
//
// dict 的适用面（字段示例与状态列 DDL 示例）：**任意 form 都可声明 dict**。
// 渲染驱动只存在于 select/radio（选项来自 dict）；其余控件下 dict 只作「值→文案」映射——
// 进响应元数据与列注释（`状态:0=停用,1=启用`，AGENTS 状态列注释口径），不驱动渲染。
// 早期实现额外要求「dict ⇒ form 必须是 select/radio」，与示例（form: input 带 dict）
// 及bool 状态列（form: switch + dict 出列注释）相悖，故移除该耦合。
func ValidateField(field Field) error {
	if !slices.Contains(FieldTypes, field.Type) {
		return fmt.Errorf("type %q 不在闭集内（%s）", field.Type, strings.Join(FieldTypes, "|"))
	}
	if field.Form != "" && !slices.Contains(FieldForms, field.Form) {
		return fmt.Errorf("form %q 不在闭集内（%s）", field.Form, strings.Join(FieldForms, "|"))
	}
	if field.Form != "" {
		if allowed := formsByType[field.Type]; !slices.Contains(allowed, field.Form) {
			return fmt.Errorf("form %q 与 type %q 不匹配（控件映射：%s）", field.Form, field.Type, strings.Join(allowed, "|"))
		}
	}

	if field.Search != nil {
		if !slices.Contains(SearchOperators, field.Search.Op) {
			return fmt.Errorf("search.op %q 不在宿主 16 项词表内（大小写敏感，词表外的写法一律拒装：宿主侧未知词会被静默丢弃）", field.Search.Op)
		}
	}

	for i, item := range field.Dict {
		if item.Value == nil {
			return fmt.Errorf("dict[%d] 缺少 value", i)
		}
		if strings.TrimSpace(item.LabelZh) == "" || strings.TrimSpace(item.LabelEn) == "" {
			return fmt.Errorf("dict[%d]（value=%v）必须同时给 label_zh 与 label_en（L2 文案走后端双语元数据）", i, item.Value)
		}
	}
	if field.Type == "enum" {
		if len(field.Dict) == 0 {
			return fmt.Errorf("type=enum 必须声明 dict（枚举页由 dict 驱动渲染）")
		}
		if !slices.Contains(dictForms, field.Form) {
			return fmt.Errorf("type=enum 的 form 必须是 %s", strings.Join(dictForms, "|"))
		}
	}
	return nil
}
