// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package cli

import (
	"context"
	"fmt"
	"sort"
)

// episodicScanCursor identifies the last completed source occurrence in a stable
// source order. It is advanced only after that occurrence has been handled.
type episodicScanCursor struct {
	HarnessID  string `json:"harness_id"`
	RefPointer string `json:"ref_pointer"`
}

// episodicScanPage makes a bounded page without discarding sources when the
// incoming list is reordered. The input must contain all available occurrences;
// callers must not first apply the old scanner's prefix limit.
func episodicScanPage(turns []candidateEpisodicTurn, after episodicScanCursor, limit int) ([]candidateEpisodicTurn, bool, error) {
	if limit <= 0 {
		return nil, false, fmt.Errorf("episodic scan page requires a positive limit")
	}
	ordered := append([]candidateEpisodicTurn(nil), turns...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].HarnessID != ordered[j].HarnessID {
			return ordered[i].HarnessID < ordered[j].HarnessID
		}
		return ordered[i].RefPointer < ordered[j].RefPointer
	})
	start := sort.Search(len(ordered), func(i int) bool {
		return ordered[i].HarnessID > after.HarnessID ||
			(ordered[i].HarnessID == after.HarnessID && ordered[i].RefPointer > after.RefPointer)
	})
	end := len(ordered)
	if len(ordered)-start > limit {
		end = start + limit
	}
	return ordered[start:end], end < len(ordered), nil
}

// visitEpisodicScanPage returns the last successfully handled occurrence on
// failure or cancellation, allowing a caller to persist only completed work.
func visitEpisodicScanPage(ctx context.Context, turns []candidateEpisodicTurn, after episodicScanCursor, limit int, visit func(candidateEpisodicTurn) error) (episodicScanCursor, bool, error) {
	page, more, err := episodicScanPage(turns, after, limit)
	if err != nil {
		return after, false, err
	}
	for _, turn := range page {
		if err := ctx.Err(); err != nil {
			return after, true, err
		}
		if turn.HarnessID == "" || turn.RefPointer == "" {
			return after, true, fmt.Errorf("episodic scan occurrence has no source identity")
		}
		if err := visit(turn); err != nil {
			return after, true, err
		}
		after = episodicScanCursor{HarnessID: turn.HarnessID, RefPointer: turn.RefPointer}
	}
	return after, more, nil
}
