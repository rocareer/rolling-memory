-- plugin: ai v0.2.0
-- 迁移 2：usage 审计面加「部分计费」标记列 billed_partial。
--
-- 为什么用真列而不是复用 error_code 前缀：分支③（流式「有块、无收尾」）现在会按已聚合内容
-- 部分计费（settleStreamFailure 的补偿分支），这一行的审计语义与「完全没计费」的失败行必须可区分、
-- 可筛选、可聚合，且要经 usage 接口（Usage 结构体直接序列化）回显。真列是实现该语义的最小改动面；
-- 复用 error_code 前缀会把「错误码」与「计费口径」两件事混在一列里。
--
-- 口径：1=该行是流式部分计费行（上游已产出内容但未收尾，tokens/cost 由 ResolveStreamUsage 补偿而来，
-- status 仍是 fail|timeout）；0=正常行（成功行、无块失败行、非流式失败行）。
-- 与旧数据的兼容：DEFAULT 0 ⇒ 存量行（迁移前写入）语义正确（它们都不是部分计费行），无需回填。
--
-- 标识符纪律与本插件 1_init 同物：表名 / 约束名一律 "__PREFIX__" 占位符 + 双引号标识符；
-- 整文件一次执行、单个隐式事务 ⇒ 禁 CREATE INDEX CONCURRENTLY 与文件内 BEGIN / COMMIT；
-- 也不得写宿主菜单 / 角色组表（ADMIN_RULES / ADMIN_GROUPS）。
--
-- 幂等：ADD COLUMN IF NOT EXISTS 与 DO 块内的约束探测都用对象事实判定（与 1_init 的
-- CREATE TABLE / CONSTRAINT ... IF NOT EXISTS 口径一致），重复执行不报错、不产生第二个约束。

ALTER TABLE "__PREFIX__plugin_ai_usage"
    ADD COLUMN IF NOT EXISTS "billed_partial" smallint NOT NULL DEFAULT 0;

-- CHECK 的 ADD CONSTRAINT 在 PostgreSQL 没有 IF NOT EXISTS ⇒ 用 DO 块按 pg_constraint 存在性判定
-- （约束名显式带前缀，便于卸载/回滚与识别归属）。
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
          FROM pg_constraint
         WHERE conname = '__PREFIX__ck_plugin_ai_usage_billed_partial'
           AND conrelid = to_regclass('"__PREFIX__plugin_ai_usage"')
    ) THEN
        ALTER TABLE "__PREFIX__plugin_ai_usage"
            ADD CONSTRAINT "__PREFIX__ck_plugin_ai_usage_billed_partial"
            CHECK ("billed_partial" IN (0, 1));
    END IF;
END
$$;

COMMENT ON COLUMN "__PREFIX__plugin_ai_usage"."billed_partial" IS
    '部分计费标记:1=流式「有块、无收尾」的补偿计费行（tokens/cost 按已聚合内容补偿，status 仍为 fail|timeout），0=正常行';