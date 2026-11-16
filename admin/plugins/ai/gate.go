// plugin: ai v0.2.0
// gate.go 是渠道级准入（RPM / TPM）的唯一实现，挂在 router.go 的 Gate 接缝上（Router.gate）。
//
// 语义（逐条对齐源 app/ai/support/ChannelGate.php，只读对照、不搬代码）：
//   - RPM：令牌桶。上限三级取值「渠道 rpm_limit → 插件缺省 channel_gate_rpm_default → 0 = 不限」；
//     桶容 = max(10, ceil(rpm/60))（约 1 秒额度，源 ChannelGate::burstOf）；桶空**睡到回填**（不轮询）；
//     等待总预算超限即 **fail-open 放行 + 记日志**（源 :156-192 / :340 口径：闸是配额保护，
//     不是正确性依赖，绝不死锁消费槽）。
//   - TPM：进程内按渠道维护「最近 60 秒已消耗 token」窗口，上限三级取值
//     「渠道 tpm_limit → 插件缺省 channel_gate_tpm_default → 0 = 不限」；窗口已满 ⇒ Admit 返回
//     (false, nil) ⇒ 调用方换下一候选、**不自增 attempt**（与熔断拒绝同形）。
//     扣减发生在每次真实尝试之后：唯一写口是 Circuit.RecordTotals（见 circuit.go 的说明与注释），
//     故本包的 installChannelGate 把同一个实例挂到 breaker.charger 上。
//
// 两重限制（如实登记，与源的能力差）：
//
//	① **单实例**：桶与窗口都在本进程内存 ⇒ 多副本各限各的（实际上限 ≈ 配置 × 副本数）、重启清零；
//	   源用 Redis（Lua 原子）跨进程共享。跨进程强一致需要 redis 客户端，宿主 go.mod 无该依赖（不引入额外依赖）。
//	② **事后扣减**：TPM 是「上游真的花了多少就扣多少」，不是事前预约 ⇒ 同一分钟窗口内并发在飞的
//	   请求可以超额（超出部分会在后续调度里表现为「已用 ≥ 上限 ⇒ 跳过该候选」）。
//	   源是事前 reserve + 结算多退少补（ChannelGate::reserveTokens / settleTokens，Lua 原子）。
//
// 其它口径差异（汇总于 README 差异节）：
//   - 源 RPM 缺省 1000，本插件的插件级缺省是 0 = 不限（少一道隐形门，设置页可见）；
//   - 源的 TPM 闸「等不到额度就 fail-open 放行」，本包改为**跳过该候选**（更强的配额保护）；
//     代价是候选全被跳过时返回本地 all_unavailable，而不是硬打上游；
//   - 源按「渠道名/厂商前缀」配置 map（精确名 → 最长前缀 → 缺省），本插件的配额落在渠道行两列
//     （rpm_limit/tpm_limit，迁移 3），无前缀匹配那一级。
package ai

import (
	"context"
	"fmt"
	"log"
	"math"
	"sync"
	"time"

	"github.com/ai-go-hub/ai-go-admin/internal/infra/database"
	"github.com/ai-go-hub/ai-go-admin/internal/model"

	"gorm.io/gorm"
)

// 准入门两键的落库名（安装器写 configs：plugin_ai_<键>，分组 plugin.ai）。
// plugin.yaml 的 configs、settingsView 回显与 settingsSave 白名单由装配面登记（本文件只读）。
const (
	configKeyChannelGateRPMDefault = "plugin_ai_channel_gate_rpm_default"
	configKeyChannelGateTPMDefault = "plugin_ai_channel_gate_tpm_default"
)

// channelGateConfigKeys 两键的声明顺序（读与回显共用一份，避免两处漂移）。
var channelGateConfigKeys = []string{configKeyChannelGateRPMDefault, configKeyChannelGateTPMDefault}

const (
	// DefaultChannelGateWaitSeconds 是 RPM 等待预算的代码缺省。
	// 与源 channel_gate.wait_seconds 的缺省一致（源 ChannelGate.php:85 的 WAIT_DEFAULT=20，最小 1s）。
	// 不单设额外配置键（仅给两键）：预算要调只能改这里，属已知代价。
	DefaultChannelGateWaitSeconds = 20

	// channelGateConfigCacheTTL 是两键的进程内缓存上限（与公开面限流层同口径：改设置后最迟 5s 生效）。
	channelGateConfigCacheTTL = 5 * time.Second

	// channelGateTokenWindowSeconds 是 TPM 窗口长度：60s（TPM 的定义就是「每分钟 token 数」）。
	channelGateTokenWindowSeconds int64 = 60

	// channelGateWaitPollCap 是等待循环的硬上限：正常路径每次睡眠都推进时钟、注定在预算内收敛，
	// 这一层只防「注入的 sleep 不推进时钟」这类夹具把循环变成死转（防御性，不是业务判据）。
	channelGateWaitPollCap = 64

	// 两键的取值上限（手滑填 1e9 不该变成隐性资源问题；与限流层 boundedInt 的 clamp 同口径）。
	channelGateRPMMax = 1000000
	channelGateTPMMax = 1000000000

	// channelGateBucketTTL / channelGateSweepInterval 是桶表与窗口表的惰性清扫参数
	// （宿主先例 internal/middleware/throttle.go:41-48，同包先例 ratelimit.go 的惰性清扫）。
	channelGateBucketTTL     = time.Hour
	channelGateSweepInterval = 5 * time.Minute
)

// ChannelGateDefaults 是插件级缺省（configs 两键；<=0 一律视为未配）。
//
// 与源的两点差别：源 RPM 缺省 1000 / TPM 缺省 0；本插件两者都缺省 0 = 不限
// （TPM 缺省 0 与源一致；RPM 差异见文件头）。
type ChannelGateDefaults struct {
	RPM int
	TPM int
}

// ResolveChannelGateRPM 是渠道 RPM 上限的唯一求值链：渠道 rpm_limit > 0 用自身 →
// 否则用插件缺省 channel_gate_rpm_default（<=0 视为未配）→ 仍未配 0 = 不限。
//
// 展示面（渠道列表的生效值）与运行面（本函数）必须走同一条链，否则「设置面回显」与
// 「实际是否限速」会各说各话（与熔断 ResolveFailThreshold 同一条纪律）。
func ResolveChannelGateRPM(channelValue, pluginDefault int) int {
	if channelValue > 0 {
		return channelValue
	}
	if pluginDefault > 0 {
		return pluginDefault
	}
	return 0
}

// ResolveChannelGateTPM 是渠道 TPM 上限的唯一求值链（同 ResolveChannelGateRPM 的三级回落）。
func ResolveChannelGateTPM(channelValue, pluginDefault int) int {
	if channelValue > 0 {
		return channelValue
	}
	if pluginDefault > 0 {
		return pluginDefault
	}
	return 0
}

// ChannelGateBurst 是 RPM 桶容：max(10, ceil(rpm/60))（源 burstOf 的自动值）。
//
// 复用公开面限流层（ratelimit.go）的同一式：桶容口径是跨层共用的常量，
// 两处各写一遍必然漂移。
func ChannelGateBurst(rpm int) int {
	return rateLimitBurstFor(rpm, 0)
}

// ===== 两键的读出口（唯一读口 + 5s TTL 进程内缓存）=====

var (
	channelGateConfigMu       sync.RWMutex
	channelGateConfigCached   ChannelGateDefaults
	channelGateConfigExpireAt time.Time
)

// loadChannelGateDefaults 读两键并解析成缺省值（命中未过期缓存即返回）。
func loadChannelGateDefaults(ctx context.Context) (ChannelGateDefaults, error) {
	channelGateConfigMu.RLock()
	if !channelGateConfigExpireAt.IsZero() && time.Now().Before(channelGateConfigExpireAt) {
		cached := channelGateConfigCached
		channelGateConfigMu.RUnlock()
		return cached, nil
	}
	channelGateConfigMu.RUnlock()

	values, err := readChannelGateConfigValues(ctx)
	if err != nil {
		return ChannelGateDefaults{}, err
	}
	defaults := parseChannelGateDefaults(values)

	channelGateConfigMu.Lock()
	channelGateConfigCached, channelGateConfigExpireAt = defaults, time.Now().Add(channelGateConfigCacheTTL)
	channelGateConfigMu.Unlock()
	return defaults, nil
}

// invalidateChannelGateConfig 立即失效准入门配置缓存（settings/save 成功后可选调用：不调用则最迟 5s 生效）。
func invalidateChannelGateConfig() {
	channelGateConfigMu.Lock()
	channelGateConfigExpireAt = time.Time{}
	channelGateConfigMu.Unlock()
}

// readChannelGateConfigValues 读两键的现值（只读本插件的键）。
func readChannelGateConfigValues(ctx context.Context) (map[string]string, error) {
	db := database.DB()
	if db == nil {
		return nil, fmt.Errorf("%w（准入门配置读取须在 database.Init() 之后）", ErrStoreUnavailable)
	}
	rows, err := gorm.G[model.Config](db).Where("name IN ?", channelGateConfigKeys).Find(ctx)
	if err != nil {
		return nil, err
	}
	values := make(map[string]string, len(rows))
	for _, row := range rows {
		if row.Value != nil {
			values[row.Name] = *row.Value
		}
	}
	return values, nil
}

// parseChannelGateDefaults 把 configs 现值解析成缺省值（纯函数，可单测）：0/负/空/非法一律 0 = 不限。
func parseChannelGateDefaults(values map[string]string) ChannelGateDefaults {
	return ChannelGateDefaults{
		RPM: boundedInt(values[configKeyChannelGateRPMDefault], channelGateRPMMax),
		TPM: boundedInt(values[configKeyChannelGateTPMDefault], channelGateTPMMax),
	}
}

// ===== 桶与窗口（进程内状态）=====

// channelRateBucket 是单个渠道的 RPM 令牌桶（tokens 用 float64 承载补桶的小数部分）。
type channelRateBucket struct {
	rpm      int
	tokens   float64
	lastSeen time.Time
}

// channelTokenStamp 是一次真实尝试的 token 消耗记录（TPM 窗口成员）。
type channelTokenStamp struct {
	at     int64
	tokens int64
}

// channelTokenWindow 是渠道的 TPM 窗口：最近 channelGateTokenWindowSeconds 秒内的消耗流水。
//
// 为什么用「窗口内求和」而不是源侧的令牌桶 + 多退少补：本包的扣减是**事后**的
// （RecordTotals 拿到的就是上游回传的真实 token 数，既无需预约也无需退款），
// 于是「窗口内已用 ≥ 每分钟额度 ⇒ 跳过」就是自然判据，且最坏跳过时长被窗口长度封顶为 60s
// （源侧 force 结算允许把桶扣成负数，能把渠道压住远超 60s——那对「事后扣减」是本可避免的风险）。
type channelTokenWindow struct {
	stamps []channelTokenStamp
}

// prune 丢弃窗口外的流水（cutoff = now - 窗口长度）。
func (w *channelTokenWindow) prune(cutoff int64) {
	kept := w.stamps[:0]
	for _, stamp := range w.stamps {
		if stamp.at > cutoff {
			kept = append(kept, stamp)
		}
	}
	w.stamps = kept
}

// spent 返回窗口内（at > cutoff）的 token 合计。
func (w *channelTokenWindow) spent(cutoff int64) int64 {
	var total int64
	for _, stamp := range w.stamps {
		if stamp.at > cutoff {
			total += stamp.tokens
		}
	}
	return total
}

// ChannelGate 是渠道级准入门（RPM 令牌桶 + TPM 窗口）。
//
// 并发安全：桶/窗口的读写都在 mu 下（准入门在候选遍历里被逐候选调用，且同一渠道会被并发请求命中）。
// now / sleep / defaults / logf / waitSeconds 是测试注入点，生产值由 NewChannelGate 装配。
type ChannelGate struct {
	mu      sync.Mutex
	buckets map[int64]*channelRateBucket
	windows map[int64]*channelTokenWindow

	lastSweep   time.Time
	defaults    func(ctx context.Context) (ChannelGateDefaults, error)
	now         func() time.Time
	sleep       func(d time.Duration)
	logf        func(format string, args ...any)
	waitSeconds int
}

// NewChannelGate 构造准入门（生产装配：真时钟 + 真睡眠 + 读 configs 两键 + 标准日志）。
func NewChannelGate() *ChannelGate {
	return &ChannelGate{
		buckets:     make(map[int64]*channelRateBucket),
		windows:     make(map[int64]*channelTokenWindow),
		defaults:    loadChannelGateDefaults,
		now:         time.Now,
		sleep:       time.Sleep,
		logf:        log.Printf,
		waitSeconds: DefaultChannelGateWaitSeconds,
	}
}

// Admit 是 Gate 接缝的实现：判定该候选能否承接本次调用。
//
//	(false, nil) ⇒ 跳过该候选（换下一候选、不自增 attempt）；本包只在 TPM 窗口已满时如此。
//	(true, nil)  ⇒ 放行（RPM 等待超预算走 fail-open，也是放行）。
//	err          ⇒ 目前只有 ctx 取消（调用方按「存储故障 ⇒ 跳过该候选」同形处理）。
//
// now 与熔断 Circuit.Enter 同签名（调用方给的秒级时刻）；本门的桶补/窗口数学一律用可注入时钟
// g.now()——秒级时间戳表达不了 rpm/60 的补桶粒度（rpm=6000 时 100 枚/秒，整秒粒度会退化成
// 「每整秒才补一批」）。
//
// 判定次序：**先 TPM（纯读、无副作用）后 RPM（可能等待并消耗令牌）**。源侧次序是
// 速率 → 并发 → TPM（ChannelGate::acquire），本包刻意反过来：注定要被 TPM 跳过的候选
// 不该再白等 RPM 额度、也不该白烧该渠道的速率令牌。
func (g *ChannelGate) Admit(ctx context.Context, ch Channel, now int64) (bool, error) {
	if g == nil {
		return true, nil
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return false, err
		}
	}
	defaults := g.resolveDefaults(ctx)

	if tpm := ResolveChannelGateTPM(ch.TPMLimit, defaults.TPM); tpm > 0 {
		if spent := g.spentTokens(ch.ID); spent >= int64(tpm) {
			g.logf("ai: 渠道 %d（%s）TPM 预算已用尽（最近 %ds 消耗 %d ≥ tpm=%d）→ 跳过该候选，换下一候选（不自增 attempt）",
				ch.ID, ch.Name, channelGateTokenWindowSeconds, spent, tpm)
			return false, nil
		}
	}

	if rpm := ResolveChannelGateRPM(ch.RPMLimit, defaults.RPM); rpm > 0 {
		if err := g.awaitRate(ctx, ch.ID, rpm); err != nil {
			return false, err
		}
	}
	return true, nil
}

// Charge 是 TokenCharger 的实现：把一次真实尝试的 token 消耗记进 TPM 窗口。
//
// 调用点唯一（Circuit.RecordTotals：tokens > 0 且 channel_id > 0）。nil 接收者安全。
func (g *ChannelGate) Charge(channelID uint64, tokens int) {
	if g == nil || channelID == 0 || tokens <= 0 {
		return
	}
	at := g.clock().Unix()
	g.mu.Lock()
	defer g.mu.Unlock()
	g.sweepLocked(at)
	window, exists := g.windows[int64(channelID)]
	if !exists {
		window = &channelTokenWindow{}
		g.windows[int64(channelID)] = window
	}
	cutoff := at - channelGateTokenWindowSeconds
	window.prune(cutoff)
	window.stamps = append(window.stamps, channelTokenStamp{at: at, tokens: int64(tokens)})
}

// spentTokens 返回该渠道在最近一个 TPM 窗口内的已消耗 token 数（窗口外流水先剪掉）。
func (g *ChannelGate) spentTokens(channelID int64) int64 {
	if g == nil {
		return 0
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	window, exists := g.windows[channelID]
	if !exists {
		return 0
	}
	cutoff := g.clock().Unix() - channelGateTokenWindowSeconds
	window.prune(cutoff)
	return window.spent(cutoff)
}

// rateTokens 返回该渠道 RPM 桶的当前余量（**测试与自检读数口**：桶是准入的内部状态，
// 生产代码不读它）。未建桶时返回 0。
func (g *ChannelGate) rateTokens(channelID int64) float64 {
	if g == nil {
		return 0
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	bucket, exists := g.buckets[channelID]
	if !exists {
		return 0
	}
	return bucket.tokens
}

// bucketCount / windowCount 是自检读数口（测试与巡检用；生产不读）。
func (g *ChannelGate) bucketCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.buckets)
}

func (g *ChannelGate) windowCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.windows)
}

// awaitRate 取一枚速率令牌：桶空就**睡到回填**（不轮询），总等待预算超限即 fail-open 放行并记日志。
func (g *ChannelGate) awaitRate(ctx context.Context, channelID int64, rpm int) error {
	deadline := g.clock().Add(g.waitBudget())
	for attempt := 0; attempt < channelGateWaitPollCap; attempt++ {
		need := g.takeRateToken(channelID, rpm)
		if need <= 0 {
			return nil
		}
		if g.clock().Add(need).After(deadline) {
			g.logf("ai: 渠道 %d 的 RPM 等待超预算（rpm=%d，等待预算 %s）→ fail-open 放行；持续出现请核对渠道 rpm_limit 或调大等待预算",
				channelID, rpm, g.waitBudget())
			return nil
		}
		if ctx != nil {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		g.sleepFor(need)
	}
	g.logf("ai: 渠道 %d 的 RPM 等待循环达上限（rpm=%d）→ fail-open 放行", channelID, rpm)
	return nil
}

// takeRateToken 取一枚令牌：返回 0 = 已取到；> 0 = 还需等待的时长（桶空时的精确回填时间）。
//
// 桶键 = 渠道 ID：生效 rpm 变化时把桶重置为满桶 —— 等价于「渠道 ID + rpm 值」作键
// （换配额即换桶），避免旧配额下耗尽的桶继续惩罚该渠道。
func (g *ChannelGate) takeRateToken(channelID int64, rpm int) time.Duration {
	burst := float64(ChannelGateBurst(rpm))
	rate := float64(rpm) / 60 // 枚/秒
	now := g.clock()

	g.mu.Lock()
	defer g.mu.Unlock()
	g.sweepLocked(now.Unix())

	bucket, exists := g.buckets[channelID]
	switch {
	case !exists:
		bucket = &channelRateBucket{rpm: rpm, tokens: burst, lastSeen: now}
		g.buckets[channelID] = bucket
	case bucket.rpm != rpm:
		bucket.rpm, bucket.tokens, bucket.lastSeen = rpm, burst, now
	default:
		if elapsed := now.Sub(bucket.lastSeen); elapsed > 0 {
			bucket.tokens = math.Min(burst, bucket.tokens+elapsed.Seconds()*rate)
		}
		bucket.lastSeen = now
	}

	if bucket.tokens >= 1 {
		bucket.tokens--
		return 0
	}
	return time.Duration((1 - bucket.tokens) / rate * float64(time.Second))
}

// sweepLocked 惰性清扫入口：距上次清扫不足 channelGateSweepInterval 就直接返回（避免每次准入都 O(n)）。
func (g *ChannelGate) sweepLocked(nowUnix int64) {
	now := time.Unix(nowUnix, 0)
	if !g.lastSweep.IsZero() && now.Sub(g.lastSweep) < channelGateSweepInterval {
		return
	}
	g.lastSweep = now
	for id, bucket := range g.buckets {
		if now.Sub(bucket.lastSeen) >= channelGateBucketTTL {
			delete(g.buckets, id)
		}
	}
	for id, window := range g.windows {
		window.prune(nowUnix - channelGateTokenWindowSeconds)
		if len(window.stamps) == 0 {
			delete(g.windows, id)
		}
	}
}

// resolveDefaults 读插件级缺省；读失败按「未配」（零值）+ 告警处理：
// 缺省那级降级不影响渠道自身配的 rpm_limit/tpm_limit（只有缺省一级失效）。
func (g *ChannelGate) resolveDefaults(ctx context.Context) ChannelGateDefaults {
	if g.defaults == nil {
		return ChannelGateDefaults{}
	}
	defaults, err := g.defaults(ctx)
	if err != nil {
		g.logf("ai: 读取渠道准入门缺省配置失败，本次按「缺省未配」处理（渠道自身配额仍然生效）: %v", err)
		return ChannelGateDefaults{}
	}
	return defaults
}

// clock / sleepFor / waitBudget 是可注入点的访问器（nil 字段一律回落生产值，避免夹具漏设时静默失效）。
func (g *ChannelGate) clock() time.Time {
	if g.now == nil {
		return time.Now()
	}
	return g.now()
}

func (g *ChannelGate) sleepFor(d time.Duration) {
	if d <= 0 {
		return
	}
	if g.sleep == nil {
		time.Sleep(d)
		return
	}
	g.sleep(d)
}

func (g *ChannelGate) waitBudget() time.Duration {
	if g.waitSeconds <= 0 {
		return 0
	}
	return time.Duration(g.waitSeconds) * time.Second
}

// ===== 装配（同包接线点，由 plugin.go 的 defaultService 调用）=====

// installChannelGate 把渠道准入门挂到两处接缝上：
//
//	s.router.gate    = gate   （Router.Admit 的候选级准入，渠道表的接缝）
//	s.breaker.charger = gate   （RecordTotals 的 token 回写口，TPM 预算扣减的唯一挂点）
//
// gate 为 nil ⇒ 两处都不挂：路由侧准入恒放行、RecordTotals 不回调，行为与「没有这一层」逐字一致。
// 注意 Go 的 typed-nil 陷阱：把 (*ChannelGate)(nil) 装进接口字段会得到**非 nil 的接口**，
// 于是 Router.Admit 会真的去调一个空指针上的方法（本包的 Admit/Charge 都做了 nil 接收者防御，
// 但挂载处仍显式挡掉，避免「看起来装了闸」的错觉）。
//
// 若挂进来的 gate 未实现 TokenCharger，TPM 预算不会扣减（闸形同只有 RPM）——这里显式告警，
// 不做静默降级（源侧同一条纪律：配置面与运行面不同源时「静默失效」最难查，见 ChannelGate::audit 注释）。
func installChannelGate(s *Service, gate Gate) {
	if s == nil || gate == nil {
		return
	}
	if concrete, ok := gate.(*ChannelGate); ok && concrete == nil {
		return
	}
	charger, ok := gate.(TokenCharger)
	if !ok {
		log.Printf("ai: 渠道准入门未实现 TokenCharger，TPM 预算将不会扣减（只有 RPM 生效）")
	}
	if s.router != nil {
		s.router.gate = gate
	}
	if s.breaker != nil && ok {
		s.breaker.charger = charger
	}
}

// 编译期断言：准入门同时满足路由接缝与 token 回写口（两者签名漂移时在这里就红）。
var (
	_ Gate         = (*ChannelGate)(nil)
	_ TokenCharger = (*ChannelGate)(nil)
)
