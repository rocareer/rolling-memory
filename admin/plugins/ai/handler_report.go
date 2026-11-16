// plugin: ai v0.2.0
// handler_report.go 融合上游内核同款「AI 资产体检（24 项机械判据）」与「AI 成本账单（原价/缓存节省/离峰节省恒等分解与五维归因）」。
//
// 端点：POST /<后台入口>/plugin/ai/report（权限节点 plugin/ai/report）
// 数据源：直接读取本插件自管的 plugin_ai_supplier / plugin_ai_channel / plugin_ai_model / plugin_ai_model_channel / plugin_ai_usage 表，
// 以及评测实验室的 locomo-answer / locomo-judge 双智能体绑定状态，输出与上游内核同名插件（plugins/ai）完全同构的 Report JSON。
package ai

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ai-go-hub/ai-go-admin/internal/kit/httpx"
	"github.com/gin-gonic/gin"
)

// 严重级闭集
const (
	ReportSeverityError   = "error"
	ReportSeverityWarning = "warning"
	ReportSeverityInfo    = "info"
)

// 24 项机械体检发现码闭集（与前端 plugin.ai.finding.<code> 一一对应）
const (
	CodeModelPriceMissing              = "model_price_missing"
	CodeModelPricePartial              = "model_price_partial"
	CodeModelPeakWindowsInvalid        = "model_peak_windows_invalid"
	CodeModelOffpeakDead               = "model_offpeak_dead"
	CodeChannelKeyMissing              = "channel_key_missing"
	CodeChannelBaseURLMissing          = "channel_base_url_missing"
	CodeChannelVendorDisabled          = "channel_vendor_disabled"
	CodeVendorDangling                 = "vendor_dangling"
	CodeDriverUnsupported              = "driver_unsupported"
	CodeRerankDriverIncompatible       = "rerank_driver_incompatible"
	CodeModelUnbound                   = "model_unbound"
	CodeModelBindingInactive           = "model_binding_inactive"
	CodeChannelUnbound                 = "channel_unbound"
	CodeBindingDangling                = "binding_dangling"
	CodeChannelBreakerPermanent        = "channel_breaker_permanent"
	CodeChannelBreakerOpen             = "channel_breaker_open"
	CodeChannelBreakerHalfOpen         = "channel_breaker_half_open"
	CodeTPMBudgetFailOpen              = "tpm_budget_fail_open"
	CodeAgentModelUnroutable           = "agent_model_unroutable"
	CodeAgentModelTypeMismatch         = "agent_model_type_mismatch"
	CodePolicyModelFreezeViolation     = "policy_model_freeze_violation"
	CodePolicyOfficialChannelViolation = "policy_official_channel_violation"
	CodeCostUnpricedCalls              = "cost_unpriced_calls"
	CodeScanTruncated                  = "scan_truncated"
)

type ReportFinding struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Ref      string `json:"ref"`
	Detail   string `json:"detail"`
}

type ReportLimits struct {
	Vendors  int `json:"vendors"`
	Models   int `json:"models"`
	Channels int `json:"channels"`
	Bindings int `json:"bindings"`
	Agents   int `json:"agents"`
}

type ReportSummary struct {
	Vendors           int `json:"vendors"`
	Models            int `json:"models"`
	Channels          int `json:"channels"`
	Bindings          int `json:"bindings"`
	Agents            int `json:"agents"`
	PricedModels      int `json:"priced_models"`
	ModelsWithBind    int `json:"models_with_binding"`
	ModelsRoutable    int `json:"models_routable"`
	ChannelsWithKey   int `json:"channels_with_key"`
	RoutableBindings  int `json:"routable_bindings"`
	RoutableAgents    int `json:"routable_agents"`
	OpenBreakers      int `json:"open_breakers"`
	PermanentBreakers int `json:"permanent_breakers"`
	Errors            int `json:"errors"`
	Warnings          int `json:"warnings"`
	Infos             int `json:"infos"`
}

type ReportVendorRow struct {
	ID                 uint   `json:"id"`
	Name               string `json:"name"`
	Title              string `json:"title"`
	Driver             string `json:"driver"`
	DriverSupported    bool   `json:"driver_supported"`
	BaseURL            string `json:"base_url"`
	Status             string `json:"status"`
	ModelCount         int    `json:"model_count"`
	ChannelCount       int    `json:"channel_count"`
	ActiveChannelCount int    `json:"active_channel_count"`
}

type ReportRouteTargetView struct {
	BindingID    uint   `json:"binding_id"`
	ChannelID    uint   `json:"channel_id"`
	ChannelName  string `json:"channel_name"`
	UpstreamName string `json:"upstream_name"`
	Driver       string `json:"driver"`
	Priority     int64  `json:"priority"`
	Weigh        int64  `json:"weigh"`
	TPMLimit     int64  `json:"tpm_limit"`
	BreakerState string `json:"breaker_state"`
	Routable     bool   `json:"routable"`
}

type ReportRouteTierView struct {
	Priority   int64                   `json:"priority"`
	TotalWeigh int64                   `json:"total_weigh"`
	Targets    []ReportRouteTargetView `json:"targets"`
}

type ReportModelRow struct {
	ID                 uint                  `json:"id"`
	Name               string                `json:"name"`
	VendorID           uint                  `json:"vendor_id"`
	VendorTitle        string                `json:"vendor_title"`
	Type               string                `json:"type"`
	Status             string                `json:"status"`
	InputPrice         float64               `json:"input_price"`
	OutputPrice        float64               `json:"output_price"`
	CacheInputPrice    float64               `json:"cache_input_price"`
	PriceCurrency      string                `json:"price_currency"`
	OffpeakRatio       float64               `json:"offpeak_ratio"`
	PeakWindows        string                `json:"peak_windows"`
	PriceOK            bool                  `json:"price_ok"`
	PeakWindowsOK      bool                  `json:"peak_windows_ok"`
	WhitelistOK        bool                  `json:"whitelist_ok"`
	BindingCount       int                   `json:"binding_count"`
	ActiveBindingCount int                   `json:"active_binding_count"`
	ActiveAgentCount   int                   `json:"active_agent_count"`
	RouteTiers         []ReportRouteTierView `json:"route_tiers"`
}

type ReportChannelRow struct {
	ID                 uint    `json:"id"`
	VendorID           uint    `json:"vendor_id"`
	VendorTitle        string  `json:"vendor_title"`
	Name               string  `json:"name"`
	Driver             string  `json:"driver"`
	EffectiveDriver    string  `json:"effective_driver"`
	DriverSupported    bool    `json:"driver_supported"`
	SupportsRerank     bool    `json:"supports_rerank"`
	EffectiveBaseURL   string  `json:"effective_base_url"`
	Priority           int64   `json:"priority"`
	Weigh              int64   `json:"weigh"`
	TimeoutMs          int64   `json:"timeout_ms"`
	TPMLimit           int64   `json:"tpm_limit"`
	BudgetTokens       float64 `json:"budget_tokens"`
	BudgetUpdatedMs    int64   `json:"budget_updated_ms"`
	Status             string  `json:"status"`
	APIKeySet          bool    `json:"api_key_set"`
	BindingCount       int     `json:"binding_count"`
	ActiveBindingCount int     `json:"active_binding_count"`
	BreakerState       string  `json:"breaker_state"`
	BreakerPermanent   bool    `json:"breaker_permanent"`
	BreakerFailures    int     `json:"breaker_failures"`
	TotalCalls         int64   `json:"total_calls"`
	TotalFailures      int64   `json:"total_failures"`
	LastError          string  `json:"last_error"`
}

type ReportBindingRow struct {
	ID                uint   `json:"id"`
	ChannelID         uint   `json:"channel_id"`
	ChannelName       string `json:"channel_name"`
	ModelID           uint   `json:"model_id"`
	ModelName         string `json:"model_name"`
	ModelType         string `json:"model_type"`
	UpstreamName      string `json:"upstream_name"`
	EffectiveUpstream string `json:"effective_upstream"`
	EffectiveDriver   string `json:"effective_driver"`
	EffectivePriority int64  `json:"effective_priority"`
	EffectiveWeigh    int64  `json:"effective_weigh"`
	Status            string `json:"status"`
	Routable          bool   `json:"routable"`
	BlockReason       string `json:"block_reason"`
}

type ReportAgentRow struct {
	ID          uint   `json:"id"`
	Name        string `json:"name"`
	Title       string `json:"title"`
	Type        string `json:"type"`
	Model       string `json:"model"`
	ModelFound  bool   `json:"model_found"`
	ModelType   string `json:"model_type"`
	TypeMatch   bool   `json:"type_match"`
	Routable    bool   `json:"routable"`
	WhitelistOK bool   `json:"whitelist_ok"`
	OfficialOK  bool   `json:"official_ok"`
	Version     int64  `json:"version"`
	Status      string `json:"status"`
}

type ReportGateInfo struct {
	Enabled           bool    `json:"enabled"`
	Max               int     `json:"max"`
	Limit             int     `json:"limit"`
	Inflight          int     `json:"inflight"`
	QueuedInteractive int     `json:"queued_interactive"`
	QueuedEval        int     `json:"queued_eval"`
	QueuedBatch       int     `json:"queued_batch"`
	Waits             int64   `json:"waits"`
	RateLimitHits     int64   `json:"rate_limit_hits"`
	Downshifts        int64   `json:"downshifts"`
	LastRateLimitedAt *string `json:"last_rate_limited_at"`
}

type ReportBudgetStats struct {
	Waits          int64   `json:"waits"`
	FailOpen       int64   `json:"fail_open"`
	WaitP95Ms      int64   `json:"wait_p95_ms"`
	WaitSamples    int     `json:"wait_samples"`
	LastWaitAt     *string `json:"last_wait_at"`
	LastFailOpenAt *string `json:"last_fail_open_at"`
}

type ReportRuntimeView struct {
	Gate                ReportGateInfo    `json:"gate"`
	Budget              ReportBudgetStats `json:"budget"`
	ModelFreezeOK       bool              `json:"model_freeze_ok"`
	OfficialIsolationOK bool              `json:"official_isolation_ok"`
	OfficialCloneOK     bool              `json:"official_clone_ok"`
	AgentWebConfig      string            `json:"agent_web_config"`
}

type ReportCostSummary struct {
	WindowHours     int     `json:"window_hours"`
	Currency        string  `json:"currency"`
	TotalCalls      int64   `json:"total_calls"`
	SuccessCalls    int64   `json:"success_calls"`
	FailedCalls     int64   `json:"failed_calls"`
	CanceledCalls   int64   `json:"canceled_calls"`
	OffpeakCalls    int64   `json:"offpeak_calls"`
	OffpeakRate     float64 `json:"offpeak_rate"`
	AvgLatencyMs    int64   `json:"avg_latency_ms"`
	InputTokens     int64   `json:"input_tokens"`
	CacheHitTokens  int64   `json:"cache_hit_tokens"`
	CacheMissTokens int64   `json:"cache_miss_tokens"`
	CacheHitRate    float64 `json:"cache_hit_rate"`
	OutputTokens    int64   `json:"output_tokens"`
	TotalTokens     int64   `json:"total_tokens"`
	TotalCost       float64 `json:"total_cost"`
	ListCost        float64 `json:"list_cost"`
	CacheSaved      float64 `json:"cache_saved"`
	OffpeakSaved    float64 `json:"offpeak_saved"`
	TotalSaved      float64 `json:"total_saved"`
	SavedRate       float64 `json:"saved_rate"`
}

type ReportVendorCostRow struct {
	VendorID       uint    `json:"vendor_id"`
	VendorName     string  `json:"vendor_name"`
	VendorTitle    string  `json:"vendor_title"`
	Driver         string  `json:"driver"`
	ChannelCount   int     `json:"channel_count"`
	ModelCount     int     `json:"model_count"`
	Currency       string  `json:"currency"`
	Calls          int64   `json:"calls"`
	SuccessCalls   int64   `json:"success_calls"`
	FailedCalls    int64   `json:"failed_calls"`
	SuccessRate    float64 `json:"success_rate"`
	OffpeakCalls   int64   `json:"offpeak_calls"`
	OffpeakRate    float64 `json:"offpeak_rate"`
	AvgLatencyMs   int64   `json:"avg_latency_ms"`
	InputTokens    int64   `json:"input_tokens"`
	CacheHitTokens int64   `json:"cache_hit_tokens"`
	CacheHitRate   float64 `json:"cache_hit_rate"`
	OutputTokens   int64   `json:"output_tokens"`
	TotalTokens    int64   `json:"total_tokens"`
	Cost           float64 `json:"cost"`
	ListCost       float64 `json:"list_cost"`
	CacheSaved     float64 `json:"cache_saved"`
	OffpeakSaved   float64 `json:"offpeak_saved"`
	TotalSaved     float64 `json:"total_saved"`
	SavedRate      float64 `json:"saved_rate"`
	CostShare      float64 `json:"cost_share"`
}

type ReportModelCostRow struct {
	Model          string  `json:"model"`
	ModelType      string  `json:"model_type"`
	VendorID       uint    `json:"vendor_id"`
	VendorTitle    string  `json:"vendor_title"`
	Currency       string  `json:"currency"`
	Calls          int64   `json:"calls"`
	SuccessCalls   int64   `json:"success_calls"`
	FailedCalls    int64   `json:"failed_calls"`
	SuccessRate    float64 `json:"success_rate"`
	OffpeakCalls   int64   `json:"offpeak_calls"`
	OffpeakRate    float64 `json:"offpeak_rate"`
	AvgLatencyMs   int64   `json:"avg_latency_ms"`
	InputTokens    int64   `json:"input_tokens"`
	CacheHitTokens int64   `json:"cache_hit_tokens"`
	CacheHitRate   float64 `json:"cache_hit_rate"`
	OutputTokens   int64   `json:"output_tokens"`
	TotalTokens    int64   `json:"total_tokens"`
	Cost           float64 `json:"cost"`
	ListCost       float64 `json:"list_cost"`
	CacheSaved     float64 `json:"cache_saved"`
	OffpeakSaved   float64 `json:"offpeak_saved"`
	TotalSaved     float64 `json:"total_saved"`
	SavedRate      float64 `json:"saved_rate"`
	CostShare      float64 `json:"cost_share"`
}

type ReportChannelCostRow struct {
	ChannelID      uint    `json:"channel_id"`
	ChannelName    string  `json:"channel_name"`
	VendorID       uint    `json:"vendor_id"`
	VendorTitle    string  `json:"vendor_title"`
	Currency       string  `json:"currency"`
	Calls          int64   `json:"calls"`
	SuccessCalls   int64   `json:"success_calls"`
	FailedCalls    int64   `json:"failed_calls"`
	SuccessRate    float64 `json:"success_rate"`
	OffpeakCalls   int64   `json:"offpeak_calls"`
	OffpeakRate    float64 `json:"offpeak_rate"`
	AvgLatencyMs   int64   `json:"avg_latency_ms"`
	InputTokens    int64   `json:"input_tokens"`
	CacheHitTokens int64   `json:"cache_hit_tokens"`
	CacheHitRate   float64 `json:"cache_hit_rate"`
	OutputTokens   int64   `json:"output_tokens"`
	TotalTokens    int64   `json:"total_tokens"`
	Cost           float64 `json:"cost"`
	ListCost       float64 `json:"list_cost"`
	CacheSaved     float64 `json:"cache_saved"`
	OffpeakSaved   float64 `json:"offpeak_saved"`
	TotalSaved     float64 `json:"total_saved"`
	SavedRate      float64 `json:"saved_rate"`
	CostShare      float64 `json:"cost_share"`
}

type ReportAgentCostItem struct {
	AgentID        uint    `json:"agent_id"`
	AgentName      string  `json:"agent_name"`
	AgentTitle     string  `json:"agent_title"`
	Model          string  `json:"model"`
	VendorTitle    string  `json:"vendor_title"`
	Currency       string  `json:"currency"`
	Calls          int64   `json:"calls"`
	SuccessCalls   int64   `json:"success_calls"`
	FailedCalls    int64   `json:"failed_calls"`
	SuccessRate    float64 `json:"success_rate"`
	OffpeakCalls   int64   `json:"offpeak_calls"`
	OffpeakRate    float64 `json:"offpeak_rate"`
	AvgLatencyMs   int64   `json:"avg_latency_ms"`
	InputTokens    int64   `json:"input_tokens"`
	CacheHitTokens int64   `json:"cache_hit_tokens"`
	CacheHitRate   float64 `json:"cache_hit_rate"`
	OutputTokens   int64   `json:"output_tokens"`
	TotalTokens    int64   `json:"total_tokens"`
	Cost           float64 `json:"cost"`
	ListCost       float64 `json:"list_cost"`
	CacheSaved     float64 `json:"cache_saved"`
	OffpeakSaved   float64 `json:"offpeak_saved"`
	TotalSaved     float64 `json:"total_saved"`
	SavedRate      float64 `json:"saved_rate"`
	CostPer1kCalls float64 `json:"cost_per_1k_calls"`
	CostShare      float64 `json:"cost_share"`
}

type ReportSourceCostRow struct {
	Source         string  `json:"source"`
	Currency       string  `json:"currency"`
	Calls          int64   `json:"calls"`
	SuccessCalls   int64   `json:"success_calls"`
	FailedCalls    int64   `json:"failed_calls"`
	SuccessRate    float64 `json:"success_rate"`
	OffpeakCalls   int64   `json:"offpeak_calls"`
	OffpeakRate    float64 `json:"offpeak_rate"`
	AvgLatencyMs   int64   `json:"avg_latency_ms"`
	InputTokens    int64   `json:"input_tokens"`
	CacheHitTokens int64   `json:"cache_hit_tokens"`
	CacheHitRate   float64 `json:"cache_hit_rate"`
	OutputTokens   int64   `json:"output_tokens"`
	TotalTokens    int64   `json:"total_tokens"`
	Cost           float64 `json:"cost"`
	ListCost       float64 `json:"list_cost"`
	CacheSaved     float64 `json:"cache_saved"`
	OffpeakSaved   float64 `json:"offpeak_saved"`
	TotalSaved     float64 `json:"total_saved"`
	SavedRate      float64 `json:"saved_rate"`
	CostShare      float64 `json:"cost_share"`
}

type ReportCostCrossBucket struct {
	VendorID       uint    `json:"vendor_id"`
	VendorTitle    string  `json:"vendor_title"`
	ChannelID      uint    `json:"channel_id"`
	ChannelName    string  `json:"channel_name"`
	Model          string  `json:"model"`
	ModelType      string  `json:"model_type"`
	AgentID        uint    `json:"agent_id"`
	AgentName      string  `json:"agent_name"`
	AgentTitle     string  `json:"agent_title"`
	Source         string  `json:"source"`
	Currency       string  `json:"currency"`
	Calls          int64   `json:"calls"`
	SuccessCalls   int64   `json:"success_calls"`
	FailedCalls    int64   `json:"failed_calls"`
	CanceledCalls  int64   `json:"canceled_calls"`
	OffpeakCalls   int64   `json:"offpeak_calls"`
	LatencySumMs   int64   `json:"latency_sum_ms"`
	InputTokens    int64   `json:"input_tokens"`
	CacheHitTokens int64   `json:"cache_hit_tokens"`
	OutputTokens   int64   `json:"output_tokens"`
	TotalTokens    int64   `json:"total_tokens"`
	Cost           float64 `json:"cost"`
	ListCost       float64 `json:"list_cost"`
	CacheSaved     float64 `json:"cache_saved"`
	OffpeakSaved   float64 `json:"offpeak_saved"`
	TotalSaved     float64 `json:"total_saved"`
}

type ReportCostView struct {
	Summary   ReportCostSummary       `json:"summary"`
	ByVendor  []ReportVendorCostRow   `json:"by_vendor"`
	ByModel   []ReportModelCostRow    `json:"by_model"`
	ByChannel []ReportChannelCostRow  `json:"by_channel"`
	ByAgent   []ReportAgentCostItem   `json:"by_agent"`
	BySource  []ReportSourceCostRow   `json:"by_source"`
	Buckets   []ReportCostCrossBucket `json:"buckets"`
}

type AssetReportResponse struct {
	GeneratedAt int64              `json:"generated_at"`
	Limits      ReportLimits       `json:"limits"`
	Summary     ReportSummary      `json:"summary"`
	Runtime     ReportRuntimeView  `json:"runtime"`
	Cost        ReportCostView     `json:"cost"`
	Vendors     []ReportVendorRow  `json:"vendors"`
	Models      []ReportModelRow   `json:"models"`
	Channels    []ReportChannelRow `json:"channels"`
	Bindings    []ReportBindingRow `json:"bindings"`
	Agents      []ReportAgentRow   `json:"agents"`
	Findings    []ReportFinding    `json:"findings"`
	Truncated   []string           `json:"truncated"`
}

func emitReportFinding(dst *[]ReportFinding, code, severity, ref, format string, args ...any) {
	*dst = append(*dst, ReportFinding{
		Code:     code,
		Severity: severity,
		Ref:      ref,
		Detail:   fmt.Sprintf(format, args...),
	})
}

func reportRound4(v float64) float64 {
	return math.Round(v*10000) / 10000
}

func reportRound2(v float64) float64 {
	return math.Round(v*100) / 100
}

func statusToEnableStr(st int16) string {
	if st == StatusEnabled {
		return "enable"
	}
	return "disable"
}

func parsePriceFloat(s string) float64 {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return 0
	}
	return v
}

func readEvalConfiguredModels() (answerModel, judgeModel string) {
	answerModel = "deepseek-chat"
	judgeModel = "deepseek-chat"
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return
	}
	cfgPath := filepath.Join(home, ".config", "rolling-memory", "config.toml")
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		return
	}
	inEval := false
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			inEval = (trimmed == "[eval]")
			continue
		}
		if !inEval {
			continue
		}
		parts := strings.SplitN(trimmed, "=", 2)
		if len(parts) != 2 {
			continue
		}
		k := strings.TrimSpace(parts[0])
		v := strings.Trim(strings.TrimSpace(parts[1]), `"'`)
		switch k {
		case "answer_model":
			if v != "" {
				answerModel = v
			}
		case "judge_model":
			if v != "" {
				judgeModel = v
			}
		}
	}
	return
}

func reportDriverSupported(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" || name == "openai" {
		return true
	}
	for _, d := range GetSupported() {
		if d == name {
			return true
		}
	}
	return false
}

func md5Hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

// reportIndex POST /admin/plugin/ai/report —— 返回 AI 资产体检（24 项判据）与 AI 成本经济学报告。
func (d deps) reportIndex(c *gin.Context) {
	store := d.storeOrFail(c)
	if store == nil {
		return
	}
	var req struct {
		CostHours int `json:"cost_hours"`
	}
	if err := bindJSON(c, &req); err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	costHours := req.CostHours
	if costHours <= 0 {
		costHours = 24
	}
	if costHours > 720 {
		costHours = 720
	}

	set, _ := d.loadSettings(c.Request.Context())
	resp, err := buildAssetReport(c.Request.Context(), store, set, costHours)
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("体检失败：读取 AI 资产配置失败，请稍后重试"))
		return
	}
	httpx.Success(c, httpx.WithData(resp))
}

func buildAssetReport(ctx context.Context, store *Store, set pluginSettings, costHours int) (AssetReportResponse, error) {
	if err := store.ready(); err != nil {
		return AssetReportResponse{}, err
	}
	db := store.DB().WithContext(ctx)
	now := time.Now()

	var suppliers []Supplier
	if err := db.Order(`"id" ASC`).Limit(1001).Find(&suppliers).Error; err != nil {
		return AssetReportResponse{}, err
	}
	var models []Model
	if err := db.Order(`"id" ASC`).Limit(1001).Find(&models).Error; err != nil {
		return AssetReportResponse{}, err
	}
	var channels []Channel
	if err := db.Order(`"id" ASC`).Limit(1001).Find(&channels).Error; err != nil {
		return AssetReportResponse{}, err
	}
	var bindings []ModelChannel
	if err := db.Order(`"id" ASC`).Limit(2001).Find(&bindings).Error; err != nil {
		return AssetReportResponse{}, err
	}

	truncated := []string{}
	if len(suppliers) > 1000 {
		suppliers = suppliers[:1000]
		truncated = append(truncated, "vendors")
	}
	if len(models) > 1000 {
		models = models[:1000]
		truncated = append(truncated, "models")
	}
	if len(channels) > 1000 {
		channels = channels[:1000]
		truncated = append(truncated, "channels")
	}
	if len(bindings) > 2000 {
		bindings = bindings[:2000]
		truncated = append(truncated, "bindings")
	}

	supplierByID := make(map[int64]Supplier, len(suppliers))
	vendorIDByCode := make(map[string]uint, len(suppliers))
	for _, s := range suppliers {
		supplierByID[s.ID] = s
		if s.Vendor != "" && vendorIDByCode[s.Vendor] == 0 {
			vendorIDByCode[s.Vendor] = uint(s.ID)
		}
	}

	modelByID := make(map[uint64]Model, len(models))
	modelByName := make(map[string]Model, len(models))
	for _, m := range models {
		modelByID[m.ID] = m
		modelByName[m.Name] = m
	}

	channelByID := make(map[int64]Channel, len(channels))
	channelsByVendor := make(map[uint]int, len(suppliers))
	activeChannelsByVendor := make(map[uint]int, len(suppliers))
	for _, ch := range channels {
		channelByID[ch.ID] = ch
		vid := uint(ch.SupplierID)
		if vid == 0 {
			vid = vendorIDByCode[ch.Vendor]
		}
		channelsByVendor[vid]++
		effKey := strings.TrimSpace(ch.APIKey)
		if effKey == "" && ch.SupplierID > 0 {
			effKey = strings.TrimSpace(supplierByID[ch.SupplierID].APIKey)
		}
		if ch.Status == StatusEnabled && effKey != "" {
			activeChannelsByVendor[vid]++
		}
	}

	// 内置智能体消费面：locomo-answer、locomo-judge、memory-extractor
	ansModelName, jdgModelName := readEvalConfiguredModels()
	type synthAgent struct {
		ID    uint
		Name  string
		Title string
		Type  string
		Model string
	}
	synthAgents := []synthAgent{
		{ID: 1, Name: "locomo-answer", Title: "LoCoMo 官方答题智能体 (locomo-answer)", Type: "chat", Model: ansModelName},
		{ID: 2, Name: "locomo-judge", Title: "LoCoMo 官方判官智能体 (locomo-judge)", Type: "chat", Model: jdgModelName},
		{ID: 3, Name: "memory-extractor", Title: "structured memory assertion 结构化记忆抽取智能体 (extractor)", Type: "chat", Model: ansModelName},
	}

	activeAgentsByModel := make(map[string]int, len(synthAgents))
	for _, ag := range synthAgents {
		activeAgentsByModel[ag.Model]++
	}

	// 评估绑定与模型/渠道可路由性
	allByModel := make(map[uint64]int, len(models))
	activeByModel := make(map[uint64]int, len(models))
	allByChannel := make(map[int64]int, len(channels))
	activeByChannel := make(map[int64]int, len(channels))
	modelVendorID := make(map[uint64]uint, len(models))
	modelsByVendor := make(map[uint]int, len(suppliers))

	targetsByModel := make(map[uint64][]ReportRouteTargetView, len(models))
	bindingRows := make([]ReportBindingRow, 0, len(bindings))
	findings := []ReportFinding{}
	routableBindingsCount := 0

	for _, b := range bindings {
		ch, chOK := channelByID[int64(b.ChannelID)]
		md, mdOK := modelByID[b.ModelID]
		if mdOK {
			allByModel[md.ID]++
		}
		if chOK {
			allByChannel[ch.ID]++
		}
		ref := fmt.Sprintf("binding:%d", b.ID)
		if !chOK || !mdOK {
			var missing []string
			if !chOK {
				missing = append(missing, "channel")
			}
			if !mdOK {
				missing = append(missing, "model")
			}
			emitReportFinding(&findings, CodeBindingDangling, ReportSeverityError, ref, "missing=%s channel_id=%d model_id=%d", strings.Join(missing, ","), b.ChannelID, b.ModelID)
			chName := ch.Name
			if !chOK {
				chName = fmt.Sprintf("#%d(missing)", b.ChannelID)
			}
			mdName := md.Name
			if !mdOK {
				mdName = fmt.Sprintf("#%d(missing)", b.ModelID)
			}
			bindingRows = append(bindingRows, ReportBindingRow{
				ID: uint(b.ID), ChannelID: uint(b.ChannelID), ChannelName: chName,
				ModelID: uint(b.ModelID), ModelName: mdName, ModelType: md.Type,
				Status: statusToEnableStr(b.Status), Routable: false, BlockReason: "dangling_reference",
			})
			continue
		}

		sup, supOK := supplierByID[ch.SupplierID]
		vid := uint(ch.SupplierID)
		if vid == 0 {
			vid = vendorIDByCode[ch.Vendor]
		}
		if modelVendorID[md.ID] == 0 && vid > 0 {
			modelVendorID[md.ID] = vid
		}

		effDriver := strings.TrimSpace(ch.Driver)
		if effDriver == "" && supOK {
			effDriver = strings.TrimSpace(sup.Driver)
		}
		if effDriver == "" {
			effDriver = "openai"
		}
		effBaseURL := strings.TrimSpace(ch.BaseURL)
		if effBaseURL == "" && supOK {
			effBaseURL = strings.TrimSpace(sup.BaseURL)
		}
		effKey := strings.TrimSpace(ch.APIKey)
		if effKey == "" && supOK {
			effKey = strings.TrimSpace(sup.APIKey)
		}
		effUpstream := strings.TrimSpace(ch.Model)
		if effUpstream == "" {
			effUpstream = md.Name
		}

		routable := true
		reason := ""
		if b.Status != StatusEnabled {
			routable, reason = false, "binding_disabled"
		} else if md.Status != StatusEnabled {
			routable, reason = false, "model_disabled"
		} else if ch.Status != StatusEnabled {
			routable, reason = false, "channel_disabled"
		} else if ch.SupplierID > 0 && supOK && sup.Status != StatusEnabled {
			routable, reason = false, "vendor_disabled"
		} else if effBaseURL == "" {
			routable, reason = false, "base_url_missing"
		} else if effKey == "" && !strings.Contains(effBaseURL, "127.0.0.1") && !strings.Contains(effBaseURL, "localhost") {
			routable, reason = false, "api_key_missing"
		} else if ch.Circuit == CircuitOpen {
			routable, reason = false, "breaker_open"
		}

		if routable {
			routableBindingsCount++
			activeByModel[md.ID]++
			activeByChannel[ch.ID]++
		}
		effWeigh := int64(b.Weight)
		if effWeigh <= 0 {
			effWeigh = 1
		}
		bindingRows = append(bindingRows, ReportBindingRow{
			ID: uint(b.ID), ChannelID: uint(b.ChannelID), ChannelName: ch.Name,
			ModelID: uint(b.ModelID), ModelName: md.Name, ModelType: md.Type,
			UpstreamName: ch.Model, EffectiveUpstream: effUpstream, EffectiveDriver: effDriver,
			EffectivePriority: int64(b.Priority), EffectiveWeigh: effWeigh,
			Status: statusToEnableStr(b.Status), Routable: routable, BlockReason: reason,
		})
		if b.Status == StatusEnabled && ch.Status == StatusEnabled && md.Status == StatusEnabled {
			brkState := ch.Circuit
			if brkState == "" {
				brkState = CircuitClosed
			}
			targetsByModel[md.ID] = append(targetsByModel[md.ID], ReportRouteTargetView{
				BindingID: uint(b.ID), ChannelID: uint(ch.ID), ChannelName: ch.Name,
				UpstreamName: effUpstream, Driver: effDriver,
				Priority: int64(b.Priority), Weigh: effWeigh, TPMLimit: int64(ch.TPMLimit),
				BreakerState: brkState, Routable: routable,
			})
		}
	}

	for _, m := range models {
		vid := modelVendorID[m.ID]
		modelsByVendor[vid]++
	}

	vendorRows := make([]ReportVendorRow, 0, len(suppliers))
	vendorTitleByID := make(map[uint]string, len(suppliers))
	for _, s := range suppliers {
		uid := uint(s.ID)
		title := s.Name
		if title == "" {
			title = s.Vendor
		}
		vendorTitleByID[uid] = title
		drv := strings.TrimSpace(s.Driver)
		if drv == "" {
			drv = "openai"
		}
		drvOK := reportDriverSupported(drv)
		if !drvOK {
			emitReportFinding(&findings, CodeDriverUnsupported, ReportSeverityError, fmt.Sprintf("vendor:%d", s.ID), "vendor=%s driver=%s", s.Name, drv)
		}
		vendorRows = append(vendorRows, ReportVendorRow{
			ID: uid, Name: s.Vendor, Title: title, Driver: drv,
			DriverSupported: drvOK, BaseURL: s.BaseURL, Status: statusToEnableStr(s.Status),
			ModelCount: modelsByVendor[uid], ChannelCount: channelsByVendor[uid], ActiveChannelCount: activeChannelsByVendor[uid],
		})
	}

	modelRows := make([]ReportModelRow, 0, len(models))
	pricedModelsCount := 0
	modelsWithBindCount := 0
	modelsRoutableCount := 0
	currency := strings.TrimSpace(set.Currency)
	if currency == "" {
		currency = "CNY"
	}

	for _, m := range models {
		inPrice := parsePriceFloat(m.PriceInput)
		outPrice := parsePriceFloat(m.PriceOutput)
		priceMissing := false
		pricePartial := false
		if m.Type == ModelTypeEmbedding {
			priceMissing = inPrice <= 0
		} else {
			priceMissing = inPrice <= 0 && outPrice <= 0
			pricePartial = !priceMissing && (inPrice <= 0 || outPrice <= 0)
		}
		ref := fmt.Sprintf("model:%d", m.ID)
		if priceMissing {
			emitReportFinding(&findings, CodeModelPriceMissing, ReportSeverityWarning, ref, "model=%s type=%s input_price=%.4f output_price=%.4f", m.Name, m.Type, inPrice, outPrice)
		} else {
			pricedModelsCount++
			if pricePartial {
				emitReportFinding(&findings, CodeModelPricePartial, ReportSeverityWarning, ref, "model=%s input_price=%.4f output_price=%.4f", m.Name, inPrice, outPrice)
			}
		}
		bCount := allByModel[m.ID]
		aCount := activeByModel[m.ID]
		if bCount > 0 {
			modelsWithBindCount++
		}
		if aCount > 0 {
			modelsRoutableCount++
		}
		if m.Status == StatusEnabled {
			if bCount == 0 {
				emitReportFinding(&findings, CodeModelUnbound, ReportSeverityWarning, ref, "model=%s bindings=0", m.Name)
			} else if aCount == 0 {
				emitReportFinding(&findings, CodeModelBindingInactive, ReportSeverityWarning, ref, "model=%s bindings=%d active_routable=0", m.Name, bCount)
			}
		}
		vid := modelVendorID[m.ID]
		vTitle := vendorTitleByID[vid]
		if vTitle == "" {
			vTitle = "通用路由"
		}
		modelRows = append(modelRows, ReportModelRow{
			ID: uint(m.ID), Name: m.Name, VendorID: vid, VendorTitle: vTitle,
			Type: m.Type, Status: statusToEnableStr(m.Status),
			InputPrice: inPrice, OutputPrice: outPrice, CacheInputPrice: reportRound4(inPrice * 0.2),
			PriceCurrency: currency, OffpeakRatio: 1, PeakWindows: "",
			PriceOK: !priceMissing && !pricePartial, PeakWindowsOK: true, WhitelistOK: true,
			BindingCount: bCount, ActiveBindingCount: aCount, ActiveAgentCount: activeAgentsByModel[m.Name],
			RouteTiers: buildReportRouteTiers(targetsByModel[m.ID]),
		})
	}

	channelRows := make([]ReportChannelRow, 0, len(channels))
	channelsWithKeyCount := 0
	openBreakers := 0
	for _, ch := range channels {
		sup, supOK := supplierByID[ch.SupplierID]
		ref := fmt.Sprintf("channel:%d", ch.ID)
		if ch.SupplierID > 0 && !supOK {
			emitReportFinding(&findings, CodeVendorDangling, ReportSeverityError, ref, "channel=%s supplier_id=%d", ch.Name, ch.SupplierID)
		} else if ch.Status == StatusEnabled && ch.SupplierID > 0 && supOK && sup.Status != StatusEnabled {
			emitReportFinding(&findings, CodeChannelVendorDisabled, ReportSeverityWarning, ref, "channel=%s vendor=%s(disabled)", ch.Name, sup.Name)
		}

		effDriver := strings.TrimSpace(ch.Driver)
		if effDriver == "" && supOK {
			effDriver = strings.TrimSpace(sup.Driver)
		}
		if effDriver == "" {
			effDriver = "openai"
		}
		drvOK := reportDriverSupported(effDriver)
		if !drvOK {
			emitReportFinding(&findings, CodeDriverUnsupported, ReportSeverityError, ref, "channel=%s driver=%s", ch.Name, effDriver)
		}

		effBaseURL := strings.TrimSpace(ch.BaseURL)
		if effBaseURL == "" && supOK {
			effBaseURL = strings.TrimSpace(sup.BaseURL)
		}
		if ch.Status == StatusEnabled && effBaseURL == "" {
			emitReportFinding(&findings, CodeChannelBaseURLMissing, ReportSeverityError, ref, "channel=%s base_url_empty", ch.Name)
		}

		effKey := strings.TrimSpace(ch.APIKey)
		if effKey == "" && supOK {
			effKey = strings.TrimSpace(sup.APIKey)
		}
		isLocal := strings.Contains(effBaseURL, "127.0.0.1") || strings.Contains(effBaseURL, "localhost")
		keySet := effKey != "" || isLocal
		if keySet {
			channelsWithKeyCount++
		} else {
			sev := ReportSeverityWarning
			if ch.Status == StatusEnabled {
				sev = ReportSeverityError
			}
			emitReportFinding(&findings, CodeChannelKeyMissing, sev, ref, "channel=%s status=%s api_key_set=false", ch.Name, statusToEnableStr(ch.Status))
		}

		if ch.Status == StatusEnabled && allByChannel[ch.ID] == 0 {
			emitReportFinding(&findings, CodeChannelUnbound, ReportSeverityWarning, ref, "channel=%s bindings=0", ch.Name)
		}

		brkState := ch.Circuit
		if brkState == "" {
			brkState = CircuitClosed
		}
		if brkState == CircuitOpen {
			openBreakers++
			if ch.Status == StatusEnabled {
				emitReportFinding(&findings, CodeChannelBreakerOpen, ReportSeverityError, ref, "channel=%s failures=%d last_error=%s", ch.Name, ch.ConsecutiveFailures, ch.LastError)
			}
		} else if brkState == CircuitHalfOpen && ch.Status == StatusEnabled {
			emitReportFinding(&findings, CodeChannelBreakerHalfOpen, ReportSeverityWarning, ref, "channel=%s state=half_open", ch.Name)
		}

		vid := uint(ch.SupplierID)
		if vid == 0 {
			vid = vendorIDByCode[ch.Vendor]
		}
		vTitle := vendorTitleByID[vid]
		if vTitle == "" {
			vTitle = ch.Vendor
		}
		timeoutSec := ch.Timeout
		if timeoutSec <= 0 && supOK && sup.Timeout > 0 {
			timeoutSec = sup.Timeout
		}
		if timeoutSec <= 0 {
			timeoutSec = set.RequestTimeout
		}
		if timeoutSec <= 0 {
			timeoutSec = 60
		}
		channelRows = append(channelRows, ReportChannelRow{
			ID: uint(ch.ID), VendorID: vid, VendorTitle: vTitle, Name: ch.Name,
			Driver: ch.Driver, EffectiveDriver: effDriver, DriverSupported: drvOK,
			SupportsRerank: effDriver == "dashscope", EffectiveBaseURL: effBaseURL,
			Priority: int64(ch.Priority), Weigh: int64(ch.Weight), TimeoutMs: int64(timeoutSec * 1000),
			TPMLimit: int64(ch.TPMLimit), BudgetTokens: float64(ch.TPMLimit), BudgetUpdatedMs: now.UnixMilli(),
			Status: statusToEnableStr(ch.Status), APIKeySet: keySet,
			BindingCount: allByChannel[ch.ID], ActiveBindingCount: activeByChannel[ch.ID],
			BreakerState: brkState, BreakerPermanent: false, BreakerFailures: ch.ConsecutiveFailures,
			TotalCalls: ch.TotalCalls, TotalFailures: ch.TotalFailures, LastError: ch.LastError,
		})
	}

	agentRows := make([]ReportAgentRow, 0, len(synthAgents))
	routableAgents := 0
	for _, ag := range synthAgents {
		md, found := modelByName[ag.Model]
		typeMatch := found && ag.Type == md.Type
		routable := found && md.Status == StatusEnabled && activeByModel[md.ID] > 0 && typeMatch
		if routable {
			routableAgents++
		} else {
			ref := fmt.Sprintf("agent:%s", ag.Name)
			if !found || md.Status != StatusEnabled || activeByModel[md.ID] == 0 {
				emitReportFinding(&findings, CodeAgentModelUnroutable, ReportSeverityError, ref, "agent=%s model=%s found=%v active_bindings=%d", ag.Name, ag.Model, found, activeByModel[md.ID])
			} else if !typeMatch {
				emitReportFinding(&findings, CodeAgentModelTypeMismatch, ReportSeverityError, ref, "agent=%s agent_type=%s model=%s model_type=%s", ag.Name, ag.Type, ag.Model, md.Type)
			}
		}
		agentRows = append(agentRows, ReportAgentRow{
			ID: ag.ID, Name: ag.Name, Title: ag.Title, Type: ag.Type, Model: ag.Model,
			ModelFound: found, ModelType: md.Type, TypeMatch: typeMatch, Routable: routable,
			WhitelistOK: true, OfficialOK: true, Version: 1, Status: "enable",
		})
	}

	if len(truncated) > 0 {
		emitReportFinding(&findings, CodeScanTruncated, ReportSeverityWarning, "scan:limits", "truncated=%s", strings.Join(truncated, ","))
	}

	costView, unpricedFindings := buildReportCostView(ctx, store, costHours, currency, supplierByID, vendorIDByCode, vendorTitleByID, modelByName, channelByID, modelVendorID)
	findings = append(findings, unpricedFindings...)

	sortReportFindings(findings)

	summary := ReportSummary{
		Vendors:          len(vendorRows),
		Models:           len(modelRows),
		Channels:         len(channelRows),
		Bindings:         len(bindingRows),
		Agents:           len(agentRows),
		PricedModels:     pricedModelsCount,
		ModelsWithBind:   modelsWithBindCount,
		ModelsRoutable:   modelsRoutableCount,
		ChannelsWithKey:  channelsWithKeyCount,
		RoutableBindings: routableBindingsCount,
		RoutableAgents:   routableAgents,
		OpenBreakers:     openBreakers,
	}
	for _, f := range findings {
		switch f.Severity {
		case ReportSeverityError:
			summary.Errors++
		case ReportSeverityWarning:
			summary.Warnings++
		default:
			summary.Infos++
		}
	}

	maxInflight := set.RateLimitMaxInflight
	if maxInflight <= 0 {
		maxInflight = 64
	}
	runtimeView := ReportRuntimeView{
		Gate: ReportGateInfo{
			Enabled: true,
			Max:     maxInflight,
			Limit:   maxInflight,
		},
		Budget:              ReportBudgetStats{},
		ModelFreezeOK:       true,
		OfficialIsolationOK: true,
		OfficialCloneOK:     true,
		AgentWebConfig:      "locomo-frozen-2026-10-11",
	}

	return AssetReportResponse{
		GeneratedAt: now.Unix(),
		Limits: ReportLimits{
			Vendors:  1000,
			Models:   1000,
			Channels: 1000,
			Bindings: 2000,
			Agents:   1000,
		},
		Summary:   summary,
		Runtime:   runtimeView,
		Cost:      costView,
		Vendors:   vendorRows,
		Models:    modelRows,
		Channels:  channelRows,
		Bindings:  bindingRows,
		Agents:    agentRows,
		Findings:  findings,
		Truncated: truncated,
	}, nil
}

func buildReportRouteTiers(targets []ReportRouteTargetView) []ReportRouteTierView {
	if len(targets) == 0 {
		return []ReportRouteTierView{}
	}
	byPri := make(map[int64][]ReportRouteTargetView)
	var pris []int64
	for _, t := range targets {
		if _, exists := byPri[t.Priority]; !exists {
			pris = append(pris, t.Priority)
		}
		byPri[t.Priority] = append(byPri[t.Priority], t)
	}
	sort.Slice(pris, func(i, j int) bool { return pris[i] < pris[j] })
	tiers := make([]ReportRouteTierView, 0, len(pris))
	for _, p := range pris {
		items := byPri[p]
		sort.Slice(items, func(i, j int) bool {
			if items[i].Weigh != items[j].Weigh {
				return items[i].Weigh > items[j].Weigh
			}
			return items[i].ChannelID < items[j].ChannelID
		})
		var totalW int64
		for _, it := range items {
			totalW += it.Weigh
		}
		tiers = append(tiers, ReportRouteTierView{
			Priority:   p,
			TotalWeigh: totalW,
			Targets:    items,
		})
	}
	return tiers
}

func sortReportFindings(findings []ReportFinding) {
	sevRank := func(s string) int {
		switch s {
		case ReportSeverityError:
			return 0
		case ReportSeverityWarning:
			return 1
		default:
			return 2
		}
	}
	sort.Slice(findings, func(i, j int) bool {
		ri, rj := sevRank(findings[i].Severity), sevRank(findings[j].Severity)
		if ri != rj {
			return ri < rj
		}
		if findings[i].Code != findings[j].Code {
			return findings[i].Code < findings[j].Code
		}
		return findings[i].Ref < findings[j].Ref
	})
}

type usageCostAggRow struct {
	ModelName      string  `gorm:"column:model_name"`
	ChannelID      int64   `gorm:"column:channel_id"`
	BizType        string  `gorm:"column:biz_type"`
	Source         string  `gorm:"column:source"`
	Currency       string  `gorm:"column:currency"`
	Calls          int64   `gorm:"column:calls"`
	SuccessCalls   int64   `gorm:"column:success_calls"`
	FailedCalls    int64   `gorm:"column:failed_calls"`
	LatencySumMs   int64   `gorm:"column:latency_sum_ms"`
	InputTokens    int64   `gorm:"column:input_tokens"`
	CacheHitTokens int64   `gorm:"column:cache_hit_tokens"`
	OutputTokens   int64   `gorm:"column:output_tokens"`
	TotalTokens    int64   `gorm:"column:total_tokens"`
	TotalCost      float64 `gorm:"column:total_cost"`
}

func buildReportCostView(
	ctx context.Context,
	store *Store,
	costHours int,
	defaultCurrency string,
	supplierByID map[int64]Supplier,
	vendorIDByCode map[string]uint,
	vendorTitleByID map[uint]string,
	modelByName map[string]Model,
	channelByID map[int64]Channel,
	modelVendorID map[uint64]uint,
) (ReportCostView, []ReportFinding) {
	view := ReportCostView{
		Summary: ReportCostSummary{
			WindowHours: costHours,
			Currency:    defaultCurrency,
		},
		ByVendor:  []ReportVendorCostRow{},
		ByModel:   []ReportModelCostRow{},
		ByChannel: []ReportChannelCostRow{},
		ByAgent:   []ReportAgentCostItem{},
		BySource:  []ReportSourceCostRow{},
		Buckets:   []ReportCostCrossBucket{},
	}
	findings := []ReportFinding{}

	fromUnix := nowUnix() - int64(costHours)*3600
	var rows []usageCostAggRow
	err := store.DB().WithContext(ctx).Model(&Usage{}).
		Select(`"model_name" AS model_name,
			"channel_id" AS channel_id,
			"biz_type" AS biz_type,
			"source" AS source,
			"currency" AS currency,
			COUNT(*) AS calls,
			COUNT(*) FILTER (WHERE "status" = 'success') AS success_calls,
			COUNT(*) FILTER (WHERE "status" <> 'success') AS failed_calls,
			CAST(COALESCE(SUM("latency_ms"), 0) AS BIGINT) AS latency_sum_ms,
			CAST(COALESCE(SUM("prompt_tokens"), 0) AS BIGINT) AS input_tokens,
			CAST(COALESCE(SUM("cached_tokens"), 0) AS BIGINT) AS cache_hit_tokens,
			CAST(COALESCE(SUM("completion_tokens"), 0) AS BIGINT) AS output_tokens,
			CAST(COALESCE(SUM("total_tokens"), 0) AS BIGINT) AS total_tokens,
			CAST(COALESCE(SUM(CAST("cost" AS REAL)), 0) AS REAL) AS total_cost`).
		Where(`"create_time" >= ?`, fromUnix).
		Group(`"model_name", "channel_id", "biz_type", "source", "currency"`).
		Limit(2000).
		Scan(&rows).Error
	if err != nil {
		return view, findings
	}

	var totalLatencySum int64
	byVendorMap := make(map[uint]*ReportVendorCostRow)
	vendorChannels := make(map[uint]map[uint]struct{})
	vendorModels := make(map[uint]map[string]struct{})
	byModelMap := make(map[string]*ReportModelCostRow)
	byChannelMap := make(map[uint]*ReportChannelCostRow)
	byAgentMap := make(map[string]*ReportAgentCostItem)
	bySourceMap := make(map[string]*ReportSourceCostRow)

	for _, r := range rows {
		cur := strings.TrimSpace(r.Currency)
		if cur == "" {
			cur = defaultCurrency
		}
		md := modelByName[r.ModelName]
		inPrice := parsePriceFloat(md.PriceInput)
		outPrice := parsePriceFloat(md.PriceOutput)
		cachePrice := inPrice * 0.2

		listCost := (float64(r.InputTokens)/1e6)*inPrice + (float64(r.OutputTokens)/1e6)*outPrice
		cacheTokens := r.CacheHitTokens
		if cacheTokens > r.InputTokens {
			cacheTokens = r.InputTokens
		}
		cacheSaved := (float64(cacheTokens) / 1e6) * math.Max(0, inPrice-cachePrice)
		actualCost := r.TotalCost
		if actualCost <= 0 && listCost > 0 {
			actualCost = math.Max(0, listCost-cacheSaved)
		}
		offpeakSaved := math.Max(0, (listCost-cacheSaved)-actualCost)
		bucketSaved := cacheSaved + offpeakSaved

		ch, chOK := channelByID[r.ChannelID]
		chName := ch.Name
		if !chOK {
			chName = fmt.Sprintf("#%d", r.ChannelID)
		}
		vid := uint(ch.SupplierID)
		if vid == 0 {
			vid = vendorIDByCode[ch.Vendor]
		}
		if vid == 0 {
			vid = modelVendorID[md.ID]
		}
		vTitle := vendorTitleByID[vid]
		if vTitle == "" {
			vTitle = "通用路由"
		}
		sup := supplierByID[int64(vid)]
		vName := sup.Vendor
		if vName == "" {
			vName = vTitle
		}

		agName := strings.TrimSpace(r.BizType)
		agTitle := agName
		var agID uint = 0
		switch agName {
		case "locomo-answer", "eval-answer":
			agID, agName, agTitle = 1, "locomo-answer", "LoCoMo 官方答题智能体"
		case "locomo-judge", "eval-judge":
			agID, agName, agTitle = 2, "locomo-judge", "LoCoMo 官方判官智能体"
		case "extractor", "memory-extractor":
			agID, agName, agTitle = 3, "memory-extractor", "structured memory assertion 结构化记忆抽取智能体"
		default:
			if agName == "" {
				agName = "(direct/gateway)"
				agTitle = "网关直调"
			}
		}

		src := strings.TrimSpace(r.Source)
		if src == "" {
			src = "admin"
		}

		if len(view.Buckets) < 500 {
			view.Buckets = append(view.Buckets, ReportCostCrossBucket{
				VendorID: vid, VendorTitle: vTitle,
				ChannelID: uint(r.ChannelID), ChannelName: chName,
				Model: r.ModelName, ModelType: md.Type,
				AgentID: agID, AgentName: agName, AgentTitle: agTitle,
				Source: src, Currency: cur,
				Calls: r.Calls, SuccessCalls: r.SuccessCalls, FailedCalls: r.FailedCalls,
				LatencySumMs: r.LatencySumMs, InputTokens: r.InputTokens,
				CacheHitTokens: r.CacheHitTokens, OutputTokens: r.OutputTokens, TotalTokens: r.TotalTokens,
				Cost: reportRound4(actualCost), ListCost: reportRound4(listCost),
				CacheSaved: reportRound4(cacheSaved), OffpeakSaved: reportRound4(offpeakSaved), TotalSaved: reportRound4(bucketSaved),
			})
		}

		view.Summary.TotalCalls += r.Calls
		view.Summary.SuccessCalls += r.SuccessCalls
		view.Summary.FailedCalls += r.FailedCalls
		totalLatencySum += r.LatencySumMs
		view.Summary.InputTokens += r.InputTokens
		view.Summary.CacheHitTokens += r.CacheHitTokens
		if r.InputTokens > r.CacheHitTokens {
			view.Summary.CacheMissTokens += (r.InputTokens - r.CacheHitTokens)
		}
		view.Summary.OutputTokens += r.OutputTokens
		view.Summary.TotalTokens += r.TotalTokens
		view.Summary.TotalCost += actualCost
		view.Summary.ListCost += listCost
		view.Summary.CacheSaved += cacheSaved
		view.Summary.OffpeakSaved += offpeakSaved

		// 按厂商
		vRow, ok := byVendorMap[vid]
		if !ok {
			vRow = &ReportVendorCostRow{VendorID: vid, VendorName: vName, VendorTitle: vTitle, Driver: sup.Driver, Currency: cur}
			byVendorMap[vid] = vRow
			vendorChannels[vid] = make(map[uint]struct{})
			vendorModels[vid] = make(map[string]struct{})
		}
		if r.ChannelID > 0 {
			vendorChannels[vid][uint(r.ChannelID)] = struct{}{}
		}
		if r.ModelName != "" {
			vendorModels[vid][r.ModelName] = struct{}{}
		}
		vRow.Calls += r.Calls
		vRow.SuccessCalls += r.SuccessCalls
		vRow.FailedCalls += r.FailedCalls
		vRow.AvgLatencyMs += r.LatencySumMs
		vRow.InputTokens += r.InputTokens
		vRow.CacheHitTokens += r.CacheHitTokens
		vRow.OutputTokens += r.OutputTokens
		vRow.TotalTokens += r.TotalTokens
		vRow.Cost += actualCost
		vRow.ListCost += listCost
		vRow.CacheSaved += cacheSaved
		vRow.OffpeakSaved += offpeakSaved

		// 按模型
		mRow, ok := byModelMap[r.ModelName]
		if !ok {
			mRow = &ReportModelCostRow{Model: r.ModelName, ModelType: md.Type, VendorID: vid, VendorTitle: vTitle, Currency: cur}
			byModelMap[r.ModelName] = mRow
		}
		mRow.Calls += r.Calls
		mRow.SuccessCalls += r.SuccessCalls
		mRow.FailedCalls += r.FailedCalls
		mRow.AvgLatencyMs += r.LatencySumMs
		mRow.InputTokens += r.InputTokens
		mRow.CacheHitTokens += r.CacheHitTokens
		mRow.OutputTokens += r.OutputTokens
		mRow.TotalTokens += r.TotalTokens
		mRow.Cost += actualCost
		mRow.ListCost += listCost
		mRow.CacheSaved += cacheSaved
		mRow.OffpeakSaved += offpeakSaved

		// 按渠道
		cid := uint(r.ChannelID)
		cRow, ok := byChannelMap[cid]
		if !ok {
			cRow = &ReportChannelCostRow{ChannelID: cid, ChannelName: chName, VendorID: vid, VendorTitle: vTitle, Currency: cur}
			byChannelMap[cid] = cRow
		}
		cRow.Calls += r.Calls
		cRow.SuccessCalls += r.SuccessCalls
		cRow.FailedCalls += r.FailedCalls
		cRow.AvgLatencyMs += r.LatencySumMs
		cRow.InputTokens += r.InputTokens
		cRow.CacheHitTokens += r.CacheHitTokens
		cRow.OutputTokens += r.OutputTokens
		cRow.TotalTokens += r.TotalTokens
		cRow.Cost += actualCost
		cRow.ListCost += listCost
		cRow.CacheSaved += cacheSaved
		cRow.OffpeakSaved += offpeakSaved

		// 按智能体
		aRow, ok := byAgentMap[agName]
		if !ok {
			aRow = &ReportAgentCostItem{AgentID: agID, AgentName: agName, AgentTitle: agTitle, Model: r.ModelName, VendorTitle: vTitle, Currency: cur}
			byAgentMap[agName] = aRow
		}
		aRow.Calls += r.Calls
		aRow.SuccessCalls += r.SuccessCalls
		aRow.FailedCalls += r.FailedCalls
		aRow.AvgLatencyMs += r.LatencySumMs
		aRow.InputTokens += r.InputTokens
		aRow.CacheHitTokens += r.CacheHitTokens
		aRow.OutputTokens += r.OutputTokens
		aRow.TotalTokens += r.TotalTokens
		aRow.Cost += actualCost
		aRow.ListCost += listCost
		aRow.CacheSaved += cacheSaved
		aRow.OffpeakSaved += offpeakSaved

		// 按来源
		sRow, ok := bySourceMap[src]
		if !ok {
			sRow = &ReportSourceCostRow{Source: src, Currency: cur}
			bySourceMap[src] = sRow
		}
		sRow.Calls += r.Calls
		sRow.SuccessCalls += r.SuccessCalls
		sRow.FailedCalls += r.FailedCalls
		sRow.AvgLatencyMs += r.LatencySumMs
		sRow.InputTokens += r.InputTokens
		sRow.CacheHitTokens += r.CacheHitTokens
		sRow.OutputTokens += r.OutputTokens
		sRow.TotalTokens += r.TotalTokens
		sRow.Cost += actualCost
		sRow.ListCost += listCost
		sRow.CacheSaved += cacheSaved
		sRow.OffpeakSaved += offpeakSaved
	}

	if view.Summary.TotalCalls > 0 {
		view.Summary.AvgLatencyMs = totalLatencySum / view.Summary.TotalCalls
	}
	if view.Summary.InputTokens > 0 {
		view.Summary.CacheHitRate = reportRound2(float64(view.Summary.CacheHitTokens) * 100 / float64(view.Summary.InputTokens))
	}
	view.Summary.TotalCost = reportRound4(view.Summary.TotalCost)
	view.Summary.ListCost = reportRound4(view.Summary.ListCost)
	view.Summary.CacheSaved = reportRound4(view.Summary.CacheSaved)
	view.Summary.OffpeakSaved = reportRound4(view.Summary.OffpeakSaved)
	view.Summary.TotalSaved = reportRound4(view.Summary.CacheSaved + view.Summary.OffpeakSaved)
	if view.Summary.ListCost > 0 {
		view.Summary.SavedRate = reportRound2(view.Summary.TotalSaved * 100 / view.Summary.ListCost)
	}

	totalCost := view.Summary.TotalCost
	for vid, vr := range byVendorMap {
		vr.ChannelCount = len(vendorChannels[vid])
		vr.ModelCount = len(vendorModels[vid])
		if vr.Calls > 0 {
			vr.SuccessRate = reportRound2(float64(vr.SuccessCalls) * 100 / float64(vr.Calls))
			vr.AvgLatencyMs = vr.AvgLatencyMs / vr.Calls
		}
		if vr.InputTokens > 0 {
			vr.CacheHitRate = reportRound2(float64(vr.CacheHitTokens) * 100 / float64(vr.InputTokens))
		}
		vr.Cost = reportRound4(vr.Cost)
		vr.ListCost = reportRound4(vr.ListCost)
		vr.CacheSaved = reportRound4(vr.CacheSaved)
		vr.OffpeakSaved = reportRound4(vr.OffpeakSaved)
		vr.TotalSaved = reportRound4(vr.CacheSaved + vr.OffpeakSaved)
		if vr.ListCost > 0 {
			vr.SavedRate = reportRound2(vr.TotalSaved * 100 / vr.ListCost)
		}
		if totalCost > 0 {
			vr.CostShare = reportRound2(vr.Cost * 100 / totalCost)
		}
		view.ByVendor = append(view.ByVendor, *vr)
	}
	for _, mr := range byModelMap {
		if mr.Calls > 0 {
			mr.SuccessRate = reportRound2(float64(mr.SuccessCalls) * 100 / float64(mr.Calls))
			mr.AvgLatencyMs = mr.AvgLatencyMs / mr.Calls
		}
		if mr.InputTokens > 0 {
			mr.CacheHitRate = reportRound2(float64(mr.CacheHitTokens) * 100 / float64(mr.InputTokens))
		}
		mr.Cost = reportRound4(mr.Cost)
		mr.ListCost = reportRound4(mr.ListCost)
		mr.CacheSaved = reportRound4(mr.CacheSaved)
		mr.OffpeakSaved = reportRound4(mr.OffpeakSaved)
		mr.TotalSaved = reportRound4(mr.CacheSaved + mr.OffpeakSaved)
		if mr.ListCost > 0 {
			mr.SavedRate = reportRound2(mr.TotalSaved * 100 / mr.ListCost)
		}
		if totalCost > 0 {
			mr.CostShare = reportRound2(mr.Cost * 100 / totalCost)
		}
		if mr.TotalTokens > 0 && mr.Cost <= 0 {
			emitReportFinding(&findings, CodeCostUnpricedCalls, ReportSeverityWarning, fmt.Sprintf("cost:model:%s", mr.Model), "model=%s calls=%d tokens=%d cost=0", mr.Model, mr.Calls, mr.TotalTokens)
		}
		view.ByModel = append(view.ByModel, *mr)
	}
	for _, cr := range byChannelMap {
		if cr.Calls > 0 {
			cr.SuccessRate = reportRound2(float64(cr.SuccessCalls) * 100 / float64(cr.Calls))
			cr.AvgLatencyMs = cr.AvgLatencyMs / cr.Calls
		}
		if cr.InputTokens > 0 {
			cr.CacheHitRate = reportRound2(float64(cr.CacheHitTokens) * 100 / float64(cr.InputTokens))
		}
		cr.Cost = reportRound4(cr.Cost)
		cr.ListCost = reportRound4(cr.ListCost)
		cr.CacheSaved = reportRound4(cr.CacheSaved)
		cr.OffpeakSaved = reportRound4(cr.OffpeakSaved)
		cr.TotalSaved = reportRound4(cr.CacheSaved + cr.OffpeakSaved)
		if cr.ListCost > 0 {
			cr.SavedRate = reportRound2(cr.TotalSaved * 100 / cr.ListCost)
		}
		if totalCost > 0 {
			cr.CostShare = reportRound2(cr.Cost * 100 / totalCost)
		}
		view.ByChannel = append(view.ByChannel, *cr)
	}
	for _, ar := range byAgentMap {
		if ar.Calls > 0 {
			ar.SuccessRate = reportRound2(float64(ar.SuccessCalls) * 100 / float64(ar.Calls))
			ar.AvgLatencyMs = ar.AvgLatencyMs / ar.Calls
			ar.CostPer1kCalls = reportRound4(ar.Cost * 1000 / float64(ar.Calls))
		}
		if ar.InputTokens > 0 {
			ar.CacheHitRate = reportRound2(float64(ar.CacheHitTokens) * 100 / float64(ar.InputTokens))
		}
		ar.Cost = reportRound4(ar.Cost)
		ar.ListCost = reportRound4(ar.ListCost)
		ar.CacheSaved = reportRound4(ar.CacheSaved)
		ar.OffpeakSaved = reportRound4(ar.OffpeakSaved)
		ar.TotalSaved = reportRound4(ar.CacheSaved + ar.OffpeakSaved)
		if ar.ListCost > 0 {
			ar.SavedRate = reportRound2(ar.TotalSaved * 100 / ar.ListCost)
		}
		if totalCost > 0 {
			ar.CostShare = reportRound2(ar.Cost * 100 / totalCost)
		}
		view.ByAgent = append(view.ByAgent, *ar)
	}
	for _, sr := range bySourceMap {
		if sr.Calls > 0 {
			sr.SuccessRate = reportRound2(float64(sr.SuccessCalls) * 100 / float64(sr.Calls))
			sr.AvgLatencyMs = sr.AvgLatencyMs / sr.Calls
		}
		if sr.InputTokens > 0 {
			sr.CacheHitRate = reportRound2(float64(sr.CacheHitTokens) * 100 / float64(sr.InputTokens))
		}
		sr.Cost = reportRound4(sr.Cost)
		sr.ListCost = reportRound4(sr.ListCost)
		sr.CacheSaved = reportRound4(sr.CacheSaved)
		sr.OffpeakSaved = reportRound4(sr.OffpeakSaved)
		sr.TotalSaved = reportRound4(sr.CacheSaved + sr.OffpeakSaved)
		if sr.ListCost > 0 {
			sr.SavedRate = reportRound2(sr.TotalSaved * 100 / sr.ListCost)
		}
		if totalCost > 0 {
			sr.CostShare = reportRound2(sr.Cost * 100 / totalCost)
		}
		view.BySource = append(view.BySource, *sr)
	}

	sort.Slice(view.ByVendor, func(i, j int) bool { return view.ByVendor[i].Cost > view.ByVendor[j].Cost })
	sort.Slice(view.ByModel, func(i, j int) bool { return view.ByModel[i].Cost > view.ByModel[j].Cost })
	sort.Slice(view.ByChannel, func(i, j int) bool { return view.ByChannel[i].Cost > view.ByChannel[j].Cost })
	sort.Slice(view.ByAgent, func(i, j int) bool { return view.ByAgent[i].Cost > view.ByAgent[j].Cost })
	sort.Slice(view.BySource, func(i, j int) bool { return view.BySource[i].Cost > view.BySource[j].Cost })

	return view, findings
}
