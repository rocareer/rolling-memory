package ws

import (
	"context"
	"encoding/json"
)

// AIMessage AI 对话上下文中的单条消息
type AIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// AIRequest AI 对话请求（由引擎从 ai/chat 消息中解析）
type AIRequest struct {
	ID       string          // 本次对话的消息 ID（流式事件的 reply_to）
	Identity Identity        // 发起者身份
	Agent    string          // agent 稳定标识（必填；模型/prompt 由 agent 定义）
	Messages []AIMessage     // 对话上下文（最后一条须为 user）
	Raw      json.RawMessage // 原始 data，实现方可读取额外字段
}

// AIChunk AI 流式增量（文本或思考过程）
type AIChunk struct {
	Delta     string `json:"delta,omitempty"`
	Reasoning string `json:"reasoning,omitempty"`
}

// AIResult AI 对话完成后的元数据
type AIResult struct {
	Usage json.RawMessage `json:"usage,omitempty"`
}

// AIProvider AI 生成实现。引擎负责协议与连接，实现方只负责产出增量。
// emit 返回非 nil 表示下游连接已不可写，实现方应尽快停止生成。
type AIProvider interface {
	Stream(ctx context.Context, req AIRequest, emit func(AIChunk) error) (*AIResult, error)
}

// AIProviderFunc 函数式 AIProvider
type AIProviderFunc func(ctx context.Context, req AIRequest, emit func(AIChunk) error) (*AIResult, error)

// Stream 实现 AIProvider
func (f AIProviderFunc) Stream(ctx context.Context, req AIRequest, emit func(AIChunk) error) (*AIResult, error) {
	return f(ctx, req, emit)
}

// aiChatData ai/chat 消息 data 的标准形态
type aiChatData struct {
	Agent    string      `json:"agent"`
	Messages []AIMessage `json:"messages"`
}
