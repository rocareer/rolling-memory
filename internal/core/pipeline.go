// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package core

import (
	"math"
	"sort"
)

// eulerNumber is Euler's number as a named constant; ePower expressions that
// are divisors of the local slot kernel keep this name so the divisor value and
// its rounding stay exactly the eulerNumber double.
const eulerNumber = math.E

// transmissionPerHop is the per-hop echo transmission 1/e: one nat of surprisal
// per hop is the unit-commensurability axiom of the delivery envelope (paper
// Theorem 4), so the local slot kernel exp(-(d-1)/e) is its geometric series and
// Z_local = 1/(1-e^{-1/e}) is that series' sum. It is a universal constant, not a
// calibrated threshold; every other file refers to it by name.
const transmissionPerHop = 1.0 / math.E

// queryDistinctProbe is an optional diagnostic sink for the number of distinct
// units that receive direct query mass in one query. It stays nil in production
// (one nil check per query); a measurement harness installs it with
// SetQueryDistinctProbe before issuing queries and is responsible for its own
// synchronization when queries run concurrently.
var queryDistinctProbe func(int)

// SetQueryDistinctProbe installs a diagnostic sink for the direct candidate-pool
// size of each query. Pass nil to disable. Diagnostic surface only: sizing for
// the stage 2d query-time DocMass decision (pre-registered §7-4).
func SetQueryDistinctProbe(fn func(int)) {
	queryDistinctProbe = fn
}

// [calibrated] anchorConcentrationFloor — calendar-concentration floor at which a
// word family counts as a temporally localized event anchor. An empirical operating
// point, not a derivation: the gate separates families whose mentions concentrate on
// a few days from families whose day-occupancy is indistinguishable from chance, and
// the value sits on the plateau between those two regimes.
//
// A distribution-free replacement (null-model noise band) was measured and rejected:
// it moves the gate off that separation, so non-localized families survive as anchors
// and the aggregate ranking falls. Change the value only together with a paired
// multi-corpus measurement. The value coincides with e^-1; it must not be re-derived
// from e.
const anchorConcentrationFloor = 0.36787944117144233

// [calibrated] shellScatterFloor — concentration below which a word family counts as
// a cross-day scattered colloquial shell and is extinguished outside the anchor cone.
// An empirical operating point, not a derivation.
//
// Softening this boundary into a continuous fade was measured and rejected: a fade
// keeps low-concentration families partially alive outside the cone, which adds
// scattered noise to the delivered set without adding anchors, so the boundary stays
// binary. Change the value only together with a paired multi-corpus measurement. The
// value coincides with e^-2; it must not be re-derived from e.
const shellScatterFloor = 0.1353352832366127

// [calibrated] cloneDampingKernel — strength of the echo/clone redundancy damping
// applied to a candidate's raw score. A calibrated kernel, not a derivation.
//
// A dose sweep put both sides of the plateau below the chosen point: too little
// damping lets near-duplicate clones keep enough weight to occupy several slots, too
// much starts damping independent records that merely share vocabulary with an
// already-selected one. Sweep the dose again before changing it. The value coincides
// with e^-2; it must not be re-derived from e.
// The damping factor is math.Max(0, 1-k*penalty) at its call sites, so the factor
// stays inside [0,1] even if the calibrated kernel or the penalty range changes.
const cloneDampingKernel = 0.1353352832366127

// anchorGateMassNats 是锚点门的质量边界：ln((N+1)/(df+1)) <= 2 的词项
// 视为背景（与 HighDFFraction=e^-2 同一标定）。有更强锚点时，背景词不生成
// 候选，只在锚点候选集内做第二阶段复核；没有锚点时完全走兼容路径。
const anchorGateMassNats = 2.0

// 透射率与亲和地板三件套的包级唯一声明；此前评分、关系与筛选各函数内的
// 同名局部副本已删除，引用处直接解析到本声明。三种历史写法（局部
// 1.0/(E·E)、局部 transmissionPerHop*transmissionPerHop、运行时 1.0-transmissionPerHop 与 1.0-transmissionPerHop*transmissionPerHop）与本声明
// 在 float64 下逐位相同，收敛不改变任何数值。
const (
	// transmissionTwoHop 是二跳透射率 e^{-2}（逐跳几何衰减的第二跳）。
	transmissionTwoHop = transmissionPerHop * transmissionPerHop
	// subFloor 是同事件邻居扫描的亲和地板 1-1/e。
	subFloor = 1.0 - transmissionPerHop
	// identityFloor 是克隆/旁观者判定的同源上限 1-e^{-2}。
	identityFloor = 1.0 - transmissionTwoHop
)

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
// bit6 强定界包络 / bit7 数值-量纲对。
// 可移植核零仓内依赖，故为字面量。
const rmFlagDocGainMask uint8 = 1<<2 | 1<<5 | 1<<6 | 1<<7

// rmFlagCompanion 是伴随修饰词槽的声明位掩码（bit4 FlagIsHarmonicCompanion）。
const rmFlagCompanion uint8 = 1 << 4

// Engine 是四步流水线的运行体：索引只读，扩散预算与能量口径可配置。
// SaturationMode 是筛选 rem 通道覆盖衰减权重的形态枚举。零值 SaturationSelf
// 为现役默认（与无量纲基线逐位等价）；非零值均为预注册判别器材，读数未立项前
// 不得默认开启。
type SaturationMode uint8

const (
	// SaturationSelf 用单元自身匹配质量 m_g(u) 作衰减权重（默认）。
	SaturationSelf SaturationMode = iota
	// SaturationPool 用查询面组质量 q_g 作衰减权重：单调次模集合目标的合法
	// 边际。作为对照臂保留：同一组内后续项共享组质量，第二项起的新增边际被
	// 前项吸收，整体没有正向贡献，因此默认关闭、只作证伪仪器。
	SaturationPool
	// SaturationGeometric 用几何中项 sqrt(m_g(u)·q_g) 作衰减权重：同组内 m_g(u)
	// 因单元而异，该形式不满足任何集合函数的边际定义（缺少可加分解所需的
	// 单调次模结构），仅作经验探针，默认关闭。
	SaturationGeometric
)

// Engine 是四步双簇检索的查询入口；零值字段组合对应生产默认路径。
type Engine struct {
	Index      *Index
	Hop1Budget int
	Hop2Budget int
	Energy     EnergyMode
	// Contraction 开关包含格二次倒排收缩通道。
	Contraction bool
	// FlagsGain 开启文档侧声明位增益：
	// 命中原子在该单元的折叠声明位命中 rmFlagDocGainMask 时，该命中质量乘
	// 1 + gain/e；0 关闭。默认 0.5。
	FlagsGain float64
	// BitsMass 开启查询侧载体宽度调制（sqrt 形式）：
	// 每个命中查询原子的质量按 bitsGainSqrt(声明宽度/语料均宽) 缩放；无声明
	// 宽度时回退语料折叠宽度，仍无宽度则质量原值。
	BitsMass bool
	// AnchorGate 开启查询锚点门：当查询中存在更强锚点时，背景词
	// （质量 <= anchorGateMassNats，约 df >= N/e²）不再生成候选，但仍参与
	// 组质量与锚点候选的第二阶段复核；没有锚点的查询完全走兼容路径。
	// 只作用于簇流水线；非簇回退路径保持不变。默认关闭。
	AnchorGate bool
	// Saturation 选择筛选 rem 通道的覆盖衰减权重形态（枚举见 SaturationMode）。
	// 只作用于簇流水线筛选的 rem 通道；novel 与静态通道不变。
	Saturation SaturationMode
	// PhraseOccupancySquash 开启 phrase 通道占用概率压缩核：dyad 匹配质量的
	// 饱和因子从 joint/(1+joint) 换成 1−exp(−joint)（泊松占据 P(k≥1|λ)）。
	// 已按预注册判负回滚（2026-11-23-phrase-occupancy-squash-prereg §6：Main 配对
	// Δ=−0.8144pp < −0.5pp，翻转 +1/−11；Hard 0 翻转）——现役失真压缩在承重，
	// 构成错误形态承重第五条证据；开关仅作判别器材保留，严禁默认开启。
	PhraseOccupancySquash bool
	// TimeKernel 开启扩散透射的时空核。
	TimeKernel bool
	// ClusterPipeline 默认开启四步簇与短语流水线（EntityCluster × SpacetimeCluster × PhraseDyad）。
	ClusterPipeline bool
	// ForceFullClusterRebuild 强制下一次重建丢弃增量基座、从新索引整建簇状态。
	// 代发布克隆用它避免把共享的旧簇状态（含族聚合）就地扣减。
	ForceFullClusterRebuild bool

	EnableStage3Plugin bool
	Stage3Plugin       Stage3Plugin

	clusterState *rmClusterState
	// prevClusterState 承载一次增量重建前的簇状态：ensureClusterState 校验通过后
	// 走增量扩展（复用族统计量与短语倒排、按 prevDrops 扣减），否则回退全量重建。
	prevClusterState *rmClusterState
	// prevDrops 是本次重建中被移除或覆盖的旧单元 ID（删除 + 同 ID 覆盖），
	// 增量路径需要据此从族统计量中扣减旧贡献。
	prevDrops map[string]struct{}
	// pendingBase/pendingDrops 是尚未物化的增量基座与待扣减集：写路径只更新索引并
	// 累积扣减，簇状态物化推迟到下一次读或后台发布时一次结算（读时物化仍按 Δ 扩展）。
	pendingBase  *rmClusterState
	pendingDrops map[string]struct{}
	queryScratch *rmQueryScratch
	mmapUnmap    func() error
	// retainBuildBase keeps a published mmap segment mapped across an in-place
	// write, so the pending incremental basis can extend it instead of being
	// discarded. Only a builder that reopens a generation sets it.
	retainBuildBase bool

	// 零拷贝增量叠加层（Delta Overlay）：在保持底层只读索引/.zcsr 映射区不卸载、不触发
	// 全量反解与全量重建的前提下，承载小批量新增/更新文档（deltaEngine）与已删除/已覆写
	// 单元遮蔽集（deltaMasked）。
	deltaEngine *Engine
	deltaMasked map[string]struct{}
	deltaDocs   map[string]Doc
	deltaOrder  []string
}

// RetainBuildBase makes in-place writes keep the published mmap base mapped
// until the next cluster materialisation, so a reopened generation can be
// extended incrementally. A normal reader/writer path leaves it false: closing
// the segment is what frees a generation it no longer owns.
func (e *Engine) RetainBuildBase(keep bool) {
	if e == nil {
		return
	}
	e.retainBuildBase = keep
}

// stageRebuild stages the pre-rebuild cluster state for incremental extension.
// A published mmap base must be staged before the segment is unmapped; when the
// caller did not ask to retain it, the segment is closed first and no basis is
// kept, because the staged slices would outlive their mapping.
func (e *Engine) stageRebuild(drops map[string]struct{}, n int) {
	if e == nil {
		return
	}
	if e.mmapUnmap != nil && !e.retainBuildBase {
		_ = e.Close()
		e.stageIncrementalBasis(drops, n)
		return
	}
	e.stageIncrementalBasis(drops, n)
}

// Close 释放引擎挂载的任何底层 mmap 磁盘段资源，并在卸载映射前将 Index 上仍指向映射区的
// 元数据与词表视图安全克隆到 Go 堆、停用映射倒排迭代闭包，确保 munmap 之后任何读取都不会
// 触碰已解除的地址空间。
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
		// TermKeys / rawTermDF/Mass/Bits/Flags are zero-copy views into the mapped
		// segment; clone them so post-Close reads stay on the Go heap.
		e.Index.TermKeys = append([]uint64(nil), e.Index.TermKeys...)
		e.Index.rawTermDF = append([]int32(nil), e.Index.rawTermDF...)
		e.Index.rawTermMass = append([]float64(nil), e.Index.rawTermMass...)
		e.Index.rawTermBits = append([]uint16(nil), e.Index.rawTermBits...)
		e.Index.rawTermFlags = append([]uint8(nil), e.Index.rawTermFlags...)
		e.Index.csrFallbackExport = nil
		// The posting iterator closes over the mmap-backed posting pool; disable it so
		// post-Close iteration degrades to empty instead of reading unmapped memory.
		e.Index.csrPostingUnits = nil
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

// ResetScratch 清空当前 Engine 实例绑定的查询暂存区，使浅拷贝后的并发协程各自懒分配独立 Scratch。
func (e *Engine) ResetScratch() {
	if e == nil {
		return
	}
	e.queryScratch = nil
}

// CloneForQuery 返回查询专用的浅拷贝：共享只读 Index / 簇状态，独占 queryScratch，
// 且不持有 mmap 卸载句柄（查询不得关闭底层段）。调用方不得在其上执行写操作。
func (e *Engine) CloneForQuery() *Engine {
	if e == nil {
		return nil
	}
	c := *e
	c.queryScratch = nil
	c.mmapUnmap = nil
	c.retainBuildBase = false
	return &c
}

// HasMmapSegment 报告引擎是否挂载着需要显式关闭的 mmap 段。mmap 基座不做后台换代：
// 旧段仍服务在途读者，增量由同步排空处理。
func (e *Engine) HasMmapSegment() bool {
	return e != nil && e.mmapUnmap != nil
}

// CloneForPublish 返回代发布专用的浅拷贝：共享只读 Index / 簇状态作为基座，
// 独占增量字段并强制整建簇状态，避免就地扣减共享的旧族聚合。
// mmap 承载的引擎返回 nil（旧段仍需服务在途读者，交由同步路径处理）。
func (e *Engine) CloneForPublish() *Engine {
	if e == nil || e.mmapUnmap != nil {
		return nil
	}
	c := *e
	c.queryScratch = nil
	c.prevClusterState = nil
	c.prevDrops = nil
	c.deltaEngine = nil
	c.deltaMasked = nil
	c.deltaDocs = nil
	c.deltaOrder = nil
	c.Stage3Plugin = nil
	c.EnableStage3Plugin = false
	c.ForceFullClusterRebuild = false
	c.clusterState = e.clusterState.CloneForPublish()
	c.pendingBase = nil
	c.pendingDrops = nil
	c.retainBuildBase = false
	// 写路径惰性化后，已物化状态可能缺位：把挂起的基座深拷贝给发布克隆，
	// 使发布仍走增量扩展而不是从零整建。
	if c.clusterState == nil && e.pendingBase != nil {
		c.pendingBase = e.pendingBase.CloneForPublish()
		if len(e.pendingDrops) > 0 {
			c.pendingDrops = make(map[string]struct{}, len(e.pendingDrops))
			for id := range e.pendingDrops {
				c.pendingDrops[id] = struct{}{}
			}
		}
	}
	c.mmapUnmap = nil
	return &c
}

// MaterializeClusterState eagerly builds the flat CSR cluster state and binds
// the CSR fallback export/posting iterators on the Index. Callers that intend
// to release transient unit maps right after construction must invoke this
// first: ReleaseTransientUnitMaps stays a no-op until those CSR readers exist,
// and a lazily built state would otherwise leave the maps resident.
func (e *Engine) MaterializeClusterState() {
	if e == nil || e.Index == nil || e.Index.N == 0 {
		return
	}
	_ = e.ensureClusterState()
}

// DocEntryView 表示 CSR 文档正向表中的单条紧凑词项记录（零堆分配只读视图）。
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
		role, extFlags := UnpackTermRoleExt(entry.roleExt)
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

// ForEachUnitDocEntry 按单元 ID 遍历其当前权威词项条目（优先读取增量叠加层 deltaEngine，
// 自动跳过已被 deltaMasked 遮蔽/删除的旧基线单元）。若找到活跃单元则返回 true。
func (e *Engine) ForEachUnitDocEntry(unitID string, fn func(DocEntryView)) bool {
	if e == nil || unitID == "" {
		return false
	}
	if e.deltaEngine != nil && e.deltaEngine.Index != nil {
		if u, ok := e.deltaEngine.Index.LookupUnit(unitID); ok {
			if fn != nil {
				e.deltaEngine.ForEachDocEntry(int(u), fn)
			}
			return true
		}
	}
	if len(e.deltaMasked) > 0 {
		if _, masked := e.deltaMasked[unitID]; masked {
			return false
		}
	}
	if e.Index == nil {
		return false
	}
	u, ok := e.Index.LookupUnit(unitID)
	if !ok {
		return false
	}
	if fn != nil {
		e.ForEachDocEntry(int(u), fn)
	}
	return true
}

// HasDelta 报告当前 Engine 是否挂载了非空的增量叠加层或删除遮蔽集。
func (e *Engine) HasDelta() bool {
	if e == nil {
		return false
	}
	return (e.deltaEngine != nil && e.deltaEngine.Index != nil && e.deltaEngine.Index.N > 0) || len(e.deltaMasked) > 0
}

// DeltaDocCount 返回当前增量叠加层中承载的新增/更新单元数量。
func (e *Engine) DeltaDocCount() int {
	if e == nil || e.deltaEngine == nil || e.deltaEngine.Index == nil {
		return 0
	}
	return e.deltaEngine.Index.N
}

// ClearDelta 清空当前 Engine 上的增量叠加层与遮蔽集，恢复纯基线视图。
func (e *Engine) ClearDelta() {
	if e == nil {
		return
	}
	e.deltaEngine = nil
	e.deltaMasked = nil
	e.deltaDocs = nil
	e.deltaOrder = nil
}

// newOverlayChildEngine 用与父 Engine 相同的算法配置构造增量微簇 Engine。
func (e *Engine) newOverlayChildEngine(docs []Doc) *Engine {
	if len(docs) == 0 {
		return nil
	}
	child := NewEngine(BuildIndex(docs))
	if e != nil {
		child.Hop1Budget = e.Hop1Budget
		child.Hop2Budget = e.Hop2Budget
		child.Energy = e.Energy
		child.Contraction = e.Contraction
		child.FlagsGain = e.FlagsGain
		child.BitsMass = e.BitsMass
		child.TimeKernel = e.TimeKernel
		child.ClusterPipeline = e.ClusterPipeline
	}
	if child.ClusterPipeline {
		child.MaterializeClusterState()
	}
	return child
}

// SetDeltaSnapshot 将相对于基线引擎的完整增量集（新增/修改文档 deltaDocs 与已删除/已修改
// 单元 ID 列表 maskedUnitIDs）原位挂载到当前 Engine，不触碰底层基线索引与 .zcsr 映射区。
func (e *Engine) SetDeltaSnapshot(deltaDocs []Doc, maskedUnitIDs []string) {
	if e == nil {
		return
	}
	if len(deltaDocs) == 0 && len(maskedUnitIDs) == 0 {
		e.ClearDelta()
		return
	}
	masked := make(map[string]struct{}, len(deltaDocs)+len(maskedUnitIDs))
	for _, id := range maskedUnitIDs {
		if id != "" {
			masked[id] = struct{}{}
		}
	}
	docMap := make(map[string]Doc, len(deltaDocs))
	order := make([]string, 0, len(deltaDocs))
	dedupDocs := make([]Doc, 0, len(deltaDocs))
	for _, d := range deltaDocs {
		if d.UnitID == "" {
			continue
		}
		masked[d.UnitID] = struct{}{}
		if _, exists := docMap[d.UnitID]; !exists {
			order = append(order, d.UnitID)
		}
		docMap[d.UnitID] = d
	}
	for _, id := range order {
		dedupDocs = append(dedupDocs, docMap[id])
	}
	e.deltaMasked = masked
	e.deltaDocs = docMap
	e.deltaOrder = order
	e.deltaEngine = e.newOverlayChildEngine(dedupDocs)
}

// OverlayDelta 在不修改当前基线 Engine 的前提下，返回一个共享底层基线索引与 .zcsr 零拷贝
// 映射区、并叠加了指定增量文档与遮蔽集的派生 Engine 视图（Copy-on-Write 零拷贝派生）。
func (e *Engine) OverlayDelta(deltaDocs []Doc, maskedUnitIDs []string) *Engine {
	if e == nil {
		return nil
	}
	derived := &Engine{
		Index:              e.Index,
		Hop1Budget:         e.Hop1Budget,
		Hop2Budget:         e.Hop2Budget,
		Energy:             e.Energy,
		Contraction:        e.Contraction,
		FlagsGain:          e.FlagsGain,
		BitsMass:           e.BitsMass,
		AnchorGate:         e.AnchorGate,
		Saturation:         e.Saturation,
		TimeKernel:         e.TimeKernel,
		ClusterPipeline:    e.ClusterPipeline,
		EnableStage3Plugin: e.EnableStage3Plugin,
		Stage3Plugin:       e.Stage3Plugin,
		clusterState:       e.clusterState,
	}
	derived.SetDeltaSnapshot(deltaDocs, maskedUnitIDs)
	return derived
}

// ExportDocs 从当前 Engine（含内存态 Index、.zcsr 磁盘 mmap 零拷贝态及增量叠加层）无损还原全部活跃文档快照。
func (e *Engine) ExportDocs() []Doc {
	if e == nil {
		return nil
	}
	baseDocs := e.exportBaseDocs()
	if !e.HasDelta() {
		return baseDocs
	}
	out := make([]Doc, 0, len(baseDocs)+len(e.deltaOrder))
	for _, d := range baseDocs {
		if _, masked := e.deltaMasked[d.UnitID]; masked {
			continue
		}
		out = append(out, d)
	}
	for _, id := range e.deltaOrder {
		if d, ok := e.deltaDocs[id]; ok {
			out = append(out, d)
		}
	}
	return out
}

func (e *Engine) exportBaseDocs() []Doc {
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
			role, extFlags := UnpackTermRoleExt(entry.roleExt)
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

// stageIncrementalBasis 累积本次重建的增量基座与待扣减 ID 集（删除 + 同 ID 覆盖）。
// 连续写入共享同一基座：基座取当前已物化状态，缺位时沿用上一次挂起的基座。
// 旧状态缺统计量、存在 overlay 或空库时清空，由 ensureClusterState 走全量重建。
func (e *Engine) stageIncrementalBasis(drops map[string]struct{}, n int) {
	if e.ForceFullClusterRebuild || !e.ClusterPipeline || e.HasDelta() || n <= 0 {
		e.pendingBase = nil
		e.pendingDrops = nil
		return
	}
	base := e.clusterState
	if base == nil {
		base = e.pendingBase
	}
	if base == nil || base.agg == nil {
		e.pendingBase = nil
		e.pendingDrops = nil
		return
	}
	e.pendingBase = base
	if len(drops) > 0 {
		if e.pendingDrops == nil {
			e.pendingDrops = make(map[string]struct{}, len(drops))
		}
		for id := range drops {
			e.pendingDrops[id] = struct{}{}
		}
	}
}

// baseClusterState 返回可用于增量基座或索引重建的最近簇状态（已物化优先，其次挂起基座）。
func (e *Engine) baseClusterState() *rmClusterState {
	if e == nil {
		return nil
	}
	if e.clusterState != nil {
		return e.clusterState
	}
	return e.pendingBase
}

// AppendDocs 将新增或更新的文档批次流式并入当前 Engine（支持内存态与 .zcsr mmap 零拷贝态热升迁），
// 自动同步底层 Index 与双簇 CSR 状态，保证分批流式写入与一次性全量构建 100% 0-ULP 逐位全等。
func (e *Engine) AppendDocs(docs []Doc) {
	if e == nil || len(docs) == 0 {
		return
	}
	// CSR snapshot rebuild: feed the live cluster state plus delta directly,
	// skipping the O(N) ExportDocs Doc materialization. Falls back to the
	// ExportDocs path when no cluster state is available.
	rebuilt := e.buildIndexWithDelta(docs)
	if rebuilt == nil {
		existing := e.ExportDocs()
		combined := make([]Doc, 0, len(existing)+len(docs))
		combined = append(combined, existing...)
		combined = append(combined, docs...)
		rebuilt = BuildIndex(combined)
	}
	// 增量基座：同 ID 覆盖记入 prevDrops（扣减旧贡献）。必须在释放/保留基座
	// 之前判定，才能在保留 mmap 基座时看到旧族聚合。
	var replaced map[string]struct{}
	if e.ClusterPipeline && e.clusterState != nil && e.clusterState.agg != nil && !e.HasDelta() && e.Index != nil {
		for _, doc := range docs {
			if doc.UnitID == "" {
				continue
			}
			if _, exists := e.Index.LookupUnit(doc.UnitID); exists {
				if replaced == nil {
					replaced = make(map[string]struct{}, len(docs))
				}
				replaced[doc.UnitID] = struct{}{}
			}
		}
	}
	e.stageRebuild(replaced, rebuilt.N)
	e.Index = rebuilt
	e.clusterState = nil
	e.queryScratch = nil
	e.ClearDelta()
	e.syncStage3Plugin()
}

// DeleteDocs 按 unitIDs 从当前 Engine（支持内存态与 .zcsr mmap 零拷贝态）精确移除指定记忆单元，
// 自动同步底层 Index 与双簇 CSR 状态，保证删除后与从未入库过被删单元的全量构建 100% 0-ULP 逐位全等。
func (e *Engine) DeleteDocs(unitIDs ...string) int {
	if e == nil || ((e.Index == nil || e.Index.N == 0) && !e.HasDelta()) || len(unitIDs) == 0 {
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
	// No-op deletes must not pay the O(N) export/rebuild: probe the base index
	// and the delta overlay directly and return early when no requested ID is
	// resident. A resident ID keeps the full path (masked base units included,
	// because they still live in the base index).
	present := false
	for id := range delSet {
		if e.Index != nil {
			if _, ok := e.Index.LookupUnit(id); ok {
				present = true
				break
			}
		}
		if e.deltaEngine != nil && e.deltaEngine.Index != nil {
			if _, ok := e.deltaEngine.Index.LookupUnit(id); ok {
				present = true
				break
			}
		}
	}
	if !present {
		return 0
	}
	drops := make(map[string]struct{}, len(delSet))
	if e.Index != nil {
		for id := range delSet {
			if _, ok := e.Index.LookupUnit(id); ok {
				drops[id] = struct{}{}
			}
		}
	}
	removed := 0
	var rebuilt *Index
	if !e.HasDelta() {
		// CSR snapshot rebuild: exclude the dropped IDs from the live snapshot
		// instead of exporting every surviving document as a Doc. Delta overlay
		// engines keep the ExportDocs path so masked/overlay semantics stay
		// exactly as before.
		for id := range delSet {
			if e.Index != nil {
				if _, ok := e.Index.LookupUnit(id); ok {
					removed++
				}
			}
		}
		if removed == 0 {
			return 0
		}
		rebuilt = e.buildIndexWithDeltaDrop(nil, delSet)
	}
	if rebuilt == nil {
		existing := e.ExportDocs()
		kept := make([]Doc, 0, len(existing))
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
		rebuilt = BuildIndex(kept)
	}
	e.stageRebuild(drops, rebuilt.N)
	e.Index = rebuilt
	e.clusterState = nil
	e.queryScratch = nil
	e.ClearDelta()
	e.syncStage3Plugin()
	return removed
}

// ApplyDocs 在一次重建中应用一批删除与新增/更新：先剔除 unitIDs，再并入 upserts。// 语义与顺序调用 DeleteDocs(unitIDs...) 再 AppendDocs(upserts) 完全一致，但索引、
// 双簇状态与 Phase-3 外挂各只结算一次，混合批（生产 drain 的删除+追加重合场景）
// 不再付两遍 O(N) 重建。同一 ID 同时出现在两侧时按顺序语义保留 upsert。
func (e *Engine) ApplyDocs(unitIDs []string, upserts []Doc) int {
	if e == nil {
		return 0
	}
	delSet := make(map[string]struct{}, len(unitIDs))
	for _, id := range unitIDs {
		if id != "" {
			delSet[id] = struct{}{}
		}
	}
	if len(delSet) == 0 {
		e.AppendDocs(upserts)
		return 0
	}
	if len(upserts) == 0 || e.HasDelta() || e.Index == nil || e.Index.N == 0 {
		removed := e.DeleteDocs(unitIDs...)
		e.AppendDocs(upserts)
		return removed
	}
	removed := 0
	drops := make(map[string]struct{}, len(delSet)+len(upserts))
	for id := range delSet {
		if _, ok := e.Index.LookupUnit(id); ok {
			removed++
			drops[id] = struct{}{}
		}
	}
	for _, doc := range upserts {
		if doc.UnitID == "" {
			continue
		}
		if _, ok := e.Index.LookupUnit(doc.UnitID); ok {
			drops[doc.UnitID] = struct{}{}
		}
	}
	if removed == 0 {
		e.AppendDocs(upserts)
		return 0
	}
	// 顺序语义下「删除后紧接同 ID 追加」以追加为准：该 ID 若仍留在 drop 集，
	// buildIndexWithDeltaDrop 会把 delta 一并排除，因此按需复制并剔除重叠项。
	drop := delSet
	copied := false
	for _, doc := range upserts {
		if _, overlap := delSet[doc.UnitID]; !overlap {
			continue
		}
		if !copied {
			clone := make(map[string]struct{}, len(delSet))
			for id := range delSet {
				clone[id] = struct{}{}
			}
			drop = clone
			copied = true
		}
		delete(drop, doc.UnitID)
	}
	rebuilt := e.buildIndexWithDeltaDrop(upserts, drop)
	if rebuilt == nil {
		existing := e.ExportDocs()
		kept := make([]Doc, 0, len(existing))
		for _, d := range existing {
			if _, gone := delSet[d.UnitID]; gone {
				continue
			}
			kept = append(kept, d)
		}
		kept = append(kept, upserts...)
		rebuilt = BuildIndex(kept)
	}
	e.stageRebuild(drops, rebuilt.N)
	e.Index = rebuilt
	e.clusterState = nil
	e.queryScratch = nil
	e.ClearDelta()
	e.syncStage3Plugin()
	return removed
}

// bitsGainSqrt 是无量纲载体宽度连续增益 sqrt(1+ratio)：
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
//
// Direct 是直接通道（查询词项 + 短语 + 实体 + 结构 + 残差）。Hop1/Hop2 在默认
// cluster pipeline 中分别是簇内 stSpread/stCorroboration，不是词项桥 hop；只有
// fallback 路径才是 term-bridge 的一跳/二跳。归因报告必须按路径读这两个字段。
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
	if e == nil || len(qTerms) == 0 {
		return nil
	}
	if !e.HasDelta() {
		return e.searchBaseWithPositions(qTerms, qPositions, topK)
	}
	baseTopK := topK
	if topK > 0 && len(e.deltaMasked) > 0 {
		baseTopK = topK + len(e.deltaMasked)
	}
	baseHits := e.searchBaseWithPositions(qTerms, qPositions, baseTopK)
	var deltaHits []Hit
	if e.deltaEngine != nil {
		deltaHits = e.deltaEngine.searchBaseWithPositions(qTerms, qPositions, topK)
	}
	return mergeOverlayHits(baseHits, deltaHits, e.deltaMasked, topK)
}

func (e *Engine) searchBaseWithPositions(qTerms map[uint64]Term, qPositions map[uint64][]TermPosition, topK int) []Hit {
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
	sortUint64Asc(queryHashes)
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
						weight = mass * (1 + e.FlagsGain/eulerNumber)
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
						weight = mass * (1 + e.FlagsGain/eulerNumber)
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
	if queryDistinctProbe != nil {
		queryDistinctProbe(len(direct))
	}
	// Residual proximity: while no candidate fully covers the query mass, each
	// candidate's matched pairs add the harmonic pair mass 2*ma*mb/(ma+mb)
	// weighted by the same-sentence closest-slot kernel exp(-(d-1)/e), scaled by
	// the pool residual max(0, 1 - best coverage). Positions are in-memory
	// derived data; units without them stay at their direct energy.
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
	// Containment-lattice contraction: a candidate matching at least two query
	// atoms whose joint intersection shrinks below the rarest single frequency
	// gains ln((minDF+1)/(intersection+1)) weighted by mass/total, with minDF
	// and intersection measured over the candidate pool.
	if e.Contraction && totalQueryMass > 0 {
		e.contractionIncrements(direct, queryHashes, masses, totalQueryMass)
	}
	sortByEnergy(directUnits, direct, idx)

	// Spacetime x entity cross-cluster coupling: with TimeKernel enabled, every
	// hop transmission is weighted by the continuous time kernel exp(-|dt|/tau)
	// between the seed's and the neighbor's reference clocks (OccurredAt ->
	// NarratedAt -> IngestedAt),
	// where tau is the endogenous mean positive consecutive gap. Units without
	// clocks keep the unweighted transmission, so absent metadata is neutral.
	var timeTau float64
	if e.TimeKernel {
		timeTau = idx.meanUnitGap()
		if timeTau <= 0 && e.clusterState != nil {
			timeTau = e.clusterState.meanGap
		}
	}
	hop1 := e.spread(directUnits, direct, qTerms, transmissionPerHop, e.Hop1Budget, timeTau)
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

	hop2 := e.spread(hop1Only, hop1, qTerms, transmissionPerHop*transmissionPerHop, e.Hop2Budget, timeTau)

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

	e.applyStage3FallbackHook(idx, qTerms, masses, totalQueryMass, direct, hop1, hop2, pool)

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

// contractionIncrements evaluates the containment lattice: for every candidate
// matching at least two query atoms, the joint intersection of its matched set
// is counted over the candidate pool, and the candidate gains
// ln((minDF+1)/(intersection+1)) * mass / total where minDF is the rarest
// matched atom's candidate frequency and mass the matched query mass. Searches
// iterate the rarest atom's postings; candidates are keyed by the shared
// coverage map, so the accumulation is order-independent and bit-for-bit
// reproducible.
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
	// set is evaluated once and shared by all its carriers; this keeps the work
	// bounded by the number of distinct matched sets instead of the candidate count.
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

// proximityIncrement computes intra-sentence pair proximity energy: over the
// unit's matched query identities, every pair (a, b) contributes the harmonic
// mean 2*ma*mb/(ma+mb) weighted by exp(-(d-1)/e) of the closest same-sentence
// occurrence distance d. Pairs without positions contribute zero. The matched
// scan follows the unit's fixed DocTerms order, so the accumulation is
// bit-for-bit reproducible.
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
				energy += 2 * leftMass * rightMass / (leftMass + rightMass) * math.Exp(-(float64(closest)-1)/eulerNumber)
			}
		}
		return energy
	}
	st := e.clusterState
	if st == nil {
		return 0
	}
	type matchedEntryPos struct {
		id       uint64
		idxMass  float64
		pos      []uint32
		posExact []TermPosition
	}
	wide := st.hasWidePos()
	entries := st.docEntriesOf(unit)
	matched := make([]matchedEntryPos, 0, len(entries))
	for _, ent := range entries {
		if _, ok := masses[ent.id]; !ok {
			continue
		}
		m, _ := idx.TermMass(ent.id)
		item := matchedEntryPos{
			id:      ent.id,
			idxMass: m,
			pos:     st.entryPositionsOf(ent),
		}
		if wide {
			item.posExact = st.entryPositionsExactOf(ent)
		}
		matched = append(matched, item)
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
			var closest int
			if wide {
				closest = closestExactSlot(left.posExact, right.posExact)
			} else {
				closest = closestPackedSlot(left.pos, right.pos)
			}
			if closest < 0 {
				continue
			}
			energy += 2 * leftMass * rightMass / (leftMass + rightMass) * math.Exp(-(float64(closest)-1)/eulerNumber)
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
		var sum float64
		for _, h := range docTermsOf(u) {
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

// StateGeneration 返回索引的单调变更世代：工厂构建置初值，AppendDocs/DeleteDocs
// 各前进一步，供派生状态判定宿主索引是否已变更并自愈重建。
func (idx *Index) StateGeneration() uint64 {
	if idx == nil {
		return 0
	}
	return idx.stateGen
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
	// Single-position entries are the common case for terms that occur once;
	// compare directly instead of running the two-pointer bookkeeping.
	if nL == 1 {
		sent := left[0] >> 16
		lSlot := int(left[0] & 0xFFFF)
		y := 0
		for y < nR && right[y]>>16 < sent {
			y++
		}
		best := -1
		for ; y < nR && right[y]>>16 == sent; y++ {
			diff := lSlot - int(right[y]&0xFFFF)
			if diff < 0 {
				diff = -diff
			}
			if best < 0 || diff < best {
				best = diff
				if best == 0 {
					return 0
				}
			}
		}
		return best
	}
	if nR == 1 {
		sent := right[0] >> 16
		rSlot := int(right[0] & 0xFFFF)
		x := 0
		for x < nL && left[x]>>16 < sent {
			x++
		}
		best := -1
		for ; x < nL && left[x]>>16 == sent; x++ {
			diff := rSlot - int(left[x]&0xFFFF)
			if diff < 0 {
				diff = -diff
			}
			if best < 0 || diff < best {
				best = diff
				if best == 0 {
					return 0
				}
			}
		}
		return best
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

// reusableQueryExtractor is an optional zero-allocation fast path implemented
// by TermExtractor to reuse the Engine's per-query map and position slice arena.
type reusableQueryExtractor interface {
	ExtractQueryInto(idx QueryIndexView, text string, qTerms map[uint64]Term, qPositions map[uint64][]TermPosition, posPool []TermPosition) []TermPosition
}

// Query extracts query terms with positions and executes the 4-step cluster pipeline.
func (e *Engine) Query(az QueryExtractor, text string, topK int) []Hit {
	if e == nil || az == nil {
		return nil
	}
	if !e.HasDelta() {
		return e.queryBase(az, text, topK)
	}
	baseTopK := topK
	if topK > 0 && len(e.deltaMasked) > 0 {
		baseTopK = topK + len(e.deltaMasked)
	}
	baseHits := e.queryBase(az, text, baseTopK)
	var deltaHits []Hit
	if e.deltaEngine != nil {
		deltaHits = e.deltaEngine.queryBase(az, text, topK)
	}
	return mergeOverlayHits(baseHits, deltaHits, e.deltaMasked, topK)
}

func (e *Engine) queryBase(az QueryExtractor, text string, topK int) []Hit {
	if e == nil || e.Index == nil || e.Index.N == 0 {
		return nil
	}
	if rq, ok := az.(reusableQueryExtractor); ok {
		st := e.ensureClusterState()
		clusterN := 0
		if st != nil {
			clusterN = st.clusterN
		}
		sc := e.ensureQueryScratch(e.Index.N, clusterN)
		if sc.qTerms == nil {
			sc.qTerms = make(map[uint64]Term, 64)
		} else {
			clear(sc.qTerms)
		}
		if sc.posMap == nil {
			sc.posMap = make(map[uint64][]TermPosition, 64)
		} else {
			clear(sc.posMap)
		}
		if cap(sc.posPool) < 128 {
			sc.posPool = make([]TermPosition, 0, 128)
		} else {
			sc.posPool = sc.posPool[:0]
		}
		sc.posPool = rq.ExtractQueryInto(e.Index, text, sc.qTerms, sc.posMap, sc.posPool)
		return e.searchBaseWithPositions(sc.qTerms, sc.posMap, topK)
	}
	qTerms, _, qPositions := az.ExtractQueryWithPositions(e.Index, text)
	return e.searchBaseWithPositions(qTerms, qPositions, topK)
}

// mergeOverlayHits 将基线命中与增量微簇命中按物理信息能量 Hit.Energy（Nats）降序统一归并
// （名次盲 Rank-Index Blind），自动剔除已被 deltaMasked 遮蔽的已删除或已覆写旧基线单元。
func mergeOverlayHits(baseHits, deltaHits []Hit, masked map[string]struct{}, topK int) []Hit {
	if len(deltaHits) == 0 {
		if len(masked) == 0 {
			if topK > 0 && len(baseHits) > topK {
				return baseHits[:topK]
			}
			return baseHits
		}
		kept := baseHits[:0]
		for _, h := range baseHits {
			if _, drop := masked[h.UnitID]; drop {
				continue
			}
			kept = append(kept, h)
			if topK > 0 && len(kept) >= topK {
				break
			}
		}
		return kept
	}
	byID := make(map[string]Hit, len(baseHits)+len(deltaHits))
	for _, h := range deltaHits {
		if h.UnitID == "" || h.Energy <= 0 || math.IsNaN(h.Energy) {
			continue
		}
		byID[h.UnitID] = h
	}
	for _, h := range baseHits {
		if h.UnitID == "" || h.Energy <= 0 || math.IsNaN(h.Energy) {
			continue
		}
		if len(masked) > 0 {
			if _, drop := masked[h.UnitID]; drop {
				continue
			}
		}
		if _, exists := byID[h.UnitID]; !exists {
			byID[h.UnitID] = h
		}
	}
	merged := make([]Hit, 0, len(byID))
	for _, h := range byID {
		merged = append(merged, h)
	}
	sort.Slice(merged, func(i, j int) bool {
		a, b := merged[i], merged[j]
		if a.Energy != b.Energy {
			return a.Energy > b.Energy
		}
		return a.UnitID < b.UnitID
	})
	if topK > 0 && len(merged) > topK {
		merged = merged[:topK]
	}
	return merged
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
