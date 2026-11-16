// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package memcore

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// RunStoreBackendComplianceSuite runs the cross-dialect 1:1 storage contract test suite
// against any StoreBackend implementation (PostgreSQL or SQLite).
func RunStoreBackendComplianceSuite(t *testing.T, backend StoreBackend) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().Unix()
	scope := fmt.Sprintf("mem:compliance:%s:%d", backend.Dialect(), time.Now().UnixNano()%1000000)
	if cleaner, ok := backend.(interface {
		PurgeScope(ctx context.Context, scope string) error
	}); ok {
		t.Cleanup(func() {
			_ = cleaner.PurgeScope(context.Background(), scope)
		})
	}

	// 1. Node creation + idempotency + alias resolution
	n1, created, err := backend.EnsureNode(ctx, scope, "referent", "Helena", "shared", "actual", now)
	if err != nil || n1 == nil || n1.ID == "" || !created {
		t.Fatalf("[%s] EnsureNode first call: node=%+v created=%v err=%v", backend.Dialect(), n1, created, err)
	}
	n1Dup, createdDup, err := backend.EnsureNode(ctx, scope, "referent", "helena", "shared", "actual", now)
	if err != nil || n1Dup == nil || n1Dup.ID != n1.ID || createdDup {
		t.Fatalf("[%s] EnsureNode idempotent call: got=%+v created=%v err=%v", backend.Dialect(), n1Dup, createdDup, err)
	}

	bound, err := backend.BindAlias(ctx, scope, n1.ID, "Hel", "remember:shortening", now)
	if err != nil || !bound {
		t.Fatalf("[%s] BindAlias Hel->Helena: bound=%v err=%v", backend.Dialect(), bound, err)
	}
	foundByAlias, ok, err := backend.FindNodeByName(ctx, scope, "Hel")
	if err != nil || !ok || foundByAlias == nil || foundByAlias.ID != n1.ID {
		t.Fatalf("[%s] FindNodeByName(Hel): got=%+v ok=%v err=%v, want ID=%s", backend.Dialect(), foundByAlias, ok, err, n1.ID)
	}

	// RA09 guard: binding "Hel" to another referent in the same scope must be rejected/no-op
	n2, _, err := backend.EnsureNode(ctx, scope, "referent", "Clara", "shared", "actual", now)
	if err != nil || n2 == nil {
		t.Fatalf("[%s] EnsureNode Clara: %v", backend.Dialect(), err)
	}
	boundCross, err := backend.BindAlias(ctx, scope, n2.ID, "Hel", "remember:referral", now)
	if err != nil || boundCross {
		t.Fatalf("[%s] BindAlias cross-entity collision must return false, got bound=%v err=%v", backend.Dialect(), boundCross, err)
	}

	// 2. L0 Trace + structured memory assertion (with unbound slots & event time) + ClosePriorAssertions
	tr, _, err := backend.AppendTrace(ctx, Trace{
		Scope:      scope,
		Session:    "sess-1",
		Turn:       1,
		Kind:       "message",
		Author:     "Helena",
		Audience:   "shared",
		Text:       "In May 2022 I lived in Seattle, and later in June 2023 I moved to Austin.",
		OccurredAt: now,
		RecordedAt: now,
	})
	if err != nil || tr == nil || tr.Hash == "" {
		t.Fatalf("[%s] AppendTrace: tr=%+v err=%v", backend.Dialect(), tr, err)
	}

	relNode, _, err := backend.EnsureNode(ctx, scope, "relation_type", "lives_in", "shared", "actual", now)
	if err != nil || relNode == nil {
		t.Fatalf("[%s] EnsureNode relation_type: %v", backend.Dialect(), err)
	}
	obj1, _, err := backend.EnsureNode(ctx, scope, "referent", "Seattle", "shared", "actual", now)
	if err != nil || obj1 == nil {
		t.Fatalf("[%s] EnsureNode Seattle: %v", backend.Dialect(), err)
	}
	obj2, _, err := backend.EnsureNode(ctx, scope, "referent", "Austin", "shared", "actual", now)
	if err != nil || obj2 == nil {
		t.Fatalf("[%s] EnsureNode Austin: %v", backend.Dialect(), err)
	}

	a1, inserted1, err := backend.AppendAssertion(ctx, Assertion{
		Scope:           scope,
		SubjectID:       n1.ID,
		NumSubjectID:    n1.NumID,
		RelID:           relNode.ID,
		NumRelID:        relNode.NumID,
		ObjectID:        obj1.ID,
		NumObjectID:     obj1.NumID,
		World:           "actual",
		Carrier:         "virtual",
		Locus:           "US-WA",
		TValidFrom:      1651363200,
		TRecord:         now,
		TSource:         now,
		TEvent:          1651363200,
		EventSrc:        "explicit",
		TemporalClass:   "dated",
		HolderID:        n1.ID,
		NumHolderID:     n1.NumID,
		Warrant:         "referential",
		AnchorTraceHash: tr.Hash,
		AnchorStart:     0,
		AnchorEnd:       30,
		SourceTraceHash: tr.Hash,
		Audience:        "shared",
		AudienceSubject: "team-alpha",
		Confidence:      0.95,
		Status:          "active",
		Hash:            AssertionDigestSHA256(scope, n1.ID, relNode.ID, obj1.ID, tr.Hash, "1"),
		CreatedAt:       now,
	}, "Helena lives_in Seattle")
	if err != nil || !inserted1 || a1 == nil || a1.NodeID == "" {
		t.Fatalf("[%s] AppendAssertion a1: a1=%+v inserted=%v err=%v", backend.Dialect(), a1, inserted1, err)
	}

	a2, inserted2, err := backend.AppendAssertion(ctx, Assertion{
		Scope:           scope,
		SubjectID:       n1.ID,
		NumSubjectID:    n1.NumID,
		RelID:           relNode.ID,
		NumRelID:        relNode.NumID,
		ObjectID:        obj2.ID,
		NumObjectID:     obj2.NumID,
		World:           "actual",
		Carrier:         "virtual",
		TValidFrom:      1685577600,
		TRecord:         now,
		TSource:         now,
		TEvent:          1685577600,
		EventSrc:        "explicit",
		TemporalClass:   "dated",
		HolderID:        n1.ID,
		NumHolderID:     n1.NumID,
		Warrant:         "referential",
		AnchorTraceHash: tr.Hash,
		AnchorStart:     32,
		AnchorEnd:       72,
		SourceTraceHash: tr.Hash,
		Audience:        "shared",
		Confidence:      0.95,
		Status:          "active",
		Hash:            AssertionDigestSHA256(scope, n1.ID, relNode.ID, obj2.ID, tr.Hash, "2"),
		CreatedAt:       now,
	}, "Helena lives_in Austin")
	if err != nil || !inserted2 || a2 == nil {
		t.Fatalf("[%s] AppendAssertion a2: a2=%+v inserted=%v err=%v", backend.Dialect(), a2, inserted2, err)
	}

	closedIDs, err := backend.ClosePriorAssertions(ctx, scope, a2.NodeID, n1.ID, relNode.ID, obj2.ID, "", 1685577600, now)
	if err != nil || len(closedIDs) != 1 || closedIDs[0] != a1.NodeID {
		t.Fatalf("[%s] ClosePriorAssertions: closed=%v err=%v, want [%s]", backend.Dialect(), closedIDs, err, a1.NodeID)
	}
	// Idempotency of ClosePriorAssertions: repeating the call must not re-close already superseded rows.
	closedAgain, err := backend.ClosePriorAssertions(ctx, scope, a2.NodeID, n1.ID, relNode.ID, obj2.ID, "", 1685577600, now)
	if err != nil || len(closedAgain) != 0 {
		t.Fatalf("[%s] ClosePriorAssertions idempotent repeat: closed=%v err=%v, want []", backend.Dialect(), closedAgain, err)
	}
	// Verify Redline-1 append-only state transition invariant on a1 -> superseded.
	a1After := *a1
	a1After.Status = "superseded"
	a1After.TValidTo = 1685577600
	if err := ValidateAssertionAppendOnlyMutation(*a1, a1After, false); err != nil {
		t.Fatalf("[%s] ValidateAssertionAppendOnlyMutation failed on closed assertion: %v", backend.Dialect(), err)
	}

	// 3. Restate + MemoryHot + EpisodeContainer 1:1 tables
	if err := backend.RecordRestate(ctx, RestateRow{
		Scope:           scope,
		AssertionNodeID: a2.NodeID,
		TraceHash:       tr.Hash,
		AnchorStart:     32,
		AnchorEnd:       72,
		Kind:            "occurrence",
		OccurredAt:      now,
		CreatedAt:       now,
	}); err != nil {
		t.Fatalf("[%s] RecordRestate: %v", backend.Dialect(), err)
	}

	if err := backend.RecordMemoryHot(ctx, MemoryHot{
		Scope:           scope,
		SessionKey:      "sess-1",
		Kind:            "recall",
		QueryText:       "Where does Helena live?",
		HitNodeIDs:      []string{n1.ID, obj2.ID},
		HitAssertionIDs: []string{a2.NodeID},
		CreatedAt:       now,
	}); err != nil {
		t.Fatalf("[%s] RecordMemoryHot: %v", backend.Dialect(), err)
	}
	hotRows, err := backend.RecentMemoryHot(ctx, scope, "sess-1", 5)
	if err != nil || len(hotRows) != 1 || len(hotRows[0].HitNodeIDs) != 2 {
		t.Fatalf("[%s] RecentMemoryHot: rows=%+v err=%v", backend.Dialect(), hotRows, err)
	}

	epKey := fmt.Sprintf("ep:%s:g1:L1:01", scope)
	if err := backend.UpsertEpisodeContainer(ctx, EpisodeContainer{
		Scope:       scope,
		EpKey:       epKey,
		Generation:  1,
		Level:       1,
		World:       "actual",
		Carrier:     "virtual",
		Locus:       "US",
		SessionKey:  "sess-1",
		TStart:      1651363200,
		TEnd:        1685577600,
		TSourceMin:  now,
		TSourceMax:  now,
		MemberCount: 2,
		UpdatedAt:   now,
	}, []EpisodeMember{
		{Scope: scope, MemberID: a1.NodeID, MemberKind: "assertion", Facet: "scene", CreatedAt: now},
		{Scope: scope, MemberID: a2.NodeID, MemberKind: "assertion", Facet: "event", CreatedAt: now},
	}); err != nil {
		t.Fatalf("[%s] UpsertEpisodeContainer: %v", backend.Dialect(), err)
	}
	eps, err := backend.ListEpisodeContainers(ctx, scope, 1, 10)
	if err != nil || len(eps) != 1 || eps[0].EpKey != epKey || eps[0].MemberCount != 2 {
		t.Fatalf("[%s] ListEpisodeContainers: eps=%+v err=%v", backend.Dialect(), eps, err)
	}

	// 4. Unified Engine end-to-end batch write (with inline sub_assertion AST expansion).
	eng := NewEngine(backend)
	batchRes, err := eng.RememberBatch(ctx, Trace{
		Scope:      scope,
		Session:    "sess-engine",
		Turn:       2,
		Kind:       "message",
		Author:     "Helena",
		Audience:   "shared",
		Text:       "Helena said that Clara painted a sunrise in May 2023.",
		OccurredAt: now,
		RecordedAt: now,
	}, []map[string]any{
		{
			"subject": "Helena",
			"rel":     "said",
			"anchor":  "Helena said that Clara painted a sunrise in May 2023.",
			"sub_assertion": map[string]any{
				"subject":   "Clara",
				"rel":       "painted",
				"object":    "sunrise",
				"time_expr": "2023-05",
			},
		},
	})
	if err != nil {
		t.Fatalf("[%s] Engine.RememberBatch error: %v", backend.Dialect(), err)
	}
	if len(batchRes.Written) != 2 {
		t.Fatalf("[%s] Engine.RememberBatch written = %d, want 2 (inner + outer)", backend.Dialect(), len(batchRes.Written))
	}
	if batchRes.Written[1].ObjectID != batchRes.Written[0].NodeID {
		t.Fatalf("[%s] Engine.RememberBatch outer ObjectID = %q, want inner NodeID %q",
			backend.Dialect(), batchRes.Written[1].ObjectID, batchRes.Written[0].NodeID)
	}
}
