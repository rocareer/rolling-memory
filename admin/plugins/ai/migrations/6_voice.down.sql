-- plugin: ai v0.2.0
-- 迁移 6 回滚（逆序）：① 恢复旧的闭集 CHECK（chat|embedding）；② 删除音色表。
--
-- 回滚硬边界（如实登记）：ADD CONSTRAINT 会校验**存量行** ⇒ 库里已存在 type='tts' 的模型或
-- call_type='tts' 的 usage 行时，本文件会在 ② 之前报 23514 而中止（隐式事务整块回滚，不半回滚）。
-- 要回滚到旧闭集必须先把这些行删掉或改回 chat/embedding——这是「闭集收窄」本身的代价，不是本文件的缺陷。
--
-- 整文件一次执行、单个隐式事务 ⇒ 禁 BEGIN/COMMIT、禁 CREATE INDEX CONCURRENTLY。
ALTER TABLE "__PREFIX__plugin_ai_model"
    DROP CONSTRAINT IF EXISTS "__PREFIX__ck_plugin_ai_model_type";
ALTER TABLE "__PREFIX__plugin_ai_model"
    ADD CONSTRAINT "__PREFIX__ck_plugin_ai_model_type" CHECK ("type" IN ('chat', 'embedding'));
COMMENT ON COLUMN "__PREFIX__plugin_ai_model"."type" IS '调用类型:chat|embedding';

ALTER TABLE "__PREFIX__plugin_ai_usage"
    DROP CONSTRAINT IF EXISTS "__PREFIX__ck_plugin_ai_usage_call_type";
ALTER TABLE "__PREFIX__plugin_ai_usage"
    ADD CONSTRAINT "__PREFIX__ck_plugin_ai_usage_call_type" CHECK ("call_type" IN ('chat', 'embedding'));
COMMENT ON COLUMN "__PREFIX__plugin_ai_usage"."call_type" IS '调用类型:chat|embedding';

DROP TABLE IF EXISTS "__PREFIX__plugin_ai_voice";