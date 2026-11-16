// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package eval

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/rocareer/rolling-memory/internal/config"
	"github.com/rocareer/rolling-memory/internal/httpclient"
)

// 答题 / 判官的模型面重试预算。
//
// 观测依据：本地离线臂 1,013 题实测 30 题（3.0%）拿到空答案并因此被静默判 0。
// 空正文与传输类瞬时故障按独立事件计，3 次尝试可把残留失败压到 0.03³ ≈ 2.7e-5 量级；
// 尝试次数不能再大：单次尝试最坏 = httpclient 内层 3 次 HTTP × 超时，评测逐题串行摊销不起。
// 退避 1s 起、倍增、8s 封顶（最坏额外等待 2s + 4s），与 ingest 侧 retryTransient 同形不同预算——
// 写穿是吞吐问题，模型调用是尾延迟问题，故不共用同一组常数。
const (
	chatMaxAttempts   = 3
	chatRetryBaseWait = time.Second
	chatRetryMaxWait  = 8 * time.Second
)

// errChatEmptyContent 标记「模型回执正文为空」：语义上属瞬时故障（推理模型耗尽 token 帽、
// 上游截断），可重试；与鉴权/契约类错误区分，后者一律原样上抛。
var errChatEmptyContent = errors.New("模型回执正文为空")

// ChatOptions 是答题器 / 判官的模型面配置（[eval] 段或命令行覆盖）。
//
// 任何 OpenAI 兼容 /chat/completions 端点都可用：base_url 允许已经带路径
// （如网关形态 `https://host/v1/<key>/<chan>`），也允许直接给到 `/chat/completions`。
type ChatOptions struct {
	BaseURL         string
	Model           string
	APIKey          string
	Temperature     float64
	MaxTokens       int
	TimeoutSec      int
	Thinking        bool
	ReasoningEffort string
}

// ChatClient 是一次评测运行里的模型通道（零状态、可并发复用）。
type ChatClient struct {
	url       string
	model     string
	key       string
	temp      float64
	maxTokens int
	timeout   time.Duration
	thinking  bool
	effort    string
}

// NewChatClient 解析端点、跑出网策略（local 模式只允许 loopback，与产品其余腿同口径）。
func NewChatClient(cfg *config.Config, o ChatOptions) (*ChatClient, error) {
	model := strings.TrimSpace(o.Model)
	if model == "" {
		return nil, fmt.Errorf("评测模型未配置：设 [eval] model 或 --eval-model")
	}
	base := strings.TrimRight(strings.TrimSpace(o.BaseURL), "/")
	if base == "" {
		return nil, fmt.Errorf("评测端点未配置：设 [eval] base_url 或 --eval-base-url")
	}
	url := base
	if !strings.HasSuffix(url, "/chat/completions") {
		url += "/chat/completions"
	}
	timeout := o.TimeoutSec
	if timeout <= 0 {
		timeout = 60
	}
	if err := cfg.CheckEgress(url); err != nil {
		return nil, err
	}
	return &ChatClient{
		url:       url,
		model:     model,
		key:       o.APIKey,
		temp:      o.Temperature,
		maxTokens: o.MaxTokens,
		timeout:   time.Duration(timeout) * time.Second,
		thinking:  o.Thinking,
		effort:    strings.TrimSpace(o.ReasoningEffort),
	}, nil
}

// URL reports the resolved endpoint (report/metrics use it for audit).
func (c *ChatClient) URL() string { return c.url }

// Model reports the model name sent on the wire.
func (c *ChatClient) Model() string { return c.model }

// Complete 发一轮对话并取回文本（system + user 两条消息）。
//
// 请求体：
//   - 思考开关按 DeepSeek 官方写法 `thinking.type = enabled|disabled`；
//   - `reasoning_effort` 显式透传（缺省 low）；
//   - `max_tokens` 仅在 >0 时下发（0 = 不设帽，让推理模型先思考再作答）；
//   - 不注入 response_format，JSON 由提示词约束。
func (c *ChatClient) Complete(system, user string) (string, error) {
	payload := map[string]any{
		"model": c.model,
		"messages": []map[string]any{
			{"role": "system", "content": system},
			{"role": "user", "content": user},
		},
		"temperature": c.temp,
		"stream":      false,
	}
	if c.maxTokens > 0 {
		payload["max_tokens"] = c.maxTokens
	}
	// 思考开关必须同时按两家上游的契约下发（各自取自己认的那个）：
	//   - DeepSeek 官方：`thinking.type = enabled|disabled`；
	//   - 网宿 AI 网关（`deepseek-v4.1-flash` 所在渠道）：**不识别** `thinking.type`，
	//     静默忽略后思考默认开启；它只认 `reasoning_effort = none|low|medium|high`，
	//     **不带该字段 = 缺省开**，隐藏推理不受 max_tokens 约束，会把正文吃空
	//     （上游实证：该渠道只认 reasoning_effort，静默忽略 thinking.type）。
	// 注意 `thinking: null` 与"不带字段"等价（不表态 ⇒ 开），**不能**用作关闭开关。
	if c.thinking {
		payload["thinking"] = map[string]any{"type": "enabled"}
		effort := strings.TrimSpace(c.effort)
		if effort == "" {
			effort = "high"
		}
		payload["reasoning_effort"] = effort
	} else {
		payload["thinking"] = map[string]any{"type": "disabled"}
		payload["reasoning_effort"] = "none"
	}
	response, err := httpclient.PostJSON(c.url, payload, httpclient.Bearer(c.key), c.timeout, 2)
	if err != nil {
		return "", err
	}
	choices, _ := response["choices"].([]any)
	if len(choices) == 0 {
		return "", fmt.Errorf("模型回执没有 choices（endpoint %s）", httpclient.URLHostSafe(c.url))
	}
	first, _ := choices[0].(map[string]any)
	message, _ := first["message"].(map[string]any)
	content, _ := message["content"].(string)
	if strings.TrimSpace(content) == "" {
		finish, _ := first["finish_reason"].(string)
		reasoning, _ := message["reasoning_content"].(string)
		return "", fmt.Errorf("%w（finish_reason=%q · reasoning %d 字）：推理模型须放开 max_tokens 帽并置 thinking=enabled",
			errChatEmptyContent, finish, len([]rune(reasoning)))
	}
	return content, nil
}

// CompleteWithRetry 调 Complete，并对传输类瞬时故障与空正文按指数退避重试。
//
// 调用方必须在最终失败时把该题记为 unscored 并写明原因，禁止把空答案静默判 0
// （空答案不是「答错」，是「没答上」；计入分母会把读者能力与链路可靠性混为一谈）。
func (c *ChatClient) CompleteWithRetry(system, user string) (string, error) {
	var lastErr error
	wait := chatRetryBaseWait
	for try := 1; try <= chatMaxAttempts; try++ {
		text, err := c.Complete(system, user)
		if err == nil {
			return text, nil
		}
		lastErr = err
		if !isRetryableChatError(err) || try == chatMaxAttempts {
			break
		}
		time.Sleep(wait)
		wait *= 2
		if wait > chatRetryMaxWait {
			wait = chatRetryMaxWait
		}
	}
	return "", lastErr
}

// isRetryableChatError 只有瞬时类可重试：空正文、限流、暂时不可用、连接级故障、超时。
// 鉴权 / 参数 / 契约错误不重试（fail-closed 快速失败，避免掩盖配置错误）。
func isRetryableChatError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, errChatEmptyContent) {
		return true
	}
	return isRetryableUpstreamError(err)
}
