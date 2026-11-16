// Code generated from internal/eval/atmofficial_scoring.go. DO NOT EDIT.
package rollingmemory

// ATM-Bench official deterministic evaluator — scoring core port.
//
// Upstream source: memqa/utils/evaluator/evaluate_qa.py
//   - _deterministic_accuracy_core / deterministic_accuracy (number qtype)
//   - _list_jaccard_core / list_jaccard_score (list_recall qtype)
//   - parse_judge_response (open_end judge response contract)
//
// Protocol adapter only. Pinned by ATMOfficialScoringFixtureSHA256 and the
// golden-fixture parity test; not a memory-engine algorithm.

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

var (
	reATMListMediaID = regexp.MustCompile(`\b(\d{8}_\d{6})\b`)
	reATMListEmailID = regexp.MustCompile(`(?i)\b(email\d+)\b`)
)

// ATMOfficialNumberAccuracy mirrors the upstream number-qtype deterministic
// accuracy. The question is required for official relative-date resolution.
func ATMOfficialNumberAccuracy(groundTruth, prediction, question string) (bool, string) {
	ok := atmOfficialDeterministicAccuracy(groundTruth, prediction, question)
	if ok {
		return true, "ATM official deterministic accuracy: match."
	}
	return false, "ATM official deterministic accuracy: mismatch."
}

// ATMOfficialListJaccard mirrors the upstream list_recall Jaccard score.
func ATMOfficialListJaccard(groundTruth, prediction string) float64 {
	gtItems := make(map[string]struct{})
	for _, item := range atmSplitListItems(groundTruth) {
		if item != "" {
			gtItems[item] = struct{}{}
		}
	}
	predList := atmPredictionListItems(prediction)
	predItems := make(map[string]struct{}, len(predList))
	for _, item := range predList {
		predItems[item] = struct{}{}
	}
	if len(gtItems) == 0 && len(predItems) == 0 {
		return 1.0
	}
	if len(gtItems) == 0 {
		return 0.0
	}
	union := make(map[string]struct{}, len(gtItems)+len(predItems))
	for item := range gtItems {
		union[item] = struct{}{}
	}
	inter := 0
	for item := range predItems {
		union[item] = struct{}{}
		if _, ok := gtItems[item]; ok {
			inter++
		}
	}
	if len(union) == 0 {
		return 0.0
	}
	return float64(inter) / float64(len(union))
}

// ATMOfficialParseJudgeResponse mirrors parse_judge_response: JSON first, then
// the upstream substring / keyword fallback. Returns (accuracy, explanation,
// parseFallback).
func ATMOfficialParseJudgeResponse(raw string) (bool, string, bool) {
	start := strings.IndexByte(raw, '{')
	end := strings.LastIndexByte(raw, '}')
	if start != -1 && end > start {
		var payload map[string]any
		if err := json.Unmarshal([]byte(raw[start:end+1]), &payload); err == nil {
			if accVal, ok := payload["accuracy"]; ok {
				acc := strings.EqualFold(strings.TrimSpace(fmt.Sprint(accVal)), "true")
				exp, _ := payload["explanation"].(string)
				return acc, strings.TrimSpace(exp), false
			}
		}
	}
	lower := strings.ToLower(raw)
	var accuracy bool
	switch {
	case strings.Contains(lower, `"accuracy": true`) || strings.Contains(lower, `"accuracy":true`):
		accuracy = true
	case strings.Contains(lower, `"accuracy": false`) || strings.Contains(lower, `"accuracy":false`):
		accuracy = false
	case strings.Contains(lower, "accurate") || strings.Contains(lower, "correct") ||
		strings.Contains(lower, "matches") || strings.Contains(lower, "true") ||
		strings.Contains(lower, "yes"):
		accuracy = true
	default:
		accuracy = false
	}
	explanation := raw
	if runes := []rune(raw); len(runes) > 500 {
		explanation = string(runes[:500]) + "..."
	}
	return accuracy, explanation, true
}

func atmOfficialDeterministicAccuracy(groundTruth, prediction, question string) bool {
	isAbstGt := atmIsAbstention(groundTruth)
	isAbstPred := atmIsAbstention(prediction)

	reference := atmExtractReferenceDate(question)
	gtResolved := atmResolveRelativeDates(groundTruth, reference)
	predResolved := atmResolveRelativeDates(prediction, reference)

	gtPrep := atmAggressivePreprocess(gtResolved)
	predPrep := atmAggressivePreprocess(predResolved)
	gtClean := atmStripParentheticalDetails(gtPrep)
	predClean := atmStripParentheticalDetails(predPrep)
	gtClean = atmStripCurrencyBreakdowns(gtClean)
	predClean = atmStripCurrencyBreakdowns(predClean)
	gtClean = atmNormalizeBetweenToRange(gtClean)
	predClean = atmNormalizeBetweenToRange(predClean)
	gtClean = atmStripLeadingArticles(gtClean)
	predClean = atmStripLeadingArticles(predClean)

	gtNorm := atmNormalizeText(gtClean)
	predNorm := atmNormalizeText(predClean)

	if isAbstGt || isAbstPred {
		return isAbstGt && isAbstPred
	}

	gtCodes := atmExtractCodes(gtClean)
	if len(gtCodes) > 0 {
		predStripped := reATMCodePrefix.ReplaceAllString(strings.ToUpper(predClean), "")
		for _, code := range gtCodes {
			if !strings.Contains(predStripped, code) {
				return false
			}
		}
	}

	gtDates := atmExtractDates(gtClean)
	predDates := atmExtractDates(predClean)
	if len(gtDates) > 0 {
		if len(predDates) == 0 || !atmDatesMatch(gtDates, predDates) {
			return false
		}
	}

	gtTimes := atmExtractTimes(gtClean)
	predTimes := atmExtractTimes(predClean)
	if len(gtTimes) > 0 && len(predTimes) > 0 {
		if !atmStringMultisetEqual(gtTimes, predTimes) {
			return false
		}
	}

	if len(gtDates) > 0 || len(gtTimes) > 0 {
		gtRemainder := atmNormalizeText(atmRemoveDateTimeText(gtClean))
		predRemainder := atmNormalizeText(atmRemoveDateTimeText(predClean))
		gtRemainderStripped := atmStripContextPhrases(gtRemainder)
		predRemainderStripped := atmStripContextPhrases(predRemainder)
		if gtRemainderStripped == "" {
			return true
		}
		if atmTokenSubsetMatch(gtRemainderStripped, predRemainderStripped) {
			return true
		}
	}

	gtNoDateTime := atmRemoveDateTimeText(gtClean)
	predNoDateTime := atmRemoveDateTimeText(predClean)
	gtNumbers, gtCurrencies := atmExtractNumbers(gtNoDateTime)
	predNumbers, predCurrencies := atmExtractNumbers(predNoDateTime)
	gtAmounts := atmExtractCurrencyAmounts(gtNoDateTime)
	predAmounts := atmExtractCurrencyAmounts(predNoDateTime)

	if len(gtCurrencies) > 0 {
		gtCurrNorm := atmNormalizeCurrencyCodes(gtCurrencies)
		predCurrNorm := atmNormalizeCurrencyCodes(predCurrencies)
		if len(predCurrNorm) > 0 && !atmStringMultisetEqual(gtCurrNorm, predCurrNorm) {
			return false
		}
	}

	if len(gtAmounts) > 0 {
		if len(predAmounts) == 0 || !atmDecimalMultisetEqual(gtAmounts, predAmounts) {
			return false
		}
		gtNumbers = atmRemoveDecimals(gtNumbers, gtAmounts)
		predNumbers = atmRemoveDecimals(predNumbers, predAmounts)
	}

	if len(gtNumbers) > 0 {
		if len(predNumbers) == 0 || !atmDecimalMultisetEqual(gtNumbers, predNumbers) {
			return false
		}
	}

	gtItems := atmSplitListItems(gtClean)
	if len(gtItems) >= 2 {
		for _, item := range gtItems {
			if !strings.Contains(predNorm, item) {
				return false
			}
		}
		return true
	}

	if strings.HasPrefix(gtNorm, "yes") || strings.HasPrefix(gtNorm, "no") {
		if predNorm == "yes" || predNorm == "no" {
			return strings.HasPrefix(gtNorm, predNorm)
		}
	}

	if atmLocationTokenMatch(gtClean, predClean) {
		return true
	}
	if len(gtNorm) >= 3 && strings.Contains(predNorm, gtNorm) {
		return true
	}
	if len(predNorm) >= 3 && strings.Contains(gtNorm, predNorm) {
		return true
	}
	if atmTokenSubsetMatch(gtClean, predClean) {
		return true
	}
	if atmSemanticUnitsMatch(gtClean, predClean) {
		return true
	}
	return gtNorm == predNorm
}

func atmDatesMatch(gtTokens, predTokens []string) bool {
	remaining := append([]string(nil), predTokens...)
	for _, gt := range gtTokens {
		matched := false
		for idx, pred := range remaining {
			if atmDateTokenMatch(gt, pred) {
				matched = true
				remaining = append(remaining[:idx], remaining[idx+1:]...)
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

func atmDateTokenMatch(gtToken, predToken string) bool {
	if strings.Contains(gtToken, "-") {
		if !strings.Contains(predToken, "-") {
			return false
		}
		gtParts := strings.SplitN(gtToken, "-", 2)
		predParts := strings.SplitN(predToken, "-", 2)
		return atmDateComponentMatch(gtParts[0], predParts[0]) &&
			atmDateComponentMatch(gtParts[1], predParts[1])
	}
	return atmDateComponentMatch(gtToken, predToken)
}

func atmDateComponentMatch(gt, pred string) bool {
	if len(gt) == 4 || len(pred) == 4 {
		return atmLast4(gt) == atmLast4(pred)
	}
	return gt == pred
}

func atmStringMultisetEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	counts := make(map[string]int, len(a))
	for _, s := range a {
		counts[s]++
	}
	for _, s := range b {
		counts[s]--
		if counts[s] < 0 {
			return false
		}
	}
	return true
}

func atmDecimalMultisetEqual(a, b []atmDecimal) bool {
	if len(a) != len(b) {
		return false
	}
	counts := make(map[string]int, len(a))
	for _, d := range a {
		counts[d.key()]++
	}
	for _, d := range b {
		counts[d.key()]--
		if counts[d.key()] < 0 {
			return false
		}
	}
	return true
}

func atmRemoveDecimals(values, remove []atmDecimal) []atmDecimal {
	removeSet := make(map[string]struct{}, len(remove))
	for _, d := range remove {
		removeSet[d.key()] = struct{}{}
	}
	out := make([]atmDecimal, 0, len(values))
	for _, d := range values {
		if _, ok := removeSet[d.key()]; ok {
			continue
		}
		out = append(out, d)
	}
	return out
}

func atmPredictionListItems(prediction string) []string {
	if ids := atmExtractListIDs(prediction); len(ids) > 0 {
		return ids
	}
	var out []string
	for _, item := range atmSplitListItems(prediction) {
		if item != "" {
			out = append(out, item)
		}
	}
	return out
}

func atmExtractListIDs(text string) []string {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	var ids []string
	seen := make(map[string]struct{})
	for _, m := range reATMListMediaID.FindAllStringSubmatch(text, -1) {
		id := m[1]
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	for _, m := range reATMListEmailID.FindAllStringSubmatch(text, -1) {
		id := strings.ToLower(m[1])
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids
}
