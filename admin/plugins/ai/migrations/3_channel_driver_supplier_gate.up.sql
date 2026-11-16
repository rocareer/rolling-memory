-- plugin: ai v0.2.0
-- 迁移 3：三级配置的中间两级（厂商表 + 渠道挂厂商）与渠道级配额/驱动列。
--
-- 三件事，一件都不多：
--   ① 建 plugin_ai_supplier（厂商：共享凭证 base_url/api_key/driver/timeout + 展示位 vendor/name）；
--   ② 给 plugin_ai_channel 加 driver / supplier_id / rpm_limit / tpm_limit 四列与 supplier_id 索引；
--   ③ 给四列的新数值口径补一条非负 CHECK（supplier_id/rpm_limit/tpm_limit）。
--
-- 口径来源（只读对照，不搬代码）：
--   - 厂商表列面照 database/migrations/20260828090852_radmin_ai_supplier.php:25-45（name/vendor/
--     base_url/api_key/driver/timeout/status/remark/时间戳）；
--   - 继承语义照 src/app/ai/service/AiRouterService.php:1274-1316 与 src/app/ai/support/ChannelProbe.php:132-153
--     （渠道非空优先、空则取厂商、再无则回落插件缺省；Go 侧唯一实现 = store.go 的 mergeSupplierInheritance）；
--   - 渠道 rpm/tpm 在源侧走 settings JSON（extra.max_rpm / channel_gate.rates，缺省 1000rpm），
--     本插件改成**真列**（可索引、可校验、可回显），缺省 0=不限（差异已登记进 README 的差异节）。
--
-- 刻意不做（与源的差异，逐条有出处）：
--   - 不建 supplier.settings / channel.settings（源 20260927152337_radmin_ai_channel_settings.php）：
--     v1 无 settings 透传面，驱动侧以静态 modelMap 替代（scopeOut 明列）；
--   - driver 列宽 255（与源一致；本插件的 driver 值是注册表名而不是 PHP 类 FQN，但列宽与源、
--     与 supplier 行模型同口径；接口层另有一道更严的形状校验，见 handler_channel.go 的
--     normalizeDriverName）；
--   - supplier.name 不建唯一索引（与源一致）：重名不阻断创建/继承（引用一律按 supplier_id）；
--     渠道名唯一是渠道表自己的口径（1_init 的 uk_plugin_ai_channel_name）。
--
-- 标识符纪律与迁移 1/2 同物：表名 / 索引名 / 约束名一律 "__PREFIX__" 占位符 + 双引号标识符；
-- 列名不拼前缀。整文件一次执行、单个隐式事务 ⇒ 禁 CREATE INDEX CONCURRENTLY 与文件内 BEGIN / COMMIT；
-- 也不得写宿主菜单 / 角色组表（ADMIN_RULES / ADMIN_GROUPS）。
--
-- 幂等（可重复应用）：CREATE TABLE / CREATE INDEX 走 IF NOT EXISTS；
-- ADD COLUMN 走 IF NOT EXISTS；ADD CONSTRAINT 在 PostgreSQL 没有 IF NOT EXISTS ⇒ 用 DO 块按
-- pg_constraint 的对象事实判定（与迁移 2 的 ck_plugin_ai_usage_billed_partial 同款写法）。
-- 列已存在时**不校验既有列的定义**（列宽/默认值漂移属安装器的 down→up 闭环，不在本文件的判据面）。

-- ===== ① 厂商：三级配置顶层，持有可被渠道继承的共享凭证 =====
CREATE TABLE IF NOT EXISTS "__PREFIX__plugin_ai_supplier" (
    "id"          bigserial    PRIMARY KEY,
    "name"        varchar(50)  NOT NULL DEFAULT '',
    "vendor"      varchar(30)  NOT NULL DEFAULT '',
    "base_url"    varchar(255) NOT NULL DEFAULT '',
    "api_key"     varchar(255) NOT NULL DEFAULT '',
    "driver"      varchar(255) NOT NULL DEFAULT '',
    "timeout"     int          NOT NULL DEFAULT 60,
    "status"      smallint     NOT NULL DEFAULT 1,
    "remark"      varchar(255) NOT NULL DEFAULT '',
    "create_time" bigint       NOT NULL DEFAULT 0,
    "update_time" bigint       NOT NULL DEFAULT 0,
    CONSTRAINT "__PREFIX__ck_plugin_ai_supplier_status" CHECK ("status" IN (0, 1)),
    CONSTRAINT "__PREFIX__ck_plugin_ai_supplier_nums" CHECK ("timeout" BETWEEN 0 AND 600)
);
-- 列表按状态筛选（厂商页的顶层 status 条件）；name 不建唯一索引，理由见文件头注。
CREATE INDEX IF NOT EXISTS "__PREFIX__idx_plugin_ai_supplier_status"
    ON "__PREFIX__plugin_ai_supplier" ("status");
COMMENT ON TABLE "__PREFIX__plugin_ai_supplier" IS 'AI 厂商：三级配置顶层，持有可被渠道继承的共享凭证（base_url/api_key/driver/timeout）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_supplier"."id" IS 'ID（渠道用 supplier_id 引用；0=未挂厂商）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_supplier"."name" IS '厂商名（可重名：引用一律按 ID，与源一致）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_supplier"."vendor" IS '厂商标识:openai|deepseek|qwen|moonshot|zhipu|ollama|other（展示与筛选项）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_supplier"."base_url" IS '默认 API 端点（渠道该项为空时继承；连接期过 safedial 内网护栏）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_supplier"."api_key" IS '默认上游凭证（渠道该项为空时继承；明文入库、出口 mask_secret 脱敏、不进日志）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_supplier"."driver" IS '默认驱动名（渠道该项为空时继承；空=默认 OpenAI 兼容协议）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_supplier"."timeout" IS '默认请求超时秒数（渠道该项为 0 时继承；探活另按 clamp(此值,3,15)）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_supplier"."status" IS '状态:1=启用,0=禁用（**不参与继承判定**：继承只认字段非空，与源一致）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_supplier"."remark" IS '备注';
COMMENT ON COLUMN "__PREFIX__plugin_ai_supplier"."create_time" IS '创建时间（UTC 秒）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_supplier"."update_time" IS '更新时间（UTC 秒）';

-- ===== ② 渠道四列：驱动名 + 所属厂商 + 渠道级 RPM/TPM 配额 =====
ALTER TABLE "__PREFIX__plugin_ai_channel"
    ADD COLUMN IF NOT EXISTS "driver" varchar(255) NOT NULL DEFAULT '';
ALTER TABLE "__PREFIX__plugin_ai_channel"
    ADD COLUMN IF NOT EXISTS "supplier_id" bigint NOT NULL DEFAULT 0;
ALTER TABLE "__PREFIX__plugin_ai_channel"
    ADD COLUMN IF NOT EXISTS "rpm_limit" int NOT NULL DEFAULT 0;
ALTER TABLE "__PREFIX__plugin_ai_channel"
    ADD COLUMN IF NOT EXISTS "tpm_limit" int NOT NULL DEFAULT 0;

-- supplier_id 上的索引：厂商页的 channel_count（count(*) where supplier_id=?）与继承 JOIN 都走它。
CREATE INDEX IF NOT EXISTS "__PREFIX__idx_plugin_ai_channel_supplier"
    ON "__PREFIX__plugin_ai_channel" ("supplier_id");

-- ===== ③ 四列的非负约束（接口层已 clamp，这里钉住「绕开接口也写不歪」）=====
-- 幂等：ADD CONSTRAINT 无 IF NOT EXISTS ⇒ 按 pg_constraint 的存在性判定（同迁移 2 的 DO 块口径）。
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
          FROM pg_constraint
         WHERE conname = '__PREFIX__ck_plugin_ai_channel_gate_nums'
           AND conrelid = to_regclass('"__PREFIX__plugin_ai_channel"')
    ) THEN
        ALTER TABLE "__PREFIX__plugin_ai_channel"
            ADD CONSTRAINT "__PREFIX__ck_plugin_ai_channel_gate_nums"
            CHECK ("supplier_id" >= 0 AND "rpm_limit" >= 0 AND "tpm_limit" >= 0);
    END IF;
END
$$;

COMMENT ON COLUMN "__PREFIX__plugin_ai_channel"."driver" IS '驱动名（驱动注册表按它分派）:空=默认 OpenAI 兼容协议；渠道自身为空时继承所挂厂商';
COMMENT ON COLUMN "__PREFIX__plugin_ai_channel"."supplier_id" IS '所属厂商 ID:0=独立渠道（不继承）；悬空 ID 不报错、只是无从继承';
COMMENT ON COLUMN "__PREFIX__plugin_ai_channel"."rpm_limit" IS '渠道级 RPM 配额（次/分钟）:0=未配⇒由插件缺省 channel_gate_rpm_default 兜底，仍为 0 即不限';
COMMENT ON COLUMN "__PREFIX__plugin_ai_channel"."tpm_limit" IS '渠道级 TPM 配额（token/分钟）:0=未配⇒由插件缺省 channel_gate_tpm_default 兜底，仍为 0 即不限';