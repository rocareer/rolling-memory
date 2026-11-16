-- 回滚：闭集收回 local|http。若库里已存在 type='git' 的来源行，ADD CONSTRAINT 会失败（fail-closed）——
-- 回滚前先用 `aigo plugin source remove <名>` 清掉 git 来源。
ALTER TABLE "__PREFIX__admin_plugin_sources"
    DROP CONSTRAINT IF EXISTS "__PREFIX__ck_admin_plugin_sources_type";
ALTER TABLE "__PREFIX__admin_plugin_sources"
    ADD CONSTRAINT "__PREFIX__ck_admin_plugin_sources_type"
    CHECK ("type" IN ('local', 'http'));

COMMENT ON TABLE "__PREFIX__admin_plugin_sources" IS '插件仓库来源（local 文件 / http(s) URL；共用 catalog v1 格式）';
COMMENT ON COLUMN "__PREFIX__admin_plugin_sources"."type" IS '运输方式:local=本地文件,http=HTTP(S)';
COMMENT ON COLUMN "__PREFIX__admin_plugin_sources"."location" IS 'local:仓库根相对路径(或显式允许的绝对路径);http:https URL';
