-- plugin: ai v0.2.0
-- 4_api_key：应用接入 API Key（对外调用方凭证 + 计量归因维度）。
--
-- 对照源：database/migrations/20260908150000_radmin_ai_api_key.php:28-45（同表同列 + uk(key_hash)
-- 与 uk(source) WHERE source <> ''）；差异（如实登记，见 apikey.go 头注与本包 risks）：
--   - source 列取 varchar(24)（源 64）：本插件的计量口径是 plugin_ai_usage.source（列宽 30），
--     网关侧写入 'key:'+source ⇒ source 必须 ≤24 才不会被 usage 写口的列宽兜底截断（store.go:1080-1089）；
--   - status 取 smallint 0/1（源 'enabled'|'disabled'）：与本插件渠道/模型表的二值状态列同口径，
--     也让宿主前端的 switch 控件与薄插件的 validStatus 校验直接复用；
--   - 多一条 key_hash 的形状约束（^[0-9a-f]{64}$）：把「库里只存 sha256 十六进制」从纪律变成库级事实。
--
-- 整文件一次执行、单个隐式事务 ⇒ 禁止 CREATE INDEX CONCURRENTLY 与文件内 BEGIN / COMMIT；
-- 标识符纪律同 1_init.up.sql：表名/索引名/约束名一律 "__PREFIX__" 占位符 + 双引号标识符。

CREATE TABLE IF NOT EXISTS "__PREFIX__plugin_ai_api_key" (
    "id"           bigserial    PRIMARY KEY,
    "name"         varchar(50)  NOT NULL DEFAULT '',
    "key_hash"     varchar(64)  NOT NULL,
    "key_prefix"   varchar(24)  NOT NULL DEFAULT '',
    "source"       varchar(24)  NOT NULL DEFAULT '',
    "model_scope"  jsonb        NOT NULL DEFAULT '[]'::jsonb,
    "rate_limit"   int          NOT NULL DEFAULT 0,
    "status"       smallint     NOT NULL DEFAULT 1,
    "expire_at"    bigint       NULL,
    "last_used_at" bigint       NULL,
    "remark"       varchar(255) NOT NULL DEFAULT '',
    "create_time"  bigint       NOT NULL DEFAULT 0,
    "update_time"  bigint       NOT NULL DEFAULT 0,
    CONSTRAINT "__PREFIX__ck_plugin_ai_api_key_status" CHECK ("status" IN (0, 1)),
    CONSTRAINT "__PREFIX__ck_plugin_ai_api_key_key_hash" CHECK ("key_hash" ~ '^[0-9a-f]{64}$'),
    CONSTRAINT "__PREFIX__ck_plugin_ai_api_key_rate_limit" CHECK ("rate_limit" >= 0),
    CONSTRAINT "__PREFIX__ck_plugin_ai_api_key_expire_at" CHECK ("expire_at" IS NULL OR "expire_at" >= 0),
    CONSTRAINT "__PREFIX__ck_plugin_ai_api_key_last_used_at" CHECK ("last_used_at" IS NULL OR "last_used_at" >= 0)
);
-- 唯一键 1：key_hash（同一明文只可能对应一行；查库鉴权的唯一索引）
CREATE UNIQUE INDEX IF NOT EXISTS "__PREFIX__uk_plugin_ai_api_key_key_hash"
    ON "__PREFIX__plugin_ai_api_key" ("key_hash");
-- 唯一键 2：source 非空时唯一（一个调用方一个 key；空 source 允许重复——与源 uk_source 同口径）
CREATE UNIQUE INDEX IF NOT EXISTS "__PREFIX__uk_plugin_ai_api_key_source"
    ON "__PREFIX__plugin_ai_api_key" ("source") WHERE "source" <> '';
-- 列表按状态筛选（渠道表同款：状态列建索引，其余筛选走顺序扫描——表量级是人工维护的应用清单）
CREATE INDEX IF NOT EXISTS "__PREFIX__idx_plugin_ai_api_key_status"
    ON "__PREFIX__plugin_ai_api_key" ("status");

COMMENT ON TABLE "__PREFIX__plugin_ai_api_key" IS 'AI 应用接入 API Key（明文仅签发/重置回执一次，库中只存 sha256）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_api_key"."id" IS 'ID';
COMMENT ON COLUMN "__PREFIX__plugin_ai_api_key"."name" IS '应用名（唯一性不约束；重名只是展示问题）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_api_key"."key_hash" IS '明文 key 的 sha256 十六进制（64 字符；永不出口，列表/详情/日志都不返回）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_api_key"."key_prefix" IS '展示前缀（明文前 12 字符 + …；照源 substr($key,0,12)."…"）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_api_key"."source" IS '调用方标识（计费/用量统计维度；网关写入 usage.source = key:<source>，故 ≤24 字符）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_api_key"."model_scope" IS '模型白名单（JSONB 字符串数组；空数组 = 允许全部模型）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_api_key"."rate_limit" IS 'Key 级限流覆盖（次/分钟）:0=跟随插件全局；>0 时覆盖全局（对齐源 ApiKeyRateLimitMiddleware 的 rate_limit>0 优先）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_api_key"."status" IS '状态:1=启用,0=停用（停用后公开面鉴权直接 401）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_api_key"."expire_at" IS '过期时间（UTC 秒）:NULL/0=永不过期';
COMMENT ON COLUMN "__PREFIX__plugin_ai_api_key"."last_used_at" IS '最近使用时刻（UTC 秒）:每请求后台异步刷新（最终一致、允许丢更新；口径见 apikey_auth.go 头注）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_api_key"."remark" IS '备注';
COMMENT ON COLUMN "__PREFIX__plugin_ai_api_key"."create_time" IS '创建时间（UTC 秒）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_api_key"."update_time" IS '更新时间（UTC 秒）';