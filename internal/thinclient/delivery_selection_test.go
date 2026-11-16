// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package thinclient

import (
	"testing"
)

func deliveryCandidate(id string, energy float64, occurredAt int64) LocalResolvedHit {
	return LocalResolvedHit{CloudQueryHit: CloudQueryHit{UnitID: id, FinalWeight: energy, OccurredAt: occurredAt}, RawPlaintext: id}
}

// TestSelectAdaptiveDeliveryPassthrough 覆盖不触发选择的两条通路：预算内原样返回、无能量 fail-safe 截断。
func TestSelectAdaptiveDeliveryPassthrough(t *testing.T) {
	hits := []LocalResolvedHit{
		deliveryCandidate("a", 10, 1000),
		deliveryCandidate("b", 5, 2000),
	}
	same := selectAdaptiveDelivery(hits, 5)
	if len(same) != 2 || same[0].UnitID != "a" || same[1].UnitID != "b" {
		t.Fatalf("expected passthrough for pool within budget, got %v", same)
	}
	noEnergy := []LocalResolvedHit{
		deliveryCandidate("a", 0, 1000),
		deliveryCandidate("b", 0, 2000),
		deliveryCandidate("c", 0, 3000),
	}
	out := selectAdaptiveDelivery(noEnergy, 2)
	if len(out) != 2 || out[0].UnitID != "a" || out[1].UnitID != "b" {
		t.Fatalf("expected fail-safe prefix truncation without energies, got %v", out)
	}
}

// TestSelectAdaptiveDeliveryPullsTemporalSiblings 验证同场同日弱匹配照应轮被时间-能量场拉进交付窗口。
func TestSelectAdaptiveDeliveryPullsTemporalSiblings(t *testing.T) {
	const day = int64(86400)
	anchor := deliveryCandidate("anchor", 1000, 100*day)
	sibling := deliveryCandidate("sibling", 1, 100*day+60) // 同日、Δt=60s、自身能量极低
	peer := deliveryCandidate("peer", 2, 100*day+120)      // 同日、用于把锚点 τ 定标为 120s
	hits := []LocalResolvedHit{anchor, sibling, peer}
	// 95 个跨日诱饵：各自独日（τ=1），得分=自身能量，介于 sibling 的场分之下。
	for i := 0; i < 95; i++ {
		hits = append(hits, deliveryCandidate(string(rune('A'+i%26))+string(rune('a'+i/26)), 300-float64(i), int64(50-i)*day))
	}
	out := selectAdaptiveDelivery(hits, 96)
	if len(out) != 96 {
		t.Fatalf("delivery budget violated: got %d", len(out))
	}
	if out[0].UnitID != "anchor" {
		t.Fatalf("anchor must stay first (own energy dominates), got %s", out[0].UnitID)
	}
	seen := map[string]bool{}
	for _, hit := range out {
		seen[hit.UnitID] = true
	}
	if !seen["sibling"] {
		t.Fatalf("temporal sibling should be pulled into the delivery window")
	}
	// 交付条数严格 ≤ 96 且全部来自候选池。
	if len(seen) != 96 {
		t.Fatalf("expected 96 distinct candidates, got %d", len(seen))
	}
}

// TestSelectAdaptiveDeliveryZeroBudget 覆盖 deliver<=0 与单条候选的边界。
func TestSelectAdaptiveDeliveryZeroBudget(t *testing.T) {
	hits := []LocalResolvedHit{deliveryCandidate("a", 1, 1)}
	if out := selectAdaptiveDelivery(hits, 0); len(out) != 1 {
		t.Fatalf("zero deliver budget should return input unchanged when pool is short, got %d", len(out))
	}
	if out := selectAdaptiveDelivery(nil, 4); len(out) != 0 {
		t.Fatalf("nil pool must stay empty, got %d", len(out))
	}
}
