-- plugin: kb v1.0.0
-- ===== 知识库模块 v1 · 管道五表（表清单见本插件 README「三・数据与迁移边界」） =====
-- 本表结构为 v1 终态（含 vector 扩展与 halfvec(1024) 维度）。
-- 表：kb_blob 原始件（内容寻址 sha256）/ kb_blob_ref 文档↔原始件引用登记（共享件的引用计数面，
--     供清理作业判「无引用 blob」——插件自管表）/
--     kb_extract 提取物（重切不重解的独立真源档）/ kb_chunk_vectors 向量行（halfvec(1024) 单空间）/
--     kb_produce_state 库级导入/派生续跑水位（单调）。
-- 纪律：语义闭集（kind/embedding_space/状态）在代码侧（不加 DB CHECK；语义闭集走代码闸，加值零迁移）；
--       索引一次到位（每个索引可指认引用查询）；表名/索引名带 __PREFIX__，命名 uk_/idx_；
--       时间列 created_at/updated_at（timestamptz）；外键列建索引但不建 FK 约束（仓内零 FK 先例）；
--       禁 CREATE INDEX CONCURRENTLY（本机制整文件隐式事务）；无 BEGIN/COMMIT。
-- 向量口径：halfvec(1024) + SET STORAGE PLAIN + 按 library_id CLUSTER；
--       禁 ANN 索引（v1 可见域内精确扫描；容量超阈重新裁决，需负责人确认）。
-- 幂等链：blob=sha256；提取物=uk(document_id,extractor,version)；向量=uk(chunk_id,model,text_hash)（落行 ON CONFLICT DO NOTHING）。

-- 向量面依赖：halfvec(1024)（pgvector；此处幂等对齐）
CREATE EXTENSION IF NOT EXISTS vector;

-- ===== ① kb_blob 原始件（私有内容寻址原始字节；静态服务不暴露，C7） =====
CREATE TABLE IF NOT EXISTS "__PREFIX__kb_blob" (
    "sha256"     char(64)     NOT NULL,
    "mime"       varchar(100) NOT NULL DEFAULT '',
    "size"       bigint       NOT NULL DEFAULT 0,
    "bytes"      bytea        NOT NULL DEFAULT ''::bytea,
    "created_at" timestamptz,
    PRIMARY KEY ("sha256")
);
COMMENT ON TABLE "__PREFIX__kb_blob" IS '原始件：私有内容寻址原始字节（sha256 主键=去重身份；不复用通用上传/attachments——C7）；对象存储 adapter 位=读写单点，列面不变';
COMMENT ON COLUMN "__PREFIX__kb_blob"."sha256" IS '内容身份 sha256(原始字节)（主键；同字节只存一份）';
COMMENT ON COLUMN "__PREFIX__kb_blob"."mime" IS 'MIME 类型';
COMMENT ON COLUMN "__PREFIX__kb_blob"."size" IS '字节数（成本/存储报表口径）';
COMMENT ON COLUMN "__PREFIX__kb_blob"."bytes" IS '原始字节（v1 唯一真源；迁对象存储时本列可空置并另开方案，列面保留）';
COMMENT ON COLUMN "__PREFIX__kb_blob"."created_at" IS '创建时间';

-- ===== ② kb_blob_ref 文档↔原始件引用登记（引用计数面：共享件清理判据） =====
CREATE TABLE IF NOT EXISTS "__PREFIX__kb_blob_ref" (
    "id"          bigserial PRIMARY KEY,
    "library_id"  bigint      NOT NULL DEFAULT 0,
    "document_id" bigint      NOT NULL DEFAULT 0,
    "sha256"      char(64)    NOT NULL DEFAULT '',
    "created_at"  timestamptz
);
-- 同文档同原始件只登记一行（ON CONFLICT 幂等：重复导入/重跑不重复计数）
CREATE UNIQUE INDEX IF NOT EXISTS "__PREFIX__uk_kb_blob_ref_document_blob" ON "__PREFIX__kb_blob_ref" ("document_id", "sha256");
-- 引用计数反查面（清理作业：blob 是否仍被引用）
CREATE INDEX IF NOT EXISTS "__PREFIX__idx_kb_blob_ref_sha256" ON "__PREFIX__kb_blob_ref" ("sha256");
-- 库级清理轴（kb:purge：按库删引用登记）
CREATE INDEX IF NOT EXISTS "__PREFIX__idx_kb_blob_ref_library_id" ON "__PREFIX__kb_blob_ref" ("library_id", "id");
COMMENT ON TABLE "__PREFIX__kb_blob_ref" IS '文档↔原始件引用登记（内容寻址 blob 是共享件：本表是引用计数面，清理判据=无引用行；硬删表，随文档/库清理随之删）';
COMMENT ON COLUMN "__PREFIX__kb_blob_ref"."id" IS 'ID';
COMMENT ON COLUMN "__PREFIX__kb_blob_ref"."library_id" IS '所属库ID（冗余过滤列：kb:purge 按库清）';
COMMENT ON COLUMN "__PREFIX__kb_blob_ref"."document_id" IS '所属文档ID（逻辑外键，不建 FK 约束）';
COMMENT ON COLUMN "__PREFIX__kb_blob_ref"."sha256" IS '被引用原始件 sha256（kb_blob.sha256；逻辑外键，不建 FK 约束）';
COMMENT ON COLUMN "__PREFIX__kb_blob_ref"."created_at" IS '创建时间';

-- ===== ③ kb_extract 提取物（一次提取的产物；重切不重解的真源档） =====
CREATE TABLE IF NOT EXISTS "__PREFIX__kb_extract" (
    "id"          bigserial   PRIMARY KEY,
    "document_id" bigint      NOT NULL DEFAULT 0,
    "kind"        varchar(16) NOT NULL DEFAULT 'text',
    "extractor"   varchar(64) NOT NULL DEFAULT '',
    "version"     varchar(32) NOT NULL DEFAULT '',
    "text"        text        NOT NULL DEFAULT '',
    "stats"       jsonb,
    "created_at"  timestamptz
);
-- 提取幂等：同文档同提取器同版本只留一行（无软删谓词——改提取口径=换 version 新行）
CREATE UNIQUE INDEX IF NOT EXISTS "__PREFIX__uk_kb_extract_document_extractor_version" ON "__PREFIX__kb_extract" ("document_id", "extractor", "version");
COMMENT ON TABLE "__PREFIX__kb_extract" IS '提取物：原文的一次提取产物（重切不重解）；锚指向本表 text 的 verbatim 文本（切片 char 区间/文本哈希的定位真源）';
COMMENT ON COLUMN "__PREFIX__kb_extract"."id" IS 'ID';
COMMENT ON COLUMN "__PREFIX__kb_extract"."document_id" IS '所属文档ID（逻辑外键，不建 FK 约束）';
COMMENT ON COLUMN "__PREFIX__kb_extract"."kind" IS '提取类型闭集(代码侧):text=文本层,ocr=光学识别,asr=语音转写,caption=图像描述';
COMMENT ON COLUMN "__PREFIX__kb_extract"."extractor" IS '提取器标识（如 kb-text；与 version 合成提取器身份 extractor@version——build_hash 组成项）';
COMMENT ON COLUMN "__PREFIX__kb_extract"."version" IS '提取器版本（口径升级=新版本新行，旧行保留；uk 组成项）';
COMMENT ON COLUMN "__PREFIX__kb_extract"."text" IS '提取文本（verbatim 原文；零文本禁静默入库——空/覆盖率/乱码率超阈=响亮失败）';
COMMENT ON COLUMN "__PREFIX__kb_extract"."stats" IS '提取读数 JSON（页数/覆盖率/乱码率/ocr_used/置信度等；NULL=未采集）';
COMMENT ON COLUMN "__PREFIX__kb_extract"."created_at" IS '创建时间';

-- ===== ④ kb_chunk_vectors 向量行（切片在某模型下的嵌入；halfvec(1024) 单空间） =====
CREATE TABLE IF NOT EXISTS "__PREFIX__kb_chunk_vectors" (
    "id"              bigserial   PRIMARY KEY,
    "chunk_id"        bigint      NOT NULL DEFAULT 0,
    "library_id"      bigint      NOT NULL DEFAULT 0,
    "model"           varchar(64) NOT NULL DEFAULT '',
    "text_hash"       char(40)    NOT NULL DEFAULT '',
    "embedding"       halfvec(1024),
    "embedding_space" varchar(32) NOT NULL DEFAULT '',
    "created_at"      timestamptz
);
-- 嵌入幂等：同切片同模型同文本哈希只留一行（落行 ON CONFLICT DO NOTHING，先例：产物键去重）
CREATE UNIQUE INDEX IF NOT EXISTS "__PREFIX__uk_kb_chunk_vectors_chunk_model_text" ON "__PREFIX__kb_chunk_vectors" ("chunk_id", "model", "text_hash");
-- 向量腿过滤轴（可读库集先下推，再做 <=> 排序）
CREATE INDEX IF NOT EXISTS "__PREFIX__idx_kb_chunk_vectors_library_id" ON "__PREFIX__kb_chunk_vectors" ("library_id");
ALTER TABLE "__PREFIX__kb_chunk_vectors" ALTER COLUMN "embedding" SET STORAGE PLAIN;
-- 按 library_id 聚簇（写入后物理相邻；0 行时执行）
CLUSTER "__PREFIX__kb_chunk_vectors" USING "__PREFIX__idx_kb_chunk_vectors_library_id";
COMMENT ON TABLE "__PREFIX__kb_chunk_vectors" IS '向量行（索引不是事实表：可删可重算）；v1 精确扫描（无 ANN 索引，容量超阈重新裁决，需负责人确认）';
COMMENT ON COLUMN "__PREFIX__kb_chunk_vectors"."id" IS 'ID';
COMMENT ON COLUMN "__PREFIX__kb_chunk_vectors"."chunk_id" IS '切片ID（kb_chunk.id；逻辑外键，不建 FK 约束）';
COMMENT ON COLUMN "__PREFIX__kb_chunk_vectors"."library_id" IS '所属库ID（冗余过滤列；向量腿第一条件，CLUSTER 轴）';
COMMENT ON COLUMN "__PREFIX__kb_chunk_vectors"."model" IS '嵌入模型调用名（换模型=新行；uk 组成项）';
COMMENT ON COLUMN "__PREFIX__kb_chunk_vectors"."text_hash" IS '被嵌入文本（ChunkTextOf 派生口径）sha1（口径变更=重嵌；uk 组成项）';
COMMENT ON COLUMN "__PREFIX__kb_chunk_vectors"."embedding" IS 'halfvec(1024)：STORAGE PLAIN、按 library_id CLUSTER、无 ANN 索引（v1 精确扫描）';
COMMENT ON COLUMN "__PREFIX__kb_chunk_vectors"."embedding_space" IS '向量空间（升级位：v1 文本单空间=空；原生多模态/双写升级时填空间名——C5）';
COMMENT ON COLUMN "__PREFIX__kb_chunk_vectors"."created_at" IS '创建时间';

-- ===== ⑤ kb_produce_state 库级水位（导入/派生续跑；单调推进） =====
CREATE TABLE IF NOT EXISTS "__PREFIX__kb_produce_state" (
    "library_id"       bigint      NOT NULL,
    "last_document_id" bigint      NOT NULL DEFAULT 0,
    "updated_at"       timestamptz,
    PRIMARY KEY ("library_id")
);
COMMENT ON TABLE "__PREFIX__kb_produce_state" IS '库级水位：last_document_id = 「≤ 该 id 的全部候选文档已终态」的连续前缀；逐窗追平、无失败窗后 GREATEST 单调推进（失败窗不堵尾）';
COMMENT ON COLUMN "__PREFIX__kb_produce_state"."library_id" IS '库ID（主键；逻辑外键，不建 FK 约束）';
COMMENT ON COLUMN "__PREFIX__kb_produce_state"."last_document_id" IS '已产连续前缀（kb_document.id 水位；0=未产；单调不回退）';
COMMENT ON COLUMN "__PREFIX__kb_produce_state"."updated_at" IS '最近更新时间';
