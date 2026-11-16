// plugin: kb v1.0.0
// Package tag 知识库标签候选作业（kb:tag · 打标方式的模型候选档）。
//
// 职责边界（三条，越其一即为越权）：
//   - **只产候选，不落标**：模型输出只是建议（tag + confidence）；本包**从不写** kb_tag_link、
//     **从不写** kb_tag（词表）。「未确认候选不入词表与过滤面」（词表污染防护 / R13）由结构保证：
//     候选只有经人工确认后走绑定服务（service/kb/tag_link.go）才成为 link。
//   - 一切模型步骤走 aiport 端口唯一入口（aiport.Chatter；框架默认实现 = openai-compatible 直连，
//     产品适配器接其受治理模型运行时——档名经 CallMeta.Agent 透传，如 kb-tagger）；
//     Source="kb" 批处理档与成本记账由产品适配器映射（本包不自建第二套计费）。
//   - **缺省关**（配置键 kb.tag.model_enabled，代码缺省 false）：关档时作业收工回 state=disabled
//     （不调模型、不产候选）；切换默认开关须先完成单变量评估。
//
// 键口径：kb:tag:<document_id>（单文档候选；params.document_id 必填）。注册随包 init 进入 serve 二进制
// 导入图（先例 search 包注册 kb:prune、kb 包注册 kb:ingest/rebuild/purge）——本包经 service/kb（本插件的服务层）
// 的绑定服务引用，故常驻导入图，无需额外接线。
//
// 未实施（登记，避免误读）：规则打标（kb_tag_rule）的批次内求值、候选的持久化暂存与确认流、
// C8 预算预检在 kb:tag 档的接线——均不在本批交付面（候选回执经作业 result 落库）。
package tag

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"

	"github.com/ai-go-hub/ai-go-admin/internal/infra/database"
	"github.com/ai-go-hub/ai-go-admin/plugins/kb/model"
	"github.com/ai-go-hub/ai-go-admin/plugins/kb/service/jobs"
	"github.com/ai-go-hub/ai-go-admin/plugins/kb/service/kb/aiport"

	"github.com/ai-go-hub/ai-go-admin/plugins/kb/kbcfg"
	"gorm.io/gorm"
)

// JobKindTag 标签候选作业 kind（键 kb:tag:<document_id>）
const JobKindTag = "kb:tag"

// 缺省参数（配置键经 config.Viper 读，代码缺省见下；config/kb.yaml 的 tag 段为运行期覆盖源）
const (
	// DefaultAgent 候选生成调用档名（产品适配器映射到其 ai agent，如 kb-tagger；框架默认实现忽略）
	DefaultAgent = "kb-tagger"
	// Source 调用来源（批处理档）
	Source = "kb"
	// DefaultMaterialChars 材料字符帽（rune；超出截断——候选只需材料提示，不整篇喂）
	DefaultMaterialChars = 6000
	// DefaultMaxCandidates 候选条数帽（模型输出再多也截断）
	DefaultMaxCandidates = 5
	// DefaultVocabularyCap 词表装载上限（平台组 ∪ 本库组的条数；超出按 weigh 序截断）
	DefaultVocabularyCap = 500
	// maxMaterialChunks 材料取样切片数帽（按 seq 升序取前 N 片）
	maxMaterialChunks = 20
	// maxTagRunes 候选标签名长度帽（rune；prompt 契约 2–12 是提示面，此处只做防御性截断判定）
	maxTagRunes = 24
)

// Params kb:tag 入参（document_id 必填）
type Params struct {
	DocumentID uint   `json:"document_id"`
	Reason     string `json:"reason,omitempty"` // 投递原因（仅日志）
}

// Candidate 单条候选（模型输出结构：标签 + 置信度）
type Candidate struct {
	Tag        string  `json:"tag"`
	Confidence float64 `json:"confidence"`
}

// MatchedCandidate 候选 + 词表命中读数（in_vocabulary=false = 未入词表：只回显、绝不落库）
type MatchedCandidate struct {
	Tag          string  `json:"tag"`
	Confidence   float64 `json:"confidence"`
	InVocabulary bool    `json:"in_vocabulary"`
	TagID        uint    `json:"tag_id,omitempty"`     // 命中词表项的 id（人工确认后可直接走绑定接口）
	GroupID      uint    `json:"group_id,omitempty"`   // 命中词表项所属组
	GroupName    string  `json:"group_name,omitempty"` // 组名（回执可读性）
}

// TagRef 词表项（标签 + 其组的作用域；库可见标签 = 平台组 ∪ 本库组——T2）
type TagRef struct {
	TagID     uint   // kb_tag.id
	Name      string // 标签名（原样，比较时归一）
	Status    uint8  // kb_tag.status（0=禁用，1=启用；禁用不进候选比对面）
	GroupID   uint   // kb_tag_group.id
	GroupName string // 组名
	Scope     string // platform|library
	LibraryID uint   // 库组=kb_library.id；平台组=0
}

// Result 候选作业回执（落作业 result JSON）
type Result struct {
	DocumentID uint               `json:"document_id"`
	LibraryID  uint               `json:"library_id"`
	State      string             `json:"state"` // disabled|skipped|suspended|candidates
	Agent      string             `json:"agent,omitempty"`
	Model      string             `json:"model,omitempty"`
	BuildHash  string             `json:"build_hash,omitempty"`
	Chars      int                `json:"chars"`      // 材料字符数（rune；成本读数）
	Vocabulary int                `json:"vocabulary"` // 词表条目数
	Candidates []MatchedCandidate `json:"candidates"`
	Notes      []string           `json:"notes,omitempty"`
}

// GenerateFunc 候选生成函数（缺省 = 经 aiport 端口调用候选档；用例注入假候选源）
type GenerateFunc func(ctx context.Context, meta aiport.CallMeta, material string) ([]Candidate, string, error)

// Service 候选作业服务（进程内可按需多实例；无内部可变状态）
type Service struct {
	db       *gorm.DB
	generate GenerateFunc  // 候选生成（可注入）
	enabled  func() bool   // 模型候选开关（可注入；缺省读 kb.tag.model_enabled）
	agent    func() string // agent 名（可注入；缺省读 kb.tag.agent，回落 kb-tagger）
}

// NewService 创建候选作业服务（全局 db + 缺省生成器/开关）
func NewService() *Service {
	return &Service{
		db:       database.DB(),
		generate: runTagger,
		enabled:  modelCandidatesEnabled,
		agent:    taggerAgent,
	}
}

// NewServiceWith 注入构造（用例：假生成器 / 强制开关；生产一律走 NewService）
func NewServiceWith(db *gorm.DB, generate GenerateFunc, enabled func() bool) *Service {
	s := NewService()
	if db != nil {
		s.db = db
	}
	if generate != nil {
		s.generate = generate
	}
	if enabled != nil {
		s.enabled = enabled
	}
	return s
}

// init 注册 kb:tag 处理器（先例：kb 包注册 kb:ingest/rebuild/purge；search 包注册 kb:prune）。
func init() {
	jobs.Register(JobKindTag, handler{})
}

// handler kb:tag 处理器（单文档候选）
type handler struct{}

// Run 执行一次候选生成（键校验 → 单文档候选；结果 JSON 落作业回执）
func (handler) Run(ctx context.Context, j *jobs.Job) error {
	key := strings.TrimSpace(j.Key)
	if !strings.HasPrefix(key, JobKindTag+":") {
		return fmt.Errorf("kb:tag 键未登记前缀: %q", key)
	}
	var p Params
	if len(j.Params) > 0 {
		if err := json.Unmarshal(j.Params, &p); err != nil {
			return fmt.Errorf("kb:tag 参数解析失败: %w", err)
		}
	}
	if p.DocumentID == 0 {
		// 键内 document_id 亦可解析（params 缺失时的兜底；两者冲突以 params 为准——不为键猜语义）
		if id, err := strconv.ParseUint(strings.TrimPrefix(key, JobKindTag+":"), 10, 64); err == nil {
			p.DocumentID = uint(id)
		}
	}
	if p.DocumentID == 0 {
		return fmt.Errorf("kb:tag 缺 document_id（键 %q 无法解析）", key)
	}
	res, err := NewService().RunCandidates(ctx, p.DocumentID)
	if err != nil {
		return err
	}
	if res != nil {
		if b, err := json.Marshal(res); err == nil {
			j.Result(b)
		}
		log.Printf("[kb] kb:tag 完成（doc=%d state=%s 候选=%d 词表=%d）",
			res.DocumentID, res.State, len(res.Candidates), res.Vocabulary)
	}
	return nil
}

// kbLibraryScope 库维 scope（`kb:library:<id>`；0 = 不设 scope）——调用元数据用
// （产品适配器据此注入出站声明；框架默认实现忽略）。
func kbLibraryScope(libraryID uint) string {
	if libraryID == 0 {
		return ""
	}
	return fmt.Sprintf("kb:library:%d", libraryID)
}

// RunCandidates 单文档候选主链：开关 → 文档就绪 → 材料装配 → 模型候选 → 词表比对 → 回执。
//
// 语义（与 ingests/rebuild 同款纪律）：
//   - 开关关（缺省）⇒ state=disabled 收工（不调模型、不产候选；不是失败——属策略选择，先例
//     kb:rebuild(reembed) 在嵌入档关时空转收工）；
//   - 文档不存在/已删 ⇒ state=skipped；文档未就绪（build_state≠ready 或 active_build 空）⇒ 响亮报错
//     （候选出自就绪内容；对未产出文档投递候选 = 调用方错误）；
//   - **绝不写** kb_tag_link / kb_tag（词表污染防护：候选经人工确认后才由绑定服务落 link）。
func (s *Service) RunCandidates(ctx context.Context, documentID uint) (*Result, error) {
	if documentID == 0 {
		return nil, fmt.Errorf("kb:tag 缺 document_id")
	}
	if !s.enabled() {
		return &Result{DocumentID: documentID, State: "disabled",
			Candidates: []MatchedCandidate{},
			Notes:      []string{"模型候选缺省关（kb.tag.model_enabled=false）：未调用模型、未产候选"}}, nil
	}

	doc, err := s.loadDocument(ctx, documentID)
	if err != nil {
		return nil, err
	}
	if doc == nil {
		return &Result{DocumentID: documentID, State: "skipped", Candidates: []MatchedCandidate{},
			Notes: []string{"文档不存在或已删：候选交 kb:tag 无对象"}}, nil
	}
	build := strings.TrimSpace(doc.ActiveBuild)
	if doc.BuildState != model.KBDocumentBuildReady || build == "" {
		return nil, fmt.Errorf("kb:tag 文档 %d 未就绪（build_state=%s active_build=%q）：候选出自就绪内容",
			documentID, doc.BuildState, build)
	}
	// 调用元数据：kb.tag、write 严档由产品适配器注入（scope=kb:library:<doc.LibraryID>；0 不设）。
	// 框架默认实现忽略 Face/Scope，只读 Kind/Source 观测。

	material, chars, err := s.assembleMaterial(ctx, doc, build)
	if err != nil {
		return nil, err
	}
	res := &Result{DocumentID: doc.ID, LibraryID: doc.LibraryID, State: "candidates",
		Agent: s.agent(), BuildHash: build, Chars: chars, Candidates: []MatchedCandidate{}}
	if strings.TrimSpace(material) == "" {
		res.State = "skipped"
		res.Notes = append(res.Notes, "材料为空（标题与首窗切片正文皆空）：不调用模型")
		return res, nil
	}

	cands, modelName, err := s.generate(ctx, s.candidateMeta(doc), material)
	if err != nil {
		return nil, err // 响亮失败：worker 退避重试；重跑不写任何行（幂等无副作用）
	}
	res.Model = modelName

	refs, err := LoadVisibleTags(ctx, s.db, doc.LibraryID, vocabularyCap())
	if err != nil {
		return nil, err
	}
	res.Vocabulary = len(refs)
	res.Candidates = MatchCandidates(cands, refs)
	if unmatched := countUnmatched(res.Candidates); unmatched > 0 {
		res.Notes = append(res.Notes, fmt.Sprintf(
			"%d 条候选未入词表（只回显，不入词表与过滤面；如需启用请先经管理面建标签再人工确认绑定）", unmatched))
	}
	return res, nil
}

// loadDocument 读存活文档（软删 = 未找到；不存在回 (nil, nil)）
func (s *Service) loadDocument(ctx context.Context, documentID uint) (*model.KBDocument, error) {
	row, err := gorm.G[model.KBDocument](s.db).
		Select("id, library_id, title, build_state, active_build").
		Where("id = ?", documentID).
		First(ctx)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("kb:tag 读文档失败: %w", err)
	}
	return &row, nil
}

// assembleMaterial 材料装配：标题 + 当前 build 首窗切片正文（verbatim；按 seq 升序取样，
// 整体按 DefaultMaterialChars 截断）。返回材料、材料字符数（rune）与错误。
func (s *Service) assembleMaterial(ctx context.Context, doc *model.KBDocument, buildHash string) (string, int, error) {
	rows, err := gorm.G[model.KBChunk](s.db).
		Select("id, seq, text").
		Where("document_id = ? AND build_hash = ?", doc.ID, buildHash).
		Order("seq ASC").Limit(maxMaterialChunks).Find(ctx)
	if err != nil {
		return "", 0, fmt.Errorf("kb:tag 读切片失败: %w", err)
	}
	var b strings.Builder
	b.WriteString("标题：")
	b.WriteString(strings.TrimSpace(doc.Title))
	b.WriteString("\n")
	for i := range rows {
		text := strings.TrimSpace(rows[i].Text)
		if text == "" {
			continue
		}
		b.WriteString(text)
		b.WriteString("\n")
	}
	material := truncateRunes(b.String(), materialCap())
	return material, countRunes(material), nil
}

// candidateMeta 候选调用元数据（Face=kb.tag / scope=kb:library:<id> / 档名=s.agent()；
// 产品适配器据此注入出站声明与 agent 绑定，框架默认实现只用 Kind/Source 观测）。
func (s *Service) candidateMeta(doc *model.KBDocument) aiport.CallMeta {
	return aiport.CallMeta{
		Face: "kb.tag", Scope: kbLibraryScope(doc.LibraryID),
		Kind: "document", Agent: s.agent(), Source: Source,
	}
}

// runTagger 缺省候选生成：经 aiport 端口调用候选档（非流式；输出契约 = JSON 数组）。
// 返回候选与模型读数（产品侧记账口径 = 其调用日志）。
func runTagger(ctx context.Context, meta aiport.CallMeta, material string) ([]Candidate, string, error) {
	provider, err := aiport.CurrentChatter()
	if err != nil {
		return nil, "", err
	}
	res, err := provider.Chat(ctx, meta, material, map[string]any{"kind": "document"})
	if err != nil {
		return nil, "", fmt.Errorf("kb:tag 候选生成调用失败（档 %s）: %w", meta.Agent, err)
	}
	cands, err := ParseCandidates(res.Content, maxCandidates())
	if err != nil {
		return nil, res.Model, fmt.Errorf("kb:tag 候选输出不合契约（档 %s）: %w", meta.Agent, err)
	}
	return cands, res.Model, nil
}

// ParseCandidates 解析候选输出（纯净函数；契约 = JSON 数组 [{"tag":..,"confidence":..}]）。
//
// 容错边界（如实）：允许输出前后有多余文字或 ``` 围栏——取首个 '[' 与末个 ']' 之间切片再解析；
// 单个元素不合法（空名/名过长/类型不符）只丢该元素；**无可解析的数组**才是错误（响亮失败，
// 不把「模型没按契约输出」当空结果吞掉）。去重按归一化名（保留最高 confidence），
// 按 confidence 降序 → 名升序（确定性），截断 max 条（max ≤ 0 回落 DefaultMaxCandidates）。
func ParseCandidates(content string, max int) ([]Candidate, error) {
	start := strings.Index(content, "[")
	end := strings.LastIndex(content, "]")
	if start < 0 || end <= start {
		return nil, fmt.Errorf("输出未含 JSON 数组（前 120 字：%s）", truncateRunes(strings.TrimSpace(content), 120))
	}
	var raw []struct {
		Tag        string  `json:"tag"`
		Confidence float64 `json:"confidence"`
	}
	if err := json.Unmarshal([]byte(content[start:end+1]), &raw); err != nil {
		return nil, fmt.Errorf("输出 JSON 数组解析失败: %w", err)
	}
	if max <= 0 {
		max = DefaultMaxCandidates
	}
	best := map[string]Candidate{}
	order := make([]string, 0, len(raw))
	for _, r := range raw {
		name := strings.TrimSpace(r.Tag)
		if name == "" || countRunes(name) > maxTagRunes {
			continue // 单元素不合法：丢弃（不因一条坏元素失败整次）
		}
		conf := r.Confidence
		if conf < 0 {
			conf = 0
		}
		if conf > 1 {
			conf = 1
		}
		key := strings.ToLower(name)
		if prev, ok := best[key]; ok {
			if conf > prev.Confidence {
				best[key] = Candidate{Tag: name, Confidence: conf}
			}
			continue
		}
		best[key] = Candidate{Tag: name, Confidence: conf}
		order = append(order, key)
	}
	out := make([]Candidate, 0, len(order))
	for _, key := range order {
		out = append(out, best[key])
	}
	sortCandidates(out)
	if len(out) > max {
		out = out[:max]
	}
	return out, nil
}

// MatchCandidates 候选 ↔ 词表比对（纯净函数）：按归一化名精确命中（不模糊匹配——词表污染防护的反面：
// 也不做「近义猜测」，命中不了就只回显）。词表项按输入顺序取首个命中（LoadVisibleTags 已按 weigh 排序）。
func MatchCandidates(cands []Candidate, vocabulary []TagRef) []MatchedCandidate {
	byName := make(map[string]TagRef, len(vocabulary))
	for _, ref := range vocabulary {
		key := normalizeName(ref.Name)
		if key == "" {
			continue
		}
		if _, ok := byName[key]; !ok {
			byName[key] = ref
		}
	}
	out := make([]MatchedCandidate, 0, len(cands))
	for _, c := range cands {
		m := MatchedCandidate{Tag: c.Tag, Confidence: c.Confidence}
		if ref, ok := byName[normalizeName(c.Tag)]; ok {
			m.InVocabulary = true
			m.TagID, m.GroupID, m.GroupName = ref.TagID, ref.GroupID, ref.GroupName
		}
		out = append(out, m)
	}
	return out
}

// ==================== 词表读取（只读；与绑定服务的引用校验共用） ====================

// LoadVisibleTags 库可见标签（T2：平台组 ∪ 本库组；仅 status=1 且未删）。
// 排序 (组 weigh DESC, 标签 weigh DESC, id ASC) 确定性；条数 ≤ limit（≤1 回落 DefaultVocabularyCap）。
func LoadVisibleTags(ctx context.Context, db *gorm.DB, libraryID uint, limit int) ([]TagRef, error) {
	if db == nil {
		return nil, fmt.Errorf("kb: 词表读取缺 db")
	}
	if limit <= 0 {
		limit = DefaultVocabularyCap
	}
	sqlText := "SELECT t.id AS tag_id, t.name AS name, t.status AS status, g.id AS group_id, g.name AS group_name," +
		" g.scope AS scope, g.library_id AS library_id FROM " + tableName(db, model.KBTag{}) + " t" +
		" JOIN " + tableName(db, model.KBTagGroup{}) + " g ON g.id = t.group_id" +
		" WHERE t.deleted_at IS NULL AND g.deleted_at IS NULL AND t.status = 1" +
		" AND (g.scope = ? OR (g.scope = ? AND g.library_id = ?))" +
		" ORDER BY g.weigh DESC, t.weigh DESC, t.id ASC LIMIT ?"
	rows := make([]vocabRow, 0, 32)
	if err := db.WithContext(ctx).Raw(sqlText,
		model.KBTagScopePlatform, model.KBTagScopeLibrary, libraryID, limit).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("kb: 读取库可见标签失败: %w", err)
	}
	out := make([]TagRef, 0, len(rows))
	for i := range rows {
		out = append(out, rows[i].ref())
	}
	return out, nil
}

// LoadTagRefsByIDs 按 id 取词表项（不存在/软删不返回；含禁用项——调用方按 Status 裁决）。
// 返回 map[tag_id]TagRef（引用校验面：绑定服务用它拒「不存在/禁用/跨库」三类引用）。
func LoadTagRefsByIDs(ctx context.Context, db *gorm.DB, ids []uint) (map[uint]TagRef, error) {
	out := map[uint]TagRef{}
	if db == nil {
		return out, fmt.Errorf("kb: 词表读取缺 db")
	}
	if len(ids) == 0 {
		return out, nil
	}
	sqlText := "SELECT t.id AS tag_id, t.name AS name, t.status AS status, g.id AS group_id, g.name AS group_name," +
		" g.scope AS scope, g.library_id AS library_id FROM " + tableName(db, model.KBTag{}) + " t" +
		" JOIN " + tableName(db, model.KBTagGroup{}) + " g ON g.id = t.group_id" +
		" WHERE t.deleted_at IS NULL AND g.deleted_at IS NULL AND t.id IN ?"
	rows := make([]vocabRow, 0, len(ids))
	if err := db.WithContext(ctx).Raw(sqlText, int64Slice(ids)).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("kb: 读取标签词表失败: %w", err)
	}
	for i := range rows {
		out[rows[i].TagID] = rows[i].ref()
	}
	return out, nil
}

// vocabRow 词表行（raw 扫描载体；列面即两条读取语句的投影）
type vocabRow struct {
	TagID     uint   `gorm:"column:tag_id"`
	Name      string `gorm:"column:name"`
	Status    uint8  `gorm:"column:status"`
	GroupID   uint   `gorm:"column:group_id"`
	GroupName string `gorm:"column:group_name"`
	Scope     string `gorm:"column:scope"`
	LibraryID uint   `gorm:"column:library_id"`
}

func (r vocabRow) ref() TagRef {
	return TagRef{TagID: r.TagID, Name: r.Name, Status: r.Status,
		GroupID: r.GroupID, GroupName: r.GroupName, Scope: r.Scope, LibraryID: r.LibraryID}
}

// ==================== 配置读取（kb.tag.* 单点） ====================

// modelCandidatesEnabled 模型候选开关（kb.tag.model_enabled；**缺省 false = 关**——无密钥部署不产候选）。
func modelCandidatesEnabled() bool {
	return kbcfg.Get().Tag.ModelEnabled
}

// taggerAgent 候选 agent 名（kb.tag.agent；空回落 kb-tagger）
func taggerAgent() string {
	if name := strings.TrimSpace(kbcfg.Get().Tag.Agent); name != "" {
		return name
	}
	return DefaultAgent
}

// materialCap 材料字符帽（kb.tag.material_chars；≤0 回落 DefaultMaterialChars）
func materialCap() int {
	if n := kbcfg.Get().Tag.MaterialChars; n > 0 {
		return n
	}
	return DefaultMaterialChars
}

// maxCandidates 候选条数帽（kb.tag.max_candidates；≤0 回落 DefaultMaxCandidates）
func maxCandidates() int {
	if n := kbcfg.Get().Tag.MaxCandidates; n > 0 {
		return n
	}
	return DefaultMaxCandidates
}

// vocabularyCap 词表装载上限（kb.tag.vocabulary_cap；≤0 回落 DefaultVocabularyCap）
func vocabularyCap() int {
	if n := kbcfg.Get().Tag.VocabularyCap; n > 0 {
		return n
	}
	return DefaultVocabularyCap
}

// ==================== 小工具 ====================

// tableName 表名单点（与模型 TableName() 同源；raw SQL 禁裸写表名——照既有口径）
func tableName(db *gorm.DB, m any) string {
	stmt := &gorm.Statement{DB: db}
	if err := stmt.Parse(m); err != nil || stmt.Schema == nil || stmt.Schema.Table == "" {
		if t, ok := m.(interface{ TableName() string }); ok {
			return t.TableName()
		}
		return ""
	}
	return stmt.Schema.Table
}

// countUnmatched 未入词表的候选数（读数）
func countUnmatched(rows []MatchedCandidate) int {
	n := 0
	for i := range rows {
		if !rows[i].InVocabulary {
			n++
		}
	}
	return n
}

// sortCandidates 确定性排序（confidence DESC → 名 ASC；亚稳定输入已去重，不依赖 sort.SliceStable）
func sortCandidates(rows []Candidate) {
	for i := 1; i < len(rows); i++ {
		for j := i; j > 0; j-- {
			if rows[j].Confidence > rows[j-1].Confidence ||
				(rows[j].Confidence == rows[j-1].Confidence && rows[j].Tag < rows[j-1].Tag) {
				rows[j], rows[j-1] = rows[j-1], rows[j]
				continue
			}
			break
		}
	}
}

// normalizeName 名称归一（比较键：首尾空白 + 大小写不敏感；CJK 不变形）
func normalizeName(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// countRunes rune 计数（本地小工具）
func countRunes(s string) int {
	n := 0
	for range s {
		n++
	}
	return n
}

// truncateRunes 按 rune 截断（n ≤ 0 = 原样返回）
func truncateRunes(s string, n int) string {
	if n <= 0 {
		return s
	}
	rs := []rune(s)
	if len(rs) <= n {
		return s
	}
	return string(rs[:n])
}

// int64Slice uint 集 → int64 参数（kb_* 主键为 bigint；uint 直传有驱动面风险，统一转换单点）
func int64Slice(ids []uint) []int64 {
	out := make([]int64, len(ids))
	for i, v := range ids {
		out[i] = int64(v)
	}
	return out
}
