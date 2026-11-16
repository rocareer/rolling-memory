-- plugin: ai v0.2.0
-- 4_api_key 的回滚（--purge 时按 down 逆序执行）：本迁移只建一张表，其索引与约束随表一起删。
-- 与 1_init.down.sql 同口径：表间不建外键，删除顺序仍按语义逆序（这里只有一张表，无依赖方）。
-- 卸载不带 --purge 时不执行本文件（保留表与数据）。
DROP TABLE IF EXISTS "__PREFIX__plugin_ai_api_key";