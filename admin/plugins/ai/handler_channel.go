// plugin: ai v0.2.0
// handler_channel.go 是渠道管理的 9 个后台端点：
//
//	POST /channel/list      列表（分页 + 快搜 + 顶层 status/vendor + wheres + 四字段统计条）
//	GET  /channel/get/:pk   单行回填（渠道自身列 + 掩码 api_key）
//	POST /channel/create    新增（name/model 必填；base_url 挂厂商时可留空 ⇒ 运行时继承厂商）
//	POST /channel/update/:pk 保存（api_key 空串或含 '*' ⇒ 不修改；清空 base_url 同上要挂厂商）
//	POST /channel/delete    批量删除（同事务先清绑定）
//	POST /channel/switch    启停（只改 status）
//	POST /channel/probe     手动探活（读生效行：继承厂商凭证；超时 clamp(渠道生效 timeout,3,15) 秒）
//	POST /channel/breaker   人工熔断 / 恢复（等价源的 forceCircuit + resetBreaker）
//	POST /channel/events    单渠道最近 20 条熔断事件
//
// 出口纪律：api_key 一律脱敏（mask_secret 同口径）、掩码永不回写；人工操作与探活都不动累计统计。
//
// 三级配置（厂商 → 渠道）：表单回显用渠道自身列（留空 = 继承厂商，由前端提示），而路由
// （store.Candidates）与探活（store.GetChannelEffective）都读**生效值**——两者共用 store.go 的
// mergeSupplierInheritance 一份实现，避免「界面显示一套、调用用另一套」。
package ai

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ai-go-hub/ai-go-admin/internal/kit/httpx"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// channelVendors 是 vendor 标签位闭集（v1 只作展示与筛选项，不触发继承/协议差异）。
// driver 刻意**不是**闭集：合法取值由驱动注册表（GetSupported）给出，登记表可扩展
// 把闭集钉死在 handler 里会让新驱动必须先改这里。未知 driver
// 在调用期由注册表返回明确错误（不静默回落），这是「拒绝非法值」的真正落点。
var channelVendors = []string{"openai", "deepseek", "qwen", "moonshot", "zhipu", "ollama", "other"}

// driverNameMaxLen 驱动名的长度上界（形状校验，比列宽 varchar(255) 更严）：
// 本插件的 driver 值是**注册表名**（openai/responses/dashscope_tts…），不是源侧的 PHP 类 FQN，
// 64 字符足够覆盖未来的注册项；列宽与源/厂商表同为 255，是为了不给跨表口径留两套宽度。
const driverNameMaxLen = 64

// channelWhereFields 是渠道表的 wheres 字段白名单（列名是已引用常量；api_key 刻意不在其中）。
var channelWhereFields = map[string]whereField{
	"id":                   {Column: `"id"`, Kind: whereInt},
	"name":                 {Column: `"name"`, Kind: whereText},
	"vendor":               {Column: `"vendor"`, Kind: whereText},
	"driver":               {Column: `"driver"`, Kind: whereText},
	"supplier_id":          {Column: `"supplier_id"`, Kind: whereInt},
	"rpm_limit":            {Column: `"rpm_limit"`, Kind: whereInt},
	"tpm_limit":            {Column: `"tpm_limit"`, Kind: whereInt},
	"base_url":             {Column: `"base_url"`, Kind: whereText},
	"model":                {Column: `"model"`, Kind: whereText},
	"timeout":              {Column: `"timeout"`, Kind: whereInt},
	"priority":             {Column: `"priority"`, Kind: whereInt},
	"weight":               {Column: `"weight"`, Kind: whereInt},
	"status":               {Column: `"status"`, Kind: whereBool01},
	"fail_threshold":       {Column: `"fail_threshold"`, Kind: whereInt},
	"cooldown_seconds":     {Column: `"cooldown_seconds"`, Kind: whereInt},
	"circuit":              {Column: `"circuit"`, Kind: whereText},
	"consecutive_failures": {Column: `"consecutive_failures"`, Kind: whereInt},
	"total_calls":          {Column: `"total_calls"`, Kind: whereInt},
	"total_failures":       {Column: `"total_failures"`, Kind: whereInt},
	"total_tokens":         {Column: `"total_tokens"`, Kind: whereInt},
	"last_error":           {Column: `"last_error"`, Kind: whereText},
	"remark":               {Column: `"remark"`, Kind: whereText},
	"create_time":          {Column: `"create_time"`, Kind: whereInt},
	"update_time":          {Column: `"update_time"`, Kind: whereInt},
}

// channelRow 是渠道行的出口形状：内嵌 Channel（其 APIKey 带 json:"-" ⇒ 永不序列化），
// 另给脱敏掩码、是否已配置、以及「生效阈值/冷却」（渠道为 0 时取插件配置，前端据此显示「全局默认」）。
type channelRow struct {
	Channel
	APIKeyMasked             string `json:"api_key"`
	HasAPIKey                bool   `json:"has_api_key"`
	EffectiveFailThreshold   int    `json:"effective_fail_threshold"`
	EffectiveCooldownSeconds int    `json:"effective_cooldown_seconds"`
}

// newChannelRow 出口脱敏（掩码与源 mask_secret 同口径；明文永不出接口）。
//
// Effective* 走 circuit.go 的求值链（渠道字段 → 插件配置 → 代码常量）：那是**运行时真正生效**
// 的口径，因此展示面与熔断行为不会各说各话（插件配置改了，这里回显的就是改后的值）。
func newChannelRow(row Channel, set pluginSettings) channelRow {
	return channelRow{
		Channel:                  row,
		APIKeyMasked:             maskSecret(row.APIKey),
		HasAPIKey:                strings.TrimSpace(row.APIKey) != "",
		EffectiveFailThreshold:   ResolveFailThreshold(row.FailThreshold, set.BreakerFailThreshold),
		EffectiveCooldownSeconds: ResolveCooldown(row.CooldownSeconds, set.BreakerCooldownSeconds),
	}
}

// newChannelRows 批量脱敏（列表与总览的异常样本共用）。
func newChannelRows(rows []Channel, set pluginSettings) []channelRow {
	out := make([]channelRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, newChannelRow(row, set))
	}
	return out
}

// effectiveSettings 取配置用于「全局默认」展示；读失败时回落代码默认（展示面不因读配置失败而报错）。
func (d deps) effectiveSettings(ctx context.Context) pluginSettings {
	set, err := d.loadSettings(ctx)
	if err != nil {
		return parsePluginSettings(nil)
	}
	return set
}

// ===== 列表 =====

// channelListRequest 渠道列表入参：宿主分页契约 + 顶层等值筛选 status/vendor（照源 Channel.php:43-50）。
type channelListRequest struct {
	listRequest
	Status optionalStatus `json:"status"`
	Vendor string         `json:"vendor"`
}

// filter 把请求收敛成 store 的筛选结构 + 已校验的 wheres 组。
func (r channelListRequest) filter() (ChannelFilter, []whereGroup, error) {
	if r.Status.Value != nil && !validStatus(*r.Status.Value) {
		return ChannelFilter{}, nil, errors.New("status 只能是 0（停用）或 1（启用）")
	}
	if err := validateSort(r.Sort, channelSortColumns, "渠道"); err != nil {
		return ChannelFilter{}, nil, err
	}
	groups, err := validateWhereGroups(channelWhereFields, r.Wheres)
	if err != nil {
		return ChannelFilter{}, nil, err
	}
	return ChannelFilter{
		Page:    r.Page,
		Limit:   r.Limit,
		Sort:    r.Sort,
		Order:   r.Order,
		Keyword: r.keyword(),
		Status:  r.Status.Value,
		Vendor:  r.Vendor,
	}, groups, nil
}

// channelList 渠道分页列表 + 四字段统计条（circuit_open / circuit_half_open / problem / fail_1h）。
func (d deps) channelList(c *gin.Context) {
	store := d.storeOrFail(c)
	if store == nil {
		return
	}
	var req channelListRequest
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
	var result ChannelListResult
	if len(groups) == 0 {
		result, err = store.ListChannels(ctx, filter)
	} else {
		result, err = store.listChannelsWithWheres(ctx, filter, groups)
	}
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("读取渠道列表失败: "+err.Error()))
		return
	}
	set := d.effectiveSettings(ctx)
	httpx.Success(c, httpx.WithData(gin.H{
		"list":  newChannelRows(result.Rows, set),
		"total": result.Total,
		"stats": result.Stats,
	}))
}

// listChannelsWithWheres 与 Store.ListChannels 同口径（同一基础筛选/排序/分页），只多一层 wheres。
// 条件由接口层拼装（store 的列表原语不接受 wheres），列名与操作符都已过白名单、值全参数化。
func (s *Store) listChannelsWithWheres(ctx context.Context, f ChannelFilter, groups []whereGroup) (ChannelListResult, error) {
	result := ChannelListResult{Rows: []Channel{}}
	if err := s.ready(); err != nil {
		return result, err
	}
	countQuery, err := applyWhereGroups(s.channelQuery(ctx, f), channelWhereFields, groups)
	if err != nil {
		return result, err
	}
	if err := countQuery.Count(&result.Total).Error; err != nil {
		return result, err
	}
	listQuery, err := applyWhereGroups(s.channelQuery(ctx, f), channelWhereFields, groups)
	if err != nil {
		return result, err
	}
	offset, limit := normalizePage(f.Page, f.Limit, DefaultChannelListLimit)
	order := orderClause(f.Sort, f.Order, channelSortColumns, `"priority" ASC, "id" DESC`)
	if err := listQuery.Order(order).Offset(offset).Limit(limit).Find(&result.Rows).Error; err != nil {
		return result, err
	}
	stats, err := s.ChannelStats(ctx, nowUnix())
	if err != nil {
		return result, err
	}
	result.Stats = stats
	return result, nil
}

// ===== 单行读取 =====

// channelGet 单行回填（含掩码 api_key；掩码含 '*' ⇒ 前端认作「不修改」）。
//
// 刻意读**渠道自身行**（GetChannel，非 GetChannelEffective）：表单必须能显示「留空」这一状态，
// 否则把继承来的厂商值回显成渠道值，一次保存就把继承悄悄固化成渠道自有凭证。
// 需要生效值的路径是探活与路由（见 channelProbe 与 store.Candidates）。
func (d deps) channelGet(c *gin.Context) {
	store := d.storeOrFail(c)
	if store == nil {
		return
	}
	id, err := parsePK(c, "渠道")
	if err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	ctx := c.Request.Context()
	row, err := store.GetChannel(ctx, id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		httpx.Fail(c, httpx.WithMessage(fmt.Sprintf("渠道 %d 不存在", id)))
		return
	}
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("读取渠道失败: "+err.Error()))
		return
	}
	httpx.Success(c, httpx.WithData(gin.H{"row": newChannelRow(*row, d.effectiveSettings(ctx))}))
}

// ===== 新增 / 保存 =====

// channelSaveRequest 是渠道新增/编辑的入参；指针字段把「缺省」与「显式零值」分开
// （api_key 的三态就是靠这个：缺省=不改 / ""=仍未配 / 含 '*'=掩码回填）。
type channelSaveRequest struct {
	Name            *string `json:"name"`
	Vendor          *string `json:"vendor"`
	Driver          *string `json:"driver"`
	SupplierID      *int64  `json:"supplier_id"`
	RPMLimit        *int    `json:"rpm_limit"`
	TPMLimit        *int    `json:"tpm_limit"`
	BaseURL         *string `json:"base_url"`
	APIKey          *string `json:"api_key"`
	Model           *string `json:"model"`
	Timeout         *int    `json:"timeout"`
	Priority        *int    `json:"priority"`
	Weight          *int    `json:"weight"`
	Status          *int16  `json:"status"`
	FailThreshold   *int    `json:"fail_threshold"`
	CooldownSeconds *int    `json:"cooldown_seconds"`
	Remark          *string `json:"remark"`
}

// channelCreate 新增渠道：未配凭证允许（调用时 fail-closed 报「渠道未配置 api_key」）。
func (d deps) channelCreate(c *gin.Context) {
	store := d.storeOrFail(c)
	if store == nil {
		return
	}
	var req channelSaveRequest
	if err := bindJSON(c, &req); err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	row, err := req.channel()
	if err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	if err := store.CreateChannel(c.Request.Context(), row); err != nil {
		if isUniqueViolation(err) {
			httpx.Fail(c, httpx.WithMessage(fmt.Sprintf("渠道名 %q 已存在（name 唯一）", row.Name)))
			return
		}
		httpx.Fail(c, httpx.WithMessage("新增渠道失败: "+redactSecret(err.Error(), row.APIKey)))
		return
	}
	httpx.Success(c, httpx.WithData(gin.H{"id": row.ID, "name": row.Name}))
}

// channelUpdate 保存渠道：api_key 为空串或含 '*' 一律视为不修改；不动熔断状态与累计统计。
func (d deps) channelUpdate(c *gin.Context) {
	store := d.storeOrFail(c)
	if store == nil {
		return
	}
	id, err := parsePK(c, "渠道")
	if err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	var req channelSaveRequest
	if err := bindJSON(c, &req); err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	ctx := c.Request.Context()
	// 「留空 base_url = 继承厂商」的准入要判**生效供应商**：本次补丁带了 supplier_id 就用它的值，
	// 没带就得读现行行（patch 是纯函数，读不到库）。
	effectiveSupplierID := int64(0)
	if req.BaseURL != nil && strings.TrimSpace(*req.BaseURL) == "" {
		supplierID, err := resolveInheritSupplierID(ctx, store, id, req.SupplierID)
		if err != nil {
			httpx.Fail(c, httpx.WithMessage("读取渠道失败: "+err.Error()))
			return
		}
		effectiveSupplierID = supplierID
	}
	patch, err := req.patchForSupplier(effectiveSupplierID)
	if err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	affected, err := store.UpdateChannel(ctx, id, patch)
	if err != nil {
		if isUniqueViolation(err) {
			httpx.Fail(c, httpx.WithMessage("渠道名已存在（name 唯一）"))
			return
		}
		httpx.Fail(c, httpx.WithMessage("保存渠道失败: "+redactSecret(err.Error(), patchAPIKey(patch))))
		return
	}
	if affected == 0 && !channelExists(ctx, store, id) {
		httpx.Fail(c, httpx.WithMessage(fmt.Sprintf("渠道 %d 不存在", id)))
		return
	}
	httpx.Success(c, httpx.WithData(gin.H{"id": id, "affected": affected}))
}

// resolveInheritSupplierID 取「清空 base_url 时的生效供应商」：补丁带 supplier_id 用它，否则读现行行。
//
// 行不存在时返回 0（由既有「渠道 %d 不存在」分支收口，这里不抢先报错）；读库失败原样上抛给调用方。
func resolveInheritSupplierID(ctx context.Context, store *Store, id int64, patchSupplierID *int64) (int64, error) {
	if patchSupplierID != nil {
		return *patchSupplierID, nil
	}
	row, err := store.GetChannel(ctx, id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return row.SupplierID, nil
}

// ===== 删除 / 启停 =====

// channelDelete 批量删除（同一事务内先删绑定再删渠道行）。
func (d deps) channelDelete(c *gin.Context) {
	store := d.storeOrFail(c)
	if store == nil {
		return
	}
	var req pksRequest
	if err := bindJSON(c, &req); err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	ids, err := req.ids("渠道")
	if err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	if err := store.DeleteChannels(c.Request.Context(), ids); err != nil {
		httpx.Fail(c, httpx.WithMessage("删除渠道失败: "+err.Error()))
		return
	}
	httpx.Success(c, httpx.WithData(gin.H{"deleted": len(ids)}))
}

// channelActionRequest 行内动作的共同入参（id + 动作私有字段）。
type channelActionRequest struct {
	ID     int64  `json:"id"`
	State  string `json:"state"`  // breaker：open|close
	Status *int16 `json:"status"` // switch：0|1
}

// channelSwitch 渠道启停：只改 status，不动熔断字段与累计统计。
func (d deps) channelSwitch(c *gin.Context) {
	store := d.storeOrFail(c)
	if store == nil {
		return
	}
	var req channelActionRequest
	if err := bindJSON(c, &req); err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	if req.ID <= 0 {
		httpx.Fail(c, httpx.WithMessage("渠道 ID 必填"))
		return
	}
	if req.Status == nil || !validStatus(*req.Status) {
		httpx.Fail(c, httpx.WithMessage("status 只能是 0（停用）或 1（启用）"))
		return
	}
	ctx := c.Request.Context()
	affected, err := store.SwitchChannel(ctx, req.ID, *req.Status)
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("渠道启停失败: "+err.Error()))
		return
	}
	if affected == 0 && !channelExists(ctx, store, req.ID) {
		httpx.Fail(c, httpx.WithMessage(fmt.Sprintf("渠道 %d 不存在", req.ID)))
		return
	}
	httpx.Success(c, httpx.WithData(gin.H{"id": req.ID, "status": *req.Status, "affected": affected}))
}

// ===== 探活 =====

// probeTimeoutSeconds 探活超时：clamp(渠道生效 timeout, 3, 15) 秒（照源 ChannelProbe.php:33-35）。
func probeTimeoutSeconds(effective int) int {
	return clampInt(effective, 3, 15)
}

// channelProbe 手动探活：GET {base_url}/models，2xx 视为成功。
//
// 经统一驱动（同一 http.Client 工厂 ⇒ 同受 safedial 内网护栏与禁重定向）；探活超时独立于渠道
// timeout（3~15s），否则交互式探活会被前端默认 10s 打断而库里已写结果。探活只写 last_probe_*
// 与 probe_fail_streak：绝不触发熔断计数与 usage 记账。
//
// 两条继承来的硬约束：
//   - 读的是**生效行**（GetChannelEffective：渠道非空优先、空则取厂商），探活与真实调用因此
//     看到同一份 base_url/api_key/driver/timeout（源侧同款语义见 ChannelProbe::effectiveCredential）。
//     渠道把凭证留空挂厂商时，探活不会拿空凭证误报 fail-closed；
//   - 驱动只按 **Driver 接口**取用（下面显式声明接口类型）：探活不依赖任何具体驱动实现，
//     装配侧把 sharedDriver 的返回类型换成按 ch.Driver 分派的注册表后，本路径自动获得分派
//     （driver='responses' 的渠道不再恒走 OpenAI 协议）。探活对驱动的全部依赖面 = Models(ctx, ch)。
func (d deps) channelProbe(c *gin.Context) {
	store := d.storeOrFail(c)
	if store == nil {
		return
	}
	var req channelActionRequest
	if err := bindJSON(c, &req); err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	if req.ID <= 0 {
		httpx.Fail(c, httpx.WithMessage("渠道 ID 必填"))
		return
	}
	ctx := c.Request.Context()
	row, err := store.GetChannelEffective(ctx, req.ID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		httpx.Fail(c, httpx.WithMessage(fmt.Sprintf("渠道 %d 不存在", req.ID)))
		return
	}
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("读取渠道失败: "+err.Error()))
		return
	}

	// 读配置失败按代码默认执行（默认 allow_private_hosts=0 ⇒ 私网上游一律被护栏拦下，fail-closed）。
	set := d.effectiveSettings(ctx)
	effective := row.Timeout
	if effective <= 0 {
		effective = set.RequestTimeout
	}
	timeoutSeconds := probeTimeoutSeconds(effective)

	// 显式声明接口类型：这是「探活只依赖 Driver 接口」的编译期保证（具体驱动由装配侧决定）。
	// 装配面把「私网开关 + SSE 三级上限 + 注册集合指纹」并进同一缓存键：探活与转发
	// 因此共用同一注册表实例（分派口径与连接池都同源）。
	var driver Driver = sharedDriver(set.AllowPrivateHosts, set.sseLimits())
	probeChannel := *row
	probeChannel.Timeout = timeoutSeconds

	probeCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSeconds)*time.Second)
	defer cancel()

	start := time.Now()
	models, probeErr := driver.Models(probeCtx, probeChannel)
	latency := int(time.Since(start).Milliseconds())

	ok := probeErr == nil
	message := ""
	httpStatus := 0
	if probeErr != nil {
		var upstream *UpstreamError
		if errors.As(probeErr, &upstream) {
			httpStatus = upstream.HTTPStatus
		}
		message = truncateRunes(probeErr.Error(), 500)
	}
	at := nowUnix()
	if _, err := store.SetProbeResult(ctx, req.ID, ok, latency, message, at); err != nil {
		httpx.Fail(c, httpx.WithMessage("探活结果写回失败: "+redactSecret(err.Error(), row.APIKey)))
		return
	}
	httpx.Success(c, httpx.WithData(gin.H{
		"id":                    req.ID,
		"ok":                    ok,
		"latency_ms":            latency,
		"http_status":           httpStatus,
		"error":                 message,
		"models_count":          len(models),
		"probe_timeout_seconds": timeoutSeconds,
		"last_probe_at":         at,
	}))
}

// ===== 人工熔断 / 恢复 =====

// channelBreaker 人工熔断与恢复（单节点两动作，等价源的 forceCircuit + resetBreaker）：
//
//	state=open  ⇒ circuit=open + opened_at=now + manual_open 事件（已 open 报「渠道已在熔断中」）
//	state=close ⇒ circuit=closed + consecutive_failures=0 + opened_at=NULL + manual_close 事件
//
// 两者都不改 total_calls / total_failures / total_tokens，也不伪造 consecutive_failures。
func (d deps) channelBreaker(c *gin.Context) {
	store := d.storeOrFail(c)
	if store == nil {
		return
	}
	var req channelActionRequest
	if err := bindJSON(c, &req); err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	if req.ID <= 0 {
		httpx.Fail(c, httpx.WithMessage("渠道 ID 必填"))
		return
	}
	ctx := c.Request.Context()
	row, err := store.GetChannel(ctx, req.ID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		httpx.Fail(c, httpx.WithMessage(fmt.Sprintf("渠道 %d 不存在", req.ID)))
		return
	}
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("读取渠道失败: "+err.Error()))
		return
	}

	// 熔断事件里的阈值/冷却是「触发时生效值**快照**」⇒ 必须带上插件配置的全局默认，
	// 否则人工熔断写下的快照与运行时（Service 里的 Circuit）回显的会是两套值。
	set := d.effectiveSettings(ctx)
	circuit := NewCircuit(store, CircuitDefaults{
		FailThreshold:   set.BreakerFailThreshold,
		CooldownSeconds: set.BreakerCooldownSeconds,
	})
	state := strings.ToLower(strings.TrimSpace(req.State))
	switch state {
	case "open":
		err = circuit.ForceOpen(ctx, *row, nowUnix())
	case "close":
		err = circuit.Reset(ctx, *row, nowUnix())
	default:
		httpx.Fail(c, httpx.WithMessage(`state 只能是 open（人工熔断）或 close（恢复）`))
		return
	}
	if errors.Is(err, ErrAlreadyOpen) {
		httpx.Fail(c, httpx.WithMessage(ErrAlreadyOpen.Error()))
		return
	}
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("人工熔断操作失败: "+err.Error()))
		return
	}
	httpx.Success(c, httpx.WithData(gin.H{"id": req.ID, "state": state}))
}

// ===== 熔断事件 =====

// channelEvents 单渠道最近 20 条熔断事件（按 id DESC）。
func (d deps) channelEvents(c *gin.Context) {
	store := d.storeOrFail(c)
	if store == nil {
		return
	}
	var req channelActionRequest
	if err := bindJSON(c, &req); err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	if req.ID <= 0 {
		httpx.Fail(c, httpx.WithMessage("渠道 ID 必填"))
		return
	}
	rows, err := store.ListBreakerEvents(c.Request.Context(), req.ID, BreakerEventListLimit)
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("读取熔断事件失败: "+err.Error()))
		return
	}
	httpx.Success(c, httpx.WithData(gin.H{"list": rows}))
}

// ===== 入参收敛（纯函数，可单测）=====

// channel 组装新行：必填校验 + 白名单收敛 + 数值 clamp（create）。
//
// base_url 的留空口径与源一致（Channel.php:89 只在 supplier_id<=0 时才要求 base_url/api_key）：
// **挂厂商时可以留空**（运行时继承厂商地址，mergeSupplierInheritance 一份实现），没挂厂商留空仍拒。
func (r channelSaveRequest) channel() (*Channel, error) {
	name, err := requiredText(r.Name, "渠道名", 50)
	if err != nil {
		return nil, err
	}
	model, err := requiredText(r.Model, "上游模型名", 100)
	if err != nil {
		return nil, err
	}
	supplierID, err := normalizeSupplierID(derefInt64(r.SupplierID))
	if err != nil {
		return nil, err
	}
	baseURL := strings.TrimSpace(derefString(r.BaseURL))
	if baseURL != "" || supplierID <= 0 {
		if err := ValidateBaseURL(baseURL); err != nil {
			return nil, fmt.Errorf("base_url 不合法: %w", err)
		}
	}
	vendor, err := normalizeVendor(r.Vendor)
	if err != nil {
		return nil, err
	}
	driver, err := normalizeDriverName(r.Driver)
	if err != nil {
		return nil, err
	}
	status := StatusEnabled
	if r.Status != nil {
		if !validStatus(*r.Status) {
			return nil, errors.New("status 只能是 0（停用）或 1（启用）")
		}
		status = *r.Status
	}
	return &Channel{
		Name:            name,
		Vendor:          vendor,
		Driver:          driver,
		SupplierID:      supplierID,
		RPMLimit:        nonNegative(derefInt(r.RPMLimit, 0)),
		TPMLimit:        nonNegative(derefInt(r.TPMLimit, 0)),
		BaseURL:         baseURL,
		APIKey:          strings.TrimSpace(derefString(r.APIKey)),
		Model:           model,
		Timeout:         clampInt(derefInt(r.Timeout, DefaultRequestTimeout), 0, 600),
		Priority:        nonNegative(derefInt(r.Priority, 1)),
		Weight:          nonNegative(derefInt(r.Weight, 1)),
		Status:          status,
		FailThreshold:   nonNegative(derefInt(r.FailThreshold, 0)),
		CooldownSeconds: nonNegative(derefInt(r.CooldownSeconds, 0)),
		Remark:          truncateRunes(strings.TrimSpace(derefString(r.Remark)), 255),
	}, nil
}

// patch 组装编辑补丁：等价 patchForSupplier(0)（不挂厂商 ⇒ 清空 base_url 依旧被拒）。
func (r channelSaveRequest) patch() (ChannelPatch, error) {
	return r.patchForSupplier(0)
}

// patchForSupplier 是 patch 的实现：supplierID 是**生效供应商**（本次补丁带 supplier_id 时是它的值，
// 否则由 channelUpdate 读现行行给出）——挂厂商（>0）时允许把 base_url 清空（运行时继承厂商地址，
// 与源 Channel.php:89 的「挂厂商可留空」同口径）；不挂厂商时留空仍是「base_url 不能为空」。
func (r channelSaveRequest) patchForSupplier(supplierID int64) (ChannelPatch, error) {
	patch := ChannelPatch{}
	if r.Name != nil {
		name, err := requiredText(r.Name, "渠道名", 50)
		if err != nil {
			return patch, err
		}
		patch.Name = &name
	}
	if r.Model != nil {
		model, err := requiredText(r.Model, "上游模型名", 100)
		if err != nil {
			return patch, err
		}
		patch.Model = &model
	}
	if r.BaseURL != nil {
		baseURL := strings.TrimSpace(*r.BaseURL)
		// 留空且非挂厂商 ⇒ 拒绝（文案与早期实现逐字一致：base_url 不合法: base_url 不能为空）；
		// 留空且挂厂商 ⇒ 放行（清空 = 回归继承厂商），非空则照旧走格式白名单。
		if baseURL != "" || supplierID <= 0 {
			if err := ValidateBaseURL(baseURL); err != nil {
				return patch, fmt.Errorf("base_url 不合法: %w", err)
			}
		}
		patch.BaseURL = &baseURL
	}
	if r.Vendor != nil {
		vendor, err := normalizeVendor(r.Vendor)
		if err != nil {
			return patch, err
		}
		patch.Vendor = &vendor
	}
	if r.Driver != nil {
		driver, err := normalizeDriverName(r.Driver)
		if err != nil {
			return patch, err
		}
		patch.Driver = &driver
	}
	if r.SupplierID != nil {
		supplierID, err := normalizeSupplierID(*r.SupplierID)
		if err != nil {
			return patch, err
		}
		patch.SupplierID = &supplierID
	}
	if r.RPMLimit != nil {
		rpm := nonNegative(*r.RPMLimit)
		patch.RPMLimit = &rpm
	}
	if r.TPMLimit != nil {
		tpm := nonNegative(*r.TPMLimit)
		patch.TPMLimit = &tpm
	}
	if r.APIKey != nil && !looksMasked(*r.APIKey) {
		key := strings.TrimSpace(*r.APIKey)
		patch.APIKey = &key
	}
	if r.Timeout != nil {
		timeout := clampInt(*r.Timeout, 0, 600)
		patch.Timeout = &timeout
	}
	if r.Priority != nil {
		priority := nonNegative(*r.Priority)
		patch.Priority = &priority
	}
	if r.Weight != nil {
		weight := nonNegative(*r.Weight)
		patch.Weight = &weight
	}
	if r.Status != nil {
		if !validStatus(*r.Status) {
			return patch, errors.New("status 只能是 0（停用）或 1（启用）")
		}
		status := *r.Status
		patch.Status = &status
	}
	if r.FailThreshold != nil {
		threshold := nonNegative(*r.FailThreshold)
		patch.FailThreshold = &threshold
	}
	if r.CooldownSeconds != nil {
		cooldown := nonNegative(*r.CooldownSeconds)
		patch.CooldownSeconds = &cooldown
	}
	if r.Remark != nil {
		remark := truncateRunes(strings.TrimSpace(*r.Remark), 255)
		patch.Remark = &remark
	}
	return patch, nil
}

// validStatus 二值状态白名单（非 0/1 一律拒）。
func validStatus(status int16) bool {
	return status == StatusEnabled || status == StatusDisabled
}

// normalizeVendor 收敛 vendor：空值取默认 openai（前端表单默认值），其余必须命中闭集。
func normalizeVendor(vendor *string) (string, error) {
	value := strings.TrimSpace(derefString(vendor))
	if value == "" {
		return "openai", nil
	}
	for _, allowed := range channelVendors {
		if value == allowed {
			return value, nil
		}
	}
	return "", fmt.Errorf("vendor %q 不在白名单内（允许：%s）", value, strings.Join(channelVendors, "|"))
}

// normalizeDriverName 收敛驱动名：允许空串（= 未指定，调用期回落默认「OpenAI 兼容」协议），
// 其余只做形状校验（trim、长度、标识符字符），不查闭集——闭集是驱动注册表的职责
// （未知名字在调用期由注册表返回明确错误，不静默回落）。
func normalizeDriverName(driver *string) (string, error) {
	value := strings.TrimSpace(derefString(driver))
	if value == "" {
		return "", nil
	}
	if len([]rune(value)) > driverNameMaxLen {
		return "", fmt.Errorf("driver 最长 %d 个字符", driverNameMaxLen)
	}
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.' {
			continue
		}
		return "", fmt.Errorf("driver %q 含非法字符（只允许字母、数字、_ - .）", value)
	}
	return value, nil
}

// normalizeSupplierID 收敛厂商 ID：0=独立渠道；负数拒（列上有 CHECK，先在接口层给可读错误）。
// 刻意**不**校验该厂商是否存在：源侧 Supplier::find 未命中即视为「无从继承」，不报错；
// 而「不许删除仍被渠道引用的厂商」是删除保护职责（写入口只需形状合法）。
func normalizeSupplierID(id int64) (int64, error) {
	if id < 0 {
		return 0, errors.New("supplier_id 不能为负数（0=独立渠道）")
	}
	return id, nil
}

// requiredText 必填文本 + 长度上限（varchar 计字符）。
func requiredText(value *string, label string, max int) (string, error) {
	text := strings.TrimSpace(derefString(value))
	if text == "" {
		return "", fmt.Errorf("%s必填", label)
	}
	if len([]rune(text)) > max {
		return "", fmt.Errorf("%s最长 %d 个字符", label, max)
	}
	return text, nil
}

// nonNegative 负数收敛为 0（数值列的 max(0) 口径）。
func nonNegative(value int) int {
	if value < 0 {
		return 0
	}
	return value
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func derefInt(value *int, fallback int) int {
	if value == nil {
		return fallback
	}
	return *value
}

func derefInt64(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

// channelExists 存在性复核：PG 在「值未变化」时也可能报 RowsAffected=0，不能只凭它判「不存在」。
func channelExists(ctx context.Context, store *Store, id int64) bool {
	_, err := store.GetChannel(ctx, id)
	return err == nil
}

// isUniqueViolation 判定唯一索引冲突（PG SQLSTATE 23505 + 驱动文案兜底）。
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "23505") ||
		strings.Contains(message, "duplicate key") ||
		strings.Contains(message, "unique constraint")
}
