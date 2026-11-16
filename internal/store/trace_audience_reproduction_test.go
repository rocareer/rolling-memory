// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package store

import "testing"

// This pins the fail-closed duplicate identity behavior on conflicting audience.
func TestDuplicateTraceAudienceConflictReproduction(t *testing.T) {
	for _, first := range []string{"shared", "private"} {
		t.Run(first, func(t *testing.T) {
			st := newTestStore(t, "mem:self")
			other := "private"
			if first == "private" {
				other = "shared"
			}
			options := TraceOptions{Session: "audience-witness", Turn: 1, Audience: first}
			hash, inserted, err := st.AddTrace("mem:self", "synthetic duplicate audience", options)
			if err != nil || !inserted {
				t.Fatalf("first insert = %q %v %v", hash, inserted, err)
			}
			options.Audience = other
			again, inserted, err := st.AddTrace("mem:self", "synthetic duplicate audience", options)
			if err == nil || inserted || again != "" {
				t.Fatalf("second insert = %q %v %v; want audience conflict error", again, inserted, err)
			}
			trace, found, err := st.Trace(hash)
			if err != nil || !found || trace.Audience != first {
				t.Fatalf("stored audience = %q found=%v err=%v", trace.Audience, found, err)
			}
		})
	}
}
