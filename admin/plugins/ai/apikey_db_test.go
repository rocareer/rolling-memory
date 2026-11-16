// plugin: ai v0.2.0
// apikey_db_test.go 是应用接入（API Key）的**库级**用例（DSN 门控）：只连真库才能暴露的语义——
// 唯一键（uk(key_hash) 与 uk(source) WHERE source<>”）、jsonb 白名单往返、status/expire_at 的
// 鉴权判定、last_used_at 的最终一致刷新、并发同 Key 调用、以及「库里只存 sha256」。
//
// 用法（**必须指向临时库**：用例会重建本插件的全部 plugin_ai_* 表）：
//
//	PLUGIN_AI_TEST_DSN='postgres://用户:口令@主机:端口/临时库?sslmode=disable' \
//	  go test -count=1 -run TestAPIKey -v ./plugins/ai/
//
// 未设 DSN ⇒ 整组跳过（默认 `go test ./plugins/ai/` 不连库、不需要 Postgres）。
// 表结构取自插件自己的迁移文件（migrations/*.up.sql 按编号升序应用，__PREFIX__ 按进程前缀替换），
// 与安装器落库同物；迁移集是「目录里全部 up 文件」而不是写死某几个编号，新增迁移自动纳入。
package ai

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// ===== 库级测试脚手架 =====

// dropAPIPluginTables 清掉本插件的全部物理表（按 <前缀>plugin_ai_% 模式扫描，不猜任何别的表）。
//
// 用模式扫描而不是写死清单：迁移集增长时清场不会漏表（漏一张就会让下一次建表撞已存在的表）。
func dropAPIPluginTables(db *gorm.DB) error {
	prefix := config.Get().Database.Prefix
	pattern := prefix + "plugin_ai_%"
	rows, err := db.Raw(
		`SELECT tablename FROM pg_tables WHERE schemaname = current_schema() AND tablename LIKE ?`,
		pattern).Rows()
	if err != nil {
		return err
	}
	names := make([]string, 0, 8)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			_ = rows.Close()
			return err
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	_ = rows.Close()
	for _, name := range names {
		if err := db.Exec(`DROP TABLE IF EXISTS ` + quoteIdent(name)).Error; err != nil {
			return err
		}
	}
	return nil
}

// applyAPIPluginMigrations 按编号升序应用 migrations 目录里的全部 up 文件（__PREFIX__ 按进程前缀替换）。
func applyAPIPluginMigrations(db *gorm.DB) error {
	entries, err := os.ReadDir("migrations")
	if err != nil {
		return err
	}
	type migration struct {
		version int
		name    string
	}
	items := make([]migration, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".up.sql") {
			continue
		}
		raw, _, ok := strings.Cut(name, "_")
		if !ok {
			continue
		}
		version, err := strconv.Atoi(raw)
		if err != nil {
			continue
		}
		items = append(items, migration{version: version, name: name})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].version != items[j].version {
			return items[i].version < items[j].version
		}
		return items[i].name < items[j].name
	})
	prefix := config.Get().Database.Prefix
	for _, item := range items {
		content, err := os.ReadFile(filepath.Join("migrations", item.name))
		if err != nil {
			return err
		}
		ddl := strings.ReplaceAll(string(content), "__PREFIX__", prefix)
		if err := db.Exec(ddl).Error; err != nil {
			return err
		}
	}
	return nil
}

// apiKeyTestDB 连临时库、清场、按迁移建表，收尾再清场（护栏与 store_db_test.go 同款）。
func apiKeyTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv(pluginAITestDSNEnv))
	if dsn == "" {
		t.Skip("需 " + pluginAITestDSNEnv + "（指向**临时库**；勿指向开发/生产库）")
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
		t.Fatalf("%s 指向的库存在 admin_plugins（疑似开发/生产库）：本用例会重建 plugin_ai_* 表，拒绝执行",
			pluginAITestDSNEnv)
	}

	if err := dropAPIPluginTables(db); err != nil {
		t.Fatalf("清场失败: %v", err)
	}
	if err := applyAPIPluginMigrations(db); err != nil {
		t.Fatalf("按迁移建表失败: %v", err)
	}
	t.Cleanup(func() {
		if err := dropAPIPluginTables(db); err != nil {
			t.Errorf("收尾清场失败: %v", err)
		}
	})
	return db
}

// apiKeyAuthEngineDB 造「真 Store + 真鉴权中间件」的引擎（库级鉴权五态与并发用例用）。
func apiKeyAuthEngineDB(db *gorm.DB, globalKey string, probe *authProbe) *gin.Engine {
	set := withAuthSetting(globalKey)
	loadSettings := func(context.Context) (pluginSettings, error) { return set, nil }
	loadStore := func(context.Context) (apiKeyAuthStore, error) { return NewStore(db), nil }
	return apiKeyAuthTestEngine(loadSettings, loadStore, http.MethodPost, "/ai/v1/chat/completions", probe)
}

// apiKeyAdminEngineDB 造后台 7 条路由的引擎（真 Store；权限链由宿主接线承担，这里只验业务处理器）。
func apiKeyAdminEngineDB(db *gorm.DB) *gin.Engine {
	d := deps{
		loadSettings: func(context.Context) (pluginSettings, error) { return withAuthSetting(""), nil },
		loadStore:    func(context.Context) (*Store, error) { return NewStore(db), nil },
	}
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.POST("/apikey/list", d.apiKeyList)
	engine.GET("/apikey/get/:pk", d.apiKeyGet)
	engine.POST("/apikey/create", d.apiKeyCreate)
	engine.POST("/apikey/update/:pk", d.apiKeyUpdate)
	engine.POST("/apikey/delete", d.apiKeyDelete)
	engine.POST("/apikey/reset/:pk", d.apiKeyReset)
	engine.POST("/apikey/switch", d.apiKeySwitch)
	return engine
}

// issuedPayload 解析 create / reset 的回执数据（明文只在这里取一次）。
type issuedPayload struct {
	ID        uint64 `json:"id"`
	APIKey    string `json:"api_key"`
	KeyPrefix string `json:"key_prefix"`
	Source    string `json:"source"`
}

// dbRowLastUsedAt 读一行的 last_used_at（轮询用；nil = 从未刷新）。
func dbRowLastUsedAt(t *testing.T, db *gorm.DB, id uint64) *int64 {
	t.Helper()
	var row APIKey
	if err := db.Where(`"id" = ?`, id).First(&row).Error; err != nil {
		t.Fatalf("读取行失败: %v", err)
	}
	return row.LastUsedAt
}

// waitForLastUsedAt 轮询等 last_used_at 落地（最终一致；≥5s 未出现即失败）。
func waitForLastUsedAt(t *testing.T, db *gorm.DB, id uint64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if dbRowLastUsedAt(t, db, id) != nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("last_used_at 在 5s 内未刷新（行 %d）", id)
}

// ===== 行模型与唯一键 =====

func TestAPIKeyStoreRoundTripAndUniqueKeysDB(t *testing.T) {
	db := apiKeyTestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	future := nowUnix() + 3600
	row := &APIKey{
		Name: "应用 A", KeyHash: HashAPIKey("ak-db-roundtrip-0001"), KeyPrefix: "ak_db_roundt…",
		Source: "app-a", ModelScope: []string{"gpt-4", "gpt-4o"}, RateLimit: 2,
		Status: StatusEnabled, ExpireAt: &future, Remark: "回归用",
	}
	if err := store.CreateAPIKey(ctx, row); err != nil {
		t.Fatalf("建行失败: %v", err)
	}
	if row.ID == 0 || row.CreateTime == 0 {
		t.Fatalf("主键/时间戳未回填: %+v", row)
	}

	got, err := store.GetAPIKey(ctx, int64(row.ID))
	if err != nil {
		t.Fatalf("取行失败: %v", err)
	}
	// jsonb 白名单往返（serializer:json ↔ jsonb）。
	if len(got.ModelScope) != 2 || got.ModelScope[0] != "gpt-4" || got.ModelScope[1] != "gpt-4o" {
		t.Fatalf("model_scope 往返不符: %v", got.ModelScope)
	}
	if got.Source != "app-a" || got.RateLimit != 2 || got.Status != StatusEnabled {
		t.Fatalf("列往返不符: %+v", got)
	}
	if got.ExpireAt == nil || *got.ExpireAt != future {
		t.Fatalf("expire_at 往返不符: %+v", got.ExpireAt)
	}
	if got.LastUsedAt != nil {
		t.Fatalf("新行的 last_used_at 应为 NULL: %+v", got.LastUsedAt)
	}

	// 唯一的 key_hash：换 source 但同 hash ⇒ 唯一键冲突。
	dupHash := &APIKey{Name: "应用 B", KeyHash: row.KeyHash, Source: "app-b"}
	if err := store.CreateAPIKey(ctx, dupHash); !isUniqueViolation(err) {
		t.Fatalf("同 key_hash 必须唯一键冲突，实际 err=%v", err)
	}
	// 唯一的 source（非空时）：换 hash 但同 source ⇒ 唯一键冲突。
	dupSource := &APIKey{Name: "应用 C", KeyHash: HashAPIKey("ak-db-roundtrip-0002"), Source: "app-a"}
	if err := store.CreateAPIKey(ctx, dupSource); !isUniqueViolation(err) {
		t.Fatalf("同 source 必须唯一键冲突，实际 err=%v", err)
	}
	// 空 source 允许重复（部分唯一索引 WHERE source <> ''）。
	for i, name := range []string{"空 source 1", "空 source 2"} {
		blank := &APIKey{Name: name, KeyHash: HashAPIKey("ak-db-blank-" + name), Source: ""}
		if err := store.CreateAPIKey(ctx, blank); err != nil {
			t.Fatalf("空 source 的第 %d 行必须允许: %v", i+1, err)
		}
	}

	// 列表：快搜 + 顶层 status/source 筛选 + 默认 id DESC。
	list, err := store.ListAPIKeys(ctx, APIKeyFilter{Page: 1, Limit: 10, Source: "app-a"})
	if err != nil {
		t.Fatalf("列表失败: %v", err)
	}
	if list.Total != 1 || len(list.Rows) != 1 || list.Rows[0].ID != row.ID {
		t.Fatalf("按 source 筛选不符: total=%d rows=%d", list.Total, len(list.Rows))
	}
	list, err = store.ListAPIKeys(ctx, APIKeyFilter{Page: 1, Limit: 10, Keyword: "应用"})
	if err != nil {
		t.Fatalf("列表失败: %v", err)
	}
	// 冲突的两行没落库 ⇒ 名字含「应用」的只有第一行；空 source 的两行用它们自己的名字快搜命中。
	if list.Total != 1 {
		t.Fatalf("快搜「应用」命中 = %d，期望 1（唯一键冲突的行不得落库）", list.Total)
	}
	list, err = store.ListAPIKeys(ctx, APIKeyFilter{Page: 1, Limit: 10, Keyword: "空 source"})
	if err != nil {
		t.Fatalf("列表失败: %v", err)
	}
	if list.Total != 2 {
		t.Fatalf("空 source 行数 = %d，期望 2", list.Total)
	}

	// 更新：改 source/rate_limit、清空白名单、清空过期时间。
	source := "app-a2"
	rateLimit := 0
	empty := []string{}
	zero := int64(0)
	if affected, err := store.UpdateAPIKey(ctx, int64(row.ID), APIKeyPatch{
		Source: &source, RateLimit: &rateLimit, ModelScope: &empty, ExpireAt: &zero,
	}); err != nil || affected == 0 {
		t.Fatalf("更新失败: affected=%d err=%v", affected, err)
	}
	got, err = store.GetAPIKey(ctx, int64(row.ID))
	if err != nil {
		t.Fatalf("取行失败: %v", err)
	}
	if got.Source != "app-a2" || got.RateLimit != 0 || len(got.ModelScope) != 0 {
		t.Fatalf("更新未生效: %+v", got)
	}
	if got.ExpireAt != nil {
		t.Fatalf("expire_at=0 必须写 NULL（永不过期），实际 %+v", got.ExpireAt)
	}

	// 启停：只改 status。
	if affected, err := store.SwitchAPIKey(ctx, int64(row.ID), StatusDisabled); err != nil || affected == 0 {
		t.Fatalf("启停失败: affected=%d err=%v", affected, err)
	}
	got, _ = store.GetAPIKey(ctx, int64(row.ID))
	if got.Status != StatusDisabled {
		t.Fatalf("停用未生效: %+v", got.Status)
	}

	// 重置：换 hash/prefix ⇒ 旧 hash 查不到、新 hash 查得到。
	newPlain := "ak-db-rotated-000001"
	if affected, err := store.RotateAPIKey(ctx, int64(row.ID), HashAPIKey(newPlain), APIKeyDisplayPrefix(newPlain), nowUnix()); err != nil || affected == 0 {
		t.Fatalf("重置失败: affected=%d err=%v", affected, err)
	}
	if _, err := store.LookupAPIKeyByHash(ctx, HashAPIKey("ak-db-roundtrip-0001")); err == nil {
		t.Fatal("旧 key_hash 必须查不到（重置后旧明文立即失效）")
	}
	found, err := store.LookupAPIKeyByHash(ctx, HashAPIKey(newPlain))
	if err != nil || found.ID != row.ID {
		t.Fatalf("新 key_hash 必须命中同一行: %+v err=%v", found, err)
	}

	// 删除：批量删除后查不到。
	if err := store.DeleteAPIKeys(ctx, []int64{int64(row.ID)}); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if _, err := store.GetAPIKey(ctx, int64(row.ID)); err == nil {
		t.Fatal("删除后不得再读到")
	}
}

func TestAPIKeyLookupMissingReturnsNotFoundDB(t *testing.T) {
	db := apiKeyTestDB(t)
	store := NewStore(db)
	_, err := store.LookupAPIKeyByHash(context.Background(), HashAPIKey("ak-not-there"))
	if err != gorm.ErrRecordNotFound {
		t.Fatalf("未命中必须返回 gorm.ErrRecordNotFound（鉴权据此回落全局 key），实际 %v", err)
	}
}

// ===== 鉴权五态（真库）=====

func TestAPIKeyAuthStatesAgainstRealStoreDB(t *testing.T) {
	db := apiKeyTestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	const plain = "ak-db-auth-states-01"
	past, future := int64(1), nowUnix()+3600
	valid := &APIKey{Name: "有效", KeyHash: HashAPIKey(plain), KeyPrefix: APIKeyDisplayPrefix(plain),
		Source: "app-valid", Status: StatusEnabled, ExpireAt: &future}
	// 停用行**不能**靠 Create 造（status 零值会被 GORM 省略、落库变成 DB 默认 1），必须走 SwitchAPIKey。
	disabled := &APIKey{Name: "停用", KeyHash: HashAPIKey("ak-db-disabled-0001"), Source: "app-disabled",
		Status: StatusEnabled}
	expired := &APIKey{Name: "过期", KeyHash: HashAPIKey("ak-db-expired-00001"), Source: "app-expired",
		Status: StatusEnabled, ExpireAt: &past}
	for _, row := range []*APIKey{valid, disabled, expired} {
		if err := store.CreateAPIKey(ctx, row); err != nil {
			t.Fatalf("建行 %s 失败: %v", row.Name, err)
		}
	}
	if affected, err := store.SwitchAPIKey(ctx, int64(disabled.ID), StatusDisabled); err != nil || affected == 0 {
		t.Fatalf("停用行失败: affected=%d err=%v", affected, err)
	}

	probe := &authProbe{}
	// 全局 key 已配置：这样「未知凭证」的口径是 401（而不是 503 api_key_not_configured）。
	engine := apiKeyAuthEngineDB(db, "sk-global-1234567890", probe)
	body := `{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`

	// ① 有效 ⇒ 放行并把 source 写成 key:<source>（不再恒为 'key:gateway'）。
	recorder := doRequest(engine, http.MethodPost, "/ai/v1/chat/completions", body,
		map[string]string{"Authorization": "Bearer " + plain})
	if recorder.Code != http.StatusOK || !probe.reached {
		t.Fatalf("有效 Key 必须放行: %d（body=%s）", recorder.Code, recorder.Body.String())
	}
	if probe.source != "key:app-valid" {
		t.Fatalf("source 接缝 = %q，期望 key:app-valid", probe.source)
	}
	// ② 停用 ⇒ 401；③ 过期 ⇒ 401；④ 未知 ⇒ 401。
	for _, tc := range []struct{ name, token string }{
		{"停用", "ak-db-disabled-0001"},
		{"过期", "ak-db-expired-00001"},
		{"未知", "ak-db-never-existed"},
	} {
		t.Run(tc.name+" ⇒ 401", func(t *testing.T) {
			recorder := doRequest(engine, http.MethodPost, "/ai/v1/chat/completions", body,
				map[string]string{"Authorization": "Bearer " + tc.token})
			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("状态码 = %d，期望 401（body=%s）", recorder.Code, recorder.Body.String())
			}
			if envelope := decodePublicError(t, recorder); envelope.Error.Code != "invalid_api_key" {
				t.Fatalf("错误码 = %q", envelope.Error.Code)
			}
		})
	}
	// ⑤ 库里无 key 且全局配置为空 ⇒ 503（现行为不变）。
	emptyDB := apiKeyTestDB(t)
	emptyProbe := &authProbe{}
	emptyEngine := apiKeyAuthEngineDB(emptyDB, "", emptyProbe)
	recorder = doRequest(emptyEngine, http.MethodPost, "/ai/v1/chat/completions", body,
		map[string]string{"Authorization": "Bearer ak-db-anything"})
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("空表 + 无全局 key 必须 503，实际 %d", recorder.Code)
	}
	if envelope := decodePublicError(t, recorder); envelope.Error.Code != "api_key_not_configured" {
		t.Fatalf("错误码 = %q，期望 api_key_not_configured", envelope.Error.Code)
	}
}

func TestAPIKeyAuthFailsClosedWhenTableMissingDB(t *testing.T) {
	db := apiKeyTestDB(t)
	store := NewStore(db)
	ctx := context.Background()
	if err := store.CreateAPIKey(ctx, &APIKey{Name: "有效", KeyHash: HashAPIKey("ak-db-failclosed-1"), Source: "app-a", Status: StatusEnabled}); err != nil {
		t.Fatalf("建行失败: %v", err)
	}
	// 表消失（迁移未跑 / 库不可用）：查库出错必须 fail-closed，**不得**回落全局 key 比对。
	if err := db.Exec(`DROP TABLE IF EXISTS ` + quoteIdent(table("plugin_ai_api_key"))).Error; err != nil {
		t.Fatalf("删表失败: %v", err)
	}
	probe := &authProbe{}
	engine := apiKeyAuthEngineDB(db, "sk-global-1234567890", probe)
	recorder := doRequest(engine, http.MethodPost, "/ai/v1/chat/completions",
		`{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`,
		map[string]string{"Authorization": "Bearer sk-global-1234567890"})
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("状态码 = %d，期望 503（查库出错 + 出示全局 key 时绝不静默放行）", recorder.Code)
	}
	if probe.reached {
		t.Fatal("查库出错时不得放行（fail-closed 被破坏）")
	}
	if envelope := decodePublicError(t, recorder); envelope.Error.Code != "store_unavailable" {
		t.Fatalf("错误码 = %q，期望 store_unavailable", envelope.Error.Code)
	}
}

func TestAPIKeyLastUsedAtEventuallyConsistentDB(t *testing.T) {
	db := apiKeyTestDB(t)
	store := NewStore(db)
	ctx := context.Background()
	row := &APIKey{Name: "刷新", KeyHash: HashAPIKey("ak-db-last-used-01"), Source: "app-used", Status: StatusEnabled}
	if err := store.CreateAPIKey(ctx, row); err != nil {
		t.Fatalf("建行失败: %v", err)
	}
	if row.LastUsedAt != nil {
		t.Fatalf("初始应为 NULL: %+v", row.LastUsedAt)
	}
	probe := &authProbe{}
	engine := apiKeyAuthEngineDB(db, "", probe)
	recorder := doRequest(engine, http.MethodPost, "/ai/v1/chat/completions",
		`{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`,
		map[string]string{"Authorization": "Bearer ak-db-last-used-01"})
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d", recorder.Code)
	}
	waitForLastUsedAt(t, db, row.ID)
}

func TestAPIKeyConcurrentSameKeyCallsDB(t *testing.T) {
	db := apiKeyTestDB(t)
	store := NewStore(db)
	ctx := context.Background()
	const plain = "ak-db-concurrent-001"
	row := &APIKey{Name: "并发", KeyHash: HashAPIKey(plain), Source: "app-conc", Status: StatusEnabled}
	if err := store.CreateAPIKey(ctx, row); err != nil {
		t.Fatalf("建行失败: %v", err)
	}

	const goroutines = 16
	probe := &authProbe{}
	engine := apiKeyAuthEngineDB(db, "", probe)
	var wait sync.WaitGroup
	statuses := make([]int, goroutines)
	wait.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(index int) {
			defer wait.Done()
			recorder := doRequest(engine, http.MethodPost, "/ai/v1/chat/completions",
				`{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`,
				map[string]string{"Authorization": "Bearer " + plain})
			statuses[index] = recorder.Code
		}(i)
	}
	wait.Wait()
	for index, status := range statuses {
		if status != http.StatusOK {
			t.Fatalf("第 %d 个并发请求状态码 = %d，期望 200", index, status)
		}
	}
	// 仍然只有一行（并发不产生重复行），且 last_used_at 最终落地。
	list, err := store.ListAPIKeys(ctx, APIKeyFilter{Page: 1, Limit: 10, Source: "app-conc"})
	if err != nil {
		t.Fatalf("列表失败: %v", err)
	}
	if list.Total != 1 {
		t.Fatalf("同 Key 并发调用后行数 = %d，期望 1", list.Total)
	}
	waitForLastUsedAt(t, db, row.ID)
}

// ===== 后台 7 条路由（真库往返 + 出口纪律）=====

func TestAPIKeyHandlersIssueListResetAndSwitchDB(t *testing.T) {
	db := apiKeyTestDB(t)
	engine := apiKeyAdminEngineDB(db)

	// ① create：回执含明文一次，库里只落 sha256。
	createBody := `{"name":"应用 A","source":"app-a","model_scope":["gpt-4"],"rate_limit":2,"remark":"回归"}`
	recorder := doRequest(engine, http.MethodPost, "/apikey/create", createBody, nil)
	envelope := decodeAdmin(t, recorder)
	if envelope.Code != 0 {
		t.Fatalf("create 失败: %s", envelope.Message)
	}
	var issued issuedPayload
	if err := json.Unmarshal(envelope.Data, &issued); err != nil {
		t.Fatalf("回执解析失败: %v（%s）", err, envelope.Data)
	}
	if issued.APIKey == "" || !strings.HasPrefix(issued.APIKey, APIKeySecretPrefix) {
		t.Fatalf("回执必须含明文一次: %+v", issued)
	}
	if issued.KeyPrefix != APIKeyDisplayPrefix(issued.APIKey) {
		t.Fatalf("回执前缀不符: %q vs %q", issued.KeyPrefix, APIKeyDisplayPrefix(issued.APIKey))
	}
	var stored APIKey
	if err := db.Where(`"id" = ?`, issued.ID).First(&stored).Error; err != nil {
		t.Fatalf("读取落库行失败: %v", err)
	}
	if stored.KeyHash != HashAPIKey(issued.APIKey) {
		t.Fatalf("库里存的不是明文 sha256: %q", stored.KeyHash)
	}
	if stored.KeyHash == issued.APIKey || strings.Contains(stored.KeyPrefix, issued.APIKey) {
		t.Fatal("库里不得出现明文或明文的完整片段")
	}
	if len(stored.ModelScope) != 1 || stored.ModelScope[0] != "gpt-4" || stored.RateLimit != 2 {
		t.Fatalf("create 未落白名单/限流: %+v", stored)
	}

	// ② create 的 source 唯一冲突 ⇒ code=1 + 可读文案。
	recorder = doRequest(engine, http.MethodPost, "/apikey/create",
		`{"name":"应用 B","source":"app-a"}`, nil)
	envelope = decodeAdmin(t, recorder)
	if envelope.Code != 1 || !strings.Contains(envelope.Message, "已被其他应用绑定") {
		t.Fatalf("source 冲突文案不符: code=%d message=%s", envelope.Code, envelope.Message)
	}
	// source > 24 字符 ⇒ 拒（保证 usage.source ≤30 不被截断）。
	recorder = doRequest(engine, http.MethodPost, "/apikey/create",
		`{"name":"应用 C","source":"`+strings.Repeat("s", APIKeyMaxSource+1)+`"}`, nil)
	if envelope = decodeAdmin(t, recorder); envelope.Code != 1 {
		t.Fatalf("source 超长必须被拒: %s", recorder.Body.String())
	}
	// 新建带 status=0 ⇒ 显式拒（不是静默建成启用行）。
	recorder = doRequest(engine, http.MethodPost, "/apikey/create", `{"name":"应用 D","source":"app-d","status":0}`, nil)
	if envelope = decodeAdmin(t, recorder); envelope.Code != 1 || !strings.Contains(envelope.Message, "恒为启用") {
		t.Fatalf("新建 status=0 必须被显式拒: code=%d message=%s", envelope.Code, envelope.Message)
	}

	// ③ list / get：只回 key_prefix，绝不含 key_hash / 明文。
	recorder = doRequest(engine, http.MethodPost, "/apikey/list", `{"page":1,"limit":10}`, nil)
	envelope = decodeAdmin(t, recorder)
	if envelope.Code != 0 {
		t.Fatalf("list 失败: %s", envelope.Message)
	}
	listText := string(envelope.Data)
	if strings.Contains(listText, "key_hash") || strings.Contains(listText, issued.APIKey) {
		t.Fatalf("列表出口泄露了 key_hash / 明文: %s", listText)
	}
	if !strings.Contains(listText, `"key_prefix":"`+issued.KeyPrefix+`"`) {
		t.Fatalf("列表出口应含展示前缀: %s", listText)
	}
	recorder = doRequest(engine, http.MethodGet, "/apikey/get/"+strconv.FormatUint(issued.ID, 10), "", nil)
	envelope = decodeAdmin(t, recorder)
	if envelope.Code != 0 || strings.Contains(string(envelope.Data), "key_hash") || strings.Contains(string(envelope.Data), issued.APIKey) {
		t.Fatalf("详情出口泄露了 key_hash / 明文: %s", recorder.Body.String())
	}

	// ④ 高级检索：wheres 分组（白名单内）生效；白名单外字段给可读拒绝而不是 500。
	recorder = doRequest(engine, http.MethodPost, "/apikey/list",
		`{"wheres":[{"wheres":[{"field":"source","operator":"eq","value":"app-a"}]}]}`, nil)
	if envelope = decodeAdmin(t, recorder); envelope.Code != 0 || !strings.Contains(string(envelope.Data), `"total":1`) {
		t.Fatalf("wheres 未生效: %s", recorder.Body.String())
	}
	recorder = doRequest(engine, http.MethodPost, "/apikey/list",
		`{"wheres":[{"wheres":[{"field":"key_hash","operator":"eq","value":"x"}]}]}`, nil)
	if envelope = decodeAdmin(t, recorder); envelope.Code != 1 {
		t.Fatalf("白名单外字段必须给 code=1 的可读拒绝: %s", recorder.Body.String())
	}

	// ⑤ update：改限流与白名单；key_hash/key_prefix 不动（换 key 只走 reset）。
	recorder = doRequest(engine, http.MethodPost, "/apikey/update/"+strconv.FormatUint(issued.ID, 10),
		`{"name":"应用 A2","rate_limit":100,"model_scope":["gpt-4","gpt-4o"]}`, nil)
	if envelope = decodeAdmin(t, recorder); envelope.Code != 0 {
		t.Fatalf("update 失败: %s", envelope.Message)
	}
	var updated APIKey
	if err := db.Where(`"id" = ?`, issued.ID).First(&updated).Error; err != nil {
		t.Fatalf("读取行失败: %v", err)
	}
	if updated.Name != "应用 A2" || updated.RateLimit != 100 || len(updated.ModelScope) != 2 {
		t.Fatalf("update 未生效: %+v", updated)
	}
	if updated.KeyHash != stored.KeyHash || updated.KeyPrefix != stored.KeyPrefix {
		t.Fatal("update 不得改 key_hash / key_prefix")
	}

	// ⑥ reset：明文只出现一次，旧明文立即失效，新明文可用（真库鉴权链）。
	recorder = doRequest(engine, http.MethodPost, "/apikey/reset/"+strconv.FormatUint(issued.ID, 10), "", nil)
	envelope = decodeAdmin(t, recorder)
	if envelope.Code != 0 {
		t.Fatalf("reset 失败: %s", envelope.Message)
	}
	var rotated issuedPayload
	if err := json.Unmarshal(envelope.Data, &rotated); err != nil {
		t.Fatalf("reset 回执解析失败: %v", err)
	}
	if rotated.APIKey == "" || rotated.APIKey == issued.APIKey {
		t.Fatalf("reset 必须换新明文: %+v", rotated)
	}
	probe := &authProbe{}
	// 全局 key 已配置：轮换/停用/删除后的旧凭证必须是 401（而不是 503）。
	authEngine := apiKeyAuthEngineDB(db, "sk-global-1234567890", probe)
	body := `{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`
	if recorder := doRequest(authEngine, http.MethodPost, "/ai/v1/chat/completions", body,
		map[string]string{"Authorization": "Bearer " + issued.APIKey}); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("reset 后旧明文必须 401，实际 %d", recorder.Code)
	}
	if recorder := doRequest(authEngine, http.MethodPost, "/ai/v1/chat/completions", body,
		map[string]string{"Authorization": "Bearer " + rotated.APIKey}); recorder.Code != http.StatusOK {
		t.Fatalf("reset 后新明文必须放行，实际 %d（body=%s）", recorder.Code, recorder.Body.String())
	}

	// ⑦ switch：停用后鉴权 401；启用后恢复。
	recorder = doRequest(engine, http.MethodPost, "/apikey/switch",
		`{"id":`+strconv.FormatUint(issued.ID, 10)+`,"status":0}`, nil)
	if envelope = decodeAdmin(t, recorder); envelope.Code != 0 {
		t.Fatalf("switch 失败: %s", envelope.Message)
	}
	if recorder := doRequest(authEngine, http.MethodPost, "/ai/v1/chat/completions", body,
		map[string]string{"Authorization": "Bearer " + rotated.APIKey}); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("停用后必须 401，实际 %d", recorder.Code)
	}
	recorder = doRequest(engine, http.MethodPost, "/apikey/switch",
		`{"id":`+strconv.FormatUint(issued.ID, 10)+`,"status":1}`, nil)
	if envelope = decodeAdmin(t, recorder); envelope.Code != 0 {
		t.Fatalf("switch 失败: %s", envelope.Message)
	}
	if recorder := doRequest(authEngine, http.MethodPost, "/ai/v1/chat/completions", body,
		map[string]string{"Authorization": "Bearer " + rotated.APIKey}); recorder.Code != http.StatusOK {
		t.Fatalf("启用后必须放行，实际 %d", recorder.Code)
	}

	// ⑧ delete：删除后鉴权 401 且行消失。
	recorder = doRequest(engine, http.MethodPost, "/apikey/delete",
		`{"pks":["`+strconv.FormatUint(issued.ID, 10)+`"]}`, nil)
	if envelope = decodeAdmin(t, recorder); envelope.Code != 0 {
		t.Fatalf("delete 失败: %s", envelope.Message)
	}
	if recorder := doRequest(authEngine, http.MethodPost, "/ai/v1/chat/completions", body,
		map[string]string{"Authorization": "Bearer " + rotated.APIKey}); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("删除后必须 401，实际 %d", recorder.Code)
	}
	var count int64
	if err := db.Model(&APIKey{}).Where(`"id" = ?`, issued.ID).Count(&count).Error; err != nil {
		t.Fatalf("计数失败: %v", err)
	}
	if count != 0 {
		t.Fatalf("删除后仍有 %d 行", count)
	}
}

// apiKeyPublicEngineDB 造「真 Store 的鉴权 + 真 Store 的公开面 handler」的链（库级用例）。
//
// 鉴权走生产装配 `d.apiKeyAuth()`（查库面 = deps.loadStore 的真 *Store），业务处理器用同一 *Store
// ——这正是接线后的形状（限流中间件是其后追加的独立层，不改变本文件要断言的返回值）。
func apiKeyPublicEngineDB(db *gorm.DB, globalKey string) *gin.Engine {
	store := NewStore(db)
	base := deps{
		loadSettings: func(context.Context) (pluginSettings, error) { return withAuthSetting(globalKey), nil },
		loadStore:    func(context.Context) (*Store, error) { return store, nil },
		loadService: func(context.Context, pluginSettings) (*Service, error) {
			return nil, errors.New("库级用例不走到编排层")
		},
	}
	auth := base.apiKeyAuth()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	group := engine.Group("")
	group.POST("/ai/v1/chat/completions", auth, base.publicChat)
	group.POST("/ai/v1/embeddings", auth, base.publicEmbeddings)
	group.GET("/ai/v1/models", auth, base.publicModels)
	return engine
}

// modelCatalogRow 造一条逻辑模型（库级用例的模型清单）。
func modelCatalogRow(name string) *Model {
	return &Model{Name: name, Type: ModelTypeChat, Strategy: StrategyPriority, Status: StatusEnabled,
		PriceInput: "0.000000", PriceOutput: "0.000000"}
}

func TestAPIKeyScopeGateAndModelsFilterDB(t *testing.T) {
	db := apiKeyTestDB(t)
	store := NewStore(db)
	ctx := context.Background()
	const plain = "ak-db-scope-gate-01"
	if err := store.CreateAPIKey(ctx, &APIKey{Name: "白名单", KeyHash: HashAPIKey(plain), Source: "app-scope",
		Status: StatusEnabled, ModelScope: []string{"gpt-4"}}); err != nil {
		t.Fatalf("建行失败: %v", err)
	}
	for _, name := range []string{"gpt-4", "gpt-5"} {
		if err := store.CreateModel(ctx, modelCatalogRow(name)); err != nil {
			t.Fatalf("建模型 %s 失败: %v", name, err)
		}
	}
	engine := apiKeyPublicEngineDB(db, "")
	auth := map[string]string{"Authorization": "Bearer " + plain}
	chatBody := `{"model":"gpt-5","messages":[{"role":"user","content":"hi"}]}`

	// ① 调用准入：白名单外模型 ⇒ OpenAI 形状 404，且**到不了编排层**
	//（本引擎的 loadService 恒报错；若准入没拦住，回执会是 503 而不是 404）。
	recorder := doRequest(engine, http.MethodPost, "/ai/v1/chat/completions", chatBody, auth)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("白名单外模型必须 404，实际 %d（body=%s）", recorder.Code, recorder.Body.String())
	}
	if envelope := decodePublicError(t, recorder); envelope.Error.Code != "model_not_found" {
		t.Fatalf("错误码 = %q，期望 model_not_found", envelope.Error.Code)
	}
	// 同一路由改用全局 key（无 scope 概念）⇒ 不再是 404（此时才走到编排层的 503）。
	globalEngine := apiKeyPublicEngineDB(db, "sk-global-1234567890")
	if recorder := doRequest(globalEngine, http.MethodPost, "/ai/v1/chat/completions", chatBody,
		map[string]string{"Authorization": "Bearer sk-global-1234567890"}); recorder.Code == http.StatusNotFound {
		t.Fatalf("全局 key 不该触发白名单准入: %s", recorder.Body.String())
	}

	// ② /ai/v1/models 只列白名单内的模型（未设 scope 的 Key 见全部启用模型）。
	modelsPayload := func(recorder *httptest.ResponseRecorder) []string {
		t.Helper()
		var body struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
			t.Fatalf("模型列表不是 JSON: %v（%s）", err, recorder.Body.String())
		}
		names := make([]string, 0, len(body.Data))
		for _, item := range body.Data {
			names = append(names, item.ID)
		}
		sort.Strings(names)
		return names
	}
	recorder = doRequest(engine, http.MethodGet, "/ai/v1/models", "", auth)
	if recorder.Code != http.StatusOK {
		t.Fatalf("模型列表状态码 = %d（body=%s）", recorder.Code, recorder.Body.String())
	}
	if names := modelsPayload(recorder); len(names) != 1 || names[0] != "gpt-4" {
		t.Fatalf("带白名单的 Key 只该看到 gpt-4，实际 %v", names)
	}
	recorder = doRequest(globalEngine, http.MethodGet, "/ai/v1/models", "",
		map[string]string{"Authorization": "Bearer sk-global-1234567890"})
	if names := modelsPayload(recorder); len(names) != 2 {
		t.Fatalf("全局 key 应看到全部启用模型（2 条），实际 %v", names)
	}
}
