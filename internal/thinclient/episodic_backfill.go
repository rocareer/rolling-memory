// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package thinclient

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/rocareer/rolling-memory/internal/episodicunit"
	ramesdk "github.com/rocareer/rolling-memory/sdk/go"
)

// EpisodicIndexBackfillReport describes one ordered pass over stored references.
// Cursor is the last completed document ID. Restart from an empty cursor to retry
// unavailable or changed sources, or after concurrent inserts before the cursor.
// Unavailable includes missing, unreadable, invalid and empty sources; Unverified
// includes legacy IDs, other scopes, and changed sources, which cannot be separated
// without historical identity evidence. Failed counts item-specific source and index
// integrity errors; encoding and infrastructure failures stop the pass for retry.
type EpisodicIndexBackfillReport struct {
	Cursor      string
	Scanned     int
	Indexed     int
	Unchanged   int
	Unavailable int
	Unverified  int
	Failed      int
}

// BackfillEpisodicIndexes walks stored pointers after cursor, resolving each source
// without modifying source documents. Only content-addressed IDs matching scope and
// the live source can be indexed; legacy or changed IDs remain untouched. Limit is
// a caller-supplied page size, not a retention or indexing threshold.
func (v *Vault) BackfillEpisodicIndexes(ctx context.Context, scope, cursor string, limit int) (EpisodicIndexBackfillReport, error) {
	var report EpisodicIndexBackfillReport
	report.Cursor = cursor
	if v == nil || v.db == nil {
		return report, errors.New("thinclient: nil Vault db")
	}
	if ctx == nil || strings.TrimSpace(scope) == "" || limit <= 0 {
		return report, errors.New("thinclient: episodic backfill requires context, scope and positive page size")
	}
	rows, err := v.db.QueryContext(ctx, `SELECT doc_id, raw_text FROM thin_vault_docs WHERE (layer = 'episodic' OR layer = '') AND substr(raw_text, 1, 5) = '@ref:' AND doc_id > ? ORDER BY doc_id LIMIT ?`, cursor, limit)
	if err != nil {
		return report, err
	}
	type reference struct{ id, pointer string }
	var refs []reference
	for rows.Next() {
		var ref reference
		if err := rows.Scan(&ref.id, &ref.pointer); err != nil {
			rows.Close()
			return report, err
		}
		refs = append(refs, ref)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return report, err
	}
	if err := rows.Close(); err != nil {
		return report, err
	}
	type turnCacheEntry struct {
		segs []string
		err  error
	}
	turnCache := make(map[string]turnCacheEntry, 64)
	resolveCached := func(refPointer string) (string, error) {
		trimmed := strings.TrimSpace(refPointer)
		tail := trimmed
		if colon := strings.LastIndex(tail, ":"); colon >= 0 {
			tail = tail[colon+1:]
		}
		_, segIdx, chunkIdx, hasSeg, hasChunk := splitPointerSegment(tail)
		if !hasSeg {
			return ResolveEpisodicPointerLive(trimmed)
		}
		hashIdx := strings.LastIndex(trimmed, "#")
		if hashIdx <= 0 {
			return ResolveEpisodicPointerLive(trimmed)
		}
		turnRef := trimmed[:hashIdx]
		cached, ok := turnCache[turnRef]
		if !ok {
			segs, sErr := LoadEpisodicTurnSegments(turnRef)
			cached = turnCacheEntry{segs: segs, err: sErr}
			turnCache[turnRef] = cached
		}
		if cached.err != nil {
			return "", cached.err
		}
		if segIdx < 0 || segIdx >= len(cached.segs) {
			return "", fmt.Errorf("thinclient: segment %d out of range for %s", segIdx, turnRef)
		}
		return episodicSegmentChunk(cached.segs[segIdx], segIdx, chunkIdx, hasChunk, turnRef)
	}

	key, err := v.episodicCodecKey()
	if err != nil {
		return report, err
	}

	var fastValidSnap map[string]struct{}
	var fastSnapDocs []vaultDocEntry
	var fastSnapIdx map[string]int
	if v.slotCacheMu != nil && v.snapRef != nil {
		v.slotCacheMu.RLock()
		if s := v.snapRef.snap; s != nil && s.episodicCodecKey == string(key) && len(s.validEpisodicIDs) > 0 {
			fastValidSnap = s.validEpisodicIDs
			fastSnapDocs = s.docs
			fastSnapIdx = s.docIdxByID
		}
		v.slotCacheMu.RUnlock()
	}

	for _, ref := range refs {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		if fastValidSnap != nil {
			if _, ok := fastValidSnap[ref.id]; ok {
				if dIdx, okIdx := fastSnapIdx[ref.id]; okIdx && dIdx >= 0 && dIdx < len(fastSnapDocs) && fastSnapDocs[dIdx].refPointer == ref.pointer {
					report.Unchanged++
					report.Scanned++
					report.Cursor = ref.id
					continue
				}
			}
		}

		// Fast check: when an index row for the current codec_key already exists,
		// verify its pointer binding and HMAC payload integrity before touching disk.
		var roRef string
		var roPayload, digest, mac []byte
		err = v.db.QueryRowContext(ctx, `SELECT ref_pointer, payload, content_digest, payload_mac FROM thin_vault_episodic_index WHERE doc_id = ? AND codec_key = ?`, ref.id, key).Scan(&roRef, &roPayload, &digest, &mac)
		if err == nil && len(digest) == 32 && len(mac) == 32 {
			if roRef == ref.pointer && v.validEpisodicPayloadMAC(ref.id, roRef, key, roPayload, digest, mac) {
				if _, _, dErr := ramesdk.DecodeWireBytes(roPayload); dErr == nil {
					report.Unchanged++
					report.Scanned++
					report.Cursor = ref.id
					continue
				}
			}
		}

		if !strings.HasPrefix(ref.pointer, "@ref:jsonl:") && !strings.HasPrefix(ref.pointer, "@ref:zcode:") {
			report.Unavailable++
		} else if text, err := resolveCached(ref.pointer); err != nil || strings.TrimSpace(text) == "" {
			report.Unavailable++
		} else if episodicunit.DocID(scope, text) != ref.id {
			report.Unverified++
		} else {
			created, err := v.ensureLocalEpisodicIndex(scope, ref.id, ref.pointer, text, true)
			if ctxErr := ctx.Err(); ctxErr != nil {
				return report, ctxErr
			}
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return report, fmt.Errorf("thinclient: backfill episodic index %s: %w", ref.id, err)
			}
			if errors.Is(err, ErrEpisodicIndexItem) {
				report.Failed++
			} else if err != nil {
				return report, fmt.Errorf("thinclient: backfill episodic index %s: %w", ref.id, err)
			} else if created {
				report.Indexed++
			} else {
				report.Unchanged++
			}
		}
		report.Scanned++
		report.Cursor = ref.id
	}
	return report, nil
}
