package database

import (
	"strings"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

// BootstrapSQLite 是 bootstrapSQLite 的导出别名：供插件模块与用例在**自有连接**上幂等建表
// （不便切全局 config 的场景可直接 `sqlite.Open(temp)` + 本函数；建出的表结构与线上 bootstrap
// 逐列同物——本项目 sqlite_bootstrap.go 是 SQLite 形态下建表的唯一事实源）。
func BootstrapSQLite(db *gorm.DB) error { return bootstrapSQLite(db) }

// bootstrapSQLite 在 SQLite 模式下幂等初始化核心表结构、基础配置、超管账号以及默认启用的 rolling-memory 插件。
func bootstrapSQLite(db *gorm.DB) error {
	ddls := []string{
		`CREATE TABLE IF NOT EXISTS "admins" (
			"id" INTEGER PRIMARY KEY AUTOINCREMENT,
			"username" varchar(64) NOT NULL DEFAULT '',
			"nickname" varchar(64) NOT NULL DEFAULT '',
			"avatar" varchar(255),
			"email" varchar(128),
			"mobile" varchar(16),
			"login_failure" bigint NOT NULL DEFAULT 0,
			"last_login_at" datetime,
			"last_login_ip" varchar(64),
			"password" varchar(255) NOT NULL DEFAULT '',
			"bio" varchar(255),
			"status" varchar(64) NOT NULL DEFAULT 'enable',
			"updated_at" datetime,
			"created_at" datetime,
			"deleted_at" datetime
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS "idx_admins_username" ON "admins" ("username") WHERE "deleted_at" IS NULL`,
		`CREATE TABLE IF NOT EXISTS "admin_rules" (
			"id" INTEGER PRIMARY KEY AUTOINCREMENT,
			"pid" bigint DEFAULT NULL,
			"type" varchar(64) NOT NULL DEFAULT '',
			"title" varchar(64) NOT NULL DEFAULT '',
			"name" varchar(64) NOT NULL DEFAULT '',
			"path" varchar(255),
			"icon" varchar(64),
			"open_type" varchar(64),
			"url" varchar(255),
			"component" varchar(255),
			"keepalive" smallint DEFAULT 0,
			"extend" varchar(64),
			"remark" varchar(255),
			"weigh" bigint DEFAULT 0,
			"status" smallint NOT NULL DEFAULT 1,
			"plugin" varchar(32) NOT NULL DEFAULT '',
			"updated_at" datetime,
			"created_at" datetime
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS "idx_admin_rules_name" ON "admin_rules" ("name")`,
		`CREATE TABLE IF NOT EXISTS "admin_groups" (
			"id" INTEGER PRIMARY KEY AUTOINCREMENT,
			"pid" bigint DEFAULT NULL,
			"name" varchar(64) NOT NULL DEFAULT '',
			"rules" text,
			"status" smallint NOT NULL DEFAULT 1,
			"updated_at" datetime,
			"created_at" datetime
		)`,
		`CREATE TABLE IF NOT EXISTS "admin_group_access" (
			"uid" bigint NOT NULL,
			"group_id" bigint NOT NULL,
			PRIMARY KEY ("uid", "group_id")
		)`,
		`CREATE TABLE IF NOT EXISTS "admin_logs" (
			"id" INTEGER PRIMARY KEY AUTOINCREMENT,
			"admin_id" bigint NOT NULL DEFAULT 0,
			"username" varchar(64) NOT NULL DEFAULT '',
			"url" varchar(1500) NOT NULL DEFAULT '',
			"title" varchar(100) DEFAULT '',
			"data" text NOT NULL DEFAULT '',
			"ip" varchar(64) NOT NULL DEFAULT '',
			"user_agent" varchar(255) DEFAULT '',
			"created_at" datetime
		)`,
		`CREATE TABLE IF NOT EXISTS "tokens" (
			"token" varchar(64) PRIMARY KEY,
			"type" varchar(32) NOT NULL,
			"user_id" bigint NOT NULL,
			"created_at" datetime,
			"expired_at" datetime NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS "captchas" (
			"key" varchar(64) PRIMARY KEY,
			"type" varchar(32) NOT NULL DEFAULT '',
			"code" varchar(255),
			"info" text,
			"created_at" datetime,
			"expired_at" datetime NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS "configs" (
			"id" INTEGER PRIMARY KEY AUTOINCREMENT,
			"name" varchar(64) NOT NULL DEFAULT '',
			"group" varchar(64) NOT NULL DEFAULT '',
			"title" varchar(64) NOT NULL DEFAULT '',
			"type" varchar(64) NOT NULL DEFAULT '',
			"tip" varchar(128) DEFAULT NULL,
			"value" text DEFAULT NULL,
			"dict" text DEFAULT NULL,
			"rule" varchar(128) DEFAULT NULL,
			"input_extend" varchar(255) DEFAULT NULL,
			"allow_del" smallint NOT NULL DEFAULT 1,
			"weigh" bigint NOT NULL DEFAULT 0
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS "idx_configs_name" ON "configs" ("name")`,
		`CREATE TABLE IF NOT EXISTS "attachments" (
			"id" INTEGER PRIMARY KEY AUTOINCREMENT,
			"topic" varchar(64) NOT NULL DEFAULT '',
			"user_id" bigint NOT NULL DEFAULT 0,
			"user_type" varchar(64) NOT NULL DEFAULT '',
			"url" varchar(255) NOT NULL DEFAULT '',
			"name" varchar(255) NOT NULL DEFAULT '',
			"size" bigint NOT NULL DEFAULT 0,
			"mimetype" varchar(64) NOT NULL DEFAULT '',
			"quote" bigint NOT NULL DEFAULT 0,
			"driver" varchar(64) NOT NULL DEFAULT '',
			"sha1" varchar(64) NOT NULL DEFAULT '',
			"created_at" datetime DEFAULT NULL,
			"last_upload_at" datetime DEFAULT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS "areas" (
			"id" INTEGER PRIMARY KEY AUTOINCREMENT,
			"pid" bigint NOT NULL DEFAULT 0,
			"name" varchar(64) NOT NULL DEFAULT '',
			"level" smallint NOT NULL DEFAULT 0,
			"code" varchar(32) NOT NULL DEFAULT '',
			"zip" varchar(32) NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS "crud_logs" (
			"id" INTEGER PRIMARY KEY AUTOINCREMENT,
			"name" varchar(255) NOT NULL DEFAULT '',
			"comment" varchar(255) NOT NULL DEFAULT '',
			"table" text,
			"fields" text,
			"sql" text DEFAULT NULL,
			"status" varchar(64) NOT NULL DEFAULT '',
			"created_at" datetime
		)`,
		`CREATE TABLE IF NOT EXISTS "admin_plugins" (
			"id" INTEGER PRIMARY KEY AUTOINCREMENT,
			"name" varchar(32) NOT NULL DEFAULT '',
			"title" varchar(64) NOT NULL DEFAULT '',
			"version" varchar(32) NOT NULL DEFAULT '',
			"kind" varchar(16) NOT NULL DEFAULT 'code',
			"status" varchar(16) NOT NULL DEFAULT 'failed',
			"host_api" varchar(64) NOT NULL DEFAULT '',
			"host_version" varchar(32) NOT NULL DEFAULT '',
			"framework_req" varchar(64) NOT NULL DEFAULT '',
			"requires" text NOT NULL DEFAULT '',
			"adopted_through" integer NOT NULL DEFAULT 0,
			"checksum" varchar(64) NOT NULL DEFAULT '',
			"source" varchar(255) NOT NULL DEFAULT '',
			"signed_by" varchar(128) NOT NULL DEFAULT '',
			"installed_at" datetime,
			"updated_at" datetime,
			"remark" varchar(255) NOT NULL DEFAULT ''
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS "uk_admin_plugins_name" ON "admin_plugins" ("name")`,
		`CREATE TABLE IF NOT EXISTS "admin_plugin_entities" (
			"id" INTEGER PRIMARY KEY AUTOINCREMENT,
			"plugin" varchar(32) NOT NULL DEFAULT '',
			"name" varchar(64) NOT NULL DEFAULT '',
			"title_zh" varchar(64) NOT NULL DEFAULT '',
			"title_en" varchar(64) NOT NULL DEFAULT '',
			"table_name" varchar(64) NOT NULL DEFAULT '',
			"spec" text NOT NULL DEFAULT '{}',
			"created_at" datetime,
			"updated_at" datetime
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS "uk_admin_plugin_entities_plugin_name" ON "admin_plugin_entities" ("plugin", "name")`,
		`CREATE TABLE IF NOT EXISTS "admin_plugin_actions" (
			"id" INTEGER PRIMARY KEY AUTOINCREMENT,
			"plugin" varchar(32) NOT NULL DEFAULT '',
			"action" varchar(16) NOT NULL DEFAULT '',
			"operator" varchar(64) NOT NULL DEFAULT '',
			"status" varchar(16) NOT NULL DEFAULT '',
			"detail" text NOT NULL DEFAULT '{}',
			"created_at" datetime NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS "admin_plugin_sources" (
			"id" INTEGER PRIMARY KEY AUTOINCREMENT,
			"name" varchar(32) NOT NULL DEFAULT '',
			"title" varchar(64) NOT NULL DEFAULT '',
			"type" varchar(16) NOT NULL DEFAULT 'local',
			"location" text NOT NULL DEFAULT '',
			"enabled" smallint NOT NULL DEFAULT 1,
			"allow_insecure_http" smallint NOT NULL DEFAULT 0,
			"allow_private_hosts" smallint NOT NULL DEFAULT 0,
			"allow_absolute_paths" smallint NOT NULL DEFAULT 0,
			"weigh" bigint NOT NULL DEFAULT 0,
			"last_sync_status" varchar(16) NOT NULL DEFAULT '',
			"last_sync_error" text NOT NULL DEFAULT '',
			"last_catalog_sha256" varchar(64) NOT NULL DEFAULT '',
			"last_catalog_generated_at" datetime,
			"last_synced_at" datetime,
			"created_by" varchar(64) NOT NULL DEFAULT '',
			"updated_by" varchar(64) NOT NULL DEFAULT '',
			"remark" varchar(255) NOT NULL DEFAULT '',
			"created_at" datetime,
			"updated_at" datetime
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS "uk_admin_plugin_sources_name" ON "admin_plugin_sources" ("name")`,
		`CREATE TABLE IF NOT EXISTS "admin_plugin_catalog_entries" (
			"id" INTEGER PRIMARY KEY AUTOINCREMENT,
			"source_id" bigint NOT NULL,
			"source_name" varchar(32) NOT NULL DEFAULT '',
			"uid" varchar(32) NOT NULL DEFAULT '',
			"version" varchar(32) NOT NULL DEFAULT '',
			"version_major" integer NOT NULL DEFAULT 0,
			"version_minor" integer NOT NULL DEFAULT 0,
			"version_patch" integer NOT NULL DEFAULT 0,
			"title_zh" varchar(64) NOT NULL DEFAULT '',
			"title_en" varchar(64) NOT NULL DEFAULT '',
			"desc_zh" varchar(256) NOT NULL DEFAULT '',
			"desc_en" varchar(256) NOT NULL DEFAULT '',
			"kind" varchar(16) NOT NULL DEFAULT '',
			"host_api" varchar(64) NOT NULL DEFAULT '',
			"framework_req" varchar(64) NOT NULL DEFAULT '',
			"requires" text NOT NULL DEFAULT '',
			"license" varchar(32) NOT NULL DEFAULT '',
			"package_url" text NOT NULL DEFAULT '',
			"package_path" text NOT NULL DEFAULT '',
			"package_size" bigint NOT NULL DEFAULT 0,
			"package_sha256" varchar(64) NOT NULL DEFAULT '',
			"package_signed" smallint NOT NULL DEFAULT 0,
			"catalog_sha256" varchar(64) NOT NULL DEFAULT '',
			"synced_at" datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
			"created_at" datetime,
			"updated_at" datetime
		)`,
	}
	for _, stmt := range ddls {
		if err := db.Exec(stmt).Error; err != nil {
			return err
		}
	}

	seeds := []string{
		`INSERT OR IGNORE INTO "configs" ("id", "name", "group", "title", "tip", "type", "value", "dict", "rule", "input_extend", "allow_del", "weigh") VALUES
			(1, 'config_group', 'basic', '配置分组', NULL, 'array', '[{"key":"basic","value":"基础配置"},{"key":"mail","value":"邮件配置"},{"key":"ai","value":"AI 配置"},{"key":"quick_entrance","value":"快捷配置入口"}]', NULL, 'required', NULL, 0, -1),
				(2, 'name', 'basic', '站点名称', NULL, 'string', 'Rolling-Memory', NULL, 'required', NULL, 0, 99),
				(5, 'version', 'basic', '系统版本号', NULL, 'string', 'v0.0.1', NULL, 'required', NULL, 0, 96)`,
		`INSERT OR IGNORE INTO "admin_groups" ("id", "pid", "name", "rules", "status", "updated_at", "created_at") VALUES
			(1, NULL, '超级管理组', '*', 1, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
		`INSERT OR IGNORE INTO "admins" ("id", "username", "nickname", "avatar", "email", "mobile", "login_failure", "last_login_at", "last_login_ip", "password", "bio", "status", "updated_at", "created_at", "deleted_at") VALUES
			(1, 'admin', 'Admin', '/static/images/avatar.png', 'admin@rolling-memory.local', '18888888888', 0, CURRENT_TIMESTAMP, '127.0.0.1', '$2a$10$T7nw94qiMClADHg0KezIzukmNvvIxHG.V5uObKOGwPTVq4W2ee7B.', NULL, 'enable', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, NULL)`,
		`INSERT OR IGNORE INTO "admin_group_access" ("uid", "group_id") VALUES (1, 1)`,
		`INSERT OR IGNORE INTO "admin_rules" ("id", "pid", "type", "title", "name", "path", "icon", "open_type", "url", "component", "keepalive", "extend", "remark", "weigh", "status", "plugin", "updated_at", "created_at") VALUES
						(1, NULL, 'dir', '控制台', 'dashboard', 'dashboard/dir', 'lucide-chart-pie', '', '', '', 1, '', '', 999, 1, '', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
						(6, 1, 'menu', '实时控制台', 'dashboard/console', 'dashboard', 'lucide-gauge', 'tab', '', '/src/views/admin/dashboard.vue', 0, '', '', 99, 1, '', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
						(2, 6, 'node', '查看', 'dashboard/read', '', '', '', '', '', 1, '', '', 0, 1, '', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
						(11, 1, 'menu', 'ROI 监控屏', 'dashboard/roi', 'dashboard/roi', 'lucide-trending-up', 'tab', '', '/src/views/admin/dashboard/roi.vue', 0, '', '', 98, 1, '', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
						(12, 11, 'node', '查看 ROI 监控屏', 'dashboard/roi/read', '', '', '', '', '', 1, '', '', 0, 1, '', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
							(7, 1, 'menu', '记忆图谱', 'dashboard/graph', 'dashboard/graph', 'lucide-git-graph', 'tab', '', '/src/views/admin/dashboard/graph.vue', 0, '', '', 97, 1, '', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
							(8, 7, 'node', '查看记忆图谱', 'dashboard/graph/read', '', '', '', '', '', 1, '', '', 0, 1, '', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
							(16, 1, 'menu', '记忆控制台', 'dashboard/artifacts', 'dashboard/artifacts', 'lucide-cpu', 'tab', '', '/src/views/admin/dashboard/artifacts.vue', 0, '', '记忆模式切换、产物对账与引擎连接状态实时控制台', 96, 1, '', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
							(17, 16, 'node', '查看记忆控制台', 'dashboard/artifacts/read', '', '', '', '', '', 1, '', '', 0, 1, '', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
			(3, NULL, 'dir', '权限管理', 'auth', 'auth', 'lucide-users', '', '', '', 1, '', '', 970, 1, '', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
			(4, 3, 'menu', '角色组管理', 'auth/group', 'auth/group', 'lucide-users-round', 'tab', '', '/src/views/admin/auth/group/index.vue', 1, '', '', 99, 1, '', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
			(5, 4, 'node', '查看', 'auth/group/read', '', '', '', '', '', 1, '', '', 0, 1, '', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
			(9, 3, 'menu', '管理员管理', 'auth/admin', 'auth/admin', 'lucide-user-key', 'tab', '', '/src/views/admin/auth/admin/index.vue', 1, '', '', 98, 1, '', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
			(10, 9, 'node', '查看', 'auth/admin/read', '', '', '', '', '', 1, '', '', 0, 1, '', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
			(14, 3, 'menu', '菜单规则管理', 'auth/rule', 'auth/rule', 'lucide-list-tree', 'tab', '', '/src/views/admin/auth/rule/index.vue', 1, '', '', 97, 1, '', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
			(15, 14, 'node', '查看', 'auth/rule/read', '', '', '', '', '', 1, '', '', 0, 1, '', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
			(20, 3, 'menu', '管理员日志管理', 'auth/log', 'auth/log', 'lucide-scroll-text', 'tab', '', '/src/views/admin/auth/log/index.vue', 1, '', '', 96, 1, '', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
			(21, 20, 'node', '查看', 'auth/log/read', '', '', '', '', '', 1, '', '', 0, 1, '', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
			(22, NULL, 'dir', '常规管理', 'routine', 'routine', 'lucide-settings', '', '', '', 1, '', '', 969, 1, '', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
			(23, 22, 'menu', '系统配置', 'routine/config', 'routine/config', 'lucide-wrench', 'tab', '', '/src/views/admin/routine/config/index.vue', 1, '', '', 99, 1, '', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
			(24, 23, 'node', '查看', 'routine/config/read', '', '', '', '', '', 1, '', '', 0, 1, '', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
			(25, 23, 'node', '更新', 'routine/config/update', '', '', '', '', '', 1, '', '', 0, 1, '', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
			(30, 22, 'menu', '个人资料', 'routine/profile', 'routine/profile', 'lucide-user', 'tab', '', '/src/views/admin/routine/profile.vue', 1, '', '', 97, 1, '', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
			(31, 30, 'node', '查看', 'routine/profile/read', '', '', '', '', '', 1, '', '', 0, 1, '', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
			(32, 30, 'node', '更新', 'routine/profile/update', '', '', '', '', '', 1, '', '', 0, 1, '', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
			(40, NULL, 'menu', '插件管理', 'plugin', 'plugin', 'lucide-puzzle', 'tab', '', '/src/views/admin/plugin/index.vue', 1, '', '', 989, 1, '', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
			(41, 40, 'node', '查看', 'plugin/read', '', '', '', '', '', 1, '', '', 0, 1, '', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
			(42, 40, 'node', '启用', 'plugin/enable', '', '', '', '', '', 1, '', '', 0, 1, '', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
				(43, 40, 'node', '停用', 'plugin/disable', '', '', '', '', '', 1, '', '', 0, 1, '', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
				(44, 40, 'node', '卸载', 'plugin/uninstall', '', '', '', '', '', 1, '', '', 0, 1, '', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
				(100, NULL, 'dir', '记忆浏览', 'plugin/rolling-memory', 'plugin/rolling-memory', 'lucide-brain', '', '', '', 1, '', '', 995, 1, 'rolling-memory', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
				(101, 100, 'menu', '记忆总览', 'plugin/rolling-memory/index', 'plugin/rolling-memory/index', 'lucide-layout-dashboard', 'tab', '', '/src/views/admin/plugin/rolling-memory/index.vue', 1, '', '', 99, 1, 'rolling-memory', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
				(102, 101, 'node', '查看记忆总览', 'plugin/rolling-memory/index/read', '', '', '', '', '', 1, '', '', 0, 1, 'rolling-memory', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
				(103, 101, 'node', '实时状态数据', 'plugin/rolling-memory/status', '', '', '', '', '', 1, '', '', 0, 1, 'rolling-memory', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
				(104, 101, 'node', '切换调试开关', 'plugin/rolling-memory/toggle', '', '', '', '', '', 1, '', '', 0, 1, 'rolling-memory', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
				(114, 101, 'node', '查询与筛选记忆列表', 'plugin/rolling-memory/memories/query', '', '', '', '', '', 1, '', '', 0, 1, 'rolling-memory', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
				(115, 101, 'node', '查看单条记忆溯源详情', 'plugin/rolling-memory/memories/detail', '', '', '', '', '', 1, '', '', 0, 1, 'rolling-memory', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
				(116, 100, 'menu', '检索调试', 'plugin/rolling-memory/recall', 'plugin/rolling-memory/recall', 'lucide-search', 'tab', '', '/src/views/admin/plugin/rolling-memory/recall.vue', 1, '', '', 100, 1, 'rolling-memory', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
				(117, 116, 'node', '查看检索调试页面', 'plugin/rolling-memory/recall/read', '', '', '', '', '', 1, '', '', 0, 1, 'rolling-memory', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
				(118, 116, 'node', '执行检索调试查询', 'plugin/rolling-memory/recall/query', '', '', '', '', '', 1, '', '', 0, 1, 'rolling-memory', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
					(119, NULL, 'dir', '评测', 'plugin/rolling-memory/eval-dir', 'plugin/rolling-memory/eval-dir', 'lucide-flask-conical', '', '', '', 1, '', '', 994, 1, 'rolling-memory', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
					(105, 119, 'menu', '评测实验室', 'plugin/rolling-memory/eval', 'plugin/rolling-memory/eval', 'lucide-flask-conical', 'tab', '', '/src/views/admin/plugin/rolling-memory/eval.vue', 1, '', '', 99, 1, 'rolling-memory', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
					(106, 105, 'node', '查看评测频道', 'plugin/rolling-memory/eval/read', '', '', '', '', '', 1, '', '', 0, 1, 'rolling-memory', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
					(107, 105, 'node', '评测数据概览', 'plugin/rolling-memory/eval/overview', '', '', '', '', '', 1, '', '', 0, 1, 'rolling-memory', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
					(108, 105, 'node', '导入与清理语料', 'plugin/rolling-memory/eval/ingest', '', '', '', '', '', 1, '', '', 0, 1, 'rolling-memory', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
					(109, 105, 'node', '单题与批量评测', 'plugin/rolling-memory/eval/run', '', '', '', '', '', 1, '', '', 0, 1, 'rolling-memory', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
						(112, 105, 'node', '读取评测协议模型', 'plugin/rolling-memory/ai/config', '', '', '', '', '', 1, '', '', 0, 1, 'rolling-memory', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
						(113, 105, 'node', '保存评测协议模型', 'plugin/rolling-memory/ai/save', '', '', '', '', '', 1, '', '', 0, 1, 'rolling-memory', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
						(120, 119, 'menu', 'LoCoMo-Backboard 榜单', 'plugin/rolling-memory/leaderboard', 'plugin/rolling-memory/leaderboard', 'lucide-trophy', 'tab', '', '/src/views/admin/plugin/rolling-memory/leaderboard.vue', 1, '', '', 97, 1, 'rolling-memory', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
						(121, 120, 'node', '查看榜单页面', 'plugin/rolling-memory/leaderboard/read', '', '', '', '', '', 1, '', '', 0, 1, 'rolling-memory', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
						(122, 120, 'node', '读取榜单与协议同构数据', 'plugin/rolling-memory/eval/leaderboard', '', '', '', '', '', 1, '', '', 0, 1, 'rolling-memory', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
						(128, 119, 'menu', '剑桥 ATM-Bench 榜单', 'plugin/rolling-memory/atm-leaderboard', 'plugin/rolling-memory/atm-leaderboard', 'lucide-graduation-cap', 'tab', '', '/src/views/admin/plugin/rolling-memory/atmLeaderboard.vue', 1, '', '', 98, 1, 'rolling-memory', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
					(129, 128, 'node', '查看剑桥榜单页面', 'plugin/rolling-memory/atm-leaderboard/read', '', '', '', '', '', 1, '', '', 0, 1, 'rolling-memory', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
					(130, 128, 'node', '读取剑桥榜单与官方协议数据', 'plugin/rolling-memory/eval/atm/leaderboard', '', '', '', '', '', 1, '', '', 0, 1, 'rolling-memory', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
					(131, 100, 'menu', 'L1 文档与代码', 'plugin/rolling-memory/background', 'plugin/rolling-memory/background', 'lucide-folder-tree', 'tab', '', '/src/views/admin/plugin/rolling-memory/layerBackground.vue', 1, '', '', 98, 1, 'rolling-memory', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
					(132, 131, 'node', '查看背景记忆页面', 'plugin/rolling-memory/background/read', '', '', '', '', '', 1, '', '', 0, 1, 'rolling-memory', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
					(133, 131, 'node', '查询已挂载背景目录', 'plugin/rolling-memory/background/dirs', '', '', '', '', '', 1, '', '', 0, 1, 'rolling-memory', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
					(134, 131, 'node', '浏览挂载目录的文件与分块', 'plugin/rolling-memory/background/files', '', '', '', '', '', 1, '', '', 0, 1, 'rolling-memory', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
					(135, 131, 'node', '选择背景目录', 'plugin/rolling-memory/background/select', '', '', '', '', '', 1, '', '', 0, 1, 'rolling-memory', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
					(136, 131, 'node', '挂载背景目录', 'plugin/rolling-memory/background/add', '', '', '', '', '', 1, '', '', 0, 1, 'rolling-memory', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
					(137, 131, 'node', '移除背景目录', 'plugin/rolling-memory/background/remove', '', '', '', '', '', 1, '', '', 0, 1, 'rolling-memory', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
					(138, 131, 'node', '执行手动背景同步', 'plugin/rolling-memory/background/sync', '', '', '', '', '', 1, '', '', 0, 1, 'rolling-memory', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
					(139, 100, 'menu', 'L2 会话记录', 'plugin/rolling-memory/episodic', 'plugin/rolling-memory/episodic', 'lucide-scroll-text', 'tab', '', '/src/views/admin/plugin/rolling-memory/layerEpisodic.vue', 1, '', '', 97, 1, 'rolling-memory', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
					(140, 139, 'node', '查看情景记忆页面', 'plugin/rolling-memory/episodic/read', '', '', '', '', '', 1, '', '', 0, 1, 'rolling-memory', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
					(141, 100, 'menu', 'L3 检索沉淀', 'plugin/rolling-memory/noetic', 'plugin/rolling-memory/noetic', 'lucide-sparkles', 'tab', '', '/src/views/admin/plugin/rolling-memory/layerNoetic.vue', 1, '', '', 96, 1, 'rolling-memory', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
					(142, 141, 'node', '查看认知记忆页面', 'plugin/rolling-memory/noetic/read', '', '', '', '', '', 1, '', '', 0, 1, 'rolling-memory', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
					(143, 101, 'node', '统计三层认知记忆分布', 'plugin/rolling-memory/layers/stats', '', '', '', '', '', 1, '', '', 0, 1, 'rolling-memory', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
		`INSERT OR IGNORE INTO "admin_plugins" ("name", "title", "version", "kind", "status", "host_api", "host_version", "framework_req", "installed_at", "updated_at") VALUES
						('env-badge', '环境检测浮标', '1.0.0', 'code', 'enabled', '>=1 <2', '0.1.0', '>=0.1 <1', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
						('mdeditorv3', 'md-editor-v3富文本编辑器', '1.0.6', 'code', 'enabled', '>=1 <2', '0.1.0', '>=0.1 <2', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
						('rolling-brand', 'Rolling 品牌包', '1.0.0', 'code', 'enabled', '>=1 <2', '0.1.0', '>=0.1 <2', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
	}
	for _, stmt := range seeds {
		if err := db.Exec(strings.TrimSpace(stmt)).Error; err != nil {
			return err
		}
	}
	// 开箱账号一次性迁移：只改写仍是旧 seed 行（username = 'admin'）的 id = 1。
	// 已是 rolling 的库不再重置口令——「个人资料」改的密码必须能持久；忘记口令恢复走显式 CLI。
	if hashed, err := bcrypt.GenerateFromPassword([]byte("rolling"), bcrypt.DefaultCost); err == nil {
		_ = db.Exec(
			`UPDATE "admins" SET "username" = 'rolling', "nickname" = 'Rolling', "password" = ?, "login_failure" = 0, "status" = 'enable' WHERE "id" = 1 AND "username" = 'admin'`,
			string(hashed),
		).Error
	}
	var hasUserAgent int64
	_ = db.Raw(`SELECT COUNT(*) FROM pragma_table_info('admin_logs') WHERE name = 'user_agent'`).Scan(&hasUserAgent).Error
	if hasUserAgent == 0 {
		_ = db.Exec(`ALTER TABLE "admin_logs" ADD COLUMN "user_agent" varchar(255) DEFAULT ''`).Error
	}
	_ = db.Exec(`INSERT OR IGNORE INTO "admin_rules" ("id", "pid", "type", "title", "name", "path", "icon", "open_type", "url", "component", "keepalive", "extend", "remark", "weigh", "status", "plugin", "updated_at", "created_at") VALUES
		(123, 101, 'node', '实时状态WS流', 'plugin/rolling-memory/ws', '', '', '', '', '', 1, '', '', 0, 1, 'rolling-memory', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
		(124, 101, 'node', '读取ROI统计事实', 'plugin/rolling-memory/roi/facts', '', '', '', '', '', 1, '', '', 0, 1, 'rolling-memory', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
		(125, 101, 'node', '云端账号连接与套餐（申请制预览）', 'plugin/rolling-memory/cloud/account', '', '', '', '', '', 1, '', '', 0, 1, 'rolling-memory', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
		(127, 105, 'node', 'Open Memory Arena横评', 'plugin/rolling-memory/eval/arena', '', '', '', '', '', 1, '', '', 0, 1, 'rolling-memory', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`).Error
	_ = db.Exec(`UPDATE "configs" SET "value" = 'Rolling-Memory' WHERE "name" = 'name'`).Error
	_ = db.Exec(`UPDATE "configs" SET "value" = 'v0.0.1' WHERE "name" = 'version'`).Error
	_ = db.Exec(`DELETE FROM "admin_plugins" WHERE "name" = 'rolling-memory'`).Error
	_ = db.Exec(`UPDATE "admin_rules" SET "plugin" = '' WHERE "plugin" = 'rolling-memory'`).Error
	_ = db.Exec(`UPDATE "admin_rules" SET "title" = '记忆浏览' WHERE "id" = 100 AND "name" = 'plugin/rolling-memory' AND "type" = 'dir' AND "title" = '记忆管理'`).Error
	// 三层平行子页面与共享统计节点：既存库幂等补齐，不改动已有权限层级。
	_ = db.Exec(`INSERT OR IGNORE INTO "admin_rules" ("id", "pid", "type", "title", "name", "path", "icon", "open_type", "url", "component", "keepalive", "extend", "remark", "weigh", "status", "plugin", "updated_at", "created_at") VALUES
		(131, 100, 'menu', 'L1 文档与代码', 'plugin/rolling-memory/background', 'plugin/rolling-memory/background', 'lucide-folder-tree', 'tab', '', '/src/views/admin/plugin/rolling-memory/layerBackground.vue', 1, '', '', 98, 1, 'rolling-memory', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
		(132, 131, 'node', '查看背景记忆页面', 'plugin/rolling-memory/background/read', '', '', '', '', '', 1, '', '', 0, 1, 'rolling-memory', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
		(133, 131, 'node', '查询已挂载背景目录', 'plugin/rolling-memory/background/dirs', '', '', '', '', '', 1, '', '', 0, 1, 'rolling-memory', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
		(134, 131, 'node', '浏览挂载目录的文件与分块', 'plugin/rolling-memory/background/files', '', '', '', '', '', 1, '', '', 0, 1, 'rolling-memory', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
		(135, 131, 'node', '选择背景目录', 'plugin/rolling-memory/background/select', '', '', '', '', '', 1, '', '', 0, 1, 'rolling-memory', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
		(136, 131, 'node', '挂载背景目录', 'plugin/rolling-memory/background/add', '', '', '', '', '', 1, '', '', 0, 1, 'rolling-memory', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
		(137, 131, 'node', '移除背景目录', 'plugin/rolling-memory/background/remove', '', '', '', '', '', 1, '', '', 0, 1, 'rolling-memory', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
		(138, 131, 'node', '执行手动背景同步', 'plugin/rolling-memory/background/sync', '', '', '', '', '', 1, '', '', 0, 1, 'rolling-memory', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
		(139, 100, 'menu', 'L2 会话记录', 'plugin/rolling-memory/episodic', 'plugin/rolling-memory/episodic', 'lucide-scroll-text', 'tab', '', '/src/views/admin/plugin/rolling-memory/layerEpisodic.vue', 1, '', '', 97, 1, 'rolling-memory', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
		(140, 139, 'node', '查看情景记忆页面', 'plugin/rolling-memory/episodic/read', '', '', '', '', '', 1, '', '', 0, 1, 'rolling-memory', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
		(141, 100, 'menu', 'L3 检索沉淀', 'plugin/rolling-memory/noetic', 'plugin/rolling-memory/noetic', 'lucide-sparkles', 'tab', '', '/src/views/admin/plugin/rolling-memory/layerNoetic.vue', 1, '', '', 96, 1, 'rolling-memory', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
		(142, 141, 'node', '查看认知记忆页面', 'plugin/rolling-memory/noetic/read', '', '', '', '', '', 1, '', '', 0, 1, 'rolling-memory', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
		(143, 101, 'node', '统计三层认知记忆分布', 'plugin/rolling-memory/layers/stats', '', '', '', '', '', 1, '', '', 0, 1, 'rolling-memory', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
		(144, 101, 'node', '读取顶部记忆小组件状态', 'plugin/rolling-memory/widget', '', '', '', '', '', 1, '', '', 0, 1, 'rolling-memory', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
		(145, 6, 'node', '读取实时控制台快照', 'plugin/rolling-memory/dashboard', '', '', '', '', '', 1, '', '', 0, 1, 'rolling-memory', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
		(146, 6, 'node', '实时控制台SSE流', 'plugin/rolling-memory/dashboard/stream', '', '', '', '', '', 1, '', '', 0, 1, 'rolling-memory', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
		(147, 6, 'node', '读取记忆效能统计', 'plugin/rolling-memory/efficacy', '', '', '', '', '', 1, '', '', 0, 1, 'rolling-memory', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
		(148, 7, 'node', '读取记忆图谱拓扑', 'plugin/rolling-memory/graph', '', '', '', '', '', 1, '', '', 0, 1, 'rolling-memory', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
		(149, 7, 'node', '展开记忆图谱邻域', 'plugin/rolling-memory/graph/expand', '', '', '', '', '', 1, '', '', 0, 1, 'rolling-memory', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
		(150, 101, 'node', '读取内置终端输出', 'plugin/rolling-memory/terminal', '', '', '', '', '', 1, '', '', 0, 1, 'rolling-memory', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
		(151, 101, 'node', '查看三层记忆单元列表与详情', 'plugin/rolling-memory/layers/read', '', '', '', '', '', 1, '', '', 0, 1, 'rolling-memory', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
		(152, 101, 'node', '删除或清理三层记忆单元', 'plugin/rolling-memory/layers/delete', '', '', '', '', '', 1, '', '', 0, 1, 'rolling-memory', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
		(153, 101, 'node', '读取Harness与金库健康状态', 'plugin/rolling-memory/env/health', '', '', '', '', '', 1, '', '', 0, 1, 'rolling-memory', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
		(154, 101, 'node', '执行三层记忆手动对账同步', 'plugin/rolling-memory/sync/reconcile', '', '', '', '', '', 1, '', '', 0, 1, 'rolling-memory', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
		(155, 101, 'node', '读取云端账号绑定状态', 'plugin/rolling-memory/cloud/account/state', '', '', '', '', '', 1, '', '', 0, 1, 'rolling-memory', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
		(156, 101, 'node', '申请云端账号（申请制预览）', 'plugin/rolling-memory/cloud/account/register', '', '', '', '', '', 1, '', '', 0, 1, 'rolling-memory', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
		(157, 101, 'node', '登录云端账号', 'plugin/rolling-memory/cloud/account/login', '', '', '', '', '', 1, '', '', 0, 1, 'rolling-memory', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
		(158, 101, 'node', '退出云端账号', 'plugin/rolling-memory/cloud/account/logout', '', '', '', '', '', 1, '', '', 0, 1, 'rolling-memory', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
		(159, 101, 'node', '轮换云端账号密钥', 'plugin/rolling-memory/cloud/account/rotate-key', '', '', '', '', '', 1, '', '', 0, 1, 'rolling-memory', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
		(160, 101, 'node', '签发数据面直连签名钥', 'plugin/rolling-memory/cloud/account/sign-key', '', '', '', '', '', 1, '', '', 0, 1, 'rolling-memory', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
		(161, 101, 'node', '管理云端设备密钥列表', 'plugin/rolling-memory/cloud/account/devices', '', '', '', '', '', 1, '', '', 0, 1, 'rolling-memory', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
		(162, 101, 'node', '云端记忆在线检索与写入', 'plugin/rolling-memory/cloud/account/memory', '', '', '', '', '', 1, '', '', 0, 1, 'rolling-memory', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`).Error
	_ = db.Exec(`INSERT OR IGNORE INTO "admin_rules" ("id", "pid", "type", "title", "name", "path", "icon", "open_type", "url", "component", "keepalive", "extend", "remark", "weigh", "status", "plugin", "updated_at", "created_at") VALUES
			(119, NULL, 'dir', '评测', 'plugin/rolling-memory/eval-dir', 'plugin/rolling-memory/eval-dir', 'lucide-flask-conical', '', '', '', 1, '', '', 994, 1, 'rolling-memory', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
			(120, 119, 'menu', 'LoCoMo-Backboard 榜单', 'plugin/rolling-memory/leaderboard', 'plugin/rolling-memory/leaderboard', 'lucide-trophy', 'tab', '', '/src/views/admin/plugin/rolling-memory/leaderboard.vue', 1, '', '', 97, 1, 'rolling-memory', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
			(121, 120, 'node', '查看榜单页面', 'plugin/rolling-memory/leaderboard/read', '', '', '', '', '', 1, '', '', 0, 1, 'rolling-memory', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
			(122, 120, 'node', '读取榜单与协议同构数据', 'plugin/rolling-memory/eval/leaderboard', '', '', '', '', '', 1, '', '', 0, 1, 'rolling-memory', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
			(128, 119, 'menu', '剑桥 ATM-Bench 榜单', 'plugin/rolling-memory/atm-leaderboard', 'plugin/rolling-memory/atm-leaderboard', 'lucide-graduation-cap', 'tab', '', '/src/views/admin/plugin/rolling-memory/atmLeaderboard.vue', 1, '', '', 98, 1, 'rolling-memory', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
			(129, 128, 'node', '查看剑桥榜单页面', 'plugin/rolling-memory/atm-leaderboard/read', '', '', '', '', '', 1, '', '', 0, 1, 'rolling-memory', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
			(130, 128, 'node', '读取剑桥榜单与官方协议数据', 'plugin/rolling-memory/eval/atm/leaderboard', '', '', '', '', '', 1, '', '', 0, 1, 'rolling-memory', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`).Error
	// 评测目录内「剑桥 ATM-Bench 榜单」作为第一主基准排在「LoCoMo-Backboard 榜单」之前（仅当两者仍为旧出厂权重 97/98 时幂等对调，保留用户自定义权重）。
	_ = db.Exec(`UPDATE "admin_rules" SET "weigh" = CASE "name" WHEN 'plugin/rolling-memory/atm-leaderboard' THEN 98 WHEN 'plugin/rolling-memory/leaderboard' THEN 97 ELSE "weigh" END WHERE "name" IN ('plugin/rolling-memory/atm-leaderboard', 'plugin/rolling-memory/leaderboard') AND (SELECT "weigh" FROM "admin_rules" WHERE "name" = 'plugin/rolling-memory/atm-leaderboard') = 97 AND (SELECT "weigh" FROM "admin_rules" WHERE "name" = 'plugin/rolling-memory/leaderboard') = 98`).Error
	_ = db.Exec(`UPDATE "admin_rules" SET "plugin" = '' WHERE "plugin" = 'rolling-memory'`).Error
	_ = db.Exec(`INSERT OR IGNORE INTO "admin_rules" ("id", "pid", "type", "title", "name", "path", "icon", "open_type", "url", "component", "keepalive", "extend", "remark", "weigh", "status", "plugin", "updated_at", "created_at") VALUES
				(200, NULL, 'dir', '插件', 'plugin-center', 'plugin-center', 'lucide-puzzle', '', '', '', 1, '', '插件频道：插件管理、仓库来源与插件清单', 968, 1, '', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
				(201, 200, 'menu', '仓库来源', 'plugin/source', 'plugin/source', 'lucide-database', 'tab', '', '/src/views/admin/plugin/source/index.vue', 1, '', '插件清单来源配置（local/http/git）', 90, 1, '', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
				(202, 201, 'node', '查看', 'plugin/source/read', '', '', '', '', '', 1, '', '', 0, 1, '', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
				(203, 201, 'node', '新增', 'plugin/source/create', '', '', '', '', '', 1, '', '', 0, 1, '', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
				(204, 201, 'node', '更新', 'plugin/source/update', '', '', '', '', '', 1, '', '', 0, 1, '', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
				(205, 201, 'node', '删除', 'plugin/source/delete', '', '', '', '', '', 1, '', '', 0, 1, '', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
				(206, 201, 'node', '测试', 'plugin/source/test', '', '', '', '', '', 1, '', '', 0, 1, '', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
				(207, 200, 'menu', '插件清单', 'plugin/catalog', 'plugin/catalog', 'lucide-package', 'tab', '', '/src/views/admin/plugin/catalog/index.vue', 1, '', '清单浏览、来源冲突提示、预演与安装', 80, 1, '', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
				(208, 207, 'node', '查看', 'plugin/catalog/read', '', '', '', '', '', 1, '', '', 0, 1, '', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
				(209, 207, 'node', '同步清单', 'plugin/catalog/sync', '', '', '', '', '', 1, '', '', 0, 1, '', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
				(210, 207, 'node', '安装', 'plugin/catalog/install', '', '', '', '', '', 1, '', '', 0, 1, '', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
				(211, 207, 'node', '预演', 'plugin/catalog/preflight', '', '', '', '', '', 1, '', '', 0, 1, '', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
					(220, NULL, 'dir', '帮助中心', 'help', 'help', 'lucide-book-open', '', '', '', 1, '', '独立通用开源文档频道（位于插件频道下方）', 980, 1, '', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
					(221, 220, 'menu', '帮助与文档', 'help/docs', 'help/docs', 'lucide-book-open-check', 'tab', '', '/src/views/admin/help/index.vue', 1, '', '开源文档模块（docs/，随代码同步；指向 GitHub rocareer/rolling-memory）', 99, 1, '', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
					(222, 221, 'node', '查看帮助与文档', 'help/docs/read', '', '', '', '', '', 1, '', '', 0, 1, '', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
					(223, 220, 'menu', '开源仓库与声明', 'help/repos', 'help/repos', 'lucide-git-branch', 'tab', '', '/src/views/admin/help/repos.vue', 1, '', '开源仓库 rocareer/rolling-memory 立场声明、许可与文档索引台', 98, 1, '', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
					(224, 223, 'node', '查看开源仓库与声明', 'help/repos/read', '', '', '', '', '', 1, '', '', 0, 1, '', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`).Error
	_ = db.Exec(`UPDATE "admin_rules" SET "pid" = 200, "weigh" = 100 WHERE "id" = 40 AND "name" = 'plugin' AND "type" = 'menu' AND "pid" IS NULL AND "weigh" = 989`).Error
	_ = db.Exec(`INSERT OR IGNORE INTO "admin_rules" ("id", "pid", "type", "title", "name", "path", "icon", "open_type", "url", "component", "keepalive", "extend", "remark", "weigh", "status", "plugin", "updated_at", "created_at") VALUES
		(11, 1, 'menu', 'ROI 监控屏', 'dashboard/roi', 'dashboard/roi', 'lucide-trending-up', 'tab', '', '/src/views/admin/dashboard/roi.vue', 0, '', '记忆 ROI 与认知效能监控台：L3 认知结晶、跨层引证复用倍数、tokenz-r 干句提纯与多工作区冷热诊断', 98, 1, '', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
		(12, 11, 'node', '查看 ROI 监控屏', 'dashboard/roi/read', '', '', '', '', '', 1, '', '', 0, 1, '', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
		(16, 1, 'menu', '记忆控制台', 'dashboard/artifacts', 'dashboard/artifacts', 'lucide-cpu', 'tab', '', '/src/views/admin/dashboard/artifacts.vue', 0, '', '记忆模式切换、产物对账与引擎连接状态实时控制台', 96, 1, '', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
		(17, 16, 'node', '查看记忆控制台', 'dashboard/artifacts/read', '', '', '', '', '', 1, '', '', 0, 1, '', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`).Error
	// 面向开发者的导航层级：日常面在上（控制台 → 记忆 → 评测 → 帮助），
	// 权限 / 常规 / 插件等管理面沉底；记忆目录内「检索调试」置顶（排障第一入口）。
	// 只迁移仍是出厂值的行（weigh / title 未被改过），用户手工调整过的菜单保持不动。
	_ = db.Exec(`INSERT OR IGNORE INTO "admin_rules" ("id", "pid", "type", "title", "name", "path", "icon", "open_type", "url", "component", "keepalive", "extend", "remark", "weigh", "status", "plugin", "updated_at", "created_at") VALUES
			(163, 116, 'node', '读取最近 Agent 检索线索', 'plugin/rolling-memory/recall/recent', '', '', '', '', '', 1, '', '', 0, 1, '', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
			(164, 101, 'node', '读取系统版本与本地组件指纹', 'plugin/rolling-memory/system/version', '', '', '', '', '', 1, '', '', 0, 1, '', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
			(165, 101, 'node', '在线检查新版本与 SHA-256 清单', 'plugin/rolling-memory/system/update/check', '', '', '', '', '', 1, '', '', 0, 1, '', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
			(166, 101, 'node', '执行在线安全升级（SHA-256 校验）', 'plugin/rolling-memory/system/update/apply', '', '', '', '', '', 1, '', '', 0, 1, '', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`).Error
	_ = db.Exec(`UPDATE "admin_rules" SET "weigh" = 970 WHERE "name" = 'auth' AND "type" = 'dir' AND "weigh" = 998`).Error
	_ = db.Exec(`UPDATE "admin_rules" SET "weigh" = 969 WHERE "name" = 'routine' AND "type" = 'dir' AND "weigh" = 997`).Error
	_ = db.Exec(`UPDATE "admin_rules" SET "weigh" = 968 WHERE "name" = 'plugin-center' AND "type" = 'dir' AND "weigh" = 989`).Error
	_ = db.Exec(`UPDATE "admin_rules" SET "weigh" = 100, "title" = '检索调试' WHERE "name" = 'plugin/rolling-memory/recall' AND "weigh" = 95 AND "title" = '检索召回'`).Error
	_ = db.Exec(`UPDATE "admin_rules" SET "title" = '执行检索调试查询' WHERE "name" = 'plugin/rolling-memory/recall/query' AND "title" = '执行检索召回查询'`).Error
	_ = db.Exec(`UPDATE "admin_rules" SET "title" = '查看检索调试页面' WHERE "name" = 'plugin/rolling-memory/recall/read' AND "title" = '查看检索召回页面'`).Error
	_ = db.Exec(`UPDATE "admin_rules" SET "title" = 'L1 文档与代码' WHERE "name" = 'plugin/rolling-memory/background' AND "title" = 'L1 背景记忆'`).Error
	_ = db.Exec(`UPDATE "admin_rules" SET "title" = 'L2 会话记录' WHERE "name" = 'plugin/rolling-memory/episodic' AND "title" = 'L2 情景记忆'`).Error
	_ = db.Exec(`UPDATE "admin_rules" SET "title" = 'L3 检索沉淀' WHERE "name" = 'plugin/rolling-memory/noetic' AND "title" = 'L3 认知记忆'`).Error
	_ = db.Exec(`UPDATE "admin_rules" SET "remark" = '记忆模式切换、产物对账与引擎连接状态实时控制台' WHERE "name" = 'dashboard/artifacts' AND "remark" = '独立记忆模式切换、产物对账与向量构建实时控制台'`).Error
	if err := bootstrapAIReportTables(db); err != nil {
		return err
	}
	if err := bootstrapRollingMemoryEvalTables(db); err != nil {
		return err
	}
	return nil
}

// bootstrapAIReportTables 幂等初始化 AI 路由插件（plugins/ai）自管的 SQLite 业务表与菜单/台账。
//
// 与 PG 版插件迁移（plugins/ai/migrations/*.sql）逐列同构，按 SQLite 方言改写：
// 序号列 → INTEGER PRIMARY KEY AUTOINCREMENT；varchar/numeric → TEXT（金额保留字符串以避免 float 漂移）；
// jsonb → TEXT；timestamptz → DATETIME；FILTER/部分唯一索引为 SQLite 3.30+ 原生支持。
// 表名固定用 plugin_ai_ 前缀（本机 admin 的 configs.database.prefix 为空）。
func bootstrapAIReportTables(db *gorm.DB) error {
	ddls := []string{
		`CREATE TABLE IF NOT EXISTS "plugin_ai_channel" (
			"id" INTEGER PRIMARY KEY AUTOINCREMENT,
			"name" varchar(50) NOT NULL DEFAULT '',
			"vendor" varchar(30) NOT NULL DEFAULT '',
			"driver" varchar(255) NOT NULL DEFAULT '',
			"supplier_id" integer NOT NULL DEFAULT 0,
			"rpm_limit" integer NOT NULL DEFAULT 0,
			"tpm_limit" integer NOT NULL DEFAULT 0,
			"base_url" varchar(255) NOT NULL DEFAULT '',
			"api_key" varchar(255) NOT NULL DEFAULT '',
			"model" varchar(100) NOT NULL DEFAULT '',
			"timeout" integer NOT NULL DEFAULT 60,
			"priority" integer NOT NULL DEFAULT 1,
			"weight" integer NOT NULL DEFAULT 1,
			"status" smallint NOT NULL DEFAULT 1,
			"fail_threshold" integer NOT NULL DEFAULT 0,
			"cooldown_seconds" integer NOT NULL DEFAULT 0,
			"circuit" varchar(16) NOT NULL DEFAULT 'closed',
			"consecutive_failures" integer NOT NULL DEFAULT 0,
			"opened_at" integer,
			"total_calls" integer NOT NULL DEFAULT 0,
			"total_failures" integer NOT NULL DEFAULT 0,
			"total_tokens" integer NOT NULL DEFAULT 0,
			"last_success_at" integer,
			"last_error" varchar(500) NOT NULL DEFAULT '',
			"last_probe_at" integer,
			"last_probe_ok" smallint,
			"last_probe_latency" integer,
			"last_probe_error" varchar(500) NOT NULL DEFAULT '',
			"probe_fail_streak" integer NOT NULL DEFAULT 0,
			"remark" varchar(255) NOT NULL DEFAULT '',
			"create_time" integer NOT NULL DEFAULT 0,
			"update_time" integer NOT NULL DEFAULT 0,
			CONSTRAINT "ck_plugin_ai_channel_status" CHECK ("status" IN (0, 1)),
			CONSTRAINT "ck_plugin_ai_channel_circuit" CHECK ("circuit" IN ('closed', 'open', 'half_open')),
			CONSTRAINT "ck_plugin_ai_channel_probe_ok" CHECK ("last_probe_ok" IS NULL OR "last_probe_ok" IN (0, 1))
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS "uk_plugin_ai_channel_name" ON "plugin_ai_channel" ("name")`,
		`CREATE INDEX IF NOT EXISTS "idx_plugin_ai_channel_status" ON "plugin_ai_channel" ("status")`,
		`CREATE INDEX IF NOT EXISTS "idx_plugin_ai_channel_circuit" ON "plugin_ai_channel" ("circuit")`,
		`CREATE INDEX IF NOT EXISTS "idx_plugin_ai_channel_supplier" ON "plugin_ai_channel" ("supplier_id")`,
		`CREATE TABLE IF NOT EXISTS "plugin_ai_model" (
			"id" INTEGER PRIMARY KEY AUTOINCREMENT,
			"name" varchar(100) NOT NULL DEFAULT '',
			"type" varchar(16) NOT NULL DEFAULT 'chat',
			"strategy" varchar(16) NOT NULL DEFAULT 'priority',
			"description" varchar(255) NOT NULL DEFAULT '',
			"status" smallint NOT NULL DEFAULT 1,
			"price_input" numeric(12,6) NOT NULL DEFAULT 0,
			"price_output" numeric(12,6) NOT NULL DEFAULT 0,
			"encoding" varchar(20) NOT NULL DEFAULT '',
			"context_window" integer NOT NULL DEFAULT 0,
			"capabilities" jsonb,
			"create_time" integer NOT NULL DEFAULT 0,
			"update_time" integer NOT NULL DEFAULT 0,
			CONSTRAINT "ck_plugin_ai_model_type" CHECK ("type" IN ('chat', 'embedding', 'tts')),
			CONSTRAINT "ck_plugin_ai_model_strategy" CHECK ("strategy" IN ('priority', 'weighted')),
			CONSTRAINT "ck_plugin_ai_model_status" CHECK ("status" IN (0, 1))
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS "uk_plugin_ai_model_name" ON "plugin_ai_model" ("name")`,
		`CREATE INDEX IF NOT EXISTS "idx_plugin_ai_model_type" ON "plugin_ai_model" ("type")`,
		`CREATE INDEX IF NOT EXISTS "idx_plugin_ai_model_status" ON "plugin_ai_model" ("status")`,
		`CREATE TABLE IF NOT EXISTS "plugin_ai_model_channel" (
			"id" INTEGER PRIMARY KEY AUTOINCREMENT,
			"model_id" integer NOT NULL,
			"channel_id" integer NOT NULL,
			"priority" integer NOT NULL DEFAULT 1,
			"weight" integer NOT NULL DEFAULT 1,
			"status" smallint NOT NULL DEFAULT 1,
			"create_time" integer NOT NULL DEFAULT 0,
			"update_time" integer NOT NULL DEFAULT 0,
			CONSTRAINT "ck_plugin_ai_mc_status" CHECK ("status" IN (0, 1))
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS "uk_plugin_ai_model_channel" ON "plugin_ai_model_channel" ("model_id", "channel_id")`,
		`CREATE INDEX IF NOT EXISTS "idx_plugin_ai_mc_model" ON "plugin_ai_model_channel" ("model_id", "status", "priority")`,
		`CREATE INDEX IF NOT EXISTS "idx_plugin_ai_mc_channel" ON "plugin_ai_model_channel" ("channel_id")`,
		`CREATE TABLE IF NOT EXISTS "plugin_ai_usage" (
			"id" INTEGER PRIMARY KEY AUTOINCREMENT,
			"request_id" varchar(64) NOT NULL DEFAULT '',
			"attempt" integer NOT NULL DEFAULT 0,
			"model_id" integer NOT NULL DEFAULT 0,
			"model_name" varchar(100) NOT NULL DEFAULT '',
			"channel_id" integer NOT NULL DEFAULT 0,
			"channel_name" varchar(50) NOT NULL DEFAULT '',
			"call_type" varchar(16) NOT NULL DEFAULT 'chat',
			"biz_type" varchar(30) NOT NULL DEFAULT '',
			"source" varchar(30) NOT NULL DEFAULT '',
			"stream" smallint NOT NULL DEFAULT 0,
			"prompt_tokens" integer NOT NULL DEFAULT 0,
			"completion_tokens" integer NOT NULL DEFAULT 0,
			"total_tokens" integer NOT NULL DEFAULT 0,
			"cached_tokens" integer NOT NULL DEFAULT 0,
			"tokens_source" varchar(16) NOT NULL DEFAULT '',
			"cost" numeric(12,6) NOT NULL DEFAULT 0,
			"currency" varchar(10) NOT NULL DEFAULT 'USD',
			"latency_ms" integer NOT NULL DEFAULT 0,
			"status" varchar(16) NOT NULL DEFAULT 'success',
			"error_code" varchar(100) NOT NULL DEFAULT '',
			"error_message" varchar(500) NOT NULL DEFAULT '',
			"error_class" varchar(20) NOT NULL DEFAULT '',
			"billed_partial" smallint NOT NULL DEFAULT 0,
			"create_time" integer NOT NULL DEFAULT 0,
			"update_time" integer NOT NULL DEFAULT 0,
			CONSTRAINT "ck_plugin_ai_usage_status" CHECK ("status" IN ('success', 'fail', 'timeout', 'breaker')),
			CONSTRAINT "ck_plugin_ai_usage_call_type" CHECK ("call_type" IN ('chat', 'embedding', 'tts'))
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS "uk_plugin_ai_usage_req_attempt" ON "plugin_ai_usage" ("request_id", "attempt")`,
		`CREATE INDEX IF NOT EXISTS "idx_plugin_ai_usage_status_time" ON "plugin_ai_usage" ("status", "create_time")`,
		`CREATE INDEX IF NOT EXISTS "idx_plugin_ai_usage_time" ON "plugin_ai_usage" ("create_time")`,
		`CREATE INDEX IF NOT EXISTS "idx_plugin_ai_usage_model_time" ON "plugin_ai_usage" ("model_id", "create_time")`,
		`CREATE TABLE IF NOT EXISTS "plugin_ai_breaker_event" (
			"id" INTEGER PRIMARY KEY AUTOINCREMENT,
			"channel_id" integer NOT NULL,
			"channel_name" varchar(50) NOT NULL DEFAULT '',
			"event" varchar(16) NOT NULL DEFAULT 'open',
			"consecutive_failures" integer NOT NULL DEFAULT 0,
			"fail_threshold" integer NOT NULL DEFAULT 0,
			"cooldown_seconds" integer NOT NULL DEFAULT 0,
			"last_error" varchar(500) NOT NULL DEFAULT '',
			"create_time" integer NOT NULL DEFAULT 0,
			"update_time" integer NOT NULL DEFAULT 0,
			CONSTRAINT "ck_plugin_ai_ev_event" CHECK ("event" IN ('open', 'close', 'manual_open', 'manual_close'))
		)`,
		`CREATE INDEX IF NOT EXISTS "idx_plugin_ai_ev_channel_time" ON "plugin_ai_breaker_event" ("channel_id", "create_time")`,
		`CREATE TABLE IF NOT EXISTS "plugin_ai_supplier" (
			"id" INTEGER PRIMARY KEY AUTOINCREMENT,
			"name" varchar(50) NOT NULL DEFAULT '',
			"vendor" varchar(30) NOT NULL DEFAULT '',
			"base_url" varchar(255) NOT NULL DEFAULT '',
			"api_key" varchar(255) NOT NULL DEFAULT '',
			"driver" varchar(255) NOT NULL DEFAULT '',
			"timeout" integer NOT NULL DEFAULT 60,
			"status" smallint NOT NULL DEFAULT 1,
			"remark" varchar(255) NOT NULL DEFAULT '',
			"create_time" integer NOT NULL DEFAULT 0,
			"update_time" integer NOT NULL DEFAULT 0,
			CONSTRAINT "ck_plugin_ai_supplier_status" CHECK ("status" IN (0, 1))
		)`,
		`CREATE INDEX IF NOT EXISTS "idx_plugin_ai_supplier_status" ON "plugin_ai_supplier" ("status")`,
		`CREATE TABLE IF NOT EXISTS "plugin_ai_api_key" (
			"id" INTEGER PRIMARY KEY AUTOINCREMENT,
			"name" varchar(50) NOT NULL DEFAULT '',
			"key_hash" varchar(64) NOT NULL,
			"key_prefix" varchar(24) NOT NULL DEFAULT '',
			"source" varchar(24) NOT NULL DEFAULT '',
			"model_scope" jsonb NOT NULL DEFAULT '[]',
			"rate_limit" integer NOT NULL DEFAULT 0,
			"status" smallint NOT NULL DEFAULT 1,
			"expire_at" integer,
			"last_used_at" integer,
			"remark" varchar(255) NOT NULL DEFAULT '',
			"create_time" integer NOT NULL DEFAULT 0,
			"update_time" integer NOT NULL DEFAULT 0,
			CONSTRAINT "ck_plugin_ai_api_key_status" CHECK ("status" IN (0, 1))
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS "uk_plugin_ai_api_key_key_hash" ON "plugin_ai_api_key" ("key_hash")`,
		`CREATE UNIQUE INDEX IF NOT EXISTS "uk_plugin_ai_api_key_source" ON "plugin_ai_api_key" ("source") WHERE "source" <> ''`,
		`CREATE INDEX IF NOT EXISTS "idx_plugin_ai_api_key_status" ON "plugin_ai_api_key" ("status")`,
		`CREATE TABLE IF NOT EXISTS "plugin_ai_task" (
			"id" INTEGER PRIMARY KEY AUTOINCREMENT,
			"task_type" varchar(16) NOT NULL DEFAULT 'chat',
			"status" varchar(16) NOT NULL DEFAULT 'pending',
			"payload" jsonb,
			"result" jsonb,
			"error" varchar(500) NOT NULL DEFAULT '',
			"source" varchar(30) NOT NULL DEFAULT '',
			"biz_type" varchar(30) NOT NULL DEFAULT '',
			"request_id" varchar(64) NOT NULL DEFAULT '',
			"attempt" integer NOT NULL DEFAULT 0,
			"progress_done" integer NOT NULL DEFAULT 0,
			"progress_total" integer NOT NULL DEFAULT 0,
			"locked_until" DATETIME,
			"create_time" integer NOT NULL DEFAULT 0,
			"update_time" integer NOT NULL DEFAULT 0,
			CONSTRAINT "ck_plugin_ai_task_type" CHECK ("task_type" IN ('chat', 'embedding')),
			CONSTRAINT "ck_plugin_ai_task_status" CHECK ("status" IN ('pending', 'processing', 'success', 'failed'))
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS "uk_plugin_ai_task_request_id" ON "plugin_ai_task" ("request_id") WHERE "request_id" <> ''`,
		`CREATE INDEX IF NOT EXISTS "idx_plugin_ai_task_status" ON "plugin_ai_task" ("status", "id")`,
		`CREATE INDEX IF NOT EXISTS "idx_plugin_ai_task_type_time" ON "plugin_ai_task" ("task_type", "create_time")`,
		`CREATE TABLE IF NOT EXISTS "plugin_ai_voice" (
			"id" INTEGER PRIMARY KEY AUTOINCREMENT,
			"name" varchar(50) NOT NULL DEFAULT '',
			"model" varchar(100) NOT NULL DEFAULT '',
			"voice" varchar(60) NOT NULL DEFAULT '',
			"format" varchar(10) NOT NULL DEFAULT 'mp3',
			"status" smallint NOT NULL DEFAULT 1,
			"remark" varchar(255) NOT NULL DEFAULT '',
			"create_time" integer NOT NULL DEFAULT 0,
			"update_time" integer NOT NULL DEFAULT 0,
			CONSTRAINT "ck_plugin_ai_voice_status" CHECK ("status" IN (0, 1)),
			CONSTRAINT "ck_plugin_ai_voice_format" CHECK ("format" IN ('mp3', 'wav', 'pcm', 'opus'))
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS "uk_plugin_ai_voice_name" ON "plugin_ai_voice" ("name")`,
		`CREATE INDEX IF NOT EXISTS "idx_plugin_ai_voice_model" ON "plugin_ai_voice" ("model")`,
		`CREATE INDEX IF NOT EXISTS "idx_plugin_ai_voice_status" ON "plugin_ai_voice" ("status")`,
	}
	for _, stmt := range ddls {
		if err := db.Exec(stmt).Error; err != nil {
			return err
		}
	}

	seeds := []string{
		`INSERT OR IGNORE INTO "admin_plugins" ("name", "title", "version", "kind", "status", "host_api", "host_version", "framework_req", "installed_at", "updated_at") VALUES
			('ai', 'AI 路由', '0.2.0', 'mixed', 'enabled', '>=1.1 <2', '0.1.0', '>=0.1 <1', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
		`INSERT OR IGNORE INTO "admin_rules" ("id", "pid", "type", "title", "name", "path", "icon", "open_type", "url", "component", "keepalive", "extend", "remark", "weigh", "status", "plugin", "updated_at", "created_at") VALUES
			(300, NULL, 'dir', 'AI 路由', 'plugin/ai', 'plugin/ai', 'lucide-bot', '', '', '', 1, '', '', 990, 1, 'ai', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
			(301, 300, 'menu', '总览', 'plugin/ai/overview', 'plugin/ai/overview', 'lucide-gauge', 'tab', '', '/src/views/admin/plugin/ai/overview/index.vue', 1, '', '', 100, 1, 'ai', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
			(302, 300, 'menu', 'AI 资产体检', 'plugin/ai/index', 'plugin/ai/index', 'lucide-stethoscope', 'tab', '', '/src/views/admin/plugin/ai/index.vue', 1, '', '内核同源 24 项机械判据体检台', 98, 1, 'ai', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
			(303, 300, 'menu', 'AI 成本账单', 'plugin/ai/cost', 'plugin/ai/cost', 'lucide-coins', 'tab', '', '/src/views/admin/plugin/ai/cost.vue', 1, '', '原价/缓存节省/离峰节省恒等分解与五维归因', 95, 1, 'ai', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
			(304, 300, 'menu', '渠道管理', 'plugin/ai/channel', 'plugin/ai/channel', 'lucide-server', 'tab', '', '/src/views/admin/plugin/ai/channel/index.vue', 1, '', '', 90, 1, 'ai', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
			(305, 300, 'menu', '厂商管理', 'plugin/ai/supplier', 'plugin/ai/supplier', 'lucide-building-2', 'tab', '', '/src/views/admin/plugin/ai/supplier/index.vue', 1, '', '', 85, 1, 'ai', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
			(306, 300, 'menu', '模型管理', 'plugin/ai/model', 'plugin/ai/model', 'lucide-boxes', 'tab', '', '/src/views/admin/plugin/ai/model/index.vue', 1, '', '', 80, 1, 'ai', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
			(307, 300, 'menu', '应用接入', 'plugin/ai/apikey', 'plugin/ai/apikey', 'lucide-key-round', 'tab', '', '/src/views/admin/plugin/ai/apikey/index.vue', 1, '', '', 75, 1, 'ai', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
			(308, 300, 'menu', '用量统计', 'plugin/ai/usage', 'plugin/ai/usage', 'lucide-chart-column', 'tab', '', '/src/views/admin/plugin/ai/usage/index.vue', 1, '', '', 70, 1, 'ai', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
			(309, 300, 'menu', '调用日志', 'plugin/ai/usage-log', 'plugin/ai/usage-log', 'lucide-scroll-text', 'tab', '', '/src/views/admin/plugin/ai/usage/index.vue', 1, '', '', 60, 1, 'ai', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
			(310, 300, 'menu', '异步任务', 'plugin/ai/task', 'plugin/ai/task', 'lucide-list-todo', 'tab', '', '/src/views/admin/plugin/ai/task/index.vue', 1, '', '', 55, 1, 'ai', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
			(311, 300, 'menu', '语音合成', 'plugin/ai/voice', 'plugin/ai/voice', 'lucide-audio-lines', 'tab', '', '/src/views/admin/plugin/ai/voice/index.vue', 1, '', '', 50, 1, 'ai', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
			(312, 302, 'node', '体检与成本报告', 'plugin/ai/report', '', '', '', '', '', 1, '', '', 0, 1, 'ai', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
			(313, 304, 'node', '查看渠道', 'plugin/ai/channel/read', '', '', '', '', '', 1, '', '', 0, 1, 'ai', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
			(314, 304, 'node', '新增渠道', 'plugin/ai/channel/create', '', '', '', '', '', 1, '', '', 0, 1, 'ai', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
			(315, 304, 'node', '编辑渠道', 'plugin/ai/channel/update', '', '', '', '', '', 1, '', '', 0, 1, 'ai', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
			(316, 304, 'node', '删除渠道', 'plugin/ai/channel/delete', '', '', '', '', '', 1, '', '', 0, 1, 'ai', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
			(317, 304, 'node', '渠道启停', 'plugin/ai/channel/switch', '', '', '', '', '', 1, '', '', 0, 1, 'ai', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
			(318, 304, 'node', '渠道探活', 'plugin/ai/channel/probe', '', '', '', '', '', 1, '', '', 0, 1, 'ai', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
			(319, 304, 'node', '人工熔断', 'plugin/ai/channel/breaker', '', '', '', '', '', 1, '', '', 0, 1, 'ai', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
			(320, 304, 'node', '熔断事件', 'plugin/ai/channel/events', '', '', '', '', '', 1, '', '', 0, 1, 'ai', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
			(321, 306, 'node', '查看模型', 'plugin/ai/model/read', '', '', '', '', '', 1, '', '', 0, 1, 'ai', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
			(322, 306, 'node', '新增模型', 'plugin/ai/model/create', '', '', '', '', '', 1, '', '', 0, 1, 'ai', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
			(323, 306, 'node', '编辑模型', 'plugin/ai/model/update', '', '', '', '', '', 1, '', '', 0, 1, 'ai', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
			(324, 306, 'node', '删除模型', 'plugin/ai/model/delete', '', '', '', '', '', 1, '', '', 0, 1, 'ai', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
			(325, 306, 'node', '绑定渠道', 'plugin/ai/model/bind', '', '', '', '', '', 1, '', '', 0, 1, 'ai', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
			(326, 308, 'node', '查看用量', 'plugin/ai/usage/read', '', '', '', '', '', 1, '', '', 0, 1, 'ai', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
			(327, 309, 'node', '查看调用日志', 'plugin/ai/usage-log/read', '', '', '', '', '', 1, '', '', 0, 1, 'ai', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
			(328, 309, 'node', '尝试链详情', 'plugin/ai/usage-log/detail', '', '', '', '', '', 1, '', '', 0, 1, 'ai', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
			(329, 301, 'node', '查看总览', 'plugin/ai/overview/index', '', '', '', '', '', 1, '', '', 0, 1, 'ai', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
			(330, 300, 'node', '保存接入设置', 'plugin/ai/settings/save', '', '', '', '', '', 1, '', '', 0, 1, 'ai', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
			(331, 305, 'node', '查看厂商', 'plugin/ai/supplier/read', '', '', '', '', '', 1, '', '', 0, 1, 'ai', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
			(332, 305, 'node', '新增厂商', 'plugin/ai/supplier/create', '', '', '', '', '', 1, '', '', 0, 1, 'ai', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
			(333, 305, 'node', '编辑厂商', 'plugin/ai/supplier/update', '', '', '', '', '', 1, '', '', 0, 1, 'ai', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
			(334, 305, 'node', '删除厂商', 'plugin/ai/supplier/delete', '', '', '', '', '', 1, '', '', 0, 1, 'ai', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
			(335, 307, 'node', '查看应用接入', 'plugin/ai/apikey/read', '', '', '', '', '', 1, '', '', 0, 1, 'ai', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
			(336, 307, 'node', '签发应用接入', 'plugin/ai/apikey/create', '', '', '', '', '', 1, '', '', 0, 1, 'ai', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
			(337, 307, 'node', '编辑应用接入', 'plugin/ai/apikey/update', '', '', '', '', '', 1, '', '', 0, 1, 'ai', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
			(338, 307, 'node', '删除应用接入', 'plugin/ai/apikey/delete', '', '', '', '', '', 1, '', '', 0, 1, 'ai', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
			(339, 307, 'node', '重置应用接入密钥', 'plugin/ai/apikey/reset', '', '', '', '', '', 1, '', '', 0, 1, 'ai', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
			(340, 307, 'node', '应用接入启停', 'plugin/ai/apikey/switch', '', '', '', '', '', 1, '', '', 0, 1, 'ai', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
			(341, 310, 'node', '查看任务', 'plugin/ai/task/read', '', '', '', '', '', 1, '', '', 0, 1, 'ai', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
			(342, 310, 'node', '提交任务', 'plugin/ai/task/submit', '', '', '', '', '', 1, '', '', 0, 1, 'ai', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
			(343, 310, 'node', '导出任务结果', 'plugin/ai/task/export', '', '', '', '', '', 1, '', '', 0, 1, 'ai', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
			(344, 311, 'node', '查看音色', 'plugin/ai/voice/read', '', '', '', '', '', 1, '', '', 0, 1, 'ai', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
			(345, 311, 'node', '新增音色', 'plugin/ai/voice/create', '', '', '', '', '', 1, '', '', 0, 1, 'ai', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
			(346, 311, 'node', '编辑音色', 'plugin/ai/voice/update', '', '', '', '', '', 1, '', '', 0, 1, 'ai', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
			(347, 311, 'node', '删除音色', 'plugin/ai/voice/delete', '', '', '', '', '', 1, '', '', 0, 1, 'ai', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
			(348, 311, 'node', '试听音色', 'plugin/ai/voice/preview', '', '', '', '', '', 1, '', '', 0, 1, 'ai', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
		// 插件配置键 17 行（与 plugin.yaml 的 configs: 逐项对齐；落库名 = plugin_ai_<键>、分组 plugin.ai、
		// allow_del=0）。原生安装器由「安装编排 ④」代写这批行；SQLite 形态由本函数幂等播种。
		// 行内 value 由插件运行期自写（POST /admin/plugin/ai/settings/save），这里只建行不覆盖。
		`INSERT OR IGNORE INTO "configs" ("name", "group", "title", "type", "tip", "value", "allow_del", "weigh") VALUES
			('plugin_ai_api_key', 'plugin.ai', '网关 API Key', 'password', '公开面 /ai/v1/* 的 Bearer 凭证；留空 = 未配置（公开面 503 fail-closed）', '', 0, 99),
			('plugin_ai_breaker_fail_threshold', 'plugin.ai', '熔断失败阈值', 'number', '渠道 fail_threshold=0 时使用（默认 5）', '5', 0, 98),
			('plugin_ai_breaker_cooldown_seconds', 'plugin.ai', '熔断冷却秒数', 'number', '渠道 cooldown_seconds=0 时使用（默认 60）', '60', 0, 97),
			('plugin_ai_request_timeout', 'plugin.ai', '请求超时（秒）', 'number', '渠道 timeout=0 时使用（默认 60）', '60', 0, 96),
			('plugin_ai_stream_timeout', 'plugin.ai', '流式总超时（秒）', 'number', '流式请求总时长上限（默认 300；不受渠道 timeout 覆盖）', '300', 0, 95),
			('plugin_ai_currency', 'plugin.ai', '计费币种', 'string', '金额出口的币种标签（默认 USD）', 'USD', 0, 94),
			('plugin_ai_allow_private_hosts', 'plugin.ai', '允许私网上游', 'switch', '1=放行 loopback/内网地址；本机 ollama 等自建上游需开启，否则出站护栏会拦下', '0', 0, 93),
			('plugin_ai_sse_max_line_bytes', 'plugin.ai', 'SSE 单行上限（字节）', 'number', '单条 SSE 行的字节上限；0=缺省 1MiB', '1048576', 0, 92),
			('plugin_ai_sse_max_frame_bytes', 'plugin.ai', 'SSE 单帧上限（字节）', 'number', '单个 SSE 事件的累积上限；0=缺省 8MiB', '8388608', 0, 91),
			('plugin_ai_sse_max_accumulated_bytes', 'plugin.ai', 'SSE 累积上限（字节）', 'number', '单次流式响应累计上限；0=缺省 32MiB', '33554432', 0, 90),
			('plugin_ai_rate_limit_rpm', 'plugin.ai', '公开面限流（次/分/Key）', 'number', '公开面按 Key 限流（0=不限）', '0', 0, 89),
			('plugin_ai_rate_limit_burst', 'plugin.ai', '公开面限流突发容量', 'number', '公开面令牌桶容量（0=按 rpm 推导）', '0', 0, 88),
			('plugin_ai_rate_limit_max_inflight', 'plugin.ai', '公开面在飞并发上限', 'number', '公开面同 Key 在飞请求上限（0=不限）', '0', 0, 87),
			('plugin_ai_channel_gate_rpm_default', 'plugin.ai', '渠道准入 RPM 缺省', 'number', '渠道 rpm_limit=0 时使用；0=不限', '0', 0, 86),
			('plugin_ai_channel_gate_tpm_default', 'plugin.ai', '渠道准入 TPM 缺省', 'number', '渠道 tpm_limit=0 时使用；0=不限', '0', 0, 85),
			('plugin_ai_task_poll_seconds', 'plugin.ai', '任务轮询间隔（秒）', 'number', '异步任务 worker 每轮间隔；0=缺省 5', '5', 0, 84),
			('plugin_ai_task_max_attempts', 'plugin.ai', '任务最大尝试次数', 'number', '失败任务重试上限；0=缺省 3（上界 10）', '3', 0, 83)`,
	}
	for _, stmt := range seeds {
		if err := db.Exec(strings.TrimSpace(stmt)).Error; err != nil {
			return err
		}
	}
	return nil
}

// bootstrapRollingMemoryEvalTables 幂等初始化 rolling-memory 插件的测评任务台账表与逐题明细表。
func bootstrapRollingMemoryEvalTables(db *gorm.DB) error {
	ddls := []string{
		`CREATE TABLE IF NOT EXISTS "plugin_rolling_memory_eval_task" (
			"id" varchar(128) PRIMARY KEY,
			"suite" varchar(32) NOT NULL DEFAULT 'locomo',
			"status" varchar(32) NOT NULL DEFAULT 'completed',
			"label" varchar(255) NOT NULL DEFAULT '',
			"label_en" varchar(255) NOT NULL DEFAULT '',
			"tier" varchar(32) NOT NULL DEFAULT '',
			"arm" varchar(64) NOT NULL DEFAULT '',
			"model" varchar(128) NOT NULL DEFAULT '',
			"protocol" varchar(64) NOT NULL DEFAULT '',
			"scope" varchar(128) NOT NULL DEFAULT '',
			"endpoint" varchar(255) NOT NULL DEFAULT '',
			"corpus_sha256" varchar(128) NOT NULL DEFAULT '',
			"purged_units" integer NOT NULL DEFAULT 0,
			"scored_questions" integer NOT NULL DEFAULT 0,
			"evidence_hit" real NOT NULL DEFAULT 0,
			"full_evidence_hit" real NOT NULL DEFAULT 0,
			"answer_in_context" real NOT NULL DEFAULT 0,
			"judge_rate" real NOT NULL DEFAULT 0,
			"judge_correct" integer NOT NULL DEFAULT 0,
			"judge_scored" integer NOT NULL DEFAULT 0,
			"elapsed_seconds" real NOT NULL DEFAULT 0,
			"avg_recall_ms" real NOT NULL DEFAULT 0,
			"p95_recall_ms" real NOT NULL DEFAULT 0,
			"avg_delivered_tokens" real NOT NULL DEFAULT 0,
			"total_delivered_tokens" integer NOT NULL DEFAULT 0,
			"avg_delivered_items" real NOT NULL DEFAULT 0,
			"category_evidence" text NOT NULL DEFAULT '{}',
			"category_judge" text NOT NULL DEFAULT '{}',
			"bucket_counts" text NOT NULL DEFAULT '{}',
			"note" text NOT NULL DEFAULT '',
			"note_en" text NOT NULL DEFAULT '',
			"repro_cli" text NOT NULL DEFAULT '',
			"error" text NOT NULL DEFAULT '',
			"payload" text NOT NULL DEFAULT '{}',
			"create_time" integer NOT NULL DEFAULT 0,
			"update_time" integer NOT NULL DEFAULT 0,
			CONSTRAINT "ck_plugin_rm_eval_task_status" CHECK ("status" IN ('completed', 'cancelled', 'failed'))
		)`,
		`CREATE INDEX IF NOT EXISTS "idx_plugin_rm_eval_task_suite_time" ON "plugin_rolling_memory_eval_task" ("suite", "create_time" DESC)`,
		`CREATE INDEX IF NOT EXISTS "idx_plugin_rm_eval_task_status" ON "plugin_rolling_memory_eval_task" ("status")`,
		`CREATE TABLE IF NOT EXISTS "plugin_rolling_memory_eval_task_item" (
			"id" INTEGER PRIMARY KEY AUTOINCREMENT,
			"run_id" varchar(128) NOT NULL DEFAULT '',
			"seq" integer NOT NULL DEFAULT 0,
			"question_id" varchar(128) NOT NULL DEFAULT '',
			"category" varchar(32) NOT NULL DEFAULT '',
			"correct" smallint NOT NULL DEFAULT 0,
			"evidence_hit" smallint NOT NULL DEFAULT 0,
			"qs_score" real NOT NULL DEFAULT 0,
			"engine_transport" varchar(64) NOT NULL DEFAULT '',
			"engine_scope" varchar(128) NOT NULL DEFAULT '',
			"cloud_hits" integer NOT NULL DEFAULT 0,
			"recall_ms" integer NOT NULL DEFAULT 0,
			"delivered_tokens" integer NOT NULL DEFAULT 0,
			"delivered_items" integer NOT NULL DEFAULT 0,
			"total_ms" integer NOT NULL DEFAULT 0,
			"payload" text NOT NULL DEFAULT '{}',
			"create_time" integer NOT NULL DEFAULT 0
		)`,
		`CREATE INDEX IF NOT EXISTS "idx_plugin_rm_eval_task_item_run_seq" ON "plugin_rolling_memory_eval_task_item" ("run_id", "seq")`,
	}
	for _, stmt := range ddls {
		if err := db.Exec(strings.TrimSpace(stmt)).Error; err != nil {
			return err
		}
	}
	return nil
}
