// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package cli

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestEpisodicScanPageDrainsAcrossSources(t *testing.T) {
	var turns []candidateEpisodicTurn
	for _, size := range []int{0, 1, 31, 1001} {
		turns = nil
		for i := 0; i < size; i++ {
			turns = append(turns, candidateEpisodicTurn{HarnessID: fmt.Sprintf("h%d", i%3), RefPointer: fmt.Sprintf("ref:%05d", size-i)})
		}
		seen := map[episodicScanCursor]bool{}
		var cursor episodicScanCursor
		for {
			var err error
			var more bool
			cursor, more, err = visitEpisodicScanPage(context.Background(), turns, cursor, 7, func(turn candidateEpisodicTurn) error {
				key := episodicScanCursor{HarnessID: turn.HarnessID, RefPointer: turn.RefPointer}
				if seen[key] {
					t.Fatalf("size %d: repeated %+v", size, key)
				}
				seen[key] = true
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if !more {
				break
			}
		}
		if len(seen) != size {
			t.Fatalf("size %d: visited %d", size, len(seen))
		}
	}
}

func TestEpisodicScanPageRetriesFailedOccurrence(t *testing.T) {
	turns := []candidateEpisodicTurn{
		{HarnessID: "h", RefPointer: "c"},
		{HarnessID: "h", RefPointer: "a"},
		{HarnessID: "h", RefPointer: "b"},
	}
	failure := errors.New("write failed")
	cursor, more, err := visitEpisodicScanPage(context.Background(), turns, episodicScanCursor{}, 3, func(turn candidateEpisodicTurn) error {
		if turn.RefPointer == "b" {
			return failure
		}
		return nil
	})
	if !errors.Is(err, failure) || !more || cursor.RefPointer != "a" {
		t.Fatalf("failed page cursor=%+v more=%v err=%v", cursor, more, err)
	}
	var retried []string
	cursor, more, err = visitEpisodicScanPage(context.Background(), turns, cursor, 3, func(turn candidateEpisodicTurn) error {
		retried = append(retried, turn.RefPointer)
		return nil
	})
	if err != nil || more || cursor.RefPointer != "c" || fmt.Sprint(retried) != "[b c]" {
		t.Fatalf("retry cursor=%+v more=%v turns=%v err=%v", cursor, more, retried, err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	before := episodicScanCursor{HarnessID: "h", RefPointer: "a"}
	cursor, more, err = visitEpisodicScanPage(cancelled, turns, before, 3, func(candidateEpisodicTurn) error { t.Fatal("visited after cancellation"); return nil })
	if !errors.Is(err, context.Canceled) || !more || cursor != before {
		t.Fatalf("cancelled cursor=%+v more=%v err=%v", cursor, more, err)
	}
}
