-- plugin: ai v0.2.0
-- 5_task 的回滚（--purge 时按 down 逆序执行；插件自管表与它的迁移同生共死）。
-- 本迁移只建 1 张表，索引与约束随表一并删除；不引用其它表 ⇒ 删除顺序无依赖。
DROP TABLE IF EXISTS "__PREFIX__plugin_ai_task";