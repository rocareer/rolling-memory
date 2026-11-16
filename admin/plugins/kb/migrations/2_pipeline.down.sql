-- plugin: kb v1.0.0
-- 回滚：逆序删表（索引随表）；与 2_pipeline.up.sql 对称。
-- 注：vector 扩展不在此回收（其他模块亦可能依赖；回收会破坏其向量面）。
DROP TABLE IF EXISTS "__PREFIX__kb_produce_state";
DROP TABLE IF EXISTS "__PREFIX__kb_chunk_vectors";
DROP TABLE IF EXISTS "__PREFIX__kb_extract";
DROP TABLE IF EXISTS "__PREFIX__kb_blob_ref";
DROP TABLE IF EXISTS "__PREFIX__kb_blob";
