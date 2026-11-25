// plugin: rolling-memory v0.0.58
// Package rollingmemory 记忆管理（全量浏览 / 多维筛选 / 多列排序 / 详情溯源）、独立「检索召回」页面与实时控制台聚合后端实现。
package rollingmemory

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/ai-go-hub/ai-go-admin/internal/kit/httpx"
	"github.com/gin-gonic/gin"

	_ "modernc.org/sqlite"
)

// localVaultCarrier 是现役记忆载体标识（唯一运行形态 = 本地明文金库）。
// 历史协议字符串不再出现在控制台交付面。
const localVaultCarrier = "local_plain"

// MemoriesQueryRequest 全量记忆列表筛选、排序与分页请求。
//
// 契约只声明薄客户端本地金库（rm_units / rm_noetic）真实具备的维度：
// 层（background / episodic / noetic）、状态（L3 active / stale）、关键词、排序与分页。
// 上一代曾经声明却从未实现、也无法从薄金库求值的字段（warrant / audience / temporal_class /
// carrier / incarnation / workspace / locus / origin / form_kind / confidence / S-X-O）已全部移除 ——
// 留下“声明了却永远返回空”的筛选面只会误导接入方。
type MemoriesQueryRequest struct {
	Scope     string `json:"scope"`      // 作用域标签（薄客户端为单租户本地金库，仅作读数标注）
	Layer     string `json:"layer"`      // background | episodic | noetic | ""（空 = 三层全量）
	Status    string `json:"status"`     // L3：active | stale | ""（L1/L2 单元恒为 active）
	Keyword   string `json:"keyword"`    // 关键词（匹配 ID / 证据原文 / 记忆地址 / L3 引证单元）
	SortBy    string `json:"sort_by"`    // id | layer | status | occurred_at（默认，等价 time/created_at）
	SortOrder string `json:"sort_order"` // desc（默认） | asc
	Page      int    `json:"page"`
	PageSize  int    `json:"page_size"`
}

// MemoryItem 单条结构化记忆单元（主体 s / 关系 x / 客体 o；含置信度、锚点原文切片、四维时空分身坐标、名册归属与元边统计）。
type MemoryItem struct {
	ID              string  `json:"id"`
	Scope           string  `json:"scope"`
	SubjectID       string  `json:"subject_id"`
	Subject         string  `json:"s"`
	SubjectOrigin   string  `json:"subject_origin"`
	PredicateID     string  `json:"predicate_id"`
	Predicate       string  `json:"x"`
	ObjectID        string  `json:"object_id"`
	Object          string  `json:"o"`
	ObjectOrigin    string  `json:"object_origin"`
	Rendered        string  `json:"rendered"`
	Confidence      float64 `json:"confidence"`
	Status          string  `json:"status"`
	Warrant         string  `json:"warrant"`
	Audience        string  `json:"audience"`
	TemporalClass   string  `json:"temporal_class"`
	World           string  `json:"world"`
	Carrier         string  `json:"carrier"`
	Locus           string  `json:"locus"`
	Workspace       string  `json:"workspace"`
	Incarnation     string  `json:"incarnation"`
	TValidFrom      int64   `json:"t_valid_from"`
	TValidTo        int64   `json:"t_valid_to"`
	TSource         int64   `json:"t_source"`
	CreatedAt       int64   `json:"created_at"`
	ValidTimeText   string  `json:"valid_time_text"`
	SourceTimeText  string  `json:"source_time_text"`
	CreatedTimeText string  `json:"created_time_text"`
	Anchor          string  `json:"anchor"`
	AnchorTraceHash string  `json:"anchor_trace_hash"`
	AnchorStart     int     `json:"anchor_start"`
	AnchorEnd       int     `json:"anchor_end"`
	AnchorSnippet   string  `json:"anchor_snippet"`
	TraceSession    string  `json:"trace_session"`
	TraceAuthor     string  `json:"trace_author"`
	ActivationScore float64 `json:"activation_score"`
	ConflictCount   int     `json:"conflict_count"`
	SupersedeCount  int     `json:"supersede_count"`
	FormKind        string  `json:"form_kind"`        // ternary | binary_sx | binary_xo
	SubjectCategory string  `json:"subject_category"` // 主语上位概念（subtype-of）
	ObjectCategory  string  `json:"object_category"`  // 客体上位概念（subtype-of）
}

// FacetCount 筛选项分布统计。
type FacetCount struct {
	Value string `json:"value"`
	Count int    `json:"count"`
}

// MemoriesSummary 记忆库统计摘要与多维自动分类分布（含 Harness、Agent 分身、工作区/仓库、目录路径、名册归属）。
type MemoriesSummary struct {
	TotalFiltered   int          `json:"total_filtered"`
	TotalInScope    int          `json:"total_in_scope"`
	ActiveCount     int          `json:"active_count"`
	AvgConfidence   float64      `json:"avg_confidence"`
	HighConfCount   int          `json:"high_conf_count"`   // confidence >= 0.9
	AnchorFullCount int          `json:"anchor_full_count"` // 逐字锚完整（有 trace 哈希且区间非空）
	PrivateCount    int          `json:"private_count"`
	ConflictEdges   int          `json:"conflict_edges"`
	Scopes          []FacetCount `json:"scopes"`
	Statuses        []FacetCount `json:"statuses"`
	Warrants        []FacetCount `json:"warrants"`
	Audiences       []FacetCount `json:"audiences"`
	Temporals       []FacetCount `json:"temporals"`
	Carriers        []FacetCount `json:"carriers"`     // Harness / 载体自动分类
	Incarnations    []FacetCount `json:"incarnations"` // Agent 分身角色自动分类
	Workspaces      []FacetCount `json:"workspaces"`   // 工作区 / Git 仓库自动分类
	Loci            []FacetCount `json:"loci"`         // 空间 / 目录路径自动分类
	Origins         []FacetCount `json:"origins"`      // self / roster / world 名册归属分布
	TopPredicates   []FacetCount `json:"top_predicates"`
}

// MemoriesQueryResponse 全量记忆管理查询响应。
type MemoriesQueryResponse struct {
	DBPath   string          `json:"db_path"`
	Scope    string          `json:"scope"`
	Page     int             `json:"page"`
	PageSize int             `json:"page_size"`
	Total    int             `json:"total"`
	Summary  MemoriesSummary `json:"summary"`
	Items    []MemoryItem    `json:"items"`
}

// MemoryDetailResponse 单条记忆完整溯源响应。
type MemoryDetailResponse struct {
	Item          MemoryItem `json:"item"`
	TraceText     string     `json:"trace_text"`
	TraceBefore   string     `json:"trace_before"`
	TraceAnchor   string     `json:"trace_anchor"`
	TraceAfter    string     `json:"trace_after"`
	TraceOccurred string     `json:"trace_occurred"`
}

// RecallQueryRequest 独立检索召回工作台请求。
type RecallQueryRequest struct {
	Query          string   `json:"q"`
	Scope          string   `json:"scope"`
	Profile        string   `json:"profile"`
	Reader         string   `json:"reader"`      // owner | deliverable
	ResultMode     string   `json:"result_mode"` // full | probe
	PresenceLocus  string   `json:"locus"`       // 可选在场空间路径（触发软势能场近场共振加权）
	Carrier        string   `json:"carrier"`     // 可选在场 Harness 载体
	Incarnation    string   `json:"incarnation"` // 可选在场 Agent 分身角色
	Bands          int      `json:"bands"`
	Nodes          int      `json:"nodes"`
	Chars          int      `json:"chars"`
	IncludeRaw     bool     `json:"include_raw"`
	RawLimit       int      `json:"raw_limit"`
	MinConfidence  *float64 `json:"min_confidence"`  // 结果集内按置信度二次过滤
	SortBy         string   `json:"sort_by"`         // score | confidence | time
	Workspace      string   `json:"workspace"`       // 可选工作区锚（= 入库 locus）：同工作区证据置前、其余不删
	Crystallize    bool     `json:"crystallize"`     // 是否把本次检索写成 L3 Noetic；默认 false（调试台只读）
	DeliveryWindow string   `json:"delivery_window"` // 智能交付窗口档位：0-16（默认）| 0-32 | 0-64
}

// RecallHitItem 扁平化召回记忆单元条目（带置信度、综合检索得分、所属带与链、四维时空分身坐标、逐字证据锚切片）。
type RecallHitItem struct {
	ID            string  `json:"id"`
	BandID        string  `json:"band_id"`
	BandTitle     string  `json:"band_title"`
	BandSubject   string  `json:"band_subject"`
	BandTimeLabel string  `json:"band_time_label"`
	BandSpace     string  `json:"band_space"`
	ChainType     string  `json:"chain_type"`
	ChainLabel    string  `json:"chain_label"`
	Subject       string  `json:"s"`
	Predicate     string  `json:"x"`
	Object        string  `json:"o"`
	Rendered      string  `json:"rendered"`
	Confidence    float64 `json:"confidence"`
	Score         float64 `json:"score"`
	Status        string  `json:"status"`
	Warrant       string  `json:"warrant"`
	Time          string  `json:"time"`
	World         string  `json:"world"`
	Carrier       string  `json:"carrier"`
	Locus         string  `json:"locus"`
	Workspace     string  `json:"workspace"`
	Incarnation   string  `json:"incarnation"`
	Anchor        string  `json:"anchor"`
	AnchorSnippet string  `json:"anchor_snippet"`
	Evidence      string  `json:"evidence"` // fact | mention_only
}

// RecallQueryResponse 独立检索召回工作台响应。
type RecallQueryResponse struct {
	Scope                 string          `json:"scope"`
	Query                 string          `json:"q"`
	Reader                string          `json:"reader"`
	ResultMode            string          `json:"result_mode"`
	ActiveMode            string          `json:"active_mode"`
	ActiveProfile         string          `json:"active_profile"`
	ConfigProfile         string          `json:"config_profile"`
	AdvancedOps           bool            `json:"advanced_ops"`
	Absent                bool            `json:"absent"`
	ElapsedMS             int             `json:"elapsed_ms"`
	TotalBands            int             `json:"total_bands"`
	TotalChains           int             `json:"total_chains"`
	TotalHits             int             `json:"total_hits"`
	AvgConfidence         float64         `json:"avg_confidence"`
	MaxConfidence         float64         `json:"max_confidence"`
	MinConfidence         float64         `json:"min_confidence"`
	AvgScore              float64         `json:"avg_score"`
	MaxScore              float64         `json:"max_score"`
	AnchorsChecked        int             `json:"anchors_checked"`
	AnchorFailed          int             `json:"anchor_failed"`
	Seeds                 int             `json:"seeds"`
	Candidates            int             `json:"candidates"`
	Neighbors             int             `json:"neighbors"`
	Legs                  map[string]int  `json:"legs"`
	Gaps                  map[string]int  `json:"gaps"`
	Hits                  []RecallHitItem `json:"hits"`
	Bands                 []any           `json:"bands"`
	RawHits               []any           `json:"raw_hits"`
	Meta                  map[string]any  `json:"meta"`
	AvailableScopes       []FacetCount    `json:"available_scopes"`
	AvailableWorkspaces   []FacetCount    `json:"available_workspaces"`
	AvailableLoci         []FacetCount    `json:"available_loci"`
	AvailableIncarnations []FacetCount    `json:"available_incarnations"`
	AvailableCarriers     []FacetCount    `json:"available_carriers"`
	Workspace             string          `json:"workspace"`
	Crystallized          bool            `json:"crystallized"`
	DeliveryWindow        string          `json:"delivery_window"`
}

func sqlQuote(s string) string {
	cleaned := strings.ReplaceAll(s, "\x00", "")
	return "'" + strings.ReplaceAll(cleaned, "'", "''") + "'"
}

// ==================== 只读 SQLite 访问（进程内，免子进程） ====================

var (
	memDBMu     sync.Mutex
	memDBByPath = map[string]*memDBHandle{}
)

// memDBHandle 进程内只读连接池 + 打开时的文件身份（用于识别库被替换/重建）。
type memDBHandle struct {
	db   *sql.DB
	info os.FileInfo
}

// memoryRODB 返回指向 memory.db 的只读连接池（mode=ro：本插件永不写引擎库）。
// 每个请求都直接读最新提交（WAL 读事务），文件被替换时按 os.SameFile 重开，
// 避免拿着旧 inode 读到过期数据。
func memoryRODB(dbPath string) (*sql.DB, error) {
	info, err := os.Stat(dbPath)
	if err != nil {
		return nil, err
	}
	memDBMu.Lock()
	defer memDBMu.Unlock()
	if handle := memDBByPath[dbPath]; handle != nil {
		if os.SameFile(handle.info, info) {
			return handle.db, nil
		}
		_ = handle.db.Close()
		delete(memDBByPath, dbPath)
	}
	db, err := sql.Open("sqlite", sqliteFileURI(dbPath)+"?mode=ro&_pragma=busy_timeout(15000)&_pragma=cache_size(-32768)&_pragma=temp_store(MEMORY)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(16)
	db.SetMaxIdleConns(8)
	memDBByPath[dbPath] = &memDBHandle{db: db, info: info}
	return db, nil
}

// sqliteFileURI returns path in the `file:`-prefixed URI form SQLite DSNs use,
// percent-encoding '?' and '#' when present so the driver never misparses a
// directory or file name containing URI metacharacters as query or fragment.
// Windows paths always take the percent-encoded form with the drive letter or
// share kept inside the URI path (`file:///C:/...`, `file:////server/share/...`):
// an unanchored `C:\...` renders as `file://C:%5C...`, the drive letter becomes
// the URI authority, and the driver rejects the DSN with "invalid uri authority".
func sqliteFileURI(path string) string {
	if uriPath, ok := windowsURIPath(path); ok {
		return (&url.URL{Scheme: "file", Path: uriPath}).String()
	}
	if !strings.ContainsAny(path, "?#") {
		return "file:" + path
	}
	return (&url.URL{Scheme: "file", Path: path}).String()
}

// windowsURIPath reports the file: URI path form of a Windows drive or UNC
// path, rewriting the separators to forward slashes; other paths return
// ok=false unchanged.
func windowsURIPath(path string) (string, bool) {
	if len(path) >= 2 && path[1] == ':' {
		if c := path[0]; 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' {
			return "/" + strings.ReplaceAll(path, `\`, "/"), true
		}
	}
	if strings.HasPrefix(path, `\\`) {
		return strings.ReplaceAll(path, `\`, "/"), true
	}
	return "", false
}

// writeConsoleFileAtomic writes data to a unique same-directory temp file, fsyncs,
// and renames into place so concurrent HTTP requests or crashes never leave a
// truncated JSON state file on disk.
func writeConsoleFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp.*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	_ = os.Chmod(tmpName, perm)
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}

// sqliteQueryJSON 执行一条只读查询并整理成行映射（与旧 sqlite3 -json 口径一致：
// 整型 int64、实数 float64、文本 string、空值 nil）。
func sqliteQueryJSON(dbPath, query string) ([]map[string]any, error) {
	return sqliteQueryJSONArgs(dbPath, query)
}

// sqliteQueryJSONArgs 与 sqliteQueryJSON 同口径，但支持位置参数绑定：
// 记忆管理列表的筛选/排序/分页全部下推 SQL 后，用户输入一律走 `?` 占位符，
// 不再有任何字符串拼接，既消除注入面也避免手写转义的口径漂移。
func sqliteQueryJSONArgs(dbPath, query string, args ...any) ([]map[string]any, error) {
	db, err := memoryRODB(dbPath)
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSQLiteRows(rows)
}

// scanSQLiteRows 把结果集整理为行映射（整型 int64 / 实数 float64 / 文本 string / 空值 nil）。
func scanSQLiteRows(rows *sql.Rows) ([]map[string]any, error) {
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	values := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range values {
		ptrs[i] = &values[i]
	}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		item := make(map[string]any, len(cols))
		for i, col := range cols {
			if raw, ok := values[i].([]byte); ok {
				item[col] = string(raw)
				continue
			}
			item[col] = values[i]
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func formatUnixTime(ts int64, dateOnly bool) string {
	if ts <= 0 {
		return "-"
	}
	t := time.Unix(ts, 0)
	if dateOnly {
		return t.Format("2006-01-02")
	}
	return t.Format("2006-01-02 15:04:05")
}

func extractWorkspacePrefix(locus string) string {
	loc := strings.Trim(strings.TrimSpace(locus), "/")
	if loc == "" {
		return ""
	}
	parts := strings.Split(loc, "/")
	if len(parts) >= 2 {
		return parts[0] + "/" + parts[1]
	}
	return parts[0]
}

var (
	facetsCacheMu sync.Mutex
	facetsCache   = map[string]struct {
		sig     string
		summary MemoriesSummary
	}{}
)

var vaultWriteEpoch uint64

func currentVaultWriteEpoch() uint64 {
	facetsCacheMu.Lock()
	ep := vaultWriteEpoch
	facetsCacheMu.Unlock()
	return ep
}

func invalidateVaultReadCaches() {
	facetsCacheMu.Lock()
	vaultWriteEpoch++
	facetsCache = map[string]struct {
		sig     string
		summary MemoriesSummary
	}{}
	facetsCacheMu.Unlock()

	layerGlobalCountsMu.Lock()
	layerGlobalCountsCache = map[string]struct {
		sig    string
		counts layerGlobalCounts
	}{}
	layerGlobalCountsMu.Unlock()

	pointerDocsCacheMu.Lock()
	pointerDocsCache = map[string]pointerDocsCacheEntry{}
	pointerDocsCacheMu.Unlock()

	dashboardSnapMu.Lock()
	dashboardSnapVersion++
	dashboardSnapCache = map[string]*dashboardSnapEntry{}
	dashboardSnapMu.Unlock()

	roiVaultBundleMu.Lock()
	roiVaultBundleCache = map[string]*roiVaultBundleEntry{}
	roiVaultBundleMu.Unlock()

	artifactCellsMu.Lock()
	artifactCellsCache = map[string]*artifactCellsEntry{}
	artifactCellsMu.Unlock()

	hydrateMu.Lock()
	hydrateCache = map[string]hydrateCacheEntry{}
	hydrateMu.Unlock()

	recallCueCacheMu.Lock()
	recallCueCache = map[string]recallCueCacheEntry{}
	recallCueCacheMu.Unlock()

	go func() {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			invalidateDaemonViaSocket(home)
		}
		notifyStreamKindRefresh(streamKindStatus)
		notifyStreamKindRefresh(streamKindDashboard)
		notifyStreamKindRefresh(streamKindROI)
	}()
}

func facetsFileSignature(dbPath string) string {
	var b strings.Builder
	isLarge := false
	if st, err := os.Stat(dbPath); err == nil {
		if st.Size() >= 16*1024*1024 {
			isLarge = true
		}
		b.WriteString(strconv.FormatInt(st.Size(), 10))
		b.WriteByte(':')
		b.WriteString(strconv.FormatInt(st.ModTime().UnixNano(), 10))
	} else {
		b.WriteByte('-')
	}
	b.WriteByte('|')
	if st, err := os.Stat(dbPath + "-wal"); err == nil {
		if isLarge {
			// 生产大库（≥16MiB）后台 autowatch 每秒刷新 rm_meta 租约会持续改写 WAL mtime/size；
			// 按 15s 时间窗量化 WAL 指纹，避免每秒打穿全站只读缓存，而控制台自身写操作经 invalidateVaultReadCaches 即刻失效。
			b.WriteString(strconv.FormatInt(st.ModTime().Unix()/15, 10))
		} else {
			b.WriteString(strconv.FormatInt(st.Size(), 10))
			b.WriteByte(':')
			b.WriteString(strconv.FormatInt(st.ModTime().UnixNano(), 10))
		}
	} else {
		b.WriteByte('-')
	}
	facetsCacheMu.Lock()
	ep := vaultWriteEpoch
	facetsCacheMu.Unlock()
	if ep > 0 {
		b.WriteByte('|')
		b.WriteString(strconv.FormatUint(ep, 10))
	}
	return b.String()
}

func queryFacets(dbPath, scope string) MemoriesSummary {
	// 作用域本就是本地单租户金库的读数标注：如实回显调用方请求的 scope（缺省 default），
	// 不再把它硬编码成常量 —— 否则「最近 Agent 检索」等页面的 scope 选项与实际读数会互相漂移。
	scopeLabel := strings.TrimSpace(scope)
	if scopeLabel == "" || scopeLabel == LegacyUserScope {
		scopeLabel = UserScope
	}
	cacheKey := dbPath + "\x00" + scopeLabel
	sig := facetsFileSignature(dbPath)
	facetsCacheMu.Lock()
	if cached, ok := facetsCache[cacheKey]; ok && cached.sig == sig {
		facetsCacheMu.Unlock()
		return cached.summary
	}
	facetsCacheMu.Unlock()

	summary := MemoriesSummary{
		Scopes:        []FacetCount{{Value: scopeLabel, Count: 0}},
		Statuses:      []FacetCount{},
		Warrants:      []FacetCount{},
		Audiences:     []FacetCount{},
		Temporals:     []FacetCount{},
		Carriers:      []FacetCount{},
		Incarnations:  []FacetCount{},
		Workspaces:    []FacetCount{},
		Loci:          []FacetCount{},
		Origins:       []FacetCount{},
		TopPredicates: []FacetCount{},
	}
	var bgChunksCnt, bgDocsCnt, epDocsCnt, noeticActiveCnt, noeticStaleCnt int
	if rows, err := sqliteQueryJSON(dbPath, `SELECT COUNT(*) AS cnt FROM rm_bg_chunks;`); err == nil && len(rows) > 0 {
		bgChunksCnt = asInt(rows[0]["cnt"])
	}
	if rows, err := sqliteQueryJSON(dbPath, `SELECT COALESCE(layer, 'episodic') AS l, COUNT(*) AS cnt FROM rm_units GROUP BY 1;`); err == nil {
		for _, r := range rows {
			if strings.EqualFold(asString(r["l"]), "background") {
				bgDocsCnt += asInt(r["cnt"])
			} else {
				epDocsCnt += asInt(r["cnt"])
			}
		}
	}
	if rows, err := sqliteQueryJSON(dbPath, `SELECT COALESCE(status, 'active') AS st, COUNT(*) AS cnt FROM rm_noetic GROUP BY 1;`); err == nil {
		for _, r := range rows {
			if strings.EqualFold(asString(r["st"]), "active") {
				noeticActiveCnt += asInt(r["cnt"])
			} else {
				noeticStaleCnt += asInt(r["cnt"])
			}
		}
	}
	bgTotal := bgChunksCnt + bgDocsCnt
	activeTotal := bgTotal + epDocsCnt + noeticActiveCnt
	totalInScope := activeTotal + noeticStaleCnt
	summary.TotalInScope = totalInScope
	summary.ActiveCount = activeTotal
	summary.AvgConfidence = 1.0
	summary.HighConfCount = activeTotal
	summary.AnchorFullCount = activeTotal
	summary.Scopes = []FacetCount{{Value: scopeLabel, Count: totalInScope}}
	summary.Statuses = []FacetCount{{Value: "active", Count: activeTotal}}
	if noeticStaleCnt > 0 {
		summary.Statuses = append(summary.Statuses, FacetCount{Value: "stale", Count: noeticStaleCnt})
	}
	summary.Warrants = []FacetCount{
		{Value: "background", Count: bgTotal},
		{Value: "episodic", Count: epDocsCnt},
		{Value: "noetic", Count: noeticActiveCnt},
	}
	// 工作区 = 入库时按仓库目录名打的 locus 标签（与 recall 的 `[<工作区>]` 锚同一口径）。
	if wsRows, err := sqliteQueryJSON(dbPath, `
		SELECT locus, COUNT(*) AS cnt FROM rm_units
		WHERE COALESCE(locus, '') != ''
		GROUP BY locus ORDER BY cnt DESC, locus ASC;
	`); err == nil {
		for _, r := range wsRows {
			if ws, cnt := strings.TrimSpace(asString(r["locus"])), asInt(r["cnt"]); ws != "" && cnt > 0 {
				summary.Workspaces = append(summary.Workspaces, FacetCount{Value: ws, Count: cnt})
			}
		}
	}
	if locRows, err := sqliteQueryJSON(dbPath, `
		SELECT locus, SUM(cnt) AS total_cnt FROM (
			SELECT CASE WHEN COALESCE(locus, '') = '' THEN '(global)' ELSE locus END AS locus, COUNT(*) AS cnt FROM rm_units GROUP BY 1
			UNION ALL
			-- 背景分块数直接取文件登记值求和：历史实现按 c.abs_path = f.abs_path 文本键三表 JOIN
			-- （5.6 万分块 × 数百文件），在纯 Go 驱动下单次聚合要 2–4s，实时控制台每一帧都重跑；
			-- 本机逐目录核对 SUM(f.chunk_count) 与 JOIN 计数完全一致（9447/9447、46643/46643），
			-- 单测夹具未填 chunk_count 时自动回退子查询。
			SELECT
				CASE WHEN COALESCE(d.root_path, '') = '' THEN '(background)' ELSE d.root_path END AS locus,
				CASE
					WHEN COALESCE(SUM(f.chunk_count), 0) > 0 THEN SUM(f.chunk_count)
					ELSE (SELECT COUNT(c.unit_id) FROM rm_bg_files f2 JOIN rm_bg_chunks c ON c.abs_path = f2.abs_path WHERE f2.dir_id = d.dir_id)
				END AS cnt
			FROM rm_bg_dirs d
			JOIN rm_bg_files f ON f.dir_id = d.dir_id
			GROUP BY d.dir_id, 1
		) GROUP BY 1;
	`); err == nil {
		// 会话记录以项目短名入库，背景目录以绝对路径入库：先按 basename 归一、再同名合并计数，
		// 最后取前 6——否则同一工作区会以两条同名柱重复出现，并挤掉真实的前 6 名。
		merged := map[string]int{}
		for _, r := range locRows {
			loc := strings.TrimSpace(asString(r["locus"]))
			if strings.Contains(loc, "/") {
				loc = filepath.Base(loc)
			}
			if cnt := asInt(r["total_cnt"]); loc != "" && cnt > 0 {
				merged[loc] += cnt
			}
		}
		type workspaceCount struct {
			name  string
			count int
		}
		ranked := make([]workspaceCount, 0, len(merged))
		for name, cnt := range merged {
			ranked = append(ranked, workspaceCount{name: name, count: cnt})
		}
		sort.Slice(ranked, func(i, j int) bool {
			if ranked[i].count != ranked[j].count {
				return ranked[i].count > ranked[j].count
			}
			return ranked[i].name < ranked[j].name
		})
		if len(ranked) > 6 {
			ranked = ranked[:6]
		}
		for _, item := range ranked {
			summary.TopPredicates = append(summary.TopPredicates, FacetCount{Value: item.name, Count: item.count})
		}
	}
	summary.Carriers = []FacetCount{{Value: localVaultCarrier, Count: totalInScope}}
	facetsCacheMu.Lock()
	facetsCache[cacheKey] = struct {
		sig     string
		summary MemoriesSummary
	}{sig: sig, summary: summary}
	facetsCacheMu.Unlock()
	return summary
}

// queryMemoriesFromDB 只读查询三层记忆列表：层 / 状态 / 关键词过滤与排序、分页全部下推 SQL，
// 不再把整库读进内存；响应仅做展示层字段装配（薄金库没有 S/X/O 信念度，故如实降级为派生展示度量）。
func queryMemoriesFromDB(dbPath string, req MemoriesQueryRequest) (MemoriesQueryResponse, error) {
	if strings.TrimSpace(req.Scope) == "" {
		req.Scope = UserScope
	}
	if req.Page <= 0 {
		req.Page = 1
	}
	if req.PageSize <= 0 {
		req.PageSize = 20
	}
	if req.PageSize > 100 {
		req.PageSize = 100
	}

	summary := queryFacets(dbPath, req.Scope)
	summary.AvgConfidence = 1.0

	sortField := strings.ToLower(strings.TrimSpace(req.SortBy))
	layerNorm := strings.ToLower(strings.TrimSpace(req.Layer))
	if layerNorm != backgroundLayer && layerNorm != episodicLayer && layerNorm != noeticLayer {
		layerNorm = ""
	}
	statusNorm := strings.ToLower(strings.TrimSpace(req.Status))
	if statusNorm != "active" && statusNorm != "stale" {
		statusNorm = ""
	}
	kwNorm := strings.ToLower(strings.TrimSpace(req.Keyword))
	offset := (req.Page - 1) * req.PageSize

	// 无关键词筛选时的快速索引通道：直接利用单表 ORDER BY ... LIMIT (offset+pageSize) 截断，
	// 省去对数万行 rm_units + rm_noetic 的全表 UNION ALL 扫描与全表排序。
	if kwNorm == "" && (sortField == "" || sortField == "occurred_at" || sortField == "id") && offset >= 0 && offset+req.PageSize <= 1000 {
		dir := "DESC"
		if strings.EqualFold(strings.TrimSpace(req.SortOrder), "asc") {
			dir = "ASC"
		}
		docOrder := fmt.Sprintf("occurred_at %s, doc_id ASC", dir)
		noeticOrder := fmt.Sprintf("occurred_at %s, noetic_id ASC", dir)
		if sortField == "id" {
			docOrder = fmt.Sprintf("doc_id %s", dir)
			noeticOrder = fmt.Sprintf("noetic_id %s", dir)
		}
		var tmp LayerStatsResponse
		fillCachedLayerGlobalCounts(dbPath, &tmp)
		bgDocsOnly := tmp.BackgroundCount - tmp.BackgroundChunkCount
		if bgDocsOnly < 0 {
			bgDocsOnly = 0
		}
		branchCap := offset + req.PageSize
		var branches []string
		var fastTotal int
		if (layerNorm == "" || layerNorm == backgroundLayer || layerNorm == episodicLayer) && (statusNorm == "" || statusNorm == "active") && vaultTablePresent(dbPath, "rm_units") {
			docWhere := "1=1"
			if layerNorm == backgroundLayer {
				docWhere = "lower(trim(COALESCE(layer, ''))) IN ('background', 'bg', 'substrate', 'document', 'doc')"
				fastTotal += bgDocsOnly
			} else if layerNorm == episodicLayer {
				docWhere = "lower(trim(COALESCE(layer, ''))) NOT IN ('background', 'bg', 'substrate', 'document', 'doc', 'noetic', 'qa', 'insight')"
				fastTotal += tmp.EpisodicCount
			} else {
				fastTotal += bgDocsOnly + tmp.EpisodicCount
			}
			branches = append(branches, fmt.Sprintf(`SELECT * FROM (
				SELECT 'doc' AS src, doc_id AS id,
				       CASE WHEN lower(trim(COALESCE(layer, ''))) IN ('background', 'bg', 'substrate', 'document', 'doc') THEN 'background' ELSE 'episodic' END AS layer,
				       'active' AS status, occurred_at AS occurred_at, raw_text AS text,
				       COALESCE(address_uri, '') AS address_uri, '' AS answer_text, '' AS cited_json
				FROM rm_units WHERE %s ORDER BY %s LIMIT %d
			)`, docWhere, docOrder, branchCap))
		}
		if (layerNorm == "" || layerNorm == noeticLayer) && vaultTablePresent(dbPath, "rm_noetic") {
			nWhere := "1=1"
			if statusNorm == "active" {
				nWhere = "COALESCE(NULLIF(status, ''), 'active') = 'active'"
				fastTotal += tmp.NoeticActiveCount
			} else if statusNorm == "stale" {
				nWhere = "COALESCE(NULLIF(status, ''), 'active') <> 'active'"
				fastTotal += tmp.NoeticStaleCount
			} else {
				fastTotal += tmp.NoeticActiveCount + tmp.NoeticStaleCount
			}
			branches = append(branches, fmt.Sprintf(`SELECT * FROM (
				SELECT 'noetic' AS src, noetic_id AS id, 'noetic' AS layer,
				       COALESCE(NULLIF(status, ''), 'active') AS status, occurred_at AS occurred_at,
				       query_text AS text, COALESCE(address_uri, '') AS address_uri,
				       answer_text AS answer_text, COALESCE(cited_unit_ids_json, '[]') AS cited_json
				FROM rm_noetic WHERE %s ORDER BY %s LIMIT %d
			)`, nWhere, noeticOrder, branchCap))
		}
		if len(branches) > 0 {
			summary.TotalFiltered = fastTotal
			summary.TotalInScope = fastTotal
			summary.ActiveCount = fastTotal
			summary.HighConfCount = fastTotal
			summary.AnchorFullCount = fastTotal
			if fastTotal == 0 {
				return MemoriesQueryResponse{
					DBPath: dbPath, Scope: req.Scope, Page: req.Page, PageSize: req.PageSize,
					Total: 0, Summary: summary, Items: []MemoryItem{},
				}, nil
			}
			fastSQL := "SELECT src, id, layer, status, occurred_at, text, address_uri, answer_text, cited_json FROM (" +
				strings.Join(branches, " UNION ALL ") + ")" + memoriesOrderSQL(req.SortBy, req.SortOrder) + " LIMIT ? OFFSET ?"
			if rows, fErr := sqliteQueryJSONArgs(dbPath, fastSQL, req.PageSize, offset); fErr == nil {
				if home, hErr := os.UserHomeDir(); hErr == nil && home != "" && len(rows) > 0 {
					batchRows := make([]map[string]any, len(rows))
					for i, r := range rows {
						batchRows[i] = map[string]any{"id": r["id"], "content": r["text"]}
					}
					warmHydratePointersBatch(home, dbPath, batchRows)
					for i := range rows {
						rows[i]["_hydrated_ref_text"] = batchRows[i]["_hydrated_ref_text"]
						rows[i]["_hydrated_ref_checked"] = batchRows[i]["_hydrated_ref_checked"]
					}
				}
				items := make([]MemoryItem, 0, len(rows))
				for _, r := range rows {
					items = append(items, memoryItemFromSQL(r, req.Scope))
				}
				return MemoriesQueryResponse{
					DBPath: dbPath, Scope: req.Scope, Page: req.Page, PageSize: req.PageSize,
					Total: fastTotal, Summary: summary, Items: items,
				}, nil
			}
		}
	}

	union, args := memoriesUnionSQL(dbPath, req)
	empty := MemoriesQueryResponse{
		DBPath:   dbPath,
		Scope:    req.Scope,
		Page:     req.Page,
		PageSize: req.PageSize,
		Total:    0,
		Summary:  summary,
		Items:    []MemoryItem{},
	}
	if union == "" {
		return empty, nil
	}

	countRows, err := sqliteQueryJSONArgs(dbPath, "SELECT COUNT(*) AS cnt FROM ("+union+") AS u", args...)
	if err != nil {
		return empty, err
	}
	total := 0
	if len(countRows) > 0 {
		total = asInt(countRows[0]["cnt"])
	}
	summary.TotalFiltered = total
	summary.TotalInScope = total
	summary.ActiveCount = total
	summary.HighConfCount = total
	summary.AnchorFullCount = total
	empty.Total = total
	empty.Summary = summary
	if total == 0 {
		return empty, nil
	}

	pageSQL := "SELECT src, id, layer, status, occurred_at, text, address_uri, answer_text, cited_json" +
		" FROM (" + union + ") AS u" + memoriesOrderSQL(req.SortBy, req.SortOrder) + " LIMIT ? OFFSET ?"
	pageArgs := append(append([]any{}, args...), req.PageSize, (req.Page-1)*req.PageSize)
	rows, err := sqliteQueryJSONArgs(dbPath, pageSQL, pageArgs...)
	if err != nil {
		return empty, err
	}
	if home, hErr := os.UserHomeDir(); hErr == nil && home != "" && len(rows) > 0 {
		batchRows := make([]map[string]any, len(rows))
		for i, r := range rows {
			batchRows[i] = map[string]any{"id": r["id"], "content": r["text"]}
		}
		warmHydratePointersBatch(home, dbPath, batchRows)
		for i := range rows {
			rows[i]["_hydrated_ref_text"] = batchRows[i]["_hydrated_ref_text"]
			rows[i]["_hydrated_ref_checked"] = batchRows[i]["_hydrated_ref_checked"]
		}
	}
	items := make([]MemoryItem, 0, len(rows))
	for _, r := range rows {
		items = append(items, memoryItemFromSQL(r, req.Scope))
	}
	empty.Items = items
	return empty, nil
}

// memoriesUnionSQL 把三层记忆投影成统一可查询视图（L1/L2 来自 rm_units，L3 来自 rm_noetic）。
func memoriesUnionSQL(dbPath string, req MemoriesQueryRequest) (string, []any) {
	layer := strings.ToLower(strings.TrimSpace(req.Layer))
	if layer != backgroundLayer && layer != episodicLayer && layer != noeticLayer {
		layer = ""
	}
	status := strings.ToLower(strings.TrimSpace(req.Status))
	if status != "active" && status != "stale" {
		status = ""
	}
	kw := strings.ToLower(strings.TrimSpace(req.Keyword))

	var selects []string
	var args []any

	// L1/L2：对话记录与挂载目录独立文档（层标签按引擎口径归一）。
	includeDocs := layer == "" || layer == backgroundLayer || layer == episodicLayer
	includeDocs = includeDocs && (status == "" || status == "active")
	if includeDocs && vaultTablePresent(dbPath, "rm_units") {
		sel := `
			SELECT
				'doc' AS src,
				doc_id AS id,
				CASE
					WHEN lower(trim(COALESCE(layer, ''))) IN ('background', 'bg', 'substrate', 'document', 'doc') THEN 'background'
					ELSE 'episodic'
				END AS layer,
				'active' AS status,
				occurred_at AS occurred_at,
				raw_text AS text,
				COALESCE(address_uri, '') AS address_uri,
				'' AS answer_text,
				'' AS cited_json
			FROM rm_units`
		conds := []string{}
		if layer == backgroundLayer {
			conds = append(conds, `lower(trim(COALESCE(layer, ''))) IN ('background', 'bg', 'substrate', 'document', 'doc')`)
		} else if layer == episodicLayer {
			conds = append(conds, `lower(trim(COALESCE(layer, ''))) NOT IN ('background', 'bg', 'substrate', 'document', 'doc', 'noetic', 'qa', 'insight')`)
		}
		if kw != "" {
			var matchedPtrIDs []string
			if home, hErr := os.UserHomeDir(); hErr == nil && home != "" {
				if ptrRows, pErr := cachedVaultPointerDocRows(dbPath, layerListQuery{layer: layer, keyword: kw}); pErr == nil && len(ptrRows) > 0 {
					cands := ptrRows
					warmHydratePointersBatch(home, dbPath, cands)
					for _, r := range cands {
						resolved, ok := hydratedPointerRowText(r)
						if !ok || resolved == "" {
							continue
						}
						id := strings.TrimSpace(asString(r["id"]))
						addr := strings.TrimSpace(asString(r["address_uri"]))
						if strings.Contains(strings.ToLower(id), kw) ||
							strings.Contains(strings.ToLower(addr), kw) ||
							strings.Contains(strings.ToLower(resolved), kw) {
							matchedPtrIDs = append(matchedPtrIDs, sqlQuote(id))
						}
					}
				}
			}
			plainMatch := `instr(lower(doc_id || ' ' || CASE WHEN substr(ltrim(raw_text), 1, 5) = '@ref:' THEN '' ELSE raw_text END || ' ' || COALESCE(address_uri, '')), ?) > 0`
			if len(matchedPtrIDs) > 0 {
				conds = append(conds, `(`+plainMatch+` OR doc_id IN (`+strings.Join(matchedPtrIDs, ",")+`))`)
			} else {
				conds = append(conds, plainMatch)
			}
			args = append(args, kw)
		}
		if len(conds) > 0 {
			sel += " WHERE " + strings.Join(conds, " AND ")
		}
		selects = append(selects, sel)
	}

	// L3：Agent 提问 + 检索证据固化（状态是 L3 独有维度）。
	if layer == "" || layer == noeticLayer {
		if vaultTablePresent(dbPath, "rm_noetic") {
			sel := `
				SELECT
					'noetic' AS src,
					noetic_id AS id,
					'noetic' AS layer,
					COALESCE(NULLIF(status, ''), 'active') AS status,
					occurred_at AS occurred_at,
					query_text AS text,
					COALESCE(address_uri, '') AS address_uri,
					answer_text AS answer_text,
					COALESCE(cited_unit_ids_json, '[]') AS cited_json
				FROM rm_noetic`
			conds := []string{}
			if status != "" {
				conds = append(conds, `COALESCE(NULLIF(status, ''), 'active') = ?`)
				args = append(args, status)
			}
			if kw != "" {
				conds = append(conds, `instr(lower(noetic_id || ' ' || query_text || ' ' || answer_text || ' ' || COALESCE(cited_unit_ids_json, '')), ?) > 0`)
				args = append(args, kw)
			}
			if len(conds) > 0 {
				sel += " WHERE " + strings.Join(conds, " AND ")
			}
			selects = append(selects, sel)
		}
	}

	if len(selects) == 0 {
		return "", nil
	}
	return strings.Join(selects, "\n\t\t\tUNION ALL"), args
}

// memoriesOrderSQL 排序下推（同值时按 id 升序，保持与三层浏览器一致的稳定次序）。
func memoriesOrderSQL(sortBy, sortOrder string) string {
	expr := "occurred_at"
	switch strings.ToLower(strings.TrimSpace(sortBy)) {
	case "id":
		expr = "id"
	case "layer":
		expr = "layer"
	case "status":
		expr = "status"
	}
	dir := "DESC"
	if strings.EqualFold(strings.TrimSpace(sortOrder), "asc") {
		dir = "ASC"
	}
	return " ORDER BY " + expr + " " + dir + ", id ASC"
}

// hydrateVaultTextForDisplay resolves zero-copy `@ref:` pointers in display surfaces
// (detail modal, recall snippet preview, recent memory streams) so users see actual
// conversation content rather than raw pointer strings.
// vaultRefGapMarker 是零拷贝指针无法水合时的展示占位（与产品 recent 同一措辞：
// 诚实披露来源缺口，绝不让原始 `@ref:` 指针以正文身份漏上展示面）。
const vaultRefGapMarker = "[来源缺口：指针身份未验证或内容不可用]"

func hydrateVaultTextForDisplay(dbPath, docID, rawTxt string) string {
	trimmed := strings.TrimSpace(rawTxt)
	if !strings.HasPrefix(trimmed, "@ref:") {
		return rawTxt
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return vaultRefGapMarker
	}
	if resolved, ok := hydratedEpisodicPointerLive(home, dbPath, docID, trimmed); ok && strings.TrimSpace(resolved) != "" {
		return resolved
	}
	return vaultRefGapMarker
}

// hydrateEpisodicDisplayText 为图谱等展示面解析本地金库行的可读正文：
// 零拷贝 `@ref:` 指针行走规范水合（守护进程优先），非指针行沿用已蒸馏正文；
// 无法水合的指针行回「来源缺口」占位，绝不把指针原样当正文。
func hydrateEpisodicDisplayText(home, dbPath, docID, rawText, distilled string) (string, string) {
	txt := distilled
	if txt == "" {
		txt = rawText
	}
	span := rawText
	if raw := strings.TrimSpace(rawText); strings.HasPrefix(raw, "@ref:") {
		if hydrated, ok := hydratedEpisodicPointerLive(home, dbPath, docID, raw); ok && hydrated != "" {
			txt, span = hydrated, hydrated
		} else {
			txt, span = vaultRefGapMarker, vaultRefGapMarker
		}
	}
	return txt, span
}

// memoryItemFromSQL 把统一投影行装配为列表条目。
//
// 说明：薄客户端本地金库没有 S/X/O 三元组的“信念度”语义，S/X/O 与 confidence 只是把
// 「记忆地址 / 层 / 原文」映射到同一张表结构上的展示降级，不是引擎真值。
func memoryItemFromSQL(r map[string]any, scope string) MemoryItem {
	src := strings.ToLower(strings.TrimSpace(asString(r["src"])))
	id := asString(r["id"])
	layer := normalizeAdminMemoryLayer(asString(r["layer"]))
	status := asString(r["status"])
	if status == "" {
		status = "active"
	}
	ts := int64(asInt(r["occurred_at"]))
	tText := formatUnixTime(ts, false)
	title := asString(r["text"])
	if strings.HasPrefix(strings.TrimSpace(title), "@ref:") {
		if text, ok := hydratedPointerRowText(r); ok {
			title = text
		} else {
			title = vaultRefGapMarker
		}
	}
	title = strings.TrimSpace(title)
	addr := asString(r["address_uri"])

	if src == "noetic" {
		aTxt := asString(r["answer_text"])
		return MemoryItem{
			ID:              id,
			Scope:           scope,
			SubjectID:       id,
			Subject:         clipRunes(title, 60),
			SubjectOrigin:   noeticLayer,
			PredicateID:     "cites",
			Predicate:       noeticLayer,
			ObjectID:        id,
			Object:          clipRunes(aTxt, 120),
			ObjectOrigin:    noeticLayer,
			Rendered:        "Q: " + title + "\nA: " + aTxt,
			Confidence:      1.0,
			Status:          status,
			Warrant:         noeticLayer,
			Audience:        "shared",
			TemporalClass:   noeticLayer,
			Carrier:         localVaultCarrier,
			Locus:           asString(r["cited_json"]),
			TSource:         ts,
			CreatedAt:       ts,
			ValidTimeText:   formatUnixTime(ts, true),
			SourceTimeText:  tText,
			CreatedTimeText: tText,
			Anchor:          id,
			AnchorSnippet:   title,
			FormKind:        "ternary",
		}
	}

	cellKey := extractCellKeyFromURI(addr)
	return MemoryItem{
		ID:              id,
		Scope:           scope,
		SubjectID:       cellKey,
		Subject:         cellKey,
		SubjectOrigin:   layer,
		PredicateID:     "member_of",
		Predicate:       layer,
		ObjectID:        id,
		Object:          clipRunes(title, 120),
		ObjectOrigin:    "vault",
		Rendered:        title,
		Confidence:      1.0,
		Status:          status,
		Warrant:         layer,
		Audience:        "shared",
		TemporalClass:   "episodic",
		Carrier:         localVaultCarrier,
		Locus:           addr,
		Workspace:       cellKey,
		TSource:         ts,
		CreatedAt:       ts,
		ValidTimeText:   formatUnixTime(ts, true),
		SourceTimeText:  tText,
		CreatedTimeText: tText,
		Anchor:          addr,
		FormKind:        "ternary",
	}
}

func memoriesQueryHandler(c *gin.Context) {
	var req MemoriesQueryRequest
	if !bindJSON(c, &req) {
		return
	}
	home, _ := os.UserHomeDir()
	dbPath := resolveMemoryDBPath(home)
	resp, err := queryMemoriesFromDB(dbPath, req)
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("查询记忆列表失败: "+err.Error()))
		return
	}
	httpx.Success(c, httpx.WithData(resp))
}

type memoryDetailRequest struct {
	ID string `json:"id"`
}

func queryMemoryDetailFromDB(dbPath, id string) (MemoryDetailResponse, error) {
	cleanID := strings.TrimPrefix(strings.TrimSpace(id), "unit:")
	if tvRows, err := sqliteQueryJSON(dbPath, `
		SELECT doc_id, COALESCE(layer, 'episodic') AS layer, occurred_at, raw_text, address_uri
		FROM rm_units
		WHERE doc_id = `+sqlQuote(cleanID)+` LIMIT 1;
	`); err == nil && len(tvRows) > 0 {
		r := tvRows[0]
		docID := asString(r["doc_id"])
		layer := normalizeAdminMemoryLayer(asString(r["layer"]))
		addr := asString(r["address_uri"])
		rawTxt := hydrateVaultTextForDisplay(dbPath, docID, asString(r["raw_text"]))
		ts := int64(asInt(r["occurred_at"]))
		cellKey := extractCellKeyFromURI(addr)
		tText := formatUnixTime(ts, false)
		it := MemoryItem{
			ID:              docID,
			Scope:           UserScope,
			SubjectID:       cellKey,
			Subject:         cellKey,
			SubjectOrigin:   layer,
			PredicateID:     "member_of",
			Predicate:       layer,
			ObjectID:        docID,
			Object:          rawTxt,
			Rendered:        rawTxt,
			Confidence:      1.0,
			Status:          "active",
			Warrant:         layer,
			Audience:        "shared",
			Carrier:         localVaultCarrier,
			Locus:           addr,
			Workspace:       cellKey,
			TSource:         ts,
			CreatedAt:       ts,
			ValidTimeText:   formatUnixTime(ts, true),
			SourceTimeText:  tText,
			CreatedTimeText: tText,
			Anchor:          addr,
			FormKind:        "ternary",
		}
		return MemoryDetailResponse{
			Item:          it,
			TraceText:     rawTxt,
			TraceBefore:   "Address URI: " + addr + "\n\n",
			TraceAnchor:   rawTxt,
			TraceAfter:    "",
			TraceOccurred: tText,
		}, nil
	}
	if tnRows, err := sqliteQueryJSON(dbPath, `
		SELECT noetic_id, occurred_at, status, query_text, answer_text, cited_unit_ids_json
		FROM rm_noetic
		WHERE noetic_id = `+sqlQuote(cleanID)+` LIMIT 1;
	`); err == nil && len(tnRows) > 0 {
		r := tnRows[0]
		nid := asString(r["noetic_id"])
		qTxt := asString(r["query_text"])
		aTxt := asString(r["answer_text"])
		ts := int64(asInt(r["occurred_at"]))
		tText := formatUnixTime(ts, false)
		it := MemoryItem{
			ID:              nid,
			Scope:           UserScope,
			SubjectID:       nid,
			Subject:         qTxt,
			SubjectOrigin:   "noetic",
			PredicateID:     "cites",
			Predicate:       "noetic",
			ObjectID:        nid,
			Object:          aTxt,
			Rendered:        "Q: " + qTxt + "\nA: " + aTxt,
			Confidence:      1.0,
			Status:          asString(r["status"]),
			Warrant:         "noetic",
			Audience:        "shared",
			Carrier:         localVaultCarrier,
			Locus:           asString(r["cited_unit_ids_json"]),
			TSource:         ts,
			CreatedAt:       ts,
			ValidTimeText:   formatUnixTime(ts, true),
			SourceTimeText:  tText,
			CreatedTimeText: tText,
			Anchor:          nid,
			AnchorSnippet:   qTxt,
			FormKind:        "ternary",
		}
		return MemoryDetailResponse{
			Item:          it,
			TraceText:     "Q: " + qTxt + "\nA: " + aTxt,
			TraceBefore:   "Q: " + qTxt + "\n",
			TraceAnchor:   aTxt,
			TraceAfter:    "\nCited Units: " + asString(r["cited_unit_ids_json"]),
			TraceOccurred: tText,
		}, nil
	}
	if bgRows, err := sqliteQueryJSON(dbPath, `
		SELECT unit_id, abs_path, rel_path, chunk_index, anchor, occurred_at, address_uri
		FROM rm_bg_chunks
		WHERE unit_id = `+sqlQuote(cleanID)+` LIMIT 1;
	`); err == nil && len(bgRows) > 0 {
		r := bgRows[0]
		uid := asString(r["unit_id"])
		absP := asString(r["abs_path"])
		relP := asString(r["rel_path"])
		anchor := asString(r["anchor"])
		addr := asString(r["address_uri"])
		ts := int64(asInt(r["occurred_at"]))
		tText := formatUnixTime(ts, false)
		preview := fmt.Sprintf("[%s#%s] (%s)", relP, anchor, absP)
		// 分块正文按源文件磁盘现场读取（守护进程正典解析）；只有源文件被移动/删除或
		// 读取引擎未就绪时，才以来源位置预览兜底，绝不拿路径坐标冒充分块正文。
		body := preview
		if home, hErr := os.UserHomeDir(); hErr == nil && home != "" {
			if resolved, ok := hydratedLocalDocumentLive(home, uid); ok {
				body = resolved
			}
		}
		cellKey := extractCellKeyFromURI(addr)
		if cellKey == "" {
			cellKey = relP
		}
		it := MemoryItem{
			ID:              uid,
			Scope:           UserScope,
			SubjectID:       cellKey,
			Subject:         cellKey,
			SubjectOrigin:   "background",
			PredicateID:     "member_of",
			Predicate:       "background",
			ObjectID:        uid,
			Object:          clipRunes(body, 120),
			Rendered:        body,
			Confidence:      1.0,
			Status:          "active",
			Warrant:         "background",
			Audience:        "shared",
			Carrier:         localVaultCarrier,
			Locus:           addr,
			Workspace:       cellKey,
			TSource:         ts,
			CreatedAt:       ts,
			ValidTimeText:   formatUnixTime(ts, true),
			SourceTimeText:  tText,
			CreatedTimeText: tText,
			Anchor:          fmt.Sprintf("%s#%s", relP, anchor),
			FormKind:        "ternary",
		}
		return MemoryDetailResponse{
			Item:          it,
			TraceText:     body,
			TraceBefore:   "File: " + absP + "\nAddress URI: " + addr + "\n\n",
			TraceAnchor:   preview,
			TraceAfter:    "",
			TraceOccurred: tText,
		}, nil
	}

	return MemoryDetailResponse{}, fmt.Errorf("未找到记忆条目: %s", id)
}

func memoriesDetailHandler(c *gin.Context) {
	var req memoryDetailRequest
	if !bindJSON(c, &req) {
		return
	}
	if strings.TrimSpace(req.ID) == "" {
		httpx.Fail(c, httpx.WithMessage("记忆 ID 不能为空"))
		return
	}
	home, _ := os.UserHomeDir()
	dbPath := resolveMemoryDBPath(home)
	resp, err := queryMemoryDetailFromDB(dbPath, req.ID)
	if err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	httpx.Success(c, httpx.WithData(resp))
}

func lookupAssertionSpacetimeMeta(dbPath string, ids []string) (snippets, incarnations, loci, carriers map[string]string) {
	snippets = make(map[string]string, len(ids))
	incarnations = make(map[string]string, len(ids))
	loci = make(map[string]string, len(ids))
	carriers = make(map[string]string, len(ids))
	if len(ids) == 0 {
		return
	}
	quoted := make([]string, len(ids))
	for i, id := range ids {
		quoted[i] = sqlQuote(id)
	}
	q := `SELECT doc_id AS id, raw_text AS text, address_uri AS locus
		FROM rm_units
		WHERE doc_id IN (` + strings.Join(quoted, ",") + `);`
	rows, err := sqliteQueryJSON(dbPath, q)
	if err != nil {
		return
	}
	for _, r := range rows {
		id := asString(r["id"])
		snippets[id] = clipRunes(hydrateVaultTextForDisplay(dbPath, id, asString(r["text"])), 120)
		loci[id] = asString(r["locus"])
		carriers[id] = localVaultCarrier
	}
	return
}

// consoleRecallBudgetMS 检索调试台的引擎预算：引擎本地缺省 10s，负载或金库锁竞争下会把
// 正常查询打成 "context deadline exceeded"（页面表现为「查询失败」）。前端该请求已等待
// 120s（recall.vue RECALL_QUERY_TIMEOUT_MS），故控制台把预算放宽到 1 分钟——正常路径时延
// 不变，极端负载下延后失败判定而非误杀。
const consoleRecallBudgetMS = 60000

// recallQueryArgs 组装检索调试台的引擎 recall 参数。
//
// 调试台默认只读（--no-crystallize）：控制台里的试查不是 Agent 的真实提问，
// 不应被写成 L3 Noetic 污染认知层；只有显式勾选「写入 L3」时才按 Agent 同等路径结晶。
// 工作区锚走引擎现役的 cue 首部 `[<工作区>]` 语法（同工作区证据置前、其余不删）。
// 引擎预算显式携带 consoleRecallBudgetMS，避免落回引擎缺省 10s 在负载下误报超时失败。
func recallQueryArgs(dbPath, scope, reader, resultMode string, bands, nodes, chars, rawLimit int, q, workspace string, includeRaw, crystallize bool) []string {
	cue := q
	if workspace != "" {
		cue = "[" + workspace + "] " + q
	}
	args := []string{
		"recall",
		"--db", dbPath,
		"--scope", scope,
		"--reader", reader,
		"--result-mode", resultMode,
		"--bands", strconv.Itoa(bands),
		"--nodes", strconv.Itoa(nodes),
		"--chars", strconv.Itoa(chars),
		"--budget-ms", strconv.Itoa(consoleRecallBudgetMS),
		"--q", cue,
		"--json",
	}
	if includeRaw {
		args = append(args, "--include-raw", "--raw-limit", strconv.Itoa(rawLimit))
	} else {
		args = append(args, "--no-raw")
	}
	if !crystallize {
		args = append(args, "--no-crystallize")
	}
	return args
}

func resolveDaemonSocketPath(home string) string {
	if override := strings.TrimSpace(os.Getenv("ROLLING_MEMORY_DAEMON_SOCKET")); override != "" {
		return override
	}
	stateHome := strings.TrimSpace(os.Getenv("XDG_STATE_HOME"))
	if stateHome == "" {
		stateHome = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(stateHome, "rolling-memory", "engine.sock")
}

// engine 内部通道：控制台不再自带 socket 客户端（裸 socket 无法证明对端就是本机引擎，
// 同 UID 伪 socket 或旧配置守护进程会被冒认），一律 exec 产品二进制的
// `engine <op> --json`，由产品侧完成已核验连接（peer 凭据 + 守护身份握手）；守护进程
// 不可用或校验失败时由调用方按既有降级路径处置，绝不回退到未核验 socket。
const (
	// engineRecallExecSlack 在请求自身预算之外为子进程启动与守护自举预留的余量。
	engineRecallExecSlack = 15 * time.Second
	// engineDataExecTimeout 限定单次 hydrate/documents 子进程调用的整体时长。
	engineDataExecTimeout = 20 * time.Second
	// engineGraphExecTimeout 限定一次引擎图谱视图子进程调用（首帧可能构建投影）。
	engineGraphExecTimeout = 70 * time.Second
	// engineInvalidateExecTimeout 限定一次缓存失效通知的整体时长（尽力而为）。
	engineInvalidateExecTimeout = 3 * time.Second
	// engineResponseLimit 限定一次 engine 应答的捕获上限（守护进程自身按 8 MiB 截断
	// HTTP 体，超出即判协议失败），避免异常子进程撑爆控制台内存。
	engineResponseLimit = 16 << 20
	// engineStderrLimit 限定诊断输出的保留字节数。
	engineStderrLimit = 64 << 10
)

// engineCommandRunner 执行一次 `engine <op> --json` 内部通道：请求 JSON 走 stdin、
// 响应 JSON 走 stdout，返回 stdout 原文供调用方按引擎应答形状解析（子进程以退出码
// 区分成败；退出码非 0 时 stdout 仍可能携带 {"ok":false,...} 应答）。生产实现 exec
// 产品二进制；测试注入桩替换。
var engineCommandRunner = runEngineCommand

// engineCapture 有界捕获子进程输出：超过上限继续排空但不存储，避免子进程卡在写管道上。
type engineCapture struct {
	buffer   bytes.Buffer
	limit    int
	overflow bool
}

func (capture *engineCapture) Write(chunk []byte) (int, error) {
	room := capture.limit - capture.buffer.Len()
	if room <= 0 {
		capture.overflow = true
		return len(chunk), nil
	}
	if len(chunk) <= room {
		_, _ = capture.buffer.Write(chunk)
		return len(chunk), nil
	}
	_, _ = capture.buffer.Write(chunk[:room])
	capture.overflow = true
	return len(chunk), nil
}

func runEngineCommand(ctx context.Context, bin, op string, args []string, request []byte) ([]byte, error) {
	if strings.TrimSpace(bin) == "" {
		return nil, errors.New("rolling-memory binary not found")
	}
	// 测试二进制只允许显式指定的产品二进制：单测不得意外执行开发机上安装的真实产品二进制。
	if adminTestBinary() && strings.TrimSpace(os.Getenv("ROLLING_MEMORY_BIN")) == "" {
		return nil, errors.New("engine channel requires an explicit ROLLING_MEMORY_BIN under test binaries")
	}
	cmd := exec.CommandContext(ctx, bin, append([]string{"engine", op, "--json"}, args...)...)
	cmd.Stdin = bytes.NewReader(request)
	stdout := &engineCapture{limit: engineResponseLimit}
	stderr := &engineCapture{limit: engineStderrLimit}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.Env = stripCloudBrainEnv(os.Environ())
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return stdout.buffer.Bytes(), ctx.Err()
		}
		message := strings.TrimSpace(stderr.buffer.String())
		if message == "" {
			message = engineEnvelopeError(stdout.buffer.Bytes())
		}
		if message == "" {
			message = err.Error()
		}
		return stdout.buffer.Bytes(), errors.New(message)
	}
	if stdout.overflow {
		return stdout.buffer.Bytes(), errors.New("engine response exceeds the size limit")
	}
	return stdout.buffer.Bytes(), nil
}

func adminTestBinary() bool {
	exe := filepath.Base(os.Args[0])
	return strings.HasSuffix(exe, ".test") || strings.Contains(exe, ".test.")
}

// engineEnvelopeError extracts the failure text from the engine CLI's stdout
// envelope. Under --json the CLI writes {"ok":false,"error":...} to stdout and
// leaves stderr empty, so a non-zero exit alone would degrade to "exit status 1"
// and hide the daemon's actual reason from the console.
func engineEnvelopeError(raw []byte) string {
	if len(bytes.TrimSpace(raw)) == 0 {
		return ""
	}
	var envelope struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return ""
	}
	if envelope.OK {
		return ""
	}
	return strings.TrimSpace(envelope.Error)
}

var (
	daemonBootstrapMu      sync.Mutex
	daemonBootstrapLastTry time.Time
)

func triggerConsoleDaemonBootstrap(home, dbPath string) {
	if strings.HasSuffix(os.Args[0], ".test") || strings.Contains(os.Args[0], ".test.") {
		return
	}
	if dbPath != resolveMemoryDBPath(home) {
		return
	}
	daemonBootstrapMu.Lock()
	if time.Since(daemonBootstrapLastTry) < 15*time.Second {
		daemonBootstrapMu.Unlock()
		return
	}
	daemonBootstrapLastTry = time.Now()
	daemonBootstrapMu.Unlock()

	go func(h, db string) {
		bin := resolveBinary(h)
		if bin == "" {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, bin, "recall", "--db", db, "--q", "warmup", "--result-mode", "probe", "--no-crystallize", "--json")
		cmd.Env = stripCloudBrainEnv(os.Environ())
		_ = cmd.Run()
	}(home, dbPath)
}

// tryConsoleDaemonRecall 经产品二进制的 `engine recall` 内部通道请求本机唯一常驻守护进程
// （已核验连接：同 UID peer 凭据 + 守护身份握手在子进程内完成），命中已在 RAM 中预热的
// 语料快照（~80-120ms），省去每次检索 fork 一次性 CLI 子进程与冷扫 SQLite 的 1.5-2.5s
// 开销；若守护进程尚未启动、socket 不可达或校验失败，返回 handled=false 由下方 CLI
// 子进程保底执行（同时异步自举拉起守护进程），绝不回退到未核验 socket。
func tryConsoleDaemonRecall(
	ctx context.Context, home, dbPath, scope, reader, resultMode string,
	bands, nodes, chars, rawLimit int, q, workspace string,
	includeRaw, crystallize bool, presenceLocus, carrier, incarnation string,
) (map[string]any, bool, error) {
	if strings.EqualFold(strings.TrimSpace(os.Getenv("ROLLING_MEMORY_DAEMON")), "0") ||
		strings.EqualFold(strings.TrimSpace(os.Getenv("ROLLING_MEMORY_DAEMON")), "false") {
		return nil, false, nil
	}
	// 测试二进制默认不路由守护进程（与引擎侧 ShouldUseUnixDaemon 同一不变量），
	// 除非显式指定 socket 覆盖，避免单测串到开发机上的真实实例。
	if adminTestBinary() && strings.TrimSpace(os.Getenv("ROLLING_MEMORY_DAEMON_SOCKET")) == "" {
		return nil, false, nil
	}
	sockPath := resolveDaemonSocketPath(home)
	if _, err := os.Stat(sockPath); err != nil {
		triggerConsoleDaemonBootstrap(home, dbPath)
		return nil, false, nil
	}
	bin := resolveBinary(home)
	if bin == "" {
		triggerConsoleDaemonBootstrap(home, dbPath)
		return nil, false, nil
	}
	cue := q
	if workspace != "" {
		cue = "[" + workspace + "] " + q
	}
	budget := consoleRecallBudgetMS
	payload := map[string]any{
		"db":             dbPath,
		"cue":            cue,
		"scope":          scope,
		"reader":         reader,
		"mode":           resultMode,
		"caps_bands":     bands,
		"caps_nodes":     nodes,
		"caps_chars":     chars,
		"include_raw":    includeRaw,
		"raw_limit":      rawLimit,
		"budget_ms":      budget,
		"no_crystallize": !crystallize,
	}
	loc := strings.TrimSpace(presenceLocus)
	cr := strings.TrimSpace(carrier)
	inc := strings.TrimSpace(incarnation)
	if loc != "" || cr != "" || inc != "" {
		payload["presence"] = map[string]any{
			"world":       "actual",
			"locus":       loc,
			"carrier":     cr,
			"incarnation": inc,
		}
	}
	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, false, nil
	}
	opCtx, cancel := context.WithTimeout(ctx, time.Duration(budget)*time.Millisecond+engineRecallExecSlack)
	defer cancel()
	out, execErr := engineCommandRunner(opCtx, bin, "recall", []string{"--db", dbPath}, bodyBytes)
	var dResp struct {
		OK          bool           `json:"ok"`
		EngineError bool           `json:"engine_error"`
		Error       string         `json:"error"`
		Result      map[string]any `json:"result"`
		Probe       map[string]any `json:"probe"`
	}
	if json.Unmarshal(out, &dResp) != nil {
		if execErr != nil {
			triggerConsoleDaemonBootstrap(home, dbPath)
		}
		return nil, false, nil
	}
	if !dResp.OK {
		if dResp.EngineError && dResp.Error != "" {
			return nil, true, fmt.Errorf("%s", dResp.Error)
		}
		if execErr != nil {
			triggerConsoleDaemonBootstrap(home, dbPath)
		}
		return nil, false, nil
	}
	if resultMode == "probe" && dResp.Probe != nil {
		return dResp.Probe, true, nil
	}
	if dResp.Result != nil {
		return dResp.Result, true, nil
	}
	return nil, false, nil
}

// RecentCueItem 最近一次真实检索留下的线索（取自 L3 Noetic：Agent 每次带证据的 recall 都会结晶一条）。
type RecentCueItem struct {
	ID        string `json:"id"`
	Cue       string `json:"cue"`
	Workspace string `json:"workspace"`
	Status    string `json:"status"`
	Cited     int    `json:"cited"`
	TimeText  string `json:"time_text"`
}

// recentRecallCues 返回最近的检索线索（新→旧，同线索只留最新一条）。
// 线索首部的 `[<工作区>]` 锚拆为独立字段，便于在调试台按原工作区重放。
func recentRecallCues(dbPath string, limit int) []RecentCueItem {
	items := []RecentCueItem{}
	rows, err := sqliteQueryJSON(dbPath, `
		SELECT noetic_id, occurred_at, status, query_text, cited_unit_ids_json
		FROM rm_noetic
		ORDER BY occurred_at DESC, noetic_id ASC
		LIMIT 200;
	`)
	if err != nil {
		return items
	}
	seen := map[string]bool{}
	for _, r := range rows {
		cue := strings.TrimSpace(asString(r["query_text"]))
		if cue == "" || seen[cue] {
			continue
		}
		seen[cue] = true
		item := RecentCueItem{
			ID:       asString(r["noetic_id"]),
			Cue:      cue,
			Status:   asString(r["status"]),
			TimeText: formatUnixTime(int64(asInt(r["occurred_at"])), false),
		}
		if ws := recallCueWorkspacePattern.FindStringSubmatch(cue); ws != nil {
			item.Workspace = strings.TrimSpace(ws[1])
			item.Cue = strings.TrimSpace(cue[len(ws[0]):])
		}
		var cited []string
		if json.Unmarshal([]byte(asString(r["cited_unit_ids_json"])), &cited) == nil {
			item.Cited = len(cited)
		}
		items = append(items, item)
		if len(items) >= limit {
			break
		}
	}
	return items
}

// recallCueWorkspacePattern 与引擎 cue 工作区锚同一语法：首部 `[<工作区>]`。
var recallCueWorkspacePattern = regexp.MustCompile(`^\s*\[([^\[\]\r\n]{1,64})\]\s*`)

func recallRecentHandler(c *gin.Context) {
	var req struct {
		Limit int `json:"limit"`
	}
	if !bindJSON(c, &req) {
		return
	}
	if req.Limit <= 0 || req.Limit > 50 {
		req.Limit = 12
	}
	home, _ := os.UserHomeDir()
	httpx.Success(c, httpx.WithData(map[string]any{
		"list": recentRecallCues(resolveMemoryDBPath(home), req.Limit),
	}))
}

func recallQueryHandler(c *gin.Context) {
	var req RecallQueryRequest
	if !bindJSON(c, &req) {
		return
	}

	scope := strings.TrimSpace(req.Scope)
	if scope == "" {
		scope = UserScope
	}
	reader := strings.TrimSpace(req.Reader)
	if reader != "owner" && reader != "deliverable" {
		reader = "owner"
	}
	resultMode := strings.TrimSpace(req.ResultMode)
	if resultMode != "probe" {
		resultMode = "full"
	}
	home, _ := os.UserHomeDir()
	dbPath := resolveMemoryDBPath(home)
	bin := resolveBinary(home)
	if bin == "" {
		httpx.Fail(c, httpx.WithMessage("未找到 rolling-memory 引擎二进制"))
		return
	}

	widgetMu.RLock()
	wEnabled := widgetEnabled
	widgetMu.RUnlock()
	sw := parseConfigSwitches(filepath.Join(home, ".config", "rolling-memory", "config.toml"), wEnabled)
	effectiveProfile := "rame"
	activeWin := sw.DeliveryWindow
	if activeWin == "" {
		activeWin = "0-16"
	}
	switch strings.ToLower(strings.TrimSpace(req.DeliveryWindow)) {
	case "auto", "0-16", "16":
		activeWin = "0-16"
	case "0-32", "32":
		activeWin = "0-32"
	case "0-64", "64":
		activeWin = "0-64"
	}
	winCap := 16
	switch activeWin {
	case "0-32":
		winCap = 32
	case "0-64":
		winCap = 64
	}

	bands := req.Bands
	if strings.TrimSpace(req.DeliveryWindow) != "" {
		bands = winCap
	} else if bands <= 0 {
		bands = winCap
	}
	if bands > 64 {
		bands = 64
	}
	nodes := req.Nodes
	if nodes <= 0 {
		nodes = 60
	}
	chars := req.Chars
	if chars <= 0 {
		chars = 64000
	}
	rawLimit := req.RawLimit
	if rawLimit <= 0 {
		rawLimit = bands
	}

	facets := queryFacets(dbPath, scope)

	q := strings.TrimSpace(req.Query)
	workspace := strings.TrimSpace(req.Workspace)

	if q == "" {
		httpx.Success(c, httpx.WithData(RecallQueryResponse{
			Scope:                 scope,
			Query:                 "",
			Reader:                reader,
			ResultMode:            resultMode,
			ActiveMode:            sw.Mode,
			ActiveProfile:         effectiveProfile,
			ConfigProfile:         effectiveProfile,
			Absent:                false,
			Legs:                  map[string]int{},
			Gaps:                  map[string]int{},
			Hits:                  []RecallHitItem{},
			Bands:                 []any{},
			RawHits:               []any{},
			Meta:                  map[string]any{},
			AvailableScopes:       facets.Scopes,
			AvailableWorkspaces:   facets.Workspaces,
			AvailableLoci:         facets.Loci,
			AvailableIncarnations: facets.Incarnations,
			AvailableCarriers:     facets.Carriers,
			Workspace:             workspace,
			DeliveryWindow:        activeWin,
		}))
		return
	}

	cliScope := resolveBoundVaultScope(dbPath, scope)
	raw, daemonHandled, daemonErr := tryConsoleDaemonRecall(
		c.Request.Context(), home, dbPath, cliScope, reader, resultMode,
		bands, nodes, chars, rawLimit, q, workspace,
		req.IncludeRaw, req.Crystallize, req.PresenceLocus, req.Carrier, req.Incarnation,
	)
	if daemonHandled && daemonErr != nil {
		httpx.Fail(c, httpx.WithMessage("执行检索召回失败: "+daemonErr.Error()))
		return
	}
	if !daemonHandled {
		args := recallQueryArgs(dbPath, cliScope, reader, resultMode, bands, nodes, chars, rawLimit, q, workspace, req.IncludeRaw, req.Crystallize)

		cmd := exec.CommandContext(c.Request.Context(), bin, args...)
		// 子进程零云端覆盖变量：不注入任何远端端点/凭据（检索只读本地明文金库；
		// 用户身份由子进程自行解析）。
		cmd.Env = stripCloudBrainEnv(os.Environ())
		if loc := strings.TrimSpace(req.PresenceLocus); loc != "" {
			cmd.Env = append(cmd.Env, "ROLLING_MEMORY_PRESENCE_LOCUS="+loc)
		}
		if cr := strings.TrimSpace(req.Carrier); cr != "" {
			cmd.Env = append(cmd.Env, "ROLLING_MEMORY_PRESENCE_CARRIER="+cr)
		}
		if inc := strings.TrimSpace(req.Incarnation); inc != "" {
			cmd.Env = append(cmd.Env, "ROLLING_MEMORY_PRESENCE_INCARNATION="+inc)
		}
		out, err := cmd.CombinedOutput()
		if err != nil {
			httpx.Fail(c, httpx.WithMessage("执行检索召回失败: "+strings.TrimSpace(string(out))))
			return
		}
		if err := json.Unmarshal(out, &raw); err != nil {
			httpx.Fail(c, httpx.WithMessage("解析召回 JSON 失败: "+err.Error()))
			return
		}
	}

	resp := RecallQueryResponse{
		Scope:                 scope,
		Query:                 q,
		Reader:                reader,
		ResultMode:            resultMode,
		ActiveMode:            sw.Mode,
		ActiveProfile:         effectiveProfile,
		ConfigProfile:         effectiveProfile,
		DeliveryWindow:        activeWin,
		Absent:                raw["absent"] == true,
		Legs:                  map[string]int{},
		Gaps:                  map[string]int{},
		Hits:                  []RecallHitItem{},
		Bands:                 []any{},
		RawHits:               []any{},
		Meta:                  map[string]any{},
		AvailableScopes:       facets.Scopes,
		AvailableWorkspaces:   facets.Workspaces,
		AvailableLoci:         facets.Loci,
		AvailableIncarnations: facets.Incarnations,
		AvailableCarriers:     facets.Carriers,
		Workspace:             workspace,
		Crystallized:          req.Crystallize,
	}

	if gMap, ok := raw["gaps"].(map[string]any); ok {
		for k, v := range gMap {
			resp.Gaps[k] = asInt(v)
		}
	}
	if mMap, ok := raw["meta"].(map[string]any); ok {
		resp.Meta = mMap
		resp.ElapsedMS = asInt(mMap["elapsed_ms"])
		resp.AnchorsChecked = asInt(mMap["anchors_checked"])
		resp.AnchorFailed = asInt(mMap["anchor_failed"])
		resp.Seeds = asInt(mMap["seeds"])
		resp.Candidates = asInt(mMap["candidates"])
		resp.Neighbors = asInt(mMap["neighbors"])
		if legsMap, ok := mMap["legs"].(map[string]any); ok {
			for k, v := range legsMap {
				resp.Legs[k] = asInt(v)
			}
		}
	}
	if rHits, ok := raw["raw_hits"].([]any); ok {
		resp.RawHits = rHits
	}

	hitIDs := []string{}
	seenNode := map[string]bool{}

	if bList, ok := raw["bands"].([]any); ok {
		resp.Bands = bList
		resp.TotalBands = len(bList)
		for _, bAny := range bList {
			bMap, ok := bAny.(map[string]any)
			if !ok {
				continue
			}
			bID := asString(bMap["id"])
			bTitle := asString(bMap["title"])
			bSubj := asString(bMap["subject"])
			bTime := asString(bMap["time_label"])
			bSpace := asString(bMap["space_label"])

			chains, hasChains := bMap["chains"].([]any)
			if hasChains && len(chains) > 0 {
				resp.TotalChains += len(chains)
				for _, cAny := range chains {
					cMap, ok := cAny.(map[string]any)
					if !ok {
						continue
					}
					cType := asString(cMap["type"])
					cLabel := asString(cMap["label"])
					cNodes, _ := cMap["nodes"].([]any)
					for _, nAny := range cNodes {
						nMap, ok := nAny.(map[string]any)
						if !ok {
							continue
						}
						id := asString(nMap["id"])
						key := bID + "|" + cType + "|" + id
						if seenNode[key] {
							continue
						}
						seenNode[key] = true
						hit := RecallHitItem{
							ID:            id,
							BandID:        bID,
							BandTitle:     bTitle,
							BandSubject:   bSubj,
							BandTimeLabel: bTime,
							BandSpace:     bSpace,
							ChainType:     cType,
							ChainLabel:    cLabel,
							Subject:       firstNonEmptyStr(asString(nMap["label"]), asString(nMap["s"])),
							Predicate:     firstNonEmptyStr(asString(nMap["kind"]), asString(nMap["x"])),
							Object:        firstNonEmptyStr(asString(nMap["content"]), asString(nMap["o"])),
							Rendered:      firstNonEmptyStr(asString(nMap["rendered"]), asString(nMap["content"])),
							Confidence:    asFloat(nMap["relative_strength"]),
							Score:         asFloat(nMap["score"]),
							Status:        asString(nMap["status"]),
							Warrant:       firstNonEmptyStr(asString(nMap["kind"]), asString(nMap["warrant"])),
							Time:          asString(nMap["time"]),
							World:         asString(nMap["world"]),
							Carrier:       asString(nMap["carrier"]),
							Locus:         asString(nMap["locus"]),
							Anchor:        asString(nMap["anchor"]),
							Evidence:      asString(nMap["evidence"]),
						}
						if hit.Evidence == "" {
							hit.Evidence = "fact"
						}
						resp.Hits = append(resp.Hits, hit)
						hitIDs = append(hitIDs, id)
					}
				}
			} else if bNodes, ok := bMap["nodes"].([]any); ok {
				for _, nAny := range bNodes {
					nMap, ok := nAny.(map[string]any)
					if !ok {
						continue
					}
					id := asString(nMap["id"])
					if seenNode[id] {
						continue
					}
					seenNode[id] = true
					hit := RecallHitItem{
						ID:            id,
						BandID:        bID,
						BandTitle:     bTitle,
						BandSubject:   bSubj,
						BandTimeLabel: bTime,
						BandSpace:     bSpace,
						ChainType:     firstNonEmptyStr(asString(bMap["kind"]), "cell_band"),
						ChainLabel:    bTitle,
						Subject:       firstNonEmptyStr(asString(nMap["label"]), asString(nMap["s"])),
						Predicate:     firstNonEmptyStr(asString(nMap["kind"]), asString(nMap["x"])),
						Object:        firstNonEmptyStr(asString(nMap["content"]), asString(nMap["o"])),
						Rendered:      firstNonEmptyStr(asString(nMap["rendered"]), asString(nMap["content"])),
						Confidence:    asFloat(nMap["relative_strength"]),
						Score:         asFloat(nMap["score"]),
						Status:        asString(nMap["status"]),
						Warrant:       firstNonEmptyStr(asString(nMap["kind"]), asString(nMap["warrant"])),
						Time:          asString(nMap["time"]),
						World:         asString(nMap["world"]),
						Carrier:       asString(nMap["carrier"]),
						Locus:         asString(nMap["locus"]),
						Anchor:        asString(nMap["anchor"]),
						Evidence:      asString(nMap["evidence"]),
					}
					if hit.Evidence == "" {
						hit.Evidence = "fact"
					}
					resp.Hits = append(resp.Hits, hit)
					hitIDs = append(hitIDs, id)
				}
			}
		}
	}

	if len(resp.Hits) == 0 {
		if pHits, ok := raw["hits"].([]any); ok {
			for _, hAny := range pHits {
				nMap, ok := hAny.(map[string]any)
				if !ok {
					continue
				}
				id := asString(nMap["id"])
				hit := RecallHitItem{
					ID:         id,
					BandTitle:  "Probe Hit",
					ChainType:  "PROBE",
					Subject:    firstNonEmptyStr(asString(nMap["label"]), asString(nMap["s"])),
					Predicate:  firstNonEmptyStr(asString(nMap["kind"]), asString(nMap["x"])),
					Object:     firstNonEmptyStr(asString(nMap["content"]), asString(nMap["o"])),
					Rendered:   firstNonEmptyStr(asString(nMap["rendered"]), asString(nMap["content"])),
					Confidence: asFloat(nMap["relative_strength"]),
					Score:      asFloat(nMap["score"]),
					Status:     asString(nMap["status"]),
					Warrant:    firstNonEmptyStr(asString(nMap["kind"]), asString(nMap["warrant"])),
					Time:       asString(nMap["time"]),
					Anchor:     asString(nMap["anchor"]),
					Evidence:   asString(nMap["evidence"]),
				}
				resp.Hits = append(resp.Hits, hit)
				hitIDs = append(hitIDs, id)
			}
		}
	}

	snippets, incarnations, loci, carriers := lookupAssertionSpacetimeMeta(dbPath, hitIDs)
	filteredHits := make([]RecallHitItem, 0, len(resp.Hits))
	var sumConf, sumScore float64
	for i := range resp.Hits {
		id := resp.Hits[i].ID
		resp.Hits[i].AnchorSnippet = snippets[id]
		resp.Hits[i].Incarnation = incarnations[id]
		if resp.Hits[i].Locus == "" {
			resp.Hits[i].Locus = loci[id]
		}
		if resp.Hits[i].Carrier == "" {
			resp.Hits[i].Carrier = carriers[id]
		}
		resp.Hits[i].Workspace = extractWorkspacePrefix(resp.Hits[i].Locus)
		if req.MinConfidence != nil && *req.MinConfidence > 0 && resp.Hits[i].Confidence < *req.MinConfidence {
			continue
		}
		filteredHits = append(filteredHits, resp.Hits[i])
	}

	switch strings.TrimSpace(req.SortBy) {
	case "confidence":
		sort.SliceStable(filteredHits, func(i, j int) bool {
			if filteredHits[i].Confidence != filteredHits[j].Confidence {
				return filteredHits[i].Confidence > filteredHits[j].Confidence
			}
			return filteredHits[i].Score > filteredHits[j].Score
		})
	case "time":
		sort.SliceStable(filteredHits, func(i, j int) bool {
			ti, tj := strings.TrimSpace(filteredHits[i].Time), strings.TrimSpace(filteredHits[j].Time)
			if ti == "-" {
				ti = ""
			}
			if tj == "-" {
				tj = ""
			}
			if (ti == "") != (tj == "") {
				return ti != ""
			}
			return ti > tj
		})
	case "score":
		sort.SliceStable(filteredHits, func(i, j int) bool {
			if filteredHits[i].Score != filteredHits[j].Score {
				return filteredHits[i].Score > filteredHits[j].Score
			}
			return filteredHits[i].Confidence > filteredHits[j].Confidence
		})
	}

	resp.Hits = filteredHits
	resp.TotalHits = len(filteredHits)
	for i, h := range filteredHits {
		sumConf += h.Confidence
		sumScore += h.Score
		if i == 0 || h.Confidence > resp.MaxConfidence {
			resp.MaxConfidence = h.Confidence
		}
		if i == 0 || h.Confidence < resp.MinConfidence {
			resp.MinConfidence = h.Confidence
		}
		if i == 0 || h.Score > resp.MaxScore {
			resp.MaxScore = h.Score
		}
	}
	if len(filteredHits) > 0 {
		resp.AvgConfidence = sumConf / float64(len(filteredHits))
		resp.AvgScore = sumScore / float64(len(filteredHits))
	}

	httpx.Success(c, httpx.WithData(resp))
}

// DashboardRequest 实时控制台聚合请求。
type DashboardRequest struct {
	Scope string `json:"scope"`
}

// DashboardTrendSeries 多维趋势序列（三层认知记忆 L1/L2/L3 存量与增量 + 检索召回明细）。
type DashboardTrendSeries struct {
	Dates      []string `json:"dates"`
	Assertions []int    `json:"assertions"`
	Activity   []int    `json:"activity"`
	Recalls    []int    `json:"recalls"`
	Traces     []int    `json:"traces"`
	L1Added    []int    `json:"l1_added"`
	L2Added    []int    `json:"l2_added"`
	L3Added    []int    `json:"l3_added"`
}

// DashboardResponse 首页实时控制台（Dashboard）全量图表与卡片数据源。
type DashboardResponse struct {
	EngineOnline             bool                 `json:"engine_online"`
	Version                  string               `json:"version"`
	CoreVersion              string               `json:"core_version"`
	Mode                     string               `json:"mode"`
	Scope                    string               `json:"scope"`
	DBPath                   string               `json:"db_path"`
	DBSizeMB                 float64              `json:"db_size_mb"`
	UpdatedAt                string               `json:"updated_at"`
	Switches                 Switches             `json:"switches"`
	AvailableScopes          []FacetCount         `json:"available_scopes"`
	ActiveAssertions         int                  `json:"active_assertions"`
	TotalAssertions          int                  `json:"total_assertions"`
	L1Total                  int                  `json:"l1_total"`
	L2Total                  int                  `json:"l2_total"`
	L3Total                  int                  `json:"l3_total"`
	HighConfCount            int                  `json:"high_conf_count"`
	PrivateCount             int                  `json:"private_count"`
	HighConfRatio            float64              `json:"high_conf_ratio"`
	AssertionSpark           []int                `json:"assertion_spark"`
	TotalNodes               int                  `json:"total_nodes"`
	TotalTraces              int                  `json:"total_traces"`
	UserTraces               int                  `json:"user_traces"`
	AnchorFullRatio          float64              `json:"anchor_full_ratio"`
	NodeSpark                []int                `json:"node_spark"`
	LastRecallMS             int                  `json:"last_recall_ms"`
	AvgRecallMS              int                  `json:"avg_recall_ms"`
	LatencyP50MS             int                  `json:"latency_p50_ms"`
	LatencyMaxMS             int                  `json:"latency_max_ms"`
	LatencySamples           int                  `json:"latency_samples"`
	LatencyTrend             float64              `json:"latency_trend"`
	LatencySpark             []int                `json:"latency_spark"`
	TotalHookEvents          int                  `json:"total_hook_events"`
	InjectedEvents           int                  `json:"injected_events"`
	InjectRatio              float64              `json:"inject_ratio"`
	HookSpark                []int                `json:"hook_spark"`
	Trend1H                  DashboardTrendSeries `json:"trend_1h"`
	Trend1D                  DashboardTrendSeries `json:"trend_1d"`
	Trend7D                  DashboardTrendSeries `json:"trend_7d"`
	Trend30D                 DashboardTrendSeries `json:"trend_30d"`
	WarrantDist              []FacetCount         `json:"warrant_dist"`
	TopPredicates            []FacetCount         `json:"top_predicates"`
	HealthScore              float64              `json:"health_score"`
	AvgConfidence            float64              `json:"avg_confidence"`
	ConflictsProposed        int                  `json:"conflicts_proposed"`
	RadarAvailable           bool                 `json:"radar_available"`
	RadarCurrent             []float64            `json:"radar_current"`
	RadarBaseline            []float64            `json:"radar_baseline"`
	RadarCurrentLabel        string               `json:"radar_current_label"`
	RadarCurrentLabelEn      string               `json:"radar_current_label_en,omitempty"`
	RadarBaselineLabel       string               `json:"radar_baseline_label"`
	RadarBaselineLabelEn     string               `json:"radar_baseline_label_en,omitempty"`
	RadarHardCurrent         []float64            `json:"radar_hard_current,omitempty"`
	RadarHardBaseline        []float64            `json:"radar_hard_baseline,omitempty"`
	RadarHardCurrentLabel    string               `json:"radar_hard_current_label,omitempty"`
	RadarHardCurrentLabelEn  string               `json:"radar_hard_current_label_en,omitempty"`
	RadarHardBaselineLabel   string               `json:"radar_hard_baseline_label,omitempty"`
	RadarHardBaselineLabelEn string               `json:"radar_hard_baseline_label_en,omitempty"`
	RecentEvents             []HookEventItem      `json:"recent_events"`
	RecentAssertions         []RecentAssertion    `json:"recent_assertions"`
	Efficacy                 map[string]any       `json:"efficacy"`
}

func dashboardHandler(c *gin.Context) {
	var req DashboardRequest
	if !bindJSON(c, &req) {
		return
	}
	home, _ := os.UserHomeDir()
	httpx.Success(c, httpx.WithData(collectDashboardData(home, req.Scope)))
}

// dashboardFileSignature 采样影响实时控制台读数的全部本地文件指纹（Size + ModTime 纳秒）
// 与内存状态代数：250ms 轮询仅做极轻量 os.Stat 系统调用（0 SQL），指纹变化才重算推送。
// 注意：不包含 -shm（WAL 共享内存锁页在只读查询时也会被触碰），写入只体现在 .db 与 .db-wal。
func dashboardFileSignature(home, dbPath, logPath string, now time.Time) string {
	ledgerPath := dbPath + ".efficacy.sqlite"
	paths := []string{
		dbPath,
		dbPath + "-wal",
		ledgerPath,
		ledgerPath + "-wal",
		logPath,
		filepath.Join(home, ".config", "rolling-memory", "config.toml"),
		// 测评任务台账（admin SQLite，WAL 模式）：写入只体现在 .db 与 -wal，与上面 memory.db 同口径。
		evalLedgerDBPath(home),
		evalLedgerDBPath(home) + "-wal",
		"../VERSION",
		"VERSION",
	}
	var b strings.Builder
	for _, p := range paths {
		if info, err := os.Stat(p); err == nil {
			fmt.Fprintf(&b, "%d:%d|", info.Size(), info.ModTime().UnixNano())
		} else {
			b.WriteString("-|")
		}
	}
	widgetMu.RLock()
	wEnabled := widgetEnabled
	widgetMu.RUnlock()
	efficacyCacheMu.Lock()
	effVersion := efficacyCacheVersion
	efficacyCacheMu.Unlock()
	dashboardSnapMu.Lock()
	snapVer := dashboardSnapVersion
	dashboardSnapMu.Unlock()
	writeEp := currentVaultWriteEpoch()
	slot := now.Unix() / hookLogSlotSec
	fmt.Fprintf(&b, "w=%v|eff=%d|snap=%d|ep=%d|slot=%d", wEnabled, effVersion, snapVer, writeEp, slot)
	return b.String()
}

type dashboardSnapEntry struct {
	sig        string
	writeEpoch uint64
	at         time.Time
	refreshing bool
	resp       DashboardResponse
}

var (
	dashboardSnapMu      sync.Mutex
	dashboardSnapCache   = map[string]*dashboardSnapEntry{}
	dashboardSnapVersion uint64
	dashboardComputeMu   sync.Mutex
)

func collectDashboardData(home, reqScope string) DashboardResponse {
	scope := strings.TrimSpace(reqScope)
	if scope == "" {
		scope = UserScope
	}
	dbPath := resolveMemoryDBPath(home)
	logPath := filepath.Join(home, ".local", "state", "rolling-memory", "rolling-memory.log")
	now := time.Now()
	isLargeVault := false
	if info, err := os.Stat(dbPath); err == nil && info.Size() >= 16*1024*1024 {
		isLargeVault = true
	}
	cacheKey := dbPath + "\x00" + scope
	writeEp := currentVaultWriteEpoch()
	sig := dashboardFileSignature(home, dbPath, logPath, now)
	if isLargeVault {
		dashboardSnapMu.Lock()
		if entry := dashboardSnapCache[cacheKey]; entry != nil && entry.resp.EngineOnline {
			if entry.sig == sig || (entry.writeEpoch == writeEp && now.Sub(entry.at) < 10*time.Second) {
				r := entry.resp
				dashboardSnapMu.Unlock()
				return hydrateDashboardRecent(home, dbPath, r)
			}
			r := entry.resp
			if !entry.refreshing {
				entry.refreshing = true
				go func(h, sc, k string) {
					fresh := computeDashboardData(h, sc)
					dashboardSnapMu.Lock()
					dashboardSnapVersion++
					dashboardSnapMu.Unlock()
					curEp := currentVaultWriteEpoch()
					fSig := dashboardFileSignature(h, fresh.DBPath, filepath.Join(h, ".local", "state", "rolling-memory", "rolling-memory.log"), time.Now())
					dashboardSnapMu.Lock()
					dashboardSnapCache[k] = &dashboardSnapEntry{
						sig:        fSig,
						writeEpoch: curEp,
						at:         time.Now(),
						resp:       fresh,
					}
					dashboardSnapMu.Unlock()
					notifyStreamKindRefresh(streamKindDashboard)
				}(home, scope, cacheKey)
			}
			dashboardSnapMu.Unlock()
			return hydrateDashboardRecent(home, dbPath, r)
		}
		dashboardSnapMu.Unlock()
	}
	if isLargeVault {
		dashboardComputeMu.Lock()
		defer dashboardComputeMu.Unlock()
		dashboardSnapMu.Lock()
		if entry := dashboardSnapCache[cacheKey]; entry != nil && entry.resp.EngineOnline {
			if entry.sig == sig || (entry.writeEpoch == writeEp && time.Since(entry.at) < 10*time.Second) {
				r := entry.resp
				dashboardSnapMu.Unlock()
				return hydrateDashboardRecent(home, dbPath, r)
			}
		}
		dashboardSnapMu.Unlock()
	}
	resp := computeDashboardData(home, scope)
	if isLargeVault && resp.EngineOnline {
		dashboardSnapMu.Lock()
		dashboardSnapCache[cacheKey] = &dashboardSnapEntry{
			sig:        sig,
			writeEpoch: writeEp,
			at:         time.Now(),
			resp:       resp,
		}
		dashboardSnapMu.Unlock()
	}
	return hydrateDashboardRecent(home, dbPath, resp)
}

func hydrateDashboardRecent(home, dbPath string, resp DashboardResponse) DashboardResponse {
	resp.RecentAssertions = append([]RecentAssertion(nil), resp.RecentAssertions...)
	rows := make([]map[string]any, len(resp.RecentAssertions))
	for i, item := range resp.RecentAssertions {
		rows[i] = map[string]any{"id": item.ID, "content": item.Rendered}
	}
	warmHydratePointersBatch(home, dbPath, rows)
	for i, row := range rows {
		if strings.HasPrefix(strings.TrimSpace(asString(row["content"])), "@ref:") {
			if text, ok := hydratedPointerRowText(row); ok {
				resp.RecentAssertions[i].Rendered = text
			} else {
				resp.RecentAssertions[i].Rendered = vaultRefGapMarker
			}
		}
	}
	return resp
}

func computeDashboardData(home, reqScope string) DashboardResponse {
	scope := strings.TrimSpace(reqScope)
	if scope == "" {
		scope = UserScope
	}

	widgetMu.RLock()
	wEnabled := widgetEnabled
	widgetMu.RUnlock()

	switches := parseConfigSwitches(filepath.Join(home, ".config", "rolling-memory", "config.toml"), wEnabled)
	dbPath := resolveMemoryDBPath(home)
	logPath := filepath.Join(home, ".local", "state", "rolling-memory", "rolling-memory.log")

	var dbSizeBytes int64
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if info, err := os.Stat(dbPath + suffix); err == nil {
			dbSizeBytes += info.Size()
		}
	}
	dbSizeMB := float64(dbSizeBytes) / (1024.0 * 1024.0)

	resp := DashboardResponse{
		EngineOnline:      false,
		Version:           liveSoftwareVersion(),
		CoreVersion:       CoreVersion,
		Mode:              switches.Mode,
		Scope:             scope,
		DBPath:            dbPath,
		DBSizeMB:          float64(int(dbSizeMB*100)) / 100,
		UpdatedAt:         time.Now().Format("15:04:05"),
		Switches:          switches,
		AvailableScopes:   []FacetCount{},
		AssertionSpark:    make([]int, 12),
		NodeSpark:         make([]int, 12),
		LatencySpark:      make([]int, 12),
		HookSpark:         make([]int, 12),
		WarrantDist:       []FacetCount{},
		TopPredicates:     []FacetCount{},
		RadarCurrent:      []float64{},
		RadarBaseline:     []float64{},
		RadarHardCurrent:  []float64{},
		RadarHardBaseline: []float64{},
		RecentEvents:      []HookEventItem{},
		RecentAssertions:  []RecentAssertion{},
	}

	if bin := resolveBinary(home); bin != "" {
		resp.EngineOnline = true
	}

	now := time.Now()
	nowUnix := now.Unix()
	loc := now.Location()

	// 近 1 小时：12 个时钟对齐的 5 分钟槽；末槽为进行中的部分槽，
	// 桶边界不随请求时刻漂移（标签即槽起点，口径与 hooks 侧 5 分钟槽一致）。
	const buckets1H = 12
	curSlot := nowUnix - nowUnix%hookLogSlotSec
	start1HUnix := curSlot - int64(buckets1H-1)*hookLogSlotSec
	slotStarts := make([]int64, buckets1H)
	labels1H := make([]string, buckets1H)
	l1_1H := make([]int, buckets1H)
	l2_1H := make([]int, buckets1H)
	l3_1H := make([]int, buckets1H)
	for i := 0; i < buckets1H; i++ {
		slotStarts[i] = start1HUnix + int64(i)*hookLogSlotSec
		labels1H[i] = time.Unix(slotStarts[i], 0).In(loc).Format("15:04")
	}
	bucket1HIndex := func(ts int64) int {
		if ts < start1HUnix || ts > nowUnix+hookLogFutureSkew {
			return -1
		}
		idx := int((ts - start1HUnix) / hookLogSlotSec)
		if idx >= buckets1H {
			idx = buckets1H - 1
		}
		return idx
	}

	// 近 1 日：24 个整点小时桶（末桶为进行中的部分小时）
	const buckets1D = 24
	curHour := time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), 0, 0, 0, loc)
	start1DHour := curHour.Add(-time.Duration(buckets1D-1) * time.Hour)
	start1DUnix := start1DHour.Unix()
	hourKeys24 := make([]string, buckets1D)
	labels1D := make([]string, buckets1D)
	hourBgMap := make(map[string]int, buckets1D)
	hourEpMap := make(map[string]int, buckets1D)
	hourNtMap := make(map[string]int, buckets1D)
	for i := 0; i < buckets1D; i++ {
		h := start1DHour.Add(time.Duration(i) * time.Hour)
		hk := h.Format("2006-01-02 15")
		hourKeys24[i] = hk
		labels1D[i] = h.Format("15:00")
	}

	// 近 30 / 7 日：日历日桶（今日为进行中的部分日）
	const buckets30D = 30
	dayKeys30 := make([]string, buckets30D)
	dayLabels30 := make([]string, buckets30D)
	dayBgMap := make(map[string]int, buckets30D)
	dayEpMap := make(map[string]int, buckets30D)
	dayNtMap := make(map[string]int, buckets30D)
	for i := 0; i < buckets30D; i++ {
		d := now.AddDate(0, 0, -(buckets30D - 1 - i))
		dayKeys30[i] = d.Format("2006-01-02")
		dayLabels30[i] = fmt.Sprintf("%d/%d", int(d.Month()), d.Day())
	}
	// 30 天日桶窗口下界（本地自然日零点）：日桶聚合与召回事实账本共用一个下界，口径一致。
	start30DUnix := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, -(buckets30D - 1)).Unix()

	// 日志腿：单次扫描 + (size, mtime) 缓存，KPI 与趋势共用同一份聚合；
	// 趋势的「召回」只计 event="recall" 行（start/close 等收尾事件不计），
	// 无 scope 字段的历史行计入所有 scope 视图（新行带 scope 后自然精确归集）。
	logStat := loadHookLogStat(logPath)
	resp.TotalHookEvents = logStat.Total
	resp.InjectedEvents = logStat.Injected
	resp.RecentEvents = append([]HookEventItem{}, logStat.Recent...)
	// Recall latency comes only from the ledger's self-recorded timer below.
	// Hook-log ms values measure prompt-hook runtime, not retrieval, so they
	// must never become the card's headline.
	for i, slot := range slotStarts {
		resp.HookSpark[i] = logStat.hookSlot(slot)
	}
	if resp.TotalHookEvents > 0 {
		rate := float64(resp.InjectedEvents) * 100.0 / float64(resp.TotalHookEvents)
		resp.InjectRatio = float64(int(rate*10)) / 10
	}

	if _, statErr := os.Stat(dbPath); statErr == nil {
		resp.EngineOnline = true

		facets := queryFacets(dbPath, scope)
		resp.AvailableScopes = facets.Scopes
		resp.ActiveAssertions = facets.ActiveCount
		resp.TotalAssertions = facets.TotalInScope
		resp.AvgConfidence = float64(int(facets.AvgConfidence*1000)) / 1000
		resp.HighConfCount = facets.HighConfCount
		resp.PrivateCount = facets.PrivateCount
		resp.ConflictsProposed = facets.ConflictEdges
		resp.WarrantDist = facets.Warrants
		if len(facets.TopPredicates) > 6 {
			resp.TopPredicates = facets.TopPredicates[:6]
		} else {
			resp.TopPredicates = facets.TopPredicates
		}

		if resp.ActiveAssertions > 0 {
			highRatio := float64(resp.HighConfCount) * 100.0 / float64(resp.ActiveAssertions)
			resp.HighConfRatio = float64(int(highRatio*10)) / 10
			anchorRatio := float64(facets.AnchorFullCount) * 100.0 / float64(resp.ActiveAssertions)
			resp.AnchorFullRatio = float64(int(anchorRatio*10)) / 10
			resp.HealthScore = float64(int(facets.AvgConfidence*1000)) / 10
			if resp.HealthScore > 100 {
				resp.HealthScore = 100
			}
		}

		if cntRows, err := sqliteQueryJSON(dbPath, `SELECT
					(SELECT COUNT(*) FROM rm_bg_chunks) AS bg_cnt,
					COALESCE(SUM(CASE WHEN COALESCE(layer, 'episodic') = 'background' THEN 1 ELSE 0 END), 0) AS bg_doc_cnt,
					COALESCE(SUM(CASE WHEN COALESCE(layer, 'episodic') != 'background' THEN 1 ELSE 0 END), 0) AS ep_cnt,
					COALESCE(SUM(CASE WHEN COALESCE(layer, 'episodic') != 'background' AND substr(ltrim(raw_text), 1, 5) = '@ref:' THEN 1 ELSE 0 END), 0) AS ptr_cnt,
					COALESCE(SUM(CASE WHEN COALESCE(address_uri, '') != '' OR substr(ltrim(raw_text), 1, 5) = '@ref:' THEN 1 ELSE 0 END), 0) AS doc_addr_cnt,
					(SELECT COALESCE(SUM(CASE WHEN status = 'active' THEN 1 ELSE 0 END), 0) FROM rm_noetic) AS noetic_cnt,
					(SELECT COALESCE(SUM(CASE WHEN status = 'stale' THEN 1 ELSE 0 END), 0) FROM rm_noetic) AS noetic_stale_cnt,
					(SELECT COALESCE(SUM(CASE WHEN status = 'active' AND (COALESCE(cited_unit_ids_json, '[]') != '[]' OR COALESCE(address_uri, '') != '') THEN 1 ELSE 0 END), 0) FROM rm_noetic) AS noetic_addr_cnt
				FROM rm_units;`); err == nil && len(cntRows) > 0 {
			bgRawCnt := asInt(cntRows[0]["bg_cnt"])
			bgCnt := bgRawCnt + asInt(cntRows[0]["bg_doc_cnt"])
			epCnt := asInt(cntRows[0]["ep_cnt"])
			nCnt := asInt(cntRows[0]["noetic_cnt"])
			staleCnt := asInt(cntRows[0]["noetic_stale_cnt"])
			ptrCnt := asInt(cntRows[0]["ptr_cnt"])
			idxPtrCnt := ptrCnt
			if ptrCnt > 0 {
				usedCachedIdx := false
				if metaRows, mErr := sqliteQueryJSON(dbPath, `SELECT CAST(v AS TEXT) AS raw FROM rm_meta WHERE k = 'episodic_fault_report_cache' LIMIT 1;`); mErr == nil && len(metaRows) > 0 {
					var cachedFault struct {
						DocCnt int `json:"doc_cnt"`
						Report struct {
							Compatible int `json:"Compatible"`
						} `json:"report"`
					}
					if json.Unmarshal([]byte(asString(metaRows[0]["raw"])), &cachedFault) == nil && cachedFault.Report.Compatible > 0 {
						idxPtrCnt = cachedFault.Report.Compatible
						usedCachedIdx = true
					}
				}
				if !usedCachedIdx {
					if idxRows, iErr := sqliteQueryJSON(dbPath, `SELECT COUNT(DISTINCT d.doc_id) AS idx_ptr_cnt FROM rm_units d JOIN rm_episodic_index i ON i.doc_id = d.doc_id WHERE COALESCE(d.layer, 'episodic') != 'background' AND substr(ltrim(d.raw_text), 1, 5) = '@ref:';`); iErr == nil && len(idxRows) > 0 {
						idxPtrCnt = asInt(idxRows[0]["idx_ptr_cnt"])
					}
				}
			}
			addrCnt := bgRawCnt + asInt(cntRows[0]["doc_addr_cnt"]) + asInt(cntRows[0]["noetic_addr_cnt"])
			resp.L1Total = bgCnt
			resp.L2Total = epCnt
			resp.L3Total = nCnt
			resp.TotalNodes = bgCnt + epCnt + nCnt
			resp.TotalTraces = epCnt
			resp.UserTraces = epCnt
			resp.ConflictsProposed = staleCnt
			if resp.TotalNodes > 0 {
				idxRatio := 100.0
				if ptrCnt > 0 {
					idxRatio = float64(idxPtrCnt) * 100.0 / float64(ptrCnt)
					if idxRatio > 100.0 {
						idxRatio = 100.0
					}
				}
				addrRatio := float64(addrCnt) * 100.0 / float64(resp.TotalNodes)
				if addrRatio > 100.0 {
					addrRatio = 100.0
				}
				freshRatio := 100.0
				if nCnt+staleCnt > 0 {
					freshRatio = float64(nCnt) * 100.0 / float64(nCnt+staleCnt)
				}
				resp.HighConfRatio = float64(int(idxRatio*10+0.5)) / 10
				resp.AnchorFullRatio = float64(int(addrRatio*10+0.5)) / 10
				composite := 0.45*idxRatio + 0.35*addrRatio + 0.20*freshRatio
				resp.HealthScore = float64(int(composite*10+0.5)) / 10
				if resp.HealthScore > 100 {
					resp.HealthScore = 100
				}
			}
		}
		// 五维能力雷达：统一采用剑桥大学 ATM-Bench 五维指标体系（Official Recall@10、List Recall、Number EM、Open-End、Hybrid QS），
		// 同时下发 Main-1013 全量主榜与 Hard-31 高难榜两套读数（我方 Rolling-Memory local_plain 对比 剑桥论文官方基准 ATM-RAG）。
		populateATMDashboardRadar(&resp)

		// 日桶只覆盖 30 天窗口（下方 dayKeys30 同口径）：加时间下界后不再对全库历史做 GROUP BY，
		// 否则实时控制台每一帧（活跃期 ≤250ms 一帧）都会重扫整份三层记忆。
		// 时间列优先取首次入库时间 inserted_at（老表或 0 值平滑回退 occurred_at），消除修改旧文件把存量分块刷进近1小时的虚增。
		bgTsExpr := vaultIngestTimeExpr(dbPath, "rm_bg_chunks")
		docTsExpr := vaultIngestTimeExpr(dbPath, "rm_units")
		ntTsExpr := vaultIngestTimeExpr(dbPath, "rm_noetic")
		qVaultDaily := fmt.Sprintf(`SELECT date(ts, 'unixepoch', 'localtime') AS d,
				SUM(CASE WHEN layer='background' THEN 1 ELSE 0 END) AS bg_cnt,
				SUM(CASE WHEN layer='episodic' THEN 1 ELSE 0 END) AS ep_cnt,
				SUM(CASE WHEN layer='noetic' THEN 1 ELSE 0 END) AS nt_cnt
				FROM (
					SELECT 'background' AS layer, %s AS ts FROM rm_bg_chunks
					UNION ALL SELECT CASE WHEN COALESCE(layer, 'episodic')='background' THEN 'background' ELSE 'episodic' END AS layer, %s AS ts FROM rm_units
					UNION ALL SELECT 'noetic' AS layer, %s AS ts FROM rm_noetic WHERE status = 'active'
				) WHERE ts >= %d
				GROUP BY d ORDER BY d DESC LIMIT 60;`, bgTsExpr, docTsExpr, ntTsExpr, start30DUnix)
		if rows, err := sqliteQueryJSON(dbPath, qVaultDaily); err == nil {
			for _, r := range rows {
				d := asString(r["d"])
				if d != "" {
					dayBgMap[d] += asInt(r["bg_cnt"])
					dayEpMap[d] += asInt(r["ep_cnt"])
					dayNtMap[d] += asInt(r["nt_cnt"])
				}
			}
		}

		qVaultRecent24H := fmt.Sprintf(`SELECT layer, ts FROM (
				SELECT 'background' AS layer, %s AS ts FROM rm_bg_chunks WHERE %s >= %d
				UNION ALL SELECT CASE WHEN COALESCE(layer, 'episodic')='background' THEN 'background' ELSE 'episodic' END AS layer, %s AS ts FROM rm_units WHERE %s >= %d
				UNION ALL SELECT 'noetic' AS layer, %s AS ts FROM rm_noetic WHERE %s >= %d AND status = 'active'
			);`, bgTsExpr, bgTsExpr, start1DUnix, docTsExpr, docTsExpr, start1DUnix, ntTsExpr, ntTsExpr, start1DUnix)
		if rows, err := sqliteQueryJSON(dbPath, qVaultRecent24H); err == nil {
			for _, r := range rows {
				ts := int64(asInt(r["ts"]))
				layer := asString(r["layer"])
				if ts >= start1DUnix {
					hk := time.Unix(ts, 0).In(loc).Format("2006-01-02 15")
					switch layer {
					case "background":
						hourBgMap[hk]++
					case "noetic":
						hourNtMap[hk]++
					default:
						hourEpMap[hk]++
					}
				}
				if b1h := bucket1HIndex(ts); b1h >= 0 {
					switch layer {
					case "background":
						l1_1H[b1h]++
					case "noetic":
						l3_1H[b1h]++
					default:
						l2_1H[b1h]++
					}
				}
			}
		}

		qRecent := `SELECT id, txt, addr FROM (
			SELECT * FROM (SELECT doc_id AS id, raw_text AS txt, address_uri AS addr, occurred_at FROM rm_units ORDER BY occurred_at DESC LIMIT 8)
			UNION ALL
			SELECT * FROM (SELECT noetic_id AS id, 'Q: ' || query_text || ' | A: ' || answer_text AS txt, COALESCE(NULLIF(address_uri, ''), noetic_id) AS addr, occurred_at FROM rm_noetic WHERE status = 'active' ORDER BY occurred_at DESC LIMIT 8)
		) ORDER BY occurred_at DESC LIMIT 8;`
		if rows, err := sqliteQueryJSON(dbPath, qRecent); err == nil {
			for _, r := range rows {
				resp.RecentAssertions = append(resp.RecentAssertions, RecentAssertion{
					ID:         asString(r["id"]),
					Rendered:   asString(r["txt"]),
					Confidence: 1.0,
					Anchor:     asString(r["addr"]),
					Status:     "active",
				})
			}
		}
	}

	// 趋势组装：L1 Background、L2 Episodic、L3 Noetic 认知结晶与 Recall 检索全量呈现。
	// 召回腿以侧车事实账本为真源（MCP 直调 + 宿主 transcript 回填按桶去重）：
	// 现行架构下 SessionStart / Stop 钩子已停用，UserPromptSubmit 仅做 4 轮节律提醒而不执行自动检索，
	// 因此当事实账本存在真实 MCP recall 记录时，KPI 的「召回触发 / 有效命中」、趋势图与实时监控流
	// 100% 采用 MCP recall 真实读数，彻底消除历史钩子日志（start/close 及节律空跑）对命中率的分母稀释；
	// 仅在账本尚无记录的回退场景（如单测夹具）下使用钩子日志兜底。
	recallLedger := loadRecallLedgerStat(dbPath, scope, start30DUnix)
	hasMCPLedger := recallLedger.totalMCPRecalls > 0
	if hasMCPLedger {
		resp.TotalHookEvents = recallLedger.totalMCPRecalls
		resp.InjectedEvents = recallLedger.hitMCPRecalls
		if resp.TotalHookEvents > 0 {
			rate := float64(resp.InjectedEvents) * 100.0 / float64(resp.TotalHookEvents)
			resp.InjectRatio = float64(int(rate*10)) / 10
		}
		resp.RecentEvents = append([]HookEventItem{}, recallLedger.recentEvents...)
	}
	if len(recallLedger.latencies) > 0 {
		latencies := recallLedger.latencies
		last, avg, p50, max := recallLatencyStats(latencies)
		resp.LastRecallMS = last
		resp.AvgRecallMS = avg
		resp.LatencyP50MS = p50
		resp.LatencyMaxMS = max
		resp.LatencySamples = len(latencies)
		resp.LatencyTrend = recallLatencyTrend(p50, avg)
		sparkStart := len(latencies) - 12
		if sparkStart >= 0 {
			resp.LatencySpark = append([]int{}, latencies[sparkStart:]...)
		} else {
			padCount := 12 - len(latencies)
			sp := make([]int, 0, 12)
			for i := 0; i < padCount; i++ {
				sp = append(sp, resp.AvgRecallMS)
			}
			sp = append(sp, latencies...)
			resp.LatencySpark = sp
		}
	}
	assert1H := make([]int, buckets1H)
	act1H := make([]int, buckets1H)
	recall1H := make([]int, buckets1H)
	for i, slot := range slotStarts {
		assert1H[i] = l1_1H[i] + l2_1H[i] + l3_1H[i]
		if hasMCPLedger {
			mcpSlot := recallLedger.slotCounts(slot).merged()
			recall1H[i] = mcpSlot
			resp.HookSpark[i] = mcpSlot
		} else {
			recall1H[i] = logStat.recallSlot(scope, slot)
			resp.HookSpark[i] = logStat.hookSlot(slot)
		}
		act1H[i] = assert1H[i] + recall1H[i]
	}
	l1_24 := make([]int, buckets1D)
	l2_24 := make([]int, buckets1D)
	l3_24 := make([]int, buckets1D)
	assert24 := make([]int, buckets1D)
	act24 := make([]int, buckets1D)
	recall24 := make([]int, buckets1D)
	for i, hk := range hourKeys24 {
		l1_24[i] = hourBgMap[hk]
		l2_24[i] = hourEpMap[hk]
		l3_24[i] = hourNtMap[hk]
		assert24[i] = l1_24[i] + l2_24[i] + l3_24[i]
		if hasMCPLedger {
			recall24[i] = recallLedger.hourCounts(hk).merged()
		} else {
			recall24[i] = logStat.recallHour(scope, hk)
		}
		act24[i] = assert24[i] + recall24[i]
	}
	l1_30 := make([]int, buckets30D)
	l2_30 := make([]int, buckets30D)
	l3_30 := make([]int, buckets30D)
	assert30 := make([]int, buckets30D)
	act30 := make([]int, buckets30D)
	recall30 := make([]int, buckets30D)
	for i, k := range dayKeys30 {
		l1_30[i] = dayBgMap[k]
		l2_30[i] = dayEpMap[k]
		l3_30[i] = dayNtMap[k]
		assert30[i] = l1_30[i] + l2_30[i] + l3_30[i]
		if hasMCPLedger {
			recall30[i] = recallLedger.dayCounts(k).merged()
		} else {
			recall30[i] = logStat.recallDay(scope, k)
		}
		act30[i] = assert30[i] + recall30[i]
	}

	resp.Trend1H = DashboardTrendSeries{
		Dates:      labels1H,
		Assertions: assert1H,
		Activity:   act1H,
		Recalls:    recall1H,
		Traces:     l2_1H,
		L1Added:    l1_1H,
		L2Added:    l2_1H,
		L3Added:    l3_1H,
	}
	resp.Trend1D = DashboardTrendSeries{
		Dates:      labels1D,
		Assertions: assert24,
		Activity:   act24,
		Recalls:    recall24,
		Traces:     l2_24,
		L1Added:    l1_24,
		L2Added:    l2_24,
		L3Added:    l3_24,
	}
	resp.Trend30D = DashboardTrendSeries{
		Dates:      dayLabels30,
		Assertions: assert30,
		Activity:   act30,
		Recalls:    recall30,
		Traces:     l2_30,
		L1Added:    l1_30,
		L2Added:    l2_30,
		L3Added:    l3_30,
	}
	resp.Trend7D = DashboardTrendSeries{
		Dates:      dayLabels30[23:],
		Assertions: assert30[23:],
		Activity:   act30[23:],
		Recalls:    recall30[23:],
		Traces:     l2_30[23:],
		L1Added:    l1_30[23:],
		L2Added:    l2_30[23:],
		L3Added:    l3_30[23:],
	}

	runningAssert := max(resp.ActiveAssertions-sumInts(assert30[18:]), 1)
	runningTrace := max(resp.UserTraces-sumInts(l2_30[18:]), 1)
	for i := 0; i < 12; i++ {
		runningAssert += assert30[18+i]
		runningTrace += l2_30[18+i]
		resp.AssertionSpark[i] = runningAssert
		resp.NodeSpark[i] = runningTrace
	}

	resp.Efficacy = collectEfficacySnapshot(home, dbPath, scope, "", 20, false)

	return resp
}

type efficacyKey struct {
	home, dbPath, scope, harness string
	limit                        int
}

type efficacyCacheEntry struct {
	data       map[string]any
	at         time.Time
	retryAt    time.Time
	lastAccess time.Time
	err        string
	running    bool
}

var (
	efficacyCacheMu      sync.Mutex
	efficacyCache        = map[efficacyKey]*efficacyCacheEntry{}
	efficacyCacheVersion uint64
	efficacyRunning      int
)

const (
	efficacyRefreshInterval = 30 * time.Second
	efficacyRetryInterval   = 30 * time.Second
	// efficacySettleBudget is the soft per-run settling budget handed to the engine
	// (`efficacy --budget-seconds`). The engine finalizes and persists whatever is already
	// settled when the budget expires, so a slow corpus can never leave the ROI screen pinned
	// to an older snapshot: every run advances the persisted ledger.
	efficacySettleBudget = 4 * time.Minute
	// efficacyCommandTimeout is the hard kill switch. It stays strictly above the soft budget
	// so the engine always self-finalizes first; a run that reaches it is a genuine fault.
	efficacyCommandTimeout = 8 * time.Minute
	efficacyMaxConcurrent  = 2
	efficacyMaxEntries     = 128
)

// EfficacyQueryRequest 跨 Harness 召回效能评估请求。
type EfficacyQueryRequest struct {
	Scope   string `json:"scope"`
	Harness string `json:"harness"`
	Limit   int    `json:"limit"`
	Refresh bool   `json:"refresh"`
}

func efficacyHandler(c *gin.Context) {
	var req EfficacyQueryRequest
	if !bindJSON(c, &req) {
		return
	}
	home, _ := os.UserHomeDir()
	dbPath := resolveMemoryDBPath(home)
	scope := strings.TrimSpace(req.Scope)
	if scope == "" {
		scope = UserScope
	}
	limit := req.Limit
	if limit <= 0 {
		limit = 20
	}
	data := collectEfficacySnapshot(home, dbPath, scope, strings.TrimSpace(req.Harness), limit, req.Refresh)
	if data != nil {
		data["available_scopes"] = queryAvailableEfficacyScopes(dbPath, scope)
	}
	httpx.Success(c, httpx.WithData(data))
}

func queryAvailableEfficacyScopes(_ string, currentScope string) []string {
	seen := map[string]bool{UserScope: true}
	out := []string{UserScope}
	if currentScope != "" && currentScope != LegacyUserScope && !seen[currentScope] {
		seen[currentScope] = true
		out = append(out, currentScope)
	}
	return out
}

// collectEfficacySnapshot 只读取缓存；仅显式请求重评时启动后台评估。
func collectEfficacySnapshot(home, dbPath, scope, harnessFilter string, limit int, forceRefresh bool) map[string]any {
	key := efficacyKey{home: home, dbPath: dbPath, scope: scope, harness: harnessFilter, limit: limit}
	effScope := scope
	if effScope == "ALL" || effScope == "ALL_WITH_EVAL" {
		effScope = UserScope
	}
	// The per-turn settlement engine reads tables the canonical era no longer
	// stores. Report that honestly instead of replaying a stale settlement cache,
	// and attach live local-vault engineering ROI metrics for the dashboard cards.
	if vaultDroppedLegacyAnalytics(dbPath) {
		return efficacyUnavailableSnapshot(dbPath, effScope, "legacy_analytics_schema_absent")
	}

	efficacyCacheMu.Lock()
	defer efficacyCacheMu.Unlock()
	now := time.Now()
	entry := efficacyCache[key]
	if entry == nil {
		// 容量不足时不排队、不驱逐仍处于退避期或活跃期的条目。
		if efficacyRunning >= efficacyMaxConcurrent {
			result := efficacyZeroSnapshot(effScope)
			result["status"] = "pending"
			return result
		}
		if len(efficacyCache) >= efficacyMaxEntries {
			var oldestKey efficacyKey
			var oldest *efficacyCacheEntry
			for k, candidate := range efficacyCache {
				if !candidate.running && !now.Before(candidate.retryAt) && now.Sub(candidate.lastAccess) >= efficacyRefreshInterval && (oldest == nil || candidate.lastAccess.Before(oldest.lastAccess)) {
					oldestKey, oldest = k, candidate
				}
			}
			if oldest == nil {
				result := efficacyZeroSnapshot(effScope)
				result["status"] = "pending"
				return result
			}
			delete(efficacyCache, oldestKey)
		}
		entry = &efficacyCacheEntry{}
		efficacyCache[key] = entry
	}
	entry.lastAccess = now
	fresh := entry.data != nil && now.Sub(entry.at) < efficacyRefreshInterval
	hasDBUpdate := false
	if !forceRefresh && (!fresh || entry.data == nil) {
		if persisted, ok, upToDate := tryLoadPersistedROILedger(home, dbPath, effScope, harnessFilter, limit); ok {
			entry.data = persisted
			if upToDate {
				entry.at = now
				fresh = true
			} else {
				hasDBUpdate = true
			}
		} else if computeAdminDBFingerprint(dbPath, effScope) != "" {
			hasDBUpdate = true
		}
	}
	if !entry.running && !now.Before(entry.retryAt) && (forceRefresh || hasDBUpdate) && efficacyRunning < efficacyMaxConcurrent {
		entry.running = true
		efficacyRunning++
		efficacyCacheVersion++
		go refreshEfficacySnapshot(key, effScope)
	}

	var result map[string]any
	if entry.data == nil {
		result = efficacyZeroSnapshot(effScope)
		result["status"] = "pending"
		if entry.err != "" {
			result["status"] = "error"
		}
	} else {
		result = make(map[string]any, len(entry.data)+2)
		for k, v := range entry.data {
			result[k] = v
		}
		result["status"] = "ready"
		if !fresh || entry.running || entry.err != "" {
			result["status"] = "stale"
		}
	}
	if entry.err != "" {
		result["error"] = entry.err + "; retry later"
		if remaining := entry.retryAt.Sub(now); remaining > 0 {
			result["retry_after_seconds"] = int((remaining + time.Second - 1) / time.Second)
		}
	} else {
		delete(result, "error")
		delete(result, "retry_after_seconds")
	}
	return result
}

// refreshEfficacySnapshot 仅更新同一键的快照；失败保留该键的旧值并退避。
func refreshEfficacySnapshot(key efficacyKey, scope string) {
	parsed, errText := runEfficacySnapshot(key, scope, efficacyCommandTimeout)
	efficacyCacheMu.Lock()
	defer efficacyCacheMu.Unlock()
	entry := efficacyCache[key]
	entry.running = false
	efficacyRunning--
	if errText != "" {
		entry.err = errText
		entry.retryAt = time.Now().Add(efficacyRetryInterval)
	} else {
		entry.data = parsed
		entry.at = time.Now()
		entry.err = ""
		entry.retryAt = time.Time{}
	}
	efficacyCacheVersion++
}

func runEfficacySnapshot(key efficacyKey, scope string, timeout time.Duration) (map[string]any, string) {
	bin := resolveBinary(key.home)
	if bin == "" {
		return nil, "efficacy engine unavailable"
	}
	cliScope := resolveBoundVaultScope(key.dbPath, scope)
	args := []string{"efficacy", "--db", key.dbPath, "--scope", cliScope, "--limit", strconv.Itoa(key.limit), "--json",
		"--budget-seconds", strconv.Itoa(int(efficacySettleBudget / time.Second))}
	if key.harness != "" {
		args = append(args, "--harness", key.harness)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	// 子进程零云端覆盖变量：不注入任何远端端点/凭据（疗效快照只统计本地明文金库）。
	cmd.Env = stripCloudBrainEnv(os.Environ())
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return nil, "efficacy evaluation timed out"
	}
	if err != nil {
		return nil, "efficacy evaluation failed"
	}
	var parsed map[string]any
	if json.Unmarshal(out, &parsed) != nil || parsed == nil {
		return nil, "efficacy evaluation returned invalid data"
	}
	evaluatedAt, ok := parsed["evaluated_at"].(string)
	if !ok {
		return nil, "efficacy evaluation returned invalid data"
	}
	if _, err := time.Parse("2006-01-02 15:04:05", evaluatedAt); err != nil {
		return nil, "efficacy evaluation returned invalid data"
	}
	return parsed, ""
}

// tryLoadPersistedROILedger reads the persistent ROI ledger snapshot from disk (<1ms)
// and verifies whether the memory DB watermark has advanced since the snapshot was written.
func tryLoadPersistedROILedger(home, dbPath, scope, harnessFilter string, limit int) (map[string]any, bool, bool) {
	if strings.TrimSpace(home) == "" || strings.TrimSpace(harnessFilter) != "" {
		return nil, false, false
	}
	expectedFP := computeAdminDBFingerprint(dbPath, scope)
	if expectedFP == "" {
		return nil, false, false
	}
	var slugBuilder strings.Builder
	for _, r := range strings.TrimSpace(scope) {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			slugBuilder.WriteRune(r)
		} else {
			slugBuilder.WriteByte('_')
		}
	}
	slug := strings.Trim(slugBuilder.String(), "_")
	if slug == "" || scope == UserScope || scope == LegacyUserScope {
		slug = "default"
	}
	ledgerPath := filepath.Join(home, ".local", "state", "rolling-memory", "roi_ledger_v1_"+slug+".json")
	raw, err := os.ReadFile(ledgerPath)
	if (err != nil || len(raw) == 0) && slug == "default" {
		legacyPath := filepath.Join(home, ".local", "state", "rolling-memory", "roi_ledger_v1_rm_self.json")
		if lRaw, lErr := os.ReadFile(legacyPath); lErr == nil && len(lRaw) > 0 {
			raw, err = lRaw, nil
		}
	}
	if err != nil || len(raw) == 0 {
		return nil, false, false
	}
	var snap struct {
		Version       int    `json:"version"`
		Scope         string `json:"scope"`
		DBFingerprint string `json:"db_fingerprint"`
		UpdatedUnix   int64  `json:"updated_unix"`
		Watermark     struct {
			TraceCount     int   `json:"trace_count"`
			MaxTraceAt     int64 `json:"max_trace_at"`
			AssertionCount int   `json:"assertion_count"`
			MaxAssertionAt int64 `json:"max_assertion_at"`
			Settled        bool  `json:"settled"`
		} `json:"watermark"`
		FullSessions []any          `json:"full_sessions"`
		Report       map[string]any `json:"report"`
	}
	if json.Unmarshal(raw, &snap) != nil || snap.Version < 2 || snap.Scope != scope || snap.DBFingerprint != expectedFP || snap.Report == nil {
		return nil, false, false
	}
	evaluatedAt, ok := snap.Report["evaluated_at"].(string)
	if !ok || strings.TrimSpace(evaluatedAt) == "" {
		return nil, false, false
	}
	out := make(map[string]any, len(snap.Report)+2)
	for k, v := range snap.Report {
		out[k] = v
	}
	sessions := snap.FullSessions
	if len(sessions) == 0 {
		if repSess, ok := snap.Report["sessions"].([]any); ok {
			sessions = repSess
		}
	}
	if limit > 0 && len(sessions) > limit {
		out["sessions"] = append([]any(nil), sessions[:limit]...)
	} else {
		out["sessions"] = append([]any(nil), sessions...)
	}

	isPartial, _ := snap.Report["partial"].(bool)
	deferredTurns := asInt(snap.Report["deferred_turns"])
	nowUnix := time.Now().Unix()
	windowFresh := snap.UpdatedUnix > 0 && (nowUnix-snap.UpdatedUnix < 120)

	upToDate := false
	todayStr := time.Now().Format("2006-01-02")
	if !isPartial && deferredTurns == 0 && windowFresh && snap.Watermark.Settled && strings.HasPrefix(strings.TrimSpace(evaluatedAt), todayStr) {
		upToDate = true
	}
	var tvDocs, tvCells, tvNoetic int
	if tvRows, err := sqliteQueryJSON(dbPath, `
			SELECT
				COUNT(*) AS docs_cnt,
				COUNT(DISTINCT CASE WHEN address_uri != '' THEN substr(address_uri, 1, instr(address_uri || '#', '#') - 1) END) AS cells_cnt
			FROM rm_units;
		`); err == nil && len(tvRows) > 0 {
		tvDocs = asInt(tvRows[0]["docs_cnt"])
		tvCells = asInt(tvRows[0]["cells_cnt"])
	}
	if tnRows, err := sqliteQueryJSON(dbPath, `SELECT COUNT(*) AS noetic_cnt FROM rm_noetic WHERE status = 'active';`); err == nil && len(tnRows) > 0 {
		tvNoetic = asInt(tnRows[0]["noetic_cnt"])
	}
	out["vault_docs"] = tvDocs
	out["vault_cells"] = tvCells
	out["vault_noetic"] = tvNoetic
	out["upstream_engine"] = "Rolling Autonoetic Memory Engine (the memory engine)"
	// 读数如实标注来源：控制台恒报本地明文金库坐标（local://vault），
	// 绝不把云端地址 / 用户 ID 当成本地读数呈现。
	out["upstream_user_id"] = ""
	out["upstream_endpoint"] = "local://vault"
	return out, true, upToDate
}

// efficacyUnavailableSnapshot 数据源不适用时的明示快照：不复用任何历史结算缓存，同时附带规范语料实时工程 ROI。
func efficacyUnavailableSnapshot(dbPath, scope, reason string) map[string]any {
	result := efficacyZeroSnapshot(scope)
	result["status"] = "unavailable"
	result["reason"] = reason
	// 面向用户的产品口径：说明「为什么没有逐轮结算读数」以及「去哪里看真实读数」，
	// 不暴露任何内部实现术语（零拷贝 / 情景指针 / 干句提纯等）。
	result["error"] = "本机记忆库按本地明文存储与检索，逐轮 Token 结算不再适用；记忆规模、引证复用与交付字节请查看「记忆事实账本」。"
	if home, err := os.UserHomeDir(); err == nil {
		// 宿主库为外部在读写的 GB 级实时库，导入走后台单飞，读接口不做外部库扫描。
		triggerProductionRecallLedgerSync(home, dbPath)
	}
	if bundle := readROIFactsVaultBundle(dbPath); bundle.vault != nil {
		result["vault_roi"] = bundle.vault
		// 事后证据复用审计的窗口明细（1h/24h/7d/30d/all）随附到首页 KPI：产品「有效命中」仅代表
		// 证据已送达，证据复用率仅表示后续工具输入/回复与交付锚点匹配（匹配 ≠ 省下一步），两口径在同一张卡上对齐。
		if len(bundle.vaultWindows) > 0 {
			windows := make([]map[string]any, 0, len(bundle.vaultWindows))
			for _, w := range bundle.vaultWindows {
				windows = append(windows, map[string]any{
					"key":                   w.Key,
					"audited_recalls":       w.AuditedRecalls,
					"adopted_recalls":       w.AdoptedRecalls,
					"adopted_tool_count":    w.AdoptedToolCount,
					"adopted_context_count": w.AdoptedContextCount,
					"adoption_rate_pct":     w.AdoptionRatePct,
				})
			}
			result["adoption_windows"] = windows
		}
	}
	return result
}

// efficacyZeroSnapshot 尚无读数时的零值快照（真实读数到位前不冒充数据）。
func efficacyZeroSnapshot(scope string) map[string]any {
	return map[string]any{
		"scope":                     scope,
		"evaluated_at":              "",
		"total_hook_runs":           0,
		"total_sessions":            0,
		"evaluated_turns":           0,
		"injected_count":            0,
		"silenced_count":            0,
		"adopted_count":             0,
		"code_coord_hits":           0,
		"novel_coord_hits":          0,
		"causal_direct_hits":        0,
		"echo_excluded_count":       0,
		"self_healed_count":         0,
		"writeback_count":           0,
		"adopt_rate":                0.0,
		"silence_rate":              0.0,
		"avg_round_cost_tokens":     0,
		"avg_round_fresh_tokens":    0,
		"avg_round_cached_tokens":   0,
		"avg_saved_rounds":          0.0,
		"total_saved_rounds":        0,
		"injected_tokens":           0,
		"saved_fresh_tokens":        0,
		"saved_cached_tokens":       0,
		"gross_saved_tokens":        0,
		"net_saved_tokens":          0,
		"net_token_roi":             0.0,
		"today_turns":               0,
		"today_injected_count":      0,
		"today_adopted_count":       0,
		"today_saved_rounds":        0,
		"today_injected_tokens":     0,
		"today_saved_fresh_tokens":  0,
		"today_saved_cached_tokens": 0,
		"today_gross_saved_tokens":  0,
		"today_net_saved_tokens":    0,
		"today_net_token_roi":       0.0,
		"week_saved_rounds":         0,
		"week_net_saved_tokens":     0,
		"windows":                   []any{},
		"hourly_savings":            []any{},
		"partial":                   false,
		"deferred_turns":            0,
		"daily_savings":             []any{},
		"avg_latency_ms":            0,
		"efficacy_score":            0.0,
		"grade":                     "-",
		"funnel": map[string]any{
			"high_value_adopted": 0,
			"context_assisted":   0,
			"dedup_suppressed":   0,
			"silence_filtered":   0,
			"noise_injected":     0,
		},
		"harnesses": []any{},
		"sessions":  []any{},
	}
}

// atmEntryRadarValues 将剑桥 ATM-Bench 榜单条目映射为五维能力雷达读数（0–100）：
//
//	[0] Official Recall@10 (GT Coverage Mean)
//	[1] Open-End QA (Cat3)
//	[2] List Recall Jaccard (Cat1)
//	[3] Number Exact Match (Cat2)
//	[4] End-to-End Hybrid QS
func atmEntryRadarValues(e ATMBenchEntry) []float64 {
	round1 := func(v float64) float64 { return float64(int(v*10+0.5)) / 10 }
	return []float64{
		round1(e.RecallAt10),
		round1(e.OpenEnd),
		round1(e.ListRecall),
		round1(e.NumberEM),
		round1(e.QSScore),
	}
}

// populateATMDashboardRadar 为实时控制台组装 ATM-Bench Main-1013 与 Hard-31 双视图五维雷达读数：
//   - 我方读数取 buildATMBenchLeaderboard() 中 IsOurs=true 的权威 local_plain 条目；若测评任务台账存在
//     同 tier（1013 / hard）且已完成端到端判官计分（JudgeScored > 0）的最新运行，则叠用最新运行的分项读数；
//   - 对照基准固定取剑桥论文官方基准「ATM-RAG (剑桥论文官方基线)」同 tier 条目（不取不同架构/不可比的外部商业榜最高行）。
func populateATMDashboardRadar(resp *DashboardResponse) {
	if resp == nil {
		return
	}
	board := buildATMBenchLeaderboard()
	findOurs := func(entries []ATMBenchEntry) (ATMBenchEntry, bool) {
		for _, e := range entries {
			if e.IsOurs {
				return e, true
			}
		}
		return ATMBenchEntry{}, false
	}
	findOfficialBaseline := func(entries []ATMBenchEntry) (ATMBenchEntry, bool) {
		for _, e := range entries {
			if strings.Contains(e.System, "ATM-RAG") {
				return e, true
			}
		}
		for _, e := range entries {
			if !e.IsOurs && e.Category != "Oracle" && e.Rank != 99 {
				return e, true
			}
		}
		return ATMBenchEntry{}, false
	}

	mainOurs, okMainOurs := findOurs(board.MainEntries)
	mainBase, okMainBase := findOfficialBaseline(board.MainEntries)
	hardOurs, okHardOurs := findOurs(board.HardEntries)
	hardBase, okHardBase := findOfficialBaseline(board.HardEntries)

	// 若用户在测评任务台账跑过最新同 tier 的 ATM 端到端评测（JudgeScored > 0），优先映射最新分项得分。
	for _, r := range loadRunHistoryBySuite(SuiteATM) {
		if r.Status != evalTaskStatusCompleted || r.JudgeScored <= 0 || !evalMetricAvailable(&r, "judge_rate") {
			continue
		}
		if (r.Tier != "1013" || r.ScoredQuestions != 1013) && (r.Tier != "hard" || r.ScoredQuestions != 31) {
			continue
		}
		switch r.Tier {
		case "1013":
			if okMainOurs && mainOurs.IndexTimeHr != "latest-run" {
				mainOurs.OpenEnd = r.CategoryJudge.Cat3 * 100
				mainOurs.ListRecall = r.CategoryJudge.Cat1 * 100
				mainOurs.NumberEM = r.CategoryJudge.Cat2 * 100
				mainOurs.QSScore = r.JudgeRate * 100
				mainOurs.IndexTimeHr = "latest-run"
			}
		case "hard":
			if okHardOurs && hardOurs.IndexTimeHr != "latest-run" {
				hardOurs.OpenEnd = r.CategoryJudge.Cat3 * 100
				hardOurs.ListRecall = r.CategoryJudge.Cat1 * 100
				hardOurs.NumberEM = r.CategoryJudge.Cat2 * 100
				hardOurs.QSScore = r.JudgeRate * 100
				hardOurs.IndexTimeHr = "latest-run"
			}
		}
	}

	if okMainOurs && okMainBase {
		resp.RadarCurrent = atmEntryRadarValues(mainOurs)
		resp.RadarBaseline = atmEntryRadarValues(mainBase)
		resp.RadarCurrentLabel = "ATM Main-1013 · Rolling-Memory (local_plain · 0-Vector/0-LLM)"
		resp.RadarCurrentLabelEn = "ATM Main-1013 · Rolling-Memory (local_plain · 0-Vector/0-LLM)"
		resp.RadarBaselineLabel = "ATM Main-1013 · " + mainBase.System
		resp.RadarBaselineLabelEn = "ATM Main-1013 · " + mainBase.SystemEn
		resp.RadarAvailable = true
	}
	if okHardOurs && okHardBase {
		resp.RadarHardCurrent = atmEntryRadarValues(hardOurs)
		resp.RadarHardBaseline = atmEntryRadarValues(hardBase)
		resp.RadarHardCurrentLabel = "ATM Hard-31 · Rolling-Memory (local_plain · 0-Vector/0-LLM)"
		resp.RadarHardCurrentLabelEn = "ATM Hard-31 · Rolling-Memory (local_plain · 0-Vector/0-LLM)"
		resp.RadarHardBaselineLabel = "ATM Hard-31 · " + hardBase.System
		resp.RadarHardBaselineLabelEn = "ATM Hard-31 · " + hardBase.SystemEn
		resp.RadarAvailable = true
	}
}

// evalRadarValues 把一次评测记录映射成五维能力雷达读数（0-100）：
//   - 当评测开启了 Judge 判官（JudgeScored > 0）时：单跳 / 多跳 / 时序取 CategoryJudge，综合分取 JudgeRate；
//   - 当评测为 0-LLM 纯检索模式（JudgeScored == 0）时：单跳 / 多跳 / 时序自动取 CategoryEvidence，综合分取 AnswerInContext（或 EvidenceHit），
//     避免 0-LLM 检索运行后雷达图后四维塌缩为 0%。
//   - 若记录来自 ATM-Bench（题型编号为 Cat1=list_recall, Cat2=number, Cat3=open_end，Cat4 未用为 0），
//     第 2 维回退取 Cat3（open_end），避免雷达图单轴因套件编号差异塌缩为 0%。
func evalRadarValues(rec EvalRunRecord) []float64 {
	scale := func(v float64) float64 { return float64(int(v*1000+0.5)) / 10 }
	if rec.JudgeScored > 0 || rec.JudgeRate > 0 {
		c4 := rec.CategoryJudge.Cat4
		if c4 <= 0 && rec.Suite == SuiteATM && rec.CategoryJudge.Cat3 > 0 {
			c4 = rec.CategoryJudge.Cat3
		}
		return []float64{
			scale(rec.EvidenceHit),
			scale(c4),
			scale(rec.CategoryJudge.Cat1),
			scale(rec.CategoryJudge.Cat2),
			scale(rec.JudgeRate),
		}
	}
	overall := rec.AnswerInContext
	if overall <= 0 {
		overall = rec.EvidenceHit
	}
	c4 := rec.CategoryEvidence.Cat4
	if c4 <= 0 && rec.Suite == SuiteATM && rec.CategoryEvidence.Cat3 > 0 {
		c4 = rec.CategoryEvidence.Cat3
	}
	return []float64{
		scale(rec.EvidenceHit),
		scale(c4),
		scale(rec.CategoryEvidence.Cat1),
		scale(rec.CategoryEvidence.Cat2),
		scale(overall),
	}
}

func sumInts(arr []int) int {
	s := 0
	for _, v := range arr {
		s += v
	}
	return s
}

// ==================== 记忆图谱（Graph 子图 + 邻域展开） ====================

// GraphQueryRequest 记忆图谱子图请求。
type GraphQueryRequest struct {
	Scope        string             `json:"scope"`
	Limit        int                `json:"limit"`
	Keyword      string             `json:"keyword"`
	Layers       []string           `json:"layers,omitempty"`        // 来源层过滤
	ClusterTypes []string           `json:"cluster_types,omitempty"` // 簇类型过滤：episode | entity | document
	LayerWeights map[string]float64 `json:"layer_weights,omitempty"` // 层加权乘子
}

// GraphExpandRequest 记忆图谱邻域展开请求。
type GraphExpandRequest struct {
	Scope    string   `json:"scope"`
	SeedKeys []string `json:"seed_keys"`
	Depth    int      `json:"depth"`
	Limit    int      `json:"limit"`
}

// GraphScopeItem 记忆范围统计项（对齐 Rolling 记忆图谱）。
type GraphScopeItem struct {
	Source    string `json:"source"`
	ScopeKey  string `json:"scope_key"`
	Events    int    `json:"events"`
	Links     int    `json:"links"`
	Materials int    `json:"materials"`
}

// GraphAtomParticle 图谱节点或拓扑中的一级原子粒子（Atom Node · 守恒荷/词根脉冲）。
type GraphAtomParticle struct {
	Token     string  `json:"token"`              // 可读词元表面形式（由端侧本地明文金库零损还原）
	HashHex   string  `json:"hash_hex,omitempty"` // 64-bit 规范粒子承诺短指纹
	DF        int     `json:"df"`                 // 全图文档频次
	MassNats  float64 `json:"mass_nats"`          // 内生信息静质量 m(h) = ln(1 + N/df(h)) (Nats)
	Potential float64 `json:"potential"`          // 动态激发电位 A(h, Epoch)
	Weight    float64 `json:"weight"`             // 在当前节点/质心中的归一化共振权重
	CellSpan  int     `json:"cell_span"`          // 投射到的记忆地址容器数
	UnitSpan  int     `json:"unit_span"`          // 出现的记忆单元数
}

// GraphMoleculeFragment 图谱节点或拓扑中的二级分子波包（Molecule Node · 命题片段/句波包）。
type GraphMoleculeFragment struct {
	MolKey     string   `json:"mol_key"`               // 规范共价包短标识（如 "M:8f3a21c0"）
	Text       string   `json:"text"`                  // 命题片段明文摘要（由端侧本地明文金库还原）
	DF         int      `json:"df"`                    // 全图分子频次
	MassNats   float64  `json:"mass_nats"`             // 分子内生静质量 m(M) = ln(1 + N/df(M))
	EnergyNats float64  `json:"energy_nats,omitempty"` // 分子内生联合惊奇度能量 E_mol(M)（香农位宽 × 原子逆频次惊奇度 × 多核共价协同）
	Gamma      float64  `json:"gamma"`                 // 容器体积压缩比传导率 γ_mol(M) ∈ [0.12, 1.0]
	CoreAtoms  []string `json:"core_atoms,omitempty"`  // 构成分子共价包的 Top 核心原子词元
	DimerPeers []string `json:"dimer_peers,omitempty"` // 二阶邻接分子突触（DimerSynapses）相连的分子摘要
	UnitSpan   int      `json:"unit_span,omitempty"`   // 承载该分子的单元数
}

// GraphWordParticle 引擎投影的词族粒子（word 节点 / 单元词荷）。
type GraphWordParticle struct {
	Token    string  `json:"token"`
	GroupHex string  `json:"group_hex,omitempty"`
	DF       int     `json:"df"`
	MassNats float64 `json:"mass_nats"`
	Weight   float64 `json:"weight"`
	UnitSpan int     `json:"unit_span,omitempty"`
}

// GraphPhraseFragment 引擎投影的二元短语片段（phrase 节点 / 单元短语荷）。
type GraphPhraseFragment struct {
	Left               string  `json:"left"`
	Right              string  `json:"right"`
	DF                 int     `json:"df"`
	LeftDF             int     `json:"left_df,omitempty"`
	RightDF            int     `json:"right_df,omitempty"`
	Containment        float64 `json:"containment,omitempty"`
	Lift               float64 `json:"lift,omitempty"`
	JointSurprisalNats float64 `json:"joint_surprisal_nats"`
	BindingEnergyNats  float64 `json:"binding_energy_nats"`
	UnitSpan           int     `json:"unit_span,omitempty"`
}

// GraphNodeItem 图谱节点（实体或值节点，内嵌一级原子粒子与二级分子波包投影）。
type GraphNodeItem struct {
	ID                int                     `json:"id"`
	NodeKey           string                  `json:"node_key"`
	Summary           string                  `json:"summary"`
	Kind              string                  `json:"kind"`      // cluster | unit | word | phrase | free_unit（LoCoMo 预览仍为 cell | unit | atom | molecule）
	Layer             string                  `json:"layer"`     // background | episodic | noetic
	Certainty         string                  `json:"certainty"` // high | medium | low
	Intention         string                  `json:"intention"` // referential | experiential | procedural | performative
	Status            string                  `json:"status"`
	Confidence        float64                 `json:"confidence"`
	ObservedAt        string                  `json:"observed_at"`
	ValidFrom         string                  `json:"valid_from"`
	ValidTo           string                  `json:"valid_to"`
	Anchor            string                  `json:"anchor"`
	Span              string                  `json:"span"`
	Aliases           []string                `json:"aliases"`
	Degree            int                     `json:"degree"`
	Weight            float64                 `json:"weight"`
	Label             string                  `json:"label,omitempty"`
	ClusterType       string                  `json:"cluster_type,omitempty"`
	MemberCount       int                     `json:"member_count,omitempty"`
	Free              bool                    `json:"free,omitempty"`
	PrimaryClusterKey string                  `json:"primary_cluster_key,omitempty"`
	EnergyNats        float64                 `json:"energy_nats,omitempty"`
	LayerMix          map[string]int          `json:"layer_mix,omitempty"`
	Words             []GraphWordParticle     `json:"words,omitempty"`
	Phrases           []GraphPhraseFragment   `json:"phrases,omitempty"`
	Atoms             []GraphAtomParticle     `json:"atoms,omitempty"`     // 该节点承载的 Top 核心原子粒子（AtomView）
	Molecules         []GraphMoleculeFragment `json:"molecules,omitempty"` // 该节点承载的 Top 核心分子波包（MoleculeView）
}

// GraphEdgeItem 图谱有向关系边。
type GraphEdgeItem struct {
	ID         int     `json:"id"`
	Assertion  string  `json:"assertion_id"`
	From       int     `json:"from"`
	To         int     `json:"to"`
	FromKey    string  `json:"from_key"`
	ToKey      string  `json:"to_key"`
	FromLabel  string  `json:"from_label"`
	ToLabel    string  `json:"to_label"`
	Relation   string  `json:"relation"`
	Layer      string  `json:"layer,omitempty"` // 来源层分类
	Warrant    string  `json:"warrant"`
	Certainty  string  `json:"certainty"`
	Confidence float64 `json:"confidence"`
	Status     string  `json:"status"`
	ObservedAt string  `json:"observed_at"`
	ValidFrom  string  `json:"valid_from"`
	Anchor     string  `json:"anchor"`
	Span       string  `json:"span"`
	Weight     float64 `json:"weight"`
}

// GraphRelationClass 关系词分类标签。
type GraphRelationClass struct {
	Key     string `json:"key"`
	Display string `json:"display"`
}

// GraphRelationOption 关系下拉与图例项。
type GraphRelationOption struct {
	Relation string               `json:"relation"`
	Count    int                  `json:"count"`
	Classes  []GraphRelationClass `json:"classes"`
}

// GraphKindCounts 节点按结构类型计数（引擎投影）。
type GraphKindCounts struct {
	Units     int `json:"units"`
	Words     int `json:"words"`
	Phrases   int `json:"phrases"`
	Clusters  int `json:"clusters"`
	FreeUnits int `json:"free_units"`
}

// GraphClusterTypeCounts 簇按容器类型计数（引擎投影）。
type GraphClusterTypeCounts struct {
	Episode int `json:"episode"`
	Entity  int `json:"entity"`
	Source  int `json:"source"`
}

// GraphStats 图谱统计栏读数（含 M27/M28/M28b/M28c 连通率指标与五级粒子拓扑统计）。
type GraphStats struct {
	EventTotal           int                     `json:"event_total"`
	LinkTotal            int                     `json:"link_total"`
	NodeShown            int                     `json:"node_shown"`
	EdgeShown            int                     `json:"edge_shown"`
	IsolatedShown        int                     `json:"isolated_shown"`
	AvgEntityDegree      float64                 `json:"avg_entity_degree"`
	MultiEdgeEntityRatio float64                 `json:"multi_edge_entity_ratio"`
	AvgRelationReuse     float64                 `json:"avg_relation_reuse"`
	ObjectNodeRatio      float64                 `json:"object_node_ratio"`
	LayerNodeCounts      map[string]int          `json:"layer_node_counts,omitempty"`
	AtomTotal            int                     `json:"atom_total,omitempty"`          // 全图活跃原子（Atom）总数
	MoleculeTotal        int                     `json:"molecule_total,omitempty"`      // 全图活跃分子波包（Molecule）总数
	DimerSynapseTotal    int                     `json:"dimer_synapse_total,omitempty"` // 全图二阶邻接分子突触总数
	TopAtoms             []GraphAtomParticle     `json:"top_atoms,omitempty"`           // 全图高能枢纽原子榜（按跨地址/电位排序 Top-16）
	TopMolecules         []GraphMoleculeFragment `json:"top_molecules,omitempty"`       // 全图核心分子波包榜（按二聚体突触/质量排序 Top-12）
	WordTotal            int                     `json:"word_total,omitempty"`
	PhraseTotal          int                     `json:"phrase_total,omitempty"`
	ClusterTotal         int                     `json:"cluster_total,omitempty"`
	FreeUnitTotal        int                     `json:"free_unit_total,omitempty"`
	KindCounts           GraphKindCounts         `json:"kind_counts"`
	ClusterTypeCounts    GraphClusterTypeCounts  `json:"cluster_type_counts"`
	BuildMS              int64                   `json:"build_ms,omitempty"`
	LabelCoveragePct     float64                 `json:"label_coverage_pct,omitempty"`
}

// GraphTruncated 图谱截断标志。
type GraphTruncated struct {
	Nodes bool `json:"nodes"`
	Edges bool `json:"edges"`
}

// GraphQueryResponse 记忆图谱完整响应。
type GraphQueryResponse struct {
	GraphSchemaVersion int                   `json:"graph_schema_version,omitempty"`
	Source             string                `json:"source"`
	ScopeKey           string                `json:"scope_key"`
	Scopes             []GraphScopeItem      `json:"scopes"`
	Nodes              []GraphNodeItem       `json:"nodes"`
	Edges              []GraphEdgeItem       `json:"edges"`
	Relations          []GraphRelationOption `json:"relations"`
	Stats              GraphStats            `json:"stats"`
	Truncated          GraphTruncated        `json:"truncated"`
}

func deterministicIntID(key string) int {
	var h uint32 = 2166136261
	for i := 0; i < len(key); i++ {
		h ^= uint32(key[i])
		h *= 16777619
	}
	res := int(h & 0x7fffffff)
	if res == 0 {
		res = 1
	}
	return res
}

func certaintyTier(conf float64) string {
	if conf >= 0.9 {
		return "confirmed (≥0.90)"
	}
	if conf >= 0.75 {
		return "probable (0.75~0.89)"
	}
	return "tentative (<0.75)"
}

func layerRelationCategoryDisplay(layer string) (string, string) {
	switch normalizeAdminMemoryLayer(layer) {
	case "background":
		return "background", "背景记忆层 (L1 Background)"
	case "noetic":
		return "noetic", "知性记忆层 (L3 Noetic)"
	default:
		return "episodic", "情景记忆层 (L2 Episodic)"
	}
}

func graphHandler(c *gin.Context) {
	var req GraphQueryRequest
	if !bindJSON(c, &req) {
		return
	}
	home, _ := os.UserHomeDir()
	dbPath := resolveMemoryDBPath(home)
	if graphSourceMode() == "legacy" {
		if corpus, err := loadLocomoDemoCorpus(); err == nil && corpus != nil {
			httpx.Success(c, httpx.WithData(buildLocomoSubgraph(corpus, req.Limit, req.Keyword, req.Layers)))
			return
		}
		if corpus, err := loadLocomoCorpus(); err == nil && corpus != nil {
			httpx.Success(c, httpx.WithData(buildLocomoSubgraph(corpus, req.Limit, req.Keyword, req.Layers)))
			return
		}
		httpx.Success(c, httpx.WithData(buildMemoryGraphWithLayers(dbPath, req.Scope, req.Limit, req.Keyword, req.Layers, req.LayerWeights)))
		return
	}
	payload := map[string]any{
		"op":            "view",
		"scope":         req.Scope,
		"limit":         req.Limit,
		"keyword":       req.Keyword,
		"layers":        req.Layers,
		"cluster_types": req.ClusterTypes,
		"layer_weights": req.LayerWeights,
	}
	resp, err := engineGraphView(home, dbPath, payload)
	if err != nil {
		triggerConsoleDaemonBootstrap(home, dbPath)
		httpx.Fail(c, httpx.WithMessage("记忆服务未启动或图谱构建失败："+err.Error()))
		return
	}
	httpx.Success(c, httpx.WithData(resp))
}

func graphExpandHandler(c *gin.Context) {
	var req GraphExpandRequest
	if !bindJSON(c, &req) {
		return
	}
	home, _ := os.UserHomeDir()
	dbPath := resolveMemoryDBPath(home)
	if graphSourceMode() == "legacy" {
		if corpus, err := loadLocomoDemoCorpus(); err == nil && corpus != nil {
			httpx.Success(c, httpx.WithData(expandLocomoSubgraph(corpus, req.SeedKeys, req.Depth, req.Limit)))
			return
		}
		if corpus, err := loadLocomoCorpus(); err == nil && corpus != nil {
			httpx.Success(c, httpx.WithData(expandLocomoSubgraph(corpus, req.SeedKeys, req.Depth, req.Limit)))
			return
		}
		httpx.Success(c, httpx.WithData(expandMemoryGraph(dbPath, req.Scope, req.SeedKeys, req.Depth, req.Limit)))
		return
	}
	payload := map[string]any{
		"op":        "expand",
		"scope":     req.Scope,
		"limit":     req.Limit,
		"seed_keys": req.SeedKeys,
		"depth":     req.Depth,
	}
	resp, err := engineGraphView(home, dbPath, payload)
	if err != nil {
		triggerConsoleDaemonBootstrap(home, dbPath)
		httpx.Fail(c, httpx.WithMessage("记忆服务未启动或图谱展开失败："+err.Error()))
		return
	}
	httpx.Success(c, httpx.WithData(resp))
}

// graphSourceMode selects the graph projection source. The engine projection
// is the default; `legacy` keeps the previous SQL/LoCoMo path for one release
// window so an operator can roll back without rebuilding.
func graphSourceMode() string {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("ROLLING_MEMORY_GRAPH_SOURCE"))) {
	case "legacy", "sql":
		return "legacy"
	default:
		return "engine"
	}
}

// engineGraphView relays one graph view through the verified product CLI
// channel. The admin module cannot import the engine's internal packages, so
// this follows the existing hydrate/documents bridge.
func engineGraphView(home, dbPath string, payload map[string]any) (GraphQueryResponse, error) {
	var out GraphQueryResponse
	bin := resolveBinary(home)
	if strings.TrimSpace(bin) == "" {
		return out, errors.New("rolling-memory binary not found")
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return out, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), engineGraphExecTimeout)
	defer cancel()
	raw, err := engineCommandRunner(ctx, bin, "graph", []string{"--db", dbPath}, body)
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("decode engine graph response: %w", err)
	}
	if strings.TrimSpace(out.Source) == "" {
		return out, errors.New("engine graph response is missing source")
	}
	return out, nil
}

func buildMemoryGraph(dbPath, reqScope string, limit int, keyword string) GraphQueryResponse {
	return buildMemoryGraphWithLayers(dbPath, reqScope, limit, keyword, nil, nil)
}

func normalizeAdminMemoryLayer(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	// L1 Background 只接受“用户自己导入的本地目录文件底物”同义词；
	// 早期 warrant（referential / procedural）不得再映射为 background。
	case "background", "bg", "substrate", "document", "doc":
		return "background"
	// L3 Noetic 只接受“Agent 提问 + 检索证据固化”的同义词。
	case "noetic", "qa", "insight":
		return "noetic"
	default:
		// L2 Episodic 承接 Harness 对话记录与未知/空层回退。
		return "episodic"
	}
}

// ==================== 图谱两相构建：领域装配 × 视图渲染 ====================
//
// 领域装配（SQL 拉取 → 三层合并 → 地址容器裁剪 → 五级粒子富化 → 全域总数解算）与请求过滤/限额无关，
// 代价昂贵（全量 DP 分词与分子解算）。同一金库指纹（graphVaultFingerprint）与同一（scope、权重）
// 下，所有视图形状共享同一份已装配领域快照，避免容量阶梯 / 关键词 / 层级切换重跑全量装配。

// graphDomain 一次装配、多视图复用的图谱领域快照。
type graphDomain struct {
	fingerprint string
	builtAt     time.Time
	scope       string
	weightsKey  string
	sourceName  string
	nodesByKey  map[string]*GraphNodeItem
	keyOrder    []string
	rawEdges    []GraphEdgeItem
	cellMembers map[string][]*GraphNodeItem
	unitCells   map[string][]string
	atomTotal   int
	molTotal    int
	dimerTotal  int
	topAtoms    []GraphAtomParticle
	topMols     []GraphMoleculeFragment
	fullNodes   int
	fullEdges   int
}

var (
	graphDomainMu  sync.Mutex
	graphDomainCur *graphDomain
	// graphDomainTTL 是领域快照的最长存活时长（指纹之外的安全网：正文原位改写等指纹盲区最多滞后一个 TTL）。
	graphDomainTTL = 10 * time.Minute
)

// graphVaultFingerprint 计算图谱装配所依赖的金库状态指纹（三表：行数 / 写入序上界 / occurred_at 上界 / 正文长度和）。
// rm_units 是 rm_docs × rm_unit_meta 的只读视图，视图本身没有 rowid；写入序改用
// 规范表 rm_docs.rowid。指纹一致 ⇒ 装配输入一致；查询失败返回空串（调用方按未命中处理）。
func graphVaultFingerprint(dbPath string) string {
	rows, err := sqliteQueryJSON(dbPath, `
		SELECT
			(SELECT COUNT(*) FROM rm_units) || ':' ||
			(SELECT IFNULL(MAX(rowid), 0) FROM rm_docs) || ':' ||
			(SELECT IFNULL(MAX(occurred_at), 0) FROM rm_units) || ':' ||
			(SELECT IFNULL(SUM(LENGTH(COALESCE(raw_text, ''))), 0) FROM rm_units) || ':' ||
			(SELECT IFNULL(SUM(LENGTH(COALESCE(sentences_json, ''))), 0) FROM rm_units) || '|' ||
			(SELECT COUNT(*) FROM rm_bg_chunks) || ':' ||
			(SELECT IFNULL(MAX(rowid), 0) FROM rm_bg_chunks) || ':' ||
			(SELECT IFNULL(MAX(occurred_at), 0) FROM rm_bg_chunks) || ':' ||
			(SELECT IFNULL(SUM(LENGTH(COALESCE(rel_path, '') || COALESCE(abs_path, '') || COALESCE(anchor, ''))), 0) FROM rm_bg_chunks) || '|' ||
			(SELECT COUNT(*) FROM rm_noetic WHERE status = 'active') || ':' ||
			(SELECT IFNULL(MAX(rowid), 0) FROM rm_noetic WHERE status = 'active') || ':' ||
			(SELECT IFNULL(MAX(occurred_at), 0) FROM rm_noetic WHERE status = 'active') || ':' ||
			(SELECT IFNULL(SUM(LENGTH(COALESCE(query_text, '') || COALESCE(answer_text, '') || COALESCE(cited_unit_ids_json, ''))), 0) FROM rm_noetic WHERE status = 'active')
			AS fp;
	`)
	if err != nil || len(rows) == 0 {
		return ""
	}
	return asString(rows[0]["fp"])
}

// graphWeightsKey 输出与请求无关的规范化权重键（层名归一 + 上下界钳位 + 缺省 1.0），用于领域快照复用判定。
func graphWeightsKey(weights map[string]float64) string {
	eff := map[string]float64{
		"background": 1.0,
		"episodic":   1.0,
		"noetic":     1.0,
	}
	for k, w := range weights {
		normK := normalizeAdminMemoryLayer(k)
		if w < 0 {
			w = 0
		} else if w > 10.0 {
			w = 10.0
		}
		eff[normK] = w
	}
	return fmt.Sprintf("background=%g|episodic=%g|noetic=%g", eff["background"], eff["episodic"], eff["noetic"])
}

// graphDomainFor 取回（或装配并缓存）与（金库指纹、scope、权重）匹配的领域快照；指纹缺失时按未命中处理。
func graphDomainFor(dbPath, scope string, weights map[string]float64) *graphDomain {
	weightsKey := graphWeightsKey(weights)
	fingerprint := graphVaultFingerprint(dbPath)
	// Graph atoms and summaries contain source text, so pointer-bearing domains
	// must be rebuilt after live verification instead of replaying a cached domain.
	if vaultHasDocPointers(dbPath, "") {
		return assembleGraphDomain(dbPath, scope, weights, weightsKey, fingerprint)
	}
	graphDomainMu.Lock()
	cur := graphDomainCur
	graphDomainMu.Unlock()
	if fingerprint != "" && cur != nil && cur.fingerprint == fingerprint &&
		cur.scope == scope && cur.weightsKey == weightsKey &&
		time.Since(cur.builtAt) <= graphDomainTTL {
		return cur
	}
	dom := assembleGraphDomain(dbPath, scope, weights, weightsKey, fingerprint)
	graphDomainMu.Lock()
	graphDomainCur = dom
	graphDomainMu.Unlock()
	return dom
}

// fullDomainViewTotals 解算「limit≥65536 且无过滤」的全域视图渲染后节点 / 边总数（含粒子节点注入）。
//
// 全域视图中全部 Unit 必然入选（名额不受限），父 Cell 随成员入选且必然通过「无可见成员即剔除」清理，
// 故全域 kept 集合 = keyOrder 全集。这里在已富化领域上执行与选材路径同构的收尾（全边装配 + 粒子注入）后取长度，
// 与历史实现「递归重跑一次全量构建」逐位一致（已对拍验证），但成本为毫秒级。
func fullDomainViewTotals(nodesByKey map[string]*GraphNodeItem, keyOrder []string, rawEdges []GraphEdgeItem) (int, int) {
	nodes := make([]GraphNodeItem, 0, len(keyOrder))
	for _, k := range keyOrder {
		if n := nodesByKey[k]; n != nil {
			nodes = append(nodes, *n)
		}
	}
	edges := make([]GraphEdgeItem, 0, len(rawEdges))
	for _, e := range rawEdges {
		if nodesByKey[e.FromKey] == nil || nodesByKey[e.ToKey] == nil {
			continue
		}
		edges = append(edges, e)
	}
	degByKey := make(map[string]int, len(nodes))
	relCounter := make(map[string]int)
	relLayer := make(map[string]string)
	for _, e := range edges {
		degByKey[e.FromKey]++
		degByKey[e.ToKey]++
		relCounter[e.Relation]++
		if relLayer[e.Relation] == "" {
			relLayer[e.Relation] = e.Layer
		}
	}
	nodes, edges = appendVisibleParticleNodesAndEdges(nodes, edges, degByKey, relCounter, relLayer)
	return len(nodes), len(edges)
}

// buildMemoryGraphWithLayers 组装指定请求形状的三层记忆图谱（图谱页全景 / 容量阶梯 / 过滤统一下钻入口）。
func buildMemoryGraphWithLayers(dbPath, reqScope string, limit int, keyword string, layers []string, weights map[string]float64) GraphQueryResponse {
	scope := strings.TrimSpace(reqScope)
	if scope == "" {
		scope = UserScope
	}
	if limit < 0 {
		// limit < 0: 全部节点 (ALL)
		limit = 65536
	} else if limit == 0 {
		limit = 256
	} else if limit > 65536 {
		limit = 65536
	}
	layerFilter := make(map[string]bool, len(layers))
	for _, l := range layers {
		if strings.TrimSpace(l) != "" {
			layerFilter[normalizeAdminMemoryLayer(l)] = true
		}
	}
	dom := graphDomainFor(dbPath, scope, weights)
	return renderGraphView(dom, dbPath, limit, keyword, layerFilter)
}

// assembleGraphDomain 装配与请求形状无关的三层图谱领域（SQL → 合并 → 地址容器裁剪 → 粒子富化 → 全域总数）。
func assembleGraphDomain(dbPath, scope string, weights map[string]float64, weightsKey string, graphFingerprint string) *graphDomain {
	// 图谱恒由本地明文金库（rm_units / rm_noetic）合并构建，零云端拓扑拉取：
	// EventTotal/LinkTotal 只随本地金库变化，与显示限额无关。
	effWeights := map[string]float64{
		"background": 1.0,
		"episodic":   1.0,
		"noetic":     1.0,
	}
	for k, w := range weights {
		normK := normalizeAdminMemoryLayer(k)
		if w < 0 {
			w = 0
		} else if w > 10.0 {
			w = 10.0
		}
		effWeights[normK] = w
	}

	// 1. 读取本地 rm_units 与 rm_noetic 明文金库（用于还原上游盲态单元的明文摘要与原文切片）
	type localDocMeta struct {
		docID      string
		layer      string
		occurredAt int64
		rawText    string
		distilled  string
		addressURI string
	}
	localDocs := make(map[string]localDocMeta)
	var localDocOrder []string
	if vaultRows, err := sqliteQueryJSON(dbPath, `SELECT doc_id, COALESCE(layer,'episodic') AS layer, occurred_at, raw_text, COALESCE(sentences_json,'[]') AS sentences_json, COALESCE(kept_indices_json,'[]') AS kept_indices_json, address_uri FROM rm_units ORDER BY occurred_at DESC LIMIT 10000;`); err == nil {
		for _, vr := range vaultRows {
			did := strings.TrimSpace(asString(vr["doc_id"]))
			if did == "" {
				continue
			}
			rawTxt := strings.TrimSpace(asString(vr["raw_text"]))
			distTxt := rawTxt
			var sents []string
			var kept []int
			_ = json.Unmarshal([]byte(asString(vr["sentences_json"])), &sents)
			_ = json.Unmarshal([]byte(asString(vr["kept_indices_json"])), &kept)
			if len(kept) > 0 && len(sents) > 0 {
				var picked []string
				for _, idx := range kept {
					if idx >= 0 && idx < len(sents) {
						picked = append(picked, sents[idx])
					}
				}
				if len(picked) > 0 {
					distTxt = strings.Join(picked, "\n")
				}
			}
			localDocs[did] = localDocMeta{
				docID:      did,
				layer:      normalizeAdminMemoryLayer(asString(vr["layer"])),
				occurredAt: int64(asInt(vr["occurred_at"])),
				rawText:    rawTxt,
				distilled:  distTxt,
				addressURI: asString(vr["address_uri"]),
			}
			localDocOrder = append(localDocOrder, did)
		}
	}
	if bgRows, err := sqliteQueryJSON(dbPath, `SELECT unit_id, abs_path, rel_path, anchor, occurred_at, address_uri FROM rm_bg_chunks ORDER BY occurred_at DESC LIMIT 10000;`); err == nil {
		for _, br := range bgRows {
			uid := strings.TrimSpace(asString(br["unit_id"]))
			if uid == "" {
				continue
			}
			if _, exists := localDocs[uid]; exists {
				continue
			}
			relP := strings.TrimSpace(asString(br["rel_path"]))
			anc := strings.TrimSpace(asString(br["anchor"]))
			absP := strings.TrimSpace(asString(br["abs_path"]))
			title := fmt.Sprintf("%s #%s", relP, anc)
			fullDesc := fmt.Sprintf("本地挂载文档分块: %s (%s)\n物理路径: %s", relP, anc, absP)
			localDocs[uid] = localDocMeta{
				docID:      uid,
				layer:      "background",
				occurredAt: int64(asInt(br["occurred_at"])),
				rawText:    fullDesc,
				distilled:  title,
				addressURI: asString(br["address_uri"]),
			}
			localDocOrder = append(localDocOrder, uid)
		}
	}

	// 零拷贝指针行在图谱节点上必须显示水合后的规范正文（守护进程优先、缓存兜底），
	// 而不是存储的 `@ref:` 指针本身；早期实现在语料不可用回落到真实金库时会把指针原样
	// 画进节点摘要。
	graphHome, _ := os.UserHomeDir()
	graphRows := make([]map[string]any, 0, len(localDocs))
	for _, did := range localDocOrder {
		dm := localDocs[did]
		if strings.HasPrefix(strings.TrimSpace(dm.rawText), "@ref:") {
			graphRows = append(graphRows, map[string]any{"id": dm.docID, "content": dm.rawText})
		}
	}
	warmHydratePointersBatch(graphHome, dbPath, graphRows)
	graphTexts := make(map[hydrateRef]string, len(graphRows))
	for _, row := range graphRows {
		if text, ok := hydratedPointerRowText(row); ok {
			graphTexts[hydrateRef{DocID: strings.TrimSpace(asString(row["id"])), Ref: strings.TrimSpace(asString(row["content"]))}] = text
		}
	}
	graphDisplayText := func(dm localDocMeta) (string, string) {
		if ref := strings.TrimSpace(dm.rawText); strings.HasPrefix(ref, "@ref:") {
			if text := graphTexts[hydrateRef{DocID: dm.docID, Ref: ref}]; text != "" {
				return text, text
			}
			return vaultRefGapMarker, vaultRefGapMarker
		}
		return hydrateEpisodicDisplayText(graphHome, dbPath, dm.docID, dm.rawText, dm.distilled)
	}

	type localNoeticMeta struct {
		noeticID     string
		occurredAt   int64
		queryText    string
		answerText   string
		citedUnitIDs []string
		addressURI   string
	}
	localNoetics := make(map[string]localNoeticMeta)
	var localNoeticOrder []string
	if noeticRows, err := sqliteQueryJSON(dbPath, `SELECT noetic_id, occurred_at, query_text, answer_text, cited_unit_ids_json, address_uri FROM rm_noetic WHERE status = 'active' ORDER BY occurred_at DESC LIMIT 10000;`); err == nil {
		for _, nr := range noeticRows {
			nid := strings.TrimSpace(asString(nr["noetic_id"]))
			if nid == "" {
				continue
			}
			var cited []string
			_ = json.Unmarshal([]byte(asString(nr["cited_unit_ids_json"])), &cited)
			localNoetics[nid] = localNoeticMeta{
				noeticID:     nid,
				occurredAt:   int64(asInt(nr["occurred_at"])),
				queryText:    strings.TrimSpace(asString(nr["query_text"])),
				answerText:   strings.TrimSpace(asString(nr["answer_text"])),
				citedUnitIDs: cited,
				addressURI:   strings.TrimSpace(asString(nr["address_uri"])),
			}
			localNoeticOrder = append(localNoeticOrder, nid)
		}
	}

	nodesByKey := make(map[string]*GraphNodeItem)
	var keyOrder []string
	var rawEdges []GraphEdgeItem
	seenEdgeKey := make(map[string]bool)

	addNode := func(item GraphNodeItem) {
		if existing, ok := nodesByKey[item.NodeKey]; ok {
			existing.Degree += item.Degree
			if item.Weight > existing.Weight {
				existing.Weight = item.Weight
			}
			if existing.Span == "" && item.Span != "" {
				existing.Span = item.Span
			}
			return
		}
		cp := item
		if cp.ID == 0 {
			cp.ID = deterministicIntID(cp.NodeKey)
		}
		if cp.Aliases == nil {
			cp.Aliases = []string{}
		}
		nodesByKey[cp.NodeKey] = &cp
		keyOrder = append(keyOrder, cp.NodeKey)
	}

	addEdge := func(fromKey, toKey, rel, layer, anchor, span string, weight float64) {
		if fromKey == "" || toKey == "" || fromKey == toKey {
			return
		}
		ek := fromKey + "|" + rel + "|" + toKey
		if seenEdgeKey[ek] {
			return
		}
		seenEdgeKey[ek] = true
		rawEdges = append(rawEdges, GraphEdgeItem{
			Assertion:  fromKey + "->" + toKey,
			FromKey:    fromKey,
			ToKey:      toKey,
			Relation:   rel,
			Layer:      layer,
			Warrant:    layer,
			Certainty:  certaintyTier(1.0),
			Confidence: 1.0,
			Status:     "active",
			Anchor:     anchor,
			Span:       span,
			Weight:     weight,
		})
	}

	// 2. 图谱恒由本地明文金库合并构建：控制台账面零云端外呼，不拉取任何远端拓扑。
	sourceName := "local-vault"

	// 3. 合并本地 rm_units 与 rm_noetic 的单元与记忆地址容器
	for _, did := range localDocOrder {
		dm := localDocs[did]
		uNodeKey := "unit:" + dm.docID
		cellKey := extractCellKeyFromURI(dm.addressURI)
		cNodeKey := "cell:" + cellKey
		obsText := formatUnixTime(dm.occurredAt, false)

		if _, exists := nodesByKey[cNodeKey]; !exists {
			addNode(GraphNodeItem{
				ID:         deterministicIntID(cNodeKey),
				NodeKey:    cNodeKey,
				Summary:    cellKey,
				Kind:       "cell",
				Layer:      dm.layer,
				Certainty:  certaintyTier(1.0),
				Intention:  dm.layer,
				Status:     "active",
				Confidence: 1.0,
				ObservedAt: obsText,
				Anchor:     dm.addressURI,
				Span:       "the memory-engine dynamic memory-address container: " + dm.addressURI,
				Aliases:    []string{},
				Degree:     1,
				Weight:     effWeights[dm.layer],
			})
		}
		if _, exists := nodesByKey[uNodeKey]; !exists {
			txt, spanTxt := graphDisplayText(dm)
			span := spanTxt
			addNode(GraphNodeItem{
				ID:         deterministicIntID(uNodeKey),
				NodeKey:    uNodeKey,
				Summary:    clipRunes(txt, 44),
				Kind:       "unit",
				Layer:      dm.layer,
				Certainty:  certaintyTier(0.95),
				Intention:  dm.layer,
				Status:     "active",
				Confidence: 0.95,
				ObservedAt: obsText,
				Anchor:     dm.addressURI,
				Span:       span,
				Aliases:    []string{dm.docID},
				Degree:     1,
				Weight:     0.95 * effWeights[dm.layer],
			})
		}
		addEdge(uNodeKey, cNodeKey, "member_of", dm.layer, dm.addressURI, dm.rawText, effWeights[dm.layer])
	}

	for _, nid := range localNoeticOrder {
		nm := localNoetics[nid]
		nKey := "unit:" + nm.noeticID
		obsText := formatUnixTime(nm.occurredAt, false)
		addrURI := nm.addressURI
		cellKey := ""
		if addrURI != "" {
			cellKey = extractCellKeyFromURI(addrURI)
		}
		if cellKey == "" {
			for _, cid := range nm.citedUnitIDs {
				cleanCID := strings.TrimPrefix(strings.TrimSpace(cid), "unit:")
				if dm, ok := localDocs[cleanCID]; ok && dm.addressURI != "" {
					cellKey = extractCellKeyFromURI(dm.addressURI)
					if addrURI == "" {
						addrURI = dm.addressURI
					}
					break
				}
			}
		}
		anchor := addrURI
		if anchor == "" {
			anchor = nm.noeticID
		}
		span := "Q: " + nm.queryText + "\nA: " + nm.answerText
		if cellKey != "" {
			cNodeKey := "cell:" + cellKey
			if _, exists := nodesByKey[cNodeKey]; !exists {
				addNode(GraphNodeItem{
					ID:         deterministicIntID(cNodeKey),
					NodeKey:    cNodeKey,
					Summary:    cellKey,
					Kind:       "cell",
					Layer:      "noetic",
					Certainty:  certaintyTier(1.0),
					Intention:  "noetic",
					Status:     "active",
					Confidence: 1.0,
					ObservedAt: obsText,
					Anchor:     anchor,
					Span:       "the memory-engine dynamic memory-address container: " + anchor,
					Aliases:    []string{},
					Degree:     1,
					Weight:     effWeights["noetic"],
				})
			}
			addEdge(nKey, cNodeKey, "member_of", "noetic", anchor, span, effWeights["noetic"])
		}
		if _, exists := nodesByKey[nKey]; !exists {
			addNode(GraphNodeItem{
				ID:         deterministicIntID(nKey),
				NodeKey:    nKey,
				Summary:    clipRunes(nm.queryText, 44),
				Kind:       "unit",
				Layer:      "noetic",
				Certainty:  certaintyTier(1.0),
				Intention:  "noetic",
				Status:     "active",
				Confidence: 1.0,
				ObservedAt: obsText,
				Anchor:     anchor,
				Span:       span,
				Aliases:    []string{nm.noeticID},
				Degree:     max(len(nm.citedUnitIDs), 1),
				Weight:     effWeights["noetic"],
			})
		}
		for _, cid := range nm.citedUnitIDs {
			targetKey := "unit:" + strings.TrimPrefix(strings.TrimSpace(cid), "unit:")
			addEdge(nKey, targetKey, "cites", "noetic", anchor, span, effWeights["noetic"])
		}
	}

	// 3.5 为每个记忆地址容器聚合其下属成员单元的明文摘要，剔除无有效成员的空壳容器，并合成跨地址共现 weak_edge
	cellMembers := make(map[string][]*GraphNodeItem)
	unitToCell := make(map[string]string)
	unitCells := make(map[string][]string)
	seenPair := make(map[string]bool)
	for _, e := range rawEdges {
		if e.Relation == "member_of" {
			if uNode, okU := nodesByKey[e.FromKey]; okU && uNode.Kind == "unit" {
				if cNode, okC := nodesByKey[e.ToKey]; okC && cNode.Kind == "cell" {
					cellMembers[cNode.NodeKey] = append(cellMembers[cNode.NodeKey], uNode)
					unitToCell[uNode.NodeKey] = cNode.NodeKey
					pk := uNode.NodeKey + "|" + cNode.NodeKey
					if !seenPair[pk] {
						seenPair[pk] = true
						unitCells[uNode.NodeKey] = append(unitCells[uNode.NodeKey], cNode.NodeKey)
					}
				}
			}
		}
	}
	for _, nid := range localNoeticOrder {
		nm := localNoetics[nid]
		var touchedCells []string
		seenCell := make(map[string]bool)
		for _, cid := range nm.citedUnitIDs {
			uKey := "unit:" + strings.TrimPrefix(strings.TrimSpace(cid), "unit:")
			if ck := unitToCell[uKey]; ck != "" && !seenCell[ck] {
				seenCell[ck] = true
				touchedCells = append(touchedCells, ck)
			}
		}
		for i := 0; i < len(touchedCells); i++ {
			for j := i + 1; j < len(touchedCells); j++ {
				addEdge(touchedCells[i], touchedCells[j], "weak_edge", "episodic", nm.noeticID, "跨地址共振（由共同引证触发）："+nm.queryText, 1.0)
			}
		}
	}
	activeKeyOrder := make([]string, 0, len(keyOrder))
	for _, k := range keyOrder {
		n := nodesByKey[k]
		if n.Kind == "cell" {
			members := cellMembers[k]
			if len(members) == 0 {
				delete(nodesByKey, k)
				continue
			}
			shortCell := strings.TrimPrefix(strings.TrimPrefix(k, "cell:"), "C6:L1:")
			var memberPreviews []string
			bgCnt, epCnt, ntCnt := 0, 0, 0
			for idx, m := range members {
				if m.Layer == "noetic" {
					ntCnt++
				} else if m.Layer == "background" {
					bgCnt++
				} else {
					epCnt++
				}
				if idx < 8 {
					memberPreviews = append(memberPreviews, fmt.Sprintf("• [%s] %s", strings.ToUpper(m.Layer), m.Summary))
				}
			}
			if bgCnt > epCnt {
				n.Layer = "background"
			} else {
				n.Layer = "episodic"
			}
			leadTopic := strings.TrimSpace(members[0].Summary)
			n.Summary = fmt.Sprintf("%s (%d) · %s", shortCell, len(members), clipRunes(leadTopic, 18))
			n.Span = fmt.Sprintf("记忆地址容器: %s（共收纳 %d 条记忆单元：背景 %d · 情景 %d · 知性 %d）\nAddress URI: %s\n\n%s",
				strings.TrimPrefix(k, "cell:"), len(members), bgCnt, epCnt, ntCnt, n.Anchor, strings.Join(memberPreviews, "\n"))
		}
		activeKeyOrder = append(activeKeyOrder, k)
	}
	keyOrder = activeKeyOrder
	// 3.8 解算全图五级粒子拓扑（Atom 一级守恒荷 & Molecule 二级命题波包 & Dimer 二阶突触）：
	// 基于端侧本地明文金库（零词表 Unicode 脚本相变分词与句界切分），为每个 Unit 节点与 Cell 容器构建可读的
	// 原子能谱（Atoms）与分子波包（Molecules），并汇总全图 Top 原子枢纽与分子波包统计。
	atomStats, molStats, dimerTotal, topAtoms, topMols := enrichGraphWithAtomsAndMolecules(nodesByKey, keyOrder, cellMembers, unitCells)

	// 3.9 全域渲染总数解算（免递归）：历史实现在此处递归重跑一次「limit≥65536 且无过滤」的全量构建，
	// 仅为读取两个总数，导致单次显示请求执行双份 SQL 拉取 + 双份 DP 分词与粒子富化（实测占 16.7s 中的 10.1s）。
	// 全域 kept 集合 = keyOrder 全集（见 fullDomainViewTotals 说明），在已富化领域上执行同构收尾即得精确总数。
	fullNodes, fullEdges := fullDomainViewTotals(nodesByKey, keyOrder, rawEdges)

	return &graphDomain{
		fingerprint: graphFingerprint,
		builtAt:     time.Now(),
		scope:       scope,
		weightsKey:  weightsKey,
		sourceName:  sourceName,
		nodesByKey:  nodesByKey,
		keyOrder:    keyOrder,
		rawEdges:    rawEdges,
		cellMembers: cellMembers,
		unitCells:   unitCells,
		atomTotal:   atomStats,
		molTotal:    molStats,
		dimerTotal:  dimerTotal,
		topAtoms:    topAtoms,
		topMols:     topMols,
		fullNodes:   fullNodes,
		fullEdges:   fullEdges,
	}
}

// renderGraphView 在领域快照上渲染指定请求形状（容量 / 关键词 / 层级过滤）的视图。
func renderGraphView(dom *graphDomain, dbPath string, limit int, keyword string, layerFilter map[string]bool) GraphQueryResponse {
	nodesByKey := dom.nodesByKey
	keyOrder := dom.keyOrder
	rawEdges := dom.rawEdges
	cellMembers := dom.cellMembers
	unitCells := dom.unitCells
	atomStats, molStats, dimerTotal, topAtoms, topMols := dom.atomTotal, dom.molTotal, dom.dimerTotal, dom.topAtoms, dom.topMols
	sourceName := dom.sourceName
	scope := dom.scope
	totalDomainNodes := len(keyOrder)
	totalDomainEdges := len(rawEdges)
	// 非全域请求的 EventTotal/LinkTotal 直接取领域快照解算的全域渲染总数（与请求过滤 / 限额无关）。
	domainTotalNodes, domainTotalEdges := -1, -1
	if !(limit >= 65536 && len(layerFilter) == 0 && strings.TrimSpace(keyword) == "") {
		domainTotalNodes, domainTotalEdges = dom.fullNodes, dom.fullEdges
	}

	// 4. 应用三层过滤、关键词过滤与成对连通选材（Connected Pair Selection：确保任何入选 Unit 与其所属 Cell 成对保留，消除孤立点）
	kwLower := strings.ToLower(strings.TrimSpace(keyword))
	layerCounts := map[string]int{
		"background": 0,
		"episodic":   0,
		"noetic":     0,
	}
	for _, k := range keyOrder {
		n := nodesByKey[k]
		if n == nil {
			continue
		}
		layer := n.Layer
		if layer == "" {
			layer = "episodic"
		}
		if n.Kind == "unit" {
			layerCounts[layer]++
		}
	}

	seedMatched := make(map[string]bool, len(keyOrder))
	hasFilter := len(layerFilter) > 0 || kwLower != ""
	for _, k := range keyOrder {
		n := nodesByKey[k]
		if n == nil {
			continue
		}
		layer := n.Layer
		if layer == "" {
			layer = "episodic"
		}
		if len(layerFilter) > 0 && !layerFilter[layer] {
			continue
		}
		if kwLower != "" {
			var atomTokens, molTexts strings.Builder
			for _, a := range n.Atoms {
				atomTokens.WriteByte(' ')
				atomTokens.WriteString(a.Token)
			}
			for _, m := range n.Molecules {
				molTexts.WriteByte(' ')
				molTexts.WriteString(m.Text)
			}
			hay := strings.ToLower(n.NodeKey + " " + n.Summary + " " + n.Span + " " + n.Anchor + " " + strings.Join(n.Aliases, " ") + atomTokens.String() + molTexts.String())
			if !strings.Contains(hay, kwLower) {
				continue
			}
		}
		seedMatched[k] = true
	}
	if hasFilter {
		neighborKeep := make(map[string]bool)
		for _, e := range rawEdges {
			if seedMatched[e.FromKey] {
				neighborKeep[e.ToKey] = true
			}
			if seedMatched[e.ToKey] {
				neighborKeep[e.FromKey] = true
			}
		}
		for nk := range neighborKeep {
			seedMatched[nk] = true
		}
	}

	keptByKey := make(map[string]*GraphNodeItem, len(keyOrder))
	var keptOrder []string
	addKept := func(k string) bool {
		if keptByKey[k] != nil {
			return true
		}
		n := nodesByKey[k]
		if n == nil || len(keptOrder) >= limit {
			return false
		}
		// Copy before mutating: nodesByKey points into the shared cached graphDomain.
		cp := *n
		if cp.Layer == "" {
			cp.Layer = "episodic"
		}
		cp.Weight = float64(int(cp.Weight*1000+0.5)) / 1000
		keptByKey[k] = &cp
		keptOrder = append(keptOrder, k)
		return true
	}

	// 优先按三层交错轮询（Stratified Layer Interleaving：noetic -> background -> episodic）+ Unit->全部所属父 Cell 成对选入：
	// 1) 杜绝按入表先后顺序截断导致前 256 个名额被单一层（如 episodic）100% 占满、致使 L1 Background 与 L3 Noetic 在画布上为 0 的缺陷；
	// 2) 优先按父记忆地址容器聚合度（收纳成员数较多的核心容器优先）排序同层候选，让 256 视图优先呈现高内聚的地址群落而非 1:1 单成员碎片。
	var bucketNoetic, bucketBg, bucketEp []string
	for _, k := range keyOrder {
		if hasFilter && !seedMatched[k] {
			continue
		}
		n := nodesByKey[k]
		if n == nil || n.Kind == "cell" {
			continue
		}
		switch n.Layer {
		case "noetic":
			bucketNoetic = append(bucketNoetic, k)
		case "background":
			bucketBg = append(bucketBg, k)
		default:
			bucketEp = append(bucketEp, k)
		}
	}
	cellStrength := func(uKey string) int {
		best := 0
		for _, ck := range unitCells[uKey] {
			if m := len(cellMembers[ck]); m > best {
				best = m
			}
		}
		return best
	}
	primaryCellOf := func(uKey string) string {
		cells := unitCells[uKey]
		if len(cells) > 0 {
			return cells[0]
		}
		return ""
	}
	sort.SliceStable(bucketBg, func(i, j int) bool {
		si, sj := cellStrength(bucketBg[i]), cellStrength(bucketBg[j])
		if si != sj {
			return si > sj
		}
		ci, cj := primaryCellOf(bucketBg[i]), primaryCellOf(bucketBg[j])
		if ci != cj {
			return ci < cj
		}
		return bucketBg[i] < bucketBg[j]
	})
	sort.SliceStable(bucketEp, func(i, j int) bool {
		si, sj := cellStrength(bucketEp[i]), cellStrength(bucketEp[j])
		if si != sj {
			return si > sj
		}
		ci, cj := primaryCellOf(bucketEp[i]), primaryCellOf(bucketEp[j])
		if ci != cj {
			return ci < cj
		}
		return bucketEp[i] < bucketEp[j]
	})

	var interleavedUnits []string
	iN, iB, iE := 0, 0, 0
	for iN < len(bucketNoetic) || iB < len(bucketBg) || iE < len(bucketEp) {
		// 每轮按 2 条 Background + 2 条 Episodic + 1 条 Noetic 的黄金配比交错抽样
		for step := 0; step < 2 && iB < len(bucketBg); step++ {
			interleavedUnits = append(interleavedUnits, bucketBg[iB])
			iB++
		}
		for step := 0; step < 2 && iE < len(bucketEp); step++ {
			interleavedUnits = append(interleavedUnits, bucketEp[iE])
			iE++
		}
		if iN < len(bucketNoetic) {
			interleavedUnits = append(interleavedUnits, bucketNoetic[iN])
			iN++
		}
	}

	admitUnitWithParents := func(uKey string, fillSiblings bool) bool {
		if keptByKey[uKey] != nil {
			return true
		}
		n := nodesByKey[uKey]
		if n == nil || n.Kind == "cell" {
			return false
		}
		parents := unitCells[uKey]
		miss := 0
		for _, pCell := range parents {
			if keptByKey[pCell] == nil {
				miss++
			}
		}
		if len(keptOrder)+miss+1 > limit && len(keptOrder) > 0 {
			return false
		}
		for _, pCell := range parents {
			_ = addKept(pCell)
		}
		if !addKept(uKey) {
			return false
		}
		if fillSiblings {
			for _, pCell := range parents {
				siblings := cellMembers[pCell]
				addedSibs := 1
				for _, sib := range siblings {
					if addedSibs >= 4 || len(keptOrder) >= limit {
						break
					}
					if sib == nil || sib.NodeKey == uKey || keptByKey[sib.NodeKey] != nil {
						continue
					}
					if hasFilter && !seedMatched[sib.NodeKey] {
						continue
					}
					if addKept(sib.NodeKey) {
						addedSibs++
					}
				}
			}
		}
		return true
	}

	// 构建 cites 引证查找表：一旦某条 Noetic 入选，立即在同轮尝试拉入其引证的底层 Unit，使跨层紫红引证束在默认视图真实可见
	citesTargets := make(map[string][]string)
	for _, e := range rawEdges {
		if e.Relation == "cites" {
			citesTargets[e.FromKey] = append(citesTargets[e.FromKey], e.ToKey)
		}
	}

	for _, k := range interleavedUnits {
		if len(keptOrder) >= limit {
			break
		}
		if !admitUnitWithParents(k, true) {
			continue
		}
		if nodesByKey[k].Layer == "noetic" {
			for idx, targetKey := range citesTargets[k] {
				if idx >= 4 || len(keptOrder) >= limit {
					break
				}
				_ = admitUnitWithParents(targetKey, true)
			}
		}
	}
	// 产品无损收口：任何已入选但画布内无可见成员的 Cell 容器一律剔除（杜绝一切孤立容器形态）
	memberSeen := make(map[string]bool)
	for _, e := range rawEdges {
		if e.Relation == "member_of" && keptByKey[e.FromKey] != nil && keptByKey[e.ToKey] != nil {
			memberSeen[e.ToKey] = true
		}
	}
	prunedOrder := keptOrder[:0]
	for _, k := range keptOrder {
		if keptByKey[k].Kind == "cell" && !memberSeen[k] {
			delete(keptByKey, k)
			continue
		}
		prunedOrder = append(prunedOrder, k)
	}
	keptOrder = prunedOrder

	filteredNodes := make([]GraphNodeItem, 0, len(keptOrder))
	for _, k := range keptOrder {
		filteredNodes = append(filteredNodes, *keptByKey[k])
	}

	// 5. 装配有向关系边并重算度数
	degByKey := make(map[string]int, len(filteredNodes))
	relCounter := make(map[string]int)
	relLayer := make(map[string]string)
	filteredEdges := make([]GraphEdgeItem, 0, len(rawEdges))

	for _, e := range rawEdges {
		fromNode, okFrom := keptByKey[e.FromKey]
		toNode, okTo := keptByKey[e.ToKey]
		if !okFrom || !okTo {
			continue
		}
		e.ID = len(filteredEdges) + 1
		e.From = fromNode.ID
		e.To = toNode.ID
		e.FromLabel = fromNode.Summary
		e.ToLabel = toNode.Summary
		if e.ObservedAt == "" {
			e.ObservedAt = fromNode.ObservedAt
		}
		if e.Span == "" || e.Span == e.Relation {
			e.Span = fromNode.Span
		}
		e.Weight = float64(int(e.Weight*1000+0.5)) / 1000
		filteredEdges = append(filteredEdges, e)
		degByKey[e.FromKey]++
		degByKey[e.ToKey]++
		relCounter[e.Relation]++
		if relLayer[e.Relation] == "" {
			relLayer[e.Relation] = e.Layer
		}
	}

	// 5.5 注入可见的一级枢纽原子节点（kind="atom"）与二级分子波包节点（kind="molecule"）及 atom_of / molecule_of 突触边，
	// 使原子与分子在 3D 星云画布、角色筛选与下钻子图中作为真正的一等几何粒子节点呈现。
	filteredNodes, filteredEdges = appendVisibleParticleNodesAndEdges(filteredNodes, filteredEdges, degByKey, relCounter, relLayer)

	// The unlimited, unfiltered build IS the domain view; every other build reports the
	// totals that view rendered, so EventTotal stays limit- and filter-independent.
	if limit >= 65536 && !hasFilter {
		totalDomainNodes = len(filteredNodes)
		totalDomainEdges = len(filteredEdges)
	} else if domainTotalNodes >= 0 {
		totalDomainNodes = domainTotalNodes
		totalDomainEdges = domainTotalEdges
	}

	var totalDeg, multiDegCnt int
	for i := range filteredNodes {
		d := degByKey[filteredNodes[i].NodeKey]
		if d > 0 {
			filteredNodes[i].Degree = d
		}
		totalDeg += filteredNodes[i].Degree
		if filteredNodes[i].Degree >= 2 {
			multiDegCnt++
		}
	}

	relations := make([]GraphRelationOption, 0, len(relCounter))
	for rel, cnt := range relCounter {
		catKey, catDisp := layerRelationCategoryDisplay(relLayer[rel])
		relations = append(relations, GraphRelationOption{
			Relation: rel,
			Count:    cnt,
			Classes:  []GraphRelationClass{{Key: catKey, Display: catDisp}},
		})
	}
	sort.Slice(relations, func(i, j int) bool {
		if relations[i].Count != relations[j].Count {
			return relations[i].Count > relations[j].Count
		}
		return relations[i].Relation < relations[j].Relation
	})

	var avgDeg, multiRatio, avgRelReuse float64
	if len(filteredNodes) > 0 {
		avgDeg = float64(int(float64(totalDeg)*100/float64(len(filteredNodes))+0.5)) / 100
		multiRatio = float64(int(float64(multiDegCnt)*1000/float64(len(filteredNodes))+0.5)) / 10
	}
	if len(relations) > 0 && len(filteredEdges) > 0 {
		avgRelReuse = float64(int(float64(len(filteredEdges))*100/float64(len(relations))+0.5)) / 100
	}

	scopes := graphScopeItems(dbPath, len(filteredNodes), len(filteredEdges))

	return GraphQueryResponse{
		Source:    sourceName,
		ScopeKey:  scope,
		Scopes:    scopes,
		Nodes:     filteredNodes,
		Edges:     filteredEdges,
		Relations: relations,
		Stats: GraphStats{
			EventTotal:           totalDomainNodes,
			LinkTotal:            totalDomainEdges,
			NodeShown:            len(filteredNodes),
			EdgeShown:            len(filteredEdges),
			IsolatedShown:        0,
			AvgEntityDegree:      avgDeg,
			MultiEdgeEntityRatio: multiRatio,
			AvgRelationReuse:     avgRelReuse,
			ObjectNodeRatio:      100.0,
			LayerNodeCounts:      layerCounts,
			AtomTotal:            atomStats,
			MoleculeTotal:        molStats,
			DimerSynapseTotal:    dimerTotal,
			TopAtoms:             topAtoms,
			TopMolecules:         topMols,
		},
		Truncated: GraphTruncated{
			Nodes: totalDomainNodes > len(filteredNodes) && !hasFilter,
			Edges: false,
		},
	}
}

func extractCellKeyFromURI(addrURI string) string {
	addrURI = strings.TrimSpace(addrURI)
	if addrURI == "" {
		return "C6:L1:LOCAL"
	}
	if parts := strings.Split(addrURI, "/"); len(parts) >= 3 {
		last := parts[len(parts)-1]
		if idx := strings.IndexByte(last, '#'); idx > 0 {
			return last[:idx]
		}
		return last
	}
	if idx := strings.IndexByte(addrURI, '#'); idx > 0 {
		return addrURI[:idx]
	}
	return addrURI
}

func clipRunes(s string, maxRunes int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	r := []rune(s)
	if len(r) > maxRunes {
		return string(r[:maxRunes]) + "…"
	}
	return s
}

// expandMemoryGraph 在 the memory engine 三层图谱上以 seedKeys 为圆心执行精确 k 跳无向邻域 BFS 下钻。
func expandMemoryGraph(dbPath, reqScope string, seedKeys []string, depth, limit int) GraphQueryResponse {
	scope := strings.TrimSpace(reqScope)
	if scope == "" {
		scope = UserScope
	}
	if depth <= 0 {
		depth = 1
	}
	if depth > 3 {
		depth = 3
	}
	if limit <= 0 {
		limit = 260
	}
	full := buildMemoryGraphWithLayers(dbPath, scope, 1000, "", nil, nil)
	if len(seedKeys) == 0 {
		return full
	}

	visited := make(map[string]bool, len(seedKeys))
	frontier := make([]string, 0, len(seedKeys))
	for _, sk := range seedKeys {
		k := strings.TrimSpace(sk)
		if k != "" && !visited[k] {
			visited[k] = true
			frontier = append(frontier, k)
		}
	}
	for step := 0; step < depth && len(frontier) > 0; step++ {
		var next []string
		for _, fk := range frontier {
			for _, e := range full.Edges {
				if e.FromKey == fk && !visited[e.ToKey] {
					visited[e.ToKey] = true
					next = append(next, e.ToKey)
				} else if e.ToKey == fk && !visited[e.FromKey] {
					visited[e.FromKey] = true
					next = append(next, e.FromKey)
				}
			}
		}
		frontier = next
	}

	var subNodes []GraphNodeItem
	for _, n := range full.Nodes {
		if visited[n.NodeKey] {
			subNodes = append(subNodes, n)
			if len(subNodes) >= limit {
				break
			}
		}
	}
	kept := make(map[string]bool, len(subNodes))
	for _, n := range subNodes {
		kept[n.NodeKey] = true
	}
	var subEdges []GraphEdgeItem
	for _, e := range full.Edges {
		if kept[e.FromKey] && kept[e.ToKey] {
			e.ID = len(subEdges) + 1
			subEdges = append(subEdges, e)
		}
	}

	full.Nodes = subNodes
	full.Edges = subEdges
	full.Stats.NodeShown = len(subNodes)
	full.Stats.EdgeShown = len(subEdges)
	return full
}

// graphScopeItems 返回当前记忆作用域统计项（优先统计 rm_units / rm_noetic 与上游单元）。
func graphScopeItems(dbPath string, fallbackUnits, fallbackEdges int) []GraphScopeItem {
	var docsCnt, cellsCnt, noeticCnt int
	if rows, err := sqliteQueryJSON(dbPath, `
		SELECT
			COUNT(*) AS docs_cnt,
			COUNT(DISTINCT CASE WHEN address_uri != '' THEN substr(address_uri, 1, instr(address_uri || '#', '#') - 1) END) AS cells_cnt
		FROM rm_units;
	`); err == nil && len(rows) > 0 {
		docsCnt = asInt(rows[0]["docs_cnt"])
		cellsCnt = asInt(rows[0]["cells_cnt"])
	}
	if rows, err := sqliteQueryJSON(dbPath, `SELECT COUNT(*) AS cnt FROM rm_noetic WHERE status = 'active';`); err == nil && len(rows) > 0 {
		noeticCnt = asInt(rows[0]["cnt"])
	}
	events := docsCnt + noeticCnt
	links := cellsCnt + docsCnt
	if events == 0 && fallbackUnits > 0 {
		events = fallbackUnits
		links = fallbackEdges
	}
	return []GraphScopeItem{
		{
			Source:    "open-memory-v1",
			ScopeKey:  UserScope,
			Events:    events,
			Links:     links,
			Materials: docsCnt,
		},
	}
}

// graphScriptClass 对齐 RAME Unicode 脚本相变分词类别（100% 语种无关、零词表、零字符长度魔数截断）。
func graphScriptClass(r rune) int {
	switch {
	case unicode.Is(unicode.Han, r):
		return 1
	case unicode.Is(unicode.Hiragana, r), unicode.Is(unicode.Katakana, r):
		return 2
	case unicode.Is(unicode.Hangul, r):
		return 3
	case unicode.IsLetter(r):
		return 4
	case unicode.IsDigit(r):
		return 5
	default:
		return 0
	}
}

func graphRunePriorBits(r rune) float64 {
	switch {
	case unicode.Is(unicode.Han, r):
		return 10.0
	case unicode.Is(unicode.Hangul, r):
		return 7.0
	case unicode.Is(unicode.Hiragana, r), unicode.Is(unicode.Katakana, r):
		return 4.2
	case unicode.IsDigit(r):
		return 3.32
	case unicode.IsLetter(r):
		if unicode.IsUpper(r) {
			return 2.6
		}
		return 2.0
	default:
		return 0
	}
}

func graphAtomFnv64(s string) uint64 {
	var h uint64 = 14695981039346656037
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= 1099511628211
	}
	h ^= h >> 30
	h *= 0xbf58476d1ce4e5b9
	h ^= h >> 27
	h *= 0x94d049bb133111eb
	h ^= h >> 31
	return h
}

type graphExtractedAtom struct {
	normKey   string
	surface   string
	h         uint64
	priorBits float64
	isCodeID  bool
}

type graphHanCorpusStats struct {
	totalHan int
	uniCount map[rune]int
	biCount  map[[2]rune]int
}

func newGraphHanCorpusStats() *graphHanCorpusStats {
	return &graphHanCorpusStats{
		uniCount: make(map[rune]int, 512),
		biCount:  make(map[[2]rune]int, 1024),
	}
}

func (st *graphHanCorpusStats) observeText(text string) {
	if st == nil {
		return
	}
	var prevHan rune
	hasPrev := false
	for _, r := range text {
		if unicode.Is(unicode.Han, r) {
			st.totalHan++
			st.uniCount[r]++
			if hasPrev {
				st.biCount[[2]rune{prevHan, r}]++
			}
			prevHan = r
			hasPrev = true
		} else {
			hasPrev = false
		}
	}
}

// segmentHanRunByEndogenousDP 使用全图双字互信息凝聚度与一维动态规划，将连续汉字串切分为非重叠双字实词（零词表、零 4 字定长硬切）。
func segmentHanRunByEndogenousDP(run []rune, st *graphHanCorpusStats) [][]rune {
	n := len(run)
	if n == 0 {
		return nil
	}
	if n <= 2 {
		return [][]rune{run}
	}
	dp := make([]float64, n+1)
	choice := make([]int, n+1) // 1 = 跳过单字, 2 = 选入双字 [i-2:i]
	for i := 1; i <= n; i++ {
		dp[i] = dp[i-1] + 0.10
		choice[i] = 1
		if i >= 2 {
			r1, r2 := run[i-2], run[i-1]
			biScore := 1.20
			if st != nil && st.totalHan > 0 {
				bc := st.biCount[[2]rune{r1, r2}]
				u1 := max(1, st.uniCount[r1])
				u2 := max(1, st.uniCount[r2])
				pmiArg := float64(st.totalHan*bc) / float64(u1*u2)
				// 高频复现双字与高互信息双字获得内生凝聚加成；被高频虚字（u1 或 u2 极大且 bc==1）跨界粘连的伪双字自动降权
				biScore = 1.0 + math.Log1p(float64(bc)) + 0.45*math.Log1p(pmiArg) - 0.18*math.Log1p(float64(u1+u2)/16.0)
				if biScore < 0.35 {
					biScore = 0.35
				}
			}
			if cand := dp[i-2] + biScore; cand >= dp[i] {
				dp[i] = cand
				choice[i] = 2
			}
		}
	}
	var rev [][]rune
	for cur := n; cur > 0; {
		if choice[cur] == 2 && cur >= 2 {
			rev = append(rev, run[cur-2:cur])
			cur -= 2
		} else {
			cur--
		}
	}
	for l, r := 0, len(rev)-1; l < r; l, r = l+1, r-1 {
		rev[l], rev[r] = rev[r], rev[l]
	}
	return rev
}

func saturateAtomPriorBits(rawBits float64) float64 {
	if rawBits <= 24.0 {
		return rawBits
	}
	return 24.0 + 6.0*math.Log1p((rawBits-24.0)/6.0)
}

func isMarkdownTableSeparatorLine(s string) bool {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" || !strings.Contains(trimmed, "-") {
		return false
	}
	for _, r := range trimmed {
		if r != '|' && r != '｜' && r != '-' && r != ':' && !unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

// stripGraphEnvelopePrefix 纯形式剥离对话/问答行首的信封元数据壳（如 "[2023-08-23] Melanie: "、"Q: "、"### 1. "），
// 严格要求说话人冒号后紧跟空白符且前缀不含代码路径/点号/反引号，彻底杜绝将 `containers.go:568` 误判为说话人前缀截断。
func stripGraphEnvelopePrefix(line string) string {
	s := strings.TrimSpace(line)
	if isMarkdownTableSeparatorLine(s) {
		return ""
	}
	for len(s) > 0 && s[0] == '[' {
		if idx := strings.IndexByte(s, ']'); idx > 0 && idx < 40 {
			s = strings.TrimSpace(s[idx+1:])
		} else {
			break
		}
	}
	// 剥离行首 Markdown 标题 '#'、引用 '>' 与无序列表头
	for len(s) > 0 && (s[0] == '#' || s[0] == '>') {
		s = strings.TrimSpace(s[1:])
	}
	for _, bullet := range []string{"- ", "* ", "• ", "+ "} {
		if strings.HasPrefix(s, bullet) {
			s = strings.TrimSpace(s[len(bullet):])
			break
		}
	}
	// 剥离行首纯数字序号 "1. " / "12) "
	if len(s) >= 3 && s[0] >= '0' && s[0] <= '9' {
		j := 0
		for j < len(s) && j < 4 && s[j] >= '0' && s[j] <= '9' {
			j++
		}
		if j < len(s) && (s[j] == '.' || s[j] == ')') && j+1 < len(s) && s[j+1] == ' ' {
			s = strings.TrimSpace(s[j+2:])
		}
	}
	if strings.HasPrefix(s, "Q: ") || strings.HasPrefix(s, "A: ") {
		s = strings.TrimSpace(s[3:])
	}
	if colonIdx := strings.IndexByte(s, ':'); colonIdx > 0 && colonIdx <= 20 && colonIdx+1 < len(s) && (s[colonIdx+1] == ' ' || s[colonIdx+1] == '\t') {
		prefix := s[:colonIdx]
		validSpeaker := true
		for _, r := range prefix {
			if !(unicode.IsLetter(r) || unicode.IsSpace(r) || r == '_' || r == '-') {
				validSpeaker = false
				break
			}
		}
		if validSpeaker && !strings.ContainsAny(prefix, "`./\\0123456789") {
			s = strings.TrimSpace(s[colonIdx+1:])
		}
	}
	return s
}

// cleanMoleculeDisplayText 规整分子波包文本：
//  1. 将 Markdown 表格单元格 '|' 转为 ' · '，剔除纯序号列；
//  2. 剥离反引号与粗体星号 '**'，防止出现 `68\` · ...` 或半截反引号残片；
//  3. 剥离行内 LaTeX 数学排版公式 `$...$` 外壳，使自然语言命题完整连贯。
func cleanMoleculeDisplayText(raw string) string {
	return formatMoleculeCells(raw, false)
}

// compactMoleculeDisplayText 生成卡片「命题波包」展示摘要：在 cleanMoleculeDisplayText 基础上，
// 当 Markdown 表格行含超长纯代码符号罗列列时自动折叠该列（代码符号已由下方「共价核心原子」展示），让命题语义完整露出。
func compactMoleculeDisplayText(raw string) string {
	return formatMoleculeCells(raw, true)
}

func formatMoleculeCells(raw string, foldCodeColumn bool) string {
	s := strings.TrimSpace(raw)
	if s == "" || isMarkdownTableSeparatorLine(s) {
		return ""
	}
	// 移除行内 $...$ LaTeX 数学公式块（避免 \mu_{\cos} 等排版代码挤占自然语言命题波包）
	for {
		l := strings.IndexByte(s, '$')
		if l < 0 {
			break
		}
		r := strings.IndexByte(s[l+1:], '$')
		if r < 0 {
			s = s[:l] + s[l+1:]
			break
		}
		s = s[:l] + " " + s[l+1+r+1:]
	}
	if strings.Contains(s, "|") || (foldCodeColumn && strings.Contains(s, " · ")) {
		sep := "|"
		if !strings.Contains(s, "|") {
			sep = " · "
		}
		rawCells := strings.Split(s, sep)
		var cleanCells []string
		for _, cell := range rawCells {
			c := strings.TrimSpace(cell)
			c = strings.ReplaceAll(c, "**", "")
			c = strings.ReplaceAll(c, "`", "")
			c = stripGraphEnvelopePrefix(c)
			if c == "" || isMarkdownTableSeparatorLine(c) {
				continue
			}
			// 跳过表格首列纯序号（如 "1"、"#1"、"1."）
			trimmedNum := strings.Trim(c, "#.() ")
			isPureShortNum := len(trimmedNum) > 0 && len(trimmedNum) <= 3
			for _, r := range trimmedNum {
				if r < '0' || r > '9' {
					isPureShortNum = false
					break
				}
			}
			if isPureShortNum && len(cleanCells) == 0 {
				continue
			}
			if foldCodeColumn {
				hasHan := false
				asciiCount := 0
				for _, r := range c {
					if unicode.Is(unicode.Han, r) {
						hasHan = true
						break
					}
					if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
						asciiCount++
					}
				}
				if !hasHan && asciiCount >= 28 && len(cleanCells) > 0 {
					continue
				}
			}
			cleanCells = append(cleanCells, c)
		}
		s = strings.Join(cleanCells, " · ")
	} else {
		s = strings.ReplaceAll(s, "**", "")
		s = strings.ReplaceAll(s, "`", "")
		s = stripGraphEnvelopePrefix(s)
	}
	s = strings.TrimSpace(s)
	for strings.HasPrefix(s, "·") || strings.HasPrefix(s, "|") || strings.HasPrefix(s, ":") || strings.HasPrefix(s, "-") {
		s = strings.TrimSpace(strings.TrimLeft(s, "·|:- "))
	}
	return strings.Join(strings.Fields(s), " ")
}

// clipMoleculeText 按词/分隔符边界安全截断分子展示文本，绝不在英文标识符中间腰斩。
func clipMoleculeText(s string, maxUnits int) string {
	s = strings.TrimSpace(s)
	runes := []rune(s)
	if len(runes) <= maxUnits {
		return s
	}
	cut := maxUnits
	if cut < len(runes) && unicode.IsLetter(runes[cut-1]) && unicode.IsLetter(runes[cut]) {
		for cut > maxUnits/2 && unicode.IsLetter(runes[cut-1]) {
			cut--
		}
	}
	out := strings.TrimSpace(strings.TrimRight(string(runes[:cut]), "·|,-:; "))
	return out + "…"
}

// segmentPlaintextToGraphAtoms 按 Unicode 脚本相变与全图双字互信息 DP 切分明文词元，计算对数软饱和香农先验位宽。
func segmentPlaintextToGraphAtoms(text string) []graphExtractedAtom {
	return segmentPlaintextToGraphAtomsWithCorpus(text, nil)
}

func segmentPlaintextToGraphAtomsWithCorpus(text string, hanStats *graphHanCorpusStats) []graphExtractedAtom {
	var out []graphExtractedAtom
	var cur []rune
	var curBits float64
	prevClass := 0
	emitAtom := func(tokens []rune, cl int) {
		if len(tokens) == 0 {
			return
		}
		if cl == 4 && len(tokens) < 2 {
			return
		}
		// 跳过纯数字行号/序号残片（cl == 5 且无字母伴随）
		if cl == 5 {
			return
		}
		var rawBits float64
		hasUpper := false
		hasLower := false
		for idx, r := range tokens {
			rawBits += graphRunePriorBits(r)
			if unicode.IsUpper(r) && idx > 0 {
				hasUpper = true
			}
			if unicode.IsLower(r) {
				hasLower = true
			}
		}
		surf := string(tokens)
		norm := strings.ToLower(surf)
		h := graphAtomFnv64(norm)
		isCode := (cl == 4) && ((hasUpper && hasLower) || strings.ContainsRune(surf, '_') || rawBits > 26.0)
		out = append(out, graphExtractedAtom{
			normKey:   norm,
			surface:   surf,
			h:         h,
			priorBits: saturateAtomPriorBits(rawBits),
			isCodeID:  isCode,
		})
	}
	flush := func() {
		if len(cur) == 0 {
			return
		}
		if prevClass == 1 {
			for _, seg := range segmentHanRunByEndogenousDP(cur, hanStats) {
				emitAtom(seg, 1)
			}
		} else {
			emitAtom(cur, prevClass)
		}
		cur = cur[:0]
		curBits = 0
	}
	for _, r := range text {
		if r == '_' && prevClass == 4 {
			cur = append(cur, r)
			curBits += 1.0
			continue
		}
		cl := graphScriptClass(r)
		if cl == 0 {
			flush()
			prevClass = 0
			continue
		}
		if prevClass != 0 && cl != prevClass && !(prevClass == 4 && cl == 5) && !(prevClass == 5 && cl == 4) {
			flush()
		}
		cur = append(cur, r)
		curBits += graphRunePriorBits(r)
		prevClass = cl
	}
	flush()
	return out
}

func splitPlaintextIntoMoleculeSentences(text string) []string {
	var out []string
	lines := strings.Split(text, "\n")
	for _, rawLine := range lines {
		cleanedLine := stripGraphEnvelopePrefix(rawLine)
		if cleanedLine == "" {
			continue
		}
		// 若该行是 Markdown 表格行（含 >=2 个 '|'），整行经 cleanMoleculeDisplayText 规整为一个完整语义分子，绝不在行内 '.' 腰斩
		if strings.Count(cleanedLine, "|") >= 2 {
			if mTxt := cleanMoleculeDisplayText(cleanedLine); mTxt != "" {
				out = append(out, mTxt)
			}
			continue
		}
		runes := []rune(cleanedLine)
		n := len(runes)
		var buf strings.Builder
		inBacktick := false
		inMath := false
		parenDepth := 0
		flush := func() {
			s := cleanMoleculeDisplayText(buf.String())
			buf.Reset()
			if s != "" {
				out = append(out, s)
			}
		}
		for i := 0; i < n; i++ {
			r := runes[i]
			if r == '`' {
				inBacktick = !inBacktick
				buf.WriteRune(r)
				continue
			}
			if r == '$' {
				inMath = !inMath
				continue
			}
			if !inBacktick && !inMath {
				switch r {
				case '(', '（', '[':
					parenDepth++
				case ')', '）', ']':
					if parenDepth > 0 {
						parenDepth--
					}
				}
			}
			if !inBacktick && !inMath && parenDepth == 0 {
				isTerm := r == '\r' || r == '。' || r == '！' || r == '？' || r == '；' || r == ';' || r == '!' || r == '?'
				if !isTerm && r == '.' {
					prevAlnum := i > 0 && (unicode.IsLetter(runes[i-1]) || unicode.IsDigit(runes[i-1]) || runes[i-1] == '_' || runes[i-1] == '`')
					nextAlnum := i+1 < n && (unicode.IsLetter(runes[i+1]) || unicode.IsDigit(runes[i+1]) || runes[i+1] == '_' || runes[i+1] == ':' || runes[i+1] == '/')
					// 排除列表序号 "1. " 与文件名/小数点 "containers.go:568" / "3.14"
					isListPrefix := i <= 3 && i > 0 && unicode.IsDigit(runes[i-1])
					isTerm = !(prevAlnum && nextAlnum) && !isListPrefix
				}
				if isTerm {
					flush()
					continue
				}
			}
			buf.WriteRune(r)
		}
		flush()
	}
	return out
}

// enrichGraphWithAtomsAndMolecules 从端侧本地明文金库节点中解算五级粒子拓扑（AtomView & MoleculeView & DimerSynapses）：
// 采用两遍内生信息物理法——
//  1. 第一遍：统计全图原子文档频次 df(h) 与跨地址分布，计算每个原子的内生逆频次惊奇度 m(h) = ln(1 + N/df(h)) 与香农先验增益；
//  2. 第二遍：切分句子分子波包，按原子真实能量 e(h) = m(h)*ln(1 + B(h)/4) 选拔高惊奇度实义核心原子（排除全图高频泛化胶水词），
//     合成分子共价包哈希与分子联合惊奇度能量 E_mol(M) = γ_mol(M) * Σ e(h) * (k_salient / (k_salient + e))，彻底淘汰寒暄短句噪音。
func enrichGraphWithAtomsAndMolecules(
	nodesByKey map[string]*GraphNodeItem,
	keyOrder []string,
	cellMembers map[string][]*GraphNodeItem,
	unitCells map[string][]string,
) (atomTotal int, molTotal int, dimerTotal int, topAtoms []GraphAtomParticle, topMols []GraphMoleculeFragment) {
	type atomRecord struct {
		surface   string
		h         uint64
		priorBits float64
		units     map[string]int
		cells     map[string]bool
	}
	type molRecord struct {
		molKey     string
		molHash    uint64
		text       string
		coreAtoms  []string
		atomEnergy float64
		salientCnt int
		units      map[string]bool
		cooccur    map[uint64]int
	}

	atomMap := make(map[uint64]*atomRecord, 512)
	unitAtomCounts := make(map[string]map[uint64]int, len(keyOrder))
	unitCleanText := make(map[string]string, len(keyOrder))
	molMap := make(map[uint64]*molRecord, 256)
	unitMols := make(map[string][]uint64, len(keyOrder))
	totalUnits := 0

	hanStats := newGraphHanCorpusStats()

	// Pass 0：剥离信封前缀并收集全图连续表意文字双字互信息统计底座（供一维 DP 词界切分使用）
	for _, k := range keyOrder {
		n := nodesByKey[k]
		if n == nil || n.Kind != "unit" {
			continue
		}
		totalUnits++
		rawText := strings.TrimSpace(n.Span)
		if rawText == "" {
			rawText = strings.TrimSpace(n.Summary)
		}
		var cleanLines []string
		for _, ln := range strings.Split(rawText, "\n") {
			if cl := stripGraphEnvelopePrefix(ln); cl != "" {
				cleanLines = append(cleanLines, cl)
			}
		}
		cleanBody := strings.Join(cleanLines, "\n")
		if cleanBody == "" {
			cleanBody = rawText
		}
		unitCleanText[k] = cleanBody
		hanStats.observeText(cleanBody)
	}

	// Pass 1：统计全图原子的逆频次惊奇度底座
	for _, k := range keyOrder {
		n := nodesByKey[k]
		if n == nil || n.Kind != "unit" {
			continue
		}
		cleanBody := unitCleanText[k]
		atoms := segmentPlaintextToGraphAtomsWithCorpus(cleanBody, hanStats)
		uCounts := make(map[uint64]int, len(atoms))
		for _, a := range atoms {
			if a.h == 0 {
				continue
			}
			uCounts[a.h]++
			rec := atomMap[a.h]
			if rec == nil {
				rec = &atomRecord{
					surface:   a.surface,
					h:         a.h,
					priorBits: a.priorBits,
					units:     make(map[string]int, 4),
					cells:     make(map[string]bool, 2),
				}
				atomMap[a.h] = rec
			}
			rec.units[k]++
			for _, ck := range unitCells[k] {
				rec.cells[ck] = true
			}
		}
		unitAtomCounts[k] = uCounts
	}

	if totalUnits < 1 {
		totalUnits = 1
	}
	nDocs := float64(totalUnits)

	// 计算拓扑内原子的平均静质量基准 meanMass，用于内生识别高惊奇度实义原子（零停用词表）
	var sumAtomMass float64
	for _, rec := range atomMap {
		df := max(1, len(rec.units))
		sumAtomMass += math.Log(1.0 + nDocs/float64(df))
	}
	meanAtomMass := 1.5
	if len(atomMap) > 0 {
		meanAtomMass = sumAtomMass / float64(len(atomMap))
	}
	salientMassFloor := 0.72 * meanAtomMass

	atomEnergyOf := func(h uint64) (energy float64, massNats float64, isSalient bool) {
		rec := atomMap[h]
		if rec == nil {
			return 0, 0, false
		}
		df := max(1, len(rec.units))
		massNats = math.Log(1.0 + nDocs/float64(df))
		shannonGain := math.Log1p(rec.priorBits / 4.0)
		// 对全图出现率超过 18% 的泛化背景词施加连续软阻尼（带样本置信核 scaleN = N/(N+16)，防小图误伤共享主题词）
		scaleN := nDocs / (nDocs + 16.0)
		ubiquityRatio := float64(df) / nDocs
		ubiquityDamp := 1.0 - scaleN*(1.0-math.Exp(-3.2*ubiquityRatio))
		energy = massNats * shannonGain * ubiquityDamp
		isSalient = massNats >= salientMassFloor && rec.priorBits >= 5.5
		return energy, massNats, isSalient
	}

	// Pass 2：基于「香农位宽 × 逆频次惊奇度 × 多核共价协同」解算高价值分子波包与二聚体突触
	for _, k := range keyOrder {
		n := nodesByKey[k]
		if n == nil || n.Kind != "unit" {
			continue
		}
		cleanBody := unitCleanText[k]
		sents := splitPlaintextIntoMoleculeSentences(cleanBody)
		if len(sents) == 0 && cleanBody != "" {
			if mTxt := cleanMoleculeDisplayText(cleanBody); mTxt != "" {
				sents = []string{mTxt}
			}
		}
		var orderedMolHashes []uint64
		seenInUnit := make(map[uint64]bool, len(sents))
		for _, sTxt := range sents {
			sAtoms := segmentPlaintextToGraphAtomsWithCorpus(sTxt, hanStats)
			if len(sAtoms) < 2 {
				continue
			}
			type scoredAtom struct {
				atom      graphExtractedAtom
				energy    float64
				isSalient bool
			}
			var uniqScored []scoredAtom
			seenA := make(map[uint64]bool, len(sAtoms))
			salientCnt := 0
			for _, sa := range sAtoms {
				if sa.h == 0 || seenA[sa.h] {
					continue
				}
				seenA[sa.h] = true
				en, _, sal := atomEnergyOf(sa.h)
				if sal {
					salientCnt++
				}
				uniqScored = append(uniqScored, scoredAtom{
					atom:      sa,
					energy:    en,
					isSalient: sal,
				})
			}
			// 至少需要 2 个不同原子且至少包含 1 个高于内生惊奇度基准的实义原子，才构成分子波包（过滤 "You too" / "How was it" 等纯寒暄碎片）
			if len(uniqScored) < 2 || salientCnt < 1 {
				continue
			}
			sort.SliceStable(uniqScored, func(i, j int) bool {
				if uniqScored[i].energy != uniqScored[j].energy {
					return uniqScored[i].energy > uniqScored[j].energy
				}
				return uniqScored[i].atom.h < uniqScored[j].atom.h
			})
			var bagXor, bagSum uint64
			var coreNames []string
			var sumCoreEnergy float64
			coreTake := len(uniqScored)
			if coreTake > 5 {
				coreTake = 5
			}
			for idx := 0; idx < coreTake; idx++ {
				sa := uniqScored[idx]
				bagXor ^= sa.atom.h
				bagSum += sa.atom.h * 0x9e3779b97f4a7c15
				sumCoreEnergy += sa.energy
			}
			// 选拔 4 个展示用共价核心原子（CoreAtoms）：
			// 当句内同时含代码标识符（isCodeID）与自然语言概念词时，代码标识符最多占 2 席，确保「代码符号 × 概念词」均衡呈现。
			hasNonCode := false
			for _, sa := range uniqScored {
				if !sa.atom.isCodeID && sa.atom.priorBits >= 12.0 {
					hasNonCode = true
					break
				}
			}
			usedIdx := make([]bool, len(uniqScored))
			codeTaken := 0
			for idx, sa := range uniqScored {
				if len(coreNames) >= 4 {
					break
				}
				if hasNonCode && sa.atom.isCodeID && codeTaken >= 2 {
					continue
				}
				if sa.atom.isCodeID {
					codeTaken++
				}
				usedIdx[idx] = true
				coreNames = append(coreNames, sa.atom.surface)
			}
			for idx, sa := range uniqScored {
				if len(coreNames) >= 4 {
					break
				}
				if !usedIdx[idx] {
					coreNames = append(coreNames, sa.atom.surface)
				}
			}
			molH := bagXor ^ bagSum
			if molH == 0 {
				continue
			}
			orderedMolHashes = append(orderedMolHashes, molH)
			if !seenInUnit[molH] {
				seenInUnit[molH] = true
				unitMols[k] = append(unitMols[k], molH)
			}
			mRec := molMap[molH]
			if mRec == nil {
				mRec = &molRecord{
					molKey:     fmt.Sprintf("M:%08x", uint32(molH)),
					molHash:    molH,
					text:       clipMoleculeText(compactMoleculeDisplayText(sTxt), 72),
					coreAtoms:  coreNames,
					atomEnergy: sumCoreEnergy,
					salientCnt: salientCnt,
					units:      make(map[string]bool, 2),
					cooccur:    make(map[uint64]int, 2),
				}
				molMap[molH] = mRec
			}
			mRec.units[k] = true
		}
		for i := 0; i+1 < len(orderedMolHashes); i++ {
			mA, mB := orderedMolHashes[i], orderedMolHashes[i+1]
			if mA == 0 || mB == 0 || mA == mB {
				continue
			}
			molMap[mA].cooccur[mB]++
			molMap[mB].cooccur[mA]++
		}
	}

	buildAtomParticle := func(rec *atomRecord, tf int) GraphAtomParticle {
		df := len(rec.units)
		if df < 1 {
			df = 1
		}
		en, massNats, _ := atomEnergyOf(rec.h)
		cellSpan := max(1, len(rec.cells))
		potential := en * (1.0 + 0.25*math.Log1p(float64(cellSpan)))
		weight := potential * (1.0 + 0.35*math.Log1p(float64(max(1, tf))))
		return GraphAtomParticle{
			Token:     rec.surface,
			HashHex:   fmt.Sprintf("%08x", uint32(rec.h)),
			DF:        df,
			MassNats:  math.Round(massNats*100) / 100,
			Potential: math.Round(potential*100) / 100,
			Weight:    math.Round(weight*100) / 100,
			CellSpan:  cellSpan,
			UnitSpan:  df,
		}
	}

	buildMolFragment := func(mRec *molRecord) GraphMoleculeFragment {
		df := len(mRec.units)
		if df < 1 {
			df = 1
		}
		massNats := math.Log(1.0 + nDocs/float64(df))
		gamma := math.Max(0.15, math.Min(1.0, 1.0/math.Sqrt(float64(df))))
		// 分子联合惊奇度能量 E_mol(M) = γ_mol(M) * Σ e(h) * (k_salient / (k_salient + e))
		synergyFactor := float64(mRec.salientCnt) / (float64(mRec.salientCnt) + math.E)
		energyNats := gamma * mRec.atomEnergy * (0.65 + 0.85*synergyFactor)

		var peers []string
		for nbH := range mRec.cooccur {
			if nbRec := molMap[nbH]; nbRec != nil && len(peers) < 3 {
				peers = append(peers, clipRunes(nbRec.text, 32))
			}
		}
		return GraphMoleculeFragment{
			MolKey:     mRec.molKey,
			Text:       mRec.text,
			DF:         df,
			MassNats:   math.Round(massNats*100) / 100,
			EnergyNats: math.Round(energyNats*100) / 100,
			Gamma:      math.Round(gamma*100) / 100,
			CoreAtoms:  mRec.coreAtoms,
			DimerPeers: peers,
			UnitSpan:   df,
		}
	}

	for _, k := range keyOrder {
		n := nodesByKey[k]
		if n == nil || n.Kind != "unit" {
			continue
		}
		uCounts := unitAtomCounts[k]
		if len(uCounts) > 0 {
			cands := make([]GraphAtomParticle, 0, len(uCounts))
			for h, tf := range uCounts {
				if rec := atomMap[h]; rec != nil {
					cands = append(cands, buildAtomParticle(rec, tf))
				}
			}
			sort.Slice(cands, func(i, j int) bool {
				if cands[i].Weight != cands[j].Weight {
					return cands[i].Weight > cands[j].Weight
				}
				return cands[i].Token < cands[j].Token
			})
			if len(cands) > 6 {
				cands = cands[:6]
			}
			n.Atoms = cands
		}
		mList := unitMols[k]
		if len(mList) > 0 {
			mCands := make([]GraphMoleculeFragment, 0, len(mList))
			for _, mh := range mList {
				if mRec := molMap[mh]; mRec != nil {
					mCands = append(mCands, buildMolFragment(mRec))
				}
			}
			sort.SliceStable(mCands, func(i, j int) bool {
				return mCands[i].EnergyNats > mCands[j].EnergyNats
			})
			if len(mCands) > 4 {
				mCands = mCands[:4]
			}
			n.Molecules = mCands
		}
	}

	for _, k := range keyOrder {
		n := nodesByKey[k]
		if n == nil || n.Kind != "cell" {
			continue
		}
		members := cellMembers[k]
		cellAtomTF := make(map[uint64]int, 32)
		seenMol := make(map[uint64]bool, 16)
		var cellMolCands []GraphMoleculeFragment
		for _, m := range members {
			for h, tf := range unitAtomCounts[m.NodeKey] {
				cellAtomTF[h] += tf
			}
			for _, mh := range unitMols[m.NodeKey] {
				if !seenMol[mh] {
					seenMol[mh] = true
					if mRec := molMap[mh]; mRec != nil {
						cellMolCands = append(cellMolCands, buildMolFragment(mRec))
					}
				}
			}
		}
		if len(cellAtomTF) > 0 {
			cands := make([]GraphAtomParticle, 0, len(cellAtomTF))
			for h, tf := range cellAtomTF {
				if rec := atomMap[h]; rec != nil {
					cands = append(cands, buildAtomParticle(rec, tf))
				}
			}
			sort.Slice(cands, func(i, j int) bool {
				if cands[i].Weight != cands[j].Weight {
					return cands[i].Weight > cands[j].Weight
				}
				return cands[i].Token < cands[j].Token
			})
			if len(cands) > 8 {
				cands = cands[:8]
			}
			n.Atoms = cands
		}
		if len(cellMolCands) > 0 {
			sort.SliceStable(cellMolCands, func(i, j int) bool {
				return cellMolCands[i].EnergyNats > cellMolCands[j].EnergyNats
			})
			if len(cellMolCands) > 4 {
				cellMolCands = cellMolCands[:4]
			}
			n.Molecules = cellMolCands
		}
	}

	allAtoms := make([]GraphAtomParticle, 0, len(atomMap))
	for _, rec := range atomMap {
		allAtoms = append(allAtoms, buildAtomParticle(rec, len(rec.units)))
	}
	sort.Slice(allAtoms, func(i, j int) bool {
		if allAtoms[i].Potential != allAtoms[j].Potential {
			return allAtoms[i].Potential > allAtoms[j].Potential
		}
		return allAtoms[i].Token < allAtoms[j].Token
	})
	if len(allAtoms) > 16 {
		topAtoms = allAtoms[:16]
	} else {
		topAtoms = allAtoms
	}

	allMols := make([]GraphMoleculeFragment, 0, len(molMap))
	for _, mRec := range molMap {
		dimerTotal += len(mRec.cooccur)
		allMols = append(allMols, buildMolFragment(mRec))
	}
	dimerTotal /= 2
	sort.Slice(allMols, func(i, j int) bool {
		if allMols[i].EnergyNats != allMols[j].EnergyNats {
			return allMols[i].EnergyNats > allMols[j].EnergyNats
		}
		return allMols[i].MolKey < allMols[j].MolKey
	})
	if len(allMols) > 12 {
		topMols = allMols[:12]
	} else {
		topMols = allMols
	}

	return len(atomMap), len(molMap), dimerTotal, topAtoms, topMols
}

// appendVisibleParticleNodesAndEdges 从当前已入选画布的 Cell / Unit 节点中提炼出最具代表性的高能枢纽原子节点（kind="atom"）
// 与共价分子波包节点（kind="molecule"），直接加入画布 nodes 与 edges（atom_of / molecule_of），让 3D 星云画布直接渲染出原子与分子粒子。
func appendVisibleParticleNodesAndEdges(
	nodes []GraphNodeItem,
	edges []GraphEdgeItem,
	degByKey map[string]int,
	relCounter map[string]int,
	relLayer map[string]string,
) ([]GraphNodeItem, []GraphEdgeItem) {
	if len(nodes) == 0 {
		return nodes, edges
	}
	type atomCand struct {
		particle GraphAtomParticle
		hosts    []*GraphNodeItem
	}
	type molCand struct {
		fragment GraphMoleculeFragment
		hosts    []*GraphNodeItem
	}
	atomHosts := make(map[string]*atomCand, 64)
	var atomOrder []string
	molHosts := make(map[string]*molCand, 64)
	var molOrder []string

	for i := range nodes {
		n := &nodes[i]
		if n.Kind != "unit" && n.Kind != "cell" {
			continue
		}
		for aIdx, a := range n.Atoms {
			if aIdx >= 3 || a.Token == "" {
				break
			}
			ac := atomHosts[a.Token]
			if ac == nil {
				ac = &atomCand{particle: a}
				atomHosts[a.Token] = ac
				atomOrder = append(atomOrder, a.Token)
			}
			if len(ac.hosts) < 6 {
				ac.hosts = append(ac.hosts, n)
			}
		}
		if n.Kind == "unit" {
			for mIdx, m := range n.Molecules {
				if mIdx >= 1 || m.MolKey == "" {
					break
				}
				mc := molHosts[m.MolKey]
				if mc == nil {
					mc = &molCand{fragment: m}
					molHosts[m.MolKey] = mc
					molOrder = append(molOrder, m.MolKey)
				}
				if len(mc.hosts) < 4 {
					mc.hosts = append(mc.hosts, n)
				}
			}
		}
	}

	sort.SliceStable(atomOrder, func(i, j int) bool {
		ai, aj := atomHosts[atomOrder[i]], atomHosts[atomOrder[j]]
		if len(ai.hosts) != len(aj.hosts) {
			return len(ai.hosts) > len(aj.hosts)
		}
		return ai.particle.Potential > aj.particle.Potential
	})
	sort.SliceStable(molOrder, func(i, j int) bool {
		mi, mj := molHosts[molOrder[i]], molHosts[molOrder[j]]
		if mi.fragment.EnergyNats != mj.fragment.EnergyNats {
			return mi.fragment.EnergyNats > mj.fragment.EnergyNats
		}
		return len(mi.hosts) > len(mj.hosts)
	})

	maxAtomNodes := 24
	if len(atomOrder) < maxAtomNodes {
		maxAtomNodes = len(atomOrder)
	}
	maxMolNodes := 16
	if len(molOrder) < maxMolNodes {
		maxMolNodes = len(molOrder)
	}

	for _, tok := range atomOrder[:maxAtomNodes] {
		ac := atomHosts[tok]
		if len(ac.hosts) == 0 {
			continue
		}
		aKey := "atom:" + tok
		aID := deterministicIntID(aKey)
		leadHost := ac.hosts[0]
		p := ac.particle
		spanDesc := fmt.Sprintf("一级守恒原子荷 (AtomView)\n词元: %s (#%s)\n内生信息静质量 m(h): %.2f nats\n动态激发电位 A(h): %.2f\n跨地址投影数: %d Addresses · 文档频次 DF: %d Units",
			p.Token, p.HashHex, p.MassNats, p.Potential, p.CellSpan, p.DF)
		nodes = append(nodes, GraphNodeItem{
			ID:         aID,
			NodeKey:    aKey,
			Summary:    fmt.Sprintf("⚛ %s (A=%.1f)", p.Token, p.Potential),
			Kind:       "atom",
			Layer:      leadHost.Layer,
			Certainty:  certaintyTier(0.98),
			Intention:  "atom",
			Status:     "active",
			Confidence: 0.98,
			ObservedAt: leadHost.ObservedAt,
			Anchor:     fmt.Sprintf("atom://%s#df=%d&cells=%d", p.HashHex, p.DF, p.CellSpan),
			Span:       spanDesc,
			Aliases:    []string{p.Token, p.HashHex},
			Degree:     len(ac.hosts),
			Weight:     p.Potential,
			Atoms:      []GraphAtomParticle{p},
		})
		for _, hNode := range ac.hosts {
			edgeID := len(edges) + 1
			edges = append(edges, GraphEdgeItem{
				ID:         edgeID,
				Assertion:  aKey + "->" + hNode.NodeKey,
				From:       aID,
				To:         hNode.ID,
				FromKey:    aKey,
				ToKey:      hNode.NodeKey,
				FromLabel:  p.Token,
				ToLabel:    hNode.Summary,
				Relation:   "atom_of",
				Layer:      hNode.Layer,
				Warrant:    "atom",
				Certainty:  certaintyTier(0.98),
				Confidence: 0.98,
				Status:     "active",
				ObservedAt: hNode.ObservedAt,
				Anchor:     aKey,
				Span:       spanDesc,
				Weight:     p.Weight,
			})
			degByKey[aKey]++
			degByKey[hNode.NodeKey]++
			relCounter["atom_of"]++
			if relLayer["atom_of"] == "" {
				relLayer["atom_of"] = hNode.Layer
			}
		}
	}

	for _, mk := range molOrder[:maxMolNodes] {
		mc := molHosts[mk]
		if len(mc.hosts) == 0 {
			continue
		}
		mKey := "mol:" + mk
		mID := deterministicIntID(mKey)
		leadHost := mc.hosts[0]
		f := mc.fragment
		spanDesc := fmt.Sprintf("二级共价分子波包 (MoleculeView)\n分子标识: %s\n命题波包: %s\n内生静质量 m(M): %.2f nats · 压缩传导率 γ: %.2f · 频次 DF: %d\n共价核心原子: %s",
			f.MolKey, f.Text, f.MassNats, f.Gamma, f.DF, strings.Join(f.CoreAtoms, " · "))
		nodes = append(nodes, GraphNodeItem{
			ID:         mID,
			NodeKey:    mKey,
			Summary:    fmt.Sprintf("◈ %s", clipRunes(f.Text, 24)),
			Kind:       "molecule",
			Layer:      leadHost.Layer,
			Certainty:  certaintyTier(0.96),
			Intention:  "molecule",
			Status:     "active",
			Confidence: 0.96,
			ObservedAt: leadHost.ObservedAt,
			Anchor:     f.MolKey,
			Span:       spanDesc,
			Aliases:    append([]string{f.MolKey}, f.CoreAtoms...),
			Degree:     len(mc.hosts),
			Weight:     f.MassNats,
			Molecules:  []GraphMoleculeFragment{f},
		})
		for _, hNode := range mc.hosts {
			edgeID := len(edges) + 1
			edges = append(edges, GraphEdgeItem{
				ID:         edgeID,
				Assertion:  mKey + "->" + hNode.NodeKey,
				From:       mID,
				To:         hNode.ID,
				FromKey:    mKey,
				ToKey:      hNode.NodeKey,
				FromLabel:  f.MolKey,
				ToLabel:    hNode.Summary,
				Relation:   "molecule_of",
				Layer:      hNode.Layer,
				Warrant:    "molecule",
				Certainty:  certaintyTier(0.96),
				Confidence: 0.96,
				Status:     "active",
				ObservedAt: hNode.ObservedAt,
				Anchor:     f.MolKey,
				Span:       spanDesc,
				Weight:     f.MassNats,
			})
			degByKey[mKey]++
			degByKey[hNode.NodeKey]++
			relCounter["molecule_of"]++
			if relLayer["molecule_of"] == "" {
				relLayer["molecule_of"] = hNode.Layer
			}
		}
	}

	return nodes, edges
}

// ==================== 实时命令行终端（BuildAdmin Terminal 风格） ====================

// TerminalRequest 实时命令行请求（stream 实时日志流 | exec 执行诊断命令）。
type TerminalRequest struct {
	Action  string `json:"action"`  // stream | exec
	Command string `json:"command"` // status | health | report | recent | conflicts | nodes
	Limit   int    `json:"limit"`
}

// TerminalStreamLine 实时命令行单行日志。
type TerminalStreamLine struct {
	ID       string `json:"id"`
	TimeText string `json:"time_text"`
	Level    string `json:"level"` // info | success | warning | primary
	Tag      string `json:"tag"`   // HOOK:recall | HOOK:close | ASSERT | ENGINE
	Text     string `json:"text"`
}

// TerminalResponse 实时命令行终端响应。
type TerminalResponse struct {
	Online      bool                 `json:"online"`
	Version     string               `json:"version"`
	Mode        string               `json:"mode"`
	Scope       string               `json:"scope"`
	BinaryPath  string               `json:"binary_path"`
	DBPath      string               `json:"db_path"`
	UpdatedAt   string               `json:"updated_at"`
	Command     string               `json:"command"`
	ExitCode    int                  `json:"exit_code"`
	OutputLines []string             `json:"output_lines"`
	StreamLines []TerminalStreamLine `json:"stream_lines"`
}

func terminalHandler(c *gin.Context) {
	var req TerminalRequest
	if !bindJSON(c, &req) {
		return
	}
	home, _ := os.UserHomeDir()
	httpx.Success(c, httpx.WithData(handleTerminalAction(home, req)))
}

func handleTerminalAction(home string, req TerminalRequest) TerminalResponse {
	action := strings.ToLower(strings.TrimSpace(req.Action))
	if action == "" {
		action = "stream"
	}
	limit := req.Limit
	if limit <= 0 {
		limit = 40
	}
	if limit > 120 {
		limit = 120
	}

	widgetMu.RLock()
	wEnabled := widgetEnabled
	widgetMu.RUnlock()
	switches := parseConfigSwitches(filepath.Join(home, ".config", "rolling-memory", "config.toml"), wEnabled)
	dbPath := resolveMemoryDBPath(home)
	bin := resolveBinary(home)
	logPath := filepath.Join(home, ".local", "state", "rolling-memory", "rolling-memory.log")

	resp := TerminalResponse{
		Online:      bin != "",
		Version:     liveSoftwareVersion(),
		Mode:        switches.Mode,
		Scope:       UserScope,
		BinaryPath:  bin,
		DBPath:      dbPath,
		UpdatedAt:   time.Now().Format("15:04:05"),
		Command:     req.Command,
		ExitCode:    0,
		OutputLines: []string{},
		StreamLines: buildTerminalStreamLines(dbPath, logPath, limit),
	}

	// 未知动作 fail-closed：早期实现把任意未知取值静默降级为只读 stream，调用方拿到的是
	// 一份看起来正常的监控流而非错误提示；此处显式回报错误动作。
	if action != "stream" && action != "exec" {
		resp.ExitCode = 1
		resp.OutputLines = []string{"[ERROR] 未知终端动作: " + req.Action + "（仅支持 stream / exec）"}
		return resp
	}

	if action == "exec" {
		cmdKey := strings.TrimSpace(req.Command)
		if bin == "" {
			resp.ExitCode = 1
			resp.OutputLines = []string{"[ERROR] rolling-memory binary not found in PATH or ~/.local/bin"}
			return resp
		}
		var args []string
		args, cmdKey = terminalExecArgs(cmdKey, dbPath)
		resp.Command = "rolling-memory " + strings.Join(args, " ")
		termCmd := exec.Command(bin, args...)
		// 终端子命令同样只读本地明文金库（子进程零云端覆盖变量）。
		termCmd.Env = stripCloudBrainEnv(os.Environ())
		out, err := termCmd.CombinedOutput()
		if err != nil {
			resp.ExitCode = 1
		}
		rawStr := strings.TrimSpace(string(out))
		if rawStr == "" {
			resp.OutputLines = []string{"(no output)"}
		} else {
			for _, line := range strings.Split(rawStr, "\n") {
				resp.OutputLines = append(resp.OutputLines, line)
			}
		}
	}

	return resp
}

// terminalExecArgs 把 Live Console 的命令键映射到现役 CLI 子命令参数（未知键回落 status）。
// 契约：命令键只允许现行子命令——govern / conflicts / nodes 已从引擎停用，
// 保留旧键会让终端点出 usage 报错（前端显示「执行失败」）。
func terminalExecArgs(cmdKey, dbPath string) ([]string, string) {
	switch strings.TrimSpace(cmdKey) {
	case "doctor":
		return []string{"doctor", "--db", dbPath, "--json"}, "doctor"
	case "check":
		return []string{"check", "--db", dbPath, "--json"}, "check"
	case "recent":
		return []string{"recent", "--db", dbPath, "--limit", "10", "--json"}, "recent"
	case "prune":
		// 只读预演（分类 + 可重锚计数）；执行回收与自愈走「回收与自愈」入口的显式确认路径，
		// 终端不提供破坏性一键命令。
		return []string{"sync", "prune", "--db", dbPath, "--json"}, "sync prune"
	default:
		return []string{"status", "--db", dbPath, "--json"}, "status"
	}
}

func buildTerminalStreamLines(dbPath, logPath string, limit int) []TerminalStreamLine {
	events, _ := readHookEvents(logPath, limit)
	lines := make([]TerminalStreamLine, 0, len(events)+6)

	// 先插入最近 4 条写入的记忆单元命令行回显
	if rows, err := sqliteQueryJSON(dbPath, `
		SELECT
			doc_id AS id,
			COALESCE(layer, 'episodic') AS layer,
			occurred_at AS ts,
			address_uri AS addr,
			raw_text AS txt
		FROM rm_units
		ORDER BY occurred_at DESC LIMIT 4;
	`); err == nil && len(rows) > 0 {
		for _, r := range rows {
			ts := int64(asInt(r["ts"]))
			tText := time.Now().Format("15:04:05")
			if ts > 0 {
				tText = time.Unix(ts, 0).Format("15:04:05")
			}
			id := asString(r["id"])
			// 终端回显必须展示水合后的正文（守护进程优先），不得把存储的 `@ref:` 指针原样打印。
			rendered := clipRunes(strings.TrimSpace(hydrateVaultTextForDisplay(dbPath, id, asString(r["txt"]))), 72)
			lines = append(lines, TerminalStreamLine{
				ID:       "unit-" + id,
				TimeText: tText,
				Level:    "success",
				Tag:      "RAME:INGEST",
				Text:     fmt.Sprintf("id=%s layer=%s addr=%s | %s", id, asString(r["layer"]), asString(r["addr"]), rendered),
			})
		}
	}

	for idx, ev := range events {
		lvl := "info"
		switch ev.Outcome {
		case "injected", "injected+nudge", "written":
			lvl = "success"
		case "dedup", "replayed":
			lvl = "primary"
		case "absent":
			lvl = "warning"
		}
		text := fmt.Sprintf("outcome=%s", ev.Outcome)
		if ev.Event == "recall" {
			text = fmt.Sprintf("outcome=%-14s bands=%d raw=%d chars=%-4d latency=%dms", ev.Outcome, ev.Bands, ev.Raw, ev.Chars, ev.ElapsedMS)
		} else if ev.Event == "close" || ev.Event == "start" {
			text = fmt.Sprintf("outcome=%-14s imported=%d replayed=%d written=%d", ev.Outcome, ev.Imported, ev.Replayed, ev.Written)
		}
		lines = append(lines, TerminalStreamLine{
			ID:       fmt.Sprintf("hook-%d-%d", ev.Timestamp, idx),
			TimeText: ev.TimeText,
			Level:    lvl,
			Tag:      "HOOK:" + ev.Event,
			Text:     text,
		})
	}
	return lines
}
