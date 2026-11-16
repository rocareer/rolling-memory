// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package memcore

import (
	"testing"
)

func TestProspectiveLoopStateTransitions(t *testing.T) {
	if got := InitialLoopState("referential", ""); got != LoopStateNone {
		t.Fatalf("expected empty loop_state for referential, got %q", got)
	}
	if got := InitialLoopState("performative", ""); got != LoopStateOpen {
		t.Fatalf("expected open loop_state for performative, got %q", got)
	}
	if got := InitialLoopState("referential", "open"); got != LoopStateOpen {
		t.Fatalf("expected explicit open loop_state to be honored, got %q", got)
	}
	if got := ResolveClosedLoopState(LoopStateOpen, "occurrence"); got != LoopStateFulfilled {
		t.Fatalf("expected open -> fulfilled on normal explicit close, got %q", got)
	}
	if got := ResolveClosedLoopState(LoopStateOpen, "correction"); got != LoopStateWaived {
		t.Fatalf("expected open -> waived on correction explicit close, got %q", got)
	}
	if got := ResolveClosedLoopState(LoopStateNone, "occurrence"); got != LoopStateNone {
		t.Fatalf("expected retrospective fact to stay empty loop_state on close, got %q", got)
	}
}

func TestZeigarnikBoundedBellWaveAndNoChitchatInflation(t *testing.T) {
	now := int64(1700000000)
	if m := ZeigarnikLoopBoost(LoopStateNone, "active", now, now); m != 1.0 {
		t.Fatalf("retrospective fact should have 1.0 boost, got %v", m)
	}
	if m := ZeigarnikLoopBoost(LoopStateFulfilled, "active", now, now); m != 1.0 {
		t.Fatalf("fulfilled loop should release Zeigarnik tension (1.0), got %v", m)
	}
	// Falsification guard: undated performative chitchat (t_event == 0) must NOT receive Zeigarnik boost
	if m := ZeigarnikLoopBoost(LoopStateOpen, "active", 0, now, now-86400); m != 1.0 {
		t.Fatalf("undated performative assertion (t_event=0) must not receive Zeigarnik boost, got %v", m)
	}
	openFarDue := ZeigarnikLoopBoost(LoopStateOpen, "active", now+14*86400, now, now)
	openNearDue := ZeigarnikLoopBoost(LoopStateOpen, "active", now+3600, now, now)
	if !(openFarDue > 1.18 && openNearDue > openFarDue) {
		t.Fatalf("expected urgency rise as t_event approaches: farDue=%v < nearDue=%v",
			openFarDue, openNearDue)
	}
	// Falsification guard: unmentioned stale open loop (90 days past t_event/t_source) decays smoothly toward 1.0
	stale90d := ZeigarnikLoopBoost(LoopStateOpen, "active", now-90*86400, now, now-95*86400)
	if stale90d >= 1.02 || stale90d < 1.0 {
		t.Fatalf("expected 90-day unmentioned open loop to decay near 1.0 (no zombie loop), got %v", stale90d)
	}
	// Falsification guard: multi-year distant future plan (e.g. 365 days ahead) decays smoothly toward 1.0
	distantFuture := ZeigarnikLoopBoost(LoopStateOpen, "active", now+365*86400, now, now)
	if distantFuture >= 1.02 || distantFuture < 1.0 {
		t.Fatalf("expected 365-day distant future plan to taper near 1.0, got %v", distantFuture)
	}
}

func TestFourTemporalInvariantsAndAsOfProjection(t *testing.T) {
	// 1. NormalizeTemporalClass strict 4-temporal consistency
	if got := NormalizeTemporalClass("unknown", true); got != "dated" {
		t.Fatalf("NormalizeTemporalClass(unknown, hasTime=true) = %q, want dated", got)
	}
	if got := NormalizeTemporalClass("dated", false); got != "unknown" {
		t.Fatalf("NormalizeTemporalClass(dated, hasTime=false) = %q, want unknown", got)
	}
	if got := NormalizeTemporalClass("atemporal", false); got != "atemporal" {
		t.Fatalf("NormalizeTemporalClass(atemporal, false) = %q, want atemporal", got)
	}
	if got := NormalizeTemporalClass("atemporal", true); got != "atemporal" {
		t.Fatalf("NormalizeTemporalClass(atemporal, true) = %q, want atemporal", got)
	}

	// 2. ClampSupersedeValidTo stop-axis positivity and non-inversion
	if got := ClampSupersedeValidTo(0, 0, 1700000000); got != 1700000000 {
		t.Fatalf("ClampSupersedeValidTo(0, 0, now) = %d, want 1700000000", got)
	}
	if got := ClampSupersedeValidTo(0, 0, 0); got != 1 {
		t.Fatalf("ClampSupersedeValidTo(0, 0, 0) = %d, want 1", got)
	}
	if got := ClampSupersedeValidTo(100, 250, 300); got != 250 {
		t.Fatalf("ClampSupersedeValidTo(100 < oldValidFrom=250) = %d, want clamped 250", got)
	}

	// 3. ProjectAssertionAsOf 4-temporal time-travel projection
	if alive, _, _ := ProjectAssertionAsOf("active", "referential", "", 200, 100, 0, 150); alive {
		t.Fatal("expected row with t_record=200 > asOf=150 to be filtered out")
	}
	if alive, _, _ := ProjectAssertionAsOf("active", "referential", "", 200, 0, 0, 150, 0, 100); alive {
		t.Fatal("expected near-term online row (t_record=200, t_source=100, gap<=30d) with t_record > asOf=150 to be filtered out")
	}
	if alive, _, _ := ProjectAssertionAsOf("active", "referential", "", 100+90*86400, 0, 0, 150, 0, 100); !alive {
		t.Fatal("expected historical corpus replay row (t_record=100+90d, t_source=100, asOf=150) to be alive at asOf>=t_source")
	}
	if alive, _, _ := ProjectAssertionAsOf("active", "referential", "", 100, 200, 0, 150); alive {
		t.Fatal("expected row with t_valid_from=200 > asOf=150 to be filtered out")
	}
	if alive, _, _ := ProjectAssertionAsOf("superseded", "referential", "", 100, 100, 150, 150); alive {
		t.Fatal("expected row with t_valid_to=150 <= asOf=150 to be filtered out")
	}
	alive, st, ls := ProjectAssertionAsOf("superseded", "performative", LoopStateFulfilled, 100, 100, 200, 150)
	if !alive || st != "active" || ls != LoopStateOpen {
		t.Fatalf("at asOf=150 (before t_valid_to=200), superseded/fulfilled performative row should project to active/open, got alive=%v status=%q loop=%q",
			alive, st, ls)
	}

	// 4. DeriveReferenceNow deterministic hierarchy
	if got := DeriveReferenceNow(500, 600, 700, 800); got != 500 {
		t.Fatalf("DeriveReferenceNow should prefer asOf=500, got %d", got)
	}
	if got := DeriveReferenceNow(0, 600, 700, 800); got != 600 {
		t.Fatalf("DeriveReferenceNow should prefer timeRef=600 when asOf=0, got %d", got)
	}
	if got := DeriveReferenceNow(0, 0, 700, 850, 800); got != 850 {
		t.Fatalf("DeriveReferenceNow should fall back to max candidate time 850, got %d", got)
	}

	// 5. SelectWaveCausalSupersedes fallback when closer.TValidFrom == 0
	pairs := SelectWaveCausalSupersedes([]WaveCausalAssertion{
		{NodeID: 101, SubjectID: 1, RelID: 2, ObjectLiteral: "old", TSource: 1000, TValidFrom: 1200, WaveOrder: 0},
		{NodeID: 102, SubjectID: 1, RelID: 2, ObjectLiteral: "new", TSource: 1100, TValidFrom: 0, WaveOrder: 1, ClosesPriorValue: "old"},
	}, []uint64{102})
	if len(pairs) != 1 || pairs[0].ValidTo != 1200 {
		t.Fatalf("SelectWaveCausalSupersedes expected ValidTo clamped to older.TValidFrom=1200, got %+v", pairs)
	}

	// 6. SelectWaveCausalSupersedes adversarial: must NOT supersede non-open clausal relation merely sharing substring "says"
	//    and must NOT supersede across different relations when ObjectID is a dialogue subject hub (bySubj[ObjectID] non-empty).
	advWave := []WaveCausalAssertion{
		{NodeID: 201, SubjectID: 10, RelID: 21, RelLabel: "says made a huge difference in", ObjectID: 301, ObjectLabel: "the kids' lives", TSource: 1000, TValidFrom: 1000, WaveOrder: 0},
		{NodeID: 202, SubjectID: 10, RelID: 22, RelLabel: "says", ObjectID: 302, ObjectLabel: "Stay safe!", TSource: 1100, TValidFrom: 1100, WaveOrder: 1},
		{NodeID: 203, SubjectID: 10, RelID: 23, RelLabel: "admires courage of", ObjectID: 20, ObjectLabel: "Joanna", TSource: 1000, TValidFrom: 1000, WaveOrder: 0},
		{NodeID: 204, SubjectID: 10, RelID: 24, RelLabel: "wishes good luck to", ObjectID: 20, ObjectLabel: "Joanna", TSource: 1200, TValidFrom: 1200, WaveOrder: 2},
		{NodeID: 205, SubjectID: 20, RelID: 25, RelLabel: "writes", ObjectID: 303, ObjectLabel: "screenplay", TSource: 1050, TValidFrom: 1050, WaveOrder: 1},
	}
	if gotAdv := SelectWaveCausalSupersedes(advWave, []uint64{202, 204}); len(gotAdv) != 0 {
		t.Fatalf("SelectWaveCausalSupersedes must not supersede non-equivalent 'says...' or dialogue subject hub Joanna, got %+v", gotAdv)
	}

	// 7. 外层言说/提问包装行（客体为断言节点）：后一次提问不关停前一次提问（G14 实测 35 对 wave-reconcile 误关）；
	//    同 (subject, rel, object) 的包装行重复提交仍按原规则关停（ObjectID 相同则 diffVal=false，不触发）。
	wrapperWave := []WaveCausalAssertion{
		{NodeID: 301, SubjectID: 30, RelID: 31, RelLabel: "asks", ObjectID: 401, ObjectIsAssertion: true, TSource: 1000, TValidFrom: 1000, WaveOrder: 0},
		{NodeID: 302, SubjectID: 30, RelID: 31, RelLabel: "asks", ObjectID: 402, ObjectIsAssertion: true, TSource: 1100, TValidFrom: 1100, WaveOrder: 1},
	}
	if got := SelectWaveCausalSupersedes(wrapperWave, []uint64{302}); len(got) != 0 {
		t.Fatalf("SelectWaveCausalSupersedes must not supersede an earlier speech/question wrapper pointing to a different proposition, got %+v", got)
	}
	// 对照：客体为普通实体的同谓词异值状态更替仍须关停（防守卫过宽）；v18 须点名旧值。
	stateWave := []WaveCausalAssertion{
		{NodeID: 311, SubjectID: 30, RelID: 32, RelLabel: "lives in", ObjectID: 411, ObjectLabel: "北京", TSource: 1000, TValidFrom: 1000, WaveOrder: 0},
		{NodeID: 312, SubjectID: 30, RelID: 32, RelLabel: "lives in", ObjectID: 412, ObjectLabel: "上海", TSource: 1100, TValidFrom: 1100, WaveOrder: 1, ClosesPriorValue: "北京"},
	}
	if got := SelectWaveCausalSupersedes(stateWave, []uint64{312}); len(got) != 1 || got[0].OlderNodeID != 311 {
		t.Fatalf("same-relation state update on plain entities must still supersede, got %+v", got)
	}
}
