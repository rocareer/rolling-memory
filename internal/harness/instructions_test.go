// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package harness

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func historicalInstructionBlock(options InstallOptions) string {
	return strings.Join([]string{
		InstructionBegin,
		fmt.Sprintf("## Rolling-Memory (%s)", options.SkillName),
		fmt.Sprintf("This harness has the Rolling-Memory core installed (MCP server `%s`, tools `remember` / `recall`).", options.Name),
		"- When a task depends on prior decisions, constraints or past mistakes, call `recall` with a specific cue before answering; call it before writing too, to avoid duplicates.",
		"- Record durable facts (decisions, constraints, verified lessons) with `remember`; prefer the structured assertion form with a verbatim `anchor_text`.",
		"- `absent: true` means \"not held locally\" -- it is not an error; never invent memory content.",
		fmt.Sprintf("Details and receipt semantics: the `%s` skill.", options.SkillName),
		InstructionEnd,
	}, "\n") + "\n"
}

func TestInstructionModifiedBlocksConflict(t *testing.T) {
	fx := newFixture(t)
	options := fx.optionsWith(func(options *InstallOptions) {
		options.Instructions = true
		options.Lang = "en"
	})
	registry, err := SelectHarnesses(fx.registry(), []string{"zcode"}, false)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(fx.home, ".zcode", "AGENTS.md")
	block := instructionBlock(options)
	other := options
	other.Name = "other-server"
	other.SkillName = "other-skill"
	cases := []struct {
		name string
		text string
	}{
		{"ordinary", strings.Replace(block, InstructionEnd, "My personal rule.\n"+InstructionEnd, 1)},
		{"bullet", strings.Replace(block, InstructionEnd, "- 这是按我们的 bullet 格式加的个人规则。\n"+InstructionEnd, 1)},
		{"heading", strings.Replace(block, InstructionEnd, "## Rolling-Memory (personal)\n"+InstructionEnd, 1)},
		{"heading-same-name-inserted", strings.Replace(block, InstructionEnd, "## Rolling-Memory (rmem)\n"+InstructionEnd, 1)},
		{"heading-other-name", strings.Replace(block, "## Rolling-Memory (rmem)", "## Rolling-Memory (personal)", 1)},
		{"minimal-bullet-reproduction", InstructionBegin + "\n## Rolling-Memory (x)\n- 这是按我们的 bullet 格式加的个人规则。\n" + InstructionEnd + "\n"},
		{"details-en", strings.Replace(block, InstructionEnd, "Details: the `personal` skill.\n"+InstructionEnd, 1)},
		{"details-zh", strings.Replace(block, InstructionEnd, "细则见 `personal` 技能。\n"+InstructionEnd, 1)},
		{"intro", strings.Replace(block, InstructionEnd, "Your persistent memory is connected via MCP `personal`.\n"+InstructionEnd, 1)},
		{"duplicate-generated-line", strings.Replace(block, InstructionEnd, "Details: the `rmem` skill.\n"+InstructionEnd, 1)},
		{"blank-line", strings.Replace(block, InstructionEnd, "\n"+InstructionEnd, 1)},
		{"trailing-space", strings.Replace(block, "## Rolling-Memory (rmem)", "## Rolling-Memory (rmem) ", 1)},
		{"heading-suffix", strings.Replace(block, "## Rolling-Memory (rmem)", "## Rolling-Memory (rmem) personal", 1)},
		{"foreign-generated-names", instructionBlock(other)},
		{"foreign-server", strings.Replace(block, "MCP `rmem`", "MCP `other-server`", 1)},
		{"foreign-details", strings.Replace(block, "Details: the `rmem` skill.", "Details: the `other-skill` skill.", 1)},
		{"mixed-eol", strings.Replace(block, "\n", "\r\n", 1)},
		{"crlf-bullet", strings.ReplaceAll(strings.Replace(block, InstructionEnd, "- personal\n"+InstructionEnd, 1), "\n", "\r\n")},
		{"duplicate-later-modified", block + strings.Replace(block, InstructionEnd, "- personal\n"+InstructionEnd, 1)},
		{"duplicate-valid", block + block},
		{"nested-marker", strings.Replace(block, InstructionEnd, InstructionBegin+"\n"+InstructionEnd, 1)},
		{"end-before-begin", InstructionEnd + "\n" + block},
		{"extra-end", block + InstructionEnd + "\n"},
		{"inline-marker", "prefix " + strings.TrimSuffix(block, "\n") + " suffix\n"},
		{"fenced-marker", "```markdown\n" + block + "```\n"},
		{"tilde-fence", "  ~~~~text\n~~~\n" + block + "~~~~\n"},
		{"short-fence-closer", "````\n```\n" + block + "````\n"},
		{"fence-with-invalid-closer", "```\n```suffix\n" + block + "```\n"},
		{"only-begin", InstructionBegin + "\npersonal\n"},
		{"only-end", "personal\n" + InstructionEnd + "\n"},
	}
	for _, tc := range cases {
		for _, dryRun := range []bool{false, true} {
			for _, force := range []bool{false, true} {
				for _, operation := range []string{"install", "uninstall", "check"} {
					t.Run(fmt.Sprintf("%s/%s/dry=%t/force=%t", tc.name, operation, dryRun, force), func(t *testing.T) {
						original := "\n# user prefix\n\n" + tc.text + "\n# user suffix\n\n"
						if err := os.WriteFile(target, []byte(original), 0o600); err != nil {
							t.Fatal(err)
						}
						opts := options
						opts.DryRun, opts.Force = dryRun, force
						var report *Report
						switch operation {
						case "install":
							report = Install(registry, opts)
						case "uninstall":
							report = Uninstall(registry, opts)
						case "check":
							report = &Report{}
							checkInstructions(registry[0], opts, report)
						}
						if action := firstAction(t, report, "zcode", "instructions"); action.Status != StatusConflict {
							t.Errorf("status = %q, want %q", action.Status, StatusConflict)
						}
						if got := readFile(t, target); got != original {
							t.Errorf("instruction file changed:\n got %q\nwant %q", got, original)
						}
					})
				}
			}
		}
	}
}

func TestInstructionGeneratedBlocksUpgradeAndUninstall(t *testing.T) {
	fx := newFixture(t)
	options := fx.optionsWith(func(options *InstallOptions) {
		options.Instructions = true
		options.Lang = "zh-CN"
	})
	registry, err := SelectHarnesses(fx.registry(), []string{"zcode"}, false)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(fx.home, ".zcode", "AGENTS.md")
	english := options
	english.Lang = "en"
	for _, tc := range []struct {
		name  string
		block string
	}{
		{"current-en", instructionBlock(english)},
		{"current-zh", instructionBlock(options)},
		{"historical-en", historicalInstructionBlock(options)},
		{"old-self-digest", withInstructionDigest(priorTemplateBlock(options))},
	} {
		for _, eol := range []string{"\n", "\r\n"} {
			for _, operation := range []string{"install", "uninstall", "check"} {
				t.Run(fmt.Sprintf("%s/eol=%q/%s", tc.name, eol, operation), func(t *testing.T) {
					block := strings.ReplaceAll(tc.block, "\n", eol)
					prefix := eol + "# user prefix" + eol + eol
					suffix := eol + "# user suffix" + eol + eol
					original := prefix + block + suffix
					if err := os.WriteFile(target, []byte(original), 0o600); err != nil {
						t.Fatal(err)
					}
					var report *Report
					var want, status string
					switch operation {
					case "install":
						report = Install(registry, options)
						status = StatusUpdated
						if tc.name == "current-zh" {
							status = StatusUnchanged
						}
						want = prefix + strings.ReplaceAll(instructionBlock(options), "\n", eol) + suffix
					case "uninstall":
						report = Uninstall(registry, options)
						status = StatusRemoved
						want = prefix + suffix
					case "check":
						report = &Report{}
						checkInstructions(registry[0], options, report)
						status, want = StatusOK, original
						if tc.name == "historical-en" || tc.name == "old-self-digest" {
							status = StatusOutdated
						}
					}
					if action := firstAction(t, report, "zcode", "instructions"); action.Status != status {
						t.Errorf("status = %q, want %q", action.Status, status)
					}
					if got := readFile(t, target); got != want {
						t.Errorf("instruction file:\n got %q\nwant %q", got, want)
					}
				})
			}
		}
	}
}

func TestInstructionDistinctNamesAndHistoricalBinding(t *testing.T) {
	fx := newFixture(t)
	options := fx.optionsWith(func(options *InstallOptions) {
		options.Name = "memory-server"
		options.SkillName = "memory-skill"
		options.Instructions = true
		options.Lang = "en"
	})
	registry, err := SelectHarnesses(fx.registry(), []string{"zcode"}, false)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(fx.home, ".zcode", "AGENTS.md")
	historical := historicalInstructionBlock(options)
	for _, tc := range []struct {
		name string
		text string
	}{
		{"server", strings.Replace(historical, "MCP server `memory-server`", "MCP server `other-server`", 1)},
		{"heading", strings.Replace(historical, "## Rolling-Memory (memory-skill)", "## Rolling-Memory (other-skill)", 1)},
		{"details", strings.Replace(historical, "the `memory-skill` skill", "the `other-skill` skill", 1)},
		{"added-line", strings.Replace(historical, InstructionEnd, "- personal\n"+InstructionEnd, 1)},
	} {
		for _, operation := range []string{"install", "uninstall", "check"} {
			t.Run(tc.name+"/"+operation, func(t *testing.T) {
				if err := os.WriteFile(target, []byte(tc.text), 0o600); err != nil {
					t.Fatal(err)
				}
				var report *Report
				switch operation {
				case "install":
					report = Install(registry, options)
				case "uninstall":
					report = Uninstall(registry, options)
				case "check":
					report = &Report{}
					checkInstructions(registry[0], options, report)
				}
				if action := firstAction(t, report, "zcode", "instructions"); action.Status != StatusConflict {
					t.Errorf("status = %q, want %q", action.Status, StatusConflict)
				}
				if got := readFile(t, target); got != tc.text {
					t.Errorf("instruction file changed:\n got %q\nwant %q", got, tc.text)
				}
			})
		}
	}
	if err := os.WriteFile(target, []byte(historical), 0o600); err != nil {
		t.Fatal(err)
	}
	report := Install(registry, options)
	if action := firstAction(t, report, "zcode", "instructions"); action.Status != StatusUpdated {
		t.Fatalf("historical upgrade status = %q", action.Status)
	}
	if got := readFile(t, target); got != instructionBlock(options) {
		t.Fatalf("historical upgrade = %q", got)
	}
	report = Uninstall(registry, options)
	if action := firstAction(t, report, "zcode", "instructions"); action.Status != StatusRemoved {
		t.Fatalf("uninstall status = %q", action.Status)
	}
}

func TestInstructionAppendAndOutsideWhitespace(t *testing.T) {
	fx := newFixture(t)
	options := fx.optionsWith(func(options *InstallOptions) { options.Instructions = true })
	registry, err := SelectHarnesses(fx.registry(), []string{"zcode"}, false)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(fx.home, ".zcode", "AGENTS.md")
	for _, tc := range []struct {
		name string
		text string
	}{
		{"leading-and-trailing-blank", "\n\n# personal\n\n\n"},
		{"no-final-newline", "# personal"},
		{"whitespace-only", "\n \n\n"},
		{"crlf", "\r\n# personal\r\n\r\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(target, []byte(tc.text), 0o600); err != nil {
				t.Fatal(err)
			}
			report := Install(registry, options)
			if action := firstAction(t, report, "zcode", "instructions"); action.Status != StatusInstalled {
				t.Fatalf("install status = %q", action.Status)
			}
			installed := readFile(t, target)
			if !strings.HasPrefix(installed, tc.text) {
				t.Fatalf("install changed user prefix: %q", installed)
			}
			if tc.name == "crlf" && strings.Contains(strings.ReplaceAll(installed, "\r\n", ""), "\n") {
				t.Fatalf("install mixed line endings: %q", installed)
			}
			report = Uninstall(registry, options)
			if action := firstAction(t, report, "zcode", "instructions"); action.Status != StatusRemoved {
				t.Fatalf("uninstall status = %q", action.Status)
			}
			if got := readFile(t, target); !strings.HasPrefix(got, tc.text) {
				t.Fatalf("uninstall changed user prefix: %q", got)
			}
		})
	}
	whitespace := "\n\n" + instructionBlock(options) + " \n\n"
	if err := os.WriteFile(target, []byte(whitespace), 0o600); err != nil {
		t.Fatal(err)
	}
	report := Uninstall(registry, options)
	if action := firstAction(t, report, "zcode", "instructions"); action.Status != StatusRemoved {
		t.Fatalf("whitespace uninstall status = %q", action.Status)
	}
	if got := readFile(t, target); got != "\n\n \n\n" {
		t.Fatalf("outside whitespace changed: %q", got)
	}
}

func instructionBackups(t *testing.T, path string) []string {
	t.Helper()
	matches, err := filepath.Glob(path + "." + BackupTag + ".*.bak")
	if err != nil {
		t.Fatal(err)
	}
	return matches
}

func instructionAction(t *testing.T, registry []Harness, options InstallOptions, operation string) Action {
	t.Helper()
	report := &Report{}
	switch operation {
	case "install":
		report = Install(registry, options)
	case "uninstall":
		report = Uninstall(registry, options)
	case "check":
		checkInstructions(registry[0], options, report)
	}
	return firstAction(t, report, "zcode", "instructions")
}

// TestInstructionSymlinkTargets: only a regular file is managed. A symlink --
// dangling, pointing at an external file or at a read-only file -- is a
// conflict that neither writes through the link nor creates its target, and a
// symlinked parent directory keeps working as ordinary path resolution.
func TestInstructionSymlinkTargets(t *testing.T) {
	fx := newFixture(t)
	options := fx.optionsWith(func(options *InstallOptions) {
		options.Instructions = true
		options.Lang = "en"
	})
	registry, err := SelectHarnesses(fx.registry(), []string{"zcode"}, false)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(fx.home, ".zcode", "AGENTS.md")
	reset := func(t *testing.T) {
		t.Helper()
		if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}

	conflictCase := func(t *testing.T) {
		t.Helper()
		for _, operation := range []string{"install", "uninstall", "check"} {
			if action := instructionAction(t, registry, options, operation); action.Status != StatusConflict {
				t.Errorf("%s status = %q, want %q", operation, action.Status, StatusConflict)
			}
		}
		if info, err := os.Lstat(target); err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("symlink replaced: %v %v", info, err)
		}
		if backups := instructionBackups(t, target); len(backups) != 0 {
			t.Fatalf("backups written for a symlink target: %v", backups)
		}
	}

	t.Run("dangling symlink", func(t *testing.T) {
		reset(t)
		missing := filepath.Join(t.TempDir(), "missing.md")
		if err := os.Symlink(missing, target); err != nil {
			t.Fatal(err)
		}
		conflictCase(t)
		if _, err := os.Lstat(missing); !os.IsNotExist(err) {
			t.Fatalf("link target was created: %v", err)
		}
	})

	t.Run("symlink to external file", func(t *testing.T) {
		reset(t)
		external := filepath.Join(t.TempDir(), "external.md")
		content := "# external\n" + instructionBlock(options)
		if err := os.WriteFile(external, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(external, target); err != nil {
			t.Fatal(err)
		}
		conflictCase(t)
		if got := readFile(t, external); got != content {
			t.Fatalf("external file changed: %q", got)
		}
		if backups := instructionBackups(t, external); len(backups) != 0 {
			t.Fatalf("backups written next to the link destination: %v", backups)
		}
	})

	t.Run("symlink to read-only file", func(t *testing.T) {
		reset(t)
		external := filepath.Join(t.TempDir(), "readonly.md")
		if err := os.WriteFile(external, []byte("# read only\n"), 0o400); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(external, target); err != nil {
			t.Fatal(err)
		}
		conflictCase(t)
		if got := readFile(t, external); got != "# read only\n" {
			t.Fatalf("read-only target changed: %q", got)
		}
	})

	t.Run("regular file", func(t *testing.T) {
		reset(t)
		original := "# user\nkeep me\n"
		if err := os.WriteFile(target, []byte(original), 0o600); err != nil {
			t.Fatal(err)
		}
		if action := instructionAction(t, registry, options, "install"); action.Status != StatusInstalled {
			t.Fatalf("install status = %q", action.Status)
		}
		if backups := instructionBackups(t, target); len(backups) != 1 {
			t.Fatalf("regular install backups = %v", backups)
		}
		if action := instructionAction(t, registry, options, "check"); action.Status != StatusOK {
			t.Fatalf("check status = %q", action.Status)
		}
		if action := instructionAction(t, registry, options, "install"); action.Status != StatusUnchanged {
			t.Fatalf("reinstall status = %q", action.Status)
		}
		if action := instructionAction(t, registry, options, "uninstall"); action.Status != StatusRemoved {
			t.Fatalf("uninstall status = %q", action.Status)
		}
		if got := readFile(t, target); !strings.HasPrefix(got, original) {
			t.Fatalf("user content lost: %q", got)
		}
	})

	t.Run("symlinked parent directory", func(t *testing.T) {
		reset(t)
		alias := filepath.Join(t.TempDir(), "alias-home")
		if err := os.Symlink(fx.home, alias); err != nil {
			t.Fatal(err)
		}
		aliased := fx.optionsWith(func(options *InstallOptions) { options.Instructions = true })
		aliased.Home = alias
		aliased.Cwd = alias
		harnesses, err := SelectHarnesses(BuildHarnesses(alias, ""), []string{"zcode"}, false)
		if err != nil {
			t.Fatal(err)
		}
		if action := instructionAction(t, harnesses, aliased, "install"); action.Status != StatusInstalled {
			t.Fatalf("install through a symlinked parent status = %q", action.Status)
		}
		real := filepath.Join(fx.home, ".zcode", "AGENTS.md")
		if got := readFile(t, real); !strings.Contains(got, InstructionBegin) {
			t.Fatalf("block missing after aliased install:\n%s", got)
		}
		if action := instructionAction(t, harnesses, aliased, "uninstall"); action.Status != StatusRemoved {
			t.Fatalf("uninstall through a symlinked parent status = %q", action.Status)
		}
	})
}

// TestInstructionPreExamples: a complete generated block kept inside an HTML
// <pre> example stays untouched -- install, uninstall and check all report a
// conflict -- while a closed <pre> elsewhere or one inside a fenced code block
// does not affect a real marker pair.
func TestInstructionPreExamples(t *testing.T) {
	fx := newFixture(t)
	options := fx.optionsWith(func(options *InstallOptions) {
		options.Instructions = true
		options.Lang = "en"
	})
	registry, err := SelectHarnesses(fx.registry(), []string{"zcode"}, false)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(fx.home, ".zcode", "AGENTS.md")
	english := instructionBlock(options)
	chinese := options
	chinese.Lang = "zh-CN"
	chineseBlock := instructionBlock(chinese)

	conflicts := []struct{ name, text string }{
		{"closed-pre", "<pre>\n" + english + "</pre>\n"},
		{"unclosed-pre", "<pre>\n" + english},
		{"pre-attributes", "<pre class=\"example\">\n" + english + "</pre>\n"},
		{"upper-case", "<PRE>\n" + english + "</PRE>\n"},
		{"same-line", "<pre>" + english + "</pre>"},
		{"chinese-template", "<pre>\n" + chineseBlock + "</pre>\n"},
		{"crlf", "<pre>\r\n" + strings.ReplaceAll(english, "\n", "\r\n") + "</pre>\r\n"},
	}
	for _, tc := range conflicts {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(target, []byte(tc.text), 0o600); err != nil {
				t.Fatal(err)
			}
			for _, version := range []InstallOptions{options, chinese} {
				for _, operation := range []string{"install", "uninstall", "check"} {
					if action := instructionAction(t, registry, version, operation); action.Status != StatusConflict {
						t.Errorf("%s/%s status = %q, want %q", operation, version.Lang, action.Status, StatusConflict)
					}
					if got := readFile(t, target); got != tc.text {
						t.Fatalf("%s/%s changed the file:\n%q", operation, version.Lang, got)
					}
				}
			}
		})
	}

	normals := []struct{ name, text string }{
		{"closed-pre-before", "<pre>snippet</pre>\n\n" + english},
		{"closed-pre-with-attributes", "<pre class=\"x\">snippet</pre>\n\n" + english},
		{"pre-after-block", english + "<pre>later\n"},
		{"pre-inside-fence", "```html\n<pre>\n```\n\n" + english},
	}
	for _, tc := range normals {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(target, []byte(tc.text), 0o600); err != nil {
				t.Fatal(err)
			}
			if action := instructionAction(t, registry, options, "install"); action.Status != StatusUnchanged {
				t.Fatalf("install status = %q", action.Status)
			}
			if action := instructionAction(t, registry, options, "check"); action.Status != StatusOK {
				t.Fatalf("check status = %q", action.Status)
			}
			if action := instructionAction(t, registry, options, "uninstall"); action.Status != StatusRemoved {
				t.Fatalf("uninstall status = %q", action.Status)
			}
			if got, want := readFile(t, target), strings.Replace(tc.text, english, "", 1); got != want {
				t.Fatalf("after uninstall = %q, want %q", got, want)
			}
		})
	}
}

// TestUninstallInstructionKeepsEditedBlock: an uninstall must never delete text
// a human added inside the marker block; it reports a conflict instead.
func TestUninstallInstructionKeepsEditedBlock(t *testing.T) {
	fx := newFixture(t)
	agents := filepath.Join(fx.home, ".zcode", "AGENTS.md")
	edited := "# rules\n" + InstructionBegin + "\n## Rolling-Memory (rmem)\nMY PERSONAL INSTRUCTIONS\n" + InstructionEnd + "\n"
	if err := os.WriteFile(agents, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	report := Uninstall(fx.registry(), fx.options())
	if action := firstAction(t, report, "zcode", "instructions"); action.Status != StatusConflict {
		t.Fatalf("status = %q, want %q", action.Status, StatusConflict)
	}
	if got := readFile(t, agents); got != edited {
		t.Fatalf("edited block content changed:\n%s", got)
	}
}

// TestInstallRefusesEditedBlock: a re-install must not overwrite a block that
// carries user edits.
func TestInstallRefusesEditedBlock(t *testing.T) {
	fx := newFixture(t)
	agents := filepath.Join(fx.home, ".zcode", "AGENTS.md")
	edited := "# rules\n" + InstructionBegin + "\n## Rolling-Memory (rmem)\nMY PERSONAL INSTRUCTIONS\n" + InstructionEnd + "\n"
	if err := os.WriteFile(agents, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	report := Install(fx.registry(), fx.optionsWith(func(options *InstallOptions) { options.Instructions = true }))
	if action := firstAction(t, report, "zcode", "instructions"); action.Status != StatusConflict {
		t.Fatalf("status = %q, want %q", action.Status, StatusConflict)
	}
	if got := readFile(t, agents); got != edited {
		t.Fatalf("edited block content changed:\n%s", got)
	}
}

// TestUninstallInstructionRemovesOursOnly: the canonical block is removed and
// the file's own leading blank lines and content survive byte for byte.
func TestUninstallInstructionRemovesOursOnly(t *testing.T) {
	fx := newFixture(t)
	agents := filepath.Join(fx.home, ".zcode", "AGENTS.md")
	options := fx.options()
	original := "\n\n# user intro\nkeep me\n\n" + instructionBlock(options)
	if err := os.WriteFile(agents, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	report := Uninstall(fx.registry(), options)
	if action := firstAction(t, report, "zcode", "instructions"); action.Status != StatusRemoved {
		t.Fatalf("status = %q, want %q", action.Status, StatusRemoved)
	}
	got := readFile(t, agents)
	if strings.Contains(got, InstructionBegin) {
		t.Fatalf("instruction block not removed:\n%s", got)
	}
	if !strings.HasPrefix(got, "\n\n# user intro") || !strings.Contains(got, "keep me") {
		t.Fatalf("user content lost or file edges damaged:\n%q", got)
	}
}

// TestUninstallInstructionRemovesBlockOnlyFile: a file whose whole content was
// the marker block does not survive the uninstall as a 0-byte leftover.
func TestUninstallInstructionRemovesBlockOnlyFile(t *testing.T) {
	fx := newFixture(t)
	agents := filepath.Join(fx.home, ".zcode", "AGENTS.md")
	options := fx.options()
	if err := os.WriteFile(agents, []byte(instructionBlock(options)), 0o644); err != nil {
		t.Fatal(err)
	}
	report := Uninstall(fx.registry(), options)
	if action := firstAction(t, report, "zcode", "instructions"); action.Status != StatusRemoved {
		t.Fatalf("status = %q, want %q", action.Status, StatusRemoved)
	}
	if _, err := os.Stat(agents); !os.IsNotExist(err) {
		t.Fatalf("block-only instruction file survived uninstall: %v", err)
	}
}

// A release may rewrite the teaching block. The self-digest proves the block
// came from this installer, so an older block is upgraded instead of being
// mistaken for a hand edit; editing inside the block still breaks the digest.
func TestInstructionBlockUpgradeKeepsOwnership(t *testing.T) {
	options := InstallOptions{Name: "rolling-memory", SkillName: "rolling-memory", Lang: "zh-CN"}
	older := withInstructionDigest(strings.Join([]string{
		InstructionBegin,
		"## Rolling-Memory (rolling-memory)",
		"跨会话持久记忆大脑已通过 MCP `rolling-memory`（工具 `recall`）连接。",
		"既有版本写入的教学块。",
		InstructionEnd,
	}, "\n") + "\n")
	if !instructionBlockIsOurs(older, options) {
		t.Fatal("a self-digested block from an older release must be recognized")
	}
	if got := strings.TrimSuffix(older, "\n"); !instructionBlockIsOurs(got, options) {
		t.Fatal("the block region excludes the trailing newline; recognition must not depend on it")
	}
	crlf := strings.TrimSuffix(strings.ReplaceAll(older, "\n", "\r\n"), "\r\n")
	if !instructionBlockIsOurs(crlf, options) {
		t.Fatal("a CRLF block must be recognized")
	}
	edited := strings.Replace(older, "既有版本写入的教学块。", "既有版本写入的教学块。\n手工追加的一行", 1)
	if instructionBlockIsOurs(edited, options) {
		t.Fatal("a hand edit inside the block must stay a conflict")
	}
}
