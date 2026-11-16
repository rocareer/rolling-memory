// plugin: ai v0.2.0
// circuit.go 是熔断状态机的唯一决策与编排住所。
//
// 三态语义（来源规则「熔断-准入/CAS 单探针/成功迁移/失败与跳闸」）：
//   - closed：放行；
//   - open：冷却期内拒绝；冷却到期后 CAS 选举唯一 half_open 探针；
//   - half_open：opened_at 转义为「探针开始时间」，冷却期内拒绝（探针在飞）；
//     超过冷却仍未收口 ⇒ 视为探针卡死（进程被杀/协程挂死），用 CAS 重选。
//
// 本文件**不写 SQL**：所有迁移都委托 CircuitStore 的条件更新原语（store.go 实现）；
// 也不做读-改-写——连续失败计数由 store.MarkFailure 的单条 UPDATE 原子自增 + RETURNING 完成。
//
// 渠道累计（total_calls/total_failures/total_tokens）统一由 RecordTotals 在
// usage 结算处调用恰好一次：非流式每次真实尝试一次；流式只在最终收口一次
// （首块时的 onSuccess 只迁移熔断状态，因为那时 token 数还不知道）。
package ai

import (
	"context"
	"errors"
	"log"
)

// 熔断三态（plugin_ai_channel.circuit 的 CHECK 闭集）。
const (
	CircuitClosed   = "closed"
	CircuitOpen     = "open"
	CircuitHalfOpen = "half_open"
)

// 熔断回落的最后一级默认值（渠道字段与插件配置都没给值时使用）。
// 正常部署下这两条常量只在「配置行缺失 / 读取失败」时兜底：生效值优先取渠道字段、
// 其次取**注入的插件配置**（plugin.yaml 的 breaker_fail_threshold/breaker_cooldown_seconds，
// 见 CircuitDefaults），Configuration 改了必然换新引擎实例（plugin.go 的 signature）。
const (
	DefaultFailThreshold   = 5
	DefaultCooldownSeconds = 60
	// HeartbeatMinSeconds 心跳降频：稳态下距上次成功 ≥60s 才补写 last_success_at。
	HeartbeatMinSeconds int64 = 60
)

// 熔断事件类型（plugin_ai_breaker_event.event 的 CHECK 闭集）。
const (
	EventOpen        = "open"
	EventClose       = "close"
	EventManualOpen  = "manual_open"
	EventManualClose = "manual_close"
)

// ErrAlreadyOpen 人工熔断时渠道已经在熔断中（来源 Channel.php 的同口径报错）。
var ErrAlreadyOpen = errors.New("渠道已在熔断中")

// BreakerEvent 是一条熔断状态机动作流水，同时是 plugin_ai_breaker_event 的 GORM 行模型。
//
// 阈值/冷却/连续失败都是**触发时的快照**，恢复事件（close）的连续失败为 0。
// 自动/人工由 event 取值区分（open/close vs manual_open/manual_close），不另设列。
// 落库失败只记日志，不影响主流程（best-effort）。
type BreakerEvent struct {
	ID                  uint64 `gorm:"column:id;primaryKey;autoIncrement" json:"id"`
	ChannelID           int64  `gorm:"column:channel_id;not null" json:"channel_id"`
	ChannelName         string `gorm:"column:channel_name;type:varchar(50);not null;default:''" json:"channel_name"`
	Event               string `gorm:"column:event;type:varchar(16);not null;default:'open'" json:"event"`
	ConsecutiveFailures int    `gorm:"column:consecutive_failures;not null;default:0" json:"consecutive_failures"`
	FailThreshold       int    `gorm:"column:fail_threshold;not null;default:0" json:"fail_threshold"`
	CooldownSeconds     int    `gorm:"column:cooldown_seconds;not null;default:0" json:"cooldown_seconds"`
	LastError           string `gorm:"column:last_error;type:varchar(500);not null;default:''" json:"last_error"`
	CreateTime          int64  `gorm:"column:create_time;not null;default:0" json:"create_time"`
	UpdateTime          int64  `gorm:"column:update_time;not null;default:0" json:"update_time"`
}

// TableName 物理表名（前缀由 model.go 的 table() 单点拼接）。
func (BreakerEvent) TableName() string { return table("plugin_ai_breaker_event") }

// CircuitStore 是熔断状态机需要的全部 SQL 原语（由 store.go 实现，签名逐字对齐）。
//
// 约定：
//   - MarkFailure 是单条 UPDATE ... consecutive_failures = consecutive_failures + 1
//     RETURNING consecutive_failures；禁止先读后写（并发丢计数会让跳闸偏晚）。
//   - EnterCAS/ReselectStaleProbe/OpenCircuit/ManualOpenCircuit 都返回是否当选/是否迁移；
//     调用方只认 true 为当选。
//   - MarkSuccess 返回是否真的写库（稳态可能被 SQL 条件拦下，返回 false）。
//   - AddChannelTotals 按 status 累加：success ⇒ total_calls+1 且 total_tokens += tokens；
//     非 success ⇒ total_failures+1；channel_id=0 时调用方跳过。
//   - InsertBreakerEvent 是 best-effort 的流水写入。
type CircuitStore interface {
	EnterCAS(ctx context.Context, id uint64, now int64) (bool, error)
	ReselectStaleProbe(ctx context.Context, id uint64, now, cooldownSeconds int64) (bool, error)
	MarkFailure(ctx context.Context, id uint64, lastError string, at int64) (int, error)
	MarkSuccess(ctx context.Context, id uint64, at int64) (bool, error)
	OpenCircuit(ctx context.Context, id uint64, at int64) (bool, error)
	ManualOpenCircuit(ctx context.Context, id uint64, at int64) (bool, error)
	ManualCloseCircuit(ctx context.Context, id uint64, at int64) (int64, error)
	AddChannelTotals(ctx context.Context, channelID uint64, success bool, tokens int, at int64) error
	InsertBreakerEvent(ctx context.Context, event *BreakerEvent) error
}

// Circuit 是熔断状态机。store 为 nil 时所有准入放行、所有迁移 no-op（仅测试装配便利）。
type Circuit struct {
	store CircuitStore
	// defaults 是插件配置注入的全局默认（渠道字段为 0 时使用；<=0 = 未配 ⇒ 再回落代码常量）。
	defaults CircuitDefaults
	logf     func(format string, args ...any)
	// charger 是渠道 token 消耗的回写口（TPM 预算扣减的唯一挂点，见 RecordTotals）。
	//
	// 为什么落在这里：渠道 token 记号的唯一写口是 RecordTotals（每次真实尝试恰好一次，
	// 调用点见 service.go 的六个 settle*），所以「上游真花了多少 token」只有在这条路径上
	// 才既不漏记也不重记；写进 OnSuccess/OnFailure 会漏掉「流式首块 onSuccess 时还不知道 token」
	// 与「失败但已聚合部分输出」两种情形。
	//
	// nil = 不扣减（未装渠道准入门时行为与现状逐字一致）。
	charger TokenCharger
}

// CircuitDefaults 是插件配置（plugin.yaml 的 breaker_fail_threshold / breaker_cooldown_seconds）
// 注入熔断器的全局默认：渠道 fail_threshold/cooldown_seconds 为 0 时使用。
//
// 必须注入的原因：plugin.yaml 的 tip 与总览设置弹窗都承诺「渠道为 0 时用插件配置」，
// 只读配置而不注入会让设置面回显一套值、引擎实际用另一套值（配置静默失效）。
type CircuitDefaults struct {
	FailThreshold   int
	CooldownSeconds int
}

// NewCircuit 构造熔断器；defaults 可选（不给 = 未配，回落代码常量）。
func NewCircuit(store CircuitStore, defaults ...CircuitDefaults) *Circuit {
	c := &Circuit{store: store, logf: log.Printf}
	if len(defaults) > 0 {
		c.defaults = defaults[0]
	}
	return c
}

// failThresholdFor / cooldownFor 是引擎侧对同一条求值链的取用（渠道字段 → 插件配置 → 代码常量）。
func (c *Circuit) failThresholdFor(channelValue int) int {
	if c == nil {
		return ResolveFailThreshold(channelValue, 0)
	}
	return ResolveFailThreshold(channelValue, c.defaults.FailThreshold)
}

func (c *Circuit) cooldownFor(channelValue int) int {
	if c == nil {
		return ResolveCooldown(channelValue, 0)
	}
	return ResolveCooldown(channelValue, c.defaults.CooldownSeconds)
}

// CircuitDecision 是纯决策函数的输出。
type CircuitDecision int

const (
	// DecisionAllow 直接放行。
	DecisionAllow CircuitDecision = iota
	// DecisionDeny 拒绝（冷却中/探针在飞/未知状态）。
	DecisionDeny
	// DecisionProbe 需要 CAS 选举/重选半开探针后才能决定是否放行。
	DecisionProbe
)

// EffectiveFailThreshold 渠道字段 >0 用自身，否则回落到代码默认 5。
// 注意是「0 等同未配」而不是「0 = 立即熔断」（来源规则「熔断-默认值」的 `?:`）。
// 这是求值链的最后一级；带插件配置的场景用 ResolveFailThreshold。
func EffectiveFailThreshold(v int) int {
	return ResolveFailThreshold(v, 0)
}

// EffectiveCooldown 渠道字段 >0 用自身，否则回落到代码默认 60s。
func EffectiveCooldown(v int) int {
	return ResolveCooldown(v, 0)
}

// ResolveFailThreshold 是熔断失败阈值的**唯一求值链**：渠道字段 >0 用自身 →
// 否则用插件配置 breaker_fail_threshold（<=0 视为未配）→ 仍未配回落代码常量 5。
//
// 引擎（Circuit）与展示面（渠道列表/总览的 effective_* 、熔断事件快照）必须走同一条链，
// 否则「设置面回显」「接口生效值」「运行时行为」会各说各话。
func ResolveFailThreshold(channelValue, pluginDefault int) int {
	if channelValue > 0 {
		return channelValue
	}
	if pluginDefault > 0 {
		return pluginDefault
	}
	return DefaultFailThreshold
}

// ResolveCooldown 是熔断冷却秒数的唯一求值链（同 ResolveFailThreshold 的三级回落）。
func ResolveCooldown(channelValue, pluginDefault int) int {
	if channelValue > 0 {
		return channelValue
	}
	if pluginDefault > 0 {
		return pluginDefault
	}
	return DefaultCooldownSeconds
}

// Decide 是熔断准入的纯决策函数。
//
//	closed                              → allow
//	open   && now-opened_at < cooldown  → deny（冷却中）
//	open   && now-opened_at >= cooldown → probe（CAS 选唯一探针）
//	half_open && now-opened_at < cooldown  → deny（探针在飞）
//	half_open && now-opened_at >= cooldown → probe（探针卡死，CAS 重选）
//
// opened_at 为空时按 probe 处理：没有基准时间就无法证明仍在冷却，
// 让 CAS 去保证唯一性，比永久拒绝安全。未知 circuit 值一律 deny（fail-closed）。
func Decide(circuit string, openedAt *int64, now int64, cooldown int64) CircuitDecision {
	if cooldown <= 0 {
		cooldown = DefaultCooldownSeconds
	}
	switch circuit {
	case CircuitClosed:
		return DecisionAllow
	case CircuitOpen, CircuitHalfOpen:
		if openedAt == nil {
			return DecisionProbe
		}
		if now-*openedAt < cooldown {
			return DecisionDeny
		}
		return DecisionProbe
	default:
		return DecisionDeny
	}
}

// Enter 执行一次熔断准入。
//
// 返回的 Admission.Circuit 是准入后的有效状态，调用方必须回写到本地渠道快照：
// open 冷却到期且 CAS 当选后是 half_open，后续 onSuccess/onFailure 用它判定。
func (c *Circuit) Enter(ctx context.Context, ch Channel, now int64) (Admission, error) {
	if c == nil || c.store == nil {
		return Admission{Allowed: true, Circuit: ch.Circuit}, nil
	}
	cooldown := int64(c.cooldownFor(ch.CooldownSeconds))
	id := uint64(ch.ID)

	switch Decide(ch.Circuit, ch.OpenedAt, now, cooldown) {
	case DecisionAllow:
		return Admission{Allowed: true, Circuit: CircuitClosed}, nil
	case DecisionDeny:
		return Admission{Allowed: false, Circuit: ch.Circuit}, nil
	case DecisionProbe:
		if ch.Circuit == CircuitOpen {
			// 冷却到期：CAS 把 open 换 half_open，false 表示被别人抢先，必须跳过。
			ok, err := c.store.EnterCAS(ctx, id, now)
			if err != nil {
				return Admission{Allowed: false, Circuit: ch.Circuit}, err
			}
			return Admission{Allowed: ok, Circuit: CircuitHalfOpen}, nil
		}
		// half_open 且已超冷却：视为探针卡死，按 opened_at < now-cooldown 重选。
		ok, err := c.store.ReselectStaleProbe(ctx, id, now, cooldown)
		if err != nil {
			return Admission{Allowed: false, Circuit: ch.Circuit}, err
		}
		return Admission{Allowed: ok, Circuit: CircuitHalfOpen}, nil
	default:
		return Admission{Allowed: false, Circuit: ch.Circuit}, nil
	}
}

// OnSuccess 处理一次真实成功。
//
// 迁移条件（来源规则「熔断-成功迁移与心跳降频」）：
//   - 需要迁移：circuit != closed 或 consecutive_failures != 0 或 opened_at 非空；
//   - 心跳超龄：last_success_at 为空或距 now ≥ 60s；
//   - 两者任一成立才写 closed/清零/opened_at=NULL/last_success_at=now；
//   - 稳态（已闭合·零失败·心跳新鲜）完全不落库。
//
// wasHalfOpen 用**进入时**的 circuit 判定；是半开探针成功则额外写 close 事件。
func (c *Circuit) OnSuccess(ctx context.Context, ch Channel, now int64) error {
	if c == nil || c.store == nil {
		return nil
	}
	wasHalfOpen := ch.Circuit == CircuitHalfOpen
	needsMigration := ch.Circuit != CircuitClosed || ch.ConsecutiveFailures != 0 || ch.OpenedAt != nil
	heartbeatStale := ch.LastSuccessAt == nil || now-*ch.LastSuccessAt >= HeartbeatMinSeconds
	if !needsMigration && !heartbeatStale {
		return nil
	}
	if _, err := c.store.MarkSuccess(ctx, uint64(ch.ID), now); err != nil {
		return err
	}
	if wasHalfOpen {
		c.appendEvent(ctx, BreakerEvent{
			ChannelID:           ch.ID,
			ChannelName:         ch.Name,
			Event:               EventClose,
			ConsecutiveFailures: 0,
			FailThreshold:       c.failThresholdFor(ch.FailThreshold),
			CooldownSeconds:     c.cooldownFor(ch.CooldownSeconds),
			CreateTime:          now,
			UpdateTime:          now,
		})
	}
	return nil
}

// OnFailure 处理一次真实失败。
//
// 次序（来源规则「熔断-失败与跳闸」）：
//  1. store.MarkFailure 原子自增 consecutive_failures 并截断 last_error（本函数只传已截断文案）；
//  2. half_open 探针失败 ⇒ 无条件回 open、重新计时冷却（不论是否到阈值）；
//  3. closed 且自增后 ≥ 阈值 ⇒ open + opened_at=now；
//  4. 跳闸用 store.OpenCircuit（WHERE circuit <> 'open'）保证并发下只有一个调用写 open 事件。
//
// 渠道累计不在这里做——见 RecordTotals 的说明。
func (c *Circuit) OnFailure(ctx context.Context, ch Channel, errMsg string, now int64) error {
	if c == nil || c.store == nil {
		return nil
	}
	msg := truncateMessage(errMsg, 500)
	consecutive, err := c.store.MarkFailure(ctx, uint64(ch.ID), msg, now)
	if err != nil {
		return err
	}
	threshold := c.failThresholdFor(ch.FailThreshold)
	cooldown := c.cooldownFor(ch.CooldownSeconds)

	trip := ch.Circuit == CircuitHalfOpen || consecutive >= threshold
	if !trip {
		return nil
	}
	ok, err := c.store.OpenCircuit(ctx, uint64(ch.ID), now)
	if err != nil {
		return err
	}
	if !ok {
		// 其他并发调用已经完成同一迁移：不重复写事件。
		return nil
	}
	c.appendEvent(ctx, BreakerEvent{
		ChannelID:           ch.ID,
		ChannelName:         ch.Name,
		Event:               EventOpen,
		ConsecutiveFailures: consecutive,
		FailThreshold:       threshold,
		CooldownSeconds:     cooldown,
		LastError:           msg,
		CreateTime:          now,
		UpdateTime:          now,
	})
	return nil
}

// ForceOpen 人工熔断：只置 circuit=open + opened_at=now，并写 manual_open 事件。
//
// 不改 total_calls/total_failures/total_tokens，也不伪造 consecutive_failures。
// 已在 open 时报 ErrAlreadyOpen（来源 Channel.php 的同口径）。
func (c *Circuit) ForceOpen(ctx context.Context, ch Channel, now int64) error {
	if c == nil || c.store == nil {
		return nil
	}
	if ch.Circuit == CircuitOpen {
		return ErrAlreadyOpen
	}
	ok, err := c.store.ManualOpenCircuit(ctx, uint64(ch.ID), now)
	if err != nil {
		return err
	}
	if !ok {
		return ErrAlreadyOpen
	}
	c.appendEvent(ctx, BreakerEvent{
		ChannelID:           ch.ID,
		ChannelName:         ch.Name,
		Event:               EventManualOpen,
		ConsecutiveFailures: ch.ConsecutiveFailures,
		FailThreshold:       c.failThresholdFor(ch.FailThreshold),
		CooldownSeconds:     c.cooldownFor(ch.CooldownSeconds),
		LastError:           truncateMessage(ch.LastError, 500),
		CreateTime:          now,
		UpdateTime:          now,
	})
	return nil
}

// Reset 人工恢复：只置 circuit=closed、consecutive_failures=0、opened_at=NULL，
// 并写 manual_close 事件；同样不动累计统计。
func (c *Circuit) Reset(ctx context.Context, ch Channel, now int64) error {
	if c == nil || c.store == nil {
		return nil
	}
	n, err := c.store.ManualCloseCircuit(ctx, uint64(ch.ID), now)
	if err != nil {
		return err
	}
	if n == 0 {
		return nil
	}
	c.appendEvent(ctx, BreakerEvent{
		ChannelID:           ch.ID,
		ChannelName:         ch.Name,
		Event:               EventManualClose,
		ConsecutiveFailures: 0,
		FailThreshold:       c.failThresholdFor(ch.FailThreshold),
		CooldownSeconds:     c.cooldownFor(ch.CooldownSeconds),
		CreateTime:          now,
		UpdateTime:          now,
	})
	return nil
}

// TokenCharger 是渠道 token 消耗的回写口（窄接口，按 Go 惯例在消费侧声明）。
//
// 消费侧是 RecordTotals：实现在 gate.go 的 ChannelGate（TPM 窗口扣减）。
// 单独抽成接口而不是直接依赖 *ChannelGate，是为了让「熔断累计」与「配额扣减」两件事
// 各自可测：RecordTotals 的用例注入假 charger 即可断言回调条件，不需要造一个准入门。
type TokenCharger interface {
	// Charge 记一次真实尝试消耗的 token 数（tokens > 0；channelID > 0）。
	// 实现必须并发安全，且**不得 panic 影响调用主流程**——它跑在结算路径上。
	Charge(channelID uint64, tokens int)
}

// RecordTotals 是渠道累计的唯一写口：每次真实尝试恰好调用一次。
//
//	success ⇒ total_calls+1 且 total_tokens += tokens
//	非 success ⇒ total_failures+1
//	channel_id=0（本地预检失败/直连不记账）跳过
//
// 放在这里而不是拆进 OnSuccess/OnFailure，是为了让流式「首块 onSuccess」与
// 「收尾才知道 token」解耦：累计只发生一次，token 不会因首块提前而记 0。
//
// TPM 预算扣减挂在本函数内（charger）：**只有真实尝试且带 token 数**才扣——
// tokens<=0（失败行、本地预检、流式无块）不占预算；channel_id=0 上面已提前返回。
// 先扣减再写库：这些 token 上游已经真实花掉了，DB 故障不该让预算记账跟着丢。
func (c *Circuit) RecordTotals(ctx context.Context, channelID uint64, success bool, tokens int, now int64) error {
	if c == nil || c.store == nil || channelID == 0 {
		return nil
	}
	if c.charger != nil && tokens > 0 {
		c.charger.Charge(channelID, tokens)
	}
	return c.store.AddChannelTotals(ctx, channelID, success, tokens, now)
}

func (c *Circuit) appendEvent(ctx context.Context, ev BreakerEvent) {
	if c == nil || c.store == nil {
		return
	}
	if err := c.store.InsertBreakerEvent(ctx, &ev); err != nil && c.logf != nil {
		// 熔断事件落库失败只记日志，不影响调用主流程（来源 AiListener 口径）。
		c.logf("ai: 写入熔断事件失败 channel=%d event=%s: %v", ev.ChannelID, ev.Event, err)
	}
}

// truncateMessage 按**字符**截断到 max（varchar(500) 是字符数，不是字节数）。
func truncateMessage(s string, maxChars int) string {
	if maxChars <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= maxChars {
		return s
	}
	return string(r[:maxChars])
}
