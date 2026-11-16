// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package harness

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	rollingmemory "github.com/rocareer/rolling-memory"
)

func TestEmbeddedSkillMatchesSource(t *testing.T) {
	for _, path := range []string{
		"skills/rolling-memory/SKILL.md",
		"skills/rolling-memory/references/SKILL.en.md",
		"skills/rolling-memory/references/SKILL.zh-CN.md",
	} {
		embedded, err := rollingmemory.Skills.ReadFile(path)
		if err != nil {
			t.Fatalf("read embedded %s: %v", path, err)
		}
		disk, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(path)))
		if err != nil {
			t.Fatalf("read source %s: %v", path, err)
		}
		if string(embedded) != string(disk) {
			t.Fatalf("embedded %s differs from source", path)
		}
	}
	options := InstallOptions{Home: t.TempDir(), Cwd: t.TempDir(), SkillName: DefaultName}
	text, err := skillText(options)
	if err != nil {
		t.Fatalf("read default embedded skill: %v", err)
	}
	want, err := rollingmemory.Skills.ReadFile("skills/rolling-memory/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	wantText := pyStripRight(strings.Join(pySplitLines(string(want)), "\n")) + "\n"
	if text != wantText {
		t.Fatal("default skill differs from embedded source")
	}
}

func TestStandaloneEmbeddedSkillInstall(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "bin", "rolling-memory")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	if err := os.MkdirAll(filepath.Dir(binary), 0o755); err != nil {
		t.Fatal(err)
	}
	build := exec.Command("go", "build", "-o", binary, "./cmd/rolling-memory")
	build.Dir = filepath.Join("..", "..")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build standalone binary: %v\n%s", err, out)
	}

	for _, tc := range []struct {
		name string
		lang string
		path string
	}{
		{"default", "", "skills/rolling-memory/references/SKILL.en.md"},
		{"english", "en", "skills/rolling-memory/references/SKILL.en.md"},
		{"chinese", "zh-CN", "skills/rolling-memory/references/SKILL.zh-CN.md"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := filepath.Join(root, tc.name, "home")
			cwd := filepath.Join(root, tc.name, "work")
			if err := os.MkdirAll(cwd, 0o755); err != nil {
				t.Fatal(err)
			}
			args := []string{"install", "--binary", binary, "--json", "codex"}
			if tc.lang != "" {
				args = append(args, "--lang", tc.lang)
			}
			cmd := exec.Command(binary, args...)
			cmd.Dir = cwd
			cmd.Env = append(os.Environ(), "HOME="+home, "USERPROFILE="+home, "ROLLING_MEMORY_PORTABLE=0")
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("standalone install: %v\n%s", err, out)
			}
			got, err := os.ReadFile(filepath.Join(home, ".codex", "skills", "rolling-memory", "SKILL.md"))
			if err != nil {
				t.Fatalf("read installed skill: %v", err)
			}
			want, err := rollingmemory.Skills.ReadFile(tc.path)
			if err != nil {
				t.Fatal(err)
			}
			wantText := pyStripRight(strings.Join(pySplitLines(string(want)), "\n")) + "\n"
			if string(got) != wantText {
				t.Fatalf("installed skill differs from embedded %s", tc.path)
			}
		})
	}
}

func TestSkillDiskPriorityAndExplicitMissing(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(root, "work")
	home := filepath.Join(root, "home")
	base := filepath.Join(cwd, "skills", "rolling-memory")
	if err := os.MkdirAll(filepath.Join(base, "references"), 0o755); err != nil {
		t.Fatal(err)
	}
	for path, text := range map[string]string{
		filepath.Join(base, "SKILL.md"):                  "---\nname: original\n---\ndisk default\n",
		filepath.Join(base, "references", "SKILL.en.md"): "---\nname: original\n---\ndisk english\n",
	} {
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	options := InstallOptions{Home: home, Cwd: cwd, SkillName: "custom"}
	for _, tc := range []struct{ lang, marker string }{{"", "disk default"}, {"en", "disk english"}, {"zh-CN", "disk default"}} {
		options.Lang = tc.lang
		text, err := skillText(options)
		if err != nil || !strings.Contains(text, tc.marker) || !strings.Contains(text, "name: custom") {
			t.Fatalf("lang %q: text %q, err %v", tc.lang, text, err)
		}
	}
	missing := filepath.Join(root, "missing.md")
	options.SkillFile = &missing
	if _, err := skillText(options); err == nil || !strings.Contains(err.Error(), "skill source not found") {
		t.Fatalf("explicit missing skill: %v", err)
	}

	explicit := filepath.Join(root, "explicit.md")
	if err := os.WriteFile(explicit, []byte("name: original\nexplicit skill\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	options.SkillFile = &explicit
	text, err := skillText(options)
	if err != nil || !strings.Contains(text, "explicit skill") || !strings.Contains(text, "name: custom") {
		t.Fatalf("explicit skill: text %q, err %v", text, err)
	}
}
