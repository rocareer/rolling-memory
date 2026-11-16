// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package golden

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// TestResealGoldenManifest recomputes MANIFEST.json for the on-disk corpus. It runs only under
// RESEAL_GOLDEN=1 and is the second half of the maintenance path: UPDATE_GOLDEN=1 regenerates the
// expectations of a deliberately changed delivery surface, then this reseals the per-file hashes and
// the seal so the corpus stays tamper-evident. Without the env var it is a no-op, so an accidental
// `go test` never re-signs drift.
func TestResealGoldenManifest(t *testing.T) {
	if os.Getenv("RESEAL_GOLDEN") != "1" {
		t.Skip("set RESEAL_GOLDEN=1 to reseal the frozen corpus")
	}
	root := GoldenRoot()
	manifestPath := filepath.Join(root, "MANIFEST.json")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var manifest Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}

	files := map[string]string{}
	err = filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			return nil
		}
		relative, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		relative = filepath.ToSlash(relative)
		if relative == "MANIFEST.json" {
			return nil
		}
		if runtimeScratch(relative) {
			// SQLite runtime sidecars are regenerated on demand; sealing them
			// would break clones and every later replay.
			return nil
		}
		digest, hashErr := fileSHA256(path)
		if hashErr != nil {
			return hashErr
		}
		files[relative] = digest
		return nil
	})
	if err != nil {
		t.Fatalf("walk corpus: %v", err)
	}

	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	changed := 0
	for _, name := range names {
		if manifest.Files[name] != files[name] {
			changed++
		}
	}
	for name := range manifest.Files {
		if _, ok := files[name]; !ok {
			changed++
		}
	}

	manifest.Files = files
	manifest.SealSHA256 = ManifestSeal(files)
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatalf("encode manifest: %v", err)
	}
	if err := os.WriteFile(manifestPath, append(encoded, '\n'), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	t.Logf("resealed %d files (%d listings changed), seal=%s", len(files), changed, manifest.SealSHA256)
}

// fileSHA256 hashes one corpus file exactly as Manifest.Verify does.
func fileSHA256(path string) (string, error) {
	handle, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer handle.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, handle); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}
