// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package recall

import "testing"

func weaveHit(session string) map[string]any {
	return map[string]any{"session": session}
}

func weaveSessions(hits []map[string]any) []string {
	out := make([]string, 0, len(hits))
	for _, h := range hits {
		out = append(out, h["session"].(string))
	}
	return out
}

// TestWeaveBySessionInterleavesWithinCap pins the session-diversity contract: the merged pool is
// delivered round-robin by session (two rounds), so a multi-day window query covers every
// contributing session instead of letting one session own the head of the raw list.
func TestWeaveBySessionInterleavesWithinCap(t *testing.T) {
	hits := []map[string]any{weaveHit("a"), weaveHit("b"), weaveHit("a"), weaveHit("a"), weaveHit("b")}
	got := weaveSessions(weaveBySession(hits, 2))
	want := []string{"a", "b", "a", "b", "a"}
	if len(got) != len(want) {
		t.Fatalf("weave must not drop hits: got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("weave order mismatch at %d: got %v want %v", i, got, want)
		}
	}
}

// TestWeaveBySessionPreservesSingleSessionOrder guards the degenerate single-session case: with no
// competing session the reorder must be an identity (arrival order preserved, nothing dropped).
func TestWeaveBySessionPreservesSingleSessionOrder(t *testing.T) {
	hits := []map[string]any{weaveHit("s"), weaveHit("s"), weaveHit("s"), weaveHit("s")}
	got := weaveSessions(weaveBySession(hits, 2))
	if len(got) != len(hits) {
		t.Fatalf("single-session weave must not drop hits: got %d want %d", len(got), len(hits))
	}
	for i, s := range got {
		if s != "s" {
			t.Fatalf("single-session weave must preserve order, mismatch at %d: %v", i, got)
		}
	}
}

func TestWeaveBySessionEmptyAndDisabled(t *testing.T) {
	if out := weaveBySession(nil, 2); len(out) != 0 {
		t.Fatalf("nil pool must stay empty, got %d", len(out))
	}
	hits := []map[string]any{weaveHit("a"), weaveHit("b"), weaveHit("a")}
	if out := weaveBySession(hits, 0); len(out) != len(hits) {
		t.Fatalf("disabled weave must pass every hit through: got %d want %d", len(out), len(hits))
	} else {
		for i := range hits {
			if out[i]["session"] != hits[i]["session"] {
				t.Fatalf("disabled weave must preserve order at %d", i)
			}
		}
	}
}
