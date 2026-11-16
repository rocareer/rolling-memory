// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

// Episodic unit rebuild reports a non-destructive estimate of segment units.
// The harness scan cannot prove source completeness, so applying the rebuild is
// disabled rather than treating unscanned or unavailable sources as deleted.
package cli

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/rocareer/rolling-memory/internal/api"
	"github.com/rocareer/rolling-memory/internal/tokenzr"
)

// episodicRebuildScanLimit bounds the per-harness dry-run estimate.
const episodicRebuildScanLimit = 20000

// EpisodicRebuildReport is the machine-readable receipt of a rebuild pass.
type EpisodicRebuildReport struct {
	Mode         string   `json:"mode"` // dry_run
	OldUnits     int      `json:"old_units"`
	NewUnits     int      `json:"new_units"`
	UnitsAdded   int      `json:"units_added"`
	UnitsRemoved int      `json:"units_removed"`
	UnitsReused  int      `json:"units_reused"`
	OldTokens    int64    `json:"old_tokens"`
	NewTokens    int64    `json:"new_tokens"`
	UnitsPushed  int      `json:"units_pushed"`
	RemotePurged int      `json:"remote_purged"`
	BackupPath   string   `json:"backup_path,omitempty"`
	Warnings     []string `json:"warnings,omitempty"`
}

// rebuildEpisodicUnits estimates segment units without modifying the corpus.
func rebuildEpisodicUnits(ctx context.Context, memory *api.Memory, apply bool) (EpisodicRebuildReport, error) {
	report := EpisodicRebuildReport{Mode: "dry_run"}
	if memory == nil || memory.Store == nil || memory.Store.DB() == nil {
		return report, fmt.Errorf("episodic rebuild requires an open local store")
	}
	if apply {
		return report, fmt.Errorf("episodic rebuild apply disabled: harness scan completeness cannot be verified and the previous backup contained only IDs; no data was deleted")
	}
	report.Warnings = append(report.Warnings, "incomplete scan estimate only: unscanned or unavailable sources are not evidence of deletion; apply is disabled")
	db := memory.Store.DB()
	scope := memory.Config.Scope

	oldRows, err := db.Query(`SELECT doc_id, occurred_at, raw_text FROM thin_vault_docs WHERE layer = 'episodic' OR layer = ''`)
	if err != nil {
		return report, err
	}
	oldIDs := map[string]int64{}
	for oldRows.Next() {
		var docID, rawText string
		var occurredAt int64
		if err := oldRows.Scan(&docID, &occurredAt, &rawText); err != nil {
			oldRows.Close()
			return report, fmt.Errorf("scan episodic unit: %w", err)
		}
		if docID == "" {
			oldRows.Close()
			return report, fmt.Errorf("scan episodic unit: empty document ID")
		}
		oldIDs[docID] = occurredAt
		if !strings.HasPrefix(strings.TrimSpace(rawText), "@ref:") {
			report.OldTokens += int64(tokenzr.Count(rawText))
		}
	}
	if err := oldRows.Err(); err != nil {
		oldRows.Close()
		return report, fmt.Errorf("read episodic units: %w", err)
	}
	if err := oldRows.Close(); err != nil {
		return report, fmt.Errorf("close episodic units: %w", err)
	}
	report.OldUnits = len(oldIDs)

	home, _ := os.UserHomeDir()
	newUnits := map[string]candidateEpisodicTurn{}
	for _, harnessID := range []string{"zcode", "claude-code", "codex", "dsh"} {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		_, turns, _ := scanHarnessTurns(home, harnessID, scope, episodicRebuildScanLimit)
		for _, turn := range turns {
			if !turn.Completed || strings.TrimSpace(turn.Text) == "" {
				continue
			}
			if _, exists := newUnits[turn.DocID]; exists {
				continue
			}
			newUnits[turn.DocID] = turn
			report.NewTokens += int64(tokenzr.Count(turn.Text))
		}
	}
	report.NewUnits = len(newUnits)

	for docID := range newUnits {
		if _, exists := oldIDs[docID]; exists {
			report.UnitsReused++
		} else {
			report.UnitsAdded++
		}
	}
	for docID := range oldIDs {
		if _, exists := newUnits[docID]; !exists {
			report.UnitsRemoved++
		}
	}
	return report, nil
}
