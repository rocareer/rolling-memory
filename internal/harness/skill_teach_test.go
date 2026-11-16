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
		"持久记忆大脑",
		"任务开始 · 先理解意图，再按缺口检索",
		"跨会话背景或意图不明、触及架构边界或历史裁定，或坐标未知准备盲搜",
		"任务自包含、坐标已知且无历史耦合时免检",
		"任务卡住 · 必执行 `recall` 检索",
		"保真 ≠ 正确",
		"absent: true",
		"## 5. 收口如实评价（本轮调过 `recall` 时必写 · 可选区块）",
		"install --check",
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
		"your persistent memory",
		"Task start — understand intent, then retrieve for information gaps",
		"cross-session context or intent is unclear, an architectural boundary or historical decision matters, or an entrypoint is unknown before a blind search",
		"Skip recall when the task is self-contained, coordinates are known, and history is irrelevant",
		"Task Stuck — Mandatory Obstacle Recall",
		"Fidelity ≠ Correctness",
		"absent: true",
		"## 5. Wrap-up Review (Required When `recall` Was Called This Turn · Optional Section)",
		"install --check",
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
	if !strings.Contains(enInstalled, "Task start — understand intent, then retrieve for information gaps") {
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
		"unclear cross-session context", "architectural boundaries or historical decisions",
		"unknown entrypoints before a blind search", "Skip recall for self-contained tasks with known coordinates and no historical coupling",
		"call `recall` when stuck or facing an error",
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
	if !strings.Contains(zhInstalled, "任务开始 · 先理解意图，再按缺口检索") {
		t.Fatalf("zh installed skill missing Chinese marker:\n%s", zhInstalled)
	}
	if strings.Contains(zhInstalled, "your persistent memory") {
		t.Fatalf("zh installed skill must not contain English body:\n%s", zhInstalled)
	}
	claudeMD := readFile(t, filepath.Join(fx.home, ".claude", "CLAUDE.md"))
	for _, want := range []string{
		"跨会话背景不明", "涉及架构或历史裁定", "坐标未知准备盲搜",
		"任务自包含、坐标已知且无历史耦合时免检", "任务卡住或报错时必检",
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
