-- ===== 存量库迁移基线（adopt）：admin_plugins 增列 adopted_through =====
-- 编号口径：按现有最大编号 +1 重取。
-- 语义：> 0 = 该插件以「插件迁移 1..该版本与**核心链**对应迁移同物」的基线口径装入——安装期只把插件版本表
-- （<前缀>plugin_<名>_migrations）写到该版本、**未执行任何旧 DDL**；0 = 普通装入（全量/增量应用插件链）。
-- 为什么必须落台账：`uninstall --purge` 的门禁要在卸载那一刻判一个**状态事实**（adopted 装入的插件其 down
-- 文件是核心链迁移的副本 ⇒ 回滚删的是核心链建的表与数据，而核心游标仍记已应用、`aigo migrate up` 不会重建）；
-- 审计是留痕不是状态，且卸载会删台账行。
-- 幂等：ADD COLUMN IF NOT EXISTS + COMMENT（低号迁移在已推进的库上永不执行，本文件因此必须可重复应用）。
ALTER TABLE "__PREFIX__admin_plugins"
    ADD COLUMN IF NOT EXISTS "adopted_through" integer NOT NULL DEFAULT 0;
COMMENT ON COLUMN "__PREFIX__admin_plugins"."adopted_through"
    IS '接管段(>0=adopt装入,1..该版本与核心链同物;安装期未执行任何旧DDL);--purge 对 >0 默认拒绝';

-- ===== CHECK 断言（迁移自检：列与默认值就位；供 aigo migrate up 的断言面核对）=====
-- 断言：列存在、类型 integer、NOT NULL、默认 0。
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_name = '__PREFIX__admin_plugins'
          AND column_name = 'adopted_through'
          AND data_type = 'integer'
          AND is_nullable = 'NO'
          AND column_default = '0'
    ) THEN
        RAISE EXCEPTION 'CHECK 断言失败：__PREFIX__admin_plugins.adopted_through 未按 (integer NOT NULL DEFAULT 0) 就位';
    END IF;
END $$;
