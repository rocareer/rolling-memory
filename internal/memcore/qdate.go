// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package memcore

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// TimeWindow represents a parsed query/assertion time window (1:1 shared between repository and service).
type TimeWindow struct {
	Years       []int32 `json:"years,omitempty"`
	Months      []int32 `json:"months,omitempty"`
	Days        []int32 `json:"days,omitempty"`
	Since       int64   `json:"since,omitempty"`
	Until       int64   `json:"until,omitempty"`
	MonthOfYear int     `json:"month_of_year,omitempty"`
	DayOfMonth  int     `json:"day_of_month,omitempty"`
	Anchor      int64   `json:"anchor,omitempty"`
	From        int64   `json:"from,omitempty"`
	To          int64   `json:"to,omitempty"`
	Label       string  `json:"label,omitempty"`
}

// Empty reports whether no dimension or timestamp bound is set on the window.
func (w TimeWindow) Empty() bool {
	return len(w.Years) == 0 && len(w.Months) == 0 && len(w.Days) == 0 && w.Since == 0 && w.Until == 0
}

// DateUTC formats unix seconds as YYYY-MM-DD in UTC (0 -> "").
func DateUTC(ts int64) string {
	if ts <= 0 {
		return ""
	}
	return time.Unix(ts, 0).UTC().Format("2006-01-02")
}

// DateTimeUTC formats unix seconds as YYYY-MM-DD (when at UTC midnight) or YYYY-MM-DD HH:MM:SS in UTC.
func DateTimeUTC(ts int64) string {
	if ts <= 0 {
		return ""
	}
	t := time.Unix(ts, 0).UTC()
	if ts%86400 == 0 {
		return t.Format("2006-01-02")
	}
	return t.Format("2006-01-02 15:04:05")
}

// DatePartsUTC extracts UTC (Year, Month, Day) from unix seconds.
func DatePartsUTC(ts int64) (p struct{ Year, Month, Day int }) {
	t := time.Unix(ts, 0).UTC()
	p.Year, p.Month, p.Day = t.Year(), int(t.Month()), t.Day()
	return p
}

// ISO formats unix seconds as UTC RFC3339 (0 -> "").
func ISO(ts int64) string {
	if ts <= 0 {
		return ""
	}
	return time.Unix(ts, 0).UTC().Format(time.RFC3339)
}

// monthNames 英文月份字面（全称 + 三字母缩写；形式层词表，非语义判定）。
var monthNames = map[string]int{
	"january": 1, "jan": 1, "february": 2, "feb": 2, "march": 3, "mar": 3,
	"april": 4, "apr": 4, "may": 5, "june": 6, "jun": 6, "july": 7, "jul": 7,
	"august": 8, "aug": 8, "september": 9, "sep": 9, "sept": 9, "october": 10, "oct": 10,
	"november": 11, "nov": 11, "december": 12, "dec": 12,
}

var (
	reFourDigitYear = regexp.MustCompile(`\b(19\d{2}|20\d{2})\b`)
	reCNYMD         = regexp.MustCompile(`(\d{4})\s*年\s*(\d{1,2})\s*月\s*(\d{1,2})\s*日?`)
	reCNYM          = regexp.MustCompile(`(\d{4})\s*年\s*(\d{1,2})\s*月`)
	reNumYMD        = regexp.MustCompile(`\b(19\d{2}|20\d{2})[-/.](\d{1,2})[-/.](\d{1,2})\b`)
	reENMonthDay    = regexp.MustCompile(`\b(?:january|jan|february|feb|march|mar|april|apr|may|june|jun|july|jul|august|aug|september|sept|sep|october|oct|november|nov|december|dec)\s+(\d{1,2})(?:st|nd|rd|th)?\b`)
	reENDayMonth    = regexp.MustCompile(`\b(\d{1,2})(?:st|nd|rd|th)?(?:\s+of)?\s+(?:january|jan|february|feb|march|mar|april|apr|may|june|jun|july|jul|august|aug|september|sept|sep|october|oct|november|nov|december|dec)\b`)
	reENDayRange    = regexp.MustCompile(`\b(?:between|from)\s+(?:january|jan|february|feb|march|mar|april|apr|may|june|jun|july|jul|august|aug|september|sept|sep|october|oct|november|nov|december|dec)\s+(\d{1,2})(?:st|nd|rd|th)?\s+(?:and|to)\s+(?:(?:january|jan|february|feb|march|mar|april|apr|may|june|jun|july|jul|august|aug|september|sept|sep|october|oct|november|nov|december|dec)\s+)?(\d{1,2})(?:st|nd|rd|th)?\b`)
	reRelPrevSeason = regexp.MustCompile(`\b(?:last\s+(?:year|summer|spring|winter|autumn|fall))\b|去年`)
)

// ParseDateWindow 从线索解析日期窗；ok=false 表无线索时间提及（逐字现状）。
func ParseDateWindow(query string) (TimeWindow, bool) {
	low := strings.ToLower(query)
	var w TimeWindow
	seenY := map[int32]bool{}
	seenM := map[int32]bool{}
	seenD := map[int32]bool{}
	addY := func(v int) {
		if v < 1900 || v > 2099 || seenY[int32(v)] {
			return
		}
		seenY[int32(v)] = true
		w.Years = append(w.Years, int32(v))
	}
	addM := func(v int) {
		if v < 1 || v > 12 || seenM[int32(v)] {
			return
		}
		seenM[int32(v)] = true
		w.Months = append(w.Months, int32(v))
	}
	addD := func(v int) {
		if v < 1 || v > 31 || seenD[int32(v)] {
			return
		}
		seenD[int32(v)] = true
		w.Days = append(w.Days, int32(v))
	}
	atoi := func(s string) int {
		v, _ := strconv.Atoi(s)
		return v
	}
	for _, m := range reCNYMD.FindAllStringSubmatch(low, -1) {
		addY(atoi(m[1]))
		addM(atoi(m[2]))
		addD(atoi(m[3]))
	}
	for _, m := range reNumYMD.FindAllStringSubmatch(low, -1) {
		addY(atoi(m[1]))
		addM(atoi(m[2]))
		addD(atoi(m[3]))
	}
	for _, m := range reCNYM.FindAllStringSubmatch(low, -1) {
		addY(atoi(m[1]))
		addM(atoi(m[2]))
	}
	for _, m := range reCNMD.FindAllStringSubmatch(low, -1) {
		addM(atoi(m[1]))
		addD(atoi(m[2]))
	}
	for _, m := range reCNM.FindAllStringSubmatch(low, -1) {
		addM(atoi(m[1]))
	}
	for _, f := range reWordTokens(low) {
		if m, ok := monthNames[f]; ok {
			addM(m)
		}
	}
	for _, m := range reFourDigitYear.FindAllString(low, -1) {
		addY(atoi(m))
	}
	bare := low
	for _, re := range []*regexp.Regexp{reCNYMD, reCNYM, reCNMD, reNumYMD, reCNM} {
		bare = re.ReplaceAllString(bare, " ")
	}
	if len(w.Months) > 0 {
		for _, m := range reENMonthDay.FindAllStringSubmatch(bare, -1) {
			addD(atoi(m[1]))
		}
		for _, m := range reENDayMonth.FindAllStringSubmatch(bare, -1) {
			addD(atoi(m[1]))
		}
		for _, m := range reENDayRange.FindAllStringSubmatch(bare, -1) {
			d1, d2 := atoi(m[1]), atoi(m[2])
			if d1 >= 1 && d2 >= d1 && d2 <= 31 && d2-d1 <= 14 {
				for d := d1; d <= d2; d++ {
					addD(d)
				}
			}
		}
	}
	for _, mw := range ParseTimeWindows(query) {
		if mw.Since <= 0 || mw.Until <= 0 || mw.Until-mw.Since > 59 {
			continue
		}
		if w.Since == 0 || mw.Since < w.Since {
			w.Since = mw.Since
		}
		if mw.Until > w.Until {
			w.Until = mw.Until
		}
	}
	if len(w.Years) == 1 {
		if reRelPrevSeason.MatchString(low) {
			addY(int(w.Years[0]) - 1)
		}
		var anchorDay time.Time
		if len(w.Months) == 1 && len(w.Days) == 1 {
			anchorDay = time.Date(int(w.Years[0]), time.Month(w.Months[0]), int(w.Days[0]), 0, 0, 0, 0, time.UTC)
		} else if len(w.Months) == 0 && len(w.Days) == 0 {
			anchorDay = time.Date(int(w.Years[0]), 7, 1, 0, 0, 0, 0, time.UTC)
		}
		if !anchorDay.IsZero() {
			for _, rw := range parseRelativeWindows(query, anchorDay) {
				if rw.Anchor <= 0 || rw.Anchor == anchorDay.Unix() {
					continue
				}
				ry, rm, rd := time.Unix(rw.Anchor, 0).UTC().Date()
				addY(ry)
				if len(w.Months) > 0 && int(rm) != int(w.Months[0]) {
					addM(int(rm))
					w.Days = nil
				} else if len(w.Days) > 0 && rd != int(w.Days[0]) {
					addD(rd)
				}
			}
		}
	}
	return w, len(w.Years) > 0 || len(w.Months) > 0 || w.Since > 0 || w.Until > 0
}

// ParseDateWindowAt 带参考时刻的日期窗解析。
func ParseDateWindowAt(query string, ref int64) (TimeWindow, int64, int64, bool) {
	if w, ok := ParseDateWindow(query); ok {
		return w, 0, 0, true
	}
	if ref <= 0 {
		return TimeWindow{}, 0, 0, false
	}
	refUTC := time.Unix(ref, 0).UTC()
	ws := ParseTimeWindowsAt(query, refUTC)
	var from, to int64
	okRel := false
	for _, w := range ws {
		if w.Since <= 0 && w.Until <= 0 {
			continue
		}
		f, u := w.Since, w.Until
		if f <= 0 {
			f = u
		}
		if u <= 0 {
			u = f
		}
		if !okRel || f < from {
			from = f
		}
		if !okRel || u > to {
			to = u
		}
		okRel = true
	}
	if !okRel {
		if a, kind := ParseTimeExprAt(query, refUTC); a > 0 && kind == "relative_resolved" {
			if a%86400 != 0 {
				return TimeWindow{}, a - 60, a + 60, true
			}
			return TimeWindow{}, a, a + 86400 - 1, true
		}
		return TimeWindow{}, 0, 0, false
	}
	return TimeWindow{}, from, to, true
}

func epochInWindow(tv, from, to int64) bool {
	if tv <= 0 {
		return false
	}
	if from > 0 && tv < from {
		return false
	}
	if to > 0 && tv > to {
		return false
	}
	return true
}

// DateWindowBonus 行时间分。
func DateWindowBonus(w TimeWindow, tValidFrom int64) float64 {
	if tValidFrom <= 0 {
		return 0
	}
	if w.Since > 0 || w.Until > 0 {
		if epochInWindow(tValidFrom, w.Since, w.Until) {
			return 2
		}
		return 0
	}
	tm := DatePartsUTC(tValidFrom)
	if len(w.Years) > 0 && !int32In(w.Years, int32(tm.Year)) {
		return 0
	}
	if len(w.Months) > 0 && !int32In(w.Months, int32(tm.Month)) {
		return 0
	}
	if len(w.Days) > 0 && !int32In(w.Days, int32(tm.Day)) {
		return 0
	}
	if len(w.Days) > 0 {
		return 3.0
	}
	if len(w.Years) > 0 && len(w.Months) > 0 {
		return 2.5
	}
	return 1.0
}

func reWordTokens(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		return (r < 'a' || r > 'z') && r != '\'' && (r < '0' || r > '9')
	})
}

func int32In(xs []int32, v int32) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

// ExtractAnchorSentence extracts the full sentence in turnText covering [anchorStart, anchorEnd)
// (supporting both ASCII and CJK full-width sentence punctuation) so leading temporal adverbs
// in the same sentence can be resolved deterministically.
func ExtractAnchorSentence(turnText string, anchorStart, anchorEnd int) string {
	if turnText == "" || anchorStart < 0 || anchorEnd <= anchorStart || anchorEnd > len(turnText) {
		return ""
	}
	isSentBreak := func(b byte) bool {
		switch b {
		case '.', '!', '?', ';', '\n', '\r':
			return true
		}
		return false
	}
	start := anchorStart
	for start > 0 {
		if isSentBreak(turnText[start-1]) {
			break
		}
		if start >= 3 {
			prev3 := turnText[start-3 : start]
			if prev3 == "。" || prev3 == "！" || prev3 == "？" || prev3 == "；" {
				break
			}
		}
		start--
	}
	end := anchorEnd
	for end < len(turnText) {
		if isSentBreak(turnText[end]) {
			end++
			break
		}
		if end+3 <= len(turnText) {
			next3 := turnText[end : end+3]
			if next3 == "。" || next3 == "！" || next3 == "？" || next3 == "；" {
				end += 3
				break
			}
		}
		end++
	}
	return strings.TrimSpace(turnText[start:end])
}

// PickTimeAnchorKind selects the resolved anchor timestamp from a slice of windows and classifies
// whether it came from the explicit prefix [0..absN) or the relative suffix.
func PickTimeAnchorKind(wins []TimeWindow, absN int) (int64, string) {
	var anchor int64
	kind := ""
	allAbs := absN > 0 && len(wins) == absN
	for i, w := range wins {
		if w.Anchor <= 0 {
			continue
		}
		if allAbs {
			return w.Anchor, "explicit"
		}
		anchor = w.Anchor
		if i < absN {
			kind = "explicit"
		} else {
			kind = "relative_resolved"
		}
	}
	if anchor <= 0 {
		return 0, ""
	}
	return anchor, kind
}

func pickTimeAnchorKind(wins []TimeWindow, absN int) (int64, string) {
	return PickTimeAnchorKind(wins, absN)
}

// PickTimeAnchor returns the last non-zero Anchor from wins.
func PickTimeAnchor(wins []TimeWindow) int64 {
	var anchor int64
	for _, w := range wins {
		if w.Anchor > 0 {
			anchor = w.Anchor
		}
	}
	return anchor
}

// Exported helpers for cross-package test & service delegation.
func TwDay(y, m, d int) TimeWindow { return twDay(y, m, d) }
func TwMonth(y, m int) TimeWindow  { return twMonth(y, m) }
func TwYear(y int) TimeWindow      { return twYear(y) }
func TwClock(y, m, d, h, mi, s int, hasSec bool) TimeWindow {
	return twClock(y, m, d, h, mi, s, hasSec)
}
func PickRelAnchor(abs []TimeWindow, loc *time.Location) (time.Time, bool) {
	return pickRelAnchor(abs, loc)
}
func EpochInWindow(tv, from, to int64) bool { return epochInWindow(tv, from, to) }
