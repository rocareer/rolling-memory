package source

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// 库级用例（DSN 门控）：验证 store 的**事务边界**（来源行与快照行必须同生同死）。
//
// 为什么必须真库：UpdateSource / DeleteSources 的一致性靠 DB 事务保证，内存假库（store_test.go 的
// memorySourceStore）只断言调用序列、无法证明「清空快照时条目真的被删」。
//
// 为什么不用 PLUGIN_DB=1：那套门控连的是配置里的**开发库**（本项目开发库有已装 demo 插件与实体数据，
// 库级测试不得指向它）。本文件只认显式给定的临时库 DSN，且只 AutoMigrate 两张新表：
//
//	CATALOG_TEST_DSN='postgres://用户:口令@主机:端口/临时库?sslmode=disable' \
//	  go test -count=1 -run TestStoreSnapshotLifecycleDB -v ./internal/plugin/source/
//
// 未设 DSN ⇒ 整组跳过（默认 `go test ./...` 不连库、不需 Postgres）。
func catalogTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("CATALOG_TEST_DSN")
	if dsn == "" {
		t.Skip("需 CATALOG_TEST_DSN（指向**临时库**；勿指向开发库）")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("连接临时库失败: %v", err)
	}
	if err := db.AutoMigrate(&Source{}, &CatalogEntry{}); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	// 逐用例清场（临时库专用；不触碰 admin_plugins 等其它表）
	if err := db.Exec("TRUNCATE admin_plugin_sources, admin_plugin_catalog_entries RESTART IDENTITY").Error; err != nil {
		t.Fatalf("清场失败: %v", err)
	}
	return db
}

func TestStoreSnapshotLifecycleDB(t *testing.T) {
	db := catalogTestDB(t)
	ctx := context.Background()
	store := NewGormStore(func(context.Context) (*gorm.DB, error) { return db, nil })

	keep := &Source{Name: "keep", Title: "保留来源", Type: TypeLocal, Location: "keep.yaml", Enabled: 1}
	edit := &Source{Name: "edit", Title: "被改来源", Type: TypeLocal, Location: "old.yaml", Enabled: 1}
	for _, row := range []*Source{keep, edit} {
		if err := store.CreateSource(ctx, row); err != nil {
			t.Fatalf("创建来源 %s 失败: %v", row.Name, err)
		}
		if err := store.ReplaceCatalog(ctx, row, catalogFor(row.Name), sha64(row.Name), time.Now()); err != nil {
			t.Fatalf("同步 %s 失败: %v", row.Name, err)
		}
	}
	entriesOf := func(id uint) int64 {
		var count int64
		if err := db.Model(&CatalogEntry{}).Where("source_id = ?", id).Count(&count).Error; err != nil {
			t.Fatalf("统计快照行失败: %v", err)
		}
		return count
	}
	if entriesOf(keep.ID) != 1 || entriesOf(edit.ID) != 1 {
		t.Fatalf("前置快照行不符: keep=%d edit=%d", entriesOf(keep.ID), entriesOf(edit.ID))
	}

	// ① 只改展示名：不清快照（ClearSnapshot=false 时条目必须留着）
	title := "改过的展示名"
	if cleared, err := store.UpdateSource(ctx, edit.ID, SourcePatch{Title: &title}); err != nil || cleared {
		t.Fatalf("只改 title 不应清快照: cleared=%v err=%v", cleared, err)
	}
	if entriesOf(edit.ID) != 1 {
		t.Fatalf("只改 title 后条目数应保持 1，实际 %d", entriesOf(edit.ID))
	}

	// ② 改 location（快照受影响）：同一事务里删条目 + 清 last_*，另一来源的条目不得受牵连
	newLocation := "new.yaml"
	if cleared, err := store.UpdateSource(ctx, edit.ID, SourcePatch{Location: &newLocation, ClearSnapshot: true}); err != nil || !cleared {
		t.Fatalf("改 location 应清快照: cleared=%v err=%v", cleared, err)
	}
	if got := entriesOf(edit.ID); got != 0 {
		t.Fatalf("清空快照后该来源条目应为 0，实际 %d（方案：快照 = entries 表）", got)
	}
	if got := entriesOf(keep.ID); got != 1 {
		t.Fatalf("另一来源的快照被误删（实际 %d）", got)
	}
	row, err := store.GetSource(ctx, edit.ID)
	if err != nil {
		t.Fatalf("读回来源失败: %v", err)
	}
	if row.LastSyncStatus != "" || row.LastCatalogSHA256 != "" || row.LastSyncedAt != nil {
		t.Fatalf("清空快照后 last_* 应为空: %+v", row)
	}

	// ③ 列表侧不再出现「已清空来源」的条目，entry_count 同步归零（方案自洽要求）
	summaries, err := store.ListSourcesSummary(ctx)
	if err != nil {
		t.Fatalf("汇总失败: %v", err)
	}
	for _, item := range summaries {
		if item.Source.Name == "edit" && item.EntryCount != 0 {
			t.Fatalf("已清空来源的 entry_count 应为 0，实际 %d", item.EntryCount)
		}
		if item.Source.Name == "keep" && item.EntryCount != 1 {
			t.Fatalf("未受影响来源的 entry_count 应为 1，实际 %d", item.EntryCount)
		}
	}

	// ④ 删除来源：条目同事务删除
	if _, err := store.DeleteSources(ctx, []uint{keep.ID}); err != nil {
		t.Fatalf("删除来源失败: %v", err)
	}
	if got := entriesOf(keep.ID); got != 0 {
		t.Fatalf("删除来源后条目应清空，实际 %d", got)
	}
	if _, err := store.GetSource(ctx, keep.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("删除后应 404 语义（ErrNotFound），实际 %v", err)
	}
}

// TestStoreListEntriesFiltersDB 清单列举的过滤条件（来源 / uid / 版本 / 关键字）必须真的进 SQL。
//
// 回归的是「泛型入口丢条件」这一类缺陷：条件若预挂在普通 *gorm.DB 上再交给 `gorm.G[T](db)`，
// 泛型入口会以 NewDB 会话重建语句（gorm.io/gorm 的 generics.go：`db.Session(&Session{NewDB: true})`
// 得到的 Statement 里 Clauses 为空），预挂的 Where 被**静默丢弃**——SQL 里既无 WHERE、也不报错，
// 只有 rows/total 一律是未过滤的读数（ORDER BY/LIMIT/OFFSET 本就挂在泛型链上，故分页看着是对的）。
//
// 断言只能落在真库的返回行数与 total 上：DryRun/ToSQL 抓不到（DryRun 分支不重建语句，同样的条件
// 在 DryRun 下反而带 WHERE），内存假库也抓不到（store_test.go 的 memorySourceStore 整个忽略查询体）。
func TestStoreListEntriesFiltersDB(t *testing.T) {
	db := catalogTestDB(t)
	ctx := context.Background()
	store := NewGormStore(func(context.Context) (*gorm.DB, error) { return db, nil })

	alpha := &Source{Name: "alpha", Title: "甲来源", Type: TypeLocal, Location: "alpha.yaml", Enabled: 1}
	beta := &Source{Name: "beta", Title: "乙来源", Type: TypeLocal, Location: "beta.yaml", Enabled: 1}
	for _, row := range []*Source{alpha, beta} {
		if err := store.CreateSource(ctx, row); err != nil {
			t.Fatalf("创建来源 %s 失败: %v", row.Name, err)
		}
	}
	// 两来源三 uid：alpha-demo@1.0.0 与 beta-demo@2.1.0 属 alpha，gamma-demo@3.0.0 属 beta
	if err := store.ReplaceCatalog(ctx, alpha, catalogWith("alpha.yaml", "甲插件 ", "alpha-demo", "1.0.0", "beta-demo", "2.1.0"), sha64("alpha"), time.Now()); err != nil {
		t.Fatalf("同步 alpha 失败: %v", err)
	}
	if err := store.ReplaceCatalog(ctx, beta, catalogWith("beta.yaml", "乙插件 ", "gamma-demo", "3.0.0"), sha64("beta"), time.Now()); err != nil {
		t.Fatalf("同步 beta 失败: %v", err)
	}

	cases := []struct {
		name      string
		query     ListEntriesQuery
		wantRows  int
		wantTotal int64
		wantUID   string
	}{
		{"未过滤", ListEntriesQuery{}, 3, 3, ""},
		{"按来源", ListEntriesQuery{SourceID: alpha.ID}, 2, 2, ""},
		{"按 uid", ListEntriesQuery{UID: "alpha-demo"}, 1, 1, "alpha-demo"},
		{"按版本", ListEntriesQuery{Version: "2.1.0"}, 1, 1, "beta-demo"},
		{"uid+版本命中", ListEntriesQuery{UID: "alpha-demo", Version: "1.0.0"}, 1, 1, "alpha-demo"},
		{"同 uid 别的版本应 0 条", ListEntriesQuery{UID: "alpha-demo", Version: "2.1.0"}, 0, 0, ""},
		{"来源+uid", ListEntriesQuery{SourceID: beta.ID, UID: "gamma-demo"}, 1, 1, "gamma-demo"},
		{"来源与 uid 不匹配应 0 条", ListEntriesQuery{SourceID: beta.ID, UID: "alpha-demo"}, 0, 0, ""},
		{"关键字命中 uid", ListEntriesQuery{Keyword: "gamma"}, 1, 1, "gamma-demo"},
		{"关键字命中中文标题", ListEntriesQuery{Keyword: "乙插件"}, 1, 1, "gamma-demo"},
		{"关键字无命中应 0 条", ListEntriesQuery{Keyword: "无此关键字"}, 0, 0, ""},
		{"不存在的 uid 应 0 条", ListEntriesQuery{UID: "no-such-plugin"}, 0, 0, ""},
		{"来源+关键字", ListEntriesQuery{SourceID: alpha.ID, Keyword: "插件"}, 2, 2, ""},
		{"过滤+分页第二页", ListEntriesQuery{SourceID: alpha.ID, Page: 2, Limit: 1}, 1, 2, ""},
		{"过滤+分页越界", ListEntriesQuery{UID: "alpha-demo", Page: 2, Limit: 1}, 0, 1, ""},
		// 复合：四个条件同时入 SQL（任一被丢则 total 变大或变小，见上「过滤条件被丢弃时」的提示语）
		{"uid+版本+关键字+分页复合命中", ListEntriesQuery{UID: "alpha-demo", Version: "1.0.0", Keyword: "alpha", Page: 1, Limit: 1}, 1, 1, "alpha-demo"},
		{"复合条件其一不中应 0 条", ListEntriesQuery{UID: "alpha-demo", Version: "1.0.0", Keyword: "无此关键字"}, 0, 0, ""},
		// 区分性要成立：本条的期望读数必须与「未过滤」不同（否则旧形态下也绿，钉不住东西——
		// 与「probe 的非区分性读数不算证据」同一口径）。
		{"关键字过滤+分页越界（total 仍为过滤后）", ListEntriesQuery{Keyword: "alpha", Page: 2, Limit: 1}, 0, 1, ""},
	}
	for _, testCase := range cases {
		rows, total, err := store.ListEntries(ctx, testCase.query)
		if err != nil {
			t.Fatalf("%s: 查询失败: %v", testCase.name, err)
		}
		if len(rows) != testCase.wantRows || total != testCase.wantTotal {
			t.Errorf("%s: rows=%d total=%d，期望 rows=%d total=%d（过滤条件被丢弃时 total 会是未过滤的 3）",
				testCase.name, len(rows), total, testCase.wantRows, testCase.wantTotal)
			continue
		}
		if testCase.wantUID != "" && rows[0].UID != testCase.wantUID {
			t.Errorf("%s: 命中 %s，期望 %s", testCase.name, rows[0].UID, testCase.wantUID)
		}
	}
}

// TestStoreListSourcesFiltersDB 来源列表的 wheres 同样必须真的进 SQL（管理面来源页的搜索走这条）。
//   - 组内按 group.Or 连接、组间 AND（与宿主通用 List 的 wheres 语义一致）；
//   - 白名单外的字段/运算符整组跳过（＝不参与条件），不是「返回 0 条」；
//   - 「不匹配应 0 条」是负例：条件被丢弃时它返回全表。
func TestStoreListSourcesFiltersDB(t *testing.T) {
	db := catalogTestDB(t)
	ctx := context.Background()
	store := NewGormStore(func(context.Context) (*gorm.DB, error) { return db, nil })

	for _, row := range []*Source{
		{Name: "alpha", Title: "甲来源", Type: TypeLocal, Location: "alpha.yaml", Enabled: 1},
		{Name: "beta", Title: "乙来源", Type: TypeLocal, Location: "beta.yaml", Enabled: 1},
	} {
		if err := store.CreateSource(ctx, row); err != nil {
			t.Fatalf("创建来源 %s 失败: %v", row.Name, err)
		}
	}

	cases := []struct {
		name   string
		wheres []FilterGroup
		want   int64
	}{
		{"未过滤", nil, 2},
		{"按 name", []FilterGroup{{Filters: []Filter{{Field: "name", Operator: "eq", Value: "alpha"}}}}, 1},
		{"不存在的 name 应 0 条", []FilterGroup{{Filters: []Filter{{Field: "name", Operator: "eq", Value: "no-such"}}}}, 0},
		{"组内 AND 全中", []FilterGroup{{Filters: []Filter{
			{Field: "name", Operator: "eq", Value: "alpha"},
			{Field: "title", Operator: "LIKE", Value: "甲"},
		}}}, 1},
		{"组内 AND 半中应 0 条", []FilterGroup{{Filters: []Filter{
			{Field: "name", Operator: "eq", Value: "alpha"},
			{Field: "title", Operator: "LIKE", Value: "乙"},
		}}}, 0},
		{"组间 AND", []FilterGroup{
			{Filters: []Filter{{Field: "name", Operator: "eq", Value: "alpha"}}},
			{Filters: []Filter{{Field: "enabled", Operator: "eq", Value: 1}}},
		}, 1},
		{"组间 AND 其一不中应 0 条", []FilterGroup{
			{Filters: []Filter{{Field: "name", Operator: "eq", Value: "alpha"}}},
			{Filters: []Filter{{Field: "enabled", Operator: "eq", Value: 0}}},
		}, 0},
		{"Or 组", []FilterGroup{{Or: true, Filters: []Filter{
			{Field: "name", Operator: "eq", Value: "alpha"},
			{Field: "name", Operator: "eq", Value: "beta"},
		}}}, 2},
		{"白名单外字段整组跳过", []FilterGroup{{Filters: []Filter{{Field: "no_such_column", Operator: "eq", Value: 1}}}}, 2},
		{"白名单外运算符整组跳过", []FilterGroup{{Filters: []Filter{{Field: "name", Operator: "REGEXP", Value: "a"}}}}, 2},
	}
	for _, testCase := range cases {
		rows, total, err := store.ListSources(ctx, ListSourcesQuery{Wheres: testCase.wheres})
		if err != nil {
			t.Fatalf("%s: 查询失败: %v", testCase.name, err)
		}
		if total != testCase.want || int64(len(rows)) != testCase.want {
			t.Errorf("%s: rows=%d total=%d，期望均为 %d（条件被丢弃时 total 会是全表 2）",
				testCase.name, len(rows), total, testCase.want)
		}
	}
}

// catalogWith 造一份多条目的清单（titlePrefix + 交替给出的 uid/version 对），用于落多行快照。
func catalogWith(path, titlePrefix string, pairs ...string) *Catalog {
	doc := &Catalog{Schema: CatalogSchema, GeneratedAt: "2026-10-23T00:00:00Z"}
	for i := 0; i+1 < len(pairs); i += 2 {
		uid, version := pairs[i], pairs[i+1]
		doc.Plugins = append(doc.Plugins, PluginEntry{
			UID: uid, Version: version, TitleZH: titlePrefix + uid, TitleEN: "Demo " + uid,
			DescZH: "示例插件", DescEN: "Demo plugin", Kind: "code", HostAPI: ">=1 <2",
			Package: CatalogPackage{Path: path, Size: 1024, SHA256: sha64(uid + "@" + version), Signed: boolPtr(false)},
		})
	}
	return doc
}

// catalogFor 造一份最小合法清单（1 条条目），用于落快照行。
func catalogFor(name string) *Catalog {
	return &Catalog{
		Schema: CatalogSchema,
		Plugins: []PluginEntry{{
			UID: "demo", Version: "1.0.0", TitleZH: "示例", TitleEN: "Demo",
			DescZH: "示例插件", DescEN: "Demo plugin", Kind: "code", HostAPI: ">=1 <2",
			Package: CatalogPackage{Path: name + ".zip", Size: 1024, SHA256: sha64(name), Signed: boolPtr(false)},
		}},
	}
}

// sha64 造一个 64 位小写十六进制摘要（同一种子恒等，便于断言）。
func sha64(seed string) string {
	const hexDigits = "0123456789abcdef"
	out := make([]byte, 64)
	for i := range out {
		out[i] = hexDigits[(int(seed[i%len(seed)])+i)%16]
	}
	return string(out)
}

func boolPtr(value bool) *bool { return &value }
