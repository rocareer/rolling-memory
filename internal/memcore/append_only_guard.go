// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package memcore

import (
	"fmt"
	"strings"
)

// Strict Governed Append-Only (Redline-1 — Axiom 10):
//   - Immutable Payload & Foreign Keys: Once an L0 trace, node, assertion, alias, meta edge,
//     or ledger entry is persisted, its identity, endpoints, verbatim anchor, and factual payload
//     are permanently frozen.
//   - Zero Hard-Delete on Live Graph: Live graph artifacts (trace, node, assertion, alias, meta,
//     event, occurrence, restate, participant) are never physically deleted during normal writes,
//     turn re-encoding, alias deduplication, or concept hierarchy construction.
//   - Monotonic State Stamp Only: Only whitelisted lifecycle state columns (status, t_valid_to,
//     loop_state, retired_at) and one-way empty-to-value enrichment columns (t_event, event_src,
//     t_valid_from, temporal_class, origin, ext_ref) may advance monotonically.
//   - Companion Ledger Mandate: Every lifecycle state transition must atomically emit a companion
//     meta edge and/or audit event in the same transaction.

// AppendOnlyCoreTables lists the four core memory tables whose rows are strictly append-only
// with governed monotonic state stamps.
var AppendOnlyCoreTables = []string{
	"trace",
	"node",
	"assertion",
	"alias",
}

// AppendOnlyLedgerTables lists the five companion and occurrence ledger tables.
var AppendOnlyLedgerTables = []string{
	"meta",
	"event",
	"occurrence",
	"restate",
	"participant",
}

// PureImmutableLedgerTables lists ledger tables where ANY UPDATE is strictly forbidden.
var PureImmutableLedgerTables = []string{
	"event",
	"occurrence",
	"restate",
	"participant",
}

// RebuildableDerivedTables lists auxiliary cache/index tables that may be rebuilt or evicted.
var RebuildableDerivedTables = []string{
	"episode_member",
	"episode_container",
	"vector",
	"workmem",
	"memory_hot",
	"session_cursor",
	"remember_state",
	"activation",
	"inbox",
	"part_parse",
}

// AssertionImmutableColumns lists all columns on memory_assertion that are permanently frozen after INSERT.
var AssertionImmutableColumns = []string{
	"scope",
	"node_id",
	"subject_id",
	"subject_literal",
	"rel_id",
	"object_id",
	"object_literal",
	"holder_id",
	"incarnation_id",
	"bare",
	"subject_absent",
	"subject_gap",
	"object_gap",
	"holder_gap",
	"warrant",
	"confidence",
	"audience",
	"audience_subject",
	"falsifier",
	"anchor_trace_hash",
	"anchor_start",
	"anchor_end",
	"source_trace_hash",
	"world",
	"carrier",
	"locus",
	"hash",
	"t_source",
	"t_record",
	"create_time",
}

// AssertionMonotonicFillColumns lists columns on memory_assertion that may only transition
// once from zero/empty/unknown to a resolved value, and can never be overwritten or cleared.
var AssertionMonotonicFillColumns = []string{
	"t_event",
	"event_src",
	"t_valid_from",
	"temporal_class",
}

// NodeImmutableColumns lists all identity columns on memory_node that are permanently frozen after INSERT.
var NodeImmutableColumns = []string{
	"id",
	"scope",
	"kind",
	"label",
	"world",
	"created_at",
}

// NodeMonotonicFillColumns lists columns on memory_node that may only be filled when empty/zero.
var NodeMonotonicFillColumns = []string{
	"origin",
	"ext_ref",
	"retired_at",
}

// ExpectedAppendOnlyTriggers lists the PostgreSQL physical trigger names enforcing Redline-1
// across all core and ledger tables.
var ExpectedAppendOnlyTriggers = []string{
	"trg_memory_assertion_append_only_guard",
	"trg_memory_node_append_only_guard",
	"trg_memory_trace_append_only_guard",
	"trg_memory_alias_append_only_guard",
	"trg_memory_meta_append_only_guard",
	"trg_memory_event_immutable_guard",
	"trg_memory_occurrence_immutable_guard",
	"trg_memory_restate_immutable_guard",
	"trg_memory_participant_immutable_guard",
}

var validAssertionStatusSet = map[string]bool{
	"active":     true,
	"superseded": true,
	"expired":    true,
	"disputed":   true,
	"retracted":  true,
}

var validNodeStatusSet = map[string]bool{
	"provisional": true,
	"active":      true,
	"merged":      true,
	"retired":     true,
}

var validMetaStatusSet = map[string]bool{
	"proposed": true,
	"active":   true,
	"reverted": true,
	"split":    true,
}

// ValidateAssertionStatusTransition checks whether a status and t_valid_to transition on
// memory_assertion obeys the Redline-1 monotonic state machine.
//   - active -> active | superseded | expired | disputed | retracted
//   - Transitioning to superseded or expired requires newValidTo > 0 (or oldValidTo > 0).
//   - Terminal states (superseded, expired, disputed, retracted) cannot revert to active
//     unless isReencodeRestore is true (idempotent replay of an un-superseded turn artifact).
//   - A non-zero oldValidTo can never be cleared or decreased unless isReencodeRestore is true.
func ValidateAssertionStatusTransition(oldStatus, newStatus string, oldValidTo, newValidTo int64, isReencodeRestore bool) error {
	from := strings.ToLower(strings.TrimSpace(oldStatus))
	to := strings.ToLower(strings.TrimSpace(newStatus))
	if from == "" {
		from = "active"
	}
	if to == "" {
		to = "active"
	}
	if !validAssertionStatusSet[from] {
		return fmt.Errorf("append-only violation: invalid old assertion status %q", oldStatus)
	}
	if !validAssertionStatusSet[to] {
		return fmt.Errorf("append-only violation: invalid new assertion status %q", newStatus)
	}
	if from != "active" && to == "active" {
		if !(from == "superseded" && isReencodeRestore) {
			return fmt.Errorf("append-only violation: non-active assertion status %q cannot revert to active", from)
		}
	}
	if from != "active" && from != to && !isReencodeRestore {
		return fmt.Errorf("append-only violation: terminal assertion status %q cannot transition to %q", from, to)
	}
	if (to == "superseded" || to == "expired") && newValidTo <= 0 && oldValidTo <= 0 {
		return fmt.Errorf("append-only violation: assertion status %q requires positive t_valid_to (got %d)", to, newValidTo)
	}
	if oldValidTo > 0 && newValidTo != oldValidTo && !isReencodeRestore {
		return fmt.Errorf("append-only violation: non-zero t_valid_to (%d) cannot be overwritten with %d", oldValidTo, newValidTo)
	}
	return nil
}

// ValidateLoopStateTransition checks whether a prospective loop_state transition obeys Axiom 9 & Redline-1:
//   - ” -> ” | 'open'
//   - 'open' -> 'open' | 'fulfilled' | 'waived' | 'broken'
//   - Terminal loop states ('fulfilled', 'waived', 'broken') are immutable and cannot revert to 'open' or ”.
func ValidateLoopStateTransition(oldLoopState, newLoopState string) error {
	from := strings.ToLower(strings.TrimSpace(oldLoopState))
	to := strings.ToLower(strings.TrimSpace(newLoopState))
	if from != "" && !validLoopStates[from] {
		return fmt.Errorf("append-only violation: invalid old loop_state %q", oldLoopState)
	}
	if to != "" && !validLoopStates[to] {
		return fmt.Errorf("append-only violation: invalid new loop_state %q", newLoopState)
	}
	if from == to {
		return nil
	}
	switch from {
	case LoopStateNone:
		if to != LoopStateOpen {
			return fmt.Errorf("append-only violation: empty loop_state can only remain empty or initialize to 'open', got %q", to)
		}
		return nil
	case LoopStateOpen:
		if to == LoopStateFulfilled || to == LoopStateWaived || to == LoopStateBroken {
			return nil
		}
		return fmt.Errorf("append-only violation: open loop_state cannot transition to %q", to)
	default:
		return fmt.Errorf("append-only violation: terminal loop_state %q is immutable (cannot change to %q)", from, to)
	}
}

// ValidateNodeStatusTransition checks whether a memory_node status transition obeys Redline-1:
//   - provisional -> provisional | active | merged | retired
//   - active -> active | merged | retired
//   - merged -> merged | retired (or provisional/active ONLY during governed RevertMerge/SplitNode)
//   - retired -> retired (terminal)
func ValidateNodeStatusTransition(oldStatus, newStatus string, isGovernedUnmergeOrSplit bool) error {
	from := strings.ToLower(strings.TrimSpace(oldStatus))
	to := strings.ToLower(strings.TrimSpace(newStatus))
	if !validNodeStatusSet[from] {
		return fmt.Errorf("append-only violation: invalid old node status %q", oldStatus)
	}
	if !validNodeStatusSet[to] {
		return fmt.Errorf("append-only violation: invalid new node status %q", newStatus)
	}
	if from == to {
		return nil
	}
	switch from {
	case "provisional":
		return nil // can promote to active, merge to merged, or retire to retired
	case "active":
		if to == "provisional" {
			return fmt.Errorf("append-only violation: active node cannot downgrade to provisional")
		}
		return nil
	case "merged":
		if to == "retired" {
			return nil
		}
		if isGovernedUnmergeOrSplit && (to == "provisional" || to == "active") {
			return nil
		}
		return fmt.Errorf("append-only violation: merged node cannot transition to %q without governed revert/split", to)
	case "retired":
		return fmt.Errorf("append-only violation: retired node is terminal and cannot transition to %q", to)
	}
	return nil
}

// ValidateMetaStatusTransition checks whether a memory_meta status transition obeys Redline-1:
//   - proposed -> proposed | active | reverted
//   - active -> active | reverted | split
//   - reverted / split -> terminal (cannot revert to active or proposed)
func ValidateMetaStatusTransition(oldStatus, newStatus string) error {
	from := strings.ToLower(strings.TrimSpace(oldStatus))
	to := strings.ToLower(strings.TrimSpace(newStatus))
	if !validMetaStatusSet[from] {
		return fmt.Errorf("append-only violation: invalid old meta status %q", oldStatus)
	}
	if !validMetaStatusSet[to] {
		return fmt.Errorf("append-only violation: invalid new meta status %q", newStatus)
	}
	if from == to {
		return nil
	}
	switch from {
	case "proposed":
		if to == "active" || to == "reverted" {
			return nil
		}
		return fmt.Errorf("append-only violation: proposed meta cannot transition to %q", to)
	case "active":
		if to == "reverted" || to == "split" {
			return nil
		}
		return fmt.Errorf("append-only violation: active meta cannot transition to %q", to)
	default:
		return fmt.Errorf("append-only violation: terminal meta status %q cannot transition to %q", from, to)
	}
}

// ValidateAliasSourceTransition checks whether a memory_alias.source update obeys Redline-1:
//   - Only a one-way transition to 'reverted' is allowed; once 'reverted', it is terminal.
func ValidateAliasSourceTransition(oldSource, newSource string) error {
	from := strings.TrimSpace(oldSource)
	to := strings.TrimSpace(newSource)
	if from == to {
		return nil
	}
	if strings.EqualFold(from, "reverted") {
		return fmt.Errorf("append-only violation: reverted alias cannot be restored or modified to %q", to)
	}
	if !strings.EqualFold(to, "reverted") {
		return fmt.Errorf("append-only violation: alias source %q can only transition to 'reverted', got %q", from, to)
	}
	return nil
}

// ValidateAssertionAppendOnlyMutation verifies that a proposed update from oldA to newA
// strictly preserves all immutable payload/FK columns and obeys one-way enrichment and state rules.
func ValidateAssertionAppendOnlyMutation(oldA, newA Assertion, isReencodeRestore bool) error {
	if oldA.Scope != newA.Scope ||
		oldA.NodeID != newA.NodeID ||
		oldA.NumNodeID != newA.NumNodeID ||
		oldA.SubjectID != newA.SubjectID ||
		oldA.NumSubjectID != newA.NumSubjectID ||
		oldA.RelID != newA.RelID ||
		oldA.NumRelID != newA.NumRelID ||
		oldA.ObjectID != newA.ObjectID ||
		oldA.NumObjectID != newA.NumObjectID ||
		oldA.ObjectLiteral != newA.ObjectLiteral ||
		oldA.HolderID != newA.HolderID ||
		oldA.NumHolderID != newA.NumHolderID ||
		oldA.Bare != newA.Bare ||
		oldA.SubjectAbsent != newA.SubjectAbsent ||
		oldA.SubjectGap != newA.SubjectGap ||
		oldA.ObjectGap != newA.ObjectGap ||
		oldA.HolderGap != newA.HolderGap ||
		oldA.Warrant != newA.Warrant ||
		oldA.Confidence != newA.Confidence ||
		oldA.Audience != newA.Audience ||
		oldA.AudienceSubject != newA.AudienceSubject ||
		oldA.AnchorTraceHash != newA.AnchorTraceHash ||
		oldA.AnchorStart != newA.AnchorStart ||
		oldA.AnchorEnd != newA.AnchorEnd ||
		oldA.SourceTraceHash != newA.SourceTraceHash ||
		oldA.World != newA.World ||
		oldA.Carrier != newA.Carrier ||
		oldA.Locus != newA.Locus ||
		oldA.Hash != newA.Hash ||
		oldA.TSource != newA.TSource ||
		oldA.TRecord != newA.TRecord ||
		oldA.CreatedAt != newA.CreatedAt {
		return fmt.Errorf("append-only violation: immutable fact/FK column modified on assertion %s", oldA.NodeID)
	}
	if oldA.TEvent != 0 && newA.TEvent != oldA.TEvent {
		return fmt.Errorf("append-only violation: non-zero t_event (%d) cannot be overwritten with %d", oldA.TEvent, newA.TEvent)
	}
	if oldA.EventSrc != "" && newA.EventSrc != oldA.EventSrc {
		return fmt.Errorf("append-only violation: non-empty event_src (%q) cannot be overwritten with %q", oldA.EventSrc, newA.EventSrc)
	}
	if oldA.TValidFrom != 0 && newA.TValidFrom != oldA.TValidFrom {
		return fmt.Errorf("append-only violation: non-zero t_valid_from (%d) cannot be overwritten with %d", oldA.TValidFrom, newA.TValidFrom)
	}
	if oldA.TemporalClass != "" && oldA.TemporalClass != "unknown" && newA.TemporalClass != oldA.TemporalClass {
		return fmt.Errorf("append-only violation: resolved temporal_class (%q) cannot be overwritten with %q", oldA.TemporalClass, newA.TemporalClass)
	}
	if (newA.TEvent == 0) != (strings.TrimSpace(newA.EventSrc) == "") {
		return fmt.Errorf("append-only violation: t_event (%d) and event_src (%q) pairing broken", newA.TEvent, newA.EventSrc)
	}
	if err := ValidateAssertionStatusTransition(oldA.Status, newA.Status, oldA.TValidTo, newA.TValidTo, isReencodeRestore); err != nil {
		return err
	}
	return ValidateLoopStateTransition(oldA.LoopState, newA.LoopState)
}

// ValidateNodeAppendOnlyMutation verifies that a proposed update from oldN to newN
// preserves all identity columns and obeys one-way enrichment and status rules.
func ValidateNodeAppendOnlyMutation(oldN, newN Node, isGovernedUnmergeOrSplit bool) error {
	if oldN.ID != newN.ID ||
		oldN.NumID != newN.NumID ||
		oldN.Scope != newN.Scope ||
		oldN.Kind != newN.Kind ||
		oldN.Label != newN.Label ||
		oldN.World != newN.World ||
		oldN.CreatedAt != newN.CreatedAt {
		return fmt.Errorf("append-only violation: immutable identity column modified on node %s", oldN.ID)
	}
	if oldN.Origin != "" && newN.Origin != oldN.Origin {
		return fmt.Errorf("append-only violation: non-empty node origin (%q) cannot be modified to %q", oldN.Origin, newN.Origin)
	}
	if oldN.ExtRef != "" && newN.ExtRef != oldN.ExtRef {
		return fmt.Errorf("append-only violation: non-empty node ext_ref (%q) cannot be modified to %q", oldN.ExtRef, newN.ExtRef)
	}
	var oldRet, newRet int64
	if oldN.RetiredAt != nil {
		oldRet = *oldN.RetiredAt
	}
	if newN.RetiredAt != nil {
		newRet = *newN.RetiredAt
	}
	if oldRet != 0 && newRet != oldRet {
		return fmt.Errorf("append-only violation: non-zero retired_at (%d) cannot be modified to %d", oldRet, newRet)
	}
	if strings.EqualFold(newN.Status, "retired") && newRet <= 0 {
		return fmt.Errorf("append-only violation: retired node %s must carry positive retired_at", oldN.ID)
	}
	return ValidateNodeStatusTransition(oldN.Status, newN.Status, isGovernedUnmergeOrSplit)
}
