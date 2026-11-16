// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

// Binary and partial assertion delivery verification
// （标签/渲染 "S X" / "X O"；零散带理由区分「主体不适用」与「S 为值」）。
package recall_test

import (
	"strings"
	"testing"

	"github.com/rocareer/rolling-memory/internal/api"
)

func TestBinaryFormsDeliverWithoutPlaceholders(t *testing.T) {
	memory := newMemory(t)
	cases := []struct {
		content   string
		anchor    string
		assertion map[string]any
	}{
		{
			content: "老张笑了，没笑出声。",
			anchor:  "老张笑了",
			assertion: map[string]any{
				"s": map[string]any{"label": "老张"}, "x": "笑",
			},
		},
		{
			content: "地震了，比赛也取消了。",
			anchor:  "地震了",
			assertion: map[string]any{
				"x": "发生", "o": map[string]any{"label": "地震"}, "subject_absent": true,
			},
		},
	}
	for _, item := range cases {
		receipt, err := memory.Remember(api.RememberRequest{
			Content: strPtr(item.content), AnchorText: strPtr(item.anchor),
			Assertion: item.assertion, Author: "user", AutoSync: boolPtr(false),
		})
		if err != nil {
			t.Fatalf("remember %q: %v", item.content, err)
		}
		if written, _ := receipt["written"].([]any); len(written) != 1 {
			t.Fatalf("written = %#v for %q", receipt["written"], item.content)
		}
	}

	result, err := memory.Recall(api.RecallRequest{Cue: "老张 地震"})
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	rendered := result.Render(8000)
	if !strings.Contains(rendered, "老张 笑") || !strings.Contains(rendered, "发生 地震") {
		t.Fatalf("binary rows missing from render:\n%s", rendered)
	}
	for _, artifact := range []string{"—笑→", "—发生→", "老张 —", "→ 地震"} {
		if strings.Contains(rendered, artifact) {
			t.Fatalf("render keeps an empty-slot artifact %q:\n%s", artifact, rendered)
		}
	}

	misc := bandFor(t, result.Bands, "零散带")
	nodes, _ := misc["nodes"].([]map[string]any)
	if len(nodes) == 0 {
		t.Fatalf("misc band empty: %#v", misc)
	}
	if reason, _ := nodes[0]["reason"].(string); reason != "未成链：主体不适用（二元-XO）" {
		t.Fatalf("misc reason = %q, want the binary-XO label", reason)
	}
}
