package ws

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// bestEffortMaxRetries 尽力投递在队列满后「弹最旧再重试入队」的次数上限
const bestEffortMaxRetries = 2

// errBestEffortDropped 尽力投递在有界重试内未能入队，本帧按尽力语义丢弃（会话保持在线，不断连）
var errBestEffortDropped = errors.New("ws: 尽力投递丢弃本帧")

// Session 服务端侧的单条 WebSocket 连接。
type Session struct {
	id       string
	conn     *websocket.Conn
	hub      *Hub
	server   *Server
	identity Identity

	remoteAddr  string    // 握手对端地址（r.RemoteAddr），创建后只读
	connectedAt time.Time // 会话创建时刻，创建后只读

	send chan []byte
	done chan struct{}

	ctx    context.Context
	cancel context.CancelFunc

	closeOnce sync.Once

	inflightMu sync.Mutex
	inflight   map[string]context.CancelFunc
}

// newSession 创建服务端会话；remoteAddr 记录握手对端地址，connectedAt 记录创建时刻
func newSession(ctx context.Context, conn *websocket.Conn, hub *Hub, server *Server, identity Identity, remoteAddr string, buffer int) *Session {
	ctx, cancel := context.WithCancel(ctx)
	return &Session{
		id:          newID(),
		conn:        conn,
		hub:         hub,
		server:      server,
		identity:    identity,
		remoteAddr:  remoteAddr,
		connectedAt: time.Now(),
		send:        make(chan []byte, buffer),
		done:        make(chan struct{}),
		ctx:         ctx,
		cancel:      cancel,
		inflight:    make(map[string]context.CancelFunc),
	}
}

// start 启动读写循环
func (s *Session) start() {
	go s.writeLoop()
	go s.readLoop()
}

// readLoop 读取客户端消息并交给引擎分发
func (s *Session) readLoop() {
	defer s.close(websocket.StatusNormalClosure, "")

	for {
		typ, data, err := s.conn.Read(s.ctx)
		if err != nil {
			return
		}
		if typ != websocket.MessageText {
			_ = s.sendError("", "unsupported_message", "仅支持文本 JSON 消息")
			continue
		}
		var msg Message
		if err := json.Unmarshal(data, &msg); err != nil {
			_ = s.sendError("", "invalid_message", "消息必须是合法 JSON")
			continue
		}
		s.server.counters.msgsIn.Add(1)
		s.server.counters.bytesIn.Add(uint64(len(data)))
		s.server.handle(s, &msg)
	}
}

// writeLoop 串行写出队列消息，并定时发送协议层 ping 探活
func (s *Session) writeLoop() {
	ticker := time.NewTicker(s.server.opts.PingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-s.done:
			return
		case data := <-s.send:
			if err := s.write(data); err != nil {
				s.close(websocket.StatusInternalError, "write failed")
				return
			}
			s.server.counters.msgsOut.Add(1)
			s.server.counters.bytesOut.Add(uint64(len(data)))
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(s.ctx, s.server.opts.WriteTimeout)
			err := s.conn.Ping(ctx)
			cancel()
			if err != nil {
				s.close(websocket.StatusGoingAway, "ping timeout")
				return
			}
		}
	}
}

// write 带超时写出一个文本帧
func (s *Session) write(data []byte) error {
	ctx, cancel := context.WithTimeout(s.ctx, s.server.opts.WriteTimeout)
	defer cancel()
	return s.conn.Write(ctx, websocket.MessageText, data)
}

// sendMessage 编码并投递一条消息（异步写）
func (s *Session) sendMessage(msg *Message) error {
	msg.ensureMeta()
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return s.sendBytes(data)
}

// sendBytes 将已编码的帧放入发送队列；队列满视为慢消费者并主动断开
func (s *Session) sendBytes(data []byte) error {
	select {
	case <-s.done:
		return ErrSessionClosed
	default:
	}

	select {
	case s.send <- data:
		return nil
	case <-s.done:
		return ErrSessionClosed
	default:
		s.server.counters.slowConsumer.Add(1)
		s.closeNow()
		return ErrSlowConsumer
	}
}

// enqueueBestEffort 尽力投递：非阻塞入队；队列满时弹一条最旧（丢最旧帧）再入队，
// 重试次数有界（bestEffortMaxRetries）；会话已关闭返回 ErrSessionClosed。
// 队列在有界重试内始终不可入队（容量为 0 或并发生产者持续插队）时丢弃本帧并返回
// errBestEffortDropped——本方法绝不因队列满断开连接，也不递增慢消费者计数。
//
// 不新增锁：与 sendBytes 同为「多生产者写 send、单消费者（writeLoop）读」模型，
// 尽力语义允许竞态下（并发生产者插队）本条帧仍被丢弃。
func (s *Session) enqueueBestEffort(data []byte) error {
	select {
	case <-s.done:
		return ErrSessionClosed
	default:
	}

	for retry := 0; ; retry++ {
		select {
		case s.send <- data:
			return nil
		case <-s.done:
			return ErrSessionClosed
		default:
		}

		if retry >= bestEffortMaxRetries {
			return errBestEffortDropped
		}

		// 队列满：弹一条最旧为本次投递腾位，随后重试入队；消费者已排空则直接重试。
		select {
		case <-s.send:
		default:
		}
	}
}

// sendError 下发统一错误消息
func (s *Session) sendError(replyTo, code, message string) error {
	return s.sendMessage(NewError(replyTo, code, message))
}

// close 关闭会话（幂等，优雅关闭帧）
func (s *Session) close(code websocket.StatusCode, reason string) {
	s.shutdown(func() {
		_ = s.conn.Close(code, reason)
	})
}

// closeNow 立即关闭会话（不等待对端关闭帧；用于慢消费者等异常路径）
func (s *Session) closeNow() {
	s.shutdown(func() {
		_ = s.conn.CloseNow()
	})
}

// shutdown 执行一次性的会话清理
func (s *Session) shutdown(closeConn func()) {
	s.closeOnce.Do(func() {
		close(s.done)
		s.cancel()
		leftRooms := s.hub.unregister(s)
		if s.server != nil && s.server.opts.OnRoomLeave != nil {
			for room, remaining := range leftRooms {
				s.server.opts.OnRoomLeave(room, remaining)
			}
		}
		s.cancelAllInflight()
		closeConn()
	})
}

// trackInflight 登记一个进行中的 AI 请求
func (s *Session) trackInflight(id string, cancel context.CancelFunc) {
	s.inflightMu.Lock()
	s.inflight[id] = cancel
	s.inflightMu.Unlock()
}

// untrackInflight 移除已结束的 AI 请求
func (s *Session) untrackInflight(id string) {
	s.inflightMu.Lock()
	delete(s.inflight, id)
	s.inflightMu.Unlock()
}

// cancelInflight 取消指定 AI 请求，返回是否命中
func (s *Session) cancelInflight(id string) bool {
	s.inflightMu.Lock()
	cancel, ok := s.inflight[id]
	if ok {
		delete(s.inflight, id)
	}
	s.inflightMu.Unlock()

	if ok {
		cancel()
	}
	return ok
}

// cancelAllInflight 取消会话内全部 AI 请求
func (s *Session) cancelAllInflight() {
	s.inflightMu.Lock()
	cancels := make([]context.CancelFunc, 0, len(s.inflight))
	for _, cancel := range s.inflight {
		cancels = append(cancels, cancel)
	}
	s.inflight = make(map[string]context.CancelFunc)
	s.inflightMu.Unlock()

	for _, cancel := range cancels {
		cancel()
	}
}
