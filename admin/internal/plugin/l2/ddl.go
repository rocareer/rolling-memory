package l2

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/ai-go-hub/ai-go-admin/internal/plugin/l2/ddlplan"
	"github.com/ai-go-hub/ai-go-admin/internal/plugin/manifest"
)

// DDL 生成与执行：
//   - 装（新实体）：CREATE TABLE IF NOT EXISTS + 索引一次到位 + COMMENT ON，逐条执行、每条独立事务、
//     失败即停并回报每条 DDL 结果（不吞错）；
//   - 升级/复装的基线：**读实际表结构自省**（information_schema.columns + pg_indexes）作为差异基线，
//     spec 是目标态——只靠 CREATE TABLE IF NOT EXISTS 会静默漏列（既有表不会被重建）；
//   - 列级变更（允许/拒绝清单，逐条落地）：
//     ✓ 加列（可空或带默认）；✓ 加/删**引擎命名**索引；✓ 放宽类变更 = `DROP DEFAULT`
//     （spec 未声明 default 时，ADD COLUMN 为落 NOT NULL 而补的零值默认随即被丢掉——
//     升级示例正是这两条成对出现；不触碰既有数据）；
//     ✗ 删列（表里存在而 spec 未声明的列一律保留，只进报告，绝不自动 DROP）；
//     ✗ 改类型 / 改长度（DDL 层都是 ALTER COLUMN TYPE ⇒ 一律拒绝，提示改用迁移文件升 mixed/L1）；
//     ✗ 改列名（现场特征 = 「旧列消失 + 新列出现」同时成立 ⇒ 点名两侧疑似改名并拒绝）；
//     ✗ 既有列的 NOT NULL 等属性变更（只报告，不自动改——自动收紧约束要扫全表，风险高于收益）。
//
// ddl_owner=migrations：引擎不做任何 ALTER/DROP，计划为空，DropTable 另有一道守卫。
//
// 标识符安全：DDL 文本里的列名/表名/索引名一律经 Engine.quote（= gorm 的 Statement.Quote，硬条款 ①）；
// 字面量（DEFAULT / COMMENT）经类型化转写与单引号翻倍转义，绝不直接拼接用户输入。

// pgIdentifierMaxBytes PG 标识符的字节上限（长度预算反推依据）
const pgIdentifierMaxBytes = 63

// Statement / Result / Plan 的**结构定义在 internal/plugin/l2/ddlplan**（CLI 与引擎的共享面：
// 安装期的 DDL 由 cli 侧编排、引擎执行，而 cli 不能直接 import 本包——本包在演示用例里 import cli，
// 反向依赖会构成 import cycle not allowed in test，见 ddlplan 的包注释）。
// 本包用类型别名保持既有 API 与用例逐字不变（字段、json 标签都只有一份，不漂移）。
type (
	// Statement 一条待执行 DDL（kind 供 dry-run 分类展示与失败定位）
	Statement = ddlplan.Statement
	// StatementResult 逐条 DDL 的执行回报（失败即停：切片含失败那条与之前已成功的条目）
	StatementResult = ddlplan.Result
	// Plan 某实体的结构计划（spec 为目标态，自省结果为基线）
	Plan = ddlplan.Plan
)

// DDL 语句类别
const (
	KindCreateTable = "create_table"
	KindAddColumn   = "add_column"
	KindAlterColumn = "alter_column"
	KindCreateIndex = "create_index"
	// KindDropIndex 删除 spec 不再声明的引擎命名索引（「允许 加/删索引」）
	KindDropIndex = "drop_index"
	KindComment   = "comment"
)

// BuildPlan 生成某实体的结构计划：表缺失 ⇒ 建表 + 索引 + 注释；表在 ⇒ 以自省为基线做差异
// （加列 / DROP DEFAULT 收尾 / 补缺失索引），并把「拒绝项」聚合成一条可读错误。
func (e *Engine) BuildPlan(ctx context.Context, spec *Spec) (*Plan, error) {
	if err := e.guard(); err != nil {
		return nil, err
	}
	table := spec.PhysicalTableName()
	if _, err := e.quoteTable(table); err != nil {
		return nil, err
	}

	plan := &Plan{Plugin: spec.Plugin, Entity: spec.Name, Table: table, DDLOwner: spec.DDLOwner()}
	if !spec.EngineOwnsDDL() {
		plan.Warnings = append(plan.Warnings,
			"ddl_owner=migrations：引擎不做任何 ALTER/DROP（表结构与删除全由插件迁移承担）；复装/升级不依赖引擎补齐列")
		return plan, nil
	}

	if spec.usesTrgm() {
		if err := e.requireTrgmExtension(ctx); err != nil {
			return nil, fmt.Errorf("实体 %s/%s 的 spec 声明了 trgm：%w", spec.Plugin, spec.Name, err)
		}
	}

	state, err := e.introspect(ctx, table)
	if err != nil {
		return nil, err
	}
	plan.Existing = state.existing

	if !state.existing {
		return e.createPlan(spec, plan)
	}
	return e.upgradePlan(spec, plan, state)
}

// createPlan 建表 + 索引 + 注释（装新实体；全部 IF NOT EXISTS，重复应用安全）
func (e *Engine) createPlan(spec *Spec, plan *Plan) (*Plan, error) {
	table, err := e.quoteTable(plan.Table)
	if err != nil {
		return nil, err
	}

	definitions := make([]string, 0, len(spec.Fields())+5)
	idColumn, err := e.quote(ColumnID)
	if err != nil {
		return nil, err
	}
	definitions = append(definitions, idColumn+" BIGSERIAL PRIMARY KEY")

	for _, field := range spec.Fields() {
		definition, err := e.columnDefinition(field)
		if err != nil {
			return nil, fmt.Errorf("字段 %s: %w", field.Name, err)
		}
		definitions = append(definitions, definition)
		if field.Required {
			if _, source, err := defaultLiteral(field); err == nil && source == defaultSourceZero {
				plan.Warnings = append(plan.Warnings, fmt.Sprintf(
					"字段 %s 必填但未声明 default：列默认取类型零值，旁路写入该列会落零值", field.Name))
			}
		}
	}
	if spec.DragSort() {
		weigh, err := e.quote(ColumnWeigh)
		if err != nil {
			return nil, err
		}
		definitions = append(definitions, weigh+" INTEGER NOT NULL DEFAULT 0")
	}
	deletedAt, err := e.quote(ColumnDeletedAt)
	if err != nil {
		return nil, err
	}
	createdAt, err := e.quote(ColumnCreatedAt)
	if err != nil {
		return nil, err
	}
	updatedAt, err := e.quote(ColumnUpdatedAt)
	if err != nil {
		return nil, err
	}
	definitions = append(definitions,
		deletedAt+" TIMESTAMPTZ",
		createdAt+" TIMESTAMPTZ NOT NULL DEFAULT now()",
		updatedAt+" TIMESTAMPTZ NOT NULL DEFAULT now()",
	)

	plan.Statements = append(plan.Statements, Statement{
		Kind: KindCreateTable,
		Text: fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s (\n  %s\n)", table, strings.Join(definitions, ",\n  ")),
	})

	indexes, err := e.indexStatements(spec, plan.Table)
	if err != nil {
		return nil, err
	}
	plan.Statements = append(plan.Statements, indexes...)
	comments, err := e.commentStatements(spec, plan.Table)
	if err != nil {
		return nil, err
	}
	plan.Statements = append(plan.Statements, comments...)
	return plan, nil
}

// upgradePlan 既有表的差异计划：加列（含注释；spec 未声明 default 时示例再收尾一条
// DROP DEFAULT）、补缺失索引；类型/长度不符 ⇒ 聚合成错误（拒绝，提示改用迁移文件）；
// 「旧列消失 + 新列出现」⇒ 点名疑似改列名并拒绝。
func (e *Engine) upgradePlan(spec *Spec, plan *Plan, state *schemaState) (*Plan, error) {
	table, err := e.quoteTable(plan.Table)
	if err != nil {
		return nil, err
	}

	rejections := make([]string, 0)
	added := make([]string, 0) // spec 声明而表上没有的列（「新列出现」侧，改名判定的依据之一）
	for _, field := range spec.Fields() {
		quoted, err := e.quote(field.Name)
		if err != nil {
			return nil, err
		}
		actual, exists := state.columns[field.Name]
		if !exists {
			definition, err := e.columnDefinition(field)
			if err != nil {
				return nil, fmt.Errorf("字段 %s: %w", field.Name, err)
			}
			literal, source, hasDefault, err := defaultClause(field)
			if err != nil {
				return nil, fmt.Errorf("字段 %s: %w", field.Name, err)
			}
			if field.Required {
				if hasDefault && source == defaultSourceZero {
					plan.Warnings = append(plan.Warnings, fmt.Sprintf(
						"字段 %s 必填但未声明 default：新增列补类型零值默认（既有行该列取零值）", field.Name))
				}
			}
			plan.Statements = append(plan.Statements, Statement{
				Kind: KindAddColumn,
				Text: fmt.Sprintf("ALTER TABLE %s ADD COLUMN IF NOT EXISTS %s", table, definition),
			})
			// 放宽类变更（升级示例的两条语句）：ADD COLUMN 必须带默认才能在非空表上落 NOT NULL，
			// 而 spec 未声明 default ⇒ 随即 DROP DEFAULT，让列结构与 spec 的目标态一致（不触碰既有数据）。
			// 显式声明了 default 的字段保留 ADD 时写入的默认（与建表示例 title ... DEFAULT '' 同口径）。
			if field.Default == nil && hasDefault {
				plan.Statements = append(plan.Statements, Statement{
					Kind: KindAlterColumn,
					Text: fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s DROP DEFAULT", table, quoted),
				})
				plan.Warnings = append(plan.Warnings, fmt.Sprintf(
					"字段 %s 未声明 default：新增列的默认是 ADD COLUMN 的收尾（%s）随之 DROP DEFAULT（「放宽类变更」；不触碰既有数据）",
					field.Name, literal))
			}
			comments, err := e.commentStatements(spec, plan.Table, field)
			if err != nil {
				return nil, err
			}
			plan.Statements = append(plan.Statements, comments...)
			added = append(added, field.Name)
			continue
		}

		// 改类型/改长度一律拒绝（「拒绝 删列 / 改类型 / 改列名」；DDL 层两者都是 ALTER COLUMN TYPE）：
		// 引擎对既有列**不做任何 ALTER COLUMN TYPE**，长度变更也在此拦下（varchar 放大曾走该路径，
		// 已按设计回退为拒绝——豁免面只在 ADD COLUMN 与 DROP DEFAULT）。
		expected := pgBaseType(field.Type)
		if actual.DataType != expected {
			rejections = append(rejections, fmt.Sprintf("字段 %s 的列类型是 %s，spec 要求 %s：改类型拒绝（请提供迁移文件并升为 mixed/L1）",
				field.Name, actual.DataType, expected))
			continue
		}
		if expected == "character varying" {
			want := int64(columnLength(field))
			have := int64(0)
			if actual.MaxLength != nil {
				have = *actual.MaxLength
			}
			switch {
			case want == have:
			case want < have:
				rejections = append(rejections, fmt.Sprintf("字段 %s 的 varchar 长度由 %d 收窄到 %d：可能截断既有数据，拒绝（请提供迁移文件并升为 mixed/L1）",
					field.Name, have, want))
			default:
				rejections = append(rejections, fmt.Sprintf("字段 %s 的 varchar 长度由 %d 放大到 %d：长度变更在 DDL 层是 ALTER COLUMN TYPE，归入「改类型」拒绝（引擎对既有列只做 ADD COLUMN 与 DROP DEFAULT；请提供迁移文件并升为 mixed/L1）",
					field.Name, have, want))
			}
		}
		if notNull := actual.Nullable == "NO"; notNull != field.Required {
			plan.Warnings = append(plan.Warnings, fmt.Sprintf(
				"字段 %s 的可空性与 spec 不一致（表上 NOT NULL=%v，spec.required=%v）：引擎不改既有列的属性（只自动放宽默认值），写入期仍按 spec 校验",
				field.Name, notNull, field.Required))
		}
	}

	for _, name := range state.order {
		if _, declared := spec.Field(name); declared || isEngineColumn(name) {
			continue
		}
		plan.KeptColumns = append(plan.KeptColumns, name)
	}
	if len(plan.KeptColumns) > 0 {
		plan.Warnings = append(plan.Warnings, fmt.Sprintf(
			"表里存在而 spec 未声明的列一律保留（不自动 DROP）：%s", strings.Join(plan.KeptColumns, ", ")))
	}

	// 改列名检测（「改列名 ⇒ 拒绝」）：改名在表结构上等价于「删旧列 + 加新列」，而删列被拒、
	// 旧列又一律保留 ⇒ 改名的现场特征是「旧列消失（进保留清单）+ 新列出现（本次要 ADD）」同时成立。
	// 纯加列升级里 spec 的列集是表列集的超集，不会出现保留列；两者并存即点名两侧并拒绝
	// （不猜哪一对是改名——人来看清单决定改用迁移文件还是新增实体）。
	if len(added) > 0 && len(plan.KeptColumns) > 0 {
		rejections = append(rejections, fmt.Sprintf(
			"疑似改列名：表上已存在而 spec 未再声明的列 [%s] 与 spec 声明而表上没有的列 [%s] 同时出现——"+
				"改列名等价于删列 + 加列（会让旧表与旧菜单成为孤儿），拒绝；"+
				"确需改名请写迁移文件并升为 mixed/L1，或改为新增独立实体（旧列一律保留、不自动 DROP）",
			strings.Join(plan.KeptColumns, ", "), strings.Join(added, ", ")))
	}

	indexes, err := e.indexStatements(spec, plan.Table)
	if err != nil {
		return nil, err
	}
	declared := make(map[string]struct{}, len(indexes))
	for _, statement := range indexes {
		name := indexNameOf(statement.Text)
		declared[name] = struct{}{}
		if _, exists := state.indexes[name]; exists {
			continue
		}
		plan.Statements = append(plan.Statements, statement)
	}

	// 反向对齐（「允许 加/删索引」）：表上仍在、而 spec 已不再声明的**引擎命名**索引 ⇒ DROP。
	// 只认引擎命名公式（前缀 + kind + "_plugin_<名>_<实体>_"；截断兜底形态同前缀），手写 /
	// 插件迁移创建 / 主键索引一律不动——不猜归属。名字形态像引擎生成却含非法标识符字符时不擅动，只报告。
	stale := make([]string, 0)
	for name := range state.indexes {
		if _, ok := declared[name]; ok {
			continue
		}
		if !engineIndexName(prefixOf(), spec.Plugin, spec.Name, name) {
			continue
		}
		stale = append(stale, name)
	}
	sort.Strings(stale) // 计划必须确定性：索引名来自 map，排序后再产出语句
	for _, name := range stale {
		if !tablePattern.MatchString(name) {
			plan.Warnings = append(plan.Warnings, fmt.Sprintf(
				"索引 %s 名字形态像引擎生成但含非标识符字符：不自动处理，请人工核对", name))
			continue
		}
		quoted, err := e.quoteIndexName(name)
		if err != nil {
			return nil, err
		}
		plan.Statements = append(plan.Statements, Statement{Kind: KindDropIndex, Text: "DROP INDEX IF EXISTS " + quoted})
		plan.Warnings = append(plan.Warnings, fmt.Sprintf(
			"索引 %s 已不在 spec 的声明内（字段的 index/unique 声明被移除）：引擎删除该索引（「允许 加/删索引」；索引不含数据）", name))
	}

	if len(rejections) > 0 {
		return plan, fmt.Errorf("实体 %s/%s 的表结构变更被拒绝：%s", spec.Plugin, spec.Name, strings.Join(rejections, "; "))
	}
	return plan, nil
}

// ApplyPlan 逐条执行计划：每条独立事务（GORM 的 Exec 不包事务，PG 单条语句即一个隐式事务），
// 失败即停并回报已执行/失败条目（不吞错、不静默继续）。
// 空计划（nil）是**调用契约违例**（应先 BuildPlan 并检查错误）⇒ 返回可读错误，不空指针 panic。
func (e *Engine) ApplyPlan(ctx context.Context, plan *Plan) ([]StatementResult, error) {
	if err := e.guard(); err != nil {
		return nil, err
	}
	if plan == nil {
		return nil, fmt.Errorf("结构计划为空（nil）：拒绝执行（调用方应先 BuildPlan 并检查其错误）")
	}
	results := make([]StatementResult, 0, len(plan.Statements))
	for _, statement := range plan.Statements {
		result := StatementResult{Kind: statement.Kind, Text: statement.Text}
		tx := e.DB().WithContext(ctx).Exec(statement.Text)
		if tx.Error != nil {
			result.Error = tx.Error.Error()
			results = append(results, result)
			return results, fmt.Errorf("执行 DDL 失败（%s）: %w", statement.Kind, tx.Error)
		}
		results = append(results, result)
	}
	return results, nil
}

// DropTable 删除实体表（--purge 唯一删表路径，：界面不提供硬删）。
// **纵深守卫**：ddl_owner=migrations 的表其结构（含删除）由插件迁移自管，本函数自己再判一道
// ——不依赖调用方（cli 的卸载编排已有一道，但这里是「能不删就不删」的最后一道闸：
// 任何新入口绕过调用方检查时，也不会把迁移自管的表 drop 掉）。
func (e *Engine) DropTable(ctx context.Context, spec *Spec) error {
	if !spec.EngineOwnsDDL() {
		return fmt.Errorf("实体 %s/%s 的 ddl_owner=%s：表结构与删除由插件迁移自管，引擎不执行 DROP",
			spec.Plugin, spec.Name, spec.DDLOwner())
	}
	table, err := e.quoteTable(spec.PhysicalTableName())
	if err != nil {
		return err
	}
	if err := e.DB().WithContext(ctx).Exec("DROP TABLE IF EXISTS " + table).Error; err != nil {
		return fmt.Errorf("删除表 %s 失败: %w", table, err)
	}
	return nil
}

// ---------- 自省（升级基线，：读实际表结构而非依赖历史元数据） ----------

// columnState 实际列状态（information_schema.columns 的投影）
type columnState struct {
	Name       string `gorm:"column:column_name"`
	DataType   string `gorm:"column:data_type"`
	MaxLength  *int64 `gorm:"column:character_maximum_length"`
	Nullable   string `gorm:"column:is_nullable"`
	HasDefault bool   `gorm:"column:has_default"`
}

// schemaState 表结构自省结果
type schemaState struct {
	columns  map[string]columnState
	order    []string
	indexes  map[string]struct{}
	existing bool
}

// introspect 读实际表结构：列（类型/长度/可空/默认）+ 索引名 + 是否存在。
// 用 Find 而不是 Scan：Scan 走 Rows()（DryRun 下返回 ErrDryRunModeUnsupported），Find 走查询回调
// （DryRun 下只渲染不执行）⇒ 自省路径可以在不连库的用例里断言 SQL 文本。
func (e *Engine) introspect(ctx context.Context, table string) (*schemaState, error) {
	var columns []columnState
	if err := e.DB().WithContext(ctx).Raw(`
SELECT column_name,
       data_type,
       character_maximum_length,
       is_nullable,
       (column_default IS NOT NULL) AS has_default
FROM information_schema.columns
WHERE table_schema = current_schema()
  AND table_name = ?
ORDER BY ordinal_position`, table).Find(&columns).Error; err != nil {
		return nil, fmt.Errorf("读取表 %s 的列结构失败: %w", table, err)
	}

	state := &schemaState{columns: map[string]columnState{}, indexes: map[string]struct{}{}}
	// 两条自省查询都无条件执行（不要在「无列」时提前返回）：DryRun 下正好能断言两条 SQL 的参数化形态，
	// 真实库上「表不存在」时第二条也只是返回空 —— 换来的是自省路径可被无库用例完整覆盖。
	var indexes []struct {
		IndexName string `gorm:"column:indexname"`
	}
	if err := e.DB().WithContext(ctx).Raw(
		"SELECT indexname FROM pg_indexes WHERE schemaname = current_schema() AND tablename = ?", table).Find(&indexes).Error; err != nil {
		return nil, fmt.Errorf("读取表 %s 的索引失败: %w", table, err)
	}
	for _, index := range indexes {
		state.indexes[index.IndexName] = struct{}{}
	}

	if len(columns) == 0 {
		return state, nil
	}
	state.existing = true
	for i := range columns {
		state.columns[columns[i].Name] = columns[i]
		state.order = append(state.order, columns[i].Name)
	}
	return state, nil
}

// requireTrgmExtension pg_trgm 可用性（声明 trgm 的实体在扩展不可用时拒装）
func (e *Engine) requireTrgmExtension(ctx context.Context) error {
	var count int64
	if err := e.DB().WithContext(ctx).Raw(
		"SELECT COUNT(*) AS count FROM pg_extension WHERE extname = 'pg_trgm'").Find(&count).Error; err != nil {
		return fmt.Errorf("检查 pg_trgm 扩展失败: %w", err)
	}
	if count == 0 {
		return fmt.Errorf("扩展 pg_trgm 不可用：请先执行 CREATE EXTENSION pg_trgm（或去掉 spec 的 trgm，模糊搜索将按前缀匹配）")
	}
	return nil
}

// usesTrgm 是否有字段声明 trgm
func (s *Spec) usesTrgm() bool {
	for _, field := range s.Entity.Fields {
		if field.Trgm {
			return true
		}
	}
	return false
}

// ---------- 列定义与字面量（DDL 文本里的一切都来自闭集：标识符经引用，字面量经类型化转写） ----------

// pgBaseType spec 类型 → PG 基类型（information_schema.data_type 的取值）
func pgBaseType(fieldType string) string {
	switch fieldType {
	case "string", "enum":
		return "character varying"
	case "text":
		return "text"
	case "int":
		return "integer"
	case "bigint":
		return "bigint"
	case "decimal":
		return "numeric"
	case "bool":
		return "smallint"
	case "date":
		return "date"
	case "datetime":
		return "timestamp with time zone"
	default:
		return ""
	}
}

// 长度缺省（键集里没有长度缺省；string 缺省 255、enum 缺省 32，在此显式登记）
const (
	defaultStringLength = 255
	defaultEnumLength   = 32
)

// columnLength 字段的 varchar 长度（仅 string / enum 有意义）
func columnLength(field manifest.Field) int {
	if field.Length > 0 {
		return field.Length
	}
	switch field.Type {
	case "string":
		return defaultStringLength
	case "enum":
		return defaultEnumLength
	}
	return 0
}

// columnDDLType 列类型在 DDL 文本里的写法（大写规范形态，与示例一致：
// VARCHAR(128) / TEXT / INTEGER / BIGINT / NUMERIC(12,2) / SMALLINT / DATE / TIMESTAMPTZ）。
// 与 pgBaseType 分工：那个是 information_schema.data_type 的小写基类型，只用于自省比对。
func columnDDLType(field manifest.Field) string {
	switch field.Type {
	case "string", "enum":
		return fmt.Sprintf("VARCHAR(%d)", columnLength(field))
	case "text":
		return "TEXT"
	case "int":
		return "INTEGER"
	case "bigint":
		return "BIGINT"
	case "decimal":
		// 键集闭集里没有 precision/scale：引擎固定 NUMERIC(12,2)（金额/数量类后台常见取值）；
		// 需要其他精度请升 mixed 用迁移文件管理该表（ddl_owner=migrations）
		return "NUMERIC(12,2)"
	case "bool":
		return "SMALLINT"
	case "date":
		return "DATE"
	case "datetime":
		return "TIMESTAMPTZ"
	}
	return ""
}

// columnDefinition 生成带引号列名 + 类型 + 约束的列定义
func (e *Engine) columnDefinition(field manifest.Field) (string, error) {
	base := columnDDLType(field)
	if base == "" {
		return "", fmt.Errorf("未知类型 %q（闭集见 manifest.FieldTypes）", field.Type)
	}

	quoted, err := e.quote(field.Name)
	if err != nil {
		return "", err
	}
	definition := quoted + " " + base
	if field.Required {
		definition += " NOT NULL"
	}
	literal, _, hasDefault, err := defaultClause(field)
	if err != nil {
		return "", err
	}
	if hasDefault {
		definition += " DEFAULT " + literal
	}
	return definition, nil
}

// 默认值来源（defaultLiteral 的第二个返回值）
const (
	defaultSourceNone = ""
	defaultSourceSpec = "spec"
	defaultSourceZero = "zero"
)

// defaultLiteral 默认值字面量与其来源（仅 spec 声明或必填字段的类型零值；其余无默认）
func defaultLiteral(field manifest.Field) (string, string, error) {
	literal, source, hasDefault, err := defaultClause(field)
	if err != nil {
		return "", "", err
	}
	if !hasDefault {
		return "", defaultSourceNone, nil
	}
	return literal, source, nil
}

// defaultClause 生成 DEFAULT 子句的字面量与来源：
//   - spec.default 声明了 ⇒ 用它（按字段类型转写并校验）；
//   - 未声明但 required ⇒ 用类型零值（文本空串、数值 0、bool 0、date CURRENT_DATE、datetime now()）——
//     否则 ADD COLUMN ... NOT NULL 在非空表上必被 PG 拒绝。
func defaultClause(field manifest.Field) (literal string, source string, hasDefault bool, err error) {
	if field.Default != nil {
		value, err := specDefaultLiteral(field)
		if err != nil {
			return "", "", false, err
		}
		return value, defaultSourceSpec, true, nil
	}
	if !field.Required {
		return "", defaultSourceNone, false, nil
	}
	switch field.Type {
	case "string", "text", "enum":
		return "''", defaultSourceZero, true, nil
	case "int", "bigint", "decimal":
		return "0", defaultSourceZero, true, nil
	case "bool":
		return "0", defaultSourceZero, true, nil
	case "date":
		return "CURRENT_DATE", defaultSourceZero, true, nil
	case "datetime":
		return "now()", defaultSourceZero, true, nil
	}
	return "", "", false, fmt.Errorf("未知类型 %q（无零值默认可补）", field.Type)
}

// specDefaultLiteral 把 spec.default 转成 SQL 字面量（字符串转义；数值必须是有限数；
// bool 列落 0/1；date/datetime 以字符串字面量交给 PG 解析）
func specDefaultLiteral(field manifest.Field) (string, error) {
	switch field.Type {
	case "bool":
		value, err := toBool01(field.Default)
		if err != nil {
			return "", fmt.Errorf("default: %w", err)
		}
		return strconv.FormatInt(value, 10), nil
	case "int", "bigint":
		value, err := toInt64(field.Default)
		if err != nil {
			return "", fmt.Errorf("default: %w", err)
		}
		return strconv.FormatInt(value, 10), nil
	case "decimal":
		text, err := toText(field.Default)
		if err != nil {
			return "", fmt.Errorf("default: %w", err)
		}
		if _, err := parseFinite(text); err != nil {
			return "", fmt.Errorf("default: %w", err)
		}
		return strings.TrimSpace(text), nil
	default:
		text, err := toText(field.Default)
		if err != nil {
			return "", fmt.Errorf("default: %w", err)
		}
		return quoteLiteral(text), nil
	}
}

// parseFinite 数值字面量校验（拒绝 NaN/Inf 与非数字：「看起来像值」的注入面）
func parseFinite(text string) (float64, error) {
	text = strings.TrimSpace(text)
	value, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return 0, fmt.Errorf("%q 不是合法数值", text)
	}
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, fmt.Errorf("%q 不是有限数值", text)
	}
	return value, nil
}

// quoteLiteral SQL 单引号字面量（内部单引号翻倍）——只用于 DDL 里无法参数化的位置（DEFAULT / COMMENT）
func quoteLiteral(text string) string {
	return "'" + strings.ReplaceAll(text, "'", "''") + "'"
}

// ---------- 索引（一次到位；命名带前缀并做长度预算与截断兜底） ----------

// indexStatements 该实体的全部索引语句：unique 部分唯一、index:true、trgm GIN、weigh（drag_sort）、软删列
func (e *Engine) indexStatements(spec *Spec, table string) ([]Statement, error) {
	quotedTable, err := e.quoteTable(table)
	if err != nil {
		return nil, err
	}
	deletedAt, err := e.quote(ColumnDeletedAt)
	if err != nil {
		return nil, err
	}
	prefix := prefixOf()
	statements := make([]Statement, 0, len(spec.Fields())+3)

	for _, field := range spec.Fields() {
		column, err := e.quote(field.Name)
		if err != nil {
			return nil, err
		}
		if field.Unique {
			name, err := indexName(prefix, spec.Plugin, spec.Name, "uk", field.Name)
			if err != nil {
				return nil, err
			}
			quotedName, err := e.quoteIndexName(name)
			if err != nil {
				return nil, err
			}
			statements = append(statements, Statement{Kind: KindCreateIndex, Text: fmt.Sprintf(
				"CREATE UNIQUE INDEX IF NOT EXISTS %s ON %s (%s) WHERE %s IS NULL",
				quotedName, quotedTable, column, deletedAt)})
		}
		if field.Index {
			name, err := indexName(prefix, spec.Plugin, spec.Name, "idx", field.Name)
			if err != nil {
				return nil, err
			}
			quotedName, err := e.quoteIndexName(name)
			if err != nil {
				return nil, err
			}
			statements = append(statements, Statement{Kind: KindCreateIndex, Text: fmt.Sprintf(
				"CREATE INDEX IF NOT EXISTS %s ON %s (%s)", quotedName, quotedTable, column)})
		}
		if field.Trgm {
			name, err := indexName(prefix, spec.Plugin, spec.Name, "idx", field.Name+"_trgm")
			if err != nil {
				return nil, err
			}
			quotedName, err := e.quoteIndexName(name)
			if err != nil {
				return nil, err
			}
			statements = append(statements, Statement{Kind: KindCreateIndex, Text: fmt.Sprintf(
				"CREATE INDEX IF NOT EXISTS %s ON %s USING GIN (%s gin_trgm_ops)",
				quotedName, quotedTable, column)})
		}
	}

	if spec.DragSort() {
		name, err := indexName(prefix, spec.Plugin, spec.Name, "idx", ColumnWeigh)
		if err != nil {
			return nil, err
		}
		quotedName, err := e.quoteIndexName(name)
		if err != nil {
			return nil, err
		}
		weigh, err := e.quote(ColumnWeigh)
		if err != nil {
			return nil, err
		}
		statements = append(statements, Statement{Kind: KindCreateIndex, Text: fmt.Sprintf(
			"CREATE INDEX IF NOT EXISTS %s ON %s (%s)", quotedName, quotedTable, weigh)})
	}

	deletedName, err := indexName(prefix, spec.Plugin, spec.Name, "idx", ColumnDeletedAt)
	if err != nil {
		return nil, err
	}
	quotedDeletedName, err := e.quoteIndexName(deletedName)
	if err != nil {
		return nil, err
	}
	statements = append(statements, Statement{Kind: KindCreateIndex, Text: fmt.Sprintf(
		"CREATE INDEX IF NOT EXISTS %s ON %s (%s)", quotedDeletedName, quotedTable, deletedAt)})

	return statements, nil
}

// indexName 索引名：<前缀><kind>_plugin_<名>_<实体>_<后缀>；超过 PG 63 字节上限时退化为
// 「截断 + 8 位 sha256」（兜底形态，如 <前缀>idx_plugin_demo_items_abcd1234，确定性可复算）；
// 截断形态仍超限就报错——**绝不**让 PG 静默截断标识符（静默截断会造成跨实体的索引名碰撞）。
// 注：最终物理名是 (前缀, 名, 实体, kind, 后缀) 的纯函数，可随时复算；dry-run 计划里逐条列出，
// 故不需要把结果回写元数据（「把最终物理名写回元数据」由此覆盖）。
func indexName(prefix, plugin, entity, kind, suffix string) (string, error) {
	base := prefix + kind + "_plugin_" + plugin + "_" + entity + "_"
	natural := base + suffix
	if len(natural) <= pgIdentifierMaxBytes {
		return natural, nil
	}
	sum := sha256.Sum256([]byte(suffix))
	truncated := base + hex.EncodeToString(sum[:])[:8]
	if len(truncated) > pgIdentifierMaxBytes {
		return "", fmt.Errorf("索引名过长（%s，%d 字节）：前缀+插件名+实体名已吃掉截断兜底的余量——请缩短插件名或实体名（长度预算）",
			truncated, len(truncated))
	}
	return truncated, nil
}

// quoteIndexName 引用索引名（索引名含配置前缀，字符集与表名同口径；仍过 gorm 的 Statement.Quote）
func (e *Engine) quoteIndexName(name string) (string, error) {
	if !tablePattern.MatchString(name) {
		return "", fmt.Errorf("索引名 %q 不在白名单内（^[A-Za-z_][A-Za-z0-9_]{0,62}$）", name)
	}
	return e.quoteIdent(name), nil
}

// engineIndexName 索引名是否由引擎为本实体生成（前缀 + kind + "_plugin_" + 名 + "_" + 实体 + "_"）。
// 引擎索引名是 (前缀, 名, 实体, kind, 后缀) 的纯函数，截断兜底形态保持同一前缀 ⇒ 按前缀即可与
// 手写 / 插件迁移创建 / 主键索引区分；只有落在公式内的名字才允许被「删索引」对齐，不猜归属。
func engineIndexName(prefix, plugin, entity, name string) bool {
	for _, kind := range []string{"idx", "uk"} {
		if strings.HasPrefix(name, prefix+kind+"_plugin_"+plugin+"_"+entity+"_") {
			return true
		}
	}
	return false
}

// indexNameOf 从索引语句文本里取回索引名（与自省结果比对用；文本由本文件生成，形如
// `CREATE [UNIQUE] INDEX IF NOT EXISTS <name> ON ...`）
func indexNameOf(statement string) string {
	const marker = "EXISTS "
	index := strings.Index(statement, marker)
	if index < 0 {
		return ""
	}
	rest := statement[index+len(marker):]
	if space := strings.IndexAny(rest, " \n"); space > 0 {
		rest = rest[:space]
	}
	return strings.Trim(rest, `"`)
}

// ---------- 注释（COMMENT ON 一次到位；含 dict 的状态列口径） ----------

// commentStatements 表注释（fields 为空时）+ 列注释；传 fields 时只注释这些列（加列路径）
func (e *Engine) commentStatements(spec *Spec, table string, fields ...manifest.Field) ([]Statement, error) {
	quotedTable, err := e.quoteTable(table)
	if err != nil {
		return nil, err
	}
	// 传了字段 ⇒ 只给这些列发注释（加列路径）
	if len(fields) > 0 {
		statements := make([]Statement, 0, len(fields))
		for _, field := range fields {
			column, err := e.quote(field.Name)
			if err != nil {
				return nil, err
			}
			statements = append(statements, Statement{Kind: KindComment, Text: fmt.Sprintf(
				"COMMENT ON COLUMN %s.%s IS %s", quotedTable, column, quoteLiteral(columnComment(field)))})
		}
		return statements, nil
	}

	statements := make([]Statement, 0, len(spec.Fields())+6)
	statements = append(statements, Statement{Kind: KindComment, Text: fmt.Sprintf(
		"COMMENT ON TABLE %s IS %s", quotedTable,
		quoteLiteral(fmt.Sprintf("插件 %s 的 L2 实体 %s（插件系统引擎生成；ddl_owner=%s）",
			spec.Plugin, spec.Name, spec.DDLOwner())))})

	column := func(name, comment string) error {
		quoted, err := e.quote(name)
		if err != nil {
			return err
		}
		statements = append(statements, Statement{Kind: KindComment, Text: fmt.Sprintf(
			"COMMENT ON COLUMN %s.%s IS %s", quotedTable, quoted, quoteLiteral(comment))})
		return nil
	}
	if err := column(ColumnID, "ID"); err != nil {
		return nil, err
	}
	for _, field := range spec.Fields() {
		if err := column(field.Name, columnComment(field)); err != nil {
			return nil, err
		}
	}
	if spec.DragSort() {
		if err := column(ColumnWeigh, "排序权重（拖动排序；drag_sort=true）"); err != nil {
			return nil, err
		}
	}
	if err := column(ColumnDeletedAt, "软删时间:delete 动作为软删（界面不提供硬删）"); err != nil {
		return nil, err
	}
	if err := column(ColumnCreatedAt, "创建时间"); err != nil {
		return nil, err
	}
	if err := column(ColumnUpdatedAt, "更新时间"); err != nil {
		return nil, err
	}
	return statements, nil
}

// columnComment 列注释：中文标题 +（声明 dict 时）值→文案对照，形如 `状态:0=停用,1=启用`
// （与宿主状态列的注释口径一致，见 cmd/migrate/migrations/000005_plugin.up.sql）
func columnComment(field manifest.Field) string {
	comment := strings.TrimSpace(field.TitleZh)
	if comment == "" {
		comment = strings.TrimSpace(field.TitleEn)
	}
	if len(field.Dict) == 0 {
		return comment
	}
	pairs := make([]string, 0, len(field.Dict))
	for _, item := range field.Dict {
		label := strings.TrimSpace(item.LabelZh)
		if label == "" {
			label = strings.TrimSpace(item.LabelEn)
		}
		pairs = append(pairs, fmt.Sprintf("%v=%s", item.Value, label))
	}
	return comment + ":" + strings.Join(pairs, ",")
}

// isEngineColumn 引擎自管列
func isEngineColumn(name string) bool {
	for _, column := range engineColumns {
		if column == name {
			return true
		}
	}
	return false
}
