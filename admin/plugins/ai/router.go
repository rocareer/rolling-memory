// plugin: ai v0.2.0
// Package ai 是 AI 路由插件的后端领域核心。
//
// 分层：
//   - 本包（plugins/ai）：调度、熔断、计费、驱动、SSE 与编排；
//   - 同包的数据层（model.go/store.go）：全部 SQL 原子原语与物理表名出口；
//   - plugins/ai/handler（接口层维护）：协议解析、鉴权与回执。
//
// 本文件只做**候选解析与准入调度**：不写 SQL、不碰 HTTP。
// 候选集事实源：store.Candidates 已用一条 JOIN 取回
// 「绑定 status=1 + 渠道 status=1」并按 priority ASC / weight DESC / channel id ASC 排序；
// 本文件对已取回的行做同口径的纯函数复核（防御性过滤 + weighted 首位随机）。
package ai

import (
	"context"
	"math/rand"
	"sort"
)

// 模型的调度策略（plugin_ai_model.strategy 的 CHECK 闭集）。
const (
	StrategyPriority = "priority"
	StrategyWeighted = "weighted"
)

// 渠道状态（plugin_ai_channel.status：1=enabled / 0=disabled）。
const (
	StatusDisabled int16 = 0
	StatusEnabled  int16 = 1
)

// 调用类型（plugin_ai_model.type / plugin_ai_usage.call_type 的 CHECK 闭集）。
const (
	CallTypeChat      = "chat"
	CallTypeEmbedding = "embedding"
)

// Channel 是渠道的领域快照，同时是 plugin_ai_channel 的 GORM 行模型。
//
// 列与插件迁移逐列对齐；物理表名由 model.go 的 table() 单点拼接。
// Timeout=0 表示未配（由插件配置 request_timeout 兜底，见 Service.resolveChannel）；
// FailThreshold/CooldownSeconds=0 表示未配（有效默认 5/60，见 circuit.go）。
// APIKey 是上游明文凭证：**不参与 JSON 序列化**，日志与错误文案都不得包含它。
//
// 三级配置（源三轮：厂商 → 渠道 → 模型，见源 AiRouterService::channelConfig）：
// BaseURL / APIKey / Driver / Timeout 是**生效值**——渠道自身非空优先，为空则取所挂厂商
// （SupplierID>0）的同名列，再为空才回落插件缺省（Timeout 由 Service.resolveChannel 兜
// request_timeout）。合并只有一处实现（store.go 的 mergeSupplierInheritance），
// 走 store.Candidates（调度）与 store.GetChannelEffective（探活）两条读路径；
// 管理面表单回显用 store.GetChannel（渠道自身列，保留「留空即继承」的语义）。
type Channel struct {
	ID     int64  `gorm:"column:id;primaryKey;autoIncrement" json:"id"`
	Name   string `gorm:"column:name;type:varchar(50);not null;default:''" json:"name"`
	Vendor string `gorm:"column:vendor;type:varchar(30);not null;default:''" json:"vendor"`
	// Driver 是渠道生效驱动名（驱动注册表按它分派；空串 = 默认 OpenAI 兼容驱动）。
	// 列宽 255 与源（那里存驱动实现类的 FQN）及厂商表同口径；接口层另有一道更严的形状校验
	// （handler_channel.go 的 normalizeDriverName：≤64 字符 + 字符集复核）。
	Driver string `gorm:"column:driver;type:varchar(255);not null;default:''" json:"driver"`
	// SupplierID 是所属厂商 ID（0=独立渠道，不使用继承）；悬空 ID 不报错、只是无从继承。
	SupplierID int64 `gorm:"column:supplier_id;not null;default:0" json:"supplier_id"`
	// RPMLimit / TPMLimit 是渠道级 RPM / TPM 配额（0=未配⇒由插件缺省兜底，见 gate.go；两者都不限=0）。
	RPMLimit int    `gorm:"column:rpm_limit;not null;default:0" json:"rpm_limit"`
	TPMLimit int    `gorm:"column:tpm_limit;not null;default:0" json:"tpm_limit"`
	BaseURL  string `gorm:"column:base_url;type:varchar(255);not null;default:''" json:"base_url"`
	APIKey   string `gorm:"column:api_key;type:varchar(255);not null;default:''" json:"-"`
	Model    string `gorm:"column:model;type:varchar(100);not null;default:''" json:"model"`
	// Timeout 刻意**不带** GORM 的 default 标签：DDL 的 DEFAULT 60 只在「裸 SQL 不写该列」时兜底，
	// 而带 default 标签会让 GORM 在 Create 时省略零值列 ⇒ 显式传 0（= 跟随插件配置 /
	// 继承所挂厂商）会被静默写成 60，与 Update 传 0 落 0 的行为分叉，也让「留空即继承」不可达。
	// 管理面省略该字段时仍由 handler 显式填 DefaultRequestTimeout，语义不变。
	Timeout             int    `gorm:"column:timeout;not null" json:"timeout"`
	Priority            int    `gorm:"column:priority;not null;default:1" json:"priority"`
	Weight              int    `gorm:"column:weight;not null;default:1" json:"weight"`
	Status              int16  `gorm:"column:status;not null;default:1" json:"status"`
	FailThreshold       int    `gorm:"column:fail_threshold;not null;default:0" json:"fail_threshold"`
	CooldownSeconds     int    `gorm:"column:cooldown_seconds;not null;default:0" json:"cooldown_seconds"`
	Circuit             string `gorm:"column:circuit;type:varchar(16);not null;default:'closed'" json:"circuit"`
	ConsecutiveFailures int    `gorm:"column:consecutive_failures;not null;default:0" json:"consecutive_failures"`
	// OpenedAt：open=跳闸时刻；half_open=探针开始时间（兼作卡死护栏基准）。
	OpenedAt      *int64 `gorm:"column:opened_at" json:"opened_at"`
	TotalCalls    int64  `gorm:"column:total_calls;not null;default:0" json:"total_calls"`
	TotalFailures int64  `gorm:"column:total_failures;not null;default:0" json:"total_failures"`
	TotalTokens   int64  `gorm:"column:total_tokens;not null;default:0" json:"total_tokens"`
	// LastSuccessAt 是心跳降频判据（距此戳 ≥60s 才补写）。
	LastSuccessAt *int64 `gorm:"column:last_success_at" json:"last_success_at"`
	LastError     string `gorm:"column:last_error;type:varchar(500);not null;default:''" json:"last_error"`

	LastProbeAt      *int64 `gorm:"column:last_probe_at" json:"last_probe_at"`
	LastProbeOK      *int16 `gorm:"column:last_probe_ok" json:"last_probe_ok"`
	LastProbeLatency *int   `gorm:"column:last_probe_latency" json:"last_probe_latency"`
	LastProbeError   string `gorm:"column:last_probe_error;type:varchar(500);not null;default:''" json:"last_probe_error"`
	ProbeFailStreak  int    `gorm:"column:probe_fail_streak;not null;default:0" json:"probe_fail_streak"`
	Remark           string `gorm:"column:remark;type:varchar(255);not null;default:''" json:"remark"`
	CreateTime       int64  `gorm:"column:create_time;not null;default:0" json:"create_time"`
	UpdateTime       int64  `gorm:"column:update_time;not null;default:0" json:"update_time"`
}

// TableName 物理表名（前缀由 model.go 的 table() 单点拼接）。
func (Channel) TableName() string { return table("plugin_ai_channel") }

// Enabled 渠道是否参与调度。
func (c Channel) Enabled() bool { return c.Status == StatusEnabled }

// Model 是逻辑模型的领域快照，同时是 plugin_ai_model 的 GORM 行模型。
//
// 单价用字符串承载 numeric(12,6)，避免 float64 漂移；金额运算一律走 billing.go 的
// big.Int 微单位实现。Capabilities 用 serializer:json 与 jsonb 列互转。
type Model struct {
	ID            uint64   `gorm:"column:id;primaryKey;autoIncrement" json:"id"`
	Name          string   `gorm:"column:name;type:varchar(100);not null;default:''" json:"name"`
	Type          string   `gorm:"column:type;type:varchar(16);not null;default:'chat'" json:"type"`
	Strategy      string   `gorm:"column:strategy;type:varchar(16);not null;default:'priority'" json:"strategy"`
	Description   string   `gorm:"column:description;type:varchar(255);not null;default:''" json:"description"`
	Status        int16    `gorm:"column:status;not null;default:1" json:"status"`
	PriceInput    string   `gorm:"column:price_input;type:numeric(12,6);not null;default:0" json:"price_input"`
	PriceOutput   string   `gorm:"column:price_output;type:numeric(12,6);not null;default:0" json:"price_output"`
	Encoding      string   `gorm:"column:encoding;type:varchar(20);not null;default:''" json:"encoding"`
	ContextWindow int      `gorm:"column:context_window;not null;default:0" json:"context_window"`
	Capabilities  []string `gorm:"column:capabilities;type:jsonb;serializer:json" json:"capabilities"`
	CreateTime    int64    `gorm:"column:create_time;not null;default:0" json:"create_time"`
	UpdateTime    int64    `gorm:"column:update_time;not null;default:0" json:"update_time"`
}

// TableName 物理表名（前缀由 model.go 的 table() 单点拼接）。
func (Model) TableName() string { return table("plugin_ai_model") }

// Enabled 模型是否可被 requireModel 选中。
func (m Model) Enabled() bool { return m.Status == StatusEnabled }

// SupportsTools 是否声明支持 function_calling。
//
// 口径：capabilities 为空/NULL = 未知不拦截；
// 显式非空且不含 function_calling 才判不支持。
func (m Model) SupportsTools() bool {
	if len(m.Capabilities) == 0 {
		return true
	}
	for _, c := range m.Capabilities {
		if c == "function_calling" {
			return true
		}
	}
	return false
}

// Binding 是逻辑模型与渠道的绑定快照（int64 口径，供 handler/展示使用）。
//
// 调度侧直接用 store.Candidates 返回的扁平 Candidate（uint64 口径），
// 本类型只作为 ModelChannel.Snapshot() 的转换目标。
type Binding struct {
	ModelID   int64
	ChannelID int64
	Priority  int
	Weight    int
	Status    int16
}

// ModelChannel 是 plugin_ai_model_channel 的 GORM 行模型（绑定全量替换/级联删除使用）。
type ModelChannel struct {
	ID         uint64 `gorm:"column:id;primaryKey;autoIncrement" json:"id"`
	ModelID    uint64 `gorm:"column:model_id;not null" json:"model_id"`
	ChannelID  uint64 `gorm:"column:channel_id;not null" json:"channel_id"`
	Priority   int    `gorm:"column:priority;not null;default:1" json:"priority"`
	Weight     int    `gorm:"column:weight;not null;default:1" json:"weight"`
	Status     int16  `gorm:"column:status;not null;default:1" json:"status"`
	CreateTime int64  `gorm:"column:create_time;not null;default:0" json:"create_time"`
	UpdateTime int64  `gorm:"column:update_time;not null;default:0" json:"update_time"`
}

// TableName 物理表名（前缀由 model.go 的 table() 单点拼接）。
func (ModelChannel) TableName() string { return table("plugin_ai_model_channel") }

// Snapshot 绑定行 → int64 口径的绑定快照（model.go 的 ModelChannelRow 别名复用本方法）。
func (r ModelChannel) Snapshot() Binding {
	return Binding{
		ModelID:   int64(r.ModelID),
		ChannelID: int64(r.ChannelID),
		Priority:  r.Priority,
		Weight:    r.Weight,
		Status:    r.Status,
	}
}

// ChannelSnapshot 把 store 的扁平候选行转成驱动/熔断需要的渠道快照。
//
// Candidate 由 model.go 定义（不嵌套 Channel）；这里做唯一一次映射，
// 避免两套结构在字段更新时漂移。BaseURL/APIKey/Driver/Timeout 取的是**生效值**
// （store.Candidates 已按「渠道非空优先、空则取厂商」合并过，见 store.go）。
func (c Candidate) ChannelSnapshot() Channel {
	return Channel{
		ID:                  int64(c.ChannelID),
		Name:                c.Name,
		Vendor:              c.Vendor,
		Driver:              c.Driver,
		SupplierID:          int64(c.SupplierID),
		RPMLimit:            c.RPMLimit,
		TPMLimit:            c.TPMLimit,
		BaseURL:             c.BaseURL,
		APIKey:              c.APIKey,
		Model:               c.Model,
		Timeout:             c.Timeout,
		Status:              c.ChannelStatus,
		FailThreshold:       c.FailThreshold,
		CooldownSeconds:     c.CooldownSeconds,
		Circuit:             c.Circuit,
		ConsecutiveFailures: c.ConsecutiveFailures,
		OpenedAt:            c.OpenedAt,
		LastSuccessAt:       c.LastSuccessAt,
		LastError:           c.LastError,
	}
}

// Rand 是加权随机源。*math/rand.Rand 天然满足本接口；nil 时用全局源。
// 注入确定性实现即可复现「weighted 首位命中」的测试。
type Rand interface {
	Intn(n int) int
}

type globalRand struct{}

func (globalRand) Intn(n int) int { return rand.Intn(n) }

// Gate 是**渠道级准入门**的接缝（RPM/TPM 等配额判断的唯一挂载点）。
//
// 语义契约：
//   - Admit 返回 (false, nil) ⇒ 该候选被跳过（换下一候选），**不自增 attempt**——
//     与熔断拒绝同形（被拒候选不是一次真实调用）；
//   - Admit 返回 err ⇒ 与熔断存储故障同形：拒绝该候选、记 lastErr、不自增 attempt；
//   - Admit 返回 (true, nil) ⇒ 放行（实现内部的等待/退避/fail-open 策略自负其责，
//     源侧口径是「等待超预算按 fail-open 放行」，见源 ChannelGate.php:156-192）；
//   - 实现必须并发安全（准入在候选遍历里被逐候选调用），且不得在 ch 上写任何字段。
//
// 挂载点：Router.gate。装配由 installChannelGate 完成（同时挂 router.gate 与
// breaker.charger）；gate 为 nil 时 Router.Admit 的行为与「没有这一层」逐字一致。
type Gate interface {
	Admit(ctx context.Context, ch Channel, now int64) (allowed bool, err error)
}

// Router 持有候选解析所需的随机源、熔断准入器与渠道准入门。
type Router struct {
	breaker *Circuit
	// gate 是渠道级准入门（装配时挂载；nil = 无这一层）。同包未导出字段：由 installChannelGate 挂载。
	gate Gate
	rnd  Rand
}

// NewRouter 构造路由器。breaker 为 nil 时熔断准入一律放行（仅用于无熔断的纯函数测试）；
// 渠道准入门（gate）由 installChannelGate 事后挂载。
func NewRouter(breaker *Circuit, rnd Rand) *Router {
	if rnd == nil {
		rnd = globalRand{}
	}
	return &Router{breaker: breaker, rnd: rnd}
}

// Build 把 store.Candidates 取回的行收敛成候选链。
//
// 规则（逐条照来源规则「调度-候选集/priority/weighted」）：
//  1. 只保留绑定 status=1 且渠道 status=1（store 的 JOIN 已做，本函数再防御一次）；
//  2. priority ASC、weight DESC；同序时按 channel_id ASC 保证稳定；
//  3. strategy=weighted 且候选>1 时，权重 = max(1,weight)，
//     按权重和取一个随机点，命中者提到队首，**其余保持原序**作为降级链；
//  4. 不是全量加权随机：只有第一位随机，后续仍按优先级降级。
func (r *Router) Build(model Model, candidates []Candidate) []Candidate {
	return ResolveCandidates(candidates, model.Strategy, r.rnd)
}

// ResolveCandidates 是候选解析的纯函数实现。
//
// 输入切片不会被修改；输出是新的切片。
func ResolveCandidates(candidates []Candidate, strategy string, rnd Rand) []Candidate {
	out := make([]Candidate, 0, len(candidates))
	for _, c := range candidates {
		if c.ChannelStatus != StatusEnabled || c.BindingStatus != StatusEnabled {
			continue
		}
		out = append(out, c)
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].BindingPriority != out[j].BindingPriority {
			return out[i].BindingPriority < out[j].BindingPriority
		}
		if out[i].BindingWeight != out[j].BindingWeight {
			return out[i].BindingWeight > out[j].BindingWeight
		}
		return out[i].ChannelID < out[j].ChannelID
	})

	if strategy != StrategyWeighted || len(out) < 2 {
		return out
	}

	total := 0
	for _, c := range out {
		total += candidateWeight(c)
	}
	if total <= 0 {
		return out
	}
	if rnd == nil {
		rnd = globalRand{}
	}
	pick := rnd.Intn(total)
	acc := 0
	hit := -1
	for i, c := range out {
		acc += candidateWeight(c)
		if pick < acc {
			hit = i
			break
		}
	}
	if hit <= 0 {
		return out
	}
	// 提到队首而不是与队首交换：交换会打乱其余候选的相对顺序。
	chosen := out[hit]
	copy(out[1:hit+1], out[0:hit])
	out[0] = chosen
	return out
}

func candidateWeight(c Candidate) int {
	if c.BindingWeight > 1 {
		return c.BindingWeight
	}
	return 1
}

// Admission 是熔断准入结果。
//
// Circuit 是**准入后的有效状态**：open 冷却到期并 CAS 成功后会变成 half_open。
// Service 必须把这个值回写到本地候选/渠道快照，否则 onSuccess 的
// 「half_open 判定用进入时的 circuit」会失真（来源规则「熔断-CAS 单探针」）。
type Admission struct {
	Allowed bool
	Circuit string
}

// Admit 对单个候选执行准入：先熔断（breaker），再渠道准入门（gate）。
//
// 顺序刻意如此：熔断拒绝的候选不该消耗 RPM 令牌；被熔断拦下的候选也不进配额统计。
// breaker 与 gate 都为 nil 时返回 Admission{Allowed: true, Circuit: ch.Circuit}（与旧行为逐字一致）。
// breaker 出错时原样返回 Circuit.Enter 的结果与错误（口径不变）；gate 出错则带当前有效的
// circuit 一并返回（调用方按「存储故障 ⇒ 跳过该候选、不自增 attempt」处理）。
func (r *Router) Admit(ctx context.Context, ch Channel, now int64) (Admission, error) {
	if r == nil {
		return Admission{Allowed: true, Circuit: ch.Circuit}, nil
	}
	ad := Admission{Allowed: true, Circuit: ch.Circuit}
	if r.breaker != nil {
		result, err := r.breaker.Enter(ctx, ch, now)
		if err != nil {
			return result, err
		}
		ad = result
	}
	if !ad.Allowed || r.gate == nil {
		return ad, nil
	}
	allowed, err := r.gate.Admit(ctx, ch, now)
	if err != nil {
		// 与熔断存储故障同形：Allowed=false 兜底（调用方即使忽略 err 也不会误放行）+ err 供 lastErr。
		return Admission{Allowed: false, Circuit: ad.Circuit}, err
	}
	if !allowed {
		return Admission{Allowed: false, Circuit: ad.Circuit}, nil
	}
	return ad, nil
}

// FirstAdmitted 按候选顺序返回第一个准入通过的候选（流式只取首个，不换道）。
//
// 被拒绝的候选只跳过、不自增 attempt；存储错误也不自增，作为 lastErr 返回。
// 返回的 Candidate.Circuit 已是准入后的有效状态。
func (r *Router) FirstAdmitted(ctx context.Context, candidates []Candidate, now int64) (Candidate, bool, error) {
	var lastErr error
	for _, c := range candidates {
		ad, err := r.Admit(ctx, c.ChannelSnapshot(), now)
		if err != nil {
			lastErr = err
			continue
		}
		if !ad.Allowed {
			continue
		}
		c.Circuit = ad.Circuit
		return c, true, nil
	}
	return Candidate{}, false, lastErr
}
