-- plugin: ai v0.2.0
-- 迁移 5：异步任务台账（F5+F6）。整文件一次执行、单个隐式事务 ⇒ 禁止 CREATE INDEX CONCURRENTLY
-- 与文件内 BEGIN / COMMIT；也不得写宿主菜单/角色组表（ADMIN_RULES / ADMIN_GROUPS）。
--
-- 标识符纪律：表名 / 索引名 / 约束名一律 "__PREFIX__" 占位符 + 双引号标识符（安装器按配置表前缀整文件替换）；
-- 列名不拼前缀。前缀需 ≤ 31 字节（最长索引主干名 32 字符，32 + 前缀 ≤ PostgreSQL 标识符 63 字节上限）。
--
-- 本表是「提交 → 进程内 worker 领取 → 执行 → 结果落库」的唯一账本（宿主无队列基建 ⇒ DB 表 + 进程内 worker）：
--   - 状态机 pending → processing → success|failed（CHECK 闭集）；
--   - request_id 的幂等 = **列 not null default '' + 部分唯一索引 `WHERE request_id <> ''`**
--     （与迁移 4 的 plugin_ai_api_key.uk(source) 同口径）：空值不互撞、非空重复只留一行；
--     提交侧同时强制生成非空 request_id（空值语义只留给内部直插与用例）；
--   - 领取 = 同事务 SELECT ... FOR UPDATE SKIP LOCKED + UPDATE（locked_until 是租约，过期即被下一个
--     进程/重启后的本进程重领，attempt 累加）⇒ 索引服务于「按状态取一批可领取行」；
--   - 不建外键：本表不引用渠道/模型行（任务载荷里存的是模型**逻辑名**，执行期才解析）。
CREATE TABLE IF NOT EXISTS "__PREFIX__plugin_ai_task" (
    "id"             bigserial    PRIMARY KEY,
    "task_type"      varchar(16)  NOT NULL DEFAULT 'chat',
    "status"         varchar(16)  NOT NULL DEFAULT 'pending',
    "payload"        jsonb        NULL,
    "result"         jsonb        NULL,
    "error"          varchar(500) NOT NULL DEFAULT '',
    "source"         varchar(30)  NOT NULL DEFAULT '',
    "biz_type"       varchar(30)  NOT NULL DEFAULT '',
    "request_id"     varchar(64)  NOT NULL DEFAULT '',
    "attempt"        int          NOT NULL DEFAULT 0,
    "progress_done"  int          NOT NULL DEFAULT 0,
    "progress_total" int          NOT NULL DEFAULT 0,
    "locked_until"   timestamptz  NULL,
    "create_time"    bigint       NOT NULL DEFAULT 0,
    "update_time"    bigint       NOT NULL DEFAULT 0,
    CONSTRAINT "__PREFIX__ck_plugin_ai_task_type" CHECK ("task_type" IN ('chat', 'embedding')),
    CONSTRAINT "__PREFIX__ck_plugin_ai_task_status" CHECK ("status" IN ('pending', 'processing', 'success', 'failed')),
    CONSTRAINT "__PREFIX__ck_plugin_ai_task_nums" CHECK (
        "attempt" >= 0
        AND "progress_done" >= 0
        AND "progress_total" >= 0
        AND ("progress_total" = 0 OR "progress_done" <= "progress_total")
    )
);
-- 幂等：非空 request_id 只允许一行（空串被部分索引排除 ⇒ 不互撞；同源口径见迁移 1 的 uk_plugin_ai_usage_req_attempt）
CREATE UNIQUE INDEX IF NOT EXISTS "__PREFIX__uk_plugin_ai_task_request_id"
    ON "__PREFIX__plugin_ai_task" ("request_id") WHERE "request_id" <> '';
-- 领取与列表的主索引：按状态取行（claim 走 status ∈ {pending, processing} + id 顺序）
CREATE INDEX IF NOT EXISTS "__PREFIX__idx_plugin_ai_task_status"
    ON "__PREFIX__plugin_ai_task" ("status", "id");
-- 按业务类型筛选（任务页）
CREATE INDEX IF NOT EXISTS "__PREFIX__idx_plugin_ai_task_type_time"
    ON "__PREFIX__plugin_ai_task" ("task_type", "create_time");
COMMENT ON TABLE "__PREFIX__plugin_ai_task" IS 'AI 异步任务台账（提交→进程内 worker 领取→执行→结果落库；request_id 非空即幂等）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_task"."id" IS 'ID';
COMMENT ON COLUMN "__PREFIX__plugin_ai_task"."task_type" IS '任务类型:chat|embedding（与 usage.call_type 同取值）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_task"."status" IS '状态:pending|processing|success|failed（worker 领取置 processing，终态不可逆）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_task"."payload" IS '任务载荷（jsonb：model/messages|inputs/biz_type/source 与 chat 直通参数）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_task"."result" IS '任务结果（jsonb：chat 为内容+usage；embedding 为 embeddings 数组，导出按条目逐行）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_task"."error" IS '最近错误（写入前截断 500；重试中的中间错误也写，成功时不清空）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_task"."source" IS '来路（提交方；进 usage.source，≤30 字符）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_task"."biz_type" IS '业务类型（提交方；进 usage.biz_type，≤30 字符）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_task"."request_id" IS '幂等键（非空即唯一；usage.request_id 由它派生：task-<本值>-a<尝试>-s<分片>）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_task"."attempt" IS '已尝试次数（每次领取 +1；重试耗尽 task_max_attempts 即置 failed）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_task"."progress_done" IS '已完成条目数（embedding 按片回写、单调不减；chat 收尾写 1）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_task"."progress_total" IS '总条目数（提交时定：embedding=输入条数，chat=1）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_task"."locked_until" IS '租约到期时刻（NULL=未被领取；过期即被重领，attempt 累加）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_task"."create_time" IS '创建时间（UTC 秒）';
COMMENT ON COLUMN "__PREFIX__plugin_ai_task"."update_time" IS '更新时间（UTC 秒）';