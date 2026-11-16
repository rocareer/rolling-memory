-- ===== 来源类型新增 git（运输方式闭集 local|http|git）=====
-- 语义：只放宽 admin_plugin_sources.type 的闭集约束并更新注释；不加列、不动数据——
-- git 的仓库地址 / ref / 仓内清单路径都编码在既有 location 字段里（<URL>[#<ref>][!<仓内清单路径>]）。
-- 幂等：DROP CONSTRAINT IF EXISTS 后再 ADD（重复执行得到等价结果）。
ALTER TABLE "__PREFIX__admin_plugin_sources"
    DROP CONSTRAINT IF EXISTS "__PREFIX__ck_admin_plugin_sources_type";
ALTER TABLE "__PREFIX__admin_plugin_sources"
    ADD CONSTRAINT "__PREFIX__ck_admin_plugin_sources_type"
    CHECK ("type" IN ('local', 'http', 'git'));

COMMENT ON TABLE "__PREFIX__admin_plugin_sources" IS '插件仓库来源（local 文件 / http(s) URL / git 仓库；共用 catalog v1 格式）';
COMMENT ON COLUMN "__PREFIX__admin_plugin_sources"."type" IS '运输方式:local=本地文件,http=HTTP(S),git=Git 仓库';
COMMENT ON COLUMN "__PREFIX__admin_plugin_sources"."location" IS 'local:仓库根相对路径(或显式允许的绝对路径);http:https URL;git:<仓库URL>[#<ref>][!<仓内清单路径>]';
