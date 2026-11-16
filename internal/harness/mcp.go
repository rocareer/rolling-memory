// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package harness

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/rocareer/rolling-memory/internal/ids"
)

// The MCP surface has three writers: a JSON deep merge (zcode / claude-code /
// codebuddy), a TOML section replacement (codex) and a marker block inside the
// dsh profile patch.

func mcpEnv(options InstallOptions) ids.OrderedMap {
	env := make(ids.OrderedMap, 0, 2)
	if options.DB != "" {
		env = append(env, ids.JSONPair{Key: "ROLLING_MEMORY_DB", Value: options.DB})
	}
	if options.Scope != "" {
		env = append(env, ids.JSONPair{Key: "ROLLING_MEMORY_SCOPE", Value: options.Scope})
	}
	return env
}

// mcpEntry is the JSON shape written into zcode / claude-code / codebuddy.
func mcpEntry(options InstallOptions) ids.OrderedMap {
	entry := ids.OrderedMap{
		{Key: "type", Value: "stdio"},
		{Key: "command", Value: options.ResolvedBinary()},
		{Key: "args", Value: []any{"mcp"}},
	}
	if env := mcpEnv(options); len(env) > 0 {
		entry = append(entry, ids.JSONPair{Key: "env", Value: env})
	}
	return entry
}

func tomlBlock(options InstallOptions) (string, error) {
	command, err := pyJSONString(options.ResolvedBinary())
	if err != nil {
		return "", err
	}
	lines := []string{
		"[mcp_servers." + options.Name + "]",
		"command = " + command,
		`args = ["mcp"]`,
		"startup_timeout_sec = 30",
	}
	if env := mcpEnv(options); len(env) > 0 {
		lines = append(lines, "", "[mcp_servers."+options.Name+".env]")
		for _, pair := range env {
			value, err := pyJSONString(pyString(pair.Value))
			if err != nil {
				return "", err
			}
			lines = append(lines, pair.Key+" = "+value)
		}
	}
	return strings.Join(lines, "\n") + "\n", nil
}

func dshMarkers(name string) (string, string) {
	return fmt.Sprintf("# >>> rolling-memory (%s) >>>", name),
		fmt.Sprintf("# <<< rolling-memory (%s) <<<", name)
}

func dshBlock(options InstallOptions) (string, error) {
	begin, end := dshMarkers(options.Name)
	command, err := pyJSONString(options.ResolvedBinary())
	if err != nil {
		return "", err
	}
	lines := []string{
		begin,
		"- insert:",
		"    - id: " + options.Name + "-mcp",
		"      name: '@deepseek-ai/dsh-mcp-client'",
		"      config:",
		"        serverName: " + options.Name,
		"        transport: stdio",
		"        command: " + command,
		"        args: ['mcp']",
		"        failOnStartupError: false",
		end,
	}
	return strings.Join(lines, "\n") + "\n", nil
}

// isOurBinary reports whether value is the resolved launcher, the bare command
// name, or a launcher shim path for this platform.
func isOurBinary(value string, options InstallOptions) bool {
	resolved := options.ResolvedBinary()
	return value == resolved || value == "rolling-memory" || value == "rmem" || hasShimSuffix(value)
}

func hasShimSuffix(value string) bool {
	return hasShimSuffixFor(value, runtime.GOOS)
}

func hasShimSuffixFor(value, goos string) bool {
	name := shimNameFor(goos)
	return strings.HasSuffix(value, "/"+name) || strings.HasSuffix(value, "\\"+name)
}

func entryIsOurs(entry any, options InstallOptions) bool {
	object, ok := asObject(entry)
	if !ok {
		return false
	}
	argsValue, _ := objectGet(object, "args")
	args, _ := asArray(argsValue)
	hasMCP := false
	for _, arg := range args {
		if pyString(arg) == "mcp" {
			hasMCP = true
			break
		}
	}
	if !hasMCP {
		return false
	}
	command, _ := objectGet(object, "command")
	return isOurBinary(pyString(command), options)
}

func entryCommand(entry any) string {
	if object, ok := asObject(entry); ok {
		if command, found := objectGet(object, "command"); found {
			return pyString(command)
		}
	}
	return "None"
}

func hookIsOurs(command string, options InstallOptions) bool {
	if !strings.Contains(command, " hook ") {
		return false
	}
	return strings.Contains(command, options.ResolvedBinary()) ||
		strings.Contains(command, "rolling_memory") ||
		strings.Contains(command, "rolling-memory") ||
		strings.HasPrefix(command, "rmem ")
}

// regionIsOurs: ours only when a binary/launcher marker is present -- the name
// alone is not proof.
func regionIsOurs(text string, options InstallOptions) bool {
	return strings.Contains(text, options.ResolvedBinary()) ||
		strings.Contains(text, "rolling_memory") ||
		strings.Contains(text, ShimName) ||
		strings.Contains(text, windowsShimName)
}

func mcpTargetPath(harness Harness, options InstallOptions) (string, bool) {
	if harness.MCPKind == "none" || harness.MCPKind == "" {
		return "", false
	}
	if harness.MCPKind == "dsh" {
		if options.Project || harness.ProfilesDir == nil {
			return "", false
		}
		return filepath.Join(*harness.ProfilesDir, options.DSHProfile, "cordis.patch.yml"), true
	}
	if options.Project {
		if harness.MCPProjectPath == nil {
			return "", false
		}
		return filepath.Join(options.Cwd, filepath.FromSlash(*harness.MCPProjectPath)), true
	}
	return harness.MCPPath, true
}

// mcpTargetPaths returns every path install/uninstall/check must cover for a
// harness: the primary target plus the legacy path when the host's read point is
// not fully settled. WorkBuddy is the one such harness: its connectors document
// ~/.workbuddy/mcp.json as the user config while earlier host builds were
// observed reading the dotted ~/.workbuddy/.mcp.json. Every writer here merges
// into the existing table and removes only entries it recognises as its own, so
// whatever the host already keeps in either file survives untouched.
func mcpTargetPaths(harness Harness, options InstallOptions) []string {
	primary, ok := mcpTargetPath(harness, options)
	if !ok {
		return nil
	}
	paths := []string{primary}
	if harness.MCPLegacyPath != "" && harness.MCPLegacyPath != primary && !options.Project {
		paths = append(paths, harness.MCPLegacyPath)
	}
	return paths
}

// mcpSkipNote names the truthful reason a harness has no writable MCP target.
func mcpSkipNote(harness Harness) string {
	if harness.MCPSkipReason != "" {
		return harness.MCPSkipReason
	}
	return "no MCP target for this harness/scope"
}

func installMCP(harness Harness, options InstallOptions, report *Report) {
	paths := mcpTargetPaths(harness, options)
	if len(paths) == 0 {
		report.Add(harness.ID, "mcp", "-", StatusSkipped, mcpSkipNote(harness))
		return
	}
	for _, path := range paths {
		switch harness.MCPKind {
		case "json":
			installMCPJSON(harness, options, path, report)
		case "toml":
			installMCPToml(harness, options, path, report)
		case "dsh":
			installMCPDsh(harness, options, path, report)
		default:
			report.Add(harness.ID, "mcp", path, StatusError, "unknown mcp kind "+pyRepr(harness.MCPKind))
		}
	}
}

func uninstallMCP(harness Harness, options InstallOptions, report *Report) {
	paths := mcpTargetPaths(harness, options)
	if len(paths) == 0 {
		report.Add(harness.ID, "mcp", "-", StatusSkipped, mcpSkipNote(harness))
		return
	}
	for _, path := range paths {
		if !fileExists(path) {
			// A secondary target the user never had is not a missing piece of
			// the installation; report on the primary one only.
			if path == paths[0] {
				report.Add(harness.ID, "mcp", path, StatusSkipped, "config file not present")
			}
			continue
		}
		switch harness.MCPKind {
		case "json":
			uninstallMCPJSON(harness, options, path, report)
		case "toml":
			uninstallMCPToml(harness, options, path, report)
		default:
			uninstallMCPDsh(harness, options, path, report)
		}
	}
}

func checkMCP(harness Harness, options InstallOptions, report *Report) {
	paths := mcpTargetPaths(harness, options)
	if len(paths) == 0 {
		report.Add(harness.ID, "mcp", "-", StatusSkipped, mcpSkipNote(harness))
		return
	}
	plugin, hasPluginMCP := activeNativePlugin(harness, options)
	hasPluginMCP = hasPluginMCP && plugin.HasMCP
	for _, path := range paths {
		if !fileExists(path) {
			if hasPluginMCP {
				if path == paths[0] {
					report.Add(harness.ID, "mcp", path, StatusOK, "plugin "+plugin.ID)
				}
				continue
			}
			if path == paths[0] {
				report.Add(harness.ID, "mcp", path, StatusMissing, "config file not present")
			}
			continue
		}
		switch harness.MCPKind {
		case "json":
			checkMCPJSON(harness, options, path, report)
		case "toml":
			checkMCPToml(harness, options, path, report)
		default:
			checkMCPDsh(harness, options, path, report)
		}
	}
}

func installMCPJSON(harness Harness, options InstallOptions, path string, report *Report) {
	if plugin, ok := activeNativePlugin(harness, options); ok && plugin.HasMCP && !options.Force {
		if fileExists(path) {
			if data, err := loadJSONFile(path); err == nil {
				keys := append(append([]string{}, harness.MCPKeys...), options.Name)
				if existing, found := jsonLookup(data, keys); found && existing != nil && entryIsOurs(existing, options) {
					if options.DryRun {
						report.Add(harness.ID, "mcp", path, StatusPlanned, "dedupe global MCP (provided by plugin "+plugin.ID+")")
						return
					}
					updated, ok := jsonTableWith(data, harness.MCPKeys, false, func(current ids.OrderedMap) ids.OrderedMap {
						return objectDelete(current, options.Name)
					})
					if ok {
						if text, err := dumpJSON(updated); err == nil {
							_, _ = writeText(path, text)
						}
					}
					report.Add(harness.ID, "mcp", path, StatusRemoved, "deduped; provided by plugin "+plugin.ID)
					return
				}
			}
		}
		report.Add(harness.ID, "mcp", path, StatusUnchanged, "provided by plugin "+plugin.ID)
		return
	}
	entry := mcpEntry(options)
	data, err := loadJSONFile(path)
	if err != nil {
		report.Add(harness.ID, "mcp", path, StatusError, "cannot parse: "+err.Error())
		return
	}
	table, _, ok := jsonTable(data, harness.MCPKeys, true)
	if !ok {
		report.Add(harness.ID, "mcp", path, StatusError, "cannot parse: missing JSON table")
		return
	}
	existing, found := objectGet(table, options.Name)
	present := found && existing != nil
	if present && jsonEqual(existing, entry) {
		report.Add(harness.ID, "mcp", path, StatusUnchanged, "")
		return
	}
	if present && !entryIsOurs(existing, options) && !options.Force {
		report.Add(harness.ID, "mcp", path, StatusConflict,
			"'"+options.Name+"' exists and is not ours; use --force")
		return
	}
	if options.DryRun {
		detail := ""
		if preview, err := dumpJSON(ids.OrderedMap{{Key: options.Name, Value: entry}}); err == nil {
			detail = oneLineJSON(preview)
		}
		report.Add(harness.ID, "mcp", path, StatusPlanned, detail)
		return
	}
	updated, _ := jsonTableWith(data, harness.MCPKeys, true, func(current ids.OrderedMap) ids.OrderedMap {
		return objectSet(current, options.Name, entry)
	})
	text, err := dumpJSON(updated)
	if err != nil {
		report.Add(harness.ID, "mcp", path, StatusError, "cannot encode: "+err.Error())
		return
	}
	if _, err := writeText(path, text); err != nil {
		report.Add(harness.ID, "mcp", path, StatusError, "cannot write: "+err.Error())
		return
	}
	status := StatusInstalled
	if present {
		status = StatusUpdated
	}
	report.Add(harness.ID, "mcp", path, status, "")
}

func uninstallMCPJSON(harness Harness, options InstallOptions, path string, report *Report) {
	data, err := loadJSONFile(path)
	if err != nil {
		report.Add(harness.ID, "mcp", path, StatusError, "cannot parse: "+err.Error())
		return
	}
	table, _, ok := jsonTable(data, harness.MCPKeys, false)
	if !ok {
		report.Add(harness.ID, "mcp", path, StatusSkipped, "entry not present")
		return
	}
	existing, found := objectGet(table, options.Name)
	if !found || existing == nil {
		report.Add(harness.ID, "mcp", path, StatusSkipped, "entry not present")
		return
	}
	if !entryIsOurs(existing, options) {
		report.Add(harness.ID, "mcp", path, StatusConflict, "entry is not ours; not touching it")
		return
	}
	if options.DryRun {
		report.Add(harness.ID, "mcp", path, StatusPlanned, "")
		return
	}
	updated, ok := jsonTableWith(data, harness.MCPKeys, false, func(current ids.OrderedMap) ids.OrderedMap {
		return objectDelete(current, options.Name)
	})
	if !ok {
		report.Add(harness.ID, "mcp", path, StatusSkipped, "entry not present")
		return
	}
	text, err := dumpJSON(updated)
	if err != nil {
		report.Add(harness.ID, "mcp", path, StatusError, "cannot encode: "+err.Error())
		return
	}
	if _, err := writeText(path, text); err != nil {
		report.Add(harness.ID, "mcp", path, StatusError, "cannot write: "+err.Error())
		return
	}
	report.Add(harness.ID, "mcp", path, StatusRemoved, "")
}

func checkMCPJSON(harness Harness, options InstallOptions, path string, report *Report) {
	data, err := loadJSONFile(path)
	if err != nil {
		report.Add(harness.ID, "mcp", path, StatusError, "cannot parse: "+err.Error())
		return
	}
	keys := append(append([]string{}, harness.MCPKeys...), options.Name)
	entry, found := jsonLookup(data, keys)
	if plugin, ok := activeNativePlugin(harness, options); ok && plugin.HasMCP {
		if found && entry != nil {
			report.Add(harness.ID, "mcp", path, StatusConflict,
				"duplicate MCP: both "+filepath.Base(path)+" and plugin "+plugin.ID+" active (run `rolling-memory install "+harness.ID+"` to deduplicate)")
			return
		}
		report.Add(harness.ID, "mcp", path, StatusOK, "plugin "+plugin.ID)
		return
	}
	if !found || entry == nil {
		report.Add(harness.ID, "mcp", path, StatusMissing,
			strings.Join(append(append([]string{}, harness.MCPKeys...), options.Name), "."))
		return
	}
	if entryIsOurs(entry, options) {
		report.Add(harness.ID, "mcp", path, StatusOK, entryCommand(entry))
		return
	}
	report.Add(harness.ID, "mcp", path, StatusConflict, "same-named entry is not ours")
}

var tomlHeaderRE = regexp.MustCompile(`^\[([^\]]+)\]\s*$`)

// tomlRegionBounds returns the bounds of the [table] block plus any [table.*]
// subtables.
func tomlRegionBounds(lines []string, table string) (int, int, bool) {
	first := -1
	for index, raw := range lines {
		match := tomlHeaderRE.FindStringSubmatch(strings.TrimSpace(raw))
		if match == nil {
			continue
		}
		key := strings.TrimSpace(match[1])
		if key == table || strings.HasPrefix(key, table+".") {
			if first < 0 {
				first = index
			}
		} else if first >= 0 {
			return first, index, true
		}
	}
	if first < 0 {
		return 0, 0, false
	}
	return first, len(lines), true
}

func tomlTableName(harness Harness, options InstallOptions) string {
	if len(harness.MCPKeys) > 0 {
		return harness.MCPKeys[0] + "." + options.Name
	}
	return options.Name
}

func installMCPToml(harness Harness, options InstallOptions, path string, report *Report) {
	desired, err := tomlBlock(options)
	if err != nil {
		report.Add(harness.ID, "mcp", path, StatusError, "cannot encode: "+err.Error())
		return
	}
	table := tomlTableName(harness, options)
	original := ""
	if fileExists(path) {
		raw, err := os.ReadFile(path)
		if err != nil {
			report.Add(harness.ID, "mcp", path, StatusError, "cannot read: "+err.Error())
			return
		}
		original = string(raw)
	}
	lines := pySplitLines(original)
	status := StatusInstalled
	var newLines []string
	if start, end, ok := tomlRegionBounds(lines, table); ok {
		current := strings.Join(lines[start:end], "\n")
		if strings.TrimSpace(current) == strings.TrimSpace(desired) {
			report.Add(harness.ID, "mcp", path, StatusUnchanged, "")
			return
		}
		if !regionIsOurs(current, options) && !options.Force {
			report.Add(harness.ID, "mcp", path, StatusConflict,
				"["+table+"] exists and is not ours; use --force")
			return
		}
		if options.DryRun {
			report.Add(harness.ID, "mcp", path, StatusPlanned, tomlPreview(desired))
			return
		}
		newLines = append(append(append([]string{}, lines[:start]...), pySplitLines(desired)...), lines[end:]...)
		status = StatusUpdated
	} else {
		if options.DryRun {
			report.Add(harness.ID, "mcp", path, StatusPlanned, tomlPreview(desired))
			return
		}
		newLines = append([]string{}, lines...)
		for len(newLines) > 0 && strings.TrimSpace(newLines[len(newLines)-1]) == "" {
			newLines = newLines[:len(newLines)-1]
		}
		if len(newLines) > 0 {
			newLines = append(newLines, "")
		}
		newLines = append(newLines, pySplitLines(desired)...)
	}
	text := pyStripRight(strings.Join(newLines, "\n")) + "\n"
	if err := validateTOML(text); err != nil {
		report.Add(harness.ID, "mcp", path, StatusError, "refusing to write invalid TOML: "+err.Error())
		return
	}
	if _, err := writeText(path, text); err != nil {
		report.Add(harness.ID, "mcp", path, StatusError, "cannot write: "+err.Error())
		return
	}
	report.Add(harness.ID, "mcp", path, status, "")
}

func tomlPreview(desired string) string {
	return strings.ReplaceAll(strings.TrimSpace(desired), "\n", " | ")
}

func uninstallMCPToml(harness Harness, options InstallOptions, path string, report *Report) {
	table := tomlTableName(harness, options)
	raw, err := os.ReadFile(path)
	if err != nil {
		report.Add(harness.ID, "mcp", path, StatusError, "cannot read: "+err.Error())
		return
	}
	lines := pySplitLines(string(raw))
	start, end, ok := tomlRegionBounds(lines, table)
	if !ok {
		report.Add(harness.ID, "mcp", path, StatusSkipped, "entry not present")
		return
	}
	current := strings.Join(lines[start:end], "\n")
	if !regionIsOurs(current, options) {
		report.Add(harness.ID, "mcp", path, StatusConflict, "region is not ours; not touching it")
		return
	}
	if options.DryRun {
		report.Add(harness.ID, "mcp", path, StatusPlanned, "")
		return
	}
	newLines := append(append([]string{}, lines[:start]...), lines[end:]...)
	for len(newLines) > 0 && strings.TrimSpace(newLines[len(newLines)-1]) == "" {
		newLines = newLines[:len(newLines)-1]
	}
	text := ""
	if len(newLines) > 0 {
		text = strings.Join(newLines, "\n") + "\n"
	}
	if err := validateTOML(text); err != nil {
		report.Add(harness.ID, "mcp", path, StatusError, "refusing to write invalid TOML: "+err.Error())
		return
	}
	if _, err := writeText(path, text); err != nil {
		report.Add(harness.ID, "mcp", path, StatusError, "cannot write: "+err.Error())
		return
	}
	report.Add(harness.ID, "mcp", path, StatusRemoved, "")
}

func checkMCPToml(harness Harness, options InstallOptions, path string, report *Report) {
	raw, err := os.ReadFile(path)
	if err != nil {
		report.Add(harness.ID, "mcp", path, StatusError, "cannot parse: "+err.Error())
		return
	}
	data, err := parseTOML(string(raw))
	if err != nil {
		report.Add(harness.ID, "mcp", path, StatusError, "cannot parse: "+err.Error())
		return
	}
	var table any
	if len(harness.MCPKeys) > 0 {
		table = data[harness.MCPKeys[0]]
	}
	tableObject, _ := table.(map[string]any)
	entry, found := tableObject[options.Name]
	if !found || entry == nil {
		report.Add(harness.ID, "mcp", path, StatusMissing,
			tomlTableName(harness, options))
		return
	}
	if tomlEntryIsOurs(entry, options) {
		report.Add(harness.ID, "mcp", path, StatusOK, entryCommand(entry))
		return
	}
	report.Add(harness.ID, "mcp", path, StatusConflict, "same-named entry is not ours")
}

// tomlEntryIsOurs mirrors `{**entry, "args": entry.get("args") or ["mcp"]}`.
func tomlEntryIsOurs(entry any, options InstallOptions) bool {
	object, ok := entry.(map[string]any)
	if !ok {
		return false
	}
	args, ok := asArray(object["args"])
	if !ok || len(args) == 0 {
		args = []any{"mcp"}
	}
	copied := make(map[string]any, len(object)+1)
	for key, value := range object {
		copied[key] = value
	}
	copied["args"] = args
	return entryIsOurs(copied, options)
}

func installMCPDsh(harness Harness, options InstallOptions, path string, report *Report) {
	if !fileExists(filepath.Dir(path)) {
		report.Add(harness.ID, "mcp", path, StatusError, "dsh profile dir not found: "+filepath.Dir(path))
		return
	}
	detail := ""
	if options.DB != "" || options.Scope != "" {
		detail = "dsh entry cannot carry env; set core.db/core.scope with `rolling-memory config set`"
	}
	begin, end := dshMarkers(options.Name)
	block, err := dshBlock(options)
	if err != nil {
		report.Add(harness.ID, "mcp", path, StatusError, "cannot encode: "+err.Error())
		return
	}
	original := ""
	if fileExists(path) {
		raw, err := os.ReadFile(path)
		if err != nil {
			report.Add(harness.ID, "mcp", path, StatusError, "cannot read: "+err.Error())
			return
		}
		original = string(raw)
	}
	if strings.Contains(original, begin) && strings.Contains(original, end) {
		regionStart := strings.Index(original, begin)
		regionEnd := strings.Index(original, end) + len(end)
		current := original[regionStart:regionEnd]
		if strings.TrimSpace(current) == strings.TrimSpace(block) {
			report.Add(harness.ID, "mcp", path, StatusUnchanged, detail)
			return
		}
		if !regionIsOurs(current, options) && !options.Force {
			report.Add(harness.ID, "mcp", path, StatusConflict, "marker block is not ours; use --force")
			return
		}
		if options.DryRun {
			report.Add(harness.ID, "mcp", path, StatusPlanned, detail)
			return
		}
		text := original[:regionStart] + strings.TrimRight(block, "\n") + original[regionEnd:]
		if !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		if _, err := writeText(path, text); err != nil {
			report.Add(harness.ID, "mcp", path, StatusError, "cannot write: "+err.Error())
			return
		}
		report.Add(harness.ID, "mcp", path, StatusUpdated, detail)
		return
	}
	if options.DryRun {
		report.Add(harness.ID, "mcp", path, StatusPlanned, detail)
		return
	}
	text := original
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	if strings.TrimSpace(text) != "" {
		text += "\n"
	}
	text += block
	if _, err := writeText(path, text); err != nil {
		report.Add(harness.ID, "mcp", path, StatusError, "cannot write: "+err.Error())
		return
	}
	report.Add(harness.ID, "mcp", path, StatusInstalled, detail)
}

func uninstallMCPDsh(harness Harness, options InstallOptions, path string, report *Report) {
	begin, end := dshMarkers(options.Name)
	raw, err := os.ReadFile(path)
	if err != nil {
		report.Add(harness.ID, "mcp", path, StatusError, "cannot read: "+err.Error())
		return
	}
	original := string(raw)
	if !strings.Contains(original, begin) || !strings.Contains(original, end) {
		report.Add(harness.ID, "mcp", path, StatusSkipped, "marker block not present")
		return
	}
	if options.DryRun {
		report.Add(harness.ID, "mcp", path, StatusPlanned, "")
		return
	}
	regionStart := strings.Index(original, begin)
	regionEnd := strings.Index(original, end) + len(end)
	text := strings.TrimRight(original[:regionStart], "\n")
	tail := strings.TrimLeft(original[regionEnd:], "\n")
	combined := ""
	if tail != "" {
		combined = text + "\n\n" + tail
	} else if text != "" {
		combined = text + "\n"
	}
	if _, err := writeText(path, combined); err != nil {
		report.Add(harness.ID, "mcp", path, StatusError, "cannot write: "+err.Error())
		return
	}
	report.Add(harness.ID, "mcp", path, StatusRemoved, "")
}

func checkMCPDsh(harness Harness, options InstallOptions, path string, report *Report) {
	raw, err := os.ReadFile(path)
	if err != nil {
		report.Add(harness.ID, "mcp", path, StatusError, "cannot parse: "+err.Error())
		return
	}
	text := string(raw)
	begin, end := dshMarkers(options.Name)
	if !strings.Contains(text, begin) || !strings.Contains(text, end) {
		report.Add(harness.ID, "mcp", path, StatusMissing, "no '"+options.Name+"' block")
		return
	}
	if strings.Contains(text, "serverName: "+options.Name) {
		report.Add(harness.ID, "mcp", path, StatusOK, "")
		return
	}
	report.Add(harness.ID, "mcp", path, StatusConflict, "marker block present but malformed")
}

func parseTOML(raw string) (map[string]any, error) {
	var data map[string]any
	if err := toml.Unmarshal([]byte(raw), &data); err != nil {
		return nil, err
	}
	return data, nil
}

func validateTOML(raw string) error {
	var data map[string]any
	return toml.Unmarshal([]byte(raw), &data)
}
