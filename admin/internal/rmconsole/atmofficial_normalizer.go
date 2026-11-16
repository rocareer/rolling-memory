// Code generated from internal/eval/atmofficial_normalizer.go. DO NOT EDIT.
package rollingmemory

// ATM-Bench official deterministic evaluator — normalizer port.
//
// This file is a faithful Go port of the upstream evaluator at
// memqa/utils/evaluator/normalizer.py (JingbiaoMei/ATM-Bench, fetched 2026-11-09).
// It is a protocol adapter for an external benchmark metric, NOT memory-engine
// semantics: it intentionally contains the upstream month/currency/abstention
// vocabularies because the published metric is defined in those terms.
// It must stay isolated from retrieval code and is pinned by
// ATMOfficialScoringFixtureSHA256 + the golden fixture parity test.

import (
	"fmt"
	"math/big"
	"regexp"
	"sort"
	"strings"
	"time"
)

const atmMonthPattern = "january|jan|february|feb|march|mar|april|apr|may|june|jun|july|jul|august|aug|september|sep|sept|october|oct|november|nov|december|dec"

var atmMonths = map[string]int{
	"january": 1, "jan": 1, "february": 2, "feb": 2,
	"march": 3, "mar": 3, "april": 4, "apr": 4,
	"may": 5, "june": 6, "jun": 6, "july": 7, "jul": 7,
	"august": 8, "aug": 8, "september": 9, "sep": 9, "sept": 9,
	"october": 10, "oct": 10, "november": 11, "nov": 11,
	"december": 12, "dec": 12,
}

var atmNumberWords = map[string]int{
	"zero": 0, "one": 1, "two": 2, "three": 3, "four": 4,
	"five": 5, "six": 6, "seven": 7, "eight": 8, "nine": 9,
	"ten": 10, "eleven": 11, "twelve": 12, "thirteen": 13,
	"fourteen": 14, "fifteen": 15, "sixteen": 16, "seventeen": 17,
	"eighteen": 18, "nineteen": 19, "twenty": 20,
}

var atmStopwords = map[string]struct{}{
	"the": {}, "a": {}, "an": {}, "of": {}, "in": {}, "at": {}, "on": {},
	"for": {}, "to": {}, "and": {}, "or": {}, "by": {}, "from": {},
	"with": {}, "into": {}, "is": {}, "was": {}, "were": {}, "be": {},
	"been": {}, "being": {},
}

var atmAddressKeywords = map[string]struct{}{
	"street": {}, "st": {}, "road": {}, "rd": {}, "lane": {}, "ln": {},
	"avenue": {}, "ave": {}, "drive": {}, "dr": {}, "place": {}, "pl": {},
	"square": {}, "sq": {}, "close": {}, "court": {}, "ct": {}, "way": {},
	"high": {}, "highstreet": {}, "boulevard": {}, "blvd": {}, "city": {},
	"town": {}, "village": {}, "county": {}, "country": {}, "oxford": {},
	"cambridge": {},
}

var atmAbstentionPhrases = []string{
	"unknown", "abstention", "no information", "not available",
	"no evidence", "no evidnece", "insufficient information",
}

var atmDayPatternWords = []string{
	"monday", "mon", "tuesday", "tue", "tues", "wednesday", "wed",
	"thursday", "thu", "thurs", "friday", "fri", "saturday", "sat",
	"sunday", "sun",
}

var (
	reATMISO            = regexp.MustCompile(`\b(\d{4})[-/](\d{1,2})[-/](\d{1,2})\b`)
	reATMCompactDate    = regexp.MustCompile(`\b((19|20)\d{2})(0[1-9]|1[0-2])(0[1-9]|[12]\d|3[01])\b`)
	reATMMonthDay       = regexp.MustCompile(`(?i)\b(` + atmMonthPattern + `)\s+(\d{1,2})(st|nd|rd|th)?(,?\s*(\d{4}))?\b`)
	reATMDayMonth       = regexp.MustCompile(`(?i)\b(\d{1,2})(st|nd|rd|th)?\s+(` + atmMonthPattern + `)(,?\s*(\d{4}))?\b`)
	reATMMonthRange     = regexp.MustCompile(`(?i)\b(` + atmMonthPattern + `)\s+(\d{1,2})(st|nd|rd|th)?\s*(-|to|–)\s*(\d{1,2})(st|nd|rd|th)?(,?\s*(\d{4}))?\b`)
	reATMTimeRange      = regexp.MustCompile(`(?i)\b(\d{1,2})(:(\d{2}))?\s*(am|pm)?\s*(-|to|–|and)\s*(\d{1,2})(:(\d{2}))?\s*(am|pm)?\b`)
	reATMTimeAMPM       = regexp.MustCompile(`(?i)\b(\d{1,2})(:(\d{2}))?\s*(am|pm)\b`)
	reATMTime24         = regexp.MustCompile(`\b([01]?\d|2[0-3]):([0-5]\d)\b`)
	reATMTimeAMPMDots   = regexp.MustCompile(`(?i)\b(\d{1,2})(:(\d{2}))?\s*(a\.m\.|p\.m\.)\b`)
	reATMCurrencySym    = regexp.MustCompile(`[£$€]`)
	reATMCurrencyCode   = regexp.MustCompile(`(?i)\b(USD|GBP|EUR|AUD|JOD)\b`)
	reATMCurrencyWords  = regexp.MustCompile(`(?i)\b(dollars?|usd|pounds?|gbp|euros?|eur)\b`)
	reATMNumber         = regexp.MustCompile(`\b\d{1,3}(,\d{3})*(\.\d+)?\b`)
	reATMCode           = regexp.MustCompile(`(?i)\b[A-Z0-9]{4,}\b`)
	reATMCurrencyPre    = regexp.MustCompile(`(?P<symbol>[£$€])\s*(?P<number>\d{1,3}(,\d{3})*(\.\d+)?)`)
	reATMCurrencySuf    = regexp.MustCompile(`(?i)(?P<number>\d{1,3}(,\d{3})*(\.\d+)?)\s*(?P<code>USD|GBP|EUR|AUD|JOD|dollars?|pounds?|euros?)\b`)
	reATMListSplit      = regexp.MustCompile(`(?i)\s*(,|;|\band\b|/)\s*`)
	reATMToken          = regexp.MustCompile(`(?i)[a-z0-9]+`)
	reATMDayPattern     = regexp.MustCompile(`(?i)\b(` + strings.Join(atmDayPatternWords, "|") + `)\b`)
	reATMNumberWords    = regexp.MustCompile(`(?i)\b(zero|one|two|three|four|five|six|seven|eight|nine|ten|eleven|twelve|thirteen|fourteen|fifteen|sixteen|seventeen|eighteen|nineteen|twenty)\b`)
	reATMSpaces         = regexp.MustCompile(`\s+`)
	reATMCommaNumber    = regexp.MustCompile(`(\d),(\d)`)
	reATMOrdinal        = regexp.MustCompile(`\b(\d+)(st|nd|rd|th)\b`)
	reATMParen          = regexp.MustCompile(`\([^)]+\)`)
	reATMTrailingUnit   = regexp.MustCompile(`(?i)\b(\d+)\s+(spots?|hours?|sessions?|days?)\b`)
	reATMBetween        = regexp.MustCompile(`(?i)\bbetween\s+`)
	reATMAndTo          = regexp.MustCompile(`(?i)\s+and\s+`)
	reATMLeadingArticle = regexp.MustCompile(`(?i)^\s*(a|an|the)\s+`)
	reATMCodePrefix     = regexp.MustCompile(`(?i)\b(CODE|ID|REFERENCE|REF|NUMBER|NUM|LABEL):\s*`)
)

var atmContextPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bit happened on\b`),
	regexp.MustCompile(`(?i)\bstarting at\b`),
	regexp.MustCompile(`(?i)\bfrom\s+\d`),
	regexp.MustCompile(`(?i)\bonwards\b`),
	regexp.MustCompile(`(?i)\btotaling\b`),
	regexp.MustCompile(`(?i)\bcomprising\b`),
	regexp.MustCompile(`(?i)\bwith pre-drinks at\b`),
	regexp.MustCompile(`(?i)\bwith pre-\w+\b`),
	regexp.MustCompile(`(?i)\band dinner at\b`),
	regexp.MustCompile(`(?i)\bdress code is\b`),
	regexp.MustCompile(`(?i)\bthe total cost was\b`),
	regexp.MustCompile(`(?i)\bcode:\s*`),
	regexp.MustCompile(`(?i)\bexpires?\s*`),
}

type atmDecimal struct {
	rat *big.Rat
}

func parseATMDecimal(raw string) (atmDecimal, bool) {
	s := strings.ReplaceAll(strings.TrimSpace(raw), ",", "")
	if s == "" {
		return atmDecimal{}, false
	}
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return atmDecimal{}, false
	}
	return atmDecimal{rat: r}, true
}

func (d atmDecimal) key() string {
	if d.rat == nil {
		return ""
	}
	return d.rat.RatString()
}

func dedupeATMStrings(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

func dedupeATMDecimals(in []atmDecimal) []atmDecimal {
	seen := make(map[string]struct{}, len(in))
	out := make([]atmDecimal, 0, len(in))
	for _, d := range in {
		k := d.key()
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, d)
	}
	return out
}

func atmCollapse(s string) string {
	return strings.TrimSpace(reATMSpaces.ReplaceAllString(s, " "))
}

func atmAggressivePreprocess(text string) string {
	cleaned := strings.TrimRight(text, ".")
	cleaned = reATMCommaNumber.ReplaceAllString(cleaned, "$1$2")
	cleaned = reATMTrailingUnit.ReplaceAllString(cleaned, "$1")
	return cleaned
}

func atmNormalizeText(text string) string {
	cleaned := strings.ToLower(strings.TrimSpace(text))
	cleaned = reATMSpaces.ReplaceAllString(cleaned, " ")
	cleaned = reATMOrdinal.ReplaceAllString(cleaned, "$1")
	cleaned = reATMParen.ReplaceAllString(cleaned, "")
	cleaned = reATMSpaces.ReplaceAllString(cleaned, " ")
	cleaned = reATMCommaNumber.ReplaceAllString(cleaned, "$1$2")
	return strings.Trim(cleaned, "\t\n\r \"'`.,;:!?()[]{}")
}

func atmStripParentheticalDetails(text string) string {
	cleaned := reATMParen.ReplaceAllString(text, "")
	return atmCollapse(cleaned)
}

func atmStripCurrencyBreakdowns(text string) string {
	cleaned := text
	cleaned = regexp.MustCompile(`(?i),?\s*totaling\s+[£$€]\d+(\.\d+)?\s+for\s+\w+\s+sessions?`).ReplaceAllString(cleaned, "")
	cleaned = regexp.MustCompile(`(?i),?\s*comprising\s+.*$`).ReplaceAllString(cleaned, "")
	cleaned = regexp.MustCompile(`(?i)per\s+\d+[- ]minute\s+session`).ReplaceAllString(cleaned, "per session")
	return atmCollapse(cleaned)
}

func atmNormalizeBetweenToRange(text string) string {
	cleaned := reATMBetween.ReplaceAllString(text, "")
	return reATMAndTo.ReplaceAllString(cleaned, " to ")
}

func atmStripLeadingArticles(text string) string {
	return reATMLeadingArticle.ReplaceAllString(text, "")
}

func atmStripContextPhrases(text string) string {
	cleaned := strings.ToLower(text)
	for _, re := range atmContextPatterns {
		cleaned = re.ReplaceAllString(cleaned, " ")
	}
	cleaned = reATMDayPattern.ReplaceAllString(cleaned, " ")
	return atmCollapse(cleaned)
}

func atmIsAbstention(text string) bool {
	normalized := atmNormalizeText(text)
	for _, phrase := range atmAbstentionPhrases {
		if strings.Contains(normalized, phrase) {
			return true
		}
	}
	return false
}

func atmFormatDate(year *int, month, day int) string {
	if year != nil && *year > 0 {
		return fmt.Sprintf("%04d%02d%02d", *year, month, day)
	}
	return fmt.Sprintf("%02d%02d", month, day)
}

func atmFormatRange(year *int, month, startDay, endDay int) string {
	if year != nil && *year > 0 {
		return fmt.Sprintf("%04d%02d%02d-%04d%02d%02d", *year, month, startDay, *year, month, endDay)
	}
	return fmt.Sprintf("%02d%02d-%02d%02d", month, startDay, month, endDay)
}

func atmMonthNumber(name string) (int, bool) {
	v, ok := atmMonths[strings.ToLower(name)]
	return v, ok
}

func atmAtoi(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}

func atmOptionalYear(m []string, idx int) *int {
	if idx >= len(m) || strings.TrimSpace(m[idx]) == "" {
		return nil
	}
	y := atmAtoi(strings.TrimSpace(m[idx]))
	if y == 0 {
		return nil
	}
	return &y
}

func atmExtractDates(text string) []string {
	var tokens []string
	for _, m := range reATMISO.FindAllStringSubmatch(text, -1) {
		y, mo, d := atmAtoi(m[1]), atmAtoi(m[2]), atmAtoi(m[3])
		tokens = append(tokens, atmFormatDate(&y, mo, d))
	}
	for _, m := range reATMCompactDate.FindAllStringSubmatch(text, -1) {
		y, mo, d := atmAtoi(m[1]), atmAtoi(m[3]), atmAtoi(m[4])
		tokens = append(tokens, atmFormatDate(&y, mo, d))
	}
	for _, m := range reATMMonthRange.FindAllStringSubmatch(text, -1) {
		mo, ok := atmMonthNumber(m[1])
		if !ok {
			continue
		}
		start, end := atmAtoi(m[2]), atmAtoi(m[5])
		tokens = append(tokens, atmFormatRange(atmOptionalYear(m, 8), mo, start, end))
	}
	withoutRanges := reATMMonthRange.ReplaceAllString(text, " ")
	for _, m := range reATMMonthDay.FindAllStringSubmatch(withoutRanges, -1) {
		mo, ok := atmMonthNumber(m[1])
		if !ok {
			continue
		}
		tokens = append(tokens, atmFormatDate(atmOptionalYear(m, 5), mo, atmAtoi(m[2])))
	}
	for _, m := range reATMDayMonth.FindAllStringSubmatch(withoutRanges, -1) {
		mo, ok := atmMonthNumber(m[3])
		if !ok {
			continue
		}
		tokens = append(tokens, atmFormatDate(atmOptionalYear(m, 5), mo, atmAtoi(m[1])))
	}
	return dedupeATMStrings(tokens)
}

func atmNormalizeTime(hour, minute int, ampm string) string {
	if ampm != "" {
		ampm = strings.ToLower(ampm)
		if hour == 12 {
			hour = 0
		}
		if ampm == "pm" {
			hour += 12
		}
	}
	return fmt.Sprintf("%02d:%02d", hour, minute)
}

func atmExtractTimes(text string) []string {
	var tokens []string
	normalized := strings.ToLower(text)
	normalized = strings.ReplaceAll(normalized, "noon", "12:00")
	normalized = strings.ReplaceAll(normalized, "midday", "12:00")
	normalized = strings.ReplaceAll(normalized, "midnight", "00:00")
	normalized = strings.ReplaceAll(normalized, "a.m.", "am")
	normalized = strings.ReplaceAll(normalized, "p.m.", "pm")
	normalized = strings.ReplaceAll(normalized, "a. m.", "am")
	normalized = strings.ReplaceAll(normalized, "p. m.", "pm")
	normalized = reATMTimeAMPMDots.ReplaceAllStringFunc(normalized, func(s string) string {
		m := reATMTimeAMPMDots.FindStringSubmatch(s)
		if len(m) < 5 {
			return s
		}
		minute := m[3]
		if minute == "" {
			minute = "00"
		}
		ampm := strings.ToLower(m[4])
		if ampm != "" {
			ampm = ampm[:1] + "m"
		}
		return m[1] + ":" + minute + ampm
	})
	for _, m := range reATMTimeRange.FindAllStringSubmatch(normalized, -1) {
		startHour := atmAtoi(m[1])
		startMin := atmAtoi(m[3])
		startAMPM := m[4]
		endHour := atmAtoi(m[6])
		endMin := atmAtoi(m[8])
		endAMPM := m[9]
		if startAMPM == "" && endAMPM != "" {
			startAMPM = endAMPM
		}
		start := atmNormalizeTime(startHour, startMin, startAMPM)
		end := atmNormalizeTime(endHour, endMin, endAMPM)
		tokens = append(tokens, start+"-"+end)
	}
	withoutRanges := reATMTimeRange.ReplaceAllString(normalized, " ")
	for _, m := range reATMTime24.FindAllStringSubmatch(withoutRanges, -1) {
		tokens = append(tokens, atmNormalizeTime(atmAtoi(m[1]), atmAtoi(m[2]), ""))
	}
	for _, m := range reATMTimeAMPM.FindAllStringSubmatch(withoutRanges, -1) {
		tokens = append(tokens, atmNormalizeTime(atmAtoi(m[1]), atmAtoi(m[3]), m[4]))
	}
	return dedupeATMStrings(tokens)
}

func atmRemoveDateTimeText(text string) string {
	cleaned := reATMISO.ReplaceAllString(text, " ")
	cleaned = reATMCompactDate.ReplaceAllString(cleaned, " ")
	cleaned = reATMMonthRange.ReplaceAllString(cleaned, " ")
	cleaned = reATMMonthDay.ReplaceAllString(cleaned, " ")
	cleaned = reATMDayMonth.ReplaceAllString(cleaned, " ")
	cleaned = reATMTimeRange.ReplaceAllString(cleaned, " ")
	cleaned = reATMTime24.ReplaceAllString(cleaned, " ")
	cleaned = reATMTimeAMPM.ReplaceAllString(cleaned, " ")
	cleaned = reATMTimeAMPMDots.ReplaceAllString(cleaned, " ")
	cleaned = strings.ReplaceAll(cleaned, "noon", " ")
	cleaned = strings.ReplaceAll(cleaned, "midday", " ")
	cleaned = strings.ReplaceAll(cleaned, "midnight", " ")
	cleaned = strings.ReplaceAll(cleaned, "a.m.", " ")
	cleaned = strings.ReplaceAll(cleaned, "p.m.", " ")
	cleaned = strings.ReplaceAll(cleaned, "a. m.", " ")
	cleaned = strings.ReplaceAll(cleaned, "p. m.", " ")
	return cleaned
}

func atmReplaceNumberWords(text string) string {
	return reATMNumberWords.ReplaceAllStringFunc(text, func(word string) string {
		if v, ok := atmNumberWords[strings.ToLower(word)]; ok {
			return fmt.Sprintf("%d", v)
		}
		return word
	})
}

func atmExtractCurrencyAmounts(text string) []atmDecimal {
	var amounts []atmDecimal
	normalized := atmReplaceNumberWords(text)
	normalized = reATMCommaNumber.ReplaceAllString(normalized, "$1$2")
	for _, m := range reATMCurrencyPre.FindAllStringSubmatch(normalized, -1) {
		if d, ok := parseATMDecimal(m[reATMCurrencyPre.SubexpIndex("number")]); ok {
			amounts = append(amounts, d)
		}
	}
	for _, m := range reATMCurrencySuf.FindAllStringSubmatch(normalized, -1) {
		if d, ok := parseATMDecimal(m[reATMCurrencySuf.SubexpIndex("number")]); ok {
			amounts = append(amounts, d)
		}
	}
	return dedupeATMDecimals(amounts)
}

func atmExtractNumbers(text string) ([]atmDecimal, []string) {
	var values []atmDecimal
	var currencies []string
	for _, m := range reATMCurrencyCode.FindAllStringSubmatch(text, -1) {
		currencies = append(currencies, strings.ToUpper(m[1]))
	}
	for _, m := range reATMCurrencyWords.FindAllStringSubmatch(text, -1) {
		word := strings.ToLower(m[1])
		switch {
		case strings.Contains(word, "usd") || strings.Contains(word, "dollar"):
			currencies = append(currencies, "USD")
		case strings.Contains(word, "gbp") || strings.Contains(word, "pound"):
			currencies = append(currencies, "GBP")
		case strings.Contains(word, "eur") || strings.Contains(word, "euro"):
			currencies = append(currencies, "EUR")
		}
	}
	for _, m := range reATMCurrencySym.FindAllString(text, -1) {
		switch m {
		case "£":
			currencies = append(currencies, "GBP")
		case "$":
			currencies = append(currencies, "USD")
		case "€":
			currencies = append(currencies, "EUR")
		}
	}
	normalized := atmReplaceNumberWords(text)
	for _, m := range reATMNumber.FindAllString(normalized, -1) {
		if d, ok := parseATMDecimal(m); ok {
			values = append(values, d)
		}
	}
	return values, currencies
}

func atmExtractCodes(text string) []string {
	var tokens []string
	for _, m := range reATMCode.FindAllString(text, -1) {
		upper := strings.ToUpper(m)
		if matched, _ := regexp.MatchString(`^\d{1,2}(ST|ND|RD|TH)$`, upper); matched {
			continue
		}
		hasAlpha, hasDigit := false, false
		for _, r := range upper {
			if r >= 'A' && r <= 'Z' {
				hasAlpha = true
			}
			if r >= '0' && r <= '9' {
				hasDigit = true
			}
		}
		if hasAlpha && hasDigit {
			tokens = append(tokens, upper)
		}
	}
	return dedupeATMStrings(tokens)
}

func atmNormalizeCurrencyCodes(currencies []string) []string {
	set := make(map[string]struct{}, len(currencies))
	for _, c := range currencies {
		upper := strings.ToUpper(c)
		switch upper {
		case "GBP", "POUNDS", "POUND", "£":
			set["GBP"] = struct{}{}
		case "USD", "DOLLARS", "DOLLAR", "$":
			set["USD"] = struct{}{}
		case "EUR", "EUROS", "EURO", "€":
			set["EUR"] = struct{}{}
		case "AUD":
			set["AUD"] = struct{}{}
		case "JOD":
			set["JOD"] = struct{}{}
		default:
			set[upper] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for c := range set {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

func atmTokenize(text string) []string {
	matches := reATMToken.FindAllString(text, -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, strings.ToLower(m))
	}
	return out
}

func atmSplitListItems(text string) []string {
	parts := reATMListSplit.Split(text, -1)
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		item := atmNormalizeText(strings.TrimSpace(part))
		if item != "" {
			out = append(out, item)
		}
	}
	return out
}

func atmTokenSubsetMatch(groundTruth, prediction string) bool {
	var gtTokens []string
	for _, token := range atmTokenize(groundTruth) {
		if _, stop := atmStopwords[token]; !stop {
			gtTokens = append(gtTokens, token)
		}
	}
	predTokens := make(map[string]struct{})
	for _, token := range atmTokenize(prediction) {
		predTokens[token] = struct{}{}
	}
	if len(gtTokens) == 0 {
		return true
	}
	for _, token := range gtTokens {
		if _, ok := predTokens[token]; !ok {
			return false
		}
	}
	return true
}

func atmLocationTokenMatch(groundTruth, prediction string) bool {
	gtTokens := atmTokenize(groundTruth)
	foundAddress := false
	for _, token := range gtTokens {
		if _, ok := atmAddressKeywords[token]; ok {
			foundAddress = true
			break
		}
	}
	if !foundAddress {
		return false
	}
	var filtered []string
	for _, token := range gtTokens {
		if _, stop := atmStopwords[token]; stop {
			continue
		}
		if isAllDigits(token) {
			continue
		}
		filtered = append(filtered, token)
	}
	if len(filtered) == 0 {
		return false
	}
	predTokens := make(map[string]struct{})
	for _, token := range atmTokenize(prediction) {
		predTokens[token] = struct{}{}
	}
	for _, token := range filtered {
		if _, ok := predTokens[token]; !ok {
			return false
		}
	}
	return true
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func atmExtractReferenceDate(text string) time.Time {
	for _, token := range atmExtractDates(text) {
		if len(token) != 8 {
			continue
		}
		year, month, day := atmAtoi(token[0:4]), atmAtoi(token[4:6]), atmAtoi(token[6:8])
		if year <= 0 || month <= 0 || day <= 0 {
			continue
		}
		return time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
	}
	return time.Time{}
}

func atmResolveRelativeDates(text string, reference time.Time) string {
	if reference.IsZero() {
		return text
	}
	mapping := []struct {
		key  string
		date time.Time
	}{
		{"today", reference},
		{"tonight", reference},
		{"tomorrow", reference.AddDate(0, 0, 1)},
		{"yesterday", reference.AddDate(0, 0, -1)},
	}
	resolved := text
	for _, item := range mapping {
		monthDay := item.date.Format("January 02")
		for _, variant := range []string{item.key, strings.ToUpper(item.key[:1]) + item.key[1:], strings.ToUpper(item.key)} {
			if strings.Contains(resolved, variant) {
				resolved = strings.ReplaceAll(resolved, variant, monthDay)
				break
			}
		}
	}
	resolved = regexp.MustCompile(`(?i)(today|tonight|tomorrow|yesterday)\s*\([^)]+\)`).ReplaceAllString(resolved, "$1")
	return resolved
}

func atmSemanticUnitsMatch(groundTruth, prediction string) bool {
	gtDates, prDates := atmExtractDates(groundTruth), atmExtractDates(prediction)
	gtTimes, prTimes := atmExtractTimes(groundTruth), atmExtractTimes(prediction)
	gtAmounts, prAmounts := atmExtractCurrencyAmounts(groundTruth), atmExtractCurrencyAmounts(prediction)
	if len(gtDates) > 0 {
		if len(prDates) == 0 {
			return false
		}
		matched := false
		for _, gd := range gtDates {
			for _, pd := range prDates {
				if atmLast4(gd) == atmLast4(pd) {
					matched = true
				}
			}
		}
		if !matched {
			return false
		}
	}
	if len(gtTimes) > 0 && len(prTimes) > 0 {
		inter := false
		for _, gt := range gtTimes {
			for _, pt := range prTimes {
				if gt == pt {
					inter = true
				}
			}
		}
		if !inter {
			return false
		}
	}
	if len(gtAmounts) > 0 {
		if len(prAmounts) == 0 {
			return false
		}
		gset, pset := map[string]struct{}{}, map[string]struct{}{}
		for _, d := range gtAmounts {
			gset[d.key()] = struct{}{}
		}
		for _, d := range prAmounts {
			pset[d.key()] = struct{}{}
		}
		if len(gset) != len(pset) {
			return false
		}
		for k := range gset {
			if _, ok := pset[k]; !ok {
				return false
			}
		}
	}
	gtContent, prContent := map[string]struct{}{}, map[string]struct{}{}
	for _, token := range atmTokenize(groundTruth) {
		if _, stop := atmStopwords[token]; stop || len(token) <= 2 {
			continue
		}
		gtContent[token] = struct{}{}
	}
	for _, token := range atmTokenize(prediction) {
		if _, stop := atmStopwords[token]; stop || len(token) <= 2 {
			continue
		}
		prContent[token] = struct{}{}
	}
	if len(gtContent) == 0 {
		return true
	}
	overlap := 0
	for token := range gtContent {
		if _, ok := prContent[token]; ok {
			overlap++
		}
	}
	return float64(overlap)/float64(len(gtContent)) >= 0.8
}

func atmLast4(s string) string {
	if len(s) <= 4 {
		return s
	}
	return s[len(s)-4:]
}
