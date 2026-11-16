// plugin: ai v0.2.0
// store_db_test.go 是 SQL/GORM 层的库级用例（DSN 门控）：本插件的 SQL 层此前零覆盖——
// 总览端点「渠道健康必报错」与「usage 文本列超长整行静默丢失」两个缺陷都只在**真库**上暴露
// （GORM 的 schema 解析与 PostgreSQL 的列宽约束，内存假库/DryRun 都抓不到）。
//
// 用法（**必须指向临时库**：用例会 drop/重建本插件的全部物理表）：
//
//	PLUGIN_AI_TEST_DSN='postgres://用户:口令@主机:端口/临时库?sslmode=disable' \
//	  go test -count=1 -run TestStore -v ./plugins/ai/
//
// 未设 DSN ⇒ 整组跳过（默认 `go test ./plugins/ai/` 不连库、不需要 Postgres）。
//
// 表结构取自插件自己的**全部**迁移文件（migrations/*.up.sql 按编号升序 + 进程表前缀），
// 与安装器落库同物：新增迁移（2、3…）落进目录后，本文件无需再改一处——清场按
// <前缀>plugin_ai_% 扫描 drop（含迁移版本表本身），建表按编号升序逐文件应用。
// 这与安装器口径一致：migrate_run.go 只推「版本 > 当前」的待执行版本，跳号会被永久静默跳过，
// 故这里对编号 1..N 连续性做硬校验（失败即测试红，不静默跳过）。
package ai

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/ai-go-hub/ai-go-admin/internal/infra/config"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// pluginAITestDSNEnv 是库级用例的 DSN 门控（指向临时库）。
const pluginAITestDSNEnv = "PLUGIN_AI_TEST_DSN"

// pluginAITestMigrationsDir 是插件自管迁移目录（相对包目录；go test 的 CWD = 包目录）。
const pluginAITestMigrationsDir = "migrations"

// pluginAITestTableLikePattern 是清场用的 LIKE 模式：<表前缀>plugin_ai_%。
//
// 用前缀扫描而不是写死表名清单：新增迁移建出的表（supplier/api_key/task/voice…）与
// 版本表 <前缀>plugin_ai_migrations 都在同一命名面内，写死清单会让新表留在临时库里
// （下一次跑用例的「防误指向」判据、以及「跑完表数回到 0」的读数都会被污染）。
// 前缀与主干里的 _ / % 按字面转义：LIKE 的 _ 是单字符通配符，不转义会误命中同前缀的邻表。
func pluginAITestTableLikePattern() string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(table("plugin_ai_")) + "%"
}

// pluginAITestTableNames 返回库里当前属于本插件的全部物理表（按名升序，便于稳定输出）。
func pluginAITestTableNames(db *gorm.DB) ([]string, error) {
	names := []string{}
	err := db.Raw(`SELECT tablename FROM pg_tables
		WHERE schemaname = current_schema() AND tablename LIKE ? ESCAPE '\'
		ORDER BY tablename`, pluginAITestTableLikePattern()).Scan(&names).Error
	if err != nil {
		return nil, err
	}
	return names, nil
}

// dropPluginAITestTables 清掉本插件的全部物理表（只碰 <前缀>plugin_ai_% 命中的表，不猜任何别的表）。
func dropPluginAITestTables(db *gorm.DB) error {
	names, err := pluginAITestTableNames(db)
	if err != nil {
		return err
	}
	for _, name := range names {
		if err := db.Exec(`DROP TABLE IF EXISTS ` + quoteIdent(name)).Error; err != nil {
			return err
		}
	}
	return nil
}

// pluginAITestMigration 是一个待应用的 up 迁移（编号 + 文件路径）。
type pluginAITestMigration struct {
	version int
	path    string
}

// pluginAITestUpMigrations 枚举 migrations/*.up.sql 并按编号升序返回。
//
// 文件名口径与安装器同物（internal/plugin/cli/migrate.go 的 migrationFilePattern：<编号>_<名>.up.sql）；
// 编号必须是 1..N 连续前缀——跳号在安装器里会被静默跳过（只推 version > 当前），
// 在测试基建里静默跳过则等于用残缺 schema 跑用例，两种都必须响铃。
func pluginAITestUpMigrations(t *testing.T) []pluginAITestMigration {
	t.Helper()
	entries, err := os.ReadDir(pluginAITestMigrationsDir)
	if err != nil {
		t.Fatalf("读取迁移目录失败: %v", err)
	}
	rows := make([]pluginAITestMigration, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".up.sql") {
			continue
		}
		head, _, ok := strings.Cut(name, "_")
		if !ok {
			t.Fatalf("迁移文件名 %q 不含编号段（应形如 <编号>_<名>.up.sql）", name)
		}
		version, err := strconv.Atoi(head)
		if err != nil {
			t.Fatalf("迁移文件名 %q 的编号段 %q 不是整数: %v", name, head, err)
		}
		rows = append(rows, pluginAITestMigration{
			version: version,
			path:    filepath.Join(pluginAITestMigrationsDir, name),
		})
	}
	if len(rows) == 0 {
		t.Fatalf("%s 下没有任何 .up.sql（插件 schema 的唯一事实源缺失）", pluginAITestMigrationsDir)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].version < rows[j].version })
	for i, row := range rows {
		if row.version != i+1 {
			t.Fatalf("插件迁移编号必须是 1..N 连续前缀：第 %d 个文件是 %d（%s）", i+1, row.version, row.path)
		}
	}
	return rows
}

// applyPluginAITestMigrations 按编号升序逐个应用 up 迁移（每个文件一个事务，与安装器的
// 「整文件一次执行、单个隐式事务」口径一致），__PREFIX__ 在执行期替换为进程表前缀。
func applyPluginAITestMigrations(t *testing.T, db *gorm.DB) {
	t.Helper()
	for _, migration := range pluginAITestUpMigrations(t) {
		migration := migration
		body, err := os.ReadFile(migration.path)
		if err != nil {
			t.Fatalf("读取迁移文件 %s 失败: %v", migration.path, err)
		}
		ddl := strings.ReplaceAll(string(body), "__PREFIX__", config.Get().Database.Prefix)
		if err := db.Transaction(func(tx *gorm.DB) error { return tx.Exec(ddl).Error }); err != nil {
			t.Fatalf("应用迁移 %s 失败: %v", migration.path, err)
		}
	}
}

// pluginAITestDB 连临时库、按前缀清场、按编号升序应用全部迁移建表，收尾再清场。详见文件头注。
func pluginAITestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv(pluginAITestDSNEnv))
	if dsn == "" {
		t.Skip("需 " + pluginAITestDSNEnv + "（指向**临时库**；勿指向开发/生产库）")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("连接临时库失败: %v", err)
	}
	// 连接池必须收口：本包的库级用例每个都新开一个句柄（本函数 + task_db_test.go / apikey_db_test.go
	// 的同款脚手架），不收口时空闲连接会随用例数累积——实测一组用例就把共享 PG 的
	// max_connections=100 打满，产出 SQLSTATE 53300 假红（「全 PASS」因此不可复现）。
	// Cleanup 是 LIFO：下面的收尾清场先跑，之后才关池。
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
		t.Fatalf("%s 指向的库存在 admin_plugins（疑似开发/生产库）：本用例会 drop/重建 plugin_ai_* 表，拒绝执行",
			pluginAITestDSNEnv)
	}

	if err := dropPluginAITestTables(db); err != nil {
		t.Fatalf("清场失败: %v", err)
	}
	applyPluginAITestMigrations(t, db)
	t.Cleanup(func() {
		if err := dropPluginAITestTables(db); err != nil {
			t.Errorf("收尾清场失败: %v", err)
		}
	})
	return db
}

// TestStoreChannelHealthDB 回归「总览端点必挂」：ChannelHealth.Problems 少了 gorm:"-" 时
// GORM 把 []Channel 当关联去猜外键，Scan 直接报 invalid field found —— 空表也报，
// 于是 POST /admin/plugin/ai/overview/index 整页 code=1。
func TestStoreChannelHealthDB(t *testing.T) {
	db := pluginAITestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	// ① 空表也必须成功（修复前这里是报错现场）。
	health, err := store.ChannelHealth(ctx)
	if err != nil {
		t.Fatalf("空表读取渠道健康失败（Problems 缺 gorm:\"-\" 时必报 invalid field found）: %v", err)
	}
	if health.Total != 0 || health.Enabled != 0 || health.CircuitOpen != 0 || health.CircuitHalfOpen != 0 || len(health.Problems) != 0 {
		t.Fatalf("空表读数不符: %+v", health)
	}

	// ② 非空表：聚合段与异常样本段（Problems 的第二段查询）都要真的取到数据。
	seeded := []Channel{
		{Name: "open-ch", Model: "gpt-test", BaseURL: "https://example.invalid/v1",
			Status: StatusEnabled, Circuit: CircuitOpen, OpenedAt: ptrInt64(100), FailThreshold: 3, CooldownSeconds: 90},
		{Name: "err-ch", Model: "gpt-test", BaseURL: "https://example.invalid/v1",
			Status: StatusEnabled, Circuit: CircuitClosed, LastError: "boom"},
		{Name: "off-ch", Model: "gpt-test", BaseURL: "https://example.invalid/v1",
			Status: StatusEnabled, Circuit: CircuitClosed},
	}
	for i := range seeded {
		if err := store.CreateChannel(ctx, &seeded[i]); err != nil {
			t.Fatalf("建渠道 %s 失败: %v", seeded[i].Name, err)
		}
	}
	// 停用行用显式 UPDATE 落 status=0：GORM 的 Create 会省略「带 default 标签且为零值」的字段
	// （status 落了 DB 默认 1），所以不能在 Create 里造 status=0 的行——这是 GORM 的既有行为，
	// 不是本用例要验的对象（生产侧「新建时选停用」的落库口径另行上报）。
	disabled := StatusDisabled
	if _, err := store.UpdateChannel(ctx, seeded[2].ID, ChannelPatch{Status: &disabled}); err != nil {
		t.Fatalf("停用渠道失败: %v", err)
	}
	health, err = store.ChannelHealth(ctx)
	if err != nil {
		t.Fatalf("读取渠道健康失败: %v", err)
	}
	if health.Total != 3 || health.Enabled != 2 || health.CircuitOpen != 1 || health.CircuitHalfOpen != 0 {
		t.Fatalf("聚合读数不符: %+v", health)
	}
	if len(health.Problems) != 2 {
		t.Fatalf("异常样本应 2 条（circuit<>closed 或 last_error<>''），实际 %d: %+v", len(health.Problems), health.Problems)
	}
	names := map[string]bool{}
	for _, row := range health.Problems {
		names[row.Name] = true
	}
	if !names["open-ch"] || !names["err-ch"] || names["off-ch"] {
		t.Fatalf("异常样本命中不对: %+v", names)
	}
}

// TestStoreModelHealthDB 覆盖总览的另一段读取（同一条 Scan 路径上的模型计数）。
func TestStoreModelHealthDB(t *testing.T) {
	db := pluginAITestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	health, err := store.ModelHealth(ctx)
	if err != nil {
		t.Fatalf("空表读取模型统计失败: %v", err)
	}
	if health.Total != 0 || health.Enabled != 0 {
		t.Fatalf("空表读数不符: %+v", health)
	}

	models := []Model{
		{Name: "gpt-test", Type: CallTypeChat, PriceInput: "1.000000", PriceOutput: "2.000000", Status: StatusEnabled},
		{Name: "gpt-off", Type: CallTypeChat, PriceInput: "1.000000", PriceOutput: "2.000000", Status: StatusEnabled},
	}
	for i := range models {
		if err := store.CreateModel(ctx, &models[i]); err != nil {
			t.Fatalf("建模型 %s 失败: %v", models[i].Name, err)
		}
	}
	// 同 ①：Create 省略零值 status（回落 DB 默认 1），停用行用显式 UPDATE 落。
	disabled := StatusDisabled
	if _, err := store.UpdateModel(ctx, models[1].ID, ModelPatch{Status: &disabled}); err != nil {
		t.Fatalf("停用模型失败: %v", err)
	}
	health, err = store.ModelHealth(ctx)
	if err != nil {
		t.Fatalf("读取模型统计失败: %v", err)
	}
	if health.Total != 2 || health.Enabled != 1 {
		t.Fatalf("模型读数不符: %+v", health)
	}
}

// TestStoreUpsertUsageTruncatesOverlongTextDB 回归「计费/审计行静默丢失」：
// 公开面自带的 31 字符 biz_type 修复前会让整行报 22001（调用方仍拿到 200、这行永久丢失、
// 渠道 total_calls 却仍 +1）；现在写口按列宽兜底截断，行必须落库且 (request_id,attempt) 幂等覆盖不变。
func TestStoreUpsertUsageTruncatesOverlongTextDB(t *testing.T) {
	db := pluginAITestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	const overlongBizType = "internal-batch-job-daily-report" // 31 字符 > varchar(30)
	usage := Usage{
		RequestID:    "req-db-1",
		Attempt:      0,
		CallType:     CallTypeChat,
		BizType:      overlongBizType,
		Source:       publicSource,
		Status:       UsageStatusSuccess,
		TokensSource: TokensSourceUsage,
		Cost:         "0.000000",
		Currency:     DefaultCurrency,
	}
	if err := store.UpsertUsage(ctx, &usage); err != nil {
		t.Fatalf("31 字符 biz_type 必须被列宽兜底截断、不得整行失败（修复前这里返回 22001）: %v", err)
	}
	chain, err := store.UsageDetailChain(ctx, "req-db-1")
	if err != nil {
		t.Fatalf("读尝试链失败: %v", err)
	}
	if len(chain) != 1 {
		t.Fatalf("usage 行数 = %d，期望 1（整行丢失时会是 0）", len(chain))
	}
	if chain[0].BizType != overlongBizType[:30] {
		t.Fatalf("落库 biz_type = %q，期望截断后的 %q", chain[0].BizType, overlongBizType[:30])
	}

	// 同 (request_id,attempt) 再写一次：覆盖业务列、不新增行（ON CONFLICT 契约）。
	usage.Status = UsageStatusFail
	usage.ErrorCode = "invalid_response"
	if err := store.UpsertUsage(ctx, &usage); err != nil {
		t.Fatalf("幂等覆盖失败: %v", err)
	}
	chain, err = store.UsageDetailChain(ctx, "req-db-1")
	if err != nil {
		t.Fatalf("读尝试链失败: %v", err)
	}
	if len(chain) != 1 || chain[0].Status != UsageStatusFail {
		t.Fatalf("幂等覆盖后应仍是 1 行且 status=fail，实际 %d 行 %+v", len(chain), chain)
	}
}
