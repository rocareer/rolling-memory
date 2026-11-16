// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package efficacy

import (
	"testing"
	"time"
)

// TestBudgetExhaustedGuard pins the soft-settling-budget gate: 0 means unbounded (interactive
// CLI runs stay unaffected) and the guard fires strictly after the deadline, so a budgeted
// Admin run always stops starting new turn settlements before the hard process timeout.
func TestBudgetExhaustedGuard(t *testing.T) {
	base := time.Unix(1793566401, 0)
	cases := []struct {
		name     string
		deadline int64
		now      time.Time
		want     bool
	}{
		{name: "unbounded stays inert", deadline: 0, now: base, want: false},
		{name: "unbounded stays inert far later", deadline: 0, now: base.Add(48 * time.Hour), want: false},
		{name: "before deadline", deadline: base.Unix() + 10, now: base, want: false},
		{name: "on the deadline second", deadline: base.Unix() + 10, now: base.Add(10 * time.Second), want: false},
		{name: "past the deadline", deadline: base.Unix() + 10, now: base.Add(11 * time.Second), want: true},
		{name: "negative deadline means unbounded", deadline: -1, now: base, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := budgetExhausted(tc.deadline, tc.now); got != tc.want {
				t.Fatalf("budgetExhausted(%d, %s) = %v, want %v", tc.deadline, tc.now.Format(time.RFC3339), got, tc.want)
			}
		})
	}
}
