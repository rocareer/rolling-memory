// plugin: kb v1.0.0
package kb

import (
	"errors"
	"strings"
	"testing"

	"github.com/ai-go-hub/ai-go-admin/plugins/kb/model"
)

// 标签三表管理面的纯函数用例（无库；DB 面由 校验脚本 --tags 覆盖）。
// 覆盖写面校验的闭集与值域：作用域一致性、状态 0/1、匹配类型闭集、名称规整、唯一冲突映射。

func TestNormalizeTagText(t *testing.T) {
	if _, err := normalizeTagText("   ", "组名", maxTagNameLen); err == nil {
		t.Fatal("空名应拒")
	}
	if got, err := normalizeTagText("  标签  ", "标签名", maxTagNameLen); err != nil || got != "标签" {
		t.Fatalf("去空白失败: got=%q err=%v", got, err)
	}
	long := strings.Repeat("字", maxTagNameLen+1)
	if _, err := normalizeTagText(long, "组名", maxTagNameLen); err == nil {
		t.Fatal("超长名应拒（按 rune 计数）")
	}
	if _, err := normalizeTagText(strings.Repeat("字", maxTagNameLen), "组名", maxTagNameLen); err != nil {
		t.Fatalf("边界长度应通过: %v", err)
	}
}

func TestScopeLibraryPair(t *testing.T) {
	cases := []struct {
		name    string
		scope   string
		library uint
		wantErr string // 空 = 期望通过
	}{
		{"平台组恒 0", model.KBTagScopePlatform, 0, ""},
		{"平台组带库拒", model.KBTagScopePlatform, 7, "恒 0"},
		{"库组缺库拒", model.KBTagScopeLibrary, 0, "必填所属库"},
		{"库组带库通过", model.KBTagScopeLibrary, 7, ""},
		{"闭集外拒", "tenant", 1, "未登记"},
	}
	for _, c := range cases {
		err := scopeLibraryPair(c.scope, c.library)
		if c.wantErr == "" {
			if err != nil {
				t.Fatalf("%s: 期望通过，得到 %v", c.name, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), c.wantErr) {
			t.Fatalf("%s: 期望含 %q 的拒绝，得到 %v", c.name, c.wantErr, err)
		}
	}
}

func TestMapStatus(t *testing.T) {
	zero, one, two := uint8(0), uint8(1), uint8(2)
	if got, err := mapStatus(zero); err != nil || got != 0 {
		t.Fatalf("0 应通过: %v", err)
	}
	if got, err := mapStatus(&one); err != nil || got != 1 {
		t.Fatalf("*1 应通过: %v", err)
	}
	if _, err := mapStatus(&two); err == nil {
		t.Fatal("2 应拒（0/1 值域）")
	}
	var nilPtr *uint8
	if _, err := mapStatus(nilPtr); err == nil {
		t.Fatal("显式 null 应拒（0/1 值域）")
	}
	if _, err := mapStatus("1"); err == nil {
		t.Fatal("字符串类型应拒")
	}
}

func TestTagRuleMatchTypesClosedSet(t *testing.T) {
	for _, ok := range []string{model.KBTagRuleMatchPathGlob, model.KBTagRuleMatchFilenameRegex, model.KBTagRuleMatchSuffix, model.KBTagRuleMatchMetaEq} {
		if !tagRuleMatchTypes[ok] {
			t.Fatalf("闭集成员被误拒: %s", ok)
		}
	}
	if tagRuleMatchTypes["glob"] {
		t.Fatal("闭集外取值被误放: glob")
	}
}

func TestMapValueAndMapUint(t *testing.T) {
	m := map[string]any{"library_id": uint(9), "name": "x", "status": (*uint8)(nil)}
	if _, ok, isNull := mapValue(m, "absent"); ok || isNull {
		t.Fatal("未传字段不应出现在 map")
	}
	if v, ok, isNull := mapValue(m, "status"); !ok || !isNull || v == nil {
		t.Fatal("显式 null 应被识别")
	}
	if got, err := mapUint(m["library_id"]); err != nil || got != 9 {
		t.Fatalf("uint 取值失败: %v", err)
	}
	if _, err := mapUint(float64(1.5)); err == nil {
		t.Fatal("带小数浮点应拒")
	}
	if _, err := mapUint(-3); err == nil {
		t.Fatal("负数应拒")
	}
}

func TestIsUniqueViolationAndWriteError(t *testing.T) {
	if isUniqueViolation(nil) {
		t.Fatal("nil 不应判冲突")
	}
	if !isUniqueViolation(errors.New(`ERROR: duplicate key value violates unique constraint "uk_kb_tag_group_scope_library_name" (SQLSTATE 23505)`)) {
		t.Fatal("23505 文案应判冲突")
	}
	err := tagWriteError(errors.New("SQLSTATE 23505"), "同作用域内组名已存在（软删豁免）")
	if err == nil || !strings.Contains(err.Error(), "已存在") {
		t.Fatalf("唯一冲突应换成面内文案，得到 %v", err)
	}
	plain := errors.New("连接中断")
	if got := tagWriteError(plain, "x"); got != plain {
		t.Fatal("非唯一冲突应原样回显")
	}
}
