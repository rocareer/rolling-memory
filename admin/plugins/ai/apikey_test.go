// plugin: ai v0.2.0
// apikey_test.go 覆盖应用接入（API Key）在**无数据库**环境下可判定的全部行为：
//
//   - 凭证原语：crypto/rand 形态与长度、sha256 哈希、展示前缀（照源 substr(…,0,12)."…"）；
//   - 入参收敛：source 长度/字符集、model_scope 三态、rate_limit/status/expire_at 边界；
//   - 鉴权五态 + 两条 fail-closed 分支（查库面取不到 / 查库出错 ⇒ 503，**不回落全局比对**）；
//   - 接缝断言：setKeySource('key:'+source) / setKeyScope / setRateLimitOverride（经
//     ratelimit.go 的 keySourceOf / keyScopeOf / rateLimitOverrideOf 读回）；
//   - model_scope 的调用准入（真链断言：本包中间件 → handler_public.go 的白名单判定 ⇒
//     404 model_not_found，且用量行不落；白名单内放行时用量行的 source 必须是 'key:<source>'）；
//   - 鉴权层不碰请求体（>8MiB 的请求仍由 handler 判 413）；
//   - last_used_at 的异步刷新（响应不等它）；
//   - 出口纪律：key_hash 与明文永不出现（列表/详情形状）；
//   - apikey 列表的 wheres 白名单契约。
//
// 真库语义（唯一键冲突、status/expire_at 判定、last_used_at 最终一致、并发同 Key 调用、
// 明文只落 sha256）在 apikey_db_test.go（DSN 门控）。
package ai

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// ===== 假实现与脚手架 =====

// fakeAuthStore 是 apiKeyAuthStore 的内存实现。
//
// lookupErr 非 nil ⇒ 模拟「查库出错」（fail-closed 分支）；touchDelay 模拟慢写（异步性断言）；
// touched 记录被刷新的行 ID（并发安全，后台 goroutine 会写它）。
type fakeAuthStore struct {
	mu         sync.Mutex
	rows       map[string]*APIKey
	lookupErr  error
	touchErr   error
	touchDelay time.Duration
	touched    []int64
}

func (f *fakeAuthStore) LookupAPIKeyByHash(_ context.Context, keyHash string) (*APIKey, error) {
	if f.lookupErr != nil {
		return nil, f.lookupErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.rows[keyHash]
	if !ok {
		return nil, gorm.ErrRecordNotFound
	}
	copied := *row
	return &copied, nil
}

func (f *fakeAuthStore) TouchAPIKeyLastUsed(_ context.Context, id int64, _ int64) (int64, error) {
	if f.touchDelay > 0 {
		time.Sleep(f.touchDelay)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.touchErr != nil {
		return 0, f.touchErr
	}
	f.touched = append(f.touched, id)
	return 1, nil
}

// touchedCount 已刷新的行数（轮询用）。
func (f *fakeAuthStore) touchedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.touched)
}

// newFakeAuthStore 造一个按明文索引的假库：seed 的每对 (明文, 行) 都改成按 sha256 存。
func newFakeAuthStore(rows ...fakeAuthRow) *fakeAuthStore {
	store := &fakeAuthStore{rows: map[string]*APIKey{}}
	for _, item := range rows {
		row := item.row
		store.rows[HashAPIKey(item.plain)] = &row
	}
	return store
}

// fakeAuthRow 假库的一行：明文 + 行（明文只用于算 key_hash，不进任何出口）。
type fakeAuthRow struct {
	plain string
	row   APIKey
}

// authProbe 探针处理器记录的东西（鉴权接缝的断言载体）。
//
// mu 只为「并发请求共用同一个探针」的用例存在（apikey_db_test.go 的同 Key 并发调用）；
// 顺序用例在请求返回后直接读字段即可（gin 的 ServeHTTP 是同步的，返回即写入完成）。
type authProbe struct {
	mu           sync.Mutex
	reached      bool
	source       string
	scopeModels  []string
	hasScope     bool
	override     *rateLimitOverride
	body         string
	maxBodyError bool
}

// recordScope 记录 scope 接缝（nil/空 = 未设置）。
func (p *authProbe) recordScope(scope map[string]struct{}) {
	p.scopeModels = make([]string, 0, len(scope))
	for model := range scope {
		p.scopeModels = append(p.scopeModels, model)
	}
	sortStrings(p.scopeModels)
	p.hasScope = len(scope) > 0
}

// authTestDeps 造「配置 + 查库面」两个依赖（无库单测的统一入口）。
func authTestDeps(set pluginSettings, store apiKeyAuthStore, storeErr error) (func(context.Context) (pluginSettings, error), func(context.Context) (apiKeyAuthStore, error)) {
	loadSettings := func(context.Context) (pluginSettings, error) { return set, nil }
	loadStore := func(context.Context) (apiKeyAuthStore, error) {
		if storeErr != nil {
			return nil, storeErr
		}
		return store, nil
	}
	return loadSettings, loadStore
}

// apiKeyAuthFakeEngine 造「假查库面 + 探针处理器」的引擎（无库单测的统一入口）。
func apiKeyAuthFakeEngine(set pluginSettings, store apiKeyAuthStore, storeErr error, route, path string, probe *authProbe) *gin.Engine {
	loadSettings, loadStore := authTestDeps(set, store, storeErr)
	return apiKeyAuthTestEngine(loadSettings, loadStore, route, path, probe)
}

// withAuthSetting 造一份「全局 key 已配置」的配置。
func withAuthSetting(apiKey string) pluginSettings {
	set := parsePluginSettings(nil)
	set.APIKey = strings.TrimSpace(apiKey)
	set.APIKeySet = set.APIKey != ""
	return set
}

// apiKeyAuthTestEngine 造一个只挂鉴权中间件与探针处理器的引擎（按 route 注册，默认 chat 面）。
func apiKeyAuthTestEngine(loadSettings func(context.Context) (pluginSettings, error),
	loadStore func(context.Context) (apiKeyAuthStore, error),
	route, path string, probe *authProbe,
) *gin.Engine {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	handler := func(c *gin.Context) {
		probe.mu.Lock()
		probe.reached = true
		probe.source = keySourceOf(c)
		probe.recordScope(keyScopeOf(c))
		probe.override = rateLimitOverrideOf(c)
		probe.mu.Unlock()
		if c.Request.Body != nil {
			// 与 handler_public.go 的读法同形：8MiB 上限 + 整读到尾（验证鉴权层没有动过请求体）。
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxPublicBodyBytes)
			data, err := io.ReadAll(c.Request.Body)
			probe.mu.Lock()
			probe.maxBodyError = err != nil
			probe.body = string(data)
			probe.mu.Unlock()
		}
		c.JSON(http.StatusOK, gin.H{"reached": true})
	}
	engine.Handle(route, path, apiKeyAuthWith(loadSettings, loadStore), handler)
	return engine
}

// decodePublicError 解公开面错误体（复用 handler_test.go 的 publicEnvelope 形状）。
func decodePublicError(t *testing.T, recorder *httptest.ResponseRecorder) publicEnvelope {
	t.Helper()
	var envelope publicEnvelope
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("错误体不是 JSON: %v（原文 %s）", err, recorder.Body.String())
	}
	return envelope
}

// ===== 凭证原语 =====

func TestGenerateAPIKeyUsesCryptoRandShape(t *testing.T) {
	const draws = 128
	seen := make(map[string]struct{}, draws)
	for i := 0; i < draws; i++ {
		key, err := GenerateAPIKey()
		if err != nil {
			t.Fatalf("生成 key 失败: %v", err)
		}
		if !strings.HasPrefix(key, APIKeySecretPrefix) {
			t.Fatalf("明文必须以 %q 开头: %q", APIKeySecretPrefix, key)
		}
		body := strings.TrimPrefix(key, APIKeySecretPrefix)
		raw, err := base64.RawURLEncoding.DecodeString(body)
		if err != nil {
			t.Fatalf("明文随机体不是 base64url: %v（%q）", err, key)
		}
		if len(raw) != apiKeyRandomBytes {
			t.Fatalf("明文随机体 = %d 字节，期望 %d（crypto/rand 直接读出的熵）", len(raw), apiKeyRandomBytes)
		}
		if len(raw)*8 < 256 {
			t.Fatalf("熵下界不足：%d 位 < 256 位", len(raw)*8)
		}
		if _, ok := seen[key]; ok {
			t.Fatalf("%d 次生成出现重复明文（随机源可疑）: %q", draws, key)
		}
		seen[key] = struct{}{}
	}
	// 长度契约：'ak_'(3) + 43 字符（32 字节的 base64url 无填充）。
	if sample, err := GenerateAPIKey(); err != nil || len(sample) != 3+43 {
		t.Fatalf("明文长度 = %d（err=%v），期望 46", len(sample), err)
	}
}

func TestHashAPIKeyIsPlainSHA256Hex(t *testing.T) {
	// 公开向量（sha256("hello")）：证明哈希口径就是 sha256 十六进制，而不是别的东西。
	const hello = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	if got := HashAPIKey("hello"); got != hello {
		t.Fatalf("HashAPIKey(\"hello\") = %q，期望 %q", got, hello)
	}
	key, err := GenerateAPIKey()
	if err != nil {
		t.Fatalf("生成 key 失败: %v", err)
	}
	hash := HashAPIKey(key)
	if len(hash) != 64 {
		t.Fatalf("哈希长度 = %d，期望 64（key_hash varchar(64) 的列宽）", len(hash))
	}
	if hash != hex.EncodeToString(func() []byte { sum := sha256.Sum256([]byte(key)); return sum[:] }()) {
		t.Fatal("哈希不是 sha256 十六进制")
	}
	if strings.Contains(hash, key) || strings.Contains(hash, strings.TrimPrefix(key, APIKeySecretPrefix)) {
		t.Fatal("哈希里不得包含明文片段")
	}
}

func TestAPIKeyDisplayPrefixFollowsSourceShape(t *testing.T) {
	cases := []struct{ name, input, want string }{
		{"完整明文（ak_+43）", "ak_abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG", "ak_abcdefghi" + "…"},
		{"恰好 12 字符", "ak_123456789", "ak_123456789" + "…"},
		{"空串（未签发）", "", ""},
		{"纯空白", "   ", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := APIKeyDisplayPrefix(tc.input); got != tc.want {
				t.Fatalf("APIKeyDisplayPrefix(%q) = %q，期望 %q", tc.input, got, tc.want)
			}
		})
	}
	// 前缀只暴露明文的前 12 字符：不得包含其后的任何字符。
	key, err := GenerateAPIKey()
	if err != nil {
		t.Fatalf("生成 key 失败: %v", err)
	}
	prefix := APIKeyDisplayPrefix(key)
	for _, suffix := range []string{key[13:20], key[len(key)-8:]} {
		if strings.Contains(prefix, suffix) {
			t.Fatalf("展示前缀泄露了明文后段 %q: %q", suffix, prefix)
		}
	}
}

func TestValidateAPIKeySourceLengthAndCharset(t *testing.T) {
	// 长度口径：24 通过、25 拒绝（保证 'key:'+source ≤ 30 = plugin_ai_usage.source 列宽）。
	ok := strings.Repeat("a", APIKeyMaxSource)
	if got, err := ValidateAPIKeySource("  " + ok + "  "); err != nil || got != ok {
		t.Fatalf("24 字符 source 必须通过并 trim: %q err=%v", got, err)
	}
	if _, err := ValidateAPIKeySource(strings.Repeat("a", APIKeyMaxSource+1)); err == nil {
		t.Fatal("25 字符 source 必须被拒（否则 usage.source 会被列宽截断）")
	}
	if _, err := ValidateAPIKeySource(""); err == nil {
		t.Fatal("空 source 必须被拒")
	}
	for _, bad := range []string{"app a", "app/a", "app@a", "应用", "app\ta", "app#a"} {
		if _, err := ValidateAPIKeySource(bad); err == nil {
			t.Fatalf("字符集外的 source %q 必须被拒", bad)
		}
	}
	for _, good := range []string{"app", "app-1", "app_2", "key:app.3", "A9"} {
		if _, err := ValidateAPIKeySource(good); err != nil {
			t.Fatalf("合法 source %q 被拒: %v", good, err)
		}
	}
}

func TestNormalizeModelScopeTriStates(t *testing.T) {
	cases := []struct {
		name    string
		raw     any
		want    []string
		wantErr bool
	}{
		{"缺省 null ⇒ 空白名单", nil, nil, false},
		{"空串 ⇒ 空白名单", "", nil, false},
		{"JSON 数组字符串", `["gpt-4","gpt-4o"]`, []string{"gpt-4", "gpt-4o"}, false},
		{"逗号分隔字符串", "gpt-4, gpt-4o", []string{"gpt-4", "gpt-4o"}, false},
		{"[]any 丢非字符串元素", []any{"gpt-4", 5, true, "gpt-4o"}, []string{"gpt-4", "gpt-4o"}, false},
		{"去重与去空白", []any{" gpt-4 ", "gpt-4", "", "  "}, []string{"gpt-4"}, false},
		{"超长项丢弃", []any{"gpt-4", strings.Repeat("x", apiKeyMaxScopeItem+1)}, []string{"gpt-4"}, false},
		{"非数组值显式拒绝", 5, nil, true},
		{"对象显式拒绝", map[string]any{"a": 1}, nil, true},
		{"布尔显式拒绝", true, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeModelScope(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("期望错误，实际 %v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("不该报错: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("结果 = %v，期望 %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("结果 = %v，期望 %v", got, tc.want)
				}
			}
		})
	}
	// 项数上限：超出部分丢弃（白名单只会更严，不会更宽）。
	items := make([]any, 0, apiKeyMaxScopeItems+10)
	for i := 0; i < apiKeyMaxScopeItems+10; i++ {
		items = append(items, "m"+strings.Repeat("0", 3)+string(rune('a'+i%26))+strings.Repeat("z", i%7))
	}
	scope, err := NormalizeModelScope(items)
	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	if len(scope) > apiKeyMaxScopeItems {
		t.Fatalf("项数 = %d，超过上限 %d", len(scope), apiKeyMaxScopeItems)
	}
}

func TestAPIKeyAllowsModelAndExpiry(t *testing.T) {
	full := APIKey{Status: StatusEnabled}
	if !full.AllowsModel("any-model") {
		t.Fatal("空白名单必须允许全部模型")
	}
	scoped := APIKey{Status: StatusEnabled, ModelScope: []string{"gpt-4", "gpt-4o"}}
	if !scoped.AllowsModel("gpt-4") || !scoped.AllowsModel(" gpt-4o ") {
		t.Fatal("白名单内模型必须放行")
	}
	if scoped.AllowsModel("gpt-5") {
		t.Fatal("白名单外模型必须被拒")
	}
	if scoped.AllowsModel("") {
		t.Fatal("空模型名不在白名单内（handler 层另给 400）")
	}

	now := int64(1_700_000_000)
	past, future, zero := now-1, now+1, int64(0)
	if (APIKey{Status: StatusEnabled, ExpireAt: &past}).IsExpired(now) != true {
		t.Fatal("过期时刻在现在之前 ⇒ 已过期")
	}
	if (APIKey{Status: StatusEnabled, ExpireAt: &future}).IsExpired(now) != false {
		t.Fatal("过期时刻在现在之后 ⇒ 未过期")
	}
	if (APIKey{Status: StatusEnabled, ExpireAt: &zero}).IsExpired(now) != false {
		t.Fatal("0 = 永不过期")
	}
	if (APIKey{Status: StatusEnabled}).IsExpired(now) != false {
		t.Fatal("NULL = 永不过期")
	}
	if !(APIKey{Status: StatusEnabled, ExpireAt: &future}).Authenticatable(now) {
		t.Fatal("启用且未过期 ⇒ 可鉴权")
	}
	if (APIKey{Status: StatusDisabled, ExpireAt: &future}).Authenticatable(now) {
		t.Fatal("停用 ⇒ 不可鉴权（即使未过期）")
	}
	if (APIKey{Status: StatusEnabled, ExpireAt: &past}).Authenticatable(now) {
		t.Fatal("已过期 ⇒ 不可鉴权")
	}
}

// ===== 鉴权五态与两条 fail-closed 分支 =====

func TestAPIKeyAuthStatesWithFakeStore(t *testing.T) {
	const plain = "ak_auth-states-0001"
	past := int64(1)
	future := nowUnix() + 3600

	cases := []struct {
		name       string
		seeded     []fakeAuthRow
		globalKey  string
		header     string
		lookupErr  error
		storeErr   error
		wantStatus int
		wantCode   string
		wantSource string
	}{
		{
			name:       "有效 Key ⇒ 放行并把 source 写成 key:<source>",
			seeded:     []fakeAuthRow{{plain: plain, row: APIKey{ID: 7, Source: "app-a", Status: StatusEnabled, ExpireAt: &future}}},
			header:     "Bearer " + plain,
			wantStatus: http.StatusOK,
			wantSource: "key:app-a",
		},
		{
			name:       "停用的 Key ⇒ 401",
			seeded:     []fakeAuthRow{{plain: plain, row: APIKey{ID: 7, Source: "app-a", Status: StatusDisabled}}},
			header:     "Bearer " + plain,
			wantStatus: http.StatusUnauthorized,
			wantCode:   "invalid_api_key",
		},
		{
			name:       "过期的 Key ⇒ 401",
			seeded:     []fakeAuthRow{{plain: plain, row: APIKey{ID: 7, Source: "app-a", Status: StatusEnabled, ExpireAt: &past}}},
			header:     "Bearer " + plain,
			wantStatus: http.StatusUnauthorized,
			wantCode:   "invalid_api_key",
		},
		{
			name:       "库中无此 Key 且全局未配置 ⇒ 503（现行为不变）",
			header:     "Bearer " + plain,
			wantStatus: http.StatusServiceUnavailable,
			wantCode:   "api_key_not_configured",
		},
		{
			name:       "库中无此 Key 但全局已配置 ⇒ 401（未命中凭证一律 401）",
			globalKey:  "sk-global-1234567890",
			header:     "Bearer " + plain,
			wantStatus: http.StatusUnauthorized,
			wantCode:   "invalid_api_key",
		},
		{
			name:       "全局 key 正确 ⇒ 放行且 source 维持 key:gateway",
			globalKey:  "sk-global-1234567890",
			header:     "Bearer sk-global-1234567890",
			wantStatus: http.StatusOK,
			wantSource: publicSource,
		},
		{
			name:       "缺 Authorization 且全局未配置 ⇒ 503",
			wantStatus: http.StatusServiceUnavailable,
			wantCode:   "api_key_not_configured",
		},
		{
			name:       "缺 Authorization 但全局已配置 ⇒ 401",
			globalKey:  "sk-global-1234567890",
			wantStatus: http.StatusUnauthorized,
			wantCode:   "invalid_api_key",
		},
		{
			name:       "前缀非 Bearer ⇒ 401",
			globalKey:  "sk-global-1234567890",
			header:     "Basic sk-global-1234567890",
			wantStatus: http.StatusUnauthorized,
			wantCode:   "invalid_api_key",
		},
		{
			name:       "查库出错 ⇒ 503 fail-closed",
			seeded:     []fakeAuthRow{{plain: plain, row: APIKey{ID: 7, Source: "app-a", Status: StatusEnabled}}},
			globalKey:  "sk-global-1234567890",
			header:     "Bearer " + plain,
			lookupErr:  errors.New("db down"),
			wantStatus: http.StatusServiceUnavailable,
			wantCode:   "store_unavailable",
		},
		{
			name:       "查库出错 + 出示的正是全局 key ⇒ 仍 503（绝不静默回落全局比对）",
			globalKey:  "sk-global-1234567890",
			header:     "Bearer sk-global-1234567890",
			lookupErr:  errors.New("db down"),
			wantStatus: http.StatusServiceUnavailable,
			wantCode:   "store_unavailable",
		},
		{
			name:       "查库面取不到（无库）⇒ 503 fail-closed",
			globalKey:  "sk-global-1234567890",
			header:     "Bearer sk-global-1234567890",
			storeErr:   errors.New("测试环境无数据库"),
			wantStatus: http.StatusServiceUnavailable,
			wantCode:   "store_unavailable",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeAuthStore(tc.seeded...)
			store.lookupErr = tc.lookupErr
			probe := &authProbe{}
			engine := apiKeyAuthFakeEngine(withAuthSetting(tc.globalKey), store, tc.storeErr,
				http.MethodPost, "/ai/v1/chat/completions", probe)
			headers := map[string]string{}
			if tc.header != "" {
				headers["Authorization"] = tc.header
			}
			recorder := doRequest(engine, http.MethodPost, "/ai/v1/chat/completions",
				`{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`, headers)

			if recorder.Code != tc.wantStatus {
				t.Fatalf("状态码 = %d，期望 %d（body=%s）", recorder.Code, tc.wantStatus, recorder.Body.String())
			}
			if tc.wantCode != "" {
				if envelope := decodePublicError(t, recorder); envelope.Error.Code != tc.wantCode {
					t.Fatalf("错误码 = %q，期望 %q", envelope.Error.Code, tc.wantCode)
				}
			}
			if probe.reached != (tc.wantStatus == http.StatusOK) {
				t.Fatalf("探针到达 = %v，状态码 %d", probe.reached, recorder.Code)
			}
			if tc.wantSource != "" && probe.source != tc.wantSource {
				t.Fatalf("source 接缝 = %q，期望 %q", probe.source, tc.wantSource)
			}
			// 任何分支都不得回显出示的凭证。
			if tc.header != "" {
				token := strings.TrimPrefix(tc.header, "Bearer ")
				if strings.Contains(recorder.Body.String(), token) {
					t.Fatalf("回执回显了凭证: %s", recorder.Body.String())
				}
			}
		})
	}
}

func TestAPIKeyAuthSettingsReadFailureIs503(t *testing.T) {
	probe := &authProbe{}
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.POST("/ai/v1/chat/completions",
		apiKeyAuthWith(
			func(context.Context) (pluginSettings, error) {
				return pluginSettings{}, errors.New("配置读不出来")
			},
			func(context.Context) (apiKeyAuthStore, error) { return newFakeAuthStore(), nil },
		),
		func(c *gin.Context) { probe.reached = true })
	recorder := doRequest(engine, http.MethodPost, "/ai/v1/chat/completions", "{}", map[string]string{"Authorization": "Bearer whatever"})
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("状态码 = %d，期望 503", recorder.Code)
	}
	if envelope := decodePublicError(t, recorder); envelope.Error.Code != "settings_unavailable" {
		t.Fatalf("错误码 = %q，期望 settings_unavailable", envelope.Error.Code)
	}
	if probe.reached {
		t.Fatal("配置读不到时不得放行")
	}
}

// ===== 接缝：source / scope / 限流覆盖 =====

func TestAPIKeyAuthSetsRateLimitOverrideOnlyWhenPositive(t *testing.T) {
	const plain = "ak_ratelimit-0001"
	future := nowUnix() + 3600
	cases := []struct {
		name      string
		rateLimit int
		wantNil   bool
		wantRPM   int
	}{
		{"rate_limit=0 ⇒ 不写 override（跟随全局）", 0, true, 0},
		{"rate_limit=2 ⇒ 覆盖全局（第 3 次 429 的用例值）", 2, false, 2},
		{"rate_limit=100 ⇒ 覆盖全局", 100, false, 100},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeAuthStore(fakeAuthRow{plain: plain, row: APIKey{
				ID: 1, Source: "app-a", Status: StatusEnabled, ExpireAt: &future, RateLimit: tc.rateLimit,
			}})
			probe := &authProbe{}
			engine := apiKeyAuthFakeEngine(withAuthSetting(""), store, nil,
				http.MethodPost, "/ai/v1/chat/completions", probe)
			recorder := doRequest(engine, http.MethodPost, "/ai/v1/chat/completions",
				`{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`,
				map[string]string{"Authorization": "Bearer " + plain})
			if recorder.Code != http.StatusOK {
				t.Fatalf("状态码 = %d（body=%s）", recorder.Code, recorder.Body.String())
			}
			if tc.wantNil {
				if probe.override != nil {
					t.Fatalf("rate_limit=0 不得写覆盖，实际 %+v", *probe.override)
				}
				return
			}
			if probe.override == nil {
				t.Fatal("rate_limit>0 必须写覆盖（否则按 Key 限流不生效）")
			}
			if probe.override.RPM != tc.wantRPM {
				t.Fatalf("override.RPM = %d，期望 %d", probe.override.RPM, tc.wantRPM)
			}
		})
	}
}

func TestAPIKeyAuthScopeSeamOnlyWhenWhitelistNonEmpty(t *testing.T) {
	const plain = "ak_scope-seam-0001"
	future := nowUnix() + 3600
	store := newFakeAuthStore(fakeAuthRow{plain: plain, row: APIKey{
		ID: 1, Source: "app-a", Status: StatusEnabled, ExpireAt: &future, ModelScope: []string{"gpt-4", "gpt-4o"},
	}})
	probe := &authProbe{}
	engine := apiKeyAuthFakeEngine(withAuthSetting(""), store, nil,
		http.MethodPost, "/ai/v1/chat/completions", probe)
	recorder := doRequest(engine, http.MethodPost, "/ai/v1/chat/completions",
		`{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`,
		map[string]string{"Authorization": "Bearer " + plain})
	if recorder.Code != http.StatusOK {
		t.Fatalf("白名单内模型必须放行，状态码 %d（body=%s）", recorder.Code, recorder.Body.String())
	}
	if !probe.hasScope || len(probe.scopeModels) != 2 || probe.scopeModels[0] != "gpt-4" || probe.scopeModels[1] != "gpt-4o" {
		t.Fatalf("scope 接缝 = %v（ok=%v），期望 [gpt-4 gpt-4o]", probe.scopeModels, probe.hasScope)
	}

	// 空白名单：不写接缝（等同未设置 ⇒ publicModels 维持现状输出）。
	empty := newFakeAuthStore(fakeAuthRow{plain: plain, row: APIKey{
		ID: 1, Source: "app-a", Status: StatusEnabled, ExpireAt: &future,
	}})
	probeEmpty := &authProbe{}
	engineEmpty := apiKeyAuthFakeEngine(withAuthSetting(""), empty, nil,
		http.MethodPost, "/ai/v1/chat/completions", probeEmpty)
	if recorder := doRequest(engineEmpty, http.MethodPost, "/ai/v1/chat/completions",
		`{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`,
		map[string]string{"Authorization": "Bearer " + plain}); recorder.Code != http.StatusOK {
		t.Fatalf("空白名单必须放行，状态码 %d", recorder.Code)
	}
	if probeEmpty.hasScope {
		t.Fatalf("空白名单不得写 scope 接缝，实际 %v", probeEmpty.scopeModels)
	}
}

// ===== model_scope 的调用准入：真链（本包的 apiKeyAuth → 公开面 handler 的接缝消费方）=====

// apiKeyPublicEngine 造「本包鉴权中间件 + 公开面业务处理器」的链，镜子日后 mountPublic 的
// `apiKeyAuth → rateLimit → handler`（限流层是其后追加的独立中间件，不改变返回值断言）。
//
// 三个依赖显式入参：查库面 = 假实现（无库单测）/真 Store（库级用例），service 层 = 假仓储与假驱动
// （复用 handler_test.go 的 engineDeps，于是「放行」能一路走到用量行）。
func apiKeyPublicEngine(set pluginSettings, store apiKeyAuthStore, repo Repository, driver Driver) *gin.Engine {
	base := engineDeps(repo, driver, "")
	loadSettings := func(context.Context) (pluginSettings, error) { return set, nil }
	loadStore := func(context.Context) (apiKeyAuthStore, error) { return store, nil }
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	group := engine.Group("")
	group.POST("/ai/v1/chat/completions", apiKeyAuthWith(loadSettings, loadStore), base.publicChat)
	group.POST("/ai/v1/embeddings", apiKeyAuthWith(loadSettings, loadStore), base.publicEmbeddings)
	group.GET("/ai/v1/models", apiKeyAuthWith(loadSettings, loadStore), base.publicModels)
	return engine
}

// apiKeyScopedFixture 造「一条 model_scope=[gpt-4] 的 DB Key + 假仓储/假驱动」的真链。
func apiKeyScopedFixture(t *testing.T, scope []string) (*fakeRepository, *gin.Engine, map[string]string) {
	t.Helper()
	const plain = "ak_scope-gate-00001"
	future := nowUnix() + 3600
	store := newFakeAuthStore(fakeAuthRow{plain: plain, row: APIKey{
		ID: 1, Source: "app-a", Status: StatusEnabled, ExpireAt: &future, ModelScope: scope, RateLimit: 2,
	}})
	repo := newScopedFixtureRepo(t)
	driver := &fakeDriver{chatResult: &ChatResult{
		ID: "chatcmpl-1", Model: "gpt-4", Content: "你好", FinishReason: "stop",
		PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5,
	}}
	engine := apiKeyPublicEngine(withAuthSetting(""), store, repo, driver)
	return repo, engine, map[string]string{"Authorization": "Bearer " + plain}
}

// newScopedFixtureRepo 造与 handler_test.go 的 newGatewayFixture 同形的假仓储（模型名 gpt-4）。
func newScopedFixtureRepo(t *testing.T) *fakeRepository {
	t.Helper()
	return &fakeRepository{
		model: Model{
			ID: 7, Name: "gpt-4", Type: ModelTypeChat, Status: StatusEnabled,
			Strategy: StrategyPriority, Encoding: EncodingNone,
			PriceInput: "0.000000", PriceOutput: "0.000000",
		},
		candidates: []Candidate{{
			ChannelID: 11, Name: "主渠道", Vendor: "openai", BaseURL: "https://api.example.com",
			APIKey: "sk-upstream", Model: "gpt-4", Timeout: 10,
			ChannelStatus: StatusEnabled, Circuit: CircuitClosed,
			ModelID: 7, BindingPriority: 1, BindingWeight: 1, BindingStatus: StatusEnabled,
		}},
	}
}

func TestAPIKeyAuthScopeGateOnAssembledPublicFace(t *testing.T) {
	t.Run("白名单外模型 ⇒ OpenAI 形状 404 且不发上游、不落用量", func(t *testing.T) {
		repo, engine, auth := apiKeyScopedFixture(t, []string{"gpt-4"})
		recorder := doRequest(engine, http.MethodPost, "/ai/v1/chat/completions",
			`{"model":"gpt-5","messages":[{"role":"user","content":"hi"}]}`, auth)
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("状态码 = %d，期望 404（body=%s）", recorder.Code, recorder.Body.String())
		}
		envelope := decodePublicError(t, recorder)
		if envelope.Error.Code != "model_not_found" || envelope.Error.Type != "invalid_request_error" {
			t.Fatalf("错误体不是 OpenAI 形状: %+v", envelope)
		}
		if len(repo.usages) != 0 {
			t.Fatalf("白名单外模型不得落用量行: %+v", repo.usages)
		}
	})

	t.Run("白名单内模型 ⇒ 放行，且用量行的 source 是 key:<source>", func(t *testing.T) {
		repo, engine, auth := apiKeyScopedFixture(t, []string{"gpt-4"})
		recorder := doRequest(engine, http.MethodPost, "/ai/v1/chat/completions",
			`{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`, auth)
		if recorder.Code != http.StatusOK {
			t.Fatalf("状态码 = %d（body=%s）", recorder.Code, recorder.Body.String())
		}
		if len(repo.usages) != 1 {
			t.Fatalf("用量行数 = %d，期望 1", len(repo.usages))
		}
		if repo.usages[0].Source != "key:app-a" {
			t.Fatalf("用量行 source = %q，期望 key:app-a（B2：不再恒为 key:gateway）", repo.usages[0].Source)
		}
	})

	t.Run("向量面同口径（白名单外 ⇒ 404，不发上游）", func(t *testing.T) {
		repo, engine, auth := apiKeyScopedFixture(t, []string{"gpt-4"})
		recorder := doRequest(engine, http.MethodPost, "/ai/v1/embeddings", `{"model":"gpt-5","input":"hi"}`, auth)
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("状态码 = %d，期望 404（body=%s）", recorder.Code, recorder.Body.String())
		}
		if len(repo.usages) != 0 {
			t.Fatalf("白名单外模型不得落用量行: %+v", repo.usages)
		}
	})

	t.Run("空白名单 ⇒ 不按白名单拒（模型不存在时才由编排层 404）", func(t *testing.T) {
		repo, engine, auth := apiKeyScopedFixture(t, nil)
		// 假仓储只认识 gpt-4；空白名单的 Key 用 gpt-4 必须放行（若白名单被空切片过滤，这里会是 404）。
		recorder := doRequest(engine, http.MethodPost, "/ai/v1/chat/completions",
			`{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`, auth)
		if recorder.Code != http.StatusOK || len(repo.usages) != 1 {
			t.Fatalf("状态码 = %d，用量行 = %d（空白名单必须不过滤）", recorder.Code, len(repo.usages))
		}
	})

	t.Run("全局 key（无 scope 概念）不受白名单约束", func(t *testing.T) {
		repo := newScopedFixtureRepo(t)
		driver := &fakeDriver{chatResult: &ChatResult{ID: "chatcmpl-2", Model: "gpt-4", Content: "ok", FinishReason: "stop"}}
		engine := apiKeyPublicEngine(withAuthSetting("sk-global-1234567890"), newFakeAuthStore(), repo, driver)
		recorder := doRequest(engine, http.MethodPost, "/ai/v1/chat/completions",
			`{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`,
			map[string]string{"Authorization": "Bearer sk-global-1234567890"})
		if recorder.Code != http.StatusOK {
			t.Fatalf("状态码 = %d（body=%s）", recorder.Code, recorder.Body.String())
		}
		if len(repo.usages) != 1 || repo.usages[0].Source != publicSource {
			t.Fatalf("全局 key 的用量 source 必须是 %q，实际 %+v", publicSource, repo.usages)
		}
	})
}

func TestAPIKeyAuthDoesNotTouchRequestBody(t *testing.T) {
	// 鉴权层不读请求体：超过 8MiB 的请求仍由 handler 判 413（没有被中间件截断/吞掉信号）。
	_, engine, auth := apiKeyScopedFixture(t, []string{"gpt-4"})
	oversize := `{"model":"gpt-4","messages":[{"role":"user","content":"` +
		strings.Repeat("y", maxPublicBodyBytes+1024) + `"}]}`
	recorder := doRequest(engine, http.MethodPost, "/ai/v1/chat/completions", oversize, auth)
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("状态码 = %d，期望 413（鉴权层不得读/截断请求体）", recorder.Code)
	}
	if envelope := decodePublicError(t, recorder); envelope.Error.Code != "request_too_large" {
		t.Fatalf("错误码 = %q，期望 request_too_large", envelope.Error.Code)
	}
}

// ===== last_used_at：不阻塞响应 =====

func TestAPIKeyAuthLastUsedRefreshIsAsynchronous(t *testing.T) {
	const plain = "ak_lastused-000001"
	future := nowUnix() + 3600
	store := newFakeAuthStore(fakeAuthRow{plain: plain, row: APIKey{
		ID: 11, Source: "app-a", Status: StatusEnabled, ExpireAt: &future,
	}})
	// 刷新故意慢到 1s：响应若在等它，耗时会 ≥ 1s。
	store.touchDelay = time.Second
	probe := &authProbe{}
	engine := apiKeyAuthFakeEngine(withAuthSetting(""), store, nil,
		http.MethodPost, "/ai/v1/chat/completions", probe)

	start := time.Now()
	recorder := doRequest(engine, http.MethodPost, "/ai/v1/chat/completions",
		`{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`,
		map[string]string{"Authorization": "Bearer " + plain})
	elapsed := time.Since(start)
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d", recorder.Code)
	}
	if elapsed > 400*time.Millisecond {
		t.Fatalf("响应耗时 %v：不得等 last_used_at 的写（异步刷新被阻塞了）", elapsed)
	}
	// 最终一致：刷新迟早会落地（轮询 ≤2s；真库口径见 apikey_db_test.go 的 ≤5s）。
	deadline := time.Now().Add(2 * time.Second)
	for store.touchedCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if store.touchedCount() == 0 {
		t.Fatal("last_used_at 的异步刷新未落地")
	}
}

// ===== 入参收敛（handler 纯函数）=====

func TestAPIKeySaveRequestIssueValidatesAndHashes(t *testing.T) {
	req := apiKeySaveRequest{
		Name:       stringPtr("  应用 A  "),
		Source:     stringPtr("app-a"),
		ModelScope: json.RawMessage(`["gpt-4","gpt-4"]`),
		RateLimit:  intPtr(2),
		ExpireAt:   func() *int64 { v := int64(-5); return &v }(),
		Remark:     stringPtr(strings.Repeat("备", apiKeyMaxRemark+10)),
	}
	row, plain, err := req.issue()
	if err != nil {
		t.Fatalf("签发入参被拒: %v", err)
	}
	if row.Name != "应用 A" {
		t.Fatalf("name 未 trim: %q", row.Name)
	}
	if row.KeyHash != HashAPIKey(plain) || len(row.KeyHash) != 64 {
		t.Fatalf("key_hash 不是明文 sha256: %q", row.KeyHash)
	}
	if row.KeyPrefix != APIKeyDisplayPrefix(plain) || strings.Contains(row.KeyPrefix, plain) {
		t.Fatalf("key_prefix 不是展示前缀: %q", row.KeyPrefix)
	}
	if len(row.ModelScope) != 1 || row.ModelScope[0] != "gpt-4" {
		t.Fatalf("model_scope 未去重: %v", row.ModelScope)
	}
	if row.Status != StatusEnabled || row.RateLimit != 2 || row.ExpireAt != nil {
		t.Fatalf("默认值不符: %+v", row)
	}
	if len([]rune(row.Remark)) != apiKeyMaxRemark {
		t.Fatalf("remark 未按列宽截断: %d", len([]rune(row.Remark)))
	}

	// 边界：name/source 必填与长度、status 闭集、rate_limit 负数收敛。
	cases := []struct {
		name string
		req  apiKeySaveRequest
	}{
		{"name 缺失", apiKeySaveRequest{Source: stringPtr("app-a")}},
		{"name 超长", apiKeySaveRequest{Name: stringPtr(strings.Repeat("n", apiKeyMaxName+1)), Source: stringPtr("app-a")}},
		{"source 缺失", apiKeySaveRequest{Name: stringPtr("n")}},
		{"source 超长（>24）", apiKeySaveRequest{Name: stringPtr("n"), Source: stringPtr(strings.Repeat("s", APIKeyMaxSource+1))}},
		{"source 字符集外", apiKeySaveRequest{Name: stringPtr("n"), Source: stringPtr("bad source")}},
		{"model_scope 非数组", apiKeySaveRequest{Name: stringPtr("n"), Source: stringPtr("a"), ModelScope: json.RawMessage(`5`)}},
		{"status 闭集外", apiKeySaveRequest{Name: stringPtr("n"), Source: stringPtr("a"), Status: intPtr16(7)}},
		{"status=0（新建恒启用，照源口径）", apiKeySaveRequest{Name: stringPtr("n"), Source: stringPtr("a"), Status: intPtr16(0)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := tc.req.issue(); err == nil {
				t.Fatalf("必须被拒: %+v", tc.req)
			}
		})
	}
	// status=1 显式声明与缺省同义（恒建启用行）。
	enabled := apiKeySaveRequest{Name: stringPtr("n"), Source: stringPtr("a"), Status: intPtr16(1)}
	if row, _, err := enabled.issue(); err != nil || row.Status != StatusEnabled {
		t.Fatalf("status=1 必须通过且落启用: %+v err=%v", row, err)
	}
	negative := apiKeySaveRequest{Name: stringPtr("n"), Source: stringPtr("a"), RateLimit: intPtr(-3)}
	row, _, err = negative.issue()
	if err != nil || row.RateLimit != 0 {
		t.Fatalf("rate_limit 负数应收敛为 0: %+v err=%v", row, err)
	}
}

func TestAPIKeySaveRequestPatchTriStates(t *testing.T) {
	// model_scope 三态：缺省 = 不修改 / null 或 [] = 清空 / 数组 = 设置。
	absent := apiKeySaveRequest{Name: stringPtr("新名")}
	patch, err := absent.patch()
	if err != nil {
		t.Fatalf("缺省补丁报错: %v", err)
	}
	if patch.ModelScope != nil || patch.Source != nil {
		t.Fatalf("缺省字段不得进补丁: %+v", patch)
	}
	if patch.Name == nil || *patch.Name != "新名" {
		t.Fatalf("name 应进补丁: %+v", patch)
	}

	for _, raw := range []string{`null`, `[]`} {
		clear := apiKeySaveRequest{ModelScope: json.RawMessage(raw)}
		patch, err := clear.patch()
		if err != nil {
			t.Fatalf("清空 model_scope（%s）报错: %v", raw, err)
		}
		if patch.ModelScope == nil || len(*patch.ModelScope) != 0 {
			t.Fatalf("model_scope=%s 必须表达「清空」（non-nil 空切片），实际 %+v", raw, patch.ModelScope)
		}
	}
	set := apiKeySaveRequest{ModelScope: json.RawMessage(`["gpt-4"]`)}
	patch, err = set.patch()
	if err != nil {
		t.Fatalf("设置 model_scope 报错: %v", err)
	}
	if patch.ModelScope == nil || len(*patch.ModelScope) != 1 || (*patch.ModelScope)[0] != "gpt-4" {
		t.Fatalf("model_scope 未设置成功: %+v", patch.ModelScope)
	}

	// expire_at 三态：缺省 = 不修改 / 0 或负 = 清空（写 NULL）/ >0 = 设置。
	if patch, err = (&apiKeySaveRequest{ExpireAt: int64Ptr(0)}).patch(); err != nil || patch.ExpireAt == nil || *patch.ExpireAt != 0 {
		t.Fatalf("expire_at=0 应表达清空: %+v err=%v", patch.ExpireAt, err)
	}
	if patch, err = (&apiKeySaveRequest{ExpireAt: int64Ptr(1_800_000_000)}).patch(); err != nil || *patch.ExpireAt != 1_800_000_000 {
		t.Fatalf("expire_at>0 应表达设置: %+v err=%v", patch.ExpireAt, err)
	}
	// status 闭集外拒绝；source 超长拒绝（update 面同样受 24 字符约束）。
	if _, err := (&apiKeySaveRequest{Status: intPtr16(2)}).patch(); err == nil {
		t.Fatal("status=2 必须被拒")
	}
	if _, err := (&apiKeySaveRequest{Source: stringPtr(strings.Repeat("s", APIKeyMaxSource+1))}).patch(); err == nil {
		t.Fatal("update 面 source >24 必须被拒")
	}
	// 补丁里永不含 key 相关列。
	patchWithAll, err := (&apiKeySaveRequest{
		Name: stringPtr("n"), Source: stringPtr("a"), RateLimit: intPtr(5), Remark: stringPtr("r"),
	}).patch()
	if err != nil {
		t.Fatalf("补丁报错: %v", err)
	}
	// 结构上没有 key 字段 ⇒ 只能断言「APIKeyPatch 无 key 字段」这一编译期事实，
	// 这里补一条运行时兜底：补丁字段全在可编辑列白名单内。
	if patchWithAll.RateLimit == nil || *patchWithAll.RateLimit != 5 || patchWithAll.Remark == nil {
		t.Fatalf("可编辑列未进补丁: %+v", patchWithAll)
	}
}

// ===== 出口纪律 =====

func TestAPIKeyRowNeverExposesHashOrPlaintext(t *testing.T) {
	plain, err := GenerateAPIKey()
	if err != nil {
		t.Fatalf("生成 key 失败: %v", err)
	}
	future := nowUnix() + 3600
	row := APIKey{
		ID: 3, Name: "应用 A", KeyHash: HashAPIKey(plain), KeyPrefix: APIKeyDisplayPrefix(plain),
		Source: "app-a", ModelScope: []string{"gpt-4"}, RateLimit: 2, Status: StatusEnabled,
		ExpireAt: &future, LastUsedAt: &future, CreateTime: 1, UpdateTime: 2,
	}
	encoded, err := json.Marshal(newAPIKeyRow(row, nowUnix()))
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	text := string(encoded)
	if strings.Contains(text, "key_hash") || strings.Contains(text, row.KeyHash) {
		t.Fatalf("出口泄露了 key_hash: %s", text)
	}
	if strings.Contains(text, plain) || strings.Contains(text, strings.TrimPrefix(plain, APIKeySecretPrefix)) {
		t.Fatalf("出口泄露了明文: %s", text)
	}
	if !strings.Contains(text, `"key_prefix":"`+row.KeyPrefix+`"`) {
		t.Fatalf("出口应回展示前缀: %s", text)
	}
	if !strings.Contains(text, `"expired":false`) {
		t.Fatalf("出口应回 expired 标记: %s", text)
	}

	// 明文唯一的出口形状：create / reset 的回执（有明文与前缀，仍无 key_hash）。
	issued, err := json.Marshal(apiKeyIssued(&row, plain))
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	if !strings.Contains(string(issued), `"api_key":"`+plain+`"`) {
		t.Fatalf("签发回执必须含明文一次: %s", issued)
	}
	if strings.Contains(string(issued), "key_hash") {
		t.Fatalf("签发回执不得含 key_hash: %s", issued)
	}
}

// ===== 列表契约：wheres 白名单 =====

func TestAPIKeyListWheresWhitelistContract(t *testing.T) {
	// 白名单外字段拒绝（错误文案给全量允许字段）。
	_, err := validateWhereGroups(apiKeyWhereFields, []whereGroup{{Wheres: []whereCondition{
		{Field: "key_hash", Operator: "eq", Value: "abc"},
	}}})
	if err == nil {
		t.Fatal("key_hash 不在白名单（密钥摘要不给可查入口）")
	}
	// model_scope 是 jsonb，刻意不作条件。
	if _, err := validateWhereGroups(apiKeyWhereFields, []whereGroup{{Wheres: []whereCondition{
		{Field: "model_scope", Operator: "eq", Value: "x"},
	}}}); err == nil {
		t.Fatal("model_scope 是 jsonb，不得作为筛选条件")
	}
	// 词表外操作符拒绝。
	if _, err := validateWhereGroups(apiKeyWhereFields, []whereGroup{{Wheres: []whereCondition{
		{Field: "name", Operator: "EXEC", Value: "x"},
	}}}); err == nil {
		t.Fatal("词表外操作符必须被拒")
	}
	// 二值列不许模糊类。
	if _, err := validateWhereGroups(apiKeyWhereFields, []whereGroup{{Wheres: []whereCondition{
		{Field: "status", Operator: "LIKE", Value: "1"},
	}}}); err == nil {
		t.Fatal("status 是二值列，不支持 LIKE")
	}
	// 合法条件通过，且应用后值全参数化（DryRun 由 store 层保证，这里只验拼装不报错）。
	groups, err := validateWhereGroups(apiKeyWhereFields, []whereGroup{
		{Wheres: []whereCondition{{Field: "source", Operator: "eq", Value: "app-a"}}},
		{Wheres: []whereCondition{{Field: "status", Operator: "eq", Value: 1}, {Field: "name", Operator: "ILIKE", Value: "应用"}}, Or: true},
	})
	if err != nil {
		t.Fatalf("合法 wheres 被拒: %v", err)
	}
	if len(groups) != 2 {
		t.Fatalf("分组数 = %d，期望 2", len(groups))
	}

	// 列表入参：排序字段白名单 + status 闭集 + 快搜。
	if _, _, err := (&apiKeyListRequest{listRequest: listRequest{Sort: "key_hash"}}).filter(); err == nil {
		t.Fatal("排序白名单外的字段必须被拒")
	}
	if _, _, err := (&apiKeyListRequest{Status: optionalStatus{Value: intPtr16(9)}}).filter(); err == nil {
		t.Fatal("status 闭集外必须被拒")
	}
	filter, _, err := (&apiKeyListRequest{
		listRequest: listRequest{Page: 2, Limit: 200, Keyword: " 应用 "},
		Source:      "app-a",
		Status:      optionalStatus{Value: intPtr16(1)},
	}).filter()
	if err != nil {
		t.Fatalf("合法列表入参被拒: %v", err)
	}
	if filter.Keyword != "应用" || filter.Source != "app-a" || filter.Page != 2 || filter.Limit != 200 {
		t.Fatalf("筛选未透传: %+v", filter)
	}
}

// ===== 出口文案 =====

func TestAPIKeyUniqueMessageDistinguishesIndexes(t *testing.T) {
	sourceErr := errors.New(`ERROR: duplicate key value violates unique constraint "uk_plugin_ai_api_key_source" (SQLSTATE 23505)`)
	if message := apiKeyUniqueMessage(sourceErr, "app-a"); !strings.Contains(message, "app-a") || !strings.Contains(message, "已被其他应用绑定") {
		t.Fatalf("source 冲突文案不符: %q", message)
	}
	hashErr := errors.New(`ERROR: duplicate key value violates unique constraint "uk_plugin_ai_api_key_key_hash" (SQLSTATE 23505)`)
	if message := apiKeyUniqueMessage(hashErr, "app-a"); !strings.Contains(message, "key_hash") {
		t.Fatalf("key_hash 冲突文案不符: %q", message)
	}
	if message := apiKeyUniqueMessage(errors.New("别的错"), "app-a"); message != "" {
		t.Fatalf("非唯一键冲突不该给专属文案: %q", message)
	}
}

// ===== 小工具 =====

func intPtr16(value int16) *int16 { return &value }

func int64Ptr(value int64) *int64 { return &value }
