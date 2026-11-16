package ws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// sink 收集客户端收到的全部消息，支持按条件等待（不消费历史）
type sink struct {
	mu     sync.Mutex
	msgs   []*Message
	notify chan struct{}
}

func newSink() *sink {
	return &sink{notify: make(chan struct{}, 1)}
}

func (s *sink) onMessage(msg *Message) {
	s.mu.Lock()
	s.msgs = append(s.msgs, msg)
	s.mu.Unlock()

	select {
	case s.notify <- struct{}{}:
	default:
	}
}

func (s *sink) snapshot() []*Message {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]*Message, len(s.msgs))
	copy(out, s.msgs)
	return out
}

func (s *sink) wait(t *testing.T, timeout time.Duration, desc string, match func(*Message) bool) *Message {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		for _, msg := range s.snapshot() {
			if match(msg) {
				return msg
			}
		}
		remain := time.Until(deadline)
		if remain <= 0 {
			t.Fatalf("等待 %s 超时，已收到: %s", desc, dumpMessages(s.snapshot()))
		}
		select {
		case <-s.notify:
		case <-time.After(remain):
		}
	}
}

func dumpMessages(msgs []*Message) string {
	parts := make([]string, 0, len(msgs))
	for _, msg := range msgs {
		parts = append(parts, string(msg.Type)+"/"+string(msg.Action)+" reply_to="+msg.ReplyTo)
	}
	return strings.Join(parts, "; ")
}

// newTestServer 启动带固定用户认证的测试服务端
func newTestServer(t *testing.T, ai AIProvider) (*Server, string) {
	t.Helper()

	srv, err := NewServer(Options{
		AI:           ai,
		PingInterval: 5 * time.Second,
		Authenticator: func(r *http.Request) (Identity, error) {
			user := strings.TrimSpace(r.URL.Query().Get("user"))
			if user == "" {
				return Identity{}, errors.New("缺少 user")
			}
			return Identity{ID: user, Name: user}, nil
		},
	})
	if err != nil {
		t.Fatalf("创建服务端失败: %v", err)
	}

	ts := httptest.NewServer(srv)
	t.Cleanup(func() {
		srv.Close()
		ts.Close()
	})
	return srv, ts.URL
}

// newTestClient 连接测试服务端并等待 welcome
func newTestClient(t *testing.T, baseURL, user string) (*Client, *sink) {
	t.Helper()

	col := newSink()
	connected := make(chan struct{}, 1)
	client, err := NewClient(ClientOptions{
		URL:       baseURL + "?user=" + url.QueryEscape(user),
		OnMessage: col.onMessage,
		OnConnect: func() {
			select {
			case connected <- struct{}{}:
			default:
			}
		},
	})
	if err != nil {
		t.Fatalf("创建客户端失败: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = client.Run(ctx)
	}()

	t.Cleanup(func() {
		cancel()
		_ = client.Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Errorf("客户端未在超时内退出")
		}
	})

	select {
	case <-connected:
	case <-time.After(2 * time.Second):
		t.Fatal("等待连接建立超时")
	}
	col.wait(t, 2*time.Second, "welcome", func(msg *Message) bool {
		return msg.Type == TypeSystem && msg.Action == ActionWelcome
	})
	return client, col
}

// frameCounter 统计测试连接收到的原始帧数与字节数，用于与 Stats 计数器逐一对账
type frameCounter struct {
	frames int
	bytes  int
}

// read 持续读取服务端帧直到命中条件，同时累计帧数与字节数
func (f *frameCounter) read(t *testing.T, ctx context.Context, conn *websocket.Conn, desc string, match func(*Message) bool) *Message {
	t.Helper()

	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("等待 %s 失败: %v", desc, err)
		}
		f.frames++
		f.bytes += len(data)

		var msg Message
		if err := json.Unmarshal(data, &msg); err != nil {
			t.Fatalf("解析 %s 帧失败: %v", desc, err)
		}
		if match(&msg) {
			return &msg
		}
	}
}

// send 编码并写出一帧客户端消息，返回实际写出的原始字节
func (f *frameCounter) send(t *testing.T, ctx context.Context, conn *websocket.Conn, msg *Message) []byte {
	t.Helper()

	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("编码消息失败: %v", err)
	}
	if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
		t.Fatalf("发送消息失败: %v", err)
	}
	return data
}

// waitStats 轮询 Stats 快照直到满足条件（写入侧计数晚于客户端读到的帧，需等其收敛）
func waitStats(t *testing.T, srv *Server, timeout time.Duration, desc string, match func(Stats) bool) Stats {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for {
		stats := srv.Stats()
		if match(stats) {
			return stats
		}
		if time.Now().After(deadline) {
			t.Fatalf("等待 %s 超时，当前快照: %+v", desc, stats)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestGroupChat 群聊：加入、广播、离开
func TestGroupChat(t *testing.T) {
	_, baseURL := newTestServer(t, nil)
	c1, _ := newTestClient(t, baseURL, "u1")
	c2, sink2 := newTestClient(t, baseURL, "u2")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if err := c1.Join(ctx, "g1"); err != nil {
		t.Fatalf("u1 加入房间失败: %v", err)
	}
	if err := c2.Join(ctx, "g1"); err != nil {
		t.Fatalf("u2 加入房间失败: %v", err)
	}

	ack, err := c1.SendGroup(ctx, "g1", map[string]string{"content": "大家好"})
	if err != nil {
		t.Fatalf("发送群聊消息失败: %v", err)
	}
	var ackData struct {
		Recipients int `json:"recipients"`
	}
	if err := ack.DecodeData(&ackData); err != nil || ackData.Recipients != 2 {
		t.Fatalf("群聊确认不符: %+v err=%v", ackData, err)
	}

	got := sink2.wait(t, 2*time.Second, "u2 收到群聊消息", func(msg *Message) bool {
		return msg.Type == TypeGroup && msg.Action == ActionMessage && msg.Room == "g1"
	})
	var data map[string]string
	if err := got.DecodeData(&data); err != nil || data["content"] != "大家好" {
		t.Fatalf("群聊载荷不符: %s err=%v", got.Data, err)
	}
	if got.From != "u1" {
		t.Fatalf("群聊 from 不符: %q", got.From)
	}

	if err := c2.Leave(ctx, "g1"); err != nil {
		t.Fatalf("u2 离开房间失败: %v", err)
	}
	ack, err = c1.SendGroup(ctx, "g1", map[string]string{"content": "还在吗"})
	if err != nil {
		t.Fatalf("再次发送群聊消息失败: %v", err)
	}
	if err := ack.DecodeData(&ackData); err != nil || ackData.Recipients != 1 {
		t.Fatalf("离开后群聊确认不符: %+v err=%v", ackData, err)
	}

	time.Sleep(100 * time.Millisecond)
	count := 0
	for _, msg := range sink2.snapshot() {
		if msg.Type == TypeGroup && msg.Action == ActionMessage && msg.Room == "g1" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("离开后仍收到群聊消息: count=%d", count)
	}
}

// TestRoomJoinAuthorizer 房间加入授权钩子：被拒房间回 permission_denied 且不入房，放行房间正常加入
func TestRoomJoinAuthorizer(t *testing.T) {
	srv, err := NewServer(Options{
		PingInterval: 5 * time.Second,
		RoomAuthorizer: func(_ context.Context, identity Identity, room string) error {
			if room == "locked" {
				return errors.New("无权加入该房间: " + room + "（用户 " + identity.ID + "）")
			}
			return nil
		},
		Authenticator: func(r *http.Request) (Identity, error) {
			user := strings.TrimSpace(r.URL.Query().Get("user"))
			if user == "" {
				return Identity{}, errors.New("缺少 user")
			}
			return Identity{ID: user, Name: user}, nil
		},
	})
	if err != nil {
		t.Fatalf("创建服务端失败: %v", err)
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(func() {
		srv.Close()
		ts.Close()
	})

	client, _ := newTestClient(t, ts.URL, "u1")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if err := client.Join(ctx, "locked"); err == nil {
		t.Fatal("被拒绝的房间不应加入成功")
	} else if !strings.Contains(err.Error(), "permission_denied") {
		t.Fatalf("拒绝错误码不符: %v", err)
	}
	if got := srv.RoomSize("locked"); got != 0 {
		t.Fatalf("被拒绝的会话不应在房间内: %d", got)
	}

	if err := client.Join(ctx, "open"); err != nil {
		t.Fatalf("放行房间加入失败: %v", err)
	}
	if got := srv.RoomSize("open"); got != 1 {
		t.Fatalf("放行房间人数不符: %d", got)
	}
}

// TestGenericMessage 通用消息：点对点投递与离线错误
func TestGenericMessage(t *testing.T) {
	_, baseURL := newTestServer(t, nil)
	c1, _ := newTestClient(t, baseURL, "u1")
	_, sink2 := newTestClient(t, baseURL, "u2")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	ack, err := c1.SendTo(ctx, "u2", map[string]any{"kind": "notice", "text": "hello"})
	if err != nil {
		t.Fatalf("点对点发送失败: %v", err)
	}
	var ackData struct {
		Recipients int `json:"recipients"`
	}
	if err := ack.DecodeData(&ackData); err != nil || ackData.Recipients != 1 {
		t.Fatalf("点对点确认不符: %+v err=%v", ackData, err)
	}

	got := sink2.wait(t, 2*time.Second, "u2 收到通用消息", func(msg *Message) bool {
		return msg.Type == TypeGeneric && msg.Action == ActionMessage && msg.From == "u1"
	})
	var data map[string]any
	if err := got.DecodeData(&data); err != nil || data["kind"] != "notice" {
		t.Fatalf("通用消息载荷不符: %s err=%v", got.Data, err)
	}

	if _, err := c1.SendTo(ctx, "nobody", map[string]string{"text": "hi"}); err == nil {
		t.Fatal("向离线用户发送应返回错误")
	} else if !strings.Contains(err.Error(), "recipient_offline") {
		t.Fatalf("离线错误不符: %v", err)
	}
}

// TestAIChat AI 流式对话：受理、增量、完成
func TestAIChat(t *testing.T) {
	provider := AIProviderFunc(func(ctx context.Context, req AIRequest, emit func(AIChunk) error) (*AIResult, error) {
		if req.Agent != "test-agent" || len(req.Messages) != 1 || req.Messages[0].Content != "你好" {
			return nil, errors.New("请求参数不符")
		}
		for _, delta := range []string{"你", "好"} {
			if err := emit(AIChunk{Delta: delta}); err != nil {
				return nil, err
			}
		}
		return &AIResult{Usage: json.RawMessage(`{"total_tokens":2}`)}, nil
	})

	_, baseURL := newTestServer(t, provider)
	client, col := newTestClient(t, baseURL, "u1")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	accepted, err := client.Chat(ctx, map[string]any{
		"agent":    "test-agent",
		"messages": []AIMessage{{Role: "user", Content: "你好"}},
	})
	if err != nil {
		t.Fatalf("AI 请求失败: %v", err)
	}
	var acceptedData struct {
		RequestID string `json:"request_id"`
	}
	if err := accepted.DecodeData(&acceptedData); err != nil || acceptedData.RequestID == "" {
		t.Fatalf("AI 受理载荷不符: %s err=%v", accepted.Data, err)
	}

	done := col.wait(t, 2*time.Second, "AI 完成", func(msg *Message) bool {
		return msg.Type == TypeAI && msg.Action == ActionDone && msg.ReplyTo == acceptedData.RequestID
	})
	var doneData struct {
		Usage json.RawMessage `json:"usage"`
	}
	if err := done.DecodeData(&doneData); err != nil || !strings.Contains(string(doneData.Usage), "total_tokens") {
		t.Fatalf("AI 完成载荷不符: %s err=%v", done.Data, err)
	}

	var text strings.Builder
	for _, msg := range col.snapshot() {
		if msg.Type != TypeAI || msg.Action != ActionDelta || msg.ReplyTo != acceptedData.RequestID {
			continue
		}
		var chunk AIChunk
		if err := msg.DecodeData(&chunk); err != nil {
			t.Fatalf("增量载荷不符: %s err=%v", msg.Data, err)
		}
		text.WriteString(chunk.Delta)
	}
	if text.String() != "你好" {
		t.Fatalf("增量拼接不符: %q", text.String())
	}
}

// TestAICancel 取消进行中的 AI 对话
func TestAICancel(t *testing.T) {
	started := make(chan struct{}, 1)
	provider := AIProviderFunc(func(ctx context.Context, req AIRequest, emit func(AIChunk) error) (*AIResult, error) {
		if err := emit(AIChunk{Delta: "开始"}); err != nil {
			return nil, err
		}
		select {
		case started <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return nil, ctx.Err()
	})

	_, baseURL := newTestServer(t, provider)
	client, _ := newTestClient(t, baseURL, "u1")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	accepted, err := client.Chat(ctx, map[string]any{
		"agent":    "test-agent",
		"messages": []AIMessage{{Role: "user", Content: "写长文"}},
	})
	if err != nil {
		t.Fatalf("AI 请求失败: %v", err)
	}
	var acceptedData struct {
		RequestID string `json:"request_id"`
	}
	_ = accepted.DecodeData(&acceptedData)

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("等待 AI 开始生成超时")
	}

	canceled, err := client.Cancel(ctx, acceptedData.RequestID)
	if err != nil {
		t.Fatalf("取消失败: %v", err)
	}
	var canceledData struct {
		RequestID string `json:"request_id"`
	}
	if err := canceled.DecodeData(&canceledData); err != nil || canceledData.RequestID != acceptedData.RequestID {
		t.Fatalf("取消回执不符: %s err=%v", canceled.Data, err)
	}
}

// TestPing 应用层心跳
func TestPing(t *testing.T) {
	_, baseURL := newTestServer(t, nil)
	client, _ := newTestClient(t, baseURL, "u1")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	msg, err := NewMessage(TypeSystem, ActionPing, map[string]string{"at": "now"})
	if err != nil {
		t.Fatalf("构造心跳失败: %v", err)
	}
	pong, err := client.Call(ctx, msg)
	if err != nil {
		t.Fatalf("心跳失败: %v", err)
	}
	if pong.Action != ActionPong || pong.ReplyTo != msg.ID {
		t.Fatalf("心跳响应不符: %+v", pong)
	}
}

// TestAuthRejected 未认证连接在升级阶段被 401 拒绝
func TestAuthRejected(t *testing.T) {
	srv, baseURL := newTestServer(t, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	conn, resp, err := websocket.Dial(ctx, baseURL, nil)
	if err == nil {
		_ = conn.CloseNow()
		t.Fatal("未认证连接应当失败")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("期望 401，实际: %+v", resp)
	}
	if got := srv.Stats().Counters.AuthFailed; got != 1 {
		t.Fatalf("auth_failed 计数不符: %d", got)
	}
}

// TestStatsCounters 收发一轮后计数器递增，Stats 数值与收发帧逐项一致
func TestStatsCounters(t *testing.T) {
	srv, baseURL := newTestServer(t, nil)

	before := srv.Stats()
	if before.Counters != (CounterStats{}) {
		t.Fatalf("初始计数器应为零值: %+v", before.Counters)
	}
	if before.Online != (OnlineStats{}) {
		t.Fatalf("初始在线规模应为零值: %+v", before.Online)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, baseURL+"?user=u1", nil)
	if err != nil {
		t.Fatalf("拨号失败: %v", err)
	}
	defer conn.CloseNow()

	recv := &frameCounter{}
	recv.read(t, ctx, conn, "welcome", func(msg *Message) bool {
		return msg.Type == TypeSystem && msg.Action == ActionWelcome
	})

	join, err := NewMessage(TypeGroup, ActionJoin, nil)
	if err != nil {
		t.Fatalf("构造 join 失败: %v", err)
	}
	join.Room = "g1"
	joinFrame := recv.send(t, ctx, conn, join)
	recv.read(t, ctx, conn, "joined", func(msg *Message) bool {
		return msg.Type == TypeGroup && msg.Action == ActionJoined && msg.ReplyTo == join.ID
	})

	ping, err := NewMessage(TypeSystem, ActionPing, map[string]string{"probe": "1"})
	if err != nil {
		t.Fatalf("构造 ping 失败: %v", err)
	}
	pingFrame := recv.send(t, ctx, conn, ping)
	recv.read(t, ctx, conn, "pong", func(msg *Message) bool {
		return msg.Type == TypeSystem && msg.Action == ActionPong && msg.ReplyTo == ping.ID
	})

	// 客户端收到 3 帧（welcome/joined/pong）；写侧计数在写成功后累加，
	// 故把收发帧数与字节数一并放进收敛条件，避免读到只累加了一半的中间态。
	wantFrames := uint64(recv.frames)
	wantBytesIn := uint64(len(joinFrame) + len(pingFrame))
	wantBytesOut := uint64(recv.bytes)
	stats := waitStats(t, srv, 2*time.Second, "计数器收敛到收发帧数", func(st Stats) bool {
		return st.Counters.MsgsIn == 2 && st.Counters.MsgsOut == wantFrames &&
			st.Counters.BytesIn == wantBytesIn && st.Counters.BytesOut == wantBytesOut
	})
	if stats.Counters.MsgsIn != 2 || stats.Counters.BytesIn != wantBytesIn {
		t.Fatalf("入站计数不符: %+v（期望 2 条 / %d 字节）", stats.Counters, wantBytesIn)
	}
	if stats.Counters.MsgsOut != wantFrames || stats.Counters.BytesOut != wantBytesOut {
		t.Fatalf("出站计数不符: %+v（期望 %d 帧 / %d 字节）", stats.Counters, wantFrames, wantBytesOut)
	}
	if stats.Counters.Connects != 1 {
		t.Fatalf("connects 不符: %d", stats.Counters.Connects)
	}
	if stats.Counters.AuthFailed != 0 || stats.Counters.SlowConsumer != 0 {
		t.Fatalf("鉴权失败/慢消费者计数应为 0: %+v", stats.Counters)
	}
	if stats.Counters.MsgsIn <= before.Counters.MsgsIn || stats.Counters.MsgsOut <= before.Counters.MsgsOut {
		t.Fatalf("计数器未递增: before=%+v after=%+v", before.Counters, stats.Counters)
	}
	if stats.Online.Sessions != 1 || stats.Online.Users != 1 || stats.Online.Rooms != 1 {
		t.Fatalf("在线规模不符: %+v", stats.Online)
	}
	if len(stats.Rooms) != 1 || stats.Rooms[0].Room != "g1" || stats.Rooms[0].Sessions != 1 {
		t.Fatalf("房间快照不符: %+v", stats.Rooms)
	}
	if stats.UptimeSeconds < 0 {
		t.Fatalf("uptime_seconds 不应为负: %d", stats.UptimeSeconds)
	}
}

// TestRoomSize 房间人数查询：已加入返回成员数，未知房间返回 0
func TestRoomSize(t *testing.T) {
	srv, baseURL := newTestServer(t, nil)
	c1, _ := newTestClient(t, baseURL, "u1")
	c2, _ := newTestClient(t, baseURL, "u2")

	if got := srv.RoomSize("unknown"); got != 0 {
		t.Fatalf("未知房间应返回 0: %d", got)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if err := c1.Join(ctx, "g1"); err != nil {
		t.Fatalf("u1 加入房间失败: %v", err)
	}
	if got := srv.RoomSize("g1"); got != 1 {
		t.Fatalf("单人加入后房间人数不符: %d", got)
	}
	if err := c2.Join(ctx, "g1"); err != nil {
		t.Fatalf("u2 加入房间失败: %v", err)
	}
	if got := srv.RoomSize("g1"); got != 2 {
		t.Fatalf("双人加入后房间人数不符: %d", got)
	}
	if err := c1.Leave(ctx, "g1"); err != nil {
		t.Fatalf("u1 离开房间失败: %v", err)
	}
	if got := srv.RoomSize("g1"); got != 1 {
		t.Fatalf("一人离开后房间人数不符: %d", got)
	}
	if err := c2.Leave(ctx, "g1"); err != nil {
		t.Fatalf("u2 离开房间失败: %v", err)
	}
	if got := srv.RoomSize("g1"); got != 0 {
		t.Fatalf("全员离开后房间人数不符: %d", got)
	}
}

// TestStatsSessionDetail 会话明细含远端地址与连接时长，并与客户端持有的连接 ID 一致
func TestStatsSessionDetail(t *testing.T) {
	srv, baseURL := newTestServer(t, nil)
	client, _ := newTestClient(t, baseURL, "u1")

	stats := waitStats(t, srv, 2*time.Second, "会话出现在快照中", func(st Stats) bool {
		return len(st.Sessions) == 1 && st.Online.Sessions == 1
	})
	sess := stats.Sessions[0]
	if sess.ID == "" || sess.ID != client.ConnectionID() {
		t.Fatalf("会话 ID 不符: %q != %q", sess.ID, client.ConnectionID())
	}
	if sess.IdentityID != "u1" || sess.IdentityName != "u1" {
		t.Fatalf("会话身份不符: %+v", sess)
	}
	host, port, err := net.SplitHostPort(sess.RemoteAddr)
	if err != nil || host == "" || port == "" {
		t.Fatalf("remote_addr 不符: %q err=%v", sess.RemoteAddr, err)
	}
	connectedAt, err := time.Parse(time.RFC3339, sess.ConnectedAt)
	if err != nil {
		t.Fatalf("connected_at 不是 RFC3339: %q err=%v", sess.ConnectedAt, err)
	}
	if since := time.Since(connectedAt); since < 0 || since > time.Minute {
		t.Fatalf("connected_at 超出合理范围: %s（距今 %s）", sess.ConnectedAt, since)
	}
	if sess.Inflight != 0 {
		t.Fatalf("空闲会话 inflight 应为 0: %d", sess.Inflight)
	}

	time.Sleep(1100 * time.Millisecond)
	stats = waitStats(t, srv, 2*time.Second, "连接时长增长", func(st Stats) bool {
		return len(st.Sessions) == 1 && st.Sessions[0].ConnectedSeconds >= 1
	})
	if stats.Sessions[0].ConnectedAt != sess.ConnectedAt {
		t.Fatalf("同一会话的 connected_at 不应变化: %q != %q", stats.Sessions[0].ConnectedAt, sess.ConnectedAt)
	}
}

// newIdleSession 构造一条不启动读写循环的会话（send 队列只进不出），
// 用于确定性地检验发送队列边界语义（入队/丢最旧/断连判定）。
// conn 取真实拨号的客户端连接，保证慢消费者断连路径（closeNow）可安全执行；
// 该会话不自动注册进 hub、不进任何房间，注册/进房由各测试显式调用。
func newIdleSession(t *testing.T, srv *Server, baseURL, user string, buffer int) *Session {
	t.Helper()

	dialCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(dialCtx, baseURL+"?user="+url.QueryEscape(user), nil)
	if err != nil {
		t.Fatalf("拨号失败: %v", err)
	}
	t.Cleanup(func() { _ = conn.CloseNow() })

	sess := newSession(context.Background(), conn, srv.hub, srv, Identity{ID: user, Name: user}, "127.0.0.1:0", buffer)
	t.Cleanup(func() { sess.closeNow() })
	return sess
}

// TestEnqueueBestEffortDropsOldest 尽力投递：小缓冲会话连续投递不断连、队列满丢最旧只留最新帧；
// 会话关闭后返回 ErrSessionClosed；全程不计慢消费者
func TestEnqueueBestEffortDropsOldest(t *testing.T) {
	srv, baseURL := newTestServer(t, nil)
	sess := newIdleSession(t, srv, baseURL, "be1", 2)

	frames := make([][]byte, 0, 5)
	for i := 0; i < 5; i++ {
		data := []byte(fmt.Sprintf("frame-%d", i))
		frames = append(frames, data)
		if err := sess.enqueueBestEffort(data); err != nil {
			t.Fatalf("尽力投递第 %d 帧失败: %v", i, err)
		}
	}

	select {
	case <-sess.done:
		t.Fatal("尽力投递不应断开会话")
	default:
	}
	if got := len(sess.send); got != 2 {
		t.Fatalf("丢最旧后队列长度不符: %d", got)
	}
	if got := string(<-sess.send); got != string(frames[3]) {
		t.Fatalf("队首应为倒数第二帧: %q，期望 %q", got, frames[3])
	}
	if got := string(<-sess.send); got != string(frames[4]) {
		t.Fatalf("队尾应为最新帧: %q，期望 %q", got, frames[4])
	}
	if got := srv.Stats().Counters.SlowConsumer; got != 0 {
		t.Fatalf("尽力投递不应计入慢消费者: %d", got)
	}

	sess.closeNow()
	if err := sess.enqueueBestEffort([]byte("after-close")); !errors.Is(err, ErrSessionClosed) {
		t.Fatalf("会话关闭后投递应返回 ErrSessionClosed: %v", err)
	}
}

// TestEnqueueBestEffortRetryBounded 队列始终不可入队（容量 0 且无消费者）时重试有界：
// 返回 errBestEffortDropped、不阻塞、不断连
func TestEnqueueBestEffortRetryBounded(t *testing.T) {
	srv, baseURL := newTestServer(t, nil)
	sess := newIdleSession(t, srv, baseURL, "be2", 0)

	if err := sess.enqueueBestEffort([]byte("frame")); !errors.Is(err, errBestEffortDropped) {
		t.Fatalf("不可入队时应返回 errBestEffortDropped: %v", err)
	}
	select {
	case <-sess.done:
		t.Fatal("丢帧不应断开会话")
	default:
	}
}

// TestSendBytesSlowConsumerDisconnect sendBytes 队满语义不回归：视为慢消费者主动断连，
// 计入 slow_consumer；丢最旧只属于尽力投递路径
func TestSendBytesSlowConsumerDisconnect(t *testing.T) {
	srv, baseURL := newTestServer(t, nil)
	sess := newIdleSession(t, srv, baseURL, "sc1", 1)

	if err := sess.sendBytes([]byte("first")); err != nil {
		t.Fatalf("首帧入队失败: %v", err)
	}
	if err := sess.sendBytes([]byte("second")); !errors.Is(err, ErrSlowConsumer) {
		t.Fatalf("队满应返回 ErrSlowConsumer: %v", err)
	}
	select {
	case <-sess.done:
	default:
		t.Fatal("慢消费者应断开会话")
	}
	if got := srv.Stats().Counters.SlowConsumer; got != 1 {
		t.Fatalf("慢消费者计数不符: %d", got)
	}
}

// TestBroadcastRoomBestEffortKeepsSlowSession 房间尽力广播：队满丢最旧、不断连、不计慢消费者；
// 空房间早退返回 0；快照竞态下已关闭会话静默忽略
func TestBroadcastRoomBestEffortKeepsSlowSession(t *testing.T) {
	srv, baseURL := newTestServer(t, nil)
	sess := newIdleSession(t, srv, baseURL, "be3", 1)
	srv.hub.join(sess, "eval")

	send := func(kind string) *Message {
		t.Helper()
		msg, err := NewMessage(TypeGroup, ActionMessage, map[string]string{"kind": kind})
		if err != nil {
			t.Fatalf("构造消息失败: %v", err)
		}
		msg.Room = "eval"
		return msg
	}

	first, second := send("first"), send("second")
	if n := srv.BroadcastRoomBestEffort("eval", first); n != 1 {
		t.Fatalf("首帧尽力广播应命中 1 个会话: %d", n)
	}
	if n := srv.BroadcastRoomBestEffort("eval", second); n != 1 {
		t.Fatalf("队满时应丢最旧后命中: %d", n)
	}
	select {
	case <-sess.done:
		t.Fatal("尽力广播不应断开慢消费者")
	default:
	}
	if got := len(sess.send); got != 1 {
		t.Fatalf("丢最旧后队列应只留 1 帧: %d", got)
	}
	want, err := json.Marshal(second)
	if err != nil {
		t.Fatalf("编码期望帧失败: %v", err)
	}
	if got := string(<-sess.send); got != string(want) {
		t.Fatalf("队内应保留最新帧: %q，期望 %q", got, want)
	}

	if n := srv.BroadcastRoomBestEffort("empty", first); n != 0 {
		t.Fatalf("空房间应早退返回 0: %d", n)
	}

	// 模拟快照竞态：会话已关闭但仍在房间快照中（hub.unregister 之前）——静默忽略、不计数
	sess.closeNow()
	srv.hub.join(sess, "eval")
	if n := srv.BroadcastRoomBestEffort("eval", first); n != 0 {
		t.Fatalf("已关闭会话应被忽略: %d", n)
	}
	if got := srv.Stats().Counters.SlowConsumer; got != 0 {
		t.Fatalf("尽力广播不应计入慢消费者: %d", got)
	}
}

// TestBroadcastRoomDisconnectSemanticsUnchanged 普通房间广播语义未动：
// 有余量即入队且线上帧可核对；队满即视为慢消费者断连（计数 +1、自动退房）
func TestBroadcastRoomDisconnectSemanticsUnchanged(t *testing.T) {
	srv, baseURL := newTestServer(t, nil)

	send := func(room, kind string) *Message {
		t.Helper()
		msg, err := NewMessage(TypeGroup, ActionMessage, map[string]string{"kind": kind})
		if err != nil {
			t.Fatalf("构造消息失败: %v", err)
		}
		msg.Room = room
		return msg
	}

	normal := newIdleSession(t, srv, baseURL, "br1", 4)
	srv.hub.join(normal, "g-normal")
	plain := send("g-normal", "plain")
	if n := srv.BroadcastRoom("g-normal", plain); n != 1 {
		t.Fatalf("普通广播应命中 1 个会话: %d", n)
	}
	want, err := json.Marshal(plain)
	if err != nil {
		t.Fatalf("编码期望帧失败: %v", err)
	}
	if got := string(<-normal.send); got != string(want) {
		t.Fatalf("普通广播入队帧不符: %q，期望 %q", got, want)
	}
	select {
	case <-normal.done:
		t.Fatal("有余量的会话不应被断开")
	default:
	}

	slow := newIdleSession(t, srv, baseURL, "br2", 1)
	srv.hub.join(slow, "g-slow")
	if n := srv.BroadcastRoom("g-slow", send("g-slow", "fill")); n != 1 {
		t.Fatalf("填满队列的广播应命中 1 个会话: %d", n)
	}
	if n := srv.BroadcastRoom("g-slow", send("g-slow", "overflow")); n != 0 {
		t.Fatalf("队满的会话不应命中: %d", n)
	}
	select {
	case <-slow.done:
	default:
		t.Fatal("普通广播队满应断开慢消费者")
	}
	if got := srv.Stats().Counters.SlowConsumer; got != 1 {
		t.Fatalf("慢消费者计数不符: %d", got)
	}
	if got := srv.RoomSize("g-slow"); got != 0 {
		t.Fatalf("断连后应自动退房: %d", got)
	}
}
