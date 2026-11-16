// plugin: ai v0.2.0
// redact_test.go 覆盖脱敏唯一实现（redact.go）的四组判据：
//   - 长值（≥6 字符）整串替换，掩码与 maskSecret 同口径；
//   - 短值（<6）按「前后非字母数字边界」判定替换——独立词命中、"xabcx" 类不误伤；
//   - 空串/纯空白候选跳过（未配置密钥）；
//   - 驱动出口单点：伪上游回显渠道 api_key 时，Chat / ChatStream（含流中 error 帧）/ Models（探活）
//     四个出口的错误文案都已脱敏，且失败分类字段与可重试标记不被脱敏破坏。
package ai

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRedactSecretReplacesWholeLongValue(t *testing.T) {
	const key = "sk-live-abcdefgh"
	message := `ERROR: invalid input syntax for type json: "` + key + `" (SQLSTATE 22P02)`
	redacted := redactSecret(message, key)
	if strings.Contains(redacted, key) {
		t.Fatalf("长值必须整串替换：%s", redacted)
	}
	if !strings.Contains(redacted, maskSecret(key)) {
		t.Fatalf("掩码口径须与 maskSecret 一致：%s", redacted)
	}
	// 多次出现同样全部替换。
	repeated := redactSecret(key+" and "+key, "  "+key+"  ")
	if strings.Contains(repeated, key) {
		t.Fatalf("候选两侧空白应先 TrimSpace 再替换：%s", repeated)
	}
}

func TestRedactSecretReplacesShortValueOnWordBoundary(t *testing.T) {
	const short = "abc"
	mask := maskSecret(short)

	if got := redactSecret("密钥 "+short+" 无效", short); got != "密钥 "+mask+" 无效" {
		t.Fatalf("独立短词必须替换：%q", got)
	}
	if got := redactSecret(short+"-key", short); got != mask+"-key" {
		t.Fatalf("行首短词必须替换：%q", got)
	}
	if got := redactSecret("value:"+short, short); got != "value:"+mask {
		t.Fatalf("非字母数字（冒号）后必须替换：%q", got)
	}
	// 嵌在更长词里：不替换（避免误伤正常文案）。
	for _, message := range []string{"xabcx", "abc9", "9abc", "abcabc"} {
		if got := redactSecret(message, short); got != message {
			t.Fatalf("%q 里的片段不得替换，实际 %q", message, got)
		}
	}
	// 中文夹持按「字母/数字」判定：中文属于词内字符 ⇒ 不替换（同一串文案）。
	if got := redactSecret("密钥abc密钥", short); got != "密钥abc密钥" {
		t.Fatalf("中文夹持的短词不得替换：%q", got)
	}
}

func TestRedactSecretSkipsEmptyAndBlankCandidates(t *testing.T) {
	message := "no secret here"
	for _, candidate := range []string{"", "   ", "\t", "\n"} {
		if got := redactSecret(message, candidate); got != message {
			t.Fatalf("候选 %q 应跳过，实际 %q", candidate, got)
		}
	}
	// 单字符候选：非空即参与替换（边界判定）。
	if got := redactSecret("a", "a"); got != "*" {
		t.Fatalf("单字符候选应替换为掩码，实际 %q", got)
	}
}

func TestRedactSecretHandlesMultipleCandidatesAndMaskedText(t *testing.T) {
	// 掩码本身不得再被当作候选误替换（幂等）。
	long := "sk-live-abcdefgh"
	once := redactSecret("key="+long+" other="+long, long)
	twice := redactSecret(once, long)
	if once != twice {
		t.Fatalf("脱敏应幂等：%q != %q", once, twice)
	}
	if got := redactSecret("a="+long+" b=shortkey", long, "shortkey"); strings.Contains(got, long) || strings.Contains(got, "shortkey") {
		t.Fatalf("多候选应全部替换：%q", got)
	}
}

func TestRedactUpstreamErrKeepsClassificationFields(t *testing.T) {
	original := &UpstreamError{
		HTTPStatus: http.StatusUnauthorized,
		Class:      ErrorClassAuth,
		Code:       "invalid_api_key",
		Message:    `上游说: api key sk-live-abcdefgh 无效`,
		Retryable:  false,
	}
	redacted := redactUpstreamErr(original, "sk-live-abcdefgh")
	var upstream *UpstreamError
	if !errors.As(redacted, &upstream) {
		t.Fatalf("脱敏后仍须是 *UpstreamError，实际 %T", redacted)
	}
	if strings.Contains(upstream.Message, "sk-live-abcdefgh") {
		t.Fatalf("文案未脱敏：%s", upstream.Message)
	}
	if upstream.HTTPStatus != http.StatusUnauthorized || upstream.Class != ErrorClassAuth ||
		upstream.Code != "invalid_api_key" || upstream.Retryable != original.Retryable {
		t.Fatalf("分类字段不得被脱敏破坏：%+v", upstream)
	}
	// 未命中密钥时保持原对象（不无谓克隆，也不改类型）。
	clean := &UpstreamError{Class: ErrorClassUpstream, Code: "c", Message: "上游 500"}
	if got := redactUpstreamErr(clean, "sk-live-abcdefgh"); got != error(clean) {
		t.Fatalf("未命中不应替换对象：%v", got)
	}
	// 未配密钥（空）时原样返回。
	if got := redactUpstreamErr(clean, "   "); got != error(clean) {
		t.Fatal("空密钥不得参与脱敏")
	}
	// 非 *UpstreamError（客户端断开）保持身份，errors.Is 判定不受影响。
	aborted := &StreamAbortedError{Err: errors.New("broken pipe")}
	if got := redactUpstreamErr(aborted, "sk-live-abcdefgh"); got != error(aborted) {
		t.Fatal("客户端断开错误必须原样返回")
	}
	if !errors.Is(redactUpstreamErr(aborted, "k"), ErrClientClosed) {
		t.Fatal("脱敏不得破坏 errors.Is(ErrClientClosed)")
	}
}

// TestPublicChatFailureRedactsChannelKeyInUsageRow 断言「落库文案」出口：
// 伪上游在错误体里回显渠道 api_key ⇒ usage 行的 error_message（service.go 的 ClassifyError 出口，
// 与渠道 last_error、用量页展示、探活回执共用同一份判决文案）必须无明文，回执也不得带明文。
//
// 用包内既有的无库脚手架（handler_test.go 的 engineDeps / newPublicEngine / doRequest
// 与 fakeRepository），与公开面其他端到端用例走同一条链路。
func TestPublicChatFailureRedactsChannelKeyInUsageRow(t *testing.T) {
	const channelKey = "sk-chan-abcdefgh"
	const gatewayKey = "sk-gw-0123456789"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprintf(w, `{"error":{"message":"invalid api key %s","type":"invalid_api_key"}}`, channelKey)
	}))
	defer server.Close()

	repo := &fakeRepository{
		model: Model{
			ID: 7, Name: "gpt-test", Type: ModelTypeChat, Status: StatusEnabled,
			Strategy: StrategyPriority, Encoding: EncodingNone,
			PriceInput: "0.000000", PriceOutput: "0.000000",
		},
		candidates: []Candidate{{
			ChannelID: 11, Name: "主渠道", Vendor: "openai", BaseURL: server.URL,
			APIKey: channelKey, Model: "gpt-test", Timeout: 5,
			ChannelStatus: StatusEnabled, Circuit: CircuitClosed,
			ModelID: 7, BindingPriority: 1, BindingWeight: 1, BindingStatus: StatusEnabled,
		}},
	}
	engine := newPublicEngine(engineDeps(repo, NewOpenAIDriver(true), gatewayKey))
	recorder := doRequest(engine, http.MethodPost, "/ai/v1/chat/completions",
		`{"model":"gpt-test","messages":[{"role":"user","content":"hi"}]}`,
		map[string]string{"Authorization": "Bearer " + gatewayKey})

	if len(repo.usages) == 0 {
		t.Fatalf("上游失败也必须落一行 usage（HTTP %d）", recorder.Code)
	}
	for i, usage := range repo.usages {
		if strings.Contains(usage.ErrorMessage, channelKey) {
			t.Fatalf("usage[%d].error_message 泄漏渠道密钥明文：%s", i, usage.ErrorMessage)
		}
		if !strings.Contains(usage.ErrorMessage, maskSecret(channelKey)) {
			t.Fatalf("usage[%d].error_message 应经同一脱敏出口：%s", i, usage.ErrorMessage)
		}
	}
	if strings.Contains(recorder.Body.String(), channelKey) {
		t.Fatalf("回执泄漏渠道密钥明文：%s", recorder.Body.String())
	}
}

// assertNoPlainKey 断言错误文案里没有密钥明文，且分类字段完整。
func assertNoPlainKey(t *testing.T, label string, err error, key string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s：应返回错误", label)
	}
	if strings.Contains(err.Error(), key) {
		t.Fatalf("%s：错误文案仍含密钥明文：%s", label, err.Error())
	}
	if !strings.Contains(err.Error(), maskSecret(key)) {
		t.Fatalf("%s：应替换成 mask_secret 掩码，实际 %s", label, err.Error())
	}
	var upstream *UpstreamError
	if !errors.As(err, &upstream) {
		t.Fatalf("%s：错误类型 = %T，期望 *UpstreamError", label, err)
	}
}

// TestOpenAIDriverRedactsKeyEchoedByUpstream 覆盖「驱动出口脱敏」的四个方法：
// Chat / ChatStream（HTTP 错误与流中 error 帧两条路径）/ Models（探活复用本方法）。
//
// 这三个出口就是 usage.error_message、渠道 last_error、探活落库与回执的**唯一来源**
// （service.go:739/825/907 的 ClassifyError(err).Message、handler_channel.go 探活的 probeErr.Error()）。
func TestOpenAIDriverRedactsKeyEchoedByUpstream(t *testing.T) {
	const key = "sk-chan-abcdefgh"
	ctx := context.Background()

	t.Run("HTTP 错误（Chat/ChatStream/Models）", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprintf(w, `{"error":{"message":"invalid api key %s for %s","type":"invalid_api_key"}}`,
				key, r.URL.Path)
		}))
		defer server.Close()

		driver := NewOpenAIDriver(true)
		channel := Channel{ID: 1, BaseURL: server.URL, APIKey: key, Model: "m", Timeout: 5}

		_, chatErr := driver.Chat(ctx, channel, ChatRequest{Messages: []Message{{Role: "user", Content: "hi"}}})
		assertNoPlainKey(t, "Chat", chatErr, key)
		var upstream *UpstreamError
		if errors.As(chatErr, &upstream) && (upstream.Class != ErrorClassAuth || upstream.HTTPStatus != http.StatusUnauthorized) {
			t.Fatalf("脱敏不得改动分类字段：%+v", upstream)
		}

		_, modelsErr := driver.Models(ctx, channel)
		assertNoPlainKey(t, "Models（探活复用）", modelsErr, key)

		_, streamErr := driver.ChatStream(ctx, channel,
			ChatRequest{Messages: []Message{{Role: "user", Content: "hi"}}}, func([]byte) error { return nil })
		assertNoPlainKey(t, "ChatStream", streamErr, key)
	})

	t.Run("流中 error 帧（ChatStream）", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "data: {\"error\":{\"message\":\"quota check failed: %s\",\"type\":\"invalid_api_key\"}}\n\n", key)
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
		}))
		defer server.Close()

		driver := NewOpenAIDriver(true)
		_, err := driver.ChatStream(ctx, Channel{ID: 1, BaseURL: server.URL, APIKey: key, Model: "m", Timeout: 5},
			ChatRequest{Messages: []Message{{Role: "user", Content: "hi"}}}, func([]byte) error { return nil })
		assertNoPlainKey(t, "ChatStream（流内 error 帧）", err, key)
	})
}
