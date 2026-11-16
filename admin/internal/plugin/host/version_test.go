package host

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
)

func TestInRange(t *testing.T) {
	cases := []struct {
		domain  string
		version string
		want    bool
	}{
		{">=1 <2", "1.0.0", true},
		{">=1 <2", "1.9.9", true},
		{">=1 <2", "2.0.0", false},
		{">=1 <2", "0.9.9", false},
		{">=1.2, <1.3", "1.2.5", true},
		{">=1.2, <1.3", "1.3.0", false},
		{"1.0.0", "1.0.0", true},
		{"1.0.0", "v1.0.0", true},
		{"1.0.0", "1.0.1", false},
		{">1", "1.0.0", false},
		{">1", "1.0.1", true},
		{"<=2", "3.0.0", false},
		{">=1", "1.2.3", true},
	}
	for _, c := range cases {
		got, err := InRange(c.domain, c.version)
		if err != nil {
			t.Fatalf("InRange(%q, %q) 报错: %v", c.domain, c.version, err)
		}
		if got != c.want {
			t.Errorf("InRange(%q, %q) = %v, want %v", c.domain, c.version, got, c.want)
		}
	}
}

func TestInRangeRejects(t *testing.T) {
	cases := []struct{ domain, version string }{
		{"", "1.0.0"},            // 空域
		{"   ", "1.0.0"},         // 空白域
		{">=1 || <3", "1.0.0"},   // 或语义不支持
		{"^1.2", "1.0.0"},        // 插入语义不支持
		{"~1.2", "1.0.0"},        // 插入语义不支持
		{">=", "1.0.0"},          // 缺少版本号
		{">=1 <2", "1.0.0-beta"}, // 预发布标签不支持
		{">=1", "abc"},           // 非数字段
		{">=1", "1.2.3.4"},       // 段数超过 3
		{">=1", "-1.0.0"},        // 负数段
	}
	for _, c := range cases {
		if _, err := InRange(c.domain, c.version); err == nil {
			t.Errorf("InRange(%q, %q) 应报错（fail-closed）", c.domain, c.version)
		}
	}
}

func TestCompatible(t *testing.T) {
	// 与自身版本同界必然相容（不写死具体号，APIVersion 递增时不误伤用例）
	if err := Compatible(">=" + APIVersion); err != nil {
		t.Fatalf("Compatible(>=%s) = %v, want nil", APIVersion, err)
	}

	// 下一主版本与当前机制接口不相容
	next := nextMajorVersion(t, APIVersion)
	if err := Compatible(">=" + next); err == nil {
		t.Errorf("Compatible(>=%s) 应与 APIVersion=%s 不相容", next, APIVersion)
	}

	if err := Compatible(""); err == nil {
		t.Error("空 host_api 域应报错")
	}
}

// nextMajorVersion 返回 major+1.0.0（构造「必然不相容」的域）
func nextMajorVersion(t *testing.T, version string) string {
	t.Helper()
	major, err := strconv.Atoi(strings.SplitN(version, ".", 2)[0])
	if err != nil {
		t.Fatalf("APIVersion %q 首段不是整数: %v", version, err)
	}
	return fmt.Sprintf("%d.0.0", major+1)
}
