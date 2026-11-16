// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package memcore

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// durationOffsetRE 「for (about/around) <N> months/years」或「for ... <N> months/years now」或「<N> months/years (now|ago)」相对回溯表达式（形式层，排除有界历史任期如 "for 41 years until 1930" / "was for only 20 years"）
var durationOffsetRE = regexp.MustCompile(`\b(?:for\s+(?:about\s+|around\s+)([0-9]{1,2}|one|two|three|four|five|six|seven|eight|nine|ten|eleven|twelve)\s+(months?|years?)\b|([0-9]{1,2}|one|two|three|four|five|six|seven|eight|nine|ten|eleven|twelve)\s+(months?|years?)\s+(?:now|ago)\b)`)

// 本文件 = 相对时间随行解算（形式层 · 时间解算豁免 · 零 LLM · 0 表）：锚原文 / 原文束行里出现
// **高精度**相对时间短语 ⇒ 按该行参考时刻（t_source）解出绝对时点，交付行随注「相对时点: …」。
//
// 动机（实测 · 实证基准分析）：金标常问事件日，原文用相对表达
// （seq 92 "I had a super fun weekend" 会话日 2022-11-07 = 周一 ⇒ 金标 November 5-6），交付行只带
// 会话日 ⇒ 答案模型把会话日当事件日。该解算仅在
// 高精度短语上执行；解不出 / 参考时刻未知 ⇒ 不注（不猜）。
//
// 语言面：短语表按语言可插拔（先英 / 中两个高精度子集）；机制 = 「短语 → 参考时刻 + 偏移 → 时点串」。

// ResolveRelativeTag 从一段原文按参考时刻解出相对时点串；"" = 无命中 / 不猜。
// 形态：单日 "2006-01-02"；跨日 "2006-01-02~02"（同月）或 "2006-01-02~2006-02-01"（跨月）。
func ResolveRelativeTag(text string, refTS int64) string {
	if refTS <= 0 || strings.TrimSpace(text) == "" {
		return ""
	}
	low := strings.ToLower(text)
	ref := time.Unix(refTS, 0).UTC()
	day := time.Date(ref.Year(), ref.Month(), ref.Day(), 0, 0, 0, 0, time.UTC)

	// ① 周末（先于「last <weekday>」判——"weekend" 含 weekday 词根面之外的形）
	switch {
	case containsAny(low, "this past weekend", "last weekend", "上周末") ||
		(strings.Contains(low, "weekend") && containsAny(low, "had a ", "had an ", "spent the ") && !containsAny(low, "this weekend", "next weekend", "will ", "going to ")):
		sat, sun := lastWeekend(day)
		return spanTag(sat, sun)
	case containsAny(low, "this weekend", "这周末", "本周末"):
		sat, sun := currentWeekend(day)
		return spanTag(sat, sun)
	}
	// ② 昨夜 / 前日后日（先于单词形判，防 "last night" 被 "last <weekday>" 抢）
	if containsAny(low, "last night", "昨天晚上", "昨晚") {
		return dayOrClockTag(day.AddDate(0, 0, -1), text, low)
	}
	if containsAny(low, "the day before yesterday", "前天") {
		return dayOrClockTag(day.AddDate(0, 0, -2), text, low)
	}
	if containsAny(low, "the day after tomorrow", "后天") {
		return dayOrClockTag(day.AddDate(0, 0, 2), text, low)
	}
	if containsAny(low, "yesterday", "昨天", "昨儿") {
		return dayOrClockTag(day.AddDate(0, 0, -1), text, low)
	}
	if containsAny(low, "tomorrow", "明天", "明日") {
		return dayOrClockTag(day.AddDate(0, 0, 1), text, low)
	}
	if containsAny(low, "today", "今天", "今日") {
		return dayOrClockTag(day, text, low)
	}
	// ③ last / next <weekday>（严格前 / 后的一枚）；中文「上周<X>」同规。
	// 定序纪律（硬规约②）：扫描按固定序列（禁用 map 序）——同输入逐字同解。
	for _, wd := range []struct {
		name string
		day  time.Weekday
	}{
		{"monday", time.Monday}, {"tuesday", time.Tuesday}, {"wednesday", time.Wednesday},
		{"thursday", time.Thursday}, {"friday", time.Friday}, {"saturday", time.Saturday}, {"sunday", time.Sunday},
	} {
		if strings.Contains(low, "last "+wd.name) {
			return dayOrClockTag(shiftWeekdayBefore(day, wd.day), text, low)
		}
		if strings.Contains(low, "next "+wd.name) {
			return dayOrClockTag(shiftWeekdayAfter(day, wd.day), text, low)
		}
	}
	for _, zh := range []struct {
		name string
		day  time.Weekday
	}{
		{"一", time.Monday}, {"二", time.Tuesday}, {"三", time.Wednesday}, {"四", time.Thursday},
		{"五", time.Friday}, {"六", time.Saturday}, {"日", time.Sunday}, {"天", time.Sunday},
	} {
		if strings.Contains(text, "上周"+zh.name) {
			return dayOrClockTag(shiftWeekdayBefore(day, zh.day), text, low)
		}
	}
	// ④ 持续时长 / 回溯跨度（如 "playing for about four months now" / "for about four months" / "4 years ago" / "学了快四个月了"）：相对于讲述日倒推年月
	if m := durationOffsetRE.FindStringSubmatch(low); len(m) == 5 {
		numStr, unitStr := m[1], m[2]
		if numStr == "" {
			numStr, unitStr = m[3], m[4]
		}
		if n := parseSmallCount(numStr); n > 0 {
			if strings.HasPrefix(unitStr, "month") {
				target := time.Date(day.Year(), day.Month()-time.Month(n), 1, 0, 0, 0, 0, time.UTC)
				return target.Format("2006-01")
			}
			if strings.HasPrefix(unitStr, "year") {
				target := time.Date(day.Year()-n, day.Month(), 1, 0, 0, 0, 0, time.UTC)
				return target.Format("2006-01")
			}
		}
	}
	if m := cnDurationRE.FindStringSubmatch(text); m != nil {
		if n := parseCNMonthNum(m[1]); n > 0 {
			if m[2] == "月" {
				target := time.Date(day.Year(), day.Month()-time.Month(n), 1, 0, 0, 0, 0, time.UTC)
				return target.Format("2006-01")
			}
			if m[2] == "年" {
				target := time.Date(day.Year()-n, day.Month(), 1, 0, 0, 0, 0, time.UTC)
				return target.Format("2006-01")
			}
		}
	}
	// ⑤ 亚日级相对时间（秒 / 分钟 / 小时）：以完整秒级 ref 为基准解出精确时刻串
	if subTS := parseSubDayRelOffset(text, low, ref); subTS > 0 {
		return DateTimeUTC(subTS)
	}
	return ""
}

func parseSmallCount(s string) int {
	if n, err := strconv.Atoi(s); err == nil && n > 0 {
		return n
	}
	switch s {
	case "one":
		return 1
	case "two":
		return 2
	case "three":
		return 3
	case "four":
		return 4
	case "five":
		return 5
	case "six":
		return 6
	case "seven":
		return 7
	case "eight":
		return 8
	case "nine":
		return 9
	case "ten":
		return 10
	case "eleven":
		return 11
	case "twelve":
		return 12
	}
	return 0
}

// containsAny 形式层子串命中（任一即可）
func containsAny(low string, pats ...string) bool {
	for _, p := range pats {
		if strings.Contains(low, strings.ToLower(p)) {
			return true
		}
	}
	return false
}

// lastWeekend 最近一个**已结束**的周六–周日：参考日即周末 ⇒ 该周末本身（周六 = 当天起、周日 = 当天止）；
// 周一至周五 ⇒ 刚过去的那个周末（先例：周一 11-07 ⇒ 11-05~06）。
func lastWeekend(day time.Time) (time.Time, time.Time) {
	switch day.Weekday() {
	case time.Saturday:
		return day, day.AddDate(0, 0, 1)
	case time.Sunday:
		return day.AddDate(0, 0, -1), day
	default:
		sun := day.AddDate(0, 0, -int(day.Weekday())) // 本周日（周一为昨日）
		return sun.AddDate(0, 0, -1), sun
	}
}

// currentWeekend 「这周末」：参考日所在周的周六–周日（周一至周五取本周；周末当天取所在周末）。
func currentWeekend(day time.Time) (time.Time, time.Time) {
	var sat time.Time
	switch day.Weekday() {
	case time.Saturday:
		sat = day
	case time.Sunday:
		sat = day.AddDate(0, 0, -1)
	default: // 周一至周五 ⇒ 本週六
		sat = day.AddDate(0, 0, 6-int(day.Weekday()))
	}
	return sat, sat.AddDate(0, 0, 1)
}

// shiftWeekdayBefore 最近一枚**严格早于**参考日的目标星期（同日 ⇒ 前 7 天）。
func shiftWeekdayBefore(day time.Time, wd time.Weekday) time.Time {
	delta := (int(day.Weekday()) - int(wd) + 7) % 7
	if delta == 0 {
		delta = 7
	}
	return day.AddDate(0, 0, -delta)
}

// shiftWeekdayAfter 最近一枚**严格晚于**参考日的目标星期（同日 ⇒ 后 7 天）。
func shiftWeekdayAfter(day time.Time, wd time.Weekday) time.Time {
	delta := (int(wd) - int(day.Weekday()) + 7) % 7
	if delta == 0 {
		delta = 7
	}
	return day.AddDate(0, 0, delta)
}

// dayTag 单日 "2006-01-02"。
func dayTag(t time.Time) string { return t.Format("2006-01-02") }

// dayOrClockTag 单日相对时间叠加同句显式时分秒（如 "yesterday at 14:30:25" ⇒ "2026-10-28 14:30:25"；无时钟则日级）。
func dayOrClockTag(t time.Time, text, low string) string {
	if ts := attachClockToDay(t.Unix(), text, low); ts > 0 {
		return DateTimeUTC(ts)
	}
	return dayTag(t)
}

// spanTag 区间：同月 "2006-01-02~02"；跨月/跨年显全日期。
func spanTag(a, b time.Time) string {
	if a.Year() == b.Year() && a.Month() == b.Month() {
		return a.Format("2006-01-02") + "~" + b.Format("02")
	}
	return a.Format("2006-01-02") + "~" + b.Format("2006-01-02")
}
