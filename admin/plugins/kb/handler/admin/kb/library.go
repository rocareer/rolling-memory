// plugin: kb v1.0.0
// 知识库后台管理控制器（手写核心树）：
// 库（列表 / 详情 / 新增 / 编辑 / 软删）、授权面板（grant_add / grant_remove）、文档最小面
// （列表 / 详情 / 软删）。授权变更与 kb_audit_log 同事务由服务层保证（I5），本层不另写审计。
//
// 轴与面：本控制器全部端点 = **后台面**（AdminAuth + AdminPermission；节点名 = 去 /admin 的
// checkPath，list/get 回落 read——与本插件清单的节点名逐名对应）；一律按 **admin 轴** 解析读视图
// （admins.id），不借用 user 谓词（admins.id 与 users.id 是两个独立自增空间，禁混用）。
// 前台用户面（user 轴）本插件无 HTTP 入口——跨用户/跨团队读的对抗用例由服务层单测覆盖
// （service/kb/acl_test.go）。
//
// 「不存在」与「无权」同型：服务层 ErrDenied（含 ErrInvalidActor / ErrActorUnavailable / ErrFailClosed
// 包装值）一律映射为同一文案（deniedMessage）的失败响应，不泄露对象存在性。
//
// 列表过滤面：接受前端 TableManagerAPI 惯例的 wheres 条件组，映射到服务层库列表能力面：
// owner_kind（eq）/ owner_id（eq）/ status（eq）/ name（ILIKE 快搜）/ id（BETWEEN）/
// created_at·updated_at（BETWEEN）。能力面外的字段与运算符 **响亮拒绝** 而非静默跳过——
// 静默跳过会让前端搜索框「看起来在工作」，属假行为（宁可报错可查）。
package kb

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/ai-go-hub/ai-go-admin/internal/kit/httpx"
	"github.com/ai-go-hub/ai-go-admin/internal/middleware"
	"github.com/ai-go-hub/ai-go-admin/internal/service"
	"github.com/ai-go-hub/ai-go-admin/plugins/kb/model"
	svcKB "github.com/ai-go-hub/ai-go-admin/plugins/kb/service/kb"

	"github.com/gin-gonic/gin"
)

// deniedMessage 「不存在」与「无权」同型文案（唯一出口；不回显细分拒绝原因——日志侧才可见）
const deniedMessage = "记录不存在或无权访问"

// Handler 知识库后台控制器
type Handler struct {
	svc *svcKB.Service
}

// NewHandler 创建知识库后台控制器
func NewHandler(svc *svcKB.Service) *Handler {
	return &Handler{svc: svc}
}

// RegisterRoutes 注册知识库后台路由（仅 GET/POST）
//
// 节点对照（本插件清单 menus 段）：
//
//	/library/list    → kb/library/list   回落 kb/library/read
//	/library/get/:pk → kb/library/get    回落 kb/library/read
//	/library/create  → kb/library/create
//	/library/update/:pk → kb/library/update
//	/library/delete  → kb/library/delete
//	/grant/list      → kb/grant/list     回落 kb/grant/read
//	/grant/add       → kb/grant/add
//	/grant/remove    → kb/grant/remove
//	/document/list   → kb/document/list  回落 kb/document/read
//	/document/get/:pk → kb/document/get  回落 kb/document/read
//	/document/delete → kb/document/delete
//	/search          → kb/search          （检索探针；末段非 list/get 无回落）
//	/chunk/get/:pk   → kb/chunk/get      回落 kb/chunk/read（逐字原文核对）
func (h *Handler) RegisterRoutes(group *gin.RouterGroup) {
	// 库
	group.POST("/library/list", middleware.AdminAuth(), middleware.AdminPermission(), h.LibraryList)
	group.GET("/library/get/:pk", middleware.AdminAuth(), middleware.AdminPermission(), h.LibraryGet)
	group.POST("/library/create", middleware.AdminAuth(), middleware.AdminPermission(), h.LibraryCreate)
	group.POST("/library/update/:pk", middleware.AdminAuth(), middleware.AdminPermission(), h.LibraryUpdate)
	group.POST("/library/delete", middleware.AdminAuth(), middleware.AdminPermission(), h.LibraryDelete)

	// 授权面板
	group.POST("/grant/list", middleware.AdminAuth(), middleware.AdminPermission(), h.GrantList)
	group.POST("/grant/add", middleware.AdminAuth(), middleware.AdminPermission(), h.GrantAdd)
	group.POST("/grant/remove", middleware.AdminAuth(), middleware.AdminPermission(), h.GrantRemove)

	// 文档（最小面：列表 / 详情 / 软删；导入与重建动作随作业面）
	group.POST("/document/list", middleware.AdminAuth(), middleware.AdminPermission(), h.DocumentList)
	group.GET("/document/get/:pk", middleware.AdminAuth(), middleware.AdminPermission(), h.DocumentGet)
	group.POST("/document/delete", middleware.AdminAuth(), middleware.AdminPermission(), h.DocumentDelete)

	// 检索面（探针 + 逐字原文核对；节点见本插件清单）
	group.POST("/search", middleware.AdminAuth(), middleware.AdminPermission(), h.SearchProbe)
	group.GET("/chunk/get/:pk", middleware.AdminAuth(), middleware.AdminPermission(), h.ChunkGet)
}

// ==================== 库 ====================

// listRequest 列表请求体（TableManagerAPI 惯例：page/limit/sort/order + wheres 条件组）
type listRequest struct {
	Page   int                  `json:"page"`
	Limit  int                  `json:"limit"`
	Sort   string               `json:"sort"`
	Order  string               `json:"order"`
	Wheres []service.WhereGroup `json:"wheres"`
}

// LibraryList 库列表（分页 ≤100；排序走 BuildOrderScope 白名单 + 主键托底；过滤面见 mapLibraryWheres）
//
// 列表行随带展示字段 owner_username（属主用户名，服务层按 users.id 回填；键仍是 owner_id）
func (h *Handler) LibraryList(c *gin.Context) {
	var req listRequest
	if err := bindJSON(c, &req); err != nil {
		httpx.Fail(c, httpx.WithMessage("参数错误: "+err.Error()))
		return
	}
	view, ok := h.adminView(c)
	if !ok {
		return
	}
	opts, err := mapLibraryWheres(req.Wheres)
	if err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	page, limit := normalizePage(req.Page, req.Limit)
	opts.Page, opts.Limit = page, limit
	opts.SortField, opts.SortOrder = req.Sort, req.Order

	list, total, err := h.svc.ListLibraries(c.Request.Context(), view, opts)
	if err != nil {
		failService(c, "查询库列表失败", err)
		return
	}
	httpx.Success(c, httpx.WithData(gin.H{"list": list, "total": total, "page": page, "limit": limit}))
}

// LibraryGet 库详情（读视图；不存在/无权同型；随带展示字段 owner_username，同列表口径）
func (h *Handler) LibraryGet(c *gin.Context) {
	pk, ok := parsePK(c)
	if !ok {
		return
	}
	view, ok := h.adminView(c)
	if !ok {
		return
	}
	row, err := h.svc.GetLibrary(c.Request.Context(), view, pk)
	if err != nil {
		failService(c, "查询库详情失败", err)
		return
	}
	httpx.Success(c, httpx.WithData(gin.H{"row": row}))
}

// LibraryCreate 新建库（系统级 / 用户级由 owner_kind 决定；owner_kind 创建后不可改）
//
// 请求体：{owner_kind, owner_id, name, description?, chunk_config?, status?}
//   - owner_kind=system → owner_id 恒 0（服务层哨兵）；
//   - owner_kind=user   → owner_id=users.id 必填（username 可被后台改，禁作键）。
func (h *Handler) LibraryCreate(c *gin.Context) {
	body, err := decodeRawBody(c)
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("参数错误: "+err.Error()))
		return
	}
	actor, ok := h.adminActor(c)
	if !ok {
		return
	}

	kind, err := rawTrimmedString(body, "owner_kind")
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("owner_kind 字段类型错误: "+err.Error()))
		return
	}
	name, err := rawTrimmedString(body, "name")
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("name 字段类型错误: "+err.Error()))
		return
	}
	ownerID, err := rawUint(body, "owner_id")
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("owner_id 字段类型错误: "+err.Error()))
		return
	}
	description, err := rawTrimmedString(body, "description")
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("description 字段类型错误: "+err.Error()))
		return
	}
	chunkConfig, err := rawTrimmedString(body, "chunk_config")
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("chunk_config 字段类型错误: "+err.Error()))
		return
	}

	in := svcKB.LibraryInput{
		OwnerKind:   kind,
		OwnerID:     ownerID,
		Name:        name,
		Description: description,
	}
	if cfg := strings.TrimSpace(chunkConfig); cfg != "" {
		in.ChunkConfig = &cfg // 空白串 = 未配置（NULL）
	}
	if raw, ok := body["status"]; ok && len(bytes.TrimSpace(raw)) > 0 && !isJSONNull(raw) {
		st, err := rawUint8(raw)
		if err != nil {
			httpx.Fail(c, httpx.WithMessage("status 取值须为 0/1: "+err.Error()))
			return
		}
		in.Status = &st
	}

	row, err := h.svc.CreateLibrary(c.Request.Context(), actor, in)
	if err != nil {
		failService(c, "创建库失败", err)
		return
	}
	httpx.Success(c, httpx.WithData(gin.H{"row": row}))
}

// LibraryUpdate 更新库元数据（管理权 = 属主 ∪ 后台轴；owner_kind/owner_id 不可改——收编走归属转移）
//
// 请求体（缺省=不改；null / 空白串 = 清空）：{name?, description?, chunk_config?, status?}
func (h *Handler) LibraryUpdate(c *gin.Context) {
	pk, ok := parsePK(c)
	if !ok {
		return
	}
	body, err := decodeRawBody(c)
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("参数错误: "+err.Error()))
		return
	}
	actor, ok := h.adminActor(c)
	if !ok {
		return
	}
	in, err := buildLibraryUpdate(body)
	if err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	row, err := h.svc.UpdateLibrary(c.Request.Context(), actor, pk, in)
	if err != nil {
		failService(c, "更新库失败", err)
		return
	}
	httpx.Success(c, httpx.WithData(gin.H{"row": row}))
}

// LibraryDelete 删除库（软删即刻；异步清理 kb:purge 入队；逐条管理权校验）
//
// 请求体：{pks: ["1","2"]}
func (h *Handler) LibraryDelete(c *gin.Context) {
	body, err := decodeRawBody(c)
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("参数错误: "+err.Error()))
		return
	}
	actor, ok := h.adminActor(c)
	if !ok {
		return
	}
	pks, err := decodePKs(body)
	if err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	for _, id := range pks {
		if err := h.svc.DeleteLibrary(c.Request.Context(), actor, id); err != nil {
			failService(c, fmt.Sprintf("删除库失败（id=%d）", id), err)
			return
		}
	}
	httpx.Success(c, httpx.WithMessage("删除成功"))
}

// ==================== 授权面板 ====================

// GrantList 授权面板列表（门槛 = **库管理权**（属主 ∪ 后台轴），与 grant_add/remove 同门；
// 主键托底序 + 浏览面上限 + total 回显）
//
// 请求体：{library_id, page?, limit?}
func (h *Handler) GrantList(c *gin.Context) {
	body, err := decodeRawBody(c)
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("参数错误: "+err.Error()))
		return
	}
	libraryID, err := rawUint(body, "library_id")
	if err != nil || libraryID == 0 {
		httpx.Fail(c, httpx.WithMessage("缺少或非法的库 ID（library_id）"))
		return
	}
	page, err := rawUint(body, "page")
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("page 字段类型错误: "+err.Error()))
		return
	}
	limit, err := rawUint(body, "limit")
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("limit 字段类型错误: "+err.Error()))
		return
	}
	pageNo, limitNo := normalizePage(int(page), int(limit))
	view, ok := h.adminView(c)
	if !ok {
		return
	}
	list, total, err := h.svc.ListGrants(c.Request.Context(), view, libraryID, pageNo, limitNo)
	if err != nil {
		failService(c, "查询授权列表失败", err)
		return
	}
	httpx.Success(c, httpx.WithData(gin.H{"list": list, "total": total, "page": pageNo, "limit": limitNo}))
}

// GrantAdd 授权（grant_add；管理权门 + 主体存活性 + 同事务审计由服务层保证；同主体同权限轴幂等）
//
// 请求体：{library_id, subject_kind(all|team|user), subject_id, perm(read|write)}
func (h *Handler) GrantAdd(c *gin.Context) {
	body, err := decodeRawBody(c)
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("参数错误: "+err.Error()))
		return
	}
	actor, ok := h.adminActor(c)
	if !ok {
		return
	}
	libraryID, err := rawUint(body, "library_id")
	if err != nil || libraryID == 0 {
		httpx.Fail(c, httpx.WithMessage("缺少或非法的库 ID（library_id）"))
		return
	}
	subjectKind, err := rawTrimmedString(body, "subject_kind")
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("subject_kind 字段类型错误: "+err.Error()))
		return
	}
	subjectID, err := rawUint(body, "subject_id")
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("subject_id 字段类型错误: "+err.Error()))
		return
	}
	perm, err := rawTrimmedString(body, "perm")
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("perm 字段类型错误: "+err.Error()))
		return
	}

	row, err := h.svc.AddGrant(c.Request.Context(), actor, libraryID, svcKB.GrantInput{
		SubjectKind: subjectKind,
		SubjectID:   subjectID,
		Perm:        perm,
	})
	if err != nil {
		failService(c, "授权失败", err)
		return
	}
	httpx.Success(c, httpx.WithData(gin.H{"row": row}))
}

// GrantRemove 撤销授权（grant_remove；软删 grant 行 = 即时生效；同事务审计由服务层保证）
//
// 请求体：{library_id, grant_id}
func (h *Handler) GrantRemove(c *gin.Context) {
	body, err := decodeRawBody(c)
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("参数错误: "+err.Error()))
		return
	}
	actor, ok := h.adminActor(c)
	if !ok {
		return
	}
	libraryID, err := rawUint(body, "library_id")
	if err != nil || libraryID == 0 {
		httpx.Fail(c, httpx.WithMessage("缺少或非法的库 ID（library_id）"))
		return
	}
	grantID, err := rawUint(body, "grant_id")
	if err != nil || grantID == 0 {
		httpx.Fail(c, httpx.WithMessage("缺少或非法的授权行 ID（grant_id）"))
		return
	}
	if err := h.svc.RemoveGrant(c.Request.Context(), actor, libraryID, grantID); err != nil {
		failService(c, "撤销授权失败", err)
		return
	}
	httpx.Success(c, httpx.WithMessage("已撤销"))
}

// ==================== 文档（最小面） ====================

// documentListRequest 文档列表请求体（显式字段；标题快搜走 trgm 表达式 GIN 支撑的 keyword）
type documentListRequest struct {
	LibraryID  uint   `json:"library_id"`
	Page       int    `json:"page"`
	Limit      int    `json:"limit"`
	Sort       string `json:"sort"`
	Order      string `json:"order"`
	BuildState string `json:"build_state"` // 状态机闭集；空=不过滤
	Keyword    string `json:"keyword"`     // title ILIKE 快搜
}

// DocumentList 库内文档列表（先过库读谓词 = 逐资源对象级校验；Count 与 List 同一构造器）
func (h *Handler) DocumentList(c *gin.Context) {
	var req documentListRequest
	if err := bindJSON(c, &req); err != nil {
		httpx.Fail(c, httpx.WithMessage("参数错误: "+err.Error()))
		return
	}
	if req.LibraryID == 0 {
		httpx.Fail(c, httpx.WithMessage("缺少库 ID（library_id）"))
		return
	}
	view, ok := h.adminView(c)
	if !ok {
		return
	}
	page, limit := normalizePage(req.Page, req.Limit)
	list, total, err := h.svc.ListDocuments(c.Request.Context(), view, req.LibraryID, svcKB.DocumentListOptions{
		Page:       page,
		Limit:      limit,
		SortField:  req.Sort,
		SortOrder:  req.Order,
		BuildState: req.BuildState,
		Keyword:    req.Keyword,
	})
	if err != nil {
		failService(c, "查询文档列表失败", err)
		return
	}
	httpx.Success(c, httpx.WithData(gin.H{"list": list, "total": total, "page": page, "limit": limit}))
}

// DocumentGet 文档详情（读谓词；不存在/无权同型）
func (h *Handler) DocumentGet(c *gin.Context) {
	pk, ok := parsePK(c)
	if !ok {
		return
	}
	view, ok := h.adminView(c)
	if !ok {
		return
	}
	row, err := h.svc.GetDocument(c.Request.Context(), view, pk)
	if err != nil {
		failService(c, "查询文档详情失败", err)
		return
	}
	httpx.Success(c, httpx.WithData(gin.H{"row": row}))
}

// DocumentDelete 删除文档（软删即刻 + 同事务审计 + kb:doc:<id> 清理入队（接线）；写谓词门）
//
// 请求体：{pks: ["1","2"]}
func (h *Handler) DocumentDelete(c *gin.Context) {
	body, err := decodeRawBody(c)
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("参数错误: "+err.Error()))
		return
	}
	actor, ok := h.adminActor(c)
	if !ok {
		return
	}
	pks, err := decodePKs(body)
	if err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	for _, id := range pks {
		if err := h.svc.DeleteDocument(c.Request.Context(), actor, id); err != nil {
			failService(c, fmt.Sprintf("删除文档失败（id=%d）", id), err)
			return
		}
	}
	httpx.Success(c, httpx.WithMessage("删除成功"))
}

// ==================== 面与轴 ====================

// adminView 解析**admin 轴**读视图（admins.id；节点授权是 HTTP 中间件前置，本层只做轴分派）。
// 取不到会话/会话非法一律拒（fail-closed——服务层同样对 actor_id≤0 短路拒）。
func (h *Handler) adminView(c *gin.Context) (*svcKB.ReadView, bool) {
	session := middleware.GetAdmin(c)
	if session == nil || session.Admin == nil || session.ID == 0 {
		httpx.Fail(c, httpx.WithMessage(deniedMessage))
		return nil, false
	}
	view, err := h.svc.AdminView(c.Request.Context(), session.ID)
	if err != nil {
		failService(c, "隔离判定失败", err)
		return nil, false
	}
	return view, true
}

// adminActor 写面主体（admin 轴；两 id 空间禁混用——此处恒取 admins.id）
func (h *Handler) adminActor(c *gin.Context) (svcKB.Actor, bool) {
	session := middleware.GetAdmin(c)
	if session == nil || session.Admin == nil || session.ID == 0 {
		httpx.Fail(c, httpx.WithMessage(deniedMessage))
		return svcKB.Actor{}, false
	}
	return svcKB.Actor{Kind: model.KBActorKindAdmin, ID: session.ID}, true
}

// failService 服务层错误 → 失败响应：拒绝类（ErrDenied 全族）一律同型文案，不泄露存在性；
// 其余（校验/约束/存储）带原因回显，便于定位。
func failService(c *gin.Context, action string, err error) {
	if errors.Is(err, svcKB.ErrDenied) {
		httpx.Fail(c, httpx.WithMessage(deniedMessage))
		return
	}
	httpx.Fail(c, httpx.WithMessage(action+": "+err.Error()))
}

// ==================== 过滤面映射 ====================

// mapLibraryWheres wheres 条件组 → 服务层库列表选项（**只映射服务层已实现的能力面**）。
//
// 支持：owner_kind（eq，闭集 system|user）、owner_id（eq，须与 owner_kind 同给）、status（eq，0/1）、
// name（ILIKE/LIKE 快搜）、id（BETWEEN）、created_at / updated_at（BETWEEN，格式
// `YYYY-MM-DD HH:mm:ss` 或 `YYYY-MM-DD`，逗号分隔两段）。
// 其余字段/运算符一律**响亮拒绝**：静默跳过会让前端搜索框看起来在工作，属假行为。
// OR 组合条件组同样拒绝（无法表达）。
func mapLibraryWheres(groups []service.WhereGroup) (svcKB.LibraryListOptions, error) {
	var opts svcKB.LibraryListOptions
	for _, g := range groups {
		if g.Or && len(g.Wheres) > 1 {
			return opts, errors.New("库列表暂不支持 OR 组合条件")
		}
		for _, w := range g.Wheres {
			field := strings.TrimSpace(w.Field)
			if field == "" {
				continue
			}
			op := service.GetOperatorByAlias(w.Operator)
			if op == "" {
				return opts, fmt.Errorf("库列表不支持的操作符: %s", w.Operator)
			}
			value := whereValueString(w.Value)
			switch field {
			case "owner_kind":
				if op != "=" {
					return opts, fmt.Errorf("owner_kind 仅支持 eq（得到 %s）", w.Operator)
				}
				if value != model.KBLibraryOwnerKindSystem && value != model.KBLibraryOwnerKindUser {
					return opts, fmt.Errorf("owner_kind 取值未登记: %s", value)
				}
				opts.OwnerKind = value
			case "owner_id":
				if op != "=" {
					return opts, fmt.Errorf("owner_id 仅支持 eq（得到 %s）", w.Operator)
				}
				id, err := parseUintValue(value)
				if err != nil {
					return opts, fmt.Errorf("owner_id 取值非法: %s", value)
				}
				opts.OwnerID = id
			case "status":
				if op != "=" {
					return opts, fmt.Errorf("status 仅支持 eq（得到 %s）", w.Operator)
				}
				st, err := parseUint8Value(value)
				if err != nil {
					return opts, fmt.Errorf("status 取值须为 0/1: %s", value)
				}
				opts.Status = &st
			case "name":
				if op != "ILIKE" && op != "LIKE" {
					return opts, fmt.Errorf("name 仅支持 ILIKE/LIKE 快搜（得到 %s）", w.Operator)
				}
				opts.NameLike = value
			case "id":
				if op != "BETWEEN" {
					return opts, fmt.Errorf("id 仅支持 BETWEEN（得到 %s）", w.Operator)
				}
				from, to, err := parseUintRange(value)
				if err != nil {
					return opts, fmt.Errorf("id 区间非法（应为 起,止）: %s", value)
				}
				opts.IDFrom, opts.IDTo = &from, &to
			case "created_at", "updated_at":
				if op != "BETWEEN" {
					return opts, fmt.Errorf("%s 仅支持 BETWEEN（得到 %s）", field, w.Operator)
				}
				from, to, err := parseTimeRange(value)
				if err != nil {
					return opts, fmt.Errorf("%s 区间非法（应为 起,止；格式 YYYY-MM-DD HH:mm:ss）: %s", field, value)
				}
				if field == "created_at" {
					opts.CreatedFrom, opts.CreatedTo = from, to
				} else {
					opts.UpdatedFrom, opts.UpdatedTo = from, to
				}
			default:
				return opts, fmt.Errorf("库列表暂不支持按 %s 过滤（过滤面：owner_kind / owner_id / status / name / id / created_at / updated_at）", field)
			}
		}
	}
	if opts.OwnerID > 0 && opts.OwnerKind == "" {
		return opts, errors.New("owner_id 需与 owner_kind 同给（单给不生效——宁响亮拒绝不静默跳过）")
	}
	return opts, nil
}

// parseUintRange "起,止" → uint 区间（两段必填；起≤止）
func parseUintRange(value string) (uint, uint, error) {
	parts := strings.SplitN(value, ",", 2)
	if len(parts) != 2 {
		return 0, 0, errors.New("缺少区间分隔符")
	}
	from, err := parseUintValue(parts[0])
	if err != nil {
		return 0, 0, err
	}
	to, err := parseUintValue(parts[1])
	if err != nil {
		return 0, 0, err
	}
	if from > to {
		return 0, 0, errors.New("区间起点大于终点")
	}
	return from, to, nil
}

// parseTimeRange "起,止" → 时间区间（格式 YYYY-MM-DD HH:mm:ss / YYYY-MM-DD；起≤止）
func parseTimeRange(value string) (*time.Time, *time.Time, error) {
	parts := strings.SplitN(value, ",", 2)
	if len(parts) != 2 {
		return nil, nil, errors.New("缺少区间分隔符")
	}
	from, err := parseTimeValue(parts[0])
	if err != nil {
		return nil, nil, err
	}
	to, err := parseTimeValue(parts[1])
	if err != nil {
		return nil, nil, err
	}
	if from.After(to) {
		return nil, nil, errors.New("区间起点大于终点")
	}
	return &from, &to, nil
}

// parseTimeValue 时间点解析（格式 YYYY-MM-DD HH:mm:ss / YYYY-MM-DD，本地时区；单点口径，
// 区间与单点入参共用——第二处 layout 列表即漂移源）
func parseTimeValue(value string) (time.Time, error) {
	s := strings.TrimSpace(value)
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("无法解析时间: %s", s)
}

// whereValueString 条件值 → 字符串（JSON 数字/字符串/bool 归一；其余 JSON 编码后去引号）
func whereValueString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(t)
	case bool:
		if t {
			return "true"
		}
		return "false"
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		raw, err := json.Marshal(t)
		if err != nil {
			return strings.TrimSpace(fmt.Sprint(t))
		}
		return strings.Trim(string(raw), `"`)
	}
}

// parseUintValue 字符串 → uint（十进制；空/负/超界非法）
func parseUintValue(s string) (uint, error) {
	v, err := strconv.ParseUint(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0, err
	}
	return uint(v), nil
}

// parseUint8Value 字符串 → uint8（0/1 域由调用方语义限定）
func parseUint8Value(s string) (uint8, error) {
	v, err := strconv.ParseUint(strings.TrimSpace(s), 10, 8)
	if err != nil {
		return 0, err
	}
	return uint8(v), nil
}

// ==================== 请求解析小工具 ====================

// bindJSON 绑定 JSON；空请求体按零值处理（列表类接口可省略 body；照 eval 先例）
func bindJSON(c *gin.Context, dst any) error {
	if err := c.ShouldBindJSON(dst); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

// decodeRawBody 读取原始体为 JSON 对象（保键序无关的「未传 / 传 null」区分；空体 = 空对象）
func decodeRawBody(c *gin.Context) (map[string]json.RawMessage, error) {
	body, err := c.GetRawData()
	if err != nil {
		return nil, err
	}
	out := map[string]json.RawMessage{}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return out, nil
	}
	if err := json.Unmarshal(trimmed, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// parsePK 解析 /:pk 路由参数
func parsePK(c *gin.Context) (uint, bool) {
	id, err := strconv.ParseUint(c.Param("pk"), 10, 32)
	if err != nil || id == 0 {
		httpx.Fail(c, httpx.WithMessage("缺少或非法的主键 ID"))
		return 0, false
	}
	return uint(id), true
}

// normalizePage 分页归一（页码 ≥1；每页条数 ≤100——浏览面硬上限，服务层再兜一次）
func normalizePage(page, limit int) (int, int) {
	if page < 1 {
		page = 1
	}
	return page, svcKB.NormalizeBrowseLimit(limit)
}

// isJSONNull 裸值是否为 JSON null
func isJSONNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

// rawString 裸值 → 字符串（null → ""；类型不符报错）
func rawString(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || isJSONNull(raw) {
		return "", nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", errors.New("须为字符串或 null")
	}
	return s, nil
}

// rawTrimmedString 裸值 → 去空白字符串（未传 = ""）
func rawTrimmedString(body map[string]json.RawMessage, key string) (string, error) {
	raw, ok := body[key]
	if !ok {
		return "", nil
	}
	s, err := rawString(raw)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(s), nil
}

// rawUint 裸值 → uint（数字或数字字符串；null/未传 → 0）
func rawUint(body map[string]json.RawMessage, key string) (uint, error) {
	raw, ok := body[key]
	if !ok || len(bytes.TrimSpace(raw)) == 0 || isJSONNull(raw) {
		return 0, nil
	}
	var n json.Number
	if err := json.Unmarshal(raw, &n); err != nil {
		return 0, errors.New("须为整数")
	}
	return parseUintValue(n.String())
}

// rawUint8 裸值 → uint8（0..255；语义域由调用方限定）
func rawUint8(raw json.RawMessage) (uint8, error) {
	var n json.Number
	if err := json.Unmarshal(raw, &n); err != nil {
		return 0, errors.New("须为整数")
	}
	return parseUint8Value(n.String())
}

// buildLibraryUpdate 更新入参（缺省=不改；null / 空白串=清空——chunk_config 清空落 NULL，
// owner_kind / owner_id 不可改：收编与转移走独立动作）
func buildLibraryUpdate(body map[string]json.RawMessage) (svcKB.LibraryUpdate, error) {
	var in svcKB.LibraryUpdate
	if _, ok := body["name"]; ok {
		name, err := rawTrimmedString(body, "name")
		if err != nil {
			return in, errors.New("name 字段类型错误: " + err.Error())
		}
		if name == "" {
			return in, errors.New("库名必填")
		}
		in.Name = &name
	}
	if _, ok := body["description"]; ok {
		desc, err := rawString(body["description"])
		if err != nil {
			return in, errors.New("description 字段类型错误: " + err.Error())
		}
		desc = strings.TrimSpace(desc)
		in.Description = &desc
	}
	if _, ok := body["chunk_config"]; ok {
		cfg, err := rawString(body["chunk_config"])
		if err != nil {
			return in, errors.New("chunk_config 字段类型错误: " + err.Error())
		}
		cfg = strings.TrimSpace(cfg) // 空白串 = 清空（服务层 normalize → NULL）
		in.ChunkConfig = &cfg
	}
	if raw, ok := body["status"]; ok {
		if len(bytes.TrimSpace(raw)) == 0 || isJSONNull(raw) {
			return in, errors.New("status 取值须为 0/1")
		}
		st, err := rawUint8(raw)
		if err != nil || (st != 0 && st != 1) {
			return in, errors.New("status 取值须为 0/1")
		}
		in.Status = &st
	}
	return in, nil
}

// decodePKs 解析批量主键（{"pks":["1","2"]}；空表/非法项拒绝）
func decodePKs(body map[string]json.RawMessage) ([]uint, error) {
	raw, ok := body["pks"]
	if !ok {
		return nil, errors.New("缺少主键列表 pks")
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, errors.New("pks 须为字符串数组")
	}
	ids := make([]uint, 0, len(items))
	for _, item := range items {
		inner := map[string]json.RawMessage{"pk": item}
		id, err := rawUint(inner, "pk")
		if err != nil || id == 0 {
			return nil, fmt.Errorf("主键非法: %s", strings.Trim(string(item), `"`))
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil, errors.New("主键列表为空")
	}
	return ids, nil
}
