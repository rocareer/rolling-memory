// plugin: kb v1.0.0
// Package kb 知识库检索腿仓储（过滤下推）：四腿 SQL（exact / fts / trgm / vector）、
// 切片逐字取回（chunk/get 核对入口）与向量内容探测。
//
// 纪律（T5 硬条款 + SQL 纪律）：
//   - **每条腿第一条件 = library_id = ANY(:readable)**（可读库集由服务层经 ACL 单点解析后传入；
//     空集由服务层直接短路，禁「先召回后过滤」）；chunkBaseCond 是「可读域 + 文档在场条件」的单点常量，
//     各腿共用一个字符串（改口径 = 改这一处）；
//   - 文档面条件：d.deleted_at IS NULL ∧ d.active_build <> ”
//     ∧ c.build_hash = d.active_build——**不再要求 build_state='ready'**：重建/换窗/失败在途时
//     旧完成 build 的行仍在场（C2「旧 build 保留至新 build 完成」）⇒ 仍可召回，消除召回空洞；
//     未产出 active_build（空）与旧 build 残留（build_hash ≠ active_build）恒不命中；
//   - 向量腿另加「当前模型身份」条件（v.model = 已解析嵌入模型标识；历史 agent 名身份行自然排除）；
//   - 表名经 Table() 前缀单点（与 GORM NamingStrategy 同源），表名禁裸写；值全参数化、标识符仅常量；
//   - 显式列（禁 SELECT *）；腿内排序确定性（相关性降序 → 主键托底）；扫描帽 ScanCap()（v1 精确扫描；
//     kb.search.scan_cap 配置化，缺省回落 5000 先例值）；
//   - 本包零语义：权限/模式/融合属服务层（service/kb/search）。
package kb

import (
	"context"
	"fmt"
	"strings"

	"github.com/ai-go-hub/ai-go-admin/internal/infra/config"
	"github.com/ai-go-hub/ai-go-admin/internal/infra/database"
	"github.com/ai-go-hub/ai-go-admin/plugins/kb/model"

	"github.com/ai-go-hub/ai-go-admin/plugins/kb/kbcfg"
	"gorm.io/gorm"
)

// ScanCapDefault v1 精确扫描帽缺省（可见域内精确扫描；帽值先例 5000——容量超阈重新裁决 ANN）。
// 帽值配置化（kb.search.scan_cap），本常量即缺省回落值（≤0/未配 = 本值）。
const ScanCapDefault = 5000

// ScanCap 当前生效扫描帽（配置单点：kb.search.scan_cap；≤0 = 缺省 5000）。
// 语义不变：腿窗帽（LIMIT）≤ 帽值；超阈重新裁决 ANN 的触发判据见 config/kb.yaml 的 kb.search 段（登记点）。
func ScanCap() int {
	if v := kbcfg.Get().Search.ScanCap; v > 0 {
		return v
	}
	return ScanCapDefault
}

// Store 检索腿 SQL 唯一口（机械 SQL，零语义）
type Store struct {
	db *gorm.DB
}

// NewStore 创建 Store（默认走全局 db 实例）
func NewStore() *Store { return &Store{} }

// WithDB 以链式方式自定义 DB 实例（测试/事务场景）
func (s *Store) WithDB(db *gorm.DB) *Store {
	s.db = db
	return s
}

// DB 当前 DB 实例（自定义优先，否则全局）
func (s *Store) DB() *gorm.DB {
	if s.db != nil {
		return s.db
	}
	return database.DB()
}

// Table 表名（前缀单点；与 GORM NamingStrategy 的 TablePrefix 同源）。
// 配置未初始化（单测/工具）回落裸前缀——不触 config.Get（未初始化会 panic）。
func (s *Store) Table(name string) string {
	if !config.Initialized() {
		return "kb_" + name
	}
	return config.Get().Database.Prefix + "kb_" + name
}

// ChunkHit 腿命中行（显式列；text 为 verbatim 原文——snippet 由服务层自本列切片，禁摘要改写）
type ChunkHit struct {
	ChunkID    uint    `gorm:"column:chunk_id"`
	DocumentID uint    `gorm:"column:document_id"`
	LibraryID  uint    `gorm:"column:library_id"`
	Title      string  `gorm:"column:title"`
	SourceRef  string  `gorm:"column:source_ref"`
	Locator    string  `gorm:"column:locator"`
	CharStart  int     `gorm:"column:char_start"`
	CharEnd    int     `gorm:"column:char_end"`
	PageFrom   int     `gorm:"column:page_from"`
	PageTo     int     `gorm:"column:page_to"`
	TimeFrom   int64   `gorm:"column:time_from"`
	TimeTo     int64   `gorm:"column:time_to"`
	TextHash   string  `gorm:"column:text_hash"`
	Text       string  `gorm:"column:text"`
	Rank       float64 `gorm:"column:rank"` // 腿内排序读数（exact 恒 1.0；RRF 只用名次，不用该分）
}

// chunkHitColumns 腿命中显式列（禁 SELECT *；text 为 snippet 真源故入选，正文之外的 jsonb 大字段不选；
// source_ref 供 locator.path（file 档）用）
const chunkHitColumns = "c.id AS chunk_id, c.document_id, c.library_id, c.locator, c.char_start, c.char_end," +
	" c.page_from, c.page_to, c.time_from, c.time_to, c.text_hash, c.text, d.title AS title, d.source_ref"

// chunkBaseCond 可读域**第一条件**单点（T5）：`c.library_id IN ?`（可读库集下推；GORM 对其展开为
// 等值列表——语义即 library_id = ANY(:readable)）+ 文档在场条件（未删 ∧ 已产出 active_build ∧
// 切片属当前 active_build）。参数顺序：readable（[]int64，见 int8Slice）。逐腿 SQL 均以本常量起头
// （第一条件；空集由服务层短路，不发兜底查询）。
//
// 口径裁决（文档在场条件）：原为「就绪三连」（d.build_state='ready' ∧ …），
// 重建/换窗/失败在途时旧完成 build 行虽在场也被整体摘除——与C2「旧 build 保留至新 build 完成」
// 的连续性意图相悖（召回空洞：文档一启动重建即从检索面消失至新 build 换出）。现改为
// `d.active_build <> ” ∧ c.build_hash = d.active_build`：未产出 active_build 的文档仍恒不命中
// （build_state∈{registered,…} 且 active_build 空）；旧 build 残留行（build_hash ≠ active_build）
// 仍恒不命中；换窗后旧行随 purge 清除（不再依赖 ready 摘除）。断言口径见 search_legs_test.go。
// 注：active_build 为 char(40)，与 ” 的比较按 bpchar 尾空格不敏感语义（零长度/全空格均判空）。
//
// 写法注（实测留痕；独立复现并校正机制描述）：`ANY(?)` 数组绑定在 GORM Raw 路径
// 不可行——clause.Expr 展开切片变量时，`?` 前一字符为 `(`（`ANY(` 即此形）则逐元素展开为逗号分隔
// 占位符且**不补括号** ⇒ 生成 `= ANY($1, $2)` 语法非法（gorm v1.31.1 clause/expression.go afterParenthesis
// 分支；整切片走 AddVar 才补括号，故 `IN ?` 合法）；故一切可读集下推落为 `IN ?`（标量等值列表），
// 过滤下推语义逐字不变（禁后过滤未变）。
const chunkBaseCond = "c.library_id IN ? AND d.deleted_at IS NULL AND d.active_build <> '' AND c.build_hash = d.active_build"

// int8Slice uint 集 → int64 参数（kb_* 主键为 bigserial=bigint；`IN ?` 展开后每元素按标量绑定，
// int64 是 pgx 必然可编码的类型——uint 直传有驱动面风险，统一转换单点）。
func int8Slice(ids []uint) []int64 {
	out := make([]int64, len(ids))
	for i, v := range ids {
		out[i] = int64(v)
	}
	return out
}

// ==================== 标签过滤下推（T5：chunk/document 两路子查询拼进同一条件） ====================

// TagFilter 标签过滤入参（nil 或空集 = 不过滤）。**下推语义**（T5 硬条款）：
// 标签条件与可读集一样进 SQL WHERE，先于 LIMIT 截断；禁「召回后应用层过滤」。
//
// 作用域说明：TagIDs 由调用方给出（多库检索时可为跨库标签并集）——**无需额外可见性校验**：
// ① link 只由绑定面写入（跨库引用在写侧已拒）；② 子查询以本腿可读域内的 c.id / c.document_id
// 为锚，域外对象的 link 天然不可命中（不泄露、不漏召回）。
type TagFilter struct {
	TagIDs []uint
}

// tagCond 标签过滤条件单点（T5：**两段子查询拼进同一条件**——切片级 link 命中 c.id，
// 文档级 link 命中 c.document_id；两段 OR 后与其它条件同 AND 级）；空集 = 无条件（"", nil）。
//
// 参数顺序：object_type('chunk')、tag_ids、object_type('document')、tag_ids——与 SQL 文本出现次序一致。
// 索引：两段子查询均命中 uk_kb_tag_link_tag_object(tag_id,object_type,object_id) 前缀（标签 → 对象方向）。
func (s *Store) tagCond(tags *TagFilter) (string, []any) {
	if tags == nil || len(tags.TagIDs) == 0 {
		return "", nil
	}
	link := s.Table("tag_link")
	cond := " AND (c.id IN (SELECT l1.object_id FROM " + link +
		" l1 WHERE l1.object_type = ? AND l1.tag_id IN ?)" +
		" OR c.document_id IN (SELECT l2.object_id FROM " + link +
		" l2 WHERE l2.object_type = ? AND l2.tag_id IN ?))"
	// 两处切片各自转换：同一个 []int64 绑定两次虽可行，但独立切片免踩驱动/展开路径的共享假设
	return cond, []any{model.KBTagObjectChunk, int8Slice(tags.TagIDs), model.KBTagObjectDocument, int8Slice(tags.TagIDs)}
}

// ==================== 命中行标签回填（item.tags[]） ====================

// ChunkTagNames 命中行标签名回填（chunk 级 link ∪ 所属文档级 link——与 tagCond 的两段锚定口径同源；
// 硬删即已解绑故不在场；词表已删标签的悬挂 link 保留其名，与详情面 ListObjectTags「如实回显不静默吞」
// 同口径）。names 按 link.tag_id 升序、逐 chunk 去重（同名多标签不重复出项）；同一数据读数确定。
// 缺省返回非 nil 空 map（调用方按「无标签 = 空数组」回执）。
func (s *Store) ChunkTagNames(ctx context.Context, chunkIDs []uint) (map[uint][]string, error) {
	out := make(map[uint][]string, len(chunkIDs))
	if len(chunkIDs) == 0 {
		return out, nil
	}
	link, tag := s.Table("tag_link"), s.Table("tag")
	sqlText := "SELECT c.id AS chunk_id, t.name AS name FROM " + s.Table("chunk") + " c" +
		" JOIN " + link + " l ON (l.object_type = ? AND l.object_id = c.id)" +
		" OR (l.object_type = ? AND l.object_id = c.document_id)" +
		" JOIN " + tag + " t ON t.id = l.tag_id" +
		" WHERE c.id IN ? ORDER BY c.id ASC, l.tag_id ASC"
	var rows []struct {
		ChunkID uint   `gorm:"column:chunk_id"`
		Name    string `gorm:"column:name"`
	}
	if err := s.DB().WithContext(ctx).Raw(sqlText,
		model.KBTagObjectChunk, model.KBTagObjectDocument, int8Slice(chunkIDs)).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("kb 命中行标签回填失败: %w", err)
	}
	seen := make(map[uint]map[string]bool, len(out))
	for i := range rows {
		name := strings.TrimSpace(rows[i].Name)
		if name == "" {
			continue
		}
		if seen[rows[i].ChunkID] == nil {
			seen[rows[i].ChunkID] = map[string]bool{}
		}
		if seen[rows[i].ChunkID][name] {
			continue
		}
		seen[rows[i].ChunkID][name] = true
		out[rows[i].ChunkID] = append(out[rows[i].ChunkID], name)
	}
	return out, nil
}

// scanLimit 腿窗帽（≤ ScanCap()；<1 回落 ScanCap()——调用方语义限定正常路径）
func scanLimit(limit int) int {
	cap := ScanCap()
	if limit <= 0 || limit > cap {
		return cap
	}
	return limit
}

// chunkSelect 腿查询骨架（固定列面 + JOIN；表名经 Table 单点，标识符常量）
func (s *Store) chunkSelect(extraCol string) string {
	return "SELECT " + chunkHitColumns + ", " + extraCol + " FROM " + s.Table("chunk") + " c" +
		" JOIN " + s.Table("document") + " d ON d.id = c.document_id" +
		" WHERE " + chunkBaseCond
}

// SearchExact 逐字子串腿（词面腿之 exact）：`c.match_text ILIKE %q%`（通配符转义；trgm GIN 支撑
// 前后 % 查询）。**匹配面 = match_text 派生文本列**（ChunkTextOf 单点；原匹配
// verbatim c.text——标题路径/表头等派生词面不可达，本批三面同切；trgm GIN 换挂见 match_text 派生列）。
// snippet 真源仍是 verbatim c.text（禁摘要改写），本腿只改匹配面、不改返回列面。
// 无相关性读数 ⇒ 恒 1.0，按主键序（确定性）。
// tags 非空时代入标签过滤下推（tagCond；与可读集同层进 WHERE，先于 LIMIT 截断——T5）。
func (s *Store) SearchExact(ctx context.Context, readable []uint, likePattern string, window int, tags *TagFilter) ([]ChunkHit, error) {
	if len(readable) == 0 || likePattern == "" {
		return nil, nil
	}
	tagSQL, tagArgs := s.tagCond(tags)
	sqlText := s.chunkSelect("1.0::float8 AS rank") + tagSQL +
		" AND c.match_text ILIKE ? ORDER BY c.id ASC LIMIT ?"
	args := append([]any{int8Slice(readable)}, tagArgs...)
	args = append(args, likePattern, scanLimit(window))
	return s.scan(ctx, sqlText, args...)
}

// SearchFTS 词面腿（tsv @@ websearch_to_tsquery；tsv 为 DB 生成列，恒自 match_text 派生——
// match_text 派生列；查询配置由调用方 ingest.ResolveFTSConfig 解析后传入，写读同点由 DDL+单点保证）。
// 排序：ts_rank 降序 → 主键托底（tags 同 SearchExact）。
func (s *Store) SearchFTS(ctx context.Context, readable []uint, query, ftsConfig string, window int, tags *TagFilter) ([]ChunkHit, error) {
	if len(readable) == 0 || query == "" {
		return nil, nil
	}
	tagSQL, tagArgs := s.tagCond(tags)
	sqlText := s.chunkSelect("ts_rank(c.tsv, websearch_to_tsquery(?::regconfig, ?))::float8 AS rank") + tagSQL +
		" AND c.tsv @@ websearch_to_tsquery(?::regconfig, ?)" +
		" ORDER BY rank DESC, c.id ASC LIMIT ?"
	// 占位符按 SQL 文本出现次序：SELECT 内 rank 表达式（2）→ 基条件（1：readable）→ 标签过滤（tags）→ @@ 谓词（2）→ LIMIT
	args := append([]any{ftsConfig, query, int8Slice(readable)}, tagArgs...)
	args = append(args, ftsConfig, query, scanLimit(window))
	return s.scan(ctx, sqlText, args...)
}

// SearchTrgm 词面近似腿（CJK 无分词场景召回；：仅 CJK 查询启用——闸在调用方）：
// pg_trgm word_similarity + `<%` 运算符（无前后通配注入面）+ 表达式 GIN 支撑；
// **匹配面 = match_text 派生文本列**（与 exact 腿同列，三面同切）；
// 阈值经 set_config（事务内；会话局部，不改全局）。排序：相似度降序 → 主键托底（tags 同 SearchExact）。
func (s *Store) SearchTrgm(ctx context.Context, readable []uint, query string, threshold float64, window int, tags *TagFilter) ([]ChunkHit, error) {
	if len(readable) == 0 || query == "" {
		return nil, nil
	}
	tagSQL, tagArgs := s.tagCond(tags)
	sqlText := s.chunkSelect("word_similarity(?, c.match_text)::float8 AS rank") + tagSQL +
		" AND ? <% c.match_text ORDER BY rank DESC, c.id ASC LIMIT ?"
	// 占位符按 SQL 文本出现次序：SELECT 内 word_similarity（1）→ 基条件（1：readable）→ 标签过滤（tags）→ `<%` 谓词（1）→ LIMIT
	args := append([]any{query, int8Slice(readable)}, tagArgs...)
	args = append(args, query, scanLimit(window))

	var hits []ChunkHit
	err := s.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		thr := fmt.Sprintf("%.4f", threshold)
		if err := tx.Exec("SELECT set_config(?, ?::text, true)", "pg_trgm.word_similarity_threshold", thr).Error; err != nil {
			return err
		}
		return tx.Raw(sqlText, args...).Scan(&hits).Error
	})
	if err != nil {
		return nil, fmt.Errorf("kb 检索腿 trgm 失败: %w", err)
	}
	return hits, nil
}

// vectorBaseCond 向量腿**第一条件**单点（T5：过滤先于 `<=>` 排序；与 chunkBaseCond 同款
// 文档在场条件，别名 v/c/d 区分——向量列面走 v）。另加**当前模型身份**条件 `v.model = ?`
// （解析后的嵌入模型标识；历史 agent 名身份与其它身份行按不匹配自然排除，禁跨空间混检索）。
// 参数顺序：readable、model；写法注同 chunkBaseCond。
const vectorBaseCond = "v.library_id IN ? AND v.model = ? AND d.deleted_at IS NULL AND d.active_build <> '' AND c.build_hash = d.active_build"

// SearchVector 向量腿（可读库集先下推，再在过滤后做 `<=>` 排序；v1 精确扫描无 ANN 索引）。
// 余弦距离升序 → 主键托底；embedding 走 pgvector 文本形态绑定（?::halfvec）。
// model = 当前模型身份（调用方（服务层）经 embed.ModelIdentity() 解析；空身份由服务层响亮失败，
// 此处机械短路不发兜底查询——`v.model = ”` 语义上会让空身份行混入，禁）。
// tags 非空时同样先过滤后排序（标签条件与可读集同层，先于 `<=>` 排序——T5）。
func (s *Store) SearchVector(ctx context.Context, readable []uint, embedding, model string, window int, tags *TagFilter) ([]ChunkHit, error) {
	if len(readable) == 0 || embedding == "" || model == "" {
		return nil, nil
	}
	tagSQL, tagArgs := s.tagCond(tags)
	sqlText := "SELECT " + chunkHitColumns + ", (v.embedding <=> ?::halfvec)::float8 AS rank" +
		" FROM " + s.Table("chunk_vectors") + " v" +
		" JOIN " + s.Table("chunk") + " c ON c.id = v.chunk_id" +
		" JOIN " + s.Table("document") + " d ON d.id = c.document_id" +
		" WHERE " + vectorBaseCond + tagSQL +
		" ORDER BY rank ASC, c.id ASC LIMIT ?"
	// 占位符按 SQL 文本出现次序：SELECT 内 `<=>`（1）→ 基条件（readable、model）→ 标签过滤 → LIMIT
	args := append([]any{embedding, int8Slice(readable), model}, tagArgs...)
	args = append(args, scanLimit(window))
	return s.scan(ctx, sqlText, args...)
}

// HasVectorContent 可读域内是否存在**可用**向量行（auto 模式「无向量内容自动降级词面」的判据；
// 与向量腿同一可读域条件 ∧ 同一当前模型身份——身份不匹配（历史 agent 名等）不算「有内容」，
// 探针与腿同判据，免起一条恒空腿还白付一次查询嵌入）。
//
// 写法：可读集两处一律 `IN ?`（与 chunkBaseCond 同款）——`= ANY(?)` 在 GORM Raw 路径下切片被逐元素
// 展开为 `ANY($1,$2,…)`：≥2 库 ⇒ PG 语法错（SQLSTATE 42601），单元素 ⇒ 标量绑给 _int8[] 编码失败；
// 即 auto（缺省）档在嵌入档开的真实部署下曾**恒失败**（非降级、整查报错）。实测已证。
// model 空 = 无可用身份 ⇒ false/nil（机械短路；响亮失败归服务层）。
func (s *Store) HasVectorContent(ctx context.Context, readable []uint, model string) (bool, error) {
	if len(readable) == 0 || model == "" {
		return false, nil
	}
	sqlText := "SELECT 1 FROM " + s.Table("chunk_vectors") + " v WHERE v.library_id IN ? AND v.model = ? AND EXISTS (" +
		"SELECT 1 FROM " + s.Table("chunk") + " c JOIN " + s.Table("document") + " d ON d.id = c.document_id" +
		" WHERE c.id = v.chunk_id AND " + chunkBaseCond + ") LIMIT 1"
	var one []int
	if err := s.DB().WithContext(ctx).Raw(sqlText, int8Slice(readable), model, int8Slice(readable)).Scan(&one).Error; err != nil {
		return false, fmt.Errorf("kb 向量内容探测失败: %w", err)
	}
	return len(one) > 0, nil
}

// HasForeignVectorContent 可读域内是否存在**非当前身份**的向量行（auto 档「当前身份无内容」时的
// 归因探针）：命中 = 域内躺着历史身份（旧 agent 名口径）或其它模型空间的向量行——
// 即「身份切换后未重嵌」的典型形态。服务层据此在降级词面时落一条点名日志（否则静默失召回：
// 既不报错、也不提示重产入口）。判据域与 HasVectorContent 逐字同构，仅 model 条件取不等；
// 探针失败由服务层按 best-effort 消费（不阻断检索）。
func (s *Store) HasForeignVectorContent(ctx context.Context, readable []uint, model string) (bool, error) {
	if len(readable) == 0 || model == "" {
		return false, nil
	}
	sqlText := "SELECT 1 FROM " + s.Table("chunk_vectors") + " v WHERE v.library_id IN ? AND v.model <> ? AND EXISTS (" +
		"SELECT 1 FROM " + s.Table("chunk") + " c JOIN " + s.Table("document") + " d ON d.id = c.document_id" +
		" WHERE c.id = v.chunk_id AND " + chunkBaseCond + ") LIMIT 1"
	var one []int
	if err := s.DB().WithContext(ctx).Raw(sqlText, int8Slice(readable), model, int8Slice(readable)).Scan(&one).Error; err != nil {
		return false, fmt.Errorf("kb 异身份向量内容探测失败: %w", err)
	}
	return len(one) > 0, nil
}

// ChunkByID 按 chunk_id 取逐字原文行（chunk/get 核对入口；同一可读域条件——不可见与不存在同型：
// 服务层把 ErrRecordNotFound 与空集一律映射为 ErrDenied）。
func (s *Store) ChunkByID(ctx context.Context, readable []uint, chunkID uint) (*ChunkHit, error) {
	if len(readable) == 0 || chunkID == 0 {
		return nil, nil
	}
	sqlText := s.chunkSelect("0::float8 AS rank") + " AND c.id = ? LIMIT 1"
	hits, err := s.scan(ctx, sqlText, int8Slice(readable), chunkID)
	if err != nil {
		return nil, err
	}
	if len(hits) == 0 {
		return nil, nil
	}
	return &hits[0], nil
}

// scan 执行腿语句并扫描命中行（无行 = 空集，不视为错误）
func (s *Store) scan(ctx context.Context, sqlText string, args ...any) ([]ChunkHit, error) {
	var hits []ChunkHit
	if err := s.DB().WithContext(ctx).Raw(sqlText, args...).Scan(&hits).Error; err != nil {
		return nil, fmt.Errorf("kb 检索腿查询失败: %w", err)
	}
	return hits, nil
}
