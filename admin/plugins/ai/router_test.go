// plugin: ai v0.2.0
package ai

import (
	"context"
	"testing"
)

// fixedRand 是确定性随机源：按队列返回 Intn 的值，用完后循环。
type fixedRand struct {
	values []int
	calls  int
}

func (f *fixedRand) Intn(n int) int {
	if len(f.values) == 0 {
		return 0
	}
	v := f.values[f.calls%len(f.values)]
	f.calls++
	if v < 0 {
		v = 0
	}
	if v >= n {
		v = n - 1
	}
	return v
}

type panicRand struct{ t *testing.T }

func (p panicRand) Intn(int) int {
	p.t.Helper()
	p.t.Fatal("候选=1 时不应调用随机源")
	return 0
}

func testCandidate(id uint64, priority, weight int) Candidate {
	return Candidate{
		ChannelID:       id,
		Name:            "ch",
		ChannelStatus:   StatusEnabled,
		BindingStatus:   StatusEnabled,
		BindingPriority: priority,
		BindingWeight:   weight,
		Circuit:         CircuitClosed,
		CooldownSeconds: 60,
	}
}

func TestResolveCandidatesFiltersSorts(t *testing.T) {
	candidates := []Candidate{
		testCandidate(10, 2, 1),
		testCandidate(11, 1, 1),
		testCandidate(12, 1, 5),
	}
	disabledBinding := testCandidate(13, 0, 9)
	disabledBinding.BindingStatus = StatusDisabled
	disabledChannel := testCandidate(14, 0, 9)
	disabledChannel.ChannelStatus = StatusDisabled
	candidates = append(candidates, disabledBinding, disabledChannel)

	got := ResolveCandidates(candidates, StrategyPriority, nil)
	if len(got) != 3 {
		t.Fatalf("候选数 = %d，期望 3：%+v", len(got), got)
	}
	wantOrder := []uint64{12, 11, 10} // priority ASC；同 priority 按 weight DESC；再按 channel_id ASC
	for i, want := range wantOrder {
		if got[i].ChannelID != want {
			t.Fatalf("第 %d 个候选 channel=%d，期望 %d（顺序 %+v）", i, got[i].ChannelID, want, got)
		}
	}
	if got[0].BindingWeight != 5 || got[1].BindingWeight != 1 {
		t.Fatalf("绑定权重未带入候选：%+v", got)
	}
}

func TestResolveCandidatesPriorityAscending(t *testing.T) {
	candidates := []Candidate{
		testCandidate(1, 3, 100),
		testCandidate(2, 1, 1),
		testCandidate(3, 2, 50),
	}
	got := ResolveCandidates(candidates, StrategyPriority, nil)
	if len(got) != 3 {
		t.Fatalf("候选数 = %d", len(got))
	}
	for i, want := range []uint64{2, 3, 1} {
		if got[i].ChannelID != want {
			t.Fatalf("顺序[%d]=%d，期望 %d", i, got[i].ChannelID, want)
		}
	}
}

func TestResolveCandidatesWeightedMovesHitToFrontPreservingRest(t *testing.T) {
	candidates := []Candidate{
		testCandidate(1, 1, 1), // A
		testCandidate(2, 2, 9), // B
		testCandidate(3, 3, 1), // C
	}
	// 权重和 = max(1,1)+max(1,9)+max(1,1) = 11：
	// 0 ⇒ A；1..9 ⇒ B；10 ⇒ C。
	cases := []struct {
		name  string
		pick  int
		order []uint64
	}{
		{"命中 A 保持原序", 0, []uint64{1, 2, 3}},
		{"命中 B 提到队首", 1, []uint64{2, 1, 3}},
		{"命中 B 末位点", 9, []uint64{2, 1, 3}},
		{"命中 C 提到队首且其余保序", 10, []uint64{3, 1, 2}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rnd := &fixedRand{values: []int{tc.pick}}
			got := ResolveCandidates(candidates, StrategyWeighted, rnd)
			if len(got) != 3 {
				t.Fatalf("候选数 = %d", len(got))
			}
			for i, want := range tc.order {
				if got[i].ChannelID != want {
					t.Fatalf("pick=%d 顺序[%d]=%d，期望 %d（实际 %+v）", tc.pick, i, got[i].ChannelID, want, got)
				}
			}
		})
	}
}

func TestResolveCandidatesWeightedSingleCandidateDoesNotRoll(t *testing.T) {
	got := ResolveCandidates([]Candidate{testCandidate(1, 1, 99)}, StrategyWeighted, panicRand{t: t})
	if len(got) != 1 || got[0].ChannelID != 1 {
		t.Fatalf("单候选退化错误：%+v", got)
	}
}

func TestResolveCandidatesPriorityStrategyDoesNotRoll(t *testing.T) {
	candidates := []Candidate{
		testCandidate(1, 2, 1),
		testCandidate(2, 1, 1),
	}
	got := ResolveCandidates(candidates, StrategyPriority, panicRand{t: t})
	if len(got) != 2 || got[0].ChannelID != 2 {
		t.Fatalf("priority 策略顺序错误：%+v", got)
	}
}

func TestResolveCandidatesTieBreakByChannelID(t *testing.T) {
	candidates := []Candidate{
		testCandidate(30, 1, 2),
		testCandidate(10, 1, 2),
		testCandidate(20, 1, 2),
	}
	got := ResolveCandidates(candidates, StrategyPriority, nil)
	for i, want := range []uint64{10, 20, 30} {
		if got[i].ChannelID != want {
			t.Fatalf("同序稳定排序失败：%+v", got)
		}
	}
}

func TestResolveCandidatesDoesNotMutateInputs(t *testing.T) {
	candidates := []Candidate{
		testCandidate(2, 2, 1),
		testCandidate(1, 1, 1),
	}
	copied := append([]Candidate(nil), candidates...)

	_ = ResolveCandidates(candidates, StrategyWeighted, &fixedRand{values: []int{0}})

	for i := range candidates {
		if candidates[i].ChannelID != copied[i].ChannelID {
			t.Fatalf("candidates 被修改：%+v vs %+v", candidates, copied)
		}
	}
}

func TestRouterFirstAdmittedRunsAdmissionForEachCandidate(t *testing.T) {
	store := newFakeCircuitStore()
	// 渠道 1：open 且冷却中 ⇒ 拒绝；渠道 2：closed ⇒ 放行。
	store.circuit[1] = CircuitOpen
	store.openedAt[1] = 100
	store.circuit[2] = CircuitClosed

	router := NewRouter(NewCircuit(store), &fixedRand{})
	open := testCandidate(1, 1, 1)
	open.Circuit = CircuitOpen
	open.OpenedAt = ptrInt64(100)
	closed := testCandidate(2, 2, 1)
	candidates := []Candidate{open, closed}

	got, ok, err := router.FirstAdmitted(context.Background(), candidates, 120) // 距 open 仅 20s
	if err != nil {
		t.Fatalf("FirstAdmitted 错误：%v", err)
	}
	if !ok || got.ChannelID != 2 {
		t.Fatalf("期望跳过冷却中的渠道 1 并选中渠道 2，实际 ok=%v got=%+v", ok, got)
	}
	if store.markFailureCalls != 0 || store.openCalls != 0 {
		t.Fatalf("准入不应写失败计数：%+v", store)
	}
}

func TestRouterFirstAdmittedAllDenied(t *testing.T) {
	store := newFakeCircuitStore()
	store.circuit[1] = CircuitHalfOpen
	store.openedAt[1] = 100
	router := NewRouter(NewCircuit(store), &fixedRand{})
	half := testCandidate(1, 1, 1)
	half.Circuit = CircuitHalfOpen
	half.OpenedAt = ptrInt64(100)
	_, ok, err := router.FirstAdmitted(context.Background(), []Candidate{half}, 110)
	if err != nil {
		t.Fatalf("FirstAdmitted 错误：%v", err)
	}
	if ok {
		t.Fatal("半开探针在飞时应全部拒绝")
	}
}
