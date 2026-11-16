// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package harness

import (
	"fmt"
	"sort"
	"strings"

	"github.com/rocareer/rolling-memory/internal/ids"
)

// The hooks surface (zcode / claude-code): an event -> groups JSON map where
// each group carries a command hook. The marker for "ours" is a " hook "
// command that references the launcher.

type hookCommand struct {
	Event   string
	Command string
	Timeout int
}

func hookCommands(options InstallOptions) []hookCommand {
	binary := shlexQuote(options.ResolvedBinary())
	commands := make([]hookCommand, 0, len(HookEvents))
	for _, event := range HookEvents {
		commands = append(commands, hookCommand{
			Event:   event.Event,
			Command: fmt.Sprintf("%s hook %s --json", binary, event.Sub),
			Timeout: event.Timeout,
		})
	}
	return commands
}

func hookGroup(command string, timeout int, extras map[string]any) ids.OrderedMap {
	entry := ids.OrderedMap{
		{Key: "type", Value: "command"},
		{Key: "command", Value: command},
		{Key: "timeout", Value: timeout},
	}
	keys := make([]string, 0, len(extras))
	for key := range extras {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		entry = append(entry, ids.JSONPair{Key: key, Value: extras[key]})
	}
	return ids.OrderedMap{{Key: "hooks", Value: []any{entry}}}
}

func hookCommandOf(hook any) string {
	object, ok := asObject(hook)
	if !ok {
		return ""
	}
	value, found := objectGet(object, "command")
	if !found || !pyTruthy(value) {
		return ""
	}
	return pyString(value)
}

// pyTruthy mirrors the subset of standard truthiness the hook reader needs.
func pyTruthy(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case string:
		return typed != ""
	}
	if number, ok := numericValue(value); ok {
		return number != 0
	}
	return true
}

func groupHooks(group any) []any {
	object, ok := asObject(group)
	if !ok {
		return nil
	}
	value, found := objectGet(object, "hooks")
	if !found {
		return nil
	}
	hooks, _ := asArray(value)
	return hooks
}

func groupHasCommand(group any, command string) bool {
	for _, hook := range groupHooks(group) {
		if hookCommandOf(hook) == command {
			return true
		}
	}
	return false
}

func groupIsOurs(group any, options InstallOptions) bool {
	for _, hook := range groupHooks(group) {
		if hookIsOurs(hookCommandOf(hook), options) {
			return true
		}
	}
	return false
}

func installHooks(harness Harness, options InstallOptions, report *Report) {
	if harness.HooksKind == "" || harness.HooksPath == nil {
		report.Add(harness.ID, "hooks", "-", StatusSkipped,
			"no hook surface; agent-driven remember/recall only")
		return
	}
	path := *harness.HooksPath
	if plugin, ok := activeNativePlugin(harness, options); ok && plugin.HasHooks && !options.Force {
		if zcodeHasGlobalHooks(harness, options) {
			if options.DryRun {
				report.Add(harness.ID, "hooks", path, StatusPlanned, "dedupe global hooks (provided by plugin "+plugin.ID+")")
				return
			}
			tmpReport := newReport("uninstall", options)
			uninstallHooks(harness, options, tmpReport)
			report.Add(harness.ID, "hooks", path, StatusRemoved, "deduped; provided by plugin "+plugin.ID)
			return
		}
		report.Add(harness.ID, "hooks", path, StatusUnchanged, "1 event (provided by plugin "+plugin.ID+")")
		return
	}
	data, err := loadJSONFile(path)
	if err != nil {
		report.Add(harness.ID, "hooks", path, StatusError, "cannot parse: "+err.Error())
		return
	}
	events, data, ok := jsonTable(data, harness.HooksKeys, true)
	if !ok {
		report.Add(harness.ID, "hooks", path, StatusError, "cannot parse: missing hooks table")
		return
	}
	dirty := false
	problems := make([]string, 0)
	for _, command := range hookCommands(options) {
		groupsValue, found := objectGet(events, command.Event)
		var groups []any
		if !found || groupsValue == nil {
			groups = []any{}
			events = objectSet(events, command.Event, groups)
		} else {
			var isArray bool
			groups, isArray = asArray(groupsValue)
			if !isArray {
				problems = append(problems, command.Event+": not a list")
				continue
			}
		}
		already := false
		stale := false
		for _, group := range groups {
			if groupHasCommand(group, command.Command) {
				already = true
			}
			if groupIsOurs(group, options) {
				stale = true
			}
		}
		if already {
			continue
		}
		if stale && !options.Force {
			problems = append(problems, command.Event+": an entry with our name differs (use --force)")
			continue
		}
		kept := make([]any, 0, len(groups)+1)
		for _, group := range groups {
			if groupIsOurs(group, options) {
				continue
			}
			kept = append(kept, group)
		}
		kept = append(kept, hookGroup(command.Command, command.Timeout, harness.HooksExtras))
		events = objectSet(events, command.Event, kept)
		dirty = true
	}
	if len(problems) > 0 {
		report.Add(harness.ID, "hooks", path, StatusConflict, strings.Join(problems, "; "))
		return
	}
	if !dirty {
		report.Add(harness.ID, "hooks", path, StatusUnchanged, "1 event")
		return
	}
	if len(harness.HooksEnabledKeys) > 0 {
		keys := harness.HooksEnabledKeys
		last := keys[len(keys)-1]
		data, _ = jsonTableWith(data, keys[:len(keys)-1], true, func(section ids.OrderedMap) ids.OrderedMap {
			return objectSet(section, last, true)
		})
	}
	data, _ = jsonTableWith(data, harness.HooksKeys, true, func(ids.OrderedMap) ids.OrderedMap {
		return events
	})
	if options.DryRun {
		report.Add(harness.ID, "hooks", path, StatusPlanned, "1 event")
		return
	}
	text, err := dumpJSON(data)
	if err != nil {
		report.Add(harness.ID, "hooks", path, StatusError, "cannot encode: "+err.Error())
		return
	}
	if _, err := writeText(path, text); err != nil {
		report.Add(harness.ID, "hooks", path, StatusError, "cannot write: "+err.Error())
		return
	}
	report.Add(harness.ID, "hooks", path, StatusInstalled, "UserPromptSubmit")
}

func uninstallHooks(harness Harness, options InstallOptions, report *Report) {
	if harness.HooksKind == "" || harness.HooksPath == nil {
		report.Add(harness.ID, "hooks", "-", StatusSkipped, "no hook surface")
		return
	}
	path := *harness.HooksPath
	if !fileExists(path) {
		report.Add(harness.ID, "hooks", path, StatusSkipped, "config file not present")
		return
	}
	data, err := loadJSONFile(path)
	if err != nil {
		report.Add(harness.ID, "hooks", path, StatusError, "cannot parse: "+err.Error())
		return
	}
	events, _, ok := jsonTable(data, harness.HooksKeys, false)
	if !ok || len(events) == 0 {
		report.Add(harness.ID, "hooks", path, StatusSkipped, "no hooks section")
		return
	}
	removed := 0
	for _, event := range HookEvents {
		groupsValue, found := objectGet(events, event.Event)
		if !found {
			continue
		}
		groups, isArray := asArray(groupsValue)
		if !isArray {
			continue
		}
		kept := make([]any, 0, len(groups))
		for _, group := range groups {
			if groupIsOurs(group, options) {
				continue
			}
			kept = append(kept, group)
		}
		removed += len(groups) - len(kept)
		if len(kept) > 0 {
			events = objectSet(events, event.Event, kept)
		} else {
			events = objectDelete(events, event.Event)
		}
	}
	if removed == 0 {
		report.Add(harness.ID, "hooks", path, StatusSkipped, "no entries of ours")
		return
	}
	if options.DryRun {
		report.Add(harness.ID, "hooks", path, StatusPlanned, fmt.Sprintf("%d entries", removed))
		return
	}
	data, ok = jsonTableWith(data, harness.HooksKeys, false, func(ids.OrderedMap) ids.OrderedMap {
		return events
	})
	if !ok {
		report.Add(harness.ID, "hooks", path, StatusSkipped, "no hooks section")
		return
	}
	text, err := dumpJSON(data)
	if err != nil {
		report.Add(harness.ID, "hooks", path, StatusError, "cannot encode: "+err.Error())
		return
	}
	if _, err := writeText(path, text); err != nil {
		report.Add(harness.ID, "hooks", path, StatusError, "cannot write: "+err.Error())
		return
	}
	report.Add(harness.ID, "hooks", path, StatusRemoved, fmt.Sprintf("%d entries", removed))
}

func checkHooks(harness Harness, options InstallOptions, report *Report) {
	if harness.HooksKind == "" || harness.HooksPath == nil {
		report.Add(harness.ID, "hooks", "-", StatusSkipped, "not supported")
		return
	}
	// A check reports the wiring that is on disk, not whether --with-hooks was
	// passed to this check run.
	path := *harness.HooksPath
	if plugin, ok := activeNativePlugin(harness, options); ok && plugin.HasHooks {
		if zcodeHasGlobalHooks(harness, options) {
			report.Add(harness.ID, "hooks", path, StatusConflict,
				"duplicate hooks: both config.json and plugin "+plugin.ID+" active (run `rolling-memory install zcode --with-hooks` to deduplicate)")
			return
		}
		report.Add(harness.ID, "hooks", path, StatusOK, "1 event · plugin "+plugin.ID)
		return
	}
	if !fileExists(path) {
		report.Add(harness.ID, "hooks", path, StatusSkipped, "not wired")
		return
	}
	data, err := loadJSONFile(path)
	if err != nil {
		report.Add(harness.ID, "hooks", path, StatusError, "cannot parse: "+err.Error())
		return
	}
	events, _, _ := jsonTable(data, harness.HooksKeys, false)
	missing := make([]string, 0)
	for _, command := range hookCommands(options) {
		groupsValue, found := objectGet(events, command.Event)
		groups, _ := asArray(groupsValue)
		foundCommand := false
		if found {
			for _, group := range groups {
				if groupHasCommand(group, command.Command) {
					foundCommand = true
					break
				}
			}
		}
		if !foundCommand {
			missing = append(missing, command.Event)
		}
	}
	if len(missing) == 0 {
		report.Add(harness.ID, "hooks", path, StatusOK, "1 event")
		return
	}
	report.Add(harness.ID, "hooks", path, StatusSkipped, "not wired ("+strings.Join(missing, ", ")+")")
}
