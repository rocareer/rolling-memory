// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package golden

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// surfaceWorkRoot returns a temporary work root with symlinks resolved, so the
// paths the product reports back through os.Getwd (macOS resolves /var to
// /private/var) match the token substitution root.
func surfaceWorkRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve work root: %v", err)
	}
	return root
}

// TestGoldenSurfaceCLI replays cli/208-cli-matrix: 58 invocations covering the
// leaf commands, one direct SQL write and the mcp/hook tails.
func TestGoldenSurfaceCLI(t *testing.T) {
	restore := ReplayModeFreeze()
	defer restore()
	if err := ReplayCLISurface(surfaceWorkRoot(t), repoRoot(t), "208-cli-matrix"); err != nil {
		t.Fatal(err)
	}
}

// TestGoldenSurfaceMCP replays the two MCP transcripts (209 caps, 220 protocol).
func TestGoldenSurfaceMCP(t *testing.T) {
	for _, scenario := range []string{"209-mcp-caps", "220-mcp"} {
		scenario := scenario
		t.Run(scenario, func(t *testing.T) {
			restore := ReplayModeFreeze()
			defer restore()
			if err := ReplayMCPSurface(surfaceWorkRoot(t), repoRoot(t), scenario); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// TestGoldenSurfaceHooks replays the cumulative hook cases: import + boot,
// recall injection, ledger dedup, silent inputs, text mode and fail-open.
func TestGoldenSurfaceHooks(t *testing.T) {
	restore := ReplayModeFreeze()
	defer restore()
	if err := ReplayHooksSurface(surfaceWorkRoot(t), repoRoot(t), "230-hooks"); err != nil {
		t.Fatal(err)
	}
}

// TestGoldenSurfaceInstall replays install/240-install across the five harness
// registries. --check performs the live MCP handshake, so the real launcher
// binary is built once and copied to <CASE>/bin/rolling-memory.
func TestGoldenSurfaceInstall(t *testing.T) {
	restore := ReplayModeFreeze()
	defer restore()
	binary := buildSurfaceBinary(t, repoRoot(t))
	if err := ReplayInstallSurface(surfaceWorkRoot(t), repoRoot(t), "240-install", binary); err != nil {
		t.Fatal(err)
	}
}

// buildSurfaceBinary builds cmd/rolling-memory for the install handshake.
func buildSurfaceBinary(t *testing.T, repoRoot string) string {
	t.Helper()
	output := filepath.Join(t.TempDir(), "rolling-memory")
	command := exec.Command("go", "build", "-trimpath", "-o", output, "./cmd/rolling-memory")
	command.Dir = repoRoot
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		t.Fatalf("build launcher: %v", err)
	}
	return output
}

// TestSurfaceShortcutTokenPlatformLauncherNames pins <SHORTCUT> to the launcher
// file name of every platform the harness can run on (harness desktopShortcutSpec:
// .command on darwin, .desktop on the other POSIX hosts, .lnk on windows). The
// replay itself only ever produces the host platform's name, so this guard keeps
// the shared corpus platform-independent for the names a runner cannot exercise.
func TestSurfaceShortcutTokenPlatformLauncherNames(t *testing.T) {
	normalizer := &Normalizer{Home: "/synthetic/home"}
	for _, name := range []string{
		"Rolling-Memory Console.command",
		"rolling-memory-console.desktop",
		"Rolling-Memory Console.lnk",
	} {
		got := normalizer.Text("/synthetic/home/Desktop/" + name)
		if want := "<HOME>/Desktop/<SHORTCUT>"; got != want {
			t.Fatalf("%s normalized to %q, want %q", name, got, want)
		}
	}
}
