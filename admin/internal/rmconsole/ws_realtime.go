// plugin: rolling-memory v0.0.58
package rollingmemory

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ai-go-hub/ai-go-admin/internal/middleware"
	wsengine "github.com/ai-go-hub/ai-go-admin/pkg/ws"

	"github.com/coder/websocket"
	"github.com/gin-gonic/gin"
)

// WebSocket 实时房间命名契约（基于 pkg/ws + MonitorSocket 单连接多房间总线）：
//
//	rm/status                → 状态帧（status）
//	rm/status/probe          → 状态帧 + 1Hz 浮标探针帧（status, probe）
//	rm/dashboard[:<scope>]   → 实时控制台全量帧（dashboard）
//	rm/env_health[:<harness>]→ 环境健康诊断帧 + 阶段进度帧（env_health, env_health_progress）
//	rm/terminal[:<limit>]            → 实时终端流帧（terminal）
//	rm/roi[:<scope>[:<harness>]]     → ROI 监控事实帧（roi）
//	rm/eval                          → 评测实验室实时进度帧（eval_progress）
const (
	wsRoomStatus      = "rm/status"
	wsRoomStatusProbe = "rm/status/probe"
	wsRoomDashboard   = "rm/dashboard"
	wsRoomEnvHealth   = "rm/env_health"
	wsRoomTerminal    = "rm/terminal"
	wsRoomROI         = "rm/roi"
	wsRoomEval        = "rm/eval"
)

type wsRoomBridge struct {
	hub  *streamHub
	sub  *streamSub
	stop chan struct{}
}

var (
	rmWSOnce    sync.Once
	rmWSServer  *wsengine.Server
	rmWSRoomsMu sync.Mutex
	rmWSRooms   = map[string]*wsRoomBridge{}
)

// wsFrameEnvelope 是房间广播消息 data 字段的统一载荷：携带事件名（如 status / probe / env_health / env_health_progress）与原始数据。
type wsFrameEnvelope struct {
	Event string          `json:"event"`
	Data  json.RawMessage `json:"data"`
}

func getRMWSServer() *wsengine.Server {
	rmWSOnce.Do(func() {
		srv, err := wsengine.NewServer(wsengine.Options{
			ReadLimit:    1 << 20,
			SendBuffer:   64,
			PingInterval: 25 * time.Second,
			WriteTimeout: 10 * time.Second,
			AcceptOptions: &websocket.AcceptOptions{
				InsecureSkipVerify: true,
			},
			Authenticator: rmWSAuthenticator,
			OnRoomJoin:    onRMWSRoomJoin,
			OnRoomLeave:   onRMWSRoomLeave,
		})
		if err == nil {
			rmWSServer = srv
		}
	})
	return rmWSServer
}

// rmWSAuthenticator 与 internal/handler/common/ws.go 的 wsAuthenticator 同构：
// 从 ?token= 或 Authorization: Bearer 提取管理员令牌并校验会话。
func rmWSAuthenticator(r *http.Request) (wsengine.Identity, error) {
	raw := strings.TrimSpace(r.URL.Query().Get("token"))
	if raw == "" {
		parts := strings.SplitN(r.Header.Get("Authorization"), " ", 2)
		if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
			raw = strings.TrimSpace(parts[1])
		}
	}
	session, errMsg := middleware.AuthenticateToken(r.Context(), raw)
	if session == nil {
		if errMsg == "" {
			errMsg = "请先登录"
		}
		return wsengine.Identity{}, errors.New(errMsg)
	}
	name := session.Nickname
	if name == "" {
		name = session.Username
	}
	return wsengine.Identity{
		ID:   strconv.FormatUint(uint64(session.ID), 10),
		Name: name,
		Meta: map[string]any{"username": session.Username},
	}, nil
}

// parseWSRoomSpec 将房间名解析为底层 streamHub 的 (streamKey, events)。
func parseWSRoomSpec(room string) (streamKey, []string, bool) {
	room = strings.TrimSpace(room)
	switch {
	case room == wsRoomStatus:
		return streamKey{kind: streamKindStatus}, []string{"status"}, true
	case room == wsRoomStatusProbe:
		return streamKey{kind: streamKindStatus}, []string{"status", "probe"}, true
	case room == wsRoomDashboard || strings.HasPrefix(room, wsRoomDashboard+":"):
		scope := strings.TrimPrefix(room, wsRoomDashboard)
		scope = strings.TrimPrefix(scope, ":")
		if scope == "" {
			scope = UserScope
		}
		return streamKey{kind: streamKindDashboard, scope: scope}, []string{"dashboard"}, true
	case room == wsRoomEnvHealth || strings.HasPrefix(room, wsRoomEnvHealth+":"):
		harness := strings.TrimPrefix(room, wsRoomEnvHealth)
		harness = strings.TrimPrefix(harness, ":")
		return streamKey{kind: streamKindEnvHealth, harness: strings.TrimSpace(harness)}, []string{"env_health", "env_health_progress"}, true
	case room == wsRoomTerminal || strings.HasPrefix(room, wsRoomTerminal+":"):
		rawLimit := strings.TrimPrefix(room, wsRoomTerminal)
		rawLimit = strings.TrimPrefix(rawLimit, ":")
		limit, _ := strconv.Atoi(rawLimit)
		if limit <= 0 {
			limit = 50
		}
		if limit > 120 {
			limit = 120
		}
		return streamKey{kind: streamKindTerminal, limit: limit}, []string{"terminal"}, true
	case room == wsRoomROI || strings.HasPrefix(room, wsRoomROI+":"):
		rest := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(room, wsRoomROI), ":"))
		scope, harness := rest, ""
		if idx := strings.IndexByte(rest, '@'); idx >= 0 {
			scope = strings.TrimSpace(rest[:idx])
			harness = strings.TrimSpace(rest[idx+1:])
		} else if strings.HasPrefix(rest, "mem:self:") {
			scope = "mem:self"
			harness = strings.TrimSpace(strings.TrimPrefix(rest, "mem:self:"))
		} else if idx := strings.IndexByte(rest, ':'); idx >= 0 && !strings.HasPrefix(rest, "mem:") {
			scope = strings.TrimSpace(rest[:idx])
			harness = strings.TrimSpace(rest[idx+1:])
		}
		if scope == "" {
			scope = UserScope
		}
		return streamKey{kind: streamKindROI, scope: scope, harness: harness}, []string{"roi"}, true
	case room == wsRoomEval || strings.HasPrefix(room, wsRoomEval+":"):
		return streamKey{kind: streamKindEval}, []string{"eval_progress", "eval_done"}, true
	default:
		return streamKey{}, nil, false
	}
}

func splitHubEventData(frame string) (string, json.RawMessage, bool) {
	var ev, dataLine string
	for _, line := range strings.Split(strings.TrimSpace(frame), "\n") {
		if strings.HasPrefix(line, "event: ") {
			ev = strings.TrimSpace(strings.TrimPrefix(line, "event: "))
		} else if strings.HasPrefix(line, "data: ") {
			dataLine = strings.TrimSpace(strings.TrimPrefix(line, "data: "))
		}
	}
	if ev == "" || dataLine == "" {
		return "", nil, false
	}
	return ev, json.RawMessage(dataLine), true
}

func broadcastWSRoomFrame(srv *wsengine.Server, room, frame string) {
	if srv == nil {
		return
	}
	ev, rawData, ok := splitHubEventData(frame)
	if !ok {
		return
	}
	msg, err := wsengine.NewMessage(wsengine.TypeGroup, wsengine.ActionMessage, wsFrameEnvelope{
		Event: ev,
		Data:  rawData,
	})
	if err != nil {
		return
	}
	msg.Room = room
	srv.BroadcastRoomBestEffort(room, msg)
}

func onRMWSRoomJoin(room string, _ int) {
	key, events, ok := parseWSRoomSpec(room)
	if !ok {
		return
	}
	wanted := make(map[string]bool, len(events))
	for _, ev := range events {
		wanted[ev] = true
	}

	rmWSRoomsMu.Lock()
	existing := rmWSRooms[room]
	if existing != nil {
		existingHub := existing.hub
		rmWSRoomsMu.Unlock()
		// 房间已在运行：立即向房间补发一次已缓存的最新帧，使新加入的客户端 0 等待拿到首帧。
		existingHub.mu.Lock()
		initial := existingHub.snapshotLocked(wanted)
		existingHub.mu.Unlock()
		for _, frame := range initial {
			broadcastWSRoomFrame(rmWSServer, room, frame)
		}
		return
	}

	// 在持有 rmWSRoomsMu 期间获取并订阅 streamHub，防止与并发的 onRMWSRoomLeave 交错导致订阅到已从
	// streamHubs 注册表摘除的孤儿 hub（onRMWSRoomLeave 释放 rmWSRoomsMu 后才调用 unsubscribe）。
	hub := streamHubFor(key)
	if hub == nil {
		rmWSRoomsMu.Unlock()
		return
	}
	sub := hub.subscribe(wanted)
	stopCh := make(chan struct{})
	rmWSRooms[room] = &wsRoomBridge{hub: hub, sub: sub, stop: stopCh}
	rmWSRoomsMu.Unlock()

	go func(roomName string, s *streamSub, stop <-chan struct{}) {
		for {
			select {
			case <-stop:
				return
			case frame, ok := <-s.ch:
				if !ok {
					return
				}
				broadcastWSRoomFrame(rmWSServer, roomName, frame)
			}
		}
	}(room, sub, stopCh)
}

func onRMWSRoomLeave(room string, remaining int) {
	if remaining > 0 {
		return
	}
	rmWSRoomsMu.Lock()
	entry := rmWSRooms[room]
	if entry != nil {
		delete(rmWSRooms, room)
	}
	rmWSRoomsMu.Unlock()
	if entry != nil {
		close(entry.stop)
		entry.hub.unsubscribe(entry.sub)
	}
}

// rmWSHandler 将 GET /ws 请求交给同源 pkg/ws 引擎完成认证、协议升级与房间订阅分发。
func rmWSHandler(c *gin.Context) {
	srv := getRMWSServer()
	if srv == nil {
		c.Status(http.StatusServiceUnavailable)
		return
	}
	srv.ServeHTTP(c.Writer, c.Request)
}
