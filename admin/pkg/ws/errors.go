package ws

import "errors"

var (
	// ErrSessionClosed 会话已关闭
	ErrSessionClosed = errors.New("ws: 会话已关闭")
	// ErrSlowConsumer 发送缓冲已满，会话被主动关闭
	ErrSlowConsumer = errors.New("ws: 发送缓冲已满")
	// ErrNotConnected 客户端当前未连接
	ErrNotConnected = errors.New("ws: 客户端未连接")
	// ErrClientClosed 客户端已关闭
	ErrClientClosed = errors.New("ws: 客户端已关闭")
)
