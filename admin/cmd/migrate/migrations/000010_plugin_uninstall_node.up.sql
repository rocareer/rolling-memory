-- ===== 插件管理面「卸载」权限节点（宿主所有；详情抽屉的卸载入口 = web/src/views/admin/plugin/index.vue）=====
-- 落点口径：管理面节点一律不授予任何角色组 ⇒ 默认只有超管可见可用；中间件按 FullPath 推节点名，
-- 精确匹配 plugin/uninstall。「插件详情面板需要停用/卸载功能」即本节点存在的目的。
--
-- 与 000004 的 3 个节点同款：type=node、path/component 空、keepalive=1、weigh=0、status=1；
-- 挂靠沿用 000005 的 UPDATE … IS DISTINCT FROM 幂等写法（菜单行在则挂，不在则保持 pid=NULL——
-- 000005 负责菜单行的存在性，本文件不新建菜单）。
--
-- 追加式：不改既有迁移的字节（迁移应用后冻结；修正走新迁移）。
INSERT INTO "__PREFIX__admin_rules" ("pid", "type", "title", "name", "path", "icon", "open_type", "url", "component", "keepalive", "extend", "remark", "weigh", "status", "updated_at", "created_at")
SELECT NULL, 'node', '卸载', 'plugin/uninstall', '', '', '', '', '', 1, '', '插件卸载：环境门禁（生产禁止）+ 先禁用后卸载硬门禁 + --purge 二次确认', 0, 1, NOW(), NOW()
WHERE NOT EXISTS (SELECT 1 FROM "__PREFIX__admin_rules" WHERE "name" = 'plugin/uninstall' AND "type" = 'node');

UPDATE "__PREFIX__admin_rules" AS n
SET "pid" = m."id",
    "updated_at" = NOW()
FROM "__PREFIX__admin_rules" AS m
WHERE m."name" = 'plugin'
  AND m."type" = 'menu'
  AND n."name" = 'plugin/uninstall'
  AND n."type" = 'node'
  AND n."pid" IS DISTINCT FROM m."id";