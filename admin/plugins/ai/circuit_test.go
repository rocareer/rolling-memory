// plugin: ai v0.2.0
package ai

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

// fakeCircuitStore 是 CircuitStore 的内存实现：用互斥锁模拟 SQL 条件更新的原子性，
// 让「CAS 未当选则不调用上游」「并发只有一个探针」可以在无数据库的测试里验证。
type fakeCircuitStore struct {
	mu          sync.Mutex
	circuit     map[uint64]string
	openedAt    map[uint64]int64
	failures    map[uint64]int
	lastSuccess map[uint64]int64
	events      []BreakerEvent
	totals      []fakeTotal

	markFailureCalls int
	markSuccessCalls int
	enterCASCalls    int
	reselectCalls    int
	openCalls        int
	forceOpenCalls   int
	manualCloseCalls int
	addTotalsCalls   int
}

type fakeTotal struct {
	channelID uint64
	success   bool
	tokens    int
}

func newFakeCircuitStore() *fakeCircuitStore {
	return &fakeCircuitStore{
		circuit:     make(map[uint64]string),
		openedAt:    make(map[uint64]int64),
		failures:    make(map[uint64]int),
		lastSuccess: make(map[uint64]int64),
	}
}

func (f *fakeCircuitStore) circuitOf(id uint64) string {
	if v, ok := f.circuit[id]; ok {
		return v
	}
	return CircuitClosed
}

func (f *fakeCircuitStore) MarkFailure(_ context.Context, id uint64, _ string, _ int64) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.markFailureCalls++
	f.failures[id]++
	return f.failures[id], nil
}

func (f *fakeCircuitStore) EnterCAS(_ context.Context, id uint64, now int64) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.enterCASCalls++
	if f.circuitOf(id) != CircuitOpen {
		return false, nil
	}
	f.circuit[id] = CircuitHalfOpen
	f.openedAt[id] = now
	return true, nil
}

func (f *fakeCircuitStore) ReselectStaleProbe(_ context.Context, id uint64, now, cooldownSeconds int64) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reselectCalls++
	if f.circuitOf(id) != CircuitHalfOpen {
		return false, nil
	}
	v, ok := f.openedAt[id]
	if !ok || v >= now-cooldownSeconds {
		return false, nil
	}
	f.openedAt[id] = now
	return true, nil
}

func (f *fakeCircuitStore) MarkSuccess(_ context.Context, id uint64, at int64) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.markSuccessCalls++
	f.circuit[id] = CircuitClosed
	f.failures[id] = 0
	delete(f.openedAt, id)
	f.lastSuccess[id] = at
	return true, nil
}

func (f *fakeCircuitStore) OpenCircuit(_ context.Context, id uint64, at int64) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.openCalls++
	if f.circuitOf(id) == CircuitOpen {
		return false, nil
	}
	f.circuit[id] = CircuitOpen
	f.openedAt[id] = at
	return true, nil
}

func (f *fakeCircuitStore) ManualOpenCircuit(_ context.Context, id uint64, at int64) (bool, error) {
	return f.OpenCircuit(context.Background(), id, at)
}

func (f *fakeCircuitStore) ManualCloseCircuit(_ context.Context, id uint64, _ int64) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.manualCloseCalls++
	f.circuit[id] = CircuitClosed
	f.failures[id] = 0
	delete(f.openedAt, id)
	return 1, nil
}

func (f *fakeCircuitStore) AddChannelTotals(_ context.Context, channelID uint64, success bool, tokens int, _ int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.addTotalsCalls++
	f.totals = append(f.totals, fakeTotal{channelID: channelID, success: success, tokens: tokens})
	return nil
}

func (f *fakeCircuitStore) InsertBreakerEvent(_ context.Context, ev *BreakerEvent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if ev != nil {
		f.events = append(f.events, *ev)
	}
	return nil
}

func ptrInt64(v int64) *int64 { return &v }

func TestDecideTable(t *testing.T) {
	cases := []struct {
		name     string
		circuit  string
		openedAt *int64
		now      int64
		cooldown int64
		want     CircuitDecision
	}{
		{"closed 放行", CircuitClosed, nil, 100, 60, DecisionAllow},
		{"closed 即使有 opened_at 也放行", CircuitClosed, ptrInt64(100), 101, 60, DecisionAllow},
		{"open 冷却中", CircuitOpen, ptrInt64(100), 159, 60, DecisionDeny},
		{"open 冷却刚好到期", CircuitOpen, ptrInt64(100), 160, 60, DecisionProbe},
		{"open 无基准时间按 probe", CircuitOpen, nil, 100, 60, DecisionProbe},
		{"half_open 探针在飞", CircuitHalfOpen, ptrInt64(100), 159, 60, DecisionDeny},
		{"half_open 卡死重选", CircuitHalfOpen, ptrInt64(100), 160, 60, DecisionProbe},
		{"未知状态 fail-closed", "broken", nil, 100, 60, DecisionDeny},
		{"cooldown=0 用默认 60", CircuitOpen, ptrInt64(100), 159, 0, DecisionDeny},
		{"cooldown=0 到期", CircuitOpen, ptrInt64(100), 160, 0, DecisionProbe},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Decide(tc.circuit, tc.openedAt, tc.now, tc.cooldown); got != tc.want {
				t.Fatalf("Decide(%s, %v, %d, %d) = %v，期望 %v", tc.circuit, tc.openedAt, tc.now, tc.cooldown, got, tc.want)
			}
		})
	}
}

func TestEffectiveBreakerDefaults(t *testing.T) {
	if got := EffectiveFailThreshold(0); got != 5 {
		t.Fatalf("fail_threshold 默认 = %d，期望 5", got)
	}
	if got := EffectiveFailThreshold(9); got != 9 {
		t.Fatalf("fail_threshold 显式 = %d，期望 9", got)
	}
	if got := EffectiveCooldown(0); got != 60 {
		t.Fatalf("cooldown 默认 = %d，期望 60", got)
	}
	if got := EffectiveCooldown(120); got != 120 {
		t.Fatalf("cooldown 显式 = %d，期望 120", got)
	}
}

func TestEnterClosedAllowsWithoutStoreCalls(t *testing.T) {
	store := newFakeCircuitStore()
	c := NewCircuit(store)
	ch := Channel{ID: 1, Circuit: CircuitClosed}
	ad, err := c.Enter(context.Background(), ch, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !ad.Allowed || ad.Circuit != CircuitClosed {
		t.Fatalf("closed 应放行：%+v", ad)
	}
	if store.enterCASCalls != 0 || store.reselectCalls != 0 {
		t.Fatalf("closed 不应调用 CAS：%+v", store)
	}
}

func TestEnterOpenDuringCooldownDenies(t *testing.T) {
	store := newFakeCircuitStore()
	store.circuit[1] = CircuitOpen
	store.openedAt[1] = 100
	c := NewCircuit(store)
	ad, err := c.Enter(context.Background(), Channel{ID: 1, Circuit: CircuitOpen, CooldownSeconds: 60, OpenedAt: ptrInt64(100)}, 130)
	if err != nil {
		t.Fatal(err)
	}
	if ad.Allowed {
		t.Fatal("冷却中不应放行")
	}
	if store.enterCASCalls != 0 {
		t.Fatal("冷却中不应尝试 CAS")
	}
}

func TestEnterOpenAfterCooldownElectsSingleProbe(t *testing.T) {
	store := newFakeCircuitStore()
	store.circuit[1] = CircuitOpen
	store.openedAt[1] = 100
	c := NewCircuit(store)
	ch := Channel{ID: 1, Circuit: CircuitOpen, CooldownSeconds: 60, OpenedAt: ptrInt64(100)}

	ad, err := c.Enter(context.Background(), ch, 160)
	if err != nil {
		t.Fatal(err)
	}
	if !ad.Allowed || ad.Circuit != CircuitHalfOpen {
		t.Fatalf("冷却到期应当选半开探针：%+v", ad)
	}
	if got := store.circuitOf(1); got != CircuitHalfOpen {
		t.Fatalf("CAS 后 circuit=%s，期望 half_open", got)
	}

	// 第二个进程拿着旧快照（仍以为 open）再抢：CAS 必须失败。
	ad2, err := c.Enter(context.Background(), ch, 161)
	if err != nil {
		t.Fatal(err)
	}
	if ad2.Allowed {
		t.Fatal("同一冷却窗内只允许一个探针")
	}
}

func TestEnterHalfOpenStaleProbeReselect(t *testing.T) {
	store := newFakeCircuitStore()
	store.circuit[1] = CircuitHalfOpen
	store.openedAt[1] = 100
	c := NewCircuit(store)
	ad, err := c.Enter(context.Background(), Channel{ID: 1, Circuit: CircuitHalfOpen, CooldownSeconds: 60}, 200)
	if err != nil {
		t.Fatal(err)
	}
	if !ad.Allowed {
		t.Fatal("half_open 超过冷却应视为卡死并重选")
	}
	if store.reselectCalls != 1 {
		t.Fatalf("reselect 调用次数 = %d", store.reselectCalls)
	}
	if got := store.openedAt[1]; got != 200 {
		t.Fatalf("opened_at = %d，期望刷新为 200", got)
	}
}

func TestEnterHalfOpenInFlightDenied(t *testing.T) {
	store := newFakeCircuitStore()
	store.circuit[1] = CircuitHalfOpen
	store.openedAt[1] = 100
	c := NewCircuit(store)
	ad, err := c.Enter(context.Background(), Channel{ID: 1, Circuit: CircuitHalfOpen, CooldownSeconds: 60, OpenedAt: ptrInt64(100)}, 130)
	if err != nil {
		t.Fatal(err)
	}
	if ad.Allowed {
		t.Fatal("半开探针在飞时应拒绝")
	}
	if store.reselectCalls != 0 {
		t.Fatal("冷却窗内不应重选")
	}
}

func TestConcurrentEnterElectsSingleProbe(t *testing.T) {
	store := newFakeCircuitStore()
	store.circuit[1] = CircuitOpen
	store.openedAt[1] = 100
	c := NewCircuit(store)
	ch := Channel{ID: 1, Circuit: CircuitOpen, CooldownSeconds: 60, OpenedAt: ptrInt64(100)}

	const goroutines = 64
	var wg sync.WaitGroup
	allowed := make(chan struct{}, goroutines)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ad, err := c.Enter(context.Background(), ch, 200)
			if err == nil && ad.Allowed {
				allowed <- struct{}{}
			}
		}()
	}
	wg.Wait()
	close(allowed)
	if n := len(allowed); n != 1 {
		t.Fatalf("并发准入当选数 = %d，期望恰好 1", n)
	}
}

func TestOnFailureThresholdTripsOpenOnce(t *testing.T) {
	store := newFakeCircuitStore()
	c := NewCircuit(store)
	ch := Channel{ID: 1, Name: "ch", Circuit: CircuitClosed, FailThreshold: 0, CooldownSeconds: 0}

	for i := 1; i <= 4; i++ {
		if err := c.OnFailure(context.Background(), ch, "boom", int64(100+i)); err != nil {
			t.Fatal(err)
		}
	}
	if got := store.circuitOf(1); got != CircuitClosed {
		t.Fatalf("未到阈值就跳闸：%s", got)
	}
	if len(store.events) != 0 {
		t.Fatalf("未到阈值不应写事件：%+v", store.events)
	}

	if err := c.OnFailure(context.Background(), ch, "boom", 105); err != nil {
		t.Fatal(err)
	}
	if got := store.circuitOf(1); got != CircuitOpen {
		t.Fatalf("第 5 次失败应跳闸，实际 %s", got)
	}
	if len(store.events) != 1 || store.events[0].Event != EventOpen {
		t.Fatalf("open 事件 = %+v", store.events)
	}
	if store.events[0].ConsecutiveFailures != 5 || store.events[0].FailThreshold != 5 || store.events[0].CooldownSeconds != 60 {
		t.Fatalf("事件快照错误：%+v", store.events[0])
	}

	// 并发/陈旧快照再报失败：CAS 期望 closed 失败，不得重复写 open 事件。
	if err := c.OnFailure(context.Background(), ch, "boom again", 106); err != nil {
		t.Fatal(err)
	}
	if len(store.events) != 1 {
		t.Fatalf("open 事件重复写入：%+v", store.events)
	}
}

func TestOnFailureHalfOpenReopensEvenBelowThreshold(t *testing.T) {
	store := newFakeCircuitStore()
	store.circuit[1] = CircuitHalfOpen
	store.openedAt[1] = 100
	store.failures[1] = 0
	c := NewCircuit(store)
	ch := Channel{ID: 1, Name: "ch", Circuit: CircuitHalfOpen, FailThreshold: 5, CooldownSeconds: 60}

	if err := c.OnFailure(context.Background(), ch, "probe failed", 200); err != nil {
		t.Fatal(err)
	}
	if got := store.circuitOf(1); got != CircuitOpen {
		t.Fatalf("半开探针失败应回 open，实际 %s", got)
	}
	if got := store.openedAt[1]; got != 200 {
		t.Fatalf("opened_at = %d，期望重新计时为 200", got)
	}
	if len(store.events) != 1 || store.events[0].Event != EventOpen {
		t.Fatalf("open 事件 = %+v", store.events)
	}
}

func TestOnSuccessHalfOpenClosesAndWritesEvent(t *testing.T) {
	store := newFakeCircuitStore()
	store.circuit[1] = CircuitHalfOpen
	store.openedAt[1] = 100
	store.failures[1] = 3
	c := NewCircuit(store)
	ch := Channel{
		ID: 1, Name: "ch", Circuit: CircuitHalfOpen,
		ConsecutiveFailures: 3, OpenedAt: ptrInt64(100),
		FailThreshold: 5, CooldownSeconds: 60,
	}

	if err := c.OnSuccess(context.Background(), ch, 200); err != nil {
		t.Fatal(err)
	}
	if got := store.circuitOf(1); got != CircuitClosed {
		t.Fatalf("半开成功后应 closed，实际 %s", got)
	}
	if store.failures[1] != 0 {
		t.Fatalf("连续失败未清零：%d", store.failures[1])
	}
	if _, ok := store.openedAt[1]; ok {
		t.Fatal("opened_at 未清空")
	}
	if len(store.events) != 1 || store.events[0].Event != EventClose {
		t.Fatalf("close 事件 = %+v", store.events)
	}
}

func TestOnSuccessSteadyStateSkipsWrite(t *testing.T) {
	store := newFakeCircuitStore()
	store.circuit[1] = CircuitClosed
	store.lastSuccess[1] = 90
	c := NewCircuit(store)
	ch := Channel{
		ID: 1, Circuit: CircuitClosed, ConsecutiveFailures: 0,
		OpenedAt: nil, LastSuccessAt: ptrInt64(90),
	}
	if err := c.OnSuccess(context.Background(), ch, 120); err != nil {
		t.Fatal(err)
	}
	if store.markSuccessCalls != 0 {
		t.Fatalf("稳态不应落库，markSuccessCalls=%d", store.markSuccessCalls)
	}
	if len(store.events) != 0 {
		t.Fatalf("稳态不应写事件：%+v", store.events)
	}
}

func TestOnSuccessHeartbeatWrites(t *testing.T) {
	store := newFakeCircuitStore()
	store.circuit[1] = CircuitClosed
	store.lastSuccess[1] = 30
	c := NewCircuit(store)
	ch := Channel{
		ID: 1, Circuit: CircuitClosed, ConsecutiveFailures: 0,
		OpenedAt: nil, LastSuccessAt: ptrInt64(30),
	}
	if err := c.OnSuccess(context.Background(), ch, 91); err != nil { // 距上次 61s
		t.Fatal(err)
	}
	if store.markSuccessCalls != 1 {
		t.Fatalf("心跳超龄应补写，markSuccessCalls=%d", store.markSuccessCalls)
	}
}

func TestForceOpenAndResetDoNotTouchTotals(t *testing.T) {
	store := newFakeCircuitStore()
	store.circuit[1] = CircuitClosed
	store.failures[1] = 2
	c := NewCircuit(store)
	ch := Channel{ID: 1, Name: "ch", Circuit: CircuitClosed, ConsecutiveFailures: 2}

	if err := c.ForceOpen(context.Background(), ch, 100); err != nil {
		t.Fatal(err)
	}
	if got := store.circuitOf(1); got != CircuitOpen {
		t.Fatalf("人工熔断后 circuit=%s", got)
	}
	if len(store.events) != 1 || store.events[0].Event != EventManualOpen {
		t.Fatalf("manual_open 事件 = %+v", store.events)
	}
	if store.events[0].ConsecutiveFailures != 2 {
		t.Fatalf("人工熔断应保留连续失败快照：%+v", store.events[0])
	}

	ch.Circuit = CircuitOpen
	if err := c.ForceOpen(context.Background(), ch, 101); !errors.Is(err, ErrAlreadyOpen) {
		t.Fatalf("已 open 再人工熔断应报 ErrAlreadyOpen，实际 %v", err)
	}
	if len(store.events) != 1 {
		t.Fatalf("重复人工熔断不应写事件：%+v", store.events)
	}

	if err := c.Reset(context.Background(), ch, 102); err != nil {
		t.Fatal(err)
	}
	if got := store.circuitOf(1); got != CircuitClosed {
		t.Fatalf("人工恢复后 circuit=%s", got)
	}
	if store.failures[1] != 0 {
		t.Fatalf("人工恢复应清零连续失败：%d", store.failures[1])
	}
	if len(store.events) != 2 || store.events[1].Event != EventManualClose {
		t.Fatalf("manual_close 事件 = %+v", store.events)
	}
	if len(store.totals) != 0 {
		t.Fatalf("人工操作不得改累计统计：%+v", store.totals)
	}
}

func TestRecordTotals(t *testing.T) {
	store := newFakeCircuitStore()
	c := NewCircuit(store)
	if err := c.RecordTotals(context.Background(), 7, true, 123, 100); err != nil {
		t.Fatal(err)
	}
	if err := c.RecordTotals(context.Background(), 0, false, 0, 100); err != nil {
		t.Fatal(err)
	}
	if len(store.totals) != 1 {
		t.Fatalf("累计调用 = %+v", store.totals)
	}
	if store.totals[0].channelID != 7 || !store.totals[0].success || store.totals[0].tokens != 123 {
		t.Fatalf("累计参数错误：%+v", store.totals[0])
	}
}

func TestTruncateMessageByRunes(t *testing.T) {
	s := strings.Repeat("中", 600)
	got := truncateMessage(s, 500)
	if len([]rune(got)) != 500 {
		t.Fatalf("截断后字符数 = %d，期望 500", len([]rune(got)))
	}
	if truncateMessage("short", 500) != "short" {
		t.Fatal("短文案不应被改动")
	}
}

// ===== 熔断默认值的三级回落（渠道字段 → 插件配置 → 代码常量）=====

func TestResolveBreakerValueChain(t *testing.T) {
	cases := []struct {
		name          string
		channel       int
		plugin        int
		wantThreshold int
		wantCooldown  int
	}{
		{"渠道显式值优先于插件配置", 9, 3, 9, 9},
		{"渠道为 0 时用插件配置", 0, 3, 3, 3},
		{"都没配时回代码常量", 0, 0, DefaultFailThreshold, DefaultCooldownSeconds},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ResolveFailThreshold(tc.channel, tc.plugin); got != tc.wantThreshold {
				t.Fatalf("ResolveFailThreshold(%d,%d) = %d，期望 %d", tc.channel, tc.plugin, got, tc.wantThreshold)
			}
			if got := ResolveCooldown(tc.channel, tc.plugin); got != tc.wantCooldown {
				t.Fatalf("ResolveCooldown(%d,%d) = %d，期望 %d", tc.channel, tc.plugin, got, tc.wantCooldown)
			}
		})
	}
}

func TestCircuitUsesPluginBreakerDefaults(t *testing.T) {
	store := newFakeCircuitStore()
	// 运维在设置面把阈值改成 3、冷却改成 90 ⇒ 引擎必须用 3/90（而不是只读配置、
	// 行为却仍旧是代码常量 5/60 —— 这正是「配置静默失效」的缺陷现场）。
	c := NewCircuit(store, CircuitDefaults{FailThreshold: 3, CooldownSeconds: 90})
	ch := Channel{ID: 1, Name: "ch", Circuit: CircuitClosed, FailThreshold: 0, CooldownSeconds: 0}

	for i := 1; i <= 2; i++ {
		if err := c.OnFailure(context.Background(), ch, "boom", int64(100+i)); err != nil {
			t.Fatal(err)
		}
	}
	if got := store.circuitOf(1); got != CircuitClosed {
		t.Fatalf("阈值 3：第 2 次失败不应跳闸（circuit=%s，代码常量 5 才会这样）", got)
	}
	if err := c.OnFailure(context.Background(), ch, "boom", 103); err != nil {
		t.Fatal(err)
	}
	if got := store.circuitOf(1); got != CircuitOpen {
		t.Fatalf("阈值 3：第 3 次失败应跳闸（circuit=%s）", got)
	}
	if len(store.events) != 1 {
		t.Fatalf("跳闸应写 1 条 open 事件：%+v", store.events)
	}
	if ev := store.events[0]; ev.FailThreshold != 3 || ev.CooldownSeconds != 90 {
		t.Fatalf("事件快照必须是生效值 3/90，实际 %+v", ev)
	}

	// 冷却 90s（而非常量 60s）：open 后 60s 仍未到期 ⇒ 拒绝且不发探针。
	ad, err := c.Enter(context.Background(), Channel{ID: 1, Circuit: CircuitOpen, OpenedAt: ptrInt64(103)}, 170)
	if err != nil {
		t.Fatal(err)
	}
	if ad.Allowed || store.enterCASCalls != 0 {
		t.Fatalf("插件冷却 90s 生效时 60s 后不得放行：%+v（CAS 次数 %d）", ad, store.enterCASCalls)
	}
	// 超过 90s 才应选举半开探针。
	ad2, err := c.Enter(context.Background(), Channel{ID: 1, Circuit: CircuitOpen, OpenedAt: ptrInt64(103)}, 194)
	if err != nil {
		t.Fatal(err)
	}
	if !ad2.Allowed || ad2.Circuit != CircuitHalfOpen {
		t.Fatalf("冷却 90s 到期后应当选半开探针：%+v", ad2)
	}

	// 未注入（配置行缺失/读取失败）时必须退回代码常量。
	plain := NewCircuit(newFakeCircuitStore())
	if got := plain.failThresholdFor(0); got != DefaultFailThreshold {
		t.Fatalf("未注入时阈值 = %d，期望 %d", got, DefaultFailThreshold)
	}
	if got := plain.cooldownFor(0); got != DefaultCooldownSeconds {
		t.Fatalf("未注入时冷却 = %d，期望 %d", got, DefaultCooldownSeconds)
	}
}

// TestServiceInjectsBreakerDefaults 把「配置 → Service → Circuit」这条注入线钉住：
// NewService 收到 Config 里的熔断默认后，OnFailure 必须按插件配置跳闸。
func TestServiceInjectsBreakerDefaults(t *testing.T) {
	store := newFakeCircuitStore()
	svc := NewService(&fakeRepository{}, &fakeDriver{}, Config{
		BreakerFailThreshold:   2,
		BreakerCooldownSeconds: 90,
	}, nil)
	svc.breaker.store = store // 决策走真 Circuit、落库走假 store
	ch := Channel{ID: 5, Circuit: CircuitClosed, FailThreshold: 0, CooldownSeconds: 0}
	if err := svc.breaker.OnFailure(context.Background(), ch, "boom", 100); err != nil {
		t.Fatal(err)
	}
	if got := store.circuitOf(5); got != CircuitClosed {
		t.Fatalf("阈值 2：第 1 次失败不应跳闸（circuit=%s）", got)
	}
	if err := svc.breaker.OnFailure(context.Background(), ch, "boom", 101); err != nil {
		t.Fatal(err)
	}
	if got := store.circuitOf(5); got != CircuitOpen {
		t.Fatalf("阈值 2：第 2 次失败应跳闸（circuit=%s，未注入时会是 5）", got)
	}
	if len(store.events) != 1 || store.events[0].CooldownSeconds != 90 {
		t.Fatalf("事件冷却快照应为插件配置 90：%+v", store.events)
	}
}
