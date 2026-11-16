// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package cli

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/rocareer/rolling-memory/internal/api"
	"github.com/rocareer/rolling-memory/internal/config"
	"github.com/rocareer/rolling-memory/internal/episodic"
	"github.com/rocareer/rolling-memory/internal/episodicunit"
	"github.com/rocareer/rolling-memory/internal/harness"
	"github.com/rocareer/rolling-memory/internal/ids"
	"github.com/rocareer/rolling-memory/internal/thinclient"
)

type episodicScanSource struct {
	HarnessID string
	Kind      string
	Path      string
	SessionID string
	Locus     string
	RawDir    string
}

type episodicSourceCursor struct {
	Source       string `json:"source"`
	Index        int    `json:"index"`
	Stamp        string `json:"stamp"`
	LastDocID    string `json:"last_doc_id,omitempty"`
	Grew         bool   `json:"grew,omitempty"`
	TurnRef      string `json:"turn_ref,omitempty"`
	TurnStartIdx int    `json:"turn_start_idx,omitempty"`
}

func (s episodicScanSource) key() string {
	return s.HarnessID + "\x00" + s.Kind + "\x00" + s.Path + "\x00" + s.SessionID
}

func discoverEpisodicScanSources(ctx context.Context, home, harnessID string) ([]episodicScanSource, error) {
	if !harnessTurnsScannable(harnessID) {
		// The host is inventoried (the console counts its sessions) but this
		// build cannot read its turn content. Enumerating its transcripts here
		// would read gigabytes this build cannot parse, hold the shared scan
		// cursor on files that keep changing, and starve the hosts whose turns
		// do import — so the importer stays silent until a turn reader exists.
		return nil, nil
	}
	// Transcript roots come from the harness adapter: the importer and the
	// console's session inventory must never disagree about where a host keeps
	// its conversations.
	patterns := harness.TranscriptGlobs(home, harnessID)
	var sources []episodicScanSource
	seen := map[string]bool{}
	for _, pattern := range patterns {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return nil, err
		}
		for _, path := range matches {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if seen[path] {
				continue
			}
			info, err := os.Stat(path)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return sources, err
			}
			if info.IsDir() {
				continue
			}
			seen[path] = true
			sources = append(sources, episodicScanSource{
				HarnessID: harnessID,
				Kind:      "jsonl",
				Path:      path,
			})
		}
	}
	if harnessID == "zcode" {
		path := filepath.Join(home, ".zcode", "cli", "db", "db.sqlite")
		if _, err := os.Stat(path); err != nil {
			if !os.IsNotExist(err) {
				return nil, err
			}
		} else {
			db, err := sql.Open("sqlite", ids.SQLiteFileURI(path)+"?mode=ro&_pragma=busy_timeout(1500)")
			if err != nil {
				return nil, err
			}
			hasDirCol := true
			rows, err := db.QueryContext(ctx, `SELECT id, COALESCE(directory, '') FROM session WHERE COALESCE(parent_id, '') = '' AND task_type != 'subagent_child' ORDER BY id`)
			if err != nil {
				hasDirCol = false
				rows, err = db.QueryContext(ctx, `SELECT id FROM session WHERE COALESCE(parent_id, '') = '' AND task_type != 'subagent_child' ORDER BY id`)
			}
			if err != nil {
				_ = db.Close()
				return nil, err
			}
			for rows.Next() {
				var session, dir string
				var scanErr error
				if hasDirCol {
					scanErr = rows.Scan(&session, &dir)
				} else {
					scanErr = rows.Scan(&session)
				}
				if scanErr != nil {
					_ = rows.Close()
					_ = db.Close()
					return nil, scanErr
				}
				sources = append(sources, episodicScanSource{
					HarnessID: harnessID,
					Kind:      "zcode",
					Path:      path,
					SessionID: session,
					RawDir:    dir,
				})
			}
			err = rows.Err()
			_ = rows.Close()
			_ = db.Close()
			if err != nil {
				return nil, err
			}
		}
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].key() < sources[j].key() })
	return sources, nil
}

// resolveJSONLTranscriptLocus extracts the workspace locus recorded inside the
// opening lines of a JSONL session transcript (`cwd` or `payload.cwd`). The
// locus is the canonical workspace identity (repository root basename, "" when
// the cwd sits outside any repository); resolved reports whether a cwd was
// found at all, so callers can tell "outside any repository" apart from "no
// readable cwd" (unknown — keep the stored locus).
func resolveJSONLTranscriptLocus(path string) (string, bool) {
	file, err := os.Open(strings.TrimSpace(path))
	if err != nil {
		return "", false
	}
	defer file.Close()
	reader := bufio.NewReader(file)
	for lineIdx := 0; lineIdx < 32; lineIdx++ {
		line, readErr := reader.ReadString('\n')
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "{") && strings.Contains(trimmed, `"cwd"`) {
			var obj map[string]any
			if json.Unmarshal([]byte(trimmed), &obj) == nil && obj != nil {
				cwd, _ := obj["cwd"].(string)
				if cwd == "" {
					if payload, ok := obj["payload"].(map[string]any); ok && payload != nil {
						cwd, _ = payload["cwd"].(string)
					}
				}
				if cwd = strings.TrimSpace(cwd); cwd != "" {
					return api.ResolveWorkspaceLocus(cwd), true
				}
			}
		}
		if readErr != nil {
			break
		}
	}
	return "", false
}

func episodicScanSourceStamp(source episodicScanSource) (string, error) {
	info, err := os.Stat(source.Path)
	if err != nil {
		return "", err
	}
	stamp := fmt.Sprintf("%d:%d", info.ModTime().UnixNano(), info.Size())
	if source.Kind == "zcode" {
		wal, err := os.Stat(source.Path + "-wal")
		if err == nil {
			stamp += fmt.Sprintf(":%d:%d", wal.ModTime().UnixNano(), wal.Size())
		} else if !os.IsNotExist(err) {
			return "", err
		}
	}
	return stamp, nil
}

var errEpisodicScanPageStop = errors.New("episodic scan page stop")

func streamEpisodicScanSource(ctx context.Context, source episodicScanSource, scope string, visit func(int, candidateEpisodicTurn) error) (int, error) {
	return streamEpisodicScanSourceFrom(ctx, source, scope, "", 0, func(i int, _ int, turn candidateEpisodicTurn) error {
		return visit(i, turn)
	})
}

func streamEpisodicScanSourceFrom(ctx context.Context, source episodicScanSource, scope, startTurnRef string, startTurnIdx int, visit func(unitIdx int, turnStartIdx int, turn candidateEpisodicTurn) error) (int, error) {
	if source.Locus == "" {
		switch source.Kind {
		case "jsonl":
			source.Locus, _ = resolveJSONLTranscriptLocus(source.Path)
		case "zcode":
			if source.RawDir != "" {
				source.Locus = api.ResolveWorkspaceLocus(source.RawDir)
			}
		}
	}
	idx := 0
	emitUnits := func(prefix string, turnStart int, segment episodic.Segment, timestamp int64, completed bool) error {
		for _, unit := range episodicunit.SegmentUnits(scope, prefix, segment, timestamp, completed) {
			if err := ctx.Err(); err != nil {
				return err
			}
			turn := candidateEpisodicTurn{
				HarnessID:  source.HarnessID,
				SessionID:  source.SessionID,
				Locus:      source.Locus,
				DocID:      unit.DocID,
				RefPointer: unit.RefPointer,
				TurnRef:    prefix,
				Text:       unit.Text,
				OccurredAt: unit.OccurredAt,
				Completed:  unit.Completed,
			}
			curIdx := idx
			idx++
			if err := visit(curIdx, turnStart, turn); err != nil {
				return err
			}
		}
		return nil
	}
	switch source.Kind {
	case "jsonl":
		info, err := os.Stat(source.Path)
		if err != nil {
			return 0, err
		}
		source.SessionID = harness.TranscriptSessionID(source.HarnessID, source.Path)
		err = episodic.VisitJSONLTurns(ctx, source.Path, func(turn episodic.JSONLTurn, closed bool) error {
			when := turn.OccurredAt
			if when <= 0 {
				when = info.ModTime().Unix()
			}
			completed := closed || ids.UTCNow()-when >= episodicTailGraceSec
			prefix := fmt.Sprintf("@ref:jsonl:%s:%d", source.Path, turn.Index)
			turnStart := idx
			for _, segment := range turn.Segments {
				if err := emitUnits(prefix, turnStart, segment, turn.OccurredAt, completed); err != nil {
					return err
				}
			}
			return nil
		})
		return idx, err
	case "zcode":
		db, err := sql.Open("sqlite", ids.SQLiteFileURI(source.Path)+"?mode=ro&_pragma=busy_timeout(1500)")
		if err != nil {
			return 0, err
		}
		defer db.Close()
		startUserMsgID := ""
		expectedPrefix := "@ref:zcode:" + source.SessionID + ":"
		if startTurnIdx > 0 && strings.HasPrefix(startTurnRef, expectedPrefix) {
			candidateMsgID := strings.TrimPrefix(startTurnRef, expectedPrefix)
			var seq sql.NullInt64
			if db.QueryRowContext(ctx, `SELECT sequence FROM message WHERE session_id = ? AND id = ?`, source.SessionID, candidateMsgID).Scan(&seq) == nil && seq.Valid {
				startUserMsgID = candidateMsgID
				idx = startTurnIdx
			}
		}
		err = episodic.VisitZCodeTurnsFrom(ctx, db, source.SessionID, startUserMsgID, func(turn episodic.Turn, closed bool) error {
			ts := turn.OccurredAt
			if ts > 1e11 {
				ts /= 1000
			}
			completed := closed || ids.UTCNow()-ts >= episodicTailGraceSec
			prefix := fmt.Sprintf("@ref:zcode:%s:%s", source.SessionID, turn.UserMsgID)
			turnStart := idx
			for _, segment := range turn.Segments {
				if err := emitUnits(prefix, turnStart, segment, ts, completed); err != nil {
					return err
				}
			}
			return nil
		})
		return idx, err
	default:
		return 0, fmt.Errorf("unknown episodic source %q", source.Kind)
	}
}

// runEpisodicSourceScanTick keeps the history scan independent of the foreground
// reconcile window. A completed cycle starts over so changes and streaming tails
// are revisited, while each checkpoint contains only successfully saved work.
func runEpisodicSourceScanTick(ctx context.Context, memory *api.Memory, home string) error {
	if memory == nil || memory.Store == nil || memory.Store.DB() == nil {
		return errors.New("episodic source scan requires an open local store")
	}
	scope := config.DefaultScope
	if memory.Config != nil && strings.TrimSpace(memory.Config.Scope) != "" {
		scope = strings.TrimSpace(memory.Config.Scope)
	}
	tc, err := memory.ThinClient()
	if err != nil {
		return err
	}
	if tc == nil || tc.Vault == nil {
		return errors.New("episodic source scan requires a local vault")
	}
	db := memory.Store.DB()
	deletedEpi := tc.Vault.DeletedUnitSet("episodic")
	key := "episodic_source_scan_cursor:" + scope
	var raw []byte
	var cursor episodicSourceCursor
	err = db.QueryRowContext(ctx, `SELECT v FROM thin_vault_meta WHERE k = ?`, key).Scan(&raw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	exists := err == nil
	if exists {
		if err := json.Unmarshal(raw, &cursor); err != nil {
			return fmt.Errorf("episodic source scan checkpoint: %w", err)
		}
	}
	var sources []episodicScanSource
	var discoveryErr error
	for _, definition := range harnessEnvDefinitions(home, "") {
		found, err := discoverEpisodicScanSources(ctx, home, definition.ID)
		if err != nil {
			discoveryErr = errors.Join(discoveryErr, fmt.Errorf("%s: %w", definition.ID, err))
		}
		sources = append(sources, found...)
	}
	next, more, scanErr := visitEpisodicSources(ctx, sources, scope, cursor, reconcileDefaultLimit, func(turn candidateEpisodicTurn) error {
		if episodicunit.DocID(scope, turn.Text) != turn.DocID {
			return fmt.Errorf("episodic source scan %s: content identity mismatch", turn.DocID)
		}
		if !turn.Completed || deletedEpi[turn.DocID] {
			return nil
		}
		// Identity is stamped for every turn — including an empty locus — so each
		// row carries its own source's workspace and never inherits a previous
		// source's identity when the source sits outside any repository.
		tc.Vault.SetIdentity(thinclient.VaultIdentity{
			AssistantID:   turn.HarnessID,
			IncarnationID: turn.SessionID,
			Locus:         turn.Locus,
		})
		var stored, curLocus string
		err := db.QueryRowContext(ctx, `SELECT raw_text, COALESCE(locus, '') FROM thin_vault_docs WHERE doc_id = ?`, turn.DocID).Scan(&stored, &curLocus)
		if err == nil {
			if turn.Locus != "" && curLocus == "" {
				_, _ = db.ExecContext(ctx, `UPDATE thin_vault_docs SET locus = ? WHERE doc_id = ?`, turn.Locus, turn.DocID)
			}
			if stored != turn.RefPointer {
				// The same speech re-enumerated under a new `#<k>` (the filter itself is
				// allowed to evolve): re-point the stored row. The save path verifies the
				// turn pointer stem and content identity and refuses anything else.
				packet, _ := tc.Vault.PackBlindPacket(turn.DocID, turn.Text, turn.OccurredAt)
				if sErr := tc.Vault.SaveLocalEpisodicPointerIndexed(turn.DocID, turn.OccurredAt, turn.RefPointer, turn.TurnRef, turn.Text, packet, thinclient.CloudIngestResponse{}); sErr != nil {
					// Identical speech reaching the vault from a second transcript: the first
					// pointer wins and the lineage row below keeps this provenance.
					if !errors.Is(sErr, thinclient.ErrEpisodicPointerSourceConflict) {
						return sErr
					}
				}
			} else if !tc.Vault.HasLocalEpisodicIndex(turn.DocID, turn.RefPointer) {
				if _, err = tc.Vault.EnsureLocalEpisodicIndex(scope, turn.DocID, turn.RefPointer, turn.Text); err != nil {
					return err
				}
			}
			if turn.TurnRef != "" {
				var hasLin int
				_ = db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM thin_vault_episodic_lineage WHERE turn_ref = ? AND doc_id = ?)`, turn.TurnRef, turn.DocID).Scan(&hasLin)
				if hasLin == 0 {
					_, err = db.ExecContext(ctx, `INSERT OR IGNORE INTO thin_vault_episodic_lineage(turn_ref, doc_id, occurred_at) VALUES (?, ?, ?)`, turn.TurnRef, turn.DocID, turn.OccurredAt)
				}
			}
			return err
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		packet, _ := tc.Vault.PackBlindPacket(turn.DocID, turn.Text, turn.OccurredAt)
		return tc.Vault.SaveLocalEpisodicPointerIndexed(turn.DocID, turn.OccurredAt, turn.RefPointer, turn.TurnRef, turn.Text, packet, thinclient.CloudIngestResponse{})
	})
	if scanErr == nil && !more {
		next = episodicSourceCursor{}
	}
	nextRaw, err := json.Marshal(next)
	if err != nil {
		return err
	}
	if err := saveEpisodicCheckpoint(db, key, raw, exists, nextRaw); err != nil {
		return errors.Join(discoveryErr, scanErr, fmt.Errorf("episodic source scan checkpoint: %w", err))
	}
	return errors.Join(discoveryErr, scanErr)
}

// visitEpisodicSources advances only after each successful visit. Changes during
// a page are retried, and completed cycles revisit earlier source occurrences.
func visitEpisodicSources(ctx context.Context, sources []episodicScanSource, scope string, cursor episodicSourceCursor, limit int, visit func(candidateEpisodicTurn) error) (episodicSourceCursor, bool, error) {
	if limit <= 0 {
		return cursor, false, fmt.Errorf("episodic scan page requires a positive limit")
	}
	ordered := append([]episodicScanSource(nil), sources...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].key() < ordered[j].key() })
	visited := 0
	var sourceErr error
	hadSourceErr := false

	for srcIdx, source := range ordered {
		if err := ctx.Err(); err != nil {
			return cursor, true, errors.Join(sourceErr, err)
		}
		key := source.key()
		if key < cursor.Source {
			continue
		}
		stamp, err := episodicScanSourceStamp(source)
		if err != nil {
			sourceErr = errors.Join(sourceErr, err)
			hadSourceErr = true
			continue
		}
		start := 0
		grew := false
		if key == cursor.Source && cursor.Index > 0 {
			start = cursor.Index
			grew = cursor.Grew
			if stamp != cursor.Stamp && cursor.Stamp != "" && source.Kind != "zcode" {
				var anchorMatched bool
				cnt, sErr := streamEpisodicScanSource(ctx, source, scope, func(i int, turn candidateEpisodicTurn) error {
					if i == start-1 {
						anchorMatched = (turn.DocID == cursor.LastDocID)
						return errEpisodicScanPageStop
					}
					return nil
				})
				if sErr != nil && !errors.Is(sErr, errEpisodicScanPageStop) {
					if errors.Is(sErr, context.Canceled) || errors.Is(sErr, context.DeadlineExceeded) {
						return cursor, true, errors.Join(sourceErr, sErr)
					}
					sourceErr = errors.Join(sourceErr, sErr)
					hadSourceErr = true
					continue
				}
				if cnt < start || (cursor.LastDocID != "" && !anchorMatched) {
					start = 0
					grew = false
				} else {
					if srcIdx+1 < len(ordered) && cursor.Grew {
						continue
					}
					grew = true
				}
			}
		}

		var visitCallErr error
		lastDocID := ""
		startTurnRef := ""
		startTurnIdx := 0
		lastTurnRef := ""
		lastTurnStartIdx := 0
		if key == cursor.Source && start > 0 {
			lastDocID = cursor.LastDocID
			startTurnRef = cursor.TurnRef
			startTurnIdx = cursor.TurnStartIdx
			lastTurnRef = cursor.TurnRef
			lastTurnStartIdx = cursor.TurnStartIdx
		}
		lastVisitedIdx := start
		stoppedForLimit := false
		_, sErr := streamEpisodicScanSourceFrom(ctx, source, scope, startTurnRef, startTurnIdx, func(i int, turnStart int, turn candidateEpisodicTurn) error {
			if i < start {
				return nil
			}
			if visited == limit {
				stoppedForLimit = true
				return errEpisodicScanPageStop
			}
			if err := visit(turn); err != nil {
				visitCallErr = err
				return errEpisodicScanPageStop
			}
			lastDocID = turn.DocID
			lastTurnRef = turn.TurnRef
			lastTurnStartIdx = turnStart
			lastVisitedIdx = i + 1
			cursor = episodicSourceCursor{
				Source:       key,
				Index:        lastVisitedIdx,
				Stamp:        stamp,
				LastDocID:    lastDocID,
				Grew:         grew,
				TurnRef:      lastTurnRef,
				TurnStartIdx: lastTurnStartIdx,
			}
			visited++
			return nil
		})
		if visitCallErr != nil {
			return cursor, true, errors.Join(sourceErr, visitCallErr)
		}
		if sErr != nil && !errors.Is(sErr, errEpisodicScanPageStop) {
			if errors.Is(sErr, context.Canceled) || errors.Is(sErr, context.DeadlineExceeded) {
				return cursor, true, errors.Join(sourceErr, sErr)
			}
			sourceErr = errors.Join(sourceErr, sErr)
			hadSourceErr = true
			continue
		}
		after, err := episodicScanSourceStamp(source)
		if err != nil {
			sourceErr = errors.Join(sourceErr, err)
			hadSourceErr = true
			continue
		}
		if after != stamp && source.Kind != "zcode" {
			return cursor, true, errors.Join(sourceErr, fmt.Errorf("episodic source changed during scan: %s", source.Path))
		}
		if stoppedForLimit {
			return cursor, true, sourceErr
		}
		cursor = episodicSourceCursor{
			Source:       key,
			Index:        lastVisitedIdx,
			Stamp:        stamp,
			LastDocID:    lastDocID,
			Grew:         grew,
			TurnRef:      lastTurnRef,
			TurnStartIdx: lastTurnStartIdx,
		}
	}
	if hadSourceErr {
		return episodicSourceCursor{}, true, sourceErr
	}
	return cursor, false, nil
}
