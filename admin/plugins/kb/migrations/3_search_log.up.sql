-- plugin: kb v1.0.0
-- ===== 知识库模块 v1 · 检索日志表（表清单与保留期口径见本插件 README「三・数据与迁移边界」「六・运维口径」） =====
-- 本表结构为 v1 终态。
-- 表：kb_search_log 检索逐条证据（增长表，只增不改不删，无软删列）。
-- 写入语义（T9）：请求内 best-effort 同步落行——脱钩短 ctx + 失败只告警，绝不失败检索本身。
-- 索引设计：建**两个最小必要索引**——
--       (created_at)           ← 保留期清理（kb:prune 按时间删）
--       (user_id, created_at)  ← 用户面检索清单（时间 + 用户过滤）
-- 纪律：表名/索引名带 __PREFIX__，命名 idx_；索引一次到位（各指认引用查询）；
--       禁 CREATE INDEX CONCURRENTLY（本机制整文件隐式事务）；无 BEGIN/COMMIT。

CREATE TABLE IF NOT EXISTS "__PREFIX__kb_search_log" (
    "id"           bigserial    PRIMARY KEY,
    "user_id"      bigint       NOT NULL DEFAULT 0,
    "mode"         varchar(16)  NOT NULL DEFAULT '',
    "legs"         jsonb        NOT NULL DEFAULT '[]'::jsonb,
    "query_hash"   char(40)     NOT NULL DEFAULT '',
    "query_text"   varchar(512) NOT NULL DEFAULT '',
    "result_count" int          NOT NULL DEFAULT 0,
    "latency_ms"   int          NOT NULL DEFAULT 0,
    "degraded"     jsonb        NOT NULL DEFAULT '[]'::jsonb,
    "request_id"   varchar(64)  NOT NULL DEFAULT '',
    "created_at"   timestamptz
);
-- 保留期清理轴（kb:prune：按时间窗删；缺省 180 天）
CREATE INDEX IF NOT EXISTS "__PREFIX__idx_kb_search_log_created_at" ON "__PREFIX__kb_search_log" ("created_at");
-- 用户面清单轴（user_id + 时间窗；非用户面行恒 0）
CREATE INDEX IF NOT EXISTS "__PREFIX__idx_kb_search_log_user_created" ON "__PREFIX__kb_search_log" ("user_id", "created_at");
COMMENT ON TABLE "__PREFIX__kb_search_log" IS '检索逐条证据（增长表：只增不改不删，无软删列）；请求内 best-effort 落行（失败告警不失败检索）；保留期缺省 180 天，清理走 kb:prune:<date> 作业';
COMMENT ON COLUMN "__PREFIX__kb_search_log"."id" IS 'ID';
COMMENT ON COLUMN "__PREFIX__kb_search_log"."user_id" IS '检索主体 users.id；0=非用户面（后台探针/系统面——admins.id 与 users.id 两空间禁混用，故后台面不落 id）';
COMMENT ON COLUMN "__PREFIX__kb_search_log"."mode" IS '生效检索模式闭集(代码侧):auto|lexical|vector|hybrid（auto 经向量内容探测后落实际执行档）';
COMMENT ON COLUMN "__PREFIX__kb_search_log"."legs" IS '各腿读数 JSON（腿名/命中数/耗时/降级原因；空数组=零腿）';
COMMENT ON COLUMN "__PREFIX__kb_search_log"."query_hash" IS '查询文本 sha1（口径同切片 text_hash；原文可关时仍可去重统计）';
COMMENT ON COLUMN "__PREFIX__kb_search_log"."query_text" IS '查询原文（按 kb.search.log_query_text_max 截断；0=配置关，落空串）';
COMMENT ON COLUMN "__PREFIX__kb_search_log"."result_count" IS '返回条数（截断后）';
COMMENT ON COLUMN "__PREFIX__kb_search_log"."latency_ms" IS '请求耗时毫秒（含腿执行与融合）';
COMMENT ON COLUMN "__PREFIX__kb_search_log"."degraded" IS '单腿降级回显 JSON（腿名: 原因；与响应 degraded[] 同源）';
COMMENT ON COLUMN "__PREFIX__kb_search_log"."request_id" IS '请求关联 id（仅日志关联用；非鉴权身份）';
COMMENT ON COLUMN "__PREFIX__kb_search_log"."created_at" IS '创建时间（保留期清理轴）';
