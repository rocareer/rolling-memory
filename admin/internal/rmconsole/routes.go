// Package rollingmemory 是 Rolling-Memory 管理后台的原生内置路由与服务模块（admin/internal/rmconsole）。
// 它直接通过 router/registry 静态挂载，作为 rm 核心的内置管理控制台。
package rollingmemory

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ai-go-hub/ai-go-admin/internal/infra/config"
	"github.com/ai-go-hub/ai-go-admin/internal/kit/httpx"
	"github.com/ai-go-hub/ai-go-admin/internal/middleware"
	"github.com/ai-go-hub/ai-go-admin/internal/router/registry"
	"github.com/ai-go-hub/ai-go-admin/internal/version"

	"github.com/gin-gonic/gin"
)

const (
	// Name 管理后台模块名（= 路由段 plugin/<名>；URL 前缀为兼容存量菜单与权限节点而保留，
	// 模块本身经 registry.RegisterAdmin 静态挂载，不是插件）。
	Name = "rolling-memory"
	// Version 软件版本（随 VERSION 文件同步）。
	Version = "0.0.1"
	// CoreVersion 记忆核心版本（Rolling Autonoetic Memory Engine · the memory engine）
	CoreVersion = "1.0.1"
	// CoreCodename 记忆核心引擎代号（the memory engine）
	CoreCodename = "rame"
	// Kind 模块形态标记（状态面展示用；模块本体为 admin 原生内置模块，不是插件）。
	Kind = "code"
	// HostAPIVersion 宿主接口协议版本（状态面展示用）。
	HostAPIVersion = "1.0"
	// modeLocal / modeCloud 引擎运行模式取值（与引擎 `[mode] name` 对齐，见 internal/config/config.go validModes）。
	modeLocal = "local"
	modeCloud = "cloud"
)

var (
	widgetMu      sync.RWMutex
	widgetEnabled = true

	bgTaskMu     sync.RWMutex
	bgTaskCancel context.CancelFunc
	bgTaskState  = BgTaskStatus{Status: "idle"}
)

func snapshotBgTask() BgTaskStatus {
	bgTaskMu.RLock()
	defer bgTaskMu.RUnlock()
	return bgTaskState
}

// rollingModeForHome 解析记忆引擎运行模式（local | cloud），口径与引擎 `[mode] name` 一致：
// ROLLING_MEMORY_MODE → `<home>/.config/rolling-memory/config.toml` 的 `[mode] name` → 默认 local。
func rollingModeForHome(home string) string {
	if v := strings.ToLower(strings.TrimSpace(os.Getenv("ROLLING_MEMORY_MODE"))); v != "" {
		return v
	}
	cfgPath := config.RollingConfigPath()
	if strings.TrimSpace(os.Getenv("ROLLING_MEMORY_CONFIG")) == "" && strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")) == "" && strings.TrimSpace(home) != "" {
		cfgPath = filepath.Join(home, ".config", "rolling-memory", "config.toml")
	}
	if v := strings.ToLower(strings.TrimSpace(config.ParseRollingConfig(cfgPath)["mode.name"])); v != "" {
		return v
	}
	return modeLocal
}

// isLocalModeForHome 报告记忆引擎是否运行在本地模式。只有显式 `cloud` 才走云端路径，
// 其余取值（含缺省与未知）一律按本地模式处置 —— 本地模式在控制台侧是零外呼硬边界。
func isLocalModeForHome(home string) bool {
	return rollingModeForHome(home) != modeCloud
}

func collectArtifactStats(dbPath, scope string) ArtifactStats {
	if scope == "" || scope == LegacyUserScope {
		scope = UserScope
	}
	st := ArtifactStats{
		UpstreamEngine:    "Rolling Autonoetic Memory Engine (the memory engine)",
		UpstreamUserID:    defaultUpstreamDevUserID,
		UpstreamEndpoint:  defaultCloudServerURL + "/open/memory/v1",
		TraceDigestionPct: 100.0,
	}
	// 本地模式没有任何远端端点可展示：如实上报本地金库坐标，绝不把云端地址当读数呈现。
	if home, err := os.UserHomeDir(); err == nil && isLocalModeForHome(home) {
		st.UpstreamEndpoint = "local://vault"
		st.UpstreamUserID = ""
	}
	if _, err := os.Stat(dbPath); err != nil {
		return st
	}
	var layerCounts LayerStatsResponse
	fillCachedLayerGlobalCounts(dbPath, &layerCounts)
	st.ThinVaultDocs = layerCounts.BackgroundCount + layerCounts.EpisodicCount
	// 全库去重 cell 统计（substr/instr + UNION + COUNT(DISTINCT)，单次 0.25–0.65s）
	// 走 TTL 缓存，不再随每次采样（浮标 1Hz / 状态帧随金库写入）重跑；见 artifact_stats_cache.go。
	st.ThinVaultCells = cachedArtifactCells(dbPath)
	st.ThinVaultNoetic = layerCounts.NoeticActiveCount
	st.ActiveAssertions = st.ThinVaultDocs + st.ThinVaultNoetic
	st.ReferentNodes = st.ThinVaultCells
	st.EligibleNodes = st.ThinVaultCells + st.ThinVaultDocs + st.ThinVaultNoetic
	return st
}

// liveSoftwareVersion reads the real-time software release version from the repository's
// root VERSION file (falling back to the compile-time Version constant) so that version
// bumps are reflected immediately on /dashboard, /status, /widget, and /terminal even before
// the rolling-admin binary is recompiled.
func liveSoftwareVersion() string {
	if raw, err := os.ReadFile("../VERSION"); err == nil {
		if v := strings.TrimSpace(string(raw)); v != "" && !strings.ContainsAny(v, " \t\r\n") {
			return v
		}
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if raw, err := os.ReadFile(filepath.Join(home, "Rolling-Memory", "VERSION")); err == nil {
			if v := strings.TrimSpace(string(raw)); v != "" && !strings.ContainsAny(v, " \t\r\n") {
				return v
			}
		}
	}
	return Version
}

func init() {
	registry.RegisterAdmin(func(admin *gin.RouterGroup) {
		mount(admin.Group("plugin/" + Name))
	})
	registry.Register(func(r *gin.Engine) {
		mountPublic(r.Group(""))
	})
}

// maxConsoleBodyBytes 是控制台请求体上限（JSON 绑定会把请求体读进内存；MCP 传输侧同口径 8 MiB）。
const maxConsoleBodyBytes = 8 << 20

// limitConsoleRequestBody 给每个请求体套上硬上限。作用域守卫只检查它审计的路由，
// 被豁免的 eval / ai / cloud account / locomo 路由必须同样有界，不能靠调用方自觉。
func limitConsoleRequestBody() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Body != nil && c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead {
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxConsoleBodyBytes)
		}
		c.Next()
	}
}

func mount(g *gin.RouterGroup) {
	g.Use(limitConsoleRequestBody())
	g.Use(consoleVaultScopeGuard())
	g.POST("/status", middleware.AdminAuth(), middleware.AdminPermission(), statusHandler)
	g.POST("/toggle", middleware.AdminAuth(), middleware.AdminPermission(), toggleHandler)
	g.POST("/widget", middleware.AdminAuth(), middleware.AdminPermission(), widgetHandler)
	g.POST("/dashboard", middleware.AdminAuth(), middleware.AdminPermission(), dashboardHandler)
	g.POST("/efficacy", middleware.AdminAuth(), middleware.AdminPermission(), efficacyHandler)
	g.POST("/roi/facts", middleware.AdminAuth(), middleware.AdminPermission(), roiFactsHandler)
	g.POST("/graph", middleware.AdminAuth(), middleware.AdminPermission(), graphHandler)
	g.POST("/graph/expand", middleware.AdminAuth(), middleware.AdminPermission(), graphExpandHandler)
	g.POST("/terminal", middleware.AdminAuth(), middleware.AdminPermission(), terminalHandler)
	g.POST("/memories/query", middleware.AdminAuth(), middleware.AdminPermission(), memoriesQueryHandler)
	g.POST("/memories/detail", middleware.AdminAuth(), middleware.AdminPermission(), memoriesDetailHandler)
	g.POST("/recall/query", middleware.AdminAuth(), middleware.AdminPermission(), recallQueryHandler)
	g.POST("/recall/recent", middleware.AdminAuth(), middleware.AdminPermission(), recallRecentHandler)
	g.POST("/eval/overview", middleware.AdminAuth(), middleware.AdminPermission(), evalOverviewHandler)
	g.POST("/eval/leaderboard", middleware.AdminAuth(), middleware.AdminPermission(), evalLeaderboardHandler)
	g.POST("/eval/atm/leaderboard", middleware.AdminAuth(), middleware.AdminPermission(), evalATMLeaderboardHandler)
	g.POST("/eval/atm/protocol", middleware.AdminAuth(), middleware.AdminPermission(), evalATMProtocolHandler)
	g.POST("/eval/ingest", middleware.AdminAuth(), middleware.AdminPermission(), evalIngestHandler)
	g.POST("/eval/run", middleware.AdminAuth(), middleware.AdminPermission(), evalRunHandler)
	g.POST("/eval/export", middleware.AdminAuth(), middleware.AdminPermission(), evalExportHandler)
	g.POST("/ai/config", middleware.AdminAuth(), middleware.AdminPermission(), aiConfigGetHandler)
	g.POST("/ai/save", middleware.AdminAuth(), middleware.AdminPermission(), aiConfigSaveHandler)

	// 三层认知记忆与 Background 管理
	g.POST("/layers/stats", middleware.AdminAuth(), middleware.AdminPermission(), layersStatsHandler)
	g.POST("/layers/list", middleware.AdminAuth(), middleware.AdminPermission(), layersStatsHandler)
	g.GET("/layers/get/:pk", middleware.AdminAuth(), middleware.AdminPermission(), layersGetHandler)
	g.POST("/layers/get", middleware.AdminAuth(), middleware.AdminPermission(), layersGetHandler)
	g.POST("/layers/delete", middleware.AdminAuth(), middleware.AdminPermission(), layersDeleteHandler)
	g.POST("/env/health", middleware.AdminAuth(), middleware.AdminPermission(), envHealthHandler)
	g.POST("/env/health/refresh", middleware.AdminAuth(), middleware.AdminPermission(), envHealthRefreshHandler)
	g.POST("/sync/reconcile", middleware.AdminAuth(), middleware.AdminPermission(), syncReconcileHandler)
	g.POST("/background/dirs", middleware.AdminAuth(), middleware.AdminPermission(), backgroundDirsHandler)
	g.POST("/background/files", middleware.AdminAuth(), middleware.AdminPermission(), backgroundFilesHandler)
	g.POST("/background/select", middleware.AdminAuth(), middleware.AdminPermission(), backgroundSelectHandler)
	g.POST("/background/add", middleware.AdminAuth(), middleware.AdminPermission(), backgroundAddHandler)
	g.POST("/background/remove", middleware.AdminAuth(), middleware.AdminPermission(), backgroundRemoveHandler)
	g.POST("/background/sync", middleware.AdminAuth(), middleware.AdminPermission(), backgroundSyncHandler)
	g.POST("/system/version", middleware.AdminAuth(), middleware.AdminPermission(), systemVersionHandler)
	g.POST("/system/update/check", middleware.AdminAuth(), middleware.AdminPermission(), systemUpdateCheckHandler)
	g.POST("/system/update/apply", middleware.AdminAuth(), middleware.AdminPermission(), systemUpdateApplyHandler)

	mountCloudAccountRoutes(g)
	triggerConsoleStartupPrewarm()
}

var startupPrewarmOnce sync.Once

func triggerConsoleStartupPrewarm() {
	if strings.HasSuffix(os.Args[0], ".test") || strings.Contains(os.Args[0], ".test.") {
		return
	}
	startupPrewarmOnce.Do(func() {
		go func() {
			home, err := os.UserHomeDir()
			if err != nil || home == "" {
				return
			}
			dbPath := resolveMemoryDBPath(home)
			if st, sErr := os.Stat(dbPath); sErr != nil || st.Size() <= 0 {
				return
			}
			_, _ = cachedVaultPointerDocRows(dbPath, layerListQuery{})
			_ = collectDashboardData(home, UserScope)
			_ = readROIFactsVaultBundle(dbPath)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_, _ = cachedEnvHealthPayload(ctx, home, activeHarnessID(home), false)
		}()
	})
}

// Switches 开发调试监控开关集合（提供给用户在面板中实时开闭，100% 向量无关 · LLM 无关）。
type Switches struct {
	BootContext   bool   `json:"boot_context"`
	Ledger        bool   `json:"ledger"`
	RawWhenSparse bool   `json:"raw_when_sparse"`
	Mode          string `json:"mode"`
	CloudAutoSync bool   `json:"cloud_auto_sync"`
	WidgetEnabled bool   `json:"widget_enabled"`
}

// HookEventItem Hook 实时流水行（来自 ~/.local/state/rolling-memory/rolling-memory.log）。
type HookEventItem struct {
	Timestamp int64  `json:"ts"`
	TimeText  string `json:"time_text"`
	Event     string `json:"event"`
	Injected  int    `json:"injected"`
	Bands     int    `json:"bands"`
	Raw       int    `json:"raw"`
	ElapsedMS int    `json:"ms"`
	Chars     int    `json:"chars"`
	Dedup     int    `json:"dedup"`
	Absent    int    `json:"absent"`
	Nudge     int    `json:"nudge"`
	Imported  int    `json:"imported"`
	Replayed  int    `json:"replayed"`
	Written   int    `json:"written"`
	Outcome   string `json:"outcome"`
}

// RecentAssertion 最近写入的记忆单元摘要。
type RecentAssertion struct {
	ID         string  `json:"id"`
	Rendered   string  `json:"rendered"`
	Confidence float64 `json:"confidence"`
	Anchor     string  `json:"anchor"`
	Status     string  `json:"status"`
}

// ArtifactStats 薄客户端明文金库（thin_vault_docs / thin_vault_noetic）与 RAME 引擎实时统计控制台数据。
type ArtifactStats struct {
	TotalTraces       int     `json:"total_traces"`
	ProcessedTraces   int     `json:"processed_traces"`
	PendingTraces     int     `json:"pending_traces"`
	TraceDigestionPct float64 `json:"trace_digestion_pct"`
	ActiveAssertions  int     `json:"active_assertions"`
	ReferentNodes     int     `json:"referent_nodes"`
	EligibleNodes     int     `json:"eligible_nodes"`
	ThinVaultDocs     int     `json:"thin_vault_docs"`
	ThinVaultCells    int     `json:"thin_vault_cells"`
	ThinVaultNoetic   int     `json:"thin_vault_noetic"`
	UpstreamEngine    string  `json:"upstream_engine"`
	UpstreamUserID    string  `json:"upstream_user_id"`
	UpstreamEndpoint  string  `json:"upstream_endpoint"`
}

// BgTaskStatus 后台任务实时进度状态。
type BgTaskStatus struct {
	TaskType       string  `json:"task_type"`
	Running        bool    `json:"running"`
	Status         string  `json:"status"` // "idle" | "running" | "completed" | "failed" | "cancelled"
	Title          string  `json:"title"`
	TitleEn        string  `json:"title_en"`
	Done           int     `json:"done"`
	Total          int     `json:"total"`
	Percent        float64 `json:"percent"`
	AddedItems     int     `json:"added_items"`
	SpeedPerSec    float64 `json:"speed_per_sec"`
	ETASeconds     float64 `json:"eta_seconds"`
	ElapsedSeconds float64 `json:"elapsed_seconds"`
	Message        string  `json:"message"`
	MessageEn      string  `json:"message_en"`
	UpdatedAt      string  `json:"updated_at"`
}

// StatusResponse L1 实时监控面板契约。
type StatusResponse struct {
	Name              string            `json:"name"`
	Version           string            `json:"version"`
	CoreVersion       string            `json:"core_version"`
	CoreCodename      string            `json:"core_codename"`
	Kind              string            `json:"kind"`
	HostVersion       string            `json:"host_version"`
	HostAPIVersion    string            `json:"host_api_version"`
	BinaryPath        string            `json:"binary_path"`
	EngineOnline      bool              `json:"engine_online"`
	Scope             string            `json:"scope"`
	DBPath            string            `json:"db_path"`
	ActiveAssertions  int               `json:"active_assertions"`
	PendingTraces     int               `json:"pending_traces"`
	ConflictsProposed int               `json:"conflicts_proposed"`
	TotalTraces       int               `json:"total_traces"`
	TotalNodes        int               `json:"total_nodes"`
	LastRecallMS      int               `json:"last_recall_ms"`
	Switches          Switches          `json:"switches"`
	Artifacts         ArtifactStats     `json:"artifacts"`
	BgTask            BgTaskStatus      `json:"bg_task"`
	HookEvents        []HookEventItem   `json:"hook_events"`
	RecentAssertions  []RecentAssertion `json:"recent_assertions"`
	UpdatedAt         string            `json:"updated_at"`
}

// WidgetResponse 布局级右下角实时监控浮标契约。
type WidgetResponse struct {
	Enabled           bool    `json:"enabled"`
	Online            bool    `json:"online"`
	UpstreamOnline    bool    `json:"upstream_online"`
	UpstreamRTTMS     float64 `json:"upstream_rtt_ms"`
	UpstreamEndpoint  string  `json:"upstream_endpoint"`
	UpstreamEngine    string  `json:"upstream_engine"`
	Version           string  `json:"version"`
	CoreVersion       string  `json:"core_version"`
	CoreCodename      string  `json:"core_codename"`
	Mode              string  `json:"mode"`
	Scope             string  `json:"scope"`
	ActiveHarness     string  `json:"active_harness"`
	ActiveAssertions  int     `json:"active_assertions"`
	ConflictsProposed int     `json:"conflicts_proposed"`
	LastEvent         string  `json:"last_event"`
	LastEventTS       int64   `json:"last_event_ts"`
	EventCount        int     `json:"event_count"`
	LastRecallMS      int     `json:"last_recall_ms"`
	BootContext       bool    `json:"boot_context"`
	Ledger            bool    `json:"ledger"`
	BgTask            string  `json:"bg_task,omitempty"`
	SampledAtUnixMS   int64   `json:"sampled_at_ms"`
}

type toggleRequest struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

func statusHandler(c *gin.Context) {
	home, _ := os.UserHomeDir()
	httpx.Success(c, httpx.WithData(collectStatus(home, version.Get().Version, HostAPIVersion)))
}

func statusStreamSignature(home, dbPath, logPath string) string {
	var b strings.Builder
	cfgPath := filepath.Join(home, ".config", "rolling-memory", "config.toml")
	ledgerPath := dbPath + ".efficacy.sqlite"
	for _, p := range []string{cfgPath, dbPath, dbPath + "-wal", ledgerPath, ledgerPath + "-wal", logPath} {
		if st, err := os.Stat(p); err == nil {
			b.WriteString(p)
			b.WriteByte(':')
			b.WriteString(strconv.FormatInt(st.Size(), 10))
			b.WriteByte('@')
			b.WriteString(strconv.FormatInt(st.ModTime().UnixNano(), 10))
			b.WriteByte(';')
		}
	}
	widgetMu.RLock()
	wOn := widgetEnabled
	widgetMu.RUnlock()
	b.WriteString(strconv.FormatBool(wOn))
	b.WriteByte(';')
	bg := snapshotBgTask()
	b.WriteString(bg.TaskType)
	b.WriteByte(':')
	b.WriteString(bg.Status)
	b.WriteByte(':')
	b.WriteString(strconv.FormatBool(bg.Running))
	b.WriteByte(':')
	b.WriteString(strconv.Itoa(bg.Done))
	b.WriteByte(':')
	b.WriteString(strconv.FormatFloat(bg.Percent, 'f', 1, 64))
	b.WriteByte(':')
	b.WriteString(strconv.FormatFloat(bg.ElapsedSeconds, 'f', 1, 64))
	return b.String()
}

func probeUpstreamV1Health(ctx context.Context) (online bool, rttMS float64, engineName, endpoint string) {
	acct := loadCloudAccountState()
	base := strings.TrimRight(strings.TrimSpace(acct.BaseURL), "/")
	if base == "" {
		base = defaultCloudServerURL
	}
	if strings.HasSuffix(base, "/open/memory/v1") {
		endpoint = base
	} else if strings.HasSuffix(base, "/open/memory") {
		endpoint = base + "/v1"
	} else {
		endpoint = base + "/open/memory/v1"
	}
	reqCtx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	t0 := time.Now()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, endpoint+"/health", nil)
	if err != nil {
		return false, 0, "", endpoint
	}
	resp, err := http.DefaultClient.Do(req)
	elapsedMS := float64(time.Since(t0).Microseconds()) / 1000.0
	elapsedMS = float64(int(elapsedMS*10+0.5)) / 10.0
	if err != nil {
		return false, elapsedMS, "", endpoint
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, elapsedMS, "", endpoint
	}
	var payload struct {
		Status string `json:"status"`
		Engine string `json:"engine"`
	}
	if json.NewDecoder(resp.Body).Decode(&payload) == nil && payload.Status == "ok" {
		return true, elapsedMS, payload.Engine, endpoint
	}
	return true, elapsedMS, "autonoetic", endpoint
}

// probeLocalVaultHealth 测量本地明文金库可用性与往返时延（local 模式浮标 1Hz 采样用；零外呼）。
func probeLocalVaultHealth(home string) (online bool, rttMS float64, engineName, endpoint string) {
	endpoint = "local://vault"
	t0 := time.Now()
	rows, err := sqliteQueryJSON(resolveMemoryDBPath(home), `SELECT 1 AS ok;`)
	elapsedMS := float64(time.Since(t0).Microseconds()) / 1000.0
	elapsedMS = float64(int(elapsedMS*10+0.5)) / 10.0
	if err != nil || len(rows) == 0 {
		return false, elapsedMS, "", endpoint
	}
	return true, elapsedMS, "local-vault", endpoint
}

// widgetHandler 浮标的一次性快照端点（首屏与手动刷新用）；实时曲线改由 status 流的 1Hz `probe` 帧驱动，
// 两者共用 buildWidgetSnapshot，口径一致。
func widgetHandler(c *gin.Context) {
	home, _ := os.UserHomeDir()
	st := collectStatus(home, version.Get().Version, HostAPIVersion)
	httpx.Success(c, httpx.WithData(buildWidgetSnapshot(c.Request.Context(), home, st)))
}

func toggleHandler(c *gin.Context) {
	var req toggleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Fail(c, httpx.WithMessage("参数格式错误: "+err.Error()))
		return
	}
	home, _ := os.UserHomeDir()
	if err := applySwitchToggle(home, req.Key, req.Value); err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	httpx.Success(c, httpx.WithData(collectStatus(home, version.Get().Version, HostAPIVersion)))
}

func resolveBinary(home string) string {
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	candidates := []string{
		os.Getenv("ROLLING_MEMORY_BIN"),
		filepath.Join(home, ".local", "bin", "rolling-memory"+ext),
		filepath.Join(home, ".local", "share", "rolling-memory", "bin", "rolling-memory"+ext),
	}
	if xdg := strings.TrimSpace(os.Getenv("XDG_DATA_HOME")); xdg != "" {
		candidates = append(candidates, filepath.Join(xdg, "rolling-memory", "bin", "rolling-memory"+ext))
	}
	if localAppData := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); localAppData != "" {
		candidates = append(candidates, filepath.Join(localAppData, "rolling-memory", "bin", "rolling-memory"+ext))
	}
	candidates = append(candidates, filepath.Join(home, ".local", "share", "rolling-memory", "bin", "rolling-memory.sh"))
	for _, cand := range candidates {
		if cand == "" {
			continue
		}
		if info, err := os.Stat(cand); err == nil && !info.IsDir() && (runtime.GOOS == "windows" || info.Mode()&0o111 != 0) {
			return cand
		}
	}
	if p, err := exec.LookPath("rolling-memory"); err == nil {
		return p
	}
	return ""
}

func collectStatus(home, hostVer, hostAPIVer string) StatusResponse {
	widgetMu.RLock()
	wEnabled := widgetEnabled
	widgetMu.RUnlock()

	switches := parseConfigSwitches(filepath.Join(home, ".config", "rolling-memory", "config.toml"), wEnabled)
	dbPath := resolveMemoryDBPath(home)
	logPath := filepath.Join(home, ".local", "state", "rolling-memory", "rolling-memory.log")
	events, lastMS := readHookEvents(logPath, 20)
	if ledger := loadRecallLedgerStat(home, dbPath, UserScope, time.Now().AddDate(0, 0, -7).Unix()); ledger != nil {
		if len(ledger.recentEvents) > 0 {
			events = ledger.recentEvents
		}
		if len(ledger.latencies) > 0 {
			lastMS = ledger.latencies[len(ledger.latencies)-1]
		}
	}

	resp := StatusResponse{
		Name:             Name,
		Version:          liveSoftwareVersion(),
		CoreVersion:      CoreVersion,
		CoreCodename:     CoreCodename,
		Kind:             Kind,
		HostVersion:      hostVer,
		HostAPIVersion:   hostAPIVer,
		Scope:            UserScope,
		DBPath:           dbPath,
		LastRecallMS:     lastMS,
		Switches:         switches,
		HookEvents:       events,
		RecentAssertions: []RecentAssertion{},
		UpdatedAt:        time.Now().Format("2006-01-02 15:04:05"),
	}

	bin := resolveBinary(home)
	resp.BinaryPath = bin
	resp.Artifacts = collectArtifactStats(resp.DBPath, resp.Scope)
	resp.BgTask = snapshotBgTask()
	if bin == "" {
		return resp
	}
	resp.EngineOnline = true
	resp.ActiveAssertions = resp.Artifacts.ThinVaultDocs + resp.Artifacts.ThinVaultNoetic
	resp.PendingTraces = 0
	resp.TotalTraces = resp.Artifacts.ThinVaultDocs
	resp.TotalNodes = resp.Artifacts.ThinVaultCells + resp.Artifacts.ThinVaultDocs + resp.Artifacts.ThinVaultNoetic
	resp.ConflictsProposed = 0

	if tvRows, err := sqliteQueryJSON(resp.DBPath, `
		SELECT
			doc_id AS id,
			COALESCE(layer, 'episodic') AS layer,
			raw_text AS txt,
			address_uri AS addr
		FROM thin_vault_docs
		ORDER BY occurred_at DESC
		LIMIT 8;
	`); err == nil && len(tvRows) > 0 {
		if home != "" {
			batchRows := make([]map[string]any, len(tvRows))
			for i, r := range tvRows {
				batchRows[i] = map[string]any{"content": r["txt"]}
			}
			warmHydrateZCodePointersBatch(home, batchRows)
		}
		for _, r := range tvRows {
			resp.RecentAssertions = append(resp.RecentAssertions, RecentAssertion{
				ID:         asString(r["id"]),
				Rendered:   strings.TrimSpace(hydrateVaultTextForDisplay(asString(r["txt"]))),
				Confidence: 1.0,
				Anchor:     asString(r["addr"]),
				Status:     "active",
			})
		}
	}
	return resp
}

func parseConfigSwitches(configPath string, wEnabled bool) Switches {
	sw := Switches{
		BootContext: true,
		Ledger:      true,
		// 运行模式缺省与引擎一致：`[mode] name` 未显式配置时引擎取 "local"（internal/config/config.go）。
		RawWhenSparse: true,
		Mode:          modeLocal,
		CloudAutoSync: true,
		WidgetEnabled: wEnabled,
	}
	cfg := config.ParseRollingConfig(configPath)
	if len(cfg) == 0 {
		return sw
	}
	if low := strings.ToLower(strings.TrimSpace(cfg["mode.name"])); low == modeLocal || low == modeCloud {
		sw.Mode = low
	}
	if val, ok := cfg["hooks.boot_context"]; ok {
		sw.BootContext = strings.TrimSpace(val) == "true"
	}
	if val, ok := cfg["hooks.ledger"]; ok {
		sw.Ledger = strings.TrimSpace(val) == "true"
	}
	if val, ok := cfg["recall.raw_when_sparse"]; ok {
		sw.RawWhenSparse = strings.TrimSpace(val) == "true"
	}
	// 自动监控同步开关：三个键任一显式配置即生效（口径与引擎 internal/cli/autowatch.go 一致）。
	for _, key := range []string{"sync.auto_watch", "hooks.auto_sync", "cloud.sync.auto_watch"} {
		val, ok := cfg[key]
		if !ok {
			continue
		}
		low := strings.ToLower(strings.TrimSpace(val))
		sw.CloudAutoSync = low != "false" && low != "0" && low != "manual"
	}
	return sw
}

func applySwitchToggle(home, key, value string) error {
	key = strings.TrimSpace(key)
	value = strings.TrimSpace(value)
	if key == "widget_enabled" {
		if value != "true" && value != "false" {
			return errors.New("widget_enabled 仅支持 true 或 false")
		}
		widgetMu.Lock()
		widgetEnabled = value == "true"
		widgetMu.Unlock()
		return nil
	}
	bin := resolveBinary(home)
	if bin == "" {
		return errors.New("未找到 rolling-memory 引擎二进制")
	}
	setCfg := func(cfgKey, cfgVal string) error {
		if out, err := exec.Command(bin, "config", "set", cfgKey, cfgVal).CombinedOutput(); err != nil {
			return errors.New("更新配置失败: " + strings.TrimSpace(string(out)))
		}
		return nil
	}

	switch key {
	case "stop_bg_task":
		bgTaskMu.Lock()
		if bgTaskCancel != nil {
			bgTaskCancel()
			bgTaskCancel = nil
		}
		bgTaskState.Running = false
		bgTaskState.Status = "cancelled"
		bgTaskMu.Unlock()
		return nil
	case "episodic_sync_paused", "pause_sync":
		low := strings.ToLower(value)
		flag := "--resume"
		if low == "true" || low == "1" || low == "paused" || low == "pause" {
			flag = "--pause"
		}
		dbPath := resolveMemoryDBPath(home)
		cmd := exec.Command(bin, "sync", "watch", flag, "--db", dbPath, "--json")
		cmd.Env = stripCloudBrainEnv(os.Environ())
		if out, err := cmd.CombinedOutput(); err != nil {
			return errors.New("切换同步暂停状态失败: " + strings.TrimSpace(string(out)))
		}
		return nil
	case "delete_memory":
		if value == "" {
			return errors.New("记忆 ID 不能为空")
		}
		dbPath := resolveMemoryDBPath(home)
		// 写路径只用非破坏可写连接；绝不在此对用户金库执行 ALTER / DROP。
		db, err := openVaultWriteOnly(dbPath)
		if err != nil {
			return err
		}
		defer db.Close()
		var unitIDs []string
		for _, rawID := range strings.Split(value, ",") {
			targetID := strings.TrimPrefix(strings.TrimSpace(rawID), "unit:")
			if targetID == "" {
				continue
			}
			_, _ = db.Exec(`DELETE FROM thin_vault_docs WHERE doc_id = ?`, targetID)
			_, _ = db.Exec(`DELETE FROM thin_vault_docs_fts WHERE doc_id = ?`, targetID)
			_, _ = db.Exec(`DELETE FROM thin_vault_noetic WHERE noetic_id = ?`, targetID)
			unitIDs = append(unitIDs, targetID)
		}
		// 本地金库已删、云端未同步属不一致状态：如实上报（禁止静默吞错）。
		// 口径与「记忆管理」表格删除完全一致（同一 helper，同一身份映射，同一本地模式零外呼闸）。
		if msg := purgeCloudUnitsIfCloudMode(home, unitIDs); msg != "" {
			return errors.New(msg)
		}
		return nil
	}

	configKey := ""
	switch key {
	case "boot_context":
		if value != "true" && value != "false" {
			return errors.New("boot_context 仅支持 true 或 false")
		}
		configKey = "hooks.boot_context"
	case "ledger":
		if value != "true" && value != "false" {
			return errors.New("ledger 仅支持 true 或 false")
		}
		configKey = "hooks.ledger"
	case "raw_when_sparse":
		if value != "true" && value != "false" {
			return errors.New("raw_when_sparse 仅支持 true 或 false")
		}
		configKey = "recall.raw_when_sparse"
	case "cloud_auto_sync", "auto_sync", "sync_mode":
		low := strings.ToLower(value)
		switch low {
		case "true", "1", "auto":
			value = "true"
		case "false", "0", "manual":
			value = "false"
		default:
			return errors.New("cloud_auto_sync 仅支持 true/false 或 auto/manual")
		}
		configKey = "cloud.sync.auto_watch"
	default:
		return errors.New("未知开关项: " + key)
	}
	return setCfg(configKey, value)
}

// hookEventsCacheMax 单次解析缓存的最大事件条数：浮标以 1Hz 调 collectStatus，
// 命中缓存即可完全跳过日志重解析（日志由 hooks 侧按 2 MiB 轮转，本缓存按 (size, mtime) 失效）。
const hookEventsCacheMax = 200

type hookEventsCacheEntry struct {
	size    int64
	modNano int64
	events  []HookEventItem
	lastMS  int
}

var (
	hookEventsCacheMu sync.Mutex
	hookEventsCache   = map[string]hookEventsCacheEntry{}
)

// readHookEvents 读取 Hook 实时流水（新→旧，最多 limit 条）并返回其中最新一条的耗时读数。
// 整份日志按 (size, mtime) 缓存：日志未变时直接复用，1Hz 采样不再重复解析整份日志。
func readHookEvents(logPath string, limit int) ([]HookEventItem, int) {
	if limit <= 0 {
		limit = hookLogRecentMax
	}
	st, err := os.Stat(logPath)
	if err != nil {
		return []HookEventItem{}, 0
	}
	hookEventsCacheMu.Lock()
	if e, ok := hookEventsCache[logPath]; ok && e.size == st.Size() && e.modNano == st.ModTime().UnixNano() {
		hookEventsCacheMu.Unlock()
		return clipHookEvents(e.events, limit), e.lastMS
	}
	hookEventsCacheMu.Unlock()

	events, lastMS := parseHookEvents(logPath, hookEventsCacheMax)
	hookEventsCacheMu.Lock()
	hookEventsCache[logPath] = hookEventsCacheEntry{size: st.Size(), modNano: st.ModTime().UnixNano(), events: events, lastMS: lastMS}
	for k := range hookEventsCache {
		if k != logPath {
			delete(hookEventsCache, k)
		}
	}
	hookEventsCacheMu.Unlock()
	return clipHookEvents(events, limit), lastMS
}

// clipHookEvents 复制出前 limit 条（避免调用方持有缓存底层数组引用）。
func clipHookEvents(events []HookEventItem, limit int) []HookEventItem {
	if limit > len(events) {
		limit = len(events)
	}
	out := make([]HookEventItem, limit)
	copy(out, events[:limit])
	return out
}

// parseHookEvents 解析整份 Hook 日志，返回最新在前的最多 max 条事件与最新耗时读数。
// 日志只保留最近 max 条事件在内存里：日志本身由 hooks 侧轮转，但此处仍不随文件增长而线性膨胀。
func parseHookEvents(logPath string, max int) ([]HookEventItem, int) {
	file, err := os.Open(logPath)
	if err != nil {
		return []HookEventItem{}, 0
	}
	defer file.Close()

	var lines []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			lines = append(lines, line)
		}
	}
	events := make([]HookEventItem, 0, max)
	lastMS := 0
	for i := len(lines) - 1; i >= 0 && len(events) < max; i-- {
		var m map[string]any
		if json.Unmarshal([]byte(lines[i]), &m) != nil {
			continue
		}
		ts := int64(asInt(m["ts"]))
		ev := asString(m["event"])
		ms := asInt(m["ms"])
		if lastMS == 0 && ms > 0 {
			lastMS = ms
		}
		item := HookEventItem{
			Timestamp: ts,
			TimeText:  time.Unix(ts, 0).Format("15:04:05"),
			Event:     ev,
			Injected:  asInt(m["injected"]),
			Bands:     asInt(m["bands"]),
			Raw:       asInt(m["raw"]),
			ElapsedMS: ms,
			Chars:     asInt(m["chars"]),
			Dedup:     asInt(m["dedup"]),
			Absent:    asInt(m["absent"]),
			Nudge:     asInt(m["nudge"]),
			Imported:  asInt(m["imported"]),
			Replayed:  asInt(m["replayed"]),
			Written:   asInt(m["written"]),
		}
		item.Outcome = classifyHookOutcome(item)
		events = append(events, item)
	}
	return events, lastMS
}

func classifyHookOutcome(item HookEventItem) string {
	switch item.Event {
	case "recall":
		if item.Nudge > 0 && item.Injected > 0 {
			return "injected+nudge"
		}
		if item.Injected > 0 {
			return "injected"
		}
		if item.Dedup > 0 {
			return "dedup"
		}
		if item.Absent > 0 {
			return "absent"
		}
		return "silent"
	case "close", "start":
		if item.Written > 0 {
			return "written"
		}
		if item.Imported > 0 {
			return "replayed"
		}
		return "idle"
	default:
		return "ok"
	}
}

func asInt(v any) int {
	switch typed := v.(type) {
	case float64:
		return int(typed)
	case int:
		return typed
	case int64:
		return int(typed)
	}
	return 0
}

func asFloat(v any) float64 {
	switch typed := v.(type) {
	case float64:
		return typed
	case int64:
		return float64(typed)
	case int:
		return float64(typed)
	}
	return 0
}

func asString(v any) string {
	switch typed := v.(type) {
	case string:
		return typed
	case []byte:
		return string(typed)
	}
	return ""
}
