package ws

import (
	"sync/atomic"
	"time"
)

// statsSessionLimit sessions 明细快照的最大条数
const statsSessionLimit = 100

// Stats 服务端可观测性只读快照；JSON 字段名即监控契约，不要改名。
type Stats struct {
	UptimeSeconds int64         `json:"uptime_seconds"`
	Online        OnlineStats   `json:"online"`
	Counters      CounterStats  `json:"counters"`
	Rooms         []RoomStat    `json:"rooms"`
	Sessions      []SessionStat `json:"sessions"`
}

// OnlineStats 在线规模（rooms 只统计非空房间）
type OnlineStats struct {
	Sessions int `json:"sessions"`
	Users    int `json:"users"`
	Rooms    int `json:"rooms"`
}

// CounterStats 累计计数器：自 Server 创建起算，只增不减
type CounterStats struct {
	MsgsIn       uint64 `json:"msgs_in"`
	MsgsOut      uint64 `json:"msgs_out"`
	BytesIn      uint64 `json:"bytes_in"`
	BytesOut     uint64 `json:"bytes_out"`
	Connects     uint64 `json:"connects"`
	AuthFailed   uint64 `json:"auth_failed"`
	SlowConsumer uint64 `json:"slow_consumer"`
}

// RoomStat 单个房间的在线人数
type RoomStat struct {
	Room     string `json:"room"`
	Sessions int    `json:"sessions"`
}

// SessionStat 单条连接的明细快照
type SessionStat struct {
	ID               string `json:"id"`
	IdentityID       string `json:"identity_id"`
	IdentityName     string `json:"identity_name"`
	RemoteAddr       string `json:"remote_addr"`
	ConnectedAt      string `json:"connected_at"`
	ConnectedSeconds int64  `json:"connected_seconds"`
	Inflight         int    `json:"inflight"`
}

// counters 服务端计数器：热路径只做原子加，不引入全局锁
type counters struct {
	msgsIn       atomic.Uint64
	msgsOut      atomic.Uint64
	bytesIn      atomic.Uint64
	bytesOut     atomic.Uint64
	connects     atomic.Uint64
	authFailed   atomic.Uint64
	slowConsumer atomic.Uint64
}

// snapshot 读出一份计数器副本
func (c *counters) snapshot() CounterStats {
	return CounterStats{
		MsgsIn:       c.msgsIn.Load(),
		MsgsOut:      c.msgsOut.Load(),
		BytesIn:      c.bytesIn.Load(),
		BytesOut:     c.bytesOut.Load(),
		Connects:     c.connects.Load(),
		AuthFailed:   c.authFailed.Load(),
		SlowConsumer: c.slowConsumer.Load(),
	}
}

// Stats 返回服务端只读快照：运行时长、在线规模、累计计数、房间列表与会话明细。
//
// rooms 按在线人数降序、房名升序；sessions 按连接时刻倒序（同一时刻按连接 ID 升序），最多 100 条；
// 两者在无数据时为空数组（不是 null）。快照非事务性：各段分别读取，只保证单段内一致。
func (s *Server) Stats() Stats {
	sessions, users := s.hub.counts()
	rooms := s.hub.roomStats()

	return Stats{
		UptimeSeconds: int64(time.Since(s.startedAt).Seconds()),
		Online: OnlineStats{
			Sessions: sessions,
			Users:    users,
			Rooms:    len(rooms),
		},
		Counters: s.counters.snapshot(),
		Rooms:    rooms,
		Sessions: s.hub.sessionStats(statsSessionLimit),
	}
}

// RoomSize 返回房间当前在线人数；房间不存在或已空时为 0
func (s *Server) RoomSize(room string) int {
	return s.hub.roomSize(room)
}

// inflightCount 返回会话内进行中的 AI 请求数
func (s *Session) inflightCount() int {
	s.inflightMu.Lock()
	defer s.inflightMu.Unlock()

	return len(s.inflight)
}

// stat 生成会话明细快照：id/身份/远端地址/连接时刻均为创建后不再写入的只读字段
func (s *Session) stat() SessionStat {
	return SessionStat{
		ID:               s.id,
		IdentityID:       s.identity.ID,
		IdentityName:     s.identity.Name,
		RemoteAddr:       s.remoteAddr,
		ConnectedAt:      s.connectedAt.Format(time.RFC3339),
		ConnectedSeconds: int64(time.Since(s.connectedAt).Seconds()),
		Inflight:         s.inflightCount(),
	}
}
