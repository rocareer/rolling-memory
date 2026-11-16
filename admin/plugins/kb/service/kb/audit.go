// plugin: kb v1.0.0
// 知识库审计面（I5）：kb_audit_log 只增不改不删。
//
// 契约：
//   - grant_add / grant_remove / owner_transfer 及其余写面变更**与业务同事务**写入
//     （调用方传事务句柄；任一步失败整事务回滚，不留「改了没记」）；
//   - 覆盖前台与作业行为（actor_kind ∈ admin|user|system；admin 轴**无免审计路径**——一切写面动作
//     一律经 writeAudit；读面（list/get/单资源校验）不落审计：动作闭集只含变更动作，
//     若需读审计须扩闭集（代码闸，加值零迁移）另议）；
//   - 列表只读、时间窗 + 过滤 + 上限，禁全表 count（增长表）。
package kb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ai-go-hub/ai-go-admin/internal/service"
	"github.com/ai-go-hub/ai-go-admin/plugins/kb/model"

	"gorm.io/gorm"
)

// auditActions 审计动作闭集（kb_audit_log.action；——加值零迁移：改闭集须同步正本与消费面）。
// 标签面扩展两值（tag_add / tag_remove；常量在本插件 model/kb_tag.go）：绑定/重挂与
// 解绑/重切重放失配丢弃——「留痕走审计不靠软删」的落点（丢弃以 detail.event='replay_drop' 区分）。
var auditActions = map[string]struct{}{
	model.KBAuditActionCreate:        {},
	model.KBAuditActionUpdate:        {},
	model.KBAuditActionDelete:        {},
	model.KBAuditActionGrantAdd:      {},
	model.KBAuditActionGrantRemove:   {},
	model.KBAuditActionOwnerTransfer: {},
	model.KBAuditActionPurge:         {},
	model.KBAuditActionImport:        {},
	model.KBAuditActionTagAdd:        {},
	model.KBAuditActionTagRemove:     {},
}

// auditActorKinds 审计主体闭集（admin→admins.id，user→users.id，system=0 哨兵；两 id 空间禁混用）
var auditActorKinds = map[string]struct{}{
	model.KBActorKindAdmin:  {},
	model.KBActorKindUser:   {},
	model.KBActorKindSystem: {},
}

// AuditEntry 审计写入入参
type AuditEntry struct {
	LibraryID  uint           // 关联库（0=无库上下文）
	DocumentID uint           // 关联文档（0=非文档对象）
	Actor      Actor          // 操作者（Kind 闭集；system 站作业面，ID 恒 0）
	Action     string         // 动作（auditActions 闭集）
	Detail     map[string]any // 变更明细（前后值/授权主体等；空则落 {}）
}

// writeAudit 写审计（**必须由调用方传入业务事务句柄**：与变更同事务，I5）。
// 闭集外的 action / actor_kind 一律拒（代码闸：响亮失败，不静默写脏值）。
func writeAudit(ctx context.Context, tx *gorm.DB, entry AuditEntry) error {
	if _, ok := auditActions[entry.Action]; !ok {
		return fmt.Errorf("kb 审计动作未登记: %s", entry.Action)
	}
	if _, ok := auditActorKinds[entry.Actor.Kind]; !ok {
		return fmt.Errorf("kb 审计主体类型未登记: %s", entry.Actor.Kind)
	}

	actorID := entry.Actor.ID
	if entry.Actor.Kind == model.KBActorKindSystem {
		actorID = 0 // system 哨兵：不得借作业面冒充 admin/user
	} else if actorID == 0 {
		return errors.New("kb 审计缺少操作者 ID（admin/user 轴必须 > 0）")
	}

	detail := "{}"
	if len(entry.Detail) > 0 {
		raw, err := json.Marshal(entry.Detail)
		if err != nil {
			return fmt.Errorf("审计明细序列化失败: %w", err)
		}
		detail = string(raw)
	}

	row := model.KBAuditLog{
		LibraryID:  entry.LibraryID,
		DocumentID: entry.DocumentID,
		ActorKind:  entry.Actor.Kind,
		ActorID:    actorID,
		Action:     entry.Action,
		Detail:     &detail,
	}
	return gorm.G[model.KBAuditLog](tx).Create(ctx, &row)
}

// auditListColumns 列表显式列（禁 SELECT *；detail jsonb 大字段不进列表）
const auditListColumns = "id, library_id, document_id, actor_kind, actor_id, action, created_at"

// AuditListOptions 审计列表选项（时间窗 + 过滤 + 上限）
type AuditListOptions struct {
	Page       int
	Limit      int
	Since      *time.Time // 时间窗起（缺省近 30 天——增长表收敛扫描面）
	Until      *time.Time // 时间窗止（nil=不限）
	LibraryID  uint
	DocumentID uint
	ActorKind  string
	ActorID    uint
	Action     string
}

// DefaultAuditWindow 审计列表缺省时间窗（近 30 天；导出供 HTTP 面回显生效窗口——
// 缺省口径单点仍在本包，调用方不得自设第二值）
const DefaultAuditWindow = 30 * 24 * time.Hour

// ListAuditLogs 审计只读列表（**admin 轴**；时间窗缺省近 30 天 + LIMIT；**不做全表 count**——
// 增长表只读口径：调用方按「有下一页」判定翻页，不做 total）。
func (s *Service) ListAuditLogs(ctx context.Context, v *ReadView, opts AuditListOptions) ([]model.KBAuditLog, error) {
	if v == nil || !v.IsAdmin() {
		return nil, ErrDenied // 审计面 = 后台轴；用户面读取列
	}

	// 时间窗（缺省近 30 天——增长表收敛扫描面；列表恒带窗，禁无界扫描）
	since := time.Now().Add(-DefaultAuditWindow)
	if opts.Since != nil {
		since = *opts.Since
	}
	q := gorm.G[model.KBAuditLog](s.db).Where("created_at >= ?", since)
	if opts.Until != nil {
		q = q.Where("created_at <= ?", *opts.Until)
	}
	if opts.LibraryID > 0 {
		q = q.Where("library_id = ?", opts.LibraryID) // 走 idx_kb_audit_log_library_id
	}
	if opts.DocumentID > 0 {
		q = q.Where("document_id = ?", opts.DocumentID)
	}
	if kind := strings.TrimSpace(opts.ActorKind); kind != "" {
		if _, ok := auditActorKinds[kind]; !ok {
			return nil, fmt.Errorf("审计主体类型未登记: %s", kind)
		}
		q = q.Where("actor_kind = ? AND actor_id = ?", kind, opts.ActorID) // 走 idx_kb_audit_log_actor
	}
	if action := strings.TrimSpace(opts.Action); action != "" {
		if _, ok := auditActions[action]; !ok {
			return nil, fmt.Errorf("审计动作未登记: %s", action)
		}
		q = q.Where("action = ?", action)
	}

	// 排序固定主键托底（无用户排序入参——复用基服务 BuildScopes，禁自拼 ORDER BY）
	scopes := s.auditBase.BuildScopes(service.Options{Page: opts.Page, Limit: NormalizeBrowseLimit(opts.Limit)})
	return q.Select(auditListColumns).Scopes(scopes...).Find(ctx)
}
