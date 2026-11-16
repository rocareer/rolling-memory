// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package thinclient

// FixedLog2Millibits returns log2(x) * 1024 using pure integer bit-scanning
// plus concave quadratic fractional interpolation:
//
//	log2(1 + f) ≈ f + f*(1-f)/3
//
// It is 100% BPE-free and requires zero floating-point transcendental calls.
func FixedLog2Millibits(x uint64) int64 {
	return fixedLog2Millibits(x)
}

// SubmodularCloneFoldedDF computes the Kish / Shannon chain-rule effective document
// frequency for a token's posting list. When adjacent postings within the same 24-hour
// window share at least 5/8 (~ 1 - 1/e) of the smaller document's distinct token set,
// they are treated as near-clone repetitions of the same underlying episode and folded
// logarithmically (1 + log2(m+1)/3) instead of inflating DF linearly.
func SubmodularCloneFoldedDF(
	postings []int,
	rawDF int,
	totalDocs int,
	occAt func(docIdx int) int64,
	termsAt func(docIdx int) map[uint64]int,
) int {
	if rawDF < 3 || rawDF > (totalDocs*3)/4 || len(postings) == 0 {
		return rawDF
	}
	indepEvents := 0
	cloneRun := 0
	prevIdx := -1
	step := 1
	if len(postings) > 512 {
		step = len(postings) / 512
	}
	sampled := 0
	for i := 0; i < len(postings); i += step {
		pIdx := postings[i]
		sampled++
		isClone := false
		if prevIdx >= 0 {
			dt := occAt(pIdx) - occAt(prevIdx)
			if dt < 0 {
				dt = -dt
			}
			if dt <= 86400 {
				aMap := termsAt(pIdx)
				bMap := termsAt(prevIdx)
				if len(aMap) >= 3 && len(bMap) >= 3 {
					if len(aMap) > len(bMap) {
						aMap, bMap = bMap, aMap
					}
					minLen, maxLen := len(aMap), len(bMap)
					if minLen*8 >= maxLen*5 {
						inter := 0
						for kh := range aMap {
							if bMap[kh] > 0 {
								inter++
							}
						}
						if inter*8 >= minLen*5 {
							isClone = true
						}
					}
				}
			}
		}
		if isClone {
			cloneRun++
			prevIdx = pIdx
			continue
		}
		if cloneRun > 1 {
			indepEvents += int(fixedLog2Millibits(uint64(cloneRun+1)) / 3072)
		}
		indepEvents++
		cloneRun = 1
		prevIdx = pIdx
	}
	if cloneRun > 1 {
		indepEvents += int(fixedLog2Millibits(uint64(cloneRun+1)) / 3072)
	}
	if step > 1 && sampled > 0 {
		indepEvents = (indepEvents * rawDF) / sampled
	}
	if indepEvents >= 1 && indepEvents < rawDF {
		return indepEvents
	}
	return rawDF
}

// ComputeDimensionlessCoherence computes the two BPE-free, scale-invariant
// Shannon self-information co-reference statistics between anchor daCounts
// and candidate diCounts:
//  1. infoDice in [0, 1]: length-normalized Sørensen-Dice purity over salient
//     corpus tokens (2 <= df(h) <= N/12);
//  2. nuEff >= 0: effective shared information degrees of freedom normalized
//     by the corpus horizon log2(N+4).
func ComputeDimensionlessCoherence(
	daCounts map[uint64]int,
	diCounts map[uint64]int,
	df map[uint64]int,
	totalDocs int,
	log2Total int64,
) (infoDice float64, nuEff float64) {
	if len(daCounts) == 0 || len(diCounts) == 0 || totalDocs <= 0 || log2Total <= 0 {
		return 0.0, 0.0
	}
	daInfoMass := SalientInfoMass(daCounts, df, totalDocs, log2Total)
	diInfoMass := SalientInfoMass(diCounts, df, totalDocs, log2Total)
	sharedInfoMass := SharedSalientInfoMass(daCounts, diCounts, df, totalDocs, log2Total)
	return CoherenceFromMasses(daInfoMass, diInfoMass, sharedInfoMass, log2Total)
}

// SalientInfoMass sums the fixed-point self-information of one document's
// salient terms (2 <= df <= totalDocs/12). It is a pure function of the
// document and the snapshot-level df table, so callers may cache it per doc.
func SalientInfoMass(counts map[uint64]int, df map[uint64]int, totalDocs int, log2Total int64) int64 {
	maxSalientDF := totalDocs / 12
	var mass int64
	for h := range counts {
		if d := df[h]; d >= 2 && d <= maxSalientDF {
			mass += log2Total - fixedLog2Millibits(uint64(d+1))
		}
	}
	return mass
}

// SharedSalientInfoMass sums the salient self-information of terms present in
// both documents. Each term's contribution depends only on its df, and int64
// addition is commutative, so iterating the smaller map and testing membership
// in the larger yields the exact sum of the straightforward one-sided loop.
func SharedSalientInfoMass(daCounts, diCounts map[uint64]int, df map[uint64]int, totalDocs int, log2Total int64) int64 {
	small, large := daCounts, diCounts
	if len(small) > len(large) {
		small, large = large, small
	}
	maxSalientDF := totalDocs / 12
	var shared int64
	for h := range small {
		if large[h] <= 0 {
			continue
		}
		if d := df[h]; d >= 2 && d <= maxSalientDF {
			shared += log2Total - fixedLog2Millibits(uint64(d+1))
		}
	}
	return shared
}

// CoherenceFromMasses finishes the dimensionless coherence pair from the three
// integer masses; identical divisions of identical integers reproduce
// ComputeDimensionlessCoherence bit for bit.
func CoherenceFromMasses(daInfoMass, diInfoMass, sharedInfoMass, log2Total int64) (infoDice float64, nuEff float64) {
	if daInfoMass+diInfoMass > 0 {
		infoDice = float64(2*sharedInfoMass) / float64(daInfoMass+diInfoMass)
	}
	nuEff = float64(sharedInfoMass) / float64(log2Total)
	return infoDice, nuEff
}

// TemporalProximityKernel evaluates the smooth hyperbolic temporal decay kernel
// tau(dt) = halfLifeSec / (halfLifeSec + |dt|) in (0, 1].
func TemporalProximityKernel(dt int64, halfLifeSec float64) float64 {
	if dt < 0 {
		dt = -dt
	}
	if halfLifeSec <= 0 {
		halfLifeSec = 1800.0
	}
	return halfLifeSec / (halfLifeSec + float64(dt))
}

// SubmodularCloneAttenuation computes the marginal novelty damping factor in (0, 1]
// for candidate i against higher-ranked hits [0..i-1]:
//   - Cross-day near-clones (>=85% overlap, length ratio >=80%) attenuate from the 2nd copy (0.86, 0.80);
//   - Same-day exact/near-exact clones (>=92% overlap, length ratio >=88%) preserve the first 2 copies
//     for multi-evidence episodes and attenuate the 3rd+ redundant clone copy as 1 / (1 + 0.35*(k-1))
//     so a single burst clone cluster cannot monopolize all Top-10 delivery slots.
func SubmodularCloneAttenuation(
	diTerms map[uint64]int,
	diOcc int64,
	priorCount int,
	priorAt func(j int) (map[uint64]int, int64, bool),
) float64 {
	if len(diTerms) < 6 || diOcc <= 0 {
		return 1.0
	}
	crossDayClones := 0
	sameDayExactClones := 0
	dayI := diOcc / 86400
	nI := len(diTerms)
	for j := 0; j < priorCount; j++ {
		djTerms, djOcc, ok := priorAt(j)
		if !ok || len(djTerms) < 6 || djOcc <= 0 {
			continue
		}
		nJ := len(djTerms)
		minN, maxN := nI, nJ
		if minN > maxN {
			minN, maxN = maxN, minN
		}
		if minN*100 < maxN*80 {
			continue
		}
		shared := 0
		// |di ∩ dj| is symmetric; iterating the smaller map against the larger
		// counts the same intersection with fewer hash probes.
		probe, in := diTerms, djTerms
		if len(probe) > len(in) {
			probe, in = in, probe
		}
		for h := range probe {
			if in[h] > 0 {
				shared++
			}
		}
		if dayI != djOcc/86400 {
			if nI >= 8 && nJ >= 8 && shared*100 >= minN*85 {
				crossDayClones++
			}
		} else if minN*100 >= maxN*88 && shared*100 >= minN*92 {
			sameDayExactClones++
		}
	}
	const eulerE = 2.71828182845904523536
	const invE = 1.0 / eulerE
	const invE2 = invE * invE
	attenuation := 1.0
	if crossDayClones >= 1 {
		attenuation = 1.0 / (1.0 + invE2*float64(crossDayClones))
	}
	if sameDayExactClones >= 2 {
		sameDayAtten := 1.0 / (1.0 + invE*float64(sameDayExactClones-1))
		if sameDayAtten < attenuation {
			attenuation = sameDayAtten
		}
	}
	return attenuation
}
