// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package cluster

import (
	"go/ast"
	"go/build"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

// TestPortableCoreTypechecksWithoutAdapters compiles exactly the files intended
// for copying, using the standard-library importer and no repository packages.
func TestPortableCoreTypechecksWithoutAdapters(t *testing.T) {
	if build.Default.GOROOT == "" {
		if out, err := exec.Command("go", "env", "GOROOT").Output(); err == nil {
			build.Default.GOROOT = strings.TrimSpace(string(out))
		}
	}
	fset := token.NewFileSet()
	files := []string{
		"index.go",
		"pipeline.go",
		"pipeline_state.go",
		"pipeline_cluster.go",
		"pipeline_select.go",
		"unit_relations.go",
	}
	parsed := make([]*ast.File, 0, len(files))
	for _, name := range files {
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, spec := range file.Imports {
			path, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			if path != "math" && path != "sort" {
				t.Fatalf("portable core %s imports %q", name, path)
			}
		}
		parsed = append(parsed, file)
	}
	conf := types.Config{Importer: importer.Default()}
	if _, err := conf.Check("portable-rm", fset, parsed, nil); err != nil {
		t.Fatalf("portable core typecheck: %v", err)
	}
}
