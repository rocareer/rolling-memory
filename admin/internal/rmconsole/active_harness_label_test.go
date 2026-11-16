// plugin: rolling-memory v0.0.58
package rollingmemory

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDetectActiveHarnessLabelHonestDefault 锁定诚实口径：无 presence 文件 / 无 carrier
// 一律回空串（界面显示「未知」），严禁硬编码回退 "ZCode"。16 机器实测正是
// 「注册表缺 workbuddy 条目 + 并列 0 分取第 0 位 + 默认回退」让从未安装 ZCode 的
// WorkBuddy 机器显示「ZCode 激活」；carrier "cli" 是宿主未能自称身份时的通用命令行口径。
func TestDetectActiveHarnessLabelHonestDefault(t *testing.T) {
	home := t.TempDir()
	if got := detectActiveHarnessLabel(home); got != "" {
		t.Fatalf("missing presence file: label = %q, want empty (unknown)", got)
	}
	stateDir := filepath.Join(home, ".local", "state", "rolling-memory")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("mkdir state dir: %v", err)
	}
	statePath := filepath.Join(stateDir, "active_workspace.json")
	cases := []struct{ carrier, want string }{
		{"harness:workbuddy", "WorkBuddy"},
		{"harness:zcode", "ZCode"},
		{"harness:cli", "CLI"},
		{"", ""},
	}
	for _, tc := range cases {
		if err := os.WriteFile(statePath, []byte(`{"carrier":"`+tc.carrier+`"}`), 0o600); err != nil {
			t.Fatalf("write presence: %v", err)
		}
		if got := detectActiveHarnessLabel(home); got != tc.want {
			t.Fatalf("carrier %q: label = %q, want %q", tc.carrier, got, tc.want)
		}
	}
}
