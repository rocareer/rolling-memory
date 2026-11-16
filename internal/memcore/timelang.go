// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package memcore

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// ParseTimeWindows 机械解析查询中的时间锚（EN 月名/日期 + 中文 年/月/日；零 LLM；UTC）。
// 输出三形态（TimeWindow）：绝对窗（带年份）/ 无年日月锚 / 无年月锚。
// 未命中任何日期表达 ⇒ 返回空（调用侧 = 时间腿不出）；**相对表达（"last week"）不在本函数解析**
// ——相对锚需参考时刻，走 ParseTimeWindowsAt（见 timelang_rel.go 头注）。
//
// 口径（与时间腿排序联动）：
//   - 日锚窗 = [锚日 −7d, 锚日 +3d]（前扩承接 "the week before X" 式提问，后扩承接提及漂移）；
//   - 月锚窗 = [月首 −3d, 次月首 +3d]；年锚窗 = 整年；
//   - "between A and B"（≥2 日锚、≥1 个带年）⇒ 合并为一个 hull 窗（缺年锚用带年锚补全）；
//   - 锚 = 解析出的日期（腿内排序用）；裸 may（英文情态动词）不入裸月名规则。

// monthNumbers 月名 → 月号（全名与三字母缩写）
var monthNumbers = map[string]int{
	"january": 1, "jan": 1,
	"february": 2, "feb": 2,
	"march": 3, "mar": 3,
	"april": 4, "apr": 4,
	"may":  5,
	"june": 6, "jun": 6,
	"july": 7, "jul": 7,
	"august": 8, "aug": 8,
	"september": 9, "sept": 9, "sep": 9,
	"october": 10, "oct": 10,
	"november": 11, "nov": 11,
	"december": 12, "dec": 12,
}

const (
	// 数字语境月名（含 may：须后随数字才成窗）
	monthAltAll = "(january|february|march|april|may|june|july|august|september|october|november|december|jan|feb|mar|apr|jun|jul|aug|sept|sep|oct|nov|dec)"
	// 裸月名（不含 may——英文情态动词高频误命中）
	monthAltBare = "(january|february|march|april|june|july|august|september|october|november|december|jan|feb|mar|apr|jun|jul|aug|sept|sep|oct|nov|dec)"
)

var (
	reMDY   = regexp.MustCompile(`\b` + monthAltAll + `\s+(\d{1,2})(?:st|nd|rd|th)?(?:\s*,\s*|\s+)(\d{4})\b`)
	reDMY   = regexp.MustCompile(`\b(\d{1,2})(?:st|nd|rd|th)?\s+` + monthAltAll + `,?\s+(\d{4})\b`)
	reDMYof = regexp.MustCompile(`\b(\d{1,2})(?:st|nd|rd|th)?\s+of\s+` + monthAltAll + `,?\s+(\d{4})\b`) // "12th of May 2023"
	// 裸月窗前置守卫："last|this|next + 月名" 由相对月名规则出更具体的年+月窗 ⇒ 裸月窗（跨年泛匹配）不再并存
	rePreMonthNamed = regexp.MustCompile(`\b(last|this|next)\s+$`)
	// CN 年前缀守卫："去年3月" 由相对复合规则消费（防 {MonthOfYear:3} 泛窗并存）
	rePreCNYear = regexp.MustCompile(`(去年|今年|明年|前年|后年|大前年|大后年)$`)
	reMY        = regexp.MustCompile(`\b` + monthAltAll + `,?\s+(\d{4})\b`)
	reMD        = regexp.MustCompile(`\b` + monthAltAll + `\s+(\d{1,2})(?:st|nd|rd|th)?\b`)
	reM         = regexp.MustCompile(`\b` + monthAltBare + `\b`)
	reY         = regexp.MustCompile(`\b(19\d{2}|20\d{2})\b`)
	reCNMDY     = regexp.MustCompile(`(\d{4})年(\d{1,2})月(\d{1,2})日`)
	reCNMY      = regexp.MustCompile(`(\d{4})年(\d{1,2})月`)
	reCNY       = regexp.MustCompile(`(\d{4})年`)
	reCNMD      = regexp.MustCompile(`(\d{1,2})月(\d{1,2})日`)
	reCNM       = regexp.MustCompile(`(\d{1,2})月`)

	// ⓪ ISO 日期[+时刻]（code 域主形；时刻可缺——`2026-05-25` / `2026-05-25 14:30` / `2026-05-25T14:30:05`）。
	reISO = regexp.MustCompile(`\b(\d{4})-(\d{2})-(\d{2})(?:[ t](\d{1,2}):(\d{2})(?::(\d{2}))?)?`)

	// 时刻后缀（紧随日期锚后 ≤12 字符窗内）。
	reClockHM    = regexp.MustCompile(`^[\s,]*?(?:at\s+|@)?(\d{1,2}):(\d{2})(?::(\d{2}))?\s*(am|pm)?`)
	reClockHopm  = regexp.MustCompile(`^[\s,]*?(?:at\s+|@)?(\d{1,2})\s*(am|pm)`)
	reClockCNHM  = regexp.MustCompile(`^[\s,]*?(\d{1,2})[:：](\d{2})(?::(\d{2}))?`)
	reClockCNWrd = regexp.MustCompile(`^[\s,]*?(凌晨|早上|上午|中午|下午|晚上|傍晚)?(\d{1,2})[点时](?:(\d{1,2})分?(?:(\d{1,2})秒)?|(半))?`)

	reBetween = regexp.MustCompile(`\bbetween\b`)
	// "in may"（裸 may 被排除在月名外，但 "in may" 是明确月份语境）
	reInMay = regexp.MustCompile(`\bin may\b`)
)

const (
	twDayPadBefore = int64(7 * 86400) // 日锚前扩
	twDayPadAfter  = int64(3*86400) + 86399
	twMonthPad     = int64(3 * 86400)
)

func utcMid(y, m, d int) int64 {
	return time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC).Unix()
}

// bareYearTokenIsStandalone reports whether s[start:end) is a standalone four-digit year token
// rather than a fragment of an identifier-like token (order numbers, SKUs, revision ids).
// Purely formal (Unicode character classes + structural token shape) and language-agnostic:
//   - Rejects glued letters/digits/underscores/hashes ("SKU2026A", "id_2026_01", "#2026-114");
//   - Rejects code-prefixed hyphenated IDs like "Q-2026-31" or "A1-2026" where the prefix before '-'
//     is a single letter or contains digits;
//   - Preserves natural year ranges ("2022-2023") and hyphenated temporal modifiers ("mid-2023", "late-2022").
func bareYearTokenIsStandalone(s string, start, end int) bool {
	if start > 0 {
		prev, prevSize := utf8.DecodeLastRuneInString(s[:start])
		latinLetter := unicode.IsLetter(prev) && !unicode.Is(unicode.Han, prev)
		if latinLetter || unicode.IsDigit(prev) || prev == '_' || prev == '#' ||
			prev == ':' || prev == '/' || prev == '.' || prev == '×' || prev == '=' || prev == '+' || prev == '@' || prev == '`' {
			return false
		}
		if prev == '-' {
			// Inspect the token immediately preceding '-'
			segEnd := start - prevSize
			segStart := segEnd
			hasDigit := false
			hasLetter := false
			for segStart > 0 {
				r, sz := utf8.DecodeLastRuneInString(s[:segStart])
				if unicode.IsLetter(r) && !unicode.Is(unicode.Han, r) {
					hasLetter = true
					segStart -= sz
				} else if unicode.IsDigit(r) {
					hasDigit = true
					segStart -= sz
				} else {
					break
				}
			}
			seg := s[segStart:segEnd]
			segBits := StringShannonBits(seg)
			// Allow "2022-2023" (preceding segment is itself a 4-digit year 19xx/20xx, 4*3.32 = 13.28 bits)
			isPrevYear := hasDigit && !hasLetter && segBits >= 13.0 && segBits <= 13.5 && (strings.HasPrefix(seg, "19") || strings.HasPrefix(seg, "20"))
			// Allow multi-letter modifier prefix like "mid-2023", "late-2022", "pre-2020" (pure letters, >= 4.0 Shannon bits)
			isModifierPrefix := !hasDigit && hasLetter && segBits >= 0.50*AtomicConceptEntropyBits
			if !isPrevYear && !isModifierPrefix {
				return false
			}
		}
	}
	if end < len(s) {
		next, size := utf8.DecodeRuneInString(s[end:])
		if unicode.IsLetter(next) && !unicode.Is(unicode.Han, next) {
			return false
		}
		if next == '_' || next == '#' || next == ':' || next == '/' || next == '×' || next == '%' || next == '`' {
			return false
		}
		// Reject line-number / table-column alignment where a 4-digit number is followed by a tab or >=2 consecutive spaces ("1976     def ...")
		if next == '\t' || (next == ' ' && end+1 < len(s) && (s[end+1] == ' ' || s[end+1] == '\t')) {
			return false
		}
		if next == '-' && end+size < len(s) {
			rem := s[end+size:]
			dLen := 0
			for dLen < len(rem) && rem[dLen] >= '0' && rem[dLen] <= '9' {
				dLen++
			}
			if dLen > 0 {
				// Allow "2022-2023" (4-digit year after '-')
				nextDigits := rem[:dLen]
				isNextYear := dLen == 4 && (strings.HasPrefix(nextDigits, "19") || strings.HasPrefix(nextDigits, "20"))
				if !isNextYear {
					return false
				}
			}
		}
	}
	return true
}

// twDay 日锚窗（前扩 7d / 后扩 3d+）
func twDay(y, m, d int) TimeWindow {
	mid := utcMid(y, m, d)
	return TimeWindow{Since: mid - twDayPadBefore, Until: mid + twDayPadAfter, Anchor: mid}
}

// twClockSpan 时刻窗区间（口径 §2：**粒度 = 表达粒度**，不猜不扩）：分表达 ⇒ [T, T+59s]；秒表达 ⇒ [T, T]。
func twClockSpan(y, m, d, h, mi, s int, hasSec bool) (int64, int64) {
	t := time.Date(y, time.Month(m), d, h, mi, s, 0, time.UTC).Unix()
	if hasSec {
		return t, t
	}
	return t, t + 59
}

// twClock 时刻窗。
func twClock(y, m, d, h, mi, s int, hasSec bool) TimeWindow {
	from, until := twClockSpan(y, m, d, h, mi, s, hasSec)
	return TimeWindow{Since: from, Until: until, Anchor: from}
}

// clockAfter 日期锚后的时刻后缀识别（紧邻窗 ≤clockAfterWindow 字符；命中 ⇒ 返回绝对跨度 + 分量）。
// 口径：只在完整日期锚之后绑定（裸时钟不解析）；无 am/pm 修饰 ⇒ 24 小时制字面；无效分量拒收 ⇒ 该日期退日窗。
func clockAfter(low string, end int) (a, b, h, mi, s int, hasSec, ok bool) {
	if end < 0 || end >= len(low) {
		return 0, 0, 0, 0, 0, false, false
	}
	winEnd := end + clockAfterWindow
	if winEnd > len(low) {
		winEnd = len(low)
	}
	win := low[end:winEnd]
	bad := func() (int, int, int, int, int, bool, bool) { return 0, 0, 0, 0, 0, false, false }
	if m := reClockHM.FindStringSubmatchIndex(win); m != nil {
		h, _ = strconv.Atoi(win[m[2]:m[3]])
		mi, _ = strconv.Atoi(win[m[4]:m[5]])
		if m[6] >= 0 {
			s, _ = strconv.Atoi(win[m[6]:m[7]])
			hasSec = true
		}
		if m[8] >= 0 { // am/pm 修饰
			ap := win[m[8]:m[9]]
			if h < 1 || h > 12 {
				return bad()
			}
			if ap == "pm" && h != 12 {
				h += 12
			}
			if ap == "am" && h == 12 {
				h = 0
			}
		}
		if h > 23 || mi > 59 || s > 59 {
			return bad()
		}
		return end + m[0], end + m[1], h, mi, s, hasSec, true
	}
	if m := reClockHopm.FindStringSubmatchIndex(win); m != nil {
		h, _ = strconv.Atoi(win[m[2]:m[3]])
		if h < 1 || h > 12 {
			return bad()
		}
		if win[m[4]:m[5]] == "pm" && h != 12 {
			h += 12
		}
		if win[m[4]:m[5]] == "am" && h == 12 {
			h = 0
		}
		return end + m[0], end + m[1], h, 0, 0, false, true
	}
	if m := reClockCNHM.FindStringSubmatchIndex(win); m != nil {
		h, _ = strconv.Atoi(win[m[2]:m[3]])
		mi, _ = strconv.Atoi(win[m[4]:m[5]])
		if m[6] >= 0 {
			s, _ = strconv.Atoi(win[m[6]:m[7]])
			hasSec = true
		}
		if h > 23 || mi > 59 || s > 59 {
			return bad()
		}
		return end + m[0], end + m[1], h, mi, s, hasSec, true
	}
	if m := reClockCNWrd.FindStringSubmatchIndex(win); m != nil {
		mod := ""
		if m[2] >= 0 {
			mod = win[m[2]:m[3]]
		}
		h, _ = strconv.Atoi(win[m[4]:m[5]])
		half := m[10] >= 0 // 「半」= 30 分
		if m[6] >= 0 {
			mi, _ = strconv.Atoi(win[m[6]:m[7]])
		} else if half {
			mi = 30
		}
		if m[8] >= 0 {
			s, _ = strconv.Atoi(win[m[8]:m[9]])
			hasSec = true
		}
		switch mod {
		case "下午", "晚上", "傍晚":
			if h < 12 {
				h += 12
			}
		case "凌晨", "早上", "上午":
			if h == 12 {
				h = 0
			}
		case "中午":
			if h < 12 {
				h += 12
			}
		}
		if h > 23 || mi > 59 || s > 59 {
			return bad()
		}
		return end + m[0], end + m[1], h, mi, s, hasSec, true
	}
	return bad()
}

// clockAfterWindow 时刻后缀的紧邻窗（**字节**；覆盖 CN 最长钟形「下午3点30分5秒」=21B + 分隔；锚定 = 紧邻 + 可选 at/@
// ⇒ 跨句时钟不绑，防误绑不变量由锚定而非窗宽保证）。
const clockAfterWindow = 24

// twMonth 月锚窗（首尾各扩 3d）
func twMonth(y, m int) TimeWindow {
	start := time.Date(y, time.Month(m), 1, 0, 0, 0, 0, time.UTC).Unix()
	end := time.Date(y, time.Month(m)+1, 1, 0, 0, 0, 0, time.UTC).Unix()
	return TimeWindow{Since: start - twMonthPad, Until: end + twMonthPad, Anchor: start}
}

// twYear 年锚窗（整年）
func twYear(y int) TimeWindow {
	start := time.Date(y, 1, 1, 0, 0, 0, 0, time.UTC).Unix()
	end := time.Date(y+1, 1, 1, 0, 0, 0, 0, time.UTC).Unix()
	return TimeWindow{Since: start, Until: end, Anchor: start}
}

// ParseTimeWindows 见文件头说明。
func ParseTimeWindows(q string) []TimeWindow {
	if cached, ok := lookupCachedTimeWindows(q); ok {
		return cached
	}
	low := strings.ToLower(q)
	type span struct{ a, b int }
	var used []span
	overlap := func(a, b int) bool {
		for _, s := range used {
			if a < s.b && s.a < b {
				return true
			}
		}
		return false
	}
	mark := func(a, b int) { used = append(used, span{a, b}) }

	type dayAnchor struct {
		y, m, d int
		h, mi   int
		s       int
		clk     bool
		hasSec  bool
	}
	var dayAnchors []dayAnchor
	var others []TimeWindow

	// ⓪ ISO 日期[+时刻]（code 域主形；先于他规运行，防其年份被裸年规则吃掉）。
	for _, m := range reISO.FindAllStringSubmatchIndex(low, -1) {
		if overlap(m[0], m[1]) {
			continue
		}
		y, _ := strconv.Atoi(low[m[2]:m[3]])
		mo, _ := strconv.Atoi(low[m[4]:m[5]])
		d, _ := strconv.Atoi(low[m[6]:m[7]])
		if y < 1900 || mo < 1 || mo > 12 || d < 1 || d > 31 {
			continue
		}
		mark(m[0], m[1])
		da := dayAnchor{y: y, m: mo, d: d}
		switch {
		case m[8] >= 0: // 同一匹配内带时刻（`…T14:30[:05]` / `… 14:30[:05]`）
			h, _ := strconv.Atoi(low[m[8]:m[9]])
			mi, _ := strconv.Atoi(low[m[10]:m[11]])
			if h <= 23 && mi <= 59 {
				da.clk, da.h, da.mi = true, h, mi
				if m[12] >= 0 {
					if s, _ := strconv.Atoi(low[m[12]:m[13]]); s <= 59 {
						da.s, da.hasSec = s, true
					}
				}
			}
		default: // ISO 日 + 后随时钟（`2026-05-25 at 14:30` 等）
			if a, b, h, mn, s, hasSec, cok := clockAfter(low, m[1]); cok {
				mark(a, b)
				da.clk, da.h, da.mi, da.s, da.hasSec = true, h, mn, s, hasSec
			}
		}
		dayAnchors = append(dayAnchors, da)
	}

	// ① EN 完整日期（Month D, YYYY / D Month, YYYY）
	for _, m := range reMDY.FindAllStringSubmatchIndex(low, -1) {
		if overlap(m[0], m[1]) {
			continue
		}
		mi := monthNumbers[low[m[2]:m[3]]]
		d, _ := strconv.Atoi(low[m[4]:m[5]])
		y, _ := strconv.Atoi(low[m[6]:m[7]])
		if d < 1 || d > 31 {
			continue
		}
		mark(m[0], m[1])
		da := dayAnchor{y: y, m: mi, d: d}
		if a, b, h, mn, s, hasSec, cok := clockAfter(low, m[1]); cok {
			mark(a, b)
			da.clk, da.h, da.mi, da.s, da.hasSec = true, h, mn, s, hasSec
		}
		dayAnchors = append(dayAnchors, da)
	}
	for _, m := range reDMY.FindAllStringSubmatchIndex(low, -1) {
		if overlap(m[0], m[1]) {
			continue
		}
		d, _ := strconv.Atoi(low[m[2]:m[3]])
		mi := monthNumbers[low[m[4]:m[5]]]
		y, _ := strconv.Atoi(low[m[6]:m[7]])
		if d < 1 || d > 31 {
			continue
		}
		mark(m[0], m[1])
		da := dayAnchor{y: y, m: mi, d: d}
		if a, b, h, mn, s, hasSec, cok := clockAfter(low, m[1]); cok {
			mark(a, b)
			da.clk, da.h, da.mi, da.s, da.hasSec = true, h, mn, s, hasSec
		}
		dayAnchors = append(dayAnchors, da)
	}

	// "17th of July 2023" 变体（先于 reMY，防 "july 2023" 被月+年规则吃成月窗后被跨度记账跳过）
	for _, m := range reDMYof.FindAllStringSubmatchIndex(low, -1) {
		if overlap(m[0], m[1]) {
			continue
		}
		d, _ := strconv.Atoi(low[m[2]:m[3]])
		mi := monthNumbers[low[m[4]:m[5]]]
		y, _ := strconv.Atoi(low[m[6]:m[7]])
		if d < 1 || d > 31 {
			continue
		}
		mark(m[0], m[1])
		da := dayAnchor{y: y, m: mi, d: d}
		if a, b, h, mn, s, hasSec, cok := clockAfter(low, m[1]); cok {
			mark(a, b)
			da.clk, da.h, da.mi, da.s, da.hasSec = true, h, mn, s, hasSec
		}
		dayAnchors = append(dayAnchors, da)
	}

	// ② EN 月+年
	for _, m := range reMY.FindAllStringSubmatchIndex(low, -1) {
		if overlap(m[0], m[1]) {
			continue
		}
		mi := monthNumbers[low[m[2]:m[3]]]
		y, _ := strconv.Atoi(low[m[4]:m[5]])
		mark(m[0], m[1])
		others = append(others, twMonth(y, mi))
	}

	// ③ 中文（先于裸年，防 "2023年8月" 的 2023 被裸年规则吃掉）
	for _, m := range reCNMDY.FindAllStringSubmatchIndex(low, -1) {
		if overlap(m[0], m[1]) {
			continue
		}
		y, _ := strconv.Atoi(low[m[2]:m[3]])
		mi, _ := strconv.Atoi(low[m[4]:m[5]])
		d, _ := strconv.Atoi(low[m[6]:m[7]])
		if d < 1 || d > 31 || mi < 1 || mi > 12 {
			continue
		}
		mark(m[0], m[1])
		da := dayAnchor{y: y, m: mi, d: d}
		if a, b, h, mn, s, hasSec, cok := clockAfter(low, m[1]); cok {
			mark(a, b)
			da.clk, da.h, da.mi, da.s, da.hasSec = true, h, mn, s, hasSec
		}
		dayAnchors = append(dayAnchors, da)
	}
	for _, m := range reCNMY.FindAllStringSubmatchIndex(low, -1) {
		if overlap(m[0], m[1]) {
			continue
		}
		y, _ := strconv.Atoi(low[m[2]:m[3]])
		mi, _ := strconv.Atoi(low[m[4]:m[5]])
		if mi < 1 || mi > 12 {
			continue
		}
		mark(m[0], m[1])
		others = append(others, twMonth(y, mi))
	}
	for _, m := range reCNY.FindAllStringSubmatchIndex(low, -1) {
		if overlap(m[0], m[1]) {
			continue
		}
		y, _ := strconv.Atoi(low[m[2]:m[3]])
		mark(m[0], m[1])
		others = append(others, twYear(y))
	}
	for _, m := range reCNMD.FindAllStringSubmatchIndex(low, -1) {
		if overlap(m[0], m[1]) {
			continue
		}
		mi, _ := strconv.Atoi(low[m[2]:m[3]])
		d, _ := strconv.Atoi(low[m[4]:m[5]])
		if d < 1 || d > 31 || mi < 1 || mi > 12 {
			continue
		}
		mark(m[0], m[1])
		dayAnchors = append(dayAnchors, dayAnchor{y: 0, m: mi, d: d})
	}
	for _, m := range reCNM.FindAllStringSubmatchIndex(low, -1) {
		if overlap(m[0], m[1]) {
			continue
		}
		if rePreCNYear.MatchString(low[:m[0]]) {
			continue // "去年3月"：相对复合规则出年+月窗，{MonthOfYear} 泛窗不再并存
		}
		mi, _ := strconv.Atoi(low[m[2]:m[3]])
		if mi < 1 || mi > 12 {
			continue
		}
		mark(m[0], m[1])
		others = append(others, TimeWindow{MonthOfYear: mi})
	}

	// ④ EN 月+日（无年）
	for _, m := range reMD.FindAllStringSubmatchIndex(low, -1) {
		if overlap(m[0], m[1]) {
			continue
		}
		mi := monthNumbers[low[m[2]:m[3]]]
		d, _ := strconv.Atoi(low[m[4]:m[5]])
		if d < 1 || d > 31 {
			continue
		}
		mark(m[0], m[1])
		dayAnchors = append(dayAnchors, dayAnchor{y: 0, m: mi, d: d})
	}

	// ⑤ EN 裸月（无年）——裸 may 除外，改由 "in may" 语境承接
	for _, m := range reM.FindAllStringSubmatchIndex(low, -1) {
		if overlap(m[0], m[1]) {
			continue
		}
		if rePreMonthNamed.MatchString(low[:m[0]]) {
			continue // "last|this|next march"：相对月名规则出年+月窗，裸月泛窗不再并存
		}
		mi := monthNumbers[low[m[2]:m[3]]]
		mark(m[0], m[1])
		others = append(others, TimeWindow{MonthOfYear: mi})
	}
	if m := reInMay.FindStringIndex(low); m != nil && !overlap(m[0], m[1]) {
		mark(m[0], m[1])
		others = append(others, TimeWindow{MonthOfYear: 5})
	}

	// ⑥ 裸年（1900–2099，防误吃长度数字）
	for _, m := range reY.FindAllStringSubmatchIndex(low, -1) {
		if overlap(m[0], m[1]) {
			continue
		}
		// 编号内嵌守卫（形式层 · 零词表）：编号/序列号里的四位数（如 "Q-2026-31"、"SKU2026A"、
		// "id_2026_01"）不是时间表达。只在年份前后均为分隔边界时才按裸年收。
		if !bareYearTokenIsStandalone(low, m[0], m[1]) {
			continue
		}
		y, _ := strconv.Atoi(low[m[2]:m[3]])
		mark(m[0], m[1])
		others = append(others, twYear(y))
	}

	// ⑦ 组装：日锚窗（between 且 ≥2 锚且 ≥1 带年 ⇒ hull；**全带钟 ⇒ 精确 hull**——口径 §3）；无年日锚 ⇒ 无年日月锚；
	// 带钟且带年 ⇒ 时刻窗（粒度 = 表达粒度，无日级外扩）。
	var wins []TimeWindow
	if reBetween.MatchString(low) && len(dayAnchors) >= 2 {
		baseYear := 0
		for _, a := range dayAnchors {
			if a.y > 0 {
				baseYear = a.y
				break
			}
		}
		if baseYear > 0 {
			allClocked := true
			for _, a := range dayAnchors {
				if !a.clk || a.y == 0 {
					allClocked = false
					break
				}
			}
			minT, maxT, anchorT := int64(0), int64(0), int64(0)
			for i, a := range dayAnchors {
				y := a.y
				if y == 0 {
					y = baseYear
				}
				if allClocked {
					from, until := twClockSpan(y, a.m, a.d, a.h, a.mi, a.s, a.hasSec)
					if i == 0 || from < minT {
						minT = from
					}
					if i == 0 || until > maxT {
						maxT = until
					}
				} else {
					mid := utcMid(y, a.m, a.d)
					if i == 0 || mid < minT {
						minT = mid
						anchorT = mid
					}
					if i == 0 || mid > maxT {
						maxT = mid
					}
				}
			}
			if allClocked {
				wins = append(wins, TimeWindow{Since: minT, Until: maxT, Anchor: minT})
			} else {
				wins = append(wins, TimeWindow{Since: minT - twDayPadBefore, Until: maxT + twDayPadAfter, Anchor: anchorT})
			}
			dayAnchors = nil
		}
	}
	for _, a := range dayAnchors {
		switch {
		case a.clk && a.y > 0:
			wins = append(wins, twClock(a.y, a.m, a.d, a.h, a.mi, a.s, a.hasSec))
		case a.y > 0:
			wins = append(wins, twDay(a.y, a.m, a.d))
		default:
			wins = append(wins, TimeWindow{MonthOfYear: a.m, DayOfMonth: a.d})
		}
	}
	wins = append(wins, others...)
	res := dedupTimeWindows(wins)
	storeCachedTimeWindows(q, res)
	return res
}

// dedupTimeWindows 去重（键 = 窗四元组）+ 上限 relMaxWindows 窗（现役口径，绝对口径专用；相对窗保证占位
// 的截尾走 capTimeWindowsAt——见其头注）。
func dedupTimeWindows(wins []TimeWindow) []TimeWindow {
	out := make([]TimeWindow, 0, len(wins))
	seen := map[string]bool{}
	for _, w := range wins {
		key := timeWindowKey(w)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, w)
		if len(out) >= relMaxWindows {
			break
		}
	}
	return out
}

// timeWindowKey 窗去重键（键 = 窗四元组；**不含 Anchor**——同区间的绝对/相对条目视同同一窗）。
func timeWindowKey(w TimeWindow) string {
	return fmt.Sprintf("%d|%d|%d|%d", w.Since, w.Until, w.MonthOfYear, w.DayOfMonth)
}

// capTimeWindowsAt 绝对 + 相对窗的**超帽截尾**（4 窗帽下相对窗静默截断的处置）。
//
// 判据（取「相对窗命中时优先占位」，不改帽、不改既有窗序）：
//  1. 未超帽（去重后 ≤ relMaxWindows）⇒ 输出与 dedupTimeWindows(绝对…+相对…) 逐字节相同（含窗序
//     [绝对…, 相对…]）——现行口径零扰动（既有单测/读数依赖该窗序，如「内锚优先」例的 [日锚, 相对区间]）。
//  2. 超帽 ⇒ **相对窗先占位**（按规则序取前 relMaxWindows 条），绝对窗按原序补足余位；输出窗序仍为
//     [绝对…, 相对…]。即：帽满时从**绝对窗尾部**截，相对窗不再被静默丢弃。
//
// 取舍与理由（注释即判据）：
//   - 不扩帽：帽 = 窗查询的扫描预算（SQL OR 子句数，单次窗查询），
//     扩帽改成本面而非本残余面。
//   - 不重排（相对窗不提前）：既有窗序被单测与只读读数（含 #243/#284「内锚优先」口径、时间腿保底与
//     池摘要对照）依赖；仅把「原本被丢的一端」从相对窗换成绝对窗尾部，扰动最小。
//   - 截绝对窗尾部而非头部：相对表达的参考日由 pickRelAnchor 取**最早**绝对锚（timelang_rel.go），
//     相对窗承载的是问题真正问的时段；多锚题的绝对窗尾部（较晚的锚）是上下文而非问点，先舍。
//   - 交叉同窗（相对条目与绝对条目四元组相同）仍以绝对窗条目为准（去重在占位前完成，同现役语义）：
//     极端边角下该条目若又落进绝对尾部截断区，则该区间丢失——与现役（相对窗无条件被丢）相比不劣化。
func capTimeWindowsAt(abs, rel []TimeWindow) []TimeWindow {
	norm := func(ws []TimeWindow) []TimeWindow { // 组内去重（保序）
		seen := map[string]bool{}
		out := make([]TimeWindow, 0, len(ws))
		for _, w := range ws {
			k := timeWindowKey(w)
			if seen[k] {
				continue
			}
			seen[k] = true
			out = append(out, w)
		}
		return out
	}
	absN, relN := norm(abs), norm(rel)
	// 跨组去重：同窗四元组以**绝对窗条目**为准（相对条目丢弃）——与现役 dedupTimeWindows 同语义。
	{
		seen := map[string]bool{}
		for _, w := range absN {
			seen[timeWindowKey(w)] = true
		}
		kept := make([]TimeWindow, 0, len(relN))
		for _, w := range relN {
			k := timeWindowKey(w)
			if seen[k] {
				continue
			}
			seen[k] = true
			kept = append(kept, w)
		}
		relN = kept
	}
	if len(absN)+len(relN) <= relMaxWindows {
		return append(absN, relN...) // 未超帽：现役逐字口径（含窗序）
	}
	if len(relN) > relMaxWindows { // 相对窗自身超帽（多表达题）：保留规则序前 4 条
		relN = relN[:relMaxWindows]
	}
	keepAbs := relMaxWindows - len(relN)
	if len(absN) > keepAbs {
		absN = absN[:keepAbs]
	}
	return append(absN, relN...)
}

// normalizeTradTime 繁体时间词归一（字符级替换；只动时间用词，不动其余文本）。
func normalizeTradTime(q string) string {
	if q == "" {
		return q
	}
	return strings.NewReplacer("兩", "两", "週", "周", "個", "个", "後", "后", "號", "号", "禮拜", "礼拜").Replace(q)
}

// ParseTimeWindowsAt 绝对锚 + 相对锚（时区声明归一）：查询时间锚解析的**带参考时刻**入口。
//
//	ref = 调用方显式传入的参考时刻（asof/请求时刻；由上层传入）——本函数零 time.Now，可测可复现。
//
// 取数序与零变化保证（timelang_rel.go 头注三条硬口径）：
//
//	① 参考时刻 = 查询内绝对锚（取最早者，见 pickRelAnchor）> ref；皆无 ⇒ 相对表达不解析；
//	② 无相对表达命中 ⇒ 返回与 ParseTimeWindows **逐字节相同**的窗（含窗序）——普通名词/数字不受影响；
//	③ 相对命中且窗数超帽 ⇒ 相对窗保证占位（capTimeWindowsAt：从绝对窗尾部截，相对窗不再被静默丢弃）；
//	④ 时间腿接线见读链 retrievalTimeRef。
func ParseTimeWindowsAt(q string, ref time.Time) []TimeWindow {
	q = normalizeTradTime(q) // 繁体时间词归一（兩/週/個/後/號 → 简；2026-10-27 对抗批 · 语言无关面）
	abs := ParseTimeWindows(q)
	loc := resolveZone(q, ref)
	refDay, ok := relRefDay(abs, ref, loc)
	if !ok {
		return abs
	}
	rel := parseRelativeWindows(q, refDay)
	if len(rel) == 0 {
		return abs
	}
	return capTimeWindowsAt(abs, rel)
}
