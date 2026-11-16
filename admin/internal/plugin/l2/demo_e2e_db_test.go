package l2

// P6 试点与端到端验收（设计说明「验收 A + 反向路径 + 验收 B」）。
//
// 为什么落在本包：验收里的「L2 引擎级 CRUD」要打到六动作执行器（entityHandler 非导出、不经 HTTP 暴露），
// 只能在包内驱动（先例：本包 db_test.go 的引擎用例）。不连库时整组跳过（PLUGIN_DB 门控在 dbSetup 里）。
//
// 运行：
//
//	PLUGIN_DB=1 go test -count=1 -timeout=180s -run 'DemoAcceptance' ./internal/plugin/l2/
//
// 三条用例与设计口径的对应：
//   - TestDemoAcceptanceA_DB：验收 A（L1 主链 + L2 引擎级 CRUD + 启停 → 卸载 → 复装 → --purge）；
//   - TestDemoAcceptanceFailurePath_DB：反向路径（注入 ⑥ 迁移失败 ⇒ failed ⇒ 归位后 uninstall --force --purge 清理干净）；
//   - TestDemoAcceptanceB_DB：验收 B（装 → enable → 升级 v2 加列（自省基线）→ 卸载留表 → 复装补齐新列 → --purge 删表）。
//
// 与工作树的约定（纪律）：
//   - 验收对象是**仓里的真示例插件**（plugins/demo）与真实库（A 用例直接装在 `plugins/demo` 上；
//     B 与反向路径用**唯一命名**的临时源，不碰仓里的 demo 一个字节）；
//   - 用例以快照/收尾还原保证结束时仓回到出厂态：工作树按用例前的字节快照写回、
//     DB 侧只按插件名收口清理（**绝不触碰既有数据**）、唯一命名用例的落位物与 .store 归档一并删除；
//     A 用例的 plugins/.store/demo-*.zip 也在收尾删除（「表与 .store 保留」的断言发生在卸载那一刻）；
//   - 起点非出厂态（台账/归属行/注册行/表）一律 skip：那可能是别人的安装现场，用例不猜也不动。
//
// 落地面（P6 收口后的现状，两条旧缺口均已闭合，用例按实现口径断言）：
//   - 安装链的 ④b 调用 L2 引擎执行结构 DDL（建表/加列；`internal/plugin/cli/l2ddl.go`），用例不再手动驱动
//     引擎——改为断言「安装本身建出了表 + 自省基线复算 0 条语句」；
//   - 安装器公式代写 L2 实体页菜单/权限节点（`internal/plugin/cli/ruleformula.go`），
//     示例插件的 `menus:` 只声明 L1 部分，断言改为「同名规则零容忍（不得重复）」。

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-go-hub/ai-go-admin/internal/infra/config"
	"github.com/ai-go-hub/ai-go-admin/internal/infra/permission"
	"github.com/ai-go-hub/ai-go-admin/internal/model"
	"github.com/ai-go-hub/ai-go-admin/internal/plugin/cli"
	"github.com/ai-go-hub/ai-go-admin/internal/plugin/host"
	"github.com/ai-go-hub/ai-go-admin/internal/plugin/manifest"
	"github.com/ai-go-hub/ai-go-admin/internal/version"

	"github.com/gin-gonic/gin"
	"go.yaml.in/yaml/v3"
	"gorm.io/gorm"
)

const (
	// demoName 仓里示例插件的名字（plugins/demo）
	demoName = "demo"
	// demoEntity 示例插件的 L2 实体名
	demoEntity = "items"
	// demoOperator 审计署名（只是署名，不构成身份认证）
	demoOperator = "plugin-e2e"
	// demoSentinelRuleID 角色组悬挂 ID 用例的哨兵（不属于任何插件，必须保留）
	demoSentinelRuleID = "777001"
)

// demoIdentifierSafe 标识符白名单（清理/断言前的复核；口径同 cli.identifierSafe，跨包不可见故本地登记）
var demoIdentifierSafe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// demoCopyNameSafe 唯一命名的合形：只用 [a-z0-9]（不出现 '-'）——cli.ConfigKey 会把插件名里的 '-' 折成
// '_'，用 '-' 会让「拷贝源里 sed 出来的配置键」与安装器算出的键对不上。
var demoCopyNameSafe = regexp.MustCompile(`^[a-z][a-z0-9]{1,31}$`)

// ---------- 前置、环境与收尾 ----------

// demoRepoRoot 仓库根（dbSetup 已 chdir 到那里；插件源与落位副本都是仓库相对路径）
func demoRepoRoot(t *testing.T) string {
	t.Helper()
	root, err := os.Getwd()
	if err != nil {
		t.Fatalf("取当前目录失败: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("当前目录不是仓库根（缺 go.mod）: %s（%v）", root, err)
	}
	return root
}

// demoEnv 验收的命令环境（真实仓库根 + 真实表前缀；输出收集到 buffer 供 dry-run 断言）
func demoEnv(root string) (*cli.Env, *bytes.Buffer) {
	out := &bytes.Buffer{}
	return &cli.Env{
		Root:        root,
		Prefix:      config.Get().Database.Prefix,
		HostVersion: version.Get().Version,
		Out:         out,
	}, out
}

// demoDeterministicCaches 把运行时闸与实体元数据缓存压到 1ns（验收要求「改库即生效」，
// ≤1s 语义在测试里取极限）；收尾还原默认值。
func demoDeterministicCaches(t *testing.T) {
	t.Helper()
	host.SetGateTTL(time.Nanosecond)
	SetMetadataTTL(time.Nanosecond)
	t.Cleanup(func() {
		host.SetGateTTL(0)
		SetMetadataTTL(0)
	})
}

// demoRequireHostSchema 宿主与插件机制的表/列齐备性（缺失 ⇒ skip：本用例不代做宿主迁移）
func demoRequireHostSchema(t *testing.T, db *gorm.DB) {
	t.Helper()
	prefix := prefixOf()
	for _, table := range []string{"admin_rules", "admin_groups", "configs", "admins", "admin_group_access", "tokens", "admin_plugins", "admin_plugin_entities"} {
		if !tableExists(t, db, prefix+table) {
			t.Skipf("数据表 %s 不存在：先 `aigo migrate up`（本用例不代做宿主迁移）", prefix+table)
		}
	}
	if _, ok := tableColumns(t, db, prefix+"admin_rules")["plugin"]; !ok {
		t.Skipf("admin_rules.plugin 归属列缺失（插件机制迁移未应用）：先 `aigo migrate up`")
	}
}

// demoRequireSource 出厂态的文件面：插件源码在仓 + 接线文件是生成器的空骨架（/C12）
func demoRequireSource(t *testing.T, env *cli.Env, name string) {
	t.Helper()
	if _, err := os.Stat(env.Abs(path.Join(cli.PluginDir(name), manifest.FileName))); err != nil {
		t.Skipf("仓里没有 %s/%s（P6 验收对象缺失）: %v", cli.PluginDir(name), manifest.FileName, err)
	}
	if err := cli.CheckSkeleton(env.Root); err != nil {
		t.Skipf("%s 不是出厂空骨架：%v（提交态应为空骨架）", cli.RegistryFile, err)
	}
}

// demoRequireCleanSlate 起点必须干净：该插件名没有台账行、归属行、注册行与自管表。
// 非干净起点一律 skip——那可能是别人的安装现场（纪律：禁止触碰既有数据）。
func demoRequireCleanSlate(t *testing.T, env *cli.Env, db *gorm.DB, name string) {
	t.Helper()
	ctx := context.Background()
	if row, ok := demoLedger(t, db, name); ok {
		t.Skipf("插件 %s 已有台账行（状态 %s）：验收只跑「出厂未安装」起点，先 `aigo plugin uninstall --keep-files %s`", name, row.Status, name)
	}
	if count, err := gorm.G[model.AdminRule](db).Where("plugin = ?", name).Count(ctx, "*"); err != nil {
		t.Fatalf("统计归属行失败: %v", err)
	} else if count != 0 {
		t.Skipf("admin_rules 已有 plugin=%s 的行 %d 条（非出厂态）：先清理再跑验收", name, count)
	}
	registered, err := cli.RegisteredPlugins(env.Root)
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", cli.RegistryFile, err)
	}
	if slices.Contains(registered, name) {
		t.Skipf("%s 已有 %s 的注册行（非出厂态）：先 `aigo plugin uninstall --keep-files %s`", cli.RegistryFile, name, name)
	}
	for _, table := range demoOwnedTables(env.Prefix, name) {
		if tableExists(t, db, table) {
			t.Skipf("表 %s 已存在（非出厂态）：先 `aigo plugin uninstall --purge --yes %s` 或人工清理", table, name)
		}
	}
}

// demoAssistantSetupA 验收 A 的公共前置（真示例插件 + 真实库）与收尾（工作树按快照还原 → DB 侧按名收口 → 删归档）。
func demoAssistantSetupA(t *testing.T) (*cli.Env, *gorm.DB) {
	t.Helper()
	db := dbSetup(t) // PLUGIN_DB 门控 + 连库 + chdir 仓库根
	root := demoRepoRoot(t)
	demoRequireHostSchema(t, db)
	env, _ := demoEnv(root)

	snapshot := demoSnapshot(t, root, demoName)
	t.Cleanup(func() {
		demoRestore(t, root, demoName, snapshot)
		demoPurgeRows(t, db, env.Prefix, demoName)
		demoRemoveStore(t, root, demoName)
	})

	demoRequireSource(t, env, demoName)
	demoRequireCleanSlate(t, env, db, demoName)
	demoDeterministicCaches(t)
	return env, db
}

// demoCopySetup B / 反向路径的公共前置：唯一命名的临时源（仓里的 demo 一个字节都不动）。
// 返回命令环境、库句柄与临时源目录；收尾还原创线文件字节、清落位物/表/归档。
func demoCopySetup(t *testing.T, tag string) (*cli.Env, *gorm.DB, string) {
	t.Helper()
	db := dbSetup(t)
	root := demoRepoRoot(t)
	demoRequireHostSchema(t, db)
	env, _ := demoEnv(root)

	if _, err := os.Stat(env.Abs(path.Join(cli.PluginDir(demoName), manifest.FileName))); err != nil {
		t.Skipf("仓里没有 %s/%s（P6 验收对象缺失）: %v", cli.PluginDir(demoName), manifest.FileName, err)
	}
	name := demoUniqueName(tag)
	src := demoCopySource(t, name)
	registryBefore := demoReadFileOrNil(t, filepath.Join(root, filepath.FromSlash(cli.RegistryFile)))
	t.Cleanup(func() {
		demoRestoreFile(t, root, cli.RegistryFile, registryBefore)
		demoRemoveFootprint(t, root, name)
		demoPurgeRows(t, db, env.Prefix, name)
		demoRemoveStore(t, root, name)
	})

	demoRequireCleanSlate(t, env, db, name)
	demoDeterministicCaches(t)
	return env, db, src
}

// demoUniqueName 唯一命名（tag + 进程号；tag 只用小写字母数字）
func demoUniqueName(tag string) string {
	return fmt.Sprintf("demoe2e%s%d", tag, os.Getpid())
}

// demoCopySource 把仓里的示例插件拷成唯一命名的临时源：逐文件复制并把文本里的 "demo" 换成新名
// （manifest 的 name 与菜单名、Go 包名与常量、迁移里的表名、视图里的语言包键、README 的示例路径），
// 保证「目录名 == name」与全部派生名（路由段/表名/配置键/语言包文件名）一致。
func demoCopySource(t *testing.T, name string) string {
	t.Helper()
	if !demoCopyNameSafe.MatchString(name) {
		t.Fatalf("唯一命名 %q 不合规（^[a-z][a-z0-9]{1,31}$，不出现 '-'）", name)
	}
	root := demoRepoRoot(t)
	srcRoot := filepath.Join(root, filepath.FromSlash(cli.PluginDir(demoName)))
	dstRoot := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(dstRoot, 0o755); err != nil {
		t.Fatalf("创建临时源目录失败: %v", err)
	}
	err := filepath.WalkDir(srcRoot, func(target string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, relErr := filepath.Rel(srcRoot, target)
		if relErr != nil {
			return relErr
		}
		dst := filepath.Join(dstRoot, rel)
		if entry.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		content, readErr := os.ReadFile(target)
		if readErr != nil {
			return readErr
		}
		return os.WriteFile(dst, bytes.ReplaceAll(content, []byte(demoName), []byte(name)), 0o644)
	})
	if err != nil {
		t.Fatalf("拷贝示例插件到 %s 失败: %v", dstRoot, err)
	}
	return dstRoot
}

// demoMutateManifest 就地改写拷贝出的清单（先按真实路径 Load 校验，改完再校验一次写回）：
// 验收 B 的「升级 v2」= 版本号 +1 与实体加一列（允许的唯一列级变更）。
func demoMutateManifest(t *testing.T, dir string, mutate func(*manifest.Manifest)) {
	t.Helper()
	loaded, _, err := manifest.Load(dir, manifest.Options{Prefix: prefixOf(), HostVersion: version.Get().Version})
	if err != nil {
		t.Fatalf("装载 %s 的清单失败: %v", dir, err)
	}
	mutate(loaded)
	raw, err := yaml.Marshal(loaded)
	if err != nil {
		t.Fatalf("序列化清单失败: %v", err)
	}
	if _, _, err := manifest.Parse(raw, manifest.Options{Prefix: prefixOf(), HostVersion: version.Get().Version, PluginDir: dir}); err != nil {
		t.Fatalf("改写后的清单未过校验（目录名 == name 等）: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, manifest.FileName), raw, 0o644); err != nil {
		t.Fatalf("写回清单失败: %v", err)
	}
}

// demoRemarkField 验收 B 的 v2 新增列（ddl_owner=engine ⇒ 引擎按**表结构自省**基线补列）
func demoRemarkField() manifest.Field {
	return manifest.Field{
		Name: "remark", Type: "string", TitleZh: "备注", TitleEn: "Remark",
		Length: 64, Default: "", List: true, Form: "input",
		Search: &manifest.Search{Op: "LIKE"},
	}
}

// demoBumpRegisterVersion 同步拷贝源 plugin.go 的登记版本常量（升级 v2 夹具的形态一致：清单版本与
// 重编译后的进程内注册版本必须同号，否则「重编译语义等价」在夹具里就不成立）。
func demoBumpRegisterVersion(t *testing.T, dir, from, to string) {
	t.Helper()
	file := filepath.Join(dir, "plugin.go")
	content := demoReadFile(t, file)
	old := []byte(`Version = "` + from + `"`)
	updated := bytes.ReplaceAll(content, old, []byte(`Version = "`+to+`"`))
	if bytes.Equal(updated, content) {
		t.Fatalf("%s 里没有 %q 常量（升级夹具失效）", file, string(old))
	}
	if err := os.WriteFile(file, updated, 0o644); err != nil {
		t.Fatalf("写回 %s 失败: %v", file, err)
	}
}

// ---------- 工作树快照与还原 ----------

// demoSnapshot 工作树字节快照（key = 仓库相对路径）：
// plugins/<名>/**（源码整树）、web/src/views/admin/plugin/<名>/**（视图副本整树）、
// web/src/lang/*/plugin/<名>.yaml（语言包副本）、internal/plugins/register_gen.go（接线文件）。
func demoSnapshot(t *testing.T, root, name string) map[string][]byte {
	t.Helper()
	snapshot := map[string][]byte{}
	for _, rel := range demoTreeFiles(t, root, name) {
		snapshot[rel] = demoReadFile(t, filepath.Join(root, filepath.FromSlash(rel)))
	}
	for _, rel := range demoFlatFiles(t, root, name) {
		snapshot[rel] = demoReadFile(t, filepath.Join(root, filepath.FromSlash(rel)))
	}
	return snapshot
}

// demoTreeFiles 整树覆盖的两个目录下的全部文件（目录不存在 ⇒ 空）
func demoTreeFiles(t *testing.T, root, name string) []string {
	t.Helper()
	files := []string{}
	for _, dir := range []string{cli.PluginDir(name), cli.ViewTarget(name)} {
		full := filepath.Join(root, filepath.FromSlash(dir))
		if _, err := os.Stat(full); errors.Is(err, fs.ErrNotExist) {
			continue
		}
		err := filepath.WalkDir(full, func(target string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				return nil
			}
			rel, relErr := filepath.Rel(root, target)
			if relErr != nil {
				return relErr
			}
			files = append(files, filepath.ToSlash(rel))
			return nil
		})
		if err != nil {
			t.Fatalf("枚举 %s 失败: %v", dir, err)
		}
	}
	return files
}

// demoFlatFiles 逐文件覆盖的清单：语言包副本（现存 glob）+ 接线文件（存在才收）
func demoFlatFiles(t *testing.T, root, name string) []string {
	t.Helper()
	files := demoLangTargets(t, root, name)
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(cli.RegistryFile))); err == nil {
		files = append(files, cli.RegistryFile)
	}
	return files
}

// demoLangTargets 该插件在各语言下的语言包副本（仓库相对路径；口径同 cli.LangTargetsOf）
func demoLangTargets(t *testing.T, root, name string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(root, filepath.FromSlash(cli.WebLangRoot), "*", "plugin", name+".yaml"))
	if err != nil {
		t.Fatalf("扫描语言包副本失败: %v", err)
	}
	out := make([]string, 0, len(matches))
	for _, match := range matches {
		rel, relErr := filepath.Rel(root, match)
		if relErr != nil {
			t.Fatalf("计算相对路径失败: %v", relErr)
		}
		out = append(out, filepath.ToSlash(rel))
	}
	slices.Sort(out)
	return out
}

// demoRestore 把工作树拉回快照字节：① 两个整树目录 RemoveAll；② 现存语言包副本与接线文件若不在
// 快照内 ⇒ 删除；③ 快照逐文件写回（含目录重建）。
func demoRestore(t *testing.T, root, name string, snapshot map[string][]byte) {
	t.Helper()
	for _, dir := range []string{cli.PluginDir(name), cli.ViewTarget(name)} {
		if err := os.RemoveAll(filepath.Join(root, filepath.FromSlash(dir))); err != nil {
			t.Errorf("还原：删除 %s 失败: %v", dir, err)
		}
	}
	for _, rel := range demoFlatFiles(t, root, name) {
		if _, ok := snapshot[rel]; ok {
			continue
		}
		if err := os.Remove(filepath.Join(root, filepath.FromSlash(rel))); err != nil && !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("还原：删除 %s 失败: %v", rel, err)
		}
	}
	for rel, content := range snapshot {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Errorf("还原：创建 %s 的目录失败: %v", rel, err)
			continue
		}
		if err := os.WriteFile(full, content, 0o644); err != nil {
			t.Errorf("还原：写回 %s 失败: %v", rel, err)
		}
	}
}

// demoRestoreFile 单个文件的字节还原（nil ⇒ 删除该文件）
func demoRestoreFile(t *testing.T, root, rel string, content []byte) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if content == nil {
		if err := os.Remove(full); err != nil && !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("还原：删除 %s 失败: %v", rel, err)
		}
		return
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Errorf("还原：创建 %s 的目录失败: %v", rel, err)
		return
	}
	if err := os.WriteFile(full, content, 0o644); err != nil {
		t.Errorf("还原：写回 %s 失败: %v", rel, err)
	}
}

// demoReadFile 读文件（失败即 Fatal：快照读不到就没有「还原」可言）
func demoReadFile(t *testing.T, full string) []byte {
	t.Helper()
	content, err := os.ReadFile(full)
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", full, err)
	}
	return content
}

// demoReadFileOrNil 读文件（不存在 ⇒ nil）
func demoReadFileOrNil(t *testing.T, full string) []byte {
	t.Helper()
	content, err := os.ReadFile(full)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", full, err)
	}
	return content
}

// demoRemoveFootprint 删除该插件名的落位物（源码树 + 视图副本 + 语言包副本；唯一命名用例的足迹）
func demoRemoveFootprint(t *testing.T, root, name string) {
	t.Helper()
	for _, dir := range []string{cli.PluginDir(name), cli.ViewTarget(name)} {
		if err := os.RemoveAll(filepath.Join(root, filepath.FromSlash(dir))); err != nil {
			t.Logf("收尾删除 %s 失败: %v", dir, err)
		}
	}
	for _, rel := range demoLangTargets(t, root, name) {
		if err := os.Remove(filepath.Join(root, filepath.FromSlash(rel))); err != nil && !errors.Is(err, fs.ErrNotExist) {
			t.Logf("收尾删除 %s 失败: %v", rel, err)
		}
	}
}

// demoRemoveStore 删除该插件名在 plugins/.store/ 的复装归档（用例足迹；「.store 保留」的断言
// 发生在卸载那一刻，见 A 用例内注释）
func demoRemoveStore(t *testing.T, root, name string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(root, filepath.FromSlash(cli.StoreDir), name+"-*.zip"))
	if err != nil {
		t.Logf("扫描 .store 失败: %v", err)
		return
	}
	for _, match := range matches {
		if err := os.Remove(match); err != nil {
			t.Logf("删除归档 %s 失败: %v", match, err)
		}
	}
}

// ---------- 库侧读数与零残留 ----------

// demoOwnedTables 该插件名下的表（版本表 + 引擎实体表 + 迁移自管表）
func demoOwnedTables(prefix, name string) []string {
	return []string{
		cli.MigrationTableName(name, prefix),
		cli.PhysicalEntityTable(prefix, name, demoEntity),
		prefix + "plugin_" + name + "_notes",
	}
}

// demoLedger 读台账行（无行 ⇒ ok=false）
func demoLedger(t *testing.T, db *gorm.DB, name string) (host.Plugin, bool) {
	t.Helper()
	row, err := gorm.G[host.Plugin](db).Where("name = ?", name).First(context.Background())
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return host.Plugin{}, false
	}
	if err != nil {
		t.Fatalf("读取插件台账失败: %v", err)
	}
	return row, true
}

// demoAssertLedger 台账状态断言
func demoAssertLedger(t *testing.T, db *gorm.DB, name, wantStatus string) host.Plugin {
	t.Helper()
	row, ok := demoLedger(t, db, name)
	if !ok {
		t.Fatalf("台账应有 %s 的行", name)
	}
	if row.Status != wantStatus {
		t.Errorf("台账状态应为 %s，实际 %s", wantStatus, row.Status)
	}
	return row
}

// demoRules 归属行（按 name 升序）
func demoRules(t *testing.T, db *gorm.DB, name string) []model.AdminRule {
	t.Helper()
	rows, err := gorm.G[model.AdminRule](db).Where("plugin = ?", name).Order("name").Find(context.Background())
	if err != nil {
		t.Fatalf("读取归属行失败: %v", err)
	}
	return rows
}

// demoRuleNames 规则名清单（错误信息用）
func demoRuleNames(rows []model.AdminRule) []string {
	names := make([]string, 0, len(rows))
	for i := range rows {
		names = append(names, rows[i].Name)
	}
	slices.Sort(names)
	return names
}

// demoRuleByName 按规则名取行（返回切片内地址，便于断言字段）
func demoRuleByName(rows []model.AdminRule, name string) *model.AdminRule {
	for i := range rows {
		if rows[i].Name == name {
			return &rows[i]
		}
	}
	return nil
}

// demoAssertMenuStatus 归属行（菜单/节点）的 status 逐行断言
func demoAssertMenuStatus(t *testing.T, db *gorm.DB, name string, want uint8) {
	t.Helper()
	rows := demoRules(t, db, name)
	if len(rows) == 0 {
		t.Fatalf("插件 %s 应有归属行（菜单/节点）", name)
	}
	for i := range rows {
		if rows[i].Status == nil || *rows[i].Status != want {
			t.Errorf("规则 %s 的 status 应为 %d，实际 %v", rows[i].Name, want, rows[i].Status)
		}
	}
}

// demoAuditCount 审计行计数（action/status 精确匹配）
func demoAuditCount(t *testing.T, db *gorm.DB, name, action, status string) int64 {
	t.Helper()
	count, err := gorm.G[cli.PluginAction](db).
		Where("plugin = ? AND action = ? AND status = ?", name, action, status).
		Count(context.Background(), "*")
	if err != nil {
		t.Fatalf("统计审计失败: %v", err)
	}
	return count
}

// demoRowCount 表行数（表名先过标识符白名单）
func demoRowCount(t *testing.T, db *gorm.DB, table string) int64 {
	t.Helper()
	if !demoIdentifierSafe.MatchString(table) {
		t.Fatalf("表名 %q 不是安全标识符", table)
	}
	var count int64
	if err := db.WithContext(context.Background()).Raw(`SELECT COUNT(*) AS count FROM "` + table + `"`).Scan(&count).Error; err != nil {
		t.Fatalf("统计 %s 失败: %v", table, err)
	}
	return count
}

// demoMigrationState 插件版本表的 (版本, dirty)；表不存在 ⇒ (0,false)（与 cli 的读数同口径）
func demoMigrationState(t *testing.T, db *gorm.DB, name string) (int64, bool) {
	t.Helper()
	table := cli.MigrationTableName(name, prefixOf())
	if !demoIdentifierSafe.MatchString(table) {
		t.Fatalf("版本表名 %q 不是安全标识符", table)
	}
	if !tableExists(t, db, table) {
		return 0, false
	}
	rows := make([]struct {
		Version int64
		Dirty   bool
	}, 0, 1)
	if err := db.Raw(`SELECT version, dirty FROM "` + table + `" LIMIT 1`).Find(&rows).Error; err != nil {
		t.Fatalf("读取版本表 %s 失败: %v", table, err)
	}
	if len(rows) == 0 {
		return 0, false // 空版本表 = 无版本（干净）
	}
	return rows[0].Version, rows[0].Dirty
}

// demoAssertZeroResidue 卸载/清理后的零残留（DB 门控口径逐项对齐）：
// 台账 / 归属行 / 实体元数据 / configs 分组 / 源码树 / 视图副本 / 语言包副本 / 注册行。
func demoAssertZeroResidue(t *testing.T, env *cli.Env, db *gorm.DB, name string) {
	t.Helper()
	ctx := context.Background()
	if row, ok := demoLedger(t, db, name); ok {
		t.Errorf("台账应无 %s 的行: %+v", name, row)
	}
	if count, err := gorm.G[model.AdminRule](db).Where("plugin = ?", name).Count(ctx, "*"); err != nil || count != 0 {
		t.Errorf("归属行应清空，实际 %d err=%v", count, err)
	}
	if count, err := gorm.G[host.Entity](db).Where("plugin = ?", name).Count(ctx, "*"); err != nil || count != 0 {
		t.Errorf("实体元数据应清空，实际 %d err=%v", count, err)
	}
	if count, err := gorm.G[model.Config](db).Where(model.Config{Group: "plugin." + name}).Count(ctx, "*"); err != nil || count != 0 {
		t.Errorf("configs 分组应清空，实际 %d err=%v", count, err)
	}
	for _, rel := range []string{cli.PluginDir(name), cli.ViewTarget(name)} {
		if _, err := os.Stat(filepath.Join(env.Root, filepath.FromSlash(rel))); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("落位物 %s 应删除: %v", rel, err)
		}
	}
	if targets := demoLangTargets(t, env.Root, name); len(targets) != 0 {
		t.Errorf("语言包副本应删除: %v", targets)
	}
	registered, err := cli.RegisteredPlugins(env.Root)
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", cli.RegistryFile, err)
	}
	if slices.Contains(registered, name) {
		t.Errorf("%s 不应再有 %s 的注册行（实际 %v）", cli.RegistryFile, name, registered)
	}
}

// demoPurgeRows 收尾清理：只按插件名收口（行：plugin/name = <名>；表：<前缀>plugin_<名>_*）。
// best-effort：失败只记录，不掩盖用例原本的失败；绝不触碰其它插件与宿主数据。
func demoPurgeRows(t *testing.T, db *gorm.DB, prefix, name string) {
	t.Helper()
	ctx := context.Background()
	deletes := []struct {
		sql string
		arg string
	}{
		{`DELETE FROM "` + prefix + `admin_rules" WHERE "plugin" = ?`, name},
		{`DELETE FROM "` + prefix + `admin_plugin_entities" WHERE "plugin" = ?`, name},
		{`DELETE FROM "` + prefix + `admin_plugin_actions" WHERE "plugin" = ?`, name},
		{`DELETE FROM "` + prefix + `configs" WHERE "group" = ?`, "plugin." + name},
		{`DELETE FROM "` + prefix + `admin_plugins" WHERE "name" = ?`, name},
	}
	for _, item := range deletes {
		if err := db.WithContext(ctx).Exec(item.sql, item.arg).Error; err != nil {
			t.Logf("收尾清理失败（%s）: %v", item.sql, err)
		}
	}
	for _, table := range demoOwnedTables(prefix, name) {
		if !demoIdentifierSafe.MatchString(table) {
			t.Logf("收尾清理：表名 %q 不是安全标识符，跳过", table)
			continue
		}
		if err := db.WithContext(ctx).Exec(`DROP TABLE IF EXISTS "` + table + `"`).Error; err != nil {
			t.Logf("收尾清理表 %s 失败: %v", table, err)
		}
	}
}

// ---------- 断言：菜单/节点/页面/接线 ----------

// demoRoutePaths 插件声明的全部后台路由（绝对前缀 + 相对段；相对段 = 引擎与插件包实际注册的形状）。
// 节点名必须等于它们的 BuildCheckPath 推导结果（list/get 兼查同级的 /read，见 AdminPermission）。
func demoRoutePaths(name string) []string {
	base := config.Get().Server.AdminBaseRoutePath
	entity := base + "/plugin/" + name + "/" + demoEntity
	return []string{
		base + "/plugin/" + name + "/overview", // L1 自定义页端点（插件包 mount 的 /overview）
		entity + "/list", entity + "/create", entity + "/delete",
		entity + "/update/:pk", entity + "/get/:pk",
		entity + "/sort", // drag_sort: true ⇒ 引擎注册 /sort
	}
}

// demoReadFallback list/get 兼查 /read（AdminPermission 的既有口径）
func demoReadFallback(checkPath string) (string, bool) {
	idx := strings.LastIndex(checkPath, "/")
	if idx < 0 {
		return "", false
	}
	last := checkPath[idx+1:]
	if last != "list" && last != "get" {
		return "", false
	}
	return checkPath[:idx] + "/read", true
}

// demoAssertRouteNodes 节点名与路由推导**逐字相等**（否则非超管全 403，硬约束）
func demoAssertRouteNodes(t *testing.T, db *gorm.DB, name string) {
	t.Helper()
	declared := map[string]bool{}
	for _, row := range demoRules(t, db, name) {
		if row.Type == "node" {
			declared[row.Name] = true
		}
	}
	names := make([]string, 0, len(declared))
	for key := range declared {
		names = append(names, key)
	}
	slices.Sort(names)

	for _, full := range demoRoutePaths(name) {
		checkPath := permission.BuildCheckPath(full)
		if declared[checkPath] {
			continue
		}
		if fallback, ok := demoReadFallback(checkPath); ok && declared[fallback] {
			continue
		}
		t.Errorf("路由 %s 的校验节点 %s 不在归属行里（非超管会 403）：已声明 %v", full, checkPath, names)
	}
}

// demoAssertL1Page L1 页的存在性断言（验收 A 的「L1 页存在」）：
// 落位副本在、菜单 component 精确指向它、keepalive=0、源与副本逐字节一致。
func demoAssertL1Page(t *testing.T, root string, rows []model.AdminRule, name string) {
	t.Helper()
	viewRel := path.Join(cli.ViewTarget(name), "index.vue")
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(viewRel))); err != nil {
		t.Errorf("L1 页落位副本 %s 应在（F7：必须落 views/admin 下）：%v", viewRel, err)
	}
	menu := demoRuleByName(rows, "plugin/"+name+"/index")
	if menu == nil {
		t.Fatalf("缺 L1 页菜单行 plugin/%s/index（实际 %v）", name, demoRuleNames(rows))
	}
	wantComponent := "/src/views/admin/plugin/" + name + "/index.vue"
	if menu.Type != "menu" || menu.Component == nil || *menu.Component != wantComponent {
		t.Errorf("L1 页菜单 component 应精确指向 %s，实际 type=%s component=%v", wantComponent, menu.Type, menu.Component)
	}
	if menu.Path == nil || *menu.Path != "plugin/"+name+"/index" {
		t.Errorf("L1 页菜单 path 应为 plugin/%s/index，实际 %v", name, menu.Path)
	}
	if menu.Keepalive == nil || *menu.Keepalive != 0 {
		t.Errorf("插件菜单 keepalive 应为 0（组件名与规则名不一致时写 1 会静默不缓存），实际 %v", menu.Keepalive)
	}
	if menu.OpenType == nil || *menu.OpenType != "tab" {
		t.Errorf("菜单 open_type 应为 tab（对齐种子 000003 的既有形态），实际 %v", menu.OpenType)
	}

	src := filepath.Join(root, filepath.FromSlash(path.Join(cli.PluginDir(name), "web", "views", "index.vue")))
	dst := filepath.Join(root, filepath.FromSlash(viewRel))
	if !bytes.Equal(demoReadFile(t, src), demoReadFile(t, dst)) {
		t.Errorf("L1 源的落位副本与源不逐字节一致（源 %s，副本 %s）：源是唯一事实源，", src, dst)
	}
}

// demoAssertEntityPage L2 实体页菜单（固定公式）：菜单名/路径 = plugin/<名>/<实体>、
// component = 宿主通用页、keepalive=0（全部实体共用一个组件 ⇒ 写 1 会静默不缓存）。
func demoAssertEntityPage(t *testing.T, rows []model.AdminRule, name string) {
	t.Helper()
	menu := demoRuleByName(rows, "plugin/"+name+"/"+demoEntity)
	if menu == nil {
		t.Fatalf("缺 L2 实体页菜单 plugin/%s/%s（实际 %v）", name, demoEntity, demoRuleNames(rows))
	}
	if menu.Component == nil || *menu.Component != "/src/views/admin/plugin/render/index.vue" {
		t.Errorf("L2 实体页 component 应为宿主通用页 /src/views/admin/plugin/render/index.vue，实际 %v", menu.Component)
	}
	if menu.Keepalive == nil || *menu.Keepalive != 0 {
		t.Errorf("L2 实体页 keepalive 应为 0，实际 %v", menu.Keepalive)
	}
	if menu.Path == nil || *menu.Path != "plugin/"+name+"/"+demoEntity {
		t.Errorf("L2 实体页 path 应为 plugin/%s/%s（坐标契约），实际 %v", name, demoEntity, menu.Path)
	}
	group := demoRuleByName(rows, "plugin/"+name)
	if group == nil || group.Type != "dir" {
		t.Errorf("缺分组目录行 plugin/%s（type=dir）", name)
	}
}

// demoAssertRecompileSemantics 重编译语义等价断言（验收 A 的第三步；本用例不重编译）：
//   - 接线文件就是生成器对该清单的输出（无手工漂移；cli.CheckSkeleton 同口径）；
//   - 生成物引用的包真实存在，且它的 init() 里 host.Register 登记的 Name/Version/Kind 与本次安装一致
//     ——两者合起来等价于「重编译一次后进程内注册表会认识 <名>」；
//   - 当前进程（未重编译）不认识该插件 ⇒ 正是「待重编译」语义。
func demoAssertRecompileSemantics(t *testing.T, env *cli.Env, name, wantVersion, wantKind string) {
	t.Helper()
	content := demoReadFile(t, env.Abs(cli.RegistryFile))
	names, err := cli.RegisteredPluginsFrom(content)
	if err != nil {
		t.Fatalf("解析 %s 失败: %v", cli.RegistryFile, err)
	}
	if !slices.Contains(names, name) {
		t.Fatalf("%s 应含 %s 的注册行，实际 %v", cli.RegistryFile, name, names)
	}
	if want := cli.BuildRegistryFile(names); string(want) != string(content) {
		t.Errorf("%s 与生成器输出不一致（勿手工编辑）:\n实际:\n%s\n期望:\n%s", cli.RegistryFile, content, want)
	}
	regName, regVersion, regKind := demoRegisterConstants(t, env.Abs(path.Join(cli.PluginsDir, name)))
	if regName != name || regVersion != wantVersion || regKind != wantKind {
		t.Errorf("%s 的登记常量与安装的清单不一致：name=%q version=%q kind=%q（期望 %q/%q/%q）",
			path.Join(cli.PluginDir(name), "plugin.go"), regName, regVersion, regKind, name, wantVersion, wantKind)
	}
	if slices.Contains(cli.WiredNames(), name) {
		t.Errorf("当前进程不应已接线 %s（接线真相在编译产物；本用例未重编译）", name)
	}
}

// demoRegisterConstants 从插件包的 plugin.go 里读 host.Register(host.Meta{...}) 的字符串实参
// （插件按 scaffold 的约定把 Name/Version/Kind 写成同文件内的常量，故字面量与常量名都要认）。
func demoRegisterConstants(t *testing.T, dir string) (name, version, kind string) {
	t.Helper()
	file := filepath.Join(dir, "plugin.go")
	parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
	if err != nil {
		t.Fatalf("解析 %s 失败: %v", file, err)
	}

	consts := map[string]string{}
	for _, decl := range parsed.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, ident := range value.Names {
				if i >= len(value.Values) {
					continue
				}
				if literal := demoStringLiteral(value.Values[i]); literal != "" {
					consts[ident.Name] = literal
				}
			}
		}
	}

	for _, decl := range parsed.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "init" || fn.Body == nil {
			continue
		}
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || selector.Sel.Name != "Register" {
				return true
			}
			literal, ok := call.Args[0].(*ast.CompositeLit)
			if !ok {
				return true
			}
			for _, element := range literal.Elts {
				pair, ok := element.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				key, ok := pair.Key.(*ast.Ident)
				if !ok {
					continue
				}
				value := demoStringLiteral(pair.Value)
				if value == "" {
					if ident, ok := pair.Value.(*ast.Ident); ok {
						value = consts[ident.Name]
					}
				}
				switch key.Name {
				case "Name":
					name = value
				case "Version":
					version = value
				case "Kind":
					kind = value
				}
			}
			return true
		})
	}
	return name, version, kind
}

// demoStringLiteral 字符串字面量（双引号或反引号；不是字面量 ⇒ 空串）
func demoStringLiteral(expr ast.Expr) string {
	literal, ok := expr.(*ast.BasicLit)
	if !ok || literal.Kind != token.STRING {
		return ""
	}
	return strings.Trim(literal.Value, "\"`")
}

// demoGateProbe 用真实的运行时闸探一次：enabled ⇒ 200（放行），其余状态 ⇒ 404（不承认存在性）
func demoGateProbe(t *testing.T, name string) int {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	group := router.Group("/admin/plugin/" + name)
	group.Use(host.Gate(name))
	group.POST("/overview", func(c *gin.Context) { c.Status(http.StatusOK) })

	request := httptest.NewRequest(http.MethodPost, "/admin/plugin/"+name+"/overview", nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder.Code
}
