// plugin: kb v1.0.0
// 规则打标求值引擎（打标方式：
// 规则 = 导入/重导入批次内求值，重跑同结果、幂等）。
//
// 职责边界（三条，越其一即为越权）：
//   - **只读 + 纯函数**：本文件全量**不做任何写**——装载走 SELECT（kb_tag_rule ⨝ kb_tag ⨝ kb_tag_group），
//     求值是纯函数（输入主体 + 规则集，输出命中/跳过）。与包内候选作业同款边界：本包从不写
//     kb_tag_link / kb_tag（词表）。落行与留痕由服务层单点承担（本插件 service/kb 的 insertTagLinks
//     ON CONFLICT 幂等 + writeAudit 同事务；见该包 job.go applyRuleTags）——「写面单点，留痕走审计」。
//   - **闭集代码闸**：match_type ∈ {path_glob, filename_regex, suffix, meta_eq}（常量在 internal/model/
//     kb_tag.go）；闭集外、匹配式非法、模式指向不可用标签一律**跳过并给原因**（不静默、不当空结果吞掉），
//     调用方把原因落作业 warnings 与日志。
//   - **不扩权**：标签只作筛选面（可读性只看 owner/grant）；求值不读、不改任何权限面。
//
// 求值语义（本文件即口径单点；桥/后台/作业面一律经此，禁第二处口径）：
//   - 顺序：按 (priority ASC, id ASC)（DDL 索引 idx(library_id,enabled,priority) 的排序轴；
//     enabled=0 的规则在装载 SQL 过滤，不参与求值）；
//   - 命中：**每条命中规则各贡献其 tag_id**（扁平多标签语义——规则相互不遮蔽；priority 只决定
//     求值/落行顺序与回执顺序的确定性）。标签集按命中顺序去重后交给调用方；
//   - 主体（文档面属性，全部取自 kb_document 行）：
//     · 文档名 Name = source_ref 非空取 source_ref，否则回落 title（与管道判型的 name 口径同源——
//     ingest.Read 注释「name 直接取 kb_document.source_ref 或标题」、job.go 同名解析）；
//     · path_glob：对 Name 全串锚定通配。`*` = 任意长（**含路径分隔符**——`*.pdf` 可命中
//     `a.pdf`）、`?` = 恰一个字符（rune），其余字面量；大小写敏感（Unix 路径语义）；
//     · filename_regex：Go RE2 正则（无回溯风险），对 **Filename**（Name 的基名，`/` 与 `\` 都认）
//     **非锚定**匹配（作者自锚 ^…$）；大小写敏感（需要时作者写 (?i)）；
//     · suffix：对 Filename 后缀比对，大小写不敏感；匹配式可带或不带前导点（`pdf` ≡ `.pdf`），
//     `tar.gz` 亦可（不限于 filepath.Ext 的单段语义）；
//     · meta_eq：`field=value`（首个 `=` 分断；闭集字段 source_kind | mime，可加值零迁移=改此表）；
//     值比较大小写不敏感；mime 只比基础类型（`;` 参数段截去——与 ingest.DetectFormat 同口径）。
//
// 幂等与重切（链路口径，实现落在服务层）：
//   - 规则落**文档级** link（object_type=document）：匹配主体是文档属性，逐切片重复同一判定无增益；
//     文档级 link 不受重切影响（重放规则：仅切片级 link 随旧 build 删除、按 text_hash 重放）——
//     故「重切批次内重放」= 重跑求值的幂等空操作（ON CONFLICT DO NOTHING 零新行 ⇒ 零留痕）；
//   - **不删除**既有 link（规则停用/删除后的回撤 = 投影重建，登记为待实施）——重跑同结果
//     的语义是「同一规则集收敛到同一 link 集（只增不减）」，不是「回撤」。
//
// 词表污染防护的对应面：模式指向的标签必须「未删 ∧ 启用 ∧ 作用域覆盖本库」（装载期 LEFT JOIN
// 分类，不可用者进 Skipped 并带原因）——与绑定面「禁用/已删标签不可新绑定、库组标签不可跨库引用」
// 同口径；即便行由直连 SQL 造出（绕过管理面校验），求值面也不会写出跨库/停用的 link。
package tag

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/ai-go-hub/ai-go-admin/plugins/kb/model"

	"gorm.io/gorm"
)

// MaxRuleRows 单库启用规则装载硬线（防御性；SQL 纪律：读取有上限、绝不静默截断——触线即响亮失败）
const MaxRuleRows = 1000

// MaxSkipNotes 回执告警条数帽（超出只报计数，防刷屏；跳过项本身不静默——计数仍在）
const MaxSkipNotes = 5

// Subject 求值主体（文档面属性；全部来自 kb_document 行，禁旁路取数）
type Subject struct {
	Title      string // 标题（kb_document.title）
	Path       string // 来源路径/引用（kb_document.source_ref；可空）
	MIME       string // kb_document.mime
	SourceKind string // kb_document.source_kind（file|text|url）
}

// Name 文档名（source_ref 非空取之，否则回落 title；两端空白剔除）
func (s Subject) Name() string {
	if p := strings.TrimSpace(s.Path); p != "" {
		return p
	}
	return strings.TrimSpace(s.Title)
}

// Filename 文档名基名（`/` 与 `\` 均为分隔符；无分隔符 = 全文）
func (s Subject) Filename() string {
	name := s.Name()
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		return name[i+1:]
	}
	return name
}

// Rule 可求值规则（装载面投影；只含求值所需列）
type Rule struct {
	ID        uint   // kb_tag_rule.id
	Name      string // 规则名（回执/审计可读性）
	MatchType string // match_type 闭集
	Pattern   string // 匹配式（按 match_type 解释）
	TagID     uint   // 命中后所打标签
	Priority  int64  // 优先级（小者先评）
}

// RuleHit 单条命中
type RuleHit struct {
	RuleID   uint   `json:"rule_id"`
	RuleName string `json:"rule_name"`
	TagID    uint   `json:"tag_id"`
	Priority int64  `json:"priority"`
}

// RuleSkip 单条跳过（原因如实；装载期与求值期共用一个载体）
type RuleSkip struct {
	RuleID   uint   `json:"rule_id"`
	RuleName string `json:"rule_name"`
	Reason   string `json:"reason"`
}

// EvalResult 一次求值回执（纯数据；调用方据此落行/留痕/告警）
type EvalResult struct {
	Rules   int        `json:"rules"`   // 参与求值的规则数（可求值集）
	Hits    []RuleHit  `json:"hits"`    // 命中（按 priority,id 顺序）
	Skipped []RuleSkip `json:"skipped"` // 跳过（装载期 + 求值期；含原因）
	TagIDs  []uint     `json:"tag_ids"` // 命中标签集（按命中顺序去重；落行输入）
}

// LoadResult 装载回执（可求值集 + 装载期跳过）
type LoadResult struct {
	Rules   []Rule     // 可求值规则（enabled=1 且标签可用，按 priority,id 排序）
	Skipped []RuleSkip // 装载期跳过（标签不存在/已删/停用/跨库）
}

// ruleRow 装载行（LEFT JOIN 空侧 = 指针零值，据此分类不可用原因）
type ruleRow struct {
	ID        uint   `gorm:"column:id"`
	Name      string `gorm:"column:name"`
	MatchType string `gorm:"column:match_type"`
	Pattern   string `gorm:"column:pattern"`
	TagID     uint   `gorm:"column:tag_id"`
	Priority  int64  `gorm:"column:priority"`

	TagStatus      *uint8     `gorm:"column:tag_status"`
	TagDeletedAt   *time.Time `gorm:"column:tag_deleted_at"`
	GroupID        *uint      `gorm:"column:group_id"`
	GroupScope     *string    `gorm:"column:group_scope"`
	GroupLibrary   *uint      `gorm:"column:group_library_id"`
	GroupDeletedAt *time.Time `gorm:"column:group_deleted_at"`
}

// LoadRules 装载某库的启用规则：kb_tag_rule ⨝ kb_tag ⨝ kb_tag_group，按 (priority ASC, id ASC)。
//
// 索引面：等值 + 排序列恰好落在 idx_kb_tag_rule_library_priority(library_id, enabled, priority)
// （索引 idx_kb_tag_rule_scope_status 即「规则求值扫描」）；两个 JOIN 各走主键（标签/组）；
// 条数上限 MaxRuleRows（触线响亮失败，绝不静默截断）。
// 标签不可用（不存在/已删/停用/组缺失/作用域不覆盖本库）的规则进 Skipped（原因如实），不进可求值集。
func LoadRules(ctx context.Context, db *gorm.DB, libraryID uint) (*LoadResult, error) {
	if db == nil {
		return nil, fmt.Errorf("kb: 规则装载缺 db")
	}
	if libraryID == 0 {
		return nil, fmt.Errorf("kb: 规则装载缺 library_id")
	}
	sqlText := "SELECT r.id AS id, r.name AS name, r.match_type AS match_type, r.pattern AS pattern," +
		" r.tag_id AS tag_id, r.priority AS priority," +
		" t.status AS tag_status, t.deleted_at AS tag_deleted_at," +
		" g.id AS group_id, g.scope AS group_scope, g.library_id AS group_library_id, g.deleted_at AS group_deleted_at" +
		" FROM " + tableName(db, model.KBTagRule{}) + " r" +
		" LEFT JOIN " + tableName(db, model.KBTag{}) + " t ON t.id = r.tag_id" +
		" LEFT JOIN " + tableName(db, model.KBTagGroup{}) + " g ON g.id = t.group_id" +
		" WHERE r.library_id = ? AND r.enabled = 1 AND r.deleted_at IS NULL" +
		" ORDER BY r.priority ASC, r.id ASC LIMIT ?"
	rows := make([]ruleRow, 0, 16)
	if err := db.WithContext(ctx).Raw(sqlText, libraryID, MaxRuleRows+1).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("kb: 装载标签规则失败: %w", err)
	}
	if len(rows) > MaxRuleRows {
		return nil, fmt.Errorf("kb: 库 %d 的启用规则数超过求值上限 %d，拒绝静默截断（请收敛规则或分批）", libraryID, MaxRuleRows)
	}
	out := &LoadResult{Rules: make([]Rule, 0, len(rows)), Skipped: make([]RuleSkip, 0)}
	for i := range rows {
		r := rows[i]
		if reason := ruleUnusableReason(r, libraryID); reason != "" {
			out.Skipped = append(out.Skipped, RuleSkip{RuleID: r.ID, RuleName: r.Name, Reason: reason})
			continue
		}
		out.Rules = append(out.Rules, Rule{
			ID: r.ID, Name: r.Name, MatchType: r.MatchType, Pattern: r.Pattern, TagID: r.TagID, Priority: r.Priority,
		})
	}
	return out, nil
}

// ruleUnusableReason 标签可用性判定（空串 = 可用）。口径与绑定面（tag_link.go 的引用校验）同源：
// 禁用/已删标签不可新绑定；库作用域标签只可用于本库对象；作用域闭集外一律拒。
func ruleUnusableReason(r ruleRow, libraryID uint) string {
	switch {
	case r.TagStatus == nil || r.TagDeletedAt != nil:
		return fmt.Sprintf("标签 %d 不存在或已删（跳过：不向不可用词表项打标）", r.TagID)
	case *r.TagStatus != 1:
		return fmt.Sprintf("标签 %d 已停用（跳过：与绑定面口径一致——禁用标签不可新绑定）", r.TagID)
	case r.GroupID == nil || r.GroupDeletedAt != nil:
		return fmt.Sprintf("标签 %d 的组不存在或已删", r.TagID)
	case r.GroupScope == nil:
		return fmt.Sprintf("标签 %d 的作用域缺失", r.TagID)
	}
	switch scope := *r.GroupScope; scope {
	case model.KBTagScopePlatform:
		return ""
	case model.KBTagScopeLibrary:
		if r.GroupLibrary == nil || *r.GroupLibrary != libraryID {
			lib := uint(0)
			if r.GroupLibrary != nil {
				lib = *r.GroupLibrary
			}
			return fmt.Sprintf("标签 %d 属库 %d，与规则库 %d 不符（跳过：跨库引用拒绝）", r.TagID, lib, libraryID)
		}
		return ""
	default:
		return fmt.Sprintf("标签 %d 作用域未登记: %q（跳过）", r.TagID, scope)
	}
}

// EvaluateRules 纯函数求值：按 (priority ASC, id ASC) 逐条判定（引擎内重排入参副本一次——求值口径
// 不依赖调用方传入顺序；loader 的 ORDER BY 与之同序，双处一致是为装载上限截断的确定性），命中按序
// 累计（标签集去重保序）。跳过项（匹配式非法/类型未登记/空匹配式）不阻断其余规则；命中项不影响彼此。
func EvaluateRules(subject Subject, rules []Rule) EvalResult {
	sorted := make([]Rule, len(rules))
	copy(sorted, rules)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Priority != sorted[j].Priority {
			return sorted[i].Priority < sorted[j].Priority
		}
		return sorted[i].ID < sorted[j].ID
	})
	res := EvalResult{
		Rules:   len(sorted),
		Hits:    make([]RuleHit, 0, len(sorted)),
		Skipped: make([]RuleSkip, 0),
		TagIDs:  make([]uint, 0, len(sorted)),
	}
	seen := make(map[uint]struct{}, len(sorted))
	for i := range sorted {
		r := sorted[i]
		matched, skipReason := matchRule(subject, r)
		if skipReason != "" {
			res.Skipped = append(res.Skipped, RuleSkip{RuleID: r.ID, RuleName: r.Name, Reason: skipReason})
			continue
		}
		if !matched {
			continue
		}
		res.Hits = append(res.Hits, RuleHit{RuleID: r.ID, RuleName: r.Name, TagID: r.TagID, Priority: r.Priority})
		if _, dup := seen[r.TagID]; !dup {
			seen[r.TagID] = struct{}{}
			res.TagIDs = append(res.TagIDs, r.TagID)
		}
	}
	return res
}

// EvaluateForLibrary 求值入口（只读）：装载（LoadRules）→ 求值（EvaluateRules）→ 合并装载期跳过。
// 调用方（服务层）拿 EvalResult.TagIDs 落行、Skipped 落告警；本函数绝不写库。
func EvaluateForLibrary(ctx context.Context, db *gorm.DB, libraryID uint, subject Subject) (*EvalResult, error) {
	load, err := LoadRules(ctx, db, libraryID)
	if err != nil {
		return nil, err
	}
	res := EvaluateRules(subject, load.Rules)
	res.Skipped = append(load.Skipped, res.Skipped...)
	return &res, nil
}

// SkipNotes 跳过项告警文案（调用方落作业 warnings 与日志；至多 MaxSkipNotes 条 + 计数收口）。
func (r *EvalResult) SkipNotes() []string {
	if r == nil || len(r.Skipped) == 0 {
		return nil
	}
	out := make([]string, 0, MaxSkipNotes+1)
	for i := range r.Skipped {
		if i >= MaxSkipNotes {
			out = append(out, fmt.Sprintf("另有 %d 条标签规则被跳过（原因见上）", len(r.Skipped)-MaxSkipNotes))
			break
		}
		s := r.Skipped[i]
		out = append(out, fmt.Sprintf("标签规则 %d「%s」跳过：%s", s.RuleID, s.RuleName, s.Reason))
	}
	return out
}

// ==================== 四类匹配（纯函数；match_type 闭集在此单点解释） ====================

// matchRule 单条规则判定：返回 (命中, 跳过原因)；跳过原因为空串 = 判定有效（命中与否看前者）。
func matchRule(s Subject, r Rule) (bool, string) {
	if strings.TrimSpace(r.Pattern) == "" {
		return false, "匹配式为空（跳过：不把空式当通配）"
	}
	switch r.MatchType {
	case model.KBTagRuleMatchPathGlob:
		return globMatch(s.Name(), r.Pattern), ""
	case model.KBTagRuleMatchFilenameRegex:
		re, err := regexp.Compile(r.Pattern)
		if err != nil {
			return false, fmt.Sprintf("文件名正则非法: %v", err)
		}
		return re.MatchString(s.Filename()), ""
	case model.KBTagRuleMatchSuffix:
		return suffixMatch(s.Filename(), r.Pattern), ""
	case model.KBTagRuleMatchMetaEq:
		return metaEqMatch(s, r.Pattern)
	default:
		return false, fmt.Sprintf("match_type 未登记: %q（闭集 path_glob|filename_regex|suffix|meta_eq）", r.MatchType)
	}
}

// globMatch 通配匹配（`*` 任意长含分隔符、`?` 恰一 rune、其余字面量；全串锚定；大小写敏感）。
// 翻译为 RE2 正则：字面量全 QuoteMeta（无注入面），元字符只剩 `.*` / `.` 与锚点 \A…\z——
// 翻译式**恒合法**，故 MustCompile 不存在 panic 面（非「对用户输入用 MustCompile」）。
func globMatch(name, pattern string) bool {
	return regexp.MustCompile(globRegexp(pattern)).MatchString(name)
}

// globRegexp glob → 正则（确定性；模式为空由调用方前置拦截）
func globRegexp(pattern string) string {
	var b strings.Builder
	b.WriteString(`\A`)
	for _, r := range pattern {
		switch r {
		case '*':
			b.WriteString(`.*`)
		case '?':
			b.WriteString(`.`)
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	b.WriteString(`\z`)
	return b.String()
}

// suffixMatch 后缀匹配（大小写不敏感；匹配式前导点可带可不带；空式由调用方前置拦截）。
func suffixMatch(filename, pattern string) bool {
	p := strings.ToLower(strings.TrimSpace(pattern))
	if !strings.HasPrefix(p, ".") {
		p = "." + p
	}
	return strings.HasSuffix(strings.ToLower(strings.TrimSpace(filename)), p)
}

// metaEqFields meta_eq 字段闭集（文档面元信息；加值零迁移 = 改此表 + 注释）
var metaEqFields = map[string]func(Subject) string{
	"source_kind": func(s Subject) string { return strings.TrimSpace(s.SourceKind) },
	"mime":        func(s Subject) string { return baseMIME(s.MIME) },
}

// metaEqMatch 元信息等值（`field=value`；闭集字段见 metaEqFields；值比较大小写不敏感）。
func metaEqMatch(s Subject, pattern string) (bool, string) {
	i := strings.Index(pattern, "=")
	if i <= 0 {
		return false, "meta_eq 匹配式须为 field=value（闭集 source_kind|mime）"
	}
	field := strings.ToLower(strings.TrimSpace(pattern[:i]))
	value := strings.TrimSpace(pattern[i+1:])
	if value == "" {
		return false, "meta_eq 取值缺失（须为 field=value）"
	}
	get, ok := metaEqFields[field]
	if !ok {
		return false, fmt.Sprintf("meta_eq 字段未登记: %q（闭集 source_kind|mime）", field)
	}
	return strings.EqualFold(get(s), value), ""
}

// baseMIME mime 基础类型（截去 `;` 参数段并去空白；与 ingest.DetectFormat 的 mime 归一同口径）
func baseMIME(m string) string {
	if i := strings.Index(m, ";"); i >= 0 {
		m = m[:i]
	}
	return strings.TrimSpace(m)
}
