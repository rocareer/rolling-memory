// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

// Package rollingmemory embeds the shipped skill editions for standalone installs.
package rollingmemory

import "embed"

//go:embed skills/rolling-memory/SKILL.md skills/rolling-memory/references/SKILL.en.md skills/rolling-memory/references/SKILL.zh-CN.md
var Skills embed.FS
