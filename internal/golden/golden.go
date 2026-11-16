// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

// Package golden replays the frozen v0.7.4 fixtures against the Go
// implementation. It reads a fixture database copy (or rebuilds the scenario
// from its op list), applies the canonical normalization, and compares the
// result with the sealed expectation files.
package golden

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/rocareer/rolling-memory/internal/api"
	"github.com/rocareer/rolling-memory/internal/clock"
	"github.com/rocareer/rolling-memory/internal/config"
	"github.com/rocareer/rolling-memory/internal/ids"
	"github.com/rocareer/rolling-memory/internal/llm"
	"github.com/rocareer/rolling-memory/internal/store"
	"github.com/rocareer/rolling-memory/internal/transcript"
	"github.com/rocareer/rolling-memory/internal/version"
)

// FixedNow is the frozen wall clock used by the standard oracle.
const FixedNow = int64(1793102400)

// DumpTables is the table list used for rows.json and table digests.
var DumpTables = []string{
	"db_meta", "trace", "node", "node_alias", "assertion", "mention",
	"meta_edge", "event", "vector", "replay_state", "session_cursor",
	"activation", "sync_state", "injection_ledger", "transcript_cursor",
}

// dumpOrderColumn pins the dump order for tables whose physical row order in
// the standard oracle is interpreter-hash dependent. The activation ledger is
// upserted while iterating a standard set, so the golden seals its rows ordered
// by node_id (README §Normalization); rowid order must not be used for it.
var dumpOrderColumn = map[string]string{
	"activation": "node_id",
}

var timeColumns = map[string][]string{
	"trace":             {"occurred_at", "recorded_at", "processed_at"},
	"node":              {"created_at", "retired_at"},
	"node_alias":        {"created_at"},
	"assertion":         {"t_valid_from", "t_valid_to", "t_record", "t_source", "created_at"},
	"mention":           {"created_at"},
	"meta_edge":         {"created_at"},
	"event":             {"created_at"},
	"vector":            {"updated_at"},
	"replay_state":      {"produced_through", "updated_at"},
	"session_cursor":    {"turn_done", "updated_at"},
	"activation":        {"updated_at"},
	"sync_state":        {"synced_at"},
	"injection_ledger":  {"created_at"},
	"transcript_cursor": {"offset", "size", "mtime", "updated_at"},
}

var nullableTimeColumns = map[string]struct{}{
	"trace.processed_at": {},
	"node.retired_at":    {},
}

var absolutePathRE = regexp.MustCompile(`(?:/Users/|/home/[^/]+/|/private/|/var/folders/|/tmp/rm-golden)`)
var msRE = regexp.MustCompile(`ms="\d+"`)
var backupStampRE = regexp.MustCompile(`rolling-memory\.\d+\.bak`)

// Fixture is one fixture.json document.
type Fixture struct {
	Scenario          string            `json:"scenario"`
	Scope             string            `json:"scope"`
	Description       string            `json:"description"`
	FixedNow          int64             `json:"fixed_now"`
	Timezone          string            `json:"timezone"`
	DropFTS           bool              `json:"drop_fts"`
	DB                string            `json:"db"`
	Config            map[string]any    `json:"config"`
	Ops               []Op              `json:"ops"`
	Expected          map[string]string `json:"expected"`
	Inputs            []string          `json:"inputs"`
	FrozenTimeColumns map[string][]any  `json:"frozen_time_columns"`
	FrozenMeta        map[string]string `json:"frozen_meta"`
	TableDigests      map[string]string `json:"table_digests"`
}

// Op is one fixture operation.
type Op struct {
	Op     string         `json:"op"`
	Args   map[string]any `json:"args"`
	Save   string         `json:"save"`
	Probe  string         `json:"probe"`
	Render string         `json:"render"`
}

// LoadFixture decodes one fixture.json with numbers preserved as json.Number.
func LoadFixture(path string) (*Fixture, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	fixture := &Fixture{}
	if err := decoder.Decode(fixture); err != nil {
		return nil, err
	}
	return fixture, nil
}

// Normalizer applies the §6.4 path/duration/order normalization. Case,
// ConfigPath, DBPath and LogPath are the surface-scenario tokens (README
// §Surface tokens); fixtures leave them empty and keep their <TMP> paths.
type Normalizer struct {
	Root       string
	Case       string
	Home       string
	Tmp        string
	Repo       string
	Out        string
	ConfigPath string
	DBPath     string
	LogPath    string
}

// NewNormalizer builds the path map for one scenario work tree.
func NewNormalizer(workRoot, scenarioRoot, repoRoot string) *Normalizer {
	return &Normalizer{
		Root: workRoot,
		Home: filepath.Join(workRoot, "home"),
		Tmp:  filepath.Join(workRoot, "tmp"),
		Repo: repoRoot,
		Out:  filepath.Join(scenarioRoot, "out"),
	}
}

// Text replaces the synthetic path prefixes and rendered millisecond values.
func (n *Normalizer) Text(value string) string {
	text := value
	text = replacePathToken(text, n.Out, "<OUT>")
	text = replacePathToken(text, n.Repo, "<REPO>")
	text = replacePathToken(text, n.ConfigPath, "<CFG>")
	text = replacePathToken(text, n.DBPath, "<DB>")
	text = replacePathToken(text, n.LogPath, "<LOG>")
	text = replacePathToken(text, n.Home, "<HOME>")
	text = replacePathToken(text, n.Case, "<CASE>")
	text = replacePathToken(text, n.Tmp, "<TMP>")
	text = replacePathToken(text, n.Root, "<TMP>")
	text = replaceDesktopLauncher(text)
	text = backupStampRE.ReplaceAllString(text, "rolling-memory.<TS>.bak")
	text = msRE.ReplaceAllString(text, `ms="0"`)
	return text
}

// replacePathToken substitutes one synthetic path; an unset path (fixtures do
// not carry the surface tokens) leaves the text untouched.
func replacePathToken(text, path, token string) string {
	if path == "" {
		return text
	}
	return strings.ReplaceAll(text, path, token)
}

// desktopLauncherNames are the platform-specific desktop launcher file names
// (harness desktopShortcutSpec: .command on darwin, .desktop elsewhere on
// POSIX, .lnk on windows). The launcher location (Desktop under <HOME>) is
// stable across platforms, but the name is not, and the corpus is shared by
// every platform the runner executes on: the name is therefore abstracted to
// the <SHORTCUT> token so the frozen expectations replay identically on each.
var desktopLauncherNames = []string{
	"Rolling-Memory Console.command",
	"rolling-memory-console.desktop",
	"Rolling-Memory Console.lnk",
}

// replaceDesktopLauncher substitutes the platform-specific desktop launcher
// file name with the <SHORTCUT> token.
func replaceDesktopLauncher(text string) string {
	for _, name := range desktopLauncherNames {
		text = strings.ReplaceAll(text, name, "<SHORTCUT>")
	}
	return text
}

// Value normalizes one JSON-ready Go value. Reflection preserves float64
// values (encoding/json round-trips would collapse 1.0 into 1).
func (n *Normalizer) Value(value any, pointer string) (any, error) {
	if value == nil {
		return nil, nil
	}
	if number, ok := value.(json.Number); ok {
		return number, nil
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Ptr, reflect.Interface:
		if reflected.IsNil() {
			return nil, nil
		}
		return n.Value(reflected.Elem().Interface(), pointer)
	case reflect.Map:
		result := make(map[string]any, reflected.Len())
		iter := reflected.MapRange()
		for iter.Next() {
			key := fmt.Sprint(iter.Key().Interface())
			childPointer := pointer + "/" + key
			normalized, err := n.Field(key, iter.Value().Interface(), childPointer)
			if err != nil {
				return nil, err
			}
			result[key] = normalized
		}
		return result, nil
	case reflect.Slice, reflect.Array:
		result := make([]any, 0, reflected.Len())
		for index := 0; index < reflected.Len(); index++ {
			normalized, err := n.Value(reflected.Index(index).Interface(), pointer+"/"+strconv.Itoa(index))
			if err != nil {
				return nil, err
			}
			result = append(result, normalized)
		}
		return result, nil
	case reflect.String:
		text := n.Text(reflected.String())
		if absolutePathRE.MatchString(text) {
			return nil, fmt.Errorf("absolute host path survived normalization at %s: %q", pointer, text)
		}
		return text, nil
	default:
		return value, nil
	}
}

// Field applies key-specific normalization rules before recursing.
func (n *Normalizer) Field(key string, value any, pointer string) (any, error) {
	parent := pointer
	if index := strings.LastIndex(parent, "/"); index >= 0 {
		parent = parent[:index]
	}
	switch key {
	case "elapsed_ms":
		return json.Number("0"), nil
	case "db_size_bytes":
		return json.Number("0"), nil
	case "exported_at":
		return json.Number(strconv.FormatInt(FixedNow, 10)), nil
	}
	if key == "budget_exceeded" && strings.HasSuffix(pointer, "/gaps") {
		return json.Number("0"), nil
	}
	if key == "python" && pointer == "/python" {
		return "<RUNTIME>", nil
	}
	if key == "sqlite" && pointer == "/sqlite" {
		return "<SQLITE>", nil
	}
	if key == "watermark" {
		if _, ok := value.(string); ok {
			return value, nil
		}
	}
	textValue, _ := value.(string)
	if key == "version" && textValue == version.Version {
		return "0.0.1", nil
	}
	if key == "path" {
		if strings.HasSuffix(textValue, "config.toml") {
			return "<CFG>", nil
		}
		// Only an export artifact under <OUT> is the §6.4 export path; install
		// action paths are JSON config files under <HOME> or <CASE>.
		if strings.HasSuffix(textValue, ".json") && n.Out != "" &&
			(textValue == n.Out || strings.HasPrefix(textValue, n.Out+string(filepath.Separator))) {
			return "<OUT>", nil
		}
	}
	if key == "db" && strings.HasSuffix(textValue, "memory.db") {
		return "<DB>", nil
	}
	if key == "log" && strings.HasSuffix(textValue, "rolling-memory.log") {
		return "<LOG>", nil
	}
	if key == "warning" && (strings.HasSuffix(parent, "/vector") || strings.HasSuffix(parent, "/embedding")) {
		return nil, nil
	}
	if key == "error" && strings.HasSuffix(parent, "/deep") {
		if truthy(value) {
			return "<ENV>", nil
		}
		return value, nil
	}
	if key == "alias_collisions" && reflect.ValueOf(value).Kind() == reflect.Slice {
		normalized, err := n.Value(value, pointer)
		if err != nil {
			return nil, err
		}
		for _, item := range normalized.([]any) {
			row, ok := item.(map[string]any)
			if !ok {
				continue
			}
			nodes, ok := row["nodes"].([]any)
			if !ok {
				continue
			}
			sort.SliceStable(nodes, func(left, right int) bool {
				leftNode, _ := nodes[left].(map[string]any)
				rightNode, _ := nodes[right].(map[string]any)
				return asString(leftNode["id"]) < asString(rightNode["id"])
			})
		}
		return normalized, nil
	}
	if key == "similar_labels" && reflect.ValueOf(value).Kind() == reflect.Slice {
		normalized, err := n.Value(value, pointer)
		if err != nil {
			return nil, err
		}
		sort.SliceStable(normalized.([]any), func(left, right int) bool {
			leftRow, _ := normalized.([]any)[left].(map[string]any)
			rightRow, _ := normalized.([]any)[right].(map[string]any)
			leftRatio := asFloat(leftRow["ratio"])
			rightRatio := asFloat(rightRow["ratio"])
			if leftRatio != rightRatio {
				return leftRatio > rightRatio
			}
			leftID := asString(nested(leftRow, "left", "id"))
			rightID := asString(nested(rightRow, "left", "id"))
			if leftID != rightID {
				return leftID < rightID
			}
			return asString(nested(leftRow, "right", "id")) < asString(nested(rightRow, "right", "id"))
		})
		return normalized, nil
	}
	return n.Value(value, pointer)
}

// CanonicalJSON renders sorted, indented JSON with a trailing newline, which is
// the byte shape the standard generator used for every expectation file.
func CanonicalJSON(value any) (string, error) {
	text, err := ids.PyJSONDumpIndent(value, false, true, 2)
	if err != nil {
		return "", err
	}
	return text + "\n", nil
}

// Runner executes one fixture against one Memory instance.
type Runner struct {
	Fixture      *Fixture
	WorkRoot     string
	ScenarioRoot string
	Normalizer   *Normalizer
	Config       *config.Config
	Memory       *api.Memory
	Saved        map[string]any
	Produced     map[string]any
	ProducedText map[string]string
}

// NewRunner builds the per-scenario work tree and configuration.
func NewRunner(fixture *Fixture, workRoot, repoRoot string) (*Runner, error) {
	scenarioRoot := filepath.Join(workRoot, "cases", fixture.Scenario)
	if err := os.MkdirAll(scenarioRoot, 0o755); err != nil {
		return nil, err
	}
	configPath := filepath.Join(scenarioRoot, "config.toml")
	dbPath := filepath.Join(scenarioRoot, "memory.db")
	mode := "local"
	scope := fixture.Scope
	reader := "deliverable"
	cfg, err := config.Load(config.LoadOptions{
		Path: &configPath, DB: &dbPath, Mode: &mode, Scope: &scope, Reader: &reader,
		Env: map[string]string{},
	})
	if err != nil {
		return nil, err
	}
	cfg.Log = filepath.Join(scenarioRoot, "state", "rolling-memory.log")
	cfg.Recall.BudgetMS = 60000
	for dotted, value := range fixture.Config {
		if err := applyConfigOverride(cfg, dotted, value); err != nil {
			return nil, err
		}
	}
	return &Runner{
		Fixture:      fixture,
		WorkRoot:     workRoot,
		ScenarioRoot: scenarioRoot,
		Normalizer:   NewNormalizer(workRoot, scenarioRoot, repoRoot),
		Config:       cfg,
		Saved:        map[string]any{},
		Produced:     map[string]any{},
		ProducedText: map[string]string{},
	}, nil
}

// OpenFresh creates the scenario database from scratch.
func (r *Runner) OpenFresh() error {
	_ = os.Remove(r.Config.DB)
	database, err := api.New(r.Config)
	if err != nil {
		return err
	}
	r.Memory = database
	return nil
}

// OpenCopy copies the sealed fixture database into the scenario work tree.
func (r *Runner) OpenCopy(source string) error {
	raw, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(r.Config.DB), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(r.Config.DB, raw, 0o600); err != nil {
		return err
	}
	database, err := api.New(r.Config)
	if err != nil {
		return err
	}
	r.Memory = database
	return nil
}

// Close closes the current Memory.
func (r *Runner) Close() {
	if r.Memory != nil {
		_ = r.Memory.Close()
		r.Memory = nil
	}
}

// RunOps executes the selected ops in order.
func (r *Runner) RunOps(include func(index int, op Op) bool) error {
	for index, op := range r.Fixture.Ops {
		if include != nil && !include(index, op) {
			continue
		}
		args, err := resolveRefs(op.Args, r.Saved)
		if err != nil {
			return err
		}
		argsMap, ok := args.(map[string]any)
		if !ok {
			argsMap = map[string]any{}
		}
		output, err := r.Invoke(op.Op, argsMap)
		if err != nil {
			return fmt.Errorf("%s op %s: %w", r.Fixture.Scenario, op.Op, err)
		}
		if op.Save != "" {
			payload := output
			if recallOutput, ok := output.(map[string]any); ok {
				if jsonPayload, ok := recallOutput["json"]; ok {
					payload = jsonPayload
				}
			}
			r.Saved[op.Save] = payload
			r.Produced[op.Save] = payload
		}
		if op.Op == "recall" {
			if recallOutput, ok := output.(map[string]any); ok {
				if op.Probe != "" {
					r.Produced[op.Probe] = recallOutput["probe"]
				}
				if op.Render != "" {
					if text, ok := recallOutput["render"].(string); ok {
						r.ProducedText[op.Render] = text
					}
				}
			}
		}
	}
	return nil
}

// Invoke dispatches one fixture op.
func (r *Runner) Invoke(name string, args map[string]any) (any, error) {
	switch name {
	case "remember":
		return r.opRemember(args)
	case "add_trace":
		return r.opAddTrace(args)
	case "ensure_node":
		return r.opEnsureNode(args)
	case "node_by_label":
		return r.opNodeByLabel(args)
	case "add_alias":
		return r.opAddAlias(args)
	case "add_mention":
		return r.opAddMention(args)
	case "recall":
		return r.opRecall(args)
	case "recent":
		return r.Memory.Recent(nil, nil, argIntDefault(args, "limit", 10))
	case "status":
		return r.Memory.Status(r.Fixture.Scope)
	case "doctor":
		return r.Memory.Doctor()
	case "check":
		return r.Memory.Check()
	case "reindex":
		return r.Memory.Reindex()
	case "export":
		return r.Memory.Export(nil, nil)
	case "import":
		return r.opImport(args)
	case "closure":
		return r.opClosure(args)
	case "dump":
		return r.opDump(args)
	case "sql":
		return r.opSQL(args)
	case "config_set":
		if err := applyConfigOverride(r.Config, argString(args, "path"), args["value"]); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true, "dotted": args["path"], "value": args["value"]}, nil
	case "drop_fts":
		return r.opDropFTS()
	case "set_fake_llm":
		return r.opSetFakeLLM(args)
	case "set_llm_none":
		r.Memory.LLM = nil
		return map[string]any{"ok": true}, nil
	case "expect_error":
		return r.opExpectError(args)
	case "config_load":
		return r.opConfigLoad(args)
	case "config_egress":
		return r.opConfigEgress(args)
	case "config_secret":
		return r.opConfigSecret(args)
	case "write_transcript":
		return r.opWriteTranscript(args)
	case "import_transcript":
		return r.opImportTranscript(args)
	}
	return nil, fmt.Errorf("unknown fixture op %q", name)
}

func (r *Runner) opRemember(args map[string]any) (any, error) {
	assertions, err := argAssertions(args)
	if err != nil {
		return nil, err
	}
	request := api.RememberRequest{
		Content:    argStringPtr(args, "content"),
		SourceText: argStringPtr(args, "source_text"),
		AnchorText: argStringPtr(args, "anchor_text"),
		Assertion:  argMap(args, "assertion"),
		Assertions: assertions,
		Scope:      argStringPtr(args, "scope"),
		Session:    argStringDefault(args, "session", "default"),
		Turn:       argAny(args, "turn", json.Number("0")),
		Author:     argString(args, "author"),
		Audience:   argStringDefault(args, "audience", "shared"),
		OccurredAt: args["occurred_at"],
		Dry:        argBool(args, "dry"),
		TimeRef:    args["time_ref"],
		Aliases:    argMapList(args, "aliases"),
		AutoSync:   argBoolPtr(args, "auto_sync"),
	}
	return r.Memory.Remember(request)
}

func (r *Runner) opAddTrace(args map[string]any) (any, error) {
	options := store.TraceOptions{
		Session:  argString(args, "session"),
		Turn:     args["turn"],
		Kind:     argString(args, "kind"),
		Author:   argString(args, "author"),
		Audience: argString(args, "audience"),
		Meta:     argMap(args, "meta"),
	}
	if value, ok := args["occurred_at"]; ok && value != nil {
		parsed := asInt64(value)
		options.OccurredAt = &parsed
	}
	if value, ok := args["recorded_at"]; ok && value != nil {
		parsed := asInt64(value)
		options.RecordedAt = &parsed
	}
	hash, inserted, err := r.Memory.Store.AddTrace(r.Fixture.Scope, argString(args, "text"), options)
	if err != nil {
		return nil, err
	}
	return map[string]any{"hash": hash, "inserted": inserted}, nil
}

func (r *Runner) opEnsureNode(args map[string]any) (any, error) {
	nodeID, err := r.Memory.Store.EnsureNode(r.Fixture.Scope,
		argString(args, "kind"), argString(args, "label"), store.NodeOptions{
			Audience:  argString(args, "audience"),
			World:     argStringPtr(args, "world"),
			Carrier:   argStringPtr(args, "carrier"),
			Locus:     argStringPtr(args, "locus"),
			Status:    argString(args, "status"),
			Qualifier: argString(args, "qualifier"),
		})
	if err != nil {
		return nil, err
	}
	return map[string]any{"id": nodeID}, nil
}

func (r *Runner) opNodeByLabel(args map[string]any) (any, error) {
	node, found, err := r.Memory.Store.NodeByLabel(r.Fixture.Scope,
		argString(args, "label"), argString(args, "kind"))
	if err != nil {
		return nil, err
	}
	if !found {
		return map[string]any{"node": nil}, nil
	}
	return map[string]any{"node": node.Map()}, nil
}

func (r *Runner) opAddAlias(args map[string]any) (any, error) {
	source := argStringDefault(args, "source", "manual")
	inserted, err := r.Memory.Store.AddAlias(argString(args, "node_id"), argString(args, "alias"), source)
	if err != nil {
		return nil, err
	}
	return map[string]any{"inserted": inserted}, nil
}

func (r *Runner) opAddMention(args map[string]any) (any, error) {
	var nodeID *string
	if value, ok := args["node_id"]; ok && value != nil {
		text := asString(value)
		nodeID = &text
	}
	if err := r.Memory.Store.AddMention(r.Fixture.Scope, argString(args, "trace_hash"),
		argString(args, "surface"), argIntDefault(args, "start", 0),
		argIntDefault(args, "end", 0), nodeID); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true}, nil
}

func (r *Runner) opRecall(args map[string]any) (any, error) {
	request := api.RecallRequest{
		Cue:             argString(args, "cue"),
		Scope:           argStringPtr(args, "scope"),
		Reader:          argStringPtr(args, "reader"),
		Mode:            argStringDefault(args, "mode", "full"),
		AsOf:            args["asof"],
		WindowStart:     args["window_start"],
		WindowEnd:       args["window_end"],
		CapsBands:       argIntPtr(args, "caps_bands"),
		CapsNodes:       argIntPtr(args, "caps_nodes"),
		CapsChars:       argIntPtr(args, "caps_chars"),
		Offset:          argIntDefault(args, "offset", 0),
		Depth:           argIntDefault(args, "depth", 2),
		IncludeRaw:      argBoolPtr(args, "include_raw"),
		BudgetMS:        argIntPtr(args, "budget_ms"),
		RawLimit:        argIntDefault(args, "raw_limit", 5),
		Session:         argStringPtr(args, "session"),
		Deep:            argBool(args, "deep"),
		TimeRef:         args["time_ref"],
		TokenCorrect:    argBoolPtr(args, "token_correct"),
		AliasExpand:     argBoolPtr(args, "alias_expand"),
		RelativeTag:     argBoolPtr(args, "relative_tag"),
		MentionBackfill: argBoolPtr(args, "mention_backfill"),
	}
	result, err := r.Memory.Recall(request)
	if err != nil {
		return nil, err
	}
	maxChars := r.Config.Recall.Chars
	if caps, ok := args["caps_chars"]; ok && truthy(caps) {
		maxChars = int(asInt64(caps))
	}
	return map[string]any{
		"json":   result.ToDict(),
		"probe":  result.ToProbe(),
		"render": result.Render(maxChars),
	}, nil
}

func (r *Runner) opImport(args map[string]any) (any, error) {
	payload := args["payload_from"]
	if text, ok := payload.(string); ok && strings.HasPrefix(text, "$") {
		resolved, err := resolveRefs(text, r.Saved)
		if err != nil {
			return nil, err
		}
		payload = resolved
	}
	payloadMap, ok := payload.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("import payload must be an object")
	}
	if argBool(args, "fresh") {
		freshRoot := filepath.Join(r.ScenarioRoot, "fresh")
		if err := os.MkdirAll(freshRoot, 0o755); err != nil {
			return nil, err
		}
		configPath := filepath.Join(freshRoot, "config.toml")
		dbPath := filepath.Join(freshRoot, "memory.db")
		mode := "local"
		scope := r.Fixture.Scope
		reader := "deliverable"
		cfg, err := config.Load(config.LoadOptions{
			Path: &configPath, DB: &dbPath, Mode: &mode, Scope: &scope, Reader: &reader,
			Env: map[string]string{},
		})
		if err != nil {
			return nil, err
		}
		cfg.Log = filepath.Join(freshRoot, "state", "rolling-memory.log")
		cfg.Recall.BudgetMS = 60000
		fresh, err := api.New(cfg)
		if err != nil {
			return nil, err
		}
		defer fresh.Close()
		result, err := fresh.ImportData(payloadMap, &scope)
		if err != nil {
			return nil, err
		}
		status, err := fresh.Status(scope)
		if err != nil {
			return nil, err
		}
		return map[string]any{"result": result, "status": status}, nil
	}
	scope := r.Fixture.Scope
	return r.Memory.ImportData(payloadMap, &scope)
}

// opWriteTranscript writes the driver's synthetic transcript file: one JSON
// line per message (non-ASCII preserved, mtime pinned to FIXED_NOW) using
// PyJSONDump's canonical separators.
func (r *Runner) opWriteTranscript(args map[string]any) (any, error) {
	filename := argStringDefault(args, "filename", "transcript.jsonl")
	var builder strings.Builder
	for _, message := range argAnyList(args, "messages") {
		line, err := ids.PyJSONDump(message, false, true, false)
		if err != nil {
			return nil, err
		}
		builder.WriteString(line)
		builder.WriteString("\n")
	}
	path := filepath.Join(r.ScenarioRoot, filename)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, []byte(builder.String()), 0o644); err != nil {
		return nil, err
	}
	stamp := time.Unix(FixedNow, 0)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"path":  filename,
		"size":  info.Size(),
		"mtime": float64(info.ModTime().UnixNano()) / 1e9,
	}, nil
}

// opImportTranscript imports the next transcript slice at the case cursor. The
// oracle ran with cwd=<CASE> and stored the relative path in transcript_cursor,
// so the op switches into the scenario directory for the call.
func (r *Runner) opImportTranscript(args map[string]any) (any, error) {
	var receipt map[string]any
	err := withWorkingDir(r.ScenarioRoot, func() error {
		result, err := transcript.ImportTranscript(
			r.Memory.Store,
			argString(args, "path"),
			argStringDefault(args, "scope", r.Fixture.Scope),
			argStringPtr(args, "session"),
			argStringDefault(args, "audience", "shared"),
			argIntDefault(args, "offset", 0),
			argIntDefault(args, "max_lines", transcript.DefaultImportLines),
		)
		receipt = result
		return err
	})
	if err != nil {
		return nil, err
	}
	return receipt, nil
}

// withWorkingDir runs fn from dir and restores the previous working directory.
// Fixture replay needs it because the transcript cursor stores relative paths;
// the surface scenarios run with cwd=<CASE> like the oracle. Package tests run
// sequentially, so the process-wide switch is safe.
func withWorkingDir(dir string, fn func() error) error {
	previous, err := os.Getwd()
	if err != nil {
		return err
	}
	if err := os.Chdir(dir); err != nil {
		return err
	}
	defer func() {
		_ = os.Chdir(previous)
	}()
	return fn()
}

func (r *Runner) opClosure(args map[string]any) (any, error) {
	nodeIDs := argStringList(args, "node_ids")
	kinds := argStringList(args, "kinds")
	if len(kinds) == 0 {
		kinds = []string{"same-as"}
	}
	limit := argIntDefault(args, "limit", 128)
	return r.Memory.Store.Closure(r.Fixture.Scope, nodeIDs, kinds, limit)
}

func (r *Runner) opDump(args map[string]any) (any, error) {
	tables := argStringList(args, "tables")
	if len(tables) == 0 {
		tables = DumpTables
	}
	result := map[string]any{}
	for _, table := range tables {
		rows, err := TableRows(r.Memory.Store.DB(), table)
		if err != nil {
			return nil, err
		}
		result[table] = rows
	}
	return result, nil
}

func (r *Runner) opSQL(args map[string]any) (any, error) {
	params := argAnyList(args, "params")
	result, err := r.Memory.Store.DB().Exec(argString(args, "sql"), params...)
	if err != nil {
		return nil, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	return map[string]any{"rowcount": affected}, nil
}

func (r *Runner) opDropFTS() (any, error) {
	if _, err := r.Memory.Store.DB().Exec("DROP TABLE IF EXISTS node_fts"); err != nil {
		return nil, err
	}
	if _, err := r.Memory.Store.DB().Exec("DROP TABLE IF EXISTS trace_fts"); err != nil {
		return nil, err
	}
	// The oracle samples has_fts when the store opens, so a store opened before
	// the drop sees the tables gone only after it re-reads the schema; refresh
	// here instead of reconnecting so the ops keep one Memory instance.
	if err := r.Memory.Store.RefreshFTS(); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "has_fts": false}, nil
}

func (r *Runner) opSetFakeLLM(args map[string]any) (any, error) {
	hints := map[string]string{}
	if raw, ok := args["hints"].(map[string]any); ok {
		for key, value := range raw {
			hints[key] = asString(value)
		}
	}
	fake := fakeQueryLLM{hints: hints}
	if text := argString(args, "error"); text != "" {
		fake.err = text
	}
	r.Memory.LLM = fake
	return map[string]any{"hints": hints, "error": argString(args, "error")}, nil
}

// fakeQueryLLM satisfies llm.Extractor and llm.HintsProvider.
type fakeQueryLLM struct {
	hints map[string]string
	err   string
}

func (f fakeQueryLLM) Name() string { return "fake" }

func (f fakeQueryLLM) Extract(text, speaker string, timeRef int64) (llm.Extraction, error) {
	return llm.Extraction{}, fmt.Errorf("fake LLM has no extractor")
}

func (f fakeQueryLLM) QueryHints(cue string) (map[string]string, error) {
	if f.err != "" {
		return nil, fmt.Errorf("%s", f.err)
	}
	copied := map[string]string{}
	for key, value := range f.hints {
		copied[key] = value
	}
	return copied, nil
}

func (r *Runner) opExpectError(args map[string]any) (any, error) {
	call, ok := args["call"].(map[string]any)
	if !ok {
		return map[string]any{"error": "", "message": ""}, nil
	}
	callArgs, err := resolveRefs(call["args"], r.Saved)
	if err != nil {
		return nil, err
	}
	argsMap, _ := callArgs.(map[string]any)
	if argsMap == nil {
		argsMap = map[string]any{}
	}
	if _, err := r.Invoke(asString(call["op"]), argsMap); err != nil {
		return map[string]any{"error": "ValueError", "message": err.Error()}, nil
	}
	return map[string]any{"error": "", "message": ""}, nil
}

// ------------------------------------------------------------------ fixture output checks

// CompareProduced normalizes every produced output and compares it with the
// sealed expectation. Only expectations whose output was produced are checked.
func (r *Runner) CompareProduced() (int, error) {
	compared := 0
	names := make([]string, 0, len(r.Produced))
	for name := range r.Produced {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		relative, ok := r.Fixture.Expected[name]
		if !ok {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(GoldenRoot(), relative))
		if err != nil {
			return compared, err
		}
		normalized, err := r.Normalizer.Value(r.Produced[name], "")
		if err != nil {
			return compared, fmt.Errorf("%s: %w", name, err)
		}
		canonical, err := CanonicalJSON(normalized)
		if err != nil {
			return compared, err
		}
		if updateGolden {
			if err := writeExpected(filepath.Join(GoldenRoot(), relative), []byte(canonical)); err != nil {
				return compared, err
			}
			compared++
			continue
		}
		if canonical != string(raw) {
			return compared, fmt.Errorf("%s: JSON mismatch\n%s", name, diffReport(canonical, string(raw)))
		}
		compared++
	}
	textNames := make([]string, 0, len(r.ProducedText))
	for name := range r.ProducedText {
		textNames = append(textNames, name)
	}
	sort.Strings(textNames)
	for _, name := range textNames {
		relative, ok := r.Fixture.Expected[name]
		if !ok {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(GoldenRoot(), relative))
		if err != nil {
			return compared, err
		}
		normalized := r.Normalizer.Text(r.ProducedText[name]) + "\n"
		if updateGolden {
			if err := writeExpected(filepath.Join(GoldenRoot(), relative), []byte(normalized)); err != nil {
				return compared, err
			}
			compared++
			continue
		}
		if normalized != string(raw) {
			return compared, fmt.Errorf("%s: text mismatch\n%s", name, diffReport(normalized, string(raw)))
		}
		compared++
	}
	return compared, nil
}

// FixturePath returns the fixture.json path under testdata/golden.
func (r *Runner) FixturePath() string {
	return filepath.Join(GoldenRoot(), "fixtures", r.Fixture.Scenario, "fixture.json")
}

// Snapshot compares frozen time columns, db_meta and table digests.
func (r *Runner) Snapshot() error {
	producedTimes := map[string]any{}
	for table, columns := range timeColumns {
		for _, column := range columns {
			key := table + "." + column
			rows, err := r.Memory.Store.DB().Query("SELECT " + column + " FROM " + table)
			if err != nil {
				return err
			}
			values := []any{}
			hasNull := false
			for rows.Next() {
				var value any
				if err := rows.Scan(&value); err != nil {
					rows.Close()
					return err
				}
				if value == nil {
					hasNull = true
					continue
				}
				switch typed := value.(type) {
				case int64:
					values = append(values, typed)
				case float64:
					values = append(values, typed)
				default:
					values = append(values, asInt64(value))
				}
			}
			if err := rows.Err(); err != nil {
				rows.Close()
				return err
			}
			rows.Close()
			if hasNull {
				if _, ok := nullableTimeColumns[key]; !ok {
					return fmt.Errorf("%s contains NULL; time columns must be explicit", key)
				}
			}
			sort.SliceStable(values, func(left, right int) bool {
				leftFloat, rightFloat := asFloat(values[left]), asFloat(values[right])
				if leftFloat != rightFloat {
					return leftFloat < rightFloat
				}
				return fmt.Sprint(values[left]) < fmt.Sprint(values[right])
			})
			unique := []any{}
			seen := map[string]struct{}{}
			for _, value := range values {
				tag := fmt.Sprintf("%T:%v", value, value)
				if _, ok := seen[tag]; ok {
					continue
				}
				seen[tag] = struct{}{}
				unique = append(unique, value)
			}
			producedTimes[key] = unique
		}
	}
	var created string
	if err := r.Memory.Store.DB().QueryRow("SELECT value FROM db_meta WHERE key = 'created_at'").Scan(&created); err != nil {
		return err
	}
	if _, err := strconv.Atoi(created); err != nil {
		return fmt.Errorf("db_meta.created_at must be an explicit integer")
	}
	createdValue := asInt64(created)
	producedTimes["db_meta.created_at"] = []any{createdValue}
	if !updateGolden && !equalJSON(producedTimes, r.Fixture.FrozenTimeColumns) {
		return fmt.Errorf("frozen_time_columns mismatch\n got: %s\nwant: %s", mustJSON(producedTimes), mustJSON(r.Fixture.FrozenTimeColumns))
	}

	producedMeta := map[string]string{}
	for _, key := range []string{"schema_version", "engine"} {
		var value string
		if err := r.Memory.Store.DB().QueryRow("SELECT value FROM db_meta WHERE key = ?", key).Scan(&value); err != nil {
			return err
		}
		producedMeta["db_meta."+key] = value
	}
	if !updateGolden && !equalJSON(producedMeta, r.Fixture.FrozenMeta) {
		return fmt.Errorf("frozen_meta mismatch\n got: %s\nwant: %s", mustJSON(producedMeta), mustJSON(r.Fixture.FrozenMeta))
	}

	producedDigests := map[string]string{}
	for _, table := range DumpTables {
		rows, err := TableRows(r.Memory.Store.DB(), table)
		if err != nil {
			return err
		}
		payload, err := ids.PyJSONDump(rows, false, true, true)
		if err != nil {
			return err
		}
		digest := sha256.Sum256([]byte(payload))
		got := hex.EncodeToString(digest[:])
		producedDigests[table] = got
		if !updateGolden {
			if want := r.Fixture.TableDigests[table]; want != got {
				return fmt.Errorf("%s digest mismatch: got %s want %s", table, got, want)
			}
		}
	}
	if updateGolden {
		rawFixture, err := os.ReadFile(r.FixturePath())
		if err != nil {
			return err
		}
		decodedFixture, err := decodeJSON(rawFixture)
		if err != nil {
			return err
		}
		fixtureMap, ok := decodedFixture.(map[string]any)
		if !ok {
			return fmt.Errorf("fixture.json is not an object")
		}
		fixtureMap["frozen_time_columns"] = producedTimes
		fixtureMap["frozen_meta"] = producedMeta
		fixtureMap["table_digests"] = producedDigests
		canonicalFixture, err := CanonicalJSON(fixtureMap)
		if err != nil {
			return err
		}
		if err := writeExpected(r.FixturePath(), []byte(canonicalFixture)); err != nil {
			return err
		}
		if r.Fixture.DB != "" {
			_, _ = r.Memory.Store.DB().Exec("PRAGMA wal_checkpoint(TRUNCATE)")
			dbBytes, err := os.ReadFile(r.Config.DB)
			if err != nil {
				return err
			}
			if err := writeExpected(filepath.Join(GoldenRoot(), r.Fixture.DB), dbBytes); err != nil {
				return err
			}
		}
	}
	return nil
}

// CompareRows compares the post-run rows.json expectation where present.
func (r *Runner) CompareRows() (bool, error) {
	relative, ok := r.Fixture.Expected["rows.json"]
	if !ok {
		return false, nil
	}
	raw, err := os.ReadFile(filepath.Join(GoldenRoot(), relative))
	if err != nil {
		return false, err
	}
	decoded, err := decodeJSON(raw)
	if err != nil {
		return false, err
	}
	expected, ok := decoded.(map[string]any)
	if !ok {
		return false, fmt.Errorf("rows.json is not an object")
	}
	tables := make([]string, 0, len(expected))
	for table := range expected {
		tables = append(tables, table)
	}
	sort.Strings(tables)
	produced := map[string]any{}
	for _, table := range tables {
		rows, err := TableRows(r.Memory.Store.DB(), table)
		if err != nil {
			return false, err
		}
		produced[table] = rows
	}
	normalized, err := r.Normalizer.Value(produced, "")
	if err != nil {
		return false, err
	}
	canonical, err := CanonicalJSON(normalized)
	if err != nil {
		return false, err
	}
	if updateGolden {
		if err := writeExpected(filepath.Join(GoldenRoot(), relative), []byte(canonical)); err != nil {
			return false, err
		}
		return true, nil
	}
	if canonical != string(raw) {
		return false, fmt.Errorf("rows.json mismatch\n%s", diffReport(canonical, string(raw)))
	}
	return true, nil
}

// tableSnapshot holds one table's rows so a replay can write them back.
type tableSnapshot struct {
	columns []string
	values  [][]any
}

// captureTable records one table's rows. The read chain records delivered hits
// in the activation ledger, so a recall replayed on the sealed copy mutates that
// table by design; TestGoldenFrozenReadPaths holds the sealed rows and restores
// them before the snapshot instead of weakening the digest comparison (the
// ledger's own content is checked by TestGoldenReplay).
func (r *Runner) captureTable(table string) (*tableSnapshot, error) {
	rows, err := r.Memory.Store.DB().Query("SELECT * FROM " + table + " ORDER BY rowid")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	snapshot := &tableSnapshot{columns: columns}
	for rows.Next() {
		values := make([]any, len(columns))
		targets := make([]any, len(columns))
		for index := range values {
			targets[index] = &values[index]
		}
		if err := rows.Scan(targets...); err != nil {
			return nil, err
		}
		copied := make([]any, len(values))
		copy(copied, values)
		snapshot.values = append(snapshot.values, copied)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return snapshot, nil
}

// restoreTable replaces one table's rows with a captured snapshot.
func (r *Runner) restoreTable(table string, snapshot *tableSnapshot) error {
	if snapshot == nil {
		return nil
	}
	if _, err := r.Memory.Store.DB().Exec("DELETE FROM " + table); err != nil {
		return err
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(snapshot.columns)), ",")
	statement := "INSERT INTO " + table + "(" + strings.Join(snapshot.columns, ",") +
		") VALUES (" + placeholders + ")"
	for _, values := range snapshot.values {
		if _, err := r.Memory.Store.DB().Exec(statement, values...); err != nil {
			return err
		}
	}
	return nil
}

// TableRows returns SELECT * rows as JSON-ready maps.
func TableRows(connection *sql.DB, table string) ([]any, error) {
	order := "rowid"
	if column, ok := dumpOrderColumn[table]; ok {
		order = column
	}
	rows, err := connection.Query("SELECT * FROM " + table + " ORDER BY " + order)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	result := []any{}
	for rows.Next() {
		values := make([]any, len(columns))
		targets := make([]any, len(columns))
		for index := range values {
			targets[index] = &values[index]
		}
		if err := rows.Scan(targets...); err != nil {
			return nil, err
		}
		row := make(map[string]any, len(columns))
		for index, column := range columns {
			value := values[index]
			if blob, ok := value.([]byte); ok {
				row[column] = map[string]any{"__blob_hex__": hex.EncodeToString(blob)}
				continue
			}
			row[column] = value
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

// ReplayModeFreeze installs the frozen wall/monotonic clock.
func ReplayModeFreeze() func() {
	return clock.SetForTest(time.Unix(FixedNow, 0), 1_000_000_000)
}

// GoldenRoot returns testdata/golden relative to the package test directory.
func GoldenRoot() string {
	return filepath.Join("..", "..", "testdata", "golden")
}

func applyConfigOverride(cfg *config.Config, dotted string, value any) error {
	switch dotted {
	case "recall.budget_ms":
		cfg.Recall.BudgetMS = int(asInt64(value))
	case "recall.vector":
		cfg.Recall.Vector = truthy(value)
	case "recall.token_correct":
		cfg.Recall.TokenCorrect = truthy(value)
	case "recall.alias_expand":
		cfg.Recall.AliasExpand = truthy(value)
	case "recall.relative_tag":
		cfg.Recall.RelativeTag = truthy(value)
	case "recall.mention_backfill":
		cfg.Recall.MentionBackfill = truthy(value)
	case "recall.raw_when_sparse":
		cfg.Recall.RawWhenSparse = truthy(value)
	case "recall.bands":
		cfg.Recall.Bands = int(asInt64(value))
	case "recall.nodes":
		cfg.Recall.Nodes = int(asInt64(value))
	case "recall.chars":
		cfg.Recall.Chars = int(asInt64(value))
	case "llm.provider":
		cfg.LLM.Provider = asString(value)
	case "hooks.max_traces":
		cfg.Hooks.MaxTraces = int(asInt64(value))
	case "hooks.max_llm_calls":
		cfg.Hooks.MaxLLMCalls = int(asInt64(value))
	default:
		return fmt.Errorf("unsupported config override %q", dotted)
	}
	return nil
}

// ------------------------------------------------------------------ helpers

func decodeJSON(raw []byte) (any, error) {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return value, nil
}

func resolveRefs(value any, saved map[string]any) (any, error) {
	switch typed := value.(type) {
	case string:
		if !strings.HasPrefix(typed, "$") {
			return typed, nil
		}
		parts := strings.Split(typed[1:], "/")
		current, ok := saved[parts[0]]
		if !ok {
			return nil, fmt.Errorf("cannot resolve %q: missing %q", typed, parts[0])
		}
		for _, part := range parts[1:] {
			switch item := current.(type) {
			case []any:
				index, err := strconv.Atoi(part)
				if err != nil || index < 0 || index >= len(item) {
					return nil, fmt.Errorf("cannot resolve %q at %q", typed, part)
				}
				current = item[index]
			case map[string]any:
				next, ok := item[part]
				if !ok {
					return nil, fmt.Errorf("cannot resolve %q at %q", typed, part)
				}
				current = next
			default:
				return nil, fmt.Errorf("cannot resolve %q at %q", typed, part)
			}
		}
		return current, nil
	case map[string]any:
		result := map[string]any{}
		for key, item := range typed {
			next, err := resolveRefs(item, saved)
			if err != nil {
				return nil, err
			}
			result[key] = next
		}
		return result, nil
	case []any:
		result := make([]any, 0, len(typed))
		for _, item := range typed {
			next, err := resolveRefs(item, saved)
			if err != nil {
				return nil, err
			}
			result = append(result, next)
		}
		return result, nil
	}
	return value, nil
}

func argAssertions(args map[string]any) ([]map[string]any, error) {
	raw, ok := args["assertions"]
	if !ok || raw == nil {
		return nil, nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("assertions must be a non-empty list")
	}
	result := make([]map[string]any, 0, len(list))
	for _, item := range list {
		row, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("every batch assertion must be an object")
		}
		result = append(result, row)
	}
	return result, nil
}

func argAny(args map[string]any, key string, fallback any) any {
	if value, ok := args[key]; ok {
		return value
	}
	return fallback
}

func argString(args map[string]any, key string) string {
	value, ok := args[key]
	if !ok || value == nil {
		return ""
	}
	return asString(value)
}

func argStringDefault(args map[string]any, key, fallback string) string {
	value, ok := args[key]
	if !ok || value == nil {
		return fallback
	}
	return asString(value)
}

func argStringPtr(args map[string]any, key string) *string {
	value, ok := args[key]
	if !ok || value == nil {
		return nil
	}
	text := asString(value)
	return &text
}

func argMap(args map[string]any, key string) map[string]any {
	value, ok := args[key]
	if !ok {
		return nil
	}
	row, _ := value.(map[string]any)
	return row
}

func argMapList(args map[string]any, key string) []map[string]any {
	value, ok := args[key]
	if !ok || value == nil {
		return nil
	}
	list, ok := value.([]any)
	if !ok {
		return nil
	}
	result := []map[string]any{}
	for _, item := range list {
		if row, ok := item.(map[string]any); ok {
			result = append(result, row)
		}
	}
	return result
}

func argStringList(args map[string]any, key string) []string {
	value, ok := args[key]
	if !ok || value == nil {
		return nil
	}
	list, ok := value.([]any)
	if !ok {
		return nil
	}
	result := []string{}
	for _, item := range list {
		result = append(result, asString(item))
	}
	return result
}

func argAnyList(args map[string]any, key string) []any {
	value, ok := args[key]
	if !ok || value == nil {
		return nil
	}
	list, _ := value.([]any)
	return list
}

func argBool(args map[string]any, key string) bool {
	value, ok := args[key]
	if !ok {
		return false
	}
	return truthy(value)
}

func argBoolPtr(args map[string]any, key string) *bool {
	value, ok := args[key]
	if !ok || value == nil {
		return nil
	}
	result := truthy(value)
	return &result
}

func argIntDefault(args map[string]any, key string, fallback int) int {
	value, ok := args[key]
	if !ok || value == nil {
		return fallback
	}
	return int(asInt64(value))
}

func argIntPtr(args map[string]any, key string) *int {
	value, ok := args[key]
	if !ok || value == nil {
		return nil
	}
	result := int(asInt64(value))
	return &result
}

func asString(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case json.Number:
		return typed.String()
	case bool:
		if typed {
			return "true"
		}
		return "false"
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case float64:
		return strconv.FormatFloat(typed, 'g', -1, 64)
	}
	return fmt.Sprint(value)
}

func asInt64(value any) int64 {
	switch typed := value.(type) {
	case json.Number:
		parsed, _ := typed.Int64()
		return parsed
	case int:
		return int64(typed)
	case int64:
		return typed
	case float64:
		return int64(typed)
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		if err == nil {
			return parsed
		}
	}
	return 0
}

func asFloat(value any) float64 {
	switch typed := value.(type) {
	case json.Number:
		parsed, _ := typed.Float64()
		return parsed
	case float64:
		return typed
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	}
	return 0
}

func truthy(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case string:
		return typed != ""
	case json.Number:
		number, err := typed.Float64()
		return err == nil && number != 0
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
	}
	return value != nil
}

func nested(row map[string]any, key, child string) any {
	inner, _ := row[key].(map[string]any)
	if inner == nil {
		return nil
	}
	return inner[child]
}

func equalJSON(left, right any) bool {
	leftJSON, err := ids.PyJSONDump(left, false, true, true)
	if err != nil {
		return false
	}
	rightJSON, err := ids.PyJSONDump(right, false, true, true)
	if err != nil {
		return false
	}
	return leftJSON == rightJSON
}

func diffReport(got, want string) string {
	gotLines := strings.Split(got, "\n")
	wantLines := strings.Split(want, "\n")
	limit := len(gotLines)
	if len(wantLines) < limit {
		limit = len(wantLines)
	}
	for index := 0; index < limit; index++ {
		if gotLines[index] == wantLines[index] {
			continue
		}
		start := index - 3
		if start < 0 {
			start = 0
		}
		end := index + 6
		if end > limit {
			end = limit
		}
		var builder strings.Builder
		fmt.Fprintf(&builder, "first difference at line %d\n", index+1)
		for line := start; line < end; line++ {
			fmt.Fprintf(&builder, "got  %4d | %s\n", line+1, gotLines[line])
		}
		for line := start; line < end; line++ {
			fmt.Fprintf(&builder, "want %4d | %s\n", line+1, wantLines[line])
		}
		if len(gotLines) != len(wantLines) {
			fmt.Fprintf(&builder, "line counts: got %d want %d\n", len(gotLines), len(wantLines))
		}
		return builder.String()
	}
	return fmt.Sprintf("line counts differ: got %d want %d", len(gotLines), len(wantLines))
}

func mustJSON(value any) string {
	text, err := ids.PyJSONDump(value, false, true, false)
	if err != nil {
		return err.Error()
	}
	return text
}
