-- plugin: kb v1.0.0
-- ===== 知识库模块 v1 · 非 LLM 成本记账（口径见本插件 README「四・配置键与覆盖口径」「六・运维口径」） =====
-- 本表结构为 v1 终态。
-- 定位：**非模型端口直连**的媒体处理成本台账（云 OCR / ASR / caption 独立驱动按页/按分钟/按帧计价）。
--   经模型端口者（chat/embedding/VLM）的记账归各 Provider 实现（产品侧为其调用日志），不落本表——两账并集即 KB 总成本。
-- 只增不改不删（同 kb_audit_log 口径）：无软删列、无 update/delete 面；保留期内清理走
--   离线/运维（保留期参数由运维配置决定，本批不设清理作业）。
-- 纪律：语义闭集（kind/unit）在代码侧（不加 DB CHECK；语义闭集走代码闸，加值零迁移）；索引一次到位（每索引可指认
--   引用查询）；表名/索引名带 __PREFIX__，命名 uk_/idx_；禁 CREATE INDEX CONCURRENTLY（整文件隐式
--   单事务）；无 BEGIN/COMMIT；时间列 created_at（timestamptz）。

CREATE TABLE IF NOT EXISTS "__PREFIX__kb_cost_log" (
    "id"          bigserial    PRIMARY KEY,
    "library_id"  bigint       NOT NULL DEFAULT 0,
    "document_id" bigint       NOT NULL DEFAULT 0,
    "kind"        varchar(16)  NOT NULL DEFAULT '',
    "provider"    varchar(64)  NOT NULL DEFAULT '',
    "unit"        varchar(16)  NOT NULL DEFAULT '',
    "quantity"    numeric(14,3) NOT NULL DEFAULT 0,
    "unit_price"  numeric(14,6) NOT NULL DEFAULT 0,
    "cost_cny"    numeric(14,6) NOT NULL DEFAULT 0,
    "detail"      jsonb,
    "created_at"  timestamptz
);
-- 库级成本聚合轴（离线报表 SQL 按库 sum；kb:purge 按库清记账面之外的尾巴时同用）
CREATE INDEX IF NOT EXISTS "__PREFIX__idx_kb_cost_log_library_id" ON "__PREFIX__kb_cost_log" ("library_id", "id");
-- 文档级对账轴（后台文档详情成本读数 / 单文档成本核对）
CREATE INDEX IF NOT EXISTS "__PREFIX__idx_kb_cost_log_document_id" ON "__PREFIX__kb_cost_log" ("document_id", "id");
-- 时间窗轴（时间范围报表与保留期清理；与 kb_search_log 保持一致）
CREATE INDEX IF NOT EXISTS "__PREFIX__idx_kb_cost_log_created_at" ON "__PREFIX__kb_cost_log" ("created_at");
COMMENT ON TABLE "__PREFIX__kb_cost_log" IS '非 LLM 成本记账：云 OCR/ASR/caption 独立驱动的按量计价行（只增不改不删）；经模型端口直连者不落本表（其记账归各 Provider 实现），对账口径 = 两账并集';
COMMENT ON COLUMN "__PREFIX__kb_cost_log"."id" IS 'ID';
COMMENT ON COLUMN "__PREFIX__kb_cost_log"."library_id" IS '所属库ID（逻辑外键，不建 FK 约束；0=无库上下文）';
COMMENT ON COLUMN "__PREFIX__kb_cost_log"."document_id" IS '所属文档ID（逻辑外键，不建 FK 约束；0=非文档对象）';
COMMENT ON COLUMN "__PREFIX__kb_cost_log"."kind" IS '成本事件闭集(代码侧):ocr=光学识别,asr=语音转写,caption=图像描述,frame=关键帧处理';
COMMENT ON COLUMN "__PREFIX__kb_cost_log"."provider" IS '提供方读数（渠道/驱动名，如 dashscope-ocr；空=未标注）';
COMMENT ON COLUMN "__PREFIX__kb_cost_log"."unit" IS '计价单位闭集(代码侧):page=页,minute=分钟,frame=帧,call=次';
COMMENT ON COLUMN "__PREFIX__kb_cost_log"."quantity" IS '计量数（页数/分钟数/帧数；实测值，非估算）';
COMMENT ON COLUMN "__PREFIX__kb_cost_log"."unit_price" IS '单价快照（CNY/单位；0=未配价——不伪造价格，detail.price_unconfigured=true）';
COMMENT ON COLUMN "__PREFIX__kb_cost_log"."cost_cny" IS '金额快照（CNY；= quantity × unit_price，写入时定格、历史不回改——价格快照口径）';
COMMENT ON COLUMN "__PREFIX__kb_cost_log"."detail" IS '明细 JSON（estimate=标注估算/confidence=置信度/price_unconfigured=未配价等；NULL=无明细）';
COMMENT ON COLUMN "__PREFIX__kb_cost_log"."created_at" IS '创建时间';
