// plugin: ai v0.2.0
// driver_registry_test.go 是 F2（多驱动注册表 + Responses 协议 + 探活分派）的判据。
//
// 覆盖（逐条）：
//   - 注册/解析：内建驱动可用、空值回落 openai（与 driver 列落地之前的现行为一致）、
//     未知驱动返回 local 错误且**不静默回落**；
//   - Responses 协议：httptest 伪上游的非流式与流式（语义化事件序列）⇒ content/usage 聚合正确、
//     [DONE] 与终局事件的收尾口径一致、上游中断/失败事件/空流的「不可重试」判定；
//   - 别名：DeepSeek/DashScope 的默认 base_url 与静态 modelMap 命中、Responses 上 embedding 明确拒绝；
//   - 探活分派：注册表按 ch.Driver 分派覆盖 Models（探活唯一依赖面）+ 未知驱动明确报错；
//   - 既有契约继承：出站护栏（allow_private 双向 + 不跟随重定向）、SSE 三级上限、脱敏出口。
package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// ===== 辅助件（drvTest 前缀避让同包其它用例的 helper 命名）=====

// drvTestEnv 注册表环境（Transport 留空 ⇒ 由注册表自建唯一 HTTP 底座）。
func drvTestEnv(allowPrivate bool) DriverEnv {
	return DriverEnv{AllowPrivateHosts: allowPrivate}
}

func drvTestRegistry(t *testing.T, allowPrivate bool) *DriverRegistry {
	t.Helper()
	reg := NewDriverRegistry(drvTestEnv(allowPrivate))
	if reg.Env().Transport == nil {
		t.Fatal("注册表必须自建护栏 HTTP 底座")
	}
	return reg
}

// drvTestContains 判定字符串切片是否含某值。
func drvTestContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// drvTestLocalError 断言错误是 local 分类并返回其错误码。
func drvTestLocalError(t *testing.T, err error, wantCode string) *UpstreamError {
	t.Helper()
	if err == nil {
		t.Fatalf("期望 local 错误（code=%s），实际 nil", wantCode)
	}
	var upstream *UpstreamError
	if !errors.As(err, &upstream) {
		t.Fatalf("错误类型 = %T，期望 *UpstreamError", err)
	}
	if upstream.Class != ErrorClassLocal {
		t.Fatalf("错误分类 = %q，期望 %q（文案：%s）", upstream.Class, ErrorClassLocal, upstream.Message)
	}
	if wantCode != "" && upstream.Code != wantCode {
		t.Fatalf("错误码 = %q，期望 %q（文案：%s）", upstream.Code, wantCode, upstream.Message)
	}
	return upstream
}

// drvTestSSEFrame 组一个语义化事件帧（event: 行 + data: 行）。
func drvTestSSEFrame(event, data string) string {
	if event == "" {
		return "data: " + data + "\n\n"
	}
	return "event: " + event + "\ndata: " + data + "\n\n"
}

// drvTestResponsesServer 起一个伪 Responses 上游：记录路径/鉴权/请求体，按脚本回帧。
func drvTestResponsesServer(t *testing.T, frames []string, body string, status int) (*httptest.Server, *drvTestRecordedRequest) {
	t.Helper()
	recorded := &drvTestRecordedRequest{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorded.path = r.URL.Path
		recorded.auth = r.Header.Get("Authorization")
		recorded.accept = r.Header.Get("Accept")
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err == nil {
			recorded.body = payload
		}
		if status != 0 {
			w.WriteHeader(status)
		}
		if frames != nil {
			w.Header().Set("Content-Type", "text/event-stream")
			flusher, _ := w.(http.Flusher)
			for _, frame := range frames {
				_, _ = io.WriteString(w, frame)
				if flusher != nil {
					flusher.Flush()
				}
			}
			return
		}
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(server.Close)
	return server, recorded
}

type drvTestRecordedRequest struct {
	path   string
	auth   string
	accept string
	body   map[string]any
}

// drvTestFakeDriver 记录调用次数（自注册驱动的分派证据；dashscope_tts 走同一条 Register 通道）。
type drvTestFakeDriver struct {
	chatCalls      int32
	embeddingCalls int32
	modelsCalls    int32
	models         []string
}

func (f *drvTestFakeDriver) Chat(context.Context, Channel, ChatRequest) (*ChatResult, error) {
	atomic.AddInt32(&f.chatCalls, 1)
	return &ChatResult{Content: "fake"}, nil
}

func (f *drvTestFakeDriver) ChatStream(_ context.Context, _ Channel, _ ChatRequest, _ func([]byte) error) (*ChatResult, error) {
	return &ChatResult{Content: "fake"}, nil
}

func (f *drvTestFakeDriver) Embeddings(context.Context, Channel, EmbeddingRequest) (*EmbeddingResult, error) {
	atomic.AddInt32(&f.embeddingCalls, 1)
	return &EmbeddingResult{}, nil
}

func (f *drvTestFakeDriver) Models(context.Context, Channel) ([]string, error) {
	atomic.AddInt32(&f.modelsCalls, 1)
	return f.models, nil
}

// ===== 注册表：解析 / 空值回落 / 未知驱动 =====

func TestRegistryResolvesBuiltinsAndFallsBackToOpenAI(t *testing.T) {
	reg := drvTestRegistry(t, true)
	supported := GetSupported()
	for _, want := range []string{DriverNameOpenAI, DriverNameDeepSeek, DriverNameDashScope, DriverNameResponses} {
		if !drvTestContains(supported, want) {
			t.Fatalf("GetSupported() = %v，缺少内建驱动 %q", supported, want)
		}
	}
	if !drvTestContains(supported, DriverNameOpenAI+"x") == false && drvTestContains(supported, "aliyun") {
		t.Fatalf("GetSupported() = %v：本批不注册源侧旧名 aliyun（差异登记见 driver_registry.go 注释）", supported)
	}

	// 空值/显式 openai ⇒ 既有 OpenAI 驱动（driver 列落地之前的现行为逐字不变）
	for _, name := range []string{"", DriverNameOpenAI, "  OpenAI "} {
		driver, err := reg.Resolve(name)
		if err != nil {
			t.Fatalf("Resolve(%q) 错误：%v", name, err)
		}
		if _, ok := driver.(*OpenAIDriver); !ok {
			t.Fatalf("Resolve(%q) = %T，期望 *OpenAIDriver", name, driver)
		}
	}

	// 渠道维度分派（ch.Driver = 迁移落的列）
	cases := map[string]any{
		"":              (*OpenAIDriver)(nil),
		"openai":        (*OpenAIDriver)(nil),
		"deepseek":      (*compatibleDriver)(nil),
		"dashscope":     (*compatibleDriver)(nil),
		"responses":     (*ResponsesDriver)(nil),
		"  ReSpOnSeS  ": (*ResponsesDriver)(nil),
	}
	for name, wantType := range cases {
		driver, err := reg.ResolveChannel(Channel{Driver: name})
		if err != nil {
			t.Fatalf("ResolveChannel(%q) 错误：%v", name, err)
		}
		switch wantType.(type) {
		case *OpenAIDriver:
			if _, ok := driver.(*OpenAIDriver); !ok {
				t.Fatalf("driver=%q 解析 = %T，期望 *OpenAIDriver", name, driver)
			}
		case *compatibleDriver:
			if _, ok := driver.(*compatibleDriver); !ok {
				t.Fatalf("driver=%q 解析 = %T，期望 *compatibleDriver", name, driver)
			}
		case *ResponsesDriver:
			if _, ok := driver.(*ResponsesDriver); !ok {
				t.Fatalf("driver=%q 解析 = %T，期望 *ResponsesDriver", name, driver)
			}
		}
	}

	// 实例按名缓存：探活与真实调用共用同一实例（连接池/护栏分桶才有意义）
	first, err := reg.Resolve(DriverNameDashScope)
	if err != nil {
		t.Fatal(err)
	}
	second, err := reg.ResolveChannel(Channel{Driver: DriverNameDashScope})
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("同一驱动名的两次解析必须命中同一实例")
	}
}

func TestRegistryRejectsUnknownDriverWithoutFallback(t *testing.T) {
	reg := drvTestRegistry(t, true)
	// wangsu（源侧有、本批未落）与 aliyun（源侧旧名、本批规范化取名 dashscope）
	for _, name := range []string{"wangsu", "aliyun", "gpt-4o-plugin"} {
		driver, err := reg.Resolve(name)
		if driver != nil {
			t.Fatalf("未知驱动 %q 不得回落出任何实例（得到 %T）", name, driver)
		}
		upstream := drvTestLocalError(t, err, "unknown_driver")
		if !strings.Contains(upstream.Message, name) {
			t.Fatalf("错误文案须点名驱动 %q：%s", name, upstream.Message)
		}
		if !strings.Contains(upstream.Message, DriverNameResponses) {
			t.Fatalf("错误文案须给出可用驱动清单：%s", upstream.Message)
		}
	}
	// 四方法入口都要走同一分派拒绝：探活（Models）、对话、流式、向量
	ctx := context.Background()
	ch := Channel{ID: 9, Driver: "wangsu", BaseURL: "https://example.com", APIKey: "k"}
	if _, err := reg.Models(ctx, ch); drvTestLocalError(t, err, "unknown_driver") == nil {
		t.Fatal("unreachable")
	}
	if _, err := reg.Chat(ctx, ch, ChatRequest{Model: "m"}); err == nil {
		t.Fatal("Chat 未拒绝未知驱动")
	}
	sink := func([]byte) error { return nil }
	if _, err := reg.ChatStream(ctx, ch, ChatRequest{Model: "m"}, sink); err == nil {
		t.Fatal("ChatStream 未拒绝未知驱动")
	}
	if _, err := reg.Embeddings(ctx, ch, EmbeddingRequest{Model: "m", Input: "x"}); err == nil {
		t.Fatal("Embeddings 未拒绝未知驱动")
	}
}

// ===== B3：探活分派（Models 是 handler_channel 探活按钮的唯一依赖面）=====

func TestRegistryModelsDispatchesByChannelDriver(t *testing.T) {
	var hits int32
	recorded := &drvTestRecordedRequest{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		recorded.path = r.URL.Path
		recorded.auth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"id":"deepseek-v4-flash"},{"id":"deepseek-r1"}]}`)
	}))
	defer server.Close()

	reg := drvTestRegistry(t, true)
	ctx := context.Background()
	ch := Channel{ID: 3, Driver: DriverNameResponses, BaseURL: server.URL, APIKey: "sk-drvTest-probe", Timeout: 5}

	// 解析到 Responses 实现（不是 OpenAI 实现）
	driver, err := reg.ResolveChannel(ch)
	if err != nil {
		t.Fatalf("ResolveChannel 错误：%v", err)
	}
	if _, ok := driver.(*ResponsesDriver); !ok {
		t.Fatalf("driver=responses 解析 = %T，期望 *ResponsesDriver（探活不得恒走 OpenAI 协议）", driver)
	}

	models, err := reg.Models(ctx, ch)
	if err != nil {
		t.Fatalf("Models 错误：%v", err)
	}
	if len(models) != 2 || models[0] != "deepseek-v4-flash" {
		t.Fatalf("探活模型列表 = %v", models)
	}
	if recorded.path != "/models" {
		t.Fatalf("探活路径 = %q，期望 /models", recorded.path)
	}
	if recorded.auth != "Bearer sk-drvTest-probe" {
		t.Fatalf("探活鉴权 = %q", recorded.auth)
	}
	if atomic.LoadInt32(&hits) != 1 {
		t.Fatalf("上游命中数 = %d，期望 1", hits)
	}

	// 未知驱动：探活回执拿到的就是这份 local 文案（handler_channel 直接透出 probeErr.Error()）
	_, err = reg.Models(ctx, Channel{ID: 4, Driver: "wangsu", BaseURL: server.URL, APIKey: "k"})
	upstream := drvTestLocalError(t, err, "unknown_driver")
	if !strings.Contains(upstream.Message, "wangsu") {
		t.Fatalf("探活错误文案未点名驱动：%s", upstream.Message)
	}
	if atomic.LoadInt32(&hits) != 1 {
		t.Fatal("未知驱动不得发起任何上游调用")
	}
}

// ===== 能力门 =====

func TestRegistryCapabilityGateRejectsUndeclaredCapability(t *testing.T) {
	var hits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/embeddings" {
			_, _ = io.WriteString(w, `{"model":"text-embedding-v4","data":[{"index":0,"object":"embedding","embedding":[0.1]}],"usage":{"prompt_tokens":1,"completion_tokens":0}}`)
			return
		}
		_, _ = io.WriteString(w, `{"id":"c1","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	}))
	defer server.Close()

	reg := drvTestRegistry(t, true)
	ctx := context.Background()
	for _, name := range []string{DriverNameResponses, DriverNameDeepSeek} {
		ch := Channel{ID: 1, Driver: name, BaseURL: server.URL, APIKey: "k", Timeout: 5}
		if _, err := reg.Embeddings(ctx, ch, EmbeddingRequest{Model: "m", Input: "x"}); err == nil {
			t.Fatalf("driver=%s 未声明 embedding，必须被能力门拒", name)
		} else {
			upstream := drvTestLocalError(t, err, "unsupported_capability")
			if !strings.Contains(upstream.Message, name) || !strings.Contains(upstream.Message, CapabilityEmbedding) {
				t.Fatalf("能力门文案 = %s", upstream.Message)
			}
		}
	}
	// dashscope 声明了 embedding、openai 声明了 chat ⇒ 放行（与上面的拒绝形成对照）
	if _, err := reg.Embeddings(ctx, Channel{ID: 1, Driver: DriverNameDashScope, BaseURL: server.URL, APIKey: "k", Timeout: 5}, EmbeddingRequest{Model: "m", Input: "x"}); err != nil {
		t.Fatalf("dashscope 声明了 embedding，不应被能力门拒：%v", err)
	}
	if _, err := reg.Chat(ctx, Channel{ID: 1, Driver: DriverNameOpenAI, BaseURL: server.URL, APIKey: "k", Timeout: 5},
		ChatRequest{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}}); err != nil {
		t.Fatalf("openai 声明了 chat，不应被能力门拒：%v", err)
	}
	if atomic.LoadInt32(&hits) != 2 {
		t.Fatalf("被拒的能力调用不得打到上游（命中 %d，期望 2=放行的两次）", hits)
	}

	// 自注册 + 能力门：声明 speech 的驱动不接受 chat（dashscope_tts 形态）
	fake := &drvTestFakeDriver{}
	if err := Register(DriverSpec{Name: "drvTest_speech", Capabilities: []string{"SPEECH"}, Factory: func(DriverEnv) Driver { return fake }}); err != nil {
		t.Fatalf("Register 错误：%v", err)
	}
	defer unregisterDriverSpec("drvTest_speech")
	if !SupportsCapability("drvTest_speech", CapabilitySpeech) {
		t.Fatal("自注册驱动能力声明未生效")
	}
	if _, err := reg.Chat(ctx, Channel{Driver: "drvTest_speech"}, ChatRequest{Model: "m"}); err == nil {
		t.Fatal("speech-only 驱动必须被 chat 能力门拒")
	}
	if _, err := reg.Models(ctx, Channel{Driver: "drvTest_speech"}); err != nil {
		t.Fatalf("Models 是探活通道，不做能力门：%v", err)
	}
}

// ===== 别名驱动：默认 base_url + 静态 modelMap =====

func TestCompatibleVariantsApplyDefaultBaseURLAndModelMap(t *testing.T) {
	reg := drvTestRegistry(t, true)

	// 默认端点：渠道留空 ⇒ 套厂商默认；显式填值 ⇒ 不覆盖（源子类构造期行为）
	deepseekDriver, err := reg.Resolve(DriverNameDeepSeek)
	if err != nil {
		t.Fatal(err)
	}
	deepseek, ok := deepseekDriver.(*compatibleDriver)
	if !ok {
		t.Fatalf("deepseek 解析 = %T", deepseekDriver)
	}
	if deepseek.defaultBaseURL != deepSeekDefaultBaseURL {
		t.Fatalf("deepseek 默认端点 = %q，期望 %q", deepseek.defaultBaseURL, deepSeekDefaultBaseURL)
	}
	if got := deepseek.upstreamChannel(Channel{}, "").BaseURL; got != deepSeekDefaultBaseURL {
		t.Fatalf("渠道留空时 base_url = %q，期望 %q", got, deepSeekDefaultBaseURL)
	}
	if got := deepseek.upstreamChannel(Channel{BaseURL: "https://example.com/v1"}, "").BaseURL; got != "https://example.com/v1" {
		t.Fatalf("渠道显式 base_url 被覆盖为 %q", got)
	}

	dashscopeDriver, err := reg.Resolve(DriverNameDashScope)
	if err != nil {
		t.Fatal(err)
	}
	dashscope, ok := dashscopeDriver.(*compatibleDriver)
	if !ok {
		t.Fatalf("dashscope 解析 = %T", dashscopeDriver)
	}
	if dashscope.defaultBaseURL != dashScopeDefaultBaseURL {
		t.Fatalf("dashscope 默认端点 = %q，期望 %q", dashscope.defaultBaseURL, dashScopeDefaultBaseURL)
	}

	// 模型名映射真的进了上游请求体（静态 modelMap 替代源的 extra.model_map）
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = nil
		_ = json.NewDecoder(r.Body).Decode(&captured)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"c1","model":"upstream-echo","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	}))
	defer server.Close()

	ctx := context.Background()
	ch := Channel{ID: 1, Driver: DriverNameDashScope, BaseURL: server.URL, APIKey: "sk-drvTest", Timeout: 5}
	for logical, want := range map[string]string{
		"deepseek-chat":     "deepseek-v3",
		"deepseek-reasoner": "deepseek-r1",
		"qwen-max":          "qwen-max", // 未命中原样透传
	} {
		res, err := reg.Chat(ctx, ch, ChatRequest{Model: logical, Messages: []Message{{Role: "user", Content: "hi"}}})
		if err != nil {
			t.Fatalf("Chat(%s) 错误：%v", logical, err)
		}
		if got := captured["model"]; got != want {
			t.Fatalf("逻辑模型 %s 的上游模型名 = %v，期望 %q", logical, got, want)
		}
		if res.Model != logical {
			t.Fatalf("结果模型名 = %q，期望请求侧解析名 %q（源 normalizeResponse 口径）", res.Model, logical)
		}
	}

	// 渠道 model 显式配置时优先，且同样过映射（源 resolveModel(options) 口径）
	res, err := reg.Chat(ctx, Channel{ID: 1, Driver: DriverNameDashScope, BaseURL: server.URL, APIKey: "sk-drvTest", Model: "deepseek-reasoner", Timeout: 5},
		ChatRequest{Model: "logical-name", Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := captured["model"]; got != "deepseek-r1" {
		t.Fatalf("渠道 model 优先但未过映射：%v", got)
	}
	if res.Model != "deepseek-reasoner" {
		t.Fatalf("结果模型名 = %q，期望渠道配置的请求侧名", res.Model)
	}
}

// ===== Responses 协议：非流式 =====

func TestResponsesDriverChatNormalizesNonStreamResponse(t *testing.T) {
	server, recorded := drvTestResponsesServer(t, nil, `{
		"id":"resp_1",
		"model":"upstream-alias",
		"output":[
			{"type":"reasoning","summary":[{"type":"summary_text","text":"先想"}]},
			{"type":"message","content":[{"type":"output_text","text":"你"},{"type":"output_text","text":"好"}]}
		],
		"incomplete_details":{"reason":"max_output_tokens"},
		"usage":{"input_tokens":7,"output_tokens":3,"input_token_details":null,"input_tokens_details":{"cached_tokens":2}}
	}`, 0)

	reg := drvTestRegistry(t, true)
	maxTokens := 32
	temperature := 0.2
	res, err := reg.Chat(context.Background(), Channel{
		ID: 1, Driver: DriverNameResponses, BaseURL: server.URL, APIKey: "sk-drvTest-key", Timeout: 5,
	}, ChatRequest{
		Model:          "deepseek-v4-flash",
		Messages:       []Message{{Role: "system", Content: "你是助手"}, {Role: "system", Content: "只答结论"}, {Role: "user", Content: "hi"}},
		MaxTokens:      &maxTokens,
		Temperature:    &temperature,
		ResponseFormat: map[string]any{"type": "json_object"},
		Extra:          map[string]any{"seed": 7, "messages": "不得透传"},
	})
	if err != nil {
		t.Fatalf("Chat 错误：%v", err)
	}
	if recorded.path != responsesPath {
		t.Fatalf("路径 = %q，期望 %q", recorded.path, responsesPath)
	}
	if recorded.auth != "Bearer sk-drvTest-key" {
		t.Fatalf("鉴权头 = %q", recorded.auth)
	}
	// 请求体口径（源 buildBody）
	if recorded.body["model"] != "deepseek-v4-flash" {
		t.Fatalf("请求 model = %v，期望请求侧解析名", recorded.body["model"])
	}
	if recorded.body["stream"] != false {
		t.Fatalf("请求 stream = %v，期望 false", recorded.body["stream"])
	}
	if recorded.body["instructions"] != "你是助手\n\n只答结论" {
		t.Fatalf("instructions = %v", recorded.body["instructions"])
	}
	input, _ := recorded.body["input"].([]any)
	if len(input) != 1 {
		t.Fatalf("input = %v，期望只含非 system 消息", recorded.body["input"])
	}
	if item := input[0].(map[string]any); item["type"] != "message" || item["role"] != "user" || item["content"] != "hi" {
		t.Fatalf("input item = %v", item)
	}
	if recorded.body["max_output_tokens"] != float64(32) {
		t.Fatalf("max_output_tokens = %v（max_tokens 须改名）", recorded.body["max_output_tokens"])
	}
	if _, ok := recorded.body["max_tokens"]; ok {
		t.Fatal("max_tokens 不得原样透传（协议字段名不同）")
	}
	text, _ := recorded.body["text"].(map[string]any)
	if format, _ := text["format"].(map[string]any); format["type"] != "json_object" {
		t.Fatalf("text.format = %v（response_format 须改名）", recorded.body["text"])
	}
	if recorded.body["seed"] != float64(7) {
		t.Fatalf("Extra 须透传：%v", recorded.body)
	}
	if _, ok := recorded.body["messages"]; ok {
		t.Fatal("Extra 不得覆盖/注入 messages")
	}
	// 响应归一
	if res.Content != "你好" || res.ReasoningContent != "先想" {
		t.Fatalf("归一内容错误：%+v", res)
	}
	if res.FinishReason != "length" {
		t.Fatalf("finish_reason = %q，期望 length（incomplete_details 非空）", res.FinishReason)
	}
	if res.Model != "deepseek-v4-flash" || res.ID != "resp_1" {
		t.Fatalf("结果 ID/Model = %q/%q", res.ID, res.Model)
	}
	if !res.UsageSeen || res.PromptTokens != 7 || res.CompletionTokens != 3 || res.CachedTokens != 2 {
		t.Fatalf("usage 归一错误：%+v", res)
	}
}

func TestResponsesDriverChatPrefersOutputTextAndDefaultsFinishReason(t *testing.T) {
	server, _ := drvTestResponsesServer(t, nil, `{"id":"resp_2","output_text":"顶层正文","usage":{"input_tokens":1,"output_tokens":1}}`, 0)
	reg := drvTestRegistry(t, true)
	res, err := reg.Chat(context.Background(), Channel{ID: 1, Driver: DriverNameResponses, BaseURL: server.URL, APIKey: "k", Timeout: 5},
		ChatRequest{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatalf("Chat 错误：%v", err)
	}
	if res.Content != "顶层正文" {
		t.Fatalf("output_text 优先口径失效：%q", res.Content)
	}
	if res.FinishReason != "stop" {
		t.Fatalf("无 incomplete_details 时 finish_reason = %q，期望 stop", res.FinishReason)
	}

	// 2xx 但没有任何回复内容 ⇒ 不可重试的无效响应（与 OpenAI 驱动同口径）
	empty, _ := drvTestResponsesServer(t, nil, `{"id":"resp_3","output":[]}`, 0)
	if _, err := reg.Chat(context.Background(), Channel{ID: 1, Driver: DriverNameResponses, BaseURL: empty.URL, APIKey: "k", Timeout: 5},
		ChatRequest{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}}); err == nil {
		t.Fatal("空回复必须报错")
	} else {
		var upstream *UpstreamError
		if !errors.As(err, &upstream) || upstream.Code != "invalid_response" || upstream.Retryable {
			t.Fatalf("空回复错误 = %+v，期望不可重试的 invalid_response", upstream)
		}
	}
}

func TestResponsesDriverMapsUpstreamStatusAndKeyIsNeverInError(t *testing.T) {
	const apiKey = "sk-drvTest-abcdefgh"
	server, _ := drvTestResponsesServer(t, nil, `{"error":{"message":"invalid api key `+apiKey+`","type":"invalid_request_error"}}`, http.StatusUnauthorized)
	reg := drvTestRegistry(t, true)
	_, err := reg.Chat(context.Background(), Channel{ID: 1, Driver: DriverNameResponses, BaseURL: server.URL, APIKey: apiKey, Timeout: 5},
		ChatRequest{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}})
	if err == nil {
		t.Fatal("401 必须报错")
	}
	if strings.Contains(err.Error(), apiKey) {
		t.Fatalf("错误文案泄露渠道 api_key：%s", err.Error())
	}
	if !strings.Contains(err.Error(), maskSecret(apiKey)) {
		t.Fatalf("须按 maskSecret 口径脱敏：%s", err.Error())
	}
	// 三处消费者（usage.error_message / 渠道 last_error / 探活回执）都取 ClassifyError 的 Message
	info := ClassifyError(err)
	if info.Class != ErrorClassAuth {
		t.Fatalf("失败分类 = %q，期望 %q", info.Class, ErrorClassAuth)
	}
	if strings.Contains(info.Message, apiKey) {
		t.Fatalf("usage.error_message 泄露密钥：%s", info.Message)
	}
}

// ===== Responses 协议：流式 =====

func TestResponsesDriverChatStreamTranslatesSemanticEvents(t *testing.T) {
	frames := []string{
		drvTestSSEFrame("response.created", `{"type":"response.created","response":{"id":"resp_9"}}`),
		drvTestSSEFrame("response.output_text.delta", `{"type":"response.output_text.delta","response_id":"resp_9","delta":"你"}`),
		drvTestSSEFrame("response.output_text.delta", `{"type":"response.output_text.delta","response_id":"resp_9","delta":"好"}`),
		drvTestSSEFrame("response.reasoning_summary_text.delta", `{"type":"response.reasoning_summary_text.delta","delta":"先想"}`),
		drvTestSSEFrame("response.completed", `{"type":"response.completed","response":{"id":"resp_9","usage":{"input_tokens":4,"output_tokens":2,"total_tokens":6}}}`),
		// 终局事件之后的帧不得再被读取/转发（驱动收到 completed 即停止读取）
		drvTestSSEFrame("response.output_text.delta", `{"delta":"不该出现"}`),
	}
	server, recorded := drvTestResponsesServer(t, frames, "", 0)
	reg := drvTestRegistry(t, true)

	var forwarded [][]byte
	res, err := reg.ChatStream(context.Background(), Channel{ID: 1, Driver: DriverNameResponses, BaseURL: server.URL, APIKey: "sk-drvTest", Timeout: 5},
		ChatRequest{Model: "deepseek-v4-flash", Messages: []Message{{Role: "user", Content: "hi"}}},
		func(data []byte) error {
			forwarded = append(forwarded, append([]byte(nil), data...))
			return nil
		})
	if err != nil {
		t.Fatalf("ChatStream 错误：%v", err)
	}
	if res.Content != "你好" || res.ReasoningContent != "先想" {
		t.Fatalf("聚合内容错误：%+v", res)
	}
	if !res.UsageSeen || res.PromptTokens != 4 || res.CompletionTokens != 2 {
		t.Fatalf("聚合 usage 错误（终局事件的用量帧未落库）：%+v", res)
	}
	if res.Chunks != 4 {
		t.Fatalf("chunks = %d，期望 4（3 增量 + 1 终局）", res.Chunks)
	}
	if res.Model != "deepseek-v4-flash" {
		t.Fatalf("结果模型名 = %q，期望请求侧解析名", res.Model)
	}
	if recorded.path != responsesPath || recorded.body["stream"] != true {
		t.Fatalf("流式请求装配错误：path=%q stream=%v", recorded.path, recorded.body["stream"])
	}

	if len(forwarded) != 4 {
		t.Fatalf("转发帧数 = %d，期望 4（终局后的帧不得转发）", len(forwarded))
	}
	for index, raw := range forwarded {
		if strings.Contains(string(raw), "不该出现") {
			t.Fatalf("转发帧 %d 含终局后的内容：%s", index, raw)
		}
		if string(raw) == "[DONE]" {
			t.Fatalf("驱动不得自行转发 [DONE]（引擎在驱动返回成功后才补 [DONE]）")
		}
		var chunk ChatChunk
		if err := json.Unmarshal(raw, &chunk); err != nil {
			t.Fatalf("转发帧 %d 不是合法 JSON：%v（%s）", index, err, raw)
		}
		if chunk.Model != "deepseek-v4-flash" {
			t.Fatalf("转发帧 %d model = %q", index, chunk.Model)
		}
		if index == 0 && chunk.Choices[0].Delta.Content != "你" {
			t.Fatalf("第 1 帧 = %s，期望 delta.content=你", raw)
		}
		if index == 2 && chunk.Choices[0].Delta.ReasoningContent != "先想" {
			t.Fatalf("第 3 帧 = %s，期望 delta.reasoning_content", raw)
		}
		if index == 3 {
			if chunk.Choices[0].FinishReason == nil || *chunk.Choices[0].FinishReason != "stop" {
				t.Fatalf("终局帧缺 finish_reason=stop：%s", raw)
			}
			if chunk.Usage == nil || chunk.Usage.PromptTokens != 4 || chunk.Usage.CompletionTokens != 2 {
				t.Fatalf("终局帧缺 usage：%s", raw)
			}
		}
	}
}

func TestResponsesDriverChatStreamAcceptsDoneSentinelAndTerminalOnlyStreams(t *testing.T) {
	reg := drvTestRegistry(t, true)
	ctx := context.Background()

	// [DONE] 收尾（与 chat 路径同口径）：语义化事件 + data: [DONE]
	doneFrames := []string{
		drvTestSSEFrame("response.output_text.delta", `{"delta":"半"}`),
		"data: [DONE]\n\n",
	}
	doneServer, _ := drvTestResponsesServer(t, doneFrames, "", 0)
	var forwarded [][]byte
	res, err := reg.ChatStream(ctx, Channel{ID: 1, Driver: DriverNameResponses, BaseURL: doneServer.URL, APIKey: "k", Timeout: 5},
		ChatRequest{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}},
		func(data []byte) error {
			forwarded = append(forwarded, data)
			return nil
		})
	if err != nil {
		t.Fatalf("[DONE] 收尾必须成功：%v", err)
	}
	if res.Content != "半" || len(forwarded) != 1 {
		t.Fatalf("聚合/转发 = %q/%d", res.Content, len(forwarded))
	}
	if strings.Contains(string(forwarded[0]), "[DONE]") {
		t.Fatal("[DONE] 是上游哨兵，不得转发给客户端")
	}

	// 网关只发终局事件（不发增量）：终局载荷的完整输出补一帧，usage 一并落 res
	terminalOnly := []string{
		drvTestSSEFrame("response.completed", `{"type":"response.completed","response":{"id":"resp_t","output_text":"整段回答","usage":{"input_tokens":3,"output_tokens":5}}}`),
	}
	terminalServer, _ := drvTestResponsesServer(t, terminalOnly, "", 0)
	var terminalFrames [][]byte
	res, err = reg.ChatStream(ctx, Channel{ID: 1, Driver: DriverNameResponses, BaseURL: terminalServer.URL, APIKey: "k", Timeout: 5},
		ChatRequest{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}},
		func(data []byte) error {
			terminalFrames = append(terminalFrames, data)
			return nil
		})
	if err != nil {
		t.Fatalf("终局-only 流必须成功：%v", err)
	}
	if res.Content != "整段回答" {
		t.Fatalf("终局载荷补偿失效：%q", res.Content)
	}
	if !res.UsageSeen || res.PromptTokens != 3 || res.CompletionTokens != 5 {
		t.Fatalf("终局 usage 未落 res：%+v", res)
	}
	if len(terminalFrames) != 2 || res.Chunks != 2 {
		t.Fatalf("转发/聚合帧数 = %d/%d，期望 2/2（正文补偿帧 + 终局帧）", len(terminalFrames), res.Chunks)
	}
}

func TestResponsesDriverChatStreamFailuresAreLoudAndNonRetryable(t *testing.T) {
	reg := drvTestRegistry(t, true)
	ctx := context.Background()
	ch := func(url string) Channel {
		return Channel{ID: 1, Driver: DriverNameResponses, BaseURL: url, APIKey: "k", Timeout: 5}
	}
	sink := func([]byte) error { return nil }

	// 空流（上游 200 但一帧不发）：与 OpenAI 驱动同口径的不可重试无效响应
	empty, _ := drvTestResponsesServer(t, nil, "", 0)
	res, err := reg.ChatStream(ctx, ch(empty.URL), ChatRequest{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}}, sink)
	if err == nil {
		t.Fatal("空流必须报错")
	}
	var upstream *UpstreamError
	if !errors.As(err, &upstream) || upstream.Code != "invalid_response" || upstream.Retryable {
		t.Fatalf("空流错误 = %+v，期望不可重试的 invalid_response", upstream)
	}
	if res == nil || res.Chunks != 0 {
		t.Fatalf("空流 res = %+v", res)
	}

	// 上游显式失败事件：class=upstream、code=response_failed、不可重试
	failed, _ := drvTestResponsesServer(t, []string{drvTestSSEFrame("response.failed", `{"type":"response.failed","response":{"error":{"message":"model not found"}}}`)}, "", 0)
	_, err = reg.ChatStream(ctx, ch(failed.URL), ChatRequest{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}}, sink)
	if err == nil {
		t.Fatal("response.failed 必须报错")
	}
	if !errors.As(err, &upstream) || upstream.Code != "response_failed" || upstream.Class != ErrorClassUpstream || upstream.Retryable {
		t.Fatalf("response.failed 分类 = %+v", upstream)
	}
	if !strings.Contains(err.Error(), "model not found") {
		t.Fatalf("失败文案未带上游 message：%s", err.Error())
	}

	// 事件流错帧：event: error ⇒ code=stream_error
	streamErr, _ := drvTestResponsesServer(t, []string{drvTestSSEFrame("error", `{"type":"error","message":"upstream overloaded"}`)}, "", 0)
	_, err = reg.ChatStream(ctx, ch(streamErr.URL), ChatRequest{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}}, sink)
	if !errors.As(err, &upstream) || upstream.Code != "stream_error" {
		t.Fatalf("event: error 分类 = %+v", upstream)
	}

	// 非法 JSON 帧：上游协议异常，不可重试
	badJSON, _ := drvTestResponsesServer(t, []string{drvTestSSEFrame("response.output_text.delta", `{"delta":`)}, "", 0)
	if _, err := reg.ChatStream(ctx, ch(badJSON.URL), ChatRequest{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}}, sink); err == nil {
		t.Fatal("非法帧必须报错")
	} else if !errors.As(err, &upstream) || upstream.Code != "invalid_response" || upstream.Retryable {
		t.Fatalf("非法帧错误 = %+v", upstream)
	}
}

func TestResponsesDriverChatStreamUpstreamAbortKeepsPartialContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Errorf("hijack 失败：%v", err)
			return
		}
		_, _ = fmt.Fprint(conn, "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nConnection: close\r\n\r\n")
		_, _ = fmt.Fprint(conn, drvTestSSEFrame("response.output_text.delta", `{"delta":"partial"}`))
		// 半帧后硬断（RST）：模拟上游中途断流
		_, _ = fmt.Fprint(conn, "event: response.output_text.delta\ndata: {\"delta\":\"trun")
		if tcp, ok := conn.(*net.TCPConn); ok {
			_ = tcp.SetLinger(0)
		}
		_ = conn.Close()
	}))
	defer server.Close()

	reg := drvTestRegistry(t, true)
	var forwarded int
	res, err := reg.ChatStream(context.Background(), Channel{ID: 1, Driver: DriverNameResponses, BaseURL: server.URL, APIKey: "k", Timeout: 5},
		ChatRequest{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}},
		func([]byte) error {
			forwarded++
			return nil
		})
	if err == nil {
		t.Fatal("上游中途断流必须报错（不得当成功收尾）")
	}
	if IsRetryable(err) {
		t.Fatalf("断流不得标记可重试（上游已计费）：%v", err)
	}
	if res == nil || res.Content != "partial" || forwarded != 1 {
		t.Fatalf("已聚合内容/已转发帧未保留：res=%+v forwarded=%d", res, forwarded)
	}
	if info := ClassifyError(err); info.Class != ErrorClassTransport {
		t.Fatalf("断流分类 = %+v，期望 transport", info)
	}
}

func TestResponsesDriverEmbeddingsIsRejectedAtDriverLevel(t *testing.T) {
	driver := newResponsesDriver(drvTestEnv(true), deepSeekDefaultBaseURL)
	_, err := driver.Embeddings(context.Background(), Channel{Driver: DriverNameResponses, BaseURL: "https://example.com", APIKey: "k"},
		EmbeddingRequest{Model: "text-embedding-v4", Input: "x"})
	upstream := drvTestLocalError(t, err, "unsupported_capability")
	if !strings.Contains(upstream.Message, "embedding") {
		t.Fatalf("拒绝文案 = %s", upstream.Message)
	}
}

// ===== 既有契约继承：出站护栏 / SSE 上限 / 脱敏（流内错误）=====

func TestRegistryGuardrailsRejectLoopbackUntilAllowPrivate(t *testing.T) {
	var hits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"resp_1","output_text":"ok"}`)
	}))
	defer server.Close()

	ctx := context.Background()
	req := ChatRequest{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}}

	// allow_private_hosts=0 ⇒ 连接建立前被护栏拦（Control 按实际 IP 判定），上游零命中
	blocked := drvTestRegistry(t, false)
	_, err := blocked.Chat(ctx, Channel{ID: 1, Driver: DriverNameResponses, BaseURL: server.URL, APIKey: "k", Timeout: 5}, req)
	if err == nil {
		t.Fatal("allow_private_hosts=0 时回环目标必须被出站护栏拒")
	}
	if !strings.Contains(err.Error(), "出站护栏拒绝连接") {
		t.Fatalf("护栏文案 = %s", err.Error())
	}
	if atomic.LoadInt32(&hits) != 0 {
		t.Fatalf("被护栏拦下的请求不得到达上游（命中 %d）", hits)
	}

	// allow_private_hosts=1 ⇒ 放行（本机伪上游/自建上游的前提）
	allowed := drvTestRegistry(t, true)
	res, err := allowed.Chat(ctx, Channel{ID: 1, Driver: DriverNameResponses, BaseURL: server.URL, APIKey: "k", Timeout: 5}, req)
	if err != nil {
		t.Fatalf("allow_private_hosts=1 必须放行：%v", err)
	}
	if res.Content != "ok" || atomic.LoadInt32(&hits) != 1 {
		t.Fatalf("放行路径异常：content=%q hits=%d", res.Content, hits)
	}

	// 别名驱动同样复用护栏（不是只有 Responses 生效）
	_, err = blocked.Chat(ctx, Channel{ID: 1, Driver: DriverNameDeepSeek, BaseURL: server.URL, APIKey: "k", Timeout: 5}, req)
	if err == nil || !strings.Contains(err.Error(), "出站护栏拒绝连接") {
		t.Fatalf("别名驱动未复用护栏：%v", err)
	}
}

func TestRegistryDoesNotFollowRedirect(t *testing.T) {
	var followed int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&followed, 1)
		_, _ = io.WriteString(w, `{"id":"resp_1","output_text":"redirected"}`)
	}))
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redirector.Close()

	reg := drvTestRegistry(t, true)
	_, err := reg.Chat(context.Background(), Channel{ID: 1, Driver: DriverNameResponses, BaseURL: redirector.URL, APIKey: "k", Timeout: 5},
		ChatRequest{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}})
	if err == nil {
		t.Fatal("重定向必须报错（防 SSRF-by-redirect）")
	}
	if !strings.Contains(err.Error(), "不跟随重定向") {
		t.Fatalf("重定向错误文案 = %s", err.Error())
	}
	if atomic.LoadInt32(&followed) != 0 {
		t.Fatal("护栏不得跟随重定向到目标地址")
	}
}

func TestResponsesDriverInheritsSSELimits(t *testing.T) {
	// 单行超限：不可重试的上游协议异常 + 停止读取
	lineServer, _ := drvTestResponsesServer(t, []string{drvTestSSEFrame("response.output_text.delta", `{"delta":"`+strings.Repeat("x", 256)+`"}`)}, "", 0)
	reg := NewDriverRegistry(DriverEnv{AllowPrivateHosts: true, SSELimits: SSELimits{MaxLineBytes: 64}})
	_, err := reg.ChatStream(context.Background(), Channel{ID: 1, Driver: DriverNameResponses, BaseURL: lineServer.URL, APIKey: "k", Timeout: 5},
		ChatRequest{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}}, func([]byte) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "单行超过上限") || IsRetryable(err) {
		t.Fatalf("单行超限错误 = %v（期望不可重试的单行上限异常）", err)
	}

	// 累积超限：不报错、帧照常转发、聚合内容截断并置 Truncated
	frames := []string{
		drvTestSSEFrame("response.output_text.delta", `{"delta":"abcd"}`),
		drvTestSSEFrame("response.output_text.delta", `{"delta":"efgh"}`),
		drvTestSSEFrame("response.output_text.delta", `{"delta":"ijkl"}`),
		drvTestSSEFrame("response.completed", `{"response":{"id":"r"}}`),
	}
	accServer, _ := drvTestResponsesServer(t, frames, "", 0)
	truncReg := NewDriverRegistry(DriverEnv{AllowPrivateHosts: true, SSELimits: SSELimits{MaxAccumulatedBytes: 8}})
	var forwarded int
	res, err := truncReg.ChatStream(context.Background(), Channel{ID: 1, Driver: DriverNameResponses, BaseURL: accServer.URL, APIKey: "k", Timeout: 5},
		ChatRequest{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}}, func([]byte) error {
			forwarded++
			return nil
		})
	if err != nil {
		t.Fatalf("累积超限不是错误：%v", err)
	}
	if !res.Truncated {
		t.Fatalf("累积超限须置 Truncated：%+v", res)
	}
	if res.Content != "abcdefgh" {
		t.Fatalf("聚合内容 = %q，期望阈值内的完整前缀", res.Content)
	}
	if forwarded != 4 {
		t.Fatalf("转发帧数 = %d，期望 4（超限不阻断转发）", forwarded)
	}
}

func TestResponsesDriverStreamErrorRedactsChannelKey(t *testing.T) {
	const apiKey = "sk-drvTestst-abcdefgh"
	// 流内错误事件回显密钥：驱动自造的错误文案也必须经脱敏出口
	frames := []string{drvTestSSEFrame("response.failed", `{"type":"response.failed","error":{"message":"rejected key `+apiKey+`"}}`)}
	server, _ := drvTestResponsesServer(t, frames, "", 0)

	reg := drvTestRegistry(t, true)
	_, err := reg.ChatStream(context.Background(), Channel{ID: 1, Driver: DriverNameResponses, BaseURL: server.URL, APIKey: apiKey, Timeout: 5},
		ChatRequest{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}}, func([]byte) error { return nil })
	if err == nil {
		t.Fatal("response.failed 必须报错")
	}
	if strings.Contains(err.Error(), apiKey) {
		t.Fatalf("流内错误文案泄露密钥：%s", err.Error())
	}
	if !strings.Contains(err.Error(), maskSecret(apiKey)) {
		t.Fatalf("流内错误未脱敏：%s", err.Error())
	}
}

// ===== 注册面：校验 / 指纹 / 自注册分派 =====

func TestRegisterValidatesAndFeedsSignature(t *testing.T) {
	before := RegistrySignature()
	bad := []DriverSpec{
		{Name: "", Capabilities: []string{CapabilityChat}, Factory: func(DriverEnv) Driver { return &drvTestFakeDriver{} }},
		{Name: "drvTest_bad_caps", Capabilities: nil, Factory: func(DriverEnv) Driver { return &drvTestFakeDriver{} }},
		{Name: "drvTest_bad_caps2", Capabilities: []string{"vision"}, Factory: func(DriverEnv) Driver { return &drvTestFakeDriver{} }},
		{Name: "drvTest_bad_factory", Capabilities: []string{CapabilityChat}},
	}
	for index, spec := range bad {
		if err := Register(spec); err == nil {
			t.Fatalf("非法规格 %d 必须被拒：%+v", index, spec)
		}
	}
	if RegistrySignature() != before {
		t.Fatal("被拒的注册不得改变注册集合指纹")
	}
	if drvTestContains(GetSupported(), "drvTest_bad_caps") {
		t.Fatal("被拒的注册不得进入 GetSupported()")
	}

	// 正常注册：进 GetSupported（名字归一为小写）+ 能力查询 + 指纹变化；注销后回到原指纹
	fake := &drvTestFakeDriver{models: []string{"tts-voice"}}
	if err := Register(DriverSpec{Name: "Fixture_Fake", Capabilities: []string{CapabilitySpeech, CapabilityChat}, Factory: func(DriverEnv) Driver { return fake }}); err != nil {
		t.Fatalf("Register 错误：%v", err)
	}
	after := RegistrySignature()
	if after == before {
		t.Fatal("注册后指纹必须变化（装配缓存键靠它感知注册集合变化）")
	}
	if !drvTestContains(GetSupported(), "fixture_fake") {
		t.Fatalf("GetSupported() = %v 未含归一后的名字", GetSupported())
	}
	if !SupportsCapability("Fixture_Fake", CapabilitySpeech) || SupportsCapability("fixture_fake", CapabilityEmbedding) {
		t.Fatal("能力查询未按声明返回")
	}
	unregisterDriverSpec("fixture_fake")
	if RegistrySignature() != before {
		t.Fatal("注销后指纹须回到原值")
	}
	if SupportsCapability("fixture_fake", CapabilityChat) {
		t.Fatal("注销后的驱动不得再被能力查询命中")
	}
}

// ===== 语音分派（service_tts.go 靠 `s.driver.(SpeechDriver)` 断言，注册表必须实现它）=====

// drvTestFakeSpeaker 是实现了 SpeechDriver 的假驱动（记录被分派到的渠道）。
type drvTestFakeSpeaker struct {
	channels []int64
}

func (s *drvTestFakeSpeaker) Speak(_ context.Context, ch Channel, req SpeechRequest) (*SpeechResult, error) {
	s.channels = append(s.channels, ch.ID)
	return &SpeechResult{Model: req.Model, Voice: req.Voice, Audio: []byte("mp3-bytes"), ContentType: "audio/mpeg"}, nil
}

// 语音驱动同样要实现 Driver 四方法（dashscope_tts 也是这个形态：非语音能力响亮拒绝）。
func (s *drvTestFakeSpeaker) Chat(context.Context, Channel, ChatRequest) (*ChatResult, error) {
	return nil, localError("unsupported_capability", "语音驱动不支持 chat")
}

func (s *drvTestFakeSpeaker) ChatStream(context.Context, Channel, ChatRequest, func([]byte) error) (*ChatResult, error) {
	return nil, localError("unsupported_capability", "语音驱动不支持 chat")
}

func (s *drvTestFakeSpeaker) Embeddings(context.Context, Channel, EmbeddingRequest) (*EmbeddingResult, error) {
	return nil, localError("unsupported_capability", "语音驱动不支持 embedding")
}

func (s *drvTestFakeSpeaker) Models(context.Context, Channel) ([]string, error) { return nil, nil }

func TestRegistryDispatchesSpeechByChannelDriver(t *testing.T) {
	speaker := &drvTestFakeSpeaker{}
	if err := Register(DriverSpec{Name: "drvTest_tts", Capabilities: []string{CapabilitySpeech}, Factory: func(DriverEnv) Driver { return speaker }}); err != nil {
		t.Fatalf("Register 错误：%v", err)
	}
	defer unregisterDriverSpec("drvTest_tts")

	reg := drvTestRegistry(t, true)
	// 语音断言面：装配后 Service 持有的 driver 就是注册表，断言必须在注册表上成立
	var asDriver Driver = reg
	asserted, ok := asDriver.(SpeechDriver)
	if !ok {
		t.Fatal("注册表必须实现 service_tts.go 的 SpeechDriver 窄接口（否则语音路由恒报『驱动不支持语音合成』）")
	}
	if declared, ok := asDriver.(interface{ SupportsSpeech() bool }); ok && !declared.SupportsSpeech() {
		t.Fatal("注册表刻意不实现 SupportsSpeech()（无渠道入参，逐渠道判断落在 Speak 内）")
	}

	res, err := asserted.Speak(context.Background(), Channel{ID: 42, Driver: "drvTest_tts"}, SpeechRequest{Model: "m", Voice: "v1", Text: "你好"})
	if err != nil {
		t.Fatalf("Speak 分派错误：%v", err)
	}
	if string(res.Audio) != "mp3-bytes" || len(speaker.channels) != 1 || speaker.channels[0] != 42 {
		t.Fatalf("语音未按渠道分派：res=%+v channels=%v", res, speaker.channels)
	}

	// 未注册驱动 ⇒ unknown_driver（不静默回落）
	if _, err := reg.Speak(context.Background(), Channel{ID: 1, Driver: "wangsu"}, SpeechRequest{Text: "x"}); err == nil {
		t.Fatal("未知驱动必须被拒")
	} else {
		drvTestLocalError(t, err, "unknown_driver")
	}
	// 未声明 speech 的驱动（openai）⇒ 能力门拒
	if _, err := reg.Speak(context.Background(), Channel{ID: 1, Driver: DriverNameOpenAI}, SpeechRequest{Text: "x"}); err == nil {
		t.Fatal("openai 未声明 speech，必须被能力门拒")
	} else {
		upstream := drvTestLocalError(t, err, "unsupported_capability")
		if !strings.Contains(upstream.Message, CapabilitySpeech) {
			t.Fatalf("语音能力门文案 = %s", upstream.Message)
		}
	}
	// 声明了 speech 但实例没实现 SpeechDriver ⇒ 同样响亮的失败
	if err := Register(DriverSpec{Name: "drvTest_tts_broken", Capabilities: []string{CapabilitySpeech}, Factory: func(DriverEnv) Driver { return &drvTestFakeDriver{} }}); err != nil {
		t.Fatal(err)
	}
	defer unregisterDriverSpec("drvTest_tts_broken")
	if _, err := reg.Speak(context.Background(), Channel{ID: 1, Name: "坏渠道", Driver: "drvTest_tts_broken"}, SpeechRequest{Text: "x"}); err == nil {
		t.Fatal("未实现 SpeechDriver 的驱动必须响亮失败")
	} else {
		drvTestLocalError(t, err, "driver_not_support_speech")
	}
}

func TestRegisteredDriverIsDispatchedThroughRegistry(t *testing.T) {
	fake := &drvTestFakeDriver{models: []string{"voice-1"}}
	if err := Register(DriverSpec{Name: "drvTest_dispatch", Capabilities: []string{CapabilityChat, CapabilityEmbedding}, Factory: func(DriverEnv) Driver { return fake }}); err != nil {
		t.Fatalf("Register 错误：%v", err)
	}
	defer unregisterDriverSpec("drvTest_dispatch")

	reg := drvTestRegistry(t, true)
	ctx := context.Background()
	ch := Channel{ID: 1, Driver: "drvTest_dispatch"}
	res, err := reg.Chat(ctx, ch, ChatRequest{Model: "m"})
	if err != nil {
		t.Fatalf("自注册驱动的分派调用错误：%v", err)
	}
	if res.Content != "fake" || atomic.LoadInt32(&fake.chatCalls) != 1 {
		t.Fatalf("自注册驱动未被分派：res=%+v calls=%d", res, fake.chatCalls)
	}
	if _, err := reg.Embeddings(ctx, ch, EmbeddingRequest{Model: "m", Input: "x"}); err != nil {
		t.Fatalf("Embeddings 分派错误：%v", err)
	}
	models, err := reg.Models(ctx, ch)
	if err != nil || len(models) != 1 || models[0] != "voice-1" {
		t.Fatalf("Models 分派错误：%v %v", models, err)
	}
	if atomic.LoadInt32(&fake.embeddingCalls) != 1 || atomic.LoadInt32(&fake.modelsCalls) != 1 {
		t.Fatalf("调用计数 = %d/%d", fake.embeddingCalls, fake.modelsCalls)
	}
}

func TestRegistrySignatureIsStableAndCoversCapabilities(t *testing.T) {
	first := RegistrySignature()
	if first != RegistrySignature() {
		t.Fatal("同集合的指纹必须稳定（装配缓存键不能抖动）")
	}
	if len(first) != 16 {
		t.Fatalf("指纹长度 = %d", len(first))
	}
	// 同名替换但能力不同 ⇒ 指纹必须变化（否则换了实现/能力后旧实例会被继续复用）
	if err := Register(DriverSpec{Name: "drvTest_replace", Capabilities: []string{CapabilityChat}, Factory: func(DriverEnv) Driver { return &drvTestFakeDriver{} }}); err != nil {
		t.Fatal(err)
	}
	defer unregisterDriverSpec("drvTest_replace")
	withChat := RegistrySignature()
	if err := Register(DriverSpec{Name: "drvTest_replace", Capabilities: []string{CapabilityChat, CapabilitySpeech}, Factory: func(DriverEnv) Driver { return &drvTestFakeDriver{} }}); err != nil {
		t.Fatal(err)
	}
	if RegistrySignature() == withChat {
		t.Fatal("同名驱动的能力扩展必须改变指纹")
	}
}
