// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/rocareer/rolling-memory/internal/api"
	"github.com/rocareer/rolling-memory/internal/config"
	"github.com/rocareer/rolling-memory/internal/eval"
	"github.com/rocareer/rolling-memory/internal/hooks"
	"github.com/rocareer/rolling-memory/internal/ids"
	"github.com/rocareer/rolling-memory/internal/mcp"
	"github.com/rocareer/rolling-memory/internal/text"
	"github.com/rocareer/rolling-memory/internal/version"
)

// Main is the process entry point shared by cmd/rolling-memory and cmd/rmem.
// CLI parser usage errors exit 2, processed application errors exit 1 and a
// broken stdout pipe exits 0.
func Main(argv []string, stdin io.Reader, stdout, stderr io.Writer) int {
	parser := BuildParser()
	args, err := parser.Parse(argv)
	if err != nil {
		var help *HelpRequest
		if errors.As(err, &help) {
			fmt.Fprint(stdout, help.Text)
			return 0
		}
		var usage *UsageError
		if errors.As(err, &usage) {
			fmt.Fprintf(stderr, "%s\n%s: error: %s\n", usage.Usage, parser.Prog, usage.Message)
			return 2
		}
		fmt.Fprintf(stderr, "error: %s\n", ids.Redact(err.Error()))
		return 1
	}
	if args.version {
		fmt.Fprintf(stdout, "rolling-memory %s\n", version.Version)
		return 0
	}
	app := &application{stdin: stdin, stdout: stdout, stderr: stderr, args: args}
	code := app.dispatch()
	if app.writeErr != nil {
		if isBrokenPipe(app.writeErr) {
			return 0
		}
		return 1
	}
	return code
}

// application carries the parsed command line and the three stdio streams.
type application struct {
	stdin    io.Reader
	stdout   io.Writer
	stderr   io.Writer
	args     *Args
	writeErr error
}

func (a *application) printf(format string, values ...any) {
	if a.writeErr != nil {
		return
	}
	if _, err := fmt.Fprintf(a.stdout, format, values...); err != nil {
		a.writeErr = err
	}
}

func (a *application) printLine(text string) { a.printf("%s\n", text) }

// printJSON mirrors json.dumps(value, ensure_ascii=False, indent=2).
func (a *application) printJSON(value any) error {
	text, err := ids.PyJSONDumpIndent(value, false, true, 2)
	if err != nil {
		// 结构体等 PyJSON 不直接支持的形态经 encoding/json 归一后再编码；
		// 解码必须保留数字字面（UseNumber），否则 int 字段会退化成 float64 并渲染成 "0.0"，
		// 破坏下游（管理后台、脚本）按整数解析 CLI 输出的契约。
		if raw, mErr := json.Marshal(value); mErr == nil {
			decoder := json.NewDecoder(bytes.NewReader(raw))
			decoder.UseNumber()
			var generic any
			if decoder.Decode(&generic) == nil {
				text, err = ids.PyJSONDumpIndent(generic, false, true, 2)
			}
		}
	}
	if err != nil {
		return err
	}
	a.printf("%s\n", text)
	return nil
}

// emit writes JSON when --json was given, or invokes human() otherwise.
func (a *application) emit(result any, human func()) error {
	if a.args.Flag("json") {
		return a.printJSON(result)
	}
	human()
	return nil
}

// fail renders one processed application error.
func (a *application) fail(err error) int {
	if isBrokenPipe(err) {
		return 0
	}
	message := ids.Redact(err.Error())
	if a.args.Flag("json") {
		text, dumpErr := ids.PyJSONDump(ids.OrderedMap{
			{Key: "ok", Value: false},
			{Key: "error", Value: message},
		}, false, false, false)
		if dumpErr == nil {
			a.printf("%s\n", text)
		}
	} else {
		fmt.Fprintf(a.stderr, "error: %s\n", message)
	}
	return 1
}

func (a *application) errorf(format string, values ...any) {
	fmt.Fprintf(a.stderr, format, values...)
}

func isBrokenPipe(err error) bool {
	return errors.Is(err, syscall.EPIPE)
}

// ------------------------------------------------------------------ dispatch

func (a *application) dispatch() int {
	switch a.args.Command() {
	case "mcp":
		return a.runMCP()
	case "hook":
		return a.runHook()
	case "init":
		return a.runInit()
	case "mode":
		return a.runMode()
	case "config":
		return a.runConfig()
	case "install":
		return a.runInstall()
	case "panel":
		return a.runPanel()
	case "update", "upgrade":
		return a.runUpdate()
	case "export":
		return a.fail(api.ExportUnavailable())
	case "eval":
		return a.runEval()
	case "wave":
		return a.runWave()
	case "recall":
		scope := a.args.String("scope")
		if eval.IsCloudEvalScope(scope) {
			return a.runCloudEvalRecall()
		}
		if err := rejectRetiredEvalScope(scope); err != nil {
			return a.fail(err)
		}
		if code, handled := a.tryDaemonRecall(); handled {
			return code
		}
	}
	return a.withMemory(a.runWithMemory)
}

func (a *application) withMemory(fn func(*api.Memory) (int, error)) int {
	memory, err := a.openMemory()
	if err != nil {
		return a.fail(err)
	}
	defer memory.Close()
	code, err := fn(memory)
	if err != nil {
		return a.fail(err)
	}
	return code
}

func (a *application) openMemory() (*api.Memory, error) {
	cfg, err := config.Load(config.LoadOptions{
		Path:  a.args.StringPtr("config"),
		DB:    a.args.StringPtr("db"),
		Mode:  a.args.StringPtr("mode"),
		Scope: a.args.StringPtr("scope"),
	})
	if err != nil {
		return nil, err
	}
	return api.New(cfg)
}

func (a *application) runWithMemory(memory *api.Memory) (int, error) {
	switch a.args.Command() {
	case "recall":
		return a.runRecall(memory)
	case "recent":
		result, err := memory.Recent(nil, a.args.StringPtr("reader"), a.args.Int("limit"))
		if err != nil {
			return 1, err
		}
		return 0, a.emit(result, func() { a.printRecent(result) })
	case "import":
		result, err := memory.ImportFile(a.args.String("path"), nil, a.args.StringPtr("session"),
			a.args.String("audience"), a.args.Flag("replay"))
		if err != nil {
			return 1, err
		}
		return 0, a.emit(result, func() { a.printImport(result) })
	case "export":
		result, err := memory.Export(a.args.StringPtr("out"), nil)
		if err != nil {
			return 1, err
		}
		return 0, a.emit(result, func() { a.printExport(result) })
	case "background":
		return a.runBackground(memory)
	case "sync":
		return a.runSyncCommand(memory)
	case "status":
		result, err := memory.Status(memory.Config.Scope)
		if err != nil {
			return 1, err
		}
		return 0, a.emit(result, func() { a.printStatus(result) })
	case "doctor":
		result, err := memory.Doctor()
		if err != nil {
			return 1, err
		}
		return 0, a.emit(result, func() { a.printDoctor(result) })
	case "check":
		result, err := memory.Check()
		if err != nil {
			return 1, err
		}
		if err := a.emit(result, func() { a.printCheck(result) }); err != nil {
			return 1, err
		}
		if ok, _ := result["ok"].(bool); ok {
			return 0, nil
		}
		return 1, nil
	case "reindex":
		result, err := memory.Reindex()
		if err != nil {
			return 1, err
		}
		return 0, a.emit(result, func() { a.printReindex(result) })
	case "eject":
		result, err := memory.Eject()
		if err != nil {
			return 1, err
		}
		if err := a.emit(result, func() { a.printSummary("eject", result) }); err != nil {
			return 1, err
		}
		if ok, _ := result["ok"].(bool); ok {
			return 0, nil
		}
		return 1, nil
	case "efficacy":
		rep, err := memory.Efficacy(a.args.StringPtr("scope"), a.args.String("harness"), a.args.Int("limit"), a.args.Int("budget_seconds"))
		if err != nil {
			return 1, err
		}
		m := rep.ToMap()
		if a.args.Flag("card") && !a.args.Flag("json") {
			a.printEfficacyCard(m)
			return 0, nil
		}
		return 0, a.emit(m, func() { a.printEfficacy(m) })
	}
	return 1, fmt.Errorf("unknown command %q", a.args.Command())
}

// --------------------------------------------------------------------- read

func (a *application) tryDaemonRecall() (int, bool) {
	if !mcp.ShouldUseUnixDaemon(a.args.String("db")) {
		return 0, false
	}
	if limit := a.args.Int("raw_limit"); limit < 1 || limit > 64 {
		return a.fail(errors.New("--raw-limit must be between 1 and 64")), true
	}
	if bands := a.args.IntPtr("bands"); bands != nil && (*bands < 0 || *bands > 64) {
		return a.fail(errors.New("--bands must be between 0 and 64")), true
	}
	cue := a.args.String("cue")
	if cue == "" && a.args.String("result_mode") != "recent" {
		return a.fail(errors.New("recall requires --q")), true
	}
	var asOf, windowStart, windowEnd, timeRef any
	if value, ok := a.args.LookupString("asof"); ok {
		asOf = value
	}
	if value, ok := a.args.LookupString("window_start"); ok {
		windowStart = value
	}
	if value, ok := a.args.LookupString("window_end"); ok {
		windowEnd = value
	}
	if value, ok := a.args.LookupString("time_ref"); ok {
		timeRef = value
	}
	var presence *text.PresenceCoordinates
	pLocus := strings.TrimSpace(os.Getenv("ROLLING_MEMORY_PRESENCE_LOCUS"))
	pCarrier := strings.TrimSpace(os.Getenv("ROLLING_MEMORY_PRESENCE_CARRIER"))
	pInc := strings.TrimSpace(os.Getenv("ROLLING_MEMORY_PRESENCE_INCARNATION"))
	if pLocus != "" || pCarrier != "" || pInc != "" {
		coords := text.PresenceCoordinates{
			World:       "actual",
			Carrier:     pCarrier,
			Locus:       pLocus,
			Incarnation: pInc,
		}.Normalized()
		if !coords.IsEmpty() {
			presence = &coords
		}
	}
	isJSONOrProbe := a.args.Flag("json") || a.args.String("result_mode") == "probe"
	renderMaxChars := 0
	if chars := a.args.IntPtr("chars"); chars != nil && *chars != 0 {
		renderMaxChars = *chars
	}
	payload := mcp.UnixRecallPayload{
		DB:              a.args.String("db"),
		Cue:             cue,
		Scope:           a.args.StringPtr("scope"),
		Reader:          a.args.StringPtr("reader"),
		Mode:            a.args.String("result_mode"),
		AsOf:            asOf,
		WindowStart:     windowStart,
		WindowEnd:       windowEnd,
		CapsBands:       a.args.IntPtr("bands"),
		CapsNodes:       a.args.IntPtr("nodes"),
		CapsChars:       a.args.IntPtr("chars"),
		CapsChains:      a.args.IntPtr("caps_chains"),
		CapsRows:        a.args.IntPtr("caps_rows"),
		ResultUnit:      a.args.String("result_unit"),
		Rank:            a.args.FlagPtr("rank"),
		Offset:          a.args.Int("offset"),
		Depth:           a.args.Int("depth"),
		IncludeRaw:      a.args.FlagPtr("include_raw"),
		BudgetMS:        a.args.IntPtr("budget_ms"),
		RawLimit:        a.args.Int("raw_limit"),
		Session:         a.args.StringPtr("session"),
		Deep:            a.args.Flag("deep"),
		TimeRef:         timeRef,
		TokenCorrect:    a.args.FlagPtr("token_correct"),
		AliasExpand:     a.args.FlagPtr("alias_expand"),
		RelativeTag:     a.args.FlagPtr("relative_tag"),
		MentionBackfill: a.args.FlagPtr("mention_backfill"),
		Presence:        presence,
		NoCrystallize:   a.args.Flag("no_crystallize"),
		RenderMaxChars:  renderMaxChars,
		NeedRendered:    !isJSONOrProbe,
	}
	resp, err := mcp.CallUnixDaemonRecall(context.Background(), "", payload)
	if err != nil {
		var engErr *mcp.EngineRecallError
		if errors.As(err, &engErr) {
			return a.fail(engErr), true
		}
		// Transport / socket failure: fall back to in-process execution transparently.
		return 0, false
	}
	if isJSONOrProbe {
		if a.args.String("result_mode") == "probe" {
			if err := a.printJSON(resp.Probe); err != nil {
				return a.fail(err), true
			}
			return 0, true
		}
		if err := a.printJSON(resp.Result); err != nil {
			return a.fail(err), true
		}
		return 0, true
	}
	if a.args.Flag("wave") {
		qText := strings.TrimSpace(cue + " " + a.args.String("s") + " " + a.args.String("x") + " " + a.args.String("o"))
		amps, hexCode, slots := ComputeTerminalWaveSpec(qText)
		a.printf("%s", RenderBrailleOscilloscope("Recall Query Wave", amps, hexCode, slots, 44))
	}
	a.printf("%s\n", resp.Rendered)
	return 0, true
}

func (a *application) runRecall(memory *api.Memory) (int, error) {
	if a.args.Flag("stdin_batch") {
		return a.runRecallStdinBatch(memory)
	}
	if limit := a.args.Int("raw_limit"); limit < 1 || limit > 64 {
		return 1, errors.New("--raw-limit must be between 1 and 64")
	}
	if bands := a.args.IntPtr("bands"); bands != nil && (*bands < 0 || *bands > 64) {
		return 1, errors.New("--bands must be between 0 and 64")
	}
	cue := a.args.String("cue")
	// The time-ordered recent primitive is cue-less by design (pure time order).
	if cue == "" && a.args.String("result_mode") != "recent" {
		return 1, errors.New("recall requires --q")
	}
	var asOf, windowStart, windowEnd, timeRef any
	if value, ok := a.args.LookupString("asof"); ok {
		asOf = value
	}
	if value, ok := a.args.LookupString("window_start"); ok {
		windowStart = value
	}
	if value, ok := a.args.LookupString("window_end"); ok {
		windowEnd = value
	}
	if value, ok := a.args.LookupString("time_ref"); ok {
		timeRef = value
	}
	var presence *text.PresenceCoordinates
	pLocus := strings.TrimSpace(os.Getenv("ROLLING_MEMORY_PRESENCE_LOCUS"))
	pCarrier := strings.TrimSpace(os.Getenv("ROLLING_MEMORY_PRESENCE_CARRIER"))
	pInc := strings.TrimSpace(os.Getenv("ROLLING_MEMORY_PRESENCE_INCARNATION"))
	if pLocus != "" || pCarrier != "" || pInc != "" {
		coords := text.PresenceCoordinates{
			World:       "actual",
			Carrier:     pCarrier,
			Locus:       pLocus,
			Incarnation: pInc,
		}.Normalized()
		if !coords.IsEmpty() {
			presence = &coords
		}
	}
	result, err := memory.Recall(api.RecallRequest{
		Cue:             cue,
		Reader:          a.args.StringPtr("reader"),
		Mode:            a.args.String("result_mode"),
		AsOf:            asOf,
		WindowStart:     windowStart,
		WindowEnd:       windowEnd,
		CapsBands:       a.args.IntPtr("bands"),
		CapsNodes:       a.args.IntPtr("nodes"),
		CapsChars:       a.args.IntPtr("chars"),
		CapsChains:      a.args.IntPtr("caps_chains"),
		CapsRows:        a.args.IntPtr("caps_rows"),
		ResultUnit:      a.args.String("result_unit"),
		Rank:            a.args.FlagPtr("rank"),
		Offset:          a.args.Int("offset"),
		Depth:           a.args.Int("depth"),
		IncludeRaw:      a.args.FlagPtr("include_raw"),
		BudgetMS:        a.args.IntPtr("budget_ms"),
		RawLimit:        a.args.Int("raw_limit"),
		Session:         a.args.StringPtr("session"),
		Deep:            a.args.Flag("deep"),
		TimeRef:         timeRef,
		TokenCorrect:    a.args.FlagPtr("token_correct"),
		AliasExpand:     a.args.FlagPtr("alias_expand"),
		RelativeTag:     a.args.FlagPtr("relative_tag"),
		MentionBackfill: a.args.FlagPtr("mention_backfill"),
		Presence:        presence,
		NoCrystallize:   a.args.Flag("no_crystallize"),
	})
	if err != nil {
		return 1, err
	}
	// A one-shot process must not exit before the L3 crystallization pulse it scheduled:
	// the wait is bounded and normally instant, and dropping it would leave the engine
	// without the noetic node this recall just produced.
	defer api.WaitForPendingNoeticPulses(api.NoeticPulseBudget)
	if a.args.Flag("json") || a.args.String("result_mode") == "probe" {
		payload := result.ToDict()
		if a.args.String("result_mode") == "probe" {
			payload = result.ToProbe()
		}
		return 0, a.printJSON(payload)
	}
	maxChars := memory.Config.Recall.Chars
	if chars := a.args.IntPtr("chars"); chars != nil && *chars != 0 {
		maxChars = *chars
	}
	if a.args.Flag("wave") {
		qText := strings.TrimSpace(cue + " " + a.args.String("s") + " " + a.args.String("x") + " " + a.args.String("o"))
		amps, hexCode, slots := ComputeTerminalWaveSpec(qText)
		a.printf("%s", RenderBrailleOscilloscope("Recall Query Wave", amps, hexCode, slots, 44))
	}
	a.printf("%s\n", result.Render(maxChars))
	return 0, nil
}

func (a *application) runWave() int {
	text := a.args.String("text")
	width := a.args.Int("width")
	amps, hexCode, slots := ComputeTerminalWaveSpec(text)
	if a.args.Flag("json") {
		ampAny := make([]any, len(amps))
		for i, v := range amps {
			ampAny[i] = v
		}
		slotAny := make([]any, len(slots))
		for i, sl := range slots {
			slotAny[i] = map[string]any{
				"token": sl.Token,
				"amp":   sl.Amp,
				"hex":   sl.Hex,
			}
		}
		if err := a.printJSON(map[string]any{
			"text":       text,
			"hex":        hexCode,
			"amplitudes": ampAny,
			"slots":      slotAny,
		}); err != nil {
			return a.fail(err)
		}
		return 0
	}
	a.printf("%s", RenderBrailleOscilloscope("Memory Engine Oscilloscope", amps, hexCode, slots, width))
	return 0
}

// ------------------------------------------------------------------- config

func (a *application) runInit() int {
	if portableDir := strings.TrimSpace(a.args.String("portable")); portableDir != "" {
		return a.runInitPortable(portableDir)
	}
	db := a.args.StringPtr("db")
	if a.args.Flag("project") && (db == nil || *db == "") {
		cwd, err := os.Getwd()
		if err != nil {
			return a.fail(err)
		}
		value := filepath.Join(cwd, ".rolling-memory", "memory.db")
		db = &value
	}
	cfg, err := config.Load(config.LoadOptions{
		Path:  a.args.StringPtr("config"),
		DB:    db,
		Mode:  a.args.StringPtr("mode"),
		Scope: a.args.StringPtr("scope"),
	})
	if err != nil {
		return a.fail(err)
	}
	if err := cfg.Ensure(); err != nil {
		return a.fail(err)
	}
	if a.args.Flag("write_config") {
		if _, err := config.WriteDefault(cfg.Path); err != nil {
			return a.fail(err)
		}
		if db != nil && *db != "" {
			if _, err := config.SetConfigValue(cfg.Path, "core.db", cfg.DB); err != nil {
				return a.fail(err)
			}
		}
		if scope, ok := a.args.LookupString("scope"); ok && scope != "" {
			if _, err := config.SetConfigValue(cfg.Path, "core.scope", cfg.Scope); err != nil {
				return a.fail(err)
			}
		}
	}
	memory, err := api.New(cfg)
	if err != nil {
		return a.fail(err)
	}
	// 初始化即物化端侧金库 schema（含与 RB 存储同构的 rm_docs / rm_terms 规范表）：
	// 交付面的数据库标准在 init 当刻即确定，不推迟到首次真正使用（import/recall）才创建。
	if _, err := memory.ThinClient(); err != nil {
		return a.fail(err)
	}
	status, statusErr := memory.Status(cfg.Scope)
	closeErr := memory.Close()
	if statusErr != nil {
		return a.fail(statusErr)
	}
	if closeErr != nil {
		return a.fail(closeErr)
	}
	result := map[string]any{
		"ok":             true,
		"db":             cfg.DB,
		"config":         cfg.Path,
		"scope":          cfg.Scope,
		"mode":           cfg.Mode,
		"schema_version": status["schema_version"],
	}
	if err := a.emit(result, func() { a.printInit(result) }); err != nil {
		return a.fail(err)
	}
	return 0
}

func (a *application) runMode() int {
	cfg, err := config.Load(config.LoadOptions{
		Path:  a.args.StringPtr("config"),
		DB:    a.args.StringPtr("db"),
		Mode:  a.args.StringPtr("mode"),
		Scope: a.args.StringPtr("scope"),
	})
	if err != nil {
		return a.fail(err)
	}
	if name := a.args.String("name"); name != "" {
		if _, err := config.SetConfigValue(cfg.Path, "mode.name", name); err != nil {
			return a.fail(err)
		}
		mode := name
		cfg, err = config.Load(config.LoadOptions{
			Path:  a.args.StringPtr("config"),
			DB:    a.args.StringPtr("db"),
			Mode:  &mode,
			Scope: a.args.StringPtr("scope"),
		})
		if err != nil {
			return a.fail(err)
		}
	}
	egress := "loopback-only"
	if cfg.Mode != "local" {
		egress = "remote-allowed"
	}
	result := map[string]any{"ok": true, "mode": cfg.Mode, "egress": egress, "config": cfg.Path}
	if err := a.emit(result, func() {
		a.printf("mode: %s (%s)\n", cfg.Mode, egress)
	}); err != nil {
		return a.fail(err)
	}
	return 0
}

func (a *application) runConfig() int {
	cfg, err := config.Load(config.LoadOptions{
		Path:  a.args.StringPtr("config"),
		DB:    a.args.StringPtr("db"),
		Mode:  a.args.StringPtr("mode"),
		Scope: a.args.StringPtr("scope"),
	})
	if err != nil {
		return a.fail(err)
	}
	switch a.args.Subcommand() {
	case "path":
		a.printLine(cfg.Path)
		return 0
	case "set":
		path, err := config.SetConfigValue(cfg.Path, a.args.String("key"), a.args.String("value"))
		if err != nil {
			return a.fail(err)
		}
		a.printf("updated %s: %s = %s\n", path, a.args.String("key"), a.args.String("value"))
		return 0
	default:
		if err := a.printJSON(cfg.PublicDict()); err != nil {
			return a.fail(err)
		}
		return 0
	}
}

// -------------------------------------------------------------- surface faces

func (a *application) runMCP() int {
	a.setEnvFromArgs()
	if a.args.Flag("daemon") {
		if err := mcp.ServeUnix(mcp.DefaultUnixSocketPath(), nil, a.backgroundMCPAutoWatch); err != nil {
			a.errorf("error: %s\n", ids.Redact(err.Error()))
			return 1
		}
		return 0
	}
	watchCtx, cancelWatch := context.WithCancel(context.Background())
	defer cancelWatch()
	if a.args.Flag("http") {
		go a.backgroundMCPAutoWatch(watchCtx)
		// The URL transports keep one long-lived process: the lazy memory opens
		// on the first tool call and reopens if the database file is replaced.
		if err := mcp.ServeHTTP(a.args.String("addr"), a.args.String("path"), nil); err != nil {
			a.errorf("error: %s\n", ids.Redact(err.Error()))
			return 1
		}
		return 0
	}
	if mcp.ShouldUseUnixDaemon(a.args.String("db")) {
		go func() { _ = mcp.EnsureUnixDaemon(mcp.DefaultUnixSocketPath()) }()
		if err := mcp.ServeWithDaemonProxy(a.stdin, a.stdout); err != nil {
			if isBrokenPipe(err) {
				return 0
			}
			a.errorf("error: %s\n", ids.Redact(err.Error()))
			return 1
		}
		return 0
	}
	go a.backgroundMCPAutoWatch(watchCtx)
	if err := mcp.Serve(a.stdin, a.stdout, nil); err != nil {
		if isBrokenPipe(err) {
			return 0
		}
		// stdout carries the protocol; diagnostics only go to stderr.
		a.errorf("error: %s\n", ids.Redact(err.Error()))
		return 1
	}
	return 0
}

func (a *application) runHook() int {
	if value := a.args.String("config"); value != "" {
		os.Setenv("ROLLING_MEMORY_CONFIG", expandUser(value))
	}
	hookArgs := []string{a.args.String("event")}
	if a.args.Flag("json") {
		hookArgs = append(hookArgs, "--json")
	}
	if budget := a.args.IntPtr("budget_ms"); budget != nil && *budget != 0 {
		hookArgs = append(hookArgs, "--budget-ms", strconv.Itoa(*budget))
	}
	return hooks.HookMain(hookArgs, a.stdin, a.stdout, a.stderr)
}

// setEnvFromArgs exports CLI flag overrides into ROLLING_MEMORY_* environment variables.
func (a *application) setEnvFromArgs() {
	for _, pair := range []struct{ name, env string }{
		{"config", "ROLLING_MEMORY_CONFIG"},
		{"db", "ROLLING_MEMORY_DB"},
		{"scope", "ROLLING_MEMORY_SCOPE"},
		{"mode", "ROLLING_MEMORY_MODE"},
	} {
		if value := a.args.String(pair.name); value != "" {
			os.Setenv(pair.env, value)
		}
	}
}
