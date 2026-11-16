// plugin: kb v1.0.0
// 作业 worker：领取 → 执行 → 续租/重试 的循环。
//
// 宿主没有插件生命周期钩子（注册表只收「名/版本/形态/挂载点」），所以**启停语义由本处自管**：
//   - 启动点 = 插件 mount（路由装配期，此时宿主已完成 DB 初始化与预检）；sync.Once 保护只起一次；
//   - 停用即停跑 = 每轮直读 `admin_plugins.status`，非 `enabled` 就不领活（pending 行原样留着）；
//   - 每日清理 = 首轮与跨日把 `kb:prune:<YYYY-MM-DD>` 键入队（替代宿主启动段里被删掉的那次入队），
//     顺带收敛终态账本行（作业表增长无界）。
//
// 多进程/重启语义：租约到期由其它进程或重启后的进程重领，attempt 累加；产物键保证重跑幂等。
package jobs

import (
	"context"
	"errors"
	"log"
	"strings"
	"sync"
	"time"

	hostmodel "github.com/ai-go-hub/ai-go-admin/internal/model"

	"gorm.io/gorm"
)

// PluginName 台账里的插件名（运行时闸与 worker 自查共用同一行）
const PluginName = "kb"

// StatusEnabled 台账启用态（与宿主 <prefix>admin_plugins.status 的闭集一致）
const StatusEnabled = "enabled"

// terminalRetention 终态账本行保留期（与检索日志保留期解耦：账本只作运维读数）
const terminalRetention = 7 * 24 * time.Hour

// pruneKind 每日清理作业的 kind（由检索包注册；未注册时 worker 不入队——避免留下永不领取的 pending 行）
const pruneKind = "kb:prune"

var (
	workerOnce sync.Once
	warnedOnce sync.Once
	lastPrune  string
	pruneMu    sync.Mutex
)

// StartWorkerOnce 启动 worker（幂等：进程内只起一次；由插件 mount 调用）
func StartWorkerOnce() {
	workerOnce.Do(func() {
		go loop(context.Background())
	})
}

// loop worker 主循环（每 tick 一轮；DB 不可用/表缺失时只提示一次，不刷屏）
func loop(ctx context.Context) {
	ticker := time.NewTicker(DefaultPollInterval)
	defer ticker.Stop()
	for {
		if _, err := RunOnce(ctx); err != nil {
			if !errors.Is(err, errNoDatabase) {
				warnedOnce.Do(func() {
					log.Printf("[kb] 作业 worker 本轮未执行（后续同类错误不再重复记录）: %v", err)
				})
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// errNoDatabase 数据库未初始化（单测/工具进程）——worker 静默跳过，不算错误
var errNoDatabase = errors.New("数据库未初始化")

// RunOnce 执行一轮：① 台账启用自查 ② 到期作业领取与执行 ③ 每日清理。
// 返回 claimed=true 表示本轮执行了一条作业（用例据此断言「停用即停跑」与领取语义）。
//
// 顺序纪律：**启用自查在最前**——非 enabled 时本轮零副作用（不领活、不做每日入队、不清账本终态行）。
// 停用中的插件不该继续写任何表：留 pending 行只是「不推进」，而每日入队/终态清理是**新增写**，
// 会让停用态的行数继续变化（也违背「停用即停跑」的运维直觉）。
func RunOnce(ctx context.Context) (claimed bool, err error) {
	store := NewStore()
	if store.DB() == nil {
		return false, errNoDatabase
	}

	enabled, err := pluginEnabled(ctx, store.DB())
	if err != nil {
		return false, err
	}
	if !enabled {
		return false, nil // 停用即停跑：本轮不领活也不写库，pending 行原样留在原状态
	}

	if err := ensurePruneQueued(ctx, store); err != nil {
		return false, err
	}

	row, err := store.claim(ctx, Registered(), DefaultLease)
	if err != nil {
		return false, err
	}
	if row == nil {
		return false, nil
	}
	return true, runClaimed(ctx, store, row, DefaultLease)
}

// pluginEnabled 直读台账状态（非 enabled / 无台账行 / 读失败 ⇒ 视为不可跑）
func pluginEnabled(ctx context.Context, db *gorm.DB) (bool, error) {
	row, err := gorm.G[hostmodel.Plugin](db).Select("status").Where("name = ?", PluginName).First(ctx)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, err
	}
	return row.Status == StatusEnabled, nil
}

// runClaimed 执行一条已领取作业：起续租协程 → 跑 handler → 落终态/退避重试。
func runClaimed(ctx context.Context, store *Store, row *claimed, lease time.Duration) error {
	handler, ok := handlerOf(row.Kind)
	if !ok {
		// 领取 SQL 已按注册集过滤 ⇒ 此处理论上不可达；真到了就响亮留痕并退回 pending（不吞行）
		return store.requeue(ctx, row.ID, DefaultBackoffBase, "kind 未注册: "+row.Kind)
	}

	job := &Job{
		ID:      row.ID,
		Kind:    row.Kind,
		Key:     row.Key,
		Attempt: row.Attempt,
		Total:   row.ProgressTotal,
		Done:    row.ProgressDone,
		store:   store,
	}
	if row.Params != nil {
		job.Params = []byte(*row.Params)
	}

	execCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go renewLoop(execCtx, store, row.ID, lease)

	runErr := runHandler(execCtx, handler, job)
	cancel() // 停止续租协程

	if runErr == nil {
		return store.markDone(ctx, row.ID, job.result)
	}

	reason := runErr.Error()
	maxAttempts := row.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = DefaultMaxAttempts
	}
	if row.Attempt >= maxAttempts {
		return store.markFailed(ctx, row.ID, reason)
	}
	return store.requeue(ctx, row.ID, backoff(row.Attempt), reason)
}

// runHandler 执行处理器并把 panic 收成错误（作业面不得因单个 handler 崩溃拖垮进程）
func runHandler(ctx context.Context, handler Handler, job *Job) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = errors.New("作业处理器 panic: " + strings.TrimSpace(toString(r)))
		}
	}()
	return handler.Run(ctx, job)
}

// renewLoop 续租循环（Lease/3）；租约已丢（进程被抢占/作业被终态）即停止
func renewLoop(ctx context.Context, store *Store, id uint64, lease time.Duration) {
	ticker := time.NewTicker(leaseRenewInterval(lease))
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n, err := store.renewLease(ctx, id, lease)
			if err != nil || n == 0 {
				return
			}
		}
	}
}

// ensurePruneQueued 每日清理（首轮 + 跨日各一次）：键 `kb:prune:<YYYY-MM-DD>`
// （**与检索包 pruneKeyFor 的键口径逐字一致**——两处都写该格式，改一处必须改另一处；
// 同键单飞由账本的部分唯一索引收口，重复入队只留一行）；顺带收敛终态账本行。
// 仅当 kind 已注册时才入队（否则会留下永不领取的 pending 行）。
// 注：本函数只在 **enabled** 时被调用（RunOnce 的启用自查在最前）——停用态本轮零写库。
func ensurePruneQueued(ctx context.Context, store *Store) error {
	today := time.Now().Format("2006-01-02")
	pruneMu.Lock()
	already := lastPrune == today
	pruneMu.Unlock()
	if already {
		return nil
	}
	if _, ok := handlerOf(pruneKind); !ok {
		return nil
	}
	if _, _, err := store.Enqueue(ctx, EnqueueInput{Kind: pruneKind, Key: pruneKind + ":" + today}); err != nil {
		return err
	}
	pruneMu.Lock()
	lastPrune = today
	pruneMu.Unlock()

	if _, err := store.purgeTerminal(ctx, terminalRetention); err != nil {
		return err
	}
	return nil
}

// toString panic 值转文本
func toString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case error:
		return t.Error()
	default:
		return "未知类型"
	}
}
