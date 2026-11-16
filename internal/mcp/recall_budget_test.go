// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package mcp

import "testing"

func TestRecallRawBudget(t *testing.T) {
	for _, tc := range []struct {
		name    string
		caps    map[string]any
		want    int
		invalid bool
	}{
		{name: "default", want: 10},
		{name: "zero", caps: map[string]any{"raw": 0}, want: 10},
		{name: "one", caps: map[string]any{"raw": 1}, want: 1},
		{name: "maximum", caps: map[string]any{"raw": 64}, want: 64},
		{name: "negative", caps: map[string]any{"raw": -1}, invalid: true},
		{name: "over maximum", caps: map[string]any{"raw": 65}, invalid: true},
		{name: "bands over maximum", caps: map[string]any{"bands": 65}, invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := recallRequest(map[string]any{"caps": tc.caps})
			if tc.invalid {
				if err == nil {
					t.Fatal("expected invalid budget error")
				}
				return
			}
			if err != nil || req.RawLimit != tc.want {
				t.Fatalf("raw limit = %d, err = %v; want %d", req.RawLimit, err, tc.want)
			}
		})
	}
}
