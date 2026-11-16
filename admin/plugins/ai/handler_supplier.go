// plugin: ai v0.2.0
// handler_supplier.go 是厂商管理的 5 个后台端点：
//
//	POST /supplier/list      列表（分页 + 快搜 + 顶层 status/vendor + wheres + 每行 channel_count）
//	GET  /supplier/get/:pk   单行回填（含掩码 api_key + channel_count）
//	POST /supplier/create    新增（name 必填、base_url/driver/status 闭集与 clamp）
//	POST /supplier/update/:pk 保存（api_key 空串或含 '*' ⇒ 不修改）
//	POST /supplier/delete    批量删除（**删除保护**：仍被渠道引用即拒，库中行不变）
//
// 出口纪律：api_key 一律掩码（maskSecret 与源 mask_secret 同口径）、掩码永不回写；
// 列表/详情都带 channel_count（口径 = 渠道表 count(*) where supplier_id=该厂商）。
//
// 与 plugin.go 的分工：路由表（adminRoute 切片）在此声明、由装配面并入 plugin.go 的 adminRoutes()，
// 本包不占 plugin.go（同包，装配面只需把 supplierAdminRoutes() 追加进去）。
package ai

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/ai-go-hub/ai-go-admin/internal/kit/httpx"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// supplierWhereFields 是厂商表的 wheres 字段白名单（列名是已引用常量）。
//
// api_key 刻意不在其中：密钥不是可检索维度（能被筛出来就等于给了爆破一个按值得分的侧信道），
// 且与其他表的白名单口径一致（channelWhereFields 同样不含 api_key）。
var supplierWhereFields = map[string]whereField{
	"id":          {Column: `"id"`, Kind: whereInt},
	"name":        {Column: `"name"`, Kind: whereText},
	"vendor":      {Column: `"vendor"`, Kind: whereText},
	"base_url":    {Column: `"base_url"`, Kind: whereText},
	"driver":      {Column: `"driver"`, Kind: whereText},
	"timeout":     {Column: `"timeout"`, Kind: whereInt},
	"status":      {Column: `"status"`, Kind: whereBool01},
	"remark":      {Column: `"remark"`, Kind: whereText},
	"create_time": {Column: `"create_time"`, Kind: whereInt},
	"update_time": {Column: `"update_time"`, Kind: whereInt},
}

// supplierRow 是厂商行的出口形状：内嵌 Supplier（其 APIKey 带 json:"-" ⇒ 永不序列化），
// 另给脱敏掩码、是否已配置、以及渠道数（列表页的「渠道」列）。
type supplierRow struct {
	Supplier
	APIKeyMasked string `json:"api_key"`
	HasAPIKey    bool   `json:"has_api_key"`
	ChannelCount int64  `json:"channel_count"`
}

// newSupplierRow 出口脱敏（掩码与源 mask_secret 同口径；明文永不出接口）。
func newSupplierRow(row Supplier, channelCount int64) supplierRow {
	return supplierRow{
		Supplier:     row,
		APIKeyMasked: maskSecret(row.APIKey),
		HasAPIKey:    strings.TrimSpace(row.APIKey) != "",
		ChannelCount: channelCount,
	}
}

// newSupplierRows 批量脱敏（逐个挂上 counts 里的渠道数；缺键即 0）。
func newSupplierRows(rows []Supplier, counts map[int64]int64) []supplierRow {
	out := make([]supplierRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, newSupplierRow(row, counts[row.ID]))
	}
	return out
}

// ===== 路由表（装配面并入 plugin.go 的 adminRoutes）=====

// supplierAdminRoutes 是厂商管理的 5 条路由，与 plugin.yaml 的 4 个节点一一对应
// （list 与 get 共用 read 节点：末段 list|get 由 AdminPermission 按 [同名, 同前缀/read] OR 回落）。
func (d deps) supplierAdminRoutes() []adminRoute {
	return []adminRoute{
		{http.MethodPost, "/supplier/list", "plugin/ai/supplier/read", d.supplierList},
		{http.MethodGet, "/supplier/get/:pk", "plugin/ai/supplier/read", d.supplierGet},
		{http.MethodPost, "/supplier/create", "plugin/ai/supplier/create", d.supplierCreate},
		{http.MethodPost, "/supplier/update/:pk", "plugin/ai/supplier/update", d.supplierUpdate},
		{http.MethodPost, "/supplier/delete", "plugin/ai/supplier/delete", d.supplierDelete},
	}
}

// ===== 列表 =====

// supplierListRequest 厂商列表入参：宿主分页契约 + 顶层等值筛选 status/vendor（照源 Supplier.php:31-42）。
type supplierListRequest struct {
	listRequest
	Status optionalStatus `json:"status"`
	Vendor string         `json:"vendor"`
}

// filter 把请求收敛成 store 的筛选结构 + 已校验的 wheres 组。
func (r supplierListRequest) filter() (SupplierFilter, []whereGroup, error) {
	if r.Status.Value != nil && !validStatus(*r.Status.Value) {
		return SupplierFilter{}, nil, errors.New("status 只能是 0（停用）或 1（启用）")
	}
	if err := validateSort(r.Sort, supplierSortColumns(), "厂商"); err != nil {
		return SupplierFilter{}, nil, err
	}
	groups, err := validateWhereGroups(supplierWhereFields, r.Wheres)
	if err != nil {
		return SupplierFilter{}, nil, err
	}
	return SupplierFilter{
		Page:    r.Page,
		Limit:   r.Limit,
		Sort:    r.Sort,
		Order:   r.Order,
		Keyword: r.keyword(),
		Status:  r.Status.Value,
		Vendor:  r.Vendor,
	}, groups, nil
}

// supplierList 厂商分页列表（每行带 channel_count；行业统计条不适用厂商页，故不返回 stats）。
func (d deps) supplierList(c *gin.Context) {
	store := d.storeOrFail(c)
	if store == nil {
		return
	}
	var req supplierListRequest
	if err := bindJSON(c, &req); err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	filter, groups, err := req.filter()
	if err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	result, err := store.listSuppliers(c.Request.Context(), filter, groups)
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("读取厂商列表失败: "+err.Error()))
		return
	}
	httpx.Success(c, httpx.WithData(gin.H{
		"list":  newSupplierRows(result.Rows, result.Counts),
		"total": result.Total,
	}))
}

// ===== 单行读取 =====

// supplierGet 单行回填（含掩码 api_key 与 channel_count；掩码含 '*' ⇒ 前端认作「不修改」）。
func (d deps) supplierGet(c *gin.Context) {
	store := d.storeOrFail(c)
	if store == nil {
		return
	}
	id, err := parsePK(c, "厂商")
	if err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	ctx := c.Request.Context()
	row, err := store.GetSupplier(ctx, id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		httpx.Fail(c, httpx.WithMessage(fmt.Sprintf("厂商 %d 不存在", id)))
		return
	}
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("读取厂商失败: "+err.Error()))
		return
	}
	counts, err := store.SupplierChannelCounts(ctx, []int64{id})
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("读取厂商渠道数失败: "+err.Error()))
		return
	}
	httpx.Success(c, httpx.WithData(gin.H{"row": newSupplierRow(*row, counts[id])}))
}

// ===== 新增 / 保存 =====

// supplierCreate 新增厂商：未配凭证允许（继承期 fail-closed 报「渠道未配置 api_key」）。
func (d deps) supplierCreate(c *gin.Context) {
	store := d.storeOrFail(c)
	if store == nil {
		return
	}
	var req supplierSaveRequest
	if err := bindJSON(c, &req); err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	row, err := req.supplier()
	if err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	if err := store.CreateSupplier(c.Request.Context(), row); err != nil {
		if isUniqueViolation(err) {
			httpx.Fail(c, httpx.WithMessage(fmt.Sprintf("厂商名 %q 已存在（name 唯一）", row.Name)))
			return
		}
		httpx.Fail(c, httpx.WithMessage("新增厂商失败: "+redactSecret(err.Error(), row.APIKey)))
		return
	}
	httpx.Success(c, httpx.WithData(gin.H{"id": row.ID, "name": row.Name}))
}

// supplierUpdate 保存厂商：api_key 为空串或含 '*' 一律视为不修改。
func (d deps) supplierUpdate(c *gin.Context) {
	store := d.storeOrFail(c)
	if store == nil {
		return
	}
	id, err := parsePK(c, "厂商")
	if err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	var req supplierSaveRequest
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
	affected, err := store.UpdateSupplier(ctx, id, patch)
	if err != nil {
		if isUniqueViolation(err) {
			httpx.Fail(c, httpx.WithMessage("厂商名已存在（name 唯一）"))
			return
		}
		httpx.Fail(c, httpx.WithMessage("保存厂商失败: "+redactSecret(err.Error(), strings.TrimSpace(derefString(req.APIKey)))))
		return
	}
	if affected == 0 && !store.SupplierExists(ctx, id) {
		httpx.Fail(c, httpx.WithMessage(fmt.Sprintf("厂商 %d 不存在", id)))
		return
	}
	httpx.Success(c, httpx.WithData(gin.H{"id": id, "affected": affected}))
}

// ===== 删除（带删除保护）=====

// supplierDelete 批量删除厂商：本批里任一厂商仍被渠道引用 ⇒ 整个请求拒（code=1 + 可读文案），
// 库中行与渠道挂靠都不变（判定与删除同事务，见 store.DeleteSuppliers）。
func (d deps) supplierDelete(c *gin.Context) {
	store := d.storeOrFail(c)
	if store == nil {
		return
	}
	var req pksRequest
	if err := bindJSON(c, &req); err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	ids, err := req.ids("厂商")
	if err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	deleted, err := store.DeleteSuppliers(c.Request.Context(), ids)
	if err != nil {
		if isSupplierInUse(err) {
			httpx.Fail(c, httpx.WithMessage(err.Error()))
			return
		}
		httpx.Fail(c, httpx.WithMessage("删除厂商失败: "+err.Error()))
		return
	}
	httpx.Success(c, httpx.WithData(gin.H{"deleted": deleted}))
}
