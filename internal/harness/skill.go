// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package harness

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	rollingmemory "github.com/rocareer/rolling-memory"
)

// The skill surface: install <skill_root>/<skill_name>/SKILL.md, replacing the
// first "name:" frontmatter line with the requested name.

func resolveDefaultSkillFile(options InstallOptions) string {
	base := resolveBaseSkillFile(options)
	lang, _ := NormalizeInstallLang(options.Lang)
	if lang != "" {
		rel := filepath.Join("references", "SKILL."+lang+".md")
		candidates := []string{
			filepath.Join(filepath.Dir(base), rel),
		}
		if options.Cwd != "" {
			candidates = append(candidates, filepath.Join(options.Cwd, "skills", "rolling-memory", rel))
		}
		if options.Home != "" {
			candidates = append(candidates,
				filepath.Join(options.Home, ".local", "share", "rolling-memory", "skills", "rolling-memory", rel),
				filepath.Join(options.Home, "Rolling-Memory", "skills", "rolling-memory", rel),
			)
		}
		for _, cand := range candidates {
			if fileExists(cand) {
				return cand
			}
		}
	}
	return base
}

func resolveBaseSkillFile(options InstallOptions) string {
	primary := DefaultSkillFile()
	if fileExists(primary) {
		return primary
	}
	candidates := []string{}
	if options.Cwd != "" {
		candidates = append(candidates, filepath.Join(options.Cwd, "skills", "rolling-memory", "SKILL.md"))
	}
	if options.Home != "" {
		candidates = append(candidates,
			filepath.Join(options.Home, ".local", "share", "rolling-memory", "skills", "rolling-memory", "SKILL.md"),
		)
		if pluginDir := resolvePluginCacheDir(filepath.Join(options.Home, ".zcode", "cli", "plugins", "cache"), "", DefaultName); pluginDir != "" {
			candidates = append(candidates, filepath.Join(pluginDir, "skills", "rolling-memory", "SKILL.md"))
		}
		candidates = append(candidates, filepath.Join(options.Home, "Rolling-Memory", "skills", "rolling-memory", "SKILL.md"))
	}
	for _, candidate := range candidates {
		if fileExists(candidate) {
			return candidate
		}
	}
	return primary
}

func skillText(options InstallOptions) (string, error) {
	source := options.SkillFile
	if source == nil {
		defaultFile := resolveDefaultSkillFile(options)
		source = &defaultFile
	}
	raw, err := os.ReadFile(*source)
	if errors.Is(err, os.ErrNotExist) && options.SkillFile == nil {
		path := "skills/rolling-memory/SKILL.md"
		if lang, _ := NormalizeInstallLang(options.Lang); lang != "" {
			path = "skills/rolling-memory/references/SKILL." + lang + ".md"
		}
		raw, err = rollingmemory.Skills.ReadFile(path)
	}
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf(
				"skill source not found: %s (run from the repo checkout or pass --skill-file)", *source)
		}
		return "", err
	}
	lines := pySplitLines(string(raw))
	for index, line := range lines {
		if strings.HasPrefix(line, "name:") {
			lines[index] = "name: " + options.SkillName
			break
		}
	}
	return pyStripRight(strings.Join(lines, "\n")) + "\n", nil
}

func skillTargets(harness Harness, options InstallOptions) []string {
	if options.Project {
		if harness.SkillProject != "" {
			return []string{filepath.Join(options.Cwd, filepath.FromSlash(harness.SkillProject), options.SkillName, "SKILL.md")}
		}
		return nil
	}
	var targets []string
	if harness.SkillUser != nil {
		targets = append(targets, filepath.Join(*harness.SkillUser, options.SkillName, "SKILL.md"))
	}
	if harness.SkillUserAlt != nil {
		targets = append(targets, filepath.Join(*harness.SkillUserAlt, options.SkillName, "SKILL.md"))
	}
	return targets
}

const skillOwnerMarker = ".rolling-memory-owner"

func skillDigest(raw []byte) string {
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]) + "\n"
}

// A marker is valid only for the exact installed bytes; edits revoke ownership.
func skillOwned(target string, raw []byte) bool {
	info, err := os.Lstat(target)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	marker := filepath.Join(filepath.Dir(target), skillOwnerMarker)
	info, err = os.Lstat(marker)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	owner, err := os.ReadFile(marker)
	return err == nil && string(owner) == skillDigest(raw)
}

func installSkill(harness Harness, options InstallOptions, report *Report) {
	targets := skillTargets(harness, options)
	if len(targets) == 0 {
		report.Add(harness.ID, "skill", "-", StatusSkipped, "no skill root for this scope")
		return
	}
	if plugin, ok := activeNativePlugin(harness, options); ok && plugin.HasSkill && !options.Force {
		target := targets[0]
		raw, err := os.ReadFile(target)
		if errors.Is(err, os.ErrNotExist) {
			if _, statErr := os.Lstat(target); statErr == nil {
				report.Add(harness.ID, "skill", target, StatusConflict, "skill path is not an owned regular file")
				return
			}
			report.Add(harness.ID, "skill", plugin.SkillMD, StatusUnchanged, "provided by plugin "+plugin.ID)
			return
		}
		if err != nil {
			report.Add(harness.ID, "skill", target, StatusError, "cannot read: "+err.Error())
			return
		}
		if !skillOwned(target, raw) {
			report.Add(harness.ID, "skill", target, StatusConflict, "duplicate skill is not owned; not touching it")
			return
		}
		if options.DryRun {
			report.Add(harness.ID, "skill", target, StatusPlanned, "dedupe user skill (provided by plugin "+plugin.ID+")")
			return
		}
		if err := os.Remove(target); err != nil {
			report.Add(harness.ID, "skill", target, StatusError, "cannot remove: "+err.Error())
			return
		}
		if err := os.Remove(filepath.Join(filepath.Dir(target), skillOwnerMarker)); err != nil {
			report.Add(harness.ID, "skill", target, StatusError, "cannot remove ownership marker: "+err.Error())
			return
		}
		if entries, err := os.ReadDir(filepath.Dir(target)); err == nil && len(entries) == 0 {
			_ = os.Remove(filepath.Dir(target))
		}
		report.Add(harness.ID, "skill", target, StatusRemoved, "deduped; provided by plugin "+plugin.ID)
		return
	}
	text, err := skillText(options)
	if err != nil {
		report.Add(harness.ID, "skill", targets[0], StatusError, err.Error())
		return
	}
	for _, target := range targets {
		marker := filepath.Join(filepath.Dir(target), skillOwnerMarker)
		current, readErr := os.ReadFile(target)
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			report.Add(harness.ID, "skill", target, StatusError, "cannot read: "+readErr.Error())
			continue
		}
		status := StatusInstalled
		if readErr == nil {
			if !skillOwned(target, current) {
				report.Add(harness.ID, "skill", target, StatusConflict, "skill is not owned or was modified; not touching it")
				continue
			}
			if string(current) == text {
				report.Add(harness.ID, "skill", target, StatusUnchanged, "")
				continue
			}
			status = StatusUpdated
		} else {
			if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
				report.Add(harness.ID, "skill", target, StatusConflict, "skill path already exists; not touching it")
				continue
			}
			if _, err := os.Lstat(marker); !errors.Is(err, os.ErrNotExist) {
				report.Add(harness.ID, "skill", target, StatusConflict, "ownership marker already exists; not touching it")
				continue
			}
		}
		if options.DryRun {
			report.Add(harness.ID, "skill", target, StatusPlanned, "")
			continue
		}
		if status == StatusInstalled {
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				report.Add(harness.ID, "skill", target, StatusError, "cannot create directory: "+err.Error())
				continue
			}
			file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666)
			if err != nil {
				report.Add(harness.ID, "skill", target, StatusError, "cannot create: "+err.Error())
				continue
			}
			_, writeErr := file.WriteString(text)
			closeErr := file.Close()
			if writeErr != nil || closeErr != nil {
				_ = os.Remove(target)
				report.Add(harness.ID, "skill", target, StatusError, fmt.Sprintf("cannot write: %v %v", writeErr, closeErr))
				continue
			}
			file, err = os.OpenFile(marker, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				_ = os.Remove(target)
				report.Add(harness.ID, "skill", target, StatusError, "cannot create ownership marker: "+err.Error())
				continue
			}
			_, writeErr = file.WriteString(skillDigest([]byte(text)))
			closeErr = file.Close()
			if writeErr != nil || closeErr != nil {
				_ = os.Remove(marker)
				_ = os.Remove(target)
				report.Add(harness.ID, "skill", target, StatusError, fmt.Sprintf("cannot write ownership marker: %v %v", writeErr, closeErr))
				continue
			}
		} else {
			if _, err := writeText(target, text); err != nil {
				report.Add(harness.ID, "skill", target, StatusError, "cannot write: "+err.Error())
				continue
			}
			if err := os.WriteFile(marker, []byte(skillDigest([]byte(text))), 0o600); err != nil {
				report.Add(harness.ID, "skill", target, StatusError, "cannot update ownership marker: "+err.Error())
				continue
			}
		}
		report.Add(harness.ID, "skill", target, status, "")
	}
}

func uninstallSkill(harness Harness, options InstallOptions, report *Report) {
	for _, target := range skillTargets(harness, options) {
		raw, err := os.ReadFile(target)
		if errors.Is(err, os.ErrNotExist) {
			if _, statErr := os.Lstat(target); statErr == nil {
				report.Add(harness.ID, "skill", target, StatusConflict, "skill path is not an owned regular file")
			} else {
				report.Add(harness.ID, "skill", target, StatusSkipped, "not present")
			}
			continue
		}
		if err != nil {
			report.Add(harness.ID, "skill", target, StatusError, "cannot read: "+err.Error())
			continue
		}
		if !skillOwned(target, raw) {
			report.Add(harness.ID, "skill", target, StatusConflict, "skill is not owned or was modified; not touching it")
			continue
		}
		if options.DryRun {
			report.Add(harness.ID, "skill", target, StatusPlanned, "")
			continue
		}
		if err := os.Remove(target); err != nil {
			report.Add(harness.ID, "skill", target, StatusError, "cannot remove: "+err.Error())
			continue
		}
		if err := os.Remove(filepath.Join(filepath.Dir(target), skillOwnerMarker)); err != nil {
			report.Add(harness.ID, "skill", target, StatusError, "cannot remove ownership marker: "+err.Error())
			continue
		}
		if entries, err := os.ReadDir(filepath.Dir(target)); err == nil && len(entries) == 0 {
			_ = os.Remove(filepath.Dir(target))
		}
		report.Add(harness.ID, "skill", target, StatusRemoved, "")
	}
}

func checkSkill(harness Harness, options InstallOptions, report *Report) {
	targets := skillTargets(harness, options)
	if len(targets) == 0 {
		report.Add(harness.ID, "skill", "-", StatusSkipped, "no skill root for this scope")
		return
	}
	if plugin, ok := activeNativePlugin(harness, options); ok && plugin.HasSkill {
		target := targets[0]
		if fileExists(target) {
			report.Add(harness.ID, "skill", target, StatusConflict,
				"duplicate skill: both user skill and plugin "+plugin.ID+" present; unowned files require manual review")
			return
		}
		report.Add(harness.ID, "skill", plugin.SkillMD, StatusOK, "plugin "+plugin.ID)
		return
	}
	for _, target := range targets {
		if !fileExists(target) {
			report.Add(harness.ID, "skill", target, StatusMissing, "")
			continue
		}
		raw, err := os.ReadFile(target)
		if err != nil {
			report.Add(harness.ID, "skill", target, StatusError, "cannot read: "+err.Error())
			continue
		}
		if !skillOwned(target, raw) {
			report.Add(harness.ID, "skill", target, StatusConflict, "skill is not owned or was modified")
			continue
		}
		lines := pySplitLines(string(raw))
		if len(lines) > 8 {
			lines = lines[:8]
		}
		head := strings.Join(lines, "\n")
		if strings.Contains(head, "name: "+options.SkillName) && strings.Contains(head, "description:") {
			report.Add(harness.ID, "skill", target, StatusOK, "")
		} else {
			report.Add(harness.ID, "skill", target, StatusConflict, "frontmatter missing name/description")
		}
	}
}
