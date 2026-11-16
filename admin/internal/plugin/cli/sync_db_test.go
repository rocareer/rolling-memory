package cli

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ai-go-hub/ai-go-admin/internal/plugin/host"

	"gorm.io/gorm"
)

// sync（以台账重放接线生成物）的库级用例（PLUGIN_DB 门控）：
//
//	PLUGIN_DB=1 go test -count=1 -timeout=180s ./internal/plugin/cli/
//
// 口径：台账是唯一事实源——手改文件造成的缺行由重放补回；台账外/声明式/failed 的行由重放撤下；
// 新补行要求源码存在（fail-closed，拒写会破坏 go build 的生成物）；dry-run 零副作用；重放不写审计。
//
// **共享库纪律**：库级用例共用一个库，其它包（l2 的验收用例）会并发安装 demoe2e* 等插件（源码在
// 真实仓根、不在本用例的一次性工作根）⇒ 成功路径一律走**按名范围**（范围外的行不参与增删判定，
// 外来台账行不影响本用例）；全域回放只钉 fail-closed 拒绝路径——自己造一条源码缺失的幽灵台账行，
// 全域重放必然被拒（判定不依赖外来行）。
func TestPluginSyncReplayDB(t *testing.T) {
	env, db := installDBSetup(t, "13")
	ctx := context.Background()
	name := pluginDBName("13")
	buf, ok := env.Out.(*bytes.Buffer)
	if !ok {
		t.Fatal("测试环境应以 *bytes.Buffer 作为输出目标")
	}

	if err := Scaffold(env, name, ScaffoldOptions{Kind: "mixed", Title: "sync 用例插件"}); err != nil {
		t.Fatalf("脚手架应成功: %v", err)
	}
	if err := Install(ctx, env, name, InstallOptions{Operator: "pdbtest"}); err != nil {
		t.Fatalf("安装应成功: %v", err)
	}
	registry := env.Abs(RegistryFile)

	// ===== ① 一致态（按名范围）：无变化、不重写 =====
	before, err := os.ReadFile(registry)
	if err != nil {
		t.Fatalf("读取接线文件失败: %v", err)
	}
	buf.Reset()
	if err := Sync(ctx, env, []string{name}, SyncOptions{}); err != nil {
		t.Fatalf("sync 应成功: %v", err)
	}
	if !strings.Contains(buf.String(), "生成物与台账已一致") {
		t.Errorf("已一致时应报「无变化」\n--- 实际输出 ---\n%s", buf.String())
	}
	after, err := os.ReadFile(registry)
	if err != nil || !bytes.Equal(before, after) {
		t.Errorf("已一致时不得重写生成物: err=%v", err)
	}

	// ===== ② 手删注册行（模拟手改文件）⇒ 重放恢复 =====
	mustWrite(t, registry, string(BuildRegistryFile(nil)))
	buf.Reset()
	if err := Sync(ctx, env, []string{name}, SyncOptions{}); err != nil {
		t.Fatalf("sync 应成功: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "重放范围：指定 1 名") {
		t.Errorf("指定名时应打印重放范围\n--- 实际输出 ---\n%s", out)
	}
	if !strings.Contains(out, "  + "+name+"（源码在 "+PluginDir(name)+"/）") {
		t.Errorf("重放计划应列出新增行与源码位置\n--- 实际输出 ---\n%s", out)
	}
	if !strings.Contains(out, "重放差异") || !strings.Contains(out, "（新增 1 / 撤下 0）") {
		t.Errorf("重放应打印差异与增删计数\n--- 实际输出 ---\n%s", out)
	}
	assertRegistry(t, registry, []string{name})

	// ===== ③ --dry-run（按名范围）：只预演（含撤下方向）=====
	phantom := name + "-phantom" // 文件面存在、台账与进程内都没有（接线悬空）
	mustWrite(t, registry, string(BuildRegistryFile([]string{name, phantom})))
	buf.Reset()
	if err := Sync(ctx, env, []string{name, phantom}, SyncOptions{DryRun: true}); err != nil {
		t.Fatalf("sync --dry-run 应成功: %v", err)
	}
	out = buf.String()
	if !strings.Contains(out, "  - "+phantom+" — 台账无行（接线悬空") {
		t.Errorf("--dry-run 应列出撤下行并给出理由\n--- 实际输出 ---\n%s", out)
	}
	if !strings.Contains(out, "--dry-run：以上为预演") {
		t.Errorf("--dry-run 应有预演收尾说明\n--- 实际输出 ---\n%s", out)
	}
	assertRegistry(t, registry, []string{name, phantom}) // 预演不得改写

	// ===== ④ 真跑（按名范围）：撤下台账外行 =====
	buf.Reset()
	if err := Sync(ctx, env, []string{name, phantom}, SyncOptions{}); err != nil {
		t.Fatalf("sync 应成功: %v", err)
	}
	if !strings.Contains(buf.String(), "（新增 0 / 撤下 1）") {
		t.Errorf("重放读数应含增删计数\n--- 实际输出 ---\n%s", buf.String())
	}
	assertRegistry(t, registry, []string{name})

	// ===== ⑤ 声明式插件被手写注册行（台账在、kind=declarative）⇒ 重放撤下并给理由 =====
	declName := name + "-decl"
	insertLedgerRow(t, db, declName, "declarative", host.StatusInstalled)
	mustWrite(t, registry, string(BuildRegistryFile([]string{name, declName})))
	buf.Reset()
	if err := Sync(ctx, env, []string{name, declName}, SyncOptions{}); err != nil {
		t.Fatalf("sync 应成功: %v", err)
	}
	if !strings.Contains(buf.String(), "declarative 无 Go 包") {
		t.Errorf("声明式注册行应被撤下并给理由\n--- 实际输出 ---\n%s", buf.String())
	}
	assertRegistry(t, registry, []string{name})

	// ===== ⑥ 全域回放 + 源码缺失的幽灵台账行（直接改库的漂移）⇒ fail-closed 拒写、文件不动 =====
	ghostName := name + "-ghost"
	insertLedgerRow(t, db, ghostName, "code", host.StatusInstalled)
	before, err = os.ReadFile(registry)
	if err != nil {
		t.Fatal(err)
	}
	buf.Reset()
	syncErr := Sync(ctx, env, nil, SyncOptions{})
	if syncErr == nil || !strings.Contains(syncErr.Error(), "fail-closed") || !strings.Contains(syncErr.Error(), ghostName) {
		t.Errorf("源码缺失时应 fail-closed 拒写并点名，实际 %v", syncErr)
	}
	out = buf.String()
	if !strings.Contains(out, "重放范围：全域（台账全部行）") {
		t.Errorf("全域回放应打印范围\n--- 实际输出 ---\n%s", out)
	}
	if !strings.Contains(out, "  + "+ghostName) {
		t.Errorf("拒写前应列出待补行（含被拒者）\n--- 实际输出 ---\n%s", out)
	}
	after, err = os.ReadFile(registry)
	if err != nil || !bytes.Equal(before, after) {
		t.Errorf("拒写时不得改写生成物: err=%v", err)
	}

	// ===== ⑦ 指定未知名（拼错防治）：报「无对账对象」，不静默无操作 =====
	if err := Sync(ctx, env, []string{name + "-nope"}, SyncOptions{}); err == nil ||
		!strings.Contains(err.Error(), "无对账对象") {
		t.Errorf("指定未知名应报「无对账对象」，实际 %v", err)
	}

	// ===== ⑧ 重放不写审计（admin_plugin_actions 动作域是六项闭集，sync 属生成物对账）=====
	count, err := gorm.G[PluginAction](db).Where("plugin = ? AND action NOT IN ?", name, []string{ActionInstall}).Count(ctx, "*")
	if err != nil || count != 0 {
		t.Errorf("sync 不应写审计（动作域闭集），实际 %d err=%v", count, err)
	}

	// ===== 收尾：卸载 ⇒ 零残留 + 无归属漂移 =====
	if err := Uninstall(ctx, env, name, UninstallOptions{Operator: "pdbtest"}); err != nil {
		t.Fatalf("卸载应成功: %v", err)
	}
	assertZeroResidue(t, env, db, name)
	assertNoOwnershipDrift(t, db)
}

// assertRegistry 断言接线生成物逐字节等于生成器输出（names 为期望的注册名单）
func assertRegistry(t *testing.T, path string, names []string) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", path, err)
	}
	if want := string(BuildRegistryFile(names)); string(content) != want {
		t.Errorf("生成物应与生成器输出逐字节一致（名单 %v）\n--- 实际 ---\n%s\n--- 期望 ---\n%s", names, content, want)
	}
}

// insertLedgerRow 直插台账行（「直接改库」漂移的夹具；自带收尾清理——行名不在本包清理链的名单内）
func insertLedgerRow(t *testing.T, db *gorm.DB, name, kind, status string) {
	t.Helper()
	now := time.Now()
	row := host.Plugin{Name: name, Title: "sync 用例台账行", Version: "1.0.0", Kind: kind, Status: status, UpdatedAt: &now}
	if err := gorm.G[host.Plugin](db).Create(context.Background(), &row); err != nil {
		t.Fatalf("直插台账行 %s 失败: %v", name, err)
	}
	t.Cleanup(func() {
		if _, err := gorm.G[host.Plugin](db).Where("name = ?", name).Delete(context.Background()); err != nil {
			t.Errorf("清理台账行 %s 失败: %v", name, err)
		}
	})
}
