package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/ai-go-hub/ai-go-admin/internal/model"
	"github.com/ai-go-hub/ai-go-admin/internal/plugin/host"
	"github.com/ai-go-hub/ai-go-admin/pkg/util"

	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// 审计动作与结果（值域见插件机制框架迁移 *_plugin.up.sql 的列注释；CLI 与管理页复用同一份常量与写入函数）
const (
	ActionInstall   = "install"
	ActionEnable    = "enable"
	ActionDisable   = "disable"
	ActionUninstall = "uninstall"
	ActionUpgrade   = "upgrade"
	ActionVerify    = "verify"

	AuditSuccess  = "success"
	AuditFailed   = "failed"
	AuditRejected = "rejected"
)

// PluginAction 插件动作审计行（表 admin_plugin_actions；append-only）。
// 唯一定义在此：四层管理面经 internal/repository/admin/plugin 的别名（PluginAction = cli.PluginAction）
// 复用本类型，不另立一份（cli 已 import model，把本类型并进 internal/model 会成环）。
type PluginAction struct {
	ID        uint           `json:"id"`
	Plugin    string         `json:"plugin"`
	Action    string         `json:"action"`
	Operator  string         `json:"operator"`
	Status    string         `json:"status"`
	Detail    datatypes.JSON `json:"detail"`
	CreatedAt time.Time      `json:"created_at"`
}

// TableName 固定表名（表前缀由迁移的 __PREFIX__ 占位符承担）
func (PluginAction) TableName() string {
	return "admin_plugin_actions"
}

// WriteAudit 写一条插件审计行（CLI 与管理页同一份留痕；--operator 只是署名，不构成身份认证）
func WriteAudit(ctx context.Context, db *gorm.DB, plugin, action, operator, status string, detail map[string]any) error {
	row, err := newAuditRow(plugin, action, operator, status, detail)
	if err != nil {
		return err
	}
	if err := gorm.G[PluginAction](db).Create(ctx, &row); err != nil {
		return fmt.Errorf("写入插件审计失败: %w", err)
	}
	return nil
}

// newAuditRow 组装一条审计行（WriteAudit 与「状态变更同事务写审计」共用同一份规整：
// 署名 trim、detail 序列化、operator 缺省 "cli"）
func newAuditRow(plugin, action, operator, status string, detail map[string]any) (PluginAction, error) {
	payload, err := json.Marshal(detail)
	if err != nil {
		return PluginAction{}, fmt.Errorf("序列化审计详情失败: %w", err)
	}
	if operator = trimOperator(operator); operator == "" {
		operator = "cli"
	}
	return PluginAction{
		Plugin:   plugin,
		Action:   action,
		Operator: operator,
		Status:   status,
		Detail:   datatypes.JSON(payload),
	}, nil
}

// trimOperator 规整署名（varchar(64)）：按 **Unicode 码点**截断（pkg/util.TruncateStr）——
// 按字节切会把多字节字符切成半个，落库即非法 UTF-8。
func trimOperator(operator string) string {
	return util.TruncateStr(operator, 64)
}

// StatusOptions 启停（enable/disable）的选项
type StatusOptions struct {
	// Operator 审计署名（只是署名，不构成身份认证）
	Operator string
	// DryRun 打印式预演（「A 的每一步都要能在 dry-run 中预演」）：
	// 同一套状态机检查照跑（该拒的照实拒），但一行都不写、不写审计
	DryRun bool
}

// Enable 置插件为 enabled：状态 + 菜单/节点 status=1 + 审计。
// 运行时闸按请求读库（≤1s 缓存）⇒ 本动作无需重启；但「是否已接线」看编译产物，
// 台账 enabled 而进程内没有该插件时明确提示「待重编译」，不假装已生效。
func Enable(ctx context.Context, env *Env, name, operator string) error {
	return EnableWith(ctx, env, name, StatusOptions{Operator: operator})
}

// Disable 置插件为 disabled：状态 + 菜单/节点 status=0 + 审计（还原只有这一条路径）
func Disable(ctx context.Context, env *Env, name, operator string) error {
	return DisableWith(ctx, env, name, StatusOptions{Operator: operator})
}

// EnableWith 带选项的启用（--dry-run 走这里；语义与 Enable 完全一致，只是可预演）
func EnableWith(ctx context.Context, env *Env, name string, opts StatusOptions) error {
	return setPluginStatusWith(ctx, env, name, opts, host.StatusEnabled)
}

// DisableWith 带选项的停用（--dry-run 走这里）
func DisableWith(ctx context.Context, env *Env, name string, opts StatusOptions) error {
	return setPluginStatusWith(ctx, env, name, opts, host.StatusDisabled)
}

// setPluginStatus 不带选项的启停（署名 + 目标状态）：安装链的 `--enable` 收尾走它——
// 那条路径没有 --dry-run 语义（install 的预演在编排开始前就返回了），保持既有签名不变。
func setPluginStatus(ctx context.Context, env *Env, name, operator, target string) error {
	return setPluginStatusWith(ctx, env, name, StatusOptions{Operator: operator}, target)
}

// setPluginStatusWith 启停共用的状态机与写库（四态与允许的下一步）。
//
// 原子性（「每动作留审计」）：台账 status、菜单/节点 status 与**成功审计**在同一个事务里落，
// 不存在「改了状态但没留痕」的中间态；失败审计在回滚之后单独写（写在事务里会被一并回滚），
// 它自身写失败必须并入返回错误——不允许「报了失败却连失败痕迹都没有」被静默吞掉。
func setPluginStatusWith(ctx context.Context, env *Env, name string, opts StatusOptions, target string) error {
	db, err := env.EnsureDB(ctx)
	if err != nil {
		return err
	}

	row, err := gorm.G[host.Plugin](db).Where("name = ?", name).First(ctx)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("台账无插件 %q（未安装）：先 `aigo plugin install`", name)
	}
	if err != nil {
		return fmt.Errorf("读取插件台账失败: %w", err)
	}

	if row.Status == target {
		env.Printf("插件 %s 已是 %s（无变化，未写审计）\n", name, target)
		return nil
	}
	switch target {
	case host.StatusEnabled:
		if row.Status == host.StatusFailed {
			return fmt.Errorf("插件 %s 处于 failed（半装）：只允许 `aigo plugin uninstall --force %s` 清理", name, name)
		}
	case host.StatusDisabled:
		if row.Status != host.StatusEnabled {
			return fmt.Errorf("插件 %s 当前是 %s，只有 enabled 才能停用", name, row.Status)
		}
	default:
		return fmt.Errorf("未知目标状态 %q", target)
	}

	menuStatus := 0
	if target == host.StatusEnabled {
		menuStatus = 1
	}
	// 将改写的归属行数（dry-run 的读数；只读计数，走 admin_rules 的 plugin 归属面）
	menuCount, err := gorm.G[model.AdminRule](db).Where("plugin = ?", name).Count(ctx, "*")
	if err != nil {
		return fmt.Errorf("统计插件 %s 的归属菜单/节点失败: %w", name, err)
	}

	if opts.DryRun {
		env.Printf("插件 %s 启停预演（--dry-run）：只读，不写任何行、不写审计\n", name)
		env.Printf("  · 台账 admin_plugins.status：%s → %s\n", row.Status, target)
		env.Printf("  · 归属菜单/节点（admin_rules.plugin=%s）%d 条：status → %d\n", name, menuCount, menuStatus)
		env.Printf("  · 审计：admin_plugin_actions 一条 %s/%s（署名 %s）\n", actionOf(target), AuditSuccess, auditOperator(opts.Operator))
		env.Printf("  · 生效方式：运行时闸按请求读库（≤1s 缓存），无需重启\n")
		if !isWired(name) {
			env.Printf("  · 进程内注册表没有插件 %s：台账会被置 %s，但路由要等「重编译 + 重启」后才存在（接线真相在编译产物）\n", name, target)
		}
		env.Printf("--dry-run：以上为启停预演，未写任何行、未写审计\n")
		return nil
	}

	now := time.Now()
	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := gorm.G[host.Plugin](tx).Where("name = ?", name).Update(ctx, "status", target); err != nil {
			return err
		}
		if _, err := gorm.G[host.Plugin](tx).Where("name = ?", name).Update(ctx, "updated_at", now); err != nil {
			return err
		}
		// 菜单/节点随状态一并置位（卸载清理按归属列整树进行，启停只改 status）
		if _, err := gorm.G[model.AdminRule](tx).Where("plugin = ?", name).Update(ctx, "status", menuStatus); err != nil {
			return err
		}
		// 成功审计与状态变更同事务：两处分事务会让「状态改了但没留痕」成为可能（「每动作留审计」）
		audit, err := newAuditRow(name, actionOf(target), opts.Operator, AuditSuccess, map[string]any{
			"from":         row.Status,
			"to":           target,
			"menu_status":  menuStatus,
			"menu_rows":    menuCount,
			"wired":        isWired(name),
			"effective_by": "gate_ttl（按请求读库，≤1s 生效，无需重启）",
		})
		if err != nil {
			return err
		}
		return gorm.G[PluginAction](tx).Create(ctx, &audit)
	})
	if err != nil {
		// 失败审计在回滚之后单独写：写在同一个事务里会被一并回滚（等于什么也没留下）。
		// 它自身失败必须如实并入错误——不允许「报了失败却连失败痕迹都没有」被静默吞掉。
		auditErr := WriteAudit(ctx, db, name, actionOf(target), opts.Operator, AuditFailed, statusFailureAuditDetail(env, err))
		if auditErr != nil {
			return fmt.Errorf("写库失败: %w；且 failed 审计也未能写入: %v（fail-closed：本动作无任何留痕）", err, auditErr)
		}
		return fmt.Errorf("写库失败: %w（已留 failed 审计）", err)
	}

	env.Printf("插件 %s：%s → %s（菜单/节点 status=%d，%d 条）\n", name, row.Status, target, menuStatus, menuCount)
	switch {
	case row.Kind == "declarative":
		// 声明式插件没有 Go 注册行（L2 实体路由由引擎按元数据注册）
		env.Printf("声明式插件（kind=declarative）：无 Go 注册行；实体路由由引擎按元数据注册，新增/变更实体需重启进程后生效\n")
	case !isWired(name):
		env.Printf("⚠ 进程内注册表没有插件 %s：台账已置 %s，但路由要等「重编译 + 重启」后才存在（接线真相在编译产物）\n", name, target)
	case target == host.StatusEnabled:
		env.Printf("生效方式：运行时闸按请求读库（≤1s 缓存）⇒ 无需重启，最迟一个缓存周期后路由返回 200\n")
	}
	return nil
}

// statusFailureAuditDetail 启停写库失败的审计 detail。
//
// error 走 SanitizePluginText（与 catalog 通道的 sanitizeCatalogText 同一实现）：写库失败的原因会带
// 落位目标/仓库根一类的绝对路径，而该字符串会在插件管理页的动作审计区块展示（第 7 条）。
// 抽成函数是为了让「脱敏确实作用在落库的 detail 上」有一条不依赖数据库的断言（与 install 侧的
// failureAuditDetail 同口径，见 install_options_test.go）。
func statusFailureAuditDetail(env *Env, cause error) map[string]any {
	return map[string]any{"error": SanitizePluginText(env, cause.Error())}
}

// auditOperator 审计署名在预演里的展示形态（与 newAuditRow 的缺省口径一致，不产生副作用）
func auditOperator(operator string) string {
	if operator = trimOperator(operator); operator == "" {
		return "cli（缺省）"
	}
	return operator
}

// actionOf 目标状态对应的审计动作名
func actionOf(status string) string {
	if status == host.StatusEnabled {
		return ActionEnable
	}
	return ActionDisable
}

// isWired 插件是否已接线到本进程（编译产物里的注册行）
func isWired(name string) bool {
	return slices.Contains(WiredNames(), name)
}
