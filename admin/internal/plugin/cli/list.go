package cli

import (
	"context"
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/ai-go-hub/ai-go-admin/internal/plugin/host"

	"gorm.io/gorm"
)

// ListItem 台账 / 进程内 / 注册文件的三方对照行
type ListItem struct {
	Name       string
	Version    string
	Kind       string
	Status     string // 台账状态（空 = 台账无行）
	Wired      bool   // 进程内注册表（编译期接线真相）
	Registered bool   // register_gen.go 的注册行（生成物）
	Entities   int    // admin_plugin_entities 行数（L2 面）
	// AdoptedThrough 接管段（台账 adopted_through）：> 0 = 存量库迁移基线（adopt）装入
	// （1..该版本与核心链对应迁移同物；--purge 默认拒绝）
	AdoptedThrough int
	Notes          []string
}

// listReport 三方对照的全部读数（`aigo plugin list` 与 `aigo plugin sync` 共用同一收集管线与差异口径：
// 两侧各写一份「台账怎么读、漂移怎么判」必然漂移——口径只有一处）
type listReport struct {
	// Items 逐插件的对照行（名字全集 = 台账 ∪ 进程内 ∪ 注册文件的出现集）
	Items map[string]*ListItem
	// Rows 台账行（按 name 升序；sync 的重放意愿集由它推导）
	Rows []host.Plugin
	// Registered 注册文件里的名字（升序；sync 的增删差集用它）
	Registered []string
	// Names 出现过的全部名字（升序，渲染顺序）
	Names []string
}

// collectListReport 收集三方读数：台账（DB 期望态）、进程内注册表（编译期接线真相）、注册文件（生成物）
func collectListReport(ctx context.Context, env *Env, db *gorm.DB) (*listReport, error) {
	rows, err := gorm.G[host.Plugin](db).Order("name").Find(ctx)
	if err != nil {
		return nil, fmt.Errorf("读取插件台账失败（插件机制的框架迁移未应用？先执行 `aigo migrate up`）: %w", err)
	}
	entities, err := gorm.G[host.Entity](db).Select("plugin").Find(ctx)
	if err != nil {
		return nil, fmt.Errorf("读取实体元数据失败: %w", err)
	}
	registered, err := RegisteredPlugins(env.Root)
	if err != nil {
		return nil, err
	}
	wired := WiredNames()

	items := map[string]*ListItem{}
	pick := func(name string) *ListItem {
		if item, ok := items[name]; ok {
			return item
		}
		item := &ListItem{Name: name}
		items[name] = item
		return item
	}
	for i := range rows {
		item := pick(rows[i].Name)
		item.Version = rows[i].Version
		item.Kind = rows[i].Kind
		item.Status = rows[i].Status
		item.AdoptedThrough = rows[i].AdoptedThrough
	}
	for i := range entities {
		pick(entities[i].Plugin).Entities++
	}
	for _, name := range wired {
		pick(name).Wired = true
	}
	for _, name := range registered {
		pick(name).Registered = true
	}

	for _, item := range items {
		item.Notes = driftNotes(env, item)
	}

	names := make([]string, 0, len(items))
	for name := range items {
		names = append(names, name)
	}
	slices.Sort(names)
	return &listReport{Items: items, Rows: rows, Registered: registered, Names: names}, nil
}

// Render 打印三方对照表（List 与 sync 共用；不一致显式报告，不做静默对齐）
func (r *listReport) Render(env *Env) {
	env.Printf("插件三方对照（台账 / 进程内注册表 / 注册文件 %s）\n\n", RegistryFile)
	env.Printf("%-24s %-9s %-12s %-10s %-6s %-7s %-5s %-5s %s\n", "名", "版本", "形态", "台账", "进程内", "注册行", "实体", "接管", "差异")
	for _, name := range r.Names {
		item := r.Items[name]
		notes := "—"
		if len(item.Notes) > 0 {
			notes = strings.Join(item.Notes, "；")
		}
		adopted := "—"
		if item.AdoptedThrough > 0 {
			adopted = strconv.Itoa(item.AdoptedThrough)
		}
		env.Printf("%-24s %-9s %-12s %-10s %-6s %-7s %-5d %-5s %s\n",
			item.Name, orDash(item.Version), orDash(item.Kind), orDash(item.Status),
			yesNo(item.Wired), yesNo(item.Registered), item.Entities, adopted, notes)
	}
	if len(r.Names) == 0 {
		env.Printf("（无插件：台账为空、注册文件为空骨架——即「出厂登记态」）\n")
	}
}

// List 三方对照（台账、进程内注册表、注册文件；不一致显式报告，不做静默对齐）
func List(ctx context.Context, env *Env) error {
	db, err := env.EnsureDB(ctx)
	if err != nil {
		return err
	}
	report, err := collectListReport(ctx, env, db)
	if err != nil {
		return err
	}
	report.Render(env)
	return nil
}

// driftNotes 逐项差异（只报「需要对账」的形态，不对齐、不修复）
func driftNotes(env *Env, item *ListItem) []string {
	var notes []string
	if item.Status == "" && item.Wired {
		notes = append(notes, "进程内有、台账无（接线悬空）：装一次或撤注册行")
	}
	if item.Status != "" && !item.Wired && item.Kind != "declarative" {
		notes = append(notes, "待重编译（台账在、进程内无此插件 ⇒ 路由 404）")
	}
	if item.Status != "" && !item.Registered && item.Kind != "declarative" {
		notes = append(notes, "缺注册行（重编译后也不生效）")
	}
	if item.Kind == "declarative" && item.Registered {
		notes = append(notes, "声明式插件不应有注册行（无 Go 包，go build 会失败）")
	}
	if item.Status == host.StatusFailed {
		notes = append(notes, "failed（半装）：清理入口 `aigo plugin uninstall --force "+item.Name+"`")
	}
	if item.Kind == "code" && item.Entities > 0 {
		notes = append(notes, "code 插件不应有 L2 实体行（归属漂移）")
	}
	if item.AdoptedThrough > 0 {
		notes = append(notes, "存量库迁移基线（adopt）装入（1.."+strconv.Itoa(item.AdoptedThrough)+
			" 与核心链同物）：--purge 默认拒绝，放行需 --purge --yes --purge-adopted")
	}
	if item.Wired {
		if _, err := env.PluginSource(item.Name); err != nil {
			notes = append(notes, "注册行的源码目录缺失（"+path.Join(PluginDir(item.Name), "**")+"）：go build 会失败")
		}
	}
	return notes
}

// yesNo 布尔展示
func yesNo(v bool) string {
	if v {
		return "有"
	}
	return "无"
}
