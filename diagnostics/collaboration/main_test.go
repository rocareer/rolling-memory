// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func toolCall(name, args string) call {
	var c call
	c.Function.Name, c.Function.Arguments = name, args
	return c
}

func TestClassifySeparatesSelectionArgumentsAndEvidence(t *testing.T) {
	c := scenario{Want: "recall", Group: "historical", Answer: "marker"}
	for _, tc := range []struct {
		name, args, text    string
		bad                 bool
		wantStrict, wantHit int
	}{
		{"direct", `{"cue":"Friday handover"}`, "source marker", false, 1, 1},
		{"extra default", `{"cue":"Friday handover","mode":"full"}`, "source marker", false, 0, 1},
		{"miss", `{"cue":"Friday handover"}`, "wrong record", false, 0, 0},
		{"error text", `{"cue":"Friday handover"}`, "error contains marker", true, 0, 0},
		{"missing query", `{}`, "", false, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := classify(c, observation{Calls: []call{toolCall("recall", tc.args)}, RecallText: tc.text, RecallError: tc.bad})
			if got.Strict != tc.wantStrict || got.EvidenceHits != tc.wantHit || got.ChoseRecall != 1 {
				t.Fatalf("got %+v", got)
			}
		})
	}
	// A compact receipt is not the source excerpt: it must not read as delivered
	// evidence even when its summary happens to retain the marker.
	got := classify(c, observation{Calls: []call{toolCall("recall", `{"cue":"Friday handover","mode":"probe"}`)}, RecallText: "source marker"})
	if got.SummaryOnly != 1 || got.EvidenceHits != 0 || got.Strict != 0 {
		t.Fatalf("probe receipt counted as evidence: %+v", got)
	}
	current := scenario{Want: "inspect", Path: "pkg/worker.go"}
	got = classify(current, observation{Calls: []call{toolCall("inspect", `{"path":"pkg/worker.go:17"}`), toolCall("recall", `{"cue":"worker"}`)}})
	if got.Unnecessary != 1 || got.Strict != 0 {
		t.Fatalf("parallel unnecessary recall hidden: %+v", got)
	}
	got = classify(current, observation{Calls: []call{toolCall("inspect", `{"path":"pkg"}`)}})
	if got.Strict != 0 {
		t.Fatalf("partial path awarded credit: %+v", got)
	}
}

func TestLocalEndpointRejectsRemoteAndCredentials(t *testing.T) {
	for _, base := range []string{"https://example.com", "http://example.com", "http://127.0.0.1.example.com", "http://127.0.0.1:80/extra", "http://user@127.0.0.1", "http://127.0.0.1?query=1"} {
		if u, err := localURL(base); err == nil {
			t.Errorf("accepted %s as %s", base, u)
		}
	}
	for _, base := range []string{"http://127.0.0.1:32190", "http://[::1]:32190"} {
		if u, err := localURL(base); err != nil || !strings.HasSuffix(u, "/v1/chat/completions") {
			t.Errorf("got %q: %v", u, err)
		}
	}
	if _, ok := safeRecallArgs(observation{Calls: []call{toolCall("recall", `{"cue":"known","scope":"other"}`)}}); ok {
		t.Fatal("scope override escaped the sandbox")
	}
}

func TestReceiptRequiresCompleteUniqueRowsAndReleaseIdentity(t *testing.T) {
	dir := t.TempDir()
	suite := filepath.Join(dir, "suite.json")
	caseBody := []byte(`[{"id":"N","lang":"en","group":"current","want":"respond","task":"Use the supplied value."}]`)
	if err := os.WriteFile(suite, caseBody, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "references"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, file := range skillFiles(dir) {
		if err := os.WriteFile(file, []byte("expected skill"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var rows bytes.Buffer
	enc := json.NewEncoder(&rows)
	for _, seed := range seeds {
		if err := enc.Encode(observation{Arm: "current", ID: "N", Seed: seed, Content: "The supplied value.", Finish: "stop", RequestHash: digest([]byte("request"))}); err != nil {
			t.Fatal(err)
		}
	}
	rev := strings.Repeat("a", 40)
	r := receipt{Format: 1, Mode: "single", SuiteSHA: digest(caseBody), CorpusSHA: digest(nil), Scope: scope, PulseMode: "local_plain", Model: modelInfo{"local", digest([]byte("weights")), "local test", 0.2, 0.9, 256}, Seeds: seeds, Subjects: map[string]subject{"current": {Version: "0.1.0", Revision: rev, BinarySHA: digest([]byte("binary")), PromptSHA: digest([]byte("prompt")), SkillSHA: map[string]string{"en": digest([]byte("expected skill")), "zh": digest([]byte("expected skill"))}}}}
	path := filepath.Join(dir, "receipt.json")
	put := func(raw []byte) {
		t.Helper()
		r.RowsSHA = digest(raw)
		if err := os.WriteFile(filepath.Join(dir, "observations.jsonl"), raw, 0600); err != nil {
			t.Fatal(err)
		}
		b, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := aggregate([]scenario{{ID: "N", Lang: "en", Group: "current", Want: "respond"}}, rows.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	put(rows.Bytes())
	got, err := verify(path, rev, "0.1.0", suite, dir, true)
	if err != nil || got.Results["current"]["all"].Strict != 3 {
		t.Fatalf("valid receipt: %+v %v", got, err)
	}
	if _, err := verify(path, strings.Repeat("b", 40), "0.1.0", suite, dir, true); err == nil {
		t.Fatal("accepted wrong release revision")
	}
	strict := r.Results["current"]["all"]
	altered := strict
	altered.Strict++
	r.Results["current"]["all"] = altered
	put(rows.Bytes())
	if _, err := verify(path, rev, "0.1.0", suite, dir, true); err == nil {
		t.Fatal("accepted inflated summary with unchanged observations")
	}
	r.Results["current"]["all"] = strict
	put(rows.Bytes()[:bytes.IndexByte(rows.Bytes(), '\n')+1])
	if _, err := verify(path, rev, "0.1.0", suite, dir, true); err == nil {
		t.Fatal("accepted incomplete observations with updated digest")
	}
	put(append(bytes.Clone(rows.Bytes()), rows.Bytes()...))
	if _, err := verify(path, rev, "0.1.0", suite, dir, true); err == nil {
		t.Fatal("accepted duplicate observations")
	}
}

func TestSourceRecoveryRequiresTheVerbatimRead(t *testing.T) {
	c := scenario{Group: "source_recovery", Want: "read_source", Source: "The ordinary rule.", Answer: "One final condition applies."}
	id := recoveryID(c)
	row := observation{
		Calls:         []call{toolCall("recall", `{"unit_id":"`+id+`"}`)},
		SourceExcerpt: "（正文已缩短）\n  已核验命中片段: " + id,
		RecallText:    "Conversation source unit " + id + ":\n\n" + recoveryBody(c),
	}
	if got := classify(c, row); got.MatchedSource != 1 || got.SourceReads != 1 || got.Strict != 1 || got.Unnecessary != 0 {
		t.Fatalf("exact recovery: %+v", got)
	}
	for _, broken := range []observation{
		{Calls: row.Calls, SourceExcerpt: row.SourceExcerpt, RecallText: "summary " + id + ":\n\n" + c.Answer},
		{Calls: row.Calls, RecallText: row.RecallText},
		{Calls: row.Calls, SourceExcerpt: row.SourceExcerpt, RecallText: row.RecallText, RecallError: true},
		{Calls: []call{toolCall("read_source", `{"unit_id":"`+id+`"}`)}, SourceExcerpt: row.SourceExcerpt, RecallText: row.RecallText},
	} {
		if got := classify(c, broken); got.MatchedSource != 1 || got.SourceReads != 0 || got.Strict != 0 {
			t.Fatalf("unverified read got credit: %+v", got)
		}
	}
}

func TestPublicSuiteIsFrozen(t *testing.T) {
	cases, sha, err := readSuite("cases-v4.json")
	if err != nil {
		t.Fatal(err)
	}
	if sha != "fbaa2a1515de4371c45194b99e8e403be4c7c218835c3764d265f68862905014" {
		t.Fatalf("suite digest changed: %s; use a new suite version", sha)
	}
	langs := map[string]int{}
	for _, c := range cases {
		langs[c.Lang]++
	}
	if langs["zh"] != 26 || langs["en"] != 26 {
		t.Fatalf("unexpected language coverage: %v", langs)
	}
	cases, sha, err = readSuite("cases-v5.json")
	if err != nil {
		t.Fatal(err)
	}
	if sha != "2a53e33c3ca31b7c71f18c1086b90a092d47b6ee1b8d07a9e9401fd320c78a40" || len(cases) != 50 {
		t.Fatalf("v5 suite changed: %s (%d cases); use a new suite version", sha, len(cases))
	}
}
