package plugin

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/ai-go-hub/ai-go-admin/internal/model"
	pluginCli "github.com/ai-go-hub/ai-go-admin/internal/plugin/cli"
	"github.com/ai-go-hub/ai-go-admin/internal/plugin/host"
)

// 纯单元测试（不连库、默认跑）：钉住管理面的两个契约——
//  1. buildRow 的差异码与注册行标记（与 internal/plugin/cli 的 driftNotes 同口径）；
//  2. Row 的 JSON 键名（前端 web/src/api/plugin/index.ts 的形状依赖它，改动即破坏前端契约）。

// rowRuntime 构造三处真相视图（registered 为空表示注册文件不可读）
func rowRuntime(wired []string, registered []string, available bool) *Runtime {
	if wired == nil {
		wired = []string{}
	}
	if registered == nil {
		registered = []string{}
	}
	return &Runtime{RegistryAvailable: available, Wired: wired, Registered: registered, Orphans: []string{}}
}

// driftOf 取差异码集合
func driftOf(codes []string) map[string]bool {
	set := make(map[string]bool, len(codes))
	for _, code := range codes {
		set[code] = true
	}
	return set
}

func TestBuildRowDrift(t *testing.T) {
	cases := []struct {
		name       string
		plugin     model.Plugin
		rt         *Runtime
		wired      bool
		registered *bool
		drift      []string
	}{
		{
			name:   "三处一致：已接线 + 有注册行 ⇒ 无差异",
			plugin: model.Plugin{Name: "demo", Kind: "code", Status: host.StatusEnabled},
			rt:     rowRuntime([]string{"demo"}, []string{"demo"}, true),
			wired:  true, registered: boolPtr(true), drift: nil,
		},
		{
			name:   "待重编译 + 缺注册行（cli.driftNotes 同口径，两条并报）",
			plugin: model.Plugin{Name: "demo", Kind: "code", Status: host.StatusEnabled},
			rt:     rowRuntime(nil, nil, true),
			wired:  false, registered: boolPtr(false),
			drift: []string{DriftUnwired, DriftMissingRegister},
		},
		{
			name:   "注册文件不可读 ⇒ registered=nil 且不报「缺注册行」（不猜）",
			plugin: model.Plugin{Name: "demo", Kind: "code", Status: host.StatusInstalled},
			rt:     rowRuntime(nil, nil, false),
			wired:  false, registered: nil,
			drift: []string{DriftUnwired},
		},
		{
			name:   "声明式插件：无 Go 注册行 ⇒ 不报「待重编译」（误报）",
			plugin: model.Plugin{Name: "decl", Kind: "declarative", Status: host.StatusEnabled},
			rt:     rowRuntime(nil, nil, true),
			wired:  false, registered: boolPtr(false), drift: nil,
		},
		{
			name:   "声明式插件却有注册行 ⇒ 报「不应有注册行」（go build 会失败）",
			plugin: model.Plugin{Name: "decl", Kind: "declarative", Status: host.StatusInstalled},
			rt:     rowRuntime(nil, []string{"decl"}, true),
			wired:  false, registered: boolPtr(true),
			drift: []string{DriftDeclarativeRegister},
		},
		{
			name:   "半装（failed）：差异码点名清理入口",
			plugin: model.Plugin{Name: "demo", Kind: "code", Status: host.StatusFailed},
			rt:     rowRuntime(nil, nil, true),
			wired:  false, registered: boolPtr(false),
			drift: []string{DriftUnwired, DriftMissingRegister, DriftFailed},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			row := buildRow(&tc.plugin, 3, tc.rt)

			if row.Entities != 3 {
				t.Fatalf("Entities = %d，期望 3", row.Entities)
			}
			if row.Wired != tc.wired {
				t.Fatalf("Wired = %v，期望 %v", row.Wired, tc.wired)
			}
			switch {
			case tc.registered == nil && row.Registered != nil:
				t.Fatalf("Registered = %v，期望 nil（注册文件不可读）", *row.Registered)
			case tc.registered != nil && row.Registered == nil:
				t.Fatalf("Registered = nil，期望 %v", *tc.registered)
			case tc.registered != nil && *row.Registered != *tc.registered:
				t.Fatalf("Registered = %v，期望 %v", *row.Registered, *tc.registered)
			}

			got := driftOf(row.Drift)
			want := driftOf(tc.drift)
			if len(got) != len(want) {
				t.Fatalf("Drift = %v，期望 %v", row.Drift, tc.drift)
			}
			for code := range want {
				if !got[code] {
					t.Fatalf("Drift = %v，缺少 %s", row.Drift, code)
				}
			}
			if row.Drift == nil {
				t.Fatal("Drift 为 nil：JSON 会输出 null，前端按数组处理（必须为空数组）")
			}
		})
	}
}

func TestRowJSONKeys(t *testing.T) {
	row := buildRow(&model.Plugin{ID: 7, Name: "demo", Kind: "code", Status: host.StatusEnabled}, 2, rowRuntime([]string{"demo"}, []string{"demo"}, true))

	payload, err := json.Marshal(row)
	if err != nil {
		t.Fatalf("序列化 Row 失败: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("反序列化 Row 失败: %v", err)
	}

	// 台账列（model.Plugin 的 json tag，snake_case）
	for _, key := range []string{"id", "name", "title", "version", "kind", "status", "host_api", "host_version",
		"framework_req", "requires", "checksum", "source", "signed_by", "installed_at", "updated_at", "remark",
		"adopted_through"} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("Row JSON 缺键 %q（进行中的契约，前端 web/src/api/plugin/index.ts 依赖）", key)
		}
	}
	// 运行时标记
	for _, key := range []string{"entities", "wired", "registered", "drift"} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("Row JSON 缺运行时键 %q", key)
		}
	}
	if decoded["registered"] != true {
		t.Fatalf("registered = %v，期望 true", decoded["registered"])
	}
}

// boolPtr 取地址（表驱动用例里的 registered 期望值）
func boolPtr(value bool) *bool {
	return &value
}

// TestUninstallOptionsMapping 卸载参数 → CLI 旗标映射（**唯一映射点**）：
//   - Purge 与 Yes 必须成对——HTTP 通道的「二次确认」由界面对话框勾选承担，
//     但 CLI 侧「--purge 必配 --yes」的门禁不得被绕过（杀错方向：purge=true/yes=false 会被 CLI 拒）；
//   - 非 purge 请求不得顺带把 Yes 带成 true（否则等于替用户确认了一次没发生的动作）；
//   - KeepFiles / Force / Operator 原样透传（failed 半装的 force 语义由 CLI 侧判定，本层不复制规则）。
func TestUninstallOptionsMapping(t *testing.T) {
	cases := []struct {
		name     string
		params   UninstallParams
		operator string
		want     pluginCli.UninstallOptions
	}{
		{
			name:     "默认（保数据保文件、非强制）：Purge/Yes 均为 false",
			params:   UninstallParams{},
			operator: "admin",
			want:     pluginCli.UninstallOptions{Operator: "admin"},
		},
		{
			name:     "彻底删除：Purge 与 Yes 强制成对",
			params:   UninstallParams{Purge: true, KeepFiles: false, Force: false},
			operator: "admin",
			want:     pluginCli.UninstallOptions{Purge: true, Yes: true, Operator: "admin"},
		},
		{
			name:     "保留文件 + 半装强制：原样透传",
			params:   UninstallParams{KeepFiles: true, Force: true},
			operator: "albert",
			want:     pluginCli.UninstallOptions{KeepFiles: true, Force: true, Operator: "albert"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := uninstallOptions(tc.params, tc.operator)
			if got != tc.want {
				t.Fatalf("uninstallOptions = %+v，期望 %+v", got, tc.want)
			}
		})
	}
}

// TestUninstallEffect 卸载回执的生效方式码：声明式无 Go 注册行（台账闸即时生效）；
// code/mixed 注册行已撤、需重编译 + 重启（与 enable/disable 的 effect 口径一致）。
func TestUninstallEffect(t *testing.T) {
	if got := uninstallEffect("declarative"); got != EffectGateTTL {
		t.Fatalf("declarative 的 effect = %s，期望 %s", got, EffectGateTTL)
	}
	for _, kind := range []string{"code", "mixed"} {
		if got := uninstallEffect(kind); got != EffectRebuildRequired {
			t.Fatalf("%s 的 effect = %s，期望 %s", kind, got, EffectRebuildRequired)
		}
	}
}

// TestUninstallRejectsEmptyName 空名先在服务层拒绝（不触库）：卸载对象必须点名
func TestUninstallRejectsEmptyName(t *testing.T) {
	svc := &Service{}
	if _, err := svc.Uninstall(context.Background(), "  ", UninstallParams{}, "admin"); err == nil {
		t.Fatal("空插件名应被拒绝")
	}
}
