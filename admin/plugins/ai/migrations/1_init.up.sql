-- plugin: ai v0.2.0
-- AI 路由插件的自管迁移（编号在插件内独立自增，走独立版本表 <前缀>plugin_ai_migrations）。
-- 整文件一次执行、单个隐式事务 ⇒ 禁止 CREATE INDEX CONCURRENTLY 与文件内 BEGIN / COMMIT；
-- 也不得写宿主菜单/角色组表（ADMIN_RULES / ADMIN_GROUPS）。
--
-- 标识符纪律：表名 / 索引名 / 约束名一律 "__PREFIX__" 占位符 + 双引号标识符（安装器按配置表前缀整文件替换）；
-- 列名不拼前缀。口径实例见 cmd/migrate/migrations/000002_common.up.sql:51（idx_configs_name）与
-- 000004_plugin.up.sql:30-31（ck_admin_plugins_kind）；前缀需 ≤ 31 字节（最长索引主干名 32 字符，
-- 32 + 前缀 ≤ PostgreSQL 标识符 63 字节上限）。
--
-- 5 张表：渠道（含熔断状态机与累计统计）、逻辑模型、模型×渠道绑定、每次真实上游尝试一行的用量流水、
-- 熔断事件流水。表结构不使用外键：删除渠道/模型时绑定在应用事务内先删（store.DeleteChannels /
-- store.DeleteModels），与源侧口径一致；不建无用索引（见各表注释）。

-- ===== ① 渠道：一个 OpenAI 兼容端点 + 熔断状态机 + 累计统计 + 探活快照 =====
CREATE TABLE IF NOT EXISTS "__PREFIX__plugin_ai_channel" (
    "id"                   bigserial    PRIMARY KEY,
    "name"                 varchar(50)  NOT NULL DEFAULT '',
    "vendor"               varchar(30)  NOT NULL DEFAULT '',
    "base_url"             varchar(255) NOT NULL DEFAULT '',
    "api_key"              varchar(255) NOT NULL DEFAULT '',
    "model"                varchar(100) NOT NULL DEFAULT '',
    "timeout"              int          NOT NULL DEFAULT 60,
    "priority"             int          NOT NULL DEFAULT 1,
    "weight"               int          NOT NULL DEFAULT 1,
    "status"               smallint     NOT NULL DEFAULT 1,
    "fail_threshold"       int          NOT NULL DEFAULT 0,
    "cooldown_seconds"     int          NOT NULL DEFAULT 0,
    "circuit"              varchar(16)  NOT NULL DEFAULT 'closed',
    "consecutive_failures" int          NOT NULL DEFAULT 0,
    "opened_at"            bigint       NULL,
    "total_calls"          bigint       NOT NULL DEFAULT 0,
    "total_failures"       bigint       NOT NULL DEFAULT 0,
    "total_tokens"         bigint       NOT NULL DEFAULT 0,
    "last_success_at"      bigint       NULL,
    "last_error"           varchar(500) NOT NULL DEFAULT '',
    "last_probe_at"        bigint       NULL,
    "last_probe_ok"        smallint     NULL,
    "last_probe_latency"   int          NULL,
    "last_probe_error"     varchar(500) NOT NULL DEFAULT '',
    "probe_fail_streak"    int          NOT NULL DEFAULT 0,
    "remark"               varchar(255) NOT NULL DEFAULT '',
    "create_time"          bigint       NOT NULL DEFAULT 0,
    "update_time"          bigint       NOT NULL DEFAULT 0,
    CONSTRAINT "__PREFIX__ck_plugin_ai_channel_status" CHECK ("status" IN (0, 1)),
    CONSTRAINT "__PREFIX__ck_plugin_ai_channel_circuit" CHECK ("circuit" IN ('closed', 'open', 'half_open')),
    CONSTRAINT "__PREFIX__ck_plugin_ai_channel_nums" CHECK (
        "timeout" BETWEEN 0 AND 600
        AND "priority" >= 0
        AND "weight" >= 0
        AND "fail_threshold" >= 0
        AND "cooldown_seconds" >= 0
        AND "consecutive_failures" >= 0
        AND "total_calls" >= 0
        AND "total_failures" >= 0
        AND "total_tokens" >= 0
    ),
    CONSTRAINT "__PREFIX__ck_plugin_ai_channel_probe_ok" CHECK ("last_probe_ok" IS NULL OR "last_probe_ok" IN (0, 1))
);
CREATE UNIQUE INDEX IF NOT EXISTS "__PREFIX__uk_plugin_ai_channel_name"
    ON "__PREFIX__plugin_ai_channel" ("name");
CREATE INDEX IF NOT EXISTS "__PREFIX__idx_plugin_ai_channel_status"
    ON "__PREFIX__plugin_ai_channel" ("status");
CREATE INDEX IF NOT EXISTS "__PREFIX__idx_plugin_ai_channel_circuit"
    ON "__PREFIX__plugin_ai_channel" ("circuit");
COMMENT ON TABLE "__PREFIX__plugin_ai_channel" IS 'AI 渠道：OpenAI 兼容端点 + 熔断状态机 + 累计统计 + 探活快照';
COMMENT ON COLUMN "__PREFIX__plugin_ai_channel"."id" IS 'ID';
COMMENT ON COLUMN "__PREFIX__plugin_ai_channel"."name" IS '渠道名（唯一；重名由应用层报可读错误）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_channel"."vendor" IS '厂商标签:openai|deepseek|qwen|moonshot|zhipu|ollama|other（只作展示与筛选项）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_channel"."base_url" IS '上游基址（写入期过 http/https 校验，连接期过 safedial 内网护栏）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_channel"."api_key" IS '上游凭证（明文入库；出口 mask_secret 脱敏、掩码永不回写；不建索引、不进日志）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_channel"."model" IS '上游真实模型名（调用时写入请求体 model）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_channel"."timeout" IS '请求超时秒数:0=跟随插件配置 request_timeout；探活另按 clamp(此值,3,15)';
COMMENT ON COLUMN "__PREFIX__plugin_ai_channel"."priority" IS '渠道默认优先级（小者优先；实际调度以绑定行为准）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_channel"."weight" IS '渠道默认权重（weighted 策略取 max(1,weight)）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_channel"."status" IS '状态:1=启用,0=禁用';
COMMENT ON COLUMN "__PREFIX__plugin_ai_channel"."fail_threshold" IS '连续失败阈值:0=未配⇒取插件配置（有效默认 5）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_channel"."cooldown_seconds" IS '熔断冷却秒数:0=未配⇒取插件配置（有效默认 60）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_channel"."circuit" IS '熔断态:closed|open|half_open';
COMMENT ON COLUMN "__PREFIX__plugin_ai_channel"."consecutive_failures" IS '连续失败计数（onFailure 走 SQL 原子自增 + RETURNING）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_channel"."opened_at" IS 'open=跳闸时刻；half_open=探针开始时间（兼作探针卡死护栏基准）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_channel"."total_calls" IS '累计成功调用数';
COMMENT ON COLUMN "__PREFIX__plugin_ai_channel"."total_failures" IS '累计失败调用数';
COMMENT ON COLUMN "__PREFIX__plugin_ai_channel"."total_tokens" IS '累计 token 数（仅成功行累加）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_channel"."last_success_at" IS '最近成功时刻（心跳降频判据：距此戳 ≥60s 才补写）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_channel"."last_error" IS '最近错误（写入前截断 500）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_channel"."last_probe_at" IS '最近探活时刻（NULL=从未探活）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_channel"."last_probe_ok" IS '最近探活结果:1=成功,0=失败,NULL=从未探活';
COMMENT ON COLUMN "__PREFIX__plugin_ai_channel"."last_probe_latency" IS '最近探活延迟毫秒（仅成功时写）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_channel"."last_probe_error" IS '最近探活错误（成功清空；被护栏拦下时写可读原因）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_channel"."probe_fail_streak" IS '探活连续失败数（v1 只记数，不做退避调度）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_channel"."remark" IS '备注';
COMMENT ON COLUMN "__PREFIX__plugin_ai_channel"."create_time" IS '创建时间（UTC 秒）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_channel"."update_time" IS '更新时间（UTC 秒）';

-- ===== ② 逻辑模型：对外统一模型名 + 调度策略 + 计费单价 + 本地计数开关 =====
CREATE TABLE IF NOT EXISTS "__PREFIX__plugin_ai_model" (
    "id"             bigserial     PRIMARY KEY,
    "name"           varchar(100)  NOT NULL DEFAULT '',
    "type"           varchar(16)   NOT NULL DEFAULT 'chat',
    "strategy"       varchar(16)   NOT NULL DEFAULT 'priority',
    "description"    varchar(255)  NOT NULL DEFAULT '',
    "status"         smallint      NOT NULL DEFAULT 1,
    "price_input"    numeric(12,6) NOT NULL DEFAULT 0,
    "price_output"   numeric(12,6) NOT NULL DEFAULT 0,
    "encoding"       varchar(20)   NOT NULL DEFAULT '',
    "context_window" int           NOT NULL DEFAULT 0,
    "capabilities"   jsonb         NULL,
    "create_time"    bigint        NOT NULL DEFAULT 0,
    "update_time"    bigint        NOT NULL DEFAULT 0,
    CONSTRAINT "__PREFIX__ck_plugin_ai_model_type" CHECK ("type" IN ('chat', 'embedding')),
    CONSTRAINT "__PREFIX__ck_plugin_ai_model_strategy" CHECK ("strategy" IN ('priority', 'weighted')),
    CONSTRAINT "__PREFIX__ck_plugin_ai_model_status" CHECK ("status" IN (0, 1)),
    CONSTRAINT "__PREFIX__ck_plugin_ai_model_price" CHECK ("price_input" >= 0 AND "price_output" >= 0),
    CONSTRAINT "__PREFIX__ck_plugin_ai_model_window" CHECK ("context_window" >= 0)
);
CREATE UNIQUE INDEX IF NOT EXISTS "__PREFIX__uk_plugin_ai_model_name"
    ON "__PREFIX__plugin_ai_model" ("name");
CREATE INDEX IF NOT EXISTS "__PREFIX__idx_plugin_ai_model_type"
    ON "__PREFIX__plugin_ai_model" ("type");
CREATE INDEX IF NOT EXISTS "__PREFIX__idx_plugin_ai_model_status"
    ON "__PREFIX__plugin_ai_model" ("status");
COMMENT ON TABLE "__PREFIX__plugin_ai_model" IS 'AI 逻辑模型：对外统一名 + 调度策略 + 计费单价 + 本地计数开关';
COMMENT ON COLUMN "__PREFIX__plugin_ai_model"."id" IS 'ID';
COMMENT ON COLUMN "__PREFIX__plugin_ai_model"."name" IS '对外统一模型名（唯一）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_model"."type" IS '调用类型:chat|embedding';
COMMENT ON COLUMN "__PREFIX__plugin_ai_model"."strategy" IS '调度策略:priority|weighted';
COMMENT ON COLUMN "__PREFIX__plugin_ai_model"."description" IS '描述';
COMMENT ON COLUMN "__PREFIX__plugin_ai_model"."status" IS '状态:1=启用,0=禁用（requireModel 只认启用且类型匹配）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_model"."price_input" IS '每百万输入 token 单价（解析层另拒 NaN/Inf/空串）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_model"."price_output" IS '每百万输出 token 单价';
COMMENT ON COLUMN "__PREFIX__plugin_ai_model"."encoding" IS '本地计数编码:''=关闭近似估算与上下文预检；tokenz-r=唯一启用编码（tiktoken 已退役）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_model"."context_window" IS '上下文窗口:0=不预检';
COMMENT ON COLUMN "__PREFIX__plugin_ai_model"."capabilities" IS '能力标签（字符串数组）:NULL/空=未知不拦截；显式非空且不含 function_calling ⇒ 带 tools 的 chat 请求 400';
COMMENT ON COLUMN "__PREFIX__plugin_ai_model"."create_time" IS '创建时间（UTC 秒）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_model"."update_time" IS '更新时间（UTC 秒）';

-- ===== ③ 模型 × 渠道绑定：调度的全部输入（priority/weight/status） =====
CREATE TABLE IF NOT EXISTS "__PREFIX__plugin_ai_model_channel" (
    "id"          bigserial PRIMARY KEY,
    "model_id"    bigint    NOT NULL,
    "channel_id"  bigint    NOT NULL,
    "priority"    int       NOT NULL DEFAULT 1,
    "weight"      int       NOT NULL DEFAULT 1,
    "status"      smallint  NOT NULL DEFAULT 1,
    "create_time" bigint    NOT NULL DEFAULT 0,
    "update_time" bigint    NOT NULL DEFAULT 0,
    CONSTRAINT "__PREFIX__ck_plugin_ai_mc_status" CHECK ("status" IN (0, 1)),
    CONSTRAINT "__PREFIX__ck_plugin_ai_mc_nums" CHECK ("priority" >= 0 AND "weight" >= 0)
);
-- 复合唯一（L2 的 unique 是字段级布尔，表达不了 ⇒ 这正是自管 DDL 的必需项之一）
CREATE UNIQUE INDEX IF NOT EXISTS "__PREFIX__uk_plugin_ai_model_channel"
    ON "__PREFIX__plugin_ai_model_channel" ("model_id", "channel_id");
-- 候选集查询：按 model_id 取启用绑定并按 priority/weight 排序
CREATE INDEX IF NOT EXISTS "__PREFIX__idx_plugin_ai_mc_model"
    ON "__PREFIX__plugin_ai_model_channel" ("model_id", "status", "priority");
-- 渠道删除时同事务级联清绑定走它
CREATE INDEX IF NOT EXISTS "__PREFIX__idx_plugin_ai_mc_channel"
    ON "__PREFIX__plugin_ai_model_channel" ("channel_id");
COMMENT ON TABLE "__PREFIX__plugin_ai_model_channel" IS 'AI 模型×渠道绑定（多对多；priority/weight/status 是调度的全部输入）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_model_channel"."id" IS 'ID';
COMMENT ON COLUMN "__PREFIX__plugin_ai_model_channel"."model_id" IS '逻辑模型 ID';
COMMENT ON COLUMN "__PREFIX__plugin_ai_model_channel"."channel_id" IS '渠道 ID';
COMMENT ON COLUMN "__PREFIX__plugin_ai_model_channel"."priority" IS '优先级（小者优先）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_model_channel"."weight" IS '权重（weighted 策略取 max(1,weight)）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_model_channel"."status" IS '状态:1=参与调度,0=不参与';
COMMENT ON COLUMN "__PREFIX__plugin_ai_model_channel"."create_time" IS '创建时间（UTC 秒）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_model_channel"."update_time" IS '更新时间（UTC 秒）';

-- ===== ④ 用量流水：每次真实上游尝试一行；(request_id,attempt) 唯一 = 幂等覆盖的语义基础 =====
CREATE TABLE IF NOT EXISTS "__PREFIX__plugin_ai_usage" (
    "id"                bigserial     PRIMARY KEY,
    "request_id"        varchar(64)   NOT NULL DEFAULT '',
    "attempt"           int           NOT NULL DEFAULT 0,
    "model_id"          bigint        NOT NULL DEFAULT 0,
    "model_name"        varchar(100)  NOT NULL DEFAULT '',
    "channel_id"        bigint        NOT NULL DEFAULT 0,
    "channel_name"      varchar(50)   NOT NULL DEFAULT '',
    "call_type"         varchar(16)   NOT NULL DEFAULT 'chat',
    "biz_type"          varchar(30)   NOT NULL DEFAULT '',
    "source"            varchar(30)   NOT NULL DEFAULT '',
    "stream"            smallint      NOT NULL DEFAULT 0,
    "prompt_tokens"     int           NOT NULL DEFAULT 0,
    "completion_tokens" int           NOT NULL DEFAULT 0,
    "total_tokens"      int           NOT NULL DEFAULT 0,
    "cached_tokens"     bigint        NOT NULL DEFAULT 0,
    "tokens_source"     varchar(16)   NOT NULL DEFAULT '',
    "cost"              numeric(12,6) NOT NULL DEFAULT 0,
    "currency"          varchar(10)   NOT NULL DEFAULT 'USD',
    "latency_ms"        int           NOT NULL DEFAULT 0,
    "status"            varchar(16)   NOT NULL DEFAULT 'success',
    "error_code"        varchar(100)  NOT NULL DEFAULT '',
    "error_message"     varchar(500)  NOT NULL DEFAULT '',
    "error_class"       varchar(20)   NOT NULL DEFAULT '',
    "create_time"       bigint        NOT NULL DEFAULT 0,
    "update_time"       bigint        NOT NULL DEFAULT 0,
    CONSTRAINT "__PREFIX__ck_plugin_ai_usage_status" CHECK ("status" IN ('success', 'fail', 'timeout', 'breaker')),
    CONSTRAINT "__PREFIX__ck_plugin_ai_usage_tokens_source" CHECK ("tokens_source" IN ('', 'usage', 'estimate')),
    CONSTRAINT "__PREFIX__ck_plugin_ai_usage_error_class" CHECK ("error_class" IN ('', 'auth', 'rate_limit', 'upstream', 'transport', 'local', 'unknown')),
    CONSTRAINT "__PREFIX__ck_plugin_ai_usage_call_type" CHECK ("call_type" IN ('chat', 'embedding')),
    CONSTRAINT "__PREFIX__ck_plugin_ai_usage_stream" CHECK ("stream" IN (0, 1)),
    CONSTRAINT "__PREFIX__ck_plugin_ai_usage_nums" CHECK (
        "attempt" >= 0
        AND "prompt_tokens" >= 0
        AND "completion_tokens" >= 0
        AND "total_tokens" >= 0
        AND "cost" >= 0
    )
);
-- (request_id,attempt) 唯一：幂等覆盖的语义基础，同时充当按 request_id 的查询索引
CREATE UNIQUE INDEX IF NOT EXISTS "__PREFIX__uk_plugin_ai_usage_req_attempt"
    ON "__PREFIX__plugin_ai_usage" ("request_id", "attempt");
-- 近 1 小时失败口径（status ∈ fail|timeout|breaker）
CREATE INDEX IF NOT EXISTS "__PREFIX__idx_plugin_ai_usage_status_time"
    ON "__PREFIX__plugin_ai_usage" ("status", "create_time");
-- h24 与逐日趋势窗口
CREATE INDEX IF NOT EXISTS "__PREFIX__idx_plugin_ai_usage_time"
    ON "__PREFIX__plugin_ai_usage" ("create_time");
-- 按模型聚合
CREATE INDEX IF NOT EXISTS "__PREFIX__idx_plugin_ai_usage_model_time"
    ON "__PREFIX__plugin_ai_usage" ("model_id", "create_time");
CREATE INDEX IF NOT EXISTS "__PREFIX__idx_plugin_ai_usage_biz_time"
    ON "__PREFIX__plugin_ai_usage" ("biz_type", "create_time");
CREATE INDEX IF NOT EXISTS "__PREFIX__idx_plugin_ai_usage_source_time"
    ON "__PREFIX__plugin_ai_usage" ("source", "create_time");
-- 不建 tokens_source / error_class 独立索引：v1 无对应查询画像，避免无用索引。
COMMENT ON TABLE "__PREFIX__plugin_ai_usage" IS 'AI 用量流水（每次真实上游尝试一行；(request_id,attempt) 幂等覆盖）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_usage"."id" IS 'ID';
COMMENT ON COLUMN "__PREFIX__plugin_ai_usage"."request_id" IS '请求 ID（ai_+12 字节 hex=26 字符；同一次调用的所有降级尝试共享）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_usage"."attempt" IS '尝试序号（只有真实调用失败换道时 +1；被熔断跳过不计数；流式行恒 0）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_usage"."model_id" IS '逻辑模型 ID（0=未落到模型）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_usage"."model_name" IS '模型名快照（改名后历史仍可读）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_usage"."channel_id" IS '渠道 ID（0=未落到渠道）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_usage"."channel_name" IS '渠道名快照';
COMMENT ON COLUMN "__PREFIX__plugin_ai_usage"."call_type" IS '调用类型:chat|embedding';
COMMENT ON COLUMN "__PREFIX__plugin_ai_usage"."biz_type" IS '业务类型（写入侧只判空，不做闭集校验）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_usage"."source" IS '来路（写入侧只判空；网关侧写 key:<调用方>）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_usage"."stream" IS '是否流式:1=流式,0=非流式（失败行恒写 0）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_usage"."prompt_tokens" IS '输入 token 数';
COMMENT ON COLUMN "__PREFIX__plugin_ai_usage"."completion_tokens" IS '输出 token 数';
COMMENT ON COLUMN "__PREFIX__plugin_ai_usage"."total_tokens" IS '总 token 数（恒 = prompt+completion）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_usage"."cached_tokens" IS '上游 prompt 缓存命中输入 token（未返回=0≠没命中；不进成本公式）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_usage"."tokens_source" IS 'token 来源:usage|estimate|空串=本次无计数';
COMMENT ON COLUMN "__PREFIX__plugin_ai_usage"."cost" IS '成本（numeric(12,6)，金额一律不经 float64）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_usage"."currency" IS '币种（默认 USD）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_usage"."latency_ms" IS '耗时毫秒';
COMMENT ON COLUMN "__PREFIX__plugin_ai_usage"."status" IS '状态:success|fail|timeout|breaker（v1 只写 success|fail|timeout）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_usage"."error_code" IS '错误码（异常类型名）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_usage"."error_message" IS '错误信息（截断 500）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_usage"."error_class" IS '失败分类:auth|rate_limit|upstream|transport|local|unknown';
COMMENT ON COLUMN "__PREFIX__plugin_ai_usage"."create_time" IS '创建时间（UTC 秒）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_usage"."update_time" IS '更新时间（UTC 秒）';

-- ===== ⑤ 熔断事件流水：状态机动作审计（open/close/manual_open/manual_close） =====
CREATE TABLE IF NOT EXISTS "__PREFIX__plugin_ai_breaker_event" (
    "id"                   bigserial    PRIMARY KEY,
    "channel_id"           bigint       NOT NULL,
    "channel_name"         varchar(50)  NOT NULL DEFAULT '',
    "event"                varchar(16)  NOT NULL DEFAULT 'open',
    "consecutive_failures" int          NOT NULL DEFAULT 0,
    "fail_threshold"       int          NOT NULL DEFAULT 0,
    "cooldown_seconds"     int          NOT NULL DEFAULT 0,
    "last_error"           varchar(500) NOT NULL DEFAULT '',
    "create_time"          bigint       NOT NULL DEFAULT 0,
    "update_time"          bigint       NOT NULL DEFAULT 0,
    CONSTRAINT "__PREFIX__ck_plugin_ai_ev_event" CHECK ("event" IN ('open', 'close', 'manual_open', 'manual_close')),
    CONSTRAINT "__PREFIX__ck_plugin_ai_ev_nums" CHECK (
        "consecutive_failures" >= 0
        AND "fail_threshold" >= 0
        AND "cooldown_seconds" >= 0
    )
);
-- 单渠道近 20 条时间线
CREATE INDEX IF NOT EXISTS "__PREFIX__idx_plugin_ai_ev_channel_time"
    ON "__PREFIX__plugin_ai_breaker_event" ("channel_id", "create_time");
COMMENT ON TABLE "__PREFIX__plugin_ai_breaker_event" IS 'AI 熔断事件流水（自动/人工动作审计；写入失败只记日志，不影响主流程）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_breaker_event"."id" IS 'ID';
COMMENT ON COLUMN "__PREFIX__plugin_ai_breaker_event"."channel_id" IS '渠道 ID';
COMMENT ON COLUMN "__PREFIX__plugin_ai_breaker_event"."channel_name" IS '渠道名快照';
COMMENT ON COLUMN "__PREFIX__plugin_ai_breaker_event"."event" IS '事件:open=自动跳闸,close=自动恢复,manual_open=人工熔断,manual_close=人工恢复';
COMMENT ON COLUMN "__PREFIX__plugin_ai_breaker_event"."consecutive_failures" IS '触发时连续失败快照（恢复事件为 0）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_breaker_event"."fail_threshold" IS '触发时生效阈值快照';
COMMENT ON COLUMN "__PREFIX__plugin_ai_breaker_event"."cooldown_seconds" IS '触发时生效冷却快照';
COMMENT ON COLUMN "__PREFIX__plugin_ai_breaker_event"."last_error" IS '触发时最近错误（截断 500）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_breaker_event"."create_time" IS '创建时间（UTC 秒）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_breaker_event"."update_time" IS '更新时间（UTC 秒）';
