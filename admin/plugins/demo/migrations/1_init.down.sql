-- plugin: demo v1.0.0
-- 1_init 的回滚（--purge 时按 down 逆序执行；a 步：破坏性步骤在最前，因为 down 文件随后会被删）。
-- 插件自管表与它的迁移同生共死：默认卸载保留表与数据，--purge --yes 才走到这里。
DROP TABLE IF EXISTS "__PREFIX__plugin_demo_notes";
