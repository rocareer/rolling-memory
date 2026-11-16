// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package api_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/rocareer/rolling-memory/internal/api"
	"github.com/rocareer/rolling-memory/internal/thinclient"
)

func TestLocalRecallDefaultAndExpandedBudgets(t *testing.T) {
	memory := newMemory(t)
	client, err := memory.ThinClient()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 70; i++ {
		id := fmt.Sprintf("budget-%02d", i)
		body := "quartz evidence " + id + " " + strings.Repeat("evidence ", 200)
		if err := client.Vault.SaveLocalPlaintext(id, testNow+int64(i), body, nil, thinclient.CloudIngestResponse{Layer: "episodic"}); err != nil {
			t.Fatal(err)
		}
	}
	thinclient.PurgeLegacyTables(memory.Store.DB())
	for _, tc := range []struct {
		name  string
		limit int
		want  int
	}{
		{name: "default", want: 10},
		{name: "expanded", limit: 64, want: 64},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := memory.Recall(api.RecallRequest{Cue: "quartz evidence", RawLimit: tc.limit, NoCrystallize: true})
			if err != nil || len(result.Bands) != tc.want {
				t.Fatalf("bands = %d, err = %v; want %d", len(result.Bands), err, tc.want)
			}
		})
	}
	chars := 32000
	result, err := memory.Recall(api.RecallRequest{Cue: "quartz evidence", RawLimit: 10, CapsChars: &chars, NoCrystallize: true})
	if err != nil {
		t.Fatal(err)
	}
	nodes := result.Bands[0]["nodes"].([]map[string]any)
	if got := len([]rune(nodes[0]["content"].(string))); got <= 900 {
		t.Fatalf("expanded chars delivered only %d runes", got)
	}
}
