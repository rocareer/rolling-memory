// plugin: kb v1.0.0
// 知识库文档动作面（后台面：重建 / 取消 / 失败列表）。
//
// 路由与节点（仅 GET/POST；节点名 = 去 /admin 的 checkPath——internal/infra/permission/admin.go
// BuildCheckPath；末段非 list/get ⇒ 无 read 回落，节点名即 checkPath）：
//
//	POST /admin/kb/document/rebuild → kb/document/rebuild（本插件清单在册，weigh 209）
//	POST /admin/kb/document/cancel  → kb/document/cancel （本插件清单在册，weigh 210）
//	POST /admin/kb/document/failed  → kb/document/failed （本插件清单登记，weigh 241）
//
// 轴与门：admin 轴（admins.id；节点授权为 HTTP 侧前置），服务层再逐资源过写/读谓词单点（禁按裸 id）：
//   - 重建 = 投递作业（文档级 kb:ingest 单键 / 库级 kb:rebuild 协调行）；文档级重建会复位取消否决
//     （cancel_requested 1→0——取消后重建可达的唯一入口，见服务层 RebuildDocument 注释）；
//   - 取消 = 置 cancel_requested=1（幂等；运行中由管道在阶段边界收口——）；
//   - 失败列表 = build_state='failed' 清单（库可选收窄；分页上限在服务层兜底）。
//
// 「不存在」与「无权」同型：failService 把 ErrDenied 全族压成唯一文案（不泄露存在性）。
package kb

import (
	"strings"

	"github.com/ai-go-hub/ai-go-admin/internal/kit/httpx"
	"github.com/ai-go-hub/ai-go-admin/internal/middleware"
	svcKB "github.com/ai-go-hub/ai-go-admin/plugins/kb/service/kb"

	"github.com/gin-gonic/gin"
)

// RegisterActionRoutes 注册文档动作面路由（挂 AdminAuth+AdminPermission，与 RegisterRoutes 同规格）。
// 注册点：router/admin/kb/kb.go（本面路由装配单点在本文件——与库/文档基础面分文件自持）。
func (h *Handler) RegisterActionRoutes(group *gin.RouterGroup) {
	group.POST("/document/rebuild", middleware.AdminAuth(), middleware.AdminPermission(), h.DocumentRebuild)
	group.POST("/document/cancel", middleware.AdminAuth(), middleware.AdminPermission(), h.DocumentCancel)
	group.POST("/document/failed", middleware.AdminAuth(), middleware.AdminPermission(), h.DocumentFailedList)
}

// documentRebuildRequest 重建请求（document_id 与 library_id **二选一**；mode 仅库级可选）
type documentRebuildRequest struct {
	DocumentID uint   `json:"document_id"`
	LibraryID  uint   `json:"library_id"`
	Mode       string `json:"mode"` // 仅库级：rechunk|reembed（缺省 rechunk；闭集校验在服务层单点）
}

// DocumentRebuild 重建（文档级 = 单文档 kb:ingest 投递；库级 = kb:rebuild 协调行投递）
//
// 请求体（二选一）：{document_id} | {library_id, mode?}
//   - document_id：单文档重建（写谓词门；含复位取消否决；回执 cancel_cleared 显式回显）；
//   - library_id ：库级重建（写谓词门；mode 缺省 rechunk，闭集 rechunk|reembed 由服务层校验）。
func (h *Handler) DocumentRebuild(c *gin.Context) {
	var req documentRebuildRequest
	if err := bindJSON(c, &req); err != nil {
		httpx.Fail(c, httpx.WithMessage("参数错误: "+err.Error()))
		return
	}
	actor, ok := h.adminActor(c)
	if !ok {
		return
	}
	switch {
	case req.DocumentID == 0 && req.LibraryID == 0:
		httpx.Fail(c, httpx.WithMessage("缺少文档 ID（document_id）或库 ID（library_id）"))
		return
	case req.DocumentID > 0 && req.LibraryID > 0:
		httpx.Fail(c, httpx.WithMessage("document_id 与 library_id 二选一（同时给无法裁决重建目标）"))
		return
	}

	var (
		res *svcKB.DocumentBuildResult
		err error
	)
	if req.DocumentID > 0 {
		if strings.TrimSpace(req.Mode) != "" {
			httpx.Fail(c, httpx.WithMessage("文档级重建不接受 mode（仅库级重建可选 rechunk|reembed）"))
			return
		}
		res, err = h.svc.RebuildDocument(c.Request.Context(), actor, req.DocumentID)
	} else {
		res, err = h.svc.RebuildLibrary(c.Request.Context(), actor, req.LibraryID, req.Mode)
	}
	if err != nil {
		failService(c, "投递重建失败", err)
		return
	}
	httpx.Success(c, httpx.WithData(gin.H{"row": res}))
}

// DocumentCancel 取消文档构建（置 cancel_requested=1；幂等；运行中由管道在阶段边界收口）
//
// 请求体：{document_id}
func (h *Handler) DocumentCancel(c *gin.Context) {
	body, err := decodeRawBody(c)
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("参数错误: "+err.Error()))
		return
	}
	actor, ok := h.adminActor(c)
	if !ok {
		return
	}
	documentID, err := rawUint(body, "document_id")
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("document_id 字段类型错误: "+err.Error()))
		return
	}
	if documentID == 0 {
		httpx.Fail(c, httpx.WithMessage("缺少或非法的文档 ID（document_id）"))
		return
	}
	res, err := h.svc.CancelDocument(c.Request.Context(), actor, documentID)
	if err != nil {
		failService(c, "取消文档失败", err)
		return
	}
	httpx.Success(c, httpx.WithData(gin.H{"row": res}))
}

// documentFailedRequest 失败列表请求（library_id 可选：0 = 跨库运维视图）
type documentFailedRequest struct {
	LibraryID uint   `json:"library_id"`
	Page      int    `json:"page"`
	Limit     int    `json:"limit"`
	Sort      string `json:"sort"`
	Order     string `json:"order"`
	Keyword   string `json:"keyword"` // 标题快搜（trgm 表达式 GIN 支撑）
}

// DocumentFailedList 失败文档列表（build_state='failed'；库可选收窄；Count/List 同构造器 + 分页上限）
//
// 请求体：{library_id?, page?, limit?, sort?, order?, keyword?}
func (h *Handler) DocumentFailedList(c *gin.Context) {
	var req documentFailedRequest
	if err := bindJSON(c, &req); err != nil {
		httpx.Fail(c, httpx.WithMessage("参数错误: "+err.Error()))
		return
	}
	view, ok := h.adminView(c)
	if !ok {
		return
	}
	page, limit := normalizePage(req.Page, req.Limit)
	list, total, err := h.svc.ListFailedDocuments(c.Request.Context(), view, req.LibraryID, svcKB.DocumentFailedOptions{
		Page:      page,
		Limit:     limit,
		SortField: req.Sort,
		SortOrder: req.Order,
		Keyword:   req.Keyword,
	})
	if err != nil {
		failService(c, "查询失败文档列表失败", err)
		return
	}
	httpx.Success(c, httpx.WithData(gin.H{
		"list":        list,
		"total":       total,
		"page":        page,
		"limit":       limit,
		"build_state": "failed",
	}))
}
