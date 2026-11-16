// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package golden

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGoldenManifestSeal is the seal check of the frozen corpus:
// it loads the committed MANIFEST, hashes every listed file, sweeps for
// unlisted files and recomputes the seal.
func TestGoldenManifestSeal(t *testing.T) {
	root := GoldenRoot()
	manifest, err := LoadManifest(root)
	if err != nil {
		t.Fatalf("load manifest: %v", err)
	}
	if len(manifest.Files) == 0 {
		t.Fatal("manifest lists no files")
	}
	if manifest.SealSHA256 == "" {
		t.Fatal("manifest records no seal_sha256")
	}
	if os.Getenv("RM_RESEAL_GOLDEN_MANIFEST") == "1" {
		for name := range manifest.Files {
			digest, hashErr := hashFile(filepath.Join(root, filepath.FromSlash(name)))
			if hashErr != nil {
				t.Fatalf("hash %s: %v", name, hashErr)
			}
			manifest.Files[name] = digest
		}
		manifest.SealSHA256 = ManifestSeal(manifest.Files)
		manifestPath := filepath.Join(root, "MANIFEST.json")
		info, statErr := os.Stat(manifestPath)
		if statErr == nil {
			_ = os.Chmod(manifestPath, info.Mode()|0o200)
		}
		encoded, marshalErr := json.MarshalIndent(manifest, "", "  ")
		if marshalErr != nil {
			t.Fatalf("marshal manifest: %v", marshalErr)
		}
		if writeErr := os.WriteFile(manifestPath, append(encoded, '\n'), 0o644); writeErr != nil {
			t.Fatalf("write manifest: %v", writeErr)
		}
		if statErr == nil {
			_ = os.Chmod(manifestPath, info.Mode())
		}
	}
	problems, err := manifest.Verify(root)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if len(problems) > 0 {
		t.Fatalf("manifest seal check failed:\n  %s", strings.Join(problems, "\n  "))
	}
	t.Logf("seal verified: %d files, seal_sha256 %s", len(manifest.Files), manifest.SealSHA256)
}

// TestGoldenSealFormula pins the seal formula on synthetic entries: sha256
// over the concatenation of sorted "<name>\t<sha256>\n" entries with the tab
// and trailing newline included (the oracle generator's own formula, README
// §Seal). The expected digests were computed outside this test with that
// formula over the very maps below.
func TestGoldenSealFormula(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
		want  string
	}{
		{
			// Insertion order differs from sort order: "a/c.txt" seals first.
			name:  "sorted entries",
			files: map[string]string{"b.txt": strings.Repeat("a", 64), "a/c.txt": strings.Repeat("0", 64)},
			want:  "898dca2bb187e1c92a800204ada575ab798c24978b3c618f5b2f17203446d681",
		},
		{
			name:  "single entry",
			files: map[string]string{"x": strings.Repeat("f", 64)},
			want:  "123d1d946878b2d9da91ecefbc425cc61a172a90bc169e7df1a62c9ca5523e42",
		},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			if got := ManifestSeal(testCase.files); got != testCase.want {
				t.Fatalf("seal mismatch: got %s want %s", got, testCase.want)
			}
		})
	}
}

// TestGoldenManifestProblems drives Verify through the drift it must catch,
// on a synthetic tree (synthetic names and contents only). It pins the
// oracle's problem wording plus the unlisted-file arm that replaces the
// deleted oracle's regeneration comparison.
func TestGoldenManifestProblems(t *testing.T) {
	root := t.TempDir()
	writeSyntheticGolden(t, root, map[string]string{
		"data/one.txt": "one\n",
		"data/two.txt": "two\n",
	})

	// A clean tree verifies.
	if problems := verifySynthetic(t, root); len(problems) != 0 {
		t.Fatalf("clean tree reported problems: %v", problems)
	}

	// A byte-flipped file is a hash mismatch.
	writeSyntheticFile(t, root, "data/two.txt", "TWO\n")
	if problems := verifySynthetic(t, root); !hasProblem(problems, "hash mismatch: data/two.txt") {
		t.Fatalf("tampered file not reported: %v", problems)
	}
	writeSyntheticFile(t, root, "data/two.txt", "two\n")

	// A lost file is a missing entry.
	if err := os.Remove(filepath.Join(root, "data", "one.txt")); err != nil {
		t.Fatalf("remove one.txt: %v", err)
	}
	if problems := verifySynthetic(t, root); !hasProblem(problems, "missing file: data/one.txt") {
		t.Fatalf("missing file not reported: %v", problems)
	}
	writeSyntheticFile(t, root, "data/one.txt", "one\n")

	// A file the manifest never listed is drift.
	writeSyntheticFile(t, root, "data/extra.txt", "extra\n")
	if problems := verifySynthetic(t, root); !hasProblem(problems, "unlisted file: data/extra.txt") {
		t.Fatalf("unlisted file not reported: %v", problems)
	}
	if err := os.Remove(filepath.Join(root, "data", "extra.txt")); err != nil {
		t.Fatalf("remove extra.txt: %v", err)
	}

	// A stale seal fails even with every file intact: the recorded seal is
	// the one contract a local edit cannot update silently.
	files := map[string]string{
		"data/one.txt": fmt.Sprintf("%x", sha256.Sum256([]byte("one\n"))),
		"data/two.txt": fmt.Sprintf("%x", sha256.Sum256([]byte("two\n"))),
	}
	writeSyntheticManifest(t, root, files, strings.Repeat("0", 64))
	want := "seal mismatch: expected " + strings.Repeat("0", 64) + " actual " + ManifestSeal(files)
	if problems := verifySynthetic(t, root); !hasProblem(problems, want) {
		t.Fatalf("stale seal not reported: %v", problems)
	}

	// Recovery: a resealed manifest verifies again.
	writeSyntheticGolden(t, root, map[string]string{
		"data/one.txt": "one\n",
		"data/two.txt": "two\n",
	})
	if problems := verifySynthetic(t, root); len(problems) != 0 {
		t.Fatalf("resealed tree reported problems: %v", problems)
	}

	// Without a files map the manifest cannot be verified at all.
	writeSyntheticFile(t, root, "MANIFEST.json", "{\"seal_sha256\": \"x\"}\n")
	if _, err := LoadManifest(root); err == nil {
		t.Fatal("manifest without a files map must fail to load")
	}
}

// writeSyntheticGolden lays down synthetic corpus files plus their MANIFEST,
// with hashes computed directly (crypto/sha256, not hashFile) and the genuine
// seal over them.
func writeSyntheticGolden(t *testing.T, root string, contents map[string]string) {
	t.Helper()
	files := make(map[string]string, len(contents))
	for name, text := range contents {
		writeSyntheticFile(t, root, name, text)
		files[name] = fmt.Sprintf("%x", sha256.Sum256([]byte(text)))
	}
	writeSyntheticManifest(t, root, files, ManifestSeal(files))
}

// writeSyntheticManifest writes one MANIFEST.json with the given files map and
// seal value.
func writeSyntheticManifest(t *testing.T, root string, files map[string]string, seal string) {
	t.Helper()
	manifest := map[string]any{
		"files":         files,
		"seal_sha256":   seal,
		"frozen_at":     FixedNow,
		"frozen_at_iso": "2026-10-27T12:00:00Z",
		"generator":     map[string]any{"command": "synthetic", "scope": "seal_test.go"},
	}
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatalf("marshal synthetic manifest: %v", err)
	}
	writeSyntheticFile(t, root, "MANIFEST.json", string(raw)+"\n")
}

// writeSyntheticFile writes one file below root, creating parent directories.
func writeSyntheticFile(t *testing.T, root, name, text string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", name, err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// verifySynthetic loads and verifies the manifest under root, failing the test
// on load or walk errors.
func verifySynthetic(t *testing.T, root string) []string {
	t.Helper()
	manifest, err := LoadManifest(root)
	if err != nil {
		t.Fatalf("load manifest: %v", err)
	}
	problems, err := manifest.Verify(root)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	return problems
}

// hasProblem reports whether any problem contains want.
func hasProblem(problems []string, want string) bool {
	for _, problem := range problems {
		if strings.Contains(problem, want) {
			return true
		}
	}
	return false
}
