// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

// Package closedset 维护 Zhao 架构的双层正交闭集世界知识同态表与零分配查找器：
//   - Part I (StandardClosedSetTable)：国际标准闭集（ISO-3166 地理、ISO-8601 日历、ISO-4217 货币、SI/IEC 度量衡）
//   - Part II (UniversalSchemaClosedSetTable)：通用人类记忆框架闭集（亲属拓扑、社会角色、场馆交通、财务凭证、气象生物）
package closedset

import (
	"strings"
	"sync"
)

var (
	mapOnce sync.Once
	tagMap  map[string][][2]string
	stdOnce sync.Once
	stdMap  map[string][][2]string
)

func buildEntriesMap(entries []string, capHint int) map[string][][2]string {
	m := make(map[string][][2]string, capHint)
	for _, entry := range entries {
		colon := strings.LastIndexByte(entry, ':')
		if colon <= 0 {
			continue
		}
		head := entry[:colon]
		pipe := strings.IndexByte(head, '|')
		if pipe <= 0 {
			continue
		}
		pair := [2]string{head[:pipe], head[pipe+1:]}
		for _, tok := range strings.Split(entry[colon+1:], ",") {
			if tok == "" {
				continue
			}
			axes := m[tok]
			dup := false
			for _, prev := range axes {
				if prev == pair {
					dup = true
					break
				}
			}
			if !dup {
				m[tok] = append(axes, pair)
			}
		}
	}
	return m
}

func ensureMap() {
	mapOnce.Do(func() {
		tagMap = buildEntriesMap(ClosedSetHarmonicEntries, 6100)
	})
}

func ensureStdMap() {
	stdOnce.Do(func() {
		stdMap = buildEntriesMap(StandardClosedSetTable, 4000)
	})
}

// StandardTagMap returns the Part-I-only (international standard: ISO-3166/8601/4217 & SI/IEC) lookup map.
func StandardTagMap() map[string][][2]string {
	ensureStdMap()
	return stdMap
}

// LookupFirstStandardTag resolves the first 1st-order international standard closed-set tag (Part I only).
func LookupFirstStandardTag(low string) string {
	if low == "" {
		return ""
	}
	isSingleASCIILetter := len(low) == 1 && low[0] >= 'a' && low[0] <= 'z'
	ensureStdMap()
	axes := stdMap[low]
	if len(axes) == 0 && strings.HasSuffix(low, ".") {
		axes = stdMap[strings.TrimSuffix(low, ".")]
	}
	for _, pair := range axes {
		for _, tag := range pair {
			if tag == "" || tag == low || strings.HasPrefix(tag, "@dim:") || strings.HasPrefix(tag, "@geo:cont:") {
				continue
			}
			if isSingleASCIILetter && strings.HasPrefix(tag, "@unit:") {
				continue
			}
			return tag
		}
	}
	return ""
}

// HasToken reports whether low is registered in the closed-set dictionary.
func HasToken(low string) bool {
	ensureMap()
	return len(tagMap[low]) > 0
}

// TagMap returns the initialized closed-set token-to-axes lookup map.
func TagMap() map[string][][2]string {
	ensureMap()
	return tagMap
}

// LookupGeoPair resolves a token into its 1st-order (@geo:cc:<iso2> or @geo:cont:<cont>)
// and 2nd-order (@geo:cont:<cont> or @dim:geo) ISO 3166 geographic harmonic tags.
func LookupGeoPair(token string) (string, string) {
	ensureMap()
	l := strings.ToLower(strings.TrimSpace(token))
	for _, pair := range tagMap[l] {
		if !strings.HasPrefix(pair[0], "@unit:") {
			return pair[0], pair[1]
		}
	}
	return "", ""
}

// LookupGeoHarmonics maps multilingual L0-L3 ISO 3166 toponyms to canonical 2-tier geographic harmonic tags.
func LookupGeoHarmonics(token string) []string {
	t1, t2 := LookupGeoPair(token)
	if t1 == "" {
		return nil
	}
	if t2 == "" || t2 == t1 {
		return []string{t1}
	}
	return []string{t1, t2}
}

// LookupUnitPair resolves a token into its (@unit:*, @dim:*) pair.
func LookupUnitPair(token string) (string, string) {
	ensureMap()
	l := strings.ToLower(strings.TrimSpace(token))
	for _, pair := range tagMap[l] {
		if strings.HasPrefix(pair[0], "@unit:") && strings.HasPrefix(pair[1], "@dim:") {
			return pair[0], pair[1]
		}
	}
	return "", ""
}

// LookupFirstTag resolves the first 1st-order closed-set dictionary tag from tagMap
// with zero slice/map allocation.
func LookupFirstTag(low string) string {
	if low == "" {
		return ""
	}
	isSingleASCIILetter := len(low) == 1 && low[0] >= 'a' && low[0] <= 'z'
	ensureMap()
	axes := tagMap[low]
	if len(axes) == 0 && strings.HasSuffix(low, ".") {
		axes = tagMap[strings.TrimSuffix(low, ".")]
	}
	for _, pair := range axes {
		for _, tag := range pair {
			if tag == "" || tag == low || strings.HasPrefix(tag, "@dim:") || strings.HasPrefix(tag, "@geo:cont:") {
				continue
			}
			if isSingleASCIILetter && strings.HasPrefix(tag, "@unit:") {
				continue
			}
			return tag
		}
	}
	return ""
}

// LookupHarmonics returns all deduplicated closed-set tags for token.
func LookupHarmonics(token string) []string {
	ensureMap()
	l := strings.ToLower(strings.TrimSpace(token))
	axes := tagMap[l]
	if len(axes) == 0 && strings.HasSuffix(l, ".") {
		axes = tagMap[strings.TrimSuffix(l, ".")]
	}
	if len(axes) == 0 {
		return nil
	}
	out := make([]string, 0, 2*len(axes))
	seen := make(map[string]bool, 2*len(axes))
	for _, pair := range axes {
		for _, tag := range pair {
			if tag == "" || seen[tag] {
				continue
			}
			seen[tag] = true
			out = append(out, tag)
		}
	}
	return out
}

var (
	ordOnce sync.Once
	ordMap  map[string]int8
)

func ensureOrdMap() {
	ordOnce.Do(func() {
		ordMap = make(map[string]int8, 64)
		for _, entry := range OrdinalPolarityClosedSetTable {
			colon := strings.LastIndexByte(entry, ':')
			if colon <= 0 {
				continue
			}
			head := entry[:colon]
			pipe := strings.IndexByte(head, '|')
			if pipe <= 0 {
				continue
			}
			var pol int8
			switch head[:pipe] {
			case "@cal:ord:begin":
				pol = -1
			case "@cal:ord:end":
				pol = 1
			default:
				continue
			}
			for _, tok := range strings.Split(entry[colon+1:], ",") {
				if tok != "" {
					ordMap[tok] = pol
				}
			}
		}
	})
}

// LookupOrdinalPolarity reports the calendar span polarity class of a token:
// -1 for @cal:ord:begin (first/earliest), +1 for @cal:ord:end (last/latest),
// 0 when the token carries no span polarity. Query-side only by design.
func LookupOrdinalPolarity(low string) int8 {
	if low == "" {
		return 0
	}
	ensureOrdMap()
	return ordMap[low]
}
