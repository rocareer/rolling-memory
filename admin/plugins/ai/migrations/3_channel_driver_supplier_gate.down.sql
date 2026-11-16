-- plugin: ai v0.2.0
-- 迁移 3 的回滚：按 up 的**逆序**撤销（先约束，再索引/四列，最后厂商表）。
-- 与 1_init.down.sql / 2_usage_partial_billing.down.sql 同口径：只删本迁移建出的对象，
-- 不触碰迁移 1 的 5 张表及其索引/约束（渠道表本身保留，只去掉本迁移加的四列）。
-- 全部走 IF EXISTS（对象不存在即跳过），重复执行不报错；DROP COLUMN 会连带删掉列上的 COMMENT。

-- ③ 非负约束
ALTER TABLE IF EXISTS "__PREFIX__plugin_ai_channel"
    DROP CONSTRAINT IF EXISTS "__PREFIX__ck_plugin_ai_channel_gate_nums";

-- ② supplier_id 索引与四列（DROP COLUMN 自动带走该列上的 COMMENT 与依赖约束）
DROP INDEX IF EXISTS "__PREFIX__idx_plugin_ai_channel_supplier";
ALTER TABLE IF EXISTS "__PREFIX__plugin_ai_channel" DROP COLUMN IF EXISTS "tpm_limit";
ALTER TABLE IF EXISTS "__PREFIX__plugin_ai_channel" DROP COLUMN IF EXISTS "rpm_limit";
ALTER TABLE IF EXISTS "__PREFIX__plugin_ai_channel" DROP COLUMN IF EXISTS "supplier_id";
ALTER TABLE IF EXISTS "__PREFIX__plugin_ai_channel" DROP COLUMN IF EXISTS "driver";

-- ① 厂商表（先删它，渠道的 supplier_id 已在上一步删掉，不留悬空引用）
DROP TABLE IF EXISTS "__PREFIX__plugin_ai_supplier";