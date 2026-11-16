package plugin

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-go-hub/ai-go-admin/internal/plugin/cli"
)

// TestParseVersionStrict migrate-force 的版本号必须是纯整数（F11：拒绝 "1.5"/"2x"/"0x10" 这类
// 前缀命中写法——Sscanf("%d") 会把它们解成 1/2/0，等于悄悄 force 到意料外的版本）。
func TestParseVersionStrict(t *testing.T) {
	accepts := map[string]int{
		"0":   0,
		"7":   7,
		"+7":  7,
		"-1":  -1, // golang-migrate 的 Force(-1) = 回退到「无版本」
		" 12": 12, // 两侧空白 trim 后接受
	}
	for raw, want := range accepts {
		got, err := parseVersion(raw)
		if err != nil {
			t.Errorf("parseVersion(%q) 应成功: %v", raw, err)
			continue
		}
		if got != want {
			t.Errorf("parseVersion(%q) = %d, want %d", raw, got, want)
		}
	}

	rejects := []string{"1.5", "2x", "0x10", "", " ", "1 2", "v1", "1e3", "--1", "１"}
	for _, raw := range rejects {
		if got, err := parseVersion(raw); err == nil {
			t.Errorf("parseVersion(%q) 必须拒绝（前缀命中会把打错的版本悄悄 force），实际解出 %d", raw, got)
		}
	}
}

// TestInstallCmdWiresFailureSanitizer 钉住 dev 通道（`aigo plugin install`）的失败审计脱敏接线：
// install 子命令必须把 cli.SanitizePluginText（与 catalog 通道同一条实现，即 source.SanitizePaths）
// 塞进 InstallOptions.Sanitize，并把命令行给的源路径当 extra（与 catalog 通道传 src.Location 同口径）。
// 不接这一行，安装失败的原因会带着 staging 解包目录、落位目标与包路径这些绝对路径落库
// （admin_plugin_actions.detail 会在插件管理页的动作审计区块展示，第 7 条）。
//
// 为什么本用例驱动的是 installCmd() 而不是某个抽出的小函数：接线只有在 RunE 里被赋值才生效，
// 删掉 cmd/plugin/plugin.go 里那行 `opts.Sanitize = ...` 必须让本用例变红。为此 plugin.go 把
// 「构造 env」与「执行安装」两个叶子调用做成了可注入变量（newPluginEnv / runPluginInstall）：
// 本用例只替换这两处，命令构造、旗标绑定与 RunE 全部走生产路径（不连库、不落盘、不改接线文件）。
func TestInstallCmdWiresFailureSanitizer(t *testing.T) {
	repoRoot := filepath.Join(string(filepath.Separator), "repo")
	source := filepath.Join(string(filepath.Separator), "pkg", "demo-1.0.0.zip")

	var (
		gotSource string
		gotOpts   cli.InstallOptions
		called    bool
	)

	envFn, installFn := newPluginEnv, runPluginInstall
	newPluginEnv = func() (*cli.Env, error) { return &cli.Env{Root: repoRoot}, nil }
	runPluginInstall = func(_ context.Context, _ *cli.Env, src string, opts cli.InstallOptions) error {
		called, gotSource, gotOpts = true, src, opts
		return nil
	}
	defer func() { newPluginEnv, runPluginInstall = envFn, installFn }()

	cmd := installCmd()
	cmd.SetArgs([]string{"--dry-run", "--operator", "tester", source})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("执行 install 子命令失败: %v", err)
	}
	if !called {
		t.Fatal("install 子命令未把源交给安装执行器（RunE 接线断了）")
	}
	if gotSource != source {
		t.Fatalf("传下去的源 = %q, want %q", gotSource, source)
	}
	// 旗标仍要绑到同一份 opts（加构造点不得打断旗标收集）
	if !gotOpts.DryRun || gotOpts.Operator != "tester" {
		t.Fatalf("旗标未绑到 InstallOptions: %+v", gotOpts)
	}

	// 核心断言：dev 通道必须接上脱敏回调
	if gotOpts.Sanitize == nil {
		t.Fatal("dev 通道 install 未接脱敏回调（cmd/plugin/plugin.go 的 opts.Sanitize 接线缺失）：失败审计会原样落库绝对路径")
	}

	text := fmt.Sprintf("⑤ 落位失败: 写入 %s 失败: 打开包内 %s 失败: 源 %s",
		filepath.Join(repoRoot, "plugins", "demo", "plugin.go"),
		filepath.Join(os.TempDir(), "aigo-plugin-stage-123456", "demo", "plugin.go"),
		source)
	// 口径必须与 catalog 通道逐字节一致（同一条 SanitizePluginText）
	want := cli.SanitizePluginText(&cli.Env{Root: repoRoot}, text, source)
	got := gotOpts.Sanitize(text)
	if got != want {
		t.Fatalf("dev 通道脱敏口径与 catalog 通道不同:\n got =%s\n want=%s", got, want)
	}
	if got == text {
		t.Fatal("脱敏回调是恒等函数（等于没接）")
	}
	for _, leak := range []string{repoRoot, source, os.TempDir()} {
		if strings.Contains(got, leak) {
			t.Fatalf("dev 通道脱敏后仍泄露 %q: %s", leak, got)
		}
	}
	// 脱敏不得损失失败步骤信息（否则审计失去可诊断性）
	for _, placeholder := range []string{"<repo>", "<tmp>", "<source>", "⑤ 落位失败"} {
		if !strings.Contains(got, placeholder) {
			t.Fatalf("脱敏结果缺 %q: %s", placeholder, got)
		}
	}
}
