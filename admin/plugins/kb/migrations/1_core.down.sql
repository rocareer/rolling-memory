-- plugin: kb v1.0.0
-- 回滚：逆序删表（索引随表）；与 1_core.up.sql 对称。
-- 对端 up 为 v1 终态（match_text 派生列 + tsv 生成列）；
--   本 down 删表即覆盖全部列面，无独立回滚步骤。
-- 注：pg_trgm 扩展不在此回收（其他模块亦可能依赖；回收会破坏其 trgm 索引）。
DROP TABLE IF EXISTS "__PREFIX__kb_audit_log";
DROP TABLE IF EXISTS "__PREFIX__kb_team";
DROP TABLE IF EXISTS "__PREFIX__kb_grant";
DROP TABLE IF EXISTS "__PREFIX__kb_chunk";
DROP TABLE IF EXISTS "__PREFIX__kb_document";
DROP TABLE IF EXISTS "__PREFIX__kb_library";
