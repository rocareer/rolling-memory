-- ===== 插件机制（宿主基座）：台账 + L2 实体元数据 + 审计三表 + admin_rules 归属列 + 管理面节点 =====
-- 幂等：DDL 全部 IF NOT EXISTS；节点种子走 NOT EXISTS 判重（admin_rules.name 无唯一索引，禁止写死 ID）。
-- 禁 CREATE INDEX CONCURRENTLY（本机制整文件隐式事务内必报错）；空表新建用普通 CREATE INDEX。
-- 边界：插件自管迁移（per-plugin 版本表）与 L2 实体表 DDL 均不进本目录；本文件只建宿主自己的表与归属列。
-- 编号说明：迁移编号按当时最大编号 +1 取号——编号只前进，低号文件在已推进到更高号的库上
-- 永不执行；本文件因此必须**可重复应用**（复装、改名重放、跨仓继承后重放都会再跑一次），
-- DDL 全 IF NOT EXISTS、节点行 NOT EXISTS 判重即为此。
-- 编号约定：后续新增迁移若与本编号相撞，按整体后移重编号处理（内容零改动）。

-- ===== ① 插件台账（status 是「期望状态」；接线真相在进程内注册表）=====
-- status 默认 failed：fail-closed——手工插入的行不应被当作已安装；正常路径由安装器显式写 failed → installed（④/⑧）。
CREATE TABLE IF NOT EXISTS "__PREFIX__admin_plugins" (
    "id"            bigserial    PRIMARY KEY,
    "name"          varchar(32)  NOT NULL DEFAULT '',
    "title"         varchar(64)  NOT NULL DEFAULT '',
    "version"       varchar(32)  NOT NULL DEFAULT '',
    "kind"          varchar(16)  NOT NULL DEFAULT 'code',
    "status"        varchar(16)  NOT NULL DEFAULT 'failed',
    "host_api"      varchar(64)  NOT NULL DEFAULT '',
    "host_version"  varchar(32)  NOT NULL DEFAULT '',
    "framework_req" varchar(64)  NOT NULL DEFAULT '',
    "requires"      text         NOT NULL DEFAULT '',
    "checksum"      varchar(64)  NOT NULL DEFAULT '',
    "source"        varchar(255) NOT NULL DEFAULT '',
    "signed_by"     varchar(128) NOT NULL DEFAULT '',
    "installed_at"  timestamptz,
    "updated_at"    timestamptz,
    "remark"        varchar(255) NOT NULL DEFAULT '',
    CONSTRAINT "__PREFIX__ck_admin_plugins_kind" CHECK ("kind" IN ('code', 'declarative', 'mixed')),
    CONSTRAINT "__PREFIX__ck_admin_plugins_status" CHECK ("status" IN ('failed', 'installed', 'enabled', 'disabled'))
);
-- 插件名唯一（= plugins/<名>/ 目录名 = manifest.name = 路由段 plugin/<名>）
CREATE UNIQUE INDEX IF NOT EXISTS "__PREFIX__uk_admin_plugins_name" ON "__PREFIX__admin_plugins" ("name");
COMMENT ON TABLE "__PREFIX__admin_plugins" IS '插件台账（期望状态；是否生效另由进程内注册表与运行时闸决定）';
COMMENT ON COLUMN "__PREFIX__admin_plugins"."id" IS 'ID';
COMMENT ON COLUMN "__PREFIX__admin_plugins"."name" IS '插件名:^[a-z][a-z0-9-]{1,31}$；= 目录名；不得命中宿主保留字';
COMMENT ON COLUMN "__PREFIX__admin_plugins"."title" IS '展示名（manifest.title）';
COMMENT ON COLUMN "__PREFIX__admin_plugins"."version" IS '插件版本（manifest.version，X.Y.Z）';
COMMENT ON COLUMN "__PREFIX__admin_plugins"."kind" IS '插件形态:code=源码插件,declarative=声明式,mixed=混合';
COMMENT ON COLUMN "__PREFIX__admin_plugins"."status" IS '状态:failed=半装,installed=已安装(菜单隐藏),enabled=启用,disabled=停用';
COMMENT ON COLUMN "__PREFIX__admin_plugins"."host_api" IS '安装时校验过的机制接口版本域（对 host.APIVersion）';
COMMENT ON COLUMN "__PREFIX__admin_plugins"."host_version" IS '安装时的宿主版本（version.Get().Version；仅诊断，不参与校验）';
COMMENT ON COLUMN "__PREFIX__admin_plugins"."framework_req" IS '框架线版本域（只记录与告警，不拒装）';
COMMENT ON COLUMN "__PREFIX__admin_plugins"."requires" IS '依赖插件列表（name@range，逗号分隔）';
COMMENT ON COLUMN "__PREFIX__admin_plugins"."checksum" IS '整包 sha256（来源认证由 signed_by 签名承载）';
COMMENT ON COLUMN "__PREFIX__admin_plugins"."source" IS '安装来源（包文件名，如 demo-1.0.0.zip）';
COMMENT ON COLUMN "__PREFIX__admin_plugins"."signed_by" IS '签名公钥指纹（空=未签名）';
COMMENT ON COLUMN "__PREFIX__admin_plugins"."installed_at" IS '安装时间';
COMMENT ON COLUMN "__PREFIX__admin_plugins"."updated_at" IS '更新时间';
COMMENT ON COLUMN "__PREFIX__admin_plugins"."remark" IS '备注';
COMMENT ON INDEX "__PREFIX__uk_admin_plugins_name" IS '插件名唯一（台账单行读的索引）';

-- ===== ② L2 实体元数据（声明式插件的实体与字段定义）=====
CREATE TABLE IF NOT EXISTS "__PREFIX__admin_plugin_entities" (
    "id"         bigserial    PRIMARY KEY,
    "plugin"     varchar(32)  NOT NULL DEFAULT '',
    "name"       varchar(64)  NOT NULL DEFAULT '',
    "title_zh"   varchar(64)  NOT NULL DEFAULT '',
    "title_en"   varchar(64)  NOT NULL DEFAULT '',
    "table_name" varchar(64)  NOT NULL DEFAULT '',
    "spec"       jsonb        NOT NULL DEFAULT '{}'::jsonb,
    "created_at" timestamptz,
    "updated_at" timestamptz
);
-- 同插件内实体名唯一；(plugin, name) 前缀同时服务「按插件取实体」的等值查询，不另建单列索引
CREATE UNIQUE INDEX IF NOT EXISTS "__PREFIX__uk_admin_plugin_entities_plugin_name"
    ON "__PREFIX__admin_plugin_entities" ("plugin", "name");
COMMENT ON TABLE "__PREFIX__admin_plugin_entities" IS 'L2 实体元数据（插件卸载默认级联删除元数据行；表与数据保留）';
COMMENT ON COLUMN "__PREFIX__admin_plugin_entities"."id" IS 'ID';
COMMENT ON COLUMN "__PREFIX__admin_plugin_entities"."plugin" IS '归属插件名（= admin_plugins.name）';
COMMENT ON COLUMN "__PREFIX__admin_plugin_entities"."name" IS '实体名（闭集标识符；参与物理表名与路由段）';
COMMENT ON COLUMN "__PREFIX__admin_plugin_entities"."title_zh" IS '实体中文标题（文案绕开构建期语言包，随响应下发）';
COMMENT ON COLUMN "__PREFIX__admin_plugin_entities"."title_en" IS '实体英文标题';
COMMENT ON COLUMN "__PREFIX__admin_plugin_entities"."table_name" IS '物理表名（显式存实际值：<前缀>plugin_<名>_<实体>，命名规则演化时不失真）';
COMMENT ON COLUMN "__PREFIX__admin_plugin_entities"."spec" IS '实体与字段 spec（JSONB，键集闭集）';
COMMENT ON COLUMN "__PREFIX__admin_plugin_entities"."created_at" IS '创建时间';
COMMENT ON COLUMN "__PREFIX__admin_plugin_entities"."updated_at" IS '更新时间';
COMMENT ON INDEX "__PREFIX__uk_admin_plugin_entities_plugin_name" IS '同插件内实体名唯一（按插件取实体的索引）';

-- ===== ③ 插件动作审计（append-only；CLI 动作不经 AdminLog）=====
-- detail 存 dry-run 摘要、执行结果与补偿回滚过程（含被拒原因）；管理页的启用/停用与 CLI 复用同一写入函数。
CREATE TABLE IF NOT EXISTS "__PREFIX__admin_plugin_actions" (
    "id"         bigserial    PRIMARY KEY,
    "plugin"     varchar(32)  NOT NULL DEFAULT '',
    "action"     varchar(16)  NOT NULL DEFAULT '',
    "operator"   varchar(64)  NOT NULL DEFAULT '',
    "status"     varchar(16)  NOT NULL DEFAULT '',
    "detail"     jsonb        NOT NULL DEFAULT '{}'::jsonb,
    "created_at" timestamptz  NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS "__PREFIX__idx_admin_plugin_actions_plugin_created"
    ON "__PREFIX__admin_plugin_actions" ("plugin", "created_at" DESC);
COMMENT ON TABLE "__PREFIX__admin_plugin_actions" IS '插件动作审计（append-only；只增不改不删）';
COMMENT ON COLUMN "__PREFIX__admin_plugin_actions"."id" IS 'ID';
COMMENT ON COLUMN "__PREFIX__admin_plugin_actions"."plugin" IS '插件名';
COMMENT ON COLUMN "__PREFIX__admin_plugin_actions"."action" IS '动作:install=安装,enable=启用,disable=停用,uninstall=卸载,upgrade=升级,verify=校验';
COMMENT ON COLUMN "__PREFIX__admin_plugin_actions"."operator" IS '操作者署名（不构成身份认证，边界在 OS/部署侧访问控制）';
COMMENT ON COLUMN "__PREFIX__admin_plugin_actions"."status" IS '执行结果（值域由写入方约定，如 success/failed/rejected；当前未固定）';
COMMENT ON COLUMN "__PREFIX__admin_plugin_actions"."detail" IS '详情 JSON（dry-run 摘要、执行结果、补偿回滚过程与被拒原因）';
COMMENT ON COLUMN "__PREFIX__admin_plugin_actions"."created_at" IS '创建时间';
COMMENT ON INDEX "__PREFIX__idx_admin_plugin_actions_plugin_created" IS '单插件审计分页索引（plugin, created_at DESC）';

-- ===== ④ admin_rules 归属列（归属是行的属性，落列即可被既有查询与清理 SQL 直接使用）=====
-- 插件写入的菜单/节点由安装器统一代写并打本列（插件自带 SQL 不得写 admin_rules），保证归属永真。
ALTER TABLE "__PREFIX__admin_rules" ADD COLUMN IF NOT EXISTS "plugin" varchar(32) NOT NULL DEFAULT '';
COMMENT ON COLUMN "__PREFIX__admin_rules"."plugin" IS '归属插件名（空=宿主所有；卸载按本列整树清理）';
-- 部分索引：只索引 non-empty；卸载清理走 WHERE plugin = ?（宿主规则不受影响）
CREATE INDEX IF NOT EXISTS "__PREFIX__idx_admin_rules_plugin"
    ON "__PREFIX__admin_rules" ("plugin") WHERE "plugin" <> '';
COMMENT ON INDEX "__PREFIX__idx_admin_rules_plugin" IS '插件归属部分索引（只索引非空归属列；卸载清理走 WHERE plugin = ?）';

-- ===== ⑤ 插件管理面权限节点（宿主所有，路径固定不占插件命名空间）=====
-- 默认不授予任何角色组：仅超管（Rules='*'）可见可用；非超管须在角色组里显式勾选。
-- pid 暂为 NULL：管理页菜单（web/src/views/admin/plugin/index.vue）随后续版本补建，届时把本 3 个节点的 pid 指向该菜单。
-- 节点名与插件规则名结构上不可能相撞（插件规则一律 plugin/<名>/…，至少三段）。
INSERT INTO "__PREFIX__admin_rules" ("pid", "type", "title", "name", "path", "icon", "open_type", "url", "component", "keepalive", "extend", "remark", "weigh", "status", "updated_at", "created_at")
SELECT NULL, 'node', '查看', 'plugin/read', '', '', '', '', '', 1, '', '', 0, 1, NOW(), NOW()
WHERE NOT EXISTS (SELECT 1 FROM "__PREFIX__admin_rules" WHERE "name" = 'plugin/read' AND "type" = 'node');

INSERT INTO "__PREFIX__admin_rules" ("pid", "type", "title", "name", "path", "icon", "open_type", "url", "component", "keepalive", "extend", "remark", "weigh", "status", "updated_at", "created_at")
SELECT NULL, 'node', '启用', 'plugin/enable', '', '', '', '', '', 1, '', '', 0, 1, NOW(), NOW()
WHERE NOT EXISTS (SELECT 1 FROM "__PREFIX__admin_rules" WHERE "name" = 'plugin/enable' AND "type" = 'node');

INSERT INTO "__PREFIX__admin_rules" ("pid", "type", "title", "name", "path", "icon", "open_type", "url", "component", "keepalive", "extend", "remark", "weigh", "status", "updated_at", "created_at")
SELECT NULL, 'node', '停用', 'plugin/disable', '', '', '', '', '', 1, '', '', 0, 1, NOW(), NOW()
WHERE NOT EXISTS (SELECT 1 FROM "__PREFIX__admin_rules" WHERE "name" = 'plugin/disable' AND "type" = 'node');
