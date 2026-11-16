// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package version

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var semverRE = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

func TestMetadata(t *testing.T) {
	if !semverRE.MatchString(Version) {
		t.Fatalf("Version = %q, want valid X.Y.Z semver", Version)
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", "VERSION"))
	if err != nil {
		t.Fatalf("read VERSION: %v", err)
	}
	if fileVer := strings.TrimSpace(string(raw)); fileVer != Version {
		t.Fatalf("VERSION file = %q, want Version %q (software version must track repository VERSION)", fileVer, Version)
	}
	if CoreVersion != "1.0.1" {
		t.Fatalf("CoreVersion = %q, want 1.0.1", CoreVersion)
	}
	if CoreCodename != "rame" {
		t.Fatalf("CoreCodename = %q, want rame", CoreCodename)
	}
	if SchemaVersion != 1 {
		t.Fatalf("SchemaVersion = %d", SchemaVersion)
	}
	if ExportFormat != "rolling-memory-export" || ExportVersion != 1 {
		t.Fatalf("export metadata = %q/%d", ExportFormat, ExportVersion)
	}
}
