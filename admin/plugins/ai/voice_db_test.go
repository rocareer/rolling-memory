// plugin: ai v0.2.0
// voice_db_test.go 是语音（TTS）的**库级**用例（DSN 门控，复用 store_db_test.go 的基建）：
//
//   - 迁移 6 的 DDL：plugin_ai_voice 表与列齐、name 唯一索引；plugin_ai_model.type /
//     plugin_ai_usage.call_type 的 CHECK 闭集**逐字**含 'tts'（查 pg_constraint 的约束定义）；
//   - 真库落 type='tts' 与 call_type='tts'（通过新 CHECK，时序纪律要求由本文件承担该断言）；
//   - 音色 CRUD 往返 + 高级检索（wheres 白名单在真库上生效、非法字段得可读拒绝）；
//   - 试听端到端：真库 + 真驱动 + 伪上游 ⇒ **恰好命中上游一次**、音频字节直出、
//     服务端不落盘（TMPDIR 零新增文件）、usage 行按冻结口径落库；
//   - down 迁移恢复旧 CHECK（回滚判据）。
//
// 用法（**必须指向临时库**）：
//
//	PLUGIN_AI_TEST_DSN='postgres://用户:口令@主机:端口/临时库?sslmode=disable' \
//	  go test -count=1 -run 'TestVoice' -v ./plugins/ai/
package ai

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-go-hub/ai-go-admin/internal/infra/config"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// voiceTablePrefix 是当前进程的表前缀（约束名与表名都带它）。
func voiceTablePrefix() string { return config.Get().Database.Prefix }

// voiceApplyMigrationFile 按安装器口径执行一个迁移文件（__PREFIX__ 替换 + 单事务 + 整文件一次执行）。
func voiceApplyMigrationFile(t *testing.T, db *gorm.DB, name string) {
	t.Helper()
	path := filepath.Join(pluginAITestMigrationsDir, name)
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取迁移文件 %s 失败: %v", path, err)
	}
	ddl := strings.ReplaceAll(string(body), "__PREFIX__", voiceTablePrefix())
	if err := db.Transaction(func(tx *gorm.DB) error { return tx.Exec(ddl).Error }); err != nil {
		t.Fatalf("执行迁移文件 %s 失败: %v", path, err)
	}
}

// voiceConstraintDef 取具名 CHECK 的定义正文（约束不存在返回空串 ⇒ 调用方据此判红）。
func voiceConstraintDef(t *testing.T, db *gorm.DB, table, constraint string) string {
	t.Helper()
	var def string
	err := db.Raw(`SELECT pg_get_constraintdef(c.oid) FROM pg_constraint c
		JOIN pg_class t ON t.oid = c.conrelid
		WHERE t.relname = ? AND c.conname = ?`, table, constraint).Row().Scan(&def)
	if errors.Is(err, sql.ErrNoRows) {
		return ""
	}
	if err != nil {
		t.Fatalf("读取约束 %s.%s 失败: %v", table, constraint, err)
	}
	return def
}

// voiceTableExists 判物理表是否存在。
func voiceTableExists(t *testing.T, db *gorm.DB, name string) bool {
	t.Helper()
	var found string
	if err := db.Raw(`SELECT COALESCE(to_regclass(?)::text, '')`, name).Row().Scan(&found); err != nil {
		t.Fatalf("探测表 %s 失败: %v", name, err)
	}
	return found != ""
}

// voiceDBDeps 注入真库 store + 真驱动（无鉴权中间件：链路挂载由 handler_test.go 的结构断言覆盖）。
func voiceDBDeps(store *Store, driver Driver) deps {
	set := parsePluginSettings(nil)
	return deps{
		loadSettings: func(context.Context) (pluginSettings, error) { return set, nil },
		loadStore:    func(context.Context) (*Store, error) { return store, nil },
		loadService: func(context.Context, pluginSettings) (*Service, error) {
			return NewService(store, driver, Config{}, nil), nil
		},
	}
}

// voiceDBEngine 挂语音的 5 个后台端点（相对段与 adminRoutes 声明逐字一致）。
func voiceDBEngine(d deps) *gin.Engine {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	group := engine.Group("/admin/plugin/" + Name)
	group.POST("/voice/list", d.voiceList)
	group.GET("/voice/get/:pk", d.voiceGet)
	group.POST("/voice/create", d.voiceCreate)
	group.POST("/voice/update/:pk", d.voiceUpdate)
	group.POST("/voice/delete", d.voiceDelete)
	group.POST("/voice/preview", d.voicePreview)
	return engine
}

// decodedEnvelope 是解码后的后台回执（code/message/data）。
type decodedEnvelope struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func decodeVoiceEnvelope(t *testing.T, recorder *httptest.ResponseRecorder) decodedEnvelope {
	t.Helper()
	var envelope decodedEnvelope
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("回执不是 JSON: %v（原文 %s）", err, recorder.Body.String())
	}
	return envelope
}

// TestVoiceSchemaAndTTSClosureDB 覆盖迁移 6 的两半：音色表 DDL 与闭集 CHECK（逐字含 'tts'）。
func TestVoiceSchemaAndTTSClosureDB(t *testing.T) {
	db := pluginAITestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	// ① 音色表存在且列齐（列名与迁移 6 逐列对照）。
	voiceTable := table("plugin_ai_voice")
	if !voiceTableExists(t, db, voiceTable) {
		t.Fatalf("迁移 6 之后应存在 %s", voiceTable)
	}
	var columns []string
	if err := db.Raw(`SELECT column_name FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = ? ORDER BY column_name`, voiceTable).
		Scan(&columns).Error; err != nil {
		t.Fatalf("读取音色表列失败: %v", err)
	}
	want := map[string]bool{
		"id": true, "name": true, "model": true, "voice": true, "format": true,
		"status": true, "remark": true, "create_time": true, "update_time": true,
	}
	for _, column := range columns {
		delete(want, column)
	}
	if len(want) != 0 {
		t.Fatalf("音色表缺列: %v（实际列 %v）", want, columns)
	}
	var indexDef string
	if err := db.Raw(`SELECT indexdef FROM pg_indexes
		WHERE schemaname = current_schema() AND tablename = ? AND indexname = ?`,
		voiceTable, voiceTablePrefix()+"uk_plugin_ai_voice_name").Row().Scan(&indexDef); err != nil {
		t.Fatalf("读取音色名唯一索引失败: %v", err)
	}
	if !strings.Contains(indexDef, "UNIQUE") {
		t.Fatalf("音色名索引不是唯一索引: %s", indexDef)
	}

	// ② 闭集 CHECK 逐字含 'tts'（model.type 与 usage.call_type）。
	modelDef := voiceConstraintDef(t, db, table("plugin_ai_model"), voiceTablePrefix()+"ck_plugin_ai_model_type")
	if !strings.Contains(modelDef, "'tts'") {
		t.Fatalf("plugin_ai_model.type 的 CHECK 未含 'tts': %q", modelDef)
	}
	usageDef := voiceConstraintDef(t, db, table("plugin_ai_usage"), voiceTablePrefix()+"ck_plugin_ai_usage_call_type")
	if !strings.Contains(usageDef, "'tts'") {
		t.Fatalf("plugin_ai_usage.call_type 的 CHECK 未含 'tts': %q", usageDef)
	}
	// 旧闭集仍必须被收窄后的子集覆盖（chat/embedding 不能因为重建约束而丢失）。
	for _, value := range []string{"'chat'", "'embedding'"} {
		if !strings.Contains(modelDef, value) || !strings.Contains(usageDef, value) {
			t.Fatalf("重建后的 CHECK 丢了旧取值 %s: model=%q usage=%q", value, modelDef, usageDef)
		}
	}

	// ③ 真库落 type='tts' 的模型与 call_type='tts' 的 usage（撞旧 CHECK 时这里会 22014/23514）。
	model := Model{Name: "tts-db", Type: ModelTypeTTS, Status: StatusEnabled,
		Strategy: StrategyPriority, PriceInput: "1.000000", PriceOutput: "0.000000"}
	if err := store.CreateModel(ctx, &model); err != nil {
		t.Fatalf("落 type='tts' 的模型失败（DB CHECK 未放宽）: %v", err)
	}
	usage := Usage{RequestID: "req-tts-db", Attempt: 0, ModelID: model.ID, ModelName: model.Name,
		CallType: CallTypeTTS, BizType: "api", Source: publicSource, Stream: 0,
		PromptTokens: 7, TotalTokens: 7, TokensSource: TokensSourceEstimate,
		Cost: "0.000021", Currency: DefaultCurrency, Status: UsageStatusSuccess}
	if err := store.UpsertUsage(ctx, &usage); err != nil {
		t.Fatalf("落 call_type='tts' 的 usage 失败（DB CHECK 未放宽）: %v", err)
	}
	chain, err := store.UsageDetailChain(ctx, "req-tts-db")
	if err != nil || len(chain) != 1 || chain[0].CallType != CallTypeTTS {
		t.Fatalf("回读 usage 失败: %v / %+v", err, chain)
	}
}

// TestVoiceMigrationDownRestoresClosureDB 覆盖回滚判据：down 之后旧 CHECK 恢复、音色表消失。
func TestVoiceMigrationDownRestoresClosureDB(t *testing.T) {
	db := pluginAITestDB(t)
	voiceApplyMigrationFile(t, db, "6_voice.down.sql")

	modelDef := voiceConstraintDef(t, db, table("plugin_ai_model"), voiceTablePrefix()+"ck_plugin_ai_model_type")
	if strings.Contains(modelDef, "'tts'") {
		t.Fatalf("down 之后 plugin_ai_model.type 的 CHECK 仍允许 'tts': %q", modelDef)
	}
	if !strings.Contains(modelDef, "'chat'") || !strings.Contains(modelDef, "'embedding'") {
		t.Fatalf("down 之后的 CHECK 不完整: %q", modelDef)
	}
	usageDef := voiceConstraintDef(t, db, table("plugin_ai_usage"), voiceTablePrefix()+"ck_plugin_ai_usage_call_type")
	if strings.Contains(usageDef, "'tts'") {
		t.Fatalf("down 之后 plugin_ai_usage.call_type 的 CHECK 仍允许 'tts': %q", usageDef)
	}
	if voiceTableExists(t, db, table("plugin_ai_voice")) {
		t.Fatal("down 之后音色表应被删除")
	}

	// 旧闭集真的会拒：插入 type='tts' 的模型必须失败。
	store := NewStore(db)
	model := Model{Name: "tts-after-down", Type: ModelTypeTTS, Status: StatusEnabled,
		Strategy: StrategyPriority, PriceInput: "1.000000", PriceOutput: "0.000000"}
	if err := store.CreateModel(context.Background(), &model); err == nil {
		t.Fatal("down 之后落 type='tts' 必须被 DB CHECK 拒绝")
	}
}

// TestVoiceCRUDAndWheresDB 覆盖音色 CRUD 往返 + 高级检索（wheres 白名单在真库上生效）。
func TestVoiceCRUDAndWheresDB(t *testing.T) {
	db := pluginAITestDB(t)
	store := NewStore(db)
	engine := voiceDBEngine(voiceDBDeps(store, NewDashScopeTTSDriver(true)))

	// 新增（format 缺省 ⇒ mp3）。
	recorder := doRequest(engine, http.MethodPost, "/admin/plugin/ai/voice/create",
		`{"name":"龙小淳","model":"tts-db","voice":"longxiaochun_v2","remark":"女声"}`,
		nil)
	envelope := decodeVoiceEnvelope(t, recorder)
	if envelope.Code != 0 {
		t.Fatalf("新增音色失败: code=%d message=%s", envelope.Code, envelope.Message)
	}
	var created struct {
		ID uint64 `json:"id"`
	}
	if err := json.Unmarshal(envelope.Data, &created); err != nil || created.ID == 0 {
		t.Fatalf("新增回执缺 id: %v（%s）", err, string(envelope.Data))
	}

	// 重名（唯一键）必须给可读拒绝。
	recorder = doRequest(engine, http.MethodPost, "/admin/plugin/ai/voice/create",
		`{"name":"龙小淳","model":"tts-db","voice":"longxiaochun_v2"}`, nil)
	if envelope = decodeVoiceEnvelope(t, recorder); envelope.Code == 0 || !strings.Contains(envelope.Message, "已存在") {
		t.Fatalf("重名音色应被拒且文案可读，实际 code=%d message=%s", envelope.Code, envelope.Message)
	}

	// 单行回填。
	recorder = doRequest(engine, http.MethodGet,
		fmt.Sprintf("/admin/plugin/ai/voice/get/%d", created.ID), "", nil)
	envelope = decodeVoiceEnvelope(t, recorder)
	if envelope.Code != 0 {
		t.Fatalf("读取音色失败: %s", envelope.Message)
	}
	var detail struct {
		Row Voice `json:"row"`
	}
	if err := json.Unmarshal(envelope.Data, &detail); err != nil {
		t.Fatalf("回填不是 {row:{…}}: %v（%s）", err, string(envelope.Data))
	}
	if detail.Row.Name != "龙小淳" || detail.Row.Voice != "longxiaochun_v2" || detail.Row.Format != VoiceFormatDefault {
		t.Fatalf("回填字段不符: %+v", detail.Row)
	}

	// 保存（改名 + 换格式 + 停用）。
	recorder = doRequest(engine, http.MethodPost,
		fmt.Sprintf("/admin/plugin/ai/voice/update/%d", created.ID),
		`{"name":"龙小淳-v2","format":"wav","status":0}`, nil)
	if envelope = decodeVoiceEnvelope(t, recorder); envelope.Code != 0 {
		t.Fatalf("保存音色失败: %s", envelope.Message)
	}
	row, err := store.GetVoice(context.Background(), int64(created.ID))
	if err != nil {
		t.Fatalf("回读音色失败: %v", err)
	}
	if row.Name != "龙小淳-v2" || row.Format != VoiceFormatWAV || row.Status != StatusDisabled {
		t.Fatalf("保存未落库: %+v", row)
	}
	if row.Model != "tts-db" || row.Voice != "longxiaochun_v2" {
		t.Fatalf("未提供的列被误改: %+v", row)
	}

	// 高级检索：白名单内条件命中；白名单外字段得可读拒绝（不是 500、也不是静默忽略）。
	recorder = doRequest(engine, http.MethodPost, "/admin/plugin/ai/voice/list",
		`{"wheres":[{"wheres":[{"field":"voice","operator":"ILIKE","value":"longxiao"}]}]}`, nil)
	envelope = decodeVoiceEnvelope(t, recorder)
	if envelope.Code != 0 {
		t.Fatalf("白名单内 wheres 被拒: %s", envelope.Message)
	}
	var listResult struct {
		List  []Voice `json:"list"`
		Total int64   `json:"total"`
	}
	if err := json.Unmarshal(envelope.Data, &listResult); err != nil {
		t.Fatalf("列表回执不是 {list,total}: %v（%s）", err, string(envelope.Data))
	}
	if listResult.Total != 1 || len(listResult.List) != 1 {
		t.Fatalf("wheres 命中数不符: total=%d list=%d", listResult.Total, len(listResult.List))
	}
	recorder = doRequest(engine, http.MethodPost, "/admin/plugin/ai/voice/list",
		`{"wheres":[{"wheres":[{"field":"api_key","operator":"eq","value":"x"}]}]}`, nil)
	if envelope = decodeVoiceEnvelope(t, recorder); envelope.Code == 0 || !strings.Contains(envelope.Message, "白名单") {
		t.Fatalf("白名单外字段应被拒，实际 code=%d message=%s", envelope.Code, envelope.Message)
	}
	// 顶层筛选与快搜（status / model）。
	recorder = doRequest(engine, http.MethodPost, "/admin/plugin/ai/voice/list",
		`{"model":"tts-db","keyword":"longxiao","status":""}`, nil)
	envelope = decodeVoiceEnvelope(t, recorder)
	if envelope.Code != 0 {
		t.Fatalf("顶层筛选请求被拒: %s", envelope.Message)
	}
	if err := json.Unmarshal(envelope.Data, &listResult); err != nil || listResult.Total != 1 {
		t.Fatalf("顶层筛选命中数不符: %v / total=%d", err, listResult.Total)
	}

	// 删除。
	recorder = doRequest(engine, http.MethodPost, "/admin/plugin/ai/voice/delete",
		fmt.Sprintf(`{"pks":["%d"]}`, created.ID), nil)
	if envelope = decodeVoiceEnvelope(t, recorder); envelope.Code != 0 {
		t.Fatalf("删除音色失败: %s", envelope.Message)
	}
	if _, err := store.GetVoice(context.Background(), int64(created.ID)); err == nil {
		t.Fatal("删除后仍能读到音色行")
	}
}

// TestVoicePreviewEndToEndDB 覆盖试听的端到端判据：恰好一次上游、音频字节直出、不落盘、按口径记账。
func TestVoicePreviewEndToEndDB(t *testing.T) {
	db := pluginAITestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	// 夹具：type=tts 的模型 + 指向伪上游的渠道 + 绑定 + 一条音色。
	mock := newTTSMockUpstream(t, ttsMockAudioBytes)
	model := Model{Name: "tts-preview", Type: ModelTypeTTS, Status: StatusEnabled,
		Strategy: StrategyPriority, PriceInput: "3.000000", PriceOutput: "0.000000"}
	if err := store.CreateModel(ctx, &model); err != nil {
		t.Fatalf("建模型失败: %v", err)
	}
	channel := Channel{Name: "tts-preview-ch", Vendor: "qwen", Driver: DashScopeTTSDriverName,
		BaseURL: mock.speechURL(), APIKey: "sk-upstream-key", Model: "cosyvoice-v2",
		Timeout: 10, Status: StatusEnabled, Priority: 1, Weight: 1}
	if err := store.CreateChannel(ctx, &channel); err != nil {
		t.Fatalf("建渠道失败: %v", err)
	}
	if _, err := store.ReplaceBindings(ctx, model.ID, []ModelChannel{
		{ModelID: model.ID, ChannelID: uint64(channel.ID), Priority: 1, Weight: 1, Status: StatusEnabled},
	}); err != nil {
		t.Fatalf("建绑定失败: %v", err)
	}
	voiceRow := Voice{Name: "龙小淳", Model: model.Name, Voice: "longxiaochun_v2",
		Format: VoiceFormatMP3, Status: StatusEnabled}
	if err := store.CreateVoice(ctx, &voiceRow); err != nil {
		t.Fatalf("建音色失败: %v", err)
	}

	engine := voiceDBEngine(voiceDBDeps(store, NewDashScopeTTSDriver(true)))

	// 「不落盘」判据：进程临时目录在本用例期间不得新增任何文件（试听只走内存与响应）。
	tmpDir := t.TempDir()
	t.Setenv("TMPDIR", tmpDir)

	recorder := doRequest(engine, http.MethodPost, "/admin/plugin/ai/voice/preview",
		fmt.Sprintf(`{"id":%d}`, voiceRow.ID), nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("试听状态码 = %d（body=%s）", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("Content-Type") != "audio/mpeg" {
		t.Fatalf("试听 Content-Type = %q，期望 audio/mpeg", recorder.Header().Get("Content-Type"))
	}
	if recorder.Body.String() != string(ttsMockAudio) {
		t.Fatalf("试听响应不是逐位音频字节: %q", recorder.Body.String())
	}
	if mock.hits() != 1 {
		t.Fatalf("试听必须恰好命中上游一次，实际 %d 次", mock.hits())
	}
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatalf("读取临时目录失败: %v", err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("试听不得落盘，临时目录出现新文件: %v", names)
	}

	// 记账：一行 call_type='tts'，字符数=默认试听文本长度，cost 与 字符数/1e6×price_input 逐位一致。
	chars := speechCharCount(voicePreviewDefaultText)
	var rows []Usage
	if err := db.Where(`"call_type" = ?`, CallTypeTTS).Find(&rows).Error; err != nil {
		t.Fatalf("读取 usage 失败: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("usage 行数 = %d，期望 1", len(rows))
	}
	wantCost, err := ComputeCost(chars, 0, "3.000000", "0.000000")
	if err != nil {
		t.Fatalf("计算期望成本失败: %v", err)
	}
	usage := rows[0]
	if usage.PromptTokens != chars || usage.TotalTokens != chars || usage.CompletionTokens != 0 {
		t.Fatalf("试听计数 = %d/%d/%d，期望 %d/0/%d",
			usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens, chars, chars)
	}
	if usage.Cost != wantCost {
		t.Fatalf("试听 cost = %q，期望 %q", usage.Cost, wantCost)
	}
	if usage.BizType != voicePreviewBizType || usage.Source != voicePreviewSource {
		t.Fatalf("试听归属 = %s/%s，期望 %s/%s",
			usage.BizType, usage.Source, voicePreviewBizType, voicePreviewSource)
	}
	if usage.Status != UsageStatusSuccess || usage.ChannelID != channel.ID {
		t.Fatalf("试听 usage 状态/渠道不符: %+v", usage)
	}

	// 试听失败路径：音色不存在 ⇒ 后台面 code=1（不是 500 形状）。
	recorder = doRequest(engine, http.MethodPost, "/admin/plugin/ai/voice/preview", `{"id":999999}`, nil)
	if envelope := decodeVoiceEnvelope(t, recorder); envelope.Code != 1 {
		t.Fatalf("不存在的音色应回 code=1，实际 %d", envelope.Code)
	}
	if mock.hits() != 1 {
		t.Fatalf("失败试听不得触达上游，实际命中 %d 次", mock.hits())
	}
}
