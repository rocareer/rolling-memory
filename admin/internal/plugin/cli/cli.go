// Package cli 实现 `aigo plugin` 命令组的业务逻辑（cobra 壳在 cmd/plugin，命令与旗标只做参数收集，
// 校验/预演/读写都在本包，便于 P2 的管理页与测试复用）。
//
// 设计说明：（状态机与审计）、（插件迁移运行器）、
// （dry-run 输出与信任定位）、（包格式与分发）。本包只写「清单外的宿主机件」一个文件
// （internal/plugins/register_gen.go），不覆盖任何既有宿主源文件。
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"github.com/ai-go-hub/ai-go-admin/internal/infra/config"
	"github.com/ai-go-hub/ai-go-admin/internal/infra/database"
	"github.com/ai-go-hub/ai-go-admin/internal/plugin/host"
	"github.com/ai-go-hub/ai-go-admin/internal/version"

	"gorm.io/gorm"
)

// 仓库内固定路径与模块路径（插件的 Go 包路径 = ModulePath + "/plugins/<名>"）
const (
	// PluginsDir 插件源码根目录（模块内顶层目录，可 import internal/**）
	PluginsDir = "plugins"
	// StoreDir 安装包留档目录（本地复装凭据；.gitignore 已忽略）
	StoreDir = "plugins/.store"
	// RegistryFile 接线生成物（安装器唯一改写的宿主文件；出厂空骨架）
	RegistryFile = "internal/plugins/register_gen.go"
	// ModulePath 模块路径（go.mod 的 module 行）
	ModulePath = "github.com/ai-go-hub/ai-go-admin"
	// WebViewsRoot 插件前端视图落位根（F7 要求：必须在 views/admin 下，否则菜单静默查不到）
	WebViewsRoot = "web/src/views/admin/plugin"
	// WebLangRoot 插件语言包落位根（filePathToKeys 生成 plugin.<名>.* 前缀）
	WebLangRoot = "web/src/lang"
)

// Env 命令运行环境（一次性解析：仓库根 / 表前缀 / 宿主版本 / 输出目标）
type Env struct {
	// Root 仓库根（含 go.mod）
	Root string
	// Prefix 数据库表前缀（长度预算与物理表名用）
	Prefix string
	// HostVersion 宿主版本 version.Get().Version（framework_req 提示用）
	HostVersion string
	// Out 输出目标（默认 os.Stdout；测试可替换）——命令的**正常输出**与 `--json` 的机器通道
	Out io.Writer
	// Err 诊断输出目标（默认 os.Stderr；测试可替换）——提示/告警走这里，不污染 Out 的机器通道
	Err io.Writer
}

// noticedRoots 已提示过归属的根（每进程每根一次；HTTP handler 会按请求构造 Env）
var noticedRoots sync.Map

// NewEnv 构造命令环境（初始化配置、定位仓库根）。
//
// 根定位与来源/清单通道共用 resolveSourceRoot（可选键 plugin.root → 否则 FindRoot）：
// 生命周期命令与「环境判定」因此对同一个事实给同一个答案——否则「非源码检出 + plugin.root 指向检出」
// 这种配置下，环境判定会判 dev/放行而 `aigo plugin install|uninstall` 却报「未找到 go.mod」，
// 页面标识与实际能力就互相矛盾了（plugins/README.md）。
//
// 这个根同时是 install/uninstall/scaffold/export/sync/defaults 的**写/删根**（Env.Abs 与 Env.Root），
// 相对路径参数也以它为基——配成另一个检出（跨仓安装是刻意支持的用法）时，构造后打印一行归属提示，
// 让「写进哪个模块」成为可见事实而不是考古结论。
func NewEnv() (*Env, error) {
	if !config.Initialized() {
		if err := config.Init(); err != nil {
			return nil, fmt.Errorf("初始化配置: %w", err)
		}
	}
	root, err := resolveSourceRoot()
	if err != nil {
		return nil, err
	}
	env := &Env{
		Root:        root,
		Prefix:      config.Get().Database.Prefix,
		HostVersion: version.Get().Version,
		Out:         os.Stdout,
		Err:         os.Stderr,
	}
	env.rootOwnerNotice()
	return env, nil
}

// rootOwnerNotice 在仓库根的 module 不是本模块时，往 **stderr** 提示一笔（跨仓安装/配错都适用）。
//
// 只提示不拒绝：configuredSourceRoot 只认「目录里有普通文件 go.mod」，不校验模块身份
// （跨仓安装是 plugin.root 存在的原因之一，但它可编译与否取决于目标仓自己是否提供本模块依赖，
// 这一点本提示不保证）；而「相对路径参数以 plugin.root 为基」这件事很安静 ⇒ 至少让写/删根与它的归属可见。
//
// 走 stderr 而不是 stdout：本函数在构造 env 时触发，而 stdout 是 `--json` 命令的机器通道
// （source.go 的 printJSON 就用 fmt.Println）——提示混进 stdout 会让 JSON 不可解析。
// 每进程每个根只提示一次：HTTP handler 是每次请求都构造 Env（router/admin/plugin 的 newEnv），
// 不这样会在跨仓部署下把服务日志刷成一行一请求。
func (e *Env) rootOwnerNotice() {
	text := rootOwnerNoticeText(e.Root)
	if text == "" {
		return
	}
	if _, seen := noticedRoots.LoadOrStore(e.Root, struct{}{}); seen {
		return
	}
	fmt.Fprint(e.ErrWriter(), text)
}

// rootOwnerNoticeText 提示文案（纯函数，便于钉住「打印/抑制」两个分支与文案本身）。
// 返回空串 = 不提示：module 读不出（无 go.mod / 不可读 / 写法认不出）或就是本模块。
func rootOwnerNoticeText(root string) string {
	module := moduleLine(root)
	if module == "" || module == ModulePath {
		return ""
	}
	return fmt.Sprintf("⚠ 插件根 %s 的模块是 %s（非 %s）：install/uninstall/scaffold/export/sync/defaults 会写入/删除该仓，相对路径参数也以它为基\n",
		root, module, ModulePath)
}

// Writer 输出目标（nil 时回落到 stdout）
func (e *Env) Writer() io.Writer {
	if e.Out == nil {
		return os.Stdout
	}
	return e.Out
}

// ErrWriter 诊断输出目标（nil 时回落到 stderr）
func (e *Env) ErrWriter() io.Writer {
	if e.Err == nil {
		return os.Stderr
	}
	return e.Err
}

// Printf 输出一行（命令面的统一出口）
func (e *Env) Printf(format string, args ...any) {
	fmt.Fprintf(e.Writer(), format, args...)
}

// Abs 把仓库相对路径（/ 分隔）转成本机绝对路径
func (e *Env) Abs(rel string) string {
	return filepath.Join(e.Root, filepath.FromSlash(rel))
}

// PluginDir 插件源码目录（仓库相对路径）
func PluginDir(name string) string {
	return path.Join(PluginsDir, name)
}

// ImportPath 插件的 Go 包导入路径（空白导入注册行用它）
func ImportPath(name string) string {
	return ModulePath + "/" + path.Join(PluginsDir, name)
}

// ViewTarget 插件视图落位目标（仓库相对路径）
func ViewTarget(name string) string {
	return path.Join(WebViewsRoot, name)
}

// LangTarget 插件语言包落位目标（lang 为语言目录名，如 zh-cn）
func LangTarget(lang, name string) string {
	return path.Join(WebLangRoot, lang, "plugin", name+".yaml")
}

// EnsureDB 取数据库句柄（未初始化则初始化一次；idempotent）
func (e *Env) EnsureDB(ctx context.Context) (*gorm.DB, error) {
	if db := database.DB(); db != nil {
		return db, nil
	}
	if err := database.Init(); err != nil {
		return nil, fmt.Errorf("初始化数据库: %w", err)
	}
	db := database.DB()
	if db == nil {
		return nil, errors.New("数据库句柄不可用")
	}
	return db, nil
}

// FindRoot 从当前目录向上定位仓库根（含 go.mod 的目录）
func FindRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("获取当前目录: %w", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("未找到 go.mod（请在仓库内运行本命令）")
		}
		dir = parent
	}
}

// resolveSourceRoot 源码根定位（唯一实现：NewEnv / NewSourceEnv / 环境判定共用，避免三处口径漂移）：
// 优先可选配置键 plugin.root（非空且目录内含 go.mod），否则 FindRoot() 从当前目录向上查找 go.mod。
// 读配置那一步留给 resolveSourceRootFrom —— 判定优先级因此可以在不起全局 viper 的前提下被钉住。
func resolveSourceRoot() (string, error) {
	configured := ""
	if vip := config.Viper(); vip != nil {
		configured = vip.GetString("plugin.root")
	}
	return resolveSourceRootFrom(configured)
}

// resolveSourceRootFrom 判定纯函数：显式根命中（目录内有普通文件 go.mod）就用它，否则回退 FindRoot()。
// 当前目录到 / 都没有 go.mod 时返回 FindRoot 的错误（保留「未找到 go.mod（请在仓库内运行本命令）」这句原文案）。
func resolveSourceRootFrom(configuredRoot string) (string, error) {
	if root, ok := configuredSourceRoot(configuredRoot); ok {
		return root, nil
	}
	return FindRoot()
}

// PluginSource 解析命令给出的插件源：优先按路径（目录 / plugin.yaml），退化按插件名查 plugins/<名>。
// 返回插件目录（仓库相对路径由调用方用 path 组装，这里返回绝对路径）与 manifest 路径。
func (e *Env) PluginSource(arg string) (dir string, err error) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return "", errors.New("缺少插件源：给插件名或插件目录/包路径")
	}

	candidates := []string{arg}
	if !strings.ContainsAny(arg, "/\\") {
		candidates = append(candidates, PluginDir(arg))
	}
	for _, candidate := range candidates {
		full := candidate
		if !filepath.IsAbs(full) {
			full = e.Abs(candidate)
		}
		info, statErr := os.Stat(full)
		if statErr != nil {
			continue
		}
		if !info.IsDir() {
			// 允许直接给 plugin.yaml 路径
			if filepath.Base(full) == "plugin.yaml" {
				return filepath.Dir(full), nil
			}
			return "", fmt.Errorf("插件源 %q 不是目录（zip 包走 `aigo plugin install <包>`：staging 解包 + zip 防护 + 清单校验都在安装链内）", arg)
		}
		if _, statErr := os.Stat(filepath.Join(full, "plugin.yaml")); statErr != nil {
			if errors.Is(statErr, fs.ErrNotExist) {
				return "", fmt.Errorf("目录 %s 内没有 plugin.yaml", full)
			}
			return "", statErr
		}
		return full, nil
	}
	return "", fmt.Errorf("插件源 %q 不存在（既不是本地目录，也不是 plugins/%s）", arg, arg)
}

// PluginName 取插件目录名（= manifest.name = 台账 = 路由段）
func PluginName(dir string) string {
	return path.Base(filepath.ToSlash(dir))
}

// WiredNames 进程内注册表里的插件名（接线真相之一；CLI 与 serve 是同一二进制，
// 插件包的 init() 在进程启动时即执行，故 CLI 侧也能看到编译期接线结果）
func WiredNames() []string {
	metas := host.Registered()
	names := make([]string, 0, len(metas))
	for _, meta := range metas {
		names = append(names, meta.Name)
	}
	return names
}
