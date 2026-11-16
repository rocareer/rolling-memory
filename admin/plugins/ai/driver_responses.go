// plugin: ai v0.2.0
// driver_responses.go 是 OpenAI Responses 协议驱动（**手写 HTTP + 语义化 SSE 事件**）。
//
// 为什么单列一个驱动：官方 OpenAI 兼容端点只覆盖 /chat/completions 与 /embeddings，
// Responses 协议（POST {base}/responses + 语义化事件）没有现成封装。源侧同款取舍见
// ResponsesDriver.php 文件头（保留手写 HTTP + SSE 解析，业务契约与 OpenAI 兼容驱动一致）。
//
// 协议映射（对齐源 ResponsesDriver.php:11-55 与 buildBody/normalizeResponse/translateResponsesEvent）：
//   - 请求：model/stream + system 消息合并为 instructions（"\n\n" 连接）+ 其余消息转 input items
//     （[{type:message,role,content}]）；temperature/top_p 原样，max_tokens → max_output_tokens，
//     response_format → text.format。stop/tools/tool_choice/user 与 OpenAI chat 形态不同，
//     **不透传**（源同口径：只搬上述四键）。
//   - 非流式响应：output_text（或 output[].content[].text）→ content；
//     output[type=reasoning].summary[].text（或顶层 reasoning_content）→ reasoning_content；
//     usage.input_tokens/output_tokens/total_tokens → prompt/completion/total；
//     incomplete_details 非空 ⇒ finish_reason=length，否则 stop。
//   - 流式：语义化事件翻译成 OpenAI 兼容 chunk（output_text.delta → delta.content、
//     reasoning_summary_text.delta → delta.reasoning_content），终局事件（completed/incomplete）
//     带上 usage 与 finish_reason 后**停止读取**（收尾口径与 chat 路径的 [DONE] 一致：
//     引擎随后补 data: [DONE]，本驱动不自行转发 [DONE]）。
//
// 明确不支持 embedding（源 ResponsesDriver.php:43-47 的响亮失败）：注册表的 chat-only 能力门
// 与驱动自身的 Embeddings 双保险，错误都是 local（code=unsupported_capability）。
//
// 复用既有契约：HTTP 走 DriverEnv.Transport（客户端由 newGuardedHTTPClient 构造：护栏 + 不跟随
// 重定向），流式上限走 SSELimits，错误出口走 redactUpstreamErr。
package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// responsesPath 是 Responses 协议的端点路径（拼在渠道 base_url 之后）。
const responsesPath = "/responses"

// ResponsesDriver 是 OpenAI Responses 协议驱动（chat：非流式 + 流式；无向量端点）。
type ResponsesDriver struct {
	env            DriverEnv
	defaultBaseURL string
	limits         SSELimits
}

// newResponsesDriver 构造驱动。defaultBaseURL 在渠道 base_url 留空时套用（源构造期行为）。
func newResponsesDriver(env DriverEnv, defaultBaseURL string) *ResponsesDriver {
	if env.Transport == nil {
		env.Transport = newBaseDriver(env)
	}
	return &ResponsesDriver{env: env, defaultBaseURL: defaultBaseURL, limits: env.SSELimits}
}

// upstreamChannel 补厂商默认端点（渠道留空即继承；不回写库）。
func (d *ResponsesDriver) upstreamChannel(ch Channel) Channel {
	if strings.TrimSpace(ch.BaseURL) == "" && d.defaultBaseURL != "" {
		ch.BaseURL = d.defaultBaseURL
	}
	return ch
}

// Chat 执行非流式对话（POST /responses；2xx 响应按上面口径归一）。
func (d *ResponsesDriver) Chat(ctx context.Context, ch Channel, req ChatRequest) (result *ChatResult, err error) {
	defer func() { err = redactUpstreamErr(err, ch.APIKey) }()
	if strings.TrimSpace(ch.APIKey) == "" {
		return nil, localError("missing_api_key", "渠道未配置 api_key")
	}
	up := d.upstreamChannel(ch)
	logical := effectiveModelName(ch, req.Model)

	callCtx, cancel := context.WithTimeout(ctx, time.Duration(EffectiveTimeout(up.Timeout))*time.Second)
	defer cancel()

	start := time.Now()
	resp, err := d.env.Transport.postJSON(callCtx, up, responsesPath, req.responsesBody(up, false))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var out responsesResponse
	if err := decodeJSON(resp.Body, &out); err != nil {
		return nil, upstreamInvalid("上游响应不是合法 JSON: " + err.Error())
	}
	content, reasoning := out.normalizeOutput()
	if content == "" {
		// 与 OpenAI 驱动同口径：2xx 但无任何回复内容 ⇒ 不可重试的无效响应（上游可能已计费）。
		return nil, upstreamInvalid("上游返回空回复")
	}
	res := &ChatResult{
		ID:               out.ID,
		Model:            logical,
		Content:          content,
		ReasoningContent: reasoning,
		FinishReason:     out.finishReason(),
		LatencyMS:        int(time.Since(start).Milliseconds()),
	}
	applyUsage(res, out.usageInfo())
	return res, nil
}

// ChatStream 执行流式对话。
//
// 与 chat 路径的差别只在"上游帧形态"：本驱动把语义化事件翻译成 OpenAI 兼容 chunk 后**逐帧转发**
// （客户端看到的是 chat 语义；源 translateResponsesEvent 同口径——翻译只有一份，聚合与转发共用，
// 防两条消费路径语义漂移）。聚合 content/reasoning/usage，客户端断开时立即停止读取并保留已聚合结果。
func (d *ResponsesDriver) ChatStream(ctx context.Context, ch Channel, req ChatRequest, sink func(data []byte) error) (result *ChatResult, err error) {
	defer func() { err = redactUpstreamErr(err, ch.APIKey) }()
	if strings.TrimSpace(ch.APIKey) == "" {
		return nil, localError("missing_api_key", "渠道未配置 api_key")
	}
	up := d.upstreamChannel(ch)
	logical := effectiveModelName(ch, req.Model)

	start := time.Now()
	resp, err := d.env.Transport.postJSON(ctx, up, responsesPath, req.responsesBody(up, true))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	acc := NewStreamAccumulator(d.limits)
	sawContentEvent := false

	deliver := func(chunk *ChatChunk) error {
		acc.Apply(chunk)
		if sink == nil {
			return nil
		}
		payload, marshalErr := json.Marshal(chunk)
		if marshalErr != nil {
			return localError("encode_chunk", "规范化流式帧失败: "+marshalErr.Error())
		}
		if err := sink(payload); err != nil {
			acc.Aborted = true
			return &StreamAbortedError{Err: err}
		}
		return nil
	}

	parseErr := ParseSSEWithLimits(resp.Body, d.limits, func(ev SSEEvent) error {
		data := bytes.TrimSpace(ev.Data)
		if len(data) == 0 {
			return nil
		}
		if string(data) == "[DONE]" {
			return errSSEDone
		}
		switch ev.Event {
		case "response.output_text.delta":
			delta, err := decodeResponsesDelta(data)
			if err != nil {
				return err
			}
			if delta.Delta == "" {
				// 空增量（占位/心跳）不产生帧，也不参与「是否见过内容事件」的判定——
				// 否则终局载荷的完整输出补偿会被误跳过。
				return nil
			}
			sawContentEvent = true
			return deliver(newResponsesChunk(delta.ID, logical, ChatDelta{Content: delta.Delta}, nil, nil))
		case "response.reasoning_summary_text.delta":
			delta, err := decodeResponsesDelta(data)
			if err != nil {
				return err
			}
			if delta.Delta == "" {
				return nil
			}
			sawContentEvent = true
			return deliver(newResponsesChunk(delta.ID, logical, ChatDelta{ReasoningContent: delta.Delta}, nil, nil))
		case "response.completed", "response.incomplete":
			terminal, err := decodeResponsesTerminal(data)
			if err != nil {
				return err
			}
			if !sawContentEvent {
				// 网关只发终局事件（不发增量）时的兜底：终局载荷里的完整输出补一帧，
				// 否则客户端一无所获、且会被上层判成"没有任何数据块"的空流失败。
				content, reasoning := terminal.Response.normalizeOutput()
				if content != "" || reasoning != "" {
					sawContentEvent = true
					if err := deliver(newResponsesChunk(terminal.id(), logical, ChatDelta{
						Content:          content,
						ReasoningContent: reasoning,
					}, nil, nil)); err != nil {
						return err
					}
				}
			}
			reason := terminal.finishReason(ev.Event)
			var usage *UsageInfo
			if terminal.Response != nil {
				usage = terminal.Response.usageInfo()
			}
			if usage == nil {
				usage = terminal.usageInfo()
			}
			if err := deliver(newResponsesChunk(terminal.id(), logical, ChatDelta{}, &reason, usage)); err != nil {
				return err
			}
			return errSSEDone
		case "response.failed":
			return &UpstreamError{
				Class:   ErrorClassUpstream,
				Code:    "response_failed",
				Message: truncateMessage("上游 responses 失败: "+responsesErrorMessage(data), 500),
			}
		case "error":
			return &UpstreamError{
				Class:   ErrorClassUpstream,
				Code:    "stream_error",
				Message: truncateMessage("上游流式响应返回错误: "+responsesErrorMessage(data), 500),
			}
		default:
			// response.created / output_item.added / content_part.added 等过程事件：
			// 与源同口径忽略（不翻译、不转发）。
			return nil
		}
	})

	res := acc.Result()
	res.LatencyMS = int(time.Since(start).Milliseconds())
	res.Model = logical
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

// Embeddings 明确拒绝：Responses 协议没有向量端点（源 ResponsesDriver.php:43-47 同文案口径的响亮失败）。
//
// 注册表的 chat-only 能力门会先在分派层拒一次（code=unsupported_capability）；本方法是第二道保险，
// 防"绕过注册表直取驱动"的调用路径静默打到错误端点。
func (d *ResponsesDriver) Embeddings(ctx context.Context, ch Channel, req EmbeddingRequest) (result *EmbeddingResult, err error) {
	defer func() { err = redactUpstreamErr(err, ch.APIKey) }()
	return nil, localError("unsupported_capability", fmt.Sprintf("驱动 %q 不支持 embedding（Responses 协议无向量端点）", DriverNameResponses))
}

// Models 是探活通道：GET {base}/models（Responses 协议本身没有 /models，DeepSeek 官网网关与 chat 同域提供；
// 源 ResponsesDriver 只给静态 catalog（不做 HTTP 探活腿）——本插件探活统一走真实 HTTP，差异见 README 差异节）。
func (d *ResponsesDriver) Models(ctx context.Context, ch Channel) (models []string, err error) {
	defer func() { err = redactUpstreamErr(err, ch.APIKey) }()
	return d.env.Transport.Models(ctx, d.upstreamChannel(ch))
}

// responsesBody 装配 Responses 请求体（源 ResponsesDriver.php:196-226 的 buildBody 同口径）。
func (r ChatRequest) responsesBody(ch Channel, stream bool) map[string]any {
	body := make(map[string]any, len(r.Extra)+6)
	for k, v := range r.Extra {
		body[k] = v
	}
	// 协议形态不同：chat 语义的这几个键不得透传（透传会被上游按非法字段 400）。
	for _, key := range []string{"stream", "stream_options", "messages", "model", "max_tokens", "response_format", "stop", "tools", "tool_choice"} {
		delete(body, key)
	}

	// model 与 chat 路径同口径（driver.go 的 body：渠道 model 非空优先，否则请求侧模型名），
	// 恒写键（空串也照发，让上游以清晰的 400 拒绝，而不是本地悄悄省掉键）。
	body["model"] = effectiveModelName(ch, r.Model)
	body["stream"] = stream

	system := make([]string, 0, 2)
	items := make([]map[string]any, 0, len(r.Messages))
	for _, msg := range r.Messages {
		role := msg.Role
		if role == "" {
			role = "user" // 源同口径：role 缺省按 user
		}
		if role == "system" {
			system = append(system, ContentText(msg.Content))
			continue
		}
		items = append(items, map[string]any{
			"type":    "message",
			"role":    role,
			"content": ContentText(msg.Content),
		})
	}
	if len(system) > 0 {
		body["instructions"] = strings.Join(system, "\n\n")
	}
	body["input"] = items
	if r.Temperature != nil {
		body["temperature"] = *r.Temperature
	}
	if r.TopP != nil {
		body["top_p"] = *r.TopP
	}
	if r.MaxTokens != nil {
		body["max_output_tokens"] = *r.MaxTokens
	}
	if r.ResponseFormat != nil {
		body["text"] = map[string]any{"format": r.ResponseFormat}
	}
	return body
}

// ===== 响应归一（非流式 body 与流式终局事件共用同一份解析）=====

// responsesResponse 是 /responses 的响应对象（非流式 body；流式终局事件里的 response 字段）。
type responsesResponse struct {
	ID                string                `json:"id"`
	Model             string                `json:"model"`
	OutputText        string                `json:"output_text"`
	ReasoningContent  string                `json:"reasoning_content"`
	Output            []responsesOutputItem `json:"output"`
	IncompleteDetails json.RawMessage       `json:"incomplete_details"`
	Usage             *responsesUsage       `json:"usage"`
}

// responsesOutputItem 是 output 数组的一项（只取 content 与 reasoning summary 两处文本）。
type responsesOutputItem struct {
	Type    string `json:"type"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Summary []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"summary"`
}

// responsesUsage 是 Responses 的 usage（键名与 chat 不同：input/output）。
type responsesUsage struct {
	InputTokens        int `json:"input_tokens"`
	OutputTokens       int `json:"output_tokens"`
	TotalTokens        int `json:"total_tokens"`
	InputTokensDetails *struct {
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"input_tokens_details"`
}

// normalizeOutput 归一正文与推理摘要（源 normalizeResponse 同口径：output_text 优先，
// 缺失时才拼 output[type=message].content[type=output_text]；两个来源都空才回落推理项）。
func (o *responsesResponse) normalizeOutput() (string, string) {
	if o == nil {
		return "", ""
	}
	content := o.OutputText
	reasoning := o.ReasoningContent
	for _, item := range o.Output {
		switch item.Type {
		case "message":
			if content != "" {
				continue
			}
			for _, part := range item.Content {
				if part.Type == "output_text" {
					content += part.Text
				}
			}
		case "reasoning":
			if reasoning != "" {
				continue
			}
			for _, part := range item.Summary {
				if part.Type == "summary_text" {
					reasoning += part.Text
				}
			}
		}
	}
	return content, reasoning
}

// finishReason：源口径 —— incomplete_details 非空 ⇒ length，否则 stop。
func (o *responsesResponse) finishReason() string {
	if o != nil && hasIncompleteDetails(o.IncompleteDetails) {
		return "length"
	}
	return "stop"
}

// usageInfo 把 Responses usage 归一成 chat 口径（usage 缺失 ⇒ nil，与 chat 路径"未见 usage"一致）。
//
// 增量（源未做）：把 input_tokens_details.cached_tokens 落到 prompt 缓存明细——本插件只记不参与
// 成本公式（billing.go 口径），与 chat 路径的 prompt_tokens_details 同形。
func (o *responsesResponse) usageInfo() *UsageInfo {
	if o == nil || o.Usage == nil {
		return nil
	}
	// 上游计数为负时收敛到 0（复用同包 handler_channel.go 的 nonNegative：数值列 max(0) 口径）。
	prompt := nonNegative(o.Usage.InputTokens)
	completion := nonNegative(o.Usage.OutputTokens)
	total := o.Usage.TotalTokens
	if total <= 0 {
		total = prompt + completion
	}
	usage := &UsageInfo{PromptTokens: prompt, CompletionTokens: completion, TotalTokens: total}
	if o.Usage.InputTokensDetails != nil && o.Usage.InputTokensDetails.CachedTokens > 0 {
		usage.PromptTokensDetails = &PromptTokensDetails{CachedTokens: o.Usage.InputTokensDetails.CachedTokens}
	}
	return usage
}

// hasIncompleteDetails 判定 incomplete_details 是否"有内容"（源 `!empty($data['incomplete_details'])` 的 Go 版）。
func hasIncompleteDetails(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	return trimmed != "" && trimmed != "null" && trimmed != "{}" && trimmed != "[]"
}

// ===== 流式语义化事件 =====

// responsesDeltaEvent 是增量事件的载荷（response.output_text.delta / response.reasoning_summary_text.delta）。
type responsesDeltaEvent struct {
	Delta      string `json:"delta"`
	ID         string `json:"id"`
	ResponseID string `json:"response_id"`
}

// responsesTerminalEvent 是终局事件的载荷（数据可能在 response 对象里，也可能直接平铺在顶层）。
type responsesTerminalEvent struct {
	ID                string             `json:"id"`
	ResponseID        string             `json:"response_id"`
	Response          *responsesResponse `json:"response"`
	Usage             *responsesUsage    `json:"usage"`
	IncompleteDetails json.RawMessage    `json:"incomplete_details"`
}

func decodeResponsesDelta(data []byte) (responsesDeltaEvent, error) {
	var event responsesDeltaEvent
	if err := json.Unmarshal(data, &event); err != nil {
		return event, upstreamInvalid("上游流式帧不是合法 JSON: " + err.Error())
	}
	return event, nil
}

func decodeResponsesTerminal(data []byte) (responsesTerminalEvent, error) {
	var event responsesTerminalEvent
	if err := json.Unmarshal(data, &event); err != nil {
		return event, upstreamInvalid("上游流式帧不是合法 JSON: " + err.Error())
	}
	return event, nil
}

// id 终局事件的响应 ID（平铺 id/response_id 优先，其次 response.id）。
func (e responsesTerminalEvent) id() string {
	if id := firstNonEmpty(e.ResponseID, e.ID); id != "" {
		return id
	}
	if e.Response != nil {
		return e.Response.ID
	}
	return ""
}

// usageInfo 平铺 usage（部分网关把 usage 放在事件顶层而不是 response 对象里）。
func (e responsesTerminalEvent) usageInfo() *UsageInfo {
	if e.Usage == nil {
		return nil
	}
	flat := &responsesResponse{Usage: e.Usage}
	return flat.usageInfo()
}

// finishReason 终局事件的收尾语义：void response.incomplete ⇒ length；
// response.completed 看 incomplete_details（与源 normalizeResponse 同口径，流式路径的增量）。
func (e responsesTerminalEvent) finishReason(event string) string {
	if e.Response != nil && hasIncompleteDetails(e.Response.IncompleteDetails) {
		return "length"
	}
	if hasIncompleteDetails(e.IncompleteDetails) || event == "response.incomplete" {
		return "length"
	}
	return "stop"
}

// newResponsesChunk 组一个 OpenAI 兼容 chunk（与 chat 路径同形：客户端与聚合层都按 chat 语义消费）。
func newResponsesChunk(id, model string, delta ChatDelta, finishReason *string, usage *UsageInfo) *ChatChunk {
	return &ChatChunk{
		ID:    id,
		Model: model,
		Choices: []ChatChoice{{
			Index:        0,
			Delta:        delta,
			FinishReason: finishReason,
		}},
		Usage: usage,
	}
}

// responsesErrorMessage 取事件里可读的错误文案（源把整个 error 载荷 JSON 截断 500 字符；
// 这里优先取 message 字段，取不到才回落原始 JSON）。
func responsesErrorMessage(data []byte) string {
	var envelope struct {
		Message string `json:"message"`
		Error   *struct {
			Message string `json:"message"`
		} `json:"error"`
		Response *struct {
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		} `json:"response"`
	}
	if err := json.Unmarshal(data, &envelope); err == nil {
		if envelope.Error != nil && envelope.Error.Message != "" {
			return envelope.Error.Message
		}
		if envelope.Response != nil && envelope.Response.Error != nil && envelope.Response.Error.Message != "" {
			return envelope.Response.Error.Message
		}
		if envelope.Message != "" {
			return envelope.Message
		}
	}
	return string(data)
}

// firstNonEmpty 取第一个非空串。
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
