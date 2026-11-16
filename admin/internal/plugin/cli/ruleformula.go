package cli

import (
	"fmt"
	"slices"
	"strings"

	"github.com/ai-go-hub/ai-go-admin/internal/plugin/manifest"
)

// 本文件是「L2 前端面：一个宿主通用页 + 元数据驱动」里**菜单/节点公式的唯一定义处**：
// 安装器按它代写 admin_rules（预演、执行、校验、卸载清理都引用这里，别处不重写字符串模式）。
//
//	插件分组  dir   name=plugin/<名>              path=plugin/<名>                pid=NULL
//	实体页    menu  name=plugin/<名>/<实体>         path=plugin/<名>/<实体>          pid=插件分组
//	                component=/src/views/admin/plugin/render/index.vue（宿主通用页）
//	                open_type=tab / keepalive=0（全部实体共用一个组件，写 1 会静默不缓存，硬决定）
//	权限节点  node  name=plugin/<名>/<实体>/{read,create,update,delete[,sort 仅 drag_sort]}  pid=实体页
//
// 节点名必须与路由 FullPath 经 BuildCheckPath 的推导结果逐字相等（list/get 兼查 /read，F4），
// 长度预算见 manifest.CheckNameEntityLength（len(名)+len(实体) ≤ 49）。
//
// **与设计的对齐口径（F3/F4）**：公式的判定同时存在于两层——manifest 的声明由安装期校验挡下
// （manifest/** 的 checkMenus 增补属其文件所有者，本批登记为跨范围项），这里负责
// 「① 声明与公式不符 ⇒ 拒装；② 公式上缺失的行由安装器补齐 ⇒ 纯声明式插件装后必有菜单入口」。

// EntityRenderComponent L2 实体页的 component（宿主通用渲染页；必须落 views/admin/plugin/** 内，
// 否则菜单查不到组件且无告警，F7）
const EntityRenderComponent = "/src/views/admin/plugin/render/index.vue"

// 公式缺省 weigh（排在宿主菜单之后；manifest 显式声明同名行时以声明为准）
const (
	formulaGroupWeigh      = 100
	formulaEntityMenuWeigh = 100
)

// EntityPageActions 实体页权限节点的动作集（顺序即写入顺序；sort 仅 drag_sort: true）
func EntityPageActions(dragSort bool) []string {
	actions := []string{"read", "create", "update", "delete"}
	if dragSort {
		actions = append(actions, "sort")
	}
	return actions
}

// EntityNodeTitle 节点标题（admin_rules.title 是单语言；与既有种子 000003 的中文口径一致）
func EntityNodeTitle(action string) string {
	switch action {
	case "read":
		return "查看"
	case "create":
		return "新增"
	case "update":
		return "编辑"
	case "delete":
		return "删除"
	case "sort":
		return "排序"
	}
	return action
}

// pluginGroupName 分组目录的规则名（= path）
func pluginGroupName(plugin string) string { return "plugin/" + plugin }

// entityPageName 实体页的规则名（= path；坐标契约：通用页从 route.path 解析 (插件, 实体)）
func entityPageName(plugin, entity string) string { return pluginGroupName(plugin) + "/" + entity }

// entityNodeName 实体页权限节点的规则名
func entityNodeName(plugin, entity, action string) string {
	return entityPageName(plugin, entity) + "/" + action
}

// entityPageRuleNames 实体页公式面的**精确规则名全集**（页 + 动作并集）：
// promote 等「退 L2」动作按它逐名定位待删行，**不用 `name LIKE '页/%'` 前缀匹配**——
// 实体名允许下划线（`_` 在 LIKE 里是单字符通配，会命中错行），前缀匹配还会误伤
// `item`/`items` 这类前缀词实体与页下作者自有的菜单行（后者不属公式面，应保留）。
// 动作取全量并集（含 sort）：drag_sort=false 时 sort 节点在安装期就被公式校验拒，不可能存在。
func entityPageRuleNames(plugin, entity string) []string {
	actions := EntityPageActions(true)
	names := make([]string, 0, 1+len(actions))
	names = append(names, entityPageName(plugin, entity))
	for _, action := range actions {
		names = append(names, entityNodeName(plugin, entity, action))
	}
	return names
}

// entityMenuTitle 实体页菜单标题（admin_rules.title 单语言 ⇒ 取中文标题，缺省回退英文）
func entityMenuTitle(entity manifest.Entity) string {
	if title := strings.TrimSpace(entity.TitleZh); title != "" {
		return title
	}
	if title := strings.TrimSpace(entity.TitleEn); title != "" {
		return title
	}
	return entity.Entity
}

// entityOwnedByEngine ddl_owner 缺省即 engine：引擎管该实体的建表/加列/加索引与 --purge 的 DROP
func entityOwnedByEngine(entity manifest.Entity) bool {
	return strings.TrimSpace(entity.DdlOwner) == "" || entity.DdlOwner == "engine"
}

// ddlOwnerOf 实体的 ddl_owner 归一键（缺省补 engine；展示与审计用）
func ddlOwnerOf(entity manifest.Entity) string {
	if !entityOwnedByEngine(entity) {
		return entity.DdlOwner
	}
	return "engine"
}

// migrationsOwnedNote ddl_owner=migrations 的展示文案（引擎只读元数据做渲染与 CRUD）
const migrationsOwnedNote = "ddl_owner=migrations：引擎不做任何 ALTER/DROP（表结构与删除全由插件迁移承担）"

// checkEntityRuleFormulas公式校验（安装期硬校验：不符一律拒装）。
//
// 校验面（与公式一一对应）：
//   - 分组行（name == plugin/<名>）：type 必须是 dir；path 只能为 plugin/<名> 或留空；
//   - 实体页行（name == plugin/<名>/<实体>）：type=menu、path=同名、component=宿主通用页、pid=分组；
//   - 实体页下的 node 行（type=node）：动作必须在闭集内（sort 仅 drag_sort）、pid=实体页。
//
// 页下自有的 menu（如「详情」子页）不属节点公式，不在校验面内（作者自己的页面照常允许）。
func checkEntityRuleFormulas(name string, menus []manifest.Menu, entities []manifest.Entity) error {
	group := pluginGroupName(name)
	pages := make(map[string]struct{}, len(entities))
	for _, entity := range entities {
		pages[entityPageName(name, entity.Entity)] = struct{}{}
	}

	for i, menu := range menus {
		if menu.Name == group {
			if menu.Type != "dir" {
				return fmt.Errorf("menus[%d]（%s）是分组公式行：type 必须是 dir，实际 %s", i, menu.Name, menu.Type)
			}
			if path := strings.TrimSpace(menu.Path); path != "" && path != group {
				return fmt.Errorf("menus[%d]（%s）是分组公式行：path 只能为 %q（或留空），实际 %q",
					i, menu.Name, group, menu.Path)
			}
			continue
		}

		if _, isPage := pages[menu.Name]; isPage {
			if menu.Type != "menu" {
				return fmt.Errorf("menus[%d]（%s）是实体页公式行：type 必须是 menu，实际 %s", i, menu.Name, menu.Type)
			}
			if path := strings.TrimSpace(menu.Path); path != menu.Name {
				return fmt.Errorf("menus[%d]（%s）是实体页公式行：path 必须逐字等于 %q，实际 %q（坐标契约：通用页从 route.path 解析插件与实体）",
					i, menu.Name, menu.Name, menu.Path)
			}
			if component := strings.TrimSpace(menu.Component); component != EntityRenderComponent {
				return fmt.Errorf("menus[%d]（%s）是实体页公式行：component 必须逐字等于宿主通用页 %q，实际 %q（全部 L2 实体共用该组件）",
					i, menu.Name, EntityRenderComponent, menu.Component)
			}
			if pid := strings.TrimSpace(menu.Pid); pid != group {
				return fmt.Errorf("menus[%d]（%s）是实体页公式行：pid 必须是插件分组 %q，实际 %q",
					i, menu.Name, group, menu.Pid)
			}
			continue
		}

		for _, entity := range entities {
			page := entityPageName(name, entity.Entity)
			if !strings.HasPrefix(menu.Name, page+"/") || menu.Type != "node" {
				continue
			}
			action := strings.TrimPrefix(menu.Name, page+"/")
			if !slices.Contains(EntityPageActions(entity.DragSort), action) {
				return fmt.Errorf("menus[%d]（%s）是实体页下的权限节点：动作 %q 不在公式闭集内（%s；sort 仅当 drag_sort: true）",
					i, menu.Name, action, strings.Join(EntityPageActions(entity.DragSort), "/"))
			}
			if pid := strings.TrimSpace(menu.Pid); pid != page {
				return fmt.Errorf("menus[%d]（%s）是实体页下的权限节点：pid 必须是实体页 %q，实际 %q",
					i, menu.Name, page, menu.Pid)
			}
		}
	}
	return nil
}

// BuildInstallMenus 装配安装器要代写的菜单/节点全集（公式代写；F2/F3/F4）：
//
//   - 先过公式校验（declared 的行与公式不符 ⇒ 拒装）；
//   - 实体页与权限节点**按公式补齐**（manifest 已声明同名行时以声明为准：标题/图标/权重由作者定，
//     但 path/component/pid/节点动作必须逐字合乎公式）；
//   - 分组目录行缺失时自动补（纯声明式插件没有 menus 段 ⇒ 据此获得菜单入口，验收 B「装后菜单入口」）；
//   - 返回清单按 name 唯一（写入侧 writeInstallRules 按 pid 拓扑排序；同名重复会被 DB 侧撞车检查之外
//     的「同名规则零容忍」断言抓到，此处的去重即写入面无重复的第一道保证）。
func BuildInstallMenus(name string, m *manifest.Manifest) ([]manifest.Menu, error) {
	if m == nil {
		return nil, fmt.Errorf("插件 %s 的 manifest 为空", name)
	}
	if err := checkEntityRuleFormulas(name, m.Menus, m.Entities); err != nil {
		return nil, fmt.Errorf("插件 %s 的 menus 与公式不符：%w", name, err)
	}

	group := pluginGroupName(name)
	menus := make([]manifest.Menu, 0, len(m.Menus)+len(m.Entities)*6)
	seen := make(map[string]struct{}, len(m.Menus)+len(m.Entities)*6)
	for _, menu := range m.Menus {
		if menu.Name == "" {
			return nil, fmt.Errorf("插件 %s 的 menus 里有空 name 的声明", name)
		}
		if _, dup := seen[menu.Name]; dup {
			return nil, fmt.Errorf("插件 %s 的 menus 重复声明了 %q（同名规则会静默替换宿主页面）", name, menu.Name)
		}
		seen[menu.Name] = struct{}{}
		menus = append(menus, menu)
	}

	if len(m.Entities) > 0 {
		if _, ok := seen[group]; !ok {
			title := strings.TrimSpace(m.Title)
			if title == "" {
				title = name
			}
			menus = append(menus, manifest.Menu{
				Title: title, Type: "dir", Name: group, Path: group,
				Weigh: formulaGroupWeigh,
			})
			seen[group] = struct{}{}
		}
	}

	for _, entity := range m.Entities {
		page := entityPageName(name, entity.Entity)
		if _, ok := seen[page]; !ok {
			menus = append(menus, manifest.Menu{
				Title: entityMenuTitle(entity), Type: "menu", Name: page, Path: page,
				Component: EntityRenderComponent, Pid: group, Weigh: formulaEntityMenuWeigh,
			})
			seen[page] = struct{}{}
		}
		for _, action := range EntityPageActions(entity.DragSort) {
			node := entityNodeName(name, entity.Entity, action)
			if _, ok := seen[node]; ok {
				continue
			}
			menus = append(menus, manifest.Menu{
				Title: EntityNodeTitle(action), Type: "node", Name: node, Pid: page,
			})
			seen[node] = struct{}{}
		}
	}
	return menus, nil
}

// formulaMenuNames 该清单里由公式代写的规则名（渲染预演时标注用；判定口径 = 「不在 manifest 声明内」）
func formulaMenuNames(m *manifest.Manifest, menus []manifest.Menu) map[string]struct{} {
	declared := make(map[string]struct{}, len(m.Menus))
	for _, menu := range m.Menus {
		declared[menu.Name] = struct{}{}
	}
	auto := make(map[string]struct{}, len(menus))
	for _, menu := range menus {
		if _, ok := declared[menu.Name]; !ok {
			auto[menu.Name] = struct{}{}
		}
	}
	return auto
}
