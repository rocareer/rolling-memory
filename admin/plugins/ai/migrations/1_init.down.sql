-- plugin: ai v0.2.0
-- 1_init 的回滚（--purge 时按 down 逆序执行；插件自管表与它的迁移同生共死）。
-- 逆序：先删依赖方（事件流水/用量/绑定），再删被引用方（模型/渠道）；表间不建外键，删除顺序仍按语义逆序。
DROP TABLE IF EXISTS "__PREFIX__plugin_ai_breaker_event";
DROP TABLE IF EXISTS "__PREFIX__plugin_ai_usage";
DROP TABLE IF EXISTS "__PREFIX__plugin_ai_model_channel";
DROP TABLE IF EXISTS "__PREFIX__plugin_ai_model";
DROP TABLE IF EXISTS "__PREFIX__plugin_ai_channel";
