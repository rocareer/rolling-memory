-- plugin: kb v1.0.0
-- 回滚：逆序删表（索引随表）；与 6_jobs.up.sql 对称。
-- 说明：账本行随表删（作业是可重投的派生面——业务产物在 kb_* 表里，不在本表）。
DROP TABLE IF EXISTS "__PREFIX__plugin_kb_jobs";
