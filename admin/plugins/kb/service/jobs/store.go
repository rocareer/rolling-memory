// plugin: kb v1.0.0
// 作业账本的存储面（机械 SQL，零业务语义）：入队（单飞）/ 领取（SKIP LOCKED + 租约）/
// 续租 / 进度 / 终态标记 / 每日清理。
//
// 并发正确性全靠 SQL 与索引（表结构见迁移 6_jobs）：
//   - 单飞 = 部分唯一索引 `uk_plugin_kb_jobs_active_key`（`(key) WHERE status IN ('pending','running')`）；
//     入队撞唯一冲突即「同键活跃行已在飞」，回既有行（created=false）——不是先查后插；
//   - 领取 = 子查询 `FOR UPDATE SKIP LOCKED` + `locked_until` 租约（含 running 行租约过期的救援）；
//   - 进度 = `GREATEST(progress_done, ?)`（done 单调）。
package jobs

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/ai-go-hub/ai-go-admin/internal/infra/database"
	"github.com/ai-go-hub/ai-go-admin/plugins/kb/model"

	"gorm.io/gorm"
)

// activeKeyIndex 单飞判据的索引名（撞它 = 同键活跃行已在飞；见迁移 6_jobs）
const activeKeyIndex = "uk_plugin_kb_jobs_active_key"

// Store 账本操作口（生产实现 = PG；测试可注入 DB）
type Store struct {
	db *gorm.DB
}

// NewStore 创建 Store（默认走全局 db 实例）
func NewStore() *Store { return &Store{} }

// WithDB 以链式方式自定义 DB 实例（测试/事务场景）
func (s *Store) WithDB(db *gorm.DB) *Store {
	s.db = db
	return s
}

// DB 当前 DB 实例（自定义优先，否则全局）
func (s *Store) DB() *gorm.DB {
	if s.db != nil {
		return s.db
	}
	return database.DB()
}

// Table 账本表名（与模型 TableName() 同源；表前缀由 GORM NamingStrategy 注入）
func (s *Store) Table() string {
	db := s.DB()
	if db == nil {
		return model.Job{}.TableName()
	}
	stmt := &gorm.Statement{DB: db}
	if err := stmt.Parse(&model.Job{}); err == nil && stmt.Schema != nil && stmt.Schema.Table != "" {
		return stmt.Schema.Table
	}
	return model.Job{}.TableName()
}

// Enqueue 入队（返回账本行与「是否新建」；同键活跃行存在时 created=false 并回既有行）
func (s *Store) Enqueue(ctx context.Context, in EnqueueInput) (*model.Job, bool, error) {
	if err := ensureTable(ctx, s); err != nil {
		return nil, false, err
	}
	kind := strings.TrimSpace(in.Kind)
	if kind == "" {
		return nil, false, ErrInvalidInput
	}
	key := strings.TrimSpace(in.Key)
	params, err := encodeParams(in.Params)
	if err != nil {
		return nil, false, err
	}
	// RunAt 未指定 ⇒ 留零值：INSERT 时 GORM 跳过该列、由表缺省 now() 落地（**单一时间源 = 库时钟**；
	// 用进程时钟写 run_at 再与库的 now() 比较，会在两者有毫秒级偏差时让「立即执行」的作业领不到）
	maxAttempts := in.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = DefaultMaxAttempts
	}

	row := model.Job{
		Kind:        kind,
		Key:         key,
		Status:      model.JobStatusPending,
		MaxAttempts: maxAttempts,
		RunAt:       in.RunAt,
		Params:      params,
	}
	if err := s.DB().WithContext(ctx).Table(s.Table()).Create(&row).Error; err != nil {
		// 同键活跃行已在飞 ⇒ 回既有行（单飞语义；不是错误）
		if key != "" && isActiveKeyConflict(err) {
			existing, findErr := s.activeByKey(ctx, key)
			if findErr != nil {
				return nil, false, findErr
			}
			return existing, false, nil
		}
		return nil, false, err
	}
	return &row, true, nil
}

// activeByKey 取同键活跃行（入队撞索引后的回读；取最早创建的一条）
func (s *Store) activeByKey(ctx context.Context, key string) (*model.Job, error) {
	row, err := gorm.G[model.Job](s.DB()).
		Where("key = ? AND status IN ?", key, model.JobActiveStatuses).
		Order("id ASC").
		First(ctx)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &row, nil
}

// Get 按 id 取账本行
func (s *Store) Get(ctx context.Context, id uint64) (*model.Job, error) {
	if err := ensureTable(ctx, s); err != nil {
		return nil, err
	}
	row, err := gorm.G[model.Job](s.DB()).Where("id = ?", id).First(ctx)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &row, nil
}

// claimed 领取结果行（领取 SQL 的 RETURNING 面）
type claimed struct {
	ID            uint64  `gorm:"column:id"`
	Kind          string  `gorm:"column:kind"`
	Key           string  `gorm:"column:key"`
	Params        *string `gorm:"column:params"`
	Attempt       int     `gorm:"column:attempt"`
	MaxAttempts   int     `gorm:"column:max_attempts"`
	ProgressDone  int64   `gorm:"column:progress_done"`
	ProgressTotal int64   `gorm:"column:progress_total"`
}

// claim 领取一条到期作业（事务内两步：SELECT ... FOR UPDATE SKIP LOCKED 取一条 → UPDATE 置 running）。
//
// 可领取 = ①`pending` 且 `run_at <= now()`；②`running` 且租约已过期（崩溃/超时救援）。
// kind 过滤走「本进程已注册 kind 集」：未注册的 kind 留 pending 等有实现的进程认领。
// 无活可领 ⇒ 返回 (nil, nil)。
//
// 为什么分两步而不是 `UPDATE ... RETURNING`：后者经 GORM 的 Raw+Scan 拿不回返回行
// （实测：语句成功但结果集为空，会静默变成「永远领不到活」），而两步在**同一事务**内
// 语义完全等价（FOR UPDATE SKIP LOCKED 已锁行，UPDATE 紧跟其后）。
func (s *Store) claim(ctx context.Context, kinds []string, lease time.Duration) (*claimed, error) {
	if err := ensureTable(ctx, s); err != nil {
		return nil, err
	}
	if len(kinds) == 0 {
		return nil, nil
	}
	table := s.Table()

	var out *claimed
	err := s.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		rows := make([]claimed, 0, 1)
		if err := tx.Raw("SELECT id, kind, key, params, attempt, max_attempts, progress_done, progress_total FROM "+table+
			" WHERE kind IN ? AND ((status = ? AND run_at <= now()) OR (status = ? AND (locked_until IS NULL OR locked_until < now())))"+
			" ORDER BY run_at ASC, id ASC FOR UPDATE SKIP LOCKED LIMIT 1",
			kinds, model.JobStatusPending, model.JobStatusRunning).Scan(&rows).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		row := rows[0]
		// attempt 累加的唯一实现处 = 这条 UPDATE；Go 侧只按同值回显（不做第二处计数）
		if err := tx.Exec("UPDATE "+table+" SET status = ?, attempt = attempt + 1,"+
			" locked_until = now() + (? * interval '1 second'), updated_at = now() WHERE id = ?",
			model.JobStatusRunning, lease.Seconds(), row.ID).Error; err != nil {
			return err
		}
		row.Attempt++ // 回显「本次是第几次尝试（含本次）」
		out = &row
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// renewLease 续租（只对仍 running 的行生效；返回受影响行数 0 = 租约已丢，调用方应停止协作）
func (s *Store) renewLease(ctx context.Context, id uint64, lease time.Duration) (int64, error) {
	tx := s.DB().WithContext(ctx).Table(s.Table()).
		Where("id = ? AND status = ?", id, model.JobStatusRunning).
		Updates(map[string]any{
			"locked_until": gorm.Expr("now() + (? * interval '1 second')", lease.Seconds()),
			"updated_at":   gorm.Expr("now()"),
		})
	return tx.RowsAffected, tx.Error
}

// updateProgress 进度回写（done 单调：取 GREATEST，防续跑回退）
func (s *Store) updateProgress(ctx context.Context, id uint64, done, total int64) error {
	if id == 0 {
		return ErrInvalidInput
	}
	return s.DB().WithContext(ctx).Table(s.Table()).
		Where("id = ?", id).
		Updates(map[string]any{
			"progress_done":  gorm.Expr("GREATEST(progress_done, ?)", done),
			"progress_total": total,
			"updated_at":     gorm.Expr("now()"),
		}).Error
}

// markDone 完成（落结果 + 进度收口到总数，便于读数面显示「已完成」）
func (s *Store) markDone(ctx context.Context, id uint64, result []byte) error {
	values := map[string]any{
		"status":        model.JobStatusDone,
		"locked_until":  nil,
		"progress_done": gorm.Expr("GREATEST(progress_done, progress_total)"),
		"updated_at":    gorm.Expr("now()"),
	}
	if len(result) > 0 {
		text := string(result)
		values["result"] = text
	}
	return s.DB().WithContext(ctx).Table(s.Table()).Where("id = ?", id).Updates(values).Error
}

// markFailed 失败（reason 记原因；重试未耗尽时由调用方重新入 pending 并给 run_at）
func (s *Store) markFailed(ctx context.Context, id uint64, reason string) error {
	return s.DB().WithContext(ctx).Table(s.Table()).Where("id = ?", id).Updates(map[string]any{
		"status":       model.JobStatusFailed,
		"locked_until": nil,
		"reason":       trimReason(reason),
		"updated_at":   gorm.Expr("now()"),
	}).Error
}

// requeue 退避重入 pending（attempt 已在领取时累加；此处只改状态与可执行时间）。
// run_at 用**库时钟**推进（now() + 退避秒数）：与 Enqueue 的缺省值同源，比较口径才一致。
func (s *Store) requeue(ctx context.Context, id uint64, delay time.Duration, reason string) error {
	return s.DB().WithContext(ctx).Table(s.Table()).Where("id = ?", id).Updates(map[string]any{
		"status":       model.JobStatusPending,
		"run_at":       gorm.Expr("now() + (? * interval '1 second')", delay.Seconds()),
		"locked_until": nil,
		"reason":       trimReason(reason),
		"updated_at":   gorm.Expr("now()"),
	}).Error
}

// purgeTerminal 清理终态账本行（作业表增长无界 ⇒ 随每日清理顺带收敛；
// 只动终态、且只动超过保留期的行，pending/running 一律不碰）
func (s *Store) purgeTerminal(ctx context.Context, retention time.Duration) (int64, error) {
	if retention <= 0 {
		return 0, nil
	}
	tx := s.DB().WithContext(ctx).Table(s.Table()).
		Where("status IN ?", []string{model.JobStatusDone, model.JobStatusCanceled, model.JobStatusFailed}).
		Where("updated_at < now() - (? * interval '1 second')", retention.Seconds()).
		Delete(&model.Job{})
	return tx.RowsAffected, tx.Error
}

// isActiveKeyConflict 单飞索引冲突判定（GORM 未开 TranslateError ⇒ 按 SQLSTATE + **索引名**判）。
//
// 判定必须窄：PG 的 `duplicate key value violates unique constraint ...` 文案里**恒含 "key"**，
// 若按该词判定，本表**任何**唯一冲突（将来新增唯一约束、或主键冲突）都会被误判成「同键活跃行已在飞」，
// 静默变成 created=false 并回一条不相干的活跃行。故只认单飞索引名——物理名带表前缀，
// 用「剥掉前缀后的逻辑名」做子串判定（前缀不足时逻辑名仍完整出现在报错里）。
func isActiveKeyConflict(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	msg := err.Error()
	if !strings.Contains(msg, "23505") && !strings.Contains(msg, "duplicate key") {
		return false
	}
	return strings.Contains(strings.ToLower(msg), activeKeyIndex)
}
