// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package thinclient

import (
	"strings"

	ramesdk "github.com/rocareer/rolling-memory/sdk/go"
)

// PulseSignalWarningCode constants and types are canonical aliases to `sdk/go` (`ramesdk`).
const (
	WarnCoarseUnsegmentedCarrier = ramesdk.WarnCoarseUnsegmentedCarrier
	WarnOverFragmentedDust       = ramesdk.WarnOverFragmentedDust
	WarnFlatCarrierModulation    = ramesdk.WarnFlatCarrierModulation
	WarnDegenerateSentenceFrames = ramesdk.WarnDegenerateSentenceFrames
	WarnHighTandemRedundancy     = ramesdk.WarnHighTandemRedundancy
	WarnCooccurHorizonTruncated  = ramesdk.WarnCooccurHorizonTruncated
)

type WordSegmenter = ramesdk.WordSegmenter
type SegmenterPackOptions = ramesdk.SegmenterPackOptions
type PulseSignalWarning = ramesdk.PulseSignalWarning
type PulseSignalReport = ramesdk.PulseSignalReport

// SetWordSegmenter configures a custom WordSegmenter on the Vault with endogenous adaptive harmonic
// emission enabled by default.
func (v *Vault) SetWordSegmenter(seg WordSegmenter) {
	if v == nil {
		return
	}
	v.segOpts = SegmenterPackOptions{
		Segmenter:       seg,
		HarmonicOverlay: true,
	}
	v.clearSlotCache()
}

// SetSegmenterOptions configures custom segmentation and harmonic options on the Vault.
func (v *Vault) SetSegmenterOptions(opts SegmenterPackOptions) {
	if v == nil {
		return
	}
	v.segOpts = opts
	v.clearSlotCache()
}

// SetCanonicalHarmonics configures opt-in structured date beacons and canonical harmonic expansion on the Vault.
func (v *Vault) SetCanonicalHarmonics(opts ramesdk.CanonicalHarmonicOptions) {
	if v == nil {
		return
	}
	v.harmOpts = opts
	v.clearSlotCache()
}

// HasFTS5BM25 reports if the Vault's FTS5 engine is ready for BM25 ranking.
func (v *Vault) HasFTS5BM25() bool {
	return v != nil && v.hasFTS5
}

// fts5BM25MatchExpr 把查询明文转成 FTS5 trigram MATCH 表达式（每个词项一个引号短语，
// 词项间 OR 联合）。载体宽度不足 60（≈3 个拉丁字母）的词项被丢弃，与 trigram 索引口径一致。
func fts5BM25MatchExpr(queryText string) string {
	var ftsTerms []string
	seenTerm := make(map[string]bool)
	for _, w := range SplitWords(queryText) {
		low := strings.ToLower(strings.TrimSpace(w))
		var cw uint32
		for _, r := range low {
			cw += uint32(mechanicalCarrierUnits(r))
		}
		if cw >= 60 && !seenTerm[low] {
			seenTerm[low] = true
			escaped := strings.ReplaceAll(low, `"`, `""`)
			ftsTerms = append(ftsTerms, `"`+escaped+`"`)
		}
	}
	if len(ftsTerms) == 0 {
		return ""
	}
	return strings.Join(ftsTerms, " OR ")
}

// QueryFTS5BM25 executes a relevance-ordered BM25 scan over the local SQLite FTS5 trigram table
// and returns a mapping of doc_id to positive integer BM25 milli-scores.
func (v *Vault) QueryFTS5BM25(queryText string, limit int) (map[string]int64, error) {
	if !v.HasFTS5BM25() {
		return nil, nil
	}
	if limit <= 0 {
		limit = 64
	}
	matchExpr := fts5BM25MatchExpr(queryText)
	if matchExpr == "" {
		return nil, nil
	}
	rows, err := v.db.Query(`
		SELECT thin_vault_fts_map.doc_id, CAST((-bm25(thin_vault_docs_fts) * 1000.0) AS INTEGER) AS bm25_milli
		FROM thin_vault_docs_fts
		JOIN thin_vault_fts_map ON thin_vault_fts_map.rowid = thin_vault_docs_fts.rowid
		WHERE thin_vault_docs_fts MATCH ?
		ORDER BY bm25(thin_vault_docs_fts) ASC
		LIMIT ?`, matchExpr, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	res := make(map[string]int64)
	for rows.Next() {
		var did string
		var bm25Milli int64
		if err := rows.Scan(&did, &bm25Milli); err == nil && did != "" {
			if bm25Milli < 1 {
				bm25Milli = 1
			}
			if bm25Milli > 400 {
				bm25Milli = 400
			}
			res[did] = bm25Milli
		}
	}
	return res, nil
}

// fts5BM25MatchExprWithDF builds an FTS5 trigram MATCH expression, prioritizing corpus-salient
// terms (df <= totalDocs/4) when totalDocs >= 16 so common 3-letter substrings do not trigger
// full-table OR-scan floods in SQLite FTS5. Falls back to all cw >= 60 terms when no salient term exists.
func (v *Vault) fts5BM25MatchExprWithDF(queryText string, totalDocs int, globalDF map[uint64]int) string {
	if totalDocs < 16 || len(globalDF) == 0 {
		return fts5BM25MatchExpr(queryText)
	}
	type ftsTermCand struct {
		escaped string
		df      int
	}
	var allTerms []ftsTermCand
	var salientTerms []ftsTermCand
	seenTerm := make(map[string]bool, 16)
	for _, w := range SplitWords(queryText) {
		low := strings.ToLower(strings.TrimSpace(w))
		var cw uint32
		for _, r := range low {
			cw += uint32(mechanicalCarrierUnits(r))
		}
		if cw >= 60 && !seenTerm[low] {
			seenTerm[low] = true
			escaped := `"` + strings.ReplaceAll(low, `"`, `""`) + `"`
			docFreq := globalDF[v.KeyedHash64(low)]
			cand := ftsTermCand{escaped: escaped, df: docFreq}
			allTerms = append(allTerms, cand)
			if docFreq <= totalDocs/4 {
				salientTerms = append(salientTerms, cand)
			}
		}
	}
	chosen := salientTerms
	if len(chosen) == 0 {
		chosen = allTerms
	}
	if len(chosen) == 0 {
		return ""
	}
	// Keep the complete term set for the FTS5 leg. This ranker is fused with the
	// blind-slot ranking by continuous BM25 energy-spectral fusion, so narrowing the query
	// alters the FTS energy distribution across the fused head. Measured on the
	// bundled ATM-Bench: keeping only the four most selective terms lowers Hard-31
	// Recall@10 from 46.12% to 45.23% and Main-1013 from 77.17% to 76.51%. The result
	// limit already bounds the scan; never truncate the query terms.
	out := make([]string, len(chosen))
	for i, c := range chosen {
		out[i] = c.escaped
	}
	return strings.Join(out, " OR ")
}

// QueryFTS5BM25Ranked executes the same FTS5 trigram BM25 scan as QueryFTS5BM25 and returns
// matched document IDs ordered by continuous BM25 energy (best first).
func (v *Vault) QueryFTS5BM25Ranked(queryText string, limit int) ([]string, error) {
	return v.queryFTS5BM25RankedWithDF(queryText, limit, 0, nil, nil)
}

func (v *Vault) queryFTS5BM25RankedWithDF(queryText string, limit int, totalDocs int, globalDF map[uint64]int, ftsRowIDToDocID map[int64]string) ([]string, error) {
	if !v.HasFTS5BM25() {
		return nil, nil
	}
	if limit <= 0 {
		limit = 64
	}
	matchExpr := v.fts5BM25MatchExprWithDF(queryText, totalDocs, globalDF)
	if matchExpr == "" {
		return nil, nil
	}
	if len(ftsRowIDToDocID) > 0 {
		rows, err := v.db.Query(`
			SELECT rowid
			FROM thin_vault_docs_fts
			WHERE thin_vault_docs_fts MATCH ?
			ORDER BY rank
			LIMIT ?`, matchExpr, limit)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := make([]string, 0, limit)
		for rows.Next() {
			var rid int64
			if err := rows.Scan(&rid); err == nil {
				if did := ftsRowIDToDocID[rid]; did != "" {
					out = append(out, did)
				}
			}
		}
		return out, rows.Err()
	}
	rows, err := v.db.Query(`
		SELECT m.doc_id
		FROM (
			SELECT rowid, bm25(thin_vault_docs_fts) AS b
			FROM thin_vault_docs_fts
			WHERE thin_vault_docs_fts MATCH ?
			ORDER BY b ASC, rowid ASC
			LIMIT ?
		) f
		JOIN thin_vault_fts_map m ON m.rowid = f.rowid
		ORDER BY f.b ASC, m.doc_id ASC`, matchExpr, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]string, 0, limit)
	for rows.Next() {
		var did string
		if err := rows.Scan(&did); err == nil && did != "" {
			out = append(out, did)
		}
	}
	return out, rows.Err()
}

// queryFTS5BM25ScoresWithDF executes an FTS5 trigram BM25 scan and returns each matched
// document's continuous positive BM25 energy (-bm25(thin_vault_docs_fts)) together with the
// maximum energy across the returned set. Preserving continuous BM25 energy eliminates
// discrete rank-index RRF (1/(k+rank)).
func (v *Vault) queryFTS5BM25ScoresWithDF(queryText string, limit int, totalDocs int, globalDF map[uint64]int, ftsRowIDToDocID map[int64]string) (map[string]float64, float64, error) {
	if !v.HasFTS5BM25() {
		return nil, 0, nil
	}
	if limit <= 0 {
		limit = 64
	}
	matchExpr := v.fts5BM25MatchExprWithDF(queryText, totalDocs, globalDF)
	if matchExpr == "" {
		return nil, 0, nil
	}
	scores := make(map[string]float64, limit)
	var maxScore float64
	if len(ftsRowIDToDocID) > 0 {
		rows, err := v.db.Query(`
			SELECT rowid, -bm25(thin_vault_docs_fts) AS s
			FROM thin_vault_docs_fts
			WHERE thin_vault_docs_fts MATCH ?
			ORDER BY rank
			LIMIT ?`, matchExpr, limit)
		if err != nil {
			return nil, 0, err
		}
		defer rows.Close()
		for rows.Next() {
			var rid int64
			var s float64
			if err := rows.Scan(&rid, &s); err == nil {
				if did := ftsRowIDToDocID[rid]; did != "" {
					if s <= 0 {
						s = 1e-6
					}
					if s > scores[did] {
						scores[did] = s
					}
					if s > maxScore {
						maxScore = s
					}
				}
			}
		}
		return scores, maxScore, rows.Err()
	}
	rows, err := v.db.Query(`
		SELECT m.doc_id, -f.b AS s
		FROM (
			SELECT rowid, bm25(thin_vault_docs_fts) AS b
			FROM thin_vault_docs_fts
			WHERE thin_vault_docs_fts MATCH ?
			ORDER BY b ASC, rowid ASC
			LIMIT ?
		) f
		JOIN thin_vault_fts_map m ON m.rowid = f.rowid
		ORDER BY f.b ASC, m.doc_id ASC`, matchExpr, limit)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var did string
		var s float64
		if err := rows.Scan(&did, &s); err == nil && did != "" {
			if s <= 0 {
				s = 1e-6
			}
			if s > scores[did] {
				scores[did] = s
			}
			if s > maxScore {
				maxScore = s
			}
		}
	}
	return scores, maxScore, rows.Err()
}

// PackBlindPacketWithSegmenter packs plaintext into a BlindPulsePacket using pluggable SegmenterPackOptions via ramesdk.
func (v *Vault) PackBlindPacketWithSegmenter(docID string, text string, occurredAt int64, opts SegmenterPackOptions) (BlindPulsePacket, []string) {
	var key [32]byte
	if v != nil {
		key = v.secretKey
	}
	enc := ramesdk.NewBlindEncoderFromRawKey(key)
	if v != nil {
		enc.SetCanonicalHarmonics(v.harmOpts)
	}
	var sdkPkt ramesdk.BlindPulsePacket
	var sents []string
	if opts.Segmenter == nil && opts.SentenceAtomPacker == nil {
		sdkPkt, sents = enc.PackBlindPacket(docID, text, occurredAt)
	} else {
		sdkPkt, sents = enc.PackBlindPacketWithSegmenter(docID, text, occurredAt, opts)
	}
	return BlindPulsePacket{
		UnitID:         sdkPkt.UnitID,
		OccurredAt:     sdkPkt.OccurredAt,
		Slots:          sdkPkt.Slots,
		SentenceFrames: sdkPkt.SentenceFrames,
	}, sents
}

// EvaluatePulseSignal computes a 100% plaintext-free segmentation quality report from a BlindPulsePacket via ramesdk.
func EvaluatePulseSignal(pkt BlindPulsePacket) PulseSignalReport {
	return ramesdk.EvaluatePulseSignal(ramesdk.BlindPulsePacket{
		UnitID:         pkt.UnitID,
		OccurredAt:     pkt.OccurredAt,
		Slots:          pkt.Slots,
		SentenceFrames: pkt.SentenceFrames,
	})
}

// InspectVaultPulseSignal samples up to sampleLimit documents from the local SQLite Vault,
// packs each with the provided SegmenterPackOptions, and returns the aggregate PulseSignalReport.
func (v *Vault) InspectVaultPulseSignal(sampleLimit int, opts SegmenterPackOptions) (PulseSignalReport, error) {
	if v == nil || v.db == nil {
		return PulseSignalReport{Score: 100, Grade: "EMPTY"}, nil
	}
	if sampleLimit <= 0 {
		sampleLimit = 64
	}
	rows, err := v.db.Query(`SELECT doc_id, occurred_at, raw_text FROM thin_vault_docs ORDER BY occurred_at DESC LIMIT ?`, sampleLimit)
	if err != nil {
		return PulseSignalReport{}, err
	}
	defer rows.Close()

	var merged BlindPulsePacket
	for rows.Next() {
		var docID, rawText string
		var occ int64
		if err := rows.Scan(&docID, &occ, &rawText); err != nil {
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(rawText), "@ref:") {
			if live, rErr := ResolveEpisodicPointerLive(strings.TrimSpace(rawText)); rErr == nil && strings.TrimSpace(live) != "" {
				rawText = live
			}
		}
		pkt, _ := v.PackBlindPacketWithSegmenter(docID, rawText, occ, opts)
		if len(pkt.Slots) == 0 {
			continue
		}
		merged.Slots = append(merged.Slots, pkt.Slots...)
		merged.SentenceFrames = append(merged.SentenceFrames, pkt.SentenceFrames...)
	}
	rep := EvaluatePulseSignal(merged)
	rep.FTS5BM25Enabled = v.HasFTS5BM25()
	return rep, nil
}
