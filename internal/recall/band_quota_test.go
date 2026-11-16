// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package recall_test

import (
	"fmt"
	"testing"

	"github.com/rocareer/rolling-memory/internal/api"
	"github.com/rocareer/rolling-memory/internal/recall"
)

// writeLiteralSubjectFact writes one structured memory assertion whose S is a literal (value
// slot) instead of a node; only the misc band can deliver this shape.
func writeLiteralSubjectFact(
	t *testing.T, memory *api.Memory, content, subjectLiteral, relation string, object any,
) string {
	t.Helper()
	receipt, err := memory.Remember(api.RememberRequest{
		Content:    strPtr(content),
		AnchorText: strPtr(content),
		Assertion: map[string]any{
			"s": map[string]any{"literal": subjectLiteral},
			"x": relation,
			"o": object,
		},
		Author:   "user",
		AutoSync: boolPtr(false),
	})
	if err != nil {
		t.Fatalf("remember %q: %v", content, err)
	}
	written, _ := receipt["written"].([]any)
	if len(written) != 1 {
		t.Fatalf("written = %#v", receipt["written"])
	}
	id, _ := written[0].(map[string]any)["id"].(string)
	if id == "" {
		t.Fatalf("written id missing: %#v", written[0])
	}
	return id
}

func bandTitles(result *recall.Result) []string {
	titles := []string{}
	for _, band := range result.Bands {
		titles = append(titles, band["title"].(string))
	}
	return titles
}

func hasNode(result *recall.Result, nodeID string) bool {
	for _, band := range result.Bands {
		for _, node := range band["nodes"].([]map[string]any) {
			if node["id"] == nodeID {
				return true
			}
		}
	}
	return false
}

func TestMiscBandKeepsLiteralSubjectCandidate(t *testing.T) {
	memory := newMemory(t)
	// The relation node is the only lexical hook; S is a value slot, so the
	// candidate cannot join any subject group.
	targetID := writeLiteralSubjectFact(t, memory, "源质丁主体携带标记戊连接桥梁端",
		"源质丁主体", "携带标记戊", map[string]any{"label": "桥梁端"})

	before := recallCue(t, memory, "标记戊", false)
	if !hasNode(before, targetID) {
		t.Fatalf("baseline: literal-S candidate not delivered: %v", bandTitles(before))
	}

	// Eight second-hop assertions attach the bridge object to eight subjects;
	// their subject bands would otherwise consume the whole band quota.
	for index := 0; index < 8; index++ {
		content := fmt.Sprintf("主体%d关联桥梁端", index)
		writeFact(t, memory, content, fmt.Sprintf("主体%d", index), "关联",
			map[string]any{"label": "桥梁端"}, nil)
	}

	after := recallCue(t, memory, "标记戊", false)
	if !hasNode(after, targetID) {
		t.Fatalf("literal-S candidate starved by subject bands: %v", bandTitles(after))
	}
	if len(after.Bands) != 8 {
		t.Fatalf("bands = %d, want 8 (caps): %v", len(after.Bands), bandTitles(after))
	}
	subjectBands := 0
	miscNodes := []map[string]any{}
	for _, band := range after.Bands {
		if band["kind"] == "subject" {
			subjectBands++
		}
		if band["kind"] == "misc" {
			miscNodes = band["nodes"].([]map[string]any)
		}
	}
	if subjectBands != 7 || len(miscNodes) == 0 {
		t.Fatalf("subject bands = %d, misc nodes = %d; want a reserved misc slot",
			subjectBands, len(miscNodes))
	}
	if miscNodes[0]["id"] != targetID {
		t.Fatalf("misc head = %v, want the literal-S candidate %s", miscNodes[0]["id"], targetID)
	}
}
