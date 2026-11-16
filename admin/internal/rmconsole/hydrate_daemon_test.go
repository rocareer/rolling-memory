// plugin: rolling-memory v0.0.58
package rollingmemory

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// startFakeHydrateDaemon 在临时 Unix socket 上模拟引擎守护进程的 `/v1/hydrate` 端点，
// 返回 socket 路径、收到的请求体通道与调用计数。
func startFakeHydrateDaemon(t *testing.T, reply func(refs []string) map[string]string) (string, *atomic.Int64, *[]map[string]any) {
	t.Helper()
	sockDir, err := os.MkdirTemp("/tmp", "rm-admin-hyd-")
	if err != nil {
		t.Fatalf("mkdir socket dir: %v", err)
	}
	sockPath := filepath.Join(sockDir, "engine.sock")
	listener, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen unix: %v", err)
	}
	var calls atomic.Int64
	var payloadMu sync.Mutex
	var payloads []map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/hydrate", func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		var payload map[string]any
		_ = json.Unmarshal(body, &payload)
		payloadMu.Lock()
		payloads = append(payloads, payload)
		payloadMu.Unlock()
		var refs []string
		if items, ok := payload["items"].([]any); ok {
			for _, item := range items {
				if m, ok := item.(map[string]any); ok {
					if ref, ok := m["ref"].(string); ok {
						refs = append(refs, ref)
					}
				}
			}
		}
		texts := reply(refs)
		out := map[string]any{"ok": true}
		list := make([]map[string]any, 0, len(texts))
		for ref, text := range texts {
			list = append(list, map[string]any{"ref": ref, "text": text})
		}
		out["texts"] = list
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	})
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(listener) }()
	t.Cleanup(func() {
		_ = srv.Close()
		_ = os.RemoveAll(sockDir)
	})
	return sockPath, &calls, &payloads
}

// startFakeDocumentsDaemon 在临时 Unix socket 上模拟引擎守护进程的 `/v1/documents` 端点，
// 返回 socket 路径与调用计数。reply 中出现的 unit id 即为守护进程裁决可读（答案正文），
// 未出现即裁决不可读（应答 error，控制台按「正文暂不可读」降级）。
func startFakeDocumentsDaemon(t *testing.T, reply func(unitIDs []string) map[string]string) (string, *atomic.Int64) {
	t.Helper()
	sockDir, err := os.MkdirTemp("/tmp", "rm-admin-doc-")
	if err != nil {
		t.Fatalf("mkdir socket dir: %v", err)
	}
	sockPath := filepath.Join(sockDir, "engine.sock")
	listener, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen unix: %v", err)
	}
	var calls atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/documents", func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		var payload struct {
			Items []struct {
				UnitID string `json:"unit_id"`
			} `json:"items"`
		}
		_ = json.Unmarshal(body, &payload)
		var unitIDs []string
		for _, item := range payload.Items {
			unitIDs = append(unitIDs, item.UnitID)
		}
		texts := reply(unitIDs)
		list := make([]map[string]any, 0, len(unitIDs))
		for _, id := range unitIDs {
			if text, ok := texts[id]; ok {
				list = append(list, map[string]any{"unit_id": id, "raw_text": text})
				continue
			}
			list = append(list, map[string]any{"unit_id": id, "error": "source unavailable"})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "docs": list})
	})
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(listener) }()
	t.Cleanup(func() {
		_ = srv.Close()
		_ = os.RemoveAll(sockDir)
	})
	return sockPath, &calls
}

// TestBackgroundChunkRowUsesDaemonBody 锁定 L1 分块列表行的正文口径：
// 批量预热经守护进程 /v1/documents 一次解析并把源文件正文写进 content，
// 未裁决的行（源文件被移动/删除）保留来源位置预览并显式标记 body_unavailable。
func TestBackgroundChunkRowUsesDaemonBody(t *testing.T) {
	readableID := "bg:readable#0"
	goneID := "bg:gone#0"
	sockPath, calls := startFakeDocumentsDaemon(t, func(unitIDs []string) map[string]string {
		out := map[string]string{}
		for _, id := range unitIDs {
			if id == readableID {
				out[id] = "# Emergency Failover Runbook\nExecute command `aurora-ctl drain --graceful`."
			}
		}
		return out
	})
	t.Setenv("ROLLING_MEMORY_DAEMON_SOCKET", sockPath)
	home := t.TempDir()

	readableRow := map[string]any{
		"src": "chunk", "id": readableID, "layer": "background", "status": "active",
		"rel_path": "docs/runbook.md", "anchor": "L1-7", "abs_path": "/ws/docs/runbook.md", "occurred_at": 1762380000,
	}
	goneRow := map[string]any{
		"src": "chunk", "id": goneID, "layer": "background", "status": "active",
		"rel_path": "docs/retired.md", "anchor": "L1-3", "abs_path": "/ws/docs/retired.md", "occurred_at": 1762380000,
	}
	rows := []map[string]any{readableRow, goneRow}
	warmHydrateLocalDocumentsBatch(home, rows)

	row := layerRowFromSQL(readableRow, home)
	if row.BodyUnavailable || !strings.Contains(row.Content, "aurora-ctl drain --graceful") {
		t.Fatalf("readable chunk row = %+v, want the daemon-resolved chunk body", row)
	}
	if row.Title != "docs/runbook.md #L1-7" {
		t.Fatalf("chunk row title = %q, want the file location", row.Title)
	}
	rowGone := layerRowFromSQL(goneRow, home)
	if !rowGone.BodyUnavailable || rowGone.Content != "[docs/retired.md#L1-3] (/ws/docs/retired.md)" {
		t.Fatalf("source-gone chunk row = %+v, want location preview flagged body_unavailable", rowGone)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("daemon calls = %d, want a single warm batch", got)
	}
	// 缓存命中后重复构造行不得再打守护进程。
	_ = layerRowFromSQL(readableRow, home)
	if got := calls.Load(); got != 1 {
		t.Fatalf("cached chunk row must not call the daemon again, calls = %d", got)
	}
}

// TestLayersGetBackgroundChunkReturnsDaemonBody 锁定 L1 分块详情（layers/get）：
// 正文可读时 content 为源文件正文且不带不可读标记；读取引擎不可用时退化为位置预览 + body_unavailable。
func TestLayersGetBackgroundChunkReturnsDaemonBody(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "bg_detail.db")
	db, err := openWritableVaultDB(dbPath)
	if err != nil {
		t.Fatalf("openWritableVaultDB: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO thin_vault_bg_chunks(unit_id, abs_path, rel_path, chunk_index, anchor, occurred_at, address_uri)
		VALUES ('bg:detail#0', '/ws/docs/plan.md', 'docs/plan.md', 0, 'L1-9', 1762380000, 'C6:L1:BG#00')`); err != nil {
		_ = db.Close()
		t.Fatalf("insert bg_chunk: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close vault: %v", err)
	}
	t.Setenv("ROLLING_MEMORY_DB", dbPath)
	// 固定 HOME：分块正文缓存按 (flavor, home, unit_id) 分键，本测试两个阶段必须落在同一键上，
	// 阶段 ② 才会真正重建「引擎不可用」路径。
	home := t.TempDir()
	t.Setenv("HOME", home)

	decodeRow := func(raw string) ThreeLayerRow {
		t.Helper()
		var envelope struct {
			Code int `json:"code"`
			Data struct {
				Row ThreeLayerRow `json:"row"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
			t.Fatalf("decode layers/get: %v", err)
		}
		if envelope.Code != 0 {
			t.Fatalf("layers/get returned business error: %s", raw)
		}
		return envelope.Data.Row
	}

	// ① 守护进程可用：content 为分块正文。
	sockPath, _ := startFakeDocumentsDaemon(t, func(unitIDs []string) map[string]string {
		return map[string]string{"bg:detail#0": "# Deployment Plan\nStep 1: drain the aurora gateway."}
	})
	t.Setenv("ROLLING_MEMORY_DAEMON_SOCKET", sockPath)
	row := decodeRow(callPluginHandler(t, layersGetHandler,
		"/admin/plugin/rolling-memory/layers/get", `{"id":"bg:detail#0"}`))
	if row.BodyUnavailable || !strings.Contains(row.Content, "drain the aurora gateway") {
		t.Fatalf("layers/get chunk row = %+v, want the canonical chunk body", row)
	}
	if row.RelPath != "docs/plan.md" || row.Anchor != "L1-9" {
		t.Fatalf("layers/get chunk row lost its source location: %+v", row)
	}

	// ② 读取引擎不可用（socket 缺失且守护进程未起）：位置预览 + body_unavailable，绝不冒充正文。
	// 先清掉 ① 写入的正文缓存，否则这里会命中缓存拿不到「不可读」路径。
	hydrateMu.Lock()
	delete(hydrateCache, hydrateCacheKey(hydrateFlavorLocalDoc, home, "bg:detail#0"))
	hydrateMu.Unlock()
	t.Setenv("ROLLING_MEMORY_DAEMON_SOCKET", "")
	t.Setenv("ROLLING_MEMORY_DAEMON", "0")
	row = decodeRow(callPluginHandler(t, layersGetHandler,
		"/admin/plugin/rolling-memory/layers/get", `{"id":"bg:detail#0"}`))
	if !row.BodyUnavailable || row.Content != "[docs/plan.md#L1-9] (/ws/docs/plan.md)" {
		t.Fatalf("engine-down chunk row = %+v, want location preview flagged body_unavailable", row)
	}
}

// TestMemoryDetailBackgroundChunkReturnsDaemonBody 锁定检索调试台详情（memories/detail）的
// L1 分块分支与列表同一口径：正文可读时给出正文，不可读时以位置预览兜底。
func TestMemoryDetailBackgroundChunkReturnsDaemonBody(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "bg_mem_detail.db")
	db, err := openWritableVaultDB(dbPath)
	if err != nil {
		t.Fatalf("openWritableVaultDB: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO thin_vault_bg_chunks(unit_id, abs_path, rel_path, chunk_index, anchor, occurred_at, address_uri)
		VALUES ('bg:mem#0', '/ws/docs/notes.md', 'docs/notes.md', 0, 'L1-5', 1762380000, 'C6:L1:BG#01')`); err != nil {
		_ = db.Close()
		t.Fatalf("insert bg_chunk: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close vault: %v", err)
	}
	t.Setenv("ROLLING_MEMORY_DB", dbPath)

	sockPath, _ := startFakeDocumentsDaemon(t, func(unitIDs []string) map[string]string {
		return map[string]string{"bg:mem#0": "Meeting notes: rotate the optical transceivers on Friday."}
	})
	t.Setenv("ROLLING_MEMORY_DAEMON_SOCKET", sockPath)
	home := t.TempDir()
	t.Setenv("HOME", home)

	det, err := queryMemoryDetailFromDB(dbPath, "bg:mem#0")
	if err != nil {
		t.Fatalf("queryMemoryDetailFromDB: %v", err)
	}
	if !strings.Contains(det.TraceText, "rotate the optical transceivers") ||
		!strings.Contains(det.Item.Object, "rotate the optical transceivers") {
		t.Fatalf("memory detail must carry the canonical chunk body: %+v", det)
	}
	if det.TraceAnchor != "[docs/notes.md#L1-5] (/ws/docs/notes.md)" {
		t.Fatalf("memory detail trace anchor = %q, want the source location", det.TraceAnchor)
	}
}

// TestHydratedPointerPrefersDaemonCanonicalText 锁定控制台水合的单条前台路径：
// 命中守护进程时直接采用规范正文（并写入缓存），重复读取不再触发 socket 调用。
func TestHydratedPointerPrefersDaemonCanonicalText(t *testing.T) {
	ref := "@ref:zcode:sess_fixture:msg_turn_1#1"
	sockPath, calls, payloads := startFakeHydrateDaemon(t, func(refs []string) map[string]string {
		out := map[string]string{}
		for _, r := range refs {
			if r == ref {
				out[r] = "[assistant] 规范分段正文"
			}
		}
		return out
	})
	t.Setenv("ROLLING_MEMORY_DAEMON_SOCKET", sockPath)
	home := t.TempDir()

	text, ok := hydratedEpisodicPointerLive(home, ref)
	if !ok || text != "[assistant] 规范分段正文" {
		t.Fatalf("daemon hydration = (%q, %v), want canonical text", text, ok)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("daemon calls = %d, want 1", got)
	}
	if _, ok := hydratedEpisodicPointerLive(home, ref); !ok {
		t.Fatalf("second read should be served from cache")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("cached read must not call the daemon again, calls = %d", got)
	}
	if len(*payloads) != 1 {
		t.Fatalf("payloads = %d, want 1", len(*payloads))
	}
	items, _ := (*payloads)[0]["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1", len(items))
	}
}

// TestWarmHydrateBatchUsesDaemonAndTracksUnanswered 锁定批量预热路径：
// 守护进程裁决的指针直接入缓存，未裁决的指针交回本地兜底扫描（此处在空 HOME 下解析为空并负缓存）。
func TestWarmHydrateBatchUsesDaemonAndTracksUnanswered(t *testing.T) {
	refAnswered := "@ref:zcode:sess_fixture:msg_turn_2#0"
	refUnanswered := "@ref:jsonl:/nonexistent/transcript.jsonl:0#0"
	refLegacy := "@ref:zcode:sess_fixture:msg_turn_2#7"
	sockPath, calls, _ := startFakeHydrateDaemon(t, func(refs []string) map[string]string {
		out := map[string]string{}
		for _, r := range refs {
			switch r {
			case refAnswered:
				out[r] = "[user] 问题"
			case refLegacy:
				out[r] = "" // 规范判定不可用：越界/缺失
			}
		}
		return out
	})
	t.Setenv("ROLLING_MEMORY_DAEMON_SOCKET", sockPath)
	home := t.TempDir()

	warmHydrateZCodePointersBatch(home, []map[string]any{
		{"content": refAnswered},
		{"content": refAnswered}, // 重复指针只请求一次
		{"content": refUnanswered},
		{"content": refLegacy},
	})
	if got := calls.Load(); got != 1 {
		t.Fatalf("daemon calls = %d, want a single batch", got)
	}
	if text, ok := readHydrateCache(hydrateCacheKey(hydrateFlavorLive, home, refAnswered)); !ok || text != "[user] 问题" {
		t.Fatalf("answered ref cache = (%q, %v), want canonical text", text, ok)
	}
	if text, ok := readHydrateCache(hydrateCacheKey(hydrateFlavorRaw, home, refAnswered)); !ok || text != "[user] 问题" {
		t.Fatalf("answered ref raw cache = (%q, %v), want canonical text", text, ok)
	}
	// 守护进程已明确裁决为空时必须负缓存，绝不能再由本地副本用整段合并文本顶上。
	if text, ok := readHydrateCache(hydrateCacheKey(hydrateFlavorLive, home, refLegacy)); !ok || text != "" {
		t.Fatalf("daemon-declared-empty ref cache = (%q, %v), want negative cache", text, ok)
	}
	// 未被守护进程裁决的指针必须落回本地兜底（空 HOME 下解析为空并负缓存），
	// 而不是永远停在未命中状态反复重试。
	if _, ok := readHydrateCache(hydrateCacheKey(hydrateFlavorLive, home, refUnanswered)); !ok {
		t.Fatalf("unanswered ref must fall through to the local fallback and get cached")
	}
}

// TestHydrateFallsBackWhenDaemonSocketMissing 锁定守护进程缺失时的降级：
// 不路由、不报错，由本地兜底解析（空 HOME 下为空），保证面板与检索不被拖垮。
func TestHydrateFallsBackWhenDaemonSocketMissing(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "missing-engine.sock")
	t.Setenv("ROLLING_MEMORY_DAEMON_SOCKET", sockPath)
	home := t.TempDir()
	ref := "@ref:zcode:sess_missing:msg_missing#0"

	done := make(chan struct{})
	var text string
	var ok bool
	go func() {
		text, ok = hydratedEpisodicPointerLive(home, ref)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("hydration must not block when the daemon socket is missing")
	}
	if ok || text != "" {
		t.Fatalf("missing daemon fallback = (%q, %v), want empty unresolved", text, ok)
	}
}

// TestHydrateWorkerLoopPrefersDaemon 锁定异步预热 worker 与前台同一口径：
// 入队指针必须优先向守护进程取规范正文，落盘缓存后由 raw 风味缓存消费方读取。
func TestHydrateWorkerLoopPrefersDaemon(t *testing.T) {
	ref := "@ref:zcode:sess_worker:msg_worker_1#2"
	sockPath, calls, _ := startFakeHydrateDaemon(t, func(refs []string) map[string]string {
		out := map[string]string{}
		for _, r := range refs {
			if r == ref {
				out[r] = "[assistant] worker 规范正文"
			}
		}
		return out
	})
	t.Setenv("ROLLING_MEMORY_DAEMON_SOCKET", sockPath)
	home := t.TempDir()

	scheduleHydrateWarm(hydrateJob{flavor: hydrateFlavorRaw, home: home, pointer: ref})
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if text, ok := readHydrateCache(hydrateCacheKey(hydrateFlavorRaw, home, ref)); ok && text == "[assistant] worker 规范正文" {
			if calls.Load() == 0 {
				t.Fatalf("worker filled cache without asking the daemon")
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("worker did not fill canonical text from the daemon")
}

// TestTerminalStreamLinesHydratePointers 锁定 Live Console 终端回显：
// 最近写入单元的 `@ref:` 指针必须展示水合后的正文，而不是指针字符串本身。
func TestTerminalStreamLinesHydratePointers(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ref := "@ref:zcode:sess_term:msg_term_1#0"
	sockPath, _, _ := startFakeHydrateDaemon(t, func(refs []string) map[string]string {
		out := map[string]string{}
		for _, r := range refs {
			if r == ref {
				out[r] = "[user] 终端回显正文"
			}
		}
		return out
	})
	t.Setenv("ROLLING_MEMORY_DAEMON_SOCKET", sockPath)

	dbPath := filepath.Join(t.TempDir(), "memory.db")
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("open vault fixture: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE thin_vault_docs (doc_id TEXT, layer TEXT, occurred_at INTEGER, address_uri TEXT, raw_text TEXT);`); err != nil {
		t.Fatalf("create fixture: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO thin_vault_docs (doc_id, layer, occurred_at, address_uri, raw_text) VALUES ('ep_term_1', 'episodic', 1700000000, 'addr-1', ?);`, ref); err != nil {
		t.Fatalf("seed fixture: %v", err)
	}
	_ = db.Close()

	lines := buildTerminalStreamLines(dbPath, filepath.Join(t.TempDir(), "missing.log"), 4)
	for _, line := range lines {
		if line.ID != "unit-ep_term_1" {
			continue
		}
		if strings.Contains(line.Text, "@ref:") {
			t.Fatalf("terminal echo still prints the raw pointer: %q", line.Text)
		}
		if !strings.Contains(line.Text, "终端回显正文") {
			t.Fatalf("terminal echo missing hydrated text: %q", line.Text)
		}
		return
	}
	t.Fatalf("terminal stream lines did not include the seeded unit: %+v", lines)
}

// TestGraphDisplayTextHydratesPointers 锁定图谱节点摘要/跨度：
// 指针行取规范水合正文，非指针行维持「蒸馏优先、原文兜底」口径。
func TestGraphDisplayTextHydratesPointers(t *testing.T) {
	ref := "@ref:zcode:sess_graph:msg_graph_1#1"
	sockPath, _, _ := startFakeHydrateDaemon(t, func(refs []string) map[string]string {
		out := map[string]string{}
		for _, r := range refs {
			if r == ref {
				out[r] = "[assistant] 图谱规范正文"
			}
		}
		return out
	})
	t.Setenv("ROLLING_MEMORY_DAEMON_SOCKET", sockPath)
	home := t.TempDir()

	txt, span := hydrateEpisodicDisplayText(home, ref, "")
	if txt != "[assistant] 图谱规范正文" || span != "[assistant] 图谱规范正文" {
		t.Fatalf("pointer row display = (%q, %q), want hydrated text", txt, span)
	}
	if txt, span := hydrateEpisodicDisplayText(home, "plain text", "distilled text"); txt != "distilled text" || span != "plain text" {
		t.Fatalf("plain row display = (%q, %q), want distilled/raw passthrough", txt, span)
	}
}

// TestVaultFollowupDocsWarmUncachedPointers 锁定采纳审计语料：
// 冷缓存的零拷贝指针行必须先走规范批量水合再入语料，早期实现只读缓存会把这行静默丢掉。
func TestVaultFollowupDocsWarmUncachedPointers(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ref := "@ref:zcode:sess_follow:msg_follow_1#1"
	sockPath, _, _ := startFakeHydrateDaemon(t, func(refs []string) map[string]string {
		out := map[string]string{}
		for _, r := range refs {
			if r == ref {
				out[r] = "[assistant] 审计语料正文"
			}
		}
		return out
	})
	t.Setenv("ROLLING_MEMORY_DAEMON_SOCKET", sockPath)

	dbPath := filepath.Join(t.TempDir(), "memory.db")
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("open vault fixture: %v", err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(`CREATE TABLE thin_vault_docs (doc_id TEXT, layer TEXT, occurred_at INTEGER, address_uri TEXT, raw_text TEXT, sentences_json TEXT, locus TEXT);`); err != nil {
		t.Fatalf("create fixture: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO thin_vault_docs (doc_id, layer, occurred_at, address_uri, raw_text, sentences_json, locus) VALUES ('ep_follow_1', 'episodic', 1700000200, 'addr-f', ?, '[]', 'locus-f');`, ref); err != nil {
		t.Fatalf("seed fixture: %v", err)
	}

	rows, _ := loadVaultFollowupDocs(context.Background(), db, t.TempDir(), 0)
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want the uncached pointer row to survive hydration", len(rows))
	}
	if rows[0].text != "[assistant] 审计语料正文" || rows[0].docID != "ep_follow_1" {
		t.Fatalf("row = %+v, want canonical hydrated text", rows[0])
	}
}

// TestUnresolvableRefShowsGapMarker 锁定诚实占位：无法水合的零拷贝指针绝不以正文身份上屏，
// 展示面回「来源缺口」占位（与产品 recent 同一措辞）。
func TestUnresolvableRefShowsGapMarker(t *testing.T) {
	ref := "@ref:zcode:sess_pruned:msg_pruned_1#0"
	sockPath, _, _ := startFakeHydrateDaemon(t, func(refs []string) map[string]string {
		out := map[string]string{}
		for _, r := range refs {
			out[r] = "" // 规范判定：宿主原文已清理，不可水合
		}
		return out
	})
	t.Setenv("ROLLING_MEMORY_DAEMON_SOCKET", sockPath)
	t.Setenv("HOME", t.TempDir())
	home := t.TempDir()

	if got := hydrateVaultTextForDisplay(ref); got != vaultRefGapMarker {
		t.Fatalf("display = %q, want gap marker", got)
	}
	if txt, span := hydrateEpisodicDisplayText(home, ref, ""); txt != vaultRefGapMarker || span != vaultRefGapMarker {
		t.Fatalf("graph display = (%q, %q), want gap marker", txt, span)
	}
	if got := hydrateVaultTextForDisplay("普通正文"); got != "普通正文" {
		t.Fatalf("plain text must pass through, got %q", got)
	}
}
