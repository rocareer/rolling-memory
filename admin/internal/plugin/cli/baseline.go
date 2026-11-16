package cli

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ai-go-hub/ai-go-admin/internal/plugin/manifest"

	"gorm.io/gorm"
)

// 存量库迁移基线（adopt）——设计说明。
//
// 缺口：插件版本表（<前缀>plugin_<名>_migrations）与全局游标（schema_migrations）互不对账，安装器对
// 「已有存量结构、但插件是后装」的库只有一条路——从空版本表全量重放插件自己的迁移链（ALTER / 回填 /
// 菜单种子会二次应用）。解法 = manifest 的 adopt 段（声明 1..through 与核心链对应迁移「同物」）+
// 安装器三态判定：
//   - **全命中**（探针逐条存在）⇒ baseline：只把版本表推进到 through（写 1 行），**不执行任何旧 DDL**；
//   - **全不命中** ⇒ full：干净库 / 下游产品，按编号顺序应用插件链（既往路径不变）；
//   - **部分命中** ⇒ fail-closed **拒装**（先于任何落盘/写库；预演侧与执行侧各算一次，执行侧读数为准）。
//
// 只读纪律（关键）：预演（--dry-run）与执行侧的判定**都只读**——版本表存在性 + 版本行 + pg_catalog
// 探针，**不构造 golang-migrate 的 migrator**：驱动在构造时就会 `CREATE TABLE IF NOT EXISTS` 版本表
// （GOMODCACHE github.com/golang-migrate/migrate/v4@v4.19.1/database/postgres/postgres.go:125 的
// ensureVersionTable）。若预演构造它，「未落盘、未写库」的 dry-run 承诺先破，且执行侧会看到一张新建的
// 空版本表 ⇒ 基线判定被绕过、存量库落全量重放（正是本机制要防的事故）。
//
// 探针只断言**存在性**（不校验类型 / 约束 / 行数）——这是显式缺口，由验收的 schema diff 兜底。
const (
	// baselineModeNone 未声明 adopt：迁移按编号顺序应用（本机制不介入，零库访问）
	baselineModeNone = "none"
	// baselineModeAdopt 全命中：只写版本表基线（Applied 必为空，补偿不做 down 回滚）
	baselineModeAdopt = "adopt"
	// baselineModeFull 全不命中（或版本表已越过接管边界）：按编号顺序应用插件链
	baselineModeFull = "full"
	// baselineModeRejected 部分命中 / 现场与版本行不一致 / dirty：fail-closed 拒装
	baselineModeRejected = "rejected"
	// baselineModeUnread 预演未能读库（连接/查询失败）：渲染与判定都按 fail-closed——照实显示未完成，
	// 不假装「未声明 adopt」（那会把一次读库失败误报成「按编号顺序应用」的常规路径）
	baselineModeUnread = "unread"
)

// BaselineProbe 单条探针的读数（dry-run ③b 段逐条打印；⑥ 判定与预演共用同一形状）
type BaselineProbe struct {
	// Version 插件迁移编号（探针逐条回溯到具体 up 文件）
	Version int
	// Kind table | column | index | constraint | none
	Kind string
	// Table 逻辑表名（不含前缀；table/index 为空）
	Table string
	// Name 逻辑对象名（不含前缀；none 为空）
	Name string
	// Physical 物理对象名（表前缀 + 逻辑名；与 L2 实体表同口径，展示与审计用）
	Physical string
	// Hit 存在性命中（kind: none 恒为 true——显式免探不算缺失）
	Hit bool
}

// Describe 单条探针的展示（物理名 = 表前缀 + 逻辑名，与 L2 实体表同口径）
func (p BaselineProbe) Describe() string {
	if p.Kind == "none" {
		return fmt.Sprintf("· v%-4d %-10s （显式免探：该版本无对象创建）", p.Version, p.Kind)
	}
	mark := "✓"
	if !p.Hit {
		mark = "✗"
	}
	return fmt.Sprintf("%s v%-4d %-10s %s", mark, p.Version, p.Kind, p.Physical)
}

// migrationState 插件版本表的只读快照（口径同 golang-migrate 的 Version：无行 = NilVersion ⇒ 0/false）
type migrationState struct {
	// Existed 版本表是否存在（补偿时据此决定是否 drop）
	Existed bool
	// Version 当前版本行（无行 ⇒ 0）
	Version int
	// Dirty 版本表当前是否 dirty
	Dirty bool
}

// BaselineRead 迁移基线（adopt）的一次读数：预演（dry-run ③b）与执行侧（⑥）**同形状**——
// 预演读数不作执行依据（⑥ 重算），但判定口径只有一处（readBaseline/decideBaselineMode）。
type BaselineRead struct {
	// Declared manifest 是否声明 adopt 段
	Declared bool
	// Through 接管边界（= manifest.adopt.through）
	Through int
	// Table 插件版本表物理名
	Table string
	// State 版本表只读快照
	State migrationState
	// Mode none | adopt | full | rejected | unread（unread 只由预演侧的读库失败给出）
	Mode string
	// Reason rejected 的理由（含缺失探针清单与两条处置）
	Reason string
	// Probes 逐条探针读数（含 kind: none 的免探项）
	Probes []BaselineProbe
	// ProbesTotal / ProbesHit 判定的**对象**探针计数（kind: none 不计入）
	ProbesTotal int
	ProbesHit   int
	// Missing 未命中的对象探针（展示用；由 Probes 派生）
	Missing []string
}

// readBaseline 读一次迁移基线（**只读**）：adopt 未声明 ⇒ 零库访问（ReturnMode=none）。
// db 由调用方给（预演与执行侧各自取同一个句柄；本函数绝不构造 migrator）。
func readBaseline(ctx context.Context, db *gorm.DB, prefix, name string, adopt *manifest.Adopt) (*BaselineRead, error) {
	read := &BaselineRead{Declared: adopt != nil, Table: MigrationTableName(name, prefix), Mode: baselineModeNone}
	if adopt == nil {
		return read, nil
	}
	read.Through = adopt.Through
	if !identifierSafe.MatchString(read.Table) {
		return read, fmt.Errorf("插件版本表名 %q 不是安全标识符：拒绝拼进 SQL", read.Table)
	}

	state, err := readMigrationState(ctx, db, read.Table)
	if err != nil {
		return read, err
	}
	read.State = state

	probes, err := evaluateProbes(ctx, db, prefix, adopt)
	if err != nil {
		return read, err
	}
	read.Probes = probes
	for i := range probes {
		if probes[i].Kind == "none" {
			continue
		}
		read.ProbesTotal++
		if probes[i].Hit {
			read.ProbesHit++
		} else {
			read.Missing = append(read.Missing, probes[i].Describe())
		}
	}
	read.Mode, read.Reason = decideBaselineMode(adopt, read.Table, state, probes)
	return read, nil
}

// readMigrationState 版本表的只读快照（存在性 + 版本行；**不构造 migrator**）。
// 版本表在插件名与表前缀的闭集内拼出，仍过标识符白名单后才进 SQL。
func readMigrationState(ctx context.Context, db *gorm.DB, table string) (migrationState, error) {
	state := migrationState{}
	if !identifierSafe.MatchString(table) {
		return state, fmt.Errorf("版本表名 %q 不是安全标识符：拒绝拼进 SQL", table)
	}
	exists, err := migrationTableExists(ctx, db, table)
	if err != nil {
		return state, err
	}
	state.Existed = exists
	if !exists {
		return state, nil
	}
	var version sql.NullInt64
	var dirty sql.NullBool
	err = db.WithContext(ctx).Raw(`SELECT "version", "dirty" FROM "`+table+`" LIMIT 1`).Row().Scan(&version, &dirty)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return state, nil // 表在、无行 = NilVersion（口径同驱动的 Version()）
	case err != nil:
		return state, fmt.Errorf("读取插件版本表 %s 失败: %w", table, err)
	}
	if version.Valid {
		state.Version = int(version.Int64)
	}
	state.Dirty = dirty.Valid && dirty.Bool
	return state, nil
}

// evaluateProbes 逐条跑探针（全部只读、全部走 pg_catalog / to_regclass；值全参数化，标识符仅闭集拼接）
func evaluateProbes(ctx context.Context, db *gorm.DB, prefix string, adopt *manifest.Adopt) ([]BaselineProbe, error) {
	probes := make([]BaselineProbe, 0, len(adopt.Probes))
	for _, declared := range adopt.Probes {
		probe := BaselineProbe{
			Version:  declared.Version,
			Kind:     declared.Kind,
			Table:    declared.Table,
			Name:     declared.Name,
			Physical: probePhysical(prefix, declared),
		}
		if declared.Kind == "none" {
			probe.Hit = true // 显式免探：不算缺失（仍逐条打印，审计可回溯）
			probe.Physical = "（无对象）"
			probes = append(probes, probe)
			continue
		}
		hit, err := probeExists(ctx, db, prefix, declared)
		if err != nil {
			return nil, err
		}
		probe.Hit = hit
		probes = append(probes, probe)
	}
	return probes, nil
}

// probePhysical 探针的物理名（展示与审计口径）：table / index = 表前缀 + 对象名；
// column / constraint = 表前缀 + 表名 + "." + 对象物理名（点后即 probeQuery 实际绑定的那个名字）。
// 对象物理名是否带前缀按 kind 分（本项目口径，见 probeQuery）：table / index / constraint 带前缀，column 不带
// —— 所以列探针显示 "t_eval_runs.title"、约束探针显示 "t_eval_runs.t_uk_x"（点后逐字是 conname）。
// 前缀为空（两仓现状）时与本修复前完全一致（约束也是 "eval_runs.uk_x"）。
func probePhysical(prefix string, probe manifest.AdoptProbe) string {
	switch probe.Kind {
	case "column":
		return prefix + probe.Table + "." + probe.Name // 列名不带前缀
	case "constraint":
		return prefix + probe.Table + "." + prefix + probe.Name // 约束名带前缀（同表名/索引名）
	}
	return prefix + probe.Name
}

// probeExists 单条探针的存在性（只读）：查询与参数由 probeQuery 构造（纯函数，可单测）。
func probeExists(ctx context.Context, db *gorm.DB, prefix string, declared manifest.AdoptProbe) (bool, error) {
	query, args, err := probeQuery(prefix, declared)
	if err != nil {
		return false, err
	}

	var hit sql.NullBool
	if err := db.WithContext(ctx).Raw(query, args...).Row().Scan(&hit); err != nil {
		return false, fmt.Errorf("执行探针（%s %s）失败: %w", declared.Kind, probePhysical(prefix, declared), err)
	}
	return hit.Valid && hit.Bool, nil
}

// probeQuery 单条探针的查询与参数（**纯函数**，无库依赖，可单测）。
//
// 物理名口径（按 kind 分，事实对照见下）：
//   - table / index：待查对象 = 表前缀 + 逻辑名（表名、索引名带前缀）；
//   - constraint：被查的约束名**也拼前缀**——约束名与表名/索引名同口径（见下）；
//   - column：被查的列名**不拼前缀**——列名从不带前缀（表前缀是**表级**标识符的前缀），只有被查的**表**拼前缀。
//
// 事实对照：
//   - 迁移纪律 docs/sql-discipline.md:145：「表名 / 索引名 / **约束名**一律 `__PREFIX__` 占位符……
//     禁止裸写无前缀表名」——列表里有约束名，没有列名；
//   - 约束名带占位符的实例：cmd/migrate/migrations/000005_plugin.up.sql（`CONSTRAINT
//     "__PREFIX__ck_admin_plugins_kind"`）及其 down 文件的 DROP CONSTRAINT；
//   - 未显式命名的约束（inline PRIMARY KEY / REFERENCES / CHECK）由 PG 按**物理表名**起名（`<表>_pkey` /
//     `<表>_<列>_fkey`）⇒ 同样含前缀（例：插件迁移里未显式命名的 PRIMARY KEY，落在物理表
//     <前缀>plugin_<名>_<实体> 上）；
//   - 列名不带前缀的实例：plugins/demo/migrations/1_init.up.sql（`body` 列）、
//     cmd/migrate/migrations/000001_admin.up.sql:22-23（`COMMENT ON COLUMN "__PREFIX__admins"."id"`——
//     占位符只在表名那一段）；
//   - 而表前缀是受支持的配置：internal/infra/database/database.go:39-43 把它设为 GORM 的 TablePrefix；
//     manifest 侧（internal/plugin/manifest/manifest.go:516）注明探针名写「不含表前缀的逻辑名，前缀由安装器拼」
//     ——「拼」与「不拼」按 kind 分，installer 侧的这条分叉就是本函数。
//
// 早期实现把前缀**统一**拼到被查名字上（physicalName 直接作 attname 绑定值）：前缀非空时 column 探针恒不命中
// ⇒ 全列形态被判「全不命中 ⇒ 干净库」走全量重放（正是本机制要防的事故方向），混合形态则恒拒装；
// 两仓当前前缀为空 ⇒ 该错判跑不出来。约束名那一路的旧行为与本项目口径一致（本条修复只动 column）。
func probeQuery(prefix string, probe manifest.AdoptProbe) (string, []any, error) {
	physicalTable := prefix + probe.Table
	physicalName := prefix + probe.Name
	// 进 SQL 的值虽全走参数绑定，仍按防御性口径过一遍标识符白名单（前缀来自配置，逻辑名来自 manifest）：
	// column 的被查名字是**逻辑名**（列名不拼前缀），故白名单校验的也是逻辑名本身
	identifiers := []string{physicalName, physicalTable}
	if probe.Kind == "column" {
		identifiers = []string{probe.Name, physicalTable}
	}
	for _, identifier := range identifiers {
		if identifier != "" && !identifierSafe.MatchString(identifier) {
			return "", nil, fmt.Errorf("探针对象名 %q 不是安全标识符（表名/索引名/约束名拼前缀、列名不拼；"+
				"只允许 [A-Za-z_][A-Za-z0-9_]*）：拒绝拼进 SQL", identifier)
		}
	}

	var (
		query string
		args  []any
	)
	switch probe.Kind {
	case "table":
		query = `SELECT to_regclass(?)::text IS NOT NULL`
		args = []any{physicalName}
	case "index":
		query = `SELECT EXISTS (SELECT 1 FROM pg_class WHERE oid = to_regclass(?) AND relkind IN ('i', 'I'))`
		args = []any{physicalName}
	case "column":
		query = `SELECT EXISTS (SELECT 1 FROM pg_attribute WHERE attrelid = to_regclass(?) AND attname = ? AND attnum > 0 AND NOT attisdropped)`
		args = []any{physicalTable, probe.Name}
	case "constraint":
		query = `SELECT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = to_regclass(?) AND conname = ?)`
		args = []any{physicalTable, physicalName}
	default:
		return "", nil, fmt.Errorf("未知探针 kind %q（闭集 %s）", probe.Kind, strings.Join(manifest.AdoptProbeKinds, "|"))
	}
	return query, args, nil
}

// decideBaselineMode 三态判定（**纯函数**，无库依赖，可单测）。table 只用于错误文案。
//
// 判定表：
//   - 未声明 adopt ⇒ none（本机制不介入）；
//   - 版本表 dirty ⇒ rejected（不静默 force）；
//   - 版本行 > through ⇒ full（插件链已越过接管边界：增量照旧，探针不参与判定）；
//   - 无版本行：全命中 ⇒ adopt；全不命中 ⇒ full；部分命中 ⇒ rejected；
//   - 0 < 版本行 ≤ through：全命中 ⇒ adopt（复装/归位：现场确认由核心链建过）；否则 rejected（版本行与结构不一致）。
func decideBaselineMode(adopt *manifest.Adopt, table string, state migrationState, probes []BaselineProbe) (string, string) {
	if adopt == nil {
		return baselineModeNone, ""
	}
	if state.Dirty {
		return baselineModeRejected, fmt.Sprintf("版本表 %s 处于 dirty（版本 %d）：先 `aigo plugin migrate-force <名> %d` 处置后再安装"+
			"（dirty 不做静默 force）", table, state.Version, state.Version)
	}

	total, hit := judgedProbes(probes)
	if total == 0 {
		return baselineModeRejected, "接管段的探针一条都没声明：无判定力 ⇒ 拒装（manifest 校验应已拦下，这里是执行侧兜底）"
	}

	if state.Version > adopt.Through {
		return baselineModeFull, fmt.Sprintf("版本表 %s 已在 %d（越过接管边界 %d）：按插件链增量应用（接管段不再介入）",
			table, state.Version, adopt.Through)
	}
	switch {
	case hit == total:
		return baselineModeAdopt, ""
	case hit == 0:
		if state.Version > 0 {
			return baselineModeRejected, fmt.Sprintf("版本表 %s 记着版本 %d（属接管段 1..%d），但 %d 条探针**一条都不命中**："+
				"版本行与结构不一致 ⇒ 拒装（先人工核对：结构是被删过，还是版本行是手工 force 出来的）",
				table, state.Version, adopt.Through, total)
		}
		return baselineModeFull, fmt.Sprintf("%d 条探针全部未命中 ⇒ 判为干净库/下游库形态：按编号顺序全量应用插件链（接管段不介入）", total)
	default:
		if state.Version > 0 {
			return baselineModeRejected, fmt.Sprintf("版本表 %s 记着版本 %d，探针只命中 %d/%d：现场与声明不一致 ⇒ 拒装（fail-closed）",
				table, state.Version, hit, total)
		}
		return baselineModeRejected, fmt.Sprintf("探针只命中 %d/%d（部分命中）⇒ 拒装（fail-closed）", hit, total)
	}
}

// judgedProbes 判定的对象探针计数与命中数（kind: none 只作覆盖声明，不参与判定）
func judgedProbes(probes []BaselineProbe) (total, hit int) {
	for i := range probes {
		if probes[i].Kind == "none" {
			continue
		}
		total++
		if probes[i].Hit {
			hit++
		}
	}
	return total, hit
}

// baselineRejectError 拒装的错误（预演侧与执行侧共用同一文案：两条处置口径只有一处）
func baselineRejectError(name string, read *BaselineRead) error {
	missing := ""
	if len(read.Missing) > 0 {
		missing = "\n  未命中的探针：\n    " + strings.Join(read.Missing, "\n    ")
	}
	return fmt.Errorf("插件 %s 的迁移基线（adopt）拒装：%s%s\n"+
		"  处置①：先把核心链推到位（`aigo migrate up`）再重装——探针全命中即走 baseline（不执行旧 DDL）；\n"+
		"  处置②：人工确认现状后归位版本表（`aigo plugin migrate-force %s <版本>`），再由插件链应用剩余版本；\n"+
		"  两条都不可行 ⇒ 先在库上补齐缺失结构，或把缺失项作为迁移补进插件链（不静默放行）",
		name, read.Reason, missing, name)
}

// formatBaselineSummary 审计与日志用的单行摘要（mode/through/探针计数）
func formatBaselineSummary(read *BaselineRead, versionTableCreated bool) string {
	if read == nil || !read.Declared {
		return "未声明 adopt（按编号顺序应用插件链）"
	}
	return fmt.Sprintf("adopt: mode=%s through=%d 探针=%d/%d 版本表新建=%v",
		read.Mode, read.Through, read.ProbesHit, read.ProbesTotal, versionTableCreated)
}

// verifyAdopt adopt 段与**迁移文件事实**的对账（安装期与 verify 共用；①②③）：
//   - through 必须等于插件迁移的最大编号（接管边界 = 1..through 与核心链同物）；
//   - 1..through 必须连续（up/down 成对由 ScanMigrations 保证）；
//   - 每条对象探针的对象名必须出现在**该版本**的 up 文件里（防虚：虚探针会让存量库被判成干净库、
//     全量重放旧迁移——这一方向的错判最危险，故在装前用文件事实兜一道）；
//   - **含 DDL 对象创建的版本必须至少一条对象探针**（防漏：该版本被声明成 kind: none 时，探针全命中
//     照样成立 ⇒ 存量库缺表/缺列也会被写成「与核心链同物」，而核心链 `aigo migrate up` 只推宿主表、
//     没有任何链会事后补建这些插件对象）。
//
// 纯文件解析（不连库）：manifest 包的 checkAdopt 只做结构闭集，本函数补「与迁移文件对账」这一半。
func verifyAdopt(dir string, adopt *manifest.Adopt, migrations []Migration) error {
	if adopt == nil {
		return nil
	}
	if len(migrations) == 0 {
		return errors.New("声明了 adopt（接管段）但插件没有迁移文件：接管边界无迁移可对应——要么去掉 adopt 段，要么补 migrations/")
	}

	versions := make(map[int]bool, len(migrations))
	maxVersion := 0
	for _, migration := range migrations {
		if migration.Kind != "up" {
			continue
		}
		versions[migration.Version] = true
		if migration.Version > maxVersion {
			maxVersion = migration.Version
		}
	}
	if adopt.Through != maxVersion {
		return fmt.Errorf("adopt.through %d ≠ 插件迁移的最大编号 %d（接管边界必须等于最大编号：1..through 与核心链对应迁移同物）",
			adopt.Through, maxVersion)
	}
	for version := 1; version <= adopt.Through; version++ {
		if !versions[version] {
			return fmt.Errorf("插件迁移缺编号 %d：1..through(%d) 必须连续（up/down 成对）", version, adopt.Through)
		}
	}

	upPath := make(map[int]string, len(migrations))
	for _, migration := range migrations {
		if migration.Kind == "up" {
			upPath[migration.Version] = migration.Path
		}
	}
	for i, probe := range adopt.Probes {
		if probe.Kind == "none" {
			continue
		}
		content, err := readUpFileContent(dir, upPath[probe.Version])
		if err != nil {
			return fmt.Errorf("adopt.probes[%d]（version %d）: %w", i, probe.Version, err)
		}
		if !containsObjectName(content, probe.Name) {
			return fmt.Errorf("adopt.probes[%d]（version %d，%s %s）的对象名在 %s 里找不到："+
				"虚探针会让存量库被判成干净库、全量重放旧迁移（防虚方向）",
				i, probe.Version, probe.Kind, probe.Name, upPath[probe.Version])
		}
	}

	// 防漏门禁：**含 DDL 对象创建**的版本必须至少一条对象探针（kind: none 一律拒）；纯菜单/文案/回填
	// 版本允许 kind: none。判据 = 该版本 up 文件的**语句事实**（scanSQL 跳注释 / 字符串 / 美元引用块），
	// 不靠作者自觉——把建表/加列的版本声明成 kind: none，「探针全命中」照样成立，于是存量库在结构不全时
	// 也能走 baseline（缺的对象此后永不补建，防漏方向）。
	probed := make(map[int]bool, len(adopt.Probes))
	for _, probe := range adopt.Probes {
		if probe.Kind != "none" {
			probed[probe.Version] = true
		}
	}
	for version := 1; version <= adopt.Through; version++ {
		scan, err := scanUpFile(dir, upPath[version])
		if err != nil {
			return fmt.Errorf("version %d: %w", version, err)
		}
		if len(scan.DDL) == 0 || probed[version] {
			continue
		}
		return fmt.Errorf("插件迁移 %s（version %d）含 DDL 对象创建（%s），但 adopt 没有为该版本声明对象探针"+
			"（kind: none 不算）：该版本的对象在存量库上探测不到，且核心链 `aigo migrate up` 不会补建 ⇒ 拒装——"+
			"为本版本声明至少一条探针（table / column / index / constraint），或把该版本的 DDL 挪出接管段"+
			"（防漏方向）",
			upPath[version], version, strings.Join(scan.DDL, " + "))
	}
	return nil
}

// scanUpFile 读该版本 up 文件的**原始正文**并做词法扫描（禁词与 DDL 形态的判据都在 scanSQL 里——
// 它自己会跳过行注释 / 块注释 / 字符串 / 美元引用块）。对象名比对另有 readUpFileContent（去注释 +
// 去 __PREFIX__ 占位符）。
func scanUpFile(dir, file string) (sqlScan, error) {
	if file == "" {
		return sqlScan{}, errors.New("该版本没有 up 文件（编号成对性应由 ScanMigrations 保证）")
	}
	raw, err := os.ReadFile(filepath.Join(dir, file))
	if err != nil {
		return sqlScan{}, fmt.Errorf("读取迁移文件 %s 失败: %w", file, err)
	}
	return scanSQL(string(raw)), nil
}

// readUpFileContent 读该版本 up 文件的正文（去掉行注释与 __PREFIX__ 占位符——对象名按无前缀形态比对）
func readUpFileContent(dir, file string) (string, error) {
	if file == "" {
		return "", errors.New("该版本没有 up 文件（编号成对性应由 ScanMigrations 保证）")
	}
	raw, err := os.ReadFile(filepath.Join(dir, file))
	if err != nil {
		return "", fmt.Errorf("读取迁移文件 %s 失败: %w", file, err)
	}
	text := strings.ReplaceAll(string(raw), "__PREFIX__", "")
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		if idx := strings.Index(line, "--"); idx >= 0 {
			lines[i] = line[:idx]
		}
	}
	return strings.Join(lines, "\n"), nil
}

// containsObjectName 对象名是否作为独立标识符出现在迁移文件正文里（词边界匹配，避免前缀撞名）
func containsObjectName(content, name string) bool {
	return regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `\b`).MatchString(content)
}
