// Package eval implements the bundled benchmark evaluation harness (`rmem eval`).
//
// This file holds the three cryptographically pinned benchmark prompts
// (protocol identifier `locomo-frozen-2026-10-11`):
//   - Answerer system prompt;
//   - Judge system prompt;
//   - Judge user prompt template (placeholders <question> / <gold_answer> / <generated_answer>).
//
// All three prompts are verified against their SHA-1 digests at test time
// (`TestFrozenProtocolPins`) and at runtime (`VerifyFrozenProtocol`, fail-closed)
// so benchmark runs remain strictly auditable and reproducible.
package eval

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

const (
	// FrozenAnswerSystem 答题 system 正文（官方 backboard answer 原句）。
	FrozenAnswerSystem = "You are a conversation memory assistant."

	// FrozenJudgeSystem 评分 system 行（Backboard 官方调用形态；sha1 497f7892…）。
	FrozenJudgeSystem = "You are evaluating conversational AI memory recall. Return JSON only with the format requested."

	// FrozenJudgeUserTPL 评分判词（占位符 <question>/<gold_answer>/<generated_answer>；sha1 38f9b6a1…，1694 字节）。
	FrozenJudgeUserTPL = "\nYour task is to label an answer to a question as 'CORRECT' or 'WRONG'. You will be given the following data:\n" +
		"    (1) a question (posed by one user to another user), \n" +
		"    (2) a 'gold' (ground truth) answer, \n" +
		"    (3) a generated answer\nwhich you will score as CORRECT/WRONG.\n\n" +
		"The point of the question is to ask about something one user should know about the other user based on their prior conversations.\n" +
		"The gold answer will usually be a concise and short answer that includes the referenced topic, for example:\n" +
		"Question: Do you remember what I got the last time I went to Hawaii?\n" +
		"Gold answer: A shell necklace\n" +
		"The generated answer might be much longer, but you should be generous with your grading - as long as it touches on the same topic as the gold answer, it should be counted as CORRECT. \n\n" +
		"For time related questions, the gold answer will be a specific date, month, year, etc. The generated answer might be much longer or use relative time references (like \"last Tuesday\" or \"next month\"), but you should be generous with your grading - as long as it refers to the same date or time period as the gold answer, it should be counted as CORRECT. Even if the format differs (e.g., \"May 7th\" vs \"7 May\"), consider it CORRECT if it's the same date.\n\n" +
		"Now it's time for the real question:\n" +
		"Question: <question>\n" +
		"Gold answer: <gold_answer>\n" +
		"Generated answer: <generated_answer>\n\n" +
		"First, provide a short (one sentence) explanation of your reasoning, then finish with CORRECT or WRONG. \n" +
		"Do NOT include both CORRECT and WRONG in your response, or it will break the evaluation script.\n\n" +
		"Return your response in JSON format with two keys: \"reasoning\" for your explanation and \"label\" for CORRECT or WRONG.\n"
)

// 冻结原文 sha1 钉（逐字一致；漂移即红）。
const (
	PinAnswerSystemSHA1 = "0d59162805571b5622b50446ee8da12490737ade"
	PinJudgeSystemSHA1  = "497f78929df5d4479c84ae7dd9993b4161f3a475"
	PinJudgeUserTPLSHA1 = "38f9b6a17ffd58bf58bb123de44ce40442f1a85b"

	// FrozenProtocolText 协议文本溯源标识（入报告 metrics；供读数审计）。
	FrozenProtocolText = "locomo-frozen-2026-10-11"
)

// Sha1Hex 原始字节 sha1（小写十六进制）。
func Sha1Hex(text string) string {
	sum := sha1.Sum([]byte(text))
	return hex.EncodeToString(sum[:])
}

// VerifyFrozenProtocol 冻结文本常量自检：三枚钉任一不符即 fail-closed（不是只让测试红）。
func VerifyFrozenProtocol() error {
	for _, item := range []struct{ label, text, want string }{
		{"冻结答题 system", FrozenAnswerSystem, PinAnswerSystemSHA1},
		{"冻结评分 system", FrozenJudgeSystem, PinJudgeSystemSHA1},
		{"冻结评分判词", FrozenJudgeUserTPL, PinJudgeUserTPLSHA1},
	} {
		if got := Sha1Hex(item.text); got != item.want {
			return fmt.Errorf("%s sha1 不符：期望 %s，实际 %s（冻结协议 fail-closed；改字须同步更新全部钉值并重钉）",
				item.label, item.want, got)
		}
	}
	return nil
}

// FrozenAnswerPayload 冻结答题载荷：编号行（`N. <id> · <内容>`）
// + 空行 + 原题；无记忆时占位 `1. （no memories）`。
func FrozenAnswerPayload(question string, items []Item) string {
	lines := make([]string, 0, len(items))
	for index, item := range items {
		lines = append(lines, fmt.Sprintf("%d. %s · %s", index+1, item.ID, item.Content))
	}
	if len(lines) == 0 {
		lines = []string{"1. （no memories）"}
	}
	return strings.Join(lines, "\n") + "\n\n" + question
}

// FrozenJudgeUser 三占位符逐字替换（唯一允许的加工）。
func FrozenJudgeUser(question, gold, answer string) string {
	return strings.NewReplacer(
		"<question>", question,
		"<gold_answer>", gold,
		"<generated_answer>", answer,
	).Replace(FrozenJudgeUserTPL)
}

// Item 是答题载荷里的一条记忆（id 用于读者回溯，content 为交付文本）。
type Item struct {
	ID      string `json:"id"`
	Content string `json:"content"`
}

// JudgeVerdict 判官回执：闭集 CORRECT / WRONG（+ 一句理由）。
type JudgeVerdict struct {
	Correct bool   `json:"correct"`
	Reason  string `json:"reason"`
}

// ParseFrozenJudgeVerdict 出参解析：取首个 `{` 到末个 `}`
// 的 JSON；`label` 严格闭集 {CORRECT, WRONG}（大小写敏感）；否则 JUDGE_UNPARSED（该题剔除）。
func ParseFrozenJudgeVerdict(raw string) (JudgeVerdict, error) {
	text := strings.TrimSpace(raw)
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start < 0 || end <= start {
		return JudgeVerdict{}, fmt.Errorf("JUDGE_UNPARSED")
	}
	var out struct {
		Reasoning string `json:"reasoning"`
		Label     string `json:"label"`
	}
	if err := json.Unmarshal([]byte(text[start:end+1]), &out); err != nil {
		return JudgeVerdict{}, fmt.Errorf("JUDGE_UNPARSED: %w", err)
	}
	if out.Label != "CORRECT" && out.Label != "WRONG" {
		return JudgeVerdict{}, fmt.Errorf("JUDGE_UNPARSED")
	}
	return JudgeVerdict{Correct: out.Label == "CORRECT", Reason: out.Reasoning}, nil
}
