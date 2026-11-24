// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package facade_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rocareer/rolling-memory/internal/memory/facade"
	"github.com/rocareer/rolling-memory/internal/memory/localvault"
	"github.com/rocareer/rolling-memory/internal/memory/recall"
)

// deliveredNodeContent returns the content carried by the node named by id.
func deliveredNodeContent(result *recall.Result, id string) string {
	for _, band := range result.Bands {
		nodes, _ := band["nodes"].([]map[string]any)
		for _, node := range nodes {
			if node["id"] == id {
				content, _ := node["content"].(string)
				return content
			}
		}
	}
	return ""
}

// The delivery budget is decided on the delivered set, not on every candidate:
// a batch whose source text fits the configured cap must be delivered whole.
func TestLocalRecallFitsWholeBatchWithinCharBudget(t *testing.T) {
	t.Run("single band under the cap", func(t *testing.T) {
		memory := newMemory(t)
		client, err := memory.LocalVault()
		if err != nil {
			t.Fatal(err)
		}
		body := "quartz evidence " + strings.Repeat("a", 933)
		if err := client.Vault.SaveLocalPlaintext("single", testNow, body, nil, localvault.IngestResponse{Layer: "episodic"}); err != nil {
			t.Fatal(err)
		}
		caps := len([]rune(body)) + 51
		result, err := memory.Recall(facade.RecallRequest{Cue: "quartz evidence", CapsChars: &caps, NoCrystallize: true})
		if err != nil {
			t.Fatal(err)
		}
		if got := deliveredNodeContent(result, "single"); got != body {
			t.Fatalf("single band was pre-cut: delivered %d of %d runes", len([]rune(got)), len([]rune(body)))
		}
	})

	t.Run("one long plus several short bands under the cap", func(t *testing.T) {
		memory := newMemory(t)
		client, err := memory.LocalVault()
		if err != nil {
			t.Fatal(err)
		}
		long := "quartz evidence long " + strings.Repeat("b", 1974)
		if err := client.Vault.SaveLocalPlaintext("long", testNow, long, nil, localvault.IngestResponse{Layer: "episodic"}); err != nil {
			t.Fatal(err)
		}
		total := len([]rune(long))
		for i := 0; i < 4; i++ {
			short := fmt.Sprintf("quartz evidence short-%d %s", i, strings.Repeat("c", 270))
			total += len([]rune(short))
			if err := client.Vault.SaveLocalPlaintext(fmt.Sprintf("short-%d", i), testNow+int64(i+1), short, nil, localvault.IngestResponse{Layer: "episodic"}); err != nil {
				t.Fatal(err)
			}
		}
		caps := total + 829
		result, err := memory.Recall(facade.RecallRequest{Cue: "quartz evidence", CapsChars: &caps, NoCrystallize: true})
		if err != nil {
			t.Fatal(err)
		}
		if got := deliveredNodeContent(result, "long"); got != long {
			t.Fatalf("long band was cut while the batch fits the cap: delivered %d of %d runes", len([]rune(got)), len([]rune(long)))
		}
		if len(result.Bands) != 5 {
			t.Fatalf("bands = %d, want all five", len(result.Bands))
		}
	})

	t.Run("one long turn plus short bands under the cap", func(t *testing.T) {
		memory := newMemory(t)
		client, err := memory.LocalVault()
		if err != nil {
			t.Fatal(err)
		}
		prompt := "[user] 请核对这次长回复的结论"
		answer := "[assistant] quartz evidence long-turn " + strings.Repeat("e", 2000)
		lines := `{"type":"user","message":{"role":"user","content":"请核对这次长回复的结论"}}` + "\n" +
			`{"type":"assistant","message":{"role":"assistant","content":"quartz evidence long-turn ` + strings.Repeat("e", 2000) + `"}}` + "\n"
		path := filepath.Join(t.TempDir(), "long-turn.jsonl")
		if err := os.WriteFile(path, []byte(lines), 0o600); err != nil {
			t.Fatal(err)
		}
		turnRef := "@ref:jsonl:" + path + ":0"
		for index, segment := range []struct{ id, text string }{
			{"turn_prompt", prompt},
			{"turn_answer", answer},
		} {
			packet, _ := client.Vault.PackBlindPacket(segment.id, segment.text, testNow+int64(index))
			pointer := fmt.Sprintf("%s#%d", turnRef, index)
			if err := client.Vault.SaveLocalEpisodicPointerIndexed(segment.id, testNow+int64(index), pointer, turnRef, segment.text, packet, localvault.IngestResponse{Layer: "episodic"}); err != nil {
				t.Fatalf("save %s: %v", segment.id, err)
			}
		}
		total := len([]rune(prompt)) + 1 + len([]rune(answer))
		for i := 0; i < 3; i++ {
			short := fmt.Sprintf("quartz evidence short-%d %s", i, strings.Repeat("c", 270))
			total += len([]rune(short))
			if err := client.Vault.SaveLocalPlaintext(fmt.Sprintf("turn-short-%d", i), testNow+int64(i+3), short, nil, localvault.IngestResponse{Layer: "episodic"}); err != nil {
				t.Fatal(err)
			}
		}
		caps := total + 497
		result, err := memory.Recall(facade.RecallRequest{Cue: "quartz evidence", CapsChars: &caps, NoCrystallize: true})
		if err != nil {
			t.Fatal(err)
		}
		want := prompt + "\n" + answer
		if got := deliveredNodeContent(result, "turn_answer"); got != want {
			t.Fatalf("grouped turn was cut while the batch fits the cap: delivered %d of %d runes", len([]rune(got)), len([]rune(want)))
		}
	})

	t.Run("low-score candidates outside the delivery window", func(t *testing.T) {
		memory := newMemory(t)
		client, err := memory.LocalVault()
		if err != nil {
			t.Fatal(err)
		}
		strong := "quartz nebula evidence " + strings.Repeat("quartz nebula evidence ", 120)
		if err := client.Vault.SaveLocalPlaintext("strong", testNow, strong, nil, localvault.IngestResponse{Layer: "episodic"}); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 12; i++ {
			body := fmt.Sprintf("quartz weak-%02d %s", i, strings.Repeat("filler token ", 30))
			if err := client.Vault.SaveLocalPlaintext(fmt.Sprintf("weak-%02d", i), testNow+int64(i+1), body, nil, localvault.IngestResponse{Layer: "episodic"}); err != nil {
				t.Fatal(err)
			}
		}
		result, err := memory.Recall(facade.RecallRequest{Cue: "quartz nebula evidence", RawLimit: 12, DeliveryWindow: "0-16", NoCrystallize: true})
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Bands) != 1 {
			t.Fatalf("bands = %d, want the single high-score band", len(result.Bands))
		}
		// The delivery cleaner trims the trailing separator space; the evidence
		// body itself must survive that known edge unchanged.
		want := strings.TrimSpace(strong)
		if got := deliveredNodeContent(result, "strong"); got != want {
			t.Fatalf("undelivered candidates shrank the budget: delivered %d of %d runes", len([]rune(got)), len([]rune(want)))
		}
	})
}

// A budget smaller than the selected band count cannot split into a positive
// per-band window; the delivery must still be cut instead of returning full bodies.
func TestLocalRecallTinyCharBudgetStillCutsBands(t *testing.T) {
	memory := newMemory(t)
	client, err := memory.LocalVault()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		body := fmt.Sprintf("quartz evidence tiny-%d %s", i, strings.Repeat("f", 100))
		if err := client.Vault.SaveLocalPlaintext(fmt.Sprintf("tiny-%d", i), testNow+int64(i), body, nil, localvault.IngestResponse{Layer: "episodic"}); err != nil {
			t.Fatal(err)
		}
	}
	caps := 2
	result, err := memory.Recall(facade.RecallRequest{Cue: "quartz evidence", CapsChars: &caps, NoCrystallize: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Bands) != 3 {
		t.Fatalf("bands = %d, want the selected set unchanged", len(result.Bands))
	}
	for _, band := range result.Bands {
		nodes, _ := band["nodes"].([]map[string]any)
		for _, node := range nodes {
			content, _ := node["content"].(string)
			if runes := len([]rune(content)); runes == 0 || runes > 1 {
				t.Fatalf("tiny budget left %d runes in a band", runes)
			}
		}
	}
	truncated, _ := result.Meta["truncated"].(map[string]any)
	if truncated == nil || truncated["chars"] != true {
		t.Fatalf("tiny budget left no chars truncation receipt: %#v", result.Meta["truncated"])
	}
}

// The full-batch rule does not remove the hard cap: an over-budget delivery is
// still cut by the existing per-band safety split.
func TestLocalRecallKeepsHardCharCapForOverBudgetDelivery(t *testing.T) {
	memory := newMemory(t)
	client, err := memory.LocalVault()
	if err != nil {
		t.Fatal(err)
	}
	body := "quartz evidence " + strings.Repeat("d", 4983)
	if err := client.Vault.SaveLocalPlaintext("huge", testNow, body, nil, localvault.IngestResponse{Layer: "episodic"}); err != nil {
		t.Fatal(err)
	}
	caps := 1000
	result, err := memory.Recall(facade.RecallRequest{Cue: "quartz evidence", CapsChars: &caps, NoCrystallize: true})
	if err != nil {
		t.Fatal(err)
	}
	delivered := deliveredNodeContent(result, "huge")
	if delivered == body || len([]rune(delivered)) > caps {
		t.Fatalf("over-budget delivery = %d runes (cap %d, source %d)", len([]rune(delivered)), caps, len([]rune(body)))
	}
	if !strings.HasPrefix(delivered, "quartz evidence") {
		t.Fatalf("over-budget delivery lost its source head: %q", delivered)
	}
	truncated, _ := result.Meta["truncated"].(map[string]any)
	if truncated == nil || truncated["chars"] != true {
		t.Fatalf("over-budget delivery left no chars truncation receipt: %#v", result.Meta["truncated"])
	}
}

// A page is the delivered set: a long result that offset left on the previous
// page and a trailing result outside caps.bands must not shrink the character
// window of the band that is actually shown. The page's short source is
// delivered whole, and neither skipped candidate leaks into nodes or raw hits.
func TestLocalRecallPagedSetDecidesCharBudget(t *testing.T) {
	memory := newMemory(t)
	client, err := memory.LocalVault()
	if err != nil {
		t.Fatal(err)
	}
	pagedLong := strings.TrimSpace("quartz evidence paged-long " + strings.Repeat("quartz evidence ", 300) + strings.Repeat("long tail ", 100))
	pageShort := strings.TrimSpace("quartz evidence page-short " + strings.Repeat("short tail ", 40))
	tailException := strings.TrimSpace("quartz evidence tail-exception " + strings.Repeat("tail tail ", 40))
	for i, doc := range []struct{ id, body string }{
		{"paged-long", pagedLong},
		{"page-short", pageShort},
		{"tail-exception", tailException},
	} {
		if err := client.Vault.SaveLocalPlaintext(doc.id, testNow+int64(i), doc.body, nil, localvault.IngestResponse{Layer: "episodic"}); err != nil {
			t.Fatal(err)
		}
	}
	caps := len([]rune(pageShort)) + 1
	one := 1
	result, err := memory.Recall(facade.RecallRequest{
		Cue: "quartz evidence", CapsBands: &one, CapsChars: &caps, Offset: 1, NoCrystallize: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Bands) != 1 {
		t.Fatalf("page bands = %d, want the single displayed band", len(result.Bands))
	}
	nodes, _ := result.Bands[0]["nodes"].([]map[string]any)
	if len(nodes) != 1 || nodes[0]["id"] != "page-short" {
		t.Fatalf("page nodes = %#v, want page-short only", nodes)
	}
	if got := deliveredNodeContent(result, "page-short"); got != pageShort {
		t.Fatalf("paged-away candidates shrank the budget: delivered %d of %d runes", len([]rune(got)), len([]rune(pageShort)))
	}
	if len(result.RawHits) != 1 || result.RawHits[0]["trace"] != "page-short" {
		t.Fatalf("page raw hits = %#v, want page-short only", result.RawHits)
	}
	if got, _ := result.RawHits[0]["text"].(string); got != pageShort {
		t.Fatalf("raw body differs from the delivered band: %d runes", len([]rune(got)))
	}
	for _, skipped := range []string{"paged-long", "tail-exception"} {
		if strings.Contains(fmt.Sprint(result.ToDict()), skipped) {
			t.Fatalf("skipped candidate %s leaked into the page: %#v", skipped, result.ToDict())
		}
	}
	if truncated, _ := result.Meta["truncated"].(map[string]any); truncated != nil && truncated["chars"] == true {
		t.Fatalf("a page that fits the cap reported a character cut: %#v", truncated)
	}
}
