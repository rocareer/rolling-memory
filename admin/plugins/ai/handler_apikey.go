// plugin: ai v0.2.0
// handler_apikey.go 是应用接入（API Key）的 7 个后台端点：
//
//	POST /apikey/list        列表（分页 + 快搜 name + 顶层 status/source + wheres 白名单）
//	GET  /apikey/get/:pk     单行回填（只有 key_prefix，**永不含 key_hash / 明文**）
//	POST /apikey/create      签发（回执含明文一次）
//	POST /apikey/update/:pk  保存（name/source/model_scope/rate_limit/expire_at/status/remark）
//	POST /apikey/delete      批量删除
//	POST /apikey/reset/:pk   重置（换新明文，旧 key 立即失效；回执含明文一次）
//	POST /apikey/switch      启停
//
// 出口纪律（照源 ApiKey.php 的安全约定）：
//   - key_hash 永不出口（APIKey.KeyHash 带 json:"-"，本文件也不把它放进任何 gin.H）；
//   - 明文只在 create / reset 的回执出现**一次**（apiKeyIssued 是它唯一的出口形状）；
//   - list/get 只回 key_prefix（前 12 字符 + …）。
//
// 与源的差异（如实登记）：
//   - source 可改（源 edit 明确「source 与 key 不可改」）：本插件的验收要求「source >24 字符在
//     create/update 都被拒」，即 update 面也接受 source；改了 source 之后，**历史用量仍留在旧
//     source 上**（用量按发生时的归因落行），新调用按新 source 计——运维须知。
//   - rate_limit 无「最低 5」约束（源：rateLimit>0 && <5 ⇒ 报错）：本插件的按 Key 覆盖验收用例就是
//     rate_limit=2（第 3 次必须 429），最低值约束会把它挡在签发环节；0 = 跟随全局的口径两边一致。
//   - 无 access_type / monthly_quota 列：本插件 v1 只有「凭证明细 + 模型白名单 + 限流覆盖」三件，
//     接入方式与月度配额属独立能力（见交付 risks 的 scopeOut）。
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

// apiKeyWhereFields 是应用接入表的 wheres 字段白名单（列名是已引用常量）。
// key_hash 刻意不在其中（密钥摘要不给可查/可探测入口）；model_scope 是 jsonb，不作条件
// （与 handler_model.go:28 对 capabilities 的口径一致）。
var apiKeyWhereFields = map[string]whereField{
	"id":           {Column: `"id"`, Kind: whereInt},
	"name":         {Column: `"name"`, Kind: whereText},
	"key_prefix":   {Column: `"key_prefix"`, Kind: whereText},
	"source":       {Column: `"source"`, Kind: whereText},
	"rate_limit":   {Column: `"rate_limit"`, Kind: whereInt},
	"status":       {Column: `"status"`, Kind: whereBool01},
	"expire_at":    {Column: `"expire_at"`, Kind: whereInt},
	"last_used_at": {Column: `"last_used_at"`, Kind: whereInt},
	"remark":       {Column: `"remark"`, Kind: whereText},
	"create_time":  {Column: `"create_time"`, Kind: whereInt},
	"update_time":  {Column: `"update_time"`, Kind: whereInt},
}

// apiKeyRow 是应用接入行的出口形状：内嵌 APIKey（KeyHash 带 json:"-" ⇒ 永不序列化），
// 另给「是否已过期」（照源 index 出参的 expired 标记，前端据此显示红色标签）。
type apiKeyRow struct {
	APIKey
	Expired bool `json:"expired"`
}

// newAPIKeyRow 单行出口（now ≤ 0 时取当前时刻）。
func newAPIKeyRow(row APIKey, now int64) apiKeyRow {
	return apiKeyRow{APIKey: row, Expired: row.IsExpired(now)}
}

// newAPIKeyRows 批量出口（列表与详情共用同一形状）。
func newAPIKeyRows(rows []APIKey, now int64) []apiKeyRow {
	out := make([]apiKeyRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, newAPIKeyRow(row, now))
	}
	return out
}

// apiKeyIssued 是**明文唯一的出口形状**（create / reset 共用）：明文只在这里出现一次，
// 其余任何出口（list/get/日志/错误文案）都不含明文。
func apiKeyIssued(row *APIKey, plain string) gin.H {
	return gin.H{
		"id":         row.ID,
		"api_key":    plain,
		"key_prefix": row.KeyPrefix,
		"source":     row.Source,
	}
}

// ===== 列表 =====

// apiKeyListRequest 列表入参：宿主分页契约 + 顶层等值筛选 status/source（照源 ApiKey.php index）。
type apiKeyListRequest struct {
	listRequest
	Status optionalStatus `json:"status"`
	Source string         `json:"source"`
}

// filter 把请求收敛成 store 的筛选结构 + 已校验的 wheres 组。
func (r apiKeyListRequest) filter() (APIKeyFilter, []whereGroup, error) {
	if r.Status.Value != nil && !validStatus(*r.Status.Value) {
		return APIKeyFilter{}, nil, errors.New("status 只能是 0（停用）或 1（启用）")
	}
	if err := validateSort(r.Sort, apiKeySortColumns, "应用接入"); err != nil {
		return APIKeyFilter{}, nil, err
	}
	groups, err := validateWhereGroups(apiKeyWhereFields, r.Wheres)
	if err != nil {
		return APIKeyFilter{}, nil, err
	}
	return APIKeyFilter{
		Page:    r.Page,
		Limit:   r.Limit,
		Sort:    r.Sort,
		Order:   r.Order,
		Keyword: r.keyword(),
		Source:  r.Source,
		Status:  r.Status.Value,
	}, groups, nil
}

// apiKeyList 应用接入分页列表（key_prefix 是唯一可见的 key 形态）。
func (d deps) apiKeyList(c *gin.Context) {
	store := d.storeOrFail(c)
	if store == nil {
		return
	}
	var req apiKeyListRequest
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
	var result APIKeyListResult
	if len(groups) == 0 {
		result, err = store.ListAPIKeys(ctx, filter)
	} else {
		result, err = store.listAPIKeysWithWheres(ctx, filter, groups)
	}
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("读取应用接入列表失败: "+err.Error()))
		return
	}
	httpx.Success(c, httpx.WithData(gin.H{
		"list":  newAPIKeyRows(result.Rows, nowUnix()),
		"total": result.Total,
	}))
}

// ===== 单行读取 =====

// apiKeyGet 单行回填（只有 key_prefix；明文不在此出口）。
func (d deps) apiKeyGet(c *gin.Context) {
	store := d.storeOrFail(c)
	if store == nil {
		return
	}
	id, err := parsePK(c, "应用接入")
	if err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	row, err := store.GetAPIKey(c.Request.Context(), id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		httpx.Fail(c, httpx.WithMessage(fmt.Sprintf("应用接入 %d 不存在", id)))
		return
	}
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("读取应用接入失败: "+err.Error()))
		return
	}
	httpx.Success(c, httpx.WithData(gin.H{"row": newAPIKeyRow(*row, nowUnix())}))
}

// ===== 新增 / 保存 =====

// apiKeySaveRequest 是签发/编辑的入参。
//
// ModelScope 用 json.RawMessage 承载三态（字段缺省 = 不修改 / null 或 [] = 清空 / 数组 = 设置）：
// 用 *[]string 表达不了「显式清空」与「缺省」的区别。
type apiKeySaveRequest struct {
	Name       *string         `json:"name"`
	Source     *string         `json:"source"`
	ModelScope json.RawMessage `json:"model_scope"`
	RateLimit  *int            `json:"rate_limit"`
	ExpireAt   *int64          `json:"expire_at"`
	Status     *int16          `json:"status"`
	Remark     *string         `json:"remark"`
}

// scopePatch 解析 model_scope 的三态：nil = 不修改；非 nil 指针 = 要写入的值（空切片 = 清空）。
func (r apiKeySaveRequest) scopePatch() (*[]string, error) {
	if len(r.ModelScope) == 0 {
		return nil, nil
	}
	var raw any
	if err := json.Unmarshal(r.ModelScope, &raw); err != nil {
		return nil, errors.New("model_scope 不是合法 JSON")
	}
	scope, err := NormalizeModelScope(raw)
	if err != nil {
		return nil, err
	}
	if scope == nil {
		scope = []string{}
	}
	return &scope, nil
}

// normalizeExpireAt 收敛过期时间：缺省 / 0 / 负数 ⇒ nil（NULL = 永不过期；与源 expireAt>0?…:null 同口径）。
func normalizeExpireAt(value *int64) *int64 {
	if value == nil || *value <= 0 {
		return nil
	}
	at := *value
	return &at
}

// issue 签发新行：必填校验 + 白名单收敛 + 生成明文（create 用；纯函数，可单测）。
// 返回的第二项是**明文**，只允许出现在回执里。
func (r apiKeySaveRequest) issue() (*APIKey, string, error) {
	name, err := requiredText(r.Name, "应用名", apiKeyMaxName)
	if err != nil {
		return nil, "", err
	}
	source, err := ValidateAPIKeySource(derefString(r.Source))
	if err != nil {
		return nil, "", err
	}
	scope, err := r.scopePatch()
	if err != nil {
		return nil, "", err
	}
	// 新建恒为启用（照源 ApiKey::add 硬写 STATUS_ENABLED）：停用走启停接口。
	// 显式拒绝 status=0 而不是静默忽略——「以为自己建了个停用的 Key」是最坏的一种静默。
	//
	// 这条纪律另有一层 GORM 原因：status 列带 default:1，Create 会省略「带 default 标签的零值字段」
	// （渠道表同款陷阱，store_db_test.go:131-134 有登记）⇒ 传 0 进去落库也会变成 1。
	if r.Status != nil {
		if !validStatus(*r.Status) {
			return nil, "", errors.New("status 只能是 0（停用）或 1（启用）")
		}
		if *r.Status == StatusDisabled {
			return nil, "", errors.New("新建应用恒为启用（照源的签发口径）：建好后用启停接口停用")
		}
	}
	plain, err := GenerateAPIKey()
	if err != nil {
		return nil, "", err
	}
	var modelScope []string
	if scope != nil {
		modelScope = *scope
	}
	return &APIKey{
		Name:       name,
		KeyHash:    HashAPIKey(plain),
		KeyPrefix:  APIKeyDisplayPrefix(plain),
		Source:     source,
		ModelScope: modelScope,
		RateLimit:  nonNegative(derefInt(r.RateLimit, 0)),
		Status:     StatusEnabled,
		ExpireAt:   normalizeExpireAt(r.ExpireAt),
		Remark:     truncateRunes(strings.TrimSpace(derefString(r.Remark)), apiKeyMaxRemark),
	}, plain, nil
}

// patch 组装编辑补丁：只带出现过的字段（key_hash / key_prefix 永不在补丁里——换 key 只走 reset）。
func (r apiKeySaveRequest) patch() (APIKeyPatch, error) {
	patch := APIKeyPatch{}
	if r.Name != nil {
		name, err := requiredText(r.Name, "应用名", apiKeyMaxName)
		if err != nil {
			return patch, err
		}
		patch.Name = &name
	}
	if r.Source != nil {
		source, err := ValidateAPIKeySource(*r.Source)
		if err != nil {
			return patch, err
		}
		patch.Source = &source
	}
	scope, err := r.scopePatch()
	if err != nil {
		return patch, err
	}
	patch.ModelScope = scope
	if r.RateLimit != nil {
		rateLimit := nonNegative(*r.RateLimit)
		patch.RateLimit = &rateLimit
	}
	if r.ExpireAt != nil {
		expireAt := int64(0)
		if *r.ExpireAt > 0 {
			expireAt = *r.ExpireAt
		}
		patch.ExpireAt = &expireAt
	}
	if r.Status != nil {
		if !validStatus(*r.Status) {
			return patch, errors.New("status 只能是 0（停用）或 1（启用）")
		}
		status := *r.Status
		patch.Status = &status
	}
	if r.Remark != nil {
		remark := truncateRunes(strings.TrimSpace(*r.Remark), apiKeyMaxRemark)
		patch.Remark = &remark
	}
	return patch, nil
}

// apiKeyUniqueMessage 把唯一键冲突翻成可读文案（uk_source / uk_key_hash 分别命中）。
// 空串表示「不是唯一键冲突」，调用方据此回落通用文案。
func apiKeyUniqueMessage(err error, source string) string {
	if !isUniqueViolation(err) {
		return ""
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "api_key_source"):
		return fmt.Sprintf("source「%s」已被其他应用绑定（一个调用方一个 key）", source)
	case strings.Contains(message, "api_key_key_hash"):
		return "该 API Key 已存在（key_hash 唯一）——请重新签发"
	default:
		return "唯一键冲突（source 或 key_hash 与既有行重复），请刷新后重试"
	}
}

// apiKeyCreate 签发应用：回执含明文一次（库里只落 sha256），source 并发冲突翻成可读文案。
func (d deps) apiKeyCreate(c *gin.Context) {
	store := d.storeOrFail(c)
	if store == nil {
		return
	}
	var req apiKeySaveRequest
	if err := bindJSON(c, &req); err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	row, plain, err := req.issue()
	if err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	if err := store.CreateAPIKey(c.Request.Context(), row); err != nil {
		if message := apiKeyUniqueMessage(err, row.Source); message != "" {
			httpx.Fail(c, httpx.WithMessage(message))
			return
		}
		httpx.Fail(c, httpx.WithMessage("签发应用失败: "+err.Error()))
		return
	}
	httpx.Success(c, httpx.WithMessage("签发成功（明文仅显示一次，请立即保存）"),
		httpx.WithData(apiKeyIssued(row, plain)))
}

// apiKeyUpdate 保存应用：不触碰 key_hash / key_prefix（换 key 只走 reset）。
func (d deps) apiKeyUpdate(c *gin.Context) {
	store := d.storeOrFail(c)
	if store == nil {
		return
	}
	id, err := parsePK(c, "应用接入")
	if err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	var req apiKeySaveRequest
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
	affected, err := store.UpdateAPIKey(ctx, id, patch)
	if err != nil {
		source := ""
		if patch.Source != nil {
			source = *patch.Source
		}
		if message := apiKeyUniqueMessage(err, source); message != "" {
			httpx.Fail(c, httpx.WithMessage(message))
			return
		}
		httpx.Fail(c, httpx.WithMessage("保存应用失败: "+err.Error()))
		return
	}
	if affected == 0 && !apiKeyExists(ctx, store, id) {
		httpx.Fail(c, httpx.WithMessage(fmt.Sprintf("应用接入 %d 不存在", id)))
		return
	}
	httpx.Success(c, httpx.WithData(gin.H{"id": id, "affected": affected}))
}

// apiKeyExists 存在性复核：PG 在「值未变化」时也可能报 RowsAffected=0，不能只凭它判「不存在」。
func apiKeyExists(ctx context.Context, store *Store, id int64) bool {
	_, err := store.GetAPIKey(ctx, id)
	return err == nil
}

// ===== 删除 / 重置 / 启停 =====

// apiKeyDelete 批量删除（删除后其 Key 立即失效：本表无缓存）。
func (d deps) apiKeyDelete(c *gin.Context) {
	store := d.storeOrFail(c)
	if store == nil {
		return
	}
	var req pksRequest
	if err := bindJSON(c, &req); err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	ids, err := req.ids("应用接入")
	if err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	if err := store.DeleteAPIKeys(c.Request.Context(), ids); err != nil {
		httpx.Fail(c, httpx.WithMessage("删除应用失败: "+err.Error()))
		return
	}
	httpx.Success(c, httpx.WithData(gin.H{"deleted": len(ids)}))
}

// apiKeyReset 重置：换新的明文 key（回执含明文一次），旧 key 立即失效。
func (d deps) apiKeyReset(c *gin.Context) {
	store := d.storeOrFail(c)
	if store == nil {
		return
	}
	id, err := parsePK(c, "应用接入")
	if err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	ctx := c.Request.Context()
	row, err := store.GetAPIKey(ctx, id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		httpx.Fail(c, httpx.WithMessage(fmt.Sprintf("应用接入 %d 不存在", id)))
		return
	}
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("读取应用接入失败: "+err.Error()))
		return
	}
	plain, err := GenerateAPIKey()
	if err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	hash, prefix := HashAPIKey(plain), APIKeyDisplayPrefix(plain)
	if _, err := store.RotateAPIKey(ctx, id, hash, prefix, nowUnix()); err != nil {
		httpx.Fail(c, httpx.WithMessage("重置 API Key 失败: "+err.Error()))
		return
	}
	row.KeyHash, row.KeyPrefix = hash, prefix
	httpx.Success(c, httpx.WithMessage("已重置（旧 key 立即失效，明文仅显示一次）"),
		httpx.WithData(apiKeyIssued(row, plain)))
}

// apiKeyActionRequest 行内动作的共同入参（switch：id + status；reset 只用 :pk）。
type apiKeyActionRequest struct {
	ID     int64  `json:"id"`
	Status *int16 `json:"status"`
}

// apiKeySwitch 应用启停：只改 status（停用后公开面鉴权直接 401，下一个请求即生效）。
func (d deps) apiKeySwitch(c *gin.Context) {
	store := d.storeOrFail(c)
	if store == nil {
		return
	}
	var req apiKeyActionRequest
	if err := bindJSON(c, &req); err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	if req.ID <= 0 {
		httpx.Fail(c, httpx.WithMessage("应用接入 ID 必填"))
		return
	}
	if req.Status == nil || !validStatus(*req.Status) {
		httpx.Fail(c, httpx.WithMessage("status 只能是 0（停用）或 1（启用）"))
		return
	}
	ctx := c.Request.Context()
	affected, err := store.SwitchAPIKey(ctx, req.ID, *req.Status)
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("应用启停失败: "+err.Error()))
		return
	}
	if affected == 0 && !apiKeyExists(ctx, store, req.ID) {
		httpx.Fail(c, httpx.WithMessage(fmt.Sprintf("应用接入 %d 不存在", req.ID)))
		return
	}
	httpx.Success(c, httpx.WithData(gin.H{"id": req.ID, "status": *req.Status, "affected": affected}))
}
