package eval

import (
	"strings"
	"testing"
)

// TestFrozenProtocolPins 冻结协议三钉（改字须同步更新全部钉值并重钉；漂移即红）。
func TestFrozenProtocolPins(t *testing.T) {
	if err := VerifyFrozenProtocol(); err != nil {
		t.Fatalf("frozen protocol drift: %v", err)
	}
	if got := Sha1Hex(FrozenAnswerSystem); got != PinAnswerSystemSHA1 {
		t.Fatalf("answer system pin = %s", got)
	}
	if got := len(FrozenJudgeUserTPL); got != 1694 {
		t.Fatalf("judge user template length = %d, want 1694", got)
	}
	if !strings.HasPrefix(FrozenJudgeUserTPL, "\n") || !strings.HasSuffix(FrozenJudgeUserTPL, "\n") {
		t.Fatalf("judge user template must keep its leading/trailing newlines")
	}
}

// TestFrozenAnswerPayloadGolden 答题载荷形态：编号行 + 空行 + 原题；空档占位。
func TestFrozenAnswerPayloadGolden(t *testing.T) {
	empty := FrozenAnswerPayload("Who am I?", nil)
	if empty != "1. （no memories）\n\nWho am I?" {
		t.Fatalf("empty payload = %q", empty)
	}
	full := FrozenAnswerPayload("Where did Caroline move from?", []Item{
		{ID: "abc123", Content: "I moved from Sweden."},
		{ID: "def456", Content: "She likes pottery."},
	})
	want := "1. abc123 · I moved from Sweden.\n2. def456 · She likes pottery.\n\nWhere did Caroline move from?"
	if full != want {
		t.Fatalf("payload = %q", full)
	}
}

// TestFrozenJudgeUserPlaceholders 判词三占位符逐字替换（唯一允许的加工）。
func TestFrozenJudgeUserPlaceholders(t *testing.T) {
	got := FrozenJudgeUser("Q?", "Gold", "Generated")
	for _, want := range []string{"Question: Q?", "Gold answer: Gold", "Generated answer: Generated"} {
		if !strings.Contains(got, want) {
			t.Fatalf("judge user missing %q", want)
		}
	}
	if strings.Contains(got, "<question>") || strings.Contains(got, "<gold_answer>") || strings.Contains(got, "<generated_answer>") {
		t.Fatalf("judge user still carries placeholders: %q", got)
	}
}

// TestParseFrozenJudgeVerdict 闭集解析：CORRECT/WRONG 通过，其余 = JUDGE_UNPARSED。
func TestParseFrozenJudgeVerdict(t *testing.T) {
	verdict, err := ParseFrozenJudgeVerdict("Here you go:\n{\"reasoning\": \"same topic\", \"label\": \"CORRECT\"}\n")
	if err != nil || !verdict.Correct || verdict.Reason != "same topic" {
		t.Fatalf("correct verdict = %+v err=%v", verdict, err)
	}
	verdict, err = ParseFrozenJudgeVerdict("{\"reasoning\": \"different\", \"label\": \"WRONG\"}")
	if err != nil || verdict.Correct {
		t.Fatalf("wrong verdict = %+v err=%v", verdict, err)
	}
	for _, raw := range []string{"", "no json here", "{\"label\": \"correct\"}", "{\"label\": \"PARTIAL\"}", "{\"label\": \"CORRECT\""} {
		if _, err := ParseFrozenJudgeVerdict(raw); err == nil {
			t.Fatalf("raw %q must be JUDGE_UNPARSED", raw)
		} else if !strings.Contains(err.Error(), "JUDGE_UNPARSED") {
			t.Fatalf("raw %q error = %v", raw, err)
		}
	}
	// 取首个 `{` 到末个 `}` 的片段 ⇒ 前后杂音不影响解析。
	verdict, err = ParseFrozenJudgeVerdict("noise {\"label\": \"CORRECT\"} trailing")
	if err != nil || !verdict.Correct {
		t.Fatalf("noise-wrapped verdict = %+v err=%v", verdict, err)
	}
}
