// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package thinclient

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/rocareer/rolling-memory/internal/episodic"
	"github.com/rocareer/rolling-memory/internal/episodicunit"
	ramesdk "github.com/rocareer/rolling-memory/sdk/go"
)

const episodicCodecVersion = "episodic-rmbp-v1"

const episodicIndexDDL = `CREATE TABLE IF NOT EXISTS thin_vault_episodic_index (
 doc_id TEXT NOT NULL, ref_pointer TEXT NOT NULL, codec_key BLOB NOT NULL, payload BLOB NOT NULL, content_digest BLOB, payload_mac BLOB,
 PRIMARY KEY (doc_id, codec_key)
);
CREATE TABLE IF NOT EXISTS thin_vault_episodic_revision (id INTEGER PRIMARY KEY CHECK (id = 1), revision INTEGER NOT NULL);
INSERT OR IGNORE INTO thin_vault_episodic_revision(id, revision) VALUES (1, 0);
CREATE TRIGGER IF NOT EXISTS thin_episodic_index_insert AFTER INSERT ON thin_vault_episodic_index BEGIN UPDATE thin_vault_episodic_revision SET revision = revision + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS thin_episodic_index_update AFTER UPDATE ON thin_vault_episodic_index BEGIN UPDATE thin_vault_episodic_revision SET revision = revision + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS thin_episodic_index_delete AFTER DELETE ON thin_vault_episodic_index BEGIN UPDATE thin_vault_episodic_revision SET revision = revision + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS thin_episodic_source_delete AFTER DELETE ON thin_vault_docs BEGIN DELETE FROM thin_vault_episodic_index WHERE doc_id = OLD.doc_id; END;`

// episodicCodecKey binds the secret, SDK version and explicit encoder options.
// Only named project language-pack functions are stable across process restarts;
// arbitrary closures cannot be identified safely and are rejected.
func (v *Vault) episodicCodecKey() ([]byte, error) {
	functionName := func(fn any) (string, error) {
		value := reflect.ValueOf(fn)
		if !value.IsValid() || value.IsNil() {
			return "", nil
		}
		f := runtime.FuncForPC(value.Pointer())
		if f == nil {
			return "", errors.New("thinclient: unidentifiable episodic encoder function")
		}
		name := f.Name()
		prefixSub := "github.com/rocareer/rolling-memory/internal/langpack/"
		prefixPkg := "github.com/rocareer/rolling-memory/internal/langpack."
		if (!strings.HasPrefix(name, prefixSub) && !strings.HasPrefix(name, prefixPkg)) || strings.Contains(name, ".func") || strings.HasSuffix(name, "-fm") {
			return "", errors.New("thinclient: unsupported episodic encoder function")
		}
		return name, nil
	}
	segmenter, err := functionName(v.segOpts.Segmenter)
	if err != nil {
		return nil, err
	}
	packer, err := functionName(v.segOpts.SentenceAtomPacker)
	if err != nil {
		return nil, err
	}
	expander, err := functionName(v.harmOpts.Expander)
	if err != nil {
		return nil, err
	}
	spec := fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s\x00%t\x00%t\x00%t", episodicCodecVersion, ramesdk.SDKVersion, segmenter, packer, expander, v.segOpts.HarmonicOverlay, v.segOpts.DisableAutoHarmonic, v.harmOpts.EnableStructuredDates)
	return episodicunit.HMACSHA256(v.secretKey[:], []byte(spec)), nil
}

// episodicContentDigest binds an index to exact source bytes without storing text.
func (v *Vault) episodicContentDigest(text string) []byte {
	return episodicunit.HMACSHA256(v.secretKey[:], []byte("episodic-content-v1\x00"), []byte(text))
}

// episodicPayloadMAC authenticates the complete index row with unambiguous framing.
func (v *Vault) episodicPayloadMAC(docID, refPointer string, codecKey, payload, digest []byte) []byte {
	parts := make([][]byte, 0, 11)
	parts = append(parts, []byte("episodic-index-row-v1\x00"))
	for _, field := range [][]byte{[]byte(docID), []byte(refPointer), codecKey, payload, digest} {
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(len(field)))
		sizeCopy := size
		parts = append(parts, sizeCopy[:], field)
	}
	return episodicunit.HMACSHA256(v.secretKey[:], parts...)
}

func (v *Vault) validEpisodicPayloadMAC(docID, refPointer string, codecKey, payload, digest, mac []byte) bool {
	return len(digest) == sha256.Size && len(mac) == sha256.Size && hmac.Equal(mac, v.episodicPayloadMAC(docID, refPointer, codecKey, payload, digest))
}

// ErrEpisodicPointerSourceConflict marks a pointer write refused because the stored
// row belongs to a different turn stem: identical speech reaching the vault from a
// second transcript is not an error (first pointer wins, lineage keeps both), while
// the same turn's re-enumerated `#<k>` is re-pointed instead.
var ErrEpisodicPointerSourceConflict = errors.New("thinclient: episodic document already exists with different source")

// resolveEpisodicTurnByDigest re-locates an indexed unit inside its own turn by
// content identity: the docID and the recorded digest both name the exact speech,
// while the stored `#<k>` index is positional and shifts whenever the segmentation
// filter evolves (or the harness appends a step). The search re-runs the canonical
// unit derivation (one unit per speech segment, sentence-boundary chunks under the
// tokenz-r budget) over the pointer's turn, so only the identical text can match.
func (v *Vault) resolveEpisodicTurnByDigest(refPointer string, digest []byte) (string, bool) {
	if len(digest) != sha256.Size {
		return "", false
	}
	segments, err := episodicPointerTurnSegments(refPointer)
	if err != nil {
		return "", false
	}
	return v.episodicTurnDigestMatch(segments, digest)
}

// episodicTurnDigestMatch looks the recorded content digest up in the canonical unit
// derivation of one enumerated turn (segment text, sentence-boundary chunks).
func (v *Vault) episodicTurnDigestMatch(segments []episodic.Segment, digest []byte) (string, bool) {
	for _, segment := range segments {
		rendered := episodicunit.Text(segment.Role, segment.Text)
		for _, chunk := range episodicunit.Chunk(rendered, episodicunit.TokenBudget) {
			if hmac.Equal(digest, v.episodicContentDigest(chunk)) {
				return chunk, true
			}
		}
	}
	return "", false
}

// EpisodicContentStatus classifies one indexed unit against its live source turn.
type EpisodicContentStatus int

const (
	// EpisodicContentUnreadable: the source turn cannot be enumerated (transcript
	// gone or store unavailable) — the row is kept (fail-safe, never pruned).
	EpisodicContentUnreadable EpisodicContentStatus = iota
	// EpisodicContentVerified: the stored pointer still resolves to the identical text.
	EpisodicContentVerified
	// EpisodicContentRelocatable: the turn still yields the identical text, at a
	// different `#<k>` (a re-scan re-points it; hydration relocates it meanwhile).
	EpisodicContentRelocatable
	// EpisodicContentAbsent: the turn enumerates successfully and does not contain
	// this content at all — an artifact of an older segmentation filter (the agent's
	// tool-step narration). Only this class is a prune candidate.
	EpisodicContentAbsent
)

// EpisodicPointerContentStatus classifies an indexed unit: resolvable at its stored
// index (Verified), present elsewhere in the same turn (Relocatable), provably
// absent from a successfully enumerated turn (Absent), or unclassifiable because
// the source is unavailable (Unreadable).
func (v *Vault) EpisodicPointerContentStatus(docID, refPointer string) EpisodicContentStatus {
	if v == nil || v.db == nil {
		return EpisodicContentUnreadable
	}
	refPointer = strings.TrimSpace(refPointer)
	var digest []byte
	if err := v.db.QueryRow(`SELECT content_digest FROM thin_vault_episodic_index WHERE doc_id = ? AND ref_pointer = ? AND length(content_digest) = 32 LIMIT 1`, docID, refPointer).Scan(&digest); err != nil || len(digest) != sha256.Size {
		return EpisodicContentUnreadable
	}
	if text, err := ResolveEpisodicPointerLive(refPointer); err == nil && strings.TrimSpace(text) != "" && hmac.Equal(digest, v.episodicContentDigest(text)) {
		return EpisodicContentVerified
	}
	segments, err := episodicPointerTurnSegments(refPointer)
	if err != nil {
		if strings.Contains(err.Error(), "empty zcode turn") {
			return EpisodicContentAbsent
		}
		return EpisodicContentUnreadable
	}
	if _, ok := v.episodicTurnDigestMatch(segments, digest); ok {
		return EpisodicContentRelocatable
	}
	return EpisodicContentAbsent
}

// isPollutedInlineEpisodicText reports whether a legacy non-pointer episodic row
// contains unstripped tool-call wire traces or internal thinking scratchpads.
func isPollutedInlineEpisodicText(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return true
	}
	for _, marker := range []string{
		"call:default_api:",
		"[thinking]",
		"<thinking>",
		"[tool_use",
		"[tool_result]",
		"[assistant] Thinking Process:",
		"\nThinking Process:\n",
		"[assistant] [{\"content\":",
	} {
		if strings.Contains(trimmed, marker) {
			return true
		}
	}
	return false
}

// ClassifyAndHealEpisodicUnit classifies any episodic vault row (indexed pointer,
// unindexed pointer, legacy whole-turn pointer, or inline text) and, when heal is
// true, repairs relocatable or unindexed pointers in-place once their content
// identity is verified against the live turn.
func (v *Vault) ClassifyAndHealEpisodicUnit(scope, docID, rawText string, occurredAt int64, heal bool) EpisodicContentStatus {
	if v == nil || v.db == nil {
		return EpisodicContentUnreadable
	}
	rawText = strings.TrimSpace(rawText)
	if !strings.HasPrefix(rawText, "@ref:") {
		if isPollutedInlineEpisodicText(rawText) {
			return EpisodicContentAbsent
		}
		return EpisodicContentVerified
	}
	stem := pointerStem(rawText)
	if stem == "" {
		// Legacy whole-turn pointer without `#<k>` has no segment authorization.
		return EpisodicContentAbsent
	}
	var digest []byte
	_ = v.db.QueryRow(`SELECT content_digest FROM thin_vault_episodic_index WHERE doc_id = ? AND length(content_digest) = 32 LIMIT 1`, docID).Scan(&digest)
	wasIndexed := v.HasLocalEpisodicIndex(docID, rawText)
	if len(digest) == sha256.Size {
		if text, err := ResolveEpisodicPointerLive(rawText); err == nil && strings.TrimSpace(text) != "" && hmac.Equal(digest, v.episodicContentDigest(text)) {
			if heal && !wasIndexed {
				_ = v.healEpisodicPointerUnit(docID, occurredAt, rawText, rawText, stem, text)
			}
			if !wasIndexed {
				return EpisodicContentRelocatable
			}
			return EpisodicContentVerified
		}
	}
	segments, err := episodicPointerTurnSegments(rawText)
	if err != nil {
		if strings.Contains(err.Error(), "empty zcode turn") {
			return EpisodicContentAbsent
		}
		return EpisodicContentUnreadable
	}
	for _, seg := range segments {
		for _, u := range episodicunit.SegmentUnits(scope, stem, seg, occurredAt, true) {
			matched := false
			if len(digest) == sha256.Size {
				matched = hmac.Equal(digest, v.episodicContentDigest(u.Text))
			} else {
				matched = u.DocID == docID || episodicunit.DocID("default", u.Text) == docID || episodicunit.DocID("mem:self", u.Text) == docID
			}
			if !matched {
				continue
			}
			if heal {
				_ = v.healEpisodicPointerUnit(docID, occurredAt, rawText, u.RefPointer, stem, u.Text)
			}
			if u.RefPointer == rawText && wasIndexed {
				return EpisodicContentVerified
			}
			return EpisodicContentRelocatable
		}
	}
	return EpisodicContentAbsent
}

// healEpisodicPointerUnit updates a verified episodic row's pointer to targetRef
// and ensures a MAC-authenticated index row exists under the current codec key.
func (v *Vault) healEpisodicPointerUnit(docID string, occurredAt int64, oldRaw, targetRef, turnRef, text string) error {
	key, err := v.episodicCodecKey()
	if err != nil {
		return err
	}
	pkt, _ := v.PackBlindPacket(docID, text, occurredAt)
	payload, err := ramesdk.EncodeWireBytes(pkt.Slots, pkt.SentenceFrames)
	if err != nil {
		return err
	}
	digest := v.episodicContentDigest(text)
	mac := v.episodicPayloadMAC(docID, targetRef, key, payload, digest)
	tx, err := v.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var curRaw string
	if err := tx.QueryRow(`SELECT raw_text FROM thin_vault_docs WHERE doc_id = ?`, docID).Scan(&curRaw); err != nil {
		return err
	}
	if curRaw != oldRaw && curRaw != targetRef {
		return errors.New("thinclient: episodic pointer changed concurrently")
	}
	var existingDigest []byte
	if err := tx.QueryRow(`SELECT content_digest FROM thin_vault_episodic_index WHERE doc_id = ? AND length(content_digest) = 32 LIMIT 1`, docID).Scan(&existingDigest); err == nil && len(existingDigest) == sha256.Size {
		if !hmac.Equal(existingDigest, digest) {
			return errors.New("thinclient: existing episodic content identity mismatch")
		}
	}
	if curRaw != targetRef {
		if _, err := tx.Exec(`UPDATE thin_vault_docs SET raw_text = ? WHERE doc_id = ? AND raw_text = ?`, targetRef, docID, curRaw); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`DELETE FROM thin_vault_episodic_index WHERE doc_id = ? AND codec_key <> ?`, docID, key); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO thin_vault_episodic_index(doc_id, ref_pointer, codec_key, payload, content_digest, payload_mac) VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT(doc_id, codec_key) DO UPDATE SET ref_pointer = excluded.ref_pointer, payload = excluded.payload, content_digest = excluded.content_digest, payload_mac = excluded.payload_mac`, docID, targetRef, key, payload, digest, mac); err != nil {
		return err
	}
	if turnRef = strings.TrimSpace(turnRef); turnRef != "" {
		_, _ = tx.Exec(`INSERT OR IGNORE INTO thin_vault_episodic_lineage(turn_ref, doc_id, occurred_at) VALUES (?, ?, ?)`, turnRef, docID, occurredAt)
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	v.InvalidateRuntimeCaches()
	return nil
}

// DeleteLocalEpisodicUnit removes one pointer-indexed episodic unit (document row,
// index row via the delete trigger, lineage references and the FTS mirror) when its
// stored pointer still matches expectPointer: a concurrent re-point (the scan
// healing a drifted row) keeps the row instead of deleting verified speech.
func (v *Vault) DeleteLocalEpisodicUnit(docID, expectPointer string) (bool, error) {
	if v == nil || v.db == nil {
		return false, errors.New("thinclient: nil Vault db")
	}
	docID = strings.TrimSpace(docID)
	expectPointer = strings.TrimSpace(expectPointer)
	if docID == "" || expectPointer == "" {
		return false, errors.New("thinclient: episodic delete requires a document and pointer")
	}
	tx, err := v.db.Begin()
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.Exec(`DELETE FROM thin_vault_docs WHERE doc_id = ? AND raw_text = ? AND (layer = 'episodic' OR layer = '')`, docID, expectPointer)
	if err != nil {
		return false, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if changed == 0 {
		return false, nil
	}
	if _, err = tx.Exec(`DELETE FROM thin_vault_episodic_lineage WHERE doc_id = ?`, docID); err != nil {
		return false, err
	}
	if v.hasFTS5 {
		if err = ftsDeleteRow(tx, docID); err != nil {
			return false, err
		}
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	v.InvalidateRuntimeCaches()
	return true, nil
}

// episodicPointerTurnSegments enumerates the speech segments of the turn a segment
// pointer names, ignoring the positional `#<k>` / `#<k>.<c>` suffix.
func episodicPointerTurnSegments(refPointer string) ([]episodic.Segment, error) {
	trimmed := strings.TrimSpace(refPointer)
	if strings.HasPrefix(trimmed, "@ref:zcode:") {
		rest := strings.TrimPrefix(trimmed, "@ref:zcode:")
		parts := strings.SplitN(rest, ":", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return nil, fmt.Errorf("thinclient: invalid zcode pointer %q", refPointer)
		}
		msgID := parts[1]
		if idx := strings.LastIndex(msgID, "#"); idx > 0 {
			msgID = msgID[:idx]
		}
		zdb, err := openZCodeReadOnly()
		if err != nil {
			return nil, err
		}
		return episodic.ZCodeTurnSegments(zdb, parts[0], msgID)
	}
	if strings.HasPrefix(trimmed, "@ref:jsonl:") {
		rest := strings.TrimPrefix(trimmed, "@ref:jsonl:")
		lastColon := strings.LastIndex(rest, ":")
		if lastColon <= 0 {
			return nil, fmt.Errorf("thinclient: invalid jsonl pointer %q", refPointer)
		}
		turnStr := rest[lastColon+1:]
		if idx := strings.LastIndex(turnStr, "#"); idx > 0 {
			turnStr = turnStr[:idx]
		}
		turnIndex, err := strconv.Atoi(strings.TrimSpace(turnStr))
		if err != nil || turnIndex < 0 {
			return nil, fmt.Errorf("thinclient: invalid jsonl turn index in %q", refPointer)
		}
		return episodic.JSONLTurnSegments(rest[:lastColon], turnIndex)
	}
	return nil, fmt.Errorf("thinclient: unsupported episodic pointer %q", refPointer)
}

// pointerStem returns the turn-level pointer a segment or chunk pointer belongs to
// (everything before the final `#<k>` / `#<k>.<c>` suffix); empty when the pointer
// carries no segment suffix. A stored pointer may only be re-pointed onto another
// index of the same stem, so a re-enumerated `#<k>` can never cross turns.
func pointerStem(refPointer string) string {
	trimmed := strings.TrimSpace(refPointer)
	if idx := strings.LastIndex(trimmed, "#"); idx > 0 {
		return trimmed[:idx]
	}
	return ""
}

func verifyEpisodicPointerSource(refPointer, text string) error {
	resolved, err := ResolveEpisodicPointerLive(refPointer)
	if err != nil {
		return fmt.Errorf("thinclient: resolve episodic pointer: %w", err)
	}
	if strings.TrimSpace(resolved) == "" || resolved != text {
		return errors.New("thinclient: episodic pointer content mismatch")
	}
	return nil
}

// LoadVerifiedEpisodicPointer resolves an indexed pointer only if its source still
// matches the content used for the index. Unindexed legacy pointers fail closed.
func (v *Vault) LoadVerifiedEpisodicPointer(docID, refPointer string) (string, error) {
	if v == nil || v.db == nil {
		return "", errors.New("thinclient: nil Vault db")
	}
	tail := refPointer
	if colon := strings.LastIndex(tail, ":"); colon >= 0 {
		tail = tail[colon+1:]
	}
	_, _, _, hasSegment, _ := splitPointerSegment(tail)
	if !hasSegment {
		return "", errors.New("thinclient: whole-turn pointer has no independently authorized segment")
	}
	key, err := v.episodicCodecKey()
	if err != nil {
		return "", err
	}
	trimmedRef := strings.TrimSpace(refPointer)
	isZCodeRef := strings.HasPrefix(trimmedRef, "@ref:zcode:")
	ptrCacheKey := ""
	if isZCodeRef {
		ptrCacheKey = "@zptr:" + docID + "\x00" + trimmedRef + "\x00" + string(key)
	} else if strings.HasPrefix(trimmedRef, "@ref:jsonl:") {
		rest := strings.TrimPrefix(trimmedRef, "@ref:jsonl:")
		if lastColon := strings.LastIndex(rest, ":"); lastColon > 0 {
			absPath := rest[:lastColon]
			if st, statErr := os.Stat(absPath); statErr == nil && st.Size() >= 4096 {
				ptrCacheKey = fmt.Sprintf("@jptr:%s\x00%s\x00%d\x00%d\x00%s", docID, trimmedRef, st.Size(), st.ModTime().UnixNano(), string(key))
			}
		}
	}
	var digest []byte
	if v.slotCacheMu != nil && v.snapRef != nil {
		v.slotCacheMu.RLock()
		snap := v.snapRef.snap
		if snap != nil && snap.episodicCodecKey == string(key) && len(snap.docs) >= 2048 {
			if ptrCacheKey != "" && v.slotCache != nil {
				if entry, ok := v.slotCache[ptrCacheKey]; ok {
					if entry.docLen < 0 {
						v.slotCacheMu.RUnlock()
						return "", errors.New("thinclient: cached unavailable episodic pointer")
					}
					if _, inValidSnap := snap.validEpisodicIDs[docID]; inValidSnap && entry.rawText != "" {
						txt := entry.rawText
						v.slotCacheMu.RUnlock()
						return txt, nil
					}
				}
			}
			if _, inValidSnap := snap.validEpisodicIDs[docID]; inValidSnap {
				if dIdx, ok := snap.docIdxByID[docID]; ok && dIdx >= 0 && dIdx < len(snap.docs) {
					d := snap.docs[dIdx]
					if d.refPointer == refPointer && len(d.contentDigest) == sha256.Size {
						digest = d.contentDigest
					}
				}
			}
		}
		v.slotCacheMu.RUnlock()
	}
	if len(digest) != sha256.Size {
		var payload, mac, storedKey []byte
		// Multi-generation union read (current generation preferred): hydration
		// of a pointer unit must survive an SDK or language-pack generation bump
		// because the stored payload is self-describing and authenticated by the
		// key it was written under.
		err = v.db.QueryRow(`SELECT i.payload, i.content_digest, i.payload_mac, i.codec_key FROM thin_vault_docs d JOIN thin_vault_episodic_index i ON i.doc_id = d.doc_id AND i.ref_pointer = d.raw_text WHERE d.doc_id = ? AND d.raw_text = ? ORDER BY (i.codec_key = ?) DESC, i.rowid DESC LIMIT 1`, docID, refPointer, key).Scan(&payload, &digest, &mac, &storedKey)
		if err != nil {
			if ptrCacheKey != "" && v.slotCacheMu != nil && v.CorpusSize() >= 2048 {
				v.slotCacheMu.Lock()
				if v.slotCache == nil {
					v.slotCache = make(map[string]vaultCachedDocSlots, 256)
				}
				v.slotCache[ptrCacheKey] = vaultCachedDocSlots{docLen: -1}
				v.slotCacheMu.Unlock()
			}
			return "", fmt.Errorf("thinclient: episodic index identity unavailable for %s: %w", docID, err)
		}
		if !v.validEpisodicPayloadMAC(docID, refPointer, storedKey, payload, digest, mac) {
			if ptrCacheKey != "" && v.slotCacheMu != nil && v.CorpusSize() >= 2048 {
				v.slotCacheMu.Lock()
				if v.slotCache == nil {
					v.slotCache = make(map[string]vaultCachedDocSlots, 256)
				}
				v.slotCache[ptrCacheKey] = vaultCachedDocSlots{docLen: -1}
				v.slotCacheMu.Unlock()
			}
			return "", fmt.Errorf("thinclient: episodic index integrity unavailable for %s", docID)
		}
	}
	text, err := ResolveEpisodicPointerLive(refPointer)
	if err != nil || strings.TrimSpace(text) == "" || !hmac.Equal(digest, v.episodicContentDigest(text)) {
		// The stored `#<k>` is positional and shifts whenever the segmentation filter
		// evolves (or the harness appends a step to the turn): re-locate the unit inside
		// the same turn by content identity — the recorded digest is bound to the exact
		// text — before failing closed.
		if relocated, ok := v.resolveEpisodicTurnByDigest(refPointer, digest); ok {
			text, err = relocated, nil
		}
	}
	if err != nil {
		// Only negative-cache permanent missing/invalid pointer errors; transient SQLite
		// lock contention on the harness DB must not poison the slot cache.
		errMsg := strings.ToLower(err.Error())
		isTransientLock := strings.Contains(errMsg, "busy") || strings.Contains(errMsg, "locked")
		if !isTransientLock && ptrCacheKey != "" && v.slotCacheMu != nil && v.CorpusSize() >= 2048 {
			v.slotCacheMu.Lock()
			if v.slotCache == nil {
				v.slotCache = make(map[string]vaultCachedDocSlots, 256)
			}
			v.slotCache[ptrCacheKey] = vaultCachedDocSlots{docLen: -1}
			v.slotCacheMu.Unlock()
		}
		return "", fmt.Errorf("thinclient: resolve episodic pointer for %s: %w", docID, err)
	}
	if strings.TrimSpace(text) == "" || !hmac.Equal(digest, v.episodicContentDigest(text)) {
		if ptrCacheKey != "" && v.slotCacheMu != nil && v.CorpusSize() >= 2048 {
			v.slotCacheMu.Lock()
			if v.slotCache == nil {
				v.slotCache = make(map[string]vaultCachedDocSlots, 256)
			}
			v.slotCache[ptrCacheKey] = vaultCachedDocSlots{docLen: -1}
			v.slotCacheMu.Unlock()
		}
		return "", fmt.Errorf("thinclient: episodic pointer content mismatch for %s", docID)
	}
	if ptrCacheKey != "" && v.slotCacheMu != nil {
		v.slotCacheMu.Lock()
		if v.slotCache == nil {
			v.slotCache = make(map[string]vaultCachedDocSlots, 256)
		}
		v.slotCache[ptrCacheKey] = vaultCachedDocSlots{rawText: text}
		v.slotCacheMu.Unlock()
	}
	return text, nil
}

// EpisodicTurnDocuments returns only stored identities explicitly linked to a turn.
// Callers must still verify each pointer before delivering its content.
func (v *Vault) EpisodicTurnDocuments(turnRef string) (map[string]string, error) {
	rows, err := v.db.Query(`SELECT d.doc_id, d.raw_text FROM thin_vault_episodic_lineage l JOIN thin_vault_docs d ON d.doc_id = l.doc_id WHERE l.turn_ref = ?`, turnRef)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	docs := make(map[string]string)
	for rows.Next() {
		var id, pointer string
		if err := rows.Scan(&id, &pointer); err != nil {
			return nil, err
		}
		docs[id] = pointer
	}
	return docs, rows.Err()
}

// VerifiedTurnFirstSegment resolves the turn's first speech segment (`#0`), which by
// construction is the user message that opened the turn, when a stored unit for it still
// passes content verification. Recall delivery prepends it so the reader always sees what
// the user asked, even when retrieval matched only later assistant segments.
func (v *Vault) VerifiedTurnFirstSegment(turnRef string) (string, bool) {
	turnRef = strings.TrimSpace(turnRef)
	if v == nil || v.db == nil || turnRef == "" {
		return "", false
	}
	docs, err := v.EpisodicTurnDocuments(turnRef)
	if err != nil || len(docs) == 0 {
		return "", false
	}
	ids := make([]string, 0, len(docs))
	for id := range docs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	bestID, bestPointer, bestChunk := "", "", -1
	for _, id := range ids {
		pointer := strings.TrimSpace(docs[id])
		colon := strings.LastIndex(pointer, ":")
		if colon <= 0 || colon == len(pointer)-1 {
			continue
		}
		_, segmentIndex, chunkIndex, hasSegment, hasChunk := splitPointerSegment(pointer[colon+1:])
		if !hasSegment || segmentIndex != 0 {
			continue
		}
		if !hasChunk {
			// A whole-segment `#0` unit outranks any chunked variant.
			bestID, bestPointer, bestChunk = id, pointer, -1
			break
		}
		if bestChunk < 0 || chunkIndex < bestChunk {
			bestID, bestPointer, bestChunk = id, pointer, chunkIndex
		}
	}
	if bestID == "" {
		return "", false
	}
	text, err := v.LoadVerifiedEpisodicPointer(bestID, bestPointer)
	if err != nil || strings.TrimSpace(text) == "" {
		return "", false
	}
	return text, true
}

// SaveLocalEpisodicPointerIndexed stores a pointer and its canonical, plaintext-free
// scoring packet in one transaction. pkt must be the packet produced by this Vault
// for livePlaintext under its current key and segmentation options.
func (v *Vault) SaveLocalEpisodicPointerIndexed(docID string, occurredAt int64, refPointer, turnRef, livePlaintext string, pkt BlindPulsePacket, resp CloudIngestResponse) error {
	if v == nil || v.db == nil {
		return errors.New("thinclient: nil Vault db")
	}
	refPointer = strings.TrimSpace(refPointer)
	if !strings.HasPrefix(refPointer, "@ref:") || strings.TrimSpace(docID) == "" || strings.TrimSpace(livePlaintext) == "" {
		return errors.New("thinclient: episodic index requires a document, pointer and plaintext")
	}
	if pkt.UnitID != docID {
		return errors.New("thinclient: episodic packet document mismatch")
	}
	if err := verifyEpisodicPointerSource(refPointer, livePlaintext); err != nil {
		return err
	}
	expected, _ := v.PackBlindPacket(docID, livePlaintext, occurredAt)
	payload, err := ramesdk.EncodeWireBytes(pkt.Slots, pkt.SentenceFrames)
	if err != nil {
		return fmt.Errorf("thinclient: encode episodic packet: %w", err)
	}
	canonical, err := ramesdk.EncodeWireBytes(expected.Slots, expected.SentenceFrames)
	if err != nil {
		return err
	}
	if !bytes.Equal(payload, canonical) {
		return errors.New("thinclient: episodic packet key or encoder mismatch")
	}
	codecKey, err := v.episodicCodecKey()
	if err != nil {
		return err
	}
	keptJSON, err := json.Marshal(resp.KeptSentenceIndices)
	if err != nil {
		return err
	}
	tx, err := v.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	digest := v.episodicContentDigest(livePlaintext)
	var existingRaw string
	err = tx.QueryRow(`SELECT raw_text FROM thin_vault_docs WHERE doc_id = ?`, docID).Scan(&existingRaw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var storedPointer string
	var storedDigest, storedPayload, storedMAC []byte
	repoint := false
	if err == nil {
		if existingRaw == refPointer {
			err = tx.QueryRow(`SELECT ref_pointer, content_digest, payload, payload_mac FROM thin_vault_episodic_index WHERE doc_id = ? AND ref_pointer = ? AND codec_key = ?`, docID, refPointer, codecKey).Scan(&storedPointer, &storedDigest, &storedPayload, &storedMAC)
		} else {
			// The docID is content-addressed (`episodicunit.DocID`), so a differing raw_text
			// means the same speech re-enumerated under a new `#<k>` index (the sources are a
			// stream: a segmentation change or an appended part shifts later indices). Accept
			// only the same turn pointer stem and only when the recorded content identity
			// matches, then re-point the row to the current index instead of refusing the
			// turn forever with stale `#<k>`.
			if stem := pointerStem(existingRaw); stem == "" || stem != pointerStem(refPointer) {
				return ErrEpisodicPointerSourceConflict
			}
			err = tx.QueryRow(`SELECT ref_pointer, content_digest, payload, payload_mac FROM thin_vault_episodic_index WHERE doc_id = ? AND ref_pointer = ? AND codec_key = ?`, docID, existingRaw, codecKey).Scan(&storedPointer, &storedDigest, &storedPayload, &storedMAC)
			repoint = true
		}
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("thinclient: existing episodic document identity is unverified; ensure index first")
		}
		if err != nil {
			return err
		}
		if !v.validEpisodicPayloadMAC(docID, storedPointer, codecKey, storedPayload, storedDigest, storedMAC) {
			return errors.New("thinclient: existing episodic index integrity unavailable; rebuild required")
		}
		if !hmac.Equal(storedDigest, digest) {
			return errors.New("thinclient: existing episodic content identity mismatch")
		}
		if !bytes.Equal(storedPayload, payload) {
			return errors.New("thinclient: existing episodic packet mismatch")
		}
	}
	_, err = tx.Exec(`INSERT INTO thin_vault_docs(doc_id, layer, occurred_at, inserted_at, raw_text, sentences_json, address_uri, coord64, waveform_bar, doc_macro_wave, kept_indices_json, assistant_id, incarnation_id, locus) VALUES (?, 'episodic', ?, ?, ?, '[]', ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(doc_id) DO UPDATE SET occurred_at=excluded.occurred_at, inserted_at=CASE WHEN thin_vault_docs.inserted_at > 0 THEN thin_vault_docs.inserted_at ELSE excluded.inserted_at END, address_uri=excluded.address_uri, coord64=excluded.coord64, waveform_bar=excluded.waveform_bar, doc_macro_wave=excluded.doc_macro_wave, kept_indices_json=excluded.kept_indices_json, assistant_id=excluded.assistant_id, incarnation_id=excluded.incarnation_id, locus=excluded.locus`, docID, occurredAt, occurredAt, refPointer, resp.AddressURI, int64(resp.AddressCoord64), resp.WaveformBar, resp.DocMacroWaveform, string(keptJSON), v.identity.AssistantID, v.identity.IncarnationID, v.identity.Locus)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT INTO thin_vault_episodic_index(doc_id, ref_pointer, codec_key, payload, content_digest, payload_mac) VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT(doc_id, codec_key) DO NOTHING`, docID, refPointer, codecKey, payload, digest, v.episodicPayloadMAC(docID, refPointer, codecKey, payload, digest)); err != nil {
		return err
	}
	if repoint {
		// Same content, new enumeration index: move the stored pointer to the index the
		// current filter names, and re-authenticate the index row against it (the MAC is
		// bound to the ref pointer), so verification and hydration agree again.
		if _, err = tx.Exec(`UPDATE thin_vault_docs SET raw_text = ? WHERE doc_id = ? AND raw_text = ?`, refPointer, docID, existingRaw); err != nil {
			return err
		}
		if _, err = tx.Exec(`UPDATE thin_vault_episodic_index SET ref_pointer = ?, payload_mac = ? WHERE doc_id = ? AND codec_key = ? AND ref_pointer = ?`, refPointer, v.episodicPayloadMAC(docID, refPointer, codecKey, payload, digest), docID, codecKey, existingRaw); err != nil {
			return err
		}
	}
	if v.hasFTS5 {
		if err = ftsDeleteRow(tx, docID); err != nil {
			return err
		}
	}
	if turnRef = strings.TrimSpace(turnRef); turnRef != "" {
		if _, err = tx.Exec(`INSERT OR IGNORE INTO thin_vault_episodic_lineage(turn_ref, doc_id, occurred_at) VALUES (?, ?, ?)`, turnRef, docID, occurredAt); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	v.InvalidateRuntimeCaches()
	return nil
}

// ErrEpisodicIndexItem identifies an individual source or index integrity failure.
var ErrEpisodicIndexItem = errors.New("thinclient: episodic index item")

// EnsureLocalEpisodicIndex indexes a content-addressed live source without replacing
// another encoder's packet or an established content identity.
func (v *Vault) EnsureLocalEpisodicIndex(scope, docID, refPointer, text string) (bool, error) {
	return v.ensureLocalEpisodicIndex(scope, docID, refPointer, text, false)
}

// HasLocalEpisodicIndex reports whether docID already has a MAC-verified index row
// for refPointer under the current episodic encoder.
func (v *Vault) HasLocalEpisodicIndex(docID, refPointer string) bool {
	if v == nil || v.db == nil {
		return false
	}
	key, err := v.episodicCodecKey()
	if err != nil {
		return false
	}
	var payload, digest, mac []byte
	if err := v.db.QueryRow(`SELECT payload, content_digest, payload_mac FROM thin_vault_episodic_index WHERE doc_id = ? AND ref_pointer = ? AND codec_key = ?`, docID, strings.TrimSpace(refPointer), key).Scan(&payload, &digest, &mac); err != nil {
		return false
	}
	return v.validEpisodicPayloadMAC(docID, strings.TrimSpace(refPointer), key, payload, digest, mac)
}

func (v *Vault) ensureLocalEpisodicIndex(scope, docID, refPointer, text string, sourceAlreadyVerified bool) (bool, error) {
	if v == nil || v.db == nil {
		return false, errors.New("thinclient: nil Vault db")
	}
	refPointer = strings.TrimSpace(refPointer)
	if strings.TrimSpace(scope) == "" || strings.TrimSpace(docID) == "" || !strings.HasPrefix(refPointer, "@ref:") || strings.TrimSpace(text) == "" {
		return false, fmt.Errorf("%w: requires a scope, document, pointer and plaintext", ErrEpisodicIndexItem)
	}
	if episodicunit.DocID(scope, text) != docID {
		return false, fmt.Errorf("%w: document content identity mismatch", ErrEpisodicIndexItem)
	}
	key, err := v.episodicCodecKey()
	if err != nil {
		return false, err
	}
	digest := v.episodicContentDigest(text)

	// Fast read-only check before touching the live transcript or re-packing: when the
	// document and its verified index row already match the current codec, content
	// digest and MAC, return without opening the harness store or taking a write lock.
	var roRaw, roRef string
	var roPayload, roDigest, roMAC []byte
	if v.db.QueryRow(`SELECT raw_text FROM thin_vault_docs WHERE doc_id = ?`, docID).Scan(&roRaw) == nil && roRaw == refPointer {
		if v.db.QueryRow(`SELECT ref_pointer, payload, content_digest, payload_mac FROM thin_vault_episodic_index WHERE doc_id = ? AND codec_key = ?`, docID, key).Scan(&roRef, &roPayload, &roDigest, &roMAC) == nil {
			if roRef == refPointer && len(roDigest) == 32 && hmac.Equal(roDigest, digest) && len(roMAC) == 32 && v.validEpisodicPayloadMAC(docID, roRef, key, roPayload, roDigest, roMAC) {
				if _, _, dErr := ramesdk.DecodeWireBytes(roPayload); dErr == nil {
					return false, nil
				}
			}
		}
	}

	if !sourceAlreadyVerified {
		if err := verifyEpisodicPointerSource(refPointer, text); err != nil {
			return false, fmt.Errorf("%w: %w", ErrEpisodicIndexItem, err)
		}
	}

	pkt, _ := v.PackBlindPacket(docID, text, 0)
	payload, err := ramesdk.EncodeWireBytes(pkt.Slots, pkt.SentenceFrames)
	if err != nil {
		return false, err
	}
	mac := v.episodicPayloadMAC(docID, refPointer, key, payload, digest)

	tx, err := v.db.Begin()
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	var raw string
	if err := tx.QueryRow(`SELECT raw_text FROM thin_vault_docs WHERE doc_id = ?`, docID).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, fmt.Errorf("%w: document unavailable: %w", ErrEpisodicIndexItem, err)
		}
		return false, err
	}
	if raw != refPointer {
		return false, fmt.Errorf("%w: episodic pointer changed", ErrEpisodicIndexItem)
	}
	var storedRef string
	var storedPayload, storedDigest, storedMAC []byte
	err = tx.QueryRow(`SELECT ref_pointer, payload, content_digest, payload_mac FROM thin_vault_episodic_index WHERE doc_id = ? AND codec_key = ?`, docID, key).Scan(&storedRef, &storedPayload, &storedDigest, &storedMAC)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	if err == nil {
		if storedRef != refPointer || !bytes.Equal(storedPayload, payload) || (len(storedDigest) != 0 && !hmac.Equal(storedDigest, digest)) {
			return false, fmt.Errorf("%w: existing episodic index identity mismatch", ErrEpisodicIndexItem)
		}
		if len(storedMAC) != 0 {
			if !v.validEpisodicPayloadMAC(docID, storedRef, key, storedPayload, storedDigest, storedMAC) {
				return false, fmt.Errorf("%w: existing episodic index integrity mismatch", ErrEpisodicIndexItem)
			}
			return false, nil
		}
		if _, err = tx.Exec(`UPDATE thin_vault_episodic_index SET content_digest = ?, payload_mac = ? WHERE doc_id = ? AND codec_key = ?`, digest, mac, docID, key); err != nil {
			return false, err
		}
	} else if _, err = tx.Exec(`INSERT INTO thin_vault_episodic_index(doc_id, ref_pointer, codec_key, payload, content_digest, payload_mac) VALUES (?, ?, ?, ?, ?, ?)`, docID, refPointer, key, payload, digest, mac); err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	v.InvalidateRuntimeCaches()
	return true, nil
}

// EpisodicIndexDiagnostics reports pointer coverage for the current encoder without
// opening source transcripts. Mismatched entries need caller-supplied reindexing.
func (v *Vault) EpisodicIndexDiagnostics() (compatible, incompatible, missing int, err error) {
	if v == nil || v.db == nil {
		return 0, 0, 0, errors.New("thinclient: nil Vault db")
	}
	key, err := v.episodicCodecKey()
	if err != nil {
		return 0, 0, 0, err
	}
	report, err := v.episodicIndexFaultDiagnostics(key)
	return report.Compatible, report.Incompatible, report.Missing, err
}

func (v *Vault) indexedEpisodicDocs() ([]vaultDocEntry, error) {
	return v.indexedEpisodicDocsContext(context.Background())
}

func (v *Vault) indexedEpisodicDocsContext(ctx context.Context) ([]vaultDocEntry, error) {
	docs, _, err := v.indexedEpisodicDocsSince(ctx, 0)
	return docs, err
}

func (v *Vault) indexedEpisodicDocsSince(ctx context.Context, minIndexRowID int64) ([]vaultDocEntry, int64, error) {
	key, err := v.episodicCodecKey()
	if err != nil {
		return nil, minIndexRowID, nil
	}
	var rows *sql.Rows
	// Multi-generation union read: index rows written by any historical codec
	// generation stay loadable (the wire payload is self-describing and each
	// row's MAC is verified with the row's own stored key), because an SDK or
	// language-pack bump must never orphan previously indexed speech. Rows are
	// ordered so the current generation wins per document; both the full load
	// and the incremental catch-up share the same query shape so rows written
	// by a differently-configured sibling process are never missed.
	selCols := `SELECT i.rowid, d.doc_id, d.layer, d.occurred_at, d.address_uri, d.coord64, d.waveform_bar, COALESCE(d.locus, ''), i.ref_pointer, i.codec_key, i.payload, i.content_digest, i.payload_mac FROM thin_vault_docs d JOIN thin_vault_episodic_index i ON i.doc_id = d.doc_id AND i.ref_pointer = d.raw_text WHERE d.layer = 'episodic'`
	if minIndexRowID > 0 {
		rows, err = v.db.QueryContext(ctx, selCols+` AND i.rowid > ? ORDER BY d.doc_id, (i.codec_key = ?) DESC, i.rowid DESC`, minIndexRowID, key)
	} else {
		rows, err = v.db.QueryContext(ctx, selCols+` ORDER BY d.doc_id, (i.codec_key = ?) DESC, i.rowid DESC`, key)
	}
	if err != nil {
		return nil, minIndexRowID, err
	}
	defer rows.Close()
	type rawEpisodicRow struct {
		rowID      int64
		docID      string
		layer      string
		occ        int64
		addrURI    string
		coord64    uint64
		waveBar    string
		locus      string
		refPointer string
		storedKey  []byte
		payload    []byte
		digest     []byte
		mac        []byte
		cacheKey   string
		ok         bool
		entry      vaultDocEntry
	}
	var cachedByMAC map[string]vaultDocEntry
	if v.slotCacheMu != nil && v.snapRef != nil {
		v.slotCacheMu.RLock()
		if v.snapRef.epiCodecKey == string(key) && len(v.snapRef.epiByMAC) > 0 {
			cachedByMAC = v.snapRef.epiByMAC
		}
		v.slotCacheMu.RUnlock()
	}
	maxSeenRowID := minIndexRowID
	var rawRows []rawEpisodicRow
	var toDecodeIdx []int
	seenDocIDs := make(map[string]bool, 4096)
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return nil, minIndexRowID, err
		}
		var r rawEpisodicRow
		var coord int64
		if err := rows.Scan(&r.rowID, &r.docID, &r.layer, &r.occ, &r.addrURI, &coord, &r.waveBar, &r.locus, &r.refPointer, &r.storedKey, &r.payload, &r.digest, &r.mac); err != nil {
			return nil, minIndexRowID, err
		}
		if r.rowID > maxSeenRowID {
			maxSeenRowID = r.rowID
		}
		// Deduplicate across generations: the SQL orders current-generation rows
		// first, so the first row seen for a document is the one to keep.
		if seenDocIDs[r.docID] {
			continue
		}
		seenDocIDs[r.docID] = true
		r.coord64 = uint64(coord)
		if len(r.mac) == sha256.Size && len(r.digest) == sha256.Size {
			r.cacheKey = r.docID + "\x00" + r.refPointer + "\x00" + string(r.mac)
			if cachedByMAC != nil {
				if prev, ok := cachedByMAC[r.cacheKey]; ok {
					prev.layer = r.layer
					prev.occ = r.occ
					prev.addrURI = r.addrURI
					prev.coord64 = r.coord64
					prev.waveBar = r.waveBar
					prev.locus = strings.TrimSpace(r.locus)
					prev.refPointer = r.refPointer
					prev.contentDigest = r.digest
					r.entry = prev
					r.ok = true
					r.payload = nil
					rawRows = append(rawRows, r)
					continue
				}
			}
		}
		toDecodeIdx = append(toDecodeIdx, len(rawRows))
		rawRows = append(rawRows, r)
	}
	if err := rows.Err(); err != nil {
		return nil, minIndexRowID, err
	}
	rows.Close()
	if len(rawRows) == 0 {
		return nil, maxSeenRowID, nil
	}

	decodeOne := func(r *rawEpisodicRow) {
		// Accept any historical codec generation: the payload is verified with
		// the key the row itself was written under, so an SDK or language-pack
		// bump cannot orphan previously indexed speech.
		if len(r.storedKey) == 0 || !v.validEpisodicPayloadMAC(r.docID, r.refPointer, r.storedKey, r.payload, r.digest, r.mac) {
			return
		}
		pkt, err := v.decodeEpisodicIndex(r.docID, r.storedKey, r.payload, r.storedKey)
		if err != nil {
			return
		}
		doc := vaultDocEntry{
			docID:         r.docID,
			layer:         r.layer,
			occ:           r.occ,
			addrURI:       r.addrURI,
			coord64:       r.coord64,
			waveBar:       r.waveBar,
			locus:         strings.TrimSpace(r.locus),
			refPointer:    r.refPointer,
			contentDigest: r.digest,
			termCounts:    make(map[uint64]int, len(pkt.Slots)/2+4),
		}
		for _, atom := range pkt.Slots {
			doc.termCounts[atom.H]++
			doc.docLen++
		}
		if len(pkt.SentenceFrames) > 0 {
			doc.sentHashes = make([][]uint64, 0, len(pkt.SentenceFrames))
			for _, frame := range pkt.SentenceFrames {
				seen := make(map[uint64]bool, len(frame))
				var hashes []uint64
				for _, atom := range frame {
					if !seen[atom.H] {
						seen[atom.H] = true
						hashes = append(hashes, atom.H)
					}
				}
				if len(hashes) > 0 {
					doc.sentHashes = append(doc.sentHashes, hashes)
				}
			}
		}
		if doc.docLen < 1 {
			doc.docLen = 1
		}
		r.payload = nil
		r.entry = doc
		r.ok = true
	}

	if len(toDecodeIdx) >= 32 {
		workers := runtime.NumCPU()
		if workers < 2 {
			workers = 2
		}
		if workers > 12 {
			workers = 12
		}
		var wg sync.WaitGroup
		step := (len(toDecodeIdx) + workers - 1) / workers
		for w := 0; w < workers; w++ {
			start := w * step
			if start >= len(toDecodeIdx) {
				break
			}
			end := start + step
			if end > len(toDecodeIdx) {
				end = len(toDecodeIdx)
			}
			wg.Add(1)
			go func(s, e int) {
				defer wg.Done()
				for i := s; i < e; i++ {
					if ctx.Err() != nil {
						return
					}
					decodeOne(&rawRows[toDecodeIdx[i]])
				}
			}(start, end)
		}
		wg.Wait()
		if err := ctx.Err(); err != nil {
			return nil, minIndexRowID, err
		}
	} else {
		for _, idx := range toDecodeIdx {
			if err := ctx.Err(); err != nil {
				return nil, minIndexRowID, err
			}
			decodeOne(&rawRows[idx])
		}
	}

	docs := make([]vaultDocEntry, 0, len(rawRows))
	if v.slotCacheMu != nil && v.snapRef != nil {
		v.slotCacheMu.Lock()
		if v.snapRef.epiCodecKey != string(key) || v.snapRef.epiByMAC == nil || minIndexRowID == 0 {
			v.snapRef.epiCodecKey = string(key)
			v.snapRef.epiByMAC = make(map[string]vaultDocEntry, len(rawRows))
		}
		for idx := range rawRows {
			if rawRows[idx].ok {
				docs = append(docs, rawRows[idx].entry)
				if rawRows[idx].cacheKey != "" {
					v.snapRef.epiByMAC[rawRows[idx].cacheKey] = rawRows[idx].entry
				}
			}
		}
		v.slotCacheMu.Unlock()
	} else {
		for idx := range rawRows {
			if rawRows[idx].ok {
				docs = append(docs, rawRows[idx].entry)
			}
		}
	}
	return docs, maxSeenRowID, nil
}

func (v *Vault) decodeEpisodicIndex(ref string, key, payload, expectedKey []byte) (BlindPulsePacket, error) {
	if !hmac.Equal(key, expectedKey) {
		return BlindPulsePacket{}, fmt.Errorf("thinclient: episodic index encoder mismatch for %s", ref)
	}
	slots, frames, err := ramesdk.DecodeWireBytes(payload)
	if err != nil {
		return BlindPulsePacket{}, fmt.Errorf("thinclient: decode episodic index: %w", err)
	}
	return BlindPulsePacket{Slots: slots, SentenceFrames: frames}, nil
}
