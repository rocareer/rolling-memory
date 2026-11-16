// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package memcore

import (
	"strconv"
	"strings"
)

// Prospective Loop Axis (Axiom 9 — Orthogonal Loop State & Target-Time Zeigarnik Field):
//   - loop_state ∈ {'', 'open', 'fulfilled', 'waived', 'broken'} tracks prospective open loops.
//   - Write-side state transitions strictly follow explicit closure (closes_prior=true):
//     implicit cross-predicate evolution (e.g., "plan to go to Beijing" -> "arrived in Beijing")
//     is naturally represented by the [SO] same-object chronological chain (chain head = current state)
//     without guessing predicate entailment in code or mutating prior rows.
//   - Read-side Zeigarnik tension boost applies ONLY when an active open loop carries an explicit
//     resolved target/due timestamp (t_event > 0) within the bounded 14-day horizon, preventing
//     undated performative greetings/chitchat from outranking factual assertions.

const (
	LoopStateNone      = ""
	LoopStateOpen      = "open"
	LoopStateFulfilled = "fulfilled"
	LoopStateWaived    = "waived"
	LoopStateBroken    = "broken"
)

var validLoopStates = map[string]bool{
	LoopStateOpen:      true,
	LoopStateFulfilled: true,
	LoopStateWaived:    true,
	LoopStateBroken:    true,
}

// IsValidLoopState reports whether s is a valid non-empty loop_state value.
func IsValidLoopState(s string) bool {
	return validLoopStates[strings.ToLower(strings.TrimSpace(s))]
}

// InitialLoopState determines the initial loop_state on assertion write:
//   - If an explicit valid loopState is supplied, it is returned normalized;
//   - Otherwise, if warrant == "performative", it defaults to "open";
//   - Otherwise, it returns "" (retrospective fact).
func InitialLoopState(warrant, explicitLoopState string) string {
	norm := strings.ToLower(strings.TrimSpace(explicitLoopState))
	if validLoopStates[norm] {
		return norm
	}
	if strings.EqualFold(strings.TrimSpace(warrant), "performative") {
		return LoopStateOpen
	}
	return LoopStateNone
}

// ResolveClosedLoopState computes the target loop_state when a prior assertion is explicitly closed
// via closes_prior=true:
//   - If priorLoopState == "open":
//   - When the closing assertion has claim == "correction" (plan change / cancellation), it transitions to "waived";
//   - Otherwise it transitions to "fulfilled" (commitment kept / plan step completed);
//   - If priorLoopState is already a terminal state ("fulfilled", "waived", "broken"), it is preserved;
//   - If priorLoopState is "" (retrospective fact), it remains "".
func ResolveClosedLoopState(priorLoopState, closingClaim string) string {
	prior := strings.ToLower(strings.TrimSpace(priorLoopState))
	if prior == LoopStateOpen {
		if strings.EqualFold(strings.TrimSpace(closingClaim), "correction") {
			return LoopStateWaived
		}
		return LoopStateFulfilled
	}
	if validLoopStates[prior] {
		return prior
	}
	return LoopStateNone
}

// ZeigarnikLoopBoost computes the bounded bell-shaped Zeigarnik potential field multiplier (>= 1.0)
// for time-anchored prospective open loops during recall fitting:
//   - Requires loop_state == "open", active status, AND a resolved target/event timestamp tEvent > 0
//     (so undated performative greetings/chitchat never receive false boosts over factual assertions);
//   - Within the active horizon around anchorTs = max(tEvent, tSource) (characteristic window 14 days),
//     time-anchored open loops receive urgency boost (up to 1.35x near tEvent);
//   - If an open loop is never closed well past its horizon (now > anchorTs + 14 days),
//     the tension decays smoothly back toward 1.0 (preventing unclosed stale intentions from becoming zombie loops);
//   - Once resolved (fulfilled / waived / broken) or superseded, tension is immediately released (1.0).
func ZeigarnikLoopBoost(loopState, status string, tEvent, now int64, optTSource ...int64) float64 {
	ls := strings.ToLower(strings.TrimSpace(loopState))
	if ls != LoopStateOpen || tEvent <= 0 {
		return 1.0
	}
	st := strings.ToLower(strings.TrimSpace(status))
	if st != "" && st != "active" {
		return 1.0
	}
	var tSource int64
	if len(optTSource) > 0 {
		tSource = optTSource[0]
	}
	const (
		baseBoost    = 0.18
		dueBoost     = 0.17
		tauLeadSecs  = 7 * 86400.0  // 7-day lead ramp before t_event
		tauStaleSecs = 14 * 86400.0 // 14-day post-horizon decay envelope (no zombie loops)
	)
	if now <= 0 && tSource > 0 {
		now = tSource
	}
	gain := baseBoost
	if now > 0 {
		delta := float64(tEvent - now) // >0: before t_event; <0: past t_event
		if delta >= 0 {
			gain += dueBoost / (1.0 + delta/tauLeadSecs)
			// Far-future horizon envelope: if target event is more than 14 days in the future,
			// smoothly taper base tension so multi-year distant plans do not crowd out imminent commitments.
			if delta > tauStaleSecs {
				aheadOver := (delta - tauStaleSecs) / tauStaleSecs
				gain /= (1.0 + aheadOver*aheadOver)
			}
		} else {
			past := -delta
			gain += dueBoost / (1.0 + past/(tauLeadSecs*0.5))
		}
	}
	// Post-horizon natural envelope decay: if now is far past max(tEvent, tSource), smoothly decay gain -> 0
	anchorTs := tEvent
	if tSource > anchorTs {
		anchorTs = tSource
	}
	if anchorTs > 0 && now > anchorTs {
		elapsedPast := float64(now - anchorTs)
		if elapsedPast > tauStaleSecs {
			over := (elapsedPast - tauStaleSecs) / tauStaleSecs
			gain /= (1.0 + over*over)
		}
	}
	return 1.0 + gain
}

// FormatLoopStateTag returns the human/LLM-readable inline tag for resolved or explicitly tracked loop states.
func FormatLoopStateTag(loopState string, includeOpen bool) string {
	return FormatLoopStateTagFor(string(DeliveryLangZH), loopState, includeOpen)
}

// ClampSupersedeValidTo enforces the 4-temporal validity window stop-axis invariant when superseding an assertion:
//   - t_valid_to must be strictly positive (> 0) so that AsOf time-travel queries never treat a superseded row as an open-ended active fact;
//   - t_valid_to must be >= oldValidFrom (when oldValidFrom > 0) to prevent inverted validity windows (t_valid_to < t_valid_from).
func ClampSupersedeValidTo(validTo, oldValidFrom, now int64) int64 {
	vt := validTo
	if vt <= 0 {
		vt = now
	}
	if vt <= 0 {
		vt = 1
	}
	if oldValidFrom > 0 && vt < oldValidFrom {
		vt = oldValidFrom
	}
	return vt
}

// ResolveEffectiveValidFrom computes the validity window start timestamp (t_valid_from):
//   - When resolvedEventTime > 0:
//   - If warrant == "performative" (a prospective commitment/plan) and turnOccurredAt > 0
//     and resolvedEventTime > turnOccurredAt (a future target/due date) and explicitValidFrom <= 0,
//     the commitment itself is valid starting from when it was made (turnOccurredAt), while t_event
//     holds the future due date;
//   - Otherwise t_valid_from = resolvedEventTime;
//   - When resolvedEventTime <= 0: falls back to explicitValidFrom (clamped >= 0).
func ResolveEffectiveValidFrom(explicitValidFrom, resolvedEventTime, turnOccurredAt int64, warrant string) int64 {
	if resolvedEventTime > 0 {
		if explicitValidFrom <= 0 && turnOccurredAt > 0 && resolvedEventTime > turnOccurredAt &&
			strings.EqualFold(strings.TrimSpace(warrant), "performative") {
			return turnOccurredAt
		}
		return resolvedEventTime
	}
	if explicitValidFrom > 0 {
		return explicitValidFrom
	}
	return 0
}

// ProjectAssertionAsOf evaluates 4-temporal visibility at a target AsOf timestamp and projects
// the epistemic status and prospective loop state to their true historical values at AsOf:
//   - When asOf <= 0 (present-state read), the row is kept as-is;
//   - When asOf > 0:
//     1) Transaction time gate: tRecord <= asOf (already recorded at asOf);
//     2) Validity start gate: effValidFrom == 0 || effValidFrom <= asOf (already in effect at asOf;
//     for legacy performative commitments where tValidFrom was set to future due date tEvent > tSource,
//     effValidFrom is normalized to tSource so the commitment is visible from when it was made);
//     3) Validity end gate: tValidTo == 0 || tValidTo > asOf (not yet superseded/expired at asOf);
//     4) Historical state projection: any assertion surviving (1)-(3) was "active" at asOf
//     (and if warrant=="performative" with a closed loop_state that only closed at tValidTo > asOf,
//     its loop_state at asOf was still "open").
func ProjectAssertionAsOf(status, warrant, loopState string, tRecord, tValidFrom, tValidTo, asOf int64, optEventAndSource ...int64) (alive bool, projStatus, projLoopState string) {
	if asOf <= 0 {
		return true, status, loopState
	}
	effRecord := tRecord
	if len(optEventAndSource) >= 2 && optEventAndSource[1] > 0 {
		tSource := optEventAndSource[1]
		if effRecord <= 0 || (effRecord-tSource > 7*86400 && asOf < effRecord-7*86400) {
			effRecord = tSource
		}
	}
	if effRecord > asOf {
		return false, "", ""
	}
	effValidFrom := tValidFrom
	if len(optEventAndSource) >= 2 {
		tEvent, tSource := optEventAndSource[0], optEventAndSource[1]
		if tValidFrom > 0 && tEvent > 0 && tSource > 0 && tValidFrom == tEvent && tEvent > tSource &&
			strings.EqualFold(strings.TrimSpace(warrant), "performative") {
			effValidFrom = tSource
		}
	}
	if effValidFrom > 0 && effValidFrom > asOf {
		return false, "", ""
	}
	if tValidTo > 0 && tValidTo <= asOf {
		return false, "", ""
	}
	projStatus = "active"
	projLoopState = loopState
	ls := strings.ToLower(strings.TrimSpace(loopState))
	if strings.EqualFold(strings.TrimSpace(warrant), "performative") &&
		(ls == LoopStateFulfilled || ls == LoopStateWaived || ls == LoopStateBroken) {
		projLoopState = LoopStateOpen
	}
	return true, projStatus, projLoopState
}

// DeriveReferenceNow computes a deterministic, zero-wall-clock reference timestamp for read-side
// temporal operators (such as ZeigarnikLoopBoost):
//  1. Explicit AsOf timestamp (> 0);
//  2. Explicit TimeRef timestamp (> 0);
//  3. Test-injected reference clock via SetNowUnixForTest;
//  4. Endogenous maximum timestamp from the candidate set (t_source / t_record / t_event).
func DeriveReferenceNow(asOf, timeRef int64, candidateTimes ...int64) int64 {
	if asOf > 0 {
		return asOf
	}
	if timeRef > 0 {
		return timeRef
	}
	if injected, ok := InjectedNowUnix(); ok && injected > 0 {
		return injected
	}
	var maxTs int64
	for _, ts := range candidateTimes {
		if ts > maxTs {
			maxTs = ts
		}
	}
	return maxTs
}

// SelectWaveCausalSupersedes 对同波并发落图的断言按源对话波次因果序（WaveOrder）计算需补齐取代的节点对：
// 当后序批次携带显式关闭/更正（closingNodeIDs 非空）但因并发微秒级交错比前序批次更早落库时，
// 严格按 WaveOrder 升序还原先后关系，输出所有满足 Older.TSource <= Closer.TSource 且同 SubjectID + 同源谓词（RelID 相等或香农比特场形态等价，或显式关闭同主客体状态/前瞻环）的取代对。
func SelectWaveCausalSupersedes(rows []WaveCausalAssertion, closingNodeIDs []uint64) []WaveSupersedePair {
	if len(rows) < 2 || len(closingNodeIDs) == 0 {
		return nil
	}
	closingSet := make(map[uint64]bool, len(closingNodeIDs))
	for _, cid := range closingNodeIDs {
		closingSet[cid] = true
	}
	bySubj := map[uint64][]WaveCausalAssertion{}
	for _, r := range rows {
		if r.SubjectID == 0 || r.RelID == 0 {
			continue
		}
		bySubj[r.SubjectID] = append(bySubj[r.SubjectID], r)
	}
	var out []WaveSupersedePair
	for _, group := range bySubj {
		if len(group) < 2 {
			continue
		}
		for i := 0; i < len(group); i++ {
			for k := i + 1; k < len(group); k++ {
				if group[k].WaveOrder < group[i].WaveOrder {
					group[i], group[k] = group[k], group[i]
				}
			}
		}
		closed := make(map[uint64]bool, len(group))
		for j := 1; j < len(group); j++ {
			closer := group[j]
			if !closingSet[closer.NodeID] {
				continue
			}
			cRelLow := strings.ToLower(strings.TrimSpace(closer.RelLabel))
			cObjLow := strings.ToLower(strings.TrimSpace(closer.ObjectLabel))
			for i := 0; i < j; i++ {
				older := group[i]
				if closed[older.NodeID] || older.TSource > closer.TSource {
					continue
				}
				if older.World == "hypothetical" && (closer.World == "" || closer.World == "actual") && older.LoopState != LoopStateOpen {
					continue
				}
				if older.TurnHash != "" && closer.TurnHash != "" && older.TurnHash == closer.TurnHash {
					continue
				}
				// 外层言说/提问包装行指向不同内层命题时是两次独立的发话事件，不是状态更替（与 selectClosePriorTargets 同口径）：
				if older.ObjectIsAssertion && older.LoopState != LoopStateOpen && older.ObjectID != closer.ObjectID {
					continue
				}
				oRelLow := strings.ToLower(strings.TrimSpace(older.RelLabel))
				oObjLow := strings.ToLower(strings.TrimSpace(older.ObjectLabel))
				sameRelSlot := older.RelID == closer.RelID ||
					(oRelLow != "" && cRelLow != "" && (AreRelationsMorphologicallyEquivalent(oRelLow, cRelLow) ||
						(older.LoopState == LoopStateOpen && (strings.Contains(oRelLow, cRelLow) || strings.Contains(cRelLow, oRelLow)))))
				openKernelMatch := older.LoopState == LoopStateOpen &&
					((older.ObjectID == 0 && cObjLow != "" && LabelsShareContentStemOrSubstring(oRelLow, cObjLow)) ||
						(closer.ObjectID == 0 && oObjLow != "" && LabelsShareContentStemOrSubstring(oObjLow, cRelLow)) ||
						(older.ObjectID == 0 && closer.ObjectID == 0 && LabelsShareContentStemOrSubstring(oRelLow, cRelLow)))
					// 波次补关只保留 open 前瞻环兑现/取消，以及**点名旧值**（closes_prior_value）
					// 的逐字命中。「同关系异值」与「同客体异关系」两条启发式会在多值关系
					// （recommended/published/states…）上误关并列事实；波次不再擅自扩大关闭面（宁漏不错）。
				if openKernelMatch || (older.LoopState == LoopStateOpen && sameRelSlot) || (sameRelSlot && waveCloseRefMatches(closer.ClosesPriorValue, older)) {
					vt := closer.TValidFrom
					if vt <= 0 {
						vt = closer.TSource
					}
					vt = ClampSupersedeValidTo(vt, older.TValidFrom, closer.TSource)
					out = append(out, WaveSupersedePair{
						OlderNodeID:  older.NodeID,
						CloserNodeID: closer.NodeID,
						ValidTo:      vt,
					})
					closed[older.NodeID] = true
				}
			}
		}
	}
	return out
}

// waveCloseRefMatches v18：波次补关的「点名旧值」匹配（零语义 · 纯字符串/图统计）。
// ref 为写侧给出的被关停旧值 O 槽原文（逐字）或 "#<断言节点 id>"；空 ref 恒不匹配。
// 精确相等优先；包含匹配仅在 ref 自信息 ≥ 1×AtomicConceptEntropyBits 时放行（防短词泛化误关）。
func waveCloseRefMatches(ref string, older WaveCausalAssertion) bool {
	ref = strings.ToLower(strings.TrimSpace(ref))
	if ref == "" {
		return false
	}
	if strings.HasPrefix(ref, "#") {
		if n, err := strconv.ParseUint(strings.TrimPrefix(ref, "#"), 10, 64); err == nil {
			return older.NodeID == n
		}
		return false
	}
	surf := strings.ToLower(strings.TrimSpace(older.ObjectLabel))
	if surf == "" {
		surf = strings.ToLower(strings.TrimSpace(older.ObjectLiteral))
	}
	if surf == "" {
		return false
	}
	if surf == ref {
		return true
	}
	return StringShannonBits(ref) >= AtomicConceptEntropyBits &&
		(strings.Contains(surf, ref) || strings.Contains(ref, surf))
}
