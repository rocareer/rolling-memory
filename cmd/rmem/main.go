// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

// Command rmem is the short alias for the rolling-memory CLI; both entry
// points delegate to internal/cli.
package main

import (
	"os"

	"github.com/rocareer/rolling-memory/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
