-- plugin: kb v1.0.0
-- 回滚：逆序删表（索引随表）；与 4_tags.up.sql 对称。
-- 注：kb_tag_link 为硬删表、其余三表软删——一律 DROP，不做数据搬迁（标签为新增面，无下游依赖）。
DROP TABLE IF EXISTS "__PREFIX__kb_tag_rule";
DROP TABLE IF EXISTS "__PREFIX__kb_tag_link";
DROP TABLE IF EXISTS "__PREFIX__kb_tag";
DROP TABLE IF EXISTS "__PREFIX__kb_tag_group";
