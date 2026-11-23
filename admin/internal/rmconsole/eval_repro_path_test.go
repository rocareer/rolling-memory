package rollingmemory

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPortableEvalStoreCommands(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	for _, tc := range []struct{ name, path, want string }{
		{"native home", filepath.Join(home, ".local", "eval.json"), "~/.local/eval.json"},
		{"home itself", home, "~"},
		{"home with spaces", filepath.Join(home, "eval files", "store.json"), "~/'eval files/store.json'"},
		{"relative with spaces", filepath.Join("eval files", "data.json"), "'eval files/data.json'"},
		{"apostrophe", filepath.Join(home, "reader's copy.json"), "~/'reader'\\''s copy.json'"},
		{"abbreviated home", "~/eval files/data.json", "~/'eval files/data.json'"},
		{"empty argument", "", "''"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, cmd := range []string{
				buildReproCLICommand("200", EvalArmSDKLocalPlain, tc.path, 20, false),
				buildATMReproCLICommand("hard", EvalArmSDKLocalPlain, tc.path, 50, false),
			} {
				if !strings.Contains(cmd, "--eval-store "+tc.want+" ") {
					t.Errorf("wrong store in %q", cmd)
				}
			}
		})
	}
	sibling := filepath.Join(home+"-other", "eval.json")
	if got := portableCLIStorePath(sibling); strings.HasPrefix(got, "~") {
		t.Errorf("folded a sibling of the home directory: %q", got)
	}
}

// Exercise the argument boundary in the shell used by the Markdown export.
func TestEvalStoreArgumentInBash(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("Bash is required to check the exported command")
	}
	const store = "eval files/reader's $HOME; notes.json"
	for _, command := range []string{
		buildReproCLICommand("1540", EvalArmSDKLocalPlain, store, 20, false),
		buildATMReproCLICommand("hard", EvalArmSDKLocalPlain, store, 50, false),
	} {
		// Stand in for rmem so this test only observes argv and never runs an eval.
		script := "rmem() { printf '%s\\0' \"$@\"; }\n" + command
		output, err := exec.Command(bash, "--noprofile", "--norc", "-c", script).CombinedOutput()
		if err != nil {
			t.Fatalf("Bash: %v: %s", err, output)
		}
		args := strings.Split(strings.TrimSuffix(string(output), "\x00"), "\x00")
		stores := 0
		for i, arg := range args {
			if arg == "--eval-store" {
				stores++
				if i+1 >= len(args) || args[i+1] != store {
					t.Fatalf("expected one literal store argument %q in %q", store, args)
				}
			}
		}
		if stores != 1 {
			t.Errorf("expected exactly one --eval-store flag in %q", args)
		}
	}
}
