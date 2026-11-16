// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package harness

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// zcodePluginInfo describes an enabled self-contained harness plugin for Rolling-Memory.
type zcodePluginInfo struct {
	ID       string
	Dir      string
	SkillMD  string
	HasSkill bool
	HasMCP   bool
	HasHooks bool
}

// activeNativePlugin checks whether the harness (zcode, claude-code or
// workbuddy) has an enabled native plugin (e.g. "rolling-memory@marketplace")
// in user scope whose cache directory provides the skill, MCP server and/or
// session hooks.
func activeNativePlugin(harness Harness, options InstallOptions) (zcodePluginInfo, bool) {
	if options.Project {
		return zcodePluginInfo{}, false
	}
	switch harness.ID {
	case "zcode":
		return activePluginFromConfig(
			harness.MCPPath,
			[]string{"plugins", "enabledPlugins"},
			filepath.Join(harness.Detect, "cli", "plugins", "cache"),
			".zcode-plugin",
			options,
		)
	case "claude-code":
		if harness.HooksPath == nil {
			return zcodePluginInfo{}, false
		}
		return activePluginFromConfig(
			*harness.HooksPath,
			[]string{"enabledPlugins"},
			filepath.Join(harness.Detect, "plugins", "cache"),
			".claude-plugin",
			options,
		)
	case "workbuddy":
		// WorkBuddy records enabled plugins in settings.json and materialises
		// them under plugins/cache/<marketplace>/<plugin>/<version> with a
		// .codebuddy-plugin manifest (the CLI host merges the plugin's own
		// .mcp.json for the MCP surface); directory marketplaces additionally
		// load in place from plugins/marketplaces/<name>.
		return activeWorkBuddyPlugin(harness, options)
	default:
		return zcodePluginInfo{}, false
	}
}

func activePluginFromConfig(configPath string, enabledPath []string, cacheRoot, manifestDir string, options InstallOptions) (zcodePluginInfo, bool) {
	if configPath == "" || !fileExists(configPath) {
		return zcodePluginInfo{}, false
	}
	data, err := loadJSONFile(configPath)
	if err != nil {
		return zcodePluginInfo{}, false
	}
	enabledVal, found := jsonLookup(data, enabledPath)
	if !found || enabledVal == nil {
		return zcodePluginInfo{}, false
	}
	enabledMap, ok := asObject(enabledVal)
	if !ok {
		return zcodePluginInfo{}, false
	}
	prefix := options.Name + "@"
	pluginID := ""
	market := ""
	for _, pair := range enabledMap {
		if pair.Key == options.Name || strings.HasPrefix(pair.Key, prefix) {
			if flag, isBool := pair.Value.(bool); isBool && flag {
				pluginID = pair.Key
				if idx := strings.IndexByte(pair.Key, '@'); idx >= 0 {
					market = pair.Key[idx+1:]
				}
				break
			}
		}
	}
	if pluginID == "" {
		return zcodePluginInfo{}, false
	}
	pluginDir := resolvePluginCacheDirFor(cacheRoot, market, options.Name, manifestDir)
	if pluginDir == "" {
		return zcodePluginInfo{}, false
	}
	return inspectPluginDir(pluginDir, manifestDir, pluginID, options)
}

// inspectPluginDir reports which components an enabled plugin directory
// provides (skill / MCP server / session hooks).
func inspectPluginDir(pluginDir, manifestDir, pluginID string, options InstallOptions) (zcodePluginInfo, bool) {
	manifest, err := loadJSONFile(filepath.Join(pluginDir, manifestDir, "plugin.json"))
	if err != nil {
		return zcodePluginInfo{}, false
	}
	skillMD := filepath.Join(pluginDir, "skills", options.SkillName, "SKILL.md")
	hasSkill := fileExists(skillMD)
	_, hasMCP := jsonLookup(manifest, []string{"mcpServers", options.Name})
	if !hasMCP {
		// CodeBuddy-family plugins may carry the MCP wiring in a sibling
		// .mcp.json instead of the manifest; the host runtimes read both, so
		// the dedup check must see both as well.
		if data, err := loadJSONFile(filepath.Join(pluginDir, ".mcp.json")); err == nil {
			_, hasMCP = jsonLookup(data, []string{"mcpServers", options.Name})
		}
	}
	hooksPath := filepath.Join(pluginDir, "hooks", "hooks.json")
	hasHooks := pluginHasHooks(hooksPath, pluginDir)
	if !hasSkill && !hasMCP && !hasHooks {
		return zcodePluginInfo{}, false
	}
	return zcodePluginInfo{
		ID:       pluginID,
		Dir:      pluginDir,
		SkillMD:  skillMD,
		HasSkill: hasSkill,
		HasMCP:   hasMCP,
		HasHooks: hasHooks,
	}, true
}

// activeWorkBuddyPlugin resolves the enabled native plugin for WorkBuddy. The
// host materialises plugins into plugins/cache/ on install, but directory
// marketplaces also load in place, so the marketplace directory is a fallback.
func activeWorkBuddyPlugin(harness Harness, options InstallOptions) (zcodePluginInfo, bool) {
	if info, ok := activePluginFromConfig(
		filepath.Join(harness.Detect, "settings.json"),
		[]string{"enabledPlugins"},
		filepath.Join(harness.Detect, "plugins", "cache"),
		".codebuddy-plugin",
		options,
	); ok {
		return info, true
	}
	return activePluginFromMarketplaceDir(harness.Detect, options)
}

// activePluginFromMarketplaceDir resolves an enabled plugin whose files live in
// the marketplace directory itself (plugins/marketplaces/<name>/<source>).
func activePluginFromMarketplaceDir(detect string, options InstallOptions) (zcodePluginInfo, bool) {
	settings, err := loadJSONFile(filepath.Join(detect, "settings.json"))
	if err != nil {
		return zcodePluginInfo{}, false
	}
	enabledValue, found := jsonLookup(settings, []string{"enabledPlugins"})
	if !found {
		return zcodePluginInfo{}, false
	}
	enabledPlugins, ok := asObject(enabledValue)
	if !ok {
		return zcodePluginInfo{}, false
	}
	marketplacesRoot := filepath.Join(detect, "plugins", "marketplaces")
	entries, err := os.ReadDir(marketplacesRoot)
	if err != nil {
		return zcodePluginInfo{}, false
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		marketDir := filepath.Join(marketplacesRoot, entry.Name())
		manifestPath := ""
		for _, candidate := range []string{
			filepath.Join(marketDir, ".codebuddy-plugin", "marketplace.json"),
			filepath.Join(marketDir, ".workbuddy-plugin", "marketplace.json"),
			filepath.Join(marketDir, "marketplace.json"),
		} {
			if fileExists(candidate) {
				manifestPath = candidate
				break
			}
		}
		if manifestPath == "" {
			continue
		}
		manifest, err := loadJSONFile(manifestPath)
		if err != nil {
			continue
		}
		marketNames := []string{entry.Name()}
		if nameValue, ok := jsonLookup(manifest, []string{"name"}); ok {
			if name, isString := nameValue.(string); isString && name != "" && name != entry.Name() {
				marketNames = append(marketNames, name)
			}
		}
		pluginsValue, found := jsonLookup(manifest, []string{"plugins"})
		if !found {
			continue
		}
		plugins, ok := asArray(pluginsValue)
		if !ok {
			continue
		}
		for _, pluginValue := range plugins {
			pluginObject, ok := asObject(pluginValue)
			if !ok {
				continue
			}
			nameValue, _ := objectGet(pluginObject, "name")
			if name, _ := nameValue.(string); name != options.Name {
				continue
			}
			pluginID := ""
			for _, marketName := range marketNames {
				key := options.Name + "@" + marketName
				if flag, ok := objectGet(enabledPlugins, key); ok {
					if enabled, isBool := flag.(bool); isBool && enabled {
						pluginID = key
						break
					}
				}
			}
			if pluginID == "" {
				continue
			}
			relative := ""
			sourceValue, _ := objectGet(pluginObject, "source")
			switch source := sourceValue.(type) {
			case string:
				relative = source
			default:
				if sourceObject, isObject := asObject(source); isObject {
					if pathValue, ok := objectGet(sourceObject, "path"); ok {
						relative, _ = pathValue.(string)
					}
				}
			}
			if relative == "" {
				continue
			}
			pluginDir := filepath.Join(marketDir, filepath.FromSlash(strings.TrimPrefix(relative, "./")))
			absDir, err := filepath.Abs(pluginDir)
			if err != nil {
				continue
			}
			baseDir, err := filepath.Abs(marketDir)
			if err != nil {
				continue
			}
			if absDir != baseDir && !strings.HasPrefix(absDir, baseDir+string(filepath.Separator)) {
				continue
			}
			return inspectPluginDir(absDir, ".codebuddy-plugin", pluginID, options)
		}
	}
	return zcodePluginInfo{}, false
}

// pluginHasHooks reports only events with an actual command hook, in either
// shipped form: the plain launcher call or its --json envelope variant. A
// hooks.json that is empty cannot replace global hooks.
func pluginHasHooks(path, pluginDir string) bool {
	data, err := loadJSONFile(path)
	if err != nil {
		return false
	}
	value, found := jsonLookup(data, []string{"hooks"})
	if !found {
		return false
	}
	events, ok := asObject(value)
	if !ok {
		return false
	}
	for _, event := range HookEvents {
		value, found := objectGet(events, event.Event)
		if !found {
			return false
		}
		groups, ok := asArray(value)
		if !ok {
			return false
		}
		foundCommand := false
		for _, group := range groups {
			for _, hook := range groupHooks(group) {
				command := strings.TrimSpace(hookCommandOf(hook))
				for _, root := range []string{"${CLAUDE_PLUGIN_ROOT}", "${ZCODE_PLUGIN_ROOT}"} {
					for _, launcher := range []string{"bin/rolling-memory-hook", "bin/rolling-memory-hook.cmd"} {
						base := root + "/" + launcher + " " + event.Sub
						if (command == base || command == base+" --json") && fileExists(filepath.Join(pluginDir, filepath.FromSlash(launcher))) {
							foundCommand = true
						}
					}
				}
			}
		}
		if !foundCommand {
			return false
		}
	}
	return len(HookEvents) > 0
}

func resolvePluginCacheDir(cacheRoot, market, pluginName string) string {
	return resolvePluginCacheDirFor(cacheRoot, market, pluginName, ".zcode-plugin")
}

func resolvePluginCacheDirFor(cacheRoot, market, pluginName, manifestDir string) string {
	var markets []string
	if market != "" {
		markets = []string{market}
	} else {
		entries, err := os.ReadDir(cacheRoot)
		if err != nil {
			return ""
		}
		for _, entry := range entries {
			if info, statErr := os.Stat(filepath.Join(cacheRoot, entry.Name())); statErr == nil && info.IsDir() {
				markets = append(markets, entry.Name())
			}
		}
		sort.Strings(markets)
	}
	for _, mkt := range markets {
		base := filepath.Join(cacheRoot, mkt, pluginName)
		entries, err := os.ReadDir(base)
		if err != nil {
			continue
		}
		versions := make([]string, 0, len(entries))
		for _, entry := range entries {
			if info, statErr := os.Stat(filepath.Join(base, entry.Name())); statErr == nil && info.IsDir() {
				versions = append(versions, entry.Name())
			}
		}
		if len(versions) == 0 {
			continue
		}
		sort.Strings(versions)
		for i := len(versions) - 1; i >= 0; i-- {
			candidate := filepath.Join(base, versions[i])
			if fileExists(filepath.Join(candidate, manifestDir, "plugin.json")) {
				return candidate
			}
		}
	}
	return ""
}

func zcodeHasGlobalHooks(harness Harness, options InstallOptions) bool {
	if harness.HooksPath == nil || !fileExists(*harness.HooksPath) {
		return false
	}
	data, err := loadJSONFile(*harness.HooksPath)
	if err != nil {
		return false
	}
	events, _, ok := jsonTable(data, harness.HooksKeys, false)
	if !ok {
		return false
	}
	for _, event := range HookEvents {
		groupsValue, found := objectGet(events, event.Event)
		if !found {
			continue
		}
		groups, _ := asArray(groupsValue)
		for _, group := range groups {
			if groupIsOurs(group, options) {
				return true
			}
		}
	}
	return false
}
