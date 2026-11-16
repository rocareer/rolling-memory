package database

import (
	"path/filepath"
	"reflect"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func openBootstrapTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "rolling-admin-bootstrap-test.db") +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	db, err := gorm.Open(sqlite.New(sqlite.Config{DriverName: "sqlite", DSN: dsn}), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("打开临时 SQLite 库失败: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

type bootstrapMenuRow struct {
	Title string
	Weigh int
	Pid   *int
}

func readBootstrapMenuRow(t *testing.T, db *gorm.DB, name string) bootstrapMenuRow {
	t.Helper()
	var row bootstrapMenuRow
	if err := db.Raw(`SELECT "title", "weigh", "pid" FROM "admin_rules" WHERE "name" = ?`, name).Scan(&row).Error; err != nil {
		t.Fatalf("读取菜单 %s: %v", name, err)
	}
	return row
}

// TestBootstrapSQLiteDeveloperMenuOrder 钉住面向开发者的导航层级：日常面（控制台 → 记忆 → 评测 → 帮助）在上，
// 权限 / 常规 / 插件管理面沉底；记忆目录内「检索调试」置顶；新库、出厂旧库两条路径结果一致且重复启动幂等。
func TestBootstrapSQLiteDeveloperMenuOrder(t *testing.T) {
	assertOrder := func(t *testing.T, db *gorm.DB) {
		t.Helper()
		order := []string{"dashboard", "plugin/rolling-memory", "plugin/rolling-memory/eval-dir", "help", "auth", "routine", "plugin-center"}
		for i := 1; i < len(order); i++ {
			prev, next := readBootstrapMenuRow(t, db, order[i-1]), readBootstrapMenuRow(t, db, order[i])
			if prev.Weigh <= next.Weigh {
				t.Fatalf("%s(weigh=%d) 应排在 %s(weigh=%d) 之前", order[i-1], prev.Weigh, order[i], next.Weigh)
			}
		}
		if got := readBootstrapMenuRow(t, db, "plugin/rolling-memory").Title; got != "记忆浏览" {
			t.Fatalf("记忆目录 title = %q", got)
		}
		recall := readBootstrapMenuRow(t, db, "plugin/rolling-memory/recall")
		if recall.Title != "检索调试" || recall.Pid == nil || *recall.Pid != 100 {
			t.Fatalf("检索调试菜单 = %+v", recall)
		}
		for _, sibling := range []string{"plugin/rolling-memory/index", "plugin/rolling-memory/background", "plugin/rolling-memory/episodic", "plugin/rolling-memory/noetic"} {
			if row := readBootstrapMenuRow(t, db, sibling); row.Weigh >= recall.Weigh {
				t.Fatalf("检索调试(weigh=%d) 应排在 %s(weigh=%d) 之前", recall.Weigh, sibling, row.Weigh)
			}
		}
		evalOrder := []string{"plugin/rolling-memory/eval", "plugin/rolling-memory/atm-leaderboard", "plugin/rolling-memory/leaderboard"}
		for i := 1; i < len(evalOrder); i++ {
			prev, next := readBootstrapMenuRow(t, db, evalOrder[i-1]), readBootstrapMenuRow(t, db, evalOrder[i])
			if prev.Weigh <= next.Weigh {
				t.Fatalf("评测子菜单 %s(weigh=%d) 应排在 %s(weigh=%d) 之前", evalOrder[i-1], prev.Weigh, evalOrder[i], next.Weigh)
			}
		}
		titles := map[string]string{
			"plugin/rolling-memory/background": "L1 文档与代码",
			"plugin/rolling-memory/episodic":   "L2 会话记录",
			"plugin/rolling-memory/noetic":     "L3 检索沉淀",
		}
		for name, want := range titles {
			if got := readBootstrapMenuRow(t, db, name).Title; got != want {
				t.Fatalf("%s title = %q, want %q", name, got, want)
			}
		}
		var recentNode int64
		db.Raw(`SELECT COUNT(*) FROM "admin_rules" WHERE "name" = 'plugin/rolling-memory/recall/recent' AND "pid" = 116 AND "type" = 'node'`).Scan(&recentNode)
		if recentNode != 1 {
			t.Fatalf("recall/recent 权限节点缺失（count=%d）", recentNode)
		}
	}

	t.Run("fresh", func(t *testing.T) {
		db := openBootstrapTestDB(t)
		for i := 0; i < 2; i++ {
			if err := BootstrapSQLite(db); err != nil {
				t.Fatalf("BootstrapSQLite #%d: %v", i+1, err)
			}
		}
		assertOrder(t, db)
	})

	t.Run("upgrade-from-factory", func(t *testing.T) {
		db := openBootstrapTestDB(t)
		if err := BootstrapSQLite(db); err != nil {
			t.Fatalf("BootstrapSQLite: %v", err)
		}
		// 回放旧版出厂值（升级前的已装库形态）后再启动一次。
		for _, stmt := range []string{
			`UPDATE "admin_rules" SET "title" = '记忆管理' WHERE "name" = 'plugin/rolling-memory'`,
			`UPDATE "admin_rules" SET "weigh" = 998 WHERE "name" = 'auth'`,
			`UPDATE "admin_rules" SET "weigh" = 997 WHERE "name" = 'routine'`,
			`UPDATE "admin_rules" SET "weigh" = 989 WHERE "name" = 'plugin-center'`,
			`UPDATE "admin_rules" SET "weigh" = 95, "title" = '检索召回' WHERE "name" = 'plugin/rolling-memory/recall'`,
			`UPDATE "admin_rules" SET "title" = 'L1 背景记忆' WHERE "name" = 'plugin/rolling-memory/background'`,
			`UPDATE "admin_rules" SET "title" = 'L2 情景记忆' WHERE "name" = 'plugin/rolling-memory/episodic'`,
			`UPDATE "admin_rules" SET "title" = 'L3 认知记忆' WHERE "name" = 'plugin/rolling-memory/noetic'`,
			`UPDATE "admin_rules" SET "weigh" = 98 WHERE "name" = 'plugin/rolling-memory/leaderboard'`,
			`UPDATE "admin_rules" SET "weigh" = 97 WHERE "name" = 'plugin/rolling-memory/atm-leaderboard'`,
			`DELETE FROM "admin_rules" WHERE "name" = 'plugin/rolling-memory/recall/recent'`,
		} {
			if err := db.Exec(stmt).Error; err != nil {
				t.Fatalf("回放旧出厂值 %q: %v", stmt, err)
			}
		}
		if err := BootstrapSQLite(db); err != nil {
			t.Fatalf("BootstrapSQLite after downgrade replay: %v", err)
		}
		assertOrder(t, db)
	})

	t.Run("user-customized-rows-untouched", func(t *testing.T) {
		db := openBootstrapTestDB(t)
		if err := BootstrapSQLite(db); err != nil {
			t.Fatalf("BootstrapSQLite: %v", err)
		}
		if err := db.Exec(`UPDATE "admin_rules" SET "weigh" = 1500 WHERE "name" = 'auth'`).Error; err != nil {
			t.Fatalf("模拟用户调整: %v", err)
		}
		if err := db.Exec(`UPDATE "admin_rules" SET "title" = '我的 L1' WHERE "name" = 'plugin/rolling-memory/background'`).Error; err != nil {
			t.Fatalf("模拟用户改名: %v", err)
		}
		if err := BootstrapSQLite(db); err != nil {
			t.Fatalf("BootstrapSQLite: %v", err)
		}
		if got := readBootstrapMenuRow(t, db, "auth").Weigh; got != 1500 {
			t.Fatalf("用户调整过的 auth weigh 被覆盖为 %d", got)
		}
		if got := readBootstrapMenuRow(t, db, "plugin/rolling-memory/background").Title; got != "我的 L1" {
			t.Fatalf("用户改过的 L1 标题被覆盖为 %q", got)
		}
	})
}

func TestBootstrapSQLitePreservesCustomizedMenuAndPermissions(t *testing.T) {
	db := openBootstrapTestDB(t)
	if err := BootstrapSQLite(db); err != nil {
		t.Fatalf("BootstrapSQLite: %v", err)
	}
	for _, stmt := range []string{
		`UPDATE "admin_rules" SET "title" = '我的记忆', "weigh" = 1500 WHERE "name" = 'plugin/rolling-memory'`,
		`UPDATE "admin_rules" SET "title" = '我的总览', "weigh" = 1501, "pid" = 1 WHERE "name" = 'plugin/rolling-memory/index'`,
		`UPDATE "admin_rules" SET "title" = '我的检索', "icon" = 'custom-icon', "pid" = 1 WHERE "name" = 'plugin/rolling-memory/recall'`,
		`UPDATE "admin_rules" SET "title" = '我的评测', "weigh" = 1502, "pid" = 1 WHERE "name" = 'plugin/rolling-memory/eval'`,
		`UPDATE "admin_rules" SET "title" = '我的图谱', "weigh" = 1503 WHERE "name" = 'dashboard/graph'`,
		`UPDATE "admin_rules" SET "title" = '我的 ROI', "icon" = 'custom-roi', "remark" = 'custom' WHERE "name" = 'dashboard/roi'`,
		`UPDATE "admin_rules" SET "pid" = 1 WHERE "name" IN ('plugin/rolling-memory/background/read', 'plugin/rolling-memory/episodic/read', 'plugin/rolling-memory/noetic/read', 'plugin/rolling-memory/layers/stats', 'plugin/rolling-memory/atm-leaderboard/read', 'plugin/rolling-memory/ai/config')`,
		`INSERT INTO "admin_rules" ("id", "pid", "type", "title", "name") VALUES (9001, 1, 'menu', '自定义文档', 'plugin/docs'), (9002, 9001, 'node', '查看自定义文档', 'plugin/docs/read'), (9003, 1, 'menu', '自定义项目', 'plugin/rolling-memory/ai'), (9004, 9003, 'node', '查看自定义项目', 'plugin/rolling-memory/ai/read')`,
	} {
		if err := db.Exec(stmt).Error; err != nil {
			t.Fatalf("模拟菜单定制 %q: %v", stmt, err)
		}
	}
	type rule struct {
		ID                                                                              int
		Pid                                                                             *int
		Type, Title, Name, Path, Icon, OpenType, URL, Component, Extend, Remark, Plugin string
		Keepalive, Weigh, Status                                                        int
	}
	readRules := func() []rule {
		t.Helper()
		var rows []rule
		if err := db.Raw(`SELECT id, pid, type, title, name, path, icon, open_type, url, component, keepalive, extend, remark, weigh, status, plugin FROM admin_rules ORDER BY id`).Scan(&rows).Error; err != nil {
			t.Fatalf("读取菜单: %v", err)
		}
		return rows
	}
	before := readRules()
	if err := BootstrapSQLite(db); err != nil {
		t.Fatalf("BootstrapSQLite after customization: %v", err)
	}
	if after := readRules(); !reflect.DeepEqual(before, after) {
		t.Fatalf("重复启动改变了定制菜单或权限节点\nbefore: %+v\nafter: %+v", before, after)
	}
}
