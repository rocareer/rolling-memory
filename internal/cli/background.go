// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package cli

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/rocareer/rolling-memory/internal/api"
	"github.com/rocareer/rolling-memory/internal/mcp"
	"github.com/rocareer/rolling-memory/internal/thinclient"
)

// backgroundSkipNote renders the exclusion tail of a human summary line: how many files were
// skipped and why (aggregated by reason), plus unreadable directories. A sync that finished
// with skips must never read like a silent clean success; the full per-file list stays in `--json`.
func backgroundSkipNote(report thinclient.BackgroundSyncReport) string {
	var parts []string
	if len(report.SkippedFiles) > 0 {
		reasons := map[string]int{}
		for _, sk := range report.SkippedFiles {
			reasons[sk.Reason]++
		}
		keys := make([]string, 0, len(reasons))
		for reason := range reasons {
			keys = append(keys, reason)
		}
		sort.Slice(keys, func(i, j int) bool {
			if reasons[keys[i]] != reasons[keys[j]] {
				return reasons[keys[i]] > reasons[keys[j]]
			}
			return keys[i] < keys[j]
		})
		reasonParts := make([]string, 0, len(keys))
		for _, reason := range keys {
			reasonParts = append(reasonParts, fmt.Sprintf("%s: %d", reason, reasons[reason]))
		}
		label := "files"
		if len(report.SkippedFiles) == 1 {
			label = "file"
		}
		example := report.SkippedFiles[0].RelPath
		if strings.TrimSpace(example) == "" {
			example = report.SkippedFiles[0].AbsPath
		}
		parts = append(parts, fmt.Sprintf("skipped %d %s (%s; e.g. %s)",
			len(report.SkippedFiles), label, strings.Join(reasonParts, " · "), example))
	}
	if len(report.UnreadableDirs) > 0 {
		parts = append(parts, fmt.Sprintf("unreadable dirs %d (e.g. %s)", len(report.UnreadableDirs), report.UnreadableDirs[0]))
	}
	if len(parts) == 0 {
		return ""
	}
	return " · " + strings.Join(parts, " · ")
}

// runBackground exposes the canonical thinclient Background mount pipeline to
// hosts that cannot import internal/thinclient directly.
func (a *application) runBackground(memory *api.Memory) (int, error) {
	client, err := memory.ThinClient()
	if err != nil {
		return 1, err
	}
	ctx := context.Background()

	switch a.args.Subcommand() {
	case "add":
		mount, err := client.AddBackgroundDir(a.args.String("path"))
		if err != nil {
			return 1, err
		}
		report, err := client.SyncBackgroundDirs(ctx)
		if err != nil {
			return 1, err
		}
		if memory != nil && memory.Config != nil && mcp.ShouldUseUnixDaemon(memory.Config.DB) {
			mcp.InvalidateUnixDaemonCaches("")
		}
		result := map[string]any{"mount": mount, "sync": report}
		return 0, a.emit(result, func() {
			a.printf("background add: %s · scanned %d · ingested %d files / %d chunks%s\n",
				mount.RootPath, report.ScannedFiles, report.IngestedFiles, report.IngestedChunks, backgroundSkipNote(report))
			if mount.Notice != "" {
				a.printf("background add: %s\n", mount.Notice)
			}
		})
	case "sync":
		report, err := client.SyncBackgroundDirs(ctx)
		if err != nil {
			return 1, err
		}
		if memory != nil && memory.Config != nil && mcp.ShouldUseUnixDaemon(memory.Config.DB) {
			mcp.InvalidateUnixDaemonCaches("")
		}
		return 0, a.emit(report, func() {
			a.printf("background sync: scanned %d · unchanged %d · ingested %d files / %d chunks · deleted %d files / %d chunks%s\n",
				report.ScannedFiles, report.UnchangedFiles, report.IngestedFiles, report.IngestedChunks, report.DeletedFiles, report.DeletedChunks, backgroundSkipNote(report))
		})
	case "remove":
		report, err := client.RemoveBackgroundDirReport(ctx, a.args.String("path"))
		if err != nil {
			return 1, err
		}
		if memory != nil && memory.Config != nil && mcp.ShouldUseUnixDaemon(memory.Config.DB) {
			mcp.InvalidateUnixDaemonCaches("")
		}
		result := map[string]any{
			"root_path":        report.RootPath,
			"removed_chunks":   len(report.RemovedUnitIDs),
			"removed_unit_ids": report.RemovedUnitIDs,
			"kept_files":       report.ReboundFiles,
		}
		return 0, a.emit(result, func() {
			keptNote := ""
			if report.ReboundFiles > 0 {
				keptNote = fmt.Sprintf(" · kept %d files still covered by another mount", report.ReboundFiles)
			}
			a.printf("background remove: %s · purged %d chunks%s\n", report.RootPath, len(report.RemovedUnitIDs), keptNote)
		})
	case "list":
		mounts, err := client.Vault.ListBackgroundDirs()
		if err != nil {
			return 1, err
		}
		return 0, a.emit(mounts, func() {
			for _, mount := range mounts {
				a.printf("%s\t%s\t%d files\n", mount.DirID, mount.RootPath, mount.FileCount)
			}
		})
	default:
		return 1, fmt.Errorf("unknown background command %q", a.args.Subcommand())
	}
}
