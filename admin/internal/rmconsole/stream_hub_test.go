// plugin: rolling-memory v0.0.58
package rollingmemory

// 后台实时流契约锁定：控制台各面板的实时面一律由共享 streamHub + WebSocket 房间驱动——
// ① 单一 watcher 服务全部订阅（成本不随标签页数量叠加）；
// ② 状态未变不推帧（静默期 0 次子进程 / 0 次 SQL / 0 次重聚合）；
// ③ 状态变化后在下一次巡检周期内推帧。

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type streamTestClient struct {
	mu   sync.Mutex
	buf  strings.Builder
	stop func()
	done chan struct{}
}

func startRoomTestClient(t *testing.T, room string) *streamTestClient {
	t.Helper()
	key, events, ok := parseWSRoomSpec(room)
	if !ok {
		t.Fatalf("invalid ws room spec: %s", room)
	}
	hub := streamHubFor(key)
	if hub == nil {
		t.Fatalf("streamHubFor(%+v) returned nil", key)
	}
	wanted := make(map[string]bool, len(events))
	for _, ev := range events {
		wanted[ev] = true
	}
	sub := hub.subscribe(wanted)
	stopCh := make(chan struct{})
	done := make(chan struct{})
	var stopOnce sync.Once
	stopFn := func() {
		stopOnce.Do(func() {
			close(stopCh)
			hub.unsubscribe(sub)
		})
	}
	client := &streamTestClient{stop: stopFn, done: done}
	go func() {
		defer close(done)
		for {
			select {
			case <-stopCh:
				return
			case frame, ok := <-sub.ch:
				if !ok {
					return
				}
				client.mu.Lock()
				client.buf.WriteString(frame)
				client.mu.Unlock()
			}
		}
	}()
	t.Cleanup(func() {
		stopFn()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Errorf("room test client did not stop in time")
		}
	})
	return client
}

func (c *streamTestClient) frames() []string {
	return strings.Split(c.body(), "\n\n")
}

func (c *streamTestClient) body() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.String()
}

// countEvent 统计已收到的指定事件帧数量（注释帧 `: open` / `: keepalive` 不计入）。
func (c *streamTestClient) countEvent(event string) int {
	n := 0
	for _, frame := range c.frames() {
		if strings.HasPrefix(strings.TrimSpace(frame), "event: "+event+"\n") {
			n++
		}
	}
	return n
}

// lastPayload 返回指定事件最近一帧的 data 载荷。
func (c *streamTestClient) lastPayload(t *testing.T, event string) map[string]any {
	t.Helper()
	var payload map[string]any
	found := false
	for _, frame := range c.frames() {
		trimmed := strings.TrimSpace(frame)
		if !strings.HasPrefix(trimmed, "event: "+event+"\n") {
			continue
		}
		idx := strings.Index(trimmed, "data: ")
		if idx < 0 {
			continue
		}
		raw := strings.TrimSpace(trimmed[idx+len("data: "):])
		if err := json.Unmarshal([]byte(raw), &payload); err != nil {
			t.Fatalf("decode %s frame payload: %v", event, err)
		}
		found = true
	}
	if !found {
		t.Fatalf("no %s frame received; body=%q", event, c.body())
	}
	return payload
}

func waitForFrames(t *testing.T, client *streamTestClient, event string, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if client.countEvent(event) >= want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d %s frame(s); body=%q", want, event, client.body())
}

// TestStreamHubSharesOneWatcherAcrossSubscribers 锁定共享流的成本模型：
// 两个订阅者共享同一个 watcher（采集次数不翻倍）、都能收到同一帧；全部退订后 watcher 停表。
func TestStreamHubSharesOneWatcherAcrossSubscribers(t *testing.T) {
	key := streamKey{kind: "test_shared_watcher"}
	if hub := streamHubFor(key); hub != nil {
		t.Fatalf("unknown kind must not build a hub: %+v", hub)
	}

	var mu sync.Mutex
	collected := 0
	hub := newStreamHub(key, 20*time.Millisecond, streamFrameProducer{
		event:      "tick",
		alwaysPush: true,
		produce: func(context.Context) (string, any, bool) {
			mu.Lock()
			collected++
			n := collected
			mu.Unlock()
			return "", map[string]any{"n": n}, true
		},
	})

	subA := hub.subscribe(map[string]bool{"tick": true})
	subB := hub.subscribe(map[string]bool{"tick": true})

	deadline := time.After(2 * time.Second)
	for {
		mu.Lock()
		n := collected
		mu.Unlock()
		if n >= 5 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("watcher did not run: collected=%d", n)
		case <-time.After(10 * time.Millisecond):
		}
	}

	frameA := <-subA.ch
	frameB := <-subB.ch
	if frameA != frameB {
		t.Fatalf("subscribers must receive the identical frame: %q vs %q", frameA, frameB)
	}

	mu.Lock()
	beforeIdle := collected
	mu.Unlock()
	time.Sleep(200 * time.Millisecond)
	mu.Lock()
	afterIdle := collected
	mu.Unlock()
	// 20ms 巡检周期下 200ms ≈ 10 轮；若两个订阅各起一个 watcher，这里的增量会翻倍。
	if afterIdle-beforeIdle > 14 {
		t.Fatalf("collection count grew %d in 200ms — watcher is not shared", afterIdle-beforeIdle)
	}

	hub.unsubscribe(subA)
	hub.unsubscribe(subB)
	mu.Lock()
	beforeStop := collected
	mu.Unlock()
	time.Sleep(150 * time.Millisecond)
	mu.Lock()
	afterStop := collected
	mu.Unlock()
	if afterStop != beforeStop {
		t.Fatalf("watcher kept running after every subscriber left: %d → %d", beforeStop, afterStop)
	}
}

// TestTerminalStreamPushesOnlyOnLogChange 锁定终端流：首帧立即到达；Hook 日志静默期不重推；
// 追加日志后在下一次巡检周期内推新帧（替代原每 2s 一次的 /terminal 轮询）。
func TestTerminalStreamPushesOnlyOnLogChange(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	logDir := filepath.Join(tmp, ".local", "state", "rolling-memory")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatalf("mkdir state dir: %v", err)
	}
	logPath := filepath.Join(logDir, "rolling-memory.log")
	if err := os.WriteFile(logPath, []byte(`{"event":"recall","outcome":"hit","ts":100}`+"\n"), 0o644); err != nil {
		t.Fatalf("seed hook log: %v", err)
	}
	db, err := openWritableVaultDB(filepath.Join(tmp, ".local", "share", "rolling-memory", "memory.db"))
	if err != nil {
		t.Fatalf("openWritableVaultDB: %v", err)
	}
	defer db.Close()

	client := startRoomTestClient(t, "rm/terminal:50")
	waitForFrames(t, client, "terminal", 1, 3*time.Second)
	time.Sleep(1500 * time.Millisecond)
	if got := client.countEvent("terminal"); got != 1 {
		t.Fatalf("idle terminal stream must not re-push frames, got %d", got)
	}

	file, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open hook log: %v", err)
	}
	if _, err := file.WriteString(`{"event":"recall","outcome":"miss","ts":200}` + "\n"); err != nil {
		t.Fatalf("append hook log: %v", err)
	}
	_ = file.Close()

	waitForFrames(t, client, "terminal", 2, 4*time.Second)
	payload := client.lastPayload(t, "terminal")
	lines, _ := payload["stream_lines"].([]any)
	if len(lines) == 0 {
		t.Fatalf("terminal frame carries no stream_lines: %+v", payload)
	}
}

// TestEnvHealthStreamProbesOnlyOnChange 锁定环境健康流最关键的省钱契约：
// 指纹未变不起子进程（本机实测单次 `sync status` 全量扫描约 22.9s，静默期连轴转会把控制台拖死）；
// 指纹变化且过节流后才重新采集并推帧。
func TestEnvHealthStreamProbesOnlyOnChange(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	// 假引擎二进制：每次调用把计数写进文件，并输出 health_score = 86 + 调用次数
	// （载荷随每轮采集变化，便于把「采集闸」与「推帧闸」分开断言）。
	binPath := filepath.Join(tmp, "fake-rolling-memory.sh")
	counterPath := filepath.Join(tmp, "probe-count")
	script := "#!/bin/sh\n" +
		"n=$(cat " + counterPath + " 2>/dev/null || echo 0)\n" +
		"echo $((n+1)) > " + counterPath + "\n" +
		"echo \"{\\\"active_harness_id\\\":\\\"zcode\\\",\\\"health_score\\\":$((86+n+1)),\\\"total_pending_sync\\\":0}\"\n"
	if err := os.WriteFile(binPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}
	t.Setenv("ROLLING_MEMORY_BIN", binPath)

	restoreMin, restoreSilence := envHealthProbeMinInterval, envHealthMaxSilence
	envHealthProbeMinInterval, envHealthMaxSilence = 50*time.Millisecond, time.Hour
	t.Cleanup(func() {
		envHealthProbeMinInterval, envHealthMaxSilence = restoreMin, restoreSilence
	})

	client := startRoomTestClient(t, "rm/env_health")
	waitForFrames(t, client, "env_health", 1, 5*time.Second)
	first := client.lastPayload(t, "env_health")
	if first["health_score"] != float64(87) {
		t.Fatalf("unexpected env/health payload: %+v", first)
	}

	// 静默期：指纹（配置/金库/Hook 日志/宿主库）没有变化 ⇒ 不再起子进程、不再推帧。
	time.Sleep(1200 * time.Millisecond)
	if probes := readProbeCount(t, counterPath); probes != 1 {
		t.Fatalf("idle env/health stream must not re-probe, probes=%d", probes)
	}
	if got := client.countEvent("env_health"); got != 1 {
		t.Fatalf("idle env/health stream must not re-push frames, got %d", got)
	}

	// 变化：配置文件 mtime/大小变化 ⇒ 指纹变化 ⇒ 越过节流后重新采集并推帧。
	cfgPath := filepath.Join(tmp, ".config", "rolling-memory", "config.toml")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(cfgPath, []byte("[mode]\nname = \"local\"\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	waitForFrames(t, client, "env_health", 2, 5*time.Second)
	if probes := readProbeCount(t, counterPath); probes != 2 {
		t.Fatalf("changed signature must trigger exactly one fresh probe, probes=%d", probes)
	}
	if got := client.lastPayload(t, "env_health")["health_score"]; got != float64(88) {
		t.Fatalf("second frame must carry the re-collected payload, health_score=%v", got)
	}
}

// TestROIStreamPushesOnceAndStaysQuiet 锁定 ROI 流：首帧即给出无台账状态；金库/台账指纹不变时不重聚合。
func TestROIStreamPushesOnceAndStaysQuiet(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	// ROLLING_MEMORY_DB 同时用作「测试库」标记：生产流水导入在测试下直接短路。
	t.Setenv("ROLLING_MEMORY_DB", filepath.Join(tmp, "memory.db"))

	client := startRoomTestClient(t, "rm/roi:default")
	waitForFrames(t, client, "roi", 1, 5*time.Second)
	payload := client.lastPayload(t, "roi")
	if payload["status"] != "unknown" {
		t.Fatalf("expected unknown ROI status without efficacy ledger, got %+v", payload["status"])
	}
	time.Sleep(1500 * time.Millisecond)
	if got := client.countEvent("roi"); got != 1 {
		t.Fatalf("idle ROI stream must not re-aggregate, got %d frames", got)
	}
}

// TestStatusStreamStaysQuietWhileIdle 锁定最贵的全量状态流在静默期不推帧：
// 指纹（配置/金库/WAL/Hook 日志/后台任务）未变时不得重算产物统计帧——
// 这条与 TestStatusStreamPushesOnVaultChange（变化必须推帧）成对，缺任一条都会让实时面退化为定时轮询。
func TestStatusStreamStaysQuietWhileIdle(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	db, err := openWritableVaultDB(filepath.Join(tmp, ".local", "share", "rolling-memory", "memory.db"))
	if err != nil {
		t.Fatalf("openWritableVaultDB: %v", err)
	}
	defer db.Close()

	client := startRoomTestClient(t, "rm/status")
	waitForFrames(t, client, "status", 1, 3*time.Second)
	// 250ms 巡检周期下 1.5s ≈ 6 轮：指纹没有变化时不应有任何新帧。
	time.Sleep(1500 * time.Millisecond)
	if got := client.countEvent("status"); got != 1 {
		t.Fatalf("idle status stream must not re-push frames, got %d", got)
	}
}

// TestStatusStreamProbeFramesPacedAt1Hz 锁定浮标实时曲线房间（rm/status/probe）契约：
// 订阅后先收到全量 status 帧，随后按 1Hz 收到 probe 采样帧。
func TestStatusStreamProbeFramesPacedAt1Hz(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	client := startRoomTestClient(t, "rm/status/probe")
	waitForFrames(t, client, "status", 1, 3*time.Second)
	waitForFrames(t, client, "probe", 1, 3*time.Second)
	firstProbeAt := time.Now()
	waitForFrames(t, client, "probe", 2, 3*time.Second)
	// 第二帧 probe 必须跨过一个 1Hz 节拍：浮标心电曲线的「每格 = 1 秒」依赖这个节拍，
	// 若按 250ms 巡检连发，曲线窗口会被压成 1/4 时长。
	if elapsed := time.Since(firstProbeAt); elapsed < 800*time.Millisecond {
		t.Fatalf("probe frames are not paced at 1Hz: second frame arrived after %s", elapsed)
	}
}

func readProbeCount(t *testing.T, path string) int {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
	return n
}
