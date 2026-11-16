// plugin: ai v0.2.0
// task_worker_test.go 是 task worker 的**无库**用例：内存假账本（fakeTaskLedger，互斥锁保护）+ 假执行面
// （fakeTaskExecutor）复刻领取/续租/终态的语义，因此可以在没有 Postgres 的环境里跑 worker 的全部行为：
//
//	空转（disabled 时不领活）、chat 成功/失败重试到 task_max_attempts、永久失败立即置 failed、
//	embedding 分片进度与保序结果、分片失败后重跑整批、租约过期重领、配置读失败回落缺省。
//
// 真库侧的 SQL 语义（SKIP LOCKED 并发恰一人领到、部分唯一索引并发、租约复位）在 task_db_test.go 断言——
// 假账本只负责「把 worker 的接线跑起来」，不替代真库判据。
package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/datatypes"
)

// ===== 假账本 =====

type fakeTaskRow struct {
	task        Task
	lockedUntil time.Time
	hasLease    bool
}

// fakeTaskLedger 内存账本：语义对齐 store_task.go 的 SQL 原语（同一状态机、同一 attempt 累加口径）。
type fakeTaskLedger struct {
	mu          sync.Mutex
	nextID      uint64
	rows        map[uint64]*fakeTaskRow
	order       []uint64
	claimCalls  int
	renewCalls  int
	resetCalls  int
	progressLog []int
	lastLease   time.Duration
	// renewReturnsZero：续租一律返回「影响行数 0 = 租约已丢」（用例构造被重领 / 租约被外力作废的现场）。
	renewReturnsZero bool
}

// ownedBy 复刻 store_task.go 的写侧守卫判据（status=processing 且 attempt 相等）。
func (r *fakeTaskRow) ownedBy(attempt int) bool {
	return r != nil && r.task.Status == TaskStatusProcessing && r.task.Attempt == attempt
}

func newFakeTaskLedger() *fakeTaskLedger {
	return &fakeTaskLedger{nextID: 1, rows: map[uint64]*fakeTaskRow{}}
}

// seed 放一行账本（ID 为 0 时自动分配）。
func (f *fakeTaskLedger) seed(task Task) uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	if task.ID == 0 {
		task.ID = f.nextID
		f.nextID++
	}
	if task.Status == "" {
		task.Status = TaskStatusPending
	}
	f.rows[task.ID] = &fakeTaskRow{task: task}
	f.order = append(f.order, task.ID)
	return task.ID
}

// mutate 直接改一行（用例构造「processing 且租约过期」这类现场）。
func (f *fakeTaskLedger) mutate(id uint64, fn func(*fakeTaskRow)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f.rows[id])
}

// snapshot 读一行（含租约）。
func (f *fakeTaskLedger) snapshot(id uint64) fakeTaskRow {
	f.mu.Lock()
	defer f.mu.Unlock()
	row := f.rows[id]
	out := *row
	out.task.Payload = append(datatypes.JSON(nil), row.task.Payload...)
	out.task.Result = append(datatypes.JSON(nil), row.task.Result...)
	return out
}

func (f *fakeTaskLedger) claimCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.claimCalls
}

// claimedLease 最近一次领取实际收到的租约（断言「worker.lease 交给了账本」）。
func (f *fakeTaskLedger) claimedLease() time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastLease
}

func (f *fakeTaskLedger) progress() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int(nil), f.progressLog...)
}

func (f *fakeTaskLedger) claimTask(_ context.Context, lease time.Duration) (*claimedTask, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.claimCalls++
	f.lastLease = lease
	now := time.Now()
	for _, id := range f.order {
		row := f.rows[id]
		claimable := row.task.Status == TaskStatusPending ||
			(row.task.Status == TaskStatusProcessing && (!row.hasLease || row.lockedUntil.Before(now)))
		if !claimable {
			continue
		}
		row.task.Status = TaskStatusProcessing
		row.task.Attempt++
		row.hasLease = true
		row.lockedUntil = now.Add(lease)
		return &claimedTask{
			ID: row.task.ID, TaskType: row.task.TaskType, Status: TaskStatusProcessing,
			Payload: row.task.Payload, RequestID: row.task.RequestID, Source: row.task.Source,
			BizType: row.task.BizType, Attempt: row.task.Attempt,
			ProgressDone: row.task.ProgressDone, ProgressTotal: row.task.ProgressTotal,
		}, nil
	}
	return nil, nil
}

func (f *fakeTaskLedger) renewTaskLease(_ context.Context, id uint64, lease time.Duration) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.renewCalls++
	if f.renewReturnsZero {
		return 0, nil // 租约已丢（被重领 / 被外力作废）：真实现的判据是 RowsAffected == 0
	}
	row := f.rows[id]
	if row == nil || row.task.Status != TaskStatusProcessing {
		return 0, nil
	}
	row.lockedUntil = time.Now().Add(lease)
	row.hasLease = true
	return 1, nil
}

func (f *fakeTaskLedger) updateTaskProgress(_ context.Context, id uint64, done, total int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	row := f.rows[id]
	if row == nil {
		return ErrTaskNotFound
	}
	if done > row.task.ProgressDone {
		row.task.ProgressDone = done
	}
	if total > 0 {
		row.task.ProgressTotal = total
	}
	f.progressLog = append(f.progressLog, row.task.ProgressDone)
	return nil
}

func (f *fakeTaskLedger) markTaskSuccess(_ context.Context, id uint64, attempt int, result datatypes.JSON) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	row := f.rows[id]
	if row == nil {
		return ErrTaskNotFound
	}
	if !row.ownedBy(attempt) {
		return ErrTaskLeaseLost
	}
	row.task.Status = TaskStatusSuccess
	row.task.Result = append(datatypes.JSON(nil), result...)
	if row.task.ProgressTotal > row.task.ProgressDone {
		row.task.ProgressDone = row.task.ProgressTotal
	}
	row.hasLease = false
	return nil
}

func (f *fakeTaskLedger) markTaskFailed(_ context.Context, id uint64, attempt int, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	row := f.rows[id]
	if row == nil {
		return ErrTaskNotFound
	}
	if !row.ownedBy(attempt) {
		return ErrTaskLeaseLost
	}
	row.task.Status = TaskStatusFailed
	row.task.Error = truncateRunes(reason, lastErrorMaxLen)
	row.hasLease = false
	return nil
}

func (f *fakeTaskLedger) requeueTask(_ context.Context, id uint64, attempt int, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	row := f.rows[id]
	if row == nil {
		return ErrTaskNotFound
	}
	if !row.ownedBy(attempt) {
		return ErrTaskLeaseLost
	}
	row.task.Status = TaskStatusPending
	row.task.Error = truncateRunes(reason, lastErrorMaxLen)
	row.hasLease = false
	return nil
}

func (f *fakeTaskLedger) resetStaleTaskLeases(_ context.Context) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resetCalls++
	var reset int64
	now := time.Now()
	for _, row := range f.rows {
		if row.task.Status == TaskStatusProcessing && (!row.hasLease || row.lockedUntil.Before(now)) {
			row.task.Status = TaskStatusPending
			row.hasLease = false
			reset++
		}
	}
	return reset, nil
}

// ===== 假执行面 =====

type fakeTaskExecutor struct {
	mu         sync.Mutex
	chatCalls  []ChatInput
	embedCalls []EmbeddingInput
	chatFn     func(ChatInput) (*ChatResult, error)
	embedFn    func(EmbeddingInput) (*EmbeddingResult, error)
	// chatCtxFn 是带 ctx 的 chat 钩子（优先于 chatFn）：租约丢失类用例靠它观测「执行面是否被取消」。
	chatCtxFn func(context.Context, ChatInput) (*ChatResult, error)
}

func (f *fakeTaskExecutor) Chat(ctx context.Context, in ChatInput) (*ChatResult, error) {
	f.mu.Lock()
	f.chatCalls = append(f.chatCalls, in)
	fn, ctxFn := f.chatFn, f.chatCtxFn
	f.mu.Unlock()
	if ctxFn != nil {
		return ctxFn(ctx, in)
	}
	if fn != nil {
		return fn(in)
	}
	return &ChatResult{ID: "cmpl-fake", Model: in.Model, Content: "回复", FinishReason: "stop",
		PromptTokens: 3, CompletionTokens: 5, TotalTokens: 8}, nil
}

func (f *fakeTaskExecutor) Embeddings(_ context.Context, in EmbeddingInput) (*EmbeddingResult, error) {
	f.mu.Lock()
	f.embedCalls = append(f.embedCalls, in)
	fn := f.embedFn
	f.mu.Unlock()
	if fn != nil {
		return fn(in)
	}
	items := embeddingInputItems(in.Input)
	result := &EmbeddingResult{Model: in.Model, PromptTokens: len(items)}
	for index := range items {
		result.Data = append(result.Data, Embedding{Index: index, Object: "embedding",
			Vector: []float64{float64(index), 1, 2}})
	}
	result.TotalTokens = result.PromptTokens
	return result, nil
}

func (f *fakeTaskExecutor) chatInputs() []ChatInput {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ChatInput(nil), f.chatCalls...)
}

func (f *fakeTaskExecutor) embedInputs() []EmbeddingInput {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]EmbeddingInput(nil), f.embedCalls...)
}

// embeddingInputItems 把 EmbeddingInput.Input 收敛成切片（假执行面用）。
func embeddingInputItems(input any) []string {
	switch v := input.(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if text, ok := item.(string); ok {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

// ===== 用例脚手架 =====

func taskTestSettings(maxAttempts int) func(context.Context) (taskSettings, error) {
	return func(context.Context) (taskSettings, error) {
		return taskSettings{PollSeconds: 1, MaxAttempts: maxAttempts}, nil
	}
}

func taskTestEnabled(enabled bool) func(context.Context) (bool, error) {
	return func(context.Context) (bool, error) { return enabled, nil }
}

// newTestTaskWorker 装配 worker（默认启用 + 1 秒轮询 / 指定尝试次数 + 短租约）。
func newTestTaskWorker(ledger *fakeTaskLedger, executor *fakeTaskExecutor, maxAttempts int) *taskWorker {
	worker := newTaskWorker(ledger, executor)
	worker.enabled = taskTestEnabled(true)
	worker.settings = taskTestSettings(maxAttempts)
	worker.lease = time.Minute
	return worker
}

func mustTaskPayload(t *testing.T, payload taskPayload) datatypes.JSON {
	t.Helper()
	raw, err := encodeTaskPayload(payload)
	if err != nil {
		t.Fatalf("载荷编码失败: %v", err)
	}
	return raw
}

// ===== 用例 =====

// TestTaskWorkerDisabledRoundIdles 停用态**空转**：每轮仍读台账 status，但既不领活也不写库。
func TestTaskWorkerDisabledRoundIdles(t *testing.T) {
	ledger := newFakeTaskLedger()
	executor := &fakeTaskExecutor{}
	id := ledger.seed(Task{
		TaskType: TaskTypeChat, RequestID: "rid-idle", ProgressTotal: 1,
		Payload: mustTaskPayload(t, taskPayload{Model: "gpt-test", Messages: []Message{{Role: "user", Content: "hi"}},
			BizType: "admin", Source: "admin:task"}),
	})

	enabledCalls := 0
	worker := newTestTaskWorker(ledger, executor, 3)
	worker.enabled = func(context.Context) (bool, error) { enabledCalls++; return false, nil }

	for round := 0; round < 3; round++ {
		claimed, err := worker.RunOnce(context.Background())
		if err != nil {
			t.Fatalf("停用态不应报错: %v", err)
		}
		if claimed {
			t.Fatal("停用态不得领取任务（本轮应空转）")
		}
	}
	if enabledCalls != 3 {
		t.Fatalf("每轮都要直读台账 status（期望 3 次，实际 %d 次）", enabledCalls)
	}
	if ledger.claimCount() != 0 || len(executor.chatInputs()) != 0 {
		t.Fatalf("停用态不得触碰领取与执行面: claim=%d chat=%d", ledger.claimCount(), len(executor.chatInputs()))
	}
	if row := ledger.snapshot(id); row.task.Status != TaskStatusPending || row.task.Attempt != 0 || row.task.Error != "" {
		t.Fatalf("停用态不得改账本行: %+v", row.task)
	}
}

// TestTaskWorkerChatSuccess 一轮内 pending → processing → success，结果与进度都落库。
func TestTaskWorkerChatSuccess(t *testing.T) {
	ledger := newFakeTaskLedger()
	executor := &fakeTaskExecutor{}
	id := ledger.seed(Task{
		TaskType: TaskTypeChat, Status: TaskStatusPending, RequestID: "rid-chat",
		BizType: "admin", Source: "admin:task", ProgressTotal: 1,
		Payload: mustTaskPayload(t, taskPayload{Model: "gpt-test", Messages: []Message{{Role: "user", Content: "hi"}},
			BizType: "admin", Source: "admin:task"}),
	})
	worker := newTestTaskWorker(ledger, executor, 3)

	claimed, err := worker.RunOnce(context.Background())
	if err != nil || !claimed {
		t.Fatalf("应领取并执行一条任务: claimed=%v err=%v", claimed, err)
	}
	row := ledger.snapshot(id)
	if row.task.Status != TaskStatusSuccess {
		t.Fatalf("终态应为 success，实际 %s（error=%s）", row.task.Status, row.task.Error)
	}
	if row.task.ProgressDone != 1 || row.task.ProgressTotal != 1 {
		t.Fatalf("进度应收口到 1/1: %d/%d", row.task.ProgressDone, row.task.ProgressTotal)
	}
	if row.task.Attempt != 1 {
		t.Fatalf("领取后 attempt 应为 1，实际 %d", row.task.Attempt)
	}
	if row.hasLease {
		t.Fatal("终态必须释放租约")
	}
	if got := taskResultEntryCount(TaskTypeChat, row.task.Result); got != 1 {
		t.Fatalf("结果条目数应为 1，实际 %d（result=%s）", got, string(row.task.Result))
	}
	calls := executor.chatInputs()
	if len(calls) != 1 {
		t.Fatalf("chat 应恰好调用一次，实际 %d 次", len(calls))
	}
	if calls[0].RequestID != "task-rid-chat-a1" {
		t.Fatalf("usage request_id 应按尝试派生，实际 %q", calls[0].RequestID)
	}
	if calls[0].BizType != "admin" || calls[0].Source != "admin:task" || calls[0].Model != "gpt-test" {
		t.Fatalf("调用上下文未透传: %+v", calls[0])
	}
}

// TestTaskWorkerRetriesUntilMaxAttempts 失败路径：attempt 到 task_max_attempts 后置 failed + error，
// 且**每次尝试各用一枚派生的 usage request_id**（重试不覆盖上一轮的计费行）。
func TestTaskWorkerRetriesUntilMaxAttempts(t *testing.T) {
	ledger := newFakeTaskLedger()
	executor := &fakeTaskExecutor{chatFn: func(ChatInput) (*ChatResult, error) {
		return nil, errors.New("上游 500：internal server error")
	}}
	id := ledger.seed(Task{
		TaskType: TaskTypeChat, Status: TaskStatusPending, RequestID: "rid-fail", ProgressTotal: 1,
		Payload: mustTaskPayload(t, taskPayload{Model: "gpt-test", Messages: []Message{{Role: "user", Content: "hi"}},
			BizType: "admin", Source: "admin:task"}),
	})
	worker := newTestTaskWorker(ledger, executor, 3)

	for attempt := 1; attempt <= 2; attempt++ {
		claimed, err := worker.RunOnce(context.Background())
		if err != nil || !claimed {
			t.Fatalf("第 %d 轮应领取: claimed=%v err=%v", attempt, claimed, err)
		}
		row := ledger.snapshot(id)
		if row.task.Status != TaskStatusPending {
			t.Fatalf("第 %d 轮后应退回 pending（未耗尽尝试），实际 %s", attempt, row.task.Status)
		}
		if row.task.Attempt != attempt {
			t.Fatalf("attempt 应为 %d，实际 %d", attempt, row.task.Attempt)
		}
		if row.task.Error == "" {
			t.Fatalf("第 %d 轮后应留下错误原因（供任务页排查）", attempt)
		}
	}

	claimed, err := worker.RunOnce(context.Background())
	if err != nil || !claimed {
		t.Fatalf("第 3 轮应领取: claimed=%v err=%v", claimed, err)
	}
	row := ledger.snapshot(id)
	if row.task.Status != TaskStatusFailed {
		t.Fatalf("尝试耗尽后应为 failed，实际 %s", row.task.Status)
	}
	if row.task.Attempt != 3 {
		t.Fatalf("失败时 attempt 应为 3，实际 %d", row.task.Attempt)
	}
	if !strings.Contains(row.task.Error, "上游 500") {
		t.Fatalf("失败原因未落库: %q", row.task.Error)
	}

	var ids []string
	for _, call := range executor.chatInputs() {
		ids = append(ids, call.RequestID)
	}
	want := "task-rid-fail-a1,task-rid-fail-a2,task-rid-fail-a3"
	if strings.Join(ids, ",") != want {
		t.Fatalf("每次尝试的 usage request_id 应各自派生（每次尝试各落一行 usage），实际 %v", ids)
	}
}

// TestTaskWorkerPermanentErrorFailsImmediately 载荷损坏是永久失败：不烧尝试次数、直接置 failed。
func TestTaskWorkerPermanentErrorFailsImmediately(t *testing.T) {
	ledger := newFakeTaskLedger()
	executor := &fakeTaskExecutor{}
	id := ledger.seed(Task{
		TaskType: TaskTypeChat, Status: TaskStatusPending, RequestID: "rid-broken", ProgressTotal: 1,
		Payload: datatypes.JSON("not json"),
	})
	worker := newTestTaskWorker(ledger, executor, 3)

	claimed, err := worker.RunOnce(context.Background())
	if err != nil || !claimed {
		t.Fatalf("应领取: claimed=%v err=%v", claimed, err)
	}
	row := ledger.snapshot(id)
	if row.task.Status != TaskStatusFailed || row.task.Attempt != 1 {
		t.Fatalf("损坏载荷应一次失败（attempt=1，status=failed），实际 attempt=%d status=%s", row.task.Attempt, row.task.Status)
	}
	if !strings.Contains(row.task.Error, "载荷") {
		t.Fatalf("失败原因应指明载荷问题: %q", row.task.Error)
	}
	if len(executor.chatInputs()) != 0 {
		t.Fatal("载荷都没解析成功，不得调用上游")
	}
}

// TestTaskWorkerUnknownTypeIsPermanent 未知任务类型同样是永久失败（响亮失败，不静默当 chat 处理）。
func TestTaskWorkerUnknownTypeIsPermanent(t *testing.T) {
	ledger := newFakeTaskLedger()
	executor := &fakeTaskExecutor{}
	id := ledger.seed(Task{
		TaskType: "image", Status: TaskStatusPending, RequestID: "rid-unknown", ProgressTotal: 1,
		Payload: mustTaskPayload(t, taskPayload{Model: "gpt-test", BizType: "admin", Source: "admin:task"}),
	})
	worker := newTestTaskWorker(ledger, executor, 3)

	claimed, err := worker.RunOnce(context.Background())
	if err != nil || !claimed {
		t.Fatalf("应领取: claimed=%v err=%v", claimed, err)
	}
	row := ledger.snapshot(id)
	if row.task.Status != TaskStatusFailed || !strings.Contains(row.task.Error, "未知任务类型") {
		t.Fatalf("未知类型应一次失败并写明原因: status=%s error=%q", row.task.Status, row.task.Error)
	}
}

// TestTaskWorkerEmbeddingShardsProgressAndResult 批量 embedding：worker 侧分片（≤20/片）、
// 逐片回写进度、结果保序且条数与提交一致，每片一枚派生 usage id。
func TestTaskWorkerEmbeddingShardsProgressAndResult(t *testing.T) {
	ledger := newFakeTaskLedger()
	executor := &fakeTaskExecutor{}
	inputs := make([]string, 0, 45)
	for i := 0; i < 45; i++ {
		inputs = append(inputs, fmt.Sprintf("文本-%d", i))
	}
	id := ledger.seed(Task{
		TaskType: TaskTypeEmbedding, Status: TaskStatusPending, RequestID: "rid-emb",
		BizType: "admin", Source: "admin:task", ProgressTotal: 45,
		Payload: mustTaskPayload(t, taskPayload{Model: "bge-test", Inputs: inputs, BizType: "admin", Source: "admin:task"}),
	})
	worker := newTestTaskWorker(ledger, executor, 3)

	claimed, err := worker.RunOnce(context.Background())
	if err != nil || !claimed {
		t.Fatalf("应领取: claimed=%v err=%v", claimed, err)
	}
	row := ledger.snapshot(id)
	if row.task.Status != TaskStatusSuccess {
		t.Fatalf("终态应为 success，实际 %s（error=%s）", row.task.Status, row.task.Error)
	}
	if row.task.ProgressDone != 45 || row.task.ProgressTotal != 45 {
		t.Fatalf("进度应收口到 45/45: %d/%d", row.task.ProgressDone, row.task.ProgressTotal)
	}
	calls := executor.embedInputs()
	if len(calls) != 3 {
		t.Fatalf("45 条应按 20/20/5 分三片调用，实际 %d 次", len(calls))
	}
	for index, call := range calls {
		items := embeddingInputItems(call.Input)
		if len(items) > maxUpstreamEmbeddingBatch {
			t.Fatalf("第 %d 片 %d 条超上游单片上限 %d（同步护栏 64 条不可被异步路径越过）",
				index+1, len(items), maxUpstreamEmbeddingBatch)
		}
		want := fmt.Sprintf("task-rid-emb-a1-s%d", index+1)
		if call.RequestID != want {
			t.Fatalf("第 %d 片的 usage request_id 应为 %q，实际 %q", index+1, want, call.RequestID)
		}
	}
	if sizes := []int{20, 20, 5}; len(calls[0].Input.([]string)) != sizes[0] ||
		len(calls[1].Input.([]string)) != sizes[1] || len(calls[2].Input.([]string)) != sizes[2] {
		t.Fatalf("分片形状不符: %d/%d/%d",
			len(calls[0].Input.([]string)), len(calls[1].Input.([]string)), len(calls[2].Input.([]string)))
	}
	var result taskEmbeddingResultJSON
	if err := json.Unmarshal(row.task.Result, &result); err != nil {
		t.Fatalf("结果不是合法 JSON: %v", err)
	}
	if result.Count != 45 || len(result.Embeddings) != 45 || result.Dimensions != 3 {
		t.Fatalf("结果收敛不符: count=%d len=%d dims=%d", result.Count, len(result.Embeddings), result.Dimensions)
	}
	// 保序：第 21 条（第 2 片第 1 条）的向量首分量应来自那一片的 index 0
	if result.Embeddings[20][0] != 0 {
		t.Fatalf("分片保序被破坏: 第 21 条向量 %v", result.Embeddings[20])
	}
	// 进度日志：逐片回写 20 → 40 → 45（收尾再补一次 45）
	progress := ledger.progress()
	if len(progress) < 3 || progress[0] != 20 || progress[1] != 40 || progress[len(progress)-1] != 45 {
		t.Fatalf("分片进度回写不符: %v", progress)
	}
}

// TestTaskWorkerEmbeddingPartialFailureRetriesWholeBatch 分片中途失败：整批退回 pending、
// 已完成的进度保留（单调），下一轮从头重跑并最终成功。
func TestTaskWorkerEmbeddingPartialFailureRetriesWholeBatch(t *testing.T) {
	ledger := newFakeTaskLedger()
	executor := &fakeTaskExecutor{}
	failSecondShard := true
	executor.embedFn = func(in EmbeddingInput) (*EmbeddingResult, error) {
		if failSecondShard && strings.HasPrefix(in.RequestID, "task-rid-emb2-a1-s2") {
			return nil, errors.New("上游 503：service unavailable")
		}
		items := embeddingInputItems(in.Input)
		result := &EmbeddingResult{Model: in.Model, PromptTokens: len(items), TotalTokens: len(items)}
		for index := range items {
			result.Data = append(result.Data, Embedding{Index: index, Vector: []float64{float64(index), 1, 2}})
		}
		return result, nil
	}
	inputs := make([]string, 0, 25)
	for i := 0; i < 25; i++ {
		inputs = append(inputs, fmt.Sprintf("t%d", i))
	}
	id := ledger.seed(Task{
		TaskType: TaskTypeEmbedding, Status: TaskStatusPending, RequestID: "rid-emb2",
		BizType: "admin", Source: "admin:task", ProgressTotal: 25,
		Payload: mustTaskPayload(t, taskPayload{Model: "bge-test", Inputs: inputs, BizType: "admin", Source: "admin:task"}),
	})
	worker := newTestTaskWorker(ledger, executor, 3)

	claimed, err := worker.RunOnce(context.Background())
	if err != nil || !claimed {
		t.Fatalf("第一轮应领取: claimed=%v err=%v", claimed, err)
	}
	row := ledger.snapshot(id)
	if row.task.Status != TaskStatusPending || row.task.Attempt != 1 {
		t.Fatalf("分片失败应退回 pending（attempt=1），实际 status=%s attempt=%d", row.task.Status, row.task.Attempt)
	}
	if row.task.ProgressDone != 20 {
		t.Fatalf("已完成分片的进度应保留（单调），实际 %d", row.task.ProgressDone)
	}
	if !strings.Contains(row.task.Error, "503") {
		t.Fatalf("失败原因未落库: %q", row.task.Error)
	}

	failSecondShard = false
	claimed, err = worker.RunOnce(context.Background())
	if err != nil || !claimed {
		t.Fatalf("第二轮应领取: claimed=%v err=%v", claimed, err)
	}
	row = ledger.snapshot(id)
	if row.task.Status != TaskStatusSuccess || row.task.ProgressDone != 25 || row.task.Attempt != 2 {
		t.Fatalf("重跑整批后应成功: status=%s progress=%d attempt=%d", row.task.Status, row.task.ProgressDone, row.task.Attempt)
	}
}

// TestTaskWorkerLeaseLossStopsExecutionAndSkipsTerminalWrite 是租约丢失的传导回归（store_task.go 的
// renewTaskLease 契约：「影响行数 0 = 租约已丢，调用方应停止协作」）：
//
//	续租返回 0（确定已被重领 / 租约被外力作废）⇒ ①执行面随 execCtx 被取消（这里用「等 ctx 结束
//	才返回」的假执行面观测，假件不返回就永远等不到——故判据确定、不靠 sleep 凑时序）
//	②一个字都不写，终态归重领者（行保持 processing，等租约到期被重领）。
//
// 修复前：renewLoop 只 return，执行面照跑并把「迟到的成功」写成终态 ⇒ 双跑 + 终态取最后写入者。
func TestTaskWorkerLeaseLossStopsExecutionAndSkipsTerminalWrite(t *testing.T) {
	ledger := newFakeTaskLedger()
	ledger.renewReturnsZero = true
	cancelled := make(chan struct{})
	executor := &fakeTaskExecutor{chatCtxFn: func(ctx context.Context, _ ChatInput) (*ChatResult, error) {
		<-ctx.Done()
		close(cancelled)
		return &ChatResult{ID: "late", Content: "迟到的成功", Chunks: 1, TotalTokens: 8}, nil
	}}
	id := ledger.seed(Task{
		TaskType: TaskTypeChat, RequestID: "rid-lease-lost", ProgressTotal: 1,
		Payload: mustTaskPayload(t, taskPayload{Model: "gpt-test", Messages: []Message{{Role: "user", Content: "hi"}},
			BizType: "admin", Source: "admin:task"}),
	})
	worker := newTestTaskWorker(ledger, executor, 3)
	worker.lease = 60 * time.Millisecond // 续租周期 = 租约/3 = 20ms

	claimed, err := worker.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("租约丢失属预期收口（不该冒错，否则主循环会把它当异常）: %v", err)
	}
	if !claimed {
		t.Fatal("本轮应领取到任务")
	}
	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("续租丢失必须传导到执行面（execCtx 未被取消 ⇒ 旧行为：执行面不停机）")
	}
	row := ledger.snapshot(id)
	if row.task.Status != TaskStatusProcessing {
		t.Fatalf("租约已丢 ⇒ 不得写终态（终态归重领者），实际 status=%s", row.task.Status)
	}
	if row.task.Result != nil || row.task.Error != "" {
		t.Fatalf("租约已丢 ⇒ 不得写结果/错误: result=%s error=%q", string(row.task.Result), row.task.Error)
	}
	if !row.hasLease {
		t.Fatal("假账本不该被续租改动（renewReturnsZero 下续租返回 0，代表没续上）")
	}
}

// TestTaskLedgerTerminalWritesAreOwnedByAttempt 是三个终态写口的写侧守卫（真库实现同判据，
// 见 store_task.go 的 updateOwnedTask）：attempt 不匹配（= 已被重领）⇒ ErrTaskLeaseLost 且不落库。
func TestTaskLedgerTerminalWritesAreOwnedByAttempt(t *testing.T) {
	ledger := newFakeTaskLedger()
	id := ledger.seed(Task{TaskType: TaskTypeChat, Status: TaskStatusProcessing, Attempt: 2, RequestID: "rid-owned"})
	ctx := context.Background()

	if err := ledger.markTaskSuccess(ctx, id, 1, datatypes.JSON(`{"stale":true}`)); !errors.Is(err, ErrTaskLeaseLost) {
		t.Fatalf("attempt 落后（已被重领）必须拒绝写: err=%v", err)
	}
	if err := ledger.markTaskFailed(ctx, id, 1, "旧持有者的失败"); !errors.Is(err, ErrTaskLeaseLost) {
		t.Fatalf("attempt 落后必须拒绝写 failed: err=%v", err)
	}
	if err := ledger.requeueTask(ctx, id, 1, "旧持有者退回 pending"); !errors.Is(err, ErrTaskLeaseLost) {
		t.Fatalf("attempt 落后必须拒绝退回 pending: err=%v", err)
	}
	row := ledger.snapshot(id)
	if row.task.Status != TaskStatusProcessing || row.task.Result != nil || row.task.Error != "" {
		t.Fatalf("被拒的写入不得改任何列: %+v", row.task)
	}
	// 仍是本次持有（attempt 相等）⇒ 正常落库。
	if err := ledger.markTaskSuccess(ctx, id, 2, datatypes.JSON(`{"ok":true}`)); err != nil {
		t.Fatalf("still-holder 写成功: %v", err)
	}
	if row := ledger.snapshot(id); row.task.Status != TaskStatusSuccess {
		t.Fatalf("still-holder 写成功应落 success: %s", row.task.Status)
	}
}

// TestTaskWorkerReclaimsExpiredLease 租约过期的 processing 行会被下一轮重新领取（attempt+1）——
// 重启续跑的 worker 侧接线（真库的 SQL 语义由 task_db_test.go 断言）。
func TestTaskWorkerReclaimsExpiredLease(t *testing.T) {
	ledger := newFakeTaskLedger()
	executor := &fakeTaskExecutor{}
	id := ledger.seed(Task{
		TaskType: TaskTypeChat, Status: TaskStatusProcessing, Attempt: 1, RequestID: "rid-lease",
		BizType: "admin", Source: "admin:task", ProgressTotal: 1,
		Payload: mustTaskPayload(t, taskPayload{Model: "gpt-test", Messages: []Message{{Role: "user", Content: "hi"}},
			BizType: "admin", Source: "admin:task"}),
	})
	// 现场：上一进程死在执行中，租约已过期
	ledger.mutate(id, func(row *fakeTaskRow) {
		row.hasLease = true
		row.lockedUntil = time.Now().Add(-time.Minute)
	})

	worker := newTestTaskWorker(ledger, executor, 3)
	claimed, err := worker.RunOnce(context.Background())
	if err != nil || !claimed {
		t.Fatalf("过期租约的行应被重领: claimed=%v err=%v", claimed, err)
	}
	row := ledger.snapshot(id)
	if row.task.Attempt != 2 {
		t.Fatalf("重领后 attempt 应为 2（attempt+1），实际 %d", row.task.Attempt)
	}
	if row.task.Status != TaskStatusSuccess {
		t.Fatalf("重领后应正常跑完: %s（error=%s）", row.task.Status, row.task.Error)
	}
	if !row.lockedUntil.IsZero() && row.hasLease {
		t.Fatal("成功后必须释放租约（不得留下幽灵 processing）")
	}
	if calls := executor.chatInputs(); len(calls) != 1 || calls[0].RequestID != "task-rid-lease-a2" {
		t.Fatalf("重领的 usage id 应带新的尝试序号: %+v", calls)
	}
}

// TestTaskWorkerStartupResetThenReclaims 启动复位：租约过期的 processing 行被退回 pending，
// 且**下一轮**即可重新领取（attempt+1）——重启续跑的完整链路（真库 SQL 语义见 task_db_test.go）。
func TestTaskWorkerStartupResetThenReclaims(t *testing.T) {
	ledger := newFakeTaskLedger()
	executor := &fakeTaskExecutor{}
	id := ledger.seed(Task{
		TaskType: TaskTypeChat, Status: TaskStatusProcessing, Attempt: 1, RequestID: "rid-reset",
		BizType: "admin", Source: "admin:task", ProgressTotal: 1,
		Payload: mustTaskPayload(t, taskPayload{Model: "gpt-test", Messages: []Message{{Role: "user", Content: "hi"}},
			BizType: "admin", Source: "admin:task"}),
	})
	ledger.mutate(id, func(row *fakeTaskRow) {
		row.hasLease = true
		row.lockedUntil = time.Now().Add(-time.Minute)
	})
	worker := newTestTaskWorker(ledger, executor, 3)

	reset, err := worker.ResetStaleLeases(context.Background())
	if err != nil || reset != 1 {
		t.Fatalf("启动复位应退回 1 行: reset=%d err=%v", reset, err)
	}
	row := ledger.snapshot(id)
	if row.task.Status != TaskStatusPending || row.hasLease {
		t.Fatalf("复位后应为 pending 且无租约: status=%s hasLease=%v", row.task.Status, row.hasLease)
	}
	if row.task.Attempt != 1 {
		t.Fatalf("复位不得改 attempt（attempt 由领取 +1）：%d", row.task.Attempt)
	}

	claimed, err := worker.RunOnce(context.Background())
	if err != nil || !claimed {
		t.Fatalf("复位后下一轮应重新领取: claimed=%v err=%v", claimed, err)
	}
	row = ledger.snapshot(id)
	if row.task.Attempt != 2 || row.task.Status != TaskStatusSuccess {
		t.Fatalf("重领后 attempt+1 并跑完: attempt=%d status=%s", row.task.Attempt, row.task.Status)
	}
}

// TestTaskWorkerSettingsReadFailureFallsBackToDefaults 配置读失败用缺省继续跑（不因一次读失败停摆），
// 且缺省尝试次数确实是 3（同一失败任务要跑满三轮才置 failed）。
func TestTaskWorkerSettingsReadFailureFallsBackToDefaults(t *testing.T) {
	ledger := newFakeTaskLedger()
	executor := &fakeTaskExecutor{}
	id := ledger.seed(Task{
		TaskType: TaskTypeChat, Status: TaskStatusPending, RequestID: "rid-defaults", ProgressTotal: 1,
		Payload: mustTaskPayload(t, taskPayload{Model: "gpt-test", Messages: []Message{{Role: "user", Content: "hi"}},
			BizType: "admin", Source: "admin:task"}),
	})
	worker := newTestTaskWorker(ledger, executor, 3)
	worker.settings = func(context.Context) (taskSettings, error) { return taskSettings{}, errors.New("配置表不可读") }

	for round := 0; round < 1; round++ {
		claimed, err := worker.RunOnce(context.Background())
		if err != nil || !claimed {
			t.Fatalf("配置读失败不应阻断本轮: claimed=%v err=%v", claimed, err)
		}
	}
	if row := ledger.snapshot(id); row.task.Status != TaskStatusSuccess {
		t.Fatalf("配置读失败应回落缺省并照常执行: %s", row.task.Status)
	}

	// 缺省尝试次数口径（纯函数侧同源）
	if DefaultTaskMaxAttempts != 3 || DefaultTaskPollSeconds != 5 {
		t.Fatalf("缺省口径被改动: attempts=%d poll=%d", DefaultTaskMaxAttempts, DefaultTaskPollSeconds)
	}
}

// TestTaskWorkerLeaseIsPassedToLedger 领取时把租约交给账本（租约过期救援的前提）。
func TestTaskWorkerLeaseIsPassedToLedger(t *testing.T) {
	ledger := newFakeTaskLedger()
	executor := &fakeTaskExecutor{}
	ledger.seed(Task{
		TaskType: TaskTypeChat, Status: TaskStatusPending, RequestID: "rid-lease-pass", ProgressTotal: 1,
		Payload: mustTaskPayload(t, taskPayload{Model: "gpt-test", Messages: []Message{{Role: "user", Content: "hi"}},
			BizType: "admin", Source: "admin:task"}),
	})
	worker := newTestTaskWorker(ledger, executor, 3)
	worker.lease = 4 * time.Minute

	if _, err := worker.RunOnce(context.Background()); err != nil {
		t.Fatalf("领取失败: %v", err)
	}
	// 断言「领取时把 worker.lease 交给账本」——终态会释放租约，故只能看领取面收到的值。
	if got := ledger.claimedLease(); got != 4*time.Minute {
		t.Fatalf("领取时应把 worker.lease 交给账本，实际 %v", got)
	}
}
