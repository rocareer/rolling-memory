package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-go-hub/ai-go-admin/internal/plugin/manifest"
	pluginSource "github.com/ai-go-hub/ai-go-admin/internal/plugin/source"
)

// 安装失败的审计面脱敏（第 7 条）：`/var/folders/...zip`、staging 解包目录与落位目标的
// 绝对路径都不得进浏览器或 admin_plugin_actions.detail。
//
// 这里断言的是**落库那条 detail**（failureAuditDetail 的产物），不是某个中间变量：审计 detail 与
// 返回前端的 message 必须由同一个 Sanitize 回调处理，否则一边脱敏、一边原样入库。
func TestFailureAuditDetailSanitizesError(t *testing.T) {
	stagingDir := filepath.Join(os.TempDir(), "aigo-plugin-stage-123456")
	zipPath := filepath.Join(os.TempDir(), "catalog-local-demo@1.0.0.zip")
	target := filepath.Join(string(filepath.Separator)+"repo", "plugins", "demo", "plugin.go")
	cause := fmt.Errorf("⑤ 落位失败: 写入 %s 失败: 打开包内 %s 失败: 源：%s",
		target, filepath.Join(stagingDir, "demo", "plugin.go"), zipPath)

	state := &installState{plan: &InstallPlan{SourceRef: "catalog:local:demo@1.0.0", Manifest: &manifest.Manifest{Kind: "code"}}}

	// ① 零值 InstallOptions（回调缺失）：原样落库——这只是 sanitizeInstallText 对 nil 回调的兜底语义
	// （见 TestSanitizeInstallTextNilSafe），**不是** dev 通道的现状：`aigo plugin install` 已在
	// cmd/plugin 接过 Sanitize（cmd/plugin/plugin.go 的 opts.Sanitize，接线由 cmd 侧
	// TestInstallCmdWiresFailureSanitizer 钉住）。
	plain := failureAuditDetail(InstallOptions{}, cause, state)
	if plain["error"] != cause.Error() {
		t.Fatalf("未配置 Sanitize 时应原样落库: %v", plain["error"])
	}
	if plain["source"] != "catalog:local:demo@1.0.0" || plain["kind"] != "code" {
		t.Fatalf("失败审计的 source/kind 不符: %+v", plain)
	}

	// ② 配置了 Sanitize（catalog 通道）：三处绝对路径都不得出现在落库文本里
	replacer := strings.NewReplacer(stagingDir, "<staging>", zipPath, "<package>", "/repo", "<repo>")
	opts := InstallOptions{Sanitize: replacer.Replace}
	sanitized := failureAuditDetail(opts, cause, state)["error"].(string)
	if sanitized != cause.Error() && sanitized == plain["error"] {
		t.Fatal("配置了 Sanitize 却未生效")
	}
	for _, leak := range []string{stagingDir, zipPath, "/repo", os.TempDir()} {
		if strings.Contains(sanitized, leak) {
			t.Fatalf("审计 detail.error 泄露路径 %q: %s", leak, sanitized)
		}
	}
	for _, placeholder := range []string{"<staging>", "<package>", "<repo>"} {
		if !strings.Contains(sanitized, placeholder) {
			t.Fatalf("脱敏应替换为 %s，实际 %s", placeholder, sanitized)
		}
	}
	// 脱敏不得吃掉失败步骤信息（否则审计失去可诊断性）
	if !strings.Contains(sanitized, "⑤ 落位失败") {
		t.Fatalf("脱敏不应损失失败步骤: %s", sanitized)
	}
}

// TestAuditFailureDetailSanitizedAcrossEntryPoints 三条**不经 catalog 通道**的失败审计链路
// （dev 通道 `aigo plugin install` / 启停写库失败 / 卸载失败）与 catalog 通道必须产出**同一段文本**。
//
// 依据第 7 条：失败原因里的 staging 临时目录与落位目标绝对路径不得进 admin_plugin_actions.detail
// （该 detail 在插件管理页展示）。四处的实现全是 source.SanitizePaths，本用例把「接线」钉住：
// 任一处改回原文落库，这里立刻不等。
func TestAuditFailureDetailSanitizedAcrossEntryPoints(t *testing.T) {
	env := &Env{Root: "/repo"}
	stagingDir := filepath.Join(os.TempDir(), "aigo-plugin-stage-123456")
	cause := fmt.Errorf("⑤ 落位失败: 写入 %s 失败: 临时目录 %s",
		filepath.Join(env.Root, "plugins", "demo", "plugin.go"), stagingDir)

	// catalog 通道：返回前端/CLI 的 message 与审计 detail.error 共用的那一条（InstallCatalog 的用法）
	want := sanitizeCatalogText(cause.Error(), env, nil, nil)

	state := &installState{plan: &InstallPlan{SourceRef: "plugins/demo", Manifest: &manifest.Manifest{Kind: "code"}}}

	got := map[string]string{
		// dev 通道 `aigo plugin install`：cmd/plugin 把 SanitizePluginText 塞进 InstallOptions.Sanitize
		// （并把命令行给的源路径当 extra，与 catalog 通道传 src.Location 同口径）
		"install": failureAuditDetail(InstallOptions{Sanitize: func(text string) string {
			return SanitizePluginText(env, text, "plugins/demo")
		}}, cause, state)["error"].(string),
		"enable/disable": statusFailureAuditDetail(env, cause)["error"].(string),
		"uninstall":      uninstallFailureDetail(env, &UninstallSummary{}, "b) 删落位物", cause)["error"].(string),
	}
	for name, text := range got {
		if text != want {
			t.Errorf("%s 链路脱敏口径与 catalog 通道不一致:\n got =%s\n want=%s", name, text, want)
		}
		for _, leak := range []string{env.Root, stagingDir, os.TempDir()} {
			if strings.Contains(text, leak) {
				t.Errorf("%s 链路的审计文本泄露 %q: %s", name, leak, text)
			}
		}
		if !strings.Contains(text, "⑤ 落位失败") {
			t.Errorf("%s 链路脱敏吃掉了失败步骤信息: %s", name, text)
		}
	}
	// 卸载链路还要保住失败步骤与台账摘要（脱敏只作用于 error 文本，不得把 detail 打薄）
	uninstall := uninstallFailureDetail(env, &UninstallSummary{}, "b) 删落位物", cause)
	if uninstall["failed_step"] != "b) 删落位物" || uninstall["purge"] != false {
		t.Fatalf("卸载失败 detail 的失败步骤/台账摘要缺失: %+v", uninstall)
	}
}

// TestSanitizePluginTextExtra 命令行给的源路径（可能是绝对路径的 zip/目录）也要替换成 <source>；
// 但退化输入（相对路径、"/"）不得参与替换——把整个文本毁掉比留一个路径更糟。
func TestSanitizePluginTextExtra(t *testing.T) {
	env := &Env{Root: "/repo"}
	text := "打开包失败: 源 /pkg/demo-1.0.0.zip；落位目标 /repo/plugins/demo/plugin.go"

	got := SanitizePluginText(env, text, "/pkg/demo-1.0.0.zip")
	if strings.Contains(got, "/pkg/demo-1.0.0.zip") {
		t.Fatalf("给的绝对路径应替换为 <source>: %s", got)
	}
	if !strings.Contains(got, "源 <source>；") || !strings.Contains(got, "<repo>/plugins/demo/plugin.go") {
		t.Fatalf("替换结果不符（<source> 与 <repo> 都要在）: %s", got)
	}
	for _, degenerate := range []string{"", "plugins/demo", "/", "////"} {
		if unchanged := SanitizePluginText(env, text, degenerate); unchanged != SanitizePluginText(env, text) {
			t.Fatalf("退化 extra %q 不应参与替换: %s", degenerate, unchanged)
		}
	}
	// env 为 nil 时不得 panic（无仓库根可换，只剩系统临时目录一层）
	if nilEnv := SanitizePluginText(nil, "文本"); nilEnv != "文本" {
		t.Fatalf("nil env 应原样返回: %s", nilEnv)
	}
}

// TestSanitizeInstallTextNilSafe：回调缺失/为 nil 时不得 panic（零值 InstallOptions 是常见入口）。
func TestSanitizeInstallTextNilSafe(t *testing.T) {
	var zero InstallOptions
	if got := zero.sanitizeInstallText("原文"); got != "原文" {
		t.Fatalf("零值 InstallOptions 应原样返回: %s", got)
	}
	upper := InstallOptions{Sanitize: strings.ToUpper}
	if got := upper.sanitizeInstallText("abc"); got != "ABC" {
		t.Fatalf("回调未生效: %s", got)
	}
}

// TestCatalogInstallFailureAndAuditShareSanitizer：catalog 安装失败时，返回前端的 message
// 与审计 detail.error 必须出自同一个脱敏器（此处直接比对两条链路的产物文本）。
func TestCatalogInstallFailureAndAuditShareSanitizer(t *testing.T) {
	env := &Env{Root: "/repo"}
	pkg := &pluginSource.PackageSource{
		Path:    filepath.Join(os.TempDir(), "catalog-local-demo@1.0.0.zip"),
		TempDir: filepath.Join(os.TempDir(), "aigo-catalog-pkg-123456"),
	}
	cause := errors.New("写入 " + filepath.Join(pkg.TempDir, "demo", "plugin.go") + " 失败: 源 " + pkg.Path)

	// 前端 message 的脱敏链路（InstallCatalog 用这一条）
	message := sanitizeCatalogText(cause.Error(), env, nil, pkg)
	// 审计 detail.error 的脱敏链路（InstallCatalog 把同一函数塞进 InstallOptions.Sanitize）
	audit := failureAuditDetail(InstallOptions{Sanitize: func(text string) string {
		return sanitizeCatalogText(text, env, nil, pkg)
	}}, cause, &installState{plan: &InstallPlan{SourceRef: "ref", Manifest: &manifest.Manifest{Kind: "code"}}})["error"].(string)

	if message != audit {
		t.Fatalf("两条链路脱敏口径不一致:\n message=%s\n audit  =%s", message, audit)
	}
	for _, leak := range []string{pkg.TempDir, pkg.Path} {
		if strings.Contains(audit, leak) {
			t.Fatalf("审计文本泄露 %q: %s", leak, audit)
		}
	}
}
