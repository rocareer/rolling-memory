// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package eval

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rocareer/rolling-memory/internal/config"
)

func testConfig(t *testing.T) *config.Config {
	t.Helper()
	path := t.TempDir() + "/config.toml"
	loaded, err := config.Load(config.LoadOptions{Path: &path, Env: map[string]string{}})
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	return loaded
}

// TestChatClientWireShape 请求体：thinking.type / reasoning_effort 透传，
// max_tokens 0 时不下发，正文为空时报错（推理模型没放开帽子的典型症状）。
func TestChatClientWireShape(t *testing.T) {
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if err := json.NewDecoder(request.Body).Decode(&captured); err != nil {
			t.Errorf("decode body: %v", err)
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"choices": []any{map[string]any{
				"finish_reason": "stop",
				"message":       map[string]any{"role": "assistant", "content": "Sweden"},
			}},
		})
	}))
	defer server.Close()

	client, err := NewChatClient(testConfig(t), ChatOptions{
		BaseURL: server.URL, Model: "deepseek-v4.1-flash", Temperature: 0.2,
		Thinking: true, ReasoningEffort: "low",
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	answer, err := client.Complete(FrozenAnswerSystem, "1. m1 · I moved from Sweden.\n\nWhere from?")
	if err != nil || answer != "Sweden" {
		t.Fatalf("complete = %q err=%v", answer, err)
	}
	if _, capped := captured["max_tokens"]; capped {
		t.Fatalf("max_tokens must be omitted when unset: %v", captured["max_tokens"])
	}
	thinking, _ := captured["thinking"].(map[string]any)
	if thinking["type"] != "enabled" {
		t.Fatalf("thinking = %v", captured["thinking"])
	}
	if captured["reasoning_effort"] != "low" {
		t.Fatalf("reasoning_effort = %v", captured["reasoning_effort"])
	}
	// 冻结协议：system 必须是冻结答题原文。
	messages, _ := captured["messages"].([]any)
	first, _ := messages[0].(map[string]any)
	if first["content"] != FrozenAnswerSystem {
		t.Fatalf("system prompt = %q", first["content"])
	}
}

func TestChatClientEmptyContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"choices": []any{map[string]any{
				"finish_reason": "length",
				"message": map[string]any{
					"role": "assistant", "content": "", "reasoning_content": strings.Repeat("想", 10),
				},
			}},
		})
	}))
	defer server.Close()
	client, err := NewChatClient(testConfig(t), ChatOptions{BaseURL: server.URL, Model: "m", Thinking: true})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if _, err := client.Complete("s", "u"); err == nil || !strings.Contains(err.Error(), "正文为空") {
		t.Fatalf("empty content error = %v", err)
	}
}

// TestNewChatClientRequiresEndpointAndEgress 端点/模型缺失与出网策略都要响亮报错。
func TestNewChatClientRequiresEndpointAndEgress(t *testing.T) {
	cfg := testConfig(t)
	if _, err := NewChatClient(cfg, ChatOptions{Model: "m"}); err == nil {
		t.Fatal("missing base_url must fail")
	}
	if _, err := NewChatClient(cfg, ChatOptions{BaseURL: "https://api.example.com/v1"}); err == nil {
		t.Fatal("missing model must fail")
	}
	if _, err := NewChatClient(cfg, ChatOptions{BaseURL: "https://api.example.com/v1", Model: "m"}); err == nil {
		t.Fatal("local mode must block remote egress")
	}
}

// TestChatClientRetryOnEmptyContent 空正文属瞬时故障：重试后拿到正文即成功；
// 三次都空则上抛原始错误（调用方据此记 unscored，而不是把空答案判 0）。
func TestChatClientRetryOnEmptyContent(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		attempts++
		content := ""
		if attempts >= 2 {
			content = "Sweden"
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"choices": []any{map[string]any{
				"finish_reason": "length",
				"message":       map[string]any{"role": "assistant", "content": content},
			}},
		})
	}))
	defer server.Close()
	client, err := NewChatClient(testConfig(t), ChatOptions{BaseURL: server.URL, Model: "m"})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	answer, err := client.CompleteWithRetry("s", "u")
	if err != nil || answer != "Sweden" {
		t.Fatalf("CompleteWithRetry = %q err=%v", answer, err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
}

// TestChatClientRetryExhaustedIsLoud 重试耗尽必须响亮报错（含空正文标记），不得返回空串 + nil。
func TestChatClientRetryExhaustedIsLoud(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		attempts++
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"choices": []any{map[string]any{
				"finish_reason": "length",
				"message":       map[string]any{"role": "assistant", "content": ""},
			}},
		})
	}))
	defer server.Close()
	client, err := NewChatClient(testConfig(t), ChatOptions{BaseURL: server.URL, Model: "m"})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	answer, err := client.CompleteWithRetry("s", "u")
	if answer != "" || err == nil {
		t.Fatalf("want loud failure, got %q err=%v", answer, err)
	}
	if !errors.Is(err, errChatEmptyContent) {
		t.Fatalf("error must wrap errChatEmptyContent: %v", err)
	}
	if attempts != chatMaxAttempts {
		t.Fatalf("attempts = %d, want %d", attempts, chatMaxAttempts)
	}
}

// TestChatClientThinkingDisabledIsExplicit 关思考必须落到**网宿真正认的** `reasoning_effort=none`：
// 网宿不识别 thinking.type，`thinking` 缺失或为 null 都只算"不表态"⇒ 缺省开思考 ⇒ 正文被推理吃空。
func TestChatClientThinkingDisabledIsExplicit(t *testing.T) {
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if err := json.NewDecoder(request.Body).Decode(&captured); err != nil {
			t.Errorf("decode body: %v", err)
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"choices": []any{map[string]any{
				"finish_reason": "stop",
				"message":       map[string]any{"role": "assistant", "content": "ok"},
			}},
		})
	}))
	defer server.Close()
	client, err := NewChatClient(testConfig(t), ChatOptions{
		BaseURL: server.URL, Model: "m", Thinking: false, ReasoningEffort: "low",
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if _, err := client.Complete("s", "u"); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if captured["reasoning_effort"] != "none" {
		t.Fatalf("reasoning_effort = %v, want none（网宿唯一认可的关闭档）", captured["reasoning_effort"])
	}
	thinking, _ := captured["thinking"].(map[string]any)
	if thinking["type"] != "disabled" {
		t.Fatalf("thinking = %v, want type=disabled", captured["thinking"])
	}
}

// TestChatClientThinkingEnabledSendsEffort 开思考必须带 reasoning_effort（网宿缺省档 high），
// 否则网宿侧同样只算"不表态"。
func TestChatClientThinkingEnabledSendsEffort(t *testing.T) {
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if err := json.NewDecoder(request.Body).Decode(&captured); err != nil {
			t.Errorf("decode body: %v", err)
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"choices": []any{map[string]any{
				"finish_reason": "stop",
				"message":       map[string]any{"role": "assistant", "content": "ok"},
			}},
		})
	}))
	defer server.Close()
	client, err := NewChatClient(testConfig(t), ChatOptions{BaseURL: server.URL, Model: "m", Thinking: true})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if _, err := client.Complete("s", "u"); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if captured["reasoning_effort"] != "high" {
		t.Fatalf("reasoning_effort = %v, want high（未显式指定时的开启档）", captured["reasoning_effort"])
	}
	thinking, _ := captured["thinking"].(map[string]any)
	if thinking["type"] != "enabled" {
		t.Fatalf("thinking = %v, want type=enabled", captured["thinking"])
	}
}

// TestChatClientRetrySkipsContractErrors 契约类错误（HTTP 400）不重试：配置错应立即暴露。
func TestChatClientRetrySkipsContractErrors(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		attempts++
		writer.WriteHeader(http.StatusBadRequest)
		_, _ = writer.Write([]byte(`{"error":"bad request"}`))
	}))
	defer server.Close()
	client, err := NewChatClient(testConfig(t), ChatOptions{BaseURL: server.URL, Model: "m"})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if _, err := client.CompleteWithRetry("s", "u"); err == nil {
		t.Fatal("HTTP 400 must fail")
	}
	if attempts != 1 {
		t.Fatalf("contract errors must not retry: attempts = %d", attempts)
	}
}
