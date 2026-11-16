// plugin: ai v0.2.0
// driver_registry.go 是多驱动注册表：按渠道的 driver 列（plugin_ai_channel.driver，由迁移 3 落位）
// 解析到具体 Driver 实现，并把注册表自身当作 Driver 交给编排层与探活复用。
//
// 分派口径：
//   - driver 为空 ⇒ openai（与 driver 列落地之前的现行为逐字一致）；
//   - 未知 driver ⇒ **不静默回落**：返回 local 错误（code=unknown_driver，文案列出可用驱动）；
//   - 能力门：Chat/ChatStream 需声明 chat、Embeddings 需声明 embedding（源 DriverInterface 的
//     supportsChat/supportsEmbedding 口径）；Models 是探活通道，不做能力门（任何驱动都必须能应答）；
//   - 内建驱动（源 DriverManager.php:36-50 内建目录的子集）：openai / deepseek / dashscope / responses。
//     Register(DriverSpec) 供本包之外自注册（dashscope_tts 走这条）；语音分派另见 Speak。
//   - 语音（SpeechDriver，service_tts.go 声明在消费侧）：注册表实现 Speak 并按 ch.Driver 分派，
//     使 `s.driver.(SpeechDriver)` 在装配为注册表后依然成立。
//
// 复用既有两项契约（新驱动不得各自实现）：
//   - 出站护栏：所有走 HTTP 的驱动共用 DriverEnv.Transport 的客户端分桶，桶内客户端由
//     driver.go 的 newGuardedHTTPClient 构造（**新驱动的唯一构造出口**：Control 拦私网 +
//     CheckRedirect 拒绝跟随）；DriverEnv.Client 是新驱动取客户端的同一入口。
//   - 错误出口：每个驱动方法在返回前调 redactUpstreamErr(err, ch.APIKey)（redact.go 的单点），
//     于是 usage.error_message / 渠道 last_error / 探活落库与回执拿到同一份已脱敏文案。
//
// 接线（plugin.go 的 sharedDriver，返回类型 *OpenAIDriver → Driver）：
//
//	registry := NewDriverRegistry(DriverEnv{
//	    AllowPrivateHosts: set.AllowPrivateHosts,
//	    SSELimits:         set.sseLimits(),   // 三个 sse_max_* 键（零值 = 缺省 1MiB/8MiB/32MiB）
//	})
//
// 装配缓存键 = allow_private + SSE 上限 + RegistrySignature()（已注册驱动集合的指纹）。
package ai

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
)

// 内建驱动名（plugin_ai_channel.driver 的闭集取值；空串按 openai 解析）。
//
// 与源侧内置目录（DriverManager.php:36-50）的差异：源侧阿里云百炼的内建名是 "aliyun"，
// 本插件取 "dashscope"（与 TTS 驱动 "dashscope_tts" 同族）；未注册的名字一律
// unknown_driver 响亮失败，不做名字猜测（见 risks/README 差异节）。
const (
	DriverNameOpenAI    = "openai"
	DriverNameDeepSeek  = "deepseek"
	DriverNameDashScope = "dashscope"
	DriverNameResponses = "responses"
)

// 驱动能力闭集（DriverSpec.Capabilities 的取值）。
//
// Speech 供 dashscope_tts 自注册：它不经 Driver.Chat/Embeddings 施展，
// 但能力闭集必须同源声明，避免"能调用但声明缺项"。
const (
	CapabilityChat      = "chat"
	CapabilityEmbedding = "embedding"
	CapabilitySpeech    = "speech"
)

// 内建厂商默认端点：渠道 base_url 留空时套用（源 DeepSeekDriver.php:20-23、
// AliyunDashScopeDriver.php:39-42、ResponsesDriver.php:33-36）。
//
// Responses 协议默认端点是 DeepSeek 官网（源注释：DeepSeek Responses API / Codex 生态），
// 故与 deepseek 共用同一条默认值。
const (
	deepSeekDefaultBaseURL  = "https://api.deepseek.com"
	dashScopeDefaultBaseURL = "https://dashscope.aliyuncs.com/compatible-mode/v1"
)

// DriverEnv 是装配期传给驱动工厂的环境（本包驱动构造的唯一注入口）。
type DriverEnv struct {
	// AllowPrivateHosts=true 放行本机/内网目标（ollama 等自建上游）；入护栏客户端的
	// guardedDialControl 与探活、真实调用共用同一开关。
	AllowPrivateHosts bool
	// SSELimits 是流式三级上限（单行/单帧/累积）。零值 ⇒ 回落缺省 1MiB/8MiB/32MiB
	// （ParseSSEWithLimits/NewStreamAccumulator 内部归一，防"配置 0 ⇒ 静默不限"）。
	SSELimits SSELimits
	// Transport 是 HTTP 底座（护栏客户端分桶 + postJSON/do 的请求装配与状态码分类）。
	// 注册表未提供时按 AllowPrivateHosts/SSELimits 自建一个（见 NewDriverRegistry）。
	// 各协议实现必须共用它——每驱动各建一套护栏是纪律违规（newGuardedHTTPClient 是唯一出口）。
	Transport *OpenAIDriver
}

// Client 取「带出站护栏」的 HTTP 客户端（按超时值分桶复用）。
//
// 新驱动（含 dashscope_tts）需要用自带客户端发请求时走本方法，不得自行 new http.Client：
// 桶内客户端由 driver.go 的 newGuardedHTTPClient 构造，护栏与 OpenAI 驱动同源单点。
func (e DriverEnv) Client(timeoutSeconds int) *http.Client {
	if e.Transport == nil {
		return newGuardedHTTPClient(e.AllowPrivateHosts, EffectiveTimeout(timeoutSeconds))
	}
	return e.Transport.client(timeoutSeconds)
}

// DriverSpec 是一条驱动注册记录。
type DriverSpec struct {
	// Name 是渠道 driver 列的取值（小写；注册时归一，重复注册按覆盖处理）。
	Name string
	// Capabilities 是能力闭集子集（chat/embedding/speech），至少一项。
	Capabilities []string
	// Factory 构造驱动实例（注册表按名缓存，实例内复用连接池）。
	Factory func(env DriverEnv) Driver
}

var (
	driverSpecMu sync.RWMutex
	driverSpecs  = builtinDriverSpecTable()
)

// builtinDriverSpecTable 内建驱动表（名字 → 规格）。
func builtinDriverSpecTable() map[string]DriverSpec {
	table := make(map[string]DriverSpec, 4)
	for _, spec := range []DriverSpec{
		{
			Name: DriverNameOpenAI,
			// 源 OpenAiCompatibleDriver：chat + embedding 都支持。
			Capabilities: []string{CapabilityChat, CapabilityEmbedding},
			Factory: func(env DriverEnv) Driver {
				if env.Transport != nil {
					// 与注册表共用同一个底座实例：护栏、连接池、错误分类三件套单点。
					return env.Transport
				}
				return newBaseDriver(env)
			},
		},
		{
			Name: DriverNameDeepSeek,
			// 源 DeepSeekDriver.php:7 明写「仅 chat 能力（官网无 embedding 端点）」。
			Capabilities: []string{CapabilityChat},
			Factory: func(env DriverEnv) Driver {
				return &compatibleDriver{
					base:           baseDriverFor(env),
					defaultBaseURL: deepSeekDefaultBaseURL,
				}
			},
		},
		{
			Name: DriverNameDashScope,
			// 源 AliyunDashScopeDriver：type='both'，chat 与 embedding 都支持。
			Capabilities: []string{CapabilityChat, CapabilityEmbedding},
			Factory: func(env DriverEnv) Driver {
				return &compatibleDriver{
					base:           baseDriverFor(env),
					defaultBaseURL: dashScopeDefaultBaseURL,
					// 源 AliyunDashScopeDriver.php:24-28 的逻辑模型名 → 百炼模型名映射。
					// 差异登记：源另有 extra.model_map（渠道/厂商 settings 覆盖链），本插件
					// 渠道与厂商表都无 settings 列 ⇒ 以静态映射替代（README 差异节）。
					modelMap: map[string]string{
						"deepseek-chat":     "deepseek-v3",
						"deepseek-reasoner": "deepseek-r1",
					},
				}
			},
		},
		{
			Name: DriverNameResponses,
			// Responses 协议无向量端点（源 ResponsesDriver.php:43-47 的响亮失败）。
			Capabilities: []string{CapabilityChat},
			Factory: func(env DriverEnv) Driver {
				return newResponsesDriver(env, deepSeekDefaultBaseURL)
			},
		},
	} {
		table[spec.Name] = spec
	}
	return table
}

// newBaseDriver 构造本包 HTTP 底座驱动（**本文件唯一的 OpenAIDriver 构造点**：
// 构造器若再加形参，只改这一处 + DriverEnv 的透传）。
func newBaseDriver(env DriverEnv) *OpenAIDriver {
	return NewOpenAIDriverWithSSELimits(env.AllowPrivateHosts, env.SSELimits)
}

// baseDriverFor 取共用底座（注册表已注入则复用，独立构造时自建）。
func baseDriverFor(env DriverEnv) *OpenAIDriver {
	if env.Transport != nil {
		return env.Transport
	}
	return newBaseDriver(env)
}

// Register 注册（或覆盖）一条驱动。装配期调用；同名再注册视为有意替换（后注册者生效，
// 装配缓存键含 RegistrySignature() ⇒ 已注册集合变化会重建注册表实例）。
func Register(spec DriverSpec) error {
	normalized, err := normalizeDriverSpec(spec)
	if err != nil {
		return err
	}
	driverSpecMu.Lock()
	defer driverSpecMu.Unlock()
	driverSpecs[normalized.Name] = normalized
	return nil
}

// unregisterDriverSpec 注销一条驱动（测试隔离用：注册-断言-注销，避免污染同包其它用例）。
func unregisterDriverSpec(name string) {
	name = canonicalDriverName(name)
	if name == "" {
		return
	}
	driverSpecMu.Lock()
	defer driverSpecMu.Unlock()
	delete(driverSpecs, name)
}

// normalizeDriverSpec 校验并归一规格（名字小写去空白、能力去重排序、工厂必填）。
func normalizeDriverSpec(spec DriverSpec) (DriverSpec, error) {
	name := canonicalDriverName(spec.Name)
	if name == "" {
		return DriverSpec{}, fmt.Errorf("驱动名不能为空")
	}
	if spec.Factory == nil {
		return DriverSpec{}, fmt.Errorf("驱动 %q 缺少工厂函数", name)
	}
	if len(spec.Capabilities) == 0 {
		return DriverSpec{}, fmt.Errorf("驱动 %q 必须声明至少一项能力（%s）", name, strings.Join(closedCapabilities(), "/"))
	}
	seen := make(map[string]struct{}, len(spec.Capabilities))
	capabilities := make([]string, 0, len(spec.Capabilities))
	for _, capability := range spec.Capabilities {
		c := strings.ToLower(strings.TrimSpace(capability))
		if !isClosedCapability(c) {
			return DriverSpec{}, fmt.Errorf("驱动 %q 声明了能力闭集之外的能力 %q（闭集：%s）", name, capability, strings.Join(closedCapabilities(), "/"))
		}
		if _, ok := seen[c]; ok {
			continue
		}
		seen[c] = struct{}{}
		capabilities = append(capabilities, c)
	}
	sort.Strings(capabilities)
	return DriverSpec{Name: name, Capabilities: capabilities, Factory: spec.Factory}, nil
}

// canonicalDriverName 归一驱动名（去空白 + 小写；空串保持空串，交由解析层回落 openai）。
//
// 与 handler_channel.go 的 normalizeDriverName（渠道表单的形状校验，签名与用途不同）
// 刻意分名：那个管「用户输入长什么样」，这个管「注册表键长什么样」。
func canonicalDriverName(name string) string { return strings.ToLower(strings.TrimSpace(name)) }

// closedCapabilities 能力闭集的稳定输出（错误文案与校验共用）。
func closedCapabilities() []string {
	return []string{CapabilityChat, CapabilityEmbedding, CapabilitySpeech}
}

func isClosedCapability(capability string) bool {
	for _, c := range closedCapabilities() {
		if c == capability {
			return true
		}
	}
	return false
}

// lookupDriverSpec 按名取规格（空串回落 openai；未注册返回 false）。
func lookupDriverSpec(name string) (DriverSpec, bool) {
	name = canonicalDriverName(name)
	if name == "" {
		name = DriverNameOpenAI
	}
	driverSpecMu.RLock()
	defer driverSpecMu.RUnlock()
	spec, ok := driverSpecs[name]
	return spec, ok
}

// GetSupported 已注册驱动名（字典序）——渠道表单「驱动下拉」的取值出口
// （路由明文；页面取的是本函数的返回值）。
func GetSupported() []string {
	driverSpecMu.RLock()
	defer driverSpecMu.RUnlock()
	names := make([]string, 0, len(driverSpecs))
	for name := range driverSpecs {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// SupportsCapability 该驱动是否声明支持能力闭集里的能力（未注册 ⇒ false）。
func SupportsCapability(name, capability string) bool {
	spec, ok := lookupDriverSpec(name)
	if !ok {
		return false
	}
	return specSupports(spec, capability)
}

// specSupports 规格是否声明某能力。
func specSupports(spec DriverSpec, capability string) bool {
	for _, c := range spec.Capabilities {
		if c == capability {
			return true
		}
	}
	return false
}

// RegistrySignature 已注册驱动集合的稳定指纹（名字 + 能力，按名排序后 sha256 前 16 位 hex）。
//
// 用途：装配面把它并入 defaultService/sharedDriver 的装配缓存键——注册集合（含能力声明）
// 变化即重建注册表实例，避免"新注册的驱动在旧实例上不生效"。
func RegistrySignature() string {
	driverSpecMu.RLock()
	specs := make([]DriverSpec, 0, len(driverSpecs))
	for _, spec := range driverSpecs {
		specs = append(specs, spec)
	}
	driverSpecMu.RUnlock()
	sort.Slice(specs, func(i, j int) bool { return specs[i].Name < specs[j].Name })

	digest := sha256.New()
	for _, spec := range specs {
		digest.Write([]byte(spec.Name))
		digest.Write([]byte{0})
		digest.Write([]byte(strings.Join(spec.Capabilities, ",")))
		digest.Write([]byte{0})
	}
	return hex.EncodeToString(digest.Sum(nil))[:16]
}

// DriverRegistry 是按渠道驱动名分派的多驱动注册表：它自身实现 Driver 四方法
// （driver.go 的 Driver 接口），因此对编排层（service.go）与探活（handler_channel.go）
// 完全透明——两处都只认接口，装配侧换成本类型即获得分派。
type DriverRegistry struct {
	env       DriverEnv
	mu        sync.Mutex
	instances map[string]Driver
}

// NewDriverRegistry 构造注册表（Transport 缺省自建：唯一 HTTP 底座，各协议实现共用）。
func NewDriverRegistry(env DriverEnv) *DriverRegistry {
	if env.Transport == nil {
		env.Transport = newBaseDriver(env)
	}
	return &DriverRegistry{env: env, instances: make(map[string]Driver)}
}

// Env 返回构造期环境（装配面/测试读取生效的 allow_private 与 SSE 上限）。
func (r *DriverRegistry) Env() DriverEnv { return r.env }

// Resolve 按驱动名解析实例（空名 ⇒ openai）。未知驱动返回 local 错误，不静默回落。
func (r *DriverRegistry) Resolve(name string) (Driver, error) {
	spec, ok := lookupDriverSpec(name)
	if !ok {
		return nil, unknownDriverError(name)
	}
	return r.instanceOf(spec)
}

// ResolveChannel 按渠道的 driver 列解析实例（本包读取该列的唯一入口；
// 探活与语音面可用它直接拿驱动，不必走四方法）。空值回落 openai。
func (r *DriverRegistry) ResolveChannel(ch Channel) (Driver, error) { return r.Resolve(ch.Driver) }

// instanceOf 取（并缓存）规格对应的驱动实例。
func (r *DriverRegistry) instanceOf(spec DriverSpec) (Driver, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if driver, ok := r.instances[spec.Name]; ok {
		return driver, nil
	}
	driver := spec.Factory(r.env)
	if driver == nil {
		return nil, localError("driver_unavailable", fmt.Sprintf("驱动 %q 的工厂未构造出实例", spec.Name))
	}
	r.instances[spec.Name] = driver
	return driver, nil
}

// resolveFor 解析 + 能力门（capability 为空 ⇒ 不做门，Models 走这条）。
func (r *DriverRegistry) resolveFor(ch Channel, capability string) (Driver, error) {
	spec, ok := lookupDriverSpec(ch.Driver)
	if !ok {
		return nil, unknownDriverError(ch.Driver)
	}
	if capability != "" && !specSupports(spec, capability) {
		return nil, unsupportedCapabilityError(spec, capability)
	}
	return r.instanceOf(spec)
}

// Chat 按渠道驱动分派非流式对话。
func (r *DriverRegistry) Chat(ctx context.Context, ch Channel, req ChatRequest) (*ChatResult, error) {
	driver, err := r.resolveFor(ch, CapabilityChat)
	if err != nil {
		return nil, err
	}
	return driver.Chat(ctx, ch, req)
}

// ChatStream 按渠道驱动分派流式对话。
func (r *DriverRegistry) ChatStream(ctx context.Context, ch Channel, req ChatRequest, sink func(data []byte) error) (*ChatResult, error) {
	driver, err := r.resolveFor(ch, CapabilityChat)
	if err != nil {
		return nil, err
	}
	return driver.ChatStream(ctx, ch, req, sink)
}

// Embeddings 按渠道驱动分派向量请求（未声明 embedding 的驱动被能力门拒）。
func (r *DriverRegistry) Embeddings(ctx context.Context, ch Channel, req EmbeddingRequest) (*EmbeddingResult, error) {
	driver, err := r.resolveFor(ch, CapabilityEmbedding)
	if err != nil {
		return nil, err
	}
	return driver.Embeddings(ctx, ch, req)
}

// Models 按渠道驱动分派探活（handler_channel.go 的探活按钮唯一依赖面；不做能力门）。
func (r *DriverRegistry) Models(ctx context.Context, ch Channel) ([]string, error) {
	driver, err := r.resolveFor(ch, "")
	if err != nil {
		return nil, err
	}
	return driver.Models(ctx, ch)
}

// Speak 按渠道驱动分派语音合成。
//
// 为什么注册表要实现 SpeechDriver（service_tts.go 声明的消费侧窄接口）：装配后 Service 持有的
// driver 就是本注册表，语音路径的 `s.driver.(SpeechDriver)` 断言必须在**本类型**上成立，
// 再由本方法按 ch.Driver 分派到真正的语音驱动（dashscope_tts 经 Register 自注册）。
//
// 两层能力门都走 speech：① 注册表按渠道驱动声明的能力拒（openai/responses 等没声明 speech）；
// ② 再问被解析出的驱动自身 SupportsSpeech()（缺席按支持处理，与 speakerSupportsSpeech 同口径）。
// 注册表**刻意不实现** SupportsSpeech()：它没有渠道入参，逐渠道路径的判断只能落在这里。
func (r *DriverRegistry) Speak(ctx context.Context, ch Channel, req SpeechRequest) (*SpeechResult, error) {
	driver, err := r.resolveFor(ch, CapabilitySpeech)
	if err != nil {
		return nil, err
	}
	speaker, ok := driver.(SpeechDriver)
	if !ok || !speakerSupportsSpeech(speaker) {
		return nil, speechUnsupportedError(ch)
	}
	return speaker.Speak(ctx, ch, req)
}

// unknownDriverError 未注册驱动：local 错误 + 可用驱动清单（不静默回落 openai）。
func unknownDriverError(name string) *UpstreamError {
	display := strings.TrimSpace(name)
	if display == "" {
		display = "(空)"
	}
	return localError("unknown_driver", fmt.Sprintf(
		"渠道配置的驱动 %q 未注册（可用驱动: %s）；请检查渠道的 driver 字段或安装对应驱动",
		display, strings.Join(GetSupported(), ", ")))
}

// unsupportedCapabilityError 能力门拒绝：local 错误 + 该驱动声明的能力清单。
func unsupportedCapabilityError(spec DriverSpec, capability string) *UpstreamError {
	return localError("unsupported_capability", fmt.Sprintf(
		"驱动 %q 不支持 %s（声明的能力: %s）", spec.Name, capability, strings.Join(spec.Capabilities, ", ")))
}

// ===== OpenAI 兼容变体驱动（deepseek / dashscope）=====

// compatibleDriver 是「OpenAI 兼容 + 厂商默认值」驱动：委托底座实现，只补三件事——
//
//	① 渠道 base_url 留空时套厂商默认端点（源子类构造期行为）；
//	② 请求模型名按静态 modelMap 归一（源 AliyunDashScopeDriver::chat/embedding 口径；
//	   以静态映射替代源的 extra.model_map，见内建表注释）；
//	③ 结果模型名回填「请求侧解析名」（源 normalizeResponse 口径：以 ra_ai_model 注册名为准，
//	   不采上游回显别名——防消息标签与用量表不同名）。
type compatibleDriver struct {
	base           Driver
	defaultBaseURL string
	modelMap       map[string]string
}

func (d *compatibleDriver) Chat(ctx context.Context, ch Channel, req ChatRequest) (result *ChatResult, err error) {
	defer func() { err = redactUpstreamErr(err, ch.APIKey) }()
	logical := effectiveModelName(ch, req.Model)
	res, err := d.base.Chat(ctx, d.upstreamChannel(ch, req.Model), req)
	return d.finalize(res, err, logical)
}

func (d *compatibleDriver) ChatStream(ctx context.Context, ch Channel, req ChatRequest, sink func(data []byte) error) (result *ChatResult, err error) {
	defer func() { err = redactUpstreamErr(err, ch.APIKey) }()
	logical := effectiveModelName(ch, req.Model)
	res, err := d.base.ChatStream(ctx, d.upstreamChannel(ch, req.Model), req, sink)
	return d.finalize(res, err, logical)
}

func (d *compatibleDriver) Embeddings(ctx context.Context, ch Channel, req EmbeddingRequest) (result *EmbeddingResult, err error) {
	defer func() { err = redactUpstreamErr(err, ch.APIKey) }()
	logical := effectiveModelName(ch, req.Model)
	res, err := d.base.Embeddings(ctx, d.upstreamChannel(ch, req.Model), req)
	if err != nil {
		return res, err
	}
	if res != nil && logical != "" {
		res.Model = logical
	}
	return res, nil
}

func (d *compatibleDriver) Models(ctx context.Context, ch Channel) (models []string, err error) {
	defer func() { err = redactUpstreamErr(err, ch.APIKey) }()
	return d.base.Models(ctx, d.upstreamChannel(ch, ""))
}

// finalize 结果收尾：成功时把结果模型名回填请求侧解析名（源 normalizeResponse 口径）。
func (d *compatibleDriver) finalize(res *ChatResult, err error, logical string) (*ChatResult, error) {
	if err != nil {
		return res, err
	}
	if res != nil && logical != "" {
		res.Model = logical
	}
	return res, nil
}

// upstreamChannel 补默认端点 + 把渠道 model 换成归一后的上游模型名。
//
// 之所以改的是渠道 model 而不是请求 Model：ChatRequest.body / EmbeddingRequest.body 的
// 模型取用口径是「渠道 model 非空优先」（driver.go:690-697），改渠道字段即命中同一条链路，
// 不必另造一套请求装配（差异最小、口径单点）。
func (d *compatibleDriver) upstreamChannel(ch Channel, reqModel string) Channel {
	if strings.TrimSpace(ch.BaseURL) == "" && d.defaultBaseURL != "" {
		ch.BaseURL = d.defaultBaseURL
	}
	if name := effectiveModelName(ch, reqModel); name != "" {
		if mapped, ok := d.modelMap[name]; ok {
			name = mapped
		}
		ch.Model = name
	}
	return ch
}

// effectiveModelName 请求实际使用的模型名：渠道 model 非空优先，否则请求侧模型名
// （与 driver.go 的 ChatRequest.body / EmbeddingRequest.body 逐字同口径）。
func effectiveModelName(ch Channel, reqModel string) string {
	if ch.Model != "" {
		return ch.Model
	}
	return reqModel
}
