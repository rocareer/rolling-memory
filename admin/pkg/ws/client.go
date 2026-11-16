package ws

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// 默认重连参数
const (
	defaultReconnectMin = 500 * time.Millisecond
	defaultReconnectMax = 30 * time.Second
)

// ClientOptions 客户端参数
type ClientOptions struct {
	URL          string      // ws:// 或 wss:// 地址
	Header       http.Header // 握手请求头（如 Authorization）
	DialOptions  *websocket.DialOptions
	Reconnect    bool           // 断线后是否自动重连
	ReconnectMin time.Duration  // 重连最小退避
	ReconnectMax time.Duration  // 重连最大退避
	OnMessage    func(*Message) // 收到任意消息（含确认与错误）
	OnConnect    func()         // 每次连接建立
	OnDisconnect func(error)    // 每次连接断开
}

// Client WebSocket 客户端：负责拨号、自动重连、请求/响应关联与房间自动重入。
type Client struct {
	opts     ClientOptions
	dialOpts *websocket.DialOptions

	mu           sync.Mutex
	conn         *websocket.Conn
	pending      map[string]chan callResult
	joined       map[string]struct{}
	userID       string
	connectionID string

	writeMu   sync.Mutex
	closeCh   chan struct{}
	closeOnce sync.Once
}

// callResult 请求/响应结果
type callResult struct {
	msg *Message
	err error
}

// NewClient 创建客户端；Run 负责实际连接与读取
func NewClient(opts ClientOptions) (*Client, error) {
	if opts.URL == "" {
		return nil, fmt.Errorf("ws: URL 必填")
	}
	if opts.ReconnectMin <= 0 {
		opts.ReconnectMin = defaultReconnectMin
	}
	if opts.ReconnectMax <= 0 {
		opts.ReconnectMax = defaultReconnectMax
	}
	if opts.ReconnectMax < opts.ReconnectMin {
		opts.ReconnectMax = opts.ReconnectMin
	}

	dialOpts := &websocket.DialOptions{}
	if opts.DialOptions != nil {
		*dialOpts = *opts.DialOptions
		if opts.DialOptions.HTTPHeader != nil {
			dialOpts.HTTPHeader = opts.DialOptions.HTTPHeader.Clone()
		}
	}
	if len(opts.Header) > 0 {
		if dialOpts.HTTPHeader == nil {
			dialOpts.HTTPHeader = make(http.Header, len(opts.Header))
		}
		for key, values := range opts.Header {
			for _, value := range values {
				dialOpts.HTTPHeader.Add(key, value)
			}
		}
	}

	return &Client{
		opts:     opts,
		dialOpts: dialOpts,
		pending:  make(map[string]chan callResult),
		joined:   make(map[string]struct{}),
		closeCh:  make(chan struct{}),
	}, nil
}

// Run 阻塞运行客户端：拨号、读取消息，按配置自动重连；调用方负责取消 ctx。
func (c *Client) Run(ctx context.Context) error {
	backoff := c.opts.ReconnectMin
	for {
		connected, err := c.connectAndRead(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		select {
		case <-c.closeCh:
			return nil
		default:
		}

		c.failPending(err)
		if c.opts.OnDisconnect != nil {
			c.opts.OnDisconnect(err)
		}
		if !c.opts.Reconnect {
			return err
		}
		if connected {
			backoff = c.opts.ReconnectMin
		}

		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return ctx.Err()
		case <-c.closeCh:
			return nil
		}
		backoff *= 2
		if backoff > c.opts.ReconnectMax {
			backoff = c.opts.ReconnectMax
		}
	}
}

// connectAndRead 建立一次连接并读取到断开；connected 表示拨号是否成功过
func (c *Client) connectAndRead(ctx context.Context) (connected bool, err error) {
	select {
	case <-c.closeCh:
		return false, nil
	default:
	}
	conn, _, err := websocket.Dial(ctx, c.opts.URL, c.dialOpts)
	if err != nil {
		return false, fmt.Errorf("ws: 拨号失败: %w", err)
	}

	c.mu.Lock()
	select {
	case <-c.closeCh:
		c.mu.Unlock()
		_ = conn.CloseNow()
		return false, nil
	default:
		c.conn = conn
	}
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		if c.conn == conn {
			c.conn = nil
		}
		c.mu.Unlock()
		_ = conn.CloseNow()
	}()

	if c.opts.OnConnect != nil {
		c.opts.OnConnect()
	}
	if err := c.rejoinRooms(ctx, conn); err != nil {
		return true, err
	}

	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			return true, err
		}
		if typ != websocket.MessageText {
			continue
		}
		var msg Message
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}
		c.dispatch(&msg)
	}
}

// rejoinRooms 重连后自动重新加入此前已加入的房间
func (c *Client) rejoinRooms(ctx context.Context, conn *websocket.Conn) error {
	c.mu.Lock()
	rooms := make([]string, 0, len(c.joined))
	for room := range c.joined {
		rooms = append(rooms, room)
	}
	c.mu.Unlock()

	for _, room := range rooms {
		msg, err := NewMessage(TypeGroup, ActionJoin, nil)
		if err != nil {
			return err
		}
		msg.Room = room
		if err := c.write(ctx, conn, msg); err != nil {
			return err
		}
	}
	return nil
}

// dispatch 分发入站消息：先完成请求关联，再回调业务
func (c *Client) dispatch(msg *Message) {
	if msg.Type == TypeSystem && msg.Action == ActionWelcome {
		var data struct {
			ConnectionID string `json:"connection_id"`
			UserID       string `json:"user_id"`
		}
		if msg.DecodeData(&data) == nil {
			c.mu.Lock()
			c.connectionID = data.ConnectionID
			c.userID = data.UserID
			c.mu.Unlock()
		}
	}

	if msg.ReplyTo != "" {
		c.mu.Lock()
		ch := c.pending[msg.ReplyTo]
		c.mu.Unlock()
		if ch != nil {
			// 缓冲为 1；迟到的重复响应直接丢弃，避免阻塞读循环
			select {
			case ch <- callResult{msg: msg}:
			default:
			}
		}
	}

	if c.opts.OnMessage != nil {
		c.opts.OnMessage(msg)
	}
}

// Send 发送一条消息；客户端当前未连接时返回 ErrNotConnected
func (c *Client) Send(ctx context.Context, msg *Message) error {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn == nil {
		return ErrNotConnected
	}
	return c.write(ctx, conn, msg)
}

// write 串行写出一帧
func (c *Client) write(ctx context.Context, conn *websocket.Conn, msg *Message) error {
	msg.ensureMeta()
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return conn.Write(ctx, websocket.MessageText, data)
}

// Call 发送消息并等待 reply_to 对应的响应
func (c *Client) Call(ctx context.Context, msg *Message) (*Message, error) {
	if msg.ID == "" {
		msg.ID = newID()
	}

	ch := make(chan callResult, 1)
	c.mu.Lock()
	c.pending[msg.ID] = ch
	c.mu.Unlock()
	defer c.removePending(msg.ID)

	if err := c.Send(ctx, msg); err != nil {
		return nil, err
	}

	select {
	case result := <-ch:
		return result.msg, result.err
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.closeCh:
		return nil, ErrClientClosed
	}
}

// removePending 移除请求关联
func (c *Client) removePending(id string) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

// failPending 连接断开时唤醒全部等待中的调用
func (c *Client) failPending(err error) {
	c.mu.Lock()
	pending := c.pending
	c.pending = make(map[string]chan callResult)
	c.mu.Unlock()

	for _, ch := range pending {
		select {
		case ch <- callResult{err: err}:
		default:
		}
	}
}

// Join 加入群聊房间
func (c *Client) Join(ctx context.Context, room string) error {
	msg, err := NewMessage(TypeGroup, ActionJoin, nil)
	if err != nil {
		return err
	}
	msg.Room = room

	resp, err := c.Call(ctx, msg)
	if err != nil {
		return err
	}
	if err := responseError(resp); err != nil {
		return err
	}

	c.mu.Lock()
	c.joined[room] = struct{}{}
	c.mu.Unlock()
	return nil
}

// Leave 离开群聊房间
func (c *Client) Leave(ctx context.Context, room string) error {
	msg, err := NewMessage(TypeGroup, ActionLeave, nil)
	if err != nil {
		return err
	}
	msg.Room = room

	resp, err := c.Call(ctx, msg)
	if err != nil {
		return err
	}
	if err := responseError(resp); err != nil {
		return err
	}

	c.mu.Lock()
	delete(c.joined, room)
	c.mu.Unlock()
	return nil
}

// SendGroup 向房间广播消息，返回服务端确认（含 recipients）
func (c *Client) SendGroup(ctx context.Context, room string, data any) (*Message, error) {
	msg, err := NewMessage(TypeGroup, ActionSend, data)
	if err != nil {
		return nil, err
	}
	msg.Room = room

	resp, err := c.Call(ctx, msg)
	if err != nil {
		return nil, err
	}
	if err := responseError(resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// SendTo 向指定用户发送通用消息，返回服务端确认（含 recipients）
func (c *Client) SendTo(ctx context.Context, userID string, data any) (*Message, error) {
	msg, err := NewMessage(TypeGeneric, ActionSend, data)
	if err != nil {
		return nil, err
	}
	msg.To = userID

	resp, err := c.Call(ctx, msg)
	if err != nil {
		return nil, err
	}
	if err := responseError(resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// SendRoomGeneric 向房间发送通用消息（data 原样透传）
func (c *Client) SendRoomGeneric(ctx context.Context, room string, data any) (*Message, error) {
	msg, err := NewMessage(TypeGeneric, ActionSend, data)
	if err != nil {
		return nil, err
	}
	msg.Room = room

	resp, err := c.Call(ctx, msg)
	if err != nil {
		return nil, err
	}
	if err := responseError(resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// Chat 发起 AI 对话，等待服务端受理；后续增量通过 OnMessage 接收
func (c *Client) Chat(ctx context.Context, data any) (*Message, error) {
	msg, err := NewMessage(TypeAI, ActionChat, data)
	if err != nil {
		return nil, err
	}

	resp, err := c.Call(ctx, msg)
	if err != nil {
		return nil, err
	}
	if err := responseError(resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// Cancel 取消指定 AI 请求
func (c *Client) Cancel(ctx context.Context, requestID string) (*Message, error) {
	msg, err := NewMessage(TypeAI, ActionCancel, map[string]string{"request_id": requestID})
	if err != nil {
		return nil, err
	}

	resp, err := c.Call(ctx, msg)
	if err != nil {
		return nil, err
	}
	if err := responseError(resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// UserID 当前连接的用户 ID（需等待 welcome 消息到达）
func (c *Client) UserID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.userID
}

// ConnectionID 当前连接 ID（需等待 welcome 消息到达）
func (c *Client) ConnectionID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.connectionID
}

// Close 关闭客户端连接；调用方仍应取消 Run 的 ctx
func (c *Client) Close() error {
	var err error
	c.closeOnce.Do(func() {
		close(c.closeCh)
		c.mu.Lock()
		conn := c.conn
		c.conn = nil
		c.mu.Unlock()
		if conn != nil {
			c.writeMu.Lock()
			err = conn.Close(websocket.StatusNormalClosure, "client close")
			c.writeMu.Unlock()
		}
	})
	return err
}

// responseError 将服务端 error 事件转换为 Go error
func responseError(msg *Message) error {
	if msg == nil || msg.Action != ActionError {
		return nil
	}
	var data ErrorData
	if err := msg.DecodeData(&data); err != nil {
		return fmt.Errorf("ws: 请求失败")
	}
	if data.Code == "" {
		data.Code = "error"
	}
	return fmt.Errorf("ws: %s: %s", data.Code, data.Message)
}
