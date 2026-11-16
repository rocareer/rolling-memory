-- plugin: kb v1.0.0
-- ===== 知识库检索日志表回滚（3_search_log） =====
-- 逆序：索引随表一并 DROP（无需单列 DROP INDEX）。

DROP TABLE IF EXISTS "__PREFIX__kb_search_log";
