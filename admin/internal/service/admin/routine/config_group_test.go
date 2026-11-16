package routine

import (
	"testing"

	repoCommon "github.com/ai-go-hub/ai-go-admin/internal/repository/common"
)

// TestConfigGroupRegistered 钉住通用配置写口的分组归属校验：
// 只允许 config_group 里登记的分组，插件自管分组（plugin.ai）与任意未知分组一律拒绝。
func TestConfigGroupRegistered(t *testing.T) {
	groups := []repoCommon.ConfigGroupItem{
		{Key: "basic", Value: "基础配置"},
		{Key: "mail", Value: "邮件配置"},
	}
	cases := []struct {
		group string
		want  bool
	}{
		{group: "basic", want: true},
		{group: "mail", want: true},
		{group: "plugin.ai", want: false},
		{group: "", want: false},
		{group: "unknown", want: false},
	}
	for _, tc := range cases {
		if got := configGroupRegistered(groups, tc.group); got != tc.want {
			t.Fatalf("configGroupRegistered(%q) = %v，期望 %v", tc.group, got, tc.want)
		}
	}
	if configGroupRegistered(nil, "basic") {
		t.Fatal("空分组表不得放行任何分组")
	}
}
