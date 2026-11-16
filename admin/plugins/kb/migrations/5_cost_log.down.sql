-- plugin: kb v1.0.0
-- 回退 5_cost_log.up.sql（与 up 对称）：逆序 DROP（本迁移仅一张表；记账行随表删——
-- 只增不改不删是运行期语义，回退迁移按 golang-migrate 惯例整表回退）。
DROP TABLE IF EXISTS "__PREFIX__kb_cost_log";
