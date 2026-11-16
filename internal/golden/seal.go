// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

// Seal verification of the frozen corpus: it loads testdata/golden/MANIFEST.json,
// hashes every file the manifest lists, recomputes the seal, and flags regular
// files the manifest does not list.
package golden

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// runtimeScratch reports SQLite runtime sidecars (-wal/-shm). They are created
// on demand by any connection that opens a fixture writable and carry no corpus
// content, so neither sealing nor flagging them keeps the corpus reproducible.
func runtimeScratch(relative string) bool {
	return strings.HasSuffix(relative, ".db-wal") || strings.HasSuffix(relative, ".db-shm")
}

// Manifest is testdata/golden/MANIFEST.json: the provenance block, the per-file
// sha256 map and the seal. The generator block is carried verbatim for audit.
type Manifest struct {
	Files       map[string]string `json:"files"`
	SealSHA256  string            `json:"seal_sha256"`
	FrozenAt    int64             `json:"frozen_at"`
	FrozenAtISO string            `json:"frozen_at_iso"`
	Generator   ManifestGenerator `json:"generator"`
}

// ManifestGenerator is the recorded generation environment.
type ManifestGenerator struct {
	Python          string `json:"python"`
	PYVersion       string `json:"py_version"`
	UnicodeData     string `json:"unicodedata"`
	SQLite          string `json:"sqlite"`
	Commit          string `json:"commit"`
	SourceSHA256    string `json:"source_sha256"`
	GeneratorSHA256 string `json:"generator_sha256"`
	TZ              string `json:"tz"`
	LCAll           string `json:"lc_all"`
	FixedNow        int64  `json:"fixed_now"`
	Scope           string `json:"scope"`
	Command         string `json:"command"`
}

// LoadManifest decodes root/MANIFEST.json.
func LoadManifest(root string) (*Manifest, error) {
	raw, err := os.ReadFile(filepath.Join(root, "MANIFEST.json"))
	if err != nil {
		return nil, err
	}
	manifest := &Manifest{}
	if err := json.Unmarshal(raw, manifest); err != nil {
		return nil, fmt.Errorf("MANIFEST.json: %w", err)
	}
	if manifest.Files == nil {
		return nil, fmt.Errorf("MANIFEST.json: files must be an object")
	}
	return manifest, nil
}

// ManifestSeal recomputes seal_sha256 over a files map exactly as the oracle
// generator sealed it: sha256 over the concatenation of sorted "<relative
// path>\t<sha256>\n" entries (README §Seal). Entries are sorted by name; the
// tab and the trailing newline of every entry are part of the digest.
func ManifestSeal(files map[string]string) string {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	digest := sha256.New()
	for _, name := range names {
		fmt.Fprintf(digest, "%s\t%s\n", name, files[name])
	}
	return hex.EncodeToString(digest.Sum(nil))
}

// Verify checks root against the manifest: every listed file must exist with
// the recorded sha256, every regular file on disk except MANIFEST.json must be
// listed, and the seal must recompute to seal_sha256. Problems keep the oracle
// verify_manifest wording ("missing file", "hash mismatch", "seal mismatch")
// so a reseal diff reads the same in both eras; the seal diagnostic comes last
// like there. A read failure that is not "file not found" is reported as a
// problem instead of aborting, the Go stand-in for the oracle's crash.
func (m *Manifest) Verify(root string) ([]string, error) {
	problems := []string{}
	names := make([]string, 0, len(m.Files))
	for name := range m.Files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		actual, err := hashFile(filepath.Join(root, filepath.FromSlash(name)))
		if err != nil {
			if os.IsNotExist(err) {
				problems = append(problems, fmt.Sprintf("missing file: %s", name))
				continue
			}
			problems = append(problems, fmt.Sprintf("unreadable file: %s: %v", name, err))
			continue
		}
		if expected := m.Files[name]; actual != expected {
			problems = append(problems, fmt.Sprintf("hash mismatch: %s expected %s actual %s", name, expected, actual))
		}
	}

	// The generator sealed every file of its sweep except MANIFEST.json
	// (its rglob); anything else found here is drift the old --check caught by
	// comparing against a fresh regeneration.
	unlisted := []string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if relative == "MANIFEST.json" {
			return nil
		}
		if runtimeScratch(relative) {
			return nil
		}
		if !entry.Type().IsRegular() {
			// Follow symlinks like the oracle's Path.is_file(); other special
			// entries were not part of the sealed sweep either.
			info, statErr := os.Stat(path)
			if statErr != nil || !info.Mode().IsRegular() {
				return nil
			}
		}
		if _, ok := m.Files[relative]; !ok {
			unlisted = append(unlisted, relative)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(unlisted)
	for _, name := range unlisted {
		problems = append(problems, fmt.Sprintf("unlisted file: %s", name))
	}

	if actual := ManifestSeal(m.Files); actual != m.SealSHA256 {
		problems = append(problems, fmt.Sprintf("seal mismatch: expected %s actual %s", m.SealSHA256, actual))
	}
	return problems, nil
}

// hashFile returns the hex sha256 of one file.
func hashFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}
