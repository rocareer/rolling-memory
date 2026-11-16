// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package cli_test

import (
	"fmt"
	"strings"
	"testing"
)

// TestLongOptionAbbreviations mirrors CLI parser's allow_abbrev: an unambiguous
// prefix of a long option is accepted, including the "--opt=value" form.
// Expectations were read off the pinned reference parser.
func TestLongOptionAbbreviations(t *testing.T) {
	newEnv(t)
	seedTranscript(t)

	abbreviatedJSON := run(t, "recent", "--jso")
	if abbreviatedJSON.code != 0 {
		t.Fatalf("--jso exit %d: %s", abbreviatedJSON.code, abbreviatedJSON.stderr)
	}
	jsonArray(t, abbreviatedJSON.stdout)

	limit := run(t, "recent", "--li", "1", "--json")
	if limit.code != 0 {
		t.Fatalf("--li exit %d: %s", limit.code, limit.stderr)
	}
	if rows := jsonArray(t, limit.stdout); len(rows) > 1 {
		t.Fatalf("--li rows = %d, want <= 1", len(rows))
	}

	attached := run(t, "recent", "--lim=1", "--json")
	if attached.code != 0 {
		t.Fatalf("--lim=1 exit %d: %s", attached.code, attached.stderr)
	}
	if rows := jsonArray(t, attached.stdout); len(rows) > 1 {
		t.Fatalf("--lim=1 rows = %d, want <= 1", len(rows))
	}

	version := run(t, "--vers")
	if version.code != 0 || !strings.HasPrefix(version.stdout, "rolling-memory ") {
		t.Fatalf("--vers = %d %q", version.code, version.stdout)
	}

	help := run(t, "import", "--hel")
	if help.code != 0 || !strings.Contains(help.stdout, "usage: rolling-memory import") {
		t.Fatalf("--hel = %d %q", help.code, help.stdout)
	}

	// CLI parser rejects an explicit argument on the help action, abbreviated or
	// not (argument -h/--help: ignored explicit argument).
	helpAttached := run(t, "import", "--hel=x")
	if helpAttached.code != 2 ||
		!strings.Contains(helpAttached.stderr, "argument --help: ignored explicit argument 'x'") {
		t.Fatalf("--hel=x = %d %q", helpAttached.code, helpAttached.stderr)
	}

	// An exact-registered short name wins over any longer prefix (CLI parser checks
	// exact matches first)：`recent --limit` 的精确名 `--li` 见上，`--q` 是 recall 的精确名。
	exact := run(t, "recall", "--q", "老张", "--no-raw", "--json")
	if exact.code != 0 {
		t.Fatalf("--q exit %d: %s", exact.code, exact.stderr)
	}

	unknown := run(t, "recent", "--nope")
	if unknown.code != 2 || !strings.Contains(unknown.stderr, "unrecognized arguments: --nope") {
		t.Fatalf("--nope = %d %q", unknown.code, unknown.stderr)
	}
}

// TestAmbiguousAbbreviations pins CLI parser's message and exit code for a prefix
// that matches more than one option（改用 recall / sync 的现役歧义面）。
func TestAmbiguousAbbreviations(t *testing.T) {
	newEnv(t)
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"recall", "--r", "x"}, "ambiguous option: --r could match --reader, --result-mode, --raw-limit, --relative-tag, --result-unit, --rank"},
		{[]string{"recall", "--no"}, "ambiguous option: --no could match --nodes, --no-raw, --no-alias-expand, --no-mention-backfill, --no-rank, --no-crystallize"},
		{[]string{"sync", "reconcile", "--l"}, "ambiguous option: --l could match --layer, --limit"},
	}
	for _, testCase := range cases {
		result := run(t, testCase.args...)
		if result.code != 2 {
			t.Fatalf("%v exit = %d, want 2 (%s)", testCase.args, result.code, result.stderr)
		}
		if !strings.Contains(result.stderr, testCase.want) {
			t.Fatalf("%v stderr = %q, want %q", testCase.args, result.stderr, testCase.want)
		}
		if !strings.HasPrefix(result.stderr, "usage: rolling-memory ") {
			t.Fatalf("%v stderr = %q", testCase.args, result.stderr)
		}
	}
}

// TestAbbreviatedOptionErrorsNameTheFullOption pins that an abbreviated
// --opt=value reports the registered option, like CLI parser's ArgumentError.
func TestAbbreviatedOptionErrorsNameTheFullOption(t *testing.T) {
	newEnv(t)
	result := run(t, "recent", "--li", "x")
	if result.code != 2 || !strings.Contains(result.stderr, "argument --limit: invalid int value: 'x'") {
		t.Fatalf("--li x = %d %q", result.code, result.stderr)
	}
}

func TestRecallHelpReportsUnsupportedFilters(t *testing.T) {
	newEnv(t)
	result := run(t, "recall", "--help")
	if result.code != 0 {
		t.Fatalf("recall help exit %d: %s", result.code, result.stderr)
	}
	for _, name := range []string{"--session", "--asof", "--window-start", "--window-end"} {
		found := false
		for _, line := range strings.Split(result.stdout, "\n") {
			if strings.Contains(line, name+" ") && strings.Contains(line, "reserved; specifying this filter currently fails closed") {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("recall help does not disclose unsupported %s: %s", name, result.stdout)
		}
	}
}

// TestHelpMatchesArgparse pins the two CLI parser help rules the port had to
// adopt: a subparser has no description of its own (the root one is not
// repeated), and only subcommands with help= are listed under the choices.
func TestHelpMatchesArgparse(t *testing.T) {
	newEnv(t)
	root := run(t, "--help")
	if root.code != 0 {
		t.Fatalf("root help exit %d", root.code)
	}
	if !strings.HasPrefix(root.stdout, "usage: rolling-memory [-h] [--version] {") {
		t.Fatalf("root usage = %q", strings.SplitN(root.stdout, "\n", 2)[0])
	}
	// 只锁「根帮助带一句产品描述」这一形状（描述文案本身随产品口径演进，不在此断言全文）。
	if !strings.Contains(root.stdout, "Local-first ") {
		t.Fatalf("root help lost the description: %s", root.stdout)
	}
	for _, name := range []string{"init", "import", "sync", "install"} {
		if !strings.Contains(root.stdout, "\n    "+name+" ") {
			t.Fatalf("root help misses subcommand %q: %s", name, root.stdout)
		}
	}

	sub := run(t, "import", "--help")
	if sub.code != 0 {
		t.Fatalf("import help exit %d", sub.code)
	}
	if strings.Contains(sub.stdout, "Local-first graph memory for coding agents") {
		t.Fatalf("subcommand help repeats the root description: %s", sub.stdout)
	}
	if !strings.Contains(sub.stdout, "usage: rolling-memory import [-h]") {
		t.Fatalf("import usage = %q", strings.SplitN(sub.stdout, "\n", 2)[0])
	}
	if !strings.Contains(sub.stdout, "  --session SESSION") {
		t.Fatalf("import help misses its options: %s", sub.stdout)
	}

	// sync's subparsers carry no help=, so CLI parser lists only the subcommand set,
	// not one row per subcommand.
	sync := run(t, "sync", "--help")
	if sync.code != 0 {
		t.Fatalf("sync help exit %d", sync.code)
	}
	if !strings.Contains(sync.stdout, "usage: rolling-memory sync [-h] {status,reconcile,watch,prune} ...") {
		t.Fatalf("sync usage = %q", strings.SplitN(sync.stdout, "\n", 2)[0])
	}
	if strings.Contains(sync.stdout, "\n    status") || strings.Contains(sync.stdout, "\n    reconcile") {
		t.Fatalf("sync help lists help-less subcommands: %s", sync.stdout)
	}

	push := run(t, "sync", "reconcile", "--help")
	if push.code != 0 || !strings.Contains(push.stdout, "usage: rolling-memory sync reconcile [-h]") {
		t.Fatalf("sync reconcile help = %d %q", push.code, push.stdout)
	}
	if !strings.Contains(push.stdout, "--layer") {
		t.Fatalf("sync reconcile help misses --layer: %s", push.stdout)
	}

	importer := run(t, "import", "--help")
	if importer.code != 0 {
		t.Fatalf("import help exit %d", importer.code)
	}
	if !strings.HasPrefix(importer.stdout, "usage: rolling-memory import [-h]") {
		t.Fatalf("import usage = %q", strings.SplitN(importer.stdout, "\n", 2)[0])
	}
	if !strings.Contains(importer.stdout, "\n  path ") {
		t.Fatalf("import help misses its positional: %s", importer.stdout)
	}
	// The one-line help= only belongs to the parent's command list.
	for _, row := range []struct{ name, text string }{
		{"import", "import a transcript (JSONL or text)"},
		{"sync", "optional cloud sync (cloud mode only)"},
	} {
		want := "\n" + fmt.Sprintf("    %-18s %s", row.name, row.text) + "\n"
		if !strings.Contains(root.stdout, want) {
			t.Fatalf("root help misses %q: %s", want, root.stdout)
		}
	}
}
