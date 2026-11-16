// plugin: ai v0.2.0
// ratelimit_test.go 覆盖（公开面按 Key 限流）的可判定面。除末尾一个 DSN 门控的真库用例外，
// 全部无库、无网、不 sleep 等真实时间（时间由假时钟推着走）⇒ 判定确定、不 flake。
//
// 覆盖清单（逐条对应）：
//   - 令牌桶：burst 次全放行 / 第 burst+1 次拒 / 时钟推进恢复 / 容量封顶 / 按 Key 互不影响 /
//     惰性清扫（桶表不随历史凭证增长）/ 32 并发恰放行 burst 次；
//   - 中间件：429 形状逐字（type=rate_limit_error、code=rate_limit_exceeded、Retry-After: 60）/
//     rate_limit_rpm=0 且无覆盖时恒放行 / 未过鉴权的请求不占配额 / 按 Key 覆盖（B1）/
//     在飞并发上限（第 N+1 个立即 429、释放后放行）/ 配置读失败 fail-open；
//   - 「限流不进计费」：429 在业务 handler 之前 Abort ⇒ 假仓储零新增 usage、渠道统计不增；
//   - 接缝：keySourceOf（B2）与 keyScopeOf（白名单）的缺省/覆盖，以及它们对公开面回执与记账的实际效果。
//
// 真库用例（TestPublicModelsKeyScopeDB）复用 store_db_test.go 的临时库脚手架（PLUGIN_AI_TEST_DSN 门控，
// 未设 DSN 即 t.Skip）；也就是说它依赖该文件里的 pluginAITestDB 仍在（该文件重构时请保留此函数名）。
package ai

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// ===== 脚手架（本文件符号一律 rl 前缀：与同包其它测试文件不重名）=====

// rlBaseTime 是假时钟的起点（固定值 ⇒ 用例可复现）。
var rlBaseTime = time.Date(2026, 10, 24, 8, 0, 0, 0, time.UTC)

// rlClock 是可控时钟：测试把时间推着走，而不是 sleep 等真实时间。
type rlClock struct {
	mu sync.Mutex
	at time.Time
}

func newRLClock(at time.Time) *rlClock { return &rlClock{at: at} }

func (c *rlClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *rlClock) advance(d time.Duration) {
	c.mu.Lock()
	c.at = c.at.Add(d)
	c.mu.Unlock()
}

// rlLayer 造一个测试层（假时钟 + 固定配置），并把它装成生产单例（收尾自动还原）。
func rlUseLayer(t *testing.T, cfg rateLimitConfig) (*rateLimitLayer, *rlClock) {
	t.Helper()
	clock := newRLClock(rlBaseTime)
	layer := newRateLimitLayer()
	layer.now = clock.now
	layer.load = func(context.Context) (rateLimitConfig, error) { return cfg, nil }
	previous := publicRateLimitLayer
	publicRateLimitLayer = layer
	t.Cleanup(func() { publicRateLimitLayer = previous })
	return layer, clock
}

// rlCounter 记录「业务 handler 到底跑了几次」——限流类判据全部落在它身上
// （没跑 handler ⇒ 不写 usage、不计渠道统计）。
type rlCounter struct {
	mu   sync.Mutex
	hits int
}

func (c *rlCounter) handler(status int) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		c.mu.Lock()
		c.hits++
		c.mu.Unlock()
		ctx.Status(status)
	}
}

func (c *rlCounter) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hits
}

// rlEngine 造一个单路由测试引擎。
func rlEngine(method, path string, chain ...gin.HandlerFunc) *gin.Engine {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Handle(method, path, chain...)
	return engine
}

// rlDo 发一次请求（authorization 为完整头值，空串表示不带该头）。
func rlDo(engine *gin.Engine, method, path, body, authorization string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)
	return recorder
}

// rlErrorBody 是公开面的 OpenAI 形状错误体。
type rlErrorBody struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	} `json:"error"`
}

func rlDecodeError(t *testing.T, recorder *httptest.ResponseRecorder) rlErrorBody {
	t.Helper()
	var body rlErrorBody
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("错误体不是 JSON: %v（原文 %s）", err, recorder.Body.String())
	}
	return body
}

// rlWaitFor 轮询等待条件成立（上限 2s）——不猜 sleep 时长。
func rlWaitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("等待「%s」超时（2s）", what)
}

// rlTestContext 造一个独立的 gin 上下文（接缝用例用）。
func rlTestContext() *gin.Context {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/ai/v1/chat/completions", nil)
	return ctx
}

// rlConfigLayer 造一个「配置读固定失败」的测试层（fail-open 用例用）。
func rlConfigErrorLayer(t *testing.T, err error) *rateLimitLayer {
	t.Helper()
	layer := newRateLimitLayer()
	layer.now = time.Now
	layer.load = func(context.Context) (rateLimitConfig, error) { return rateLimitConfig{}, err }
	previous := publicRateLimitLayer
	publicRateLimitLayer = layer
	t.Cleanup(func() { publicRateLimitLayer = previous })
	return layer
}

// ===== 桶键与参数解析（纯函数）=====

func TestRateLimitBucketKeyIsSHA256Prefix16(t *testing.T) {
	credential := "sk-gateway-key"
	key := rateLimitBucketKey(credential)
	sum := sha256.Sum256([]byte(credential))
	if want := hex.EncodeToString(sum[:8]); key != want {
		t.Fatalf("桶键 = %q，期望 sha256 前 16 位 hex = %q", key, want)
	}
	if len(key) != 16 {
		t.Fatalf("桶键长度 = %d，期望 16", len(key))
	}
	if strings.Contains(key, credential) {
		t.Fatalf("桶键不得包含凭证本体: %q", key)
	}
	if rateLimitBucketKey("sk-other") == key {
		t.Fatalf("不同凭证必须落在不同桶")
	}
}

func TestRateLimitBurstForFollowsSourceFormula(t *testing.T) {
	cases := []struct {
		name       string
		rpm, burst int
		want       int
	}{
		{"rpm=1 取下限 10", 1, 0, 10},
		{"rpm=10 取下限 10", 10, 0, 10},
		{"rpm=60 向上取整后仍取下限 10", 60, 0, 10},
		{"rpm=600 取下限 10", 600, 0, 10},
		{"rpm=1200 ⇒ ceil(20)", 1200, 0, 20},
		{"rpm=6000 ⇒ ceil(100)", 6000, 0, 100},
		{"rpm=601 ⇒ ceil(11)", 601, 0, 11},
		{"显式 burst 优先", 1200, 5, 5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := rateLimitBurstFor(tc.rpm, tc.burst); got != tc.want {
				t.Fatalf("burst = %d，期望 %d", got, tc.want)
			}
		})
	}
}

func TestParseRateLimitConfigDefaultsBoundsAndClamps(t *testing.T) {
	zero := parseRateLimitConfig(nil)
	if zero.RPM != 0 || zero.Burst != 0 || zero.MaxInflight != 0 {
		t.Fatalf("三个键缺省必须为 0=不限，实际 %+v", zero)
	}
	bad := parseRateLimitConfig(map[string]string{
		configKeyRateLimitRPM:         "-5",
		configKeyRateLimitBurst:       "",
		configKeyRateLimitMaxInflight: "abc",
	})
	if bad.RPM != 0 || bad.Burst != 0 || bad.MaxInflight != 0 {
		t.Fatalf("负/空/非法一律 0，实际 %+v", bad)
	}
	ok := parseRateLimitConfig(map[string]string{
		configKeyRateLimitRPM:         "60",
		configKeyRateLimitBurst:       "5",
		configKeyRateLimitMaxInflight: "3",
	})
	if ok.RPM != 60 || ok.Burst != 5 || ok.MaxInflight != 3 {
		t.Fatalf("正常取值解析错: %+v", ok)
	}
	huge := parseRateLimitConfig(map[string]string{
		configKeyRateLimitRPM:         "999999999",
		configKeyRateLimitBurst:       "999999999",
		configKeyRateLimitMaxInflight: "999999999",
	})
	if huge.RPM != rateLimitRPMMax || huge.Burst != rateLimitBurstMax || huge.MaxInflight != rateLimitMaxInflightMax {
		t.Fatalf("越界值必须 clamp: %+v", huge)
	}
	spaced := parseRateLimitConfig(map[string]string{configKeyRateLimitRPM: " 120 "})
	if spaced.RPM != 120 {
		t.Fatalf("带空白的值应被裁剪: %+v", spaced)
	}
}

func TestResolveRateLimitLimitsOrder(t *testing.T) {
	// 全局 0 + 按 Key 覆盖 100 ⇒ 覆盖生效（对齐源 ApiKeyRateLimitMiddleware.php:48-50）；覆盖层的
	// 桶容量 = 该 Key 自己的 rate_limit（源 = 60s 固定窗口、limit=rate_limit，**没有** max(10,…) 这条
	// 渠道级下限）。早期实现把渠道级下限套到按 Key 层，rate_limit=2 的 Key 首窗可放行 10 次。
	override := resolveRateLimitLimits(rateLimitConfig{}, &rateLimitOverride{RPM: 100})
	if override.rpm != 100 || override.burst != 100 {
		t.Fatalf("覆盖在全局为 0 时也必须生效、容量取 Key 自身 limit: %+v", override)
	}
	// 小限额同样取 Key 自身值（早期实现在这里会是 10）。
	small := resolveRateLimitLimits(rateLimitConfig{RPM: 10}, &rateLimitOverride{RPM: 2})
	if small.rpm != 2 || small.burst != 2 {
		t.Fatalf("rate_limit=2 的 Key 容量应为 2（源固定窗口口径）: %+v", small)
	}
	// 全局有值、无覆盖 ⇒ 用全局；burst 显式给了就用显式值。
	global := resolveRateLimitLimits(rateLimitConfig{RPM: 10, Burst: 5}, nil)
	if global.rpm != 10 || global.burst != 5 {
		t.Fatalf("无覆盖应用全局: %+v", global)
	}
	// 全局 0、无覆盖 ⇒ 不限。
	unlimited := resolveRateLimitLimits(rateLimitConfig{}, nil)
	if unlimited.rpm != 0 {
		t.Fatalf("全局 0 + 无覆盖必须不限: %+v", unlimited)
	}
	// 覆盖可自带显式 burst。
	withBurst := resolveRateLimitLimits(rateLimitConfig{RPM: 10}, &rateLimitOverride{RPM: 100, Burst: 3})
	if withBurst.burst != 3 {
		t.Fatalf("覆盖的显式 burst 应生效: %+v", withBurst)
	}
	// 在飞上限没有 Key 级覆盖：恒取全局。
	inflight := resolveRateLimitLimits(rateLimitConfig{MaxInflight: 4}, &rateLimitOverride{RPM: 100})
	if inflight.maxInflight != 4 {
		t.Fatalf("在飞上限应恒取全局: %+v", inflight)
	}
}

// ===== 令牌桶 =====

func TestRateLimitTokenBucketBurstRefillAndCap(t *testing.T) {
	layer := newRateLimitLayer()
	now := rlBaseTime
	const rpm, burst = 60, 10 // 每秒补 1 枚
	key := rateLimitBucketKey("sk-a")

	for i := 1; i <= burst; i++ {
		if !layer.allow(key, rpm, burst, now) {
			t.Fatalf("第 %d 次（≤ burst=%d）必须放行", i, burst)
		}
	}
	if layer.allow(key, rpm, burst, now) {
		t.Fatalf("第 %d 次（burst+1）必须被拒", burst+1)
	}

	// 时钟推进 1s ⇒ 补 1 枚：放行一次，再取又空。
	now = now.Add(time.Second)
	if !layer.allow(key, rpm, burst, now) {
		t.Fatalf("推进 1s 后应补回 1 枚")
	}
	if layer.allow(key, rpm, burst, now) {
		t.Fatalf("只补回 1 枚，第二次应被拒")
	}

	// 不到一枚的补桶（0.5s ⇒ 0.5 枚）不够放行，再 0.5s 才够。
	now = now.Add(500 * time.Millisecond)
	if layer.allow(key, rpm, burst, now) {
		t.Fatalf("0.5 枚不足以放行")
	}
	now = now.Add(500 * time.Millisecond)
	if !layer.allow(key, rpm, burst, now) {
		t.Fatalf("1.0 枚应放行")
	}

	// 容量封顶：长时间空闲（小于桶 TTL，避免被清扫重建）后最多补到 burst。
	now = now.Add(30 * time.Minute)
	granted := 0
	for i := 0; i < burst+5; i++ {
		if layer.allow(key, rpm, burst, now) {
			granted++
		}
	}
	if granted != burst {
		t.Fatalf("长空闲后恰应补满 burst=%d 枚，实际 %d", burst, granted)
	}
}

func TestRateLimitBucketsAreIsolatedPerKey(t *testing.T) {
	layer := newRateLimitLayer()
	now := rlBaseTime
	const rpm, burst = 60, 3
	first, second := rateLimitBucketKey("sk-a"), rateLimitBucketKey("sk-b")

	for i := 0; i < burst; i++ {
		if !layer.allow(first, rpm, burst, now) {
			t.Fatalf("凭证 A 第 %d 次应放行", i+1)
		}
	}
	if layer.allow(first, rpm, burst, now) {
		t.Fatalf("凭证 A 用完应被拒")
	}
	for i := 0; i < burst; i++ {
		if !layer.allow(second, rpm, burst, now) {
			t.Fatalf("凭证 B 不受 A 影响（第 %d 次）", i+1)
		}
	}
	if layer.bucketCount() != 2 {
		t.Fatalf("两个凭证应各占一桶，实际 %d", layer.bucketCount())
	}
}

func TestRateLimitSweepPurgesIdleBucketsAndStopsTableGrowth(t *testing.T) {
	layer := newRateLimitLayer()
	now := rlBaseTime
	const rpm, burst = 60, 10

	// 100 个「一次性凭证」各取一枚 ⇒ 桶表 100 条。
	for i := 0; i < 100; i++ {
		if !layer.allow(fmt.Sprintf("cred-%d", i), rpm, burst, now) {
			t.Fatalf("新凭证首枚必须放行（%d）", i)
		}
	}
	if layer.bucketCount() != 100 {
		t.Fatalf("桶表应含 100 条，实际 %d", layer.bucketCount())
	}

	// 空闲超过桶 TTL + 清扫间隔后再来一个请求 ⇒ 惰性清扫把旧桶全部回收，
	// 桶表回到「当前活跃」的大小（不随历史凭证增长）。
	now = now.Add(rateLimitBucketTTL + rateLimitSweepInterval)
	if !layer.allow("cred-fresh", rpm, burst, now) {
		t.Fatalf("新凭证首枚必须放行")
	}
	if layer.bucketCount() != 1 {
		t.Fatalf("清扫后桶表应只剩当前活跃的 1 条，实际 %d", layer.bucketCount())
	}

	// 同一凭证再取 ⇒ 不新建桶，表仍为 1；显式清扫同样幂等。
	layer.allow("cred-fresh", rpm, burst, now)
	layer.sweepNow(now)
	if layer.bucketCount() != 1 {
		t.Fatalf("重复取用不得增长桶表，实际 %d", layer.bucketCount())
	}

	// TTL 之内的空闲桶必须保活（否则等于给足量新令牌）。
	now = now.Add(rateLimitBucketTTL - time.Second)
	layer.sweepNow(now)
	if layer.bucketCount() != 1 {
		t.Fatalf("未到 TTL 的桶不得被清扫，实际 %d", layer.bucketCount())
	}
}

func TestRateLimitConcurrentAllowNeverOversells(t *testing.T) {
	layer := newRateLimitLayer()
	now := rlBaseTime // 固定时刻 ⇒ 期间不补桶，判定确定
	const rpm, burst = 60, 8
	key := rateLimitBucketKey("sk-concurrent")

	var granted atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if layer.allow(key, rpm, burst, now) {
				granted.Add(1)
			}
		}()
	}
	wg.Wait()

	if got := granted.Load(); got != burst {
		t.Fatalf("32 并发恰应放行 burst=%d 次，实际 %d", burst, got)
	}
	if layer.bucketCount() != 1 {
		t.Fatalf("同一凭证只应有 1 个桶，实际 %d", layer.bucketCount())
	}
}

// ===== 中间件：429 形状与取值顺序 =====

func TestRateLimitMiddlewareBurstThen429Shape(t *testing.T) {
	_, clock := rlUseLayer(t, rateLimitConfig{RPM: 60, Burst: 3})
	counter := &rlCounter{}
	d := deps{}
	engine := rlEngine(http.MethodPost, "/rl", d.rateLimit(), counter.handler(http.StatusOK))
	auth := "Bearer sk-gateway-key"

	for i := 1; i <= 3; i++ {
		if recorder := rlDo(engine, http.MethodPost, "/rl", "", auth); recorder.Code != http.StatusOK {
			t.Fatalf("第 %d 次（≤ burst）应为 200，实际 %d（body=%s）", i, recorder.Code, recorder.Body.String())
		}
	}

	recorder := rlDo(engine, http.MethodPost, "/rl", "", auth)
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("第 4 次应为 429，实际 %d（body=%s）", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Retry-After"); got != "60" {
		t.Fatalf("Retry-After = %q，期望 60", got)
	}
	body := rlDecodeError(t, recorder)
	if body.Error.Type != "rate_limit_error" || body.Error.Code != "rate_limit_exceeded" {
		t.Fatalf("429 形状必须逐字命中：type=%q code=%q", body.Error.Type, body.Error.Code)
	}
	if want := "请求过于频繁（该 Key 每分钟上限 60 次）"; body.Error.Message != want {
		t.Fatalf("message = %q，期望 %q", body.Error.Message, want)
	}
	if counter.count() != 3 {
		t.Fatalf("429 不得进业务 handler（应恰 3 次），实际 %d", counter.count())
	}

	// 窗口推进 1s（rpm=60 ⇒ 1 枚）⇒ 恢复放行。
	clock.advance(time.Second)
	if recorder := rlDo(engine, http.MethodPost, "/rl", "", auth); recorder.Code != http.StatusOK {
		t.Fatalf("补桶后应恢复放行，实际 %d", recorder.Code)
	}
	if counter.count() != 4 {
		t.Fatalf("恢复后 handler 应被调用第 4 次，实际 %d", counter.count())
	}
}

func TestRateLimitMiddlewareRPMZeroAlwaysPasses(t *testing.T) {
	rlUseLayer(t, rateLimitConfig{}) // 三键皆 0 = 不限，且无按 Key 覆盖
	counter := &rlCounter{}
	d := deps{}
	engine := rlEngine(http.MethodPost, "/rl", d.rateLimit(), counter.handler(http.StatusOK))

	for i := 0; i < 64; i++ {
		if recorder := rlDo(engine, http.MethodPost, "/rl", "", "Bearer sk-gateway-key"); recorder.Code != http.StatusOK {
			t.Fatalf("不限流时第 %d 次应放行，实际 %d", i+1, recorder.Code)
		}
	}
	if counter.count() != 64 {
		t.Fatalf("handler 应被调用 64 次，实际 %d", counter.count())
	}
}

func TestRateLimitMiddlewareUnmeteredRequestsDoNotSpendQuota(t *testing.T) {
	// 「未通过鉴权的请求不占限流配额」（与源一致）：链上鉴权在限流之前 ⇒ 错 Key 连发不得吃掉配额。
	rlUseLayer(t, rateLimitConfig{RPM: 60, Burst: 1})
	set := parsePluginSettings(map[string]string{configKeyAPIKey: "sk-good-key"})
	d := deps{
		loadSettings: func(context.Context) (pluginSettings, error) { return set, nil },
		loadStore:    func(context.Context) (*Store, error) { return nil, errors.New("测试环境无数据库") },
		loadService: func(context.Context, pluginSettings) (*Service, error) {
			return nil, errors.New("测试环境无数据库")
		},
	}
	counter := &rlCounter{}

	// ① 生产链形状（鉴权在限流之前）：无名 / 错 Key 一律 401，且都不吃配额。
	engine := rlEngine(http.MethodPost, "/rl", d.bearerAuth(), d.rateLimit(), counter.handler(http.StatusOK))
	for i := 0; i < 5; i++ {
		if recorder := rlDo(engine, http.MethodPost, "/rl", "", ""); recorder.Code != http.StatusUnauthorized {
			t.Fatalf("无名请求应被鉴权层 401（不占配额），实际 %d", recorder.Code)
		}
	}
	for i := 0; i < 5; i++ {
		if recorder := rlDo(engine, http.MethodPost, "/rl", "", "Bearer sk-wrong-key"); recorder.Code != http.StatusUnauthorized {
			t.Fatalf("错 Key 应 401，实际 %d", recorder.Code)
		}
	}
	// ② 正确 Key：配额仍是满的 ⇒ 第 1 次放行，第 2 次才 429（burst=1）。
	if recorder := rlDo(engine, http.MethodPost, "/rl", "", "Bearer sk-good-key"); recorder.Code != http.StatusOK {
		t.Fatalf("配额未被前面的无名/错 Key 请求吃掉，应为 200，实际 %d", recorder.Code)
	}
	recorder := rlDo(engine, http.MethodPost, "/rl", "", "Bearer sk-good-key")
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("burst=1 的第 2 次应为 429，实际 %d", recorder.Code)
	}

	// ③ 无名/错 Key 的请求不得进业务 handler：全程只有那次有效请求跑到了 handler。
	if counter.count() != 1 {
		t.Fatalf("handler 调用次数（仅 1 次有效请求进入业务）= 1，实际 %d", counter.count())
	}
}

// TestRateLimitMiddlewareSkipsRequestsWithoutCredential 覆盖限流层单独挂载（鉴权在别处）时的
// 防御性分支：凭证解析不出来 ⇒ 放行且**不占配额**（口径与源一致：源中间件挂在鉴权之后）。
func TestRateLimitMiddlewareSkipsRequestsWithoutCredential(t *testing.T) {
	rlUseLayer(t, rateLimitConfig{RPM: 60, Burst: 1})
	counter := &rlCounter{}
	d := deps{}
	engine := rlEngine(http.MethodPost, "/rl", d.rateLimit(), counter.handler(http.StatusOK))

	for i := 0; i < 5; i++ {
		if recorder := rlDo(engine, http.MethodPost, "/rl", "", ""); recorder.Code != http.StatusOK {
			t.Fatalf("无限流凭证时直接放行（由鉴权层拒绝），实际 %d", recorder.Code)
		}
	}
	if recorder := rlDo(engine, http.MethodPost, "/rl", "", "Bearer sk-good-key"); recorder.Code != http.StatusOK {
		t.Fatalf("前面 5 次无名请求不得吃掉该凭证的配额，实际 %d", recorder.Code)
	}
	if recorder := rlDo(engine, http.MethodPost, "/rl", "", "Bearer sk-good-key"); recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("burst=1 的第 2 次应为 429，实际 %d", recorder.Code)
	}
	if counter.count() != 6 {
		t.Fatalf("handler 调用次数（5 无名 + 1 有效）= 6，实际 %d", counter.count())
	}
}

func TestRateLimitOverrideBeatsGlobalAndOnlyForItsRequest(t *testing.T) {
	// 全局 rpm=10（burst 缺省 10）；覆盖 rpm=100（burst 缺省 10）。
	// 两桶的初始容量相同 ⇒ 判别点是**补桶速率**：按 100ms/次的节奏连发（与真实 HTTP 同形），
	// 到第 11 次时累计约 1.0s：全局补 10/60≈0.17 枚（<1 ⇒ 429），覆盖补 100/60≈1.67 枚（≥1 ⇒ 放行）。
	_, clock := rlUseLayer(t, rateLimitConfig{RPM: 10})
	counter := &rlCounter{}
	d := deps{}
	withOverride := func(c *gin.Context) {
		setRateLimitOverride(c, &rateLimitOverride{RPM: 100}) // 模拟 apiKeyAuth 命中 DB Key
		c.Next()
	}
	engine := rlEngine(http.MethodPost, "/rl/override", withOverride, d.rateLimit(), counter.handler(http.StatusOK))
	engine = rlAddRoute(engine, http.MethodPost, "/rl/global", d.rateLimit(), counter.handler(http.StatusOK))

	// 两条路由各用**不同凭证**（桶按凭证分，覆盖只作用于携带它的那个凭证的那条链）。
	for i := 1; i <= 10; i++ {
		clock.advance(100 * time.Millisecond)
		if recorder := rlDo(engine, http.MethodPost, "/rl/override", "", "Bearer sk-key-override"); recorder.Code != http.StatusOK {
			t.Fatalf("覆盖路由第 %d 次（≤ burst）应放行，实际 %d", i, recorder.Code)
		}
		if recorder := rlDo(engine, http.MethodPost, "/rl/global", "", "Bearer sk-key-global"); recorder.Code != http.StatusOK {
			t.Fatalf("全局路由第 %d 次（≤ burst）应放行，实际 %d", i, recorder.Code)
		}
	}
	clock.advance(100 * time.Millisecond)

	if recorder := rlDo(engine, http.MethodPost, "/rl/override", "", "Bearer sk-key-override"); recorder.Code != http.StatusOK {
		t.Fatalf("带 override{rpm:100} 的第 11 次必须仍非 429，实际 %d（body=%s）", recorder.Code, recorder.Body.String())
	}
	recorder := rlDo(engine, http.MethodPost, "/rl/global", "", "Bearer sk-key-global")
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("全局 rpm=10 且无覆盖的第 11 次应为 429，实际 %d", recorder.Code)
	}
	if body := rlDecodeError(t, recorder); body.Error.Code != "rate_limit_exceeded" {
		t.Fatalf("429 code = %q，期望 rate_limit_exceeded", body.Error.Code)
	}
	if counter.count() != 21 {
		t.Fatalf("handler 调用次数 = 21（10+10+1），实际 %d", counter.count())
	}

	// 覆盖的写入口径：RPM ≤ 0 视为「跟随全局」（不写覆盖）。
	ctx := rlTestContext()
	setRateLimitOverride(ctx, &rateLimitOverride{RPM: 0})
	if override := rateLimitOverrideOf(ctx); override != nil {
		t.Fatalf("RPM ≤ 0 不得写覆盖，实际 %+v", override)
	}
	setRateLimitOverride(ctx, nil)
	if override := rateLimitOverrideOf(ctx); override != nil {
		t.Fatalf("nil 覆盖不得改变上下文")
	}
	setRateLimitOverride(ctx, &rateLimitOverride{RPM: 100})
	if override := rateLimitOverrideOf(ctx); override == nil || override.RPM != 100 {
		t.Fatalf("有效覆盖必须读得到，实际 %+v", override)
	}
}

// TestRateLimitKeyOverrideThirdRequestIs429 是 acceptance 口径的重申（handler_apikey.go:23 同一句）：
// 全局 rpm=10、DB Key rate_limit=2 ⇒ **第 3 次** 429，且 429 拦在业务 handler 之前。
// 修复前这条判据不成立（第 1..10 次全放行、第 11 次才 429）：桶容量被渠道级下限 max(10, ceil(rpm/60))
// 顶到 10，而源对 DB Key 是固定窗口 limit=rate_limit（首窗 2 次）。
func TestRateLimitKeyOverrideThirdRequestIs429(t *testing.T) {
	_, clock := rlUseLayer(t, rateLimitConfig{RPM: 10})
	counter := &rlCounter{}
	d := deps{}
	withOverride := func(c *gin.Context) {
		setRateLimitOverride(c, &rateLimitOverride{RPM: 2}) // 模拟 apiKeyAuth 命中 rate_limit=2 的 DB Key
		c.Next()
	}
	engine := rlEngine(http.MethodPost, "/rl/key2", withOverride, d.rateLimit(), counter.handler(http.StatusOK))

	for i := 1; i <= 2; i++ {
		clock.advance(120 * time.Millisecond)
		if recorder := rlDo(engine, http.MethodPost, "/rl/key2", "", "Bearer sk-key-limit2"); recorder.Code != http.StatusOK {
			t.Fatalf("第 %d 次（≤ rate_limit=2）应放行，实际 %d（body=%s）", i, recorder.Code, recorder.Body.String())
		}
	}
	clock.advance(120 * time.Millisecond)
	recorder := rlDo(engine, http.MethodPost, "/rl/key2", "", "Bearer sk-key-limit2")
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("第 3 次必须 429（桶容量 = Key 的 rate_limit = 2），实际 %d（body=%s）", recorder.Code, recorder.Body.String())
	}
	body := rlDecodeError(t, recorder)
	if body.Error.Type != "rate_limit_error" || body.Error.Code != "rate_limit_exceeded" {
		t.Fatalf("429 形状: type=%q code=%q", body.Error.Type, body.Error.Code)
	}
	if counter.count() != 2 {
		t.Fatalf("429 必须在业务 handler 之前 Abort：handler 调用次数 = %d，期望 2", counter.count())
	}

	// 对照：同一层上 rpm=10 的全局桶（无覆盖）仍有 10 枚初始令牌（渠道级口径未被动过）。
	globalEngine := rlEngine(http.MethodPost, "/rl/global10", d.rateLimit(), counter.handler(http.StatusOK))
	for i := 1; i <= 10; i++ {
		clock.advance(100 * time.Millisecond)
		if recorder := rlDo(globalEngine, http.MethodPost, "/rl/global10", "", "Bearer sk-key-global10"); recorder.Code != http.StatusOK {
			t.Fatalf("全局 rpm=10 第 %d 次（≤ burst=10）应放行，实际 %d", i, recorder.Code)
		}
	}
	clock.advance(100 * time.Millisecond)
	if recorder := rlDo(globalEngine, http.MethodPost, "/rl/global10", "", "Bearer sk-key-global10"); recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("全局 rpm=10 第 11 次应为 429（burst 仍按 max(10, ceil(10/60))=10），实际 %d", recorder.Code)
	}
}

func TestRateLimitMiddlewareMaxInflightImmediate429AndRelease(t *testing.T) {
	layer, _ := rlUseLayer(t, rateLimitConfig{RPM: 0, MaxInflight: 2})
	d := deps{}

	started := make(chan struct{}, 8)
	release := make(chan struct{})
	handler := func(c *gin.Context) {
		started <- struct{}{}
		<-release
		c.Status(http.StatusOK)
	}
	engine := rlEngine(http.MethodPost, "/rl", d.rateLimit(), handler)
	results := make(chan int, 8)
	launch := func() {
		go func() { results <- rlDo(engine, http.MethodPost, "/rl", "", "Bearer sk-gateway-key").Code }()
	}

	launch()
	launch()
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatalf("第 %d 个在飞请求没有进 handler", i+1)
		}
	}
	if got := layer.inflightCount(); got != 2 {
		t.Fatalf("在飞数 = %d，期望 2", got)
	}

	// 第 N+1 个：立即 429（不排队）——注意此时 release 尚未关闭。
	recorder := rlDo(engine, http.MethodPost, "/rl", "", "Bearer sk-gateway-key")
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("第 3 个在飞请求应立刻 429，实际 %d", recorder.Code)
	}
	body := rlDecodeError(t, recorder)
	if body.Error.Type != "rate_limit_error" || body.Error.Code != "rate_limit_exceeded" {
		t.Fatalf("在飞 429 形状: type=%q code=%q", body.Error.Type, body.Error.Code)
	}
	if !strings.Contains(body.Error.Message, "在飞") {
		t.Fatalf("在飞 429 文案应说明并发语义: %q", body.Error.Message)
	}
	if got := recorder.Header().Get("Retry-After"); got != "60" {
		t.Fatalf("在飞 429 也应带 Retry-After: 60，实际 %q", got)
	}
	if got := layer.inflightCount(); got != 2 {
		t.Fatalf("被拒的请求不得占名额，在飞数 = %d", got)
	}

	// 释放 ⇒ 两个在飞请求收尾、名额归还，然后第 3 个才放行。
	close(release)
	for i := 0; i < 2; i++ {
		select {
		case code := <-results:
			if code != http.StatusOK {
				t.Fatalf("在飞请求收尾应为 200，实际 %d", code)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("在飞请求没有收尾")
		}
	}
	rlWaitFor(t, "在飞名额归零", func() bool { return layer.inflightCount() == 0 })

	if recorder := rlDo(engine, http.MethodPost, "/rl", "", "Bearer sk-gateway-key"); recorder.Code != http.StatusOK {
		t.Fatalf("释放后应放行，实际 %d", recorder.Code)
	}
	rlWaitFor(t, "收尾后在飞名额归零", func() bool { return layer.inflightCount() == 0 })
}

func TestRateLimitMiddlewareFailOpenOnConfigError(t *testing.T) {
	rlConfigErrorLayer(t, errors.New("配置库不可用"))
	counter := &rlCounter{}
	d := deps{}
	engine := rlEngine(http.MethodPost, "/rl", d.rateLimit(), counter.handler(http.StatusOK))

	for i := 0; i < 5; i++ {
		if recorder := rlDo(engine, http.MethodPost, "/rl", "", "Bearer sk-gateway-key"); recorder.Code != http.StatusOK {
			t.Fatalf("限流配置读失败必须 fail-open（放行 + 日志），实际 %d", recorder.Code)
		}
	}
	if counter.count() != 5 {
		t.Fatalf("fail-open 时 handler 应被调用 5 次，实际 %d", counter.count())
	}
}

// rlAddRoute 在同一引擎上再挂一条路由。
func rlAddRoute(engine *gin.Engine, method, path string, chain ...gin.HandlerFunc) *gin.Engine {
	engine.Handle(method, path, chain...)
	return engine
}

// ===== 接缝：source / model_scope =====

func TestKeySourceSeamDefaultsAndOverride(t *testing.T) {
	if got := keySourceOf(nil); got != publicSource {
		t.Fatalf("nil 上下文应回落 %q，实际 %q", publicSource, got)
	}
	ctx := rlTestContext()
	if got := keySourceOf(ctx); got != publicSource {
		t.Fatalf("未设置时应为 %q，实际 %q", publicSource, got)
	}
	setKeySource(ctx, "   ")
	if got := keySourceOf(ctx); got != publicSource {
		t.Fatalf("纯空白视为未设置，实际 %q", got)
	}
	setKeySource(ctx, " key:app1 ")
	if got := keySourceOf(ctx); got != "key:app1" {
		t.Fatalf("设置后应取所设值，实际 %q", got)
	}
	setKeySource(ctx, "")
	if got := keySourceOf(ctx); got != "key:app1" {
		t.Fatalf("空串不得把已设值抹掉，实际 %q", got)
	}
}

func TestKeyScopeSeamNormalizeMatchAndErrorShape(t *testing.T) {
	if keyScopeSet(nil) != nil || keyScopeSet([]string{"", "  "}) != nil {
		t.Fatalf("无有效项 = 未设置（nil）")
	}
	scope := keyScopeSet([]string{" gpt-a ", "gpt-a", "", "gpt-b"})
	if len(scope) != 2 {
		t.Fatalf("应收敛为 2 项（去空白/去空项/去重），实际 %v", scope)
	}
	if _, exists := scope["gpt-a"]; !exists {
		t.Fatalf("集合应含 gpt-a: %v", scope)
	}

	ctx := rlTestContext()
	if got := keyScopeOf(nil); len(got) != 0 {
		t.Fatalf("nil 上下文 = 未设置，实际 %v", got)
	}
	if got := keyScopeOf(ctx); len(got) != 0 {
		t.Fatalf("未设置时必须为空（len=0 即「不限制」），实际 %v", got)
	}
	if !modelInKeyScope(ctx, "any-model") {
		t.Fatalf("未设置 scope ⇒ 恒放行（v1 现行为不变）")
	}
	setKeyScope(ctx, nil)
	if !modelInKeyScope(ctx, "any-model") {
		t.Fatalf("setKeyScope(nil) 不得变成「拒绝一切」")
	}
	setKeyScope(ctx, []string{" gpt-a "})
	got := keyScopeOf(ctx)
	if len(got) != 1 {
		t.Fatalf("设置后应读回 1 项集合，实际 %v", got)
	}
	if _, exists := got["gpt-a"]; !exists {
		t.Fatalf("读回的白名单应已去空白，实际 %v", got)
	}
	if !modelInKeyScope(ctx, " gpt-a ") {
		t.Fatalf("白名单内（含空白）应放行")
	}
	if modelInKeyScope(ctx, "gpt-b") {
		t.Fatalf("白名单外应被拒")
	}

	apiErr := keyScopeModelNotFound("gpt-b")
	if apiErr.HTTPStatus != http.StatusNotFound || apiErr.Type != "invalid_request_error" || apiErr.Code != "model_not_found" {
		t.Fatalf("白名单外模型错误形状: %+v", apiErr)
	}
	if !strings.Contains(apiErr.Message, "gpt-b") {
		t.Fatalf("文案应含模型名: %q", apiErr.Message)
	}
}

func TestPublicChatAndEmbeddingsSourceFollowKeySourceSeam(t *testing.T) {
	rlUseLayer(t, rateLimitConfig{}) // 限流不参与本用例的判定
	repo, driver := rlChatFixture("gpt-test")
	d := rlServiceDeps(repo, driver, "sk-gateway-key")
	engine := rlPublicEngine(d, nil)

	// ① 未设置 key source ⇒ 与现状逐字一致，仍是 'key:gateway'。
	if recorder := rlDo(engine, http.MethodPost, "/ai/v1/chat/completions",
		`{"model":"gpt-test","messages":[{"role":"user","content":"hi"}]}`, "Bearer sk-gateway-key"); recorder.Code != http.StatusOK {
		t.Fatalf("对话应 200，实际 %d（body=%s）", recorder.Code, recorder.Body.String())
	}
	if rows := repo.usageRows(); len(rows) != 1 || rows[0].Source != publicSource {
		t.Fatalf("未设置 source 时 usage.source 必须仍为 %q，实际 %+v", publicSource, rows)
	}

	// ② 设置后（模拟 apiKeyAuth 命中 DB Key）= 所设值。
	engine = rlPublicEngine(d, rlSourceSetter("key:app1"))
	if recorder := rlDo(engine, http.MethodPost, "/ai/v1/chat/completions",
		`{"model":"gpt-test","messages":[{"role":"user","content":"hi"}]}`, "Bearer sk-gateway-key"); recorder.Code != http.StatusOK {
		t.Fatalf("对话应 200，实际 %d（body=%s）", recorder.Code, recorder.Body.String())
	}
	if rows := repo.usageRows(); len(rows) != 2 || rows[1].Source != "key:app1" {
		t.Fatalf("设置后 usage.source 应为 key:app1，实际 %+v", rows)
	}

	// ③ 向量面同口径（Source 在 handler 里经 keySourceOf 取）。
	embedRepo, embedDriver := rlEmbeddingFixture("bge-test")
	embedDeps := rlServiceDeps(embedRepo, embedDriver, "sk-gateway-key")
	if recorder := rlDo(rlPublicEngine(embedDeps, nil), http.MethodPost, "/ai/v1/embeddings",
		`{"model":"bge-test","input":"hello"}`, "Bearer sk-gateway-key"); recorder.Code != http.StatusOK {
		t.Fatalf("向量应 200，实际 %d（body=%s）", recorder.Code, recorder.Body.String())
	}
	if rows := embedRepo.usageRows(); len(rows) != 1 || rows[0].Source != publicSource {
		t.Fatalf("向量未设置 source 时仍应为 %q，实际 %+v", publicSource, rows)
	}
	if recorder := rlDo(rlPublicEngine(embedDeps, rlSourceSetter("key:app2")), http.MethodPost, "/ai/v1/embeddings",
		`{"model":"bge-test","input":"hello"}`, "Bearer sk-gateway-key"); recorder.Code != http.StatusOK {
		t.Fatalf("向量应 200，实际 %d（body=%s）", recorder.Code, recorder.Body.String())
	}
	if rows := embedRepo.usageRows(); len(rows) != 2 || rows[1].Source != "key:app2" {
		t.Fatalf("向量设置后 usage.source 应为 key:app2，实际 %+v", rows)
	}
}

func TestPublicChatAndEmbeddingsRejectModelsOutsideKeyScope(t *testing.T) {
	rlUseLayer(t, rateLimitConfig{})
	repo, driver := rlChatFixture("gpt-test")
	d := rlServiceDeps(repo, driver, "sk-gateway-key")

	// 白名单内：照常放行（本用例同时是「设置 scope 后正常模型不受影响」的对照）。
	engine := rlPublicEngine(d, rlScopeSetter([]string{"gpt-test", "bge-test"}))
	if recorder := rlDo(engine, http.MethodPost, "/ai/v1/chat/completions",
		`{"model":"gpt-test","messages":[{"role":"user","content":"hi"}]}`, "Bearer sk-gateway-key"); recorder.Code != http.StatusOK {
		t.Fatalf("白名单内模型应放行，实际 %d", recorder.Code)
	}

	// 白名单外：404 model_not_found，且**不进编排**（假仓储零新增 usage）。
	engine = rlPublicEngine(d, rlScopeSetter([]string{"other-model"}))
	recorder := rlDo(engine, http.MethodPost, "/ai/v1/chat/completions",
		`{"model":"gpt-test","messages":[{"role":"user","content":"hi"}]}`, "Bearer sk-gateway-key")
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("白名单外应 404，实际 %d（body=%s）", recorder.Code, recorder.Body.String())
	}
	if body := rlDecodeError(t, recorder); body.Error.Code != "model_not_found" || body.Error.Type != "invalid_request_error" {
		t.Fatalf("白名单外错误形状: %+v", body.Error)
	}
	if rows := repo.usageRows(); len(rows) != 1 {
		t.Fatalf("被拒的调用不得进编排（仍应只有前面那 1 行），实际 %d 行", len(rows))
	}

	embedRepo, embedDriver := rlEmbeddingFixture("bge-test")
	embedDeps := rlServiceDeps(embedRepo, embedDriver, "sk-gateway-key")
	recorder = rlDo(rlPublicEngine(embedDeps, rlScopeSetter([]string{"gpt-test"})), http.MethodPost, "/ai/v1/embeddings",
		`{"model":"bge-test","input":"hello"}`, "Bearer sk-gateway-key")
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("向量白名单外应 404，实际 %d（body=%s）", recorder.Code, recorder.Body.String())
	}
	if rows := embedRepo.usageRows(); len(rows) != 0 {
		t.Fatalf("被拒的向量调用不得进编排，实际 %d 行", len(rows))
	}
}

func TestRateLimit429DoesNotWriteUsageOrChannelStats(t *testing.T) {
	// 429 在业务 handler 之前 Abort ⇒ ① 假仓储不新增 usage 行；② 渠道统计（AddChannelTotals）不增。
	// （真库 SQL 断言另属专面，本用例只做「链路未进入」的判别式。）
	rlUseLayer(t, rateLimitConfig{RPM: 60, Burst: 1})
	repo, driver := rlChatFixture("gpt-test")
	engine := rlPublicEngine(rlServiceDeps(repo, driver, "sk-gateway-key"), nil)

	body := `{"model":"gpt-test","messages":[{"role":"user","content":"hi"}]}`
	if recorder := rlDo(engine, http.MethodPost, "/ai/v1/chat/completions", body, "Bearer sk-gateway-key"); recorder.Code != http.StatusOK {
		t.Fatalf("第 1 次应 200，实际 %d", recorder.Code)
	}
	if got := repo.totalCalls(); got != 1 {
		t.Fatalf("成功路径应恰计一次渠道统计，实际 %d", got)
	}

	recorder := rlDo(engine, http.MethodPost, "/ai/v1/chat/completions", body, "Bearer sk-gateway-key")
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("第 2 次应 429，实际 %d", recorder.Code)
	}
	if rows := repo.usageRows(); len(rows) != 1 {
		t.Fatalf("429 路径不得写 usage，实际 %d 行", len(rows))
	}
	if got := repo.totalCalls(); got != 1 {
		t.Fatalf("429 路径不得计渠道统计，实际 %d", got)
	}
}

// ===== 无库端到端脚手架 =====

// rlServiceDeps 装配公开面的 deps：真配置读（假值）+ 假仓储/假驱动（不碰数据库）。
func rlServiceDeps(repo Repository, driver Driver, apiKey string) deps {
	set := parsePluginSettings(map[string]string{configKeyAPIKey: apiKey})
	return deps{
		loadSettings: func(context.Context) (pluginSettings, error) { return set, nil },
		loadStore:    func(context.Context) (*Store, error) { return nil, errors.New("测试环境无数据库") },
		loadService: func(context.Context, pluginSettings) (*Service, error) {
			return NewService(repo, driver, Config{}, nil), nil
		},
	}
}

// rlPublicEngine 装配「bearerAuth → rateLimit →（可选接缝中间件）→ 业务 handler」的公开面引擎，
// 与装配面落地的链路同形（apiKeyAuth 位置在本用例里由 setter 占位）。
func rlPublicEngine(d deps, setter gin.HandlerFunc) *gin.Engine {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	chain := func(handler gin.HandlerFunc) []gin.HandlerFunc {
		middlewares := []gin.HandlerFunc{d.bearerAuth(), d.rateLimit()}
		if setter != nil {
			middlewares = append(middlewares, setter)
		}
		return append(middlewares, handler)
	}
	engine.POST("/ai/v1/chat/completions", chain(d.publicChat)...)
	engine.POST("/ai/v1/embeddings", chain(d.publicEmbeddings)...)
	engine.GET("/ai/v1/models", chain(d.publicModels)...)
	return engine
}

// rlSourceSetter 模拟 apiKeyAuth 命中 DB Key 后写 source（B2 接缝的生产调用点）。
func rlSourceSetter(source string) gin.HandlerFunc {
	return func(c *gin.Context) {
		setKeySource(c, source)
		c.Next()
	}
}

// rlScopeSetter 模拟 apiKeyAuth 命中带 model_scope 的 DB Key 后写白名单。
func rlScopeSetter(models []string) gin.HandlerFunc {
	return func(c *gin.Context) {
		setKeyScope(c, models)
		c.Next()
	}
}

// rlFakeRepo 是 Repository 的无库假实现：读面返回注入值，写面记录 usage 与渠道统计调用次数。
type rlFakeRepo struct {
	mu         sync.Mutex
	model      Model
	candidates []Candidate
	usages     []Usage
	totals     int
}

func (f *rlFakeRepo) usageRows() []Usage {
	f.mu.Lock()
	defer f.mu.Unlock()
	rows := make([]Usage, len(f.usages))
	copy(rows, f.usages)
	return rows
}

func (f *rlFakeRepo) totalCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.totals
}

func (f *rlFakeRepo) FindModel(_ context.Context, name string) (Model, error) {
	if f.model.Name == "" || f.model.Name != name {
		return Model{}, ErrModelNotFound
	}
	return f.model, nil
}

func (f *rlFakeRepo) Candidates(context.Context, uint64) ([]Candidate, error) {
	return f.candidates, nil
}

func (f *rlFakeRepo) UpsertUsage(_ context.Context, usage *Usage) error {
	if usage != nil {
		f.mu.Lock()
		f.usages = append(f.usages, *usage)
		f.mu.Unlock()
	}
	return nil
}

func (f *rlFakeRepo) EnterCAS(context.Context, uint64, int64) (bool, error) { return true, nil }

func (f *rlFakeRepo) ReselectStaleProbe(context.Context, uint64, int64, int64) (bool, error) {
	return true, nil
}

func (f *rlFakeRepo) MarkFailure(context.Context, uint64, string, int64) (int, error) { return 1, nil }

func (f *rlFakeRepo) MarkSuccess(context.Context, uint64, int64) (bool, error) { return true, nil }

func (f *rlFakeRepo) OpenCircuit(context.Context, uint64, int64) (bool, error) { return true, nil }

func (f *rlFakeRepo) ManualOpenCircuit(context.Context, uint64, int64) (bool, error) {
	return true, nil
}

func (f *rlFakeRepo) ManualCloseCircuit(context.Context, uint64, int64) (int64, error) { return 1, nil }

func (f *rlFakeRepo) AddChannelTotals(context.Context, uint64, bool, int, int64) error {
	f.mu.Lock()
	f.totals++
	f.mu.Unlock()
	return nil
}

func (f *rlFakeRepo) InsertBreakerEvent(context.Context, *BreakerEvent) error { return nil }

// rlFakeDriver 是 Driver 的无网假实现（本用例只关心编排是否被进入，不关心上游细节）。
type rlFakeDriver struct {
	chatResult *ChatResult
	embedding  *EmbeddingResult
}

func (f *rlFakeDriver) Chat(context.Context, Channel, ChatRequest) (*ChatResult, error) {
	if f.chatResult == nil {
		return nil, upstreamInvalid("假驱动没有配置对话结果")
	}
	return f.chatResult, nil
}

func (f *rlFakeDriver) ChatStream(context.Context, Channel, ChatRequest, func([]byte) error) (*ChatResult, error) {
	return nil, upstreamInvalid("本用例不走流式")
}

func (f *rlFakeDriver) Embeddings(context.Context, Channel, EmbeddingRequest) (*EmbeddingResult, error) {
	if f.embedding == nil {
		return nil, upstreamInvalid("假驱动没有配置向量结果")
	}
	return f.embedding, nil
}

func (f *rlFakeDriver) Models(context.Context, Channel) ([]string, error) { return []string{"m1"}, nil }

// rlChatFixture 造「单模型单渠道 + 假驱动」的对话夹具。
func rlChatFixture(modelName string) (*rlFakeRepo, *rlFakeDriver) {
	repo := &rlFakeRepo{
		model: Model{
			ID: 7, Name: modelName, Type: ModelTypeChat, Status: StatusEnabled,
			Strategy: StrategyPriority, Encoding: EncodingNone,
			PriceInput: "0.000000", PriceOutput: "0.000000",
		},
		candidates: []Candidate{{
			ChannelID: 11, Name: "主渠道", Vendor: "openai", BaseURL: "https://api.example.com",
			APIKey: "sk-upstream", Model: modelName, Timeout: 10,
			ChannelStatus: StatusEnabled, Circuit: CircuitClosed,
			ModelID: 7, BindingPriority: 1, BindingWeight: 1, BindingStatus: StatusEnabled,
		}},
	}
	driver := &rlFakeDriver{chatResult: &ChatResult{
		ID: "chatcmpl-rl", Model: modelName, Content: "pong", FinishReason: "stop",
		PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5,
	}}
	return repo, driver
}

// rlEmbeddingFixture 造「单向量模型单渠道 + 假驱动」的夹具。
func rlEmbeddingFixture(modelName string) (*rlFakeRepo, *rlFakeDriver) {
	repo := &rlFakeRepo{
		model: Model{
			ID: 8, Name: modelName, Type: ModelTypeEmbedding, Status: StatusEnabled,
			Strategy: StrategyPriority, Encoding: EncodingNone,
			PriceInput: "0.000000", PriceOutput: "0.000000",
		},
		candidates: []Candidate{{
			ChannelID: 12, Name: "向量渠道", Vendor: "openai", BaseURL: "https://api.example.com",
			APIKey: "sk-upstream", Model: modelName, Timeout: 10,
			ChannelStatus: StatusEnabled, Circuit: CircuitClosed,
			ModelID: 8, BindingPriority: 1, BindingWeight: 1, BindingStatus: StatusEnabled,
		}},
	}
	driver := &rlFakeDriver{embedding: &EmbeddingResult{
		Model: modelName, Data: []Embedding{{Index: 0, Object: "embedding", Vector: []float64{0.1, 0.2}}},
		PromptTokens: 2, CompletionTokens: 0, TotalTokens: 2, UsageSeen: true,
	}}
	return repo, driver
}

// ===== 真库（DSN 门控）：publicModels 的白名单过滤 =====

// TestPublicModelsKeyScopeDB 是本文件唯一的真库用例：未设置 scope 时 /ai/v1/models 的输出
// 必须与 store.ListModels 的同一筛选口径**逐字一致**；已设置 scope 时只剩白名单内的模型。
func TestPublicModelsKeyScopeDB(t *testing.T) {
	db := pluginAITestDB(t)
	store := NewStore(db)
	now := time.Now().Unix()
	for _, row := range []Model{
		{Name: "m-chat-a", Type: ModelTypeChat, Status: StatusEnabled, Strategy: StrategyPriority, CreateTime: now},
		{Name: "m-chat-b", Type: ModelTypeChat, Status: StatusEnabled, Strategy: StrategyPriority, CreateTime: now + 1},
		{Name: "m-embed", Type: ModelTypeEmbedding, Status: StatusEnabled, Strategy: StrategyPriority, CreateTime: now + 2},
		{Name: "m-disabled", Type: ModelTypeChat, Status: StatusEnabled, Strategy: StrategyPriority, CreateTime: now + 3},
	} {
		if err := db.Create(&row).Error; err != nil {
			t.Fatalf("造模型 %s 失败: %v", row.Name, err)
		}
	}
	// 禁用态必须显式 UPDATE 写 0：GORM 对带 default:1 的列会**省略零值**，直接用 Status=0 建行
	// 会被建表默认值改成启用（真库陷阱，内存假库抓不到）。
	if err := db.Model(&Model{}).Where(`"name" = ?`, "m-disabled").
		Update(`"status"`, StatusDisabled).Error; err != nil {
		t.Fatalf("置禁用态失败: %v", err)
	}

	// 期望值：与 handler 同一筛选口径（type=chat、status=1、默认排序 + Limit=MaxListLimit）。
	modelType, status := ModelTypeChat, StatusEnabled
	listed, err := store.ListModels(context.Background(), ModelFilter{
		Page: 1, Limit: MaxListLimit, Type: &modelType, Status: &status,
	})
	if err != nil {
		t.Fatalf("基准列表失败: %v", err)
	}
	want := make([]rlModelEntry, 0, len(listed.Rows))
	for _, row := range listed.Rows {
		want = append(want, rlModelEntry{ID: row.Name, Object: "model", Created: row.CreateTime, OwnedBy: Name})
	}
	if len(want) != 2 {
		t.Fatalf("基准列表应含 2 个启用 chat 模型，实际 %d", len(want))
	}

	d := deps{
		loadSettings: func(context.Context) (pluginSettings, error) {
			return parsePluginSettings(map[string]string{configKeyAPIKey: "sk-gateway-key"}), nil
		},
		loadStore: func(context.Context) (*Store, error) { return store, nil },
		loadService: func(context.Context, pluginSettings) (*Service, error) {
			return nil, errors.New("本用例不调编排")
		},
	}

	// ① 未设置 scope：输出与基准逐字一致。
	recorder := rlDo(rlPublicEngine(d, nil), http.MethodGet, "/ai/v1/models", "", "Bearer sk-gateway-key")
	if recorder.Code != http.StatusOK {
		t.Fatalf("模型列表应 200，实际 %d（body=%s）", recorder.Code, recorder.Body.String())
	}
	got := rlDecodeModels(t, recorder)
	if got.Object != "list" || !reflect.DeepEqual(got.Data, want) {
		t.Fatalf("未设置 scope 的输出必须与现状逐字一致\n got=%+v\nwant=%+v", got.Data, want)
	}

	// ② 设置 scope：只列白名单内的模型，其余字段形状不变。
	recorder = rlDo(rlPublicEngine(d, rlScopeSetter([]string{"m-chat-b"})), http.MethodGet, "/ai/v1/models", "", "Bearer sk-gateway-key")
	if recorder.Code != http.StatusOK {
		t.Fatalf("模型列表应 200，实际 %d", recorder.Code)
	}
	got = rlDecodeModels(t, recorder)
	if len(got.Data) != 1 || got.Data[0].ID != "m-chat-b" {
		t.Fatalf("白名单应只剩 m-chat-b，实际 %+v", got.Data)
	}
	if wantRow := want[0]; got.Data[0].Object != wantRow.Object || got.Data[0].OwnedBy != wantRow.OwnedBy {
		t.Fatalf("白名单过滤不得改动字段形状: %+v", got.Data[0])
	}

	// ③ 空/空白名单 = 未设置（不得变成「一个都不给」）。
	recorder = rlDo(rlPublicEngine(d, rlScopeSetter([]string{"", " "})), http.MethodGet, "/ai/v1/models", "", "Bearer sk-gateway-key")
	if got = rlDecodeModels(t, recorder); !reflect.DeepEqual(got.Data, want) {
		t.Fatalf("空白名单必须等价于未设置\n got=%+v\nwant=%+v", got.Data, want)
	}
}

// rlModelEntry 是 /ai/v1/models 的一条条目（逐字段断言用）。
type rlModelEntry struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

// rlModelsBody 是 /ai/v1/models 的响应体。
type rlModelsBody struct {
	Object string         `json:"object"`
	Data   []rlModelEntry `json:"data"`
}

func rlDecodeModels(t *testing.T, recorder *httptest.ResponseRecorder) rlModelsBody {
	t.Helper()
	var body rlModelsBody
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("模型列表不是 JSON: %v（原文 %s）", err, recorder.Body.String())
	}
	return body
}
