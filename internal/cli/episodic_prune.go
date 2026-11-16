// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

// Episodic unit prune retires the pointer-indexed units whose content is provably
// no longer part of their turn. This is the maintenance counterpart of the
// segmentation filter: when the filter evolves (for example excluding the agent's
// tool-step narration), older imports keep `@ref:` rows that the current
// enumeration no longer yields, and the console can only render them as
// "来源缺口". A row is removed only when the source turn enumerates successfully
// AND no segment matches the recorded content digest — rows that still resolve and
// rows whose source is unavailable are kept (fail-safe), and every removal is
// recorded in a receipt file before the sweep reports.
package cli

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rocareer/rolling-memory/internal/api"
	"github.com/rocareer/rolling-memory/internal/config"
	"github.com/rocareer/rolling-memory/internal/ids"
	"github.com/rocareer/rolling-memory/internal/mcp"
	"github.com/rocareer/rolling-memory/internal/thinclient"
)

// episodicPruneScanLimit bounds one prune pass over the vault's pointer rows.
const episodicPruneScanLimit = 20000

// EpisodicPruneReceipt is one removed unit's record inside the receipt file.
type EpisodicPruneReceipt struct {
	DocID      string `json:"doc_id"`
	RefPointer string `json:"ref_pointer"`
	RemovedAt  int64  `json:"removed_at"`
}

// EpisodicPruneReport is the machine-readable receipt of one prune pass.
type EpisodicPruneReport struct {
	Mode          string   `json:"mode"` // dry_run | apply
	Scanned       int      `json:"scanned"`
	Verified      int      `json:"verified"`
	Relocatable   int      `json:"relocatable"`
	Absent        int      `json:"absent"`
	Unreadable    int      `json:"unreadable"`
	Removed       int      `json:"removed"`
	Kept          int      `json:"kept"`
	NoeticRemoved int      `json:"noetic_removed,omitempty"`
	LocusHealed   int      `json:"locus_healed,omitempty"`
	Limit         int      `json:"limit"`
	Receipt       string   `json:"receipt,omitempty"`
	Candidates    []string `json:"candidates,omitempty"`
	Warnings      []string `json:"warnings,omitempty"`
}

// pruneEpisodicUnits retires episodic rows whose content the current turn
// enumeration can no longer produce (and, when includeUnreadable is set, pointer
// rows whose underlying transcript or session is gone). Dry-run by default; apply
// requires --yes and also heals relocatable or unindexed pointers in-place.
func pruneEpisodicUnits(ctx context.Context, memory *api.Memory, limit int, apply, confirmed bool, includeUnreadable ...bool) (EpisodicPruneReport, error) {
	pruneUnreadable := len(includeUnreadable) > 0 && includeUnreadable[0]
	report := EpisodicPruneReport{Mode: "dry_run", Limit: limit}
	if memory == nil || memory.Store == nil || memory.Store.DB() == nil {
		return report, errors.New("episodic prune requires an open local store")
	}
	if limit <= 0 || limit > episodicPruneScanLimit {
		limit = episodicPruneScanLimit
		report.Limit = limit
	}
	if apply && !confirmed {
		return report, errors.New("episodic prune --apply requires --yes")
	}
	scope := config.DefaultScope
	if memory.Config != nil && strings.TrimSpace(memory.Config.Scope) != "" {
		scope = strings.TrimSpace(memory.Config.Scope)
	}
	tc, err := memory.ThinClient()
	if err != nil {
		return report, err
	}
	if tc == nil || tc.Vault == nil {
		return report, errors.New("episodic prune requires a local vault")
	}
	db := memory.Store.DB()
	// Materialize the episodic rows before classifying: the classifier issues its own
	// queries, and a live Rows iterator would hold the single vault connection.
	rows, err := db.QueryContext(ctx, `
		SELECT d.doc_id, d.raw_text, d.occurred_at
		FROM thin_vault_docs d
		WHERE (d.layer = 'episodic' OR d.layer = '')
		ORDER BY d.doc_id
		LIMIT ?`, limit)
	if err != nil {
		return report, err
	}
	type candidate struct {
		docID      string
		refPointer string
		occurredAt int64
	}
	var candidates []candidate
	for rows.Next() {
		var docID, refPointer string
		var occurredAt int64
		if err := rows.Scan(&docID, &refPointer, &occurredAt); err != nil {
			rows.Close()
			return report, err
		}
		candidates = append(candidates, candidate{docID: docID, refPointer: refPointer, occurredAt: occurredAt})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return report, err
	}
	rows.Close()

	report.Scanned = len(candidates)
	var absent []candidate
	for _, item := range candidates {
		if err := ctx.Err(); err != nil {
			break
		}
		switch tc.Vault.ClassifyAndHealEpisodicUnit(scope, item.docID, item.refPointer, item.occurredAt, apply) {
		case thinclient.EpisodicContentAbsent:
			report.Absent++
			absent = append(absent, item)
		case thinclient.EpisodicContentRelocatable:
			report.Relocatable++
		case thinclient.EpisodicContentVerified:
			report.Verified++
		default:
			report.Unreadable++
			if pruneUnreadable {
				absent = append(absent, item)
			}
		}
	}

	if !apply {
		for _, item := range absent {
			if len(report.Candidates) >= 20 {
				break
			}
			report.Candidates = append(report.Candidates, item.docID+"\t"+item.refPointer)
		}
		report.Warnings = append(report.Warnings,
			"dry run: pass --apply --yes to retire provably-absent units; rows that still resolve or whose source is unavailable are always kept")
		return report, nil
	}

	report.Mode = "apply"
	if len(absent) > 0 {
		receiptDir := config.DefaultStateDir()
		if err := os.MkdirAll(receiptDir, 0o700); err != nil {
			return report, fmt.Errorf("episodic prune receipt: %w", err)
		}
		stamp := time.Now().UTC().Format("20060102T150405Z")
		receiptPath := filepath.Join(receiptDir, "episodic-prune-"+stamp+".jsonl")
		receipt, err := os.OpenFile(receiptPath, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
		for seq := 2; errors.Is(err, os.ErrExist) && seq <= 64; seq++ {
			receiptPath = filepath.Join(receiptDir, fmt.Sprintf("episodic-prune-%s-%d.jsonl", stamp, seq))
			receipt, err = os.OpenFile(receiptPath, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
		}
		if err != nil {
			return report, fmt.Errorf("episodic prune receipt: %w", err)
		}
		encoder := json.NewEncoder(receipt)
		removedAt := ids.UTCNow()
		for _, item := range absent {
			if err := ctx.Err(); err != nil {
				break
			}
			if err := encoder.Encode(EpisodicPruneReceipt{DocID: item.docID, RefPointer: item.refPointer, RemovedAt: removedAt}); err != nil {
				_ = receipt.Close()
				return report, fmt.Errorf("episodic prune receipt: %w", err)
			}
			removed, delErr := tc.Vault.DeleteLocalEpisodicUnit(item.docID, item.refPointer)
			if delErr != nil {
				report.Warnings = append(report.Warnings, fmt.Sprintf("episodic prune %s: %v", item.docID, delErr))
				continue
			}
			if removed {
				report.Removed++
			} else {
				// The row changed underneath us (a re-point healed it): keep it.
				report.Kept++
			}
		}
		if err := receipt.Close(); err != nil {
			return report, fmt.Errorf("episodic prune receipt: %w", err)
		}
		report.Receipt = receiptPath
	}
	report.NoeticRemoved, report.LocusHealed = pruneOrphanNoeticAndHealLoci(ctx, db)
	if memory.Config != nil && mcp.ShouldUseUnixDaemon(memory.Config.DB) {
		mcp.InvalidateUnixDaemonCaches("")
	}
	if report.Kept > 0 {
		report.Warnings = append(report.Warnings,
			fmt.Sprintf("%d candidates were re-pointed concurrently and kept", report.Kept))
	}
	if report.Scanned >= limit {
		report.Warnings = append(report.Warnings, "scan limit reached; run the prune again to continue")
	}
	return report, nil
}

// pruneOrphanNoeticAndHealLoci retires internal warmup probes and orphaned L3 Noetic
// rows (whose cited L1/L2 units no longer exist in the vault), and repairs episodic
// rows whose locus was stamped as "admin" or empty by looking up the session's Git
// workspace root in the ZCode database.
func pruneOrphanNoeticAndHealLoci(ctx context.Context, db *sql.DB) (noeticRemoved int, locusHealed int) {
	if db == nil {
		return 0, 0
	}
	type noeticRow struct {
		id        string
		status    string
		queryText string
		citedJSON string
	}
	var nRows []noeticRow
	rows, err := db.QueryContext(ctx, `SELECT noetic_id, COALESCE(status, 'active'), COALESCE(query_text, ''), COALESCE(cited_unit_ids_json, '[]') FROM thin_vault_noetic`)
	if err == nil {
		for rows.Next() {
			var r noeticRow
			if rows.Scan(&r.id, &r.status, &r.queryText, &r.citedJSON) == nil {
				nRows = append(nRows, r)
			}
		}
		rows.Close()
	}
	var hasBgChunks int
	_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'thin_vault_bg_chunks'`).Scan(&hasBgChunks)
	var dropIDs []string
	var staleIDs []string
	for _, r := range nRows {
		qLower := strings.ToLower(strings.TrimSpace(r.queryText))
		if strings.HasPrefix(qLower, "daemon warmup") || qLower == "daemon-warmup" {
			dropIDs = append(dropIDs, r.id)
			continue
		}
		var cited []string
		if json.Unmarshal([]byte(r.citedJSON), &cited) != nil || len(cited) == 0 {
			continue
		}
		anyAlive := false
		anyMissing := false
		for _, cid := range cited {
			cid = strings.TrimSpace(cid)
			if cid == "" {
				continue
			}
			unitAlive := false
			var exists int
			if qErr := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM thin_vault_docs WHERE doc_id = ?)`, cid).Scan(&exists); qErr != nil || exists != 0 {
				unitAlive = true
			} else if hasBgChunks > 0 {
				if bgErr := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM thin_vault_bg_chunks WHERE unit_id = ?)`, cid).Scan(&exists); bgErr != nil || exists != 0 {
					unitAlive = true
				}
			}
			if unitAlive {
				anyAlive = true
			} else {
				anyMissing = true
			}
		}
		if !anyAlive {
			dropIDs = append(dropIDs, r.id)
		} else if anyMissing && strings.EqualFold(strings.TrimSpace(r.status), "active") {
			staleIDs = append(staleIDs, r.id)
		}
	}
	if len(dropIDs) > 0 || len(staleIDs) > 0 {
		if tx, txErr := db.BeginTx(ctx, nil); txErr == nil {
			for _, id := range dropIDs {
				if ctx != nil && ctx.Err() != nil {
					break
				}
				if res, delErr := tx.ExecContext(ctx, `DELETE FROM thin_vault_noetic WHERE noetic_id = ?`, id); delErr == nil {
					if n, _ := res.RowsAffected(); n > 0 {
						noeticRemoved += int(n)
					}
				}
			}
			for _, id := range staleIDs {
				if ctx != nil && ctx.Err() != nil {
					break
				}
				_, _ = tx.ExecContext(ctx, `UPDATE thin_vault_noetic SET status = 'stale' WHERE noetic_id = ?`, id)
			}
			_ = tx.Commit()
		}
	}

	sessionLocus := make(map[string]string, 256)
	dirLocusCache := make(map[string]string, 32)
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		zdbPath := filepath.Join(home, ".zcode", "cli", "db", "db.sqlite")
		if _, statErr := os.Stat(zdbPath); statErr == nil {
			if zdb, oErr := sql.Open("sqlite", ids.SQLiteFileURI(zdbPath)+"?mode=ro&_pragma=busy_timeout(1500)"); oErr == nil {
				if sRows, qErr := zdb.QueryContext(ctx, `SELECT id, COALESCE(directory, '') FROM session WHERE COALESCE(directory, '') <> ''`); qErr == nil {
					for sRows.Next() {
						var sid, dir string
						if sRows.Scan(&sid, &dir) == nil && sid != "" && dir != "" {
							loc, cached := dirLocusCache[dir]
							if !cached {
								loc = api.ResolveWorkspaceLocus(dir)
								dirLocusCache[dir] = loc
							}
							// A resolved empty locus (session outside any repository) is a
							// value too: it heals stale directory-name loci to "".
							sessionLocus[sid] = loc
						}
					}
					sRows.Close()
				}
				_ = zdb.Close()
			}
		}
	}
	type docLocusUpdate struct {
		docID string
		locus string
	}
	var updates []docLocusUpdate
	type locusResolution struct {
		locus    string
		resolved bool
	}
	jsonlLocusCache := make(map[string]locusResolution, 32)
	dRows, err := db.QueryContext(ctx, `SELECT doc_id, raw_text, COALESCE(locus, '') FROM thin_vault_docs WHERE (layer = 'episodic' OR layer = '')`)
	if err == nil {
		for dRows.Next() {
			var docID, rawText, curLocus string
			if dRows.Scan(&docID, &rawText, &curLocus) != nil {
				continue
			}
			trimmed := strings.TrimSpace(rawText)
			trueLocus := ""
			resolved := false
			if strings.HasPrefix(trimmed, "@ref:zcode:") {
				rest := strings.TrimPrefix(trimmed, "@ref:zcode:")
				if colon := strings.IndexByte(rest, ':'); colon > 0 {
					if loc, found := sessionLocus[rest[:colon]]; found {
						trueLocus = loc
						resolved = true
					}
				}
			} else if strings.HasPrefix(trimmed, "@ref:jsonl:") {
				rest := strings.TrimPrefix(trimmed, "@ref:jsonl:")
				if lastColon := strings.LastIndexByte(rest, ':'); lastColon > 0 {
					jPath := rest[:lastColon]
					entry, cached := jsonlLocusCache[jPath]
					if !cached {
						loc, ok := resolveJSONLTranscriptLocus(jPath)
						entry = locusResolution{locus: loc, resolved: ok}
						jsonlLocusCache[jPath] = entry
					}
					trueLocus = entry.locus
					resolved = entry.resolved
				}
			}
			if resolved && curLocus != trueLocus {
				updates = append(updates, docLocusUpdate{docID: docID, locus: trueLocus})
			}
		}
		dRows.Close()
	}
	if len(updates) > 0 {
		if tx, txErr := db.BeginTx(ctx, nil); txErr == nil {
			for _, u := range updates {
				if ctx != nil && ctx.Err() != nil {
					break
				}
				if res, uErr := tx.ExecContext(ctx, `UPDATE thin_vault_docs SET locus = ? WHERE doc_id = ?`, u.locus, u.docID); uErr == nil {
					if n, _ := res.RowsAffected(); n > 0 {
						locusHealed += int(n)
					}
				}
			}
			_ = tx.Commit()
		}
	}
	if noeticRemoved > 0 || locusHealed > 0 {
		_, _ = db.ExecContext(ctx, `UPDATE thin_vault_episodic_revision SET revision = revision + 1 WHERE id = 1`)
	}
	return noeticRemoved, locusHealed
}

// episodicPruneText renders one prune pass for humans.
func (a *application) printEpisodicPrune(report EpisodicPruneReport) {
	a.printf("episodic prune (%s): scanned %d · verified %d · relocatable %d · provably absent %d · unavailable %d · removed %d · kept %d\n",
		report.Mode, report.Scanned, report.Verified, report.Relocatable, report.Absent, report.Unreadable, report.Removed, report.Kept)
	for _, item := range report.Candidates {
		a.printf("  candidate: %s\n", strings.ReplaceAll(item, "\t", " "))
	}
	if report.Receipt != "" {
		a.printf("receipt: %s\n", report.Receipt)
	}
	for _, warning := range report.Warnings {
		a.printf("warning: %s\n", warning)
	}
}
