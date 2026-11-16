-- plugin: kb v1.0.0
-- ===== 知识库模块 v1 · 核心六表（表清单见本插件 README「三・数据与迁移边界」） =====
-- 本表结构为 v1 终态（match_text 派生列 + tsv DB 生成列）：kb_chunk 直接以 match_text 派生文本列
--   + tsv DB 生成列（自 match_text）+ trgm 挂 match_text 落盘，不携带「后补迁移」与存量回填步骤。
-- 表：kb_library 库（聚合根）/ kb_document 文档（去重键 content_hash）/ kb_chunk 切片（派生面，无软删，重切即删重建）
--     / kb_grant 授权（可见性单一来源）/ kb_team 团队映射（绑 user_groups.name）/ kb_audit_log 审计（只增不改不删）。
-- 纪律：语义闭集（owner_kind/subject_kind/perm/action/source_kind/build_state）在代码侧（不加 DB CHECK；语义闭集走代码闸，加值零迁移）；
--       索引一次到位（每个索引可指认引用查询）；表名/索引名带 __PREFIX__，命名 uk_/idx_；
--       软删表配「软删部分唯一 + deleted_at 单列索引」；外键列建索引但不建 FK 约束（仓内零 FK 先例）；
--       禁 CREATE INDEX CONCURRENTLY（本机制整文件隐式事务）；无 BEGIN/COMMIT。
-- 索引口径注①：kb_grant 的 uk 含 perm——「write 授权不扩读（需读写双授时落两行）」要求同主体两行可表达。
-- 索引口径注②：kb_chunk.config_hash 列来自「chunk 行落 config_snapshot + config_hash」口径（关键列清单非穷举）。
-- 索引口径注③：kb_chunk / kb_audit_log 只增（重切即删重建、审计只增不改不删）→ 仅 created_at（先例：只增表的索引面收敛到创建时间）。
-- 匹配面（召回口径）：exact/trgm/tsv/嵌入文本一律走 match_text（chunk.ChunkTextOf 单点派生）——text 列保
--   verbatim 原文作锚/逐字核对真源；trgm 索引与 tsv 生成列均挂 match_text（先例：同仓既有 trgm 索引）。
-- 扩展依赖：pg_trgm（本迁移）；vector 扩展见 2_pipeline。受限权限环境需预装扩展（运维步骤见交付说明）。

CREATE EXTENSION IF NOT EXISTS pg_trgm;

-- ===== ① kb_library 知识库（聚合根：文档 + 切片 + 授权；唯一属主 owner_kind/owner_id） =====
CREATE TABLE IF NOT EXISTS "__PREFIX__kb_library" (
    "id"           bigserial PRIMARY KEY,
    "owner_kind"   varchar(16)  NOT NULL DEFAULT '',
    "owner_id"     bigint       NOT NULL DEFAULT 0,
    "name"         varchar(128) NOT NULL DEFAULT '',
    "description"  varchar(512) NOT NULL DEFAULT '',
    "chunk_config" jsonb,
    "status"       smallint     NOT NULL DEFAULT 1,
    "updated_at"   timestamptz,
    "created_at"   timestamptz,
    "deleted_at"   timestamptz
);
-- 同属主同库名唯一（软删豁免：删后可复用）
CREATE UNIQUE INDEX IF NOT EXISTS "__PREFIX__uk_kb_library_owner_name" ON "__PREFIX__kb_library" ("owner_kind", "owner_id", "name") WHERE "deleted_at" IS NULL;
-- 属主库清单主查询轴（排序轴 updated_at）
CREATE INDEX IF NOT EXISTS "__PREFIX__idx_kb_library_owner_updated" ON "__PREFIX__kb_library" ("owner_kind", "owner_id", "updated_at");
CREATE INDEX IF NOT EXISTS "__PREFIX__idx_kb_library_deleted_at" ON "__PREFIX__kb_library" ("deleted_at");
COMMENT ON TABLE "__PREFIX__kb_library" IS '知识库：文档+切片+授权的聚合根；可见性单一来源=kb_grant（本表仅承载归属与管理权）';
COMMENT ON COLUMN "__PREFIX__kb_library"."id" IS 'ID';
COMMENT ON COLUMN "__PREFIX__kb_library"."owner_kind" IS '属主类型闭集(代码侧):system=系统级(owner_id=0,仅后台管理),user=用户级(owner_id=users.id)；创建后不可改';
COMMENT ON COLUMN "__PREFIX__kb_library"."owner_id" IS '属主ID（system=0；username 可被后台改，禁作键）';
COMMENT ON COLUMN "__PREFIX__kb_library"."name" IS '库名（同属主内唯一，软删豁免 uk）';
COMMENT ON COLUMN "__PREFIX__kb_library"."description" IS '描述';
COMMENT ON COLUMN "__PREFIX__kb_library"."chunk_config" IS '库级切片配置 JSON（三层覆盖 global▷library▷document 的中间层；NULL=未配置；未知键写侧拒绝）';
COMMENT ON COLUMN "__PREFIX__kb_library"."status" IS '状态:0=禁用,1=启用（禁用库对一切读路径不可见）';
COMMENT ON COLUMN "__PREFIX__kb_library"."updated_at" IS '更新时间（属主清单排序轴）';
COMMENT ON COLUMN "__PREFIX__kb_library"."created_at" IS '创建时间';
COMMENT ON COLUMN "__PREFIX__kb_library"."deleted_at" IS '删除时间（软删即刻读路径不可见；异步 kb:purge 清 document/chunk/向量/授权）';

-- ===== ② kb_document 文档（库内可追溯知识单元；去重键 content_hash） =====
CREATE TABLE IF NOT EXISTS "__PREFIX__kb_document" (
    "id"               bigserial PRIMARY KEY,
    "library_id"       bigint       NOT NULL DEFAULT 0,
    "title"            varchar(255) NOT NULL DEFAULT '',
    "source_kind"      varchar(32)  NOT NULL DEFAULT '',
    "source_ref"       text         NOT NULL DEFAULT '',
    "content_hash"     char(40)     NOT NULL DEFAULT '',
    "mime"             varchar(100) NOT NULL DEFAULT '',
    "size"             bigint       NOT NULL DEFAULT 0,
    "build_state"      varchar(16)  NOT NULL DEFAULT 'registered',
    "active_build"     char(40)     NOT NULL DEFAULT '',
    "failed_stage"     varchar(32)  NOT NULL DEFAULT '',
    "failed_reason"    varchar(512) NOT NULL DEFAULT '',
    "cancel_requested" smallint     NOT NULL DEFAULT 0,
    "chunk_config"     jsonb,
    "created_by_kind"  varchar(16)  NOT NULL DEFAULT '',
    "created_by_id"    bigint       NOT NULL DEFAULT 0,
    "updated_at"       timestamptz,
    "created_at"       timestamptz,
    "deleted_at"       timestamptz
);
-- 导入幂等：同库同内容只登记一次（软删豁免；先例：部分唯一索引形态）
CREATE UNIQUE INDEX IF NOT EXISTS "__PREFIX__uk_kb_document_library_content" ON "__PREFIX__kb_document" ("library_id", "content_hash") WHERE "deleted_at" IS NULL;
-- 库文档清单主查询轴
CREATE INDEX IF NOT EXISTS "__PREFIX__idx_kb_document_library_id" ON "__PREFIX__kb_document" ("library_id", "id");
-- 失败/在跑清单轴（build_state 等值；也服务检索腿 ready 谓词）
CREATE INDEX IF NOT EXISTS "__PREFIX__idx_kb_document_library_build_state" ON "__PREFIX__kb_document" ("library_id", "build_state");
CREATE INDEX IF NOT EXISTS "__PREFIX__idx_kb_document_deleted_at" ON "__PREFIX__kb_document" ("deleted_at");
-- 列表快搜前置（>10 万行禁裸 ILIKE；仅 CJK 查询启用）
CREATE INDEX IF NOT EXISTS "__PREFIX__idx_kb_document_title_trgm" ON "__PREFIX__kb_document" USING gin ("title" gin_trgm_ops);
COMMENT ON TABLE "__PREFIX__kb_document" IS '库内可追溯知识单元（file/text/url）；去重键 content_hash（sha1）；源字节不落本表（私有 blob，落 kb_blob）';
COMMENT ON COLUMN "__PREFIX__kb_document"."id" IS 'ID';
COMMENT ON COLUMN "__PREFIX__kb_document"."library_id" IS '所属库ID（逻辑外键，不建 FK 约束）';
COMMENT ON COLUMN "__PREFIX__kb_document"."title" IS '标题（列表快搜轴，trgm GIN 支撑）';
COMMENT ON COLUMN "__PREFIX__kb_document"."source_kind" IS '来源闭集(代码侧):file=文件,text=文本,url=链接';
COMMENT ON COLUMN "__PREFIX__kb_document"."source_ref" IS '来源引用（外部 ref 文本；不复制文件）';
COMMENT ON COLUMN "__PREFIX__kb_document"."content_hash" IS '内容身份=原文 sha1（导入幂等键，uk 部分唯一）';
COMMENT ON COLUMN "__PREFIX__kb_document"."mime" IS 'MIME 类型';
COMMENT ON COLUMN "__PREFIX__kb_document"."size" IS '字节数';
COMMENT ON COLUMN "__PREFIX__kb_document"."build_state" IS '构建状态:registered/extracting/extracted/chunking/chunked/embedding/ready（旁路 failed/canceled 带 stage/reason）';
COMMENT ON COLUMN "__PREFIX__kb_document"."active_build" IS '当前生效 build_hash（重切原子换出点；空=未产出过；换窗期检索腿钉它）';
COMMENT ON COLUMN "__PREFIX__kb_document"."failed_stage" IS '失败阶段（build_state=failed 时有意义）';
COMMENT ON COLUMN "__PREFIX__kb_document"."failed_reason" IS '失败原因（响亮失败：零文本/覆盖率/乱码率超阈）';
COMMENT ON COLUMN "__PREFIX__kb_document"."cancel_requested" IS '取消请求:0=无,1=已请求（jobs Cancel 仅限 pending，运行中取消走本业务标志）';
COMMENT ON COLUMN "__PREFIX__kb_document"."chunk_config" IS '文档级切片配置 JSON（三层覆盖的末层；NULL=继承库/全局）';
COMMENT ON COLUMN "__PREFIX__kb_document"."created_by_kind" IS '创建者类型:admin=后台代建,user=前台（S7 写面）';
COMMENT ON COLUMN "__PREFIX__kb_document"."created_by_id" IS '创建者ID（admin→admins.id，user→users.id；两 id 空间分列禁混用）';
COMMENT ON COLUMN "__PREFIX__kb_document"."updated_at" IS '更新时间';
COMMENT ON COLUMN "__PREFIX__kb_document"."created_at" IS '创建时间';
COMMENT ON COLUMN "__PREFIX__kb_document"."deleted_at" IS '删除时间（软删即刻 + kb:doc:<document_id> 入队清块/向量）';

-- ===== ③ kb_chunk 切片（派生面：无软删，重切即删重建） =====
CREATE TABLE IF NOT EXISTS "__PREFIX__kb_chunk" (
    "id"              bigserial PRIMARY KEY,
    "library_id"      bigint      NOT NULL DEFAULT 0,
    "document_id"     bigint      NOT NULL DEFAULT 0,
    "build_hash"      char(40)    NOT NULL DEFAULT '',
    "config_hash"     char(40)    NOT NULL DEFAULT '',
    "chunker"         varchar(32) NOT NULL DEFAULT '',
    "chunker_version" varchar(32) NOT NULL DEFAULT '',
    "seq"             int         NOT NULL DEFAULT 0,
    "locator"         text        NOT NULL DEFAULT '',
    "char_start"      int         NOT NULL DEFAULT 0,
    "char_end"        int         NOT NULL DEFAULT 0,
    "heading_path"    jsonb,
    "page_from"       int         NOT NULL DEFAULT 0,
    "page_to"         int         NOT NULL DEFAULT 0,
    "time_from"       bigint      NOT NULL DEFAULT 0,
    "time_to"         bigint      NOT NULL DEFAULT 0,
    "table_meta"      jsonb,
    "text"            text        NOT NULL DEFAULT '',
    "match_text"      text        NOT NULL DEFAULT '',
    "text_hash"       char(40)    NOT NULL DEFAULT '',
    "token_count"     int         NOT NULL DEFAULT 0,
    "config_snapshot" jsonb,
    "tsv"             tsvector    NOT NULL GENERATED ALWAYS AS (to_tsvector('simple'::regconfig, "match_text")) STORED,
    "created_at"      timestamptz
);
-- 构建内块序唯一（无软删谓词；换出删旧 build 命中本 uk 前缀）
CREATE UNIQUE INDEX IF NOT EXISTS "__PREFIX__uk_kb_chunk_document_build_seq" ON "__PREFIX__kb_chunk" ("document_id", "build_hash", "seq");
-- 库切片清单主查询轴
CREATE INDEX IF NOT EXISTS "__PREFIX__idx_kb_chunk_library_id" ON "__PREFIX__kb_chunk" ("library_id", "id");
-- 词面腿 tsv（DB 生成列，恒自 match_text 派生：to_tsvector('simple', match_text)；Go 不读写，写读同源由 DDL 保证）
CREATE INDEX IF NOT EXISTS "__PREFIX__idx_kb_chunk_tsv" ON "__PREFIX__kb_chunk" USING GIN ("tsv");
-- CJK 无分词召回腿 + exact 腿 ILIKE 支撑（匹配面 = match_text 派生文本；仅 CJK 查询启用）
CREATE INDEX IF NOT EXISTS "__PREFIX__idx_kb_chunk_match_text_trgm" ON "__PREFIX__kb_chunk" USING gin ("match_text" gin_trgm_ops);
COMMENT ON TABLE "__PREFIX__kb_chunk" IS '切片（派生面：无软删，重切即删重建）；定位真值=char 区间+text_hash+chunker_version（seq 仅显示缓存）';
COMMENT ON COLUMN "__PREFIX__kb_chunk"."id" IS 'ID';
COMMENT ON COLUMN "__PREFIX__kb_chunk"."library_id" IS '所属库ID（冗余过滤列，检索腿第一条件）';
COMMENT ON COLUMN "__PREFIX__kb_chunk"."document_id" IS '所属文档ID（逻辑外键，不建 FK 约束）';
COMMENT ON COLUMN "__PREFIX__kb_chunk"."build_hash" IS '构建身份 sha1=sha1(content_hash|config_hash|chunker_version|extractor@version)';
COMMENT ON COLUMN "__PREFIX__kb_chunk"."config_hash" IS '有效切片配置 sha1（build_hash 组成项；可自 config_snapshot 复算）';
COMMENT ON COLUMN "__PREFIX__kb_chunk"."chunker" IS '切片策略名（代码闭集注册表：fixed/heading/table/code/…；加策略=闭集+版本+1）';
COMMENT ON COLUMN "__PREFIX__kb_chunk"."chunker_version" IS '切片器版本（锚可复算要素之一；换版本=换 build）';
COMMENT ON COLUMN "__PREFIX__kb_chunk"."seq" IS '块序（显示缓存，非定位真值；512→256 换档不得据它反推锚）';
COMMENT ON COLUMN "__PREFIX__kb_chunk"."locator" IS '锚=<doc_locator>#pN（锚语法同构，可反解析回父单元）';
COMMENT ON COLUMN "__PREFIX__kb_chunk"."char_start" IS '字符区间起（定位真值，rune 口径）';
COMMENT ON COLUMN "__PREFIX__kb_chunk"."char_end" IS '字符区间止（定位真值，rune 口径）';
COMMENT ON COLUMN "__PREFIX__kb_chunk"."heading_path" IS '标题路径 JSON 数组（heading 策略产出）';
COMMENT ON COLUMN "__PREFIX__kb_chunk"."page_from" IS '起始页（1 起；0=不适用——pdf/图片面）';
COMMENT ON COLUMN "__PREFIX__kb_chunk"."page_to" IS '截止页（0=不适用）';
COMMENT ON COLUMN "__PREFIX__kb_chunk"."time_from" IS '时间区间起（BIGINT 秒；0=不适用——音视频/会话）';
COMMENT ON COLUMN "__PREFIX__kb_chunk"."time_to" IS '时间区间止（BIGINT 秒；0=不适用）';
COMMENT ON COLUMN "__PREFIX__kb_chunk"."table_meta" IS '表格元信息 JSON（表头/行列区间；table 策略产出）';
COMMENT ON COLUMN "__PREFIX__kb_chunk"."text" IS '切片正文（verbatim 原文；锚/逐字核对真源，不掺派生前缀）。匹配面（exact/trgm/tsv/嵌入）一律走 match_text 列（ChunkTextOf 单点）';
COMMENT ON COLUMN "__PREFIX__kb_chunk"."match_text" IS '派生文本（匹配面唯一真源；= chunk.ChunkTextOf：标题路径前缀+表头+verbatim 正文）。写侧单点填充，空串=未回填';
COMMENT ON COLUMN "__PREFIX__kb_chunk"."text_hash" IS '切片正文 sha1（重切重放/幂等判据）';
COMMENT ON COLUMN "__PREFIX__kb_chunk"."token_count" IS 'token 估算数（成本口径）';
COMMENT ON COLUMN "__PREFIX__kb_chunk"."config_snapshot" IS '本次切分的有效配置快照 JSON（复算 config_hash 的载体）';
COMMENT ON COLUMN "__PREFIX__kb_chunk"."tsv" IS '词面腿 tsvector（DB 生成列，恒自 match_text 派生：to_tsvector(''simple'', match_text)；Go 不读写，写读同源由 DDL 保证）';
COMMENT ON COLUMN "__PREFIX__kb_chunk"."created_at" IS '创建时间（重切即删重建，不设 updated_at）';
COMMENT ON INDEX "__PREFIX__idx_kb_chunk_match_text_trgm" IS 'exact 腿 ILIKE 前后通配支撑（匹配面 = match_text 派生文本；仅 CJK 查询启用 trgm 腿）';

-- ===== ④ kb_grant 授权（可见性单一来源：可枚举 grant 行） =====
CREATE TABLE IF NOT EXISTS "__PREFIX__kb_grant" (
    "id"              bigserial PRIMARY KEY,
    "library_id"      bigint      NOT NULL DEFAULT 0,
    "subject_kind"    varchar(16) NOT NULL DEFAULT '',
    "subject_id"      bigint      NOT NULL DEFAULT 0,
    "perm"            varchar(16) NOT NULL DEFAULT 'read',
    "granted_by_kind" varchar(16) NOT NULL DEFAULT '',
    "granted_by_id"   bigint      NOT NULL DEFAULT 0,
    "updated_at"      timestamptz,
    "created_at"      timestamptz,
    "deleted_at"      timestamptz
);
-- 同库同主体同权限轴唯一（软删豁免：撤销后可重授；读写双授=两行）
CREATE UNIQUE INDEX IF NOT EXISTS "__PREFIX__uk_kb_grant_library_subject_perm" ON "__PREFIX__kb_grant" ("library_id", "subject_kind", "subject_id", "perm") WHERE "deleted_at" IS NULL;
-- 可读集反查面（组/用户 → 命中 library_id 集；每请求一次）
CREATE INDEX IF NOT EXISTS "__PREFIX__idx_kb_grant_subject" ON "__PREFIX__kb_grant" ("subject_kind", "subject_id") WHERE "deleted_at" IS NULL;
CREATE INDEX IF NOT EXISTS "__PREFIX__idx_kb_grant_deleted_at" ON "__PREFIX__kb_grant" ("deleted_at");
COMMENT ON TABLE "__PREFIX__kb_grant" IS '授权行（可见性单一来源）；公开=subject_kind=all 一行；撤销=软删（即时生效）；读写两轴正交';
COMMENT ON COLUMN "__PREFIX__kb_grant"."id" IS 'ID';
COMMENT ON COLUMN "__PREFIX__kb_grant"."library_id" IS '被授权库ID（逻辑外键，不建 FK 约束）';
COMMENT ON COLUMN "__PREFIX__kb_grant"."subject_kind" IS '主体类型闭集(代码侧):all=公开(subject_id=0 哨兵),team=团队(subject_id=kb_team.id),user=用户(subject_id=users.id)';
COMMENT ON COLUMN "__PREFIX__kb_grant"."subject_id" IS '主体ID（all 恒 0；subject_id=0 是 all 哨兵——入口守卫禁 actor_id≤0 全库放大）';
COMMENT ON COLUMN "__PREFIX__kb_grant"."perm" IS '权限轴闭集(代码侧):read|write（正交：write 不扩读；读谓词显式要求 perm=read；双授落两行）';
COMMENT ON COLUMN "__PREFIX__kb_grant"."granted_by_kind" IS '操作者类型:admin|user';
COMMENT ON COLUMN "__PREFIX__kb_grant"."granted_by_id" IS '操作者ID（与 kb_audit_log 互为佐证）';
COMMENT ON COLUMN "__PREFIX__kb_grant"."updated_at" IS '更新时间（撤销/重授留痕面在 kb_audit_log，本列仅行状态）';
COMMENT ON COLUMN "__PREFIX__kb_grant"."created_at" IS '创建时间';
COMMENT ON COLUMN "__PREFIX__kb_grant"."deleted_at" IS '删除时间（撤销=软删，读路径即时不可见；重授=新行）';

-- ===== ⑤ kb_team 团队映射（稳定 id ↔ user_groups.name 绑定） =====
CREATE TABLE IF NOT EXISTS "__PREFIX__kb_team" (
    "id"             bigserial PRIMARY KEY,
    "group_name"     varchar(64)  NOT NULL DEFAULT '',
    "bound_group_id" bigint       NOT NULL DEFAULT 0,
    "display_name"   varchar(128) NOT NULL DEFAULT '',
    "status"         smallint     NOT NULL DEFAULT 1,
    "updated_at"     timestamptz,
    "created_at"     timestamptz,
    "deleted_at"     timestamptz
);
-- 组名唯一（软删豁免；按名绑定是仓内既定语义——同名即同语义组）
CREATE UNIQUE INDEX IF NOT EXISTS "__PREFIX__uk_kb_team_group_name" ON "__PREFIX__kb_team" ("group_name") WHERE "deleted_at" IS NULL;
CREATE INDEX IF NOT EXISTS "__PREFIX__idx_kb_team_deleted_at" ON "__PREFIX__kb_team" ("deleted_at");
COMMENT ON TABLE "__PREFIX__kb_team" IS '团队映射：稳定 id ↔ user_groups.name（按名匹配；成员判定仍走 MemberOfAny 语义单点）';
COMMENT ON COLUMN "__PREFIX__kb_team"."id" IS 'ID（kb_grant.subject_id 的 team 取值域）';
COMMENT ON COLUMN "__PREFIX__kb_team"."group_name" IS '绑定的组名（user_groups.name；组改名=改一行绑定，grant 行不动；软删豁免 uk）';
COMMENT ON COLUMN "__PREFIX__kb_team"."bound_group_id" IS '绑定时 FindByName 最早命中组ID（aigo kb check 巡检比对回收：现行 id 与之不符即点名）';
COMMENT ON COLUMN "__PREFIX__kb_team"."display_name" IS '显示名';
COMMENT ON COLUMN "__PREFIX__kb_team"."status" IS '状态:0=禁用,1=启用（禁用团队不参与读视图 teams(u) 判定）';
COMMENT ON COLUMN "__PREFIX__kb_team"."updated_at" IS '更新时间';
COMMENT ON COLUMN "__PREFIX__kb_team"."created_at" IS '创建时间';
COMMENT ON COLUMN "__PREFIX__kb_team"."deleted_at" IS '删除时间（删绑定后悬空授权不自动删，由巡检点名 + 人工重绑兜底）';

-- ===== ⑥ kb_audit_log 审计（只增不改不删，无软删） =====
CREATE TABLE IF NOT EXISTS "__PREFIX__kb_audit_log" (
    "id"          bigserial PRIMARY KEY,
    "library_id"  bigint      NOT NULL DEFAULT 0,
    "document_id" bigint      NOT NULL DEFAULT 0,
    "actor_kind"  varchar(16) NOT NULL DEFAULT '',
    "actor_id"    bigint      NOT NULL DEFAULT 0,
    "action"      varchar(32) NOT NULL DEFAULT '',
    "detail"      jsonb       NOT NULL DEFAULT '{}'::jsonb,
    "created_at"  timestamptz
);
CREATE INDEX IF NOT EXISTS "__PREFIX__idx_kb_audit_log_library_id" ON "__PREFIX__kb_audit_log" ("library_id", "id");
CREATE INDEX IF NOT EXISTS "__PREFIX__idx_kb_audit_log_actor" ON "__PREFIX__kb_audit_log" ("actor_kind", "actor_id", "id");
COMMENT ON TABLE "__PREFIX__kb_audit_log" IS '审计（只增不改不删）；grant_add/grant_remove/owner_transfer 与业务变更同事务写入，覆盖前台与作业行为';
COMMENT ON COLUMN "__PREFIX__kb_audit_log"."id" IS 'ID';
COMMENT ON COLUMN "__PREFIX__kb_audit_log"."library_id" IS '关联库ID（0=无库上下文）';
COMMENT ON COLUMN "__PREFIX__kb_audit_log"."document_id" IS '关联文档ID（0=非文档对象）';
COMMENT ON COLUMN "__PREFIX__kb_audit_log"."actor_kind" IS '操作者类型闭集(代码侧):admin|user|system（admins.id 与 users.id 两独立空间，禁混用）';
COMMENT ON COLUMN "__PREFIX__kb_audit_log"."actor_id" IS '操作者ID（admin→admins.id，user→users.id，system=0）';
COMMENT ON COLUMN "__PREFIX__kb_audit_log"."action" IS '动作闭集(代码侧):create/update/delete/grant_add/grant_remove/owner_transfer/purge/import';
COMMENT ON COLUMN "__PREFIX__kb_audit_log"."detail" IS '变更明细 JSON（前后值/授权主体等）';
COMMENT ON COLUMN "__PREFIX__kb_audit_log"."created_at" IS '创建时间（列表时间窗轴）';
