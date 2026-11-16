// plugin: env-badge v1.0.0
package envbadge

import (
	"strings"
	"testing"

	"github.com/ai-go-hub/ai-go-admin/internal/plugin/cli"
)

// TestBuildInfo 覆盖判定的五个取值格（显式 dev / 显式 prod / 认不出的显式值 / 自动 dev / 自动 prod）：
// 浮标正文、英文标题与判定依据必须与宿主 Environment 一致，且认不出的值要如实标注「按生产处置」。
func TestBuildInfo(t *testing.T) {
	cases := []struct {
		name      string
		env       cli.Environment
		wantLabel string
		wantTitle string
		wantBasis string
	}{
		{
			name:      "显式 dev",
			env:       cli.Environment{Name: cli.EnvDev, Source: cli.EnvSourceConfig, Configured: "dev", LifecycleAllowed: true},
			wantLabel: "DEV",
			wantTitle: "Development",
			wantBasis: "explicit config plugin.env=dev",
		},
		{
			name:      "显式 prod（大小写与空白不改变判定）",
			env:       cli.Environment{Name: cli.EnvProd, Source: cli.EnvSourceConfig, Configured: " PROD ", LifecycleAllowed: false},
			wantLabel: "PROD",
			wantTitle: "Production",
			wantBasis: "explicit config plugin.env=PROD",
		},
		{
			name:      "认不出的显式值按生产处置",
			env:       cli.Environment{Name: cli.EnvProd, Source: cli.EnvSourceConfig, Configured: "staging", LifecycleAllowed: false},
			wantLabel: "PROD",
			wantTitle: "Production",
			wantBasis: `plugin.env="staging" is not recognized`,
		},
		{
			name:      "未配置且源码检出可用",
			env:       cli.Environment{Name: cli.EnvDev, Source: cli.EnvSourceAuto, LifecycleAllowed: true},
			wantLabel: "DEV",
			wantTitle: "Development",
			wantBasis: "source checkout present",
		},
		{
			name:      "未配置且无源码检出",
			env:       cli.Environment{Name: cli.EnvProd, Source: cli.EnvSourceAuto, LifecycleAllowed: false},
			wantLabel: "PROD",
			wantTitle: "Production",
			wantBasis: "no source checkout",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := buildInfo(tc.env, "0.1.0", "1.1.0")
			if got.Name != tc.env.Name {
				t.Errorf("name = %q，期望 %q", got.Name, tc.env.Name)
			}
			if got.Label != tc.wantLabel {
				t.Errorf("label = %q，期望 %q", got.Label, tc.wantLabel)
			}
			if got.Title != tc.wantTitle {
				t.Errorf("title = %q，期望 %q", got.Title, tc.wantTitle)
			}
			if !strings.Contains(got.Basis, tc.wantBasis) {
				t.Errorf("basis = %q，应包含 %q", got.Basis, tc.wantBasis)
			}
			if got.LifecycleAllowed != tc.env.LifecycleAllowed {
				t.Errorf("lifecycle_allowed = %v，期望 %v", got.LifecycleAllowed, tc.env.LifecycleAllowed)
			}
			if got.HostVersion != "0.1.0" || got.HostAPIVersion != "1.1.0" {
				t.Errorf("宿主版本透传错误: %q / %q", got.HostVersion, got.HostAPIVersion)
			}
		})
	}
}
