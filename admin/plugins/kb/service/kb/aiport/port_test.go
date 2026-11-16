// plugin: kb v1.0.0
// 端口面单测（注册表与 fail-closed；两仓共享——不引用任何 Provider 实现符号）。
//
// 与 provider_openai_test.go 的分工（按文件边界排除默认实现）：
//   - 本文件：只经注册表 + 端口接口断言（stub 实现本地定义）；
//   - provider_openai_test.go：默认实现的黑盒契约（含 httptest 伪上游），按 D1 随默认实现一并被
//     下游部署物理排除（按文件边界）——排除后本文件仍须可编译。
package aiport

import (
	"context"
	"strings"
	"testing"
)

// stubEmbedder / stubChatter 假实现（注册表用例）
type stubEmbedder struct{ name string }

func (s *stubEmbedder) Name() string { return s.name }
func (s *stubEmbedder) Identity(context.Context) (string, error) {
	return "stub-model", nil
}
func (s *stubEmbedder) AnchorIdentity(context.Context) (string, error) {
	return s.name + "@stub-model", nil
}
func (s *stubEmbedder) Embed(context.Context, CallMeta, []string) (*EmbedResult, error) {
	return &EmbedResult{ModelIdentity: "stub-model"}, nil
}

type stubChatter struct{}

func (s *stubChatter) Name() string { return "stub-chatter" }
func (s *stubChatter) Chat(context.Context, CallMeta, string, map[string]any) (*ChatResult, error) {
	return &ChatResult{Content: "[]", Model: "stub-model"}, nil
}

func TestRegistrySemantics(t *testing.T) {
	RegisterEmbedder("stub-embedder", func() Embedder { return &stubEmbedder{name: "stub-embedder"} })
	if _, ok := LookupEmbedder("stub-embedder"); !ok {
		t.Fatal("已注册的嵌入 Provider 应可查得")
	}
	if _, ok := LookupEmbedder("no-such-embedder"); ok {
		t.Fatal("未注册名不得查得")
	}
	RegisterChatter("stub-chatter", func() Chatter { return &stubChatter{} })
	if _, ok := LookupChatter("stub-chatter"); !ok {
		t.Fatal("已注册的对话 Provider 应可查得")
	}

	// 空名 / 空工厂属编程错误（init 期暴露）
	if !panics(func() { RegisterEmbedder("", func() Embedder { return &stubEmbedder{} }) }) {
		t.Fatal("空名注册应 panic")
	}
	if !panics(func() { RegisterChatter("x", nil) }) {
		t.Fatal("空工厂注册应 panic")
	}

	// 诊断消息须带已注册名单（排障面）
	err := UnknownProviderError("嵌入", "ghost", RegisteredEmbedders())
	if err == nil || !strings.Contains(err.Error(), "ghost") || !strings.Contains(err.Error(), "已注册") {
		t.Fatalf("未注册错误消息不符（应含名字与已注册名单）: %v", err)
	}
}

// panics 断言函数必然 panic（注册期编程错误暴露面）
func panics(fn func()) (did bool) {
	defer func() {
		if recover() != nil {
			did = true
		}
	}()
	fn()
	return
}
