// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package mcp

import "testing"

func TestRecallWindowDoesNotSetExcerptBudget(t *testing.T) {
	for _, window := range []string{"", "auto", "0-16", "0-32", "0-64"} {
		args := map[string]any{"cue": "handover"}
		want := "auto"
		if window != "" {
			args["delivery_window"] = window
			want = window
		}
		req, err := recallRequest(args)
		if err != nil || req.DeliveryWindow != want || req.CapsChars != nil || req.CapsBands != nil || req.CapsNodes != nil {
			t.Errorf("window %q: request = %+v, err = %v", window, req, err)
		}
	}
	if _, err := recallRequest(map[string]any{"delivery_window": "0-2800"}); err == nil {
		t.Fatal("accepted an invalid delivery window")
	}
}
