// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package memcore

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// 查询侧相对锚解析（时间语义档 §18.1.4 · 查询侧相对锚 + 时区声明归一）：
// 相对表达（EN/CN）→ 绝对区间；时区声明（UTC±N / IANA 名 / 北京时间·东八区）→ 解释时区。
//
// 三条硬口径（入口见 timelang.go 的 ParseTimeWindowsAt）：
//  ① 参考时刻由调用方显式传入（ref）——本文件零 time.Now（引擎侧同样不读墙钟），可测可复现；
//  ② 参考时刻取数序 = **查询内绝对锚（取最早者）> ref**；两者皆无（ref 零值且查询无绝对锚）⇒
//     相对表达一律不解析（逐字回落 ParseTimeWindows 的现役绝对口径）；
//  ③ 无相对表达命中（或 ref 零值）⇒ 输出与 ParseTimeWindows **逐字节相同**（含窗序）——负控单测钉死。
//
// 区间形态（与现役绝对窗同构，读口 SQL 零改动）：
//   - 日型（today/yesterday/N days ago…）⇒ 同 twDay：[D−7d, D+3d+86399]，Anchor = D（解释时区零点）；
//   - 区间型（last|this week、last|this weekend、month 系）⇒ [起 −3d, 末日次日零点 +3d −1]，Anchor = 起；
//   - 年系（last|this|next year、N years ago）⇒ 整年不扩（同 twYear），Anchor = 年起。
//   - 半开 [from, to) 与既有闭区间读口（`occurred_at BETWEEN Since AND Until`）的对应：Until = to − 1
//     ⇒ 端点包含性与绝对窗同口径（含首日零点、含末日最后一秒）。
//
// 表达口径（与交付侧 L3 相对日解算对齐处、以及**有意不一致处**）：
//   - last week = [ref−7, ref−1]（滚动 7 天，非日历周）——与交付侧 reldate-families v1 同读法
//     （gold 依据 q240/q238/q253）；
//     滚动口径同时避开「周起始约定（周一/周日）」这一不可闭合项（该文件 §5）。
//   - this week = [ref−6, ref]（滚动，与 last week 互补）；next week = [ref+1, ref+7]。
//   - last weekend = 最近一对（周六,周日）且其周日严格早于 ref 日（同交付侧）；this weekend = ref 日
//     落在周末 ⇒ 含当日那一对，否则 ⇒ 即将到来的一对。
//   - last/this/next month、N months ago = 日历月（交付侧同）；N years ago = 目标年整年（交付侧解成
//     单日——查询侧是召回区间，缺月日信息时按年粒度召回更保守）。
//   - 未来式：in N units（显式计数）/ N units later / N units from now /
//     next weekend / CN「N 单位后」；裸 <weekday> 与「in a unit」（度量句式歧义）仍闭集外。

const (
	relRangePadWeekMonth = 3   // 周/月/周末型区间前后扩天数（同 twMonthPad）
	relRangePadYear      = 0   // 年型区间不扩（同 twYear）
	relMaxN              = 999 // "<N> unit ago" 的 N 上限（同交付侧）
	relMaxWindows        = 4   // 与 ParseTimeWindows 同：窗数上限
	relSecsPerDay        = 86400
)

// ── 时区声明归一 ─────────────────────────────────────────────────────────────

var (
	// utc+8 / utc +8:00 / gmt-5（在小写副本上匹配）
	reZoneUTC = regexp.MustCompile(`\b(?:utc|gmt)\s*([+-])\s*(\d{1,2})(?::(\d{2}))?\b`)
	// IANA 名（Asia/Shanghai、America/Argentina/Buenos_Aires）——须 LoadLocation 成功方可采信
	reZoneIANA = regexp.MustCompile(`\b([A-Z][A-Za-z_]{1,}(?:/[A-Za-z_]{1,}){1,2})\b`)
	// 中文惯用声明
	reZoneCN = regexp.MustCompile(`(北京时间|东八区)`)
)

// resolveZone 时区声明归一：查询内声明（最左命中优先）> ref 自身 Location > UTC。
// 纯查询面：不读环境/会话时区；IANA 名加载失败 ⇒ 该声明不采信（回落次选）。
func resolveZone(q string, ref time.Time) *time.Location {
	low := strings.ToLower(q)
	best := -1
	var loc *time.Location
	consider := func(idx int, l *time.Location) {
		if l == nil || idx < 0 {
			return
		}
		if best < 0 || idx < best {
			best, loc = idx, l
		}
	}
	if m := reZoneUTC.FindStringSubmatchIndex(low); m != nil {
		sign := 1
		if low[m[2]:m[3]] == "-" {
			sign = -1
		}
		h, _ := strconv.Atoi(low[m[4]:m[5]])
		min := 0
		if m[6] >= 0 {
			min, _ = strconv.Atoi(low[m[6]:m[7]])
		}
		if h <= 14 && min < 60 {
			off := sign * (h*3600 + min*60)
			consider(m[0], time.FixedZone(fmt.Sprintf("UTC%s%02d%02d", low[m[2]:m[3]], h, min), off))
		}
	}
	if m := reZoneIANA.FindStringSubmatchIndex(q); m != nil {
		if l, err := time.LoadLocation(q[m[2]:m[3]]); err == nil {
			consider(m[0], l)
		}
	}
	if m := reZoneCN.FindStringIndex(q); m != nil {
		consider(m[0], time.FixedZone("CST", 8*3600))
	}
	if loc != nil {
		return loc
	}
	if ref.IsZero() {
		return time.UTC
	}
	return ref.Location()
}

// pickRelAnchor 多锚题的相对锚选择（**显式单点**；2026-10-19 复核拾遗：原为 relRefDay 内联隐式行为）。
//
// 口径：查询内绝对锚（Anchor > 0）中取**最早**者，返回其在解释时区下的日历日零点；无正锚 ⇒ ok=false。
//
// 理由（维持「最早」，写入判据）：
//   - 语义：相对表达在叙述序里回指**首个**提到的时间点（"…as mentioned on November 6, 2023" 类题面
//     正是以绝对日标注相对表达的落点）；取最早锚 = 问题的时间起点，解出的相对窗落在更早一侧。
//   - 召回方向保守：相对窗一律带 ±3d/前 7d 类扩边（见 §3.3 窗形态），锚偏早时窗覆盖向前延伸而非
//     误缩到「以后」；多锚题里较晚的锚另有其自身绝对窗兜底。
//   - 确定性：多锚无序依赖（扫描全表取最小，与出现序/数量无关）——同题恒同窗（同集同缓存纪律）。
//   - 取数顺序：查询内绝对锚（取最早者）优先于 ref。
//
// 注意：本函数只认 Anchor > 0 的**绝对**窗；无年日月锚（MonthOfYear/DayOfMonth 窗）无 Anchor，不参与。
func pickRelAnchor(abs []TimeWindow, loc *time.Location) (time.Time, bool) {
	best := int64(0)
	for _, w := range abs {
		if w.Until-w.Since >= 360*relSecsPerDay {
			continue
		}
		if w.Anchor > 0 && (best == 0 || w.Anchor < best) {
			best = w.Anchor
		}
	}
	if best <= 0 {
		return time.Time{}, false
	}
	// 锚的 UTC 日历日 = 查询所写之日，再落解释时区零点。
	y, m, d := time.Unix(best, 0).UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, loc), true
}

// relRefDay 参考日（解释时区下的日历日零点）：
//   - 查询内绝对锚优先（pickRelAnchor：取 Anchor 最小者）——多锚口径见其头注（显式单点）；
//   - 无内锚 ⇒ ref 在 loc 下的日历日；ref 零值 ⇒ ok=false（相对表达不解析）。
func relRefDay(abs []TimeWindow, ref time.Time, loc *time.Location) (time.Time, bool) {
	if d, ok := pickRelAnchor(abs, loc); ok {
		return d, true
	}
	if ref.IsZero() {
		return time.Time{}, false
	}
	r := ref.In(loc)
	return time.Date(r.Year(), r.Month(), r.Day(), 0, 0, 0, 0, loc), true
}

// ── 窗构造（与现役绝对窗同构）───────────────────────────────────────────────

// relDayTarget 日型窗：同 twDay 口径 [D−7d, D+3d+86399]，Anchor = D 零点。
func relDayTarget(d time.Time) TimeWindow {
	mid := d.Unix()
	return TimeWindow{Since: mid - twDayPadBefore, Until: mid + twDayPadAfter, Anchor: mid}
}

// relRangeTarget 区间型窗：半开 [start, end]（日粒度，含 end 整日）→ 闭 [start−pad, end次日零点+pad−1]。
func relRangeTarget(start, end time.Time, padDays int) TimeWindow {
	s := start.Unix()
	to := end.AddDate(0, 0, 1).Unix()
	pad := int64(padDays) * relSecsPerDay
	return TimeWindow{Since: s - pad, Until: to + pad - 1, Anchor: s}
}

// relMonthSpan d 所在月 +delta 的 [首日, 末日]（日历月；Go 归一化处理跨年）。
func relMonthSpan(d time.Time, delta int) (time.Time, time.Time) {
	first := time.Date(d.Year(), d.Month()+time.Month(delta), 1, 0, 0, 0, 0, d.Location())
	return first, first.AddDate(0, 1, -1)
}

// relYearSpan 年 y 的 [首日, 末日]。
func relYearSpan(y int, loc *time.Location) (time.Time, time.Time) {
	first := time.Date(y, 1, 1, 0, 0, 0, 0, loc)
	return first, time.Date(y, 12, 31, 0, 0, 0, 0, loc)
}

// relPrevWeekend 最近一对（周六,周日）且其周日严格早于 refDay（同交付侧；refDay = 周六/周日 ⇒ 取更早一对）。
func relPrevWeekend(refDay time.Time) (time.Time, time.Time) {
	sun := refDay.AddDate(0, 0, -int(refDay.Weekday()))
	if !sun.Before(refDay) {
		sun = sun.AddDate(0, 0, -7)
	}
	return sun.AddDate(0, 0, -1), sun
}

// relThisWeekend refDay 落在周末 ⇒ 含当日那一对；否则 ⇒ 即将到来的一对（首个周六）。
func relThisWeekend(refDay time.Time) (time.Time, time.Time) {
	switch int(refDay.Weekday()) {
	case 0: // 周日
		return refDay.AddDate(0, 0, -1), refDay
	case 6: // 周六
		return refDay, refDay.AddDate(0, 0, 1)
	default:
		sat := refDay.AddDate(0, 0, 6-int(refDay.Weekday()))
		return sat, sat.AddDate(0, 0, 1)
	}
}

// ── 规则表 ───────────────────────────────────────────────────────────────────

type relRule struct {
	re    *regexp.Regexp
	build func(m []string, refDay time.Time) []TimeWindow
}

// relNum 数量词 → N（ASCII/全角数字、中文数词、one..ten）；越界/歧义 ⇒ ok=false（不解析该表达）。
func relNum(s string) (int, bool) {
	if n, err := strconv.Atoi(toHalfWidthDigits(s)); err == nil {
		if n < 1 || n > relMaxN {
			return 0, false
		}
		return n, true
	}
	if n, ok := enNumbers[strings.ToLower(s)]; ok {
		return n, true
	}
	if n, ok := cnNumber(s); ok && n >= 1 && n <= relMaxN {
		return n, true
	}
	return 0, false
}

var enNumbers = map[string]int{
	"one": 1, "two": 2, "three": 3, "four": 4, "five": 5,
	"six": 6, "seven": 7, "eight": 8, "nine": 9, "ten": 10,
	"a": 1, "an": 1, // "a week ago" 型
	"couple": 2, // "a couple of days ago" 型（口语）
}

const enNumAlt = `(\d{1,3}|one|two|three|four|five|six|seven|eight|nine|ten|a|an|couple)`

// enNumExplicit 「in N units」专用显式数词（不含 a/an/couple）：「in a year/day/week」在度量句式
// （"How many days are in a year?"）与惯用语中高频出现，未来式读法被其淹没 ⇒ 该式只认显式计数；
// 「N units later / N units from now」无度量读法歧义，仍用 enNumAlt（a week from now ✓）。
const enNumExplicit = `(\d{1,3}|one|two|three|four|five|six|seven|eight|nine|ten)`

// relAgoUnit <N> unit ago → 日/月/年型（N = 数字或 one..ten；单位闭集）。
func relAgoUnit(m []string, refDay time.Time) []TimeWindow {
	n, ok := relNum(m[1])
	if !ok {
		return nil
	}
	switch strings.ToLower(m[2]) {
	case "day", "days":
		return []TimeWindow{relDayTarget(refDay.AddDate(0, 0, -n))}
	case "week", "weeks":
		return []TimeWindow{relDayTarget(refDay.AddDate(0, 0, -7*n))}
	case "month", "months":
		s, e := relMonthSpan(refDay, -n)
		return []TimeWindow{relRangeTarget(s, e, relRangePadWeekMonth)}
	case "year", "years":
		s, e := relYearSpan(refDay.Year()-n, refDay.Location())
		return []TimeWindow{relRangeTarget(s, e, relRangePadYear)}
	}
	return nil
}

// relAheadUnit 「in N units / N units later」：目标 = refDay + N 单位（日/月/年型；周 = 7 天）。
func relAheadUnit(m []string, refDay time.Time) []TimeWindow {
	n, ok := relNum(m[1])
	if !ok || n < 1 {
		return nil
	}
	switch strings.ToLower(m[2]) {
	case "day", "days":
		return []TimeWindow{relDayTarget(refDay.AddDate(0, 0, n))}
	case "week", "weeks":
		return []TimeWindow{relDayTarget(refDay.AddDate(0, 0, 7*n))}
	case "month", "months":
		s, e := relMonthSpan(refDay, n)
		return []TimeWindow{relRangeTarget(s, e, relRangePadWeekMonth)}
	case "year", "years":
		s, e := relYearSpan(refDay.Year()+n, refDay.Location())
		return []TimeWindow{relRangeTarget(s, e, relRangePadYear)}
	}
	return nil
}

// relDayOff 日型偏移窗（-N 天/周）
func relDayOff(days int) func(m []string, refDay time.Time) []TimeWindow {
	return func(_ []string, refDay time.Time) []TimeWindow {
		return []TimeWindow{relDayTarget(refDay.AddDate(0, 0, days))}
	}
}

// relDayAheadN 日型「N 个单位后」（单位 = 天 / 周；正向与 relDayN 对称）。
func relDayAheadN(unitDays int) func(m []string, refDay time.Time) []TimeWindow {
	return func(m []string, refDay time.Time) []TimeWindow {
		n, ok := relNum(m[1])
		if !ok || n < 1 {
			return nil
		}
		return []TimeWindow{relDayTarget(refDay.AddDate(0, 0, unitDays*n))}
	}
}

// relMonthAheadN 月型「N 个月后」（正向）。
func relMonthAheadN(m []string, refDay time.Time) []TimeWindow {
	n, ok := relNum(m[1])
	if !ok || n < 1 {
		return nil
	}
	s, e := relMonthSpan(refDay, n)
	return []TimeWindow{relRangeTarget(s, e, relRangePadWeekMonth)}
}

// relYearAheadN 年型「N 年后」（正向）。
func relYearAheadN(m []string, refDay time.Time) []TimeWindow {
	n, ok := relNum(m[1])
	if !ok || n < 1 {
		return nil
	}
	s, e := relYearSpan(refDay.Year()+n, refDay.Location())
	return []TimeWindow{relRangeTarget(s, e, relRangePadYear)}
}

// relDayN 日型「N 个单位前」（单位 = 天 / 周）
func relDayN(unitDays int) func(m []string, refDay time.Time) []TimeWindow {
	return func(m []string, refDay time.Time) []TimeWindow {
		n, ok := relNum(m[1])
		if !ok {
			return nil
		}
		return []TimeWindow{relDayTarget(refDay.AddDate(0, 0, -unitDays*n))}
	}
}

// relMonthN 月型「N 个月前」
func relMonthN(m []string, refDay time.Time) []TimeWindow {
	n, ok := relNum(m[1])
	if !ok {
		return nil
	}
	s, e := relMonthSpan(refDay, -n)
	return []TimeWindow{relRangeTarget(s, e, relRangePadWeekMonth)}
}

// relYearN 年型「N 年前」
func relYearN(m []string, refDay time.Time) []TimeWindow {
	n, ok := relNum(m[1])
	if !ok {
		return nil
	}
	s, e := relYearSpan(refDay.Year()-n, refDay.Location())
	return []TimeWindow{relRangeTarget(s, e, relRangePadYear)}
}

// relWeek 周型区间（滚动 7 天口径；避开周起始约定）：
//   - last week = [refDay−7, refDay−1]（上次的 7 天）；this week = [refDay−6, refDay]（含当日往回 7 天）；
//   - next week = [refDay+1, refDay+7]。
func relWeek(offset int) func(m []string, refDay time.Time) []TimeWindow {
	return func(_ []string, refDay time.Time) []TimeWindow {
		var s, e time.Time
		switch {
		case offset < 0:
			s = refDay.AddDate(0, 0, 7*offset)
			e = s.AddDate(0, 0, 6)
		case offset == 0:
			e = refDay
			s = e.AddDate(0, 0, -6)
		default:
			s = refDay.AddDate(0, 0, 7*(offset-1)+1) // offset=1 ⇒ ref+1（原式逐字节不变）；offset=2 ⇒ ref+8
			e = s.AddDate(0, 0, 6)
		}
		return []TimeWindow{relRangeTarget(s, e, relRangePadWeekMonth)}
	}
}

// relMonthYear 区间型日历偏移（月 / 年）
func relMonthYear(unit string, delta int) func(m []string, refDay time.Time) []TimeWindow {
	return func(_ []string, refDay time.Time) []TimeWindow {
		if unit == "month" {
			s, e := relMonthSpan(refDay, delta)
			return []TimeWindow{relRangeTarget(s, e, relRangePadWeekMonth)}
		}
		s, e := relYearSpan(refDay.Year()+delta, refDay.Location())
		return []TimeWindow{relRangeTarget(s, e, relRangePadYear)}
	}
}

// relWeekend 周末型（prev = 最近一对；否则含当日/即将到来的一对）
func relWeekend(prev bool) func(m []string, refDay time.Time) []TimeWindow {
	return func(_ []string, refDay time.Time) []TimeWindow {
		var s, e time.Time
		if prev {
			s, e = relPrevWeekend(refDay)
		} else {
			s, e = relThisWeekend(refDay)
		}
		return []TimeWindow{relRangeTarget(s, e, relRangePadWeekMonth)}
	}
}

// relBeforeAfter 「N <unit> before|after（参照 = refDay）」：day/week/weekend/month/year 差移。
// before ⇒ 目标日 = refDay ∓ N；weekend 取周六族（最近周六之前再回退，与 ago 族同规）。
func relBeforeAfter(m []string, refDay time.Time) []TimeWindow {
	n, ok := relNum(m[1])
	if !ok || n < 1 {
		return nil
	}
	return shiftUnit(refDay, strings.ToLower(m[2]), strings.ToLower(m[3]), n)
}

// relBeforeAfterOne 无 N 的「the <unit> before/after」⇒ N=1。
func relBeforeAfterOne(m []string, refDay time.Time) []TimeWindow {
	return shiftUnit(refDay, strings.ToLower(m[1]), strings.ToLower(m[2]), 1)
}

// shiftUnit 单位差移（before=−，after=+；weekend = 周六族；month/year = 日历跨度）。
func shiftUnit(refDay time.Time, unit, dir string, n int) []TimeWindow {
	sign := -1
	if dir == "after" {
		sign = 1
	}
	switch unit {
	case "day", "days":
		return []TimeWindow{relDayTarget(refDay.AddDate(0, 0, sign*n))}
	case "week", "weeks":
		return []TimeWindow{relDayTarget(refDay.AddDate(0, 0, sign*7*n))}
	case "weekend", "weekends":
		// before ⇒ 最近周六（严格早于参考日）；after ⇒ 后续首个周六（严格晚于参考日）——再按 7(N−1) 展幅。
		var s1 time.Time
		if sign < 0 {
			s1 = refDay.AddDate(0, 0, -saturdayBackStrict(refDay))
		} else {
			s1 = refDay.AddDate(0, 0, (int(time.Saturday)-int(refDay.Weekday())+7)%7)
			if !s1.After(refDay) {
				s1 = s1.AddDate(0, 0, 7)
			}
		}
		return []TimeWindow{relDayTarget(s1.AddDate(0, 0, sign*7*(n-1)))}
	case "month", "months":
		s, e := relMonthSpan(refDay, sign*n)
		return []TimeWindow{relRangeTarget(s, e, relRangePadWeekMonth)}
	case "year", "years":
		s, e := relYearSpan(refDay.Year()+sign*n, refDay.Location())
		return []TimeWindow{relRangeTarget(s, e, relRangePadYear)}
	}
	return nil
}

// relWeekdayBefore 「the <weekday> before（参照）」：严格早于 refDay 的最近该星期（命中多重则取首个匹配的语句序）。
func relWeekdayBefore(m []string, refDay time.Time) []TimeWindow {
	target, ok := weekdayIndex(strings.ToLower(m[1]))
	if !ok {
		return nil
	}
	back := (int(refDay.Weekday()) - target + 7) % 7
	if back == 0 {
		back = 7
	}
	return []TimeWindow{relDayTarget(refDay.AddDate(0, 0, -back))}
}

// relWeekday 「last|this|next <weekday>」（周一起算周）：weekStart = refDay 所在周一。
func relWeekday(m []string, refDay time.Time) []TimeWindow {
	target, ok := weekdayIndex(strings.ToLower(m[2]))
	if !ok {
		return nil
	}
	weekStart := refDay.AddDate(0, 0, -((int(refDay.Weekday()) + 6) % 7)) // 周一
	off := (target + 6) % 7                                               // 周一=0..周日=6
	switch strings.ToLower(m[1]) {
	case "last":
		return []TimeWindow{relDayTarget(weekStart.AddDate(0, 0, -7+off))}
	case "next":
		return []TimeWindow{relDayTarget(weekStart.AddDate(0, 0, 7+off))}
	default: // this
		return []TimeWindow{relDayTarget(weekStart.AddDate(0, 0, off))}
	}
}

// relWeekdayCN 「上|这|下 + 周/星期/礼拜 + 星期字」：与 EN 星期族同规（周一起算周）。
func relWeekdayCN(m []string, refDay time.Time) []TimeWindow {
	target, ok := weekdayIndexCN(m[2])
	if !ok {
		return nil
	}
	weekStart := refDay.AddDate(0, 0, -((int(refDay.Weekday()) + 6) % 7))
	off := (target + 6) % 7
	switch m[1] {
	case "上":
		return []TimeWindow{relDayTarget(weekStart.AddDate(0, 0, -7+off))}
	case "下":
		return []TimeWindow{relDayTarget(weekStart.AddDate(0, 0, 7+off))}
	default: // 这
		return []TimeWindow{relDayTarget(weekStart.AddDate(0, 0, off))}
	}
}

// relAgoWeekend2 「上上周末」= 两个周末前（其周六）。
func relAgoWeekend2(_ []string, refDay time.Time) []TimeWindow {
	s1 := refDay.AddDate(0, 0, -saturdayBackStrict(refDay))
	return []TimeWindow{relDayTarget(s1.AddDate(0, 0, -7))}
}

// weekdayIndex 星期名 → time.Weekday 值。
func weekdayIndex(w string) (int, bool) {
	switch w {
	case "monday":
		return 1, true
	case "tuesday":
		return 2, true
	case "wednesday":
		return 3, true
	case "thursday":
		return 4, true
	case "friday":
		return 5, true
	case "saturday":
		return 6, true
	case "sunday":
		return 0, true
	}
	return 0, false
}

// weekdayIndexCN 中文星期字 → 值（日/天 = 周日）。
func weekdayIndexCN(x string) (int, bool) {
	switch x {
	case "一":
		return 1, true
	case "二":
		return 2, true
	case "三":
		return 3, true
	case "四":
		return 4, true
	case "五":
		return 5, true
	case "六":
		return 6, true
	case "日", "天":
		return 0, true
	}
	return 0, false
}

// saturdayBackStrict 距 refDay 之前最近周六的天数（严格早于当日；即周六 ⇒ 7）。
func saturdayBackStrict(refDay time.Time) int {
	back := (int(refDay.Weekday()) - int(time.Saturday) + 7) % 7
	if back == 0 {
		back = 7
	}
	return back
}

// relAgoWeekend 「N 个周末前」（写侧时间服务与查询面共用）：
// 目标 = refDay 之前最近周六 S1 再回退 7(N−1) 天，日型锚 = 该周六。
// n=1 ≡ last weekend 的周六（口径对齐：last weekend 一对 = [S1, S1+1]）；n 超帽 ⇒ nil（闭集外不解析）。
func relAgoWeekend(m []string, refDay time.Time) []TimeWindow {
	numStr := m[1]
	if numStr == "两" { // 「两个周末前」口语数词
		numStr = "二"
	}
	n, ok := relNum(numStr)
	if !ok || n < 1 {
		return nil
	}
	s1 := refDay.AddDate(0, 0, -saturdayBackStrict(refDay))
	return []TimeWindow{relDayTarget(s1.AddDate(0, 0, -7*(n-1)))}
}

// relWeekendNext 「next weekend」/「下周末」：严格晚于参考日的首个（周六,周日）对（区间型；
// 周一~周五参考日与 this weekend 同解，周六/周日参考日则顺延一对）。
func relWeekendNext(_ []string, refDay time.Time) []TimeWindow {
	sat := refDay.AddDate(0, 0, (int(time.Saturday)-int(refDay.Weekday())+7)%7)
	if !sat.After(refDay) {
		sat = sat.AddDate(0, 0, 7)
	}
	return []TimeWindow{relRangeTarget(sat, sat.AddDate(0, 0, 1), relRangePadWeekMonth)}
}

// relAheadWeekendN 「N 个周末后」：首个严格晚于参考日的周六再前移 7(N−1)（与 relAgoWeekend 镜像；日型锚）。
func relAheadWeekendN(m []string, refDay time.Time) []TimeWindow {
	numStr := m[1]
	if numStr == "两" { // 「两个周末后」口语数词
		numStr = "二"
	}
	n, ok := relNum(numStr)
	if !ok || n < 1 {
		return nil
	}
	sat := refDay.AddDate(0, 0, (int(time.Saturday)-int(refDay.Weekday())+7)%7)
	if !sat.After(refDay) {
		sat = sat.AddDate(0, 0, 7)
	}
	return []TimeWindow{relDayTarget(sat.AddDate(0, 0, 7*(n-1)))}
}

// relMonthNamed 「last|this|next + 月名」：目标 = 该月（anchor = 月首；may 除外，见 monthAltBare——情态动词误命中）。
// last ⇒ 最近已过去的该月（月号 ≥ 参考月 ⇒ 上一年）；next ⇒ 最近将来的该月（月号 ≤ 参考月 ⇒ 下一年）；
// this ⇒ 参考年该月（裸月无年写侧锚点补全）。
func relMonthNamed(m []string, refDay time.Time) []TimeWindow {
	mi, ok := monthNumbers[strings.ToLower(m[2])]
	if !ok {
		return nil
	}
	y := refDay.Year()
	switch strings.ToLower(m[1]) {
	case "last":
		if mi >= int(refDay.Month()) {
			y--
		}
	case "next":
		if mi <= int(refDay.Month()) {
			y++
		}
	}
	s, e := relMonthSpan(time.Date(y, time.Month(mi), 1, 0, 0, 0, 0, refDay.Location()), 0)
	return []TimeWindow{relRangeTarget(s, e, relRangePadWeekMonth)}
}

// relMonthCNNamed 「(去年|今年|明年|前年|后年|大前年|大后年) + N月」：目标 = 对应年的该月（anchor = 月首）。
// （年前缀 + 月粒度的复合锚；先于裸年规则消费，防年份被裸年规则吃掉。）
func relMonthCNNamed(m []string, refDay time.Time) []TimeWindow {
	delta := map[string]int{"去年": -1, "今年": 0, "明年": 1, "前年": -2, "后年": 2, "大前年": -3, "大后年": 3}[m[1]]
	mi, ok := relNum(m[2])
	if !ok || mi < 1 || mi > 12 {
		return nil
	}
	s, e := relMonthSpan(time.Date(refDay.Year()+delta, time.Month(mi), 1, 0, 0, 0, 0, refDay.Location()), 0)
	return []TimeWindow{relRangeTarget(s, e, relRangePadWeekMonth)}
}

// relWeekdayCN2 「上上周X / 下下周X」：周偏移 ∓2（与 relWeekdayCN 同规，周一起算周）。
func relWeekdayCN2(m []string, refDay time.Time) []TimeWindow {
	target, ok := weekdayIndexCN(m[2])
	if !ok {
		return nil
	}
	weekStart := refDay.AddDate(0, 0, -((int(refDay.Weekday()) + 6) % 7))
	off := (target + 6) % 7
	weeks := -2
	if m[1] == "下下" {
		weeks = 2
	}
	return []TimeWindow{relDayTarget(weekStart.AddDate(0, 0, 7*weeks+off))}
}

// relRollingDays 中英「最近 N 天」滚动区间 [refDay−(N−1), refDay]（N ∈ [1, 999]）
func relRollingDays(m []string, refDay time.Time) []TimeWindow {
	n, ok := relNum(m[1])
	if !ok {
		return nil
	}
	s := refDay.AddDate(0, 0, -(n - 1))
	return []TimeWindow{relRangeTarget(s, refDay, relRangePadWeekMonth)}
}

// relNotOf 「last <unit> of …」守卫（第 1 捕获组 = 可选的 " of"）：命中 ⇒ 判为修饰/特指用法，
// 不解析（同交付侧 reldate-families v1 的唯一守卫分支：「the last week of August」／「the last night of
// the tour」非时间指涉）。Go RE2 无沿后视，故以可选捕获组建守卫。
func relNotOf(build func(m []string, refDay time.Time) []TimeWindow) func(m []string, refDay time.Time) []TimeWindow {
	return func(m []string, refDay time.Time) []TimeWindow {
		if len(m) > 1 && strings.TrimSpace(m[1]) != "" {
			return nil
		}
		return build(m, refDay)
	}
}

// relRules 规则表（有序：长式先于短式——"last weekend" 先于 "last week"、"大前天" 先于 "前天"；
// 命中跨度记账见 parseRelativeWindows，跨规则不重复消费同一段文本）。
var relRules = []relRule{
	// ── EN 日型 ──
	{regexp.MustCompile(`\bthe night before last\b`), relDayOff(-2)}, // 前晚
	{regexp.MustCompile(`\bthe week before last\b`), relWeek(-2)},    // 上上周（区间型）
	{regexp.MustCompile(`\bthe day before yesterday\b`), relDayOff(-2)},
	{regexp.MustCompile(`\bthe day after tomorrow\b`), relDayOff(2)},
	{regexp.MustCompile(`\byesterday\b`), relDayOff(-1)},
	{regexp.MustCompile(`\blast night\b(\s+of\b)?`), relNotOf(relDayOff(-1))},
	{regexp.MustCompile(`\bthis\s+(morning|afternoon|evening|night)\b`), relDayOff(0)},
	{regexp.MustCompile(`\btoday\b`), relDayOff(0)},
	{regexp.MustCompile(`\btonight\b`), relDayOff(0)},
	{regexp.MustCompile(`\btomorrow\b`), relDayOff(1)},
	// ── EN 周末（先于周）──
	{regexp.MustCompile(`\blast weekend\b(\s+of\b)?`), relNotOf(relWeekend(true))},
	{regexp.MustCompile(`\bthis weekend\b`), relWeekend(false)},
	{regexp.MustCompile(`\bnext weekend\b(\s+of\b)?`), relNotOf(relWeekendNext)},            // 下周末
	{regexp.MustCompile(`\b` + enNumAlt + `\s+(?:of\s+)?weekends?\s+ago\b`), relAgoWeekend}, // 「N weekends ago」
	// ── EN 锚定前后族（two weekends before 17 July 2023）──
	// 参考日 = 查询内绝对锚（pickRelAnchor）> ref；「N 个 X before/after」= 该参考日 ∓ N 个 X。
	{regexp.MustCompile(`\b(?:the\s+)?week after next\b`), relWeek(2)}, // 下下周（先于「week after」单数式）
	{regexp.MustCompile(`\b` + enNumAlt + `\s+(days?|weeks?|weekends?|months?|years?)\s+(before|after)\b`), relBeforeAfter},
	{regexp.MustCompile(`\b(day|week|weekend|month|year)\s+(before|after)\b`), relBeforeAfterOne}, // 无 N："the week before …" ⇒ 1
	{regexp.MustCompile(`\bthe\s+(monday|tuesday|wednesday|thursday|friday|saturday|sunday)\s+before\b`), relWeekdayBefore},
	// ── EN 星期族（last/this/next + 星期；周一起算周）──
	{regexp.MustCompile(`\b(last|this|next)\s+(monday|tuesday|wednesday|thursday|friday|saturday|sunday)\b`), relWeekday},
	// ── EN 周 / 月 / 年 ──
	{regexp.MustCompile(`\blast week\b(\s+of\b)?`), relNotOf(relWeek(-1))},
	{regexp.MustCompile(`\bthis week\b`), relWeek(0)},
	{regexp.MustCompile(`\bnext week\b`), relWeek(1)},
	{regexp.MustCompile(`\blast month\b(\s+of\b)?`), relNotOf(relMonthYear("month", -1))},
	{regexp.MustCompile(`\bthis month\b`), relMonthYear("month", 0)},
	{regexp.MustCompile(`\bnext month\b`), relMonthYear("month", 1)},
	{regexp.MustCompile(`\blast year\b(\s+of\b)?`), relNotOf(relMonthYear("year", -1))},
	{regexp.MustCompile(`\bthis year\b`), relMonthYear("year", 0)},
	{regexp.MustCompile(`\bnext year\b`), relMonthYear("year", 1)},
	// last|this|next + 月名（may 除外）：裸月无年写侧锚点补全
	{regexp.MustCompile(`\b(last|this|next)\s+` + monthAltBare + `\b`), relMonthNamed},
	// ── EN <N> unit ago ──
	{regexp.MustCompile(`\b` + enNumAlt + `\s+(?:of\s+)?(day|days|week|weeks|month|months|year|years)\s+ago\b`), relAgoUnit},
	// 未来式（规划类话语）：in <N> units / <N> units later
	{regexp.MustCompile(`\bin\s+` + enNumExplicit + `\s+(days?|weeks?|months?|years?)\b`), relAheadUnit}, // in N units（显式计数；无 N 的 in a unit 不解析）
	{regexp.MustCompile(`\b` + enNumAlt + `\s+(days?|weeks?|months?|years?)\s+later\b`), relAheadUnit},
	{regexp.MustCompile(`\b` + enNumAlt + `\s+(?:of\s+)?(days?|weeks?|months?|years?)\s+from\s+now\b`), relAheadUnit}, // a week from now
	// ── CN 日型（长式在前）──
	{regexp.MustCompile(`大前天`), relDayOff(-3)},
	{regexp.MustCompile(`大后天`), relDayOff(3)},
	{regexp.MustCompile(`(前天|前晚)`), relDayOff(-2)},
	{regexp.MustCompile(`后天`), relDayOff(2)},
	{regexp.MustCompile(`(昨晚|昨天晚上)`), relDayOff(-1)}, // 昨晚
	{regexp.MustCompile(`(昨天|昨日)`), relDayOff(-1)},
	{regexp.MustCompile(`(今天|今日|今晚|今早|今晨|今儿)`), relDayOff(0)},
	{regexp.MustCompile(`(明天|明日|明晚|明早)`), relDayOff(1)},
	// ── CN 周末（先于周）──
	{regexp.MustCompile(`(上上个周末|上上周末)`), relAgoWeekend2}, // 上上周末（先于上周末匹配）
	{regexp.MustCompile(`(上周末|上个周末)`), relWeekend(true)},
	{regexp.MustCompile(`(本周末|这周末|这个周末)`), relWeekend(false)},
	{regexp.MustCompile(`(下周末|下个周末)`), relWeekendNext},                       // 下周末
	{regexp.MustCompile(`(两|[一两三四五六七八九十]+|\d+)个?周末(以)?后`), relAheadWeekendN}, // 「N 个周末后」
	{regexp.MustCompile(`(两|[一两三四五六七八九十]+|\d+)个?周末(以)?前`), relAgoWeekend},    // 「N 个周末前」
	// ── CN 周 / 月 / 年 ──
	{regexp.MustCompile(`(上上|下下)(?:个)?(?:周|星期|礼拜)([一二三四五六日天])`), relWeekdayCN2}, // 上上周五/下下周三（先于整周规则）
	{regexp.MustCompile(`(上上周|上上星期|上上个星期)`), relWeek(-2)},                       // 上上周（先于上周）
	{regexp.MustCompile(`(下下周|下下星期|下下个星期)`), relWeek(2)},                        // 下下周（先于下周）
	{regexp.MustCompile(`(上|这|下)(?:个)?(?:周|星期|礼拜)([一二三四五六日天])`), relWeekdayCN},  // 上周五/下周三（先于整周规则）
	{regexp.MustCompile(`(上周|上星期|上个星期)`), relWeek(-1)},
	{regexp.MustCompile(`(本周|这周|这星期|这个星期)`), relWeek(0)},
	{regexp.MustCompile(`(下周|下星期|下个星期)`), relWeek(1)},
	{regexp.MustCompile(`(上上个月|上上月)`), relMonthYear("month", -2)}, // 上上个月（先于上个月）
	{regexp.MustCompile(`(下下个月|下下月)`), relMonthYear("month", 2)},  // 下下个月（先于下个月）
	{regexp.MustCompile(`(上个月|上月)`), relMonthYear("month", -1)},
	{regexp.MustCompile(`(这个月|本月|这月)`), relMonthYear("month", 0)},
	{regexp.MustCompile(`(下个月|下月)`), relMonthYear("month", 1)},
	// (去年|今年|明年|前年|后年…)+N月：月粒度复合锚（先于裸年规则）
	{regexp.MustCompile(`(去年|今年|明年|前年|后年|大前年|大后年)([0-9０-９]{1,2}|[一二三四五六七八九十]{1,3})月`), relMonthCNNamed},
	{regexp.MustCompile(`(大前年)`), relMonthYear("year", -3)}, // 大前年（先于前年）
	{regexp.MustCompile(`(前年)`), relMonthYear("year", -2)},
	{regexp.MustCompile(`(大后年)`), relMonthYear("year", 3)}, // 大后年（先于后年）
	{regexp.MustCompile(`(后年)`), relMonthYear("year", 2)},
	{regexp.MustCompile(`(去年|上年|上一年)`), relMonthYear("year", -1)},
	{regexp.MustCompile(`(今年|本年)`), relMonthYear("year", 0)},
	{regexp.MustCompile(`(明年|下一年)`), relMonthYear("year", 1)},
	// ── CN「最近 N 天」（区间）──
	{regexp.MustCompile(`(?:最近|近|过去)([0-9０-９]{1,3}|[〇零一二两三四五六七八九十]{1,3})天`), relRollingDays},
	// ── CN「N 天/周/个月/年前」与「前 N 天」──
	{regexp.MustCompile(`([0-9０-９]{1,3}|[〇零一二两三四五六七八九十]{1,3})(?:天|日)前`), relDayN(1)},
	{regexp.MustCompile(`([0-9０-９]{1,3}|[〇零一二两三四五六七八九十]{1,3})(?:周|个星期|星期)前`), relDayN(7)},
	{regexp.MustCompile(`([0-9０-９]{1,3}|[〇零一二两三四五六七八九十]{1,3})个月前`), relMonthN},
	{regexp.MustCompile(`([0-9０-９]{1,3}|[〇零一二两三四五六七八九十]{1,3})年前`), relYearN},
	// 未来式 CN：N 单位后（正向构造器——relDayN 系减号族，禁复用）
	{regexp.MustCompile(`([0-9０-９]{1,3}|[〇零一二两三四五六七八九十]{1,3})(?:天|日)后`), relDayAheadN(1)},
	{regexp.MustCompile(`([0-9０-９]{1,3}|[〇零一二两三四五六七八九十]{1,3})(?:周|个星期|星期)后`), relDayAheadN(7)},
	{regexp.MustCompile(`([0-9０-９]{1,3}|[〇零一二两三四五六七八九十]{1,3})个月后`), relMonthAheadN},
	{regexp.MustCompile(`([0-9０-９]{1,3}|[〇零一二两三四五六七八九十]{1,3})年后`), relYearAheadN},
	{regexp.MustCompile(`前([0-9０-９]{1,3}|[〇零一二两三四五六七八九十]{1,3})天`), relDayN(1)},
}

// cnNumber 中文数词 → int（〇零一二两三四五六七八九十；支持 十/十二/二十/二十三 式）。
func cnNumber(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	digits := map[rune]int{'〇': 0, '零': 0, '一': 1, '二': 2, '两': 2, '三': 3, '四': 4,
		'五': 5, '六': 6, '七': 7, '八': 8, '九': 9}
	total, cur, seen := 0, 0, false
	for _, r := range s {
		if r == '十' {
			if cur == 0 {
				cur = 1
			}
			total += cur * 10
			cur = 0
			seen = true
			continue
		}
		d, ok := digits[r]
		if !ok {
			return 0, false
		}
		cur = cur*10 + d
		seen = true
	}
	if !seen {
		return 0, false
	}
	return total + cur, true
}

// toHalfWidthDigits 全角数字 → 半角（中文输入法下的 "３天前" 与 "3天前" 同解）
func toHalfWidthDigits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '０' && r <= '９' {
			b.WriteRune('0' + (r - '０'))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// parseRelativeWindows 相对表达 → 窗（零 LLM；规则序 + 跨度记账；无命中 ⇒ nil）。
func parseRelativeWindows(q string, refDay time.Time) []TimeWindow {
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
	var out []TimeWindow
	for _, r := range relRules {
		for _, m := range r.re.FindAllStringSubmatchIndex(low, -1) {
			if overlap(m[0], m[1]) {
				continue
			}
			sub := make([]string, len(m)/2)
			for i := 0; i < len(m); i += 2 {
				if m[i] < 0 {
					continue
				}
				sub[i/2] = low[m[i]:m[i+1]]
			}
			wins := r.build(sub, refDay)
			if len(wins) == 0 {
				continue // 数值越界/歧义 ⇒ 不解析、不消费跨度
			}
			used = append(used, span{m[0], m[1]})
			out = append(out, wins...)
		}
	}
	return out
}
