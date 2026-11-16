-- ===== 插件机制宿主基座迁移回滚（本迁移的完整逆向）=====
-- 顺序：管理面节点 → admin_rules 归属列与部分索引 → 三表（与 up 逆向；索引与 CHECK 随表/列一并消失）。
-- admin_groups.rules 的 CSV ID 不清洗（授权按 join 匹配，失效 ID 自然忽略）。
-- 插件自管迁移版本表（<前缀>plugin_<名>_migrations）不属本迁移：其回滚与 drop 由 CLI 承担（uninstall --purge）。

DELETE FROM "__PREFIX__admin_rules"
WHERE "name" IN ('plugin/read', 'plugin/enable', 'plugin/disable')
  AND "type" = 'node';

DROP INDEX IF EXISTS "__PREFIX__idx_admin_rules_plugin";
ALTER TABLE "__PREFIX__admin_rules" DROP COLUMN IF EXISTS "plugin";

DROP TABLE IF EXISTS "__PREFIX__admin_plugin_actions";
DROP TABLE IF EXISTS "__PREFIX__admin_plugin_entities";
DROP TABLE IF EXISTS "__PREFIX__admin_plugins";
