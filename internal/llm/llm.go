// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

// Package llm implements the optional OpenAI-compatible extractor and the
// deterministic local ruleset.
//
// The local ruleset is intentionally conservative. It is not a replacement for
// an LLM; it exists so that mode=local with llm.provider=none still produces
// anchored, auditable assertions instead of silently doing nothing. Recall
// never needs a model.
package llm

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/rocareer/rolling-memory/internal/config"
	"github.com/rocareer/rolling-memory/internal/httpclient"
	"github.com/rocareer/rolling-memory/internal/ids"
)

const systemPrompt = `You are the memory encoding sub-agent (noetic) of Rolling-Memory.
You extract atomic structured memory assertions from one evidence passage and return exactly one JSON object, no markdown:
{"assertions":[{"s":{"label":"..."},"x":"relation label","o":{"label":"..."},"warrant":"referential|performative|procedural|experiential","confidence":0.0,"evidence":"verbatim substring","t_valid_from":"YYYY-MM-DD or empty","closes_prior":false,"world":"","carrier":"","locus":""}],"unresolved":[{"surface":"..."}]}

Rules (Unified Four-Axiom Specification):

Method before patterns (self-biographical cognition; teach the method, never a pattern list):
- Two memory lines. FACT memory: S is an entity, O is an entity or value (world, others, events). META-COGNITION memory: S is a cognizing subject and O is a PROPOSITION — a reference to an assertion node ("I believed [#A]", "老李 thinks [#A]"). Meta-cognition is identified STRUCTURALLY by O pointing at a proposition, never by a word list; S only decides whose cognition it is (self = introspection; another entity = belief/report; reported belief's warrant is usually referential).
- Three questions before writing each assertion (they read the axioms, not a pattern library):
  1) Direction — who does this relation come from? S is the perspective subject of the line (experiencer / speaker / cognizer); O is what the relation lands on. Reported speech puts the speaker in S.
  2) Completeness — fill every coordinate the passage carries (who / what (X + O) / when / where / by what warrant / to whom). A stated "what" with empty O is an unfinished assertion; a whole proposition as O must be a proposition reference, not a clause literal.
  3) Anchoredness — every slot must be pointable. An antecedent present in the passage MUST be resolved (entity label / value / proposition id); otherwise declare it in "unresolved" with its surface form (never guess, never silently drop).
- Aliases: only register an alias that has an actual basis in the passage (self-declared name, explicit referral, stated relation); when no basis is present, leave it out — never guess.
- When O is a proposition, prefer the id form "#<id>" if an earlier write in this loop returned one; otherwise record the inner assertion first and reference "@k".

- Axiom 1 (Atomic SPO, Dual Forms & Strict Language Conservation):
  - Split multi-clause or parallel facts into separate atomic assertions; do not record questions, greetings, or filler.
  - Keep S, x, and O in the EXACT SAME language and script as the "evidence" span.
  - "x" is a short canonical verb/relation phrase in the passage's language (for example 养, 喜欢, moved to, works at), never snake_case.
  - Use {"label":"..."} for entities/nouns and {"literal":"..."} verbatim for values/numbers/states.
  - Dual forms are valid: omit "o" for objectless actions (binary SX); set "subject_absent": true and omit "s" for subjectless objective events (binary XO).
- Axiom 2 (Coreference & First-Person Ego Binding):
  - Bind first-person pronouns ("I", "me", "my", "we", "我", "我们") to the speaker when speaker is a concrete entity name.
  - Resolve pronouns from the passage. Never put raw pronouns (he/she/it/they/this/that/他/她/它) or role placeholders (user/assistant/reader) into "s" or "o". If a reference cannot be resolved, add it to "unresolved" and omit that assertion.
- Axiom 3 (Multi-layer Speech & Epistemic "@k"):
  - Decompose "A said/thinks P" into inner assertion P first (at 1-based index k) and outer assertion with "o": "@k".
- Axiom 4 (Time, State Evolution & Verbatim Anchor):
  - "evidence" MUST be copied verbatim from the passage. Never paraphrase evidence.
  - Normalize relative date expressions to "YYYY-MM-DD" using time_ref; leave "" when unknown or clock-only.
  - Set "closes_prior": true ONLY when the passage explicitly states a prior state/event has ended or terminated.
  - world is actual|fictional|hypothetical|dream or empty; carrier is real|virtual or empty; confidence 0.0-1.0.
  - If the passage contains no rememberable assertion, return {"assertions":[],"unresolved":[]}.`

const querySystem = `You rewrite one memory query into short retrieval hints.
Return exactly one JSON object: {"s":"...","x":"...","o":"..."}.
Use the entity names, relation label and value that appear in the query's language.
Leave a field as an empty string when the query does not identify it.
Do not answer the question. Do not add facts. No markdown.`

// Extraction is one extraction result: assertions, aliases, and unresolved mentions.
type Extraction struct {
	Assertions   []map[string]any
	Aliases      []map[string]any
	Unresolved   []map[string]any
	AnchorFailed int
	Extractor    string
	Warning      string
}

// Extractor is the write-side extraction seam.
type Extractor interface {
	Extract(text, speaker string, timeRef int64) (Extraction, error)
	Name() string
}

// HintsProvider is the optional read-side query rewrite seam (deep recall).
type HintsProvider interface {
	QueryHints(cue string) (map[string]string, error)
}

// LLMError marks a failed model call.
type LLMError struct{ message string }

func (e *LLMError) Error() string { return e.message }

func llmErrorf(format string, args ...any) error {
	return &LLMError{message: fmt.Sprintf(format, args...)}
}

// The copula/preposition/light-verb relations (是/为/在/有) are deliberately
// NOT patterns: in prose they read as structural particles and the lazy
// subject capture shreds clauses into fragment triples («生产确实 在 …»,
// «两类缺陷均 为 …» — live-graph regressions 2026-10-28). rule-v1 stays on
// specific predicates instead.
var cjkPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?P<s>[\x{4e00}-\x{9fff}A-Za-z0-9_·\-]{1,20}?)(?P<x>名叫|叫做|叫)(?P<o>[^\s，。；！？,.;!?]{1,40})`),
	regexp.MustCompile(`(?P<s>[\x{4e00}-\x{9fff}A-Za-z0-9_·\-]{1,20}?)(?P<x>喜欢|爱|讨厌|偏好|想要|需要|养|喂|照顾|住在|工作在|工作于|任职于|就职于|出生于|生于|毕业(?:于)?|就读于|搬到|搬到了|迁移到|来自|拥有)(?P<o>[^\s，。；！？,.;!?]{1,40})`),
	regexp.MustCompile(`(?P<s>[\x{4e00}-\x{9fff}A-Za-z0-9_·\-]{1,20}?)(?P<x>在)(?P<o>[^\s，。；！？,.;!?]{1,20}?)(?:工作|生活|学习|上班)`),
}

var enPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\b(?P<s>I|we|he|she|they|[A-Z][A-Za-z0-9_.\-]*(?:\s+[A-Z][A-Za-z0-9_.\-]*){0,2})\s+(?P<x>was born in|born in|moved to|moves to|is married to|married to|works for|worked for|studies at|studied at|graduated from|graduated at)\s+(?P<o>[^.!?;\n]{1,60})`),
	regexp.MustCompile(`\b(?P<s>I|we|he|she|they|[A-Z][A-Za-z0-9_.\-]*(?:\s+[A-Z][A-Za-z0-9_.\-]*){0,2})\s+(?P<x>is|was|are|were|am)\s+(?P<o>[^.!?;\n]{1,60})`),
	regexp.MustCompile(`\b(?P<s>I|we|he|she|they|[A-Z][A-Za-z0-9_.\-]*)\s+(?P<x>like|likes|love|loves|hate|hates|prefer|prefers|use|uses|work at|works at|live in|lives in|have|has|own|owns|feed|feeds)\s+(?P<o>[^.!?;\n]{1,60})`),
}

// engDecisionPattern captures explicit, whole-clause engineering rulings,
// architecture convergences, root causes and negative constraints with high
// confidence (0.85) so they surface as settled facts rather than mention_only.
var engDecisionPattern = regexp.MustCompile(`^(?P<s>[\x{4e00}-\x{9fff}A-Za-z0-9_·\-/]{2,24}?)\s*(?P<x>升级为|收敛为|裁定为|定为|改为|切换为|统一为|根因为|修复为|软连接到|不使用|禁止使用)\s*(?P<o>[\x{4e00}-\x{9fff}A-Za-z0-9_·\-/]{2,40})[。；！!.;]?$`)

var (
	questionRE    = regexp.MustCompile(`[?？]\s*$`)
	negationRE    = regexp.MustCompile(`(?i)(不|没|没有|未|别|not\s|n't\s)`)
	conditionalRE = regexp.MustCompile(`(?i)(如果|假如|要是|若|即便|即使|\bif\b)`)
	timeRE        = regexp.MustCompile(`(?i)\d{4}[-/年]\d{1,2}[-/月]\d{1,2}日?|\d{1,2}月\d{1,2}日|` +
		`今天|今日|昨天|昨日|前天|明天|明日|后天|大后天|上周|上星期|本周|这周|下周|下星期|去年|今年|明年|` +
		`\d+\s*天[前后]|\d+\s*(?:周|个星期)[前后]|` +
		`yesterday|today|tomorrow|last year|this year|next year`)
	leadingParticles  = regexp.MustCompile(`^(?:其实|真的|一直|已经|曾经|也|还|就|都|很|太|最|更|又|再|才|只|刚|刚刚|大概|可能|应该)+`)
	trailingParticles = regexp.MustCompile(`(?:的|了|呢|啊|吧|呀|哦|嘛)+$`)
	objectPrefix      = regexp.MustCompile(`(?i)^(?:(?:了|着|过|一只|一个|一位|一条|一件|一些|这个|那个|the|a|an|named)\s*)+`)
	properNounRE      = regexp.MustCompile(`\b[A-Z][A-Za-z0-9_.\-]{1,30}\b`)
	fenceStartRE      = regexp.MustCompile(`^` + "```" + `[a-zA-Z0-9_-]*\s*`)
	fenceEndRE        = regexp.MustCompile(`\s*` + "```" + `$`)
	interrogativeRE   = regexp.MustCompile(`(?i)(什么|谁|哪|怎么|如何|多少|几个|是否|吗|呢|\b(?:what|who|whom|whose|where|which|how|why|when)\b)`)
	labelNoiseRE      = regexp.MustCompile("[`*|<>{}=~^\\\\]")
	labelStubTailRE   = regexp.MustCompile(`[既就都也还才又再则即且而但或被把向给对以由从于着过很太更只刚却它此正]$`)
	// Markdown/table/code prose is a report, not a memory sentence: the
	// conservative fallback skips it whole instead of shredding it into
	// 0.35-confidence fragment triples (live-graph regressions: 「全量启用」
	// 「恰是反例」「表即交付启用」 all came from `|`/`**`/backtick lines).
	sentenceNoiseRE = regexp.MustCompile("[`*|]")
	// A label starting with a connector/particle is chopped prose
	// («但该卷落»), not an entity.
	labelStubHeadRE = regexp.MustCompile(`^(?:但|而|则|即|且|也|都|还|就|才|又|再|很|太|更|只|刚|却|它|此|之|的|了|着|地|得|并|因|若|故|已)`)
	// Quotes inside a label mean a broken span («"死向量窗"、需按»).
	labelQuoteRE = regexp.MustCompile("[\"\u201c\u201d\u2018\u2019]")
)

// RuleExtractor is the deterministic, conservative Chinese/English pattern
// extractor used when no LLM is configured.
type RuleExtractor struct{}

// Name returns the extractor identifier stored on receipts.
func (RuleExtractor) Name() string { return "rule-v1" }

// Extract runs the local sentence patterns against one evidence passage.
func (RuleExtractor) Extract(text, speaker string, timeRef int64) (Extraction, error) {
	cleaned := ids.StripInjected(text)
	sentences := ids.SentenceSplit(cleaned, 40, 240)
	assertions := []map[string]any{}
	surfaces := []string{}
	seen := map[string]struct{}{}

	for _, sentence := range sentences {
		if questionRE.MatchString(sentence) {
			continue
		}
		if conditionalRE.MatchString(sentence) {
			continue
		}
		if sentenceNoiseRE.MatchString(sentence) {
			continue
		}
		validFrom := int64(0)
		if match := timeRE.FindString(sentence); match != "" {
			validFrom = ids.ParseTime(match, ids.ParseTimeOptions{TimeRef: &timeRef, Default: 0})
		}
		if engMatch := engDecisionPattern.FindStringSubmatch(strings.TrimSpace(sentence)); engMatch != nil {
			subject := cleanEntity(engMatch[engDecisionPattern.SubexpIndex("s")])
			relation := cleanEntity(engMatch[engDecisionPattern.SubexpIndex("x")])
			object := cleanObject(engMatch[engDecisionPattern.SubexpIndex("o")])
			if subject != "" && relation != "" && object != "" && subject != object &&
				plausibleLabel(subject) && plausibleLabel(object) {
				subject = resolveSubject(subject, speaker)
				key := subject + "\x00" + relation + "\x00" + object
				if _, ok := seen[key]; !ok {
					seen[key] = struct{}{}
					assertions = append(assertions, map[string]any{
						"s":            map[string]any{"label": subject},
						"x":            relation,
						"o":            map[string]any{"label": object},
						"warrant":      "referential",
						"confidence":   0.85,
						"evidence":     sentence,
						"t_valid_from": validFrom,
						"world":        nil,
						"carrier":      nil,
						"locus":        nil,
					})
					surfaces = append(surfaces, subject, object)
					if len(assertions) >= 12 {
						break
					}
					continue
				}
			}
		}
		if negationRE.MatchString(sentence) {
			continue
		}
		matched := false
		for _, pattern := range append(append([]*regexp.Regexp{}, cjkPatterns...), enPatterns...) {
			match := pattern.FindStringSubmatch(sentence)
			if match == nil {
				continue
			}
			subject := cleanEntity(match[pattern.SubexpIndex("s")])
			relation := cleanEntity(match[pattern.SubexpIndex("x")])
			object := cleanObject(match[pattern.SubexpIndex("o")])
			if subject == "" || relation == "" || object == "" {
				continue
			}
			if subject == object {
				continue
			}
			// Prose shreds into fragment triples: markdown emphasis, chopped
			// connector stubs, interrogative tails, unbalanced brackets. The
			// local ruleset is the conservative fallback, so drop those instead
			// of writing 0.35-confidence noise.
			if !plausibleLabel(subject) || !plausibleLabel(object) {
				continue
			}
			subject = resolveSubject(subject, speaker)
			key := subject + "\x00" + relation + "\x00" + object
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			firstPerson := subject == "我" || subject == "I" || subject == "me" || subject == "we"
			warrant := "referential"
			if firstPerson {
				warrant = "experiential"
			}
			assertions = append(assertions, map[string]any{
				"s":            map[string]any{"label": subject},
				"x":            relation,
				"o":            map[string]any{"label": object},
				"warrant":      warrant,
				"confidence":   0.35,
				"evidence":     sentence,
				"t_valid_from": validFrom,
				"world":        nil,
				"carrier":      nil,
				"locus":        nil,
			})
			surfaces = append(surfaces, subject, object)
			matched = true
			break
		}
		if len(assertions) >= 12 {
			break
		}
		if !matched {
			// Keep proper-noun mentions even when no pattern fires.
			for _, token := range properNounRE.FindAllString(sentence, -1) {
				switch strings.ToLower(token) {
				case "i", "the", "a", "an":
					continue
				}
				surfaces = append(surfaces, token)
			}
		}
	}

	surfaces = append(surfaces, properNounRE.FindAllString(cleaned, -1)...)
	unresolved := []map[string]any{}
	mentionRows := []map[string]any{}
	spanSeen := map[string]struct{}{}
	for _, surface := range ids.UniqueOrdered(surfaces) {
		start := runeIndex(cleaned, surface)
		if start < 0 {
			continue
		}
		key := surface + "\x00" + strconv.Itoa(start)
		if _, ok := spanSeen[key]; ok {
			continue
		}
		spanSeen[key] = struct{}{}
		mentionRows = append(mentionRows, map[string]any{
			"surface": surface, "start": start, "end": start + len([]rune(surface)),
		})
	}
	for _, surface := range ids.UniqueOrdered(surfaces) {
		if surface == "我" || surface == "I" || surface == "me" || surface == "we" {
			continue
		}
		present := false
		for _, row := range mentionRows {
			if row["surface"] == surface {
				present = true
				break
			}
		}
		if !present {
			unresolved = append(unresolved, map[string]any{"surface": surface})
		}
	}
	if len(assertions) > 12 {
		assertions = assertions[:12]
	}
	if len(unresolved) > 20 {
		unresolved = unresolved[:20]
	}
	warning := ""
	if len(assertions) > 0 {
		warning = "rule extractor: conservative patterns only; use an LLM or structured remember for full coverage"
	}
	return Extraction{
		Assertions: assertions,
		Unresolved: unresolved,
		Extractor:  "rule-v1",
		Warning:    warning,
	}, nil
}

// OpenAILLM is a single-step chat-completions extractor (temperature 0, one
// call) plus the optional deep-recall query rewrite.
type OpenAILLM struct {
	config  *config.Config
	name    string
	headers map[string]string
	timeout time.Duration
}

// NewOpenAILLM builds the client and resolves the API key.
func NewOpenAILLM(cfg *config.Config) (*OpenAILLM, error) {
	key, err := cfg.LLMKey()
	if err != nil {
		return nil, err
	}
	if key == "" {
		return nil, configError("llm provider 'openai' requires an API key")
	}
	headers := httpclient.Bearer(key)
	timeout := time.Duration(cfg.LLM.TimeoutSec * float64(time.Second))
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &OpenAILLM{
		config:  cfg,
		name:    "openai:" + cfg.LLM.Model,
		headers: headers,
		timeout: timeout,
	}, nil
}

// Name returns the extractor identifier stored on receipts.
func (o *OpenAILLM) Name() string { return o.name }

// Extract sends one chat-completions request and normalizes the JSON object.
func (o *OpenAILLM) Extract(text, speaker string, timeRef int64) (Extraction, error) {
	passage := ids.Truncate(ids.StripInjected(text), 12000, "")
	if passage == "" {
		return Extraction{Extractor: o.name}, nil
	}
	if speaker == "" {
		speaker = "unknown"
	}
	parsed, err := o.chat(map[string]any{
		"messages": []map[string]any{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": fmt.Sprintf("speaker: %s\ntime_ref: %d\npassage:\n%s", speaker, timeRef, passage)},
		},
		"max_tokens": o.config.LLM.MaxTokens,
	})
	if err != nil {
		return Extraction{}, err
	}
	return normalizeLLMOutput(parsed, o.name), nil
}

// QueryHints is the one explicit LLM rewrite for recall(deep=true).
func (o *OpenAILLM) QueryHints(cue string) (map[string]string, error) {
	if o.config == nil || !strings.EqualFold(strings.TrimSpace(o.config.Mode), "cloud") {
		return nil, llmErrorf("local_plain recall blocks remote query rewriting")
	}
	prompt := strings.TrimSpace(cue)
	if prompt == "" {
		return map[string]string{}, nil
	}
	runes := []rune(prompt)
	if len(runes) > 2000 {
		prompt = string(runes[:2000])
	}
	parsed, err := o.chat(map[string]any{
		"messages": []map[string]any{
			{"role": "system", "content": querySystem},
			{"role": "user", "content": prompt},
		},
		"max_tokens": 200,
	})
	if err != nil {
		return nil, err
	}
	hints := map[string]string{}
	for _, key := range []string{"s", "x", "o"} {
		value := parsed[key]
		if item, ok := value.(map[string]any); ok {
			if label, ok := item["label"].(string); ok {
				value = label
			} else if literal, ok := item["literal"]; ok {
				value = literal
			}
		}
		if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
			hints[key] = strings.TrimSpace(text)
		}
	}
	return hints, nil
}

func (o *OpenAILLM) chat(body map[string]any) (map[string]any, error) {
	endpoint := strings.TrimRight(o.config.LLM.BaseURL, "/") + "/chat/completions"
	if err := o.config.CheckEgress(endpoint); err != nil {
		return nil, err
	}
	payload := map[string]any{
		"model":       o.config.LLM.Model,
		"temperature": 0,
	}
	for key, value := range body {
		payload[key] = value
	}
	if _, ok := payload["max_tokens"]; !ok {
		payload["max_tokens"] = o.config.LLM.MaxTokens
	}
	if _, ok := payload["response_format"]; !ok {
		payload["response_format"] = map[string]any{"type": "json_object"}
	}
	response, err := httpclient.PostJSON(endpoint, payload, o.headers, o.timeout, 1)
	if httpErr := (*httpclient.HTTPError)(nil); errors.As(err, &httpErr) &&
		httpErr.Status == 400 && strings.Contains(strings.ToLower(httpErr.Body), "response_format") {
		delete(payload, "response_format")
		response, err = httpclient.PostJSON(endpoint, payload, o.headers, o.timeout, 1)
	}
	if err != nil {
		return nil, llmErrorf("%s", err.Error())
	}
	choices, ok := response["choices"].([]any)
	if !ok || len(choices) == 0 {
		return nil, llmErrorf("LLM response has no choices")
	}
	first, _ := choices[0].(map[string]any)
	message, _ := first["message"].(map[string]any)
	content, _ := message["content"].(string)
	if strings.TrimSpace(content) == "" {
		return nil, llmErrorf("LLM response has no content")
	}
	parsed := ParseJSONObject(content)
	if parsed == nil {
		return nil, llmErrorf("LLM response is not a JSON object")
	}
	return parsed, nil
}

// BuildLLM returns (nil, warning) when no model is configured or reachable.
func BuildLLM(cfg *config.Config) (Extractor, *string) {
	if cfg.LLM.Provider == "none" {
		return nil, nil
	}
	client, err := NewOpenAILLM(cfg)
	if err != nil {
		message := "LLM unavailable, using local rule extractor: " + err.Error()
		return nil, &message
	}
	return client, nil
}

// ParseJSONObject strips code fences and decodes the first JSON object.
func ParseJSONObject(content string) map[string]any {
	text := strings.TrimSpace(content)
	if strings.HasPrefix(text, "```") {
		text = fenceStartRE.ReplaceAllString(text, "")
		text = fenceEndRE.ReplaceAllString(text, "")
	}
	parsed := map[string]any{}
	if err := json.Unmarshal([]byte(text), &parsed); err == nil {
		return parsed
	}
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start < 0 || end <= start {
		return nil
	}
	parsed = map[string]any{}
	if err := json.Unmarshal([]byte(text[start:end+1]), &parsed); err != nil {
		return nil
	}
	return parsed
}

func normalizeLLMOutput(parsed map[string]any, extractor string) Extraction {
	assertions := []map[string]any{}
	anchorFailed := 0
	if raw, ok := parsed["assertions"].([]any); ok {
		for index, item := range raw {
			if index >= 24 {
				break
			}
			row, ok := item.(map[string]any)
			if !ok {
				continue
			}
			subjectAbsent := false
			if b, ok := row["subject_absent"].(bool); ok && b {
				subjectAbsent = true
			}
			subject := slot(row["s"])
			object := slot(row["o"])
			relation := row["x"]
			if value, ok := relation.(map[string]any); ok {
				if label, ok := value["label"]; ok {
					relation = label
				} else {
					relation = value["literal"]
				}
			}
			relationText := ""
			if relation != nil {
				relationText = strings.TrimSpace(pyString(relation))
			}
			evidence, evidenceOK := row["evidence"].(string)
			if relationText == "" || (subjectAbsent && object == nil) || (!subjectAbsent && subject == nil) {
				anchorFailed++
				continue
			}
			if !evidenceOK || strings.TrimSpace(evidence) == "" {
				anchorFailed++
				continue
			}
			warrant := strings.ToLower(strings.TrimSpace(pyString(defaultAny(row["warrant"], "referential"))))
			switch warrant {
			case "referential", "performative", "procedural", "experiential":
			default:
				warrant = "referential"
			}
			world := optionalLLMString(row["world"])
			carrier := optionalLLMString(row["carrier"])
			locus := optionalLLMString(row["locus"])
			holder := optionalLLMString(row["holder"])
			outRow := map[string]any{
				"x":            relationText,
				"warrant":      warrant,
				"confidence":   ids.Clamp(pyFloat(defaultAny(row["confidence"], 0.5), 0.5), 0.0, 1.0),
				"evidence":     strings.TrimSpace(evidence),
				"t_valid_from": row["t_valid_from"],
				"world":        world,
				"carrier":      carrier,
				"locus":        locus,
				"holder":       holder,
			}
			if subjectAbsent {
				outRow["subject_absent"] = true
			} else {
				outRow["s"] = subject
			}
			if object != nil {
				outRow["o"] = object
			}
			if b, ok := row["closes_prior"].(bool); ok && b {
				outRow["closes_prior"] = true
			}
			assertions = append(assertions, outRow)
		}
	}
	unresolved := []map[string]any{}
	if raw, ok := parsed["unresolved"].([]any); ok {
		for index, item := range raw {
			if index >= 20 {
				break
			}
			switch typed := item.(type) {
			case map[string]any:
				if surface, ok := typed["surface"].(string); ok && strings.TrimSpace(surface) != "" {
					unresolved = append(unresolved, map[string]any{"surface": strings.TrimSpace(surface)})
				}
			case string:
				if strings.TrimSpace(typed) != "" {
					unresolved = append(unresolved, map[string]any{"surface": strings.TrimSpace(typed)})
				}
			}
		}
	}
	aliases := []map[string]any{}
	if raw, ok := parsed["aliases"].([]any); ok {
		for index, item := range raw {
			if index >= 24 {
				break
			}
			if typed, ok := item.(map[string]any); ok {
				of := strings.TrimSpace(pyString(typed["of"]))
				al := strings.TrimSpace(pyString(typed["alias"]))
				if of != "" && al != "" && !strings.EqualFold(of, al) {
					aliases = append(aliases, map[string]any{"of": of, "alias": al})
				}
			}
		}
	}
	return Extraction{
		Assertions:   assertions,
		Aliases:      aliases,
		Unresolved:   unresolved,
		AnchorFailed: anchorFailed,
		Extractor:    extractor,
	}
}

func slot(value any) map[string]any {
	switch typed := value.(type) {
	case string:
		if label := strings.TrimSpace(typed); label != "" {
			return map[string]any{"label": label}
		}
	case map[string]any:
		if literal, ok := typed["literal"]; ok {
			if isScalar(literal) && strings.TrimSpace(pyString(literal)) != "" {
				return map[string]any{"literal": strings.TrimSpace(pyString(literal))}
			}
		}
		if label, ok := typed["label"]; ok && isScalar(label) && strings.TrimSpace(pyString(label)) != "" {
			return map[string]any{"label": strings.TrimSpace(pyString(label))}
		}
	}
	return nil
}

func cleanEntity(value string) string {
	text := strings.Trim(value, " \t\r\n，。；！？,.;!?：:")
	text = leadingParticles.ReplaceAllString(text, "")
	text = trailingParticles.ReplaceAllString(text, "")
	return strings.TrimSpace(text)
}

func cleanObject(value string) string {
	text := strings.Trim(value, " \t\r\n，。；！？,.;!?：:")
	text = objectPrefix.ReplaceAllString(text, "")
	text = trailingParticles.ReplaceAllString(text, "")
	return strings.TrimSpace(text)
}

// labelStopwords are sentence-level function words; as a whole label they mark
// a chopped prose fragment, not an entity.
var labelStopwords = map[string]struct{}{
	"这": {}, "那": {}, "它": {}, "其": {}, "此": {}, "该": {}, "已": {}, "现": {}, "侧": {}, "所": {},
	"且": {}, "但": {}, "而": {}, "则": {}, "即": {}, "都": {}, "也": {}, "还": {}, "就": {}, "才": {},
	"又": {}, "再": {}, "很": {}, "太": {}, "更": {}, "最": {}, "只": {}, "刚": {}, "总": {}, "常": {},
	"之": {}, "的": {}, "了": {}, "着": {}, "地": {}, "得": {}, "与": {}, "及": {}, "或": {}, "和": {},
	"这些": {}, "那些": {}, "这个": {}, "那个": {}, "这些事": {}, "下面": {}, "上面": {}, "上述": {}, "下述": {},
	"其中": {}, "同样": {}, "而且": {}, "但是": {}, "可是": {}, "不过": {}, "只是": {}, "还是": {}, "或是": {},
	"以及": {}, "所以": {}, "因此": {}, "于是": {}, "然后": {}, "接着": {}, "同时": {}, "此外": {}, "另外": {},
	"反之": {}, "总之": {}, "综上": {}, "例如": {}, "比如": {}, "关于": {}, "对于": {}, "至于": {}, "几乎": {},
	"大约": {}, "大概": {}, "可能": {}, "应该": {}, "必须": {}, "需要": {}, "值得": {}, "可以": {}, "能够": {},
	"开始": {}, "继续": {}, "随后": {}, "最终": {}, "最后": {}, "首先": {}, "其次": {}, "再次": {}, "本来": {},
	"原来": {}, "其实": {}, "确实": {}, "当然": {}, "显然": {}, "从而": {}, "因而": {}, "甚至": {}, "尤其": {},
	"特别": {}, "一直": {}, "已经": {}, "曾经": {}, "从来": {}, "始终": {}, "不断": {}, "渐渐": {}, "终于": {},
	"究竟": {}, "毕竟": {}, "难道": {}, "无论": {}, "不管": {}, "只要": {}, "只有": {}, "除非": {}, "既然": {},
	"虽然": {}, "尽管": {}, "然而": {}, "况且": {}, "何况": {}, "即使": {}, "如果": {}, "假如": {}, "要是": {},
	"倘若": {}, "一旦": {}, "为了": {}, "由于": {}, "因为": {},
}

// plausibleLabel rejects fragment labels that the sentence patterns produce
// when they fire on prose (markdown/code noise, interrogatives, connector
// stubs, chopped heads, quoted spans, unbalanced brackets).
func plausibleLabel(value string) bool {
	if value == "" {
		return false
	}
	if interrogativeRE.MatchString(value) || labelNoiseRE.MatchString(value) {
		return false
	}
	if _, stop := labelStopwords[value]; stop {
		return false
	}
	if labelStubTailRE.MatchString(value) {
		return false
	}
	if labelStubHeadRE.MatchString(value) || labelQuoteRE.MatchString(value) {
		return false
	}
	return labelBracketsBalanced(value)
}

func labelBracketsBalanced(value string) bool {
	for _, pair := range [][2]string{
		{"(", ")"}, {"（", "）"}, {"「", "」"}, {"『", "』"}, {"【", "】"}, {"[", "]"}, {"《", "》"}, {"〈", "〉"},
	} {
		if strings.Count(value, pair[0]) != strings.Count(value, pair[1]) {
			return false
		}
	}
	return true
}

func resolveSubject(subject, speaker string) string {
	switch subject {
	case "I", "i", "me", "我":
		if speaker != "" && speaker != "user" && speaker != "assistant" && speaker != "system" && speaker != "human" && speaker != "ai" {
			return speaker
		}
		return "我"
	case "we", "We", "我们":
		if speaker != "" && speaker != "user" && speaker != "assistant" && speaker != "system" && speaker != "human" && speaker != "ai" {
			return speaker
		}
		return "我们"
	}
	return subject
}

func runeIndex(haystack, needle string) int {
	byteIndex := strings.Index(haystack, needle)
	if byteIndex < 0 {
		return -1
	}
	return len([]rune(haystack[:byteIndex]))
}

// ------------------------------------------------------------------ helpers

func isScalar(value any) bool {
	switch value.(type) {
	case string, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64, bool:
		return true
	}
	return false
}

func pyString(value any) string {
	switch typed := value.(type) {
	case nil:
		return "None"
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
	case float32:
		return strconv.FormatFloat(float64(typed), 'g', -1, 32)
	}
	return fmt.Sprint(value)
}

func pyFloat(value any, fallback float64) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case float32:
		return float64(typed)
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		if err == nil {
			return parsed
		}
	}
	return fallback
}

func optionalLLMString(value any) any {
	text, ok := value.(string)
	if !ok || strings.TrimSpace(text) == "" {
		return nil
	}
	return strings.TrimSpace(text)
}

func defaultAny(value, fallback any) any {
	if value == nil {
		return fallback
	}
	return value
}

func configError(message string) error {
	return fmt.Errorf("%s", message)
}
