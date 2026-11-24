// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package core

import (
	"math"
	"sort"
)

// Stage3Candidate 承载四步流水线在完成前两簇（EntityCluster 与 SpacetimeCluster）计算后、
// 向可选 Phase-3 关系簇外挂暴露的只读候选快照。
type Stage3Candidate struct {
	Unit        int32   // Index.UnitIDs 中的单元下标
	DocID       string  // 单元唯一标识 Index.UnitIDs[Unit]
	Stage2Nats  float64 // 前两簇合算的基础信息能量（Nats）
	CoreLexNats float64 // 剥离高频游离碎片后的核心词项匹配能量（Nats）
	StructGate  float64 // 主锚条件化结构门因子（[1/e, 1.0]）
}

// Stage3Delta 表示可选 Phase-3 外挂对单个候选单元返回的非负信息能量增量（单位：Nats）。
// Hop1Nats 对应一跳近邻/伴随团共振增益，Hop2Nats 对应二跳三阶张量残差桥接增益。
// 任何负数或非有限值（NaN/Inf）在接入埋点处均被安全钳位为 0，确保外挂绝不削弱或破坏
// rm 核心原有的前两阶段能量分布。
type Stage3Delta struct {
	Unit     int32
	Hop1Nats float64
	Hop2Nats float64
}

// Stage3Plugin 定义 rm 四步双簇内核的可插拔 Phase-3 关系簇扩展接口。
type Stage3Plugin interface {
	AugmentRMStage2(idx *Index, qTerms map[uint64]Term, qGroups map[uint64]float64, totalQueryMass float64, stage2 []Stage3Candidate) []Stage3Delta
}

type stage3EngineSyncer interface {
	SyncRMEngine(eng *Engine)
}

type stage3CandidateSort []Stage3Candidate

func (s stage3CandidateSort) Len() int { return len(s) }
func (s stage3CandidateSort) Less(i, j int) bool {
	if s[i].Stage2Nats != s[j].Stage2Nats {
		return s[i].Stage2Nats > s[j].Stage2Nats
	}
	return s[i].Unit < s[j].Unit
}
func (s stage3CandidateSort) Swap(i, j int) { s[i], s[j] = s[j], s[i] }

func sortStage3Candidates(stage2 []Stage3Candidate) {
	if len(stage2) < 2 {
		return
	}
	sort.Sort(stage3CandidateSort(stage2))
}

// SetStage3Plugin 挂载或卸载可选的 Phase-3 关系簇外挂，并同步设置启用开关。
// 传入 plugin == nil 或 enabled == false 即可安全关闭外挂，恢复纯双簇内核运行。
func (e *Engine) SetStage3Plugin(plugin Stage3Plugin, enabled bool) {
	if e == nil {
		return
	}
	e.Stage3Plugin = plugin
	e.EnableStage3Plugin = enabled && plugin != nil
}

// EnableStage3 动态开启或关闭已挂载的 Phase-3 外挂。
func (e *Engine) EnableStage3(enabled bool) {
	if e == nil {
		return
	}
	e.EnableStage3Plugin = enabled && e.Stage3Plugin != nil
}

func (e *Engine) syncStage3Plugin() {
	if e != nil && e.Stage3Plugin != nil {
		if syncer, ok := e.Stage3Plugin.(stage3EngineSyncer); ok {
			syncer.SyncRMEngine(e)
		}
	}
}

func (e *Engine) applyStage3FallbackHook(idx *Index, qTerms map[uint64]Term, masses map[uint64]float64, totalQueryMass float64, direct, hop1, hop2 map[int32]float64, pool map[int32]struct{}) {
	if !e.EnableStage3Plugin || e.Stage3Plugin == nil || len(pool) == 0 {
		return
	}
	stage2 := make([]Stage3Candidate, 0, len(pool))
	for unit := range pool {
		e2 := direct[unit] + hop1[unit] + hop2[unit]
		if e2 > 0 {
			stage2 = append(stage2, Stage3Candidate{
				Unit:        unit,
				DocID:       idx.UnitIDs[unit],
				Stage2Nats:  e2,
				CoreLexNats: direct[unit],
				StructGate:  1.0,
			})
		}
	}
	sortStage3Candidates(stage2)
	for _, delta := range e.Stage3Plugin.AugmentRMStage2(idx, qTerms, masses, totalQueryMass, stage2) {
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

// applyStage3ClusterHook 在前两簇（EntityCluster + SpacetimeCluster）结算完毕后，
// 构造只读候选快照并调用已挂载的 Stage3Plugin 外挂，将返回的非负关系簇增益
// （Hop1Nats / Hop2Nats）注入候选能量场。
func (e *Engine) applyStage3ClusterHook(idx *Index, st *rmClusterState, sc *rmQueryScratch, candidates []rmCandidate, unitStructGate []float64, qTerms map[uint64]Term, groups map[uint64]float64, total float64) {
	stage2 := make([]Stage3Candidate, 0, len(sc.dirtyUnits))
	for _, u := range sc.dirtyUnits {
		c := &candidates[u]
		if c.lex+c.stSpread+c.stCorrob <= 0 {
			continue
		}
		gate := gateAt(unitStructGate, u)
		if !c.structureSet {
			c.structure = st.computeStructuredIncrement(idx, sc, c, groups, total, gate)
			c.structureSet = true
		}
		e2 := c.lex + c.phrase + c.entity + c.structure + c.stSpread + c.stCorrob
		if e2 <= 0 {
			continue
		}
		stage2 = append(stage2, Stage3Candidate{
			Unit:        u,
			DocID:       idx.UnitIDs[u],
			Stage2Nats:  e2,
			CoreLexNats: c.coreLex,
			StructGate:  gate,
		})
	}
	if len(stage2) == 0 {
		return
	}
	sortStage3Candidates(stage2)
	addedNew := false
	dGen := sc.dirtyGen
	for _, delta := range e.Stage3Plugin.AugmentRMStage2(idx, qTerms, groups, total, stage2) {
		if delta.Unit < 0 || int(delta.Unit) >= len(candidates) {
			continue
		}
		h1 := !math.IsNaN(delta.Hop1Nats) && !math.IsInf(delta.Hop1Nats, 0) && delta.Hop1Nats > 0
		h2 := !math.IsNaN(delta.Hop2Nats) && !math.IsInf(delta.Hop2Nats, 0) && delta.Hop2Nats > 0
		if !h1 && !h2 {
			continue
		}
		if sc.unitDirtyGen[delta.Unit] != dGen {
			sc.unitDirtyGen[delta.Unit] = dGen
			sc.dirtyUnits = append(sc.dirtyUnits, delta.Unit)
			unitStructGate[delta.Unit] = 1.0
			addedNew = true
		}
		c := &candidates[delta.Unit]
		if h1 {
			c.stSpread += delta.Hop1Nats
		}
		if h2 {
			c.stCorrob += delta.Hop2Nats
		}
	}
	if addedNew {
		sortInt32Asc(sc.dirtyUnits)
	}
}
