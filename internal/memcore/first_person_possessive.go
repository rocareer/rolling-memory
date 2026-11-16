// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package memcore

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/rocareer/rolling-memory/internal/tokenzr"
)

// 本文件 = 槽位标签的「未消解第一人称领属限定语」去语境化（纯形式层 · 0 语义推断）：
//   - 与 PMAS M11（`hasUnresolvedFirstPerson`）共用同一封闭形式类，避免判据与规范化漂移；
//   - 只剥限定语、不推断领属人（身份判定权属 LLM，见设计稿文档）。
//   - 限定语后必须仍有实义字符（字母/表意字），否则原样返回（防 "our" / "我们的" 这类纯限定语空壳）。

// 架构契约「代词 / 指代 / 关系 / 词面闸口 100% 归 LLM，代码不处理语义与词句」：
// 第一人称领属限定语的静态词表（原 {"my ","our ","我的","我们的"}）已废止——属代码语义猜测。
// 以下三枚 API 保留形状供调用侧零改动，但恒定不命中：未消解的第一人称由 LLM 解算，判不出落 gap。

// HasFirstPersonPossessivePrefix 已废止：恒定 false（第一人称领属判定归 LLM）。
func HasFirstPersonPossessivePrefix(string) bool { return false }

// StripFirstPersonPossessivePrefix 已废止：原样返回，不再剥离任何限定语。
func StripFirstPersonPossessivePrefix(label string) (string, bool) { return label, false }

// IsFirstPersonSingularPronoun 已废止：恒定 false（人称判定归 LLM）。
func IsFirstPersonSingularPronoun(string) bool { return false }

// IsPronounSurface reports whether s is an unresolved standalone pronoun / reflexive fragment
// using pure tokenz-r 128k BPE merge-tree topology and priors (zero stopword tables, zero Shannon kill switches).
// Multi-word phrases, role placeholders, uppercase acronyms (US/IT/WHO/CAN/OR), single-letter uppercase initials (J),
// and open-class content words / proper names (Will/May/Rose/Mark/Bill/Joy/Max/art/cat/gym/app/tv) return false.
//
// 覆盖边界：指示词长指代 NP（这件事 / 该公司 / 该结论 / 这份方案）**不**在本闸
// 判定范围内——「该」先验 10.97 与普通实词同区间，而「这/那」又与「人/事/的」同区间，任何无词表判据都会两头错
// （漏 该公司 / 误杀 人口·事故·个人）；引入 CJK 指示词+量词闭集则违反语种无关与代码零语义契约。
// 该面的正确兜底 = 写侧抽取约束（严禁裸代词作 S/X/O）+ 读链对无度节点的资格过滤，不在本算子继续加猜。
func IsPronounSurface(alias string) bool {
	trimmed := strings.TrimSpace(CanonicalizeApostrophe(alias))
	if trimmed == "" || IsSelfDesignator(trimmed) || IsRolePlaceholder(trimmed) || strings.Contains(trimmed, " ") {
		return false
	}
	if len(trimmed) == 1 && trimmed[0] >= 'A' && trimmed[0] <= 'Z' && trimmed != "I" {
		return false
	}
	allUpper := true
	hasUpper := false
	for _, r := range trimmed {
		if !unicode.IsLetter(r) || !unicode.IsUpper(r) || IsContinuousSyllabicOrIdeographic(r) {
			allUpper = false
			break
		}
		hasUpper = true
	}
	if allUpper && hasUpper && len(trimmed) >= 2 {
		return false
	}
	lo := strings.ToLower(trimmed)
	if IsPureCJK(lo) {
		rFirst, wFirst := utf8.DecodeRuneInString(lo)
		if wFirst <= 0 {
			return false
		}
		if wFirst == len(lo) {
			return tokenzr.TokenPriorBits(lo) < 9.96
		}
		_, wSecond := utf8.DecodeRuneInString(lo[wFirst:])
		if wFirst+wSecond == len(lo) {
			if tokenzr.Count(lo) != 1 {
				return false
			}
			rLast, _ := utf8.DecodeLastRuneInString(lo)
			pFirst := tokenzr.TokenPriorBits(string(rFirst))
			pLast := tokenzr.TokenPriorBits(string(rLast))
			pWhole := tokenzr.TokenPriorBits(lo)
			if pFirst >= 10.0 && pLast < 8.0 {
				return false
			}
			if pWhole < 10.80 {
				return true
			}
			if pLast >= 9.65 && pWhole >= 10.80 {
				return false
			}
			return pWhole < 11.40 && pLast < 10.60
		}
		if tokenzr.TokenPriorBits(string(rFirst)) >= 9.40 || tokenzr.Count(lo) > 2 {
			return false
		}
		for _, sub := range tokenzr.SplitSubwords(lo[wFirst:]) {
			if tokenzr.TokenPriorBits(sub) >= 10.60 {
				return false
			}
		}
		return true
	}
	r0, _ := utf8.DecodeRuneInString(trimmed)
	val := tokenzr.BPEInflectionValence(lo)
	for _, reflSfx := range []string{"selves", "self"} {
		if lo != reflSfx && strings.HasSuffix(lo, reflSfx) {
			stem := strings.TrimSuffix(lo, reflSfx)
			// 组合律：代词基 + 反身语素 ⇒ 反身复合代词（herself / ourselves / yourself / itself）。
			// 原判据要求 BPEInflectionValence(stem)==0，实测把 her/our/your 这三个高屈折代词基全部漏判；
			// 改为「词干本身是代词表面」后覆盖完整，且不会引入任何词表（her/our/your 由 BPE 拓扑判定）。
			if stem != "" && IsPronounSurface(stem) {
				return true
			}
		}
	}
	pr := tokenzr.TokenPriorBits(lo)
	spID, minC, cnt4k := tokenzr.BPEPrefixStats(lo)
	if unicode.IsUpper(r0) {
		if tokenzr.IsBPEProperNounPrior(lo) || pr >= 10.50 || (val >= 3 && pr >= 9.95) || (val >= 2 && pr >= 10.00) || (val >= 2 && pr >= 9.64 && cnt4k >= 1) || (val == 1 && pr < 9.60) || (minC > 0 && minC < 1000) {
			return false
		}
		if pr >= 10.10 && pr <= 10.20 && val == 0 {
			return false
		}
		return true
	}
	if tokenzr.Count(lo) != 1 || tokenzr.IsBPEProperNounPrior(lo) {
		return false
	}
	if (val >= 3 && pr >= 9.95) || (val >= 2 && pr >= 10.00) || (val >= 1 && pr >= 10.20) {
		return false
	}
	if tokenzr.IsBPEStandaloneGluePrior(lo) {
		return true
	}
	if spID > 1948 {
		return false
	}
	if pr >= 9.64 {
		if cnt4k >= 1 || (minC > 0 && minC < 5000) || (minC >= 6000 && minC <= 10000 && pr >= 10.00) || (val >= 1 && minC == 0 && pr >= 9.70) {
			return false
		}
	}
	return true
}

// IsCandidateSpeakerEntity checks whether a node label is a valid cross-session first-person
// speaker real name using pure formal rules (0 wordlists: excludes role placeholders, anonymous
// stranger codes, digits, code punctuation /_().:[]{}<>="'`\ , intra-word CamelCase
// identifiers, and >3-word phrases).
func IsCandidateSpeakerEntity(lb string) bool {
	lb = strings.TrimSpace(lb)
	if lb == "" || IsRolePlaceholder(lb) || IsAnonymousStrangerLabel(lb) || StringShannonBits(lb) == 0 {
		return false
	}
	if strings.ContainsAny(lb, "/_().:[]{}<>=\"'`\\0123456789") {
		return false
	}
	words := strings.Fields(lb)
	if len(words) == 0 || CountTokens(lb) > TokenTier4 {
		return false
	}
	for _, w := range words {
		rs := []rune(w)
		for i := 1; i < len(rs); i++ {
			if unicode.IsUpper(rs[i]) && unicode.IsLower(rs[i-1]) {
				return false
			}
		}
	}
	return true
}
