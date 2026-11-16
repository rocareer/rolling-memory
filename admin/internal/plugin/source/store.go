package source

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ai-go-hub/ai-go-admin/internal/infra/database"
	"github.com/ai-go-hub/ai-go-admin/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// DBProvider 每次操作时取数据库句柄（便于测试注入；生产用 database.DB）。
type DBProvider func(context.Context) (*gorm.DB, error)

// Filter 列表筛选条件（与宿主通用 List 的 wheres 形状对齐；字段与运算符均走白名单）。
type Filter struct {
	Field    string `json:"field"`
	Operator string `json:"operator"`
	Value    any    `json:"value"`
}

// FilterGroup 一组筛选条件（Or 控制组内连接；组间一律 AND，对齐宿主通用 List 的 wheres 语义）。
type FilterGroup struct {
	Filters []Filter
	Or      bool
}

// ListSourcesQuery 来源列表查询。
type ListSourcesQuery struct {
	Page   int
	Limit  int
	Sort   string
	Order  string
	Wheres []FilterGroup
}

// SourcePatch 来源更新字段（nil = 不改；ClearSnapshot 由调用方在 type/location/安全开关变化时置 true）。
type SourcePatch struct {
	Title              *string
	Type               *string
	Location           *string
	Enabled            *int16
	AllowInsecureHTTP  *int16
	AllowPrivateHosts  *int16
	AllowAbsolutePaths *int16
	Weigh              *int64
	Remark             *string
	ClearSnapshot      bool
	UpdatedBy          string
}

// ListEntriesQuery 清单快照分页查询。
type ListEntriesQuery struct {
	SourceID uint
	UID      string
	Version  string
	Keyword  string
	Page     int
	Limit    int
}

// SourceSummary 清单页的来源页签数据（含条目数）。
type SourceSummary struct {
	Source
	EntryCount int64 `json:"entry_count"`
}

// InstalledPlugin 台账里的已安装信息（清单页只读合并）。
type InstalledPlugin struct {
	Version  string `json:"version"`
	Status   string `json:"status"`
	Checksum string `json:"checksum"`
	SignedBy string `json:"signed_by"`
	Source   string `json:"source"`
}

// Store 来源与清单快照的持久化接口（生产为 GormStore；测试可注入内存实现）。
type Store interface {
	ListSources(ctx context.Context, q ListSourcesQuery) ([]Source, int64, error)
	GetSource(ctx context.Context, id uint) (*Source, error)
	GetSourceByName(ctx context.Context, name string) (*Source, error)
	CreateSource(ctx context.Context, src *Source) error
	UpdateSource(ctx context.Context, id uint, patch SourcePatch) (snapshotCleared bool, err error)
	DeleteSources(ctx context.Context, ids []uint) (int64, error)
	ReplaceCatalog(ctx context.Context, src *Source, catalog *Catalog, catalogSHA string, syncedAt time.Time) error
	MarkSyncFailure(ctx context.Context, id uint, message string) error
	ListEntries(ctx context.Context, q ListEntriesQuery) ([]CatalogEntry, int64, error)
	ListEntryPeers(ctx context.Context, pairs []UIDVersion) ([]CatalogEntry, error)
	ListSourcesSummary(ctx context.Context) ([]SourceSummary, error)
	InstalledVersions(ctx context.Context, uids []string) (map[string]InstalledPlugin, error)
}

// ListAllSources 分页取全部来源（同步缺省目标与来源摘要用；单页上限 100）。
func ListAllSources(ctx context.Context, store Store) ([]Source, error) {
	if store == nil {
		return nil, errors.New("来源存储不可用")
	}
	out := make([]Source, 0, 16)
	for page := 1; ; page++ {
		rows, total, err := store.ListSources(ctx, ListSourcesQuery{Page: page, Limit: 100})
		if err != nil {
			return nil, err
		}
		out = append(out, rows...)
		if len(rows) == 0 || int64(len(out)) >= total {
			break
		}
	}
	return out, nil
}

// GormStore Store 的 GORM 实现（表名走模型 TableName + GORM 命名策略前缀）。
type GormStore struct {
	db DBProvider
}

// NewGormStore 构造 GORM 存储（provider 为 nil 时所有操作返回不可用错误）。
func NewGormStore(provider DBProvider) *GormStore {
	return &GormStore{db: provider}
}

func (s *GormStore) handle(ctx context.Context) (*gorm.DB, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("数据库句柄不可用")
	}
	db, err := s.db(ctx)
	if err != nil {
		return nil, err
	}
	if db == nil {
		return nil, errors.New("数据库句柄不可用")
	}
	return db.WithContext(ctx), nil
}

// ListSources 分页列出来源（默认 weigh DESC, id DESC）。
//
// 筛选条件走 `gorm.G[Source](db, exprs...)` 的**子句入参**，不是预挂在 *gorm.DB 上再交给泛型入口：
// 泛型入口在非 DryRun 下会以 NewDB 会话重建语句（gorm.io/gorm 的 generics.go 里
// `db.Session(&Session{NewDB: true})`），预挂的 Where 会被静默丢弃——SQL 里既无 WHERE 也不报错
// （清单列举的同类缺陷见 ListEntries 的注释与 store_db_test.go 的库级用例）。
func (s *GormStore) ListSources(ctx context.Context, q ListSourcesQuery) ([]Source, int64, error) {
	db, err := s.handle(ctx)
	if err != nil {
		return nil, 0, err
	}
	exprs := sourceFilterExpressions(q.Wheres)
	total, err := gorm.G[Source](db, exprs...).Count(ctx, "id")
	if err != nil {
		return nil, 0, err
	}
	page, limit := normalizePage(q.Page, q.Limit)
	order := sourceOrder(q.Sort, q.Order)
	rows, err := gorm.G[Source](db, exprs...).Order(order).Offset((page - 1) * limit).Limit(limit).Find(ctx)
	if err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// GetSource 按主键取来源。
func (s *GormStore) GetSource(ctx context.Context, id uint) (*Source, error) {
	db, err := s.handle(ctx)
	if err != nil {
		return nil, err
	}
	row, err := gorm.G[Source](db).Where("id = ?", id).First(ctx)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// GetSourceByName 按来源键取来源（name 唯一）。
func (s *GormStore) GetSourceByName(ctx context.Context, name string) (*Source, error) {
	db, err := s.handle(ctx)
	if err != nil {
		return nil, err
	}
	row, err := gorm.G[Source](db).Where("name = ?", name).First(ctx)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// CreateSource 新增来源（name 重复返回 ErrNameConflict）。
func (s *GormStore) CreateSource(ctx context.Context, src *Source) error {
	if src == nil {
		return fmt.Errorf("%w: 来源为空", ErrInvalidInput)
	}
	db, err := s.handle(ctx)
	if err != nil {
		return err
	}
	if _, err := gorm.G[Source](db).Where("name = ?", src.Name).First(ctx); err == nil {
		return ErrNameConflict
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	if err := gorm.G[Source](db).Create(ctx, src); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "duplicate key") {
			return ErrNameConflict
		}
		return err
	}
	return nil
}

// UpdateSource 更新来源白名单字段；返回是否清空了清单快照。
func (s *GormStore) UpdateSource(ctx context.Context, id uint, patch SourcePatch) (bool, error) {
	db, err := s.handle(ctx)
	if err != nil {
		return false, err
	}
	if _, err := gorm.G[Source](db).Where("id = ?", id).First(ctx); errors.Is(err, gorm.ErrRecordNotFound) {
		return false, ErrNotFound
	} else if err != nil {
		return false, err
	}
	now := time.Now()
	updates := map[string]any{"updated_at": now}
	if patch.UpdatedBy != "" {
		updates["updated_by"] = patch.UpdatedBy
	}
	if patch.Title != nil {
		updates["title"] = strings.TrimSpace(*patch.Title)
	}
	if patch.Type != nil {
		updates["type"] = strings.ToLower(strings.TrimSpace(*patch.Type))
		if err := propagateTypeFlags(updates, strings.ToLower(strings.TrimSpace(*patch.Type))); err != nil {
			return false, err
		}
	}
	if patch.Location != nil {
		updates["location"] = strings.TrimSpace(*patch.Location)
	}
	if patch.Enabled != nil {
		updates["enabled"] = *patch.Enabled
	}
	if patch.AllowInsecureHTTP != nil {
		updates["allow_insecure_http"] = *patch.AllowInsecureHTTP
	}
	if patch.AllowPrivateHosts != nil {
		updates["allow_private_hosts"] = *patch.AllowPrivateHosts
	}
	if patch.AllowAbsolutePaths != nil {
		updates["allow_absolute_paths"] = *patch.AllowAbsolutePaths
	}
	if patch.Weigh != nil {
		updates["weigh"] = *patch.Weigh
	}
	if patch.Remark != nil {
		updates["remark"] = strings.TrimSpace(*patch.Remark)
	}
	if patch.ClearSnapshot {
		updates["last_sync_status"] = ""
		updates["last_sync_error"] = ""
		updates["last_catalog_sha256"] = ""
		updates["last_catalog_generated_at"] = nil
		updates["last_synced_at"] = nil
	}
	// 白名单字段（含 enabled=0 / weigh=0 / 清空快照）用 map 落库，避免结构体更新跳过零值。
	// 快照清空必须**同时删快照行**（与 enabled 无关，只看 ChangeSnapshot）：快照的语义是「该来源这份
	// 清单解析出的条目集合」（表 admin_plugin_catalog_entries），只把来源行的 last_* 置空会出现
	// 「来源显示未同步、清单页仍列出其条目并计入 entry_count」的自相矛盾状态（方案）。
	// 与 DeleteSources 同口径：来源行与快照行在同一事务；已安装插件不受影响。
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&Source{}).Where("id = ?", id).Updates(updates).Error; err != nil {
			return err
		}
		if patch.ClearSnapshot {
			if _, err := gorm.G[CatalogEntry](tx).Where("source_id = ?", id).Delete(ctx); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return false, err
	}
	return patch.ClearSnapshot, nil
}

// DeleteSources 删除来源并在同一事务删除其清单快照（已安装插件不受影响）。
func (s *GormStore) DeleteSources(ctx context.Context, ids []uint) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	db, err := s.handle(ctx)
	if err != nil {
		return 0, err
	}
	var deleted int64
	err = db.Transaction(func(tx *gorm.DB) error {
		if _, err := gorm.G[CatalogEntry](tx).Where("source_id IN ?", ids).Delete(ctx); err != nil {
			return err
		}
		n, err := gorm.G[Source](tx).Where("id IN ?", ids).Delete(ctx)
		if err != nil {
			return err
		}
		deleted = int64(n)
		return nil
	})
	if err != nil {
		return 0, err
	}
	return deleted, nil
}

// ReplaceCatalog 在一个事务内替换来源的全部快照行并更新 last_*；
// 同来源同 (uid,version) 的包摘要变化 ⇒ 整份失败（ErrVersionContentMutated），保留旧快照。
func (s *GormStore) ReplaceCatalog(ctx context.Context, src *Source, catalog *Catalog, catalogSHA string, syncedAt time.Time) error {
	if src == nil || catalog == nil {
		return fmt.Errorf("%w: 来源或清单为空", ErrInvalidInput)
	}
	db, err := s.handle(ctx)
	if err != nil {
		return err
	}
	generatedAt, err := parseGeneratedAt(catalog.GeneratedAt)
	if err != nil {
		return err
	}
	return db.Transaction(func(tx *gorm.DB) error {
		existing, err := gorm.G[CatalogEntry](tx).Where("source_id = ?", src.ID).Find(ctx)
		if err != nil {
			return err
		}
		oldSHA := make(map[string]string, len(existing))
		for i := range existing {
			oldSHA[existing[i].UID+"@"+existing[i].Version] = existing[i].PackageSHA256
		}
		for i := range catalog.Plugins {
			entry := &catalog.Plugins[i]
			key := entry.UID + "@" + entry.Version
			if old, ok := oldSHA[key]; ok && old != entry.Package.SHA256 {
				return fmt.Errorf("%w: %s 的包摘要由 %s 变为 %s（请升版本号，不能改写已发布版本）",
					ErrVersionContentMutated, key, old, entry.Package.SHA256)
			}
		}
		if _, err := gorm.G[CatalogEntry](tx).Where("source_id = ?", src.ID).Delete(ctx); err != nil {
			return err
		}
		now := syncedAt
		for i := range catalog.Plugins {
			entry := &catalog.Plugins[i]
			major, minor, patch, err := ParseVersion(entry.Version)
			if err != nil {
				return err
			}
			requires, _, err := NormalizeRequires(entry.UID, entry.Requires)
			if err != nil {
				return err
			}
			signed := int16(0)
			if entry.Package.Signed != nil && *entry.Package.Signed {
				signed = 1
			}
			row := &CatalogEntry{
				SourceID:      src.ID,
				SourceName:    src.Name,
				UID:           entry.UID,
				Version:       entry.Version,
				VersionMajor:  major,
				VersionMinor:  minor,
				VersionPatch:  patch,
				TitleZH:       entry.TitleZH,
				TitleEN:       entry.TitleEN,
				DescZH:        entry.DescZH,
				DescEN:        entry.DescEN,
				Kind:          entry.Kind,
				HostAPI:       entry.HostAPI,
				FrameworkReq:  entry.FrameworkReq,
				Requires:      requires,
				License:       entry.License,
				PackageURL:    entry.Package.URL,
				PackagePath:   entry.Package.Path,
				PackageSize:   entry.Package.Size,
				PackageSHA256: entry.Package.SHA256,
				PackageSigned: signed,
				CatalogSHA256: catalogSHA,
				SyncedAt:      now,
				CreatedAt:     &now,
				UpdatedAt:     &now,
			}
			if err := gorm.G[CatalogEntry](tx).Create(ctx, row); err != nil {
				return err
			}
		}
		result := tx.Model(&Source{}).Where("id = ?", src.ID).Updates(map[string]any{
			"last_sync_status":          "ok",
			"last_sync_error":           "",
			"last_catalog_sha256":       catalogSHA,
			"last_catalog_generated_at": generatedAt,
			"last_synced_at":            now,
			"updated_at":                now,
		})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// MarkSyncFailure 记录最后一次同步失败（不改 last_catalog_sha256/快照，旧快照保留）。
func (s *GormStore) MarkSyncFailure(ctx context.Context, id uint, message string) error {
	db, err := s.handle(ctx)
	if err != nil {
		return err
	}
	now := time.Now()
	result := db.Model(&Source{}).Where("id = ?", id).Updates(map[string]any{
		"last_sync_status": "error",
		"last_sync_error":  TruncateSyncError(message),
		"updated_at":       now,
	})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// ListEntries 分页列举快照（默认 uid 升序、版本降序）。
//
// 过滤条件以**子句入参**交给泛型入口 `gorm.G[CatalogEntry](db, exprs...)`，不是预挂在 *gorm.DB 上：
// 泛型入口在非 DryRun 下会以 NewDB 会话重建语句（gorm.io/gorm 的 generics.go：
// `db.Session(&Session{NewDB: true})` 得到的 Statement 里 Clauses 为空），预挂的 Where 会被静默丢弃
// ——SQL 里既无 WHERE 也不报错，rows/total 变成未过滤读数；逐条断言见 store_db_test.go 的库级用例。
func (s *GormStore) ListEntries(ctx context.Context, q ListEntriesQuery) ([]CatalogEntry, int64, error) {
	db, err := s.handle(ctx)
	if err != nil {
		return nil, 0, err
	}
	exprs := entryFilterExpressions(q)
	total, err := gorm.G[CatalogEntry](db, exprs...).Count(ctx, "id")
	if err != nil {
		return nil, 0, err
	}
	page, limit := normalizePage(q.Page, q.Limit)
	rows, err := gorm.G[CatalogEntry](db, exprs...).
		Order("uid ASC").
		Order("version_major DESC").
		Order("version_minor DESC").
		Order("version_patch DESC").
		Order("source_id ASC").
		Offset((page - 1) * limit).
		Limit(limit).
		Find(ctx)
	if err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// entryFilterExpressions 把清单列举的过滤条件编译成 gorm 子句入参（各条之间 AND）。
func entryFilterExpressions(q ListEntriesQuery) []clause.Expression {
	exprs := make([]clause.Expression, 0, 4)
	if q.SourceID > 0 {
		exprs = append(exprs, clause.Expr{SQL: "source_id = ?", Vars: []any{q.SourceID}})
	}
	if uid := strings.TrimSpace(q.UID); uid != "" {
		exprs = append(exprs, clause.Expr{SQL: "uid = ?", Vars: []any{uid}})
	}
	if version := strings.TrimSpace(q.Version); version != "" {
		exprs = append(exprs, clause.Expr{SQL: "version = ?", Vars: []any{version}})
	}
	if keyword := strings.TrimSpace(q.Keyword); keyword != "" {
		like := "%" + keyword + "%"
		op := "ILIKE"
		if database.IsSQLite() {
			op = "LIKE"
		}
		exprs = append(exprs, clause.Expr{
			SQL:  "(uid " + op + " ? OR title_zh " + op + " ? OR title_en " + op + " ?)",
			Vars: []any{like, like, like},
		})
	}
	return exprs
}

// ListEntryPeers 取与本页 (uid,version) 相关的全部来源行（跨来源冲突检测；不只看本页）。
func (s *GormStore) ListEntryPeers(ctx context.Context, pairs []UIDVersion) ([]CatalogEntry, error) {
	if len(pairs) == 0 {
		return nil, nil
	}
	db, err := s.handle(ctx)
	if err != nil {
		return nil, err
	}
	uids := make([]string, 0, len(pairs))
	seen := make(map[string]struct{}, len(pairs))
	for _, pair := range pairs {
		if _, ok := seen[pair.UID]; ok {
			continue
		}
		seen[pair.UID] = struct{}{}
		uids = append(uids, pair.UID)
	}
	rows, err := gorm.G[CatalogEntry](db).Where("uid IN ?", uids).Find(ctx)
	if err != nil {
		return nil, err
	}
	wanted := make(map[string]struct{}, len(pairs))
	for _, pair := range pairs {
		wanted[pair.UID+"@"+pair.Version] = struct{}{}
	}
	out := make([]CatalogEntry, 0, len(rows))
	for i := range rows {
		if _, ok := wanted[rows[i].UID+"@"+rows[i].Version]; ok {
			out = append(out, rows[i])
		}
	}
	return out, nil
}

// ListSourcesSummary 全部来源（含条目数；停用来源也返回，供“全部”页签展示历史快照）。
func (s *GormStore) ListSourcesSummary(ctx context.Context) ([]SourceSummary, error) {
	db, err := s.handle(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := gorm.G[Source](db).Order("weigh DESC, id DESC").Find(ctx)
	if err != nil {
		return nil, err
	}
	type countRow struct {
		SourceID uint  `gorm:"column:source_id"`
		Count    int64 `gorm:"column:count"`
	}
	var counts []countRow
	if err := db.Model(&CatalogEntry{}).
		Select("source_id, COUNT(*) AS count").
		Group("source_id").
		Scan(&counts).Error; err != nil {
		return nil, err
	}
	countMap := make(map[uint]int64, len(counts))
	for _, row := range counts {
		countMap[row.SourceID] = row.Count
	}
	out := make([]SourceSummary, 0, len(rows))
	for i := range rows {
		out = append(out, SourceSummary{Source: rows[i], EntryCount: countMap[rows[i].ID]})
	}
	return out, nil
}

// InstalledVersions 按 uid 批量读台账（清单页合并已安装状态）。
func (s *GormStore) InstalledVersions(ctx context.Context, uids []string) (map[string]InstalledPlugin, error) {
	out := make(map[string]InstalledPlugin, len(uids))
	if len(uids) == 0 {
		return out, nil
	}
	db, err := s.handle(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := gorm.G[model.Plugin](db).Select("name, version, status, checksum, signed_by, source").Where("name IN ?", uids).Find(ctx)
	if err != nil {
		return nil, err
	}
	for i := range rows {
		out[rows[i].Name] = InstalledPlugin{
			Version:  rows[i].Version,
			Status:   rows[i].Status,
			Checksum: rows[i].Checksum,
			SignedBy: rows[i].SignedBy,
			Source:   rows[i].Source,
		}
	}
	return out, nil
}

// ---- 内部辅助 ----

func normalizePage(page, limit int) (int, int) {
	if page < 1 {
		page = 1
	}
	if limit < 1 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	return page, limit
}

func sourceOrder(sort, order string) string {
	allowed := map[string]string{
		"id": "id", "name": "name", "title": "title", "type": `"type"`,
		"enabled": "enabled", "weigh": "weigh", "last_sync_status": "last_sync_status",
		"last_synced_at": "last_synced_at", "updated_at": "updated_at", "created_at": "created_at",
	}
	col, ok := allowed[strings.TrimSpace(sort)]
	if !ok {
		return "weigh DESC, id DESC"
	}
	dir := "ASC"
	if strings.EqualFold(strings.TrimSpace(order), "desc") {
		dir = "DESC"
	}
	return col + " " + dir + ", id DESC"
}

// sourceFilterExpressions 把筛选组编译成 gorm 子句入参：组内按 group.Or 连接，组间 AND
// （与宿主通用 List 的 wheres 语义一致）。
//
// 返回 []clause.Expression（而非已挂条件的 *gorm.DB）是**必须**的：调用方要把它交给泛型入口
// `gorm.G[Source](db, exprs...)`，预挂在 *gorm.DB 上的条件会被泛型入口的 NewDB 会话丢掉。
// 每组编译成一个 clause.Expr（SQL 里自行加括号），组间由 gorm 的 clause.Where 以 AND 拼接。
func sourceFilterExpressions(groups []FilterGroup) []clause.Expression {
	exprs := make([]clause.Expression, 0, len(groups))
	for _, group := range groups {
		clauses := make([]string, 0, len(group.Filters))
		args := make([]any, 0, len(group.Filters)*2)
		for _, filter := range group.Filters {
			clause, values, ok := sourceFilterClause(filter)
			if !ok {
				continue // 未在白名单内的字段/运算符不参与查询（与通用 service 的“未知字段跳过”同口径）
			}
			clauses = append(clauses, clause)
			args = append(args, values...)
		}
		if len(clauses) == 0 {
			continue
		}
		joiner := " AND "
		if group.Or {
			joiner = " OR "
		}
		exprs = append(exprs, clause.Where{Exprs: []clause.Expression{
			clause.Expr{SQL: "(" + strings.Join(clauses, joiner) + ")", Vars: args},
		}})
	}
	if len(exprs) == 0 {
		return nil
	}
	return exprs
}

func sourceFilterClause(filter Filter) (string, []any, bool) {
	fields := map[string]string{
		"id": "id", "name": "name", "title": "title", "type": `"type"`, "location": "location",
		"enabled": "enabled", "allow_insecure_http": "allow_insecure_http",
		"allow_private_hosts": "allow_private_hosts", "allow_absolute_paths": "allow_absolute_paths",
		"weigh": "weigh", "last_sync_status": "last_sync_status",
		"last_synced_at": "last_synced_at", "updated_at": "updated_at", "created_at": "created_at",
		"remark": "remark",
	}
	col, ok := fields[strings.TrimSpace(filter.Field)]
	if !ok {
		return "", nil, false
	}
	op := operatorSQL(filter.Operator)
	if op == "" {
		return "", nil, false
	}
	switch op {
	case "IS NULL", "IS NOT NULL":
		return col + " " + op, nil, true
	case "BETWEEN", "NOT BETWEEN":
		text, ok := filter.Value.(string)
		if !ok {
			return "", nil, false
		}
		parts := strings.SplitN(text, ",", 2)
		if len(parts) != 2 {
			return "", nil, false
		}
		return col + " " + op + " ? AND ?", []any{strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])}, true
	case "LIKE", "NOT LIKE", "ILIKE", "NOT ILIKE":
		if database.IsSQLite() {
			op = strings.ReplaceAll(op, "ILIKE", "LIKE")
		}
		value := filter.Value
		if text, ok := value.(string); ok && !strings.Contains(text, "%") {
			value = "%" + text + "%"
		}
		return col + " " + op + " ?", []any{value}, true
	case "IN", "NOT IN":
		return col + " " + op + " (?)", []any{filter.Value}, true
	default:
		return col + " " + op + " ?", []any{filter.Value}, true
	}
}

func operatorSQL(op string) string {
	switch strings.TrimSpace(op) {
	case "eq", "":
		return "="
	case "ne":
		return "!="
	case "gt":
		return ">"
	case "egt", "gte":
		return ">="
	case "lt":
		return "<"
	case "elt", "lte":
		return "<="
	case "LIKE", "NOT LIKE", "ILIKE", "NOT ILIKE":
		return strings.TrimSpace(op)
	case "IN", "NOT IN":
		return strings.TrimSpace(op)
	case "BETWEEN", "NOT BETWEEN":
		return strings.TrimSpace(op)
	case "NULL":
		return "IS NULL"
	case "NOT NULL":
		return "IS NOT NULL"
	default:
		return ""
	}
}

func propagateTypeFlags(updates map[string]any, typ string) error {
	switch typ {
	case TypeLocal:
		updates["allow_insecure_http"] = int16(0)
		updates["allow_private_hosts"] = int16(0)
	case TypeHTTP, TypeGit:
		updates["allow_absolute_paths"] = int16(0)
	default:
		return fmt.Errorf("%w: type %q 不在闭集内", ErrInvalidInput, typ)
	}
	return nil
}

func parseGeneratedAt(raw string) (*time.Time, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	value, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil, fmt.Errorf("%w: generated_at %q 不是 RFC3339", ErrInvalidInput, raw)
	}
	return &value, nil
}
