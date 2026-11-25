// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

// Command collaboration measures assistant actions against synthetic memory cases.
package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"

	"github.com/rocareer/rolling-memory/internal/episodicunit"
)

const (
	scope = "diagnostic-mem-eval-atmbench-local-plain-collaboration"
	guide = "Choose only your next action. Call an available tool or answer directly if none is needed. Do not report a review of the tool."
)

var seeds = []int{17, 39, 71}

type scenario struct {
	ID      string `json:"id"`
	Lang    string `json:"lang"`
	Group   string `json:"group"`
	Want    string `json:"want"`
	Task    string `json:"task"`
	Source  string `json:"source"`
	Answer  string `json:"answer"`
	Control string `json:"control"`
	Unit    string `json:"unit"`
	Path    string `json:"path"`
}

type call struct {
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type observation struct {
	Arm           string `json:"arm"`
	ID            string `json:"id"`
	Seed          int    `json:"seed"`
	Calls         []call `json:"tool_calls"`
	Content       string `json:"content"`
	Finish        string `json:"finish"`
	RequestHash   string `json:"request_sha256"`
	RecallText    string `json:"recall_text,omitempty"`
	RecallError   bool   `json:"recall_error,omitempty"`
	SourceExcerpt string `json:"source_excerpt,omitempty"`
}

type counts struct {
	Attempts        int `json:"attempts"`
	CorrectAction   int `json:"correct_action"`
	Strict          int `json:"strict"`
	NeedsRecall     int `json:"needs_recall"`
	ChoseRecall     int `json:"chose_needed_recall"`
	NoRecallNeeded  int `json:"no_recall_needed"`
	Unnecessary     int `json:"unnecessary_recall"`
	EvidenceTrials  int `json:"evidence_trials"`
	EvidenceHits    int `json:"evidence_hits"`
	RequestedSource int `json:"source_trials"`
	MatchedSource   int `json:"correct_source"`
	SummaryOnly     int `json:"summary_only"`
	SourceReads     int `json:"verified_source_reads,omitempty"`
}

type subject struct {
	Version      string            `json:"version"`
	Revision     string            `json:"revision"`
	Modified     bool              `json:"modified"`
	BinarySHA    string            `json:"binary_sha256"`
	SkillSHA     map[string]string `json:"skill_sha256"`
	PromptSHA    string            `json:"prompt_sha256"`
	PromptTokens map[string]int    `json:"prompt_tokens"`
}

type modelInfo struct {
	ID          string  `json:"id"`
	WeightsSHA  string  `json:"weights_sha256"`
	Server      string  `json:"server"`
	Temperature float64 `json:"temperature"`
	TopP        float64 `json:"top_p"`
	MaxTokens   int     `json:"max_tokens"`
}

type receipt struct {
	Format       int                          `json:"format"`
	Mode         string                       `json:"mode"`
	SuiteSHA     string                       `json:"suite_sha256"`
	CorpusSHA    string                       `json:"corpus_sha256"`
	RowsSHA      string                       `json:"rows_sha256"`
	Scope        string                       `json:"scope"`
	PulseMode    string                       `json:"pulse_mode"`
	Model        modelInfo                    `json:"model"`
	Seeds        []int                        `json:"seeds"`
	Subjects     map[string]subject           `json:"subjects"`
	Results      map[string]map[string]counts `json:"results"`
	StablePasses map[string]int               `json:"all_seeds_passed"`
}

func digest(b []byte) string {
	return fmt.Sprintf("%x", sha256.Sum256(b))
}

func validDigest(s string, size int) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == size
}

func readSuite(path string) ([]scenario, string, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}
	var cases []scenario
	if err := json.Unmarshal(body, &cases); err != nil {
		return nil, "", err
	}
	if len(cases) == 0 {
		return nil, "", errors.New("empty suite")
	}
	ids := map[string]bool{}
	for _, c := range cases {
		if c.ID == "" || ids[c.ID] || c.Task == "" || c.Group == "" || (c.Lang != "en" && c.Lang != "zh") {
			return nil, "", fmt.Errorf("invalid case %q", c.ID)
		}
		ids[c.ID] = true
		switch c.Want {
		case "recall", "respond", "inspect", "read_source":
		default:
			return nil, "", fmt.Errorf("unknown action in %s", c.ID)
		}
		if isFact(c) && (c.Want != "recall" || c.Control == "" || c.Answer == "" || !strings.Contains(c.Source, c.Answer) || strings.Contains(c.Task+c.Control, c.Answer)) {
			return nil, "", fmt.Errorf("missing or exposed evidence marker in %s", c.ID)
		}
		if isRecovery(c) {
			if c.Want != "read_source" || strings.Count(c.Task, "{{excerpt}}") != 1 || c.Unit != "" || c.Control == "" || c.Source == "" || c.Answer == "" || strings.Contains(c.Task+c.Control+c.Source, c.Answer) {
				return nil, "", fmt.Errorf("invalid source recovery fixture in %s", c.ID)
			}
		} else if c.Want == "read_source" && c.Unit == "" {
			return nil, "", fmt.Errorf("missing source ID in %s", c.ID)
		}
		if c.Want == "inspect" && len(casePaths(c)) == 0 {
			return nil, "", fmt.Errorf("missing inspect path in %s", c.ID)
		}
	}
	return cases, digest(body), nil
}

var quotedPath = regexp.MustCompile("`([^`]+/[^`]+)`")

func pathPart(path string) string {
	return strings.SplitN(strings.SplitN(strings.TrimPrefix(strings.TrimSpace(path), "./"), ":", 2)[0], "#", 2)[0]
}

func casePaths(c scenario) []string {
	if c.Path != "" {
		return []string{pathPart(c.Path)}
	}
	var paths []string
	for _, v := range quotedPath.FindAllStringSubmatch(c.Task, -1) {
		paths = append(paths, pathPart(v[1]))
	}
	if strings.Contains(c.Task, "`config.toml`") {
		paths = append(paths, "config.toml")
	}
	return paths
}

func classify(c scenario, row observation) counts {
	m := counts{Attempts: 1}
	if c.Want == "recall" {
		m.NeedsRecall = 1
	} else {
		m.NoRecallNeeded = 1
	}
	if isFact(c) {
		m.EvidenceTrials = 1
	}
	if c.Want == "read_source" {
		m.RequestedSource = 1
	}
	action := "respond"
	var args map[string]any
	if len(row.Calls) > 0 {
		action = chosenAction(row.Calls[0])
		_ = json.Unmarshal([]byte(row.Calls[0].Function.Arguments), &args)
	}
	for _, call := range row.Calls {
		if chosenAction(call) == "recall" && c.Want != "recall" {
			m.Unnecessary = 1
		}
	}
	if c.Want == "recall" && action == "recall" {
		m.ChoseRecall = 1
	}
	if isFact(c) && action == "recall" && !row.RecallError {
		mode, _ := args["mode"].(string)
		if mode != "" && mode != "full" {
			// A compact receipt is not the source excerpt. Even when its summary
			// happens to retain the marker, it must not read as delivered evidence.
			m.SummaryOnly = 1
		} else if strings.Contains(row.RecallText, c.Answer) {
			m.EvidenceHits = 1
		}
	}
	if action != c.Want || len(row.Calls) > 1 {
		return m
	}
	m.CorrectAction = 1
	ok := false
	switch action {
	case "respond":
		ok = strings.TrimSpace(row.Content) != "" && row.Finish != "length"
	case "recall":
		cue, _ := args["cue"].(string)
		ok = len(args) == 1 && strings.TrimSpace(cue) != "" && (!isFact(c) || m.EvidenceHits == 1)
	case "inspect":
		p, _ := args["path"].(string)
		for _, path := range casePaths(c) {
			ok = ok || pathPart(p) == path
		}
	case "read_source":
		if isRecovery(c) {
			id := recoveryID(c)
			if len(args) == 1 && args["unit_id"] == id {
				m.MatchedSource = 1
				if row.Calls[0].Function.Name == "recall" && !row.RecallError && hasRecoveryExcerpt(c, row.SourceExcerpt) && recoveredOriginal(c, row.RecallText) {
					m.SourceReads = 1
					ok = true
				}
			}
		} else {
			ok = args["unit_id"] == c.Unit
			if ok {
				m.MatchedSource = 1
			}
		}
	}
	if ok {
		m.Strict = 1
	}
	return m
}

func chosenAction(c call) string {
	if c.Function.Name == "recall" {
		var args map[string]json.RawMessage
		if json.Unmarshal([]byte(c.Function.Arguments), &args) == nil && args["unit_id"] != nil {
			return "read_source"
		}
	}
	return c.Function.Name
}

func isRecovery(c scenario) bool { return c.Group == "source_recovery" }

func recoverySource(c scenario) string {
	var body strings.Builder
	body.WriteString(c.Source)
	for i := range 28 {
		fmt.Fprintf(&body, " This line records a routine review under reference %d.", i+1)
	}
	body.WriteByte(' ')
	body.WriteString(c.Answer)
	return body.String()
}

func recoveryBody(c scenario) string { return "[assistant] " + recoverySource(c) }

func recoveryID(c scenario) string { return episodicunit.DocID(scope, recoveryBody(c)) }

func hasRecoveryExcerpt(c scenario, text string) bool {
	for _, line := range strings.Split(text, "\n") {
		if _, handles, ok := strings.Cut(line, "已核验命中片段: "); ok && slices.Contains(strings.Split(handles, ", "), recoveryID(c)) {
			return strings.Contains(text, "正文已缩短") && !strings.Contains(text, c.Answer)
		}
	}
	return false
}

func recoveredOriginal(c scenario, text string) bool {
	header, body, ok := strings.Cut(text, "\n\n")
	return ok && strings.Contains(header, recoveryID(c)) && body == recoveryBody(c)
}

func isFact(c scenario) bool {
	return c.Group == "historical" || c.Group == "excerpt"
}

func add(a, b counts) counts {
	return counts{a.Attempts + b.Attempts, a.CorrectAction + b.CorrectAction, a.Strict + b.Strict, a.NeedsRecall + b.NeedsRecall,
		a.ChoseRecall + b.ChoseRecall, a.NoRecallNeeded + b.NoRecallNeeded, a.Unnecessary + b.Unnecessary,
		a.EvidenceTrials + b.EvidenceTrials, a.EvidenceHits + b.EvidenceHits, a.RequestedSource + b.RequestedSource, a.MatchedSource + b.MatchedSource,
		a.SummaryOnly + b.SummaryOnly, a.SourceReads + b.SourceReads}
}

func aggregate(cases []scenario, rows []byte, r *receipt) error {
	type key struct {
		arm, id string
		seed    int
	}
	cs := map[string]scenario{}
	for _, c := range cases {
		cs[c.ID] = c
	}
	validSeed := map[int]bool{}
	for _, seed := range seeds {
		validSeed[seed] = true
	}
	seen := map[key]bool{}
	strict := map[[2]string]int{}
	r.Results = map[string]map[string]counts{}
	r.StablePasses = map[string]int{}
	s := bufio.NewScanner(bytes.NewReader(rows))
	s.Buffer(make([]byte, 4096), 4<<20)
	for s.Scan() {
		var row observation
		if err := json.Unmarshal(s.Bytes(), &row); err != nil {
			return err
		}
		c, knownCase := cs[row.ID]
		_, knownArm := r.Subjects[row.Arm]
		k := key{row.Arm, row.ID, row.Seed}
		if !knownCase || !knownArm || !validSeed[row.Seed] || seen[k] || !validDigest(row.RequestHash, 32) {
			return fmt.Errorf("invalid or duplicate row: %+v", k)
		}
		seen[k] = true
		m := classify(c, row)
		strict[[2]string{row.Arm, row.ID}] += m.Strict
		if r.Results[row.Arm] == nil {
			r.Results[row.Arm] = map[string]counts{}
		}
		groups := r.Results[row.Arm]
		for _, group := range []string{"all", "group:" + c.Group, "lang:" + c.Lang} {
			groups[group] = add(groups[group], m)
		}
	}
	if err := s.Err(); err != nil {
		return err
	}
	if len(seen) != len(cases)*len(seeds)*len(r.Subjects) {
		return fmt.Errorf("incomplete run: got %d rows", len(seen))
	}
	for k, n := range strict {
		if n == len(seeds) {
			r.StablePasses[k[0]]++
		}
	}
	return nil
}

func fileDigest(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

func skillFiles(dir string) map[string]string {
	return map[string]string{"zh": filepath.Join(dir, "SKILL.md"), "en": filepath.Join(dir, "references", "SKILL.en.md")}
}

func verify(path, revision, version, suite, skills string, release bool) (*receipt, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var r receipt
	if err = json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	cases, sha, err := readSuite(suite)
	if err != nil {
		return nil, err
	}
	rows, err := os.ReadFile(filepath.Join(filepath.Dir(path), "observations.jsonl"))
	if err != nil {
		return nil, err
	}
	if r.Format != 1 || r.Scope != scope || r.PulseMode != "local_plain" || r.SuiteSHA != sha || r.RowsSHA != digest(rows) || r.CorpusSHA != digest(corpus(cases)) || r.Model.ID == "" || !validDigest(r.Model.WeightsSHA, 32) || r.Model.Server == "" || r.Model.Temperature != 0.2 || r.Model.TopP != 0.9 || r.Model.MaxTokens != 256 || !slices.Equal(r.Seeds, seeds) {
		return nil, errors.New("diagnostic receipt does not match its data or protocol")
	}
	if r.Mode != "single" && r.Mode != "paired" {
		return nil, errors.New("unknown diagnostic mode")
	}
	current, ok := r.Subjects["current"]
	if !ok || r.Mode == "single" && len(r.Subjects) != 1 || r.Mode == "paired" && (len(r.Subjects) != 2 || r.Subjects["baseline"].Version == "") {
		return nil, errors.New("invalid diagnostic arms")
	}
	for _, sub := range r.Subjects {
		if sub.Version == "" || !validDigest(sub.BinarySHA, 32) || !validDigest(sub.PromptSHA, 32) {
			return nil, errors.New("missing subject identity")
		}
	}
	if release {
		if !validDigest(revision, 20) || version == "" || current.Revision != revision || current.Modified || current.Version != version {
			return nil, errors.New("diagnostic binary is not the clean release revision and version")
		}
		for lang, file := range skillFiles(skills) {
			h, err := fileDigest(file)
			if err != nil {
				return nil, err
			}
			if current.SkillSHA[lang] != h {
				return nil, fmt.Errorf("diagnostic %s skill is not the release copy", lang)
			}
		}
	}
	reported, stable := r.Results, r.StablePasses
	if err := aggregate(cases, rows, &r); err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(reported, r.Results) || !reflect.DeepEqual(stable, r.StablePasses) {
		return nil, errors.New("reported counts disagree with observations")
	}
	return &r, nil
}

func entry() error {
	binary := flag.String("binary", "", "Rolling Memory executable to test")
	skills := flag.String("skills", "skills/rolling-memory", "directory of the tested skill editions")
	baseline := flag.String("baseline", "", "optional baseline executable")
	baseSkills := flag.String("baseline-skills", "", "directory of the baseline skill editions")
	suite := flag.String("suite", "diagnostics/collaboration/cases-v5.json", "synthetic scenario file")
	endpoint := flag.String("endpoint", "http://127.0.0.1:32190", "local model server base URL")
	model := flag.String("model", "local", "model name supplied to the server")
	weights := flag.String("weights", "", "local model weights file for the receipt hash")
	server := flag.String("server", "", "model server build label for the receipt")
	out := flag.String("out", "", "new directory for run files")
	review := flag.String("review", "", "recompute a receipt from observed actions")
	release := flag.String("verify-release", "", "verify a receipt and bind it to a release revision")
	revision := flag.String("revision", "", "required release revision")
	version := flag.String("version", "", "required release version")
	flag.Parse()
	if *review != "" || *release != "" {
		path := *review
		if *release != "" {
			path = *release
		}
		r, err := verify(path, *revision, *version, *suite, *skills, *release != "")
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(r)
	}
	if *binary == "" || *out == "" || *weights == "" || *server == "" || (*baseline == "") != (*baseSkills == "") {
		return errors.New("run requires -binary, -out, -weights, -server and both baseline flags if paired")
	}
	return run(*binary, *skills, *baseline, *baseSkills, *suite, *endpoint, *model, *weights, *server, *out)
}

func main() {
	if err := entry(); err != nil {
		fmt.Fprintln(os.Stderr, "collaboration:", err)
		os.Exit(1)
	}
}
