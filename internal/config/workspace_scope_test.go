// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestWorkspaceScopeNameDerivation pins the per-workspace scope derivation:
// the ROLLING_MEMORY_LOCUS override wins, otherwise the cwd basename; unusable
// names fall back to "" so callers keep their existing scope.
func TestWorkspaceScopeNameDerivation(t *testing.T) {
	cases := []struct {
		override, wd, want string
	}{
		{"Rolling-Memory", "/srv/workspaces/other-workspace", "Rolling-Memory"},
		{"", "/srv/workspaces/Rolling-Memory", "Rolling-Memory"},
		{"", "/srv/workspaces/Rolling-Memory/", "Rolling-Memory"},
		{"", "/", ""},
		{"", "", ""},
		{"", ".", ""},
		{"a:b", "/tmp/x", "a-b"},
		{"bad name", "/tmp/x", ""},
	}
	for _, tc := range cases {
		if got := WorkspaceScopeName(tc.override, tc.wd); got != tc.want {
			t.Fatalf("WorkspaceScopeName(%q, %q) = %q, want %q", tc.override, tc.wd, got, tc.want)
		}
	}
}

// TestLoadScopeFromWorkspaceSwitch pins [cloud.sync] scope_from_workspace:
// default scopes derive from the workspace, explicit scopes win unchanged.
func TestLoadScopeFromWorkspaceSwitch(t *testing.T) {
	isolateHome(t)
	tmp := t.TempDir()
	env := map[string]string{"ROLLING_MEMORY_LOCUS": "Rolling-Memory"}

	onPath := filepath.Join(tmp, "on.toml")
	writeConfig(t, onPath, `[core]
db = "`+filepath.Join(tmp, "on.db")+`"
scope = "mem:self"

[cloud.sync]
scope_from_workspace = true
`)
	cfg, err := Load(LoadOptions{Path: &onPath, Env: env})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Cloud.ScopeFromWorkspace {
		t.Fatalf("ScopeFromWorkspace = false, want true")
	}
	if cfg.Scope != "Rolling-Memory" {
		t.Fatalf("derived scope = %q, want Rolling-Memory", cfg.Scope)
	}

	explicitPath := filepath.Join(tmp, "explicit.toml")
	writeConfig(t, explicitPath, `[core]
db = "`+filepath.Join(tmp, "explicit.db")+`"
scope = "mem:task:demo"

[cloud.sync]
scope_from_workspace = true
`)
	cfg, err = Load(LoadOptions{Path: &explicitPath, Env: env})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Scope != "mem:task:demo" {
		t.Fatalf("explicit scope = %q, want mem:task:demo", cfg.Scope)
	}

	override := "cli-override"
	cfg, err = Load(LoadOptions{Path: &onPath, Scope: &override, Env: env})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Scope != override {
		t.Fatalf("cli scope override = %q, want %q", cfg.Scope, override)
	}

	offPath := filepath.Join(tmp, "off.toml")
	writeConfig(t, offPath, `[core]
db = "`+filepath.Join(tmp, "off.db")+`"
scope = "mem:self"
`)
	cfg, err = Load(LoadOptions{Path: &offPath, Env: env})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Scope != "mem:self" {
		t.Fatalf("scope with switch off = %q, want mem:self", cfg.Scope)
	}

	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	cfg, err = Load(LoadOptions{Path: &onPath, Env: map[string]string{}})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if want := filepath.Base(wd); cfg.Scope != want {
		t.Fatalf("scope without LOCUS override = %q, want cwd basename %q", cfg.Scope, want)
	}
}
