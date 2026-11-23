// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package core

import (
	"fmt"
	"sort"
	"testing"
	"time"
)

// TestClusterStateDigestLateWriteEqualsFullRebuild covers RFC §六-11 / G5: an append whose
// timestamps fall inside the existing timeline (a late write) must leave exactly the state a
// full rebuild produces. The existing append parity test only appends later timestamps, so
// bucket insertion in the middle and the episode boundaries that depend on it were untested.
func TestClusterStateDigestLateWriteEqualsFullRebuild(t *testing.T) {
	docs := appendProbeDocs(300)
	split := 180
	base := int64(1700000000)

	inc := NewEngine(BuildIndex(docs[:split]))
	inc.ensureClusterState()

	late := make([]Doc, 0, len(docs)-split)
	for i, doc := range docs[split:] {
		// Interleave: every late document lands between two existing hourly buckets.
		doc.OccurredAt = base + int64(i)*3600 + 1800
		late = append(late, doc)
	}
	inc.AppendDocs(late)

	combined := make([]Doc, 0, len(docs))
	combined = append(combined, docs[:split]...)
	combined = append(combined, late...)
	full := NewEngine(BuildIndex(combined))
	full.ensureClusterState()

	want := clusterStateCanonicalDigest(full.ensureClusterState())
	got := clusterStateCanonicalDigest(inc.ensureClusterState())
	if want != got {
		t.Fatalf("late-write cluster state digest mismatch:\n full=%s\n inc =%s", want, got)
	}
	assertSameQuery(t, full, inc, digestProbeQuery(combined))
}

// seg2aMeasureDoc builds one document with a realistic term profile. Reference-scale
// corpora hold roughly 139 distinct terms per document, so pair computation does profile
// intersection of that size instead of comparing one-term documents.
func seg2aMeasureDoc(i int) Doc {
	doc := epTestDoc(fmt.Sprintf("same-%05d", i), 1700000000)
	const termsPerDoc = 128
	const vocab = 20000
	doc.Terms = make(map[uint64]Term, termsPerDoc)
	doc.Counts = make(map[uint64]int, termsPerDoc)
	doc.Positions = make(map[uint64][]TermPosition, termsPerDoc)
	for k := 0; k < termsPerDoc; k++ {
		// Zipf-like rank selection: real corpora concentrate mass on low ranks, so pair
		// computation meets overlapping profiles instead of disjoint uniform samples.
		u := float64((uint64(i)*2654435761+uint64(k)*40503)%1000003) / 1000003.0
		rank := int(u * u * u * vocab)
		h := uint64(9000 + rank)
		doc.Terms[h] = Term{Hash: h, Bits: 40, Role: RolePrimary, ExtFlags: ExtFlagSeenPrimary, Group: h, Sent: uint16(k / 32), Slot: uint16(k % 32)}
		doc.Counts[h] = 1
		doc.Positions[h] = []TermPosition{{Sentence: k / 32, Slot: k % 32}}
	}
	return doc
}

// TestDayCosQueryTimeCostPathologicalBucket measures RFC §六-8 / G1: if dayCos stops being
// precomputed at build time, a query that expands a pathological bucket pays pair costs.
// It reports per-pair p50/p95 for the raw computation, the per-query cache fill, and the
// per-query cache hit.
func TestDayCosQueryTimeCostPathologicalBucket(t *testing.T) {
	if testing.Short() {
		t.Skip("measurement")
	}
	const docs = 3000
	const seeds = 50
	corpus := make([]Doc, docs)
	for i := range corpus {
		// All documents share one instant: the single-bucket worst case.
		corpus[i] = seg2aMeasureDoc(i)
	}
	eng := NewEngine(BuildIndex(corpus))
	st := eng.ensureClusterState()
	// Simulate the post-2a state: no build-time pair cache, on-demand computation only.
	st.dayCos = nil
	st.dayCosKeys = nil
	st.dayCosVals = nil
	st.dayCosRowPtr = nil
	sc := eng.ensureQueryScratch(eng.Index.N, st.clusterN)
	pairs := seeds * (docs - 1)

	raw := make([]time.Duration, 0, pairs)
	start := time.Now()
	for seed := 0; seed < seeds; seed++ {
		for other := 0; other < docs; other++ {
			if other == seed {
				continue
			}
			pairStart := time.Now()
			_ = st.computeDocSubjectCos(eng.Index, int32(seed), int32(other))
			raw = append(raw, time.Since(pairStart))
		}
	}
	rawTotal := time.Since(start)

	fill := make([]time.Duration, 0, pairs)
	start = time.Now()
	for seed := 0; seed < seeds; seed++ {
		for other := 0; other < docs; other++ {
			if other == seed {
				continue
			}
			pairStart := time.Now()
			_ = st.docSubjectCos(eng.Index, sc, int32(seed), int32(other))
			fill = append(fill, time.Since(pairStart))
		}
	}
	fillTotal := time.Since(start)

	hit := make([]time.Duration, 0, pairs)
	start = time.Now()
	for seed := 0; seed < seeds; seed++ {
		for other := 0; other < docs; other++ {
			if other == seed {
				continue
			}
			pairStart := time.Now()
			_ = st.docSubjectCos(eng.Index, sc, int32(seed), int32(other))
			hit = append(hit, time.Since(pairStart))
		}
	}
	hitTotal := time.Since(start)

	t.Logf("same-bucket n=%d E=%d pairs=%d", docs, seeds, len(raw))
	t.Logf("raw   total=%s p50=%s p95=%s per-pair=%.3fµs",
		rawTotal.Round(time.Millisecond), percentileDuration(raw, 0.50), percentileDuration(raw, 0.95),
		float64(rawTotal.Nanoseconds())/float64(len(raw))/1000.0)
	t.Logf("fill  total=%s p50=%s p95=%s per-pair=%.3fµs",
		fillTotal.Round(time.Millisecond), percentileDuration(fill, 0.50), percentileDuration(fill, 0.95),
		float64(fillTotal.Nanoseconds())/float64(len(fill))/1000.0)
	t.Logf("hit   total=%s p50=%s p95=%s per-pair=%.3fµs",
		hitTotal.Round(time.Millisecond), percentileDuration(hit, 0.50), percentileDuration(hit, 0.95),
		float64(hitTotal.Nanoseconds())/float64(len(hit))/1000.0)
}

func percentileDuration(samples []time.Duration, fraction float64) time.Duration {
	if len(samples) == 0 {
		return 0
	}
	sorted := append([]time.Duration(nil), samples...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	index := int(float64(len(sorted)-1) * fraction)
	return sorted[index]
}
