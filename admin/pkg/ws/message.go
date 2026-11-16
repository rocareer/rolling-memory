// Package ws 通用 WebSocket 引擎：服务端 Hub（群聊房间 + 用户直发）与客户端（拨号/重连）共用一套消息协议。
//
// 协议只承载三类业务消息——群聊（group）、AI（ai）、通用（generic），另加系统（system）控制消息；
// 引擎不解释业务载荷（data 原样透传），AI 生成由实现方通过 AIProvider 注入。
package ws

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// MessageType 消息大类
type MessageType string

const (
	TypeGroup   MessageType = "group"   // 群聊：加入/离开房间、房间广播
	TypeAI      MessageType = "ai"      // AI：对话请求、增量、结束、取消
	TypeGeneric MessageType = "generic" // 通用：点对点或房间内任意 JSON 载荷
	TypeSystem  MessageType = "system"  // 系统：欢迎、心跳、错误
)

// Action 消息动作（事件名）
type Action string

const (
	ActionJoin     Action = "join"     // 客户端请求加入房间
	ActionLeave    Action = "leave"    // 客户端请求离开房间
	ActionSend     Action = "send"     // 客户端发送消息
	ActionChat     Action = "chat"     // 客户端发起 AI 对话
	ActionCancel   Action = "cancel"   // 客户端取消 AI 对话
	ActionDelta    Action = "delta"    // 服务端下发 AI 增量
	ActionDone     Action = "done"     // 服务端下发 AI 完成
	ActionMessage  Action = "message"  // 服务端下发业务消息
	ActionJoined   Action = "joined"   // 服务端确认加入房间
	ActionLeft     Action = "left"     // 服务端确认离开房间
	ActionSent     Action = "sent"     // 服务端确认消息已投递
	ActionAccepted Action = "accepted" // 服务端确认 AI 请求已受理
	ActionCanceled Action = "canceled" // AI 对话已取消
	ActionWelcome  Action = "welcome"  // 连接建立后的欢迎消息
	ActionPing     Action = "ping"     // 应用层心跳请求
	ActionPong     Action = "pong"     // 应用层心跳响应
	ActionError    Action = "error"    // 统一错误事件
)

// Message 统一消息信封。
//
// id 为客户端消息 ID（缺省由服务端补），reply_to 指向被响应消息的 id；
// 服务端下发的广播消息会填 from/from_name；data 为不透明 JSON，引擎不解释其结构。
type Message struct {
	ID       string          `json:"id,omitempty"`
	ReplyTo  string          `json:"reply_to,omitempty"`
	Type     MessageType     `json:"type"`
	Action   Action          `json:"action"`
	Room     string          `json:"room,omitempty"`
	To       string          `json:"to,omitempty"`
	From     string          `json:"from,omitempty"`
	FromName string          `json:"from_name,omitempty"`
	Data     json.RawMessage `json:"data,omitempty"`
	TS       int64           `json:"ts,omitempty"`
}

// ErrorData 统一错误载荷
type ErrorData struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// NewMessage 创建一条消息并生成消息 ID
func NewMessage(t MessageType, action Action, data any) (*Message, error) {
	return NewMessageWithID(uuid.NewString(), t, action, data)
}

// NewMessageWithID 以指定消息 ID 创建消息
func NewMessageWithID(id string, t MessageType, action Action, data any) (*Message, error) {
	raw, err := encodeData(data)
	if err != nil {
		return nil, err
	}
	return &Message{
		ID:     id,
		Type:   t,
		Action: action,
		Data:   raw,
		TS:     NowMillis(),
	}, nil
}

// NewError 创建统一错误消息
func NewError(replyTo, code, message string) *Message {
	raw, _ := json.Marshal(ErrorData{Code: code, Message: message})
	return &Message{
		ID:      uuid.NewString(),
		ReplyTo: replyTo,
		Type:    TypeSystem,
		Action:  ActionError,
		Data:    raw,
		TS:      NowMillis(),
	}
}

// DecodeData 将 data 解码到目标结构；data 为空时不做处理
func (m *Message) DecodeData(v any) error {
	if len(m.Data) == 0 {
		return nil
	}
	return json.Unmarshal(m.Data, v)
}

// Validate 校验客户端入站消息的协议形态（服务端下发消息不经过此校验）
func (m *Message) Validate() error {
	if m.Type == "" {
		return errors.New("type 必填")
	}
	if m.Action == "" {
		return errors.New("action 必填")
	}
	if len(m.Data) > 0 && !json.Valid(m.Data) {
		return errors.New("data 必须是合法 JSON")
	}

	switch m.Type {
	case TypeGroup:
		if m.Room == "" {
			return errors.New("group 消息缺少 room")
		}
		switch m.Action {
		case ActionJoin, ActionLeave, ActionSend:
		default:
			return fmt.Errorf("不支持的 group 动作: %s", m.Action)
		}
	case TypeAI:
		switch m.Action {
		case ActionChat, ActionCancel:
		default:
			return fmt.Errorf("不支持的 ai 动作: %s", m.Action)
		}
	case TypeGeneric:
		if m.Action != ActionSend {
			return fmt.Errorf("不支持的 generic 动作: %s", m.Action)
		}
		if m.Room == "" && m.To == "" {
			return errors.New("generic 消息需要 room 或 to")
		}
		if m.Room != "" && m.To != "" {
			return errors.New("generic 消息的 room 与 to 只能二选一")
		}
	case TypeSystem:
		if m.Action != ActionPing {
			return fmt.Errorf("不支持的 system 动作: %s", m.Action)
		}
	default:
		return fmt.Errorf("不支持的消息类型: %s", m.Type)
	}
	return nil
}

// ensureMeta 补齐服务端下发消息的 ID 与时间戳
func (m *Message) ensureMeta() {
	if m.ID == "" {
		m.ID = uuid.NewString()
	}
	if m.TS == 0 {
		m.TS = NowMillis()
	}
}

// encodeData 将任意载荷编码为 JSON；nil 返回空 data
func encodeData(data any) (json.RawMessage, error) {
	if data == nil {
		return nil, nil
	}
	if raw, ok := data.(json.RawMessage); ok {
		if !json.Valid(raw) {
			return nil, errors.New("data 必须是合法 JSON")
		}
		return raw, nil
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("编码 data: %w", err)
	}
	return raw, nil
}

// NowMillis 当前时间的 Unix 毫秒
func NowMillis() int64 {
	return time.Now().UnixMilli()
}

// newID 生成消息/会话 ID
func newID() string {
	return uuid.NewString()
}
