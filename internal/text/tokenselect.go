// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package text

import (
	"sort"
	"strings"
	"sync/atomic"

	"github.com/rocareer/rolling-memory/internal/tokenzr"
)

// SelectTopInformationTokens 按香农信息质量取前 k 个词元（保序输出）。
func SelectTopInformationTokens(tokens []string, k int) []string {
	if k <= 0 || len(tokens) <= k {
		return tokens
	}
	type scorable struct {
		idx  int
		mass float64
	}
	seen := make(map[string]bool, len(tokens))
	sc := make([]scorable, 0, len(tokens))
	for i, t := range tokens {
		if strings.TrimSpace(t) == "" || seen[t] {
			continue
		}
		seen[t] = true
		sc = append(sc, scorable{idx: i, mass: TokenInformationMass(t)})
	}
	if len(sc) <= k {
		out := make([]string, 0, len(sc))
		for _, s := range sc {
			out = append(out, tokens[s.idx])
		}
		return out
	}
	sort.SliceStable(sc, func(a, b int) bool {
		if sc[a].mass != sc[b].mass {
			return sc[a].mass > sc[b].mass
		}
		return sc[a].idx < sc[b].idx
	})
	keep := sc[:k]
	sort.Slice(keep, func(a, b int) bool { return keep[a].idx < keep[b].idx })
	out := make([]string, 0, k)
	for _, s := range keep {
		out = append(out, tokens[s.idx])
	}
	return out
}

// TokenCounterFunc defines a custom tokenization/counting function.
type TokenCounterFunc func(string) int

var defaultTokenCounter atomic.Value

// CountTokens 使用 tokenz-r 词元计数器计算文本 Token 数。
func CountTokens(text string) int {
	if text == "" {
		return 0
	}
	if v := defaultTokenCounter.Load(); v != nil {
		if fn, ok := v.(TokenCounterFunc); ok && fn != nil {
			return fn(text)
		}
	}
	return tokenzr.Count(text)
}

// SetDefaultTokenCounter 注册全局默认 tokenz-r 词元计数器（宿主在 init 中注入真实 bpe/tokenz-r 实现；传 nil 无操作）。
func SetDefaultTokenCounter(fn TokenCounterFunc) {
	if fn != nil {
		defaultTokenCounter.Store(fn)
	}
}
