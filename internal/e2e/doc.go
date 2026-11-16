// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

// Package e2e holds the process-level acceptance tests of the Go port.
//
// TestMain builds the real rolling-memory binary once; every test then drives
// it as a black box through one of its process faces (CLI, stdio MCP, harness
// hooks, install/skill registration) inside a throwaway HOME/XDG/
// ROLLING_MEMORY_* sandbox, so the live memory core and the host harness
// configs are never opened or written.
//
// The checks cover the CLI / MCP / hooks / install smoke and add
// the export→import roundtrip, the post-write SQLite integrity check and the
// cross-platform portability matrix.
package e2e
