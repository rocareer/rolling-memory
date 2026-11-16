// plugin: ai v0.2.0
// handler_usage.go 是日志两张面：
//
//	POST /usage/list         用量聚合（group=model|biz|source|biz_source|day + totals + filters）
//	POST /usage-log/list     调用日志明细（等值/模糊/范围三类显式筛选 + wheres + 分页）
//	POST /usage-log/detail   按 request_id 还原尝试链（rows 按 attempt 升序 + summary）
//
// 出口纪律：cost 一律以 6 位小数字符串返回（numeric 文本 → ParsePriceMicro → FormatMicro），
// 前端只展示不做算术；usage 表无敏感列（上游密钥从不落库）。
package ai

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ai-go-hub/ai-go-admin/internal/kit/httpx"

	"github.com/gin-gonic/gin"
)

// usageGroups 用量聚合维度闭集（照源 Router.php 的 groups：day 按天倒序，其余按 calls 降序）。
var usageGroups = []string{"model", "biz", "source", "biz_source", "day"}

// usageLogStatuses 调用日志状态闭集（plugin_ai_usage.status 的 CHECK）。
var usageLogStatuses = []string{UsageStatusSuccess, UsageStatusFail, UsageStatusTimeout, UsageStatusBreaker}

// usageLogCallTypes 调用类型闭集（chat|embedding|tts；TTS 增量常量见 domain_closure.go）。
// 源侧还含 'asr'（语音转写），v1 无该入口 ⇒ 刻意不在闭集内（传 asr 被拒是口径，不是漏项）。
var usageLogCallTypes = []string{CallTypeChat, CallTypeEmbedding, CallTypeTTS}

// usageLogTokensSources token 来源闭集（空串 = 本次无计数）。
var usageLogTokensSources = []string{TokensSourceNone, TokensSourceUsage, TokensSourceEstimate}

// usageLogWhereFields 是 usage 表的 wheres 字段白名单（api_key 一类的秘密列本表不存在）。
var usageLogWhereFields = map[string]whereField{
	"id":                {Column: `"id"`, Kind: whereInt},
	"request_id":        {Column: `"request_id"`, Kind: whereText},
	"attempt":           {Column: `"attempt"`, Kind: whereInt},
	"model_id":          {Column: `"model_id"`, Kind: whereInt},
	"model_name":        {Column: `"model_name"`, Kind: whereText},
	"channel_id":        {Column: `"channel_id"`, Kind: whereInt},
	"channel_name":      {Column: `"channel_name"`, Kind: whereText},
	"call_type":         {Column: `"call_type"`, Kind: whereText},
	"biz_type":          {Column: `"biz_type"`, Kind: whereText},
	"source":            {Column: `"source"`, Kind: whereText},
	"stream":            {Column: `"stream"`, Kind: whereBool01},
	"prompt_tokens":     {Column: `"prompt_tokens"`, Kind: whereInt},
	"completion_tokens": {Column: `"completion_tokens"`, Kind: whereInt},
	"total_tokens":      {Column: `"total_tokens"`, Kind: whereInt},
	"cached_tokens":     {Column: `"cached_tokens"`, Kind: whereInt},
	"tokens_source":     {Column: `"tokens_source"`, Kind: whereText},
	"cost":              {Column: `"cost"`, Kind: whereText},
	"currency":          {Column: `"currency"`, Kind: whereText},
	"latency_ms":        {Column: `"latency_ms"`, Kind: whereInt},
	"status":            {Column: `"status"`, Kind: whereText},
	"error_code":        {Column: `"error_code"`, Kind: whereText},
	"error_message":     {Column: `"error_message"`, Kind: whereText},
	"error_class":       {Column: `"error_class"`, Kind: whereText},
	"create_time":       {Column: `"create_time"`, Kind: whereInt},
	"update_time":       {Column: `"update_time"`, Kind: whereInt},
}

// formatCost 把 numeric 文本归一成 6 位小数字符串；解析不了时原样返回（不伪造金额）。
func formatCost(raw string) string {
	micro, err := ParsePriceMicro(strings.TrimSpace(raw))
	if err != nil {
		return raw
	}
	return FormatMicro(micro)
}

// ===== 用量聚合 =====

// usageListRequest 用量聚合入参（无分页；聚合行上限 100）。
type usageListRequest struct {
	Group     string `json:"group"`
	ModelName string `json:"model_name"`
	BizType   string `json:"biz_type"`
	Source    string `json:"source"`
	Start     *int64 `json:"start"` // create_time ≥（秒级闭区间）
	End       *int64 `json:"end"`   // create_time ≤
}

// query 收敛聚合维度与筛选。
func (r usageListRequest) query() (UsageAggregateQuery, error) {
	group := strings.TrimSpace(r.Group)
	if group == "" {
		group = "model"
	}
	if !containsString(usageGroups, group) {
		return UsageAggregateQuery{}, fmt.Errorf("group %q 不在维度闭集内（允许：%s）", r.Group, strings.Join(usageGroups, "|"))
	}
	if r.Start != nil && r.End != nil && *r.Start > *r.End {
		return UsageAggregateQuery{}, errors.New("时间范围下界不能大于上界")
	}
	return UsageAggregateQuery{
		Group:     group,
		ModelName: strings.TrimSpace(r.ModelName),
		BizType:   strings.TrimSpace(r.BizType),
		Source:    strings.TrimSpace(r.Source),
		Start:     r.Start,
		End:       r.End,
	}, nil
}

// usageList 用量聚合：list + totals + filters（下拉数据源由同一响应的真实去重值回填）。
func (d deps) usageList(c *gin.Context) {
	store := d.storeOrFail(c)
	if store == nil {
		return
	}
	var req usageListRequest
	if err := bindJSON(c, &req); err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	query, err := req.query()
	if err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	result, err := store.AggregateUsage(c.Request.Context(), query)
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("读取用量统计失败: "+err.Error()))
		return
	}
	for i := range result.Rows {
		result.Rows[i].Cost = formatCost(result.Rows[i].Cost)
	}
	result.Totals.Cost = formatCost(result.Totals.Cost)
	httpx.Success(c, httpx.WithData(gin.H{
		"list":    result.Rows,
		"totals":  result.Totals,
		"filters": result.Filters,
	}))
}

// ===== 调用日志明细 =====

// usageLogListRequest 调用日志入参：宿主分页契约 + 三类显式筛选
// （等值 status/call_type/biz_type/source/tokens_source/request_id；模糊 model_name/channel_name；范围 start/end）。
type usageLogListRequest struct {
	listRequest
	Status       string `json:"status"`
	CallType     string `json:"call_type"`
	BizType      string `json:"biz_type"`
	Source       string `json:"source"`
	TokensSource string `json:"tokens_source"`
	RequestID    string `json:"request_id"`
	ModelName    string `json:"model_name"`
	ChannelName  string `json:"channel_name"`
	Start        *int64 `json:"start"`
	End          *int64 `json:"end"`
}

// filter 收敛筛选 + 已校验的 wheres 组（闭集列非空即校验，模糊列由 store 走 ILIKE）。
func (r usageLogListRequest) filter() (UsageFilter, []whereGroup, error) {
	status := strings.TrimSpace(r.Status)
	if status != "" && !containsString(usageLogStatuses, status) {
		return UsageFilter{}, nil, fmt.Errorf("status %q 不在闭集内（允许：%s）", r.Status, strings.Join(usageLogStatuses, "|"))
	}
	callType := strings.TrimSpace(r.CallType)
	if callType != "" && !containsString(usageLogCallTypes, callType) {
		return UsageFilter{}, nil, fmt.Errorf("call_type %q 不在闭集内（允许：%s）", r.CallType, strings.Join(usageLogCallTypes, "|"))
	}
	tokensSource := strings.TrimSpace(r.TokensSource)
	if tokensSource != "" && !containsString(usageLogTokensSources, tokensSource) {
		return UsageFilter{}, nil, fmt.Errorf("tokens_source %q 不在闭集内（允许：%s）", r.TokensSource, strings.Join(usageLogTokensSources, "|"))
	}
	if r.Start != nil && r.End != nil && *r.Start > *r.End {
		return UsageFilter{}, nil, errors.New("时间范围下界不能大于上界")
	}
	if err := validateSort(r.Sort, usageLogSortColumns, "调用日志"); err != nil {
		return UsageFilter{}, nil, err
	}
	groups, err := validateWhereGroups(usageLogWhereFields, r.Wheres)
	if err != nil {
		return UsageFilter{}, nil, err
	}
	return UsageFilter{
		Page:         r.Page,
		Limit:        r.Limit,
		Status:       status,
		CallType:     callType,
		BizType:      strings.TrimSpace(r.BizType),
		Source:       strings.TrimSpace(r.Source),
		TokensSource: tokensSource,
		RequestID:    strings.TrimSpace(r.RequestID),
		ModelName:    strings.TrimSpace(r.ModelName),
		ChannelName:  strings.TrimSpace(r.ChannelName),
		Start:        r.Start,
		End:          r.End,
		Sort:         r.Sort,
		Order:        r.Order,
	}, groups, nil
}

// usageLogList 调用日志分页（默认 limit 20、id DESC；time 列不走 comSearch 的 datetime/BETWEEN）。
func (d deps) usageLogList(c *gin.Context) {
	store := d.storeOrFail(c)
	if store == nil {
		return
	}
	var req usageLogListRequest
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
	var result UsageLogPage
	if len(groups) == 0 {
		result, err = store.ListUsageLogs(ctx, filter)
	} else {
		result, err = store.listUsageLogsWithWheres(ctx, filter, groups)
	}
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("读取调用日志失败: "+err.Error()))
		return
	}
	for i := range result.Rows {
		result.Rows[i].Cost = formatCost(result.Rows[i].Cost)
	}
	httpx.Success(c, httpx.WithData(gin.H{"list": result.Rows, "total": result.Total}))
}

// listUsageLogsWithWheres 与 Store.ListUsageLogs 同口径（同一基础筛选/排序/分页），只多一层 wheres。
func (s *Store) listUsageLogsWithWheres(ctx context.Context, f UsageFilter, groups []whereGroup) (UsageLogPage, error) {
	result := UsageLogPage{Rows: []Usage{}}
	if err := s.ready(); err != nil {
		return result, err
	}
	countQuery, err := applyWhereGroups(s.usageLogQuery(ctx, f), usageLogWhereFields, groups)
	if err != nil {
		return result, err
	}
	if err := countQuery.Count(&result.Total).Error; err != nil {
		return result, err
	}
	listQuery, err := applyWhereGroups(s.usageLogQuery(ctx, f), usageLogWhereFields, groups)
	if err != nil {
		return result, err
	}
	offset, limit := normalizePage(f.Page, f.Limit, DefaultUsageLogLimit)
	order := orderClause(f.Sort, f.Order, usageLogSortColumns, `"id" DESC`)
	if err := listQuery.Order(order).Offset(offset).Limit(limit).Find(&result.Rows).Error; err != nil {
		return result, err
	}
	return result, nil
}

// ===== 尝试链详情 =====

// usageSummary 尝试链汇总。
//
// 桶定义（非阻塞项已钉死）：calls = success + fail + timeout，
// **fail 不含 timeout**（源只有 success/fail 两桶、timeout 归 fail；此处细分，
// 与源对账时用 fail+timeout）。schemas.status='breaker' 是保留枚举（v1 无写入点），出现时计入 fail。
type usageSummary struct {
	Calls       int64  `json:"calls"`
	Success     int64  `json:"success"`
	Fail        int64  `json:"fail"`
	Timeout     int64  `json:"timeout"`
	TotalTokens int64  `json:"total_tokens"`
	Cost        string `json:"cost"`
	// TotalCostMicro 是逐行精确求和（微单位整数），不进 JSON。
	TotalCostMicro int64 `json:"-"`
}

// summarizeUsage 逐行汇总（金额用 ParsePriceMicro 求微单位整数，绝不走 float64）。
func summarizeUsage(rows []Usage) usageSummary {
	summary := usageSummary{}
	for _, row := range rows {
		summary.Calls++
		switch row.Status {
		case UsageStatusSuccess:
			summary.Success++
		case UsageStatusTimeout:
			summary.Timeout++
		default:
			summary.Fail++
		}
		summary.TotalTokens += int64(row.TotalTokens)
		if micro, err := ParsePriceMicro(strings.TrimSpace(row.Cost)); err == nil {
			summary.TotalCostMicro += micro
		}
	}
	summary.Cost = FormatMicro(summary.TotalCostMicro)
	return summary
}

// usageLogDetailRequest 详情入参。
type usageLogDetailRequest struct {
	RequestID string `json:"request_id"`
}

// usageLogDetail 按 request_id 还原尝试链（rows 按 attempt 升序）。
//
// request_id 为空 ⇒ 直接报「渠道直连调试不记账」（不查库）：源侧直连腿绕过调度与记账。
func (d deps) usageLogDetail(c *gin.Context) {
	store := d.storeOrFail(c)
	if store == nil {
		return
	}
	var req usageLogDetailRequest
	if err := bindJSON(c, &req); err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	requestID := strings.TrimSpace(req.RequestID)
	if requestID == "" {
		httpx.Fail(c, httpx.WithMessage("渠道直连调试不记账（缺少 request_id）"))
		return
	}
	rows, err := store.UsageDetailChain(c.Request.Context(), requestID)
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("读取尝试链失败: "+err.Error()))
		return
	}
	httpx.Success(c, httpx.WithData(gin.H{
		"request_id": requestID,
		"rows":       rows,
		"summary":    summarizeUsage(rows),
	}))
}

// containsString 闭集判定（列表都很短，线性扫描足够）。
func containsString(items []string, value string) bool {
	for _, item := range items {
		if item == value {
			return true
		}
	}
	return false
}
