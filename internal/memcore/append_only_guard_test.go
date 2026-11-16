// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package memcore

import "testing"

func TestAppendOnlyGuardStateTransitions(t *testing.T) {
	// 1. Assertion status transitions
	if err := ValidateAssertionStatusTransition("active", "superseded", 0, 1710000100, false); err != nil {
		t.Fatalf("expected active->superseded with validTo>0 to pass, got: %v", err)
	}
	if err := ValidateAssertionStatusTransition("active", "superseded", 0, 0, false); err == nil {
		t.Fatalf("expected active->superseded with validTo=0 to fail")
	}
	if err := ValidateAssertionStatusTransition("superseded", "active", 1710000100, 0, false); err == nil {
		t.Fatalf("expected superseded->active without reencodeRestore to fail")
	}
	if err := ValidateAssertionStatusTransition("superseded", "active", 1710000100, 0, true); err != nil {
		t.Fatalf("expected superseded->active with reencodeRestore=true to pass, got: %v", err)
	}
	if err := ValidateAssertionStatusTransition("active", "active", 1710000100, 1710000200, false); err == nil {
		t.Fatalf("expected overwriting non-zero t_valid_to to fail")
	}

	// 2. LoopState transitions
	if err := ValidateLoopStateTransition("", "open"); err != nil {
		t.Fatalf("expected ''->'open' to pass: %v", err)
	}
	if err := ValidateLoopStateTransition("", "fulfilled"); err == nil {
		t.Fatalf("expected ''->'fulfilled' directly to fail")
	}
	for _, term := range []string{LoopStateFulfilled, LoopStateWaived, LoopStateBroken} {
		if err := ValidateLoopStateTransition(LoopStateOpen, term); err != nil {
			t.Fatalf("expected open->%s to pass: %v", term, err)
		}
		if err := ValidateLoopStateTransition(term, LoopStateOpen); err == nil {
			t.Fatalf("expected terminal %s->open to fail", term)
		}
		if err := ValidateLoopStateTransition(term, LoopStateNone); err == nil {
			t.Fatalf("expected terminal %s->'' to fail", term)
		}
	}

	// 3. Node status transitions
	if err := ValidateNodeStatusTransition("provisional", "active", false); err != nil {
		t.Fatalf("expected provisional->active to pass: %v", err)
	}
	if err := ValidateNodeStatusTransition("active", "provisional", false); err == nil {
		t.Fatalf("expected active->provisional downgrade to fail")
	}
	if err := ValidateNodeStatusTransition("merged", "active", false); err == nil {
		t.Fatalf("expected un-governed merged->active to fail")
	}
	if err := ValidateNodeStatusTransition("merged", "active", true); err != nil {
		t.Fatalf("expected governed split merged->active to pass: %v", err)
	}
	if err := ValidateNodeStatusTransition("retired", "active", true); err == nil {
		t.Fatalf("expected retired->active to fail")
	}

	// 4. Meta & Alias transitions
	if err := ValidateMetaStatusTransition("proposed", "active"); err != nil {
		t.Fatalf("expected proposed->active to pass: %v", err)
	}
	if err := ValidateMetaStatusTransition("active", "reverted"); err != nil {
		t.Fatalf("expected active->reverted to pass: %v", err)
	}
	if err := ValidateMetaStatusTransition("reverted", "active"); err == nil {
		t.Fatalf("expected reverted->active to fail")
	}
	if err := ValidateMetaStatusTransition("split", "active"); err == nil {
		t.Fatalf("expected split->active to fail")
	}
	if err := ValidateAliasSourceTransition("remember:shortening", "reverted"); err != nil {
		t.Fatalf("expected alias source->reverted to pass: %v", err)
	}
	if err := ValidateAliasSourceTransition("reverted", "remember:shortening"); err == nil {
		t.Fatalf("expected reverted->remember:shortening to fail")
	}
	if err := ValidateAliasSourceTransition("remember:shortening", "manual"); err == nil {
		t.Fatalf("expected remember:shortening->manual mutation to fail")
	}
}

func TestValidateAssertionAndNodeAppendOnlyMutation(t *testing.T) {
	baseA := Assertion{
		Scope:           "person:test",
		NodeID:          "100",
		NumNodeID:       100,
		SubjectID:       "1",
		NumSubjectID:    1,
		RelID:           "2",
		NumRelID:        2,
		ObjectID:        "3",
		NumObjectID:     3,
		Warrant:         "performative",
		Confidence:      0.9,
		Audience:        "shared",
		World:           "actual",
		Carrier:         "virtual",
		Locus:           "room-a",
		AnchorTraceHash: "th1",
		AnchorStart:     0,
		AnchorEnd:       10,
		SourceTraceHash: "th1",
		TemporalClass:   "unknown",
		Status:          "active",
		LoopState:       "open",
		Hash:            "h1",
		TSource:         1710000000,
		TRecord:         1710000000,
		CreatedAt:       1710000000,
	}

	// Valid one-way time enrichment + loop fulfillment + supersede
	enrichedA := baseA
	enrichedA.TEvent = 1710086400
	enrichedA.EventSrc = "explicit"
	enrichedA.TValidFrom = 1710086400
	enrichedA.TemporalClass = "dated"
	enrichedA.Status = "superseded"
	enrichedA.TValidTo = 1710086400
	enrichedA.LoopState = "fulfilled"
	if err := ValidateAssertionAppendOnlyMutation(baseA, enrichedA, false); err != nil {
		t.Fatalf("expected valid one-way enrichment + supersede to pass, got: %v", err)
	}

	// Mutating subject_id must fail
	badSubj := enrichedA
	badSubj.SubjectID = "999"
	if err := ValidateAssertionAppendOnlyMutation(baseA, badSubj, false); err == nil {
		t.Fatalf("expected subject_id mutation to fail")
	}

	// Clearing non-zero t_event must fail
	clearedEvent := enrichedA
	clearedEvent.TEvent = 0
	clearedEvent.EventSrc = ""
	if err := ValidateAssertionAppendOnlyMutation(enrichedA, clearedEvent, false); err == nil {
		t.Fatalf("expected clearing non-zero t_event to fail")
	}

	// Node mutation validation
	baseN := Node{
		ID:        "1",
		NumID:     1,
		Scope:     "person:test",
		Kind:      "referent",
		Label:     "Elizabeth",
		World:     "actual",
		Status:    "provisional",
		CreatedAt: 1710000000,
	}
	promotedN := baseN
	promotedN.Status = "active"
	promotedN.Origin = "roster"
	promotedN.ExtRef = "roster:elizabeth"
	if err := ValidateNodeAppendOnlyMutation(baseN, promotedN, false); err != nil {
		t.Fatalf("expected valid node promotion + origin/ext_ref fill to pass, got: %v", err)
	}
	overwriteOrigin := promotedN
	overwriteOrigin.Origin = "world"
	if err := ValidateNodeAppendOnlyMutation(promotedN, overwriteOrigin, false); err == nil {
		t.Fatalf("expected overwriting non-empty origin to fail")
	}
	retireNoTs := promotedN
	retireNoTs.Status = "retired"
	retireNoTs.RetiredAt = nil
	if err := ValidateNodeAppendOnlyMutation(promotedN, retireNoTs, false); err == nil {
		t.Fatalf("expected retiring node with retired_at=0 to fail")
	}
}
