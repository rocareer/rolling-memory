-- ===== 插件管理页菜单行与节点挂靠的回滚（与 up 逆向：先复位挂靠、再删菜单行）=====
-- 3 个管理面节点本身是 000004 的种子（本迁移只动它们的 pid）⇒ 这里只把 pid 复位为 NULL，不删节点行；
-- 顺序：复位 → 删菜单（与 up 的「建菜单 → 挂靠」逆向）。
-- admin_groups.rules 的 CSV ID 不清洗（挂靠不改 ID）。

UPDATE "__PREFIX__admin_rules" AS n
SET "pid" = NULL,
    "updated_at" = NOW()
FROM "__PREFIX__admin_rules" AS m
WHERE m."name" = 'plugin'
  AND m."type" = 'menu'
  AND n."name" IN ('plugin/read', 'plugin/enable', 'plugin/disable')
  AND n."type" = 'node'
  AND n."pid" = m."id";

DELETE FROM "__PREFIX__admin_rules"
WHERE "name" = 'plugin'
  AND "type" = 'menu';