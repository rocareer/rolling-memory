// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package store

import (
	"sync"
	"testing"
)

func TestDuplicateTraceAudienceConflictFailsClosed(t *testing.T) {
	for _, first := range []string{"shared", "private"} {
		t.Run(first, func(t *testing.T) {
			st := newTestStore(t, "mem:self")
			other := "private"
			if first == "private" {
				other = "shared"
			}
			opts := TraceOptions{Session: "audience-conflict", Turn: 1, Audience: first}
			hash, inserted, err := st.AddTrace("mem:self", "synthetic duplicate", opts)
			if err != nil || !inserted {
				t.Fatalf("first insert: inserted=%v err=%v", inserted, err)
			}
			opts.Audience = other
			if _, _, err := st.AddTrace("mem:self", "synthetic duplicate", opts); err == nil {
				t.Fatal("audience conflict accepted")
			}
			trace, found, err := st.Trace(hash)
			if err != nil || !found || trace.Audience != first {
				t.Fatalf("original trace changed: %#v found=%v err=%v", trace, found, err)
			}
		})
	}
}

func TestImportDuplicateAudienceConflictRollsBack(t *testing.T) {
	st := newTestStore(t, "mem:self")
	hash, _, err := st.AddTrace("mem:self", "existing", TraceOptions{Session: "duplicate", Audience: "shared"})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := st.ExportScope("mem:self")
	if err != nil {
		t.Fatal(err)
	}
	rows := payload["traces"].([]map[string]any)
	rows[0]["audience"] = "private"
	rows = append([]map[string]any{{"hash": "rolled-back", "scope": "mem:self", "session": "import", "turn": 1, "kind": "message", "audience": "shared", "text": "before conflict", "occurred_at": 1, "recorded_at": 1}}, rows...)
	payload["traces"] = rows
	if _, err := st.ImportScope("mem:self", payload); err == nil {
		t.Fatal("audience conflict accepted")
	}
	if _, found, err := st.Trace("rolled-back"); err != nil || found {
		t.Fatalf("preceding import row was not rolled back: found=%v err=%v", found, err)
	}
	trace, found, err := st.Trace(hash)
	if err != nil || !found || trace.Audience != "shared" {
		t.Fatalf("original trace changed: %#v found=%v err=%v", trace, found, err)
	}
}

func TestConcurrentDuplicateAudienceConflict(t *testing.T) {
	st := newTestStore(t, "mem:self")
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, audience := range []string{"shared", "private"} {
		wg.Add(1)
		go func(audience string) {
			defer wg.Done()
			<-start
			_, _, err := st.AddTrace("mem:self", "concurrent duplicate", TraceOptions{Session: "concurrent", Audience: audience})
			results <- err
		}(audience)
	}
	close(start)
	wg.Wait()
	close(results)
	failures := 0
	for err := range results {
		if err != nil {
			failures++
		}
	}
	if failures != 1 {
		t.Fatalf("audience conflicts=%d, want 1", failures)
	}
}
