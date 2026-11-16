package ws

import (
	"encoding/json"
	"sort"
	"sync"
)

// Hub 服务端连接注册表：维护在线会话、用户连接与群聊房间。
// 所有方法并发安全；广播/直发先在锁内取快照，再在锁外投递，避免慢消费者拖住注册表。
type Hub struct {
	mu       sync.RWMutex
	sessions map[*Session]struct{}
	users    map[string]map[*Session]struct{}
	rooms    map[string]map[*Session]struct{}
}

// newHub 创建连接注册表
func newHub() *Hub {
	return &Hub{
		sessions: make(map[*Session]struct{}),
		users:    make(map[string]map[*Session]struct{}),
		rooms:    make(map[string]map[*Session]struct{}),
	}
}

// register 登记新会话
func (h *Hub) register(s *Session) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.sessions[s] = struct{}{}
	set, ok := h.users[s.identity.ID]
	if !ok {
		set = make(map[*Session]struct{})
		h.users[s.identity.ID] = set
	}
	set[s] = struct{}{}
}

// unregister 注销会话并清理其全部房间关系，返回受影响的房间及剩余人数。
func (h *Hub) unregister(s *Session) map[string]int {
	h.mu.Lock()
	defer h.mu.Unlock()

	delete(h.sessions, s)
	if set, ok := h.users[s.identity.ID]; ok {
		delete(set, s)
		if len(set) == 0 {
			delete(h.users, s.identity.ID)
		}
	}
	leftRooms := make(map[string]int)
	for room, set := range h.rooms {
		if _, in := set[s]; !in {
			continue
		}
		delete(set, s)
		leftRooms[room] = len(set)
		if len(set) == 0 {
			delete(h.rooms, room)
		}
	}
	return leftRooms
}

// join 将会话加入房间，返回加入后的房间人数
func (h *Hub) join(s *Session, room string) int {
	h.mu.Lock()
	defer h.mu.Unlock()

	set, ok := h.rooms[room]
	if !ok {
		set = make(map[*Session]struct{})
		h.rooms[room] = set
	}
	set[s] = struct{}{}
	return len(set)
}

// leave 将会话移出房间，返回剩余人数
func (h *Hub) leave(s *Session, room string) int {
	h.mu.Lock()
	defer h.mu.Unlock()

	set, ok := h.rooms[room]
	if !ok {
		return 0
	}
	delete(set, s)
	n := len(set)
	if n == 0 {
		delete(h.rooms, room)
	}
	return n
}

// inRoom 判断会话是否在房间内
func (h *Hub) inRoom(s *Session, room string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()

	set, ok := h.rooms[room]
	if !ok {
		return false
	}
	_, ok = set[s]
	return ok
}

// roomSize 返回房间人数
func (h *Hub) roomSize(room string) int {
	h.mu.RLock()
	defer h.mu.RUnlock()

	return len(h.rooms[room])
}

// sessionsSnapshot 返回全部会话快照
func (h *Hub) sessionsSnapshot() []*Session {
	h.mu.RLock()
	defer h.mu.RUnlock()

	out := make([]*Session, 0, len(h.sessions))
	for s := range h.sessions {
		out = append(out, s)
	}
	return out
}

// roomSnapshot 返回房间内会话快照
func (h *Hub) roomSnapshot(room string) []*Session {
	h.mu.RLock()
	defer h.mu.RUnlock()

	set := h.rooms[room]
	out := make([]*Session, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	return out
}

// userSnapshot 返回指定用户的全部会话快照
func (h *Hub) userSnapshot(userID string) []*Session {
	h.mu.RLock()
	defer h.mu.RUnlock()

	set := h.users[userID]
	out := make([]*Session, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	return out
}

// broadcastRoom 向房间内全部会话投递同一条消息，返回成功入队的会话数
func (h *Hub) broadcastRoom(room string, msg *Message) int {
	targets := h.roomSnapshot(room)
	if len(targets) == 0 {
		return 0
	}
	msg.ensureMeta()
	data, err := json.Marshal(msg)
	if err != nil {
		return 0
	}
	n := 0
	for _, s := range targets {
		if s.sendBytes(data) == nil {
			n++
		}
	}
	return n
}

// broadcastRoomBestEffort 尽力投递版房间广播：队列满时丢最旧帧、不断连；
// 会话已关闭/未命中直接忽略（不计慢消费者、不主动断开），返回成功入队的会话数。
// 与 broadcastRoom 同为「锁内取快照、锁外投递」，空房间早退、单次 marshal。
func (h *Hub) broadcastRoomBestEffort(room string, msg *Message) int {
	targets := h.roomSnapshot(room)
	if len(targets) == 0 {
		return 0
	}
	msg.ensureMeta()
	data, err := json.Marshal(msg)
	if err != nil {
		return 0
	}
	n := 0
	for _, s := range targets {
		if s.enqueueBestEffort(data) == nil {
			n++
		}
	}
	return n
}

// sendToUser 向指定用户的全部会话投递消息，返回成功入队的会话数
func (h *Hub) sendToUser(userID string, msg *Message) int {
	targets := h.userSnapshot(userID)
	if len(targets) == 0 {
		return 0
	}
	msg.ensureMeta()
	data, err := json.Marshal(msg)
	if err != nil {
		return 0
	}
	n := 0
	for _, s := range targets {
		if s.sendBytes(data) == nil {
			n++
		}
	}
	return n
}

// counts 返回 (在线连接数, 在线用户数)
func (h *Hub) counts() (int, int) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	return len(h.sessions), len(h.users)
}

// roomStats 返回全部房间的在线人数快照：按人数降序、房名升序（无房间时为空切片）。
// 只读快照，复用既有 RWMutex，不新增锁也不改变锁顺序。
func (h *Hub) roomStats() []RoomStat {
	h.mu.RLock()
	out := make([]RoomStat, 0, len(h.rooms))
	for room, set := range h.rooms {
		out = append(out, RoomStat{Room: room, Sessions: len(set)})
	}
	h.mu.RUnlock()

	sort.Slice(out, func(i, j int) bool {
		if out[i].Sessions != out[j].Sessions {
			return out[i].Sessions > out[j].Sessions
		}
		return out[i].Room < out[j].Room
	})
	return out
}

// sessionStats 返回会话明细快照：按连接时刻倒序、同一时刻按连接 ID 升序，最多 limit 条
// （limit <= 0 表示不限；无会话时为空切片）。
// 锁内只做会话拷贝，明细字段（id/身份/远端地址/连接时刻）在锁外读取——它们创建后不再写入，
// 既避免锁内再去获取会话自身的互斥量，也不改变既有锁顺序。
func (h *Hub) sessionStats(limit int) []SessionStat {
	sessions := h.sessionsSnapshot()

	sort.Slice(sessions, func(i, j int) bool {
		if !sessions[i].connectedAt.Equal(sessions[j].connectedAt) {
			return sessions[i].connectedAt.After(sessions[j].connectedAt)
		}
		return sessions[i].id < sessions[j].id
	})
	if limit > 0 && len(sessions) > limit {
		sessions = sessions[:limit]
	}

	out := make([]SessionStat, 0, len(sessions))
	for _, sess := range sessions {
		out = append(out, sess.stat())
	}
	return out
}
