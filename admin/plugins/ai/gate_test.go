// plugin: ai v0.2.0
// gate_test.go 覆盖渠道准入门（gate.go）与 token 回写挂点（circuit.go 的 charger）的三类判据：
//
//	① 纯函数/单件：三级取值、桶容、等待与 fail-open、TPM 窗口扣减与恢复、并发不超发、
//	   与按 Key 限流桶（ratelimit.go）互不串扰；
//	② 回写挂点：charger 的回调条件（tokens>0 且 channel_id>0）、nil-safe、挂载/卸载；
//	③ Service 级集成（假 repo + 假驱动）：Chat / ChatStream / Embeddings 成功路径**必然扣减**
//	   且等于该次 counts.TotalTokens；失败路径（tokens=0）不扣；TPM 跳过的候选不自增 attempt、
//	   由下一候选承接（并与「真失败换道」的 attempt 序列对照）。
//
// 夹具自持（不复用 handler_test.go / circuit_test.go 的假件）：那两个文件属接口层包的既有用例，
// 本文件的判据要能在「只动本包三个文件」的前提下独立复跑。等待路径用假时钟 + 「睡眠推进时钟」
// 实现，因此「睡到回填」的断言是确定性的，不需要真等待。
package ai

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ===== 夹具：假时钟 / 日志 / 准入门装配 =====

type gateTestClock struct {
	mu sync.Mutex
	at time.Time
}

func (c *gateTestClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *gateTestClock) advance(d time.Duration) {
	c.mu.Lock()
	c.at = c.at.Add(d)
	c.mu.Unlock()
}

// gateTestLog 收集日志行：fail-open 与「跳过候选」都必须「有日志」，只看返回值不足以断言。
type gateTestLog struct {
	mu    sync.Mutex
	lines []string
}

func (l *gateTestLog) logf(format string, args ...any) {
	l.mu.Lock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
	l.mu.Unlock()
}

func (l *gateTestLog) count(substr string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, line := range l.lines {
		if strings.Contains(line, substr) {
			n++
		}
	}
	return n
}

func (l *gateTestLog) joined() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

// gateTestFixture 是「准入门 + 假时钟 + 注入缺省」的测试装配。
// sleep 会推进假时钟（等价于真实现「睡到回填」），故等待路径零真实耗时且完全可复现。
type gateTestFixture struct {
	gate  *ChannelGate
	clock *gateTestClock
	logs  *gateTestLog
	start time.Time

	mu    sync.Mutex
	waits []time.Duration
}

func newGateTestFixture(defaults ChannelGateDefaults, waitSeconds int) *gateTestFixture {
	start := time.Unix(1700000000, 0)
	f := &gateTestFixture{clock: &gateTestClock{at: start}, logs: &gateTestLog{}, start: start}
	f.gate = &ChannelGate{
		buckets:     make(map[int64]*channelRateBucket),
		windows:     make(map[int64]*channelTokenWindow),
		defaults:    func(context.Context) (ChannelGateDefaults, error) { return defaults, nil },
		now:         f.clock.now,
		sleep:       f.sleep,
		logf:        f.logs.logf,
		waitSeconds: waitSeconds,
	}
	return f
}

func (f *gateTestFixture) sleep(d time.Duration) {
	f.mu.Lock()
	f.waits = append(f.waits, d)
	f.mu.Unlock()
	f.clock.advance(d)
}

func (f *gateTestFixture) waitsCopy() []time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]time.Duration, len(f.waits))
	copy(out, f.waits)
	return out
}

func (f *gateTestFixture) waitCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.waits)
}

func (f *gateTestFixture) elapsed() time.Duration {
	return f.clock.now().Sub(f.start)
}

// ===== ① 三级取值与桶容（纯函数）=====

func TestResolveChannelGateLimitsThreeLevels(t *testing.T) {
	cases := []struct {
		name          string
		channel       int
		pluginDefault int
		want          int
	}{
		{"渠道配了就用渠道", 120, 60, 120},
		{"渠道未配回落到插件缺省", 0, 60, 60},
		{"两级都未配 = 不限", 0, 0, 0},
		{"负数视为未配（防御）", -3, -3, 0},
	}
	for _, tc := range cases {
		if got := ResolveChannelGateRPM(tc.channel, tc.pluginDefault); got != tc.want {
			t.Fatalf("ResolveChannelGateRPM(%d,%d) = %d，期望 %d（%s）", tc.channel, tc.pluginDefault, got, tc.want, tc.name)
		}
		if got := ResolveChannelGateTPM(tc.channel, tc.pluginDefault); got != tc.want {
			t.Fatalf("ResolveChannelGateTPM(%d,%d) = %d，期望 %d（%s）", tc.channel, tc.pluginDefault, got, tc.want, tc.name)
		}
	}

	burstCases := []struct {
		rpm  int
		want int
	}{{60, 10}, {599, 10}, {600, 10}, {601, 11}, {6000, 100}, {1, 10}}
	for _, tc := range burstCases {
		if got := ChannelGateBurst(tc.rpm); got != tc.want {
			t.Fatalf("ChannelGateBurst(%d) = %d，期望 %d（max(10, ceil(rpm/60))）", tc.rpm, got, tc.want)
		}
	}
}

func TestParseChannelGateDefaults(t *testing.T) {
	got := parseChannelGateDefaults(map[string]string{
		configKeyChannelGateRPMDefault: "1200",
		configKeyChannelGateTPMDefault: "240000",
	})
	if got.RPM != 1200 || got.TPM != 240000 {
		t.Fatalf("解析两键 = %+v，期望 {1200 240000}", got)
	}

	for _, values := range []map[string]string{
		{},
		{configKeyChannelGateRPMDefault: "", configKeyChannelGateTPMDefault: "abc"},
		{configKeyChannelGateRPMDefault: "-5", configKeyChannelGateTPMDefault: "0"},
		{configKeyChannelGateRPMDefault: "  ", configKeyChannelGateTPMDefault: "3.7"},
	} {
		if got := parseChannelGateDefaults(values); got.RPM != 0 || got.TPM != 0 {
			t.Fatalf("非法/空/负数应一律 0 = 不限：values=%v → %+v", values, got)
		}
	}

	clamped := parseChannelGateDefaults(map[string]string{
		configKeyChannelGateRPMDefault: fmt.Sprintf("%d", channelGateRPMMax+1),
		configKeyChannelGateTPMDefault: fmt.Sprintf("%d", channelGateTPMMax+1),
	})
	if clamped.RPM != channelGateRPMMax || clamped.TPM != channelGateTPMMax {
		t.Fatalf("超上限应 clamp：%+v", clamped)
	}
}

// ===== ② RPM：等待、fail-open、不限 =====

func mustAdmit(t *testing.T, gate *ChannelGate, ch Channel, label string) {
	t.Helper()
	ok, err := gate.Admit(context.Background(), ch, 0)
	if err != nil || !ok {
		t.Fatalf("%s：应放行，实际 ok=%v err=%v", label, ok, err)
	}
}

func TestChannelGateRPMWaitsThenAdmits(t *testing.T) {
	f := newGateTestFixture(ChannelGateDefaults{}, DefaultChannelGateWaitSeconds)
	ch := Channel{ID: 1, Name: "c1", RPMLimit: 60} // 桶容 10、补桶 1 枚/秒

	for i := 0; i < ChannelGateBurst(60); i++ {
		mustAdmit(t, f.gate, ch, fmt.Sprintf("桶容内第 %d 次", i+1))
	}
	if got := f.waitCount(); got != 0 {
		t.Fatalf("桶容内不得等待，实际睡了 %d 次", got)
	}
	if got := f.gate.rateTokens(ch.ID); got != 0 {
		t.Fatalf("桶应被取尽，实际余量 %v", got)
	}

	mustAdmit(t, f.gate, ch, "第 11 次（桶空后）")
	if got := f.waitCount(); got != 1 {
		t.Fatalf("第 11 次应恰睡一次（睡到回填，不轮询），实际 %d 次", got)
	}
	if d := f.waitsCopy()[0]; d < 900*time.Millisecond || d > 1100*time.Millisecond {
		t.Fatalf("等待时长应为 ~1s（rpm=60 ⇒ 1 枚/秒），实际 %s", d)
	}
	if got := f.elapsed(); got < 900*time.Millisecond {
		t.Fatalf("时钟应随睡眠推进 ≥1s，实际 %s", got)
	}
	if f.logs.count("fail-open") != 0 {
		t.Fatalf("预算内的等待不该出现 fail-open：\n%s", f.logs.joined())
	}
}

func TestChannelGateRPMFailOpenBeyondWaitBudget(t *testing.T) {
	f := newGateTestFixture(ChannelGateDefaults{}, 1) // 等待预算 1s
	ch := Channel{ID: 1, RPMLimit: 6}                 // 补桶 0.1 枚/秒 ⇒ 桶空后需等 10s > 预算

	for i := 0; i < ChannelGateBurst(6); i++ {
		mustAdmit(t, f.gate, ch, fmt.Sprintf("桶容内第 %d 次", i+1))
	}
	mustAdmit(t, f.gate, ch, "超预算的第 11 次")
	if got := f.waitCount(); got != 0 {
		t.Fatalf("超预算应直接 fail-open（源口径：超限即放行），不得再睡，实际 %d 次", got)
	}
	if f.logs.count("fail-open") == 0 {
		t.Fatalf("fail-open 必须留日志（否则超限变成静默失效）：\n%s", f.logs.joined())
	}
}

func TestChannelGateRPMUnlimitedIsSilent(t *testing.T) {
	f := newGateTestFixture(ChannelGateDefaults{}, DefaultChannelGateWaitSeconds)
	ch := Channel{ID: 1, Name: "c1"} // 渠道与插件缺省都 0 ⇒ 不限

	for i := 0; i < 200; i++ {
		mustAdmit(t, f.gate, ch, fmt.Sprintf("不限渠道第 %d 次", i+1))
	}
	if got := f.waitCount(); got != 0 {
		t.Fatalf("不限不该有任何等待：%d 次", got)
	}
	if got := f.gate.bucketCount(); got != 0 {
		t.Fatalf("不限不该建桶（少一层隐形门）：%d", got)
	}
	if f.logs.count("fail-open") != 0 || f.logs.count("TPM") != 0 {
		t.Fatalf("不限不该有日志：\n%s", f.logs.joined())
	}
}

// TestChannelGateRPMQuotaSpreadOverSixtyCalls 是 acceptance「rpm=60 时 1s 窗口内第 61 次被拒/等待」
// 在本包的形态：rpm=60、桶容 10（约 1 秒额度）⇒ 前 10 次立即放行，第 11 次起每次必须等满 ~1s
// （配额被摊平成 1 枚/秒），**第 61 次也不是立即放行**。
//
// 为什么这里没有「被拒」形态：RPM 侧超预算按源口径 fail-open 放行 + 记日志（见上一个用例），
// 该闸的否决形态不存在；「拒」由 TPM 承担（见 TPM 用例）。判据口径如实登记在 README 差异节。
func TestChannelGateRPMQuotaSpreadOverSixtyCalls(t *testing.T) {
	f := newGateTestFixture(ChannelGateDefaults{}, DefaultChannelGateWaitSeconds)
	ch := Channel{ID: 1, RPMLimit: 60}

	for i := 0; i < ChannelGateBurst(60); i++ {
		mustAdmit(t, f.gate, ch, fmt.Sprintf("前 10 次（第 %d 次）", i+1))
	}
	if got := f.waitCount(); got != 0 {
		t.Fatalf("前 10 次（桶容）不得等待：%d", got)
	}
	for i := ChannelGateBurst(60); i < 61; i++ {
		mustAdmit(t, f.gate, ch, fmt.Sprintf("第 %d 次", i+1))
	}
	if got := f.waitCount(); got != 51 {
		t.Fatalf("第 11..61 次应各睡一次，实际 %d 次", got)
	}
	if got := f.elapsed(); got < 50*time.Second {
		t.Fatalf("60 次调用应约摊到 50s 以上（1 枚/秒），实际 %s", got)
	}
	if f.logs.count("fail-open") != 0 {
		t.Fatalf("预算 20s 内连续调用不该 fail-open：\n%s", f.logs.joined())
	}
}

// ===== ③ TPM 窗口：扣减、拒候选、恢复 =====

func TestChannelGateTPMWindowDeniesUntilRecovered(t *testing.T) {
	f := newGateTestFixture(ChannelGateDefaults{TPM: 1000}, 0)
	ch := Channel{ID: 9, Name: "c9"} // 渠道未配 ⇒ 用插件缺省 1000

	mustAdmit(t, f.gate, ch, "窗口空闲时")
	f.gate.Charge(9, 999)
	mustAdmit(t, f.gate, ch, "已用 999 < 1000")

	f.gate.Charge(9, 1)
	ok, err := f.gate.Admit(context.Background(), ch, 0)
	if err != nil || ok {
		t.Fatalf("窗口已满（1000 ≥ 1000）应被拒：ok=%v err=%v", ok, err)
	}
	if f.logs.count("TPM 预算已用尽") == 0 {
		t.Fatalf("拒候选必须留日志（否则「静默跳过」最难查）：\n%s", f.logs.joined())
	}
	if got := f.gate.spentTokens(9); got != 1000 {
		t.Fatalf("被拒不得改动窗口（拒不是消耗）：%d", got)
	}

	f.clock.advance(61 * time.Second)
	mustAdmit(t, f.gate, ch, "窗口滚过 60s 后")
	if got := f.gate.spentTokens(9); got != 0 {
		t.Fatalf("窗口外流水应被剪掉：%d", got)
	}
}

func TestChannelGateTPMChannelLimitOverridesPluginDefault(t *testing.T) {
	f := newGateTestFixture(ChannelGateDefaults{TPM: 5000}, 0)

	limited := Channel{ID: 2, TPMLimit: 100}
	f.gate.Charge(2, 100)
	if ok, _ := f.gate.Admit(context.Background(), limited, 0); ok {
		t.Fatal("渠道 tpm_limit=100 生效时同一窗口不得被插件缺省 5000 放开")
	}

	idle := Channel{ID: 3} // 渠道未配 ⇒ 插件缺省 5000
	f.gate.Charge(3, 100)
	mustAdmit(t, f.gate, idle, "渠道未配时按插件缺省判定（100 ≪ 5000）")
}

func TestChannelGateTPMDeniedCandidateKeepsRateQuota(t *testing.T) {
	f := newGateTestFixture(ChannelGateDefaults{}, 0)
	ch := Channel{ID: 4, RPMLimit: 60, TPMLimit: 100}
	burst := float64(ChannelGateBurst(60))

	f.gate.Charge(4, 100)
	if ok, _ := f.gate.Admit(context.Background(), ch, 0); ok {
		t.Fatal("TPM 满应被拒")
	}
	// 桶未建 = 速率配额一枚都没动（判定次序：先 TPM 后 RPM）
	if got := f.gate.bucketCount(); got != 0 {
		t.Fatalf("被 TPM 拒的候选不该动用速率桶：桶数 %d", got)
	}

	f.clock.advance(61 * time.Second)
	mustAdmit(t, f.gate, ch, "TPM 恢复后")
	if got := f.gate.rateTokens(4); got != burst-1 {
		t.Fatalf("放行一次应扣一枚速率令牌：余量 %v，期望 %v", got, burst-1)
	}
}

func TestChannelGateChargeIgnoresNonPositiveAndNilReceiver(t *testing.T) {
	f := newGateTestFixture(ChannelGateDefaults{TPM: 10}, 0)
	f.gate.Charge(0, 100) // channel_id=0（本地预检）
	f.gate.Charge(1, 0)   // tokens=0（失败行）
	f.gate.Charge(1, -5)  // 非正
	if got := f.gate.spentTokens(1); got != 0 {
		t.Fatalf("非正 token / channel_id=0 不得记入窗口：%d", got)
	}
	if got := f.gate.windowCount(); got != 0 {
		t.Fatalf("无有效记录不该建窗口：%d", got)
	}

	var nilGate *ChannelGate
	nilGate.Charge(1, 10) // 不 panic
	if got := nilGate.spentTokens(1); got != 0 {
		t.Fatalf("nil 接收者读数应为 0：%d", got)
	}
	ok, err := nilGate.Admit(context.Background(), Channel{ID: 1}, 0)
	if err != nil || !ok {
		t.Fatalf("nil 准入门应放行（未装闸的等价形态）：ok=%v err=%v", ok, err)
	}
}

// ===== ④ 并发不超发 + 与按 Key 限流桶互不串扰 =====

func TestChannelGateConcurrentDoesNotOverIssue(t *testing.T) {
	const rpm = 6000 // 桶容 100
	f := newGateTestFixture(ChannelGateDefaults{RPM: rpm}, 0)
	ch := Channel{ID: 5, Name: "c5"}

	const goroutines, perGoroutine = 32, 5
	var denied, failed atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < perGoroutine; j++ {
				ok, err := f.gate.Admit(context.Background(), ch, 0)
				switch {
				case err != nil:
					failed.Add(1)
				case !ok:
					denied.Add(1)
				}
			}
		}()
	}
	wg.Wait()

	if got := failed.Load(); got != 0 {
		t.Fatalf("准入不得报错：%d", got)
	}
	if got := denied.Load(); got != 0 {
		t.Fatalf("RPM 超预算走 fail-open（放行），不该出现被拒：%d", got)
	}
	burst := ChannelGateBurst(rpm)
	if got := f.gate.rateTokens(ch.ID); got != 0 {
		t.Fatalf("并发下桶余量应为 0（不超发、不为负）：%v", got)
	}
	if got := f.gate.bucketCount(); got != 1 {
		t.Fatalf("同渠道只应有一个桶：%d", got)
	}
	// 真取到令牌的恰好是桶容那 burst 次；其余全部走 fail-open（日志可数，见 gate.go 的日志文案）。
	if got, want := f.logs.count("fail-open"), goroutines*perGoroutine-int(burst); got != want {
		t.Fatalf("fail-open 次数 = %d，期望 %d（= 总调用 - 桶容）", got, want)
	}
}

func TestChannelBucketsIndependentFromKeyRateLimitBuckets(t *testing.T) {
	// 公开面按 Key 桶（ratelimit.go）：同 rpm 值、同「桶」字样，但两套状态必须互不影响。
	layer := newRateLimitLayer()
	key := rateLimitBucketKey("sk-gate-test")
	now := time.Unix(1700000000, 0)
	for i := 0; i < 10; i++ {
		if !layer.allow(key, 60, 0, now) {
			t.Fatalf("按 Key 桶第 %d 次应放行", i+1)
		}
	}
	if layer.allow(key, 60, 0, now) {
		t.Fatal("按 Key 桶第 11 次应超限（夹具自检）")
	}

	f := newGateTestFixture(ChannelGateDefaults{RPM: 60}, 0)
	ch := Channel{ID: 6, Name: "c6"}
	mustAdmit(t, f.gate, ch, "渠道准入")
	if got := f.gate.rateTokens(ch.ID); got != float64(ChannelGateBurst(60)-1) {
		t.Fatalf("渠道桶应独立扣减：余量 %v", got)
	}
	if got := layer.bucketCount(); got != 1 {
		t.Fatalf("渠道准入不该写进按 Key 桶表：%d", got)
	}
	if got := f.gate.bucketCount(); got != 1 {
		t.Fatalf("按 Key 限流不该写进渠道桶表：%d", got)
	}
	// 反向对照：渠道准入不得改动按 Key 桶自身的状态——它仍处于自己那套配额（rpm=60）的耗尽态，
	// 说明两套状态确实各记各的（而不是「共用一张桶表」）。
	if layer.allow(key, 60, 0, now) {
		t.Fatal("按 Key 桶的状态被渠道准入改动了（两套状态必须互不串扰）")
	}
	if got := f.gate.rateTokens(ch.ID); got != float64(ChannelGateBurst(60)-1) {
		t.Fatalf("按 Key 限流不该动用渠道桶：余量 %v", got)
	}
}

// ===== ⑤ 回写挂点（circuit.go）=====

type gateTestCharge struct {
	channelID uint64
	tokens    int
}

type gateTestCharger struct {
	mu    sync.Mutex
	calls []gateTestCharge
}

func (c *gateTestCharger) Charge(channelID uint64, tokens int) {
	c.mu.Lock()
	c.calls = append(c.calls, gateTestCharge{channelID: channelID, tokens: tokens})
	c.mu.Unlock()
}

func (c *gateTestCharger) snapshot() []gateTestCharge {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]gateTestCharge, len(c.calls))
	copy(out, c.calls)
	return out
}

func TestCircuitRecordTotalsChargerHookConditions(t *testing.T) {
	repo := newGateTestRepo(gateTestChatModel())
	circuit := NewCircuit(repo)
	charger := &gateTestCharger{}
	circuit.charger = charger
	ctx := context.Background()

	// ① 成功路径：tokens>0 且 channel_id>0 ⇒ 恰回调一次，值与累计同源
	if err := circuit.RecordTotals(ctx, 7, true, 123, 100); err != nil {
		t.Fatal(err)
	}
	if got := charger.snapshot(); len(got) != 1 || got[0] != (gateTestCharge{channelID: 7, tokens: 123}) {
		t.Fatalf("回调 = %+v，期望一次 {7 123}", got)
	}

	// ② 失败行：tokens=0 ⇒ 不扣（这次尝试没有花上游 token）
	if err := circuit.RecordTotals(ctx, 7, false, 0, 100); err != nil {
		t.Fatal(err)
	}
	if got := charger.snapshot(); len(got) != 1 {
		t.Fatalf("tokens=0 不得扣减：%+v", got)
	}

	// ③ 本地预检：channel_id=0 ⇒ 不回调、也不写累计
	if err := circuit.RecordTotals(ctx, 0, false, 0, 100); err != nil {
		t.Fatal(err)
	}
	if err := circuit.RecordTotals(ctx, 0, true, 500, 100); err != nil {
		t.Fatal(err)
	}
	if got := charger.snapshot(); len(got) != 1 {
		t.Fatalf("channel_id=0 不得扣减（含 tokens>0 的情形）：%+v", got)
	}
	if len(repo.totals) != 2 {
		t.Fatalf("累计写库 = %+v，期望 2 次（channel_id=0 跳过）", repo.totals)
	}

	// ④ charger 为 nil ⇒ 行为与现状逐字一致（仍写累计、不 panic）；
	//    circuit_test.go 的 TestRecordTotals 既是这条的既有断言，也在本批复跑（见验收命令）。
	bare := NewCircuit(repo)
	if err := bare.RecordTotals(ctx, 7, true, 5, 100); err != nil {
		t.Fatal(err)
	}
	if len(repo.totals) != 3 || repo.totals[2].tokens != 5 || !repo.totals[2].success {
		t.Fatalf("nil charger 下累计 = %+v", repo.totals)
	}

	// ⑤ nil Circuit（防御）与 nil store 都不回调、也不 panic
	var nilCircuit *Circuit
	if err := nilCircuit.RecordTotals(ctx, 7, true, 5, 100); err != nil {
		t.Fatal(err)
	}
	if got := charger.snapshot(); len(got) != 1 {
		t.Fatalf("nil Circuit 不得回调：%+v", got)
	}
}

// ===== ⑥ 装配：installChannelGate =====

func TestInstallChannelGateMountsBothSeams(t *testing.T) {
	repo := newGateTestRepo(gateTestChatModel(), gateTestCandidate(1, 1))
	driver := &gateTestDriver{chat: &ChatResult{Content: "ok"}}
	newService := func() *Service {
		return NewService(repo, driver, Config{RequestTimeout: 30, StreamTimeout: 30, Currency: "USD"}, nil)
	}

	gate := NewChannelGate()
	service := newService()
	installChannelGate(service, gate)
	if service.router.gate != Gate(gate) {
		t.Fatalf("router.gate 未挂上：%#v", service.router.gate)
	}
	if service.breaker.charger != TokenCharger(gate) {
		t.Fatalf("breaker.charger 未挂上：%#v", service.breaker.charger)
	}

	// nil ⇒ 两处都不挂（准入恒放行、RecordTotals 不回调）
	plain := newService()
	installChannelGate(plain, nil)
	if plain.router.gate != nil || plain.breaker.charger != nil {
		t.Fatalf("gate=nil 时不得挂载：gate=%#v charger=%#v", plain.router.gate, plain.breaker.charger)
	}

	// typed-nil（Go 陷阱）：(*ChannelGate)(nil) 装进接口会变成「非 nil 的接口」⇒ 必须挡在挂载处
	var typedNil *ChannelGate
	typedNilService := newService()
	installChannelGate(typedNilService, typedNil)
	if typedNilService.router.gate != nil {
		t.Fatal("typed-nil 不得挂成非 nil 接口（否则 Router.Admit 会调空指针）")
	}
	if typedNilService.breaker.charger != nil {
		t.Fatal("typed-nil 不得挂到 charger")
	}
}

// ===== ⑦ Service 级集成（假 repo + 假驱动）=====

// gateTestTotal 是渠道累计写口的记账探针。
type gateTestTotal struct {
	channelID uint64
	success   bool
	tokens    int
}

// gateTestRepo 是 Repository 的无库假实现（读面返回注入值，写面记账）。
type gateTestRepo struct {
	model      Model
	candidates []Candidate
	usages     []Usage
	totals     []gateTestTotal
}

func newGateTestRepo(mdl Model, candidates ...Candidate) *gateTestRepo {
	return &gateTestRepo{model: mdl, candidates: candidates}
}

func (f *gateTestRepo) FindModel(_ context.Context, name string) (Model, error) {
	if f.model.Name == "" || f.model.Name != name {
		return Model{}, ErrModelNotFound
	}
	return f.model, nil
}

func (f *gateTestRepo) Candidates(context.Context, uint64) ([]Candidate, error) {
	return f.candidates, nil
}

func (f *gateTestRepo) UpsertUsage(_ context.Context, usage *Usage) error {
	if usage != nil {
		f.usages = append(f.usages, *usage)
	}
	return nil
}

func (f *gateTestRepo) EnterCAS(context.Context, uint64, int64) (bool, error) { return true, nil }

func (f *gateTestRepo) ReselectStaleProbe(context.Context, uint64, int64, int64) (bool, error) {
	return true, nil
}

func (f *gateTestRepo) MarkFailure(context.Context, uint64, string, int64) (int, error) {
	return 1, nil
}

func (f *gateTestRepo) MarkSuccess(context.Context, uint64, int64) (bool, error) { return true, nil }

func (f *gateTestRepo) OpenCircuit(context.Context, uint64, int64) (bool, error) { return true, nil }

func (f *gateTestRepo) ManualOpenCircuit(context.Context, uint64, int64) (bool, error) {
	return true, nil
}

func (f *gateTestRepo) ManualCloseCircuit(context.Context, uint64, int64) (int64, error) {
	return 1, nil
}

func (f *gateTestRepo) AddChannelTotals(_ context.Context, channelID uint64, success bool, tokens int, _ int64) error {
	f.totals = append(f.totals, gateTestTotal{channelID: channelID, success: success, tokens: tokens})
	return nil
}

func (f *gateTestRepo) InsertBreakerEvent(context.Context, *BreakerEvent) error { return nil }

// gateTestDriver 是 Driver 的无网假实现：按渠道注入结果/错误，并记录被调用的渠道（换道断言用）。
type gateTestDriver struct {
	chat         *ChatResult
	chatErr      error
	chatErrs     map[int64]error
	stream       *ChatResult
	streamChunk  []byte
	streamErr    error
	embedding    *EmbeddingResult
	embeddingErr error
	// streamPartialWithErr 只在 streamErr 非空时生效：返回「有块、无收尾」形状（res 非空 + 错误），
	// 供分支③（部分计费）的预算扣减用例使用；nil 时行为与既有用例逐字一致（返回 nil 结果）。
	streamPartialWithErr *ChatResult

	chatChannels   []int64
	streamChannels []int64
	embedChannels  []int64
}

func (d *gateTestDriver) Chat(_ context.Context, ch Channel, _ ChatRequest) (*ChatResult, error) {
	d.chatChannels = append(d.chatChannels, ch.ID)
	if err := d.chatErrs[ch.ID]; err != nil {
		return nil, err
	}
	if d.chatErr != nil {
		return nil, d.chatErr
	}
	return d.chat, nil
}

func (d *gateTestDriver) ChatStream(_ context.Context, ch Channel, _ ChatRequest, sink func([]byte) error) (*ChatResult, error) {
	d.streamChannels = append(d.streamChannels, ch.ID)
	if d.streamErr != nil {
		// 「有块、无收尾」：驱动把已聚合的结果连同错误一起交回（生产驱动在客户端断开 /
		// 上游中途断连时正是这样返回，见 driver.go 的 ChatStream）。
		if d.streamPartialWithErr != nil {
			return d.streamPartialWithErr, d.streamErr
		}
		return nil, d.streamErr
	}
	if sink != nil && len(d.streamChunk) > 0 {
		if err := sink(d.streamChunk); err != nil {
			return nil, err
		}
	}
	return d.stream, nil
}

func (d *gateTestDriver) Embeddings(_ context.Context, ch Channel, _ EmbeddingRequest) (*EmbeddingResult, error) {
	d.embedChannels = append(d.embedChannels, ch.ID)
	if d.embeddingErr != nil {
		return nil, d.embeddingErr
	}
	return d.embedding, nil
}

func (d *gateTestDriver) Models(context.Context, Channel) ([]string, error) {
	return []string{"m1"}, nil
}

func gateTestChatModel() Model {
	return Model{
		ID: 7, Name: "m1", Type: CallTypeChat, Status: StatusEnabled,
		PriceInput: "1.000000", PriceOutput: "2.000000",
	}
}

func gateTestCandidate(channelID uint64, priority int) Candidate {
	return Candidate{
		ChannelID:       channelID,
		Name:            fmt.Sprintf("c%d", channelID),
		ChannelStatus:   StatusEnabled,
		BindingStatus:   StatusEnabled,
		BindingPriority: priority,
		BindingWeight:   1,
		// Circuit 必须显式给 closed：熔断对未知状态 fail-closed（circuit.go 的 Decide 默认分支），
		// 留空会让候选在熔断那一层就被拒（既有夹具同口径，见 handler_test.go 的候选构造）。
		Circuit: CircuitClosed,
	}
}

func gateTestChatInput() ChatInput {
	return ChatInput{
		Model:     "m1",
		Messages:  []Message{{Role: "user", Content: "hi"}},
		BizType:   "api",
		Source:    "test",
		RequestID: "req-gate-chat",
	}
}

// newGateServiceFixture 装配「假 repo + 假驱动 + 可选准入门」的 Service。
func newGateServiceFixture(t *testing.T, mdl Model, candidates []Candidate, driver Driver, f *gateTestFixture) (*gateTestRepo, *Service) {
	t.Helper()
	repo := newGateTestRepo(mdl, candidates...)
	service := NewService(repo, driver, Config{RequestTimeout: 30, StreamTimeout: 30, Currency: "USD"}, nil)
	if f != nil {
		installChannelGate(service, f.gate)
	}
	return repo, service
}

func TestServiceChatChargesChannelGateTokens(t *testing.T) {
	driver := &gateTestDriver{chat: &ChatResult{
		ID: "chat-1", Content: "hello", PromptTokens: 120, CompletionTokens: 30, UsageSeen: true, LatencyMS: 3,
	}}
	f := newGateTestFixture(ChannelGateDefaults{}, 0)
	repo, service := newGateServiceFixture(t, gateTestChatModel(), []Candidate{gateTestCandidate(1, 1)}, driver, f)

	if _, err := service.Chat(context.Background(), gateTestChatInput()); err != nil {
		t.Fatalf("Chat 应成功：%v", err)
	}
	if len(repo.usages) != 1 {
		t.Fatalf("应有 1 行 usage：%+v", repo.usages)
	}
	usage := repo.usages[0]
	if usage.ChannelID != 1 || usage.Attempt != 0 || usage.Status != UsageStatusSuccess || usage.TotalTokens != 150 {
		t.Fatalf("usage = %+v，期望 channel=1 attempt=0 success total=150", usage)
	}
	if got := f.gate.spentTokens(1); got != int64(usage.TotalTokens) {
		t.Fatalf("渠道预算扣减 = %d，期望等于该次 counts.TotalTokens = %d（B1：成功路径必然扣减）", got, usage.TotalTokens)
	}
	if len(repo.totals) != 1 || repo.totals[0].channelID != 1 || repo.totals[0].tokens != 150 || !repo.totals[0].success {
		t.Fatalf("渠道累计 = %+v", repo.totals)
	}
}

func TestServiceChatStreamChargesChannelGateTokens(t *testing.T) {
	driver := &gateTestDriver{
		stream: &ChatResult{
			ID: "stream-1", Content: "hi", PromptTokens: 80, CompletionTokens: 20, UsageSeen: true, Chunks: 1, LatencyMS: 2,
		},
		streamChunk: []byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n"),
	}
	f := newGateTestFixture(ChannelGateDefaults{}, 0)
	repo, service := newGateServiceFixture(t, gateTestChatModel(), []Candidate{gateTestCandidate(1, 1)}, driver, f)

	var frames [][]byte
	if _, err := service.ChatStream(context.Background(), gateTestChatInput(), func(data []byte) error {
		frames = append(frames, data)
		return nil
	}); err != nil {
		t.Fatalf("ChatStream 应成功：%v", err)
	}
	if len(frames) != 1 {
		t.Fatalf("应原样转发 1 帧：%d", len(frames))
	}
	if len(repo.usages) != 1 {
		t.Fatalf("应有 1 行 usage：%+v", repo.usages)
	}
	usage := repo.usages[0]
	if usage.Stream != 1 || usage.TokensSource != TokensSourceUsage || usage.TotalTokens != 100 {
		t.Fatalf("usage = %+v，期望 stream=1 usage 口径 total=100", usage)
	}
	if got := f.gate.spentTokens(1); got != int64(usage.TotalTokens) {
		t.Fatalf("流式成功路径应扣 %d，实际 %d", usage.TotalTokens, got)
	}
}

func TestServiceEmbeddingsChargesChannelGateTokens(t *testing.T) {
	mdl := gateTestChatModel()
	mdl.Type = CallTypeEmbedding
	driver := &gateTestDriver{embedding: &EmbeddingResult{
		Model: "m1",
		Data: []Embedding{
			{Index: 0, Object: "embedding", Vector: []float64{0.1, 0.2}},
			{Index: 1, Object: "embedding", Vector: []float64{0.3, 0.4}},
		},
		PromptTokens: 40, UsageSeen: true,
	}}
	f := newGateTestFixture(ChannelGateDefaults{}, 0)
	repo, service := newGateServiceFixture(t, mdl, []Candidate{gateTestCandidate(1, 1)}, driver, f)

	in := EmbeddingInput{Model: "m1", Input: []string{"a", "b"}, BizType: "api", Source: "test", RequestID: "req-gate-emb"}
	if _, err := service.Embeddings(context.Background(), in); err != nil {
		t.Fatalf("Embeddings 应成功：%v", err)
	}
	if len(repo.usages) != 1 {
		t.Fatalf("应有 1 行 usage：%+v", repo.usages)
	}
	usage := repo.usages[0]
	if usage.CallType != CallTypeEmbedding || usage.TotalTokens != 40 || usage.ChannelID != 1 {
		t.Fatalf("usage = %+v，期望 embedding/channel=1/total=40", usage)
	}
	if got := f.gate.spentTokens(1); got != int64(usage.TotalTokens) {
		t.Fatalf("embedding 成功路径应扣 %d，实际 %d", usage.TotalTokens, got)
	}
}

// TestServicePartialBilledStreamChargesChannelGateTokens 是「有块、无收尾」部分计费路径的预算回归：
// 分支③已按 counts 写 usage（billed_partial=1、tokens>0、cost>0）⇒ 同一份 counts 必须进渠道 TPM 扣减
// （gate.go:20-21「上游真的花了多少就扣多少」、README 的 TPM 口径）。
// 修复前这里 RecordTotals 收到 tokens=0 ⇒ 渠道预算一分不扣，配额在该路径被系统性少计。
func TestServicePartialBilledStreamChargesChannelGateTokens(t *testing.T) {
	partial := &ChatResult{
		ID: "stream-partial", Content: "半", Chunks: 2, LatencyMS: 2,
		PromptTokens: 80, CompletionTokens: 20, UsageSeen: true,
	}
	driver := &gateTestDriver{
		streamErr:            errors.New("上游中断"),
		streamPartialWithErr: partial,
	}
	f := newGateTestFixture(ChannelGateDefaults{}, 0)
	repo, service := newGateServiceFixture(t, gateTestChatModel(), []Candidate{gateTestCandidate(1, 1)}, driver, f)

	if _, err := service.ChatStream(context.Background(), gateTestChatInput(), func([]byte) error { return nil }); err == nil {
		t.Fatal("有块无收尾必须向上冒错误（失败语义不变）")
	}
	if len(repo.usages) != 1 {
		t.Fatalf("分支③必须恰落一行账: %+v", repo.usages)
	}
	usage := repo.usages[0]
	if usage.BilledPartial != 1 || usage.TotalTokens != 100 || usage.Status == UsageStatusSuccess {
		t.Fatalf("部分计费行 = %+v，期望 billed_partial=1 / total=100 / status 非 success", usage)
	}
	if got := f.gate.spentTokens(1); got != int64(usage.TotalTokens) {
		t.Fatalf("部分计费行必须扣 %d token，实际 %d（修复前恒 0）", usage.TotalTokens, got)
	}
	// 渠道累计口径不变：失败行仍只累加 total_failures（AddChannelTotals 忽略 tokens）。
	if len(repo.totals) != 1 || repo.totals[0].channelID != 1 || repo.totals[0].success || repo.totals[0].tokens != 100 {
		t.Fatalf("渠道累计 = %+v（期望 channel=1 success=false tokens=100）", repo.totals)
	}
}

func TestServiceFailureDoesNotChargeChannelGateTokens(t *testing.T) {
	t.Run("非流式失败", func(t *testing.T) {
		driver := &gateTestDriver{chatErr: errors.New("upstream down")}
		f := newGateTestFixture(ChannelGateDefaults{}, 0)
		repo, service := newGateServiceFixture(t, gateTestChatModel(), []Candidate{gateTestCandidate(1, 1)}, driver, f)

		if _, err := service.Chat(context.Background(), gateTestChatInput()); err == nil {
			t.Fatal("上游失败应返回错误")
		}
		if got := f.gate.spentTokens(1); got != 0 {
			t.Fatalf("失败路径（tokens=0）不得扣预算：%d", got)
		}
		if got := f.gate.windowCount(); got != 0 {
			t.Fatalf("失败路径不该建 TPM 窗口：%d", got)
		}
		if len(repo.totals) != 1 || repo.totals[0].tokens != 0 || repo.totals[0].success {
			t.Fatalf("失败行的渠道累计 = %+v，期望 tokens=0 且 success=false", repo.totals)
		}
	})

	t.Run("流式无块失败", func(t *testing.T) {
		driver := &gateTestDriver{streamErr: errors.New("dial tcp 127.0.0.1:9000: connect: connection refused")}
		f := newGateTestFixture(ChannelGateDefaults{}, 0)
		_, service := newGateServiceFixture(t, gateTestChatModel(), []Candidate{gateTestCandidate(1, 1)}, driver, f)

		if _, err := service.ChatStream(context.Background(), gateTestChatInput(), func([]byte) error { return nil }); err == nil {
			t.Fatal("无块流式失败应返回错误")
		}
		if got := f.gate.spentTokens(1); got != 0 {
			t.Fatalf("无块失败（tokens=0）不得扣预算：%d", got)
		}
	})
}

// TestServiceTPMSkippedCandidateFallsThrough 是 acceptance 的核心集成断言：
// 两候选时第一个被 TPM 跳过 ⇒ 第二个承接；被跳过**不是一次真实尝试**（usage 的 attempt 保持 0），
// 与「真失败换道」的 attempt 递增序列形成对照。
func TestServiceTPMSkippedCandidateFallsThrough(t *testing.T) {
	candidates := []Candidate{gateTestCandidate(1, 1), gateTestCandidate(2, 2)}
	candidates[0].TPMLimit = 100 // 第一候选的窗口上限 100

	driver := &gateTestDriver{chat: &ChatResult{
		Content: "ok", PromptTokens: 10, CompletionTokens: 5, UsageSeen: true,
	}}
	f := newGateTestFixture(ChannelGateDefaults{}, 0)
	repo, service := newGateServiceFixture(t, gateTestChatModel(), candidates, driver, f)

	// 把第一候选的 TPM 窗口灌满（窗口判据：已用 ≥ tpm ⇒ 跳过该候选）
	f.gate.Charge(1, 100)

	if _, err := service.Chat(context.Background(), gateTestChatInput()); err != nil {
		t.Fatalf("第二候选应承接：%v", err)
	}
	if len(driver.chatChannels) != 1 || driver.chatChannels[0] != 2 {
		t.Fatalf("上游只该被第二候选打到：%v", driver.chatChannels)
	}
	if len(repo.usages) != 1 {
		t.Fatalf("被跳过的候选不该写 usage（跳 ≠ 一次尝试）：%+v", repo.usages)
	}
	usage := repo.usages[0]
	if usage.ChannelID != 2 || usage.Attempt != 0 {
		t.Fatalf("跳过不自增 attempt：usage = %+v，期望 channel=2 attempt=0", usage)
	}
	if got := f.gate.spentTokens(2); got != int64(usage.TotalTokens) {
		t.Fatalf("承接候选应扣 %d，实际 %d", usage.TotalTokens, got)
	}
	if got := f.gate.spentTokens(1); got != 100 {
		t.Fatalf("被跳过的候选不该追加扣减：%d（应仍为初始灌入的 100）", got)
	}

	// 对照组：第一候选**真失败**（非跳过）⇒ 现有降级口径 = attempt 0 失败行 + attempt 1 成功行
	controlDriver := &gateTestDriver{
		chat:     &ChatResult{Content: "ok", PromptTokens: 10, CompletionTokens: 5, UsageSeen: true},
		chatErrs: map[int64]error{1: errors.New("upstream 500")},
	}
	controlGate := newGateTestFixture(ChannelGateDefaults{}, 0)
	controlRepo, controlService := newGateServiceFixture(t, gateTestChatModel(), candidates, controlDriver, controlGate)
	if _, err := controlService.Chat(context.Background(), gateTestChatInput()); err != nil {
		t.Fatalf("对照组第二候选应承接：%v", err)
	}
	if len(controlDriver.chatChannels) != 2 || controlDriver.chatChannels[0] != 1 || controlDriver.chatChannels[1] != 2 {
		t.Fatalf("对照组应逐候选尝试：%v", controlDriver.chatChannels)
	}
	if len(controlRepo.usages) != 2 {
		t.Fatalf("对照组应有两条 usage（失败 attempt=0 + 成功 attempt=1）：%+v", controlRepo.usages)
	}
	if controlRepo.usages[0].Attempt != 0 || controlRepo.usages[0].Status == UsageStatusSuccess ||
		controlRepo.usages[1].Attempt != 1 || controlRepo.usages[1].Status != UsageStatusSuccess {
		t.Fatalf("对照组的 attempt 序列/状态 = %+v", controlRepo.usages)
	}
	if got := controlGate.gate.spentTokens(1); got != 0 {
		t.Fatalf("失败尝试（tokens=0）不该扣：%d", got)
	}
}

// TestServiceGateNilAndAllowAllMatchBaseline 覆盖 acceptance「Gate=nil / Gate 全放行 两种配置下
// 既有用例结果不变」：同一场景两种装配的 usage 关键列必须逐项相同（时间戳与 request_id 之外）。
func TestServiceGateNilAndAllowAllMatchBaseline(t *testing.T) {
	run := func(t *testing.T, f *gateTestFixture) Usage {
		t.Helper()
		driver := &gateTestDriver{chat: &ChatResult{Content: "hello", PromptTokens: 120, CompletionTokens: 30, UsageSeen: true}}
		repo, service := newGateServiceFixture(t, gateTestChatModel(), []Candidate{gateTestCandidate(1, 1)}, driver, f)
		if _, err := service.Chat(context.Background(), gateTestChatInput()); err != nil {
			t.Fatalf("Chat 应成功：%v", err)
		}
		if len(repo.usages) != 1 {
			t.Fatalf("应有 1 行 usage：%+v", repo.usages)
		}
		return repo.usages[0]
	}

	baseline := run(t, nil) // 未装闸
	allowAll := run(t, newGateTestFixture(ChannelGateDefaults{}, DefaultChannelGateWaitSeconds))

	if baseline.ChannelID != allowAll.ChannelID || baseline.Attempt != allowAll.Attempt ||
		baseline.Status != allowAll.Status || baseline.TokensSource != allowAll.TokensSource ||
		baseline.CallType != allowAll.CallType ||
		baseline.PromptTokens != allowAll.PromptTokens || baseline.CompletionTokens != allowAll.CompletionTokens ||
		baseline.TotalTokens != allowAll.TotalTokens || baseline.Cost != allowAll.Cost {
		t.Fatalf("Gate=nil 与 Gate 全放行的 usage 不一致：\n nil=%+v\n gate=%+v", baseline, allowAll)
	}
}
