package eval

import (
	"fmt"

	"github.com/rocareer/rolling-memory/internal/config"
)

// 协议对 = 冻结答题 × 冻结判官：调用点直接用冻结常量文本，
// 每次调用前自检三枚 sha1 钉（fail-closed）。

// Answerer 是答题角色（冻结答题协议）。
type Answerer interface {
	Answer(question string, items []Item) (string, error)
	Describe() map[string]any
}

// Judge 是评分角色（冻结判官协议；闭集 CORRECT/WRONG）。
type Judge interface {
	Judge(question, gold, answer string) (JudgeVerdict, error)
	Describe() map[string]any
}

// noneAnswerer 是零模型答题臂：不产生答案（只量检索面）。
type noneAnswerer struct{}

func (noneAnswerer) Answer(string, []Item) (string, error) { return "", nil }
func (noneAnswerer) Describe() map[string]any {
	return map[string]any{"protocol": "none", "model": nil}
}

// FrozenAnswerer 用冻结答题 system + 冻结载荷调模型。
type FrozenAnswerer struct {
	client *ChatClient
}

// NewFrozenAnswerer 构造冻结答题器（client 为空 ⇒ 报错，调用方负责只在 llm 臂使用）。
func NewFrozenAnswerer(_ *config.Config, client *ChatClient) (*FrozenAnswerer, error) {
	if client == nil {
		return nil, fmt.Errorf("冻结答题器需要模型通道")
	}
	if err := VerifyFrozenProtocol(); err != nil {
		return nil, err
	}
	return &FrozenAnswerer{client: client}, nil
}

// Answer 按冻结载荷答题（每次调用前重核冻结钉，字节保真直达，不做静默截断）。
// 空正文与传输类瞬时故障按退避重试；最终失败上抛，由调用方记 unscored。
func (a *FrozenAnswerer) Answer(question string, items []Item) (string, error) {
	if err := VerifyFrozenProtocol(); err != nil {
		return "", err
	}
	return a.client.CompleteWithRetry(FrozenAnswerSystem, FrozenAnswerPayload(question, items))
}

// Describe 报告协议面（入汇总 metrics 供读数审计）。
func (a *FrozenAnswerer) Describe() map[string]any {
	return map[string]any{
		"protocol":        FrozenProtocolText,
		"protocol_pair":   "local-qa-judge",
		"role":            "answer",
		"model":           a.client.Model(),
		"endpoint_host":   hostOf(a.client.URL()),
		"system_sha1":     PinAnswerSystemSHA1,
		"temperature":     a.client.temp,
		"response_format": nil,
	}
}

// FrozenJudge 用冻结评分 system + 冻结判词调模型，闭集解析。
type FrozenJudge struct {
	client *ChatClient
}

// NewFrozenJudge 构造冻结判官。
func NewFrozenJudge(_ *config.Config, client *ChatClient) (*FrozenJudge, error) {
	if client == nil {
		return nil, fmt.Errorf("冻结判官需要模型通道")
	}
	if err := VerifyFrozenProtocol(); err != nil {
		return nil, err
	}
	return &FrozenJudge{client: client}, nil
}

// Judge 按冻结判词评分（每次调用前重核冻结钉；瞬时故障退避重试后仍失败则上抛）。
func (j *FrozenJudge) Judge(question, gold, answer string) (JudgeVerdict, error) {
	if err := VerifyFrozenProtocol(); err != nil {
		return JudgeVerdict{}, err
	}
	raw, err := j.client.CompleteWithRetry(FrozenJudgeSystem, FrozenJudgeUser(question, gold, answer))
	if err != nil {
		return JudgeVerdict{}, err
	}
	return ParseFrozenJudgeVerdict(raw)
}

// Describe 报告协议面（入汇总 metrics）。
func (j *FrozenJudge) Describe() map[string]any {
	return map[string]any{
		"protocol":      FrozenProtocolText,
		"protocol_pair": "local-qa-judge",
		"role":          "judge",
		"model":         j.client.Model(),
		"endpoint_host": hostOf(j.client.URL()),
		"system_sha1":   PinJudgeSystemSHA1,
		"user_tpl_sha1": PinJudgeUserTPLSHA1,
		"temperature":   j.client.temp,
	}
}

// NewAnswerer 按配置构造答题角色（none | llm）。
func NewAnswerer(cfg *config.Config, kind string, client *ChatClient) (Answerer, error) {
	switch kind {
	case "", "none":
		return noneAnswerer{}, nil
	case "llm":
		return NewFrozenAnswerer(cfg, client)
	}
	return nil, fmt.Errorf("answerer 必须是 none|llm，得到 %q", kind)
}

// NewJudge 按配置构造评分角色（off | llm）。
func NewJudge(cfg *config.Config, kind string, client *ChatClient) (Judge, error) {
	switch kind {
	case "", "off":
		return nil, nil
	case "llm":
		return NewFrozenJudge(cfg, client)
	}
	return nil, fmt.Errorf("judge 必须是 off|llm，得到 %q", kind)
}

// Itemize 把检索上下文切成冻结答题载荷的记忆条目（逐行归条，行号即读者回溯位）。
func Itemize(context string, limit int) []Item {
	lines := splitLines(context)
	items := make([]Item, 0, len(lines))
	for _, line := range lines {
		if line == "" {
			continue
		}
		items = append(items, Item{
			ID:      fmt.Sprintf("m%d", len(items)+1),
			Content: line,
		})
		if limit > 0 && len(items) >= limit {
			break
		}
	}
	return items
}

func splitLines(text string) []string {
	lines := []string{}
	current := []rune{}
	for _, char := range text {
		if char == '\n' {
			lines = append(lines, string(current))
			current = current[:0]
			continue
		}
		current = append(current, char)
	}
	lines = append(lines, string(current))
	return lines
}

// hostOf 见 ollama.go（共用一处 host 摘要，不另立等价工具）。
