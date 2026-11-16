package host

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
)

// identifierPattern 实体名与字段名的闭集标识符（硬条款：列名逐字段复核）。
// 实体名同口径——它同时进入物理表名与路由段，任何用户输入都不得进入标识符位置。
var identifierPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

// 以下键集与值域全部照抄（该处声明为唯一定义处，别处只引用）：
var (
	// specEntityKeys 实体级键集
	specEntityKeys = []string{"entity", "title_zh", "title_en", "drag_sort", "ddl_owner", "fields"}
	// specFieldKeys 字段级键集
	specFieldKeys = []string{"name", "type", "title_zh", "title_en", "required", "length", "default",
		"unique", "index", "list", "sort", "search", "quickSearch", "form", "dict", "trgm"}
	// specFieldTypes 字段类型闭集（type 9 种）
	specFieldTypes = []string{"string", "text", "int", "bigint", "decimal", "bool", "date", "datetime", "enum"}
	// specFieldBoolKeys 值为布尔的字段级键（required/unique/index/list/sort/quickSearch/trgm）
	// ——键集闭集只保证「键名在闭集内」，值的类型必须另行断言，否则 drag_sort: "yes" 这类明显非法的
	// spec 会通过启动预检（spec 非法 ⇒ 启动失败）。
	specFieldBoolKeys = []string{"required", "unique", "index", "list", "sort", "quickSearch", "trgm"}
	// specTitleKeys 值为字符串的标题键（实体级与字段级同名；双语元数据随响应下发，类型错会让渲染层收到非字符串）
	specTitleKeys = []string{"title_zh", "title_en"}
	// specForms 表单控件闭集（form 8 项）
	specForms = []string{"input", "textarea", "input-number", "switch", "date", "datetime", "select", "radio"}
	// specSearchOps 搜索操作符闭集 = 宿主词表 16 项原文（与 internal/service/base.go 的
	// GetOperatorByAlias 同一套）。大小写敏感：宿主对词表外的写法返回空串、条件被静默丢弃，
	// 所以这里必须拒装（例：gte、like 小写都不在词表内）。
	specSearchOps = []string{"eq", "ne", "gt", "egt", "lt", "elt", "LIKE", "NOT LIKE", "ILIKE", "NOT ILIKE",
		"IN", "NOT IN", "BETWEEN", "NOT BETWEEN", "NULL", "NOT NULL"}
	// specSearchKeys search 子键闭集（示例只含 op）
	specSearchKeys = []string{"op"}
	// specDDLOwners ddl_owner 闭集
	specDDLOwners = []string{"engine", "migrations"}
	// specDictKeys dict 条目键集（示例：{value,label_zh,label_en}）
	specDictKeys = []string{"value", "label_zh", "label_en"}
)

// SpecValidator 实体 spec 校验器（-②：spec 无法装载/非法 ⇒ 启动失败并点名实体）。
type SpecValidator func(entity Entity) error

var (
	specMu        sync.RWMutex
	specValidator SpecValidator
)

// SetSpecValidator 注册**追加**校验器（传 nil 清空）：它在内置闭集校验（validateSpec）之后执行，
// 命中即拒——语义是「更严」，不是「替换」。这条语义是 L2 引擎（internal/plugin/l2）与宿主共用同一份
// 判定的前提：引擎在此注册自己的增补规则（form↔type 映射、enum 必须有 dict、保留列名、trgm、长度预算、
// 物理表名一致性），安装期写库校验（cli 的 host.ValidateSpec 探测）与启动预检（Preflight）因此走同一条链；
// 若做成替换语义，注册方要么丢掉宿主闭集校验、要么被迫复制一份宿主规则——两处各写一套必然漂移
// （manifest/fields.go 的设计意图同款）。
func SetSpecValidator(v SpecValidator) {
	specMu.Lock()
	defer specMu.Unlock()
	specValidator = v
}

// ValidateSpec 校验实体 spec（供启动预检、安装期写库与 CLI 复用）：
// 内置闭集校验先行（永不被追加校验器绕过），随后依次执行已注册的追加校验器。
func ValidateSpec(entity Entity) error {
	if err := validateSpec(entity); err != nil {
		return err
	}
	specMu.RLock()
	extra := specValidator
	specMu.RUnlock()
	if extra == nil {
		return nil
	}
	return extra(entity)
}

// validateSpec 内置校验器（完整闭集校验；ValidateSpec 里**永远先跑**，追加校验器绕不过它）——
// 实体/字段名标识符、键集闭集（多余键一律拒装）、键值类型（字符串/布尔/整数）、
// type/form/search.op/dict/ddl_owner 值域。
func validateSpec(entity Entity) error {
	if !identifierPattern.MatchString(entity.Name) {
		return fmt.Errorf("实体名 %q 不是合法标识符（^[a-z][a-z0-9_]{0,62}$）", entity.Name)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(entity.Spec, &raw); err != nil {
		return fmt.Errorf("spec 不是合法 JSON 对象: %w", err)
	}
	if err := checkSpecKeys(raw, specEntityKeys, ""); err != nil {
		return err
	}

	if _, ok := raw["entity"]; !ok {
		return errors.New("spec.entity 缺失")
	}
	name, err := decodeString(raw["entity"])
	if err != nil {
		return fmt.Errorf("spec.entity 不是字符串: %w", err)
	}
	if name != entity.Name {
		return fmt.Errorf("spec.entity=%q 与实体行 name=%q 不一致", name, entity.Name)
	}

	// 实体级标题与 drag_sort 的类型（值域同示例：标题字符串、drag_sort 布尔）
	if err := checkSpecStrings(raw, specTitleKeys, "spec"); err != nil {
		return err
	}
	if err := checkSpecBoolKeys(raw, []string{"drag_sort"}, "spec"); err != nil {
		return err
	}

	if owner, ok := raw["ddl_owner"]; ok {
		value, err := decodeString(owner)
		if err != nil {
			return fmt.Errorf("spec.ddl_owner 不是字符串: %w", err)
		}
		if !slices.Contains(specDDLOwners, value) {
			return fmt.Errorf("spec.ddl_owner=%q 不在闭集内（%s）", value, strings.Join(specDDLOwners, "|"))
		}
	}

	if _, ok := raw["fields"]; !ok {
		return errors.New("spec.fields 缺失：实体至少声明一个字段")
	}
	var fields []map[string]json.RawMessage
	if err := json.Unmarshal(raw["fields"], &fields); err != nil {
		return fmt.Errorf("spec.fields 不是对象数组: %w", err)
	}
	if len(fields) == 0 {
		return errors.New("spec.fields 为空：实体至少声明一个字段")
	}

	seen := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		if err := validateSpecField(field, seen); err != nil {
			return err
		}
	}
	return nil
}

// validateSpecField 校验单个字段 spec
func validateSpecField(field map[string]json.RawMessage, seen map[string]struct{}) error {
	if err := checkSpecKeys(field, specFieldKeys, "spec.fields[]"); err != nil {
		return err
	}

	if _, ok := field["name"]; !ok {
		return errors.New("spec.fields[] 缺 name")
	}
	name, err := decodeString(field["name"])
	if err != nil {
		return fmt.Errorf("spec.fields[].name 不是字符串: %w", err)
	}
	if !identifierPattern.MatchString(name) {
		return fmt.Errorf("字段名 %q 不是合法标识符（^[a-z][a-z0-9_]{0,62}$）", name)
	}
	if _, dup := seen[name]; dup {
		return fmt.Errorf("字段名 %q 重复声明", name)
	}
	seen[name] = struct{}{}

	// 字段级标题/布尔键/长度键的类型（键集闭集不校验类型，此处补齐——
	// 形如 {"length":"abc","unique":"yes"} 的 spec 必须拒）
	if err := checkSpecStrings(field, specTitleKeys, "字段 "+name); err != nil {
		return err
	}
	if err := checkSpecBoolKeys(field, specFieldBoolKeys, "字段 "+name); err != nil {
		return err
	}
	if length, ok := field["length"]; ok {
		if err := checkSpecPositiveInt(length, "字段 "+name+" 的 length"); err != nil {
			return err
		}
	}

	if _, ok := field["type"]; !ok {
		return fmt.Errorf("字段 %s 缺 type", name)
	}
	fieldType, err := decodeString(field["type"])
	if err != nil {
		return fmt.Errorf("字段 %s 的 type 不是字符串: %w", name, err)
	}
	if !slices.Contains(specFieldTypes, fieldType) {
		return fmt.Errorf("字段 %s 的类型 %q 不在闭集内（%s）", name, fieldType, strings.Join(specFieldTypes, "|"))
	}

	if form, ok := field["form"]; ok {
		value, err := decodeString(form)
		if err != nil {
			return fmt.Errorf("字段 %s 的 form 不是字符串: %w", name, err)
		}
		if !slices.Contains(specForms, value) {
			return fmt.Errorf("字段 %s 的 form=%q 不在闭集内（%s）", name, value, strings.Join(specForms, "|"))
		}
	}

	if search, ok := field["search"]; ok {
		var sub map[string]json.RawMessage
		if err := json.Unmarshal(search, &sub); err != nil {
			return fmt.Errorf("字段 %s 的 search 不是对象: %w", name, err)
		}
		if err := checkSpecKeys(sub, specSearchKeys, "字段 "+name+" 的 search"); err != nil {
			return err
		}
		op, err := decodeString(sub["op"])
		if err != nil {
			return fmt.Errorf("字段 %s 的 search.op 不是字符串: %w", name, err)
		}
		if !slices.Contains(specSearchOps, op) {
			return fmt.Errorf("字段 %s 的 search.op=%q 不在宿主操作符词表内（%s）",
				name, op, strings.Join(specSearchOps, "|"))
		}
	}

	if dict, ok := field["dict"]; ok {
		if err := validateSpecDict(name, dict); err != nil {
			return err
		}
	}
	return nil
}

// validateSpecDict 校验 dict 形状：对象数组，每条恰含 value / label_zh / label_en
func validateSpecDict(fieldName string, dict json.RawMessage) error {
	var entries []map[string]json.RawMessage
	if err := json.Unmarshal(dict, &entries); err != nil {
		return fmt.Errorf("字段 %s 的 dict 不是对象数组: %w", fieldName, err)
	}
	if len(entries) == 0 {
		return fmt.Errorf("字段 %s 的 dict 为空数组", fieldName)
	}
	for i, entry := range entries {
		if err := checkSpecKeys(entry, specDictKeys, fmt.Sprintf("字段 %s 的 dict[%d]", fieldName, i)); err != nil {
			return err
		}
		for _, key := range []string{"label_zh", "label_en"} {
			if _, err := decodeString(entry[key]); err != nil {
				return fmt.Errorf("字段 %s 的 dict[%d].%s 不是字符串: %w", fieldName, i, key, err)
			}
		}
		// value 允许数字或字符串（enum 值域两种都见）
		if _, ok := entry["value"]; !ok {
			return fmt.Errorf("字段 %s 的 dict[%d] 缺 value", fieldName, i)
		}
		var probe any
		if err := json.Unmarshal(entry["value"], &probe); err != nil {
			return fmt.Errorf("字段 %s 的 dict[%d].value 不是合法 JSON: %w", fieldName, i, err)
		}
		switch probe.(type) {
		case float64, string:
		default:
			return fmt.Errorf("字段 %s 的 dict[%d].value 必须是数字或字符串", fieldName, i)
		}
	}
	return nil
}

// checkSpecKeys 键集闭集校验：出现未列出的键即拒绝（「多余键一律拒装」）
func checkSpecKeys(raw map[string]json.RawMessage, allowed []string, where string) error {
	unknown := make([]string, 0)
	for key := range raw {
		if !slices.Contains(allowed, key) {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)
	if where != "" {
		where += " "
	}
	return fmt.Errorf("%s出现闭集外的键 %s（键集）", where, strings.Join(unknown, ", "))
}

// checkSpecStrings 逐个键做字符串类型校验（缺失即跳过：可选项的缺省由各键语义决定；null 拒收）
func checkSpecStrings(raw map[string]json.RawMessage, keys []string, where string) error {
	for _, key := range keys {
		value, ok := raw[key]
		if !ok {
			continue
		}
		if isJSONNull(value) {
			return fmt.Errorf("%s 的 %s 是 null（该键值域为字符串）", where, key)
		}
		if _, err := decodeString(value); err != nil {
			return fmt.Errorf("%s 的 %s 不是字符串: %w", where, key, err)
		}
	}
	return nil
}

// checkSpecBoolKeys 逐个键做布尔类型校验（形如 "yes"/"true"/1/null 一律拒绝，json 只接受 true/false）
func checkSpecBoolKeys(raw map[string]json.RawMessage, keys []string, where string) error {
	for _, key := range keys {
		value, ok := raw[key]
		if !ok {
			continue
		}
		if isJSONNull(value) {
			return fmt.Errorf("%s 的 %s 是 null（该键值域为 true/false）", where, key)
		}
		var b bool
		if err := json.Unmarshal(value, &b); err != nil {
			return fmt.Errorf("%s 的 %s 不是布尔值（true/false）: %w", where, key, err)
		}
	}
	return nil
}

// checkSpecPositiveInt 整数键的类型校验：必须为 JSON 数字、非负且无小数
// （`"128"` 字符串写法与 `1.5`、`-1`、`null` 一律拒绝；示例把 length 写为数字）
func checkSpecPositiveInt(value json.RawMessage, where string) error {
	if isJSONNull(value) {
		return fmt.Errorf("%s 是 null（该键值域为非负整数）", where)
	}
	var number float64
	if err := json.Unmarshal(value, &number); err != nil {
		return fmt.Errorf("%s 不是数字: %w", where, err)
	}
	if number < 0 || number != math.Trunc(number) {
		return fmt.Errorf("%s 必须是非负整数（实际 %v）", where, number)
	}
	return nil
}

// isJSONNull 该值是否是 JSON null（json.Unmarshal 对 null 是 no-op：不解此判定，null 会被当成「零值通过」）
func isJSONNull(value json.RawMessage) bool {
	return string(bytes.TrimSpace(value)) == "null"
}

// decodeString 解出字符串值（缺失或类型不符都给可读错误）
func decodeString(raw json.RawMessage) (string, error) {
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", err
	}
	return value, nil
}
