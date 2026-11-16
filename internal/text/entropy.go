// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package text

import (
	"math"
	"unicode"
)

const (
	AtomicConceptEntropyBits      = 8.0
	SingleAlternationMaxBits      = 4.5
	OverStemmingDivergenceMaxBits = 11.5
)

// RuneShannonBits returns the intrinsic prior Shannon information content H_0(r) (in bits)
// of a single Unicode code point r based on the effective symbol capacity of its Unicode script block.
func RuneShannonBits(r rune) float64 {
	if r < 0x80 {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			return 2.0
		}
		if r >= '0' && r <= '9' {
			return 3.32
		}
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return 0.0
		}
		return 1.0
	}
	if unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Mc, r) || unicode.Is(unicode.Me, r) {
		return 0.5
	}
	if unicode.IsSpace(r) || unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
		return 0.0
	}
	switch {
	case unicode.Is(unicode.Han, r):
		return 10.0
	case unicode.Is(unicode.Hangul, r):
		return 7.0
	case unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r) || r == 'ー' ||
		unicode.Is(unicode.Thai, r) || unicode.Is(unicode.Lao, r) ||
		unicode.Is(unicode.Khmer, r) || unicode.Is(unicode.Myanmar, r) ||
		unicode.Is(unicode.Tibetan, r):
		return 4.2
	case unicode.IsDigit(r):
		return 3.32
	case unicode.IsLetter(r):
		return 2.0
	default:
		return 1.0
	}
}

// classifyRuneShannonFast returns (isCfSkip, isSpace, isAlnum, lowR, priorBits) with an inline
// zero-table ASCII fast path, preserving 100% bitwise equivalence with unicode.* across all codepoints.
func classifyRuneShannonFast(r rune) (skipCf, isSpace, isAlnum bool, lowR rune, rb float64) {
	if r < 0x80 {
		if r >= 'a' && r <= 'z' {
			return false, false, true, r, 2.0
		}
		if r >= 'A' && r <= 'Z' {
			return false, false, true, r + ('a' - 'A'), 2.0
		}
		if r >= '0' && r <= '9' {
			return false, false, true, r, 3.32
		}
		if r == ' ' || (r >= '\t' && r <= '\r') {
			return false, true, false, r, 0.0
		}
		if r < 0x20 || r == 0x7F {
			return false, false, false, r, 0.0
		}
		return false, false, false, r, 1.0
	}
	if r >= 0xAD && unicode.Is(unicode.Cf, r) {
		return true, false, false, 0, 0
	}
	isSpace = unicode.IsSpace(r)
	lowR = unicode.ToLower(r)
	isAlnum = unicode.IsLetter(r) || unicode.IsNumber(r)
	rb = RuneShannonBits(r)
	return false, isSpace, isAlnum, lowR, rb
}

// powQuarter returns 0.25^exp (for exp >= 1) using fast bit-shift / multiplication without math.Pow overhead.
func powQuarter(exp int) float64 {
	switch exp {
	case 1:
		return 0.25
	case 2:
		return 0.0625
	case 3:
		return 0.015625
	case 4:
		return 0.00390625
	default:
		if exp <= 24 {
			return 1.0 / float64(uint64(1)<<(2*uint(exp)))
		}
		return math.Pow(0.25, float64(exp))
	}
}

// SliceShannonBits computes the total prior Shannon self-information (in bits) of a rune slice,
// enforcing non-carrier zeroing (0.0 bits when no letter/digit is present), 1st-order Markov repetition
// damping (0.25^(k-2) for identical rune runs of length k >= 3), sliding-window period-p (p in 1..4)
// tandem-repeat collapse on compact single-token strings, and cross-space periodic flood collapse on
// long space-delimited repetition floods (e.g. "a a a ...", "ab ab ab ...").
func SliceShannonBits(rs []rune) float64 {
	var bits float64
	hasAlnum := false
	hasSpace := false
	var prevRune rune
	runLen := 0
	var buf [64]rune
	var ring [4]rune
	var pMatch [5]int
	tandemTotal := 0
	inTandemCur := false
	n := 0
	for _, r := range rs {
		skipCf, isSp, isAlnum, lowR, rb := classifyRuneShannonFast(r)
		if skipCf {
			continue
		}
		if isSp {
			hasSpace = true
		}
		if isAlnum {
			hasAlnum = true
			if n < 64 {
				buf[n] = lowR
			}
			tandemTotal, inTandemCur = stepStreamingTandem(lowR, n, &ring, &pMatch, tandemTotal, inTandemCur)
			n++
		}
		if lowR == prevRune {
			runLen++
		} else {
			prevRune = lowR
			runLen = 1
		}
		if runLen >= 3 && rb > 0 {
			rb *= powQuarter(runLen - 2)
		}
		bits += rb
	}
	if !hasAlnum {
		return 0
	}
	if !hasSpace && n >= 4 {
		bits *= fullStringTandemDamping(&buf, n, tandemTotal)
	} else if hasSpace && n >= 16 && tandemTotal*4 >= n*3 {
		bits *= spacedPeriodicFloodDamping(n, tandemTotal)
	}
	return bits
}

// StringShannonBits computes the total prior Shannon self-information (in bits) of a string,
// enforcing non-carrier zeroing (0.0 bits when no letter/digit is present), 1st-order Markov repetition
// damping (0.25^(k-2) for identical rune runs of length k >= 3), sliding-window period-p (p in 1..4)
// tandem-repeat collapse on compact single-token strings, and cross-space periodic flood collapse on
// long space-delimited repetition floods.
//
// 双副本契约：FTS 边界变体额外忽略 FTS 边界标记 '^'/'$'
// （本版各计 1.0 bit）；其余行为必须逐值全等，由 TestShannonPrimitivesDualCopyParity 守卫。
func StringShannonBits(s string) float64 {
	var bits float64
	hasAlnum := false
	hasSpace := false
	var prevRune rune
	runLen := 0
	var buf [64]rune
	var ring [4]rune
	var pMatch [5]int
	tandemTotal := 0
	inTandemCur := false
	n := 0
	for _, r := range s {
		skipCf, isSp, isAlnum, lowR, rb := classifyRuneShannonFast(r)
		if skipCf {
			continue
		}
		if isSp {
			hasSpace = true
		}
		if isAlnum {
			hasAlnum = true
			if n < 64 {
				buf[n] = lowR
			}
			tandemTotal, inTandemCur = stepStreamingTandem(lowR, n, &ring, &pMatch, tandemTotal, inTandemCur)
			n++
		}
		if lowR == prevRune {
			runLen++
		} else {
			prevRune = lowR
			runLen = 1
		}
		if runLen >= 3 && rb > 0 {
			rb *= powQuarter(runLen - 2)
		}
		bits += rb
	}
	if !hasAlnum {
		return 0
	}
	if !hasSpace && n >= 4 {
		bits *= fullStringTandemDamping(&buf, n, tandemTotal)
	} else if hasSpace && n >= 16 && tandemTotal*4 >= n*3 {
		bits *= spacedPeriodicFloodDamping(n, tandemTotal)
	}
	return bits
}

// spacedPeriodicFloodDamping collapses degenerate space-separated period-1..4 alphanumeric floods
// (where >=75% of >=16 carrier runes are locked in a period-1..4 tandem loop, e.g. "a a a ..." or "ab ab ab ...")
// below AtomicConceptEntropyBits, while leaving normal multi-word natural language sentences untouched.
func spacedPeriodicFloodDamping(n, tandemTotal int) float64 {
	ratio := float64(tandemTotal) / float64(n)
	dFull := 1.0 - ratio
	if dFull < 0.05 {
		dFull = 0.05
	}
	scale := 16.0 / float64(n)
	if scale > 1.0 {
		scale = 1.0
	}
	return dFull * dFull * 0.25 * scale
}

func stepStreamingTandem(lowR rune, n int, ring *[4]rune, pMatch *[5]int, tandemTotal int, inTandemCur bool) (int, bool) {
	hitPeriod := false
	for p := 1; p <= 4; p++ {
		if n >= p && lowR == ring[(n-p)&3] {
			pMatch[p]++
			if pMatch[p] == 2*p {
				if !inTandemCur {
					add := 3*p - 1
					if tandemTotal+add > n {
						add = n - tandemTotal
					}
					tandemTotal += add
				}
				hitPeriod = true
			} else if pMatch[p] > 2*p {
				hitPeriod = true
			}
		} else {
			pMatch[p] = 0
		}
	}
	if hitPeriod {
		tandemTotal++
		inTandemCur = true
	} else {
		inTandemCur = false
	}
	ring[n&3] = lowR
	return tandemTotal, inTandemCur
}

func fullStringTandemDamping(buf *[64]rune, n, tandemTotal int) float64 {
	win := n
	if win > 64 {
		win = 64
	}
	d := periodicTandemDamping64(buf, win)
	if n > 64 && tandemTotal > 0 {
		ratio := float64(tandemTotal) / float64(n)
		if ratio >= 0.50 {
			dFull := 1.0 - ratio
			if dFull < 0.05 {
				dFull = 0.05
			}
			dStream := dFull * dFull * 0.04 * (64.0 / float64(n))
			if dStream < d {
				d = dStream
			}
		}
	}
	return d
}

func periodicTandemDamping64(buf *[64]rune, n int) float64 {
	var inTandem [64]bool
	anyTandem := false
	for p := 1; p <= 4; p++ {
		if n < 3*p {
			break
		}
		for start := 0; start <= n-3*p; start++ {
			match3 := true
			for k := start + p; k < start+3*p; k++ {
				if buf[k] != buf[start+(k-start)%p] {
					match3 = false
					break
				}
			}
			if match3 {
				anyTandem = true
				end := start + 3*p
				for end < n && buf[end] == buf[start+(end-start)%p] {
					end++
				}
				for k := start; k < end; k++ {
					inTandem[k] = true
				}
			}
		}
	}
	if !anyTandem {
		return 1.0
	}
	tandemCnt := 0
	for i := 0; i < n; i++ {
		if inTandem[i] {
			tandemCnt++
		}
	}
	ratio := float64(tandemCnt) / float64(n)
	if ratio >= 0.60 {
		d := 1.0 - ratio
		if d < 0.05 {
			d = 0.05
		}
		return d * d * 0.25
	}
	return 1.0
}

// IsPureHan reports whether s consists solely of Han ideographs (without Hiragana/Katakana inflection endings).
func IsPureHan(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !unicode.Is(unicode.Han, r) {
			return false
		}
	}
	return true
}
