package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ai-go-hub/ai-go-admin/internal/model"
	"github.com/ai-go-hub/ai-go-admin/internal/plugin/host"
	"github.com/ai-go-hub/ai-go-admin/internal/plugin/manifest"

	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// configKeyPattern 插件配置键的合形（对齐 configs.name 的 varchar(64) 与既有 snake_case 口径）
var configKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// installArtifacts ④ 写入的 DB 侧产物（补偿回滚与卸载清理按同一张清单逐项对齐）
type installArtifacts struct {
	// PluginID 台账行 ID
	PluginID uint
	// RuleIDs 归属本插件的 admin_rules 行 ID（含子树）
	RuleIDs []uint
	// EntityIDs 实体元数据行 ID
	EntityIDs []uint
	// ConfigIDs configs 行 ID
	ConfigIDs []uint
}

// writeInstallDB 安装编排的 ④：**单事务**写入台账行（status=failed，fail-closed：
// 手工插入的行不应被当作已安装）+ 实体元数据 + 菜单/节点（打归属列，status=0 隐藏至 enable）+
// configs 的 plugin.<名> 分组。任一失败即整事务回滚（无半写现场）。
func writeInstallDB(ctx context.Context, env *Env, db *gorm.DB, plan *InstallPlan, source, checksum, signedBy string) (*installArtifacts, error) {
	m := plan.Manifest
	now := time.Now()
	artifacts := &installArtifacts{}

	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if row, err := gorm.G[host.Plugin](tx).Where("name = ?", m.Name).First(ctx); err == nil {
			return fmt.Errorf("插件 %s 台账已有行（状态 %s）：重复安装不接受——已装者先 `aigo plugin uninstall %s`，半装（failed）用 `aigo plugin uninstall --force %s` 清理",
				m.Name, row.Status, m.Name, m.Name)
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("读取台账失败: %w", err)
		}

		// 名称撞车硬拦截（第二道）：待写 admin_rules.name 与既有行同名 ⇒ 拒装
		// （同名 vue-router 路由是「删旧加新」，会静默替换宿主页面；前缀白名单是第一道，DB 查重是第二道）。
		if err := checkRuleNameCollision(ctx, tx, plan); err != nil {
			return err
		}

		ledger := host.Plugin{
			Name:         m.Name,
			Title:        m.Title,
			Version:      m.Version,
			Kind:         m.Kind,
			Status:       host.StatusFailed,
			HostAPI:      m.HostAPI,
			HostVersion:  env.HostVersion,
			FrameworkReq: m.FrameworkReq,
			Requires:     m.Requires,
			Checksum:     checksum,
			Source:       source,
			SignedBy:     signedBy,
			UpdatedAt:    &now,
		}
		if err := gorm.G[host.Plugin](tx).Create(ctx, &ledger); err != nil {
			return fmt.Errorf("写入插件台账失败: %w", err)
		}
		artifacts.PluginID = ledger.ID

		if err := writeInstallRules(ctx, tx, plan, now, artifacts); err != nil {
			return err
		}
		if err := writeInstallEntities(ctx, tx, env, plan, now, artifacts); err != nil {
			return err
		}
		return writeInstallConfigs(ctx, tx, plan, artifacts)
	})
	if err != nil {
		return nil, err
	}
	return artifacts, nil
}

// checkRuleNameCollision 菜单/节点名与既有 admin_rules 行撞车检查
func checkRuleNameCollision(ctx context.Context, db *gorm.DB, plan *InstallPlan) error {
	if len(plan.Menus) == 0 {
		return nil
	}
	names := make([]string, 0, len(plan.Menus))
	for _, menu := range plan.Menus {
		names = append(names, menu.Name)
	}
	rows, err := gorm.G[model.AdminRule](db).Select("id, name").Where("name IN ?", names).Find(ctx)
	if err != nil {
		return fmt.Errorf("检查规则名撞车失败: %w", err)
	}
	if len(rows) == 0 {
		return nil
	}
	collided := make([]string, 0, len(rows))
	for i := range rows {
		collided = append(collided, rows[i].Name)
	}
	sort.Strings(collided)
	return fmt.Errorf("插件 %s 声明了与既有 admin_rules 同名的规则 %s：拒装（同名 vue-router 路由会静默替换宿主页面）",
		plan.Name, strings.Join(collided, ", "))
}

// writeInstallRules 代写菜单/节点（插件自带 SQL 不得写 admin_rules；父先子后按 pid 依赖排序）。
//
// 归属列 plugin 按**列名直写**（建行后回填），不经 model 字段：插件模块必须能在没有该字段的宿主上
// 编译并工作（宿主加字段只是把归属暴露到列表/详情 API 的展示增强）。列缺失时这里立即失败即停，
// 不静默丢归属（启动预检另有列存在性前置检查，见 host.Preflight）。
func writeInstallRules(ctx context.Context, tx *gorm.DB, plan *InstallPlan, now time.Time, artifacts *installArtifacts) error {
	ordered, err := orderMenus(plan.Menus)
	if err != nil {
		return err
	}
	ids := make(map[string]uint, len(ordered))
	for _, menu := range ordered {
		rule := model.AdminRule{
			Type:      menu.Type,
			Title:     menu.Title,
			Name:      menu.Name,
			Path:      stringPtr(menu.Path),
			Icon:      stringPtr(menu.Icon),
			OpenType:  stringPtr(openTypeOf(menu.Type)),
			URL:       stringPtr(""),
			Component: stringPtr(menu.Component),
			Keepalive: uint8Ptr(0),
			Extend:    stringPtr(""),
			Remark:    stringPtr(""),
			Weigh:     &menu.Weigh,
			Status:    uint8Ptr(0), // 安装期统一隐藏，enable 时置 1
			CreatedAt: &now,
			UpdatedAt: &now,
		}
		if menu.Pid != "" {
			pid := ids[menu.Pid]
			rule.Pid = &pid
		}
		if err := gorm.G[model.AdminRule](tx).Create(ctx, &rule); err != nil {
			return fmt.Errorf("写入菜单/节点 %s 失败: %w", menu.Name, err)
		}
		// 建行后回填归属（列名直写，见函数头注释）：写不进去就是错误，不允许静默留下无归属的行
		if _, err := gorm.G[model.AdminRule](tx).Where("id = ?", rule.ID).Update(ctx, "plugin", plan.Name); err != nil {
			return fmt.Errorf("写入菜单/节点 %s 的归属列失败: %w", menu.Name, err)
		}
		ids[menu.Name] = rule.ID
		artifacts.RuleIDs = append(artifacts.RuleIDs, rule.ID)
	}
	return nil
}

// openTypeOf 菜单打开方式：菜单项写 tab（对齐种子 000003 的既有形态），目录与节点留空
func openTypeOf(menuType string) string {
	if menuType == "menu" {
		return "tab"
	}
	return ""
}

// writeInstallEntities 写入 L2 实体元数据（spec 走与启动预检同一份校验器）
func writeInstallEntities(ctx context.Context, tx *gorm.DB, env *Env, plan *InstallPlan, now time.Time, artifacts *installArtifacts) error {
	for _, entity := range plan.Manifest.Entities {
		raw, err := json.Marshal(entity)
		if err != nil {
			return fmt.Errorf("序列化实体 %s 的 spec 失败: %w", entity.Entity, err)
		}
		probe := host.Entity{Plugin: plan.Name, Name: entity.Entity, Spec: datatypes.JSON(raw)}
		if err := host.ValidateSpec(probe); err != nil {
			return fmt.Errorf("实体 %s 的 spec 非法: %w", entity.Entity, err)
		}
		row := host.Entity{
			Plugin:        plan.Name,
			Name:          entity.Entity,
			TitleZh:       entity.TitleZh,
			TitleEn:       entity.TitleEn,
			PhysicalTable: PhysicalEntityTable(env.Prefix, plan.Name, entity.Entity),
			Spec:          datatypes.JSON(raw),
			CreatedAt:     &now,
			UpdatedAt:     &now,
		}
		if err := gorm.G[host.Entity](tx).Create(ctx, &row); err != nil {
			return fmt.Errorf("写入实体 %s 的元数据失败: %w", entity.Entity, err)
		}
		artifacts.EntityIDs = append(artifacts.EntityIDs, row.ID)
	}
	return nil
}

// PhysicalEntityTable L2 实体物理表名（显式存实际值，不由命名规则现场推导）
func PhysicalEntityTable(prefix, plugin, entity string) string {
	return prefix + "plugin_" + plugin + "_" + entity
}

// ConfigKey 插件配置键的落库名（configs.name 全局唯一 ⇒ 安装器加 plugin_<名>_ 前缀避免跨插件撞名）
func ConfigKey(plugin, key string) string {
	return "plugin_" + strings.ReplaceAll(plugin, "-", "_") + "_" + key
}

// writeInstallConfigs 写入 configs 的 plugin.<名> 分组（复用既有配置页渲染，不改前端）。
// allow_del=0：这些键的真源是 manifest，配置页不应允许删（删了下次安装会重建，语义上属安装器自管）。
func writeInstallConfigs(ctx context.Context, tx *gorm.DB, plan *InstallPlan, artifacts *installArtifacts) error {
	group := "plugin." + plan.Name
	for _, item := range plan.Manifest.Configs {
		key := ConfigKey(plan.Name, item.Name)
		if !configKeyPattern.MatchString(key) {
			return fmt.Errorf("配置键 %q（%s.%s）合形后不合法或超长（configs.name 为 varchar(64)）：装前先改短", key, plan.Name, item.Name)
		}
		if row, err := gorm.G[model.Config](tx).Where("name = ?", key).First(ctx); err == nil {
			if row.Group != group {
				return fmt.Errorf("configs 已有别处占用的同名键 %q（分组 %s）：插件配置键全局唯一（configs.name 唯一索引），请改键名", key, row.Group)
			}
			return fmt.Errorf("configs 已有同名键 %q（上一次安装的残留）：先清理该行再安装", key)
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("检查配置键 %q 失败: %w", key, err)
		}

		row := model.Config{
			Name:     key,
			Group:    group,
			Title:    item.Title,
			Type:     item.Type,
			Tip:      stringPtr(item.Tip),
			Value:    stringPtr(item.Value),
			Dict:     stringPtr(item.Dict),
			Rule:     stringPtr(item.Rule),
			AllowDel: 0,
			Weigh:    item.Weigh,
		}
		if err := gorm.G[model.Config](tx).Create(ctx, &row); err != nil {
			return fmt.Errorf("写入配置键 %q 失败: %w", item.Name, err)
		}
		// allow_del=0 必须**显式回写**：该列的 default 是 1（not null;default:1），GORM 对带 default 标签的
		// 零值字段在 INSERT 时会跳过、让 DB 默认值生效（零值 ≠ 默认值）——不回写的话配置页会给安装器
		// 自管的键渲染删除按钮（前端 v-if="item.allow_del"）。宿主自己的系统键同样是 allow_del=0。
		if _, err := gorm.G[model.Config](tx).Where("id = ?", row.ID).Update(ctx, "allow_del", 0); err != nil {
			return fmt.Errorf("回写配置键 %q 的 allow_del 失败: %w", item.Name, err)
		}
		artifacts.ConfigIDs = append(artifacts.ConfigIDs, row.ID)
	}
	return nil
}

// cleanReport 一次 DB 侧清理的读数（审计与 dry-run 共用）
type cleanReport struct {
	Rules        int64 // 删除的 admin_rules 归属行（含子树）
	Entities     int64 // 删除的实体元数据行
	Configs      int64 // 删除的 configs 分组行
	Groups       int   // 写回清除悬挂 ID 的角色组数
	DanglingIDs  int   // 清除的悬挂 ID 个数
	LedgerDelete bool  // 是否删除台账行
}

// cleanOwnedRows 清理该插件的全部归属行（卸载 c 与补偿回滚共用同一实现）：
//   - admin_rules 中 plugin=<名> 的行（含子节点，按归属列整树收口）；
//   - 各 admin_groups.rules CSV 里这些被删 ID 的悬挂项**一并写回清除**（复装节点 ID 已变，
//     清悬挂项不能恢复授权关系，只能避免残留）；
//   - admin_plugin_entities 元数据行（元数据是「期望态」，插件没了就不该留渲染依据）；
//   - configs 的 plugin.<名> 分组。
//
// 必须在调用方的事务内执行（与台账行删除、审计写入保持原子）。
func cleanOwnedRows(ctx context.Context, tx *gorm.DB, name string) (*cleanReport, error) {
	report := &cleanReport{}

	owned, err := gorm.G[model.AdminRule](tx).Select("id").Where("plugin = ?", name).Find(ctx)
	if err != nil {
		return nil, fmt.Errorf("读取归属规则行失败: %w", err)
	}
	ids := make(map[uint]struct{}, len(owned))
	for i := range owned {
		ids[owned[i].ID] = struct{}{}
	}
	if len(ids) > 0 {
		affected, err := gorm.G[model.AdminRule](tx).Where("plugin = ?", name).Delete(ctx)
		if err != nil {
			return nil, fmt.Errorf("删除归属规则行失败: %w", err)
		}
		report.Rules = int64(affected)
		groups, dangling, err := cleanGroupRuleIDs(ctx, tx, ids)
		if err != nil {
			return nil, err
		}
		report.Groups, report.DanglingIDs = groups, dangling
	}

	entities, err := gorm.G[host.Entity](tx).Where("plugin = ?", name).Delete(ctx)
	if err != nil {
		return nil, fmt.Errorf("删除实体元数据失败: %w", err)
	}
	report.Entities = int64(entities)

	// group 是 SQL 保留字：用结构体条件让 GORM 自行引用标识符（`"group" = ?`），不手拼
	configs, err := gorm.G[model.Config](tx).Where(model.Config{Group: "plugin." + name}).Delete(ctx)
	if err != nil {
		return nil, fmt.Errorf("删除配置键分组失败: %w", err)
	}
	report.Configs = int64(configs)
	return report, nil
}

// cleanGroupRuleIDs 把被删规则 ID 从各角色组的 rules CSV 里写回清除：
//   - 只清「本属本插件」的 ID（调用方传入），不动其它项；
//   - 无法解析的项原样保留（不猜、不吞）；
//   - 行数口径：admin_groups 行数与角色组数同级（个位到十位），整表读可接受（与 permission.GetRules 同读法）。
func cleanGroupRuleIDs(ctx context.Context, tx *gorm.DB, ids map[uint]struct{}) (updated int, removed int, err error) {
	groups, err := gorm.G[model.AdminGroup](tx).Where("rules IS NOT NULL AND rules <> '' AND rules <> '*'").Find(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("读取角色组失败: %w", err)
	}
	for i := range groups {
		if groups[i].Rules == nil {
			continue
		}
		original := *groups[i].Rules
		kept := make([]string, 0, strings.Count(original, ",")+1)
		for part := range strings.SplitSeq(original, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			id, parseErr := strconv.ParseUint(part, 10, 32)
			if parseErr != nil {
				kept = append(kept, part)
				continue
			}
			if _, hit := ids[uint(id)]; hit {
				removed++
				continue
			}
			kept = append(kept, strconv.FormatUint(id, 10))
		}
		next := strings.Join(kept, ",")
		if next == original {
			continue
		}
		if _, err := gorm.G[model.AdminGroup](tx).Where("id = ?", groups[i].ID).Update(ctx, "rules", next); err != nil {
			return updated, removed, fmt.Errorf("写回角色组 %d 的 rules 失败: %w", groups[i].ID, err)
		}
		updated++
	}
	return updated, removed, nil
}

// finishInstallStatus ⑧（收尾）：台账置 installed（菜单仍隐藏，enable 时置 1）+ installed_at/updated_at；
// adoptedThrough > 0 时另回写接管段列（adopt 装入：1..该版本与核心链对应迁移同物）——**必须落台账**而非只落审计：
// --purge 的门禁要在卸载那一刻判一个「状态事实」，而卸载会删台账行（设计说明）。
// 时机：本函数在 ⑥ 之后调用（安装 ④ 建台账行时还读不到执行侧的基线事实）。
func finishInstallStatus(ctx context.Context, db *gorm.DB, name string, adoptedThrough int) error {
	now := time.Now()
	// Generics API 的 Updates 只接受 T（结构体）；状态下载指针字段非空 ⇒ 一并写入
	if _, err := gorm.G[host.Plugin](db).
		Where("name = ?", name).
		Updates(ctx, host.Plugin{Status: host.StatusInstalled, InstalledAt: &now, UpdatedAt: &now}); err != nil {
		return err
	}
	if adoptedThrough <= 0 {
		return nil
	}
	// 单列更新：零值在结构体 Updates 里会被跳过（GORM 的零值语义），用列名 + 值显式写
	_, err := gorm.G[host.Plugin](db).Where("name = ?", name).Update(ctx, "adopted_through", adoptedThrough)
	return err
}

// orderMenus 把菜单/节点按 pid 依赖排序（父先子后）；环或悬空引用即报错（manifest 已校验，这里是兜底）
func orderMenus(menus []manifest.Menu) ([]manifest.Menu, error) {
	remaining := make([]manifest.Menu, len(menus))
	copy(remaining, menus)
	done := make(map[string]bool, len(menus))
	ordered := make([]manifest.Menu, 0, len(menus))

	for len(remaining) > 0 {
		progress := false
		next := make([]manifest.Menu, 0, len(remaining))
		for _, menu := range remaining {
			if menu.Pid != "" && !done[menu.Pid] {
				next = append(next, menu)
				continue
			}
			ordered = append(ordered, menu)
			done[menu.Name] = true
			progress = true
		}
		if !progress {
			names := make([]string, 0, len(next))
			for _, menu := range next {
				names = append(names, menu.Name)
			}
			return nil, fmt.Errorf("菜单/节点的 pid 依赖成环或悬空（%s）：拒装", strings.Join(names, ", "))
		}
		remaining = next
	}
	return ordered, nil
}

// stringPtr 空串也落库为空串（对齐种子 000003 的既有形态，不写 NULL）
func stringPtr(value string) *string {
	return &value
}

// uint8Ptr 小整数指针（admin_rules 的 status/keepalive）
func uint8Ptr(value uint8) *uint8 {
	return &value
}
