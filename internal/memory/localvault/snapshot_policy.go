// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package localvault

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	core "github.com/rocareer/rolling-memory/internal/core"
)

// snapshotBuiltDocsRevKey records the corpus write generation the last
// published .zcsr was built from. Recall surfaces compare it with the live
// rm_revision.docs_rev to decide when the background builder must run again.
const snapshotBuiltDocsRevKey = "snapshot_built_docs_rev"

// snapshotBuiltChangeSeqKey records the rm_change sequence the last published
// .zcsr was built from. The reader applies only rows above this watermark.
const snapshotBuiltChangeSeqKey = "snapshot_built_change_seq"

// snapshotBuiltPurgeEpochKey records the destructive-purge epoch the published
// cluster was built from. A reader must never serve a generation older than the
// live purge epoch.
const snapshotBuiltPurgeEpochKey = "snapshot_built_purge_epoch"

// PublishedSnapshotReady reports whether a durable disk cluster exists. It is
// a cheap file probe: a stale cluster still counts as ready because the delta
// overlay keeps serving recent units until the next publish.
func (v *Vault) PublishedSnapshotReady() bool {
	if v == nil {
		return false
	}
	if v.db != nil {
		var docCnt int64
		if err := v.db.QueryRow(`SELECT docs_cnt FROM rm_revision WHERE id = 1`).Scan(&docCnt); err == nil && docCnt == 0 {
			// An empty corpus has nothing to materialise; treat it as ready so the
			// supervisor does not schedule a build loop before the first turn.
			return true
		}
	}
	dbPath := v.mainDBPath()
	if strings.TrimSpace(dbPath) == "" {
		return false
	}
	// A generation this build already failed to open must not keep the builder
	// idle; the next publication is what makes readiness true again.
	return PublishedSnapshotUsable(v.db, dbPath)
}

// SnapshotBuildGap reports how many corpus writes accumulated since the last
// published snapshot. known is false before the first recorded build.
func (v *Vault) SnapshotBuildGap(ctx context.Context) (gap int64, known bool) {
	if v == nil || v.db == nil {
		return 0, false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var docsRev int64
	if err := v.db.QueryRowContext(ctx, `SELECT docs_rev FROM rm_revision WHERE id = 1`).Scan(&docsRev); err != nil {
		return 0, false
	}
	var raw []byte
	if err := v.db.QueryRowContext(ctx, `SELECT v FROM rm_meta WHERE k = ?`, snapshotBuiltDocsRevKey).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return docsRev, false
		}
		return 0, false
	}
	built, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil {
		return docsRev, false
	}
	if docsRev < built {
		return 0, true
	}
	return docsRev - built, true
}

// SnapshotChangeGap reports how many change-log rows accumulated since the last
// published cluster. It is the precise trigger for the background builder; the
// docs revision gap remains as the pre-change-log fallback.
func (v *Vault) SnapshotChangeGap(ctx context.Context) (gap int64, known bool) {
	if v == nil || v.db == nil {
		return 0, false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var changeSeq int64
	if err := v.db.QueryRowContext(ctx, `SELECT change_seq FROM rm_revision WHERE id = 1`).Scan(&changeSeq); err != nil {
		return 0, false
	}
	built, ok := v.snapshotBuiltChangeSeq(ctx)
	if !ok {
		return changeSeq, false
	}
	if changeSeq < built {
		return 0, true
	}
	return changeSeq - built, true
}

// snapshotBuiltChangeSeq reads the change-log watermark of the last published// cluster. A missing marker means the reader must fall back to the full docs
// view once; the next build establishes it.
func (v *Vault) snapshotBuiltChangeSeq(ctx context.Context) (int64, bool) {
	if v == nil || v.db == nil {
		return 0, false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var raw []byte
	if err := v.db.QueryRowContext(ctx, `SELECT v FROM rm_meta WHERE k = ?`, snapshotBuiltChangeSeqKey).Scan(&raw); err != nil {
		return 0, false
	}
	value, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil || value < 0 {
		return 0, false
	}
	return value, true
}

// snapshotBuiltPurgeEpoch reads the purge epoch the published cluster was built
// from. A missing marker is conservative: the reader treats it as stale when the
// live epoch is non-zero.
func (v *Vault) snapshotBuiltPurgeEpoch(ctx context.Context) (int64, bool) {
	if v == nil || v.db == nil {
		return 0, false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var raw []byte
	if err := v.db.QueryRowContext(ctx, `SELECT v FROM rm_meta WHERE k = ?`, snapshotBuiltPurgeEpochKey).Scan(&raw); err != nil {
		return 0, false
	}
	value, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil || value < 0 {
		return 0, false
	}
	return value, true
}

// BumpPurgeEpoch invalidates every published generation after a destructive
// bulk purge. The background builder republishes; readers serve warming until
// the new cluster clears the barrier.
func (v *Vault) BumpPurgeEpoch(ctx context.Context) error {
	if v == nil || v.db == nil {
		return errors.New("localvault: nil vault")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	_, err := v.db.ExecContext(ctx, `UPDATE rm_revision SET purge_epoch = purge_epoch + 1 WHERE id = 1`)
	return err
}

// changeRowsAfter returns the bounded change rows in (after, through].
func (v *Vault) changeRowsAfter(ctx context.Context, after, through int64, limit int) ([]corpusChange, bool) {
	if v == nil || v.db == nil || through <= after {
		return nil, true
	}
	if limit <= 0 {
		limit = maxCSRDeltaDocs + 1
	}
	rows, err := v.db.QueryContext(ctx,
		`SELECT seq, unit_id, op FROM rm_change WHERE seq > ? AND seq <= ? ORDER BY seq LIMIT ?`,
		after, through, limit)
	if err != nil {
		return nil, false
	}
	defer rows.Close()
	out := make([]corpusChange, 0, 64)
	for rows.Next() {
		var change corpusChange
		if err := rows.Scan(&change.seq, &change.unitID, &change.op); err != nil {
			return nil, false
		}
		out = append(out, change)
	}
	if err := rows.Err(); err != nil {
		return nil, false
	}
	if len(out) >= limit {
		// The caller treats "at or above the limit" as too large and falls back.
		return out, false
	}
	if len(out) > 0 && out[0].seq != after+1 {
		return out, false
	}
	return out, true
}

type corpusChange struct {
	seq    int64
	unitID string
	op     string
}

// pruneChangeLog drops rows already covered by the previous published
// generation. The previous generation's readers can still catch up; readers
// further behind detect the gap and reload the cluster.
func (v *Vault) pruneChangeLog(ctx context.Context, through int64) {
	if v == nil || v.db == nil || through <= 0 {
		return
	}
	_, _ = v.db.ExecContext(ctx, `DELETE FROM rm_change WHERE seq <= ?`, through)
}

// recordSnapshotBuildAt stamps the corpus generation captured before the build
// started, so writes that arrive during the build are not claimed as published.
func (v *Vault) recordSnapshotBuildAt(ctx context.Context, docsRev, changeSeq, purgeEpoch int64) error {
	if v == nil || v.db == nil {
		return errors.New("localvault: nil vault")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if _, err := v.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS rm_meta (k TEXT PRIMARY KEY, v BLOB NOT NULL)`); err != nil {
		return err
	}
	tx, err := v.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	upsert := `INSERT INTO rm_meta(k, v) VALUES (?, ?) ON CONFLICT(k) DO UPDATE SET v = excluded.v`
	if _, err := tx.ExecContext(ctx, upsert, snapshotBuiltDocsRevKey, []byte(strconv.FormatInt(docsRev, 10))); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, upsert, snapshotBuiltChangeSeqKey, []byte(strconv.FormatInt(changeSeq, 10))); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, upsert, snapshotBuiltPurgeEpochKey, []byte(strconv.FormatInt(purgeEpoch, 10))); err != nil {
		return err
	}
	return tx.Commit()
}

// RebuildCorpusSnapshot forces one full-corpus materialisation and publishes
// the disk cluster. It returns the corpus generation captured before the build;
// the caller must not assume later writes are included. It is the entry point
// of the background builder process, never of the recall path.
func (v *Vault) RebuildCorpusSnapshot(ctx context.Context) (int64, error) {
	if v == nil || v.db == nil {
		return 0, errors.New("localvault: nil vault")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if strings.TrimSpace(v.mainDBPath()) == "" {
		return 0, errors.New("localvault: snapshot build requires a file-backed vault")
	}
	if err := v.checkSnapshotDiskSpace(); err != nil {
		return 0, err
	}
	var docCnt, builtDocsRev, builtChangeSeq, builtPurgeEpoch int64
	if err := v.db.QueryRowContext(ctx, `SELECT docs_cnt, docs_rev, change_seq, purge_epoch FROM rm_revision WHERE id = 1`).Scan(&docCnt, &builtDocsRev, &builtChangeSeq, &builtPurgeEpoch); err != nil {
		return 0, err
	}
	previousChangeSeq, _ := v.snapshotBuiltChangeSeq(ctx)
	if docCnt == 0 {
		if err := v.recordSnapshotBuildAt(ctx, builtDocsRev, builtChangeSeq, builtPurgeEpoch); err != nil {
			return 0, err
		}
		v.pruneChangeLog(ctx, previousChangeSeq)
		return builtDocsRev, nil
	}
	signature := v.canonicalSignature()
	if strings.TrimSpace(signature) == "" {
		return 0, errors.New("localvault: canonical store is not populated")
	}
	if previousChangeSeq > 0 && previousChangeSeq < builtChangeSeq {
		result, incErr := v.buildCorpusEngineIncremental(ctx, previousChangeSeq)
		if incErr == nil && result != nil {
			pubErr := v.publishBuiltGeneration(ctx, signature, result.changeSeq, result.docsRev, result.purgeEpoch, result.eng)
			_ = result.eng.Close()
			if pubErr == nil {
				v.pruneChangeLog(ctx, previousChangeSeq)
				return result.docsRev, nil
			}
		}
	}
	eng := v.buildCorpusEngine(ctx)
	if eng == nil {
		return 0, errors.New("localvault: full-corpus build failed")
	}
	if err := v.publishBuiltGeneration(ctx, signature, builtChangeSeq, builtDocsRev, builtPurgeEpoch, eng); err != nil {
		return 0, err
	}
	v.pruneChangeLog(ctx, previousChangeSeq)
	return builtDocsRev, nil
}

// publishBuiltGeneration writes one generation, records the build marker and
// drops the reader cache so the next load resolves the new slot.
func (v *Vault) publishBuiltGeneration(ctx context.Context, signature string, changeSeq, docsRev, purgeEpoch int64, eng *core.Engine) error {
	var publishedDocs []vaultDocEntry
	if docs, _, docsErr := v.canonicalSnapshotDocs(ctx); docsErr == nil {
		publishedDocs = docs
	}
	if _, err := v.publishCorpusSlot(ctx, v.mainDBPath(), signature, changeSeq, docsRev, eng, publishedDocs); err != nil {
		return err
	}
	v.clearRejectedSnapshotSlot()
	if !v.PublishedSnapshotReady() {
		return errors.New("localvault: snapshot publish failed")
	}
	if err := v.recordSnapshotBuildAt(ctx, docsRev, changeSeq, purgeEpoch); err != nil {
		return err
	}
	v.slotCacheMu.Lock()
	var previous *vaultCorpusSnapshot
	if v.snapRef != nil {
		previous = v.replaceCachedSnapshotLocked(nil)
	}
	v.slotCacheMu.Unlock()
	if previous != nil {
		previous.retire()
	}
	return nil
}

// AcquireBuildLock guards the single snapshot-builder slot across processes.
// The lock is abandoned automatically when it is older than staleAfter, so a
// killed builder cannot block publication forever.
func AcquireBuildLock(dbPath string, staleAfter time.Duration) (release func(), acquired bool, err error) {
	noop := func() {}
	if strings.TrimSpace(dbPath) == "" {
		return noop, true, nil
	}
	if staleAfter <= 0 {
		staleAfter = 45 * time.Minute
	}
	lockPath := dbPath + ".build.lock"
	token := strconv.Itoa(os.Getpid())
	for attempt := 0; attempt < 2; attempt++ {
		handle, openErr := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if openErr == nil {
			_, writeErr := handle.WriteString(token + "\n")
			closeErr := handle.Close()
			if writeErr != nil || closeErr != nil {
				_ = os.Remove(lockPath)
				return noop, false, errors.Join(writeErr, closeErr)
			}
			return func() {
				raw, readErr := os.ReadFile(lockPath)
				if readErr == nil && strings.TrimSpace(string(raw)) == token {
					_ = os.Remove(lockPath)
				}
			}, true, nil
		}
		if !os.IsExist(openErr) {
			return noop, false, openErr
		}
		info, statErr := os.Stat(lockPath)
		if statErr != nil {
			if os.IsNotExist(statErr) {
				continue
			}
			return noop, false, statErr
		}
		if time.Since(info.ModTime()) <= staleAfter {
			return noop, false, nil
		}
		if removeErr := os.Remove(lockPath); removeErr != nil && !os.IsNotExist(removeErr) {
			return noop, false, fmt.Errorf("localvault: remove stale build lock: %w", removeErr)
		}
	}
	return noop, false, nil
}
