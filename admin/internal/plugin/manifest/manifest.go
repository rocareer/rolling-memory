// Package manifest 解析与校验插件的 plugin.yaml（设计说明键表）。
//
// 定位：manifest 是**两层（L1 源码插件 / L2 声明式插件）共用的唯一元数据事实源**，
// 键集闭集——未知键一律拒装；校验分两级：
//   - 本包（安装准入）：结构闭集（未知键）、name 正则与宿主保留字、host_api 版本域（对 host.APIVersion）、
//     menus[].name 的 plugin/<名>/ 前缀、名+实体长度预算（varchar(64) 与 PG 标识符 63 字节取更紧者）；
//   - 字段级值域闭集（type 9 / form 8 及其控件映射 / 搜索操作符 16 项词表 / dict 形状）也在本包
//     （fields.go 的 ValidateField——**唯一定义处**）：要求「词表外的写法一律拒装」，
//     而宿主侧遇到未知操作符返回空串、条件被静默丢弃，不能靠它兜底；
//     L2 引擎的 spec 校验器（P3，经 host.SetSpecValidator 同时服务安装期与启动预检）应复用本包的集合与函数。
//
// 本包不读库、不读配置：长度预算所需的表前缀由调用方（CLI / 安装器）经 Options 传入，纯函数可单测。
package manifest

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ai-go-hub/ai-go-admin/internal/plugin/host"

	"go.yaml.in/yaml/v3"
)

// FileName manifest 的固定文件名（插件目录根）
const FileName = "plugin.yaml"

// 插件名闭集：= plugins/ 下目录名 = admin_plugins.name = 路由段 plugin/<名>
var namePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{1,31}$`)

// 用户变量名（configs 段）：与既有 configs.name 的 snake_case 口径一致，varchar(64)
var configKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

// 插件版本 X.Y.Z
var versionPattern = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

// ReservedNames 宿主保留字（清单 + read）：插件名不得命中——它们被宿主管理面与 L2 通用页占用
// （render = L2 通用渲染页；list/get/enable/disable/uninstall = 管理面动作（uninstall 为详情抽屉的卸载
// 端点，2026-10-23 随管理页功能补入），也是「插件名不可能与
// plugin.<保留字>.* 语言包子树相撞」的结构依据——宿主管理面语言包落在保留字命名空间下）。
//
// read 的纳入（清单的补强，理由在下）：「宿主管理面节点名」在 admin_rules 里是
// plugin/read（框架数据库迁移的 3 个种子之一，中间件由 FullPath 推导、list/get 回落 read），
// 而插件公式声明的分组 dir 规则名恰为 plugin/<名> ⇒ 名为 read 的插件会声明出与宿主节点
// **逐字相等**的 admin_rules.name（规则页手工建同名 dir 规则更绕不过安装期查重），
// 使「插件名不得为宿主保留字」这道结构性防撞在 read 上失效、只剩安装期 DB 查重兜底（第二道）。
// enable/disable 同理（管理面动作名）；uninstall 亦同（占用 `/admin/plugin/uninstall` 路由段）。
//
// source/catalog 的纳入（来源与清单的「保留字联动」规则）：
// 本批新增的宿主页面与接口占用了 `plugin/source`、`plugin/catalog` 两个路由段
// （`/admin/plugin/source/...`、`/admin/plugin/catalog/...` 与 `web/src/views/admin/plugin/{source,catalog}/`）；
// 名为 source/catalog 的插件会声明出与之相撞的分组规则/页面路径。本清单是**唯一正本**：
// 静态门禁 `校验脚本` 的 RESERVED_NAMES 与 `plugins/README.md`
// 的保留字说明必须与之逐字同步（`internal/plugin/source` 有同口径的漂移哨兵测试），三处缺一即不一致。
// 新增宿主管理面节点时应同步复核本清单。
var ReservedNames = []string{"render", "list", "get", "read", "enable", "disable", "source", "catalog", "uninstall"}

// Kinds 插件形态闭集
var Kinds = []string{"code", "declarative", "mixed"}

// MenuTypes 菜单/节点类型闭集（对齐 admin_rules.type）
var MenuTypes = []string{"dir", "menu", "node"}

// DdlOwners 实体 DDL 归属闭集（engine=引擎管结构；migrations=mixed 插件用迁移文件管）
var DdlOwners = []string{"engine", "migrations"}

// AdoptProbeKinds 接管探针（adopt）的对象闭集（存量库迁移基线正本：
// 设计说明）：
// table=表存在；column=表上的列；index=索引；constraint=表上的约束；none=显式免探（纯菜单/文案/回填版本）。
var AdoptProbeKinds = []string{"table", "column", "index", "constraint", "none"}

// Manifest plugin.yaml 的完整键表（闭集，未知键拒装）
type Manifest struct {
	// Name 插件名（正则 + 保留字 + 目录名一致 + 台账 = 路由段）
	Name string `yaml:"name" json:"name"`
	// Title 展示名（单语言；菜单标题另由 admin_rules.title 控制）
	Title string `yaml:"title" json:"title"`
	// Version 插件版本 X.Y.Z
	Version string `yaml:"version" json:"version"`
	// Kind 形态：code | declarative | mixed
	Kind string `yaml:"kind" json:"kind"`
	// HostAPI 机制接口版本域（如 ">=1 <2"），安装时对 host.APIVersion 硬校验
	HostAPI string `yaml:"host_api" json:"host_api"`
	// FrameworkReq 框架线版本域（对 version.Get().Version）：只提示不拒装
	FrameworkReq string `yaml:"framework_req,omitempty" json:"framework_req,omitempty"`
	// Requires 依赖插件列表（name@range，逗号分隔；v1 只校验「已装且版本相容」）
	Requires string `yaml:"requires,omitempty" json:"requires,omitempty"`
	// Menus 菜单/节点声明（安装器代写 admin_rules 并打归属列；插件 SQL 不得直写）
	Menus []Menu `yaml:"menus,omitempty" json:"menus,omitempty"`
	// Entities L2 实体段（闭集）；kind: code 的插件留空
	Entities []Entity `yaml:"entities,omitempty" json:"entities,omitempty"`
	// MigrationsDir 插件迁移目录（默认 migrations/）
	MigrationsDir string `yaml:"migrations_dir,omitempty" json:"migrations_dir,omitempty"`
	// Adopt 接管段（存量库迁移基线：1..through 与宿主核心链对应迁移「同物」，安装期只写版本表、不执行旧 DDL）
	Adopt *Adopt `yaml:"adopt,omitempty" json:"adopt,omitempty"`
	// Web 前端落位声明（源是唯一事实源，落位副本是产物）
	Web *Web `yaml:"web,omitempty" json:"web,omitempty"`
	// Configs 自带配置键（安装器写入 configs 的 plugin.<名> 分组）
	Configs []ConfigItem `yaml:"configs,omitempty" json:"configs,omitempty"`
	// Signature 分离签名（签包内清单 files.sha256）的相对路径，默认 plugin.yaml.sig
	Signature string `yaml:"signature,omitempty" json:"signature,omitempty"`
}

// Adopt 接管段（manifest 的 adopt 键；设计说明）：
// 声明「插件的 1..through 号迁移与宿主核心链的对应迁移是同物」——存量库里这些对象已由核心链建过，
// 安装器据此做三态判定（全命中 ⇒ 只把版本表推进到 through、不执行旧 DDL；全不命中 ⇒ 全量；部分命中 ⇒ 拒装）。
//
// 本段只在**插件版本表不存在或无版本行**等「接管域内」的现场生效；版本表已推进到 through 之后，
// 本条不改变任何行为（增量照旧）。校验只做**结构闭集**（kind / 标识符 / version 域 / 逐版本覆盖 /
// 至少一条对象探针）；与迁移文件的交叉对账（through = 最大编号、1..through 连续、对象名必须出现在
// 该版本 up 文件里）在安装期做（cli.verifyAdopt——本包不读迁移文件内容）。
type Adopt struct {
	// Through 接管边界（插件迁移编号上界；1..through 与核心链对应迁移同物）
	Through int `yaml:"through" json:"through"`
	// Probes 逐版本存在性探针（每个版本 1..through 都要显式覆盖：对象探针或 kind: none）
	Probes []AdoptProbe `yaml:"probes" json:"probes"`
}

// AdoptProbe 一条接管探针（只断言**存在性**：不校验类型/约束/行数——类型级差异由验收的 schema diff 兜底）。
// 对象名一律写**不含前缀**的逻辑名（安装器用表前缀拼物理名，与 L2 实体表同口径）。
type AdoptProbe struct {
	// Version 插件迁移编号（∈ [1, through]；探针据此逐条回溯到具体 up 文件）
	Version int `yaml:"version" json:"version"`
	// Kind table | column | index | constraint | none
	Kind string `yaml:"kind" json:"kind"`
	// Table 逻辑表名（kind: column/constraint 必填；table/index 不得带——对象名本身即待查对象）
	Table string `yaml:"table,omitempty" json:"table,omitempty"`
	// Name 逻辑对象名（kind: none 不得带）
	Name string `yaml:"name,omitempty" json:"name,omitempty"`
}

// Menu 菜单/节点声明（由安装器代写进 admin_rules 并打 plugin 归属列）
type Menu struct {
	// Title 菜单标题（admin_rules.title，单语言）
	Title string `yaml:"title" json:"title"`
	// Type dir | menu | node
	Type string `yaml:"type" json:"type"`
	// Name 规则名：必须以 plugin/<插件名>/ 开头（结构性防撞宿主路由名）
	Name string `yaml:"name" json:"name"`
	// Path 前端路径（相对后台入口；仅 dir/menu 有意义）
	Path string `yaml:"path,omitempty" json:"path,omitempty"`
	// Icon lucide-* 图标名
	Icon string `yaml:"icon,omitempty" json:"icon,omitempty"`
	// Component 前端组件路径（精确写 /src/views/admin/plugin/<名>/index.vue）
	Component string `yaml:"component,omitempty" json:"component,omitempty"`
	// Pid 父菜单的 name（空 = 根；安装器解析为 admin_rules.pid 的 ID）
	Pid string `yaml:"pid,omitempty" json:"pid,omitempty"`
	// Weigh 排序权重（默认排在宿主菜单之后）
	Weigh int `yaml:"weigh,omitempty" json:"weigh,omitempty"`
	// Status 初始状态（安装期安装器统一写 0 隐藏，enable 时置 1；此处仅声明期望初始值）
	Status int `yaml:"status,omitempty" json:"status,omitempty"`
}

// Entity L2 实体声明（实体级键集；安装器把整段序列化为 admin_plugin_entities.spec）
type Entity struct {
	// Entity 实体名（闭集标识符；参与物理表名与路由段）
	Entity string `yaml:"entity" json:"entity"`
	// TitleZh / TitleEn 实体双语标题（文案绕开构建期语言包，随响应下发）
	TitleZh string `yaml:"title_zh" json:"title_zh"`
	TitleEn string `yaml:"title_en" json:"title_en"`
	// DragSort 拖动排序开关（true ⇒ 引擎自动附加 weigh 列并注册 /sort）
	DragSort bool `yaml:"drag_sort,omitempty" json:"drag_sort,omitempty"`
	// DdlOwner engine | migrations（默认 engine；migrations ⇒ 引擎只读元数据，不做任何 ALTER/DROP）
	DdlOwner string `yaml:"ddl_owner,omitempty" json:"ddl_owner,omitempty"`
	// Fields 字段声明（值域闭集见 fields.go：type/form/控件映射/搜索操作符/dict）
	Fields []Field `yaml:"fields" json:"fields"`
}

// Field 字段声明（字段级键集；值域闭集与控件映射由 ValidateField 判定，见 fields.go）
type Field struct {
	Name        string  `yaml:"name" json:"name"`
	Type        string  `yaml:"type" json:"type"`
	TitleZh     string  `yaml:"title_zh" json:"title_zh"`
	TitleEn     string  `yaml:"title_en" json:"title_en"`
	Required    bool    `yaml:"required,omitempty" json:"required,omitempty"`
	Length      int     `yaml:"length,omitempty" json:"length,omitempty"`
	Default     any     `yaml:"default,omitempty" json:"default,omitempty"`
	Unique      bool    `yaml:"unique,omitempty" json:"unique,omitempty"`
	Index       bool    `yaml:"index,omitempty" json:"index,omitempty"`
	List        bool    `yaml:"list,omitempty" json:"list,omitempty"`
	Sort        bool    `yaml:"sort,omitempty" json:"sort,omitempty"`
	Search      *Search `yaml:"search,omitempty" json:"search,omitempty"`
	QuickSearch bool    `yaml:"quickSearch,omitempty" json:"quickSearch,omitempty"`
	Form        string  `yaml:"form,omitempty" json:"form,omitempty"`
	Dict        []Dict  `yaml:"dict,omitempty" json:"dict,omitempty"`
	Trgm        bool    `yaml:"trgm,omitempty" json:"trgm,omitempty"`
}

// Search 字段的搜索声明（op 闭集 = 宿主 16 项词表原文，见 fields.go 的 SearchOperators 与 ValidateField）
type Search struct {
	Op string `yaml:"op" json:"op"`
}

// Dict 枚举字典项（双语）
type Dict struct {
	Value   any    `yaml:"value" json:"value"`
	LabelZh string `yaml:"label_zh" json:"label_zh"`
	LabelEn string `yaml:"label_en" json:"label_en"`
}

// Web 前端落位声明（plugins/<名>/web/** 是源，落位副本是产物）
type Web struct {
	// Views 视图源路径列表（目录或文件，插件目录内相对路径）→ web/src/views/admin/plugin/<名>/
	Views []string `yaml:"views,omitempty" json:"views,omitempty"`
	// Lang 语言包源路径列表（如 web/lang/zh-cn.yaml）→ web/src/lang/<lang>/plugin/<名>.yaml
	Lang []string `yaml:"lang,omitempty" json:"lang,omitempty"`
}

// ConfigItem 自带配置键（形状对齐 configs 表列；分组由安装器写 plugin.<名>）
type ConfigItem struct {
	Name  string `yaml:"name" json:"name"`
	Title string `yaml:"title" json:"title"`
	Type  string `yaml:"type,omitempty" json:"type,omitempty"`
	Tip   string `yaml:"tip,omitempty" json:"tip,omitempty"`
	Value string `yaml:"value,omitempty" json:"value,omitempty"`
	Dict  string `yaml:"dict,omitempty" json:"dict,omitempty"`
	Rule  string `yaml:"rule,omitempty" json:"rule,omitempty"`
	Weigh int    `yaml:"weigh,omitempty" json:"weigh,omitempty"`
}

// Options 校验上下文（由调用方注入，保持本包无全局依赖）
type Options struct {
	// PluginDir 插件目录（非空时校验：manifest 所在目录名 == name；web / migrations_dir 声明的路径存在）
	PluginDir string
	// Prefix 数据库表前缀（长度预算取 varchar(64) 与 PG 标识符 63 字节两者更紧者）
	Prefix string
	// HostVersion 宿主版本（version.Get().Version；仅用于 framework_req 的提示，不参与拒装）
	HostVersion string
	// IgnoreDirName 跳过「目录名 == name」这一条（**只给包源用**，其余校验照旧）：
	// 该不变式属**仓库树**（plugins/<名>/ = name），而包源（zip）解包出的临时目录名
	// 与插件名无关（包根目录不承载名字）。安装器落位时按 manifest.name 建 plugins/<名>/，
	// 不变式由构造保证；目录源、verify、export、migrate-force 一律不置本开关，仍强制该校验。
	IgnoreDirName bool
}

// Parse 解析并校验 manifest 字节（闭集解码：未知键一律拒绝）
func Parse(data []byte, opts Options) (*Manifest, []string, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true) // 键集闭集：未知键（含嵌套段与字段级）一律拒装，

	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return nil, nil, fmt.Errorf("解析 %s 失败（键集闭集，未知键拒装）: %w", FileName, err)
	}

	warnings, err := Validate(&m, opts)
	if err != nil {
		return nil, nil, err
	}
	return &m, warnings, nil
}

// Load 读取插件目录下的 plugin.yaml 并校验（dir 为插件目录）
func Load(dir string, opts Options) (*Manifest, []string, error) {
	dir = strings.TrimSuffix(filepath.ToSlash(dir), "/")
	if opts.PluginDir == "" {
		opts.PluginDir = dir
	}

	data, err := os.ReadFile(path.Join(dir, FileName))
	if err != nil {
		return nil, nil, fmt.Errorf("读取 %s 失败: %w", path.Join(dir, FileName), err)
	}
	return Parse(data, opts)
}

// Validate 校验 manifest（结构 + 命名 + 兼容域 + 长度预算）。
// 返回的 warnings 为非致命提示（framework_req 不相容等），error 为拒装理由。
func Validate(m *Manifest, opts Options) ([]string, error) {
	if m == nil {
		return nil, errors.New("manifest 为空")
	}

	// ① 必填项（标 ✓ 的四项 + host_api）
	if err := checkName(m.Name); err != nil {
		return nil, err
	}
	if strings.TrimSpace(m.Title) == "" {
		return nil, errors.New("title 不能为空（展示名）")
	}
	if !versionPattern.MatchString(m.Version) {
		return nil, fmt.Errorf("version %q 不是 X.Y.Z 形式", m.Version)
	}
	if !contains(Kinds, m.Kind) {
		return nil, fmt.Errorf("kind %q 不在闭集内（%s）", m.Kind, strings.Join(Kinds, "|"))
	}

	// ② host_api：硬校验的兼容域（对宿主机制接口版本常量）
	if strings.TrimSpace(m.HostAPI) == "" {
		return nil, errors.New("host_api 不能为空（机制接口版本域，如 \">=1 <2\"）")
	}
	if err := host.Compatible(m.HostAPI); err != nil {
		return nil, err
	}

	// ③ framework_req：只提示不拒装（VERSION 属「各仓保留」，下游项目是产品版本线）
	warnings, err := frameworkWarnings(m, opts)
	if err != nil {
		return nil, err
	}

	// ④ requires：name@range 逗号分隔
	if err := checkRequires(m.Requires); err != nil {
		return nil, err
	}

	// ⑤ menus：类型闭集 + name 前缀 plugin/<名>/ + 名长（admin_rules.name varchar(64)）
	if err := checkMenus(m); err != nil {
		return nil, err
	}

	// ⑥ entities：实体名闭集 + 唯一 + 长度预算；kind: code 不留实体段
	if err := checkEntities(m, opts); err != nil {
		return nil, err
	}

	// ⑥b adopt：接管段（存量库迁移基线）的结构闭集；与迁移文件的交叉对账在安装期（cli.verifyAdopt）
	if err := checkAdopt(m); err != nil {
		return nil, err
	}

	// ⑦ web / configs / migrations_dir / signature
	if err := checkWeb(m, opts); err != nil {
		return nil, err
	}
	if err := checkConfigs(m); err != nil {
		return nil, err
	}
	if err := checkMigrationsDir(m, opts); err != nil {
		return nil, err
	}
	if err := checkSignature(m); err != nil {
		return nil, err
	}

	// ⑧ 目录名 == name（门禁同口径；安装前先拦。包源解包出的临时目录名无关 ⇒ IgnoreDirName 跳过）
	if opts.PluginDir != "" && !opts.IgnoreDirName {
		base := path.Base(strings.TrimSuffix(filepath.ToSlash(opts.PluginDir), "/"))
		if base != m.Name {
			return nil, fmt.Errorf("插件目录名 %q 与 manifest 的 name %q 不一致（= 目录名 = 台账 = 路由段）", base, m.Name)
		}
	}

	return warnings, nil
}

// checkName 插件名闭集与宿主保留字（保留字由 name 校验与门禁双拦）
func checkName(name string) error {
	if !namePattern.MatchString(name) {
		return fmt.Errorf("name %q 不合法（^[a-z][a-z0-9-]{1,31}$，2–32 字符）", name)
	}
	if contains(ReservedNames, name) {
		return fmt.Errorf("name %q 命中宿主保留字（%s）：保留字留给宿主管理面与 L2 通用页", name, strings.Join(ReservedNames, "/"))
	}
	return nil
}

// frameworkWarnings framework_req 的提示（不拒装）
func frameworkWarnings(m *Manifest, opts Options) ([]string, error) {
	if strings.TrimSpace(m.FrameworkReq) == "" {
		return nil, nil
	}
	if opts.HostVersion == "" {
		return nil, nil
	}
	ok, err := host.InRange(m.FrameworkReq, opts.HostVersion)
	if err != nil {
		return nil, fmt.Errorf("framework_req 域 %q 无法解析: %w", m.FrameworkReq, err)
	}
	if !ok {
		return []string{fmt.Sprintf("framework_req %s 不含当前宿主版本 %s（仅提示，不拒装；VERSION 属「各仓保留」，下游项目为产品版本线）", m.FrameworkReq, opts.HostVersion)}, nil
	}
	return nil, nil
}

// checkRequires 依赖声明校验（格式层；「已装且版本相容」在安装期查台账）
func checkRequires(requires string) error {
	for _, item := range splitList(requires) {
		name, domain, found := strings.Cut(item, "@")
		if !found || strings.TrimSpace(domain) == "" {
			return fmt.Errorf("requires 项 %q 不是 name@range 形式", item)
		}
		if err := checkName(strings.TrimSpace(name)); err != nil {
			return fmt.Errorf("requires 项 %q: %w", item, err)
		}
		// 版本域语法复用 host 的解析：用任意版本探解析是否成立（域合法性判定）
		if _, err := host.InRange(strings.TrimSpace(domain), "0.0.0"); err != nil {
			return fmt.Errorf("requires 项 %q 的版本域无法解析: %w", item, err)
		}
	}
	return nil
}

// checkMenus 菜单/节点声明校验
func checkMenus(m *Manifest) error {
	prefix := "plugin/" + m.Name + "/"
	group := "plugin/" + m.Name
	seen := make(map[string]struct{}, len(m.Menus))
	for i, menu := range m.Menus {
		if !contains(MenuTypes, menu.Type) {
			return fmt.Errorf("menus[%d].type %q 不在闭集内（%s）", i, menu.Type, strings.Join(MenuTypes, "|"))
		}
		if strings.TrimSpace(menu.Name) == "" {
			return fmt.Errorf("menus[%d].name 不能为空", i)
		}
		// 结构性防撞宿主路由名：同名 vue-router 路由是「删旧加新」，会静默替换宿主页面。
		// 命名形态对齐两条公式：分组 dir = plugin/<名>；菜单/节点 = plugin/<名>/…
		collisionSafe := strings.HasPrefix(menu.Name, prefix) || (menu.Type == "dir" && menu.Name == group)
		if !collisionSafe {
			return fmt.Errorf("menus[%d].name %q 必须以 %q 开头（分组 dir 例外：可恰为 %q）", i, menu.Name, prefix, group)
		}
		if len(menu.Name) > 64 { // admin_rules.name varchar(64)
			return fmt.Errorf("menus[%d].name %q 长度 %d 超过 admin_rules.name 的 64 上限", i, menu.Name, len(menu.Name))
		}
		if _, dup := seen[menu.Name]; dup {
			return fmt.Errorf("menus[%d].name %q 重复声明", i, menu.Name)
		}
		seen[menu.Name] = struct{}{}
		if menu.Type == "menu" && strings.TrimSpace(menu.Component) == "" {
			return fmt.Errorf("menus[%d]（%s）是菜单，必须声明 component（前端按精确路径查表，落 web/src/views/admin/plugin/** 内）", i, menu.Name)
		}
	}
	// pid 指向的父菜单必须在同一清单内（或留空表示根）
	for i, menu := range m.Menus {
		if menu.Pid == "" {
			continue
		}
		if _, ok := seen[menu.Pid]; !ok {
			return fmt.Errorf("menus[%d]（%s）的 pid %q 不在本清单的 menus[].name 内", i, menu.Name, menu.Pid)
		}
	}
	return nil
}

// checkEntities 实体声明校验（实体名闭集 + 唯一 + 长度预算 + 字段值域闭集见 fields.go）
func checkEntities(m *Manifest, opts Options) error {
	if m.Kind == "code" && len(m.Entities) > 0 {
		return errors.New("kind: code 的插件不得声明 entities（L2 实体段只属 declarative / mixed）")
	}
	seen := make(map[string]struct{}, len(m.Entities))
	for i, entity := range m.Entities {
		if !identifierPattern.MatchString(entity.Entity) {
			return fmt.Errorf("entities[%d].entity %q 不是合法标识符（^[a-z][a-z0-9_]{0,62}$）", i, entity.Entity)
		}
		if _, dup := seen[entity.Entity]; dup {
			return fmt.Errorf("entities[%d].entity %q 重复声明", i, entity.Entity)
		}
		seen[entity.Entity] = struct{}{}
		if strings.TrimSpace(entity.TitleZh) == "" || strings.TrimSpace(entity.TitleEn) == "" {
			return fmt.Errorf("entities[%d]（%s）必须声明 title_zh 与 title_en（L2 文案走后端双语元数据）", i, entity.Entity)
		}
		if entity.DdlOwner != "" && !contains(DdlOwners, entity.DdlOwner) {
			return fmt.Errorf("entities[%d].ddl_owner %q 不在闭集内（%s）", i, entity.DdlOwner, strings.Join(DdlOwners, "|"))
		}
		if len(entity.Fields) == 0 {
			return fmt.Errorf("entities[%d]（%s）的 fields 为空（至少声明一个字段）", i, entity.Entity)
		}
		fieldSeen := make(map[string]struct{}, len(entity.Fields))
		for j, field := range entity.Fields {
			if !identifierPattern.MatchString(field.Name) {
				return fmt.Errorf("entities[%d].fields[%d] 的 name %q 不是合法标识符（^[a-z][a-z0-9_]{0,62}$）", i, j, field.Name)
			}
			if _, dup := fieldSeen[field.Name]; dup {
				return fmt.Errorf("entities[%d].fields[%d] 的 name %q 重复声明", i, j, field.Name)
			}
			fieldSeen[field.Name] = struct{}{}
			// 保留列名（引擎自管）：id / created_at / updated_at / deleted_at / weigh（drag_sort 时）
			if reservedColumn(field.Name, entity.DragSort) {
				return fmt.Errorf("entities[%d].fields[%d] 的 name %q 命中引擎保留列名", i, j, field.Name)
			}
			// 值域闭集（type/form 及其映射、搜索操作符词表、dict 形状；fields.go）
			if err := ValidateField(field); err != nil {
				return fmt.Errorf("entities[%d].fields[%d]（%s）: %w", i, j, field.Name, err)
			}
		}
		// 长度预算（引擎索引名 PG 标识符 63 字节 /admin_rules.name varchar(64) 取更紧者；
		// ddl_owner=migrations 的表引擎不发索引名 ⇒ 只剩节点名约束，见 budget.go）
		if err := CheckNameEntityLength(m.Name, entity.Entity, opts.Prefix, entity.DdlOwner); err != nil {
			return fmt.Errorf("entities[%d]: %w", i, err)
		}
	}
	return nil
}

// checkAdopt 接管段（adopt）的结构闭集校验（设计说明）：
//   - through ≥ 1（接管边界 = 插件迁移编号上界）；
//   - 至少一条**对象探针**（全是 kind: none 的 adopt 段没有判定力 ⇒ 拒装）；
//   - probes 覆盖 1..through 的**每一个**版本（对象探针或显式 kind: none）——漏一条会让存量库被判成
//     干净库、全量重放旧迁移（防漏方向：宁可在装前拒）；
//   - kind 闭集；对象名/表名是不含前缀的逻辑名且过标识符闭集；
//   - column/constraint 必须带 table；table/index 不得带 table；none 两个都不带；
//   - 同一 (version, kind, table, name) 不得重复声明。
func checkAdopt(m *Manifest) error {
	if m.Adopt == nil {
		return nil
	}
	if m.Adopt.Through < 1 {
		return fmt.Errorf("adopt.through %d 非法（接管边界 = 插件迁移编号上界，必须 ≥ 1）", m.Adopt.Through)
	}
	seen := make(map[string]struct{}, len(m.Adopt.Probes))
	covered := make(map[int]bool, m.Adopt.Through)
	objects := 0
	for i, probe := range m.Adopt.Probes {
		if !contains(AdoptProbeKinds, probe.Kind) {
			return fmt.Errorf("adopt.probes[%d].kind %q 不在闭集内（%s）", i, probe.Kind, strings.Join(AdoptProbeKinds, "|"))
		}
		if probe.Version < 1 || probe.Version > m.Adopt.Through {
			return fmt.Errorf("adopt.probes[%d].version %d 不在 1..through(%d) 内", i, probe.Version, m.Adopt.Through)
		}
		covered[probe.Version] = true

		if probe.Kind == "none" {
			if probe.Table != "" || probe.Name != "" {
				return fmt.Errorf("adopt.probes[%d]（version %d）是 kind: none（显式免探），不得带 table/name", i, probe.Version)
			}
			continue
		}
		objects++
		if !identifierPattern.MatchString(probe.Name) {
			return fmt.Errorf("adopt.probes[%d].name %q 不是合法标识符（^[a-z][a-z0-9_]{0,62}$）：写**不含表前缀**的逻辑名，前缀由安装器拼",
				i, probe.Name)
		}
		switch probe.Kind {
		case "column", "constraint":
			if !identifierPattern.MatchString(probe.Table) {
				return fmt.Errorf("adopt.probes[%d]（kind: %s）必须声明 table（不含前缀的逻辑表名）：%q", i, probe.Kind, probe.Table)
			}
		case "table", "index":
			if probe.Table != "" {
				return fmt.Errorf("adopt.probes[%d]（kind: %s）不得带 table（对象名 %q 本身即待查对象）", i, probe.Kind, probe.Name)
			}
		}
		key := fmt.Sprintf("%d/%s/%s/%s", probe.Version, probe.Kind, probe.Table, probe.Name)
		if _, dup := seen[key]; dup {
			return fmt.Errorf("adopt.probes[%d]（version %d，%s %s）重复声明", i, probe.Version, probe.Kind, probe.Name)
		}
		seen[key] = struct{}{}
	}
	if objects == 0 {
		return errors.New("adopt.probes 里一条对象探针都没有（全是 kind: none）：接管段无判定力 ⇒ 拒装——" +
			"至少声明一条 table/column/index/constraint 探针（存在性判定靠它）")
	}
	for version := 1; version <= m.Adopt.Through; version++ {
		if !covered[version] {
			return fmt.Errorf("adopt.probes 缺 version %d 的声明：1..through(%d) 的每个版本都要显式覆盖"+
				"（对象探针或 kind: none）——漏一条会让存量库被判成干净库、全量重放旧迁移（防漏方向）", version, m.Adopt.Through)
		}
	}
	return nil
}

// reservedColumn 引擎自管的保留列名（软删列、主键、时间戳、拖动排序列）
func reservedColumn(name string, dragSort bool) bool {
	switch name {
	case "id", "created_at", "updated_at", "deleted_at":
		return true
	case "weigh":
		return dragSort // drag_sort 时 weigh 由引擎附加；未开时允许作者自定义名为 weigh 的业务列
	}
	return false
}

// checkSignature 分离签名的相对路径校验（标「相对路径」）：签名文件是**包内形态**（包
// 结构里有 plugin.yaml.sig），只允许插件目录内的相对路径——绝对路径或 `..` 段会让落位/打包侧
// （export 的 CollectPackFiles、安装链的签名读取）把插件目录之外的文件读进包或被当作签名。
// 与 web / migrations_dir 同口径复用 checkRelPath。
//
// **不强制存在**：未签名包是合法形态（安装期按「无签名」fail-closed 处理，--allow-unsigned）；
// 同时不得占用两个包内核定名（清单自身与 files.sha256 —— 后者 = cli.PackManifestFile，本包不 import cli）。
func checkSignature(m *Manifest) error {
	if strings.TrimSpace(m.Signature) == "" {
		return nil
	}
	if err := checkRelPath(m.Signature, "signature"); err != nil {
		return err
	}
	switch path.Clean(filepath.ToSlash(m.Signature)) {
	case FileName, "files.sha256":
		return fmt.Errorf("signature %q 是包内核定名（%s / files.sha256），不能用作签名文件名；请换名（默认 plugin.yaml.sig）",
			m.Signature, FileName)
	}
	return nil
}

// checkWeb 前端落位声明校验（源路径必须是插件目录内的相对路径；声明了就必须存在。
// declarative 插件不需要前端文件，其 L2 实体共用宿主通用页，故 web 段可选）
func checkWeb(m *Manifest, opts Options) error {
	if m.Web == nil {
		return nil
	}
	if err := checkWebPaths(m.Web.Views, "web.views", opts); err != nil {
		return err
	}
	return checkWebPaths(m.Web.Lang, "web.lang", opts)
}

// checkWebPaths 逐个前端源路径校验（相对路径合法性 + 存在性）
func checkWebPaths(items []string, field string, opts Options) error {
	for i, p := range items {
		if err := checkRelPath(p, field); err != nil {
			return fmt.Errorf("%s[%d]: %w", field, i, err)
		}
		if opts.PluginDir == "" {
			continue
		}
		full := path.Join(opts.PluginDir, p)
		if _, err := os.Stat(full); err != nil {
			return fmt.Errorf("%s[%d] 的源路径 %q 不存在（%s）: %w", field, i, p, full, err)
		}
	}
	return nil
}

// checkConfigs 自带配置键校验（分组由安装器写 plugin.<名>，这里只查键名与重复）
func checkConfigs(m *Manifest) error {
	seen := make(map[string]struct{}, len(m.Configs))
	for i, item := range m.Configs {
		if !configKeyPattern.MatchString(item.Name) {
			return fmt.Errorf("configs[%d].name %q 不合法（^[a-z][a-z0-9_]{0,62}$）", i, item.Name)
		}
		if _, dup := seen[item.Name]; dup {
			return fmt.Errorf("configs[%d].name %q 重复声明", i, item.Name)
		}
		seen[item.Name] = struct{}{}
		if strings.TrimSpace(item.Title) == "" {
			return fmt.Errorf("configs[%d]（%s）必须声明 title（配置页展示用）", i, item.Name)
		}
	}
	return nil
}

// checkMigrationsDir 迁移目录声明校验（默认 migrations/；显式声明了就必须存在于插件目录内——
// 未声明的插件允许没有迁移目录，此时安装器按「无迁移可应用」处理）
func checkMigrationsDir(m *Manifest, opts Options) error {
	if m.MigrationsDir == "" {
		return nil
	}
	if err := checkRelPath(m.MigrationsDir, "migrations_dir"); err != nil {
		return err
	}
	if opts.PluginDir == "" {
		return nil
	}
	full := path.Join(opts.PluginDir, m.MigrationsDir)
	info, err := os.Stat(full)
	if err != nil {
		return fmt.Errorf("migrations_dir %q 不存在（%s）: %w", m.MigrationsDir, full, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("migrations_dir %q 不是目录（%s）", m.MigrationsDir, full)
	}
	return nil
}

// checkRelPath 相对路径校验：不得绝对、不得含 .. 段（落位目标逐级校验的前置，与zip 防护同口径）
func checkRelPath(p, field string) error {
	if strings.TrimSpace(p) == "" {
		return fmt.Errorf("%s 含空路径项", field)
	}
	clean := path.Clean(filepath.ToSlash(p))
	if path.IsAbs(clean) || strings.HasPrefix(clean, "../") || clean == ".." {
		return fmt.Errorf("%s 的路径 %q 必须是插件目录内的相对路径（不得绝对、不得含 ..）", field, p)
	}
	return nil
}

// splitList 拆逗号分隔清单（去空项与首尾空白）
func splitList(s string) []string {
	var out []string
	for _, item := range strings.Split(s, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

// contains 闭集成员判定
func contains(set []string, value string) bool {
	for _, item := range set {
		if item == value {
			return true
		}
	}
	return false
}
