// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package text

import (
	"os"
	"path/filepath"
	"testing"
)

// writeGitDirFixture creates a repository root fixture with a `.git` directory
// (and, when remote is non-empty, an origin URL inside its config).
func writeGitDirFixture(t *testing.T, dir, remote string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	if remote != "" {
		body := "[remote \"origin\"]\n\turl = " + remote + "\n"
		if err := os.WriteFile(filepath.Join(dir, ".git", "config"), []byte(body), 0o644); err != nil {
			t.Fatalf("write git config: %v", err)
		}
	}
	return dir
}

func TestDetectGitWorkspaceNameIdentity(t *testing.T) {
	root := writeGitDirFixture(t, filepath.Join(t.TempDir(), "my-repo"), "git@github.com:acme/my-repo.git")
	cases := []struct {
		name string
		dir  string
		want string
	}{
		{"repo root", root, "my-repo"},
		// 子目录必须归一到仓库身份，而不是目录自身（历史缺陷：admin/web/... 各成工作区）。
		{"repo subdirectory", filepath.Join(root, "admin", "web", "src"), "my-repo"},
		{"non-git directory", filepath.Join(t.TempDir(), "codex-smoke"), ""},
		{"nonexistent directory", filepath.Join(t.TempDir(), "gone", "child"), ""},
		{"filesystem root", string(filepath.Separator), ""},
	}
	for _, tc := range cases {
		if got := DetectGitWorkspaceName(tc.dir); got != tc.want {
			t.Errorf("%s: DetectGitWorkspaceName(%q) = %q, want %q", tc.name, tc.dir, got, tc.want)
		}
	}
}

func TestDetectGitWorkspaceNameHomeAndRuntimeGuards(t *testing.T) {
	home := filepath.Join(t.TempDir(), "fakehome")
	// 家目录自身就是一个 git 仓库（dotfiles 场景）：仍不得成为工作区。
	writeGitDirFixture(t, home, "")
	runtimeDir := filepath.Join(home, ".local", "share", "rolling-memory")
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		t.Fatalf("mkdir runtime dir: %v", err)
	}
	t.Setenv("HOME", home)

	for _, dir := range []string{
		home,
		runtimeDir,
		filepath.Join(runtimeDir, "admin"),
		filepath.Join(home, ".local", "state", "rolling-memory"),
	} {
		if got := DetectGitWorkspaceName(dir); got != "" {
			t.Errorf("DetectGitWorkspaceName(%q) = %q, want \"\" (home/runtime guard)", dir, got)
		}
	}
	// 家目录下的普通目录（无自身 .git）同样不得冒用家目录仓库名。
	if got := DetectGitWorkspaceName(filepath.Join(home, "drafts")); got != "" {
		t.Errorf("DetectGitWorkspaceName(drafts under home repo) = %q, want \"\"", got)
	}
	// 家目录下的独立仓库仍是一个正当工作区。
	repo := writeGitDirFixture(t, filepath.Join(home, "proj"), "")
	if got := DetectGitWorkspaceName(filepath.Join(repo, "sub")); got != "proj" {
		t.Errorf("DetectGitWorkspaceName(repo under home) = %q, want proj", got)
	}
}

func TestDetectGitWorkspaceNameLinkedWorktreeAndSubmodule(t *testing.T) {
	base := t.TempDir()
	main := writeGitDirFixture(t, filepath.Join(base, "main-repo"), "")
	worktree := filepath.Join(base, "linked-wt")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	pointer := "gitdir: " + filepath.Join(main, ".git", "worktrees", "linked-wt") + "\n"
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte(pointer), 0o644); err != nil {
		t.Fatalf("write worktree pointer: %v", err)
	}
	// worktree 归主仓身份（同仓同工作区），而非 worktree 目录自身。
	if got := DetectGitWorkspaceName(worktree); got != "main-repo" {
		t.Errorf("worktree root = %q, want main-repo", got)
	}
	if got := DetectGitWorkspaceName(filepath.Join(worktree, "pkg")); got != "main-repo" {
		t.Errorf("worktree subdir = %q, want main-repo", got)
	}

	// 相对 gitdir 指针同样归主仓。
	relWorktree := filepath.Join(base, "rel-wt")
	if err := os.MkdirAll(relWorktree, 0o755); err != nil {
		t.Fatalf("mkdir rel worktree: %v", err)
	}
	relPointer := "gitdir: ../main-repo/.git/worktrees/rel-wt\n"
	if err := os.WriteFile(filepath.Join(relWorktree, ".git"), []byte(relPointer), 0o644); err != nil {
		t.Fatalf("write rel pointer: %v", err)
	}
	if got := DetectGitWorkspaceName(relWorktree); got != "main-repo" {
		t.Errorf("relative worktree = %q, want main-repo", got)
	}

	// 子模块指针保持自身身份（它是独立仓库）。
	sub := filepath.Join(base, "super", "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir submodule: %v", err)
	}
	subPointer := "gitdir: " + filepath.Join(base, "super", ".git", "modules", "sub") + "\n"
	if err := os.WriteFile(filepath.Join(sub, ".git"), []byte(subPointer), 0o644); err != nil {
		t.Fatalf("write submodule pointer: %v", err)
	}
	if got := DetectGitWorkspaceName(sub); got != "sub" {
		t.Errorf("submodule = %q, want sub", got)
	}
}

// TestDetectGitWorkspaceLocusSpatialContract pins the spatial-coordinate
// synthesizer after the walker refactor: the relative subdirectory stays part
// of the coordinate and the remote slug supplies the org segment.
func TestDetectGitWorkspaceLocusSpatialContract(t *testing.T) {
	root := writeGitDirFixture(t, filepath.Join(t.TempDir(), "my-repo"), "git@github.com:acme/my-repo.git")
	if got := DetectGitWorkspaceLocus(root); got != "acme/my-repo" {
		t.Errorf("locus(root) = %q, want acme/my-repo", got)
	}
	if got := DetectGitWorkspaceLocus(filepath.Join(root, "pkg", "engine")); got != "acme/my-repo/pkg/engine" {
		t.Errorf("locus(subdir) = %q, want acme/my-repo/pkg/engine", got)
	}
	plain := writeGitDirFixture(t, filepath.Join(t.TempDir(), "solo"), "")
	if got, want := DetectGitWorkspaceLocus(plain), filepath.Base(filepath.Dir(plain))+"/solo"; got != want {
		t.Errorf("locus(no-remote repo) = %q, want %q", got, want)
	}
}
