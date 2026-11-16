// plugin: ai v0.2.0
// store_supplier.go 是厂商（plugin_ai_supplier）的 SQL 原子操作住所：CRUD + channel_count + 删除保护。
//
// 为什么单开一个文件而不是并入 store.go：store.go 是数据契约与渠道写路径的集中面（渠道候选集与
// UpsertUsage 的列面改动都在其内），厂商资源面独立成文件，避免与该面的并行改动互相干扰。
// 分层与纪律与 store.go 完全一致：
//   - 只有代码常量与已白名单化的列名进 SQL 文本，值一律占位符；
//   - 物理表名只经 model.go 的 table() 拼前缀，quoteIdent 只包裹已复核的名字；
//   - api_key 由 store 原样返回（调用/继承需要真值），**出口脱敏是 handler 的职责**。
//
// 渠道表的两个事实由渠道表迁移落地（本文件按名引用，不依赖其 Go 结构体，互不阻塞）：
//   - 渠道新增列 supplier_id（0=独立渠道），删除保护与 channel_count 都只读它；
//   - 继承合并（渠道非空优先、空则取厂商）在 store.Candidates，本文件不重复实现。
package ai

import (
	"context"
	"errors"
	"strings"

	"gorm.io/gorm"
)

// DefaultSupplierListLimit 厂商列表默认分页（与渠道/模型同值 10；上限仍走 store.go 的 MaxListLimit=100）。
const DefaultSupplierListLimit = 10

// supplierSortColumns 排序白名单（用户输入只命中键，SQL 文本恒为代码常量）。
//
// channel_count 走相关子查询（列表页要能按「该厂商挂了多少渠道」排序）；因为子查询里要拼
// 当前进程的表前缀，所以用函数返回而不是包级 var（包级初始化期不能安全调用 table()）。
func supplierSortColumns() map[string]string {
	return map[string]string{
		"id":            `"id"`,
		"name":          `"name"`,
		"vendor":        `"vendor"`,
		"timeout":       `"timeout"`,
		"status":        `"status"`,
		"create_time":   `"create_time"`,
		"update_time":   `"update_time"`,
		"channel_count": `(SELECT COUNT(*) FROM ` + quoteIdent(table("plugin_ai_channel")) + ` WHERE "supplier_id" = ` + quoteIdent(table("plugin_ai_supplier")) + `."id")`,
	}
}

// SupplierFilter 厂商列表筛选（顶层等值 status/vendor + keyword 快搜 name ILIKE；高级 wheres 由 handler 合并）
type SupplierFilter struct {
	Page    int
	Limit   int
	Keyword string
	Status  *int16
	Vendor  string
	Sort    string
	Order   string
}

// SupplierPatch 厂商可编辑列（nil = 不修改；创建/更新时间由 store 维护）。
type SupplierPatch struct {
	Name    *string
	Vendor  *string
	BaseURL *string
	APIKey  *string
	Driver  *string
	Timeout *int
	Status  *int16
	Remark  *string
}

// SupplierListResult 厂商分页结果：Rows + Total + 每个厂商的 channel_count（一次分组查询取回）。
type SupplierListResult struct {
	Rows   []Supplier      `json:"list"`
	Total  int64           `json:"total"`
	Counts map[int64]int64 `json:"-"`
}

// supplierQuery 列表/计数的共同筛选（复用同一实现，避免 list 与 total 条件漂移）。
func (s *Store) supplierQuery(ctx context.Context, f SupplierFilter) *gorm.DB {
	q := s.db.WithContext(ctx).Model(&Supplier{})
	if f.Status != nil {
		q = q.Where(`"status" = ?`, *f.Status)
	}
	if vendor := strings.TrimSpace(f.Vendor); vendor != "" {
		q = q.Where(`"vendor" = ?`, vendor)
	}
	if keyword := strings.TrimSpace(f.Keyword); keyword != "" {
		q = q.Where(`"name" `+s.likeOp()+` ?`, "%"+keyword+"%")
	}
	return q
}

// ListSuppliers 厂商分页列表（无高级检索；带 wheres 的入口见 listSuppliersWithWheres）。
func (s *Store) ListSuppliers(ctx context.Context, f SupplierFilter) (SupplierListResult, error) {
	return s.listSuppliers(ctx, f, nil)
}

// listSuppliers 列表内部统一实现：groups 为空即「无高级检索」（两条入口共用同一排序/分页/计数口径）。
//
// 计数与列表**分两次查询**：ORDER BY 里可能带 channel_count 的相关子查询，而聚合查询（COUNT(*)）
// 在没有 GROUP BY 时不允许 ORDER BY 引用外层列——把 Order 只挂在列表查询上。
func (s *Store) listSuppliers(ctx context.Context, f SupplierFilter, groups []whereGroup) (SupplierListResult, error) {
	result := SupplierListResult{Rows: []Supplier{}, Counts: map[int64]int64{}}
	if err := s.ready(); err != nil {
		return result, err
	}
	countQuery, err := applyWhereGroups(s.supplierQuery(ctx, f), supplierWhereFields, groups)
	if err != nil {
		return result, err
	}
	if err := countQuery.Count(&result.Total).Error; err != nil {
		return result, err
	}
	listQuery, err := applyWhereGroups(s.supplierQuery(ctx, f), supplierWhereFields, groups)
	if err != nil {
		return result, err
	}
	offset, limit := normalizePage(f.Page, f.Limit, DefaultSupplierListLimit)
	order := orderClause(f.Sort, f.Order, supplierSortColumns(), `"id" DESC`)
	if err := listQuery.Order(order).Offset(offset).Limit(limit).Find(&result.Rows).Error; err != nil {
		return result, err
	}
	ids := make([]int64, 0, len(result.Rows))
	for _, row := range result.Rows {
		ids = append(ids, row.ID)
	}
	counts, err := s.SupplierChannelCounts(ctx, ids)
	if err != nil {
		return result, err
	}
	result.Counts = counts
	return result, nil
}

// SupplierChannelCounts 批量取「厂商 ID → 渠道数」。
//
// 口径与源 Supplier::index 的 `ChannelModel::where('supplier_id', $row->id)->count()` 逐字一致：
// count(*) where supplier_id = 该厂商；这里用一条 GROUP BY 覆盖整页，避免 N+1。
func (s *Store) SupplierChannelCounts(ctx context.Context, supplierIDs []int64) (map[int64]int64, error) {
	counts := make(map[int64]int64, len(supplierIDs))
	if err := s.ready(); err != nil {
		return counts, err
	}
	if len(supplierIDs) == 0 {
		return counts, nil
	}
	rows := make([]struct {
		SupplierID int64 `gorm:"column:supplier_id"`
		Channels   int64 `gorm:"column:channels"`
	}, 0, len(supplierIDs))
	if err := s.db.WithContext(ctx).Table(table("plugin_ai_channel")).
		Select(`"supplier_id" AS supplier_id, COUNT(*) AS channels`).
		Where(`"supplier_id" IN ?`, supplierIDs).
		Group("supplier_id").
		Scan(&rows).Error; err != nil {
		return counts, err
	}
	for _, row := range rows {
		counts[row.SupplierID] = row.Channels
	}
	return counts, nil
}

// GetSupplier 按 ID 取厂商（不存在返回 gorm.ErrRecordNotFound；api_key 原样返回，脱敏在 handler）。
func (s *Store) GetSupplier(ctx context.Context, id int64) (*Supplier, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	var row Supplier
	if err := s.db.WithContext(ctx).Where(`"id" = ?`, id).First(&row).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

// SupplierExists 存在性复核（PG 在「值未变化」时也可能报 RowsAffected=0，不能只凭它判「不存在」）。
func (s *Store) SupplierExists(ctx context.Context, id int64) bool {
	_, err := s.GetSupplier(ctx, id)
	return err == nil
}

// CreateSupplier 新增厂商（调用方负责必填与闭集校验；本方法只补时间戳）。
func (s *Store) CreateSupplier(ctx context.Context, row *Supplier) error {
	if err := s.ready(); err != nil {
		return err
	}
	if row == nil {
		return errors.New("插件 ai：厂商为空")
	}
	at := nowUnix()
	if row.CreateTime == 0 {
		row.CreateTime = at
	}
	if row.UpdateTime == 0 {
		row.UpdateTime = at
	}
	return s.db.WithContext(ctx).Create(row).Error
}

// UpdateSupplier 更新厂商可编辑列（返回受影响行数；0 = 不存在或值未变化）。
func (s *Store) UpdateSupplier(ctx context.Context, id int64, patch SupplierPatch) (int64, error) {
	if err := s.ready(); err != nil {
		return 0, err
	}
	updates := map[string]any{"update_time": nowUnix()}
	if patch.Name != nil {
		updates["name"] = *patch.Name
	}
	if patch.Vendor != nil {
		updates["vendor"] = *patch.Vendor
	}
	if patch.BaseURL != nil {
		updates["base_url"] = *patch.BaseURL
	}
	if patch.APIKey != nil {
		updates["api_key"] = *patch.APIKey
	}
	if patch.Driver != nil {
		updates["driver"] = *patch.Driver
	}
	if patch.Timeout != nil {
		updates["timeout"] = *patch.Timeout
	}
	if patch.Status != nil {
		updates["status"] = *patch.Status
	}
	if patch.Remark != nil {
		updates["remark"] = *patch.Remark
	}
	res := s.db.WithContext(ctx).Model(&Supplier{}).Where(`"id" = ?`, id).Updates(updates)
	return res.RowsAffected, res.Error
}

// DeleteSuppliers 批量删除厂商，**带删除保护**：本批里任一厂商仍被渠道引用（supplier_id 指向它）
// ⇒ 整个事务拒绝并回滚（不出现「删了一半」的中间态）；返回受影响行数。
//
// 判定与删除在同一事务内完成（引用读数与主行删除之间不留竞态窗口）；命中的错误类型是
// *SupplierInUseError（errors.Is(err, ErrSupplierInUse) 可判定），其文案即给调用方的行动指引。
func (s *Store) DeleteSuppliers(ctx context.Context, ids []int64) (int64, error) {
	if err := s.ready(); err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, nil
	}
	var deleted int64
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		refs, err := supplierReferencesWith(tx, ids)
		if err != nil {
			return err
		}
		if err := supplierDeleteRefusal(refs); err != nil {
			return err
		}
		res := tx.Where(`"id" IN ?`, ids).Delete(&Supplier{})
		deleted = res.RowsAffected
		return res.Error
	})
	if err != nil {
		return 0, err
	}
	return deleted, nil
}

// supplierReferencesWith 取「本批厂商」的引用读数（渠道数 + 厂商名，供拒绝文案使用）。
//
// 只查被引用的那些行（渠道数 >0），未命中引用的厂商连名字都不必取——判定只看渠道数。
func supplierReferencesWith(tx *gorm.DB, ids []int64) ([]supplierReference, error) {
	countRows := make([]struct {
		SupplierID int64 `gorm:"column:supplier_id"`
		Channels   int64 `gorm:"column:channels"`
	}, 0, len(ids))
	if err := tx.Table(table("plugin_ai_channel")).
		Select(`"supplier_id" AS supplier_id, COUNT(*) AS channels`).
		Where(`"supplier_id" IN ?`, ids).
		Group("supplier_id").
		Scan(&countRows).Error; err != nil {
		return nil, err
	}
	if len(countRows) == 0 {
		return nil, nil
	}
	referencedIDs := make([]int64, 0, len(countRows))
	for _, row := range countRows {
		referencedIDs = append(referencedIDs, row.SupplierID)
	}
	named := make([]struct {
		ID   int64  `gorm:"column:id"`
		Name string `gorm:"column:name"`
	}, 0, len(referencedIDs))
	if err := tx.Model(&Supplier{}).Select(`"id" AS id, "name" AS name`).
		Where(`"id" IN ?`, referencedIDs).Scan(&named).Error; err != nil {
		return nil, err
	}
	names := make(map[int64]string, len(named))
	for _, row := range named {
		names[row.ID] = row.Name
	}
	refs := make([]supplierReference, 0, len(countRows))
	for _, row := range countRows {
		refs = append(refs, supplierReference{ID: row.SupplierID, Name: names[row.SupplierID], Channels: row.Channels})
	}
	return refs, nil
}
