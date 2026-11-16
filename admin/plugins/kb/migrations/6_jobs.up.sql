-- plugin: kb v1.0.0
-- ===== 知识库插件 · 作业账本表（本插件自管最小作业面，见 service/jobs） =====
-- 定位：kind 注册表 + 入队（同键单飞）+ 领取/执行/重试循环的**读模型**；业务真源就是本表。
-- 与宿主作业面无交集：本表的裁决与领取全在插件内（宿主没有供插件复用的、与「插件迁移隔离」相容的作业面）。
--
-- 表：plugin_kb_jobs 作业账本（kind/key/status/attempt/租约/进度/结果/原因）。
-- 不变量（与 service/jobs 成对；索引形态是不变量的实现处，不可随手改）：
--   ① 同 key 至多一条活跃行（pending/running）——由**部分唯一索引** uk_plugin_kb_jobs_active_key 承担，
--      不是「先查后插」（并发入队同键时靠唯一冲突收敛为 created=false）；
--   ② 领取 = 到期 pending ∪ 租约过期的 running（进程崩溃/超时救援），attempt 累加；
--   ③ 进度 done 单调不回退（写侧取 GREATEST）；
--   ④ 终态 = done/failed/canceled，不再被领取。
-- 纪律：语义闭集（kind/status）在代码侧（不加 DB CHECK，照 KB 其它闭集列口径）；
--       索引一次到位（每个索引可指认引用查询）；表名/索引名带 __PREFIX__，命名 uk_/idx_；
--       无软删列（终态行由每日清理按保留期收敛，见 worker 的 purgeTerminal）；
--       禁 CREATE INDEX CONCURRENTLY（本机制整文件隐式事务）；无 BEGIN/COMMIT。
CREATE TABLE IF NOT EXISTS "__PREFIX__plugin_kb_jobs" (
  "id" bigserial PRIMARY KEY,
  "kind" varchar(64) NOT NULL DEFAULT '',
  "key" varchar(255) NOT NULL DEFAULT '',
  "status" varchar(16) NOT NULL DEFAULT 'pending',
  "attempt" integer NOT NULL DEFAULT 0,
  "max_attempts" integer NOT NULL DEFAULT 3,
  "run_at" timestamptz NOT NULL DEFAULT NOW(),
  "locked_until" timestamptz,
  "params" jsonb,
  "result" jsonb,
  "progress_done" bigint NOT NULL DEFAULT 0,
  "progress_total" bigint NOT NULL DEFAULT 0,
  "reason" varchar(512) NOT NULL DEFAULT '',
  "created_at" timestamptz DEFAULT NOW(),
  "updated_at" timestamptz DEFAULT NOW()
);

COMMENT ON TABLE "__PREFIX__plugin_kb_jobs" IS '知识库作业账本：kind=作业类型（kb:ingest/kb:rebuild/kb:purge/kb:tag/kb:prune），key=幂等键（同键至多一个活跃行），status=pending/running/done/failed/canceled';
COMMENT ON COLUMN "__PREFIX__plugin_kb_jobs"."key" IS '幂等键（kb:ingest:<document_id>:<build_hash> / kb:rebuild:<library_id>:<build> / kb:<library_id> / kb:doc:<document_id> / kb:tag:<document_id> / kb:prune:<YYYY-MM-DD>）';
COMMENT ON COLUMN "__PREFIX__plugin_kb_jobs"."locked_until" IS '租约到期时间：running 行超过该时刻即可被本进程或其它进程重领（崩溃救援）';
COMMENT ON COLUMN "__PREFIX__plugin_kb_jobs"."progress_done" IS '进度已完成（单调不回退：写侧 GREATEST）';
COMMENT ON COLUMN "__PREFIX__plugin_kb_jobs"."reason" IS '最近一次失败原因（重试未耗尽时为退避重入的原因，便于运维定位）';

-- ① 单飞判据（唯一实现处）：同 key 至多一条活跃行
CREATE UNIQUE INDEX IF NOT EXISTS "__PREFIX__uk_plugin_kb_jobs_active_key"
  ON "__PREFIX__plugin_kb_jobs" ("key")
  WHERE "status" IN ('pending', 'running');

-- ② 领取面：到期 pending / 租约过期 running 的扫描（(status, run_at) 前缀即领取条件）
CREATE INDEX IF NOT EXISTS "__PREFIX__idx_plugin_kb_jobs_due"
  ON "__PREFIX__plugin_kb_jobs" ("status", "run_at");

-- ③ 运维读数：按 kind + status 计数（队列快照）
CREATE INDEX IF NOT EXISTS "__PREFIX__idx_plugin_kb_jobs_kind_status"
  ON "__PREFIX__plugin_kb_jobs" ("kind", "status");
