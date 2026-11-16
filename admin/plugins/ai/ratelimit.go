// plugin: ai v0.2.0
// ratelimit.go 是公开面（/ai/v1/*）的按 Key 限流层：自持令牌桶 + 在飞并发上限 + OpenAI 形状 429。
// 对照源实现 app/middleware/ApiKeyRateLimitMiddleware.php（只读对照，不搬代码）。
//
// 语义：
//   - 桶键 = sha256(出示凭证) 前 16 位 hex（每凭证一桶，与源 'ai:rl:'+substr(key_hash,0,16) 同构，
//     且桶表里不出现凭证本体）；
//   - 取值顺序 = 按 Key 覆盖（apikey_auth.go 命中 DB Key 时写入）→ 插件全局三键 → 不限（≤0）；
//   - 超限回 OpenAI 形状 429（type=rate_limit_error、code=rate_limit_exceeded、Retry-After: 60）；
//     判定在中间件层 Abort ⇒ 业务 handler 不被调用 ⇒ 不写 plugin_ai_usage、不计渠道统计；
//   - 在飞并发上限：达上限**立即** 429（不排队），请求结束必释放（defer）；
//   - 限流配置读失败 fail-open（放行 + 日志，源 :66-69 同口径）：限流件故障不得放大成网关不可用。
//
// 与源的两点差异（如实登记，汇总于 README 差异节）：
//
//	① 源用固定窗口（webman/limiter，60s）且 Redis 多进程共享；本层是**进程内令牌桶**
//	   （容量 burst、按 rpm/60 每秒补桶）⇒ 多副本各限各的（实际限额≈配置×副本数）、重启清零；
//	   DB Key 覆盖层的容量 = 该 Key 的 rate_limit（源的窗口额度），全局层容量见 rate_limit_burst。
//	② 源的全局缺省是 120 次/分/Key；本插件的三键缺省一律 0 = 不限（少一道隐形门，设置页可见）。
//	   相通的语义：429 形状逐字一致、未过鉴权的请求不占配额（源中间件挂在 AiGatewayAuthMiddleware 之后）、
//	   桶按凭证哈希前缀分组、Retry-After 同为 60。
//
// 为什么自持而不复用宿主的 tollbooth（go.mod:18 已是直接依赖）：tollbooth 的键维度只能借
// IPLookup/RemoteAddr 施展（internal/middleware/throttle.go:84-96 先例），本层要的是
// 「按 Key 覆盖 + OpenAI 形状 429 + 在飞并发上限」三件语义，且必须能用假时钟单测。
// 宿主全局 Throttle 仍在插件 handler 之前 Abort（例外清单见 handler_public.go 头注）。
package ai

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ai-go-hub/ai-go-admin/internal/infra/database"
	"github.com/ai-go-hub/ai-go-admin/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// 限流三键的落库名（安装器写 configs：plugin_<名>_<键>，分组 plugin.ai）。
// plugin.yaml 的 configs、settingsView 回显与 settingsSave 白名单由装配面登记（本文件只读）。
const (
	configKeyRateLimitRPM         = "plugin_ai_rate_limit_rpm"
	configKeyRateLimitBurst       = "plugin_ai_rate_limit_burst"
	configKeyRateLimitMaxInflight = "plugin_ai_rate_limit_max_inflight"
)

// rateLimitRetryAfterSeconds 是 429 的 Retry-After 值（与源 Retry-After=60 逐字一致）。
const rateLimitRetryAfterSeconds = 60

// rateLimitConfigCacheTTL 是限流配置的进程内缓存上限：改设置后最迟 5s 生效
// （与插件配置缓存 settingsCacheTTL 同口径；settings/save 若调用 invalidateRateLimitConfig 则 0s 生效）。
const rateLimitConfigCacheTTL = 5 * time.Second

const (
	// rateLimitBucketKeyBytes = sha256 前 8 字节 = 16 位 hex（对齐源 substr(key_hash, 0, 16)）。
	rateLimitBucketKeyBytes = 8
	// rateLimitBucketTTL 是桶的空闲存活时长：超过即被清扫（宿主先例 internal/middleware/throttle.go:41-48
	// 用 expiration_ttl/2 周期清理 expirable-cache；本层不起 goroutine，改为按访问惰性清扫）。
	rateLimitBucketTTL = time.Hour
	// rateLimitSweepInterval 是惰性清扫的最小间隔：避免每个请求都 O(n) 遍历桶表。
	rateLimitSweepInterval = 5 * time.Minute
)

// 上限的三键值域（越界一律 clamp，避免把「手滑填的 1e9」变成隐性资源问题）。
const (
	rateLimitRPMMax         = 1000000
	rateLimitBurstMax       = 1000000
	rateLimitMaxInflightMax = 100000
)

// rateLimitConfigKeys 三键的声明顺序（读与回显共用一份，避免两处漂移）。
var rateLimitConfigKeys = []string{
	configKeyRateLimitRPM,
	configKeyRateLimitBurst,
	configKeyRateLimitMaxInflight,
}

// rateLimitConfig 是「插件全局」三键的生效值（0/负/非法一律 0 = 不限）。
type rateLimitConfig struct {
	RPM         int
	Burst       int
	MaxInflight int
}

// 键面（gin.Context）：全部用私有类型 + 前缀常量，避免与宿主或其它插件撞键。
type rateLimitContextKey string

const (
	ctxKeyRateLimitOverride rateLimitContextKey = "ai.rate_limit_override"
	ctxKeyKeySource         rateLimitContextKey = "ai.key_source"
	ctxKeyKeyScope          rateLimitContextKey = "ai.key_scope"
)

// ===== 按 Key 覆盖（B1 接缝：本文件定义，apikey_auth.go 调用）=====

// rateLimitOverride 是「按 Key 覆盖」（后台签发的 Key 自带 rate_limit，源 AiApiKey.rate_limit）。
// RPM > 0 即生效——**即使插件全局 rpm=0**（对齐源 ApiKeyRateLimitMiddleware.php:48-50 的
// `rate_limit > 0 ? rate_limit : 全局`）；Burst 为 0 时按 RPM 推导（见 rateLimitBurstFor）。
type rateLimitOverride struct {
	RPM   int
	Burst int
}

// setRateLimitOverride 由鉴权层（apikey_auth.go）在命中 DB Key 后调用；RPM ≤ 0 视为「跟随全局」不写覆盖。
func setRateLimitOverride(c *gin.Context, override *rateLimitOverride) {
	if c == nil || override == nil || override.RPM <= 0 {
		return
	}
	value := *override
	if value.Burst < 0 {
		value.Burst = 0
	}
	if value.RPM > rateLimitRPMMax {
		value.RPM = rateLimitRPMMax
	}
	c.Set(string(ctxKeyRateLimitOverride), value)
}

// rateLimitOverrideOf 读本次请求的按 Key 覆盖（没有 ⇒ nil）。
func rateLimitOverrideOf(c *gin.Context) *rateLimitOverride {
	if c == nil {
		return nil
	}
	raw, exists := c.Get(string(ctxKeyRateLimitOverride))
	if !exists {
		return nil
	}
	override, ok := raw.(rateLimitOverride)
	if !ok || override.RPM <= 0 {
		return nil
	}
	return &override
}

// ===== 计量归因 source（B2 接缝：本文件定义，handler_public.go 与 apikey_auth.go 共用）=====

// setKeySource 由鉴权层在命中 DB Key 后写入 'key:<source>'；空串/纯空白视为不设置（保持缺省）。
//
// 口径：source 的列宽是 varchar(30)（store.go 按 30 截断），因此 'key:' + source ≤ 30 ⇒
// source 自身必须 ≤ 24 字符——该长度校验由签发侧（apikey handler）承担，本层只做去空白。
func setKeySource(c *gin.Context, source string) {
	if c == nil {
		return
	}
	source = strings.TrimSpace(source)
	if source == "" {
		return
	}
	c.Set(string(ctxKeyKeySource), source)
}

// keySourceOf 取本次请求的计量归因 source（缺省 publicSource = 'key:gateway'，即全局配置 key 的老行为）。
func keySourceOf(c *gin.Context) string {
	if c == nil {
		return publicSource
	}
	raw, exists := c.Get(string(ctxKeyKeySource))
	if !exists {
		return publicSource
	}
	source, ok := raw.(string)
	if !ok || strings.TrimSpace(source) == "" {
		return publicSource
	}
	return source
}

// ===== 模型白名单（keyScope 接缝：本文件定义，handler_public.go 消费）=====

// setKeyScope 由鉴权层在命中**带 model_scope 的** DB Key 后调用（apikey_auth.go）。
// nil / 空切片 / 全是空白的切片 = 未设置（公开放行语义：不过滤、不拒绝）。
func setKeyScope(c *gin.Context, models []string) {
	if c == nil {
		return
	}
	scope := keyScopeSet(models)
	if len(scope) == 0 {
		return
	}
	c.Set(string(ctxKeyKeyScope), scope)
}

// keyScopeOf 取本次请求的模型白名单集合（接缝签名在此冻结：单值 map，未设置 ⇒ nil/空）。
//
// 为什么是「集合」而不是「列表 + ok」：白名单的两种语义（未设置 / 已设置）由 len 区分即可——
// 「显式空清单」不存在（apikey_auth.go 只在意非空 model_scope 时才写；空 = 允许全部），
// 因此不需要额外的 ok；同时集合正是过滤与匹配要用的结构。
func keyScopeOf(c *gin.Context) map[string]struct{} {
	if c == nil {
		return nil
	}
	raw, exists := c.Get(string(ctxKeyKeyScope))
	if !exists {
		return nil
	}
	scope, ok := raw.(map[string]struct{})
	if !ok || len(scope) == 0 {
		return nil
	}
	return scope
}

// modelInKeyScope 判定模型是否在该 Key 的白名单内（未设置 scope ⇒ 恒 true，v1 现行为不变）。
func modelInKeyScope(c *gin.Context, model string) bool {
	scope := keyScopeOf(c)
	if len(scope) == 0 {
		return true
	}
	_, allowed := scope[strings.TrimSpace(model)]
	return allowed
}

// keyScopeSet 把白名单收敛成集合：去空白、去空项、去重（无有效项 ⇒ nil = 未设置）。
func keyScopeSet(models []string) map[string]struct{} {
	var scope map[string]struct{}
	for _, item := range models {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if scope == nil {
			scope = make(map[string]struct{}, len(models))
		}
		scope[item] = struct{}{}
	}
	return scope
}

// keyScopeModelNotFound 构造「模型不在该 Key 白名单内」的 404。
// 口径（handler_public.go 头注冻结）：OpenAI 形状 404 + code=model_not_found
// （与「模型不存在」同形，不向调用方区分「没有这个模型」与「这个 Key 没权限」）。
func keyScopeModelNotFound(model string) *APIError {
	return &APIError{
		HTTPStatus: http.StatusNotFound,
		Type:       "invalid_request_error",
		Code:       "model_not_found",
		Message:    fmt.Sprintf("模型 [%s] 不存在，或该 Key 无权访问", strings.TrimSpace(model)),
		Class:      ErrorClassLocal,
	}
}

// ===== 三键的读出口（唯一读口 + 5s TTL 进程内缓存）=====

var (
	rateLimitConfigMu       sync.RWMutex
	rateLimitConfigCached   rateLimitConfig
	rateLimitConfigExpireAt time.Time
)

// loadRateLimitConfig 读三键并解析成生效值（命中未过期缓存即返回）。
func loadRateLimitConfig(ctx context.Context) (rateLimitConfig, error) {
	rateLimitConfigMu.RLock()
	if !rateLimitConfigExpireAt.IsZero() && time.Now().Before(rateLimitConfigExpireAt) {
		cached := rateLimitConfigCached
		rateLimitConfigMu.RUnlock()
		return cached, nil
	}
	rateLimitConfigMu.RUnlock()

	values, err := readRateLimitConfigValues(ctx)
	if err != nil {
		return rateLimitConfig{}, err
	}
	cfg := parseRateLimitConfig(values)

	rateLimitConfigMu.Lock()
	rateLimitConfigCached, rateLimitConfigExpireAt = cfg, time.Now().Add(rateLimitConfigCacheTTL)
	rateLimitConfigMu.Unlock()
	return cfg, nil
}

// invalidateRateLimitConfig 立即失效限流配置缓存（settings/save 成功后可选调用：不调用则最迟 5s 生效）。
func invalidateRateLimitConfig() {
	rateLimitConfigMu.Lock()
	rateLimitConfigExpireAt = time.Time{}
	rateLimitConfigMu.Unlock()
}

// readRateLimitConfigValues 读三个限流键的现值（只读本插件的键）。
func readRateLimitConfigValues(ctx context.Context) (map[string]string, error) {
	db := database.DB()
	if db == nil {
		return nil, fmt.Errorf("%w（限流配置读取须在 database.Init() 之后）", ErrStoreUnavailable)
	}
	rows, err := gorm.G[model.Config](db).Where("name IN ?", rateLimitConfigKeys).Find(ctx)
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

// parseRateLimitConfig 把 configs 现值解析成生效值（纯函数，可单测）：0/负/空/非法一律 0 = 不限。
func parseRateLimitConfig(values map[string]string) rateLimitConfig {
	return rateLimitConfig{
		RPM:         boundedInt(values[configKeyRateLimitRPM], rateLimitRPMMax),
		Burst:       boundedInt(values[configKeyRateLimitBurst], rateLimitBurstMax),
		MaxInflight: boundedInt(values[configKeyRateLimitMaxInflight], rateLimitMaxInflightMax),
	}
}

// boundedInt 解析非负整数并 clamp 上限（负数/空/非法 ⇒ 0）。
func boundedInt(raw string, high int) int {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value <= 0 {
		return 0
	}
	return clampInt(value, 0, high)
}

// ===== 限流取值收敛（覆盖 → 全局 → 不限）=====

// rateLimitLimits 是一次请求最终生效的上限（三者已收敛完）。
type rateLimitLimits struct {
	rpm         int
	burst       int
	maxInflight int
}

// resolveRateLimitLimits 按「按 Key 覆盖 → 插件全局 → 不限」解析本次请求的上限。
// 覆盖的 RPM > 0 即整体接管 rpm/burst（即使全局 rpm=0）；在飞上限没有 Key 级覆盖，恒取全局。
func resolveRateLimitLimits(cfg rateLimitConfig, override *rateLimitOverride) rateLimitLimits {
	limits := rateLimitLimits{maxInflight: cfg.MaxInflight}
	rpm, burst := cfg.RPM, cfg.Burst
	if override != nil && override.RPM > 0 {
		rpm, burst = override.RPM, override.Burst
		// 按 Key 层单独规则（刻意**不走** rateLimitBurstFor 的下限）：源对 DB Key 用的是固定窗口
		// limit=rate_limit（ApiKeyRateLimitMiddleware.php:47-56 的 Limiter::check($bucket,$limit,60)），
		// 没有 max(10, …) 这条下限 ⇒ 桶容量取该 Key 自己的 rate_limit，首窗最多放行 rate_limit 次。
		// 沿用下限会让 rate_limit<600 的 Key 首窗瞬间放行 10 次（rate_limit=2 时打到 5 倍限额），
		// 与源、与本插件自己登记的验收口径（「rate_limit=2 ⇒ 第 3 次 429」，handler_apikey.go:23）都不符。
		if burst <= 0 {
			burst = rpm
		}
	}
	if rpm <= 0 {
		return limits // 不限：一次令牌都不取
	}
	limits.rpm = rpm
	limits.burst = rateLimitBurstFor(rpm, burst)
	return limits
}

// rateLimitBurstFor 推导桶容量：显式 burst > 0 用它，否则按源**渠道级准入**口径 max(10, ceil(rpm/60))
// （源 ChannelGate::burstOf 同式：每分钟 N 次 ⇒ 桶容量 N/60 向上取整，至少 10）。源的**按 Key 限流**
// 没有这条下限（固定窗口，见 resolveRateLimitLimits 的覆盖分支）——本函数只服务全局层与显式 burst。
func rateLimitBurstFor(rpm, burst int) int {
	if burst > 0 {
		return burst
	}
	return max(10, int(math.Ceil(float64(rpm)/60)))
}

// ===== 令牌桶 + 在飞计数 =====

// rateLimitBucket 是单个凭证的桶状态（tokens 用 float64 承载补桶的小数部分）。
type rateLimitBucket struct {
	tokens   float64
	lastSeen time.Time
}

// rateLimitLayer 是进程内的桶表 + 在飞计数。
//
// 单实例语义（如实登记）：桶与计数都在本进程内存里 ⇒ 多副本各限各的、重启清零；
// 跨进程强一致需要 Redis 客户端，而宿主 go.mod 无该依赖（不引入额外外部依赖，已登记为范围外）。
type rateLimitLayer struct {
	mu        sync.Mutex
	buckets   map[string]*rateLimitBucket
	lastSweep time.Time
	inflight  int
	// now / load 是测试注入点（假时钟与假配置）；生产值是 time.Now 与 loadRateLimitConfig。
	now  func() time.Time
	load func(ctx context.Context) (rateLimitConfig, error)
}

// newRateLimitLayer 构造一个空的桶层（默认时钟与默认配置读出口）。
func newRateLimitLayer() *rateLimitLayer {
	return &rateLimitLayer{
		buckets: make(map[string]*rateLimitBucket),
		now:     time.Now,
		load:    loadRateLimitConfig,
	}
}

// publicRateLimitLayer 是生产单例（桶表跨请求共享；测试可整体替换它注入假时钟/假配置）。
var publicRateLimitLayer = newRateLimitLayer()

// rateLimitBucketKey 桶键 = sha256(出示凭证) 前 16 位 hex（对齐源 'ai:rl:'+substr(key_hash,0,16)）。
func rateLimitBucketKey(credential string) string {
	sum := sha256.Sum256([]byte(credential))
	return hex.EncodeToString(sum[:rateLimitBucketKeyBytes])
}

// allow 取一枚令牌（now 由入参给出，便于假时钟单测）：桶满容量起步、按 rpm/60 每秒补桶、容量封顶 burst。
func (l *rateLimitLayer) allow(key string, rpm, burst int, now time.Time) bool {
	if rpm <= 0 {
		return true // 0 = 不限（防御性：调用方已收敛，这里再兜一层）
	}
	if burst <= 0 {
		burst = rateLimitBurstFor(rpm, burst)
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	l.sweepLocked(now)

	bucket, exists := l.buckets[key]
	if !exists {
		bucket = &rateLimitBucket{tokens: float64(burst), lastSeen: now}
		l.buckets[key] = bucket
	} else if elapsed := now.Sub(bucket.lastSeen); elapsed > 0 {
		refill := elapsed.Seconds() * float64(rpm) / 60
		bucket.tokens = math.Min(float64(burst), bucket.tokens+refill)
	}
	bucket.lastSeen = now
	if bucket.tokens < 1 {
		return false
	}
	bucket.tokens--
	return true
}

// sweepLocked 惰性清扫入口：距上次清扫不足 rateLimitSweepInterval 就直接返回（避免每请求 O(n)）。
func (l *rateLimitLayer) sweepLocked(now time.Time) {
	if !l.lastSweep.IsZero() && now.Sub(l.lastSweep) < rateLimitSweepInterval {
		return
	}
	l.lastSweep = now
	l.sweepExpiredLocked(now)
}

// sweepExpiredLocked 删除空闲超过 rateLimitBucketTTL 的桶（宿主 throttle.go 的
// DeleteExpiredTokenBuckets 同口径：过期桶滞留内存不会放行错，但会让桶表无限增长）。
func (l *rateLimitLayer) sweepExpiredLocked(now time.Time) {
	for key, bucket := range l.buckets {
		if now.Sub(bucket.lastSeen) >= rateLimitBucketTTL {
			delete(l.buckets, key)
		}
	}
}

// sweepNow 强制立即清扫（测试与自检用；现网走 sweepLocked 的惰性路径）。
func (l *rateLimitLayer) sweepNow(now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweepExpiredLocked(now)
}

// bucketCount 桶表当前大小（测试与自检用）。
func (l *rateLimitLayer) bucketCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets)
}

// acquireInflight 取一个在飞名额：达上限**立即**拒绝（不排队）；成功返回释放函数（调用方 defer）。
// limit ≤ 0 = 不限（返回空释放函数，保证调用方无需分支）。
func (l *rateLimitLayer) acquireInflight(limit int) (func(), bool) {
	if limit <= 0 {
		return func() {}, true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.inflight >= limit {
		return nil, false
	}
	l.inflight++
	return l.releaseInflight, true
}

// releaseInflight 释放在飞名额（幂等安全：不为负）。
func (l *rateLimitLayer) releaseInflight() {
	l.mu.Lock()
	if l.inflight > 0 {
		l.inflight--
	}
	l.mu.Unlock()
}

// inflightCount 当前在飞数（测试与自检用）。
func (l *rateLimitLayer) inflightCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.inflight
}

// ===== 429 回执 =====

// writeRateLimitExceeded 写 OpenAI 形状的 429：Retry-After: 60 + type=rate_limit_error +
// code=rate_limit_exceeded（与源 ApiKeyRateLimitMiddleware.php:56-64 逐字同口径；源多一个 param:null，
// 本插件沿用既有 writePublicAPIError 的三字段形状，type/code 逐字一致）。
func writeRateLimitExceeded(c *gin.Context, message string) {
	c.Header("Retry-After", strconv.Itoa(rateLimitRetryAfterSeconds))
	writePublicAPIError(c, &APIError{
		HTTPStatus: http.StatusTooManyRequests,
		Type:       "rate_limit_error",
		Code:       "rate_limit_exceeded",
		Message:    message,
		Class:      ErrorClassLocal,
	})
}

// ===== 中间件 =====

// rateLimit 是公开面按 Key 限流中间件。挂链（装配面落地）：
//
//	d.apiKeyAuth()（负责写 source/覆盖/白名单）→ d.rateLimit() → route.Handler
//
// 次序与口径：
//
//	① 配置读失败 ⇒ fail-open 放行 + 日志（源 :66-69 同口径）；
//	② 未出示凭证 ⇒ 直接放行，不占配额——鉴权层在限流层之前，会回 401/503（与源一致：
//	   源中间件挂在 AiGatewayAuthMiddleware 之后，只有过了鉴权的请求才计数；因此「错 Key 爆破」
//	   只受宿主 IP 级 Throttle 约束，按凭证防爆破属独立项，已登记为范围外）；
//	③ 在飞上限先取名额（超限立即 429，不排队），再取令牌（取不到则释放名额后 429）；
//	④ 放行时 defer 释放名额——请求结束（含业务 handler 内部任一出口）必释放。
//
// 429 在业务 handler 之前 Abort ⇒ 不写 plugin_ai_usage、不计渠道统计（判据：handler 未被调用）。
func (d deps) rateLimit() gin.HandlerFunc {
	return func(c *gin.Context) {
		layer := publicRateLimitLayer
		cfg, err := layer.load(c.Request.Context())
		if err != nil {
			log.Printf("ai: 读取限流配置失败，本次放行（fail-open）: %v", err)
			c.Next()
			return
		}
		limits := resolveRateLimitLimits(cfg, rateLimitOverrideOf(c))
		if limits.rpm <= 0 && limits.maxInflight <= 0 {
			c.Next()
			return
		}
		credential, ok := bearerToken(c.GetHeader("Authorization"))
		if !ok {
			c.Next()
			return
		}
		release, ok := layer.acquireInflight(limits.maxInflight)
		if !ok {
			writeRateLimitExceeded(c, fmt.Sprintf("网关同时在飞请求已达上限（%d），请稍后重试", limits.maxInflight))
			return
		}
		defer release()
		if limits.rpm > 0 && !layer.allow(rateLimitBucketKey(credential), limits.rpm, limits.burst, layer.now()) {
			writeRateLimitExceeded(c, fmt.Sprintf("请求过于频繁（该 Key 每分钟上限 %d 次）", limits.rpm))
			return
		}
		c.Next()
	}
}
