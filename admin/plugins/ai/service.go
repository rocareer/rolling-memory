// plugin: ai v0.2.0
// service.go 是 Chat/Embedding 的编排层：解析逻辑模型 → 候选链 → 熔断准入 →
// 上游调用（候选内先做传输层重试）→ 成功/失败结算（计费 + 熔断 + 用量）→ 降级。
//
// 边界：
//   - 本文件不写 SQL，全部持久化经 Repository（store 层实现）；
//   - 不解析 HTTP、不写回执（handler 层职责）；
//   - 同一状态迁移只有一处实现：熔断迁移在 circuit.go，usage 行由本文件收口。
//
// 失败语义：
//   - 无启用绑定 ⇒ 「模型 [x] 无启用的渠道绑定（请在 AI 路由为该模型绑定渠道）」；
//   - 候选耗尽 ⇒ 「所有渠道均不可用: <最后错误>」（Detail 里带原因，公开 Message 不泄露内部渠道）；
//   - 公开面映射：upstream_unavailable=503、timeout=504、rate_limit=429。
package ai

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"
)

// 默认流式总时长（插件配置 stream_timeout 的默认值）。
const DefaultStreamTimeout = 300

// streamGuardSeconds 是流式「丢失回调」护栏：stream_timeout + 15s 内既无块也无收尾
// 就按失败收口，绝不悬空。
const streamGuardSeconds = 15

// maxTransientAttempts 传输层重试预算：首击 + 2 次重试。
const maxTransientAttempts = 3

// transientBackoffStep 第 n 次重试前 sleep 200ms×n。
const transientBackoffStep = 200 * time.Millisecond

// maxPublicEmbeddingInput 公开面 embedding input 条数上限（超过立即 400，不发上游）。
const maxPublicEmbeddingInput = 64

// maxUpstreamEmbeddingBatch 单请求上游条数上限（取上游最严账号值，超限的 400 会污染渠道失败率）。
const maxUpstreamEmbeddingBatch = 20

// ErrModelNotFound 由 Repository 在逻辑模型不存在时返回；Service 会翻成公开的 404。
var ErrModelNotFound = errors.New("模型不可用")

// APIError 是面向接口层的错误：真实 HTTP 状态码 + OpenAI 形状的 type/code/message。
//
// Error() 只返回公开 Message（不含上游细节）；Detail() 给日志/内部诊断用。
type APIError struct {
	HTTPStatus int
	Type       string
	Code       string
	Message    string
	Class      string
	Err        error
}

func (e *APIError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

func (e *APIError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// Detail 返回带内部原因的文案（只用于日志，不写给客户端）。
func (e *APIError) Detail() string {
	if e == nil {
		return ""
	}
	if e.Err != nil && e.Err.Error() != "" {
		return e.Message + ": " + e.Err.Error()
	}
	return e.Message
}

// Config 是编排层需要的插件配置（settings.go 读出后注入）。
type Config struct {
	RequestTimeout int
	StreamTimeout  int
	Currency       string
	// BreakerFailThreshold / BreakerCooldownSeconds 是 plugin.yaml 的熔断全局默认
	// （渠道 fail_threshold/cooldown_seconds 为 0 时使用）；<=0 视为未配，回落代码常量。
	BreakerFailThreshold   int
	BreakerCooldownSeconds int
	// AllowPrivateHosts 只透传给驱动工厂；编排层不做目标地址判断。
	AllowPrivateHosts bool
}

func (c Config) withDefaults() Config {
	if c.RequestTimeout <= 0 {
		c.RequestTimeout = DefaultRequestTimeout
	}
	if c.RequestTimeout > 600 {
		c.RequestTimeout = 600
	}
	if c.StreamTimeout <= 0 {
		c.StreamTimeout = DefaultStreamTimeout
	}
	c.Currency = CurrencyOrDefault(c.Currency)
	return c
}

// Repository 是编排层依赖的持久化端口。
//
// 由 *Store（store.go）实现（store.go 末尾有编译期断言）：
//   - FindModel：按对外统一名查逻辑模型（找不到返回 ErrModelNotFound）；
//   - Candidates：一次 JOIN 取回该模型的扁平候选行（禁 N+1）；
//   - UpsertUsage：按 (request_id,attempt) 幂等覆盖；
//   - 以及 CircuitStore 的全部熔断原子原语。
type Repository interface {
	CircuitStore
	FindModel(ctx context.Context, name string) (Model, error)
	Candidates(ctx context.Context, modelID uint64) ([]Candidate, error)
	UpsertUsage(ctx context.Context, usage *Usage) error
}

// Service 是 AI 路由插件的编排单例。
type Service struct {
	repo    Repository
	driver  Driver
	breaker *Circuit
	router  *Router
	cfg     Config
	now     func() int64
}

// NewService 装配编排层。rnd 为 nil 时用全局随机源；breaker 的 store 与 repo 是同一个实例。
// 熔断的全局默认（渠道字段为 0 时使用）由 cfg 注入——配置改了必须换新实例才生效，
// 指纹见 plugin.go 的 pluginSettings.signature。
func NewService(repo Repository, driver Driver, cfg Config, rnd Rand) *Service {
	breaker := NewCircuit(repo, CircuitDefaults{
		FailThreshold:   cfg.BreakerFailThreshold,
		CooldownSeconds: cfg.BreakerCooldownSeconds,
	})
	return &Service{
		repo:    repo,
		driver:  driver,
		breaker: breaker,
		router:  NewRouter(breaker, rnd),
		cfg:     cfg.withDefaults(),
		now:     func() int64 { return time.Now().Unix() },
	}
}

// ChatInput 是一次对话调用的入参。
//
// BizType / Source **只判空**、绝不做闭集校验：网关传 'key:gateway'、源侧传
// 'internal' 都在前端下拉闭集之外，按闭集拦截会让三条公开路由 100% 失败（P0-1）。
type ChatInput struct {
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

	BizType string
	Source  string
	// RequestID 仅供测试/重放注入；为空时生成 ai_+24 hex。
	RequestID string
}

// EmbeddingInput 是一次向量调用的入参。Input 为 string 或 []string。
type EmbeddingInput struct {
	Model   string
	Input   any
	BizType string
	Source  string

	RequestID string
}

// ValidateCallContext 是 requireContext 的唯一实现：只拒空值。
//
// 文案与源 AiRouterService::requireContext 逐字同口径。
func ValidateCallContext(bizType, source string) error {
	if strings.TrimSpace(bizType) == "" || strings.TrimSpace(source) == "" {
		return &APIError{
			HTTPStatus: 400,
			Type:       "invalid_request_error",
			Code:       "invalid_request_error",
			Message:    "调用方必须携带 biz_type（业务类型）与 source（来路）",
			Class:      ErrorClassLocal,
		}
	}
	return nil
}

// Chat 执行非流式对话：按候选链顺序降级，首个成功即返回。
func (s *Service) Chat(ctx context.Context, in ChatInput) (*ChatResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	m, requestID, err := s.begin(ctx, in.Model, CallTypeChat, in.BizType, in.Source, in.RequestID)
	if err != nil {
		return nil, err
	}
	if in.Tools != nil && !m.SupportsTools() {
		return nil, &APIError{
			HTTPStatus: 400,
			Type:       "invalid_request_error",
			Code:       "tools_not_supported",
			Message:    fmt.Sprintf("模型 [%s] 不支持工具调用（capabilities 未声明 function_calling）", m.Name),
			Class:      ErrorClassLocal,
		}
	}

	promptText := MessagesText(in.Messages)
	hasMedia := HasMultimodalMessages(in.Messages)
	if _, err := CheckContextWindow(m.Encoding, m.ContextWindow, promptText, hasMedia); err != nil {
		apiErr := &APIError{
			HTTPStatus: 400,
			Type:       "invalid_request_error",
			Code:       "context_window_exceeded",
			Message:    err.Error(),
			Class:      ErrorClassLocal,
			Err:        err,
		}
		s.recordLocalFailure(ctx, m, requestID, CallTypeChat, in.BizType, in.Source, apiErr)
		return nil, apiErr
	}

	candidates, err := s.candidates(ctx, m)
	if err != nil {
		return nil, err
	}

	req := ChatRequest{
		Model:          in.Model,
		Messages:       in.Messages,
		Temperature:    in.Temperature,
		TopP:           in.TopP,
		MaxTokens:      in.MaxTokens,
		Stop:           in.Stop,
		Tools:          in.Tools,
		ToolChoice:     in.ToolChoice,
		ResponseFormat: in.ResponseFormat,
		User:           in.User,
		Extra:          in.Extra,
	}

	attempt := 0
	var lastErr error
	for _, cand := range candidates {
		if err := ctx.Err(); err != nil {
			lastErr = err
			break
		}
		ch := s.resolveChannel(cand.ChannelSnapshot())
		ad, err := s.router.Admit(ctx, ch, s.nowUnix())
		if err != nil {
			// 熔断存储故障：拒绝该候选，但不算一次真实调用（不自增 attempt）。
			lastErr = err
			continue
		}
		if !ad.Allowed {
			// 熔断/冷却拒绝：跳过且不自增 attempt。
			continue
		}
		ch.Circuit = ad.Circuit

		start := time.Now()
		res, callErr := retryCall(ctx, "chat", func() (*ChatResult, error) {
			return s.driver.Chat(ctx, ch, req)
		})
		latency := int(time.Since(start).Milliseconds())

		if callErr == nil {
			s.settleChatSuccess(ctx, m, ch, res, latency, requestID, attempt, in.BizType, in.Source, promptText, hasMedia)
			return res, nil
		}
		s.settleChatFailure(ctx, m, ch, callErr, latency, requestID, attempt, in.BizType, in.Source)
		lastErr = callErr
		attempt++
	}
	if lastErr == nil {
		lastErr = errors.New("没有可用渠道")
	}
	return nil, allUnavailableError(lastErr)
}

// ChatStream 执行流式对话：只取首个准入通过的渠道，中途失败不换道。
//
// 记账四分支：
//
//	① 首块到达 → breaker.OnSuccess（熔断记成功），此时不写 usage；
//	② 正常收尾 → 写一行 success（stream=1，聚合 usage）；
//	③ 有块、无收尾 → **不再** onFailure，写一行 fail|timeout，但按已聚合内容**部分计费**：
//	   stream=1、tokens_source=usage|estimate（ResolveStreamUsage）、cost 按模型单价算、
//	   billed_partial=1（审计标记，见 billing.go 的 Usage.BilledPartial 与迁移 2）；
//	④ 无块、无收尾（护栏 stream_timeout+15s 或首块前失败）→ onFailure + 同款失败行
//	   （attempt=0、stream=0、tokens_source=''、cost='0.000000'、billed_partial=0）。
//
// 客户端断开走 ③：立即停止读取，已聚合的内容与已见 usage 保留在返回的 res 里，
// 记账由 settleStreamFailure 的补偿分支消费这份 res。
func (s *Service) ChatStream(ctx context.Context, in ChatInput, emit func(data []byte) error) (*ChatResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	m, requestID, err := s.begin(ctx, in.Model, CallTypeChat, in.BizType, in.Source, in.RequestID)
	if err != nil {
		return nil, err
	}
	if in.Tools != nil && !m.SupportsTools() {
		return nil, &APIError{
			HTTPStatus: 400,
			Type:       "invalid_request_error",
			Code:       "tools_not_supported",
			Message:    fmt.Sprintf("模型 [%s] 不支持工具调用（capabilities 未声明 function_calling）", m.Name),
			Class:      ErrorClassLocal,
		}
	}
	promptText := MessagesText(in.Messages)
	hasMedia := HasMultimodalMessages(in.Messages)
	if _, err := CheckContextWindow(m.Encoding, m.ContextWindow, promptText, hasMedia); err != nil {
		apiErr := &APIError{
			HTTPStatus: 400,
			Type:       "invalid_request_error",
			Code:       "context_window_exceeded",
			Message:    err.Error(),
			Class:      ErrorClassLocal,
			Err:        err,
		}
		s.recordLocalFailure(ctx, m, requestID, CallTypeChat, in.BizType, in.Source, apiErr)
		return nil, apiErr
	}

	candidates, err := s.candidates(ctx, m)
	if err != nil {
		return nil, err
	}

	var chosenChannel Channel
	found := false
	var lastErr error
	now := s.nowUnix()
	for _, cand := range candidates {
		ch := s.resolveChannel(cand.ChannelSnapshot())
		ad, err := s.router.Admit(ctx, ch, now)
		if err != nil {
			lastErr = err
			continue
		}
		if !ad.Allowed {
			continue
		}
		ch.Circuit = ad.Circuit
		cand.Circuit = ad.Circuit
		chosenChannel = ch
		found = true
		break
	}
	if !found {
		if lastErr == nil {
			lastErr = errors.New("所有候选都被熔断/冷却拒绝")
		}
		return nil, allUnavailableError(lastErr)
	}

	guardCtx, cancel := context.WithTimeout(ctx, time.Duration(s.cfg.StreamTimeout+streamGuardSeconds)*time.Second)
	defer cancel()

	req := ChatRequest{
		Model:          in.Model,
		Messages:       in.Messages,
		Temperature:    in.Temperature,
		TopP:           in.TopP,
		MaxTokens:      in.MaxTokens,
		Stop:           in.Stop,
		Tools:          in.Tools,
		ToolChoice:     in.ToolChoice,
		ResponseFormat: in.ResponseFormat,
		User:           in.User,
		Extra:          in.Extra,
	}

	started := false
	recorded := false
	res, callErr := s.driver.ChatStream(guardCtx, chosenChannel, req, func(data []byte) error {
		if !started {
			started = true
			acct := accountingContext(ctx)
			if err := s.breaker.OnSuccess(acct.ctx, chosenChannel, s.nowUnix()); err != nil {
				log.Printf("ai: 流式首块后迁移熔断状态失败 channel=%d: %v", chosenChannel.ID, err)
			}
			acct.cancel()
		}
		if emit == nil {
			return nil
		}
		return emit(data)
	})

	latency := 0
	if res != nil {
		latency = res.LatencyMS
	}

	if callErr == nil {
		if res == nil || res.Chunks == 0 {
			// 上游以空流「正常」结束：当作无块、无收尾的失败（分支④）。
			callErr = upstreamInvalid("上游流式响应没有任何数据块")
		} else {
			if !recorded {
				s.settleStreamSuccess(ctx, m, chosenChannel, res, latency, requestID, in.BizType, in.Source, promptText, hasMedia)
				recorded = true
			}
			return res, nil
		}
	}

	if !recorded {
		info := ClassifyError(callErr)
		s.settleStreamFailure(ctx, m, chosenChannel, info, latency, requestID, in.BizType, in.Source, res, promptText, hasMedia)
		recorded = true
		// 分支③（有块、无收尾）不动熔断；分支④（无块）才 onFailure。
		if !started {
			acct := accountingContext(ctx)
			if err := s.breaker.OnFailure(acct.ctx, chosenChannel, info.Message, s.nowUnix()); err != nil {
				log.Printf("ai: 流式失败后迁移熔断状态失败 channel=%d: %v", chosenChannel.ID, err)
			}
			acct.cancel()
		}
	}
	return res, streamPublicError(callErr)
}

// Embeddings 执行向量调用：按候选链降级，同渠道内 20 条一片串行、data 保序合并。
func (s *Service) Embeddings(ctx context.Context, in EmbeddingInput) (*EmbeddingResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	m, requestID, err := s.begin(ctx, in.Model, CallTypeEmbedding, in.BizType, in.Source, in.RequestID)
	if err != nil {
		return nil, err
	}

	count := EmbeddingInputCount(in.Input)
	if count <= 0 {
		return nil, &APIError{
			HTTPStatus: 400,
			Type:       "invalid_request_error",
			Code:       "invalid_request_error",
			Message:    "embedding input 不能为空",
			Class:      ErrorClassLocal,
		}
	}
	if count > maxPublicEmbeddingInput {
		return nil, &APIError{
			HTTPStatus: 400,
			Type:       "invalid_request_error",
			Code:       "input_too_large",
			Message:    fmt.Sprintf("embedding input 最多 %d 条，当前 %d 条", maxPublicEmbeddingInput, count),
			Class:      ErrorClassLocal,
		}
	}

	promptText := EmbeddingInputText(in.Input)
	if _, err := CheckContextWindow(m.Encoding, m.ContextWindow, promptText, false); err != nil {
		apiErr := &APIError{
			HTTPStatus: 400,
			Type:       "invalid_request_error",
			Code:       "context_window_exceeded",
			Message:    err.Error(),
			Class:      ErrorClassLocal,
			Err:        err,
		}
		s.recordLocalFailure(ctx, m, requestID, CallTypeEmbedding, in.BizType, in.Source, apiErr)
		return nil, apiErr
	}

	candidates, err := s.candidates(ctx, m)
	if err != nil {
		return nil, err
	}

	attempt := 0
	var lastErr error
	for _, cand := range candidates {
		if err := ctx.Err(); err != nil {
			lastErr = err
			break
		}
		ch := s.resolveChannel(cand.ChannelSnapshot())
		ad, err := s.router.Admit(ctx, ch, s.nowUnix())
		if err != nil {
			lastErr = err
			continue
		}
		if !ad.Allowed {
			continue
		}
		ch.Circuit = ad.Circuit

		start := time.Now()
		res, callErr := s.callEmbedding(ctx, ch, m, in.Input)
		latency := int(time.Since(start).Milliseconds())
		if callErr == nil {
			s.settleEmbeddingSuccess(ctx, m, ch, res, latency, requestID, attempt, in.BizType, in.Source, promptText)
			return res, nil
		}
		s.settleEmbeddingFailure(ctx, m, ch, callErr, latency, requestID, attempt, in.BizType, in.Source)
		lastErr = callErr
		attempt++
	}
	if lastErr == nil {
		lastErr = errors.New("没有可用渠道")
	}
	return nil, allUnavailableError(lastErr)
}

// begin 做公共入口校验：requireContext → 解析逻辑模型 → 生成 request_id。
func (s *Service) begin(ctx context.Context, modelName, callType, bizType, source, requestID string) (Model, string, error) {
	if err := ValidateCallContext(bizType, source); err != nil {
		return Model{}, "", err
	}
	if requestID == "" {
		requestID = NewRequestID()
	}
	m, err := s.repo.FindModel(ctx, modelName)
	if err != nil {
		if errors.Is(err, ErrModelNotFound) {
			return Model{}, "", modelNotFoundError(modelName)
		}
		return Model{}, "", &APIError{
			HTTPStatus: 503,
			Type:       "upstream_unavailable",
			Code:       "store_unavailable",
			Message:    "服务暂不可用",
			Class:      ErrorClassLocal,
			Err:        err,
		}
	}
	if !m.Enabled() {
		return Model{}, "", modelNotFoundError(modelName)
	}
	if m.Type != callType {
		return Model{}, "", &APIError{
			HTTPStatus: 400,
			Type:       "invalid_request_error",
			Code:       "model_type_mismatch",
			Message:    fmt.Sprintf("模型 [%s] 类型不匹配（需要 %s，实际 %s）", modelName, callType, m.Type),
			Class:      ErrorClassLocal,
		}
	}
	return m, requestID, nil
}

// bindingLister 是 *Store 已有的 ListBindings 签名（用于区分两种失败文案）。
// 用可选接口而不是写进 Repository：测试的假 store 不必实现它。
type bindingLister interface {
	ListBindings(ctx context.Context, modelIDs []uint64) ([]ModelBindingRow, error)
}

func (s *Service) candidates(ctx context.Context, m Model) ([]Candidate, error) {
	rows, err := s.repo.Candidates(ctx, m.ID)
	if err != nil {
		return nil, &APIError{
			HTTPStatus: 503,
			Type:       "upstream_unavailable",
			Code:       "store_unavailable",
			Message:    "服务暂不可用",
			Class:      ErrorClassLocal,
			Err:        err,
		}
	}
	candidates := s.router.Build(m, rows)
	if len(candidates) > 0 {
		return candidates, nil
	}
	// 两种文案刻意区分：没有绑定 vs 有绑定但渠道全不可用。
	hasBinding := false
	if lister, ok := s.repo.(bindingLister); ok {
		if binds, listErr := lister.ListBindings(ctx, []uint64{m.ID}); listErr == nil {
			for _, b := range binds {
				if b.Status == StatusEnabled {
					hasBinding = true
					break
				}
			}
		}
	}
	if !hasBinding {
		return nil, &APIError{
			HTTPStatus: 503,
			Type:       "upstream_unavailable",
			Code:       "no_channel_binding",
			Message:    fmt.Sprintf("模型 [%s] 无启用的渠道绑定（请在 AI 路由为该模型绑定渠道）", m.Name),
			Class:      ErrorClassLocal,
		}
	}
	return nil, &APIError{
		HTTPStatus: 503,
		Type:       "upstream_unavailable",
		Code:       "no_available_channel",
		Message:    fmt.Sprintf("所有渠道均不可用: 模型 [%s] 没有启用的渠道", m.Name),
		Class:      ErrorClassLocal,
	}
}

func (s *Service) callEmbedding(ctx context.Context, ch Channel, m Model, input any) (*EmbeddingResult, error) {
	chunks := splitEmbeddingInput(input, maxUpstreamEmbeddingBatch)
	merged := &EmbeddingResult{Model: m.Name}
	for _, chunk := range chunks {
		part, err := retryCall(ctx, "embedding", func() (*EmbeddingResult, error) {
			return s.driver.Embeddings(ctx, ch, EmbeddingRequest{Model: m.Name, Input: chunk})
		})
		if err != nil {
			return nil, err
		}
		offset := len(merged.Data)
		for _, item := range part.Data {
			item.Index = offset
			offset++
			merged.Data = append(merged.Data, item)
		}
		merged.PromptTokens += part.PromptTokens
		merged.CompletionTokens += part.CompletionTokens
		merged.CachedTokens += part.CachedTokens
		merged.UsageSeen = merged.UsageSeen || part.UsageSeen
		if part.Model != "" {
			merged.Model = part.Model
		}
	}
	merged.TotalTokens = merged.PromptTokens + merged.CompletionTokens
	return merged, nil
}

func splitEmbeddingInput(input any, size int) []any {
	if size <= 0 {
		size = maxUpstreamEmbeddingBatch
	}
	switch v := input.(type) {
	case []string:
		out := make([]any, 0, (len(v)+size-1)/size)
		for start := 0; start < len(v); start += size {
			end := start + size
			if end > len(v) {
				end = len(v)
			}
			out = append(out, v[start:end])
		}
		return out
	case []any:
		out := make([]any, 0, (len(v)+size-1)/size)
		for start := 0; start < len(v); start += size {
			end := start + size
			if end > len(v) {
				end = len(v)
			}
			out = append(out, v[start:end])
		}
		return out
	default:
		return []any{input}
	}
}

func (s *Service) resolveChannel(ch Channel) Channel {
	if ch.Timeout <= 0 {
		ch.Timeout = s.cfg.RequestTimeout
	}
	return ch
}

func (s *Service) settleChatSuccess(ctx context.Context, m Model, ch Channel, res *ChatResult, latency int, requestID string, attempt int, bizType, source, promptText string, hasMedia bool) {
	if res == nil {
		return
	}
	counts := ResolveUsageCounts(res.PromptTokens, res.CompletionTokens, res.CachedTokens, m.Encoding, promptText, res.Content, hasMedia)
	now := s.nowUnix()
	usage := Usage{
		RequestID:        requestID,
		Attempt:          attempt,
		ModelID:          m.ID,
		ModelName:        m.Name,
		ChannelID:        ch.ID,
		ChannelName:      ch.Name,
		CallType:         CallTypeChat,
		BizType:          bizType,
		Source:           source,
		Stream:           0,
		PromptTokens:     counts.PromptTokens,
		CompletionTokens: counts.CompletionTokens,
		TotalTokens:      counts.TotalTokens,
		CachedTokens:     counts.CachedTokens,
		TokensSource:     counts.Source,
		Cost:             s.costOrZero(m, counts.PromptTokens, counts.CompletionTokens),
		Currency:         s.cfg.Currency,
		LatencyMS:        latency,
		Status:           UsageStatusSuccess,
		CreateTime:       now,
		UpdateTime:       now,
	}
	acct := accountingContext(ctx)
	defer acct.cancel()
	s.saveUsage(acct.ctx, usage)
	if err := s.breaker.RecordTotals(acct.ctx, uint64(ch.ID), true, counts.TotalTokens, now); err != nil {
		log.Printf("ai: 累加渠道成功计数失败 channel=%d: %v", ch.ID, err)
	}
	if err := s.breaker.OnSuccess(acct.ctx, ch, now); err != nil {
		log.Printf("ai: 成功迁移熔断状态失败 channel=%d: %v", ch.ID, err)
	}
}

func (s *Service) settleChatFailure(ctx context.Context, m Model, ch Channel, callErr error, latency int, requestID string, attempt int, bizType, source string) {
	info := ClassifyError(callErr)
	now := s.nowUnix()
	usage := Usage{
		RequestID:    requestID,
		Attempt:      attempt,
		ModelID:      m.ID,
		ModelName:    m.Name,
		ChannelID:    ch.ID,
		ChannelName:  ch.Name,
		CallType:     CallTypeChat,
		BizType:      bizType,
		Source:       source,
		Stream:       0,
		TokensSource: TokensSourceNone,
		Cost:         "0.000000",
		Currency:     s.cfg.Currency,
		LatencyMS:    latency,
		Status:       info.Status,
		ErrorCode:    info.Code,
		ErrorMessage: truncateMessage(info.Message, 500),
		ErrorClass:   info.Class,
		CreateTime:   now,
		UpdateTime:   now,
	}
	acct := accountingContext(ctx)
	defer acct.cancel()
	s.saveUsage(acct.ctx, usage)
	if err := s.breaker.RecordTotals(acct.ctx, uint64(ch.ID), false, 0, now); err != nil {
		log.Printf("ai: 累加渠道失败计数失败 channel=%d: %v", ch.ID, err)
	}
	if err := s.breaker.OnFailure(acct.ctx, ch, info.Message, now); err != nil {
		log.Printf("ai: 失败迁移熔断状态失败 channel=%d: %v", ch.ID, err)
	}
}

func (s *Service) settleStreamSuccess(ctx context.Context, m Model, ch Channel, res *ChatResult, latency int, requestID string, bizType, source, promptText string, hasMedia bool) {
	counts := ResolveStreamUsage(res, m.Encoding, promptText, res.Content, hasMedia)
	now := s.nowUnix()
	usage := Usage{
		RequestID:        requestID,
		Attempt:          0,
		ModelID:          m.ID,
		ModelName:        m.Name,
		ChannelID:        ch.ID,
		ChannelName:      ch.Name,
		CallType:         CallTypeChat,
		BizType:          bizType,
		Source:           source,
		Stream:           1,
		PromptTokens:     counts.PromptTokens,
		CompletionTokens: counts.CompletionTokens,
		TotalTokens:      counts.TotalTokens,
		CachedTokens:     counts.CachedTokens,
		TokensSource:     counts.Source,
		Cost:             s.costOrZero(m, counts.PromptTokens, counts.CompletionTokens),
		Currency:         s.cfg.Currency,
		LatencyMS:        latency,
		Status:           UsageStatusSuccess,
		CreateTime:       now,
		UpdateTime:       now,
	}
	acct := accountingContext(ctx)
	defer acct.cancel()
	s.saveUsage(acct.ctx, usage)
	if err := s.breaker.RecordTotals(acct.ctx, uint64(ch.ID), true, counts.TotalTokens, now); err != nil {
		log.Printf("ai: 累加流式渠道成功计数失败 channel=%d: %v", ch.ID, err)
	}
	// 熔断 onSuccess 已在首块到达时执行；这里不再重复（稳态心跳也不会因此丢）。
}

// settleStreamFailure 是流式失败的唯一结算点（分支③与分支④共用）。
//
// 它按**分支③的判定**（res 非空且 Chunks>0，即上游已产出内容却没收尾）做补偿：
//
//	本次真实消耗了上游资源 ⇒ 按 ResolveStreamUsage(res, encoding, promptText, res.Content, hasMedia)
//	求 counts（上游已见 usage 优先，缺失才按 encoding 近似估算），并据此写
//	prompt/completion/total/cached tokens、tokens_source、cost，且 stream=1；
//	**status 仍是 fail|timeout**（失败语义不变），billed_partial=1 是审计标记
//	（这一行是部分计费行，与「完全没计费」的失败行可区分）。
//
// 分支④（res 为空或 Chunks==0）保持原样：tokens_source 为空串、cost='0.000000'、
// billed_partial=0（零值 ⇒ GORM 省略该列，落库用默认 0）。
//
// 熔断语义两边都不变：分支③不触发 onFailure（调用点用 started 守卫，见 ChatStream）；
// 本函数只写 usage 与渠道累计，不改 consecutive_failures / circuit。
//
// 分支③的 token 数**照常交给 RecordTotals**（tokens = counts.TotalTokens）：这些 token 上游已经
// 真实花掉了 ⇒ 渠道 TPM 预算必须扣（gate.go:20-21「上游真的花了多少就扣多少」）。DB 侧的
// `channel.total_tokens` 口径不受影响——AddChannelTotals 在 success=false 时只累加 total_failures、
// 忽略 tokens（见 store.go 的列注释与实现）。分支④（无块）仍传 0：没有可归因的上游消耗。
func (s *Service) settleStreamFailure(ctx context.Context, m Model, ch Channel, info ErrorInfo, latency int, requestID string, bizType, source string, res *ChatResult, promptText string, hasMedia bool) {
	now := s.nowUnix()
	chargedTokens := 0
	usage := Usage{
		RequestID:    requestID,
		Attempt:      0,
		ModelID:      m.ID,
		ModelName:    m.Name,
		ChannelID:    ch.ID,
		ChannelName:  ch.Name,
		CallType:     CallTypeChat,
		BizType:      bizType,
		Source:       source,
		Stream:       0,
		TokensSource: TokensSourceNone,
		Cost:         "0.000000",
		Currency:     s.cfg.Currency,
		LatencyMS:    latency,
		Status:       info.Status,
		ErrorCode:    info.Code,
		ErrorMessage: truncateMessage(info.Message, 500),
		ErrorClass:   info.Class,
		CreateTime:   now,
		UpdateTime:   now,
	}
	// 分支③（有块、无收尾）：部分计费补偿。
	if res != nil && res.Chunks > 0 {
		counts := ResolveStreamUsage(res, m.Encoding, promptText, res.Content, hasMedia)
		usage.Stream = 1
		usage.PromptTokens = counts.PromptTokens
		usage.CompletionTokens = counts.CompletionTokens
		usage.TotalTokens = counts.TotalTokens
		usage.CachedTokens = counts.CachedTokens
		usage.TokensSource = counts.Source
		usage.Cost = s.costOrZero(m, counts.PromptTokens, counts.CompletionTokens)
		usage.BilledPartial = 1
		chargedTokens = counts.TotalTokens // 上游确已消耗 ⇒ 进 TPM 扣减（与 usage 行同一份 counts）
	}
	acct := accountingContext(ctx)
	defer acct.cancel()
	s.saveUsage(acct.ctx, usage)
	if err := s.breaker.RecordTotals(acct.ctx, uint64(ch.ID), false, chargedTokens, now); err != nil {
		log.Printf("ai: 累加流式渠道失败计数失败 channel=%d: %v", ch.ID, err)
	}
}

func (s *Service) settleEmbeddingSuccess(ctx context.Context, m Model, ch Channel, res *EmbeddingResult, latency int, requestID string, attempt int, bizType, source, promptText string) {
	counts := ResolveUsageCounts(res.PromptTokens, res.CompletionTokens, res.CachedTokens, m.Encoding, promptText, "", false)
	now := s.nowUnix()
	usage := Usage{
		RequestID:        requestID,
		Attempt:          attempt,
		ModelID:          m.ID,
		ModelName:        m.Name,
		ChannelID:        ch.ID,
		ChannelName:      ch.Name,
		CallType:         CallTypeEmbedding,
		BizType:          bizType,
		Source:           source,
		Stream:           0,
		PromptTokens:     counts.PromptTokens,
		CompletionTokens: counts.CompletionTokens,
		TotalTokens:      counts.TotalTokens,
		CachedTokens:     counts.CachedTokens,
		TokensSource:     counts.Source,
		Cost:             s.costOrZero(m, counts.PromptTokens, counts.CompletionTokens),
		Currency:         s.cfg.Currency,
		LatencyMS:        latency,
		Status:           UsageStatusSuccess,
		CreateTime:       now,
		UpdateTime:       now,
	}
	acct := accountingContext(ctx)
	defer acct.cancel()
	s.saveUsage(acct.ctx, usage)
	if err := s.breaker.RecordTotals(acct.ctx, uint64(ch.ID), true, counts.TotalTokens, now); err != nil {
		log.Printf("ai: 累加 embedding 渠道成功计数失败 channel=%d: %v", ch.ID, err)
	}
	if err := s.breaker.OnSuccess(acct.ctx, ch, now); err != nil {
		log.Printf("ai: embedding 成功迁移熔断状态失败 channel=%d: %v", ch.ID, err)
	}
}

func (s *Service) settleEmbeddingFailure(ctx context.Context, m Model, ch Channel, callErr error, latency int, requestID string, attempt int, bizType, source string) {
	info := ClassifyError(callErr)
	now := s.nowUnix()
	usage := Usage{
		RequestID:    requestID,
		Attempt:      attempt,
		ModelID:      m.ID,
		ModelName:    m.Name,
		ChannelID:    ch.ID,
		ChannelName:  ch.Name,
		CallType:     CallTypeEmbedding,
		BizType:      bizType,
		Source:       source,
		Stream:       0,
		TokensSource: TokensSourceNone,
		Cost:         "0.000000",
		Currency:     s.cfg.Currency,
		LatencyMS:    latency,
		Status:       info.Status,
		ErrorCode:    info.Code,
		ErrorMessage: truncateMessage(info.Message, 500),
		ErrorClass:   info.Class,
		CreateTime:   now,
		UpdateTime:   now,
	}
	acct := accountingContext(ctx)
	defer acct.cancel()
	s.saveUsage(acct.ctx, usage)
	if err := s.breaker.RecordTotals(acct.ctx, uint64(ch.ID), false, 0, now); err != nil {
		log.Printf("ai: 累加 embedding 渠道失败计数失败 channel=%d: %v", ch.ID, err)
	}
	if err := s.breaker.OnFailure(acct.ctx, ch, info.Message, now); err != nil {
		log.Printf("ai: embedding 失败迁移熔断状态失败 channel=%d: %v", ch.ID, err)
	}
}

func (s *Service) recordLocalFailure(ctx context.Context, m Model, requestID, callType, bizType, source string, apiErr *APIError) {
	now := s.nowUnix()
	class := apiErr.Class
	if class == "" {
		class = ErrorClassLocal
	}
	usage := Usage{
		RequestID:    requestID,
		Attempt:      0,
		ModelID:      m.ID,
		ModelName:    m.Name,
		ChannelID:    0,
		ChannelName:  "",
		CallType:     callType,
		BizType:      bizType,
		Source:       source,
		Stream:       0,
		TokensSource: TokensSourceNone,
		Cost:         "0.000000",
		Currency:     s.cfg.Currency,
		Status:       UsageStatusFail,
		ErrorCode:    apiErr.Code,
		ErrorMessage: truncateMessage(apiErr.Message, 500),
		ErrorClass:   class,
		CreateTime:   now,
		UpdateTime:   now,
	}
	acct := accountingContext(ctx)
	defer acct.cancel()
	s.saveUsage(acct.ctx, usage)
}

func (s *Service) saveUsage(ctx context.Context, usage Usage) {
	if err := s.repo.UpsertUsage(ctx, &usage); err != nil {
		// 落库失败不打断上游响应。
		log.Printf("ai: 写入 usage 失败 request_id=%s attempt=%d: %v", usage.RequestID, usage.Attempt, err)
	}
}

// costOrZero 计算成本；算不出（单价非法 / 超出 numeric(12,6)）时记日志并按 0 落库，
// 保住这一行计费/审计流水（整行因列溢出/超界而写不进去才是更坏的静默丢失）。
func (s *Service) costOrZero(m Model, promptTokens, completionTokens int) string {
	cost, err := ComputeCost(promptTokens, completionTokens, m.PriceInput, m.PriceOutput)
	if err != nil {
		log.Printf("ai: 计算成本失败，该行按 0.000000 落库（请在模型管理核对单价） model=%s: %v", m.Name, err)
		return "0.000000"
	}
	return cost
}

func (s *Service) nowUnix() int64 {
	if s.now == nil {
		return time.Now().Unix()
	}
	return s.now()
}

type accountingCtx struct {
	ctx    context.Context
	cancel context.CancelFunc
}

// accountingContext 让记账/熔断写入不被客户端断开取消（记完再还请求）。
func accountingContext(parent context.Context) accountingCtx {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), 3*time.Second)
	return accountingCtx{ctx: ctx, cancel: cancel}
}

func retryCall[T any](ctx context.Context, label string, call func() (T, error)) (T, error) {
	var zero T
	var lastErr error
	for attempt := 0; attempt < maxTransientAttempts; attempt++ {
		if attempt > 0 {
			if err := sleepContext(ctx, time.Duration(attempt)*transientBackoffStep); err != nil {
				return zero, err
			}
			log.Printf("ai: %s 传输层重试 %d/%d（仅连接建立阶段失败）", label, attempt, maxTransientAttempts-1)
		}
		res, err := call()
		if err == nil {
			return res, nil
		}
		lastErr = err
		if !IsRetryable(err) {
			return zero, err
		}
	}
	return zero, lastErr
}

func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func modelNotFoundError(name string) *APIError {
	return &APIError{
		HTTPStatus: 404,
		Type:       "invalid_request_error",
		Code:       "model_not_found",
		Message:    fmt.Sprintf("模型 [%s] 不存在或未启用", name),
		Class:      ErrorClassLocal,
	}
}

// allUnavailableError 把候选耗尽翻成公开错误。
//
// 内部 Detail 保留最后错误；公开 Message 不泄露渠道/上游细节。
// 只有上游 429 与超时改状态码，其余统一 503（auth/upstream 不暴露真实含义）。
func allUnavailableError(lastErr error) *APIError {
	apiErr := &APIError{
		HTTPStatus: 503,
		Type:       "upstream_unavailable",
		Code:       "upstream_unavailable",
		Message:    "所有渠道均不可用",
		Class:      ErrorClassUpstream,
		Err:        lastErr,
	}
	if lastErr == nil {
		return apiErr
	}
	info := ClassifyError(lastErr)
	apiErr.Class = info.Class
	if info.Status == UsageStatusTimeout {
		apiErr.HTTPStatus = 504
		apiErr.Type = "timeout"
		apiErr.Code = "timeout"
		apiErr.Message = "上游请求超时"
		return apiErr
	}
	if info.Class == ErrorClassRateLimit {
		apiErr.HTTPStatus = 429
		apiErr.Type = "rate_limit_error"
		apiErr.Code = "rate_limit_error"
		apiErr.Message = "上游限流"
	}
	return apiErr
}

// streamPublicError 是流式收口的公开错误映射。
//
// 客户端断开给 499（nginx 约定）；此时响应通常已无法写出，handler 应直接跳过回执。
func streamPublicError(err error) *APIError {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrClientClosed) || errors.Is(err, context.Canceled) {
		return &APIError{
			HTTPStatus: 499,
			Type:       "client_closed",
			Code:       "client_closed",
			Message:    "客户端已断开",
			Class:      ErrorClassTransport,
			Err:        err,
		}
	}
	return allUnavailableError(err)
}
