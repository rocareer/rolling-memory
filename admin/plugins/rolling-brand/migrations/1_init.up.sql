-- Rolling 品牌包：插件自管设置表（不写核心表）。
-- 默认卸载保留表与数据；--purge 按本文件 down 回滚。
CREATE TABLE IF NOT EXISTS "__PREFIX__rolling_brand_settings" (
    "id" bigserial PRIMARY KEY,
    "site_name" varchar(128) NOT NULL DEFAULT '',
    "record_number" varchar(128) NOT NULL DEFAULT '',
    "welcome_text" text NOT NULL DEFAULT '',
    "updated_at" timestamptz NOT NULL DEFAULT now()
);

INSERT INTO "__PREFIX__rolling_brand_settings" ("site_name", "record_number", "welcome_text")
SELECT
    'Rolling-Admin',
    '京ICP备8888888号-1',
    'Rocareer 旗下的 Go 语言开发框架；高性能、稳定可靠、易于扩展，为内部项目提供统一技术底座，欢迎您加入我们！'
WHERE NOT EXISTS (SELECT 1 FROM "__PREFIX__rolling_brand_settings");
