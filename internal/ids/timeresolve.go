// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package ids

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Extended time DSL and natural-language temporal resolution rules.
var (
	dslRangeRE     = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})\s*(?:\.\.|~)\s*(\d{4})-(\d{2})-(\d{2})$`)
	dslMonthRE     = regexp.MustCompile(`^(\d{4})-(\d{2})$`)
	dslTenDayRE    = regexp.MustCompile(`^(\d{4})-(\d{1,2})-(early|mid|late|上旬|中旬|下旬)$`)
	dslYearPhaseRE = regexp.MustCompile(`^(\d{4})-(early|mid|late|end)$`)
	dslRelYearRE   = regexp.MustCompile(`^(last|this|next):year-(early|mid|late|end)$`)
	dslQuarterRE   = regexp.MustCompile(`^(\d{4})-q([1-4])$`)
	dslSeasonRE    = regexp.MustCompile(`^(\d{4})-(spring|summer|autumn|fall|winter|春|夏|秋|冬)$`)
	dslOffsetRE    = regexp.MustCompile(`^(?:(\d{4})-(\d{2})-(\d{2})\s+)?([+-]\d{1,3})\s*(d|w|m|y|days?|weeks?|months?|years?)$`)
	dslRelSeasonRE = regexp.MustCompile(`^(last|this|next):(spring|summer|autumn|fall|winter|q[1-4])$`)

	cnDurationRE   = regexp.MustCompile(`(?:学|玩|练|住|做|干|待|工作|持续|已经|有)?了?(?:快|大概|大约|将近|差不多|约)?\s*([0-9０-９]{1,2}|[一二两三四五六七八九十]{1,3})\s*(?:个)?(月|年)(?:了|左右)?`)
	enTenDayMYRE   = regexp.MustCompile(`\b(early|mid|middle\s+of|late)[\s-]+(january|february|march|april|may|june|july|august|september|october|november|december|jan|feb|mar|apr|jun|jul|aug|sept|sep|oct|nov|dec)(?:,?\s+(\d{4}))?\b`)
	cnTenDayRE     = regexp.MustCompile(`(?:(\d{4})年|(去年|今年|明年|前年|后年))?\s*([0-9０-９]{1,2}|[一二三四五六七八九十]{1,2})月(上旬|中旬|下旬|月初|月中|月底|月末)`)
	enSeasonYearRE = regexp.MustCompile(`\b(spring|summer|autumn|fall|winter)\s+(?:of\s+)?(\d{4})\b`)
	enRelSeasonRE  = regexp.MustCompile(`\b(last|this|next)\s+(spring|summer|autumn|fall|winter)\b`)
	cnSeasonRE     = regexp.MustCompile(`(?:(\d{4})年|(去年|今年|明年|前年|后年))(?:的)?(春天|夏天|秋天|冬天|春季|夏季|秋季|冬季|春|夏|秋|冬)`)
	enVagueAgoRE   = regexp.MustCompile(`\b(a\s+few|several)\s+(days?|weeks?|months?|years?)\s+ago\b`)
	cnVagueAgoRE   = regexp.MustCompile(`(?:好几|几)(?:个)?(天|日|周|星期|礼拜|月|年)(?:以)?前`)
)

func parseExtendedTime(raw string, today time.Time) (int64, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, false
	}
	low := strings.ToLower(s)

	if m := dslRangeRE.FindStringSubmatch(low); m != nil {
		y, _ := strconv.Atoi(m[1])
		mo, _ := strconv.Atoi(m[2])
		d, _ := strconv.Atoi(m[3])
		if parsed, ok := dateFromYMD(y, mo, d); ok {
			return localMidnight(parsed), true
		}
	}
	if m := dslMonthRE.FindStringSubmatch(low); m != nil {
		y, _ := strconv.Atoi(m[1])
		mo, _ := strconv.Atoi(m[2])
		if y >= 1900 && y <= 2099 && mo >= 1 && mo <= 12 {
			return localMidnight(time.Date(y, time.Month(mo), 1, 0, 0, 0, 0, time.Local)), true
		}
	}
	if m := dslTenDayRE.FindStringSubmatch(low); m != nil {
		y, _ := strconv.Atoi(m[1])
		mo, _ := strconv.Atoi(m[2])
		if y >= 1900 && y <= 2099 && mo >= 1 && mo <= 12 {
			return localMidnight(time.Date(y, time.Month(mo), tenDayStart(m[3]), 0, 0, 0, 0, time.Local)), true
		}
	}
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
			return localMidnight(time.Date(y, time.Month(mo), 1, 0, 0, 0, 0, time.Local)), true
		}
	}
	if m := dslRelYearRE.FindStringSubmatch(low); m != nil {
		y := today.Year()
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
		return localMidnight(time.Date(y, time.Month(mo), 1, 0, 0, 0, 0, time.Local)), true
	}
	if m := dslQuarterRE.FindStringSubmatch(low); m != nil {
		y, _ := strconv.Atoi(m[1])
		q, _ := strconv.Atoi(m[2])
		if y >= 1900 && y <= 2099 && q >= 1 && q <= 4 {
			return localMidnight(time.Date(y, time.Month((q-1)*3+1), 1, 0, 0, 0, 0, time.Local)), true
		}
	}
	if m := dslSeasonRE.FindStringSubmatch(low); m != nil {
		y, _ := strconv.Atoi(m[1])
		if y >= 1900 && y <= 2099 {
			if mo := seasonStartMonth(m[2]); mo > 0 {
				return localMidnight(time.Date(y, time.Month(mo), 1, 0, 0, 0, 0, time.Local)), true
			}
		}
	}
	if m := dslOffsetRE.FindStringSubmatch(low); m != nil {
		base := today
		if m[1] != "" {
			y, _ := strconv.Atoi(m[1])
			mo, _ := strconv.Atoi(m[2])
			d, _ := strconv.Atoi(m[3])
			parsed, ok := dateFromYMD(y, mo, d)
			if !ok {
				return 0, false
			}
			base = parsed
		}
		delta, err := strconv.Atoi(m[4])
		if err != nil {
			return 0, false
		}
		switch m[5][:1] {
		case "d":
			return localMidnight(base.AddDate(0, 0, delta)), true
		case "w":
			return localMidnight(base.AddDate(0, 0, 7*delta)), true
		case "m":
			return localMidnight(time.Date(base.Year(), base.Month()+time.Month(delta), 1, 0, 0, 0, 0, time.Local)), true
		case "y":
			return localMidnight(time.Date(base.Year()+delta, 1, 1, 0, 0, 0, 0, time.Local)), true
		}
	}
	if m := dslRelSeasonRE.FindStringSubmatch(low); m != nil {
		y := today.Year()
		switch m[1] {
		case "last":
			y--
		case "next":
			y++
		}
		target := m[2]
		if strings.HasPrefix(target, "q") {
			q, _ := strconv.Atoi(target[1:])
			return localMidnight(time.Date(y, time.Month((q-1)*3+1), 1, 0, 0, 0, 0, time.Local)), true
		}
		if mo := seasonStartMonth(target); mo > 0 {
			return localMidnight(time.Date(y, time.Month(mo), 1, 0, 0, 0, 0, time.Local)), true
		}
	}

	if m := enTenDayMYRE.FindStringSubmatch(low); m != nil {
		mo := enMonthShortOrFull(m[2])
		d := tenDayStart(m[1])
		if mo > 0 && m[3] != "" {
			y, _ := strconv.Atoi(m[3])
			return localMidnight(time.Date(y, time.Month(mo), d, 0, 0, 0, 0, time.Local)), true
		}
	}
	if m := cnTenDayRE.FindStringSubmatch(s); m != nil {
		mo := parseCNNum(m[3])
		d := tenDayStart(m[4])
		if mo >= 1 && mo <= 12 {
			if m[1] != "" {
				y, _ := strconv.Atoi(m[1])
				return localMidnight(time.Date(y, time.Month(mo), d, 0, 0, 0, 0, time.Local)), true
			}
			if m[2] != "" {
				delta := map[string]int{"去年": -1, "今年": 0, "明年": 1, "前年": -2, "后年": 2}[m[2]]
				return localMidnight(time.Date(today.Year()+delta, time.Month(mo), d, 0, 0, 0, 0, time.Local)), true
			}
		}
	}
	if m := enSeasonYearRE.FindStringSubmatch(low); m != nil {
		y, _ := strconv.Atoi(m[2])
		if mo := seasonStartMonth(m[1]); mo > 0 && y >= 1900 && y <= 2099 {
			return localMidnight(time.Date(y, time.Month(mo), 1, 0, 0, 0, 0, time.Local)), true
		}
	}
	if m := enRelSeasonRE.FindStringSubmatch(low); m != nil {
		y := today.Year()
		switch m[1] {
		case "last":
			y--
		case "next":
			y++
		}
		if mo := seasonStartMonth(m[2]); mo > 0 {
			return localMidnight(time.Date(y, time.Month(mo), 1, 0, 0, 0, 0, time.Local)), true
		}
	}
	if m := cnSeasonRE.FindStringSubmatch(s); m != nil {
		mo := seasonStartMonth(m[3])
		if mo > 0 {
			if m[1] != "" {
				y, _ := strconv.Atoi(m[1])
				return localMidnight(time.Date(y, time.Month(mo), 1, 0, 0, 0, 0, time.Local)), true
			}
			if m[2] != "" {
				delta := map[string]int{"去年": -1, "今年": 0, "明年": 1, "前年": -2, "后年": 2}[m[2]]
				return localMidnight(time.Date(today.Year()+delta, time.Month(mo), 1, 0, 0, 0, 0, time.Local)), true
			}
		}
	}
	if m := cnDurationRE.FindStringSubmatch(s); m != nil {
		if n := parseCNNum(m[1]); n > 0 {
			if m[2] == "月" {
				return localMidnight(time.Date(today.Year(), today.Month()-time.Month(n), 1, 0, 0, 0, 0, time.Local)), true
			}
			if m[2] == "年" {
				return localMidnight(time.Date(today.Year()-n, 1, 1, 0, 0, 0, 0, time.Local)), true
			}
		}
	}
	if m := enVagueAgoRE.FindStringSubmatch(low); m != nil {
		const n = 3
		unit := m[2]
		switch {
		case strings.HasPrefix(unit, "day"):
			return localMidnight(today.AddDate(0, 0, -n)), true
		case strings.HasPrefix(unit, "week"):
			return localMidnight(today.AddDate(0, 0, -7*n)), true
		case strings.HasPrefix(unit, "month"):
			return localMidnight(time.Date(today.Year(), today.Month()-time.Month(n), 1, 0, 0, 0, 0, time.Local)), true
		case strings.HasPrefix(unit, "year"):
			return localMidnight(time.Date(today.Year()-n, 1, 1, 0, 0, 0, 0, time.Local)), true
		}
	}
	if m := cnVagueAgoRE.FindStringSubmatch(s); m != nil {
		const n = 3
		switch m[1] {
		case "天", "日":
			return localMidnight(today.AddDate(0, 0, -n)), true
		case "周", "星期", "礼拜":
			return localMidnight(today.AddDate(0, 0, -7*n)), true
		case "月":
			return localMidnight(time.Date(today.Year(), today.Month()-time.Month(n), 1, 0, 0, 0, 0, time.Local)), true
		case "年":
			return localMidnight(time.Date(today.Year()-n, 1, 1, 0, 0, 0, 0, time.Local)), true
		}
	}
	return 0, false
}

func tenDayStart(tag string) int {
	switch strings.ToLower(strings.TrimSpace(tag)) {
	case "mid", "middle of", "中旬", "月中":
		return 11
	case "late", "下旬", "月底", "月末":
		return 21
	default:
		return 1
	}
}

func seasonStartMonth(s string) int {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "spring", "春", "春天", "春季":
		return 3
	case "summer", "夏", "夏天", "夏季":
		return 6
	case "autumn", "fall", "秋", "秋天", "秋季":
		return 9
	case "winter", "冬", "冬天", "冬季":
		return 12
	}
	return 0
}

func enMonthShortOrFull(s string) int {
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

func parseCNNum(s string) int {
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
		if r == '十' {
			if cur == 0 {
				cur = 1
			}
			total += cur * 10
			cur = 0
			continue
		}
		if d, ok := digits[r]; ok {
			cur = cur*10 + d
		}
	}
	return total + cur
}
