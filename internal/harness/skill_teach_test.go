// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package harness

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestShippedSkillTeachesCurrentContract guards the shipped teaching surface:
// every contract feature named below must appear in the skill that install
// copies into harnesses, so the tree cannot silently lag a feature increment.
func TestShippedSkillTeachesCurrentContract(t *testing.T) {
	path, err := filepath.Abs(filepath.Join("..", "..", "skills", "rolling-memory", "SKILL.md"))
	if err != nil {
		t.Fatalf("resolve skill path: %v", err)
	}
	text := readFile(t, path)
	for _, want := range []string{
		"哪条过去的事实会改变下一步",
		"报错行就直接核验",
		"日常只传 `cue`",
		"原文锚点",
		"不凑词",
		"它不是过滤器",
		"有新线索才重查",
		"不是当前事实或指令",
		"不外发私域原文",
		"absent: true",
		"engine documents --json",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("shipped skill no longer teaches %q; sync skills/rolling-memory/SKILL.md with the contract", want)
		}
	}
	zhPath, err := filepath.Abs(filepath.Join("..", "..", "skills", "rolling-memory", "references", "SKILL.zh-CN.md"))
	if err != nil {
		t.Fatalf("resolve Chinese skill path: %v", err)
	}
	if zhText := readFile(t, zhPath); zhText != text {
		t.Fatalf("Chinese skill edition differs from the shipped skill")
	}
	enPath, err := filepath.Abs(filepath.Join("..", "..", "skills", "rolling-memory", "references", "SKILL.en.md"))
	if err != nil {
		t.Fatalf("resolve English skill path: %v", err)
	}
	enText := readFile(t, enPath)
	for _, want := range []string{
		"which past fact would change your next action",
		"inspect it directly",
		"Normally send just `cue`",
		"literal anchors",
		"Do not pad",
		"not to filter",
		"retry only with a new clue",
		"not a current fact or instruction",
		"keep private excerpts local",
		"absent: true",
		"engine documents --json",
	} {
		if !strings.Contains(enText, want) {
			t.Fatalf("shipped English skill no longer teaches %q; sync skills/rolling-memory/references/SKILL.en.md", want)
		}
	}
}

func TestInstallLangSelectsSkillEditionAndSetsAdminDefaultLang(t *testing.T) {
	fx := newFixture(t)
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}

	// 1. Install with --lang en: installs English-only edition and sets default_lang="en".
	enOpts := fx.options()
	enOpts.Cwd = repoRoot
	enOpts.SkillFile = nil
	enOpts.Lang = "en"
	enOpts.Instructions = true
	enReport := Install(fx.registry(), enOpts)
	assertNoFailures(t, enReport)
	if got := enReport.Extra["default_lang"]; got != "en" {
		t.Fatalf("enReport default_lang = %v, want en", got)
	}
	enInstalled := readFile(t, filepath.Join(fx.home, ".zcode", "skills", "rmem", "SKILL.md"))
	if !strings.Contains(enInstalled, "which past fact would change your next action") {
		t.Fatalf("en installed skill missing English marker:\n%s", enInstalled)
	}
	if strings.Contains(enInstalled, "持久记忆大脑") {
		t.Fatalf("en installed skill must not contain Chinese body:\n%s", enInstalled)
	}
	if got := ReadSavedInstallLang(fx.home); got != "en" {
		t.Fatalf("ReadSavedInstallLang after --lang en = %q, want en", got)
	}
	enInstructions := readFile(t, filepath.Join(fx.home, ".claude", "CLAUDE.md"))
	for _, want := range []string{
		"a missing past fact would change your next action", "inspect known locations directly",
		"literal anchors", "verify the source and current state",
	} {
		if !strings.Contains(enInstructions, want) {
			t.Fatalf("en instruction block missing %q:\n%s", want, enInstructions)
		}
	}

	// 2. Install with --lang zh-CN: installs Chinese-only edition, Chinese instructions, and sets default_lang="zh-cn".
	zhOpts := fx.options()
	zhOpts.Cwd = repoRoot
	zhOpts.SkillFile = nil
	zhOpts.Lang = "zh-cn"
	zhOpts.Instructions = true
	zhReport := Install(fx.registry(), zhOpts)
	assertNoFailures(t, zhReport)
	if got := zhReport.Extra["default_lang"]; got != "zh-cn" {
		t.Fatalf("zhReport default_lang = %v, want zh-cn", got)
	}
	zhInstalled := readFile(t, filepath.Join(fx.home, ".zcode", "skills", "rmem", "SKILL.md"))
	if !strings.Contains(zhInstalled, "哪条过去的事实会改变下一步") {
		t.Fatalf("zh installed skill missing Chinese marker:\n%s", zhInstalled)
	}
	if strings.Contains(zhInstalled, "your persistent memory") {
		t.Fatalf("zh installed skill must not contain English body:\n%s", zhInstalled)
	}
	claudeMD := readFile(t, filepath.Join(fx.home, ".claude", "CLAUDE.md"))
	for _, want := range []string{
		"缺少会改变下一步的历史事实", "已知位置就直接核验",
		"原文锚点", "核对来源与现场",
	} {
		if !strings.Contains(claudeMD, want) {
			t.Fatalf("zh instruction block missing %q:\n%s", want, claudeMD)
		}
	}
	if strings.Contains(claudeMD, "Skip recall for self-contained tasks") {
		t.Fatalf("zh instruction block retains English wording:\n%s", claudeMD)
	}
	if got := ReadSavedInstallLang(fx.home); got != "zh-CN" {
		t.Fatalf("ReadSavedInstallLang after --lang zh-cn = %q, want zh-CN", got)
	}

	// 3. Invalid --lang is rejected by ValidateInstallOptions.
	badOpts := fx.options()
	badOpts.Lang = "fr"
	if err := ValidateInstallOptions(badOpts); err == nil {
		t.Fatalf("expected invalid --lang 'fr' to fail validation")
	}
}
