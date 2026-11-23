package rollingmemory

import (
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
