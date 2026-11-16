-- ===== 插件来源配置 + 清单快照 + 插件中心菜单/节点的回滚（与 up 逆向）=====
-- 顺序：先删节点、再删菜单、复位 plugin 菜单、处理 dir 下未知子菜单、删 dir、最后删表。
-- 所有删除都限定 plugin=''：绝不删除插件归属行（即使 name 巧合相同）。
-- 不清洗 admin_groups.rules 的 ID（与既有插件迁移 down 同口径，失效 ID 由 join 自然忽略）。

DELETE FROM "__PREFIX__admin_rules"
WHERE "type" = 'node' AND "plugin" = '' AND COALESCE("path",'') = '' AND COALESCE("component",'') = ''
  AND "name" IN (
    'plugin/source/read','plugin/source/create','plugin/source/update','plugin/source/delete','plugin/source/test',
    'plugin/catalog/read','plugin/catalog/sync','plugin/catalog/install','plugin/catalog/preflight'
  );

DELETE FROM "__PREFIX__admin_rules"
WHERE "type" = 'menu' AND "plugin" = ''
  AND (("name" = 'plugin/source' AND "path" = 'plugin/source' AND "component" = '/src/views/admin/plugin/source/index.vue')
    OR ("name" = 'plugin/catalog' AND "path" = 'plugin/catalog' AND "component" = '/src/views/admin/plugin/catalog/index.vue'));

UPDATE "__PREFIX__admin_rules" AS p
SET "pid" = NULL, "weigh" = 989, "updated_at" = NOW()
FROM "__PREFIX__admin_rules" AS d
WHERE d."name" = 'plugin-center' AND d."type" = 'dir' AND COALESCE(d."component",'') = '' AND d."plugin" = ''
  AND p."name" = 'plugin' AND p."type" = 'menu' AND p."path" = 'plugin'
  AND p."component" = '/src/views/admin/plugin/index.vue' AND p."plugin" = '' AND p."pid" = d."id";

-- 先把 dir 下所有宿主子行（含管理员手工新增/形状不符的同名行）pid 置 NULL，避免删 dir 后留孤儿；不删这些行。
UPDATE "__PREFIX__admin_rules" AS c
SET "pid" = NULL, "updated_at" = NOW()
FROM "__PREFIX__admin_rules" AS d
WHERE d."name" = 'plugin-center' AND d."type" = 'dir' AND d."plugin" = ''
  AND c."pid" = d."id" AND c."plugin" = '';

DELETE FROM "__PREFIX__admin_rules"
WHERE "name" = 'plugin-center' AND "type" = 'dir' AND "path" = 'plugin-center'
  AND COALESCE("component",'') = '' AND "plugin" = '';

DROP TABLE IF EXISTS "__PREFIX__admin_plugin_catalog_entries";
DROP TABLE IF EXISTS "__PREFIX__admin_plugin_sources";
