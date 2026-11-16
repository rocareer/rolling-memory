package ws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// 默认参数
const (
	defaultReadLimit    = 1 << 20 // 单条消息上限 1 MiB
	defaultSendBuffer   = 64      // 单连接发送队列长度
	defaultPingInterval = 30 * time.Second
	defaultWriteTimeout = 10 * time.Second
)

// Identity 已认证的连接身份
type Identity struct {
	ID   string         `json:"id"`
	Name string         `json:"name,omitempty"`
	Meta map[string]any `json:"meta,omitempty"`
}

// Authenticator 握手阶段的认证钩子；返回错误时以 HTTP 401 拒绝升级
type Authenticator func(r *http.Request) (Identity, error)

// RoomAuthorizer 房间加入授权钩子：group join 前调用，返回错误则拒绝加入（回 permission_denied）。
// ctx 为发起 join 的会话上下文，连接关闭即取消；identity 为握手阶段认证得到的身份。
// 权限策略由使用方决定：未配置时不做房间级校验（引擎默认放行，保持既有群聊语义）。
type RoomAuthorizer func(ctx context.Context, identity Identity, room string) error

// Options 服务端参数
type Options struct {
	Authenticator  Authenticator  // 认证钩子；未配置时必须显式允许匿名
	AllowAnonymous bool           // 无认证时是否允许匿名连接
	AI             AIProvider     // AI 生成实现；nil 时 ai/chat 返回 ai_unavailable
	RoomAuthorizer RoomAuthorizer // 房间加入授权钩子；nil 时不校验（由使用方决定策略）
	OnRoomJoin     func(room string, members int)
	OnRoomLeave    func(room string, members int)
	AcceptOptions  *websocket.AcceptOptions // 升级参数（Origin 白名单、压缩等）
	ReadLimit      int64                    // 单条消息字节上限
	SendBuffer     int                      // 单连接发送队列长度
	PingInterval   time.Duration            // 协议层 ping 间隔
	WriteTimeout   time.Duration            // 单次写/ ping 超时
}

// Server WebSocket 引擎服务端：实现 http.Handler，可直接挂到 Gin 路由；
// Stats/RoomSize 提供只读可观测性快照（见 stats.go）。
type Server struct {
	opts Options
	hub  *Hub

	ctx    context.Context
	cancel context.CancelFunc

	startedAt time.Time // 创建时刻，用于 uptime_seconds
	counters  counters  // 累计计数器（原子，热路径无锁）

	closeOnce sync.Once
	seq       uint64
	seqMu     sync.Mutex
}

// NewServer 创建服务端；未配置认证且未允许匿名时返回错误（fail-closed）
func NewServer(opts Options) (*Server, error) {
	if opts.Authenticator == nil && !opts.AllowAnonymous {
		return nil, errors.New("ws: 未配置 Authenticator，且未允许匿名连接")
	}
	if opts.ReadLimit <= 0 {
		opts.ReadLimit = defaultReadLimit
	}
	if opts.SendBuffer <= 0 {
		opts.SendBuffer = defaultSendBuffer
	}
	if opts.PingInterval <= 0 {
		opts.PingInterval = defaultPingInterval
	}
	if opts.WriteTimeout <= 0 {
		opts.WriteTimeout = defaultWriteTimeout
	}

	ctx, cancel := context.WithCancel(context.Background())
	return &Server{
		opts:      opts,
		hub:       newHub(),
		ctx:       ctx,
		cancel:    cancel,
		startedAt: time.Now(),
	}, nil
}

// ServeHTTP 完成认证与协议升级，随后由读写循环接管连接
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "仅支持 GET 升级", http.StatusMethodNotAllowed)
		return
	}

	identity, err := s.authenticate(r)
	if err != nil {
		s.counters.authFailed.Add(1)
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}

	conn, err := websocket.Accept(w, r, s.opts.AcceptOptions)
	if err != nil {
		// Accept 已向客户端写出 HTTP 错误
		return
	}
	conn.SetReadLimit(s.opts.ReadLimit)

	sess := newSession(s.ctx, conn, s.hub, s, identity, r.RemoteAddr, s.opts.SendBuffer)
	s.hub.register(sess)
	s.counters.connects.Add(1)
	sess.start()

	welcome, _ := NewMessage(TypeSystem, ActionWelcome, map[string]any{
		"connection_id": sess.id,
		"user_id":       identity.ID,
		"name":          identity.Name,
		"meta":          identity.Meta,
	})
	_ = sess.sendMessage(welcome)
}

// authenticate 执行认证；匿名模式下分配临时访客 ID
func (s *Server) authenticate(r *http.Request) (Identity, error) {
	if s.opts.Authenticator == nil {
		s.seqMu.Lock()
		s.seq++
		seq := s.seq
		s.seqMu.Unlock()
		return Identity{ID: fmt.Sprintf("guest-%d", seq), Name: "guest"}, nil
	}

	identity, err := s.opts.Authenticator(r)
	if err != nil {
		return Identity{}, err
	}
	if strings.TrimSpace(identity.ID) == "" {
		return Identity{}, errors.New("认证结果缺少用户 ID")
	}
	return identity, nil
}

// handle 分发一条已解析的客户端消息
func (s *Server) handle(sess *Session, msg *Message) {
	if err := msg.Validate(); err != nil {
		_ = sess.sendError(msg.ID, "invalid_message", err.Error())
		return
	}

	switch msg.Type {
	case TypeGroup:
		s.handleGroup(sess, msg)
	case TypeAI:
		s.handleAI(sess, msg)
	case TypeGeneric:
		s.handleGeneric(sess, msg)
	case TypeSystem:
		s.handleSystem(sess, msg)
	}
}

// handleGroup 处理群聊：加入、离开、房间广播
func (s *Server) handleGroup(sess *Session, msg *Message) {
	switch msg.Action {
	case ActionJoin:
		if err := s.authorizeJoin(sess, msg.Room); err != nil {
			_ = sess.sendError(msg.ID, "permission_denied", err.Error())
			return
		}
		members := s.hub.join(sess, msg.Room)
		reply, _ := NewMessage(TypeGroup, ActionJoined, map[string]any{
			"room":    msg.Room,
			"members": members,
		})
		reply.ReplyTo = msg.ID
		reply.Room = msg.Room
		_ = sess.sendMessage(reply)
		if s.opts.OnRoomJoin != nil {
			s.opts.OnRoomJoin(msg.Room, members)
		}
	case ActionLeave:
		members := s.hub.leave(sess, msg.Room)
		reply, _ := NewMessage(TypeGroup, ActionLeft, map[string]any{
			"room":    msg.Room,
			"members": members,
		})
		reply.ReplyTo = msg.ID
		reply.Room = msg.Room
		_ = sess.sendMessage(reply)
		if s.opts.OnRoomLeave != nil {
			s.opts.OnRoomLeave(msg.Room, members)
		}
	case ActionSend:
		if !s.hub.inRoom(sess, msg.Room) {
			_ = sess.sendError(msg.ID, "not_in_room", "请先加入房间")
			return
		}
		out := &Message{
			Type:     TypeGroup,
			Action:   ActionMessage,
			Room:     msg.Room,
			From:     sess.identity.ID,
			FromName: sess.identity.Name,
			Data:     msg.Data,
		}
		out.ensureMeta()
		recipients := s.hub.broadcastRoom(msg.Room, out)

		ack, _ := NewMessage(TypeGroup, ActionSent, map[string]any{
			"room":       msg.Room,
			"recipients": recipients,
		})
		ack.ReplyTo = msg.ID
		ack.Room = msg.Room
		_ = sess.sendMessage(ack)
	}
}

// authorizeJoin 校验会话是否有权加入房间；未配置 RoomAuthorizer 时放行
func (s *Server) authorizeJoin(sess *Session, room string) error {
	if s.opts.RoomAuthorizer == nil {
		return nil
	}
	return s.opts.RoomAuthorizer(sess.ctx, sess.identity, room)
}

// handleGeneric 处理通用消息：点对点或房间广播，data 原样透传
func (s *Server) handleGeneric(sess *Session, msg *Message) {
	out := &Message{
		Type:     TypeGeneric,
		Action:   ActionMessage,
		Room:     msg.Room,
		From:     sess.identity.ID,
		FromName: sess.identity.Name,
		Data:     msg.Data,
	}
	out.ensureMeta()

	var recipients int
	if msg.Room != "" {
		if !s.hub.inRoom(sess, msg.Room) {
			_ = sess.sendError(msg.ID, "not_in_room", "请先加入房间")
			return
		}
		recipients = s.hub.broadcastRoom(msg.Room, out)
	} else {
		recipients = s.hub.sendToUser(msg.To, out)
		if recipients == 0 {
			_ = sess.sendError(msg.ID, "recipient_offline", "目标用户不在线")
			return
		}
	}

	ack, _ := NewMessage(TypeGeneric, ActionSent, map[string]any{
		"recipients": recipients,
	})
	ack.ReplyTo = msg.ID
	ack.Room = msg.Room
	ack.To = msg.To
	_ = sess.sendMessage(ack)
}

// handleSystem 处理应用层心跳
func (s *Server) handleSystem(sess *Session, msg *Message) {
	if msg.Action != ActionPing {
		return
	}
	pong, _ := NewMessage(TypeSystem, ActionPong, msg.Data)
	pong.ReplyTo = msg.ID
	_ = sess.sendMessage(pong)
}

// handleAI 处理 AI 对话：发起与取消
func (s *Server) handleAI(sess *Session, msg *Message) {
	switch msg.Action {
	case ActionChat:
		s.startAI(sess, msg)
	case ActionCancel:
		s.cancelAI(sess, msg)
	}
}

// startAI 解析请求并异步消费 AIProvider 的流式增量
func (s *Server) startAI(sess *Session, msg *Message) {
	if s.opts.AI == nil {
		_ = sess.sendError(msg.ID, "ai_unavailable", "AI 服务未配置")
		return
	}

	var data aiChatData
	if err := msg.DecodeData(&data); err != nil {
		_ = sess.sendError(msg.ID, "invalid_message", "AI 请求 data 格式错误: "+err.Error())
		return
	}
	if len(data.Messages) == 0 {
		_ = sess.sendError(msg.ID, "invalid_message", "AI 请求缺少 messages")
		return
	}
	if data.Agent == "" {
		_ = sess.sendError(msg.ID, "invalid_message", "AI 请求缺少 agent（所有 LLM 请求必须指定定义过的 agent）")
		return
	}

	reqID := msg.ID
	if reqID == "" {
		reqID = newID()
	}
	req := AIRequest{
		ID:       reqID,
		Identity: sess.identity,
		Agent:    data.Agent,
		Messages: data.Messages,
		Raw:      msg.Data,
	}

	ctx, cancel := context.WithCancel(sess.ctx)
	sess.trackInflight(reqID, cancel)

	accepted, _ := NewMessage(TypeAI, ActionAccepted, map[string]any{"request_id": reqID})
	accepted.ReplyTo = msg.ID
	if err := sess.sendMessage(accepted); err != nil {
		cancel()
		sess.untrackInflight(reqID)
		return
	}

	go func() {
		defer sess.untrackInflight(reqID)
		defer cancel()

		result, err := s.opts.AI.Stream(ctx, req, func(chunk AIChunk) error {
			delta, err := NewMessage(TypeAI, ActionDelta, chunk)
			if err != nil {
				return err
			}
			delta.ReplyTo = reqID
			return sess.sendMessage(delta)
		})
		if err != nil {
			// 取消由 cancelAI 统一回执，这里不再重复下发
			if ctx.Err() != nil || errors.Is(err, context.Canceled) {
				return
			}
			fail := NewError(reqID, "ai_failed", err.Error())
			fail.Type = TypeAI
			_ = sess.sendMessage(fail)
			return
		}

		doneData := map[string]any{}
		if result != nil && len(result.Usage) > 0 {
			doneData["usage"] = json.RawMessage(result.Usage)
		}
		done, _ := NewMessage(TypeAI, ActionDone, doneData)
		done.ReplyTo = reqID
		_ = sess.sendMessage(done)
	}()
}

// cancelAI 取消进行中的 AI 对话
func (s *Server) cancelAI(sess *Session, msg *Message) {
	var data struct {
		RequestID string `json:"request_id"`
	}
	_ = msg.DecodeData(&data)
	reqID := strings.TrimSpace(data.RequestID)
	if reqID == "" {
		reqID = msg.ID
	}
	if !sess.cancelInflight(reqID) {
		_ = sess.sendError(msg.ID, "not_found", "未找到进行中的 AI 请求")
		return
	}

	reply, _ := NewMessage(TypeAI, ActionCanceled, map[string]any{"request_id": reqID})
	reply.ReplyTo = msg.ID
	_ = sess.sendMessage(reply)
}

// SendToUser 向指定用户的全部在线连接推送消息（供业务侧主动调用）
func (s *Server) SendToUser(userID string, msg *Message) int {
	return s.hub.sendToUser(userID, msg)
}

// BroadcastRoom 向房间内全部连接推送消息（供业务侧主动调用）
func (s *Server) BroadcastRoom(room string, msg *Message) int {
	return s.hub.broadcastRoom(room, msg)
}

// BroadcastRoomBestEffort 尽力投递版房间广播（供事件面等可丢弃通道使用）：
// 队列满时丢最旧帧、不断连；会话已关闭/未命中直接忽略（不计慢消费者），返回成功入队的会话数。
// 与 BroadcastRoom 的差别仅在于慢消费者处置：后者队满即视为慢消费者并主动断开连接
// （AI 流等强语义通道保持该行为），本方法只丢帧、连接保持在线。
func (s *Server) BroadcastRoomBestEffort(room string, msg *Message) int {
	return s.hub.broadcastRoomBestEffort(room, msg)
}

// Online 返回 (在线连接数, 在线用户数)
func (s *Server) Online() (sessions int, users int) {
	return s.hub.counts()
}

// Close 关闭全部连接并停止接收新连接
func (s *Server) Close() {
	s.closeOnce.Do(func() {
		s.cancel()
		for _, sess := range s.hub.sessionsSnapshot() {
			sess.close(websocket.StatusGoingAway, "server shutdown")
		}
	})
}
