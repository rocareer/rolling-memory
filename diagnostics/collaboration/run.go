// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package main

import (
	"bytes"
	"debug/buildinfo"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/rocareer/rolling-memory/sdk/go/public/tokenzr"
)

type sandbox struct {
	dir string
	env []string
}

func (s sandbox) command(bin string, input []byte, args ...string) ([]byte, error) {
	cmd := exec.Command(bin, args...)
	cmd.Env, cmd.Dir, cmd.Stdin = s.env, s.dir, bytes.NewReader(input)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	b, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s: %w: %s", strings.Join(args, " "), err, stderr.String())
	}
	return b, nil
}

func (s sandbox) rpc(bin string, requests ...map[string]any) ([]json.RawMessage, error) {
	var input bytes.Buffer
	enc := json.NewEncoder(&input)
	for i, r := range requests {
		r["jsonrpc"] = "2.0"
		r["id"] = i + 1
		if err := enc.Encode(r); err != nil {
			return nil, err
		}
	}
	b, err := s.command(bin, input.Bytes(), "mcp")
	if err != nil {
		return nil, err
	}
	var out []json.RawMessage
	d := json.NewDecoder(bytes.NewReader(b))
	for i := range requests {
		var v struct {
			ID     int
			Result json.RawMessage
			Error  json.RawMessage
		}
		if err := d.Decode(&v); err != nil {
			return nil, err
		}
		if v.ID != i+1 || len(v.Result) == 0 || len(v.Error) > 0 {
			return nil, fmt.Errorf("invalid MCP reply: %s", b)
		}
		out = append(out, v.Result)
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return nil, errors.New("extra MCP response")
	}
	return out, nil
}

func (s sandbox) recall(bin string, args map[string]any) (string, bool, error) {
	v, err := s.rpc(bin, map[string]any{"method": "tools/call", "params": map[string]any{"name": "recall", "arguments": args}})
	if err != nil {
		return "", false, err
	}
	var r struct {
		IsError bool `json:"isError"`
		Content []struct{ Text string }
	}
	if err := json.Unmarshal(v[0], &r); err != nil {
		return "", false, err
	}
	if len(r.Content) != 1 {
		return "", false, errors.New("unexpected recall content")
	}
	return r.Content[0].Text, r.IsError, nil
}

func corpus(cases []scenario) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	n := 0
	for _, c := range cases {
		if !isFact(c) {
			continue
		}
		for j, texts := range [][2]string{{c.Control, c.Source}, {c.Control + " status ping", strings.Fields(c.Control)[0] + " status check only; this record did not decide the question."}} {
			for i, role := range []string{"user", "assistant"} {
				ts := time.Date(2026, 10, 20, 10, n, j*30+i, 0, time.UTC).Format(time.RFC3339)
				_ = enc.Encode(map[string]any{"type": role, "sessionId": c.ID, "timestamp": ts, "message": map[string]any{"role": role, "stop_reason": "end_turn", "content": texts[i]}})
			}
		}
		n++
	}
	return b.Bytes()
}

func (s sandbox) prepare(bin string, cases []scenario) error {
	path := filepath.Join(s.dir, "synthetic.jsonl")
	if err := os.WriteFile(path, corpus(cases), 0600); err != nil {
		return err
	}
	for _, args := range [][]string{{"init", "--json"}, {"import", path, "--json"}, {"build", "--json"}} {
		b, err := s.command(bin, nil, args...)
		if err != nil {
			return err
		}
		var r struct {
			OK        bool
			Published bool
			Inserted  int
		}
		if err = json.Unmarshal(b, &r); err != nil {
			return err
		}
		if !r.OK || args[0] == "build" && !r.Published || args[0] == "import" && r.Inserted != bytes.Count(corpus(cases), []byte("\n")) {
			return fmt.Errorf("fixture preparation failed: %s", b)
		}
	}
	for _, c := range cases {
		if !isFact(c) {
			continue
		}
		text, bad, err := s.recall(bin, map[string]any{"cue": c.Control, "mode": "full"})
		if err != nil {
			return err
		}
		if bad || !strings.Contains(text, c.Answer) {
			return fmt.Errorf("positive fixture control %s failed", c.ID)
		}
	}
	text, bad, err := s.recall(bin, map[string]any{"cue": "vvxjjzxwvvwvvjzz", "mode": "full"})
	if err != nil {
		return err
	}
	if bad || !strings.Contains(text, `absent="true"`) {
		return errors.New("negative fixture control failed")
	}
	return nil
}

type function struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}
type tool struct {
	Type     string   `json:"type"`
	Function function `json:"function"`
}
type prompt struct {
	System map[string]string
	Tools  []tool
}

type subjectRun struct {
	bin    string
	skills string
	prompt prompt
}

func auxiliaryTool(name, description, arg string) tool {
	schema, _ := json.Marshal(map[string]any{"type": "object", "properties": map[string]any{arg: map[string]string{"type": "string"}}, "required": []string{arg}})
	return tool{"function", function{name, description, schema}}
}

func (s sandbox) snapshot(bin, skills string) (subjectRun, subject, error) {
	var v subject
	v.SkillSHA = map[string]string{}
	v.PromptTokens = map[string]int{}
	abs, err := filepath.Abs(bin)
	if err != nil {
		return subjectRun{}, v, err
	}
	v.BinarySHA, err = fileDigest(abs)
	if err != nil {
		return subjectRun{}, v, err
	}
	b, err := s.command(abs, nil, "--version")
	if err != nil {
		return subjectRun{}, v, err
	}
	v.Version = strings.TrimSpace(strings.TrimPrefix(string(b), "rolling-memory "))
	v.Modified = true
	if info, err := buildinfo.ReadFile(abs); err == nil {
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				v.Revision = setting.Value
			case "vcs.modified":
				v.Modified = setting.Value != "false"
			}
		}
	}
	response, err := s.rpc(abs, map[string]any{"method": "initialize", "params": map[string]string{"protocolVersion": "2024-11-05"}}, map[string]any{"method": "tools/list"})
	if err != nil {
		return subjectRun{}, v, err
	}
	var init struct{ Instructions string }
	var list struct {
		Tools []struct {
			Name        string
			Description string
			Schema      json.RawMessage `json:"inputSchema"`
		}
	}
	if err = json.Unmarshal(response[0], &init); err != nil {
		return subjectRun{}, v, err
	}
	if err = json.Unmarshal(response[1], &list); err != nil {
		return subjectRun{}, v, err
	}
	if len(list.Tools) != 1 || list.Tools[0].Name != "recall" || init.Instructions == "" {
		return subjectRun{}, v, errors.New("expected the recall tool and MCP instructions")
	}
	t := list.Tools[0]
	p := prompt{System: map[string]string{}, Tools: []tool{{"function", function{t.Name, t.Description, t.Schema}}, auxiliaryTool("inspect", "Inspect a known path in the current workspace.", "path"), auxiliaryTool("read_source", "Read a cited local memory unit by its ID.", "unit_id")}}
	for lang, path := range skillFiles(skills) {
		data, err := os.ReadFile(path)
		if err != nil {
			return subjectRun{}, v, err
		}
		v.SkillSHA[lang] = digest(data)
		v.PromptTokens[lang] = tokenzr.Count(string(data))
		p.System[lang] = guide + "\n\n" + init.Instructions + "\n\n" + string(data)
	}
	var schema struct {
		Properties map[string]struct{ Description string }
	}
	if err := json.Unmarshal(t.Schema, &schema); err != nil {
		return subjectRun{}, v, err
	}
	descriptions := []string{init.Instructions, t.Description}
	for _, key := range []string{"cue", "delivery_window", "mode", "offset", "reader", "scope"} {
		descriptions = append(descriptions, schema.Properties[key].Description)
	}
	v.PromptTokens["mcp"] = tokenzr.Count(strings.Join(descriptions, "\n"))
	raw, _ := json.Marshal(p)
	v.PromptSHA = digest(raw)
	return subjectRun{abs, skills, p}, v, nil
}

func localURL(base string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme != "http" || ip == nil || !ip.IsLoopback() || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", errors.New("endpoint must be a loopback HTTP origin")
	}
	return strings.TrimRight(u.String(), "/") + "/v1/chat/completions", nil
}

var client = &http.Client{Timeout: 120 * time.Second, Transport: &http.Transport{}, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("model redirects are disabled") }}

func complete(endpoint string, model modelInfo, p prompt, c scenario, seed int) (observation, error) {
	body, err := json.Marshal(map[string]any{"model": model.ID, "messages": []map[string]string{{"role": "system", "content": p.System[c.Lang]}, {"role": "user", "content": c.Task}}, "tools": p.Tools, "tool_choice": "auto", "temperature": model.Temperature, "top_p": model.TopP, "max_tokens": model.MaxTokens, "seed": seed})
	if err != nil {
		return observation{}, err
	}
	resp, err := client.Post(endpoint, "application/json", bytes.NewReader(body))
	if err != nil {
		return observation{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return observation{}, fmt.Errorf("model request HTTP %d", resp.StatusCode)
	}
	var r struct {
		Choices []struct {
			Finish  string `json:"finish_reason"`
			Message struct {
				Content string
				Calls   []call `json:"tool_calls"`
			}
		}
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&r); err != nil {
		return observation{}, err
	}
	if len(r.Choices) != 1 {
		return observation{}, errors.New("expected one model choice")
	}
	choice := r.Choices[0]
	return observation{ID: c.ID, Seed: seed, Calls: choice.Message.Calls, Content: choice.Message.Content, Finish: choice.Finish, RequestHash: digest(body)}, nil
}

func safeRecallArgs(row observation) (map[string]any, bool) {
	if len(row.Calls) != 1 || row.Calls[0].Function.Name != "recall" {
		return nil, false
	}
	var args map[string]any
	if json.Unmarshal([]byte(row.Calls[0].Function.Arguments), &args) != nil {
		return nil, false
	}
	cue, _ := args["cue"].(string)
	if strings.TrimSpace(cue) == "" {
		return nil, false
	}
	for k := range args {
		switch k {
		case "cue", "mode", "delivery_window", "offset":
		default:
			return nil, false
		}
	}
	return args, true
}

func run(bin, skills, baseline, baseSkills, suite, baseURL, model, weights, server, out string) error {
	url, err := localURL(baseURL)
	if err != nil {
		return err
	}
	cases, suiteSHA, err := readSuite(suite)
	if err != nil {
		return err
	}
	weightSHA, err := fileDigest(weights)
	if err != nil {
		return err
	}
	if err = os.Mkdir(out, 0700); err != nil {
		return fmt.Errorf("output must be a new directory: %w", err)
	}
	dir, err := os.MkdirTemp("", "rm-collaboration.")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	home := filepath.Join(dir, "home")
	if err = os.Mkdir(home, 0700); err != nil {
		return err
	}
	env := []string{"HOME=" + home, "USERPROFILE=" + home, "PATH=" + os.Getenv("PATH"), "ROLLING_MEMORY_MODE=local", "ROLLING_MEMORY_DAEMON=0", "ROLLING_MEMORY_DB=" + filepath.Join(dir, "synthetic.db"), "ROLLING_MEMORY_SCOPE=" + scope, "XDG_DATA_HOME=" + filepath.Join(dir, "data"), "XDG_CONFIG_HOME=" + filepath.Join(dir, "config"), "XDG_STATE_HOME=" + filepath.Join(dir, "state"), "TMPDIR=" + dir, "TZ=UTC"}
	s := sandbox{dir, env}
	r := receipt{Format: 1, Mode: "single", SuiteSHA: suiteSHA, CorpusSHA: digest(corpus(cases)), Scope: scope, PulseMode: "local_plain", Model: modelInfo{model, weightSHA, server, 0.2, 0.9, 256}, Seeds: seeds, Subjects: map[string]subject{}}
	arms := []string{"current"}
	runs := map[string]subjectRun{}
	if baseline != "" {
		arms = []string{"baseline", "current"}
		r.Mode = "paired"
	}
	for _, arm := range arms {
		executable, skillDir := bin, skills
		if arm == "baseline" {
			executable, skillDir = baseline, baseSkills
		}
		state, subject, err := s.snapshot(executable, skillDir)
		if err != nil {
			return err
		}
		runs[arm], r.Subjects[arm] = state, subject
	}
	if err = s.prepare(runs["current"].bin, cases); err != nil {
		return err
	}
	rowsPath := filepath.Join(out, "observations.jsonl")
	f, err := os.OpenFile(rowsPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	type completed struct {
		row observation
		err error
	}
	for slot, seed := range seeds {
		order := append([]scenario(nil), cases...)
		rand.New(rand.NewSource(int64(seed))).Shuffle(len(order), func(i, j int) { order[i], order[j] = order[j], order[i] })
		for i := 0; i < len(order); i += 2 {
			batch := order[i:min(i+2, len(order))]
			for phase := range arms {
				jobs := make([]chan completed, 0, len(batch))
				for j, c := range batch {
					arm := arms[(slot+i/2+j+phase)%len(arms)]
					ch := make(chan completed, 1)
					jobs = append(jobs, ch)
					go func() {
						row, err := complete(url, r.Model, runs[arm].prompt, c, seed)
						row.Arm = arm
						ch <- completed{row, err}
					}()
				}
				for j, ch := range jobs {
					result := <-ch
					if result.err != nil {
						return fmt.Errorf("%s seed %d: %w", batch[j].ID, seed, result.err)
					}
					row := result.row
					if c := batch[j]; isFact(c) {
						if args, ok := safeRecallArgs(row); ok {
							row.RecallText, row.RecallError, err = s.recall(runs["current"].bin, args)
							if err != nil {
								return err
							}
						}
					}
					if err = enc.Encode(row); err != nil {
						return err
					}
				}
			}
		}
		fmt.Fprintf(os.Stderr, "completed seed %d (%d cases, %d arm(s))\n", seed, len(cases), len(arms))
	}
	if err = f.Close(); err != nil {
		return err
	}
	if _, err = os.Stat(filepath.Join(dir, "synthetic.db.crystallize.d")); !os.IsNotExist(err) {
		return errors.New("diagnostic recall created a consolidation queue")
	}
	for _, arm := range arms {
		sub := r.Subjects[arm]
		if hash, err := fileDigest(runs[arm].bin); err != nil || hash != sub.BinarySHA {
			return errors.New("diagnostic binary changed during run")
		}
		for lang, file := range skillFiles(runs[arm].skills) {
			if hash, err := fileDigest(file); err != nil || hash != sub.SkillSHA[lang] {
				return errors.New("diagnostic skill changed during run")
			}
		}
	}
	if hash, err := fileDigest(suite); err != nil || hash != suiteSHA {
		return errors.New("diagnostic suite changed during run")
	}
	rows, err := os.ReadFile(rowsPath)
	if err != nil {
		return err
	}
	r.RowsSHA = digest(rows)
	if err = aggregate(cases, rows, &r); err != nil {
		return err
	}
	body, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(out, "receipt.json"), append(body, '\n'), 0600); err != nil {
		return err
	}
	fmt.Println(string(body))
	return nil
}
