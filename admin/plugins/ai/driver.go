// plugin: ai v0.2.0
// driver.go 是 OpenAI 兼容驱动的唯一实现（v1 只有这一种驱动）。
//
// 端点：{base_url}/chat/completions、{base_url}/embeddings、{base_url}/models。
// 凭证：Authorization: Bearer <api_key>——**永不进日志、永不出现在错误文案**。
// 超时：非流式按渠道生效 timeout（0⇒插件配置 request_timeout，默认 60s）设 context deadline；
// 流式不设总超时（由编排层的 stream_timeout+15s 护栏收口），连接期
// ResponseHeaderTimeout 仍按渠道 timeout。
// 出站护栏：base_url 写入期校验 + 连接期按实际 IP 拒绝回环/内网/链路本地/未指定
// （allow_private_hosts=1 时放行，供本机 ollama 等自建上游），重定向一律不跟随。
// 护栏的**唯一构造出口**是 newGuardedHTTPClient（本文件）——新增驱动（driver_registry）
// 必须用它构造 http.Client，不得各写一套（契约，见 plugins/ai/README.md 安全节）。
//
// 流式上限：单行/单帧/累积三级（SSELimits，缺省 1MiB/8MiB/32MiB，走插件配置 sse_max_*）；
// 单行与单帧超限 ⇒ 停止读取 + 不可重试的上游协议异常，累积超限 ⇒ 截断标记（不报错）。
//
// 失败分类只认结构化信号（HTTP 状态码、context deadline、net.Error.Timeout），不解析错误文案；
// 只有「连接建立阶段失败（请求字节尚未写出）」标记为可重试，避免上游已计费却重复调用。
// 所有出口错误都用渠道 api_key 脱敏（redactUpstreamErr）：usage.error_message、渠道 last_error、
// 探活落库与回执因此共用同一个脱敏出口。
package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// 上游失败分类闭集（plugin_ai_usage.error_class 的 CHECK）。
const (
	ErrorClassAuth      = "auth"
	ErrorClassRateLimit = "rate_limit"
	ErrorClassUpstream  = "upstream"
	ErrorClassTransport = "transport"
	ErrorClassLocal     = "local"
	ErrorClassUnknown   = "unknown"
)

// 默认请求超时（插件配置 request_timeout 的默认值）。
const DefaultRequestTimeout = 60

// Message 是 OpenAI 消息体（content 可为字符串或多模态数组）。
type Message struct {
	Role       string `json:"role"`
	Content    any    `json:"content,omitempty"`
	Name       string `json:"name,omitempty"`
	ToolCalls  any    `json:"tool_calls,omitempty"`
	ToolCallID string `json:"tool_call_id,omitempty"`
}

// ChatRequest 是驱动层的对话请求。
//
// Extra 会原样并入请求体（核心字段 model/messages/stream 优先，Extra 不能覆盖）。
type ChatRequest struct {
	Model          string
	Messages       []Message
	Temperature    *float64
	TopP           *float64
	MaxTokens      *int
	Stop           any
	Tools          any
	ToolChoice     any
	ResponseFormat any
	User           string
	Extra          map[string]any
}

// ChatResult 是非流式/流式聚合后的响应。
type ChatResult struct {
	ID               string
	Model            string
	Content          string
	ReasoningContent string
	FinishReason     string
	ToolCalls        json.RawMessage

	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	CachedTokens     int64
	UsageSeen        bool

	LatencyMS int
	Chunks    int
	Aborted   bool
	// Truncated 表示流式聚合内容触到累积上限（SSELimits.MaxAccumulatedBytes）而被截断：
	// 已收到的内容仍在 Content/ReasoningContent 里（长度停在阈值内），只是不再增长。
	// 上游 usage 优先的计费口径不变（见 ResolveStreamUsage）。
	Truncated bool
}

// EmbeddingRequest 是驱动层的向量请求。Input 为 string 或 []string。
type EmbeddingRequest struct {
	Model string
	Input any
	Extra map[string]any
}

// Embedding 是单条向量结果（Index 为分片内位次；编排层会改写为全局位次）。
type Embedding struct {
	Index  int
	Object string
	Vector []float64
}

// EmbeddingResult 是驱动层的向量响应。
type EmbeddingResult struct {
	Model            string
	Data             []Embedding
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	CachedTokens     int64
	UsageSeen        bool
	LatencyMS        int
}

// UsageInfo 是上游 usage 的原样结构。
//
// cached_tokens 优先取 prompt_tokens_details.cached_tokens，为 0 再退化
// prompt_cache_hit_tokens；两者都没有时为 0（≠ 没命中）。
type UsageInfo struct {
	PromptTokens         int                  `json:"prompt_tokens"`
	CompletionTokens     int                  `json:"completion_tokens"`
	TotalTokens          int                  `json:"total_tokens"`
	PromptTokensDetails  *PromptTokensDetails `json:"prompt_tokens_details,omitempty"`
	PromptCacheHitTokens int64                `json:"prompt_cache_hit_tokens,omitempty"`
}

// PromptTokensDetails 是 OpenAI 的 prompt 缓存明细。
type PromptTokensDetails struct {
	CachedTokens int64 `json:"cached_tokens"`
}

// CachedTokens 取缓存命中输入 token（只落库，不进成本公式）。
func (u *UsageInfo) CachedTokens() int64 {
	if u == nil {
		return 0
	}
	if u.PromptTokensDetails != nil && u.PromptTokensDetails.CachedTokens > 0 {
		return u.PromptTokensDetails.CachedTokens
	}
	return u.PromptCacheHitTokens
}

// Driver 是编排层依赖的上游能力接口（v1 只有 OpenAIDriver 一个实现）。
type Driver interface {
	Chat(ctx context.Context, ch Channel, req ChatRequest) (*ChatResult, error)
	ChatStream(ctx context.Context, ch Channel, req ChatRequest, sink func(data []byte) error) (*ChatResult, error)
	Embeddings(ctx context.Context, ch Channel, req EmbeddingRequest) (*EmbeddingResult, error)
	Models(ctx context.Context, ch Channel) ([]string, error)
}

// UpstreamError 是驱动层统一错误。
//
//	HTTPStatus>0  ⇒ 按 401/403=auth、429=rate_limit、其余=upstream 分类；
//	HTTPStatus==0 ⇒ transport（建链/超时/断连）；
//	Class=local   ⇒ 本地错误（凭证未配/入参/base_url/JSON 编码）。
//
// Retryable 只对「请求字节尚未写出的连接建立阶段失败」为 true；
// embedding 的「缺 data/条数不一致」是幂等安全的重试，单独标记。
type UpstreamError struct {
	HTTPStatus int
	Class      string
	Code       string
	Message    string
	Retryable  bool
	Err        error
}

func (e *UpstreamError) Error() string {
	if e == nil {
		return ""
	}
	if e.Message != "" {
		return e.Message
	}
	return e.Code
}

func (e *UpstreamError) Unwrap() error { return e.Err }

// ErrorInfo 是失败分类结果，直接映射 usage 的 status/error_class/error_code/error_message。
type ErrorInfo struct {
	Class   string
	Code    string
	Message string
	Status  string
}

// ClassifyError 产出失败分类闭集与 status（timeout|fail）。
//
// 只认结构化信号：context deadline/exceeded、net.Error.Timeout()、上游 504。
// 不解析错误文案（与源 PHP 的正则判定刻意不同，详见 README 差异节）。
func ClassifyError(err error) ErrorInfo {
	info := ErrorInfo{Class: ErrorClassUnknown, Code: "nil", Status: UsageStatusFail}
	if err == nil {
		return info
	}
	var aborted *StreamAbortedError
	if errors.As(err, &aborted) {
		info.Class = ErrorClassTransport
		info.Code = "client_closed"
		info.Message = "客户端已断开"
		return info
	}
	var ae *APIError
	if errors.As(err, &ae) {
		info.Class = ae.Class
		if info.Class == "" {
			info.Class = ErrorClassLocal
		}
		info.Code = ae.Code
		if info.Code == "" {
			info.Code = errorTypeName(err)
		}
		info.Message = truncateMessage(ae.Message, 500)
		return info
	}
	var ue *UpstreamError
	if errors.As(err, &ue) {
		info.Class = ue.Class
		if info.Class == "" {
			info.Class = ErrorClassUpstream
		}
		info.Code = ue.Code
		if info.Code == "" {
			info.Code = errorTypeName(err)
		}
		info.Message = truncateMessage(ue.Message, 500)
		if IsTimeoutError(err) {
			info.Status = UsageStatusTimeout
		}
		return info
	}
	info.Code = errorTypeName(err)
	info.Message = truncateMessage(err.Error(), 500)
	switch {
	case IsTimeoutError(err):
		info.Class = ErrorClassTransport
		info.Code = "timeout"
		info.Status = UsageStatusTimeout
	case errors.Is(err, context.Canceled):
		info.Class = ErrorClassTransport
		info.Code = "canceled"
	default:
		info.Class = ErrorClassUnknown
	}
	return info
}

// IsTimeoutError 是结构化超时判定（context deadline、net.Error.Timeout、上游 504）。
func IsTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return true
	}
	var ue *UpstreamError
	if errors.As(err, &ue) && ue.HTTPStatus == http.StatusGatewayTimeout {
		return true
	}
	return false
}

// IsRetryable 是否允许在**同一候选内**重试（传输层重试预算 3 次）。
func IsRetryable(err error) bool {
	var ue *UpstreamError
	if errors.As(err, &ue) {
		return ue.Retryable
	}
	return false
}

// EffectiveTimeout 渠道 timeout 的有效值：0=未配 ⇒ 60s；上限收敛到 600s。
func EffectiveTimeout(seconds int) int {
	if seconds <= 0 {
		return DefaultRequestTimeout
	}
	if seconds > 600 {
		return 600
	}
	return seconds
}

// ValidateBaseURL 是 base_url 的写入期校验：必须 http/https、可解析、无 userinfo/query/fragment。
//
// 连接期的内网拦截在 OpenAIDriver.control 里按实际 IP 执行（DNS 解析之后）。
func ValidateBaseURL(raw string) error {
	s := strings.TrimSpace(raw)
	if s == "" {
		return errors.New("base_url 不能为空")
	}
	u, err := url.Parse(s)
	if err != nil {
		return fmt.Errorf("base_url 不是合法 URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("base_url 只支持 http/https，当前为 %q", u.Scheme)
	}
	if u.Host == "" {
		return errors.New("base_url 缺少主机名")
	}
	if u.User != nil {
		return errors.New("base_url 不得包含用户名/密码，凭证请填写 api_key")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return errors.New("base_url 不应包含查询串或片段")
	}
	return nil
}

// OpenAIDriver 是 OpenAI 兼容驱动。
//
// http.Client 按超时值分桶复用（ResponseHeaderTimeout 是客户端级配置，不能逐请求改）；
// 不设客户端总超时——流式必须由 context 控制。
// limits 是流式解析的三级上限（单行/单帧/累积），由插件配置 sse_max_* 传入。
type OpenAIDriver struct {
	allowPrivate bool
	limits       SSELimits

	mu      sync.Mutex
	clients map[int]*http.Client
}

// NewOpenAIDriver 构造驱动（SSE 上限取缺省 1MiB/8MiB/32MiB）。
// allowPrivate=true 时放行本机/内网目标（ollama 等）。
func NewOpenAIDriver(allowPrivate bool) *OpenAIDriver {
	return NewOpenAIDriverWithSSELimits(allowPrivate, DefaultSSELimits())
}

// NewOpenAIDriverWithSSELimits 构造驱动并指定流式三级上限（插件配置 sse_max_* 直接传入；
// ≤0 的字段回落缺省）。
func NewOpenAIDriverWithSSELimits(allowPrivate bool, limits SSELimits) *OpenAIDriver {
	return &OpenAIDriver{
		allowPrivate: allowPrivate,
		limits:       limits.normalized(),
		clients:      make(map[int]*http.Client),
	}
}

func (d *OpenAIDriver) client(timeoutSeconds int) *http.Client {
	seconds := EffectiveTimeout(timeoutSeconds)
	d.mu.Lock()
	defer d.mu.Unlock()
	if c, ok := d.clients[seconds]; ok {
		return c
	}
	c := newGuardedHTTPClient(d.allowPrivate, seconds)
	d.clients[seconds] = c
	return c
}

// newGuardedHTTPClient 构造带出站护栏的 http.Client（**新驱动的唯一构造出口**，
// 供 driver_registry 里各协议实现复用，避免每个驱动各写一套护栏）。
//
// 护栏内容：
//   - Proxy 关死：不读 HTTP_PROXY/HTTPS_PROXY——代理会让 Control 只看到代理 IP，绕过内网判定；
//   - 连接建立前按**实际目标 IP**（DNS 已解析）拒绝回环/内网/链路本地/未指定/组播，除非 allowPrivate；
//   - 不跟随重定向（防 SSRF-by-redirect；返回可读错误而不是静默跟到内网）；
//   - ResponseHeaderTimeout 按生效 timeout（0 ⇒ 60s，上限 600s）设连接期超时。
//
// timeoutSeconds ≤ 0 时按 DefaultRequestTimeout 归一（与渠道 timeout 同口径）。
func newGuardedHTTPClient(allowPrivate bool, timeoutSeconds int) *http.Client {
	seconds := EffectiveTimeout(timeoutSeconds)
	transport := &http.Transport{
		Proxy: nil,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
			Control:   guardedDialControl(allowPrivate),
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: time.Duration(seconds) * time.Second,
		ExpectContinueTimeout: time.Second,
	}
	return &http.Client{
		Transport: transport,
		// 不跟随重定向：防 SSRF-by-redirect；返回可读错误而不是静默跟到内网。
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			from := "?"
			if len(via) > 0 && via[0] != nil && via[0].URL != nil {
				from = via[0].URL.String()
			}
			return fmt.Errorf("上游返回重定向（%s → %s），出站护栏不跟随重定向", from, req.URL)
		},
	}
}

// guardedDialControl 在 TCP 连接建立前检查实际目标 IP（DNS 已解析）。
func guardedDialControl(allowPrivate bool) func(network, address string, c syscall.RawConn) error {
	return func(_, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return fmt.Errorf("出站护栏无法解析目标地址 %q: %w", address, err)
		}
		ip := net.ParseIP(host)
		if ip == nil {
			return nil // 理论上不会发生：Dialer 已把域名解析成 IP。
		}
		if allowPrivate || !isBlockedIP(ip) {
			return nil
		}
		return fmt.Errorf("出站护栏拒绝连接 %s：目标属于本机回环/内网/链路本地/未指定地址；本机自建上游请在 AI 设置里开启 allow_private_hosts", ip.String())
	}
}

func isBlockedIP(ip net.IP) bool {
	return ip.IsLoopback() ||
		ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() ||
		ip.IsMulticast()
}

func endpointURL(base, path string) (string, error) {
	if err := ValidateBaseURL(base); err != nil {
		return "", err
	}
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil {
		return "", err
	}
	u.Path = strings.TrimRight(u.Path, "/") + path
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}

// Chat 执行非流式对话。
//
// 出口纪律（与 redact.go 的契约）：错误文案在返回前统一用本渠道 api_key 脱敏，
// 使 usage.error_message / 渠道 last_error / 探活落库与回执全部经**同一出口**。
func (d *OpenAIDriver) Chat(ctx context.Context, ch Channel, req ChatRequest) (result *ChatResult, err error) {
	defer func() { err = redactUpstreamErr(err, ch.APIKey) }()
	if strings.TrimSpace(ch.APIKey) == "" {
		return nil, localError("missing_api_key", "渠道未配置 api_key")
	}
	callCtx, cancel := context.WithTimeout(ctx, time.Duration(EffectiveTimeout(ch.Timeout))*time.Second)
	defer cancel()

	start := time.Now()
	resp, err := d.postJSON(callCtx, ch, "/chat/completions", req.body(ch, false))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var out chatCompletionResponse
	if err := decodeJSON(resp.Body, &out); err != nil {
		return nil, upstreamInvalid("上游响应不是合法 JSON: " + err.Error())
	}
	if len(out.Choices) == 0 {
		return nil, upstreamInvalid("上游响应缺少 choices 数组")
	}
	choice := out.Choices[0]
	content := ContentText(choice.Message.Content)
	if content == "" && isEmptyRawJSON(choice.Message.ToolCalls) {
		return nil, upstreamInvalid("上游返回空回复")
	}
	res := &ChatResult{
		ID:               out.ID,
		Model:            out.Model,
		Content:          content,
		ReasoningContent: choice.Message.ReasoningContent,
		FinishReason:     choice.FinishReason,
		ToolCalls:        choice.Message.ToolCalls,
		LatencyMS:        int(time.Since(start).Milliseconds()),
	}
	applyUsage(res, out.Usage)
	return res, nil
}

// ChatStream 执行流式对话。
//
// 每收到一个 data: 帧就回调 sink（原始 JSON 字节，便于网关原样转发）；
// 聚合 content 与 usage，并在客户端断开/回调写失败时立即停止读取、返回部分结果。
// 返回的 error 为 *StreamAbortedError 表示客户端断开（已产生的聚合内容仍在 res 里）。
//
// 上限：单行/单帧超限 ⇒ 立即停止读取并返回不可重试的上游协议异常（res 里保留已聚合内容）；
// 累积内容超限 ⇒ 不报错，置 res.Truncated 并停止累积（帧继续转发，计费口径不变）。
func (d *OpenAIDriver) ChatStream(ctx context.Context, ch Channel, req ChatRequest, sink func(data []byte) error) (result *ChatResult, err error) {
	defer func() { err = redactUpstreamErr(err, ch.APIKey) }()
	if strings.TrimSpace(ch.APIKey) == "" {
		return nil, localError("missing_api_key", "渠道未配置 api_key")
	}
	start := time.Now()
	resp, err := d.postJSON(ctx, ch, "/chat/completions", req.body(ch, true))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	acc := NewStreamAccumulator(d.limits)
	parseErr := ParseSSEWithLimits(resp.Body, d.limits, func(ev SSEEvent) error {
		data := bytes.TrimSpace(ev.Data)
		if len(data) == 0 {
			return nil
		}
		if string(data) == "[DONE]" {
			return errSSEDone
		}
		chunk, err := ParseChatChunk(data)
		if err != nil {
			return err
		}
		acc.Apply(chunk)
		if sink == nil {
			return nil
		}
		if err := sink(data); err != nil {
			acc.Aborted = true
			return &StreamAbortedError{Err: err}
		}
		return nil
	})

	res := acc.Result()
	res.LatencyMS = int(time.Since(start).Milliseconds())
	switch {
	case errors.Is(parseErr, errSSEDone):
		return res, nil
	case parseErr == nil:
		if res.Chunks == 0 {
			return res, upstreamInvalid("上游流式响应没有任何数据块")
		}
		return res, nil
	}

	var aborted *StreamAbortedError
	if errors.As(parseErr, &aborted) {
		return res, parseErr
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return res, classifyTransportError(ctxErr, false)
	}
	var ue *UpstreamError
	if errors.As(parseErr, &ue) {
		return res, parseErr
	}
	return res, classifyTransportError(parseErr, false)
}

// Embeddings 执行向量请求。
//
// 单请求条数上限由编排层分片（20/片）；本方法只校验「返回条数与输入一致」。
//
// 2xx 但 data 缺失 / 条数不一致按**不可重试**的无效响应处理（upstreamInvalid）：
// 走到这里上游已经收了请求并回了 2xx（上游可能已计费），而重发没有幂等键、对上游不可区分
// ——重试只会让上游账单可能大于本地记录（本地每个候选尝试只落 1 行 usage）。
// 同口径：流式解析里「没有任何数据块」也是 invalid_response（本就不可重试）。
func (d *OpenAIDriver) Embeddings(ctx context.Context, ch Channel, req EmbeddingRequest) (result *EmbeddingResult, err error) {
	defer func() { err = redactUpstreamErr(err, ch.APIKey) }()
	if strings.TrimSpace(ch.APIKey) == "" {
		return nil, localError("missing_api_key", "渠道未配置 api_key")
	}
	want := EmbeddingInputCount(req.Input)
	if want <= 0 {
		return nil, localError("empty_input", "embedding input 不能为空")
	}
	callCtx, cancel := context.WithTimeout(ctx, time.Duration(EffectiveTimeout(ch.Timeout))*time.Second)
	defer cancel()

	start := time.Now()
	resp, err := d.postJSON(callCtx, ch, "/embeddings", req.body(ch))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var out embeddingResponse
	if err := decodeJSON(resp.Body, &out); err != nil {
		return nil, upstreamInvalid("上游响应不是合法 JSON: " + err.Error())
	}
	if out.Data == nil {
		return nil, upstreamInvalid("上游 embedding 响应缺少 data 数组")
	}
	if len(out.Data) != want {
		return nil, upstreamInvalid(fmt.Sprintf("上游 embedding 条数与输入不一致: %d != %d", len(out.Data), want))
	}
	data := make([]Embedding, 0, len(out.Data))
	for i, item := range out.Data {
		if item.Embedding == nil {
			return nil, upstreamInvalid(fmt.Sprintf("上游 embedding 第 %d 条缺少向量", i))
		}
		data = append(data, Embedding{Index: item.Index, Object: item.Object, Vector: item.Embedding})
	}
	sort.SliceStable(data, func(i, j int) bool { return data[i].Index < data[j].Index })

	res := &EmbeddingResult{
		Model:     out.Model,
		Data:      data,
		LatencyMS: int(time.Since(start).Milliseconds()),
	}
	if out.Usage != nil {
		res.UsageSeen = true
		res.PromptTokens = out.Usage.PromptTokens
		res.CompletionTokens = out.Usage.CompletionTokens
		res.TotalTokens = out.Usage.PromptTokens + out.Usage.CompletionTokens
		res.CachedTokens = out.Usage.CachedTokens()
	}
	return res, nil
}

// Models 调用 GET {base_url}/models（手动探活复用本方法；2xx 即成功）。
func (d *OpenAIDriver) Models(ctx context.Context, ch Channel) (models []string, err error) {
	defer func() { err = redactUpstreamErr(err, ch.APIKey) }()
	if strings.TrimSpace(ch.APIKey) == "" {
		return nil, localError("missing_api_key", "渠道未配置 api_key")
	}
	callCtx, cancel := context.WithTimeout(ctx, time.Duration(EffectiveTimeout(ch.Timeout))*time.Second)
	defer cancel()

	u, err := endpointURL(ch.BaseURL, "/models")
	if err != nil {
		return nil, localError("invalid_base_url", err.Error())
	}
	req, err := http.NewRequestWithContext(callCtx, http.MethodGet, u, nil)
	if err != nil {
		return nil, localError("invalid_request", err.Error())
	}
	req.Header.Set("Authorization", "Bearer "+ch.APIKey)
	req.Header.Set("Accept", "application/json")

	resp, err := d.do(req, ch.Timeout)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := decodeJSON(resp.Body, &out); err != nil {
		return nil, upstreamInvalid("上游 /models 响应不是合法 JSON: " + err.Error())
	}
	if out.Data == nil {
		return nil, upstreamInvalid("上游 /models 响应缺少 data 数组")
	}
	ids := make([]string, 0, len(out.Data))
	for _, item := range out.Data {
		if item.ID != "" {
			ids = append(ids, item.ID)
		}
	}
	return ids, nil
}

// EmbeddingInputCount 返回 input 的条数（string=1，数组=len）。
func EmbeddingInputCount(input any) int {
	switch v := input.(type) {
	case nil:
		return 0
	case string:
		return 1
	case []string:
		return len(v)
	case []any:
		return len(v)
	default:
		return 1
	}
}

func (r ChatRequest) body(ch Channel, stream bool) map[string]any {
	body := make(map[string]any, len(r.Extra)+8)
	for k, v := range r.Extra {
		body[k] = v
	}
	delete(body, "stream")
	delete(body, "stream_options")

	model := ch.Model
	if model == "" {
		model = r.Model
	}
	body["model"] = model
	body["messages"] = r.Messages
	if r.Temperature != nil {
		body["temperature"] = *r.Temperature
	}
	if r.TopP != nil {
		body["top_p"] = *r.TopP
	}
	if r.MaxTokens != nil {
		body["max_tokens"] = *r.MaxTokens
	}
	if r.Stop != nil {
		body["stop"] = r.Stop
	}
	if r.Tools != nil {
		body["tools"] = r.Tools
	}
	if r.ToolChoice != nil {
		body["tool_choice"] = r.ToolChoice
	}
	if r.ResponseFormat != nil {
		body["response_format"] = r.ResponseFormat
	}
	if r.User != "" {
		body["user"] = r.User
	}
	if stream {
		body["stream"] = true
		// 让 OpenAI 兼容上游在末尾补一个 usage 帧；部分上游忽略未知字段，
		// 忽略时编排层按内容做近似估算兜底。
		body["stream_options"] = map[string]any{"include_usage": true}
	}
	return body
}

func (r EmbeddingRequest) body(ch Channel) map[string]any {
	body := make(map[string]any, len(r.Extra)+2)
	for k, v := range r.Extra {
		body[k] = v
	}
	model := ch.Model
	if model == "" {
		model = r.Model
	}
	body["model"] = model
	body["input"] = r.Input
	return body
}

func (d *OpenAIDriver) postJSON(ctx context.Context, ch Channel, path string, body any) (*http.Response, error) {
	u, err := endpointURL(ch.BaseURL, path)
	if err != nil {
		return nil, localError("invalid_base_url", err.Error())
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, localError("encode_request", "编码上游请求失败: "+err.Error())
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(payload))
	if err != nil {
		return nil, localError("invalid_request", err.Error())
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	// 密钥只进请求头；任何错误文案都不拼接 Header。
	req.Header.Set("Authorization", "Bearer "+ch.APIKey)
	return d.do(req, ch.Timeout)
}

func (d *OpenAIDriver) do(req *http.Request, timeoutSeconds int) (*http.Response, error) {
	var wrote atomic.Bool
	trace := &httptrace.ClientTrace{
		WroteRequest: func(httptrace.WroteRequestInfo) { wrote.Store(true) },
	}
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), trace))

	resp, err := d.client(timeoutSeconds).Do(req)
	if err != nil {
		return nil, classifyTransportError(err, !wrote.Load())
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		return nil, upstreamErrorFromResponse(resp)
	}
	return resp, nil
}

func classifyTransportError(err error, connectPhase bool) *UpstreamError {
	code := "transport"
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		code = "timeout"
	case errors.Is(err, context.Canceled):
		code = "canceled"
	default:
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			code = "timeout"
		}
	}
	return &UpstreamError{
		Class:     ErrorClassTransport,
		Code:      code,
		Message:   truncateMessage(err.Error(), 500),
		Retryable: connectPhase,
		Err:       err,
	}
}

func upstreamErrorFromResponse(resp *http.Response) *UpstreamError {
	const maxBody = 64 << 10
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	message := ""
	code := ""
	var envelope struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    any    `json:"code"`
		} `json:"error"`
	}
	if len(body) > 0 && json.Unmarshal(body, &envelope) == nil {
		message = envelope.Error.Message
		code = envelope.Error.Type
		if c, ok := envelope.Error.Code.(string); ok && c != "" {
			code = c
		}
	}
	if message == "" {
		message = strings.TrimSpace(resp.Status)
		if message == "" {
			message = fmt.Sprintf("HTTP %d", resp.StatusCode)
		} else {
			message = fmt.Sprintf("%s (HTTP %d)", message, resp.StatusCode)
		}
	}
	if code == "" {
		code = fmt.Sprintf("http_%d", resp.StatusCode)
	}
	return &UpstreamError{
		HTTPStatus: resp.StatusCode,
		Class:      classifyHTTPStatus(resp.StatusCode),
		Code:       code,
		Message:    truncateMessage(message, 500),
	}
}

func classifyHTTPStatus(status int) string {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return ErrorClassAuth
	case http.StatusTooManyRequests:
		return ErrorClassRateLimit
	default:
		return ErrorClassUpstream
	}
}

func localError(code, message string) *UpstreamError {
	return &UpstreamError{Class: ErrorClassLocal, Code: code, Message: truncateMessage(message, 500)}
}

func upstreamInvalid(message string) *UpstreamError {
	return &UpstreamError{Class: ErrorClassUpstream, Code: "invalid_response", Message: truncateMessage(message, 500)}
}

// upstreamRetryable 标记「重发是安全的」上游异常（同一候选内允许再试）。
//
// 使用纪律：只有「上游未受理该请求」或「重发确实幂等、不产生第二次计费」的场景才可用。
// HTTP 2xx 之后才发现的坏体（上游已收下并处理）不满足这条——见 Embeddings 的头注。
func upstreamRetryable(message string) *UpstreamError {
	return &UpstreamError{Class: ErrorClassUpstream, Code: "invalid_response", Message: truncateMessage(message, 500), Retryable: true}
}

func decodeJSON(r io.Reader, dst any) error {
	const maxResponse = 64 << 20
	return json.NewDecoder(io.LimitReader(r, maxResponse)).Decode(dst)
}

func applyUsage(res *ChatResult, usage *UsageInfo) {
	if usage == nil {
		return
	}
	res.UsageSeen = true
	res.PromptTokens = usage.PromptTokens
	res.CompletionTokens = usage.CompletionTokens
	res.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	res.CachedTokens = usage.CachedTokens()
}

func isEmptyRawJSON(raw json.RawMessage) bool {
	s := strings.TrimSpace(string(raw))
	return s == "" || s == "null" || s == "[]"
}

func errorTypeName(err error) string {
	if err == nil {
		return ""
	}
	t := fmt.Sprintf("%T", err)
	t = strings.TrimPrefix(t, "*")
	if i := strings.LastIndex(t, "."); i >= 0 {
		t = t[i+1:]
	}
	if t == "" {
		t = "error"
	}
	return truncateMessage(t, 100)
}

type chatCompletionResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Index   int `json:"index"`
		Message struct {
			Role             string          `json:"role"`
			Content          any             `json:"content"`
			ReasoningContent string          `json:"reasoning_content"`
			ToolCalls        json.RawMessage `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *UsageInfo `json:"usage"`
}

type embeddingResponse struct {
	Model string `json:"model"`
	Data  []struct {
		Index     int       `json:"index"`
		Object    string    `json:"object"`
		Embedding []float64 `json:"embedding"`
	} `json:"data"`
	Usage *UsageInfo `json:"usage"`
}
