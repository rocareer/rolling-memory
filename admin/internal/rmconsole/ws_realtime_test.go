// plugin: rolling-memory v0.0.58
package rollingmemory

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	wsengine "github.com/ai-go-hub/ai-go-admin/pkg/ws"

	"github.com/coder/websocket"
	"github.com/gin-gonic/gin"
)

// TestWSRealtimeRoomJoinAndPush 锁定同源 pkg/ws 实时通道契约：
// 客户端连接 /ws 并 join 房间（rm/status、rm/env_health）后，
// 可在同一条 WebSocket 连接上多路复用收到各房间的实时帧。
func TestWSRealtimeRoomJoinAndPush(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ROLLING_MEMORY_MODE", "local")
	dbPath := filepath.Join(home, "ws_test.db")
	t.Setenv("ROLLING_MEMORY_DB", dbPath)
	seedVault(t, dbPath,
		`INSERT INTO thin_vault_docs(doc_id, layer, occurred_at, raw_text, address_uri)
		 VALUES ('ws-doc-1', 'episodic', 1793270000, 'ws realtime test doc', 'C6:L1:LOCAL#ws-doc-1')`)

	prevExec := executeAdminSyncCLI
	t.Cleanup(func() { executeAdminSyncCLI = prevExec })
	executeAdminSyncCLI = func(_ context.Context, _, _, _, _ string, _ int) ([]byte, error) {
		return []byte(`{"mode":"local","active_harness_id":"zcode","active_harness_label":"ZCode","health_score":95,"harnesses":[{"id":"zcode","label":"ZCode"}]}`), nil
	}

	testSrv, err := wsengine.NewServer(wsengine.Options{
		ReadLimit:    1 << 20,
		SendBuffer:   64,
		PingInterval: 25 * time.Second,
		WriteTimeout: 10 * time.Second,
		AcceptOptions: &websocket.AcceptOptions{
			InsecureSkipVerify: true,
		},
		Authenticator: func(_ *http.Request) (wsengine.Identity, error) {
			return wsengine.Identity{ID: "1", Name: "rolling"}, nil
		},
		OnRoomJoin:  onRMWSRoomJoin,
		OnRoomLeave: onRMWSRoomLeave,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	prevServer := rmWSServer
	rmWSServer = testSrv
	t.Cleanup(func() {
		rmWSServer = prevServer
	})

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.GET("/ws", func(c *gin.Context) {
		testSrv.ServeHTTP(c.Writer, c.Request)
	})
	ts := httptest.NewServer(engine)
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	wsURL := "ws" + ts.URL[len("http"):] + "/ws?token=test-token"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("websocket.Dial: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	joinCmd, _ := json.Marshal(map[string]any{
		"type":   "group",
		"action": "join",
		"room":   wsRoomEnvHealth,
	})
	if err := conn.Write(ctx, websocket.MessageText, joinCmd); err != nil {
		t.Fatalf("write join: %v", err)
	}

	gotEnvHealth := false
	for i := 0; i < 12; i++ {
		_, raw, rErr := conn.Read(ctx)
		if rErr != nil {
			t.Fatalf("read frame: %v", rErr)
		}
		var msg wsengine.Message
		if err := json.Unmarshal(raw, &msg); err != nil {
			t.Fatalf("unmarshal ws message: %v", err)
		}
		if msg.Type == wsengine.TypeGroup && msg.Action == wsengine.ActionMessage && msg.Room == wsRoomEnvHealth {
			var env struct {
				Event string         `json:"event"`
				Data  map[string]any `json:"data"`
			}
			if err := json.Unmarshal(msg.Data, &env); err != nil {
				t.Fatalf("unmarshal envelope: %v", err)
			}
			if env.Event == "env_health" && env.Data["health_score"] == float64(95) {
				gotEnvHealth = true
				break
			}
		}
	}
	if !gotEnvHealth {
		t.Fatalf("did not receive env_health frame over WebSocket")
	}
}

func TestParseWSRoomSpec_ROIScopeAndHarness(t *testing.T) {
	key, events, ok := parseWSRoomSpec("rm/roi:mem:self:zcode")
	if !ok {
		t.Fatalf("expected rm/roi:mem:self:zcode to parse")
	}
	if key.kind != streamKindROI || key.scope != "mem:self" || key.harness != "zcode" {
		t.Fatalf("unexpected key: %+v", key)
	}
	if len(events) != 1 || events[0] != "roi" {
		t.Fatalf("unexpected events: %+v", events)
	}
}
