// plugin: rolling-memory v0.0.58
package rollingmemory

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// callPluginHandler 以真实 gin 上下文调用插件处理器并返回响应体（与 plugin_test.go 同口径）。
func callPluginHandler(t *testing.T, handler gin.HandlerFunc, path, body string) string {
	t.Helper()
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	handler(c)
	return recorder.Body.String()
}

func decodeLayersStatsPayload(t *testing.T, raw string) LayerStatsResponse {
	t.Helper()
	var envelope struct {
		Code int                `json:"code"`
		Data LayerStatsResponse `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		t.Fatalf("decode layers/stats response %q: %v", raw, err)
	}
	if envelope.Code != 0 {
		t.Fatalf("layers/stats returned business error: %s", raw)
	}
	return envelope.Data
}

// TestLayersStatsFiltersAndBackgroundFiles 锁定 L1/L3 页面新增的过滤契约：
// ① L3 status（active/stale）服务端过滤；② L1 dir_id 按挂载目录过滤分块；
// ③ /background/files 文件级清单（目录 → 文件 → 分块三级浏览的中间层）。
func TestLayersStatsFiltersAndBackgroundFiles(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "layers_filters.db")
	db, err := openWritableVaultDB(dbPath)
	if err != nil {
		t.Fatalf("openWritableVaultDB: %v", err)
	}
	seeds := []string{
		`INSERT INTO thin_vault_docs(doc_id, layer, occurred_at, raw_text, address_uri)
		 VALUES ('EPI-1', 'episodic', 1762380000, 'episodic turn', 'C6:L1:B001#00')`,
		`INSERT INTO thin_vault_noetic(noetic_id, occurred_at, status, query_text, answer_text, cited_unit_ids_json)
		 VALUES ('NOE-ACT', 1762380100, 'active', 'q-active', 'a-active', '["EPI-1"]')`,
		`INSERT INTO thin_vault_noetic(noetic_id, occurred_at, status, query_text, answer_text, cited_unit_ids_json)
		 VALUES ('NOE-STALE', 1762380200, 'stale', 'q-stale', 'a-stale', '[]')`,
		`INSERT INTO thin_vault_bg_dirs(dir_id, root_path, added_at) VALUES ('d_a', '/tmp/dir-a', 1762380000)`,
		`INSERT INTO thin_vault_bg_dirs(dir_id, root_path, added_at) VALUES ('d_b', '/tmp/dir-b', 1762380000)`,
		`INSERT INTO thin_vault_bg_files(abs_path, dir_id, rel_path, mtime_ns, file_size, chunk_count, unit_ids_json)
		 VALUES ('/tmp/dir-a/a1.md', 'd_a', 'a1.md', 1762380000000000000, 2048, 2, '["bg:a1#0","bg:a1#1"]')`,
		`INSERT INTO thin_vault_bg_files(abs_path, dir_id, rel_path, mtime_ns, file_size, chunk_count, unit_ids_json)
		 VALUES ('/tmp/dir-b/b1.md', 'd_b', 'b1.md', 1762380000000000000, 300, 1, '["bg:b1#0"]')`,
		`INSERT INTO thin_vault_bg_chunks(unit_id, abs_path, rel_path, chunk_index, anchor, occurred_at, address_uri)
		 VALUES ('bg:a1#0', '/tmp/dir-a/a1.md', 'a1.md', 0, 'L1-9', 1762380000, 'C6:L1:B001#00')`,
		`INSERT INTO thin_vault_bg_chunks(unit_id, abs_path, rel_path, chunk_index, anchor, occurred_at, address_uri)
		 VALUES ('bg:a1#1', '/tmp/dir-a/a1.md', 'a1.md', 1, 'L10-20', 1762380001, 'C6:L1:B002#00')`,
		`INSERT INTO thin_vault_bg_chunks(unit_id, abs_path, rel_path, chunk_index, anchor, occurred_at, address_uri)
		 VALUES ('bg:b1#0', '/tmp/dir-b/b1.md', 'b1.md', 0, 'L1-7', 1762380002, 'C6:L1:B003#00')`,
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
	t.Setenv("ROLLING_MEMORY_DB", dbPath)

	// ① L3 状态过滤：active 只回 active，stale 只回 stale
	activeStats := decodeLayersStatsPayload(t, callPluginHandler(t, layersStatsHandler,
		"/admin/plugin/rolling-memory/layers/stats", `{"layer":"noetic","status":"active"}`))
	if len(activeStats.Items) != 1 || activeStats.Items[0].ID != "NOE-ACT" || activeStats.Items[0].Status != "active" {
		t.Fatalf("noetic status=active filter mismatch: %+v", activeStats.Items)
	}
	if activeStats.NoeticActiveCount != 1 || activeStats.NoeticStaleCount != 1 {
		t.Fatalf("noetic counters should stay global: active=%d stale=%d", activeStats.NoeticActiveCount, activeStats.NoeticStaleCount)
	}
	staleStats := decodeLayersStatsPayload(t, callPluginHandler(t, layersStatsHandler,
		"/admin/plugin/rolling-memory/layers/stats", `{"layer":"noetic","status":"stale"}`))
	if len(staleStats.Items) != 1 || staleStats.Items[0].ID != "NOE-STALE" || staleStats.Items[0].Status != "stale" {
		t.Fatalf("noetic status=stale filter mismatch: %+v", staleStats.Items)
	}
	allStats := decodeLayersStatsPayload(t, callPluginHandler(t, layersStatsHandler,
		"/admin/plugin/rolling-memory/layers/stats", `{"layer":"noetic"}`))
	if len(allStats.Items) != 2 {
		t.Fatalf("noetic without status filter should return both statuses: %+v", allStats.Items)
	}

	// ② L1 目录过滤：dir_id=d_a 只回 d_a 的分块（且每行都带引擎记忆地址 address_uri）
	dirStats := decodeLayersStatsPayload(t, callPluginHandler(t, layersStatsHandler,
		"/admin/plugin/rolling-memory/layers/stats", `{"layer":"background","dir_id":"d_a"}`))
	if len(dirStats.Items) != 2 {
		t.Fatalf("dir_id filter should return 2 chunks of d_a: %+v", dirStats.Items)
	}
	for _, row := range dirStats.Items {
		if row.Layer != "background" || row.RelPath != "a1.md" {
			t.Fatalf("dir_id filter leaked a foreign chunk: %+v", row)
		}
		if row.ID != "bg:a1#0" && row.ID != "bg:a1#1" {
			t.Fatalf("dir_id filter returned an unexpected chunk id: %+v", row)
		}
		if strings.TrimSpace(row.AddressURI) == "" {
			t.Fatalf("dir_id filter dropped engine cell addressing: %+v", dirStats.Items)
		}
	}
	allBg := decodeLayersStatsPayload(t, callPluginHandler(t, layersStatsHandler,
		"/admin/plugin/rolling-memory/layers/stats", `{"layer":"background"}`))
	if len(allBg.Items) != 3 {
		t.Fatalf("background without dir_id should return all 3 chunks: %+v", allBg.Items)
	}

	// ③ 文件级清单：dir_id=d_a 只回 a1.md，带人类可读大小与分块数
	var filesEnvelope struct {
		Code int                  `json:"code"`
		Data []BackgroundFileItem `json:"data"`
	}
	if err := json.Unmarshal([]byte(callPluginHandler(t, backgroundFilesHandler,
		"/admin/plugin/rolling-memory/background/files", `{"dir_id":"d_a"}`)), &filesEnvelope); err != nil {
		t.Fatalf("decode background/files: %v", err)
	}
	if filesEnvelope.Code != 0 || len(filesEnvelope.Data) != 1 {
		t.Fatalf("background/files dir_id=d_a mismatch: %+v", filesEnvelope.Data)
	}
	file := filesEnvelope.Data[0]
	if file.RelPath != "a1.md" || file.DirID != "d_a" || file.ChunkCount != 2 || file.SizeText != "2.0 KiB" {
		t.Fatalf("background/files item mismatch: %+v", file)
	}
	if file.UpdatedText == "" || file.UpdatedText == "-" {
		t.Fatalf("background/files should expose a readable mtime: %+v", file)
	}
	var allFilesEnvelope struct {
		Data []BackgroundFileItem `json:"data"`
	}
	if err := json.Unmarshal([]byte(callPluginHandler(t, backgroundFilesHandler,
		"/admin/plugin/rolling-memory/background/files", `{}`)), &allFilesEnvelope); err != nil {
		t.Fatalf("decode background/files (all): %v", err)
	}
	if len(allFilesEnvelope.Data) != 2 {
		t.Fatalf("background/files without dir_id should list both files: %+v", allFilesEnvelope.Data)
	}

	// ④ BuildAdmin TableManager 契约：分页 (page/limit)、wheres 过滤、get 单条与 delete 级联失效
	pagedStats := decodeLayersStatsPayload(t, callPluginHandler(t, layersStatsHandler,
		"/admin/plugin/rolling-memory/layers/list",
		`{"layer":"all","page":1,"limit":2,"sort":"id","order":"asc","wheres":[{"wheres":[{"field":"layer","operator":"eq","value":"background"}],"or":false}]}`))
	if pagedStats.Total != 3 || len(pagedStats.List) != 2 || pagedStats.List[0].ID != "bg:a1#0" {
		t.Fatalf("baTable pagination + wheres mismatch: total=%d list=%+v", pagedStats.Total, pagedStats.List)
	}

	var getEnvelope struct {
		Code int `json:"code"`
		Data struct {
			Row ThreeLayerRow `json:"row"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(callPluginHandler(t, layersGetHandler,
		"/admin/plugin/rolling-memory/layers/get", `{"id":"EPI-1"}`)), &getEnvelope); err != nil {
		t.Fatalf("decode layers/get: %v", err)
	}
	if getEnvelope.Code != 0 || getEnvelope.Data.Row.ID != "EPI-1" || getEnvelope.Data.Row.Layer != "episodic" {
		t.Fatalf("layers/get mismatch: %+v", getEnvelope)
	}

	// L3 引证可定位 L1 分块：正文经守护进程现场重读重切；单测环境无守护进程（.test 直连被
	// 门禁禁用）且夹具文件不存在，必须退化为来源位置预览并显式标记 body_unavailable，
	// 绝不拿坐标冒充正文、也不假装正文可读。
	if err := json.Unmarshal([]byte(callPluginHandler(t, layersGetHandler,
		"/admin/plugin/rolling-memory/layers/get", `{"id":"bg:a1#0"}`)), &getEnvelope); err != nil {
		t.Fatalf("decode background layers/get: %v", err)
	}
	if row := getEnvelope.Data.Row; getEnvelope.Code != 0 || row.ID != "bg:a1#0" || row.Layer != "background" || row.RelPath != "a1.md" || row.Content != "[a1.md#L1-9] (/tmp/dir-a/a1.md)" || !row.BodyUnavailable {
		t.Fatalf("background layers/get unavailable-body mismatch: %+v", getEnvelope)
	}

	var delEnvelope struct {
		Code int `json:"code"`
		Data struct {
			Deleted           int `json:"deleted"`
			InvalidatedNoetic int `json:"invalidated_noetic"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(callPluginHandler(t, layersDeleteHandler,
		"/admin/plugin/rolling-memory/layers/delete", `{"pks":["EPI-1"]}`)), &delEnvelope); err != nil {
		t.Fatalf("decode layers/delete: %v", err)
	}
	if delEnvelope.Code != 0 || delEnvelope.Data.Deleted != 1 || delEnvelope.Data.InvalidatedNoetic != 1 {
		t.Fatalf("layers/delete should delete EPI-1 and cascade-invalidate NOE-ACT: %+v", delEnvelope)
	}
	afterDelNoetic := decodeLayersStatsPayload(t, callPluginHandler(t, layersStatsHandler,
		"/admin/plugin/rolling-memory/layers/stats", `{"layer":"noetic","status":"stale"}`))
	if len(afterDelNoetic.Items) != 2 {
		t.Fatalf("expected 2 stale noetic items after cascading delete, got %+v", afterDelNoetic.Items)
	}
}

// TestRecallRawHitDetailUsesTrace 守卫原文命中使用真实响应的 trace 标识导航。
func TestRecallRawHitDetailUsesTrace(t *testing.T) {
	path := filepath.Join("..", "..", "web", "src", "views", "admin", "plugin", "rolling-memory", "recall.vue")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read recall debugger: %v", err)
	}
	for _, want := range []string{
		"typeof hit.trace === 'string' ? hit.trace.trim() : ''",
		`v-if="rawHitUnitID(rh)"`,
		`@click="openRawHit(rawHitUnitID(rh))"`,
		`query: { unit_id: id, ref: String(Date.now()) }`,
	} {
		if !strings.Contains(string(data), want) {
			t.Errorf("recall debugger missing raw hit detail contract %q", want)
		}
	}
}

// TestNormalizeAdminMemoryLayerKeepsThreeLayerBoundaries 守卫三层语义边界：
// L1 只承接用户自己导入的本地目录文件底物，L2 只承接 Harness 对话记录与未知回退，
// L3 只承接 Agent 提问 + 检索证据固化；早期 warrant 词不得再被翻译成 background。
func TestNormalizeAdminMemoryLayerKeepsThreeLayerBoundaries(t *testing.T) {
	cases := map[string]string{
		"background": "background", "BG": "background", "substrate": "background", "document": "background", "doc": "background",
		"episodic": "episodic", "conversation": "episodic", "": "episodic",
		"noetic": "noetic", "qa": "noetic", "insight": "noetic",
		"referential": "episodic", "procedural": "episodic", "performative": "episodic",
	}
	for raw, want := range cases {
		if got := normalizeAdminMemoryLayer(raw); got != want {
			t.Fatalf("normalizeAdminMemoryLayer(%q)=%q, want %q", raw, got, want)
		}
	}
}

// TestMemoryBrowserSharedComponentAndNoWaveformRegression 收敛留痕守卫（波形面已从产品移除）：
// ① 共享浏览器组件 layerBrowser.vue 必须保留 baTable 契约，四个入口页面必须复用它，不得各自复制第二套列表实现；
// ② L2 页面必须保留本地 Harness 同步能力；
// ③ 反回归：layerBrowser.vue 与中英语言包不得再出现波形字段/渲染口径（waveform_bar / waveform_hex / hasWaveform( / colWave / spectrum:）；
// ④ 不得出现伪造采样映射（正文词元严禁被伪造成波形采样绑定）。
func TestMemoryBrowserSharedComponentAndNoWaveformRegression(t *testing.T) {
	viewsRoot := filepath.Join("..", "..", "web", "src", "views", "admin", "plugin", "rolling-memory")
	data, err := os.ReadFile(filepath.Join(viewsRoot, "layerBrowser.vue"))
	if err != nil {
		t.Fatalf("read memory browser template: %v", err)
	}
	source := string(data)
	for _, forbidden := range []string{
		"activeModalSlot.token",
		"modalSlots[idx]?.token",
		"sl.token",
		"逐词",
		"[0.22, 0.68, 0.92, 0.38, 0.84, 0.46, 0.75, 0.3]",
	} {
		if strings.Contains(source, forbidden) {
			t.Fatalf("memory browser contains fabricated token/sample mapping %q", forbidden)
		}
	}
	for _, required := range []string{
		"TableHeader",
		"useTableManager",
		"TableManagerAPI",
		"infoRow.layer === 'background' && infoRow.rel_path && infoRow.body_unavailable",
		"The chunk text is currently unavailable (the source file may have been moved or deleted, or the local reading engine is not ready). Only the source location is shown below.",
		// L1 背景分块详情必须经 /layers/get 取规范正文（守护进程现场重读重切），
		// 而不是停在列表行的位置预览。
		"if (row.layer === 'background' && row.rel_path) {",
		// 详情正文统一经 Markdown 预览面展示；正文不可读时提供「重新读取」重试。
		`:text="infoRow.content"`,
		`:hint="{ layer: infoRow.layer, relPath: infoRow.rel_path }"`,
		"btn-dialog-reload-body",
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("memory browser missing baTable contract %q", required)
		}
	}
	// 反回归：云端已不下发波形字段，控制台只表达「记忆已编码并已获得记忆地址」，禁任何波形口径再现。
	for _, forbidden := range []string{"waveform_bar", "waveform_hex", "hasWaveform(", "colWave", "spectrum:"} {
		if strings.Contains(source, forbidden) {
			t.Fatalf("layerBrowser.vue must not reintroduce waveform contract %q", forbidden)
		}
	}
	epiData, err := os.ReadFile(filepath.Join(viewsRoot, "layerEpisodic.vue"))
	if err != nil {
		t.Fatalf("read layerEpisodic.vue: %v", err)
	}
	for _, required := range []string{"btn-sync-active-harness", "btn-sync-all-harnesses", "/admin/plugin/rolling-memory/sync/reconcile"} {
		if !strings.Contains(string(epiData), required) {
			t.Fatalf("layerEpisodic.vue missing local harness sync capability %q", required)
		}
	}
	// 四个入口页面（总览 + L1/L2/L3）必须经共享组件渲染列表，不得各自复制第二套实现。
	for _, page := range []string{
		filepath.Join(viewsRoot, "index.vue"),
		filepath.Join(viewsRoot, "layerBackground.vue"),
		filepath.Join(viewsRoot, "layerEpisodic.vue"),
		filepath.Join(viewsRoot, "layerNoetic.vue"),
	} {
		pageData, err := os.ReadFile(page)
		if err != nil {
			t.Fatalf("read memory page %s: %v", page, err)
		}
		if !strings.Contains(string(pageData), "layerBrowser") {
			t.Fatalf("memory page %s does not reuse the shared browser component", page)
		}
	}
	for _, langFile := range []string{
		filepath.Join("..", "..", "web", "src", "lang", "zh-cn", "plugin", "rolling-memory.yaml"),
		filepath.Join("..", "..", "web", "src", "lang", "en", "plugin", "rolling-memory.yaml"),
	} {
		langData, err := os.ReadFile(langFile)
		if err != nil {
			t.Fatalf("read memory browser labels %s: %v", langFile, err)
		}
		langSource := string(langData)
		for _, forbidden := range []string{"waveform_bar", "waveform_hex", "hasWaveform(", "colWave", "spectrum:"} {
			if strings.Contains(langSource, forbidden) {
				t.Fatalf("%s must not reintroduce waveform labels %q", langFile, forbidden)
			}
		}
	}
}

func TestEnvHealthAndSyncReconcileHandlersAndLegacyTablePurge(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "clean_rame.db")
	db, err := openWritableVaultDB(dbPath)
	if err != nil {
		t.Fatalf("openWritableVaultDB: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE trace (hash TEXT PRIMARY KEY); CREATE TABLE assertion (node_id TEXT PRIMARY KEY);`); err != nil {
		_ = db.Close()
		t.Fatalf("seed legacy tables: %v", err)
	}
	_ = db.Close()

	db2, err := openWritableVaultDB(dbPath)
	if err != nil {
		t.Fatalf("re-openWritableVaultDB: %v", err)
	}
	var legacyCnt int
	if err := db2.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name IN ('trace','assertion','node','vector')`).Scan(&legacyCnt); err != nil {
		_ = db2.Close()
		t.Fatalf("check legacy tables: %v", err)
	}
	_ = db2.Close()
	if legacyCnt != 0 {
		t.Fatalf("expected openWritableVaultDB to purge legacy tables, found %d", legacyCnt)
	}

	t.Setenv("ROLLING_MEMORY_DB", dbPath)
	prevExec := executeAdminSyncCLI
	t.Cleanup(func() { executeAdminSyncCLI = prevExec })

	var gotAction, gotLayer string
	executeAdminSyncCLI = func(_ context.Context, action, _, _, layer string, _ int) ([]byte, error) {
		gotAction = action
		gotLayer = layer
		if action == "status" {
			return []byte(`{"active_harness_id":"zcode","active_harness_label":"ZCode","health_score":96,"health_grade":"healthy","total_pending_sync":1}`), nil
		}
		return []byte(`{"layer":"` + layer + `","episodic_imported_from_harness":2,"episodic_pushed_to_remote":2,"total_reconciled":2,"status":{"health_score":100}}`), nil
	}

	healthRaw := callPluginHandler(t, envHealthHandler, "/admin/plugin/rolling-memory/env/health", `{}`)
	if gotAction != "status" || !strings.Contains(healthRaw, `"active_harness_label":"ZCode"`) || !strings.Contains(healthRaw, `"health_score":96`) {
		t.Fatalf("unexpected envHealthHandler response: %s", healthRaw)
	}

	recRaw := callPluginHandler(t, syncReconcileHandler, "/admin/plugin/rolling-memory/sync/reconcile", `{"layer":"episodic","limit":20}`)
	if gotAction != "reconcile" || gotLayer != "episodic" || !strings.Contains(recRaw, `"episodic_imported_from_harness":2`) {
		t.Fatalf("unexpected syncReconcileHandler response: %s", recRaw)
	}
}

// TestSyncReconcileHandlerStripsWindowScopeNotice 锁定后台 sync/reconcile 接口必须剥离
// CLI 仅用于 JSON 契约断言的常规窗口范围说明（"episodic index backfill covers only the scanned harness window"），
// 同时保留真正的异常 warning，避免控制台（含未刷新的旧前端页面）在正常同步时弹出黄色警告。
func TestSyncReconcileHandlerStripsWindowScopeNotice(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	prevExec := executeAdminSyncCLI
	t.Cleanup(func() { executeAdminSyncCLI = prevExec })

	executeAdminSyncCLI = func(_ context.Context, _, _, _, _ string, _ int) ([]byte, error) {
		return []byte(`{"layer":"all","episodic_imported_from_harness":3,"warnings":["episodic index backfill covers only the scanned harness window; unavailable or unscanned historical sources are unknown"],"status":{"health_score":100}}`), nil
	}
	raw := callPluginHandler(t, syncReconcileHandler, "/admin/plugin/rolling-memory/sync/reconcile", `{"layer":"all"}`)
	if strings.Contains(raw, "episodic index backfill covers only the scanned harness window") || strings.Contains(raw, `"warnings"`) {
		t.Fatalf("routine window notice must be stripped from admin reconcile response, got: %s", raw)
	}
	if !strings.Contains(raw, `"auto_sync_enabled"`) {
		t.Fatalf("reconcile status should be enriched with auto_sync_enabled, got: %s", raw)
	}

	executeAdminSyncCLI = func(_ context.Context, _, _, _, _ string, _ int) ([]byte, error) {
		return []byte(`{"layer":"all","warnings":["episodic index backfill covers only the scanned harness window; unavailable or unscanned historical sources are unknown","background dir sync: permission denied"],"status":{"health_score":80}}`), nil
	}
	rawWithRealWarn := callPluginHandler(t, syncReconcileHandler, "/admin/plugin/rolling-memory/sync/reconcile", `{"layer":"all"}`)
	if strings.Contains(rawWithRealWarn, "episodic index backfill covers only") {
		t.Fatalf("routine window notice must be stripped when mixed with real warnings: %s", rawWithRealWarn)
	}
	if !strings.Contains(rawWithRealWarn, "background dir sync: permission denied") {
		t.Fatalf("actionable warning must be preserved, got: %s", rawWithRealWarn)
	}
}

// TestEnvHealthHandlerDefaultsToActiveHarness 锁定一次性 /env/health 的扫描口径：
// 空 harness 必须解析为 active_workspace.json 的 carrier 机器标识并透传给 `sync status --harness`
// （与实时流 /「重新检测」同一口径；全量跨 Harness 扫描与单 Harness 过滤载荷逐字段一致，
// 仅 updated_at 不同，见 collectEnvHealthPayload 注释），显式 harness 原样透传不覆盖；
// 活动工作区缺失时保持空串回退全量，不误配其它 Harness。
func TestEnvHealthHandlerDefaultsToActiveHarness(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	stateDir := filepath.Join(home, ".local", "state", "rolling-memory")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("mkdir state dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "active_workspace.json"), []byte(`{"carrier":"harness:zcode"}`), 0o644); err != nil {
		t.Fatalf("write active workspace: %v", err)
	}
	prevExec := executeAdminSyncCLI
	t.Cleanup(func() { executeAdminSyncCLI = prevExec })

	var gotHarness string
	executeAdminSyncCLI = func(_ context.Context, _, _, harnessID, _ string, _ int) ([]byte, error) {
		gotHarness = harnessID
		return []byte(`{"active_harness_id":"zcode","health_score":96}`), nil
	}
	_ = callPluginHandler(t, envHealthHandler, "/admin/plugin/rolling-memory/env/health", `{}`)
	if gotHarness != "zcode" {
		t.Fatalf("empty harness should resolve to active carrier, got %q", gotHarness)
	}

	_ = callPluginHandler(t, envHealthHandler, "/admin/plugin/rolling-memory/env/health", `{"harness":"claude-code"}`)
	if gotHarness != "claude-code" {
		t.Fatalf("explicit harness should pass through, got %q", gotHarness)
	}

	// 活动工作区缺失（全新环境）→ 空串回退全量，不猜测其它 Harness。
	if err := os.Remove(filepath.Join(stateDir, "active_workspace.json")); err != nil {
		t.Fatalf("remove active workspace: %v", err)
	}
	_ = callPluginHandler(t, envHealthHandler, "/admin/plugin/rolling-memory/env/health", `{"force":true}`)
	if gotHarness != "" {
		t.Fatalf("missing active workspace should fall back to empty harness, got %q", gotHarness)
	}
}

func TestLayersDeleteCleansContentlessFTSLineageTombstonesAndActiveCitationLinks(t *testing.T) {
	t.Setenv("ROLLING_MEMORY_MODE", "local")
	dbPath := filepath.Join(t.TempDir(), "delete_consistency.db")
	t.Setenv("ROLLING_MEMORY_DB", dbPath)

	seedVault(t, dbPath,
		`CREATE VIRTUAL TABLE IF NOT EXISTS thin_vault_docs_fts USING fts5(raw_text, doc_id UNINDEXED, content='', contentless_delete=1, tokenize = 'trigram')`,
		`CREATE TABLE IF NOT EXISTS thin_vault_fts_map (rowid INTEGER PRIMARY KEY AUTOINCREMENT, doc_id TEXT NOT NULL UNIQUE)`,
		`CREATE TABLE IF NOT EXISTS thin_vault_episodic_lineage (turn_ref TEXT NOT NULL, doc_id TEXT NOT NULL, occurred_at INTEGER NOT NULL DEFAULT 0, PRIMARY KEY (turn_ref, doc_id))`,
		`INSERT INTO thin_vault_docs(doc_id, layer, occurred_at, raw_text, address_uri) VALUES ('ep_del_1', 'episodic', 1762380000, 'deleted episodic text', 'C6:L1:E001#00')`,
		`INSERT INTO thin_vault_fts_map(rowid, doc_id) VALUES (101, 'ep_del_1'), (102, 'bg:del#0')`,
		`INSERT INTO thin_vault_docs_fts(rowid, raw_text) VALUES (101, 'deleted episodic text'), (102, 'deleted bg chunk text')`,
		`INSERT INTO thin_vault_episodic_lineage(turn_ref, doc_id, occurred_at) VALUES ('@ref:zcode:s1:m1', 'ep_del_1', 1762380000)`,
		`INSERT INTO thin_vault_bg_dirs(dir_id, root_path, added_at) VALUES ('d_del', '/tmp/dir-del', 1762380000)`,
		`INSERT INTO thin_vault_bg_files(abs_path, dir_id, rel_path, mtime_ns, file_size, chunk_count, unit_ids_json, chunk_hashes_json) VALUES ('/tmp/dir-del/f.md', 'd_del', 'f.md', 1762380000000000000, 512, 1, '["bg:del#0"]', '["h0"]')`,
		`INSERT INTO thin_vault_bg_chunks(unit_id, abs_path, rel_path, chunk_index, anchor, occurred_at, address_uri) VALUES ('bg:del#0', '/tmp/dir-del/f.md', 'f.md', 0, 'L1-5', 1762380000, 'C6:L1:B001#00')`,
		`INSERT INTO thin_vault_noetic(noetic_id, occurred_at, status, query_text, answer_text, cited_unit_ids_json) VALUES ('nt_1', 1762380010, 'active', 'q1', 'a1', '["ep_del_1","bg:del#0"]')`,
		`INSERT INTO thin_vault_noetic(noetic_id, occurred_at, status, query_text, answer_text, cited_unit_ids_json) VALUES ('nt_2', 1762380020, 'active', 'q2', 'a2', '["ep_alive"]')`,
	)

	// Before deletion: both nt_1 (2 links) and nt_2 (1 link) are active -> NoeticCitedLinks == 3.
	beforeRaw := callPluginHandler(t, layersStatsHandler, "/admin/plugin/rolling-memory/layers/stats", `{"layer":"noetic"}`)
	if !strings.Contains(beforeRaw, `"noetic_cited_links":3`) {
		t.Fatalf("expected noetic_cited_links=3 before deletion, got: %s", beforeRaw)
	}

	delRaw := callPluginHandler(t, layersDeleteHandler, "/admin/plugin/rolling-memory/layers/delete", `{"pks":["ep_del_1","bg:del#0"]}`)
	if !strings.Contains(delRaw, `"deleted":2`) || !strings.Contains(delRaw, `"invalidated_noetic":1`) {
		t.Fatalf("unexpected delete response: %s", delRaw)
	}

	// After deletion: nt_1 became stale, so active NoeticCitedLinks must drop from 3 to 1 (matching ROI screen).
	afterRaw := callPluginHandler(t, layersStatsHandler, "/admin/plugin/rolling-memory/layers/stats", `{"layer":"noetic"}`)
	if !strings.Contains(afterRaw, `"noetic_cited_links":1`) || !strings.Contains(afterRaw, `"noetic_stale_count":1`) {
		t.Fatalf("expected noetic_cited_links=1 and noetic_stale_count=1 after deletion, got: %s", afterRaw)
	}

	db, err := openVaultWriteOnly(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	var ftsMapCnt, ftsCnt, lineageCnt, tombCnt int
	_ = db.QueryRow(`SELECT COUNT(*) FROM thin_vault_fts_map WHERE doc_id IN ('ep_del_1', 'bg:del#0')`).Scan(&ftsMapCnt)
	_ = db.QueryRow(`SELECT COUNT(*) FROM thin_vault_docs_fts WHERE thin_vault_docs_fts MATCH 'deleted'`).Scan(&ftsCnt)
	_ = db.QueryRow(`SELECT COUNT(*) FROM thin_vault_episodic_lineage WHERE doc_id = 'ep_del_1'`).Scan(&lineageCnt)
	_ = db.QueryRow(`SELECT COUNT(*) FROM thin_vault_deleted_units WHERE unit_id IN ('ep_del_1', 'bg:del#0')`).Scan(&tombCnt)
	if ftsMapCnt != 0 || ftsCnt != 0 || lineageCnt != 0 || tombCnt != 2 {
		t.Fatalf("expected ftsMap=0 fts=0 lineage=0 tombstones=2, got ftsMap=%d fts=%d lineage=%d tombstones=%d", ftsMapCnt, ftsCnt, lineageCnt, tombCnt)
	}
}
