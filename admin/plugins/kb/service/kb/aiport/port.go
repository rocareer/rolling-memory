// plugin: kb v1.0.0
// Package aiport 知识库 AI 能力端口（向量嵌入 / 候选生成）。
//
// 定位：端口随插件携带（实现方经 Register* 注入自有 Provider；默认实现文件可被下游部署物理排除）：
//   - 本插件只交付默认实现 openai-compatible（provider_openai.go：读宿主 configs 的 ai 组直连上游）；
//   - 下游部署经 RegisterEmbedder / RegisterChatter 注册自有实现（如接其受治理模型运行时——
//     agent/egress/成本口径全在产品侧），框架不 import 产品代码；
//   - 默认实现文件可被下游部署物理排除：其注册自包含于
//     该文件的 init()，未排除文件（本文件等）一律不得引用其符号——排除后仍可编译；
//   - 配置指向的 Provider 未注册时 **fail-closed**：返回响亮错误，绝不回落到默认直连实现。
//
// CallMeta 是中性调用元数据：框架默认实现只读 Kind/Source 用于观测；产品适配器据此补其治理语义
// （Face/Scope → 出站脱敏 egress 声明；Agent → ai agent 绑定）。Face 为空 = 不声明（调用面与
// 产品侧「查询嵌入不做出站声明」的既有口径对齐）。
package aiport

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/ai-go-hub/ai-go-admin/plugins/kb/kbcfg"
)

// ProviderOpenAICompatible 框架默认 Provider 名（与 configs/配置缺省取值一致）
const ProviderOpenAICompatible = "openai-compatible"

// ==================== 调用元数据 ====================

// CallMeta 一次调用的元数据（产品适配器据此补治理语义；框架默认实现忽略除 Kind/Source 外字段）
type CallMeta struct {
	Face   string // 面名：kb.embed / kb.ingest / kb.tag……；空 = 不做出站声明
	Scope  string // 资源作用域（kb:library:<id>；空 = 不设）
	Kind   string // 调用面细分：document / query / sentence……
	Agent  string // 调用档名（产品适配器映射到其 ai agent；框架默认实现忽略）
	Source string // 调用来源（缺省 kb）
}

// Normalize 归一（Source 缺省 kb；不改 Face/Scope 的空语义）
func (m CallMeta) Normalize() CallMeta {
	if strings.TrimSpace(m.Source) == "" {
		m.Source = "kb"
	}
	return m
}

// ==================== 嵌入端口 ====================

// EmbedResult 一次嵌入调用结果
type EmbedResult struct {
	Vectors       [][]float32 // 与入参 Inputs 顺序对应
	ModelIdentity string      // 调用回显的模型身份（与 Identity() 漂移 = 调用方响亮失败）
	Model         string      // 路由模型读数（记账/观测）
	Channel       string      // 渠道读数（观测）
}

// Embedder 向量嵌入端口
type Embedder interface {
	// Name Provider 标识（与配置 kb.embed.provider 取值一致）
	Name() string
	// Identity 当前向量空间身份（模型标识；空 = 未就绪，调用方必须 fail-closed：
	// 禁以空身份落向量行/发查询——等值空串会让无身份行混入）
	Identity(ctx context.Context) (string, error)
	// AnchorIdentity 语义切分锚身份（入 chunker_version：换实现/换模型/换版本 ⇒ 新 build_hash ⇒ 重产）；
	// 框架默认实现 = <Provider 名>@<模型身份>；产品适配器可返回其 agent 名@版本（沿用产品既有锚口径）
	AnchorIdentity(ctx context.Context) (string, error)
	// Embed 批量嵌入（保持 Inputs 顺序；实现须在返回前校验条数与维度，
	// 并在结果内回显 ModelIdentity 供调用方核对漂移）
	Embed(ctx context.Context, meta CallMeta, inputs []string) (*EmbedResult, error)
}

// ==================== 候选生成（对话）端口 ====================

// ChatResult 一次非流式对话调用结果
type ChatResult struct {
	Content string // 模型输出正文（候选生成契约 = JSON 数组文本）
	Model   string // 路由模型读数
}

// Chatter 非流式对话端口（kb:tag 候选生成）
type Chatter interface {
	// Name Provider 标识（与配置 kb.tag.provider 取值一致）
	Name() string
	// Chat 发起一次非流式生成（input 由调用方装配；vars 供实现映射模板变量）
	Chat(ctx context.Context, meta CallMeta, input string, vars map[string]any) (*ChatResult, error)
}

// ==================== 注册表 ====================

// EmbedderFactory 构造 Embedder 实例（注册时提供；每次调用返回可用实例，允许返回同一实例）
type EmbedderFactory func() Embedder

// ChatterFactory 构造 Chatter 实例
type ChatterFactory func() Chatter

var (
	mu        sync.RWMutex
	embedders = map[string]EmbedderFactory{}
	chatters  = map[string]ChatterFactory{}
)

// RegisterEmbedder 注册嵌入 Provider（进程内注册表；同名重复注册覆盖——产品可用它替换默认实现）。
// 空名或空工厂属于编程错误（注册发生在 init 期），直接 panic 暴露。
func RegisterEmbedder(name string, factory EmbedderFactory) {
	name = strings.TrimSpace(name)
	if name == "" {
		panic("aiport: RegisterEmbedder 需要非空 Provider 名称")
	}
	if factory == nil {
		panic("aiport: RegisterEmbedder 需要非空工厂函数: " + name)
	}
	mu.Lock()
	defer mu.Unlock()
	embedders[name] = factory
}

// RegisterChatter 注册对话 Provider（语义同 RegisterEmbedder）
func RegisterChatter(name string, factory ChatterFactory) {
	name = strings.TrimSpace(name)
	if name == "" {
		panic("aiport: RegisterChatter 需要非空 Provider 名称")
	}
	if factory == nil {
		panic("aiport: RegisterChatter 需要非空工厂函数: " + name)
	}
	mu.Lock()
	defer mu.Unlock()
	chatters[name] = factory
}

// EmbedderName 配置指定的嵌入 Provider 名（缺省 openai-compatible）
func EmbedderName() string {
	name := strings.TrimSpace(kbcfg.Get().Embed.Provider)
	if name == "" {
		name = ProviderOpenAICompatible
	}
	return name
}

// ChatterName 配置指定的对话 Provider 名（缺省 openai-compatible）
func ChatterName() string {
	name := strings.TrimSpace(kbcfg.Get().Tag.Provider)
	if name == "" {
		name = ProviderOpenAICompatible
	}
	return name
}

// LookupEmbedder 按名取嵌入 Provider 实例；未注册返回 (nil, false)。
// 调用方必须 fail-closed：把未注册映射为响亮错误，绝不回退到其他实现。
func LookupEmbedder(name string) (Embedder, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, false
	}
	mu.RLock()
	factory, ok := embedders[name]
	mu.RUnlock()
	if !ok {
		return nil, false
	}
	provider := factory()
	if provider == nil {
		return nil, false
	}
	return provider, true
}

// LookupChatter 按名取对话 Provider 实例（语义同 LookupEmbedder）
func LookupChatter(name string) (Chatter, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, false
	}
	mu.RLock()
	factory, ok := chatters[name]
	mu.RUnlock()
	if !ok {
		return nil, false
	}
	provider := factory()
	if provider == nil {
		return nil, false
	}
	return provider, true
}

// RegisteredEmbedders 已注册嵌入 Provider 名单（字典序；仅用于诊断与错误消息）
func RegisteredEmbedders() []string { return registered(embedders) }

// RegisteredChatters 已注册对话 Provider 名单（字典序）
func RegisteredChatters() []string { return registered(chatters) }

func registered[T any](m map[string]T) []string {
	mu.RLock()
	defer mu.RUnlock()
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// CurrentEmbedder 取配置指定的嵌入 Provider；未注册/空实现 ⇒ 响亮错误（fail-closed，不回落）
func CurrentEmbedder() (Embedder, error) {
	name := EmbedderName()
	if provider, ok := LookupEmbedder(name); ok {
		return provider, nil
	}
	return nil, UnknownProviderError("嵌入", name, RegisteredEmbedders())
}

// CurrentChatter 取配置指定的对话 Provider；未注册/空实现 ⇒ 响亮错误（fail-closed，不回落）
func CurrentChatter() (Chatter, error) {
	name := ChatterName()
	if provider, ok := LookupChatter(name); ok {
		return provider, nil
	}
	return nil, UnknownProviderError("对话", name, RegisteredChatters())
}

// UnknownProviderError 构造「配置指向未注册 Provider」的 fail-closed 错误（消息含已注册名单）
func UnknownProviderError(face, name string, registered []string) error {
	hint := "未注册任何 Provider"
	if len(registered) > 0 {
		hint = "已注册: " + strings.Join(registered, ", ")
	}
	return fmt.Errorf("kb: %s Provider %q 未注册（%s）；已 fail-closed，不回落到默认实现", face, name, hint)
}
