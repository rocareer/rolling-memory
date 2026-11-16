// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package memcore

import (
	"fmt"
	"strings"
	"testing"
)

func TestMemcoreSliceAndContextBudget(t *testing.T) {
	short := "Alice met Bob at the conference in Berlin."
	if got := SplitTurnIntoVerbatimSlices(short, nil); len(got) != 1 || got[0] != short {
		t.Fatalf("expected unsliced short text, got %v", got)
	}

	var sb strings.Builder
	for i := 1; i <= 140; i++ {
		fmt.Fprintf(&sb, "Item #%03d records that architect Carol validated distributed consensus protocol P-%03d in cluster. ", i, i)
	}
	longText := sb.String()
	slices := SplitTurnIntoVerbatimSlices(longText, nil)
	if len(slices) < 2 {
		t.Fatalf("expected multiple slices for long text, got %d", len(slices))
	}
	for i, s := range slices {
		if !strings.Contains(longText, s) {
			t.Fatalf("slice %d is not a verbatim substring of original text", i)
		}
	}

	shortLines := []string{
		"Alice: Did you check the report?",
		"Bob: The Q3 performance one?",
		"Alice: Yes.",
		"Bob: Looks solid.",
	}
	capped := CapContextTurnsByTokens(shortLines, nil)
	if len(capped) != 4 {
		t.Fatalf("expected 4 short context turns preserved verbatim, got %d", len(capped))
	}
}

func TestAdaptiveWholeTurnSliceAndBarrierDecision(t *testing.T) {
	// 1. Short turn (<= 1024): keeps 1024 high-attention budget
	specShort := ComputeAdaptiveTurnSliceSpec(512)
	if specShort.ShouldSlice || specShort.BatchTokenBudget != BaseAttentionTokenBudget {
		t.Fatalf("expected short turn to use 1024 baseline without slicing, got %+v", specShort)
	}

	// 2. Medium/Long turn (1024 < tokens <= 4096): kept intact so working context carries at least one whole turn
	var medBuilder strings.Builder
	for i := 1; i <= 90; i++ {
		fmt.Fprintf(&medBuilder, "Statement #%02d records that clinician Dr. Lin documented medication dosage adjustment %d mg on 2026-10-30. ", i, i*5)
	}
	medTurn := medBuilder.String()
	medToks := CountTokens(medTurn)
	if medToks <= BaseAttentionTokenBudget || medToks > MaxWholeTurnTokenBudget {
		t.Fatalf("expected medTurn tokens in (1024, 4096], got %d", medToks)
	}
	medSlices, medSpec := SplitTurnAdaptiveWholeTurn(medTurn, nil)
	if len(medSlices) != 1 || medSlices[0] != medTurn || medSpec.ShouldSlice || medSpec.BatchTokenBudget != medToks {
		t.Fatalf("expected whole turn in (1024, 4096] to remain 1 intact slice with budget=%d, got slices=%d spec=%+v", medToks, len(medSlices), medSpec)
	}

	// 3. Preceding context turn in (1024, 4096] is preserved as a complete whole turn in working context
	ctxLines := []string{
		"Alice: Short earlier note.",
		"Dr. Lin: " + medTurn,
	}
	cappedCtx := CapContextTurnsByTokens(ctxLines, nil)
	if len(cappedCtx) != 1 || cappedCtx[0] != "Dr. Lin: "+strings.TrimSpace(medTurn) {
		t.Fatalf("expected working context to carry the complete preceding >1024 whole turn verbatim, got %d lines", len(cappedCtx))
	}

	// 4. Adaptive barrier: implicit preference query (InitialRelevantPending == 0, RelevantPendingTraces == 0)
	// with 24 remaining traces (<= 64, drainable well before 90s safe window) MUST NOT release prematurely at 12s!
	decHold := EvaluateAdaptiveBarrierDecision(AdaptiveBarrierSnapshot{
		TotalPendingTraces:     24,
		RelevantPendingTraces:  0,
		InitialRelevantPending: 0,
		AdvancedAny:            true,
		HasActiveHeartbeat:     true,
		Elapsed:                12_000_000_000, // 12s
		ClientSafeWindow:       90_000_000_000, // 90s
		VelocityTracesPerSec:   1.31,
	})
	if decHold.ShouldRelease {
		t.Fatalf("expected barrier to hold for small/drainable scope on zero-literal-overlap query, got release reason=%s", decHold.Reason)
	}

	// 5. Massive background trajectory (TotalPendingTraces = 280, vel = 0.8/s -> ETA 350s > remaining safe window)
	// with zero overlap after 30s warmup should release cleanly.
	decRelease := EvaluateAdaptiveBarrierDecision(AdaptiveBarrierSnapshot{
		TotalPendingTraces:     280,
		RelevantPendingTraces:  0,
		InitialRelevantPending: 0,
		AdvancedAny:            true,
		HasActiveHeartbeat:     true,
		Elapsed:                30_000_000_000, // 30s
		ClientSafeWindow:       90_000_000_000, // 90s
		VelocityTracesPerSec:   0.80,
	})
	if !decRelease.ShouldRelease || decRelease.Reason != "query_zero_pending_overlap_after_warmup" {
		t.Fatalf("expected massive un-drainable zero-overlap scope to release after 30s warmup, got %+v", decRelease)
	}

	// 6. Stem-aware pending trace matching ("reading" matches "read" in Arjun's bookstore turn)
	totUnfin, relUnfin, _ := CountQueryRelevantPendingTraces(
		"What are some ways to make my weekend reading time feel more relaxing and immersive?",
		[]PendingTraceSummary{
			{ID: 12, Session: "s1", SearchText: "Lots of time in the garden. On Sundays, I still grab the local paper and read it cover to cover."},
		},
		nil,
	)
	if totUnfin != 1 || relUnfin != 1 {
		t.Fatalf("expected stem-matched pending trace to be counted as relevant (tot=1, rel=1), got tot=%d rel=%d", totUnfin, relUnfin)
	}

	// 7. Small/medium active scope (TotalPendingTraces = 25 <= 32, InitialRelevantPending = 1, RelevantPendingTraces = 0)
	// must hold to finish the remaining 25 traces rather than dropping the second half of the session.
	decHoldSmallRel := EvaluateAdaptiveBarrierDecision(AdaptiveBarrierSnapshot{
		TotalPendingTraces:     25,
		RelevantPendingTraces:  0,
		InitialRelevantPending: 1,
		AdvancedAny:            true,
		HasActiveHeartbeat:     true,
		Elapsed:                16_000_000_000, // 16s
		ClientSafeWindow:       90_000_000_000, // 90s
		VelocityTracesPerSec:   1.50,
	})
	if decHoldSmallRel.ShouldRelease {
		t.Fatalf("expected barrier to hold for <=32 drainable traces even when initial literal hit drained, got %+v", decHoldSmallRel)
	}

	// 8. Abstract analytical / inference query with >=4 non-subject cues and 0 literal overlap in primary band
	// must not emit a false-positive single-subject premise deficit note.
	abstractNote := ComputeBandPremiseAttributionNote(
		[]string{"behavior", "group", "indicate", "role", "strongly"},
		[]BandConceptProfile{
			{SubjectLabel: "FOREMAN", IsCueMatched: true, RowCount: 44, DeclHits: map[string]bool{}, MaxRowHits: 0, MultiHitRows: 0},
		},
		true,
	)
	if abstractNote != "" {
		t.Fatalf("expected empty premise note for >=4 abstract analytical cues with 0 literal overlap, got %q", abstractNote)
	}
}

func TestAnonymousStrangerIndividualPrimitives(t *testing.T) {
	if suf, ok := ExtractStrangerChannelSuffix("user"); !ok || suf != "1" {
		t.Fatalf("expected user -> ('1', true), got (%q, %v)", suf, ok)
	}
	if suf, ok := ExtractStrangerChannelSuffix("user_2"); !ok || suf != "2" {
		t.Fatalf("expected user_2 -> ('2', true), got (%q, %v)", suf, ok)
	}
	if _, ok := ExtractStrangerChannelSuffix("assistant"); ok {
		t.Fatalf("assistant must not be treated as stranger channel")
	}
	if got := FormatAnonymousStrangerLabel("1", true); got != "人1" {
		t.Fatalf("expected 人1, got %q", got)
	}
	if got := FormatAnonymousStrangerLabel("2", false); got != "Person 2" {
		t.Fatalf("expected Person 2, got %q", got)
	}
	if !IsAnonymousStrangerLabel("人1") || !IsAnonymousStrangerLabel("Person 2") || IsAnonymousStrangerLabel("Sarah Garcia") {
		t.Fatalf("IsAnonymousStrangerLabel mismatch")
	}
	if IsCandidateSpeakerEntity("人1") || IsCandidateSpeakerEntity("Person 1") || !IsCandidateSpeakerEntity("Sarah Garcia") {
		t.Fatalf("IsCandidateSpeakerEntity must reject unnamed base-category ordinal labels and accept real names")
	}
	if spk := ExtractInlineTurnSpeaker("[tool_result] Score: 0.9999999999999999"); spk != "" {
		t.Fatalf("ExtractInlineTurnSpeaker must reject [tool_result] key-value header, got %q", spk)
	}
	if spk := ExtractInlineTurnSpeaker("[2023-05-08] Caroline: I went to the park."); spk != "Caroline" {
		t.Fatalf("ExtractInlineTurnSpeaker must extract real speaker after timestamp bracket, got %q", spk)
	}
	if spk := ExtractInlineTurnSpeaker("Jo: I went to the park."); spk != "Jo" {
		t.Fatalf("ExtractInlineTurnSpeaker must not false-kill 2-letter TitleCase speaker 'Jo', got %q", spk)
	}
	if spk := ExtractInlineTurnSpeaker("A: first option in multiple choice"); spk != "" {
		t.Fatalf("ExtractInlineTurnSpeaker must reject 1-letter MCQ option label 'A:', got %q", spk)
	}
	if !HasNumberedLineRange("10\timport os\n11\timport sys\n12\tdef main():") {
		t.Fatalf("HasNumberedLineRange should detect >=3 numbered lines")
	}
}

func TestMemcoreWatermarkAndWaveCausalSupersedes(t *testing.T) {
	rows := []TraceWatermarkRow{
		{ID: 10, Session: "s1"},
		{ID: 20, Session: "s2"},
		{ID: 30, Session: "s3"},
		{ID: 31, Session: "s4"},
	}
	done := map[string]int64{"s1": 10, "s2": 20, "s3": 30, "s4": 0}
	if got := ComputeSafePartialWatermark(rows, 0, done); got != 30 {
		t.Fatalf("expected safe partial watermark 30, got %d", got)
	}

	// Out-of-order DB insertion in same wave: closer (WaveOrder=1) has smaller NodeID (101) than older (WaveOrder=0, NodeID=102)
	// v18：波次补关须点名旧值（ClosesPriorValue），未点名不关（多值关系防误关）。
	wave := []WaveCausalAssertion{
		{NodeID: 101, SubjectID: 1, RelID: 2, ObjectID: 20, ObjectLabel: "新址", TSource: 1000, TValidFrom: 1000, WaveOrder: 1, ClosesPriorValue: "旧址"},
		{NodeID: 102, SubjectID: 1, RelID: 2, ObjectID: 10, ObjectLabel: "旧址", TSource: 1000, TValidFrom: 1000, WaveOrder: 0},
	}
	pairs := SelectWaveCausalSupersedes(wave, []uint64{101})
	if len(pairs) != 1 || pairs[0].OlderNodeID != 102 || pairs[0].CloserNodeID != 101 {
		t.Fatalf("expected older=102 superseded by closer=101, got %+v", pairs)
	}
	// 未点名旧值 ⇒ 同关系异值不得被波次关闭（v18 回归守卫）。
	noRefWave := []WaveCausalAssertion{
		{NodeID: 111, SubjectID: 1, RelID: 2, ObjectID: 20, ObjectLabel: "新址", TSource: 1000, TValidFrom: 1000, WaveOrder: 1},
		{NodeID: 112, SubjectID: 1, RelID: 2, ObjectID: 10, ObjectLabel: "旧址", TSource: 1000, TValidFrom: 1000, WaveOrder: 0},
	}
	if got := SelectWaveCausalSupersedes(noRefWave, []uint64{111}); len(got) != 0 {
		t.Fatalf("v18 未点名旧值仍被波次关闭：%+v", got)
	}

	// Multi-hop causal chain in a concurrent wave:
	// Turn 1 (WaveOrder=0, NodeID=203, T=100): lives in Beijing
	// Turn 5 (WaveOrder=1, NodeID=201, T=200, closer): moved to Shanghai -> must close 203 with ValidTo=200
	// Turn 9 (WaveOrder=2, NodeID=202, T=300, closer): moved to Shenzhen -> must close 201 with ValidTo=300
	multiHop := []WaveCausalAssertion{
		{NodeID: 202, SubjectID: 1, RelID: 2, ObjectID: 30, ObjectLabel: "深圳", TSource: 300, TValidFrom: 300, WaveOrder: 2, ClosesPriorValue: "上海"},
		{NodeID: 203, SubjectID: 1, RelID: 2, ObjectID: 10, ObjectLabel: "北京", TSource: 100, TValidFrom: 100, WaveOrder: 0},
		{NodeID: 201, SubjectID: 1, RelID: 2, ObjectID: 20, ObjectLabel: "上海", TSource: 200, TValidFrom: 200, WaveOrder: 1, ClosesPriorValue: "北京"},
	}
	mhPairs := SelectWaveCausalSupersedes(multiHop, []uint64{201, 202})
	if len(mhPairs) != 2 {
		t.Fatalf("expected 2 chained supersede pairs, got %+v", mhPairs)
	}
	if mhPairs[0].OlderNodeID != 203 || mhPairs[0].CloserNodeID != 201 || mhPairs[0].ValidTo != 200 {
		t.Fatalf("expected first hop 203->201@200, got %+v", mhPairs[0])
	}
	if mhPairs[1].OlderNodeID != 201 || mhPairs[1].CloserNodeID != 202 || mhPairs[1].ValidTo != 300 {
		t.Fatalf("expected second hop 201->202@300, got %+v", mhPairs[1])
	}

	// Cross-inflection morphological quotient supersede (RelID differs: "works at" vs "worked at"):
	inflectedWave := []WaveCausalAssertion{
		{NodeID: 301, SubjectID: 9, RelID: 11, RelLabel: "works at", ObjectID: 50, ObjectLabel: "老东家", TSource: 100, TValidFrom: 100, WaveOrder: 0},
		{NodeID: 302, SubjectID: 9, RelID: 12, RelLabel: "worked at", ObjectID: 60, ObjectLabel: "新东家", TSource: 200, TValidFrom: 200, WaveOrder: 1, ClosesPriorValue: "老东家"},
	}
	infPairs := SelectWaveCausalSupersedes(inflectedWave, []uint64{302})
	if len(infPairs) != 1 || infPairs[0].OlderNodeID != 301 || infPairs[0].CloserNodeID != 302 {
		t.Fatalf("expected morphological quotient supersede 301->302 across works_at/worked_at, got %+v", infPairs)
	}
	// Hypothetical embedded sub-assertion (e.g. asks whether X, LoopState="") must NOT be closed by later actual assertion:
	hypWave := []WaveCausalAssertion{
		{NodeID: 401, SubjectID: 9, RelID: 11, RelLabel: "has ever been in any dance comps", World: "hypothetical", TSource: 100, TValidFrom: 100, WaveOrder: 0},
		{NodeID: 402, SubjectID: 9, RelID: 12, RelLabel: "proposes dance moves", World: "actual", TSource: 200, TValidFrom: 200, WaveOrder: 1},
	}
	if hypPairs := SelectWaveCausalSupersedes(hypWave, []uint64{402}); len(hypPairs) != 0 {
		t.Fatalf("expected hypothetical non-open sub-assertion 401 to NOT be superseded by actual assertion 402, got %+v", hypPairs)
	}
	if !AreRelationsMorphologicallyEquivalent("works at", "worked at") || AreRelationsMorphologicallyEquivalent("works at", "adopted") {
		t.Fatalf("AreRelationsMorphologicallyEquivalent mismatch")
	}

	// Intra-session prior turn Shannon bit-field resonance (0-LLM)
	priors := []PriorTurnCandidate{
		{TurnSeq: 1, Speaker: "Caroline", DateStr: "2023-05-08", Text: "I adopted a golden retriever puppy named Buster from the shelter."},
		{TurnSeq: 2, Speaker: "Melanie", DateStr: "2023-05-08", Text: "That is wonderful news!"},
		{TurnSeq: 3, Speaker: "Caroline", DateStr: "2023-05-09", Text: "We went pottery painting at the downtown studio."},
	}
	res := SelectIntraSessionPriorResonance(priors, "Buster chewed through his leash at the park today.", 4, 256, nil)
	if len(res) != 1 || res[0].TurnSeq != 1 {
		t.Fatalf("expected prior turn #1 (Buster) to resonate, got %+v", res)
	}

	// 架构不变量：连续 BPE 先验信息增益（零 continue 硬截断阈值）：
	// 1) 实义技术词（事务/隔离/级别/幻读）连续信息量高于同数目的高频连词噪声轮（因为/虽然/但是/那个）；
	// 2) TokenPriorBits < 13.0b 的常用双字实词（北京=12.58b / 医院=12.85b / 数据=11.72b / 系统=11.54b / 项目=11.77b）
	//    100% 保留参与共振，严禁被 <13.0b 截断阈值误杀。
	cjkPriors := []PriorTurnCandidate{
		{TurnSeq: 1, Speaker: "Alice", DateStr: "2026-10-01", Text: "我们在内部存储模块把事务隔离级别改成了可串行化快照，彻底修复了幻读。"},
		{TurnSeq: 2, Speaker: "Alice", DateStr: "2026-10-02", Text: "我们在北京医院上线了新的数据系统项目。"},
		{TurnSeq: 4, Speaker: "Noise", DateStr: "2026-10-04", Text: "因为所以虽然但是然而不过而且并且因此于是乎我们大家觉得这个那个真的非常可以。"},
	}
	resRare := SelectIntraSessionPriorResonance(cjkPriors, "因为虽然但是，那个事务隔离级别和幻读的问题现在解决了吗？", 1, 384, nil)
	if len(resRare) != 1 || resRare[0].TurnSeq != 1 {
		t.Fatalf("expected continuous BPE prior gain to rank Turn#1 (事务/隔离/级别/幻读) above Turn#4 glue noise, got %+v", resRare)
	}
	resCommon := SelectIntraSessionPriorResonance(cjkPriors, "北京医院的那个数据系统项目现在怎么样了？", 1, 384, nil)
	if len(resCommon) != 1 || resCommon[0].TurnSeq != 2 {
		t.Fatalf("架构不变量: common <13.0b CJK content words (北京/医院/数据/系统/项目) must NEVER be dropped by a hard cutoff, got %+v", resCommon)
	}
	// 短词/重复字母词（如 fee / go，UTF-8 StringShannonBits < 4.5b）仍按连续信息量参与远距共振（零 4.5b 布尔截断阈值）：
	shortPriors := []PriorTurnCandidate{
		{TurnSeq: 1, Speaker: "Alice", DateStr: "2026-10-01", Text: "We waived the fee for Go."},
		{TurnSeq: 2, Speaker: "Bob", DateStr: "2026-10-02", Text: "Hello there."},
	}
	if resShort := SelectIntraSessionPriorResonance(shortPriors, "What about the fee for Go?", 1, 384, nil); len(resShort) != 1 || resShort[0].TurnSeq != 1 {
		t.Fatalf("expected short content tokens (fee/go <4.5b) to participate in continuous resonance without boolean gate, got %+v", resShort)
	}
	for _, w := range []string{"Go", "New", "App", "go", "new", "app"} {
		if IsPronounSurface(w) {
			t.Fatalf("case monotonicity guard: inflected open-class word %q must not be misclassified as pronoun", w)
		}
	}
}
