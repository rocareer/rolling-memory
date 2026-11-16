// plugin: ai v0.2.0
// plugin.go 是 AI 路由插件的唯一接线点，同时承载接口层的公共装配：可注入依赖（deps）、
// 插件配置的读出口（5s TTL 进程内缓存）、密钥脱敏与宿主表格的列表契约（分页 + wheres 高级检索）。
//
// 接线：
//   - mount：宿主（internal/plugins/wiring.go）建好带运行时闸 host.Gate("ai") 的 plugin/ai 子组后转交；
//     本包只写相对段（写了绝对前缀会双前缀，且 BuildCheckPath 算出的节点名与 manifest 不再逐字相等）。
//     每条后台路由**自挂** middleware.AdminAuth + middleware.AdminPermission：闸只管「插件是否启用」，
//     节点管「这个管理员能不能用」；adminRoutes() 的 Node 与 plugin.yaml 的 menus[].name 逐字相等
//     （运行时节点名 = permission.BuildCheckPath(FullPath)：剥后台入口前缀、遇 ":" 段截断、
//     list|get 由中间件按 [同名, 同前缀/read] OR 回落）。
//   - mountPublic：在根引擎上逐字写 /ai/v1/*（无后台前缀、无 AdminAuth），只带宿主的运行时闸；
//     链路 = apiKeyAuth（按 Key 查库 + 计量归因 + 按 Key 限流覆盖）→ rateLimit（插件自持令牌桶 + 在飞并发）
//     → 业务处理器；鉴权兜底仍是 v1 的全局密钥（sha256 + subtle.ConstantTimeCompare、空 key 503 fail-closed），
//     错误体为 OpenAI 形状且带真实 HTTP 状态码。
//   - 装配点（defaultService / mount）：驱动注册表（多驱动分派）、渠道准入门（RPM/TPM）、
//     任务 worker 的启动都在这里接线；三者的开关语义见各自文件头注。
//
// 分层：handler 只做协议（解析/鉴权/回执）；SQL 只在 store.go；决策只在 circuit.go/service.go。
// 唯一的越层点是 wheres 高级检索的条件拼装——store.go 的列表原语（ListChannels/ListModels/
// ListUsageLogs）不接受 wheres，而接口契约要求「只有不在筛选字段白名单的字段名 / 不在 16 项词表的
// 操作符才拒」。本层的拼装只用白名单内的**已引用列名常量**与位置占位符，基础查询仍复用 store 的
// channelQuery/modelQuery/usageLogQuery（同一筛选、排序、分页口径），因此不构成第二套列表实现。
package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ai-go-hub/ai-go-admin/internal/infra/database"
	"github.com/ai-go-hub/ai-go-admin/internal/kit/httpx"
	"github.com/ai-go-hub/ai-go-admin/internal/middleware"
	"github.com/ai-go-hub/ai-go-admin/internal/model"
	"github.com/ai-go-hub/ai-go-admin/internal/plugin/host"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// 插件自身的事实（与 plugin.yaml 逐字对齐；重编译后进程内注册表以本处常量为准）。
const (
	// Name 插件名（= plugins/ 目录名 = admin_plugins.name = 路由段 plugin/<名>）
	Name = "ai"
	// Version 插件版本（= plugin.yaml 的 version）
	//
	// 0.2.0 是迁移 2..6 对应的版本：新增 SSE 三级上限、按 Key 鉴权/限流、厂商继承、
	// 渠道准入门、多驱动注册表、异步任务、TTS 七类能力，并登记 10 个新配置键。
	// **升级代价（与 install 的归属标记联动）**：每次 install 会对源树逐文件前插/刷新
	// `// plugin: ai v<本常量>` 标记（internal/plugin/cli/marker.go 的幂等判据是「文件头已是
	// 同一 version 标记」），版本一变标记就叠一层 ⇒ 下次 install 会改写插件源树的文件头
	// （含 web 源与落位副本；两者同批处理，sha256 仍一致）。已装插件要应用新增迁移只能走
	// 「disable → uninstall --keep-files → install --enable」（台账已有行时 install 硬拒，
	// internal/plugin/cli/dbwrite.go:45-51；宿主没有「对已装插件单独应用新迁移」的命令）。
	Version = "0.2.0"
	// Kind 插件形态（= plugin.yaml 的 kind：mixed —— 有 Go 代码与自带 L1 页，表由迁移自管）
	Kind = "mixed"
)

func init() {
	// 语音驱动（dashscope_tts）在装配期自注册：注册表按渠道 driver 列分派到它，
	// 且注册集合并入 RegistrySignature()（装配缓存键）。不注册 ⇒ 驱动名走 unknown_driver，
	// 语音路由以「渠道驱动不支持语音合成」响亮失败（不静默降级）。
	if err := Register(DriverSpec{
		Name:         DashScopeTTSDriverName,
		Capabilities: []string{CapabilitySpeech},
		Factory:      func(env DriverEnv) Driver { return NewDashScopeTTSDriver(env.AllowPrivateHosts) },
	}); err != nil {
		log.Printf("ai: 注册语音驱动 %s 失败（语音路由将不可用）: %v", DashScopeTTSDriverName, err)
	}
	// MountPublic 需要机制接口 >=1.1（host.APIVersion 当前 1.1.0），manifest 的 host_api 下界已随之提高。
	host.Register(host.Meta{Name: Name, Version: Version, Kind: Kind, Mount: mount, MountPublic: mountPublic})
}

// mount 后台路由挂载点：宿主给的是**已挂运行时闸**的 plugin/ai 子组。
func mount(g *gin.RouterGroup) {
	defaultDeps.mountAdmin(g)
	// 异步任务 worker：进程内单例（sync.Once）、DB 已初始化后启动；停用态不靠钩子（宿主注册表只有
	// 「名/版本/形态/挂载点」，无生命周期回调），由 worker 每轮自查台账 admin_plugins.status 收敛。
	StartTaskWorkerOnce(context.Background())
}

// mountPublic 公开路由挂载点：宿主在根引擎上建「只挂运行时闸」的组后转交，路径逐字对外。
func mountPublic(g *gin.RouterGroup) { defaultDeps.mountPublic(g) }

// ===== 路由表（接线与权限节点的唯一事实源）=====

// adminRoute 一条后台路由。
//
// Node 是权限节点名（= plugin.yaml 的 menus[].name = BuildCheckPath(FullPath)）；它不参与
// 中间件的运行时判定（中间件自己从 FullPath 算），而是把「路由 ↔ 节点」的对应关系固化成
// 可被测试逐条比对的表（handler_test.go 与 plugin.yaml 交叉核对）。
type adminRoute struct {
	Method  string
	Path    string // 相对段（宿主已提供 plugin/ai 前缀）
	Node    string
	Handler gin.HandlerFunc
}

// publicRoute 一条公开路由（MountPublic 逐字写绝对路径）。
type publicRoute struct {
	Method  string
	Path    string
	Handler gin.HandlerFunc
}

// adminRoutes 42 条后台路由
// （渠道 9 / 模型 6 / 用量 1 / 调用日志 2 / 总览 1 / 设置 1 = v1 的 20 条
// ＋ 厂商 5 / 应用接入 7 / 任务 4 / 语音 6 = 本批 22 条），
// 与 plugin.yaml 的 36 个节点一一对应（list 与 get 共用 read 节点）。
func (d deps) adminRoutes() []adminRoute {
	return []adminRoute{
		// ---- 渠道管理 ----
		{http.MethodPost, "/channel/list", "plugin/ai/channel/read", d.channelList},
		{http.MethodGet, "/channel/get/:pk", "plugin/ai/channel/read", d.channelGet},
		{http.MethodPost, "/channel/create", "plugin/ai/channel/create", d.channelCreate},
		{http.MethodPost, "/channel/update/:pk", "plugin/ai/channel/update", d.channelUpdate},
		{http.MethodPost, "/channel/delete", "plugin/ai/channel/delete", d.channelDelete},
		{http.MethodPost, "/channel/switch", "plugin/ai/channel/switch", d.channelSwitch},
		{http.MethodPost, "/channel/probe", "plugin/ai/channel/probe", d.channelProbe},
		{http.MethodPost, "/channel/breaker", "plugin/ai/channel/breaker", d.channelBreaker},
		{http.MethodPost, "/channel/events", "plugin/ai/channel/events", d.channelEvents},
		// ---- 模型管理 ----
		{http.MethodPost, "/model/list", "plugin/ai/model/read", d.modelList},
		{http.MethodGet, "/model/get/:pk", "plugin/ai/model/read", d.modelGet},
		{http.MethodPost, "/model/create", "plugin/ai/model/create", d.modelCreate},
		{http.MethodPost, "/model/update/:pk", "plugin/ai/model/update", d.modelUpdate},
		{http.MethodPost, "/model/delete", "plugin/ai/model/delete", d.modelDelete},
		{http.MethodPost, "/model/bind", "plugin/ai/model/bind", d.modelBind},
		// ---- 用量与调用日志 ----
		{http.MethodPost, "/usage/list", "plugin/ai/usage/read", d.usageList},
		{http.MethodPost, "/usage-log/list", "plugin/ai/usage-log/read", d.usageLogList},
		{http.MethodPost, "/usage-log/detail", "plugin/ai/usage-log/detail", d.usageLogDetail},
		// ---- 总览与设置 ----
		{http.MethodPost, "/overview/index", "plugin/ai/overview/index", d.overviewIndex},
		{http.MethodPost, "/settings/save", "plugin/ai/settings/save", d.settingsSave},
		// ---- 厂商管理（F3）----
		{http.MethodPost, "/supplier/list", "plugin/ai/supplier/read", d.supplierList},
		{http.MethodGet, "/supplier/get/:pk", "plugin/ai/supplier/read", d.supplierGet},
		{http.MethodPost, "/supplier/create", "plugin/ai/supplier/create", d.supplierCreate},
		{http.MethodPost, "/supplier/update/:pk", "plugin/ai/supplier/update", d.supplierUpdate},
		{http.MethodPost, "/supplier/delete", "plugin/ai/supplier/delete", d.supplierDelete},
		// ---- 应用接入 / API Key（F1）----
		{http.MethodPost, "/apikey/list", "plugin/ai/apikey/read", d.apiKeyList},
		{http.MethodGet, "/apikey/get/:pk", "plugin/ai/apikey/read", d.apiKeyGet},
		{http.MethodPost, "/apikey/create", "plugin/ai/apikey/create", d.apiKeyCreate},
		{http.MethodPost, "/apikey/update/:pk", "plugin/ai/apikey/update", d.apiKeyUpdate},
		{http.MethodPost, "/apikey/delete", "plugin/ai/apikey/delete", d.apiKeyDelete},
		{http.MethodPost, "/apikey/reset/:pk", "plugin/ai/apikey/reset", d.apiKeyReset},
		{http.MethodPost, "/apikey/switch", "plugin/ai/apikey/switch", d.apiKeySwitch},
		// ---- 异步任务（F5+F6）----
		{http.MethodPost, "/task/list", "plugin/ai/task/read", d.taskList},
		{http.MethodGet, "/task/get/:pk", "plugin/ai/task/read", d.taskGet},
		{http.MethodPost, "/task/submit", "plugin/ai/task/submit", d.taskSubmit},
		{http.MethodGet, "/task/export/:pk", "plugin/ai/task/export", d.taskExport},
		// ---- 语音 / 音色（F7）----
		{http.MethodPost, "/voice/list", "plugin/ai/voice/read", d.voiceList},
		{http.MethodGet, "/voice/get/:pk", "plugin/ai/voice/read", d.voiceGet},
		{http.MethodPost, "/voice/create", "plugin/ai/voice/create", d.voiceCreate},
		{http.MethodPost, "/voice/update/:pk", "plugin/ai/voice/update", d.voiceUpdate},
		{http.MethodPost, "/voice/delete", "plugin/ai/voice/delete", d.voiceDelete},
		{http.MethodPost, "/voice/preview", "plugin/ai/voice/preview", d.voicePreview},
		// ---- 上游内核同款 AI 资产体检（24 项机械判据）与成本经济学账单 ----
		{http.MethodPost, "/report", "plugin/ai/report", d.reportIndex},
	}
}

// chain 一条后台路由的处理器链：AdminAuth（未登录 401）→ AdminPermission（无节点 403）→ 业务处理器。
//
// 单独成函数，是为了让「每条路由都挂着这两个中间件」成为可被测试逐条断言的事实
// （handler_test.go 按运行时函数名核对链路，并逐个发无凭证请求验行为）。
func (r adminRoute) chain() []gin.HandlerFunc {
	return []gin.HandlerFunc{middleware.AdminAuth(), middleware.AdminPermission(), r.Handler}
}

// mountAdmin 逐条挂载：AdminAuth（未登录 401）→ AdminPermission（无节点 403）→ 业务处理器。
func (d deps) mountAdmin(g *gin.RouterGroup) {
	for _, route := range d.adminRoutes() {
		g.Handle(route.Method, route.Path, route.chain()...)
	}
}

// publicRoutes 4 条公开路由（/ai/v1/*）：非流式与流式共用 chat/completions，stream 由请求体决定；
// 语音合成（F7）与 chat/embeddings 共用同一鉴权与限流链。
func (d deps) publicRoutes() []publicRoute {
	return []publicRoute{
		{http.MethodPost, "/ai/v1/chat/completions", d.publicChat},
		{http.MethodPost, "/ai/v1/embeddings", d.publicEmbeddings},
		{http.MethodGet, "/ai/v1/models", d.publicModels},
		{http.MethodPost, "/ai/v1/audio/speech", d.publicSpeech},
	}
}

// mountPublic 逐条挂载：apiKeyAuth（按 Key 查库 + 计量归因 + 限流覆盖，未命中回落全局密钥，
// 查库出错 fail-closed）→ rateLimit（插件自持令牌桶；未过鉴权的请求不占配额）→ 业务处理器。
//
// 宿主另有全局 Throttle（cmd/serve/serve.go 在插件 handler 之前 Abort，按 IP+URL 10/s），
// 任何断言「本插件 429」的场景须先关/调高宿主节流（见 README 的差异与安全节）。
func (d deps) mountPublic(g *gin.RouterGroup) {
	for _, route := range d.publicRoutes() {
		g.Handle(route.Method, route.Path, d.apiKeyAuth(), d.rateLimit(), route.Handler)
	}
}

// ===== 接口层可注入依赖 =====

// deps 是接口层的全部依赖。生产用 defaultDeps（真库 + 真驱动 + 配置缓存）；
// 测试注入假实现，从而在**无数据库**的环境下覆盖鉴权、参数校验与脱敏（handler_test.go）。
type deps struct {
	// loadSettings 读插件配置（含 5s TTL 缓存）
	loadSettings func(ctx context.Context) (pluginSettings, error)
	// loadStore 取数据访问单例（未接库时返回可读错误，fail-closed）
	loadStore func(ctx context.Context) (*Store, error)
	// loadService 取编排单例（按配置指纹与库句柄建缓存）
	loadService func(ctx context.Context, set pluginSettings) (*Service, error)
	// loadAuthStore 公开面鉴权（apiKeyAuth）的查库面；缺省（nil）从 loadStore 取 *Store。
	// 无库单测据此注入假 store，使公开面的协议用例仍走**生产挂载链**（mountPublic 一字不改）。
	loadAuthStore func(ctx context.Context) (apiKeyAuthStore, error)
}

// defaultDeps 生产装配。
var defaultDeps = deps{
	loadSettings: loadPluginSettings,
	loadStore:    defaultStore,
	loadService:  defaultService,
}

// storeOrFail 取 store；不可用时写 fail-closed 回执（HTTP 200 + code=1）并返回 nil。
func (d deps) storeOrFail(c *gin.Context) *Store {
	store, err := d.loadStore(c.Request.Context())
	if err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return nil
	}
	return store
}

// serviceOrFail 取编排单例；不可用时按公开面错误形状 fail-closed 并返回 false。
func (d deps) serviceOrFail(c *gin.Context) (*Service, pluginSettings, bool) {
	ctx := c.Request.Context()
	set, err := d.loadSettings(ctx)
	if err != nil {
		writePublicAPIError(c, &APIError{
			HTTPStatus: http.StatusServiceUnavailable,
			Type:       "upstream_unavailable",
			Code:       "settings_unavailable",
			Message:    "服务暂不可用",
			Class:      ErrorClassLocal,
			Err:        err,
		})
		return nil, pluginSettings{}, false
	}
	service, err := d.loadService(ctx, set)
	if err != nil {
		writePublicAPIError(c, &APIError{
			HTTPStatus: http.StatusServiceUnavailable,
			Type:       "upstream_unavailable",
			Code:       "store_unavailable",
			Message:    "服务暂不可用",
			Class:      ErrorClassLocal,
			Err:        err,
		})
		return nil, set, false
	}
	return service, set, true
}

var (
	storeMu  sync.Mutex
	storeDB  *gorm.DB
	storeVal *Store

	engineMu    sync.Mutex
	engineDB    *gorm.DB
	engineSig   string
	engineVal   *Service
	engineReady bool

	driverMu    sync.Mutex
	driverVal   Driver
	driverSig   string
	driverReady bool

	channelGateMu  sync.Mutex
	channelGateVal *ChannelGate
)

// sharedDriver 按「私网上游开关 + SSE 三级上限 + 已注册驱动集合」复用驱动注册表实例。
//
// 分派与护栏都在注册表内部（driver_registry.go）：按渠道的 driver 列解析实现、未知驱动响亮失败；
// 表内各驱动共用同一 http.Client 分桶（每请求新建会让连接池与 TLS 会话失效；
// 探活与转发共用同一实例）。注册集合变化（含内建驱动 + dashscope_tts 自注册）
// 会让 RegistrySignature() 变化 ⇒ 重建实例。
func sharedDriver(allowPrivate bool, limits SSELimits) Driver {
	limits = limits.normalized()
	sig := fmt.Sprintf("p=%t|line=%d|frame=%d|acc=%d|reg=%s",
		allowPrivate, limits.MaxLineBytes, limits.MaxFrameBytes, limits.MaxAccumulatedBytes, RegistrySignature())

	driverMu.Lock()
	defer driverMu.Unlock()
	if driverVal != nil && driverReady && driverSig == sig {
		return driverVal
	}
	driverVal = NewDriverRegistry(DriverEnv{AllowPrivateHosts: allowPrivate, SSELimits: limits})
	driverSig, driverReady = sig, true
	return driverVal
}

// sharedChannelGate 复用渠道准入门实例：桶与 TPM 窗口是**进程内记账状态**，
// 每次重建 Service（设置指纹变化）都换新实例会把已消耗的预算清零（等于静默放宽限额）。
func sharedChannelGate() *ChannelGate {
	channelGateMu.Lock()
	defer channelGateMu.Unlock()
	if channelGateVal == nil {
		channelGateVal = NewChannelGate()
	}
	return channelGateVal
}

// defaultStore 返回与当前全局连接绑定的 *Store（连接句柄变化时重建缓存）。
func defaultStore(ctx context.Context) (*Store, error) {
	db := database.DB()
	if db == nil {
		return nil, fmt.Errorf("%w（接口层须在 database.Init() 之后使用）", ErrStoreUnavailable)
	}
	storeMu.Lock()
	defer storeMu.Unlock()
	if storeVal != nil && storeDB == db {
		return storeVal, nil
	}
	storeVal = NewStore(db)
	storeDB = db
	return storeVal, nil
}

// defaultService 按「库句柄 + 配置指纹」缓存编排单例：
// 驱动持私网上游开关、编排层持超时与币种，任一项变化都必须换一个新实例（否则改了设置不生效）。
func defaultService(ctx context.Context, set pluginSettings) (*Service, error) {
	store, err := defaultStore(ctx)
	if err != nil {
		return nil, err
	}
	sig := set.signature()

	engineMu.Lock()
	defer engineMu.Unlock()
	if engineVal != nil && engineReady && engineDB == store.DB() && engineSig == sig {
		return engineVal, nil
	}
	driver := sharedDriver(set.AllowPrivateHosts, set.sseLimits())
	service := NewService(store, driver, Config{
		RequestTimeout:         set.RequestTimeout,
		StreamTimeout:          set.StreamTimeout,
		Currency:               set.Currency,
		AllowPrivateHosts:      set.AllowPrivateHosts,
		BreakerFailThreshold:   set.BreakerFailThreshold,
		BreakerCooldownSeconds: set.BreakerCooldownSeconds,
	}, nil)
	// 渠道准入门（F4）：同包安装函数同时挂 Router.gate（候选级准入）与 Circuit.charger（TPM 预算回写）。
	// 门实例进程内单例（见 sharedChannelGate）：重建 Service 不得清零已消耗的预算。
	installChannelGate(service, sharedChannelGate())
	engineVal, engineDB, engineSig, engineReady = service, store.DB(), sig, true
	return service, nil
}

// ===== 插件配置（17 键）：唯一读出口 + 5s TTL 进程内缓存 =====

// 插件配置的落库键名（安装器写 configs：plugin_<名>_<键>，分组 plugin.ai）。
const (
	configKeyAPIKey               = "plugin_ai_api_key"
	configKeyBreakerFailThreshold = "plugin_ai_breaker_fail_threshold"
	configKeyBreakerCooldown      = "plugin_ai_breaker_cooldown_seconds"
	configKeyRequestTimeout       = "plugin_ai_request_timeout"
	configKeyStreamTimeout        = "plugin_ai_stream_timeout"
	configKeyCurrency             = "plugin_ai_currency"
	configKeyAllowPrivateHosts    = "plugin_ai_allow_private_hosts"
)

// 本批新增键里**只有 SSE 三键的常量落在本文件**：另外三组（公开面限流三键、渠道准入门两缺省、
// 任务 worker 两键）的键名常量由各自的读出口文件声明（ratelimit.go / gate.go / task.go 的
// configKey* 常量），本文件只把它们并入 pluginConfigKeys 与设置面——
// 键名在包内是唯一的，两处各写一遍会立刻是重复声明。
const (
	configKeySSEMaxLineBytes        = "plugin_ai_sse_max_line_bytes"
	configKeySSEMaxFrameBytes       = "plugin_ai_sse_max_frame_bytes"
	configKeySSEMaxAccumulatedBytes = "plugin_ai_sse_max_accumulated_bytes"
)

// pluginConfigKeys 17 键的声明顺序（读、写、回显共用一份，避免三处漂移）。
//
// 分组：v1 的 7 键 + 本批 10 键（SSE 三上限 / 限流三参数 / 渠道 Gate 两缺省 / 任务 worker 两键）；
// 顺序即 settings/save 的落库顺序与 updatePluginSettings 的固定遍历序。
var pluginConfigKeys = []string{
	configKeyAPIKey,
	configKeyBreakerFailThreshold,
	configKeyBreakerCooldown,
	configKeyRequestTimeout,
	configKeyStreamTimeout,
	configKeyCurrency,
	configKeyAllowPrivateHosts,
	configKeySSEMaxLineBytes,
	configKeySSEMaxFrameBytes,
	configKeySSEMaxAccumulatedBytes,
	configKeyRateLimitRPM,
	configKeyRateLimitBurst,
	configKeyRateLimitMaxInflight,
	configKeyChannelGateRPMDefault,
	configKeyChannelGateTPMDefault,
	configKeyTaskPollSeconds,
	configKeyTaskMaxAttempts,
}

// settingsCacheTTL 配置缓存上限：settings/save 后立即失效，最迟 5s 生效（显式取舍，写进 README）。
const settingsCacheTTL = 5 * time.Second

// pluginSettings 是 17 键的生效值（0/空/非法一律已回落代码默认与各键的归一函数）。
type pluginSettings struct {
	APIKey                 string
	APIKeySet              bool
	BreakerFailThreshold   int
	BreakerCooldownSeconds int
	RequestTimeout         int
	StreamTimeout          int
	Currency               string
	AllowPrivateHosts      bool

	// SSELimits 流式解析三级上限（≤0 已回落缺省 1MiB/8MiB/32MiB）。
	SSELimits SSELimits
	// RateLimit* 公开面按 Key 限流的插件全局三键（0 = 不限）。
	RateLimitRPM         int
	RateLimitBurst       int
	RateLimitMaxInflight int
	// ChannelGate* 渠道准入门的两缺省（0 = 不限；渠道自身的 rpm_limit/tpm_limit 优先）。
	ChannelGateRPMDefault int
	ChannelGateTPMDefault int
	// Task* 任务 worker 两键（poll 已 clamp ≥1、缺省 5；attempts 缺省 3）。
	TaskPollSeconds int
	TaskMaxAttempts int
}

// sseLimits 生效的 SSE 三级上限（≤0 再兜一层归一：防「配置 0 ⇒ 静默不限」）。
func (s pluginSettings) sseLimits() SSELimits { return s.SSELimits.normalized() }

// signature 是「影响编排实例构建」的配置指纹（改任一值即重建 Service）。
// 熔断阈值/冷却也在其中：它们是注入 Circuit 的全局默认，不重建实例就改了不生效；
// SSE 上限与驱动注册集合也在其中：前者进驱动实例、后者决定注册表实例（sharedDriver 的同名键）。
//
// 刻意**不在**其中的三组：公开面限流三键、渠道 Gate 两缺省、任务 worker 两键——
// 它们的读出口（ratelimit.go / gate.go / task.go）各自持有 5s TTL 缓存或每轮直读，
// 与 Service 实例无关；并入指纹会让「改限流」白重建几十个驱动桶与熔断状态。
func (s pluginSettings) signature() string {
	limits := s.sseLimits()
	return fmt.Sprintf("t=%d|s=%d|c=%s|p=%t|bt=%d|bc=%d|line=%d|frame=%d|acc=%d|reg=%s",
		s.RequestTimeout, s.StreamTimeout, s.Currency, s.AllowPrivateHosts,
		s.BreakerFailThreshold, s.BreakerCooldownSeconds,
		limits.MaxLineBytes, limits.MaxFrameBytes, limits.MaxAccumulatedBytes, RegistrySignature())
}

// parsePluginSettings 把 configs 的现值解析成生效配置（纯函数，可单测）。
// 数值键为 0 / 空串 / 非法一律回落代码默认（5 / 60 / 60 / 300），币种空 ⇒ USD。
//
// 本批 10 键复用**各自的归一函数**（parseRateLimitConfig / parseChannelGateDefaults /
// parseTaskSettings / SSELimits.normalized）：设置面回显与运行面判定必须同一份默认与 clamp，
// 否则「页面显示 100 而实际不限」这类口径分叉无法被测试咬住。
func parsePluginSettings(values map[string]string) pluginSettings {
	set := pluginSettings{
		APIKey:                 strings.TrimSpace(values[configKeyAPIKey]),
		BreakerFailThreshold:   DefaultFailThreshold,
		BreakerCooldownSeconds: DefaultCooldownSeconds,
		RequestTimeout:         DefaultRequestTimeout,
		StreamTimeout:          DefaultStreamTimeout,
		Currency:               DefaultCurrency,
	}
	set.APIKeySet = set.APIKey != ""
	if v, ok := positiveInt(values[configKeyBreakerFailThreshold]); ok {
		set.BreakerFailThreshold = v
	}
	if v, ok := positiveInt(values[configKeyBreakerCooldown]); ok {
		set.BreakerCooldownSeconds = v
	}
	if v, ok := positiveInt(values[configKeyRequestTimeout]); ok {
		set.RequestTimeout = clampInt(v, 1, 600)
	}
	if v, ok := positiveInt(values[configKeyStreamTimeout]); ok {
		set.StreamTimeout = clampInt(v, 1, 86400)
	}
	if currency := strings.TrimSpace(values[configKeyCurrency]); currency != "" {
		set.Currency = truncateRunes(currency, 10)
	}
	set.AllowPrivateHosts = isTruthy(values[configKeyAllowPrivateHosts])

	// 本批 10 键：SSE 三级上限逐键回落，其余三组走各自的归一函数（同一份默认与 clamp）。
	limits := DefaultSSELimits()
	if v, ok := positiveInt(values[configKeySSEMaxLineBytes]); ok {
		limits.MaxLineBytes = v
	}
	if v, ok := positiveInt(values[configKeySSEMaxFrameBytes]); ok {
		limits.MaxFrameBytes = v
	}
	if v, ok := positiveInt(values[configKeySSEMaxAccumulatedBytes]); ok {
		limits.MaxAccumulatedBytes = v
	}
	set.SSELimits = limits.normalized()

	rate := parseRateLimitConfig(values)
	set.RateLimitRPM, set.RateLimitBurst, set.RateLimitMaxInflight = rate.RPM, rate.Burst, rate.MaxInflight

	gate := parseChannelGateDefaults(values)
	set.ChannelGateRPMDefault, set.ChannelGateTPMDefault = gate.RPM, gate.TPM

	task := parseTaskSettings(values)
	set.TaskPollSeconds, set.TaskMaxAttempts = task.PollSeconds, task.MaxAttempts
	return set
}

// positiveInt 解析正整数（0 / 空串 / 非法一律视为「未配」）。
func positiveInt(raw string) (int, bool) {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value <= 0 {
		return 0, false
	}
	return value, true
}

// isTruthy 解析开关列的写法（1/true/yes/on，大小写不敏感）。
func isTruthy(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func clampInt(value, low, high int) int {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}

var (
	settingsMu       sync.RWMutex
	settingsCached   pluginSettings
	settingsExpireAt time.Time
)

// loadPluginSettings 配置的唯一读出口：命中未过期缓存即返回，否则查库并刷新缓存（含空值回落的默认）。
func loadPluginSettings(ctx context.Context) (pluginSettings, error) {
	settingsMu.RLock()
	if !settingsExpireAt.IsZero() && time.Now().Before(settingsExpireAt) {
		cached := settingsCached
		settingsMu.RUnlock()
		return cached, nil
	}
	settingsMu.RUnlock()

	values, err := readPluginConfigValues(ctx)
	if err != nil {
		return pluginSettings{}, err
	}
	set := parsePluginSettings(values)

	settingsMu.Lock()
	settingsCached, settingsExpireAt = set, time.Now().Add(settingsCacheTTL)
	settingsMu.Unlock()
	return set, nil
}

// invalidatePluginSettings 立即失效配置缓存（settings/save 成功后调用 ⇒ 密钥轮换与开关最迟 0s 生效）。
//
// 同时失效本批另外两组的读出口缓存（公开面限流三键、渠道准入门两缺省）：它们各自持 5s TTL，
// 不一起失效会出现「保存 rate_limit_rpm=10 成功、页面也回显 10，但第 11 次仍不 429」的窗口。
// 任务 worker 两键是每轮直读（无缓存），无需失效。
func invalidatePluginSettings() {
	settingsMu.Lock()
	settingsExpireAt = time.Time{}
	settingsMu.Unlock()
	invalidateRateLimitConfig()
	invalidateChannelGateConfig()
}

// readPluginConfigValues 读 17 个 plugin_ai_* 键的现值（只读本插件的键）。
func readPluginConfigValues(ctx context.Context) (map[string]string, error) {
	db := database.DB()
	if db == nil {
		return nil, fmt.Errorf("%w（配置读取须在 database.Init() 之后）", ErrStoreUnavailable)
	}
	rows, err := gorm.G[model.Config](db).Where("name IN ?", pluginConfigKeys).Find(ctx)
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

// ===== 密钥脱敏（与源 radmin/src/app/functions.php:68-75 的 mask_secret 逐字同口径）=====

// maskSecret 长度 ≤ 8 ⇒ 全 '*'；否则 前 4 字符 + (len-8) 个 '*' + 后 4 字符。
// 空串 ⇒ 空串（未配置）。掩码永不回写：编辑回填判据是「空串或含 '*' ⇒ 不修改」。
func maskSecret(secret string) string {
	runes := []rune(secret)
	if len(runes) <= 8 {
		return strings.Repeat("*", len(runes))
	}
	return string(runes[:4]) + strings.Repeat("*", len(runes)-8) + string(runes[len(runes)-4:])
}

// looksMasked 判定「这是回填的掩码而不是新密钥」（空串或含 '*'）。
func looksMasked(value string) bool {
	return strings.TrimSpace(value) == "" || strings.Contains(value, "*")
}

// 注：错误文案的密钥脱敏（redactSecret）已迁到 redact.go——它是驱动、探活、usage 三处共用的
// **唯一出口**，本文件不再保留第二份实现。

// patchAPIKey 取补丁里的明文密钥（没有则空串），供错误文案脱敏用。
func patchAPIKey(patch ChannelPatch) string {
	if patch.APIKey == nil {
		return ""
	}
	return *patch.APIKey
}

// ===== 宿主表格的列表契约：分页 / 排序 / wheres =====

// listRequest 是宿主表格契约的入参子集（internal/handler/base.go:74-86 的同名字段）：
// page/limit/sort/order/wheres + 两个快搜键。**未知顶层 JSON 键按宿主管家口径忽略**
// （Gin 与宿主 handler.Request 都只绑显式字段），因此这里不做 DisallowUnknownFields。
type listRequest struct {
	Page                int                 `json:"page"`
	Limit               int                 `json:"limit"`
	Sort                string              `json:"sort"`
	Order               string              `json:"order"`
	Wheres              []whereGroup        `json:"wheres"`
	Keyword             string              `json:"keyword"`
	QuickSearchKeywords quickSearchKeywords `json:"quickSearchKeywords"`
}

// keyword 兼容两种快搜写法：顶层 keyword（本插件口径）与宿主前端的 quickSearchKeywords
// （字符串或字符串数组，由前端 useTableManager 的 getQuickSearchData 生成 wheres 之外的冗余传参）。
func (r listRequest) keyword() string {
	if keyword := strings.TrimSpace(r.Keyword); keyword != "" {
		return keyword
	}
	for _, item := range r.QuickSearchKeywords {
		if trimmed := strings.TrimSpace(item); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// quickSearchKeywords 兼容字符串与字符串数组两种 JSON 形状。
type quickSearchKeywords []string

// UnmarshalJSON 接受 "kw" 与 ["kw"] / ["kw1","kw2"]。
func (q *quickSearchKeywords) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		*q = nil
		return nil
	}
	if strings.HasPrefix(trimmed, "[") {
		var items []string
		if err := json.Unmarshal(data, &items); err != nil {
			// 数组里混了非字符串：按「快搜不生效」处理，不因此拒掉整个列表请求。
			*q = nil
			return nil
		}
		*q = items
		return nil
	}
	var single string
	if err := json.Unmarshal(data, &single); err != nil {
		*q = nil
		return nil
	}
	*q = []string{single}
	return nil
}

// optionalStatus 顶层二值筛选（渠道/模型列表的 status）。
//
// 容错口径：缺省、null、空串一律表示「不筛选」——前端在清空筛选器时可能发空串，
// 让它把整个请求打成 400 并不是调用方能理解的失败；其余取值必须是 0/1 的整数。
type optionalStatus struct {
	Value *int16
}

// UnmarshalJSON 容忍 ""/null；非法值给可读错误。
func (s *optionalStatus) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "null" || trimmed == `""` {
		s.Value = nil
		return nil
	}
	var value int16
	if err := json.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("status 必须是 0（停用）或 1（启用）")
	}
	s.Value = &value
	return nil
}

// whereGroup / whereCondition 与宿主 internal/service/base.go:19-31 的 JSON 形状一致：
// 组间 AND；组内按 Or 决定 OR（true）/ AND（false）。
type whereGroup struct {
	Wheres []whereCondition `json:"wheres"`
	Or     bool             `json:"or"`
}

type whereCondition struct {
	Field    string `json:"field"`
	Operator string `json:"operator"`
	Value    any    `json:"value"`
}

// whereKind 是 wheres 白名单字段的值类型（决定可用的操作符与值收敛方式）。
type whereKind int

const (
	// whereText 文本列：允许全部 16 项操作符（模糊类只许它）
	whereText whereKind = iota
	// whereInt 整数列：不许模糊类
	whereInt
	// whereBool01 二值列（status/stream）：只许等值、IN、NULL 类
	whereBool01
)

// whereField 白名单项：Column 是**已引用**的列名（代码常量），Kind 决定操作符与值收敛。
type whereField struct {
	Column string
	Kind   whereKind
}

// whereOperators 是宿主的 16 项操作符词表（大小写敏感；大小写不符即词表外，一律拒）。
var whereOperators = map[string]string{
	"eq":          "=",
	"ne":          "!=",
	"gt":          ">",
	"egt":         ">=",
	"lt":          "<",
	"elt":         "<=",
	"LIKE":        "LIKE",
	"NOT LIKE":    "NOT LIKE",
	"ILIKE":       "ILIKE",
	"NOT ILIKE":   "NOT ILIKE",
	"IN":          "IN",
	"NOT IN":      "NOT IN",
	"BETWEEN":     "BETWEEN",
	"NOT BETWEEN": "NOT BETWEEN",
	"NULL":        "IS NULL",
	"NOT NULL":    "IS NOT NULL",
}

// whereOperatorNames 词表名（错误文案与测试用，顺序固定）。
var whereOperatorNames = []string{
	"eq", "ne", "gt", "egt", "lt", "elt",
	"LIKE", "NOT LIKE", "ILIKE", "NOT ILIKE",
	"IN", "NOT IN", "BETWEEN", "NOT BETWEEN",
	"NULL", "NOT NULL",
}

// maxWhereInValues IN/NOT IN 的元素上限（请求路径上的条件不得无界增长）。
const maxWhereInValues = 1000

// validateWhereGroups 逐条校验 wheres（字段白名单 + 操作符词表 + 类型兼容 + 值形状）。
// 空组（无条件）按宿主管家口径忽略。
func validateWhereGroups(fields map[string]whereField, groups []whereGroup) ([]whereGroup, error) {
	kept := make([]whereGroup, 0, len(groups))
	for _, group := range groups {
		if len(group.Wheres) == 0 {
			continue
		}
		for _, where := range group.Wheres {
			if _, _, err := buildWhereCondition(fields, where); err != nil {
				return nil, err
			}
		}
		kept = append(kept, group)
	}
	return kept, nil
}

// applyWhereGroups 把已校验的 wheres 挂到查询上（组间 AND；组内按 Or 决定 OR/AND；值全参数化）。
func applyWhereGroups(q *gorm.DB, fields map[string]whereField, groups []whereGroup) (*gorm.DB, error) {
	for _, group := range groups {
		if len(group.Wheres) == 0 {
			continue
		}
		fragments := make([]string, 0, len(group.Wheres))
		args := make([]any, 0, len(group.Wheres))
		for _, where := range group.Wheres {
			fragment, values, err := buildWhereCondition(fields, where)
			if err != nil {
				return nil, err
			}
			fragments = append(fragments, fragment)
			args = append(args, values...)
		}
		joiner := " AND "
		if group.Or {
			joiner = " OR "
		}
		q = q.Where("("+strings.Join(fragments, joiner)+")", args...)
	}
	return q, nil
}

// buildWhereCondition 把一条条件翻成「已引用列名 + 位置占位符」的 SQL 片段与参数（值全参数化）。
func buildWhereCondition(fields map[string]whereField, where whereCondition) (string, []any, error) {
	name := strings.TrimSpace(where.Field)
	field, ok := fields[name]
	if !ok {
		return "", nil, fmt.Errorf("字段 %q 不在筛选字段白名单内（允许：%s）", where.Field, strings.Join(whereFieldNames(fields), "|"))
	}
	operator := where.Operator

	switch operator {
	case "NULL":
		return field.Column + " IS NULL", nil, nil
	case "NOT NULL":
		return field.Column + " IS NOT NULL", nil, nil
	case "BETWEEN", "NOT BETWEEN":
		if field.Kind == whereBool01 {
			return "", nil, fmt.Errorf("字段 %s 是二值列，不支持 %s", name, operator)
		}
		values, err := splitRangeValues(where.Value)
		if err != nil {
			return "", nil, fmt.Errorf("字段 %s 的 %s 需要两个值: %w", name, operator, err)
		}
		low, err := coerceWhereValue(name, field.Kind, values[0])
		if err != nil {
			return "", nil, fmt.Errorf("字段 %s 的下界: %w", name, err)
		}
		high, err := coerceWhereValue(name, field.Kind, values[1])
		if err != nil {
			return "", nil, fmt.Errorf("字段 %s 的上界: %w", name, err)
		}
		return field.Column + " " + operator + " ? AND ?", []any{low, high}, nil
	case "IN", "NOT IN":
		values, err := splitInValues(where.Value)
		if err != nil {
			return "", nil, fmt.Errorf("字段 %s 的 %s: %w", name, operator, err)
		}
		args := make([]any, 0, len(values))
		for i, raw := range values {
			value, err := coerceWhereValue(name, field.Kind, raw)
			if err != nil {
				return "", nil, fmt.Errorf("字段 %s 的 %s 第 %d 项: %w", name, operator, i+1, err)
			}
			args = append(args, value)
		}
		return field.Column + " " + operator + wherePlaceholders(len(args)), args, nil
	}

	sqlOperator, ok := whereOperators[operator]
	if !ok {
		return "", nil, fmt.Errorf("操作符 %q 不在词表内（支持：%s）", where.Operator, strings.Join(whereOperatorNames, ", "))
	}
	if isSQLite(database.DB()) {
		switch sqlOperator {
		case "ILIKE":
			sqlOperator = "LIKE"
		case "NOT ILIKE":
			sqlOperator = "NOT LIKE"
		}
	}
	if err := checkWhereOperatorKind(name, operator, field.Kind); err != nil {
		return "", nil, err
	}
	value, err := coerceWhereValue(name, field.Kind, where.Value)
	if err != nil {
		return "", nil, err
	}
	if isFuzzyOperator(operator) {
		// 与宿主 CRUD 的模糊搜索同口径：值不含 % 时补成前后通配（值仍参数化）。
		if text, ok := value.(string); ok && !strings.Contains(text, "%") {
			value = "%" + text + "%"
		}
	}
	return field.Column + " " + sqlOperator + " ?", []any{value}, nil
}

// wherePlaceholders 生成 " (?, ?, ...)"（长度只由调用方的元素数决定）。
func wherePlaceholders(count int) string {
	if count <= 0 {
		return " (NULL)"
	}
	return " (" + strings.TrimSuffix(strings.Repeat("?, ", count), ", ") + ")"
}

// checkWhereOperatorKind 拦住「词表内但类型不兼容」的组合（口径同宿主 L2 引擎）：
// 模糊类只许文本列；有序比较类不许二值列。
func checkWhereOperatorKind(name, operator string, kind whereKind) error {
	if isFuzzyOperator(operator) && kind != whereText {
		return fmt.Errorf("字段 %s 不是文本列，不支持 %s", name, operator)
	}
	switch operator {
	case "gt", "egt", "lt", "elt", "BETWEEN", "NOT BETWEEN":
		if kind == whereBool01 {
			return fmt.Errorf("字段 %s 是二值列，不支持 %s", name, operator)
		}
	}
	return nil
}

// isFuzzyOperator 模糊匹配类（LIKE / NOT LIKE / ILIKE / NOT ILIKE）。
func isFuzzyOperator(operator string) bool {
	switch operator {
	case "LIKE", "NOT LIKE", "ILIKE", "NOT ILIKE":
		return true
	default:
		return false
	}
}

// coerceWhereValue 按字段类型收敛值（只做形状与类型对齐，不做业务校验）；值为 null 一律拒。
func coerceWhereValue(name string, kind whereKind, value any) (any, error) {
	if value == nil {
		return nil, fmt.Errorf("值为 null（判定为空请用 NULL / NOT NULL 操作符）")
	}
	switch kind {
	case whereInt:
		number, err := toInt64(value)
		if err != nil {
			return nil, fmt.Errorf("字段 %s 的值 %v 不是整数", name, value)
		}
		return number, nil
	case whereBool01:
		flag, err := toBool01(value)
		if err != nil {
			return nil, fmt.Errorf("字段 %s 的值 %v 不是 0/1", name, value)
		}
		return flag, nil
	default:
		text, err := toText(value)
		if err != nil {
			return nil, fmt.Errorf("字段 %s 的值: %w", name, err)
		}
		return text, nil
	}
}

// toInt64 收敛为整数（JSON 数字、数字字符串、布尔都接受；显式拒绝小数与对象）。
func toInt64(value any) (int64, error) {
	switch v := value.(type) {
	case int:
		return int64(v), nil
	case int64:
		return v, nil
	case float64:
		if v != float64(int64(v)) {
			return 0, fmt.Errorf("不是整数")
		}
		return int64(v), nil
	case json.Number:
		if number, err := v.Int64(); err == nil {
			return number, nil
		}
		asFloat, err := v.Float64()
		if err != nil || asFloat != float64(int64(asFloat)) {
			return 0, fmt.Errorf("不是整数")
		}
		return int64(asFloat), nil
	case bool:
		if v {
			return 1, nil
		}
		return 0, nil
	case string:
		number, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		if err != nil {
			return 0, fmt.Errorf("不是整数")
		}
		return number, nil
	default:
		return 0, fmt.Errorf("不是标量")
	}
}

// toBool01 收敛为 0/1（二值列口径）。
func toBool01(value any) (int64, error) {
	switch v := value.(type) {
	case bool:
		if v {
			return 1, nil
		}
		return 0, nil
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "1", "true":
			return 1, nil
		case "0", "false":
			return 0, nil
		}
		return 0, fmt.Errorf("不是 0/1")
	default:
		number, err := toInt64(value)
		if err != nil {
			return 0, err
		}
		if number != 0 && number != 1 {
			return 0, fmt.Errorf("不是 0/1")
		}
		return number, nil
	}
}

// toText 收敛为字符串（对象/数组一律拒；数值与布尔按字面量转）。
func toText(value any) (string, error) {
	switch v := value.(type) {
	case string:
		return v, nil
	case bool:
		if v {
			return "1", nil
		}
		return "0", nil
	case int:
		return strconv.Itoa(v), nil
	case int64:
		return strconv.FormatInt(v, 10), nil
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64), nil
	case json.Number:
		return v.String(), nil
	case map[string]any, []any:
		return "", fmt.Errorf("值必须是标量（实际是对象或数组）")
	default:
		return fmt.Sprint(v), nil
	}
}

// splitRangeValues BETWEEN 的两个值：接受 "下界,上界" 字符串或长度 2 的数组（宿主两种写法都出现过）。
func splitRangeValues(value any) ([]any, error) {
	switch v := value.(type) {
	case []any:
		if len(v) != 2 {
			return nil, fmt.Errorf("需要 2 个值，实际 %d 个", len(v))
		}
		return v, nil
	case string:
		parts := strings.SplitN(v, ",", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("需要 \"下界,上界\" 形式的字符串，实际 %q", v)
		}
		return []any{strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])}, nil
	default:
		return nil, fmt.Errorf("需要 \"下界,上界\" 字符串或长度 2 的数组")
	}
}

// splitInValues IN/NOT IN 的取值列表：数组或逗号分隔字符串；元素数 1..maxWhereInValues。
func splitInValues(value any) ([]any, error) {
	var values []any
	switch v := value.(type) {
	case []any:
		values = v
	case string:
		for _, part := range strings.Split(v, ",") {
			values = append(values, strings.TrimSpace(part))
		}
	default:
		return nil, fmt.Errorf("需要数组或逗号分隔字符串")
	}
	if len(values) == 0 {
		return nil, fmt.Errorf("取值列表为空")
	}
	if len(values) > maxWhereInValues {
		return nil, fmt.Errorf("取值列表过长（%d 项，上限 %d）", len(values), maxWhereInValues)
	}
	return values, nil
}

// whereFieldNames 白名单字段名（错误文案与测试用，顺序按名排）。
func whereFieldNames(fields map[string]whereField) []string {
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sortStrings(names)
	return names
}

// sortStrings 升序排序（避免引入额外依赖；列表很短）。
func sortStrings(items []string) {
	for i := 1; i < len(items); i++ {
		for j := i; j > 0 && items[j] < items[j-1]; j-- {
			items[j], items[j-1] = items[j-1], items[j]
		}
	}
}

// validateSort 排序字段白名单：白名单外一律拒（静默回落会让调用方以为排序已生效）。
func validateSort(field string, allowed map[string]string, label string) error {
	name := strings.TrimSpace(field)
	if name == "" {
		return nil
	}
	if _, ok := allowed[name]; !ok {
		names := make([]string, 0, len(allowed))
		for key := range allowed {
			names = append(names, key)
		}
		sortStrings(names)
		return fmt.Errorf("%s列表的排序字段 %q 不在白名单内（允许：%s）", label, field, strings.Join(names, "|"))
	}
	return nil
}

// bindJSON 绑定请求体：空体按零值处理（列表页可能发空对象），非法 JSON 给可读错误。
// 未知顶层键一律忽略（宿主管家口径）。
func bindJSON(c *gin.Context, dst any) error {
	if c.Request == nil || c.Request.Body == nil {
		return nil
	}
	decoder := json.NewDecoder(c.Request.Body)
	if err := decoder.Decode(dst); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return fmt.Errorf("请求体不是合法 JSON: %w", err)
	}
	return nil
}

// parsePK 解析路径参数里的主键（:pk 段）。
func parsePK(c *gin.Context, label string) (int64, error) {
	raw := strings.TrimSpace(c.Param("pk"))
	if raw == "" {
		return 0, fmt.Errorf("%s ID 不能为空", label)
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s ID %q 不是正整数", label, raw)
	}
	return value, nil
}

// pksRequest 批量删除入参（对齐宿主 handler.DeleteRequest 的 {pks:[...]} 形状）。
type pksRequest struct {
	Pks []string `json:"pks"`
}

// ids 把 pks 收敛为 int64 主键列表（空列表一律拒；不是正文数字的一律拒）。
func (r pksRequest) ids(label string) ([]int64, error) {
	if len(r.Pks) == 0 {
		return nil, fmt.Errorf("请先选择要删除的%s", label)
	}
	ids := make([]int64, 0, len(r.Pks))
	for _, raw := range r.Pks {
		value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err != nil || value <= 0 {
			return nil, fmt.Errorf("%s ID %q 不是正整数", label, raw)
		}
		ids = append(ids, value)
	}
	return ids, nil
}

// u64IDs 把 int64 主键列表转成 uint64（模型表主键口径）。
func u64IDs(ids []int64) []uint64 {
	out := make([]uint64, 0, len(ids))
	for _, id := range ids {
		out = append(out, uint64(id))
	}
	return out
}
