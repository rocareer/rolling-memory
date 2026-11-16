// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package ids

import (
	"bytes"
	"encoding/json"
	"net/url"
	"os"
	"reflect"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	previous := time.Local
	time.Local = time.UTC
	code := m.Run()
	time.Local = previous
	os.Exit(code)
}

func loadVector(t *testing.T, name string, target any) {
	t.Helper()
	raw, err := os.ReadFile("../../testdata/golden/vectors/" + name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		t.Fatalf("decode %s: %v", name, err)
	}
}

type idsVectors struct {
	AssertionHashes []struct {
		Args struct {
			AnchorTraceHash string  `json:"anchor_trace_hash"`
			Audience        string  `json:"audience"`
			Bare            bool    `json:"bare"`
			Carrier         *string `json:"carrier"`
			HolderID        *string `json:"holder_id"`
			Locus           *string `json:"locus"`
			ObjectKey       string  `json:"object_key"`
			RelID           string  `json:"rel_id"`
			Scope           string  `json:"scope"`
			SubjectKey      string  `json:"subject_key"`
			Warrant         string  `json:"warrant"`
			World           *string `json:"world"`
		} `json:"args"`
		Expected string `json:"expected"`
		Name     string `json:"name"`
	} `json:"assertion_hashes"`
	AssertionNodeIDs []struct {
		Expected     string `json:"expected"`
		IdentityHash string `json:"identity_hash"`
		Name         string `json:"name"`
	} `json:"assertion_node_ids"`
	Audience []struct {
		Left      string   `json:"left"`
		Narrower  string   `json:"narrower"`
		Right     string   `json:"right"`
		Audiences []string `json:"audiences"`
		Reader    string   `json:"reader"`
		Visible   bool     `json:"visible"`
	} `json:"audience"`
	Keys []struct {
		Expected string  `json:"expected"`
		Literal  *string `json:"literal"`
		Node     *string `json:"node"`
	} `json:"keys"`
	NodeIDs []struct {
		Expected  string `json:"expected"`
		Kind      string `json:"kind"`
		Name      string `json:"name"`
		NormLabel string `json:"norm_label"`
		Qualifier string `json:"qualifier"`
		Scope     string `json:"scope"`
	} `json:"node_ids"`
	RenderAssertion []struct {
		Expected string `json:"expected"`
		Object   string `json:"o"`
		Subject  string `json:"s"`
		Relation string `json:"x"`
	} `json:"render_assertion"`
	SHA256 []struct {
		Expected string `json:"expected"`
		Input    string `json:"input"`
	} `json:"sha256"`
	StableJSON []struct {
		Expected string `json:"expected"`
		Input    any    `json:"input"`
	} `json:"stable_json"`
	TraceHashes []struct {
		Expected string      `json:"expected"`
		Name     string      `json:"name"`
		Scope    string      `json:"scope"`
		Session  string      `json:"session"`
		Text     string      `json:"text"`
		Turn     json.Number `json:"turn"`
	} `json:"trace_hashes"`
	TurnNormalization []struct {
		Expected int64  `json:"expected"`
		Input    string `json:"input"`
	} `json:"turn_normalization"`
}

func TestGoldenIDs(t *testing.T) {
	var vectors idsVectors
	loadVector(t, "ids.json", &vectors)

	t.Run("sha256", func(t *testing.T) {
		for _, testCase := range vectors.SHA256 {
			if got := SHA256Hex(testCase.Input); got != testCase.Expected {
				t.Errorf("SHA256Hex(%q) = %q, want %q", testCase.Input, got, testCase.Expected)
			}
		}
	})

	t.Run("node_ids", func(t *testing.T) {
		for _, testCase := range vectors.NodeIDs {
			got := NodeID(testCase.Scope, testCase.Kind, testCase.NormLabel, testCase.Qualifier)
			if got != testCase.Expected {
				t.Errorf("%s: NodeID = %q, want %q", testCase.Name, got, testCase.Expected)
			}
		}
	})

	t.Run("trace_hashes", func(t *testing.T) {
		for _, testCase := range vectors.TraceHashes {
			turn, err := testCase.Turn.Int64()
			if err != nil {
				t.Fatalf("%s: bad turn: %v", testCase.Name, err)
			}
			got := TraceHash(testCase.Scope, testCase.Session, turn, testCase.Text)
			if got != testCase.Expected {
				t.Errorf("%s: TraceHash = %q, want %q", testCase.Name, got, testCase.Expected)
			}
		}
	})

	t.Run("turn_normalization", func(t *testing.T) {
		for _, testCase := range vectors.TurnNormalization {
			if got := NormalizeTurn(testCase.Input); got != testCase.Expected {
				t.Errorf("NormalizeTurn(%q) = %d, want %d", testCase.Input, got, testCase.Expected)
			}
		}
	})

	t.Run("assertion_hashes", func(t *testing.T) {
		for _, testCase := range vectors.AssertionHashes {
			got := AssertionHash(AssertionHashParts{
				Scope:           testCase.Args.Scope,
				SubjectKey:      testCase.Args.SubjectKey,
				RelID:           testCase.Args.RelID,
				ObjectKey:       testCase.Args.ObjectKey,
				Bare:            testCase.Args.Bare,
				HolderID:        valueOrEmpty(testCase.Args.HolderID),
				Warrant:         testCase.Args.Warrant,
				AnchorTraceHash: testCase.Args.AnchorTraceHash,
				Audience:        testCase.Args.Audience,
				World:           valueOrEmpty(testCase.Args.World),
				Carrier:         valueOrEmpty(testCase.Args.Carrier),
				Locus:           valueOrEmpty(testCase.Args.Locus),
			})
			if got != testCase.Expected {
				t.Errorf("%s: AssertionHash = %q, want %q", testCase.Name, got, testCase.Expected)
			}
		}
	})

	t.Run("assertion_node_ids", func(t *testing.T) {
		for _, testCase := range vectors.AssertionNodeIDs {
			if got := AssertionNodeID(testCase.IdentityHash); got != testCase.Expected {
				t.Errorf("%s: AssertionNodeID = %q, want %q", testCase.Name, got, testCase.Expected)
			}
		}
	})

	t.Run("keys", func(t *testing.T) {
		for _, testCase := range vectors.Keys {
			got := KeyForNodeOrLiteral(valueOrEmpty(testCase.Node), valueOrEmpty(testCase.Literal))
			if got != testCase.Expected {
				t.Errorf("KeyForNodeOrLiteral = %q, want %q", got, testCase.Expected)
			}
		}
	})

	t.Run("audience", func(t *testing.T) {
		for _, testCase := range vectors.Audience {
			if testCase.Reader != "" {
				if got := AudienceVisible(testCase.Reader, testCase.Audiences...); got != testCase.Visible {
					t.Errorf("AudienceVisible(%q, %v) = %v, want %v",
						testCase.Reader, testCase.Audiences, got, testCase.Visible)
				}
				continue
			}
			if got := AudienceNarrower(testCase.Left, testCase.Right); got != testCase.Narrower {
				t.Errorf("AudienceNarrower(%q, %q) = %q, want %q",
					testCase.Left, testCase.Right, got, testCase.Narrower)
			}
		}
	})

	t.Run("render_assertion", func(t *testing.T) {
		for _, testCase := range vectors.RenderAssertion {
			if got := RenderAssertion(testCase.Subject, testCase.Relation, testCase.Object); got != testCase.Expected {
				t.Errorf("RenderAssertion = %q, want %q", got, testCase.Expected)
			}
		}
	})

	t.Run("stable_json", func(t *testing.T) {
		for _, testCase := range vectors.StableJSON {
			if got := StableJSON(testCase.Input); got != testCase.Expected {
				t.Errorf("StableJSON = %q, want %q", got, testCase.Expected)
			}
		}
	})
}

// TestAudienceIntersection verifies AudienceNarrower and AudienceVisible rules.
func TestAudienceIntersection(t *testing.T) {
	if got := AudienceNarrower("shared", "private"); got != "private" {
		t.Errorf("AudienceNarrower(shared, private) = %q, want private", got)
	}
	if !AudienceVisible("owner", "private") {
		t.Error("owner should see private")
	}
	if AudienceVisible("deliverable", "private") {
		t.Error("deliverable must not see private")
	}
	if !AudienceVisible("deliverable", "shared", "group") {
		t.Error("deliverable should see shared/group")
	}
}

// TestTraceHashDeterministic verifies that TraceHash is deterministic and binds the turn number.
func TestTraceHashDeterministic(t *testing.T) {
	left := TraceHash("mem:test", "s", 1, "hello")
	if left != TraceHash("mem:test", "s", 1, "hello") {
		t.Fatalf("TraceHash is not deterministic for identical input")
	}
	if left == TraceHash("mem:test", "s", 2, "hello") {
		t.Fatalf("turn is not part of the trace hash")
	}
}

func TestGoldenNormalize(t *testing.T) {
	var vectors struct {
		Cases []struct {
			Input      string `json:"input"`
			Label      string `json:"label"`
			Map        []int  `json:"map"`
			Normalized string `json:"normalized"`
			Text       string `json:"text"`
		} `json:"cases"`
	}
	loadVector(t, "normalize.json", &vectors)
	for _, testCase := range vectors.Cases {
		normalized, mapping := NormalizeWithMap(testCase.Input)
		if normalized != testCase.Normalized {
			t.Errorf("NormalizeWithMap(%q) = %q, want %q", testCase.Input, normalized, testCase.Normalized)
		}
		if !reflect.DeepEqual(mapping, testCase.Map) {
			t.Errorf("NormalizeWithMap(%q) map = %v, want %v", testCase.Input, mapping, testCase.Map)
		}
		if got := NormalizeText(testCase.Input); got != testCase.Text {
			t.Errorf("NormalizeText(%q) = %q, want %q", testCase.Input, got, testCase.Text)
		}
		if got := NormalizeLabel(testCase.Input); got != testCase.Label {
			t.Errorf("NormalizeLabel(%q) = %q, want %q", testCase.Input, got, testCase.Label)
		}
	}
}

func TestGoldenTokenize(t *testing.T) {
	var vectors struct {
		Cap   int `json:"cap"`
		Cases []struct {
			Input string   `json:"input"`
			Terms []string `json:"terms"`
		} `json:"cases"`
	}
	loadVector(t, "tokenize.json", &vectors)
	if vectors.Cap != 48 {
		t.Fatalf("unexpected golden cap %d", vectors.Cap)
	}
	for _, testCase := range vectors.Cases {
		got := Tokenize(testCase.Input)
		if !reflect.DeepEqual(got, testCase.Terms) {
			t.Errorf("Tokenize(%q) = %v, want %v", testCase.Input, got, testCase.Terms)
		}
	}
}

func TestGoldenTime(t *testing.T) {
	var vectors struct {
		Cases []struct {
			Default  int64       `json:"default"`
			Expected int64       `json:"expected"`
			Input    any         `json:"input"`
			TimeRef  json.Number `json:"time_ref"`
		} `json:"cases"`
		FormatTime []struct {
			Epoch    int64  `json:"epoch"`
			Expected string `json:"expected"`
		} `json:"format_time"`
		TemporalClass []struct {
			Epoch    int64  `json:"epoch"`
			Expected string `json:"expected"`
			TimeRef  int64  `json:"time_ref"`
		} `json:"temporal_class"`
	}
	loadVector(t, "time.json", &vectors)
	for _, testCase := range vectors.Cases {
		timeRef, err := testCase.TimeRef.Int64()
		if err != nil {
			t.Fatalf("bad time_ref: %v", err)
		}
		got := ParseTime(testCase.Input, ParseTimeOptions{TimeRef: &timeRef, Default: testCase.Default})
		if got != testCase.Expected {
			t.Errorf("ParseTime(%v) = %d, want %d", testCase.Input, got, testCase.Expected)
		}
	}
	for _, testCase := range vectors.FormatTime {
		if got := FormatTime(testCase.Epoch, "unknown"); got != testCase.Expected {
			t.Errorf("FormatTime(%d) = %q, want %q", testCase.Epoch, got, testCase.Expected)
		}
	}
	for _, testCase := range vectors.TemporalClass {
		timeRef := testCase.TimeRef
		if got := TemporalClass(testCase.Epoch, &timeRef); got != testCase.Expected {
			t.Errorf("TemporalClass(%d) = %q, want %q", testCase.Epoch, got, testCase.Expected)
		}
	}
}

// TestFormatTimeSeconds pins the recall delivery label shape: local wall clock
// at second precision with an explicit numeric zone offset, so a reader never
// guesses the zone. The package pins time.Local to UTC; the offset suffix must
// follow the zone, not the value's UTC-ness.
func TestFormatTimeSeconds(t *testing.T) {
	if got := FormatTimeSeconds(1793102400, "unknown"); got != "2026-10-27 12:00:00 +00:00" {
		t.Fatalf("FormatTimeSeconds = %q, want %q", got, "2026-10-27 12:00:00 +00:00")
	}
	if got := FormatTimeSeconds(0, "未知"); got != "未知" {
		t.Fatalf("FormatTimeSeconds(0) = %q, want the empty placeholder", got)
	}
	previous := time.Local
	time.Local = time.FixedZone("UTC+8", 8*3600)
	defer func() { time.Local = previous }()
	if got := FormatTimeSeconds(1793102400, ""); got != "2026-10-27 20:00:00 +08:00" {
		t.Fatalf("FormatTimeSeconds in +08:00 = %q, want %q", got, "2026-10-27 20:00:00 +08:00")
	}
}

func TestGoldenQueryWindow(t *testing.T) {
	var vectors struct {
		Cases []struct {
			Input  string `json:"input"`
			Month  int    `json:"month"`
			Window []any  `json:"window"`
		} `json:"cases"`
	}
	loadVector(t, "query-window.json", &vectors)
	for _, testCase := range vectors.Cases {
		start, end, label := ParseQueryWindow(testCase.Input)
		wantStart := jsonInt64(t, testCase.Window[0])
		wantEnd := jsonInt64(t, testCase.Window[1])
		wantLabel, _ := testCase.Window[2].(string)
		if start != wantStart || end != wantEnd || label != wantLabel {
			t.Errorf("ParseQueryWindow(%q) = (%d,%d,%q), want (%d,%d,%q)",
				testCase.Input, start, end, label, wantStart, wantEnd, wantLabel)
		}
		if got := ParseQueryMonth(testCase.Input); got != testCase.Month {
			t.Errorf("ParseQueryMonth(%q) = %d, want %d", testCase.Input, got, testCase.Month)
		}
	}
}

func jsonInt64(t *testing.T, value any) int64 {
	t.Helper()
	number, ok := value.(json.Number)
	if !ok {
		t.Fatalf("expected json number, got %T", value)
	}
	parsed, err := number.Int64()
	if err != nil {
		t.Fatalf("bad json number %q: %v", number, err)
	}
	return parsed
}

func TestGoldenPyJSON(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/golden/vectors/pyjson.json")
	if err != nil {
		t.Fatalf("read pyjson.json: %v", err)
	}
	var vectors struct {
		Dumps []struct {
			Compact     bool `json:"compact"`
			EnsureASCII bool `json:"ensure_ascii"`
			Indent      int  `json:"indent"`
			Name        string
			Output      string `json:"output"`
			SortKeys    bool   `json:"sort_keys"`
		} `json:"dumps"`
		EventPayloads []struct {
			EnsureASCII bool           `json:"ensure_ascii"`
			Name        string         `json:"name"`
			Output      string         `json:"output"`
			Payload     map[string]any `json:"payload"`
		} `json:"event_payloads"`
	}
	if err := json.Unmarshal(raw, &vectors); err != nil {
		t.Fatalf("decode pyjson.json: %v", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&vectors); err != nil {
		t.Fatalf("decode pyjson.json with numbers: %v", err)
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		t.Fatalf("decode pyjson root: %v", err)
	}
	payload, err := orderedPyJSONPayload(root["payload"])
	if err != nil {
		t.Fatalf("decode ordered payload: %v", err)
	}
	for _, testCase := range vectors.Dumps {
		var got string
		var err error
		if testCase.Indent > 0 {
			got, err = PyJSONDumpIndent(payload, testCase.EnsureASCII, testCase.SortKeys, testCase.Indent)
		} else {
			got, err = PyJSONDump(payload, testCase.EnsureASCII, testCase.SortKeys, testCase.Compact)
		}
		if err != nil {
			t.Fatalf("%s: %v", testCase.Name, err)
		}
		if got != testCase.Output {
			t.Errorf("%s: PyJSONDump = %q, want %q", testCase.Name, got, testCase.Output)
		}
	}
	for _, testCase := range vectors.EventPayloads {
		got, err := PyJSONDump(testCase.Payload, testCase.EnsureASCII, true, false)
		if err != nil {
			t.Fatalf("%s: %v", testCase.Name, err)
		}
		if got != testCase.Output {
			t.Errorf("%s: PyJSONDump = %q, want %q", testCase.Name, got, testCase.Output)
		}
	}
}

func orderedPyJSONPayload(raw []byte) (OrderedMap, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	values := map[string]any{}
	if err := decoder.Decode(&values); err != nil {
		return nil, err
	}
	nestedValues, _ := values["nested"].(map[string]any)
	// The stored payload is serialized with sort_keys=True, so the original
	// key order of the fixture is restored explicitly below before re-dumping.
	return OrderedMap{
		{Key: "z", Value: values["z"]},
		{Key: "a", Value: values["a"]},
		{Key: "emoji", Value: values["emoji"]},
		{Key: "html", Value: values["html"]},
		{Key: "line", Value: values["line"]},
		{Key: "nested", Value: OrderedMap{
			{Key: "x", Value: nestedValues["x"]},
			{Key: "y", Value: nestedValues["y"]},
			{Key: "float", Value: nestedValues["float"]},
		}},
		{Key: "floats", Value: values["floats"]},
	}, nil
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func TestStemTokenAndFilterQueryTerms(t *testing.T) {
	cases := map[string]string{
		"painted":       "paint",
		"painting":      "paint",
		"rejected":      "rejec",
		"rejection":     "reject",
		"collaboration": "collabora",
		"collaborate":   "collabo",
		"promote":       "promo",
		"creative":      "creat",
		"scripts":       "scrip",
		"matches":       "match",
		"moved":         "move",
		"rings":         "ring",
		"running":       "runni",
		"stunning":      "stunn",
		"countries":     "countr",
		"musical":       "music",
		"classical":     "classi",
	}
	for input, want := range cases {
		if got := StemToken(input); got != want {
			t.Errorf("StemToken(%q) = %q, want %q", input, got, want)
		}
	}
	// Zero-wordlist gain tiers (2026-11-04 doctrine): "would"/"about" sit in the
	// closed-class band and are dropped, while "whether" (14b) reaches the primary
	// gain tier and survives alongside "caroline"/"religious" — code never kills a
	// high-information token, regardless of its traditional function-word status.
	filtered := FilterQueryTerms([]string{"would", "caroline", "whether", "religious", "about"})
	if len(filtered) != 3 || filtered[0] != "caroline" || filtered[1] != "whether" || filtered[2] != "religious" {
		t.Errorf("FilterQueryTerms = %v, want [caroline whether religious]", filtered)
	}
	allStop := FilterQueryTerms([]string{"would", "about"})
	if len(allStop) != 2 {
		t.Errorf("FilterQueryTerms fallback failed: %v", allStop)
	}
	zhFiltered := FilterQueryTerms([]string{"我", "喜欢", "咖啡", "的", "很"})
	if len(zhFiltered) != 2 || zhFiltered[0] != "喜欢" || zhFiltered[1] != "咖啡" {
		t.Errorf("FilterQueryTerms(zh) = %v, want [喜欢 咖啡]", zhFiltered)
	}
	zhAllStop := FilterQueryTerms([]string{"的"})
	if len(zhAllStop) != 1 || zhAllStop[0] != "的" {
		t.Errorf("FilterQueryTerms(zh) fallback failed: %v", zhAllStop)
	}
	ref := int64(1683504000) // 2023-05-08
	if got := FormatTime(ParseTime("4 years ago", ParseTimeOptions{TimeRef: &ref}), ""); got != "2019-05-01" {
		t.Errorf("ParseTime(4 years ago) = %q, want 2019-05-01", got)
	}
	if got := FormatTime(ParseTime("for about four months now", ParseTimeOptions{TimeRef: &ref}), ""); got != "2023-01-01" {
		t.Errorf("ParseTime(for about four months now) = %q, want 2023-01-01", got)
	}
	if got := NormalizeFirstPersonObject("my home country", "Caroline", "Caroline"); got != "Caroline's home country" {
		t.Errorf("NormalizeFirstPersonObject(my) = %q, want Caroline's home country", got)
	}
	if got := NormalizeFirstPersonObject("reminds me of my home", "Nate", ""); got != "reminds me of my home" {
		t.Errorf("NormalizeFirstPersonObject(non-leading) = %q, want unchanged", got)
	}
	if got := NormalizeFirstPersonEntity("me stay focused", "James", "James"); got != "James stay focused" {
		t.Errorf("NormalizeFirstPersonEntity(leading me) = %q, want James stay focused", got)
	}
	if got := NormalizeFirstPersonEntity("my home country", "Caroline", ""); got != "Caroline's home country" {
		t.Errorf("NormalizeFirstPersonEntity(boundary my) = %q, want Caroline's home country", got)
	}
	dslCases := map[string]string{
		"2023-07-mid":            "2023-07-11",
		"2026-late":              "2026-12-01",
		"2026-end":               "2026-12-01",
		"2026-mid":               "2026-06-01",
		"this:year-late":         "2023-12-01",
		"last:year-mid":          "2022-06-01",
		"2023-Q3":                "2023-07-01",
		"2023-summer":            "2023-06-01",
		"mid-July, 2023":         "2023-07-11",
		"late August 2022":       "2022-08-21",
		"2023年5月中旬":              "2023-05-11",
		"in the summer of 2023":  "2023-06-01",
		"2023年夏天":                "2023-06-01",
		"去年秋天":                   "2022-09-01",
		"我已经学了快四个月了":             "2023-01-01",
		"a few days ago":         "2023-05-05",
		"2023-08-03 -1w":         "2023-07-27",
		"2023-05-01..2023-05-10": "2023-05-01",
	}
	for expr, want := range dslCases {
		if got := FormatTime(ParseTime(expr, ParseTimeOptions{TimeRef: &ref}), ""); got != want {
			t.Errorf("ParseTime(%q) = %q, want %q", expr, got, want)
		}
	}
}

func TestSQLiteFileURI(t *testing.T) {
	cases := map[string]string{
		"/tmp/plain.db":         "file:/tmp/plain.db",
		"/home/a#b/mem.db":      "file:///home/a%23b/mem.db",
		"/home/a?b/mem.db":      "file:///home/a%3Fb/mem.db",
		"C:/Users/a/mem.db":     "file:///C:/Users/a/mem.db",
		`C:\Users\a\mem.db`:     "file:///C:/Users/a/mem.db",
		`c:\users\a\mem.db`:     "file:///c:/users/a/mem.db",
		`C:\a#b\mem.db`:         "file:///C:/a%23b/mem.db",
		`\\server\share\mem.db`: "file:////server/share/mem.db",
	}
	for path, want := range cases {
		if got := SQLiteFileURI(path); got != want {
			t.Errorf("SQLiteFileURI(%q) = %q, want %q", path, got, want)
		}
	}
	// Non-ASCII characters are percent-encoded on the way in and must decode
	// back to the original path bytes.
	parsed, err := url.Parse(SQLiteFileURI(`C:\用户\记忆库.db`))
	if err != nil {
		t.Fatalf("unicode path: %v", err)
	}
	if parsed.Path != "/C:/用户/记忆库.db" {
		t.Errorf("unicode path decoded to %q", parsed.Path)
	}
}
