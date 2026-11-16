// plugin: ai v0.2.0
// handler_public.go 是对外网关（MountPublic 逐字对外，无后台前缀）：
//
//	POST /ai/v1/chat/completions  非流式 JSON 与 stream=true 的 SSE
//	POST /ai/v1/embeddings        input ≤ 64 条
//	GET  /ai/v1/models            只列 status=1 且 type=chat 的逻辑模型
//
// 鉴权：Authorization: Bearer plugin_ai_api_key。空/格式错/前缀非 Bearer ⇒ 401；
// 密钥未配置（空串或纯空白）⇒ 503 fail-closed；比较用 sha256 + subtle.ConstantTimeCompare（与长度无关）。
//
// 错误形状：真实 HTTP 状态码 + {"error":{"message","type","code"}}，**不沿用** httpx 的「HTTP 200 + code=1」。
//
// 例外清单（按发生次序）：
//   - 宿主全局 Throttle（cmd/serve/serve.go:59 的 engine.Use(middleware.Throttle())）在插件 handler **之前**
//     Abort 并按宿主形状输出（HTTP 200 + {"code":1,...}），插件无法改造 ⇒ 不承诺「公开面所有错误都是
//     OpenAI 形状」；凡要断言本插件 429 / 413 的场景，必须先隔离宿主节流（throttle.enabled=false 或调高
//     throttle.max，默认 10/s 按 IP+URL），否则会被宿主形状的回执抢先。
//   - 本包自持的 429（ratelimit.go 的 rateLimit 中间件）：type=rate_limit_error、code=rate_limit_exceeded、
//     Retry-After: 60，OpenAI 形状；它挂在鉴权之后、业务 handler 之前 ⇒ 429 不进 handler、不写 usage。
//
// 请求体先套 http.MaxBytesReader（8MiB），超限 413 request_too_large 且不落原文；
// biz_type/source 传入引擎前**只判空**（本层直接给常量，绝不做闭集校验）。
//
// 两处按 Key 的接缝（都在 ratelimit.go 定义读写辅助，本文件消费）：
//   - source = keySourceOf(c)：缺省 publicSource（全局配置 key 的老行为）；命中后台签发的 DB Key 时
//     由 apiKeyAuth 写 'key:<source>'，计量归因因此不再恒为 'key:gateway'。
//   - model_scope 白名单 = keyScopeOf(c)：未设置（nil/空）⇒ 完全不过滤、不拒绝（v1 现行为逐字不变）；
//     已设置 ⇒ /ai/v1/models 只列白名单内模型，对话/向量命中白名单外的模型回 404 +
//     code=model_not_found（与「模型不存在」同形，不向调用方区分二者——口径在此冻结）。
package ai

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

const (
	// maxPublicBodyBytes 公开面请求体上限（超限 413，且不把原文写进任何回执或日志）。
	maxPublicBodyBytes = 8 << 20
	// publicBizType / publicSource 是网关侧的**缺省**调用上下文（source 的 'key:<调用方>' 形态）。
	// source 缺省 'key:gateway'（全局配置 key 的老行为）；命中 DB Key 时经 setKeySource 覆盖，读出口是 keySourceOf。
	publicBizType = "api"
	publicSource  = "key:gateway"
)

// bearerAuth 公开面鉴权中间件。
//
// 次序：先读配置（读不到 ⇒ 503），再看密钥是否已配置（未配置 ⇒ 503 fail-closed），
// 最后比对头部（缺头/格式错/不匹配 ⇒ 401）。密钥永不进日志。
func (d deps) bearerAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		set, err := d.loadSettings(c.Request.Context())
		if err != nil {
			writePublicAPIError(c, &APIError{
				HTTPStatus: http.StatusServiceUnavailable,
				Type:       "upstream_unavailable",
				Code:       "settings_unavailable",
				Message:    "服务暂不可用",
				Class:      ErrorClassLocal,
				Err:        err,
			})
			return
		}
		if !set.APIKeySet {
			writePublicAPIError(c, &APIError{
				HTTPStatus: http.StatusServiceUnavailable,
				Type:       "upstream_unavailable",
				Code:       "api_key_not_configured",
				Message:    "网关密钥未配置（请在 AI 路由总览页的接入设置中配置 API Key）",
				Class:      ErrorClassLocal,
			})
			return
		}
		key, ok := bearerToken(c.GetHeader("Authorization"))
		if !ok || !secretEqual(key, set.APIKey) {
			writePublicAPIError(c, &APIError{
				HTTPStatus: http.StatusUnauthorized,
				Type:       "invalid_request_error",
				Code:       "invalid_api_key",
				Message:    "无效的 API Key",
				Class:      ErrorClassAuth,
			})
			return
		}
		c.Next()
	}
}

// bearerToken 解析 Authorization 头：前缀必须 Bearer（大小写不敏感），token 不得为空。
func bearerToken(header string) (string, bool) {
	parts := strings.SplitN(strings.TrimSpace(header), " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return "", false
	}
	token := strings.TrimSpace(parts[1])
	if token == "" {
		return "", false
	}
	return token, true
}

// secretEqual 恒定时间比较：先 sha256 摘要（比较长度恒为 32 字节，与密钥长度无关）。
func secretEqual(a, b string) bool {
	left := sha256.Sum256([]byte(a))
	right := sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(left[:], right[:]) == 1
}

// writePublicAPIError 公开面统一错误写出口（真实 HTTP 状态码 + OpenAI 形状）。
func writePublicAPIError(c *gin.Context, apiErr *APIError) {
	if apiErr == nil {
		return
	}
	status := apiErr.HTTPStatus
	if status <= 0 {
		status = http.StatusInternalServerError
	}
	c.AbortWithStatusJSON(status, gin.H{
		"error": gin.H{
			"message": apiErr.Message,
			"type":    apiErr.Type,
			"code":    apiErr.Code,
		},
	})
}

// invalidRequest 构造 400（参数类错误统一走它，文案给调用方看）。
func invalidRequest(message string) *APIError {
	return &APIError{
		HTTPStatus: http.StatusBadRequest,
		Type:       "invalid_request_error",
		Code:       "invalid_request_error",
		Message:    message,
		Class:      ErrorClassLocal,
	}
}

// publicAPIError 把编排层错误收敛成公开错误；不是 *APIError 的一律 503（不泄露内部细节）。
func publicAPIError(err error) *APIError {
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr != nil {
		return apiErr
	}
	return &APIError{
		HTTPStatus: http.StatusServiceUnavailable,
		Type:       "upstream_unavailable",
		Code:       "upstream_unavailable",
		Message:    "服务暂不可用",
		Class:      ErrorClassUnknown,
		Err:        err,
	}
}

// publicStoreOrFail 公开面取数据单例；不可用时按 OpenAI 形状 fail-closed。
func (d deps) publicStoreOrFail(c *gin.Context) (*Store, bool) {
	store, err := d.loadStore(c.Request.Context())
	if err != nil {
		writePublicAPIError(c, &APIError{
			HTTPStatus: http.StatusServiceUnavailable,
			Type:       "upstream_unavailable",
			Code:       "store_unavailable",
			Message:    "服务暂不可用",
			Class:      ErrorClassLocal,
			Err:        err,
		})
		return nil, false
	}
	return store, true
}

// readPublicJSONObject 读公开面请求体：8MiB 上限（超限 413 且不落原文）+ JSON 对象校验。
func readPublicJSONObject(c *gin.Context) (map[string]any, *APIError) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxPublicBodyBytes)

	decoder := json.NewDecoder(c.Request.Body)
	decoder.UseNumber()
	var body map[string]any
	if err := decoder.Decode(&body); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return nil, &APIError{
				HTTPStatus: http.StatusRequestEntityTooLarge,
				Type:       "invalid_request_error",
				Code:       "request_too_large",
				Message:    fmt.Sprintf("请求体超过上限（%d 字节）", maxPublicBodyBytes),
				Class:      ErrorClassLocal,
				Err:        err,
			}
		}
		if errors.Is(err, io.EOF) {
			return nil, invalidRequest("请求体不能为空")
		}
		return nil, invalidRequest("请求体不是合法 JSON")
	}
	if body == nil {
		return nil, invalidRequest("请求体必须是 JSON 对象")
	}
	return body, nil
}

// ===== 对话 =====

// publicChat 对外对话转发：非流式走候选链降级，流式只取首个准入渠道（不换道）。
func (d deps) publicChat(c *gin.Context) {
	body, apiErr := readPublicJSONObject(c)
	if apiErr != nil {
		writePublicAPIError(c, apiErr)
		return
	}
	input, stream, apiErr := parsePublicChat(body)
	if apiErr != nil {
		writePublicAPIError(c, apiErr)
		return
	}
	// 计量归因：缺省 'key:gateway'，命中 DB Key 时由 apiKeyAuth 写 'key:<source>'。
	input.Source = keySourceOf(c)
	// 白名单准入：未设置 model_scope ⇒ 恒放行（现行为不变）；设置了 ⇒ 白名单外回 404 model_not_found。
	if !modelInKeyScope(c, input.Model) {
		writePublicAPIError(c, keyScopeModelNotFound(input.Model))
		return
	}
	service, _, ok := d.serviceOrFail(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()

	if stream {
		d.streamChat(c, service, input)
		return
	}
	result, err := service.Chat(ctx, input)
	if err != nil {
		writePublicAPIError(c, publicAPIError(err))
		return
	}
	c.JSON(http.StatusOK, buildChatCompletion(input.Model, result))
}

// streamChat 流式收口：首块到达才写 SSE 头（因此「未开始写流」的失败仍能带真实状态码）。
func (d deps) streamChat(c *gin.Context, service *Service, input ChatInput) {
	ctx := c.Request.Context()
	writer := NewSSEWriter(c.Writer)
	started := false

	beginStream := func() {
		if started {
			return
		}
		started = true
		PrepareSSEHeaders(c.Writer.Header())
		c.Status(http.StatusOK)
		c.Writer.Flush()
	}

	_, err := service.ChatStream(ctx, input, func(data []byte) error {
		beginStream()
		return writer.Data(data)
	})

	if err == nil {
		beginStream()
		_ = writer.Done()
		return
	}
	apiErr := publicAPIError(err)
	if apiErr.HTTPStatus == 499 {
		// 客户端已断开（nginx 约定 499）：回执写不出去，也不该写。
		return
	}
	if !started {
		writePublicAPIError(c, apiErr)
		return
	}
	// 流已开始，真实状态码已发不出去：只能用 data 帧告知错误。
	// 不补 [DONE]——补了会让客户端把失败当正常收尾。
	_ = writer.Data(FormatSSEError(apiErr.Message, apiErr.Type, apiErr.Code))
}

// chatKnownFields 是网关显式消费的字段（其余原样透传给上游，见 ChatRequest.Extra）。
var chatKnownFields = map[string]struct{}{
	"model": {}, "messages": {}, "stream": {}, "temperature": {}, "top_p": {},
	"max_tokens": {}, "stop": {}, "tools": {}, "tool_choice": {}, "response_format": {},
	"user": {}, "biz_type": {},
}

// parsePublicChat 解析对话请求体（model/messages 必填；stream 决定走 SSE）。
func parsePublicChat(body map[string]any) (ChatInput, bool, *APIError) {
	model := strings.TrimSpace(asStringValue(body["model"]))
	if model == "" {
		return ChatInput{}, false, invalidRequest("model 必填")
	}
	rawMessages, ok := body["messages"].([]any)
	if !ok || len(rawMessages) == 0 {
		return ChatInput{}, false, invalidRequest("messages 必填（且至少一条消息）")
	}
	messages := make([]Message, 0, len(rawMessages))
	for index, item := range rawMessages {
		encoded, err := json.Marshal(item)
		if err != nil {
			return ChatInput{}, false, invalidRequest(fmt.Sprintf("messages[%d] 无法解析", index))
		}
		var message Message
		if err := json.Unmarshal(encoded, &message); err != nil {
			return ChatInput{}, false, invalidRequest(fmt.Sprintf("messages[%d] 不是合法的消息对象", index))
		}
		if strings.TrimSpace(message.Role) == "" {
			return ChatInput{}, false, invalidRequest(fmt.Sprintf("messages[%d].role 必填", index))
		}
		messages = append(messages, message)
	}

	stream := false
	if raw, exists := body["stream"]; exists && raw != nil {
		flag, ok := raw.(bool)
		if !ok {
			return ChatInput{}, false, invalidRequest("stream 必须是布尔值")
		}
		stream = flag
	}
	temperature, apiErr := bodyFloat(body, "temperature")
	if apiErr != nil {
		return ChatInput{}, false, apiErr
	}
	topP, apiErr := bodyFloat(body, "top_p")
	if apiErr != nil {
		return ChatInput{}, false, apiErr
	}
	maxTokens, apiErr := bodyInt(body, "max_tokens")
	if apiErr != nil {
		return ChatInput{}, false, apiErr
	}

	// Source 不在此处定：它是请求级上下文（缺省 'key:gateway'，命中 DB Key 时被 apiKeyAuth 覆盖），
	// 由 publicChat 调 keySourceOf(c) 填（保持本函数是纯请求体解析器，无 context 依赖）。
	return ChatInput{
		Model:          model,
		Messages:       messages,
		Temperature:    temperature,
		TopP:           topP,
		MaxTokens:      maxTokens,
		Stop:           body["stop"],
		Tools:          body["tools"],
		ToolChoice:     body["tool_choice"],
		ResponseFormat: body["response_format"],
		User:           strings.TrimSpace(asStringValue(body["user"])),
		Extra:          chatExtra(body),
		BizType:        bizTypeFromBody(body),
	}, stream, nil
}

// bizTypeFromBody 调用上下文：允许调用方自带非空 biz_type（便于业务侧分类），缺省 'api'；source 固定 'key:gateway'。
func bizTypeFromBody(body map[string]any) string {
	if value := strings.TrimSpace(asStringValue(body["biz_type"])); value != "" {
		return value
	}
	return publicBizType
}

// chatExtra 透传字段（显式字段之外的一切，含 response_format 之外的厂商私有参数）。
func chatExtra(body map[string]any) map[string]any {
	extra := make(map[string]any)
	for key, value := range body {
		if _, known := chatKnownFields[key]; known {
			continue
		}
		extra[key] = value
	}
	if len(extra) == 0 {
		return nil
	}
	return extra
}

// buildChatCompletion 组装 OpenAI 形状的对话响应。
//
// usage 直出上游计数：上游未返回 usage 时为 0（内部记账另有近似估算兜底，但那不进公开回执，
// 避免把「估算值」当上游真值暴露给调用方）。
func buildChatCompletion(model string, result *ChatResult) gin.H {
	if result == nil {
		result = &ChatResult{}
	}
	id := result.ID
	if strings.TrimSpace(id) == "" {
		id = NewRequestID()
	}
	message := gin.H{"role": "assistant", "content": result.Content}
	if result.ReasoningContent != "" {
		message["reasoning_content"] = result.ReasoningContent
	}
	if len(result.ToolCalls) > 0 && string(result.ToolCalls) != "null" {
		message["tool_calls"] = result.ToolCalls
	}
	finishReason := result.FinishReason
	if finishReason == "" {
		finishReason = "stop"
	}
	return gin.H{
		"id":      id,
		"object":  "chat.completion",
		"created": nowUnix(),
		"model":   model,
		"choices": []gin.H{{
			"index":         0,
			"message":       message,
			"finish_reason": finishReason,
		}},
		"usage": gin.H{
			"prompt_tokens":     result.PromptTokens,
			"completion_tokens": result.CompletionTokens,
			"total_tokens":      result.TotalTokens,
		},
	}
}

// ===== 向量 =====

// publicEmbeddings 对外向量转发：input 为字符串或字符串数组（≤64 条，超限 400 且不发上游）。
func (d deps) publicEmbeddings(c *gin.Context) {
	body, apiErr := readPublicJSONObject(c)
	if apiErr != nil {
		writePublicAPIError(c, apiErr)
		return
	}
	model := strings.TrimSpace(asStringValue(body["model"]))
	if model == "" {
		writePublicAPIError(c, invalidRequest("model 必填"))
		return
	}
	input, count, apiErr := normalizeEmbeddingInput(body["input"])
	if apiErr != nil {
		writePublicAPIError(c, apiErr)
		return
	}
	if count > maxPublicEmbeddingInput {
		writePublicAPIError(c, &APIError{
			HTTPStatus: http.StatusBadRequest,
			Type:       "invalid_request_error",
			Code:       "input_too_large",
			Message:    fmt.Sprintf("input 最多 %d 条（本次 %d 条）", maxPublicEmbeddingInput, count),
			Class:      ErrorClassLocal,
		})
		return
	}
	// 白名单准入（口径同 publicChat）：未设置 model_scope ⇒ 恒放行。
	if !modelInKeyScope(c, model) {
		writePublicAPIError(c, keyScopeModelNotFound(model))
		return
	}
	service, _, ok := d.serviceOrFail(c)
	if !ok {
		return
	}
	result, err := service.Embeddings(c.Request.Context(), EmbeddingInput{
		Model:   model,
		Input:   input,
		BizType: bizTypeFromBody(body),
		// 计量归因：缺省 'key:gateway'，命中 DB Key 时由 apiKeyAuth 写 'key:<source>'。
		Source: keySourceOf(c),
	})
	if err != nil {
		writePublicAPIError(c, publicAPIError(err))
		return
	}
	data := make([]gin.H, 0, len(result.Data))
	for _, item := range result.Data {
		data = append(data, gin.H{
			"object":    "embedding",
			"index":     item.Index,
			"embedding": item.Vector,
		})
	}
	c.JSON(http.StatusOK, gin.H{
		"object": "list",
		"data":   data,
		"model":  model,
		"usage": gin.H{
			"prompt_tokens": result.PromptTokens,
			"total_tokens":  result.TotalTokens,
		},
	})
}

// normalizeEmbeddingInput 收敛 input：字符串或字符串数组（v1 不支持 token 数组）。
func normalizeEmbeddingInput(input any) (any, int, *APIError) {
	switch value := input.(type) {
	case string:
		if strings.TrimSpace(value) == "" {
			return nil, 0, invalidRequest("input 不能是空字符串")
		}
		return value, 1, nil
	case []any:
		if len(value) == 0 {
			return nil, 0, invalidRequest("input 不能是空数组")
		}
		items := make([]string, 0, len(value))
		for index, item := range value {
			text, ok := item.(string)
			if !ok || strings.TrimSpace(text) == "" {
				return nil, 0, invalidRequest(fmt.Sprintf("input[%d] 必须是非空字符串（v1 不支持 token 数组）", index))
			}
			items = append(items, text)
		}
		return items, len(items), nil
	default:
		return nil, 0, invalidRequest("input 必须是字符串或字符串数组")
	}
}

// ===== 模型列表 =====

// publicModels 可用模型列表：只列 status=1 且 type=chat 的逻辑模型（对外统一名）。
// 按 Key 白名单（model_scope 经 keyScopeOf(c) 取，接缝在 ratelimit.go）：未设置 ⇒ 不过滤
// （v1 现行为逐字不变，nil/空与「不设置」等价）；已设置 ⇒ 只列白名单内的模型名。
//
// 已知边界（v1 如实登记）：返回条数受列表契约上限约束（≤100 条，MaxListLimit），不做翻页——
// 逻辑模型是人工维护的白名单，量级远小于上限；真要超过 100 个模型时本端点需要改为循环翻页。
func (d deps) publicModels(c *gin.Context) {
	store, ok := d.publicStoreOrFail(c)
	if !ok {
		return
	}
	modelType := ModelTypeChat
	status := StatusEnabled
	result, err := store.ListModels(c.Request.Context(), ModelFilter{
		Page:   1,
		Limit:  MaxListLimit,
		Type:   &modelType,
		Status: &status,
	})
	if err != nil {
		writePublicAPIError(c, &APIError{
			HTTPStatus: http.StatusServiceUnavailable,
			Type:       "upstream_unavailable",
			Code:       "store_unavailable",
			Message:    "服务暂不可用",
			Class:      ErrorClassLocal,
			Err:        err,
		})
		return
	}
	// 未设置 scope ⇒ scope 为 nil/空，下面的判定整体短路：输出与现状逐字一致。
	scope := keyScopeOf(c)
	data := make([]gin.H, 0, len(result.Rows))
	for _, row := range result.Rows {
		if len(scope) > 0 {
			if _, allowed := scope[row.Name]; !allowed {
				continue
			}
		}
		data = append(data, gin.H{
			"id":       row.Name,
			"object":   "model",
			"created":  row.CreateTime,
			"owned_by": Name,
		})
	}
	c.JSON(http.StatusOK, gin.H{"object": "list", "data": data})
}

// ===== 公开面取值收敛 =====

// asStringValue 取值里的字符串（非字符串返回空串）。
func asStringValue(value any) string {
	text, _ := value.(string)
	return text
}

// bodyFloat 取可选的浮点字段（json.Number / 数字都接受；显式 null 视为未传）。
func bodyFloat(body map[string]any, key string) (*float64, *APIError) {
	raw, exists := body[key]
	if !exists || raw == nil {
		return nil, nil
	}
	switch value := raw.(type) {
	case json.Number:
		number, err := value.Float64()
		if err != nil {
			return nil, invalidRequest(key + " 必须是数字")
		}
		return &number, nil
	case float64:
		return &value, nil
	default:
		return nil, invalidRequest(key + " 必须是数字")
	}
}

// bodyInt 取可选的整数字段（显式 null 视为未传）。
func bodyInt(body map[string]any, key string) (*int, *APIError) {
	raw, exists := body[key]
	if !exists || raw == nil {
		return nil, nil
	}
	number, err := toInt64(raw)
	if err != nil {
		return nil, invalidRequest(key + " 必须是整数")
	}
	value := int(number)
	return &value, nil
}
