// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package store

import "sort"

// sequenceMatcher mirrors standard SequenceMatcher for two rune
// sequences. Only ratio() is needed, but the full find_longest_match /
// get_matching_blocks decomposition is kept so the ratio stays bit-identical
// near the 0.45 / 0.70 thresholds.
type sequenceMatcher struct {
	a   []rune
	b   []rune
	b2j map[rune][]int
}

func newSequenceMatcher(a, b []rune, autojunk bool) *sequenceMatcher {
	matcher := &sequenceMatcher{a: a, b: b, b2j: map[rune][]int{}}
	for index, char := range b {
		matcher.b2j[char] = append(matcher.b2j[char], index)
	}
	if autojunk && len(b) >= 200 {
		popular := map[rune]struct{}{}
		threshold := len(b)/100 + 1
		for char, positions := range matcher.b2j {
			if len(positions) > threshold {
				popular[char] = struct{}{}
			}
		}
		for char := range popular {
			delete(matcher.b2j, char)
		}
	}
	return matcher
}

func sequenceRatio(a, b []rune, autojunk bool) float64 {
	return newSequenceMatcher(a, b, autojunk).ratio()
}

func (m *sequenceMatcher) ratio() float64 {
	total := len(m.a) + len(m.b)
	if total == 0 {
		return 1.0
	}
	matched := 0
	for _, block := range m.matchingBlocks() {
		matched += block.size
	}
	return 2.0 * float64(matched) / float64(total)
}

type matchBlock struct {
	a    int
	b    int
	size int
}

func (m *sequenceMatcher) matchingBlocks() []matchBlock {
	type region struct {
		aLow, aHigh int
		bLow, bHigh int
	}
	queue := []region{{0, len(m.a), 0, len(m.b)}}
	blocks := []matchBlock{}
	for len(queue) > 0 {
		current := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		block := m.findLongestMatch(current.aLow, current.aHigh, current.bLow, current.bHigh)
		if block.size == 0 {
			continue
		}
		blocks = append(blocks, block)
		if current.aLow < block.a && current.bLow < block.b {
			queue = append(queue, region{current.aLow, block.a, current.bLow, block.b})
		}
		if block.a+block.size < current.aHigh && block.b+block.size < current.bHigh {
			queue = append(queue, region{
				block.a + block.size, current.aHigh,
				block.b + block.size, current.bHigh,
			})
		}
	}
	sort.Slice(blocks, func(left, right int) bool {
		if blocks[left].a != blocks[right].a {
			return blocks[left].a < blocks[right].a
		}
		return blocks[left].b < blocks[right].b
	})
	return blocks
}

func (m *sequenceMatcher) findLongestMatch(aLow, aHigh, bLow, bHigh int) matchBlock {
	bestA, bestB, bestSize := aLow, bLow, 0
	j2len := map[int]int{}
	for index := aLow; index < aHigh; index++ {
		newJ2len := map[int]int{}
		for _, position := range m.b2j[m.a[index]] {
			if position < bLow {
				continue
			}
			if position >= bHigh {
				break
			}
			length := j2len[position-1] + 1
			newJ2len[position] = length
			if length > bestSize {
				bestA = index - length + 1
				bestB = position - length + 1
				bestSize = length
			}
		}
		j2len = newJ2len
	}
	for bestA > aLow && bestB > bLow && m.a[bestA-1] == m.b[bestB-1] {
		bestA--
		bestB--
		bestSize++
	}
	for bestA+bestSize < aHigh && bestB+bestSize < bHigh &&
		m.a[bestA+bestSize] == m.b[bestB+bestSize] {
		bestSize++
	}
	return matchBlock{a: bestA, b: bestB, size: bestSize}
}
