// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package memcore

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// 本文件 = 写侧统一时间解算引擎：
//   1. 优先解析在场 LLM（写侧编码器）整形的统一时间微语言（DSL）槽 `time_expr`（100% 语言无关、免除模型心算 Unix 秒）；
//   2. 兼容自然语言时间短语：若 `time_expr` 为自然语言短语或未填，走 `parseNaturalTimeText`（底层复用确定性解析器
//      `ParseTimeWindowsAt`，并无缝支持读侧持续时长 `for about N months`、回顾式周末 `had a ... weekend`、
//      中英文旬 `early/mid/late` / `上中下旬`、四季 `spring..winter` / `春夏秋冬` 与模糊数词 `a few / several / 几`）；
//   3. 解不出 ⇒ 返回 (0, "")（诚实留空，不猜）。

var (
	// DSL ① 绝对日期 / 时刻 / 区间 / 年月 / 年（支持秒级精度、可选小数秒及 Z/±HH:MM 时区 YYYY-MM-DD[THH:MM[:SS[.fff]][Z|±HH:MM]]）
	dslRangeRE = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})(?:[t\s](\d{1,2}):(\d{2})(?::(\d{2})(?:\.\d{1,9})?)?(?:z|([+-]\d{2}):?(\d{2}))?)?\s*(?:\.\.|~)\s*(\d{4})-(\d{2})-(\d{2})(?:[t\s](\d{1,2}):(\d{2})(?::(\d{2})(?:\.\d{1,9})?)?(?:z|([+-]\d{2}):?(\d{2}))?)?$`)
	dslDateRE  = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})(?:[t\s](\d{1,2}):(\d{2})(?::(\d{2})(?:\.\d{1,9})?)?(?:z|([+-]\d{2}):?(\d{2}))?)?$`)
	dslMonthRE = regexp.MustCompile(`^(\d{4})-(\d{2})$`)
	dslYearRE  = regexp.MustCompile(`^(19\d{2}|20\d{2})$`)

	// DSL ② 旬 / 年阶段 / 季度 / 季节（带显式年份 · 纯 ASCII 标准 DSL 符号，不含自然语言汉字词表）
	dslTenDayRE    = regexp.MustCompile(`^(\d{4})-(\d{1,2})-(early|mid|late)$`)
	dslYearPhaseRE = regexp.MustCompile(`^(\d{4})-(early|mid|late|end)$`)
	dslQuarterRE   = regexp.MustCompile(`^(\d{4})-q([1-4])$`)
	dslSeasonRE    = regexp.MustCompile(`^(\d{4})-(spring|summer|autumn|fall|winter)$`)
	dslRelYearRE   = regexp.MustCompile(`^(last|this|next):year-(early|mid|late|end)$`)

	// DSL ③ 相对偏移 / 锚定复合偏移：[YYYY-MM-DD[THH:MM[:SS]] ]±N(s|sec|min|h|hr|d|w|m|y|...)
	dslOffsetRE = regexp.MustCompile(`^(?:(\d{4})-(\d{2})-(\d{2})(?:[t\s](\d{1,2}):(\d{2})(?::(\d{2})(?:\.\d{1,9})?)?(?:z|([+-]\d{2}):?(\d{2}))?)?\s+)?([+-]\d{1,6})\s*(s|sec|secs|second|seconds|min|mins|minute|minutes|h|hr|hrs|hour|hours|d|w|m|y|days?|weeks?|months?|years?)$`)

	// DSL ④ 周末 / 星期相对算子：[YYYY-MM-DD ](last|this|next|[+-]\d+):(weekend|mon..sun)
	dslWeekOpRE = regexp.MustCompile(`^(?:(\d{4})-(\d{2})-(\d{2})\s+)?(last|this|next|[+-]?\d{1,2}):(weekends?|mon|monday|tue|tuesday|wed|wednesday|thu|thursday|fri|friday|sat|saturday|sun|sunday)$`)

	// DSL ⑤ 相对季节 / 相对季度：(last|this|next):(spring|summer|autumn|fall|winter|q1..q4)
	dslRelSeasonRE = regexp.MustCompile(`^(last|this|next):(spring|summer|autumn|fall|winter|q[1-4])$`)

	// 自然语言补充 0：显式日期+时分秒（ISO 含可选小数秒与 Z/±HH:MM 时区 / 中文年月日时分秒 / 英文月名日年+时分秒）
	isoDateTimeTextRE = regexp.MustCompile(`\b(19\d{2}|20\d{2})[-/.](\d{1,2})[-/.](\d{1,2})[t\s]+(\d{1,2}):(\d{2})(?::(\d{2})(?:\.\d{1,9})?)?(?:\s*(am|pm))?(?:z|([+-]\d{2}):(\d{2}))?(?:\b|$)`)
	cnDateTimeTextRE  = regexp.MustCompile(`(19\d{2}|20\d{2})\s*年\s*(\d{1,2})\s*月\s*(\d{1,2})\s*[日号]?\s*(?:(凌晨|早上|上午|中午|下午|傍晚|晚上)\s*)?(?:(\d{1,2})\s*[:：]\s*(\d{1,2})(?:\s*[:：]\s*(\d{1,2}))?|(\d{1,2})\s*[时点](?:(半)|(?:\s*(\d{1,2})\s*分(?:\s*(\d{1,2})\s*秒)?)?))`)
	enMDYClockTextRE  = regexp.MustCompile(`\b(january|february|march|april|may|june|july|august|september|october|november|december|jan|feb|mar|apr|jun|jul|aug|sept|sep|oct|nov|dec)\s+(\d{1,2})(?:st|nd|rd|th)?,?\s+(19\d{2}|20\d{2})\s+(?:at\s+)?(\d{1,2}):(\d{2})(?::(\d{2}))?(?:\s*(am|pm))?\b`)
	enDMYClockTextRE  = regexp.MustCompile(`\b(\d{1,2})(?:st|nd|rd|th)?\s+(?:of\s+)?(january|february|march|april|may|june|july|august|september|october|november|december|jan|feb|mar|apr|jun|jul|aug|sept|sep|oct|nov|dec),?\s+(19\d{2}|20\d{2})\s+(?:at\s+)?(\d{1,2}):(\d{2})(?::(\d{2}))?(?:\s*(am|pm))?\b`)

	// 自然语言补充 0b：亚日级相对时间（秒 / 分钟 / 小时）及相对日内时钟后缀
	enSubDayAgoRE   = regexp.MustCompile(`\b(a|an|one|two|three|four|five|six|seven|eight|nine|ten|eleven|twelve|fifteen|twenty|thirty|forty-five|a\s+couple\s+of|a\s+few|several|half\s+an|\d{1,5})\s+(seconds?|secs?|minutes?|mins?|hours?|hrs?)\s+(ago|later|from\s+now)\b`)
	enSubDayInRE    = regexp.MustCompile(`\bin\s+(a|an|one|two|three|four|five|six|seven|eight|nine|ten|eleven|twelve|fifteen|twenty|thirty|forty-five|a\s+couple\s+of|a\s+few|several|half\s+an|\d{1,5})\s+(seconds?|secs?|minutes?|mins?|hours?|hrs?)\b`)
	cnSubDayRelRE   = regexp.MustCompile(`(?:([0-9０-９]{1,5}|[一二两三四五六七八九十百零〇]{1,5})\s*(?:个)?(秒钟?|分钟?|小时|钟头)|半(?:个)?(小时|钟头)|(一刻钟))(?:以)?(前|后|之后|以前)`)
	relDayClockENRE = regexp.MustCompile(`\b(?:at\s+)?(\d{1,2}):(\d{2})(?::(\d{2}))?(?:\s*(am|pm))?\b`)
	relDayClockCNRE = regexp.MustCompile(`(?:(凌晨|早上|上午|中午|下午|傍晚|晚上)\s*)?(?:(\d{1,2})\s*[:：]\s*(\d{1,2})(?:\s*[:：]\s*(\d{1,2}))?|(\d{1,2})\s*[时点](?:(半)|(?:\s*(\d{1,2})\s*分(?:\s*(\d{1,2})\s*秒)?)?))`)

	// 自然语言补充 ①：相对年月回溯（零动词/零季节/零旬词表）
	cnDurationRE = regexp.MustCompile(`([0-9０-９]{1,2}|[一二两三四五六七八九十]{1,3})\s*(?:个)?(月|年)(?:以)?前`)

	// 自然语言补充 ④：中英模糊数词（"a few days ago" / "several weeks ago" / "几天前" / "好几个星期前" / "几个月前"）
	enVagueAgoRE = regexp.MustCompile(`\b(a\s+few|several)\s+(days?|weeks?|months?|years?)\s+ago\b`)
	cnVagueAgoRE = regexp.MustCompile(`(?:好几|几)(?:个)?(天|日|周|星期|礼拜|月|年)(?:以)?前`)
)

// ResolveAssertionTime 写侧断言时间解算总入口（方案 A · 双保险 + 防 t_source 回声污染）：
//  1. 先从正文时间短语（`timeText` → `anchorSpan`）走 `ParseTimeExprAt` 取得原文证据 `(aText, kindText)`；
//  2. 若 `timeExpr` 非空，走 `ParseTimeExprAt`（DSL → 增强自然语言）解出 `(aExpr, kindExpr)`：
//     - 守卫 A（防误抄外层 `t_source` 元数据头）：若 `aExpr` 恰好等于 `turnOccurredAt` 当天 00:00 UTC 且为 `"explicit"`，
//     但原文（`timeText` / `anchorSpan`）既解不出任何时间（`aText == 0`）也不含 `timeExpr` 字面量，则判定为模型误抄外层
//     `【#i】时间：YYYY-MM-DD` 基准头，拒绝将其作为事件时间 `t_event`（返回 `0, ""`）；
//     - 守卫 B（`event_src` 原文保真）：若模型把 `"yesterday"` / `"today"` 等相对词心算成 `"YYYY-MM-DD"` 填入 `timeExpr`
//     （致 `kindExpr == "explicit"`），而原文证据 `kindText == "relative_resolved"`，则 `event_src` 保真为 `"relative_resolved"`；
//  3. 若 `timeExpr` 未解出，则返回原文证据 `(aText, kindText)`；均解不出 ⇒ 返回 `(0, "")`（诚实留空，不猜）。
func ResolveAssertionTime(timeExpr, timeText, anchorSpan string, turnOccurredAt int64) (int64, string) {
	var ref, refDay time.Time
	if turnOccurredAt > 0 {
		ref = time.Unix(turnOccurredAt, 0).UTC()
		refDay = time.Date(ref.Year(), ref.Month(), ref.Day(), 0, 0, 0, 0, time.UTC)
	}
	var aText int64
	var kindText string
	for _, src := range []string{strings.TrimSpace(timeText), strings.TrimSpace(anchorSpan)} {
		if src == "" {
			continue
		}
		if a, k := ParseTimeExprAt(src, ref); a > 0 && k != "" {
			aText, kindText = a, k
			break
		}
	}
	expr := strings.TrimSpace(timeExpr)
	if expr != "" {
		if aExpr, kindExpr := ParseTimeExprAt(expr, ref); aExpr > 0 && kindExpr != "" {
			if !refDay.IsZero() && strings.TrimSpace(anchorSpan) != "" {
				uExpr := time.Unix(aExpr, 0).UTC()
				yd := uExpr.Year() - refDay.Year()
				if yd < 0 {
					yd = -yd
				}
				if yd >= 2 && !strings.Contains(anchorSpan, strconv.Itoa(uExpr.Year())) {
					if aText > 0 {
						return aText, kindText
					}
					return 0, ""
				}
			}
			// 守卫 A：拦截无原文依据的外层 t_source（DateUTC 日级或 DateTimeUTC 秒级）绝对日期/时刻回声
			if !refDay.IsZero() && (aExpr == refDay.Unix() || aExpr == ref.Unix()) && kindExpr == "explicit" && aText == 0 {
				lowExpr := strings.ToLower(expr)
				if !strings.Contains(strings.ToLower(anchorSpan), lowExpr) && !strings.Contains(strings.ToLower(timeText), lowExpr) {
					return 0, ""
				}
			}
			// 守卫 B：拦截将模糊近况词无依据臆造为 "-1m" 月首日期的情况
			if aText == 0 && kindExpr == "relative_resolved" {
				lowExpr := strings.ToLower(expr)
				if strings.HasSuffix(lowExpr, "m") && len(ParseTimeWindows(timeText+" "+anchorSpan)) == 0 {
					return 0, ""
				}
			}
			// 守卫 C：当原文确定性解析已解出相对日期（aText > 0 && kindText == "relative_resolved"）且与模型 time_expr 不一致时，
			// 以确定性日历解析器的原文结果 aText 为准，消除模型心算日历与粗粒度截断误差。
			if aText > 0 && kindText == "relative_resolved" && aExpr != aText {
				lowExpr := strings.ToLower(expr)
				lowSpan := strings.ToLower(timeText + " " + anchorSpan)
				if strings.Contains(lowExpr, "-01-01 ") && !strings.Contains(lowSpan, "-01-01") {
					return aText, kindText
				}
				uExpr := time.Unix(aExpr, 0).UTC()
				uText := time.Unix(aText, 0).UTC()
				if uExpr.Year() == uText.Year() && uExpr.Month() == time.January && uExpr.Day() == 1 &&
					(uText.Month() != time.January || uText.Day() != 1) {
					return aText, kindText
				}
				if !strings.Contains(lowSpan, lowExpr) && len(ParseTimeWindows(timeText+" "+anchorSpan)) == 0 {
					diff := aExpr - aText
					if diff < 0 {
						diff = -diff
					}
					if diff <= 14*86400 {
						return aText, kindText
					}
				}
			}
			// 守卫 D：若原文实为相对时间（如 yesterday/today/30 minutes ago），且原文并不含模型所填的显式日期字面量，
			// 则即使模型在 time_expr 中写成了显式时刻，event_src 仍保真为 relative_resolved
			if kindExpr == "explicit" && kindText == "relative_resolved" {
				lowExpr := strings.ToLower(expr)
				if !strings.Contains(strings.ToLower(anchorSpan), lowExpr) && !strings.Contains(strings.ToLower(timeText), lowExpr) {
					kindExpr = "relative_resolved"
				}
			}
			return aExpr, kindExpr
		}
	}
	return aText, kindText
}

// ParseTimeExprAt 解算单个时间表达式（支持标准时间 DSL 与中英自然语言短语）。
// 返回 `(anchorUnixSec, kind)`，`kind ∈ {"explicit", "relative_resolved", ""}`。
func ParseTimeExprAt(raw string, ref time.Time) (int64, string) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, ""
	}
	// 1. 先尝试紧凑型标准时间 DSL（100% 语言无关；若已是纯 ISO 日期/年月形状但日历非法，如平年 2023-02-29，直接短路拒绝，防下层 time.Date 溢出到 3 月）
	if a, kind, isMalformedISO := parseTimeDSL(s, ref); isMalformedISO {
		return 0, ""
	} else if a > 0 && kind != "" {
		return a, kind
	}
	// 2. 回退到自然语言时间解析（确定性解析器 + 增强规则）
	return parseNaturalTimeText(s, ref)
}

// parseTimeDSL 解析统一时间微语言（DSL）五形态。第三返回值 isMalformedISO 标识「匹配了纯 ISO 日期/年月形状但日历值越界」。
func parseTimeDSL(raw string, ref time.Time) (int64, string, bool) {
	low := strings.ToLower(strings.TrimSpace(raw))
	var refDay time.Time
	hasRef := !ref.IsZero()
	if hasRef {
		u := ref.UTC()
		refDay = time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
	}

	// ① 绝对区间 YYYY-MM-DD[THH:MM[:SS]]..YYYY-MM-DD[THH:MM[:SS]]
	if m := dslRangeRE.FindStringSubmatch(low); m != nil {
		t1, ok1 := validUTCDateTimeTZ(m[1], m[2], m[3], m[4], m[5], m[6], m[7], m[8])
		_, ok2 := validUTCDateTimeTZ(m[9], m[10], m[11], m[12], m[13], m[14], m[15], m[16])
		if ok1 && ok2 {
			return t1.Unix(), "explicit", false
		}
		return 0, "", true
	}
	// ① 绝对日期 / 时刻 YYYY-MM-DD[THH:MM[:SS]]
	if m := dslDateRE.FindStringSubmatch(low); m != nil {
		if t, ok := validUTCDateTimeTZ(m[1], m[2], m[3], m[4], m[5], m[6], m[7], m[8]); ok {
			return t.Unix(), "explicit", false
		}
		return 0, "", true
	}
	// ① 绝对年月 YYYY-MM
	if m := dslMonthRE.FindStringSubmatch(low); m != nil {
		y, _ := strconv.Atoi(m[1])
		mo, _ := strconv.Atoi(m[2])
		if y >= 1900 && y <= 2099 && mo >= 1 && mo <= 12 {
			return time.Date(y, time.Month(mo), 1, 0, 0, 0, 0, time.UTC).Unix(), "explicit", false
		}
		return 0, "", true
	}
	// ① 绝对年 YYYY
	if m := dslYearRE.FindStringSubmatch(low); m != nil {
		y, _ := strconv.Atoi(m[1])
		return time.Date(y, 1, 1, 0, 0, 0, 0, time.UTC).Unix(), "explicit", false
	}

	// ② 旬 YYYY-MM-(early|mid|late|上旬|中旬|下旬)
	if m := dslTenDayRE.FindStringSubmatch(low); m != nil {
		y, _ := strconv.Atoi(m[1])
		mo, _ := strconv.Atoi(m[2])
		if y >= 1900 && y <= 2099 && mo >= 1 && mo <= 12 {
			d := tenDayStart(m[3])
			return time.Date(y, time.Month(mo), d, 0, 0, 0, 0, time.UTC).Unix(), "explicit", false
		}
		return 0, "", true
	}
	// ② 年阶段 YYYY-(early|mid|late|end)
	if m := dslYearPhaseRE.FindStringSubmatch(low); m != nil {
		y, _ := strconv.Atoi(m[1])
		if y >= 1900 && y <= 2099 {
			mo := 1
			switch m[2] {
			case "mid":
				mo = 6
			case "late", "end":
				mo = 12
			}
			return time.Date(y, time.Month(mo), 1, 0, 0, 0, 0, time.UTC).Unix(), "explicit", false
		}
	}
	if m := dslRelYearRE.FindStringSubmatch(low); m != nil && hasRef {
		y := refDay.Year()
		switch m[1] {
		case "last":
			y--
		case "next":
			y++
		}
		mo := 1
		switch m[2] {
		case "mid":
			mo = 6
		case "late", "end":
			mo = 12
		}
		return time.Date(y, time.Month(mo), 1, 0, 0, 0, 0, time.UTC).Unix(), "relative_resolved", false
	}
	// ② 季度 YYYY-Q1..Q4
	if m := dslQuarterRE.FindStringSubmatch(low); m != nil {
		y, _ := strconv.Atoi(m[1])
		q, _ := strconv.Atoi(m[2])
		if y >= 1900 && y <= 2099 && q >= 1 && q <= 4 {
			mo := (q-1)*3 + 1
			return time.Date(y, time.Month(mo), 1, 0, 0, 0, 0, time.UTC).Unix(), "explicit", false
		}
	}
	// ② 季节 YYYY-(spring|summer|autumn|fall|winter)
	if m := dslSeasonRE.FindStringSubmatch(low); m != nil {
		y, _ := strconv.Atoi(m[1])
		if y >= 1900 && y <= 2099 {
			if mo := seasonStartMonth(m[2]); mo > 0 {
				return time.Date(y, time.Month(mo), 1, 0, 0, 0, 0, time.UTC).Unix(), "explicit", false
			}
		}
	}

	// ③ 相对偏移 / 锚定复合偏移：[YYYY-MM-DD[THH:MM[:SS]] ]±N(s|sec|min|h|hr|d|w|m|y)
	if m := dslOffsetRE.FindStringSubmatch(low); m != nil {
		base := refDay
		basePrecise := ref.UTC()
		baseOK := hasRef
		hasExplicitClock := m[4] != ""
		if m[1] != "" {
			if t, ok := validUTCDateTimeTZ(m[1], m[2], m[3], m[4], m[5], m[6], m[7], m[8]); ok {
				base, basePrecise, baseOK = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC), t, true
			} else {
				return 0, "", true
			}
		}
		if !baseOK {
			return 0, "", false
		}
		delta, err := strconv.Atoi(m[9])
		if err != nil {
			return 0, "", false
		}
		rawUnit := m[10]
		switch {
		case rawUnit == "s" || strings.HasPrefix(rawUnit, "sec"):
			return basePrecise.Add(time.Duration(delta) * time.Second).Unix(), "relative_resolved", false
		case strings.HasPrefix(rawUnit, "min") || (rawUnit == "m" && hasExplicitClock):
			return basePrecise.Add(time.Duration(delta) * time.Minute).Unix(), "relative_resolved", false
		case rawUnit == "h" || strings.HasPrefix(rawUnit, "hr") || strings.HasPrefix(rawUnit, "hour"):
			return basePrecise.Add(time.Duration(delta) * time.Hour).Unix(), "relative_resolved", false
		case strings.HasPrefix(rawUnit, "d"):
			return base.AddDate(0, 0, delta).Unix(), "relative_resolved", false
		case strings.HasPrefix(rawUnit, "w"):
			return base.AddDate(0, 0, 7*delta).Unix(), "relative_resolved", false
		case strings.HasPrefix(rawUnit, "m"):
			return time.Date(base.Year(), base.Month()+time.Month(delta), 1, 0, 0, 0, 0, time.UTC).Unix(), "relative_resolved", false
		case strings.HasPrefix(rawUnit, "y"):
			return time.Date(base.Year()+delta, 1, 1, 0, 0, 0, 0, time.UTC).Unix(), "relative_resolved", false
		}
	}

	// ④ 周末 / 星期相对算子：[YYYY-MM-DD ](last|this|next|±N):(weekend|mon..sun)
	if m := dslWeekOpRE.FindStringSubmatch(low); m != nil {
		base := refDay
		baseOK := hasRef
		hasExplicitBase := false
		if m[1] != "" {
			if t, ok := validUTCDate(m[1], m[2], m[3]); ok {
				base, baseOK, hasExplicitBase = t, true, true
			} else {
				return 0, "", true
			}
		}
		if !baseOK {
			return 0, "", false
		}
		op, target := m[4], m[5]
		if strings.HasPrefix(target, "weekend") {
			return resolveDSLWeekend(base, op).Unix(), "relative_resolved", false
		}
		if wd, ok := parseDSLWeekday(target); ok {
			return resolveDSLWeekday(base, op, wd, hasExplicitBase).Unix(), "relative_resolved", false
		}
	}

	// ⑤ 相对季节 / 季度：(last|this|next):(spring..winter|q1..q4)
	if m := dslRelSeasonRE.FindStringSubmatch(low); m != nil && hasRef {
		y := refDay.Year()
		switch m[1] {
		case "last":
			y--
		case "next":
			y++
		}
		target := m[2]
		if strings.HasPrefix(target, "q") {
			q, _ := strconv.Atoi(target[1:])
			return time.Date(y, time.Month((q-1)*3+1), 1, 0, 0, 0, 0, time.UTC).Unix(), "relative_resolved", false
		}
		if mo := seasonStartMonth(target); mo > 0 {
			return time.Date(y, time.Month(mo), 1, 0, 0, 0, 0, time.UTC).Unix(), "relative_resolved", false
		}
	}

	return 0, "", false
}

// parseNaturalTimeText 自然语言时间短语解算（长式/细粒度规则先行 → 确定性解析器 → 持续时长/回顾周末/模糊数词兜底）。
func parseNaturalTimeText(src string, ref time.Time) (int64, string) {
	low := strings.ToLower(src)
	var refDay time.Time
	hasRef := !ref.IsZero()
	if hasRef {
		u := ref.UTC()
		refDay = time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
	}

	// 2b. 当文本不含显式绝对日期窗口时，相对年月回溯（"one year ago" / "N个月前"）
	// 保留参考时刻的精确月份 refDay.Month()，防止被查询侧宽召回窗 relYearSpan 粗化抹平到年初 01-01。
	if hasRef && len(ParseTimeWindows(src)) == 0 {
		if m := durationOffsetRE.FindStringSubmatch(low); len(m) == 5 {
			numStr, unitStr := m[1], m[2]
			if numStr == "" {
				numStr, unitStr = m[3], m[4]
			}
			if n := parseSmallCount(numStr); n > 0 {
				if strings.HasPrefix(unitStr, "month") {
					return time.Date(refDay.Year(), refDay.Month()-time.Month(n), 1, 0, 0, 0, 0, time.UTC).Unix(), "relative_resolved"
				}
				if strings.HasPrefix(unitStr, "year") {
					return time.Date(refDay.Year()-n, refDay.Month(), 1, 0, 0, 0, 0, time.UTC).Unix(), "relative_resolved"
				}
			}
		}
		if m := cnDurationRE.FindStringSubmatch(src); m != nil {
			numStr, unitStr := m[1], m[2]
			if n := parseCNMonthNum(numStr); n > 0 {
				if unitStr == "月" {
					return time.Date(refDay.Year(), refDay.Month()-time.Month(n), 1, 0, 0, 0, 0, time.UTC).Unix(), "relative_resolved"
				}
				if unitStr == "年" {
					return time.Date(refDay.Year()-n, refDay.Month(), 1, 0, 0, 0, 0, time.UTC).Unix(), "relative_resolved"
				}
			}
		}
	}

	// 3. 走既有确定性解析器（100% 保持既有 79 例基准逐字一致），并在命中时将亚日级时分秒精度提升到秒
	if !ref.IsZero() {
		wins := ParseTimeWindowsAt(src, ref)
		if a, kind := pickTimeAnchorKind(wins, len(ParseTimeWindows(src))); a > 0 {
			if kind == "explicit" {
				if ts := extractExplicitSecondTimestamp(src, low); ts > 0 {
					return ts, "explicit"
				}
			} else if kind == "relative_resolved" && a%86400 == 0 {
				if ts := attachClockToDay(a, src, low); ts > 0 {
					return ts, "relative_resolved"
				}
			}
			return a, kind
		}
	} else {
		wins := ParseTimeWindowsAt(src, time.Time{})
		if a, kind := pickTimeAnchorKind(wins, len(ParseTimeWindows(src))); a > 0 {
			if kind == "explicit" {
				if ts := extractExplicitSecondTimestamp(src, low); ts > 0 {
					return ts, "explicit"
				}
			}
			return a, kind
		}
	}
	if ts := extractExplicitSecondTimestamp(src, low); ts > 0 {
		return ts, "explicit"
	}

	if !hasRef {
		return 0, ""
	}

	// 3b. 亚日级相对时间（秒 / 分钟 / 小时）：以完整秒级 ref.UTC() 为基准解算
	if ts := parseSubDayRelOffset(src, low, ref.UTC()); ts > 0 {
		return ts, "relative_resolved"
	}

	// 5. 补充规则 D（收编自读侧 reldate.go + 中文扩展）：持续时长倒推（"for about four months now" / "了四个月"）
	if m := durationOffsetRE.FindStringSubmatch(low); len(m) == 5 {
		numStr, unitStr := m[1], m[2]
		if numStr == "" {
			numStr, unitStr = m[3], m[4]
		}
		if n := parseSmallCount(numStr); n > 0 {
			if strings.HasPrefix(unitStr, "month") {
				return time.Date(refDay.Year(), refDay.Month()-time.Month(n), 1, 0, 0, 0, 0, time.UTC).Unix(), "relative_resolved"
			}
			if strings.HasPrefix(unitStr, "year") {
				return time.Date(refDay.Year()-n, refDay.Month(), 1, 0, 0, 0, 0, time.UTC).Unix(), "relative_resolved"
			}
		}
	}
	if m := cnDurationRE.FindStringSubmatch(src); m != nil {
		if n := parseCNMonthNum(m[1]); n > 0 {
			if m[2] == "月" {
				return time.Date(refDay.Year(), refDay.Month()-time.Month(n), 1, 0, 0, 0, 0, time.UTC).Unix(), "relative_resolved"
			}
			if m[2] == "年" {
				return time.Date(refDay.Year()-n, refDay.Month(), 1, 0, 0, 0, 0, time.UTC).Unix(), "relative_resolved"
			}
		}
	}

	// 6. 补充规则 E：中英模糊数词（"a few days ago" / "several weeks ago" / "几天前" / "几个月前"；保守取 3）
	if m := enVagueAgoRE.FindStringSubmatch(low); m != nil {
		const n = 3
		unit := m[2]
		switch {
		case strings.HasPrefix(unit, "day"):
			return refDay.AddDate(0, 0, -n).Unix(), "relative_resolved"
		case strings.HasPrefix(unit, "week"):
			return refDay.AddDate(0, 0, -7*n).Unix(), "relative_resolved"
		case strings.HasPrefix(unit, "month"):
			return time.Date(refDay.Year(), refDay.Month()-time.Month(n), 1, 0, 0, 0, 0, time.UTC).Unix(), "relative_resolved"
		case strings.HasPrefix(unit, "year"):
			return time.Date(refDay.Year()-n, 1, 1, 0, 0, 0, 0, time.UTC).Unix(), "relative_resolved"
		}
	}
	if m := cnVagueAgoRE.FindStringSubmatch(src); m != nil {
		const n = 3
		switch m[1] {
		case "天", "日":
			return refDay.AddDate(0, 0, -n).Unix(), "relative_resolved"
		case "周", "星期", "礼拜":
			return refDay.AddDate(0, 0, -7*n).Unix(), "relative_resolved"
		case "月":
			return time.Date(refDay.Year(), refDay.Month()-time.Month(n), 1, 0, 0, 0, 0, time.UTC).Unix(), "relative_resolved"
		case "年":
			return time.Date(refDay.Year()-n, 1, 1, 0, 0, 0, 0, time.UTC).Unix(), "relative_resolved"
		}
	}

	return 0, ""
}

func validUTCDate(ys, ms, ds string) (time.Time, bool) {
	y, err1 := strconv.Atoi(ys)
	mo, err2 := strconv.Atoi(ms)
	d, err3 := strconv.Atoi(ds)
	if err1 != nil || err2 != nil || err3 != nil || y < 1900 || y > 2099 || mo < 1 || mo > 12 || d < 1 || d > 31 {
		return time.Time{}, false
	}
	t := time.Date(y, time.Month(mo), d, 0, 0, 0, 0, time.UTC)
	if t.Year() != y || int(t.Month()) != mo || t.Day() != d {
		return time.Time{}, false
	}
	return t, true
}

func validUTCDateTime(ys, ms, ds, hhs, mms, sss string) (time.Time, bool) {
	return validUTCDateTimeTZ(ys, ms, ds, hhs, mms, sss, "", "")
}

func validUTCDateTimeTZ(ys, ms, ds, hhs, mms, sss, tzHs, tzMs string) (time.Time, bool) {
	base, ok := validUTCDate(ys, ms, ds)
	if !ok {
		return time.Time{}, false
	}
	if hhs == "" {
		return base, true
	}
	hh, err1 := strconv.Atoi(hhs)
	mm, err2 := strconv.Atoi(mms)
	ss := 0
	var err3 error
	if sss != "" {
		ss, err3 = strconv.Atoi(sss)
	}
	if err1 != nil || err2 != nil || err3 != nil || hh < 0 || hh > 23 || mm < 0 || mm > 59 || ss < 0 || ss > 59 {
		return time.Time{}, false
	}
	t := time.Date(base.Year(), base.Month(), base.Day(), hh, mm, ss, 0, time.UTC)
	if tzHs != "" {
		tzH, errH := strconv.Atoi(tzHs)
		tzM := 0
		var errM error
		if tzMs != "" {
			tzM, errM = strconv.Atoi(tzMs)
		}
		if errH != nil || errM != nil || tzH < -14 || tzH > 14 || tzM < 0 || tzM > 59 {
			return time.Time{}, false
		}
		sign := 1
		if strings.HasPrefix(tzHs, "-") {
			sign = -1
			if tzH < 0 {
				tzH = -tzH
			}
		}
		offsetSec := sign * (tzH*3600 + tzM*60)
		t = t.Add(-time.Duration(offsetSec) * time.Second)
	}
	return t, true
}

func applyMeridiemHour(hh int, mer string) (int, bool) {
	mer = strings.ToLower(strings.TrimSpace(mer))
	switch mer {
	case "":
		return hh, hh >= 0 && hh <= 23
	case "am", "凌晨", "早上", "上午":
		if hh < 1 || hh > 12 {
			return 0, false
		}
		if hh == 12 {
			return 0, true
		}
		return hh, true
	case "pm", "下午", "傍晚", "晚上":
		if hh < 1 || hh > 12 {
			if hh >= 13 && hh <= 23 {
				return hh, true
			}
			return 0, false
		}
		if hh < 12 {
			return hh + 12, true
		}
		return 12, true
	case "中午":
		if hh >= 1 && hh <= 3 {
			return hh + 12, true
		}
		return hh, hh >= 11 && hh <= 14
	}
	return hh, hh >= 0 && hh <= 23
}

// extractExplicitSecondTimestamp 从含显式日期+时分秒的文本中提取秒级 UTC 时间戳（无合法时刻则返回 0）。
func extractExplicitSecondTimestamp(src, low string) int64 {
	if m := isoDateTimeTextRE.FindStringSubmatch(low); m != nil {
		if d, ok := validUTCDate(m[1], m[2], m[3]); ok {
			hh, _ := strconv.Atoi(m[4])
			mm, _ := strconv.Atoi(m[5])
			ss, _ := strconv.Atoi(m[6])
			if hh2, okH := applyMeridiemHour(hh, m[7]); okH && mm >= 0 && mm <= 59 && ss >= 0 && ss <= 59 {
				if t, okTZ := validUTCDateTimeTZ(m[1], m[2], m[3], strconv.Itoa(hh2), m[5], m[6], m[8], m[9]); okTZ {
					return t.Unix()
				}
				return time.Date(d.Year(), d.Month(), d.Day(), hh2, mm, ss, 0, time.UTC).Unix()
			}
		}
	}
	if m := enMDYClockTextRE.FindStringSubmatch(low); m != nil {
		mo := enMonthNumber(m[1])
		if d, ok := validUTCDate(m[3], strconv.Itoa(mo), m[2]); ok {
			hh, _ := strconv.Atoi(m[4])
			mm, _ := strconv.Atoi(m[5])
			ss, _ := strconv.Atoi(m[6])
			if hh2, okH := applyMeridiemHour(hh, m[7]); okH && mm >= 0 && mm <= 59 && ss >= 0 && ss <= 59 {
				return time.Date(d.Year(), d.Month(), d.Day(), hh2, mm, ss, 0, time.UTC).Unix()
			}
		}
	}
	if m := enDMYClockTextRE.FindStringSubmatch(low); m != nil {
		mo := enMonthNumber(m[2])
		if d, ok := validUTCDate(m[3], strconv.Itoa(mo), m[1]); ok {
			hh, _ := strconv.Atoi(m[4])
			mm, _ := strconv.Atoi(m[5])
			ss, _ := strconv.Atoi(m[6])
			if hh2, okH := applyMeridiemHour(hh, m[7]); okH && mm >= 0 && mm <= 59 && ss >= 0 && ss <= 59 {
				return time.Date(d.Year(), d.Month(), d.Day(), hh2, mm, ss, 0, time.UTC).Unix()
			}
		}
	}
	if m := cnDateTimeTextRE.FindStringSubmatch(src); m != nil {
		if d, ok := validUTCDate(m[1], m[2], m[3]); ok {
			mer := m[4]
			var hh, mm, ss int
			if m[5] != "" {
				hh, _ = strconv.Atoi(m[5])
				mm, _ = strconv.Atoi(m[6])
				ss, _ = strconv.Atoi(m[7])
			} else if m[8] != "" {
				hh, _ = strconv.Atoi(m[8])
				if m[9] == "半" {
					mm = 30
				} else if m[10] != "" {
					mm, _ = strconv.Atoi(m[10])
					ss, _ = strconv.Atoi(m[11])
				}
			}
			if hh2, okH := applyMeridiemHour(hh, mer); okH && mm >= 0 && mm <= 59 && ss >= 0 && ss <= 59 {
				return time.Date(d.Year(), d.Month(), d.Day(), hh2, mm, ss, 0, time.UTC).Unix()
			}
		}
	}
	return 0
}

// attachClockToDay 当相对日期解出整日零点 dayUnix 时，若同句含有显式时分秒（如 "yesterday at 14:30:25" / "昨天下午3点20分15秒"），
// 将日内时钟叠加到该相对日期上，产出秒级相对时刻（若同句同时含显式年月日，不叠加防串台）。
func attachClockToDay(dayUnix int64, src, low string) int64 {
	if extractExplicitSecondTimestamp(src, low) > 0 {
		return 0
	}
	d := time.Unix(dayUnix, 0).UTC()
	if m := relDayClockENRE.FindStringSubmatch(low); m != nil {
		hh, _ := strconv.Atoi(m[1])
		mm, _ := strconv.Atoi(m[2])
		ss, _ := strconv.Atoi(m[3])
		if hh2, okH := applyMeridiemHour(hh, m[4]); okH && mm >= 0 && mm <= 59 && ss >= 0 && ss <= 59 {
			return time.Date(d.Year(), d.Month(), d.Day(), hh2, mm, ss, 0, time.UTC).Unix()
		}
	}
	if m := relDayClockCNRE.FindStringSubmatch(src); m != nil {
		mer := m[1]
		var hh, mm, ss int
		if m[2] != "" {
			hh, _ = strconv.Atoi(m[2])
			mm, _ = strconv.Atoi(m[3])
			ss, _ = strconv.Atoi(m[4])
		} else if m[5] != "" {
			hh, _ = strconv.Atoi(m[5])
			if m[6] == "半" {
				mm = 30
			} else if m[7] != "" {
				mm, _ = strconv.Atoi(m[7])
				ss, _ = strconv.Atoi(m[8])
			}
		}
		if hh2, okH := applyMeridiemHour(hh, mer); okH && mm >= 0 && mm <= 59 && ss >= 0 && ss <= 59 && (hh2 > 0 || mm > 0 || ss > 0) {
			return time.Date(d.Year(), d.Month(), d.Day(), hh2, mm, ss, 0, time.UTC).Unix()
		}
	}
	return 0
}

func parseSubDayCount(raw string) int {
	s := strings.ToLower(strings.TrimSpace(raw))
	switch s {
	case "a", "an", "one":
		return 1
	case "two", "a couple of":
		return 2
	case "three", "a few", "several":
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
	case "fifteen":
		return 15
	case "twenty":
		return 20
	case "thirty":
		return 30
	case "forty-five":
		return 45
	}
	if n, err := strconv.Atoi(s); err == nil && n > 0 {
		return n
	}
	return parseCNMonthNum(s)
}

func parseSubDayRelOffset(src, low string, refUTC time.Time) int64 {
	if m := enSubDayAgoRE.FindStringSubmatch(low); m != nil {
		sign := -1
		if m[3] == "later" || strings.HasPrefix(m[3], "from") {
			sign = 1
		}
		if strings.HasPrefix(m[1], "half") && strings.HasPrefix(m[2], "h") {
			return refUTC.Add(time.Duration(sign*30) * time.Minute).Unix()
		}
		if n := parseSubDayCount(m[1]); n > 0 {
			u := m[2]
			switch {
			case strings.HasPrefix(u, "sec"):
				return refUTC.Add(time.Duration(sign*n) * time.Second).Unix()
			case strings.HasPrefix(u, "min"):
				return refUTC.Add(time.Duration(sign*n) * time.Minute).Unix()
			case strings.HasPrefix(u, "h"):
				return refUTC.Add(time.Duration(sign*n) * time.Hour).Unix()
			}
		}
	}
	if m := enSubDayInRE.FindStringSubmatch(low); m != nil {
		if strings.HasPrefix(m[1], "half") && strings.HasPrefix(m[2], "h") {
			return refUTC.Add(30 * time.Minute).Unix()
		}
		if n := parseSubDayCount(m[1]); n > 0 {
			u := m[2]
			switch {
			case strings.HasPrefix(u, "sec"):
				return refUTC.Add(time.Duration(n) * time.Second).Unix()
			case strings.HasPrefix(u, "min"):
				return refUTC.Add(time.Duration(n) * time.Minute).Unix()
			case strings.HasPrefix(u, "h"):
				return refUTC.Add(time.Duration(n) * time.Hour).Unix()
			}
		}
	}
	if m := cnSubDayRelRE.FindStringSubmatch(src); m != nil {
		sign := -1
		if strings.Contains(m[5], "后") {
			sign = 1
		}
		if m[3] != "" { // 半小时前/后
			return refUTC.Add(time.Duration(sign*30) * time.Minute).Unix()
		}
		if m[4] != "" { // 一刻钟前/后
			return refUTC.Add(time.Duration(sign*15) * time.Minute).Unix()
		}
		if n := parseCNMonthNum(m[1]); n > 0 {
			switch m[2] {
			case "秒", "秒钟":
				return refUTC.Add(time.Duration(sign*n) * time.Second).Unix()
			case "分", "分钟":
				return refUTC.Add(time.Duration(sign*n) * time.Minute).Unix()
			case "小时", "钟头":
				return refUTC.Add(time.Duration(sign*n) * time.Hour).Unix()
			}
		}
	}
	return 0
}

func tenDayStart(tag string) int {
	switch strings.ToLower(strings.TrimSpace(tag)) {
	case "mid":
		return 11
	case "late":
		return 21
	default:
		return 1
	}
}

func seasonStartMonth(s string) int {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "spring":
		return 3
	case "summer":
		return 6
	case "autumn", "fall":
		return 9
	case "winter":
		return 12
	}
	return 0
}

func enMonthNumber(s string) int {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "january", "jan":
		return 1
	case "february", "feb":
		return 2
	case "march", "mar":
		return 3
	case "april", "apr":
		return 4
	case "may":
		return 5
	case "june", "jun":
		return 6
	case "july", "jul":
		return 7
	case "august", "aug":
		return 8
	case "september", "sept", "sep":
		return 9
	case "october", "oct":
		return 10
	case "november", "nov":
		return 11
	case "december", "dec":
		return 12
	}
	return 0
}

func parseCNMonthNum(s string) int {
	s = strings.TrimSpace(s)
	var half strings.Builder
	for _, r := range s {
		if r >= '０' && r <= '９' {
			half.WriteRune('0' + (r - '０'))
		} else {
			half.WriteRune(r)
		}
	}
	if n, err := strconv.Atoi(half.String()); err == nil && n > 0 {
		return n
	}
	digits := map[rune]int{'一': 1, '二': 2, '两': 2, '三': 3, '四': 4, '五': 5, '六': 6, '七': 7, '八': 8, '九': 9}
	total, cur := 0, 0
	for _, r := range s {
		if r == '百' {
			if cur == 0 {
				cur = 1
			}
			total += cur * 100
			cur = 0
			continue
		}
		if r == '十' {
			if cur == 0 {
				cur = 1
			}
			total += cur * 10
			cur = 0
			continue
		}
		if r == '零' || r == '〇' {
			continue
		}
		if d, ok := digits[r]; ok {
			cur = cur*10 + d
		}
	}
	return total + cur
}

func resolveDSLWeekend(base time.Time, op string) time.Time {
	switch op {
	case "last", "-1":
		back := (int(base.Weekday()) - int(time.Saturday) + 7) % 7
		if back == 0 {
			back = 7
		}
		return base.AddDate(0, 0, -back)
	case "this", "0":
		sat, _ := currentWeekend(base)
		return sat
	case "next", "+1", "1":
		sat := base.AddDate(0, 0, (int(time.Saturday)-int(base.Weekday())+7)%7)
		if !sat.After(base) {
			sat = sat.AddDate(0, 0, 7)
		}
		return sat
	default:
		n, err := strconv.Atoi(op)
		if err != nil || n == 0 {
			return time.Time{}
		}
		if n < 0 {
			back := (int(base.Weekday()) - int(time.Saturday) + 7) % 7
			if back == 0 {
				back = 7
			}
			s1 := base.AddDate(0, 0, -back)
			return s1.AddDate(0, 0, 7*(n+1))
		}
		sat := base.AddDate(0, 0, (int(time.Saturday)-int(base.Weekday())+7)%7)
		if !sat.After(base) {
			sat = sat.AddDate(0, 0, 7)
		}
		return sat.AddDate(0, 0, 7*(n-1))
	}
}

func parseDSLWeekday(s string) (time.Weekday, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "mon", "monday":
		return time.Monday, true
	case "tue", "tuesday":
		return time.Tuesday, true
	case "wed", "wednesday":
		return time.Wednesday, true
	case "thu", "thursday":
		return time.Thursday, true
	case "fri", "friday":
		return time.Friday, true
	case "sat", "saturday":
		return time.Saturday, true
	case "sun", "sunday":
		return time.Sunday, true
	}
	return 0, false
}

func resolveDSLWeekday(base time.Time, op string, wd time.Weekday, hasExplicitBase bool) time.Time {
	// 若带有显式基准锚日（如 "2022-06-24 -1:fri" = The Friday before 24 June 2022），
	// -1 / last 表示严格早于基准日的最近该星期，+1 / next 表示严格晚于基准日的最近该星期。
	if hasExplicitBase {
		switch op {
		case "last", "-1":
			return shiftWeekdayBefore(base, wd)
		case "next", "+1", "1":
			return shiftWeekdayAfter(base, wd)
		default:
			if n, err := strconv.Atoi(op); err == nil && n != 0 {
				if n < 0 {
					first := shiftWeekdayBefore(base, wd)
					return first.AddDate(0, 0, 7*(n+1))
				}
				first := shiftWeekdayAfter(base, wd)
				return first.AddDate(0, 0, 7*(n-1))
			}
		}
	}
	// 无显式基准锚日时，与 relWeekday 保持完全同口径（周一起算日历周）
	weekStart := base.AddDate(0, 0, -((int(base.Weekday()) + 6) % 7))
	off := (int(wd) + 6) % 7
	switch op {
	case "last", "-1":
		return weekStart.AddDate(0, 0, -7+off)
	case "this", "0":
		return weekStart.AddDate(0, 0, off)
	case "next", "+1", "1":
		return weekStart.AddDate(0, 0, 7+off)
	default:
		if n, err := strconv.Atoi(op); err == nil {
			return weekStart.AddDate(0, 0, 7*n+off)
		}
	}
	return time.Time{}
}
