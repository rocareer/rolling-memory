// plugin: kb v1.0.0
// 默认 Provider（openai-compatible）的黑盒契约用例（不下库、不触网：伪上游用 httptest）。
//
// 纪律（D1）：本文件引用 provider_openai.go 的实现符号
// （类型/私有注入缝），因此**随默认实现一并被下游部署物理排除**：
//   - 排除默认实现后本文件不存在 ⇒ 编译无碍；
//   - 两仓共享的注册表断言在 port_test.go（不引用实现符号）。
package aiport

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDefaultEmbedderIdentityFailClosed(t *testing.T) {
	// 未配置 kb.embed.model（注入零值配置）⇒ 身份不可用，响亮报错（禁空身份落行）
	p := &openAICompatibleEmbedder{settings: func(context.Context) (*upstreamSettings, error) {
		return &upstreamSettings{}, nil
	}}
	if _, err := p.Identity(context.Background()); err == nil {
		t.Fatal("模型未配置时 Identity 应报错")
	}
	if _, err := p.Embed(context.Background(), CallMeta{}, []string{"x"}); err == nil {
		t.Fatal("模型未配置时 Embed 应报错")
	}
}

func TestDefaultEmbedderUpstreamContract(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("上游 Authorization 头不符: %q", r.Header.Get("Authorization"))
		}
		var payload struct {
			Model string   `json:"model"`
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("上游请求体解析失败: %v", err)
		}
		if payload.Model != "kb-model" || len(payload.Input) != 2 {
			t.Errorf("上游请求不符: model=%q inputs=%d", payload.Model, len(payload.Input))
		}
		// 乱序返回（index 1 在前）——验证调用方按 index 还原顺序
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"kb-model","data":[` +
			`{"embedding":[0.3,0.3,0.3],"index":1},` +
			`{"embedding":[0.1,0.1,0.1],"index":0}]}`))
	}))
	defer srv.Close()

	p := &openAICompatibleEmbedder{settings: func(context.Context) (*upstreamSettings, error) {
		return &upstreamSettings{APIURL: srv.URL, APIKey: "test-key", AllowPrivateURL: true, EmbedModel: "kb-model", EmbedDim: 3}, nil
	}}
	res, err := p.Embed(context.Background(), CallMeta{Face: "kb.embed", Scope: "kb:library:1", Kind: "document"}, []string{"甲", "乙"})
	if err != nil {
		t.Fatalf("嵌入失败: %v", err)
	}
	if gotPath != "/embeddings" {
		t.Fatalf("上游路径不符: %s", gotPath)
	}
	if res.ModelIdentity != "kb-model" || len(res.Vectors) != 2 {
		t.Fatalf("结果不符: %+v", res)
	}
	// index 还原：第 0 位应是 index=0 的向量（0.1），第 1 位是 index=1（0.3）
	if res.Vectors[0][0] != 0.1 || res.Vectors[1][0] != 0.3 {
		t.Fatalf("批次顺序未按 index 还原: %v", res.Vectors)
	}
	if got, err := p.AnchorIdentity(context.Background()); err != nil || got != ProviderOpenAICompatible+"@kb-model" {
		t.Fatalf("锚身份不符: %q err=%v", got, err)
	}

	// 维度闸：配置 dim=4 而上游回 3 维 ⇒ 响亮失败（列存 halfvec 维度须一致）
	pDim := &openAICompatibleEmbedder{settings: func(context.Context) (*upstreamSettings, error) {
		return &upstreamSettings{APIURL: srv.URL, APIKey: "test-key", AllowPrivateURL: true, EmbedModel: "kb-model", EmbedDim: 4}, nil
	}}
	if _, err := pDim.Embed(context.Background(), CallMeta{}, []string{"甲", "乙"}); err == nil ||
		!strings.Contains(err.Error(), "维度不符") {
		t.Fatalf("维度不符应响亮失败: %v", err)
	}
}

func TestDefaultChatterUpstreamContract(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("上游路径不符: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"chat-model","choices":[{"message":{"content":"[{\"tag\":\"x\",\"confidence\":0.9}]"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	p := &openAICompatibleChatter{settings: func(context.Context) (*upstreamSettings, error) {
		return &upstreamSettings{APIURL: srv.URL, APIKey: "test-key", AllowPrivateURL: true, TagModel: "chat-model"}, nil
	}}
	res, err := p.Chat(context.Background(), CallMeta{Face: "kb.tag", Kind: "document"}, "材料", map[string]any{"kind": "document"})
	if err != nil {
		t.Fatalf("对话调用失败: %v", err)
	}
	if res.Model != "chat-model" || !strings.Contains(res.Content, "confidence") {
		t.Fatalf("对话结果不符: %+v", res)
	}

	// 模型未配置（kb.tag.model 与 ai_default_model 皆空）⇒ 响亮失败
	pNoModel := &openAICompatibleChatter{settings: func(context.Context) (*upstreamSettings, error) {
		return &upstreamSettings{APIURL: srv.URL, APIKey: "test-key", AllowPrivateURL: true}, nil
	}}
	if _, err := pNoModel.Chat(context.Background(), CallMeta{}, "x", nil); err == nil {
		t.Fatal("对话模型未配置应报错")
	}
}
