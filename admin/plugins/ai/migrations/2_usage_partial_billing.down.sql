-- plugin: ai v0.2.0
-- 迁移 2 的回滚：逆序撤销 up 的两步（先约束、再列；列上的 COMMENT 随列删除自动消失）。
-- 与 1_init.down.sql 同口径：只删本迁移建出的对象，不触碰迁移 1 的表/索引/约束。
-- 全部走 IF EXISTS（对象不存在即跳过），重复执行不报错。

ALTER TABLE IF EXISTS "__PREFIX__plugin_ai_usage"
    DROP CONSTRAINT IF EXISTS "__PREFIX__ck_plugin_ai_usage_billed_partial";

ALTER TABLE IF EXISTS "__PREFIX__plugin_ai_usage"
    DROP COLUMN IF EXISTS "billed_partial";