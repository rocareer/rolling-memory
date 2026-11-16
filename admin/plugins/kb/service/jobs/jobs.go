// plugin: kb v1.0.0
// Package jobs 是本插件的**最小作业面**：kind 注册表 + 入队（同键单飞）+ 领取/执行/重试循环。
//
// 为什么自带：宿主没有供插件复用的作业面——插件的表由插件自管迁移建（独立版本表）、插件不得写宿主
// 自管表，因此本插件自带一张账本表（model.Job / 迁移 6_jobs）与一个 worker，语义与调用面照着
// 业务代码既有的用法设计（调用面保持同名同签名，业务侧零改动即接入）。
//
// 契约（逐条）：
//   - **同键单飞**：同 key 至多一条活跃行（pending/running），由账本表的部分唯一索引承担，
//     不靠「先查后插」；并发入队同键时恰好一个 `created=true`，其余 `created=false` 并返回既有行。
//   - **at-least-once**：handler 必须可被重复调用而不破坏语义（租约到期/进程重启会重跑）；
//     业务侧靠产物键（blob sha256 / 提取物 uk / 切片 uk / 向量 uk）续跑，不靠「跑没跑过」。
//   - **租约**：领取即 `attempt+1` 并置 `locked_until`；租约到期（含进程崩溃遗留的 running 行）
//     可被本进程或其它进程重领，attempt 累加。
//   - **重试上限**：`attempt >= max_attempts` 仍失败 ⇒ 落 `failed`（不再被领取）；未到上限 ⇒
//     退避重入 `pending`（`run_at = now() + backoff`）。
//   - **停用即停跑**：worker 每轮自查 `admin_plugins.status == 'enabled'`（直读台账）；
//     非 enabled 时本轮不领活，pending 行原样留在原状态。
//   - **未注册 kind 不领**：领取 SQL 按「本进程已注册 kind 集」过滤 ⇒ 未注册的 kind 留 pending
//     等有实现的进程来认领（不静默丢弃、不误执行）。
//   - **进度单调**：`Progress` 的 done 不回退（SQL 侧 GREATEST 保证）。
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"
)

// 作业队列缺省参数（无配置项：本插件不把作业面参数开放给运维改——改它属改文件重装）
const (
	// DefaultMaxAttempts 单作业最大尝试次数缺省
	DefaultMaxAttempts = 3
	// DefaultPollInterval worker 轮询间隔
	DefaultPollInterval = 2 * time.Second
	// DefaultLease 租约时长（领取后未续租即可被重领；续租间隔 = Lease/3）
	DefaultLease = 5 * time.Minute
	// DefaultBackoffBase 退避基数（backoff = base * 2^(attempt-1)，上限 maxBackoff）
	DefaultBackoffBase = 30 * time.Second
	// maxBackoff 退避上限
	maxBackoff = 30 * time.Minute
	// maxReasonLen 失败原因落库上限（列 varchar(512)）
	maxReasonLen = 480
)

// 存储层错误（可用 errors.Is 判定）
var (
	// ErrNotFound 作业不存在
	ErrNotFound = errors.New("作业不存在")
	// ErrInvalidInput 入参不合法（kind 为空等）
	ErrInvalidInput = errors.New("作业入参不合法")
)

// Handler 作业处理器（业务侧按 kind 注册；Run 必须可被重复调用）
type Handler interface {
	Run(ctx context.Context, j *Job) error
}

// Job 出队视图（handler 侧：入参读取 + 进度/结果上报）
type Job struct {
	ID      uint64
	Kind    string
	Key     string
	Params  []byte // 入参 JSON（原样）
	Attempt int    // 本次是第几次尝试（含本次）
	Total   int64  // 上次落库的进度总数
	Done    int64  // 上次落库的进度已完成

	store  *Store
	result []byte
}

// Progress 上报进度（写账本；done 单调不回退——SQL 侧取 GREATEST）。
// ctx 已取消（停机）时返回错误，供长作业协作停止。
func (j *Job) Progress(ctx context.Context, done, total int64) error {
	if j == nil {
		return ErrInvalidInput
	}
	j.Done, j.Total = done, total
	if j.store == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return j.store.updateProgress(ctx, j.ID, done, total)
}

// Result 记录结果（成功时随账本完成一并落库）
func (j *Job) Result(data []byte) {
	if j == nil {
		return
	}
	j.result = data
}

// ── 注册表（kind → Handler；进程内唯一，init 期注册）────────────────────────────

var (
	registryMu sync.RWMutex
	registry   = map[string]Handler{}
)

// Register 注册作业处理器（重复注册 = 编程错误，panic 响亮失败）
func Register(kind string, h Handler) {
	kind = strings.TrimSpace(kind)
	if kind == "" {
		panic("jobs: kind 不能为空")
	}
	if h == nil {
		panic("jobs: handler 不能为空（kind=" + kind + "）")
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, ok := registry[kind]; ok {
		panic("jobs: kind 重复注册: " + kind)
	}
	registry[kind] = h
}

// Registered 已注册 kind 清单（有序；领取 SQL 按此过滤）
func Registered() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	kinds := make([]string, 0, len(registry))
	for k := range registry {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	return kinds
}

// handlerOf 取 kind 对应处理器
func handlerOf(kind string) (Handler, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	h, ok := registry[kind]
	return h, ok
}

// EnqueueInput 入队参数
type EnqueueInput struct {
	Kind        string    // 作业类型（业务侧注册名）
	Key         string    // 幂等键（空 = 不去重）
	Params      any       // 入参（序列化为 JSON；nil = 不写入）
	RunAt       time.Time // 可执行时间（零值 = 立即）
	MaxAttempts int       // 最大尝试次数（<=0 = 缺省 3）
}

// backoff 退避时长：base * 2^(attempt-1)，上限 maxBackoff（纯函数）
func backoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	d := DefaultBackoffBase
	for i := 1; i < attempt && d < maxBackoff; i++ {
		d *= 2
	}
	if d > maxBackoff {
		d = maxBackoff
	}
	return d
}

// leaseRenewInterval 续租间隔（Lease/3：远密于租约，避免仍在跑的作业被重领）
func leaseRenewInterval(lease time.Duration) time.Duration {
	d := lease / 3
	if d < 10*time.Second {
		d = 10 * time.Second
	}
	return d
}

// encodeParams 入参序列化（nil = 不写；非 nil 必须可序列化）
func encodeParams(v any) (*string, error) {
	if v == nil {
		return nil, nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	text := string(raw)
	return &text, nil
}

// trimReason 失败原因截断（按 rune；列 varchar(512)）
func trimReason(s string) string {
	rs := []rune(strings.TrimSpace(s))
	if len(rs) <= maxReasonLen {
		return string(rs)
	}
	return string(rs[:maxReasonLen]) + "…"
}

// ensureTable 账本表存在性（迁移未应用时给一句可读的提示，而不是裸 SQL 错）
func ensureTable(ctx context.Context, s *Store) error {
	if s == nil || s.DB() == nil {
		return errors.New("jobs: 数据库未初始化")
	}
	return nil
}
