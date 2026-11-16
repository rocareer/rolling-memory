// plugin: ai v0.2.0
package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestParseSSEFrames(t *testing.T) {
	input := ": ping\n\n" +
		"data: {\"a\":1}\n\n" +
		"data: line1\ndata: line2\n\n" +
		"event: custom\nid: 7\nretry: 100\ndata: x\n\n" +
		"data\n\n" // 空 data 行也是一个事件
	var events []SSEEvent
	if err := ParseSSE(strings.NewReader(input), func(ev SSEEvent) error {
		events = append(events, ev)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(events) != 4 {
		t.Fatalf("事件数 = %d，期望 4：%+v", len(events), events)
	}
	if string(events[0].Data) != `{"a":1}` {
		t.Fatalf("事件 0 data = %q", events[0].Data)
	}
	if string(events[1].Data) != "line1\nline2" {
		t.Fatalf("多行 data 拼接 = %q", events[1].Data)
	}
	if events[2].Event != "custom" || events[2].ID != "7" || string(events[2].Data) != "x" {
		t.Fatalf("事件 2 = %+v", events[2])
	}
	if string(events[3].Data) != "" {
		t.Fatalf("空 data 事件 = %q", events[3].Data)
	}
}

func TestParseSSECRLFAndComments(t *testing.T) {
	input := ": keepalive\r\n\r\ndata: a\r\n\r\ndata: b\r\n\r\n"
	var got []string
	if err := ParseSSE(strings.NewReader(input), func(ev SSEEvent) error {
		got = append(got, string(ev.Data))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("CRLF 解析 = %+v", got)
	}
}

func TestParseSSEDropsIncompleteEventAtEOF(t *testing.T) {
	var got []string
	if err := ParseSSE(strings.NewReader("data: x"), func(ev SSEEvent) error {
		got = append(got, string(ev.Data))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("未以空行收尾的半个事件应丢弃：%+v", got)
	}
}

func TestParseSSEStopsOnCallbackError(t *testing.T) {
	sentinel := errors.New("stop")
	err := ParseSSE(strings.NewReader("data: a\n\ndata: b\n\n"), func(ev SSEEvent) error {
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("ParseSSE 应原样返回回调错误，实际 %v", err)
	}
}

func TestParseChatChunkContentUsageAndFinish(t *testing.T) {
	data := []byte(`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"content":"你好"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":2}}}`)
	chunk, err := ParseChatChunk(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunk.Choices) != 1 || chunk.Choices[0].Delta.Content != "你好" {
		t.Fatalf("内容解析错误：%+v", chunk)
	}
	if chunk.Choices[0].FinishReason == nil || *chunk.Choices[0].FinishReason != "stop" {
		t.Fatalf("finish_reason 解析错误：%+v", chunk.Choices[0])
	}
	if chunk.Usage == nil || chunk.Usage.PromptTokens != 3 || chunk.Usage.CompletionTokens != 5 {
		t.Fatalf("usage 解析错误：%+v", chunk.Usage)
	}
	if got := chunk.Usage.CachedTokens(); got != 2 {
		t.Fatalf("cached_tokens = %d，期望 2", got)
	}
}

func TestParseChatChunkUsageFallbackCacheField(t *testing.T) {
	chunk, err := ParseChatChunk([]byte(`{"choices":[],"usage":{"prompt_tokens":1,"prompt_cache_hit_tokens":9}}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := chunk.Usage.CachedTokens(); got != 9 {
		t.Fatalf("prompt_cache_hit_tokens 退化 = %d，期望 9", got)
	}
}

func TestParseChatChunkErrorEnvelope(t *testing.T) {
	_, err := ParseChatChunk([]byte(`{"error":{"message":"bad request","type":"invalid_request_error","code":"bad"}}`))
	if err == nil {
		t.Fatal("流中 error 帧应返回错误")
	}
	var ue *UpstreamError
	if !errors.As(err, &ue) {
		t.Fatalf("错误类型 = %T，期望 *UpstreamError", err)
	}
	if ue.Class != ErrorClassUpstream || ue.Code != "bad" || !strings.Contains(ue.Message, "bad request") {
		t.Fatalf("错误分类 = %+v", ue)
	}
}

func TestParseChatChunkInvalidJSON(t *testing.T) {
	if _, err := ParseChatChunk([]byte("{not json")); err == nil {
		t.Fatal("非法 JSON 应返回错误，而不是静默当空块")
	}
}

func TestStreamAccumulator(t *testing.T) {
	acc := &StreamAccumulator{}
	acc.Apply(mustChunk(t, `{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"你"}}]}`))
	acc.Apply(mustChunk(t, `{"id":"c1","choices":[{"index":0,"delta":{"content":"好"},"finish_reason":"stop"}]}`))
	acc.Apply(mustChunk(t, `{"id":"c1","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":5}}`))

	res := acc.Result()
	if res.Content != "你好" || res.FinishReason != "stop" || res.ID != "c1" || res.Model != "m" {
		t.Fatalf("聚合结果错误：%+v", res)
	}
	if res.Chunks != 3 || !res.UsageSeen || res.PromptTokens != 3 || res.CompletionTokens != 5 || res.TotalTokens != 8 {
		t.Fatalf("聚合 usage 错误：%+v", res)
	}
}

func mustChunk(t *testing.T, data string) *ChatChunk {
	t.Helper()
	chunk, err := ParseChatChunk([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	return chunk
}

// ===== SSE 三级上限（单行 / 单帧 / 累积）=====

// countingReader 记录被读取的字节数：用于断言「超限即停止读取」（不再从上游拉一个字节）。
type countingReader struct {
	data []byte
	off  int
}

func (r *countingReader) Read(p []byte) (int, error) {
	if r.off >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.off:])
	r.off += n
	return n, nil
}

// assertNonRetryableUpstream 断言「上游协议异常」三件套：分类 = upstream/invalid_response、
// 不可重试。上限类失败重发只会拿到同样的帧，标记可重试等于让上游重复计费。
func assertNonRetryableUpstream(t *testing.T, err error) *UpstreamError {
	t.Helper()
	var upstream *UpstreamError
	if !errors.As(err, &upstream) {
		t.Fatalf("错误类型 = %T（%v），期望 *UpstreamError", err, err)
	}
	if upstream.Class != ErrorClassUpstream || upstream.Code != "invalid_response" {
		t.Fatalf("错误分类 = %+v，期望 upstream/invalid_response", upstream)
	}
	if upstream.Retryable || IsRetryable(err) {
		t.Fatalf("流式上限异常必须不可重试：%+v", upstream)
	}
	return upstream
}

func TestParseSSEStopsOnOverlongLine(t *testing.T) {
	limits := DefaultSSELimits()
	limits.MaxLineBytes = 1024
	// 一条永不换行的巨行（8MiB）后跟一个正常帧：解析必须在超限处终止，
	// **不得**继续读到后面的内容，更不得把巨行截断后当成功返回。
	data := make([]byte, 0, (8<<20)+32)
	data = append(data, bytes.Repeat([]byte("A"), 8<<20)...)
	data = append(data, "\n\ndata: {\"ok\":1}\n\n"...)
	reader := &countingReader{data: data}

	var events int
	err := ParseSSEWithLimits(reader, limits, func(SSEEvent) error {
		events++
		return nil
	})
	upstream := assertNonRetryableUpstream(t, err)
	if !strings.Contains(upstream.Message, "sse_max_line_bytes") {
		t.Fatalf("错误文案应指向配置键 sse_max_line_bytes：%s", upstream.Message)
	}
	if events != 0 {
		t.Fatalf("超限前不应派发事件，实际 %d", events)
	}
	if reader.off >= len(data) {
		t.Fatalf("必须停止读取：已读 %d/%d 字节", reader.off, len(data))
	}
	if reader.off > 256<<10 {
		t.Fatalf("超限后仍在读取：已读 %d 字节（总 %d）", reader.off, len(data))
	}
}

func TestParseSSEStopsOnOversizedFrame(t *testing.T) {
	limits := DefaultSSELimits()
	limits.MaxFrameBytes = 4096
	// 同一帧里连发多行 data（各 100 字节、无空行分隔）：帧累积超限即终止。
	var builder strings.Builder
	for i := 0; i < 5000; i++ {
		builder.WriteString("data: ")
		builder.WriteString(strings.Repeat("B", 100))
		builder.WriteString("\n")
	}
	builder.WriteString("\ndata: [DONE]\n\n")
	data := []byte(builder.String())
	reader := &countingReader{data: data}

	var events int
	err := ParseSSEWithLimits(reader, limits, func(SSEEvent) error {
		events++
		return nil
	})
	upstream := assertNonRetryableUpstream(t, err)
	if !strings.Contains(upstream.Message, "sse_max_frame_bytes") {
		t.Fatalf("错误文案应指向配置键 sse_max_frame_bytes：%s", upstream.Message)
	}
	if events != 0 {
		t.Fatalf("超限前不应派发事件（空行才派发），实际 %d", events)
	}
	if reader.off >= len(data) {
		t.Fatalf("必须停止读取：已读 %d/%d 字节", reader.off, len(data))
	}
	if reader.off > 256<<10 {
		t.Fatalf("超限后仍在读取：已读 %d 字节（总 %d）", reader.off, len(data))
	}
}

func TestParseSSEWithinLimitsStillParses(t *testing.T) {
	limits := SSELimits{MaxLineBytes: 4096, MaxFrameBytes: 8192, MaxAccumulatedBytes: 16384}
	input := "data: {\"a\":1}\n\ndata: line1\ndata: line2\n\ndata: [DONE]\n\n"
	var got []string
	if err := ParseSSEWithLimits(strings.NewReader(input), limits, func(ev SSEEvent) error {
		got = append(got, string(ev.Data))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0] != `{"a":1}` || got[1] != "line1\nline2" || got[2] != "[DONE]" {
		t.Fatalf("上限内的解析结果 = %+v", got)
	}
}

func TestParseSSEZeroLimitsFallBackToDefaults(t *testing.T) {
	// 配置缺省/非法（0）时回落缺省上限：**不允许**「0 = 不限」的静默放开。
	normalized := SSELimits{}.normalized()
	if normalized != DefaultSSELimits() {
		t.Fatalf("零值上限必须回落缺省：%+v", normalized)
	}
	var events int
	if err := ParseSSEWithLimits(strings.NewReader("data: x\n\n"), SSELimits{}, func(SSEEvent) error {
		events++
		return nil
	}); err != nil || events != 1 {
		t.Fatalf("零值上限仍应正常解析：events=%d err=%v", events, err)
	}
}

func TestStreamAccumulatorStopsAtAccumulatedLimit(t *testing.T) {
	limits := DefaultSSELimits()
	limits.MaxAccumulatedBytes = 64
	acc := NewStreamAccumulator(limits)
	for i := 0; i < 8; i++ {
		acc.Apply(mustChunk(t, `{"id":"c1","choices":[{"index":0,"delta":{"content":"0123456789"}}]}`))
	}
	acc.Apply(mustChunk(t, `{"id":"c1","choices":[{"index":0,"delta":{"reasoning_content":"推理内容推理内容"}}]}`))
	acc.Apply(mustChunk(t, `{"id":"c1","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":5}}`))

	res := acc.Result()
	if !res.Truncated {
		t.Fatal("累积超限必须置截断标记（经 ChatResult.Truncated 出口）")
	}
	if len(res.Content) != 64 {
		t.Fatalf("content 长度 = %d，应停在阈值 64 内", len(res.Content))
	}
	if res.ReasoningContent != "" {
		t.Fatalf("触顶后不得再累积 reasoning：%q", res.ReasoningContent)
	}
	if res.Chunks != 10 {
		t.Fatalf("帧仍照收（只是不再累积内容）：Chunks=%d", res.Chunks)
	}
	// usage 决策仍走原口径：上游 usage 优先，与是否截断无关。
	got := ResolveStreamUsage(res, EncodingTokenzR, "", "", false)
	if got.Source != TokensSourceUsage || got.PromptTokens != 3 || got.CompletionTokens != 5 || got.TotalTokens != 8 {
		t.Fatalf("截断不得改变 usage 决策：%+v", got)
	}
}

func TestStreamAccumulatorTruncationKeepsUTF8Boundary(t *testing.T) {
	limits := DefaultSSELimits()
	limits.MaxAccumulatedBytes = 4 // "你好" 是 6 字节：触顶处不得切断多字节字符
	acc := NewStreamAccumulator(limits)
	acc.Apply(mustChunk(t, `{"choices":[{"index":0,"delta":{"content":"你好"}}]}`))
	res := acc.Result()
	if !res.Truncated {
		t.Fatal("应置截断标记")
	}
	if res.Content != "你" {
		t.Fatalf("截断必须落在完整 UTF-8 边界：%q（%d 字节）", res.Content, len(res.Content))
	}
}

func TestStreamAccumulatorWithinLimitKeepsEverything(t *testing.T) {
	limits := DefaultSSELimits()
	limits.MaxAccumulatedBytes = 1024
	acc := NewStreamAccumulator(limits)
	for i := 0; i < 10; i++ {
		acc.Apply(mustChunk(t, `{"choices":[{"index":0,"delta":{"content":"0123456789"}}]}`))
	}
	res := acc.Result()
	if res.Truncated {
		t.Fatal("未触顶不得置截断标记")
	}
	if len(res.Content) != 100 {
		t.Fatalf("content 长度 = %d，期望 100", len(res.Content))
	}
	// 零值构造（既有写法 &StreamAccumulator{}）也必须受缺省上限保护。
	if (&StreamAccumulator{}).accumulationLimit() != DefaultMaxSSEAccumulatedBytes {
		t.Fatal("零值构造器必须回落缺省累积上限")
	}
}

func TestOpenAIDriverChatStreamFailsOnOversizedUpstreamFrame(t *testing.T) {
	cases := []struct {
		name  string
		write func(w http.ResponseWriter, flusher http.Flusher)
	}{
		{
			"单行超限",
			func(w http.ResponseWriter, flusher http.Flusher) {
				fmt.Fprint(w, "data: ")
				for i := 0; i < 128; i++ { // 128 × 4KiB = 512KiB，远超 MaxLineBytes=1024
					fmt.Fprint(w, strings.Repeat("X", 4096))
					if flusher != nil {
						flusher.Flush()
					}
				}
				fmt.Fprint(w, "\n\ndata: [DONE]\n\n")
				if flusher != nil {
					flusher.Flush()
				}
			},
		},
		{
			"单帧超限",
			func(w http.ResponseWriter, flusher http.Flusher) {
				for i := 0; i < 64; i++ { // 64 × 1KiB 的 data 行，同帧累积 64KiB > MaxFrameBytes=2048
					fmt.Fprintf(w, "data: %s\n", strings.Repeat("Y", 1024))
					if flusher != nil {
						flusher.Flush()
					}
				}
				fmt.Fprint(w, "\ndata: [DONE]\n\n")
				if flusher != nil {
					flusher.Flush()
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				flusher, _ := w.(http.Flusher)
				tc.write(w, flusher)
			}))
			defer server.Close()

			driver := NewOpenAIDriverWithSSELimits(true,
				SSELimits{MaxLineBytes: 1024, MaxFrameBytes: 2048, MaxAccumulatedBytes: 4096})
			var forwarded []string
			res, err := driver.ChatStream(context.Background(), Channel{
				ID: 1, BaseURL: server.URL, APIKey: "test-key", Model: "m", Timeout: 5,
			}, ChatRequest{Messages: []Message{{Role: "user", Content: "hi"}}}, func(data []byte) error {
				forwarded = append(forwarded, string(data))
				return nil
			})
			if err == nil {
				t.Fatal("上游超限必须返回错误，不得静默截断为成功")
			}
			assertNonRetryableUpstream(t, err)
			if res == nil || res.Chunks != 0 || res.Content != "" {
				t.Fatalf("超限前不应有聚合内容：%+v", res)
			}
			if len(forwarded) != 0 {
				t.Fatalf("超长帧不得转发给客户端：%+v", forwarded)
			}
		})
	}
}

func TestOpenAIDriverChatStreamTruncatesAccumulationButKeepsForwarding(t *testing.T) {
	const chunks = 40
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		for i := 0; i < chunks; i++ {
			fmt.Fprintf(w, "data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"%s\"}}]}\n\n",
				strings.Repeat("z", 100))
			if flusher != nil {
				flusher.Flush()
			}
		}
		fmt.Fprint(w, "data: {\"id\":\"c1\",\"choices\":[],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":2}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	}))
	defer server.Close()

	driver := NewOpenAIDriverWithSSELimits(true,
		SSELimits{MaxLineBytes: 1 << 20, MaxFrameBytes: 1 << 20, MaxAccumulatedBytes: 1000})
	var forwardedContent, forwardedUsage int
	res, err := driver.ChatStream(context.Background(), Channel{
		ID: 1, BaseURL: server.URL, APIKey: "test-key", Model: "m", Timeout: 5,
	}, ChatRequest{Messages: []Message{{Role: "user", Content: "hi"}}}, func(data []byte) error {
		switch {
		case bytes.Contains(data, []byte(`"usage"`)):
			forwardedUsage++
		case bytes.Contains(data, []byte(`"content"`)):
			forwardedContent++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("累积超限不是错误（客户端应照常收完流）：%v", err)
	}
	if res == nil || !res.Truncated {
		t.Fatalf("应置截断标记：%+v", res)
	}
	if len(res.Content) != 1000 {
		t.Fatalf("content 长度 = %d，应停在阈值 1000 内", len(res.Content))
	}
	// 上限只保护**聚合内存**：触顶后的帧仍须逐帧转发（含收尾的 usage 帧）。
	if forwardedContent != chunks {
		t.Fatalf("内容帧转发数 = %d，期望 %d", forwardedContent, chunks)
	}
	if forwardedUsage != 1 {
		t.Fatalf("usage 帧转发数 = %d，期望 1", forwardedUsage)
	}
	if !res.UsageSeen || res.TotalTokens != 3 {
		t.Fatalf("上游 usage 仍要按原口径取用：%+v", res)
	}
}

func TestResolveStreamUsagePrefersUpstreamUsage(t *testing.T) {
	res := &ChatResult{
		UsageSeen: true, PromptTokens: 3, CompletionTokens: 5, CachedTokens: 2,
		Content: strings.Repeat("中", 100),
	}
	got := ResolveStreamUsage(res, EncodingTokenzR, strings.Repeat("中", 100), "", false)
	if got.Source != TokensSourceUsage || got.PromptTokens != 3 || got.CompletionTokens != 5 || got.TotalTokens != 8 || got.CachedTokens != 2 {
		t.Fatalf("上游 usage 应优先：%+v", got)
	}
}

func TestResolveStreamUsageEstimatesWhenMissingOrZero(t *testing.T) {
	prompt := strings.Repeat("中", 10) // o200k = 8
	completion := strings.Repeat("中", 10)

	missing := ResolveStreamUsage(&ChatResult{Content: completion}, EncodingTokenzR, prompt, "", false)
	if missing.Source != TokensSourceEstimate || missing.TotalTokens != 16 {
		t.Fatalf("无 usage 应估算：%+v", missing)
	}

	zero := ResolveStreamUsage(&ChatResult{UsageSeen: true, Content: completion}, EncodingTokenzR, prompt, "", false)
	if zero.Source != TokensSourceEstimate || zero.TotalTokens != 16 {
		t.Fatalf("usage 全 0 应估算：%+v", zero)
	}

	noEncoding := ResolveStreamUsage(&ChatResult{Content: completion}, "", prompt, "", false)
	if noEncoding.Source != TokensSourceNone || noEncoding.TotalTokens != 0 {
		t.Fatalf("encoding='' 不估算：%+v", noEncoding)
	}

	media := ResolveStreamUsage(&ChatResult{Content: completion}, EncodingTokenzR, prompt, "", true)
	if media.Source != TokensSourceNone || media.TotalTokens != 0 {
		t.Fatalf("多模态不估算：%+v", media)
	}
}

func TestSSEWriterFlushAndHeaders(t *testing.T) {
	rec := httptest.NewRecorder()
	PrepareSSEHeaders(rec.Header())
	w := NewSSEWriter(rec)
	if err := w.Data([]byte(`{"x":1}`)); err != nil {
		t.Fatal(err)
	}
	if err := w.Done(); err != nil {
		t.Fatal(err)
	}
	if got := rec.Header().Get("Content-Type"); got != "text/event-stream; charset=utf-8" {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := rec.Header().Get("X-Accel-Buffering"); got != "no" {
		t.Fatalf("X-Accel-Buffering = %q", got)
	}
	if got := rec.Body.String(); got != "data: {\"x\":1}\n\ndata: [DONE]\n\n" {
		t.Fatalf("SSE 输出 = %q", got)
	}
	if !rec.Flushed {
		t.Fatal("每帧后必须 Flush")
	}
}

func TestOpenAIDriverChatStreamForwardsFramesAndUsage(t *testing.T) {
	var requestBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("路径 = %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Errorf("请求体解析失败：%v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		frames := []string{
			`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"content":"你"}}]}`,
			`{"id":"c1","choices":[{"index":0,"delta":{"content":"好"},"finish_reason":"stop"}]}`,
			`{"id":"c1","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":2}}}`,
			`[DONE]`,
		}
		for _, f := range frames {
			fmt.Fprintf(w, "data: %s\n\n", f)
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	defer server.Close()

	d := NewOpenAIDriver(true)
	var forwarded []string
	res, err := d.ChatStream(context.Background(), Channel{
		ID: 1, BaseURL: server.URL, APIKey: "test-key", Model: "upstream-model", Timeout: 5,
	}, ChatRequest{
		Model:    "logical-model",
		Messages: []Message{{Role: "user", Content: "hi"}},
	}, func(data []byte) error {
		forwarded = append(forwarded, string(data))
		return nil
	})
	if err != nil {
		t.Fatalf("ChatStream 错误：%v", err)
	}
	if res.Content != "你好" || res.FinishReason != "stop" {
		t.Fatalf("聚合内容错误：%+v", res)
	}
	if res.Chunks != 3 || !res.UsageSeen || res.PromptTokens != 3 || res.CompletionTokens != 5 || res.CachedTokens != 2 {
		t.Fatalf("聚合 usage 错误：%+v", res)
	}
	if len(forwarded) != 3 || !strings.Contains(forwarded[2], "usage") {
		t.Fatalf("转发帧 = %+v", forwarded)
	}
	if requestBody["model"] != "upstream-model" {
		t.Fatalf("请求体 model = %v，期望渠道上游模型名", requestBody["model"])
	}
	if requestBody["stream"] != true {
		t.Fatalf("请求体 stream = %v", requestBody["stream"])
	}
	opts, _ := requestBody["stream_options"].(map[string]any)
	if opts["include_usage"] != true {
		t.Fatalf("stream_options = %+v", requestBody["stream_options"])
	}
}

func TestOpenAIDriverChatStreamClientDisconnectKeepsPartialUsage(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		fmt.Fprintf(w, "data: %s\n\n", `{"id":"c1","choices":[{"index":0,"delta":{"content":"partial"}}],"usage":{"prompt_tokens":2,"completion_tokens":1}}`)
		if flusher != nil {
			flusher.Flush()
		}
		// 客户端断开后继续写会失败；忽略即可，不要让 handler 阻塞测试收尾。
		fmt.Fprintf(w, "data: %s\n\n", `{"id":"c1","choices":[{"index":0,"delta":{"content":"more"}}]}`)
		if flusher != nil {
			flusher.Flush()
		}
		close(release)
	}))
	defer server.Close()

	d := NewOpenAIDriver(true)
	res, err := d.ChatStream(context.Background(), Channel{
		ID: 1, BaseURL: server.URL, APIKey: "test-key", Timeout: 5,
	}, ChatRequest{Messages: []Message{{Role: "user", Content: "hi"}}}, func([]byte) error {
		return errors.New("client gone")
	})
	if !errors.Is(err, ErrClientClosed) {
		t.Fatalf("客户端断开错误 = %v，期望 ErrClientClosed", err)
	}
	if res == nil || res.Content != "partial" {
		t.Fatalf("已产生内容未保留：%+v", res)
	}
	if !res.UsageSeen || res.PromptTokens != 2 || res.CompletionTokens != 1 {
		t.Fatalf("已见 usage 未保留：%+v", res)
	}
	<-release
}

func TestOpenAIDriverChatResponseNormalization(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantErr string
	}{
		{"缺 choices", `{"id":"x"}`, "choices"},
		{"空回复", `{"choices":[{"message":{"content":""}}]}`, "空回复"},
		{"正常", `{"id":"x","model":"m","choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2}}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			d := NewOpenAIDriver(true)
			res, err := d.Chat(context.Background(), Channel{BaseURL: server.URL, APIKey: "k", Timeout: 5}, ChatRequest{Messages: []Message{{Role: "user", Content: "hi"}}})
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("错误 = %v，期望包含 %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if res.Content != "ok" || res.TotalTokens != 3 || !res.UsageSeen {
				t.Fatalf("归一结果错误：%+v", res)
			}
		})
	}
}

func TestOpenAIDriverHTTPErrorClassification(t *testing.T) {
	cases := []struct {
		status      int
		wantClass   string
		wantTimeout bool
	}{
		{http.StatusUnauthorized, ErrorClassAuth, false},
		{http.StatusForbidden, ErrorClassAuth, false},
		{http.StatusTooManyRequests, ErrorClassRateLimit, false},
		{http.StatusInternalServerError, ErrorClassUpstream, false},
		{http.StatusGatewayTimeout, ErrorClassUpstream, true},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("HTTP %d", tc.status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				fmt.Fprint(w, `{"error":{"message":"upstream says no","type":"some_error"}}`)
			}))
			defer server.Close()
			d := NewOpenAIDriver(true)
			_, err := d.Chat(context.Background(), Channel{BaseURL: server.URL, APIKey: "k", Timeout: 5}, ChatRequest{Messages: []Message{{Role: "user", Content: "hi"}}})
			var ue *UpstreamError
			if !errors.As(err, &ue) {
				t.Fatalf("错误类型 = %T，期望 *UpstreamError", err)
			}
			if ue.Class != tc.wantClass || ue.HTTPStatus != tc.status {
				t.Fatalf("分类 = %+v", ue)
			}
			if got := IsTimeoutError(err); got != tc.wantTimeout {
				t.Fatalf("IsTimeoutError = %v，期望 %v", got, tc.wantTimeout)
			}
			if IsRetryable(err) {
				t.Fatal("已发出请求的 HTTP 错误不可重试")
			}
			if strings.Contains(ue.Message, "k") && strings.Contains(ue.Message, "Bearer") {
				t.Fatal("错误文案不得包含凭证")
			}
		})
	}
}

func TestClassifyError(t *testing.T) {
	info := ClassifyError(&UpstreamError{HTTPStatus: http.StatusTooManyRequests, Class: ErrorClassRateLimit, Code: "rate", Message: "slow down"})
	if info.Class != ErrorClassRateLimit || info.Status != UsageStatusFail {
		t.Fatalf("429 分类 = %+v", info)
	}
	info = ClassifyError(&UpstreamError{HTTPStatus: http.StatusGatewayTimeout, Class: ErrorClassUpstream, Code: "timeout", Message: "timeout"})
	if info.Status != UsageStatusTimeout {
		t.Fatalf("504 应判 timeout：%+v", info)
	}
	info = ClassifyError(context.DeadlineExceeded)
	if info.Status != UsageStatusTimeout || info.Class != ErrorClassTransport {
		t.Fatalf("context deadline 分类 = %+v", info)
	}
	info = ClassifyError(context.Canceled)
	if info.Status != UsageStatusFail || info.Class != ErrorClassTransport {
		t.Fatalf("context canceled 分类 = %+v", info)
	}
	info = ClassifyError(&APIError{Code: "bad", Message: "bad local", Class: ErrorClassLocal})
	if info.Class != ErrorClassLocal || info.Code != "bad" {
		t.Fatalf("local 分类 = %+v", info)
	}
	info = ClassifyError(&StreamAbortedError{Err: errors.New("broken pipe")})
	if info.Class != ErrorClassTransport || info.Code != "client_closed" || info.Status != UsageStatusFail {
		t.Fatalf("客户端断开分类 = %+v", info)
	}
}

func TestOpenAIDriverEmbeddingsSortsAndSumsUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embeddings" {
			t.Errorf("路径 = %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"model":"m","data":[{"index":1,"object":"embedding","embedding":[1,2]},{"index":0,"object":"embedding","embedding":[3,4]}],"usage":{"prompt_tokens":7,"total_tokens":7}}`)
	}))
	defer server.Close()

	d := NewOpenAIDriver(true)
	res, err := d.Embeddings(context.Background(), Channel{BaseURL: server.URL, APIKey: "k", Timeout: 5},
		EmbeddingRequest{Input: []string{"a", "b"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Data) != 2 {
		t.Fatalf("条数 = %d", len(res.Data))
	}
	if res.Data[0].Index != 0 || res.Data[0].Vector[0] != 3 || res.Data[1].Index != 1 || res.Data[1].Vector[0] != 1 {
		t.Fatalf("未按 index 排序：%+v", res.Data)
	}
	if !res.UsageSeen || res.PromptTokens != 7 || res.TotalTokens != 7 {
		t.Fatalf("usage = %+v", res)
	}
}

func TestOpenAIDriverEmbeddingsNoRetryAfterBad2xxBody(t *testing.T) {
	// 2xx（上游已受理、可能已计费）之后才发现的坏体**不得**标记可重试：重发没有幂等键、
	// 对上游不可区分，重试只会让上游账单可能大于本地记录（本地每个候选尝试只落 1 行 usage）。
	// 用生产组合 retryCall（service.callEmbedding 的同一条路径）断言「上游只收到 1 次请求」。
	cases := []struct {
		name string
		body string
	}{
		{"缺 data", `{"model":"m"}`},
		{"条数不一致", `{"model":"m","data":[{"index":0,"embedding":[1]}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var requests int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				atomic.AddInt32(&requests, 1)
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			d := NewOpenAIDriver(true)
			_, err := retryCall(context.Background(), "embedding", func() (*EmbeddingResult, error) {
				return d.Embeddings(context.Background(),
					Channel{BaseURL: server.URL, APIKey: "k", Timeout: 5},
					EmbeddingRequest{Input: []string{"a", "b"}})
			})
			if err == nil || IsRetryable(err) {
				t.Fatalf("2xx 坏体必须按不可重试的无效响应处理，实际 %v", err)
			}
			var ue *UpstreamError
			if !errors.As(err, &ue) || ue.Code != "invalid_response" {
				t.Fatalf("错误码应为 invalid_response，实际 %v", err)
			}
			if got := atomic.LoadInt32(&requests); got != 1 {
				t.Fatalf("上游请求次数 = %d，期望 1（重试会重复计费）", got)
			}
		})
	}
}

func TestEmbeddingInputCount(t *testing.T) {
	cases := []struct {
		in   any
		want int
	}{
		{"a", 1},
		{[]string{"a", "b"}, 2},
		{[]any{"a", "b", "c"}, 3},
		{nil, 0},
	}
	for _, tc := range cases {
		if got := EmbeddingInputCount(tc.in); got != tc.want {
			t.Fatalf("EmbeddingInputCount(%#v) = %d，期望 %d", tc.in, got, tc.want)
		}
	}
}

func TestValidateBaseURL(t *testing.T) {
	for _, ok := range []string{"http://example.com", "https://example.com/v1"} {
		if err := ValidateBaseURL(ok); err != nil {
			t.Fatalf("%q 应合法：%v", ok, err)
		}
	}
	for _, bad := range []string{
		"",
		"ftp://example.com",
		"example.com",
		"https://user:pass@example.com",
		"https://example.com/v1?x=1",
		"https://example.com/v1#frag",
	} {
		if err := ValidateBaseURL(bad); err == nil {
			t.Fatalf("%q 应被拒", bad)
		}
	}
}
