// plugin: rolling-memory v0.0.58
package rollingmemory

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ai-go-hub/ai-go-admin/internal/kit/httpx"
	"github.com/gin-gonic/gin"
)

type roiFactsRequest struct {
	Scope   string `json:"scope"`
	Harness string `json:"harness"`
	Limit   int    `json:"limit"`
}

type roiFactEvent struct {
	ObservedAt  int64  `json:"observed_at"`
	Action      string `json:"action"`
	Outcome     string `json:"outcome"`
	Observation string `json:"observation"`
	Harness     string `json:"harness"`
	Hook        string `json:"hook"`
	TextBytes   *int64 `json:"text_bytes"`
}

type roiFactCounts struct {
	Total             int64            `json:"total"`
	Actions           map[string]int64 `json:"actions"`
	DecisionOutcomes  map[string]int64 `json:"decision_outcomes"`
	OutputOutcomes    map[string]int64 `json:"output_outcomes"`
	RecallOutcomes    map[string]int64 `json:"recall_outcomes"`
	KnownTextBytes    int64            `json:"known_text_bytes"`
	KnownByteEvents   int64            `json:"known_byte_events"`
	UnknownByteEvents int64            `json:"unknown_byte_events"`
}

// roiFactWindow aggregates settled ledger events and live vault activity into one rolling window.
type roiFactWindow struct {
	Key                 string           `json:"key"`
	Seconds             int64            `json:"seconds"`
	SinceUnix           int64            `json:"since_unix"`
	Total               int64            `json:"total"`
	Actions             map[string]int64 `json:"actions"`
	RecallOutcomes      map[string]int64 `json:"recall_outcomes"`
	KnownTextBytes      int64            `json:"known_text_bytes"`
	UnknownByteEvents   int64            `json:"unknown_byte_events"`
	EpisodicAdded       int64            `json:"episodic_added"`
	BackgroundAdded     int64            `json:"background_added"`
	NoeticCrystallized  int64            `json:"noetic_crystallized"`
	CitationLinks       int64            `json:"citation_links"`
	UniqueCitedUnits    int64            `json:"unique_cited_units"`
	DeliveredTokens     int64            `json:"delivered_tokens"`
	AuditedRecalls      int64            `json:"audited_recalls"`
	AdoptedRecalls      int64            `json:"adopted_recalls"`
	AdoptedToolCount    int64            `json:"adopted_tool_count"`
	AdoptedContextCount int64            `json:"adopted_context_count"`
	AdoptionRatePct     float64          `json:"adoption_rate_pct"`
}

// roiDailyBucket tracks daily 3-layer ingestion, L3 crystallization, and citation activity.
type roiDailyBucket struct {
	Date            string `json:"date"`
	EpisodicCount   int64  `json:"episodic_count"`
	BackgroundCount int64  `json:"background_count"`
	NoeticCount     int64  `json:"noetic_count"`
	CitationLinks   int64  `json:"citation_links"`
	DeliveredTokens int64  `json:"delivered_tokens"`
}

// roiUnitCitation is one recorded recall that cited a unit, with the full query
// and the crystallized answer so the provenance drawer never shows a truncated row.
type roiUnitCitation struct {
	NoeticID        string   `json:"noetic_id"`
	QueryText       string   `json:"query_text"`
	AnswerText      string   `json:"answer_text"`
	OccurredAt      int64    `json:"occurred_at"`
	AssistantID     string   `json:"assistant_id,omitempty"`
	AdoptionOutcome string   `json:"adoption_outcome,omitempty"`
	MatchedAnchors  []string `json:"matched_anchors,omitempty"`
}

// roiTopCitedUnit ranks underlying L1/L2 memory units by how often L3 Noetic recalls cite them.
type roiTopCitedUnit struct {
	UnitID        string            `json:"unit_id"`
	Layer         string            `json:"layer"`
	Locus         string            `json:"locus"`
	AddressURI    string            `json:"address_uri"`
	WaveformBar   string            `json:"waveform_bar"`
	CitationCount int64             `json:"citation_count"`
	AdoptedCount  int64             `json:"adopted_count"`
	LastCitedAt   int64             `json:"last_cited_at"`
	OccurredAt    int64             `json:"occurred_at"`
	Preview       string            `json:"preview"`
	FullText      string            `json:"full_text"`
	IsHydrated    bool              `json:"is_hydrated"`
	Tokens        int64             `json:"tokens"`
	IsZeroCopyRef bool              `json:"is_zero_copy_ref"`
	RecentQueries []string          `json:"recent_queries"`
	Citations     []roiUnitCitation `json:"citations"`
}

// roiCitedUnitDetail is one cited unit expanded for the L3 evidence drawer.
type roiCitedUnitDetail struct {
	UnitID        string `json:"unit_id"`
	Layer         string `json:"layer"`
	Locus         string `json:"locus"`
	AddressURI    string `json:"address_uri"`
	StoredText    string `json:"stored_text"`
	FullText      string `json:"full_text"`
	IsHydrated    bool   `json:"is_hydrated"`
	IsZeroCopyRef bool   `json:"is_zero_copy_ref"`
}

// roiLocusRow summarizes per-workspace (locus) memory coverage and recall citation heat.
type roiLocusRow struct {
	Locus           string `json:"locus"`
	EpisodicCount   int64  `json:"episodic_count"`
	BackgroundCount int64  `json:"background_count"`
	CitedUnitCount  int64  `json:"cited_unit_count"`
	CitationHits    int64  `json:"citation_hits"`
	AdoptedHits     int64  `json:"adopted_hits"`
	LastActiveAt    int64  `json:"last_active_at"`
	Status          string `json:"status"` // "hot" | "warm" | "cold"
}

// roiNoeticEvent represents one crystallized L3 Noetic retrieval handle and its cited evidence.
type roiNoeticEvent struct {
	NoeticID          string               `json:"noetic_id"`
	OccurredAt        int64                `json:"occurred_at"`
	IngestAt          int64                `json:"-"`
	Status            string               `json:"status"`
	QueryText         string               `json:"query_text"`
	AnswerText        string               `json:"answer_text"`
	CitedUnitIDs      []string             `json:"cited_unit_ids"`
	CitedSentences    []string             `json:"cited_sentences"`
	CitedUnits        []roiCitedUnitDetail `json:"cited_units"`
	CitationCount     int                  `json:"citation_count"`
	DeliveredTokens   int64                `json:"delivered_tokens"`
	AddressURI        string               `json:"address_uri"`
	WaveformBar       string               `json:"waveform_bar"`
	AssistantID       string               `json:"assistant_id"`
	IncarnationID     string               `json:"incarnation_id"`
	AdoptionOutcome   string               `json:"adoption_outcome"`
	MatchedAnchors    []string             `json:"matched_anchors"`
	EchoExcludedCount int                  `json:"echo_excluded_count"`
}

// roiFactsVault reports what the selected memory database currently holds, along
// with physical tokenz-r distillation compression and cross-session citation reuse.
type roiFactsVault struct {
	SchemaEra               string  `json:"schema_era"`
	TotalUnits              int64   `json:"total_units"`
	BackgroundDirs          int64   `json:"background_dirs"`
	BackgroundFiles         int64   `json:"background_files"`
	BackgroundChunks        int64   `json:"background_chunks"`
	BackgroundBytes         int64   `json:"background_bytes"`
	EpisodicDocs            int64   `json:"episodic_docs"`
	EpisodicPointers        int64   `json:"episodic_pointers"`
	EpisodicText            int64   `json:"episodic_text"`
	EpisodicPointerRatioPct float64 `json:"episodic_pointer_ratio_pct"`
	EpisodicCells           int64   `json:"episodic_cells"`
	NoeticUnits             int64   `json:"noetic_units"`
	NoeticStale             int64   `json:"noetic_stale"`
	NoeticWithCitations     int64   `json:"noetic_with_citations"`
	TotalCitationLinks      int64   `json:"total_citation_links"`
	UniqueCitedUnits        int64   `json:"unique_cited_units"`
	ReusedUnitsCount        int64   `json:"reused_units_count"`
	AvgReuseMultiplier      float64 `json:"avg_reuse_multiplier"`
	MaxReuseCount           int64   `json:"max_reuse_count"`
	AvgCitationsPerRecall   float64 `json:"avg_citations_per_recall"`
	TotalDeliveredTokens    int64   `json:"total_delivered_tokens"`
	AvgDeliveredTokens      int64   `json:"avg_delivered_tokens"`
	InlineRawTokens         int64   `json:"inline_raw_tokens"`
	InlineDistilledTokens   int64   `json:"inline_distilled_tokens"`
	DistillationSavedPct    float64 `json:"distillation_saved_pct"`
	TodayEpisodic           int64   `json:"today_episodic"`
	TodayBackground         int64   `json:"today_background"`
	TodayNoetic             int64   `json:"today_noetic"`
	TodayCitations          int64   `json:"today_citations"`
	InjectionLedgerRows     int64   `json:"injection_ledger_rows"`
	AuditedRecalls          int64   `json:"audited_recalls"`
	AdoptedRecalls          int64   `json:"adopted_recalls"`
	AdoptedToolCount        int64   `json:"adopted_tool_count"`
	AdoptedContextCount     int64   `json:"adopted_context_count"`
	EchoExcludedTotal       int64   `json:"echo_excluded_total"`
	AdoptionRatePct         float64 `json:"adoption_rate_pct"`
}

type roiFactsResponse struct {
	Status         string            `json:"status"`
	Reason         string            `json:"reason,omitempty"`
	Source         string            `json:"source"`
	Coverage       string            `json:"coverage"`
	IdentityStatus string            `json:"identity_status"`
	Scope          string            `json:"scope"`
	Harness        string            `json:"harness"`
	Counts         *roiFactCounts    `json:"counts"`
	Windows        []roiFactWindow   `json:"windows"`
	VaultWindows   []roiFactWindow   `json:"vault_windows"`
	Vault          *roiFactsVault    `json:"vault"`
	DailySeries    []roiDailyBucket  `json:"daily_series"`
	TopCitedUnits  []roiTopCitedUnit `json:"top_cited_units"`
	LocusMatrix    []roiLocusRow     `json:"locus_matrix"`
	NoeticStream   []roiNoeticEvent  `json:"noetic_stream"`
	RecentEvents   []roiFactEvent    `json:"recent_events"`
}

// roiFactWindows is the fixed rolling-window set the monitor reports.
var roiFactWindows = []struct {
	Key     string
	Seconds int64
}{
	{Key: "1h", Seconds: 3600},
	{Key: "24h", Seconds: 86400},
	{Key: "7d", Seconds: 7 * 86400},
	{Key: "30d", Seconds: 30 * 86400},
}

func roiFactsUnknown(scope, harness, reason string) roiFactsResponse {
	return roiFactsResponse{
		Status:         "unknown",
		Reason:         reason,
		Source:         "admin_selected_db",
		Coverage:       "unknown",
		IdentityStatus: "unknown",
		Scope:          scope,
		Harness:        harness,
		Windows:        []roiFactWindow{},
		VaultWindows:   []roiFactWindow{},
		DailySeries:    []roiDailyBucket{},
		TopCitedUnits:  []roiTopCitedUnit{},
		LocusMatrix:    []roiLocusRow{},
		NoeticStream:   []roiNoeticEvent{},
		RecentEvents:   []roiFactEvent{},
	}
}

func roiFactsHandler(c *gin.Context) {
	var req roiFactsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Fail(c, httpx.WithMessage("参数格式错误"))
		return
	}
	home, err := os.UserHomeDir()
	if err != nil {
		httpx.Success(c, httpx.WithData(roiFactsUnknown(req.Scope, req.Harness, "database_unavailable")))
		return
	}
	dbPath := resolveMemoryDBPath(home)
	triggerProductionRecallLedgerSync(home, dbPath)
	resp := queryROIFacts(c.Request.Context(), dbPath, req)
	if resp.Status == "available" && resp.IdentityStatus != "verified" {
		resp.Status, resp.Reason, resp.Coverage, resp.IdentityStatus = "unknown", "ledger_identity_unverified", "unknown", "unknown"
		resp.Counts, resp.Windows, resp.RecentEvents = nil, []roiFactWindow{}, []roiFactEvent{}
	}
	httpx.Success(c, httpx.WithData(resp))
}

// bytesToTokenzR converts UTF-8 delivered recall payload bytes into tokenz-r
// tokens using the measured 128k Byte-Level BPE density on <rolling-memory-recall>
// bilingual outputs (0.24 token/byte).
func bytesToTokenzR(bytes int64) int64 {
	if bytes <= 0 {
		return 0
	}
	tok := int64(float64(bytes)*0.24 + 0.5)
	if tok < 1 {
		tok = 1
	}
	return tok
}

// 零拷贝指针水合的进程内缓存与后台预热。
//
// 宿主库（~/.zcode/cli/db/db.sqlite）由外部进程持续读写、体量在 GB 级，而控制台面板每 5s 轮询一轮、
// 同一批 `@ref:` 指针反复出现：在请求路径上逐指针读取宿主库会把读接口堆死（实测并发 /dashboard
// 全部停在宿主库 WAL 读上）。因此请求只读缓存（含空结果负缓存），未命中排队后台单飞预热，
// 本轮预览回退到干句（调用方已有该回退）。
const (
	hydrateCacheTTL = 6 * time.Hour
	// hydrateCacheMaxKeys 需容纳本地金库的全部零拷贝指针（当前生产库已达 ~2×10^4 行且持续增长）：
	// 旧值 16384 在指针数增至 19,682 后于单次全表扫描中途触发整表清空，造成 100% 缓存抖动。
	// 现扩容至 131072 并采用渐进淘汰（先清过期、再按需淘汰 1/4），消除跨尺度悬崖。
	hydrateCacheMaxKeys = 131072
	hydrateQueueDepth   = 32
	// hydrateFlavorRaw 纯对话正文（ROI 抽屉的零拷贝指针水合）；
	// hydrateFlavorLive 带角色前缀的列表正文（三层列表的 Harness 原位读）；
	// hydrateFlavorLocalDoc L1 背景分块的现场正文（守护进程 /v1/documents 解析结果）。
	// 三者格式不同，缓存按口径分键，绝不混用。
	hydrateFlavorRaw      = "raw"
	hydrateFlavorLive     = "live"
	hydrateFlavorLocalDoc = "localdoc"

	// localDocCacheTTL 有意短于指针水合缓存：L1 分块正文是源文件磁盘现场的重读重切结果，
	// 文件随时可能被编辑或删除，展示面应尽快反映现场，而不是长时间回放旧文。
	localDocCacheTTL = 2 * time.Minute
)

type hydrateCacheEntry struct {
	text string
	at   time.Time
}

type hydrateJob struct {
	flavor  string
	home    string
	pointer string
}

var (
	hydrateMu    sync.Mutex
	hydrateCache = map[string]hydrateCacheEntry{}
	hydrateQueue = make(chan hydrateJob, hydrateQueueDepth)
	hydrateStart sync.Once
)

func hydrateCacheKey(flavor, home, pointer string) string {
	return flavor + "\x00" + strings.TrimSpace(home) + "\x00" + strings.TrimSpace(pointer)
}

// readHydrateCache 取缓存水合结果（含空结果负缓存），过期视为未命中。
func readHydrateCache(key string) (string, bool) {
	return readHydrateCacheWithin(key, hydrateCacheTTL)
}

// readHydrateCacheWithin 以指定新鲜度窗口读取缓存：L1 分块正文按磁盘现场读取，
// 需要比指针水合更短的窗口（见 hydrateFlavorLocalDoc），键空间与淘汰规则共用同一张缓存表。
func readHydrateCacheWithin(key string, ttl time.Duration) (string, bool) {
	hydrateMu.Lock()
	entry, ok := hydrateCache[key]
	hydrateMu.Unlock()
	if !ok || time.Since(entry.at) >= ttl {
		return "", false
	}
	return entry.text, true
}

// storeHydrateCache 写入缓存；容量满时先清理过期项，仍满则淘汰 1/4（避免整表清空引发遍历期缓存雪崩）。
func storeHydrateCache(key, text string) {
	now := time.Now()
	hydrateMu.Lock()
	evictHydrateCacheLocked(now)
	hydrateCache[key] = hydrateCacheEntry{text: text, at: now}
	hydrateMu.Unlock()
}

func evictHydrateCacheLocked(now time.Time) {
	if len(hydrateCache) < hydrateCacheMaxKeys {
		return
	}
	for k, v := range hydrateCache {
		if now.Sub(v.at) >= hydrateCacheTTL {
			delete(hydrateCache, k)
		}
	}
	if len(hydrateCache) < hydrateCacheMaxKeys {
		return
	}
	target := hydrateCacheMaxKeys / 4
	removed := 0
	for k := range hydrateCache {
		delete(hydrateCache, k)
		removed++
		if removed >= target {
			break
		}
	}
}

// hydratedEpisodicPointer 只从缓存取水合结果；未命中时排队后台预热并返回 ok=false。
func hydratedEpisodicPointer(home, pointer string) (string, bool) {
	key := hydrateCacheKey(hydrateFlavorRaw, home, pointer)
	if text, ok := readHydrateCache(key); ok {
		return text, true
	}
	scheduleHydrateWarm(hydrateJob{flavor: hydrateFlavorRaw, home: home, pointer: pointer})
	return "", false
}

// hydratedEpisodicPointerLive 缓存优先、未命中同步解析一次并写入缓存：
// 三层列表的关键字检索基于水合后的正文，必须保证本轮拿到正文（不做异步回退）。
// 解析顺序 = 引擎守护进程（规范口径）→ 本地兜底扫描（守护进程不可用时）。
func hydratedEpisodicPointerLive(home, pointer string) (string, bool) {
	key := hydrateCacheKey(hydrateFlavorLive, home, pointer)
	if text, ok := readHydrateCache(key); ok {
		return text, text != ""
	}
	if texts, handled := hydrateRefsViaDaemon(home, []string{pointer}); handled {
		text := strings.TrimSpace(texts[pointer])
		// 守护进程给出的是规范判定：越界/缺失即空，按空结果负缓存，
		// 绝不用本地副本的整段合并回退把错误文本顶上来。
		storeHydrateCache(key, text)
		return text, text != ""
	}
	text := resolveAdminEpisodicRefLive(home, pointer)
	storeHydrateCache(key, text)
	return text, text != ""
}

// hydrateDaemonBatchSize 单次向守护进程请求水合的指针数上限；超出即分批。
const hydrateDaemonBatchSize = 64

// hydrateRefsViaDaemon 通过本机唯一常驻守护进程（engine.sock）批量水合零拷贝 `@ref:` 指针。
//
// 分段与清洗口径必须与摄取侧完全同源：控制台曾自研按 turn 扫描的副本，在「下一条 user 消息」
// 处就截断 turn（不管该消息是否只是 synthetic 队列通知），而规范侧的定段是「下一位带正文的
// user 消息才开新 turn」—— 副本里越界的 `#k` 会回退成整段合并文本，把同一条消息的分段行
// 渲染成重复内容。因此以守护进程的正典解析（与产品 recall / recent 同一条
// ResolveEpisodicPointerLive）为准；handled=false 表示守护进程不可用，调用方退回本地兜底扫描。
//
// 返回 map 中出现的 ref 即为守护进程已裁决（text 可能为空 = 规范判定不可用，调用方应负缓存）。
func hydrateRefsViaDaemon(home string, refs []string) (map[string]string, bool) {
	if len(refs) == 0 {
		return nil, false
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("ROLLING_MEMORY_DAEMON"))) {
	case "0", "false", "off", "no":
		return nil, false
	}
	sockPath := resolveDaemonSocketPath(home)
	if _, err := os.Stat(sockPath); err != nil {
		triggerConsoleDaemonBootstrap(home, resolveMemoryDBPath(home))
		return nil, false
	}
	// 测试二进制默认不路由守护进程（与引擎侧 ShouldUseUnixDaemon 同一不变量），
	// 除非显式指定 socket 覆盖，避免单测串到开发机上的真实实例。
	exe := filepath.Base(os.Args[0])
	if (strings.HasSuffix(exe, ".test") || strings.Contains(exe, ".test.")) && strings.TrimSpace(os.Getenv("ROLLING_MEMORY_DAEMON_SOCKET")) == "" {
		return nil, false
	}
	dbPath := resolveMemoryDBPath(home)
	out := make(map[string]string, len(refs))
	for start := 0; start < len(refs); start += hydrateDaemonBatchSize {
		end := start + hydrateDaemonBatchSize
		if end > len(refs) {
			end = len(refs)
		}
		chunk := refs[start:end]
		items := make([]map[string]string, 0, len(chunk))
		for _, ref := range chunk {
			items = append(items, map[string]string{"ref": ref})
		}
		bodyBytes, err := json.Marshal(map[string]any{"db": dbPath, "items": items})
		if err != nil {
			break
		}
		dialer := &net.Dialer{Timeout: 500 * time.Millisecond}
		client := &http.Client{
			Timeout: 5 * time.Second,
			Transport: &http.Transport{
				DialContext: func(dCtx context.Context, _, _ string) (net.Conn, error) {
					return dialer.DialContext(dCtx, "unix", sockPath)
				},
				DisableKeepAlives: true,
			},
		}
		httpReq, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://unix/v1/hydrate", bytes.NewReader(bodyBytes))
		if err != nil {
			break
		}
		httpReq.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(httpReq)
		if err != nil {
			triggerConsoleDaemonBootstrap(home, dbPath)
			break
		}
		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			break
		}
		var dResp struct {
			OK    bool `json:"ok"`
			Texts []struct {
				Ref  string `json:"ref"`
				Text string `json:"text"`
			} `json:"texts"`
		}
		rawBody, rErr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		_ = resp.Body.Close()
		if rErr != nil || json.Unmarshal(rawBody, &dResp) != nil || !dResp.OK {
			break
		}
		for _, item := range dResp.Texts {
			ref := strings.TrimSpace(item.Ref)
			if ref == "" {
				continue
			}
			out[ref] = item.Text
		}
	}
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}

// documentDaemonBatchSize 单次向守护进程请求正文解析的文档数上限；超出即分批。
// 每个 L1 分块都要现场重读重切其源文件，批量宁小勿大（守护进程侧另有 64 条硬上限）。
const documentDaemonBatchSize = 32

// localDocumentBody 是守护进程按规范口径解析出的一份本地文档正文：
// L1 背景分块 = 源文件磁盘现场重读重切 + 已存 kept 句索引重建的蒸馏句。
type localDocumentBody struct {
	raw       string
	distilled string
}

// documentsViaDaemon 通过本机唯一常驻守护进程（engine.sock）批量解析本地文档正文
// （`/v1/documents` → 产品正典 `Vault.LoadLocalDocument`）。控制台禁止导入 thinclient，
// 也不允许自研第二套文件分块副本——L1 分块的正文只能由内核现场重读重切，展示层不得
// 用路径坐标冒充正文；handled=false 表示守护进程不可用，调用方按「正文暂不可读」降级。
//
// 返回 map 中出现的 unitID 即为守护进程已裁决（raw 可能为空 = 源文件不可读/内容为空，
// 调用方应负缓存，绝不用本地副本猜内容顶上）。
func documentsViaDaemon(home string, unitIDs []string) (map[string]localDocumentBody, bool) {
	if len(unitIDs) == 0 {
		return nil, false
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("ROLLING_MEMORY_DAEMON"))) {
	case "0", "false", "off", "no":
		return nil, false
	}
	sockPath := resolveDaemonSocketPath(home)
	if _, err := os.Stat(sockPath); err != nil {
		triggerConsoleDaemonBootstrap(home, resolveMemoryDBPath(home))
		return nil, false
	}
	// 测试二进制默认不路由守护进程（与引擎侧 ShouldUseUnixDaemon 同一不变量），
	// 除非显式指定 socket 覆盖，避免单测串到开发机上的真实实例。
	exe := filepath.Base(os.Args[0])
	if (strings.HasSuffix(exe, ".test") || strings.Contains(exe, ".test.")) && strings.TrimSpace(os.Getenv("ROLLING_MEMORY_DAEMON_SOCKET")) == "" {
		return nil, false
	}
	dbPath := resolveMemoryDBPath(home)
	out := make(map[string]localDocumentBody, len(unitIDs))
	for start := 0; start < len(unitIDs); start += documentDaemonBatchSize {
		end := start + documentDaemonBatchSize
		if end > len(unitIDs) {
			end = len(unitIDs)
		}
		chunk := unitIDs[start:end]
		items := make([]map[string]string, 0, len(chunk))
		for _, unitID := range chunk {
			items = append(items, map[string]string{"unit_id": unitID})
		}
		bodyBytes, err := json.Marshal(map[string]any{"db": dbPath, "items": items})
		if err != nil {
			break
		}
		dialer := &net.Dialer{Timeout: 500 * time.Millisecond}
		client := &http.Client{
			Timeout: 5 * time.Second,
			Transport: &http.Transport{
				DialContext: func(dCtx context.Context, _, _ string) (net.Conn, error) {
					return dialer.DialContext(dCtx, "unix", sockPath)
				},
				DisableKeepAlives: true,
			},
		}
		httpReq, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://unix/v1/documents", bytes.NewReader(bodyBytes))
		if err != nil {
			break
		}
		httpReq.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(httpReq)
		if err != nil {
			triggerConsoleDaemonBootstrap(home, dbPath)
			break
		}
		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			break
		}
		var dResp struct {
			OK   bool `json:"ok"`
			Docs []struct {
				UnitID        string `json:"unit_id"`
				RawText       string `json:"raw_text"`
				DistilledText string `json:"distilled_text"`
				Error         string `json:"error"`
			} `json:"docs"`
		}
		rawBody, rErr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		_ = resp.Body.Close()
		if rErr != nil || json.Unmarshal(rawBody, &dResp) != nil || !dResp.OK {
			break
		}
		for _, item := range dResp.Docs {
			unitID := strings.TrimSpace(item.UnitID)
			if unitID == "" {
				continue
			}
			if strings.TrimSpace(item.Error) != "" {
				out[unitID] = localDocumentBody{}
				continue
			}
			out[unitID] = localDocumentBody{raw: item.RawText, distilled: item.DistilledText}
		}
	}
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}

// hydratedLocalDocumentLive 缓存优先、未命中同步向守护进程解析一次。
// L1 分块正文是磁盘现场的重读重切结果（短 TTL），且绝不自研第二套文件分块副本兜底：
// 守护进程不可用即判「正文暂不可读」，展示层回退到来源位置预览 + 如实说明。
func hydratedLocalDocumentLive(home, unitID string) (string, bool) {
	unitID = strings.TrimSpace(unitID)
	if unitID == "" {
		return "", false
	}
	key := hydrateCacheKey(hydrateFlavorLocalDoc, home, unitID)
	if text, ok := readHydrateCacheWithin(key, localDocCacheTTL); ok {
		return text, text != ""
	}
	bodies, handled := documentsViaDaemon(home, []string{unitID})
	if !handled {
		return "", false
	}
	body, answered := bodies[unitID]
	if !answered {
		return "", false
	}
	// 守护进程已裁决（含源文件不可读/内容为空）→ 负缓存，避免每轮重复打守护进程。
	text := strings.TrimSpace(body.raw)
	storeHydrateCache(key, text)
	return text, text != ""
}

// invalidateDaemonViaSocket 通知本机常驻守护进程（engine.sock）立即丢弃内存中的三层快照与分块缓存。
func invalidateDaemonViaSocket(home string) {
	if strings.TrimSpace(home) == "" {
		return
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("ROLLING_MEMORY_DAEMON"))) {
	case "0", "false", "off", "no":
		return
	}
	exe := filepath.Base(os.Args[0])
	if (strings.HasSuffix(exe, ".test") || strings.Contains(exe, ".test.")) && strings.TrimSpace(os.Getenv("ROLLING_MEMORY_DAEMON_SOCKET")) == "" {
		return
	}
	sockPath := resolveDaemonSocketPath(home)
	if _, err := os.Stat(sockPath); err != nil {
		return
	}
	dialer := &net.Dialer{Timeout: 250 * time.Millisecond}
	client := &http.Client{
		Timeout: 400 * time.Millisecond,
		Transport: &http.Transport{
			DialContext: func(dCtx context.Context, _, _ string) (net.Conn, error) {
				return dialer.DialContext(dCtx, "unix", sockPath)
			},
			DisableKeepAlives: true,
		},
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://unix/v1/invalidate", strings.NewReader(`{}`))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if resp, dErr := client.Do(req); dErr == nil && resp != nil {
		_ = resp.Body.Close()
	}
}

// warmHydrateLocalDocumentsBatch 为三层列表投影行中的 L1 背景分块（src == 'chunk'）
// 批量预热正文：一次守护进程批量解析（≤ documentDaemonBatchSize 条/批）写入缓存，
// 供 layerRowFromSQL 逐行读取，避免逐行同步打守护进程。守护进程不可用时不做任何事，
// 展示层按「正文暂不可读」降级（保留来源位置预览）。
func warmHydrateLocalDocumentsBatch(home string, rows []map[string]any) {
	if strings.TrimSpace(home) == "" || len(rows) == 0 {
		return
	}
	seen := make(map[string]struct{}, len(rows))
	unitIDs := make([]string, 0, len(rows))
	for _, r := range rows {
		if strings.ToLower(strings.TrimSpace(asString(r["src"]))) != "chunk" {
			continue
		}
		unitID := strings.TrimSpace(asString(r["id"]))
		if unitID == "" {
			continue
		}
		if _, dup := seen[unitID]; dup {
			continue
		}
		seen[unitID] = struct{}{}
		if _, ok := readHydrateCacheWithin(hydrateCacheKey(hydrateFlavorLocalDoc, home, unitID), localDocCacheTTL); ok {
			continue
		}
		unitIDs = append(unitIDs, unitID)
	}
	if len(unitIDs) == 0 {
		return
	}
	bodies, handled := documentsViaDaemon(home, unitIDs)
	if !handled {
		return
	}
	for _, unitID := range unitIDs {
		body, answered := bodies[unitID]
		if !answered {
			continue
		}
		storeHydrateCache(hydrateCacheKey(hydrateFlavorLocalDoc, home, unitID), strings.TrimSpace(body.raw))
	}
}

// hydrateZCodePointersViaDaemon 先让守护进程按规范口径批量水合 rows 中的 `@ref:` 指针：
// 已裁决的指针写入水合缓存（含空结果负缓存），返回仍需本地兜底扫描的行。
// handled=false（守护进程不可用）时原样返回全部行，交由下方既有兜底逻辑处理。
func hydrateZCodePointersViaDaemon(home string, rows []map[string]any) []map[string]any {
	const zPrefix = "@ref:zcode:"
	const jPrefix = "@ref:jsonl:"
	seen := make(map[string]struct{}, len(rows))
	refs := make([]string, 0, len(rows))
	unanswered := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		raw := strings.TrimSpace(asString(r["content"]))
		if !strings.HasPrefix(raw, zPrefix) && !strings.HasPrefix(raw, jPrefix) {
			unanswered = append(unanswered, r)
			continue
		}
		if _, dup := seen[raw]; dup {
			continue
		}
		seen[raw] = struct{}{}
		if _, ok := readHydrateCache(hydrateCacheKey(hydrateFlavorLive, home, raw)); ok {
			continue
		}
		refs = append(refs, raw)
	}
	if len(refs) == 0 {
		return nil
	}
	texts, handled := hydrateRefsViaDaemon(home, refs)
	if !handled {
		return rows
	}
	now := time.Now()
	hydrateMu.Lock()
	evictHydrateCacheLocked(now)
	for _, raw := range refs {
		text, answered := texts[raw]
		if !answered {
			unanswered = append(unanswered, map[string]any{"content": raw})
			continue
		}
		text = strings.TrimSpace(text)
		hydrateCache[hydrateCacheKey(hydrateFlavorLive, home, raw)] = hydrateCacheEntry{text: text, at: now}
		hydrateCache[hydrateCacheKey(hydrateFlavorRaw, home, raw)] = hydrateCacheEntry{text: text, at: now}
	}
	hydrateMu.Unlock()
	return unanswered
}

// scheduleHydrateWarm 把指针排进后台预热队列；队列满即丢弃（下一轮轮询会再次请求）。
func scheduleHydrateWarm(job hydrateJob) {
	hydrateStart.Do(func() { go hydrateWorkerLoop() })
	select {
	case hydrateQueue <- job:
	default:
	}
}

// hydrateWorkerLoop 单飞预热：同一时刻只读一次宿主库，避免并发读外部库互相争 WAL。
// 与前台水合同一口径：优先走引擎守护进程的规范解析（与产品 recall / recent 同一条
// 定段与清洗），守护进程不可用时才回落到本地兜底扫描。
func hydrateWorkerLoop() {
	for job := range hydrateQueue {
		text := ""
		if texts, handled := hydrateRefsViaDaemon(job.home, []string{job.pointer}); handled {
			text = strings.TrimSpace(texts[job.pointer])
		} else {
			text = hydrateEpisodicPointer(job.home, job.pointer)
		}
		storeHydrateCache(hydrateCacheKey(job.flavor, job.home, job.pointer), text)
	}
}

// zcodeSpeechMessageFilter mirrors the canonical server-side predicate
// (`internal/episodic.ZCodeTextPartFilter`): the agent's process narration — a tool
// step's text (any message that invoked a tool, whatever its finish shape) and
// harness-hidden messages (`semantics.uiVisibility:"hidden"`) — is never
// conversation. Keep this clone in lockstep with the canonical filter, or hydrated
// `#<k>` indices diverge.
const zcodeSpeechMessageFilter = `COALESCE((SELECT json_extract(am.data, '$.semantics.uiVisibility') FROM message am WHERE am.id = p.message_id), 'visible') <> 'hidden'
		  AND NOT EXISTS (SELECT 1 FROM part tp WHERE tp.message_id = p.message_id AND json_extract(tp.data, '$.type') = 'tool')`

// hydrateEpisodicPointer resolves a zero-copy `@ref:zcode:<session>:<msg>[#k]`
// or `@ref:jsonl:<abs_path>:<turn>[#k]` pointer to the FULL speech text held by
// the local harness database or transcript, so the provenance drawer and layer
// browser render complete content instead of the stored pointer.
func hydrateEpisodicPointer(home, pointer string) string {
	trimmed := strings.TrimSpace(pointer)
	if strings.HasPrefix(trimmed, "@ref:jsonl:") {
		return hydrateJSONLEpisodicPointer(home, trimmed)
	}
	const prefix = "@ref:zcode:"
	if !strings.HasPrefix(trimmed, prefix) || strings.TrimSpace(home) == "" {
		return ""
	}
	rest := strings.TrimPrefix(trimmed, prefix)
	sep := strings.Index(rest, ":")
	if sep <= 0 || sep >= len(rest)-1 {
		return ""
	}
	sessionID := rest[:sep]
	tail := rest[sep+1:]
	messageID := tail
	segmentIndex := -1
	if hash := strings.LastIndex(tail, "#"); hash > 0 && hash < len(tail)-1 {
		segPart := tail[hash+1:]
		if dot := strings.IndexByte(segPart, '.'); dot > 0 {
			segPart = segPart[:dot]
		}
		if parsed, err := strconv.Atoi(segPart); err == nil && parsed >= 0 {
			messageID = tail[:hash]
			segmentIndex = parsed
		}
	}
	// 复用按路径 + inode 缓存的只读连接池 memoryRODB：避免每次水合都 sql.Open + db.Close
	// 7.8GB 宿主库并重建 14GB WAL 的 -shm 索引（1.9 万条指针下曾耗 264s）。
	zdb, zErr := memoryRODB(filepath.Join(home, ".zcode", "cli", "db", "db.sqlite"))
	if zErr != nil || zdb == nil {
		return ""
	}
	// 两阶段查询（与 internal/episodic.ZCodeTurnSegments 同构）：
	// 1) 先只查 message 表（走 message_session_sequence_idx 范围索引，不跨表 JOIN part，
	//    消除跨表 ORDER BY 触发的 USE TEMP B-TREE 全 session 扫描），遇到下一条 user 消息即早停；
	// 2) 只对本轮 turn 的 1..N 条 message 按 message_id 查 part，并用 64B 前缀短路跳过大 tool-result。
	var startSeq sql.NullInt64
	if err := zdb.QueryRow(`SELECT sequence FROM message WHERE id = ? AND session_id = ?`, messageID, sessionID).Scan(&startSeq); err != nil {
		return ""
	}
	var mRows *sql.Rows
	var err error
	if startSeq.Valid {
		mRows, err = zdb.Query(`
			SELECT m.id, COALESCE(json_extract(m.data, '$.role'), '')
			FROM message m
			WHERE m.session_id = ? AND m.sequence >= ?
			ORDER BY m.sequence ASC
			LIMIT 32`, sessionID, startSeq.Int64)
	} else {
		mRows, err = zdb.Query(`
			SELECT m.id, COALESCE(json_extract(m.data, '$.role'), '')
			FROM message m
			WHERE m.session_id = ?
			ORDER BY m.sequence ASC
			LIMIT 32`, sessionID)
	}
	if err != nil {
		return ""
	}
	type msgHdr struct {
		id   string
		role string
	}
	hdrs := make([]msgHdr, 0, 6)
	seenTurnStart := false
	for mRows.Next() {
		var mid, role string
		if mRows.Scan(&mid, &role) != nil {
			continue
		}
		role = strings.ToLower(strings.TrimSpace(role))
		if role == "" {
			role = "unknown"
		}
		if !seenTurnStart && startSeq.Valid && mid != messageID && role != "user" {
			continue
		}
		if role == "user" {
			if !seenTurnStart {
				seenTurnStart = true
			} else if mid != messageID {
				break
			}
		} else {
			seenTurnStart = true
		}
		hdrs = append(hdrs, msgHdr{id: mid, role: role})
	}
	mRows.Close()
	if len(hdrs) == 0 {
		return ""
	}

	segments := make([]string, 0, 8)
	for _, h := range hdrs {
		pRows, pErr := zdb.Query(`
			SELECT COALESCE(json_extract(p.data, '$.text'), '')
			FROM part p
			WHERE p.message_id = ?
			  AND json_extract(p.data, '$.type') = 'text'
			  AND COALESCE(json_extract(p.data, '$.synthetic'), 0) = 0
			  AND `+zcodeSpeechMessageFilter+`
			ORDER BY p.sequence ASC
			LIMIT 64`, h.id)
		if pErr != nil {
			continue
		}
		for pRows.Next() {
			var text string
			if pRows.Scan(&text) != nil {
				continue
			}
			text = strings.TrimSpace(text)
			if text == "" {
				continue
			}
			segments = append(segments, "["+h.role+"] "+text)
		}
		pRows.Close()
	}
	if len(segments) == 0 {
		return ""
	}
	joined := strings.Join(segments, "\n")
	// 同一次查询已经取出该 turn 的全部分片：顺手把 base 指针与 #0..#k 全部写入缓存，
	// 同一轮对话的后续分片（#1, #2...）直接 O(1) 命中内存，无需再次查询宿主库。
	basePtr := prefix + sessionID + ":" + messageID
	now := time.Now()
	hydrateMu.Lock()
	evictHydrateCacheLocked(now)
	hydrateCache[hydrateCacheKey(hydrateFlavorLive, home, basePtr)] = hydrateCacheEntry{text: joined, at: now}
	hydrateCache[hydrateCacheKey(hydrateFlavorRaw, home, basePtr)] = hydrateCacheEntry{text: joined, at: now}
	for idx, seg := range segments {
		segPtr := basePtr + "#" + strconv.Itoa(idx)
		hydrateCache[hydrateCacheKey(hydrateFlavorLive, home, segPtr)] = hydrateCacheEntry{text: seg, at: now}
		hydrateCache[hydrateCacheKey(hydrateFlavorRaw, home, segPtr)] = hydrateCacheEntry{text: seg, at: now}
	}
	hydrateMu.Unlock()
	if segmentIndex >= 0 && segmentIndex < len(segments) {
		return segments[segmentIndex]
	}
	return joined
}

func parseAdminJSONLSpeakerLine(line string) (string, []string) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || !strings.HasPrefix(trimmed, "{") {
		return "", nil
	}
	var obj map[string]any
	if json.Unmarshal([]byte(trimmed), &obj) != nil || obj == nil {
		return "", nil
	}
	if isTruthy, _ := obj["isMeta"].(bool); isTruthy {
		return "", nil
	}
	if isCompact, _ := obj["isCompactSummary"].(bool); isCompact {
		return "", nil
	}
	message, _ := obj["message"].(map[string]any)
	if message == nil {
		if payload, ok := obj["payload"].(map[string]any); ok && payload != nil {
			if pType, _ := payload["type"].(string); pType == "message" || payload["role"] != nil {
				message = payload
			}
		}
	}
	if message == nil {
		message = obj
	}
	roleText, _ := message["role"].(string)
	if roleText == "" {
		roleText, _ = obj["type"].(string)
	}
	var speaker string
	switch strings.ToLower(strings.TrimSpace(roleText)) {
	case "user", "human":
		speaker = "user"
	case "assistant", "ai", "bot", "agent":
		speaker = "assistant"
	default:
		return "", nil
	}
	if speaker == "assistant" {
		// Process narration of a tool step, mirroring the canonical filter: the
		// assistant row ended with a tool call (`stop_reason:"tool_use"`).
		if stopReason, _ := message["stop_reason"].(string); strings.TrimSpace(stopReason) == "tool_use" {
			return "", nil
		}
	}
	var parts []string
	switch content := message["content"].(type) {
	case string:
		if s := strings.TrimSpace(content); s != "" {
			parts = append(parts, s)
		}
	case []any:
		for _, block := range content {
			item, ok := block.(map[string]any)
			if !ok {
				continue
			}
			blockType, _ := item["type"].(string)
			if blockType != "text" && blockType != "input_text" && blockType != "output_text" {
				continue
			}
			if value, ok := item["text"].(string); ok && strings.TrimSpace(value) != "" {
				parts = append(parts, strings.TrimSpace(value))
			}
		}
	}
	var texts []string
	for _, p := range parts {
		if strings.HasPrefix(p, "<app-context>") || strings.HasPrefix(p, "# AGENTS.md instructions for ") || strings.HasPrefix(p, "<environment_context>") {
			continue
		}
		texts = append(texts, p)
	}
	if len(texts) == 0 {
		return "", nil
	}
	return speaker, texts
}

func isAdminJSONLToolCallLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || !strings.HasPrefix(trimmed, "{") {
		return false
	}
	if !strings.Contains(trimmed, "function_call") &&
		!strings.Contains(trimmed, "custom_tool_call") &&
		!strings.Contains(trimmed, "web_search_call") &&
		!strings.Contains(trimmed, "local_shell_call") {
		return false
	}
	var obj map[string]any
	if json.Unmarshal([]byte(trimmed), &obj) != nil || obj == nil {
		return false
	}
	checkType := func(t string) bool {
		switch strings.TrimSpace(t) {
		case "function_call", "custom_tool_call", "web_search_call", "local_shell_call":
			return true
		}
		return false
	}
	if topType, _ := obj["type"].(string); checkType(topType) {
		return true
	}
	if payload, ok := obj["payload"].(map[string]any); ok && payload != nil {
		if pType, _ := payload["type"].(string); checkType(pType) {
			return true
		}
	}
	return false
}

func hydrateJSONLEpisodicPointer(home, pointer string) string {
	const prefix = "@ref:jsonl:"
	if !strings.HasPrefix(pointer, prefix) {
		return ""
	}
	rest := strings.TrimPrefix(pointer, prefix)
	lastColon := strings.LastIndex(rest, ":")
	if lastColon <= 0 || lastColon >= len(rest)-1 {
		return ""
	}
	absPath := strings.TrimSpace(rest[:lastColon])
	tail := rest[lastColon+1:]
	turnStr := tail
	segmentIndex := -1
	if hash := strings.LastIndex(tail, "#"); hash > 0 && hash < len(tail)-1 {
		segPart := tail[hash+1:]
		if dot := strings.IndexByte(segPart, '.'); dot > 0 {
			segPart = segPart[:dot]
		}
		if parsed, err := strconv.Atoi(segPart); err == nil && parsed >= 0 {
			turnStr = tail[:hash]
			segmentIndex = parsed
		}
	}
	turnIndex, err := strconv.Atoi(strings.TrimSpace(turnStr))
	if err != nil || turnIndex < 0 || absPath == "" {
		return ""
	}
	info, err := os.Stat(absPath)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 256*1024*1024 {
		return ""
	}
	raw, err := os.ReadFile(absPath)
	if err != nil {
		return ""
	}
	var turns [][]string
	current := -1
	var pendingAssist []string
	flushPending := func() {
		if current < 0 || len(pendingAssist) == 0 {
			pendingAssist = pendingAssist[:0]
			return
		}
		turns[current] = append(turns[current], pendingAssist...)
		pendingAssist = pendingAssist[:0]
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if isAdminJSONLToolCallLine(line) {
			pendingAssist = pendingAssist[:0]
			continue
		}
		speaker, texts := parseAdminJSONLSpeakerLine(line)
		if speaker == "" || len(texts) == 0 {
			continue
		}
		if speaker == "user" {
			flushPending()
			current++
			for len(turns) <= current {
				turns = append(turns, nil)
			}
		}
		if current >= 0 {
			if speaker == "assistant" {
				for _, txt := range texts {
					pendingAssist = append(pendingAssist, "["+speaker+"] "+txt)
				}
			} else {
				for _, txt := range texts {
					turns[current] = append(turns[current], "["+speaker+"] "+txt)
				}
			}
		}
	}
	flushPending()
	now := time.Now()
	hydrateMu.Lock()
	evictHydrateCacheLocked(now)
	for tIdx, segs := range turns {
		if len(segs) == 0 {
			continue
		}
		joined := strings.Join(segs, "\n")
		basePtr := prefix + absPath + ":" + strconv.Itoa(tIdx)
		hydrateCache[hydrateCacheKey(hydrateFlavorLive, home, basePtr)] = hydrateCacheEntry{text: joined, at: now}
		hydrateCache[hydrateCacheKey(hydrateFlavorRaw, home, basePtr)] = hydrateCacheEntry{text: joined, at: now}
		for sIdx, seg := range segs {
			segPtr := basePtr + "#" + strconv.Itoa(sIdx)
			hydrateCache[hydrateCacheKey(hydrateFlavorLive, home, segPtr)] = hydrateCacheEntry{text: seg, at: now}
			hydrateCache[hydrateCacheKey(hydrateFlavorRaw, home, segPtr)] = hydrateCacheEntry{text: seg, at: now}
		}
	}
	hydrateMu.Unlock()
	if turnIndex >= len(turns) || len(turns[turnIndex]) == 0 {
		return ""
	}
	if segmentIndex >= 0 && segmentIndex < len(turns[turnIndex]) {
		return turns[turnIndex][segmentIndex]
	}
	return strings.Join(turns[turnIndex], "\n")
}

func hydrateZCodeSessionBatch(home, sessionID string) {
	if strings.TrimSpace(home) == "" || strings.TrimSpace(sessionID) == "" {
		return
	}
	zdb, zErr := memoryRODB(filepath.Join(home, ".zcode", "cli", "db", "db.sqlite"))
	if zErr != nil || zdb == nil {
		return
	}
	jRows, err := zdb.Query(`
		SELECT m.id,
		       COALESCE(json_extract(m.data, '$.role'), '') AS role,
		       COALESCE(json_extract(p.data, '$.text'), '') AS txt,
		       COALESCE(p.sequence, 0) AS pseq,
		       p.rowid AS prowid
		FROM message m
		JOIN part p ON p.message_id = m.id
		WHERE m.session_id = ?
		  AND json_extract(p.data, '$.type') = 'text'
		  AND COALESCE(json_extract(p.data, '$.synthetic'), 0) = 0
		  AND `+zcodeSpeechMessageFilter+`
		ORDER BY m.sequence ASC`, sessionID)
	if err != nil {
		return
	}
	type msgHdr struct {
		id   string
		role string
	}
	type partRow struct {
		txt    string
		pseq   int64
		prowid int64
	}
	var hdrs []msgHdr
	partsByMsg := make(map[string][]string, 64)
	curMid := ""
	curRole := ""
	var curParts []partRow
	flushMsg := func() {
		if curMid == "" || len(curParts) == 0 {
			curParts = curParts[:0]
			return
		}
		if len(curParts) > 1 {
			sort.SliceStable(curParts, func(i, j int) bool {
				if curParts[i].pseq != curParts[j].pseq {
					return curParts[i].pseq < curParts[j].pseq
				}
				return curParts[i].prowid < curParts[j].prowid
			})
		}
		txts := make([]string, 0, len(curParts))
		for _, pt := range curParts {
			if pt.txt != "" {
				txts = append(txts, pt.txt)
			}
		}
		curParts = curParts[:0]
		if len(txts) > 0 {
			hdrs = append(hdrs, msgHdr{id: curMid, role: curRole})
			partsByMsg[curMid] = txts
		}
	}
	for jRows.Next() {
		var mid, role, txt string
		var pseq, prowid int64
		if jRows.Scan(&mid, &role, &txt, &pseq, &prowid) != nil {
			continue
		}
		role = strings.ToLower(strings.TrimSpace(role))
		if role != "user" && role != "assistant" {
			continue
		}
		txt = strings.TrimSpace(txt)
		if txt == "" {
			continue
		}
		if mid != curMid {
			flushMsg()
			curMid = mid
			curRole = role
		}
		curParts = append(curParts, partRow{txt: txt, pseq: pseq, prowid: prowid})
	}
	flushMsg()
	jRows.Close()
	if len(hdrs) == 0 {
		return
	}

	type turnEntry struct {
		userMsgID string
		segments  []string
	}
	var turns []turnEntry
	cur := -1
	for _, h := range hdrs {
		txts := partsByMsg[h.id]
		if len(txts) == 0 {
			continue
		}
		if h.role == "user" && (cur < 0 || turns[cur].userMsgID != h.id) {
			turns = append(turns, turnEntry{userMsgID: h.id})
			cur = len(turns) - 1
		}
		if cur >= 0 {
			for _, t := range txts {
				turns[cur].segments = append(turns[cur].segments, "["+h.role+"] "+t)
			}
		}
	}
	now := time.Now()
	hydrateMu.Lock()
	evictHydrateCacheLocked(now)
	for _, tr := range turns {
		if len(tr.segments) == 0 {
			continue
		}
		joined := strings.Join(tr.segments, "\n")
		basePtr := "@ref:zcode:" + sessionID + ":" + tr.userMsgID
		hydrateCache[hydrateCacheKey(hydrateFlavorLive, home, basePtr)] = hydrateCacheEntry{text: joined, at: now}
		hydrateCache[hydrateCacheKey(hydrateFlavorRaw, home, basePtr)] = hydrateCacheEntry{text: joined, at: now}
		for sIdx, seg := range tr.segments {
			segPtr := basePtr + "#" + strconv.Itoa(sIdx)
			hydrateCache[hydrateCacheKey(hydrateFlavorLive, home, segPtr)] = hydrateCacheEntry{text: seg, at: now}
			hydrateCache[hydrateCacheKey(hydrateFlavorRaw, home, segPtr)] = hydrateCacheEntry{text: seg, at: now}
		}
	}
	hydrateMu.Unlock()
}

// warmHydrateZCodePointersBatch 按 session/文件聚合尚未进入缓存的 @ref:zcode: 与 @ref:jsonl: 指针，
// 对多轮同会话直接按 session 一次扫描并并发预热，使全表水合检索保持极速响应。
// 优先走引擎守护进程的规范批量水合（与摄取侧同一分段/清洗口径），只有守护进程不可用时
// 才落到下方的本地兜底扫描。
func warmHydrateZCodePointersBatch(home string, rows []map[string]any) {
	if strings.TrimSpace(home) == "" || len(rows) == 0 {
		return
	}
	rows = hydrateZCodePointersViaDaemon(home, rows)
	if len(rows) == 0 {
		return
	}
	const zPrefix = "@ref:zcode:"
	const jPrefix = "@ref:jsonl:"
	seenBase := make(map[string]struct{}, 256)
	zSessionBases := make(map[string][]string, 64)
	seenJSONLFile := make(map[string]string, 32)
	var pendingRaws []string

	for _, r := range rows {
		raw := strings.TrimSpace(asString(r["content"]))
		if strings.HasPrefix(raw, zPrefix) {
			if _, ok := readHydrateCache(hydrateCacheKey(hydrateFlavorLive, home, raw)); ok {
				continue
			}
			pendingRaws = append(pendingRaws, raw)
			base := raw
			if hash := strings.LastIndex(raw, "#"); hash > len(zPrefix) {
				base = raw[:hash]
			}
			if _, dup := seenBase[base]; dup {
				continue
			}
			seenBase[base] = struct{}{}
			rest := strings.TrimPrefix(base, zPrefix)
			if sep := strings.IndexByte(rest, ':'); sep > 0 {
				sid := rest[:sep]
				zSessionBases[sid] = append(zSessionBases[sid], base)
			}
		} else if strings.HasPrefix(raw, jPrefix) {
			if _, ok := readHydrateCache(hydrateCacheKey(hydrateFlavorLive, home, raw)); ok {
				continue
			}
			pendingRaws = append(pendingRaws, raw)
			rest := strings.TrimPrefix(raw, jPrefix)
			if lastColon := strings.LastIndex(rest, ":"); lastColon > 0 {
				fpath := rest[:lastColon]
				if _, dup := seenJSONLFile[fpath]; !dup {
					seenJSONLFile[fpath] = raw
				}
			}
		}
	}

	type batchWork struct {
		sessionID string
		pointer   string
	}
	var works []batchWork
	for _, samplePtr := range seenJSONLFile {
		works = append(works, batchWork{pointer: samplePtr})
	}
	for sid, bases := range zSessionBases {
		// 单轮点查（hydrateEpisodicPointer）走 (session_id, sequence) 范围索引且遇到下一条 user 消息即早停，
		// 单次仅需 ~2-5ms；而整会话扫描（hydrateZCodeSessionBatch）在数千条消息的巨型会话上需读取全部历史分片。
		// 因此只有当同一会话待水合的独立 turn 数达到 32 条以上时才切换整会话批量扫描。
		if len(bases) >= 32 {
			works = append(works, batchWork{sessionID: sid})
		} else {
			for _, b := range bases {
				works = append(works, batchWork{pointer: b})
			}
		}
	}
	if len(works) > 0 {
		runWork := func(w batchWork) {
			if w.sessionID != "" {
				hydrateZCodeSessionBatch(home, w.sessionID)
				for _, b := range zSessionBases[w.sessionID] {
					if _, ok := readHydrateCache(hydrateCacheKey(hydrateFlavorLive, home, b)); !ok {
						storeHydrateCache(hydrateCacheKey(hydrateFlavorLive, home, b), "")
					}
				}
				return
			}
			text := hydrateEpisodicPointer(home, w.pointer)
			if text == "" {
				storeHydrateCache(hydrateCacheKey(hydrateFlavorLive, home, w.pointer), "")
			}
		}
		if len(works) <= 6 {
			for _, w := range works {
				runWork(w)
			}
		} else {
			workers := 6
			ch := make(chan batchWork, len(works))
			for _, w := range works {
				ch <- w
			}
			close(ch)
			var wg sync.WaitGroup
			for i := 0; i < workers; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for w := range ch {
						runWork(w)
					}
				}()
			}
			wg.Wait()
		}
	}

	if len(pendingRaws) > 0 {
		now := time.Now()
		hydrateMu.Lock()
		evictHydrateCacheLocked(now)
		for _, raw := range pendingRaws {
			kLive := hydrateCacheKey(hydrateFlavorLive, home, raw)
			if entry, ok := hydrateCache[kLive]; ok && now.Sub(entry.at) < hydrateCacheTTL {
				continue
			}
			resolved := ""
			if hash := strings.LastIndex(raw, "#"); hash > 0 {
				segPart := raw[hash+1:]
				if dot := strings.IndexByte(segPart, '.'); dot > 0 {
					parentPtr := raw[:hash+1+dot]
					if pEntry, ok := hydrateCache[hydrateCacheKey(hydrateFlavorLive, home, parentPtr)]; ok {
						resolved = pEntry.text
					}
				}
				if resolved == "" {
					basePtr := raw[:hash]
					if bEntry, ok := hydrateCache[hydrateCacheKey(hydrateFlavorLive, home, basePtr)]; ok && bEntry.text != "" {
						resolved = bEntry.text
					}
				}
			}
			hydrateCache[kLive] = hydrateCacheEntry{text: resolved, at: now}
			hydrateCache[hydrateCacheKey(hydrateFlavorRaw, home, raw)] = hydrateCacheEntry{text: resolved, at: now}
		}
		hydrateMu.Unlock()
	}
}

var prewarmHydrateOnce sync.Once

func triggerBackgroundHydratePrewarm() {
	prewarmHydrateOnce.Do(func() {
		go func() {
			time.Sleep(100 * time.Millisecond)
			home, err := os.UserHomeDir()
			if err != nil || home == "" {
				return
			}
			dbPath := resolveMemoryDBPath(home)
			if st, sErr := os.Stat(dbPath); sErr != nil || st.Size() < 16*1024*1024 {
				return
			}
			rows, qErr := sqliteQueryJSON(dbPath, `SELECT raw_text AS content FROM thin_vault_docs WHERE substr(ltrim(raw_text), 1, 5) = '@ref:' ORDER BY occurred_at DESC LIMIT 512;`)
			if qErr == nil && len(rows) > 0 {
				warmHydrateZCodePointersBatch(home, rows)
			}
			if !strings.HasSuffix(os.Args[0], ".test") && !strings.Contains(os.Args[0], ".test.") {
				_ = collectDashboardData(home, UserScope)
				_ = readROIFactsVaultBundle(dbPath)
			}
		}()
	})
}

// zcodeRecallLedgerLookback 是宿主库首次导入的回溯窗口。宿主库
// （~/.zcode/cli/db/db.sqlite）由外部进程持续读写、体量在 GB 级：只回溯近期流水即可满足
// 面板 1h / 24h / 7d / 30d 窗口，避免对无索引谓词做全表扫。
const zcodeRecallLedgerLookback = 30 * 24 * time.Hour

var (
	zcodeLedgerSyncMu      sync.Mutex
	zcodeLedgerSyncRunning bool
)

// triggerProductionRecallLedgerSync 以单飞方式在后台导入宿主 MCP recall 流水。
// 宿主库是外部在读写的 GB 级实时库，导入绝不阻塞控制台读接口：本轮快照少读一段，
// 下一轮补齐（导入按 event_id 幂等）。
func triggerProductionRecallLedgerSync(home, memoryPath string) {
	zcodeLedgerSyncMu.Lock()
	if zcodeLedgerSyncRunning {
		zcodeLedgerSyncMu.Unlock()
		return
	}
	zcodeLedgerSyncRunning = true
	zcodeLedgerSyncMu.Unlock()

	go func() {
		defer func() {
			zcodeLedgerSyncMu.Lock()
			zcodeLedgerSyncRunning = false
			zcodeLedgerSyncMu.Unlock()
		}()
		syncProductionRecallLedger(home, memoryPath)
	}()
}

// readZcodeToolWatermark 返回已导入的宿主 recall 流水水位（毫秒时间戳；0 表示尚未导入）。
func readZcodeToolWatermark(ldb *sql.DB) int64 {
	var raw string
	if err := ldb.QueryRow(`SELECT value FROM efficacy_meta WHERE key = 'zcode_tool_watermark'`).Scan(&raw); err != nil {
		return 0
	}
	ms, _ := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	return ms
}

// syncProductionRecallLedger idempotently merges any rotated .epoch-* sidecar
// ledgers and ingests completed MCP recall tool_usage records from the local
// ZCode host database into the production efficacy ledger. It is strictly no-op
// whenever ROLLING_MEMORY_DB is set or when memoryPath is a test database.
func syncProductionRecallLedger(home, memoryPath string) {
	if strings.TrimSpace(home) == "" || strings.TrimSpace(os.Getenv("ROLLING_MEMORY_DB")) != "" {
		return
	}
	defaultPath := filepath.Join(home, ".local", "share", "rolling-memory", "memory.db")
	if filepath.Clean(memoryPath) != filepath.Clean(defaultPath) {
		return
	}
	fp := computeAdminDBFingerprint(memoryPath, UserScope)
	if fp == "" {
		return
	}
	ledgerPath := memoryPath + ".efficacy.sqlite"
	ldb, err := openPlainSQLite(ledgerPath)
	if err != nil {
		return
	}
	defer ldb.Close()

	_, _ = ldb.Exec(`
		CREATE TABLE IF NOT EXISTS efficacy_event (
			seq INTEGER PRIMARY KEY AUTOINCREMENT,
			event_id TEXT NOT NULL UNIQUE,
			execution_id TEXT NOT NULL,
			session TEXT NOT NULL,
			scope TEXT NOT NULL,
			harness TEXT NOT NULL,
			hook TEXT NOT NULL,
			output_outcome TEXT NOT NULL,
			action TEXT NOT NULL,
			target_event_id TEXT NOT NULL,
			observed_at INTEGER NOT NULL,
			observation TEXT NOT NULL,
			text_sha256 TEXT NOT NULL,
			text_bytes INTEGER
		);
		CREATE TABLE IF NOT EXISTS efficacy_meta (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		);
		PRAGMA user_version = 1;
	`)
	_, _ = ldb.Exec(`INSERT OR REPLACE INTO efficacy_meta(key, value) VALUES ('db_fingerprint', ?)`, fp)

	// 1. Merge any rotated .epoch-* sidecar ledgers from the same production database.
	if matches, gErr := filepath.Glob(ledgerPath + ".epoch-*"); gErr == nil {
		for _, epPath := range matches {
			if strings.HasSuffix(epPath, "-wal") || strings.HasSuffix(epPath, "-shm") {
				continue
			}
			edb := openVaultReadOnly(epPath)
			if edb == nil {
				continue
			}
			rows, qErr := edb.Query(`
				SELECT event_id, execution_id, session, scope, harness, hook,
				       output_outcome, action, target_event_id, observed_at,
				       observation, text_sha256, text_bytes
				FROM efficacy_event
			`)
			if qErr == nil {
				tx, txErr := ldb.Begin()
				if txErr == nil {
					for rows.Next() {
						var evID, execID, sess, sc, harn, hk, outc, act, tgt, obs, sha string
						var obsAt int64
						var tb sql.NullInt64
						if rows.Scan(&evID, &execID, &sess, &sc, &harn, &hk, &outc, &act, &tgt, &obsAt, &obs, &sha, &tb) == nil {
							var tbArg any
							if tb.Valid {
								tbArg = tb.Int64
							}
							_, _ = tx.Exec(`
								INSERT OR IGNORE INTO efficacy_event(
									event_id, execution_id, session, scope, harness, hook,
									output_outcome, action, target_event_id, observed_at,
									observation, text_sha256, text_bytes
								) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
							`, evID, execID, sess, sc, harn, hk, outc, act, tgt, obsAt, obs, sha, tbArg)
						}
					}
					_ = tx.Commit()
				}
				rows.Close()
			}
			edb.Close()
			_ = os.Remove(epPath)
			_ = os.Remove(epPath + "-wal")
			_ = os.Remove(epPath + "-shm")
		}
	}

	// 2. Sync real MCP recall tool calls from all JSONL harnesses (Claude Code, Codex, WorkBuddy, CodeBuddy, DSH)
	//    as well as the ZCode host tool_usage table.
	syncMultiHarnessJSONLRecallLedger(home, fp, ldb)

	zdbPath := filepath.Join(home, ".zcode", "cli", "db", "db.sqlite")
	zdb, zOpenErr := memoryRODB(zdbPath)
	if zOpenErr != nil || zdb == nil {
		return
	}
	// 以 started_at 水位增量扫描（命中宿主 tool_usage_started_tool_idx 的前缀列）；
	// `tool_name IN (...)` 单独作谓词无索引可用，对 GB 级实时库会退化成全表扫。
	watermarkMS := readZcodeToolWatermark(ldb)
	if watermarkMS <= 0 {
		watermarkMS = time.Now().Add(-zcodeRecallLedgerLookback).UnixMilli()
	}
	zRows, zErr := zdb.Query(`
		SELECT tool_call_id, session_id, status, coalesce(started_at, 0), coalesce(completed_at, 0), coalesce(output_bytes, 0)
		FROM tool_usage
		WHERE started_at >= ?
		  AND tool_name IN ('mcp__plugin_rolling-memory_rolling-memory__recall', 'mcp__rolling-memory__recall', 'rolling-memory:rolling-memory__recall')
		ORDER BY started_at ASC
	`, watermarkMS)
	if zErr != nil {
		return
	}
	defer zRows.Close()
	tx, txErr := ldb.Begin()
	if txErr != nil {
		return
	}
	var maxStartedMS int64
	for zRows.Next() {
		var callID, sessID, status string
		var startMS, endMS, outBytes int64
		if zRows.Scan(&callID, &sessID, &status, &startMS, &endMS, &outBytes) != nil {
			continue
		}
		callID = strings.TrimSpace(callID)
		if callID == "" {
			continue
		}
		tsMS := endMS
		if tsMS <= 0 {
			tsMS = startMS
		}
		if tsMS <= 0 {
			continue
		}
		obsAt := tsMS * int64(time.Millisecond)
		outcome := "hits"
		if status == "error" || status == "cancelled" {
			outcome = "error"
		} else if outBytes <= 0 {
			outcome = "empty"
		}
		sum := sha256.Sum256([]byte("zcode\x00" + callID))
		shaHex := hex.EncodeToString(sum[:])
		sessSum := sha256.Sum256([]byte(fp + "\x00" + strings.TrimSpace(sessID)))
		sessDigest := "s_" + hex.EncodeToString(sessSum[:])[:24]
		_, _ = tx.Exec(`
			INSERT OR IGNORE INTO efficacy_event(
				event_id, execution_id, session, scope, harness, hook,
				output_outcome, action, target_event_id, observed_at,
				observation, text_sha256, text_bytes
			) VALUES (?, ?, ?, 'mem:self', 'zcode', 'mcp_recall:tool', ?, 'recall', '', ?, 'observed', ?, ?)
		`, "zcode_"+callID, shaHex[:32], sessDigest, outcome, obsAt, shaHex, outBytes)
		if startMS > maxStartedMS {
			maxStartedMS = startMS
		}
	}
	_ = tx.Commit()
	// 自愈校准：早期守护进程在未携带宿主环境变量时将 ZCode 触发的直写行（mcp_recall:<mode>）
	// 误标为 harness='cli'，此处与已落盘的 zcode 回填孪生行（±2s 同窗口）对齐还原为 'zcode'。
	_, _ = ldb.Exec(`
			UPDATE efficacy_event
			SET harness = 'zcode'
			WHERE harness = 'cli'
			  AND action = 'recall'
			  AND hook LIKE 'mcp_recall:%'
			  AND hook != 'mcp_recall:tool'
			  AND EXISTS (
				SELECT 1 FROM efficacy_event t
				WHERE t.scope = efficacy_event.scope
				  AND t.harness = 'zcode'
				  AND t.action = 'recall'
				  AND t.hook = 'mcp_recall:tool'
				  AND ABS(t.observed_at - efficacy_event.observed_at) <= 2000000000
			  )
		`)
	// 水位只前进（写已提交之后落盘）：下轮导入从本次最大 started_at 接着扫。
	if maxStartedMS > 0 {
		_, _ = ldb.Exec(`INSERT OR REPLACE INTO efficacy_meta(key, value) VALUES ('zcode_tool_watermark', ?)`, strconv.FormatInt(maxStartedMS, 10))
		notifyStreamKindRefresh(streamKindROI)
	}
}

// openVaultReadOnly opens the memory database read-only. It returns nil when the
// path is not a plain regular file, so callers never create or mutate a vault.
func openVaultReadOnly(memoryPath string) *sql.DB {
	if strings.TrimSpace(memoryPath) == "" || memoryPath == ":memory:" || strings.HasPrefix(memoryPath, "file:") || strings.ContainsAny(memoryPath, "?#") {
		return nil
	}
	absPath, err := filepath.Abs(memoryPath)
	if err != nil {
		return nil
	}
	info, err := os.Stat(absPath)
	if err != nil || !info.Mode().IsRegular() {
		return nil
	}
	uri := sqliteFileURI(absPath) + "?mode=ro&_pragma=busy_timeout%2815000%29&_pragma=cache_size%28-32768%29&_pragma=temp_store%28MEMORY%29"
	db, err := sql.Open("sqlite", uri)
	if err != nil {
		return nil
	}
	return db
}

// borrowVaultReadOnly reuses the in-process read-only connection pool (memoryRODB)
// when available, avoiding repeated sql.Open/Close overhead on every ROI pass.
func borrowVaultReadOnly(memoryPath string) (*sql.DB, func()) {
	if strings.TrimSpace(memoryPath) == "" || memoryPath == ":memory:" || strings.HasPrefix(memoryPath, "file:") || strings.ContainsAny(memoryPath, "?#") {
		return nil, func() {}
	}
	absPath, err := filepath.Abs(memoryPath)
	if err != nil {
		return nil, func() {}
	}
	info, err := os.Stat(absPath)
	if err != nil || !info.Mode().IsRegular() {
		return nil, func() {}
	}
	if pooled, pErr := memoryRODB(absPath); pErr == nil && pooled != nil {
		return pooled, func() {}
	}
	db := openVaultReadOnly(absPath)
	if db == nil {
		return nil, func() {}
	}
	return db, func() { _ = db.Close() }
}

// computeAdminDBFingerprint mirrors the ledger writer's identity formula: one
// database, one fingerprint, independent of which scope the console is viewing.
func computeAdminDBFingerprint(memoryPath, scope string) string {
	db, release := borrowVaultReadOnly(memoryPath)
	if db == nil {
		return ""
	}
	defer release()
	var createdAt, engine, firstDocID string
	_ = db.QueryRow(`SELECT coalesce(value, '') FROM db_meta WHERE key = 'created_at'`).Scan(&createdAt)
	_ = db.QueryRow(`SELECT coalesce(value, '') FROM db_meta WHERE key = 'engine'`).Scan(&engine)
	_ = db.QueryRow(`SELECT coalesce(doc_id, '') FROM thin_vault_docs ORDER BY occurred_at ASC, rowid ASC LIMIT 1`).Scan(&firstDocID)
	if strings.TrimSpace(firstDocID) == "" {
		_ = db.QueryRow(`SELECT coalesce(noetic_id, '') FROM thin_vault_noetic ORDER BY occurred_at ASC, rowid ASC LIMIT 1`).Scan(&firstDocID)
	}
	if strings.TrimSpace(firstDocID) == "" {
		_ = db.QueryRow(`SELECT coalesce(hash, '') FROM trace ORDER BY recorded_at ASC, rowid ASC LIMIT 1`).Scan(&firstDocID)
		if strings.TrimSpace(firstDocID) == "" {
			_ = db.QueryRow(`SELECT coalesce(id, '') FROM node ORDER BY created_at ASC, rowid ASC LIMIT 1`).Scan(&firstDocID)
		}
	}
	if strings.TrimSpace(createdAt) == "" || strings.TrimSpace(firstDocID) == "" {
		return ""
	}
	raw := strings.Join([]string{
		strings.TrimSpace(createdAt),
		strings.TrimSpace(engine),
		strings.TrimSpace(firstDocID),
	}, "|")
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])[:24]
}

// vaultDroppedLegacyAnalytics reports a live thin-vault database that no longer
// carries the retired trace/assertion tables the per-turn settlement engine
// reads. A database that cannot be opened is not judged here.
func vaultDroppedLegacyAnalytics(memoryPath string) bool {
	db, release := borrowVaultReadOnly(memoryPath)
	if db == nil {
		return false
	}
	defer release()
	var vaultTables int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'thin_vault_docs'`).Scan(&vaultTables); err != nil || vaultTables == 0 {
		return false
	}
	var legacyTables int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name IN ('trace', 'assertion')`).Scan(&legacyTables); err != nil {
		return false
	}
	return legacyTables == 0
}

// vaultAnalyticsBundle holds all live vault telemetry extracted in a single read pass.
type vaultAnalyticsBundle struct {
	vault         *roiFactsVault
	vaultWindows  []roiFactWindow
	dailySeries   []roiDailyBucket
	topCitedUnits []roiTopCitedUnit
	locusMatrix   []roiLocusRow
	noeticStream  []roiNoeticEvent
}

type roiVaultBundleEntry struct {
	sig        string
	writeEpoch uint64
	at         time.Time
	refreshing bool
	bundle     vaultAnalyticsBundle
}

var (
	roiVaultBundleMu    sync.Mutex
	roiVaultBundleCache = map[string]*roiVaultBundleEntry{}
)

func roiVaultBundleSignature(memoryPath string, now time.Time) string {
	var b strings.Builder
	b.WriteString(strconv.FormatInt(now.Unix()/60, 10))
	b.WriteByte(':')
	b.WriteString(strconv.FormatUint(currentVaultWriteEpoch(), 10))
	for _, p := range []string{
		memoryPath,
		memoryPath + "-wal",
		memoryPath + ".efficacy.sqlite",
		memoryPath + ".efficacy.sqlite-wal",
	} {
		b.WriteByte('|')
		if st, err := os.Stat(p); err == nil {
			b.WriteString(strconv.FormatInt(st.Size(), 10))
			b.WriteByte(':')
			b.WriteString(strconv.FormatInt(st.ModTime().UnixNano(), 10))
		} else {
			b.WriteString("-")
		}
	}
	return b.String()
}

var roiVaultComputeMu sync.Mutex

// readROIFactsVaultBundle reads the live vault telemetry, L3 Noetic citation graph,
// per-workspace coverage matrix, daily ingestion/crystallization series, and top cited units.
func readROIFactsVaultBundle(memoryPath string) vaultAnalyticsBundle {
	now := time.Now()
	writeEp := currentVaultWriteEpoch()
	sig := roiVaultBundleSignature(memoryPath, now)
	isLargeVault := false
	if st, err := os.Stat(memoryPath); err == nil && st.Size() >= 16*1024*1024 {
		isLargeVault = true
	}
	roiVaultBundleMu.Lock()
	if cached := roiVaultBundleCache[memoryPath]; cached != nil && cached.bundle.vault != nil {
		if cached.sig == sig || (isLargeVault && cached.writeEpoch == writeEp && now.Sub(cached.at) < 15*time.Second) {
			b := cached.bundle
			roiVaultBundleMu.Unlock()
			return b
		}
		if isLargeVault {
			b := cached.bundle
			if !cached.refreshing {
				cached.refreshing = true
				go func(p string) {
					fresh, fSig := computeROIFactsVaultBundle(p)
					curEp := currentVaultWriteEpoch()
					roiVaultBundleMu.Lock()
					if fresh.vault != nil {
						roiVaultBundleCache[p] = &roiVaultBundleEntry{
							sig:        fSig,
							writeEpoch: curEp,
							at:         time.Now(),
							bundle:     fresh,
						}
					} else if cur := roiVaultBundleCache[p]; cur != nil {
						cur.refreshing = false
					}
					roiVaultBundleMu.Unlock()
					if fresh.vault != nil {
						notifyStreamKindRefresh(streamKindROI)
					}
				}(memoryPath)
			}
			roiVaultBundleMu.Unlock()
			return b
		}
	}
	roiVaultBundleMu.Unlock()

	roiVaultComputeMu.Lock()
	defer roiVaultComputeMu.Unlock()
	roiVaultBundleMu.Lock()
	if cached := roiVaultBundleCache[memoryPath]; cached != nil && cached.bundle.vault != nil {
		if cached.sig == sig || (isLargeVault && cached.writeEpoch == writeEp && time.Since(cached.at) < 15*time.Second) {
			b := cached.bundle
			roiVaultBundleMu.Unlock()
			return b
		}
	}
	roiVaultBundleMu.Unlock()

	out, computedSig := computeROIFactsVaultBundle(memoryPath)
	if out.vault != nil {
		roiVaultBundleMu.Lock()
		roiVaultBundleCache[memoryPath] = &roiVaultBundleEntry{
			sig:        computedSig,
			writeEpoch: currentVaultWriteEpoch(),
			at:         time.Now(),
			bundle:     out,
		}
		roiVaultBundleMu.Unlock()
	}
	return out
}

func computeROIFactsVaultBundle(memoryPath string) (vaultAnalyticsBundle, string) {
	now := time.Now()
	sig := roiVaultBundleSignature(memoryPath, now)
	out := vaultAnalyticsBundle{
		vaultWindows:  []roiFactWindow{},
		dailySeries:   []roiDailyBucket{},
		topCitedUnits: []roiTopCitedUnit{},
		locusMatrix:   []roiLocusRow{},
		noeticStream:  []roiNoeticEvent{},
	}
	home, _ := os.UserHomeDir()
	db, release := borrowVaultReadOnly(memoryPath)
	if db == nil {
		return out, sig
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var hasVault int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'thin_vault_docs'`).Scan(&hasVault); err != nil || hasVault == 0 {
		return out, sig
	}

	nowUnix := now.Unix()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).Unix()

	floor1h := nowUnix - 3600
	floor24h := nowUnix - 86400
	floor7d := nowUnix - 7*86400
	floor30d := nowUnix - 30*86400

	// 「今日/窗口新增」与「记忆吞吐趋势」同口径：按首次入库时间（inserted_at，
	// 未迁移的只读旧夹具缺列时平滑回退内容时间 occurred_at）聚合，避免编辑
	// 旧文件把存量分块刷进近期新增的账实不一致。
	docTsExpr := vaultIngestTimeExpr(memoryPath, "thin_vault_docs")
	bgTsExpr := vaultIngestTimeExpr(memoryPath, "thin_vault_bg_chunks")
	ntTsExpr := vaultIngestTimeExpr(memoryPath, "thin_vault_noetic")

	// 层归属与控制台统一口径（三层浏览 / facets / 趋势图）一致：docs 表中
	// layer=='background' 的行属 L1 背景层（计入背景计数），其余属 L2 情景层；
	// 旧夹具缺 layer 列时退回不分层（全部按 L2 计）。
	bgDocCond := "0"
	var layerCols int
	if lErr := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('thin_vault_docs') WHERE name = 'layer'`).Scan(&layerCols); lErr == nil && layerCols > 0 {
		bgDocCond = "lower(trim(COALESCE(layer, ''))) = 'background'"
	}

	vault := &roiFactsVault{SchemaEra: "thin_vault"}
	var ep1h, ep24h, ep7d, ep30d, epAll int64
	_ = db.QueryRowContext(ctx, fmt.Sprintf(`
			SELECT COUNT(*),
			       COALESCE(SUM(CASE WHEN raw_text LIKE '@ref:%%' THEN 1 ELSE 0 END), 0),
			       COALESCE(SUM(CASE WHEN %s >= ? THEN 1 ELSE 0 END), 0),
			       COALESCE(SUM(CASE WHEN %s >= ? THEN 1 ELSE 0 END), 0),
			       COALESCE(SUM(CASE WHEN %s >= ? THEN 1 ELSE 0 END), 0),
			       COALESCE(SUM(CASE WHEN %s >= ? THEN 1 ELSE 0 END), 0),
			       COALESCE(SUM(CASE WHEN %s >= ? THEN 1 ELSE 0 END), 0),
			       COALESCE(SUM(CASE WHEN %s >= 0 THEN 1 ELSE 0 END), 0)
			FROM thin_vault_docs WHERE NOT (%s)`, docTsExpr, docTsExpr, docTsExpr, docTsExpr, docTsExpr, docTsExpr, bgDocCond),
		todayStart, floor1h, floor24h, floor7d, floor30d,
	).Scan(&vault.EpisodicDocs, &vault.EpisodicPointers, &vault.TodayEpisodic, &ep1h, &ep24h, &ep7d, &ep30d, &epAll)
	// 背景层文档（layer=='background'）的窗口计数单取，随后并入 L1 背景计数。
	var bgDocToday, bgDoc1h, bgDoc24h, bgDoc7d, bgDoc30d, bgDocAll int64
	_ = db.QueryRowContext(ctx, fmt.Sprintf(`
			SELECT COALESCE(SUM(CASE WHEN %s >= ? THEN 1 ELSE 0 END), 0),
			       COALESCE(SUM(CASE WHEN %s >= ? THEN 1 ELSE 0 END), 0),
			       COALESCE(SUM(CASE WHEN %s >= ? THEN 1 ELSE 0 END), 0),
			       COALESCE(SUM(CASE WHEN %s >= ? THEN 1 ELSE 0 END), 0),
			       COALESCE(SUM(CASE WHEN %s >= ? THEN 1 ELSE 0 END), 0),
			       COALESCE(SUM(CASE WHEN %s >= 0 THEN 1 ELSE 0 END), 0)
			FROM thin_vault_docs WHERE %s`, docTsExpr, docTsExpr, docTsExpr, docTsExpr, docTsExpr, docTsExpr, bgDocCond),
		todayStart, floor1h, floor24h, floor7d, floor30d,
	).Scan(&bgDocToday, &bgDoc1h, &bgDoc24h, &bgDoc7d, &bgDoc30d, &bgDocAll)
	_ = db.QueryRowContext(ctx, fmt.Sprintf(`SELECT COUNT(DISTINCT CASE WHEN instr(address_uri, '#') > 0 THEN substr(address_uri, 1, instr(address_uri, '#') - 1) ELSE address_uri END) FROM thin_vault_docs WHERE address_uri != '' AND NOT (%s)`, bgDocCond)).Scan(&vault.EpisodicCells)
	vault.EpisodicText = vault.EpisodicDocs - vault.EpisodicPointers
	if vault.EpisodicDocs > 0 {
		vault.EpisodicPointerRatioPct = roundFloat(float64(vault.EpisodicPointers)*100.0/float64(vault.EpisodicDocs), 1)
	}
	_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM thin_vault_bg_dirs`).Scan(&vault.BackgroundDirs)
	_ = db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(file_size), 0) FROM thin_vault_bg_files`).Scan(&vault.BackgroundFiles, &vault.BackgroundBytes)

	var bg1h, bg24h, bg7d, bg30d, bgAll int64
	_ = db.QueryRowContext(ctx, fmt.Sprintf(`
			SELECT COUNT(*),
			       COALESCE(SUM(CASE WHEN %s >= ? THEN 1 ELSE 0 END), 0),
			       COALESCE(SUM(CASE WHEN %s >= ? THEN 1 ELSE 0 END), 0),
			       COALESCE(SUM(CASE WHEN %s >= ? THEN 1 ELSE 0 END), 0),
			       COALESCE(SUM(CASE WHEN %s >= ? THEN 1 ELSE 0 END), 0),
			       COALESCE(SUM(CASE WHEN %s >= ? THEN 1 ELSE 0 END), 0),
			       COALESCE(SUM(CASE WHEN %s >= 0 THEN 1 ELSE 0 END), 0)
			FROM thin_vault_bg_chunks`, bgTsExpr, bgTsExpr, bgTsExpr, bgTsExpr, bgTsExpr, bgTsExpr),
		todayStart, floor1h, floor24h, floor7d, floor30d,
	).Scan(&vault.BackgroundChunks, &vault.TodayBackground, &bg1h, &bg24h, &bg7d, &bg30d, &bgAll)
	// 层归属统一：L1 = 背景分块 + docs 表背景层文档；L2 已按层过滤（上方便只剩情景文档）。
	vault.BackgroundChunks += bgDocAll
	vault.TodayBackground += bgDocToday
	bg1h += bgDoc1h
	bg24h += bgDoc24h
	bg7d += bgDoc7d
	bg30d += bgDoc30d
	bgAll += bgDocAll

	_ = db.QueryRowContext(ctx, `
			SELECT COALESCE(SUM(CASE WHEN status = 'active' THEN 1 ELSE 0 END), 0),
			       COALESCE(SUM(CASE WHEN status != 'active' THEN 1 ELSE 0 END), 0),
			       COALESCE(SUM(CASE WHEN status = 'active' AND cited_unit_ids_json NOT IN ('', '[]') THEN 1 ELSE 0 END), 0)
			FROM thin_vault_noetic`,
	).Scan(&vault.NoeticUnits, &vault.NoeticStale, &vault.NoeticWithCitations)
	_ = db.QueryRowContext(ctx, fmt.Sprintf(`SELECT COUNT(*) FROM thin_vault_noetic WHERE status = 'active' AND %s >= ?`, ntTsExpr), todayStart).Scan(&vault.TodayNoetic)
	_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM injection_ledger`).Scan(&vault.InjectionLedgerRows)

	vault.TotalUnits = vault.BackgroundChunks + vault.EpisodicDocs + vault.NoeticUnits

	// Prepare rolling windows (1h, 24h, 7d, 30d, plus "all" for full-history view).
	type winSpec struct {
		Key     string
		Seconds int64
		Floor   int64
		EpAdded int64
		BgAdded int64
	}
	specs := []winSpec{
		{Key: "1h", Seconds: 3600, Floor: floor1h, EpAdded: ep1h, BgAdded: bg1h},
		{Key: "24h", Seconds: 86400, Floor: floor24h, EpAdded: ep24h, BgAdded: bg24h},
		{Key: "7d", Seconds: 7 * 86400, Floor: floor7d, EpAdded: ep7d, BgAdded: bg7d},
		{Key: "30d", Seconds: 30 * 86400, Floor: floor30d, EpAdded: ep30d, BgAdded: bg30d},
		{Key: "all", Seconds: 0, Floor: 0, EpAdded: epAll, BgAdded: bgAll},
	}
	winMap := make(map[string]*roiFactWindow, len(specs))
	winCitedSets := make(map[string]map[string]bool, len(specs))
	for _, sp := range specs {
		winMap[sp.Key] = &roiFactWindow{
			Key:             sp.Key,
			Seconds:         sp.Seconds,
			SinceUnix:       sp.Floor,
			EpisodicAdded:   sp.EpAdded,
			BackgroundAdded: sp.BgAdded,
			Actions:         map[string]int64{},
			RecallOutcomes:  map[string]int64{},
		}
		winCitedSets[sp.Key] = make(map[string]bool)
	}

	// Scan all active & historical L3 Noetic rows to compute citation graph, reuse multipliers,
	// delivered tokenz-r tokens, window aggregations, daily buckets, and the Noetic stream.
	type citeStat struct {
		count        int64
		adoptedCount int64
		lastAt       int64
		queries      []string
		seenQuery    map[string]bool
		firstSnippet string
		citations    []roiUnitCitation
	}
	citedStats := make(map[string]*citeStat)
	dailyMap := make(map[string]*roiDailyBucket)
	for d := 13; d >= 0; d-- {
		dayStr := now.AddDate(0, 0, -d).Format("2006-01-02")
		dailyMap[dayStr] = &roiDailyBucket{Date: dayStr}
	}

	// Read actual delivered byte measurements from the sidecar efficacy ledger
	// (.efficacy.sqlite) so token delivery metrics reflect full <rolling-memory-recall>
	// payloads rather than truncated L3 citation snippets.
	recallBytesBySec := make(map[int64]int64)
	ledgerWinTokens := make(map[string]int64, len(specs))
	ledgerDayTokens := make(map[string]int64, 14)
	var ledgerTotalTokens, mcpRecallHitTokens, mcpRecallHitCalls, ledgerHitCalls int64
	if ldb := openVaultReadOnly(memoryPath + ".efficacy.sqlite"); ldb != nil {
		var lVer int
		if ldb.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&lVer) == nil && lVer == 1 {
			lRows, lErr := ldb.QueryContext(ctx, `
					SELECT e.action, e.output_outcome, coalesce(e.text_bytes, 0), e.observed_at
					FROM efficacy_event e
					WHERE e.action IN ('recall', 'output_attempt') AND e.observation = 'observed' AND e.text_bytes > 0
					  AND (e.hook != 'mcp_recall:tool' OR NOT EXISTS (
						SELECT 1 FROM efficacy_event d
						WHERE d.scope = e.scope
						  AND d.action = 'recall'
						  AND d.hook LIKE 'mcp_recall:%'
						  AND d.hook != 'mcp_recall:tool'
						  AND ABS(d.observed_at - e.observed_at) <= 2000000000
					  ))
				`)
			if lErr == nil {
				for lRows.Next() {
					var act, outc string
					var tBytes, obsAt int64
					if lRows.Scan(&act, &outc, &tBytes, &obsAt) != nil || tBytes <= 0 {
						continue
					}
					sec := obsAt / int64(time.Second)
					if sec <= 0 {
						sec = obsAt
					}
					if tBytes > recallBytesBySec[sec] {
						recallBytesBySec[sec] = tBytes
					}
					tok := bytesToTokenzR(tBytes)
					ledgerTotalTokens += tok
					ledgerHitCalls++
					if act == "recall" && outc == "hits" {
						mcpRecallHitTokens += tok
						mcpRecallHitCalls++
					}
					for _, sp := range specs {
						if sec >= sp.Floor {
							ledgerWinTokens[sp.Key] += tok
						}
					}
					dayStr := time.Unix(sec, 0).In(now.Location()).Format("2006-01-02")
					if _, ok := dailyMap[dayStr]; ok {
						ledgerDayTokens[dayStr] += tok
					}
				}
				lRows.Close()
			}
		}
		ldb.Close()
	}

	noeticRows, err := db.QueryContext(ctx, fmt.Sprintf(`
		SELECT noetic_id, occurred_at, %s AS ingest_at, status, query_text, answer_text,
		       cited_unit_ids_json, cited_sentences_json,
		       coalesce(address_uri, ''), coalesce(waveform_bar, ''),
		       coalesce(assistant_id, ''), coalesce(incarnation_id, '')
		FROM thin_vault_noetic
		ORDER BY occurred_at DESC, rowid DESC
	`, ntTsExpr))
	var allNoetics []roiNoeticEvent
	var matchedFlags []bool
	var matchedTokSum, matchedCiteSum int64
	if err == nil {
		for noeticRows.Next() {
			var ev roiNoeticEvent
			var citedIDsRaw, citedSentsRaw string
			if scanErr := noeticRows.Scan(
				&ev.NoeticID, &ev.OccurredAt, &ev.IngestAt, &ev.Status, &ev.QueryText, &ev.AnswerText,
				&citedIDsRaw, &citedSentsRaw,
				&ev.AddressURI, &ev.WaveformBar, &ev.AssistantID, &ev.IncarnationID,
			); scanErr != nil {
				continue
			}
			_ = json.Unmarshal([]byte(citedIDsRaw), &ev.CitedUnitIDs)
			_ = json.Unmarshal([]byte(citedSentsRaw), &ev.CitedSentences)
			if ev.CitedUnitIDs == nil {
				ev.CitedUnitIDs = []string{}
			}
			if ev.CitedSentences == nil {
				ev.CitedSentences = []string{}
			}
			ev.CitationCount = len(ev.CitedUnitIDs)
			deliveredText := ev.AnswerText
			if len(ev.CitedSentences) > 0 {
				deliveredText = strings.Join(ev.CitedSentences, "\n")
			}
			ev.DeliveredTokens = int64(estimateTokenzRCount(ev.QueryText + "\n" + deliveredText))
			hasFullMatch := false
			if ev.OccurredAt > 0 && len(recallBytesBySec) > 0 {
				var matchedBytes int64
				for delta := int64(0); delta <= 8; delta++ {
					if b := recallBytesBySec[ev.OccurredAt-delta]; b > matchedBytes {
						matchedBytes = b
					}
					if b := recallBytesBySec[ev.OccurredAt+delta]; b > matchedBytes {
						matchedBytes = b
					}
					if matchedBytes > 256 {
						break
					}
				}
				if matchedBytes > 256 {
					if actualTok := bytesToTokenzR(matchedBytes); actualTok > ev.DeliveredTokens {
						ev.DeliveredTokens = actualTok
						hasFullMatch = true
						if ev.CitationCount > 0 {
							matchedTokSum += actualTok
							matchedCiteSum += int64(ev.CitationCount)
						}
					}
				}
			}
			allNoetics = append(allNoetics, ev)
			matchedFlags = append(matchedFlags, hasFullMatch)
		}
		noeticRows.Close()
	}

	var endogenousTokPerCite int64
	if matchedCiteSum > 0 {
		endogenousTokPerCite = (matchedTokSum + matchedCiteSum/2) / matchedCiteSum
	}

	for i := range allNoetics {
		ev := &allNoetics[i]
		if !matchedFlags[i] && endogenousTokPerCite > 0 && ev.CitationCount > 0 {
			if derived := endogenousTokPerCite * int64(ev.CitationCount); derived > ev.DeliveredTokens {
				ev.DeliveredTokens = derived
			}
		}
		if len(out.noeticStream) < 50 {
			out.noeticStream = append(out.noeticStream, *ev)
		}
		if ev.Status != "active" {
			continue
		}
		vault.TotalDeliveredTokens += ev.DeliveredTokens
		vault.TotalCitationLinks += int64(ev.CitationCount)
		if ev.IngestAt >= todayStart {
			vault.TodayCitations += int64(ev.CitationCount)
		}
		if ev.IngestAt > 0 {
			dayStr := time.Unix(ev.IngestAt, 0).In(now.Location()).Format("2006-01-02")
			if b, ok := dailyMap[dayStr]; ok {
				b.NoeticCount++
				b.CitationLinks += int64(ev.CitationCount)
				b.DeliveredTokens += ev.DeliveredTokens
			}
		}

		for _, sp := range specs {
			if ev.IngestAt >= sp.Floor {
				w := winMap[sp.Key]
				w.NoeticCrystallized++
				w.CitationLinks += int64(ev.CitationCount)
				w.DeliveredTokens += ev.DeliveredTokens
				for _, uid := range ev.CitedUnitIDs {
					u := strings.TrimSpace(uid)
					if u != "" {
						winCitedSets[sp.Key][u] = true
					}
				}
			}
		}

		for idx, uid := range ev.CitedUnitIDs {
			u := strings.TrimSpace(uid)
			if u == "" {
				continue
			}
			st, exists := citedStats[u]
			if !exists {
				st = &citeStat{seenQuery: make(map[string]bool)}
				citedStats[u] = st
			}
			st.count++
			if ev.OccurredAt > st.lastAt {
				st.lastAt = ev.OccurredAt
			}
			if st.firstSnippet == "" {
				if idx < len(ev.CitedSentences) && strings.TrimSpace(ev.CitedSentences[idx]) != "" {
					st.firstSnippet = strings.TrimSpace(ev.CitedSentences[idx])
				} else if len(ev.CitedSentences) > 0 && strings.TrimSpace(ev.CitedSentences[0]) != "" {
					st.firstSnippet = strings.TrimSpace(ev.CitedSentences[0])
				} else if strings.TrimSpace(ev.AnswerText) != "" {
					st.firstSnippet = strings.TrimSpace(ev.AnswerText)
				}
			}
			qTrim := strings.TrimSpace(ev.QueryText)
			if qTrim != "" && !st.seenQuery[qTrim] && len(st.queries) < 5 {
				st.seenQuery[qTrim] = true
				st.queries = append(st.queries, qTrim)
			}
			if len(st.citations) < 16 {
				st.citations = append(st.citations, roiUnitCitation{
					NoeticID:   ev.NoeticID,
					QueryText:  clipRunes(ev.QueryText, 240),
					AnswerText: clipRunes(ev.AnswerText, 360),
					OccurredAt: ev.OccurredAt,
				})
			}
		}
	}

	vault.UniqueCitedUnits = int64(len(citedStats))
	for _, st := range citedStats {
		if st.count >= 2 {
			vault.ReusedUnitsCount++
		}
		if st.count > vault.MaxReuseCount {
			vault.MaxReuseCount = st.count
		}
	}
	if vault.UniqueCitedUnits > 0 {
		vault.AvgReuseMultiplier = roundFloat(float64(vault.TotalCitationLinks)/float64(vault.UniqueCitedUnits), 2)
	}
	if vault.NoeticWithCitations > 0 {
		vault.AvgCitationsPerRecall = roundFloat(float64(vault.TotalCitationLinks)/float64(vault.NoeticWithCitations), 2)
	}
	for _, sp := range specs {
		if ledgerWinTokens[sp.Key] > winMap[sp.Key].DeliveredTokens {
			winMap[sp.Key].DeliveredTokens = ledgerWinTokens[sp.Key]
		}
	}
	for d, b := range dailyMap {
		if ledgerDayTokens[d] > b.DeliveredTokens {
			b.DeliveredTokens = ledgerDayTokens[d]
		}
	}
	if ledgerTotalTokens > vault.TotalDeliveredTokens {
		vault.TotalDeliveredTokens = ledgerTotalTokens
	}
	if mcpRecallHitCalls > 0 {
		vault.AvgDeliveredTokens = (mcpRecallHitTokens + mcpRecallHitCalls/2) / mcpRecallHitCalls
	} else if ledgerHitCalls > 0 {
		vault.AvgDeliveredTokens = (ledgerTotalTokens + ledgerHitCalls/2) / ledgerHitCalls
	} else if vault.NoeticUnits > 0 {
		vault.AvgDeliveredTokens = (vault.TotalDeliveredTokens + vault.NoeticUnits/2) / vault.NoeticUnits
	}

	// Measure inline distillation compression ratio (raw sentences vs the memory engine kept_indices).
	distRows, err := db.QueryContext(ctx, `
		SELECT raw_text, sentences_json, kept_indices_json
		FROM thin_vault_docs
		WHERE raw_text NOT LIKE '@ref:%' AND raw_text != ''
		LIMIT 500
	`)
	if err == nil {
		for distRows.Next() {
			var rawTxt, sentsJSON, keptJSON string
			if distRows.Scan(&rawTxt, &sentsJSON, &keptJSON) != nil {
				continue
			}
			rawTok := int64(estimateTokenzRCount(rawTxt))
			vault.InlineRawTokens += rawTok
			var sents []string
			var kept []int
			_ = json.Unmarshal([]byte(sentsJSON), &sents)
			_ = json.Unmarshal([]byte(keptJSON), &kept)
			if len(sents) > 0 && len(kept) > 0 && len(kept) < len(sents) {
				var keptParts []string
				for _, idx := range kept {
					if idx >= 0 && idx < len(sents) {
						keptParts = append(keptParts, sents[idx])
					}
				}
				if len(keptParts) > 0 {
					distTok := int64(estimateTokenzRCount(strings.Join(keptParts, "\n")))
					if distTok < rawTok {
						vault.InlineDistilledTokens += distTok
						continue
					}
				}
			}
			vault.InlineDistilledTokens += rawTok
		}
		distRows.Close()
	}
	if vault.InlineRawTokens > 0 && vault.InlineDistilledTokens < vault.InlineRawTokens {
		vault.DistillationSavedPct = roundFloat(float64(vault.InlineRawTokens-vault.InlineDistilledTokens)*100.0/float64(vault.InlineRawTokens), 1)
	} else if vault.InlineRawTokens > 0 && vault.TotalDeliveredTokens > 0 && vault.TotalDeliveredTokens < vault.InlineRawTokens {
		vault.InlineDistilledTokens = vault.TotalDeliveredTokens
		vault.DistillationSavedPct = roundFloat(float64(vault.InlineRawTokens-vault.TotalDeliveredTokens)*100.0/float64(vault.InlineRawTokens), 1)
	}

	// Fill daily episodic & background counts for the last 14 days.
	floor14d := now.AddDate(0, 0, -14).Unix()
	epDayRows, err := db.QueryContext(ctx, fmt.Sprintf(`
		SELECT date(%s, 'unixepoch', 'localtime') AS d,
		       COALESCE(SUM(CASE WHEN %s THEN 1 ELSE 0 END), 0) AS bg_doc_cnt,
		       COALESCE(SUM(CASE WHEN NOT (%s) THEN 1 ELSE 0 END), 0) AS ep_cnt
		FROM thin_vault_docs
		WHERE %s >= ?
		GROUP BY d
	`, docTsExpr, bgDocCond, bgDocCond, docTsExpr), floor14d)
	if err == nil {
		for epDayRows.Next() {
			var d string
			var bgDocCnt, epCnt int64
			if epDayRows.Scan(&d, &bgDocCnt, &epCnt) == nil {
				if b, ok := dailyMap[d]; ok {
					b.EpisodicCount += epCnt
					b.BackgroundCount += bgDocCnt
				}
			}
		}
		epDayRows.Close()
	}
	bgDayRows, err := db.QueryContext(ctx, fmt.Sprintf(`
		SELECT date(%s, 'unixepoch', 'localtime') AS d, COUNT(*)
		FROM thin_vault_bg_chunks
		WHERE %s >= ?
		GROUP BY d
	`, bgTsExpr, bgTsExpr), floor14d)
	if err == nil {
		for bgDayRows.Next() {
			var d string
			var cnt int64
			if bgDayRows.Scan(&d, &cnt) == nil {
				if b, ok := dailyMap[d]; ok {
					b.BackgroundCount += cnt
				}
			}
		}
		bgDayRows.Close()
	}
	for d := 13; d >= 0; d-- {
		dayStr := now.AddDate(0, 0, -d).Format("2006-01-02")
		if b, ok := dailyMap[dayStr]; ok {
			out.dailySeries = append(out.dailySeries, *b)
		}
	}

	// Build Top Cited Units (Top 25) and resolve unit metadata + locus attribution.
	type unitMeta struct {
		layer       string
		locus       string
		addressURI  string
		waveformBar string
		occurredAt  int64
		rawText     string
		isBGChunk   bool
	}
	unitMetaMap := make(map[string]unitMeta, len(citedStats))
	allCitedUIDs := make([]string, 0, len(citedStats))
	for uid := range citedStats {
		allCitedUIDs = append(allCitedUIDs, uid)
	}
	const metaBatchSize = 256
	for start := 0; start < len(allCitedUIDs); start += metaBatchSize {
		end := start + metaBatchSize
		if end > len(allCitedUIDs) {
			end = len(allCitedUIDs)
		}
		chunk := allCitedUIDs[start:end]
		ph := strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",")
		args := make([]any, len(chunk))
		for i, uid := range chunk {
			args[i] = uid
		}
		if dRows, dErr := db.QueryContext(ctx, `
				SELECT doc_id, coalesce(layer, 'episodic'), coalesce(locus, ''), coalesce(address_uri, ''),
				       coalesce(waveform_bar, ''), occurred_at, raw_text
				FROM thin_vault_docs WHERE doc_id IN (`+ph+`)
			`, args...); dErr == nil {
			for dRows.Next() {
				var id string
				var m unitMeta
				if dRows.Scan(&id, &m.layer, &m.locus, &m.addressURI, &m.waveformBar, &m.occurredAt, &m.rawText) == nil {
					unitMetaMap[id] = m
				}
			}
			dRows.Close()
		}
		var missingBG []any
		for _, uid := range chunk {
			if _, ok := unitMetaMap[uid]; !ok {
				missingBG = append(missingBG, uid)
			}
		}
		if len(missingBG) > 0 {
			bgPH := strings.TrimSuffix(strings.Repeat("?,", len(missingBG)), ",")
			if bRows, bErr := db.QueryContext(ctx, `
					SELECT unit_id, coalesce(abs_path, ''), coalesce(rel_path, ''), coalesce(anchor, ''), coalesce(address_uri, ''),
					       coalesce(waveform_bar, ''), occurred_at
					FROM thin_vault_bg_chunks WHERE unit_id IN (`+bgPH+`)
				`, missingBG...); bErr == nil {
				for bRows.Next() {
					var id, absPath, relPath, anchor string
					var m unitMeta
					if bRows.Scan(&id, &absPath, &relPath, &anchor, &m.addressURI, &m.waveformBar, &m.occurredAt) == nil {
						m.layer = "background"
						m.isBGChunk = true
						if absPath != "" {
							m.locus = filepath.Base(filepath.Dir(absPath))
							m.rawText = fmt.Sprintf("[%s#%s] (%s)", relPath, anchor, absPath)
						} else {
							m.rawText = relPath + " " + anchor
						}
						unitMetaMap[id] = m
					}
				}
				bRows.Close()
			}
		}
	}

	// 零拷贝指针与 L1 分块先走引擎守护进程的规范批量水合，再进下方缓存读；
	// 否则冷缓存会让证据抽屉退化成片段预览或仅显示路径坐标。
	var refRows []map[string]any
	var bgDocRows []map[string]any
	for uid, m := range unitMetaMap {
		if raw := strings.TrimSpace(m.rawText); strings.HasPrefix(raw, "@ref:") {
			refRows = append(refRows, map[string]any{"content": raw})
		} else if m.isBGChunk {
			bgDocRows = append(bgDocRows, map[string]any{"src": "chunk", "id": uid})
		}
	}
	if len(refRows) > 0 {
		warmHydrateZCodePointersBatch(home, refRows)
	}
	if len(bgDocRows) > 0 {
		warmHydrateLocalDocumentsBatch(home, bgDocRows)
	}

	// Expand every cited unit of the L3 stream with its full stored/hydrated text
	// so the evidence drawer can render complete content for each citation.
	hydrationCache := make(map[string]string, len(unitMetaMap))
	resolveFullText := func(uid string) (string, bool, bool) {
		m, ok := unitMetaMap[uid]
		if !ok {
			return "", false, false
		}
		stored := strings.TrimSpace(m.rawText)
		if cached, seen := hydrationCache[uid]; seen {
			return cached, cached != "" && cached != stored, strings.HasPrefix(stored, "@ref:")
		}
		if m.isBGChunk {
			full := stored
			hydrated := false
			if body, ok := hydratedLocalDocumentLive(home, uid); ok && strings.TrimSpace(body) != "" {
				full = body
				hydrated = true
			}
			hydrationCache[uid] = full
			return full, hydrated, false
		}
		isRef := strings.HasPrefix(stored, "@ref:")
		full := stored
		if isRef {
			// 指针行只回水合正文：无法水合时留空（前端回退片段预览 + 「宿主原文已清理」提示），
			// 绝不把原始 `@ref:` 指针当正文下发。
			full = ""
			if hydrated, ok := hydratedEpisodicPointer(home, stored); ok && hydrated != "" {
				full = hydrated
			}
		}
		hydrationCache[uid] = full
		return full, full != "" && full != stored, isRef
	}
	for i := range allNoetics {
		ev := &allNoetics[i]
		if len(ev.CitedUnitIDs) == 0 {
			ev.CitedUnits = []roiCitedUnitDetail{}
			continue
		}
		details := make([]roiCitedUnitDetail, 0, len(ev.CitedUnitIDs))
		for _, rawUID := range ev.CitedUnitIDs {
			uid := strings.TrimSpace(rawUID)
			if uid == "" {
				continue
			}
			m := unitMetaMap[uid]
			layer := m.layer
			if layer == "" {
				if strings.HasPrefix(uid, "bg:") {
					layer = "background"
				} else {
					layer = "episodic"
				}
			}
			full, hydrated, isRef := resolveFullText(uid)
			details = append(details, roiCitedUnitDetail{
				UnitID:        uid,
				Layer:         layer,
				Locus:         m.locus,
				AddressURI:    m.addressURI,
				StoredText:    strings.TrimSpace(m.rawText),
				FullText:      clipRunes(full, 8192),
				IsHydrated:    hydrated,
				IsZeroCopyRef: isRef,
			})
		}
		ev.CitedUnits = details
	}
	adoptSummary := auditNoeticStreamAdoption(ctx, db, home, memoryPath, allNoetics)
	vault.AuditedRecalls = adoptSummary.AuditedRecalls
	vault.AdoptedRecalls = adoptSummary.AdoptedRecalls
	vault.AdoptedToolCount = adoptSummary.AdoptedToolCount
	vault.AdoptedContextCount = adoptSummary.AdoptedContextCount
	vault.EchoExcludedTotal = adoptSummary.EchoExcludedTotal
	vault.AdoptionRatePct = adoptSummary.AdoptionRatePct

	out.noeticStream = out.noeticStream[:0]
	for i := range allNoetics {
		if len(out.noeticStream) >= 50 {
			break
		}
		out.noeticStream = append(out.noeticStream, allNoetics[i])
	}

	// Propagate per-Noetic adoption outcomes across ALL Noetic events to rolling windows and cited underlying units.
	noeticAdoptByID := make(map[string]roiNoeticEvent, len(allNoetics))
	for _, ev := range allNoetics {
		noeticAdoptByID[ev.NoeticID] = ev
		if ev.Status != "active" {
			continue
		}
		for _, sp := range specs {
			if ev.IngestAt >= sp.Floor {
				w := winMap[sp.Key]
				switch ev.AdoptionOutcome {
				case "adopted_tool":
					w.AuditedRecalls++
					w.AdoptedRecalls++
					w.AdoptedToolCount++
				case "adopted_context":
					w.AuditedRecalls++
					w.AdoptedRecalls++
					w.AdoptedContextCount++
				case "unadopted":
					w.AuditedRecalls++
				}
			}
		}
	}
	for _, sp := range specs {
		w := winMap[sp.Key]
		if w.AuditedRecalls > 0 {
			w.AdoptionRatePct = roundFloat(float64(w.AdoptedRecalls)*100.0/float64(w.AuditedRecalls), 1)
		}
	}
	for _, st := range citedStats {
		for idx := range st.citations {
			c := &st.citations[idx]
			if ev, ok := noeticAdoptByID[c.NoeticID]; ok {
				c.AdoptionOutcome = ev.AdoptionOutcome
				c.MatchedAnchors = ev.MatchedAnchors
				c.AssistantID = ev.AssistantID
				if ev.AdoptionOutcome == "adopted_tool" || ev.AdoptionOutcome == "adopted_context" {
					st.adoptedCount++
				}
			}
		}
	}

	type rankedCite struct {
		uid string
		st  *citeStat
	}
	rankedCites := make([]rankedCite, 0, len(citedStats))
	for uid, st := range citedStats {
		rankedCites = append(rankedCites, rankedCite{uid: uid, st: st})
	}
	sort.Slice(rankedCites, func(i, j int) bool {
		if rankedCites[i].st.count != rankedCites[j].st.count {
			return rankedCites[i].st.count > rankedCites[j].st.count
		}
		if rankedCites[i].st.lastAt != rankedCites[j].st.lastAt {
			return rankedCites[i].st.lastAt > rankedCites[j].st.lastAt
		}
		return rankedCites[i].uid < rankedCites[j].uid
	})
	if len(rankedCites) > 25 {
		rankedCites = rankedCites[:25]
	}
	for _, rc := range rankedCites {
		uid, st := rc.uid, rc.st
		m, existsInVault := unitMetaMap[uid]
		layer := m.layer
		if layer == "" {
			if strings.HasPrefix(uid, "bg:") {
				layer = "background"
			} else {
				layer = "episodic"
			}
		}
		fullText, isHydrated, isRef := resolveFullText(uid)
		if !existsInVault && fullText == "" {
			fullText = st.firstSnippet
		}
		preview := fullText
		if (isRef || m.isBGChunk) && !isHydrated && st.firstSnippet != "" {
			preview = st.firstSnippet
		}
		if preview == "" {
			preview = st.firstSnippet
		}
		if preview == "" {
			preview = uid
		}
		out.topCitedUnits = append(out.topCitedUnits, roiTopCitedUnit{
			UnitID:        uid,
			Layer:         layer,
			Locus:         m.locus,
			AddressURI:    m.addressURI,
			WaveformBar:   m.waveformBar,
			CitationCount: st.count,
			AdoptedCount:  st.adoptedCount,
			LastCitedAt:   st.lastAt,
			OccurredAt:    m.occurredAt,
			Preview:       clipRunes(preview, 240),
			FullText:      clipRunes(fullText, 8192),
			IsHydrated:    isHydrated,
			Tokens:        int64(estimateTokenzRCount(fullText)),
			IsZeroCopyRef: isRef,
			RecentQueries: st.queries,
			Citations:     st.citations,
		})
	}

	// Build Workspace (Locus) Coverage & Heat Matrix.
	locusMap := make(map[string]*roiLocusRow)
	ensureLocus := func(raw string) *roiLocusRow {
		k := strings.TrimSpace(raw)
		if k == "" {
			k = "(global / unscoped)"
		}
		row, ok := locusMap[k]
		if !ok {
			row = &roiLocusRow{Locus: k, Status: "cold"}
			locusMap[k] = row
		}
		return row
	}
	locRows, err := db.QueryContext(ctx, `
		SELECT coalesce(layer, 'episodic'), coalesce(locus, ''), COUNT(*), COALESCE(MAX(occurred_at), 0)
		FROM thin_vault_docs
		GROUP BY 1, 2
	`)
	if err == nil {
		for locRows.Next() {
			var layer, loc string
			var cnt, maxAt int64
			if locRows.Scan(&layer, &loc, &cnt, &maxAt) != nil {
				continue
			}
			r := ensureLocus(loc)
			if strings.EqualFold(layer, "background") {
				r.BackgroundCount += cnt
			} else {
				r.EpisodicCount += cnt
			}
			if maxAt > r.LastActiveAt {
				r.LastActiveAt = maxAt
			}
		}
		locRows.Close()
	}
	if vault.BackgroundChunks > 0 {
		// 优先按 thin_vault_bg_files 的 chunk_count 与 mtime_ns 聚合，避免每帧对 5.6 万分块做
		// c.abs_path = f.abs_path 文本键三表 JOIN；仅当单测夹具未填 chunk_count/mtime_ns 时回退子查询。
		bgDirRows, bgErr := db.QueryContext(ctx, `
			SELECT
				d.root_path,
				CASE
					WHEN COALESCE(SUM(f.chunk_count), 0) > 0 THEN SUM(f.chunk_count)
					ELSE (SELECT COUNT(c.unit_id) FROM thin_vault_bg_files f2 JOIN thin_vault_bg_chunks c ON c.abs_path = f2.abs_path WHERE f2.dir_id = d.dir_id)
				END AS chunk_cnt,
				COALESCE(
					NULLIF(MAX(f.mtime_ns / 1000000000), 0),
					(SELECT MAX(c.occurred_at) FROM thin_vault_bg_files f2 JOIN thin_vault_bg_chunks c ON c.abs_path = f2.abs_path WHERE f2.dir_id = d.dir_id),
					0
				) AS max_at
			FROM thin_vault_bg_dirs d
			LEFT JOIN thin_vault_bg_files f ON f.dir_id = d.dir_id
			GROUP BY d.dir_id, d.root_path
		`)
		if bgErr == nil {
			for bgDirRows.Next() {
				var rootPath string
				var chunkCnt, maxAt int64
				if bgDirRows.Scan(&rootPath, &chunkCnt, &maxAt) == nil && chunkCnt > 0 {
					r := ensureLocus(filepath.Base(rootPath))
					r.BackgroundCount += chunkCnt
					if maxAt > r.LastActiveAt {
						r.LastActiveAt = maxAt
					}
				}
			}
			bgDirRows.Close()
		}
	}
	for uid, st := range citedStats {
		m := unitMetaMap[uid]
		r := ensureLocus(m.locus)
		r.CitedUnitCount++
		r.CitationHits += st.count
		r.AdoptedHits += st.adoptedCount
		if st.lastAt > r.LastActiveAt {
			r.LastActiveAt = st.lastAt
		}
	}
	for _, r := range locusMap {
		if r.CitationHits > 0 {
			r.Status = "hot"
		} else if r.LastActiveAt >= nowUnix-86400 {
			r.Status = "warm"
		} else {
			r.Status = "cold"
		}
		out.locusMatrix = append(out.locusMatrix, *r)
	}
	sort.Slice(out.locusMatrix, func(i, j int) bool {
		if out.locusMatrix[i].CitationHits != out.locusMatrix[j].CitationHits {
			return out.locusMatrix[i].CitationHits > out.locusMatrix[j].CitationHits
		}
		totI := out.locusMatrix[i].EpisodicCount + out.locusMatrix[i].BackgroundCount
		totJ := out.locusMatrix[j].EpisodicCount + out.locusMatrix[j].BackgroundCount
		if totI != totJ {
			return totI > totJ
		}
		return out.locusMatrix[i].Locus < out.locusMatrix[j].Locus
	})

	for _, sp := range specs {
		w := winMap[sp.Key]
		w.UniqueCitedUnits = int64(len(winCitedSets[sp.Key]))
		out.vaultWindows = append(out.vaultWindows, *w)
	}
	out.vault = vault
	return out, sig
}

// queryROIFacts reads a single SQLite snapshot. Only explicitly selected scopes
// are included; unassigned scopes and harnesses never enter a selected bucket.
func queryROIFacts(ctx context.Context, memoryPath string, req roiFactsRequest) roiFactsResponse {
	scope := strings.TrimSpace(req.Scope)
	harness := strings.TrimSpace(req.Harness)
	if scope == "" {
		scope = UserScope
	}
	altScope := scope
	if scope == UserScope {
		altScope = LegacyUserScope
	} else if scope == LegacyUserScope {
		altScope = UserScope
	}
	bundle := readROIFactsVaultBundle(memoryPath)
	attachBundle := func(resp roiFactsResponse) roiFactsResponse {
		resp.Vault = bundle.vault
		resp.VaultWindows = bundle.vaultWindows
		resp.DailySeries = bundle.dailySeries
		resp.TopCitedUnits = bundle.topCitedUnits
		resp.LocusMatrix = bundle.locusMatrix
		resp.NoeticStream = bundle.noeticStream
		return resp
	}
	unknown := func(reason string) roiFactsResponse {
		return attachBundle(roiFactsUnknown(scope, harness, reason))
	}
	if scope == "ALL" || scope == "ALL_WITH_EVAL" || strings.TrimSpace(memoryPath) == "" || memoryPath == ":memory:" || strings.HasPrefix(memoryPath, "file:") || strings.ContainsAny(memoryPath, "?#") {
		return unknown("invalid_selection")
	}
	path, err := filepath.Abs(memoryPath)
	if err != nil {
		return unknown("database_unavailable")
	}
	path += ".efficacy.sqlite"
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return unknown("ledger_missing")
	}
	if err != nil || !info.Mode().IsRegular() {
		return unknown("database_unavailable")
	}
	db, release := borrowVaultReadOnly(path)
	if db == nil {
		return unknown("database_unavailable")
	}
	defer release()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return unknown("database_unavailable")
	}
	defer tx.Rollback()
	var version int
	if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return unknown("database_unavailable")
	}
	if version != 1 {
		return unknown("schema_version")
	}

	limit := req.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	// A correction replaces its original observation; a retraction removes it.
	// The scope and harness must match both the original and its revision.
	const effective = `WITH effective AS (
			SELECT seq, action, harness, hook, outcome, observation, text_bytes, observed_at, effective_seq
			FROM (
				SELECT e.seq, e.event_id, e.action, e.harness, e.hook, COALESCE(r.output_outcome, e.output_outcome) AS outcome,
					COALESCE(r.observation, e.observation) AS observation,
					CASE WHEN r.seq IS NULL THEN e.text_bytes ELSE r.text_bytes END AS text_bytes,
					COALESCE(r.observed_at, e.observed_at) AS observed_at,
					COALESCE(r.seq, e.seq) AS effective_seq
				FROM efficacy_event e
				LEFT JOIN efficacy_event r ON r.seq = (
					SELECT x.seq FROM efficacy_event x
					WHERE x.target_event_id = e.event_id AND x.scope = e.scope
						AND x.harness = e.harness AND x.session = e.session AND x.hook = e.hook
						AND x.execution_id = e.execution_id AND x.action IN ('correction', 'retraction')
					ORDER BY x.seq DESC LIMIT 1)
					WHERE e.scope IN (?, ?) AND e.scope != '' AND (? = '' OR e.harness = ?)
						AND e.action IN ('decision', 'prepared', 'output_attempt', 'recall')
						AND (r.seq IS NULL OR r.action != 'retraction')
						AND (e.hook != 'mcp_recall:tool' OR NOT EXISTS (
							SELECT 1 FROM efficacy_event d
							WHERE d.scope = e.scope
							  AND d.action = 'recall'
							  AND d.hook LIKE 'mcp_recall:%'
							  AND d.hook != 'mcp_recall:tool'
							  AND ABS(d.observed_at - e.observed_at) <= 2000000000
						))
				)
				GROUP BY event_id
				)`
	counts := &roiFactCounts{Actions: map[string]int64{}, DecisionOutcomes: map[string]int64{}, OutputOutcomes: map[string]int64{}, RecallOutcomes: map[string]int64{}}
	rows, err := tx.QueryContext(ctx, effective+` SELECT action, outcome, observation, COUNT(*),
		COUNT(CASE WHEN observation = 'observed' AND text_bytes >= 0 THEN 1 END),
		COALESCE(SUM(CASE WHEN observation = 'observed' AND text_bytes >= 0 THEN text_bytes ELSE 0 END), 0)
		FROM effective GROUP BY action, outcome, observation`, scope, altScope, harness, harness)
	if err != nil {
		return unknown("schema_or_query_error")
	}
	for rows.Next() {
		var action, outcome, observation string
		var total, known, bytes int64
		if err := rows.Scan(&action, &outcome, &observation, &total, &known, &bytes); err != nil {
			rows.Close()
			return unknown("schema_or_query_error")
		}
		if !validROIFactOutcome(action, outcome) || (observation != "observed" && observation != "unknown") || bytes > int64(^uint64(0)>>1)-counts.KnownTextBytes {
			rows.Close()
			return unknown("invalid_event")
		}
		counts.Total += total
		counts.Actions[action] += total
		switch action {
		case "decision":
			counts.DecisionOutcomes[outcome] += total
		case "output_attempt":
			counts.OutputOutcomes[outcome] += total
		case "recall":
			counts.RecallOutcomes[outcome] += total
		}
		counts.KnownTextBytes += bytes
		counts.KnownByteEvents += known
		counts.UnknownByteEvents += total - known
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return unknown("database_unavailable")
	}
	rows.Close()
	if counts.Total == 0 {
		return unknown("no_matching_events")
	}
	windows, ok := queryROIFactWindows(ctx, tx, effective, scope, altScope, harness, bundle.vaultWindows)
	if !ok {
		return unknown("schema_or_query_error")
	}
	rows, err = tx.QueryContext(ctx, effective+` SELECT action, outcome, observation, harness, hook, text_bytes, observed_at FROM effective ORDER BY effective_seq DESC LIMIT ?`, scope, altScope, harness, harness, limit)
	if err != nil {
		return unknown("schema_or_query_error")
	}
	recent := []roiFactEvent{}
	for rows.Next() {
		var item roiFactEvent
		var bytes sql.NullInt64
		if err := rows.Scan(&item.Action, &item.Outcome, &item.Observation, &item.Harness, &item.Hook, &bytes, &item.ObservedAt); err != nil {
			rows.Close()
			return unknown("schema_or_query_error")
		}
		if item.ObservedAt > 0 {
			item.ObservedAt /= int64(time.Second)
		}
		if item.Observation == "observed" && bytes.Valid && bytes.Int64 >= 0 {
			item.TextBytes = &bytes.Int64
		}
		recent = append(recent, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return unknown("database_unavailable")
	}
	rows.Close()
	identityStatus := "unknown"
	var storedFP string
	if err := tx.QueryRowContext(ctx, `SELECT value FROM efficacy_meta WHERE key = 'db_fingerprint'`).Scan(&storedFP); err == nil && storedFP != "" {
		if expectedFP := computeAdminDBFingerprint(memoryPath, scope); expectedFP != "" && storedFP == expectedFP {
			identityStatus = "verified"
		}
	}
	return attachBundle(roiFactsResponse{
		Status:         "available",
		Source:         "admin_selected_db",
		Coverage:       "unknown",
		IdentityStatus: identityStatus,
		Scope:          scope,
		Harness:        harness,
		Counts:         counts,
		Windows:        windows,
		RecentEvents:   recent,
	})
}

// validROIFactOutcome keeps the closed outcome set per action; an unknown action
// or outcome means the ledger was written by something this reader cannot
// honestly interpret, so the whole snapshot is refused.
func validROIFactOutcome(action, outcome string) bool {
	switch action {
	case "decision":
		return outcome == "output" || outcome == "empty" || outcome == "error" || outcome == "unknown"
	case "prepared":
		return outcome == "not_applicable"
	case "output_attempt":
		return outcome == "success" || outcome == "failure" || outcome == "short_write" || outcome == "unknown"
	case "recall":
		return outcome == "hits" || outcome == "empty" || outcome == "error" || outcome == "unknown"
	}
	return false
}

// queryROIFactWindows folds the settled events into the fixed rolling windows
// and merges any live vault window metrics for the same keys.
func queryROIFactWindows(ctx context.Context, tx *sql.Tx, effective string, scope, altScope, harness string, vaultWins []roiFactWindow) ([]roiFactWindow, bool) {
	now := time.Now().Unix()
	buckets := map[string]*roiFactWindow{}
	order := make([]string, 0, len(roiFactWindows))
	vaultByKey := make(map[string]roiFactWindow, len(vaultWins))
	for _, vw := range vaultWins {
		vaultByKey[vw.Key] = vw
	}
	for _, window := range roiFactWindows {
		vw := vaultByKey[window.Key]
		buckets[window.Key] = &roiFactWindow{
			Key:                 window.Key,
			Seconds:             window.Seconds,
			SinceUnix:           now - window.Seconds,
			Actions:             map[string]int64{},
			RecallOutcomes:      map[string]int64{},
			EpisodicAdded:       vw.EpisodicAdded,
			BackgroundAdded:     vw.BackgroundAdded,
			NoeticCrystallized:  vw.NoeticCrystallized,
			CitationLinks:       vw.CitationLinks,
			UniqueCitedUnits:    vw.UniqueCitedUnits,
			DeliveredTokens:     vw.DeliveredTokens,
			AuditedRecalls:      vw.AuditedRecalls,
			AdoptedRecalls:      vw.AdoptedRecalls,
			AdoptedToolCount:    vw.AdoptedToolCount,
			AdoptedContextCount: vw.AdoptedContextCount,
			AdoptionRatePct:     vw.AdoptionRatePct,
		}
		order = append(order, window.Key)
	}
	floor := (now - roiFactWindows[len(roiFactWindows)-1].Seconds) * int64(time.Second)
	rows, err := tx.QueryContext(ctx, effective+` SELECT action, outcome, observation, text_bytes, observed_at FROM effective WHERE observed_at >= ?`, scope, altScope, harness, harness, floor)
	if err != nil {
		return nil, false
	}
	defer rows.Close()
	for rows.Next() {
		var action, outcome, observation string
		var bytes sql.NullInt64
		var observedAt int64
		if err := rows.Scan(&action, &outcome, &observation, &bytes, &observedAt); err != nil {
			return nil, false
		}
		for _, key := range order {
			window := buckets[key]
			if observedAt < window.SinceUnix*int64(time.Second) {
				continue
			}
			window.Total++
			window.Actions[action]++
			if action == "recall" {
				window.RecallOutcomes[outcome]++
			}
			if observation == "observed" && bytes.Valid && bytes.Int64 >= 0 {
				window.KnownTextBytes += bytes.Int64
			} else {
				window.UnknownByteEvents++
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, false
	}
	windows := make([]roiFactWindow, 0, len(order))
	for _, key := range order {
		windows = append(windows, *buckets[key])
	}
	return windows, true
}
