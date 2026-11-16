// plugin: kb v1.0.0
// Package kb 知识库服务核心：隔离与权限单点、库/文档读写面、审计与巡检。
//
// 隔离口径（本包为施工落点）：
//   - 三条轴：平台（隐式单例 P0，不落列）；系统级 owner_kind='system'（owner_id=0，仅后台创建）；
//     用户级 owner_kind='user'（owner_id=users.id，username 可被后台改禁作键）；
//     团队 = kb_team 稳定 id ↔ user_groups.name 按名绑定（成员判定仍走 MemberOfAny 语义单点）。
//   - 可见性单一来源 = kb_grant（owner ∪ grant；无 grant 即无权，非默认有权）；公开 = 'all' 一行；
//     撤销 = 软删 grant 行（即时生效）；读写两轴正交（write 不扩读，双授落两行）。
//   - 两轴谓词分离（admins.id 与 users.id 是独立自增空间，禁混用）：
//     user 轴：actor_kind='user'，actor_id=users.id —— owner ∪（perm='read' 的 all/user/team 授权行）；
//     admin 轴：actor_kind='admin'，actor_id=admins.id —— 经节点授权全库可见（**不借用 user 谓词**；
//     「全库」= 未删库全集：软删库对一切面不可见（含单资源门与按库收窄的文档面——统一谓词）；
//     一切写面操作同事务落 kb_audit_log，无免审计路径）。
//   - 入口守卫：actor_id ≤ 0 短路拒（subject_id=0 是 'all' 哨兵，喂 0 即全库放大）。
//   - 过滤一律进 SQL（单点谓词构造器；Count 与 List 同一构造器）；禁应用层后过滤。
//   - 列表两步反查（禁逐行 EXISTS 放大）：①组名集 → kb_team id 集（每请求一次、请求内复用）
//     ② kb_grant 反查命中 library_id 集；③主查询 = owner 集 ∪ ② + 排序（BuildOrderScope）+ LIMIT。
//   - 单资源校验用 EXISTS（命中 uk_kb_grant_library_subject_perm 前缀）。
//   - fail-closed：未登录 / 令牌错型 / 账号停用 / 组籍读异常 / 团队悬空 ⇒ 一律拒。
//     分工：未登录与令牌错型在入口面（宿主鉴权中间件：Bearer 提取 + 令牌类型 +
//     账号状态）拒；本层对「账号存在性/停用 + 组籍/团队映射读取」再做一次 fail-closed 核对
//     （防旁路入口直调服务层——服务层不信任任何未经 BuildReadView 解析的主体）。
//   - 「不存在」与「无权」同型：服务层同回 ErrDenied（HTTP 层一律 404，不泄露存在性）。
package kb

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ai-go-hub/ai-go-admin/internal/infra/database"
	"github.com/ai-go-hub/ai-go-admin/internal/repository"
	"github.com/ai-go-hub/ai-go-admin/internal/service"
	"github.com/ai-go-hub/ai-go-admin/plugins/kb/model"

	"gorm.io/gorm"
)

// 分页上限（/）：浏览面 ≤ MaxBrowseLimit；任何路径不得越过 MaxHardLimit（绝对硬线）。
const (
	DefaultBrowseLimit = 20
	MaxBrowseLimit     = 100  // 浏览面（库/文档/授权/审计清单）
	MaxHardLimit       = 1000 // 硬上限：内部/作业面批量读取的绝对线（浏览面已在其内）
)

// userStatusEnabled 前台账号启用态（enable|disable 闭集；由 user 轴主体面端口的实现方判定）
const userStatusEnabled = "enable"

// Actor 主体（两轴分离：Kind ∈ {admin, user}；ID 分别在 admins.id / users.id 两个自增空间）
type Actor struct {
	Kind string // model.KBActorKindAdmin | model.KBActorKindUser
	ID   uint
}

// 拒绝语义（fail-closed；「不存在」与「无权」同型）。
// 一切拒绝类错误都满足 errors.Is(err, ErrDenied)：HTTP 层一律按 404 同型响应，不泄露对象存在性；
// 细分值（InvalidActor/ActorUnavailable/FailClosed）仅用于日志、告警与用例断言，不得回显客户端。
var (
	// ErrDenied 拒绝访问根哨兵（对象不存在或无权——同型）
	ErrDenied = errors.New("kb: 拒绝访问（对象不存在或无权）")
	// ErrInvalidActor 入口守卫：actor_id ≤ 0（uint 下限即 0）或未登记轴
	ErrInvalidActor = fmt.Errorf("%w：主体非法（actor_id=0 或未登记轴）", ErrDenied)
	// ErrActorUnavailable 主体不可用：账号不存在（含软删）或已停用
	ErrActorUnavailable = fmt.Errorf("%w：主体不可用（账号停用或不存在）", ErrDenied)
	// ErrFailClosed 隔离判定依赖（组籍/团队映射）读取失败——fail-closed 拒，不降级放行
	ErrFailClosed = fmt.Errorf("%w：隔离判定依赖读取失败（fail-closed）", ErrDenied)
)

// ReadView 读视图（每请求解析一次；请求内复用）。
// 字段不导出：外部无法伪造 admin 视图——一律经 BuildReadView 按轴分派解析（禁旁路）。
type ReadView struct {
	actor   Actor
	teamIDs []uint // 步骤①（user 轴：组名集 → kb_team id 集；团队悬空 = 不在集内 ⇒ 拒）
}

// Actor 解析后的主体（只读）
func (v *ReadView) Actor() Actor { return v.actor }

// IsAdmin 是否 admin 轴（经节点授权全库可见；节点校验是 HTTP 中间件前置，本层只做轴分派与审计义务）
func (v *ReadView) IsAdmin() bool { return v.actor.Kind == model.KBActorKindAdmin }

// Service 知识库服务（隔离单点 + 读写面；进程内可按需多实例，无内部可变状态）
type Service struct {
	db *gorm.DB

	// 各模型泛型基服务：仅用于 BuildOrderScope（Schema 白名单 + 主键托底），禁自拼 ORDER BY
	libraryBase  service.IService[model.KBLibrary]
	documentBase service.IService[model.KBDocument]
	grantBase    service.IService[model.KBGrant]
	auditBase    service.IService[model.KBAuditLog]

	// 表名单点（与模型 TableName() 同源；raw 谓词禁裸写表名）
	tLibrary  string
	tDocument string
	tGrant    string
	tTeam     string
	tAudit    string
}

// NewService 创建知识库服务（全局 db 实例 + 默认仓储）
func NewService() *Service { return newServiceWithDB(database.DB()) }

// newServiceWithDB 注入 db 构造（用例注入捕获型会话/同一句柄时使用）
func newServiceWithDB(db *gorm.DB) *Service {
	s := &Service{
		db:        db,
		tLibrary:  tableOf(db, &model.KBLibrary{}, "kb_library"),
		tDocument: tableOf(db, &model.KBDocument{}, "kb_document"),
		tGrant:    tableOf(db, &model.KBGrant{}, "kb_grant"),
		tTeam:     tableOf(db, &model.KBTeam{}, "kb_team"),
		tAudit:    tableOf(db, &model.KBAuditLog{}, "kb_audit_log"),
	}
	s.libraryBase = service.NewService[model.KBLibrary](repository.NewRepository[model.KBLibrary](repository.WithDB(db)))
	s.documentBase = service.NewService[model.KBDocument](repository.NewRepository[model.KBDocument](repository.WithDB(db)))
	s.grantBase = service.NewService[model.KBGrant](repository.NewRepository[model.KBGrant](repository.WithDB(db)))
	s.auditBase = service.NewService[model.KBAuditLog](repository.NewRepository[model.KBAuditLog](repository.WithDB(db)))
	return s
}

// tableOf 表名单点：与模型 TableName() 同源（raw 谓词禁裸写表名）；解析失败回落字面名
// （db 为空时直接回落：路由装配在无 DB 的测试进程里也会构造服务）
func tableOf(db *gorm.DB, m any, fallback string) string {
	if db == nil {
		return fallback
	}
	stmt := &gorm.Statement{DB: db}
	if err := stmt.Parse(m); err != nil || stmt.Schema == nil || stmt.Schema.Table == "" {
		return fallback
	}
	return stmt.Schema.Table
}

// BuildReadView 读视图**谓词构造器入口**（入参 (actor_kind, actor_id)——硬条款）：
//   - actor_id == 0 → 短路拒（不查任何表；subject_id=0 是 'all' 哨兵，喂 0 即全库放大）；
//   - admin 轴：只认 (kind, id)，不解析组籍/团队（不借用 user 谓词）；
//   - user 轴：账号可用性（fail-closed）→ 启用组名集 → 团队 id 集（步骤①，每请求一次）；
//   - 其余 actor_kind（含 system——它是 owner_kind/审计侧闭集，不是请求主体轴）一律拒。
func (s *Service) BuildReadView(ctx context.Context, actorKind string, actorID uint) (*ReadView, error) {
	if actorID == 0 {
		return nil, ErrInvalidActor
	}
	switch actorKind {
	case model.KBActorKindAdmin:
		return &ReadView{actor: Actor{Kind: actorKind, ID: actorID}}, nil
	case model.KBActorKindUser:
		if err := s.ensureUserActive(ctx, actorID); err != nil {
			return nil, err
		}
		// 组籍/团队映射一律经 user 轴主体面端口（未接入用户体系 ⇒ fail-closed，不降级放行）
		names, err := CurrentPrincipal().EnabledGroupNames(ctx, actorID)
		if err != nil {
			return nil, fmt.Errorf("%w：组籍读取失败", ErrFailClosed)
		}
		teamIDs, err := s.enabledTeamIDs(ctx, names)
		if err != nil {
			return nil, fmt.Errorf("%w：团队映射读取失败", ErrFailClosed)
		}
		return &ReadView{actor: Actor{Kind: actorKind, ID: actorID}, teamIDs: teamIDs}, nil
	}
	return nil, ErrInvalidActor
}

// UserView 前台用户读视图（持 users.id；令牌类型/停用校验在入口面，本层再做一次 fail-closed 核对）
func (s *Service) UserView(ctx context.Context, userID uint) (*ReadView, error) {
	return s.BuildReadView(ctx, model.KBActorKindUser, userID)
}

// AdminView 后台管理员读视图（节点授权（AdminPermission）是 HTTP 侧前置；本层只做轴分派）
func (s *Service) AdminView(ctx context.Context, adminID uint) (*ReadView, error) {
	return s.BuildReadView(ctx, model.KBActorKindAdmin, adminID)
}

// ensureUserActive 账号可用性（fail-closed）：不存在（含软删）/ 停用 ⇒ 拒。
// 判定经 user 轴主体面端口（未接入用户体系 ⇒ ErrFailClosed；端口报错 ⇒ ErrActorUnavailable）——
// 本层不再自查宿主用户表（那属端口实现方的面）。
func (s *Service) ensureUserActive(ctx context.Context, userID uint) error {
	if err := CurrentPrincipal().UserActive(ctx, userID); err != nil {
		if IsPrincipalUnavailable(err) {
			return fmt.Errorf("%w：账号读取失败", ErrFailClosed)
		}
		return ErrActorUnavailable
	}
	return nil
}

// enabledTeamIDs 步骤①：组名集 → kb_team id 集（status=1 ∧ 未删；每请求一次，请求内复用）。
// 组名不在集内 / 团队禁用或已删（悬空）⇒ 不产出 id ⇒ 相关授权一律不命中（fail-closed）。
func (s *Service) enabledTeamIDs(ctx context.Context, groupNames []string) ([]uint, error) {
	if len(groupNames) == 0 {
		return nil, nil
	}
	ids := make([]uint, 0, 8)
	if err := gorm.G[model.KBTeam](s.db).
		Select("id").
		Where("status = 1 AND group_name IN ?", groupNames).
		Order("id ASC").
		Scan(ctx, &ids); err != nil {
		return nil, err
	}
	return ids, nil
}

// grantMatchCond **单点主体谓词**（EXISTS 与反查两条路径共用；grant 表别名固定为 g）：
//
//	g.deleted_at IS NULL ∧ g.perm = ? ∧ ( 公开 ∨ 本人 ∨ 团队 ∈ 步骤①集 )
//
// 团队分支仅在 teamIDs 非空时拼入（空集 = 无团队授权命中，不发 IN (NULL) 兜底查询）。
func (v *ReadView) grantMatchCond(perm string) (string, []any) {
	cond := "g.deleted_at IS NULL AND g.perm = ? AND (g.subject_kind = ? OR (g.subject_kind = ? AND g.subject_id = ?)"
	args := []any{perm, model.KBGrantSubjectAll, model.KBGrantSubjectUser, v.actor.ID}
	if len(v.teamIDs) > 0 {
		cond += " OR (g.subject_kind = ? AND g.subject_id IN ?)"
		args = append(args, model.KBGrantSubjectTeam, v.teamIDs)
	}
	return cond + ")", args
}

// libraryAccessCond 库侧**单点谓词**（单资源读/写与取数共用；EXISTS 命中 uk_kb_grant_library_subject_perm 前缀）：
//   - admin 轴：仅未删（全库可见——含禁用库，供管理面；不借用 user 谓词）；
//   - user 轴：未删 ∧ status=1 ∧（owner=本人 ∨ EXISTS 授权行（按 perm 轴））。
func (s *Service) libraryAccessCond(v *ReadView, perm string) (string, []any) {
	lib := s.tLibrary
	if v.IsAdmin() {
		return lib + ".deleted_at IS NULL", nil
	}
	match, matchArgs := v.grantMatchCond(perm)
	cond := lib + ".deleted_at IS NULL AND " + lib + ".status = 1 AND " +
		"((" + lib + ".owner_kind = ? AND " + lib + ".owner_id = ?)" +
		" OR EXISTS (SELECT 1 FROM " + s.tGrant + " g WHERE g.library_id = " + lib + ".id AND " + match + "))"
	args := []any{model.KBLibraryOwnerKindUser, v.actor.ID}
	return cond, append(args, matchArgs...)
}

// libraryListFilter 列表/计数**同一构造器**（步骤③：owner 集 ∪ ② 反查集；Count 与 List 共用，
// 防两处口径穿帮）。反查集为空 ⇒ 只留 owner 分支（无授权即只读得到自己建的库）。
func (s *Service) libraryListFilter(v *ReadView, grantLibraryIDs []uint) (string, []any) {
	lib := s.tLibrary
	if v.IsAdmin() {
		return lib + ".deleted_at IS NULL", nil
	}
	cond := lib + ".deleted_at IS NULL AND " + lib + ".status = 1 AND ((" + lib + ".owner_kind = ? AND " + lib + ".owner_id = ?)"
	args := []any{model.KBLibraryOwnerKindUser, v.actor.ID}
	if len(grantLibraryIDs) > 0 {
		cond += " OR " + lib + ".id IN ?"
		args = append(args, grantLibraryIDs)
	}
	return cond + ")", args
}

// manageableCond 管理权谓词（**管理权 = 属主 ∪ 后台轴**；写授权不扩管理权——I3 归属与授权分离）。
// 不含 status 条件：属主/后台可改回启用（禁用库停写不停管）。
func (s *Service) manageableCond(v *ReadView) (string, []any) {
	lib := s.tLibrary
	cond := lib + ".deleted_at IS NULL"
	if v.IsAdmin() {
		return cond, nil
	}
	return cond + " AND " + lib + ".owner_kind = ? AND " + lib + ".owner_id = ?", []any{model.KBLibraryOwnerKindUser, v.actor.ID}
}

// grantLibraryIDs 步骤②：kb_grant 反查命中 library_id 集（每请求一次、请求内复用；
// 走 idx_kb_grant_subject 部分索引，非逐行 EXISTS）。
func (s *Service) grantLibraryIDs(ctx context.Context, v *ReadView, perm string) ([]uint, error) {
	if v.IsAdmin() {
		return nil, nil // admin 轴不借用 user 谓词（调用方不消费该集）
	}
	match, args := v.grantMatchCond(perm)
	query := "SELECT DISTINCT g.library_id FROM " + s.tGrant + " g WHERE " + match
	ids := make([]uint, 0, 16)
	if err := s.db.WithContext(ctx).Raw(query, args...).Scan(&ids).Error; err != nil {
		return nil, err
	}
	return ids, nil
}

// ReadableLibrary 单资源读校验（读谓词要求 perm='read'）
func (s *Service) ReadableLibrary(ctx context.Context, v *ReadView, libraryID uint) (bool, error) {
	return s.libraryAccessible(ctx, v, libraryID, model.KBGrantPermRead)
}

// WritableLibrary 单资源写校验（写 = owner ∪ perm='write' 授权 ∪ 后台轴；write 不扩读）
func (s *Service) WritableLibrary(ctx context.Context, v *ReadView, libraryID uint) (bool, error) {
	return s.libraryAccessible(ctx, v, libraryID, model.KBGrantPermWrite)
}

// ManageableLibrary 单资源管理权校验（属主 ∪ 后台轴；授权变更/库元数据/删除/归属转移的门）。
// 两轴统一走 manageableCond（admin 轴 = 未删；user 轴 = 未删 ∧ 属主）——与取数面 manageableLibraryRow
// **同一谓词单点**，禁 O(1) 恒真短路（已整改：原 admin 分支恒真绕过库谓词 ⇒ 软删库仍可经
// 管理门列授权（ListGrants 残留可见面），与 F8 同批的 libraryAccessible 及本包「软删库对一切面
// 不可见」口径相悖）。
func (s *Service) ManageableLibrary(ctx context.Context, v *ReadView, libraryID uint) (bool, error) {
	if v == nil || libraryID == 0 {
		return false, nil
	}
	cond, args := s.manageableCond(v)
	args = append([]any{libraryID}, args...)
	n, err := gorm.G[model.KBLibrary](s.db).
		Where(s.tLibrary+".id = ? AND "+cond, args...).
		Count(ctx, "id")
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// libraryAccessible 单资源读/写校验单点（EXISTS；两轴统一走 libraryAccessCond 谓词——**含「库未删除」**）。
//
// 口径：admin 轴原为 O(1) 恒真短路，绕过库谓词 ⇒ 已软删库仍被单资源门放行
// （文档列表等按 library_id 收窄的面可列出软删库内文档）。现两轴统一：admin 轴亦为「未删」单查
// （库不存在/已软删 ⇒ 不可命中；节点授权不等于绕过软删，与 documentAccessCond/可读集口径一致）；
// user 轴 = 未删 ∧ status=1 ∧（owner ∪ 对应 perm 授权行）。
func (s *Service) libraryAccessible(ctx context.Context, v *ReadView, libraryID uint, perm string) (bool, error) {
	if v == nil {
		return false, ErrDenied // 未登录/未解析视图：一律拒
	}
	if libraryID == 0 {
		return false, nil // 裸 0 恒不可命中（同型：上层按 404 处理）
	}
	cond, args := s.libraryAccessCond(v, perm)
	args = append([]any{libraryID}, args...)
	n, err := gorm.G[model.KBLibrary](s.db).
		Where(s.tLibrary+".id = ? AND "+cond, args...).
		Count(ctx, "id")
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// RequireRead 单资源读门（不通过回 ErrDenied——「不存在」与「无权」同型）
func (s *Service) RequireRead(ctx context.Context, v *ReadView, libraryID uint) error {
	ok, err := s.ReadableLibrary(ctx, v, libraryID)
	if err != nil {
		return err
	}
	if !ok {
		return ErrDenied
	}
	return nil
}

// RequireWrite 单资源写门（读/写两谓词正交：写过不蕴含读过）
func (s *Service) RequireWrite(ctx context.Context, v *ReadView, libraryID uint) error {
	ok, err := s.WritableLibrary(ctx, v, libraryID)
	if err != nil {
		return err
	}
	if !ok {
		return ErrDenied
	}
	return nil
}

// RequireManage 单资源管理权门（授权面板/库变更/删除/归属转移）
func (s *Service) RequireManage(ctx context.Context, v *ReadView, libraryID uint) error {
	ok, err := s.ManageableLibrary(ctx, v, libraryID)
	if err != nil {
		return err
	}
	if !ok {
		return ErrDenied
	}
	return nil
}

// manageableLibraryRow 管理权校验 + 取行（事务内；未命中/无权同型 ErrDenied；显式列）
func (s *Service) manageableLibraryRow(ctx context.Context, tx *gorm.DB, v *ReadView, libraryID uint) (*model.KBLibrary, error) {
	if v == nil || libraryID == 0 {
		return nil, ErrDenied
	}
	cond, args := s.manageableCond(v)
	args = append([]any{libraryID}, args...)
	row, err := gorm.G[model.KBLibrary](tx).
		Where(s.tLibrary+".id = ? AND "+cond, args...).
		Select(libraryDetailColumns).
		First(ctx)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrDenied
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// clampLimit 分页上限漏斗（唯一限度实现）：浏览面 max=MaxBrowseLimit；内部/作业面 max=MaxHardLimit。
// limit < 1 回落 DefaultBrowseLimit。
func clampLimit(limit, max int) int {
	if max < 1 {
		max = 1
	}
	if limit < 1 {
		limit = DefaultBrowseLimit
	}
	if limit > max {
		limit = max
	}
	return limit
}

// NormalizeBrowseLimit 浏览面 limit（≤ MaxBrowseLimit；缺省 DefaultBrowseLimit）
func NormalizeBrowseLimit(limit int) int { return clampLimit(limit, MaxBrowseLimit) }

// NormalizeHardLimit 内部/作业面 limit（≤ MaxHardLimit——绝对硬线，任何路径不得越过）
func NormalizeHardLimit(limit int) int { return clampLimit(limit, MaxHardLimit) }

// normalizeSortList 排序入参归一（闭集白名单 + 主键托底）：
// 不命中白名单回落 id DESC；方向仅认 asc/ascending，其余一律 DESC。
// id 与「主键托底」同轴（BuildOrderScope 恒附加主键 DESC）→ 归并到托底，避免 ORDER BY 重复子句。
func normalizeSortList(field, order string, allowed map[string]bool) (string, string) {
	field = strings.TrimSpace(field)
	if field == "" || field == "id" || !allowed[field] {
		return "", ""
	}
	switch strings.ToLower(strings.TrimSpace(order)) {
	case "asc", "ascending":
		return field, "asc"
	default:
		return field, "desc"
	}
}

// isUniqueViolation 唯一键冲突判定（GORM 未开 TranslateError ⇒ 按 SQLSTATE/文案判；照 eval 先例）
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "23505") || strings.Contains(msg, "duplicate key")
}
