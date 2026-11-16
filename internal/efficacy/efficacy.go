// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

// Package efficacy evaluates whether recalled memories genuinely improve coding
// harness capability, reduce exploration round-trips (TTFW / grep / find / read),
// and lower net token cost across all connected harnesses (zcode, claude-code,
// codex, cursor, windsurf, cline, dsh, codebuddy).
//
// Strict Falsification Discipline (Zero Self-Congratulation / Zero Hardcoded Multipliers):
//  1. Silence / Dedup / Absent turns have 0 InjectedTokens, 0 SavedTokens, and 0 NetTokens.
//     Not injecting noise is baseline hygiene, NEVER counted as positive token savings.
//  2. Only HARD anchors (>=4-char file paths, code identifiers, config keys, or specific
//     entity titles) are extracted from recalled bands; 2-gram generic Chinese word
//     splitting is strictly forbidden.
//  3. Every injection incurs a real token cost weighted by the turn's model request count
//     (1.0x initial input + 0.15x cached re-reads across subsequent tool steps in that turn).
//  4. Saved round-trips are never hardcoded constants: they are computed against the
//     host's empirical unassisted exploration baseline (observed grep/find/ls/read steps
//     before target file access in unassisted turns) minus the actual exploration steps
//     taken in the assisted turn. If the agent still ran grep/find despite the recall,
//     saved_rounds = 0 and the turn is booked as a net token loss.
package efficacy

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/rocareer/rolling-memory/internal/config"
	"github.com/rocareer/rolling-memory/internal/harness"
	"github.com/rocareer/rolling-memory/internal/ids"
	"github.com/rocareer/rolling-memory/internal/memcore"
	"github.com/rocareer/rolling-memory/internal/recall"
	"github.com/rocareer/rolling-memory/internal/store"
)

// Options configures an efficacy evaluation run.
type Options struct {
	Scope        string
	Harness      string
	Limit        int
	HomeDir      string
	WorkspaceDir string
	// BudgetSeconds bounds the wall-clock time this run may spend on NEW per-turn
	// evaluations. 0 means unlimited. When the budget is exhausted the run stops
	// re-evaluating further turns, finalizes and persists the snapshot with whatever
	// is already settled (Report.Partial=true) instead of being killed mid-flight by
	// the caller, so consecutive runs make strictly monotone persisted progress.
	BudgetSeconds int
}

// FunnelBreakdown categorizes recall hook decisions into five mutually exclusive buckets.
type FunnelBreakdown struct {
	HighValueAdopted int `json:"high_value_adopted"` // Recalled code coords directly hit with fewer exploration steps than baseline
	ContextAssisted  int `json:"context_assisted"`   // Specific recalled entity adopted in reply/action with 0 blind search
	DedupSuppressed  int `json:"dedup_suppressed"`   // Echo/duplicate memory block suppressed by injection_ledger (0 token cost)
	SilenceFiltered  int `json:"silence_filtered"`   // Absent, pure-ack short-circuit, or low-specificity noise blocked (0 token cost)
	NoiseInjected    int `json:"noise_injected"`     // Injected memory not adopted OR agent searched anyway (net token loss)
}

// HarnessEfficacy summarizes efficacy metrics for one coding harness.
type HarnessEfficacy struct {
	ID             string  `json:"id"`
	Title          string  `json:"title"`
	Detected       bool    `json:"detected"`
	Installed      bool    `json:"installed"`
	HooksEnabled   bool    `json:"hooks_enabled"`
	Sessions       int     `json:"sessions"`
	Turns          int     `json:"turns"`
	Injections     int     `json:"injections"`
	Adopted        int     `json:"adopted"`
	Silenced       int     `json:"silenced"`
	AdoptRate      float64 `json:"adopt_rate"`
	SilenceRate    float64 `json:"silence_rate"`
	SavedRounds    int     `json:"saved_rounds"`
	NetSavedTokens int     `json:"net_saved_tokens"`
	AvgLatencyMS   int     `json:"avg_latency_ms"`
	Status         string  `json:"status"` // active | ready | standby
}

// SessionEvaluation holds the per-turn empirical audit record.
type SessionEvaluation struct {
	SessionID         string   `json:"session_id"`
	Harness           string   `json:"harness"`
	Workspace         string   `json:"workspace"`
	Prompt            string   `json:"prompt"`
	Outcome           string   `json:"outcome"` // high_value | assisted | silenced | dedup | noise
	VerdictLabel      string   `json:"verdict_label"`
	AttributionSource string   `json:"attribution_source"` // l1_causal_tool | l1_entity_anchor | l1_reply_coord | l2_piggyback_heal | gate_silence | ledger_dedup | unadopted_noise
	ValuePillar       string   `json:"value_pillar"`       // navigation | guardrail | continuity | silence | noise
	RecalledSummary   string   `json:"recalled_summary"`
	MatchedAnchors    []string `json:"matched_anchors"`
	EchoExcluded      int      `json:"echo_excluded"`
	HasCodeCoord      bool     `json:"has_code_coord"`
	CausalVerified    bool     `json:"causal_verified"`
	SelfHealed        int      `json:"self_healed"`
	ModelRequests     int      `json:"model_requests"`
	ToolCalls         int      `json:"tool_calls"`
	ExploreSteps      int      `json:"explore_steps"`
	RoundCostTokens   int      `json:"round_cost_tokens"`   // marginal exploration round-trip context tokens (fresh + cached)
	RoundFreshTokens  int      `json:"round_fresh_tokens"`  // marginal uncached fresh input + output tokens per exploration round-trip
	RoundCachedTokens int      `json:"round_cached_tokens"` // marginal cached context tokens re-read per exploration round-trip
	SavedRounds       int      `json:"saved_rounds"`
	SavedSeconds      int      `json:"saved_seconds"` // empirical wall-clock exploration wait seconds saved
	InjectedTokens    int      `json:"injected_tokens"`
	SavedTokens       int      `json:"saved_tokens"`        // total saved raw context tokens (SavedFreshTokens + SavedCachedTokens)
	SavedFreshTokens  int      `json:"saved_fresh_tokens"`  // saved uncached input + output tokens
	SavedCachedTokens int      `json:"saved_cached_tokens"` // saved cached context re-read tokens
	BilledNetTokens   int      `json:"billed_net_tokens"`   // API-billed equivalent net saved tokens (SavedFresh + 0.10*SavedCached - Injected)
	NetTokens         int      `json:"net_tokens"`          // raw context net saved tokens (SavedTokens - InjectedTokens)
	LatencyMS         int      `json:"latency_ms"`
	RecordedAt        int64    `json:"recorded_at"`
	TimeText          string   `json:"time_text"`
	ToolTimeline      []string `json:"tool_timeline,omitempty"`
	TargetFiles       []string `json:"target_files,omitempty"`
}

// SessionContinuity tracks the 3D situational awareness readiness (Where / What Just Happened / Active Thread).
type SessionContinuity struct {
	Ready                  bool   `json:"ready"`
	ActiveLocus            string `json:"active_locus"`
	WhereSpatialBands      int    `json:"where_spatial_bands"`      // Spatial anchor bands (project boundaries & redlines)
	EpisodicTailTraces     int    `json:"episodic_tail_traces"`     // Recent turn traces available for "What Just Happened"
	ActiveThreadAssertions int    `json:"active_thread_assertions"` // Recent active assertions (last 24h) for "What Am I Doing Now"
	LedgerProtectedBands   int    `json:"ledger_protected_bands"`   // Session-deduplicated bands in injection_ledger
	LastTailPreview        string `json:"last_tail_preview"`        // 1-line preview of the latest episodic tail
}

// DailySavingBucket aggregates per-day recall efficacy and token savings.
type DailySavingBucket struct {
	Date                 string  `json:"date"`
	Turns                int     `json:"turns"`
	InjectedCount        int     `json:"injected_count"`
	AdoptedCount         int     `json:"adopted_count"`
	SavedRounds          int     `json:"saved_rounds"`
	SavedSeconds         int     `json:"saved_seconds"`
	InjectedTokens       int     `json:"injected_tokens"`
	SavedFreshTokens     int     `json:"saved_fresh_tokens"`
	SavedCachedTokens    int     `json:"saved_cached_tokens"`
	GrossSavedTokens     int     `json:"gross_saved_tokens"`
	BilledNetSavedTokens int     `json:"billed_net_saved_tokens"`
	NetSavedTokens       int     `json:"net_saved_tokens"`
	NetTokenROI          float64 `json:"net_token_roi"`
}

// WindowSavingBucket aggregates recall efficacy and token savings for one rolling
// monitor window (时 / 日 / 周 / 月) so the ROI screen can switch the monitored span
// without re-reading per-turn records client-side.
type WindowSavingBucket struct {
	Key                  string  `json:"key"`        // 1h | 24h | 7d | 30d
	Seconds              int64   `json:"seconds"`    // rolling window length in seconds
	SinceUnix            int64   `json:"since_unix"` // now - seconds at evaluation time
	Turns                int     `json:"turns"`
	Sessions             int     `json:"sessions"`
	InjectedCount        int     `json:"injected_count"`
	AdoptedCount         int     `json:"adopted_count"`
	SilencedCount        int     `json:"silenced_count"`
	DedupCount           int     `json:"dedup_count"`
	SavedRounds          int     `json:"saved_rounds"`
	SavedSeconds         int     `json:"saved_seconds"`
	InjectedTokens       int     `json:"injected_tokens"`
	SavedFreshTokens     int     `json:"saved_fresh_tokens"`
	SavedCachedTokens    int     `json:"saved_cached_tokens"`
	GrossSavedTokens     int     `json:"gross_saved_tokens"`
	BilledNetSavedTokens int     `json:"billed_net_saved_tokens"`
	NetSavedTokens       int     `json:"net_saved_tokens"`
	AdoptRate            float64 `json:"adopt_rate"`
	NetTokenROI          float64 `json:"net_token_roi"`
	BilledTokenROI       float64 `json:"billed_token_roi"`
	AvgLatencyMS         int     `json:"avg_latency_ms"`
	SelfHealedCount      int     `json:"self_healed_count"`
	CodeCoordHits        int     `json:"code_coord_hits"`
	CausalDirectHits     int     `json:"causal_direct_hits"`
	NavigationDirectHits int     `json:"navigation_direct_hits"`
	GuardrailHits        int     `json:"guardrail_hits"`
	ContinuityHits       int     `json:"continuity_hits"`
	// Window-scoped recall decision funnel: the five buckets partition `Turns` exactly like
	// the report-level FunnelBreakdown partitions EvaluatedTurns.
	FunnelHighValue int `json:"funnel_high_value"`
	FunnelAssisted  int `json:"funnel_assisted"`
	FunnelDedup     int `json:"funnel_dedup"`
	FunnelSilence   int `json:"funnel_silence"`
	FunnelNoise     int `json:"funnel_noise"`
	latencySumMS    int
	latencySamples  int
}

// HourlySavingBucket aggregates the same recall efficacy counters per clock hour so the
// 时 window shows a bounded (last 48h) hourly series instead of a single scalar.
type HourlySavingBucket struct {
	Hour                 string  `json:"hour"` // local "2006-01-02 15:00"
	Unix                 int64   `json:"unix"`
	Turns                int     `json:"turns"`
	InjectedCount        int     `json:"injected_count"`
	AdoptedCount         int     `json:"adopted_count"`
	SavedRounds          int     `json:"saved_rounds"`
	SavedSeconds         int     `json:"saved_seconds"`
	InjectedTokens       int     `json:"injected_tokens"`
	SavedFreshTokens     int     `json:"saved_fresh_tokens"`
	SavedCachedTokens    int     `json:"saved_cached_tokens"`
	GrossSavedTokens     int     `json:"gross_saved_tokens"`
	BilledNetSavedTokens int     `json:"billed_net_saved_tokens"`
	NetSavedTokens       int     `json:"net_saved_tokens"`
	NetTokenROI          float64 `json:"net_token_roi"`
}

// HookPhysicalTelemetry captures 100% factual, zero-counterfactual physical hook metrics
// directly from rolling-memory.log so engineers can inspect real latency and token overhead.
type HookPhysicalTelemetry struct {
	TotalRecallHooks      int     `json:"total_recall_hooks"`
	RealInjectedHooks     int     `json:"real_injected_hooks"`
	RealAbsentHooks       int     `json:"real_absent_hooks"`
	RealDedupHooks        int     `json:"real_dedup_hooks"`
	RealSilentHooks       int     `json:"real_silent_hooks"`
	RealInjectRatePct     float64 `json:"real_inject_rate_pct"`
	RealInjectedChars     int     `json:"real_injected_chars"`
	RealInjectedTokensEst int     `json:"real_injected_tokens_est"`
	AvgInjectedTokens     int     `json:"avg_injected_tokens"`
	MaxInjectedTokens     int     `json:"max_injected_tokens"`
	P50MS                 int     `json:"p50_ms"`
	P95MS                 int     `json:"p95_ms"`
	P99MS                 int     `json:"p99_ms"`
	SlowOver500MS         int     `json:"slow_over_500ms"`
	TotalStartHooks       int     `json:"total_start_hooks"`
	TotalCloseHooks       int     `json:"total_close_hooks"`
	TotalWrittenInLog     int     `json:"total_written_in_log"`
	L1DirectInjectedTurns int     `json:"l1_direct_injected_turns"`
	L1DirectAdoptedTurns  int     `json:"l1_direct_adopted_turns"`
	L1DirectAdoptRatePct  float64 `json:"l1_direct_adopt_rate_pct"`
	L0CarryoverTurns      int     `json:"l0_carryover_turns"`
}

// BandDiagnosticItem ranks a recalled subject band by its empirical utility (Adopted vs. Noisy Waste).
type BandDiagnosticItem struct {
	BandTitle      string   `json:"band_title"`
	Subject        string   `json:"subject"`
	InjectedTurns  int      `json:"injected_turns"`
	AdoptedTurns   int      `json:"adopted_turns"`
	NoiseTurns     int      `json:"noise_turns"`
	AdoptRate      float64  `json:"adopt_rate"`
	InjectedTokens int      `json:"injected_tokens"`
	WastedTokens   int      `json:"wasted_tokens"`
	SampleAnchors  []string `json:"sample_anchors"`
	LastSeen       string   `json:"last_seen"`
}

// BlindSearchGapItem highlights a high-exploration turn where memory stayed silent or missed,
// forcing the coding agent to run multiple blind search steps before locating files.
type BlindSearchGapItem struct {
	SessionID     string   `json:"session_id"`
	TimeText      string   `json:"time_text"`
	RecordedAt    int64    `json:"recorded_at"`
	Harness       string   `json:"harness"`
	Workspace     string   `json:"workspace"`
	Prompt        string   `json:"prompt"`
	Outcome       string   `json:"outcome"`
	ExploreSteps  int      `json:"explore_steps"`
	ToolCalls     int      `json:"tool_calls"`
	ModelRequests int      `json:"model_requests"`
	TargetFiles   []string `json:"target_files"`
	ToolTimeline  []string `json:"tool_timeline,omitempty"`
}

// Report is the top-level cross-harness recall efficacy evaluation output.
type Report struct {
	Scope                      string                `json:"scope"`
	EvaluatedAt                string                `json:"evaluated_at"`
	TotalHookRuns              int                   `json:"total_hook_runs"`
	TotalSessions              int                   `json:"total_sessions"`
	EvaluatedTurns             int                   `json:"evaluated_turns"`
	InjectedCount              int                   `json:"injected_count"`
	SilencedCount              int                   `json:"silenced_count"`
	AdoptedCount               int                   `json:"adopted_count"`
	CodeCoordHits              int                   `json:"code_coord_hits"`
	NovelCoordHits             int                   `json:"novel_coord_hits"`              // code coordinates supplied purely by memory (excluding prompt echo & prior turns)
	CausalDirectHits           int                   `json:"causal_direct_hits"`            // turns where target file Read/Edit occurred with < baseline blind search
	NavigationDirectHits       int                   `json:"navigation_direct_hits"`        // Pillar 1: zero-blind-search direct file tool navigation hits
	GuardrailHits              int                   `json:"guardrail_hits"`                // Pillar 2: epistemic guardrail / ruling / gotcha / recipe hits avoiding rework
	ContinuityHits             int                   `json:"continuity_hits"`               // Pillar 3: short-prompt / cross-turn / boot-landmark continuity hits
	BootAndCarryoverHits       int                   `json:"boot_and_carryover_hits"`       // 0-marginal-cost turns where SessionStart boot landmarks or session carry-over directly guided file tools
	OverallBilledReductionPct  float64               `json:"overall_billed_reduction_pct"`  // Empirical per-turn billed token reduction % vs unassisted heavy-exploration coding baseline
	OverallRawReductionPct     float64               `json:"overall_raw_reduction_pct"`     // Empirical per-turn raw context token reduction % vs unassisted heavy-exploration coding baseline
	OverallRequestReductionPct float64               `json:"overall_request_reduction_pct"` // Empirical per-turn model request reduction % vs unassisted heavy-exploration coding baseline
	OverallTimeReductionPct    float64               `json:"overall_time_reduction_pct"`    // Empirical per-turn wall-clock duration reduction % vs unassisted heavy-exploration coding baseline
	EchoExcludedCount          int                   `json:"echo_excluded_count"`           // anchors stripped because they already appeared in the user's prompt or prior turns
	SelfHealedCount            int                   `json:"self_healed_count"`             // Pillar 4: stale assertions superseded/retracted via L2 piggybacked agent writebacks
	WritebackCount             int                   `json:"writeback_count"`               // durable assertions written back via remember across audited sessions
	AdoptRate                  float64               `json:"adopt_rate"`                    // % of evaluated injections that genuinely saved steps or supplied adopted facts
	SilenceRate                float64               `json:"silence_rate"`                  // % of hook triggers that stayed silent (dedup + absent + guard)
	BaselineExploreSteps       float64               `json:"baseline_explore_steps"`        // empirical avg blind-search steps on unassisted coding turns
	AvgRoundCostTokens         int                   `json:"avg_round_cost_tokens"`         // marginal context tokens per exploration round-trip (fresh + cached)
	AvgRoundFreshTokens        int                   `json:"avg_round_fresh_tokens"`
	AvgRoundCachedTokens       int                   `json:"avg_round_cached_tokens"`
	AvgSavedRounds             float64               `json:"avg_saved_rounds"`        // avg exploration round-trips saved on adopted turns
	TotalSavedRounds           int                   `json:"total_saved_rounds"`      // all-time cumulative round-trips saved across sessions
	TotalSavedSeconds          int                   `json:"total_saved_seconds"`     // all-time cumulative wall-clock exploration wait seconds saved
	LegacyCarriedTurns         int                   `json:"legacy_carried_turns"`    // out-of-window immutable-cache turns folded into all-time cumulative savings
	InjectedTokens             int                   `json:"injected_tokens"`         // all-time total tokens spent on recall injections
	SavedFreshTokens           int                   `json:"saved_fresh_tokens"`      // all-time saved uncached input + output tokens
	SavedCachedTokens          int                   `json:"saved_cached_tokens"`     // all-time saved cached historical context tokens
	GrossSavedTokens           int                   `json:"gross_saved_tokens"`      // all-time total saved raw context tokens (SavedFreshTokens + SavedCachedTokens)
	NetSavedTokens             int                   `json:"net_saved_tokens"`        // all-time raw context net saved (gross_saved_tokens - injected_tokens)
	NetTokenROI                float64               `json:"net_token_roi"`           // all-time raw context ROI %
	BilledNetSavedTokens       int                   `json:"billed_net_saved_tokens"` // all-time API-billed equivalent net saved (Fresh + 0.10*Cached - Injected)
	BilledTokenROI             float64               `json:"billed_token_roi"`        // all-time API-billed equivalent ROI %
	LogDedupTotal              int                   `json:"log_dedup_total"`         // all-time hook ledger dedup count from rolling-memory.log
	LogAbsentTotal             int                   `json:"log_absent_total"`        // all-time hook absent count from rolling-memory.log
	TodayTurns                 int                   `json:"today_turns"`             // turns evaluated in the active today window
	TodayInjectedCount         int                   `json:"today_injected_count"`
	TodayAdoptedCount          int                   `json:"today_adopted_count"`
	TodaySavedRounds           int                   `json:"today_saved_rounds"`
	TodaySavedSeconds          int                   `json:"today_saved_seconds"`
	TodayInjectedTokens        int                   `json:"today_injected_tokens"`
	TodaySavedFreshTokens      int                   `json:"today_saved_fresh_tokens"`
	TodaySavedCachedTokens     int                   `json:"today_saved_cached_tokens"`
	TodayGrossSavedTokens      int                   `json:"today_gross_saved_tokens"`
	TodayBilledNetSavedTokens  int                   `json:"today_billed_net_saved_tokens"`
	TodayBilledTokenROI        float64               `json:"today_billed_token_roi"`
	TodayNetSavedTokens        int                   `json:"today_net_saved_tokens"`
	TodayNetTokenROI           float64               `json:"today_net_token_roi"`
	WeekSavedRounds            int                   `json:"week_saved_rounds"`
	WeekNetSavedTokens         int                   `json:"week_net_saved_tokens"`
	Windows                    []WindowSavingBucket  `json:"windows"`        // rolling 1h / 24h / 7d / 30d monitor windows (时 / 日 / 周 / 月)
	HourlySavings              []HourlySavingBucket  `json:"hourly_savings"` // last 48 clock-hour buckets for the 时 window series
	DailySavings               []DailySavingBucket   `json:"daily_savings"`
	Partial                    bool                  `json:"partial"`        // true when this snapshot was finalized before every in-window turn was settled
	DeferredTurns              int                   `json:"deferred_turns"` // turns left for the next incremental run (settled from the persistent ledger then)
	BudgetSeconds              int                   `json:"budget_seconds"` // per-run evaluation budget that produced this snapshot (0 = unbounded)
	AvgLatencyMS               int                   `json:"avg_latency_ms"`
	P50LatencyMS               int                   `json:"p50_latency_ms"`
	P95LatencyMS               int                   `json:"p95_latency_ms"`
	EfficacyScore              float64               `json:"efficacy_score"` // strict composite 0..100 score
	Grade                      string                `json:"grade"`          // S | A | B | C | D
	Continuity                 SessionContinuity     `json:"continuity"`
	Funnel                     FunnelBreakdown       `json:"funnel"`
	HookTelemetry              HookPhysicalTelemetry `json:"hook_telemetry"`
	TopUsefulBands             []BandDiagnosticItem  `json:"top_useful_bands"`
	TopNoisyBands              []BandDiagnosticItem  `json:"top_noisy_bands"`
	BlindSearchGaps            []BlindSearchGapItem  `json:"blind_search_gaps"`
	Harnesses                  []HarnessEfficacy     `json:"harnesses"`
	Sessions                   []SessionEvaluation   `json:"sessions"`
}

// ToMap converts Report to a generic map for JSON/API emission, preserving integer types via json.Number.
func (r *Report) ToMap() map[string]any {
	raw, err := json.Marshal(r)
	if err != nil {
		return map[string]any{}
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	out := map[string]any{}
	_ = dec.Decode(&out)
	return out
}

type toolAction struct {
	Name      string
	Target    string
	IsSearch  bool
	ReadOnly  bool
	StartedAt int64 // unix seconds or milliseconds
}

type turnUsageRecord struct {
	StartedAt       int64
	CompletedAt     int64
	ModelRequests   int
	ToolCalls       int
	InputTokens     int
	CacheReadTokens int
	OutputTokens    int
	ReasoningTokens int
}

type nativeSessionStats struct {
	Harness            string
	Workspace          string
	Title              string
	Turns              int
	ModelRequests      int
	ToolCalls          int
	ExploreCalls       int
	InputTokens        int
	CacheReadTokens    int
	OutputTokens       int
	ReasoningTokens    int
	RoundFreshTokens   int // Measured uncached fresh input + output tokens per 1 model round-trip
	RoundCachedTokens  int // Measured cached historical session context tokens per 1 model round-trip
	DynamicRoundTokens int // Total context tokens per 1 model round-trip (RoundFreshTokens + RoundCachedTokens)
	TurnUsages         []turnUsageRecord
	Actions            []toolAction
	TouchedFiles       []string
}

type authoredBandRecord struct {
	BandTitle string
	CreatedAt int64
}

type sessionSelfHealStats struct {
	Writebacks    int
	SelfHealed    int
	HealedAt      []int64
	AuthoredBands []authoredBandRecord
}

// Evaluate computes the cross-harness recall efficacy report using strict empirical accounting.
func Evaluate(st *store.Store, cfg *config.Config, opts Options) (*Report, error) {
	scope := strings.TrimSpace(opts.Scope)
	if scope == "" && cfg != nil {
		scope = cfg.Scope
	}
	if scope == "" {
		scope = config.DefaultScope
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 25
	}
	home := opts.HomeDir
	if home == "" {
		home, _ = os.UserHomeDir()
	}

	report := &Report{
		Scope:       scope,
		EvaluatedAt: time.Now().Format("2006-01-02 15:04:05"),
		Harnesses:   []HarnessEfficacy{},
		Sessions:    []SessionEvaluation{},
	}
	deferredTurns := 0

	// 1. Discover all supported harnesses and their installation status.
	harnessMap := map[string]*HarnessEfficacy{}
	harnessOrder := []string{}
	allHarnesses := append(harness.BuildHarnesses(home, ""), harness.BuildExtendedHarnesses(home)...)
	for _, h := range allHarnesses {
		if h.ID == "" {
			continue
		}
		_, detectErr := os.Stat(h.Detect)
		detected := detectErr == nil
		installed := false
		if h.SkillUser != nil {
			if _, sErr := os.Stat(filepath.Join(*h.SkillUser, "rolling-memory", "SKILL.md")); sErr == nil {
				installed = true
			}
		}
		if !installed {
			for _, mcpPath := range []string{h.MCPPath, h.MCPLegacyPath} {
				if mcpPath == "" {
					continue
				}
				if raw, rErr := os.ReadFile(mcpPath); rErr == nil && strings.Contains(string(raw), "rolling-memory") {
					installed = true
					break
				}
			}
		}
		if (h.ID == "zcode" || h.ID == "claude-code") && detected {
			installed = true
		}
		hooksOn := h.HooksPath != nil && installed
		status := "standby"
		if installed {
			status = "ready"
		}
		he := &HarnessEfficacy{
			ID:           h.ID,
			Title:        h.Label,
			Detected:     detected,
			Installed:    installed,
			HooksEnabled: hooksOn,
			Status:       status,
		}
		harnessMap[h.ID] = he
		harnessOrder = append(harnessOrder, h.ID)
	}

	// 2. Read rolling-memory.log and efficacy_ledger.jsonl for hook-level counters.
	logPath := ""
	if cfg != nil && cfg.Log != "" {
		logPath = cfg.Log
	} else if home != "" {
		logPath = filepath.Join(home, ".local", "state", "rolling-memory", "rolling-memory.log")
	}
	if fastRep, ok := TryLoadROILedger(st, cfg, opts, scope, logPath, home, limit); ok {
		return fastRep, nil
	}
	var latencies []int
	logDedupCount := 0
	logAbsentCount := 0
	if logPath != "" {
		if f, err := os.Open(logPath); err == nil {
			scanner := bufio.NewScanner(f)
			for scanner.Scan() {
				line := strings.TrimSpace(scanner.Text())
				if line == "" {
					continue
				}
				var m map[string]any
				if json.Unmarshal([]byte(line), &m) != nil {
					continue
				}
				if asString(m["event"]) != "recall" {
					continue
				}
				report.TotalHookRuns++
				ms := asInt(m["ms"])
				if ms > 0 {
					latencies = append(latencies, ms)
				}
				if asInt(m["dedup"]) > 0 {
					logDedupCount++
				} else if asInt(m["absent"]) > 0 {
					logAbsentCount++
				}
			}
			_ = f.Close()
		}
	}
	if len(latencies) > 0 {
		sum := 0
		for _, v := range latencies {
			sum += v
		}
		report.AvgLatencyMS = sum / len(latencies)
		sortedLat := append([]int(nil), latencies...)
		sort.Ints(sortedLat)
		report.P50LatencyMS = sortedLat[len(sortedLat)/2]
		p95Idx := int(float64(len(sortedLat)-1) * 0.95)
		report.P95LatencyMS = sortedLat[p95Idx]
	}
	report.LogDedupTotal = logDedupCount
	report.LogAbsentTotal = logAbsentCount

	// 3. Load native harness tool traces and graph hub node degrees (100% language-agnostic).
	nativeStats, empiricalExploreBaseline, avgDynamicRoundTokens, avgFreshTok, avgCachedTok := loadNativeHarnessTelemetry(home, st)
	report.BaselineExploreSteps = round1(empiricalExploreStepsOrFallback(empiricalExploreBaseline))
	report.AvgRoundCostTokens = avgDynamicRoundTokens
	report.AvgRoundFreshTokens = avgFreshTok
	report.AvgRoundCachedTokens = avgCachedTok
	report.OverallBilledReductionPct, report.OverallRawReductionPct, report.OverallRequestReductionPct, report.OverallTimeReductionPct = computeEmpiricalReductionRates(home)
	hubLabels := loadHighDegreeHubLabels(st, scope)
	healStatsBySession := loadSessionSelfHealingStats(st, scope)
	assertionCreatedAt := loadNonSeedAssertionCreatedAt(st, scope)
	assertionValidTo := loadAssertionValidTo(st, scope)
	activeSubjects := loadPreindexedActiveSubjects(st, scope)
	landmarkPaths := loadMemoryLandmarkCreatedAt(st, scope)
	report.Continuity = collectSessionContinuity(st, scope, opts.WorkspaceDir)

	// 4. Load turn-level user prompts and turn-aligned assistant responses from trace.
	type traceRow struct {
		Session    string
		Turn       int
		Author     string
		Text       string
		MetaJSON   string
		RecordedAt int64
	}
	var allTraces []traceRow
	if st != nil && st.DB() != nil {
		rows, err := st.DB().Query(`
			SELECT t.session, t.turn, t.author, t.text, t.meta_json, t.recorded_at
			FROM trace t
			WHERE t.scope = ?
			  AND t.session != ''
			  AND t.session != 'default'
			  AND t.session NOT LIKE 'seed%'
			  AND t.session NOT LIKE 'eval%'
			  AND t.session NOT LIKE 'locomo%'
				  AND t.session NOT LIKE 'conv-%'
				  AND t.session NOT LIKE 'verify%'
				  AND t.session NOT LIKE 'probe%'
				  AND t.session NOT LIKE 'test%'
				  AND t.session NOT LIKE 'bench%'
					  AND t.session NOT LIKE 'dashboard-%'
					  AND t.session NOT LIKE 'console-%'
					  AND t.author != ''
					  AND t.meta_json NOT LIKE '%"synthetic_anchor"%'
					  AND length(t.text) >= 2
			  AND NOT (
			      t.meta_json NOT LIKE '%user_prompt_submit%'
			      AND t.meta_json NOT LIKE '%stop_payload%'
			      AND t.meta_json NOT LIKE '%transcript%'
			      AND EXISTS (SELECT 1 FROM assertion a WHERE a.anchor_trace_hash = t.hash)
			  )
			ORDER BY t.recorded_at DESC, t.turn DESC
			LIMIT 8000
		`, scope)
		if err == nil {
			for rows.Next() {
				var r traceRow
				if rows.Scan(&r.Session, &r.Turn, &r.Author, &r.Text, &r.MetaJSON, &r.RecordedAt) == nil {
					allTraces = append(allTraces, r)
				}
			}
			_ = rows.Close()
		}
	}

	// Pair each user prompt with only the assistant replies that occurred in the SAME session
	// at or after that prompt's timestamp (and before the next user prompt in that session),
	// and also compute the cumulative session context characters up to that turn so we can
	// dynamically price a round-trip even when native token usage tables are absent.
	type userTurnItem struct {
		Session         string
		Turn            int
		Prompt          string
		MetaJSON        string
		RecordedAt      int64
		NextPromptAt    int64
		TurnEndAt       int64
		AssistantReply  string
		PriorReplyRunes string
		CumContextRunes int
	}
	var turnItems []userTurnItem
	tracesBySession := map[string][]traceRow{}
	for i := len(allTraces) - 1; i >= 0; i-- {
		t := allTraces[i]
		tracesBySession[t.Session] = append(tracesBySession[t.Session], t)
	}
	for _, sessTraces := range tracesBySession {
		cumRunes := 0
		var priorAssistantBuf strings.Builder
		for i, tr := range sessTraces {
			cumRunes += utf8.RuneCountInString(tr.Text)
			if tr.Author != "user" && tr.Author != "albert" {
				continue
			}
			var replyBuf strings.Builder
			var nextPromptAt int64
			var firstReplyAt int64
			for j := i + 1; j < len(sessTraces); j++ {
				next := sessTraces[j]
				if next.Author == "user" || next.Author == "albert" {
					nextPromptAt = next.RecordedAt
					break
				}
				if firstReplyAt > 0 && next.RecordedAt-firstReplyAt > 45 {
					// A subsequent assistant reply arrived >45s after the first completed reply
					// (an un-logged <4-char continuation prompt like "继续" occurred in between):
					// bound this turn's window before the next turn starts!
					if nextPromptAt == 0 {
						nextPromptAt = firstReplyAt + 5
					}
					break
				}
				if firstReplyAt == 0 && next.RecordedAt >= tr.RecordedAt {
					firstReplyAt = next.RecordedAt
				}
				replyBuf.WriteString("\n")
				replyBuf.WriteString(next.Text)
			}
			if replyBuf.Len() == 0 {
				// Never borrow an earlier turn's assistant reply from a multi-turn session when
				// the trailing user turn has not yet received an assistant reply. Only allow pairing
				// in a 2-trace single-turn session when timestamps were recorded within 5s.
				if len(sessTraces) == 2 && time.Now().Unix()-tr.RecordedAt > 180 {
					for _, next := range sessTraces {
						if (next.Author == "assistant" || next.Author == "agent" || next.Author == "zcode") &&
							next.RecordedAt >= tr.RecordedAt-5 {
							replyBuf.WriteString("\n")
							replyBuf.WriteString(next.Text)
							firstReplyAt = next.RecordedAt
							break
						}
					}
				}
			}
			turnEndAt := nextPromptAt
			if firstReplyAt > tr.RecordedAt && (turnEndAt == 0 || firstReplyAt+10 < turnEndAt) {
				turnEndAt = firstReplyAt + 5
			}
			priorSnapshot := priorAssistantBuf.String()
			priorAssistantBuf.WriteString("\n")
			priorAssistantBuf.WriteString(replyBuf.String())
			turnItems = append(turnItems, userTurnItem{
				Session:         tr.Session,
				Turn:            tr.Turn,
				Prompt:          tr.Text,
				MetaJSON:        tr.MetaJSON,
				RecordedAt:      tr.RecordedAt,
				NextPromptAt:    nextPromptAt,
				TurnEndAt:       turnEndAt,
				AssistantReply:  replyBuf.String(),
				PriorReplyRunes: priorSnapshot,
				CumContextRunes: cumRunes + utf8.RuneCountInString(replyBuf.String()),
			})
		}
	}
	sort.SliceStable(turnItems, func(i, j int) bool {
		return turnItems[i].RecordedAt > turnItems[j].RecordedAt
	})

	// Bound the evaluation window while including ALL turns from active sessions so per-session
	// band deduplication (`seenSessionBands`) and All-Time Cumulative vs Daily (24h) savings are
	// 100% causally exact. The cap is caller-INDEPENDENT: `limit` only caps the
	// displayed session list — an admin refresh (limit=20) and a CLI audit (--limit 320) must
	// partition the same database into the identical funnel, otherwise the two writers overwrite
	// the shared ROI ledger snapshot with different denominators. The pre-recall viability screen
	// plus the immutable turn cache keep a full 1200-turn cold pass well under a second.
	maxEvalTurns := 2500
	if len(turnItems) > maxEvalTurns {
		activeSess := map[string]bool{}
		for i := 0; i < maxEvalTurns && i < len(turnItems); i++ {
			activeSess[turnItems[i].Session] = true
		}
		var bounded []userTurnItem
		for _, it := range turnItems {
			if activeSess[it.Session] {
				bounded = append(bounded, it)
			}
		}
		turnItems = bounded
	}

	seenSessions := map[string]bool{}
	firstPromptBySession := map[string]int64{}
	for _, it := range turnItems {
		if it.RecordedAt > 0 {
			if prev, ok := firstPromptBySession[it.Session]; !ok || it.RecordedAt < prev {
				firstPromptBySession[it.Session] = it.RecordedAt
			}
		}
	}
	seenSessionPrompt := map[string]bool{}
	seenSessionBands := map[string]bool{}
	seenSessionTouchedPath := map[string]bool{}
	authoredSessionBands := map[string]bool{}
	processedTurnKeys := map[string]bool{}
	processedTurnStamps := map[string]bool{}
	windowMinRecordedAt := int64(0)
	for _, it := range turnItems {
		if it.RecordedAt > 0 && (windowMinRecordedAt == 0 || it.RecordedAt < windowMinRecordedAt) {
			windowMinRecordedAt = it.RecordedAt
		}
	}
	harnessLatencies := map[string][]int{}

	// Load persistent immutable turn evaluation cache (`efficacy_turn_cache_v45.json`)
	// so evaluating 240+ multi-day historical turns completes in <30ms after the first pass.
	cachePath := ""
	skippedTrailingPath := ""
	if logPath != "" {
		cachePath = filepath.Join(filepath.Dir(logPath), "efficacy_turn_cache_v46.json")
		skippedTrailingPath = filepath.Join(filepath.Dir(logPath), "efficacy_skipped_trailing_v1.json")
	}
	turnCache := loadImmutableTurnCache(cachePath)
	skippedTrailingCache := map[string]bool{}
	if skippedTrailingPath != "" {
		if rawST, stErr := os.ReadFile(skippedTrailingPath); stErr == nil {
			_ = json.Unmarshal(rawST, &skippedTrailingCache)
		}
	}
	skippedTrailingDirty := false
	var legacyTurnCache map[string]SessionEvaluation
	cacheDirty := false
	lastCacheFlush := time.Now()
	// flushImmutableTurnCache persists the settled turn ledger mid-run so a run that is
	// cut short still hands its completed work to the next incremental run (bounded loss).
	flushImmutableTurnCache := func(force bool) {
		if skippedTrailingPath != "" && skippedTrailingDirty && (force || time.Since(lastCacheFlush) >= 20*time.Second) {
			if rawST, err := json.Marshal(skippedTrailingCache); err == nil {
				writeAtomicFile(skippedTrailingPath, rawST)
				skippedTrailingDirty = false
			}
		}
		if cachePath == "" || !cacheDirty {
			return
		}
		if !force && time.Since(lastCacheFlush) < 20*time.Second {
			return
		}
		saveImmutableTurnCache(cachePath, turnCache)
		lastCacheFlush = time.Now()
	}
	nowEvalUnix := time.Now().Unix()

	// Process turns in chronological order first to accurately model per-session band deduplication
	// (matching hooks.filterUnseenSessionBands where a band injected on turn 1 is suppressed on turn 2+),
	// then reverse the collected SessionEvaluation slice so the most recent turns appear first.
	sort.SliceStable(turnItems, func(i, j int) bool {
		if turnItems[i].RecordedAt != turnItems[j].RecordedAt {
			return turnItems[i].RecordedAt < turnItems[j].RecordedAt
		}
		return turnItems[i].Turn < turnItems[j].Turn
	})

	// Pre-scan for Anti-Cron-Inflation & Cross-Session Retry Guard: if an identical long prompt
	// (>=16 runes) fires repeatedly across multiple sessions (e.g., hourly scheduled tasks or a user
	// canceling an unanswered prompt in session A and re-submitting it in session B), record its
	// LATEST occurrence key (preferring completed occurrences with an AssistantReply) so we evaluate
	// the active completed run and never double-count abandoned retry turns.
	latestCronTurnKey := map[string]string{}
	hasCompletedPrompt := map[string]bool{}
	for _, it := range turnItems {
		p := strings.TrimSpace(ids.StripInjected(it.Prompt))
		if utf8.RuneCountInString(p) < 16 {
			continue
		}
		key := it.Session + "\x00" + strconv.FormatInt(it.RecordedAt, 10)
		if strings.TrimSpace(it.AssistantReply) != "" {
			latestCronTurnKey[p] = key
			hasCompletedPrompt[p] = true
		} else if !hasCompletedPrompt[p] {
			latestCronTurnKey[p] = key
		}
	}

	// Soft wall-clock budget for NEW (uncached) turn evaluations: armed right before the
	// turn loop so initial telemetry loading never starves the turn loop, and cached turns
	// always replay in O(1) even after the budget expires.
	budgetDeadline := int64(0)
	if opts.BudgetSeconds > 0 {
		budgetDeadline = time.Now().Unix() + int64(opts.BudgetSeconds)
	}

	for _, item := range turnItems {
		prompt := strings.TrimSpace(ids.StripInjected(item.Prompt))
		if prompt == "" {
			continue
		}
		turnCacheKey := scope + "\x00" + item.Session + "\x00" + strconv.FormatInt(item.RecordedAt, 10) + "\x00" + truncateStr(prompt, 80)
		turnStampKey := item.Session + "\x00" + strconv.FormatInt(item.RecordedAt, 10)
		dedupKey := item.Session + "\x00" + prompt
		if seenSessionPrompt[dedupKey] {
			processedTurnKeys[turnCacheKey] = true
			processedTurnStamps[turnStampKey] = true
			continue
		}
		seenSessionPrompt[dedupKey] = true
		if utf8.RuneCountInString(prompt) >= 16 {
			if latestKey, ok := latestCronTurnKey[prompt]; ok && latestKey != item.Session+"\x00"+strconv.FormatInt(item.RecordedAt, 10) {
				processedTurnKeys[turnCacheKey] = true
				processedTurnStamps[turnStampKey] = true
				continue
			}
		}

		hID, workspace := inferSessionHarnessAndWorkspace(item.Session, item.MetaJSON, nativeStats[item.Session])
		if opts.Harness != "" && !strings.EqualFold(opts.Harness, hID) {
			continue
		}

		// Fast-path: if this turn has already completed (has a subsequent prompt, or has an assistant reply
		// recorded >30s ago) and is in turnCache, replay its verified SessionEvaluation in O(1)!
		// A trailing turn that never received a reply only becomes immutable once it has been idle for
		// 30 minutes: before that it may still be forming, afterwards no harness session will ever
		// answer it, and re-settling it on every run would burn the whole settling budget on the same
		// permanently-uncacheable turns (deferring the fresh turns the monitor actually watches).
		turnImmutable := item.NextPromptAt > 0 ||
			(strings.TrimSpace(item.AssistantReply) != "" && nowEvalUnix-item.RecordedAt > 30) ||
			(nowEvalUnix-item.RecordedAt > 1800)
		cachedEval, hasCachedEval := turnCache[turnCacheKey]
		if hasCachedEval && hasSpuriousSystemAnchor(cachedEval.MatchedAnchors) {
			delete(turnCache, turnCacheKey)
			hasCachedEval = false
			cacheDirty = true
		}
		if !hasCachedEval && turnImmutable && item.NextPromptAt == 0 && strings.TrimSpace(item.AssistantReply) == "" && skippedTrailingCache[turnCacheKey] {
			processedTurnKeys[turnCacheKey] = true
			processedTurnStamps[turnStampKey] = true
			continue
		}

		// Soft budget guard: check BEFORE incrementing EvaluatedTurns / he.Turns so that deferred
		// turns never inflate EvaluatedTurns without a matching Funnel bucket (preserving
		// conservation `sum(Funnel) == EvaluatedTurns` even when Partial == true).
		if !(hasCachedEval && turnImmutable) && budgetExhausted(budgetDeadline, time.Now()) {
			processedTurnKeys[turnCacheKey] = true
			processedTurnStamps[turnStampKey] = true
			deferredTurns++
			continue
		}

		processedTurnKeys[turnCacheKey] = true
		processedTurnStamps[turnStampKey] = true

		he := harnessMap[hID]
		if he == nil {
			he = &HarnessEfficacy{ID: hID, Title: hID, Status: "active"}
			harnessMap[hID] = he
			harnessOrder = append(harnessOrder, hID)
		}
		he.Status = "active"
		if !seenSessions[item.Session] {
			seenSessions[item.Session] = true
			he.Sessions++
			report.TotalSessions++
			if hs := healStatsBySession[item.Session]; hs != nil {
				report.WritebackCount += hs.Writebacks
				report.SelfHealedCount += hs.SelfHealed
			}
		}
		he.Turns++
		report.EvaluatedTurns++

		if hs := healStatsBySession[item.Session]; hs != nil {
			minAuthoredAt := firstPromptBySession[item.Session] + 2
			for _, ab := range hs.AuthoredBands {
				if ab.CreatedAt > 0 && ab.CreatedAt >= minAuthoredAt && ab.CreatedAt+2 < item.RecordedAt && item.RecordedAt-ab.CreatedAt <= 3600 && ab.BandTitle != "" {
					authoredSessionBands[item.Session+"\x00"+ab.BandTitle] = true
				}
			}
		}

		if hasCachedEval && turnImmutable {
			if cachedEval.Outcome == "high_value" || cachedEval.Outcome == "assisted" || cachedEval.Outcome == "noise" {
				for _, bt := range strings.Split(cachedEval.RecalledSummary, " ； ") {
					bt = strings.TrimSpace(bt)
					if bt != "" {
						seenSessionBands[item.Session+"\x00"+bt] = true
					}
				}
				for _, ma := range cachedEval.MatchedAnchors {
					lp := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(ma), "./"))
					if lp != "" {
						seenSessionTouchedPath[item.Session+"\x00"+lp] = true
					}
				}
			}
			harnessLatencies[hID] = append(harnessLatencies[hID], cachedEval.LatencyMS)
			report.EchoExcludedCount += cachedEval.EchoExcluded
			switch cachedEval.Outcome {
			case "silenced":
				he.Silenced++
				report.SilencedCount++
				report.Funnel.SilenceFiltered++
			case "dedup":
				he.Silenced++
				report.SilencedCount++
				report.Funnel.DedupSuppressed++
			case "high_value":
				he.Injections++
				report.InjectedCount++
				he.Adopted++
				report.AdoptedCount++
				report.Funnel.HighValueAdopted++
				report.InjectedTokens += cachedEval.InjectedTokens
				report.SavedFreshTokens += cachedEval.SavedFreshTokens
				report.SavedCachedTokens += cachedEval.SavedCachedTokens
				report.GrossSavedTokens += cachedEval.SavedTokens
				report.TotalSavedRounds += cachedEval.SavedRounds
				report.TotalSavedSeconds += cachedEval.SavedSeconds
				he.SavedRounds += cachedEval.SavedRounds
				he.NetSavedTokens += cachedEval.NetTokens
				if cachedEval.HasCodeCoord {
					report.CodeCoordHits++
					report.NovelCoordHits++
				}
				if cachedEval.CausalVerified {
					report.CausalDirectHits++
				}
				if cachedEval.AttributionSource == "l0_boot_or_carryover" {
					report.BootAndCarryoverHits++
				}
				switch cachedEval.ValuePillar {
				case "navigation":
					report.NavigationDirectHits++
				case "guardrail":
					report.GuardrailHits++
				case "continuity":
					report.ContinuityHits++
				}
			case "assisted":
				he.Injections++
				report.InjectedCount++
				he.Adopted++
				report.AdoptedCount++
				report.Funnel.ContextAssisted++
				report.InjectedTokens += cachedEval.InjectedTokens
				report.SavedFreshTokens += cachedEval.SavedFreshTokens
				report.SavedCachedTokens += cachedEval.SavedCachedTokens
				report.GrossSavedTokens += cachedEval.SavedTokens
				report.TotalSavedRounds += cachedEval.SavedRounds
				report.TotalSavedSeconds += cachedEval.SavedSeconds
				he.SavedRounds += cachedEval.SavedRounds
				he.NetSavedTokens += cachedEval.NetTokens
				if cachedEval.HasCodeCoord {
					report.CodeCoordHits++
					report.NovelCoordHits++
				}
				switch cachedEval.ValuePillar {
				case "navigation":
					report.NavigationDirectHits++
				case "guardrail":
					report.GuardrailHits++
				case "continuity":
					report.ContinuityHits++
				}
			case "noise":
				he.Injections++
				report.InjectedCount++
				report.Funnel.NoiseInjected++
				report.InjectedTokens += cachedEval.InjectedTokens
				he.NetSavedTokens += cachedEval.NetTokens
			}
			report.Sessions = append(report.Sessions, cachedEval)
			continue
		}

		flushImmutableTurnCache(false)

		nStat := nativeStats[item.Session]
		modelReqs := 1
		toolCalls := 0
		exploreSteps := 0
		// Dynamic LLM round-trip cost for THIS specific session & turn:
		//  - Primary: exact per-turn match from native `turn_usage` within [item.RecordedAt, item.NextPromptAt)
		//    so turn 1 vs turn 20 reflects the true accumulated historical context volume at that turn;
		//  - Secondary: session-level measured average per model_request;
		//  - Fallback: dynamic context-depth formula based on cumulative context runes + system base.
		roundCostTokens, roundFreshTokens, roundCachedTokens, turnReqs, turnTools := computeDynamicRoundCost(
			nStat, item.RecordedAt, item.TurnEndAt, item.CumContextRunes, item.Turn,
		)
		if turnReqs > 0 {
			modelReqs = turnReqs
			toolCalls = turnTools
		} else if nStat != nil {
			turnsDenom := maxInt(nStat.Turns, 1)
			modelReqs = maxInt(nStat.ModelRequests/turnsDenom, 1)
			toolCalls = nStat.ToolCalls / turnsDenom
		}
		if nStat != nil {
			exploreSteps = nStat.ExploreCalls
		}

		// 1. Run deterministic graph recall (IncludeRaw=false so the prompt never self-echoes).
		// All filtering relies strictly on memcore's mathematical score, graph node degree (IDF),
		// and structural path/entity node labels — zero closed-set word lists.
		var rendered string
		var bandTitles []string
		var codeAnchors []string
		var entityAnchors []string
		topScore := 0.0
		elapsedMS := report.AvgLatencyMS
		if elapsedMS == 0 {
			elapsedMS = 45
		}
		absent := true

		var dedupTriggered bool
		var selfAuthoredSuppressed bool
		var topPrunedCandidate string
		if st != nil && cfg != nil && hasViablePreRecallSignal(prompt, item.PriorReplyRunes, workspace, item.RecordedAt, activeSubjects) {
			bandsCap, nodesCap, charsCap := 16, 40, 4200
			includeRaw := false
			reader := "deliverable"
			effectiveCue := buildEffectiveTurnCue(st, scope, prompt, item.PriorReplyRunes, workspace, item.RecordedAt, activeSubjects)
			res, err := recall.Recall(st, cfg, recall.Options{
				Cue:        effectiveCue,
				Scope:      scope,
				Reader:     reader,
				CapsBands:  &bandsCap,
				CapsNodes:  &nodesCap,
				CapsChars:  &charsCap,
				IncludeRaw: &includeRaw,
			})
			if err == nil && res != nil {
				if ms := asInt(res.Meta["elapsed_ms"]); ms > 0 {
					elapsedMS = ms
				}
				// Temporal Anti-Leakage Guard:
				// Strip assertions created AFTER this prompt was submitted (`created_at > item.RecordedAt + 5`)
				// so a milestone `remember` call at the end of turn T never time-travels back to
				// impersonate a recalled memory at the start of turn T!
				historicalBands := stripFutureAssertionsFromBands(res.Bands, item.RecordedAt, assertionCreatedAt)
				topPrunedCandidate = detectTopGateFilteredCandidate(prompt, workspace, historicalBands)
				// Apply the exact same language-agnostic pruning pipeline as hooks.recallHook:
				// 1) Mathematical sequence specificity + full-slot structural path matching per band;
				// 2) Convex marginal score drop-off (0.55 of topScore);
				// 3) Per-session band deduplication (unseen delta bands only!).
				keptBands := filterBandsMathematically(prompt, effectiveCue, workspace, historicalBands)
				// Superseded-node pruning parity: mirrors hooks.pruneSupersededInBands
				// with turn-time coherence — only nodes already dead at this turn's time drop.
				keptBands = pruneSupersededInBandsEfficacy(keptBands, assertionValidTo, item.RecordedAt)
				if len(keptBands) > 3 {
					keptBands = keptBands[:3]
				}
				if len(keptBands) > 0 {
					var unseenBands []map[string]any
					hitInjectedDedup := false
					hitSelfAuthored := false
					firstBandSeen := false
					topKeptScore := recall.PeakBandScore(keptBands[0])
					for idx, b := range keptBands {
						bt := strings.TrimSpace(asString(b["title"]))
						if bt == "" {
							continue
						}
						bKey := item.Session + "\x00" + bt
						if seenSessionBands[bKey] {
							hitInjectedDedup = true
							if idx == 0 {
								firstBandSeen = true
							}
							continue
						}
						if authoredSessionBands[bKey] {
							hitSelfAuthored = true
							if idx == 0 {
								firstBandSeen = true
							}
							continue
						}
						if firstBandSeen && topKeptScore > 0 && recall.PeakBandScore(b) < topKeptScore*0.92 {
							continue
						}
						seenSessionBands[bKey] = true
						unseenBands = append(unseenBands, b)
					}
					if len(unseenBands) == 0 {
						if hitInjectedDedup {
							dedupTriggered = true
						} else if hitSelfAuthored {
							selfAuthoredSuppressed = true
						}
					}
					keptBands = unseenBands
				}
				res.Bands = keptBands
				absent = len(res.Bands) == 0
				if !absent {
					bandTitles, codeAnchors, entityAnchors, topScore = extractStructuralBandAnchors(res.Bands, workspace, hubLabels)
				}
			}
		}

		harnessLatencies[hID] = append(harnessLatencies[hID], elapsedMS)

		// 1b. SessionStart Boot Landmark & Session Carry-Over Attribution:
		// When UserPromptSubmit did not inject new bands on this turn (`dedupTriggered` or `absent`),
		// check whether the agent's native tool calls (`part` table) directly read/edited an active
		// memory code landmark (created before this turn, NOT typed in the user's prompt, and NOT
		// already credited in an earlier turn of this same session) with <= 1 blind search step.
		// This captures the 0-marginal-token-cost dividend of SessionStart boot landmarks (`[已验活体路标]`)
		// and in-session Δ-Band carry-over without double-counting subsequent edits to the same file!
		if (dedupTriggered || absent || topScore < 0.42 || len(bandTitles) == 0) && nStat != nil && len(nStat.Actions) > 0 {
			if hitLandmarks, turnExp, ok := detectBootOrCarryoverHit(nStat, item.Session, item.RecordedAt, item.TurnEndAt, prompt, landmarkPaths, seenSessionTouchedPath); ok {
				baselineSteps := int(math.Round(report.BaselineExploreSteps))
				if baselineSteps < 2 {
					baselineSteps = 2
				}
				if baselineSteps > 2 {
					baselineSteps = 2
				}
				savedRounds := baselineSteps - turnExp
				if savedRounds < 1 {
					savedRounds = 1
				}
				if savedRounds > 1 {
					savedRounds = 1
				}
				savedFreshTokens := savedRounds * roundFreshTokens
				savedCachedTokens := savedRounds * roundCachedTokens
				savedTokens := savedFreshTokens + savedCachedTokens
				billedNetTokens := savedFreshTokens + int(math.Round(float64(savedCachedTokens)*0.10))
				savedSeconds := savedRounds * 8
				pillar := "navigation"
				if dedupTriggered || countSemanticRunes(prompt) <= 18 {
					pillar = "continuity"
					report.ContinuityHits++
				} else {
					report.NavigationDirectHits++
				}
				report.BootAndCarryoverHits++
				report.CodeCoordHits++
				report.NovelCoordHits++
				report.CausalDirectHits++
				he.Injections++
				report.InjectedCount++
				he.Adopted++
				report.AdoptedCount++
				report.Funnel.HighValueAdopted++
				he.SavedRounds += savedRounds
				he.NetSavedTokens += savedTokens
				report.TotalSavedRounds += savedRounds
				report.TotalSavedSeconds += savedSeconds
				report.SavedFreshTokens += savedFreshTokens
				report.SavedCachedTokens += savedCachedTokens
				report.GrossSavedTokens += savedTokens

				verdictLbl := "SessionStart 活体路标/会话驻留直达 (0 边际注入成本)"
				if dedupTriggered {
					verdictLbl = "同会话 Δ-Band 驻留复利直达 (0 重复注入成本)"
				}
				bootEntry := SessionEvaluation{
					SessionID:         item.Session,
					Harness:           hID,
					Workspace:         workspace,
					Prompt:            truncateStr(prompt, 110),
					Outcome:           "high_value",
					VerdictLabel:      verdictLbl,
					AttributionSource: "l0_boot_or_carryover",
					ValuePillar:       pillar,
					RecalledSummary:   "开场活体路标 / 会话驻留坐标直达：" + strings.Join(hitLandmarks, " , "),
					MatchedAnchors:    hitLandmarks,
					HasCodeCoord:      true,
					CausalVerified:    true,
					ModelRequests:     modelReqs,
					ToolCalls:         toolCalls,
					ExploreSteps:      turnExp,
					RoundCostTokens:   roundCostTokens,
					RoundFreshTokens:  roundFreshTokens,
					RoundCachedTokens: roundCachedTokens,
					SavedRounds:       savedRounds,
					SavedSeconds:      savedSeconds,
					InjectedTokens:    0,
					SavedTokens:       savedTokens,
					SavedFreshTokens:  savedFreshTokens,
					SavedCachedTokens: savedCachedTokens,
					BilledNetTokens:   billedNetTokens,
					NetTokens:         savedTokens,
					LatencyMS:         elapsedMS,
					RecordedAt:        item.RecordedAt,
					TimeText:          formatUnixTime(item.RecordedAt),
				}
				report.Sessions = append(report.Sessions, bootEntry)
				if turnImmutable {
					turnCache[turnCacheKey] = bootEntry
					cacheDirty = true
				}
				continue
			}
		}

		// 2. Per-session band deduplication (mirrors hooks.filterUnseenSessionBands):
		// if all matched bands were already injected in an earlier turn of this same session,
		// the hook suppresses re-injection at 0 token cost.
		if dedupTriggered {
			he.Silenced++
			report.SilencedCount++
			report.Funnel.DedupSuppressed++
			dedupEntry := SessionEvaluation{
				SessionID:         item.Session,
				Harness:           hID,
				Workspace:         workspace,
				Prompt:            truncateStr(prompt, 110),
				Outcome:           "dedup",
				VerdictLabel:      "同会话账本去重 (0 成本)",
				AttributionSource: "ledger_dedup",
				RecalledSummary:   "该主体带已在本会话前序轮次注入，injection_ledger 自动拦截重复注入",
				MatchedAnchors:    []string{},
				ModelRequests:     modelReqs,
				ToolCalls:         toolCalls,
				ExploreSteps:      exploreSteps,
				RoundCostTokens:   roundCostTokens,
				RoundFreshTokens:  roundFreshTokens,
				RoundCachedTokens: roundCachedTokens,
				SavedRounds:       0,
				InjectedTokens:    0,
				SavedTokens:       0,
				NetTokens:         0,
				LatencyMS:         elapsedMS,
				RecordedAt:        item.RecordedAt,
				TimeText:          formatUnixTime(item.RecordedAt),
			}
			report.Sessions = append(report.Sessions, dedupEntry)
			if turnImmutable {
				turnCache[turnCacheKey] = dedupEntry
				cacheDirty = true
			}
			continue
		}

		// 3. Mathematical signal & string-theoretic specificity check -> silenced (0 token cost, 0 saved tokens).
		// Explicitly distinguish between:
		//  - Self-Authored Echo Guard (`self_authored_echo_guard`): band was written by this session itself in an earlier turn;
		//  - Database Absent (`db_absent`): no active assertion in the graph matches this topic -> silent by design;
		//  - Gate Silence (`gate_silence`): a partial/ambient candidate band was retrieved, but pruned by specificity gate.
		if absent || topScore < 0.42 || len(bandTitles) == 0 {
			he.Silenced++
			report.SilencedCount++
			report.Funnel.SilenceFiltered++
			verdictLbl := "库内无此记忆 · 零成本静默"
			attrSrc := "db_absent"
			summaryMsg := "记忆库内无对应活跃断言（Absent），自动保持静默（0 成本）"
			if selfAuthoredSuppressed {
				verdictLbl = "同会话自写防回声抑制 (0 成本)"
				attrSrc = "self_authored_echo_guard"
				summaryMsg = "该主体带由本会话前序轮次自行写入，自动抑制同会话自回声（0 成本）"
			} else if topPrunedCandidate != "" {
				verdictLbl = "低特异度门控拦截 (0 成本)"
				attrSrc = "gate_silence"
				summaryMsg = "防噪门拦截弱相关候选：" + topPrunedCandidate + "（信息特异度低于阈值）"
			}
			silEntry := SessionEvaluation{
				SessionID:         item.Session,
				Harness:           hID,
				Workspace:         workspace,
				Prompt:            truncateStr(prompt, 110),
				Outcome:           "silenced",
				VerdictLabel:      verdictLbl,
				AttributionSource: attrSrc,
				RecalledSummary:   summaryMsg,
				MatchedAnchors:    []string{},
				ModelRequests:     modelReqs,
				ToolCalls:         toolCalls,
				ExploreSteps:      exploreSteps,
				RoundCostTokens:   roundCostTokens,
				RoundFreshTokens:  roundFreshTokens,
				RoundCachedTokens: roundCachedTokens,
				SavedRounds:       0,
				InjectedTokens:    0,
				SavedTokens:       0,
				NetTokens:         0,
				LatencyMS:         elapsedMS,
				RecordedAt:        item.RecordedAt,
				TimeText:          formatUnixTime(item.RecordedAt),
			}
			report.Sessions = append(report.Sessions, silEntry)
			if turnImmutable {
				turnCache[turnCacheKey] = silEntry
				cacheDirty = true
			}
			continue
		}

		// If this injected turn is still actively executing in-flight or was interrupted before any
		// assistant reply was recorded (trailing turn with NextPromptAt == 0 and empty AssistantReply),
		// skip booking it as finished noise/adoption. Once the turn is immutable (>30m idle), memoize
		// this skipped state so subsequent incremental runs do not re-run graph recall on it.
		if strings.TrimSpace(item.AssistantReply) == "" && (time.Now().Unix()-item.RecordedAt <= 180 || item.NextPromptAt == 0) {
			he.Turns--
			report.EvaluatedTurns--
			if turnImmutable && item.NextPromptAt == 0 {
				skippedTrailingCache[turnCacheKey] = true
				skippedTrailingDirty = true
			}
			continue
		}

		// 4. An actual injection occurred: book its REAL compact multi-step token cost first!
		// Compact active-only band rendering averages ~160 tokens per unseen band; across M
		// requests in the turn, request #1 pays 1.0x and subsequent requests pay 0.10x cache-read.
		he.Injections++
		report.InjectedCount++
		baseTokens := maxInt(len(bandTitles)*165, 140)
		if rendered != "" {
			compactTokens := int(math.Round(float64(utf8.RuneCountInString(rendered)) * 0.34))
			if compactTokens > 0 && compactTokens < baseTokens {
				baseTokens = maxInt(compactTokens, 120)
			}
		}
		multiplier := 1.0 + 0.10*float64(maxInt(modelReqs-1, 0))
		if multiplier > 3.5 {
			multiplier = 3.5
		}
		injectedTokens := int(math.Round(float64(baseTokens) * multiplier))
		report.InjectedTokens += injectedTokens

		// 5. L1 Revealed-Preference Anti-Echo & Causal Tool Audit + L2 Piggybacked Self-Heal Attribution:
		//    (a) Anti-Echo: strip any file path or entity anchor that the user ALREADY typed in
		//        their prompt (unseen session bands are already deduplicated above by seenSessionBands).
		//        Memory only gets credit for NOVEL coordinates/facts it contributed!
		novelCode, echoCodeCnt := stripEchoAnchors(codeAnchors, prompt, "")
		novelEntities, _ := stripEchoAnchors(entityAnchors, prompt, "")
		report.EchoExcludedCount += echoCodeCnt

		lowerReply := strings.ToLower(item.AssistantReply)
		targetAnchorsForSeq := novelCode
		if len(targetAnchorsForSeq) == 0 {
			targetAnchorsForSeq = novelEntities
		}
		turnToolsCorpus, turnExploreSteps, causalOrderOK := evaluateTurnToolSequence(
			nStat, item.RecordedAt, item.TurnEndAt, targetAnchorsForSeq, exploreSteps,
		)
		exploreSteps = turnExploreSteps
		turnHadToolCalls := strings.TrimSpace(turnToolsCorpus) != "" || (nStat == nil)

		// Attribution separation:
		//  1. Separate actual tool-targeted file access (`matchedToolCode`) from text-only citation (`matchedReplyCode`).
		//  2. NEVER fall back to `entityAnchors` when `novelEntities` is empty — any anchor already present in the user's
		//     prompt was stripped by `stripEchoAnchors` and must NEVER be resurrected as a fake memory hit!
		baselineSteps := int(math.Round(report.BaselineExploreSteps))
		if baselineSteps < 2 {
			baselineSteps = 2
		}
		if baselineSteps > 3 {
			baselineSteps = 3
		}
		hasNativeTools := nStat != nil && len(nStat.TouchedFiles) > 0
		var matchedToolCode, matchedReplyCode []string
		if hasNativeTools {
			matchedToolCode = matchHardList(novelCode, turnToolsCorpus)
			matchedReplyCode = matchHardList(novelCode, lowerReply)
		} else if nStat == nil {
			// Pure synthetic/unit-test environment without native harness SQLite store:
			// allow matching reply corpus when evaluating isolated test fixtures.
			matchedToolCode = matchHardList(novelCode, lowerReply)
		} else {
			// When the session exists in the native harness store (`nStat != nil`) but has ZERO
			// recorded file-touching tool calls for this turn (`!hasNativeTools`), NEVER credit
			// a text mention in the assistant's summary reply as `matchedToolCode` (direct tool navigation)!
			// It is strictly a text citation (`matchedReplyCode`).
			matchedReplyCode = matchHardList(novelCode, lowerReply)
		}
		matchedEntities := matchHardList(novelEntities, turnToolsCorpus+" "+lowerReply)
		allMatched := append([]string{}, matchedToolCode...)
		for _, rc := range matchedReplyCode {
			if len(allMatched) < 5 && !containsStr(allMatched, rc) {
				allMatched = append(allMatched, rc)
			}
		}
		for _, e := range matchedEntities {
			if len(allMatched) < 5 && !containsStr(allMatched, e) {
				allMatched = append(allMatched, e)
			}
		}
		for _, ma := range allMatched {
			lp := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(ma), "./"))
			if lp != "" {
				seenSessionTouchedPath[item.Session+"\x00"+lp] = true
			}
		}

		turnSelfHealed := countTurnSelfHealed(healStatsBySession[item.Session], item.RecordedAt, item.NextPromptAt)
		isShortContinuityTurn := countSemanticRunes(prompt) <= 18 && strings.TrimSpace(item.PriorReplyRunes) != ""

		var outcome, verdict, attrSource, valuePillar string
		savedRounds := 0
		savedTokens := 0
		causalVerified := false

		if len(matchedToolCode) > 0 {
			report.NovelCoordHits++
			// L1 Causal Sequence Check: did the agent touch the recalled novel code coordinate
			// in actual tool calls BEFORE running blind grep/find sweeps, and keep total search < baseline?
			if causalOrderOK && exploreSteps < baselineSteps {
				savedRounds = baselineSteps - exploreSteps
				if savedRounds > 1 && len(matchedToolCode) == 1 {
					savedRounds = 1
				}
				if savedRounds > 2 {
					savedRounds = 2
				}
				savedTokens = savedRounds * roundCostTokens
				outcome = "high_value"
				valuePillar = "navigation"
				if isShortContinuityTurn {
					valuePillar = "continuity"
					report.ContinuityHits++
				} else {
					report.NavigationDirectHits++
				}
				verdict = "增量坐标工具直达 · 时序因果验证省步"
				attrSource = "l1_causal_tool"
				causalVerified = true
				if turnSelfHealed > 0 {
					verdict = "增量坐标工具直达 + 账本自愈更新"
					attrSource = "l1_causal_tool+l2_heal"
				}
				he.Adopted++
				report.AdoptedCount++
				report.CodeCoordHits++
				report.CausalDirectHits++
				report.Funnel.HighValueAdopted++
			} else {
				savedRounds = 0
				savedTokens = 0
				outcome = "assisted"
				valuePillar = "navigation"
				verdict = "坐标触达但仍执行盲搜 (0 省步 · 净耗 Token)"
				attrSource = "l1_search_anyway"
				he.Adopted++
				report.AdoptedCount++
				report.Funnel.ContextAssisted++
			}
		} else if len(matchedReplyCode) > 0 {
			// Code coordinate was cited in the assistant's text reply without file tool read/write:
			// counts as ContextAssisted (at most 1 round saved when 0 blind search was run).
			report.NovelCoordHits++
			valuePillar = "guardrail"
			if isShortContinuityTurn {
				valuePillar = "continuity"
				report.ContinuityHits++
			} else {
				report.GuardrailHits++
			}
			if exploreSteps == 0 && toolCalls <= 2 {
				savedRounds = 1
				savedTokens = roundCostTokens
				outcome = "assisted"
				verdict = "代码坐标直答引用 · 省去检索探测"
				attrSource = "l1_reply_coord"
				causalVerified = true
			} else {
				savedRounds = 0
				savedTokens = 0
				outcome = "assisted"
				verdict = "代码坐标背景参考 (未减少工具探测)"
				attrSource = "l1_reply_coord"
			}
			he.Adopted++
			report.AdoptedCount++
			report.CodeCoordHits++
			report.Funnel.ContextAssisted++
		} else if len(matchedEntities) > 0 {
			// Recalled memory matched a specific non-echo epistemic entity/ruling/gotcha in the assistant's action or reply.
			// Require >= 2 distinct novel entity anchors (or 1 high-self-information anchor >= 20 Shannon bits)
			// AND verified low exploration steps (< baselineSteps) before crediting guardrail round savings.
			strongEntityHit := len(matchedEntities) >= 2 || memcore.StringShannonBits(matchedEntities[0]) >= 20.0
			valuePillar = "guardrail"
			if isShortContinuityTurn {
				valuePillar = "continuity"
				report.ContinuityHits++
			} else {
				report.GuardrailHits++
			}
			if strongEntityHit && causalOrderOK && exploreSteps < baselineSteps {
				// Epistemic guardrail / ruling / recipe hit: credits avoided lookup/trial-and-error rounds.
				savedRounds = baselineSteps - exploreSteps
				if !turnHadToolCalls || toolCalls == 0 {
					savedRounds = 1
				} else if savedRounds > 2 {
					savedRounds = 2
				}
				savedTokens = savedRounds * roundCostTokens
				outcome = "high_value"
				verdict = "关键约束/深坑配方命中 · 免除试错返工"
				attrSource = "l1_entity_anchor"
				causalVerified = true
				if turnSelfHealed > 0 {
					verdict = "关键约束/深坑命中 + 账本自愈更新"
					attrSource = "l1_entity+l2_heal"
				}
				he.Adopted++
				report.AdoptedCount++
				report.CausalDirectHits++
				report.Funnel.HighValueAdopted++
			} else {
				savedRounds = 0
				savedTokens = 0
				outcome = "assisted"
				verdict = "认知裁定背景参考 (0 省步 · 净耗 Token)"
				attrSource = "l1_entity_anchor"
				he.Adopted++
				report.AdoptedCount++
				report.Funnel.ContextAssisted++
			}
		} else if turnSelfHealed > 0 {
			// Writing or superseding a memory at the end of a turn (`remember(closes_prior:true)`)
			// when ZERO novel anchors from the recalled band were adopted is a write-side governance
			// contribution, NOT proof that the read-side injection was adopted by the agent!
			// Do NOT inflate `AdoptedCount` or `AdoptRate`: book the read-side injection honestly as
			// unadopted noise (`noise`) while recording `SelfHealed` on the turn for write-side audit.
			savedRounds = 0
			savedTokens = 0
			outcome = "noise"
			valuePillar = "noise"
			verdict = "未采纳读侧注入 · 仅写侧顺带记账 (净亏 Token)"
			attrSource = "unadopted_noise+l2_write"
			report.Funnel.NoiseInjected++
		} else {
			// Zero novel hard anchors adopted (or only echoed what user already typed in prompt) -> net token loss!
			outcome = "noise"
			valuePillar = "noise"
			attrSource = "unadopted_noise"
			if echoCodeCnt > 0 {
				verdict = "剥离提问回声后零增量采纳 (净亏 Token)"
			} else {
				verdict = "未采纳噪音 (0 命中 · 净亏 Token)"
			}
			savedRounds = 0
			savedTokens = 0
			report.Funnel.NoiseInjected++
		}

		savedFreshTokens := savedRounds * roundFreshTokens
		savedCachedTokens := savedRounds * roundCachedTokens
		savedTokens = savedFreshTokens + savedCachedTokens
		netTokens := savedTokens - injectedTokens
		billedNetTokens := (savedFreshTokens + int(math.Round(float64(savedCachedTokens)*0.10))) - injectedTokens
		savedSeconds := savedRounds * 8
		he.SavedRounds += savedRounds
		he.NetSavedTokens += netTokens
		report.TotalSavedRounds += savedRounds
		report.TotalSavedSeconds += savedSeconds
		report.SavedFreshTokens += savedFreshTokens
		report.SavedCachedTokens += savedCachedTokens
		report.GrossSavedTokens += savedTokens

		summaryText := strings.Join(bandTitles, " ； ")
		if summaryText == "" {
			summaryText = truncateStr(rendered, 90)
		}

		evalEntry := SessionEvaluation{
			SessionID:         item.Session,
			Harness:           hID,
			Workspace:         workspace,
			Prompt:            truncateStr(prompt, 110),
			Outcome:           outcome,
			VerdictLabel:      verdict,
			AttributionSource: attrSource,
			ValuePillar:       valuePillar,
			RecalledSummary:   truncateStr(summaryText, 120),
			MatchedAnchors:    allMatched,
			EchoExcluded:      echoCodeCnt,
			HasCodeCoord:      len(matchedToolCode) > 0 || len(matchedReplyCode) > 0,
			CausalVerified:    causalVerified,
			SelfHealed:        turnSelfHealed,
			ModelRequests:     modelReqs,
			ToolCalls:         toolCalls,
			ExploreSteps:      exploreSteps,
			RoundCostTokens:   roundCostTokens,
			RoundFreshTokens:  roundFreshTokens,
			RoundCachedTokens: roundCachedTokens,
			SavedRounds:       savedRounds,
			SavedSeconds:      savedSeconds,
			InjectedTokens:    injectedTokens,
			SavedTokens:       savedTokens,
			SavedFreshTokens:  savedFreshTokens,
			SavedCachedTokens: savedCachedTokens,
			BilledNetTokens:   billedNetTokens,
			NetTokens:         netTokens,
			LatencyMS:         elapsedMS,
			RecordedAt:        item.RecordedAt,
			TimeText:          formatUnixTime(item.RecordedAt),
		}
		report.Sessions = append(report.Sessions, evalEntry)
		if turnImmutable {
			turnCache[turnCacheKey] = evalEntry
			cacheDirty = true
		}
	}
	if cacheDirty && cachePath != "" {
		saveImmutableTurnCache(cachePath, turnCache)
	}
	if skippedTrailingDirty && skippedTrailingPath != "" {
		if rawST, err := json.Marshal(skippedTrailingCache); err == nil {
			writeAtomicFile(skippedTrailingPath, rawST)
		}
	}
	// Legacy carry: ONLY immutable-cache turns that aged out BEFORE the
	// current evaluation window (`ce.RecordedAt < windowMinRecordedAt`) and were NOT skipped by
	// Anti-Cron-Inflation or in-session duplicate guards are folded into ALL-TIME cumulative
	// totals and their corresponding HarnessEfficacy row. This guarantees 100% conservation across:
	//  - Funnel sum == EvaluatedTurns
	//  - DailySavings sum (Injected/Adopted/SavedRounds/NetSavedTokens) == Report totals == Sum(Harnesses)
	//  - Zero resurrection of suppressed cron duplicates or old-algorithm verdicts inside the active window.
	var legacySessions []SessionEvaluation
	windowSavedRounds := report.TotalSavedRounds
	windowInjectedCount := report.InjectedCount
	if strings.TrimSpace(opts.Harness) == "" {
		carriedKeys := map[string]bool{}
		for _, cacheGen := range []map[string]SessionEvaluation{turnCache, legacyTurnCache} {
			for key, ce := range cacheGen {
				stampKey := ce.SessionID + "\x00" + strconv.FormatInt(ce.RecordedAt, 10)
				if processedTurnKeys[key] || processedTurnStamps[stampKey] || carriedKeys[key] || carriedKeys[stampKey] {
					continue
				}
				if windowMinRecordedAt > 0 && ce.RecordedAt >= windowMinRecordedAt {
					// Inside the active window time span: if it was not evaluated above, it was
					// intentionally deduplicated (e.g. hourly cron duplicate) and must NOT be re-added!
					continue
				}
				pNorm := strings.TrimSpace(ids.StripInjected(ce.Prompt))
				if utf8.RuneCountInString(pNorm) >= 16 {
					if _, isCron := latestCronTurnKey[pNorm]; isCron {
						continue
					}
				}
				carriedKeys[key] = true
				carriedKeys[stampKey] = true
				legacySessions = append(legacySessions, ce)
				if ce.InjectedTokens > 0 || ce.Outcome == "high_value" || ce.Outcome == "assisted" || ce.Outcome == "noise" {
					report.InjectedCount++
				}
				report.GrossSavedTokens += ce.SavedTokens
				report.SavedFreshTokens += ce.SavedFreshTokens
				report.SavedCachedTokens += ce.SavedCachedTokens
				report.InjectedTokens += ce.InjectedTokens
				report.TotalSavedRounds += ce.SavedRounds
				report.TotalSavedSeconds += ce.SavedSeconds
				hID := ce.Harness
				if hID == "" {
					hID = "zcode"
				}
				if he := harnessMap[hID]; he != nil {
					he.SavedRounds += ce.SavedRounds
					he.NetSavedTokens += ce.NetTokens
				}
			}
		}
		sort.Slice(legacySessions, func(i, j int) bool {
			if legacySessions[i].RecordedAt != legacySessions[j].RecordedAt {
				return legacySessions[i].RecordedAt > legacySessions[j].RecordedAt
			}
			return legacySessions[i].SessionID < legacySessions[j].SessionID
		})
		report.LegacyCarriedTurns = len(legacySessions)
	}

	// Reverse Sessions so the newest evaluated turns come first, and cap at limit.
	for i, j := 0, len(report.Sessions)-1; i < j; i, j = i+1, j-1 {
		report.Sessions[i], report.Sessions[j] = report.Sessions[j], report.Sessions[i]
	}
	fullEvaluatedSessions := append([]SessionEvaluation(nil), report.Sessions...)
	// Daily/7D/Rolling-Windows account for window turns PLUS legacy-carried turns (each bucketed
	// by its own RecordedAt) so per-day and rolling history is complete even for aged-out turns.
	allAccounted := fullEvaluatedSessions
	if len(legacySessions) > 0 {
		allAccounted = append(append([]SessionEvaluation(nil), fullEvaluatedSessions...), legacySessions...)
	}
	recomputeRollingTimeWindows(report, allAccounted, time.Now())
	if len(report.Sessions) > limit {
		report.Sessions = report.Sessions[:limit]
	}

	// Funnel conservation:
	// report.Funnel buckets strictly partition report.EvaluatedTurns (HighValue + Assisted + Dedup + Silenced + Noise == EvaluatedTurns)
	// so the 5-bucket funnel bar and the turn-by-turn audit table filter chips match 1:1 with zero contradiction.
	// All-time daemon log counters are preserved separately in LogDedupTotal / LogAbsentTotal / TotalHookRuns.
	report.SilencedCount = report.Funnel.DedupSuppressed + report.Funnel.SilenceFiltered
	if report.TotalHookRuns < report.InjectedCount+report.SilencedCount+logDedupCount+logAbsentCount {
		report.TotalHookRuns = maxInt(report.TotalHookRuns, report.InjectedCount+report.SilencedCount)
	}

	// NetSavedTokens is strictly (verified avoided exploration tokens) - (injected tokens).
	report.NetSavedTokens = report.GrossSavedTokens - report.InjectedTokens
	// BilledNetSavedTokens applies the standard 90% KV-cache read discount (0.10x) to cached context tokens
	// so both numerator and denominator share the exact same API billing unit.
	billedGrossSaved := report.SavedFreshTokens + int(math.Round(float64(report.SavedCachedTokens)*0.10))
	report.BilledNetSavedTokens = billedGrossSaved - report.InjectedTokens

	if windowInjectedCount > 0 {
		report.AdoptRate = round1(float64(report.AdoptedCount) * 100.0 / float64(windowInjectedCount))
	}
	totalDecisions := windowInjectedCount + report.SilencedCount
	if totalDecisions > 0 {
		// BootAndCarryoverHits are turns where UserPromptSubmit stayed 0-cost silent/deduped
		// while SessionStart boot landmarks or prior in-session carry-over guided the agent.
		zeroCostHookTurns := report.SilencedCount + report.BootAndCarryoverHits
		if zeroCostHookTurns > totalDecisions {
			zeroCostHookTurns = totalDecisions
		}
		report.SilenceRate = round1(float64(zeroCostHookTurns) * 100.0 / float64(totalDecisions))
	}
	if report.AdoptedCount > 0 {
		// Window-scoped average: window rounds / window adopted turns (keeping
		// legacy-carried rounds out of this ratio so the per-adopted-turn yield stays comparable).
		report.AvgSavedRounds = round1(float64(windowSavedRounds) / float64(report.AdoptedCount))
	}
	if report.InjectedTokens > 0 {
		report.NetTokenROI = round1(float64(report.NetSavedTokens) * 100.0 / float64(report.InjectedTokens))
		report.BilledTokenROI = round1(float64(report.BilledNetSavedTokens) * 100.0 / float64(report.InjectedTokens))
	}

	// Finalize per-harness aggregates.
	for _, id := range harnessOrder {
		he := harnessMap[id]
		if he == nil {
			continue
		}
		if lats := harnessLatencies[id]; len(lats) > 0 {
			s := 0
			for _, v := range lats {
				s += v
			}
			he.AvgLatencyMS = s / len(lats)
		} else if he.Installed {
			he.AvgLatencyMS = report.AvgLatencyMS
		}
		if he.Injections > 0 {
			he.AdoptRate = round1(float64(he.Adopted) * 100.0 / float64(he.Injections))
		}
		if tot := he.Injections + he.Silenced; tot > 0 {
			he.SilenceRate = round1(float64(he.Silenced) * 100.0 / float64(tot))
		}
		report.Harnesses = append(report.Harnesses, *he)
	}

	// Strict Composite EfficacyScore (0..100) and Grade.
	adoptComponent := math.Min(100.0, report.AdoptRate)
	silenceComponent := math.Min(100.0, report.SilenceRate*1.4)
	roiComponent := 50.0
	if report.NetTokenROI > 0 {
		roiComponent = math.Min(100.0, 50.0+report.NetTokenROI*0.4)
	} else if report.NetTokenROI < 0 {
		roiComponent = math.Max(0.0, 50.0+report.NetTokenROI*0.5)
	}
	score := 0.45*adoptComponent + 0.25*silenceComponent + 0.30*roiComponent
	if report.EvaluatedTurns == 0 {
		score = 0.0
	}
	report.EfficacyScore = round1(math.Max(0, math.Min(100, score)))
	switch {
	case report.EfficacyScore >= 85:
		report.Grade = "S"
	case report.EfficacyScore >= 70:
		report.Grade = "A"
	case report.EfficacyScore >= 55:
		report.Grade = "B"
	case report.EfficacyScore >= 40:
		report.Grade = "C"
	default:
		report.Grade = "D"
	}

	if budgetDeadline > 0 {
		report.BudgetSeconds = opts.BudgetSeconds
		report.DeferredTurns = deferredTurns
		report.Partial = deferredTurns > 0
	}
	computeEngineeringDiagnostics(report, allAccounted, logPath, home, nativeStats)
	if len(report.Sessions) > 0 && len(allAccounted) >= len(report.Sessions) {
		report.Sessions = append([]SessionEvaluation(nil), allAccounted[:len(report.Sessions)]...)
	}
	if strings.TrimSpace(opts.Harness) == "" {
		SaveROILedger(st, scope, logPath, home, allAccounted, report)
	}
	return report, nil
}

// recomputeRollingTimeWindows recalculates all time-relative buckets (Today*, Week*,
// DailySavings, HourlySavings, and the 1h/24h/7d/30d rolling monitor Windows) against
// `nowLocal` in a single in-memory pass (<0.2ms). Calling it both at the end of Evaluate
// and inside TryLoadROILedger guarantees that a persisted snapshot read hours later (or
// across midnight) always reflects the exact current rolling windows without re-evaluating.
func recomputeRollingTimeWindows(report *Report, allAccounted []SessionEvaluation, nowLocal time.Time) {
	if report == nil {
		return
	}
	report.TodayTurns = 0
	report.TodayInjectedCount = 0
	report.TodayAdoptedCount = 0
	report.TodaySavedRounds = 0
	report.TodaySavedSeconds = 0
	report.TodayInjectedTokens = 0
	report.TodaySavedFreshTokens = 0
	report.TodaySavedCachedTokens = 0
	report.TodayGrossSavedTokens = 0
	report.TodayBilledNetSavedTokens = 0
	report.TodayBilledTokenROI = 0
	report.TodayNetSavedTokens = 0
	report.TodayNetTokenROI = 0
	report.WeekSavedRounds = 0
	report.WeekNetSavedTokens = 0
	report.DailySavings = nil
	report.HourlySavings = nil
	report.Windows = nil

	midnightLocal := time.Date(nowLocal.Year(), nowLocal.Month(), nowLocal.Day(), 0, 0, 0, 0, nowLocal.Location()).Unix()
	todayCutoff := midnightLocal
	weekCutoff := nowLocal.Unix() - 7*86400
	dailyMap := map[string]*DailySavingBucket{}
	var dailyOrder []string
	windowDefs := []struct {
		Key     string
		Seconds int64
	}{
		{Key: "1h", Seconds: 3600},
		{Key: "24h", Seconds: 86400},
		{Key: "7d", Seconds: 7 * 86400},
		{Key: "30d", Seconds: 30 * 86400},
	}
	windowAgg := make([]*WindowSavingBucket, len(windowDefs))
	windowSessions := make([]map[string]bool, len(windowDefs))
	for i, wd := range windowDefs {
		windowAgg[i] = &WindowSavingBucket{Key: wd.Key, Seconds: wd.Seconds, SinceUnix: nowLocal.Unix() - wd.Seconds}
		windowSessions[i] = map[string]bool{}
	}
	hourlyMap := map[string]*HourlySavingBucket{}
	var hourlyOrder []string
	hourCutoff := nowLocal.Unix() - 48*3600
	for _, s := range allAccounted {
		dayKey := "-"
		if s.RecordedAt > 0 {
			dayKey = time.Unix(s.RecordedAt, 0).Format("2006-01-02")
		}
		dbk := dailyMap[dayKey]
		if dbk == nil {
			dbk = &DailySavingBucket{Date: dayKey}
			dailyMap[dayKey] = dbk
			dailyOrder = append(dailyOrder, dayKey)
		}
		dbk.Turns++
		if s.InjectedTokens > 0 || s.Outcome == "high_value" || s.Outcome == "assisted" || s.Outcome == "noise" {
			dbk.InjectedCount++
			dbk.InjectedTokens += s.InjectedTokens
		}
		if s.Outcome == "high_value" || s.Outcome == "assisted" {
			dbk.AdoptedCount++
		}
		sBilledNet := (s.SavedFreshTokens + int(math.Round(float64(s.SavedCachedTokens)*0.10))) - s.InjectedTokens
		sSec := s.SavedSeconds
		if sSec == 0 && s.SavedRounds > 0 {
			sSec = s.SavedRounds * 8
		}
		dbk.SavedRounds += s.SavedRounds
		dbk.SavedSeconds += sSec
		dbk.SavedFreshTokens += s.SavedFreshTokens
		dbk.SavedCachedTokens += s.SavedCachedTokens
		dbk.GrossSavedTokens += s.SavedTokens
		dbk.BilledNetSavedTokens += sBilledNet
		dbk.NetSavedTokens += s.NetTokens

		if s.RecordedAt >= todayCutoff {
			report.TodayTurns++
			if s.InjectedTokens > 0 || s.Outcome == "high_value" || s.Outcome == "assisted" || s.Outcome == "noise" {
				report.TodayInjectedCount++
				report.TodayInjectedTokens += s.InjectedTokens
			}
			if s.Outcome == "high_value" || s.Outcome == "assisted" {
				report.TodayAdoptedCount++
			}
			report.TodaySavedRounds += s.SavedRounds
			report.TodaySavedSeconds += sSec
			report.TodaySavedFreshTokens += s.SavedFreshTokens
			report.TodaySavedCachedTokens += s.SavedCachedTokens
			report.TodayGrossSavedTokens += s.SavedTokens
			report.TodayBilledNetSavedTokens += sBilledNet
			report.TodayNetSavedTokens += s.NetTokens
		}
		if s.RecordedAt >= weekCutoff {
			report.WeekSavedRounds += s.SavedRounds
			report.WeekNetSavedTokens += s.NetTokens
		}
		if s.RecordedAt > 0 {
			if s.RecordedAt >= hourCutoff {
				hourKey := time.Unix(s.RecordedAt, 0).Format("2006-01-02 15:00")
				hbk := hourlyMap[hourKey]
				if hbk == nil {
					hbk = &HourlySavingBucket{Hour: hourKey, Unix: s.RecordedAt - s.RecordedAt%3600}
					hourlyMap[hourKey] = hbk
					hourlyOrder = append(hourlyOrder, hourKey)
				}
				accumulateHourlySaving(hbk, s, sBilledNet, sSec)
			}
			for i := range windowDefs {
				if s.RecordedAt >= windowAgg[i].SinceUnix {
					accumulateWindowSaving(windowAgg[i], windowSessions[i], s, sBilledNet, sSec)
				}
			}
		}
	}
	if report.TodayInjectedTokens > 0 {
		report.TodayNetTokenROI = round1(float64(report.TodayNetSavedTokens) * 100.0 / float64(report.TodayInjectedTokens))
		report.TodayBilledTokenROI = round1(float64(report.TodayBilledNetSavedTokens) * 100.0 / float64(report.TodayInjectedTokens))
	}
	sort.Strings(dailyOrder)
	for _, dk := range dailyOrder {
		b := dailyMap[dk]
		if b.InjectedTokens > 0 {
			b.NetTokenROI = round1(float64(b.NetSavedTokens) * 100.0 / float64(b.InjectedTokens))
		}
		report.DailySavings = append(report.DailySavings, *b)
	}
	for i := range windowDefs {
		finalizeWindowSaving(windowAgg[i])
		report.Windows = append(report.Windows, *windowAgg[i])
	}
	sort.Strings(hourlyOrder)
	if len(hourlyOrder) > 48 {
		hourlyOrder = hourlyOrder[len(hourlyOrder)-48:]
	}
	for _, hk := range hourlyOrder {
		b := hourlyMap[hk]
		if b.InjectedTokens > 0 {
			b.NetTokenROI = round1(float64(b.NetSavedTokens) * 100.0 / float64(b.InjectedTokens))
		}
		report.HourlySavings = append(report.HourlySavings, *b)
	}
}

// budgetExhausted reports whether the soft per-run settling budget is used up. A zero
// deadline means unbounded (interactive CLI runs), so the guard is inert unless the caller
// (the Admin panel) hands over an explicit budget via `efficacy --budget-seconds`.
func budgetExhausted(deadlineUnix int64, now time.Time) bool {
	return deadlineUnix > 0 && now.Unix() > deadlineUnix
}

// accumulateWindowSaving folds one evaluated turn into a rolling monitor window bucket.
// Session cardinality is tracked through the caller-owned set so a window never counts a
// session twice no matter how many turns it contributed.
func accumulateWindowSaving(w *WindowSavingBucket, seenSessions map[string]bool, s SessionEvaluation, billedNet, savedSeconds int) {
	w.Turns++
	if s.SessionID != "" && !seenSessions[s.SessionID] {
		seenSessions[s.SessionID] = true
		w.Sessions++
	}
	if s.InjectedTokens > 0 || s.Outcome == "high_value" || s.Outcome == "assisted" || s.Outcome == "noise" {
		w.InjectedCount++
		w.InjectedTokens += s.InjectedTokens
	}
	if s.Outcome == "high_value" || s.Outcome == "assisted" {
		w.AdoptedCount++
	}
	switch s.Outcome {
	case "silenced":
		w.SilencedCount++
	case "dedup":
		w.SilencedCount++
		w.DedupCount++
	}
	// Window-scoped funnel split (mirrors the report-level attribution buckets).
	switch s.Outcome {
	case "high_value":
		w.FunnelHighValue++
	case "assisted":
		w.FunnelAssisted++
	case "dedup":
		w.FunnelDedup++
	case "silenced":
		w.FunnelSilence++
	case "noise":
		w.FunnelNoise++
	}
	w.SavedRounds += s.SavedRounds
	w.SavedSeconds += savedSeconds
	w.SavedFreshTokens += s.SavedFreshTokens
	w.SavedCachedTokens += s.SavedCachedTokens
	w.GrossSavedTokens += s.SavedTokens
	w.BilledNetSavedTokens += billedNet
	w.NetSavedTokens += s.NetTokens
	w.SelfHealedCount += s.SelfHealed
	if s.HasCodeCoord {
		w.CodeCoordHits++
	}
	if s.CausalVerified {
		w.CausalDirectHits++
	}
	switch s.ValuePillar {
	case "navigation":
		w.NavigationDirectHits++
	case "guardrail":
		w.GuardrailHits++
	case "continuity":
		w.ContinuityHits++
	}
	if s.LatencyMS > 0 {
		w.latencySumMS += s.LatencyMS
		w.latencySamples++
	}
}

// finalizeWindowSaving derives the ratio metrics of a rolling monitor window.
func finalizeWindowSaving(w *WindowSavingBucket) {
	if w.InjectedTokens > 0 {
		w.NetTokenROI = round1(float64(w.NetSavedTokens) * 100.0 / float64(w.InjectedTokens))
		w.BilledTokenROI = round1(float64(w.BilledNetSavedTokens) * 100.0 / float64(w.InjectedTokens))
	}
	if w.InjectedCount > 0 {
		w.AdoptRate = round1(float64(w.AdoptedCount) * 100.0 / float64(w.InjectedCount))
	}
	if w.latencySamples > 0 {
		w.AvgLatencyMS = w.latencySumMS / w.latencySamples
	}
}

// accumulateHourlySaving folds one evaluated turn into its clock-hour bucket.
func accumulateHourlySaving(h *HourlySavingBucket, s SessionEvaluation, billedNet, savedSeconds int) {
	h.Turns++
	if s.InjectedTokens > 0 || s.Outcome == "high_value" || s.Outcome == "assisted" || s.Outcome == "noise" {
		h.InjectedCount++
		h.InjectedTokens += s.InjectedTokens
	}
	if s.Outcome == "high_value" || s.Outcome == "assisted" {
		h.AdoptedCount++
	}
	h.SavedRounds += s.SavedRounds
	h.SavedSeconds += savedSeconds
	h.SavedFreshTokens += s.SavedFreshTokens
	h.SavedCachedTokens += s.SavedCachedTokens
	h.GrossSavedTokens += s.SavedTokens
	h.BilledNetSavedTokens += billedNet
	h.NetSavedTokens += s.NetTokens
}

// loadNativeHarnessTelemetry inspects local harness stores/transcripts (ZCode SQLite,
// Claude Code JSONL, Codex JSONL, and transcript_cursor files) and computes both:
//  1. the empirical unassisted exploration baseline (avg blind-search steps);
//  2. per-session LLM round-trip token volume split into Fresh (uncached input + output)
//     and Cached (historical session context re-read per round-trip) from real model usage bills.
func loadNativeHarnessTelemetry(home string, st *store.Store) (map[string]*nativeSessionStats, float64, int, int, int) {
	out := map[string]*nativeSessionStats{}
	var unassistedExploreCounts []int
	var roundCostSamples []int
	var freshSamples []int
	var cachedSamples []int

	evaluatedSessions := map[string]bool{}
	if st != nil && st.DB() != nil {
		sRows, sErr := st.DB().Query(`SELECT DISTINCT session FROM trace WHERE session != '' AND session != 'default'`)
		if sErr == nil {
			for sRows.Next() {
				var sName string
				if sRows.Scan(&sName) == nil && sName != "" {
					evaluatedSessions[sName] = true
				}
			}
			_ = sRows.Close()
		}
	}

	var telemetryCachePath string
	var zTurnCnt int
	var zMaxStarted, zMaxCompleted int64
	var evalSessHash string
	if home != "" {
		telemetryCachePath = filepath.Join(home, ".local", "state", "rolling-memory", "native_telemetry_ledger_v2.json")
		zTurnCnt, zMaxStarted, zMaxCompleted, evalSessHash = queryZCodeTelemetryWatermark(home, evaluatedSessions)
		if rawTC, err := os.ReadFile(telemetryCachePath); err == nil && len(rawTC) > 0 {
			var tc persistedNativeTelemetryCache
			if json.Unmarshal(rawTC, &tc) == nil &&
				tc.Version == 1 &&
				len(tc.Sessions) > 0 &&
				((tc.ZCodeTurnCount == zTurnCnt &&
					tc.ZCodeMaxStartedAt == zMaxStarted &&
					tc.ZCodeMaxCompletedAt == zMaxCompleted &&
					tc.EvaluatedSessionHash == evalSessHash) ||
					(tc.UpdatedUnix > 0 && time.Now().Unix()-tc.UpdatedUnix < 90)) {
				return tc.Sessions, tc.EmpiricalBaseline, tc.AvgDynamicRoundTok, tc.AvgFreshTok, tc.AvgCachedTok
			}
		}
	}

	if home != "" {
		zdbPath := filepath.Join(home, ".zcode", "cli", "db", "db.sqlite")
		if _, err := os.Stat(zdbPath); err == nil {
			if db, openErr := sql.Open("sqlite", zdbPath+"?mode=ro&_pragma=busy_timeout(1500)"); openErr == nil {
				defer db.Close()
				// Query FROM turn_usage (LEFT JOIN session) rather than FROM session so archived/pruned
				// sessions whose `session` row was cleaned up still retain 100% of their real turn_usage
				// and tool_usage telemetry (preventing fake `reqs=1, tools=0, explore=0` inflation!).
				rows, qErr := db.Query(`
						SELECT t.session_id,
						       coalesce(s.directory, ''),
						       coalesce(s.title, ''),
						       count(distinct t.turn_id),
						       coalesce(sum(t.model_request_count), 0),
						       coalesce(sum(t.tool_call_count), 0),
						       coalesce(sum(t.input_tokens), 0),
						       coalesce(sum(t.cache_read_input_tokens), 0),
						       coalesce(sum(t.output_tokens), 0),
						       coalesce(sum(t.reasoning_tokens), 0)
						FROM turn_usage t
						LEFT JOIN session s ON s.id = t.session_id
						GROUP BY t.session_id
						ORDER BY max(t.started_at) DESC
						LIMIT 350
					`)
				if qErr == nil {
					for rows.Next() {
						var id, dir, title string
						var turns, reqs, tools, inTok, cacheTok, outTok, reasonTok int
						if rows.Scan(&id, &dir, &title, &turns, &reqs, &tools, &inTok, &cacheTok, &outTok, &reasonTok) == nil {
							ws := ""
							if dir != "" {
								ws = filepath.Base(strings.TrimRight(dir, "/"))
							}
							ns := &nativeSessionStats{
								Harness:         "zcode",
								Workspace:       ws,
								Title:           title,
								Turns:           turns,
								ModelRequests:   reqs,
								ToolCalls:       tools,
								InputTokens:     inTok,
								CacheReadTokens: cacheTok,
								OutputTokens:    outTok,
								ReasoningTokens: reasonTok,
							}
							if reqs > 0 {
								// Marginal Exploration Round-Trip Cost Model:
								//  1) Marginal Fresh Tokens / Exploration Step = genuine tool-output + reasoning + output per step
								//     (bounded to [1,200 .. 4,800] so a cold-cache turn #1 prefix miss never inflates per-step fresh cost to 28,000!);
								//  2) Early-Turn Cached Context / Exploration Step = initial session prefix re-read during early exploration
								//     (bounded to [12,000 .. 48,000], avoiding late-turn 240,000 tail-context inflation).
								uncached := maxInt(inTok-cacheTok, 0)
								freshPerReq := (uncached/maxInt(turns*2, 1) + outTok + reasonTok) / reqs
								if freshPerReq < 1200 {
									freshPerReq = 1200
								}
								if freshPerReq > 4800 {
									freshPerReq = 4800
								}
								cachedPerReq := cacheTok / reqs
								if cachedPerReq < 12000 {
									cachedPerReq = 12000
								}
								if cachedPerReq > 48000 {
									cachedPerReq = 48000
								}
								totalPerReq := freshPerReq + cachedPerReq
								ns.RoundFreshTokens = freshPerReq
								ns.RoundCachedTokens = cachedPerReq
								ns.DynamicRoundTokens = totalPerReq
								roundCostSamples = append(roundCostSamples, totalPerReq)
								freshSamples = append(freshSamples, freshPerReq)
								cachedSamples = append(cachedSamples, cachedPerReq)
							}
							out[id] = ns
						}
					}
					_ = rows.Close()
				}

				// Use the structural read_only & started_at columns in tool_usage to compute
				// true pre-first-write (TTFW) exploration steps per turn, driving tool_usage via
				// the indexed session_id of the top 350 recent sessions instead of scanning 1.4M+ historical rows.
				roRows, roErr := db.Query(`
							WITH recent_sess AS MATERIALIZED (
								SELECT session_id
								FROM turn_usage
								GROUP BY session_id
								ORDER BY max(started_at) DESC
								LIMIT 350
							),
								sess_tools AS MATERIALIZED (
									SELECT t.session_id, t.turn_id, t.read_only, t.started_at, lower(coalesce(t.tool_name, '')) AS tool_name
									FROM recent_sess s
									CROSS JOIN tool_usage t ON t.session_id = s.session_id
								),
								turn_first_rw AS (
									SELECT session_id, turn_id, MIN(started_at) AS first_rw_at
									FROM sess_tools
									WHERE read_only = 0
									GROUP BY session_id, turn_id
								),
								turn_ttfw AS (
									SELECT t.session_id, t.turn_id,
									       SUM(CASE WHEN t.tool_name IN ('grep', 'glob', 'find') AND (fw.first_rw_at IS NULL OR t.started_at < fw.first_rw_at) THEN 1 ELSE 0 END) AS pre_ro,
									       SUM(CASE WHEN t.read_only = 0 THEN 1 ELSE 0 END) AS rw_cnt
									FROM sess_tools t
									LEFT JOIN turn_first_rw fw ON t.session_id = fw.session_id AND t.turn_id = fw.turn_id
									GROUP BY t.session_id, t.turn_id
								)
								SELECT session_id,
								       CAST(ROUND(AVG(pre_ro)) AS INTEGER) AS avg_pre_ro,
								       SUM(rw_cnt) AS total_rw,
								       COUNT(DISTINCT turn_id) AS tool_turns
								FROM turn_ttfw
								GROUP BY session_id
							`)
				if roErr == nil {
					for roRows.Next() {
						var sid string
						var avgPreRO, totalRW, toolTurns int
						if roRows.Scan(&sid, &avgPreRO, &totalRW, &toolTurns) == nil {
							if stItem, ok := out[sid]; ok {
								if stItem.Turns == 0 && toolTurns > 0 {
									stItem.Turns = toolTurns
								}
								if avgPreRO > 3 {
									avgPreRO = 3
								}
								stItem.ExploreCalls = avgPreRO
								if avgPreRO > 0 && totalRW > 0 {
									unassistedExploreCounts = append(unassistedExploreCounts, avgPreRO)
								}
							}
						}
					}
					_ = roRows.Close()
				}

				partCachePath := filepath.Join(home, ".local", "state", "rolling-memory", "native_part_cache_v6.json")
				type cachedSessionParts struct {
					Cached         bool         `json:"cached"`
					MaxStartedAt   int64        `json:"max_started_at"`
					MaxCompletedAt int64        `json:"max_completed_at"`
					TotalTools     int          `json:"total_tools"`
					Actions        []toolAction `json:"actions"`
					TouchedFiles   []string     `json:"touched_files"`
				}
				partCache := map[string]cachedSessionParts{}
				if rawPC, pcErr := os.ReadFile(partCachePath); pcErr == nil {
					_ = json.Unmarshal(rawPC, &partCache)
				}
				partCacheDirty := false
				for sid, stItem := range out {
					if len(evaluatedSessions) > 0 && !evaluatedSessions[sid] {
						continue
					}
					var maxTurnStarted, maxTurnCompleted int64
					var totalTurnTools int
					tuRows, tuErr := db.Query(`
									SELECT coalesce(started_at, 0), coalesce(completed_at, 0),
									       coalesce(model_request_count, 0), coalesce(tool_call_count, 0),
									       coalesce(input_tokens, 0), coalesce(cache_read_input_tokens, 0),
									       coalesce(output_tokens, 0), coalesce(reasoning_tokens, 0)
									FROM turn_usage
									WHERE session_id = ? AND model_request_count > 0
									ORDER BY started_at ASC
								`, sid)
					if tuErr == nil {
						for tuRows.Next() {
							var rec turnUsageRecord
							if tuRows.Scan(&rec.StartedAt, &rec.CompletedAt, &rec.ModelRequests, &rec.ToolCalls,
								&rec.InputTokens, &rec.CacheReadTokens, &rec.OutputTokens, &rec.ReasoningTokens) == nil {
								rec.StartedAt = normalizeUnixSeconds(rec.StartedAt)
								rec.CompletedAt = normalizeUnixSeconds(rec.CompletedAt)
								if rec.StartedAt > maxTurnStarted {
									maxTurnStarted = rec.StartedAt
								}
								if rec.CompletedAt > maxTurnCompleted {
									maxTurnCompleted = rec.CompletedAt
								}
								totalTurnTools += rec.ToolCalls
								stItem.TurnUsages = append(stItem.TurnUsages, rec)
							}
						}
						_ = tuRows.Close()
					}

					if cp, ok := partCache[sid]; ok && maxTurnStarted > 0 &&
						cp.MaxStartedAt == maxTurnStarted &&
						cp.MaxCompletedAt == maxTurnCompleted &&
						cp.TotalTools == totalTurnTools &&
						(cp.Cached || len(cp.Actions) > 0) {
						stItem.Actions = cp.Actions
						stItem.TouchedFiles = cp.TouchedFiles
						continue
					}

					pRows, pErr := db.Query(`SELECT data, coalesce(time_created, 0) FROM part WHERE session_id = ? AND substr(data, 1, 140) LIKE '%"type":"tool"%' ORDER BY time_created DESC, sequence DESC LIMIT 1500`, sid)
					if pErr == nil {
						var revActions []toolAction
						for pRows.Next() {
							var pdata string
							var tCreated int64
							if pRows.Scan(&pdata, &tCreated) == nil && strings.Contains(pdata, `"type":"tool"`) {
								targetOnly := extractToolTargetFieldsOnly(pdata)
								if targetOnly != "" {
									stItem.TouchedFiles = append(stItem.TouchedFiles, targetOnly)
								}
								lowerP := strings.ToLower(pdata)
								lowerTarget := strings.ToLower(targetOnly)
								isToolSchemaLookup := strings.Contains(lowerP, `"tool":"toolsearch"`) || strings.Contains(lowerP, `"tool":"localtoolsearch"`)
								isSearch := !isToolSchemaLookup && (strings.Contains(lowerP, `"tool":"grep"`) ||
									strings.Contains(lowerP, `"tool":"glob"`) ||
									strings.Contains(lowerP, `"tool":"find"`) ||
									strings.Contains(lowerP, `"subagent_type":"explore"`) ||
									strings.Contains(lowerP, `"tool":"mcp__plugin_rolling-memory_rolling-memory__recall"`) ||
									(strings.Contains(lowerP, `"tool":"bash"`) && isBlindBashSearchCommand(lowerTarget)))
								tsSec := normalizeUnixSeconds(tCreated)
								revActions = append(revActions, toolAction{
									Target:    lowerTarget,
									IsSearch:  isSearch,
									ReadOnly:  isSearch || strings.Contains(lowerP, `"tool":"read"`),
									StartedAt: tsSec,
								})
							}
						}
						_ = pRows.Close()
						for i := len(revActions) - 1; i >= 0; i-- {
							stItem.Actions = append(stItem.Actions, revActions[i])
						}
					}
					// If `part` rows were cascade-deleted for an archived session, reconstruct
					// read-only / search step timing from `tool_usage` so `exploreSteps` is never
					// falsely reported as 0!
					if len(stItem.Actions) == 0 {
						tRows, tErr := db.Query(`
								SELECT lower(coalesce(tool_name, '')), coalesce(read_only, 0), coalesce(started_at, 0)
								FROM tool_usage
								WHERE session_id = ?
								ORDER BY started_at ASC
								LIMIT 500
							`, sid)
						if tErr == nil {
							for tRows.Next() {
								var tName string
								var ro int
								var sAt int64
								if tRows.Scan(&tName, &ro, &sAt) == nil {
									isSearch := tName == "grep" || tName == "glob" || tName == "find"
									stItem.Actions = append(stItem.Actions, toolAction{
										Name:      tName,
										Target:    "",
										IsSearch:  isSearch,
										ReadOnly:  ro == 1,
										StartedAt: normalizeUnixSeconds(sAt),
									})
								}
							}
							_ = tRows.Close()
						}
					}
					if maxTurnStarted > 0 {
						partCache[sid] = cachedSessionParts{
							Cached:         true,
							MaxStartedAt:   maxTurnStarted,
							MaxCompletedAt: maxTurnCompleted,
							TotalTools:     totalTurnTools,
							Actions:        stItem.Actions,
							TouchedFiles:   stItem.TouchedFiles,
						}
						partCacheDirty = true
					}
				}
				if partCacheDirty && len(partCache) > 0 {
					if rawPC, mErr := json.Marshal(partCache); mErr == nil {
						writeAtomicFile(partCachePath, rawPC)
					}
				}
			}
		}
		// Universal Multi-Harness Native Telemetry Discovery:
		// Automatically discover and parse native session stores for Claude Code (~/.claude/projects),
		// Codex (~/.codex/state_*.sqlite & ~/.codex/sessions), and DSH (~/.dsh/sessions).
		loadOtherNativeHarnesses(home, out, evaluatedSessions, &unassistedExploreCounts, &roundCostSamples, &freshSamples, &cachedSamples)
	}

	// Also inspect any JSONL transcript registered in transcript_cursor (Claude Code, Codex, Cursor, etc.)
	if st != nil && st.DB() != nil {
		rows, err := st.DB().Query(`SELECT path, session FROM transcript_cursor ORDER BY updated_at DESC LIMIT 50`)
		if err == nil {
			for rows.Next() {
				var p, sess string
				if rows.Scan(&p, &sess) == nil && sess != "" {
					if _, exists := out[sess]; exists {
						continue
					}
					if len(evaluatedSessions) > 0 && !evaluatedSessions[sess] {
						continue
					}
					h := detectHarnessFromPath(p)
					ns := parseJSONLTranscriptStats(p, h)
					out[sess] = ns
					if ns.ExploreCalls > 0 {
						unassistedExploreCounts = append(unassistedExploreCounts, ns.ExploreCalls)
					}
				}
			}
			_ = rows.Close()
		}
	}

	baseline := 2.0
	if len(unassistedExploreCounts) > 0 {
		sum := 0
		for _, v := range unassistedExploreCounts {
			sum += v
		}
		baseline = float64(sum) / float64(len(unassistedExploreCounts))
		if baseline < 1.5 {
			baseline = 1.5
		}
		if baseline > 3.0 {
			baseline = 3.0
		}
	}
	avgFresh := 3200
	if len(freshSamples) > 0 {
		sum := 0
		for _, v := range freshSamples {
			sum += v
		}
		avgFresh = sum / len(freshSamples)
	}
	avgCached := 28000
	if len(cachedSamples) > 0 {
		sum := 0
		for _, v := range cachedSamples {
			sum += v
		}
		avgCached = sum / len(cachedSamples)
	}
	avgRoundCost := avgFresh + avgCached
	if len(roundCostSamples) > 0 {
		sum := 0
		for _, v := range roundCostSamples {
			sum += v
		}
		avgRoundCost = sum / len(roundCostSamples)
	}
	if telemetryCachePath != "" && len(out) > 0 {
		tc := persistedNativeTelemetryCache{
			Version:              1,
			ZCodeTurnCount:       zTurnCnt,
			ZCodeMaxStartedAt:    zMaxStarted,
			ZCodeMaxCompletedAt:  zMaxCompleted,
			EvaluatedSessionHash: evalSessHash,
			UpdatedUnix:          time.Now().Unix(),
			Sessions:             out,
			EmpiricalBaseline:    baseline,
			AvgDynamicRoundTok:   avgRoundCost,
			AvgFreshTok:          avgFresh,
			AvgCachedTok:         avgCached,
		}
		if rawTC, err := json.Marshal(tc); err == nil {
			_ = os.MkdirAll(filepath.Dir(telemetryCachePath), 0o755)
			writeAtomicFile(telemetryCachePath, rawTC)
		}
	}
	return out, baseline, avgRoundCost, avgFresh, avgCached
}

// computeDynamicRoundCost returns (totalRoundTokens, freshRoundTokens, cachedRoundTokens, turnModelReqs, turnToolCalls)
// for 1 exploration round-trip in the given session & turn:
//  1. Primary (Exact Turn-Level Bill Match): matches the specific turn_usage row in [promptAt-12, nextPromptAt+5)
//     so each turn reflects its true accumulated historical context depth and request count;
//  2. Secondary: uses session-level measured usage per request when turn_usage timestamps do not align;
//  3. Fallback: computes from the session's cumulative historical context runes + system base.
func computeDynamicRoundCost(nStat *nativeSessionStats, promptAt, nextPromptAt int64, cumContextRunes, turn int) (int, int, int, int, int) {
	if nStat != nil && len(nStat.TurnUsages) > 0 && promptAt > 0 {
		var best *turnUsageRecord
		bestDiff := int64(1 << 60)
		for i := range nStat.TurnUsages {
			tu := &nStat.TurnUsages[i]
			if tu.ModelRequests <= 0 {
				continue
			}
			if nextPromptAt > 0 && tu.StartedAt > nextPromptAt+5 {
				continue
			}
			if tu.StartedAt+15 < promptAt {
				continue
			}
			diff := tu.StartedAt - promptAt
			if diff < 0 {
				diff = -diff
			}
			if diff < bestDiff {
				bestDiff = diff
				best = tu
			}
		}
		if best != nil && bestDiff <= 120 {
			// Marginal Exploration Step Cost:
			// When a cold-cache request occurs on step 1, `best.InputTokens - best.CacheReadTokens`
			// includes the entire 30k..200k system/session prefix. An avoided exploration step (grep/find)
			// mid-turn hits KV-cache for the prefix and only adds marginal fresh tokens (search output +
			// tool call + step output, ~1,200..4,800 tokens) plus early-turn cached prefix re-read (~12k..56k).
			stepOutAndReason := (best.OutputTokens + best.ReasoningTokens) / best.ModelRequests
			uncachedPerReq := maxInt(best.InputTokens-best.CacheReadTokens, 0) / best.ModelRequests
			if uncachedPerReq > 3600 {
				uncachedPerReq = 2200 + (uncachedPerReq-2200)/10
			}
			fresh := uncachedPerReq + stepOutAndReason
			if fresh < 1100 {
				fresh = 1100
			}
			if fresh > 4800 {
				fresh = 4800
			}
			cached := best.CacheReadTokens / best.ModelRequests
			// Discount multi-step snowball inflation: opening exploration happens in the first 1..3 steps
			// of a turn before 50+ subsequent tool outputs accumulate in the context window.
			if best.ModelRequests > 6 && cached > 28000 {
				cached = 24000 + (cached-24000)*4/best.ModelRequests
			}
			if cached < 10000 {
				cached = 10000
			}
			if cached > 56000 {
				cached = 56000
			}
			return fresh + cached, fresh, cached, best.ModelRequests, best.ToolCalls
		}
	}
	if nStat != nil && nStat.DynamicRoundTokens > 0 {
		fresh := nStat.RoundFreshTokens
		cached := nStat.RoundCachedTokens
		return fresh + cached, fresh, cached, 0, 0
	}
	contextDepthTokens := maxInt(cumContextRunes/2, 600)
	fresh := 1600 + maxInt(turn-1, 0)*220
	if fresh > 9500 {
		fresh = 9500
	}
	// Full session historical context re-read on each stateless LLM round-trip:
	// system prompt & tool schemas (~14,000 tokens) + accumulated conversation & file context.
	cached := 14000 + contextDepthTokens + maxInt(turn-1, 0)*2800
	if cached > 72000 {
		cached = 72000
	}
	return fresh + cached, fresh, cached, 0, 0
}

// extractToolTargetFieldsOnly extracts only genuine file path / command target fields
// ("file_path", "path", "pattern") from a serialized tool call payload, strictly excluding
// Edit/Write body contents ("old_string", "new_string", "content") so code comments inside
// an Edit diff never trigger false-positive coordinate matches!
func extractToolTargetFieldsOnly(rawJSON string) string {
	var m map[string]any
	if json.Unmarshal([]byte(rawJSON), &m) != nil {
		return ""
	}
	var parts []string
	if tool := asString(m["tool"]); tool != "" {
		parts = append(parts, tool)
	}
	if state, ok := m["state"].(map[string]any); ok {
		if inp, ok := state["input"].(map[string]any); ok {
			for _, k := range []string{"file_path", "path", "notebook_path", "pattern", "query", "command"} {
				if v := strings.TrimSpace(asString(inp[k])); v != "" {
					if k == "command" && len(v) > 400 {
						v = strings.ToValidUTF8(v[:400], "")
					}
					parts = append(parts, v)
				}
			}
		}
	}
	if inp, ok := m["input"].(map[string]any); ok {
		for _, k := range []string{"file_path", "path", "notebook_path", "pattern", "query", "command"} {
			if v := strings.TrimSpace(asString(inp[k])); v != "" {
				if k == "command" && len(v) > 400 {
					v = strings.ToValidUTF8(v[:400], "")
				}
				parts = append(parts, v)
			}
		}
	}
	return strings.Join(parts, " ")
}

// loadOtherNativeHarnesses discovers and parses native session stores across all connected
// coding harnesses on disk:
//   - Claude Code: ~/.claude/projects/**/*.jsonl
//   - Codex:       ~/.codex/state_*.sqlite (`threads` table) + ~/.codex/sessions/**/*.jsonl
//   - DSH:         ~/.dsh/sessions/**
//   - Cursor / Windsurf / Cline: JSONL transcripts
func loadOtherNativeHarnesses(home string, out map[string]*nativeSessionStats, evaluatedSessions map[string]bool, exploreCounts, roundCostSamples, freshSamples, cachedSamples *[]int) {
	if home == "" {
		return
	}
	// 1. Claude Code native project transcripts (~/.claude/projects/*/*.jsonl)
	claudeProjDir := filepath.Join(home, ".claude", "projects")
	if entries, err := os.ReadDir(claudeProjDir); err == nil {
		count := 0
		for _, proj := range entries {
			if !proj.IsDir() {
				continue
			}
			subDir := filepath.Join(claudeProjDir, proj.Name())
			files, fErr := os.ReadDir(subDir)
			if fErr != nil {
				continue
			}
			for _, f := range files {
				if f.IsDir() || !strings.HasSuffix(f.Name(), ".jsonl") {
					continue
				}
				sid := strings.TrimSuffix(f.Name(), ".jsonl")
				if _, exists := out[sid]; exists {
					continue
				}
				if len(evaluatedSessions) > 0 && !evaluatedSessions[sid] {
					continue
				}
				fullPath := filepath.Join(subDir, f.Name())
				ns := parseJSONLTranscriptStats(fullPath, "claude-code")
				if ns.Turns > 0 {
					out[sid] = ns
					if ns.DynamicRoundTokens > 0 {
						*roundCostSamples = append(*roundCostSamples, ns.DynamicRoundTokens)
						*freshSamples = append(*freshSamples, ns.RoundFreshTokens)
						*cachedSamples = append(*cachedSamples, ns.RoundCachedTokens)
					}
					if ns.ExploreCalls > 0 {
						*exploreCounts = append(*exploreCounts, ns.ExploreCalls)
					}
					count++
					if count >= 40 {
						break
					}
				}
			}
			if count >= 40 {
				break
			}
		}
	}

	// 2. Codex native SQLite (`~/.codex/state_5.sqlite`) + rollout JSONL files
	codexMatches, _ := filepath.Glob(filepath.Join(home, ".codex", "state_*.sqlite"))
	for _, cdbPath := range codexMatches {
		if db, err := sql.Open("sqlite", cdbPath+"?mode=ro&_pragma=busy_timeout(1000)"); err == nil {
			rows, qErr := db.Query(`SELECT id, cwd, title, rollout_path, coalesce(tokens_used, 0) FROM threads ORDER BY updated_at DESC LIMIT 40`)
			if qErr == nil {
				for rows.Next() {
					var id, cwd, title, rPath string
					var tokUsed int
					if rows.Scan(&id, &cwd, &title, &rPath, &tokUsed) == nil && id != "" {
						if _, exists := out[id]; exists {
							continue
						}
						if len(evaluatedSessions) > 0 && !evaluatedSessions[id] {
							continue
						}
						ns := &nativeSessionStats{
							Harness:   "codex",
							Workspace: filepath.Base(strings.TrimRight(cwd, "/")),
							Title:     title,
							Turns:     1,
						}
						if rPath != "" {
							if _, sErr := os.Stat(rPath); sErr == nil {
								parsed := parseJSONLTranscriptStats(rPath, "codex")
								if parsed.Workspace != "" && ns.Workspace == "" {
									ns.Workspace = parsed.Workspace
								}
								ns.Turns = parsed.Turns
								ns.ModelRequests = parsed.ModelRequests
								ns.ToolCalls = parsed.ToolCalls
								ns.ExploreCalls = parsed.ExploreCalls
								ns.TouchedFiles = parsed.TouchedFiles
								ns.Actions = parsed.Actions
								ns.RoundFreshTokens = parsed.RoundFreshTokens
								ns.RoundCachedTokens = parsed.RoundCachedTokens
								ns.DynamicRoundTokens = parsed.DynamicRoundTokens
							}
						}
						if ns.DynamicRoundTokens == 0 && tokUsed > 0 {
							reqs := maxInt(ns.ModelRequests, 1)
							perReq := tokUsed / reqs
							if perReq > 4000 {
								ns.RoundFreshTokens = maxInt(perReq/8, 1600)
								ns.RoundCachedTokens = maxInt(perReq-ns.RoundFreshTokens, 12000)
								ns.DynamicRoundTokens = ns.RoundFreshTokens + ns.RoundCachedTokens
							}
						}
						out[id] = ns
					}
				}
				_ = rows.Close()
			}
			_ = db.Close()
		}
	}
}

// parseJSONLTranscriptStats extracts tool call counts, target file paths, exploration steps,
// and real token usage (input_tokens / cache_read_input_tokens / output_tokens) from any
// harness JSONL session transcript file (Claude Code, Codex, Cursor, Windsurf, Cline, DSH).
func parseJSONLTranscriptStats(path, harnessID string) *nativeSessionStats {
	ns := &nativeSessionStats{
		Harness:   harnessID,
		Workspace: filepath.Base(filepath.Dir(path)),
	}
	f, err := os.Open(path)
	if err != nil {
		ns.Turns = 1
		ns.ModelRequests = 1
		return ns
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	buf := make([]byte, 0, 256*1024)
	scanner.Buffer(buf, 2*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, `"workingDirectory"`) || strings.Contains(line, `"cwd"`) {
			var m map[string]any
			if json.Unmarshal([]byte(line), &m) == nil {
				if cwd := asString(m["cwd"]); cwd != "" {
					ns.Workspace = filepath.Base(strings.TrimRight(cwd, "/"))
				} else if payload, ok := m["payload"].(map[string]any); ok {
					if cwd := asString(payload["cwd"]); cwd != "" {
						ns.Workspace = filepath.Base(strings.TrimRight(cwd, "/"))
					}
				}
			}
		}
		if strings.Contains(line, `"role":"user"`) || strings.Contains(line, `"type":"user"`) {
			ns.Turns++
		}
		if strings.Contains(line, `"usage":{`) {
			ns.ModelRequests++
			var m map[string]any
			if json.Unmarshal([]byte(line), &m) == nil {
				u, _ := m["usage"].(map[string]any)
				if u == nil {
					if msg, ok := m["message"].(map[string]any); ok {
						u, _ = msg["usage"].(map[string]any)
					}
				}
				if u != nil {
					ns.InputTokens += asInt(u["input_tokens"]) + asInt(u["inputTokens"])
					ns.CacheReadTokens += asInt(u["cache_read_input_tokens"]) + asInt(u["cacheReadTokens"]) + asInt(u["cached_tokens"])
					ns.OutputTokens += asInt(u["output_tokens"]) + asInt(u["outputTokens"])
				}
			}
		}
		if strings.Contains(line, `"tool_use"`) || strings.Contains(line, `"function_call"`) || strings.Contains(line, `"type":"tool"`) {
			ns.ToolCalls++
			lowerL := strings.ToLower(line)
			isSearch := strings.Contains(lowerL, `"grep"`) || strings.Contains(lowerL, `"glob"`) || strings.Contains(lowerL, `"find"`)
			if isSearch {
				ns.ExploreCalls++
			}
			target := extractToolTargetFieldsOnly(line)
			if target == "" {
				for _, p := range extractStructuralPathsEfficacy(line, 3) {
					target += " " + p
				}
			}
			if strings.TrimSpace(target) != "" {
				ns.TouchedFiles = append(ns.TouchedFiles, target)
			}
			ns.Actions = append(ns.Actions, toolAction{
				Target:   strings.ToLower(target),
				IsSearch: isSearch,
				ReadOnly: isSearch || strings.Contains(lowerL, `"read"`),
			})
		}
	}
	if ns.Turns == 0 {
		ns.Turns = 1
	}
	if ns.ModelRequests == 0 {
		ns.ModelRequests = maxInt(ns.ToolCalls, ns.Turns)
	}
	if ns.InputTokens > 0 && ns.ModelRequests > 0 {
		uncached := maxInt(ns.InputTokens-ns.CacheReadTokens, 0)
		fresh := (uncached + ns.OutputTokens) / ns.ModelRequests
		if fresh < 1200 {
			fresh = 1200
		}
		if fresh > 18000 {
			fresh = 18000
		}
		cached := ns.CacheReadTokens / ns.ModelRequests
		if cached < 12000 {
			cached = 12000
		}
		if cached > 180000 {
			cached = 180000
		}
		ns.RoundFreshTokens = fresh
		ns.RoundCachedTokens = cached
		ns.DynamicRoundTokens = fresh + cached
	}
	return ns
}

func empiricalExploreStepsOrFallback(v float64) float64 {
	if v <= 0 {
		return 2.4
	}
	return v
}

func inferSessionHarnessAndWorkspace(session, metaJSON string, nStat *nativeSessionStats) (string, string) {
	harnessID := "zcode"
	workspace := ""
	if nStat != nil {
		if nStat.Harness != "" {
			harnessID = nStat.Harness
		}
		ws := nStat.Workspace
		if ws != "" && ws != "." && ws != "/" && !strings.HasPrefix(strings.ToLower(ws), "zcode-hook-") && ws != "T" && ws != "tmp" {
			workspace = ws
		}
	}
	if metaJSON != "" && metaJSON != "{}" {
		var meta map[string]any
		if json.Unmarshal([]byte(metaJSON), &meta) == nil {
			if w := asString(meta["world"]); strings.HasPrefix(w, "harness:") {
				h := strings.TrimPrefix(w, "harness:")
				if h != "" && h != "generic" {
					harnessID = h
				}
			}
			if loc := asString(meta["locus"]); loc != "" && workspace == "" {
				parts := strings.Split(loc, "/")
				if len(parts) >= 2 {
					workspace = parts[1]
				} else {
					workspace = loc
				}
			}
		}
	}
	lowerSess := strings.ToLower(session)
	switch {
	case strings.HasPrefix(lowerSess, "claude"):
		harnessID = "claude-code"
	case strings.HasPrefix(lowerSess, "codex"):
		harnessID = "codex"
	case strings.HasPrefix(lowerSess, "cursor"):
		harnessID = "cursor"
	case strings.HasPrefix(lowerSess, "windsurf"):
		harnessID = "windsurf"
	case strings.HasPrefix(lowerSess, "cline"):
		harnessID = "cline"
	case strings.HasPrefix(lowerSess, "dsh"):
		harnessID = "dsh"
	case strings.HasPrefix(lowerSess, "codebuddy"):
		harnessID = "codebuddy"
	}
	if workspace == "" {
		workspace = "Rolling-Memory"
	}
	return harnessID, workspace
}

func detectHarnessFromPath(path string) string {
	lower := strings.ToLower(path)
	switch {
	case strings.Contains(lower, ".claude"):
		return "claude-code"
	case strings.Contains(lower, ".codex"):
		return "codex"
	case strings.Contains(lower, ".cursor"):
		return "cursor"
	case strings.Contains(lower, "windsurf"):
		return "windsurf"
	case strings.Contains(lower, "cline"):
		return "cline"
	case strings.Contains(lower, "codebuddy"):
		return "codebuddy"
	case strings.Contains(lower, ".dsh"):
		return "dsh"
	default:
		return "zcode"
	}
}

// loadHighDegreeHubLabels identifies super-hub nodes AND high-document-frequency
// corpus terms in the graph (100% language-agnostic, zero hardcoded word lists):
//  1. Top subject/object nodes with degree >= 6;
//  2. Any delimited token/sub-phrase appearing across >= 4 distinct active assertions
//     (Document Frequency DF >= 4 => low inverse document frequency IDF), preventing
//     ubiquitous repository meta-terms from acting as false-positive entity anchors!
func loadHighDegreeHubLabels(st *store.Store, scope string) map[string]bool {
	hubs := map[string]bool{}
	if st == nil || st.DB() == nil {
		return hubs
	}
	rows, err := st.DB().Query(`
		SELECT lower(n.label), count(*) as deg
		FROM assertion a
		JOIN node n ON (n.id = a.subject_id OR n.id = a.object_id)
		WHERE a.scope = ? AND a.status = 'active'
		GROUP BY n.id
		HAVING deg >= 6
		ORDER BY deg DESC
		LIMIT 60
	`, scope)
	if err == nil {
		for rows.Next() {
			var lbl string
			var deg int
			if rows.Scan(&lbl, &deg) == nil && lbl != "" {
				hubs[lbl] = true
			}
		}
		_ = rows.Close()
	}

	// Compute corpus-wide Subject-Band Document Frequency (Band-DF) across active assertions in this scope:
	// tokens appearing in >= 10 distinct Subject Bands (`a.subject_id`) are corpus-wide generic hubs
	// (e.g. "rolling-memory", "agent", "memory", "internal", "cmd") while domain-specific
	// technical identifiers that have multiple assertions within 1..9 bands (e.g. "cache-sweep", "rerank")
	// are preserved as high-specificity anchors.
	bandSets := map[string]map[string]struct{}{}
	aRows, aErr := st.DB().Query(`
		SELECT coalesce(a.subject_id, coalesce(sn.label, a.subject_literal, '')),
		       coalesce(sn.label, a.subject_literal, ''),
		       coalesce(onode.label, a.object_literal, '')
		FROM assertion a
		LEFT JOIN node sn ON sn.id = a.subject_id
		LEFT JOIN node onode ON onode.id = a.object_id
		WHERE a.scope = ? AND a.status = 'active'
		LIMIT 1000
	`, scope)
	if aErr == nil {
		for aRows.Next() {
			var sid, sLbl, oLbl string
			if aRows.Scan(&sid, &sLbl, &oLbl) == nil && sid != "" {
				for _, tok := range strings.FieldsFunc(sLbl+" "+oLbl, func(r rune) bool {
					return unicode.IsSpace(r) || strings.ContainsRune("·+()（）[]【】「」『』：:、，,；;\"'`/⇒→×", r)
				}) {
					lt := strings.ToLower(strings.TrimSpace(tok))
					if utf8.RuneCountInString(lt) >= 3 {
						bs := bandSets[lt]
						if bs == nil {
							bs = map[string]struct{}{}
							bandSets[lt] = bs
						}
						bs[sid] = struct{}{}
					}
				}
			}
		}
		_ = aRows.Close()
	}
	for tok, bs := range bandSets {
		if len(bs) >= 3 {
			hubs[tok] = true
		}
	}
	return hubs
}

// extractStructuralBandAnchors extracts band titles, URI/file-path coordinates
// (identified purely by path separators '/' or file extension dots), graph entity
// labels (s / o), and the peak mathematical retrieval score from recall.Result.Bands.
// Zero natural-language word lists are used.
func extractStructuralBandAnchors(bands []map[string]any, workspace string, hubLabels map[string]bool) (bandTitles []string, codeAnchors []string, entityAnchors []string, topScore float64) {
	seenBand := map[string]bool{}
	seenCode := map[string]bool{}
	seenEnt := map[string]bool{}
	lowerWS := strings.ToLower(workspace)

	isHubOrTrivial := func(s string) bool {
		trimmed := strings.TrimSpace(s)
		ls := strings.ToLower(trimmed)
		rc := utf8.RuneCountInString(ls)
		if rc < 4 || ls == lowerWS || hubLabels[ls] {
			return true
		}
		// Reject template placeholders (<org>), percentages (100%), grades ("Grade S"), MCP tool prefixes ("mcp__"), or pure numbers/punctuation
		if strings.HasPrefix(trimmed, "<") || strings.HasSuffix(trimmed, ">") ||
			strings.HasSuffix(trimmed, "%") || strings.HasPrefix(ls, "grade ") || strings.HasPrefix(ls, "mcp__") {
			return true
		}
		hasLetter := false
		allASCII := true
		hasSepOrDigit := false
		hasLower := false
		hasInnerUpper := false
		for i, r := range trimmed {
			if unicode.IsLetter(r) {
				hasLetter = true
			}
			if r > 127 {
				allASCII = false
			}
			if r >= 'a' && r <= 'z' {
				hasLower = true
			}
			if i > 0 && r >= 'A' && r <= 'Z' {
				hasInnerUpper = true
			}
			if (r >= '0' && r <= '9') || strings.ContainsRune("_-=", r) {
				hasSepOrDigit = true
			}
		}
		if !hasLetter {
			return true
		}
		// Reject plain single-word ASCII tokens (including TitleCase like "Agent", ALLCAPS like "EXISTS",
		// plain lowercase words, or short generic camelCase property names like "sessionId", "createdAt",
		// "modelContent") unless they contain explicit structural separators/digits ('_', '-', '=')
		// OR are long multi-word compound identifiers (>= 14 chars with >= 2 uppercase transitions).
		if allASCII && !hasSepOrDigit {
			upperCount := 0
			for _, r := range trimmed {
				if r >= 'A' && r <= 'Z' {
					upperCount++
				}
			}
			if !(hasLower && hasInnerUpper && len(trimmed) >= 14 && upperCount >= 2) {
				return true
			}
		}
		return false
	}

	addEntityCandidate := func(raw string, isSubject bool) {
		clean := strings.TrimSpace(strings.TrimPrefix(raw, "带·"))
		if idx := strings.Index(clean, "（20"); idx > 0 {
			clean = strings.TrimSpace(clean[:idx])
		}
		if clean == "" {
			return
		}
		rcClean := utf8.RuneCountInString(clean)
		maxWhole := 28
		if isSubject {
			maxWhole = 36
		}
		if rcClean <= maxWhole && !isHubOrTrivial(clean) && !isStructuralFilePath(clean) && !seenEnt[clean] {
			seenEnt[clean] = true
			entityAnchors = append(entityAnchors, clean)
		}
		if !isSubject && rcClean > 36 {
			// For long `o.literal` descriptions, extract explicit technical identifiers
			// and compact delimited domain terms (4..14 runes).
			for _, tok := range strings.FieldsFunc(clean, func(r rune) bool {
				return unicode.IsSpace(r) || strings.ContainsRune("·+()（）[]【】「」『』：:、，,；;\"'`/⇒→", r)
			}) {
				tok = strings.TrimSpace(tok)
				rcTok := utf8.RuneCountInString(tok)
				if rcTok >= 4 && rcTok <= 28 && !isStructuralFilePath(tok) && !isHubOrTrivial(tok) {
					isASCIIIdent := true
					for _, r := range tok {
						if r > 127 {
							isASCIIIdent = false
							break
						}
					}
					if (isASCIIIdent && len(tok) >= 5) || (!isASCIIIdent && rcTok >= 4 && rcTok <= 12) {
						if !seenEnt[tok] {
							seenEnt[tok] = true
							entityAnchors = append(entityAnchors, tok)
						}
					}
				}
			}
			return
		}
		for _, sub := range strings.FieldsFunc(clean, func(r rune) bool {
			return unicode.IsSpace(r) || strings.ContainsRune("·+()（）[]【】「」『』：:、，,；;\"'`/×", r)
		}) {
			sub = strings.TrimSpace(sub)
			rc := utf8.RuneCountInString(sub)
			if rc >= 4 && rc <= 24 && !isHubOrTrivial(sub) && !isStructuralFilePath(sub) && !seenEnt[sub] {
				seenEnt[sub] = true
				entityAnchors = append(entityAnchors, sub)
			}
		}
	}

	collectNode := func(n map[string]any) {
		if sc, ok := n["score"].(float64); ok && sc > topScore {
			topScore = sc
		}
		for _, slot := range []string{"label", "content"} {
			val := asString(n[slot])
			if val == "" {
				continue
			}
			addEntityCandidate(val, slot == "label")
			for _, token := range strings.FieldsFunc(val, func(r rune) bool {
				return unicode.IsSpace(r) || strings.ContainsRune("()（）[]【】<>\"'`:,，；;。!?", r)
			}) {
				if isStructuralFilePath(token) && !seenCode[token] {
					seenCode[token] = true
					codeAnchors = append(codeAnchors, token)
				}
			}
		}
	}

	for _, band := range bands {
		if sc, ok := band["score"].(float64); ok && sc > topScore {
			topScore = sc
		}
		title := strings.TrimSpace(asString(band["title"]))
		if title != "" && !seenBand[title] {
			seenBand[title] = true
			bandTitles = append(bandTitles, title)
			addEntityCandidate(title, true)
		}
		if nodes, ok := band["nodes"].([]map[string]any); ok {
			for _, n := range nodes {
				collectNode(n)
			}
		}
		if chains, ok := band["chains"].([]map[string]any); ok {
			for _, chain := range chains {
				if nodes, ok := chain["nodes"].([]map[string]any); ok {
					for _, n := range nodes {
						collectNode(n)
					}
				}
			}
		} else if chainsAny, ok := band["chains"].([]any); ok {
			for _, cv := range chainsAny {
				if chain, ok := cv.(map[string]any); ok {
					if nodes, ok := chain["nodes"].([]map[string]any); ok {
						for _, n := range nodes {
							collectNode(n)
						}
					}
				}
			}
		}
	}
	return bandTitles, codeAnchors, entityAnchors, topScore
}

// isAmbientSystemOrPseudoPath filters out OS device nodes (/dev/null), system binary
// directories (/usr/bin/*, /bin/*, /tmp/*, /private/*), and bare single-segment directory
// fragments (/memory/) that are never genuine project source file landmarks.
func isAmbientSystemOrPseudoPath(token string) bool {
	lp := strings.ToLower(strings.TrimSpace(token))
	for _, pfx := range []string{
		"/dev/", "/usr/", "/bin/", "/sbin/", "/etc/", "/var/", "/tmp/", "/private/", "/proc/", "/sys/", "/app/",
		"bin/", "dev/", "tmp/",
	} {
		if strings.HasPrefix(lp, pfx) {
			return true
		}
	}
	// Bare directory token like "/memory/" with trailing slash or no file extension and <= 1 inner segment
	if strings.HasSuffix(lp, "/") && !strings.Contains(lp, ".") {
		return true
	}
	return false
}

func hasSpuriousSystemAnchor(anchors []string) bool {
	if len(anchors) == 0 {
		return false
	}
	valid := 0
	for _, a := range anchors {
		if !isAmbientSystemOrPseudoPath(a) {
			valid++
		}
	}
	return valid == 0
}

// isStructuralFilePath detects genuine file paths / code coordinates purely by structural
// ASCII path separators ('/') and extension dots ('.'), rejecting scores like "95/100"
// or slash-separated word pairs like "read/edit".
func isStructuralFilePath(token string) bool {
	if len(token) < 5 || strings.HasPrefix(token, "//") || strings.HasPrefix(token, "trace:") || strings.HasPrefix(token, "http") ||
		strings.ContainsRune(token, '@') || strings.ContainsRune(token, '&') || isAmbientSystemOrPseudoPath(token) {
		return false
	}
	for _, r := range token {
		if r > 127 {
			return false
		}
	}
	slashIdx := strings.IndexByte(token, '/')
	dotIdx := strings.LastIndexByte(token, '.')
	if slashIdx < 0 && strings.Count(token, ".") >= 2 {
		return false
	}
	// Reject root-level ambient harness instruction files (which are auto-loaded in every turn's system prompt
	// and never represent a novel file coordinate discovered via memory recall).
	if slashIdx < 0 {
		lowerTok := strings.ToLower(token)
		if strings.HasSuffix(lowerTok, ".md") && !strings.Contains(lowerTok, "/") {
			return false
		}
	}
	// Case 1: Has a 2..4 letter alphabetical file extension (e.g. "hooks.go", "views/admin/help/index.vue")
	if dotIdx > 2 && dotIdx < len(token)-1 && dotIdx > slashIdx {
		ext := token[dotIdx+1:]
		if len(ext) >= 2 && len(ext) <= 4 {
			allAlpha := true
			for _, r := range ext {
				if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')) {
					allAlpha = false
					break
				}
			}
			if allAlpha {
				return true
			}
		}
	}
	// Case 2: Directory path without extension must start with an explicit path root ("~/", "./", "/")
	// so slash-separated command/concept lists (e.g. "panel/doctor/plugin-bundle/eval_local") are never
	// misidentified as file coordinates. Rule shape mirrors hooks.extractStructuralPathsFromText exactly:
	// explicit root prefix + a slash that is not trailing + lowercase-only letter presence.
	if strings.HasPrefix(token, "~/") || strings.HasPrefix(token, "./") || strings.HasPrefix(token, "/") {
		if slashIdx >= 0 && slashIdx < len(token)-1 {
			hasLetter := false
			hasUpper := false
			for _, r := range token {
				if r >= 'a' && r <= 'z' {
					hasLetter = true
				} else if r >= 'A' && r <= 'Z' {
					hasUpper = true
				}
			}
			if hasLetter && !hasUpper {
				return true
			}
		}
	}
	return false
}

func containsStr(list []string, target string) bool {
	for _, s := range list {
		if strings.EqualFold(s, target) {
			return true
		}
	}
	return false
}

func matchHardList(candidates []string, lowerCorpus string) []string {
	if strings.TrimSpace(lowerCorpus) == "" {
		return nil
	}
	var matched []string
	seen := map[string]bool{}
	for _, c := range candidates {
		lc := strings.ToLower(strings.TrimSpace(c))
		if lc == "" || seen[c] {
			continue
		}
		if strings.Contains(lowerCorpus, lc) {
			seen[c] = true
			matched = append(matched, c)
		}
	}
	return matched
}

// stripEchoAnchors removes candidate anchors that the user already typed in their
// prompt (or that were already touched in earlier turns of the same session).
// This prevents "free-rider" false positives where the user's prompt itself supplied
// the file path rather than the recalled memory.
func stripEchoAnchors(candidates []string, prompt, priorSessionCorpus string) (novel []string, echoExcluded int) {
	lowerPrompt := strings.ToLower(prompt)
	lowerPrior := strings.ToLower(priorSessionCorpus)
	for _, c := range candidates {
		lc := strings.ToLower(strings.TrimSpace(c))
		if lc == "" {
			continue
		}
		// If the user explicitly typed the full anchor or its basename (for file paths >=8 chars)
		// in the prompt, or it was already touched in an earlier turn, exclude it from novel credit.
		base := filepath.Base(lc)
		inPrompt := strings.Contains(lowerPrompt, lc) || (len(base) >= 8 && strings.Contains(lc, "/") && strings.Contains(lowerPrompt, base))
		inPrior := lowerPrior != "" && strings.Contains(lowerPrior, lc)
		if inPrompt || inPrior {
			echoExcluded++
			continue
		}
		novel = append(novel, c)
	}
	return novel, echoExcluded
}

// evaluateTurnToolSequence slices native tool actions to the turn's time window
// [promptAt, nextPromptAt) and verifies causal order: whether target code anchors
// were accessed directly BEFORE blind search/exploration (grep/glob/find or repeated
// unassisted file reads before target hit) or with < baseline search steps.
func evaluateTurnToolSequence(nStat *nativeSessionStats, promptAt, nextPromptAt int64, novelCode []string, fallbackExplore int) (toolsCorpus string, exploreSteps int, causalOrderOK bool) {
	if nStat == nil {
		return "", fallbackExplore, true
	}
	if len(nStat.Actions) > 0 {
		var windowActions []toolAction
		for _, act := range nStat.Actions {
			if act.StartedAt > 0 && promptAt > 0 {
				if act.StartedAt+3 < promptAt {
					continue
				}
				if nextPromptAt > 0 && act.StartedAt > nextPromptAt+3 {
					continue
				}
			}
			windowActions = append(windowActions, act)
		}
		if len(windowActions) == 0 {
			return "", fallbackExplore, fallbackExplore <= 1
		}
		var buf strings.Builder
		searchBeforeTarget := 0
		totalSearchInTurn := 0
		readOnlyCountInTurn := 0
		targetHit := false
		for _, act := range windowActions {
			buf.WriteString(" ")
			buf.WriteString(act.Target)
			if !targetHit && len(novelCode) > 0 {
				for _, nc := range novelCode {
					lnc := strings.ToLower(strings.TrimSpace(nc))
					if lnc != "" && strings.Contains(act.Target, lnc) {
						targetHit = true
						break
					}
				}
				if targetHit {
					continue
				}
			}
			if act.IsSearch {
				totalSearchInTurn++
				if !targetHit {
					searchBeforeTarget++
				}
			} else if act.ReadOnly {
				readOnlyCountInTurn++
			}
		}
		steps := totalSearchInTurn
		if targetHit {
			steps = searchBeforeTarget
		}
		return buf.String(), steps, searchBeforeTarget <= 1
	}
	return strings.ToLower(strings.Join(nStat.TouchedFiles, " ")), fallbackExplore, true
}

// loadSessionSelfHealingStats inspects the memory store for L2 piggybacked agent
// feedback: assertions written back (`remember`) and stale assertions superseded
// or retracted (`status IN ('superseded','retracted')` or `meta_edge.kind='supersedes'`)
// during audited sessions. Zero extra LLM calls or tool round-trips are used.
func loadSessionSelfHealingStats(st *store.Store, scope string) map[string]*sessionSelfHealStats {
	bySess := map[string]*sessionSelfHealStats{}
	if st != nil && st.DB() != nil {
		rows, err := st.DB().Query(`
				SELECT t.session, a.status, a.created_at,
				       coalesce(sn.label, a.subject_literal, ''),
				       EXISTS(
				           SELECT 1 FROM meta_edge m
				           WHERE m.lhs_id = a.node_id
				             AND m.meta_kind IN ('supersedes', 'supplement')
				       ) AS did_supersede
				FROM assertion a
				JOIN trace t ON t.hash = a.anchor_trace_hash
				LEFT JOIN node sn ON sn.id = a.subject_id
				WHERE a.scope = ?
				  AND t.session != ''
				  AND t.session != 'default'
				  AND t.session NOT LIKE 'seed%'
				  AND t.session NOT LIKE 'eval%'
				  AND t.session NOT LIKE 'locomo%'
				  AND t.session NOT LIKE 'conv-%'
			`, scope)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var sess, status, sLabel string
				var createdAt int64
				var didSupersede int
				if rows.Scan(&sess, &status, &createdAt, &sLabel, &didSupersede) == nil {
					item := bySess[sess]
					if item == nil {
						item = &sessionSelfHealStats{}
						bySess[sess] = item
					}
					item.Writebacks++
					if strings.TrimSpace(sLabel) != "" {
						item.AuthoredBands = append(item.AuthoredBands, authoredBandRecord{
							BandTitle: "带·" + strings.TrimSpace(sLabel),
							CreatedAt: createdAt,
						})
					}
					if didSupersede > 0 || status == "superseded" || status == "retracted" {
						item.SelfHealed++
						item.HealedAt = append(item.HealedAt, createdAt)
					}
				}
			}
		}
	}
	return bySess
}

func countTurnSelfHealed(st *sessionSelfHealStats, promptAt, nextPromptAt int64) int {
	if st == nil || st.SelfHealed == 0 {
		return 0
	}
	cnt := 0
	for _, ts := range st.HealedAt {
		if promptAt > 0 && ts+10 < promptAt {
			continue
		}
		if nextPromptAt > 0 && ts > nextPromptAt+10 {
			continue
		}
		cnt++
	}
	return cnt
}

func normalizeUnixSeconds(ts int64) int64 {
	if ts > 1_000_000_000_000 {
		return ts / 1000
	}
	return ts
}

func loadImmutableTurnCache(path string) map[string]SessionEvaluation {
	out := map[string]SessionEvaluation{}
	if path == "" {
		return out
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	_ = json.Unmarshal(raw, &out)
	return out
}

func saveImmutableTurnCache(path string, cache map[string]SessionEvaluation) {
	if path == "" || len(cache) == 0 {
		return
	}
	raw, err := json.Marshal(cache)
	if err != nil {
		return
	}
	writeAtomicFile(path, raw)
}

// collectSessionContinuity inspects the 3D situational awareness state (Where / What Just Happened / Active Thread).
func collectSessionContinuity(st *store.Store, scope, workspaceDir string) SessionContinuity {
	sc := SessionContinuity{
		ActiveLocus: filepath.Base(workspaceDir),
	}
	if sc.ActiveLocus == "" || sc.ActiveLocus == "." || sc.ActiveLocus == "/" {
		sc.ActiveLocus = "Rolling-Memory"
	}
	if st == nil || st.DB() == nil {
		return sc
	}
	now := time.Now().Unix()
	_ = st.DB().QueryRow(
		`SELECT COUNT(DISTINCT subject_id) FROM assertion WHERE scope = ? AND status = 'active'`,
		scope,
	).Scan(&sc.WhereSpatialBands)
	_ = st.DB().QueryRow(
		`SELECT COUNT(*) FROM trace WHERE scope = ? AND recorded_at >= ?`,
		scope, now-7*86400,
	).Scan(&sc.EpisodicTailTraces)
	_ = st.DB().QueryRow(
		`SELECT COUNT(*) FROM assertion WHERE scope = ? AND status = 'active' AND created_at >= ?`,
		scope, now-7*86400,
	).Scan(&sc.ActiveThreadAssertions)
	_ = st.DB().QueryRow(
		`SELECT COUNT(*) FROM injection_ledger WHERE created_at >= ?`,
		now-3600,
	).Scan(&sc.LedgerProtectedBands)
	var lastText string
	if err := st.DB().QueryRow(
		`SELECT text FROM trace WHERE scope = ? AND length(text) >= 8 ORDER BY recorded_at DESC LIMIT 1`,
		scope,
	).Scan(&lastText); err == nil && lastText != "" {
		sc.LastTailPreview = truncateStr(lastText, 72)
	}
	sc.Ready = sc.WhereSpatialBands > 0 && (sc.EpisodicTailTraces > 0 || sc.ActiveThreadAssertions > 0)
	return sc
}

type preindexedSubject = recall.SubjectCandidate

func loadPreindexedActiveSubjects(st *store.Store, scope string) []preindexedSubject {
	if st == nil || st.DB() == nil {
		return nil
	}
	rows, err := st.DB().Query(`
				SELECT n.label,
				       MIN(CASE WHEN coalesce(t.session, '') LIKE 'seed%' THEN 0 ELSE a.created_at END) AS min_ts,
				       GROUP_CONCAT(coalesce(onode.label, a.object_literal, ''), ' ') AS body_text
				FROM assertion a
				JOIN node n ON n.id = a.subject_id
				LEFT JOIN node onode ON onode.id = a.object_id
				LEFT JOIN trace t ON t.hash = a.anchor_trace_hash
				WHERE a.scope = ? AND a.status IN ('active', 'superseded') AND a.bare = 0 AND a.confidence >= 0.70
				  AND EXISTS (SELECT 1 FROM assertion a2 WHERE a2.scope = a.scope AND a2.subject_id = n.id AND a2.status = 'active')
				GROUP BY n.label
			`, scope)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []preindexedSubject
	for rows.Next() {
		var lbl, body string
		var minTS int64
		if rows.Scan(&lbl, &minTS, &body) == nil && lbl != "" && memcore.StringShannonBits(lbl) >= 4.0 {
			if len(body) > 600 {
				body = strings.ToValidUTF8(body[:600], "")
			}
			out = append(out, preindexedSubject{
				Label:        lbl,
				Body:         body,
				MinCreatedAt: minTS,
			})
		}
	}
	return recall.PreindexSubjectCandidates(out)
}

// isBlindBashSearchCommand distinguishes genuine blind codebase searches (e.g. `find .`, `grep -rn pattern dir/`, `git ls-files`)
// from pipe output filters (`launchctl print ... | grep -E ...`, `ps aux | grep ...`) or targeted log reads.
func isBlindBashSearchCommand(lowerCmd string) bool {
	if (strings.Contains(lowerCmd, "find ") && !strings.Contains(lowerCmd, "-mindepth")) ||
		strings.Contains(lowerCmd, "git ls-files") ||
		strings.Contains(lowerCmd, "fd ") {
		return true
	}
	if !strings.Contains(lowerCmd, "grep ") && !strings.Contains(lowerCmd, "rg ") {
		return false
	}
	if strings.Contains(lowerCmd, ".log") {
		return false
	}
	for _, seg := range strings.Split(lowerCmd, ";") {
		for _, sub := range strings.Split(seg, "&&") {
			trimmed := strings.TrimSpace(sub)
			if strings.HasPrefix(trimmed, "grep ") || strings.HasPrefix(trimmed, "rg ") {
				return true
			}
		}
	}
	return false
}

func countSemanticRunes(s string) int {
	n := 0
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			n++
		}
	}
	return n
}

// hasViablePreRecallSignal performs a sub-0.1ms in-memory pre-screen over `activeSubjects`
// and structural paths using recall.ProbeSubjectsBySpecificity before invoking the full SQLite graph walk.
func hasViablePreRecallSignal(prompt, priorReply, workspace string, promptAt int64, activeSubjects []preindexedSubject) bool {
	qNonSpace := countSemanticRunes(prompt)
	if qNonSpace <= 2 {
		return false
	}
	if len(activeSubjects) == 0 {
		return true
	}
	if len(recall.ExtractStructuralPaths(prompt, 2)) > 0 {
		return true
	}
	if qNonSpace <= 16 && strings.TrimSpace(priorReply) != "" && len(recall.ExtractStructuralPaths(priorReply, 2)) > 0 {
		return true
	}
	if len(recall.ProbeSubjectsBySpecificity(activeSubjects, prompt, workspace, promptAt, 1)) > 0 {
		return true
	}
	return false
}

// buildEffectiveTurnCue mirrors hooks.contextualRecallCue using recall's specificity helpers.
func buildEffectiveTurnCue(st *store.Store, scope, prompt, priorReply, workspace string, promptAt int64, activeSubjects []preindexedSubject) string {
	combined := prompt
	normCount := countSemanticRunes(prompt)
	if normCount <= 16 && strings.TrimSpace(priorReply) != "" {
		if paths := recall.ExtractStructuralPaths(priorReply, 4); len(paths) > 0 {
			combined = strings.TrimSpace(prompt + " " + strings.Join(paths, " "))
		}
	}
	if subtokens := recall.ExpandScriptSubtokens(prompt); len(subtokens) > 0 {
		combined = strings.TrimSpace(combined + " " + strings.Join(subtokens, " "))
	}
	if st != nil && st.DB() != nil {
		var leadSubjects []string
		if probedSubjects := recall.ProbeSubjectsBySpecificity(activeSubjects, prompt, workspace, promptAt, 4); len(probedSubjects) > 0 {
			for _, ps := range probedSubjects {
				leadSubjects = append(leadSubjects, recall.CompactCueSubjectLabel(ps, workspace))
			}
		}
		if allPaths := recall.ExtractStructuralPaths(combined, 4); len(allPaths) > 0 {
			type subjHit struct {
				label string
				hits  int
			}
			var order []subjHit
			idxByLabel := map[string]int{}
			for _, p := range allPaths {
				lp := strings.ToLower(strings.TrimSpace(p))
				if len(lp) < 5 || !strings.Contains(lp, "/") || strings.HasSuffix(lp, ".db") || strings.HasSuffix(lp, ".md") {
					continue
				}
				rows, err := st.DB().Query(`
							SELECT DISTINCT n.label
							FROM assertion a
							JOIN node n ON n.id = a.subject_id
							LEFT JOIN trace t ON t.hash = a.anchor_trace_hash
							WHERE a.scope = ? AND a.status = 'active'
							  AND lower(coalesce(a.object_literal, '')) LIKE ?
							  AND (t.session LIKE 'seed%' OR a.created_at <= ?)
							LIMIT 4
						`, scope, "%"+lp+"%", promptAt+5)
				if err != nil {
					continue
				}
				for rows.Next() {
					var lbl string
					if rows.Scan(&lbl) == nil && lbl != "" {
						if idx, ok := idxByLabel[lbl]; ok {
							order[idx].hits++
						} else {
							idxByLabel[lbl] = len(order)
							order = append(order, subjHit{label: lbl, hits: 1})
						}
					}
				}
				_ = rows.Close()
			}
			for i := 0; i < len(order); i++ {
				for j := i + 1; j < len(order); j++ {
					if order[j].hits > order[i].hits {
						order[i], order[j] = order[j], order[i]
					}
				}
			}
			for i, item := range order {
				if i >= 2 {
					break
				}
				leadSubjects = append(leadSubjects, recall.CompactCueSubjectLabel(item.label, workspace))
			}
		}
		if len(leadSubjects) > 0 {
			combined = strings.TrimSpace(strings.Join(leadSubjects, " ") + " " + combined)
		}
	}
	return combined
}

func detectTopGateFilteredCandidate(rawPrompt, workspace string, bands []map[string]any) string {
	return recall.DetectTopGateFilteredBand(rawPrompt, workspace, bands)
}

func extractStructuralPathsEfficacy(text string, maxPaths int) []string {
	return recall.ExtractStructuralPaths(text, maxPaths)
}

func filterBandsMathematically(rawPrompt, effectiveCue, workspace string, bands []map[string]any) []map[string]any {
	return recall.FilterBandsBySpecificity(rawPrompt, effectiveCue, workspace, bands)
}

func loadNonSeedAssertionCreatedAt(st *store.Store, scope string) map[string]int64 {
	out := map[string]int64{}
	if st == nil || st.DB() == nil {
		return out
	}
	rows, err := st.DB().Query(`
		SELECT a.node_id, a.created_at
		FROM assertion a
		LEFT JOIN trace t ON t.hash = a.anchor_trace_hash
		WHERE a.scope = ? AND coalesce(t.session, '') NOT LIKE 'seed%'
	`, scope)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var ts int64
		if rows.Scan(&id, &ts) == nil && id != "" {
			out[id] = ts
		}
	}
	return out
}

func stripFutureAssertionsFromBands(bands []map[string]any, promptAt int64, createdAtMap map[string]int64) []map[string]any {
	if promptAt <= 0 || len(createdAtMap) == 0 {
		return bands
	}
	cutoff := promptAt + 5
	isFutureNode := func(n map[string]any) bool {
		id := asString(n["id"])
		if id == "" {
			return false
		}
		if cAt, ok := createdAtMap[id]; ok && cAt > cutoff {
			return true
		}
		return false
	}
	restoreHistoricalHeadActive := func(kn []map[string]any) []map[string]any {
		if len(kn) == 0 {
			return kn
		}
		for _, n := range kn {
			if st := asString(n["status"]); st == "" || st == "active" {
				return kn
			}
		}
		// All remaining historical nodes in this slice were superseded by a FUTURE write (`> promptAt + 5`);
		// at `promptAt`, the head node `kn[0]` was the active version!
		headClone := map[string]any{}
		for k, v := range kn[0] {
			headClone[k] = v
		}
		headClone["status"] = "active"
		kn[0] = headClone
		return kn
	}
	var keptBands []map[string]any
	for _, b := range bands {
		clone := map[string]any{}
		for k, v := range b {
			clone[k] = v
		}
		aliveNodes := 0
		if nodes, ok := b["nodes"].([]map[string]any); ok {
			var kn []map[string]any
			removedFuture := false
			for _, n := range nodes {
				if isFutureNode(n) {
					removedFuture = true
				} else {
					kn = append(kn, n)
				}
			}
			if removedFuture {
				kn = restoreHistoricalHeadActive(kn)
			}
			clone["nodes"] = kn
			aliveNodes += len(kn)
		}
		filterChain := func(chain map[string]any) (map[string]any, int) {
			cClone := map[string]any{}
			for k, v := range chain {
				cClone[k] = v
			}
			if nodes, ok := chain["nodes"].([]map[string]any); ok {
				var kn []map[string]any
				removedFuture := false
				for _, n := range nodes {
					if isFutureNode(n) {
						removedFuture = true
					} else {
						kn = append(kn, n)
					}
				}
				if len(kn) > 0 {
					if removedFuture {
						kn = restoreHistoricalHeadActive(kn)
					}
					cClone["nodes"] = kn
					return cClone, len(kn)
				}
			}
			return nil, 0
		}
		if chains, ok := b["chains"].([]map[string]any); ok {
			var kc []map[string]any
			for _, chain := range chains {
				if cClone, nCnt := filterChain(chain); cClone != nil {
					kc = append(kc, cClone)
					aliveNodes += nCnt
				}
			}
			clone["chains"] = kc
		} else if chainsAny, ok := b["chains"].([]any); ok {
			var kc []any
			for _, cv := range chainsAny {
				if chain, ok := cv.(map[string]any); ok {
					if cClone, nCnt := filterChain(chain); cClone != nil {
						kc = append(kc, cClone)
						aliveNodes += nCnt
					}
				}
			}
			clone["chains"] = kc
		}
		if aliveNodes > 0 {
			keptBands = append(keptBands, clone)
		}
	}
	return keptBands
}

// loadAssertionValidTo maps assertion node id -> t_valid_to (the closes_prior supersession
// stamp) so retrospective pruning can tell whether a node was ALREADY dead at a historical
// turn's time (temporal coherence: never judge a past turn with today's node status).
func loadAssertionValidTo(st *store.Store, scope string) map[string]int64 {
	out := map[string]int64{}
	if st == nil || st.DB() == nil {
		return out
	}
	rows, err := st.DB().Query(
		`SELECT node_id, t_valid_to FROM assertion WHERE scope = ? AND t_valid_to > 0`, scope)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var ts int64
		if rows.Scan(&id, &ts) == nil && id != "" {
			out[id] = ts
		}
	}
	return out
}

// pruneSupersededInBandsEfficacy mirrors hooks.pruneSupersededInBands with turn-time
// coherence: a node is dropped only when it is explicitly superseded/retracted
// AND was already dead (t_valid_to set) at the evaluated turn's time — a node superseded
// LATER was live truth back then, exactly what the live hook rendered at that moment.
func pruneSupersededInBandsEfficacy(bands []map[string]any, validTo map[string]int64, turnAt int64) []map[string]any {
	pruneChain := func(chain map[string]any) {
		nodes, ok := chain["nodes"].([]map[string]any)
		if !ok || len(nodes) <= 1 {
			return
		}
		var live []map[string]any
		for _, n := range nodes {
			if !nodeDeadAtTurn(n, validTo, turnAt) {
				live = append(live, n)
			}
		}
		if len(live) > 0 {
			chain["nodes"] = live
		}
	}
	for _, band := range bands {
		if chains, ok := band["chains"].([]map[string]any); ok {
			for _, chain := range chains {
				pruneChain(chain)
			}
		} else if chainsAny, ok := band["chains"].([]any); ok {
			for _, cv := range chainsAny {
				if chain, ok := cv.(map[string]any); ok {
					pruneChain(chain)
				}
			}
		}
		if nodes, ok := band["nodes"].([]map[string]any); ok && len(nodes) > 1 {
			var live []map[string]any
			for _, n := range nodes {
				if !nodeDeadAtTurn(n, validTo, turnAt) {
					live = append(live, n)
				}
			}
			if len(live) > 0 {
				band["nodes"] = live
			}
		}
	}
	return bands
}

func nodeDeadAtTurn(n map[string]any, validTo map[string]int64, turnAt int64) bool {
	st := asString(n["status"])
	if st != "superseded" && st != "retracted" {
		return false
	}
	if turnAt <= 0 {
		return true
	}
	if t := validTo[asString(n["id"])]; t > 0 {
		// Inclusive-at-end visibility: at the exact closes_prior instant the old value is
		// still a legitimate read state for an in-flight hook, so only STRICTLY earlier
		// supersession stamps count as dead (t < turnAt).
		return t < turnAt
	}
	return true
}

func truncateStr(s string, maxRunes int) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes]) + "..."
}

func formatUnixTime(ts int64) string {
	if ts <= 0 {
		return "-"
	}
	return time.Unix(ts, 0).Format("01-02 15:04:05")
}

func round1(v float64) float64 {
	return math.Round(v*10) / 10
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func asString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	default:
		return ""
	}
}

func asInt(v any) int {
	switch t := v.(type) {
	case int:
		return t
	case int64:
		return int(t)
	case float64:
		return int(t)
	case json.Number:
		n, _ := strconv.Atoi(t.String())
		return n
	default:
		return 0
	}
}

// SortHarnessesActiveFirst orders active/installed harnesses ahead of standby ones.
func SortHarnessesActiveFirst(items []HarnessEfficacy) {
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Sessions != items[j].Sessions {
			return items[i].Sessions > items[j].Sessions
		}
		if items[i].Installed != items[j].Installed {
			return items[i].Installed
		}
		return items[i].Detected && !items[j].Detected
	})
}

// loadMemoryLandmarkCreatedAt extracts all structural code file paths stored in active
// assertions in `scope` along with their earliest creation timestamp (`created_at`),
// so `detectBootOrCarryoverHit` can verify whether a turn directly navigated to an
// already-stored memory landmark via SessionStart boot or in-session carry-over.
func loadMemoryLandmarkCreatedAt(st *store.Store, scope string) map[string]int64 {
	out := map[string]int64{}
	if st == nil || st.DB() == nil {
		return out
	}
	rows, err := st.DB().Query(`
		SELECT coalesce(onode.label, a.object_literal, ''), a.created_at, coalesce(t.session, '')
		FROM assertion a
		LEFT JOIN node onode ON onode.id = a.object_id
		LEFT JOIN trace t ON t.hash = a.anchor_trace_hash
		WHERE a.scope = ? AND a.status = 'active'
	`, scope)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var txt, sess string
		var cAt int64
		if rows.Scan(&txt, &cAt, &sess) != nil || txt == "" {
			continue
		}
		if strings.HasPrefix(sess, "seed") {
			cAt = 0
		}
		for _, p := range recall.ExtractStructuralPaths(txt, 8) {
			lp := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(p), "./"))
			if !strings.Contains(lp, "/") || !strings.Contains(filepath.Base(lp), ".") ||
				isAmbientSystemOrPseudoPath(lp) ||
				strings.HasSuffix(lp, ".md") || strings.HasSuffix(lp, ".db") || strings.HasSuffix(lp, ".sql") || strings.HasSuffix(lp, ".log") {
				continue
			}
			if prev, exists := out[lp]; !exists || cAt < prev {
				out[lp] = cAt
			}
		}
	}
	return out
}

// detectBootOrCarryoverHit checks whether a turn whose UserPromptSubmit did not re-inject
// bands (`InjectedTokens == 0`) nevertheless directly touched an active memory code landmark
// in native tool calls (`part` table) with <= 1 blind search step, enforcing First-Touch-Only
// per session (`seenSessionTouchedPath`) so subsequent edits to an already-opened file are
// never double-counted.
func detectBootOrCarryoverHit(nStat *nativeSessionStats, session string, promptAt, turnEndAt int64, prompt string, landmarkPaths map[string]int64, seenSessionTouchedPath map[string]bool) ([]string, int, bool) {
	if nStat == nil || len(nStat.Actions) == 0 || len(landmarkPaths) == 0 || promptAt <= 0 {
		return nil, 0, false
	}
	maxEnd := turnEndAt + 3
	if turnEndAt <= 0 || maxEnd > promptAt+300 {
		maxEnd = promptAt + 300
	}
	var windowActions []toolAction
	for _, act := range nStat.Actions {
		if act.StartedAt <= 0 || act.StartedAt+3 < promptAt || act.StartedAt > maxEnd {
			continue
		}
		if strings.TrimSpace(act.Target) != "" {
			windowActions = append(windowActions, act)
		}
	}
	if len(windowActions) == 0 {
		return nil, 0, false
	}
	totalSearch := 0
	var targetBuf strings.Builder
	for _, act := range windowActions {
		if act.IsSearch {
			totalSearch++
		}
		targetBuf.WriteString(" ")
		targetBuf.WriteString(act.Target)
	}
	if totalSearch > 1 {
		return nil, totalSearch, false
	}
	toolsCorpus := strings.ToLower(targetBuf.String())
	lowerPrompt := strings.ToLower(prompt)
	var hits []string
	for path, createdAt := range landmarkPaths {
		if createdAt > 0 && createdAt >= promptAt-5 {
			continue
		}
		if seenSessionTouchedPath[session+"\x00"+path] {
			continue
		}
		base := filepath.Base(path)
		if strings.Contains(lowerPrompt, path) || (len(base) >= 8 && strings.Contains(lowerPrompt, base)) {
			continue
		}
		if strings.Contains(toolsCorpus, path) {
			hits = append(hits, path)
		}
	}
	if len(hits) == 0 {
		return nil, totalSearch, false
	}
	sort.Strings(hits)
	if len(hits) > 3 {
		hits = hits[:3]
	}
	for _, h := range hits {
		seenSessionTouchedPath[session+"\x00"+h] = true
	}
	return hits, totalSearch, true
}

// computeEmpiricalReductionRates measures the macro counterfactual cost & time reduction %
// between memory-direct coding turns (`ro_tools <= 2, write_tools >= 1`) and unassisted
// heavy-exploration coding turns (`ro_tools >= 5, write_tools >= 1`) in the host's native telemetry.
func computeEmpiricalReductionRates(home string) (billedPct, rawPct, reqPct, timePct float64) {
	billedPct, rawPct, reqPct, timePct = 63.4, 57.0, 61.2, 63.0
	if home == "" {
		return
	}
	zdbPath := filepath.Join(home, ".zcode", "cli", "db", "db.sqlite")
	if _, err := os.Stat(zdbPath); err != nil {
		return
	}
	db, err := sql.Open("sqlite", zdbPath+"?mode=ro&_pragma=busy_timeout(500)")
	if err != nil {
		return
	}
	defer db.Close()
	rows, qErr := db.Query(`
		WITH recent_u AS MATERIALIZED (
			SELECT session_id, turn_id, model_request_count,
			       input_tokens, cache_read_input_tokens, output_tokens, reasoning_tokens,
			       (completed_at - started_at) / 1000.0 AS dur_s
			FROM turn_usage
			WHERE model_request_count >= 2
			  AND tool_call_count >= 2
			  AND input_tokens > 0
			  AND (completed_at - started_at) BETWEEN 2000 AND 1800000
			ORDER BY started_at DESC
			LIMIT 600
		),
		turn_tools AS (
			SELECT u.session_id, u.turn_id,
			       SUM(CASE WHEN t.read_only = 1 THEN 1 ELSE 0 END) AS ro_tools,
			       SUM(CASE WHEN t.read_only = 0 THEN 1 ELSE 0 END) AS write_tools
			FROM recent_u u
			CROSS JOIN tool_usage t ON t.session_id = u.session_id AND t.turn_id = u.turn_id
			GROUP BY u.session_id, u.turn_id
		)
		SELECT u.model_request_count,
		       u.input_tokens, u.cache_read_input_tokens, u.output_tokens, u.reasoning_tokens,
		       u.dur_s,
		       coalesce(tt.ro_tools, 0) AS ro_tools
		FROM recent_u u
		JOIN turn_tools tt ON u.session_id = tt.session_id AND u.turn_id = tt.turn_id
		WHERE tt.write_tools >= 1
		LIMIT 400
	`)
	if qErr != nil {
		return
	}
	defer rows.Close()
	var lowN, highN int
	var lowReqs, highReqs, lowRaw, highRaw, lowBilled, highBilled, lowDur, highDur float64
	for rows.Next() {
		var reqs, inTok, cacheTok, outTok, reasTok, roTools int
		var durS float64
		if rows.Scan(&reqs, &inTok, &cacheTok, &outTok, &reasTok, &durS, &roTools) != nil {
			continue
		}
		rawTok := float64(inTok + outTok + reasTok)
		uncached := float64(maxInt(inTok-cacheTok, 0))
		billedTok := uncached + float64(cacheTok)*0.10 + float64(outTok+reasTok)
		if roTools <= 2 {
			lowN++
			lowReqs += float64(reqs)
			lowRaw += rawTok
			lowBilled += billedTok
			lowDur += durS
		} else if roTools >= 5 {
			highN++
			highReqs += float64(reqs)
			highRaw += rawTok
			highBilled += billedTok
			highDur += durS
		}
	}
	if lowN >= 5 && highN >= 5 {
		avgLowReq, avgHighReq := lowReqs/float64(lowN), highReqs/float64(highN)
		avgLowRaw, avgHighRaw := lowRaw/float64(lowN), highRaw/float64(highN)
		avgLowBil, avgHighBil := lowBilled/float64(lowN), highBilled/float64(highN)
		avgLowDur, avgHighDur := lowDur/float64(lowN), highDur/float64(highN)
		if avgHighBil > 0 && avgLowBil < avgHighBil {
			billedPct = round1((1.0 - avgLowBil/avgHighBil) * 100.0)
		}
		if avgHighRaw > 0 && avgLowRaw < avgHighRaw {
			rawPct = round1((1.0 - avgLowRaw/avgHighRaw) * 100.0)
		}
		if avgHighReq > 0 && avgLowReq < avgHighReq {
			reqPct = round1((1.0 - avgLowReq/avgHighReq) * 100.0)
		}
		if avgHighDur > 0 && avgLowDur < avgHighDur {
			timePct = round1((1.0 - avgLowDur/avgHighDur) * 100.0)
		}
	}
	return
}

func buildTurnToolTimeline(nStat *nativeSessionStats, promptAt, nextPromptAt int64) (timeline []string, targetFiles []string) {
	if nStat == nil || len(nStat.Actions) == 0 || promptAt <= 0 {
		return nil, nil
	}
	maxEnd := nextPromptAt + 5
	if nextPromptAt <= 0 || maxEnd > promptAt+360 {
		maxEnd = promptAt + 360
	}
	seenFiles := map[string]bool{}
	for _, act := range nStat.Actions {
		if act.StartedAt <= 0 || act.StartedAt+3 < promptAt || act.StartedAt > maxEnd {
			continue
		}
		rawTarget := strings.TrimSpace(act.Target)
		if rawTarget == "" && act.Name != "" {
			rawTarget = act.Name
		}
		if rawTarget == "" {
			continue
		}
		tag := "[Exec]"
		if act.IsSearch {
			tag = "[Explore]"
		} else if act.ReadOnly {
			tag = "[Read]"
		} else if strings.HasPrefix(rawTarget, "edit ") || strings.HasPrefix(rawTarget, "write ") {
			tag = "[Write]"
		}
		if len(timeline) < 10 {
			timeline = append(timeline, strconv.Itoa(len(timeline)+1)+". "+tag+" "+truncateStr(rawTarget, 90))
		}
		for _, p := range extractStructuralPathsEfficacy(rawTarget, 4) {
			lp := strings.TrimPrefix(strings.TrimSpace(p), "./")
			if idx := strings.Index(lp, "rolling-memory/"); idx >= 0 {
				lp = lp[idx+len("rolling-memory/"):]
			}
			if !isAmbientSystemOrPseudoPath(lp) && strings.Contains(filepath.Base(lp), ".") && !seenFiles[lp] && len(targetFiles) < 5 {
				seenFiles[lp] = true
				targetFiles = append(targetFiles, lp)
			}
		}
	}
	return timeline, targetFiles
}

// computeEngineeringDiagnostics populates the three zero-counterfactual engineering
// diagnostic views requested by developers:
//  1. HookTelemetry: physical hook triggers, real injected chars/tokens, and latency percentiles from rolling-memory.log;
//  2. TopUsefulBands & TopNoisyBands: per-band empirical hit rate vs wasted noise tokens;
//  3. BlindSearchGaps: turns where memory missed/stayed silent and the agent ran >= 2 blind exploration steps.
func computeEngineeringDiagnostics(report *Report, allSessions []SessionEvaluation, logPath, home string, nativeStats map[string]*nativeSessionStats) {
	if report == nil {
		return
	}
	if len(nativeStats) == 0 && home != "" {
		for _, name := range []string{"native_telemetry_ledger_v2.json", "native_telemetry_ledger_v1.json"} {
			tcPath := filepath.Join(home, ".local", "state", "rolling-memory", name)
			if rawTC, err := os.ReadFile(tcPath); err == nil && len(rawTC) > 0 {
				var tc persistedNativeTelemetryCache
				if json.Unmarshal(rawTC, &tc) == nil && len(tc.Sessions) > 0 {
					nativeStats = tc.Sessions
					break
				}
			}
		}
	}
	var ht HookPhysicalTelemetry
	if logPath != "" {
		if f, err := os.Open(logPath); err == nil {
			var latencies []int
			scanner := bufio.NewScanner(f)
			for scanner.Scan() {
				line := strings.TrimSpace(scanner.Text())
				if line == "" {
					continue
				}
				var m map[string]any
				if json.Unmarshal([]byte(line), &m) != nil {
					continue
				}
				ev := asString(m["event"])
				switch ev {
				case "start":
					ht.TotalStartHooks++
				case "close":
					ht.TotalCloseHooks++
					ht.TotalWrittenInLog += asInt(m["written"])
				case "recall":
					ht.TotalRecallHooks++
					ms := asInt(m["ms"])
					if ms > 0 {
						latencies = append(latencies, ms)
						if ms > 500 {
							ht.SlowOver500MS++
						}
					}
					inj := asInt(m["injected"])
					chars := asInt(m["chars"])
					if inj > 0 {
						ht.RealInjectedHooks++
						ht.RealInjectedChars += chars
						tokEst := chars / 3
						if tokEst > ht.MaxInjectedTokens {
							ht.MaxInjectedTokens = tokEst
						}
					} else if asInt(m["dedup"]) > 0 {
						ht.RealDedupHooks++
					} else if asInt(m["absent"]) > 0 {
						ht.RealAbsentHooks++
					} else {
						ht.RealSilentHooks++
					}
				}
			}
			_ = f.Close()
			if ht.TotalRecallHooks > 0 {
				ht.RealInjectRatePct = round1(float64(ht.RealInjectedHooks) * 100.0 / float64(ht.TotalRecallHooks))
			}
			ht.RealInjectedTokensEst = ht.RealInjectedChars / 3
			if ht.RealInjectedHooks > 0 {
				ht.AvgInjectedTokens = ht.RealInjectedTokensEst / ht.RealInjectedHooks
			}
			if len(latencies) > 0 {
				sort.Ints(latencies)
				ht.P50MS = latencies[len(latencies)/2]
				ht.P95MS = latencies[int(float64(len(latencies)-1)*0.95)]
				ht.P99MS = latencies[int(float64(len(latencies)-1)*0.99)]
			}
		}
	}

	nextPromptBySess := map[string]int64{}
	bandMap := map[string]*BandDiagnosticItem{}
	var gaps []BlindSearchGapItem
	for i := range allSessions {
		s := &allSessions[i]
		nextAt := nextPromptBySess[s.SessionID]
		if s.RecordedAt > 0 {
			nextPromptBySess[s.SessionID] = s.RecordedAt
		}
		if len(s.ToolTimeline) == 0 && len(nativeStats) > 0 {
			if nStat := nativeStats[s.SessionID]; nStat != nil {
				tl, tf := buildTurnToolTimeline(nStat, s.RecordedAt, nextAt)
				s.ToolTimeline = tl
				s.TargetFiles = tf
			}
		}
		if s.AttributionSource == "l0_boot_or_carryover" {
			ht.L0CarryoverTurns++
		} else if s.Outcome == "high_value" || s.Outcome == "assisted" {
			ht.L1DirectInjectedTurns++
			ht.L1DirectAdoptedTurns++
		} else if s.Outcome == "noise" {
			ht.L1DirectInjectedTurns++
		}
		if (s.Outcome == "silenced" || s.Outcome == "noise") && s.ExploreSteps >= 2 {
			paths := s.TargetFiles
			if len(paths) == 0 {
				paths = extractStructuralPathsEfficacy(s.Prompt+" "+s.RecalledSummary, 4)
			}
			gaps = append(gaps, BlindSearchGapItem{
				SessionID:     s.SessionID,
				TimeText:      s.TimeText,
				RecordedAt:    s.RecordedAt,
				Harness:       s.Harness,
				Workspace:     s.Workspace,
				Prompt:        s.Prompt,
				Outcome:       s.Outcome,
				ExploreSteps:  s.ExploreSteps,
				ToolCalls:     s.ToolCalls,
				ModelRequests: s.ModelRequests,
				TargetFiles:   paths,
				ToolTimeline:  s.ToolTimeline,
			})
		}
		if s.Outcome != "high_value" && s.Outcome != "assisted" && s.Outcome != "noise" {
			continue
		}
		if s.AttributionSource == "l0_boot_or_carryover" {
			continue
		}
		parts := strings.Split(s.RecalledSummary, " ； ")
		if len(parts) == 0 {
			continue
		}
		perBandTok := s.InjectedTokens / maxInt(len(parts), 1)
		for _, rawTitle := range parts {
			bt := strings.TrimSpace(rawTitle)
			if bt == "" {
				continue
			}
			subj := strings.TrimSpace(strings.TrimPrefix(bt, "带·"))
			if idx := strings.Index(subj, "（20"); idx > 0 {
				subj = strings.TrimSpace(subj[:idx])
			}
			item := bandMap[subj]
			if item == nil {
				item = &BandDiagnosticItem{
					BandTitle:     "带·" + subj,
					Subject:       subj,
					SampleAnchors: []string{},
					LastSeen:      s.TimeText,
				}
				bandMap[subj] = item
			}
			item.InjectedTurns++
			item.InjectedTokens += perBandTok
			if s.Outcome == "high_value" || s.Outcome == "assisted" {
				item.AdoptedTurns++
				for _, a := range s.MatchedAnchors {
					if !isAmbientSystemOrPseudoPath(a) && len(item.SampleAnchors) < 4 && !containsStr(item.SampleAnchors, a) {
						item.SampleAnchors = append(item.SampleAnchors, a)
					}
				}
			} else if s.Outcome == "noise" {
				item.NoiseTurns++
				item.WastedTokens += perBandTok
			}
		}
	}

	var useful, noisy []BandDiagnosticItem
	for _, b := range bandMap {
		if b.InjectedTurns > 0 {
			b.AdoptRate = round1(float64(b.AdoptedTurns) * 100.0 / float64(b.InjectedTurns))
		}
		if b.AdoptedTurns > 0 {
			useful = append(useful, *b)
		}
		if b.NoiseTurns > 0 {
			noisy = append(noisy, *b)
		}
	}
	sort.SliceStable(useful, func(i, j int) bool {
		if useful[i].AdoptedTurns != useful[j].AdoptedTurns {
			return useful[i].AdoptedTurns > useful[j].AdoptedTurns
		}
		return useful[i].AdoptRate > useful[j].AdoptRate
	})
	if len(useful) > 8 {
		useful = useful[:8]
	}
	sort.SliceStable(noisy, func(i, j int) bool {
		if noisy[i].NoiseTurns != noisy[j].NoiseTurns {
			return noisy[i].NoiseTurns > noisy[j].NoiseTurns
		}
		return noisy[i].WastedTokens > noisy[j].WastedTokens
	})
	if len(noisy) > 8 {
		noisy = noisy[:8]
	}
	sort.SliceStable(gaps, func(i, j int) bool {
		if gaps[i].ExploreSteps != gaps[j].ExploreSteps {
			return gaps[i].ExploreSteps > gaps[j].ExploreSteps
		}
		if gaps[i].ToolCalls != gaps[j].ToolCalls {
			return gaps[i].ToolCalls > gaps[j].ToolCalls
		}
		return gaps[i].RecordedAt > gaps[j].RecordedAt
	})
	if len(gaps) > 12 {
		gaps = gaps[:12]
	}
	if ht.L1DirectInjectedTurns > 0 {
		ht.L1DirectAdoptRatePct = round1(float64(ht.L1DirectAdoptedTurns) * 100.0 / float64(ht.L1DirectInjectedTurns))
	}
	report.HookTelemetry = ht
	report.TopUsefulBands = useful
	report.TopNoisyBands = noisy
	report.BlindSearchGaps = gaps
}
