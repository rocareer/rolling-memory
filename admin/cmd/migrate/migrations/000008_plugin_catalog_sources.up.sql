-- ===== 插件来源配置 + 统一清单快照 + 插件中心菜单/节点 =====
-- 设计：插件来源（local/http 共用一种 catalog 格式）与清单安装通道。
-- 幂等：表/索引 IF NOT EXISTS；菜单/节点按 name/path 判重；挂靠用 IS DISTINCT FROM。
-- 不建外键：删除来源时由服务在同一事务删除快照行（与 000004 的应用层归属清理同风格）。
-- 本文件只建宿主表与宿主菜单/节点；不写 admin_groups.rules（新节点默认只有超管可见可用）。

CREATE TABLE IF NOT EXISTS "__PREFIX__admin_plugin_sources" (
    "id"                        bigserial    PRIMARY KEY,
    "name"                      varchar(32)  NOT NULL DEFAULT '',
    "title"                     varchar(64)  NOT NULL DEFAULT '',
    "type"                      varchar(16)  NOT NULL DEFAULT 'local',
    "location"                  text         NOT NULL DEFAULT '',
    "enabled"                   smallint     NOT NULL DEFAULT 1,
    "allow_insecure_http"       smallint     NOT NULL DEFAULT 0,
    "allow_private_hosts"       smallint     NOT NULL DEFAULT 0,
    "allow_absolute_paths"      smallint     NOT NULL DEFAULT 0,
    "weigh"                     bigint       NOT NULL DEFAULT 0,
    "last_sync_status"          varchar(16)  NOT NULL DEFAULT '',
    "last_sync_error"           text         NOT NULL DEFAULT '',
    "last_catalog_sha256"       varchar(64)  NOT NULL DEFAULT '',
    "last_catalog_generated_at" timestamptz,
    "last_synced_at"            timestamptz,
    "created_by"                varchar(64)  NOT NULL DEFAULT '',
    "updated_by"                varchar(64)  NOT NULL DEFAULT '',
    "remark"                    varchar(255) NOT NULL DEFAULT '',
    "created_at"                timestamptz,
    "updated_at"                timestamptz,
    CONSTRAINT "__PREFIX__ck_admin_plugin_sources_type" CHECK ("type" IN ('local', 'http')),
    CONSTRAINT "__PREFIX__ck_admin_plugin_sources_enabled" CHECK ("enabled" IN (0, 1)),
    CONSTRAINT "__PREFIX__ck_admin_plugin_sources_flags" CHECK (
        "allow_insecure_http" IN (0, 1) AND
        "allow_private_hosts" IN (0, 1) AND
        "allow_absolute_paths" IN (0, 1)
    )
);
CREATE UNIQUE INDEX IF NOT EXISTS "__PREFIX__uk_admin_plugin_sources_name"
    ON "__PREFIX__admin_plugin_sources" ("name");
CREATE INDEX IF NOT EXISTS "__PREFIX__idx_admin_plugin_sources_enabled_weigh"
    ON "__PREFIX__admin_plugin_sources" ("enabled", "weigh");
COMMENT ON TABLE "__PREFIX__admin_plugin_sources" IS '插件仓库来源（local 文件 / http(s) URL；共用 catalog v1 格式）';
COMMENT ON COLUMN "__PREFIX__admin_plugin_sources"."name" IS '来源键:^[a-z][a-z0-9-]{1,31}$；创建后不可改名';
COMMENT ON COLUMN "__PREFIX__admin_plugin_sources"."type" IS '运输方式:local=本地文件,http=HTTP(S)';
COMMENT ON COLUMN "__PREFIX__admin_plugin_sources"."location" IS 'local:仓库根相对路径(或显式允许的绝对路径);http:https URL';
COMMENT ON COLUMN "__PREFIX__admin_plugin_sources"."last_sync_status" IS '最后同步状态:空=never,ok,error';
COMMENT ON COLUMN "__PREFIX__admin_plugin_sources"."last_catalog_sha256" IS '最后成功同步的清单字节sha256';
COMMENT ON COLUMN "__PREFIX__admin_plugin_sources"."last_sync_error" IS '最后同步失败原因(已脱敏,截断2000字符)';

CREATE TABLE IF NOT EXISTS "__PREFIX__admin_plugin_catalog_entries" (
    "id"              bigserial    PRIMARY KEY,
    "source_id"       bigint       NOT NULL,
    "source_name"     varchar(32)  NOT NULL DEFAULT '',
    "uid"             varchar(32)  NOT NULL DEFAULT '',
    "version"         varchar(32)  NOT NULL DEFAULT '',
    "version_major"   integer      NOT NULL DEFAULT 0,
    "version_minor"   integer      NOT NULL DEFAULT 0,
    "version_patch"   integer      NOT NULL DEFAULT 0,
    "title_zh"        varchar(64)  NOT NULL DEFAULT '',
    "title_en"        varchar(64)  NOT NULL DEFAULT '',
    "desc_zh"         varchar(256) NOT NULL DEFAULT '',
    "desc_en"         varchar(256) NOT NULL DEFAULT '',
    "kind"            varchar(16)  NOT NULL DEFAULT '',
    "host_api"        varchar(64)  NOT NULL DEFAULT '',
    "framework_req"   varchar(64)  NOT NULL DEFAULT '',
    "requires"        text         NOT NULL DEFAULT '',
    "license"         varchar(32)  NOT NULL DEFAULT '',
    "package_url"     text         NOT NULL DEFAULT '',
    "package_path"    text         NOT NULL DEFAULT '',
    "package_size"    bigint       NOT NULL DEFAULT 0,
    "package_sha256"  varchar(64)  NOT NULL DEFAULT '',
    "package_signed"  smallint     NOT NULL DEFAULT 0,
    "catalog_sha256"  varchar(64)  NOT NULL DEFAULT '',
    "synced_at"       timestamptz  NOT NULL DEFAULT NOW(),
    "created_at"      timestamptz,
    "updated_at"      timestamptz,
    CONSTRAINT "__PREFIX__ck_admin_plugin_catalog_kind"
        CHECK ("kind" IN ('code', 'declarative', 'mixed')),
    CONSTRAINT "__PREFIX__ck_admin_plugin_catalog_size" CHECK ("package_size" > 0),
    CONSTRAINT "__PREFIX__ck_admin_plugin_catalog_signed" CHECK ("package_signed" IN (0, 1)),
    CONSTRAINT "__PREFIX__ck_admin_plugin_catalog_location" CHECK (
        ("package_url" <> '' AND "package_path" = '') OR
        ("package_url" = '' AND "package_path" <> '')
    ),
    CONSTRAINT "__PREFIX__ck_admin_plugin_catalog_source" CHECK ("source_id" > 0),
    CONSTRAINT "__PREFIX__ck_admin_plugin_catalog_versions" CHECK (
        "version_major" >= 0 AND "version_minor" >= 0 AND "version_patch" >= 0
    )
);
CREATE UNIQUE INDEX IF NOT EXISTS "__PREFIX__uk_admin_plugin_catalog_entries_source_uid_version"
    ON "__PREFIX__admin_plugin_catalog_entries" ("source_id", "uid", "version");
CREATE INDEX IF NOT EXISTS "__PREFIX__idx_admin_plugin_catalog_entries_uid_version"
    ON "__PREFIX__admin_plugin_catalog_entries" ("uid", "version");
CREATE INDEX IF NOT EXISTS "__PREFIX__idx_admin_plugin_catalog_entries_source_name"
    ON "__PREFIX__admin_plugin_catalog_entries" ("source_name");
COMMENT ON TABLE "__PREFIX__admin_plugin_catalog_entries" IS '统一清单快照（每次成功同步按来源整份替换；仅展示/筛选/冲突检测）';
COMMENT ON COLUMN "__PREFIX__admin_plugin_catalog_entries"."uid" IS '插件身份键（必须等于包内元数据 uid/name）';
COMMENT ON COLUMN "__PREFIX__admin_plugin_catalog_entries"."package_sha256" IS '包体sha256（清单声明；安装前必须与实际字节一致）';
COMMENT ON COLUMN "__PREFIX__admin_plugin_catalog_entries"."catalog_sha256" IS '该快照来自的清单字节sha256（安装前与来源当前清单比对）';

-- ===== 冲突防线：目标 name/path 已存在时，必须与预期形状逐字一致，否则 RAISE =====
-- 只在 NOT EXISTS 跳过会让下游“看起来迁移成功、其实没有宿主菜单”；down 按 name 删还可能误删宿主行。
DO $$
DECLARE bad text;
BEGIN
    SELECT string_agg("name" || '(' || "type" || ',' || COALESCE("path",'') || ',' || COALESCE("component",'') || ',' || "plugin" || ')', ', ')
    INTO bad
    FROM "__PREFIX__admin_rules"
    WHERE ("name" = 'plugin-center' AND NOT (
              "type" = 'dir' AND "path" = 'plugin-center' AND COALESCE("component",'') = '' AND "plugin" = '' AND "status" = 1))
       OR ("name" = 'plugin/source' AND NOT (
              "type" = 'menu' AND "path" = 'plugin/source'
              AND "component" = '/src/views/admin/plugin/source/index.vue' AND "plugin" = '' AND "status" = 1))
       OR ("name" = 'plugin/catalog' AND NOT (
              "type" = 'menu' AND "path" = 'plugin/catalog'
              AND "component" = '/src/views/admin/plugin/catalog/index.vue' AND "plugin" = '' AND "status" = 1));
    IF bad IS NOT NULL THEN
        RAISE EXCEPTION 'admin_rules 已有同名但形状不符的行：% ⇒ 拒迁移，先人工处理', bad;
    END IF;

    SELECT string_agg("name" || '(' || COALESCE("path",'') || ')', ', ')
    INTO bad
    FROM "__PREFIX__admin_rules"
    WHERE "path" IN ('plugin-center', 'plugin/source', 'plugin/catalog')
      AND "name" NOT IN ('plugin-center', 'plugin/source', 'plugin/catalog');
    IF bad IS NOT NULL THEN
        RAISE EXCEPTION 'admin_rules 已有同路径但不同名的行：% ⇒ 拒迁移，先人工处理', bad;
    END IF;
END $$;

-- ===== 插件中心 dir：不直接挂到现有 plugin 菜单下（有 children 的 menu 会变成不可点击的父菜单）=====
INSERT INTO "__PREFIX__admin_rules"
    ("pid", "type", "title", "name", "path", "icon", "open_type", "url", "component", "keepalive", "extend", "remark", "weigh", "status", "updated_at", "created_at")
SELECT NULL, 'dir', '插件中心', 'plugin-center', 'plugin-center', 'lucide-puzzle', '', '', '', 1, '', '插件来源、清单与已安装插件的分组入口', 989, 1, NOW(), NOW()
WHERE NOT EXISTS (
    SELECT 1 FROM "__PREFIX__admin_rules"
    WHERE "name" = 'plugin-center' OR ("type" = 'dir' AND "path" = 'plugin-center')
);

-- 现有 plugin 菜单挂到 dir 下；只改 pid/weigh，不动 name/path/component/权限
UPDATE "__PREFIX__admin_rules" AS p
SET "pid" = d."id", "weigh" = 100, "updated_at" = NOW()
FROM "__PREFIX__admin_rules" AS d
WHERE d."name" = 'plugin-center' AND d."type" = 'dir'
  AND p."name" = 'plugin' AND p."type" = 'menu' AND p."plugin" = ''
  AND (p."pid" IS DISTINCT FROM d."id" OR p."weigh" IS DISTINCT FROM 100);

-- 来源页菜单（组件路径与前端页面逐字一致）
INSERT INTO "__PREFIX__admin_rules"
    ("pid", "type", "title", "name", "path", "icon", "open_type", "url", "component", "keepalive", "extend", "remark", "weigh", "status", "updated_at", "created_at")
SELECT d."id", 'menu', '仓库来源', 'plugin/source', 'plugin/source', 'lucide-database', 'tab', '', '/src/views/admin/plugin/source/index.vue', 1, '', '插件清单来源配置（local/http 共用一种 catalog 格式）', 90, 1, NOW(), NOW()
FROM "__PREFIX__admin_rules" AS d
WHERE d."name" = 'plugin-center' AND d."type" = 'dir'
  AND NOT EXISTS (
      SELECT 1 FROM "__PREFIX__admin_rules"
      WHERE "name" = 'plugin/source' OR ("type" = 'menu' AND "path" = 'plugin/source')
  );

-- 清单页菜单（组件路径与前端页面逐字一致）
INSERT INTO "__PREFIX__admin_rules"
    ("pid", "type", "title", "name", "path", "icon", "open_type", "url", "component", "keepalive", "extend", "remark", "weigh", "status", "updated_at", "created_at")
SELECT d."id", 'menu', '插件清单', 'plugin/catalog', 'plugin/catalog', 'lucide-package', 'tab', '', '/src/views/admin/plugin/catalog/index.vue', 1, '', '清单浏览、来源冲突提示、预演与安装', 80, 1, NOW(), NOW()
FROM "__PREFIX__admin_rules" AS d
WHERE d."name" = 'plugin-center' AND d."type" = 'dir'
  AND NOT EXISTS (
      SELECT 1 FROM "__PREFIX__admin_rules"
      WHERE "name" = 'plugin/catalog' OR ("type" = 'menu' AND "path" = 'plugin/catalog')
  );

-- 来源页节点（list/get 回落 read；create/update/delete/test 精确匹配）
INSERT INTO "__PREFIX__admin_rules" ("pid","type","title","name","path","icon","open_type","url","component","keepalive","extend","remark","weigh","status","updated_at","created_at")
SELECT m."id",'node','查看','plugin/source/read','','','','','',1,'','',0,1,NOW(),NOW()
FROM "__PREFIX__admin_rules" AS m
WHERE m."name"='plugin/source' AND m."type"='menu'
  AND NOT EXISTS (SELECT 1 FROM "__PREFIX__admin_rules" WHERE "name"='plugin/source/read' AND "type"='node');
INSERT INTO "__PREFIX__admin_rules" ("pid","type","title","name","path","icon","open_type","url","component","keepalive","extend","remark","weigh","status","updated_at","created_at")
SELECT m."id",'node','新增','plugin/source/create','','','','','',1,'','',0,1,NOW(),NOW()
FROM "__PREFIX__admin_rules" AS m
WHERE m."name"='plugin/source' AND m."type"='menu'
  AND NOT EXISTS (SELECT 1 FROM "__PREFIX__admin_rules" WHERE "name"='plugin/source/create' AND "type"='node');
INSERT INTO "__PREFIX__admin_rules" ("pid","type","title","name","path","icon","open_type","url","component","keepalive","extend","remark","weigh","status","updated_at","created_at")
SELECT m."id",'node','更新','plugin/source/update','','','','','',1,'','',0,1,NOW(),NOW()
FROM "__PREFIX__admin_rules" AS m
WHERE m."name"='plugin/source' AND m."type"='menu'
  AND NOT EXISTS (SELECT 1 FROM "__PREFIX__admin_rules" WHERE "name"='plugin/source/update' AND "type"='node');
INSERT INTO "__PREFIX__admin_rules" ("pid","type","title","name","path","icon","open_type","url","component","keepalive","extend","remark","weigh","status","updated_at","created_at")
SELECT m."id",'node','删除','plugin/source/delete','','','','','',1,'','',0,1,NOW(),NOW()
FROM "__PREFIX__admin_rules" AS m
WHERE m."name"='plugin/source' AND m."type"='menu'
  AND NOT EXISTS (SELECT 1 FROM "__PREFIX__admin_rules" WHERE "name"='plugin/source/delete' AND "type"='node');
INSERT INTO "__PREFIX__admin_rules" ("pid","type","title","name","path","icon","open_type","url","component","keepalive","extend","remark","weigh","status","updated_at","created_at")
SELECT m."id",'node','测试','plugin/source/test','','','','','',1,'','',0,1,NOW(),NOW()
FROM "__PREFIX__admin_rules" AS m
WHERE m."name"='plugin/source' AND m."type"='menu'
  AND NOT EXISTS (SELECT 1 FROM "__PREFIX__admin_rules" WHERE "name"='plugin/source/test' AND "type"='node');

-- 清单页节点（list 回落 read；sync/install/preflight 精确匹配）
INSERT INTO "__PREFIX__admin_rules" ("pid","type","title","name","path","icon","open_type","url","component","keepalive","extend","remark","weigh","status","updated_at","created_at")
SELECT m."id",'node','查看','plugin/catalog/read','','','','','',1,'','',0,1,NOW(),NOW()
FROM "__PREFIX__admin_rules" AS m
WHERE m."name"='plugin/catalog' AND m."type"='menu'
  AND NOT EXISTS (SELECT 1 FROM "__PREFIX__admin_rules" WHERE "name"='plugin/catalog/read' AND "type"='node');
INSERT INTO "__PREFIX__admin_rules" ("pid","type","title","name","path","icon","open_type","url","component","keepalive","extend","remark","weigh","status","updated_at","created_at")
SELECT m."id",'node','同步清单','plugin/catalog/sync','','','','','',1,'','',0,1,NOW(),NOW()
FROM "__PREFIX__admin_rules" AS m
WHERE m."name"='plugin/catalog' AND m."type"='menu'
  AND NOT EXISTS (SELECT 1 FROM "__PREFIX__admin_rules" WHERE "name"='plugin/catalog/sync' AND "type"='node');
INSERT INTO "__PREFIX__admin_rules" ("pid","type","title","name","path","icon","open_type","url","component","keepalive","extend","remark","weigh","status","updated_at","created_at")
SELECT m."id",'node','安装','plugin/catalog/install','','','','','',1,'','',0,1,NOW(),NOW()
FROM "__PREFIX__admin_rules" AS m
WHERE m."name"='plugin/catalog' AND m."type"='menu'
  AND NOT EXISTS (SELECT 1 FROM "__PREFIX__admin_rules" WHERE "name"='plugin/catalog/install' AND "type"='node');
INSERT INTO "__PREFIX__admin_rules" ("pid","type","title","name","path","icon","open_type","url","component","keepalive","extend","remark","weigh","status","updated_at","created_at")
SELECT m."id",'node','预演','plugin/catalog/preflight','','','','','',1,'','',0,1,NOW(),NOW()
FROM "__PREFIX__admin_rules" AS m
WHERE m."name"='plugin/catalog' AND m."type"='menu'
  AND NOT EXISTS (SELECT 1 FROM "__PREFIX__admin_rules" WHERE "name"='plugin/catalog/preflight' AND "type"='node');
