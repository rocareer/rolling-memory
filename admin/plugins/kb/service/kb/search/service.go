// plugin: kb v1.0.0
// Package search 知识库检索与消费面。
//
// 面与纪律：
//   - 入口先 `svcKB.BuildReadView`（admin/user 两轴）再进业务；一切拒绝 errors.Is(err, svcKB.ErrDenied)
//     （「不存在」与「无权」同型）；
//   - **权限先解析可读库集再召回**（T5 硬条款）：可读集经 package kb 的单点构造器解析
//     （ReadableLibraryIDs = owner 集 ∪ grantLibraryActions(read)），拼 `library_id = ANY(:readable)` 作
//     每条腿**第一条件**下推（仓储 repository/kb/search_legs.go）；空集直接返回空；禁后过滤；
//   - 标签过滤（T5）与权限**同层**下推：Options.TagIDs 非空 ⇒ 每条腿 WHERE 内拼入
//     「切片级 link 命中 c.id OR 文档级 link 命中 c.document_id」两段子查询（同一条件；禁后过滤）；
//   - 模式 auto|lexical|vector|hybrid（缺省 auto=hybrid，无向量内容自动降级词面）；RRF(k=60) 融合；
//     单腿失败只降该腿并回显 degraded[]，全腿失败才整查失败；
//   - top_k 缺省 8、硬上限 50（超出截断并回显生效值）；腿窗（浏览面）≤100（NormalizeBrowseLimit）；
//   - 检索日志 kb_search_log 请求内 best-effort 同步落行（失败告警不失败检索；T9）；
//   - kb:prune（保留期清理）入队源双源：作业面 worker 的每日补键 + 每日首请求懒触发
//     （ensureDailyPrune）；键 kb:prune:<date> 天然每日可再入队（幂等键单飞）。
package search

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/ai-go-hub/ai-go-admin/internal/infra/database"
	repoKB "github.com/ai-go-hub/ai-go-admin/plugins/kb/repository/kb"
	"github.com/ai-go-hub/ai-go-admin/plugins/kb/service/jobs"
	svcKB "github.com/ai-go-hub/ai-go-admin/plugins/kb/service/kb"
	"github.com/ai-go-hub/ai-go-admin/plugins/kb/service/kb/chunk"

	"github.com/ai-go-hub/ai-go-admin/plugins/kb/kbcfg"
	"gorm.io/gorm"
)

// 检索面常量（分页上限：检索面 rank 帽 50；浏览面帽 = svcKB.MaxBrowseLimit=100）
const (
	// DefaultTopK top_k 缺省
	DefaultTopK = 8
	// MaxTopK top_k 硬上限（超出截断并回显）
	MaxTopK = 50
	// defaultRRFK RRF 秩融合常数 k（RRF(k=60)；viper kb.search.rrf_k 覆盖）
	defaultRRFK = 60
	// defaultTrgmThreshold trgm 腿相似度阈值缺省（先例读数 0.45）
	defaultTrgmThreshold = 0.45
	// defaultQueryTextCap 查询原文落日志的截断帽（rune；kb.search.log_query_text_max=0 关）
	defaultQueryTextCap = 200
	// maxSearchLogQueryTextCap 截断帽上限（= kb_search_log.query_text 列宽 varchar(512)；
	// 超列宽写入整行失败、best-effort 落日志会静默丢行——已整改，rune 截断同口径）
	maxSearchLogQueryTextCap = 512
)

// 检索模式闭集（T4；缺省 auto=hybrid）
const (
	ModeAuto    = "auto"
	ModeLexical = "lexical"
	ModeVector  = "vector"
	ModeHybrid  = "hybrid"
)

// Options 检索入参（探针/桥共用）
type Options struct {
	Query     string // 检索词（必填；逐字进各腿）
	Mode      string // auto|lexical|vector|hybrid（空=auto）
	TopK      int    // 直出条数（缺省 8；硬上限 50）
	Limit     int    // 腿候选窗（浏览面；缺省 = top_k；上限 100）
	LibraryID uint   // 收窄到单库（0=全可读集；>0 先逐资源过读视图）
	// TagIDs 标签过滤（T5：非空 = 每条腿第一层条件内下推标签子查询；空 = 不过滤）。
	// 语义 = 「锚定对象挂有**任一**给定标签」——切片级 link 命中 chunk，文档级 link 命中其全部切片。
	TagIDs []uint
}

// LegStat 单腿读数（回执与 kb_search_log.legs 同源）
type LegStat struct {
	Name     string `json:"name"`
	Hits     int    `json:"hits"`
	Ms       int64  `json:"ms"`
	Degraded bool   `json:"degraded"`
	Error    string `json:"error,omitempty"`
}

// Item 检索返回项（返回定位；tags[] 在回填前恒空数组）
type Item struct {
	ChunkID    uint     `json:"chunk_id"`
	DocumentID uint     `json:"document_id"`
	LibraryID  uint     `json:"library_id"`
	Title      string   `json:"title"`
	Snippet    string   `json:"snippet"`
	Highlight  [2]int   `json:"highlight"` // snippet 内 rune 区间 [起,止)；[-1,-1] = 无高亮
	Locator    Locator  `json:"locator"`
	Score      float64  `json:"score"` // RRF 融合分（四位小数）
	Legs       []string `json:"legs"`
	Tags       []string `json:"tags"`
}

// Result 检索回执（生效档位与降级读数回显）
type Result struct {
	Items             []Item    `json:"items"`
	Mode              string    `json:"mode"`
	TopK              int       `json:"top_k"`
	Limit             int       `json:"limit"`
	Degraded          []string  `json:"degraded"`
	Legs              []LegStat `json:"legs"`
	ReadableLibraries int       `json:"readable_libraries"`
}

// ChunkDetail 切片逐字原文（原文校验入口：防幻觉核对；text_hash 供复算）
type ChunkDetail struct {
	ChunkID    uint    `json:"chunk_id"`
	DocumentID uint    `json:"document_id"`
	LibraryID  uint    `json:"library_id"`
	Title      string  `json:"title"`
	Locator    Locator `json:"locator"`
	CharStart  int     `json:"char_start"`
	CharEnd    int     `json:"char_end"`
	Text       string  `json:"text"`
	TextHash   string  `json:"text_hash"`
}

// Service 检索服务（进程内可按需多实例；无内部可变状态——仅查询缓存持锁）
type Service struct {
	db    *gorm.DB
	core  *svcKB.Service
	store *repoKB.Store
	qvec  *queryVecCache

	embedQuery func(ctx context.Context, query string) ([]float32, error) // 查询嵌入（可注入；缺省 queryVector）
	now        func() time.Time
}

// NewService 创建检索服务（全局 db + 默认仓储；core 为 package kb 的单点服务）
func NewService(core *svcKB.Service) *Service {
	return NewServiceWith(database.DB(), core, repoKB.NewStore(), newQueryVecCache())
}

// NewServiceWith 注入构造（用例：捕获型会话 / 假嵌入 / 内存缓存）
func NewServiceWith(db *gorm.DB, core *svcKB.Service, store *repoKB.Store, cache *queryVecCache) *Service {
	s := &Service{db: db, core: core, store: store, qvec: cache, now: time.Now}
	s.embedQuery = s.queryVector
	return s
}

// NormalizeTopK top_k 规整：缺省 8；硬上限 50（超出截断——调用方负责回显生效值）
func NormalizeTopK(topK int) int {
	if topK <= 0 {
		return DefaultTopK
	}
	if topK > MaxTopK {
		return MaxTopK
	}
	return topK
}

// normalizeMode 模式归一并闭集校验（空=auto）
func normalizeMode(mode string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", ModeAuto:
		return ModeAuto, nil
	case ModeLexical:
		return ModeLexical, nil
	case ModeVector:
		return ModeVector, nil
	case ModeHybrid:
		return ModeHybrid, nil
	}
	return "", fmt.Errorf("检索模式未登记: %s（闭集 auto|lexical|vector|hybrid）", mode)
}

// Search 检索主入口（探针/桥共用；隔离与拒绝语义见包注释）。
//
// 返回错误两类：入参/整查失败（error）与 ErrDenied 族（无权——HTTP 层同型 404）。
func (s *Service) Search(ctx context.Context, view *svcKB.ReadView, opts Options) (*Result, error) {
	start := s.now()
	query := strings.TrimSpace(opts.Query)
	if query == "" {
		return nil, errors.New("检索词必填（query）")
	}
	mode, err := normalizeMode(opts.Mode)
	if err != nil {
		return nil, err
	}
	topK := NormalizeTopK(opts.TopK)
	limit := opts.Limit
	if limit <= 0 {
		limit = topK // 缺省腿窗 = top_k（浏览面上限 ≤100 由 NormalizeBrowseLimit 兜底）
	}
	limit = svcKB.NormalizeBrowseLimit(limit)

	//T5：权限先解析可读库集（空集直接返回空）。单库收窄（library_id>0）亦走**同一可读集单点**做
	// membership 判定，不用 RequireRead：user 轴的读集是 owner∪grant 反查集（非单资源 EXISTS），且检索腿
	// 第一条件就是「可读集」本身——集合单点一条口径喂全部腿，scoped 收窄与全量检索逐元素同源。
	// （实测抓到：已软删库 scope 直取可召回。旧缺口已一并补齐：admin 轴
	// RequireRead 由「O(1) 恒真」补为「库未删」单查（acl.go·libraryAccessible 统一谓词，F8），
	// 此处仍保持集合单点。）membership 口径与可读集完全一致（user=owner∪grant∧status=1∧未删；
	// admin=未删），不可读一律同型 ErrDenied。
	ids, err := s.core.ReadableLibraryIDs(ctx, view)
	if err != nil {
		return nil, err
	}
	readable := ids
	if opts.LibraryID > 0 {
		found := false
		for _, id := range ids {
			if id == opts.LibraryID {
				found = true
				break
			}
		}
		if !found {
			return nil, svcKB.ErrDenied // 不存在/无权同型（含软删库与禁用库——按可读集单点口径）
		}
		readable = []uint{opts.LibraryID}
	}

	res := &Result{Mode: mode, TopK: topK, Limit: limit, Degraded: []string{}, Legs: []LegStat{}, ReadableLibraries: len(readable)}
	reqID := newRequestID()

	if len(readable) == 0 {
		// 可读集为空：不召回任何腿（不是失败——按空集回执；绝不发兜底查询）
		s.writeSearchLog(view, query, res, time.Since(start), reqID)
		s.ensureDailyPrune(ctx)
		return res, nil
	}

	legs, planNotes, err := s.planLegsWithNotes(ctx, readable, mode, query)
	if err != nil {
		return nil, err
	}
	// 标签过滤下推（T5）：非空集随可读集进每条腿的 WHERE（先于 LIMIT/`<=>` 截断），
	// 禁应用层后过滤；空/未给 = 不过滤（nil 由仓储面短路，不发兜底子查询）。
	var tags *repoKB.TagFilter
	if len(opts.TagIDs) > 0 {
		tags = &repoKB.TagFilter{TagIDs: opts.TagIDs}
	}
	outcomes := s.runLegs(ctx, readable, query, limit, legs, tags)

	degraded := make([]string, 0, len(outcomes)+len(planNotes))
	failed := 0
	for _, o := range outcomes {
		st := LegStat{Name: o.name, Hits: len(o.hits), Ms: o.ms}
		if o.err != nil {
			st.Degraded = true
			st.Error = o.err.Error()
			degraded = append(degraded, o.name+": "+o.err.Error())
			failed++
			log.Printf("[kb] 检索腿降级（%s）: %v", o.name, o.err)
		}
		res.Legs = append(res.Legs, st)
	}
	// 规划期降级注明（auto 档：异身份行在、当前身份无行——回显见 planLegsWithNotes；不改结果语义）
	degraded = append(degraded, planNotes...)
	res.Degraded = degraded
	if len(outcomes) > 0 && failed == len(outcomes) {
		// 全腿失败才整查失败（先例：逐腿 try、全败才报）；失败读数已随腿集落痕
		s.writeSearchLog(view, query, res, time.Since(start), reqID)
		s.ensureDailyPrune(ctx)
		return nil, fmt.Errorf("全部检索腿失败（%d 条）: %s", failed, strings.Join(degraded, "; "))
	}

	fused := rrfFuse(outcomes, rrfK(), topK)
	// 命中行标签回填（item.tags[]）：按命中 chunk_id 集一次反查（chunk 级 ∪ 文档级 link）；
	// 回填失败**不整查失败**（标签是附加读数——降级为空数组 + 日志，与「单腿失败只降该腿」同哲学）。
	tagNames := map[uint][]string{}
	if len(fused) > 0 {
		ids := make([]uint, 0, len(fused))
		for i := range fused {
			ids = append(ids, fused[i].hit.ChunkID)
		}
		names, terr := s.store.ChunkTagNames(ctx, ids)
		if terr != nil {
			log.Printf("[kb] 命中行标签回填失败（tags[] 按空数组回执）: %v", terr)
		} else {
			tagNames = names
		}
	}
	res.Items = make([]Item, 0, len(fused))
	for _, f := range fused {
		snippet, hl := buildSnippet(f.hit.Text, query, defaultSnippetCap)
		tags := tagNames[f.hit.ChunkID]
		if tags == nil {
			tags = []string{} // 无标签 = 空数组（JSON 面 []，非 null）
		}
		res.Items = append(res.Items, Item{
			ChunkID:    f.hit.ChunkID,
			DocumentID: f.hit.DocumentID,
			LibraryID:  f.hit.LibraryID,
			Title:      f.hit.Title,
			Snippet:    snippet,
			Highlight:  hl,
			Locator:    buildLocator(f.hit),
			Score:      f.score,
			Legs:       f.legs,
			Tags:       tags,
		})
	}
	s.writeSearchLog(view, query, res, time.Since(start), reqID)
	s.ensureDailyPrune(ctx)
	return res, nil
}

// GetChunk 按 chunk_id 取逐字原文（原文校验入口；同一可读集条件——不可见与不存在同型 ErrDenied）。
func (s *Service) GetChunk(ctx context.Context, view *svcKB.ReadView, chunkID uint) (*ChunkDetail, error) {
	if view == nil || chunkID == 0 {
		return nil, svcKB.ErrDenied
	}
	readable, err := s.core.ReadableLibraryIDs(ctx, view)
	if err != nil {
		return nil, err
	}
	hit, err := s.store.ChunkByID(ctx, readable, chunkID)
	if err != nil {
		return nil, err
	}
	if hit == nil {
		return nil, svcKB.ErrDenied // 不存在/无权同型
	}
	return &ChunkDetail{
		ChunkID:    hit.ChunkID,
		DocumentID: hit.DocumentID,
		LibraryID:  hit.LibraryID,
		Title:      hit.Title,
		Locator:    buildLocator(*hit),
		CharStart:  hit.CharStart,
		CharEnd:    hit.CharEnd,
		Text:       hit.Text,
		TextHash:   hit.TextHash,
	}, nil
}

// ==================== 检索日志（T9：best-effort 同步落 kb_search_log） ====================

// searchLogRow kb_search_log 行类型（migrations/3_search_log.up.sql 列面对齐；表名不带前缀，经 GORM NamingStrategy 注入）
type searchLogRow struct {
	ID          uint64     `gorm:"column:id;primarykey"`
	UserID      uint       `gorm:"column:user_id"`
	Mode        string     `gorm:"column:mode"`
	Legs        string     `gorm:"column:legs"`
	QueryHash   string     `gorm:"column:query_hash"`
	QueryText   string     `gorm:"column:query_text"`
	ResultCount int        `gorm:"column:result_count"`
	LatencyMs   int        `gorm:"column:latency_ms"`
	Degraded    string     `gorm:"column:degraded"`
	RequestID   string     `gorm:"column:request_id"`
	CreatedAt   *time.Time `gorm:"column:created_at"`
}

// TableName 表名单点（前缀由 NamingStrategy 注入）
func (searchLogRow) TableName() string { return "kb_search_log" }

// writeSearchLog 落一行检索日志（best-effort：脱钩短 ctx + 失败只告警，绝不失败检索本身；
// 先例（同仓既有实现）：脱钩 5s ctx + 仅告警）。
//
// user_id 口径（迁移注释）：user 轴填 users.id；admin/系统面 = 0（两 id 空间禁混用，故不回填 admins.id）。
func (s *Service) writeSearchLog(view *svcKB.ReadView, query string, res *Result, d time.Duration, reqID string) {
	userID := uint(0)
	if view != nil && !view.IsAdmin() {
		userID = view.Actor().ID
	}
	legsJSON, err := json.Marshal(res.Legs)
	if err != nil {
		legsJSON = []byte("[]")
	}
	degradedJSON, err := json.Marshal(res.Degraded)
	if err != nil {
		degradedJSON = []byte("[]")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sqlText := "INSERT INTO " + tableName(s.db, searchLogRow{}) +
		" (user_id, mode, legs, query_hash, query_text, result_count, latency_ms, degraded, request_id, created_at)" +
		" VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, NOW())"
	if err := s.db.WithContext(ctx).Exec(sqlText,
		userID, res.Mode, string(legsJSON), chunk.TextHash(query), truncateRunes(query, searchLogQueryTextCap()),
		len(res.Items), d.Milliseconds(), string(degradedJSON), reqID).Error; err != nil {
		log.Printf("[kb] 检索日志写入失败（不影响检索结果）: %v", err)
	}
}

// newRequestID 请求关联 id（16 位十六进制；仅日志关联用，非身份）
func newRequestID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	return hex.EncodeToString(b[:])
}

// ==================== kb:prune（保留期清理；入队源 = 作业面 worker 每日补键 + 每日首请求懒触发） ====================

// JobKindPrune 检索日志保留期清理作业（键 kb:prune:<date>——天然每日可再入队）
const JobKindPrune = "kb:prune"

// defaultRetentionDays 检索日志保留期缺省（180 天；viper kb.prune.retention_days 覆盖）
const defaultRetentionDays = 180

// pruneBatch 单批删除行数 / pruneMaxBatches 单次作业批数上限（增长表清理：短语句、可重入）
const (
	pruneBatch      = 5000
	pruneMaxBatches = 20
)

// init 注册 kb:prune 处理器（"随包 init 进入导入图"；同仓先例：本插件 service/kb/job.go 的三类作业注册）。
// 注册必须先于任何入队源（未注册 kind 会被 worker 响亮落 failed——jobs/worker.go:176）。
func init() {
	jobs.Register(JobKindPrune, pruneHandler{})
}

// pruneHandler kb:prune 处理器（保留期清理；幂等可重入）
type pruneHandler struct{}

// Run 执行一次保留期清理（键校验 + 分批删；结果 JSON 落作业回执）
func (pruneHandler) Run(ctx context.Context, j *jobs.Job) error {
	if !strings.HasPrefix(strings.TrimSpace(j.Key), JobKindPrune+":") {
		return fmt.Errorf("kb:prune 键未登记前缀: %q", j.Key)
	}
	days := retentionDays()
	svc := &Service{db: database.DB(), now: time.Now}
	deleted, err := svc.PruneSearchLog(ctx, days)
	if err != nil {
		return err
	}
	if j != nil {
		if b, err := json.Marshal(map[string]any{"retention_days": days, "deleted": deleted}); err == nil {
			j.Result(b)
		}
	}
	log.Printf("[kb] kb:prune 完成（保留期 %d 天，删除 %d 行）", days, deleted)
	return nil
}

// PruneSearchLog 按保留期分批清理 kb_search_log（DB 时钟计时——NOW() - make_interval(days => ?)，
// 免受应用/库时钟差影响；单批 pruneBatch 行、至多 pruneMaxBatches 批，可重入）。
func (s *Service) PruneSearchLog(ctx context.Context, retentionDays int) (int64, error) {
	if retentionDays <= 0 {
		retentionDays = defaultRetentionDays
	}
	table := tableName(s.db, searchLogRow{})
	// 表不存在 = 本部署未应用本插件的自管迁移（建表随自管迁移走，而作业注册随插件代码走）：
	// 静默跳过，避免每日保留期清理反复失败刷日志。
	var tableReg *string
	if err := s.db.WithContext(ctx).Raw("SELECT to_regclass(?)::text", table).Scan(&tableReg).Error; err != nil {
		return 0, fmt.Errorf("kb: 检索日志清理失败: %w", err)
	}
	if tableReg == nil || *tableReg == "" {
		log.Printf("[kb] %s 不存在（未安装 KB 模块），跳过保留期清理", table)
		return 0, nil
	}
	sqlText := "DELETE FROM " + table + " WHERE id IN (SELECT id FROM " + table +
		" WHERE created_at < NOW() - make_interval(days => ?) ORDER BY id LIMIT ?)"
	var total int64
	for i := 0; i < pruneMaxBatches; i++ {
		res := s.db.WithContext(ctx).Exec(sqlText, retentionDays, pruneBatch)
		if res.Error != nil {
			return total, fmt.Errorf("kb: 检索日志清理失败: %w", res.Error)
		}
		total += res.RowsAffected
		if res.RowsAffected < pruneBatch {
			break
		}
	}
	return total, nil
}

var pruneDateMu sync.Mutex
var pruneLastDate string

// ensureDailyPrune 每日首请求懒触发（入队源②；进程内每日一次——进程重启后的补键由作业面 worker
// 的每日入队承担，见 service/jobs 的 ensurePruneQueued：同键弹掉由活跃键唯一索引收口）。
// 同步执行（一次幂等 INSERT，代价可忽略）；键幂等 ⇒ 多实例并发触发也只留一行。
func (s *Service) ensureDailyPrune(_ context.Context) {
	today := time.Now().Format("2006-01-02")
	pruneDateMu.Lock()
	if pruneLastDate == today {
		pruneDateMu.Unlock()
		return
	}
	pruneLastDate = today
	pruneDateMu.Unlock()
	enqueuePruneKey(pruneKeyFor(time.Now()))
}

// pruneKeyFor 幂等键口径 kb:prune:<YYYY-MM-DD>（每日一键；参数可裁属作业侧）
func pruneKeyFor(t time.Time) string { return JobKindPrune + ":" + t.Format("2006-01-02") }

// enqueuePruneKey 入队（best-effort：失败只告警；脱钩短 ctx）
func enqueuePruneKey(key string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	job, created, err := jobs.NewStore().Enqueue(ctx, jobs.EnqueueInput{Kind: JobKindPrune, Key: key})
	if err != nil {
		log.Printf("[kb] kb:prune 入队失败（键 %s）: %v", key, err)
		return
	}
	if created {
		log.Printf("[kb] kb:prune 已入队（键 %s，作业 %d）", key, job.ID)
	}
}

// ==================== 配置读取（kb.* 单点；config/kb.yaml 为落地位置，本批以 viper 键 + 代码缺省给出） ====================

// embedEnabled 嵌入档开关（关 = 只切不嵌；检索面向量腿位降级——与 kb:ingest 同源键）。
// 缺省档来自插件内嵌配置（缺省关）：无 AI 密钥的部署下不发嵌入调用、也不产出 degraded 腿。
func embedEnabled() bool {
	return kbcfg.Get().Embed.Enabled
}

// rrfK RRF 融合常数（kb.search.rrf_k；≤0 回落 60）
func rrfK() int {
	if k := kbcfg.Get().Search.RRFK; k > 0 {
		return k
	}
	return defaultRRFK
}

// trgmThreshold trgm 腿相似度阈值（kb.search.trgm_threshold；取值域 (0,1)，越域回落缺省）
func trgmThreshold() float64 {
	if t := kbcfg.Get().Search.TrgmThreshold; t > 0 && t < 1 {
		return t
	}
	return defaultTrgmThreshold
}

// searchLogQueryTextCap 查询原文落日志截断帽（kb.search.log_query_text_max）：
// 缺省 200（内嵌档）；显式 0/负数 = 关（不落原文，query_text 恒空）；超出列宽（512）夹取到列宽
// ——超列宽写入整行失败（best-effort 落日志静默丢行），夹取是防呆而非口径变更。
func searchLogQueryTextCap() int {
	switch n := kbcfg.Get().Search.LogQueryTextMax; {
	case n <= 0:
		return 0
	case n > maxSearchLogQueryTextCap:
		return maxSearchLogQueryTextCap
	default:
		return n
	}
}

// retentionDays 检索日志保留期天数（kb.prune.retention_days；≤0 回落 180）
func retentionDays() int {
	if d := kbcfg.Get().Prune.RetentionDays; d > 0 {
		return d
	}
	return defaultRetentionDays
}

// ==================== 小工具 ====================

// tableName 表名单点（与模型 TableName() 同源；raw SQL 禁裸写表名——照既有口径）
func tableName(db *gorm.DB, m any) string {
	stmt := &gorm.Statement{DB: db}
	if err := stmt.Parse(m); err != nil || stmt.Schema == nil || stmt.Schema.Table == "" {
		if t, ok := m.(interface{ TableName() string }); ok {
			return t.TableName()
		}
		return "kb_search_log"
	}
	return stmt.Schema.Table
}

// truncateRunes 按 rune 截断（n ≤ 0 = 空串，即关）
func truncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	rs := []rune(s)
	if len(rs) <= n {
		return s
	}
	return string(rs[:n])
}
