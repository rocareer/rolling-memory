// Package plugin 是插件管理面的后台控制器（设计说明）。
//
// 模块路径判定：**泛型基类手写**（明文）——管理面不允许 create/delete，
// 动作是 enable/disable（有副作用语义、须同步台账状态与菜单 status），因此不套 CRUD 生成器；
// model.Plugin 仍走泛型四层出 list/get（`handler.Handler[model.Plugin]` 嵌入 + 自定义端点）。
//
// 只 GET/POST；五个端点的权限节点由 AdminPermission 按 FullPath 推导：
// plugin/list、plugin/get 回落 plugin/read，enable/disable/uninstall 精确匹配（同既有惯例）。
// **逐 handler 超管校验**：管理面节点（000005/000010 种子）在框架迁移里不授予任何角色组
// （默认只有超管可见可用），中间件层是第一道，本层的 requireSuperAdmin 是第二道（落点 ② 与）。
package plugin

import (
	"errors"
	"net/http"

	"github.com/ai-go-hub/ai-go-admin/internal/dto"
	"github.com/ai-go-hub/ai-go-admin/internal/handler"
	"github.com/ai-go-hub/ai-go-admin/internal/infra/permission"
	"github.com/ai-go-hub/ai-go-admin/internal/kit/httpx"
	"github.com/ai-go-hub/ai-go-admin/internal/middleware"
	"github.com/ai-go-hub/ai-go-admin/internal/model"
	pluginCli "github.com/ai-go-hub/ai-go-admin/internal/plugin/cli"
	svcPlugin "github.com/ai-go-hub/ai-go-admin/internal/service/admin/plugin"

	"github.com/gin-gonic/gin"
)

// Handler 插件管理面控制器，嵌入通用控制器并扩展自定义方法
type Handler struct {
	*handler.Handler[model.Plugin]
	svc *svcPlugin.Service
}

// NewHandler 创建插件管理面控制器实例
func NewHandler(svc *svcPlugin.Service) *Handler {
	h := &Handler{svc: svc}
	// create/update/delete/sort 端点不注册（管理面没有这些动作）；Omit 只是纵深防御——
	// 即使将来有人误挂基路由，请求体也覆盖不了 id。
	h.Handler = handler.NewHandler(svc, handler.WithOmitFields(handler.ActionFields{
		Create: []string{"id"},
		Update: []string{"id"},
	}))
	return h
}

// nameRequest 单插件端点（get/enable/disable）的请求体
type nameRequest struct {
	Name string `json:"name" binding:"required"`
}

// detailRequest 详情请求体（name 必填；page/limit 只作用于动作审计面）
type detailRequest struct {
	Name  string `json:"name" binding:"required"`
	Page  int    `json:"page"`
	Limit int    `json:"limit"`
}

// uninstallRequest 卸载请求体（旗标与 CLI 一一对应；「彻底删除」的二级确认由界面对话框勾选承担，
// 服务层把 purge 与 CLI 的 --yes 强制成对，不绕过 CLI 门禁）
type uninstallRequest struct {
	Name      string `json:"name" binding:"required"`
	Purge     bool   `json:"purge"`
	KeepFiles bool   `json:"keep_files"`
	Force     bool   `json:"force"`
}

// RegisterRoutes 注册插件管理面路由（API 表；相对段挂在 /admin 组下）：
//
//	POST /admin/plugin/list    → 节点 plugin/list（末段 list ⇒ AdminPermission 回落 plugin/read）
//	POST /admin/plugin/get     → 节点 plugin/get（同样回落 plugin/read）
//	POST /admin/plugin/enable  → 精确节点 plugin/enable
//	POST /admin/plugin/disable → 精确节点 plugin/disable
//	POST /admin/plugin/uninstall → 精确节点 plugin/uninstall（框架数据库迁移种子；详情抽屉的卸载入口）
//
// 本组是**宿主**所有、路径固定：与插件命名空间 /admin/plugin/<名>/… 结构上不冲突
// （插件名被宿主保留字 render/list/get/read/enable/disable 排除，见 manifest 校验与 plugins/README.md）。
func (h *Handler) RegisterRoutes(group *gin.RouterGroup) {
	group.POST("/list", middleware.AdminAuth(), middleware.AdminPermission(), h.List)
	group.POST("/get", middleware.AdminAuth(), middleware.AdminPermission(), h.Get)
	group.POST("/enable", middleware.AdminAuth(), middleware.AdminPermission(), h.Enable)
	group.POST("/disable", middleware.AdminAuth(), middleware.AdminPermission(), h.Disable)
	group.POST("/uninstall", middleware.AdminAuth(), middleware.AdminPermission(), h.Uninstall)
}

// List 台账分页列表。
//
// 契约对齐宿主通用 List（{list,total}），另带 runtime 段（：带「运行时是否接线」与
// 「台账/进程内/注册文件」三方差异标记）：各行 wired/registered/drift + 页面级 orphans；
// 以及 env 段（环境判定：插件页面的环境标识与生产环境的安装/卸载禁用依据）。
func (h *Handler) List(c *gin.Context) {
	if _, ok := h.requireSuperAdmin(c); !ok {
		return
	}

	var req handler.ListRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Fail(c, httpx.WithMessage("参数错误: "+err.Error()))
		return
	}

	ctx := c.Request.Context()
	opts := h.BuildSerOpts(c, "List", req.Request)

	list, err := h.svc.List(ctx, opts)
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("查询失败: "+err.Error()))
		return
	}

	total, err := h.svc.Count(ctx, opts)
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("总数查询失败: "+err.Error()))
		return
	}

	rows, runtime, err := h.svc.BuildRows(ctx, list)
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("组装运行时对照失败: "+err.Error()))
		return
	}

	httpx.Success(c, httpx.WithData(gin.H{
		"list":    rows,
		"total":   total,
		"runtime": runtime,
		// env 段：插件页面据此渲染环境标识（dev/prod + 判定依据）。判定实现单点在
		// pluginCli.DetectEnvironment（internal/plugin/cli/environment.go）：生产环境
		// 禁止安装/卸载，页面按 lifecycle_allowed 置灰入口，后端门禁才是权威。
		"env": pluginCli.DetectEnvironment(),
	}))
}

// Get 单插件详情：manifest 摘要（台账行）+ 文件清单 + 校验和（台账 checksum 列）+
// 实体列表 + 动作审计（分页）。
func (h *Handler) Get(c *gin.Context) {
	if _, ok := h.requireSuperAdmin(c); !ok {
		return
	}

	var req detailRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Fail(c, httpx.WithMessage("参数错误: "+err.Error()))
		return
	}

	detail, err := h.svc.Detail(c.Request.Context(), req.Name, req.Page, req.Limit)
	if errors.Is(err, svcPlugin.ErrNotFound) {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("查询插件详情失败: "+err.Error()))
		return
	}

	httpx.Success(c, httpx.WithData(gin.H{
		"row":      detail.Row,
		"entities": detail.Entities,
		"audit":    detail.Audit,
		"files":    detail.Files,
		// env 段：详情抽屉据此渲染环境标识，并按 lifecycle_allowed 置灰卸载入口（判定单点见
		// pluginCli.DetectEnvironment；后端门禁才是权威，前端不复述规则）。
		"env": pluginCli.DetectEnvironment(),
	}))
}

// Enable 启用插件：台账 enabled + 菜单/节点 status=1 + 写审计（复用 CLI 同一实现）。
// 回执带 effect 码：进程内没有该插件时 effect=rebuild_required（「待重编译」），不假装已生效。
func (h *Handler) Enable(c *gin.Context) {
	admin, ok := h.requireSuperAdmin(c)
	if !ok {
		return
	}

	var req nameRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Fail(c, httpx.WithMessage("参数错误: "+err.Error()))
		return
	}

	result, err := h.svc.Enable(c.Request.Context(), req.Name, admin.Username)
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("启用失败: "+err.Error()))
		return
	}

	httpx.Success(c, httpx.WithData(result))
}

// Disable 停用插件：台账 disabled + 菜单/节点 status=0 + 写审计（还原宿主只有这一条代码路径）
func (h *Handler) Disable(c *gin.Context) {
	admin, ok := h.requireSuperAdmin(c)
	if !ok {
		return
	}

	var req nameRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Fail(c, httpx.WithMessage("参数错误: "+err.Error()))
		return
	}

	result, err := h.svc.Disable(c.Request.Context(), req.Name, admin.Username)
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("停用失败: "+err.Error()))
		return
	}

	httpx.Success(c, httpx.WithData(result))
}

// Uninstall 卸载插件（管理面卸载入口；写路径复用 cli.Uninstall 单点：环境门禁 / 先禁用后卸载硬门禁 /
// --purge 二次确认 / adopt 装入的 --purge 默认拒绝 都在 CLI 侧同一实现，本层不复制规则）。
// 回执带 report（CLI 执行回执，已脱敏）与 effect 码（rebuild_required | gate_ttl）：不假装已生效。
func (h *Handler) Uninstall(c *gin.Context) {
	admin, ok := h.requireSuperAdmin(c)
	if !ok {
		return
	}

	// 环境门禁（生产禁止卸载）：HTTP 面第一道——先于参数校验与一切读库/文件动作；
	// 写路径（cli.Uninstall）另有同款门禁，两处同一实现同一错误码（lifecycle_forbidden ⇒ 403）。
	if err := pluginCli.RequireLifecycleWith("卸载", pluginCli.DetectEnvironment()); err != nil {
		writeUninstallError(c, err)
		return
	}

	var req uninstallRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Fail(c, httpx.WithMessage("参数错误: "+err.Error()))
		return
	}

	result, err := h.svc.Uninstall(c.Request.Context(), req.Name, svcPlugin.UninstallParams{
		Purge:     req.Purge,
		KeepFiles: req.KeepFiles,
		Force:     req.Force,
	}, admin.Username)
	if err != nil {
		writeUninstallError(c, err)
		return
	}

	httpx.Success(c, httpx.WithData(result))
}

// writeUninstallError 卸载错误映射：稳定错误码（lifecycle_forbidden 等）按 Status+Code 返回，
// 其余原样为可展示文案（service 层已脱敏，不含本机路径）。
func writeUninstallError(c *gin.Context, err error) {
	var catalogErr *pluginCli.CatalogError
	if errors.As(err, &catalogErr) {
		httpx.Fail(c, httpx.WithCode(catalogErr.Status), httpx.WithMessage(catalogErr.Message),
			httpx.WithData(gin.H{"error_code": catalogErr.Code}))
		return
	}
	httpx.Fail(c, httpx.WithMessage("卸载失败: "+err.Error()))
}

// requireSuperAdmin 管理面入口的超管校验（「只对超管开放」落点 ②）。
//
// 本项目无超管中间件，逐 handler 判断是既有口径（先例 internal/handler/admin/admin.go:111-125）；
// 与先例的一处差别：依赖数据库的 err 与「非超管」分开——DB 故障回 503（不把基础设施错误
// 误读成没权限），与 AdminPermission 中间件的口径一致（internal/middleware/admin_permission.go:50-58）。
// operator 署名取自会话用户名（审计行的 operator 列；它只是署名，不构成身份认证）。
func (h *Handler) requireSuperAdmin(c *gin.Context) (*dto.AdminSession, bool) {
	admin := middleware.GetAdmin(c)
	if admin == nil {
		// 防御性分支（正常链路中 AdminAuth 已先阻断）：401 口径与其他认证失败一致
		httpx.Fail(c, httpx.WithCode(http.StatusUnauthorized), httpx.WithMessage("请先登录"))
		return nil, false
	}

	super, err := permission.New().IsSuperAdmin(c.Request.Context(), admin.ID)
	if err != nil {
		httpx.Fail(c, httpx.WithCode(http.StatusServiceUnavailable), httpx.WithMessage("服务暂时不可用，请稍后再试"))
		return nil, false
	}
	if !super {
		httpx.Fail(c, httpx.WithCode(http.StatusForbidden), httpx.WithMessage("无权限"))
		return nil, false
	}
	return admin, true
}
