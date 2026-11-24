// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package harness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSavedLang(t *testing.T, home, lang, adminLang string) {
	t.Helper()
	directory := filepath.Join(home, ".config", "rolling-memory")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", directory, err)
	}
	payload := `{"lang":"` + lang + `","default_lang":"` + adminLang + `"}`
	if err := os.WriteFile(filepath.Join(directory, "ui-lang.json"), []byte(payload), 0o644); err != nil {
		t.Fatalf("write ui-lang.json: %v", err)
	}
}

func installZCodeForLang(t *testing.T, home string) *Report {
	t.Helper()
	registry := BuildHarnesses(home, "")
	options := InstallOptions{
		Home:      home,
		Cwd:       home,
		Binary:    "rolling-memory",
		SkillName: "rolling-memory",
	}
	return Install(registry[:1], options)
}

func installedSkillText(t *testing.T, home string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(home, ".zcode", "skills", "rolling-memory", "SKILL.md"))
	if err != nil {
		t.Fatalf("read installed skill: %v", err)
	}
	return string(raw)
}

// TestInstallResumesSavedLanguage pins the fix for the silent language flip: a
// bare install (no --lang) on a machine whose saved preference is zh-CN must
// keep installing the zh-CN skill.
func TestInstallResumesSavedLanguage(t *testing.T) {
	home := t.TempDir()
	writeSavedLang(t, home, "zh-CN", "zh-cn")

	report := installZCodeForLang(t, home)
	if failures := report.Failures(); len(failures) != 0 {
		t.Fatalf("unexpected failures: %#v", failures)
	}
	text := installedSkillText(t, home)
	if !strings.Contains(text, "# Rolling Memory 协作") || strings.Contains(text, "# Working with Rolling Memory") {
		t.Fatalf("installed skill is not the zh-CN variant:\n%s", text[:120])
	}
	if got := report.Extra["default_lang"]; got != "zh-cn" {
		t.Fatalf("default_lang = %v, want zh-cn", got)
	}
}

// TestInstallDefaultsToEnglishOnFreshMachine keeps the documented first-run
// default: no flag and no saved preference installs the English variant.
func TestInstallDefaultsToEnglishOnFreshMachine(t *testing.T) {
	home := t.TempDir()

	report := installZCodeForLang(t, home)
	if failures := report.Failures(); len(failures) != 0 {
		t.Fatalf("unexpected failures: %#v", failures)
	}
	text := installedSkillText(t, home)
	if !strings.Contains(text, "# Working with Rolling Memory") {
		t.Fatalf("fresh install is not the English variant:\n%s", text[:120])
	}
}

// TestInstallExplicitLangOverridesSavedPreference keeps the explicit flag
// authoritative over the persisted preference.
func TestInstallExplicitLangOverridesSavedPreference(t *testing.T) {
	home := t.TempDir()
	writeSavedLang(t, home, "zh-CN", "zh-cn")

	registry := BuildHarnesses(home, "")
	options := InstallOptions{
		Home:      home,
		Cwd:       home,
		Binary:    "rolling-memory",
		SkillName: "rolling-memory",
		Lang:      "en",
	}
	report := Install(registry[:1], options)
	if failures := report.Failures(); len(failures) != 0 {
		t.Fatalf("unexpected failures: %#v", failures)
	}
	text := installedSkillText(t, home)
	if !strings.Contains(text, "# Working with Rolling Memory") {
		t.Fatalf("explicit --lang en did not win over the saved preference:\n%s", text[:120])
	}
}
