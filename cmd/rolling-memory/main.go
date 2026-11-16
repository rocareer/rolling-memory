// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

// Command rolling-memory is the primary CLI entry point (`rmem` is the short
// alias delegating to the same implementation).
package main

import (
	"os"

	"github.com/rocareer/rolling-memory/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
