// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package episodic

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
)

type cachedJSONLTurns struct {
	modNano int64
	size    int64
	sum     [32]byte
	hasSum  bool
	turns   [][]Segment
}

var (
	jsonlTurnsCache      sync.Map
	jsonlTurnsCacheCount int
	jsonlTurnsCacheMu    sync.Mutex
)

const maxJSONLTurnsCacheFiles = 256

func storeJSONLTurnsCache(path string, entry cachedJSONLTurns) {
	jsonlTurnsCacheMu.Lock()
	if jsonlTurnsCacheCount >= maxJSONLTurnsCacheFiles {
		jsonlTurnsCache.Range(func(k, _ any) bool {
			jsonlTurnsCache.Delete(k)
			return true
		})
		jsonlTurnsCacheCount = 0
	}
	jsonlTurnsCacheCount++
	jsonlTurnsCacheMu.Unlock()
	jsonlTurnsCache.Store(path, entry)
}

// jsonlTurnSegments uses the same row parser and turn boundaries as JSONLTurns,
// caching parsed turn segments keyed by file identity so resolving thousands of
// segment pointers in the same transcript does not re-parse the file per pointer.
func jsonlTurnSegments(path string, turnIndex int) ([]Segment, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("episodic: empty jsonl path")
	}
	if turnIndex < 0 {
		return nil, fmt.Errorf("episodic: turn %d not found in %s", turnIndex, path)
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	const hashThreshold = 256 * 1024
	if info.Size() > hashThreshold {
		if val, ok := jsonlTurnsCache.Load(path); ok {
			cached := val.(cachedJSONLTurns)
			if cached.modNano == info.ModTime().UnixNano() && cached.size == info.Size() {
				if turnIndex >= len(cached.turns) || len(cached.turns[turnIndex]) == 0 {
					return nil, fmt.Errorf("episodic: turn %d not found in %s", turnIndex, path)
				}
				out := make([]Segment, len(cached.turns[turnIndex]))
				copy(out, cached.turns[turnIndex])
				return out, nil
			}
		}
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var sum [32]byte
	hasSum := info.Size() <= hashThreshold
	if hasSum {
		sum = sha256.Sum256(raw)
		if val, ok := jsonlTurnsCache.Load(path); ok {
			cached := val.(cachedJSONLTurns)
			if cached.hasSum && cached.sum == sum {
				if turnIndex >= len(cached.turns) || len(cached.turns[turnIndex]) == 0 {
					return nil, fmt.Errorf("episodic: turn %d not found in %s", turnIndex, path)
				}
				out := make([]Segment, len(cached.turns[turnIndex]))
				copy(out, cached.turns[turnIndex])
				return out, nil
			}
		}
	}

	var turns [][]Segment
	current := -1
	var pendingAssist []Segment
	flushPending := func() {
		if current < 0 || len(pendingAssist) == 0 {
			pendingAssist = pendingAssist[:0]
			return
		}
		for _, seg := range pendingAssist {
			seg.Index = len(turns[current])
			turns[current] = append(turns[current], seg)
		}
		pendingAssist = pendingAssist[:0]
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if isJSONLToolCallLine(line) {
			pendingAssist = pendingAssist[:0]
			continue
		}
		role, texts, occurredAt := parseJSONLLine(line)
		if role != "" && len(texts) != 0 {
			if role == RoleUser {
				flushPending()
				current++
				for len(turns) <= current {
					turns = append(turns, nil)
				}
			}
			if current >= 0 {
				if role == RoleAssistant {
					for _, text := range texts {
						pendingAssist = append(pendingAssist, Segment{
							Role:       role,
							Text:       text,
							OccurredAt: occurredAt,
						})
					}
				} else {
					for _, text := range texts {
						turns[current] = append(turns[current], Segment{
							Role:       role,
							Text:       text,
							OccurredAt: occurredAt,
							Index:      len(turns[current]),
						})
					}
				}
			}
		}
	}
	flushPending()
	storeJSONLTurnsCache(path, cachedJSONLTurns{
		modNano: info.ModTime().UnixNano(),
		size:    info.Size(),
		sum:     sum,
		hasSum:  hasSum,
		turns:   turns,
	})
	if turnIndex >= len(turns) || len(turns[turnIndex]) == 0 {
		return nil, fmt.Errorf("episodic: turn %d not found in %s", turnIndex, path)
	}
	out := make([]Segment, len(turns[turnIndex]))
	copy(out, turns[turnIndex])
	return out, nil
}
