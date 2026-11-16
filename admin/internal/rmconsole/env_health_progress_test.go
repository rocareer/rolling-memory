package rollingmemory

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRelayAdminSyncProgressSplitsProgressAndDiagnostics(t *testing.T) {
	input := strings.Join([]string{
		`{"progress":{"stage":"vault","status":"start"}}`,
		`diagnostic noise`,
		`{"progress":{"stage":"scan","status":"step","turns":5}}`,
		``,
	}, "\n") + "\n"
	var got []map[string]any
	var errBuf bytes.Buffer
	relayAdminSyncProgress(strings.NewReader(input), func(ev map[string]any) { got = append(got, ev) }, &errBuf)
	if len(got) != 2 || got[0]["stage"] != "vault" || got[1]["stage"] != "scan" {
		t.Fatalf("progress relay mismatch: %#v", got)
	}
	if strings.TrimSpace(errBuf.String()) != "diagnostic noise" {
		t.Fatalf("non-progress lines must land in the error buffer, got %q", errBuf.String())
	}
}

func TestRunAdminSyncCLIStreamsChildProgress(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	shim := filepath.Join(dir, "rolling-memory-shim")
	script := "#!/bin/sh\n" +
		`echo '{"progress":{"stage":"vault","status":"done"}}' >&2` + "\n" +
		`echo '{"progress":{"stage":"scan","status":"step","turns":7}}' >&2` + "\n" +
		`printf '{"ok":true,"progress_env":"%s"}\n' "$ROLLING_MEMORY_STATUS_PROGRESS"` + "\n"
	if err := os.WriteFile(shim, []byte(script), 0o755); err != nil {
		t.Fatalf("write shim: %v", err)
	}
	t.Setenv("ROLLING_MEMORY_BIN", shim)

	var got []map[string]any
	ctx := withEnvHealthProgressSink(context.Background(), func(ev map[string]any) { got = append(got, ev) })
	out, err := runAdminSyncCLI(ctx, "status", filepath.Join(dir, "memory.db"), "", "", 0)
	if err != nil {
		t.Fatalf("runAdminSyncCLI: %v", err)
	}
	if len(got) != 2 || got[0]["stage"] != "vault" || got[1]["stage"] != "scan" {
		t.Fatalf("child progress relay mismatch: %#v", got)
	}
	if !strings.Contains(string(out), `"progress_env":"1"`) {
		t.Fatalf("streaming collection must ask the child for progress events, got %s", out)
	}
	if !strings.Contains(string(out), `"ok":true`) {
		t.Fatalf("stdout payload must pass through unchanged, got %s", out)
	}
}

func TestRunAdminSyncCLIWithoutSinkKeepsBatchSemantics(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	shim := filepath.Join(dir, "rolling-memory-shim")
	script := "#!/bin/sh\n" +
		`printf '{"ok":true,"progress_env":"%s"}\n' "$ROLLING_MEMORY_STATUS_PROGRESS"` + "\n"
	if err := os.WriteFile(shim, []byte(script), 0o755); err != nil {
		t.Fatalf("write shim: %v", err)
	}
	t.Setenv("ROLLING_MEMORY_BIN", shim)

	out, err := runAdminSyncCLI(context.Background(), "status", filepath.Join(dir, "memory.db"), "", "", 0)
	if err != nil {
		t.Fatalf("runAdminSyncCLI: %v", err)
	}
	if !strings.Contains(string(out), `"progress_env":""`) {
		t.Fatalf("batch path must not request progress events, got %s", out)
	}
}

func TestStreamHubBroadcastDeliversProgressFrames(t *testing.T) {
	hub := newStreamHub(streamKey{kind: streamKindEnvHealth}, time.Hour,
		streamFrameProducer{event: "env_health", produce: func(context.Context) (string, any, bool) {
			return "", nil, false
		}},
	)
	sub := hub.subscribe(map[string]bool{"env_health_progress": true})
	defer hub.unsubscribe(sub)

	hub.broadcast("env_health_progress", map[string]any{"stage": "scan", "turns": 7})
	select {
	case frame := <-sub.ch:
		if !strings.HasPrefix(frame, "event: env_health_progress\ndata: ") || !strings.Contains(frame, `"turns":7`) {
			t.Fatalf("unexpected progress frame: %q", frame)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("progress frame not delivered")
	}
}

func TestStreamHubBroadcastSkipsUninterestedSubscribers(t *testing.T) {
	hub := newStreamHub(streamKey{kind: streamKindEnvHealth}, time.Hour,
		streamFrameProducer{event: "env_health", produce: func(context.Context) (string, any, bool) {
			return "", nil, false
		}},
	)
	sub := hub.subscribe(map[string]bool{"env_health": true})
	defer hub.unsubscribe(sub)

	hub.broadcast("env_health_progress", map[string]any{"stage": "scan"})
	select {
	case frame := <-sub.ch:
		t.Fatalf("subscriber without the progress event must not receive it: %q", frame)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestStreamHubBroadcastReplacesOlderProgressFrames(t *testing.T) {
	hub := newStreamHub(streamKey{kind: streamKindEnvHealth}, time.Hour,
		streamFrameProducer{event: "env_health", produce: func(context.Context) (string, any, bool) {
			return "", nil, false
		}},
	)
	sub := hub.subscribe(map[string]bool{"env_health_progress": true})
	defer hub.unsubscribe(sub)

	for i := 1; i <= 3; i++ {
		hub.broadcast("env_health_progress", map[string]any{"turns": i})
	}
	// 队列容量 2：最旧的进度帧被替换，最新进度必须送达，turns=1 应已被挤出。
	var got []string
	for len(got) < 2 {
		select {
		case frame := <-sub.ch:
			got = append(got, frame)
		case <-time.After(500 * time.Millisecond):
			t.Fatalf("expected 2 queued progress frames, got %d", len(got))
		}
	}
	if strings.Contains(got[0], `"turns":1`) {
		t.Fatalf("oldest progress frame must be replaced, got %q", got[0])
	}
	if !strings.Contains(got[1], `"turns":3`) {
		t.Fatalf("newest progress frame must be delivered, got %q", got[1])
	}
}

func TestStreamHubBroadcastKeepsQueuedStateFrames(t *testing.T) {
	hub := newStreamHub(streamKey{kind: streamKindEnvHealth}, time.Hour,
		streamFrameProducer{event: "env_health", produce: func(context.Context) (string, any, bool) {
			return "", nil, false
		}},
	)
	sub := hub.subscribe(map[string]bool{"env_health": true, "env_health_progress": true})
	defer hub.unsubscribe(sub)

	stateFrame := "event: env_health\ndata: {\"marker\":\"state\"}\n\n"
	sub.ch <- stateFrame
	hub.broadcast("env_health_progress", map[string]any{"turns": 1})
	hub.broadcast("env_health_progress", map[string]any{"turns": 2})

	var got []string
	for len(got) < 2 {
		select {
		case frame := <-sub.ch:
			got = append(got, frame)
		case <-time.After(500 * time.Millisecond):
			t.Fatalf("expected 2 queued frames, got %d", len(got))
		}
	}
	stateKept := got[0] == stateFrame || got[1] == stateFrame
	if !stateKept {
		t.Fatalf("queued state frame must be preserved, got %q / %q", got[0], got[1])
	}
	if !strings.Contains(got[0], `"turns":1`) && !strings.Contains(got[1], `"turns":1`) {
		t.Fatalf("already-queued progress frame must survive, got %q / %q", got[0], got[1])
	}
	if strings.Contains(got[0], `"turns":2`) || strings.Contains(got[1], `"turns":2`) {
		t.Fatalf("newest progress frame must yield to the queued state frame, got %q / %q", got[0], got[1])
	}
}

func TestStreamHubRequestRefreshForcesCollection(t *testing.T) {
	var forced atomic.Int32
	var hub *streamHub
	hub = newStreamHub(streamKey{kind: streamKindEnvHealth}, 50*time.Millisecond,
		streamFrameProducer{event: "env_health", produce: func(context.Context) (string, any, bool) {
			if hub.consumeForce() {
				forced.Add(1)
			}
			// 返回恒定签名 "same-sig"：常规巡检在首帧后会因 sig == lastSigs 被去重跳过，
			// 但 RequestRefresh 触发的强制采集必须把数据帧送达订阅者，否则前端 checkingHealth 会永久转圈。
			return "same-sig", map[string]any{"ok": true}, true
		}},
	)
	sub := hub.subscribe(map[string]bool{"env_health": true})
	defer hub.unsubscribe(sub)

	// 消费首帧
	select {
	case <-sub.ch:
	case <-time.After(2 * time.Second):
		t.Fatal("initial frame not delivered")
	}

	hub.RequestRefresh()
	deadline := time.Now().Add(2 * time.Second)
	for forced.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if forced.Load() == 0 {
		t.Fatal("forced collection not observed")
	}
	select {
	case frame := <-sub.ch:
		if !strings.HasPrefix(frame, "event: env_health\ndata: ") {
			t.Fatalf("unexpected forced frame: %q", frame)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("forced refresh must deliver frame even when payload signature is unchanged")
	}
}

func TestStreamHubPeekDoesNotCreate(t *testing.T) {
	key := streamKey{kind: streamKindEnvHealth, harness: "peek-test-missing"}
	if hub := streamHubPeek(key); hub != nil {
		t.Fatalf("peek must not create a hub, got %#v", hub)
	}
}
