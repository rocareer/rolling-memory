// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package cluster

import (
	"math"
	"sort"
)

// invE 是普适常数 e 的倒数：一跳透射率取 invE，二跳取 invE²（逐跳几何衰减）。
const invE = 1.0 / math.E

// 单次查询的扩散计算预算（非评分参数）按种子能量组消费。
// 跨过预算边界的等能量组完整展开，因此预算是软上限。
const (
	DefaultHop1Expansions = 1024
	DefaultHop2Expansions = 512
)

// EnergyMode 选择直接命中的能量口径。
type EnergyMode int

const (
	// EnergyMutualInfo：E(u) = I(u)，候选与查询共享原子的自信息和（Nats）。
	// 共享符号集合的信息能量本身就是可加自信息之和，无额外归一。
	EnergyMutualInfo EnergyMode = iota
	// EnergyDensity：E(u) = I(u)²/H(u)，共享信息密度口径，对长文档惩罚强（对照组）。
	EnergyDensity
)

// rmFlagDocGainMask 是文档侧声明位增益的位掩码：bit2 数字 / bit5 焦点头 /
// bit6 强定界包络 / bit7 数值-量纲对（与 blindkit / engine/term 位序一致）。
// 可移植核零仓内依赖，故为字面量。
const rmFlagDocGainMask uint8 = 1<<2 | 1<<5 | 1<<6 | 1<<7

// rmFlagCompanion 是伴随修饰词槽的声明位掩码（bit4 FlagIsHarmonicCompanion）。
const rmFlagCompanion uint8 = 1 << 4

// Phase3Candidate 承载四步流水线在完成前两簇（Stage 1 EntityCluster 与 Stage 2
// SpacetimeCluster）计算后、向可选 Phase-3 关系簇外挂（如 RB 云端独占的
// relation-cluster extension RelationCluster）暴露的只读候选快照。
//
// 设计边界：
//   - rm 核心自身 100% 不依赖、不导入任何外部 knn 包；
//   - 当未挂载外挂或开关关闭时，rm 核心不分配该切片，保持零额外开销。
type Phase3Candidate struct {
	Unit        int32   // Index.UnitIDs 中的单元下标
	DocID       string  // 单元唯一标识 Index.UnitIDs[Unit]
	Stage2Nats  float64 // 前两簇合算的基础信息能量（Nats）
	CoreLexNats float64 // 剥离高频游离碎片后的核心词项匹配能量（Nats）
	StructGate  float64 // 主锚条件化结构门因子（[1/e, 1.0]）
}

// Phase3Delta 表示可选 Phase-3 外挂对单个候选单元返回的非负信息能量增量（单位：Nats）。
// Hop1Nats 对应一跳近邻/伴随团共振增益，Hop2Nats 对应二跳三阶张量残差桥接增益。
// 任何负数或非有限值（NaN/Inf）在接入埋点处均被安全钳位为 0，确保外挂绝不削弱或破坏
// rm 核心原有的 Stage 1+2 能量分布。
type Phase3Delta struct {
	Unit     int32
	Hop1Nats float64
	Hop2Nats float64
}

// Phase3Plugin 定义 rm 四步双簇内核的可插拔 Phase-3 关系簇扩展接口（非侵入式埋点契约）。
//
// 接入与隔离规范：
//  1. 端侧本地模式（Rolling-Memory）保持 Phase3Plugin == nil 且 EnablePhase3Plugin == false，
//     仅运行 EntityCluster + SpacetimeCluster 双簇内核；
//  2. 云端引擎（cloud engine）可通过 Engine.SetPhase3Plugin(plugin, true) 动态挂载
//     relation-cluster extension 等高阶关系簇算子，或通过 Engine.EnablePhase3(false) 随时一键关闭；
//  3. 实现方仅依据候选自身信息能量与语料统计量返回非负增量（严格遵守名次盲性）。
type Phase3Plugin interface {
	AugmentRMStage2(idx *Index, qTerms map[uint64]Term, qGroups map[uint64]float64, totalQueryMass float64, stage2 []Phase3Candidate) []Phase3Delta
}

type stage3EngineSyncer interface {
	SyncRMEngine(eng *Engine)
}

// Engine 是四步流水线的运行体：索引只读，扩散预算与能量口径可调（用于敏感性实测）。
type Engine struct {
	Index      *Index
	Hop1Budget int
	Hop2Budget int
	Energy     EnergyMode
	// Contraction 开关包含格二次倒排收缩通道（对齐 minimal lattice）。
	Contraction bool
	// FlagsGain 开启文档侧声明位增益（对齐 minimal flagsPerEntry/flagsGain）：
	// 命中原子在该单元的折叠声明位命中 rmFlagDocGainMask 时，该命中质量乘
	// 1 + gain/e；0 关闭。默认 0.5（与 minimal R12 采纳值同源）。
	FlagsGain float64
	// BitsMass 开启 minimal 已验证的查询侧载体宽度调制（sqrt 形式）：
	// 每个命中查询原子的质量按 bitsGainSqrt(声明宽度/语料均宽) 缩放；无声明
	// 宽度时回退语料折叠宽度，仍无宽度则质量原值。
	BitsMass bool
	// TimeKernel 开启扩散透射的时空核（对齐引擎跨簇联动的时空维度）。
	TimeKernel bool
	// ClusterPipeline 默认开启四步簇与短语流水线（EntityCluster × SpacetimeCluster × PhraseDyad）。
	ClusterPipeline bool

	// EnablePhase3Plugin 控制是否启用可选的 Phase-3 外部关系簇外挂埋点（默认 false 关闭）。
	// 仅当 EnablePhase3Plugin == true 且 Phase3Plugin != nil 时，流水线才会在前两簇
	// （EntityCluster + SpacetimeCluster）结算后调用外挂注入非负能量增益；
	// 设为 false 或 Phase3Plugin == nil 时 100% 零开销直通，不侵入 rm 核心逻辑。
	EnablePhase3Plugin bool
	// Phase3Plugin 挂载可选的 Phase-3 关系簇外挂实例（如 RB 云端 relation-cluster extension）。
	// rm 核心包自身不持有任何具体实现，可自由挂载、切换或置 nil 卸载。
	Phase3Plugin Phase3Plugin

	clusterState *rmClusterState
	queryScratch *rmQueryScratch
	mmapUnmap    func() error
}

// Close 释放引擎挂载的任何底层 mmap 磁盘段资源，并在卸载映射前将 Index 上的字符串元数据安全克隆到 Go 堆。
func (e *Engine) Close() error {
	if e == nil || e.mmapUnmap == nil {
		return nil
	}
	if e.Index != nil {
		for u, id := range e.Index.UnitIDs {
			e.Index.UnitIDs[u] = string([]byte(id))
		}
		for u := range e.Index.UnitMeta {
			if len(e.Index.UnitMeta[u].Layer) > 0 {
				e.Index.UnitMeta[u].Layer = string([]byte(e.Index.UnitMeta[u].Layer))
			}
		}
		e.Index.csrFallbackExport = nil
	}
	e.clusterState = nil
	fn := e.mmapUnmap
	e.mmapUnmap = nil
	return fn()
}

// NewEngine 构造流水线运行体（默认开启 ClusterPipeline 与 FlagsGain=0.5）。
func NewEngine(idx *Index) *Engine {
	return &Engine{
		Index:           idx,
		Hop1Budget:      DefaultHop1Expansions,
		Hop2Budget:      DefaultHop2Expansions,
		FlagsGain:       0.5,
		ClusterPipeline: true,
	}
}

// SetPhase3Plugin 挂载或卸载可选的 Phase-3 关系簇外挂，并同步设置启用开关。
// 传入 plugin == nil 或 enabled == false 即可安全关闭外挂，恢复纯双簇内核运行。
func (e *Engine) SetPhase3Plugin(plugin Phase3Plugin, enabled bool) {
	if e == nil {
		return
	}
	e.Phase3Plugin = plugin
	e.EnablePhase3Plugin = enabled && plugin != nil
}

// EnablePhase3 动态开启或关闭已挂载的 Phase-3 外挂（无需卸载外挂实例即可自由切换对比）。
func (e *Engine) EnablePhase3(enabled bool) {
	if e == nil {
		return
	}
	e.EnablePhase3Plugin = enabled && e.Phase3Plugin != nil
}

// ResetScratch 清空当前 Engine 实例绑定的查询暂存区，使浅拷贝后的并发协程各自懒分配独立 Scratch。
func (e *Engine) ResetScratch() {
	if e == nil {
		return
	}
	e.queryScratch = nil
}

// DocEntryView 表示 CSR 文档正向表中的单条紧凑词项记录（零堆分配只读视图，供跨模块如 knn 共享底层 CSR 使用）。
type DocEntryView struct {
	ID       uint64
	Group    uint64
	TF       int32
	Bits     uint16
	Flags    uint8
	Role     TermRole
	ExtFlags uint16
}

// DocEntryCount 返回单元 u 在 CSR 正向表中的去重词项条目数。
func (e *Engine) DocEntryCount(u int) int {
	if e == nil || e.Index == nil || u < 0 || u >= e.Index.N {
		return 0
	}
	st := e.ensureClusterState()
	if st == nil || u+1 >= len(st.docRowPtr) {
		return 0
	}
	return int(st.docRowPtr[u+1] - st.docRowPtr[u])
}

// ForEachDocEntry 以零堆分配顺序遍历单元 u 在 CSR 正向表中的全部词项条目（兼容内存态与 .zcsr mmap 零拷贝态）。
func (e *Engine) ForEachDocEntry(u int, fn func(DocEntryView)) {
	if e == nil || e.Index == nil || u < 0 || u >= e.Index.N || fn == nil {
		return
	}
	st := e.ensureClusterState()
	if st == nil || u+1 >= len(st.docRowPtr) {
		return
	}
	for _, entry := range st.docEntriesOf(int32(u)) {
		role, extFlags := unpackTermRoleExt(entry.roleExt)
		fn(DocEntryView{
			ID:       entry.id,
			Group:    entry.group,
			TF:       entry.tf,
			Bits:     entry.bits,
			Flags:    entry.flags,
			Role:     role,
			ExtFlags: extFlags,
		})
	}
}

// ExportDocs 从当前 Engine（含内存态 Index 或 .zcsr 磁盘 mmap 零拷贝态）无损还原全部文档快照。
func (e *Engine) ExportDocs() []Doc {
	if e == nil || e.Index == nil || e.Index.N == 0 {
		return nil
	}
	if e.Index.Postings != nil || (e.clusterState == nil && len(e.Index.DocTerms) == e.Index.N) {
		return e.Index.ExportDocs()
	}
	st := e.clusterState
	if st == nil {
		return nil
	}
	idx := e.Index
	docs := make([]Doc, idx.N)
	for u := 0; u < idx.N; u++ {
		var meta DocMeta
		if u < len(idx.UnitMeta) {
			meta = idx.UnitMeta[u]
		}
		if u < len(st.refTimes) && meta.OccurredAt == 0 && meta.NarratedAt == 0 && meta.IngestedAt == 0 {
			meta.OccurredAt = st.refTimes[u]
		}
		entries := st.docEntriesOf(int32(u))
		atoms := make(map[uint64]Term, len(entries))
		counts := make(map[uint64]int, len(entries))
		var positions map[uint64][]TermPosition
		for _, entry := range entries {
			packedPos := st.entryPositionsOf(entry)
			var sent, slot uint16
			if len(packedPos) > 0 {
				if positions == nil {
					positions = make(map[uint64][]TermPosition, len(entries))
				}
				pl := make([]TermPosition, len(packedPos))
				for i, pp := range packedPos {
					s := int(pp >> 16)
					sl := int(pp & 0xFFFF)
					if st.widePos != nil {
						if wp, ok := st.widePos[entry.posOff+uint32(i)]; ok {
							s = wp.Sentence
							sl = wp.Slot
						}
					}
					pl[i] = TermPosition{Sentence: s, Slot: sl}
					if i == 0 {
						if s+1 > 0xFFFF {
							sent = 0xFFFF
						} else if s >= 0 {
							sent = uint16(s + 1)
						}
						if sl+1 > 0xFFFF {
							slot = 0xFFFF
						} else if sl >= 0 {
							slot = uint16(sl + 1)
						}
					}
				}
				positions[entry.id] = pl
			}
			role, extFlags := unpackTermRoleExt(entry.roleExt)
			if u < len(idx.UnitExtFlags) && idx.UnitExtFlags[u] != nil {
				extFlags |= idx.UnitExtFlags[u][entry.id]
			}
			atoms[entry.id] = Term{
				Hash:     entry.id,
				Bits:     entry.bits,
				Flags:    entry.flags,
				Role:     role,
				ExtFlags: extFlags,
				Group:    entry.group,
				Sent:     sent,
				Slot:     slot,
			}
			counts[entry.id] = int(entry.tf)
		}
		var cited []string
		if len(meta.CitedIDs) > 0 {
			// Clone each ID off any mmap-backed string so the exported snapshot
			// survives Engine.Close() exactly like UnitID/Layer above.
			cited = make([]string, len(meta.CitedIDs))
			for ci, cid := range meta.CitedIDs {
				cited[ci] = string([]byte(cid))
			}
		}
		docs[u] = Doc{
			UnitID:     string([]byte(idx.UnitIDs[u])),
			OccurredAt: meta.OccurredAt,
			NarratedAt: meta.NarratedAt,
			IngestedAt: meta.IngestedAt,
			AuthorHash: meta.AuthorHash,
			DocHash:    meta.DocHash,
			BlockHash:  meta.BlockHash,
			LineStart:  meta.LineStart,
			LineEnd:    meta.LineEnd,
			Layer:      string([]byte(meta.Layer)),
			CitedIDs:   cited,
			Terms:      atoms,
			Counts:     counts,
			Positions:  positions,
		}
	}
	return docs
}

// AppendDocs 将新增或更新的文档批次流式并入当前 Engine（支持内存态与 .zcsr mmap 零拷贝态热升迁），
// 自动同步底层 Index 与双簇 CSR 状态，保证分批流式写入与一次性全量构建 100% 0-ULP 逐位全等。
func (e *Engine) AppendDocs(docs []Doc) {
	if e == nil || len(docs) == 0 {
		return
	}
	existing := e.ExportDocs()
	combined := make([]Doc, 0, len(existing)+len(docs))
	combined = append(combined, existing...)
	combined = append(combined, docs...)
	rebuilt := BuildIndex(combined)
	if e.mmapUnmap != nil {
		_ = e.Close()
	}
	e.Index = rebuilt
	e.clusterState = nil
	e.queryScratch = nil
	if e.ClusterPipeline {
		_ = e.ensureClusterState()
	}
	if e.Phase3Plugin != nil {
		if syncer, ok := e.Phase3Plugin.(stage3EngineSyncer); ok {
			syncer.SyncRMEngine(e)
		}
	}
}

// DeleteDocs 按 unitIDs 从当前 Engine（支持内存态与 .zcsr mmap 零拷贝态）精确撤回指定记忆单元，
// 自动同步底层 Index 与双簇 CSR 状态，保证删除后与从未入库过被删单元的全量构建 100% 0-ULP 逐位全等。
func (e *Engine) DeleteDocs(unitIDs ...string) int {
	if e == nil || e.Index == nil || e.Index.N == 0 || len(unitIDs) == 0 {
		return 0
	}
	delSet := make(map[string]struct{}, len(unitIDs))
	for _, id := range unitIDs {
		if id != "" {
			delSet[id] = struct{}{}
		}
	}
	if len(delSet) == 0 {
		return 0
	}
	existing := e.ExportDocs()
	kept := make([]Doc, 0, len(existing))
	removed := 0
	for _, d := range existing {
		if _, drop := delSet[d.UnitID]; drop {
			removed++
			continue
		}
		kept = append(kept, d)
	}
	if removed == 0 {
		return 0
	}
	rebuilt := BuildIndex(kept)
	if e.mmapUnmap != nil {
		_ = e.Close()
	}
	e.Index = rebuilt
	e.clusterState = nil
	e.queryScratch = nil
	if e.ClusterPipeline && rebuilt.N > 0 {
		_ = e.ensureClusterState()
	}
	if e.Phase3Plugin != nil {
		if syncer, ok := e.Phase3Plugin.(stage3EngineSyncer); ok {
			syncer.SyncRMEngine(e)
		}
	}
	return removed
}

// bitsGainSqrt 是 minimal 已验证的 sqrt 形式宽度增益 sqrt(1+ratio)：
// ratio=0 时恒为 1，单调无硬阈值。
func bitsGainSqrt(ratio float64) float64 {
	if ratio <= 0 {
		return 1
	}
	return math.Sqrt(1 + ratio)
}

// queryWeights maps every query identity present in the corpus to the mass that
// enters the direct accumulation: the plain corpus surprisal, optionally
// scaled by the query-side carrier-width modulation (BitsMass).
func (e *Engine) queryWeights(queryHashes []uint64, qTerms map[uint64]Term) map[uint64]float64 {
	out := make(map[uint64]float64, len(queryHashes))
	meanBits := e.Index.MeanBits
	if meanBits <= 0 && e.clusterState != nil {
		meanBits = e.clusterState.entryMeanBits
	}
	for _, hash := range queryHashes {
		mass, ok := e.Index.TermMass(hash)
		if !ok {
			continue
		}
		if e.BitsMass && meanBits > 0 {
			bits := qTerms[hash].Bits
			if bits == 0 {
				bits = e.Index.TermBitsOf(hash)
			}
			if bits > 0 {
				mass *= bitsGainSqrt(float64(bits) / meanBits)
			}
		}
		out[hash] = mass
	}
	return out
}

// Hit 是一条命中的能量明细（全部为 Nats）。
type Hit struct {
	UnitID string
	Energy float64
	Direct float64
	Hop1   float64
	Hop2   float64
}

// Search 执行 term → index → expand → rank 四步。
//
//  1. 词项由调用方给出（Query 内完成抽取）；
//  2. 倒排精确匹配：候选共享自信息 I(u) = Σ m(a)（a ∈ A(Q) ∩ A(u)）；
//  3. 扩散：一跳经非查询桥词项 b，二跳经一跳候选再扩散一跳；
//  4. 排序：默认按候选自身能量 E(u) = I(u) + Δ1 + Δ2，无名次耦合。
func (e *Engine) Search(qTerms map[uint64]Term, topK int) []Hit {
	return e.SearchWithPositions(qTerms, nil, topK)
}

func (e *Engine) SearchWithPositions(qTerms map[uint64]Term, qPositions map[uint64][]TermPosition, topK int) []Hit {
	idx := e.Index
	if idx == nil || idx.N == 0 || len(qTerms) == 0 {
		return nil
	}
	if e.canUseClusterPipeline() {
		return e.searchClustersWithPositions(qTerms, qPositions, topK)
	}

	shared := make(map[int32]float64, 512)
	queryHashes := make([]uint64, 0, len(qTerms))
	for hash := range qTerms {
		queryHashes = append(queryHashes, hash)
	}
	sort.Slice(queryHashes, func(i, j int) bool { return queryHashes[i] < queryHashes[j] })
	masses := e.queryWeights(queryHashes, qTerms)
	for _, hash := range queryHashes {
		mass, ok := masses[hash]
		if !ok {
			continue
		}
		docGainMask := uint8(1<<2 | 1<<5 | 1<<7)
		if qTerms[hash].Flags&(1<<6) != 0 {
			docGainMask |= 1 << 6
		}
		if idx.Postings != nil {
			for _, p := range idx.Postings[hash] {
				weight := mass
				if e.FlagsGain != 0 && int(p.Unit) < len(idx.UnitFlags) {
					if unitFlags := idx.UnitFlags[p.Unit]; unitFlags != nil && unitFlags[hash]&docGainMask != 0 {
						weight = mass * (1 + e.FlagsGain/math.E)
					}
				}
				shared[p.Unit] += weight
			}
		} else if e.clusterState != nil {
			st := e.clusterState
			for _, p := range st.termPostings(hash) {
				weight := mass
				if e.FlagsGain != 0 {
					entries := st.docEntriesOf(p.unit)
					if int(p.entry) >= 0 && int(p.entry) < len(entries) && entries[p.entry].flags&docGainMask != 0 {
						weight = mass * (1 + e.FlagsGain/math.E)
					}
				}
				shared[p.unit] += weight
			}
		}
	}

	direct := make(map[int32]float64, len(shared))
	directUnits := make([]int32, 0, len(shared))
	for unit, info := range shared {
		var total float64
		if int(unit) < len(idx.DocMass) {
			total = idx.DocMass[unit]
		} else if e.clusterState != nil {
			entries := e.clusterState.docEntriesOf(unit)
			atoms := make([]uint64, len(entries))
			for i, ent := range entries {
				atoms[i] = ent.id
			}
			sort.Slice(atoms, func(a, b int) bool {
				ma, _ := idx.TermMass(atoms[a])
				mb, _ := idx.TermMass(atoms[b])
				if ma == mb {
					return atoms[a] < atoms[b]
				}
				return ma > mb
			})
			for _, h := range atoms {
				m, _ := idx.TermMass(h)
				total += m
			}
		}
		if total <= 0 {
			continue
		}
		if e.Energy == EnergyDensity {
			direct[unit] = info * info / total
		} else {
			direct[unit] = info
		}
		directUnits = append(directUnits, unit)
	}
	// Residual proximity (verified in minimal, aligned here): while no
	// candidate fully covers the query mass, each candidate's matched pairs add
	// the harmonic pair mass 2*ma*mb/(ma+mb) weighted by the same-sentence
	// closest-slot kernel exp(-(d-1)/e), scaled by the pool residual
	// max(0, 1 - best coverage). Positions are in-memory derived data; units
	// without them stay exactly at their direct energy.
	totalQueryMass := 0.0
	for _, hash := range queryHashes {
		totalQueryMass += masses[hash]
	}
	if residual := poolResidualFrom(shared, totalQueryMass); residual > 0 {
		for unit := range direct {
			if (int(unit) < len(idx.UnitPositions) && len(idx.UnitPositions[unit]) > 0) || e.clusterState != nil {
				direct[unit] += residual * e.proximityIncrement(unit, masses)
			}
		}
	}
	// Containment-lattice contraction (aligned from minimal): a candidate
	// matching at least two query atoms whose joint intersection shrinks below
	// the rarest single frequency gains ln((minDF+1)/(intersection+1)) weighted
	// by mass/total, with minDF and intersection measured over the candidate
	// pool. The channel is switchable (eval-atm -contraction) because the
	// minimal side measured its default as zero/negative in the current
	// configuration.
	if e.Contraction && totalQueryMass > 0 {
		e.contractionIncrements(direct, queryHashes, masses, totalQueryMass)
	}
	sortByEnergy(directUnits, direct, idx)

	// Spacetime x entity cross-cluster coupling (aligned from the engine's
	// linkage): with TimeKernel enabled, every hop transmission is weighted by
	// the continuous time kernel exp(-|dt|/tau) between the seed's and the
	// neighbor's reference clocks (OccurredAt -> NarratedAt -> IngestedAt),
	// where tau is the endogenous mean positive consecutive gap. Units without
	// clocks keep the unweighted transmission, so absent metadata is neutral.
	var timeTau float64
	if e.TimeKernel {
		timeTau = idx.meanUnitGap()
		if timeTau <= 0 && e.clusterState != nil {
			timeTau = e.clusterState.meanGap
		}
	}
	hop1 := e.spread(directUnits, direct, qTerms, invE, e.Hop1Budget, timeTau)
	hop1Only := make([]int32, 0, len(hop1))
	for unit, energy := range hop1 {
		if _, isDirect := direct[unit]; isDirect {
			continue
		}
		if energy > 0 {
			hop1Only = append(hop1Only, unit)
		}
	}
	sortByEnergy(hop1Only, hop1, idx)

	hop2 := e.spread(hop1Only, hop1, qTerms, invE*invE, e.Hop2Budget, timeTau)

	pool := make(map[int32]struct{}, len(direct)+len(hop1)+len(hop2))
	for unit := range direct {
		pool[unit] = struct{}{}
	}
	for unit := range hop1 {
		pool[unit] = struct{}{}
	}
	for unit := range hop2 {
		pool[unit] = struct{}{}
	}

	// 可选 Phase-3 关系簇外挂接入埋点（非簇回退路径）：
	// 仅在 EnablePhase3Plugin == true 且 Phase3Plugin != nil 时触发；默认关闭时零开销跳过。
	if e.EnablePhase3Plugin && e.Phase3Plugin != nil && len(pool) > 0 {
		stage2 := make([]Phase3Candidate, 0, len(pool))
		for unit := range pool {
			e2 := direct[unit] + hop1[unit] + hop2[unit]
			if e2 > 0 {
				stage2 = append(stage2, Phase3Candidate{
					Unit:        unit,
					DocID:       idx.UnitIDs[unit],
					Stage2Nats:  e2,
					CoreLexNats: direct[unit],
					StructGate:  1.0,
				})
			}
		}
		sort.Slice(stage2, func(i, j int) bool {
			if stage2[i].Stage2Nats != stage2[j].Stage2Nats {
				return stage2[i].Stage2Nats > stage2[j].Stage2Nats
			}
			return stage2[i].Unit < stage2[j].Unit
		})
		for _, delta := range e.Phase3Plugin.AugmentRMStage2(idx, qTerms, masses, totalQueryMass, stage2) {
			if delta.Unit < 0 || int(delta.Unit) >= idx.N {
				continue
			}
			if !math.IsNaN(delta.Hop1Nats) && !math.IsInf(delta.Hop1Nats, 0) && delta.Hop1Nats > 0 {
				hop1[delta.Unit] += delta.Hop1Nats
				pool[delta.Unit] = struct{}{}
			}
			if !math.IsNaN(delta.Hop2Nats) && !math.IsInf(delta.Hop2Nats, 0) && delta.Hop2Nats > 0 {
				hop2[delta.Unit] += delta.Hop2Nats
				pool[delta.Unit] = struct{}{}
			}
		}
	}

	out := make([]Hit, 0, len(pool))
	for unit := range pool {
		energy := direct[unit] + hop1[unit] + hop2[unit]
		if energy <= 0 {
			continue
		}
		out = append(out, Hit{
			UnitID: string([]byte(idx.UnitIDs[unit])),
			Energy: energy,
			Direct: direct[unit],
			Hop1:   hop1[unit],
			Hop2:   hop2[unit],
		})
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if math.IsNaN(a.Energy) || math.IsNaN(b.Energy) {
			if math.IsNaN(a.Energy) != math.IsNaN(b.Energy) {
				return !math.IsNaN(a.Energy)
			}
			return a.UnitID < b.UnitID
		}
		if a.Energy != b.Energy {
			return a.Energy > b.Energy
		}
		return a.UnitID < b.UnitID
	})
	if topK > 0 && len(out) > topK {
		out = out[:topK]
	}
	return out
}

// poolResidualFrom is max(0, 1 - best coverage) of the query mass over the
// direct candidates: zero when some candidate carries every query atom.
func poolResidualFrom(shared map[int32]float64, totalQueryMass float64) float64 {
	if totalQueryMass <= 0 {
		return 0
	}
	best := 0.0
	for _, info := range shared {
		if info > best {
			best = info
		}
	}
	return math.Max(0, 1-best/totalQueryMass)
}

// contractionIncrements mirrors the minimal pipeline's verified containment
// lattice: for every candidate matching at least two query atoms, the joint
// intersection of its matched set is counted over the candidate pool, and the
// candidate gains ln((minDF+1)/(intersection+1)) * mass / total where minDF is
// the rarest matched atom's candidate frequency and mass the matched query
// mass. Searches iterate the rarest atom's postings; candidates are keyed by
// the shared coverage map, so the accumulation is order-independent and
// bit-for-bit reproducible.
// latticeCandidateSet is one candidate's folded atom membership plus its
// matched query subset (ascending), used by the containment-lattice channel.
type latticeCandidateSet struct {
	members map[uint64]struct{}
	matched []uint64
}

func (e *Engine) contractionIncrements(direct map[int32]float64, queryHashes []uint64, masses map[uint64]float64, totalQueryMass float64) {
	idx := e.Index
	st := e.clusterState
	sets := make(map[int32]latticeCandidateSet, len(direct))
	candDF := make(map[uint64]int, len(queryHashes))
	for unit := range direct {
		var atoms []uint64
		if int(unit) < len(idx.DocTerms) && len(idx.DocTerms[unit]) > 0 {
			atoms = idx.DocTerms[unit]
		} else if st != nil {

			entries := st.docEntriesOf(unit)
			atoms = make([]uint64, len(entries))
			for i, ent := range entries {
				atoms[i] = ent.id
			}
		}
		if len(atoms) == 0 {
			continue
		}
		set := latticeCandidateSet{members: make(map[uint64]struct{}, len(atoms))}
		for _, hash := range atoms {
			set.members[hash] = struct{}{}
		}
		for _, hash := range queryHashes {
			if _, ok := set.members[hash]; ok {
				set.matched = append(set.matched, hash)
			}
		}
		for _, hash := range set.matched {
			candDF[hash]++
		}
		sets[unit] = set
	}
	// The contraction delta depends only on the matched set, so every distinct
	// set is evaluated once and shared by all its carriers (mirrors the
	// minimal pipeline's per-set frequency cache); this keeps the work bounded
	// by the number of distinct matched sets instead of the candidate count.
	deltaBySet := make(map[string]float64, len(sets))
	for unit, set := range sets {
		if len(set.matched) < 2 {
			continue
		}
		key := matchedSetKey(set.matched)
		delta, ok := deltaBySet[key]
		if !ok {
			delta = e.contractionDelta(set.matched, sets, candDF, masses, totalQueryMass)
			deltaBySet[key] = delta
		}
		if delta > 0 {
			direct[unit] += delta
		}
	}
}

// matchedSetKey encodes an ascending matched set into a stable cache key using
// only builtins (the portable core restricts imports to math and sort).
func matchedSetKey(matched []uint64) string {
	key := make([]byte, 0, len(matched)*8)
	for _, hash := range matched {
		for shift := 0; shift < 64; shift += 8 {
			key = append(key, byte(hash>>uint(shift)))
		}
	}
	return string(key)
}

// contractionDelta computes the containment-lattice contraction surprisal of
// one matched set: ln((minDF+1)/(intersection+1)) * mass / total where minDF is
// the rarest matched atom's candidate frequency, intersection the number of
// candidates containing every matched atom, and mass the matched query mass.
// The result is strictly non-negative: minDF <= 1 and intersection >= minDF
// both yield zero (the formula already equals zero there).
func (e *Engine) contractionDelta(matched []uint64, sets map[int32]latticeCandidateSet, candDF map[uint64]int, masses map[uint64]float64, totalQueryMass float64) float64 {
	rarest := matched[0]
	minDF := candDF[rarest]
	var mass float64
	for _, hash := range matched {
		mass += masses[hash]
		if candDF[hash] < minDF {
			minDF = candDF[hash]
		}
	}
	if minDF <= 1 {
		return 0
	}
	intersection := 0
	if e.Index.Postings != nil {
		for _, posting := range e.Index.Postings[rarest] {
			other, ok := sets[posting.Unit]
			if !ok {
				continue
			}
			contains := true
			for _, hash := range matched {
				if _, ok := other.members[hash]; !ok {
					contains = false
					break
				}
			}
			if contains {
				intersection++
			}
		}
	} else if e.clusterState != nil {
		for _, posting := range e.clusterState.termPostings(rarest) {
			other, ok := sets[posting.unit]
			if !ok {
				continue
			}
			contains := true
			for _, hash := range matched {
				if _, ok := other.members[hash]; !ok {
					contains = false
					break
				}
			}
			if contains {
				intersection++
			}
		}
	}
	if intersection <= 0 || intersection >= minDF {
		return 0
	}
	return math.Log(float64(minDF+1)/float64(intersection+1)) * mass / totalQueryMass
}

// proximityIncrement mirrors the minimal pipeline's verified mechanism: over
// the unit's matched query identities, every pair (a, b) contributes the
// harmonic mean 2*ma*mb/(ma+mb) weighted by exp(-(d-1)/e) of the closest
// same-sentence occurrence distance d. Pairs without positions contribute
// zero. The matched scan follows the unit's fixed DocTerms order, so the
// accumulation is bit-for-bit reproducible.
func (e *Engine) proximityIncrement(unit int32, masses map[uint64]float64) float64 {
	if len(masses) < 2 {
		return 0
	}
	idx := e.Index
	if int(unit) < len(idx.UnitPositions) && len(idx.UnitPositions[unit]) > 0 && int(unit) < len(idx.DocTerms) {
		positions := idx.UnitPositions[unit]
		matched := make([]uint64, 0, len(positions))
		for _, hash := range idx.DocTerms[unit] {
			if _, ok := masses[hash]; ok {
				matched = append(matched, hash)
			}
		}

		if len(matched) < 2 {
			return 0
		}
		var energy float64
		for at, left := range matched {
			leftPositions := positions[left]
			if len(leftPositions) == 0 {
				continue
			}
			leftMass := masses[left]
			for _, right := range matched[at+1:] {
				rightMass := masses[right]
				if leftMass+rightMass <= 0 {
					continue
				}
				closest := closestTermSlot(leftPositions, positions[right])
				if closest < 0 {
					continue
				}
				energy += 2 * leftMass * rightMass / (leftMass + rightMass) * math.Exp(-(float64(closest)-1)/math.E)
			}
		}
		return energy
	}
	st := e.clusterState
	if st == nil {
		return 0
	}
	type matchedEntryPos struct {
		id      uint64
		idxMass float64
		pos     []uint32
	}
	entries := st.docEntriesOf(unit)
	matched := make([]matchedEntryPos, 0, len(entries))
	for _, ent := range entries {
		if _, ok := masses[ent.id]; !ok {
			continue
		}
		m, _ := idx.TermMass(ent.id)
		matched = append(matched, matchedEntryPos{
			id:      ent.id,
			idxMass: m,
			pos:     st.entryPositionsOf(ent),
		})
	}
	if len(matched) < 2 {
		return 0
	}
	sort.Slice(matched, func(a, b int) bool {
		if matched[a].idxMass == matched[b].idxMass {
			return matched[a].id < matched[b].id
		}
		return matched[a].idxMass > matched[b].idxMass
	})
	var energy float64
	for at, left := range matched {
		if len(left.pos) == 0 {
			continue
		}
		leftMass := masses[left.id]
		for _, right := range matched[at+1:] {
			if len(right.pos) == 0 {
				continue
			}
			rightMass := masses[right.id]
			if leftMass+rightMass <= 0 {
				continue
			}
			closest := closestPackedSlot(left.pos, right.pos)
			if closest < 0 {
				continue
			}
			energy += 2 * leftMass * rightMass / (leftMass + rightMass) * math.Exp(-(float64(closest)-1)/math.E)
		}
	}
	return energy
}

// closestTermSlot returns the smallest same-sentence slot distance between two
// ascending position lists, or -1 when they share no sentence.
func closestTermSlot(left, right []TermPosition) int {
	nL, nR := len(left), len(right)
	if nL == 0 || nR == 0 {
		return -1
	}
	if left[nL-1].Sentence < right[0].Sentence || right[nR-1].Sentence < left[0].Sentence {
		return -1
	}
	closest := -1
	for x, y := 0, 0; x < nL && y < nR; {
		switch {
		case left[x].Sentence < right[y].Sentence:
			x++
		case left[x].Sentence > right[y].Sentence:
			y++
		default:
			diff := left[x].Slot - right[y].Slot
			if diff < 0 {
				diff = -diff
			}
			if closest < 0 || diff < closest {
				closest = diff
				if closest == 0 {
					return 0
				}
			}
			if left[x].Slot < right[y].Slot {
				x++
			} else {
				y++
			}
		}
	}
	return closest
}

// spread 从种子出发扩散一跳：经桥原子 b 到达 v 的透射能量为
//
//	T · E(seed) · m(b) / min(H(seed), H(v))
//
// 其中 T 为逐跳透射率（e^-hop），m(b) 为桥原子信息能量，H 为文档总信息能量。
// 桥纯度是无量纲比值（[0,1] 内），不含任何名次量。
//
// 多个 (种子, 桥原子) 路径到达同一邻居时取最大值（扩散激活取 MAX 不求和）。
// 种子须按能量降序排列；预算只在完整的等能量种子组之间检查，组内所有桥均展开。
// 因此边界组可能超过预算，时间上界仍为被选种子所有桥的倒排遍历工作量。
func (e *Engine) spread(seeds []int32, seedEnergy map[int32]float64, qTerms map[uint64]Term, transmission float64, budget int, timeTau float64) map[int32]float64 {
	idx := e.Index
	st := e.clusterState
	out := make(map[int32]float64, 256)
	if idx == nil || budget <= 0 {
		return out
	}
	var docMassCache []float64
	docMassOf := func(u int32) float64 {
		if u < 0 {
			return 0
		}
		if int(u) < len(idx.DocMass) {
			return idx.DocMass[u]
		}
		if int(u) >= idx.N {
			return 0
		}
		if docMassCache == nil {
			docMassCache = make([]float64, idx.N)
			for i := range docMassCache {
				docMassCache[i] = -1
			}
		}
		if docMassCache[u] >= 0 {
			return docMassCache[u]
		}
		if st == nil {
			docMassCache[u] = 0
			return 0
		}
		entries := st.docEntriesOf(u)
		atoms := make([]uint64, len(entries))
		for i, ent := range entries {
			atoms[i] = ent.id
		}
		sort.Slice(atoms, func(a, b int) bool {
			ma, _ := idx.TermMass(atoms[a])
			mb, _ := idx.TermMass(atoms[b])
			if ma == mb {
				return atoms[a] < atoms[b]
			}
			return ma > mb
		})
		var sum float64
		for _, h := range atoms {
			m, _ := idx.TermMass(h)
			sum += m
		}
		docMassCache[u] = sum
		return sum
	}
	refTimeOf := func(u int32) int64 {
		if u < 0 {
			return 0
		}
		if int(u) < len(idx.UnitMeta) {
			return idx.UnitMeta[u].RefTime()
		}
		if st != nil && int(u) < len(st.refTimes) {
			return st.refTimes[u]
		}
		return 0
	}
	docTermsOf := func(u int32) []uint64 {
		if int(u) < len(idx.DocTerms) && len(idx.DocTerms[u]) > 0 {
			return idx.DocTerms[u]
		}
		if st == nil {
			return nil
		}
		entries := st.docEntriesOf(u)
		atoms := make([]uint64, len(entries))
		for i, ent := range entries {
			atoms[i] = ent.id
		}
		sort.Slice(atoms, func(a, b int) bool {
			ma, _ := idx.TermMass(atoms[a])
			mb, _ := idx.TermMass(atoms[b])
			if ma == mb {
				return atoms[a] < atoms[b]
			}
			return ma > mb
		})
		return atoms
	}
	remaining := budget
	for start := 0; start < len(seeds) && remaining > 0; {
		seedMass := seedEnergy[seeds[start]]
		end := start + 1
		for end < len(seeds) && seedEnergy[seeds[end]] == seedMass {
			end++
		}
		for _, unit := range seeds[start:end] {
			totalMass := docMassOf(unit)
			if seedMass <= 0 || totalMass <= 0 {
				continue
			}
			seedTime := int64(0)
			if timeTau > 0 {
				seedTime = refTimeOf(unit)
			}
			for _, bridge := range docTermsOf(unit) {
				if _, isQueryTerm := qTerms[bridge]; isQueryTerm {
					continue
				}
				bridgeMass, _ := idx.TermMass(bridge)
				if bridgeMass <= 0 {
					continue
				}
				if remaining > 0 {
					remaining--
				}
				if idx.Postings != nil {
					for _, p := range idx.Postings[bridge] {
						neighbor := p.Unit
						if neighbor == unit {
							continue
						}
						neighborMass := docMassOf(neighbor)
						denom := math.Min(totalMass, neighborMass)
						if denom <= 0 {
							continue
						}
						weight := transmission * seedMass * (bridgeMass / denom)
						if timeTau > 0 && seedTime > 0 {
							if neighborTime := refTimeOf(neighbor); neighborTime > 0 {
								weight *= math.Exp(-math.Abs(float64(seedTime-neighborTime)) / timeTau)
							}
						}
						if weight > out[neighbor] {
							out[neighbor] = weight
						}
					}
				} else if st != nil {
					for _, p := range st.termPostings(bridge) {
						neighbor := p.unit
						if neighbor == unit {
							continue
						}
						neighborMass := docMassOf(neighbor)
						denom := math.Min(totalMass, neighborMass)
						if denom <= 0 {
							continue
						}
						weight := transmission * seedMass * (bridgeMass / denom)
						if timeTau > 0 && seedTime > 0 {
							if neighborTime := refTimeOf(neighbor); neighborTime > 0 {
								weight *= math.Exp(-math.Abs(float64(seedTime-neighborTime)) / timeTau)
							}
						}
						if weight > out[neighbor] {
							out[neighbor] = weight
						}
					}
				}
			}
		}
		start = end
	}
	return out
}

// sortByEnergy 按候选自身能量降序排序，等能量按 UnitID 字典序（与名次位次无关）。
func sortByEnergy(units []int32, energy map[int32]float64, idx *Index) {
	sort.Slice(units, func(a, b int) bool {
		ea, eb := energy[units[a]], energy[units[b]]
		if math.IsNaN(ea) || math.IsNaN(eb) {
			if math.IsNaN(ea) != math.IsNaN(eb) {
				return !math.IsNaN(ea)
			}
			return idx.UnitIDs[units[a]] < idx.UnitIDs[units[b]]
		}
		if ea == eb {
			return idx.UnitIDs[units[a]] < idx.UnitIDs[units[b]]
		}
		return ea > eb
	})
}

func packTermPosition(sent, slot int) uint32 {
	if sent < 0 {
		sent = 0
	} else if sent > 0xFFFF {
		sent = 0xFFFF
	}
	if slot < 0 {
		slot = 0
	} else if slot > 0xFFFF {
		slot = 0xFFFF
	}
	return (uint32(sent) << 16) | uint32(slot)
}

func closestPackedSlot(left, right []uint32) int {
	nL, nR := len(left), len(right)
	if nL == 0 || nR == 0 {
		return -1
	}
	if (left[nL-1]>>16) < (right[0]>>16) || (right[nR-1]>>16) < (left[0]>>16) {
		return -1
	}
	closest := -1
	for x, y := 0, 0; x < nL && y < nR; {
		lSent, rSent := left[x]>>16, right[y]>>16
		switch {
		case lSent < rSent:
			x++
		case lSent > rSent:
			y++
		default:
			lSlot, rSlot := int(left[x]&0xFFFF), int(right[y]&0xFFFF)
			diff := lSlot - rSlot
			if diff < 0 {
				diff = -diff
			}
			if closest < 0 || diff < closest {
				closest = diff
				if closest == 0 {
					return 0
				}
			}
			if lSlot < rSlot {
				x++
			} else {
				y++
			}
		}
	}
	return closest
}

// QueryExtractor abstracts query-side term and position extraction for Engine.Query.
type QueryExtractor interface {
	ExtractQueryWithPositions(idx *Index, text string) (map[uint64]Term, map[uint64]int, map[uint64][]TermPosition)
}

// Query extracts query terms with positions and executes the 4-step cluster pipeline.
func (e *Engine) Query(az QueryExtractor, text string, topK int) []Hit {
	qTerms, _, qPositions := az.ExtractQueryWithPositions(e.Index, text)
	return e.SearchWithPositions(qTerms, qPositions, topK)
}

// Rank calls Engine.Search and returns the ranked UnitID slice.
func (e *Engine) Rank(qTerms map[uint64]Term, topK int) []string {
	hits := e.Search(qTerms, topK)
	out := make([]string, 0, len(hits))
	for _, hit := range hits {
		out = append(out, hit.UnitID)
	}
	return out
}

func (e *Engine) Spread(seeds []int32, seedEnergy map[int32]float64, qTerms map[uint64]Term, transmission float64, budget int, timeTau float64) map[int32]float64 {
	return e.spread(seeds, seedEnergy, qTerms, transmission, budget, timeTau)
}

func (e *Engine) IsPrimaryWord(h uint64) bool {
	return e.ensureClusterState().isPrimaryOf(h)
}

func SortByEnergy(units []int32, energy map[int32]float64, idx *Index) {
	sortByEnergy(units, energy, idx)
}

func PackTermPosition(sent, slot int) uint32 {
	return packTermPosition(sent, slot)
}
