// plugin: kb v1.0.0
// 知识库审计只读列表（`kb_audit_log` 行：
// 手写只读列表——**时间窗 + 过滤 + 上限，禁全表 count**）。
//
// 路由与节点（仅 POST；节点名 = 去 /admin 的 checkPath——internal/infra/permission/admin.go BuildCheckPath；
// 末段 list 由 AdminPermission 回落 read）：
//
//	POST /admin/kb/audit/list → kb/audit/list 回落 kb/audit/read（read 节点在本插件清单登记）
//
// 轴与门：admin 轴（admins.id；节点授权为 HTTP 侧前置），服务层再验 IsAdmin（审计面 = 后台轴）——
// 非 admin 轴一律 ErrDenied 同型（不泄露存在性）。读面自身不落审计（服务层动作闭集只含变更动作）。
//
// 口径：
//   - 时间窗：since / until 可选（缺省 since = 近 30 天，svcKB.DefaultAuditWindow）——列表**恒带窗**
//     （增长表收敛扫描面，禁无界扫描）；格式 `YYYY-MM-DD HH:mm:ss` 或 `YYYY-MM-DD`；since 晚于 until
//     响亮拒绝（不静默回退默认窗）。生效窗口随回执回显（调用方可复算）。
//   - 过滤：library_id / document_id / actor_kind(+actor_id) / action；actor_kind、action 的**闭集校验在
//     服务层单点**（本层只做键对可解释性校验，见 actorFilter）。无过滤 = 全库审计按时间窗。
//   - 上限：page ≥1、limit 经 normalizePage → NormalizeBrowseLimit 收敛 ≤100（服务层再兜一次），
//     回执回显生效值。
//   - 禁全表 count（增长表）：回执**不给 total**——翻页判定 = 满页（len(list) == limit）则试下一页
//     （服务层同口径注释：调用方按「有下一页」判定）。
//
// 「不存在」与「无权」同型：failService 把 ErrDenied 全族压成唯一文案（不泄露存在性）。
package kb

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/ai-go-hub/ai-go-admin/internal/kit/httpx"
	"github.com/ai-go-hub/ai-go-admin/internal/middleware"
	"github.com/ai-go-hub/ai-go-admin/plugins/kb/model"
	svcKB "github.com/ai-go-hub/ai-go-admin/plugins/kb/service/kb"

	"github.com/gin-gonic/gin"
)

// RegisterAuditRoutes 注册审计只读面路由（挂 AdminAuth+AdminPermission，与 RegisterRoutes 同规格）。
// 注册点：router/admin/kb/kb.go（本面路由装配单点在本文件自持——与文档动作面同法）。
func (h *Handler) RegisterAuditRoutes(group *gin.RouterGroup) {
	group.POST("/audit/list", middleware.AdminAuth(), middleware.AdminPermission(), h.AuditList)
}

// AuditList 审计只读列表
//
// 请求体：{page?, limit?, since?, until?, library_id?, document_id?, actor_kind?, actor_id?, action?}
func (h *Handler) AuditList(c *gin.Context) {
	body, err := decodeRawBody(c)
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("参数错误: "+err.Error()))
		return
	}

	var (
		pageU, limitU, libraryID, documentID, actorID uint
		actorKind, action                             string
	)
	// 逐字段严格解析（未传 / null → 零值；类型不符响亮拒绝）；固定顺序 = 报错可复现
	for _, f := range []struct {
		key string
		dst *uint
	}{
		{"page", &pageU}, {"limit", &limitU}, {"library_id", &libraryID},
		{"document_id", &documentID}, {"actor_id", &actorID},
	} {
		v, err := rawUint(body, f.key)
		if err != nil {
			httpx.Fail(c, httpx.WithMessage(f.key+" 字段类型错误: "+err.Error()))
			return
		}
		*f.dst = v
	}
	for _, f := range []struct {
		key string
		dst *string
	}{{"actor_kind", &actorKind}, {"action", &action}} {
		v, err := rawTrimmedString(body, f.key)
		if err != nil {
			httpx.Fail(c, httpx.WithMessage(f.key+" 字段类型错误: "+err.Error()))
			return
		}
		*f.dst = v
	}
	since, err := parseOptionalTime(body, "since")
	if err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	until, err := parseOptionalTime(body, "until")
	if err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}

	if err := actorFilter(actorKind, actorID); err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	if since == nil {
		def := time.Now().Add(-svcKB.DefaultAuditWindow) // 缺省窗单点在服务包（本行只回显，不另设值）
		since = &def
	}
	if until != nil && since.After(*until) {
		httpx.Fail(c, httpx.WithMessage("时间窗非法：since 晚于 until"))
		return
	}

	view, ok := h.adminView(c)
	if !ok {
		return
	}
	page, limit := normalizePage(int(pageU), int(limitU))
	list, err := h.svc.ListAuditLogs(c.Request.Context(), view, svcKB.AuditListOptions{
		Page:       page,
		Limit:      limit,
		Since:      since,
		Until:      until,
		LibraryID:  libraryID,
		DocumentID: documentID,
		ActorKind:  actorKind,
		ActorID:    actorID,
		Action:     action,
	})
	if err != nil {
		failService(c, "查询审计列表失败", err)
		return
	}
	httpx.Success(c, httpx.WithData(gin.H{
		"list":  list,
		"page":  page,
		"limit": limit,
		"since": since,
		"until": until,
	}))
}

// parseOptionalTime 可选时间点（未传 / null / 空白 = nil = 不限；格式 YYYY-MM-DD HH:mm:ss / YYYY-MM-DD）
func parseOptionalTime(body map[string]json.RawMessage, key string) (*time.Time, error) {
	raw, err := rawTrimmedString(body, key)
	if err != nil {
		return nil, fmt.Errorf("%s 字段类型错误: %w", key, err)
	}
	if raw == "" {
		return nil, nil
	}
	t, err := parseTimeValue(raw)
	if err != nil {
		return nil, fmt.Errorf("%s 无法解析（格式 YYYY-MM-DD HH:mm:ss 或 YYYY-MM-DD）: %s", key, raw)
	}
	return &t, nil
}

// actorFilter 主体过滤键对校验：(actor_kind, actor_id) 必须同给且互容。
//
// 服务层按 `actor_kind = ? AND actor_id = ?` 单条下压（service/kb ListAuditLogs 主体过滤）：只给 id
// 不给 kind 会被静默忽略、admin/user 轴漏给 id 会命中恒空集（system 哨兵 id=0）——两者都让过滤
// 「看起来在工作」，属假行为 ⇒ 响亮拒绝。
// kind 的闭集校验仍在服务层单点（本函数对闭集外取值不作断言）。
func actorFilter(kind string, id uint) error {
	if kind == "" {
		if id > 0 {
			return errors.New("actor_id 须与 actor_kind 同给（缺 actor_kind 时该过滤会被忽略）")
		}
		return nil
	}
	switch kind {
	case model.KBActorKindSystem:
		if id != 0 {
			return errors.New("actor_kind=system 恒 actor_id=0（作业面哨兵）")
		}
	case model.KBActorKindAdmin, model.KBActorKindUser:
		if id == 0 {
			return fmt.Errorf("actor_kind=%s 须给出 actor_id（缺 id 只会命中恒空集）", kind)
		}
	}
	return nil
}
