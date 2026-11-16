package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-go-hub/ai-go-admin/internal/infra/config"
)

// TestClassifyEnvironment 判定纯函数逐格：显式 dev / 显式 prod / 认不出的显式值（fail-closed）/
// 未配置（按源码根可用性）。表驱动，一并覆盖大小写与首尾空白。
func TestClassifyEnvironment(t *testing.T) {
	cases := []struct {
		name          string
		configured    string
		rootAvailable bool
		wantName      string
		wantSource    string
		wantAllowed   bool
	}{
		{"显式 dev（源码根无关）", "dev", false, EnvDev, EnvSourceConfig, true},
		{"显式 prod 压过源码检出", "prod", true, EnvProd, EnvSourceConfig, false},
		{"大小写与首尾空白不算错", "  PROD ", true, EnvProd, EnvSourceConfig, false},
		{"认不出的显式值按生产处置", "production", true, EnvProd, EnvSourceConfig, false},
		{"未配置 + 源码检出 ⇒ dev", "", true, EnvDev, EnvSourceAuto, true},
		{"未配置 + 无源码检出 ⇒ prod", "", false, EnvProd, EnvSourceAuto, false},
	}
	for _, tc := range cases {
		got := classifyEnvironment(tc.configured, tc.rootAvailable)
		if got.Name != tc.wantName || got.Source != tc.wantSource || got.LifecycleAllowed != tc.wantAllowed {
			t.Fatalf("%s：判定不符 got=%+v（want name=%s source=%s allowed=%v）",
				tc.name, got, tc.wantName, tc.wantSource, tc.wantAllowed)
		}
	}

	// Configured 原样回报（只去首尾空白）：管理面据此自查 plugin.env 的笔误
	if got := classifyEnvironment(" production ", true); got.Configured != "production" {
		t.Fatalf("Configured 应回报去空白后的配置原值: %q", got.Configured)
	}
	if got := classifyEnvironment("", true); got.Configured != "" {
		t.Fatalf("未配置时 Configured 应为空: %q", got.Configured)
	}
}

// TestRequireLifecycleWith dev 放行 / prod 拒绝：稳定错误码 + HTTP 403 + 可展示文案（且不含本机路径）。
func TestRequireLifecycleWith(t *testing.T) {
	if err := RequireLifecycleWith("安装", classifyEnvironment("dev", false)); err != nil {
		t.Fatalf("dev 环境应放行: %v", err)
	}

	err := RequireLifecycleWith("卸载", classifyEnvironment("prod", true))
	var catalogErr *CatalogError
	if !errors.As(err, &catalogErr) {
		t.Fatalf("生产环境应返回 *CatalogError（handler 按 code/status 映射）: %v", err)
	}
	if catalogErr.Code != "lifecycle_forbidden" || catalogErr.Status != 403 {
		t.Fatalf("错误码/语义码不符: code=%s status=%d", catalogErr.Code, catalogErr.Status)
	}
	for _, token := range []string{"生产环境", "卸载", "plugin.env=prod"} {
		if !strings.Contains(catalogErr.Message, token) {
			t.Fatalf("文案缺「%s」: %s", token, catalogErr.Message)
		}
	}
	// 脱敏：错误会进页面提示与动作审计，不得带本机路径（含分隔符即视为泄露）
	if strings.ContainsAny(catalogErr.Message, "/\\") {
		t.Fatalf("文案不得含路径分隔符: %s", catalogErr.Message)
	}

	// 认不出的显式值同样按生产处置，且文案点名「无法识别」（运维据此自查笔误）
	err = RequireLifecycleWith("安装", classifyEnvironment("production", true))
	if !errors.As(err, &catalogErr) || catalogErr.Code != "lifecycle_forbidden" {
		t.Fatalf("认不出的显式值应按生产处置: %v", err)
	}
	if !strings.Contains(catalogErr.Message, "无法识别") {
		t.Fatalf("认不出的显式值应在文案里点名: %s", catalogErr.Message)
	}
}

// TestRunInstallBlockedInProduction 写路径门禁：生产环境在写库/落盘之前就被拒（runInstall 的第一行）。
// 传 nil integrity 是刻意的：门禁先于任何 plan/integrity 解引用——若把门禁挪后，本用例会因空指针崩掉。
func TestRunInstallBlockedInProduction(t *testing.T) {
	restore := detectEnvironment
	detectEnvironment = func() Environment { return classifyEnvironment("prod", true) }
	t.Cleanup(func() { detectEnvironment = restore })

	err := runInstall(context.Background(), &Env{Root: t.TempDir()}, &InstallPlan{Name: "demo"}, nil, InstallOptions{})
	var catalogErr *CatalogError
	if !errors.As(err, &catalogErr) || catalogErr.Code != "lifecycle_forbidden" {
		t.Fatalf("生产环境应拒装（且先于任何副作用）: %v", err)
	}
}

// TestDetectEnvironmentInSourceCheckout 自动判定这一格的活体验证：本项目测试进程就在源码检出内
// （包目录有 go.mod 祖先），未显式配置时应判定 dev。
//
// 不在本文件初始化全局配置是刻意的：本包的库级用例（frameworkMigrationSetup，PLUGIN_DB 门控）
// 依赖「谁先 config.Init() 谁定工作目录」，抢先后会让它读到包目录而非仓库根的配置，从而改变其行为。
// 因此这里只在配置尚未初始化时断言自动判定格；显式配置格由 classifyEnvironment 表覆盖。
func TestDetectEnvironmentInSourceCheckout(t *testing.T) {
	if config.Initialized() {
		t.Skip("本进程已初始化全局配置（另一用例先行）：显式配置分支由 TestClassifyEnvironment 覆盖")
	}
	got := DetectEnvironment()
	if got.Source != EnvSourceAuto || got.Name != EnvDev || !got.LifecycleAllowed {
		t.Fatalf("源码检出内（未配置 plugin.env）应自动判定为 dev: %+v", got)
	}
	if _, err := os.Stat("go.mod"); err == nil {
		t.Fatalf("本用例假定包目录不在仓库根（否则自动判定失去区分度）")
	}
}

// TestConfiguredSourceRoot plugin.root 的准入判据：只认「目录里有普通文件 go.mod」
// （刻意不校验模块身份——跨仓安装是支持的用法；归属只提示不拒绝，见 cli.go 的 rootOwnerNotice）。
func TestConfiguredSourceRoot(t *testing.T) {
	withMod := t.TempDir()
	if err := os.WriteFile(filepath.Join(withMod, "go.mod"), []byte("module example.com/other\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dirAsMod := t.TempDir()
	if err := os.Mkdir(filepath.Join(dirAsMod, "go.mod"), 0o755); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name   string
		raw    string
		wantOK bool
	}{
		{"空值不认", "", false},
		{"只有空白不认", "   ", false},
		{"目录不存在不认", filepath.Join(withMod, "nope"), false},
		{"无 go.mod 不认", t.TempDir(), false},
		{"go.mod 是目录（非普通文件）不认", dirAsMod, false},
		{"含普通文件 go.mod 的目录认（模块身份不参与判据）", withMod, true},
	}
	for _, tc := range cases {
		got, ok := configuredSourceRoot(tc.raw)
		if ok != tc.wantOK {
			t.Fatalf("%s：got ok=%v（%q）", tc.name, ok, got)
		}
		if ok && got != filepath.Clean(tc.raw) {
			t.Fatalf("%s：应回报绝对路径，got %q want %q", tc.name, got, filepath.Clean(tc.raw))
		}
	}
}

// TestResolveSourceRootFrom 根定位的两条分支与优先级：显式根命中即用它（优先于 FindRoot），
// 显式根为空或判据不认时回退 FindRoot——本项目测试进程的 CWD 在源码检出内，回退格应解析到含 go.mod 的目录。
// 用 resolveSourceRootFrom 这个参数化入口，是为了不起全局 viper（本文件不初始化全局配置，理由见上）。
func TestResolveSourceRootFrom(t *testing.T) {
	configured := t.TempDir()
	if err := os.WriteFile(filepath.Join(configured, "go.mod"), []byte("module example.com/other\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := resolveSourceRootFrom(configured)
	if err != nil {
		t.Fatalf("显式根命中时不应报错: %v", err)
	}
	if got != filepath.Clean(configured) {
		t.Fatalf("显式根应优先于 FindRoot：got %q want %q", got, filepath.Clean(configured))
	}

	// 判据不认（目录不存在）⇒ 静默回退 FindRoot（与显式命中同一条入口，优先级的两侧都钉住）
	fallback, err := resolveSourceRootFrom(filepath.Join(configured, "不存在"))
	if err != nil {
		t.Fatalf("回退 FindRoot 应能定位到当前测试进程的检出: %v", err)
	}
	if fallback == filepath.Clean(configured) {
		t.Fatalf("判据不认时不得沿用它: %q", fallback)
	}
	if info, statErr := os.Stat(filepath.Join(fallback, "go.mod")); statErr != nil || !info.Mode().IsRegular() {
		t.Fatalf("回退结果里没有普通文件 go.mod: %q（%v）", fallback, statErr)
	}
}

// TestRootOwnerNoticeText 归属提示的两个分支与文案：异模块 ⇒ 有文案（并含写的命令枚举与「相对路径」字样）；
// 本模块 / module 行读不出 ⇒ 空串（不提示）。纯函数测试，不碰 stderr。
func TestRootOwnerNoticeText(t *testing.T) {
	if text := rootOwnerNoticeText("/dev/null"); text != "" {
		t.Fatalf("读不出 module 不应提示：%q", text)
	}

	own := t.TempDir()
	if err := os.WriteFile(filepath.Join(own, "go.mod"), []byte("module "+ModulePath+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if text := rootOwnerNoticeText(own); text != "" {
		t.Fatalf("本模块不应提示：%q", text)
	}
	// 带引号的写法也是合法 go.mod（go mod edit -json 会给出去引号的 Path）⇒ 不得误报异模块
	quotedOwn := t.TempDir()
	if err := os.WriteFile(filepath.Join(quotedOwn, "go.mod"), []byte("module \""+ModulePath+"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if text := rootOwnerNoticeText(quotedOwn); text != "" {
		t.Fatalf("带引号的本模块不应提示：%q", text)
	}

	other := t.TempDir()
	if err := os.WriteFile(filepath.Join(other, "go.mod"), []byte("module ai-go-product\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	text := rootOwnerNoticeText(other)
	if text == "" {
		t.Fatal("异模块应提示")
	}
	for _, token := range []string{"ai-go-product", "defaults", "相对路径"} {
		if !strings.Contains(text, token) {
			t.Fatalf("提示缺「%s」: %s", token, text)
		}
	}
}

// TestRootOwnerNoticeGoesToErr 提示必须走 Env 的**诊断通道**（默认 stderr），不能污染 Out
// （stdout 是 `--json` 的机器通道）；同时钉住「每进程每根只提示一次」的抑制行为。
func TestRootOwnerNoticeGoesToErr(t *testing.T) {
	other := t.TempDir()
	if err := os.WriteFile(filepath.Join(other, "go.mod"), []byte("module ai-go-product\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errBuf bytes.Buffer
	env := &Env{Root: other, Out: &out, Err: &errBuf}

	env.rootOwnerNotice()
	if out.Len() != 0 {
		t.Fatalf("提示不得写进 Out（--json 通道）：%q", out.String())
	}
	if !strings.Contains(errBuf.String(), "ai-go-product") {
		t.Fatalf("提示应写进 Err：%q", errBuf.String())
	}
	if env.ErrWriter() != io.Writer(&errBuf) {
		t.Fatal("ErrWriter 应回落到 Err")
	}

	// 第二次构造同一个根（HTTP handler 每请求一次）不再重复打印
	again := &Env{Root: other, Out: &out, Err: &errBuf}
	before := errBuf.Len()
	again.rootOwnerNotice()
	if errBuf.Len() != before {
		t.Fatal("同根第二次构造不应重复提示")
	}
}

// TestModuleLine module 行读取（注释在前、行首空白、tab 分隔、带引号都要能认；读不到返回空串）。
// tab 与引号两格是刻意加的：注释里把「按空白切词」立为选型理由，用例必须能因实现回退而变红。
func TestModuleLine(t *testing.T) {
	root := t.TempDir()
	got0 := moduleLine(root)
	if got0 != "" {
		t.Fatalf("无 go.mod 应返回空串，got %q", got0)
	}
	cases := []struct {
		name    string
		content string
		want    string
	}{
		{"注释在前 + 行首空白", "// 注释行\n\n  module   github.com/example/x  \n\ngo 1.24\n", "github.com/example/x"},
		{"tab 分隔", "module\tgithub.com/example/tab\n", "github.com/example/tab"},
		{"带引号（go 也接受）", "module \"github.com/example/quoted\"\n", "github.com/example/quoted"},
		{"没有 module 行", "go 1.24\n", ""},
	}
	for _, tc := range cases {
		if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte(tc.content), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := moduleLine(root); got != tc.want {
			t.Fatalf("%s：got %q want %q", tc.name, got, tc.want)
		}
	}
}
