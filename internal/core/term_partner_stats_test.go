// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

// 本件保证「词项化伙伴统计」路径可执行、权重有限且确定性一致；质量对照由评测层的
// 环境变量门控校验承担。
package core

import (
	"fmt"
	"math"
	"testing"
)

func TestTermKeyedPartnerStatsBuildsFinitely(t *testing.T) {
	docs := appendProbeDocs(160)
	build := func(term bool) *rmClusterState {
		SetTermPartnerStats(term)
		defer SetTermPartnerStats(false)
		e := NewEngine(BuildIndex(docs))
		return e.ensureClusterState()
	}
	for _, term := range []bool{false, true} {
		st := build(term)
		if st == nil || st.agg == nil {
			t.Fatalf("term=%v: missing cluster state", term)
		}
		if len(st.agg.fams) == 0 {
			t.Fatalf("term=%v: empty families", term)
		}
		for root, fa := range st.agg.fams {
			if math.IsNaN(fa.weight) || math.IsInf(fa.weight, 0) || fa.weight < 0 {
				t.Fatalf("term=%v family %x weight=%v", term, root, fa.weight)
			}
			if len(fa.partnerKey) != len(fa.partnerCnt) || len(fa.clusterKey) != len(fa.clusterCnt) {
				t.Fatalf("term=%v family %x inconsistent counts", term, root)
			}
		}
		for i, w := range st.termEffWeight {
			if math.IsNaN(w) || math.IsInf(w, 0) {
				t.Fatalf("term=%v term %d effWeight=%v", term, i, w)
			}
		}
	}
	// 同一模式下两次构建必须逐位一致。
	if a, b := aggCanonicalDigest(build(true).agg), aggCanonicalDigest(build(true).agg); a != b {
		t.Fatalf("term-keyed build is not deterministic: %s != %s", a, b)
	}
}

// TestTermKeyedIncrementalMatchesFullRebuild 验证词项模式的增量扩展与一次性全量
// 构建逐位一致（含族集合同步、伙伴计数增减与全族权重重算）。
func TestTermKeyedIncrementalMatchesFullRebuild(t *testing.T) {
	docs := appendProbeDocs(420)
	SetTermPartnerStats(true)
	defer SetTermPartnerStats(false)

	full := NewEngine(BuildIndex(docs))
	full.ensureClusterState()
	inc := NewEngine(BuildIndex(docs[:180]))
	inc.ensureClusterState()
	inc.AppendDocs(docs[180:])
	assertIncrementalFired(t, inc, true)

	if a, b := clusterStateCanonicalDigest(full.ensureClusterState()), clusterStateCanonicalDigest(inc.ensureClusterState()); a != b {
		t.Fatalf("term-keyed cluster digest mismatch:\n full=%s\n inc =%s", a, b)
	}
	if a, b := aggCanonicalDigest(full.clusterState.agg), aggCanonicalDigest(inc.clusterState.agg); a != b {
		fa, ia := full.clusterState.agg, inc.clusterState.agg
		famDig := func(f *clusterFamilyAgg) string {
			var d digestWriter
			d.i32(f.df)
			d.i32(f.multiDocCount)
			d.f64(f.weight)
			d.i64s(f.clusterKey)
			d.i32s(f.clusterCnt)
			d.u64s(f.partnerKey)
			d.i32s(f.partnerCnt)
			return fmt.Sprintf("%x", d.h[:8])
		}
		report := 0
		for k, w := range fa.fams {
			g := ia.fams[k]
			if g == nil {
				t.Logf("DIFF fam %x missing in inc (df=%d)", k, w.df)
				report++
			} else if w.df != g.df || w.multiDocCount != g.multiDocCount {
				t.Logf("DIFF fam %x df/multi full=%d/%d inc=%d/%d", k, w.df, w.multiDocCount, g.df, g.multiDocCount)
				report++
			} else if fmt.Sprint(w.clusterKey, w.clusterCnt) != fmt.Sprint(g.clusterKey, g.clusterCnt) {
				t.Logf("DIFF fam %x clusters", k)
				report++
			} else if w.weight != g.weight {
				t.Logf("DIFF fam %x weight full=%v inc=%v", k, w.weight, g.weight)
				report++
			} else if famDig(w) != famDig(g) {
				t.Logf("DIFF fam %x digest %s vs %s partnerFull=%d partnerInc=%d", k, famDig(w), famDig(g), len(w.partnerKey), len(g.partnerKey))
				report++
			}
			if report >= 6 {
				break
			}
		}
		for k := range ia.fams {
			if fa.fams[k] == nil {
				t.Logf("DIFF fam %x extra in inc (df=%d)", k, ia.fams[k].df)
				report++
				if report >= 8 {
					break
				}
			}
		}
		tp := 0
		for k, w := range fa.termPartners {
			g := ia.termPartners[k]
			if g == nil {
				t.Logf("TP missing host %x keys=%v", k, w)
				tp++
			} else if fmt.Sprint(w, fa.termPartnerCnt[k]) != fmt.Sprint(g, ia.termPartnerCnt[k]) {
				t.Logf("TP host %x full=%v/%v inc=%v/%v", k, w, fa.termPartnerCnt[k], g, ia.termPartnerCnt[k])
				tp++
			}
			if tp >= 6 {
				break
			}
		}
		for k := range ia.termPartners {
			if fa.termPartners[k] == nil {
				t.Logf("TP extra host %x keys=%v", k, ia.termPartners[k])
				tp++
				if tp >= 8 {
					break
				}
			}
		}
		t.Logf("TP diffs reported=%d hosts_full=%d hosts_inc=%d nil_full=%v nil_inc=%v", tp, len(fa.termPartners), len(ia.termPartners), fa.termPartners == nil, ia.termPartners == nil)
		t.Fatalf("term-keyed aggregate digest mismatch:\n full=%s\n inc =%s", a, b)
	}
	assertSameQuery(t, full, inc, digestProbeQuery(docs))
}
