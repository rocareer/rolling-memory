// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package mcp

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rocareer/rolling-memory/internal/api"
)

func TestUnixDaemonServeRecallAndStdioProxy(t *testing.T) {
	memory := newMemory(t)
	catText := "老张养了一只橘猫叫大黄。"
	if _, err := memory.Remember(api.RememberRequest{
		Content:    &catText,
		Assertions: []map[string]any{{"s": "老张", "x": "养了", "o": "橘猫"}},
	}); err != nil {
		t.Fatalf("Remember: %v", err)
	}

	sockPath := filepath.Join(t.TempDir(), "engine.sock")
	t.Setenv(DaemonSocketEnv, sockPath)

	done := make(chan error, 1)
	go func() {
		done <- ServeUnix(sockPath, memory, nil)
	}()
	t.Cleanup(func() {
		requestUnixDaemonShutdown(sockPath)
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
	})

	// Wait for the Unix socket to bind.
	deadline := time.Now().Add(2 * time.Second)
	var health *UnixHealthResponse
	for time.Now().Before(deadline) {
		if h, err := PingUnixDaemon(sockPath, 100*time.Millisecond); err == nil && h != nil {
			health = h
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if health == nil || !health.OK {
		t.Fatalf("daemon did not become healthy on %s", sockPath)
	}

	// 1. Direct /v1/recall call (used by CLI and Admin Console).
	resp, err := CallUnixDaemonRecall(context.Background(), sockPath, UnixRecallPayload{
		Cue:           "猫",
		Mode:          "full",
		NoCrystallize: true,
		NeedRendered:  true,
	})
	if err != nil {
		t.Fatalf("CallUnixDaemonRecall: %v", err)
	}
	if !resp.OK || resp.Result == nil || !strings.Contains(resp.Rendered, "<rolling-memory-recall") {
		t.Fatalf("unexpected recall response: %+v", resp)
	}
	meta, _ := resp.Result["meta"].(map[string]any)
	if _, isFloat := meta["candidates"].(float64); isFloat {
		t.Fatalf("daemon response integers must not degrade to float64: %#v", meta["candidates"])
	}

	// 2. DB mismatch returns conflict error so caller can fall back to local open.
	_, err = CallUnixDaemonRecall(context.Background(), sockPath, UnixRecallPayload{
		DB:   filepath.Join(t.TempDir(), "other.db"),
		Cue:  "猫",
		Mode: "full",
	})
	if err == nil || !strings.Contains(err.Error(), "daemon db mismatch") {
		t.Fatalf("expected db mismatch error, got %v", err)
	}

	// 3. Stdio MCP Server with ProxyToDaemon=true forwards `tools/call` over the Unix socket
	//    without opening its own LazyMemory (`lazy.memory` stays nil).
	var out bytes.Buffer
	proxyLazy := NewLazyMemory()
	stdioSrv := &Server{
		In:            strings.NewReader(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"recall","arguments":{"cue":"猫","mode":"probe"}}}` + "\n"),
		Out:           &out,
		Lazy:          proxyLazy,
		ProxyToDaemon: true,
	}
	if err := stdioSrv.Run(); err != nil {
		t.Fatalf("stdio proxy Run: %v", err)
	}
	if proxyLazy.memory != nil {
		t.Fatalf("stdio proxy must not open local LazyMemory when Unix daemon succeeds")
	}
	lines := decodeLines(t, out.String())
	if len(lines) != 1 || isError(t, lines[0]) {
		t.Fatalf("proxied tools/call response: %#v", lines)
	}
}

// TestUnixDaemonDocumentsResolveBackgroundChunkBody 锁定 L1 背景分块正文的读取通道：
// 展示层（Admin 控制台）不能导入 thinclient，只能经守护进程 /v1/documents 取规范正文；
// 守护进程必须从挂载目录的源文件现场重读重切，返回分块正文，而不是让调用方拿路径坐标代替。
func TestUnixDaemonDocumentsResolveBackgroundChunkBody(t *testing.T) {
	memory := newMemory(t)
	tc, err := memory.ThinClient()
	if err != nil || tc == nil || tc.Vault == nil {
		t.Fatalf("thin client: %v", err)
	}
	dir := t.TempDir()
	chunkBody := "# Emergency Failover Runbook\nExecute command `aurora-ctl drain --graceful` before replacing optical transceivers.\n"
	if err := os.WriteFile(filepath.Join(dir, "runbook.md"), []byte(chunkBody), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if _, err := tc.AddBackgroundDir(dir); err != nil {
		t.Fatalf("AddBackgroundDir: %v", err)
	}
	if _, err := tc.SyncBackgroundDirs(context.Background()); err != nil {
		t.Fatalf("SyncBackgroundDirs: %v", err)
	}

	db, err := sql.Open("sqlite", memory.Config.DB)
	if err != nil {
		t.Fatalf("open vault: %v", err)
	}
	var unitID string
	if err := db.QueryRow(`SELECT unit_id FROM thin_vault_bg_chunks LIMIT 1`).Scan(&unitID); err != nil {
		_ = db.Close()
		t.Fatalf("background chunk was not ingested: %v", err)
	}
	_ = db.Close()

	sockDir, err := os.MkdirTemp("/tmp", "rm-doc-")
	if err != nil {
		t.Fatalf("mkdir socket dir: %v", err)
	}
	sockPath := filepath.Join(sockDir, "engine.sock")
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })

	done := make(chan error, 1)
	go func() {
		done <- ServeUnix(sockPath, memory, nil)
	}()
	t.Cleanup(func() {
		requestUnixDaemonShutdown(sockPath)
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
	})
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := PingUnixDaemon(sockPath, 100*time.Millisecond); err == nil {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}

	request := func(payload UnixDocumentPayload) UnixDocumentResponse {
		t.Helper()
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal payload: %v", err)
		}
		client := newUnixHTTPClient(sockPath, 5*time.Second)
		resp, err := client.Post("http://unix/v1/documents", "application/json", bytes.NewReader(raw))
		if err != nil {
			t.Fatalf("POST /v1/documents: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("POST /v1/documents status = %d", resp.StatusCode)
		}
		var out UnixDocumentResponse
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatalf("decode /v1/documents: %v", err)
		}
		return out
	}

	out := request(UnixDocumentPayload{DB: memory.Config.DB, Items: []UnixDocumentItem{{UnitID: unitID}}})
	if !out.OK || len(out.Docs) != 1 {
		t.Fatalf("documents response: %+v", out)
	}
	if out.Docs[0].Error != "" || !strings.Contains(out.Docs[0].RawText, "aurora-ctl drain --graceful") {
		t.Fatalf("chunk body must be the source text, got %+v", out.Docs[0])
	}

	// Unresolvable unit ids are per-item errors, never a batch-wide failure: the display
	// layer keeps its location preview for that row only.
	out = request(UnixDocumentPayload{Items: []UnixDocumentItem{{UnitID: "bg:nonexistent#0"}}})
	if !out.OK || len(out.Docs) != 1 || out.Docs[0].Error == "" {
		t.Fatalf("missing unit must answer with a per-item error: %+v", out)
	}
}
