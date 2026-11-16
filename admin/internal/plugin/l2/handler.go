package l2

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"slices"
	"strings"

	"github.com/ai-go-hub/ai-go-admin/internal/kit/httpx"
	"github.com/ai-go-hub/ai-go-admin/internal/plugin/manifest"
	"github.com/ai-go-hub/ai-go-admin/internal/service"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// 六动作（handler.go 行 /；契约对齐 F5/F9）：
//
//	POST /list        列表（分页 + 白名单排序 + 条件闭集；响应带列/表单元数据）
//	GET  /get/:pk     单条（只读未删行）
//	POST /create      新增
//	POST /update/:pk  更新（**必须带 :pk**：前端 TableManagerAPI.post 拼的是 ${url}/${pk}，F9）
//	POST /delete      软删（UPDATE deleted_at = now()；界面不提供硬删）
//	POST /sort        拖动排序（仅 drag_sort: true 的实体注册）
//
// 请求体形状与宿主四层同款（internal/handler/base.go 的 Request/SortRequest/DeleteRequest）：
// 前端与 L2 通用页共用一套表格组件，契约一致才不会出现「宿主发的条件 L2 不认」。

// listRequest 列表请求（形状对齐 internal/handler.Request 的列表用法 + lang：
// 「页面把当前 locale 作为 lang 参数随请求下发」；未带则按中文）
type listRequest struct {
	Page   int                  `json:"page"`
	Limit  int                  `json:"limit"`
	Sort   string               `json:"sort"`
	Order  string               `json:"order"`
	Wheres []service.WhereGroup `json:"wheres"`
	Lang   string               `json:"lang"`
}

// deleteRequest 软删请求（对齐宿主 handler.DeleteRequest：pks 必填且至少一条）
type deleteRequest struct {
	Pks []string `json:"pks" binding:"required,min=1"`
}

// sortRequest 拖动排序请求（对齐宿主 handler.SortRequest 的契约 {move,target,direction,weigh}）
type sortRequest struct {
	Page      int                  `json:"page"`
	Limit     int                  `json:"limit"`
	Sort      string               `json:"sort"`
	Order     string               `json:"order"`
	Wheres    []service.WhereGroup `json:"wheres"`
	Move      string               `json:"move" binding:"required"`
	Target    string               `json:"target" binding:"required"`
	Direction string               `json:"direction" binding:"required,oneof=up down"`
	Weigh     int64                `json:"weigh"`
}

// ColumnMeta 列表列元数据（前端按此渲染列；dict 统一走 Meta.Dicts，避免两处真相）
type ColumnMeta struct {
	Prop        string            `json:"prop"`
	Label       string            `json:"label"`
	Width       int               `json:"width,omitempty"`
	Sortable    bool              `json:"sortable,omitempty"`
	Render      string            `json:"render,omitempty"`   // switch（0/1 二值列）/ tag（枚举列）
	Custom      map[string]string `json:"custom,omitempty"`   // tag 列的值→颜色（AGENTS 状态列唯一写法）
	Operator    string            `json:"operator,omitempty"` // 该列的默认搜索操作符（spec.search.op）
	QuickSearch bool              `json:"quickSearch,omitempty"`
}

// FormMeta 表单控件元数据（控件映射闭集：type/form 分派）
type FormMeta struct {
	Prop     string `json:"prop"`
	Label    string `json:"label"`
	Type     string `json:"type"`
	Required bool   `json:"required,omitempty"`
	Length   int    `json:"length,omitempty"`
}

// Meta 元数据（形状：title / columns / form / dicts；dicts 是 Record<值, 文案>）
type Meta struct {
	Plugin   string                       `json:"plugin"`
	Entity   string                       `json:"entity"`
	Title    string                       `json:"title"`
	DragSort bool                         `json:"drag_sort"`
	Columns  []ColumnMeta                 `json:"columns"`
	Form     []FormMeta                   `json:"form"`
	Dicts    map[string]map[string]string `json:"dicts"`
}

// entityHandler 单个实体的六动作执行器（路由注册时按 (plugin, entity) 构造一次；元数据按请求解析，
// 字段/文案/校验变更即时生效——新增实体才需要重启）
type entityHandler struct {
	engine *Engine
	plugin string
	entity string
	// resolver 测试注入点（生产恒为 nil ⇒ 走 engine.Resolve 的按请求读库）
	resolver func(ctx context.Context) (*Spec, error)
}

// resolve 解析实体运行期定义（元数据不存在 ⇒ ErrNotInstalled ⇒ 404，前端据此显示空态）
func (h *entityHandler) resolve(ctx context.Context) (*Spec, error) {
	if h.resolver != nil {
		return h.resolver(ctx)
	}
	return h.engine.Resolve(ctx, h.plugin, h.entity)
}

// fail 统一失败出口：元数据缺失回 404（对齐 host.Gate 的不承认存在性），其余回业务失败
func (h *entityHandler) fail(c *gin.Context, err error) {
	if errors.Is(err, ErrNotInstalled) {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	httpx.Fail(c, httpx.WithMessage(err.Error()))
}

// List 列表：分页（上限 1000）+ 白名单排序 + 条件闭集，响应带元数据（meta+list+total+has_more）
func (h *entityHandler) List(c *gin.Context) {
	var req listRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Fail(c, httpx.WithMessage("参数错误: "+err.Error()))
		return
	}
	if lang := strings.TrimSpace(c.Query("lang")); lang != "" && req.Lang == "" {
		req.Lang = lang
	}

	ctx := c.Request.Context()
	spec, err := h.resolve(ctx)
	if err != nil {
		h.fail(c, err)
		return
	}

	condition, args, err := h.engine.whereClause(spec, req.Wheres)
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("查询条件错误: "+err.Error()))
		return
	}
	order, err := h.engine.buildOrder(spec, req.Sort, req.Order)
	if err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}

	table, err := h.engine.tableName(spec)
	if err != nil {
		h.fail(c, err)
		return
	}
	columns, err := h.engine.quoteColumns(spec.ListColumns())
	if err != nil {
		h.fail(c, err)
		return
	}

	page, limit := normalizePaginate(req.Page, req.Limit)
	rows := make([]map[string]any, 0, limit)
	query := h.engine.DB().WithContext(ctx).Table(table).Select(columns).
		Where(condition, args...).Scopes(orderScope(order))
	if scope := paginateScope(page, limit); scope != nil {
		query = query.Scopes(scope)
	}
	if err := query.Find(&rows).Error; err != nil {
		httpx.Fail(c, httpx.WithMessage("查询失败: "+err.Error()))
		return
	}

	total, err := h.engine.countQuery(spec, condition, args)
	if err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}

	httpx.Success(c, httpx.WithData(gin.H{
		"meta":     BuildMeta(spec, req.Lang),
		"list":     rows,
		"total":    total,
		"has_more": int64((page-1)*limit+len(rows)) < total,
	}))
}

// Get 单条（显式列；软删行视为不存在——与列表口径一致）
func (h *entityHandler) Get(c *gin.Context) {
	ctx := c.Request.Context()
	spec, err := h.resolve(ctx)
	if err != nil {
		h.fail(c, err)
		return
	}
	pk, err := parseIntPK(c.Param("pk"))
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("参数错误: "+err.Error()))
		return
	}

	condition, args, err := h.rowCondition(spec, pk)
	if err != nil {
		h.fail(c, err)
		return
	}
	row, err := h.engine.selectRow(spec, spec.RowColumns(), condition, args)
	if err != nil {
		h.fail(c, err)
		return
	}
	if row == nil {
		httpx.Fail(c, httpx.WithMessage("记录不存在"))
		return
	}
	httpx.Success(c, httpx.WithData(gin.H{"row": row}))
}

// Create 新增（请求体只认 spec 字段；必填/长度在写入前校验，不给数据库抛原始约束错误）
func (h *entityHandler) Create(c *gin.Context) {
	ctx := c.Request.Context()
	spec, err := h.resolve(ctx)
	if err != nil {
		h.fail(c, err)
		return
	}
	body, err := decodeBody(c)
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("参数错误: "+err.Error()))
		return
	}
	columns, args, err := spec.collectValues(body, true)
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("参数错误: "+err.Error()))
		return
	}
	if len(columns) == 0 {
		httpx.Fail(c, httpx.WithMessage("参数错误: 请求体没有任何可写字段"))
		return
	}

	table, err := h.engine.quoteTable(spec.PhysicalTableName())
	if err != nil {
		h.fail(c, err)
		return
	}
	quoted, err := h.engine.quoteColumns(columns)
	if err != nil {
		h.fail(c, err)
		return
	}
	placeholders := make([]string, len(columns))
	for i := range placeholders {
		placeholders[i] = "?"
	}
	sql := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", table, strings.Join(quoted, ", "), strings.Join(placeholders, ", "))
	if err := h.engine.DB().WithContext(ctx).Exec(sql, args...).Error; err != nil {
		httpx.Fail(c, httpx.WithMessage("创建失败: "+err.Error()))
		return
	}
	httpx.Success(c)
}

// Update 更新（:pk 由路由提供；只更新请求体里出现的字段，与宿主 Tri.Map 的「所见即所得」同语义）
func (h *entityHandler) Update(c *gin.Context) {
	ctx := c.Request.Context()
	spec, err := h.resolve(ctx)
	if err != nil {
		h.fail(c, err)
		return
	}
	pk, err := parseIntPK(c.Param("pk"))
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("参数错误: "+err.Error()))
		return
	}
	body, err := decodeBody(c)
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("参数错误: "+err.Error()))
		return
	}
	columns, args, err := spec.collectValues(body, false)
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("参数错误: "+err.Error()))
		return
	}
	if len(columns) == 0 {
		httpx.Fail(c, httpx.WithMessage("参数错误: 请求体没有任何可写字段"))
		return
	}

	table, err := h.engine.quoteTable(spec.PhysicalTableName())
	if err != nil {
		h.fail(c, err)
		return
	}
	assignments := make([]string, 0, len(columns)+1)
	for _, column := range columns {
		quoted, err := h.engine.quote(column)
		if err != nil {
			h.fail(c, err)
			return
		}
		assignments = append(assignments, quoted+" = ?")
	}
	updatedAt, err := h.engine.quote(ColumnUpdatedAt)
	if err != nil {
		h.fail(c, err)
		return
	}
	assignments = append(assignments, updatedAt+" = CURRENT_TIMESTAMP")

	condition, conditionArgs, err := h.rowCondition(spec, pk)
	if err != nil {
		h.fail(c, err)
		return
	}
	sql := fmt.Sprintf("UPDATE %s SET %s WHERE %s", table, strings.Join(assignments, ", "), condition)
	tx := h.engine.DB().WithContext(ctx).Exec(sql, append(args, conditionArgs...)...)
	if tx.Error != nil {
		httpx.Fail(c, httpx.WithMessage("更新失败: "+tx.Error.Error()))
		return
	}
	if tx.RowsAffected == 0 {
		httpx.Fail(c, httpx.WithMessage("记录不存在"))
		return
	}
	httpx.Success(c)
}

// Delete 软删（delete 动作写 deleted_at；界面不提供硬删；唯一硬删路径是 --purge 与迁移回滚）
func (h *entityHandler) Delete(c *gin.Context) {
	var req deleteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Fail(c, httpx.WithMessage("参数错误: "+err.Error()))
		return
	}

	ctx := c.Request.Context()
	spec, err := h.resolve(ctx)
	if err != nil {
		h.fail(c, err)
		return
	}

	ids := make([]uint64, 0, len(req.Pks))
	for _, raw := range req.Pks {
		pk, err := parseIntPK(raw)
		if err != nil {
			httpx.Fail(c, httpx.WithMessage("参数错误: "+err.Error()))
			return
		}
		ids = append(ids, pk)
	}

	table, err := h.engine.quoteTable(spec.PhysicalTableName())
	if err != nil {
		h.fail(c, err)
		return
	}
	idColumn, err := h.engine.quote(ColumnID)
	if err != nil {
		h.fail(c, err)
		return
	}
	deletedAt, err := h.engine.quote(ColumnDeletedAt)
	if err != nil {
		h.fail(c, err)
		return
	}
	updatedAt, err := h.engine.quote(ColumnUpdatedAt)
	if err != nil {
		h.fail(c, err)
		return
	}
	placeholders := make([]string, len(ids))
	args := make([]any, 0, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args = append(args, id)
	}
	sql := fmt.Sprintf("UPDATE %s SET %s = CURRENT_TIMESTAMP, %s = CURRENT_TIMESTAMP WHERE %s IS NULL AND %s IN (%s)",
		table, deletedAt, updatedAt, deletedAt, idColumn, strings.Join(placeholders, ", "))
	if err := h.engine.DB().WithContext(ctx).Exec(sql, args...).Error; err != nil {
		httpx.Fail(c, httpx.WithMessage("删除失败: "+err.Error()))
		return
	}
	httpx.Success(c)
}

// Sort 拖动排序（仅 drag_sort: true 的实体注册该路由；算法与宿主 Service.Sort 的「增量重排法」同构：
// 先整体位移、再在目标等权重区间内逐行落位，保证等权重区间的相对顺序不变）
func (h *entityHandler) Sort(c *gin.Context) {
	var req sortRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Fail(c, httpx.WithMessage("参数错误: "+err.Error()))
		return
	}

	ctx := c.Request.Context()
	spec, err := h.resolve(ctx)
	if err != nil {
		h.fail(c, err)
		return
	}
	if !spec.DragSort() {
		httpx.Fail(c, httpx.WithMessage("该实体未开启拖动排序（drag_sort）"))
		return
	}
	move := strings.TrimSpace(req.Move)
	target := strings.TrimSpace(req.Target)
	if move == "" || target == "" || move == target {
		httpx.Fail(c, httpx.WithMessage("移动行和目标行必填且不能相同"))
		return
	}

	condition, args, err := h.engine.whereClause(spec, req.Wheres)
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("查询条件错误: "+err.Error()))
		return
	}
	order, err := h.engine.buildOrder(spec, req.Sort, req.Order)
	if err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	if err := h.engine.dragSort(ctx, spec, dragRequest{
		Move: move, Target: target, Direction: req.Direction, Weigh: req.Weigh,
		Order: order, Condition: condition, Args: args,
	}); err != nil {
		httpx.Fail(c, httpx.WithMessage("调整排序失败: "+err.Error()))
		return
	}
	httpx.Success(c)
}

// dragRequest 拖动排序的已校验入参（条件与排序片段由 handler 构造，SQL 构造集中在 query.go/ddl.go）
type dragRequest struct {
	Move      string
	Target    string
	Direction string
	Weigh     int64
	Order     string
	Condition string
	Args      []any
}

// dragSort 拖动排序主流程（与宿主 internal/service/base.go 的 Sort 同构：
// 先整体位移、再在目标等权重区间内逐行落位；单事务）
func (e *Engine) dragSort(ctx context.Context, spec *Spec, req dragRequest) error {
	table, err := e.quoteTable(spec.PhysicalTableName())
	if err != nil {
		return err
	}
	idColumn, err := e.quote(ColumnID)
	if err != nil {
		return err
	}
	weighColumn, err := e.quote(ColumnWeigh)
	if err != nil {
		return err
	}
	deletedAt, err := e.quote(ColumnDeletedAt)
	if err != nil {
		return err
	}

	rows, err := e.selectWeighIDs(ctx, spec, req)
	if err != nil {
		return err
	}
	weighIDs := make([]int64, 0, len(rows))
	for _, row := range rows {
		id, err := toInt64(row[ColumnID])
		if err != nil {
			return fmt.Errorf("读取同权重行主键失败: %w", err)
		}
		weighIDs = append(weighIDs, id)
	}
	moveID, err := parseInt64PK(req.Move)
	if err != nil {
		return err
	}
	targetID, err := parseInt64PK(req.Target)
	if err != nil {
		return err
	}

	// 用户排序方式 asc（权重小的在前）: 向上拖 → 波及行权重 +1；向下拖 → 波及行权重 −1
	// desc（权重大的在前）: 向上拖 → −1；向下拖 → +1（与宿主同表）
	sortOrderDesc := strings.EqualFold(strings.TrimSpace(req.Order), "desc")
	updateOp := "-"
	if (sortOrderDesc && req.Direction == "down") || (!sortOrderDesc && req.Direction == "up") {
		updateOp = "+"
	}
	bulkOp := "<"
	if updateOp == "+" {
		bulkOp = ">"
	}

	// ① 波及行整体挪位（等权重区间先腾出一格）
	shift := fmt.Sprintf(
		"UPDATE %s SET %s = %s %s ? WHERE %s IS NULL AND %s %s ? AND %s <> ? AND (%s)",
		table, weighColumn, weighColumn, updateOp, deletedAt, weighColumn, bulkOp, idColumn, req.Condition)
	shiftArgs := append([]any{int64(len(weighIDs)), req.Weigh, moveID}, req.Args...)

	// ② 等权重区间内逐行落位（命中目标行时把被拖动行放到该位置）
	setOne := fmt.Sprintf("UPDATE %s SET %s = ? WHERE %s IS NULL AND %s = ?", table, weighColumn, deletedAt, idColumn)
	offsetOf := func(i int) int64 {
		if updateOp == "+" {
			return req.Weigh + int64(i)
		}
		return req.Weigh - int64(i)
	}

	return e.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(shift, shiftArgs...).Error; err != nil {
			return err
		}
		ordered := append([]int64{}, weighIDs...)
		if req.Direction == "down" {
			slices.Reverse(ordered) // 向下拖动时反转，保证等权重区间内相对顺序不变
		}
		moveComplete := 0
		for i, rowID := range ordered {
			if rowID == moveID {
				continue
			}
			rowWeigh := offsetOf(i)
			if rowID == targetID {
				if err := tx.Exec(setOne, rowWeigh, moveID).Error; err != nil {
					return err
				}
				moveComplete = 1
			}
			if moveComplete == 1 {
				rowWeigh = offsetOf(i + moveComplete)
			}
			if err := tx.Exec(setOne, rowWeigh, rowID).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// selectWeighIDs 取与目标同权重的行主键（同一套过滤 + 同一套排序 ⇒ 与列表的展示顺序一致）
func (e *Engine) selectWeighIDs(ctx context.Context, spec *Spec, req dragRequest) ([]map[string]any, error) {
	table, err := e.tableName(spec)
	if err != nil {
		return nil, err
	}
	idColumn, err := e.quote(ColumnID)
	if err != nil {
		return nil, err
	}
	weighColumn, err := e.quote(ColumnWeigh)
	if err != nil {
		return nil, err
	}
	rows := make([]map[string]any, 0, 8)
	query := e.DB().WithContext(ctx).Table(table).Select([]string{idColumn}).
		Where(req.Condition, req.Args...).Where(weighColumn+" = ?", req.Weigh).Scopes(orderScope(req.Order))
	if err := query.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("查询同权重行失败: %w", err)
	}
	return rows, nil
}

// parseInt64PK 主键的 int64 形态（拖动排序内部与表内 id 同型比较；超出 int64 即拒绝）
func parseInt64PK(raw string) (int64, error) {
	pk, err := parseIntPK(raw)
	if err != nil {
		return 0, err
	}
	if pk > math.MaxInt64 {
		return 0, fmt.Errorf("主键值 %q 超出 int64 范围", raw)
	}
	return int64(pk), nil
}

// rowCondition 单行条件：id = ? AND deleted_at IS NULL
func (h *entityHandler) rowCondition(spec *Spec, pk uint64) (string, []any, error) {
	idColumn, err := h.engine.quote(ColumnID)
	if err != nil {
		return "", nil, err
	}
	softDelete, err := h.engine.softDeleteCondition()
	if err != nil {
		return "", nil, err
	}
	return fmt.Sprintf("%s = ? AND %s", idColumn, softDelete), []any{pk}, nil
}

// decodeBody 解析请求体（UseNumber：整数不经过 float64，避免大整数精度损失；空体给可读错误）
func decodeBody(c *gin.Context) (map[string]any, error) {
	decoder := json.NewDecoder(c.Request.Body)
	decoder.UseNumber()
	var body map[string]any
	if err := decoder.Decode(&body); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("请求体为空")
		}
		return nil, err
	}
	if body == nil {
		return nil, errors.New("请求体不是 JSON 对象")
	}
	return body, nil
}

// collectValues 按 spec 字段顺序收集可写值：只认 spec 声明的字段（引擎列与未知键一律忽略——
// 拖动排序只能走 /sort，软删只能走 delete 动作，见 spec.ValueColumns 的注释）。
//
// create 语义：必填字段必须出现且非空；
// update 语义：只处理出现的字段（未出现的跳过，与宿主 Tri.Map 的「所见即所得」一致），
// 出现的必填字段不得置空。null 值与「未传」在两种语义下都区分对待（对齐 internal/handler/base.go 的注释）。
func (s *Spec) collectValues(body map[string]any, create bool) ([]string, []any, error) {
	columns := make([]string, 0, len(s.Entity.Fields))
	args := make([]any, 0, len(s.Entity.Fields))

	for _, field := range s.Entity.Fields {
		value, present := body[field.Name]
		if !present {
			if create && field.Required {
				return nil, nil, fmt.Errorf("字段 %s（%s）必填", field.Name, FieldTitle(field, ""))
			}
			continue
		}
		if value == nil {
			if field.Required {
				return nil, nil, fmt.Errorf("字段 %s 必填，不能置空", field.Name)
			}
			columns = append(columns, field.Name)
			args = append(args, nil)
			continue
		}
		coerced, err := coerceWriteValue(field, value)
		if err != nil {
			return nil, nil, fmt.Errorf("字段 %s: %w", field.Name, err)
		}
		if text, ok := coerced.(string); ok {
			if length := columnLength(field); length > 0 && len([]rune(text)) > length {
				return nil, nil, fmt.Errorf("字段 %s 超出长度上限 %d（实际 %d 个字符）", field.Name, length, len([]rune(text)))
			}
			if field.Required && strings.TrimSpace(text) == "" {
				return nil, nil, fmt.Errorf("字段 %s 必填，不能为空", field.Name)
			}
		}
		columns = append(columns, field.Name)
		args = append(args, coerced)
	}
	return columns, args, nil
}

// coerceWriteValue 写入值收敛（与查询条件的 coerceValue 同口径，另把 date/datetime 收敛为字符串，
// 由驱动按列类型解析；bool 列落 0/1 与宿主状态列口径一致）
func coerceWriteValue(field manifest.Field, value any) (any, error) {
	switch field.Type {
	case "int", "bigint":
		return toInt64(value)
	case "bool":
		return toBool01(value)
	case "decimal":
		text, err := toText(value)
		if err != nil {
			return nil, err
		}
		if _, err := parseFinite(text); err != nil {
			return nil, err
		}
		return text, nil
	case "date", "datetime":
		return toText(value)
	default:
		return toText(value)
	}
}

// BuildMeta 由 spec 生成渲染元数据（控件映射闭集：页面按 type/form 分派）
func BuildMeta(spec *Spec, lang string) Meta {
	meta := Meta{
		Plugin:   spec.Plugin,
		Entity:   spec.Name,
		Title:    spec.Title(lang),
		DragSort: spec.DragSort(),
		Columns:  make([]ColumnMeta, 0, len(spec.ListColumns())),
		Form:     make([]FormMeta, 0, len(spec.Fields())),
		Dicts:    map[string]map[string]string{},
	}

	for _, name := range spec.ListColumns() {
		if label, width, ok := engineColumnMeta(name, lang); ok {
			meta.Columns = append(meta.Columns, ColumnMeta{
				Prop: name, Label: label, Width: width, Sortable: spec.Sortable(name),
			})
			continue
		}
		field, ok := spec.Field(name)
		if !ok {
			continue
		}
		column := ColumnMeta{
			Prop:        field.Name,
			Label:       FieldTitle(field, lang),
			Width:       columnWidth(field),
			Sortable:    spec.Sortable(field.Name),
			QuickSearch: field.QuickSearch,
		}
		if field.Search != nil {
			column.Operator = field.Search.Op
		}
		if field.Type == "bool" {
			column.Render = "switch" // AGENTS 状态列唯一写法：数值 0/1 用 switch、不带 custom
		}
		if field.Type == "enum" {
			column.Render = "tag" // AGENTS：字符串枚举用 tag + custom + dict
			column.Custom = tagColors(field.Dict)
		}
		meta.Columns = append(meta.Columns, column)
	}

	for _, field := range spec.Fields() {
		meta.Form = append(meta.Form, FormMeta{
			Prop:     field.Name,
			Label:    FieldTitle(field, lang),
			Type:     formControl(field),
			Required: field.Required,
			Length:   columnLength(field),
		})
		if len(field.Dict) > 0 {
			meta.Dicts[field.Name] = dictLabels(field.Dict, lang)
		}
	}
	return meta
}

// engineColumnMeta 引擎列的展示元数据：引擎列不在 spec 里，没有双语元数据可读 ⇒ 标签在此按 lang 取值
// （与字段文案同一口径：随响应下发、绕开构建期语言包；宽度不变）
func engineColumnMeta(name, lang string) (string, int, bool) {
	english := isEnglish(lang)
	switch name {
	case ColumnID:
		return "ID", 80, true
	case ColumnWeigh:
		if english {
			return "Sort Weight", 90, true
		}
		return "排序权重", 90, true
	case ColumnCreatedAt:
		if english {
			return "Created At", 170, true
		}
		return "创建时间", 170, true
	}
	return "", 0, false
}

// columnWidth 列宽（UI 甜头，按类型给常见取值；页面可自行覆盖）
func columnWidth(field manifest.Field) int {
	switch field.Type {
	case "string", "text":
		return 200
	case "enum":
		return 120
	case "int", "bigint":
		return 120
	case "decimal":
		return 140
	case "bool":
		return 100
	case "date":
		return 120
	case "datetime":
		return 170
	}
	return 160
}

// tagColors 枚举列的颜色映射（tag 渲染器的 custom；调色按 dict 顺序确定性分配，页面可覆盖）
func tagColors(dict []manifest.Dict) map[string]string {
	palette := []string{"success", "info", "warning", "danger", "primary"}
	colors := make(map[string]string, len(dict))
	for i, item := range dict {
		colors[fmt.Sprint(item.Value)] = palette[i%len(palette)]
	}
	return colors
}

// dictLabels 字典的值→文案（按语言；Meta.Dicts 的形状 = 宿主 dict 的形状：Record<值, 文案>）
func dictLabels(dict []manifest.Dict, lang string) map[string]string {
	labels := make(map[string]string, len(dict))
	for _, item := range dict {
		label := item.LabelZh
		if isEnglish(lang) && strings.TrimSpace(item.LabelEn) != "" {
			label = item.LabelEn
		}
		labels[fmt.Sprint(item.Value)] = label
	}
	return labels
}

// formControl 表单控件（控件映射闭集）：spec.form 优先，缺省按 type 推导
func formControl(field manifest.Field) string {
	if control := strings.TrimSpace(field.Form); control != "" {
		return control
	}
	switch field.Type {
	case "string":
		return "input"
	case "text":
		return "textarea"
	case "int", "bigint", "decimal":
		return "input-number"
	case "bool":
		return "switch"
	case "date":
		return "date"
	case "datetime":
		return "datetime"
	case "enum":
		return "select"
	}
	return "input"
}
