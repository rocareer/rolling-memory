// plugin: ai v0.2.0
// task_worker.go 是异步任务的**进程内 worker**：启用自查 → 领取 → 执行 → 落终态 / 退回 pending。
//
// 为什么是「DB 表 + 进程内 worker」：宿主没有队列/定时任务基建（go.mod 无 redis/amqp/asynq，
// internal/ 下无 cron/worker 先例），本插件**不新增依赖** ⇒ 照 plugins/kb/service/jobs/worker.go 的
// 既有范式自管：
//   - 启动点 = 插件 mount（路由装配期，宿主已完成 DB 初始化）+ sync.Once，只起一次；
//     接线见 plugin.go 的 StartTaskWorkerOnce 调用（本包只提供该函数与 loop）；
//   - **每轮先直读 <prefix>admin_plugins.status**：非 enabled 就只**空转**——本轮不领活、不写任何表，
//     pending 行原样留着，等启用后下一轮继续。这是「停用即停跑」的运维直觉（停用的插件不该继续消耗
//     上游额度、也不该继续改台账），与 host.Gate 的运行时闸用同一行、同一判据；读台账失败按
//     「不可跑」处理（fail-closed，只记一行日志），本轮同样不领活；
//   - 领取 = 同事务 SELECT ... FOR UPDATE SKIP LOCKED + UPDATE（多进程互不重复领取；进程被杀时由
//     locked_until 租约过期救援，attempt 累加）；
//   - **执行面 = 与公开面同一个 *Service**（熔断、候选降级、计费与 usage 落库全沿用同步路径的实现）；
//   - 失败：attempt 达到 task_max_attempts 即置 failed + error；否则退回 pending，下一轮重领。
//
// 单实例语义（如实登记）：worker 在进程内 ⇒ 多副本时各副本都会领活（SKIP LOCKED 保证同一条任务
// 不被两个副本同时持有，但**总并发度 = 副本数**）；轮询间隔是进程内各自读取的配置（改设置即时生效）。
package ai

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/ai-go-hub/ai-go-admin/internal/infra/database"
	"github.com/ai-go-hub/ai-go-admin/internal/model"
	"github.com/ai-go-hub/ai-go-admin/internal/plugin/host"

	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// taskExecutor 是执行面端口（真实现 = *Service；单测注入假件即可无库跑）。
//
// 只要这两个方法：worker 不做流式（异步任务没有客户端连接可写），也不需要 Models（探活另走）。
type taskExecutor interface {
	Chat(ctx context.Context, in ChatInput) (*ChatResult, error)
	Embeddings(ctx context.Context, in EmbeddingInput) (*EmbeddingResult, error)
}

// taskLedger 是 worker 依赖的账本端口（*Store 实现；单测注入内存假件，无库可跑）。
// 只列 worker 真正调用的原语——提交/查询/导出由 handler 直接持 *Store 本体。
//
// 三个终态写口带 attempt（= 本次领取的尝试序号）：实现必须**只对仍是本次持有的行**生效
// （status=processing 且 attempt 相等），未命中即返回 ErrTaskLeaseLost——这是「终态归重领者」
// 的写侧守卫，与 execute 的取消传导配套（只靠取消挡不住「续租周期内租约被外力作废」那一档）。
type taskLedger interface {
	claimTask(ctx context.Context, lease time.Duration) (*claimedTask, error)
	renewTaskLease(ctx context.Context, id uint64, lease time.Duration) (int64, error)
	updateTaskProgress(ctx context.Context, id uint64, done, total int) error
	markTaskSuccess(ctx context.Context, id uint64, attempt int, result datatypes.JSON) error
	markTaskFailed(ctx context.Context, id uint64, attempt int, reason string) error
	requeueTask(ctx context.Context, id uint64, attempt int, reason string) error
	resetStaleTaskLeases(ctx context.Context) (int64, error)
}

// errNoTaskDatabase 数据库未初始化（单测/工具进程）：worker 静默跳过，不算错误。
var errNoTaskDatabase = errors.New("插件 ai：任务 worker 需要数据库")

// taskPermanentError 标记「重试也不会好」的失败（载荷损坏/任务类型未知）：直接置 failed，
// 不白烧 task_max_attempts 次上游额度。
type taskPermanentError struct{ err error }

func (e *taskPermanentError) Error() string { return e.err.Error() }
func (e *taskPermanentError) Unwrap() error { return e.err }

// permanentTaskError 包装永久失败。
func permanentTaskError(err error) error {
	if err == nil {
		return nil
	}
	return &taskPermanentError{err: err}
}

// isPermanentTaskError 判定永久失败（errors.As 走 Unwrap 链，包在内层的也算）。
func isPermanentTaskError(err error) bool {
	var target *taskPermanentError
	return errors.As(err, &target)
}

// taskWorker 一轮执行的依赖集合（生产见 defaultTaskWorker；单测直接构造）。
type taskWorker struct {
	ledger   taskLedger
	executor taskExecutor
	// settings 读两键（poll/max_attempts）；nil ⇒ 用缺省
	settings func(ctx context.Context) (taskSettings, error)
	// enabled 台账启用自查（每轮先跑）；nil ⇒ 视为启用（只在单测里这么用）
	enabled func(ctx context.Context) (bool, error)
	lease   time.Duration
}

// newTaskWorker 构造 worker：账本 + 执行面为注入项，配置读取与启用自查走生产实现。
func newTaskWorker(ledger taskLedger, executor taskExecutor) *taskWorker {
	return &taskWorker{
		ledger:   ledger,
		executor: executor,
		settings: loadTaskSettings,
		// 句柄在**调用时**取（database.DB()），避免构造期快照到旧连接
		enabled: func(ctx context.Context) (bool, error) { return taskPluginEnabled(ctx, database.DB()) },
		lease:   DefaultTaskLease,
	}
}

// defaultTaskWorker 生产装配：账本 = 当前库的 *Store，执行面 = 与公开面同一 *Service。
func defaultTaskWorker(ctx context.Context) (*taskWorker, error) {
	store, err := defaultStore(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errNoTaskDatabase, err)
	}
	set, err := loadPluginSettings(ctx)
	if err != nil {
		return nil, err
	}
	service, err := defaultService(ctx, set)
	if err != nil {
		return nil, err
	}
	return newTaskWorker(store, service), nil
}

// ===== 启动与主循环 =====

var (
	taskWorkerOnce sync.Once
	taskWarnOnce   sync.Once
)

// StartTaskWorkerOnce 启动任务 worker（幂等：进程内只起一次；由插件 mount 调用）。
//
// ctx 决定 worker 的生命周期（取消即退出）；接线方传 context.Background() 即可——宿主没有插件
// 卸载钩子（注册表只收「名/版本/形态/挂载点」），停用态靠**每轮自查台账**收敛，不靠 ctx。
func StartTaskWorkerOnce(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	taskWorkerOnce.Do(func() { go taskWorkerLoop(ctx) })
}

// taskWorkerLoop worker 主循环：启动复位一次 → 每轮 RunTaskOnce → 按配置间隔等待（间隔 ≥1s）。
func taskWorkerLoop(ctx context.Context) {
	// 启动复位：把租约过期的 processing 行退回 pending（重启续跑；领取 SQL 本身也能救援过期租约，
	// 这一步是让重启后的台账读数立刻自洽）。失败只提示一次，不阻断主循环。
	if worker, err := defaultTaskWorker(ctx); err != nil {
		taskWarnOnce.Do(func() {
			log.Printf("[ai] 任务 worker 的启动复位被跳过（数据库未就绪）: %v", err)
		})
	} else if reset, err := worker.ResetStaleLeases(ctx); err != nil {
		taskWarnOnce.Do(func() {
			log.Printf("[ai] 任务 worker 启动复位失败（后续同类错误不再重复记录）: %v", err)
		})
	} else if reset > 0 {
		log.Printf("[ai] 任务 worker 已复位 %d 行租约过期的 processing 任务", reset)
	}

	for {
		if _, err := RunTaskOnce(ctx); err != nil && !errors.Is(err, errNoTaskDatabase) {
			taskWarnOnce.Do(func() {
				log.Printf("[ai] 任务 worker 本轮未执行（后续同类错误不再重复记录）: %v", err)
			})
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(taskPollInterval(ctx)):
		}
	}
}

// taskPollInterval 每轮重读 task_poll_seconds（配置读不到用缺省 5s）。
// 返回值恒 ≥ MinTaskPollSeconds（clamp 在 normalizeTaskPollSeconds 里做，这里再保一层：
// worker 主循环**不允许**出现 0 间隔的忙等）。
func taskPollInterval(ctx context.Context) time.Duration {
	seconds := DefaultTaskPollSeconds
	if set, err := loadTaskSettings(ctx); err == nil {
		seconds = set.PollSeconds
	}
	if seconds < MinTaskPollSeconds {
		seconds = MinTaskPollSeconds
	}
	return time.Duration(seconds) * time.Second
}

// taskPluginEnabled 直读台账状态（非 enabled / 无台账行 / 读失败 ⇒ 视为不可跑）。
//
// 表与判据同 host.Gate（internal/plugin/host/gate.go 的 readStatus）与 kb 的 jobs worker：
// 三处都读 model.Plugin 的同一行 status，因此「闸放不放行」「worker 领不领活」不会分叉。
func taskPluginEnabled(ctx context.Context, db *gorm.DB) (bool, error) {
	if db == nil {
		return false, errNoTaskDatabase
	}
	row, err := gorm.G[model.Plugin](db).Select("status").Where("name = ?", Name).First(ctx)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, err
	}
	return row.Status == host.StatusEnabled, nil
}

// RunTaskOnce 执行一轮（供 StartTaskWorkerOnce 与库级用例调用）。
// 返回 claimed=true 表示本轮领取并执行了一条任务。
func RunTaskOnce(ctx context.Context) (claimed bool, err error) {
	worker, err := defaultTaskWorker(ctx)
	if err != nil {
		return false, err
	}
	return worker.RunOnce(ctx)
}

// ===== 一轮 =====

// ResetStaleLeases 启动复位：把租约过期的 processing 行退回 pending（主循环启动时调一次）。
//
// 抽成方法是为了让「重启续跑」的这一步可被无库用例直接驱动（见 task_worker_test.go）：
// 领取 SQL 本身也能救援过期租约，复位只是让重启后的台账读数立刻自洽。
func (w *taskWorker) ResetStaleLeases(ctx context.Context) (int64, error) {
	if w == nil || w.ledger == nil {
		return 0, errNoTaskDatabase
	}
	return w.ledger.resetStaleTaskLeases(ctx)
}

// RunOnce 一轮：① 台账启用自查（非 enabled ⇒ 空转）② 领取一条 ③ 执行并落终态/退回 pending。
//
// 顺序纪律（与 kb 的 jobs worker 同款）：**启用自查在最前**——非 enabled 时本轮零副作用
// （不领活、不写库），pending 行原样留在原状态。
func (w *taskWorker) RunOnce(ctx context.Context) (bool, error) {
	if w == nil || w.ledger == nil || w.executor == nil {
		return false, errNoTaskDatabase
	}
	if w.enabled != nil {
		enabled, err := w.enabled(ctx)
		if err != nil {
			return false, err
		}
		if !enabled {
			// 空转：停用态只读台账、不领活（本轮不产生任何写库动作）
			return false, nil
		}
	}
	row, err := w.ledger.claimTask(ctx, w.leaseDuration())
	if err != nil {
		return false, err
	}
	if row == nil {
		return false, nil
	}
	set := w.settingsOr(ctx)
	return true, w.execute(ctx, row, set)
}

// leaseDuration 租约（<=0 回落缺省）。
func (w *taskWorker) leaseDuration() time.Duration {
	if w.lease <= 0 {
		return DefaultTaskLease
	}
	return w.lease
}

// settingsOr 读两键；读失败用缺省（宁可慢一拍也要继续跑，不得因一次配置读失败停摆）。
func (w *taskWorker) settingsOr(ctx context.Context) taskSettings {
	if w.settings == nil {
		return taskSettings{PollSeconds: DefaultTaskPollSeconds, MaxAttempts: DefaultTaskMaxAttempts}
	}
	set, err := w.settings(ctx)
	if err == nil {
		return set
	}
	log.Printf("[ai] 任务 worker 读取配置失败，本轮用缺省（间隔 %ds / 尝试 %d 次）: %v",
		DefaultTaskPollSeconds, DefaultTaskMaxAttempts, err)
	return taskSettings{PollSeconds: DefaultTaskPollSeconds, MaxAttempts: DefaultTaskMaxAttempts}
}

// errTaskLeaseLost 是「续租失败 / 影响行数为 0」的取消原因：租约已丢，本副本不再是持有者。
var errTaskLeaseLost = errors.New("插件 ai：任务租约已丢失，本副本停止协作")

// execute 执行一条已领取任务：起续租协程 → 跑业务 → 落终态 / 退回 pending。
//
// 租约纪律（store_task.go 的 renewTaskLease 契约：「影响行数 0 = 租约已丢，调用方应停止协作」）：
// 续租报错或影响行数 0 ⇒ renewLoop 取消 execCtx（原因 errTaskLeaseLost）⇒
//
//	①业务随 execCtx 中断（上游调用不再继续烧额度）；
//	②本函数**一个字都不写**——终态归重领者（它已 attempt+1），否则就是「双跑 + 终态取最后写入者」。
//
// 报错与 n==0 同等处理（报错时租约状态未知）：宁可停手让重领者收口，也不与它抢着写；
// 代价如实登记——一次瞬时续租失败会让这一轮作废（行留在 processing，租约到期后被重领重跑）。
// 残余风险：续租周期 = 租约/3（默认 100s），因此「租约被外力作废（如带外 SQL 改 locked_until）
// 后的一个续租周期内」本副本仍可能继续跑——双跑窗口收敛到续租周期，不是 0。
func (w *taskWorker) execute(ctx context.Context, row *claimedTask, set taskSettings) error {
	execCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	go w.renewLoop(execCtx, row.ID, cancel)

	done, result, runErr := w.run(execCtx, row)
	cancel(nil) // 正常收口：停掉续租协程（不设取消原因）

	if errors.Is(context.Cause(execCtx), errTaskLeaseLost) {
		log.Printf("[ai] 任务租约已丢失，本副本停止执行与收口（终态归重领者） task=%d attempt=%d: %v",
			row.ID, row.Attempt, runErr)
		return nil
	}

	if runErr == nil {
		if done > 0 {
			if err := w.ledger.updateTaskProgress(ctx, row.ID, done, row.ProgressTotal); err != nil {
				log.Printf("[ai] 任务进度收口失败 task=%d: %v", row.ID, err)
			}
		}
		return w.ledger.markTaskSuccess(ctx, row.ID, row.Attempt, result)
	}
	reason := taskFailureReason(runErr)
	if isPermanentTaskError(runErr) || row.Attempt >= set.MaxAttempts {
		return w.ledger.markTaskFailed(ctx, row.ID, row.Attempt, reason)
	}
	return w.ledger.requeueTask(ctx, row.ID, row.Attempt, reason)
}

// taskFailureReason 把失败原因收敛成**可读且有信息量**的文案（落 error 列，任务页据此定位）。
//
// 编排层的 *APIError 只把公开 Message 放进 Error()（例如「所有渠道均不可用」），上游真相在
// Detail() 里（例如「上游 500: internal server error」）——任务台账要的是后者，
// 否则运维只看到一句「不可用」而查不到上游为何失败。非 APIError 直接取其文案。
// 文案里的密钥已由驱动出口（redact.go 的 redactSecret）脱敏，故这里可原样落库。
func taskFailureReason(err error) string {
	if err == nil {
		return ""
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		if detail := strings.TrimSpace(apiErr.Detail()); detail != "" {
			return detail
		}
		return apiErr.Error()
	}
	return err.Error()
}

// renewLoop 续租循环（租约/3 周期）；租约已丢（进程被抢占 / 任务已终态 / 续租报错）即停止，
// 并**把丢失传导到执行面**（leaseLost = execute 传进来的 cancel(errTaskLeaseLost)）——
// 只 return 是旧行为：执行面不会停机、还会无条件写终态（见 execute 的租约纪律）。
func (w *taskWorker) renewLoop(ctx context.Context, id uint64, leaseLost func(error)) {
	interval := w.leaseDuration() / 3
	if interval <= 0 {
		interval = time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n, err := w.ledger.renewTaskLease(ctx, id, w.leaseDuration())
			if err != nil || n == 0 {
				if leaseLost != nil {
					leaseLost(errTaskLeaseLost)
				}
				return
			}
		}
	}
}

// ===== 业务执行（按任务类型分派；分片进度在 runEmbedding 内逐片落库）=====

// run 执行一条任务的业务面：返回（已完成条目数, 落库结果, 错误）。
func (w *taskWorker) run(ctx context.Context, row *claimedTask) (int, datatypes.JSON, error) {
	payload, err := decodeTaskPayload(row.Payload)
	if err != nil {
		// 载荷损坏是永久失败：重试不会修好它
		return 0, nil, permanentTaskError(err)
	}
	switch row.TaskType {
	case TaskTypeChat:
		return w.runChat(ctx, row, payload)
	case TaskTypeEmbedding:
		return w.runEmbedding(ctx, row, payload)
	default:
		return 0, nil, permanentTaskError(fmt.Errorf("未知任务类型 %q（闭集 %s|%s）", row.TaskType, TaskTypeChat, TaskTypeEmbedding))
	}
}

// runChat 执行 chat 任务：走与公开面同一 *Service（熔断/降级/计费同一实现）。
//
// usage request_id 按尝试派生（taskUsageRequestID），因此「每次尝试各落一行 usage」，
// 重试不会覆盖上一轮的计费行（详见 task.go 头注）。
func (w *taskWorker) runChat(ctx context.Context, row *claimedTask, payload taskPayload) (int, datatypes.JSON, error) {
	if strings.TrimSpace(payload.Model) == "" || len(payload.Messages) == 0 {
		return 0, nil, permanentTaskError(errors.New("chat 任务载荷缺少 model 或 messages"))
	}
	res, err := w.executor.Chat(ctx, ChatInput{
		Model:       payload.Model,
		Messages:    payload.Messages,
		Temperature: payload.Temperature,
		TopP:        payload.TopP,
		MaxTokens:   payload.MaxTokens,
		BizType:     payload.BizType,
		Source:      payload.Source,
		RequestID:   taskUsageRequestID(row.RequestID, row.Attempt, 0),
	})
	if err != nil {
		return 0, nil, err
	}
	raw, err := encodeChatTaskResult(res)
	if err != nil {
		return 0, nil, permanentTaskError(err)
	}
	return 1, raw, nil
}

// runEmbedding 执行批量 embedding：**worker 侧分片**（每片 ≤ maxUpstreamEmbeddingBatch=20），
// 每片一次 Service.Embeddings 调用、每片用完即回写进度。
//
// 为什么必须分片：Service.Embeddings 自身有同步护栏（service.go:38-42 的 maxPublicEmbeddingInput=64，
// 超限直接 400）——「同步 64 条上限保持不变、异步可提交千/万级」的实现方式就是在这里按片喂，
// 公开面 handler_public.go:421-430 的 400 一行不动。分片复用同步路径的同一分片器 splitEmbeddingInput，
// 不另写一套切分口径。
//
// 顺序：按片序串行调用并**按返回顺序拼接**（同步路径内部也是如此保序），因此向量位次与输入位次一致。
// 失败：任一片失败即整批失败（退回 pending / 置 failed），重试从头跑（进度 done 单调不回退）。
func (w *taskWorker) runEmbedding(ctx context.Context, row *claimedTask, payload taskPayload) (int, datatypes.JSON, error) {
	if strings.TrimSpace(payload.Model) == "" || len(payload.Inputs) == 0 {
		return 0, nil, permanentTaskError(errors.New("embedding 任务载荷缺少 model 或 inputs"))
	}
	total := len(payload.Inputs)
	shards := splitEmbeddingInput(payload.Inputs, maxUpstreamEmbeddingBatch)
	vectors := make([][]float64, 0, total)
	model := payload.Model
	for index, shard := range shards {
		res, err := w.executor.Embeddings(ctx, EmbeddingInput{
			Model:     payload.Model,
			Input:     shard,
			BizType:   payload.BizType,
			Source:    payload.Source,
			RequestID: taskUsageRequestID(row.RequestID, row.Attempt, index+1),
		})
		if err != nil {
			return len(vectors), nil, err
		}
		if res != nil {
			if res.Model != "" {
				model = res.Model
			}
			for _, item := range res.Data {
				vectors = append(vectors, item.Vector)
			}
		}
		done := len(vectors)
		if done > total {
			done = total
		}
		if err := w.ledger.updateTaskProgress(ctx, row.ID, done, total); err != nil {
			// 进度回写失败只是读数不新鲜，不打断任务本身
			log.Printf("[ai] 任务分片进度回写失败 task=%d: %v", row.ID, err)
		}
	}
	if len(vectors) != total {
		return len(vectors), nil, fmt.Errorf("上游返回向量数 %d 与提交条数 %d 不一致（按失败处理，下轮重跑整批）",
			len(vectors), total)
	}
	raw, err := encodeEmbeddingTaskResult(model, vectors)
	if err != nil {
		return total, nil, permanentTaskError(err)
	}
	return total, raw, nil
}
