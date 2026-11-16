-- plugin: kb v1.0.0
-- ===== 知识库模块 v1 · 标签四表（表清单见本插件 README「三・数据与迁移边界」） =====
-- 本表结构为 v1 终态。
-- 表：① kb_tag_group 标签组（分类/命名空间；scope 闭集 platform|library，library_id 0=平台）
--     ② kb_tag 标签（组内平铺：组内唯一、组间可同名；不建 pid 树——T1）
--     ③ kb_tag_link 标签关联（**硬删**：解除即删行；uk ON CONFLICT 幂等；留痕走 kb_audit_log 不靠软删）
--     ④ kb_tag_rule 标签规则（match_type 闭集在代码侧：path_glob|filename_regex|suffix|meta_eq）
-- 作用域口径：统一表 + scope 闭集；**library_id 0=平台**（不用 NULL——部分唯一索引下 NULL
--   互不相等会使约束失效；先例：同仓既有「0 = 匿名/哨兵」口径）；库可见标签 = 平台组 ∪ 本库组；
--   库组标签不可被其它库引用（服务层校验，不建 FK——仓内零 FK 先例）。
-- 语义闭集（scope/object_type/match_type/status/enabled）一律走代码闸、加值零迁移（语义闭集走代码闸，不加 CHECK）。
-- 纪律：索引一次到位（每个索引可指认引用查询）；表名/索引名带 __PREFIX__，命名 uk_/idx_；
--       软删表配「软删部分唯一 + deleted_at 单列索引」；外键列建索引但不建 FK 约束；
--       禁 CREATE INDEX CONCURRENTLY（本机制整文件隐式事务）；无 BEGIN/COMMIT。
-- 引用查询对照（性能四问的「走哪个索引」）：
--   uk_kb_tag_group_scope_library_name ← 库可见组清单（平台组 ∪ 本库组，等值前缀）与建组重名拦
--   uk_kb_tag_group_name                ← 组内标签清单（等值前缀）与建标签重名拦（组间可同名）
--   uk_kb_tag_link_tag_object           ← 标签过滤下推（tag_id + object_type 前缀；过滤子查询）
--   idx_kb_tag_link_object              ← 对象侧反查/重切重放/清理（object_type + object_id 前缀）
--   idx_kb_tag_rule_library_priority    ← 规则求值扫描（库 + enabled + priority）

-- ===== ① kb_tag_group 标签组（命名空间；平台组由超管维护，库组由属主/后台维护） =====
CREATE TABLE IF NOT EXISTS "__PREFIX__kb_tag_group" (
    "id"         bigserial   PRIMARY KEY,
    "scope"      varchar(16) NOT NULL DEFAULT '',
    "library_id" bigint      NOT NULL DEFAULT 0,
    "name"       varchar(64) NOT NULL DEFAULT '',
    "weigh"      bigint      NOT NULL DEFAULT 0,
    "updated_at" timestamptz,
    "created_at" timestamptz,
    "deleted_at" timestamptz
);
-- 同作用域内组名唯一（软删豁免：删后可复用）
CREATE UNIQUE INDEX IF NOT EXISTS "__PREFIX__uk_kb_tag_group_scope_library_name" ON "__PREFIX__kb_tag_group" ("scope", "library_id", "name") WHERE "deleted_at" IS NULL;
-- 组清单排序轴（库可见组 = 平台组 ∪ 本库组，按 weigh 排序展示）
CREATE INDEX IF NOT EXISTS "__PREFIX__idx_kb_tag_group_scope_library_weigh" ON "__PREFIX__kb_tag_group" ("scope", "library_id", "weigh");
CREATE INDEX IF NOT EXISTS "__PREFIX__idx_kb_tag_group_deleted_at" ON "__PREFIX__kb_tag_group" ("deleted_at");
COMMENT ON TABLE "__PREFIX__kb_tag_group" IS '标签组（分类/命名空间）；库可见标签 = 平台组 ∪ 本库组（作用域口径）';
COMMENT ON COLUMN "__PREFIX__kb_tag_group"."id" IS 'ID';
COMMENT ON COLUMN "__PREFIX__kb_tag_group"."scope" IS '作用域闭集(代码侧):platform=平台(library_id 恒 0，超管维护),library=库(library_id=kb_library.id，属主/后台维护)';
COMMENT ON COLUMN "__PREFIX__kb_tag_group"."library_id" IS '所属库ID（0=平台；禁 NULL——部分唯一索引下 NULL 互不相等使约束失效）';
COMMENT ON COLUMN "__PREFIX__kb_tag_group"."name" IS '组名（同作用域内唯一，软删豁免 uk）';
COMMENT ON COLUMN "__PREFIX__kb_tag_group"."weigh" IS '排序权重（大者在前；榜单轴 idx(scope,library_id,weigh)）';
COMMENT ON COLUMN "__PREFIX__kb_tag_group"."updated_at" IS '更新时间';
COMMENT ON COLUMN "__PREFIX__kb_tag_group"."created_at" IS '创建时间';
COMMENT ON COLUMN "__PREFIX__kb_tag_group"."deleted_at" IS '删除时间（软删；库组删除后其标签不可再被新绑定引用，既有 link 保留待人工处置）';

-- ===== ② kb_tag 标签（组内平铺：组内唯一、组间可同名；不建树） =====
CREATE TABLE IF NOT EXISTS "__PREFIX__kb_tag" (
    "id"         bigserial   PRIMARY KEY,
    "group_id"   bigint      NOT NULL DEFAULT 0,
    "name"       varchar(64) NOT NULL DEFAULT '',
    "weigh"      bigint      NOT NULL DEFAULT 0,
    "status"     smallint    NOT NULL DEFAULT 1,
    "updated_at" timestamptz,
    "created_at" timestamptz,
    "deleted_at" timestamptz
);
-- 组内标签名唯一（软删豁免；组间可同名——跨组同名靠 group_id 区分）
CREATE UNIQUE INDEX IF NOT EXISTS "__PREFIX__uk_kb_tag_group_name" ON "__PREFIX__kb_tag" ("group_id", "name") WHERE "deleted_at" IS NULL;
-- 组内标签清单轴（排序 weigh）
CREATE INDEX IF NOT EXISTS "__PREFIX__idx_kb_tag_group_weigh" ON "__PREFIX__kb_tag" ("group_id", "weigh");
CREATE INDEX IF NOT EXISTS "__PREFIX__idx_kb_tag_deleted_at" ON "__PREFIX__kb_tag" ("deleted_at");
COMMENT ON TABLE "__PREFIX__kb_tag" IS '标签（组内平铺：组内唯一、组间可同名；不建 pid 树）；引用数按需 count（走 kb_tag_link 的 uk 前缀），不设引用计数物化列（写热点）';
COMMENT ON COLUMN "__PREFIX__kb_tag"."id" IS 'ID（kb_tag_link.tag_id 的取值域）';
COMMENT ON COLUMN "__PREFIX__kb_tag"."group_id" IS '所属标签组ID（kb_tag_group.id；作用域经组继承）';
COMMENT ON COLUMN "__PREFIX__kb_tag"."name" IS '标签名（组内唯一，软删豁免 uk；组间同名合法）';
COMMENT ON COLUMN "__PREFIX__kb_tag"."weigh" IS '排序权重（组内清单轴）';
COMMENT ON COLUMN "__PREFIX__kb_tag"."status" IS '状态:0=禁用,1=启用（禁用标签不可新绑定，既有 link 保留）';
COMMENT ON COLUMN "__PREFIX__kb_tag"."updated_at" IS '更新时间';
COMMENT ON COLUMN "__PREFIX__kb_tag"."created_at" IS '创建时间';
COMMENT ON COLUMN "__PREFIX__kb_tag"."deleted_at" IS '删除时间（软删；既有 link 不自动清——回撤属投影重建，随 / 重放能力处置）';

-- ===== ③ kb_tag_link 标签关联（硬删：解除即删行；留痕走审计） =====
CREATE TABLE IF NOT EXISTS "__PREFIX__kb_tag_link" (
    "id"          bigserial   PRIMARY KEY,
    "tag_id"      bigint      NOT NULL DEFAULT 0,
    "object_type" varchar(16) NOT NULL DEFAULT '',
    "object_id"   bigint      NOT NULL DEFAULT 0,
    "operator_id" bigint      NOT NULL DEFAULT 0,
    "created_at"  timestamptz
);
-- 同标签同对象唯一（**无软删谓词**：解除即删行；本 uk 即 ON CONFLICT 幂等落点）
CREATE UNIQUE INDEX IF NOT EXISTS "__PREFIX__uk_kb_tag_link_tag_object" ON "__PREFIX__kb_tag_link" ("tag_id", "object_type", "object_id");
-- 对象侧反查/重切重放/清理轴（object_type + object_id 前缀）
CREATE INDEX IF NOT EXISTS "__PREFIX__idx_kb_tag_link_object" ON "__PREFIX__kb_tag_link" ("object_type", "object_id", "tag_id");
COMMENT ON TABLE "__PREFIX__kb_tag_link" IS '标签关联（硬删：解除即删行）；uk(tag_id,object_type,object_id)=ON CONFLICT 幂等；留痕走 kb_audit_log（tag_add/tag_remove），不靠软删';
COMMENT ON COLUMN "__PREFIX__kb_tag_link"."id" IS 'ID';
COMMENT ON COLUMN "__PREFIX__kb_tag_link"."tag_id" IS '标签ID（kb_tag.id；作用域校验在服务层——跨库引用拒绝）';
COMMENT ON COLUMN "__PREFIX__kb_tag_link"."object_type" IS '锚定对象类型闭集(代码侧):document=文档(kb_document.id),chunk=切片(kb_chunk.id)';
COMMENT ON COLUMN "__PREFIX__kb_tag_link"."object_id" IS '锚定对象ID（document→kb_document.id；chunk→kb_chunk.id——重切按 (document_id,text_hash) 重放）';
COMMENT ON COLUMN "__PREFIX__kb_tag_link"."operator_id" IS '操作者ID（admin→admins.id，user→users.id；两 id 空间禁混用，轴归属以 kb_audit_log 为准；system 作业面=0）';
COMMENT ON COLUMN "__PREFIX__kb_tag_link"."created_at" IS '创建时间（重放重挂=删旧行+插新行，本列即重放时刻）';

-- ===== ④ kb_tag_rule 标签规则（随导入/重导批次求值；match_type 闭集在代码侧） =====
CREATE TABLE IF NOT EXISTS "__PREFIX__kb_tag_rule" (
    "id"         bigserial    PRIMARY KEY,
    "library_id" bigint       NOT NULL DEFAULT 0,
    "name"       varchar(64)  NOT NULL DEFAULT '',
    "enabled"    smallint     NOT NULL DEFAULT 1,
    "match_type" varchar(16)  NOT NULL DEFAULT '',
    "pattern"    varchar(255) NOT NULL DEFAULT '',
    "tag_id"     bigint       NOT NULL DEFAULT 0,
    "priority"   bigint       NOT NULL DEFAULT 0,
    "updated_at" timestamptz,
    "created_at" timestamptz,
    "deleted_at" timestamptz
);
-- 同库内规则名唯一（软删豁免）
CREATE UNIQUE INDEX IF NOT EXISTS "__PREFIX__uk_kb_tag_rule_library_name" ON "__PREFIX__kb_tag_rule" ("library_id", "name") WHERE "deleted_at" IS NULL;
-- 规则求值扫描轴（库 + enabled + priority；重跑同结果：priority 升序确定性）
CREATE INDEX IF NOT EXISTS "__PREFIX__idx_kb_tag_rule_library_priority" ON "__PREFIX__kb_tag_rule" ("library_id", "enabled", "priority");
CREATE INDEX IF NOT EXISTS "__PREFIX__idx_kb_tag_rule_deleted_at" ON "__PREFIX__kb_tag_rule" ("deleted_at");
COMMENT ON TABLE "__PREFIX__kb_tag_rule" IS '标签规则（导入/重导入批次内求值，重跑同结果幂等；match_type 闭集在代码侧）；规则删除后的既有标签回撤 = 投影重建（随重放能力落地）';
COMMENT ON COLUMN "__PREFIX__kb_tag_rule"."id" IS 'ID';
COMMENT ON COLUMN "__PREFIX__kb_tag_rule"."library_id" IS '所属库ID（规则随库作用，不跨库）';
COMMENT ON COLUMN "__PREFIX__kb_tag_rule"."name" IS '规则名（库内唯一，软删豁免 uk）';
COMMENT ON COLUMN "__PREFIX__kb_tag_rule"."enabled" IS '启用:0=停用,1=启用（停用不参与求值；既有 link 保留）';
COMMENT ON COLUMN "__PREFIX__kb_tag_rule"."match_type" IS '匹配类型闭集(代码侧):path_glob=路径通配,filename_regex=文件名正则,suffix=后缀,meta_eq=元信息等值';
COMMENT ON COLUMN "__PREFIX__kb_tag_rule"."pattern" IS '匹配式（按 match_type 解释；写侧校验）';
COMMENT ON COLUMN "__PREFIX__kb_tag_rule"."tag_id" IS '命中后所打标签（kb_tag.id；作用域校验在服务层）';
COMMENT ON COLUMN "__PREFIX__kb_tag_rule"."priority" IS '优先级（小者先评；同 priority 按 id 升序，重跑确定性）';
COMMENT ON COLUMN "__PREFIX__kb_tag_rule"."updated_at" IS '更新时间';
COMMENT ON COLUMN "__PREFIX__kb_tag_rule"."created_at" IS '创建时间';
COMMENT ON COLUMN "__PREFIX__kb_tag_rule"."deleted_at" IS '删除时间（软删；规则删除后的既有标签回撤 = 投影重建，不随删即时清）';
