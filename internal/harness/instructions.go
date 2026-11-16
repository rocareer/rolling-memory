// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package harness

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The instruction surface: a marker block appended to AGENTS.md / CLAUDE.md /
// CODEBUDDY.md. Uninstall removes exactly the block, never the whole file; a
// half marker is a conflict that a human must fix.

func instructionBlock(options InstallOptions) string {
	if norm, _ := NormalizeInstallLang(options.Lang); norm == "zh-CN" {
		lines := []string{
			InstructionBegin,
			fmt.Sprintf("## Rolling-Memory (%s)", options.SkillName),
			fmt.Sprintf("跨会话持久记忆大脑已通过 MCP `%s`（工具 `recall` · 回忆）连接。", options.Name),
			"- 任务开始先理解意图；跨会话背景不明、涉及架构或历史裁定、或坐标未知准备盲搜时，用具体 `cue` 调用 `recall`。任务自包含、坐标已知且无历史耦合时免检。任务卡住或报错时必检，再排障。",
			"- `absent: true` 表示暂无相关历史记忆——这不是报错，严禁编造记忆内容。",
			fmt.Sprintf("细则见 `%s` 技能。", options.SkillName),
			InstructionEnd,
		}
		return strings.Join(lines, "\n") + "\n"
	}
	lines := []string{
		InstructionBegin,
		fmt.Sprintf("## Rolling-Memory (%s)", options.SkillName),
		fmt.Sprintf("Your persistent memory is connected via MCP `%s` (tool `recall` · 回忆).", options.Name),
		"- At task start, understand intent; call `recall` with a specific `cue` for unclear cross-session context," +
			" architectural boundaries or historical decisions, or unknown entrypoints before a blind search." +
			" Skip recall for self-contained tasks with known coordinates and no historical coupling;" +
			" call `recall` when stuck or facing an error before troubleshooting.",
		"- `absent: true` means you have no prior memory of it -- it is not an error;" +
			" never invent memory content.",
		fmt.Sprintf("Details: the `%s` skill.", options.SkillName),
		InstructionEnd,
	}
	return strings.Join(lines, "\n") + "\n"
}

func instructionTargets(harness Harness, options InstallOptions) []string {
	if options.Project {
		if harness.InstructionsProject != "" {
			return []string{filepath.Join(options.Cwd, filepath.FromSlash(harness.InstructionsProject))}
		}
		return nil
	}
	if harness.InstructionsUser != nil {
		return []string{*harness.InstructionsUser}
	}
	return nil
}

// markerRegion returns the block bounds and whether the markers are complete.
// A half marker (one side only, or end before begin) is reported separately.
func markerRegion(text string) (int, int, bool, bool) {
	begin := strings.Index(text, InstructionBegin)
	end := strings.Index(text, InstructionEnd)
	if begin == -1 && end == -1 {
		return 0, 0, false, false
	}
	if begin == -1 || end == -1 || end < begin {
		return 0, 0, false, true
	}
	return begin, end + len(InstructionEnd), true, false
}

func installInstructions(harness Harness, options InstallOptions, report *Report) {
	targets := instructionTargets(harness, options)
	if len(targets) == 0 {
		report.Add(harness.ID, "instructions", "-", StatusSkipped, "no instruction target for this scope")
		return
	}
	block := instructionBlock(options)
	for _, target := range targets {
		original := ""
		if fileExists(target) {
			raw, err := os.ReadFile(target)
			if err != nil {
				report.Add(harness.ID, "instructions", target, StatusError, "cannot read: "+err.Error())
				continue
			}
			original = string(raw)
		}
		start, end, found, half := markerRegion(original)
		if half {
			report.Add(harness.ID, "instructions", target, StatusConflict, "half marker found; fix by hand")
			continue
		}
		if !found {
			if options.DryRun {
				report.Add(harness.ID, "instructions", target, StatusPlanned, "append marker block")
				continue
			}
			text := original
			if text != "" && !strings.HasSuffix(text, "\n") {
				text += "\n"
			}
			if strings.TrimSpace(text) != "" {
				text += "\n"
			}
			if _, err := writeText(target, text+block); err != nil {
				report.Add(harness.ID, "instructions", target, StatusError, "cannot write: "+err.Error())
				continue
			}
			report.Add(harness.ID, "instructions", target, StatusInstalled, "")
			continue
		}
		current := original[start:end]
		if strings.TrimSpace(current) == strings.TrimSpace(block) {
			report.Add(harness.ID, "instructions", target, StatusUnchanged, "")
			continue
		}
		if options.DryRun {
			report.Add(harness.ID, "instructions", target, StatusPlanned, "replace marker block")
			continue
		}
		if _, err := writeText(target, original[:start]+strings.TrimRight(block, "\n")+original[end:]); err != nil {
			report.Add(harness.ID, "instructions", target, StatusError, "cannot write: "+err.Error())
			continue
		}
		report.Add(harness.ID, "instructions", target, StatusUpdated, "")
	}
}

func uninstallInstructions(harness Harness, options InstallOptions, report *Report) {
	for _, target := range instructionTargets(harness, options) {
		if !fileExists(target) {
			report.Add(harness.ID, "instructions", target, StatusSkipped, "not present")
			continue
		}
		raw, err := os.ReadFile(target)
		if err != nil {
			report.Add(harness.ID, "instructions", target, StatusError, "cannot read: "+err.Error())
			continue
		}
		original := string(raw)
		start, end, found, half := markerRegion(original)
		if !found && !half {
			report.Add(harness.ID, "instructions", target, StatusSkipped, "no marker block")
			continue
		}
		if half {
			report.Add(harness.ID, "instructions", target, StatusConflict, "half marker found; fix by hand")
			continue
		}
		if options.DryRun {
			report.Add(harness.ID, "instructions", target, StatusPlanned, "")
			continue
		}
		text := strings.Trim(original[:start]+original[end:], "\n")
		if text != "" {
			text += "\n"
		}
		if _, err := writeText(target, text); err != nil {
			report.Add(harness.ID, "instructions", target, StatusError, "cannot write: "+err.Error())
			continue
		}
		report.Add(harness.ID, "instructions", target, StatusRemoved, "")
	}
}

func checkInstructions(harness Harness, options InstallOptions, report *Report) {
	targets := instructionTargets(harness, options)
	if len(targets) == 0 {
		report.Add(harness.ID, "instructions", "-", StatusSkipped, "no instruction target for this scope")
		return
	}
	// A check reports the instructions that are on disk, not whether
	// --with-instructions was passed to this check run.
	for _, target := range targets {
		if !fileExists(target) {
			report.Add(harness.ID, "instructions", target, StatusSkipped, "not wired")
			continue
		}
		raw, err := os.ReadFile(target)
		if err != nil {
			report.Add(harness.ID, "instructions", target, StatusError, "cannot read: "+err.Error())
			continue
		}
		_, _, found, half := markerRegion(string(raw))
		switch {
		case half:
			report.Add(harness.ID, "instructions", target, StatusConflict, "half marker found")
		case !found:
			report.Add(harness.ID, "instructions", target, StatusSkipped, "not wired (marker block not present)")
		default:
			report.Add(harness.ID, "instructions", target, StatusOK, "")
		}
	}
}
