// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package harness

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSkillOwnershipProtectsUserFiles(t *testing.T) {
	fx := newFixture(t)
	options := fx.options()
	registry, err := SelectHarnesses(fx.registry(), []string{"zcode"}, false)
	if err != nil {
		t.Fatal(err)
	}
	h := registry[0]
	target := skillTargets(h, options)[0]
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	user := "---\nname: " + options.SkillName + "\ndescription: user skill\n---\n"
	backup := target + ".rolling-memory.20260101000000.bak"
	if err := os.WriteFile(target, []byte(user), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backup, []byte("user backup"), 0o600); err != nil {
		t.Fatal(err)
	}
	check := &Report{}
	checkSkill(h, options, check)
	if check.Actions[0].Status != StatusConflict {
		t.Fatalf("check unowned skill: %#v", check.Actions)
	}
	for _, force := range []bool{false, true} {
		options.Force = force
		for _, operation := range []func(Harness, InstallOptions, *Report){installSkill, uninstallSkill} {
			report := &Report{}
			operation(h, options, report)
			if len(report.Actions) != 1 || report.Actions[0].Status != StatusConflict {
				t.Fatalf("force=%v: expected conflict, got %#v", force, report.Actions)
			}
			if got := readFile(t, target); got != user {
				t.Fatalf("user skill changed: %q", got)
			}
			if got := readFile(t, backup); got != "user backup" {
				t.Fatalf("user backup changed: %q", got)
			}
		}
	}
}

func TestSkillOwnershipPluginDedupPreservesUnowned(t *testing.T) {
	fx := newFixture(t)
	options := fx.options()
	options.Name = DefaultName
	options.SkillName = DefaultName
	registry, err := SelectHarnesses(fx.registry(), []string{"zcode"}, false)
	if err != nil {
		t.Fatal(err)
	}
	h := registry[0]
	target := skillTargets(h, options)[0]
	report := &Report{}
	installSkill(h, options, report)
	if report.Actions[0].Status != StatusInstalled {
		t.Fatalf("install: %#v", report.Actions)
	}
	backup := target + ".rolling-memory.20260101000000.bak"
	if err := os.WriteFile(backup, []byte("user backup"), 0o600); err != nil {
		t.Fatal(err)
	}
	setupZCodePluginCache(t, fx, "0.7.4")
	if err := os.MkdirAll(filepath.Dir(fx.zcodeConfig()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fx.zcodeConfig(), []byte(`{"plugins":{"enabledPlugins":{"rolling-memory@marketplace":true}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("user skill"), 0o600); err != nil {
		t.Fatal(err)
	}
	report = &Report{}
	installSkill(h, options, report)
	if report.Actions[0].Status != StatusConflict || readFile(t, target) != "user skill" || readFile(t, backup) != "user backup" {
		t.Fatalf("unowned duplicate: %#v", report.Actions)
	}
	text, err := skillText(options)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	report = &Report{}
	installSkill(h, options, report)
	if report.Actions[0].Status != StatusRemoved || fileExists(target) || readFile(t, backup) != "user backup" {
		t.Fatalf("owned duplicate: %#v", report.Actions)
	}
}

func TestSkillOwnershipUpdateAndModifiedConflict(t *testing.T) {
	fx := newFixture(t)
	options := fx.options()
	registry, err := SelectHarnesses(fx.registry(), []string{"zcode"}, false)
	if err != nil {
		t.Fatal(err)
	}
	h := registry[0]
	target := skillTargets(h, options)[0]
	report := &Report{}
	installSkill(h, options, report)
	if report.Actions[0].Status != StatusInstalled {
		t.Fatalf("install: %#v", report.Actions)
	}
	if err := os.WriteFile(*options.SkillFile, []byte(syntheticSkill+"updated\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	report = &Report{}
	installSkill(h, options, report)
	if report.Actions[0].Status != StatusUpdated {
		t.Fatalf("update: %#v", report.Actions)
	}
	backups, err := filepath.Glob(target + ".rolling-memory.*.bak")
	if err != nil || len(backups) != 1 {
		t.Fatalf("backup: %v, %v", backups, err)
	}
	if err := os.WriteFile(target, []byte("user modified"), 0o600); err != nil {
		t.Fatal(err)
	}
	report = &Report{}
	uninstallSkill(h, options, report)
	if report.Actions[0].Status != StatusConflict || readFile(t, target) != "user modified" {
		t.Fatalf("modified skill: %#v", report.Actions)
	}
	text, err := skillText(options)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	report = &Report{}
	uninstallSkill(h, options, report)
	if report.Actions[0].Status != StatusRemoved || fileExists(target) {
		t.Fatalf("owned uninstall: %#v", report.Actions)
	}
	if !fileExists(backups[0]) {
		t.Fatal("uninstall removed backup")
	}
}
