package cli

import (
	"regexp"
	"strings"
	"testing"

	"github.com/ai-go-hub/ai-go-admin/internal/plugin/host"
)

// TestDefaultActionFor 默认插件状态机的逐格断言：无行安装 / 三态分流 / failed 与脏值 fail-closed。
func TestDefaultActionFor(t *testing.T) {
	cases := []struct {
		name    string
		status  string
		exists  bool
		want    defaultAction
		wantErr string
	}{
		{name: "台账无行 ⇒ 安装", exists: false, want: defaultActionInstall},
		{name: "已启用 ⇒ 无变化", status: host.StatusEnabled, exists: true, want: defaultActionNone},
		{name: "已安装未启用 ⇒ 启用", status: host.StatusInstalled, exists: true, want: defaultActionEnable},
		{name: "已停用 ⇒ 启用", status: host.StatusDisabled, exists: true, want: defaultActionEnable},
		{name: "半装 ⇒ 拒绝清理", status: host.StatusFailed, exists: true, wantErr: "uninstall --force"},
		{name: "四态之外 ⇒ fail-closed", status: "unknown", exists: true, wantErr: "fail-closed"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := defaultActionFor("env-badge", tc.status, tc.exists)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("错误 = %v，应包含 %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("不应报错: %v", err)
			}
			if got != tc.want {
				t.Fatalf("动作 = %v，期望 %v", got, tc.want)
			}
		})
	}
}

// TestDefaultPluginsDeclared 默认依赖插件清单的形态：非空、名字合法、无重复、包含 env-badge。
// 清单是 host.DefaultPlugins 的唯一正本；名字形态与 manifest 的插件名规则同口径（这里不 import
// manifest 以避免循环，规则本身是稳定的公开契约）。
func TestDefaultPluginsDeclared(t *testing.T) {
	if len(host.DefaultPlugins) == 0 {
		t.Fatal("默认依赖插件清单为空")
	}
	namePattern := regexp.MustCompile(`^[a-z][a-z0-9-]{1,31}$`)
	seen := make(map[string]struct{}, len(host.DefaultPlugins))
	for _, name := range host.DefaultPlugins {
		if !namePattern.MatchString(name) {
			t.Errorf("默认插件名 %q 不符合 ^[a-z][a-z0-9-]{1,31}$", name)
		}
		if _, dup := seen[name]; dup {
			t.Errorf("默认插件名 %q 重复", name)
		}
		seen[name] = struct{}{}
	}
	if !host.IsDefaultPlugin("env-badge") {
		t.Error("env-badge 应是默认依赖插件")
	}
	if host.IsDefaultPlugin("not-a-default") {
		t.Error("不在清单里的名字不应被判为默认插件")
	}
}
