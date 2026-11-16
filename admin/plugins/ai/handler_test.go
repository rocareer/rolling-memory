// plugin: ai v0.2.0
package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/ai-go-hub/ai-go-admin/internal/infra/config"
	"github.com/ai-go-hub/ai-go-admin/internal/infra/permission"
	"github.com/ai-go-hub/ai-go-admin/internal/plugin/manifest"

	"github.com/gin-gonic/gin"
)

// 本文件覆盖接口层在**无数据库**环境下的可判定行为：
//   - 鉴权：公开面 Bearer（401/503/放行）与后台面每条路由的 AdminAuth+AdminPermission 挂载；
//   - 参数校验：分页/排序/wheres 白名单与词表、渠道与模型入参收敛、单价解析、设置项边界；
//   - 脱敏：mask_secret 口径与「掩码不回写」；
//   - 接线的结构性事实：路由 ↔ 权限节点 ↔ plugin.yaml 三方一致（BuildCheckPath 运行时复核）；
//   - 公开面协议：OpenAI 形状回执、SSE 帧与 [DONE]、错误帧、请求体上限。
//
// 需要真库/真实上游的部分（DDL、migrate、install、熔断三态真库、safedial 实连接）不在本文件覆盖范围。

// ===== 测试脚手架 =====

// testDeps 无库依赖：配置由入参给出；store / 编排一律不可用（fail-closed 语义也因此被覆盖）。
//
// loadAuthStore 例外：公开面链（apiKeyAuth）在「出示了凭证」时必先查库，拿不到查库面即 503 fail-closed。
// 本夹具给的是一个**空档**（newFakeAuthStore 无行 ⇒ LookupAPIKeyByHash 恒 ErrRecordNotFound），
// 于是链继续走 v1 的全局密钥兜底路径——协议用例断言的 401/503/放行与改造前逐一相同，
// 而链路本身是生产的那一条（mountPublic）。DB Key 命中分支由 apikey_test.go / apikey_db_test.go 覆盖。
func testDeps(set pluginSettings) deps {
	unavailable := errors.New("测试环境无数据库")
	return deps{
		loadSettings:  func(context.Context) (pluginSettings, error) { return set, nil },
		loadStore:     func(context.Context) (*Store, error) { return nil, unavailable },
		loadService:   func(context.Context, pluginSettings) (*Service, error) { return nil, unavailable },
		loadAuthStore: func(context.Context) (apiKeyAuthStore, error) { return newFakeAuthStore(), nil },
	}
}

// engineDeps 注入假仓储与假驱动（公开面端到端用；仍不触碰数据库）。
func engineDeps(repo Repository, driver Driver, apiKey string) deps {
	set := parsePluginSettings(nil)
	set.APIKey = apiKey
	set.APIKeySet = apiKey != ""
	return deps{
		loadSettings: func(context.Context) (pluginSettings, error) { return set, nil },
		loadStore:    func(context.Context) (*Store, error) { return nil, errors.New("测试环境无数据库") },
		loadService: func(context.Context, pluginSettings) (*Service, error) {
			return NewService(repo, driver, Config{}, nil), nil
		},
		loadAuthStore: func(context.Context) (apiKeyAuthStore, error) { return newFakeAuthStore(), nil },
	}
}

func newAdminEngine(d deps) *gin.Engine {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	// 与宿主接线同形：admin 组 → plugin/<名> 子组（真实链路上还多一层 host.Gate，见 internal/plugins/wiring.go）。
	d.mountAdmin(engine.Group("/admin/plugin/" + Name))
	return engine
}

func newPublicEngine(d deps) *gin.Engine {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	d.mountPublic(engine.Group(""))
	return engine
}

// handlerNames 取处理器链的函数全名（用于断言中间件真的挂在链上）。
func handlerNames(handlers []gin.HandlerFunc) []string {
	names := make([]string, 0, len(handlers))
	for _, handler := range handlers {
		if handler == nil {
			names = append(names, "")
			continue
		}
		names = append(names, runtime.FuncForPC(reflect.ValueOf(handler).Pointer()).Name())
	}
	return names
}

func hasHandlerNamed(names []string, fragment string) bool {
	for _, name := range names {
		if strings.Contains(name, fragment) {
			return true
		}
	}
	return false
}

func doRequest(engine *gin.Engine, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)
	return recorder
}

// adminEnvelope 后台面的 httpx 回执（HTTP 200 + code/message/data）。
type adminEnvelope struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func decodeAdmin(t *testing.T, recorder *httptest.ResponseRecorder) adminEnvelope {
	t.Helper()
	var envelope adminEnvelope
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("后台回执不是 JSON: %v（原文 %s）", err, recorder.Body.String())
	}
	return envelope
}

// publicEnvelope 公开面的 OpenAI 形状错误体。
type publicEnvelope struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	} `json:"error"`
}

// ===== 脱敏 =====

func TestMaskSecretFollowsSourceCipher(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"空串（未配置）", "", ""},
		{"短密钥全长掩码", "abc12345", "********"},
		{"8 字符边界", "12345678", "********"},
		{"9 字符首尾各留 4", "123456789", "1234*6789"},
		{"典型密钥", "sk-abcdefghijklmnop", "sk-a***********mnop"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := maskSecret(tc.input); got != tc.want {
				t.Fatalf("maskSecret(%q) = %q，期望 %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestLooksMaskedRejectsEmptyAndMasked(t *testing.T) {
	if !looksMasked("") {
		t.Fatal("空串必须判为「不修改」")
	}
	if !looksMasked("sk-a***********mnop") {
		t.Fatal("含 '*' 的掩码必须判为「不修改」")
	}
	if looksMasked("sk-real-key") {
		t.Fatal("明文密钥必须判为「可写入」")
	}
}

func TestRedactSecretRemovesKeyFromErrorText(t *testing.T) {
	message := `ERROR: invalid input syntax: "sk-live-abcdefgh" (SQLSTATE 22P02)`
	redacted := redactSecret(message, "sk-live-abcdefgh")
	if strings.Contains(redacted, "sk-live-abcdefgh") {
		t.Fatalf("错误文案必须脱敏: %s", redacted)
	}
	if !strings.Contains(redacted, maskSecret("sk-live-abcdefgh")) {
		t.Fatalf("脱敏应替换成 mask_secret 同口径的掩码: %s", redacted)
	}
	if got := redactSecret("no secret here", "abc"); got != "no secret here" {
		t.Fatalf("过短候选不得参与替换: %s", got)
	}
}

func TestNewChannelRowNeverExposesPlainKey(t *testing.T) {
	// 插件设置为空（未配）⇒ 生效阈值应回落到代码默认；渠道显式冷却 7 优先于任何默认。
	row := newChannelRow(
		Channel{ID: 9, Name: "主渠道", APIKey: "sk-abcdefghijklmnop", FailThreshold: 0, CooldownSeconds: 7},
		pluginSettings{},
	)
	if row.APIKeyMasked != "sk-a***********mnop" {
		t.Fatalf("掩码不对: %q", row.APIKeyMasked)
	}
	if !row.HasAPIKey {
		t.Fatal("已配置密钥必须置 has_api_key")
	}
	encoded, err := json.Marshal(row)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	if strings.Contains(string(encoded), "sk-abcdefghijklmnop") {
		t.Fatalf("出口 JSON 泄漏了明文密钥: %s", encoded)
	}
	if row.EffectiveCooldownSeconds != 7 || row.EffectiveFailThreshold != DefaultFailThreshold {
		t.Fatalf("生效值回落不对: %+v", row)
	}
}

// TestChannelRowEffectiveValuesFollowPluginSettings 展示面与运行时走同一条求值链：
// 设置面改了阈值/冷却后，渠道列表/总览回显的 effective_* 必须跟着走（否则「设置回显一套、
// 生效面另一套」的缺陷会在接口层复现）。
func TestChannelRowEffectiveValuesFollowPluginSettings(t *testing.T) {
	set := parsePluginSettings(map[string]string{
		configKeyBreakerFailThreshold: "3",
		configKeyBreakerCooldown:      "90",
	})
	fallback := newChannelRow(Channel{ID: 1, FailThreshold: 0, CooldownSeconds: 0}, set)
	if fallback.EffectiveFailThreshold != 3 || fallback.EffectiveCooldownSeconds != 90 {
		t.Fatalf("渠道为 0 时应回显插件配置 3/90，实际: %+v", fallback)
	}
	explicit := newChannelRow(Channel{ID: 2, FailThreshold: 7, CooldownSeconds: 120}, set)
	if explicit.EffectiveFailThreshold != 7 || explicit.EffectiveCooldownSeconds != 120 {
		t.Fatalf("渠道显式值必须优先于插件配置: %+v", explicit)
	}
}

// ===== 插件配置解析 =====

func TestParsePluginSettingsDefaultsAndOverrides(t *testing.T) {
	defaults := parsePluginSettings(nil)
	if defaults.BreakerFailThreshold != DefaultFailThreshold ||
		defaults.BreakerCooldownSeconds != DefaultCooldownSeconds ||
		defaults.RequestTimeout != DefaultRequestTimeout ||
		defaults.StreamTimeout != DefaultStreamTimeout ||
		defaults.Currency != DefaultCurrency ||
		defaults.AllowPrivateHosts || defaults.APIKeySet {
		t.Fatalf("空配置的默认值不对: %+v", defaults)
	}

	overridden := parsePluginSettings(map[string]string{
		configKeyAPIKey:               "  sk-x  ",
		configKeyBreakerFailThreshold: "3",
		configKeyBreakerCooldown:      "90",
		configKeyRequestTimeout:       "900",
		configKeyStreamTimeout:        "600",
		configKeyCurrency:             "CNY",
		configKeyAllowPrivateHosts:    "1",
	})
	if overridden.APIKey != "sk-x" || !overridden.APIKeySet {
		t.Fatalf("密钥应去空白并置 APIKeySet: %+v", overridden)
	}
	if overridden.BreakerFailThreshold != 3 || overridden.BreakerCooldownSeconds != 90 {
		t.Fatalf("熔断配置未生效: %+v", overridden)
	}
	if overridden.RequestTimeout != 600 {
		t.Fatalf("request_timeout 必须收敛到 600: %d", overridden.RequestTimeout)
	}
	if overridden.StreamTimeout != 600 || overridden.Currency != "CNY" || !overridden.AllowPrivateHosts {
		t.Fatalf("流式/币种/私网开关未生效: %+v", overridden)
	}

	zeroed := parsePluginSettings(map[string]string{
		configKeyBreakerFailThreshold: "0",
		configKeyRequestTimeout:       "abc",
		configKeyAllowPrivateHosts:    "not-a-switch",
		configKeyAPIKey:               "   ",
	})
	if zeroed.BreakerFailThreshold != DefaultFailThreshold || zeroed.RequestTimeout != DefaultRequestTimeout {
		t.Fatalf("0/非法值必须回落默认: %+v", zeroed)
	}
	if zeroed.AllowPrivateHosts {
		t.Fatal("无法识别的开关值必须按 0（拒绝私网）处理")
	}
	if zeroed.APIKeySet {
		t.Fatal("纯空白密钥必须按未配置处理（公开面 503 fail-closed）")
	}
}

// TestSignatureCoversBreakerSettings 熔断阈值/冷却是注入引擎的全局默认（NewService 的
// Config → Circuit），必须进实例指纹：否则改了设置既不换实例、也不生效（「配置静默失效」）。
func TestSignatureCoversBreakerSettings(t *testing.T) {
	base := parsePluginSettings(map[string]string{
		configKeyBreakerFailThreshold: "5",
		configKeyBreakerCooldown:      "60",
	})
	for _, tc := range []struct {
		name   string
		mutate func(pluginSettings) pluginSettings
	}{
		{"阈值变化", func(s pluginSettings) pluginSettings { s.BreakerFailThreshold = 3; return s }},
		{"冷却变化", func(s pluginSettings) pluginSettings { s.BreakerCooldownSeconds = 90; return s }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.mutate(base).signature() == base.signature() {
				t.Fatalf("指纹未覆盖熔断设置，改动 %s 不会重建引擎实例", tc.name)
			}
		})
	}
	if base.signature() != parsePluginSettings(map[string]string{
		configKeyBreakerFailThreshold: "5",
		configKeyBreakerCooldown:      "60",
	}).signature() {
		t.Fatal("等价配置的指纹必须相同（否则每次读配置都重建实例）")
	}
}

// ===== wheres 高级检索 =====

func TestBuildWhereConditionWhitelistAndOperators(t *testing.T) {
	fields := map[string]whereField{
		"status": {Column: `"status"`, Kind: whereBool01},
		"name":   {Column: `"name"`, Kind: whereText},
		"weight": {Column: `"weight"`, Kind: whereInt},
	}

	if _, _, err := buildWhereCondition(fields, whereCondition{Field: "api_key", Operator: "eq", Value: "x"}); err == nil {
		t.Fatal("白名单外字段（api_key）必须被拒")
	}
	if _, _, err := buildWhereCondition(fields, whereCondition{Field: "name", Operator: "ilike", Value: "x"}); err == nil {
		t.Fatal("操作符大小写不符必须在词表外被拒")
	}
	if _, _, err := buildWhereCondition(fields, whereCondition{Field: "name", Operator: "ILIKE; DROP TABLE", Value: "x"}); err == nil {
		t.Fatal("注入型操作符必须被拒")
	}
	if _, _, err := buildWhereCondition(fields, whereCondition{Field: "weight", Operator: "ILIKE", Value: "x"}); err == nil {
		t.Fatal("整数列上的模糊操作符必须被拒")
	}
	if _, _, err := buildWhereCondition(fields, whereCondition{Field: "status", Operator: "gt", Value: 1}); err == nil {
		t.Fatal("二值列上的有序比较必须被拒")
	}
	if _, _, err := buildWhereCondition(fields, whereCondition{Field: "name", Operator: "eq", Value: nil}); err == nil {
		t.Fatal("null 值必须被拒（判定为空请用 NULL 操作符）")
	}

	sql, args, err := buildWhereCondition(fields, whereCondition{Field: "status", Operator: "eq", Value: float64(1)})
	if err != nil || sql != `"status" = ?` || len(args) != 1 || args[0] != int64(1) {
		t.Fatalf("等值条件拼装不对: sql=%q args=%v err=%v", sql, args, err)
	}

	sql, args, err = buildWhereCondition(fields, whereCondition{Field: "name", Operator: "ILIKE", Value: "gpt"})
	if err != nil || sql != `"name" ILIKE ?` || args[0] != "%gpt%" {
		t.Fatalf("模糊条件必须补前后通配（值仍参数化）: sql=%q args=%v err=%v", sql, args, err)
	}

	sql, args, err = buildWhereCondition(fields, whereCondition{Field: "name", Operator: "ILIKE", Value: "%raw%"})
	if err != nil || args[0] != "%raw%" {
		t.Fatalf("已含 %% 的值不得二次包装: args=%v err=%v", args, err)
	}

	sql, args, err = buildWhereCondition(fields, whereCondition{Field: "weight", Operator: "BETWEEN", Value: "1,9"})
	if err != nil || sql != `"weight" BETWEEN ? AND ?` || len(args) != 2 || args[0] != int64(1) || args[1] != int64(9) {
		t.Fatalf("BETWEEN 字符串形态拼装不对: sql=%q args=%v err=%v", sql, args, err)
	}

	sql, args, err = buildWhereCondition(fields, whereCondition{Field: "status", Operator: "IN", Value: []any{float64(0), float64(1)}})
	if err != nil || sql != `"status" IN (?, ?)` || len(args) != 2 {
		t.Fatalf("IN 数组形态拼装不对: sql=%q args=%v err=%v", sql, args, err)
	}

	sql, _, err = buildWhereCondition(fields, whereCondition{Field: "name", Operator: "NULL"})
	if err != nil || sql != `"name" IS NULL` {
		t.Fatalf("NULL 操作符拼装不对: sql=%q err=%v", sql, err)
	}
	sql, _, err = buildWhereCondition(fields, whereCondition{Field: "name", Operator: "NOT NULL"})
	if err != nil || sql != `"name" IS NOT NULL` {
		t.Fatalf("NOT NULL 操作符拼装不对: sql=%q err=%v", sql, err)
	}

	if _, _, err := buildWhereCondition(fields, whereCondition{Field: "weight", Operator: "IN", Value: ""}); err == nil {
		t.Fatal("空 IN 列表必须被拒")
	}
	if _, _, err := buildWhereCondition(fields, whereCondition{Field: "weight", Operator: "BETWEEN", Value: "1"}); err == nil {
		t.Fatal("BETWEEN 缺上界必须被拒")
	}
}

func TestWhereOperatorsCoverHostVocabulary(t *testing.T) {
	if len(whereOperators) != 16 || len(whereOperatorNames) != 16 {
		t.Fatalf("操作符词表必须是 16 项: %d / %d", len(whereOperators), len(whereOperatorNames))
	}
	for _, name := range whereOperatorNames {
		if _, ok := whereOperators[name]; !ok {
			t.Fatalf("词表名 %q 没有 SQL 映射", name)
		}
	}
}

func TestValidateWhereGroupsIgnoresEmptyGroups(t *testing.T) {
	groups, err := validateWhereGroups(channelWhereFields, []whereGroup{{Or: true}, {Wheres: []whereCondition{{Field: "name", Operator: "eq", Value: "x"}}}})
	if err != nil {
		t.Fatalf("合法 wheres 不应报错: %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("空组应按宿主管家口径忽略: %+v", groups)
	}
	if _, err := validateWhereGroups(channelWhereFields, []whereGroup{{Wheres: []whereCondition{{Field: "name", Operator: "MATCHES", Value: "x"}}}}); err == nil {
		t.Fatal("词表外操作符必须被拒")
	}
}

func TestValidateSortRejectsUnknownField(t *testing.T) {
	if err := validateSort("priority", channelSortColumns, "渠道"); err != nil {
		t.Fatalf("白名单内排序不应报错: %v", err)
	}
	if err := validateSort("", channelSortColumns, "渠道"); err != nil {
		t.Fatalf("缺省排序不应报错: %v", err)
	}
	if err := validateSort("api_key", channelSortColumns, "渠道"); err == nil {
		t.Fatal("白名单外排序必须被拒（静默回落会让调用方以为排序已生效）")
	}
}

func TestQuickSearchKeywordsAcceptsBothShapes(t *testing.T) {
	var single listRequest
	if err := json.Unmarshal([]byte(`{"quickSearchKeywords":"gpt"}`), &single); err != nil {
		t.Fatalf("字符串形态解析失败: %v", err)
	}
	if single.keyword() != "gpt" {
		t.Fatalf("字符串形态快搜未生效: %q", single.keyword())
	}

	var many listRequest
	if err := json.Unmarshal([]byte(`{"quickSearchKeywords":["","gpt-4"],"keyword":"fallback"}`), &many); err != nil {
		t.Fatalf("数组形态解析失败: %v", err)
	}
	if many.keyword() != "fallback" {
		t.Fatalf("顶层 keyword 优先: %q", many.keyword())
	}

	var noisy listRequest
	if err := json.Unmarshal([]byte(`{"quickSearchKeywords":{"bad":true}}`), &noisy); err != nil {
		t.Fatalf("异常形状不应让整个列表请求失败: %v", err)
	}
	if noisy.keyword() != "" {
		t.Fatalf("异常形状应退化为「快搜不生效」: %q", noisy.keyword())
	}
}

// ===== 渠道入参 =====

func TestChannelPatchNeverWritesBackMask(t *testing.T) {
	empty := ""
	if patch, err := (channelSaveRequest{APIKey: &empty}).patch(); err != nil || patch.APIKey != nil {
		t.Fatalf("空串 api_key 必须视为不修改: patch=%+v err=%v", patch, err)
	}
	masked := "sk-a***********mnop"
	if patch, err := (channelSaveRequest{APIKey: &masked}).patch(); err != nil || patch.APIKey != nil {
		t.Fatalf("含 '*' 的 api_key 必须视为不修改: patch=%+v err=%v", patch, err)
	}
	plain := "sk-new-key-value"
	patch, err := (channelSaveRequest{APIKey: &plain}).patch()
	if err != nil || patch.APIKey == nil || *patch.APIKey != plain {
		t.Fatalf("明文 api_key 必须写入: patch=%+v err=%v", patch, err)
	}
	if patch.Remark != nil {
		t.Fatal("缺省字段不得进补丁")
	}
}

func TestChannelPatchValidatesOnceSeen(t *testing.T) {
	badURL := "ftp://example.com"
	if _, err := (channelSaveRequest{BaseURL: &badURL}).patch(); err == nil {
		t.Fatal("非 http(s) 的 base_url 必须被拒")
	}
	privateURL := "http://127.0.0.1:11434"
	if _, err := (channelSaveRequest{BaseURL: &privateURL}).patch(); err != nil {
		t.Fatalf("写入期不拦私网地址（连接期由 safedial 拦）: %v", err)
	}
	badStatus := int16(2)
	if _, err := (channelSaveRequest{Status: &badStatus}).patch(); err == nil {
		t.Fatal("status=2 必须被拒（非 0/1 一律拒）")
	}
	badVendor := "unknown-vendor"
	if _, err := (channelSaveRequest{Vendor: &badVendor}).patch(); err == nil {
		t.Fatal("白名单外的 vendor 必须被拒")
	}
	if _, err := (channelSaveRequest{Name: stringPtr("   ")}).patch(); err == nil {
		t.Fatal("显式空白渠道名必须被拒")
	}
	timeout := 900
	weight := -3
	patch, err := (channelSaveRequest{Timeout: &timeout, Weight: &weight}).patch()
	if err != nil {
		t.Fatalf("数值收敛不应报错: %v", err)
	}
	if patch.Timeout == nil || *patch.Timeout != 600 {
		t.Fatalf("timeout 必须收敛到 600: %+v", patch.Timeout)
	}
	if patch.Weight == nil || *patch.Weight != 0 {
		t.Fatalf("weight 负数必须收敛为 0: %+v", patch.Weight)
	}
}

func TestChannelCreateRowDefaultsAndRequired(t *testing.T) {
	if _, err := (channelSaveRequest{}).channel(); err == nil {
		t.Fatal("缺 name/model/base_url 必须被拒")
	}
	name, model := " 主渠道 ", " gpt-4o "
	baseURL := "https://api.example.com/v1"
	status := int16(0)
	row, err := (channelSaveRequest{Name: &name, Model: &model, BaseURL: &baseURL, Status: &status}).channel()
	if err != nil {
		t.Fatalf("最小合法入参不应报错: %v", err)
	}
	if row.Name != "主渠道" || row.Model != "gpt-4o" {
		t.Fatalf("必填字段应去首尾空白: %+v", row)
	}
	if row.Vendor != "openai" || row.Timeout != DefaultRequestTimeout || row.Priority != 1 || row.Weight != 1 {
		t.Fatalf("缺省值不对: %+v", row)
	}
	if row.Status != StatusDisabled {
		t.Fatalf("显式 status=0 必须保留: %d", row.Status)
	}
	if row.APIKey != "" {
		t.Fatal("api_key 允许为空（调用时 fail-closed）")
	}
	if row.FailThreshold != 0 || row.CooldownSeconds != 0 {
		t.Fatal("熔断阈值/冷却缺省必须是 0（= 取插件配置）")
	}
}

func TestProbeTimeoutIsClampedTo3And15(t *testing.T) {
	cases := map[int]int{0: 3, 1: 3, 3: 3, 10: 10, 15: 15, 60: 15, 600: 15}
	for input, want := range cases {
		if got := probeTimeoutSeconds(input); got != want {
			t.Fatalf("probeTimeoutSeconds(%d) = %d，期望 %d", input, got, want)
		}
	}
}

// ===== 模型入参 =====

func TestModelCreateCoercesWhitelistFields(t *testing.T) {
	name := "gpt-4o"
	badType := "audio"
	badStrategy := "round-robin"
	badStatus := int16(7)
	badEncoding := "cl100k"
	window := -5
	var capabilities capabilityList
	if err := capabilities.UnmarshalJSON([]byte(`"chat, function_calling ,chat"`)); err != nil {
		t.Fatalf("逗号串 capabilities 解析失败: %v", err)
	}
	row, err := (modelSaveRequest{
		Name:          &name,
		Type:          &badType,
		Strategy:      &badStrategy,
		Status:        &badStatus,
		Encoding:      &badEncoding,
		ContextWindow: &window,
		Capabilities:  &capabilities,
	}).model()
	if err != nil {
		t.Fatalf("白名单收敛不应报错: %v", err)
	}
	if row.Type != ModelTypeChat || row.Strategy != StrategyPriority || row.Status != StatusEnabled {
		t.Fatalf("非法枚举必须回落 chat/priority/1: %+v", row)
	}
	if row.Encoding != EncodingNone {
		t.Fatalf("非闭集 encoding 必须置空: %q", row.Encoding)
	}
	if row.ContextWindow != 0 {
		t.Fatalf("context_window 负数必须收敛为 0: %d", row.ContextWindow)
	}
	if row.PriceInput != "0.000000" || row.PriceOutput != "0.000000" {
		t.Fatalf("缺省单价必须是 0.000000: %q / %q", row.PriceInput, row.PriceOutput)
	}
	if len(row.Capabilities) != 2 || row.Capabilities[0] != "chat" || row.Capabilities[1] != "function_calling" {
		t.Fatalf("capabilities 必须去空白去重后落库: %+v", row.Capabilities)
	}
}

// TestModelTypeClosureKeepsTTSAndRejectsOthers 是闭集扩展（TTS 增量）的接口层断言：
//
//	① model.type 闭集 = chat|embedding|tts ⇒ 'tts' 必须**保留**（不再回落 chat，源侧口径：
//	   admin/model/AiModel.php:21 的 TYPE_TTS 与 admin/controller/ai/Model.php:234 的白名单）；
//	② 闭集外（如 'audio'/'speech'）仍回落 chat，**不静默放行**；
//	③ 列表筛选顶层 type 同口径（'tts' 放行、闭集外被拒）。
//
// 显式限定：本断言只在**接口层**（纯函数），不主张任何真库断言——此时 DB CHECK 仍是
// migrations/1_init.up.sql:111 的 chat|embedding，真库落 type='tts' 由迁移 6 + voice_db_test 覆盖。
func TestModelTypeClosureKeepsTTSAndRejectsOthers(t *testing.T) {
	tts := ModelTypeTTS
	if got := normalizeModelType(&tts); got != ModelTypeTTS {
		t.Fatalf("'tts' 是闭集内取值，必须保留，实际回落成 %q", got)
	}
	for _, unknown := range []string{"audio", "speech", "image", ""} {
		value := unknown
		if got := normalizeModelType(&value); got != ModelTypeChat {
			t.Fatalf("闭集外的 type %q 必须回落 chat，实际 %q", unknown, got)
		}
	}

	if filter, _, err := (modelListRequest{Type: "tts"}).filter(); err != nil {
		t.Fatalf("列表筛选 type='tts' 应被接受: %v", err)
	} else if filter.Type == nil || *filter.Type != ModelTypeTTS {
		t.Fatalf("列表筛选 type='tts' 未生效: %+v", filter.Type)
	}
	if _, _, err := (modelListRequest{Type: "audio"}).filter(); err == nil {
		t.Fatal("列表筛选闭集外的 type 必须被拒（不得静默查空）")
	}
}

func TestModelCapabilitiesAcceptsArrayAndCommaString(t *testing.T) {
	var arrayShape modelSaveRequest
	if err := json.Unmarshal([]byte(`{"capabilities":["chat","function_calling"]}`), &arrayShape); err != nil {
		t.Fatalf("数组形态解析失败: %v", err)
	}
	if arrayShape.Capabilities == nil || len(*arrayShape.Capabilities) != 2 {
		t.Fatalf("数组形态未被识别: %+v", arrayShape.Capabilities)
	}
	var commaShape modelSaveRequest
	if err := json.Unmarshal([]byte(`{"capabilities":"chat, function_calling"}`), &commaShape); err != nil {
		t.Fatalf("逗号串形态解析失败: %v", err)
	}
	if commaShape.Capabilities == nil || len(*commaShape.Capabilities) != 2 || (*commaShape.Capabilities)[1] != " function_calling" {
		t.Fatalf("逗号串形态未被识别: %+v", commaShape.Capabilities)
	}
	var absent modelSaveRequest
	if err := json.Unmarshal([]byte(`{}`), &absent); err != nil {
		t.Fatalf("空体解析失败: %v", err)
	}
	if absent.Capabilities != nil {
		t.Fatal("字段缺省必须是「不修改」而不是「清空」")
	}
	if len(normalizeCapabilities([]string{"chat", "chat", " ", "code"})) != 2 {
		t.Fatal("capabilities 必须去空白去重")
	}
}

func TestModelPriceParsingRejectsNaNInfAndNormalizes(t *testing.T) {
	for _, raw := range []string{"NaN", "Inf", "-Inf", "NaN,", "abc", "1.2.3", "-1"} {
		value := any(raw)
		if _, err := (modelSaveRequest{PriceInput: &value}).patch(); err == nil {
			t.Fatalf("单价 %q 必须被拒（PG 把 NaN 视为大于一切数值）", raw)
		}
	}
	value := any("0.1234567")
	patch, err := (modelSaveRequest{PriceInput: &value}).patch()
	if err != nil {
		t.Fatalf("6 位以上小数应按 half-away-from-zero 舍入: %v", err)
	}
	if patch.PriceInput == nil || *patch.PriceInput != "0.123457" {
		t.Fatalf("单价未按 6 位归一: %+v", patch.PriceInput)
	}
	emptied := any("")
	patch, err = (modelSaveRequest{PriceOutput: &emptied}).patch()
	if err != nil || patch.PriceOutput == nil || *patch.PriceOutput != "0.000000" {
		t.Fatalf("清空单价应写 0.000000: %+v err=%v", patch.PriceOutput, err)
	}
	if _, err := (modelSaveRequest{}).patch(); err != nil {
		t.Fatalf("空补丁不应报错: %v", err)
	}
}

// ===== 用量汇总 / 日志 =====

func TestSummarizeUsageBucketsAndExactCost(t *testing.T) {
	rows := []Usage{
		{Status: UsageStatusSuccess, TotalTokens: 10, Cost: "0.000100"},
		{Status: UsageStatusSuccess, TotalTokens: 5, Cost: "0.000200"},
		{Status: UsageStatusTimeout, TotalTokens: 3, Cost: "0.000050"},
		{Status: UsageStatusFail, TotalTokens: 2, Cost: "0.000010"},
		{Status: UsageStatusBreaker, TotalTokens: 0, Cost: "0"},
	}
	summary := summarizeUsage(rows)
	if summary.Calls != 5 || summary.Success != 2 || summary.Timeout != 1 || summary.Fail != 2 {
		t.Fatalf("桶定义不对（fail 不含 timeout；breaker 计入 fail）: %+v", summary)
	}
	if summary.Success+summary.Fail+summary.Timeout != summary.Calls {
		t.Fatalf("success+fail+timeout 必须等于 calls: %+v", summary)
	}
	if summary.TotalTokens != 20 {
		t.Fatalf("token 求和不对: %d", summary.TotalTokens)
	}
	if summary.Cost != "0.000360" {
		t.Fatalf("成本必须精确求和成 6 位小数字符串: %q", summary.Cost)
	}
}

func TestFormatCostKeepsExactDecimals(t *testing.T) {
	cases := map[string]string{"0": "0.000000", "0.1234": "0.123400", "12.5": "12.500000", "": ""}
	for input, want := range cases {
		if got := formatCost(input); got != want {
			t.Fatalf("formatCost(%q) = %q，期望 %q", input, got, want)
		}
	}
}

func TestUsageListRequestGroupWhitelist(t *testing.T) {
	for _, group := range usageGroups {
		if _, err := (usageListRequest{Group: group}).query(); err != nil {
			t.Fatalf("维度 %q 应被接受: %v", group, err)
		}
	}
	if query, err := (usageListRequest{}).query(); err != nil || query.Group != "model" {
		t.Fatalf("缺省维度应为 model: %+v err=%v", query, err)
	}
	if _, err := (usageListRequest{Group: "channel"}).query(); err == nil {
		t.Fatal("闭集外维度必须被拒")
	}
	start, end := int64(200), int64(100)
	if _, err := (usageListRequest{Start: &start, End: &end}).query(); err == nil {
		t.Fatal("下界大于上界必须被拒")
	}
}

func TestUsageLogFilterValidatesClosedSets(t *testing.T) {
	if _, _, err := (usageLogListRequest{Status: "bogus"}).filter(); err == nil {
		t.Fatal("闭集外的 status 必须被拒")
	}
	// 闭集扩展（TTS 增量）后 'tts' 必须放行（源侧 ai_usage.call_type 已 ADD VALUE 'tts'，
	// database/migrations/20260909155604_radmin_ai_voice.php:33；DB CHECK 由迁移 6 落地）。
	ttsFilter, _, err := (usageLogListRequest{CallType: CallTypeTTS}).filter()
	if err != nil {
		t.Fatalf("call_type='tts' 已在闭集内，必须放行: %v", err)
	}
	if ttsFilter.CallType != CallTypeTTS {
		t.Fatalf("call_type='tts' 未透传进筛选结构: %+v", ttsFilter)
	}
	// 'asr' 是源侧存在但当前刻意不收的取值（v1 无语音转写入口）⇒ 仍必须被拒。
	for _, outside := range []string{"asr", "bogus"} {
		if _, _, err := (usageLogListRequest{CallType: outside}).filter(); err == nil {
			t.Fatalf("闭集外的 call_type %q 必须被拒", outside)
		}
	}
	if _, _, err := (usageLogListRequest{TokensSource: "something"}).filter(); err == nil {
		t.Fatal("闭集外的 tokens_source 必须被拒")
	}
	// 源侧传 'internal'、网关传 'key:gateway' 都在前端闭集之外 ⇒ biz/source 只判空、不做闭集校验。
	filter, _, err := (usageLogListRequest{BizType: "api", Source: "key:gateway"}).filter()
	if err != nil || filter.Source != "key:gateway" || filter.BizType != "api" {
		t.Fatalf("biz/source 必须按自由文本放行: %+v err=%v", filter, err)
	}
}

// ===== 设置项 =====

func TestSettingsSaveTriStateAndBounds(t *testing.T) {
	empty := ""
	updates, err := (settingsSaveRequest{APIKey: &empty}).updates()
	if err != nil || updates[configKeyAPIKey] != "" {
		t.Fatalf("显式空串必须落成清空: %+v err=%v", updates, err)
	}
	// 非空密钥必须去空白后写入。注：非空但 <16 字符 ⇒ 拒收的那三态断言归
	// handler_overview_test.go（settingsSaveRequest.updates 的归属方）；此处只留 ≥16 的放行腿
	// （原先用 6 字符 "sk-new" 钉死旧契约，与「≥16」新口径相告，故改写）。
	value := "  sk-abcdefghijklmnop  "
	updates, err = (settingsSaveRequest{APIKey: &value}).updates()
	if err != nil || updates[configKeyAPIKey] != "sk-abcdefghijklmnop" {
		t.Fatalf("非空密钥必须去空白后写入: %+v err=%v", updates, err)
	}
	if _, err := (settingsSaveRequest{}).updates(); err == nil {
		t.Fatal("没有任何键时必须报「没有需要保存的设置项」")
	}
	if _, err := (settingsSaveRequest{APIKey: nil, RequestTimeout: intPtr(601)}).updates(); err == nil {
		t.Fatal("request_timeout 超上界必须被拒")
	}
	if _, err := (settingsSaveRequest{AllowPrivateHosts: intPtr(2)}).updates(); err == nil {
		t.Fatal("allow_private_hosts 只能是 0/1")
	}
	blank := "   "
	if _, err := (settingsSaveRequest{Currency: &blank}).updates(); err == nil {
		t.Fatal("空币种必须被拒")
	}
	updates, err = (settingsSaveRequest{BreakerFailThreshold: intPtr(0), AllowPrivateHosts: intPtr(1)}).updates()
	if err != nil || updates[configKeyBreakerFailThreshold] != "0" || updates[configKeyAllowPrivateHosts] != "1" {
		t.Fatalf("0 与开关位必须落成字符串: %+v err=%v", updates, err)
	}
}

// ===== 路由结构：中间件 / 权限节点 / 清单三方一致 =====

func TestAdminRoutesMountAuthAndPermission(t *testing.T) {
	routes := defaultDeps.adminRoutes()
	if len(routes) != 43 {
		t.Fatalf("后台路由应为 43 条（基础 20 + 扩展：厂商 5 / 应用接入 7 / 任务 4 / 语音 6 / 体检 1），实际 %d 条", len(routes))
	}
	seen := make(map[string]bool, len(routes))
	for _, route := range routes {
		key := route.Method + " " + route.Path
		if seen[key] {
			t.Fatalf("路由重复: %s", key)
		}
		seen[key] = true

		names := handlerNames(route.chain())
		if len(names) != 3 {
			t.Fatalf("%s 的处理器链应为 3 段（AdminAuth + AdminPermission + 业务），实际 %d 段", key, len(names))
		}
		// 运行时函数名即中间件闭包名（形如 ...middleware.AdminAuth.func1；内联时前缀会带上调用点，
		// 故按「含 AdminAuth / AdminPermission」判定）。
		if !hasHandlerNamed(names, "AdminAuth") {
			t.Errorf("%s 未挂 AdminAuth（任何持有效会话者都能直打）: %v", key, names)
		}
		if !hasHandlerNamed(names, "AdminPermission") {
			t.Errorf("%s 未挂 AdminPermission（manifest 的节点会变成装饰）: %v", key, names)
		}
		if route.Node == "" {
			t.Errorf("%s 缺权限节点声明", key)
		}
	}
}

func TestAdminRoutesRejectAnonymousRequests(t *testing.T) {
	d := testDeps(parsePluginSettings(nil))
	engine := newAdminEngine(d)
	for _, route := range d.adminRoutes() {
		path := "/admin/plugin/ai" + strings.ReplaceAll(route.Path, ":pk", "1")
		recorder := doRequest(engine, route.Method, path, "{}", nil)
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s %s：后台面必须沿用宿主口径 HTTP 200，实际 %d", route.Method, path, recorder.Code)
		}
		envelope := decodeAdmin(t, recorder)
		if envelope.Code != http.StatusUnauthorized || envelope.Message != "请先登录" {
			t.Fatalf("%s %s：未带凭证应被 AdminAuth 拒（code=401/请先登录），实际 %+v", route.Method, path, envelope)
		}

		nonBearer := doRequest(engine, route.Method, path, "{}", map[string]string{"Authorization": "Basic abc"})
		if envelope := decodeAdmin(t, nonBearer); envelope.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s：非 Bearer 前缀必须被拒，实际 %+v", route.Method, path, envelope)
		}
	}
}

func TestAdminRouteNodesMatchManifestAndCheckPath(t *testing.T) {
	// 测试进程的 CWD 就是插件目录：IgnoreDirName 跳过「目录名 == name」（该不变式由仓级门禁
	// check-plugin-manifest.sh 在仓库树上校验），web/migrations 的源路径存在性仍照常校验。
	m, _, err := manifest.Load(".", manifest.Options{PluginDir: ".", IgnoreDirName: true})
	if err != nil {
		t.Fatalf("读取 plugin.yaml 失败: %v", err)
	}
	declared := make(map[string]bool)
	for _, menu := range m.Menus {
		if menu.Type == "node" {
			declared[menu.Name] = true
		}
	}
	if len(declared) != 37 {
		t.Fatalf("manifest 应声明 37 个权限节点（基础 18 + 扩展 19：厂商 4 / 应用接入 6 / 任务 3 / 语音 5 / 体检 1），实际 %d 个", len(declared))
	}

	used := make(map[string]bool)
	for _, route := range defaultDeps.adminRoutes() {
		if !declared[route.Node] {
			t.Errorf("路由 %s %s 的节点 %q 不在 plugin.yaml 里（非超管将 100%% 403）", route.Method, route.Path, route.Node)
		}
		used[route.Node] = true
	}
	for name := range declared {
		if !used[name] {
			t.Errorf("plugin.yaml 的节点 %q 没有任何路由使用", name)
		}
	}

	if !config.Initialized() {
		if err := config.Init(); err != nil {
			t.Fatalf("初始化配置失败: %v", err)
		}
	}
	base := strings.TrimSuffix(config.Get().Server.AdminBaseRoutePath, "/")
	for _, route := range defaultDeps.adminRoutes() {
		full := base + "/plugin/" + Name + route.Path
		checkPath := permission.BuildCheckPath(full)
		if checkPath == route.Node {
			continue
		}
		// 末段为 list|get 时，AdminPermission 按 [checkPath, 同前缀/read] OR 判定
		//（internal/middleware/admin_permission.go:33-42）⇒ 只声明 read 即覆盖 list 与 get。
		if index := strings.LastIndex(checkPath, "/"); index > 0 {
			last := checkPath[index+1:]
			if (last == "list" || last == "get") && route.Node == checkPath[:index]+"/read" {
				continue
			}
		}
		t.Errorf("BuildCheckPath(%s) = %q，与声明的节点 %q 不一致（也不构成 list|get 的 read 回落）", full, checkPath, route.Node)
	}
}

func TestPublicRoutesAreExplicitlyMounted(t *testing.T) {
	routes := defaultDeps.publicRoutes()
	if len(routes) != 4 {
		t.Fatalf("公开路由应为 4 条（chat/completions、embeddings、models、audio/speech），实际 %d 条", len(routes))
	}
	engine := newPublicEngine(testDeps(parsePluginSettings(nil)))
	registered := make(map[string]bool)
	for _, route := range engine.Routes() {
		registered[route.Method+" "+route.Path] = true
	}
	for _, route := range routes {
		if !registered[route.Method+" "+route.Path] {
			t.Fatalf("公开路由未注册: %s %s", route.Method, route.Path)
		}
	}
	// 根级、无后台前缀：带 /admin 前缀的路径必须不存在。
	if registered["POST /admin/ai/v1/chat/completions"] {
		t.Fatal("公开路由不得带后台前缀")
	}
}

// ===== 公开面鉴权 =====

func TestPublicBearerAuthStatuses(t *testing.T) {
	const key = "sk-test-abcdefgh"
	cases := []struct {
		name       string
		configured string
		header     string
		wantStatus int
		wantCode   string
	}{
		{"缺 Authorization", key, "", http.StatusUnauthorized, "invalid_api_key"},
		{"前缀非 Bearer", key, "Basic " + key, http.StatusUnauthorized, "invalid_api_key"},
		{"只有前缀", key, "Bearer", http.StatusUnauthorized, "invalid_api_key"},
		{"key 不匹配", key, "Bearer sk-other", http.StatusUnauthorized, "invalid_api_key"},
		{"key 未配置", "", "Bearer " + key, http.StatusServiceUnavailable, "api_key_not_configured"},
		{"key 为纯空白", "   ", "Bearer " + key, http.StatusServiceUnavailable, "api_key_not_configured"},
		{"key 正确", key, "Bearer " + key, http.StatusOK, ""},
		{"前缀大小写不敏感", key, "bearer " + key, http.StatusOK, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			set := parsePluginSettings(nil)
			set.APIKey = strings.TrimSpace(tc.configured)
			set.APIKeySet = set.APIKey != ""
			d := testDeps(set)
			gin.SetMode(gin.TestMode)
			engine := gin.New()
			engine.POST("/ai/v1/chat/completions", d.bearerAuth(), func(c *gin.Context) {
				c.JSON(http.StatusOK, gin.H{"reached": true})
			})
			headers := map[string]string{}
			if tc.header != "" {
				headers["Authorization"] = tc.header
			}
			recorder := doRequest(engine, http.MethodPost, "/ai/v1/chat/completions", `{}`, headers)
			if recorder.Code != tc.wantStatus {
				t.Fatalf("状态码 = %d，期望 %d（body=%s）", recorder.Code, tc.wantStatus, recorder.Body.String())
			}
			if tc.wantCode == "" {
				if !strings.Contains(recorder.Body.String(), `"reached":true`) {
					t.Fatalf("正确密钥必须放行到业务处理器: %s", recorder.Body.String())
				}
				return
			}
			var envelope publicEnvelope
			if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
				t.Fatalf("错误体不是 JSON: %v", err)
			}
			if envelope.Error.Code != tc.wantCode {
				t.Fatalf("错误码 = %q，期望 %q", envelope.Error.Code, tc.wantCode)
			}
			if strings.Contains(recorder.Body.String(), key) {
				t.Fatalf("错误体不得回显密钥: %s", recorder.Body.String())
			}
		})
	}
}

func TestPublicBearerAuthTakesEffectOnKeyRotation(t *testing.T) {
	current := parsePluginSettings(nil)
	current.APIKey = "sk-old-key-value"
	current.APIKeySet = true

	d := deps{
		loadSettings: func(context.Context) (pluginSettings, error) { return current, nil },
		loadStore:    testDeps(current).loadStore,
		loadService:  testDeps(current).loadService,
	}
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.GET("/ai/v1/models", d.bearerAuth(), func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })

	if recorder := doRequest(engine, http.MethodGet, "/ai/v1/models", "", map[string]string{"Authorization": "Bearer sk-old-key-value"}); recorder.Code != http.StatusOK {
		t.Fatalf("旧密钥应放行: %d", recorder.Code)
	}
	// 密钥轮换（settings/save 会失效缓存；此处模拟缓存已失效后的下一次读取）
	current.APIKey, current.APIKeySet = "sk-new-key-value", true
	if recorder := doRequest(engine, http.MethodGet, "/ai/v1/models", "", map[string]string{"Authorization": "Bearer sk-old-key-value"}); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("轮换后旧密钥必须立即失效: %d", recorder.Code)
	}
	if recorder := doRequest(engine, http.MethodGet, "/ai/v1/models", "", map[string]string{"Authorization": "Bearer sk-new-key-value"}); recorder.Code != http.StatusOK {
		t.Fatalf("轮换后新密钥必须放行: %d", recorder.Code)
	}
}

// ===== 公开面参数校验 =====

func TestPublicChatRejectsInvalidRequests(t *testing.T) {
	const key = "sk-test-key-1234"
	d := testDeps(parsePluginSettings(nil))
	d.loadSettings = func(context.Context) (pluginSettings, error) {
		set := parsePluginSettings(nil)
		set.APIKey, set.APIKeySet = key, true
		return set, nil
	}
	engine := newPublicEngine(d)
	auth := map[string]string{"Authorization": "Bearer " + key}

	cases := []struct {
		name     string
		body     string
		wantCode string
	}{
		{"请求体不是 JSON", "{", "invalid_request_error"},
		{"请求体不是对象", `[]`, "invalid_request_error"},
		{"缺 model", `{"messages":[{"role":"user","content":"hi"}]}`, "invalid_request_error"},
		{"缺 messages", `{"model":"gpt"}`, "invalid_request_error"},
		{"messages 为空数组", `{"model":"gpt","messages":[]}`, "invalid_request_error"},
		{"消息缺 role", `{"model":"gpt","messages":[{"content":"hi"}]}`, "invalid_request_error"},
		{"stream 非布尔", `{"model":"gpt","messages":[{"role":"user","content":"hi"}],"stream":"yes"}`, "invalid_request_error"},
		{"temperature 非数字", `{"model":"gpt","messages":[{"role":"user","content":"hi"}],"temperature":"hot"}`, "invalid_request_error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := doRequest(engine, http.MethodPost, "/ai/v1/chat/completions", tc.body, auth)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("状态码 = %d，期望 400（body=%s）", recorder.Code, recorder.Body.String())
			}
			var envelope publicEnvelope
			if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
				t.Fatalf("错误体不是 JSON: %v", err)
			}
			if envelope.Error.Code != tc.wantCode || envelope.Error.Type != "invalid_request_error" {
				t.Fatalf("错误体不是 OpenAI 形状: %+v", envelope)
			}
		})
	}

	// 合法入参但库不可用 ⇒ 503（fail-closed，且绝不是 200+code=1 的宿主形状）
	recorder := doRequest(engine, http.MethodPost, "/ai/v1/chat/completions",
		`{"model":"gpt","messages":[{"role":"user","content":"hi"}]}`, auth)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("库不可用必须 503，实际 %d（body=%s）", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), `"code":1`) {
		t.Fatalf("公开面不得沿用 httpx 的 HTTP 200 + code=1 形状: %s", recorder.Body.String())
	}
}

func TestPublicRequestTooLargeIs413(t *testing.T) {
	const key = "sk-test-key-1234"
	d := testDeps(parsePluginSettings(nil))
	d.loadSettings = func(context.Context) (pluginSettings, error) {
		set := parsePluginSettings(nil)
		set.APIKey, set.APIKeySet = key, true
		return set, nil
	}
	engine := newPublicEngine(d)

	// 8MiB 上限：超 1 字节即 413，且不回显原文。
	oversized := `{"model":"gpt","messages":[{"role":"user","content":"` + strings.Repeat("a", maxPublicBodyBytes) + `"}]}`
	recorder := doRequest(engine, http.MethodPost, "/ai/v1/chat/completions", oversized,
		map[string]string{"Authorization": "Bearer " + key})
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("超限请求必须 413，实际 %d", recorder.Code)
	}
	var envelope publicEnvelope
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("错误体不是 JSON: %v", err)
	}
	if envelope.Error.Code != "request_too_large" {
		t.Fatalf("错误码 = %q，期望 request_too_large", envelope.Error.Code)
	}
	if strings.Contains(recorder.Body.String(), "aaaa") {
		t.Fatal("超限回执不得回显请求体内容")
	}
}

func TestPublicEmbeddingsInputRules(t *testing.T) {
	const key = "sk-test-key-1234"
	d := testDeps(parsePluginSettings(nil))
	d.loadSettings = func(context.Context) (pluginSettings, error) {
		set := parsePluginSettings(nil)
		set.APIKey, set.APIKeySet = key, true
		return set, nil
	}
	engine := newPublicEngine(d)
	auth := map[string]string{"Authorization": "Bearer " + key}

	items := make([]string, 0, maxPublicEmbeddingInput)
	for i := 0; i < maxPublicEmbeddingInput; i++ {
		items = append(items, fmt.Sprintf("text-%d", i))
	}
	encoded, err := json.Marshal(items)
	if err != nil {
		t.Fatalf("构造入参失败: %v", err)
	}
	// 上限内：通过校验后落到「库不可用」⇒ 503（证明没有在参数层被拒）
	recorder := doRequest(engine, http.MethodPost, "/ai/v1/embeddings",
		`{"model":"bge","input":`+string(encoded)+`}`, auth)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("上限内的 input 不应被参数层拒绝，实际 %d（body=%s）", recorder.Code, recorder.Body.String())
	}

	tooMany := append(items, "extra")
	encoded, _ = json.Marshal(tooMany)
	recorder = doRequest(engine, http.MethodPost, "/ai/v1/embeddings",
		`{"model":"bge","input":`+string(encoded)+`}`, auth)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("超限 input 必须 400，实际 %d", recorder.Code)
	}
	var envelope publicEnvelope
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("错误体不是 JSON: %v", err)
	}
	if envelope.Error.Code != "input_too_large" {
		t.Fatalf("错误码 = %q，期望 input_too_large", envelope.Error.Code)
	}

	recorder = doRequest(engine, http.MethodPost, "/ai/v1/embeddings", `{"model":"bge","input":[1,2]}`, auth)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("token 数组必须被拒（v1 只接受字符串）: %d", recorder.Code)
	}
}

// ===== 公开面端到端（假仓储 + 假驱动，不触碰数据库）=====

// fakeRepository 是 Repository 的无库假实现：读面返回注入值，写面 no-op 但记录 usage。
//
// markFailureCalls / openCircuitCalls 是熔断写面的调用计数：断言「分支③（有块、无收尾）不增熔断计数」
// 需要一个可观察的探针（真库里 consecutive_failures 由 store.MarkFailure 自增，这里等价地数调用次数）。
type fakeRepository struct {
	model            Model
	candidates       []Candidate
	usages           []Usage
	markFailureCalls int
	openCircuitCalls int
}

func (f *fakeRepository) FindModel(_ context.Context, name string) (Model, error) {
	if f.model.Name == "" || f.model.Name != name {
		return Model{}, ErrModelNotFound
	}
	return f.model, nil
}

func (f *fakeRepository) Candidates(context.Context, uint64) ([]Candidate, error) {
	return f.candidates, nil
}

func (f *fakeRepository) UpsertUsage(_ context.Context, usage *Usage) error {
	if usage != nil {
		f.usages = append(f.usages, *usage)
	}
	return nil
}

func (f *fakeRepository) EnterCAS(context.Context, uint64, int64) (bool, error) { return true, nil }

func (f *fakeRepository) ReselectStaleProbe(context.Context, uint64, int64, int64) (bool, error) {
	return true, nil
}

func (f *fakeRepository) MarkFailure(context.Context, uint64, string, int64) (int, error) {
	f.markFailureCalls++
	return 1, nil
}

func (f *fakeRepository) MarkSuccess(context.Context, uint64, int64) (bool, error) { return true, nil }

func (f *fakeRepository) OpenCircuit(context.Context, uint64, int64) (bool, error) {
	f.openCircuitCalls++
	return true, nil
}

func (f *fakeRepository) ManualOpenCircuit(context.Context, uint64, int64) (bool, error) {
	return true, nil
}

func (f *fakeRepository) ManualCloseCircuit(context.Context, uint64, int64) (int64, error) {
	return 1, nil
}

func (f *fakeRepository) AddChannelTotals(context.Context, uint64, bool, int, int64) error {
	return nil
}

func (f *fakeRepository) InsertBreakerEvent(context.Context, *BreakerEvent) error { return nil }

// fakeDriver 是 Driver 的无网假实现：流式沿用包内 SSE 解析，保证帧格式与真实驱动一致。
type fakeDriver struct {
	chatResult   *ChatResult
	chatErr      error
	streamChunks []string
	streamErr    error
	embedding    *EmbeddingResult
}

func (f *fakeDriver) Chat(context.Context, Channel, ChatRequest) (*ChatResult, error) {
	if f.chatErr != nil {
		return nil, f.chatErr
	}
	return f.chatResult, nil
}

func (f *fakeDriver) ChatStream(_ context.Context, _ Channel, _ ChatRequest, sink func([]byte) error) (*ChatResult, error) {
	accumulator := &StreamAccumulator{}
	for _, chunk := range f.streamChunks {
		parsed, err := ParseChatChunk([]byte(chunk))
		if err != nil {
			return accumulator.Result(), err
		}
		accumulator.Apply(parsed)
		if sink != nil {
			if err := sink([]byte(chunk)); err != nil {
				accumulator.Aborted = true
				return accumulator.Result(), &StreamAbortedError{Err: err}
			}
		}
	}
	if f.streamErr != nil {
		return accumulator.Result(), f.streamErr
	}
	result := accumulator.Result()
	if result.Chunks == 0 {
		return result, upstreamInvalid("上游流式响应没有任何数据块")
	}
	return result, nil
}

func (f *fakeDriver) Embeddings(context.Context, Channel, EmbeddingRequest) (*EmbeddingResult, error) {
	if f.embedding == nil {
		return nil, upstreamInvalid("假驱动没有配置向量结果")
	}
	return f.embedding, nil
}

func (f *fakeDriver) Models(context.Context, Channel) ([]string, error) { return []string{"m1"}, nil }

func newGatewayFixture(t *testing.T, driver Driver) (*fakeRepository, *gin.Engine) {
	t.Helper()
	repo := &fakeRepository{
		model: Model{
			ID: 7, Name: "gpt-test", Type: ModelTypeChat, Status: StatusEnabled,
			Strategy: StrategyPriority, Encoding: EncodingNone,
			PriceInput: "0.000000", PriceOutput: "0.000000",
		},
		candidates: []Candidate{{
			ChannelID: 11, Name: "主渠道", Vendor: "openai", BaseURL: "https://api.example.com",
			APIKey: "sk-upstream", Model: "gpt-test", Timeout: 10,
			ChannelStatus: StatusEnabled, Circuit: CircuitClosed,
			ModelID: 7, BindingPriority: 1, BindingWeight: 1, BindingStatus: StatusEnabled,
		}},
	}
	return repo, newPublicEngine(engineDeps(repo, driver, "sk-gateway-key"))
}

func TestPublicChatNonStreamReturnsOpenAIShape(t *testing.T) {
	driver := &fakeDriver{chatResult: &ChatResult{
		ID: "chatcmpl-1", Model: "gpt-test", Content: "你好", FinishReason: "stop",
		PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5,
	}}
	repo, engine := newGatewayFixture(t, driver)

	recorder := doRequest(engine, http.MethodPost, "/ai/v1/chat/completions",
		`{"model":"gpt-test","messages":[{"role":"user","content":"hi"}]}`,
		map[string]string{"Authorization": "Bearer sk-gateway-key"})
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d（body=%s）", recorder.Code, recorder.Body.String())
	}
	var payload struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Model   string `json:"model"`
		Choices []struct {
			Index   int `json:"index"`
			Message struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("响应不是 JSON: %v", err)
	}
	if payload.ID != "chatcmpl-1" || payload.Object != "chat.completion" || payload.Model != "gpt-test" {
		t.Fatalf("响应头字段不对: %+v", payload)
	}
	if len(payload.Choices) != 1 || payload.Choices[0].Message.Content != "你好" || payload.Choices[0].Message.Role != "assistant" {
		t.Fatalf("choices 不对: %+v", payload.Choices)
	}
	if payload.Usage.TotalTokens != 5 || payload.Usage.PromptTokens != 3 || payload.Usage.CompletionTokens != 2 {
		t.Fatalf("usage 直出上游计数: %+v", payload.Usage)
	}
	// 记账：一次真实尝试恰一行 success（channel_id 已落到渠道 11）。
	if len(repo.usages) != 1 {
		t.Fatalf("应写 1 行 usage，实际 %d 行", len(repo.usages))
	}
	if repo.usages[0].Status != UsageStatusSuccess || repo.usages[0].ChannelID != 11 || repo.usages[0].Source != publicSource {
		t.Fatalf("usage 行不对: %+v", repo.usages[0])
	}
}

func TestPublicChatStreamWritesSSEFramesAndDone(t *testing.T) {
	driver := &fakeDriver{streamChunks: []string{
		`{"id":"chatcmpl-s","model":"gpt-test","choices":[{"index":0,"delta":{"content":"你"},"finish_reason":null}]}`,
		`{"id":"chatcmpl-s","model":"gpt-test","choices":[{"index":0,"delta":{"content":"好"},"finish_reason":"stop"}]}`,
		`{"id":"chatcmpl-s","model":"gpt-test","choices":[],"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6}}`,
	}}
	repo, engine := newGatewayFixture(t, driver)

	recorder := doRequest(engine, http.MethodPost, "/ai/v1/chat/completions",
		`{"model":"gpt-test","messages":[{"role":"user","content":"hi"}],"stream":true}`,
		map[string]string{"Authorization": "Bearer sk-gateway-key"})
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d（body=%s）", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Fatalf("Content-Type = %q，期望 text/event-stream", got)
	}
	if got := recorder.Header().Get("X-Accel-Buffering"); got != "no" {
		t.Fatalf("必须关闭反代缓冲（X-Accel-Buffering: no），实际 %q", got)
	}
	body := recorder.Body.String()
	if !strings.HasSuffix(strings.TrimSpace(body), "data: [DONE]") {
		t.Fatalf("SSE 必须以 data: [DONE] 收尾: %q", body)
	}
	if strings.Count(body, "data: ") != 4 {
		t.Fatalf("应有 3 个数据帧 + 1 个 [DONE]，实际: %q", body)
	}
	if !strings.Contains(body, "data: {") {
		t.Fatalf("数据帧必须是原始 chunk JSON: %q", body)
	}
	if len(repo.usages) != 1 || repo.usages[0].Status != UsageStatusSuccess {
		t.Fatalf("流式正常收尾必须记一行 success: %+v", repo.usages)
	}
	if repo.usages[0].Stream != 1 {
		t.Fatalf("流式成功行必须 stream=1: %+v", repo.usages[0])
	}
}

func TestPublicChatStreamFailureWritesErrorFrameWithoutDone(t *testing.T) {
	// 分支③（有块、无收尾）：不动熔断，但按已聚合内容**部分计费**——
	// stream=1、tokens_source=usage（上游在流里报过 usage）、cost 与单价×counts 逐位一致、
	// billed_partial=1、status 仍是 fail。
	driver := &fakeDriver{
		streamChunks: []string{
			`{"id":"c","model":"gpt-test","choices":[{"index":0,"delta":{"content":"半"},"finish_reason":null}]}`,
			`{"id":"c","model":"gpt-test","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":4,"total_tokens":14}}`,
		},
		streamErr: upstreamInvalid("上游中断"),
	}
	repo, engine := newGatewayFixture(t, driver)
	// 单价必须非零，cost 断言才能区分「按 counts 算」与「写死 0.000000」。
	repo.model.PriceInput = "1.000000"
	repo.model.PriceOutput = "2.000000"

	recorder := doRequest(engine, http.MethodPost, "/ai/v1/chat/completions",
		`{"model":"gpt-test","messages":[{"role":"user","content":"hi"}],"stream":true}`,
		map[string]string{"Authorization": "Bearer sk-gateway-key"})
	if recorder.Code != http.StatusOK {
		t.Fatalf("流已开始后状态码只能是 200，实际 %d", recorder.Code)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, `"error"`) {
		t.Fatalf("中途失败必须发错误帧: %q", body)
	}
	if strings.Contains(body, "[DONE]") {
		t.Fatalf("失败时不得补 [DONE]（客户端会把失败当正常收尾）: %q", body)
	}
	if len(repo.usages) != 1 {
		t.Fatalf("分支③必须恰落一行账，实际 %d 行", len(repo.usages))
	}
	row := repo.usages[0]
	if row.Status != UsageStatusFail && row.Status != UsageStatusTimeout {
		t.Fatalf("部分计费行的 status 仍是 fail|timeout，实际 %q", row.Status)
	}
	if row.Attempt != 0 {
		t.Fatalf("流式行 attempt 恒 0: %d", row.Attempt)
	}
	if row.Stream != 1 {
		t.Fatalf("有块无收尾的行必须 stream=1（与「无块」行可区分）: %+v", row)
	}
	if row.TokensSource != TokensSourceUsage {
		t.Fatalf("上游已报 usage ⇒ tokens_source=usage，实际 %q", row.TokensSource)
	}
	if row.PromptTokens != 10 || row.CompletionTokens != 4 || row.TotalTokens != 14 {
		t.Fatalf("必须按已见 usage 记 token: %+v", row)
	}
	// 手算：10/1e6×1.000000 + 4/1e6×2.000000 = 0.000010 + 0.000008 = 0.000018（不经被测函数自证）。
	if row.Cost != "0.000018" {
		t.Fatalf("cost = %q，期望 0.000018（单价×counts 逐位一致）", row.Cost)
	}
	if row.BilledPartial != 1 {
		t.Fatalf("部分计费行必须 billed_partial=1，实际 %d", row.BilledPartial)
	}
	// 熔断计数不增：分支③ 不触发 onFailure（MarkFailure 是 consecutive_failures 的唯一写口）。
	if repo.markFailureCalls != 0 {
		t.Fatalf("分支③（有块）不得触发 onFailure，实际 MarkFailure 被调 %d 次", repo.markFailureCalls)
	}
}

// TestPublicChatStreamPartialChunksFallBackToEstimate 分支③ 的估算腿：
// 上游没报 usage 且模型开了本地计数（encoding 非空）⇒ tokens_source=estimate，
// cost 同样按「单价 × counts」逐位对上。
func TestPublicChatStreamPartialChunksFallBackToEstimate(t *testing.T) {
	driver := &fakeDriver{
		streamChunks: []string{`{"id":"c","model":"gpt-test","choices":[{"index":0,"delta":{"content":"半"},"finish_reason":null}]}`},
		streamErr:    upstreamInvalid("上游中断"),
	}
	repo, engine := newGatewayFixture(t, driver)
	repo.model.Encoding = EncodingTokenzR
	repo.model.PriceInput = "3.000000"
	repo.model.PriceOutput = "5.000000"

	recorder := doRequest(engine, http.MethodPost, "/ai/v1/chat/completions",
		`{"model":"gpt-test","messages":[{"role":"user","content":"hi"}],"stream":true}`,
		map[string]string{"Authorization": "Bearer sk-gateway-key"})
	if recorder.Code != http.StatusOK {
		t.Fatalf("流已开始后状态码只能是 200，实际 %d", recorder.Code)
	}
	if len(repo.usages) != 1 {
		t.Fatalf("分支③必须恰落一行账，实际 %d 行", len(repo.usages))
	}
	row := repo.usages[0]
	if row.TokensSource != TokensSourceEstimate {
		t.Fatalf("无上游 usage 且 encoding 非空 ⇒ tokens_source=estimate，实际 %q", row.TokensSource)
	}
	// 手算（billing.go:EstimateTokens：tokens = ceil(cjk*4/5) + ceil(other/4)）：
	//   prompt     = MessagesText([{user,hi}]) = "user\nhi\n" ⇒ 8 个非 CJK 字符 ⇒ ceil(8/4) = 2
	//   completion = res.Content = "半" ⇒ 1 个 CJK ⇒ ceil(1*4/5) = 1
	//   cost       = (2×3.000000 + 1×5.000000)/1e6 = 0.000011
	if row.PromptTokens != 2 || row.CompletionTokens != 1 || row.TotalTokens != 3 {
		t.Fatalf("估算 counts 不对（期望 2/1/3）: %+v", row)
	}
	if row.Cost != "0.000011" {
		t.Fatalf("cost = %q，期望 0.000011（单价×counts 逐位一致）", row.Cost)
	}
	if row.BilledPartial != 1 || row.Stream != 1 {
		t.Fatalf("估算腿同样是部分计费行: stream=%d billed_partial=%d", row.Stream, row.BilledPartial)
	}
	if repo.markFailureCalls != 0 {
		t.Fatalf("分支③（有块）不得触发 onFailure，实际 MarkFailure 被调 %d 次", repo.markFailureCalls)
	}
}

// TestPublicChatStreamNoChunksBillsNothingAndStillTripsBreaker 分支④（无块、首块前失败）：
// tokens_source 为空串、cost='0.000000'、billed_partial=0，且 onFailure 照旧（熔断计数照常 +1）。
func TestPublicChatStreamNoChunksBillsNothingAndStillTripsBreaker(t *testing.T) {
	driver := &fakeDriver{streamErr: upstreamRetryable("连接建立失败")}
	repo, engine := newGatewayFixture(t, driver)
	repo.model.Encoding = EncodingTokenzR // 即使开了本地计数，无块 ⇒ 不估算（没有可计数的内容）
	repo.model.PriceInput = "1.000000"
	repo.model.PriceOutput = "2.000000"

	recorder := doRequest(engine, http.MethodPost, "/ai/v1/chat/completions",
		`{"model":"gpt-test","messages":[{"role":"user","content":"hi"}],"stream":true}`,
		map[string]string{"Authorization": "Bearer sk-gateway-key"})
	if recorder.Code != http.StatusServiceUnavailable && recorder.Code != http.StatusGatewayTimeout {
		t.Fatalf("未开始写流时必须回真实状态码，实际 %d（body=%s）", recorder.Code, recorder.Body.String())
	}
	if len(repo.usages) != 1 {
		t.Fatalf("分支④必须恰落一行账，实际 %d 行", len(repo.usages))
	}
	row := repo.usages[0]
	if row.Stream != 0 || row.TokensSource != TokensSourceNone || row.Cost != "0.000000" || row.BilledPartial != 0 {
		t.Fatalf("无块失败行必须 stream=0 / tokens_source='' / cost=0.000000 / billed_partial=0: %+v", row)
	}
	if row.Status != UsageStatusFail {
		t.Fatalf("分支④ 落 fail 行: %q", row.Status)
	}
	if repo.markFailureCalls == 0 {
		t.Fatal("分支④（无块）必须仍触发 onFailure（熔断计数照旧 +1）")
	}
}

// TestSettleStreamFailureHandlesNilAndUncountableResults 是结算点的两个边界：
//   - res 为 nil（驱动未产出聚合结果）⇒ 不得 panic、按「无块」口径落零计费行；
//   - res 有块但既无上游 usage 又没开本地计数（encoding 为空）⇒ 走补偿分支（billed_partial=1、
//     stream=1，标记「本次结算走了部分计费路径」），但绝不伪造计数：tokens_source 为空串、cost=0.000000。
func TestSettleStreamFailureHandlesNilAndUncountableResults(t *testing.T) {
	info := ErrorInfo{Class: ErrorClassUpstream, Code: "invalid_response", Message: "上游中断", Status: UsageStatusFail}
	model := Model{ID: 7, Name: "gpt-test", Type: ModelTypeChat, Encoding: EncodingNone}

	// ① res=nil：零计费行。
	repo := &fakeRepository{}
	service := NewService(repo, &fakeDriver{}, Config{}, nil)
	service.settleStreamFailure(context.Background(), model, Channel{ID: 11, Name: "主渠道"}, info,
		0, "req-nil", "api", publicSource, nil, "user\nhi\n", false)
	if len(repo.usages) != 1 {
		t.Fatalf("res=nil 也必须落一行账，实际 %d 行", len(repo.usages))
	}
	if row := repo.usages[0]; row.Stream != 0 || row.BilledPartial != 0 || row.TokensSource != TokensSourceNone || row.Cost != "0.000000" {
		t.Fatalf("res=nil 按分支④ 口径: %+v", row)
	}

	// ② 有块但计无可计（encoding='' 且上游未报 usage）：补偿路径 + 零计数。
	repo = &fakeRepository{}
	service = NewService(repo, &fakeDriver{}, Config{}, nil)
	service.settleStreamFailure(context.Background(), model, Channel{ID: 11, Name: "主渠道"}, info,
		0, "req-uncountable", "api", publicSource,
		&ChatResult{Content: "半", Chunks: 1}, "user\nhi\n", false)
	if len(repo.usages) != 1 {
		t.Fatalf("必须落一行账，实际 %d 行", len(repo.usages))
	}
	row := repo.usages[0]
	if row.Stream != 1 || row.BilledPartial != 1 {
		t.Fatalf("有块无收尾即走补偿路径（与无块可区分）: stream=%d billed_partial=%d", row.Stream, row.BilledPartial)
	}
	if row.TokensSource != TokensSourceNone || row.Cost != "0.000000" || row.TotalTokens != 0 {
		t.Fatalf("计无可计时绝不伪造计数: %+v", row)
	}
}

func TestPublicChatStreamFailsBeforeFirstChunkWithRealStatus(t *testing.T) {
	driver := &fakeDriver{streamErr: upstreamRetryable("连接建立失败")}
	_, engine := newGatewayFixture(t, driver)

	recorder := doRequest(engine, http.MethodPost, "/ai/v1/chat/completions",
		`{"model":"gpt-test","messages":[{"role":"user","content":"hi"}],"stream":true}`,
		map[string]string{"Authorization": "Bearer sk-gateway-key"})
	if recorder.Code != http.StatusServiceUnavailable && recorder.Code != http.StatusGatewayTimeout {
		t.Fatalf("未开始写流时必须回真实状态码，实际 %d（body=%s）", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "[DONE]") {
		t.Fatalf("失败回执不得含 [DONE]: %s", recorder.Body.String())
	}
	var envelope publicEnvelope
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("失败回执必须是 OpenAI 形状 JSON: %v", err)
	}
	if envelope.Error.Code == "" {
		t.Fatalf("失败回执应带 code: %+v", envelope)
	}
}

func TestPublicModelsListsEnabledChatModels(t *testing.T) {
	repo := &fakeRepository{}
	engine := newPublicEngine(engineDeps(repo, &fakeDriver{}, "sk-gateway-key"))

	// store 不可用（无库）⇒ 503 fail-closed
	recorder := doRequest(engine, http.MethodGet, "/ai/v1/models", "", map[string]string{"Authorization": "Bearer sk-gateway-key"})
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("库不可用必须 503，实际 %d", recorder.Code)
	}
	// 无凭证连库都到不了：401 先发生
	recorder = doRequest(engine, http.MethodGet, "/ai/v1/models", "", nil)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("未带凭证必须 401，实际 %d", recorder.Code)
	}
}

// ===== 其它小口径 =====

func TestBindRowsDefaultsAndClamp(t *testing.T) {
	zero := 0
	rows := bindRows(5, []modelBindItem{
		{ChannelID: 11, Enabled: true},
		{ChannelID: 0, Enabled: true},
		{ChannelID: 12, Priority: &zero, Weight: &zero, Enabled: false},
	})
	if len(rows) != 2 {
		t.Fatalf("channel_id=0 必须跳过: %+v", rows)
	}
	if rows[0].ModelID != 5 || rows[0].Priority != 1 || rows[0].Weight != 1 || rows[0].Status != StatusEnabled {
		t.Fatalf("缺省 priority/weight 必须是 1、enabled=true ⇒ status=1: %+v", rows[0])
	}
	if rows[1].Priority != 0 || rows[1].Weight != 0 || rows[1].Status != StatusDisabled {
		t.Fatalf("显式 0 必须保留、enabled=false ⇒ status=0: %+v", rows[1])
	}
}

func TestIsUniqueViolationDetectsPostgresDuplicate(t *testing.T) {
	if !isUniqueViolation(errors.New(`ERROR: duplicate key value violates unique constraint "admin_plugin_ai_model_name" (SQLSTATE 23505)`)) {
		t.Fatal("PG 唯一索引冲突必须被识别（用于把 DB 错误翻成可读文案）")
	}
	if isUniqueViolation(nil) || isUniqueViolation(errors.New("connection refused")) {
		t.Fatal("非唯一冲突不得误判")
	}
}

func TestParsePKAndPksValidation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.GET("/x/:pk", func(c *gin.Context) {
		id, err := parsePK(c, "渠道")
		if err != nil {
			c.String(http.StatusBadRequest, err.Error())
			return
		}
		c.String(http.StatusOK, fmt.Sprintf("%d", id))
	})
	if recorder := doRequest(engine, http.MethodGet, "/x/12", "", nil); recorder.Body.String() != "12" {
		t.Fatalf("正常主键解析失败: %s", recorder.Body.String())
	}
	if recorder := doRequest(engine, http.MethodGet, "/x/0", "", nil); recorder.Code != http.StatusBadRequest {
		t.Fatal("0 不是合法主键")
	}
	if recorder := doRequest(engine, http.MethodGet, "/x/abc", "", nil); recorder.Code != http.StatusBadRequest {
		t.Fatal("非数字主键必须被拒")
	}

	if _, err := (pksRequest{}).ids("渠道"); err == nil {
		t.Fatal("空 pks 必须被拒")
	}
	if _, err := (pksRequest{Pks: []string{"1", "x"}}).ids("渠道"); err == nil {
		t.Fatal("非法 pks 元素必须被拒")
	}
	ids, err := (pksRequest{Pks: []string{"1", "2"}}).ids("渠道")
	if err != nil || len(ids) != 2 {
		t.Fatalf("合法 pks 解析失败: %+v err=%v", ids, err)
	}
}

func TestBindJSONIgnoresUnknownTopLevelKeys(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.POST("/x", func(c *gin.Context) {
		var req channelListRequest
		if err := bindJSON(c, &req); err != nil {
			c.String(http.StatusBadRequest, err.Error())
			return
		}
		c.JSON(http.StatusOK, gin.H{"limit": req.Limit, "status": req.Status, "vendor": req.Vendor})
	})
	recorder := doRequest(engine, http.MethodPost, "/x",
		`{"limit":100,"vendor":"ollama","selector":true,"pk":"9","unknown":"x","status":0}`, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("未知顶层键必须被忽略（宿主管家口径），实际 %d", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), `"limit":100`) {
		t.Fatalf("显式字段必须绑定: %s", recorder.Body.String())
	}
	// 空体按零值处理（列表页可能发空对象）
	if recorder := doRequest(engine, http.MethodPost, "/x", "", nil); recorder.Code != http.StatusOK {
		t.Fatalf("空体必须按默认值处理: %d", recorder.Code)
	}
}

func TestTopLevelStatusToleratesEmptyAndRejectsGarbage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.POST("/x", func(c *gin.Context) {
		var req channelListRequest
		if err := bindJSON(c, &req); err != nil {
			c.String(http.StatusBadRequest, err.Error())
			return
		}
		filter, _, err := req.filter()
		if err != nil {
			c.String(http.StatusBadRequest, err.Error())
			return
		}
		if filter.Status == nil {
			c.String(http.StatusOK, "unset")
			return
		}
		c.String(http.StatusOK, fmt.Sprintf("%d", *filter.Status))
	})

	// 清空筛选器时的三种「不筛选」写法都必须被容忍（空串把请求打成 400 不可接受）。
	for _, body := range []string{`{}`, `{"status":null}`, `{"status":""}`} {
		recorder := doRequest(engine, http.MethodPost, "/x", body, nil)
		if recorder.Code != http.StatusOK || recorder.Body.String() != "unset" {
			t.Fatalf("%s 应视为「不筛选」，实际 %d/%s", body, recorder.Code, recorder.Body.String())
		}
	}
	for body, want := range map[string]string{`{"status":1}`: "1", `{"status":0}`: "0"} {
		recorder := doRequest(engine, http.MethodPost, "/x", body, nil)
		if recorder.Code != http.StatusOK || recorder.Body.String() != want {
			t.Fatalf("%s 应解析成 %s，实际 %d/%s", body, want, recorder.Code, recorder.Body.String())
		}
	}
	recorder := doRequest(engine, http.MethodPost, "/x", `{"status":5}`, nil)
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "status") {
		t.Fatalf("0/1 之外的 status 必须被拒且文案点名: %d/%s", recorder.Code, recorder.Body.String())
	}
	recorder = doRequest(engine, http.MethodPost, "/x", `{"status":"x"}`, nil)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("非 0/1 的 status 必须被拒，实际 %d", recorder.Code)
	}
}

func stringPtr(value string) *string { return &value }

func intPtr(value int) *int { return &value }
