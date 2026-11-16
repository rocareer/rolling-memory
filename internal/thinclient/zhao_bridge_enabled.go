// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package thinclient

import (
	"sort"

	"github.com/rocareer/rolling-memory/internal/zhao/rm"
)

// ZhaoRMCoreEmbedded reports whether the full 4-step dual-cluster engine
// (`internal/zhao/rm`, excluding cloud-only `knn`) is compiled into this binary.
const ZhaoRMCoreEmbedded = true

// NewZhaoRMRuntimeEngine constructs an in-memory 4-step dual-cluster engine
// from the embedded `internal/zhao/rm` kernel.
func NewZhaoRMRuntimeEngine(docs []rm.Doc) *rm.Engine {
	return rm.NewEngine(rm.BuildIndex(docs))
}

// noeticFusionDoc projects a matched Layer-3 consolidation hit into the fusion
// mini-index as a pure citation carrier: identity, occurrence clock, layer tag
// and the declarative CitedIDs edge list, with zero fabricated terms. The
// kernel registers term-less units in UnitMeta, so the citation provenance
// edges (subject-cos floor + complementary anchor bridge) stay active while
// the carrier itself never competes in the lexical channel.
func noeticFusionDoc(hit CloudQueryHit) (rm.Doc, bool) {
	if hit.UnitID == "" || len(hit.CitedUnitIDs) == 0 {
		return rm.Doc{}, false
	}
	return rm.Doc{
		UnitID:     hit.UnitID,
		OccurredAt: hit.OccurredAt,
		Layer:      LayerNoetic,
		CitedIDs:   append([]string(nil), hit.CitedUnitIDs...),
	}, true
}

func applyZhaoRMEnergyFusion(snap *vaultCorpusSnapshot, qPkt BlindPulsePacket, topK int, docs []vaultDocEntry, docIdxMap map[string]int, hits []CloudQueryHit) []CloudQueryHit {
	if snap == nil || len(hits) <= 1 || len(qPkt.Slots) == 0 {
		return hits
	}
	candLim := topK * 2
	if candLim < 16 {
		candLim = 16
	}
	if candLim > 24 {
		candLim = 24
	}
	if candLim > len(hits) {
		candLim = len(hits)
	}
	qAtoms := make(map[uint64]rm.Term, len(qPkt.Slots))
	qPos := make(map[uint64][]rm.TermPosition, len(qPkt.Slots))
	for sIdx, sf := range qPkt.SentenceFrames {
		for slotIdx, tok := range sf {
			if tok.H == 0 {
				continue
			}
			bits := tok.RawBitsX10
			if bits == 0 {
				bits = 32
			}
			role := rm.RolePrimary
			ext := rm.ExtFlagSeenPrimary
			if (tok.RawBitsX10&1) == 1 && (tok.ClassFlags&FlagHasConn) != 0 {
				role = rm.RoleMorphStem
				ext = rm.ExtFlagSeenMorphOrClos
			}
			if cur, ok := qAtoms[tok.H]; !ok || bits > cur.Bits {
				qAtoms[tok.H] = rm.Term{
					Hash:     tok.H,
					Bits:     bits,
					Flags:    tok.ClassFlags,
					Role:     role,
					ExtFlags: ext,
					Group:    tok.H,
					Sent:     uint16(sIdx + 1),
					Slot:     uint16(slotIdx + 1),
				}
			}
			qPos[tok.H] = append(qPos[tok.H], rm.TermPosition{Sentence: sIdx, Slot: slotIdx + 1})
		}
	}
	if len(qAtoms) == 0 {
		return hits
	}
	rmDocs := make([]rm.Doc, 0, candLim)
	for i := 0; i < candLim; i++ {
		di, ok := docIdxMap[hits[i].UnitID]
		if !ok {
			// Noetic consolidation hits live outside the episodic corpus map.
			// Project them into the mini-index as citation carriers so the
			// kernel's citation provenance edges (subject-cos floor 1/e +
			// Question-3 complementary anchor bridge) couple each consolidation
			// to the evidence units a reader previously confirmed.
			if hits[i].Layer == LayerNoetic && len(hits[i].CitedUnitIDs) > 0 {
				if nd, ok := noeticFusionDoc(hits[i]); ok {
					rmDocs = append(rmDocs, nd)
				}
			}
			continue
		}
		d := &docs[di]
		atoms := make(map[uint64]rm.Term, len(qAtoms)*2)
		counts := make(map[uint64]int, len(qAtoms)*2)
		positions := make(map[uint64][]rm.TermPosition, len(qAtoms)*2)
		for sIdx, sh := range d.sentHashes {
			for slotIdx, h := range sh {
				if h == 0 {
					continue
				}
				if _, inQ := qAtoms[h]; !inQ && len(atoms) >= 24 {
					continue
				}
				pos := rm.TermPosition{Sentence: sIdx, Slot: slotIdx + 1}
				positions[h] = append(positions[h], pos)
				counts[h]++
				if _, exists := atoms[h]; !exists {
					atoms[h] = rm.Term{
						Hash:     h,
						Bits:     32,
						Role:     rm.RolePrimary,
						ExtFlags: rm.ExtFlagSeenPrimary,
						Group:    h,
						Sent:     uint16(sIdx + 1),
						Slot:     uint16(slotIdx + 1),
					}
				}
			}
		}
		for h := range qAtoms {
			if c := d.termCounts[h]; c > 0 {
				if _, exists := atoms[h]; !exists {
					atoms[h] = rm.Term{
						Hash:     h,
						Bits:     32,
						Role:     rm.RolePrimary,
						ExtFlags: rm.ExtFlagSeenPrimary,
						Group:    h,
						Sent:     1,
						Slot:     1,
					}
					counts[h] = c
					positions[h] = []rm.TermPosition{{Sentence: 0, Slot: 1}}
				}
			}
		}
		rmDocs = append(rmDocs, rm.Doc{
			UnitID:     d.docID,
			OccurredAt: d.occ,
			Terms:      atoms,
			Counts:     counts,
			Positions:  positions,
		})
	}
	if len(rmDocs) < 2 {
		return hits
	}
	eng := rm.NewEngine(rm.BuildIndex(rmDocs))
	zHits := eng.SearchWithPositions(qAtoms, qPos, candLim)
	if len(zHits) == 0 || zHits[0].Energy <= 0 {
		return hits
	}
	maxZ := zHits[0].Energy
	zScoreByID := make(map[string]float64, len(zHits))
	for _, zh := range zHits {
		if zh.Energy > 0 {
			zScoreByID[zh.UnitID] = zh.Energy / maxZ
		}
	}
	maxW := hits[0].FinalWeight
	if maxW <= 0 {
		maxW = 1.0
	}
	for i := 0; i < candLim; i++ {
		if zNorm, ok := zScoreByID[hits[i].UnitID]; ok {
			hits[i].FinalWeight += 0.1353 * maxW * zNorm
			hits[i].WaveScore = hits[i].FinalWeight
		}
	}
	sub := hits[:candLim]
	sort.SliceStable(sub, func(i, j int) bool {
		if sub[i].FinalWeight != sub[j].FinalWeight {
			return sub[i].FinalWeight > sub[j].FinalWeight
		}
		return sub[i].UnitID < sub[j].UnitID
	})
	return hits
}
