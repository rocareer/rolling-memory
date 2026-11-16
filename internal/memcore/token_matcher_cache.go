// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package memcore

import "sync/atomic"

// compiledTokenSpec 预编译词元静态数学规格（与 hay 无关的纯函数不变量）：
// 消除在 evalBridgeSurfaceHits / evalSlot / turnTokenMatchMemo 内层数万次循环中对同一 (tok, st)
// 重复执行 IsShortOrDigitsToken、HasIdeographRune、IsPureHan、[]rune 分配、SliceShannonBits 与
// string(qRunes[:onsetEnd]) / string(qRunes[codaStart:]) 堆分配（runtime.slicerunetostring）。
type compiledTokenSpec struct {
	tok         string
	st          string
	tokShort    bool
	stShort     bool
	hasIdeo     bool
	pureHan     bool
	bQ          float64
	onsetEnd    int
	onsetStr    string
	hasCodaCand bool
	codaStr     string
	qRunes      []rune
	stRunes     []rune
}

var compiledTokSlots [1024]atomic.Pointer[compiledTokenSpec]

func fnv32Pair(a, b string) uint32 {
	var h uint32 = 2166136261
	for i := 0; i < len(a); i++ {
		h ^= uint32(a[i])
		h *= 16777619
	}
	h ^= 0xff
	h *= 16777619
	for i := 0; i < len(b); i++ {
		h ^= uint32(b[i])
		h *= 16777619
	}
	return h
}

func loadOrCompileTokenSpec(tok, stem string) *compiledTokenSpec {
	if len(tok) <= 64 && len(stem) <= 64 {
		slot := fnv32Pair(tok, stem) & 1023
		if spec := compiledTokSlots[slot].Load(); spec != nil && spec.tok == tok && spec.st == stem {
			return spec
		}
		spec := buildCompiledTokenSpec(tok, stem)
		compiledTokSlots[slot].Store(spec)
		return spec
	}
	return buildCompiledTokenSpec(tok, stem)
}

func buildCompiledTokenSpec(tok, stem string) *compiledTokenSpec {
	spec := &compiledTokenSpec{
		tok:      tok,
		st:       stem,
		tokShort: IsShortOrDigitsToken(tok),
	}
	if spec.tokShort {
		return spec
	}
	spec.hasIdeo = HasIdeographRune(tok)
	if spec.hasIdeo {
		spec.pureHan = IsPureHan(tok)
		if !spec.pureHan {
			spec.qRunes = []rune(tok)
		}
		return spec
	}
	qRunes := []rune(tok)
	bQ := SliceShannonBits(qRunes)
	spec.qRunes = qRunes
	spec.bQ = bQ
	if stem != "" && stem != tok {
		if IsShortOrDigitsToken(stem) {
			spec.stShort = true
		} else {
			spec.stRunes = []rune(stem)
		}
	}
	if bQ < 0.50*AtomicConceptEntropyBits {
		return spec
	}
	onsetEnd := 0
	var onsetBits float64
	for onsetEnd < len(qRunes) && onsetBits < 0.50*AtomicConceptEntropyBits {
		onsetBits += RuneShannonBits(qRunes[onsetEnd])
		onsetEnd++
	}
	spec.onsetEnd = onsetEnd
	if onsetEnd > 0 {
		spec.onsetStr = string(qRunes[:onsetEnd])
	}
	if bQ >= 1.25*AtomicConceptEntropyBits {
		codaStart := len(qRunes)
		var codaBits float64
		for codaStart > 0 && codaBits < 0.75*AtomicConceptEntropyBits {
			codaStart--
			codaBits += RuneShannonBits(qRunes[codaStart])
		}
		spec.hasCodaCand = true
		spec.codaStr = string(qRunes[codaStart:])
	}
	return spec
}

type cachedTimeWindowsEntry struct {
	q    string
	wins []TimeWindow
}

var timeWindowsSlots [512]atomic.Pointer[cachedTimeWindowsEntry]

func lookupCachedTimeWindows(q string) ([]TimeWindow, bool) {
	if len(q) > 1024 {
		return nil, false
	}
	slot := fnv32Pair(q, "") & 511
	e := timeWindowsSlots[slot].Load()
	if e == nil || e.q != q {
		return nil, false
	}
	if len(e.wins) == 0 {
		return []TimeWindow{}, true
	}
	out := make([]TimeWindow, len(e.wins))
	copy(out, e.wins)
	return out, true
}

func storeCachedTimeWindows(q string, wins []TimeWindow) {
	if len(q) > 1024 {
		return
	}
	var copied []TimeWindow
	if len(wins) > 0 {
		copied = make([]TimeWindow, len(wins))
		copy(copied, wins)
	}
	slot := fnv32Pair(q, "") & 511
	timeWindowsSlots[slot].Store(&cachedTimeWindowsEntry{q: q, wins: copied})
}
