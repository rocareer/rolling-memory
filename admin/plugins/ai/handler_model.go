// plugin: ai v0.2.0
// handler_model.go 是模型管理的 6 个后台端点：
//
//	POST /model/list      列表（分页 + 快搜 + 顶层 type/status + wheres；行内附绑定明细）
//	GET  /model/get/:pk   单体回填（含已绑定渠道明细，供绑定弹窗回填）
//	POST /model/create    新增（name 唯一；type/strategy/status 白名单收敛；单价过解析器）
//	POST /model/update/:pk 保存（改名查重；不触碰绑定）
//	POST /model/delete    批量删除（同事务先删绑定）
//	POST /model/bind      绑定渠道（全量替换：事务内先删该模型全部绑定再插入）
//
// 金额纪律：price_input/price_output 一律经 ParsePriceMicro 解析（拒 NaN/Inf/非十进制），
// 写库前用 FormatMicro 归一成 6 位小数字符串——金额全程不经 float64。
package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/ai-go-hub/ai-go-admin/internal/kit/httpx"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// modelWhereFields 是模型表的 wheres 字段白名单（capabilities 是 jsonb，刻意不作条件）。
var modelWhereFields = map[string]whereField{
	"id":             {Column: `"id"`, Kind: whereInt},
	"name":           {Column: `"name"`, Kind: whereText},
	"type":           {Column: `"type"`, Kind: whereText},
	"strategy":       {Column: `"strategy"`, Kind: whereText},
	"description":    {Column: `"description"`, Kind: whereText},
	"status":         {Column: `"status"`, Kind: whereBool01},
	"price_input":    {Column: `"price_input"`, Kind: whereText},
	"price_output":   {Column: `"price_output"`, Kind: whereText},
	"encoding":       {Column: `"encoding"`, Kind: whereText},
	"context_window": {Column: `"context_window"`, Kind: whereInt},
	"create_time":    {Column: `"create_time"`, Kind: whereInt},
	"update_time":    {Column: `"update_time"`, Kind: whereInt},
}

// validModelType 调用类型闭集（chat|embedding|tts；TTS 增量常量见 domain_closure.go）。
//
// 闭集一旦放宽，DB 侧 CHECK 必须同步（迁移 6；旧 CHECK 只有 chat|embedding）——
// 时序纪律见 domain_closure.go 头注。
func validModelType(value string) bool {
	return value == ModelTypeChat || value == ModelTypeEmbedding || value == ModelTypeTTS
}

// validStrategy 调度策略闭集（priority|weighted）。
func validStrategy(value string) bool {
	return value == StrategyPriority || value == StrategyWeighted
}

// validEncoding 本地计数编码闭集（”=关闭估算与预检；唯一合法编码 tokenz-r）。
func validEncoding(value string) bool {
	return value == EncodingNone || value == EncodingTokenzR
}

// ===== 列表 =====

// modelListRequest 模型列表入参：宿主分页契约 + 顶层等值筛选 type/status。
type modelListRequest struct {
	listRequest
	Type   string         `json:"type"`
	Status optionalStatus `json:"status"`
}

// filter 把请求收敛成 store 的筛选结构 + 已校验的 wheres 组。
func (r modelListRequest) filter() (ModelFilter, []whereGroup, error) {
	if r.Status.Value != nil && !validStatus(*r.Status.Value) {
		return ModelFilter{}, nil, errors.New("status 只能是 0（停用）或 1（启用）")
	}
	typeValue := strings.TrimSpace(r.Type)
	if typeValue != "" && !validModelType(typeValue) {
		return ModelFilter{}, nil, errors.New("type 只能是 chat、embedding 或 tts")
	}
	if err := validateSort(r.Sort, modelSortColumns, "模型"); err != nil {
		return ModelFilter{}, nil, err
	}
	groups, err := validateWhereGroups(modelWhereFields, r.Wheres)
	if err != nil {
		return ModelFilter{}, nil, err
	}
	filter := ModelFilter{
		Page:    r.Page,
		Limit:   r.Limit,
		Sort:    r.Sort,
		Order:   r.Order,
		Keyword: r.keyword(),
		Status:  r.Status.Value,
	}
	if typeValue != "" {
		filter.Type = &typeValue
	}
	return filter, groups, nil
}

// modelList 模型分页列表（每行附 channels / channel_names / bound_count，无 N+1）。
func (d deps) modelList(c *gin.Context) {
	store := d.storeOrFail(c)
	if store == nil {
		return
	}
	var req modelListRequest
	if err := bindJSON(c, &req); err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	filter, groups, err := req.filter()
	if err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	ctx := c.Request.Context()
	var result ModelListResult
	if len(groups) == 0 {
		result, err = store.ListModels(ctx, filter)
	} else {
		result, err = store.listModelsWithWheres(ctx, filter, groups)
	}
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("读取模型列表失败: "+err.Error()))
		return
	}
	httpx.Success(c, httpx.WithData(gin.H{"list": result.Rows, "total": result.Total}))
}

// listModelsWithWheres 与 Store.ListModels 同口径（同一基础筛选/排序/分页与绑定装配），只多一层 wheres。
func (s *Store) listModelsWithWheres(ctx context.Context, f ModelFilter, groups []whereGroup) (ModelListResult, error) {
	result := ModelListResult{Rows: []ModelWithBindings{}}
	if err := s.ready(); err != nil {
		return result, err
	}
	countQuery, err := applyWhereGroups(s.modelQuery(ctx, f), modelWhereFields, groups)
	if err != nil {
		return result, err
	}
	if err := countQuery.Count(&result.Total).Error; err != nil {
		return result, err
	}
	listQuery, err := applyWhereGroups(s.modelQuery(ctx, f), modelWhereFields, groups)
	if err != nil {
		return result, err
	}
	offset, limit := normalizePage(f.Page, f.Limit, DefaultModelListLimit)
	order := orderClause(f.Sort, f.Order, modelSortColumns, `"id" DESC`)
	if err := listQuery.Order(order).Offset(offset).Limit(limit).Find(&result.Rows).Error; err != nil {
		return result, err
	}
	ids := make([]uint64, 0, len(result.Rows))
	for i := range result.Rows {
		ids = append(ids, result.Rows[i].ID)
	}
	bindings, err := s.ListBindings(ctx, ids)
	if err != nil {
		return result, err
	}
	attachBindings(result.Rows, bindings)
	return result, nil
}

// ===== 单体读取 =====

// modelGet 单体回填：含全部可编辑列与已绑定渠道明细（绑定弹窗据此回填 priority/weight/status）。
func (d deps) modelGet(c *gin.Context) {
	store := d.storeOrFail(c)
	if store == nil {
		return
	}
	id, err := parsePK(c, "模型")
	if err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	row, err := store.GetModel(c.Request.Context(), uint64(id))
	if errors.Is(err, gorm.ErrRecordNotFound) {
		httpx.Fail(c, httpx.WithMessage(fmt.Sprintf("模型 %d 不存在", id)))
		return
	}
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("读取模型失败: "+err.Error()))
		return
	}
	httpx.Success(c, httpx.WithData(gin.H{"row": row}))
}

// ===== 新增 / 保存 =====

// modelSaveRequest 模型新增/编辑入参（指针字段区分「缺省」与「显式零值」）。
type modelSaveRequest struct {
	Name          *string         `json:"name"`
	Type          *string         `json:"type"`
	Strategy      *string         `json:"strategy"`
	Description   *string         `json:"description"`
	Status        *int16          `json:"status"`
	PriceInput    *any            `json:"price_input"`
	PriceOutput   *any            `json:"price_output"`
	Encoding      *string         `json:"encoding"`
	ContextWindow *int            `json:"context_window"`
	Capabilities  *capabilityList `json:"capabilities"`
}

// capabilityList 接受字符串数组或逗号串。
type capabilityList []string

// UnmarshalJSON 接受 ["chat","function_calling"] 与 "chat,function_calling"。
func (c *capabilityList) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		*c = nil
		return nil
	}
	if strings.HasPrefix(trimmed, "[") {
		var items []string
		if err := json.Unmarshal(data, &items); err != nil {
			return errors.New("capabilities 必须是字符串数组或逗号分隔字符串")
		}
		*c = items
		return nil
	}
	var single string
	if err := json.Unmarshal(data, &single); err != nil {
		return errors.New("capabilities 必须是字符串数组或逗号分隔字符串")
	}
	if strings.TrimSpace(single) == "" {
		*c = nil
		return nil
	}
	*c = strings.Split(single, ",")
	return nil
}

// modelCreate 新增模型。
func (d deps) modelCreate(c *gin.Context) {
	store := d.storeOrFail(c)
	if store == nil {
		return
	}
	var req modelSaveRequest
	if err := bindJSON(c, &req); err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	row, err := req.model()
	if err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	ctx := c.Request.Context()
	if existing, findErr := store.FindModel(ctx, row.Name); findErr == nil && existing.ID != 0 {
		httpx.Fail(c, httpx.WithMessage(fmt.Sprintf("模型名 %q 已存在（name 唯一）", row.Name)))
		return
	}
	if err := store.CreateModel(ctx, row); err != nil {
		if isUniqueViolation(err) {
			httpx.Fail(c, httpx.WithMessage(fmt.Sprintf("模型名 %q 已存在（name 唯一）", row.Name)))
			return
		}
		httpx.Fail(c, httpx.WithMessage("新增模型失败: "+err.Error()))
		return
	}
	httpx.Success(c, httpx.WithData(gin.H{"id": row.ID, "name": row.Name}))
}

// modelUpdate 保存模型（改名查重；绑定只走 model/bind 的全量替换）。
func (d deps) modelUpdate(c *gin.Context) {
	store := d.storeOrFail(c)
	if store == nil {
		return
	}
	id, err := parsePK(c, "模型")
	if err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	var req modelSaveRequest
	if err := bindJSON(c, &req); err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	patch, err := req.patch()
	if err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	ctx := c.Request.Context()
	if patch.Name != nil {
		if existing, findErr := store.FindModel(ctx, *patch.Name); findErr == nil && existing.ID != 0 && existing.ID != uint64(id) {
			httpx.Fail(c, httpx.WithMessage(fmt.Sprintf("模型名 %q 已被其它模型占用（name 唯一）", *patch.Name)))
			return
		}
	}
	affected, err := store.UpdateModel(ctx, uint64(id), patch)
	if err != nil {
		if isUniqueViolation(err) {
			httpx.Fail(c, httpx.WithMessage("模型名已存在（name 唯一）"))
			return
		}
		httpx.Fail(c, httpx.WithMessage("保存模型失败: "+err.Error()))
		return
	}
	if affected == 0 {
		if _, getErr := store.GetModel(ctx, uint64(id)); errors.Is(getErr, gorm.ErrRecordNotFound) {
			httpx.Fail(c, httpx.WithMessage(fmt.Sprintf("模型 %d 不存在", id)))
			return
		}
	}
	httpx.Success(c, httpx.WithData(gin.H{"id": id, "affected": affected}))
}

// modelDelete 批量删除（同一事务内先删绑定再删模型行）。
func (d deps) modelDelete(c *gin.Context) {
	store := d.storeOrFail(c)
	if store == nil {
		return
	}
	var req pksRequest
	if err := bindJSON(c, &req); err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	ids, err := req.ids("模型")
	if err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	if err := store.DeleteModels(c.Request.Context(), u64IDs(ids)); err != nil {
		httpx.Fail(c, httpx.WithMessage("删除模型失败: "+err.Error()))
		return
	}
	httpx.Success(c, httpx.WithData(gin.H{"deleted": len(ids)}))
}

// ===== 绑定（全量替换）=====

// modelBindRequest 绑定入参：{model_id, binds:[{channel_id,priority,weight,enabled}]}。
// enabled 缺省 false ⇒ status=0（只有 1 参与调度）。
type modelBindRequest struct {
	ModelID uint64          `json:"model_id"`
	Binds   []modelBindItem `json:"binds"`
}

// modelBindItem 单条绑定：priority/weight 缺省 1（显式 0 保留 0），enabled 缺省 false。
type modelBindItem struct {
	ChannelID uint64 `json:"channel_id"`
	Priority  *int   `json:"priority"`
	Weight    *int   `json:"weight"`
	Enabled   bool   `json:"enabled"`
}

// modelBind 绑定渠道（全量替换）：事务内先删该模型全部绑定再插入；channel_id 不存在则跳过。
func (d deps) modelBind(c *gin.Context) {
	store := d.storeOrFail(c)
	if store == nil {
		return
	}
	var req modelBindRequest
	if err := bindJSON(c, &req); err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	if req.ModelID == 0 {
		httpx.Fail(c, httpx.WithMessage("model_id 必填"))
		return
	}
	binds := bindRows(req.ModelID, req.Binds)
	bound, err := store.ReplaceBindings(c.Request.Context(), req.ModelID, binds)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		httpx.Fail(c, httpx.WithMessage(fmt.Sprintf("模型 %d 不存在", req.ModelID)))
		return
	}
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("绑定渠道失败: "+err.Error()))
		return
	}
	httpx.Success(c, httpx.WithData(gin.H{"model_id": req.ModelID, "bound": bound}))
}

// ===== 入参收敛（纯函数，可单测）=====

// bindRows 把绑定入参收敛成绑定行：channel_id=0 跳过，priority/weight 缺省 1、负数收敛为 0，
// enabled=false ⇒ status=0（只有 1 参与调度；渠道是否存在由 store 的事务再筛一遍）。
func bindRows(modelID uint64, items []modelBindItem) []ModelChannel {
	rows := make([]ModelChannel, 0, len(items))
	for _, item := range items {
		if item.ChannelID == 0 {
			continue
		}
		status := StatusDisabled
		if item.Enabled {
			status = StatusEnabled
		}
		rows = append(rows, ModelChannel{
			ModelID:   modelID,
			ChannelID: item.ChannelID,
			Priority:  nonNegative(derefInt(item.Priority, 1)),
			Weight:    nonNegative(derefInt(item.Weight, 1)),
			Status:    status,
		})
	}
	return rows
}

// model 组装新行：type/strategy/status 白名单收敛（非法值回落 chat/priority/1）、
// encoding 非闭集置空、单价过解析器、context_window 负数收敛为 0。
func (r modelSaveRequest) model() (*Model, error) {
	name, err := requiredText(r.Name, "模型名", 100)
	if err != nil {
		return nil, err
	}
	priceInput, err := parsePriceValue(r.PriceInput)
	if err != nil {
		return nil, fmt.Errorf("输入单价: %w", err)
	}
	priceOutput, err := parsePriceValue(r.PriceOutput)
	if err != nil {
		return nil, fmt.Errorf("输出单价: %w", err)
	}
	row := &Model{
		Name:          name,
		Type:          normalizeModelType(r.Type),
		Strategy:      normalizeStrategy(r.Strategy),
		Description:   truncateRunes(strings.TrimSpace(derefString(r.Description)), 255),
		Status:        normalizeStatus(r.Status),
		PriceInput:    priceInput,
		PriceOutput:   priceOutput,
		Encoding:      normalizeEncoding(r.Encoding),
		ContextWindow: nonNegative(derefInt(r.ContextWindow, 0)),
	}
	if r.Capabilities != nil {
		row.Capabilities = normalizeCapabilities(*r.Capabilities)
	}
	return row, nil
}

// patch 组装编辑补丁：只带出现过的字段；capabilities 出现即整组替换（空数组 = 清空）。
func (r modelSaveRequest) patch() (ModelPatch, error) {
	patch := ModelPatch{}
	if r.Name != nil {
		name, err := requiredText(r.Name, "模型名", 100)
		if err != nil {
			return patch, err
		}
		patch.Name = &name
	}
	if r.Type != nil {
		value := normalizeModelType(r.Type)
		patch.Type = &value
	}
	if r.Strategy != nil {
		value := normalizeStrategy(r.Strategy)
		patch.Strategy = &value
	}
	if r.Description != nil {
		value := truncateRunes(strings.TrimSpace(*r.Description), 255)
		patch.Description = &value
	}
	if r.Status != nil {
		value := normalizeStatus(r.Status)
		patch.Status = &value
	}
	if r.PriceInput != nil {
		value, err := parsePriceValue(r.PriceInput)
		if err != nil {
			return patch, fmt.Errorf("输入单价: %w", err)
		}
		patch.PriceInput = &value
	}
	if r.PriceOutput != nil {
		value, err := parsePriceValue(r.PriceOutput)
		if err != nil {
			return patch, fmt.Errorf("输出单价: %w", err)
		}
		patch.PriceOutput = &value
	}
	if r.Encoding != nil {
		value := normalizeEncoding(r.Encoding)
		patch.Encoding = &value
	}
	if r.ContextWindow != nil {
		value := nonNegative(*r.ContextWindow)
		patch.ContextWindow = &value
	}
	if r.Capabilities != nil {
		value := normalizeCapabilities(*r.Capabilities)
		patch.Capabilities = &value
	}
	return patch, nil
}

// parsePriceValue 把入参里的单价收敛成 numeric(12,6) 文本。
//
// 缺省/空串/null ⇒ 0.000000（前端清空输入框）；其余一律经 ParsePriceMicro：
// 显式拒绝 NaN / Inf / 负数 / 非十进制形态（PG 视 NaN 为大于一切数值，CHECK(x>=0) 拦不住），
// 再经 FormatMicro 归一成 6 位小数（half-away-from-zero 舍入在解析器内完成）。
func parsePriceValue(value *any) (string, error) {
	if value == nil || *value == nil {
		return FormatMicro(0), nil
	}
	var raw string
	switch v := (*value).(type) {
	case string:
		raw = strings.TrimSpace(v)
		if raw == "" {
			return FormatMicro(0), nil
		}
	case float64:
		raw = fmt.Sprintf("%.6f", v)
	case int:
		raw = fmt.Sprintf("%d", v)
	case int64:
		raw = fmt.Sprintf("%d", v)
	default:
		return "", errors.New("必须是数字或十进制字符串")
	}
	micro, err := ParsePriceMicro(raw)
	if err != nil {
		return "", err
	}
	return FormatMicro(micro), nil
}

// normalizeModelType 调用类型白名单收敛：闭集外回落 chat（tts 是闭集内取值，保留）。
func normalizeModelType(value *string) string {
	text := strings.TrimSpace(derefString(value))
	if validModelType(text) {
		return text
	}
	return ModelTypeChat
}

// normalizeStrategy 策略白名单收敛：非法值回落 priority。
func normalizeStrategy(value *string) string {
	text := strings.TrimSpace(derefString(value))
	if validStrategy(text) {
		return text
	}
	return StrategyPriority
}

// normalizeStatus 二值状态收敛：非法值回落 1（启用）。
func normalizeStatus(value *int16) int16 {
	if value != nil && validStatus(*value) {
		return *value
	}
	return StatusEnabled
}

// normalizeEncoding 编码白名单收敛：非闭集一律置空（空 = 关闭本地估算与上下文预检）。
func normalizeEncoding(value *string) string {
	text := strings.TrimSpace(derefString(value))
	if validEncoding(text) {
		return text
	}
	return EncodingNone
}

// normalizeCapabilities 去空白、去重、逐项截断（jsonb 数组；顺序保持首次出现的次序）。
func normalizeCapabilities(items []string) []string {
	out := make([]string, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		value := truncateRunes(strings.TrimSpace(item), 64)
		if value == "" {
			continue
		}
		if _, dup := seen[value]; dup {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
		if len(out) >= 20 {
			break
		}
	}
	if out == nil {
		return []string{}
	}
	return out
}
