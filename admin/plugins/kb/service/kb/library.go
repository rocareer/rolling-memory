// plugin: kb v1.0.0
// 知识库库面服务（I3/I6 +手写核心树）：列表（两步反查）、详情、变更、软删、归属转移、
// 授权面板（grant_add / grant_remove 与审计同事务）。
//
// 谓词分派（全部经 acl.go 单点构造器）：
//   - 读/写：libraryAccessCond（owner ∪ 授权行；perm 轴正交）；
//   - 管理：manageableCond（owner ∪ 后台轴——授权变更/元数据/删除/归属转移的门；写授权不扩管理权）；
//   - 列表：libraryListFilter（owner 集 ∪ 反查集；Count 与 List 同一构造器）。
package kb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/ai-go-hub/ai-go-admin/internal/service"
	"github.com/ai-go-hub/ai-go-admin/plugins/kb/model"

	"gorm.io/gorm"
)

// PurgeEnqueueHook 清理作业入队钩子（best-effort；键口径见调用点注释）。
// 清理处理器与入队钩子同批接线（本插件 service/kb/job.go 注册）——未注册 kind 会被
// jobs 响亮落 failed（作业面 worker 的终态判定），故此处不直接入队；
// 启动段（作业面自管）把它接到 jobs.NewStore().Enqueue(kind="kb:purge", key=...) 包装函数即可。
var PurgeEnqueueHook func(ctx context.Context, key string) error

// enqueuePurge 清理入队（best-effort：失败只记日志，不回滚已提交的软删——作业幂等可重放）
func enqueuePurge(ctx context.Context, key string) {
	if PurgeEnqueueHook == nil {
		return
	}
	if err := PurgeEnqueueHook(ctx, key); err != nil {
		log.Printf("[kb] 清理作业入队失败（键 %s）: %v", key, err)
	}
}

// libraryListColumns 库列表显式列（禁 SELECT *；chunk_config jsonb 大字段不进列表）
const libraryListColumns = "id, owner_kind, owner_id, name, description, status, updated_at, created_at"

// libraryDetailColumns 库详情列（显式；含 chunk_config）
const libraryDetailColumns = "id, owner_kind, owner_id, name, description, chunk_config, status, updated_at, created_at"

// librarySortFields 库列表可排序列（数值/时间列闭集；禁 text/jsonb 大字段排序）
var librarySortFields = map[string]bool{
	"id": true, "owner_id": true, "status": true, "updated_at": true, "created_at": true,
}

// LibraryListOptions 库列表选项（浏览面上限 MaxBrowseLimit）
//
// 过滤面 = 隔离谓词（不可绕过）之上的管理面收敛：owner/status/name 快搜/id 区间/时间窗。
// name ILIKE 仅用于 kb_library（管理面小表，库数目级；>10 万行量级才需 trgm 支撑——SQL 纪律口径）。
type LibraryListOptions struct {
	Page        int
	Limit       int
	SortField   string
	SortOrder   string
	OwnerKind   string // system|user|''（空=不过滤；闭集外忽略）
	OwnerID     uint   // 与 OwnerKind 同给才生效
	Status      *uint8 // nil=不过滤
	NameLike    string // 库名快搜（ILIKE %kw%，通配符转义）
	IDFrom      *uint
	IDTo        *uint
	CreatedFrom *time.Time
	CreatedTo   *time.Time
	UpdatedFrom *time.Time
	UpdatedTo   *time.Time
}

// LibraryInput 新建库入参（owner_kind 闭集；系统级 owner_id 恒 0）
type LibraryInput struct {
	OwnerKind   string
	OwnerID     uint
	Name        string
	Description string
	ChunkConfig *string // jsonb；空白串=未配置（NULL）
	Status      *uint8  // nil → 缺省 1（启用）
}

// LibraryUpdate 更新入参（nil = 不改；owner_kind/owner_id 不可改——收编走 TransferLibraryOwner）
type LibraryUpdate struct {
	Name        *string
	Description *string
	ChunkConfig *string
	Status      *uint8
}

// GrantInput 授权入参（subject_kind ∈ all|team|user；perm ∈ read|write）
type GrantInput struct {
	SubjectKind string
	SubjectID   uint
	Perm        string
}

// ListLibraries 库列表（步骤②反查 → 同一构造器过滤 → BuildOrderScope → LIMIT；返回总数）。
func (s *Service) ListLibraries(ctx context.Context, v *ReadView, opts LibraryListOptions) ([]model.KBLibrary, int64, error) {
	if v == nil {
		return nil, 0, ErrDenied
	}

	// 步骤②：kb_grant 反查命中 library_id 集（每请求一次；admin 轴不消费）
	ids, err := s.grantLibraryIDs(ctx, v, model.KBGrantPermRead)
	if err != nil {
		return nil, 0, fmt.Errorf("可读集反查失败: %w", err)
	}

	// 步骤③：过滤条件（Count 与 List **同一构造器**）
	cond, args := s.libraryListFilter(v, ids)
	lq := gorm.G[model.KBLibrary](s.db).Where(cond, args...)
	cq := gorm.G[model.KBLibrary](s.db).Where(cond, args...)

	if kind := strings.TrimSpace(opts.OwnerKind); kind == model.KBLibraryOwnerKindSystem || kind == model.KBLibraryOwnerKindUser {
		lq, cq = lq.Where("owner_kind = ?", kind), cq.Where("owner_kind = ?", kind)
		if opts.OwnerID > 0 {
			lq, cq = lq.Where("owner_id = ?", opts.OwnerID), cq.Where("owner_id = ?", opts.OwnerID)
		}
	}
	if opts.Status != nil {
		lq, cq = lq.Where("status = ?", *opts.Status), cq.Where("status = ?", *opts.Status)
	}
	if kw := strings.TrimSpace(opts.NameLike); kw != "" {
		like := "%" + escapeLike(kw) + "%"
		lq, cq = lq.Where(s.tLibrary+".name ILIKE ?", like), cq.Where(s.tLibrary+".name ILIKE ?", like)
	}
	if opts.IDFrom != nil {
		lq, cq = lq.Where(s.tLibrary+".id >= ?", *opts.IDFrom), cq.Where(s.tLibrary+".id >= ?", *opts.IDFrom)
	}
	if opts.IDTo != nil {
		lq, cq = lq.Where(s.tLibrary+".id <= ?", *opts.IDTo), cq.Where(s.tLibrary+".id <= ?", *opts.IDTo)
	}
	if opts.CreatedFrom != nil {
		lq, cq = lq.Where(s.tLibrary+".created_at >= ?", *opts.CreatedFrom), cq.Where(s.tLibrary+".created_at >= ?", *opts.CreatedFrom)
	}
	if opts.CreatedTo != nil {
		lq, cq = lq.Where(s.tLibrary+".created_at <= ?", *opts.CreatedTo), cq.Where(s.tLibrary+".created_at <= ?", *opts.CreatedTo)
	}
	if opts.UpdatedFrom != nil {
		lq, cq = lq.Where(s.tLibrary+".updated_at >= ?", *opts.UpdatedFrom), cq.Where(s.tLibrary+".updated_at >= ?", *opts.UpdatedFrom)
	}
	if opts.UpdatedTo != nil {
		lq, cq = lq.Where(s.tLibrary+".updated_at <= ?", *opts.UpdatedTo), cq.Where(s.tLibrary+".updated_at <= ?", *opts.UpdatedTo)
	}

	total, err := cq.Count(ctx, "id")
	if err != nil {
		return nil, 0, err
	}

	field, order := normalizeSortList(opts.SortField, opts.SortOrder, librarySortFields)
	scopes := s.libraryBase.BuildScopes(service.Options{
		SortField: field,
		SortOrder: order,
		Page:      opts.Page,
		Limit:     NormalizeBrowseLimit(opts.Limit),
	})
	rows, err := lq.Select(libraryListColumns).Scopes(scopes...).Find(ctx)
	if err != nil {
		return nil, 0, err
	}
	if err := s.fillOwnerUsernames(ctx, rows); err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// fillOwnerUsernames 回填属主用户名（**展示字段** OwnerUsername；owner_kind=user 行按 users.id 反查）。
//
// 单条 IN 查询随带（行数 = 页上限 ≤100 / 详情 1 行；users 主键索引，EXPLAIN 走索引扫描），
// 不并入列表 SQL：库列表列清单（libraryListColumns，禁 SELECT *）与排序面保持不动，也免去
// 对 users 表名/前缀的 raw 拼接（表名单点禁裸写——acl.go 口径）。属主已删/查无 ⇒ 空串，
// 前端回落裸 owner_id（标识键仍是 owner_id；username 可改，禁作键——）。
func (s *Service) fillOwnerUsernames(ctx context.Context, rows []model.KBLibrary) error {
	ids := make([]uint, 0, len(rows))
	seen := make(map[uint]struct{}, len(rows))
	for _, row := range rows {
		if row.OwnerKind != model.KBLibraryOwnerKindUser || row.OwnerID == 0 {
			continue
		}
		if _, ok := seen[row.OwnerID]; ok {
			continue
		}
		seen[row.OwnerID] = struct{}{}
		ids = append(ids, row.OwnerID)
	}
	if len(ids) == 0 {
		return nil
	}
	// 用户名经 user 轴主体面端口（展示字段）。端口不可用（未接入用户体系）⇒ owner_username
	// 整体不填、**列表照常返回**：可见性判定不经本字段，缺展示字段不改变权限语义。
	names, err := CurrentPrincipal().UserNames(ctx, ids)
	if err != nil {
		if IsPrincipalUnavailable(err) {
			return nil
		}
		return err
	}
	for i := range rows {
		if rows[i].OwnerKind == model.KBLibraryOwnerKindUser {
			rows[i].OwnerUsername = names[rows[i].OwnerID]
		}
	}
	return nil
}

// GetLibrary 单库详情（读视图；不存在/无权同型 ErrDenied）
func (s *Service) GetLibrary(ctx context.Context, v *ReadView, libraryID uint) (*model.KBLibrary, error) {
	if v == nil || libraryID == 0 {
		return nil, ErrDenied
	}
	cond, args := s.libraryAccessCond(v, model.KBGrantPermRead)
	args = append([]any{libraryID}, args...)
	row, err := gorm.G[model.KBLibrary](s.db).
		Where(s.tLibrary+".id = ? AND "+cond, args...).
		Select(libraryDetailColumns).
		First(ctx)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrDenied
	}
	if err != nil {
		return nil, err
	}
	rows := []model.KBLibrary{row}
	if err := s.fillOwnerUsernames(ctx, rows); err != nil {
		return nil, err
	}
	return &rows[0], nil
}

// CreateLibrary 新建库（同事务写审计 create；uk(owner_kind,owner_id,name) 拦重名）。
//
// 轴纪律：系统级库仅后台轴可建；前台轴只允许「自建自属」用户级库（前台建库的对外开闸
// 前台面只读，无路由暴露本方法）。
func (s *Service) CreateLibrary(ctx context.Context, actor Actor, in LibraryInput) (*model.KBLibrary, error) {
	if actor.ID == 0 {
		return nil, ErrInvalidActor
	}

	kind := strings.TrimSpace(in.OwnerKind)
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, errors.New("库名必填")
	}

	ownerID := in.OwnerID
	switch kind {
	case model.KBLibraryOwnerKindSystem:
		ownerID = 0 // 系统级哨兵：owner_id 恒 0
	case model.KBLibraryOwnerKindUser:
		if ownerID == 0 {
			return nil, errors.New("用户级库必须指定属主（users.id）")
		}
	default:
		return nil, fmt.Errorf("属主类型未登记: %s", kind)
	}

	switch actor.Kind {
	case model.KBActorKindAdmin:
		// 后台代建/系统级建库：任意属主（节点授权为 HTTP 侧前置）
	case model.KBActorKindUser:
		if kind != model.KBLibraryOwnerKindUser || ownerID != actor.ID {
			return nil, ErrDenied // 禁前台建系统级库 / 代建他人库
		}
		if err := s.ensureUserActive(ctx, actor.ID); err != nil {
			return nil, err // 自建自属亦须账号可用（fail-closed：停用/已删账号不建库）
		}
	default:
		return nil, ErrInvalidActor
	}

	status := uint8(1)
	if in.Status != nil {
		status = *in.Status
	}
	if status != 0 && status != 1 {
		return nil, errors.New("状态值域: 0=禁用,1=启用")
	}
	chunkConfig, err := normalizeChunkConfig(in.ChunkConfig)
	if err != nil {
		return nil, err
	}

	lib := model.KBLibrary{
		OwnerKind:   kind,
		OwnerID:     ownerID,
		Name:        name,
		Description: strings.TrimSpace(in.Description),
		ChunkConfig: chunkConfig,
		Status:      &status,
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 属主存在性（用户级 owner_id = users.id；username 可改禁作键——）。后台代建同样校验，
		// 防写入悬空属主/把 admins.id 当 users.id（两 id 空间独立自增，禁混用）。
		if kind == model.KBLibraryOwnerKindUser {
			if err := checkOwnerExists(ctx, ownerID); err != nil {
				return err
			}
		}
		if err := gorm.G[model.KBLibrary](tx).Create(ctx, &lib); err != nil {
			return err
		}
		return writeAudit(ctx, tx, AuditEntry{
			LibraryID: lib.ID,
			Actor:     actor,
			Action:    model.KBAuditActionCreate,
			Detail:    map[string]any{"owner_kind": kind, "owner_id": ownerID, "name": name, "status": status},
		})
	})
	if err != nil {
		if isUniqueViolation(err) {
			return nil, fmt.Errorf("同属主下库名已存在: %s", name)
		}
		return nil, err
	}
	return &lib, nil
}

// UpdateLibrary 更新库元数据（管理权 = 属主 ∪ 后台轴；同事务写审计 update，记前后值；真变才写）。
func (s *Service) UpdateLibrary(ctx context.Context, actor Actor, libraryID uint, in LibraryUpdate) (*model.KBLibrary, error) {
	view, err := s.BuildReadView(ctx, actor.Kind, actor.ID)
	if err != nil {
		return nil, err
	}
	if libraryID == 0 {
		return nil, ErrDenied
	}

	var updated model.KBLibrary
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		row, err := s.manageableLibraryRow(ctx, tx, view, libraryID)
		if err != nil {
			return err
		}

		changes := map[string]any{}
		after := map[string]any{}
		before := map[string]any{}

		if in.Name != nil {
			name := strings.TrimSpace(*in.Name)
			if name == "" {
				return errors.New("库名必填")
			}
			if name != row.Name {
				changes["name"], after["name"], before["name"] = name, name, row.Name
			}
		}
		if in.Description != nil {
			desc := strings.TrimSpace(*in.Description)
			if desc != row.Description {
				changes["description"], after["description"], before["description"] = desc, desc, row.Description
			}
		}
		if in.ChunkConfig != nil {
			cfg, err := normalizeChunkConfig(in.ChunkConfig)
			if err != nil {
				return err
			}
			if !sameStrPtr(cfg, row.ChunkConfig) {
				changes["chunk_config"] = cfg // nil = 清空（列 NULL）
				after["chunk_config"] = derefStr(cfg)
				before["chunk_config"] = derefStr(row.ChunkConfig)
			}
		}
		if in.Status != nil {
			if *in.Status != 0 && *in.Status != 1 {
				return errors.New("状态值域: 0=禁用,1=启用")
			}
			if row.Status == nil || *row.Status != *in.Status {
				changes["status"] = *in.Status
				after["status"] = *in.Status
				before["status"] = derefUint8(row.Status)
			}
		}

		if len(changes) == 0 {
			updated = *row // 无真变：不写库、不落审计
			return nil
		}
		changes["updated_at"] = gorm.Expr("NOW()")
		if _, err := gorm.G[map[string]any](tx).Table(s.tLibrary).
			Where("id = ? AND deleted_at IS NULL", row.ID).
			Updates(ctx, changes); err != nil {
			return err
		}
		if err := writeAudit(ctx, tx, AuditEntry{
			LibraryID: row.ID,
			Actor:     actor,
			Action:    model.KBAuditActionUpdate,
			Detail:    map[string]any{"before": before, "after": after},
		}); err != nil {
			return err
		}
		refreshed, err := gorm.G[model.KBLibrary](tx).Where("id = ?", row.ID).First(ctx)
		if err != nil {
			return err
		}
		updated = refreshed
		return nil
	})
	if err != nil {
		if isUniqueViolation(err) {
			return nil, fmt.Errorf("同属主下库名已存在")
		}
		return nil, err
	}
	return &updated, nil
}

// DeleteLibrary 删库（**管理权**；软删即刻——读路径即时不可见；同事务写审计 delete；
// 清理入队经 PurgeEnqueueHook，键 kb:<library_id>，入队接线——I6）。
func (s *Service) DeleteLibrary(ctx context.Context, actor Actor, libraryID uint) error {
	view, err := s.BuildReadView(ctx, actor.Kind, actor.ID)
	if err != nil {
		return err
	}
	if libraryID == 0 {
		return ErrDenied
	}

	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		row, err := s.manageableLibraryRow(ctx, tx, view, libraryID)
		if err != nil {
			return err
		}
		if _, err := gorm.G[model.KBLibrary](tx).Where("id = ?", row.ID).Delete(ctx); err != nil {
			return err
		}
		return writeAudit(ctx, tx, AuditEntry{
			LibraryID: row.ID,
			Actor:     actor,
			Action:    model.KBAuditActionDelete,
			Detail:    map[string]any{"name": row.Name, "owner_kind": row.OwnerKind, "owner_id": row.OwnerID},
		})
	})
	if err != nil {
		return err
	}

	enqueuePurge(ctx, "kb:"+strconv.FormatUint(uint64(libraryID), 10))
	return nil
}

// TransferLibraryOwner 归属转移（**仅后台轴**显式操作 + 同事务审计 owner_transfer；
// 授权行一律不动——「转移归属不自动改授权」）。目标属主必须存在（fail-closed）。
func (s *Service) TransferLibraryOwner(ctx context.Context, actor Actor, libraryID uint, ownerKind string, ownerID uint) error {
	if actor.Kind != model.KBActorKindAdmin || actor.ID == 0 {
		return ErrDenied // 用户级→系统级收编 / 归属转移仅允许后台显式操作
	}
	if libraryID == 0 {
		return ErrDenied
	}

	kind := strings.TrimSpace(ownerKind)
	switch kind {
	case model.KBLibraryOwnerKindSystem:
		ownerID = 0
	case model.KBLibraryOwnerKindUser:
		if ownerID == 0 {
			return errors.New("用户级库必须指定属主（users.id）")
		}
	default:
		return fmt.Errorf("属主类型未登记: %s", kind)
	}

	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 后台轴视图（本方法已限定仅后台轴；节点授权为 HTTP 侧前置）
		row, err := s.manageableLibraryRow(ctx, tx, &ReadView{actor: actor}, libraryID)
		if err != nil {
			return err
		}
		if row.OwnerKind == kind && row.OwnerID == ownerID {
			return nil // 目标态即现状：不写库、不落审计
		}
		if kind == model.KBLibraryOwnerKindUser {
			if err := checkOwnerExists(ctx, ownerID); err != nil {
				return err
			}
		}
		if _, err := gorm.G[map[string]any](tx).Table(s.tLibrary).
			Where("id = ? AND deleted_at IS NULL", row.ID).
			Updates(ctx, map[string]any{
				"owner_kind": kind,
				"owner_id":   ownerID,
				"updated_at": gorm.Expr("NOW()"),
			}); err != nil {
			return err
		}
		return writeAudit(ctx, tx, AuditEntry{
			LibraryID: row.ID,
			Actor:     actor,
			Action:    model.KBAuditActionOwnerTransfer,
			Detail: map[string]any{
				"before": map[string]any{"owner_kind": row.OwnerKind, "owner_id": row.OwnerID},
				"after":  map[string]any{"owner_kind": kind, "owner_id": ownerID},
			},
		})
	})
	return err
}

// grantListColumns 授权列表显式列（禁 SELECT *）
const grantListColumns = "id, library_id, subject_kind, subject_id, perm, granted_by_kind, granted_by_id, updated_at, created_at"

// ListGrants 库授权面板列表（**管理权**门——与 grant_add/grant_remove 同门：授权主体枚举只对
// 属主 ∪ 后台轴开放，读授权行不得借读权扩出主体清单；主键托底序 + 浏览面上限 + total 回显）。
func (s *Service) ListGrants(ctx context.Context, v *ReadView, libraryID uint, page, limit int) ([]model.KBGrant, int64, error) {
	if err := s.RequireManage(ctx, v, libraryID); err != nil {
		return nil, 0, err
	}
	// Count 与 List 同一 library_id 收窄（面板行数用于「是否还有下一页」）
	q := gorm.G[model.KBGrant](s.db).Where("library_id = ?", libraryID)
	cq := gorm.G[model.KBGrant](s.db).Where("library_id = ?", libraryID)
	total, err := cq.Count(ctx, "id")
	if err != nil {
		return nil, 0, err
	}
	// 排序固定主键托底（无用户排序入参——复用基服务 BuildScopes，禁自拼 ORDER BY）
	scopes := s.grantBase.BuildScopes(service.Options{Page: page, Limit: NormalizeBrowseLimit(limit)})
	rows, err := q.
		Select(grantListColumns).
		Scopes(scopes...).
		Find(ctx)
	if err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// errGrantConcurrent 并发双授的唯一键冲突哨兵：PostgreSQL 语句错误后事务进入 aborted 态，
// 不得在同一 tx 内回读（必报 current transaction is aborted）——由 AddGrant 在事务回滚后新连接回读。
var errGrantConcurrent = errors.New("kb: 授权并发冲突（回读现有行）")

// AddGrant 授权（**管理权**门；主体闭集校验 + 悬空主体拒绝；同事务写审计 grant_add）。
// 幂等：同 (library, subject, perm) 活跃行已存在 ⇒ 直接返回现有行（不重复落审计）；
// 并发双授 ⇒ 唯一键冲突后**在事务外**回读现有行（幂等收口，见 errGrantConcurrent）。
func (s *Service) AddGrant(ctx context.Context, actor Actor, libraryID uint, in GrantInput) (*model.KBGrant, error) {
	view, err := s.BuildReadView(ctx, actor.Kind, actor.ID)
	if err != nil {
		return nil, err
	}
	if libraryID == 0 {
		return nil, ErrDenied
	}

	kind := strings.TrimSpace(in.SubjectKind)
	perm := strings.TrimSpace(in.Perm)
	subjectID := in.SubjectID
	switch kind {
	case model.KBGrantSubjectAll:
		subjectID = 0 // 公开 = 'all' 一行，subject_id 恒 0（哨兵）
	case model.KBGrantSubjectTeam:
		if subjectID == 0 {
			return nil, errors.New("团队授权必须指定 kb_team.id")
		}
	case model.KBGrantSubjectUser:
		if subjectID == 0 {
			return nil, errors.New("用户授权必须指定 users.id")
		}
	default:
		return nil, fmt.Errorf("授权主体类型未登记: %s", kind)
	}
	if perm != model.KBGrantPermRead && perm != model.KBGrantPermWrite {
		return nil, fmt.Errorf("权限轴未登记: %s", perm)
	}

	var grant model.KBGrant
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := s.manageableLibraryRow(ctx, tx, view, libraryID); err != nil {
			return err
		}
		if err := checkSubjectAlive(ctx, tx, kind, subjectID); err != nil {
			return err
		}

		existing, err := gorm.G[model.KBGrant](tx).
			Select(grantListColumns).
			Where("library_id = ? AND subject_kind = ? AND subject_id = ? AND perm = ?", libraryID, kind, subjectID, perm).
			First(ctx)
		if err == nil {
			grant = existing // 幂等：已授直接返回
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		grant = model.KBGrant{
			LibraryID:     libraryID,
			SubjectKind:   kind,
			SubjectID:     subjectID,
			Perm:          perm,
			GrantedByKind: actor.Kind,
			GrantedByID:   actor.ID,
		}
		if err := gorm.G[model.KBGrant](tx).Create(ctx, &grant); err != nil {
			if isUniqueViolation(err) {
				// 并发双授：本事务已中止（PostgreSQL 语义）——哨兵交给事务外回读；审计由先提交方写。
				return errGrantConcurrent
			}
			return err
		}
		return writeAudit(ctx, tx, AuditEntry{
			LibraryID: libraryID,
			Actor:     actor,
			Action:    model.KBAuditActionGrantAdd,
			Detail:    map[string]any{"subject_kind": kind, "subject_id": subjectID, "perm": perm, "grant_id": grant.ID},
		})
	})
	if err != nil {
		if errors.Is(err, errGrantConcurrent) {
			// 事务已回滚：在事务外（新快照）回读先提交方的行；失败即如实上报（不回显幂等假象）
			row, rerr := gorm.G[model.KBGrant](s.db).
				Select(grantListColumns).
				Where("library_id = ? AND subject_kind = ? AND subject_id = ? AND perm = ?", libraryID, kind, subjectID, perm).
				First(ctx)
			if rerr != nil {
				return nil, fmt.Errorf("并发授权后回读失败: %w", rerr)
			}
			return &row, nil
		}
		return nil, err
	}
	return &grant, nil
}

// RemoveGrant 撤销授权（**管理权**门；软删 grant 行 = 即时生效；同事务写审计 grant_remove）。
func (s *Service) RemoveGrant(ctx context.Context, actor Actor, libraryID, grantID uint) error {
	view, err := s.BuildReadView(ctx, actor.Kind, actor.ID)
	if err != nil {
		return err
	}
	if libraryID == 0 || grantID == 0 {
		return ErrDenied
	}

	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := s.manageableLibraryRow(ctx, tx, view, libraryID); err != nil {
			return err
		}
		row, err := gorm.G[model.KBGrant](tx).
			Select(grantListColumns).
			Where("id = ? AND library_id = ?", grantID, libraryID).
			First(ctx)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrDenied // 已撤销/不存在：同型
		}
		if err != nil {
			return err
		}
		if _, err := gorm.G[model.KBGrant](tx).Where("id = ?", row.ID).Delete(ctx); err != nil {
			return err
		}
		return writeAudit(ctx, tx, AuditEntry{
			LibraryID: libraryID,
			Actor:     actor,
			Action:    model.KBAuditActionGrantRemove,
			Detail: map[string]any{
				"subject_kind": row.SubjectKind,
				"subject_id":   row.SubjectID,
				"perm":         row.Perm,
				"grant_id":     row.ID,
			},
		})
	})
}

// checkSubjectAlive 授权主体存活性（fail-closed：悬空主体不许授权——团队映射/用户必须现行可用）。
func checkSubjectAlive(ctx context.Context, tx *gorm.DB, kind string, subjectID uint) error {
	switch kind {
	case model.KBGrantSubjectTeam:
		if _, err := gorm.G[model.KBTeam](tx).Select("id").Where("id = ? AND status = 1", subjectID).First(ctx); err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("团队不存在、已删或已禁用（悬空主体不授权）: %d", subjectID)
			}
			return err
		}
	case model.KBGrantSubjectUser:
		if err := checkOwnerExists(ctx, subjectID); err != nil {
			if IsPrincipalUnavailable(err) {
				// 未接入用户体系 ⇒ fail-closed：不静默放行，也不谎称「主体不存在」
				return err
			}
			return fmt.Errorf("用户不存在或已删除（悬空主体不授权）: %d", subjectID)
		}
	}
	return nil
}

// checkOwnerExists 用户主体存在性（属主/被授权用户）：经 user 轴主体面端口。
//
// 三类结果分别如实上抛（**不做语义折叠**）：
//   - 存在 ⇒ nil；
//   - 端口不可用（未接入用户体系）⇒ ErrFailClosed（与「主体不存在」不同源，日志可区分）；
//   - 端口报「不存在」或其它错误 ⇒ 保留原始原因（`%w` 包装）并以「目标属主不存在」起头——
//     UI 文案同型，排障时仍能看到端口侧的真实错误（瞬时 DB 错误不会被伪装成「主体不存在」）。
func checkOwnerExists(ctx context.Context, userID uint) error {
	err := CurrentPrincipal().UserExists(ctx, userID)
	if err == nil {
		return nil
	}
	if IsPrincipalUnavailable(err) {
		return err
	}
	return fmt.Errorf("目标属主不存在: %d: %w", userID, err)
}

// normalizeChunkConfig 切片配置归一（写侧）：nil = 未配置；空白串 = 清空（NULL）；
// 其余须为合法 JSON（jsonb 列拒非法值）。未知键的响亮拒绝随 chunk.Effective 单点落地（C3）。
func normalizeChunkConfig(raw *string) (*string, error) {
	if raw == nil {
		return nil, nil
	}
	text := strings.TrimSpace(*raw)
	if text == "" {
		return nil, nil
	}
	if !json.Valid([]byte(text)) {
		return nil, errors.New("chunk_config 必须是合法 JSON")
	}
	return &text, nil
}

// sameStrPtr 字符串指针等值（nil 语义：NULL/未配置）
func sameStrPtr(a, b *string) bool {
	return derefStr(a) == derefStr(b)
}

// derefStr 空安全解引用（*string → string；nil = ""）
func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// derefUint8 空安全解引用（*uint8 → uint8；nil = 0）
func derefUint8(p *uint8) uint8 {
	if p == nil {
		return 0
	}
	return *p
}
