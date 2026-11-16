// Package plugin 的插件来源/清单管理面（与既有 plugin.go 同包、同 /admin/plugin 前缀）。
//
// 路径与权限节点（与框架数据库迁移逐字对应）：
//
//	POST /admin/plugin/source/list          → plugin/source/list（回落 plugin/source/read）
//	GET  /admin/plugin/source/get/:pk       → plugin/source/get（回落 plugin/source/read）
//	POST /admin/plugin/source/create        → plugin/source/create
//	POST /admin/plugin/source/update/:pk    → plugin/source/update
//	POST /admin/plugin/source/delete        → plugin/source/delete
//	POST /admin/plugin/source/test          → plugin/source/test
//	POST /admin/plugin/catalog/list         → plugin/catalog/list（回落 plugin/catalog/read）
//	POST /admin/plugin/catalog/sync         → plugin/catalog/sync
//	POST /admin/plugin/catalog/preflight    → plugin/catalog/preflight
//	POST /admin/plugin/catalog/install      → plugin/catalog/install
//
// 本组是宿主所有、路径固定：不注册 /source/sort（迁移没有该节点），也不暴露 --force/覆盖。
// 每个 handler 除中间件外再做一次超管校验（与既有插件管理面同款纵深防御）。
package plugin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/ai-go-hub/ai-go-admin/internal/dto"
	"github.com/ai-go-hub/ai-go-admin/internal/handler"
	"github.com/ai-go-hub/ai-go-admin/internal/infra/database"
	"github.com/ai-go-hub/ai-go-admin/internal/infra/permission"
	"github.com/ai-go-hub/ai-go-admin/internal/kit/httpx"
	"github.com/ai-go-hub/ai-go-admin/internal/middleware"
	pluginCli "github.com/ai-go-hub/ai-go-admin/internal/plugin/cli"
	pluginSource "github.com/ai-go-hub/ai-go-admin/internal/plugin/source"
	"github.com/ai-go-hub/ai-go-admin/internal/router/registry"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// SuperAdminChecker 超管判定（生产默认 permission.New；测试注入 fake，不依赖真库）。
type SuperAdminChecker interface {
	IsSuperAdmin(ctx context.Context, adminID uint) (bool, error)
}

type permissionChecker struct{}

func (permissionChecker) IsSuperAdmin(ctx context.Context, adminID uint) (bool, error) {
	return permission.New().IsSuperAdmin(ctx, adminID)
}

// adminSourceHandler 来源/清单管理面控制器。
type adminSourceHandler struct {
	store   pluginSource.Store
	checker SuperAdminChecker
	newEnv  func() (*pluginCli.Env, error)
	// env 环境判定（生产禁止安装/卸载）：测试注入固定值；生产默认 pluginCli.DetectEnvironment。
	env       func() pluginCli.Environment
	preflight func(ctx context.Context, env *pluginCli.Env, store pluginSource.Store, sourceID uint, uid, version, operator string) (*pluginCli.CatalogPreflightResult, error)
	install   func(ctx context.Context, env *pluginCli.Env, store pluginSource.Store, req pluginCli.CatalogInstallRequest) (*pluginCli.CatalogInstallResult, error)
}

func newAdminSourceHandler() *adminSourceHandler {
	return &adminSourceHandler{
		store:     pluginSource.NewGormStore(adminDB),
		checker:   permissionChecker{},
		newEnv:    pluginCli.NewSourceEnv,
		env:       pluginCli.DetectEnvironment,
		preflight: pluginCli.PreflightCatalog,
		install:   pluginCli.InstallCatalog,
	}
}

// adminDB 与既有 handler 一致：只取全局句柄，不在请求期做数据库初始化（serve 启动时已初始化）。
func adminDB(ctx context.Context) (*gorm.DB, error) {
	db := database.DB()
	if db == nil {
		return nil, errors.New("数据库尚未初始化")
	}
	return db.WithContext(ctx), nil
}

func init() {
	registry.RegisterAdmin(func(group *gin.RouterGroup) {
		newAdminSourceHandler().RegisterRoutes(group.Group("/plugin"))
	})
}

// RegisterRoutes 注册来源与清单端点（source/catalog 两个子组）。
func (h *adminSourceHandler) RegisterRoutes(group *gin.RouterGroup) {
	sourceGroup := group.Group("/source")
	sourceGroup.POST("/list", middleware.AdminAuth(), middleware.AdminPermission(), h.sourceList)
	sourceGroup.GET("/get/:pk", middleware.AdminAuth(), middleware.AdminPermission(), h.sourceGet)
	sourceGroup.POST("/create", middleware.AdminAuth(), middleware.AdminPermission(), h.sourceCreate)
	sourceGroup.POST("/update/:pk", middleware.AdminAuth(), middleware.AdminPermission(), h.sourceUpdate)
	sourceGroup.POST("/delete", middleware.AdminAuth(), middleware.AdminPermission(), h.sourceDelete)
	sourceGroup.POST("/test", middleware.AdminAuth(), middleware.AdminPermission(), h.sourceTest)

	catalogGroup := group.Group("/catalog")
	catalogGroup.POST("/list", middleware.AdminAuth(), middleware.AdminPermission(), h.catalogList)
	catalogGroup.POST("/sync", middleware.AdminAuth(), middleware.AdminPermission(), h.catalogSync)
	catalogGroup.POST("/preflight", middleware.AdminAuth(), middleware.AdminPermission(), h.catalogPreflight)
	catalogGroup.POST("/install", middleware.AdminAuth(), middleware.AdminPermission(), h.catalogInstall)
}

// ---- 来源 CRUD ----

type sourceWriteRequest struct {
	Name               *string `json:"name"`
	Title              *string `json:"title"`
	Type               *string `json:"type"`
	Location           *string `json:"location"`
	Enabled            *int16  `json:"enabled"`
	AllowInsecureHTTP  *int16  `json:"allow_insecure_http"`
	AllowPrivateHosts  *int16  `json:"allow_private_hosts"`
	AllowAbsolutePaths *int16  `json:"allow_absolute_paths"`
	Weigh              *int64  `json:"weigh"`
	Remark             *string `json:"remark"`
}

func (h *adminSourceHandler) sourceList(c *gin.Context) {
	if _, ok := h.requireSuperAdmin(c); !ok {
		return
	}
	var req handler.ListRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid_input", "参数错误: "+err.Error())
		return
	}
	groups := make([]pluginSource.FilterGroup, 0, len(req.Wheres))
	for _, group := range req.Wheres {
		item := pluginSource.FilterGroup{Or: group.Or}
		for _, where := range group.Wheres {
			item.Filters = append(item.Filters, pluginSource.Filter{Field: where.Field, Operator: where.Operator, Value: where.Value})
		}
		groups = append(groups, item)
	}
	list, total, err := h.store.ListSources(c.Request.Context(), pluginSource.ListSourcesQuery{
		Page: req.Page, Limit: req.Limit, Sort: req.Sort, Order: req.Order, Wheres: groups,
	})
	if err != nil {
		fail(c, http.StatusServiceUnavailable, "repo_root_unavailable", "查询来源失败: "+err.Error())
		return
	}
	// env 段：与 catalog/list、plugin/list 同一口径（来源配置页的环境标识与频道门禁弹窗据此判定）。
	httpx.Success(c, httpx.WithData(gin.H{"list": list, "total": total, "env": h.env()}))
}

func (h *adminSourceHandler) sourceGet(c *gin.Context) {
	if _, ok := h.requireSuperAdmin(c); !ok {
		return
	}
	id, err := parsePK(c.Param("pk"))
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid_input", err.Error())
		return
	}
	row, err := h.store.GetSource(c.Request.Context(), id)
	if errors.Is(err, pluginSource.ErrNotFound) {
		fail(c, http.StatusNotFound, "source_not_found", "来源不存在")
		return
	}
	if err != nil {
		fail(c, http.StatusServiceUnavailable, "repo_root_unavailable", "查询来源失败: "+err.Error())
		return
	}
	httpx.Success(c, httpx.WithData(gin.H{"row": row}))
}

func (h *adminSourceHandler) sourceCreate(c *gin.Context) {
	admin, ok := h.requireSuperAdmin(c)
	if !ok {
		return
	}
	var req sourceWriteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid_input", "参数错误: "+err.Error())
		return
	}
	if req.Name == nil || req.Location == nil {
		fail(c, http.StatusBadRequest, "invalid_input", "name 与 location 必填")
		return
	}
	in := pluginSource.SourceInput{
		Name: *req.Name, Type: valueOr(req.Type, pluginSource.TypeLocal), Location: *req.Location,
		Enabled: req.Enabled, AllowInsecureHTTP: req.AllowInsecureHTTP,
		AllowPrivateHosts: req.AllowPrivateHosts, AllowAbsolutePaths: req.AllowAbsolutePaths,
		Weigh: req.Weigh,
	}
	if req.Title != nil {
		in.Title = *req.Title
	}
	if req.Remark != nil {
		in.Remark = *req.Remark
	}
	src, err := pluginSource.NewSource(in, admin.Username)
	if err != nil {
		fail(c, http.StatusUnprocessableEntity, "invalid_input", err.Error())
		return
	}
	if err := h.store.CreateSource(c.Request.Context(), src); err != nil {
		if errors.Is(err, pluginSource.ErrNameConflict) {
			fail(c, http.StatusConflict, "invalid_input", "来源名已存在")
			return
		}
		fail(c, http.StatusServiceUnavailable, "repo_root_unavailable", "新增来源失败: "+err.Error())
		return
	}
	httpx.Success(c, httpx.WithData(gin.H{"row": src}))
}

func (h *adminSourceHandler) sourceUpdate(c *gin.Context) {
	admin, ok := h.requireSuperAdmin(c)
	if !ok {
		return
	}
	id, err := parsePK(c.Param("pk"))
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid_input", err.Error())
		return
	}
	var req sourceWriteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid_input", "参数错误: "+err.Error())
		return
	}
	current, err := h.store.GetSource(c.Request.Context(), id)
	if errors.Is(err, pluginSource.ErrNotFound) {
		fail(c, http.StatusNotFound, "source_not_found", "来源不存在")
		return
	}
	if err != nil {
		fail(c, http.StatusServiceUnavailable, "repo_root_unavailable", "查询来源失败: "+err.Error())
		return
	}
	if req.Name != nil && strings.TrimSpace(*req.Name) != current.Name {
		fail(c, http.StatusConflict, "invalid_input", "来源名不可修改")
		return
	}

	candidate := *current
	patch := pluginSource.SourcePatch{UpdatedBy: admin.Username}
	if req.Title != nil {
		candidate.Title = *req.Title
		patch.Title = req.Title
	}
	if req.Type != nil {
		candidate.Type = *req.Type
		patch.Type = req.Type
	}
	if req.Location != nil {
		candidate.Location = *req.Location
		patch.Location = req.Location
	}
	if req.Enabled != nil {
		candidate.Enabled = *req.Enabled
		patch.Enabled = req.Enabled
	}
	if req.Weigh != nil {
		candidate.Weigh = *req.Weigh
		patch.Weigh = req.Weigh
	}
	if req.Remark != nil {
		candidate.Remark = *req.Remark
		patch.Remark = req.Remark
	}
	if req.AllowInsecureHTTP != nil {
		candidate.AllowInsecureHTTP = *req.AllowInsecureHTTP
	}
	if req.AllowPrivateHosts != nil {
		candidate.AllowPrivateHosts = *req.AllowPrivateHosts
	}
	if req.AllowAbsolutePaths != nil {
		candidate.AllowAbsolutePaths = *req.AllowAbsolutePaths
	}
	if err := candidate.NormalizeAndValidate(); err != nil {
		fail(c, http.StatusUnprocessableEntity, "invalid_input", err.Error())
		return
	}
	// 类型/路径/安全开关变化：清空快照；开关的规范化结果一并写入（避免旧值残留）。
	clearSnapshot := (req.Type != nil && *req.Type != current.Type) ||
		(req.Location != nil && *req.Location != current.Location) ||
		(req.AllowInsecureHTTP != nil && *req.AllowInsecureHTTP != current.AllowInsecureHTTP) ||
		(req.AllowPrivateHosts != nil && *req.AllowPrivateHosts != current.AllowPrivateHosts) ||
		(req.AllowAbsolutePaths != nil && *req.AllowAbsolutePaths != current.AllowAbsolutePaths)
	if clearSnapshot || req.Type != nil || req.AllowInsecureHTTP != nil || req.AllowPrivateHosts != nil || req.AllowAbsolutePaths != nil {
		insecure := candidate.AllowInsecureHTTP
		private := candidate.AllowPrivateHosts
		absolute := candidate.AllowAbsolutePaths
		patch.AllowInsecureHTTP = &insecure
		patch.AllowPrivateHosts = &private
		patch.AllowAbsolutePaths = &absolute
	}
	patch.ClearSnapshot = clearSnapshot
	cleared, err := h.store.UpdateSource(c.Request.Context(), id, patch)
	if err != nil {
		if errors.Is(err, pluginSource.ErrNotFound) {
			fail(c, http.StatusNotFound, "source_not_found", "来源不存在")
			return
		}
		fail(c, http.StatusServiceUnavailable, "repo_root_unavailable", "更新来源失败: "+err.Error())
		return
	}
	row, err := h.store.GetSource(c.Request.Context(), id)
	if err != nil {
		fail(c, http.StatusServiceUnavailable, "repo_root_unavailable", "读取更新后的来源失败: "+err.Error())
		return
	}
	httpx.Success(c, httpx.WithData(gin.H{"row": row, "snapshot_cleared": cleared}))
}

func (h *adminSourceHandler) sourceDelete(c *gin.Context) {
	if _, ok := h.requireSuperAdmin(c); !ok {
		return
	}
	var req handler.DeleteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid_input", "参数错误: "+err.Error())
		return
	}
	ids := make([]uint, 0, len(req.PrimaryKeyValues))
	for _, raw := range req.PrimaryKeyValues {
		id, err := parsePK(raw)
		if err != nil {
			fail(c, http.StatusBadRequest, "invalid_input", err.Error())
			return
		}
		ids = append(ids, id)
	}
	deleted, err := h.store.DeleteSources(c.Request.Context(), ids)
	if err != nil {
		fail(c, http.StatusServiceUnavailable, "repo_root_unavailable", "删除来源失败: "+err.Error())
		return
	}
	if deleted == 0 {
		fail(c, http.StatusNotFound, "source_not_found", "来源不存在")
		return
	}
	httpx.Success(c, httpx.WithData(gin.H{"deleted": deleted}))
}

func (h *adminSourceHandler) sourceTest(c *gin.Context) {
	if _, ok := h.requireSuperAdmin(c); !ok {
		return
	}
	var req sourceWriteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid_input", "参数错误: "+err.Error())
		return
	}
	if req.Location == nil {
		fail(c, http.StatusBadRequest, "invalid_input", "location 必填")
		return
	}
	src := &pluginSource.Source{Type: valueOr(req.Type, pluginSource.TypeLocal), Location: *req.Location}
	if req.AllowInsecureHTTP != nil {
		src.AllowInsecureHTTP = *req.AllowInsecureHTTP
	}
	if req.AllowPrivateHosts != nil {
		src.AllowPrivateHosts = *req.AllowPrivateHosts
	}
	if req.AllowAbsolutePaths != nil {
		src.AllowAbsolutePaths = *req.AllowAbsolutePaths
	}
	if err := src.ValidateLocation(); err != nil {
		fail(c, http.StatusUnprocessableEntity, "invalid_input", err.Error())
		return
	}
	root := ""
	env, envErr := h.newEnv()
	if envErr == nil && env != nil {
		root = env.Root
	}
	if root == "" && src.Type == pluginSource.TypeLocal {
		fail(c, http.StatusServiceUnavailable, "repo_root_unavailable", "仓库根不可用：只支持源码检出内运行或配置 plugin.root")
		return
	}
	doc, err := pluginSource.LoadCatalog(c.Request.Context(), src, root)
	if err != nil {
		code, status := mapSourceError(err)
		message := pluginSource.SanitizePaths(err.Error(), root, src.Location)
		httpx.Fail(c, httpx.WithCode(status), httpx.WithMessage(message),
			httpx.WithData(gin.H{"ok": false, "errors": []string{message}, "error_code": code}))
		return
	}
	httpx.Success(c, httpx.WithData(gin.H{
		"ok":             true,
		"schema":         doc.Catalog.Schema,
		"generated_at":   doc.Catalog.GeneratedAt,
		"entries":        len(doc.Catalog.Plugins),
		"catalog_sha256": doc.SHA256,
	}))
}

// ---- 清单 ----

type catalogListRequest struct {
	SourceID uint   `json:"source_id"`
	UID      string `json:"uid"`
	Keyword  string `json:"keyword"`
	Page     int    `json:"page"`
	Limit    int    `json:"limit"`
}

type catalogRow struct {
	pluginSource.CatalogEntry
	InstalledVersion string `json:"installed_version"`
	InstalledStatus  string `json:"installed_status"`
	SameContent      bool   `json:"same_content"`
	SourceConflict   bool   `json:"source_conflict"`
}

func (h *adminSourceHandler) catalogList(c *gin.Context) {
	if _, ok := h.requireSuperAdmin(c); !ok {
		return
	}
	var req catalogListRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid_input", "参数错误: "+err.Error())
		return
	}
	rows, total, err := h.store.ListEntries(c.Request.Context(), pluginSource.ListEntriesQuery{
		SourceID: req.SourceID, UID: req.UID, Keyword: req.Keyword, Page: req.Page, Limit: req.Limit,
	})
	if err != nil {
		fail(c, http.StatusServiceUnavailable, "repo_root_unavailable", "查询清单失败: "+err.Error())
		return
	}
	uids := make([]string, 0, len(rows))
	pairs := make([]pluginSource.UIDVersion, 0, len(rows))
	for i := range rows {
		uids = append(uids, rows[i].UID)
		pairs = append(pairs, pluginSource.UIDVersion{UID: rows[i].UID, Version: rows[i].Version})
	}
	installed, err := h.store.InstalledVersions(c.Request.Context(), uids)
	if err != nil {
		fail(c, http.StatusServiceUnavailable, "repo_root_unavailable", "读取插件台账失败: "+err.Error())
		return
	}
	peers, err := h.store.ListEntryPeers(c.Request.Context(), pairs)
	if err != nil {
		fail(c, http.StatusServiceUnavailable, "repo_root_unavailable", "检测来源冲突失败: "+err.Error())
		return
	}
	flags := pluginSource.EntryPeerFlags(rows, peers)
	list := make([]catalogRow, 0, len(rows))
	for i := range rows {
		item := catalogRow{CatalogEntry: rows[i]}
		if installedRow, ok := installed[rows[i].UID]; ok {
			item.InstalledVersion = installedRow.Version
			item.InstalledStatus = installedRow.Status
		}
		flag := flags[pluginSource.EntryRowKey(rows[i])]
		item.SameContent = flag.SameContent
		item.SourceConflict = flag.SourceConflict
		list = append(list, item)
	}
	sources, err := h.store.ListSourcesSummary(c.Request.Context())
	if err != nil {
		fail(c, http.StatusServiceUnavailable, "repo_root_unavailable", "读取来源摘要失败: "+err.Error())
		return
	}
	// env 段：插件页面据此渲染环境标识（dev/prod + 判定依据），并在生产环境禁用安装入口。
	// 判定实现单点在 pluginCli.DetectEnvironment（environment.go），页面不复述规则。
	httpx.Success(c, httpx.WithData(gin.H{"list": list, "total": total, "sources": sources, "env": h.env()}))
}

type catalogSyncRequest struct {
	SourceIDs []uint `json:"source_ids"`
}

func (h *adminSourceHandler) catalogSync(c *gin.Context) {
	if _, ok := h.requireSuperAdmin(c); !ok {
		return
	}
	var req catalogSyncRequest
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		fail(c, http.StatusBadRequest, "invalid_input", "参数错误: "+err.Error())
		return
	}
	sources, err := h.syncTargets(c.Request.Context(), req.SourceIDs)
	if err != nil {
		fail(c, http.StatusServiceUnavailable, "repo_root_unavailable", "读取来源失败: "+err.Error())
		return
	}
	root := ""
	for i := range sources {
		if sources[i].Type == pluginSource.TypeLocal {
			env, envErr := h.newEnv()
			if envErr != nil || env == nil || env.Root == "" {
				fail(c, http.StatusServiceUnavailable, "repo_root_unavailable", "仓库根不可用：只支持源码检出内运行或配置 plugin.root")
				return
			}
			root = env.Root
			break
		}
	}
	results := make([]pluginSource.SyncResult, 0, len(sources))
	succeeded, failed := 0, 0
	for i := range sources {
		result := pluginSource.SyncSource(c.Request.Context(), h.store, &sources[i], root, false)
		if result.Status == "ok" {
			succeeded++
		} else {
			failed++
		}
		results = append(results, result)
	}
	httpx.Success(c, httpx.WithData(gin.H{"results": results, "partial": failed > 0 && succeeded > 0}))
}

func (h *adminSourceHandler) syncTargets(ctx context.Context, ids []uint) ([]pluginSource.Source, error) {
	if len(ids) > 0 {
		out := make([]pluginSource.Source, 0, len(ids))
		for _, id := range ids {
			row, err := h.store.GetSource(ctx, id)
			if err != nil {
				return nil, err
			}
			out = append(out, *row)
		}
		return out, nil
	}
	rows, err := pluginSource.ListAllSources(ctx, h.store)
	if err != nil {
		return nil, err
	}
	out := make([]pluginSource.Source, 0, len(rows))
	for i := range rows {
		if rows[i].Enabled == 1 {
			out = append(out, rows[i])
		}
	}
	return out, nil
}

type catalogActionRequest struct {
	SourceID      uint   `json:"source_id"`
	UID           string `json:"uid"`
	Version       string `json:"version"`
	AllowUnsigned bool   `json:"allow_unsigned"`
	ConfirmSHA256 string `json:"confirm_sha256"`
}

func (h *adminSourceHandler) catalogPreflight(c *gin.Context) {
	if _, ok := h.requireSuperAdmin(c); !ok {
		return
	}
	var req catalogActionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid_input", "参数错误: "+err.Error())
		return
	}
	if req.SourceID == 0 || req.UID == "" || req.Version == "" {
		fail(c, http.StatusBadRequest, "invalid_input", "source_id / uid / version 必填")
		return
	}
	env, err := h.newEnv()
	if err != nil {
		fail(c, http.StatusServiceUnavailable, "repo_root_unavailable", "仓库根不可用：只支持源码检出内运行或配置 plugin.root")
		return
	}
	result, err := h.preflight(c.Request.Context(), env, h.store, req.SourceID, req.UID, req.Version, currentOperator(c))
	if err != nil {
		writeCatalogError(c, err)
		return
	}
	httpx.Success(c, httpx.WithData(result))
}

func (h *adminSourceHandler) catalogInstall(c *gin.Context) {
	if _, ok := h.requireSuperAdmin(c); !ok {
		return
	}
	// 环境门禁（生产禁止安装）：HTTP 面第一道、先于参数校验与取包——错误码与写路径同一实现
	// （lifecycle_forbidden ⇒ 403，writeCatalogError 直接映射）；纵深处的 InstallCatalog/runInstall
	// 另有同款门禁，此道只为「不下载、不暂存」的快速拒绝。
	if err := pluginCli.RequireLifecycleWith("安装", h.env()); err != nil {
		writeCatalogError(c, err)
		return
	}
	var req catalogActionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid_input", "参数错误: "+err.Error())
		return
	}
	if req.SourceID == 0 || req.UID == "" || req.Version == "" || req.ConfirmSHA256 == "" {
		fail(c, http.StatusBadRequest, "invalid_input", "source_id / uid / version / confirm_sha256 必填")
		return
	}
	env, err := h.newEnv()
	if err != nil {
		fail(c, http.StatusServiceUnavailable, "repo_root_unavailable", "仓库根不可用：只支持源码检出内运行或配置 plugin.root")
		return
	}
	result, err := h.install(c.Request.Context(), env, h.store, pluginCli.CatalogInstallRequest{
		SourceID:      req.SourceID,
		UID:           req.UID,
		Version:       req.Version,
		AllowUnsigned: req.AllowUnsigned,
		ConfirmSHA256: req.ConfirmSHA256,
		Operator:      currentOperator(c),
	})
	if err != nil {
		writeCatalogError(c, err)
		return
	}
	httpx.Success(c, httpx.WithData(result))
}

// ---- 鉴权与响应辅助 ----

func (h *adminSourceHandler) requireSuperAdmin(c *gin.Context) (*dto.AdminSession, bool) {
	admin := middleware.GetAdmin(c)
	if admin == nil {
		fail(c, http.StatusUnauthorized, "invalid_input", "请先登录")
		return nil, false
	}
	super, err := h.checker.IsSuperAdmin(c.Request.Context(), admin.ID)
	if err != nil {
		fail(c, http.StatusServiceUnavailable, "repo_root_unavailable", "服务暂时不可用，请稍后再试")
		return nil, false
	}
	if !super {
		fail(c, http.StatusForbidden, "invalid_input", "无权限")
		return nil, false
	}
	return admin, true
}

func currentOperator(c *gin.Context) string {
	if admin := middleware.GetAdmin(c); admin != nil {
		return admin.Username
	}
	return ""
}

func fail(c *gin.Context, status int, code, message string) {
	httpx.Fail(c, httpx.WithCode(status), httpx.WithMessage(message), httpx.WithData(gin.H{"error_code": code}))
}

func writeCatalogError(c *gin.Context, err error) {
	var catalogErr *pluginCli.CatalogError
	if errors.As(err, &catalogErr) {
		fail(c, catalogErr.Status, catalogErr.Code, catalogErr.Message)
		return
	}
	fail(c, http.StatusInternalServerError, "install_failed", err.Error())
}

func mapSourceError(err error) (string, int) {
	switch {
	case errors.Is(err, pluginSource.ErrInvalidInput):
		return "catalog_invalid", http.StatusUnprocessableEntity
	case errors.Is(err, pluginSource.ErrPathEscape):
		return "catalog_invalid", http.StatusUnprocessableEntity
	case errors.Is(err, pluginSource.ErrCatalogNotFound):
		// source/test 只读清单；本地清单文件缺失属于配置错误，不是上游故障。
		return "catalog_invalid", http.StatusUnprocessableEntity
	case errors.Is(err, pluginSource.ErrPackageNotFound):
		return "package_not_found", http.StatusBadGateway
	case errors.Is(err, pluginSource.ErrPackageSizeMismatch):
		return "package_size_mismatch", http.StatusUnprocessableEntity
	case errors.Is(err, pluginSource.ErrPackageSHAMismatch):
		return "package_sha_mismatch", http.StatusUnprocessableEntity
	case errors.Is(err, pluginSource.ErrPackageSignedMismatch):
		return "package_signed_mismatch", http.StatusUnprocessableEntity
	case errors.Is(err, pluginSource.ErrManifestMismatch):
		return "manifest_mismatch", http.StatusUnprocessableEntity
	case errors.Is(err, pluginSource.ErrNetworkBlocked):
		return "network_blocked", http.StatusBadGateway
	case errors.Is(err, pluginSource.ErrUpstreamError):
		return "upstream_error", http.StatusBadGateway
	default:
		return "catalog_invalid", http.StatusUnprocessableEntity
	}
}

func parsePK(raw string) (uint, error) {
	value, err := strconv.ParseUint(strings.TrimSpace(raw), 10, 64)
	if err != nil || value == 0 {
		return 0, fmt.Errorf("主键 %q 不合法", raw)
	}
	return uint(value), nil
}

func valueOr(value *string, fallback string) string {
	if value == nil {
		return fallback
	}
	return *value
}
