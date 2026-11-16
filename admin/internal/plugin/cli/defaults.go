package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/ai-go-hub/ai-go-admin/internal/plugin/host"

	"gorm.io/gorm"
)

// DefaultsOperator `aigo plugin defaults` 未显式给 --operator 时的审计署名。
const DefaultsOperator = "defaults"

// DefaultsOptions `aigo plugin defaults` 的旗标。
type DefaultsOptions struct {
	// DryRun 只预演：同一套状态机与安装预演照跑（该拒的照实拒），一行都不写、不写审计。
	DryRun bool
	// Operator 审计署名（只是署名，不构成身份认证）。
	Operator string
}

// defaultAction 默认插件在台账当前状态下应执行的动作（纯函数，便于逐格测试）。
type defaultAction int

const (
	// defaultActionNone 已 enabled：无变化
	defaultActionNone defaultAction = iota
	// defaultActionInstall 台账无行：走真实安装链并启用
	defaultActionInstall
	// defaultActionEnable installed / disabled：只启用（安装态与文件已在）
	defaultActionEnable
)

// defaultActionFor 默认插件的状态机（exists=false ⇒ 安装；enabled ⇒ 无变化；installed/disabled ⇒ 启用；
// failed ⇒ 拒绝并给出清理入口；四态之外的脏值一律 fail-closed）。
func defaultActionFor(name, status string, exists bool) (defaultAction, error) {
	if !exists {
		return defaultActionInstall, nil
	}
	switch status {
	case host.StatusEnabled:
		return defaultActionNone, nil
	case host.StatusInstalled, host.StatusDisabled:
		return defaultActionEnable, nil
	case host.StatusFailed:
		return 0, fmt.Errorf("默认插件 %s 处于 failed（半装）：只允许 `aigo plugin uninstall --force %s` 清理", name, name)
	default:
		return 0, fmt.Errorf("默认插件 %s 的台账状态 %q 不在四态内（fail-closed）", name, status)
	}
}

// EnsureDefaults 确保插件模块的默认依赖插件已安装并启用（幂等；正本 = host.DefaultPlugins）。
//
// 每个默认插件按台账状态分流：
//   - 无行 ⇒ 走 Install（真实安装链：落位 / 接线 / 审计，安装完成即启用）；
//   - installed / disabled ⇒ EnableWith（只改状态与菜单，不重装）；
//   - enabled ⇒ 无变化（不写审计）。
//
// 环境门禁同源：无行的默认插件在非 dev 环境由 RequireLifecycleWith 拒绝（安装是源码树操作），
// 已安装的只做启用（启停不受生产门禁限制）。DryRun 走同一状态机与 Install/EnableWith 的预演。
func EnsureDefaults(ctx context.Context, env *Env, opts DefaultsOptions) error {
	db, err := env.EnsureDB(ctx)
	if err != nil {
		return err
	}

	operator := opts.Operator
	if operator == "" {
		operator = DefaultsOperator
	}
	envState := DetectEnvironment()

	for _, name := range host.DefaultPlugins {
		row, err := gorm.G[host.Plugin](db).Where("name = ?", name).First(ctx)
		exists := err == nil
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("读取默认插件 %s 的台账失败: %w", name, err)
		}

		action, err := defaultActionFor(name, row.Status, exists)
		if err != nil {
			return err
		}
		switch action {
		case defaultActionNone:
			env.Printf("默认插件 %s：已启用（无变化）\n", name)
		case defaultActionInstall:
			// 生产环境不在启动/部署时偷偷装插件：无行即拒绝，提示走环境判定依据。
			if err := RequireLifecycleWith("安装默认依赖 "+name, envState); err != nil {
				return err
			}
			env.Printf("默认插件 %s：台账无行 ⇒ 安装并启用\n", name)
			source := PluginDir(name)
			if err := Install(ctx, env, source, InstallOptions{
				DryRun:   opts.DryRun,
				Enable:   true,
				Operator: operator,
				// 失败审计会进插件管理页：与 install 命令同一脱敏回调，避免把本机绝对路径写进台账
				Sanitize: func(text string) string { return SanitizePluginText(env, text, source) },
			}); err != nil {
				return fmt.Errorf("安装默认插件 %s 失败: %w", name, err)
			}
		case defaultActionEnable:
			env.Printf("默认插件 %s：%s ⇒ 启用\n", name, row.Status)
			if err := EnableWith(ctx, env, name, StatusOptions{DryRun: opts.DryRun, Operator: operator}); err != nil {
				return fmt.Errorf("启用默认插件 %s 失败: %w", name, err)
			}
		}
	}
	return nil
}
