// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package localvault

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"runtime"
	"sync"
	"time"

	plain "github.com/rocareer/rolling-memory/sdk/go/pulse/plain"
)

const episodicFaultCacheMetaKey = "episodic_fault_report_cache"

type persistedEpisodicFaultCache struct {
	DocCnt      int64                    `json:"doc_cnt"`
	DocRowSum   int64                    `json:"doc_row_sum"`
	DocMaxRowID int64                    `json:"doc_max_rowid"`
	DocTextLen  int64                    `json:"doc_text_len"`
	EpisodicRev int64                    `json:"episodic_rev"`
	IdxCnt      int64                    `json:"idx_cnt"`
	CodecKeyHex string                   `json:"codec_key_hex"`
	Report      EpisodicIndexFaultReport `json:"report"`
}

// EpisodicIndexFaultReport counts pointer coverage, corrupt compatible payloads,
// and index rows whose source document no longer exists. It never changes data.
type EpisodicIndexFaultReport struct {
	Compatible   int
	Incompatible int
	Missing      int
	Corrupt      int
	NeedsRebuild int
	Orphaned     int
}

// EpisodicIndexFaultDiagnostics inspects the current index without opening sources.
func (v *Vault) EpisodicIndexFaultDiagnostics() (EpisodicIndexFaultReport, error) {
	if v == nil || v.db == nil {
		return EpisodicIndexFaultReport{}, errors.New("localvault: nil Vault db")
	}
	key, err := v.episodicCodecKey()
	if err != nil {
		return EpisodicIndexFaultReport{}, err
	}
	return v.episodicIndexFaultDiagnostics(key)
}

func (v *Vault) episodicIndexFaultDiagnostics(key []byte) (EpisodicIndexFaultReport, error) {
	if v.slotCacheMu != nil && v.snapRef != nil {
		v.slotCacheMu.RLock()
		ready := v.snapRef.faultReady && v.snapRef.faultCodecKey == string(key)
		hasLargeSnap := v.snapRef.snap != nil && len(v.snapRef.snap.docs) >= 2048
		cached := v.snapRef.faultReport
		v.slotCacheMu.RUnlock()
		if ready && hasLargeSnap {
			return cached, nil
		}
	}
	return v.computeEpisodicIndexFaultDiagnostics(key)
}

func (v *Vault) computeEpisodicIndexFaultDiagnostics(key []byte) (EpisodicIndexFaultReport, error) {
	var report EpisodicIndexFaultReport
	var docCnt, docRowSum, docMaxRowID, docTextLen, episodicRevision, idxCnt int64
	hasFingerprint := false
	var snapValidIDs map[string]struct{}
	if v.slotCacheMu != nil && v.snapRef != nil {
		if v.db.QueryRow(`SELECT
				(SELECT COUNT(*) FROM rm_units),
				(SELECT COALESCE(SUM(rowid), 0) FROM rm_docs),
				(SELECT COALESCE(MAX(rowid), 0) FROM rm_docs),
				(SELECT COALESCE(SUM(LENGTH(raw_text)), 0) FROM rm_units)`).
			Scan(&docCnt, &docRowSum, &docMaxRowID, &docTextLen) == nil &&
			v.db.QueryRow(`SELECT episodic_rev FROM rm_revision WHERE id = 1`).Scan(&episodicRevision) == nil &&
			v.db.QueryRow(`SELECT COUNT(*) FROM rm_episodic_index`).Scan(&idxCnt) == nil {
			hasFingerprint = true
			v.slotCacheMu.RLock()
			if v.snapRef.faultReady &&
				v.snapRef.faultDocCnt == docCnt &&
				v.snapRef.faultDocRowSum == docRowSum &&
				v.snapRef.faultMaxRowID == docMaxRowID &&
				v.snapRef.faultDocTextLen == docTextLen &&
				v.snapRef.faultEpisodicRev == episodicRevision &&
				v.snapRef.faultIdxCnt == idxCnt &&
				v.snapRef.faultCodecKey == string(key) {
				cached := v.snapRef.faultReport
				v.slotCacheMu.RUnlock()
				v.slotCacheMu.Lock()
				v.snapRef.lastFaultTime = time.Now()
				v.slotCacheMu.Unlock()
				return cached, nil
			}
			if s := v.snapRef.snap; s != nil &&
				s.docCnt == docCnt &&
				s.docRowSum == docRowSum &&
				s.maxRowID == docMaxRowID &&
				s.docTextLen == docTextLen &&
				s.episodicRevision == episodicRevision &&
				s.episodicCodecKey == string(key) {
				snapValidIDs = s.validEpisodicIDs
			}
			v.slotCacheMu.RUnlock()
			if idxCnt >= 1024 && len(snapValidIDs) == 0 {
				var rawCache string
				if v.db.QueryRow(`SELECT CAST(v AS TEXT) FROM rm_meta WHERE k = ?`, episodicFaultCacheMetaKey).Scan(&rawCache) == nil && rawCache != "" {
					var pc persistedEpisodicFaultCache
					if json.Unmarshal([]byte(rawCache), &pc) == nil &&
						pc.DocCnt == docCnt &&
						pc.DocRowSum == docRowSum &&
						pc.DocMaxRowID == docMaxRowID &&
						pc.DocTextLen == docTextLen &&
						pc.EpisodicRev == episodicRevision &&
						pc.IdxCnt == idxCnt &&
						pc.CodecKeyHex == hex.EncodeToString(key) {
						v.slotCacheMu.Lock()
						v.snapRef.faultReady = true
						v.snapRef.lastFaultTime = time.Now()
						v.snapRef.faultDocCnt = docCnt
						v.snapRef.faultDocRowSum = docRowSum
						v.snapRef.faultMaxRowID = docMaxRowID
						v.snapRef.faultDocTextLen = docTextLen
						v.snapRef.faultEpisodicRev = episodicRevision
						v.snapRef.faultIdxCnt = idxCnt
						v.snapRef.faultCodecKey = string(key)
						v.snapRef.faultReport = pc.Report
						v.slotCacheMu.Unlock()
						return pc.Report, nil
					}
				}
				if s, release, sErr := v.acquireCorpusSnapshot(context.Background()); sErr == nil && s != nil {
					if s.docCnt == docCnt &&
						s.docRowSum == docRowSum &&
						s.maxRowID == docMaxRowID &&
						s.docTextLen == docTextLen &&
						s.episodicRevision == episodicRevision &&
						s.episodicCodecKey == string(key) {
						snapValidIDs = s.validEpisodicIDs
					}
					release()
				}

			}
		}
	}

	// Fast path when `loadCorpusSnapshot` already verified all compatible rows under the
	// exact same table & revision fingerprint: every docID in `snapValidIDs` has already
	// passed `i.ref_pointer == d.raw_text`, 32-byte digest/MAC, HMAC verification, and
	// `DecodeWireBytes` under this exact revision and codecKey.
	if len(snapValidIDs) > 0 {
		lightRows, lErr := v.db.Query(`
			SELECT d.doc_id, d.raw_text, i.ref_pointer, i.codec_key IS NOT NULL,
			       COALESCE(length(i.content_digest), 0), COALESCE(length(i.payload_mac), 0),
			       CASE WHEN i.codec_key IS NOT NULL THEN 0
			            ELSE EXISTS(SELECT 1 FROM rm_episodic_index other WHERE other.doc_id = d.doc_id AND other.ref_pointer = d.raw_text)
			       END
			FROM rm_units d
			LEFT JOIN rm_episodic_index i ON i.doc_id = d.doc_id AND i.codec_key = ?
			WHERE d.layer = 'episodic' AND substr(trim(d.raw_text), 1, 5) = '@ref:'`, key)
		if lErr == nil {
			scanOK := true
			for lightRows.Next() {
				var docID, refPointer string
				var storedRef sql.NullString
				var hasStored, hasOther bool
				var digestLen, macLen int
				if err := lightRows.Scan(&docID, &refPointer, &storedRef, &hasStored, &digestLen, &macLen, &hasOther); err != nil {
					scanOK = false
					break
				}
				if _, ok := snapValidIDs[docID]; ok {
					report.Compatible++
					continue
				}
				if !hasStored {
					if hasOther {
						report.Incompatible++
					} else {
						report.Missing++
					}
				} else if !storedRef.Valid || storedRef.String != refPointer {
					report.Corrupt++
				} else if macLen == 0 || digestLen == 0 {
					report.NeedsRebuild++
				} else {
					// Not in snapValidIDs despite having stored key, matching ref_pointer and non-empty MAC/digest:
					// indexedEpisodicDocsContext rejected it due to MAC mismatch or wire decode error.
					report.Corrupt++
				}
			}
			if lightRows.Err() != nil {
				scanOK = false
			}
			lightRows.Close()
			if scanOK {
				err := v.db.QueryRow(`SELECT count(*) FROM rm_episodic_index i LEFT JOIN rm_docs d ON d.unit_id = i.doc_id WHERE d.unit_id IS NULL`).Scan(&report.Orphaned)
				if err == nil && hasFingerprint && v.slotCacheMu != nil && v.snapRef != nil {
					v.slotCacheMu.Lock()
					v.snapRef.faultReady = true
					v.snapRef.lastFaultTime = time.Now()
					v.snapRef.faultDocCnt = docCnt
					v.snapRef.faultDocRowSum = docRowSum
					v.snapRef.faultMaxRowID = docMaxRowID
					v.snapRef.faultDocTextLen = docTextLen
					v.snapRef.faultEpisodicRev = episodicRevision
					v.snapRef.faultIdxCnt = idxCnt
					v.snapRef.faultCodecKey = string(key)
					v.snapRef.faultReport = report
					v.slotCacheMu.Unlock()
					if idxCnt >= 1024 {
						if encoded, mErr := json.Marshal(persistedEpisodicFaultCache{
							DocCnt:      docCnt,
							DocRowSum:   docRowSum,
							DocMaxRowID: docMaxRowID,
							DocTextLen:  docTextLen,
							EpisodicRev: episodicRevision,
							IdxCnt:      idxCnt,
							CodecKeyHex: hex.EncodeToString(key),
							Report:      report,
						}); mErr == nil {
							_, _ = v.db.Exec(`INSERT OR REPLACE INTO rm_meta(k, v) VALUES (?, ?)`, episodicFaultCacheMetaKey, encoded)
						}
					}
				}
				return report, err
			}
			report = EpisodicIndexFaultReport{}
		}
	}

	rows, err := v.db.Query(`SELECT d.doc_id, d.raw_text, i.ref_pointer, i.codec_key, i.payload, i.content_digest, i.payload_mac, EXISTS(SELECT 1 FROM rm_episodic_index other WHERE other.doc_id = d.doc_id AND other.ref_pointer = d.raw_text) FROM rm_units d LEFT JOIN rm_episodic_index i ON i.doc_id = d.doc_id AND i.codec_key = ? WHERE d.layer = 'episodic' AND substr(trim(d.raw_text), 1, 5) = '@ref:'`, key)
	if err != nil {
		return report, err
	}
	type toVerifyRow struct {
		docID      string
		refPointer string
		stored     []byte
		payload    []byte
		digest     []byte
		mac        []byte
		valid      bool
	}
	var pending []toVerifyRow
	for rows.Next() {
		var docID, refPointer string
		var storedRef sql.NullString
		var stored, payload, digest, mac []byte
		var hasOther bool
		if err := rows.Scan(&docID, &refPointer, &storedRef, &stored, &payload, &digest, &mac, &hasOther); err != nil {
			rows.Close()
			return report, err
		}
		if stored == nil {
			if hasOther {
				report.Incompatible++
			} else {
				report.Missing++
			}
		} else if !storedRef.Valid || storedRef.String != refPointer {
			report.Corrupt++
		} else if len(mac) == 0 || len(digest) == 0 {
			report.NeedsRebuild++
		} else if len(digest) != sha256.Size {
			report.Corrupt++
		} else {
			pending = append(pending, toVerifyRow{
				docID:      docID,
				refPointer: refPointer,
				stored:     stored,
				payload:    payload,
				digest:     digest,
				mac:        mac,
			})
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return report, err
	}
	if err := rows.Close(); err != nil {
		return report, err
	}
	if len(pending) >= 64 {
		workers := runtime.NumCPU()
		if workers < 2 {
			workers = 2
		}
		if workers > 12 {
			workers = 12
		}
		var wg sync.WaitGroup
		step := (len(pending) + workers - 1) / workers
		for w := 0; w < workers; w++ {
			start := w * step
			if start >= len(pending) {
				break
			}
			end := start + step
			if end > len(pending) {
				end = len(pending)
			}
			wg.Add(1)
			go func(s, e int) {
				defer wg.Done()
				for idx := s; idx < e; idx++ {
					p := &pending[idx]
					if v.validEpisodicPayloadMAC(p.docID, p.refPointer, p.stored, p.payload, p.digest, p.mac) {
						if _, _, dErr := plain.DecodeWireBytes(p.payload); dErr == nil {
							p.valid = true
						}
					}
				}
			}(start, end)
		}
		wg.Wait()
	} else {
		for idx := range pending {
			p := &pending[idx]
			if v.validEpisodicPayloadMAC(p.docID, p.refPointer, p.stored, p.payload, p.digest, p.mac) {
				if _, _, dErr := plain.DecodeWireBytes(p.payload); dErr == nil {
					p.valid = true
				}
			}
		}
	}
	for idx := range pending {
		if pending[idx].valid {
			report.Compatible++
		} else {
			report.Corrupt++
		}
	}
	err = v.db.QueryRow(`SELECT count(*) FROM rm_episodic_index i LEFT JOIN rm_docs d ON d.unit_id = i.doc_id WHERE d.unit_id IS NULL`).Scan(&report.Orphaned)
	if err == nil && hasFingerprint && v.slotCacheMu != nil && v.snapRef != nil {
		v.slotCacheMu.Lock()
		v.snapRef.faultReady = true
		v.snapRef.lastFaultTime = time.Now()
		v.snapRef.faultDocCnt = docCnt
		v.snapRef.faultDocRowSum = docRowSum
		v.snapRef.faultMaxRowID = docMaxRowID
		v.snapRef.faultDocTextLen = docTextLen
		v.snapRef.faultEpisodicRev = episodicRevision
		v.snapRef.faultIdxCnt = idxCnt
		v.snapRef.faultCodecKey = string(key)
		v.snapRef.faultReport = report
		v.slotCacheMu.Unlock()
		if idxCnt >= 1024 {
			if encoded, mErr := json.Marshal(persistedEpisodicFaultCache{
				DocCnt:      docCnt,
				DocRowSum:   docRowSum,
				DocMaxRowID: docMaxRowID,
				DocTextLen:  docTextLen,
				EpisodicRev: episodicRevision,
				IdxCnt:      idxCnt,
				CodecKeyHex: hex.EncodeToString(key),
				Report:      report,
			}); mErr == nil {
				_, _ = v.db.Exec(`INSERT OR REPLACE INTO rm_meta(k, v) VALUES (?, ?)`, episodicFaultCacheMetaKey, encoded)
			}
		}
	}
	return report, err
}
