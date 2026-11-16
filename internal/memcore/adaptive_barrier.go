// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package memcore

import (
	"sort"
	"strings"
	"time"
)

// PendingTraceSummary summarizes an unencoded raw trace for query-aware barrier evaluation and scheduling.
type PendingTraceSummary struct {
	ID         uint64
	Session    string
	OccurredAt int64
	SearchText string
}

// SessionScheduleMeta holds metadata for ordering multi-session encoding queues.
type SessionScheduleMeta struct {
	Session   string
	TurnCount int
	MinAt     int64
	MaxAt     int64
	MinID     uint64
}

// AdaptiveBarrierSnapshot captures the real-time progress and relevance state of a scope during WaitEnriched.
type AdaptiveBarrierSnapshot struct {
	TotalPendingTraces     int
	RelevantPendingTraces  int
	InitialRelevantPending int
	AdvancedAny            bool
	HasActiveHeartbeat     bool
	Elapsed                time.Duration
	IdleSinceProgress      time.Duration
	BaseTimeout            time.Duration
	MaxTotalWait           time.Duration
	ClientSafeWindow       time.Duration
	VelocityTracesPerSec   float64
}

// AdaptiveBarrierDecision represents the convergence verdict of the query-aware enrich barrier.
type AdaptiveBarrierDecision struct {
	ShouldRelease bool
	TimedOut      bool
	Reason        string
}

// CountQueryRelevantPendingTraces inspects unencoded traces against the incoming search query tokens
// (pure formal lexical/statistical matching, 0 LLM, language-agnostic) to determine how many
// pending traces actually bear information relevant to the active query, and computes per-session priority weights.
func CountQueryRelevantPendingTraces(
	query string,
	pending []PendingTraceSummary,
	sessionCursors map[string]int64,
) (totalUnfinished int, relevantUnfinished int, sessionHitWeights map[string]int) {
	sessionHitWeights = make(map[string]int)
	if len(pending) == 0 {
		return 0, 0, sessionHitWeights
	}
	rawTokens := Tokenize(query)
	var salTokens []string
	var highWeightTokens []string
	seen := make(map[string]bool, len(rawTokens))
	for _, tk := range rawTokens {
		tl := strings.ToLower(strings.TrimSpace(tk))
		if tl == "" || seen[tl] {
			continue
		}
		bits := StringShannonBits(tl)
		if bits < 4.0 {
			continue
		}
		seen[tl] = true
		salTokens = append(salTokens, tl)
		if bits >= 6.5 || strings.Contains(tl, "_") {
			highWeightTokens = append(highWeightTokens, tl)
		}
	}

	for _, tr := range pending {
		if sessionCursors != nil && int64(tr.ID) <= sessionCursors[tr.Session] {
			continue
		}
		totalUnfinished++
		if len(salTokens) == 0 {
			relevantUnfinished++
			continue
		}
		lowText := strings.ToLower(tr.SearchText)
		if lowText == "" {
			continue
		}
		hitCnt := 0
		highHitCnt := 0
		weight := 0
		for _, tk := range salTokens {
			root := MorphRootPrefix(tk)
			matched := strings.Contains(lowText, tk) ||
				MatchTokenOrStemWithStem(lowText, tk, StemToken(tk), false) > 0 ||
				(root != "" && root != tk && StringShannonBits(root) >= AtomicConceptEntropyBits && ContainsWordBounded(lowText, root))
			if matched {
				hitCnt++
				if StringShannonBits(tk) >= 1.5*AtomicConceptEntropyBits || strings.Contains(tk, "_") {
					highHitCnt++
					weight += 3
				} else {
					weight += 1
				}
			}
		}
		isRel := false
		if len(salTokens) == 1 {
			isRel = hitCnt >= 1
		} else if highHitCnt >= 1 && hitCnt >= 2 {
			isRel = true
		} else if len(highWeightTokens) == 0 && hitCnt >= 2 {
			isRel = true
		} else if len(salTokens) >= 4 && hitCnt*3 >= len(salTokens) {
			isRel = true
		}
		if isRel {
			relevantUnfinished++
			sessionHitWeights[tr.Session] += weight
		}
	}
	return totalUnfinished, relevantUnfinished, sessionHitWeights
}

// EvaluateAdaptiveBarrierDecision determines whether the read-side enrich barrier can release early
// because all query-relevant traces have been encoded, or whether it should converge gracefully within
// the client-safe window before triggering upstream HTTP client timeouts.
func EvaluateAdaptiveBarrierDecision(snap AdaptiveBarrierSnapshot) AdaptiveBarrierDecision {
	if snap.TotalPendingTraces <= 0 {
		return AdaptiveBarrierDecision{
			ShouldRelease: true,
			Reason:        "all_traces_drained",
		}
	}
	baseTimeout := snap.BaseTimeout
	if baseTimeout <= 0 {
		baseTimeout = 60 * time.Second
	}
	maxWait := snap.MaxTotalWait
	if maxWait <= 0 {
		maxWait = 240 * time.Second
	}
	clientSafe := snap.ClientSafeWindow
	if clientSafe <= 0 {
		// Default client-safe convergence ceiling (90s) stays comfortably below typical 120s-140s agent tool HTTP timeouts.
		clientSafe = 90 * time.Second
	}

	// 1. Information-theoretic early release:
	//  - Case A (InitialRelevantPending > 0): the query explicitly matched pending traces at arrival and all of them
	//    have now been 100% encoded (RelevantPendingTraces == 0). If only a tiny tail (<= 12 traces) remains and can
	//    drain within a few seconds inside the safe window, let it finish cleanly; otherwise release immediately.
	//  - Case B (InitialRelevantPending == 0): the query has zero literal token overlap with pending traces (e.g.
	//    implicit preference queries like E1/E2 or abstract cross-session prompts). Do NOT release prematurely if the
	//    remaining scope is small/medium (<= 64 traces) or if the entire remaining scope can finish within the client-safe
	//    window at current velocity; only release early for massive background trajectories after warmup.
	if snap.RelevantPendingTraces == 0 && snap.AdvancedAny {
		canDrainAllBeforeSafe := snap.TotalPendingTraces <= 64
		if !canDrainAllBeforeSafe && snap.VelocityTracesPerSec > 0 {
			totalETASec := float64(snap.TotalPendingTraces) / snap.VelocityTracesPerSec
			remSafeSec := (clientSafe - snap.Elapsed).Seconds()
			if totalETASec+8.0 <= remSafeSec {
				canDrainAllBeforeSafe = true
			}
		}
		if snap.InitialRelevantPending > 0 {
			tinyTailCanFinish := (snap.TotalPendingTraces <= 12 || (canDrainAllBeforeSafe && snap.TotalPendingTraces <= 32)) &&
				snap.HasActiveHeartbeat && snap.Elapsed+15*time.Second < clientSafe
			if !tinyTailCanFinish {
				return AdaptiveBarrierDecision{
					ShouldRelease: true,
					Reason:        "query_relevant_traces_drained",
				}
			}
		} else if !canDrainAllBeforeSafe && snap.Elapsed >= 25*time.Second {
			return AdaptiveBarrierDecision{
				ShouldRelease: true,
				Reason:        "query_zero_pending_overlap_after_warmup",
			}
		}
	}

	// 2. Velocity & ETA-aware client-safe window convergence:
	// If we have reached the client-safe window and relevant traces have mostly drained (or remaining traces
	// cannot finish within a short grace margin at current velocity), release cleanly so the caller receives
	// the enriched graph without hitting a client-side HTTP disconnect.
	if snap.Elapsed >= clientSafe {
		if snap.VelocityTracesPerSec > 0 && snap.RelevantPendingTraces > 0 {
			etaSec := float64(snap.RelevantPendingTraces) / snap.VelocityTracesPerSec
			if etaSec <= 12.0 && snap.Elapsed < clientSafe+12*time.Second && snap.HasActiveHeartbeat {
				return AdaptiveBarrierDecision{ShouldRelease: false}
			}
		}
		return AdaptiveBarrierDecision{
			ShouldRelease: true,
			TimedOut:      false,
			Reason:        "client_safe_window_converged",
		}
	}

	// 3. Stagnation / hard ceiling guard
	if snap.Elapsed >= maxWait || (snap.Elapsed >= baseTimeout && snap.IdleSinceProgress >= baseTimeout && !snap.HasActiveHeartbeat) {
		return AdaptiveBarrierDecision{
			ShouldRelease: true,
			TimedOut:      true,
			Reason:        "barrier_stalled_or_max_wait",
		}
	}

	return AdaptiveBarrierDecision{ShouldRelease: false}
}

// OrderSessionsByQueryRelevanceAndSPT returns session indices ordered for multi-session encoding:
//  1. For small causal session sets (<= 4 sessions and total turns <= 80), strict chronological order is preserved;
//  2. For large multi-session workloads, sessions with higher query-token hit weights and shorter processing time (SPT)
//     are scheduled first so query-critical sessions finish encoding ahead of giant background trajectories.
func OrderSessionsByQueryRelevanceAndSPT(sessions []SessionScheduleMeta, sessionHitWeights map[string]int) []int {
	n := len(sessions)
	indices := make([]int, n)
	totalTurns := 0
	for i := range sessions {
		indices[i] = i
		totalTurns += sessions[i].TurnCount
	}
	if n <= 1 {
		return indices
	}
	hasQueryWeights := false
	for _, w := range sessionHitWeights {
		if w > 0 {
			hasQueryWeights = true
			break
		}
	}
	_ = hasQueryWeights
	if n <= 4 && totalTurns <= 80 {
		sort.SliceStable(indices, func(a, b int) bool {
			ia, ib := indices[a], indices[b]
			if sessions[ia].MinAt > 0 && sessions[ib].MinAt > 0 && sessions[ia].MinAt != sessions[ib].MinAt {
				return sessions[ia].MinAt < sessions[ib].MinAt
			}
			return sessions[ia].MinID < sessions[ib].MinID
		})
		return indices
	}

	sort.SliceStable(indices, func(a, b int) bool {
		ia, ib := indices[a], indices[b]
		wa, wb := 0, 0
		if sessionHitWeights != nil {
			wa = sessionHitWeights[sessions[ia].Session]
			wb = sessionHitWeights[sessions[ib].Session]
		}
		if wa != wb {
			return wa > wb
		}
		if sessions[ia].TurnCount != sessions[ib].TurnCount {
			return sessions[ia].TurnCount < sessions[ib].TurnCount
		}
		if sessions[ia].MinAt > 0 && sessions[ib].MinAt > 0 && sessions[ia].MinAt != sessions[ib].MinAt {
			return sessions[ia].MinAt < sessions[ib].MinAt
		}
		return sessions[ia].MinID < sessions[ib].MinID
	})
	return indices
}

const (
	// BaseAttentionTokenBudget Short-turn high-attention baseline budget (<= 1024 tokens).
	BaseAttentionTokenBudget = 1024
	// MaxWholeTurnTokenBudget Maximum token budget to keep a single medium/long turn intact in one working context.
	MaxWholeTurnTokenBudget = 4096
	// OversizedTurnSliceTarget Target slice token budget when a single extreme turn exceeds MaxWholeTurnTokenBudget.
	OversizedTurnSliceTarget = 2048
	// DefaultSliceOverlapTokens Sentence-boundary overlap tokens when an extreme turn (> 4096 tokens) is sliced.
	DefaultSliceOverlapTokens = 128
	// DefaultSliceMinTailTokens Minimum tail tokens before merging into the preceding slice.
	DefaultSliceMinTailTokens = 256
)

// AdaptiveTurnSliceSpec describes the slice and batch token budgets for a turn of given token count.
type AdaptiveTurnSliceSpec struct {
	ShouldSlice      bool
	SliceTarget      int
	OverlapTokens    int
	MinTailTokens    int
	BatchTokenBudget int
}

// ComputeAdaptiveTurnSliceSpec implements the whole-turn integrity adaptive sizing law:
//  1. If turnTokens <= BaseAttentionTokenBudget (1024): do not slice; pack multiple short turns up to 1024 tokens
//     to preserve peak attention density;
//  2. If 1024 < turnTokens <= MaxWholeTurnTokenBudget (4096): do not slice; adaptively expand the batch working context
//     budget to turnTokens so the working context always carries at least one complete whole turn without mid-turn splitting;
//  3. If turnTokens > MaxWholeTurnTokenBudget (4096): slice at sentence boundaries using OversizedTurnSliceTarget (2048)
//     and set BatchTokenBudget = MaxWholeTurnTokenBudget so focused head+tail slices of a single turn stay in one working context.
func ComputeAdaptiveTurnSliceSpec(turnTokens int) AdaptiveTurnSliceSpec {
	if turnTokens <= BaseAttentionTokenBudget {
		return AdaptiveTurnSliceSpec{
			ShouldSlice:      false,
			SliceTarget:      BaseAttentionTokenBudget,
			OverlapTokens:    DefaultSliceOverlapTokens,
			MinTailTokens:    DefaultSliceMinTailTokens,
			BatchTokenBudget: BaseAttentionTokenBudget,
		}
	}
	if turnTokens <= MaxWholeTurnTokenBudget {
		return AdaptiveTurnSliceSpec{
			ShouldSlice:      false,
			SliceTarget:      turnTokens,
			OverlapTokens:    DefaultSliceOverlapTokens,
			MinTailTokens:    DefaultSliceMinTailTokens,
			BatchTokenBudget: turnTokens,
		}
	}
	return AdaptiveTurnSliceSpec{
		ShouldSlice:      true,
		SliceTarget:      OversizedTurnSliceTarget,
		OverlapTokens:    DefaultSliceOverlapTokens,
		MinTailTokens:    DefaultSliceMinTailTokens,
		BatchTokenBudget: MaxWholeTurnTokenBudget,
	}
}

// ComputeAdaptiveContextBudget returns the single-turn cap and total context token budget given the immediately preceding
// turn's token count, ensuring the working context always carries at least one complete preceding turn (up to MaxWholeTurnTokenBudget)
// while keeping the 1024 high-attention baseline when the preceding turn is short.
func ComputeAdaptiveContextBudget(latestPrevTurnTokens int) (singleTurnCap int, totalBudget int) {
	if latestPrevTurnTokens <= BaseAttentionTokenBudget {
		return BaseAttentionTokenBudget, BaseAttentionTokenBudget
	}
	if latestPrevTurnTokens <= MaxWholeTurnTokenBudget {
		return latestPrevTurnTokens, latestPrevTurnTokens
	}
	return MaxWholeTurnTokenBudget, MaxWholeTurnTokenBudget
}
