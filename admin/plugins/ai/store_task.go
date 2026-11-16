// plugin: ai v0.2.0
// store_task.go 是任务账本的全部 SQL 原语（机械 SQL，零业务语义）。
//
// 并发正确性全靠 SQL 与索引（表结构见 migrations/5_task.up.sql）：
//   - 幂等提交 = 部分唯一索引 `uk_plugin_ai_task_request_id`（`(request_id) WHERE request_id <> ”`）；
//     撞索引即「同 request_id 的任务已存在」，回既有行（created=false）——不是先查后插；
//   - 领取 = 子查询 `FOR UPDATE SKIP LOCKED` + `locked_until` 租约（可领取 = pending，
//     或 processing 且租约已过期 ⇒ 崩溃/被抢占的重启续跑路径），同事务内紧跟 UPDATE 置 processing；
//   - 进度 = `GREATEST(progress_done, ?)`（done 单调，防续跑/重试回退）；
//   - 时间口径：locked_until 用**库时钟**（now()）与库侧比较；update_time 等 bigint 列沿用
//     本插件既有口径（Go 侧 nowUnix()），两者不参与同一条判定，不会互相干扰。
package ai

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// taskRequestIndex 幂等判据的索引名（撞它 = 同 request_id 的任务已存在；见迁移 5）。
// 物理名带表前缀，判定用「剥掉前缀后的逻辑名」做子串匹配（前缀不足时逻辑名仍完整出现在报错里）。
const taskRequestIndex = "uk_plugin_ai_task_request_id"

// DefaultTaskListLimit 任务列表默认每页条数（与调用日志同档；clamp 1~100 见 normalizePage）。
const DefaultTaskListLimit = 20

// taskTable 物理表名（与 Task.TableName() 同源：table() 单点拼前缀 + 标识符复核）。
func (s *Store) taskTable() string { return table("plugin_ai_task") }

// TaskSubmitInput 是提交一条任务的入参（Payload 已由 handler 编码成 jsonb 文本）。
type TaskSubmitInput struct {
	TaskType      string
	Payload       datatypes.JSON
	RequestID     string
	BizType       string
	Source        string
	ProgressTotal int
}

// SubmitTask 提交一条任务（返回账本行与「是否新建」；同 request_id 已存在时 created=false 并回既有行）。
//
// request_id 为空时不参与幂等判据（部分唯一索引排除空串 ⇒ 并发空提交各成一行，不会互撞）；
// 提交接口（handler_task.go）会**强制生成非空 request_id**，空值语义只留给内部直插与库级用例。
func (s *Store) SubmitTask(ctx context.Context, in TaskSubmitInput) (*Task, bool, error) {
	if err := s.ready(); err != nil {
		return nil, false, err
	}
	taskType := strings.TrimSpace(in.TaskType)
	if taskType != TaskTypeChat && taskType != TaskTypeEmbedding {
		return nil, false, fmt.Errorf("任务类型 %q 不在闭集内（允许：%s|%s）", in.TaskType, TaskTypeChat, TaskTypeEmbedding)
	}
	if len(in.Payload) == 0 {
		return nil, false, errors.New("任务载荷不能为空")
	}
	at := nowUnix()
	row := Task{
		TaskType:      taskType,
		Status:        TaskStatusPending,
		Payload:       in.Payload,
		BizType:       truncateRunes(strings.TrimSpace(in.BizType), 30),
		Source:        truncateRunes(strings.TrimSpace(in.Source), 30),
		RequestID:     strings.TrimSpace(in.RequestID),
		ProgressTotal: in.ProgressTotal,
		CreateTime:    at,
		UpdateTime:    at,
	}
	if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
		// 同 request_id 的任务已在飞 ⇒ 回既有行（幂等语义；不是错误）
		if row.RequestID != "" && isTaskRequestIDConflict(err) {
			existing, findErr := s.taskByRequestID(ctx, row.RequestID)
			if findErr != nil {
				return nil, false, findErr
			}
			return existing, false, nil
		}
		return nil, false, err
	}
	return &row, true, nil
}

// taskByRequestID 取同 request_id 的任务（提交撞索引后的回读；取最早创建的一条）。
func (s *Store) taskByRequestID(ctx context.Context, requestID string) (*Task, error) {
	row, err := gorm.G[Task](s.db).Where(`"request_id" = ?`, requestID).Order(`"id" ASC`).First(ctx)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrTaskNotFound
		}
		return nil, err
	}
	return &row, nil
}

// GetTask 按 id 取任务行（不存在返回 ErrTaskNotFound）。
func (s *Store) GetTask(ctx context.Context, id uint64) (*Task, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	row, err := gorm.G[Task](s.db).Where(`"id" = ?`, id).First(ctx)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrTaskNotFound
		}
		return nil, err
	}
	return &row, nil
}

// isTaskRequestIDConflict 幂等索引冲突判定（GORM 未开 TranslateError ⇒ 按 SQLSTATE / 约束标识判）。
//
// 判定必须窄：PG 的 duplicate key 报错文案里恒含 "key"，若按该词判定，本表任何唯一冲突
// （主键冲突、将来新增唯一约束）都会被误判成「同 request_id 已在飞」，静默回一条不相干的任务。
// 故只认幂等索引名（口径与 plugins/kb/service/jobs/store.go 的 isActiveKeyConflict 一致）；
// SQLite（modernc 驱动）报 `UNIQUE constraint failed: plugin_ai_task.request_id`——只认
// 「本表名 + request_id 列 + UNIQUE constraint failed」三者同时命中，同样不误伤主键冲突。
func isTaskRequestIDConflict(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	lower := strings.ToLower(err.Error())
	if strings.Contains(lower, "unique constraint failed") {
		return strings.Contains(lower, "request_id") && strings.Contains(lower, table("plugin_ai_task"))
	}
	if !strings.Contains(err.Error(), "23505") && !strings.Contains(err.Error(), "duplicate key") {
		return false
	}
	return strings.Contains(lower, taskRequestIndex)
}

// ===== 列表 =====

// taskSortColumns 任务列表的排序白名单。
var taskSortColumns = map[string]string{
	"id":             `"id"`,
	"task_type":      `"task_type"`,
	"status":         `"status"`,
	"attempt":        `"attempt"`,
	"progress_done":  `"progress_done"`,
	"progress_total": `"progress_total"`,
	"create_time":    `"create_time"`,
	"update_time":    `"update_time"`,
}

// TaskFilter 任务列表筛选（等值 status/task_type；快搜 keyword 命中 model/request_id/source/biz_type）。
type TaskFilter struct {
	Page     int
	Limit    int
	Status   string
	TaskType string
	Keyword  string
	Sort     string
	Order    string
}

// TaskPage 任务分页结果。
type TaskPage struct {
	Rows  []Task
	Total int64
}

// taskQuery 任务列表的共同筛选。
func (s *Store) taskQuery(ctx context.Context, f TaskFilter) *gorm.DB {
	q := s.db.WithContext(ctx).Model(&Task{})
	if f.Status != "" {
		q = q.Where(`"status" = ?`, f.Status)
	}
	if f.TaskType != "" {
		q = q.Where(`"task_type" = ?`, f.TaskType)
	}
	if keyword := strings.TrimSpace(f.Keyword); keyword != "" {
		like := "%" + keyword + "%"
		if isSQLite(s.db) {
			q = q.Where(`("request_id" LIKE ? OR "source" LIKE ? OR "biz_type" LIKE ? OR json_extract("payload", '$.model') LIKE ?)`,
				like, like, like, like)
		} else {
			q = q.Where(`("request_id" ILIKE ? OR "source" ILIKE ? OR "biz_type" ILIKE ? OR "payload"->>'model' ILIKE ?)`,
				like, like, like, like)
		}
	}
	return q
}

// ListTasks 任务分页（默认 id DESC、limit 20）。
func (s *Store) ListTasks(ctx context.Context, f TaskFilter) (TaskPage, error) {
	result := TaskPage{Rows: []Task{}}
	if err := s.ready(); err != nil {
		return result, err
	}
	if err := s.taskQuery(ctx, f).Count(&result.Total).Error; err != nil {
		return result, err
	}
	offset, limit := normalizePage(f.Page, f.Limit, DefaultTaskListLimit)
	order := orderClause(f.Sort, f.Order, taskSortColumns, `"id" DESC`)
	if err := s.taskQuery(ctx, f).Order(order).Offset(offset).Limit(limit).Find(&result.Rows).Error; err != nil {
		return result, err
	}
	return result, nil
}

// TaskUsageRows 取任务名下的 usage 行（累计口径：token/成本/成功失败次数都由此汇总）。
// ids 为空返回空切片（不查库）。
func (s *Store) TaskUsageRows(ctx context.Context, ids []string) ([]Usage, error) {
	rows := []Usage{}
	if err := s.ready(); err != nil {
		return rows, err
	}
	if len(ids) == 0 {
		return rows, nil
	}
	if err := s.db.WithContext(ctx).Model(&Usage{}).
		Where(`"request_id" IN ?`, ids).
		Order(`"request_id" ASC, "attempt" ASC`).
		Find(&rows).Error; err != nil {
		return rows, err
	}
	return rows, nil
}

// ===== 领取 / 续租 / 终态（worker 的原语）=====

// claimedTask 领取结果行（领取 SQL 的返回面：worker 执行所需的全部字段）。
type claimedTask struct {
	ID            uint64         `gorm:"column:id"`
	TaskType      string         `gorm:"column:task_type"`
	Status        string         `gorm:"column:status"`
	Payload       datatypes.JSON `gorm:"column:payload"`
	RequestID     string         `gorm:"column:request_id"`
	Source        string         `gorm:"column:source"`
	BizType       string         `gorm:"column:biz_type"`
	Attempt       int            `gorm:"column:attempt"`
	ProgressDone  int            `gorm:"column:progress_done"`
	ProgressTotal int            `gorm:"column:progress_total"`
}

// claimTask 领取一条可执行任务（事务内两步：SELECT ... FOR UPDATE SKIP LOCKED 取一条 → UPDATE 置 processing）。
//
// 可领取 = ①`pending`；②`processing` 且租约已过期（NULL 或 < now()）——崩溃/被抢占的重启续跑路径。
// 每次领取 `attempt = attempt + 1`（attempt 累加的唯一实现处就是这条 UPDATE）。
// 无活可领 ⇒ 返回 (nil, nil)。
//
// 为什么分两步而不是 `UPDATE ... RETURNING`：与 plugins/kb/service/jobs/store.go:147-155 同因——
// 经 GORM 的 Raw+Scan 拿不回返回行，会静默变成「永远领不到活」；两步在**同一事务**内语义等价
// （FOR UPDATE SKIP LOCKED 已锁行，UPDATE 紧跟其后）。
func (s *Store) claimTask(ctx context.Context, lease time.Duration) (*claimedTask, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	if lease <= 0 {
		lease = DefaultTaskLease
	}
	tbl := quoteIdent(s.taskTable())

	var out *claimedTask
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		rows := make([]claimedTask, 0, 1)
		if isSQLite(s.db) {
			nowTime := time.Now().UTC()
			leaseUntil := nowTime.Add(lease)
			if err := tx.Raw("SELECT id, task_type, status, payload, request_id, source, biz_type,"+
				" attempt, progress_done, progress_total FROM "+tbl+
				" WHERE status = ? OR (status = ? AND (locked_until IS NULL OR locked_until < ?))"+
				" ORDER BY id ASC LIMIT 1",
				TaskStatusPending, TaskStatusProcessing, nowTime).Scan(&rows).Error; err != nil {
				return err
			}
			if len(rows) == 0 {
				return nil
			}
			row := rows[0]
			if err := tx.Exec("UPDATE "+tbl+" SET status = ?, attempt = attempt + 1,"+
				" locked_until = ?, update_time = ? WHERE id = ?",
				TaskStatusProcessing, leaseUntil, nowUnix(), row.ID).Error; err != nil {
				return err
			}
			row.Status = TaskStatusProcessing
			row.Attempt++
			out = &row
			return nil
		}
		if err := tx.Raw("SELECT id, task_type, status, payload, request_id, source, biz_type,"+
			" attempt, progress_done, progress_total FROM "+tbl+
			" WHERE status = ? OR (status = ? AND (locked_until IS NULL OR locked_until < now()))"+
			" ORDER BY id ASC FOR UPDATE SKIP LOCKED LIMIT 1",
			TaskStatusPending, TaskStatusProcessing).Scan(&rows).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		row := rows[0]
		if err := tx.Exec("UPDATE "+tbl+" SET status = ?, attempt = attempt + 1,"+
			" locked_until = now() + (? * interval '1 second'), update_time = ? WHERE id = ?",
			TaskStatusProcessing, lease.Seconds(), nowUnix(), row.ID).Error; err != nil {
			return err
		}
		row.Status = TaskStatusProcessing
		row.Attempt++ // 回显「本次是第几次尝试（含本次）」
		out = &row
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// renewTaskLease 续租（只对仍 processing 的行生效；返回受影响行数 0 = 租约已丢，调用方应停止协作）。
func (s *Store) renewTaskLease(ctx context.Context, id uint64, lease time.Duration) (int64, error) {
	if err := s.ready(); err != nil {
		return 0, err
	}
	if lease <= 0 {
		lease = DefaultTaskLease
	}
	var lockedUntil any = gorm.Expr("now() + (? * interval '1 second')", lease.Seconds())
	if isSQLite(s.db) {
		lockedUntil = time.Now().UTC().Add(lease)
	}
	tx := s.db.WithContext(ctx).Table(s.taskTable()).
		Where(`"id" = ? AND "status" = ?`, id, TaskStatusProcessing).
		Updates(map[string]any{
			"locked_until": lockedUntil,
			"update_time":  nowUnix(),
		})
	return tx.RowsAffected, tx.Error
}

// updateTaskProgress 进度回写（done 单调：取 GREATEST/MAX，防重试/续跑回退；total 只在 > 0 时覆盖）。
func (s *Store) updateTaskProgress(ctx context.Context, id uint64, done, total int) error {
	if err := s.ready(); err != nil {
		return err
	}
	if id == 0 {
		return errors.New("插件 ai：任务 ID 不能为空")
	}
	doneExpr := gorm.Expr(`GREATEST("progress_done", ?)`, done)
	if isSQLite(s.db) {
		doneExpr = gorm.Expr(`MAX("progress_done", ?)`, done)
	}
	values := map[string]any{
		"progress_done": doneExpr,
		"update_time":   nowUnix(),
	}
	if total > 0 {
		values["progress_total"] = total
	}
	return s.db.WithContext(ctx).Table(s.taskTable()).Where(`"id" = ?`, id).Updates(values).Error
}

// ErrTaskLeaseLost 表示终态写入时该行**已不归本次尝试持有**（status/attempt 守卫未命中）：
// 租约已被重领者接管或任务已进入其他终态。调用方不得把这种返回当成功，也不得再做任何收口动作
// （终态与派生记账归重领者的那一轮）。
var ErrTaskLeaseLost = errors.New("插件 ai：任务租约已丢失（该行已由更晚的持有者接管）")

// markTaskSuccess 完成（落结果 jsonb + 释放租约 + 进度收口到总数）。
//
// 写侧守卫：`status='processing' AND attempt=?`（attempt = 本次领取的尝试序号）——租约丢了就
// 不该再写（否则 stale 持有者会覆盖重领者已经写下的终态，实测可复现：failed 被后到的 success 盖掉）。
func (s *Store) markTaskSuccess(ctx context.Context, id uint64, attempt int, result datatypes.JSON) error {
	if err := s.ready(); err != nil {
		return err
	}
	doneExpr := gorm.Expr(`GREATEST("progress_done", "progress_total")`)
	if isSQLite(s.db) {
		doneExpr = gorm.Expr(`MAX("progress_done", "progress_total")`)
	}
	values := map[string]any{
		"status":        TaskStatusSuccess,
		"locked_until":  nil,
		"progress_done": doneExpr,
		"update_time":   nowUnix(),
	}
	if len(result) > 0 {
		// 显式转型：模型字段走 datatypes.JSON，map 路径在 PG 带 ::jsonb，SQLite 直接写文本
		if isSQLite(s.db) {
			values["result"] = string(result)
		} else {
			values["result"] = gorm.Expr("?::jsonb", string(result))
		}
	}
	return s.updateOwnedTask(ctx, id, attempt, values)
}

// markTaskFailed 失败（终态；error 记最近原因、按列宽截断；结果列保持为空）。
func (s *Store) markTaskFailed(ctx context.Context, id uint64, attempt int, reason string) error {
	if err := s.ready(); err != nil {
		return err
	}
	return s.updateOwnedTask(ctx, id, attempt, map[string]any{
		"status":       TaskStatusFailed,
		"locked_until": nil,
		"error":        truncateRunes(reason, lastErrorMaxLen),
		"update_time":  nowUnix(),
	})
}

// requeueTask 退回 pending（尝试次数未耗尽的下一轮重试；attempt 已在领取时累加，此处只改状态与错误）。
//
// 无退避：本表没有 run_at 列（迁移 5 的既定列集），因此重试在**下一轮**（默认 5s 内）发生。
// 取舍如实登记：源侧 redis-queue 有 retry_seconds 退避，本包按固定轮询间隔重试（属 v1 差异）。
func (s *Store) requeueTask(ctx context.Context, id uint64, attempt int, reason string) error {
	if err := s.ready(); err != nil {
		return err
	}
	return s.updateOwnedTask(ctx, id, attempt, map[string]any{
		"status":       TaskStatusPending,
		"locked_until": nil,
		"error":        truncateRunes(reason, lastErrorMaxLen),
		"update_time":  nowUnix(),
	})
}

// updateOwnedTask 是三个终态写口的共同实现：只在「仍是本次持有」时落库。
//
// 判据 = `id=? AND status='processing' AND attempt=?`（attempt 由 claimTask 在领取事务里 +1 并回显）；
// 影响行数 0 ⇒ ErrTaskLeaseLost（调用方只记日志，不得当成功、不得重试写）。
func (s *Store) updateOwnedTask(ctx context.Context, id uint64, attempt int, values map[string]any) error {
	if id == 0 {
		return errors.New("插件 ai：任务 ID 不能为空")
	}
	tx := s.db.WithContext(ctx).Table(s.taskTable()).
		Where(`"id" = ? AND "status" = ? AND "attempt" = ?`, id, TaskStatusProcessing, attempt).
		Updates(values)
	if tx.Error != nil {
		return tx.Error
	}
	if tx.RowsAffected == 0 {
		return ErrTaskLeaseLost
	}
	return nil
}

// resetStaleTaskLeases 把租约过期（或从未拿到租约）的 processing 行复位为 pending（启动时调用一次）。
//
// 与领取 SQL 的救援路径**不冲突而是互补**：领取 SQL 本就能重领过期租约的行；这一步是让重启后的
// 台账读数立刻自洽（否则「已停机的进程还挂着一批 processing」会误导运维）。
// 不重置 attempt（attempt 由领取 +1）。
func (s *Store) resetStaleTaskLeases(ctx context.Context) (int64, error) {
	if err := s.ready(); err != nil {
		return 0, err
	}
	query := s.db.WithContext(ctx).Table(s.taskTable()).
		Where(`"status" = ? AND ("locked_until" IS NULL OR "locked_until" < now())`, TaskStatusProcessing)
	if isSQLite(s.db) {
		query = s.db.WithContext(ctx).Table(s.taskTable()).
			Where(`"status" = ? AND ("locked_until" IS NULL OR "locked_until" < ?)`, TaskStatusProcessing, time.Now().UTC())
	}
	tx := query.Updates(map[string]any{
		"status":       TaskStatusPending,
		"locked_until": nil,
		"update_time":  nowUnix(),
	})
	return tx.RowsAffected, tx.Error
}
