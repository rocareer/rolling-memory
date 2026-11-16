-- plugin: demo v1.0.0
-- 示例插件的自管迁移（编号在插件内独立自增 1,2,3…，走独立版本表 <前缀>plugin_demo_migrations）。
-- 整文件一次执行、单个隐式事务 ⇒ 禁 CREATE INDEX CONCURRENTLY 与文件内 BEGIN / COMMIT（docs/sql-discipline.md）。
--
-- 这张表是「L1 数据面」的样板：结构由插件迁移承担（与 L2 实体由引擎管结构的分工不同）；
-- 卸载默认保留表与数据，--purge 才按 down 逆序回滚（a 步）。
-- 查询形态只有「按主键倒序取一行」⇒ 索引由主键承担，不另建（SQL 纪律：索引按谓词建，不预建用不上的索引）。
CREATE TABLE IF NOT EXISTS "__PREFIX__plugin_demo_notes" (
    "id"         bigserial    PRIMARY KEY,
    "body"       varchar(255) NOT NULL DEFAULT '',
    "created_at" timestamptz  NOT NULL DEFAULT now(),
    "updated_at" timestamptz  NOT NULL DEFAULT now()
);
COMMENT ON TABLE  "__PREFIX__plugin_demo_notes" IS '示例插件：迁移自管的备注（L1 数据面样板）';
COMMENT ON COLUMN "__PREFIX__plugin_demo_notes"."body" IS '备注内容';

-- 种子：只在该表还没有行时写入（迁移由版本表保证只跑一次；这一层判重让「手工重放」同样安全）
INSERT INTO "__PREFIX__plugin_demo_notes" ("body")
SELECT '插件迁移写入的初始备注（kind: mixed 的 L1 数据面）'
WHERE NOT EXISTS (SELECT 1 FROM "__PREFIX__plugin_demo_notes");
