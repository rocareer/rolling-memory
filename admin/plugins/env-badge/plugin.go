// plugin: env-badge v1.0.0
// Package envbadge 是「环境检测浮标」插件（kind: code）的 Go 包。
//
// 只做两件事（与其它插件同构）：
//   - init()：把自身登记进宿主注册表（只 import internal/plugin/host；**不要** import
//     internal/plugins——那会构成 Go 非法 import cycle）；
//   - mount()：在宿主建好的 `plugin/env-badge` 子组上挂浮标数据端点（只写相对段）。
//
// 环境判定不另立口径：直接复用宿主唯一权威 internal/plugin/cli/environment.go 的 DetectEnvironment
// （显式 plugin.env 优先；未配置时按源码检出自动判定）。本包只负责把判定结果翻成英文浮标契约。
//
// 目录名带连字符（插件名规则允许），Go 包名取无连字符的 envbadge；import path 仍是 plugins/env-badge。
package envbadge

import (
	"fmt"
	"strings"

	"github.com/ai-go-hub/ai-go-admin/internal/kit/httpx"
	"github.com/ai-go-hub/ai-go-admin/internal/middleware"
	"github.com/ai-go-hub/ai-go-admin/internal/plugin/cli"
	"github.com/ai-go-hub/ai-go-admin/internal/plugin/host"
	"github.com/ai-go-hub/ai-go-admin/internal/version"

	"github.com/gin-gonic/gin"
)

// 插件自身的事实（与 plugin.yaml 逐字对齐）
const (
	// Name 插件名（= plugins/ 目录名 = admin_plugins.name = 路由段 plugin/<名>）
	Name = "env-badge"
	// Version 插件版本（= plugin.yaml 的 version）
	Version = "1.0.0"
	// Kind 插件形态（= plugin.yaml 的 kind）
	Kind = "code"
)

func init() {
	host.Register(host.Meta{Name: Name, Version: Version, Kind: Kind, Mount: mount})
}

// mount 浮标数据端点：只挂 AdminAuth，不挂 AdminPermission。
//
// 浮标是面向全体已登录管理员的展示件（不在侧栏、不占权限节点）：它只回报 dev/prod 与判定依据，
// 不含本机路径、配置敏感值或业务数据；为此要求每个角色组都授予一个节点只会让非超管永远看不到环境标识。
func mount(g *gin.RouterGroup) {
	g.POST("/info", middleware.AdminAuth(), info)
}

// infoResponse 浮标契约（字段 snake_case；前端 convertCase 保持关闭，与插件管理面同口径）。
type infoResponse struct {
	// Name 判定结果：dev | prod（与宿主插件生命周期门禁同源）
	Name string `json:"name"`
	// Label 浮标正文（英文大写：DEV | PROD）
	Label string `json:"label"`
	// Title 英文环境名（Development | Production），供 tooltip 首行使用
	Title string `json:"title"`
	// Basis 英文判定依据（显式配置 / 源码检出自动判定 / 认不出的配置值按生产处置）
	Basis string `json:"basis"`
	// Source 判定依据码：config（显式配置）| auto（未配置，按能力判定）
	Source string `json:"source"`
	// Configured 显式配置的原始值（Source=config 时非空；认不出的值也原样回报，便于运维自查）
	Configured string `json:"configured"`
	// LifecycleAllowed 安装 / 卸载是否放行（= Name 为 dev）
	LifecycleAllowed bool `json:"lifecycle_allowed"`
	// HostVersion 宿主版本（展示用；VERSION 属各仓保留）
	HostVersion string `json:"host_version"`
	// HostAPIVersion 插件机制接口版本（manifest 的 host_api 对本常量校验）
	HostAPIVersion string `json:"host_api_version"`
}

// info 当前环境判定（POST /<后台入口前缀>/plugin/env-badge/info）
func info(c *gin.Context) {
	httpx.Success(c, httpx.WithData(buildInfo(cli.DetectEnvironment(), version.Get().Version, host.APIVersion)))
}

// buildInfo 把宿主判定翻成浮标契约（纯函数，便于逐格测试）。
func buildInfo(env cli.Environment, hostVersion, hostAPIVersion string) infoResponse {
	title := "Production"
	if env.Name == cli.EnvDev {
		title = "Development"
	}
	return infoResponse{
		Name:             env.Name,
		Label:            strings.ToUpper(env.Name),
		Title:            title,
		Basis:            basisEn(env),
		Source:           env.Source,
		Configured:       env.Configured,
		LifecycleAllowed: env.LifecycleAllowed,
		HostVersion:      hostVersion,
		HostAPIVersion:   hostAPIVersion,
	}
}

// basisEn 判定依据的英文表述（只决定 tooltip 文案；dev/prod 与放行结论一律以 Environment 为准）。
func basisEn(env cli.Environment) string {
	if env.Source == cli.EnvSourceConfig {
		if !knownEnvValue(env.Configured) {
			return fmt.Sprintf("explicit config plugin.env=%q is not recognized (only dev | prod) — treated as production", env.Configured)
		}
		return "explicit config plugin.env=" + strings.TrimSpace(env.Configured)
	}
	if env.LifecycleAllowed {
		return "auto-detected: source checkout present (plugin.env not set)"
	}
	return "auto-detected: no source checkout (plugin.env not set)"
}

// knownEnvValue 显式配置值是否是可识别的环境名（与 cli.isKnownEnvValue 同口径；仅供文案判读）。
func knownEnvValue(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case cli.EnvDev, cli.EnvProd:
		return true
	default:
		return false
	}
}
