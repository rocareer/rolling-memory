// plugin: ai v0.2.0
// sse.go 是 SSE 帧解析、流式转发与流式用量统计的实现。
//
// 解析口径：按 SSE 规范逐行读，`data:` 可多行（以 "\n" 拼回一帧），空行派发，
// 注释行（":" 开头）忽略，`[DONE]` 是结束哨兵。转发时每个 data 帧立即 Flush，
// 保证网关逐块可读（text/event-stream + X-Accel-Buffering: no 由 PrepareSSEHeaders 设置）。
//
// 用量口径：
//   - 上游最终 chunk 的 usage 优先（tokens_source='usage'）；
//   - 没有 usage（或 prompt/completion 全 0）时按累计内容做近似估算（tokens_source='estimate'）；
//   - 客户端断开时立即停止读取，但**已聚合的内容与已见 usage 保留在返回的 ChatResult 里**，
//     由编排层按「有块、无收尾」分支落一行失败（不改熔断状态）。
//
// 上限（本品 R1：源 PHP 侧对流式无任何体量护栏——RelayDriver 的 $buffer 与
// SseFrameRepair 都以「上游友好」为前提，生产实测有生图帧以 MB 计；Go 侧必须自带上限，
// 否则一个坏上游能把网关内存吃干）：
//   - 单行（MaxLineBytes，缺省 1MiB）：超限按上游协议异常终止，返回**不可重试**的
//     upstreamInvalid 并立即停止读取（不再从 upstream 拉一个字节）；
//   - 单帧（MaxFrameBytes，缺省 8MiB）：data 行拼帧的累积字节超限同样终止；
//   - 累积内容（MaxAccumulatedBytes，缺省 32MiB）：在 StreamAccumulator 内生效，
//     **不报错**——只置截断标记并停止累积（内容仍逐帧转发给客户端，计费口径不变）。
//
// 三级上限都走插件配置（sse_max_line_bytes / sse_max_frame_bytes / sse_max_accumulated_bytes）。
package ai

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"
)

// SSE 三级上限的缺省值（与公开面 8MiB 请求体同量级；配置 0/空/非法一律回落本处）。
const (
	// DefaultMaxSSELineBytes 单行上限（含行尾换行）。
	DefaultMaxSSELineBytes = 1 << 20 // 1MiB
	// DefaultMaxSSEFrameBytes 单帧上限（同帧 data 行拼接后的字节数）。
	DefaultMaxSSEFrameBytes = 8 << 20 // 8MiB
	// DefaultMaxSSEAccumulatedBytes 累积内容上限（content + reasoning）。
	DefaultMaxSSEAccumulatedBytes = 32 << 20 // 32MiB
)

// sseReadBufferBytes 是 bufio 的读缓冲大小：只决定单次读取的粒度，**不是**长度上限
// （行/帧的上限由 SSELimits 强制，跨缓冲累加在 readSSELine 里做）。
const sseReadBufferBytes = 64 * 1024

// SSELimits 是流式解析的三级上限（≤0 的字段一律回落缺省，防「配置 0 ⇒ 静默不限」）。
type SSELimits struct {
	// MaxLineBytes 单行上限（含行尾换行）。
	MaxLineBytes int
	// MaxFrameBytes 单帧上限（同帧 data 行拼接后的字节数）。
	MaxFrameBytes int
	// MaxAccumulatedBytes 累积内容上限（content + reasoning）。
	MaxAccumulatedBytes int
}

// DefaultSSELimits 返回缺省三级上限（1MiB / 8MiB / 32MiB）。
func DefaultSSELimits() SSELimits {
	return SSELimits{
		MaxLineBytes:        DefaultMaxSSELineBytes,
		MaxFrameBytes:       DefaultMaxSSEFrameBytes,
		MaxAccumulatedBytes: DefaultMaxSSEAccumulatedBytes,
	}
}

// normalized 补齐未配置（≤0）的上限。
func (l SSELimits) normalized() SSELimits {
	def := DefaultSSELimits()
	if l.MaxLineBytes <= 0 {
		l.MaxLineBytes = def.MaxLineBytes
	}
	if l.MaxFrameBytes <= 0 {
		l.MaxFrameBytes = def.MaxFrameBytes
	}
	if l.MaxAccumulatedBytes <= 0 {
		l.MaxAccumulatedBytes = def.MaxAccumulatedBytes
	}
	return l
}

// formatByteLimit 把字节上限写成可读文案（错误文案里要能一眼看出是哪个键）。
func formatByteLimit(n int) string {
	switch {
	case n >= 1<<20 && n%(1<<20) == 0:
		return fmt.Sprintf("%dMiB", n>>20)
	case n >= 1<<10 && n%(1<<10) == 0:
		return fmt.Sprintf("%dKiB", n>>10)
	default:
		return fmt.Sprintf("%d 字节", n)
	}
}

// errSSEDone 是读到 data: [DONE] 的内部哨兵（不是失败）。
var errSSEDone = errors.New("sse done")

// ErrClientClosed 表示客户端已断开（写回 SSE 失败或请求 context 被取消）。
var ErrClientClosed = errors.New("客户端已断开")

// StreamAbortedError 包装客户端断开的原因，同时保留已产生的聚合结果。
type StreamAbortedError struct {
	Err error
}

func (e *StreamAbortedError) Error() string {
	if e == nil || e.Err == nil {
		return ErrClientClosed.Error()
	}
	return ErrClientClosed.Error() + ": " + e.Err.Error()
}

func (e *StreamAbortedError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// Is 让 errors.Is(err, ErrClientClosed) 成立，编排层不必解析文案。
func (e *StreamAbortedError) Is(target error) bool { return target == ErrClientClosed }

// SSEEvent 是一个完整的 SSE 事件。
type SSEEvent struct {
	Event string
	Data  []byte
	ID    string
}

// ParseSSE 逐事件解析 SSE 流（施加缺省三级上限）。回调返回错误时立即停止并把该错误原样返回。
//
// 读到 EOF 时返回 nil；未以空行收尾的半个事件按规范丢弃。
// 单行/单帧超限 ⇒ 立即停止读取并返回**不可重试**的上游协议异常（不得静默截断为成功）。
func ParseSSE(r io.Reader, fn func(SSEEvent) error) error {
	return ParseSSEWithLimits(r, DefaultSSELimits(), fn)
}

// ParseSSEWithLimits 同 ParseSSE，但由调用方指定三级上限（驱动按插件配置传入）。
func ParseSSEWithLimits(r io.Reader, limits SSELimits, fn func(SSEEvent) error) error {
	if r == nil {
		return nil
	}
	limits = limits.normalized()
	br := bufio.NewReaderSize(r, sseReadBufferBytes)

	var (
		event      string
		id         string
		dataLines  []string
		frameBytes int // 同帧 data 行拼接后的字节数（含 Join 的 "\n" 分隔符）
	)
	reset := func() {
		event = ""
		id = ""
		dataLines = dataLines[:0]
		frameBytes = 0
	}
	dispatch := func() error {
		if len(dataLines) == 0 {
			reset()
			return nil
		}
		ev := SSEEvent{Event: event, ID: id, Data: []byte(strings.Join(dataLines, "\n"))}
		reset()
		return fn(ev)
	}

	for {
		line, readErr := readSSELine(br, limits.MaxLineBytes)
		if len(line) > 0 {
			text := string(line)
			text = strings.TrimSuffix(text, "\n")
			text = strings.TrimSuffix(text, "\r")
			if strings.HasPrefix(text, "\uFEFF") {
				text = strings.TrimPrefix(text, "\uFEFF")
			}
			switch {
			case text == "":
				if err := dispatch(); err != nil {
					return err
				}
			case strings.HasPrefix(text, ":"):
				// 注释/心跳，忽略。
			default:
				field, value, _ := strings.Cut(text, ":")
				value = strings.TrimPrefix(value, " ")
				switch field {
				case "data":
					frameBytes += len(value)
					if len(dataLines) > 0 {
						frameBytes++ // Join 的 "\n"
					}
					if frameBytes > limits.MaxFrameBytes {
						// 立即停止读取：`return` 之后本函数不再从 r 拉任何字节
						//（调用方 defer 的 Body.Close 负责断开）。
						return upstreamInvalid(fmt.Sprintf(
							"上游 SSE 单帧数据超过上限（>%s，插件设置 sse_max_frame_bytes）：已停止读取",
							formatByteLimit(limits.MaxFrameBytes)))
					}
					dataLines = append(dataLines, value)
				case "event":
					event = value
				case "id":
					id = value
				}
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return nil
			}
			return readErr
		}
	}
}

// readSSELine 读一行（含行尾 '\n'；EOF 时可能不含），并施加单行上限。
//
// 为什么不是 ReadString：ReadString 只按缓冲大小给切片，遇到超长行会**无界累加**——
// 上游发一行 1GiB 的垃圾就能把网关内存吃干。这里用 ReadSlice + 跨缓冲累加，
// 一旦超过 maxLineBytes 立即返回不可重试的上游异常（调用方随即停止读取）。
//
// ReadSlice 返回的切片指向 bufio 的内部缓冲（下次读即失效），故跨缓冲时必须立即拷贝。
func readSSELine(br *bufio.Reader, maxLineBytes int) ([]byte, error) {
	fragment, err := br.ReadSlice('\n')
	if len(fragment) > maxLineBytes {
		return nil, lineTooLongError(maxLineBytes)
	}
	if !errors.Is(err, bufio.ErrBufferFull) {
		return fragment, err
	}
	line := make([]byte, 0, 2*len(fragment))
	line = append(line, fragment...)
	for {
		fragment, err = br.ReadSlice('\n')
		line = append(line, fragment...)
		if len(line) > maxLineBytes {
			return nil, lineTooLongError(maxLineBytes)
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return line, err
	}
}

// lineTooLongError 单行超限的上游协议异常（不可重试：重发只会拿到同一行）。
func lineTooLongError(maxLineBytes int) error {
	return upstreamInvalid(fmt.Sprintf(
		"上游 SSE 单行超过上限（>%s，插件设置 sse_max_line_bytes）：已停止读取",
		formatByteLimit(maxLineBytes)))
}

// ChatChunk 是 OpenAI chat.completion.chunk 的解析结果（只取流式需要的字段）。
type ChatChunk struct {
	ID      string       `json:"id"`
	Model   string       `json:"model"`
	Choices []ChatChoice `json:"choices"`
	Usage   *UsageInfo   `json:"usage,omitempty"`
	Error   *streamError `json:"error,omitempty"`
}

// ChatChoice 是流式 chunk 的一个 choice。
type ChatChoice struct {
	Index        int       `json:"index"`
	Delta        ChatDelta `json:"delta"`
	FinishReason *string   `json:"finish_reason,omitempty"`
}

// ChatDelta 是增量内容。
type ChatDelta struct {
	Role             string          `json:"role,omitempty"`
	Content          string          `json:"content,omitempty"`
	ReasoningContent string          `json:"reasoning_content,omitempty"`
	ToolCalls        json.RawMessage `json:"tool_calls,omitempty"`
}

type streamError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    any    `json:"code"`
}

// ParseChatChunk 解析一个 data 帧。
//
// 上游在流中下发 {"error":{...}} 时转成 *UpstreamError（class=upstream）；
// 非法 JSON 也按上游异常处理——不能把它当空块静默吞掉。
func ParseChatChunk(data []byte) (*ChatChunk, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, upstreamInvalid("上游流式帧为空")
	}
	var chunk ChatChunk
	if err := json.Unmarshal(data, &chunk); err != nil {
		return nil, upstreamInvalid("上游流式帧不是合法 JSON: " + err.Error())
	}
	if chunk.Error != nil {
		code := chunk.Error.Type
		if c, ok := chunk.Error.Code.(string); ok && c != "" {
			code = c
		}
		msg := chunk.Error.Message
		if msg == "" {
			msg = "上游流式响应返回错误"
		}
		return nil, &UpstreamError{
			Class:   ErrorClassUpstream,
			Code:    code,
			Message: truncateMessage(msg, 500),
		}
	}
	return &chunk, nil
}

// StreamAccumulator 聚合流式内容与 usage。
type StreamAccumulator struct {
	id        string
	model     string
	content   strings.Builder
	reasoning strings.Builder
	finish    string
	toolCalls json.RawMessage
	usage     *UsageInfo
	chunks    int

	// maxAccumulatedBytes 是 content+reasoning 的累积上限（≤0 ⇒ 缺省 32MiB）；
	// accumulatedBytes 是已累积字节数；Truncated 置位后**不再累积**（但内容照常转发）。
	maxAccumulatedBytes int
	accumulatedBytes    int
	// Truncated 表示上游流超过累积上限、聚合内容已被截断（经 ChatResult.Truncated 出口）。
	Truncated bool

	// Aborted 由驱动在客户端断开/写失败时置位；不参与 usage 决策。
	Aborted bool
}

// NewStreamAccumulator 构造流式聚合器（limits.MaxAccumulatedBytes 为 content+reasoning 累积上限）。
func NewStreamAccumulator(limits SSELimits) *StreamAccumulator {
	return &StreamAccumulator{maxAccumulatedBytes: limits.normalized().MaxAccumulatedBytes}
}

// accumulationLimit 取生效上限（零值构造器也受缺省保护，避免「忘了传 ⇒ 不限」）。
func (a *StreamAccumulator) accumulationLimit() int {
	if a.maxAccumulatedBytes > 0 {
		return a.maxAccumulatedBytes
	}
	return DefaultMaxSSEAccumulatedBytes
}

// appendLimited 在累积上限内写入内容：
//   - 未触顶 ⇒ 整段写入；
//   - 本段会触顶 ⇒ 只写入能容纳的完整前缀（不切断 UTF-8 字符）并置 Truncated；
//   - 已触顶 ⇒ 丢弃后续内容（Truncated 保持为真）。
//
// 超限**不是错误**：已收到的内容仍要落库与回执，只是不再把上游的无限流堆进内存
// （usage 决策仍走原口径：上游 usage 优先，缺失时按截断后的内容估算）。
func (a *StreamAccumulator) appendLimited(dst *strings.Builder, text string) {
	if text == "" {
		return
	}
	limit := a.accumulationLimit()
	if a.accumulatedBytes >= limit {
		a.Truncated = true
		return
	}
	if room := limit - a.accumulatedBytes; len(text) > room {
		cut := room
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
		dst.WriteString(text[:cut])
		a.accumulatedBytes += cut
		a.Truncated = true
		return
	}
	dst.WriteString(text)
	a.accumulatedBytes += len(text)
}

// Apply 吸收一个 chunk。
func (a *StreamAccumulator) Apply(chunk *ChatChunk) {
	if chunk == nil {
		return
	}
	a.chunks++
	if chunk.ID != "" {
		a.id = chunk.ID
	}
	if chunk.Model != "" {
		a.model = chunk.Model
	}
	if chunk.Usage != nil {
		a.usage = chunk.Usage
	}
	if len(chunk.Choices) == 0 {
		return
	}
	choice := chunk.Choices[0]
	a.appendLimited(&a.content, choice.Delta.Content)
	a.appendLimited(&a.reasoning, choice.Delta.ReasoningContent)
	if choice.FinishReason != nil && *choice.FinishReason != "" {
		a.finish = *choice.FinishReason
	}
	if !isEmptyRawJSON(choice.Delta.ToolCalls) {
		a.toolCalls = choice.Delta.ToolCalls
	}
}

// Result 产出聚合结果。usage 全 0 时 UsageSeen 仍为 true（由上层决定是否估算）。
func (a *StreamAccumulator) Result() *ChatResult {
	res := &ChatResult{
		ID:               a.id,
		Model:            a.model,
		Content:          a.content.String(),
		ReasoningContent: a.reasoning.String(),
		FinishReason:     a.finish,
		ToolCalls:        a.toolCalls,
		Chunks:           a.chunks,
		Aborted:          a.Aborted,
		Truncated:        a.Truncated,
	}
	applyUsage(res, a.usage)
	return res
}

// ResolveStreamUsage 决定流式收尾的 token 计数。
//
// 上游最终 usage 优先；缺失或全 0 时按 promptText + completionText 近似估算。
// encoding=” 时既不用 usage 之外的估算，也保持 tokens_source=”（既定口径）。
func ResolveStreamUsage(res *ChatResult, encoding, promptText, completionText string, hasMedia bool) UsageResolution {
	if res == nil {
		return UsageResolution{Source: TokensSourceNone}
	}
	if res.UsageSeen && (res.PromptTokens > 0 || res.CompletionTokens > 0) {
		return UsageResolution{
			PromptTokens:     res.PromptTokens,
			CompletionTokens: res.CompletionTokens,
			TotalTokens:      res.PromptTokens + res.CompletionTokens,
			CachedTokens:     res.CachedTokens,
			Source:           TokensSourceUsage,
		}
	}
	if completionText == "" {
		completionText = res.Content
	}
	return ResolveUsageCounts(0, 0, res.CachedTokens, encoding, promptText, completionText, hasMedia)
}

// PrepareSSEHeaders 设置网关 SSE 响应头（含反代缓冲关闭）。
func PrepareSSEHeaders(h http.Header) {
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache, no-transform")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
}

// SSEWriter 逐帧写 data 并立即 Flush。
type SSEWriter struct {
	w io.Writer
	f http.Flusher
}

// NewSSEWriter 从 http.ResponseWriter 构造（gin 的 ResponseWriter 实现 http.Flusher）。
func NewSSEWriter(w http.ResponseWriter) *SSEWriter {
	sw := &SSEWriter{}
	if w == nil {
		return sw
	}
	sw.w = w
	if f, ok := w.(http.Flusher); ok {
		sw.f = f
	}
	return sw
}

// Data 写一个 data 帧（用户给的 payload 原样转发，不重新编码）。
func (s *SSEWriter) Data(payload []byte) error {
	if s == nil || s.w == nil {
		return errors.New("SSE writer 未初始化")
	}
	frame := make([]byte, 0, len(payload)+8)
	frame = append(frame, "data: "...)
	frame = append(frame, payload...)
	frame = append(frame, '\n', '\n')
	if _, err := s.w.Write(frame); err != nil {
		return err
	}
	if s.f != nil {
		s.f.Flush()
	}
	return nil
}

// Comment 写一个 SSE 注释帧（可用于心跳；v1 不主动发心跳）。
func (s *SSEWriter) Comment(text string) error {
	if s == nil || s.w == nil {
		return errors.New("SSE writer 未初始化")
	}
	frame := []byte(": " + text + "\n\n")
	if _, err := s.w.Write(frame); err != nil {
		return err
	}
	if s.f != nil {
		s.f.Flush()
	}
	return nil
}

// Done 写结束帧 data: [DONE]。
func (s *SSEWriter) Done() error { return s.Data([]byte("[DONE]")) }

// FormatSSEError 把错误编码成 OpenAI 形状的错误帧（流已开始后不能改 HTTP 状态码，
// 只能以 data 帧告知客户端；网关在未开始写流时才用真实状态码）。
func FormatSSEError(message, errType, code string) []byte {
	b, err := json.Marshal(map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    errType,
			"code":    code,
		},
	})
	if err != nil {
		return []byte(fmt.Sprintf(`{"error":{"message":%q,"type":%q,"code":%q}}`, message, errType, code))
	}
	return b
}
