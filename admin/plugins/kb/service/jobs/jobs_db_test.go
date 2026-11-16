// plugin: kb v1.0.0
// 作业面的不变量用例（DB 门控；本插件自管作业面 = v1 里**唯一没有参考实现**的新码，
// 核心闭环全押在它上面，故六条不变量必须写成代码级断言，而不是写在注释里）：
//
//	① 同键至多一活跃行：并发 Enqueue 同 Key ⇒ 恰好 1 个 created=true（靠部分唯一索引，不靠先查后插）；
//	② 租约到期可重领：locked_until 置为过去 ⇒ 下一轮被重领，attempt **累加**；
//	③ attempt 上限：attempt >= max_attempts 仍失败 ⇒ 落 failed 且不再被领取；
//	④ 停用即停跑：admin_plugins.status <> 'enabled' ⇒ 本轮不领活（pending 行原样留在原状态）；
//	⑤ 进度单调：Progress 的 done 不回退；
//	⑥ kind 过滤：未注册 kind 的行不被领取（留 pending 等有实现的进程）。
//
// 跑法（隔离库；库名必须含 kb_test）：
//
//	createdb -h <主机> -p <端口> -U <用户> aigo_kb_test
//	DATABASE_WRITE_DBNAME=aigo_kb_test PLUGIN_KB_DB=1 go -C ai-go-admin test -count=1 -p 1 ./plugins/kb/service/jobs/
package jobs

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-go-hub/ai-go-admin/internal/infra/config"
	"github.com/ai-go-hub/ai-go-admin/internal/infra/database"
	"github.com/ai-go-hub/ai-go-admin/internal/model"
	"github.com/ai-go-hub/ai-go-admin/internal/plugin/cli"
	"github.com/ai-go-hub/ai-go-admin/internal/plugin/host"
	kbmodel "github.com/ai-go-hub/ai-go-admin/plugins/kb/model"

	"gorm.io/gorm"
)

// dbGateEnv 本插件库级用例的统一门控（与宿主插件套件的 PLUGIN_DB 刻意不同名：那个门控会跑宿主套件）
const dbGateEnv = "PLUGIN_KB_DB"

// testFixture 作业面用例夹具（自建自清；只动本用例登记的行）
type testFixture struct {
	t   *testing.T
	ctx context.Context
	db  *gorm.DB

	store     *Store
	pluginRow bool // 本用例是否建过台账行（清理时才删）
	jobIDs    []uint64
}

// newTestFixture 连库夹具（门控 + 库名守卫 + 应用插件迁移 + 台账行置 enabled）
func newTestFixture(t *testing.T) *testFixture {
	t.Helper()
	if os.Getenv(dbGateEnv) != "1" {
		t.Skipf("需要数据库：设 %s=1 启用（隔离库，库名须含 kb_test）", dbGateEnv)
	}
	chdirRepoRoot(t)
	if err := config.Init(); err != nil {
		t.Fatalf("初始化配置: %v", err)
	}
	if err := database.Init(); err != nil {
		t.Fatalf("初始化数据库: %v", err)
	}
	db := database.DB()
	requireKBTestDatabase(t, db)
	applyPluginMigrations(t)

	f := &testFixture{t: t, ctx: context.Background(), db: db, store: NewStore()}
	f.setPluginStatus(host.StatusEnabled)
	t.Cleanup(f.cleanup)
	return f
}

// chdirRepoRoot 切到仓库根（config/database 初始化依赖相对路径）
func chdirRepoRoot(t *testing.T) {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(dir + "/go.mod"); err == nil {
			if err := os.Chdir(dir); err != nil {
				t.Fatal(err)
			}
			return
		}
		parent := dir[:strings.LastIndex(dir, "/")]
		if parent == dir || parent == "" {
			t.Fatal("未找到 go.mod（仓库根）")
		}
		dir = parent
	}
}

// requireKBTestDatabase 库名守卫（**硬闸**：误连开发库会真删知识库表行，故不符即中止整组用例）
func requireKBTestDatabase(t *testing.T, db *gorm.DB) {
	t.Helper()
	if db == nil {
		t.Fatalf("数据库未初始化")
	}
	var name string
	if err := db.Raw("SELECT current_database()").Scan(&name).Error; err != nil {
		t.Fatalf("读取当前库名失败: %v", err)
	}
	if !strings.Contains(name, "kb_test") {
		t.Fatalf("库级用例只允许在隔离库上跑（库名须含 kb_test），当前库 = %q", name)
	}
}

// applyPluginMigrations 应用本插件自管迁移（幂等；库需允许建扩展 pg_trgm/vector）
func applyPluginMigrations(t *testing.T) {
	t.Helper()
	env, err := cli.NewEnv()
	if err != nil {
		t.Fatalf("构造迁移环境失败: %v", err)
	}
	if _, err := cli.ApplyPluginMigrations(context.Background(), env, "kb", "plugins/kb/migrations"); err != nil {
		t.Fatalf("应用插件迁移失败（隔离库需允许建扩展 pg_trgm/vector）: %v", err)
	}
}

// setPluginStatus 写台账状态（worker 每轮自查它 ⇒ 停用即停跑）
func (f *testFixture) setPluginStatus(status string) {
	f.t.Helper()
	_ = model.Plugin{} // 台账行的列面与宿主模型同源（避免魔法字符串漂移）
	err := f.db.Exec(`INSERT INTO admin_plugins (name, title, version, kind, status) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (name) DO UPDATE SET status = EXCLUDED.status`, PluginName, "知识库", "1.0.0", "code", status).Error
	if err != nil {
		f.t.Fatalf("写台账行失败: %v", err)
	}
	f.pluginRow = true
}

// enqueue 入队并登记（created=false 时不登记 —— 既有行由首次创建者登记）
func (f *testFixture) enqueue(in EnqueueInput) (*kbmodel.Job, bool) {
	f.t.Helper()
	job, created, err := f.store.Enqueue(f.ctx, in)
	if err != nil {
		f.t.Fatalf("入队失败: %v", err)
	}
	if created && job != nil {
		f.jobIDs = append(f.jobIDs, job.ID)
	}
	return job, created
}

// cleanup 自清：删本用例登记的行；台账行按需删（只删本用例建的）
func (f *testFixture) cleanup() {
	for _, id := range f.jobIDs {
		if err := f.db.Exec("DELETE FROM "+f.store.Table()+" WHERE id = ?", id).Error; err != nil {
			f.t.Logf("清理作业行 %d 失败（请手工核）: %v", id, err)
		}
	}
	if f.pluginRow {
		if err := f.db.Exec("DELETE FROM admin_plugins WHERE name = ?", PluginName).Error; err != nil {
			f.t.Logf("清理台账行失败（请手工核）: %v", err)
		}
	}
}

// registerKind 注册一个可编排的处理器（返回「本次应失败」的开关指针）
func registerKind(t *testing.T, kind string) *bool {
	t.Helper()
	fail := new(bool)
	handler := &probeHandler{fail: func() bool { return *fail }}
	Register(kind, handler)
	return fail
}

// probeHandler 可编排处理器（Run 计数 + 记录 Progress 调用）
type probeHandler struct {
	mu    sync.Mutex
	runs  int
	fail  func() bool
	delay time.Duration
}

func (h *probeHandler) Run(ctx context.Context, j *Job) error {
	h.mu.Lock()
	h.runs++
	h.mu.Unlock()
	if h.delay > 0 {
		select {
		case <-time.After(h.delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if h.fail != nil && h.fail() {
		return errProbeFailure
	}
	j.Result([]byte(`{"ok":true}`))
	return nil
}

func (h *probeHandler) runCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.runs
}

// errProbeFailure 处理器可编排失败
var errProbeFailure = &probeError{}

type probeError struct{}

func (*probeError) Error() string { return "用例编排的失败" }

// TestSameKeySingleFlightDB ① 同键至多一活跃行：并发入队同 Key ⇒ 恰好 1 个 created=true
func TestSameKeySingleFlightDB(t *testing.T) {
	f := newTestFixture(t)
	const workers = 8
	key := "kb:test:sf:" + f.suffix() // 键在并发之前算好：每个 goroutine 各算一次会得到不同键（测不到单飞）

	results := make([]bool, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, created, err := f.store.Enqueue(f.ctx, EnqueueInput{Kind: "test:singleflight", Key: key})
			if err != nil {
				t.Errorf("并发入队失败: %v", err)
				return
			}
			results[idx] = created
		}(i)
	}
	wg.Wait()

	// 登记本用例的活跃行（清理用）：同键只有一条活跃行 ⇒ 按 key 查回来登记
	var activeID uint64
	if err := f.db.Raw("SELECT id FROM "+f.store.Table()+" WHERE key = ?", key).Scan(&activeID).Error; err != nil {
		t.Fatalf("回读活跃行失败: %v", err)
	}
	if activeID != 0 {
		f.jobIDs = append(f.jobIDs, activeID)
	}

	createdCount := 0
	for _, created := range results {
		if created {
			createdCount++
		}
	}
	if createdCount != 1 {
		t.Fatalf("并发入队同键的 created=true 数 = %d，期望恰好 1（单飞判据失效）", createdCount)
	}

	var rows int64
	if err := f.db.Raw("SELECT COUNT(*) FROM "+f.store.Table()+" WHERE key = ? AND status IN ?",
		key, kbmodel.JobActiveStatuses).Scan(&rows).Error; err != nil {
		t.Fatalf("统计活跃行失败: %v", err)
	}
	if rows != 1 {
		t.Fatalf("同键活跃行 = %d，期望 1", rows)
	}
}

// TestLeaseExpiryReclaimDB ② 租约到期可重领：attempt 累加
func TestLeaseExpiryReclaimDB(t *testing.T) {
	f := newTestFixture(t)
	kind := "test:lease"
	Register(kind, &probeHandler{})

	job, created := f.enqueue(EnqueueInput{Kind: kind, Key: "kb:test:lease:" + f.suffix()})
	if !created {
		t.Fatal("首次入队应 created=true")
	}
	if claimed, err := RunOnce(f.ctx); err != nil || !claimed {
		t.Fatalf("首轮应领取一条作业（claimed=%v err=%v）", claimed, err)
	}

	first, err := f.store.Get(f.ctx, job.ID)
	if err != nil {
		t.Fatalf("读作业失败: %v", err)
	}
	if first.Attempt != 1 {
		t.Fatalf("首次领取后 attempt = %d，期望 1", first.Attempt)
	}

	// 手工把租约置为过去（模拟进程崩溃/超时救援）
	if err := f.db.Exec("UPDATE "+f.store.Table()+" SET status = ?, locked_until = now() - interval '1 hour' WHERE id = ?",
		kbmodel.JobStatusRunning, job.ID).Error; err != nil {
		t.Fatalf("置租约过期失败: %v", err)
	}
	if claimed, err := RunOnce(f.ctx); err != nil || !claimed {
		t.Fatalf("租约过期后应可重领（claimed=%v err=%v）", claimed, err)
	}
	second, err := f.store.Get(f.ctx, job.ID)
	if err != nil {
		t.Fatalf("读作业失败: %v", err)
	}
	if second.Attempt != 2 {
		t.Fatalf("重领后 attempt = %d，期望 2（attempt 必须累加）", second.Attempt)
	}
}

// TestAttemptCapDB ③ attempt 上限：仍失败 ⇒ 落 failed 且不再被领取
func TestAttemptCapDB(t *testing.T) {
	f := newTestFixture(t)
	kind := "test:cap"
	fail := registerKind(t, kind)
	*fail = true

	job, _ := f.enqueue(EnqueueInput{Kind: kind, Key: "kb:test:cap:" + f.suffix(), MaxAttempts: 2})

	for i := 0; i < 4; i++ {
		if _, err := RunOnce(f.ctx); err != nil {
			t.Fatalf("第 %d 轮执行失败: %v", i+1, err)
		}
		// 快速推进退避：把 run_at 拨回过去（等价于「等退避时长过去」），否则 30s 退避内不会被重领
		if err := f.db.Exec("UPDATE "+f.store.Table()+" SET run_at = now() - interval '1 second' WHERE id = ?", job.ID).Error; err != nil {
			t.Fatalf("推进退避失败: %v", err)
		}
	}
	row, err := f.store.Get(f.ctx, job.ID)
	if err != nil {
		t.Fatalf("读作业失败: %v", err)
	}
	if row.Status != kbmodel.JobStatusFailed {
		t.Fatalf("超上限后状态 = %q，期望 failed", row.Status)
	}
	if row.Attempt != 2 {
		t.Fatalf("attempt = %d，期望停在 max_attempts=2（不再被领取）", row.Attempt)
	}
}

// TestDisabledPluginStopsClaimDB ④ 停用即停跑：pending 行原样留存，且本轮**零写库**
// （每日入队与终态账本清理同样不在停用态发生——见 worker.RunOnce 的顺序纪律）。
func TestDisabledPluginStopsClaimDB(t *testing.T) {
	f := newTestFixture(t)
	kind := "test:disabled"
	Register(kind, &probeHandler{})
	// 让「每日入队」这条路径在本用例里真的可走：注册 kb:prune（jobs 包的内置用例不能 import
	// search 包——会成环），并复位进程内「今日已入队」标记，否则断言会因提前 return 而空转
	Register(pruneKind, &probeHandler{})
	pruneMu.Lock()
	lastPrune = ""
	pruneMu.Unlock()

	job, _ := f.enqueue(EnqueueInput{Kind: kind, Key: "kb:test:disabled:" + f.suffix()})
	before := f.countRows(t)
	f.setPluginStatus("disabled")

	if claimed, err := RunOnce(f.ctx); err != nil || claimed {
		t.Fatalf("停用态不应领活（claimed=%v err=%v）", claimed, err)
	}
	row, err := f.store.Get(f.ctx, job.ID)
	if err != nil {
		t.Fatalf("读作业失败: %v", err)
	}
	if row.Status != kbmodel.JobStatusPending || row.Attempt != 0 {
		t.Fatalf("停用态下 pending 行应原样留存，实际 status=%q attempt=%d", row.Status, row.Attempt)
	}
	if after := f.countRows(t); after != before {
		t.Fatalf("停用态本轮不得写库（含每日入队/终态清理）：行数 %d → %d", before, after)
	}
}

// TestProgressMonotonicDB ⑤ 进度单调：done 不回退
func TestProgressMonotonicDB(t *testing.T) {
	f := newTestFixture(t)
	job, _ := f.enqueue(EnqueueInput{Kind: "test:progress", Key: "kb:test:progress:" + f.suffix()})

	if err := f.store.updateProgress(f.ctx, job.ID, 10, 100); err != nil {
		t.Fatalf("进度回写失败: %v", err)
	}
	if err := f.store.updateProgress(f.ctx, job.ID, 3, 100); err != nil {
		t.Fatalf("进度回写失败: %v", err)
	}
	row, err := f.store.Get(f.ctx, job.ID)
	if err != nil {
		t.Fatalf("读作业失败: %v", err)
	}
	if row.ProgressDone != 10 {
		t.Fatalf("done = %d，期望 10（进度必须单调不回退）", row.ProgressDone)
	}
}

// TestUnregisteredKindNotClaimedDB ⑥ kind 过滤：未注册 kind 留 pending
func TestUnregisteredKindNotClaimedDB(t *testing.T) {
	f := newTestFixture(t)
	kind := "test:unregistered:" + f.suffix() // 刻意不注册
	job, _ := f.enqueue(EnqueueInput{Kind: kind, Key: "kb:test:unregistered:" + f.suffix()})

	// 多轮（把本包其它用例注册过的 kind 的到期行先领干净）：断言的是**本用例这条未注册行**不被推进，
	// 而不是「本轮一定没领到活」——后者会被同一测试二进制里其它用例的注册集污染
	for i := 0; i < 3; i++ {
		if _, err := RunOnce(f.ctx); err != nil {
			t.Fatalf("第 %d 轮执行失败: %v", i+1, err)
		}
	}
	row, err := f.store.Get(f.ctx, job.ID)
	if err != nil {
		t.Fatalf("读作业失败: %v", err)
	}
	if row.Status != kbmodel.JobStatusPending {
		t.Fatalf("未注册 kind 的行应留 pending（等有实现的进程），实际 %q", row.Status)
	}
}

// countRows 账本行数（停用态「零写库」断言的读数面）
func (f *testFixture) countRows(t *testing.T) int64 {
	t.Helper()
	var n int64
	if err := f.db.Raw("SELECT COUNT(*) FROM " + f.store.Table()).Scan(&n).Error; err != nil {
		t.Fatalf("统计账本行失败: %v", err)
	}
	return n
}

// suffix 用例唯一后缀（同进程内多用例隔离；作业键与行都带它）
func (f *testFixture) suffix() string {
	return time.Now().Format("150405.000000000")
}
