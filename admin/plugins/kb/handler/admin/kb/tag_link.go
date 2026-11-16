// plugin: kb v1.0.0
// 知识库标签绑定面（kb_tag_link 为**手写**面
// ——关联表无独立单表语义，走对象（文档/切片）批量绑定/解绑/读取，不入 CRUD 生成器）。
//
// 分层：本文件只做**管理面 HTTP 层**（入参解析 + 会话轴 + 错误映射）；一切判定（对象级写谓词门、
// 绑定幂等 ON CONFLICT、跨库引用拒绝、禁用/已删标签拒绝、同事务审计）在 service/kb/tag_link.go
// 单点，本层不复制谓词、不另立拒绝语义。
//
// 节点：kb/tag_link/list（回落 read）/ kb/tag_link/bind / kb/tag_link/unbind（节点见本插件清单）。
// 「不存在」与「无权」同型：服务层 ErrDenied 全族一律映射为 deniedMessage（与库/文档/授权面同文案）。
package kb

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"

	"github.com/ai-go-hub/ai-go-admin/internal/kit/httpx"
	"github.com/ai-go-hub/ai-go-admin/internal/middleware"

	"github.com/gin-gonic/gin"
)

// RegisterTagLinkRoutes 注册标签绑定路由（仅 GET/POST；节点名 = 去 /admin 的 checkPath）
//
//	/bind   → kb/tag_link/bind
//	/unbind → kb/tag_link/unbind
//	/list   → kb/tag_link/list   回落 kb/tag_link/read
func (h *Handler) RegisterTagLinkRoutes(group *gin.RouterGroup) {
	group.POST("/list", middleware.AdminAuth(), middleware.AdminPermission(), h.TagLinkList)
	group.POST("/bind", middleware.AdminAuth(), middleware.AdminPermission(), h.TagLinkBind)
	group.POST("/unbind", middleware.AdminAuth(), middleware.AdminPermission(), h.TagLinkUnbind)
}

// tagLinkTarget 绑定面公共入参（对象锚定 = (document|chunk) + 对象 id）
type tagLinkTarget struct {
	ObjectType string `json:"object_type"`
	ObjectID   uint   `json:"object_id"`
	TagIDs     []uint `json:"tag_ids,omitempty"`
}

// TagLinkList 对象已挂标签（读谓词门；详情面读取）
func (h *Handler) TagLinkList(c *gin.Context) {
	body, err := decodeRawBody(c)
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("参数错误: "+err.Error()))
		return
	}
	target, err := parseTagLinkTarget(body, false)
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("参数错误: "+err.Error()))
		return
	}
	view, ok := h.adminView(c)
	if !ok {
		return
	}
	list, err := h.svc.ListObjectTags(c.Request.Context(), view, target.ObjectType, target.ObjectID)
	if err != nil {
		failService(c, "读取对象标签失败", err)
		return
	}
	httpx.Success(c, httpx.WithData(gin.H{"list": list}))
}

// TagLinkBind 批量绑定（幂等：ON CONFLICT 命中不产重复行、不重复留痕）
func (h *Handler) TagLinkBind(c *gin.Context) {
	body, err := decodeRawBody(c)
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("参数错误: "+err.Error()))
		return
	}
	target, err := parseTagLinkTarget(body, true)
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("参数错误: "+err.Error()))
		return
	}
	actor, ok := h.adminActor(c)
	if !ok {
		return
	}
	res, err := h.svc.BindTags(c.Request.Context(), actor, target.ObjectType, target.ObjectID, target.TagIDs)
	if err != nil {
		failService(c, "绑定标签失败", err)
		return
	}
	httpx.Success(c, httpx.WithData(gin.H{"row": res}))
}

// TagLinkUnbind 批量解绑（硬删：解除即删行；同事务审计 tag_remove）
func (h *Handler) TagLinkUnbind(c *gin.Context) {
	body, err := decodeRawBody(c)
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("参数错误: "+err.Error()))
		return
	}
	target, err := parseTagLinkTarget(body, true)
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("参数错误: "+err.Error()))
		return
	}
	actor, ok := h.adminActor(c)
	if !ok {
		return
	}
	res, err := h.svc.UnbindTags(c.Request.Context(), actor, target.ObjectType, target.ObjectID, target.TagIDs)
	if err != nil {
		failService(c, "解绑标签失败", err)
		return
	}
	httpx.Success(c, httpx.WithData(gin.H{"row": res}))
}

// parseTagLinkTarget 解析对象锚定入参；needTags=true 时要求 tag_ids 非空
func parseTagLinkTarget(body map[string]json.RawMessage, needTags bool) (*tagLinkTarget, error) {
	objectType, err := rawTrimmedString(body, "object_type")
	if err != nil {
		return nil, errors.New("object_type 字段类型错误: " + err.Error())
	}
	if objectType == "" {
		return nil, errors.New("object_type 必填（document|chunk）")
	}
	objectID, err := rawUint(body, "object_id")
	if err != nil {
		return nil, errors.New("object_id 字段类型错误: " + err.Error())
	}
	if objectID == 0 {
		return nil, errors.New("object_id 必填（> 0）")
	}
	target := &tagLinkTarget{ObjectType: objectType, ObjectID: objectID}
	if !needTags {
		return target, nil
	}
	tagIDs, err := rawUintSlice(body, "tag_ids")
	if err != nil {
		return nil, err
	}
	if len(tagIDs) == 0 {
		return nil, errors.New("tag_ids 必填（非空整数数组）")
	}
	target.TagIDs = tagIDs
	return target, nil
}

// rawUintSlice 裸值 → []uint（数字元素或数字字符串元素；空数组/未传 = 空表）
func rawUintSlice(body map[string]json.RawMessage, key string) ([]uint, error) {
	raw, ok := body[key]
	if !ok || len(bytes.TrimSpace(raw)) == 0 || isJSONNull(raw) {
		return nil, nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, errors.New(key + " 须为整数数组")
	}
	ids := make([]uint, 0, len(items))
	for _, item := range items {
		inner := map[string]json.RawMessage{"v": item}
		id, err := rawUint(inner, "v")
		if err != nil || id == 0 {
			return nil, errors.New(key + " 含非法取值（须为正整数）: " + strings.Trim(string(item), `"`))
		}
		ids = append(ids, id)
	}
	return ids, nil
}
