package l2

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/ai-go-hub/ai-go-admin/internal/infra/database"
	"github.com/ai-go-hub/ai-go-admin/internal/plugin/manifest"
	"github.com/ai-go-hub/ai-go-admin/internal/service"

	"gorm.io/gorm"
)

// Engine 引擎句柄。持一个显式注入的 *gorm.DB（生产走 Default()，测试注入 DryRun 或真实库）。
type Engine struct {
	db *gorm.DB
}

// New 构造引擎（db 为 nil 时所有动作返回可读错误，不 panic）
func New(db *gorm.DB) *Engine { return &Engine{db: db} }

// Default 生产入口：取全局连接（database.Init 之后有效，与既有四层同源）
func Default() *Engine { return &Engine{db: database.DB()} }

// DB 当前连接（可能为 nil：未初始化）
func (e *Engine) DB() *gorm.DB {
	if e == nil {
		return nil
	}
	return e.db
}

// guard 连接可用性检查（各动作入口先过它，避免把 nil 连接错误抛成 panic）
func (e *Engine) guard() error {
	if e.DB() == nil {
		return fmt.Errorf("插件 L2 引擎未接入数据库（须在 database.Init() 之后使用）")
	}
	return nil
}

// ---------- 标识符安全（硬条款 ①②：白名单 + 正则复核 + Statement.Quote；值全参数化） ----------

// quoteIdent 纯引用（入参已过字符集白名单；表名/索引名含配置前缀，不走列名正则）。
// 引用入口只有 gorm 的 Statement.Quote/QuoteTo（clause 包没有 Quote，原文提示）。
func (e *Engine) quoteIdent(name string) string {
	return (&gorm.Statement{DB: e.DB()}).Quote(name)
}

// quote 引用一个标识符：先过闭集正则，再用 gorm 的 Statement.Quote 加引号。
func (e *Engine) quote(name string) (string, error) {
	if err := e.guard(); err != nil {
		return "", err
	}
	if !identifierPattern.MatchString(name) {
		return "", fmt.Errorf("标识符 %q 不在白名单内（^[a-z][a-z0-9_]{0,62}$）", name)
	}
	return e.quoteIdent(name), nil
}

// quoteTable 引用物理表名（DDL 文本拼接用：CREATE/ALTER/DROP 需要「已引用」的形态）。
// 查询路径请用 tableName —— gorm 的 db.Table() 会自行引用名字，喂已引用的名字会被二次引用。
func (e *Engine) quoteTable(table string) (string, error) {
	if err := e.guard(); err != nil {
		return "", err
	}
	if !tablePattern.MatchString(table) {
		return "", fmt.Errorf("物理表名 %q 不在白名单内（^[A-Za-z_][A-Za-z0-9_]{0,62}$）", table)
	}
	return e.quoteIdent(table), nil
}

// tableName 校验物理表名字符集并返回**裸名**：db.Table(裸名) 由 gorm 自行 Quote
// （chainable_api.go 的 Table → Statement.Quote；GORM 写法即此路径）。
func (e *Engine) tableName(spec *Spec) (string, error) {
	if err := e.guard(); err != nil {
		return "", err
	}
	name := spec.PhysicalTableName()
	if !tablePattern.MatchString(name) {
		return "", fmt.Errorf("物理表名 %q 不在白名单内（^[A-Za-z_][A-Za-z0-9_]{0,62}$）", name)
	}
	return name, nil
}

// quoteColumns 批量引用（保持入参顺序）
func (e *Engine) quoteColumns(columns []string) ([]string, error) {
	quoted := make([]string, 0, len(columns))
	for _, column := range columns {
		item, err := e.quote(column)
		if err != nil {
			return nil, err
		}
		quoted = append(quoted, item)
	}
	return quoted, nil
}

// ---------- 条件构造（硬条款 ⑥：闭集 = 宿主词表；未知词拒绝而非静默丢弃） ----------

// maxInValues IN/NOT IN 的元素上限（SQL 纪律：请求路径上的条件不得无界增长）
const maxInValues = 1000

// whereClause 组装完整条件片段：软删过滤（列表/计数/排序/更新/删除一律只看未删行）+
// 各 WhereGroup（组间 AND，组内按 Or 决定 OR/AND，与宿主 BuildWhereScopes 同语义）。
// 返回值是「已引用标识符 + 位置占位符」的 SQL 片段与参数（值全参数化，硬条款 ②）。
func (e *Engine) whereClause(spec *Spec, groups []service.WhereGroup) (string, []any, error) {
	deletedAt, err := e.quote(ColumnDeletedAt)
	if err != nil {
		return "", nil, err
	}
	parts := []string{deletedAt + " IS NULL"}
	args := make([]any, 0, len(groups))

	for _, group := range groups {
		if len(group.Wheres) == 0 {
			continue
		}
		conditions := make([]string, 0, len(group.Wheres))
		groupArgs := make([]any, 0, len(group.Wheres))
		for _, where := range group.Wheres {
			condition, values, err := e.buildCondition(spec, where)
			if err != nil {
				return "", nil, err
			}
			conditions = append(conditions, condition)
			groupArgs = append(groupArgs, values...)
		}
		if len(conditions) == 0 {
			continue
		}
		joiner := " AND "
		if group.Or {
			joiner = " OR "
		}
		parts = append(parts, "("+strings.Join(conditions, joiner)+")")
		args = append(args, groupArgs...)
	}
	return strings.Join(parts, " AND "), args, nil
}

// buildCondition 单条条件：字段必须可作条件（声明了 search 或 quickSearch），操作符必须在宿主
// 词表内且与类型兼容，值按字段类型收敛后参数化。
func (e *Engine) buildCondition(spec *Spec, where service.Where) (string, []any, error) {
	fieldName := strings.TrimSpace(where.Field)
	field, ok := spec.Filterable(fieldName)
	if !ok {
		return "", nil, fmt.Errorf("字段 %q 不支持条件查询（可作条件的字段需声明 search 或 quickSearch；本实体允许：%s）",
			where.Field, strings.Join(spec.FilterableNames(), "|"))
	}

	op, err := spec.checkOperatorAllowed(field, where.Operator)
	if err != nil {
		return "", nil, err
	}

	quoted, err := e.quote(fieldName)
	if err != nil {
		return "", nil, err
	}

	switch op {
	case "NULL":
		return quoted + " IS NULL", nil, nil
	case "NOT NULL":
		return quoted + " IS NOT NULL", nil, nil
	case "BETWEEN", "NOT BETWEEN":
		values, err := splitRangeValues(where.Value)
		if err != nil {
			return "", nil, fmt.Errorf("字段 %s 的 %s 需要两个值: %w", fieldName, op, err)
		}
		low, err := coerceValue(field, values[0])
		if err != nil {
			return "", nil, fmt.Errorf("字段 %s 的下界: %w", fieldName, err)
		}
		high, err := coerceValue(field, values[1])
		if err != nil {
			return "", nil, fmt.Errorf("字段 %s 的上界: %w", fieldName, err)
		}
		return quoted + " " + op + " ? AND ?", []any{low, high}, nil
	case "IN", "NOT IN":
		values, err := splitInValues(where.Value)
		if err != nil {
			return "", nil, fmt.Errorf("字段 %s 的 %s: %w", fieldName, op, err)
		}
		placeholders := make([]string, 0, len(values))
		args := make([]any, 0, len(values))
		for _, value := range values {
			coerced, err := coerceValue(field, value)
			if err != nil {
				return "", nil, fmt.Errorf("字段 %s 的 %s 元素: %w", fieldName, op, err)
			}
			placeholders = append(placeholders, "?")
			args = append(args, coerced)
		}
		return quoted + " " + op + " (" + strings.Join(placeholders, ", ") + ")", args, nil
	}

	// 其余词条走宿主同款映射（internal/service/base.go 的 GetOperatorByAlias），模糊类在 L2 内
	// 一律前缀匹配（kw%）；声明 trgm 的字段才前后通配（%kw%），「模糊搜索语义」。
	sqlOp := service.GetOperatorByAlias(op)
	if sqlOp == "" {
		return "", nil, fmt.Errorf("操作符 %q 无法映射为 SQL 运算符（宿主词表外的写法一律拒绝）", op)
	}

	value, err := coerceValue(field, where.Value)
	if err != nil {
		return "", nil, fmt.Errorf("字段 %s: %w", fieldName, err)
	}
	if isFuzzyOperator(op) {
		value = fuzzyPattern(fmt.Sprint(value), field.Trgm)
	}
	return quoted + " " + sqlOp + " ?", []any{value}, nil
}

// isFuzzyOperator 模糊匹配类（LIKE / NOT LIKE / ILIKE / NOT ILIKE）
func isFuzzyOperator(op string) bool {
	return op == "LIKE" || op == "NOT LIKE" || op == "ILIKE" || op == "NOT ILIKE"
}

// fuzzyPattern 模糊搜索的值形态：默认**前缀匹配**（kw%）；声明 trgm 的字段走前后通配（%kw%）。
// 已知差异（如实登记）：宿主 CRUD 给无 % 的值自动补成 %kw%（internal/service/base.go:543-546），
// L2 不照抄——SQL 纪律禁止大表裸前后通配；需要前后通配必须声明 trgm（引擎建 pg_trgm GIN 索引）。
func fuzzyPattern(keyword string, trgm bool) string {
	keyword = strings.TrimSpace(keyword)
	if trgm {
		return "%" + keyword + "%"
	}
	return keyword + "%"
}

// splitRangeValues BETWEEN 的两个值：宿主口径是 "a,b" 字符串（internal/service/base.go 的
// BuildLeafExpr），也接受长度 2 的数组（前端 comSearch 可能发数组）。
func splitRangeValues(value any) ([]any, error) {
	switch v := value.(type) {
	case []any:
		if len(v) != 2 {
			return nil, fmt.Errorf("需要 2 个值，实际 %d 个", len(v))
		}
		return v, nil
	case string:
		parts := strings.SplitN(v, ",", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("需要 \"下界,上界\" 形式的字符串，实际 %q", v)
		}
		return []any{strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])}, nil
	default:
		return nil, fmt.Errorf("需要 \"下界,上界\" 字符串或长度 2 的数组")
	}
}

// splitInValues IN/NOT IN 的取值列表：数组或逗号分隔字符串；元素数 1..maxInValues（有界）
func splitInValues(value any) ([]any, error) {
	var values []any
	switch v := value.(type) {
	case []any:
		values = v
	case string:
		for _, part := range strings.Split(v, ",") {
			values = append(values, strings.TrimSpace(part))
		}
	default:
		return nil, fmt.Errorf("需要数组或逗号分隔字符串")
	}
	if len(values) == 0 {
		return nil, fmt.Errorf("取值列表为空")
	}
	if len(values) > maxInValues {
		return nil, fmt.Errorf("取值列表过长（%d 项，上限 %d）", len(values), maxInValues)
	}
	return values, nil
}

// ---------- 值类型收敛（请求体里的 JSON 值 → 与列类型相称的 Go 值；值仍全参数化） ----------

// coerceValue 按字段类型收敛查询条件里的值。收敛只做「形状检查 + 类型对齐」，不做业务校验。
func coerceValue(field manifest.Field, value any) (any, error) {
	if value == nil {
		return nil, fmt.Errorf("值为 null（判定为空请用 NULL / NOT NULL 操作符）")
	}
	switch field.Type {
	case "int", "bigint":
		number, err := toInt64(value)
		if err != nil {
			return nil, err
		}
		return number, nil
	case "bool":
		flag, err := toBool01(value)
		if err != nil {
			return nil, err
		}
		return flag, nil
	case "decimal", "date", "datetime", "string", "text", "enum":
		return toText(value)
	default:
		return nil, fmt.Errorf("未知字段类型 %q", field.Type)
	}
}

// toText 收敛为字符串（decimal/date/datetime 以文本形态交给驱动，避免精度与格式损失）
func toText(value any) (string, error) {
	switch v := value.(type) {
	case string:
		return v, nil
	case bool:
		if v {
			return "1", nil
		}
		return "0", nil
	case int:
		return strconv.Itoa(v), nil
	case int64:
		return strconv.FormatInt(v, 10), nil
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64), nil
	case map[string]any, []any:
		return "", fmt.Errorf("值必须是标量（实际是对象或数组）")
	default:
		return fmt.Sprint(v), nil
	}
}

// toInt64 收敛为整数
func toInt64(value any) (int64, error) {
	switch v := value.(type) {
	case int:
		return int64(v), nil
	case int64:
		return v, nil
	case json.Number:
		if number, err := v.Int64(); err == nil {
			return number, nil
		}
		asFloat, err := v.Float64()
		if err != nil || asFloat != math.Trunc(asFloat) {
			return 0, fmt.Errorf("值 %s 不是整数", v.String())
		}
		return int64(asFloat), nil
	case bool:
		if v {
			return 1, nil
		}
		return 0, nil
	case float64:
		if v != math.Trunc(v) {
			return 0, fmt.Errorf("值 %v 不是整数", v)
		}
		return int64(v), nil
	case string:
		number, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		if err != nil {
			return 0, fmt.Errorf("值 %q 不是整数", v)
		}
		return number, nil
	case map[string]any, []any:
		return 0, fmt.Errorf("值必须是标量（实际是对象或数组）")
	default:
		return 0, fmt.Errorf("值 %v 不是整数", v)
	}
}

// toBool01 收敛为 0/1（L2 的二值列是 smallint 0/1，与宿主状态列口径一致）
func toBool01(value any) (int64, error) {
	switch v := value.(type) {
	case bool:
		if v {
			return 1, nil
		}
		return 0, nil
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "1", "true":
			return 1, nil
		case "0", "false":
			return 0, nil
		}
		return 0, fmt.Errorf("值 %q 不是 0/1 或 true/false", v)
	default:
		number, err := toInt64(value)
		if err != nil {
			return 0, err
		}
		if number != 0 && number != 1 {
			return 0, fmt.Errorf("值 %d 不是 0/1", number)
		}
		return number, nil
	}
}

// ---------- 排序（硬条款 ④：白名单 + 主键 DESC 托底；白名单外拒绝，不静默跳过） ----------

// buildOrder 生成 ORDER BY 片段（已引用标识符）。无用户排序字段时只留主键托底——分页稳定性由它保证
// （与宿主 BuildOrderScope 的托底语义一致）。
func (e *Engine) buildOrder(spec *Spec, sortField, sortOrder string) (string, error) {
	field := strings.TrimSpace(sortField)
	clauses := make([]string, 0, 2)

	if field != "" {
		if !spec.Sortable(field) {
			return "", fmt.Errorf("排序字段 %q 不在白名单内（允许：%s）；白名单外一律拒绝——静默跳过会让调用方以为排序已生效",
				field, strings.Join(spec.SortableNames(), "|"))
		}
		quoted, err := e.quote(field)
		if err != nil {
			return "", err
		}
		direction := "ASC"
		if strings.EqualFold(strings.TrimSpace(sortOrder), "desc") {
			direction = "DESC"
		}
		clauses = append(clauses, quoted+" "+direction)
	}

	if field != ColumnID {
		primary, err := e.quote(ColumnID)
		if err != nil {
			return "", err
		}
		clauses = append(clauses, primary+" DESC")
	}
	return strings.Join(clauses, ", "), nil
}

// ---------- 分页（硬条款 ③：复用宿主 service.BuildPaginateScope；上限自持，maxPageLimit=1000） ----------

// maxPageLimit 本机制自持的每页条数硬上限。宿主列表本身不设上限（有意为批量导出保留），
// 用户可控 limit 的收敛由本包负责，不改变宿主行为。
const maxPageLimit = 1000

// normalizePaginate 收敛页码与每页条数（规则与 service.BuildPaginateScope 一致：
// page<1→1、limit<1→10、limit>maxPageLimit→maxPageLimit）。列表动作另需要 offset 计算 has_more，
// 故在此显式复算一遍——真正落到 SQL 上的 LIMIT/OFFSET 仍由 BuildPaginateScope 生成（单一实现）。
func normalizePaginate(page, limit int) (int, int) {
	if page < 1 {
		page = 1
	}
	if limit < 1 {
		limit = 10
	}
	if limit > maxPageLimit {
		limit = maxPageLimit
	}
	return page, limit
}

// paginateScope 把宿主的分页 scope 套到查询链上（scope 是 Statement 级实现，Generics API 与传统 API
// 共用同一份判定；此处只做形状转换）。limit 先经 normalizePaginate 收敛再交给宿主——本机制自持
// 「limit 不得直穿数据库」的保证，不依赖宿主 BuildPaginateScope 是否带上限钳制。
func paginateScope(page, limit int) func(*gorm.DB) *gorm.DB {
	page, limit = normalizePaginate(page, limit)
	scope := service.BuildPaginateScope(page, limit)
	if scope == nil {
		return nil
	}
	return func(tx *gorm.DB) *gorm.DB {
		scope(tx.Statement)
		return tx
	}
}

// countQuery 计数（与列表同一套条件；不含排序与分页）
func (e *Engine) countQuery(spec *Spec, condition string, args []any) (int64, error) {
	table, err := e.tableName(spec)
	if err != nil {
		return 0, err
	}
	var total int64
	if err := e.DB().Table(table).Where(condition, args...).Count(&total).Error; err != nil {
		return 0, fmt.Errorf("统计总数失败: %w", err)
	}
	return total, nil
}

// selectRow 单条读取（get 动作；显式列，不 SELECT *）
func (e *Engine) selectRow(spec *Spec, columns []string, condition string, args []any) (map[string]any, error) {
	table, err := e.tableName(spec)
	if err != nil {
		return nil, err
	}
	quoted, err := e.quoteColumns(columns)
	if err != nil {
		return nil, err
	}
	var rows []map[string]any
	if err := e.DB().Table(table).Select(quoted).Where(condition, args...).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("读取记录失败: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return rows[0], nil
}

// orderClause 把 ORDER BY 片段挂到查询上（GORM 的 Order(string) 按原样引用 ⇒ 片段必须已引用标识符）
func orderScope(order string) func(*gorm.DB) *gorm.DB {
	return func(tx *gorm.DB) *gorm.DB {
		if order != "" {
			return tx.Order(order)
		}
		return tx
	}
}

// softDeleteCondition 软删过滤片段（条件拼装的最小单元：update/delete 复用）
func (e *Engine) softDeleteCondition() (string, error) {
	deletedAt, err := e.quote(ColumnDeletedAt)
	if err != nil {
		return "", err
	}
	return deletedAt + " IS NULL", nil
}
