// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package golden

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	previous := time.Local
	time.Local = time.UTC
	code := m.Run()
	time.Local = previous
	os.Exit(code)
}

func fixtureScenarios(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(GoldenRoot(), "fixtures"))
	if err != nil {
		t.Fatalf("read fixtures: %v", err)
	}
	names := []string{}
	for _, entry := range entries {
		if entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		t.Fatal("no golden fixtures found")
	}
	return names
}

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("repo root: %v", err)
	}
	return root
}

func TestGoldenReplay(t *testing.T) {
	for _, scenario := range fixtureScenarios(t) {
		scenario := scenario
		t.Run(scenario, func(t *testing.T) {
			restore := ReplayModeFreeze()
			defer restore()
			fixture, err := LoadFixture(filepath.Join(GoldenRoot(), "fixtures", scenario, "fixture.json"))
			if err != nil {
				t.Fatalf("load fixture: %v", err)
			}
			runner, err := NewRunner(fixture, t.TempDir(), repoRoot(t))
			if err != nil {
				t.Fatalf("runner: %v", err)
			}
			defer runner.Close()
			if err := runner.OpenFresh(); err != nil {
				t.Fatalf("open: %v", err)
			}
			if err := runner.RunOps(nil); err != nil {
				t.Fatalf("run ops: %v", err)
			}
			compared, err := runner.CompareProduced()
			if err != nil {
				t.Fatalf("compare: %v", err)
			}
			if compared == 0 {
				t.Fatal("no expectation files compared")
			}
			missing := []string{}
			for name := range fixture.Expected {
				if name == "rows.json" {
					continue
				}
				if _, ok := runner.Produced[name]; ok {
					continue
				}
				if _, ok := runner.ProducedText[name]; ok {
					continue
				}
				missing = append(missing, name)
			}
			sort.Strings(missing)
			if len(missing) > 0 {
				t.Fatalf("missing produced expectations: %s", strings.Join(missing, ", "))
			}
			if _, err := runner.CompareRows(); err != nil {
				t.Fatalf("rows: %v", err)
			}
			if err := runner.Snapshot(); err != nil {
				t.Fatalf("snapshot: %v", err)
			}
		})
	}
}

// readOperations lists fixture ops that only read (they leave the sealed
// database untouched), so TestGoldenFrozenReadPaths can run them on a copy.
// The config/egress probes of scenario 250 belong here: they load
// configuration, evaluate the egress policy and resolve secrets without
// touching the graph.
var readOperations = map[string]struct{}{
	"recall": {}, "status": {}, "check": {}, "doctor": {}, "health": {},
	"why": {}, "changed": {}, "portrait": {}, "govern": {}, "nodes": {},
	"conflicts": {}, "node_by_label": {}, "export": {}, "closure": {},
	"dump": {}, "expect_error": {},
	"config_load": {}, "config_egress": {}, "config_secret": {},
}

func TestGoldenFrozenReadPaths(t *testing.T) {
	for _, scenario := range fixtureScenarios(t) {
		scenario := scenario
		t.Run(scenario, func(t *testing.T) {
			restore := ReplayModeFreeze()
			defer restore()
			fixture, err := LoadFixture(filepath.Join(GoldenRoot(), "fixtures", scenario, "fixture.json"))
			if err != nil {
				t.Fatalf("load fixture: %v", err)
			}
			capture, err := NewRunner(fixture, t.TempDir(), repoRoot(t))
			if err != nil {
				t.Fatalf("capture runner: %v", err)
			}
			if err := capture.OpenFresh(); err != nil {
				capture.Close()
				t.Fatalf("capture open: %v", err)
			}
			// Replay once in a scratch database to resolve $step references;
			// scenarios with no fixture writes still leave a usable empty map.
			_ = capture.RunOps(nil)
			capture.Close()

			runner, err := NewRunner(fixture, t.TempDir(), repoRoot(t))
			if err != nil {
				t.Fatalf("runner: %v", err)
			}
			defer runner.Close()
			runner.Saved = capture.Saved
			if err := runner.OpenCopy(filepath.Join(GoldenRoot(), fixture.DB)); err != nil {
				t.Fatalf("open copy: %v", err)
			}
			// A recall on the sealed copy records its delivered hits in the
			// activation ledger, so that one table is mutated by design:
			// hold the sealed rows and write them back before the snapshot.
			ledger, err := runner.captureTable("activation")
			if err != nil {
				t.Fatalf("capture activation: %v", err)
			}
			lastWrite := -1
			for index, op := range fixture.Ops {
				if _, ok := readOperations[op.Op]; !ok {
					lastWrite = index
				}
			}
			if err := runner.RunOps(func(index int, op Op) bool {
				if _, ok := readOperations[op.Op]; !ok {
					return false
				}
				return index > lastWrite
			}); err != nil {
				t.Fatalf("run read ops: %v", err)
			}
			compared, err := runner.CompareProduced()
			if err != nil {
				t.Fatalf("compare: %v", err)
			}
			if compared == 0 {
				// A scenario whose only expectations are writes has nothing to
				// check against the sealed read state.
				if lastWrite < len(fixture.Ops)-1 {
					t.Fatal("no expectation files compared")
				}
				return
			}
			if _, err := runner.CompareRows(); err != nil {
				t.Fatalf("rows: %v", err)
			}
			if err := runner.restoreTable("activation", ledger); err != nil {
				t.Fatalf("restore activation: %v", err)
			}
			if err := runner.Snapshot(); err != nil {
				t.Fatalf("snapshot: %v", err)
			}
		})
	}
}
