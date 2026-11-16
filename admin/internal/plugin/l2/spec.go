// Package l2 是 L2 声明式插件的数据面（动态 CRUD 引擎）。
//
// 设计说明（spec 闭集与语义）/（引擎结构与 SQL 安全硬条款）/
// （DDL 与演进）/（元数据契约）/（启动预检与装载）。为什么自建引擎而不复用泛型四层：
// F5 已证 bindx.Tri[T] 与 handler.Handler[T any] 需要编译期 T，而运行时实体在 Go 里没有类型。
//
// 文件分工（表格）：
//   - spec.go    元数据装载（按请求读库 + ≤1s 缓存）与 spec 校验（宿主闭集 + 引擎增补规则）；
//   - ddl.go     CREATE/ALTER/COMMENT/索引生成 + 表结构自省（升级基线）+ 逐条执行；
//   - ddlplan/   CLI 与引擎之间的**结构计划接口叶子包**（cli 不能直接 import 本包：本包的演示用例
//     import cli，反向依赖即 import cycle not allowed in test；类型只有一份，本包用别名指向它）；
//   - planner.go 把引擎注册进 ddlplan（安装期 DDL 由 CLI 侧编排、本引擎生成与执行）；
//   - query.go   动态查询构造：标识符白名单 + 正则复核 + Statement.Quote，值全参数化，分页/排序口径；
//   - handler.go 六动作 list/get/create/update/delete/sort（契约对齐 F5/F9）；
//   - router.go  按元数据注册相对段路由（挂 host.Gate，前缀由宿主 admin 组提供）。
//
// 接入方式（两条路等价，包级 Mount 幂等——两条都做也不会双重注册）：
//
//	① 宿主加一行空白导入 `_ ".../internal/plugin/l2"`（本包 init() 自注册到 registry.RegisterAdmin，
//	   路由在 router.Setup 遍历时按 host.Loaded() 快照注册）；
//	② 接线侧显式调用 l2.Mount(adminGroup)（internal/plugins/wiring.go 的 mount 内一行）。
//
// 前置：元数据必须先装载（cmd/serve 的 host.Preflight 在 router.Setup 之前）。
//
// SQL 安全（硬条款，本包的实现口径）：① 表名由引擎按配置前缀 + 占位公式构造（元数据里存的物理名
// 亦过同一字符集白名单）；列名只来自 spec 且逐字段过 ^[a-z][a-z0-9_]{0,62}$ 复核；拼接标识符一律用
// gorm 的 Statement.Quote（注意 clause 包没有 Quote 函数）；② 值一律参数化，任何用户输入不进标识符位置；
// 操作符经宿主同款白名单（复用 manifest.SearchOperators 与 service.GetOperatorByAlias）；③ 分页复用
// service.BuildPaginateScope（上限自持 maxPageLimit=1000）；④ 排序 = 列级 sort:true 字段 + 引擎列（id/created_at/
// weigh）白名单 + 主键 DESC 托底（不复用 BuildOrderScope：它按 GORM Schema 校验字段，动态表无 Schema）；
// ⑤ list 显式列出字段，不 SELECT *；⑥ where 条件不复用 service.BuildWhereScopes（同因），由本包按闭集生成。
package l2

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ai-go-hub/ai-go-admin/internal/infra/config"
	"github.com/ai-go-hub/ai-go-admin/internal/plugin/host"
	"github.com/ai-go-hub/ai-go-admin/internal/plugin/manifest"

	"gorm.io/gorm"
)

// identifierPattern 实体名 / 字段名的闭集标识符（与 internal/plugin/host 与 manifest 同形）：
// 任何用户输入都不得进入标识符位置（硬条款 ①）。
var identifierPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

// pluginNamePattern 插件名形状（manifest.name：^[a-z][a-z0-9-]{1,31}$）
var pluginNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{1,31}$`)

// tablePattern 物理表名的字符集白名单。表名含全局配置前缀（默认空），故不能套实体名的
// ^[a-z... 正则；仍必须是「裸标识符」——引擎随后用 Statement.Quote 引用，两者叠加即：
// 元数据被改写也引不到任意表达式（表名注入的负例见 query_test.go）。
var tablePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,62}$`)

// 引擎自管列（/）：spec 字段名不得占用（与 manifest.checkEntities 的 reservedColumn 同口径，
// 只是这里作用在库里的 spec 上——元数据可被手工改写，引擎必须自己再拦一道）。
const (
	ColumnID        = "id"
	ColumnWeigh     = "weigh"
	ColumnDeletedAt = "deleted_at"
	ColumnCreatedAt = "created_at"
	ColumnUpdatedAt = "updated_at"
)

// engineColumns 引擎列全集（DDL 与查询构造用）
var engineColumns = []string{ColumnID, ColumnWeigh, ColumnDeletedAt, ColumnCreatedAt, ColumnUpdatedAt}

// 操作符分类（与 manifest/fields.go 的 fuzzyOperators 同口径；范围与比较类另列）
var (
	fuzzyOperators   = []string{"LIKE", "NOT LIKE", "ILIKE", "NOT ILIKE"}
	rangeOperators   = []string{"BETWEEN", "NOT BETWEEN"}
	compareOperators = []string{"gt", "egt", "lt", "elt"}
)

// 类型分组（操作符与类型的兼容性判定用，9 种 type）
var (
	textishTypes = []string{"string", "text", "enum"}
	orderedTypes = []string{"string", "text", "enum", "int", "bigint", "decimal", "date", "datetime"}
)

// Spec 运行期实体定义：元数据行 + 解析后的 spec（manifest.Entity 是 spec JSON 的唯一形状定义，
// 安装器写的 spec 就是它的序列化结果 ⇒ 引擎复用同一类型，不另立一套必然漂移的结构）。
type Spec struct {
	// Plugin 归属插件名（= admin_plugins.name = 路由段 plugin/<名>）
	Plugin string
	// Name 实体名（闭集标识符；参与物理表名与路由段）
	Name string
	// PhysicalTable 物理表名（元数据显式存实际值；空则按占位公式推导，见 PhysicalTable）
	PhysicalTable string
	// Entity 解析后的 spec 段（字段、drag_sort、ddl_owner、双语标题）
	Entity manifest.Entity
}

// PhysicalEntityTable 物理表名占位公式（<前缀>plugin_<插件>_<实体>）。
// 与 internal/plugin/cli 的 PhysicalEntityTable 同式（该包在 CLI 侧，引擎不复用其实现以免把
// cobra/golang-migrate 依赖拖进运行期）。
func PhysicalEntityTable(prefix, plugin, entity string) string {
	return prefix + "plugin_" + plugin + "_" + entity
}

// PhysicalTableName 实体物理表名：元数据存了实际值就用它（「显式存物理名，避免命名规则演化时失真」），
// 否则按占位公式推导。
func (s *Spec) PhysicalTableName() string {
	if name := strings.TrimSpace(s.PhysicalTable); name != "" {
		return name
	}
	return PhysicalEntityTable(prefixOf(), s.Plugin, s.Name)
}

// Fields spec 声明的字段（顺序即声明顺序：列表列与表单顺序都按它）
func (s *Spec) Fields() []manifest.Field { return s.Entity.Fields }

// DragSort 是否开启拖动排序（实体级开关；true ⇒ 引擎附加 weigh 列并注册 /sort）
func (s *Spec) DragSort() bool { return s.Entity.DragSort }

// DDLOwner ddl_owner（缺省 engine）：engine ⇒ 引擎负责建表/加列/加索引与 --purge 的 DROP；
// migrations ⇒ 引擎只读元数据做渲染与 CRUD，不做任何 ALTER/DROP。
func (s *Spec) DDLOwner() string {
	if strings.TrimSpace(s.Entity.DdlOwner) == "" {
		return "engine"
	}
	return s.Entity.DdlOwner
}

// EngineOwnsDDL：ddl_owner=migrations 时引擎不做任何结构变更
func (s *Spec) EngineOwnsDDL() bool { return s.DDLOwner() == "engine" }

// Field 按名取字段
func (s *Spec) Field(name string) (manifest.Field, bool) {
	for _, field := range s.Entity.Fields {
		if field.Name == name {
			return field, true
		}
	}
	return manifest.Field{}, false
}

// Filterable 字段是否可作查询条件：声明了 search（公开搜索操作符）或 quickSearch（快速搜索参与，
// 宿主快速搜索硬编码 operator:'ILIKE'，见 web/src/hooks/useTableManager.ts:600）。
// 闭集之外一律拒绝（硬条款 ⑥），不做「静默丢弃」（宿主侧未知词会被丢弃，是本层要规避的行为）。
func (s *Spec) Filterable(name string) (manifest.Field, bool) {
	field, ok := s.Field(name)
	if !ok {
		return manifest.Field{}, false
	}
	if field.Search != nil || field.QuickSearch {
		return field, true
	}
	return manifest.Field{}, false
}

// Sortable 列级排序白名单（硬条款 ④）：声明 sort:true 的字段 + 引擎列 id/created_at（宿主
// 页面默认 filter 常为 created_at，见 web/src/views/admin/auth/group/index.vue:47-50）+ weigh（仅
// drag_sort，拖动排序要求 filter.sort='weigh'，见 useTableManager.ts:445-447）。
// 白名单之外一律拒绝——「静默跳过」会让用户以为排序生效（宿主 BuildOrderScope 就是静默跳过）。
func (s *Spec) Sortable(name string) bool {
	switch name {
	case ColumnID, ColumnCreatedAt:
		return true
	case ColumnWeigh:
		return s.DragSort()
	}
	field, ok := s.Field(name)
	return ok && field.Sort
}

// SortableNames 排序白名单的可读列表（错误信息用，顺序稳定）
func (s *Spec) SortableNames() []string {
	names := []string{ColumnID, ColumnCreatedAt}
	if s.DragSort() {
		names = append(names, ColumnWeigh)
	}
	for _, field := range s.Entity.Fields {
		if field.Sort {
			names = append(names, field.Name)
		}
	}
	return names
}

// FilterableNames 可作条件查询的字段列表（错误信息用）
func (s *Spec) FilterableNames() []string {
	names := make([]string, 0, len(s.Entity.Fields))
	for _, field := range s.Entity.Fields {
		if field.Search != nil || field.QuickSearch {
			names = append(names, field.Name)
		}
	}
	return names
}

// ListColumns 列表显式列（硬条款 ⑤：不 SELECT *）。构成：id + 声明 list:true 的字段 +
// drag_sort 时的 weigh + created_at。**未声明 list 的实体**（无任何字段标 list:true）回退为
// 「除 text 外的全部字段」——text 是大字段，按 SQL 纪律不进列表；该回退是引擎的可预期默认
// （否则忘记标 list 的实体列表只有 id/时间列，表面像功能坏了）。
func (s *Spec) ListColumns() []string {
	columns := make([]string, 0, len(s.Entity.Fields)+3)
	columns = append(columns, ColumnID)
	marked := false
	for _, field := range s.Entity.Fields {
		if field.List {
			marked = true
			break
		}
	}
	for _, field := range s.Entity.Fields {
		if marked {
			if field.List {
				columns = append(columns, field.Name)
			}
			continue
		}
		if field.Type != "text" {
			columns = append(columns, field.Name)
		}
	}
	if s.DragSort() {
		columns = append(columns, ColumnWeigh)
	}
	return append(columns, ColumnCreatedAt)
}

// RowColumns 单条读取（get）的显式列：id + 全部 spec 字段 + drag_sort 时的 weigh + created_at
// （表单回填需要全字段；deleted_at/updated_at 对渲染无用，不进选择列表）。
func (s *Spec) RowColumns() []string {
	columns := make([]string, 0, len(s.Entity.Fields)+3)
	columns = append(columns, ColumnID)
	for _, field := range s.Entity.Fields {
		columns = append(columns, field.Name)
	}
	if s.DragSort() {
		columns = append(columns, ColumnWeigh)
	}
	return append(columns, ColumnCreatedAt)
}

// ValueColumns 可写列（create/update 的白名单）：全部 spec 字段。引擎列（id/weigh/deleted_at/
// created_at/updated_at）不在此列——请求体里的同名键一律忽略（拖动排序只能走 /sort，软删是 delete 动作）。
func (s *Spec) ValueColumns() []string {
	columns := make([]string, 0, len(s.Entity.Fields))
	for _, field := range s.Entity.Fields {
		columns = append(columns, field.Name)
	}
	return columns
}

// Title 实体标题（双语元数据：lang 以 "en" 开头取英文，其余取中文）
func (s *Spec) Title(lang string) string {
	if isEnglish(lang) {
		if title := strings.TrimSpace(s.Entity.TitleEn); title != "" {
			return title
		}
		return strings.TrimSpace(s.Entity.TitleZh)
	}
	if title := strings.TrimSpace(s.Entity.TitleZh); title != "" {
		return title
	}
	return strings.TrimSpace(s.Entity.TitleEn)
}

// FieldTitle 字段标题（同 Title 的口径）
func FieldTitle(field manifest.Field, lang string) string {
	if isEnglish(lang) {
		if title := strings.TrimSpace(field.TitleEn); title != "" {
			return title
		}
		return strings.TrimSpace(field.TitleZh)
	}
	if title := strings.TrimSpace(field.TitleZh); title != "" {
		return title
	}
	return strings.TrimSpace(field.TitleEn)
}

// isEnglish locale 判定（前端可能下发 zh-cn / en / en-US；只认前缀，避免新增语言时收不到英文）
func isEnglish(lang string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(lang)), "en")
}

// prefixOf 当前配置的表前缀（与 host.physicalName / cli.PhysicalEntityTable 同源）
func prefixOf() string { return config.Get().Database.Prefix }

// ErrNotInstalled 元数据行不存在（插件已卸载或实体未安装）：请求层据此回 404（
// 「解析失败或 meta 404 ⇒ 显示『插件未安装或已停用』空态」）。
var ErrNotInstalled = errors.New("l2: 实体元数据不存在（插件未安装或已卸载）")

// Parse 解析并校验一条实体元数据行：
//   - 宿主闭集校验 + 引擎增补规则（本包注册进 host 的 spec 校验器：键集闭集 / 键值类型 /
//     type/form/search.op/dict/ddl_owner 值域 + 字段值域闭集 manifest.ValidateField + 保留列名、
//     操作符与类型兼容、trgm 只许文本列、标识符长度预算、物理表名一致性）；
//   - 同一批规则随后在本函数内再算一遍（幂等）；解码用 DisallowUnknownFields 复核键集。
//
// 关于 host.SetSpecValidator：本包在 init 里注册**增补**校验器（宿主闭集校验先行，永不被绕过；
// 见 host/spec.go 的 SetSpecValidator 注释），因此安装期写库校验（cli 的 host.ValidateSpec 探测）
// 与启动预检（host.Preflight）与本函数走的是**同一条判定链**（「两处各写一套必然漂移」）。
func Parse(row host.Entity) (*Spec, error) {
	if err := host.ValidateSpec(row); err != nil {
		return nil, fmt.Errorf("插件 %s 的实体 %s 未通过宿主 spec 校验: %w", row.Plugin, row.Name, err)
	}

	dec := json.NewDecoder(strings.NewReader(string(row.Spec)))
	dec.DisallowUnknownFields()
	var entity manifest.Entity
	if err := dec.Decode(&entity); err != nil {
		return nil, fmt.Errorf("插件 %s 的实体 %s 的 spec 无法装载: %w", row.Plugin, row.Name, err)
	}

	spec := &Spec{Plugin: row.Plugin, Name: row.Name, PhysicalTable: row.PhysicalTable, Entity: entity}
	if err := checkEngineRules(row, spec); err != nil {
		return nil, err
	}
	return spec, nil
}

// ValidateEntitySpec 引擎增补的 spec 校验器（init 里经 host.SetSpecValidator 注册）：
// 它**不调用** host.ValidateSpec（那会成环），只做「宿主闭集之外、引擎运行期必须成立」的判定——
// 与 Parse 的后半段共用 checkEntityName / checkEngineRules，两处同一份实现。
func ValidateEntitySpec(row host.Entity) error {
	dec := json.NewDecoder(strings.NewReader(string(row.Spec)))
	dec.DisallowUnknownFields()
	var entity manifest.Entity
	if err := dec.Decode(&entity); err != nil {
		return fmt.Errorf("插件 %s 的实体 %s 的 spec 无法装载: %w", row.Plugin, row.Name, err)
	}
	spec := &Spec{Plugin: row.Plugin, Name: row.Name, PhysicalTable: row.PhysicalTable, Entity: entity}
	return checkEngineRules(row, spec)
}

// init 注册引擎增补校验器（挂到宿主 SpecValidator 链上；宿主先跑自己的闭集校验）。
// 链接本包的二进制（aigo / serve 都经 internal/plugins → 本包）由此让安装期与启动预检同判定。
func init() { host.SetSpecValidator(ValidateEntitySpec) }

// checkEntityName 实体名与 spec.entity 的一致性、标识符形状、以及物理表名一致性
func checkEntityName(row host.Entity, entity manifest.Entity) error {
	name := strings.TrimSpace(row.Name)
	if !identifierPattern.MatchString(name) {
		return fmt.Errorf("实体名 %q 不是合法标识符（^[a-z][a-z0-9_]{0,62}$）", name)
	}
	if got := strings.TrimSpace(entity.Entity); got != name {
		return fmt.Errorf("spec.entity=%q 与实体行 name=%q 不一致", got, name)
	}
	if !identifierPattern.MatchString(row.Plugin) && !pluginNamePattern.MatchString(row.Plugin) {
		return fmt.Errorf("插件名 %q 不是合法标识符（^[a-z][a-z0-9-]{1,31}$）", row.Plugin)
	}
	table := strings.TrimSpace(row.PhysicalTable)
	if table != "" && !tablePattern.MatchString(table) {
		return fmt.Errorf("物理表名 %q 不是合法标识符（引擎按裸标识符引用，表名注入的负例见 query_test.go）", table)
	}
	// 表名一致性（「table_name 显式存物理名」）：元数据里的物理名必须等于占位公式
	// <前缀>plugin_<名>_<实体> 的结果。不一致 ⇒ 拒载并**点名两侧**——典型成因是表前缀配置被改
	// （元数据记的是旧前缀的表）或元数据被手工改写；此时引擎仍会去读写「公式推导名」，
	// 与元数据声称的表不是同一张 ⇒ 静默读写错表，必须 fail-closed。
	if expected := PhysicalEntityTable(prefixOf(), row.Plugin, name); table != "" && table != expected {
		return fmt.Errorf("物理表名 %q 与命名公式不一致（应为 %q = <前缀>plugin_<插件>_<实体>）："+
			"元数据显式存的表名与实际读写目标必须是同一张表，否则会静默读写错表——"+
			"请核对表前缀配置（database.prefix）或修正元数据行 admin_plugin_entities.table_name",
			table, expected)
	}
	return nil
}

// checkEngineRules 引擎增补规则（宿主闭集之外的、本层运行期必须成立的约束）
func checkEngineRules(row host.Entity, spec *Spec) error {
	if err := checkEntityName(row, spec.Entity); err != nil {
		return err
	}
	if err := manifest.CheckNameEntityLength(spec.Plugin, spec.Name, prefixOf(), spec.DDLOwner()); err != nil {
		return fmt.Errorf("插件 %s 的实体 %s: %w", spec.Plugin, spec.Name, err)
	}
	if len(spec.Entity.Fields) == 0 {
		return fmt.Errorf("插件 %s 的实体 %s 的 spec.fields 为空", spec.Plugin, spec.Name)
	}
	for _, field := range spec.Entity.Fields {
		if reservedColumn(field.Name, spec.DragSort()) {
			return fmt.Errorf("插件 %s 的实体 %s 的字段 %q 命中引擎保留列名（%s）",
				spec.Plugin, spec.Name, field.Name, strings.Join(engineColumns, "/"))
		}
		// 字段值域闭集复用 manifest.ValidateField（唯一定义处：type/form 及其映射、
		// 搜索操作符词表、dict 形状、enum 必须有 dict）——安装期清单校验与运行期引擎判定同一份实现
		if err := manifest.ValidateField(field); err != nil {
			return fmt.Errorf("插件 %s 的实体 %s 的字段 %s: %w", spec.Plugin, spec.Name, field.Name, err)
		}
		if field.Trgm && !slices.Contains(textishTypes, field.Type) {
			return fmt.Errorf("插件 %s 的实体 %s 的字段 %s 声明了 trgm：仅文本类型（%s）可建 pg_trgm GIN 索引",
				spec.Plugin, spec.Name, field.Name, strings.Join(textishTypes, "|"))
		}
		if field.Search != nil {
			if err := checkOperatorType(spec, field, field.Search.Op); err != nil {
				return err
			}
		}
	}
	return nil
}

// reservedColumn 引擎自管列（与 manifest.checkEntities 的 reservedColumn 同口径）
func reservedColumn(name string, dragSort bool) bool {
	switch name {
	case ColumnID, ColumnCreatedAt, ColumnUpdatedAt, ColumnDeletedAt:
		return true
	case ColumnWeigh:
		return dragSort // drag_sort 时 weigh 由引擎附加；未开时允许作者自定义名为 weigh 的业务列
	}
	return false
}

// checkOperatorType 操作符与字段类型的兼容性（硬条款 ② 的「闭集」在本层的落地）：
// 词表本身由宿主闭集把关，此处再拦「词表内但类型上必报错」的组合——如 int 字段声明 ILIKE
// （PG 运行期直接报错），与其在请求期把 500 抛给用户，不如装载期就说清楚。
func checkOperatorType(spec *Spec, field manifest.Field, op string) error {
	bad := func(reason string) error {
		return fmt.Errorf("插件 %s 的实体 %s 的字段 %s（type=%s）的 search.op=%q %s",
			spec.Plugin, spec.Name, field.Name, field.Type, op, reason)
	}
	switch {
	case slices.Contains(fuzzyOperators, op):
		if !slices.Contains(textishTypes, field.Type) {
			return bad("是模糊匹配类操作符，只用于 " + strings.Join(textishTypes, "|"))
		}
	case slices.Contains(rangeOperators, op), slices.Contains(compareOperators, op):
		if !slices.Contains(orderedTypes, field.Type) {
			return bad("是有序比较类操作符，只用于 " + strings.Join(orderedTypes, "|"))
		}
	}
	return nil
}

// checkOperatorAllowed 请求期操作符校验：空串按 eq（与宿主 GetOperatorByAlias("") 同义），
// 其余必须**逐字**落在宿主 16 项词表内（不 trim、不折叠大小写——宿主映射表就是逐字 switch，
// 带空白/异体写法在宿主侧会被静默丢弃，本层一律拒装；请求方按词表原文下发即可）。
func (s *Spec) checkOperatorAllowed(field manifest.Field, op string) (string, error) {
	if op == "" {
		return "eq", nil
	}
	if !slices.Contains(manifest.SearchOperators, op) {
		return "", fmt.Errorf("操作符 %q 不在宿主 16 项词表内（逐字匹配，大小写敏感、不接受空白；%s）",
			op, strings.Join(manifest.SearchOperators, "|"))
	}
	if err := checkOperatorType(s, field, op); err != nil {
		return "", err
	}
	return op, nil
}

// ---------- 元数据装载（按请求读库 + ≤1s 缓存；「元数据装载（启动一次 + 变更失效）」） ----------
//
// 为什么不是「启动快照」：验收口径是「字段/校验/文案变更**即时生效**（元数据驱动）」——启动快照
// 会让新增字段要等重启。路由仍按启动快照注册（新增实体需重启，显式边界），而字段/文案/校验走
// 本函数的短缓存读库（单行索引读，与 host.Gate 同款策略；成本口径）。

// defaultMetadataTTL 元数据缓存上限（与 host 的闸缓存同量级；≤1s ⇒ CLI 改元数据后立即体现）
const defaultMetadataTTL = time.Second

var (
	metadataTTL   = defaultMetadataTTL
	metadataMu    sync.Mutex
	metadataCache = map[string]metadataEntry{}
)

type metadataEntry struct {
	spec    *Spec
	expires time.Time
}

// SetMetadataTTL 设置元数据缓存上限（<=0 恢复默认）；变更会清空已缓存条目。
func SetMetadataTTL(ttl time.Duration) {
	metadataMu.Lock()
	defer metadataMu.Unlock()
	if ttl <= 0 {
		metadataTTL = defaultMetadataTTL
	} else {
		metadataTTL = ttl
	}
	metadataCache = map[string]metadataEntry{}
}

// Resolve 取实体的运行期定义：优先命中缓存，否则按 (plugin, name) 单行读 admin_plugin_entities 并校验。
// 行不存在 ⇒ ErrNotInstalled；spec 非法 ⇒ 带插件/实体名的错误（不静默降级，fail-fast 口径）。
// 主键 (plugin, name) 有唯一索引（框架数据库迁移），是一条索引单行读。
func (e *Engine) Resolve(ctx context.Context, plugin, entity string) (*Spec, error) {
	if err := e.guard(); err != nil {
		return nil, err
	}
	key := plugin + "/" + entity

	metadataMu.Lock()
	if entry, ok := metadataCache[key]; ok && time.Now().Before(entry.expires) {
		metadataMu.Unlock()
		return entry.spec, nil
	}
	metadataMu.Unlock()

	row, err := gorm.G[host.Entity](e.DB()).Where("plugin = ? AND name = ?", plugin, entity).First(ctx)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("%w（plugin=%s entity=%s）", ErrNotInstalled, plugin, entity)
	}
	if err != nil {
		return nil, fmt.Errorf("读取实体元数据失败（plugin=%s entity=%s）: %w", plugin, entity, err)
	}

	spec, err := Parse(row)
	if err != nil {
		return nil, err
	}

	metadataMu.Lock()
	metadataCache[key] = metadataEntry{spec: spec, expires: time.Now().Add(metadataTTL)}
	metadataMu.Unlock()
	return spec, nil
}

// parseIntPK 主键值解析（请求体/path 参数里的 pk 一律字符串；只接受十进制非负整数）
func parseIntPK(raw string) (uint64, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return 0, errors.New("主键值为空")
	}
	pk, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("主键值 %q 不是合法的十进制整数", raw)
	}
	return pk, nil
}
