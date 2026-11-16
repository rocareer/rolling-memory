-- 管理员日志的列表与统计都按 created_at 过滤（默认时间窗）与排序（created_at DESC、id 托底），
-- 而该表原先只有 admin_id 单列索引 ⇒ 排序与范围过滤无法走索引。增补 (created_at DESC, id DESC)，
-- 与列表排序口径对齐，可用索引有序访问 + LIMIT 即停。
-- 注意：本迁移机制整文件在隐式事务内执行，禁用 CREATE INDEX CONCURRENTLY（必报错）。
CREATE INDEX IF NOT EXISTS "__PREFIX__idx_admin_logs_created_at" ON "__PREFIX__admin_logs" ("created_at" DESC, "id" DESC);
