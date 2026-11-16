// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package memcore

import (
	"github.com/rocareer/rolling-memory/internal/text"
)

// SelectTopInformationTokens 按香农信息质量取前 k 个词元（保序输出，delegates to internal/text）。
func SelectTopInformationTokens(tokens []string, k int) []string {
	return text.SelectTopInformationTokens(tokens, k)
}
