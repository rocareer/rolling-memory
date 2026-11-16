// plugin: rolling-memory v0.0.58
package rollingmemory

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// seedVault 用可写连接建库并写入种子行（写连接是测试夹具，请求路径一律只读）。
func seedVault(t *testing.T, dbPath string, seeds ...string) {
	t.Helper()
	db, err := openVaultWriteOnly(dbPath)
	if err != nil {
		t.Fatalf("openVaultWriteOnly: %v", err)
	}
	for _, seed := range seeds {
		if _, err := db.Exec(seed); err != nil {
			_ = db.Close()
			t.Fatalf("seed %q: %v", seed, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close vault: %v", err)
	}
}

// TestLayerListReadPathHasNoSideEffects 锁定 P1 修复：记忆管理列表是只读面 ——
// 金库不存在时不建库、不建表、不落任何写入，并如实下发空列表。
func TestLayerListReadPathHasNoSideEffects(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ROLLING_MEMORY_MODE", "local")
	dbPath := filepath.Join(home, "must_not_be_created.db")
	t.Setenv("ROLLING_MEMORY_DB", dbPath)

	payload := decodeLayersStatsPayload(t, callPluginHandler(t, layersStatsHandler,
		"/admin/plugin/rolling-memory/layers/list", `{"layer":"all","page":1,"limit":20}`))
	if payload.Total != 0 || len(payload.Items) != 0 {
		t.Fatalf("missing vault must yield an empty page, got total=%d items=%d", payload.Total, len(payload.Items))
	}
	if _, err := os.Stat(dbPath); err == nil {
		t.Fatalf("read path must not create the vault file at %s", dbPath)
	}
}

// TestLayerListKeywordAndSingleDayBetween 锁定列表筛选的两处关键口径：
// ① 关键词（页内快搜）在 SQL 侧过滤；② 时间区间按 epoch 数值比较 ——
// 纯日期（YYYY-MM-DD）作上下界时必须能查到「当天」的行（早期实现用字符串比较会全部漏掉）。
func TestLayerListKeywordAndSingleDayBetween(t *testing.T) {
	t.Setenv("ROLLING_MEMORY_MODE", "local")
	dbPath := filepath.Join(t.TempDir(), "layer_list.db")
	t.Setenv("ROLLING_MEMORY_DB", dbPath)
	// 用本机时区构造「同一天 12:00」的两个 epoch，避免把测试钉死在某个时区/年份上。
	dayA := time.Date(2026, 11, 10, 12, 0, 0, 0, time.Local).Unix()
	dayB := time.Date(2026, 11, 11, 12, 0, 0, 0, time.Local).Unix()
	seedVault(t, dbPath,
		fmt.Sprintf(`INSERT INTO thin_vault_docs(doc_id, layer, occurred_at, raw_text, address_uri)
		 VALUES ('doc-alpha', 'episodic', %d, 'alpha login flow', 'C6:L1:B001#00')`, dayA),
		fmt.Sprintf(`INSERT INTO thin_vault_docs(doc_id, layer, occurred_at, raw_text, address_uri)
		 VALUES ('doc-beta', 'episodic', %d, 'beta billing flow', 'C6:L1:B002#00')`, dayB))

	kw := decodeLayersStatsPayload(t, callPluginHandler(t, layersStatsHandler,
		"/admin/plugin/rolling-memory/layers/list", `{"layer":"all","page":1,"limit":20,"keyword":"billing"}`))
	if kw.Total != 1 || len(kw.Items) != 1 || kw.Items[0].ID != "doc-beta" {
		t.Fatalf("keyword pushdown mismatch: total=%d items=%+v", kw.Total, kw.Items)
	}

	day := decodeLayersStatsPayload(t, callPluginHandler(t, layersStatsHandler,
		"/admin/plugin/rolling-memory/layers/list",
		`{"layer":"all","page":1,"limit":20,"wheres":[{"wheres":[{"field":"time_text","operator":"BETWEEN","value":"2026-11-10,2026-11-10"}],"or":false}]}`))
	if day.Total != 1 || len(day.Items) != 1 || day.Items[0].ID != "doc-alpha" {
		t.Fatalf("single-day BETWEEN should return exactly that day's row: total=%d items=%+v", day.Total, day.Items)
	}

	// 未知层名不得被静默放大成全表（历史口径：未知层 → 0 行）。
	unknown := decodeLayersStatsPayload(t, callPluginHandler(t, layersStatsHandler,
		"/admin/plugin/rolling-memory/layers/list", `{"layer":"sideways","page":1,"limit":20}`))
	if unknown.Total != 0 || len(unknown.Items) != 0 {
		t.Fatalf("unknown layer must yield no rows: total=%d items=%+v", unknown.Total, unknown.Items)
	}
}

// TestLayerListHydratedPointerKeywordSearchAndCorruptedDBError 验证：
// ① @ref: 零拷贝指针在水合会话正文后可被关键词与高级筛选检索；
// ② 损坏的 SQLite 文件返回明确错误，不冒充成功空列表。
func TestLayerListHydratedPointerKeywordSearchAndCorruptedDBError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ROLLING_MEMORY_MODE", "local")
	dbPath := filepath.Join(home, "pointer_list.db")
	t.Setenv("ROLLING_MEMORY_DB", dbPath)

	zcodeDir := filepath.Join(home, ".zcode", "cli", "db")
	if err := os.MkdirAll(zcodeDir, 0o755); err != nil {
		t.Fatalf("mkdir zcode: %v", err)
	}
	zdbPath := filepath.Join(zcodeDir, "db.sqlite")
	zdb, err := sql.Open("sqlite", zdbPath)
	if err != nil {
		t.Fatalf("open zdb: %v", err)
	}
	defer zdb.Close()
	_, err = zdb.Exec(`CREATE TABLE message (id TEXT, session_id TEXT, sequence INTEGER, data TEXT); CREATE TABLE part (message_id TEXT, sequence INTEGER, data TEXT)`)
	if err != nil {
		t.Fatalf("create zdb schema: %v", err)
	}
	// sessionID: "sess-1", messageID: "msg-1"
	_, err = zdb.Exec(`INSERT INTO message (id, session_id, sequence, data) VALUES ('msg-1', 'sess-1', 1, '{"role":"user"}')`)
	_, err = zdb.Exec(`INSERT INTO part (message_id, sequence, data) VALUES ('msg-1', 1, '{"type":"text","text":"fix quantum entanglement bug in router"}')`)

	ref := "@ref:zcode:sess-1:msg-1"
	jsonlPath := filepath.Join(home, "session.jsonl")
	if err := os.WriteFile(jsonlPath, []byte(`{"type":"user","message":{"role":"user","content":"superconductor phase transition witness"}}`+"\n"), 0o600); err != nil {
		t.Fatalf("write jsonl: %v", err)
	}
	jsonlRef := "@ref:jsonl:" + jsonlPath + ":0#0"
	seedVault(t, dbPath,
		fmt.Sprintf(`INSERT INTO thin_vault_docs(doc_id, layer, occurred_at, raw_text, address_uri)
		 VALUES ('doc-ref', 'episodic', 1762380000, %q, 'C6:L1:B001#00')`, ref),
		fmt.Sprintf(`INSERT INTO thin_vault_docs(doc_id, layer, occurred_at, raw_text, address_uri)
		 VALUES ('doc-jsonl-ref', 'episodic', 1762380010, %q, 'C6:L1:B001#01')`, jsonlRef))

	// 等待下一次 plugin 处理，强制让缓存生效（此测试用的是内存 DB，需在 seed 后手动刷新一次）
	got := decodeLayersStatsPayload(t, callPluginHandler(t, layersStatsHandler,
		"/admin/plugin/rolling-memory/layers/list", `{"layer":"episodic","page":1,"limit":20,"keyword":"entanglement"}`))
	if got.Total != 1 || len(got.Items) != 1 || got.Items[0].ID != "doc-ref" || !strings.Contains(got.Items[0].Content, "entanglement") {
		t.Fatalf("hydrated @ref:zcode: keyword search failed: total=%d items=%+v", got.Total, got.Items)
	}
	gotJSONL := decodeLayersStatsPayload(t, callPluginHandler(t, layersStatsHandler,
		"/admin/plugin/rolling-memory/layers/list", `{"layer":"episodic","page":1,"limit":20,"keyword":"superconductor"}`))
	if gotJSONL.Total != 1 || len(gotJSONL.Items) != 1 || gotJSONL.Items[0].ID != "doc-jsonl-ref" || !strings.Contains(gotJSONL.Items[0].Content, "superconductor") {
		t.Fatalf("hydrated @ref:jsonl: keyword search failed: total=%d items=%+v", gotJSONL.Total, gotJSONL.Items)
	}

	corruptPath := filepath.Join(home, "corrupt.db")
	if err := os.WriteFile(corruptPath, []byte("not a valid sqlite database"), 0o600); err != nil {
		t.Fatalf("write corrupt db: %v", err)
	}
	t.Setenv("ROLLING_MEMORY_DB", corruptPath)
	rawResp := callPluginHandler(t, layersStatsHandler, "/admin/plugin/rolling-memory/layers/list", `{"layer":"all","page":1,"limit":20}`)
	var env struct {
		Code int `json:"code"`
	}
	if err := json.Unmarshal([]byte(rawResp), &env); err != nil {
		t.Fatalf("decode corrupt response: %v", err)
	}
	if env.Code == 0 {
		t.Fatalf("corrupted database must fail rather than return empty success: %s", rawResp)
	}
}

// TestBackgroundFilesKeywordBeatsLimit 锁定 P1 修复：文件级关键词过滤发生在 LIMIT 之前，
// 早期实现先 LIMIT 再在 Go 里过滤，会把 limit 窗口之外的匹配文件静默丢弃。
func TestBackgroundFilesKeywordBeatsLimit(t *testing.T) {
	t.Setenv("ROLLING_MEMORY_MODE", "local")
	dbPath := filepath.Join(t.TempDir(), "bg_files.db")
	t.Setenv("ROLLING_MEMORY_DB", dbPath)
	seedVault(t, dbPath,
		`INSERT INTO thin_vault_bg_dirs(dir_id, root_path, added_at) VALUES ('d_a', '/tmp/dir-a', 1762380000)`,
		`INSERT INTO thin_vault_bg_files(abs_path, dir_id, rel_path, mtime_ns, file_size, chunk_count, unit_ids_json)
		 VALUES ('/tmp/dir-a/a1.md', 'd_a', 'a1.md', 1762380000000000000, 10, 1, '["bg:a1#0"]')`,
		`INSERT INTO thin_vault_bg_files(abs_path, dir_id, rel_path, mtime_ns, file_size, chunk_count, unit_ids_json)
		 VALUES ('/tmp/dir-a/a2.md', 'd_a', 'a2.md', 1762380000000000000, 10, 1, '["bg:a2#0"]')`,
		`INSERT INTO thin_vault_bg_files(abs_path, dir_id, rel_path, mtime_ns, file_size, chunk_count, unit_ids_json)
		 VALUES ('/tmp/dir-a/z-target.md', 'd_a', 'z-target.md', 1762380000000000000, 10, 1, '["bg:zt#0"]')`)

	var envelope struct {
		Code int                  `json:"code"`
		Data []BackgroundFileItem `json:"data"`
	}
	if err := json.Unmarshal([]byte(callPluginHandler(t, backgroundFilesHandler,
		"/admin/plugin/rolling-memory/background/files", `{"dir_id":"d_a","keyword":"target","limit":1}`)), &envelope); err != nil {
		t.Fatalf("decode background/files: %v", err)
	}
	if envelope.Code != 0 || len(envelope.Data) != 1 || envelope.Data[0].RelPath != "z-target.md" {
		t.Fatalf("keyword must be applied before LIMIT: %+v", envelope.Data)
	}
}

// TestLayersDeleteResyncsBackgroundFileBookkeeping 锁定删除一致性修复：
// 删掉 L1 分块后，归属文件的派生记账（chunk_count / unit_ids_json / chunk_hashes_json）
// 必须收敛到真实剩余集合，否则目录统计虚高、增量同步语义错乱。
func TestLayersDeleteResyncsBackgroundFileBookkeeping(t *testing.T) {
	t.Setenv("ROLLING_MEMORY_MODE", "local") // 本地模式零外呼：删除路径绝不触发云端 purge
	dbPath := filepath.Join(t.TempDir(), "bg_delete.db")
	t.Setenv("ROLLING_MEMORY_DB", dbPath)
	seedVault(t, dbPath,
		`INSERT INTO thin_vault_bg_dirs(dir_id, root_path, added_at) VALUES ('d_a', '/tmp/dir-a', 1762380000)`,
		`INSERT INTO thin_vault_bg_files(abs_path, dir_id, rel_path, mtime_ns, file_size, chunk_count, unit_ids_json, chunk_hashes_json)
		 VALUES ('/tmp/dir-a/a1.md', 'd_a', 'a1.md', 1762380000000000000, 2048, 2, '["bg:a1#0","bg:a1#1"]', '["h0","h1"]')`,
		`INSERT INTO thin_vault_bg_chunks(unit_id, abs_path, rel_path, chunk_index, anchor, occurred_at, address_uri)
		 VALUES ('bg:a1#0', '/tmp/dir-a/a1.md', 'a1.md', 0, 'L1-9', 1762380000, 'C6:L1:B001#00')`,
		`INSERT INTO thin_vault_bg_chunks(unit_id, abs_path, rel_path, chunk_index, anchor, occurred_at, address_uri)
		 VALUES ('bg:a1#1', '/tmp/dir-a/a1.md', 'a1.md', 1, 'L10-20', 1762380001, 'C6:L1:B002#00')`)

	var delEnvelope struct {
		Code int `json:"code"`
		Data struct {
			Deleted int `json:"deleted"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(callPluginHandler(t, layersDeleteHandler,
		"/admin/plugin/rolling-memory/layers/delete", `{"pks":["bg:a1#0"]}`)), &delEnvelope); err != nil {
		t.Fatalf("decode layers/delete: %v", err)
	}
	if delEnvelope.Code != 0 || delEnvelope.Data.Deleted != 1 {
		t.Fatalf("delete bg chunk mismatch: %+v", delEnvelope)
	}

	db, err := memoryRODB(dbPath)
	if err != nil {
		t.Fatalf("open ro: %v", err)
	}
	var chunkCount int
	var unitIDsJSON, hashesJSON string
	if err := db.QueryRow(`SELECT chunk_count, unit_ids_json, chunk_hashes_json FROM thin_vault_bg_files WHERE abs_path = ?`, "/tmp/dir-a/a1.md").
		Scan(&chunkCount, &unitIDsJSON, &hashesJSON); err != nil {
		t.Fatalf("read file bookkeeping: %v", err)
	}
	if chunkCount != 1 || unitIDsJSON != `["bg:a1#1"]` || hashesJSON != "[]" {
		t.Fatalf("file bookkeeping not resynced: count=%d unit_ids=%s hashes=%s", chunkCount, unitIDsJSON, hashesJSON)
	}
}

// TestReadHookEventsCacheRefreshesOnAppend 锁定 Hook 流水缓存：同一份日志重复读取命中缓存，
// 日志追加后必须立刻看到新事件（缓存按 size + mtime 失效）。
func TestReadHookEventsCacheRefreshesOnAppend(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "rolling-memory.log")
	first := `{"event":"recall","injected":1,"ms":142,"ts":1793270000}` + "\n"
	if err := os.WriteFile(logPath, []byte(first), 0o600); err != nil {
		t.Fatalf("write log: %v", err)
	}
	events, lastMS := readHookEvents(logPath, 10)
	if len(events) != 1 || lastMS != 142 {
		t.Fatalf("first read = %d events, lastMS=%d", len(events), lastMS)
	}
	// 命中缓存（同一文件指纹）不应改变读数。
	if again, ms := readHookEvents(logPath, 10); len(again) != 1 || ms != 142 {
		t.Fatalf("cached read = %d events, lastMS=%d", len(again), ms)
	}
	second := `{"event":"recall","injected":2,"ms":200,"ts":1793270010}` + "\n"
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open append: %v", err)
	}
	if _, err := f.WriteString(second); err != nil {
		_ = f.Close()
		t.Fatalf("append: %v", err)
	}
	_ = f.Close()
	events, lastMS = readHookEvents(logPath, 10)
	if len(events) != 2 || events[0].Event != "recall" || lastMS != 200 {
		t.Fatalf("after append = %d events, lastMS=%d (events=%+v)", len(events), lastMS, events)
	}
}

// TestLayerListSortOnlyAvoidsFullHydration 锁定 P1 修复：排序（表头默认 occurred_at 等列）
// 走 SQL 下推，不再因「带 sort」整体水合三层全表；同时锁住下推路径的显示契约 ——
// 当前页内的 @ref: 零拷贝指针仍逐行水合出会话正文。正文类排序（content / title）
// 依旧走水合路径，保证按正文定序。
func TestLayerListSortOnlyAvoidsFullHydration(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ROLLING_MEMORY_MODE", "local")
	dbPath := filepath.Join(home, "pointer_sort.db")
	t.Setenv("ROLLING_MEMORY_DB", dbPath)

	// 宿主会话库：@ref:zcode:<session>:<message> 可解析出 part 正文。
	zcodeDir := filepath.Join(home, ".zcode", "cli", "db")
	if err := os.MkdirAll(zcodeDir, 0o755); err != nil {
		t.Fatalf("mkdir zcode: %v", err)
	}
	zdb, err := sql.Open("sqlite", filepath.Join(zcodeDir, "db.sqlite"))
	if err != nil {
		t.Fatalf("open zdb: %v", err)
	}
	// 正文首字母刻意与水合前的指针文本顺序相反：水合后 doc-old（aaa…）应排在前。
	if _, err = zdb.Exec(`CREATE TABLE message (id TEXT, session_id TEXT, sequence INTEGER, data TEXT);
		CREATE TABLE part (message_id TEXT, sequence INTEGER, data TEXT);
		INSERT INTO message (id, session_id, sequence, data) VALUES ('msg-old', 'sess-old', 1, '{"role":"assistant"}');
		INSERT INTO message (id, session_id, sequence, data) VALUES ('msg-new', 'sess-new', 1, '{"role":"assistant"}');
		INSERT INTO part (message_id, sequence, data) VALUES ('msg-old', 1, '{"type":"text","text":"aaa pointer body"}');
		INSERT INTO part (message_id, sequence, data) VALUES ('msg-new', 1, '{"type":"text","text":"zzz pointer body"}');`); err != nil {
		_ = zdb.Close()
		t.Fatalf("seed zdb: %v", err)
	}
	if err := zdb.Close(); err != nil {
		t.Fatalf("close zdb: %v", err)
	}

	dayA := time.Date(2026, 11, 10, 12, 0, 0, 0, time.Local).Unix()
	dayB := time.Date(2026, 11, 11, 12, 0, 0, 0, time.Local).Unix()
	seedVault(t, dbPath,
		fmt.Sprintf(`INSERT INTO thin_vault_docs(doc_id, layer, occurred_at, raw_text, address_uri)
		 VALUES ('doc-old', 'episodic', %d, '@ref:zcode:sess-old:msg-old', 'C6:L1:B001#00')`, dayA),
		fmt.Sprintf(`INSERT INTO thin_vault_docs(doc_id, layer, occurred_at, raw_text, address_uri)
		 VALUES ('doc-new', 'episodic', %d, '@ref:zcode:sess-new:msg-new', 'C6:L1:B002#00')`, dayB))

	// 表头默认排序（occurred_at desc）：新行在前，且正文已由指针水合。
	page := decodeLayersStatsPayload(t, callPluginHandler(t, layersStatsHandler,
		"/admin/plugin/rolling-memory/layers/list",
		`{"layer":"all","page":1,"limit":20,"sort":"occurred_at","order":"desc"}`))
	if page.Total != 2 || len(page.Items) != 2 || page.Items[0].ID != "doc-new" {
		t.Fatalf("sort-only page mismatch: total=%d items=%+v", page.Total, page.Items)
	}
	if !strings.Contains(page.Items[0].Content, "zzz pointer body") {
		t.Fatalf("sort-only path must still hydrate pointer content for display, got %q", page.Items[0].Content)
	}

	// 正文类排序仍走水合路径：按 content 升序时水合后的 "aaa…"（doc-old）必须排在前。
	byContent := decodeLayersStatsPayload(t, callPluginHandler(t, layersStatsHandler,
		"/admin/plugin/rolling-memory/layers/list",
		`{"layer":"all","page":1,"limit":20,"sort":"content","order":"asc"}`))
	if len(byContent.Items) != 2 || byContent.Items[0].ID != "doc-old" {
		t.Fatalf("content sort must order by hydrated text: items=%+v", byContent.Items)
	}

	// 非正文列 wheres（如按 layer / id 筛选）走 SQL 下推：验证 wheresNeedHydration 为 false。
	nonHydratedWheres := []tableWhereGroup{{
		Wheres: []tableWhereCondition{
			{Field: "layer", Operator: "eq", Value: "episodic"},
			{Field: "id", Operator: "LIKE", Value: "doc-"},
		},
	}}
	if wheresNeedHydration(nonHydratedWheres) {
		t.Fatalf("non-content wheres must not force full-table pointer hydration")
	}
	contentWheres := []tableWhereGroup{{
		Wheres: []tableWhereCondition{
			{Field: "content", Operator: "LIKE", Value: "pointer"},
		},
	}}
	if !wheresNeedHydration(contentWheres) {
		t.Fatalf("content wheres must enable pointer hydration")
	}
}

// TestPruneLargeVaultHydrationCandidatesCapsPointerLiteralKeyword 锁定关键边界：
// 当查询关键词本身是指针协议前缀或路径子串（如 "zcode" / "jsonl" / 仓库路径名）时，
// pruneLargeVaultHydrationCandidates 绝不能因为未水合的 @ref:zcode:... 字面量包含该词
// 就把上万条未缓存指针全部放行去同步水合，必须严格受限于 maxUncachedHydrateCap（≤64）。
func TestPruneLargeVaultHydrationCandidatesCapsPointerLiteralKeyword(t *testing.T) {
	home := t.TempDir()
	dbPath := filepath.Join(home, "vault.db")
	rows := make([]map[string]any, 0, 800)
	for i := 0; i < 800; i++ {
		rows = append(rows, map[string]any{
			"id":          fmt.Sprintf("ep_%04d", i),
			"layer":       "episodic",
			"src":         "doc",
			"content":     fmt.Sprintf("@ref:zcode:sess_%04d:msg_%04d#0", i, i),
			"address_uri": fmt.Sprintf("C6:L1:LOCAL#ep_%04d", i),
			"occurred_at": 1700000000 + i,
		})
	}
	pruned := pruneLargeVaultHydrationCandidates(dbPath, home, rows, "zcode", nil)
	if len(pruned) > 64 {
		t.Fatalf("expected uncached @ref: candidates to be capped at <= 64 when searching 'zcode', got %d", len(pruned))
	}
}
