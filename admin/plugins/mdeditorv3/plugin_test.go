// plugin: mdeditorv3 v1.0.6
package mdeditorv3

import (
	"slices"
	"testing"
)

func TestBuildInfo(t *testing.T) {
	got := buildInfo("0.1.0", "1.2.0")
	if got.Name != Name {
		t.Fatalf("Name = %q, want %q", got.Name, Name)
	}
	if got.Version != Version {
		t.Fatalf("Version = %q, want %q", got.Version, Version)
	}
	if got.Engine != "md-editor-v3" || got.EngineVersion != EngineVersion {
		t.Fatalf("Engine = %s@%s, want md-editor-v3@%s", got.Engine, got.EngineVersion, EngineVersion)
	}
	for _, capName := range []string{"markdown_parse", "markdown_edit", "catalog_toc", "image_upload"} {
		if !slices.Contains(got.Capabilities, capName) {
			t.Errorf("Capabilities missing %q: %v", capName, got.Capabilities)
		}
	}
	if got.HostVersion != "0.1.0" || got.HostAPIVersion != "1.2.0" {
		t.Errorf("host version mismatch: %q / %q", got.HostVersion, got.HostAPIVersion)
	}
}
