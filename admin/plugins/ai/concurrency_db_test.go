// plugin: ai v0.2.0
// concurrency_db_test.go 是真库并发验收（DSN 门控 + 真 Postgres + 多协程）：
// 把「并发下只会发生一次」「计数在并发下不丢」两类声明放到**真库**上验收——
// 行锁 + 条件更新（CAS）+ 单条 UPDATE 原子自增的真实语义只有真 Postgres 能给，
// 内存假库（circuit_test.go 的 fakeCircuitStore 用互斥锁「模拟」原子性）验不到。
//
// 用法（**必须指向临时库**：用例会 drop/重建本插件的全部 plugin_ai_* 表，并在收尾 drop 掉
// 用例自建的宿主 configs 表）：
//
//	PLUGIN_AI_TEST_DSN='postgres://用户:口令@主机:端口/临时库?sslmode=disable' \
//	  go test -count=1 -run TestConcurrent -v ./plugins/ai/
//
// 未设 DSN ⇒ 整组跳过（默认 `go test ./plugins/ai/` 不连库、不需要 Postgres）。
// 本文件**只新增**：不改 store_db_test.go 的 pluginAITestDB（复用其清场 + 按编号升序应用
// migrations/*.up.sql 的脚手架），也不改任何既有用例与断言。
//
// 覆盖面（逐条对应验收项）：
//
//	① 熔断 CAS 单探针：冷却到期的 open 渠道 N 协程并发 Enter ⇒ 恰 1 个当选（真库条件更新），
//	   且「拿着旧快照的后到者」不得当选（CAS 是状态条件更新，不是先到先得）；
//	② 探针卡死重选：half_open 且 opened_at 超冷却 ⇒ N 协程并发恰 1 个当选；
//	③ 探针在飞：half_open 且 opened_at 新鲜 ⇒ 全部拒绝、零写库；
//	④ closed 快路径：N 协程并发 Enter 全部放行且**一次库写都没有**；
//	⑤ 计数原子性：MarkFailure 并发自增的 RETURNING 值恰是 1..N 的一个排列（丢计数会被抓），
//	   落库 consecutive_failures == N；
//	⑥ 跳闸唯一：64 并发失败只写 1 条 open 事件、opened_at 记一次；
//	⑦ 渠道累计：total_calls/total_tokens/total_failures 与 TPM 扣减（TokenCharger）在并发下不丢；
//	⑧ 限流并发（限流层）：按 Key 覆盖（真库 plugin_ai_api_key.rate_limit）与
//	   插件全局三键（真库 configs + 生产 loadRateLimitConfig 读出口）两条链上的并发不超卖，
//	   以及在飞上限（max_inflight）的并发精确性 + 429 形状 + 「429 不进业务 handler」。
package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ai-go-hub/ai-go-admin/internal/infra/config"
	"github.com/ai-go-hub/ai-go-admin/internal/infra/database"
	"github.com/ai-go-hub/ai-go-admin/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ===== 脚手架（本文件符号一律 conc 前缀，与同包其它测试文件不重名）=====

// concWorkers 是并发度：足以覆盖真库的行锁竞争，又不超过本地 PG 的连接余量。
const concWorkers = 32

// concRun 让 n 个协程在同一栅栏后同时起跑（真并发，不用 sleep 凑时序）。
func concRun(n int, fn func(i int)) {
	var start, done sync.WaitGroup
	start.Add(1)
	for i := 0; i < n; i++ {
		done.Add(1)
		go func(i int) {
			defer done.Done()
			start.Wait()
			fn(i)
		}(i)
	}
	start.Done()
	done.Wait()
}

// concSandboxDB 打开临时库（复用 store_db_test.go 的 pluginAITestDB：清场 + 按编号升序建表），
// 并把连接池设闸——多协程用例必须自己设上限，否则瞬时并发会顶到共享 PG 的 max_connections；
// 池的**空闲**上限同样压小：pluginAITestDB 每个用例新开一个句柄，一度不 Close、空闲连接随用例数
// 累积（首轮实测：24 空闲 × 6 个用例就把 PG 的 100 条打满 ⇒ 53300）；该句柄现已在 t.Cleanup 里
// Close（收口），本闸是并发期内的第二道保险。
func concSandboxDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := pluginAITestDB(t)
	concLimitPool(t, db)
	return db
}

// concLimitPool 给 *gorm.DB 的连接池设并发上限（open=16 / idle=2，理由见 concSandboxDB）。
func concLimitPool(t *testing.T, db *gorm.DB) {
	t.Helper()
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("取 sql.DB 失败: %v", err)
	}
	sqlDB.SetMaxOpenConns(16)
	sqlDB.SetMaxIdleConns(2)
}

// concSeedChannel 建一条渠道行并返回（id 由 DB 回填）。
func concSeedChannel(t *testing.T, store *Store, row Channel) Channel {
	t.Helper()
	if err := store.CreateChannel(context.Background(), &row); err != nil {
		t.Fatalf("建渠道 %q 失败: %v", row.Name, err)
	}
	if row.ID == 0 {
		t.Fatalf("渠道主键未回填: %+v", row)
	}
	return row
}

// concLoadChannel 读回渠道行（并发后的落库状态判据全落在它身上）。
func concLoadChannel(t *testing.T, store *Store, id int64) Channel {
	t.Helper()
	row, err := store.GetChannel(context.Background(), id)
	if err != nil {
		t.Fatalf("读回渠道 %d 失败: %v", id, err)
	}
	return *row
}

// concCountEvents 数该渠道的某类熔断事件条数。
func concCountEvents(t *testing.T, store *Store, channelID int64, event string) int {
	t.Helper()
	events, err := store.ListBreakerEvents(context.Background(), channelID, MaxListLimit)
	if err != nil {
		t.Fatalf("读熔断事件失败: %v", err)
	}
	count := 0
	for _, row := range events {
		if row.Event == event {
			count++
		}
	}
	return count
}

// ===== ① 熔断 CAS 单探针（真库）=====

// TestConcurrentCircuitCASSingleProbeDB：冷却到期（open 且 now-opened_at ≥ cooldown）的渠道，
// N 协程并发 Enter ⇒ 恰 1 个当选唯一半开探针（store.EnterCAS 的单条条件更新），
// 且「拿着同一份旧快照的后到者」不得当选——CAS 是 circuit='open' 的状态条件更新（store.go:454），
// 不是先到先得；已停用（status=0）的渠道也不得当选（同一条 WHERE 的 status 条件）。
func TestConcurrentCircuitCASSingleProbeDB(t *testing.T) {
	db := concSandboxDB(t)
	store := NewStore(db)
	breaker := NewCircuit(store)
	ctx := context.Background()
	now := nowUnix()

	// 冷却 60s 已过：opened_at = now-61 ⇒ Decide ⇒ DecisionProbe。
	row := concSeedChannel(t, store, Channel{
		Name: "conc-cas", BaseURL: "https://example.invalid/v1", Status: StatusEnabled,
		Circuit: CircuitOpen, OpenedAt: ptrInt64(now - 61), CooldownSeconds: 60, FailThreshold: 5,
	})
	snapshot := row // 各协程共用同一份快照（真实调度里多个 goroutine 会各持一份「我看到的是 open」）

	var winners atomic.Int64
	var errMu sync.Mutex
	var errs []error
	concRun(concWorkers, func(int) {
		ad, err := breaker.Enter(ctx, snapshot, now)
		if err != nil {
			errMu.Lock()
			errs = append(errs, err)
			errMu.Unlock()
			return
		}
		if ad.Allowed {
			winners.Add(1)
		}
	})
	if len(errs) > 0 {
		t.Fatalf("并发 Enter 出错: %v", errs)
	}
	if got := winners.Load(); got != 1 {
		t.Fatalf("冷却到期的 open 渠道并发 Enter 应恰 1 个探针当选，实际 %d", got)
	}
	after := concLoadChannel(t, store, row.ID)
	if after.Circuit != CircuitHalfOpen || after.OpenedAt == nil || *after.OpenedAt != now {
		t.Fatalf("当选后落库状态应为 half_open/opened_at=%d，实际 circuit=%q opened_at=%v",
			now, after.Circuit, after.OpenedAt)
	}

	// 第二波：仍拿旧快照（自称 open）⇒ WHERE circuit='open' 已不命中，全部不得当选。
	var lateWinners atomic.Int64
	concRun(concWorkers, func(int) {
		ad, err := breaker.Enter(ctx, snapshot, now)
		if err != nil {
			errMu.Lock()
			errs = append(errs, err)
			errMu.Unlock()
			return
		}
		if ad.Allowed {
			lateWinners.Add(1)
		}
	})
	if len(errs) > 0 {
		t.Fatalf("第二波并发 Enter 出错: %v", errs)
	}
	if got := lateWinners.Load(); got != 0 {
		t.Fatalf("状态已迁到 half_open 后，拿旧 open 快照的后到者不得当选，实际 %d 个", got)
	}
	after = concLoadChannel(t, store, row.ID)
	if after.Circuit != CircuitHalfOpen || after.OpenedAt == nil || *after.OpenedAt != now {
		t.Fatalf("第二波不得再改状态：实际 circuit=%q opened_at=%v", after.Circuit, after.OpenedAt)
	}

	// 停用渠道：冷却同样到期，但 EnterCAS 的 status=1 条件不满足 ⇒ 0 个当选、状态不动。
	// 注意停用行必须用显式 UPDATE 落 status=0：GORM 的 Create 会省略「带 default 标签且为零值」的
	// 字段（status 带 default:1 ⇒ Create 里的 0 被省略、落库成 1），这是 store_db_test.go:211
	// 登记过的既有 GORM 行为，不是本用例要验的对象。
	disabled := concSeedChannel(t, store, Channel{
		Name: "conc-cas-disabled", BaseURL: "https://example.invalid/v1", Status: StatusDisabled,
		Circuit: CircuitOpen, OpenedAt: ptrInt64(now - 61), CooldownSeconds: 60, FailThreshold: 5,
	})
	off := StatusDisabled
	if _, err := store.UpdateChannel(ctx, disabled.ID, ChannelPatch{Status: &off}); err != nil {
		t.Fatalf("停用渠道失败: %v", err)
	}
	if got := concLoadChannel(t, store, disabled.ID); got.Status != StatusDisabled {
		t.Fatalf("停用未落库（status=%d）：后续断言会失去判别力", got.Status)
	}
	disabled = concLoadChannel(t, store, disabled.ID)
	var disabledWinners atomic.Int64
	concRun(16, func(int) {
		ad, err := breaker.Enter(ctx, disabled, now)
		if err != nil {
			errMu.Lock()
			errs = append(errs, err)
			errMu.Unlock()
			return
		}
		if ad.Allowed {
			disabledWinners.Add(1)
		}
	})
	if len(errs) > 0 {
		t.Fatalf("停用渠道并发 Enter 出错: %v", errs)
	}
	if got := disabledWinners.Load(); got != 0 {
		t.Fatalf("停用渠道不得被选为探针（store.go:454 的 status=1 条件），实际 %d 个当选", got)
	}
	if got := concLoadChannel(t, store, disabled.ID); got.Circuit != CircuitOpen {
		t.Fatalf("停用渠道状态应保持 open，实际 %q", got.Circuit)
	}
}

// ===== ② 探针卡死重选（真库）=====

// TestConcurrentCircuitStaleProbeReselectDB：half_open 且 opened_at 早于 now-cooldown
// （进程被杀导致探针未收口）⇒ N 协程并发恰 1 个当选重选（store.ReselectStaleProbe）。
func TestConcurrentCircuitStaleProbeReselectDB(t *testing.T) {
	db := concSandboxDB(t)
	store := NewStore(db)
	breaker := NewCircuit(store)
	ctx := context.Background()
	now := nowUnix()

	row := concSeedChannel(t, store, Channel{
		Name: "conc-reselect", BaseURL: "https://example.invalid/v1", Status: StatusEnabled,
		Circuit: CircuitHalfOpen, OpenedAt: ptrInt64(now - 61), CooldownSeconds: 60, FailThreshold: 5,
	})
	snapshot := row

	var winners atomic.Int64
	var errMu sync.Mutex
	var errs []error
	concRun(concWorkers, func(int) {
		ad, err := breaker.Enter(ctx, snapshot, now)
		if err != nil {
			errMu.Lock()
			errs = append(errs, err)
			errMu.Unlock()
			return
		}
		if ad.Allowed {
			winners.Add(1)
		}
	})
	if len(errs) > 0 {
		t.Fatalf("并发重选出错: %v", errs)
	}
	if got := winners.Load(); got != 1 {
		t.Fatalf("卡死探针重选应恰 1 个当选，实际 %d", got)
	}
	after := concLoadChannel(t, store, row.ID)
	if after.Circuit != CircuitHalfOpen || after.OpenedAt == nil || *after.OpenedAt != now {
		t.Fatalf("重选后 opened_at 应刷成 %d，实际 circuit=%q opened_at=%v", now, after.Circuit, after.OpenedAt)
	}
}

// ===== ③ 探针在飞 / ④ closed 快路径 =====

// TestConcurrentCircuitInflightProbeDeniesDB：half_open 且 opened_at 新鲜（探针在飞）⇒
// N 协程并发全部拒绝，且一次库写都没有（opened_at 不变、零熔断事件）。
func TestConcurrentCircuitInflightProbeDeniesDB(t *testing.T) {
	db := concSandboxDB(t)
	store := NewStore(db)
	breaker := NewCircuit(store)
	ctx := context.Background()
	now := nowUnix()

	row := concSeedChannel(t, store, Channel{
		Name: "conc-inflight", BaseURL: "https://example.invalid/v1", Status: StatusEnabled,
		Circuit: CircuitHalfOpen, OpenedAt: ptrInt64(now), CooldownSeconds: 60, FailThreshold: 5,
	})
	snapshot := row

	var winners atomic.Int64
	var errMu sync.Mutex
	var errs []error
	concRun(concWorkers, func(int) {
		ad, err := breaker.Enter(ctx, snapshot, now)
		if err != nil {
			errMu.Lock()
			errs = append(errs, err)
			errMu.Unlock()
			return
		}
		if ad.Allowed {
			winners.Add(1)
		}
	})
	if len(errs) > 0 {
		t.Fatalf("探针在飞并发 Enter 出错: %v", errs)
	}
	if got := winners.Load(); got != 0 {
		t.Fatalf("探针在飞期间不得有第二个探针（冷却内拒绝），实际 %d 个放行", got)
	}
	after := concLoadChannel(t, store, row.ID)
	if after.OpenedAt == nil || *after.OpenedAt != now {
		t.Fatalf("拒绝路径不得改 opened_at，实际 %v", after.OpenedAt)
	}
	if got := concCountEvents(t, store, row.ID, EventOpen) + concCountEvents(t, store, row.ID, EventClose); got != 0 {
		t.Fatalf("拒绝路径不得写熔断事件，实际 %d 条", got)
	}
}

// TestConcurrentCircuitClosedAllowsWithoutWriteDB：稳态 closed 渠道 N 协程并发 Enter 全部放行，
// 且 update_time 纹丝不动（把 update_time 预置成一小时前 ⇒ 任何一次库写都会把它推到 now）。
func TestConcurrentCircuitClosedAllowsWithoutWriteDB(t *testing.T) {
	db := concSandboxDB(t)
	store := NewStore(db)
	breaker := NewCircuit(store)
	ctx := context.Background()
	now := nowUnix()

	row := concSeedChannel(t, store, Channel{
		Name: "conc-closed", BaseURL: "https://example.invalid/v1", Status: StatusEnabled,
		Circuit: CircuitClosed, CooldownSeconds: 60, FailThreshold: 5,
	})
	stale := now - 3600
	if err := db.Exec(`UPDATE `+quoteIdent(table("plugin_ai_channel"))+` SET "update_time" = ? WHERE "id" = ?`,
		stale, row.ID).Error; err != nil {
		t.Fatalf("预置 update_time 失败: %v", err)
	}
	snapshot := concLoadChannel(t, store, row.ID)

	var allowed atomic.Int64
	var notClosed atomic.Int64
	var errMu sync.Mutex
	var errs []error
	concRun(concWorkers, func(int) {
		ad, err := breaker.Enter(ctx, snapshot, now)
		if err != nil {
			errMu.Lock()
			errs = append(errs, err)
			errMu.Unlock()
			return
		}
		if ad.Allowed {
			allowed.Add(1)
		}
		if ad.Circuit != CircuitClosed {
			notClosed.Add(1)
		}
	})
	if len(errs) > 0 {
		t.Fatalf("closed 并发 Enter 出错: %v", errs)
	}
	if got := allowed.Load(); got != concWorkers {
		t.Fatalf("closed 渠道应全部放行（%d），实际 %d", concWorkers, got)
	}
	if got := notClosed.Load(); got != 0 {
		t.Fatalf("closed 渠道的准入状态应为 closed，实际 %d 个不是", got)
	}
	if got := concLoadChannel(t, store, row.ID).UpdateTime; got != stale {
		t.Fatalf("closed 快路径不得写库（update_time 应停在 %d），实际 %d", stale, got)
	}
}

// ===== ⑤ 计数原子性（真库）=====

// TestConcurrentMarkFailureAtomicDB：N 协程并发 store.MarkFailure ⇒
// ① RETURNING 返回的自增值恰是 1..N 的一个排列（任何「先读后写」都会产生重复值/丢计数）；
// ② 落库 consecutive_failures == N（丢一次就 < N）。
func TestConcurrentMarkFailureAtomicDB(t *testing.T) {
	db := concSandboxDB(t)
	store := NewStore(db)
	ctx := context.Background()
	now := nowUnix()

	row := concSeedChannel(t, store, Channel{
		Name: "conc-mark-failure", BaseURL: "https://example.invalid/v1", Status: StatusEnabled,
		Circuit: CircuitClosed, CooldownSeconds: 60, FailThreshold: 5,
	})

	values := make([]int, concWorkers)
	var errMu sync.Mutex
	var errs []error
	concRun(concWorkers, func(i int) {
		got, err := store.MarkFailure(ctx, uint64(row.ID), "boom", now)
		if err != nil {
			errMu.Lock()
			errs = append(errs, err)
			errMu.Unlock()
			return
		}
		values[i] = got
	})
	if len(errs) > 0 {
		t.Fatalf("并发 MarkFailure 出错: %v", errs)
	}
	sorted := append([]int(nil), values...)
	sort.Ints(sorted)
	for i, got := range sorted {
		if got != i+1 {
			t.Fatalf("并发自增的 RETURNING 值应是 1..%d 的排列，第 %d 位是 %d（全部 %v）",
				concWorkers, i+1, got, sorted)
		}
	}
	after := concLoadChannel(t, store, row.ID)
	if after.ConsecutiveFailures != concWorkers {
		t.Fatalf("落库 consecutive_failures = %d，期望 %d（丢计数）", after.ConsecutiveFailures, concWorkers)
	}
	if after.LastError != "boom" {
		t.Fatalf("last_error = %q，期望 boom", after.LastError)
	}
}

// TestConcurrentOnFailureTripOnceDB：阈值 8、64 协程并发真失败 ⇒
// ① 连续失败计数仍恰为 64（跳闸后也继续自增，不丢）；② circuit=open、opened_at=now（记一次）；
// ③ 熔断流水恰 1 条 open 事件（store.OpenCircuit 的 WHERE circuit<>'open' 在真库行锁下的唯一性）。
func TestConcurrentOnFailureTripOnceDB(t *testing.T) {
	db := concSandboxDB(t)
	store := NewStore(db)
	breaker := NewCircuit(store)
	ctx := context.Background()
	now := nowUnix()

	row := concSeedChannel(t, store, Channel{
		Name: "conc-trip-once", BaseURL: "https://example.invalid/v1", Status: StatusEnabled,
		Circuit: CircuitClosed, CooldownSeconds: 60, FailThreshold: 8,
	})
	snapshot := row // 全部协程都以为自己是 closed（真实并发下的同一时刻快照）

	var errMu sync.Mutex
	var errs []error
	concRun(64, func(int) {
		if err := breaker.OnFailure(ctx, snapshot, "上游 500", now); err != nil {
			errMu.Lock()
			errs = append(errs, err)
			errMu.Unlock()
		}
	})
	if len(errs) > 0 {
		t.Fatalf("并发 OnFailure 出错: %v", errs)
	}
	after := concLoadChannel(t, store, row.ID)
	if after.ConsecutiveFailures != 64 {
		t.Fatalf("落库 consecutive_failures = %d，期望 64", after.ConsecutiveFailures)
	}
	if after.Circuit != CircuitOpen {
		t.Fatalf("达到阈值后应跳闸 open，实际 %q", after.Circuit)
	}
	if after.OpenedAt == nil || *after.OpenedAt != now {
		t.Fatalf("跳闸应记 opened_at=%d，实际 %v", now, after.OpenedAt)
	}
	if got := concCountEvents(t, store, row.ID, EventOpen); got != 1 {
		t.Fatalf("并发跳闸应恰写 1 条 open 事件，实际 %d 条", got)
	}
	if got := concCountEvents(t, store, row.ID, EventClose); got != 0 {
		t.Fatalf("失败路径不得写 close 事件，实际 %d 条", got)
	}
}

// TestConcurrentOnSuccessClosesCircuitDB：N 协程并发持半开快照做真实成功 ⇒
// 收口状态必须收敛到 closed / 连续失败清零 / opened_at=NULL（MarkSuccess 的条件更新在真库并发下不丢），
// 并至少留下 1 条 close 事件（恢复迁移被审计）。
//
// 事件条数在本夹具里会 >1（N 个协程都自称半开）：这是**不可达的生产态**（CAS 保证同一时刻只有一个
// 探针，见 ① 的断言），故条数只记录、不断言唯一——断言的是状态收敛与「至少写了一条」。
func TestConcurrentOnSuccessClosesCircuitDB(t *testing.T) {
	db := concSandboxDB(t)
	store := NewStore(db)
	breaker := NewCircuit(store)
	ctx := context.Background()
	now := nowUnix()

	row := concSeedChannel(t, store, Channel{
		Name: "conc-success-close", BaseURL: "https://example.invalid/v1", Status: StatusEnabled,
		Circuit: CircuitHalfOpen, OpenedAt: ptrInt64(now - 30), ConsecutiveFailures: 3,
		CooldownSeconds: 60, FailThreshold: 5,
	})
	snapshot := row

	var errMu sync.Mutex
	var errs []error
	concRun(concWorkers, func(int) {
		if err := breaker.OnSuccess(ctx, snapshot, now); err != nil {
			errMu.Lock()
			errs = append(errs, err)
			errMu.Unlock()
		}
	})
	if len(errs) > 0 {
		t.Fatalf("并发 OnSuccess 出错: %v", errs)
	}
	after := concLoadChannel(t, store, row.ID)
	if after.Circuit != CircuitClosed || after.ConsecutiveFailures != 0 || after.OpenedAt != nil {
		t.Fatalf("并发成功收口后应为 closed/0/NULL，实际 circuit=%q failures=%d opened_at=%v",
			after.Circuit, after.ConsecutiveFailures, after.OpenedAt)
	}
	if after.LastSuccessAt == nil || *after.LastSuccessAt != now {
		t.Fatalf("收口应写心跳 last_success_at=%d，实际 %v", now, after.LastSuccessAt)
	}
	closeEvents := concCountEvents(t, store, row.ID, EventClose)
	if closeEvents < 1 {
		t.Fatalf("恢复迁移应至少留 1 条 close 事件，实际 %d 条", closeEvents)
	}
	t.Logf("半开快照并发成功 %d 次的 close 事件条数 = %d（生产态由 CAS 保证只有 1 个探针）",
		concWorkers, closeEvents)
	if openEvents := concCountEvents(t, store, row.ID, EventOpen); openEvents != 0 {
		t.Fatalf("成功路径不得写 open 事件，实际 %d 条", openEvents)
	}
}

// ===== ⑦ 渠道累计 + TPM 扣减（真库）=====

// concCharger 是 TokenCharger 的并发安全探针（对应生产侧 ChannelGate 的 TPM 窗口扣减）。
type concCharger struct {
	mu     sync.Mutex
	calls  int
	tokens int
}

func (c *concCharger) Charge(_ uint64, tokens int) {
	c.mu.Lock()
	c.calls++
	c.tokens += tokens
	c.mu.Unlock()
}

func (c *concCharger) snapshot() (int, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls, c.tokens
}

// TestConcurrentRecordTotalsAtomicDB：64 次成功（每次 7 token）+ 32 次失败并发混跑 ⇒
// total_calls=64、total_tokens=448、total_failures=32（gorm.Expr 的 SQL 侧自增不丢），
// 且 TPM 扣减探针与写库同量（RecordTotals 先扣后写，两条路径都不得漏）。
func TestConcurrentRecordTotalsAtomicDB(t *testing.T) {
	db := concSandboxDB(t)
	store := NewStore(db)
	breaker := NewCircuit(store)
	charger := &concCharger{}
	breaker.charger = charger
	ctx := context.Background()
	now := nowUnix()

	const successes, failures, tokensPerCall = 64, 32, 7
	row := concSeedChannel(t, store, Channel{
		Name: "conc-totals", BaseURL: "https://example.invalid/v1", Status: StatusEnabled,
		Circuit: CircuitClosed, CooldownSeconds: 60, FailThreshold: 5,
	})

	var errMu sync.Mutex
	var errs []error
	concRun(successes+failures, func(i int) {
		var err error
		if i < successes {
			err = breaker.RecordTotals(ctx, uint64(row.ID), true, tokensPerCall, now)
		} else {
			err = breaker.RecordTotals(ctx, uint64(row.ID), false, 0, now)
		}
		if err != nil {
			errMu.Lock()
			errs = append(errs, err)
			errMu.Unlock()
		}
	})
	if len(errs) > 0 {
		t.Fatalf("并发 RecordTotals 出错: %v", errs)
	}
	after := concLoadChannel(t, store, row.ID)
	if after.TotalCalls != successes {
		t.Fatalf("total_calls = %d，期望 %d", after.TotalCalls, successes)
	}
	if after.TotalTokens != successes*tokensPerCall {
		t.Fatalf("total_tokens = %d，期望 %d", after.TotalTokens, successes*tokensPerCall)
	}
	if after.TotalFailures != failures {
		t.Fatalf("total_failures = %d，期望 %d", after.TotalFailures, failures)
	}
	chargedCalls, chargedTokens := charger.snapshot()
	if chargedCalls != successes || chargedTokens != successes*tokensPerCall {
		t.Fatalf("TPM 扣减应恰 %d 次 / %d token（与写库同量），实际 %d 次 / %d token",
			successes, successes*tokensPerCall, chargedCalls, chargedTokens)
	}
}

// ===== ⑧ 限流并发（真库 configs/plugin_ai_api_key + 生产读出口）=====

// concRLClock 是限流层的冻结时钟：桶在整段并发里不补（判定确定，不靠真实时间差不 flake）。
var concRLClock = time.Date(2026, 10, 24, 8, 0, 0, 0, time.UTC)

// concEnsureHostConfigs 在临时库里建宿主的 configs 表（限流三键的读取面）。
// 用宿主自己的行模型建表 ⇒ 列与生产读取路径（ratelimit.go 的 readRateLimitConfigValues）同源；
// 收尾 drop，只碰临时库。
func concEnsureHostConfigs(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.AutoMigrate(&model.Config{}); err != nil {
		t.Fatalf("建 configs 表失败: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Exec(`DROP TABLE IF EXISTS ` + quoteIdent((model.Config{}).TableName())).Error; err != nil {
			t.Errorf("收尾 drop configs 失败: %v", err)
		}
	})
}

// concWriteRateLimitConfig 写限流三键（真库）+ 失效进程内缓存（等同 settings/save 的动作）：
// 不失效就吃到上一个用例缓存下来的值（缓存上限 5s），断言会失去判别力。
func concWriteRateLimitConfig(t *testing.T, db *gorm.DB, rpm, burst, maxInflight int) {
	t.Helper()
	values := map[string]int{
		configKeyRateLimitRPM:         rpm,
		configKeyRateLimitBurst:       burst,
		configKeyRateLimitMaxInflight: maxInflight,
	}
	for name, raw := range values {
		value := strconv.Itoa(raw)
		row := model.Config{Name: name, Group: "plugin.ai", Type: "text", Value: &value, AllowDel: 1}
		if err := db.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "name"}},
			DoUpdates: clause.AssignmentColumns([]string{"value"}),
		}).Create(&row).Error; err != nil {
			t.Fatalf("写限流配置 %s=%s 失败: %v", name, value, err)
		}
	}
	invalidateRateLimitConfig()
}

// concInitHostDB 把宿主的全局库句柄（database.DB()）指向**同一个临时库**：
// 生产读取面 readRateLimitConfigValues 只认 database.DB()，没有别的注入点。
//
// 护栏（先判后连，绝不在别的库上跑并发用例）：
//   - 用 PLUGIN_AI_TEST_DSN 反解出 host/port/user/dbname，写成 DATABASE_WRITE_* 环境变量
//     （config.Init 的 viper 读 env：SetEnvKeyReplacer("." → "_")，键 database.write.dbname）；
//   - config.Init 后**先核对** database.write.* 是否等于 DSN 目标；不等（配置已被他处初始化）
//     ⇒ 明确 skip，绝不改道去连别的库；
//   - database.Init 后再以 SELECT current_database() 复核一次，不一致直接红。
func concInitHostDB(t *testing.T) {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv(pluginAITestDSNEnv))
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("解析 %s 失败: %v", pluginAITestDSNEnv, err)
	}
	host := parsed.Hostname()
	port, _ := strconv.Atoi(parsed.Port())
	user := parsed.User.Username()
	password, _ := parsed.User.Password()
	wantDB := strings.TrimPrefix(parsed.Path, "/")
	if host == "" || port == 0 || wantDB == "" {
		t.Fatalf("%s 缺少 host/port/dbname: %q", pluginAITestDSNEnv, dsn)
	}

	t.Setenv("DATABASE_WRITE_HOST", host)
	t.Setenv("DATABASE_WRITE_PORT", strconv.Itoa(port))
	t.Setenv("DATABASE_WRITE_USER", user)
	t.Setenv("DATABASE_WRITE_PASSWORD", password)
	t.Setenv("DATABASE_WRITE_DBNAME", wantDB)

	if !config.Initialized() {
		if err := config.Init(); err != nil {
			t.Fatalf("初始化全局配置失败: %v", err)
		}
	}
	write := config.Get().Database.Write
	if write.Host != host || write.Port != port || write.DBName != wantDB {
		t.Skipf("本进程的全局配置已在他处初始化（database.write=%s:%d/%s ≠ 临时库 %s:%d/%s）："+
			"限流真库用例需单独运行（-run TestConcurrent）",
			write.Host, write.Port, write.DBName, host, port, wantDB)
	}
	if err := database.Init(); err != nil {
		t.Fatalf("初始化全局库句柄失败: %v", err)
	}
	if database.DB() == nil {
		t.Fatal("database.DB() 仍为 nil")
	}
	var current string
	if err := database.DB().Raw("SELECT current_database()").Row().Scan(&current); err != nil {
		t.Fatalf("探测全局句柄当前库失败: %v", err)
	}
	if current != wantDB {
		t.Fatalf("database.DB() 指向 %q，期望临时库 %q —— 拒绝在非临时库上跑并发用例", current, wantDB)
	}
	concLimitPool(t, database.DB())
}

// concUseProductionRateLimitLayer 把**生产**限流层装成单例：load 保持 newRateLimitLayer 的缺省
// （即 loadRateLimitConfig，走 database.DB() 真库 configs），只把时钟换成冻结时钟；收尾还原。
func concUseProductionRateLimitLayer(t *testing.T) *rateLimitLayer {
	t.Helper()
	layer := newRateLimitLayer()
	if layer.load == nil {
		t.Fatal("生产层缺省 load 为空（本用例假定它以 loadRateLimitConfig 为读出口）")
	}
	layer.now = func() time.Time { return concRLClock }
	previous := publicRateLimitLayer
	publicRateLimitLayer = layer
	t.Cleanup(func() { publicRateLimitLayer = previous })
	return layer
}

// concProbe 记录业务 handler 被调用的次数（「429 不进业务 handler」的判据载体）。
type concProbe struct {
	hits atomic.Int64
}

func (p *concProbe) handler(status int) gin.HandlerFunc {
	return func(c *gin.Context) {
		p.hits.Add(1)
		c.Status(status)
	}
}

func (p *concProbe) count() int { return int(p.hits.Load()) }

// concRateLimitEngine 造生产公开面链的引擎：apiKeyAuth（真 Store 查库 + 按 Key 覆盖）→
// rateLimit（生产中间件 + 上面装的生产层）→ 业务 handler。
func concRateLimitEngine(db *gorm.DB, set pluginSettings, handler gin.HandlerFunc) *gin.Engine {
	d := deps{
		loadSettings: func(context.Context) (pluginSettings, error) { return set, nil },
		loadStore:    func(context.Context) (*Store, error) { return NewStore(db), nil },
	}
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.POST("/ai/v1/chat/completions", d.apiKeyAuth(), d.rateLimit(), handler)
	return engine
}

// concDoRequest 发一次公开面请求（authorization 为完整头值）。
func concDoRequest(engine *gin.Engine, authorization string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/ai/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-conc","messages":[{"role":"user","content":"ping"}]}`))
	request.Header.Set("Content-Type", "application/json")
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)
	return recorder
}

// concAssert429Shape 断言 429 的形状逐字命中（与限流层文档口径一致），并按需匹配文案片段。
func concAssert429Shape(t *testing.T, recorder *httptest.ResponseRecorder, messagePart string) {
	t.Helper()
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("应为 429，实际 %d（body=%s）", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Retry-After"); got != "60" {
		t.Fatalf("Retry-After = %q，期望 60", got)
	}
	var body struct {
		Error struct {
			Type    string `json:"type"`
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("429 错误体不是 JSON: %v（原文 %s）", err, recorder.Body.String())
	}
	if body.Error.Type != "rate_limit_error" || body.Error.Code != "rate_limit_exceeded" {
		t.Fatalf("429 形状必须逐字命中：type=%q code=%q", body.Error.Type, body.Error.Code)
	}
	if messagePart != "" && !strings.Contains(body.Error.Message, messagePart) {
		t.Fatalf("429 文案应含 %q，实际 %q", messagePart, body.Error.Message)
	}
}

// TestConcurrentRateLimitPerKeyOverrideDB：全局三键皆 0（真库 configs）而 DB Key 的 rate_limit=10
// ⇒ 32 并发经「apiKeyAuth 查库 → 按 Key 覆盖 → 令牌桶」恰 10 个放行（桶容量 = **该 Key 自身**的
// rate_limit = 10；源对 DB Key 是 60s 固定窗口 limit=rate_limit，同窗口径），其余 22 个 429；
// 429 文案必须体现**按 Key 覆盖**的 10（而不是全局 0）⇒ 覆盖真的接管了上限。
func TestConcurrentRateLimitPerKeyOverrideDB(t *testing.T) {
	db := concSandboxDB(t)
	concEnsureHostConfigs(t, db)
	concInitHostDB(t)
	concWriteRateLimitConfig(t, db, 0, 0, 0) // 全局不限 ⇒ 生效上限只能来自 Key 覆盖
	concUseProductionRateLimitLayer(t)

	store := NewStore(db)
	ctx := context.Background()
	const plain = "sk-conc-perkey-0001"
	row := &APIKey{
		Name: "并发验收-按 Key 覆盖", KeyHash: HashAPIKey(plain), KeyPrefix: APIKeyDisplayPrefix(plain),
		Source: "conc-app", ModelScope: []string{}, RateLimit: 10, Status: StatusEnabled,
	}
	if err := store.CreateAPIKey(ctx, row); err != nil {
		t.Fatalf("建 API Key 行失败: %v", err)
	}

	probe := &concProbe{}
	engine := concRateLimitEngine(db, withAuthSetting(""), probe.handler(http.StatusOK))

	const wantPass = 10 // = Key 的 rate_limit（resolveRateLimitLimits 的覆盖分支：容量取 Key 自身值）
	recorders := make([]*httptest.ResponseRecorder, concWorkers)
	concRun(concWorkers, func(i int) {
		recorders[i] = concDoRequest(engine, "Bearer "+plain)
	})

	passed, denied := 0, 0
	for i, recorder := range recorders {
		switch recorder.Code {
		case http.StatusOK:
			passed++
		case http.StatusTooManyRequests:
			denied++
			concAssert429Shape(t, recorder, "每分钟上限 10 次")
		default:
			t.Errorf("第 %d 个请求既非 200 也非 429：code=%d body=%s", i, recorder.Code, recorder.Body.String())
		}
	}
	if passed != wantPass {
		t.Fatalf("按 Key 覆盖 rate_limit=10 时 %d 并发应恰 %d 个放行，实际 %d（多放行 = 超卖，少放行 = 覆盖没生效）",
			concWorkers, wantPass, passed)
	}
	if denied != concWorkers-wantPass {
		t.Fatalf("429 计数 = %d，期望 %d", denied, concWorkers-wantPass)
	}
	if got := probe.count(); got != wantPass {
		t.Fatalf("业务 handler 应恰被调用 %d 次（429 在 handler 之前 Abort），实际 %d", wantPass, got)
	}
	// 真库并发副作用：每请求一个后台 goroutine 刷 last_used_at（并发写同一行）必须最终落到行上。
	waitForLastUsedAt(t, db, row.ID)
}

// TestConcurrentRateLimitGlobalConfigInflightDB：① 插件全局三键（真库 configs）rpm=60/burst=10
// ⇒ 32 并发（全局密钥路径，无 DB Key）恰 10 个放行；② 全局改成 rpm=0/inflight=4 ⇒
// 在飞上限 4 时再并发打 24 个全部**立即** 429（不排队、不进 handler），释放后名额归零且恢复放行。
func TestConcurrentRateLimitGlobalConfigInflightDB(t *testing.T) {
	db := concSandboxDB(t)
	concEnsureHostConfigs(t, db)
	concInitHostDB(t)
	layer := concUseProductionRateLimitLayer(t)
	const globalKey = "sk-conc-global-0001"
	set := withAuthSetting(globalKey)

	// 阶段 ①：全局 rpm=60 / burst=10（真库三键）。
	concWriteRateLimitConfig(t, db, 60, 10, 0)
	probe := &concProbe{}
	engine := concRateLimitEngine(db, set, probe.handler(http.StatusOK))

	const wantPass = 10
	recorders := make([]*httptest.ResponseRecorder, concWorkers)
	concRun(concWorkers, func(i int) {
		recorders[i] = concDoRequest(engine, "Bearer "+globalKey)
	})
	passed := 0
	for i, recorder := range recorders {
		switch recorder.Code {
		case http.StatusOK:
			passed++
		case http.StatusTooManyRequests:
			concAssert429Shape(t, recorder, "每分钟上限 60 次")
		default:
			t.Errorf("第 %d 个请求既非 200 也非 429：code=%d body=%s", i, recorder.Code, recorder.Body.String())
		}
	}
	if passed != wantPass {
		t.Fatalf("全局 rpm=60/burst=10 时 %d 并发应恰 %d 个放行，实际 %d", concWorkers, wantPass, passed)
	}
	if got := probe.count(); got != wantPass {
		t.Fatalf("业务 handler 应恰被调用 %d 次，实际 %d", wantPass, got)
	}
	if got := layer.bucketCount(); got != 1 {
		t.Fatalf("同一凭证应只占 1 个桶，实际 %d", got)
	}

	// 阶段 ②：全局 rpm=0 / burst=0 / max_inflight=4（在飞上限走同一条真库配置读出口）。
	concWriteRateLimitConfig(t, db, 0, 0, 4)
	gate := make(chan struct{})
	entered := make(chan struct{}, 64)
	slow := func(c *gin.Context) {
		entered <- struct{}{}
		<-gate
		c.Status(http.StatusOK)
	}
	inflightEngine := concRateLimitEngine(db, set, slow)

	const cap = 4
	inFlight := make([]*httptest.ResponseRecorder, cap)
	var wg sync.WaitGroup
	for i := 0; i < cap; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			inFlight[i] = concDoRequest(inflightEngine, "Bearer "+globalKey)
		}(i)
	}
	for i := 0; i < cap; i++ {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatalf("第 %d 个在飞请求没有进业务 handler", i+1)
		}
	}
	if got := layer.inflightCount(); got != cap {
		t.Fatalf("在飞数 = %d，期望 %d", got, cap)
	}

	// 名额已满（gate 未关、4 个仍在 handler 里）⇒ 再并发 24 个必须**全部立即 429**。
	extra := make([]*httptest.ResponseRecorder, 24)
	concRun(len(extra), func(i int) {
		extra[i] = concDoRequest(inflightEngine, "Bearer "+globalKey)
	})
	for i, recorder := range extra {
		if recorder.Code != http.StatusTooManyRequests {
			t.Fatalf("在飞已满时的第 %d 个附加请求应 429，实际 %d（body=%s）",
				i+1, recorder.Code, recorder.Body.String())
		}
		concAssert429Shape(t, recorder, "在飞")
	}
	if got := len(entered); got != 0 {
		t.Fatalf("被拒的 %d 个请求不得进业务 handler（应无排队）", got)
	}
	if got := layer.inflightCount(); got != cap {
		t.Fatalf("被拒请求不得占名额，在飞数 = %d，期望 %d", got, cap)
	}

	// 释放：4 个在飞收尾 200，名额归零，之后恢复放行（rpm=0 ⇒ 只受在飞上限约束）。
	close(gate)
	wg.Wait()
	for i, recorder := range inFlight {
		if recorder.Code != http.StatusOK {
			t.Fatalf("在飞的第 %d 个请求收尾应为 200，实际 %d", i+1, recorder.Code)
		}
	}
	if got := layer.inflightCount(); got != 0 {
		t.Fatalf("收尾后在飞数应归零，实际 %d", got)
	}
	if recorder := concDoRequest(inflightEngine, "Bearer "+globalKey); recorder.Code != http.StatusOK {
		t.Fatalf("释放后应恢复放行，实际 %d（body=%s）", recorder.Code, recorder.Body.String())
	}
	if got := layer.inflightCount(); got != 0 {
		t.Fatalf("收尾后在飞数应归零，实际 %d", got)
	}
}
