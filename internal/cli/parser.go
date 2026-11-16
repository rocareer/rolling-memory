// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

// Package cli implements the rolling-memory / rmem command line, its option
// table, and the human/JSON renderers.
//
// The package owns argv parsing, exit codes (0 success, 1 processed
// application error, 2 usage error) and output formatting. Business decisions
// stay in internal/api and the surface packages wired in here (mcp, hooks,
// harness); the CLI adds no policy of its own.
package cli

import (
	"fmt"
	"strconv"
	"strings"
)

// optionKind is the CLI parser action/type pair used by one option.
type optionKind int

const (
	kindString optionKind = iota
	kindInt
	kindFlagTrue
	kindFlagFalse
)

// Option is one long option in the parser table (CLI parser add_argument).
type Option struct {
	Names    []string
	Dest     string
	Help     string
	Kind     optionKind
	Choices  []string
	Default  any
	Required bool
	Metavar  string
}

func (o *Option) name() string { return o.Names[0] }

func (o *Option) isFlag() bool { return o.Kind == kindFlagTrue || o.Kind == kindFlagFalse }

// usage renders the option the way CLI parser folds it into a usage line.
func (o *Option) usage() string {
	body := o.name()
	if !o.isFlag() {
		body += " " + o.metavar()
	}
	if o.Required {
		return body
	}
	return "[" + body + "]"
}

func (o *Option) metavar() string {
	if o.Metavar != "" {
		return o.Metavar
	}
	if len(o.Choices) > 0 {
		return "{" + strings.Join(o.Choices, ",") + "}"
	}
	// CLI parser's default metavar is dest.upper(); underscores stay.
	return strings.ToUpper(o.Dest)
}

// label renders the option the way CLI parser lists it under "options:": every
// option string, plus the metavar for options that take a value.
func (o *Option) label() string {
	names := strings.Join(o.Names, ", ")
	if o.isFlag() {
		return names
	}
	return names + " " + o.metavar()
}

// positional is one positional argument (nargs default, "?" or "*").
type positional struct {
	Name     string
	Help     string
	Choices  []string
	Required bool
	Variadic bool
}

func (p *positional) usage() string {
	body := p.Name
	if len(p.Choices) > 0 {
		body = "{" + strings.Join(p.Choices, ",") + "}"
	}
	if p.Variadic {
		return "[" + body + " ...]"
	}
	if p.Required {
		return body
	}
	return "[" + body + "]"
}

// label renders the positional the way CLI parser lists it: the choices set when
// it has one (mode's name, hook's event), otherwise the bare name.
func (p *positional) label() string {
	if len(p.Choices) > 0 {
		return "{" + strings.Join(p.Choices, ",") + "}"
	}
	return p.Name
}

// command is one node of the subcommand tree.
type command struct {
	Name        string
	Path        []string
	Help        string
	Description string
	Options     []*Option
	Positionals []*positional
	Subcommands []*command
	SubDest     string
	// DefaultSub 是未显式给出子命令时落入的动作（eval 裸调用 = 全流程 "all"）；
	// 为空时缺少子命令仍按用法错误处理。
	DefaultSub string
}

// helpOption is CLI parser's implicit -h/--help; it takes part in the
// long-option abbreviation search so "--hel" resolves to help.
var helpOption = &Option{Names: []string{"--help"}, Dest: helpDest, Kind: kindFlagTrue,
	Help: "show this help message and exit"}

// helpDest marks the implicit help action (never stored in Args).
const helpDest = "__help__"

// longOption is one option string of a command, in CLI parser registration
// order (help first, then the table, aliases in place).
type longOption struct {
	option *Option
	name   string
}

func (c *command) longOptions() []longOption {
	result := make([]longOption, 0, len(c.Options)+1)
	result = append(result, longOption{helpOption, "--help"})
	for _, option := range c.Options {
		for _, name := range option.Names {
			result = append(result, longOption{option, name})
		}
	}
	return result
}

func (c *command) findOption(name string) *Option {
	for _, option := range c.Options {
		for _, candidate := range option.Names {
			if candidate == name {
				return option
			}
		}
	}
	return nil
}

// matchOption resolves one long option the way CLI parser does: an exact name
// wins, otherwise a unique prefix does (allow_abbrev; CLI parser
// _get_option_tuples). candidates lists every match, in table order, when the
// prefix is ambiguous.
func (c *command) matchOption(name string) (option *Option, matched string, candidates []string) {
	if found := c.findOption(name); found != nil {
		return found, name, nil
	}
	matches := []longOption{}
	for _, candidate := range c.longOptions() {
		if strings.HasPrefix(candidate.name, name) {
			matches = append(matches, candidate)
		}
	}
	if len(matches) == 1 {
		return matches[0].option, matches[0].name, nil
	}
	for _, candidate := range matches {
		candidates = append(candidates, candidate.name)
	}
	return nil, "", candidates
}

func (c *command) findSubcommand(name string) *command {
	for _, sub := range c.Subcommands {
		if sub.Name == name {
			return sub
		}
	}
	return nil
}

func (c *command) subNames() []string {
	names := make([]string, 0, len(c.Subcommands))
	for _, sub := range c.Subcommands {
		names = append(names, sub.Name)
	}
	return names
}

// Parser is the table-driven replacement for CLI parser.
type Parser struct {
	Prog        string
	Description string
	root        *command
}

// UsageError is an CLI parser-style usage failure; Main exits 2.
type UsageError struct {
	Usage   string
	Message string
}

func (e *UsageError) Error() string { return e.Message }

// HelpRequest reports a -h/--help hit; Main prints Text and exits 0.
type HelpRequest struct{ Text string }

func (e *HelpRequest) Error() string { return "help requested" }

// Args is one parsed command line.
type Args struct {
	path     []string
	values   map[string]any
	provided map[string]bool
	version  bool
}

// Command returns the first-level subcommand.
func (a *Args) Command() string {
	if len(a.path) == 0 {
		return ""
	}
	return a.path[0]
}

// Subcommand returns the second-level subcommand (config/govern/sync).
func (a *Args) Subcommand() string {
	if len(a.path) < 2 {
		return ""
	}
	return a.path[1]
}

// String returns a string value, or "" when the option is absent.
func (a *Args) String(name string) string {
	value, _ := a.values[name].(string)
	return value
}

// StringPtr returns nil when the option is absent, mirroring None.
func (a *Args) StringPtr(name string) *string {
	if !a.provided[name] {
		return nil
	}
	value, ok := a.values[name].(string)
	if !ok {
		return nil
	}
	return &value
}

// LookupString distinguishes an absent option from an empty one.
func (a *Args) LookupString(name string) (string, bool) {
	if !a.provided[name] {
		return "", false
	}
	value, ok := a.values[name].(string)
	return value, ok
}

// Int returns an int value, or 0 when the option is absent.
func (a *Args) Int(name string) int {
	value, _ := a.values[name].(int)
	return value
}

// Float returns a float value, or 0 when the option is absent.
func (a *Args) Float(name string) float64 {
	switch value := a.values[name].(type) {
	case float64:
		return value
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err == nil {
			return parsed
		}
	}
	return 0
}

// IntPtr returns nil when the option is absent.
func (a *Args) IntPtr(name string) *int {
	if !a.provided[name] {
		return nil
	}
	value, ok := a.values[name].(int)
	if !ok {
		return nil
	}
	return &value
}

// Flag returns a boolean value, or false when the option is absent.
func (a *Args) Flag(name string) bool {
	value, _ := a.values[name].(bool)
	return value
}

// FlagPtr keeps the CLI parser None default of tri-state flags.
func (a *Args) FlagPtr(name string) *bool {
	if !a.provided[name] {
		return nil
	}
	value, ok := a.values[name].(bool)
	if !ok {
		return nil
	}
	return &value
}

// Raw returns the stored value, preserving --turn's str-or-int type.
func (a *Args) Raw(name string) any { return a.values[name] }

// List returns a variadic positional slice.
func (a *Args) List(name string) []string {
	value, _ := a.values[name].([]string)
	return value
}

// Parse resolves argv against the table. Errors are *UsageError or
// *HelpRequest; both are printed by Main and are not application failures.
func (p *Parser) Parse(argv []string) (*Args, error) {
	args := &Args{values: map[string]any{}, provided: map[string]bool{}}
	if err := p.parseLevel(p.root, argv, args); err != nil {
		return nil, err
	}
	return args, nil
}

func (p *Parser) parseLevel(cmd *command, tokens []string, args *Args) error {
	args.path = cmd.Path
	for _, option := range cmd.Options {
		if option.Default != nil && !args.provided[option.Dest] {
			args.values[option.Dest] = option.Default
		}
	}
	for _, pos := range cmd.Positionals {
		if pos.Variadic {
			args.values[pos.Name] = []string{}
		}
	}

	positionalIndex := 0
	stopOptions := false
	var selected *command
	var remainder []string

	for index := 0; index < len(tokens); index++ {
		token := tokens[index]
		if !stopOptions && token == "--" {
			stopOptions = true
			continue
		}
		if !stopOptions && (token == "-h" || token == "--help") {
			return &HelpRequest{Text: p.help(cmd)}
		}
		if !stopOptions && strings.HasPrefix(token, "--") {
			name, value, attached := strings.Cut(token, "=")
			option, matched, candidates := cmd.matchOption(name)
			if len(candidates) > 1 {
				return p.usageError(cmd, "ambiguous option: %s could match %s",
					name, strings.Join(candidates, ", "))
			}
			if option == nil {
				return p.usageError(cmd, "unrecognized arguments: %s", token)
			}
			if option.Dest == helpDest {
				if attached {
					return p.usageError(cmd, "argument %s: ignored explicit argument '%s'", matched, value)
				}
				return &HelpRequest{Text: p.help(cmd)}
			}
			if option.Dest == "__version__" {
				args.version = true
				return nil
			}
			if option.isFlag() {
				if attached {
					return p.usageError(cmd, "argument %s: ignored explicit argument '%s'", matched, value)
				}
				if option.Kind == kindFlagTrue {
					args.values[option.Dest] = true
				} else {
					args.values[option.Dest] = false
				}
				args.provided[option.Dest] = true
				continue
			}
			if !attached {
				if index+1 >= len(tokens) || looksLikeOption(tokens[index+1]) {
					return p.usageError(cmd, "argument %s: expected one argument", matched)
				}
				index++
				value = tokens[index]
			}
			if err := p.assignOption(cmd, option, matched, value, args); err != nil {
				return err
			}
			continue
		}
		if !stopOptions && token != "-" && strings.HasPrefix(token, "-") {
			return p.usageError(cmd, "unrecognized arguments: %s", token)
		}

		if len(cmd.Subcommands) > 0 {
			sub := cmd.findSubcommand(token)
			if sub == nil {
				return p.usageError(cmd, "argument %s: invalid choice: '%s' (choose from %s)",
					cmd.SubDest, token, quoteChoices(cmd.subNames()))
			}
			selected = sub
			remainder = tokens[index+1:]
			break
		}
		if positionalIndex >= len(cmd.Positionals) {
			return p.usageError(cmd, "unrecognized arguments: %s", token)
		}
		pos := cmd.Positionals[positionalIndex]
		if err := p.assignPositional(cmd, pos, token, args); err != nil {
			return err
		}
		if !pos.Variadic {
			positionalIndex++
		}
	}

	missing := []string{}
	for _, option := range cmd.Options {
		if !option.Required || args.provided[option.Dest] {
			continue
		}
		missing = append(missing, option.name())
	}
	for _, pos := range cmd.Positionals {
		if !pos.Required {
			continue
		}
		if _, ok := args.values[pos.Name]; !ok {
			missing = append(missing, pos.Name)
		}
	}
	if len(missing) > 0 {
		return p.usageError(cmd, "the following arguments are required: %s", strings.Join(missing, ", "))
	}
	if len(cmd.Subcommands) > 0 {
		if selected == nil {
			if cmd.DefaultSub == "" {
				return p.usageError(cmd, "the following arguments are required: %s", cmd.SubDest)
			}
			selected = cmd.findSubcommand(cmd.DefaultSub)
			remainder = nil
		}
		return p.parseLevel(selected, remainder, args)
	}
	return nil
}

// assignOption stores one option value; label is the option string the token
// resolved to (an abbreviated prefix reports the full name, like CLI parser).
func (p *Parser) assignOption(cmd *command, option *Option, label, raw string, args *Args) error {
	if err := p.checkChoice(cmd, label, raw, option.Choices); err != nil {
		return err
	}
	switch option.Kind {
	case kindInt:
		number, err := strconv.Atoi(raw)
		if err != nil {
			return p.usageError(cmd, "argument %s: invalid int value: '%s'", label, raw)
		}
		args.values[option.Dest] = number
	default:
		args.values[option.Dest] = raw
	}
	args.provided[option.Dest] = true
	return nil
}

func (p *Parser) assignPositional(cmd *command, pos *positional, raw string, args *Args) error {
	if err := p.checkChoice(cmd, pos.Name, raw, pos.Choices); err != nil {
		return err
	}
	if pos.Variadic {
		values, _ := args.values[pos.Name].([]string)
		args.values[pos.Name] = append(values, raw)
	} else {
		args.values[pos.Name] = raw
	}
	args.provided[pos.Name] = true
	return nil
}

func (p *Parser) checkChoice(cmd *command, label, raw string, choices []string) error {
	if len(choices) == 0 {
		return nil
	}
	for _, choice := range choices {
		if choice == raw {
			return nil
		}
	}
	return &UsageError{
		Usage:   p.usage(cmd),
		Message: fmt.Sprintf("argument %s: invalid choice: '%s' (choose from %s)", label, raw, quoteChoices(choices)),
	}
}

func (p *Parser) usageError(cmd *command, format string, values ...any) error {
	return &UsageError{
		Usage:   p.usage(cmd),
		Message: fmt.Sprintf(format, values...),
	}
}

func quoteChoices(choices []string) string {
	quoted := make([]string, 0, len(choices))
	for _, choice := range choices {
		quoted = append(quoted, "'"+choice+"'")
	}
	return strings.Join(quoted, ", ")
}

// looksLikeOption mirrors CLI parser's rule for "is this the next value or the
// next flag?"; negative numbers stay values because no option looks like one.
func looksLikeOption(token string) bool {
	if len(token) < 2 || !strings.HasPrefix(token, "-") {
		return false
	}
	char := token[1]
	if char >= '0' && char <= '9' || char == '.' {
		return false
	}
	return true
}

// usage renders the full CLI parser usage line for one command.
func (p *Parser) usage(cmd *command) string {
	var builder strings.Builder
	builder.WriteString("usage: ")
	builder.WriteString(p.Prog)
	for _, part := range cmd.Path {
		builder.WriteString(" ")
		builder.WriteString(part)
	}
	builder.WriteString(" [-h]")
	for _, option := range cmd.Options {
		builder.WriteString(" ")
		builder.WriteString(option.usage())
	}
	for _, pos := range cmd.Positionals {
		builder.WriteString(" ")
		builder.WriteString(pos.usage())
	}
	if len(cmd.Subcommands) > 0 {
		builder.WriteString(" {")
		builder.WriteString(strings.Join(cmd.subNames(), ","))
		builder.WriteString("} ...")
	}
	return builder.String()
}

// help renders one command the way CLI parser does: usage, the description (the
// root's description only appears on the root parser; subparsers have none),
// positionals, then options.
func (p *Parser) help(cmd *command) string {
	var builder strings.Builder
	builder.WriteString(p.usage(cmd))
	builder.WriteString("\n")
	description := cmd.Description
	if description == "" && cmd == p.root {
		description = p.Description
	}
	if description != "" {
		builder.WriteString("\n")
		builder.WriteString(description)
		builder.WriteString("\n")
	}
	hasPositionals := len(cmd.Positionals) > 0 || len(cmd.Subcommands) > 0
	if hasPositionals {
		builder.WriteString("\npositional arguments:\n")
		for _, pos := range cmd.Positionals {
			builder.WriteString(fmt.Sprintf("  %-20s %s\n", pos.label(), pos.Help))
		}
		if len(cmd.Subcommands) > 0 {
			builder.WriteString(fmt.Sprintf("  %-20s\n", "{"+strings.Join(cmd.subNames(), ",")+"}"))
			for _, sub := range cmd.Subcommands {
				if sub.Help == "" {
					// CLI parser only lists subcommands that were given a help=
					// (the bare {a,b} line still shows every choice).
					continue
				}
				builder.WriteString(fmt.Sprintf("    %-18s %s\n", sub.Name, sub.Help))
			}
		}
	}
	builder.WriteString("\noptions:\n")
	builder.WriteString(fmt.Sprintf("  %-20s %s\n", "-h, --help", helpOption.Help))
	if cmd == p.root {
		builder.WriteString("  --version             show program's version number and exit\n")
	}
	for _, option := range cmd.Options {
		if option.Dest == "__version__" {
			continue
		}
		builder.WriteString(fmt.Sprintf("  %-20s %s\n", option.label(), option.Help))
	}
	return builder.String()
}

// ------------------------------------------------------------ parser table

func stringOption(names []string, dest string, def any, help string) *Option {
	return &Option{Names: names, Dest: dest, Kind: kindString, Default: def, Help: help}
}

func intOption(names []string, dest string, def int, help string) *Option {
	return &Option{Names: names, Dest: dest, Kind: kindInt, Default: def, Help: help}
}

func floatOption(names []string, dest string, def float64, help string) *Option {
	return &Option{Names: names, Dest: dest, Kind: kindString,
		Default: strconv.FormatFloat(def, 'g', -1, 64), Help: help}
}

func flagOption(names []string, dest string, def any, help string) *Option {
	return &Option{Names: names, Dest: dest, Kind: kindFlagTrue, Default: def, Help: help}
}

func choiceOption(names []string, dest string, choices []string, def any, help string) *Option {
	return &Option{Names: names, Dest: dest, Kind: kindString, Choices: choices, Default: def, Help: help}
}

func requiredOption(names []string, dest, metavar, help string) *Option {
	return &Option{Names: names, Dest: dest, Kind: kindString, Required: true, Metavar: metavar, Help: help}
}

func commonOptions() []*Option {
	return []*Option{
		stringOption([]string{"--config"}, "config", nil,
			"config file (default: $XDG_CONFIG_HOME/rolling-memory/config.toml)"),
		stringOption([]string{"--db"}, "db", nil, "database path"),
		stringOption([]string{"--scope"}, "scope", nil, "memory scope"),
		choiceOption([]string{"--mode"}, "mode", []string{"local", "cloud"}, nil,
			"override mode for this command"),
		flagOption([]string{"--json"}, "json", false, ""),
	}
}

// BuildParser returns the full CLI parser-compatible command table.
func BuildParser() *Parser {
	parser := &Parser{
		Prog:        "rolling-memory",
		Description: "Local-first graph memory for coding agents (SQLite, single `recall` MCP tool).",
	}
	root := &command{
		SubDest: "command",
		Options: []*Option{{
			Names: []string{"--version"}, Dest: "__version__", Kind: kindFlagTrue,
			Help: "show program's version number and exit",
		}},
	}

	initCmd := &command{
		Name: "init", Path: []string{"init"}, Help: "create the database and show paths",
		Options: append(commonOptions(),
			flagOption([]string{"--write-config"}, "write_config", false,
				"write a commented default config file"),
			flagOption([]string{"--project"}, "project", false,
				"use ./.rolling-memory/memory.db for this project"),
			stringOption([]string{"--portable"}, "portable", "",
				"assemble a self-contained USB portable payload at DIR"),
		),
	}

	modeCmd := &command{
		Name: "mode", Path: []string{"mode"}, Help: "show or switch storage mode (local/cloud)",
		Options: commonOptions(),
		Positionals: []*positional{{
			Name: "name", Choices: []string{"local", "cloud"},
		}},
	}

	configCmd := &command{
		Name: "config", Path: []string{"config"}, Help: "show or edit configuration",
		SubDest: "config_command",
	}
	configCmd.Subcommands = []*command{
		{Name: "show", Path: []string{"config", "show"}, Options: commonOptions()},
		{Name: "path", Path: []string{"config", "path"}, Options: commonOptions()},
		{
			Name: "set", Path: []string{"config", "set"}, Options: commonOptions(),
			Positionals: []*positional{
				{Name: "key", Help: "dotted key, e.g. mode.name", Required: true},
				{Name: "value", Required: true},
			},
		},
	}

	recallCmd := &command{
		Name: "recall", Path: []string{"recall"}, Help: "read memory as bands x chains",
		Options: append(commonOptions(),
			stringOption([]string{"--q", "--cue"}, "cue", "", ""),
			choiceOption([]string{"--reader"}, "reader", []string{"owner", "deliverable"}, nil, ""),
			choiceOption([]string{"--result-mode"}, "result_mode", []string{"full", "probe", "recent"}, "full", ""),
			stringOption([]string{"--asof"}, "asof", nil, "reserved; specifying this filter currently fails closed"),
			stringOption([]string{"--window-start"}, "window_start", nil, "reserved; specifying this filter currently fails closed"),
			stringOption([]string{"--window-end"}, "window_end", nil, "reserved; specifying this filter currently fails closed"),
			&Option{Names: []string{"--bands"}, Dest: "bands", Kind: kindInt, Default: nil, Help: ""},
			&Option{Names: []string{"--nodes"}, Dest: "nodes", Kind: kindInt, Default: nil, Help: ""},
			&Option{Names: []string{"--chars"}, Dest: "chars", Kind: kindInt, Default: nil, Help: ""},
			intOption([]string{"--offset"}, "offset", 0, ""),
			intOption([]string{"--depth"}, "depth", 2, ""),
			flagOption([]string{"--include-raw"}, "include_raw", nil, ""),
			&Option{Names: []string{"--no-raw"}, Dest: "include_raw", Kind: kindFlagFalse, Help: ""},
			intOption([]string{"--raw-limit"}, "raw_limit", 10, "raw-hit limit (default 10, maximum 64)"),
			stringOption([]string{"--session"}, "session", nil,
				"reserved; specifying this filter currently fails closed"),
			stringOption([]string{"--time-ref"}, "time_ref", nil,
				"reference time for relative expressions (ISO date or epoch)"),
			flagOption([]string{"--token-correct"}, "token_correct", nil,
				"enable the zero-hit token correction leg"),
			&Option{Names: []string{"--no-alias-expand"}, Dest: "alias_expand", Kind: kindFlagFalse,
				Help: "disable raw-leg token expansion with seed aliases"},
			flagOption([]string{"--relative-tag"}, "relative_tag", nil,
				"annotate relative time phrases with resolved dates"),
			&Option{Names: []string{"--no-mention-backfill"}, Dest: "mention_backfill", Kind: kindFlagFalse,
				Help: "disable mention/anchor backfill from raw hits"},
			&Option{Names: []string{"--budget-ms"}, Dest: "budget_ms", Kind: kindInt, Default: nil, Help: ""},
			choiceOption([]string{"--result-unit"}, "result_unit", []string{"band", "item"}, nil,
				"delivery unit: subject bands (default) or flat source items"),
			flagOption([]string{"--rank"}, "rank", nil,
				"item-mode grouping: grouped by 记忆单元→原文块 (default); pair with --no-rank for pure relevance order"),
			&Option{Names: []string{"--no-rank"}, Dest: "rank", Kind: kindFlagFalse,
				Help: "deliver items in pure relevance order without grouping"},
			&Option{Names: []string{"--caps-chains"}, Dest: "caps_chains", Kind: kindInt, Default: nil, Help: ""},
			&Option{Names: []string{"--caps-rows"}, Dest: "caps_rows", Kind: kindInt, Default: nil, Help: ""},
			flagOption([]string{"--deep"}, "deep", false,
				"allow one LLM query rewrite (cloud/loopback only)"),
			flagOption([]string{"--wave"}, "wave", false,
				"render high-resolution Unicode Braille oscilloscope waveform in terminal"),
			flagOption([]string{"--no-crystallize"}, "no_crystallize", false,
				"read-only recall: do not record this query as an L3 noetic entry (debug consoles, probes)"),
			flagOption([]string{"--stdin-batch"}, "stdin_batch", false,
				"serve multiple recall requests over stdin JSONL ({\"req_id\",\"q\",\"raw_limit\"}) on one warm process; one JSON response per line"),
		),
	}

	recentCmd := &command{
		Name: "recent", Path: []string{"recent"}, Help: "show the most recent memory units",
		Options: append(commonOptions(),
			intOption([]string{"--limit"}, "limit", 10, ""),
			choiceOption([]string{"--reader"}, "reader", []string{"owner", "deliverable"}, nil, ""),
		),
	}

	// evalOptions 同时挂在 eval 父命令（裸调用 = 全流程）与全部阶段子命令上：
	// 每个阶段的检索/模型/数据面参数与一把梭完全一致，子命令只决定推进哪一段状态机。
	evalOptions := []*Option{
		stringOption([]string{"--config"}, "config", nil,
			"config file (default: $XDG_CONFIG_HOME/rolling-memory/config.toml)"),
		flagOption([]string{"--json"}, "json", false, ""),
		stringOption([]string{"--job"}, "job", nil,
			"evaluation job id (default for ingest/run/grade/status: latest job of the suite/store)"),
		choiceOption([]string{"--suite"}, "suite", []string{"locomo", "atm"}, "locomo",
			"benchmark suite (locomo | atm)"),
		choiceOption([]string{"--tier"}, "tier", []string{"1540", "hard", "1013"}, "1540",
			"question tier (LoCoMo: 1540 = official cat1-4 set; ATM-Bench: hard/1013; the retired LoCoMo convenience tiers 100/200 are no longer offered)"),
		stringOption([]string{"--eval-data"}, "eval_data", nil,
			"evaluation data directory (default: evaldata next to the binary)"),
		stringOption([]string{"--fetch"}, "fetch", nil,
			"download upstream benchmark data before running (atm = ATM-Bench from Hugging Face, ~35 MB, rebuilt locally into the pinned form; locomo = LoCoMo10 annotations from the upstream repository, ~2.8 MB, rebuilt into the official cat1-4 question set)"),
		flagOption([]string{"--fetch-only"}, "fetch_only", false,
			"only download + rebuild + verify the upstream data, then exit"),
		stringOption([]string{"--fetch-revision"}, "fetch_revision", nil,
			"override the pinned upstream dataset revision (advanced; default: the revision pinned in this binary)"),
		flagOption([]string{"--accept-upstream-license"}, "accept_upstream_license", false,
			"accept the upstream data license (CC BY-NC 4.0) before downloading"),
		stringOption([]string{"--out-dir"}, "out_dir", nil,
			"report directory (default: <state>/eval/out; jobs live in <out-dir>/jobs)"),
		stringOption([]string{"--eval-store"}, "eval_store", nil,
			"isolated plaintext JSON vault (default: <state>/eval/eval-locomo.json or eval-atm.json)"),
		flagOption([]string{"--oracle"}, "oracle", false,
			"gold evidence as context (answerer ceiling)"),
		choiceOption([]string{"--answerer"}, "answerer", []string{"none", "llm"}, nil,
			"answerer side of the frozen protocol (default: [eval] answerer)"),
		choiceOption([]string{"--judge"}, "judge", []string{"off", "llm"}, nil,
			"judge side of the frozen protocol (default: [eval] judge)"),
		stringOption([]string{"--eval-model"}, "eval_model", nil,
			"override [eval] model (wire name; e.g. the gateway-side name)"),
		stringOption([]string{"--eval-base-url"}, "eval_base_url", nil,
			"override [eval] base_url (any OpenAI-compatible /chat/completions root)"),
		{Names: []string{"--limit"}, Dest: "limit", Kind: kindInt, Default: nil,
			Help: "cap questions"},
		intOption([]string{"--offset"}, "offset", 0, "question offset"),
		intOption([]string{"--workers"}, "workers", 0, "parallel questions (default: [eval] workers)"),
		intOption([]string{"--timeout"}, "timeout", 0, "per-call timeout seconds"),
		intOption([]string{"--max-tokens"}, "max_tokens", 0, "per-call max tokens"),
		intOption([]string{"--context-chars"}, "context_chars", 0, "context character cap"),
		intOption([]string{"--top-k", "--raw-limit"}, "raw_limit", 0, "remote engine hit cap"),
		intOption([]string{"--corpus-limit"}, "corpus_limit", 0, "ingest only N corpus rows (smoke)"),
		intOption([]string{"--progress-every"}, "progress_every", 20, "progress line interval"),
		flagOption([]string{"--rebuild"}, "rebuild", false, "reset the isolated cloud scope (purge every remembered unit), then re-ingest the corpus"),
		flagOption([]string{"--reuse-prepare"}, "reuse_prepare", false, "reuse an identical isolated corpus only after exact remote-set verification"),
		flagOption([]string{"--verify-prepare"}, "verify_prepare", false, "verify exact manifest/corpus/remote-unit equality without ingesting or scoring"),
		choiceOption([]string{"--pulse-tokenizer"}, "pulse_tokenizer", []string{"fts5-bm25", "standard", "fts5-trigram"}, "fts5-bm25",
			"blind pulse tokenizer: fts5-bm25 = default max-capability product tokenizer (128k BPE prior + stem/root/compound harmonics + unit/date beacons; default) | standard = SDK native surface+subword | fts5-trigram = local FTS5 trigram index tokenizer"),
		choiceOption([]string{"--pulse-mode"}, "pulse_mode", []string{"local_plain", "pulse_cipher", "pulse_plain"}, "local_plain",
			"evaluation arm (each registered in its own isolated scope & store): local_plain (本地明文, default; the only arm available in local mode) | pulse_cipher (脉冲密文, requires cloud mode) | pulse_plain (脉冲明文, requires cloud mode)"),
		flagOption([]string{"--no-verify"}, "no_verify", false,
			"skip the sha256 check of the bundled data files"),
		floatOption([]string{"--denoise-ratio"}, "denoise_ratio", 0.0,
			"分形干句降噪比例 (0.0=零损, 0.25=安全, 0.50=推荐, 0.75=激进)"),
	}
	evalStage := func(name, help string) *command {
		return &command{Name: name, Path: []string{"eval", name}, Help: help, Options: evalOptions}
	}
	evalCmd := &command{
		Name: "eval", Path: []string{"eval"},
		Help:    "isolated cloud-SDK evaluation (jobs: import → ingest → run → grade)",
		Options: evalOptions, SubDest: "eval_command", DefaultSub: "all",
		Subcommands: []*command{
			evalStage("all", "full pipeline: import → ingest → run → grade (same as a bare `rmem eval`)"),
			evalStage("import", "import corpus: verify data files and materialize job artifacts (offline)"),
			evalStage("ingest", "ingest: write the job corpus to the isolated cloud scope through the SDK"),
			evalStage("run", "run the benchmark: per-question recall only (no LLM), writes recall artifacts"),
			evalStage("grade", "answer + judge with your own OpenAI-compatible LLM, reads recall artifacts"),
			evalStage("status", "show one recorded evaluation run (--job or the latest) from the out-dir ledger (read-only)"),
			evalStage("jobs", "list recorded evaluation runs from the out-dir ledger (read-only, never triggers a run)"),
		},
	}

	importCmd := &command{
		Name: "import", Path: []string{"import"}, Help: "import a transcript (JSONL or text)",
		Options: append(commonOptions(),
			stringOption([]string{"--session"}, "session", nil, ""),
			choiceOption([]string{"--audience"}, "audience",
				[]string{"private", "group", "shared", "public"}, "shared", ""),
			flagOption([]string{"--replay"}, "replay", false, ""),
		),
		Positionals: []*positional{{Name: "path", Required: true}},
	}

	exportCmd := &command{
		Name: "export", Path: []string{"export"}, Help: "unavailable: JSON export is not a complete backup",
		Options: append(commonOptions(), stringOption([]string{"--out"}, "out", nil, "")),
	}

	syncCmd := &command{
		Name: "sync", Path: []string{"sync"}, Help: "optional cloud sync (cloud mode only)",
		SubDest: "sync_command",
	}
	syncStatusCmd := &command{
		Name: "status", Path: []string{"sync", "status"}, Options: append(commonOptions(),
			stringOption([]string{"--harness"}, "harness", nil, "optional harness id filter (zcode/claude-code/codex/all)")),
	}
	syncReconcileCmd := &command{
		Name: "reconcile", Path: []string{"sync", "reconcile"}, Options: append(commonOptions(),
			stringOption([]string{"--layer"}, "layer", "all", "layer to reconcile (all|episodic|background|noetic)"),
			stringOption([]string{"--harness"}, "harness", nil, "optional harness id filter (zcode/claude-code/codex/all)"),
			intOption([]string{"--limit"}, "limit", 50, "max units to reconcile per layer in one pass"),
			flagOption([]string{"--rebuild-episodic"}, "rebuild_episodic", false, "rebuild L2 episodic as one unit per speech segment (dry-run unless --yes)"),
			flagOption([]string{"--yes"}, "yes", false, "confirm the episodic rebuild (purges superseded units)")),
	}
	syncWatchCmd := &command{
		Name: "watch", Path: []string{"sync", "watch"}, Options: append(commonOptions(),
			intOption([]string{"--interval-sec"}, "interval_sec", 10, "poll cadence in seconds"),
			flagOption([]string{"--once"}, "once", false, "run one single-flight auto-watch pass and exit"),
			flagOption([]string{"--pause"}, "pause", false, "pause background episodic scanning and index backfilling"),
			flagOption([]string{"--resume"}, "resume", false, "resume background episodic scanning and index backfilling"),
			flagOption([]string{"--step"}, "step", false, "advance one bounded pass of episodic index backfilling and source scanning")),
	}
	syncPruneCmd := &command{
		Name: "prune", Path: []string{"sync", "prune"}, Options: append(commonOptions(),
			intOption([]string{"--limit"}, "limit", 20000, "max pointer rows to classify in one pass"),
			flagOption([]string{"--include-unreadable"}, "include_unreadable", false, "also retire pointers whose source session or transcript is gone"),
			flagOption([]string{"--apply"}, "apply", false, "retire the units whose content the turn no longer yields (dry-run without it)"),
			flagOption([]string{"--yes"}, "yes", false, "confirm the prune (required with --apply)")),
	}
	syncCmd.Subcommands = []*command{syncStatusCmd, syncReconcileCmd, syncWatchCmd, syncPruneCmd}

	backgroundCmd := &command{
		Name: "background", Path: []string{"background"}, Help: "mount, read and synchronize local document directories",
		SubDest: "background_command",
	}
	backgroundCmd.Subcommands = []*command{
		{
			Name: "add", Path: []string{"background", "add"}, Options: append(commonOptions(),
				requiredOption([]string{"--path"}, "path", "DIR", "local directory to mount and ingest")),
		},
		{Name: "sync", Path: []string{"background", "sync"}, Options: commonOptions()},
		{
			Name: "remove", Path: []string{"background", "remove"}, Options: append(commonOptions(),
				requiredOption([]string{"--path"}, "path", "DIR", "mounted directory to purge and unmount")),
		},
		{Name: "list", Path: []string{"background", "list"}, Options: commonOptions()},
	}

	mcpCmd := &command{
		Name: "mcp", Path: []string{"mcp"},
		Help: "run the MCP server (recall); --http serves the URL transports",
		Options: append(commonOptions(),
			flagOption([]string{"--http"}, "http", false,
				"serve the MCP URL transports (streamable HTTP + legacy SSE) instead of stdio"),
			flagOption([]string{"--daemon"}, "daemon", false,
				"serve the shared local Unix domain socket engine daemon"),
			stringOption([]string{"--addr"}, "addr", "",
				"listen address for --http (default 127.0.0.1:8765)"),
			stringOption([]string{"--path"}, "path", "",
				"streamable HTTP endpoint path (default /mcp)")),
	}

	hookCmd := &command{
		Name: "hook", Path: []string{"hook"}, Help: "run a harness hook (fail-open)",
		Options: []*Option{
			flagOption([]string{"--json"}, "json", false, ""),
			{Names: []string{"--budget-ms"}, Dest: "budget_ms", Kind: kindInt, Default: nil, Help: ""},
			stringOption([]string{"--config"}, "config", nil, ""),
		},
		Positionals: []*positional{{
			Name: "event", Choices: []string{"start", "recall", "close"}, Required: true,
		}},
	}

	statusCmd := &command{
		Name: "status", Path: []string{"status"}, Help: "show counts, mode and storage paths",
		Options: append(commonOptions(),
			flagOption([]string{"--wave"}, "wave", false,
				"render high-resolution Unicode Braille oscilloscope waveform in terminal"),
		),
	}
	doctorCmd := &command{
		Name: "doctor", Path: []string{"doctor"}, Help: "environment and integrity diagnostics",
		Options: commonOptions(),
	}
	checkCmd := &command{
		Name: "check", Path: []string{"check"}, Help: "run integrity checks; exit 1 on errors",
		Options: commonOptions(),
	}
	reindexCmd := &command{
		Name: "reindex", Path: []string{"reindex"}, Help: "rebuild the lexical index and the local compatibility index（本地兼容索引，不参与引擎检索）",
		Options: commonOptions(),
	}

	installCmd := &command{
		Name: "install", Path: []string{"install"},
		Help: "wire this memory core into agent harnesses (skill + MCP + optional hooks/instructions)",
		Options: []*Option{
			flagOption([]string{"--list"}, "list", false, "show the harness adapter table"),
			flagOption([]string{"--dry-run"}, "dry_run", false,
				"show planned writes without touching files"),
			flagOption([]string{"--uninstall"}, "uninstall", false,
				"remove what install wrote"),
			flagOption([]string{"--check"}, "check", false,
				"verify registration and the MCP handshake"),
			flagOption([]string{"--project"}, "project", false,
				"write into the current project instead of user scope"),
			stringOption([]string{"--name"}, "name", "rolling-memory",
				"MCP server name (default: rolling-memory)"),
			stringOption([]string{"--skill-name"}, "skill_name", nil,
				"installed skill name (default: same as --name)"),
			stringOption([]string{"--binary"}, "binary", nil,
				"launcher command to register (default: generated shim)"),
			stringOption([]string{"--skill-file"}, "skill_file", nil,
				"SKILL.md source (default: skills/rolling-memory/SKILL.md)"),
			stringOption([]string{"--lang"}, "lang", "en",
				"language for installed skill, instructions, and admin console default (en | zh-CN; default: en)"),
			stringOption([]string{"--db"}, "db", nil,
				"ROLLING_MEMORY_DB written into the MCP entry"),
			stringOption([]string{"--scope"}, "scope", nil,
				"ROLLING_MEMORY_SCOPE written into the MCP entry"),
			flagOption([]string{"--mcp-only"}, "mcp_only", false,
				"wire only the MCP server (skip skill, hooks, and instruction files)"),
			flagOption([]string{"--with-hooks"}, "with_hooks", false,
				"wire the per-prompt recall nudge (UserPromptSubmit)"),
			flagOption([]string{"--with-instructions"}, "with_instructions", false,
				"append the marker block to instruction files"),
			flagOption([]string{"--desktop-shortcut"}, "desktop_shortcut", false,
				"also create a desktop launcher that opens the local web console"),
			flagOption([]string{"--force"}, "force", false,
				"overwrite same-named foreign entries"),
			stringOption([]string{"--profile"}, "profile", "web",
				"dsh profile for the MCP patch (default: web)"),
			flagOption([]string{"--json"}, "json", false, ""),
		},
		Positionals: []*positional{{
			Name: "harnesses", Variadic: true,
			Help: "harness ids (default: all detected); see --list",
		}},
	}

	ejectCmd := &command{
		Name: "eject", Path: []string{"eject"},
		Help:    "flush and truncate the SQLite WAL and verify integrity before removable media eject",
		Options: commonOptions(),
	}

	efficacyCmd := &command{
		Name: "efficacy", Path: []string{"efficacy"},
		Help: "evaluate cross-harness recall efficacy, round-trip savings and net token ROI",
		Options: append(commonOptions(),
			stringOption([]string{"--harness"}, "harness", "",
				"filter by harness id (zcode/claude-code/codex/cursor/windsurf/cline/dsh/codebuddy/workbuddy/doubao)"),
			intOption([]string{"--limit"}, "limit", 20,
				"maximum recent session evaluations to list"),
			intOption([]string{"--budget-seconds"}, "budget_seconds", 0,
				"soft wall-clock budget for settling unsettled turns; the run finalizes and persists a partial snapshot once exhausted (0 = unbounded)"),
			flagOption([]string{"--card"}, "card", false,
				"render a compact shareable ASCII ROI receipt card"),
		),
	}

	waveCmd := &command{
		Name: "wave", Path: []string{"wave"},
		Help: "render high-resolution Unicode Braille oscilloscope waveform for a sentence or query",
		Options: append(commonOptions(),
			intOption([]string{"--width"}, "width", 44, "oscilloscope character column width (20..80)"),
		),
		Positionals: []*positional{{
			Name: "text", Help: "input sentence or query text", Required: true,
		}},
	}

	panelCmd := &command{
		Name: "panel", Path: []string{"panel"},
		Help: "open the local web console (backend :9006 + web frontend :8008; fetches the console on first use)",
		Options: append(commonOptions(),
			flagOption([]string{"--no-open"}, "no_open", false,
				"start the console without opening the browser"),
			flagOption([]string{"--update"}, "update", false,
				"check the release channel and update console backend/frontend assets if newer digests exist"),
			stringOption([]string{"--release-base"}, "release_base", nil,
				"override the release channel URL (default: official GitHub releases; ROLLING_MEMORY_RELEASE_BASE also supported)"),
			intOption([]string{"--api-port"}, "api_port", 0,
				"console backend port (default: 9006; RM_PANEL_API_PORT overrides)"),
			intOption([]string{"--web-port"}, "web_port", 0,
				"console web port (default: 8008; RM_PANEL_WEB_PORT overrides)"),
			stringOption([]string{"--serve-web"}, "serve_web", nil,
				"internal: serve console web assets from this directory in the foreground"),
		),
		Positionals: []*positional{{
			Name: "verbs", Variadic: true, Choices: []string{"start", "stop", "status"},
			Help: "panel action (default: start)",
		}},
	}

	updateOptions := append(commonOptions(),
		flagOption([]string{"--check"}, "check", false,
			"only check whether a newer release is available without modifying files"),
		flagOption([]string{"--force"}, "force", false,
			"re-download and reinstall even when local SHA-256 digests already match"),
		flagOption([]string{"--with-console"}, "with_console", false,
			"also fetch and install the local web console if not yet installed"),
		flagOption([]string{"--no-console"}, "no_console", false,
			"skip checking or updating the local web console assets"),
		flagOption([]string{"--no-install"}, "no_install", false,
			"skip refreshing host harness integrations after updating the binary"),
		flagOption([]string{"--defer-console-restart"}, "defer_console_restart", false,
			"update console assets in place without synchronously stopping the running console process"),
		stringOption([]string{"--release-base"}, "release_base", nil,
			"override the release channel URL (default: official GitHub releases; ROLLING_MEMORY_RELEASE_BASE also supported)"),
	)
	updateCmd := &command{
		Name: "update", Path: []string{"update"},
		Help:    "self-update the CLI binary, web console assets and host integrations (SHA-256 verified, no git required)",
		Options: updateOptions,
	}
	upgradeCmd := &command{
		Name: "upgrade", Path: []string{"upgrade"},
		Help:    "alias for `update`: self-update the CLI binary, web console assets and host integrations",
		Options: updateOptions,
	}

	root.Subcommands = []*command{
		initCmd, modeCmd, configCmd, recallCmd, recentCmd,
		importCmd, exportCmd, syncCmd, backgroundCmd, mcpCmd, hookCmd,
		statusCmd, doctorCmd, checkCmd, reindexCmd, installCmd, evalCmd,
		ejectCmd, efficacyCmd, waveCmd, panelCmd, updateCmd, upgradeCmd,
	}
	for _, sub := range root.Subcommands {
		sub.Path = []string{sub.Name}
	}
	parser.root = root
	return parser
}
