// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRecallCharsDefaultAndOverride(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	cfg, err := Load(LoadOptions{Path: &path, Env: map[string]string{}})
	if err != nil || cfg.Recall.Chars != 16000 {
		t.Fatalf("missing config chars = %v, err = %v", cfg, err)
	}
	if err := os.WriteFile(path, []byte("[recall]\nchars = 8000\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(LoadOptions{Path: &path, Env: map[string]string{}})
	if err != nil || cfg.Recall.Chars != 8000 {
		t.Fatalf("explicit chars = %v, err = %v", cfg, err)
	}
}
