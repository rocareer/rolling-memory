-- plugin: ai v0.2.0
-- 迁移 6：语音合成（TTS）——① 新建音色表 plugin_ai_voice；② 把 plugin_ai_model.type 与
-- plugin_ai_usage.call_type 的闭集 CHECK 扩为含 'tts'（DROP 旧约束 + ADD 新约束，约束名沿用
-- 1_init.up.sql 的既有名，PG 的 CHECK 只能重建、没有 ADD VALUE 这类写法）。
--
-- 口径：
--   - 音色行 = 管理面的一条可试听配置：name（唯一，管理标识）/ model（**逻辑模型名**，调用期由
--     requireModel 收口为必须 type='tts'）/ voice（**上游音色 ID**，如 longxiaochun_v2）/
--     format（音频格式闭集）/ status / remark；引用完整性不做外键（与 1_init 同口径：
--     「表结构不使用外键」，删除与校验都在应用层）。
--   - 源的对照物是 database/migrations/20260909155604_radmin_ai_voice.php：那边只做枚举扩展
--     （PG 用 ALTER TYPE ... ADD VALUE 追加 model.type 的 'tts' 与 usage.call_type 的 'tts'/'asr'），
--     音色表落在业务包。本插件把闭集扩到 'tts'，**不含 'asr'**（本插件 v1 没有转写入口，
--     差异汇总进 README）。源的 down 段明确「不回滚：枚举归接入方维护」；本插件的 down
--     按包验收要求**真的恢复旧 CHECK**（见 6_voice.down.sql）。
--
-- 整文件一次执行、单个隐式事务 ⇒ 禁 BEGIN/COMMIT、禁 CREATE INDEX CONCURRENTLY；
-- 标识符一律 "__PREFIX__" 占位符 + 双引号（安装器按配置表前缀整文件替换）；
-- 列名不拼前缀。前缀需 ≤ 31 字节（最长索引主干名 32 字符，32 + 前缀 ≤ PG 标识符 63 字节上限）。

-- ===== ① 音色表：管理面配置 + 试听（试听即真实合成，不落盘，见 handler_voice.go） =====
CREATE TABLE IF NOT EXISTS "__PREFIX__plugin_ai_voice" (
    "id"          bigserial    PRIMARY KEY,
    "name"        varchar(50)  NOT NULL DEFAULT '',
    "model"       varchar(100) NOT NULL DEFAULT '',
    "voice"       varchar(60)  NOT NULL DEFAULT '',
    "format"      varchar(10)  NOT NULL DEFAULT 'mp3',
    "status"      smallint     NOT NULL DEFAULT 1,
    "remark"      varchar(255) NOT NULL DEFAULT '',
    "create_time" bigint       NOT NULL DEFAULT 0,
    "update_time" bigint       NOT NULL DEFAULT 0,
    CONSTRAINT "__PREFIX__ck_plugin_ai_voice_status" CHECK ("status" IN (0, 1)),
    CONSTRAINT "__PREFIX__ck_plugin_ai_voice_format" CHECK ("format" IN ('mp3', 'wav', 'pcm', 'opus'))
);
-- 音色名唯一（重名由应用层报可读错误，照渠道/模型的 name 口径）
CREATE UNIQUE INDEX IF NOT EXISTS "__PREFIX__uk_plugin_ai_voice_name"
    ON "__PREFIX__plugin_ai_voice" ("name");
-- 按逻辑模型筛音色（列表顶层筛选 model）
CREATE INDEX IF NOT EXISTS "__PREFIX__idx_plugin_ai_voice_model"
    ON "__PREFIX__plugin_ai_voice" ("model");
CREATE INDEX IF NOT EXISTS "__PREFIX__idx_plugin_ai_voice_status"
    ON "__PREFIX__plugin_ai_voice" ("status");
COMMENT ON TABLE "__PREFIX__plugin_ai_voice" IS 'AI 语音音色：逻辑模型 × 上游音色 ID 的一条可试听配置';
COMMENT ON COLUMN "__PREFIX__plugin_ai_voice"."id" IS 'ID';
COMMENT ON COLUMN "__PREFIX__plugin_ai_voice"."name" IS '音色名（唯一；重名由应用层报可读错误）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_voice"."model" IS '逻辑模型名（须指向 type=tts 的 plugin_ai_model；引用完整性在调用期由 requireModel 收口）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_voice"."voice" IS '上游音色 ID（写入合成请求 input.voice，如 longxiaochun_v2）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_voice"."format" IS '音频格式:mp3|wav|pcm|opus（写入合成请求 input.format）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_voice"."status" IS '状态:1=启用,0=禁用（v1 只作展示与筛选，调用期不校验——与源音色页口径一致）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_voice"."remark" IS '备注';
COMMENT ON COLUMN "__PREFIX__plugin_ai_voice"."create_time" IS '创建时间（UTC 秒）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_voice"."update_time" IS '更新时间（UTC 秒）';

-- ===== ② 闭集扩 'tts'：逻辑模型类型 =====
ALTER TABLE "__PREFIX__plugin_ai_model"
    DROP CONSTRAINT IF EXISTS "__PREFIX__ck_plugin_ai_model_type";
ALTER TABLE "__PREFIX__plugin_ai_model"
    ADD CONSTRAINT "__PREFIX__ck_plugin_ai_model_type" CHECK ("type" IN ('chat', 'embedding', 'tts'));
COMMENT ON COLUMN "__PREFIX__plugin_ai_model"."type" IS '调用类型:chat|embedding|tts（tts=语音合成）';

-- ===== ③ 闭集扩 'tts'：用量流水的调用类型 =====
ALTER TABLE "__PREFIX__plugin_ai_usage"
    DROP CONSTRAINT IF EXISTS "__PREFIX__ck_plugin_ai_usage_call_type";
ALTER TABLE "__PREFIX__plugin_ai_usage"
    ADD CONSTRAINT "__PREFIX__ck_plugin_ai_usage_call_type" CHECK ("call_type" IN ('chat', 'embedding', 'tts'));
COMMENT ON COLUMN "__PREFIX__plugin_ai_usage"."call_type" IS '调用类型:chat|embedding|tts（tts 的 prompt_tokens 记的是**字符数**，见 service_tts.go）';