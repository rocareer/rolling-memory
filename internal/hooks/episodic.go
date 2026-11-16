// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

// Thin-brain episodic upload: the harness Stop hook hands the just-ended turn to
// the Autonoetic Engine `/open/memory/v1/ingest` with `layer="episodic"` in real
// time, so the cloud distils the episode into Cell addressing while the turn is
// still hot instead of on the next session boot.
//
// The turn is uploaded as one unit per speech segment (a user message or one
// assistant text part), not as one blob: a harness turn mixes dozens of tool steps
// into a single text, which averages the wave address over unrelated topics and
// forces delivery to slice it arbitrarily. Segment units keep every spoken word —
// low-value chatter simply stops matching cues — and only a segment that alone
// outgrows the tokenz-r top rung is split at sentence boundaries.
//
// The upload is best-effort and fail-open: a shared timeout keeps the hook inside
// the harness Stop budget, the local plaintext vault is written by the same call
// (the engine only ever receives the blind `{h, b, f}` integer frame), and every
// failure is logged without raising into the harness. Units that miss the budget
// stay in the local vault for the next reconcile sweep to push.
package hooks

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/rocareer/rolling-memory/internal/api"
	"github.com/rocareer/rolling-memory/internal/config"
	"github.com/rocareer/rolling-memory/internal/episodic"
	"github.com/rocareer/rolling-memory/internal/episodicunit"
	"github.com/rocareer/rolling-memory/internal/ids"
	"github.com/rocareer/rolling-memory/internal/tokenzr"
)

const (
	// episodicUploadTimeout bounds the whole Stop-time ingest (all units) so a slow
	// or unreachable engine never eats the harness hook budget (Stop is wired with 5s).
	episodicUploadTimeout = 3 * time.Second
)

// episodicTurnRow is one fallback trace row of the just-ended turn in file order.
type episodicTurnRow struct {
	Author string
	Text   string
}

// uploadCurrentEpisodicTurn composes the newest turn of the session as segment
// units and ingests each into the engine's episodic layer. The receipt is merged
// into the hook log line (`uploaded` / `unit` / `units` / `tokens`).
func uploadCurrentEpisodicTurn(memory *api.Memory, cfg *config.Config, session string, payload map[string]any) map[string]any {
	if memory == nil || memory.Store == nil || memory.Store.DB() == nil {
		return map[string]any{"uploaded": 0}
	}
	units, readCnt := currentTurnUnits(cfg.Scope, session, payload)
	if len(units) == 0 {
		return map[string]any{"uploaded": 0}
	}
	tc, err := memory.ThinClient()
	if err != nil || tc == nil || tc.Transport == nil {
		return map[string]any{"uploaded": 0}
	}
	ctx, cancel := context.WithTimeout(context.Background(), episodicUploadTimeout)
	defer cancel()
	var deletedEpi map[string]bool
	if tc.Vault != nil {
		deletedEpi = tc.Vault.DeletedUnitSet("episodic")
	}
	uploaded := 0
	tokens := 0
	firstUnit := ""
	for _, unit := range units {
		if deletedEpi[unit.DocID] {
			continue
		}
		if _, err := tc.RememberEpisodicByRef(ctx, unit.DocID, unit.RefPointer, unit.TurnRef, unit.Text, unit.OccurredAt); err != nil {
			continue
		}
		if uploaded == 0 {
			firstUnit = unit.DocID
		}
		uploaded++
		tokens += tokenzr.Count(unit.Text)
	}
	out := map[string]any{"uploaded": uploaded, "units": len(units)}
	if uploaded > 0 {
		out["unit"] = firstUnit
		out["tokens"] = tokens
	}
	if readCnt > 0 {
		out["direct_messages"] = readCnt
	}
	return out
}

// currentTurnUnits resolves the just-ended turn as segment units:
//  1. `payload["transcript_path"]` JSONL → the newest closed turn (zero-copy
//     `@ref:jsonl:<path>:<turn>#<k>` pointers);
//  2. ZCode CLI SQLite session tail (zero-copy `@ref:zcode:<session>:<msg>#<k>`);
//  3. payload fallback (`last_assistant_message` + prompt) with local text only.
func currentTurnUnits(scope, session string, payload map[string]any) ([]episodicunit.Unit, int) {
	if payload != nil {
		if rawPath := firstTruthy(payload["transcript_path"], payload["transcriptPath"]); rawPath != nil {
			path := expandUser(pyStr(rawPath))
			if !filepath.IsAbs(path) {
				if cwd := firstTruthy(payload["cwd"]); cwd != nil {
					path = pyJoin(expandUser(pyStr(cwd)), path)
				} else if wd, err := os.Getwd(); err == nil {
					path = pyJoin(wd, path)
				}
			}
			if turns, err := episodic.JSONLTurns(path); err == nil && len(turns) > 0 {
				turn := turns[len(turns)-1]
				prefix := "@ref:jsonl:" + path + ":" + strconv.Itoa(turn.Index)
				return unitsForSegments(scope, prefix, turn.Segments, turn.OccurredAt, true, len(turn.Segments))
			}
		}
		if rows := payloadTurnRows(payload); len(rows) > 0 {
			segments := segmentsFromRows(rows)
			// No transcript path and no readable session store: the payload is the only
			// copy of this turn, so its segments are kept as local plaintext.
			return unitsForSegments(scope, "", segments, ids.UTCNow(), true, len(segments))
		}
	}
	if session != "" && session != "default" {
		if turns, err := zcodeSessionTurns(session); err == nil && len(turns) > 0 {
			turn := turns[len(turns)-1]
			prefix := "@ref:zcode:" + session + ":" + turn.UserMsgID
			return unitsForSegments(scope, prefix, turn.Segments, turn.OccurredAt, true, len(turn.Segments))
		}
	}
	return nil, 0
}

// payloadTurnRows rebuilds the turn from the hook payload when no transcript is
// available: the opening prompt plus the closing assistant message.
func payloadTurnRows(payload map[string]any) []episodicTurnRow {
	lastMsg := firstTruthy(payload["last_assistant_message"], payload["lastAssistantMessage"])
	if lastMsg == nil {
		return nil
	}
	cleaned := stripMemoryAttributionLine(stripDegenerateTail(ids.StripInjected(pyStrip(pyStr(lastMsg)))))
	if cleaned == "" {
		return nil
	}
	rows := make([]episodicTurnRow, 0, 2)
	if prompt := firstTruthy(payload["prompt"], payload["user_prompt"]); prompt != nil {
		if userText := ids.StripInjected(pyStrip(pyStr(prompt))); userText != "" {
			rows = append(rows, episodicTurnRow{Author: "user", Text: userText})
		}
	}
	return append(rows, episodicTurnRow{Author: "assistant", Text: cleaned})
}

func segmentsFromRows(rows []episodicTurnRow) []episodic.Segment {
	segments := make([]episodic.Segment, 0, len(rows))
	for _, row := range rows {
		if strings.TrimSpace(row.Text) == "" {
			continue
		}
		segments = append(segments, episodic.Segment{
			Role:  row.Author,
			Text:  row.Text,
			Index: len(segments),
		})
	}
	return segments
}

// unitsForSegments renders segment units for one turn and drops duplicate units
// (identical speech inside one turn must not be ingested twice).
func unitsForSegments(scope, pointerPrefix string, segments []episodic.Segment, occurredAt int64, completed bool, readCnt int) ([]episodicunit.Unit, int) {
	units := make([]episodicunit.Unit, 0, len(segments))
	seen := map[string]bool{}
	for _, segment := range segments {
		for _, unit := range episodicunit.SegmentUnits(scope, pointerPrefix, segment, occurredAt, completed) {
			if seen[unit.DocID] {
				continue
			}
			seen[unit.DocID] = true
			units = append(units, unit)
		}
	}
	return units, readCnt
}

// zcodeSessionTurns enumerates the closed turns of one ZCode CLI session.
func zcodeSessionTurns(session string) ([]episodic.Turn, error) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil, err
	}
	zdbPath := filepath.Join(home, ".zcode", "cli", "db", "db.sqlite")
	if _, err := os.Stat(zdbPath); err != nil {
		return nil, err
	}
	zdb, err := sql.Open("sqlite", ids.SQLiteFileURI(zdbPath)+"?mode=ro&_pragma=busy_timeout(1500)")
	if err != nil {
		return nil, err
	}
	defer zdb.Close()
	return episodic.ZCodeSessionTurns(zdb, session)
}

const (
	// backgroundHookCooldownSec throttles L1 Background directory scans inside the
	// UserPromptSubmit hook so back-to-back prompts within 60s incur zero scan work.
	backgroundHookCooldownSec = int64(60)
	// backgroundHookSyncTimeout bounds the incremental background sync inside a hook.
	backgroundHookSyncTimeout = 1500 * time.Millisecond
	// backgroundHookMetaKey stores the last background hook scan unix timestamp in thin_vault_meta.
	backgroundHookMetaKey = "bg_last_hook_sync_ts"
)

// maybeSyncBackgroundOnHook triggers a lightweight incremental L1 Background sync
// during UserPromptSubmit when at least one directory is mounted in `thin_vault_bg_dirs`
// and the 60s cooldown window has elapsed. Unchanged files are skipped in O(1) via
// `os.Stat(mtime_ns, size)` inside `SyncBackgroundDirs`.
func maybeSyncBackgroundOnHook(memory *api.Memory) {
	if memory == nil || !memory.IsThinBrainProxy() || memory.Store == nil || memory.Store.DB() == nil {
		return
	}
	db := memory.Store.DB()
	var dirCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM thin_vault_bg_dirs`).Scan(&dirCount); err != nil || dirCount <= 0 {
		return
	}
	now := ids.UTCNow()
	var raw []byte
	if err := db.QueryRow(`SELECT v FROM thin_vault_meta WHERE k = ?`, backgroundHookMetaKey).Scan(&raw); err == nil {
		if lastTS, pErr := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64); pErr == nil {
			if now-lastTS >= 0 && now-lastTS < backgroundHookCooldownSec {
				return
			}
		}
	}
	_, _ = db.Exec(`CREATE TABLE IF NOT EXISTS thin_vault_meta (k TEXT PRIMARY KEY, v BLOB NOT NULL)`)
	_, _ = db.Exec(`INSERT OR REPLACE INTO thin_vault_meta(k, v) VALUES (?, ?)`,
		backgroundHookMetaKey, []byte(strconv.FormatInt(now, 10)))

	tc, err := memory.ThinClient()
	if err != nil || tc == nil || tc.Transport == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), backgroundHookSyncTimeout)
	defer cancel()
	_, _ = tc.SyncBackgroundDirs(ctx)
}
