package assets

import "embed"

// VersionFS 内嵌发布版本号（仓库根 VERSION，版本号唯一事实源；见 internal/version）。
// 单独一件嵌入，不改动既有 embed.go 的指令行。
//
//go:embed VERSION
var VersionFS embed.FS
