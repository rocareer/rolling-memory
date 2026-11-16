// plugin: ai v0.2.0
// task_db_test.go 是异步任务的**库级**用例（DSN 门控 + 真 Postgres）：迁移 DDL、并发幂等、
// SKIP LOCKED 领取、租约过期重领、worker 端到端（真库 + 真 Service + 假驱动）、导出与 wheres 列表。
//
// 用法（**必须指向临时库**：用例会 drop/重建本插件的全部 plugin_ai_* 表）：
//
//	PLUGIN_AI_TEST_DSN='postgres://用户:口令@主机:端口/临时库?sslmode=disable' \
//	  go test -count=1 -run TestTask -v ./plugins/ai/
//
// 未设 DSN ⇒ 整组跳过（默认 `go test ./plugins/ai/` 不连库、不需要 Postgres）。
//
// 表结构与 store_db_test.go 的先例一致：**按 migrations/ 目录按编号升序应用全部 up 迁移**
// （本包自带这份 helper，不依赖其它用例文件的重构进度），清场也按前缀扫描——新增迁移自动纳入。
//
// 与 store_db_test.go 的两点差异（如实登记）：
//   - 本文件会**自建自删**一张最小的 `admin_plugins` 存根（只有 name/status 两列）：worker 每轮直读
//     `<prefix>admin_plugins.status` 是生产语义，要真验它就必须有这张表，而临时库默认没有
//     （这正是防误指向门禁的判据）。存根由 t.Cleanup 删除；进程中途被杀会让它残留，
//     后续用例的门禁会拒跑并提示手工 `DROP TABLE admin_plugins` 清理。
//   - 本文件不复用 store_db_test.go 的 pluginAITestDB（那份只建 1 号迁移的 5 张表，且其脚手架
//     正在并行改动），避免跨文件耦合与符号撞名。
package ai

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-go-hub/ai-go-admin/internal/infra/config"

	"github.com/gin-gonic/gin"
	"gorm.io/datatypes"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// taskTestDSNEnv 库级用例的 DSN 门控（与 store_db_test.go 同名的环境变量，**常量自带**避免跨文件耦合）。
const taskTestDSNEnv = "PLUGIN_AI_TEST_DSN"

// ===== 库与迁移的脚手架 =====

// taskTestDB 连临时库、清场、按编号升序应用全部 up 迁移，收尾再清场。
func taskTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv(taskTestDSNEnv))
	if dsn == "" {
		t.Skip("需 " + taskTestDSNEnv + "（指向**临时库**；勿指向开发/生产库）")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("连接临时库失败: %v", err)
	}
	// 连接池必须收口（与 store_db_test.go 的 pluginAITestDB 同口径）：不收口时空闲连接随用例数
	// 累积，会把共享 PG 的 max_connections 打满 ⇒ 53300 假红。Cleanup 是 LIFO：清场先跑、再关池。
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("取 sql.DB 失败: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	// 防误指向真库：带宿主台账（admin_plugins）的库一律拒绝——本用例会重建插件表。
	var hostTable sql.NullString
	if err := db.Raw("SELECT to_regclass('admin_plugins')::text").Row().Scan(&hostTable); err != nil {
		t.Fatalf("探测宿主台账失败: %v", err)
	}
	if hostTable.Valid && hostTable.String != "" {
		t.Fatalf("%s 指向的库存在 admin_plugins（疑似开发/生产库，或上一次 task_db_test 中途崩溃的存根残留）："+
			"本用例会 drop/重建 plugin_ai_* 表，拒绝执行；确认是临时库残留后手工 `DROP TABLE admin_plugins` 清理即可",
			taskTestDSNEnv)
	}

	if err := taskTestDropPluginTables(db); err != nil {
		t.Fatalf("清场失败: %v", err)
	}
	taskTestApplyAllMigrations(t, db)
	t.Cleanup(func() {
		if err := taskTestDropPluginTables(db); err != nil {
			t.Errorf("收尾清场失败: %v（临时库残留 plugin_ai_* 表）", err)
		}
	})
	return db
}

// taskTestDropPluginTables 按**前缀扫描**清掉本插件的全部物理表（不猜表名：新增迁移自动纳入）；
// 同时清掉可能残留的版本表（plugin_ai_migrations 也在同一前缀下）。
func taskTestDropPluginTables(db *gorm.DB) error {
	prefix := config.Get().Database.Prefix
	type tableRow struct {
		TableName string `gorm:"column:tablename"`
	}
	var rows []tableRow
	if err := db.Raw("SELECT tablename FROM pg_tables WHERE schemaname = current_schema()").Scan(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		if !strings.HasPrefix(row.TableName, prefix+"plugin_ai_") {
			continue
		}
		if err := db.Exec("DROP TABLE IF EXISTS " + quoteIdent(row.TableName)).Error; err != nil {
			return err
		}
	}
	return nil
}

// taskTestApplyAllMigrations 按编号升序应用 migrations/*.up.sql（__PREFIX__ 替换与安装器同口径）。
func taskTestApplyAllMigrations(t *testing.T, db *gorm.DB) {
	t.Helper()
	entries, err := os.ReadDir("migrations")
	if err != nil {
		t.Fatalf("读取迁移目录失败: %v", err)
	}
	ups := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".up.sql") {
			ups = append(ups, entry.Name())
		}
	}
	sort.Slice(ups, func(i, j int) bool { return taskTestMigrationNumber(t, ups[i]) < taskTestMigrationNumber(t, ups[j]) })
	if len(ups) == 0 {
		t.Fatal("migrations/ 下没有 up 迁移")
	}
	for _, name := range ups {
		if err := db.Exec(taskTestReadMigration(t, name)).Error; err != nil {
			t.Fatalf("应用迁移 %s 失败: %v", name, err)
		}
	}
}

// taskTestMigrationNumber 取迁移文件名开头的编号（5_task.up.sql ⇒ 5）。
func taskTestMigrationNumber(t *testing.T, name string) int {
	t.Helper()
	head, _, found := strings.Cut(name, "_")
	if !found {
		t.Fatalf("迁移文件名 %q 不是 <编号>_<名>.up.sql 形状", name)
	}
	number, err := strconv.Atoi(head)
	if err != nil {
		t.Fatalf("迁移文件名 %q 的编号不是整数: %v", name, err)
	}
	return number
}

// taskTestReadMigration 读迁移文件并替换 __PREFIX__（与安装器同口径）。
func taskTestReadMigration(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("migrations", name))
	if err != nil {
		t.Fatalf("读取迁移文件 %s 失败: %v", name, err)
	}
	return strings.ReplaceAll(string(raw), "__PREFIX__", config.Get().Database.Prefix)
}

// ===== 假驱动（真 Service 用；只覆盖 worker 需要的两个方法语义）=====

type taskTestDriver struct {
	chatErr  error
	embedErr error
	// embedResult 覆盖默认的「每条输入一条 3 维向量」
	embedResult func(EmbeddingRequest) (*EmbeddingResult, error)
	mu          sync.Mutex
	embedCalls  []EmbeddingRequest
}

func (d *taskTestDriver) Chat(_ context.Context, _ Channel, req ChatRequest) (*ChatResult, error) {
	if d.chatErr != nil {
		return nil, d.chatErr
	}
	return &ChatResult{ID: "cmpl-db", Model: req.Model, Content: "库级回复", FinishReason: "stop",
		PromptTokens: 4, CompletionTokens: 6, TotalTokens: 10}, nil
}

func (d *taskTestDriver) ChatStream(context.Context, Channel, ChatRequest, func([]byte) error) (*ChatResult, error) {
	return nil, fmt.Errorf("异步任务不走流式")
}

func (d *taskTestDriver) Embeddings(_ context.Context, _ Channel, req EmbeddingRequest) (*EmbeddingResult, error) {
	d.mu.Lock()
	d.embedCalls = append(d.embedCalls, req)
	fn := d.embedResult
	d.mu.Unlock()
	if d.embedErr != nil {
		return nil, d.embedErr
	}
	if fn != nil {
		return fn(req)
	}
	items := embeddingInputItems(req.Input)
	result := &EmbeddingResult{Model: req.Model, PromptTokens: len(items), TotalTokens: len(items)}
	for index := range items {
		result.Data = append(result.Data, Embedding{Index: index, Object: "embedding", Vector: []float64{float64(index), 1, 2}})
	}
	return result, nil
}

func (d *taskTestDriver) Models(context.Context, Channel) ([]string, error) {
	return []string{"m"}, nil
}

// ===== 假执行面的状态探针（断言「一轮内 pending → processing → success」）=====

type taskStatusProbe struct {
	store *Store
	inner taskExecutor
	id    uint64
	mu    sync.Mutex
	first string
}

func (p *taskStatusProbe) record(ctx context.Context) {
	if p.id == 0 {
		return
	}
	row, err := p.store.GetTask(ctx, p.id)
	if err != nil {
		return
	}
	p.mu.Lock()
	if p.first == "" {
		p.first = row.Status
	}
	p.mu.Unlock()
}

func (p *taskStatusProbe) Chat(ctx context.Context, in ChatInput) (*ChatResult, error) {
	p.record(ctx)
	return p.inner.Chat(ctx, in)
}

func (p *taskStatusProbe) Embeddings(ctx context.Context, in EmbeddingInput) (*EmbeddingResult, error) {
	p.record(ctx)
	return p.inner.Embeddings(ctx, in)
}

func (p *taskStatusProbe) observed() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.first
}

// ===== 宿主台账存根 =====

type taskTestPluginStub struct {
	db   *gorm.DB
	name string
}

// taskTestStubPluginLedger 建一张最小 admin_plugins 存根并写入 ai 行（见文件头注的取舍与风险）。
func taskTestStubPluginLedger(t *testing.T, db *gorm.DB, status string) *taskTestPluginStub {
	t.Helper()
	if err := db.Exec(`CREATE TABLE IF NOT EXISTS "admin_plugins" (` +
		`"id" bigserial PRIMARY KEY, "name" varchar(32) NOT NULL DEFAULT '', "status" varchar(16) NOT NULL DEFAULT 'failed')`).Error; err != nil {
		t.Fatalf("建 admin_plugins 存根失败: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Exec(`DROP TABLE IF EXISTS "admin_plugins"`).Error; err != nil {
			t.Errorf("清 admin_plugins 存根失败: %v（残留会让后续用例的防误指向门禁拒跑）", err)
		}
	})
	stub := &taskTestPluginStub{db: db, name: Name}
	stub.setStatus(t, status)
	return stub
}

func (s *taskTestPluginStub) setStatus(t *testing.T, status string) {
	t.Helper()
	if err := s.db.Exec(`DELETE FROM "admin_plugins" WHERE "name" = ?`, s.name).Error; err != nil {
		t.Fatalf("清理存根行失败: %v", err)
	}
	if err := s.db.Exec(`INSERT INTO "admin_plugins" ("name", "status") VALUES (?, ?)`, s.name, status).Error; err != nil {
		t.Fatalf("写存根行失败: %v", err)
	}
}

// ===== 其它脚手架 =====

// taskTestChatPayload 造一份合法 chat 载荷。
func taskTestChatPayload(t *testing.T) datatypes.JSON {
	t.Helper()
	raw, err := encodeTaskPayload(taskPayload{
		Model: "gpt-db", Messages: []Message{{Role: "user", Content: "你好"}},
		BizType: "admin", Source: "admin:task",
	})
	if err != nil {
		t.Fatalf("载荷编码失败: %v", err)
	}
	return raw
}

// taskTestEmbeddingPayload 造一份 n 条输入的 embedding 载荷。
func taskTestEmbeddingPayload(t *testing.T, count int) datatypes.JSON {
	t.Helper()
	inputs := make([]string, 0, count)
	for i := 0; i < count; i++ {
		inputs = append(inputs, fmt.Sprintf("文本-%d", i))
	}
	raw, err := encodeTaskPayload(taskPayload{
		Model: "bge-db", Inputs: inputs, BizType: "admin", Source: "admin:task",
	})
	if err != nil {
		t.Fatalf("载荷编码失败: %v", err)
	}
	return raw
}

// taskTestSeedRoute 在库里造「一个启用的逻辑模型 + 一个启用渠道 + 一条绑定」，返回模型 ID。
func taskTestSeedRoute(t *testing.T, store *Store, modelType, modelName, channelName string) uint64 {
	t.Helper()
	ctx := context.Background()
	model := Model{Name: modelName, Type: modelType, Strategy: StrategyPriority,
		PriceInput: "0.100000", PriceOutput: "0.200000", Status: StatusEnabled}
	if err := store.CreateModel(ctx, &model); err != nil {
		t.Fatalf("建模型失败: %v", err)
	}
	channel := Channel{Name: channelName, Model: "upstream-model", BaseURL: "https://example.invalid/v1",
		APIKey: "sk-verify", Timeout: 5, Status: StatusEnabled}
	if err := store.CreateChannel(ctx, &channel); err != nil {
		t.Fatalf("建渠道失败: %v", err)
	}
	if _, err := store.ReplaceBindings(ctx, model.ID, []ModelChannel{
		{ModelID: model.ID, ChannelID: uint64(channel.ID), Priority: 1, Weight: 1, Status: StatusEnabled},
	}); err != nil {
		t.Fatalf("建绑定失败: %v", err)
	}
	return model.ID
}

// taskTestWorker 造一个「读真库 + 走真 Service + 用真台账自查」的 worker。
func taskTestWorker(t *testing.T, db *gorm.DB, store *Store, executor taskExecutor, maxAttempts int) *taskWorker {
	t.Helper()
	worker := newTaskWorker(store, executor)
	worker.enabled = func(ctx context.Context) (bool, error) { return taskPluginEnabled(ctx, db) }
	worker.settings = taskTestSettings(maxAttempts)
	worker.lease = time.Minute
	return worker
}

// taskTestHarness HTTP 侧的依赖注入（不挂 AdminAuth/AdminPermission：那两件由装配面登记的
// adminRoutes 承担，此处只验 handler 的数据面）。
func taskTestDeps(store *Store) deps {
	return deps{
		loadSettings: func(context.Context) (pluginSettings, error) { return pluginSettings{}, nil },
		loadStore:    func(context.Context) (*Store, error) { return store, nil },
		loadService:  func(context.Context, pluginSettings) (*Service, error) { return nil, ErrStoreUnavailable },
	}
}

func taskTestEngine(d deps) *gin.Engine {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.POST("/task/list", d.taskList)
	engine.GET("/task/get/:pk", d.taskGet)
	engine.POST("/task/submit", d.taskSubmit)
	engine.GET("/task/export/:pk", d.taskExport)
	return engine
}

type taskAdminEnvelope struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func taskTestCall(t *testing.T, engine *gin.Engine, method, path, body string) (*httptest.ResponseRecorder, taskAdminEnvelope) {
	t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)
	var envelope taskAdminEnvelope
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("%s %s 的响应不是 JSON 信封: %v（body=%s）", method, path, err, recorder.Body.String())
	}
	return recorder, envelope
}

// taskCountingWriter 记录「Write 被调用了几次」：导出必须是**逐行写**（不是整包一次 Write）。
type taskCountingWriter struct {
	header http.Header
	body   bytes.Buffer
	writes int
	status int
}

func (w *taskCountingWriter) Header() http.Header { return w.header }
func (w *taskCountingWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
}
func (w *taskCountingWriter) Write(p []byte) (int, error) {
	w.writes++
	return w.body.Write(p)
}

// ===== ① 迁移 DDL：表 / 列 / 索引 / 约束 / down 回滚 =====

func TestTaskMigrationDDLDB(t *testing.T) {
	db := taskTestDB(t)
	prefix := config.Get().Database.Prefix
	phys := table("plugin_ai_task")

	// 表存在
	var reg sql.NullString
	if err := db.Raw("SELECT to_regclass(?)::text", phys).Row().Scan(&reg); err != nil {
		t.Fatalf("探测表失败: %v", err)
	}
	if !reg.Valid || reg.String == "" {
		t.Fatalf("迁移 5 未建出表 %s", phys)
	}

	// 列集合与类型（\\d 的列判据）
	type columnRow struct {
		ColumnName string `gorm:"column:column_name"`
		DataType   string `gorm:"column:data_type"`
	}
	var columns []columnRow
	if err := db.Raw(`SELECT column_name, data_type FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = ?`, phys).Scan(&columns).Error; err != nil {
		t.Fatalf("读列信息失败: %v", err)
	}
	got := map[string]string{}
	for _, column := range columns {
		got[column.ColumnName] = column.DataType
	}
	want := map[string]string{
		"id": "bigint", "task_type": "character varying", "status": "character varying",
		"payload": "jsonb", "result": "jsonb", "error": "character varying",
		"source": "character varying", "biz_type": "character varying", "request_id": "character varying",
		"attempt": "integer", "progress_done": "integer", "progress_total": "integer",
		"locked_until": "timestamp with time zone", "create_time": "bigint", "update_time": "bigint",
	}
	for name, wantType := range want {
		if got[name] != wantType {
			t.Errorf("列 %s 类型 = %q，期望 %q（实际列集 %v）", name, got[name], wantType, got)
		}
	}

	// 状态索引（acceptance：状态索引存在）
	var statusIndexDef string
	if err := db.Raw(`SELECT indexdef FROM pg_indexes WHERE schemaname = current_schema()
		AND tablename = ? AND indexname = ?`, phys, prefix+"idx_plugin_ai_task_status").Scan(&statusIndexDef).Error; err != nil {
		t.Fatalf("读状态索引失败: %v", err)
	}
	if !strings.Contains(statusIndexDef, "plugin_ai_task") || !strings.Contains(statusIndexDef, "status") {
		t.Fatalf("状态索引缺失或不是 status 索引: %q", statusIndexDef)
	}

	// request_id 的部分唯一约束（acceptance：UNIQUE (request_id) WHERE request_id <> ''）
	var uniqueRow struct {
		Unique    bool           `gorm:"column:is_unique"`
		Predicate sql.NullString `gorm:"column:predicate"`
	}
	if err := db.Raw(`SELECT i.indisunique AS is_unique, pg_get_expr(i.indpred, i.indrelid) AS predicate
		FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid WHERE c.relname = ?`,
		prefix+"uk_plugin_ai_task_request_id").Scan(&uniqueRow).Error; err != nil {
		t.Fatalf("读 request_id 索引失败: %v", err)
	}
	if !uniqueRow.Unique {
		t.Fatal("request_id 索引不是唯一索引（幂等语义缺失）")
	}
	if !uniqueRow.Predicate.Valid || !strings.Contains(uniqueRow.Predicate.String, "request_id") {
		t.Fatalf("request_id 索引缺少部分谓词（空值应被排除）：%v", uniqueRow.Predicate)
	}

	// CHECK 闭集（task_type / status）
	for _, check := range []struct {
		name string
		want []string
	}{
		{prefix + "ck_plugin_ai_task_type", []string{"'chat'", "'embedding'"}},
		{prefix + "ck_plugin_ai_task_status", []string{"'pending'", "'processing'", "'success'", "'failed'"}},
	} {
		var def string
		if err := db.Raw("SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conname = ?", check.name).Scan(&def).Error; err != nil {
			t.Fatalf("读约束 %s 失败: %v", check.name, err)
		}
		for _, fragment := range check.want {
			if !strings.Contains(def, fragment) {
				t.Fatalf("约束 %s 缺 %s: %q", check.name, fragment, def)
			}
		}
	}

	// down 可回滚：执行 down 后表消失（后续用例的 helper 会重建）
	if err := db.Exec(taskTestReadMigration(t, "5_task.down.sql")).Error; err != nil {
		t.Fatalf("down 迁移执行失败: %v", err)
	}
	var after sql.NullString
	if err := db.Raw("SELECT to_regclass(?)::text", phys).Row().Scan(&after); err != nil {
		t.Fatalf("探测 down 后的表失败: %v", err)
	}
	if after.Valid && after.String != "" {
		t.Fatalf("down 迁移后表仍存在: %s", after.String)
	}
}

// ===== ② 幂等提交（含并发）=====

func TestTaskSubmitIdempotentDB(t *testing.T) {
	db := taskTestDB(t)
	store := NewStore(db)
	ctx := context.Background()
	payload := taskTestChatPayload(t)
	input := TaskSubmitInput{
		TaskType: TaskTypeChat, Payload: payload, RequestID: "rid-idem",
		BizType: "admin", Source: "admin:task", ProgressTotal: 1,
	}
	physTable := quoteIdent(table("plugin_ai_task"))

	first, created, err := store.SubmitTask(ctx, input)
	if err != nil || !created {
		t.Fatalf("首次提交应新建: created=%v err=%v", created, err)
	}
	if first.Status != TaskStatusPending || first.RequestID != "rid-idem" {
		t.Fatalf("新行口径不符: %+v", first)
	}

	second, created, err := store.SubmitTask(ctx, input)
	if err != nil {
		t.Fatalf("重复提交不应报错: %v", err)
	}
	if created || second.ID != first.ID {
		t.Fatalf("同 request_id 重复提交必须回既有行: created=%v id=%d/%d", created, second.ID, first.ID)
	}

	// 并发同 request_id：32 个 goroutine 只留一行
	const workers = 32
	ids := make([]uint64, workers)
	errs := make([]error, workers)
	var wg sync.WaitGroup
	for index := 0; index < workers; index++ {
		wg.Add(1)
		go func(slot int) {
			defer wg.Done()
			row, _, err := store.SubmitTask(ctx, input)
			if err != nil {
				errs[slot] = err
				return
			}
			ids[slot] = row.ID
		}(index)
	}
	wg.Wait()
	for index, err := range errs {
		if err != nil {
			t.Fatalf("并发提交失败（第 %d 个）: %v", index, err)
		}
		if index > 0 && ids[index] != ids[0] {
			t.Fatalf("并发提交应回同一行：%d vs %d", ids[index], ids[0])
		}
	}
	var count int64
	if err := db.Raw("SELECT COUNT(*) FROM "+physTable+` WHERE "request_id" = ?`, "rid-idem").Scan(&count).Error; err != nil {
		t.Fatalf("计数失败: %v", err)
	}
	if count != 1 {
		t.Fatalf("并发幂等后应只有 1 行，实际 %d 行", count)
	}
}

// TestTaskEmptyRequestIDConcurrentNoCollisionDB 空 request_id 语义：部分唯一索引排除空串 ⇒
// 并发空提交各成一行（不互撞），这正是「列 not null default ” + 部分索引」与「唯一索引直接建在列上」
// 的关键差别。
func TestTaskEmptyRequestIDConcurrentNoCollisionDB(t *testing.T) {
	db := taskTestDB(t)
	store := NewStore(db)
	ctx := context.Background()
	payload := taskTestChatPayload(t)

	const workers = 32
	errs := make([]error, workers)
	createds := make([]bool, workers)
	var wg sync.WaitGroup
	for index := 0; index < workers; index++ {
		wg.Add(1)
		go func(slot int) {
			defer wg.Done()
			_, created, err := store.SubmitTask(ctx, TaskSubmitInput{
				TaskType: TaskTypeChat, Payload: payload, RequestID: "",
				BizType: "admin", Source: "admin:task", ProgressTotal: 1,
			})
			errs[slot], createds[slot] = err, created
		}(index)
	}
	wg.Wait()
	for index := range errs {
		if errs[index] != nil {
			t.Fatalf("空 request_id 并发提交不应失败（第 %d 个）: %v", index, errs[index])
		}
		if !createds[index] {
			t.Fatalf("空 request_id 不参与幂等：第 %d 个应新建", index)
		}
	}
	var count int64
	if err := db.Raw(`SELECT COUNT(*) FROM ` + quoteIdent(table("plugin_ai_task")) + ` WHERE "request_id" = ''`).Scan(&count).Error; err != nil {
		t.Fatalf("计数失败: %v", err)
	}
	if count != workers {
		t.Fatalf("空 request_id 应各成一行（%d 行），实际 %d 行", workers, count)
	}
}

// ===== ③ 领取：SKIP LOCKED 并发恰一人领到 =====

func TestTaskClaimSkipLockedSingleWinnerDB(t *testing.T) {
	db := taskTestDB(t)
	store := NewStore(db)
	ctx := context.Background()
	row, _, err := store.SubmitTask(ctx, TaskSubmitInput{
		TaskType: TaskTypeChat, Payload: taskTestChatPayload(t), RequestID: "rid-claim",
		BizType: "admin", Source: "admin:task", ProgressTotal: 1,
	})
	if err != nil {
		t.Fatalf("提交失败: %v", err)
	}

	const workers = 32
	type claimResult struct {
		row *claimedTask
		err error
	}
	results := make([]claimResult, workers)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for index := 0; index < workers; index++ {
		wg.Add(1)
		go func(slot int) {
			defer wg.Done()
			<-start
			claimed, err := store.claimTask(ctx, time.Minute)
			results[slot] = claimResult{row: claimed, err: err}
		}(index)
	}
	close(start)
	wg.Wait()

	winners := 0
	for index, result := range results {
		if result.err != nil {
			t.Fatalf("并发领取报错（第 %d 个）: %v", index, result.err)
		}
		if result.row != nil {
			winners++
			if result.row.ID != row.ID || result.row.Attempt != 1 || result.row.Status != TaskStatusProcessing {
				t.Fatalf("领到的行口径不符: %+v", result.row)
			}
		}
	}
	if winners != 1 {
		t.Fatalf("SKIP LOCKED 并发领取应恰一人领到，实际 %d 人", winners)
	}

	got, err := store.GetTask(ctx, row.ID)
	if err != nil {
		t.Fatalf("读回失败: %v", err)
	}
	if got.Status != TaskStatusProcessing || got.Attempt != 1 || got.LockedUntil == nil {
		t.Fatalf("库内终态不符: status=%s attempt=%d locked=%v", got.Status, got.Attempt, got.LockedUntil)
	}
}

// ===== ④ 租约过期的重领与启动复位 =====

func TestTaskLeaseExpiryReclaimAndStartupResetDB(t *testing.T) {
	db := taskTestDB(t)
	store := NewStore(db)
	ctx := context.Background()
	physTable := quoteIdent(table("plugin_ai_task"))
	row, _, err := store.SubmitTask(ctx, TaskSubmitInput{
		TaskType: TaskTypeChat, Payload: taskTestChatPayload(t), RequestID: "rid-lease",
		BizType: "admin", Source: "admin:task", ProgressTotal: 1,
	})
	if err != nil {
		t.Fatalf("提交失败: %v", err)
	}

	// 现场：上一进程死在执行中（processing + 租约已过期）
	if err := db.Exec("UPDATE "+physTable+` SET "status" = ?, "attempt" = 1, "locked_until" = now() - interval '1 minute' WHERE "id" = ?`,
		TaskStatusProcessing, row.ID).Error; err != nil {
		t.Fatalf("造租约过期现场失败: %v", err)
	}

	claimed, err := store.claimTask(ctx, time.Minute)
	if err != nil {
		t.Fatalf("重领失败: %v", err)
	}
	if claimed == nil || claimed.ID != row.ID || claimed.Attempt != 2 {
		t.Fatalf("租约过期的行应被重领且 attempt+1: %+v", claimed)
	}

	// 启动复位：把同一行再造成过期现场，resetStaleTaskLeases 应把它退回 pending
	if err := db.Exec("UPDATE "+physTable+` SET "status" = ?, "locked_until" = now() - interval '1 minute' WHERE "id" = ?`,
		TaskStatusProcessing, row.ID).Error; err != nil {
		t.Fatalf("造复位现场失败: %v", err)
	}
	reset, err := store.resetStaleTaskLeases(ctx)
	if err != nil {
		t.Fatalf("启动复位失败: %v", err)
	}
	if reset != 1 {
		t.Fatalf("应复位 1 行，实际 %d 行", reset)
	}
	got, err := store.GetTask(ctx, row.ID)
	if err != nil {
		t.Fatalf("读回失败: %v", err)
	}
	if got.Status != TaskStatusPending || got.LockedUntil != nil {
		t.Fatalf("复位后应为 pending 且无租约: status=%s locked=%v", got.Status, got.LockedUntil)
	}
	if got.Attempt != 2 {
		t.Fatalf("复位不得改 attempt（attempt 由领取 +1）：%d", got.Attempt)
	}
}

// TestTaskTerminalWritesGuardedByAttemptDB 是三个终态写口的**写侧守卫**真库判据（store_task.go 的
// updateOwnedTask）：`status='processing' AND attempt=?` 不命中 ⇒ 0 行 + ErrTaskLeaseLost + 库内一格不变。
// 为什么必须真库：判据是「WHERE 命中几行」（RowsAffected）——GORM 的 map 更新 + GREATEST/::jsonb 表达式
// 只有在真 PG 上才证得动；这也正是「终态取最后写入者」的堵口。
func TestTaskTerminalWritesGuardedByAttemptDB(t *testing.T) {
	db := taskTestDB(t)
	store := NewStore(db)
	ctx := context.Background()
	physTable := quoteIdent(table("plugin_ai_task"))
	row, _, err := store.SubmitTask(ctx, TaskSubmitInput{
		TaskType: TaskTypeChat, Payload: taskTestChatPayload(t), RequestID: "rid-owned-db",
		BizType: "admin", Source: "admin:task", ProgressTotal: 1,
	})
	if err != nil {
		t.Fatalf("提交失败: %v", err)
	}
	// 现场：该行已被另一个副本重领（processing + attempt=2），而旧持有者手里是 attempt=1
	if err := db.Exec("UPDATE "+physTable+` SET "status" = ?, "attempt" = 2,`+
		` "locked_until" = now() + interval '1 minute' WHERE "id" = ?`,
		TaskStatusProcessing, row.ID).Error; err != nil {
		t.Fatalf("造重领现场失败: %v", err)
	}

	for name, call := range map[string]func() error{
		"markTaskSuccess": func() error { return store.markTaskSuccess(ctx, row.ID, 1, datatypes.JSON(`{"stale":true}`)) },
		"markTaskFailed":  func() error { return store.markTaskFailed(ctx, row.ID, 1, "旧持有者的失败") },
		"requeueTask":     func() error { return store.requeueTask(ctx, row.ID, 1, "旧持有者退回 pending") },
	} {
		if err := call(); !errors.Is(err, ErrTaskLeaseLost) {
			t.Fatalf("%s：attempt 落后（已被重领）必须拒绝写，实际 err=%v", name, err)
		}
	}
	got, err := store.GetTask(ctx, row.ID)
	if err != nil {
		t.Fatalf("读回失败: %v", err)
	}
	if got.Status != TaskStatusProcessing || got.Attempt != 2 || got.Result != nil || got.Error != "" || got.LockedUntil == nil {
		t.Fatalf("被拒的写入不得改任何列: status=%s attempt=%d result=%s error=%q locked=%v",
			got.Status, got.Attempt, string(got.Result), got.Error, got.LockedUntil)
	}

	// still-holder（attempt 相等）⇒ 照常落 success 并释放租约。
	if err := store.markTaskSuccess(ctx, row.ID, 2, datatypes.JSON(`{"ok":true}`)); err != nil {
		t.Fatalf("still-holder 写成功: %v", err)
	}
	got, err = store.GetTask(ctx, row.ID)
	if err != nil {
		t.Fatalf("读回失败: %v", err)
	}
	if got.Status != TaskStatusSuccess || got.LockedUntil != nil || len(got.Result) == 0 {
		t.Fatalf("still-holder 写成功应落 success 且释放租约: status=%s locked=%v result=%s",
			got.Status, got.LockedUntil, string(got.Result))
	}
}

// ===== ⑤ worker 端到端：真库 + 真 Service（假驱动）+ 真台账自查 =====

func TestTaskWorkerEndToEndEmbeddingDB(t *testing.T) {
	db := taskTestDB(t)
	taskTestStubPluginLedger(t, db, "enabled")
	store := NewStore(db)
	ctx := context.Background()
	taskTestSeedRoute(t, store, CallTypeEmbedding, "bge-db", "ch-emb-db")

	driver := &taskTestDriver{}
	service := NewService(store, driver, Config{RequestTimeout: 5, Currency: DefaultCurrency}, nil)
	probe := &taskStatusProbe{store: store, inner: service}

	// 1000 条：覆盖 acceptance 的批量规模；分片 = 50 片
	const inputs = 1000
	row, created, err := store.SubmitTask(ctx, TaskSubmitInput{
		TaskType: TaskTypeEmbedding, Payload: taskTestEmbeddingPayload(t, inputs), RequestID: "rid-e2e-emb",
		BizType: "admin", Source: "admin:task", ProgressTotal: inputs,
	})
	if err != nil || !created {
		t.Fatalf("提交失败: created=%v err=%v", created, err)
	}
	probe.id = row.ID

	worker := taskTestWorker(t, db, store, probe, 3)
	claimed, err := worker.RunOnce(ctx)
	if err != nil || !claimed {
		t.Fatalf("一轮应领取并执行: claimed=%v err=%v", claimed, err)
	}

	// 状态机：pending → processing（执行中）→ success（落库）
	if probe.observed() != TaskStatusProcessing {
		t.Fatalf("执行期间库内状态应为 processing，实际 %q", probe.observed())
	}
	got, err := store.GetTask(ctx, row.ID)
	if err != nil {
		t.Fatalf("读回失败: %v", err)
	}
	if got.Status != TaskStatusSuccess || got.Attempt != 1 {
		t.Fatalf("终态不符: status=%s attempt=%d error=%s", got.Status, got.Attempt, got.Error)
	}
	if got.ProgressDone != inputs || got.ProgressTotal != inputs {
		t.Fatalf("分片进度应收口到 %d/%d: %d/%d", inputs, inputs, got.ProgressDone, got.ProgressTotal)
	}
	var result taskEmbeddingResultJSON
	if err := json.Unmarshal(got.Result, &result); err != nil {
		t.Fatalf("结果不是合法 JSON: %v", err)
	}
	if result.Count != inputs || len(result.Embeddings) != inputs || result.Dimensions != 3 {
		t.Fatalf("结果收敛不符: count=%d len=%d dims=%d", result.Count, len(result.Embeddings), result.Dimensions)
	}

	// 分片调用与派生 usage id（每片一枚）
	calls := func() int {
		driver.mu.Lock()
		defer driver.mu.Unlock()
		return len(driver.embedCalls)
	}()
	if want := (inputs + maxUpstreamEmbeddingBatch - 1) / maxUpstreamEmbeddingBatch; calls != want {
		t.Fatalf("应分 %d 片调用，实际 %d 次", want, calls)
	}

	// 累计 usage 口径：每条 usage 行都在，且都挂在派生的 request_id 上
	usageIDs := taskUsageRequestIDs(*got)
	if len(usageIDs) != calls {
		t.Fatalf("派生 usage id 数 %d ≠ 分片数 %d", len(usageIDs), calls)
	}
	usages, err := store.TaskUsageRows(ctx, usageIDs)
	if err != nil {
		t.Fatalf("读任务用量失败: %v", err)
	}
	if len(usages) != calls {
		t.Fatalf("每次分片调用应各落一行 usage（共 %d 行），实际 %d 行", calls, len(usages))
	}
	totalTokens := 0
	for _, usage := range usages {
		if usage.Status != UsageStatusSuccess || usage.CallType != CallTypeEmbedding {
			t.Fatalf("分片 usage 行口径不符: %+v", usage)
		}
		totalTokens += usage.TotalTokens
	}
	if totalTokens != inputs {
		t.Fatalf("usage 累计 token 应为 %d，实际 %d", inputs, totalTokens)
	}

	// HTTP 面：详情给累计口径、导出是 ndjson 且行数 = 条目数、wheres 列表可用
	engine := taskTestEngine(taskTestDeps(store))
	_, envelope := taskTestCall(t, engine, http.MethodGet, fmt.Sprintf("/task/get/%d", row.ID), "")
	if envelope.Code != 0 {
		t.Fatalf("详情应 code=0: %s", envelope.Message)
	}
	var detail struct {
		Row     Task            `json:"row"`
		Entries int             `json:"entries"`
		Usage   usageSummary    `json:"usage"`
		IDs     []string        `json:"usage_request_ids"`
		Raw     json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(envelope.Data, &detail); err != nil {
		t.Fatalf("详情数据不是合法 JSON: %v", err)
	}
	if detail.Entries != inputs || len(detail.IDs) != calls {
		t.Fatalf("详情条目数/usage id 数不符: entries=%d ids=%d", detail.Entries, len(detail.IDs))
	}
	if detail.Usage.Calls != int64(calls) || detail.Usage.Success != int64(calls) || detail.Usage.TotalTokens != int64(inputs) {
		t.Fatalf("详情累计口径不符: %+v", detail.Usage)
	}

	// 导出：逐行写、行数 = 条目数、Content-Type 前缀正确
	counting := &taskCountingWriter{header: http.Header{}}
	exportReq := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/task/export/%d", row.ID), nil)
	engine.ServeHTTP(counting, exportReq)
	if got := counting.header.Get("Content-Type"); !strings.HasPrefix(got, "application/x-ndjson") {
		t.Fatalf("导出 Content-Type 应为 application/x-ndjson，实际 %q", got)
	}
	body := strings.TrimRight(counting.body.String(), "\n")
	lines := strings.Split(body, "\n")
	if len(lines) != inputs {
		t.Fatalf("导出行数 %d ≠ 结果条目数 %d", len(lines), inputs)
	}
	if counting.writes < len(lines) {
		t.Fatalf("导出必须逐行写（Write 次数 %d < 行数 %d）——整包一次写会把内存吃满", counting.writes, len(lines))
	}
	var firstLine struct {
		Index     int       `json:"index"`
		Object    string    `json:"object"`
		Embedding []float64 `json:"embedding"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &firstLine); err != nil {
		t.Fatalf("导出首行不是合法 JSON: %v", err)
	}
	if firstLine.Index != 0 || firstLine.Object != "embedding" || len(firstLine.Embedding) != 3 {
		t.Fatalf("导出首行内容不符: %+v", firstLine)
	}

	// wheres 高级检索：白名单内字段可筛、白名单外字段可读拒绝
	_, envelope = taskTestCall(t, engine, http.MethodPost, "/task/list",
		`{"page":1,"limit":10,"wheres":[{"wheres":[{"field":"request_id","operator":"eq","value":"rid-e2e-emb"}],"or":false}]}`)
	if envelope.Code != 0 {
		t.Fatalf("wheres 列表应 code=0: %s", envelope.Message)
	}
	var page struct {
		List  []taskRow `json:"list"`
		Total int64     `json:"total"`
	}
	if err := json.Unmarshal(envelope.Data, &page); err != nil {
		t.Fatalf("列表数据不是合法 JSON: %v", err)
	}
	if page.Total != 1 || len(page.List) != 1 || page.List[0].RequestID != "rid-e2e-emb" {
		t.Fatalf("wheres 过滤结果不符: total=%d list=%+v", page.Total, page.List)
	}
	if page.List[0].Entries != inputs || page.List[0].Summary == "" {
		t.Fatalf("列表摘要不符: %+v", page.List[0])
	}
	_, envelope = taskTestCall(t, engine, http.MethodPost, "/task/list",
		`{"page":1,"limit":10,"wheres":[{"wheres":[{"field":"payload","operator":"eq","value":"x"}]}]}`)
	if envelope.Code != 1 || !strings.Contains(envelope.Message, "白名单") {
		t.Fatalf("白名单外字段必须被拒（可读文案）: code=%d msg=%s", envelope.Code, envelope.Message)
	}
}

// TestTaskWorkerFailurePerAttemptUsageDB 失败路径：上游 500 ⇒ attempt 到 task_max_attempts 置
// failed + error；**每次尝试各落一行 usage**（派生的 request_id 各自成行，不互相覆盖）。
func TestTaskWorkerFailurePerAttemptUsageDB(t *testing.T) {
	db := taskTestDB(t)
	taskTestStubPluginLedger(t, db, "enabled")
	store := NewStore(db)
	ctx := context.Background()
	taskTestSeedRoute(t, store, CallTypeChat, "gpt-db", "ch-chat-db")

	driver := &taskTestDriver{chatErr: fmt.Errorf("上游 500: internal server error")}
	service := NewService(store, driver, Config{RequestTimeout: 5, Currency: DefaultCurrency}, nil)

	row, _, err := store.SubmitTask(ctx, TaskSubmitInput{
		TaskType: TaskTypeChat, Payload: taskTestChatPayload(t), RequestID: "rid-e2e-fail",
		BizType: "admin", Source: "admin:task", ProgressTotal: 1,
	})
	if err != nil {
		t.Fatalf("提交失败: %v", err)
	}
	worker := taskTestWorker(t, db, store, service, 3)
	for round := 0; round < 3; round++ {
		claimed, err := worker.RunOnce(ctx)
		if err != nil || !claimed {
			t.Fatalf("第 %d 轮应领取: claimed=%v err=%v", round+1, claimed, err)
		}
	}
	got, err := store.GetTask(ctx, row.ID)
	if err != nil {
		t.Fatalf("读回失败: %v", err)
	}
	if got.Status != TaskStatusFailed || got.Attempt != 3 {
		t.Fatalf("尝试耗尽应为 failed/attempt=3，实际 %s/%d", got.Status, got.Attempt)
	}
	if !strings.Contains(got.Error, "500") {
		t.Fatalf("失败原因未落库: %q", got.Error)
	}
	if len(got.Result) != 0 {
		t.Fatalf("失败任务不得留结果: %s", string(got.Result))
	}

	// 每次尝试各落一行 usage（3 次尝试 ⇒ 3 行；熔断阈值默认 5，3 次不至于跳闸）
	usageIDs := taskUsageRequestIDs(*got)
	if len(usageIDs) != 3 {
		t.Fatalf("3 次尝试应派生 3 枚 usage id，实际 %v", usageIDs)
	}
	usages, err := store.TaskUsageRows(ctx, usageIDs)
	if err != nil {
		t.Fatalf("读任务用量失败: %v", err)
	}
	if len(usages) != 3 {
		t.Fatalf("每次尝试应各落一行 usage，实际 %d 行", len(usages))
	}
	for _, usage := range usages {
		if usage.Status == UsageStatusSuccess {
			t.Fatalf("失败尝试不得写成 success: %+v", usage)
		}
	}

	// HTTP 面：详情给出累计口径（3 次尝试 3 次失败）；未完成的任务不导出
	engine := taskTestEngine(taskTestDeps(store))
	_, envelope := taskTestCall(t, engine, http.MethodGet, fmt.Sprintf("/task/get/%d", row.ID), "")
	if envelope.Code != 0 {
		t.Fatalf("详情应 code=0: %s", envelope.Message)
	}
	var detail struct {
		Row   Task         `json:"row"`
		Usage usageSummary `json:"usage"`
	}
	if err := json.Unmarshal(envelope.Data, &detail); err != nil {
		t.Fatalf("详情数据不是合法 JSON: %v", err)
	}
	if detail.Row.Status != TaskStatusFailed || detail.Usage.Calls != 3 || detail.Usage.Fail != 3 {
		t.Fatalf("详情累计口径不符: status=%s usage=%+v", detail.Row.Status, detail.Usage)
	}
	_, envelope = taskTestCall(t, engine, http.MethodGet, fmt.Sprintf("/task/export/%d", row.ID), "")
	if envelope.Code != 1 || !strings.Contains(envelope.Message, "尚未完成") {
		t.Fatalf("失败任务不得导出: code=%d msg=%s", envelope.Code, envelope.Message)
	}
}

// TestTaskDisabledWorkerIdlesDB 停用态**空转**：真台账查询下，本轮不领活、不改行；启用后同一轮立刻领到。
func TestTaskDisabledWorkerIdlesDB(t *testing.T) {
	db := taskTestDB(t)
	stub := taskTestStubPluginLedger(t, db, "disabled")
	store := NewStore(db)
	ctx := context.Background()
	taskTestSeedRoute(t, store, CallTypeChat, "gpt-db", "ch-chat-db")

	row, _, err := store.SubmitTask(ctx, TaskSubmitInput{
		TaskType: TaskTypeChat, Payload: taskTestChatPayload(t), RequestID: "rid-disabled",
		BizType: "admin", Source: "admin:task", ProgressTotal: 1,
	})
	if err != nil {
		t.Fatalf("提交失败: %v", err)
	}
	driver := &taskTestDriver{}
	service := NewService(store, driver, Config{RequestTimeout: 5, Currency: DefaultCurrency}, nil)
	worker := taskTestWorker(t, db, store, service, 3)

	for round := 0; round < 2; round++ {
		claimed, err := worker.RunOnce(ctx)
		if err != nil {
			t.Fatalf("停用态不应报错: %v", err)
		}
		if claimed {
			t.Fatal("停用态不得领活（本轮应空转）")
		}
	}
	got, err := store.GetTask(ctx, row.ID)
	if err != nil {
		t.Fatalf("读回失败: %v", err)
	}
	if got.Status != TaskStatusPending || got.Attempt != 0 || got.LockedUntil != nil {
		t.Fatalf("停用态不得改账本行: status=%s attempt=%d locked=%v", got.Status, got.Attempt, got.LockedUntil)
	}

	// 启用后立刻就绪（停用只是「不推进」，不是丢任务）
	stub.setStatus(t, "enabled")
	claimed, err := worker.RunOnce(ctx)
	if err != nil || !claimed {
		t.Fatalf("启用后应领到任务: claimed=%v err=%v", claimed, err)
	}
	got, err = store.GetTask(ctx, row.ID)
	if err != nil {
		t.Fatalf("读回失败: %v", err)
	}
	if got.Status != TaskStatusSuccess || got.Attempt != 1 {
		t.Fatalf("启用后一轮内应完成: status=%s attempt=%d error=%s", got.Status, got.Attempt, got.Error)
	}
}

// TestTaskSubmitHTTPRejectsAndExportsChatDB 提交 HTTP 面的两条边界：非法入参可读拒绝、
// 同 request_id 重复提交回既有行；chat 成功任务的导出为单行 ndjson。
func TestTaskSubmitHTTPRejectsAndExportsChatDB(t *testing.T) {
	db := taskTestDB(t)
	taskTestStubPluginLedger(t, db, "enabled")
	store := NewStore(db)
	ctx := context.Background()
	taskTestSeedRoute(t, store, CallTypeChat, "gpt-db", "ch-chat-db")

	engine := taskTestEngine(taskTestDeps(store))
	body := `{"task_type":"chat","model":"gpt-db","messages":[{"role":"user","content":"hi"}],"biz_type":"admin","source":"admin:task","request_id":"rid-http"}`
	_, envelope := taskTestCall(t, engine, http.MethodPost, "/task/submit", body)
	if envelope.Code != 0 {
		t.Fatalf("提交应 code=0: %s", envelope.Message)
	}
	var submitted struct {
		ID        uint64 `json:"id"`
		RequestID string `json:"request_id"`
		Created   bool   `json:"created"`
		Status    string `json:"status"`
	}
	if err := json.Unmarshal(envelope.Data, &submitted); err != nil {
		t.Fatalf("提交数据不是合法 JSON: %v", err)
	}
	if !submitted.Created || submitted.ID == 0 || submitted.RequestID != "rid-http" || submitted.Status != TaskStatusPending {
		t.Fatalf("提交回执不符: %+v", submitted)
	}

	// 重复提交：同 request_id 回既有行
	_, envelope = taskTestCall(t, engine, http.MethodPost, "/task/submit", body)
	var repeat struct {
		ID      uint64 `json:"id"`
		Created bool   `json:"created"`
	}
	if err := json.Unmarshal(envelope.Data, &repeat); err != nil {
		t.Fatalf("重复提交数据不是合法 JSON: %v", err)
	}
	if envelope.Code != 0 || repeat.Created || repeat.ID != submitted.ID {
		t.Fatalf("重复提交应回既有任务: code=%d created=%v id=%d（首次 %d）", envelope.Code, repeat.Created, repeat.ID, submitted.ID)
	}

	// 非法入参：闭集外类型 / 缺 messages / 空 inputs（一律可读拒绝，不建行）
	for _, bad := range []string{
		`{"task_type":"image","model":"gpt-db","biz_type":"admin","source":"admin:task"}`,
		`{"task_type":"chat","model":"gpt-db","biz_type":"admin","source":"admin:task"}`,
		`{"task_type":"embedding","model":"bge-db","inputs":[],"biz_type":"admin","source":"admin:task"}`,
	} {
		_, rejected := taskTestCall(t, engine, http.MethodPost, "/task/submit", bad)
		if rejected.Code == 0 {
			t.Fatalf("非法提交必须被拒: %s", bad)
		}
	}

	// 走 worker 完成，然后导出：chat 单行 ndjson
	driver := &taskTestDriver{}
	service := NewService(store, driver, Config{RequestTimeout: 5, Currency: DefaultCurrency}, nil)
	worker := taskTestWorker(t, db, store, service, 3)
	if claimed, err := worker.RunOnce(ctx); err != nil || !claimed {
		t.Fatalf("一轮应领取: claimed=%v err=%v", claimed, err)
	}
	counting := &taskCountingWriter{header: http.Header{}}
	engine.ServeHTTP(counting, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/task/export/%d", submitted.ID), nil))
	if got := counting.header.Get("Content-Type"); !strings.HasPrefix(got, "application/x-ndjson") {
		t.Fatalf("导出 Content-Type 应为 application/x-ndjson，实际 %q", got)
	}
	lines := strings.Split(strings.TrimRight(counting.body.String(), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("chat 任务导出应为 1 行，实际 %d 行", len(lines))
	}
	var exportLine struct {
		Index   int    `json:"index"`
		Object  string `json:"object"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &exportLine); err != nil {
		t.Fatalf("导出首行不是合法 JSON: %v", err)
	}
	if exportLine.Index != 0 || exportLine.Object != "chat.completion" || exportLine.Content == "" {
		t.Fatalf("导出内容不符: %+v", exportLine)
	}
}
