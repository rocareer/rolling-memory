// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package eval

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/rocareer/rolling-memory/internal/ids"
	"github.com/rocareer/rolling-memory/internal/text"
)

var articles = map[string]struct{}{"a": {}, "an": {}, "the": {}}

var (
	noRecordAnswer = regexp.MustCompile(`(?i)\b(no record|not mentioned|cannot determine|i don't know)\b`)
)

// NormalizeAnswer lowercases, preserves all Unicode letters/digits (splitting
// continuous ideographic/syllabic characters into atomic tokens), and drops
// punctuation and SQuAD articles (scoring:104-108 + upstream gold_coverage.go).
func NormalizeAnswer(rawText string) string {
	value := strings.ToLower(pyStr(rawText))
	var cleaned strings.Builder
	for _, char := range value {
		if text.IsContinuousSyllabicOrIdeographic(char) {
			cleaned.WriteByte(' ')
			cleaned.WriteRune(char)
			cleaned.WriteByte(' ')
			continue
		}
		if unicode.IsLetter(char) || unicode.IsNumber(char) || isPySpace(char) {
			cleaned.WriteRune(char)
			continue
		}
		cleaned.WriteByte(' ')
	}
	words := []string{}
	for _, word := range strings.Fields(cleaned.String()) {
		if _, ok := articles[word]; ok {
			continue
		}
		words = append(words, word)
	}
	return strings.Join(words, " ")
}

// AnswerTokens is normalize_answer(text).split().
func AnswerTokens(text string) []string {
	return strings.Fields(NormalizeAnswer(text))
}

// TokenF1 is the multiset F1 over normalized answer tokens.
func TokenF1(prediction, gold string) float64 {
	pred := AnswerTokens(prediction)
	truth := AnswerTokens(gold)
	if len(pred) == 0 || len(truth) == 0 {
		return 0.0
	}
	predCounts := map[string]int{}
	for _, token := range pred {
		predCounts[token]++
	}
	overlap := 0
	for _, token := range truth {
		if predCounts[token] > 0 {
			predCounts[token]--
			overlap++
		}
	}
	if overlap == 0 {
		return 0.0
	}
	precision := float64(overlap) / float64(len(pred))
	recall := float64(overlap) / float64(len(truth))
	return 2 * precision * recall / (precision + recall)
}

// TokenRecall is the share of needle tokens present in haystack (scoring:129-134).
func TokenRecall(needle, haystack string) float64 {
	wanted := map[string]struct{}{}
	for _, token := range ids.Tokenize(needle) {
		wanted[token] = struct{}{}
	}
	if len(wanted) == 0 {
		return 0.0
	}
	have := map[string]struct{}{}
	for _, token := range ids.Tokenize(haystack) {
		have[token] = struct{}{}
	}
	hit := 0
	for token := range wanted {
		if _, ok := have[token]; ok {
			hit++
		}
	}
	return float64(hit) / float64(len(wanted))
}

// BestEvidenceRecall is the best token recall over the gold evidence texts.
func BestEvidenceRecall(evidenceTexts []string, context string) float64 {
	if len(evidenceTexts) == 0 {
		return 0.0
	}
	best := 0.0
	for _, text := range evidenceTexts {
		if score := TokenRecall(text, context); score > best {
			best = score
		}
	}
	return best
}

// FullTokenRecall is the cap-free sibling of TokenRecall: the share of needle
// terms found anywhere in the *whole* haystack.
//
// TokenRecall tokenizes both sides with ids.Tokenize, whose query cap keeps only
// the first 48 unique terms (internal/ids/ids.go:317-319). On a delivered
// context of a few thousand characters the haystack side therefore degrades into
// a head-of-context hit rate instead of a full-context evidence hit rate.
// FullTokenRecall keeps exactly the same term semantics and drops only that cap,
// so the two rails differ in a single variable.
func FullTokenRecall(needle, haystack string) float64 {
	return fullTokenCoverage(needle, fullTermSet(haystack))
}

// BestFullEvidenceRecall is the cap-free sibling of BestEvidenceRecall.
func BestFullEvidenceRecall(evidenceTexts []string, context string) float64 {
	if len(evidenceTexts) == 0 {
		return 0.0
	}
	have := fullTermSet(context)
	best := 0.0
	for _, text := range evidenceTexts {
		if score := fullTokenCoverage(text, have); score > best {
			best = score
		}
	}
	return best
}

// fullTokenCoverage is |fullTermSet(needle) ∩ have| / |fullTermSet(needle)|.
func fullTokenCoverage(needle string, have map[string]struct{}) float64 {
	wanted := fullTermSet(needle)
	if len(wanted) == 0 {
		return 0.0
	}
	hit := 0
	for token := range wanted {
		if _, ok := have[token]; ok {
			hit++
		}
	}
	return float64(hit) / float64(len(wanted))
}

// queryCapBound is min(query cap, len(tokenCapProbe)): ids.Tokenize answers an
// over-long query with exactly its internal cap, so probing with more distinct
// terms than any realistic cap yields a bound that never exceeds the real cap.
// "fewer terms than the bound came back" then proves the cap did not bind.
var queryCapBound = detectQueryCapBound()

// tokenCapProbe asks for more distinct synthetic terms than any realistic cap.
const tokenCapProbe = 200

func detectQueryCapBound() int {
	probe := make([]string, 0, tokenCapProbe)
	for index := 0; index < tokenCapProbe; index++ {
		probe = append(probe, "tkz"+strconv.Itoa(1000+index))
	}
	bound := len(ids.Tokenize(strings.Join(probe, " ")))
	if bound < 1 {
		// A degenerate cap must not collapse the split below into a no-op.
		bound = 1
	}
	return bound
}

// fullTermSet is ids.Tokenize's term set without the query cap: a chunk that may
// have been truncated is split in two and re-tokenized until every chunk comes
// back short of the cap, then the parts are unioned. Splitting prefers the
// whitespace closest to the middle, which leaves whitespace-delimited terms
// untouched (NormalizeWithMap folds each rune in place, so it has no cross-rune
// effect either); text without whitespace splits at a rune boundary and can lose
// the one bigram (plus up to two trigrams) that crossed that boundary.
func fullTermSet(text string) map[string]struct{} {
	terms := map[string]struct{}{}
	var walk func(chunk string)
	walk = func(chunk string) {
		tokens := ids.Tokenize(chunk)
		if len(tokens) < queryCapBound {
			for _, token := range tokens {
				terms[token] = struct{}{}
			}
			return
		}
		left, right := splitTokenChunk(chunk)
		if left == "" || right == "" {
			for _, token := range tokens {
				terms[token] = struct{}{}
			}
			return
		}
		walk(left)
		walk(right)
	}
	walk(text)
	return terms
}

// splitTokenChunk halves a chunk at the whitespace closest to its middle (no
// token spans whitespace), falling back to the rune midpoint.
func splitTokenChunk(chunk string) (string, string) {
	runes := []rune(chunk)
	if len(runes) < 2 {
		return "", ""
	}
	middle := len(runes) / 2
	at := middle
	closest := len(runes)
	for index, char := range runes {
		if !unicode.IsSpace(char) {
			continue
		}
		distance := index - middle
		if distance < 0 {
			distance = -distance
		}
		if distance < closest {
			closest = distance
			at = index
		}
	}
	if at <= 0 || at >= len(runes) {
		at = middle
	}
	return string(runes[:at]), string(runes[at:])
}

// ClassifyFailureBucket deterministically maps a single question's evaluation
// metrics into the 5-state engineering taxonomy (HIT + 4 failure buckets:
// RECALL_MISS, RANK_CUTOFF, READER_GAP, JUDGE_AMBIGUITY).
func ClassifyFailureBucket(evidenceHit, answerInContext bool, bestEvidenceRecall, f1, goldTokenRecall float64, judgeVerdict string) string {
	verdict := strings.ToLower(strings.TrimSpace(judgeVerdict))
	if verdict == "correct" {
		return "HIT"
	}
	if verdict == "" || verdict == "off" || verdict == "none" || verdict == "<nil>" {
		if evidenceHit || answerInContext {
			return "HIT"
		}
		if bestEvidenceRecall > 0 {
			return "RANK_CUTOFF"
		}
		return "RECALL_MISS"
	}
	// When judge ran and returned wrong / unparsed / error:
	if f1 >= 0.50 || goldTokenRecall >= 0.80 {
		return "JUDGE_AMBIGUITY"
	}
	if evidenceHit || answerInContext {
		return "READER_GAP"
	}
	if bestEvidenceRecall > 0 {
		return "RANK_CUTOFF"
	}
	return "RECALL_MISS"
}

func firstRunes(value string, limit int) string {
	runes := []rune(value)
	if limit >= 0 && len(runes) > limit {
		return string(runes[:limit])
	}
	return value
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}

func firstTruthy(primary, fallback any) any {
	if truthy(primary) {
		return primary
	}
	return fallback
}

func truthy(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case string:
		return typed != ""
	case int:
		return typed != 0
	case int64:
		return typed != 0
	case float64:
		return typed != 0
	case []any:
		return len(typed) > 0
	case map[string]any:
		return len(typed) > 0
	case json.Number:
		parsed, err := typed.Float64()
		return err != nil || parsed != 0
	}
	return true
}

// pyStr mirrors str() for the value types the harness carries.
func pyStr(value any) string {
	switch typed := value.(type) {
	case nil:
		return "None"
	case json.Number:
		return typed.String()
	case string:
		return typed
	case bool:
		if typed {
			return "True"
		}
		return "False"
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case float64:
		return strconv.FormatFloat(typed, 'g', -1, 64)
	}
	return fmt.Sprint(value)
}

func pyStrip(value string) string {
	return strings.TrimFunc(value, isPySpace)
}

func isPySpace(char rune) bool {
	if unicode.IsSpace(char) {
		return true
	}
	return char >= '\u001c' && char <= '\u001f'
}
