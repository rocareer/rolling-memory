// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package cluster

import (
	"math"
	"testing"
)

// 构造 n 个单元：前 majority 个共享 AuthorHash=A（泛滥作者），
// 其余各自独立 AuthorHash（稀有作者成对出现时高区分度）。
func buildEnvelopeFloorIndex(t *testing.T, n, majority int, rareAuthor uint64) *Index {
	t.Helper()
	docs := make([]Doc, 0, n)
	for i := 0; i < n; i++ {
		var author uint64
		if i < majority {
			author = 0xAAAA
		} else {
			author = rareAuthor
		}
		docs = append(docs, Doc{
			UnitID:     string(rune('a'+i%26)) + string(rune('0'+i/26)),
			OccurredAt: int64(1700000000 + i*86400*30), // 拉开日期避免同日耦合
			AuthorHash: author,
			Terms:      map[uint64]Term{uint64(1000 + i): {Hash: uint64(1000 + i)}},
			Counts:     map[uint64]int{uint64(1000 + i): 1},
		})
	}
	return BuildIndex(docs)
}

// unitPosByAuthor 返回携带指定 AuthorHash 的全部索引位（BuildIndex 可能重排单元）。
func unitPosByAuthor(idx *Index, author uint64) []int32 {
	var out []int32
	for u := 0; u < idx.N && u < len(idx.UnitMeta); u++ {
		if idx.UnitMeta[u].AuthorHash == author {
			out = append(out, int32(u))
		}
	}
	return out
}

// TestEnvelopeFloorCollisionSurprisal 钉住信封地板的归一化碰撞惊奇度
// 公式 floor = (1/e) * (-ln(df/N)/ln N) 的四个解析锚点。
func TestEnvelopeFloorCollisionSurprisal(t *testing.T) {
	const n = 100
	idx := buildEnvelopeFloorIndex(t, n, n-2, 0xBBBB)
	eng := NewEngine(idx)
	st := eng.ensureClusterState()

	invE := 1.0 / math.E
	majPos := unitPosByAuthor(idx, 0xAAAA)
	rarePos := unitPosByAuthor(idx, 0xBBBB)
	if len(majPos) != n-2 || len(rarePos) != 2 {
		t.Fatalf("作者分布装配错误: majority=%d rare=%d", len(majPos), len(rarePos))
	}

	// 1) 泛滥作者（df=98/100）：增益 -ln(0.98)/ln(100) ≈ 0.00439，地板趋零。
	gotMaj := st.computeDocSubjectCos(idx, majPos[0], majPos[1])
	wantMaj := invE * (-math.Log(98.0/100.0) / math.Log(100.0))
	if math.Abs(gotMaj-wantMaj) > 1e-12 {
		t.Fatalf("泛滥作者地板失准: got=%.12f want=%.12f", gotMaj, wantMaj)
	}
	if gotMaj > 0.01 {
		t.Fatalf("泛滥作者地板应趋零: got=%.6f", gotMaj)
	}

	// 2) 稀有作者（df=2/100）：增益 -ln(0.02)/ln(100) ≈ 0.8495，接近满额。
	gotRare := st.computeDocSubjectCos(idx, rarePos[0], rarePos[1])
	wantRare := invE * (-math.Log(2.0/100.0) / math.Log(100.0))
	if math.Abs(gotRare-wantRare) > 1e-12 {
		t.Fatalf("稀有作者地板失准: got=%.12f want=%.12f", gotRare, wantRare)
	}
	if gotRare <= gotMaj || gotRare < 0.25 {
		t.Fatalf("稀有作者应显著高于泛滥作者: rare=%.6f majority=%.6f", gotRare, gotMaj)
	}

	// 3) 跨作者对：无共享键，地板为 0（词项亦不相交）。
	if got := st.computeDocSubjectCos(idx, majPos[0], rarePos[1]); got != 0 {
		t.Fatalf("跨作者地板应为零: got=%.6f", got)
	}

	// 4) 上界：任何地板不得超过 1/e。
	if gotRare > invE || gotMaj > invE {
		t.Fatalf("地板越过 1/e 上界: rare=%.6f majority=%.6f", gotRare, gotMaj)
	}
}

// TestEnvelopeFloorDegenerateScales 钉住零极值与退化态：
// 零哈希、N=1、全库同作者（df=N ⟹ 地板精确为 0）。
func TestEnvelopeFloorDegenerateScales(t *testing.T) {
	// 全库同一作者：碰撞是必然事件，地板精确为 0。
	idxAll := buildEnvelopeFloorIndex(t, 50, 50, 0)
	engAll := NewEngine(idxAll)
	stAll := engAll.ensureClusterState()
	if got := stAll.computeDocSubjectCos(idxAll, 0, 1); got != 0 {
		t.Fatalf("全库同作者地板应精确为零: got=%.12f", got)
	}

	// 零哈希：信封缺位，不触发任何地板。
	docsZero := []Doc{
		{UnitID: "z1", OccurredAt: 1700000000, Terms: map[uint64]Term{1: {Hash: 1}}, Counts: map[uint64]int{1: 1}},
		{UnitID: "z2", OccurredAt: 1800000000, Terms: map[uint64]Term{2: {Hash: 2}}, Counts: map[uint64]int{2: 1}},
	}
	idxZero := BuildIndex(docsZero)
	engZero := NewEngine(idxZero)
	stZero := engZero.ensureClusterState()
	if got := stZero.computeDocSubjectCos(idxZero, 0, 1); got != 0 {
		t.Fatalf("零哈希地板应为零: got=%.12f", got)
	}

	// N=1：ln(1)=0 的除零退化态必须 fail-safe 返 0 增益。
	docsOne := []Doc{{UnitID: "o1", OccurredAt: 1700000000, AuthorHash: 7, Terms: map[uint64]Term{1: {Hash: 1}}, Counts: map[uint64]int{1: 1}}}
	idxOne := BuildIndex(docsOne)
	engOne := NewEngine(idxOne)
	stOne := engOne.ensureClusterState()
	if g := stOne.envelopeKeyGain(7, idxOne.N); g != 0 || math.IsNaN(g) || math.IsInf(g, 0) {
		t.Fatalf("N=1 增益应 fail-safe 为零: got=%v", g)
	}
}
