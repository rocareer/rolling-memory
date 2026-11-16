// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

// Verification of the two activation ledger config keys
// (recall.activation_record, default true; recall.activation_rank,
// default false) across the template / parse / serialize faces.
package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestRecallActivationKeys(t *testing.T) {
	isolateHome(t)
	if !strings.Contains(DefaultConfigText, "activation_record = true") ||
		!strings.Contains(DefaultConfigText, "activation_rank = false") {
		t.Fatal("DefaultConfigText is missing the activation keys")
	}

	missing := filepath.Join(t.TempDir(), "missing.toml")
	cfg, err := Load(LoadOptions{Path: &missing, Env: map[string]string{}})
	if err != nil {
		t.Fatalf("Load defaults: %v", err)
	}
	if !cfg.Recall.ActivationRecord || cfg.Recall.ActivationRank {
		t.Fatalf("activation defaults = %v/%v, want true/false",
			cfg.Recall.ActivationRecord, cfg.Recall.ActivationRank)
	}
	recallTable, ok := cfg.PublicDict()["recall"].(map[string]any)
	if !ok {
		t.Fatalf("PublicDict recall = %#v", cfg.PublicDict()["recall"])
	}
	if recallTable["activation_record"] != true || recallTable["activation_rank"] != false {
		t.Fatalf("serialized activation keys = %v/%v",
			recallTable["activation_record"], recallTable["activation_rank"])
	}

	path := filepath.Join(t.TempDir(), "config.toml")
	writeConfig(t, path, `[recall]
activation_record = "off"
activation_rank = 1
`)
	parsed, err := Load(LoadOptions{Path: &path, Env: map[string]string{}})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if parsed.Recall.ActivationRecord || !parsed.Recall.ActivationRank {
		t.Fatalf("parsed activation keys = %v/%v, want false/true",
			parsed.Recall.ActivationRecord, parsed.Recall.ActivationRank)
	}
}
