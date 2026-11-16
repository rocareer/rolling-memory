-- ===== 回滚「卸载」权限节点（与 up 逆向：节点随挂靠一并删除，无需先复位 pid）=====
-- 该节点默认不授予任何角色组（up 语义）；若被手工授予过，本回滚不含 admin_groups.rules 的 CSV 清洗
-- （迁移回滚的既有口径：ID 清洗由卸载/规则的运行期逻辑承担，不靠迁移 DELETE 猜）。
DELETE FROM "__PREFIX__admin_rules"
WHERE "name" = 'plugin/uninstall'
  AND "type" = 'node';