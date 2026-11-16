// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

// Package ramesdk implements the official Go Client SDK and RAME-BlindPulse-v1
// deterministic blind integer pulse encoder for the Rolling Autonoetic Memory Engine (`/open/memory/v1/*`).
package ramesdk

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/bits"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	SpecVersion           = "RAME-BlindPulse-v1"
	FlagHasUpper    uint8 = 1 << 0
	FlagHasCamel    uint8 = 1 << 1
	FlagHasDigit    uint8 = 1 << 2
	FlagHasConn     uint8 = 1 << 3
	LayerBackground       = "background"
	LayerEpisodic         = "episodic"
	LayerNoetic           = "noetic"

	// Canary* constants define the built-in self-test vector shared by all 4 SDKs and `/open/memory/v1/health`.
	CanarySecret    = "rame-codec-canary-key-v1"
	CanaryDocID     = "CANARY_V1"
	CanaryText      = "RollingMemory v1.0.1 WaveEngine_256 洛灵记忆. Zero plaintext!"
	CanaryTS        = int64(1700000000)
	CanarySlotCount = 21
	CanarySentCount = 2
	CanaryAtomsB64  = "Uk1CUAEAPA0AFAAAABUAAhQhUFBjZHiLAYwBtAHIAYQCtQJAEATBATAPB0jHISlR96QHZAI40Tt3T0fPAxxcTN+orGsN87QYgKG+M3wAaBUxyxkJx8f8zRbMt024NoH4VZEtimANzWUASZ6GWiX3Z7rb64Eq7inVwwMYS98I2I7VbHWwUFW4WszbtBQnTCgoPkEZy9pjGebXDmmqB9vwbo0DeZvaEa9TH6Ti6fHUlg3MbBKkCYTXBbqHJcBSS+ORCU+K9G0qrLWQNLboQC4oYxzk6gIUARCElBIB"
)

// SDKVersion stays on its own line: the cross-language version gate extracts the Go
// side with a single-space assignment pattern, so a gofmt-aligned const block would
// silently blank the gate.
const SDKVersion = "1.11.3"

// DefaultAutoTopK (0) selects RB's endogenous adaptive delivery budget (0..96 hits).
// Pass topK > 0 (e.g. 1..96) to override with an explicit cap.
const (
	DefaultAutoTopK     = 0
	MaxAutoDeliveryTopK = 96
)

// RAME Client Operating Modes (100% Symmetric [local | pulse] × [plain | cipher]):
//   - ModeLocalPlain  ("local_plain"):  100% local offline plaintext vault, zero cloud traffic;
//   - ModePulsePlain  ("pulse_plain"):  cloud pulse + plaintext mode (RB directly receives/returns plaintext);
//   - ModePulseCipher ("pulse_cipher"): cloud irreversible cipher pulse mode (0 plaintext leaves client).
const (
	ModeLocalPlain  = "local_plain"
	ModePulsePlain  = "pulse_plain"
	ModePulseCipher = "pulse_cipher"
)

// UnitMeta carries optional speaker, tri-temporal metadata (narrated_at, occurred_at, ingested_at),
// and 4D spatial coordinates (file_url/doc_h, block_id/block_h, line_start, line_end, file_parse).
// All fields are strictly opt-in: unset fields stay empty/zero and produce zero prefix or wire overhead.
type UnitMeta struct {
	Author     string `json:"author,omitempty"`
	NarratedAt int64  `json:"narrated_at,omitempty"`
	OccurredAt int64  `json:"occurred_at,omitempty"`
	IngestedAt int64  `json:"ingested_at,omitempty"`
	FileURL    string `json:"file_url,omitempty"`
	FileParse  string `json:"file_parse,omitempty"`
	BlockID    string `json:"block_id,omitempty"`
	DocHash    uint64 `json:"doc_h,omitempty"`
	BlockHash  uint64 `json:"block_h,omitempty"`
	LineStart  int    `json:"line_start,omitempty"`
	LineEnd    int    `json:"line_end,omitempty"`
}

// FormatPlaintextWithMeta symmetrically formats rawText with any user-supplied speaker,
// tri-temporal metadata, and optional parsed file/attachment text. When meta has no fields set, rawText is returned unchanged.
func FormatPlaintextWithMeta(rawText string, meta UnitMeta) string {
	text := strings.TrimSpace(rawText)
	fileParse := strings.TrimSpace(meta.FileParse)
	if text == "" && fileParse != "" {
		text = fileParse
		rawText = fileParse
	} else if text == "" {
		return rawText
	} else if fileParse != "" && !strings.Contains(text, fileParse) {
		text = text + "\n" + fileParse
		rawText = text
	}
	author := strings.TrimSpace(meta.Author)
	if author == "" && meta.NarratedAt <= 0 && meta.OccurredAt <= 0 && meta.IngestedAt <= 0 {
		return rawText
	}
	fmtDay := func(ts int64) string {
		if ts <= 0 {
			return ""
		}
		return time.Unix(ts, 0).UTC().Format("2006-01-02")
	}
	occDay := fmtDay(meta.OccurredAt)
	narDay := fmtDay(meta.NarratedAt)
	ingDay := fmtDay(meta.IngestedAt)
	var timeParts []string
	primaryDay := occDay
	if primaryDay == "" {
		primaryDay = narDay
	}
	if primaryDay != "" {
		timeParts = append(timeParts, primaryDay)
	}
	if narDay != "" && narDay != primaryDay {
		timeParts = append(timeParts, "narrated:"+narDay)
	}
	if ingDay != "" && ingDay != primaryDay && ingDay != narDay {
		timeParts = append(timeParts, "ingested:"+ingDay)
	}
	timeTag := ""
	if len(timeParts) > 0 {
		timeTag = "[" + strings.Join(timeParts, "|") + "]"
	}
	prefix := ""
	if timeTag != "" && author != "" {
		prefix = timeTag + " " + author + ": "
	} else if timeTag != "" {
		prefix = timeTag + " "
	} else if author != "" {
		prefix = author + ": "
	}
	if prefix == "" || strings.HasPrefix(text, prefix) {
		return text
	}
	return prefix + text
}

type BlindTokenAtom struct {
	H          uint64 `json:"h"`
	RawBitsX10 uint16 `json:"b"`
	ClassFlags uint8  `json:"f"`
}

type BlindPulsePacket struct {
	TenantID         string             `json:"tenant_id,omitempty"`
	Scope            string             `json:"scope,omitempty"`
	UserID           string             `json:"user_id,omitempty"`
	AssistantID      string             `json:"assistant_id,omitempty"`
	IncarnationID    string             `json:"incarnation_id,omitempty"`
	UnitID           string             `json:"id"`
	Layer            string             `json:"layer,omitempty"`
	CitedUnitIDs     []string           `json:"cited_unit_ids,omitempty"`
	OccurredAt       int64              `json:"ts,omitempty"`
	NarratedAt       int64              `json:"narrated_at,omitempty"`
	IngestedAt       int64              `json:"ingested_at,omitempty"`
	AuthorHash       uint64             `json:"author_h,omitempty"`
	DocHash          uint64             `json:"doc_h,omitempty"`
	BlockHash        uint64             `json:"block_h,omitempty"`
	LineStart        int                `json:"line_start,omitempty"`
	LineEnd          int                `json:"line_end,omitempty"`
	Slots            []BlindTokenAtom   `json:"slots,omitempty"`
	SentenceFrames   [][]BlindTokenAtom `json:"sents,omitempty"`
	DenoiseRatio     *float64           `json:"denoise_ratio,omitempty"`
	MinKeepSentences int                `json:"min_keep_sentences,omitempty"`
	WaveRadix        int                `json:"wave_radix,omitempty"`
	AtomsB64         string             `json:"atoms_b64,omitempty"`
	RawText          string             `json:"raw_text,omitempty"`
	PulseMode        string             `json:"pulse_mode,omitempty"`
}

type CloudIngestResponse struct {
	TenantID            string               `json:"tenant_id,omitempty"`
	Scope               string               `json:"scope,omitempty"`
	PulseMode           string               `json:"pulse_mode,omitempty"`
	UnitID              string               `json:"id"`
	Layer               string               `json:"layer,omitempty"`
	CitedUnitIDs        []string             `json:"cited_unit_ids,omitempty"`
	Epoch               int                  `json:"epoch"`
	ConservedMass       float64              `json:"conserved_mass"`
	QualityMult         float64              `json:"quality_mult"`
	AddressURI          string               `json:"address_uri"`
	AddressCoord64      uint64               `json:"address_coord64"`
	ContainerID         uint64               `json:"container_id"`
	ParentID            uint64               `json:"parent_id"`
	ShiftScore          float64              `json:"shift_score"`
	KeptSentenceIndices []int                `json:"kept_sentence_indices,omitempty"`
	CompressionRatio    float64              `json:"compression_ratio,omitempty"`
	PulseSignalScore    int                  `json:"pulse_signal_score,omitempty"`
	PulseSignalGrade    string               `json:"pulse_signal_grade,omitempty"`
	PulseSignalWarnings []PulseSignalWarning `json:"pulse_signal_warnings,omitempty"`
}

const (
	WarnCoarseUnsegmentedCarrier = "COARSE_UNSEGMENTED_CARRIER"
	WarnOverFragmentedDust       = "OVER_FRAGMENTED_DUST"
	WarnFlatCarrierModulation    = "FLAT_CARRIER_MODULATION"
	WarnDegenerateSentenceFrames = "DEGENERATE_SENTENCE_FRAMES"
	WarnHighTandemRedundancy     = "HIGH_TANDEM_REDUNDANCY"
	WarnCooccurHorizonTruncated  = "COOCCUR_HORIZON_TRUNCATED"

	blindCooccurHorizonCap = 64
)

type WordSegmenter func(sentence string) []string

type SentenceAtomPacker func(enc *BlindEncoder, sentence string, sentIdx int) []BlindTokenAtom

// HarmonicExpander maps a surface word token to up to 2 canonical equivalence tags
// (e.g., "@unit:mass:kg", "@geo:Q60") that are injected as non-destructive companion
// harmonic slots (`b = B_even - 1` / `B_even - 3`, `f = FlagHasConn | FlagIsHarmonicCompanion`)
// right after the primary atom without altering native segmentation slots. The explicit
// companion bit mirrors the implicit odd-parity carrier channel consumed server-side.
type HarmonicExpander func(token string) []string

// CanonicalHarmonicOptions configures opt-in client-side canonical harmonic overlay
// (structured ISO date canonicalization + optional domain/ontology harmonic expander).
type CanonicalHarmonicOptions struct {
	EnableStructuredDates bool
	Expander              HarmonicExpander
}

type SegmenterPackOptions struct {
	Segmenter           WordSegmenter
	SentenceAtomPacker  SentenceAtomPacker
	HarmonicOverlay     bool
	DisableAutoHarmonic bool
}

type PulseSignalWarning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type PulseSignalReport struct {
	Score                    int                  `json:"score"`
	Grade                    string               `json:"grade"`
	NeedsAttention           bool                 `json:"needs_attention"`
	TotalSlots               int                  `json:"total_slots"`
	UniqueSlotCount          int                  `json:"unique_slot_count"`
	SentenceFrameCount       int                  `json:"sentence_frame_count"`
	MeanCarrierBitsX10       int                  `json:"mean_carrier_bits_x10"`
	GluedSlotCount           int                  `json:"glued_slot_count"`
	GluedSlotPermille        int                  `json:"glued_slot_permille"`
	DustSlotCount            int                  `json:"dust_slot_count"`
	DustSlotPermille         int                  `json:"dust_slot_permille"`
	TandemRepeatCount        int                  `json:"tandem_repeat_count"`
	TandemRepeatPermille     int                  `json:"tandem_repeat_permille"`
	SingleSlotFrameCount     int                  `json:"single_slot_frame_count"`
	SingleSlotFramePermille  int                  `json:"single_slot_frame_permille"`
	WidthSpreadPermille      int                  `json:"width_spread_permille"`
	CooccurCoveredUnique     int                  `json:"cooccur_covered_unique"`
	CooccurTruncatedUnique   int                  `json:"cooccur_truncated_unique"`
	CooccurCoveragePermille  int                  `json:"cooccur_coverage_permille"`
	FoldedBiUniSpans         int                  `json:"folded_bi_uni_spans"`
	CompoundHarmonicGroups   int                  `json:"compound_harmonic_groups"`
	CarrierHealthSubscore    int                  `json:"carrier_health_subscore"`
	ModulationHealthSubscore int                  `json:"modulation_health_subscore"`
	FramingHealthSubscore    int                  `json:"framing_health_subscore"`
	DiversityHealthSubscore  int                  `json:"diversity_health_subscore"`
	CooccurHealthSubscore    int                  `json:"cooccur_health_subscore"`
	FTS5BM25Enabled          bool                 `json:"fts5_bm25_enabled"`
	Warnings                 []PulseSignalWarning `json:"warnings,omitempty"`
}

type CloudQueryHit struct {
	UnitID              string   `json:"id"`
	Layer               string   `json:"layer,omitempty"`
	CitedUnitIDs        []string `json:"cited_unit_ids,omitempty"`
	OccurredAt          int64    `json:"occurred_at,omitempty"`
	NarratedAt          int64    `json:"narrated_at,omitempty"`
	IngestedAt          int64    `json:"ingested_at,omitempty"`
	AuthorHash          uint64   `json:"author_h,omitempty"`
	DocHash             uint64   `json:"doc_h,omitempty"`
	BlockHash           uint64   `json:"block_h,omitempty"`
	LineStart           int      `json:"line_start,omitempty"`
	LineEnd             int      `json:"line_end,omitempty"`
	AddressURI          string   `json:"address_uri"`
	AddressCoord64      uint64   `json:"address_coord64"`
	WaveScore           float64  `json:"wave_score"`
	CellWormhole        float64  `json:"cell_wormhole"`
	ManifoldGain        float64  `json:"manifold_gain"`
	FinalWeight         float64  `json:"final_weight"`
	KeptSentenceIndices []int    `json:"kept_sentence_indices,omitempty"`
	RawText             string   `json:"raw_text,omitempty"`
	DistilledText       string   `json:"distilled_text,omitempty"`
}

type LocalNoeticRecord struct {
	NoeticID     string   `json:"noetic_id"`
	OccurredAt   int64    `json:"occurred_at"`
	QueryText    string   `json:"query_text"`
	AnswerText   string   `json:"answer_text"`
	CitedUnitIDs []string `json:"cited_unit_ids"`
	// AssistantID / IncarnationID pin the crystallizing persona and its concrete
	// lifetime instance, so parallel incarnations never overwrite each other's answer.
	AssistantID   string `json:"assistant_id,omitempty"`
	IncarnationID string `json:"incarnation_id,omitempty"`
}

// DeriveBlindPepper extracts a rotation-invariant tenant blind pepper from a short-lived
// `mem2.<payload_b64>.<sig>` data-plane signed key (`rame-tenant-vault:<tid>:<uid>`),
// or returns secret unchanged for static keys/phrases.
func DeriveBlindPepper(secret []byte) []byte {
	trimmed := strings.TrimSpace(string(secret))
	if strings.HasPrefix(trimmed, "mem2.") {
		if parts := strings.Split(trimmed, "."); len(parts) == 3 {
			if raw, err := base64.RawURLEncoding.DecodeString(parts[1]); err == nil {
				var claims struct {
					Tid string `json:"tid"`
					UID string `json:"uid"`
				}
				if json.Unmarshal(raw, &claims) == nil && strings.TrimSpace(claims.Tid) != "" {
					return []byte("rame-tenant-vault:" + strings.TrimSpace(claims.Tid) + ":" + strings.TrimSpace(claims.UID))
				}
			}
		}
	}
	return secret
}

type BlindEncoder struct {
	secretKey [32]byte
	segOpts   SegmenterPackOptions
	harmOpts  CanonicalHarmonicOptions
}

func (e *BlindEncoder) SetWordSegmenter(seg WordSegmenter) {
	if e == nil {
		return
	}
	e.segOpts = SegmenterPackOptions{Segmenter: seg, HarmonicOverlay: true}
}

func (e *BlindEncoder) SetSegmenterOptions(opts SegmenterPackOptions) {
	if e == nil {
		return
	}
	e.segOpts = opts
}

func (e *BlindEncoder) SetCanonicalHarmonics(opts CanonicalHarmonicOptions) {
	if e == nil {
		return
	}
	e.harmOpts = opts
}

func NewBlindEncoder(secret []byte) *BlindEncoder {
	enc := &BlindEncoder{}
	pepper := DeriveBlindPepper(secret)
	if len(pepper) > 0 {
		enc.secretKey = sha256.Sum256(pepper)
	} else {
		_, _ = rand.Read(enc.secretKey[:])
	}
	return enc
}

func NewBlindEncoderFromRawKey(rawKey [32]byte) *BlindEncoder {
	return &BlindEncoder{secretKey: rawKey}
}

func (e *BlindEncoder) KeyedHash64(token string) uint64 {
	mac := hmac.New(sha256.New, e.secretKey[:])
	mac.Write([]byte(token))
	return binary.LittleEndian.Uint64(mac.Sum(nil)[:8])
}

func isHanRune(r rune) bool {
	return (r >= 0x4E00 && r <= 0x9FFF) || (r >= 0x3400 && r <= 0x4DBF) || (r >= 0xF900 && r <= 0xFAFF) || (r >= 0x20000 && r <= 0x2FA1F)
}
func isHangulRune(r rune) bool {
	return (r >= 0xAC00 && r <= 0xD7AF) || (r >= 0x1100 && r <= 0x11FF) || (r >= 0x3130 && r <= 0x318F)
}
func isKanaOrSEAsianRune(r rune) bool {
	return r == 0x30FC || (r >= 0x3040 && r <= 0x30FF) || (r >= 0xFF66 && r <= 0xFF9F) || (r >= 0x0E00 && r <= 0x0EFF) || (r >= 0x1780 && r <= 0x17FF)
}
func isLetterOrNumber(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.IsMark(r)
}
func isUnspacedIdeographic(r rune) bool { return isHanRune(r) || isKanaOrSEAsianRune(r) }
func isWordConn(r rune) bool            { return r == '-' || r == '_' || r == '.' }

func splitCompoundSubwords(tok string) []string {
	var parts []string
	var cur strings.Builder
	var prevR rune
	hasPrev := false
	for idx, r := range tok {
		if isWordConn(r) {
			if cur.Len() > 0 {
				parts = append(parts, cur.String())
				cur.Reset()
			}
			hasPrev = false
			continue
		}
		w := utf8.RuneLen(r)
		nextIsLower := false
		if idx+w < len(tok) {
			if nr, _ := utf8.DecodeRuneInString(tok[idx+w:]); unicode.IsLower(nr) {
				nextIsLower = true
			}
		}
		if hasPrev && cur.Len() > 0 && ((unicode.IsLower(prevR) && unicode.IsUpper(r)) ||
			(unicode.IsUpper(prevR) && unicode.IsUpper(r) && nextIsLower) ||
			(unicode.IsNumber(prevR) != unicode.IsNumber(r))) {
			parts = append(parts, cur.String())
			cur.Reset()
		}
		cur.WriteRune(r)
		prevR, hasPrev = r, true
	}
	if cur.Len() > 0 {
		parts = append(parts, cur.String())
	}
	if len(parts) < 2 {
		return nil
	}
	return parts
}

func CarrierUnits(r rune) uint16 {
	if r < 0x80 {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			return 20
		}
		if r >= '0' && r <= '9' {
			return 33
		}
		return 10
	}
	if isHanRune(r) {
		return 100
	}
	if isHangulRune(r) {
		return 70
	}
	if isKanaOrSEAsianRune(r) {
		return 42
	}
	if unicode.IsDigit(r) {
		return 33
	}
	if unicode.IsLetter(r) {
		return 20
	}
	return 10
}

func clampBitsX10(v uint32) uint16 {
	if v < 10 {
		return 10
	}
	if v > 65535 {
		return 65535
	}
	return uint16(v)
}

func classifyRuneFlag(r rune, upperAllowed, hasLower bool) (flags uint8, nextHasLower bool) {
	nextHasLower = hasLower
	if unicode.IsUpper(r) {
		if upperAllowed {
			flags |= FlagHasUpper
		}
		if hasLower {
			flags |= FlagHasCamel
		}
	} else if unicode.IsLower(r) {
		nextHasLower = true
	} else if unicode.IsNumber(r) {
		flags |= FlagHasDigit
	} else if isWordConn(r) {
		flags |= FlagHasConn
	}
	return flags, nextHasLower
}

func SplitSentences(text string) []string {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	var out []string
	var cur strings.Builder
	for i, n := 0, len(text); i < n; {
		r, w := utf8.DecodeRuneInString(text[i:])
		isBound := r == '\n' || r == '\r' || r == '。' || r == '！' || r == '？' || r == '；' || r == ';' || r == '!' || r == '?'
		if !isBound && r == '.' {
			prevAlnum, nextAlnum := false, false
			if i > 0 {
				pr, _ := utf8.DecodeLastRuneInString(text[:i])
				prevAlnum = isLetterOrNumber(pr)
			}
			if i+w < n {
				nr, _ := utf8.DecodeRuneInString(text[i+w:])
				nextAlnum = isLetterOrNumber(nr)
			}
			isBound = !(prevAlnum && nextAlnum)
		}
		if isBound {
			if s := strings.TrimSpace(cur.String()); s != "" {
				out = append(out, s)
			}
			cur.Reset()
		} else {
			cur.WriteRune(r)
		}
		i += w
	}
	if tail := strings.TrimSpace(cur.String()); tail != "" {
		out = append(out, tail)
	}
	return out
}

func SplitWords(sent string) []string {
	if strings.TrimSpace(sent) == "" {
		return nil
	}
	var units []string
	for i, n := 0, len(sent); i < n; {
		r, w := utf8.DecodeRuneInString(sent[i:])
		if !isLetterOrNumber(r) {
			i += w
			continue
		}
		if isUnspacedIdeographic(r) {
			if isHanRune(r) {
				var hanOffsets []int
				end := i
				for end < n {
					nr, nw := utf8.DecodeRuneInString(sent[end:])
					if !isHanRune(nr) {
						break
					}
					hanOffsets = append(hanOffsets, end)
					end += nw
				}
				hanOffsets = append(hanOffsets, end)
				if runCount := len(hanOffsets) - 1; runCount == 1 {
					units = append(units, sent[hanOffsets[0]:hanOffsets[1]])
				} else {
					for k := 0; k < runCount-1; k++ {
						units = append(units, sent[hanOffsets[k]:hanOffsets[k+2]])
					}
					for k := 0; k < runCount; k++ {
						units = append(units, sent[hanOffsets[k]:hanOffsets[k+1]])
					}
				}
				i = end
				continue
			}
			end := i + w
			for end < n {
				nr, nw := utf8.DecodeRuneInString(sent[end:])
				if !isUnspacedIdeographic(nr) || isHanRune(nr) {
					break
				}
				end += nw
			}
			units = append(units, sent[i:end])
			i = end
			continue
		}
		end := i + w
		for end < n {
			nr, nw := utf8.DecodeRuneInString(sent[end:])
			if isLetterOrNumber(nr) && !isUnspacedIdeographic(nr) {
				end += nw
				continue
			}
			if isWordConn(nr) && end+nw < n {
				if nextR, _ := utf8.DecodeRuneInString(sent[end+nw:]); isLetterOrNumber(nextR) && !isUnspacedIdeographic(nextR) {
					end += nw
					continue
				}
			}
			break
		}
		tok := sent[i:end]
		units = append(units, tok)
		if subs := splitCompoundSubwords(tok); len(subs) >= 2 {
			units = append(units, subs...)
		}
		i = end
	}
	return units
}

// FTS5TrigramSegmenter 复现本地 SQLite FTS5 索引 `tokenize='trigram'` 的令牌流：
// 每个表面词内部按 3 字符滑窗产出重叠三元组（小写）。FTS5 trigram 无法索引/匹配
// 短于 3 字符的串，故整词长度 < 3 时直接丢弃（与索引口径一致）。
//
// 与 FTS5BM25Segmenter 的区别：后者是「词项」流（整词匹配），本函数是索引真实
// 令牌流（子串匹配），对 ATM 这类改写提问能提供形态近似（hotel/hotels 共享 "hot"/"ote"/"tel"）。
func FTS5TrigramSegmenter(sentence string) []string {
	words := SplitWords(sentence)
	if len(words) == 0 {
		return nil
	}
	out := make([]string, 0, len(words)*3)
	for _, w := range words {
		runes := []rune(strings.ToLower(strings.TrimSpace(w)))
		if len(runes) < 3 {
			continue
		}
		for i := 0; i+3 <= len(runes); i++ {
			out = append(out, string(runes[i:i+3]))
		}
	}
	return out
}

// FTS5BM25TermMinCarrierX10 是本地 SQLite FTS5/BM25 词项纳入查询的最小机械载体宽度
// （×10）：60 ≈ 3 个拉丁字母 / 2 个汉字，低于它的词项被本地 FTS/BM25 视为噪声丢弃。
const FTS5BM25TermMinCarrierX10 = 60

// FTS5BM25Segmenter 复现本地 SQLite FTS5/BM25 索引的词项流：对一句话取
// SplitWords（表面词 + 复合子词，与 QueryFTS5BM25 完全同源），只保留机械载体宽度
// ≥ FTS5BM25TermMinCarrierX10 的词项，按首次出现顺序返回且**保留重复**
// （重复进入盲态波形的 tf 统计，与 FTS 倒排的词频同口径）。
//
// 用途：把它挂到 `BlindEncoder.SetSegmenterOptions` 后打包的盲态脉冲，其词项集合与
// 本地 FTS5/BM25 检索词表逐项对齐——端侧只负责切词与盲化，排序仍 100% 由云端
// `the memory engine` 完成，绝不与本地 FTS 检索结果做并集或融合。
func FTS5BM25Segmenter(sentence string) []string {
	words := SplitWords(sentence)
	if len(words) == 0 {
		return nil
	}
	out := make([]string, 0, len(words))
	for _, w := range words {
		low := strings.ToLower(strings.TrimSpace(w))
		if low == "" {
			continue
		}
		var carrier uint32
		for _, r := range low {
			carrier += uint32(CarrierUnits(r))
		}
		if carrier < FTS5BM25TermMinCarrierX10 {
			continue
		}
		out = append(out, low)
	}
	return out
}

// Declared high-nibble form flags (bit5 / bit6 / bit7) — pure-Unicode structural facts that are
// free on the client plaintext side and impossible to rebuild from blind `{h, b, f}` pulses:
//
//	bit5 FlagIsFocalHeader    focal header / heading / summary-field marker
//	bit6 FlagHasBoundaryQuote strong-delimiter (bracket / quote) envelope marker
//	bit7 FlagIsQuantityOrUnit quantity-dimension dyad marker (dimension side)
//
// All three predicates are language-agnostic and zero-lexicon: they only use Unicode general
// categories (`Ps`/`Pe`/`Pi`/`Pf`), the CommonMark ATX heading bound (`#` 1..6) and the connector /
// compound-splitting predicates shared with the tokenizer. They feed continuous gains and soft
// suppression only — never a boolean gate.
const (
	// FlagIsHarmonicCompanion marks a companion harmonic overtone slot (`2k-1` / `2k-3`).
	FlagIsHarmonicCompanion uint8 = 1 << 4
	// FlagIsFocalHeader marks the lead slot of a focal header line (ATX heading or `label:` field line).
	FlagIsFocalHeader uint8 = 1 << 5
	// FlagHasBoundaryQuote marks the lead slot enclosed by a strong delimiter (open bracket / quote).
	FlagHasBoundaryQuote uint8 = 1 << 6
	// FlagIsQuantityOrUnit marks the dimension side of an adjacent numeric-quantity dyad (`50 ms`, `50kg`).
	FlagIsQuantityOrUnit uint8 = 1 << 7
)

// BlindFormFlagMarks computes the declared form flags aligned one-to-one with the `SplitWords`
// items of a sentence (index i holds the mask to OR into the flags of words[i]).
func BlindFormFlagMarks(line string, words []string) []uint8 {
	return blindFormFlagMarks(line, words)
}

// blindFormFlagMarks computes the declared form flags aligned one-to-one with the `SplitWords`
// items of a sentence (index i holds the mask to OR into the flags of words[i]).
func blindFormFlagMarks(line string, words []string) []uint8 {
	if len(words) == 0 {
		return nil
	}
	marks := make([]uint8, len(words))
	if r, ok := firstNonSpaceRune(line); ok && isStrongDelimiterRune(r) {
		marks[0] |= FlagHasBoundaryQuote
	}
	if isBlindFocalHeaderLine(line) {
		marks[0] |= FlagIsFocalHeader
	}
	parents := blindCompoundParentIdx(words)
	carrierStarts := blindAdjacentCarrierStarts(line)
	for i := 1; i < len(words); i++ {
		if !blindNumericAnchorAt(words, i-1) || !blindUnitCarrierAt(words, parents, i-1, i) {
			continue
		}
		if !blindCarrierTextuallyAdjacent(line, carrierStarts, words[i]) {
			continue
		}
		marks[i] |= FlagIsQuantityOrUnit
	}
	return marks
}

// blindAdjacentCarrierStarts returns the byte offsets of every word run that directly follows a
// pure numeric run with nothing but whitespace in between. A punctuation/symbol separator
// (`10 & aaaaa_repeat`) therefore never forms a numeric-dimension dyad.
func blindAdjacentCarrierStarts(line string) []int {
	if line == "" {
		return nil
	}
	runes := make([]rune, 0, len(line))
	offsets := make([]int, 0, len(line))
	for i, r := range line {
		runes = append(runes, r)
		offsets = append(offsets, i)
	}
	var starts []int
	for i := 0; i < len(runes); {
		if !unicode.IsNumber(runes[i]) && !isWordConn(runes[i]) {
			i++
			continue
		}
		hasDigit := false
		j := i
		for j < len(runes) {
			if unicode.IsNumber(runes[j]) {
				hasDigit = true
				j++
				continue
			}
			if isWordConn(runes[j]) {
				j++
				continue
			}
			break
		}
		if hasDigit {
			k := j
			for k < len(runes) && unicode.IsSpace(runes[k]) {
				k++
			}
			if k < len(runes) && (unicode.IsLetter(runes[k]) || unicode.IsMark(runes[k])) {
				starts = append(starts, offsets[k])
			}
		}
		i = j
	}
	return starts
}

// blindCarrierTextuallyAdjacent reports whether the carrier item starts at one of the adjacent
// word-run offsets (the item text is always a prefix of the run it was cut from).
func blindCarrierTextuallyAdjacent(line string, starts []int, carrier string) bool {
	if carrier == "" {
		return false
	}
	for _, s := range starts {
		if s <= len(line) && strings.HasPrefix(line[s:], carrier) {
			return true
		}
	}
	return false
}

// blindCompoundParentIdx rebuilds the compound-splitting structure of `SplitWords`: that function
// appends the `splitCompoundSubwords` children right after each compound item, so the same pure
// form predicate can recover the child -> parent ownership in place (non-children return -1).
func blindCompoundParentIdx(words []string) []int {
	parents := make([]int, len(words))
	for i := range parents {
		parents[i] = -1
	}
	for i := 0; i < len(words); i++ {
		if parents[i] >= 0 {
			continue
		}
		subs := splitCompoundSubwords(words[i])
		if len(subs) < 2 {
			continue
		}
		for k := 1; k <= len(subs) && i+k < len(words); k++ {
			if words[i+k] != subs[k-1] {
				break
			}
			parents[i+k] = i
		}
	}
	return parents
}

// blindNumericAnchorAt reports whether item i can act as the numeric anchor of a dyad:
// a pure numeric token itself (`50`, `2.5`, `50-60`, including numeric children such as `36.5`).
func blindNumericAnchorAt(words []string, i int) bool {
	return i >= 0 && i < len(words) && isBlindNumericOnlyToken(words[i])
}

// blindUnitCarrierAt reports whether item i can act as the dimension side of a dyad anchored at
// item anchor = i-1:
//   - carrier is a compound child: it must share the parent with the anchor and the parent must be
//     a digit-led alphanumeric token (`50ms` -> `ms`, `50kg` -> `kg`);
//   - carrier is a standalone item: when the anchor itself is a child, its parent must be pure
//     numeric (`36.5 C`, `50-60 Hz`); digit children of letter-led mixed parents (`v1.0.1`,
//     `WaveEngine_256`, `GPT-4`) must never leak onto the following item — this is the structural
//     reason the pinned cross-repo `RAME-BlindPulse-v1` canary vector stays byte-identical.
func blindUnitCarrierAt(words []string, parents []int, anchor, i int) bool {
	if !isBlindLetterOnlyToken(words[i]) {
		return false
	}
	carrierParent := parents[i]
	if carrierParent >= 0 {
		return carrierParent == parents[anchor] && isBlindDigitLedAlnumToken(words[carrierParent])
	}
	if anchorParent := parents[anchor]; anchorParent >= 0 {
		return isBlindNumericOnlyToken(words[anchorParent])
	}
	return true
}

// isBlindFocalHeaderLine reports whether the line looks like a focal header (zero lexicon):
//  1. ATX heading: 1..6 leading `#` (CommonMark 4.2) not followed by another `#`;
//  2. field label line: the prefix before the first colon is non-empty, space-free and free of
//     sentence terminators (`time: ...`, `Date: ...`).
func isBlindFocalHeaderLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return false
	}
	if isATXHeadingPrefix(trimmed) {
		return true
	}
	return hasLeadingFieldLabelPrefix(trimmed)
}

// isATXHeadingPrefix reports an ATX heading prefix: 1..6 leading `#` not followed by another `#`.
// Whitespace after the run is not required: space-less scripts write `#title`, and demanding a
// space would leak a whitespace-segmentation assumption into the predicate.
func isATXHeadingPrefix(trimmed string) bool {
	n := 0
	for _, r := range trimmed {
		if r != '#' {
			break
		}
		n++
		if n > 6 {
			return false
		}
	}
	if n == 0 {
		return false
	}
	rest := trimmed[n:]
	return rest == "" || !strings.HasPrefix(rest, "#")
}

// hasLeadingFieldLabelPrefix reports a `label: <value>` field prefix: the prefix before the first
// colon must be non-empty, space-free and terminator-free, and the value side must start with a
// pure numeric structured payload. Zero lexicon — the numeric-value requirement is what separates
// card metadata (`Date: 2026-05-01`, `ID: 0012`) from CJK definitional clauses (`单字：好`) and
// quoted-speech leads (`他说：你好`), which therefore stay unmarked.
func hasLeadingFieldLabelPrefix(trimmed string) bool {
	colon, colonLen := -1, 0
	for idx, r := range trimmed {
		if isFieldLabelColon(r) {
			colon, colonLen = idx, len(string(r))
			break
		}
	}
	if colon <= 0 {
		return false
	}
	for _, r := range trimmed[:colon] {
		if unicode.IsSpace(r) || isBlindSentenceTerminator(r) {
			return false
		}
	}
	return isBlindNumericLedValue(trimmed[colon+colonLen:])
}

// isBlindNumericLedValue reports whether the post-colon value starts with a pure numeric payload.
func isBlindNumericLedValue(rest string) bool {
	rest = strings.TrimLeftFunc(rest, unicode.IsSpace)
	if rest == "" {
		return false
	}
	hasDigit := false
	for _, r := range rest {
		switch {
		case unicode.IsNumber(r):
			hasDigit = true
		case isWordConn(r):
		default:
			return hasDigit
		}
	}
	return hasDigit
}

// isFieldLabelColon matches the half-width and full-width field label colons.
func isFieldLabelColon(r rune) bool { return r == ':' || r == '：' }

// isBlindSentenceTerminator matches the sentence boundary shapes used by `SplitSentences`.
func isBlindSentenceTerminator(r rune) bool {
	switch r {
	case '\n', '\r', '.', '。', '！', '？', '；', ';', '!', '?':
		return true
	}
	return false
}

// isStrongDelimiterRune reports Unicode strong delimiters: open brackets (Ps), close brackets (Pe),
// initial quotes (Pi) and final quotes (Pf).
func isStrongDelimiterRune(r rune) bool {
	return unicode.Is(unicode.Ps, r) || unicode.Is(unicode.Pe, r) ||
		unicode.Is(unicode.Pi, r) || unicode.Is(unicode.Pf, r)
}

// isBlindNumericOnlyToken reports a pure numeric token (>= 1 digit, zero letters; connectors allowed).
func isBlindNumericOnlyToken(tok string) bool {
	hasDigit := false
	for _, r := range tok {
		switch {
		case unicode.IsNumber(r):
			hasDigit = true
		case isWordConn(r):
		default:
			return false
		}
	}
	return hasDigit
}

// isBlindLetterOnlyToken reports a pure letter token (>= 1 letter, zero digits).
func isBlindLetterOnlyToken(tok string) bool {
	hasLetter := false
	for _, r := range tok {
		switch {
		case unicode.IsNumber(r):
			return false
		case unicode.IsLetter(r) || unicode.IsMark(r):
			hasLetter = true
		}
	}
	return hasLetter
}

// isBlindDigitLedAlnumToken reports a digit-led alphanumeric token (`50kg`, `50ms`, `2FA`).
func isBlindDigitLedAlnumToken(tok string) bool {
	for _, r := range tok {
		if !unicode.IsNumber(r) {
			return false
		}
		return !isBlindNumericOnlyToken(tok)
	}
	return false
}

// firstNonSpaceRune returns the first non-space rune of the line.
func firstNonSpaceRune(line string) (rune, bool) {
	for _, r := range line {
		if !unicode.IsSpace(r) {
			return r, true
		}
	}
	return 0, false
}

func (e *BlindEncoder) packSymbolAtoms(sent string) []BlindTokenAtom {
	var atoms []BlindTokenAtom
	var prevR rune
	runLen, hasLower := 0, false
	for idx, r := range sent {
		if unicode.IsSpace(r) {
			continue
		}
		if lr := unicode.ToLower(r); lr == prevR {
			runLen++
		} else {
			prevR, runLen = lr, 1
		}
		rw := uint32(CarrierUnits(r))
		if runLen >= 3 {
			rw /= 4
		}
		var fl uint8
		fl, hasLower = classifyRuneFlag(r, idx > 0, idx > 0 && hasLower)
		atoms = append(atoms, BlindTokenAtom{H: e.KeyedHash64(strings.ToLower(string(r))), RawBitsX10: clampBitsX10(rw), ClassFlags: fl})
	}
	return atoms
}

func (e *BlindEncoder) buildSingleWordAtom(w string, wordIdx int, extraFlags uint8) (BlindTokenAtom, bool) {
	low := strings.ToLower(strings.TrimSpace(w))
	if low == "" {
		return BlindTokenAtom{}, false
	}
	var rawWidth uint32
	flags := extraFlags
	var prevR rune
	runLen, hasLower := 0, false
	for idx, r := range w {
		if unicode.IsSpace(r) {
			continue
		}
		if lr := unicode.ToLower(r); lr == prevR {
			runLen++
		} else {
			prevR, runLen = lr, 1
		}
		rw := uint32(CarrierUnits(r))
		if runLen >= 3 {
			rw /= 4
		}
		rawWidth += rw
		var fl uint8
		fl, hasLower = classifyRuneFlag(r, wordIdx > 0 || idx > 0, idx > 0 && hasLower)
		flags |= fl
	}
	return BlindTokenAtom{H: e.KeyedHash64(low), RawBitsX10: clampBitsX10(rawWidth), ClassFlags: flags}, true
}

func adaptiveSubcarrierSlices(tok string) []string {
	var runeOffsets, runeWidths []int
	totalW := 0
	for idx, r := range tok {
		if unicode.IsSpace(r) {
			continue
		}
		runeOffsets = append(runeOffsets, idx)
		rw := int(CarrierUnits(r))
		runeWidths = append(runeWidths, rw)
		totalW += rw
	}
	runeCount := len(runeOffsets)
	if runeCount < 4 || totalW <= 160 {
		return nil
	}
	k := (totalW + 119) / 120
	if k < 2 {
		k = 2
	}
	if k > 8 {
		k = 8
	}
	if k > runeCount/2 {
		k = runeCount / 2
	}
	if k < 2 {
		return nil
	}
	targetPerSub := totalW / k
	if targetPerSub < 40 {
		targetPerSub = 40
	}
	var out []string
	startPos, accW := 0, 0
	for i := 0; i < runeCount; i++ {
		accW += runeWidths[i]
		remainingRunes := runeCount - 1 - i
		remainingChunks := k - len(out) - 1
		if remainingChunks > 0 && accW >= targetPerSub && remainingRunes >= remainingChunks*2 {
			out = append(out, tok[runeOffsets[startPos]:runeOffsets[i+1]])
			startPos, accW = i+1, 0
		}
	}
	if startPos < runeCount {
		out = append(out, tok[runeOffsets[startPos]:])
	}
	if len(out) < 2 {
		return nil
	}
	return out
}

func (e *BlindEncoder) appendAdaptiveHarmonicAtoms(sAtoms []BlindTokenAtom, cwTrim string, wordIdx int, gluedThresholdX10 uint16) []BlindTokenAtom {
	parentAtom, ok := e.buildSingleWordAtom(cwTrim, wordIdx, 0)
	if !ok {
		return sAtoms
	}
	rawSubs := SplitWords(cwTrim)
	var distinctSubs []string
	for _, sub := range rawSubs {
		if st := strings.TrimSpace(sub); st != "" && !strings.EqualFold(st, cwTrim) {
			distinctSubs = append(distinctSubs, st)
		}
	}
	if len(distinctSubs) < 2 && parentAtom.RawBitsX10 > gluedThresholdX10 {
		distinctSubs = adaptiveSubcarrierSlices(cwTrim)
	}
	if len(distinctSubs) < 2 {
		return append(sAtoms, parentAtom)
	}
	subAtoms := make([]BlindTokenAtom, 0, len(distinctSubs))
	subStrings := make([]string, 0, len(distinctSubs))
	for subIdx, sub := range distinctSubs {
		if sa, sOK := e.buildSingleWordAtom(sub, wordIdx+subIdx, 0); sOK && sa.H != parentAtom.H {
			subAtoms = append(subAtoms, sa)
			subStrings = append(subStrings, sub)
		}
	}
	if len(subAtoms) < 2 {
		return append(sAtoms, parentAtom)
	}
	if len(subAtoms) <= 8 {
		var sumSubB uint32
		maxSubB := uint16(0)
		for _, sa := range subAtoms {
			sumSubB += uint32(sa.RawBitsX10)
			if sa.RawBitsX10 > maxSubB {
				maxSubB = sa.RawBitsX10
			}
		}
		if maxSubB >= 33 && sumSubB >= 60 {
			parentAtom.ClassFlags |= FlagHasConn
			parentAtom.RawBitsX10 = clampBitsX10(sumSubB)
			sAtoms = append(sAtoms, parentAtom)
			return append(sAtoms, subAtoms...)
		}
		return append(sAtoms, parentAtom)
	}
	for start := 0; start < len(subAtoms); start += 8 {
		end := start + 8
		if end > len(subAtoms) {
			end = len(subAtoms)
		}
		chunk := subAtoms[start:end]
		if len(chunk) == 1 {
			sAtoms = append(sAtoms, chunk[0])
			continue
		}
		var sumChunkB uint32
		for _, sa := range chunk {
			sumChunkB += uint32(sa.RawBitsX10)
		}
		frameParent := parentAtom
		if start > 0 {
			if fp, fpOK := e.buildSingleWordAtom(strings.Join(subStrings[start:end], " "), wordIdx+start, FlagHasConn); fpOK {
				frameParent = fp
			}
		}
		frameParent.ClassFlags |= FlagHasConn
		if sumChunkB > 65535 {
			frameParent.RawBitsX10 = 65535
		} else if sumChunkB >= 60 {
			frameParent.RawBitsX10 = uint16(sumChunkB)
		}
		sAtoms = append(sAtoms, frameParent)
		sAtoms = append(sAtoms, chunk...)
	}
	return sAtoms
}

func carrierWidthClamped(w string) uint16 {
	var rawWidth uint32
	var prevR rune
	runLen := 0
	for _, r := range w {
		if lr := unicode.ToLower(r); lr == prevR {
			runLen++
		} else {
			prevR, runLen = lr, 1
		}
		rw := uint32(CarrierUnits(r))
		if runLen >= 3 {
			rw /= 4
		}
		rawWidth += rw
	}
	return clampBitsX10(rawWidth)
}

func (e *BlindEncoder) PackSentenceAtoms(sent string) []BlindTokenAtom {
	words := SplitWords(sent)
	if len(words) == 0 {
		return e.packSymbolAtoms(sent)
	}
	// Declared high-nibble form flags (bit5 focal header / bit6 boundary quote / bit7 quantity
	// dimension dyad side), aligned one-to-one with the SplitWords items — see formflags.go.
	formMarks := blindFormFlagMarks(sent, words)
	atoms := make([]BlindTokenAtom, 0, len(words))
	var expandedWords map[string]bool
	if e != nil && e.harmOpts.Expander != nil {
		expandedWords = make(map[string]bool, len(words)+4)
	}
	for wordIdx, w := range words {
		low := strings.ToLower(strings.TrimSpace(w))
		if low == "" {
			continue
		}
		var rawWidth uint32
		var flags uint8
		var prevR rune
		runLen, hasLower := 0, false
		for idx, r := range w {
			if lr := unicode.ToLower(r); lr == prevR {
				runLen++
			} else {
				prevR, runLen = lr, 1
			}
			rw := uint32(CarrierUnits(r))
			if runLen >= 3 {
				rw /= 4
			}
			rawWidth += rw
			var fl uint8
			fl, hasLower = classifyRuneFlag(r, wordIdx > 0 || idx > 0, idx > 0 && hasLower)
			flags |= fl
		}
		clampedW := clampBitsX10(rawWidth)
		flags |= formMarks[wordIdx]
		if clampedW > 420 && len(splitCompoundSubwords(w)) < 2 {
			prevLen := len(atoms)
			atoms = e.appendAdaptiveHarmonicAtoms(atoms, w, wordIdx, 420)
			if len(atoms) > prevLen && formMarks[wordIdx] != 0 {
				atoms[prevLen].ClassFlags |= formMarks[wordIdx]
			}
			continue
		}
		if expandedWords != nil {
			expandedWords[low] = true
		}
		atoms = e.appendWordWithCanonicalHarmonics(atoms, low, clampedW, flags)
	}
	if e != nil && e.harmOpts.Expander != nil {
		for _, symUnit := range ExtractInlineSymbolUnits(sent) {
			if expandedWords[symUnit] {
				continue
			}
			expandedWords[symUnit] = true
			atoms = e.appendWordWithCanonicalHarmonics(atoms, symUnit, carrierWidthClamped(symUnit), FlagIsQuantityOrUnit)
		}
		for _, seaUnit := range ExtractSEAsianUnitWords(sent) {
			if expandedWords[seaUnit] {
				continue
			}
			expandedWords[seaUnit] = true
			atoms = e.appendWordWithCanonicalHarmonics(atoms, seaUnit, carrierWidthClamped(seaUnit), 0)
		}
	}
	atoms = e.appendStructuredDateHarmonicAtoms(sent, atoms)
	return atoms
}

// ExtractStructuredDateHarmonics scans sentence text using a zero-lexicon numeric-separator
// state machine (`YYYY-MM-DD`, `YYYY/M/D`, `YYYY.MM.DD`, `YYYY年M月D日`) and returns
// canonical ISO `"YYYY-MM-DD"` strings for valid Gregorian dates (Y in [1900,2199], M in [1,12], D in [1,31]).
func ExtractStructuredDateHarmonics(sent string) []string {
	type numSpan struct {
		val       int
		digits    int
		startRune int
		endRune   int
	}
	runes := []rune(sent)
	n := len(runes)
	if n < 8 {
		return nil
	}
	var spans []numSpan
	for i := 0; i < n; {
		if runes[i] >= '0' && runes[i] <= '9' {
			start := i
			val := 0
			for i < n && runes[i] >= '0' && runes[i] <= '9' {
				if i-start < 6 {
					val = val*10 + int(runes[i]-'0')
				}
				i++
			}
			spans = append(spans, numSpan{val: val, digits: i - start, startRune: start, endRune: i})
		} else {
			i++
		}
	}
	if len(spans) < 3 {
		return nil
	}
	var out []string
	seen := make(map[string]bool, 2)
	for i := 0; i+2 < len(spans); i++ {
		ySpan, mSpan, dSpan := spans[i], spans[i+1], spans[i+2]
		if ySpan.digits != 4 || mSpan.digits < 1 || mSpan.digits > 2 || dSpan.digits < 1 || dSpan.digits > 2 {
			continue
		}
		if ySpan.val < 1900 || ySpan.val > 2199 || mSpan.val < 1 || mSpan.val > 12 || dSpan.val < 1 || dSpan.val > 31 {
			continue
		}
		gap1 := mSpan.startRune - ySpan.endRune
		gap2 := dSpan.startRune - mSpan.endRune
		if gap1 != 1 || gap2 != 1 {
			continue
		}
		sep1 := runes[ySpan.endRune]
		sep2 := runes[mSpan.endRune]
		if (sep1 >= '0' && sep1 <= '9') || (sep2 >= '0' && sep2 <= '9') || unicode.IsSpace(sep1) || unicode.IsSpace(sep2) {
			continue
		}
		// Reject IPv4 / version chains with an immediately connected 4th numeric segment (e.g. 1920.10.10.1)
		if i > 0 && ySpan.startRune-spans[i-1].endRune == 1 && isWordConn(runes[spans[i-1].endRune]) {
			continue
		}
		if i+3 < len(spans) && spans[i+3].startRune-dSpan.endRune == 1 && isWordConn(runes[dSpan.endRune]) {
			continue
		}
		iso := fmt.Sprintf("%04d-%02d-%02d", ySpan.val, mSpan.val, dSpan.val)
		if !seen[iso] {
			seen[iso] = true
			out = append(out, iso)
		}
		i += 2
	}
	return out
}

func (e *BlindEncoder) appendWordWithCanonicalHarmonics(atoms []BlindTokenAtom, low string, clampedW uint16, flags uint8) []BlindTokenAtom {
	if e == nil || e.harmOpts.Expander == nil {
		return append(atoms, BlindTokenAtom{H: e.KeyedHash64(low), RawBitsX10: clampedW, ClassFlags: flags})
	}
	tags := e.harmOpts.Expander(low)
	var cleanTags []string
	for _, t := range tags {
		tt := strings.ToLower(strings.TrimSpace(t))
		if tt != "" && tt != low {
			cleanTags = append(cleanTags, tt)
			if len(cleanTags) == 2 {
				break
			}
		}
	}
	if len(cleanTags) == 0 {
		return append(atoms, BlindTokenAtom{H: e.KeyedHash64(low), RawBitsX10: clampedW, ClassFlags: flags})
	}
	evenW := clampedW
	if (evenW & 1) != 0 {
		if evenW < 65534 {
			evenW++
		} else {
			evenW--
		}
	}
	if evenW < 24 {
		evenW = 24
	}
	primaryH := e.KeyedHash64(low)
	atoms = append(atoms, BlindTokenAtom{H: primaryH, RawBitsX10: evenW, ClassFlags: flags &^ FlagHasConn})
	h1 := e.KeyedHash64(cleanTags[0])
	if h1 != primaryH {
		atoms = append(atoms, BlindTokenAtom{H: h1, RawBitsX10: evenW - 1, ClassFlags: FlagHasConn | FlagIsHarmonicCompanion})
	}
	if len(cleanTags) >= 2 {
		h2 := e.KeyedHash64(cleanTags[1])
		if h2 != primaryH && h2 != h1 {
			atoms = append(atoms, BlindTokenAtom{H: h2, RawBitsX10: evenW - 3, ClassFlags: FlagHasConn | FlagIsHarmonicCompanion})
		}
	}
	return atoms
}

// CalendarBeaconWidthX10 is the carrier width of the canonical calendar-beacon companion pulse
// (`H(K_user, "cal:"+iso)`, `f = FlagHasDigit | FlagHasConn`) emitted alongside the ISO date pulse
// when structured dates are enabled. The dedicated width (distinct from the native date-token
// width 284) marks the pulse as an opaque calendar anchor the cloud engine consumes for
// calendar-axis resonance; every shipped SDK and companion packer pins this exact value.
const CalendarBeaconWidthX10 uint16 = 588

func (e *BlindEncoder) appendStructuredDateHarmonicAtoms(sent string, atoms []BlindTokenAtom) []BlindTokenAtom {
	if e == nil || !e.harmOpts.EnableStructuredDates || len(atoms) == 0 {
		return atoms
	}
	dates := ExtractStructuredDateHarmonics(sent)
	if len(dates) == 0 {
		return atoms
	}
	existing := make(map[uint64]bool, len(atoms))
	for _, a := range atoms {
		existing[a.H] = true
	}
	for _, iso := range dates {
		calH := e.KeyedHash64("cal:" + iso)
		if !existing[calH] {
			existing[calH] = true
			atoms = append(atoms, BlindTokenAtom{
				H:          calH,
				RawBitsX10: CalendarBeaconWidthX10,
				ClassFlags: FlagHasDigit | FlagHasConn,
			})
		}
		h := e.KeyedHash64(iso)
		if existing[h] {
			continue
		}
		existing[h] = true
		atoms = append(atoms, BlindTokenAtom{
			H:          h,
			RawBitsX10: 284, // 8 digits (8*33=264) + 2 hyphens (2*10=20) = 284
			ClassFlags: FlagHasDigit | FlagHasConn,
		})
	}
	return atoms
}

func (e *BlindEncoder) PackBlindPacketWithSegmenter(docID string, text string, occurredAt int64, opts SegmenterPackOptions) (BlindPulsePacket, []string) {
	if opts.Segmenter == nil && opts.SentenceAtomPacker == nil {
		return e.packBlindPacketDefault(docID, text, occurredAt)
	}
	autoHarmonic := !opts.DisableAutoHarmonic
	trimmed := strings.TrimSpace(text)
	sents := SplitSentences(text)
	if len(sents) == 0 && trimmed != "" {
		sents = []string{trimmed}
	}
	var allSlots []BlindTokenAtom
	var sentFrames [][]BlindTokenAtom
	var validSents []string
	for sentIdx, s := range sents {
		var sAtoms []BlindTokenAtom
		if opts.SentenceAtomPacker != nil {
			sAtoms = opts.SentenceAtomPacker(e, s, sentIdx)
		} else {
			customWords := opts.Segmenter(s)
			for wordIdx, cw := range customWords {
				cwTrim := strings.TrimSpace(cw)
				if cwTrim == "" {
					continue
				}
				if !autoHarmonic {
					if atom, ok := e.buildSingleWordAtom(cwTrim, wordIdx, 0); ok {
						sAtoms = append(sAtoms, atom)
					}
					continue
				}
				sAtoms = e.appendAdaptiveHarmonicAtoms(sAtoms, cwTrim, wordIdx, 320)
			}
		}
		if len(sAtoms) == 0 {
			continue
		}
		validSents = append(validSents, s)
		sentFrames = append(sentFrames, sAtoms)
		allSlots = append(allSlots, sAtoms...)
	}
	var atomsB64 string
	if len(allSlots) > 0 {
		atomsB64, _ = EncodeWirePayload(allSlots, sentFrames)
	}
	return BlindPulsePacket{UnitID: docID, OccurredAt: occurredAt, Slots: allSlots, SentenceFrames: sentFrames, AtomsB64: atomsB64}, validSents
}

func (e *BlindEncoder) packBlindPacketDefault(docID string, text string, occurredAt int64) (BlindPulsePacket, []string) {
	trimmed := strings.TrimSpace(text)
	var sents []string
	if trimmed != "" && len(SplitWords(trimmed)) == 0 {
		sents = []string{trimmed}
	} else if sents = SplitSentences(text); len(sents) == 0 && trimmed != "" {
		sents = []string{trimmed}
	}
	var allSlots []BlindTokenAtom
	var sentFrames [][]BlindTokenAtom
	var validSents []string
	for _, s := range sents {
		if sAtoms := e.PackSentenceAtoms(s); len(sAtoms) > 0 {
			validSents = append(validSents, s)
			sentFrames = append(sentFrames, sAtoms)
			allSlots = append(allSlots, sAtoms...)
		}
	}
	var atomsB64 string
	if len(allSlots) > 0 {
		atomsB64, _ = EncodeWirePayload(allSlots, sentFrames)
	}
	return BlindPulsePacket{UnitID: docID, OccurredAt: occurredAt, Slots: allSlots, SentenceFrames: sentFrames, AtomsB64: atomsB64}, validSents
}

func (e *BlindEncoder) PackBlindPacket(docID string, text string, occurredAt int64) (BlindPulsePacket, []string) {
	if e != nil && (e.segOpts.Segmenter != nil || e.segOpts.SentenceAtomPacker != nil) {
		return e.PackBlindPacketWithSegmenter(docID, text, occurredAt, e.segOpts)
	}
	return e.packBlindPacketDefault(docID, text, occurredAt)
}

func (e *BlindEncoder) PackBlindPacketWithMeta(docID string, text string, meta UnitMeta) (BlindPulsePacket, []string) {
	fullText := FormatPlaintextWithMeta(text, meta)
	occ := meta.OccurredAt
	if occ <= 0 && meta.NarratedAt > 0 {
		occ = meta.NarratedAt
	}
	pkt, validSents := e.PackBlindPacket(docID, fullText, occ)
	if meta.NarratedAt > 0 {
		pkt.NarratedAt = meta.NarratedAt
	}
	if meta.IngestedAt > 0 {
		pkt.IngestedAt = meta.IngestedAt
	}
	if author := strings.ToLower(strings.TrimSpace(meta.Author)); author != "" {
		pkt.AuthorHash = e.KeyedHash64(author)
	}
	pkt.DocHash = meta.DocHash
	if pkt.DocHash == 0 {
		if fileURL := strings.TrimSpace(meta.FileURL); fileURL != "" {
			pkt.DocHash = e.KeyedHash64(fileURL)
		}
	}
	pkt.BlockHash = meta.BlockHash
	if pkt.BlockHash == 0 {
		if blockID := strings.TrimSpace(meta.BlockID); blockID != "" {
			pkt.BlockHash = e.KeyedHash64(blockID)
		}
	}
	if meta.LineStart > 0 {
		pkt.LineStart = meta.LineStart
	}
	if meta.LineEnd > 0 {
		pkt.LineEnd = meta.LineEnd
	}
	if pkt.LineStart > 0 && pkt.LineEnd < pkt.LineStart {
		pkt.LineEnd = pkt.LineStart
	}
	return pkt, validSents
}

type SubUnit struct {
	Kind      string `json:"kind"`
	ID        string `json:"id,omitempty"`
	Text      string `json:"text"`
	BlockID   string `json:"block_id,omitempty"`
	FileParse string `json:"file_parse,omitempty"`
	LineStart int    `json:"line_start,omitempty"`
	LineEnd   int    `json:"line_end,omitempty"`
}

func ComposeSubUnitID(parentID, kind string, index int) string {
	p, k := strings.TrimSpace(parentID), strings.TrimSpace(kind)
	if k == "" {
		k = "sub"
	}
	if index <= 0 {
		return p + "#" + k
	}
	return fmt.Sprintf("%s#%s:%d", p, k, index)
}

func resolveSubUnitIDAndText(parentID string, su SubUnit, kindCounts map[string]int) (subID, kind, txt string, idx int, ok bool) {
	txt = strings.TrimSpace(su.Text)
	fp := strings.TrimSpace(su.FileParse)
	if txt == "" {
		txt = fp
	} else if fp != "" && !strings.Contains(txt, fp) {
		txt = txt + "\n" + fp
	}
	if txt == "" {
		return "", "", "", 0, false
	}
	if kind = strings.TrimSpace(su.Kind); kind == "" {
		kind = "sub"
	}
	idx = kindCounts[kind]
	kindCounts[kind] = idx + 1
	if subID = strings.TrimSpace(su.ID); subID == "" {
		subID = ComposeSubUnitID(parentID, kind, idx)
	}
	return subID, kind, txt, idx, true
}

func (e *BlindEncoder) PackCompositePackets(parentID string, parentText string, occurredAt int64, subUnits []SubUnit) ([]BlindPulsePacket, map[string]string) {
	parentID = strings.TrimSpace(parentID)
	var parentDocHash uint64
	if parentID != "" && e != nil {
		parentDocHash = e.KeyedHash64(parentID)
	}
	subToParent := make(map[string]string, len(subUnits)*2)
	var pkts []BlindPulsePacket
	if parentID != "" && strings.TrimSpace(parentText) != "" {
		if pkt, _ := e.PackBlindPacket(parentID, parentText, occurredAt); len(pkt.Slots) > 0 {
			pkt.DocHash = parentDocHash
			pkts = append(pkts, pkt)
		}
	}
	kindCounts := make(map[string]int, 4)
	for _, su := range subUnits {
		subID, kind, txt, idx, ok := resolveSubUnitIDAndText(parentID, su, kindCounts)
		if !ok {
			continue
		}
		subPkt, _ := e.PackBlindPacket(subID, txt, occurredAt)
		if len(subPkt.Slots) == 0 {
			continue
		}
		subPkt.DocHash = parentDocHash
		if e != nil {
			if blkID := strings.TrimSpace(su.BlockID); blkID != "" {
				subPkt.BlockHash = e.KeyedHash64(blkID)
			} else if subID != "" {
				subPkt.BlockHash = e.KeyedHash64(subID)
			}
		}
		if su.LineStart > 0 {
			subPkt.LineStart = su.LineStart
		}
		if su.LineEnd > 0 {
			subPkt.LineEnd = su.LineEnd
		}
		if subPkt.LineStart > 0 && subPkt.LineEnd < subPkt.LineStart {
			subPkt.LineEnd = subPkt.LineStart
		}
		if parentID != "" {
			subPkt.CitedUnitIDs = []string{parentID}
			subToParent[subID] = parentID
			if idx == 0 {
				subToParent[fmt.Sprintf("%s#%s:0", parentID, kind)] = parentID
			}
		}
		pkts = append(pkts, subPkt)
	}
	return pkts, subToParent
}

type PlaintextStore interface {
	PackBlindPacket(docID string, text string, occurredAt int64) (BlindPulsePacket, []string)
	SaveLocalPlaintext(docID string, occurredAt int64, rawText string, sents []string, resp CloudIngestResponse) error
	LoadLocalDocument(docID string) (rawText string, distilledText string, keptSents []string, err error)
}

func distillPlaintext(rawText string, allSents []string, keptIdx []int) (string, string, []string, error) {
	if len(keptIdx) == 0 || len(allSents) == 0 || len(keptIdx) >= len(allSents) {
		return rawText, rawText, allSents, nil
	}
	keptSents := make([]string, 0, len(keptIdx))
	for _, idx := range keptIdx {
		if idx >= 0 && idx < len(allSents) {
			keptSents = append(keptSents, allSents[idx])
		}
	}
	if len(keptSents) == 0 || len(keptSents) >= len(allSents) {
		return rawText, rawText, allSents, nil
	}
	sep := "\n"
	if !strings.Contains(rawText, "\n") {
		sep = ". "
	}
	return rawText, strings.Join(keptSents, sep), keptSents, nil
}

type jsonDocRecord struct {
	DocID       string   `json:"doc_id"`
	OccurredAt  int64    `json:"occurred_at"`
	RawText     string   `json:"raw_text"`
	Sentences   []string `json:"sentences"`
	AddressURI  string   `json:"address_uri"`
	Coord64     uint64   `json:"coord64"`
	KeptIndices []int    `json:"kept_indices"`
}

type JSONFileStore struct {
	*BlindEncoder
	mu          sync.RWMutex
	filePath    string
	docs        map[string]jsonDocRecord
	subToParent map[string]string
}

var _ PlaintextStore = (*JSONFileStore)(nil)

func NewJSONFileStore(path string, secret []byte) (*JSONFileStore, error) {
	filePath := strings.TrimSpace(path)
	if filePath == "" {
		return nil, errors.New("ramesdk: empty json file path")
	}
	s := &JSONFileStore{BlindEncoder: NewBlindEncoder(secret), filePath: filePath, docs: make(map[string]jsonDocRecord), subToParent: make(map[string]string)}
	raw, err := os.ReadFile(filePath)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	} else if err != nil {
		return nil, fmt.Errorf("ramesdk: read JSON store: %w", err)
	}
	if err := json.Unmarshal(raw, &s.docs); err != nil || s.docs == nil {
		return nil, fmt.Errorf("ramesdk: decode JSON store: %v", err)
	}
	return s, nil
}

func (s *JSONFileStore) SaveSubUnitMapping(subUnitID, parentUnitID string) {
	if subUnitID, parentUnitID = strings.TrimSpace(subUnitID), strings.TrimSpace(parentUnitID); subUnitID == "" || parentUnitID == "" {
		return
	}
	s.mu.Lock()
	if s.subToParent == nil {
		s.subToParent = make(map[string]string)
	}
	s.subToParent[subUnitID] = parentUnitID
	s.mu.Unlock()
}

func (s *JSONFileStore) ResolveCanonicalUnitID(unitID string) string {
	unitID = strings.TrimSpace(unitID)
	s.mu.RLock()
	p := s.subToParent[unitID]
	s.mu.RUnlock()
	if p != "" {
		return p
	}
	return unitID
}

func (s *JSONFileStore) SaveLocalPlaintext(docID string, occurredAt int64, rawText string, sents []string, resp CloudIngestResponse) error {
	s.mu.Lock()
	s.docs[docID] = jsonDocRecord{DocID: docID, OccurredAt: occurredAt, RawText: rawText, Sentences: append([]string(nil), sents...), AddressURI: resp.AddressURI, Coord64: resp.AddressCoord64, KeptIndices: append([]int(nil), resp.KeptSentenceIndices...)}
	s.mu.Unlock()
	return nil
}

func (s *JSONFileStore) DeleteLocalUnits(unitIDs []string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	deleted := 0
	for _, id := range unitIDs {
		if _, ok := s.docs[id]; ok {
			delete(s.docs, id)
			deleted++
		}
	}
	return deleted
}

func (s *JSONFileStore) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := json.MarshalIndent(s.docs, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	dir := filepath.Dir(s.filePath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(s.filePath)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = tmp.Close(); _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(0o600); err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, s.filePath)
}

func (s *JSONFileStore) LoadLocalDocument(docID string) (string, string, []string, error) {
	s.mu.RLock()
	if parentID := s.subToParent[docID]; parentID != "" {
		docID = parentID
	}
	rec, ok := s.docs[docID]
	if !ok {
		s.mu.RUnlock()
		return "", "", nil, errors.New("ramesdk: document not found in JSON store: " + docID)
	}
	allSents, keptIdx := append([]string(nil), rec.Sentences...), append([]int(nil), rec.KeptIndices...)
	s.mu.RUnlock()
	return distillPlaintext(rec.RawText, allSents, keptIdx)
}

type MemoryStore struct {
	*BlindEncoder
	mu          sync.RWMutex
	raw, layer  map[string]string
	occ         map[string]int64
	sents       map[string][]string
	kept        map[string][]int
	noetics     map[string]LocalNoeticRecord
	subToParent map[string]string
}

func NewMemoryStore(secret []byte) *MemoryStore {
	return &MemoryStore{
		BlindEncoder: NewBlindEncoder(secret), raw: make(map[string]string), layer: make(map[string]string),
		occ: make(map[string]int64), sents: make(map[string][]string), kept: make(map[string][]int),
		noetics: make(map[string]LocalNoeticRecord), subToParent: make(map[string]string),
	}
}

func (m *MemoryStore) SaveSubUnitMapping(subUnitID, parentUnitID string) {
	if subUnitID, parentUnitID = strings.TrimSpace(subUnitID), strings.TrimSpace(parentUnitID); subUnitID == "" || parentUnitID == "" {
		return
	}
	m.mu.Lock()
	if m.subToParent == nil {
		m.subToParent = make(map[string]string)
	}
	m.subToParent[subUnitID] = parentUnitID
	m.mu.Unlock()
}

func (m *MemoryStore) ResolveCanonicalUnitID(unitID string) string {
	unitID = strings.TrimSpace(unitID)
	m.mu.RLock()
	p := m.subToParent[unitID]
	m.mu.RUnlock()
	if p != "" {
		return p
	}
	return unitID
}

func (m *MemoryStore) SaveLocalPlaintext(docID string, occurredAt int64, rawText string, sents []string, resp CloudIngestResponse) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.raw[docID] = rawText
	lyr := strings.ToLower(strings.TrimSpace(resp.Layer))
	if lyr != LayerBackground && lyr != LayerNoetic {
		lyr = LayerEpisodic
	}
	m.layer[docID], m.occ[docID] = lyr, occurredAt
	m.sents[docID] = append([]string(nil), sents...)
	m.kept[docID] = append([]int(nil), resp.KeptSentenceIndices...)
	return nil
}

func (m *MemoryStore) SaveLocalNoetic(rec LocalNoeticRecord) error {
	if strings.TrimSpace(rec.NoeticID) == "" {
		return errors.New("ramesdk: noetic_id is required")
	}
	m.mu.Lock()
	m.noetics[rec.NoeticID] = rec
	m.mu.Unlock()
	return nil
}

func (m *MemoryStore) GetLocalNoetic(noeticID string) (LocalNoeticRecord, bool) {
	m.mu.RLock()
	rec, ok := m.noetics[noeticID]
	m.mu.RUnlock()
	return rec, ok
}

func (m *MemoryStore) DeleteLocalUnits(unitIDs []string) int {
	target := make(map[string]bool, len(unitIDs))
	for _, id := range unitIDs {
		if id = strings.TrimSpace(id); id != "" {
			target[id] = true
		}
	}
	if len(target) == 0 {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	deleted := 0
	for id := range target {
		if _, ok := m.raw[id]; ok {
			delete(m.raw, id)
			delete(m.layer, id)
			delete(m.occ, id)
			delete(m.sents, id)
			delete(m.kept, id)
			deleted++
		}
		if _, ok := m.noetics[id]; ok {
			delete(m.noetics, id)
			deleted++
		}
	}
	for nid, rec := range m.noetics {
		for _, cid := range rec.CitedUnitIDs {
			if target[cid] {
				delete(m.noetics, nid)
				break
			}
		}
	}
	return deleted
}

func (m *MemoryStore) LoadLocalDocument(docID string) (string, string, []string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if parentID := m.subToParent[docID]; parentID != "" {
		docID = parentID
	}
	raw, ok := m.raw[docID]
	if !ok {
		if rec, nOK := m.noetics[docID]; nOK {
			combined := strings.TrimSpace("Q: " + rec.QueryText + "\nA: " + rec.AnswerText)
			return combined, combined, []string{combined}, nil
		}
		return "", "", nil, errors.New("ramesdk: document not found")
	}
	return distillPlaintext(raw, m.sents[docID], m.kept[docID])
}

type Client struct {
	Endpoint, APIToken, UserID, TenantID, Scope string
	// AssistantID is the answering persona (unlimited value space); IncarnationID is one
	// concrete lifetime instance of that persona. Both default from the environment
	// (ROLLING_MEMORY_ASSISTANT_ID / ROLLING_MEMORY_INCARNATION_ID) and are stamped onto
	// every blind pulse leaving this client, so any number of parallel incarnations can
	// each answer when / where / what without widening the core envelope.
	AssistantID, IncarnationID string
	// CrossIncarnation overrides the engine's sibling-incarnation resonance on recall.
	// nil keeps the engine default (同一 Assistant 跨分身本我共振开启); false locks recall
	// to the current incarnation only — evaluation/isolation callers must set false.
	CrossIncarnation *bool
	// Mode selects the client operating mode: ModeLocalPlain ("local_plain") | ModePulsePlain ("pulse_plain") | ModePulseCipher ("pulse_cipher").
	// Setting PlaintextMode = true is a shorthand for Mode = ModePulsePlain.
	Mode          string
	PlaintextMode bool
	Store         PlaintextStore
	HTTPClient    *http.Client
}

// SetPulseMode switches the client's operating pulse mode ("local_plain" | "pulse_plain" | "pulse_cipher")
// and returns an error if an unsupported mode is supplied.
func (c *Client) SetPulseMode(mode string) error {
	if c == nil {
		return errors.New("ramesdk: nil client")
	}
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", ModePulseCipher:
		c.Mode = ModePulseCipher
		c.PlaintextMode = false
		return nil
	case ModePulsePlain:
		c.Mode = ModePulsePlain
		c.PlaintextMode = true
		return nil
	case ModeLocalPlain:
		c.Mode = ModeLocalPlain
		c.PlaintextMode = false
		return nil
	default:
		return fmt.Errorf("ramesdk: invalid pulse_mode %q (must be %q, %q, or %q)", mode, ModePulseCipher, ModePulsePlain, ModeLocalPlain)
	}
}

// EffectiveCloudPulseMode returns the wire pulse_mode ("pulse_cipher" | "pulse_plain") sent to the cloud engine.
func (c *Client) EffectiveCloudPulseMode() string {
	if c.IsPlaintextMode() {
		return ModePulsePlain
	}
	return ModePulseCipher
}

// IsPlaintextMode reports whether the client is operating in Pulse Plain Mode ("pulse_plain").
func (c *Client) IsPlaintextMode() bool {
	if c == nil {
		return false
	}
	if c.PlaintextMode {
		return true
	}
	m := strings.ToLower(strings.TrimSpace(c.Mode))
	return m == ModePulsePlain
}

func isLocalPulseMode(mode string) bool {
	return strings.EqualFold(strings.TrimSpace(mode), ModeLocalPlain)
}

// envOrDefault returns the trimmed environment value, or fallback when unset.
func envOrDefault(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func NewClient(endpoint, apiToken, userID string, store PlaintextStore) *Client {
	cleanUserID := strings.TrimSpace(userID)
	mode := strings.TrimSpace(os.Getenv("ROLLING_MEMORY_PULSE_MODE"))
	if mode == "" {
		// Backward-compatible env name, but only canonical pulse values are accepted;
		// the config-mode values "local"/"cloud" are a different axis and must not
		// leak into the pulse-mode enum.
		if candidate := strings.TrimSpace(os.Getenv("ROLLING_MEMORY_MODE")); candidate == ModeLocalPlain || candidate == ModePulsePlain || candidate == ModePulseCipher {
			mode = candidate
		}
	}
	if mode != "" && mode != ModeLocalPlain && mode != ModePulsePlain && mode != ModePulseCipher {
		mode = ""
	}
	if mode == "" {
		if strings.TrimSpace(endpoint) == "" {
			mode = ModeLocalPlain
		} else {
			mode = ModePulseCipher
		}
	}
	return &Client{
		Endpoint: strings.TrimRight(strings.TrimSpace(endpoint), "/"), APIToken: strings.TrimSpace(apiToken),
		UserID: cleanUserID, TenantID: cleanUserID, Store: store, HTTPClient: &http.Client{Timeout: 15 * time.Second},
		AssistantID:   envOrDefault("ROLLING_MEMORY_ASSISTANT_ID", "default"),
		IncarnationID: strings.TrimSpace(os.Getenv("ROLLING_MEMORY_INCARNATION_ID")),
		Mode:          mode,
		PlaintextMode: strings.EqualFold(mode, ModePulsePlain),
	}
}

func (c *Client) Remember(ctx context.Context, docID, rawText string, occurredAt int64) (CloudIngestResponse, error) {
	return c.RememberWithLayer(ctx, docID, rawText, occurredAt, LayerEpisodic)
}
func (c *Client) RememberEpisodic(ctx context.Context, docID, rawText string, occurredAt int64) (CloudIngestResponse, error) {
	return c.RememberWithLayer(ctx, docID, rawText, occurredAt, LayerEpisodic)
}
func (c *Client) RememberBackground(ctx context.Context, docID, rawText string, occurredAt int64) (CloudIngestResponse, error) {
	return c.RememberWithLayer(ctx, docID, rawText, occurredAt, LayerBackground)
}

func (c *Client) IngestPacket(ctx context.Context, pkt BlindPulsePacket) (CloudIngestResponse, error) {
	if len(pkt.Slots) == 0 && len(pkt.SentenceFrames) == 0 && strings.TrimSpace(pkt.AtomsB64) == "" && strings.TrimSpace(pkt.RawText) == "" {
		return CloudIngestResponse{}, errors.New("ramesdk: empty pulse packet")
	}
	if strings.TrimSpace(pkt.Layer) == "" {
		pkt.Layer = LayerEpisodic
	}
	if pkt.TenantID == "" {
		pkt.TenantID = c.TenantID
	}
	if pkt.UserID == "" {
		pkt.UserID = c.UserID
	}
	if pkt.Scope == "" {
		pkt.Scope = c.Scope
	}
	if strings.TrimSpace(pkt.AssistantID) == "" {
		pkt.AssistantID = c.AssistantID
	}
	if strings.TrimSpace(pkt.IncarnationID) == "" {
		pkt.IncarnationID = c.IncarnationID
	}
	var wirePkt BlindPulsePacket
	if len(pkt.Slots) == 0 && len(pkt.SentenceFrames) == 0 && strings.TrimSpace(pkt.AtomsB64) == "" && strings.TrimSpace(pkt.RawText) != "" {
		wirePkt = pkt
	} else {
		var err error
		wirePkt, err = pkt.WirePacket()
		if err != nil {
			return CloudIngestResponse{}, err
		}
		if c.IsPlaintextMode() && strings.TrimSpace(pkt.RawText) != "" {
			wirePkt.RawText = pkt.RawText
		}
	}
	if strings.TrimSpace(wirePkt.PulseMode) == "" {
		wirePkt.PulseMode = c.EffectiveCloudPulseMode()
	}
	resp := CloudIngestResponse{UnitID: pkt.UnitID, Layer: pkt.Layer, PulseMode: wirePkt.PulseMode}
	if c.Endpoint != "" && !isLocalPulseMode(c.Mode) {
		if err := c.postJSON(ctx, "/ingest", wirePkt, &resp); err != nil {
			return CloudIngestResponse{}, err
		}
		if err := c.validateCloudSpace(resp.TenantID, resp.Scope); err != nil {
			return CloudIngestResponse{}, fmt.Errorf("ramesdk: ingest space validation: %w", err)
		}
		if resp.PulseMode != "" && !sameCloudPulseMode(resp.PulseMode, wirePkt.PulseMode) {
			return CloudIngestResponse{}, fmt.Errorf("ramesdk: ingest pulse_mode mismatch (server=%q client=%q)", resp.PulseMode, wirePkt.PulseMode)
		}
		if resp.Layer == "" {
			resp.Layer = pkt.Layer
		}
	}
	return resp, nil
}

func sameCloudPulseMode(a, b string) bool {
	na := normalizeWirePulseMode(a)
	nb := normalizeWirePulseMode(b)
	return na != "" && na == nb
}

func normalizeWirePulseMode(m string) string {
	switch strings.ToLower(strings.TrimSpace(m)) {
	case ModePulseCipher:
		return ModePulseCipher
	case ModePulsePlain:
		return ModePulsePlain
	default:
		return strings.ToLower(strings.TrimSpace(m))
	}
}

type keyedHasher interface {
	KeyedHash64(token string) uint64
}

func (c *Client) RememberWithLayer(ctx context.Context, docID, rawText string, occurredAt int64, layer string) (CloudIngestResponse, error) {
	if c == nil || c.Store == nil {
		return CloudIngestResponse{}, errors.New("ramesdk: nil local store")
	}
	pkt, sents := c.Store.PackBlindPacket(docID, rawText, occurredAt)
	if len(pkt.Slots) == 0 && strings.TrimSpace(pkt.AtomsB64) == "" {
		return CloudIngestResponse{}, errors.New("ramesdk: empty pulse packet")
	}
	if c.IsPlaintextMode() {
		pkt.RawText = rawText
	}
	if lyr := strings.TrimSpace(layer); lyr != "" {
		pkt.Layer = lyr
	} else {
		pkt.Layer = LayerEpisodic
	}
	resp, err := c.IngestPacket(ctx, pkt)
	if err != nil {
		return CloudIngestResponse{}, err
	}
	return resp, c.Store.SaveLocalPlaintext(docID, occurredAt, rawText, sents, resp)
}

func (c *Client) RememberWithMeta(ctx context.Context, docID, rawText, layer string, meta UnitMeta) (CloudIngestResponse, error) {
	if c == nil || c.Store == nil {
		return CloudIngestResponse{}, errors.New("ramesdk: nil local store")
	}
	fullText := FormatPlaintextWithMeta(rawText, meta)
	occ := meta.OccurredAt
	if occ <= 0 && meta.NarratedAt > 0 {
		occ = meta.NarratedAt
	}
	pkt, sents := c.Store.PackBlindPacket(docID, fullText, occ)
	if len(pkt.Slots) == 0 && strings.TrimSpace(pkt.AtomsB64) == "" {
		return CloudIngestResponse{}, errors.New("ramesdk: empty pulse packet")
	}
	if c.IsPlaintextMode() {
		pkt.RawText = fullText
	}
	if meta.NarratedAt > 0 {
		pkt.NarratedAt = meta.NarratedAt
	}
	if meta.IngestedAt > 0 {
		pkt.IngestedAt = meta.IngestedAt
	}
	kh, hasKH := c.Store.(keyedHasher)
	if author := strings.ToLower(strings.TrimSpace(meta.Author)); author != "" && hasKH {
		pkt.AuthorHash = kh.KeyedHash64(author)
	}
	pkt.DocHash = meta.DocHash
	if pkt.DocHash == 0 && hasKH {
		if fileURL := strings.TrimSpace(meta.FileURL); fileURL != "" {
			pkt.DocHash = kh.KeyedHash64(fileURL)
		}
	}
	pkt.BlockHash = meta.BlockHash
	if pkt.BlockHash == 0 && hasKH {
		if blockID := strings.TrimSpace(meta.BlockID); blockID != "" {
			pkt.BlockHash = kh.KeyedHash64(blockID)
		}
	}
	if meta.LineStart > 0 {
		pkt.LineStart = meta.LineStart
	}
	if meta.LineEnd > 0 {
		pkt.LineEnd = meta.LineEnd
	}
	if pkt.LineStart > 0 && pkt.LineEnd < pkt.LineStart {
		pkt.LineEnd = pkt.LineStart
	}
	if lyr := strings.TrimSpace(layer); lyr != "" {
		pkt.Layer = lyr
	} else {
		pkt.Layer = LayerEpisodic
	}
	resp, err := c.IngestPacket(ctx, pkt)
	if err != nil {
		return CloudIngestResponse{}, err
	}
	return resp, c.Store.SaveLocalPlaintext(docID, occ, fullText, sents, resp)
}

type SubUnitMapper interface {
	SaveSubUnitMapping(subUnitID, parentUnitID string)
	ResolveCanonicalUnitID(unitID string) string
}

func (c *Client) RememberComposite(ctx context.Context, docID, rawText string, occurredAt int64, layer string, subUnits []SubUnit) ([]CloudIngestResponse, error) {
	lyr := strings.TrimSpace(layer)
	if lyr == "" {
		lyr = LayerEpisodic
	}
	kh, hasKH := c.Store.(keyedHasher)
	var parentDocHash uint64
	if hasKH && strings.TrimSpace(docID) != "" {
		parentDocHash = kh.KeyedHash64(strings.TrimSpace(docID))
	}
	parentResp, err := c.RememberWithMeta(ctx, docID, rawText, lyr, UnitMeta{
		OccurredAt: occurredAt,
		DocHash:    parentDocHash,
	})
	if err != nil {
		return nil, err
	}
	resps := []CloudIngestResponse{parentResp}
	mapper, _ := c.Store.(SubUnitMapper)
	kindCounts := make(map[string]int, 4)
	for _, su := range subUnits {
		subID, _, txt, _, ok := resolveSubUnitIDAndText(docID, su, kindCounts)
		if !ok {
			continue
		}
		subPkt, _ := c.Store.PackBlindPacket(subID, txt, occurredAt)
		if len(subPkt.Slots) == 0 {
			continue
		}
		subPkt.Layer, subPkt.CitedUnitIDs = lyr, []string{docID}
		subPkt.DocHash = parentDocHash
		if hasKH {
			if blkID := strings.TrimSpace(su.BlockID); blkID != "" {
				subPkt.BlockHash = kh.KeyedHash64(blkID)
			} else if subID != "" {
				subPkt.BlockHash = kh.KeyedHash64(subID)
			}
		}
		if su.LineStart > 0 {
			subPkt.LineStart = su.LineStart
		}
		if su.LineEnd > 0 {
			subPkt.LineEnd = su.LineEnd
		}
		if subPkt.LineStart > 0 && subPkt.LineEnd < subPkt.LineStart {
			subPkt.LineEnd = subPkt.LineStart
		}
		subResp, sErr := c.IngestPacket(ctx, subPkt)
		if sErr != nil {
			return resps, sErr
		}
		if mapper != nil {
			mapper.SaveSubUnitMapping(subID, docID)
		}
		resps = append(resps, subResp)
	}
	return resps, nil
}

type LocalNoeticSaver interface {
	SaveLocalNoetic(rec LocalNoeticRecord) error
}

func (c *Client) RememberNoetic(noeticID, queryText string, citedUnitIDs []string, answerText string, occurredAt int64) error {
	ns, ok := c.Store.(LocalNoeticSaver)
	if !ok {
		return errors.New("ramesdk: Store does not implement LocalNoeticSaver")
	}
	return ns.SaveLocalNoetic(LocalNoeticRecord{
		NoeticID: noeticID, OccurredAt: occurredAt, QueryText: queryText, AnswerText: answerText,
		CitedUnitIDs: citedUnitIDs, AssistantID: c.AssistantID, IncarnationID: c.IncarnationID,
	})
}

type ResolvedHit struct {
	CloudQueryHit
	RawPlaintext       string   `json:"raw_plaintext"`
	DistilledPlaintext string   `json:"distilled_plaintext"`
	KeptSentences      []string `json:"kept_sentences"`
}

func (c *Client) QueryHits(ctx context.Context, queryText string, topK int) ([]CloudQueryHit, error) {
	if c == nil || c.Store == nil {
		return nil, errors.New("ramesdk: nil local store")
	}
	if topK < 0 {
		topK = 0
	}
	if strings.TrimSpace(c.Endpoint) == "" {
		return nil, errors.New("ramesdk: QueryHits requires a cloud Endpoint")
	}
	qPkt, _ := c.Store.PackBlindPacket("QUERY", queryText, 0)
	if len(qPkt.Slots) == 0 && strings.TrimSpace(qPkt.AtomsB64) == "" {
		return nil, errors.New("ramesdk: empty query pulse packet")
	}
	wireQuery, err := qPkt.WirePacket()
	if err != nil {
		return nil, err
	}
	pulseMode := c.EffectiveCloudPulseMode()
	wireQuery.PulseMode = pulseMode
	if c.IsPlaintextMode() {
		wireQuery.RawText = queryText
	}
	payload := map[string]any{
		"tenant_id":  c.TenantID,
		"user_id":    c.UserID,
		"scope":      c.Scope,
		"pulse_mode": pulseMode,
		"query":      wireQuery,
		"top_k":      topK,
	}
	if topK == 0 {
		payload["delivery_budget"] = "auto"
	}
	if c.IsPlaintextMode() {
		payload["query_text"] = queryText
	}
	// CrossIncarnation 由调用方显式设置时才下发（nil = 引擎默认：同 Assistant 跨分身本我共振）。
	// 反馈/评测类需要锁死单一分身空间的调用方设 false（引擎侧 cross_incarnation=false = 仅当前分身）。
	if c.CrossIncarnation != nil {
		payload["cross_incarnation"] = *c.CrossIncarnation
	}
	var rawEnv struct {
		Success   bool            `json:"success"`
		TenantID  string          `json:"tenant_id"`
		Scope     string          `json:"scope"`
		PulseMode string          `json:"pulse_mode"`
		Engine    string          `json:"engine"`
		Hits      []CloudQueryHit `json:"hits"`
		Count     int             `json:"count"`
	}
	if err := c.postJSON(ctx, "/query?envelope=1", payload, &rawEnv); err != nil {
		return nil, err
	}
	if !rawEnv.Success || rawEnv.Engine != "autonoetic" {
		return nil, fmt.Errorf("ramesdk: invalid query envelope (success=%v engine=%q)", rawEnv.Success, rawEnv.Engine)
	}
	if err := c.validateCloudSpace(rawEnv.TenantID, rawEnv.Scope); err != nil {
		return nil, fmt.Errorf("ramesdk: query space validation: %w", err)
	}
	if rawEnv.PulseMode != "" && !sameCloudPulseMode(rawEnv.PulseMode, pulseMode) {
		return nil, fmt.Errorf("ramesdk: query pulse_mode mismatch (server=%q client=%q)", rawEnv.PulseMode, pulseMode)
	}
	if rawEnv.Count != len(rawEnv.Hits) {
		return nil, fmt.Errorf("ramesdk: cloud query count %d does not match %d hits", rawEnv.Count, len(rawEnv.Hits))
	}
	for _, h := range rawEnv.Hits {
		if strings.TrimSpace(h.UnitID) == "" {
			return nil, errors.New("ramesdk: cloud query returned an empty UnitID")
		}
	}
	return rawEnv.Hits, nil
}

func (c *Client) Recall(ctx context.Context, queryText string, topK int) ([]ResolvedHit, error) {
	hits, err := c.QueryHits(ctx, queryText, topK)
	if err != nil {
		return nil, err
	}
	out := make([]ResolvedHit, 0, len(hits))
	seenCanonical := make(map[string]bool, len(hits))
	mapper, _ := c.Store.(SubUnitMapper)
	for _, h := range hits {
		lookupID := h.UnitID
		if mapper != nil {
			lookupID = mapper.ResolveCanonicalUnitID(lookupID)
		}
		if lookupID == h.UnitID && len(h.CitedUnitIDs) == 1 && strings.TrimSpace(h.CitedUnitIDs[0]) != "" && h.Layer != LayerNoetic {
			lookupID = strings.TrimSpace(h.CitedUnitIDs[0])
		}
		if seenCanonical[lookupID] {
			continue
		}
		var raw, dist string
		var kept []string
		var loadErr error
		if c.IsPlaintextMode() && strings.TrimSpace(h.RawText) != "" {
			raw = h.RawText
			dist = h.DistilledText
			if strings.TrimSpace(dist) == "" {
				dist = raw
			}
			kept = SplitSentences(dist)
		} else {
			if c.Store != nil {
				raw, dist, kept, loadErr = c.Store.LoadLocalDocument(lookupID)
			} else {
				loadErr = errors.New("ramesdk: nil local store")
			}
			if loadErr != nil {
				if strings.TrimSpace(h.RawText) != "" {
					raw = h.RawText
					dist = h.DistilledText
					if strings.TrimSpace(dist) == "" {
						dist = raw
					}
					kept = SplitSentences(dist)
					loadErr = nil
				} else {
					return nil, fmt.Errorf("ramesdk: resolve cloud query UnitID %q: %w", h.UnitID, loadErr)
				}
			}
		}
		seenCanonical[lookupID] = true
		h.UnitID = lookupID
		out = append(out, ResolvedHit{CloudQueryHit: h, RawPlaintext: raw, DistilledPlaintext: dist, KeptSentences: kept})
	}
	return out, nil
}

func (c *Client) validateCloudSpace(tenantID, scope string) error {
	if strings.TrimSpace(tenantID) == "" || tenantID != c.TenantID || !c.matchesCloudScope(scope) {
		return fmt.Errorf("space mismatch (got tenant=%q scope=%q, want tenant=%q scope=%q)", tenantID, scope, c.TenantID, c.Scope)
	}
	return nil
}

func isSharedUserScope(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	return s == "@user" || s == "shared" || s == "user_shared"
}

func composeClientCanonicalScope(tenantID, baseScope, assistantID, incarnationID string) string {
	ast := strings.TrimSpace(assistantID)
	inc := strings.TrimSpace(incarnationID)
	base := strings.TrimSpace(baseScope)
	if isSharedUserScope(ast) || (ast == "" && inc == "" && isSharedUserScope(base)) {
		return "@user"
	}
	if strings.HasPrefix(base, "ast:") {
		rest := strings.TrimPrefix(base, "ast:")
		var pAst, pInc string
		if idx := strings.Index(rest, ":inc:"); idx >= 0 {
			pAst, pInc = rest[:idx], rest[idx+len(":inc:"):]
		} else {
			pAst = rest
		}
		if ast == "" {
			ast = pAst
		}
		if inc == "" {
			inc = pInc
		}
	}
	if ast == "" && inc == "" {
		return base
	}
	if ast == "" {
		ast = "default"
	}
	if inc == "" && base != "" && base != "default" && base != strings.TrimSpace(tenantID) && !strings.HasPrefix(base, "ast:") {
		inc = base
	}
	if inc == "" {
		return "ast:" + ast
	}
	return "ast:" + ast + ":inc:" + inc
}

// matchesCloudScope 校验引擎回显的 scope。引擎按 ComposeAssistantIncarnationScope 将请求
// (tenant_id, scope, assistant_id, incarnation_id) 归一为分身坐标规范形回显。
func (c *Client) matchesCloudScope(scope string) bool {
	if scope == "" || scope != strings.TrimSpace(scope) {
		return false
	}
	want := strings.TrimSpace(c.Scope)
	if want != "" {
		if scope == want {
			return true
		}
		if scope == composeClientCanonicalScope(c.TenantID, want, c.AssistantID, c.IncarnationID) {
			return true
		}
		assistant := strings.TrimSpace(c.AssistantID)
		if assistant == "" {
			assistant = "default"
		}
		return strings.TrimSpace(c.IncarnationID) == "" && scope == "ast:"+assistant+":inc:"+want
	}
	// 当 c.Scope 为空时，服务端 NormalizeTenantAndScope 将 baseScope 归一为 UserID（或 TenantID / "default"）后再合成。
	candidates := []string{strings.TrimSpace(c.UserID), strings.TrimSpace(c.TenantID), "default"}
	for _, base := range candidates {
		if base == "" {
			continue
		}
		if scope == base || scope == composeClientCanonicalScope(c.TenantID, base, c.AssistantID, c.IncarnationID) {
			return true
		}
	}
	return false
}

// CodecHealthReport holds the local + remote codec verification result from `/health`.
type CodecHealthReport struct {
	LocalVerified  bool   `json:"local_verified"`
	RemoteCanaryOK bool   `json:"remote_canary_ok"`
	RemoteProbeOK  bool   `json:"remote_probe_ok"`
	SpecVersion    string `json:"spec_version"`
	SlotCount      int    `json:"slot_count"`
	SentCount      int    `json:"sent_count"`
	AtomsB64       string `json:"atoms_b64"`
}

// VerifyCodecLocal executes an in-process round-trip self-test of all 5 RAME-BlindPulse-v1 stages
// (KeyedHash64 -> SplitSentences -> SplitWords -> CarrierUnits/Flags -> RMBP AtomsB64 encode & decode)
// against the pinned canary vector without any network I/O.
func VerifyCodecLocal() error {
	enc := NewBlindEncoder([]byte(CanarySecret))
	pkt, sents := enc.PackBlindPacket(CanaryDocID, CanaryText, CanaryTS)
	if len(sents) != CanarySentCount || len(pkt.Slots) != CanarySlotCount {
		return fmt.Errorf("ramesdk: local codec canary count mismatch (got slots=%d sents=%d, want slots=%d sents=%d)",
			len(pkt.Slots), len(sents), CanarySlotCount, CanarySentCount)
	}
	if pkt.AtomsB64 != CanaryAtomsB64 {
		return fmt.Errorf("ramesdk: local codec canary atoms_b64 drift (got %q, want %q)", pkt.AtomsB64, CanaryAtomsB64)
	}
	decSlots, decFrames, err := DecodeWirePayload(pkt.AtomsB64)
	if err != nil {
		return fmt.Errorf("ramesdk: local codec canary decode failed: %w", err)
	}
	if len(decSlots) != len(pkt.Slots) || len(decFrames) != len(pkt.SentenceFrames) {
		return fmt.Errorf("ramesdk: local codec canary round-trip size mismatch")
	}
	for i := range pkt.Slots {
		if decSlots[i] != pkt.Slots[i] {
			return fmt.Errorf("ramesdk: local codec canary slot[%d] mismatch: got %+v want %+v", i, decSlots[i], pkt.Slots[i])
		}
	}
	return nil
}

type healthWireResponse struct {
	Status      string `json:"status"`
	Engine      string `json:"engine"`
	Version     string `json:"version"`
	SpecVersion string `json:"spec_version"`
	CodecCanary *struct {
		Spec      string `json:"spec"`
		SlotCount int    `json:"slot_count"`
		SentCount int    `json:"sent_count"`
		AtomsB64  string `json:"atoms_b64"`
	} `json:"codec_canary"`
	CodecProbe *struct {
		Verified  bool   `json:"verified"`
		SlotCount int    `json:"slot_count"`
		SentCount int    `json:"sent_count"`
		AtomsB64  string `json:"atoms_b64"`
	} `json:"codec_probe"`
}

func (c *Client) Health(ctx context.Context) error {
	if err := VerifyCodecLocal(); err != nil {
		return err
	}
	if strings.TrimSpace(c.Endpoint) == "" {
		return errors.New("ramesdk: Health requires a cloud Endpoint")
	}
	var resp healthWireResponse
	if err := c.getJSON(ctx, "/health", &resp); err != nil {
		return err
	}
	if resp.Status != "ok" || resp.Engine != "autonoetic" || resp.Version != "v1" {
		return fmt.Errorf("ramesdk: unhealthy response status=%q engine=%q version=%q", resp.Status, resp.Engine, resp.Version)
	}
	if resp.CodecCanary != nil && resp.CodecCanary.AtomsB64 != "" {
		if resp.CodecCanary.AtomsB64 != CanaryAtomsB64 || resp.CodecCanary.SlotCount != CanarySlotCount {
			return fmt.Errorf("ramesdk: remote codec canary mismatch (server_b64=%q slots=%d, local_b64=%q slots=%d)",
				resp.CodecCanary.AtomsB64, resp.CodecCanary.SlotCount, CanaryAtomsB64, CanarySlotCount)
		}
	}
	return nil
}

// VerifyCodec runs a full bidirectional codec self-test: first verifying local encode/decode
// against the built-in canary, then sending the encoded `atoms_b64` probe to `/health` so the
// remote engine decodes and re-encodes it live.
func (c *Client) VerifyCodec(ctx context.Context) (CodecHealthReport, error) {
	if err := VerifyCodecLocal(); err != nil {
		return CodecHealthReport{}, err
	}
	report := CodecHealthReport{
		LocalVerified: true,
		SpecVersion:   SpecVersion,
		SlotCount:     CanarySlotCount,
		SentCount:     CanarySentCount,
		AtomsB64:      CanaryAtomsB64,
	}
	if strings.TrimSpace(c.Endpoint) == "" {
		return report, nil
	}
	var resp healthWireResponse
	err := c.postJSON(ctx, "/health", map[string]any{"atoms_b64": CanaryAtomsB64}, &resp)
	if err != nil {
		// Fallback to GET /health?atoms_b64=... for GET-only servers
		err = c.getJSON(ctx, "/health?atoms_b64="+url.QueryEscape(CanaryAtomsB64), &resp)
		if err != nil {
			return report, err
		}
	}
	if resp.Status != "ok" || resp.Engine != "autonoetic" || resp.Version != "v1" {
		return report, fmt.Errorf("ramesdk: unhealthy response status=%q engine=%q version=%q", resp.Status, resp.Engine, resp.Version)
	}
	if resp.CodecCanary != nil && resp.CodecCanary.AtomsB64 != "" {
		if resp.CodecCanary.AtomsB64 != CanaryAtomsB64 || resp.CodecCanary.SlotCount != CanarySlotCount {
			return report, fmt.Errorf("ramesdk: remote codec canary mismatch (server=%q, local=%q)", resp.CodecCanary.AtomsB64, CanaryAtomsB64)
		}
		report.RemoteCanaryOK = true
	}
	if resp.CodecProbe != nil {
		if !resp.CodecProbe.Verified || resp.CodecProbe.AtomsB64 != CanaryAtomsB64 || resp.CodecProbe.SlotCount != CanarySlotCount {
			return report, fmt.Errorf("ramesdk: remote codec probe verification failed: %+v", resp.CodecProbe)
		}
		report.RemoteProbeOK = true
	}
	return report, nil
}

type AuthIdentity struct {
	Success        bool     `json:"success"`
	Valid          bool     `json:"valid"`
	Engine         string   `json:"engine"`
	TenantID       string   `json:"tenant_id"`
	UserIDPrefixes []string `json:"user_id_prefixes"`
	ExactNamespace bool     `json:"exact_namespace"`
	LockedUserID   string   `json:"locked_user_id"`
	IsManager      bool     `json:"is_manager"`
}

func (c *Client) VerifyAuth(ctx context.Context) (AuthIdentity, error) {
	if strings.TrimSpace(c.Endpoint) == "" {
		return AuthIdentity{}, errors.New("ramesdk: VerifyAuth requires a cloud Endpoint")
	}
	var identity AuthIdentity
	if err := c.getJSON(ctx, "/auth/verify", &identity); err != nil {
		return AuthIdentity{}, err
	}
	if !identity.Success || !identity.Valid || identity.Engine != "autonoetic" {
		return AuthIdentity{}, fmt.Errorf("ramesdk: invalid auth verify response (success=%v valid=%v engine=%q)", identity.Success, identity.Valid, identity.Engine)
	}
	return identity, nil
}

func (c *Client) ListCloudUnitIDs(ctx context.Context) ([]string, error) {
	if strings.TrimSpace(c.Endpoint) == "" {
		return nil, errors.New("ramesdk: ListCloudUnitIDs requires a cloud Endpoint")
	}
	payload := map[string]any{"tenant_id": c.TenantID, "user_id": c.UserID, "scope": c.Scope}
	var list struct {
		Success  bool     `json:"success"`
		TenantID string   `json:"tenant_id"`
		Scope    string   `json:"scope"`
		Count    int      `json:"count"`
		UnitIDs  []string `json:"unit_ids"`
	}
	if err := c.postJSON(ctx, "/units", payload, &list); err != nil {
		return nil, err
	}
	if !list.Success {
		return nil, errors.New("ramesdk: cloud unit list returned success=false")
	}
	if err := c.validateCloudSpace(list.TenantID, list.Scope); err != nil {
		return nil, fmt.Errorf("ramesdk: unit list space validation: %w", err)
	}
	if list.Count != len(list.UnitIDs) {
		return nil, fmt.Errorf("ramesdk: cloud unit list count %d does not match %d IDs", list.Count, len(list.UnitIDs))
	}
	ids, seen := make([]string, 0, len(list.UnitIDs)), make(map[string]bool, len(list.UnitIDs))
	for _, id := range list.UnitIDs {
		if id = strings.TrimSpace(id); id == "" || seen[id] {
			return nil, fmt.Errorf("ramesdk: cloud unit list returned an empty or duplicate unit ID %q", id)
		}
		seen[id] = true
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}

type CloudPurgeResponse struct {
	Success        bool   `json:"success"`
	TenantID       string `json:"tenant_id,omitempty"`
	Scope          string `json:"scope,omitempty"`
	Mode           string `json:"mode"`
	DeletedUnits   int64  `json:"deleted_units"`
	RemainingUnits int64  `json:"remaining_units"`
}

type LocalUnitDeleter interface{ DeleteLocalUnits(unitIDs []string) int }
type localStoreFlusher interface{ Flush() error }

func deleteAndFlushLocal(store PlaintextStore, unitIDs []string) (int, error) {
	deleted := 0
	if ud, ok := store.(LocalUnitDeleter); ok {
		deleted = ud.DeleteLocalUnits(unitIDs)
	}
	if deleted > 0 {
		if flusher, ok := store.(localStoreFlusher); ok {
			if err := flusher.Flush(); err != nil {
				return deleted, err
			}
		}
	}
	return deleted, nil
}

func (c *Client) UpdateUnit(ctx context.Context, docID, newText string, occurredAt int64, layer string) (CloudIngestResponse, error) {
	if ud, ok := c.Store.(LocalUnitDeleter); ok {
		ud.DeleteLocalUnits([]string{docID})
	}
	return c.RememberWithLayer(ctx, docID, newText, occurredAt, layer)
}

func (c *Client) DeleteUnits(ctx context.Context, unitIDs []string) (CloudPurgeResponse, error) {
	var clean []string
	for _, id := range unitIDs {
		if id = strings.TrimSpace(id); id != "" {
			clean = append(clean, id)
		}
	}
	if len(clean) == 0 {
		return CloudPurgeResponse{}, errors.New("ramesdk: unit_ids must not be empty")
	}
	if c.Endpoint == "" {
		localDeleted, err := deleteAndFlushLocal(c.Store, clean)
		if err != nil {
			return CloudPurgeResponse{}, fmt.Errorf("ramesdk: flush local purge: %w", err)
		}
		return CloudPurgeResponse{Success: true, Mode: "units", DeletedUnits: int64(localDeleted)}, nil
	}
	payload := map[string]any{"tenant_id": c.TenantID, "user_id": c.UserID, "scope": c.Scope, "mode": "units", "unit_ids": clean}
	var resp CloudPurgeResponse
	if err := c.postJSON(ctx, "/purge", payload, &resp); err != nil {
		return CloudPurgeResponse{}, err
	}
	if !resp.Success {
		return CloudPurgeResponse{}, errors.New("ramesdk: cloud purge returned success=false")
	}
	if err := c.validateCloudSpace(resp.TenantID, resp.Scope); err != nil {
		return CloudPurgeResponse{}, fmt.Errorf("ramesdk: purge space validation: %w", err)
	}
	if _, err := deleteAndFlushLocal(c.Store, clean); err != nil {
		return resp, fmt.Errorf("ramesdk: flush local purge after cloud success: %w", err)
	}
	return resp, nil
}

func (c *Client) postJSON(ctx context.Context, subPath string, reqBody any, respOut any) error {
	return c.doJSON(ctx, http.MethodPost, subPath, reqBody, respOut)
}
func (c *Client) getJSON(ctx context.Context, subPath string, respOut any) error {
	return c.doJSON(ctx, http.MethodGet, subPath, nil, respOut)
}

func (c *Client) doJSON(ctx context.Context, method, subPath string, reqBody any, respOut any) error {
	var reqReader io.Reader
	if reqBody != nil {
		rawReq, err := json.Marshal(reqBody)
		if err != nil {
			return err
		}
		reqReader = bytes.NewReader(rawReq)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Endpoint+subPath, reqReader)
	if err != nil {
		return err
	}
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.APIToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIToken)
		req.Header.Set("X-Api-Key", c.APIToken)
	}
	if c.UserID != "" {
		req.Header.Set("X-Rolling-Memory-User", c.UserID)
	}
	if c.TenantID != "" {
		req.Header.Set("X-Rolling-Memory-Tenant", c.TenantID)
	}
	if c.Scope != "" {
		req.Header.Set("X-Rolling-Memory-Scope", c.Scope)
	}
	if c.AssistantID != "" {
		req.Header.Set("X-Rolling-Memory-Assistant", c.AssistantID)
	}
	if c.IncarnationID != "" {
		req.Header.Set("X-Rolling-Memory-Incarnation", c.IncarnationID)
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable {
			// 限流/暂不可用：把服务端的 Retry-After 带进错误，客户端据此精确退避而不是猜速率
			// （限流裁决与预算都在服务端，客户端只做反应式刹车）。
			return &ThrottleError{
				StatusCode: resp.StatusCode, Path: subPath,
				Body: strings.TrimSpace(string(bodyBytes)), RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After")),
			}
		}
		return fmt.Errorf("ramesdk: HTTP %d from %s: %s", resp.StatusCode, subPath, strings.TrimSpace(string(bodyBytes)))
	}
	if respOut != nil && len(bodyBytes) > 0 {
		return json.Unmarshal(bodyBytes, respOut)
	}
	return nil
}

// ThrottleError 是服务端限流（429）或暂不可用（503）的响应错误。
// RetryAfter 为服务端声明的等待时长（0 = 未声明）；错误文本与历史格式保持一致，便于既有断言与日志解析。
type ThrottleError struct {
	StatusCode int
	Path       string
	Body       string
	RetryAfter time.Duration
}

func (e *ThrottleError) Error() string {
	message := fmt.Sprintf("ramesdk: HTTP %d from %s: %s", e.StatusCode, e.Path, e.Body)
	if e.RetryAfter > 0 {
		message += fmt.Sprintf(" (retry after %s)", e.RetryAfter)
	}
	return message
}

// parseRetryAfter 解析 Retry-After（秒；HTTP-date 形式不支持时返回 0，由调用方退避兜底）。
func parseRetryAfter(raw string) time.Duration {
	seconds, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || seconds <= 0 {
		return 0
	}
	if seconds > 300 {
		seconds = 300
	}
	return time.Duration(seconds) * time.Second
}

// ============================================================================
// Canonical RAME-BlindPulse-v1 Binary Wire Codec ("RMBP" v1 · Mixed-Radix Golomb-Rice)
// ============================================================================

const (
	wireMagic                   = "RMBP"
	wireVersion                 = 1
	wireMaxF                    = 0xFF
	wireHeaderBytes             = 16
	wireMaxDictN                = 65535
	wireMaxSlotN                = 1 << 20
	wireMaxSentN                = 65535
	wireMaxPalN                 = 255
	wireFlagIdentityIndex uint8 = 1 << 0
	wireFlagWideF         uint8 = 1 << 1
	wireMaxFlag                 = wireFlagIdentityIndex | wireFlagWideF
)

type wireBF struct {
	B uint16
	F uint8
}

func wireAtomLess(a, b BlindTokenAtom) bool {
	if a.H != b.H {
		return a.H < b.H
	}
	if a.RawBitsX10 != b.RawBitsX10 {
		return a.RawBitsX10 < b.RawBitsX10
	}
	return a.ClassFlags < b.ClassFlags
}

func wireGolombKForDict(dictN int) int {
	if dictN <= 0 {
		return 0
	}
	if dictN == 1 {
		return 64
	}
	lg := bits.Len(uint(dictN)) - 1
	k := 64 - lg
	if k < 48 {
		return 48
	}
	if k > 64 {
		return 64
	}
	return k
}

func wireBitsForDict(dictN int) int {
	if dictN <= 1 {
		return 0
	}
	return bits.Len(uint(dictN - 1))
}

// NormalizeWire expands `AtomsB64` into `Slots` and `SentenceFrames` idempotently.
func (p *BlindPulsePacket) NormalizeWire() error {
	if p == nil || p.AtomsB64 == "" {
		return nil
	}
	slots, frames, err := DecodeWirePayload(p.AtomsB64)
	if err != nil {
		return err
	}
	p.Slots = slots
	p.SentenceFrames = frames
	p.AtomsB64 = ""
	return nil
}

// WirePacket returns a wire-ready copy of p carrying the canonical `atoms_b64` payload
// and omitting the redundant `slots` and `sents` JSON arrays.
func (p BlindPulsePacket) WirePacket() (BlindPulsePacket, error) {
	out := p
	if strings.TrimSpace(out.AtomsB64) == "" {
		slots := out.Slots
		if len(slots) == 0 && len(out.SentenceFrames) > 0 {
			for _, fr := range out.SentenceFrames {
				slots = append(slots, fr...)
			}
		}
		b64, err := EncodeWirePayload(slots, out.SentenceFrames)
		if err != nil {
			return BlindPulsePacket{}, err
		}
		out.AtomsB64 = b64
	}
	out.Slots = nil
	out.SentenceFrames = nil
	return out, nil
}

// EncodeWirePayload encodes flat slots and sentence frames into the canonical `atoms_b64` payload.
func EncodeWirePayload(slots []BlindTokenAtom, frames [][]BlindTokenAtom) (string, error) {
	raw, err := EncodeWireBytes(slots, frames)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(raw), nil
}

// DecodeWirePayload decodes a canonical `atoms_b64` payload back into flat slots and sentence frames.
func DecodeWirePayload(b64 string) ([]BlindTokenAtom, [][]BlindTokenAtom, error) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, nil, fmt.Errorf("ramesdk: decode wire base64: %w", err)
	}
	return DecodeWireBytes(raw)
}

// EncodeWireBytes encodes flat slots and sentence frames into the compact lossless `RMBP` binary stream.
func EncodeWireBytes(slots []BlindTokenAtom, frames [][]BlindTokenAtom) ([]byte, error) {
	sent := make([]int, 0, len(frames))
	if len(frames) > 0 {
		cum := 0
		for _, fr := range frames {
			cum += len(fr)
			sent = append(sent, cum)
		}
		if len(slots) == 0 {
			slots = make([]BlindTokenAtom, 0, cum)
			for _, fr := range frames {
				slots = append(slots, fr...)
			}
		} else if cum != len(slots) {
			return nil, fmt.Errorf("ramesdk: sentence frames hold %d atoms, slots hold %d", cum, len(slots))
		}
	}
	if len(slots) > wireMaxSlotN {
		return nil, fmt.Errorf("ramesdk: slotN=%d exceeds capacity", len(slots))
	}
	if len(sent) > wireMaxSentN {
		return nil, fmt.Errorf("ramesdk: sentN=%d exceeds capacity", len(sent))
	}
	for i, a := range slots {
		if a.ClassFlags > wireMaxF {
			return nil, fmt.Errorf("ramesdk: atom[%d] flag %#x out of 8-bit range (0x00..0xFF)", i, a.ClassFlags)
		}
	}
	if err := checkWireOffsets(sent, len(slots)); err != nil {
		return nil, err
	}

	// 去重：改为显式排序后去重，防止 map 内存布局造成的语义相同但 Key 不同问题
	all := make([]BlindTokenAtom, len(slots))
	copy(all, slots)
	sort.Slice(all, func(i, j int) bool { return wireAtomLess(all[i], all[j]) })

	dict := make([]BlindTokenAtom, 0, len(slots))
	if len(all) > 0 {
		dict = append(dict, all[0])
		for i := 1; i < len(all); i++ {
			if all[i] != all[i-1] {
				dict = append(dict, all[i])
			}
		}
	}
	if len(dict) >= wireMaxDictN {
		return nil, fmt.Errorf("ramesdk: dictN exceeds %d", wireMaxDictN)
	}

	dictPos := make(map[BlindTokenAtom]int, len(dict))
	for i, a := range dict {
		dictPos[a] = i
	}
	index := make([]int, len(slots))
	identity := len(dict) == len(slots)
	for i, a := range slots {
		p := dictPos[a]
		index[i] = p
		if p != i {
			identity = false
		}
	}

	grK := wireGolombKForDict(len(dict))

	var palette []wireBF
	palPos := make(map[wireBF]int, 32)
	for _, a := range dict {
		bf := wireBF{B: a.RawBitsX10, F: a.ClassFlags}
		if _, ok := palPos[bf]; !ok {
			palPos[bf] = len(palette)
			palette = append(palette, bf)
		}
	}
	usePalette := len(dict) > 0 && len(palette) <= wireMaxPalN
	palN := 0
	if usePalette {
		palN = len(palette)
		sort.Slice(palette, func(i, j int) bool {
			if palette[i].B != palette[j].B {
				return palette[i].B < palette[j].B
			}
			return palette[i].F < palette[j].F
		})
		for i, bf := range palette {
			palPos[bf] = i
		}
	}

	var flags uint8
	if identity {
		flags |= wireFlagIdentityIndex
	}
	wideF := false
	for _, a := range dict {
		if a.ClassFlags > 0x0F {
			wideF = true
			break
		}
	}
	if wideF {
		flags |= wireFlagWideF
	}

	out := make([]byte, 0, wireHeaderBytes+len(dict)*8)
	out = append(out, wireMagic...)
	out = append(out, wireVersion, flags, uint8(grK), uint8(palN))
	out = binary.BigEndian.AppendUint16(out, uint16(len(dict)))
	out = binary.BigEndian.AppendUint32(out, uint32(len(slots)))
	out = binary.BigEndian.AppendUint16(out, uint16(len(sent)))

	if usePalette {
		for _, bf := range palette {
			out = appendWireVarint(out, bf.B)
		}
		if wideF {
			for _, bf := range palette {
				out = append(out, bf.F)
			}
		} else {
			fBytes := (palN + 1) / 2
			fCol := make([]byte, fBytes)
			for i, bf := range palette {
				if i%2 == 0 {
					fCol[i/2] = bf.F
				} else {
					fCol[i/2] |= bf.F << 4
				}
			}
			out = append(out, fCol...)
		}
	} else if len(dict) > 0 {
		for _, a := range dict {
			out = appendWireVarint(out, a.RawBitsX10)
		}
		if wideF {
			for _, a := range dict {
				out = append(out, a.ClassFlags)
			}
		} else {
			fBytes := (len(dict) + 1) / 2
			fCol := make([]byte, fBytes)
			for i, a := range dict {
				if i%2 == 0 {
					fCol[i/2] = a.ClassFlags
				} else {
					fCol[i/2] |= a.ClassFlags << 4
				}
			}
			out = append(out, fCol...)
		}
	}

	bw := &wireBitWriter{buf: make([]byte, 0, len(dict)*8+len(slots))}
	var prevH uint64
	for i, a := range dict {
		var delta uint64
		if i == 0 {
			delta = a.H
		} else {
			delta = a.H - prevH
		}
		prevH = a.H
		if grK == 64 {
			bw.write(delta, 64)
		} else {
			q := delta >> uint(grK)
			rem := delta & ((uint64(1) << uint(grK)) - 1)
			for u := uint64(0); u < q; u++ {
				bw.write(1, 1)
			}
			bw.write(0, 1)
			bw.write(rem, grK)
		}
	}

	if usePalette {
		palBits := wireBitsForDict(palN)
		if palBits > 0 {
			for _, a := range dict {
				bw.write(uint64(palPos[wireBF{B: a.RawBitsX10, F: a.ClassFlags}]), palBits)
			}
		}
	}

	if !identity && len(dict) > 1 {
		seenPos := make([]int, len(dict))
		for i := range seenPos {
			seenPos[i] = -1
		}
		remaining := make([]int, len(dict))
		for i := range remaining {
			remaining[i] = i
		}
		seenCount := 0
		for _, dIdx := range index {
			if seenCount == 0 {
				rPos := sort.SearchInts(remaining, dIdx)
				remBits := wireBitsForDict(len(remaining))
				bw.write(uint64(rPos), remBits)
				remaining = append(remaining[:rPos], remaining[rPos+1:]...)
				seenPos[dIdx] = seenCount
				seenCount++
				continue
			}
			if sp := seenPos[dIdx]; sp >= 0 {
				if len(remaining) > 0 {
					bw.write(1, 1)
				}
				sBits := wireBitsForDict(seenCount)
				if sBits > 0 {
					bw.write(uint64(sp), sBits)
				}
			} else {
				bw.write(0, 1)
				rPos := sort.SearchInts(remaining, dIdx)
				remBits := wireBitsForDict(len(remaining))
				if remBits > 0 {
					bw.write(uint64(rPos), remBits)
				}
				remaining = append(remaining[:rPos], remaining[rPos+1:]...)
				seenPos[dIdx] = seenCount
				seenCount++
			}
		}
	}
	bw.flush()
	out = append(out, bw.buf...)

	prevOff := 0
	for _, o := range sent {
		deltaLen := uint32(o - prevOff - 1)
		out = appendWireVarint32(out, deltaLen)
		prevOff = o
	}
	return out, nil
}

// DecodeWireBytes decodes a compact lossless `RMBP` binary stream into flat slots and sentence frames.
func DecodeWireBytes(raw []byte) ([]BlindTokenAtom, [][]BlindTokenAtom, error) {
	if len(raw) < wireHeaderBytes {
		return nil, nil, errors.New("ramesdk: wire payload truncated")
	}
	if string(raw[0:4]) != wireMagic {
		return nil, nil, errors.New("ramesdk: wire payload magic mismatch")
	}
	if raw[4] != wireVersion {
		return nil, nil, fmt.Errorf("ramesdk: wire payload unsupported version %d", raw[4])
	}
	flags, grK, palN := raw[5], int(raw[6]), int(raw[7])
	if flags&^wireMaxFlag != 0 {
		return nil, nil, errors.New("ramesdk: wire payload invalid flags")
	}
	dictN := int(binary.BigEndian.Uint16(raw[8:10]))
	slotN := int(binary.BigEndian.Uint32(raw[10:14]))
	sentN := int(binary.BigEndian.Uint16(raw[14:16]))

	if dictN != 0 && grK != wireGolombKForDict(dictN) {
		return nil, nil, fmt.Errorf("ramesdk: wire payload grK=%d want %d", grK, wireGolombKForDict(dictN))
	}
	if dictN == 0 && (grK != 0 || palN != 0 || slotN != 0) {
		return nil, nil, errors.New("ramesdk: wire payload invalid empty header")
	}
	if palN > dictN {
		return nil, nil, errors.New("ramesdk: wire payload palN > dictN")
	}
	identity := flags&wireFlagIdentityIndex != 0
	wideF := flags&wireFlagWideF != 0
	if identity && dictN != slotN {
		return nil, nil, errors.New("ramesdk: wire payload identity conflicts with dictN/slotN")
	}
	if dictN == 0 && wideF {
		return nil, nil, errors.New("ramesdk: wire payload invalid empty wideF header")
	}
	if slotN > wireMaxSlotN || (dictN > 1 && slotN > len(raw)*8) {
		return nil, nil, fmt.Errorf("ramesdk: wire payload slotN=%d exceeds capacity", slotN)
	}

	off := wireHeaderBytes
	dict := make([]BlindTokenAtom, dictN)
	var palette []wireBF

	if palN > 0 {
		palette = make([]wireBF, palN)
		for i := range palette {
			v, n, err := readWireVarint(raw, off)
			if err != nil {
				return nil, nil, err
			}
			palette[i].B = v
			off += n
		}
		if wideF {
			if off+palN > len(raw) {
				return nil, nil, errors.New("ramesdk: wire payload truncated in wide palette")
			}
			hasHighNib := false
			for i := range palette {
				palette[i].F = raw[off+i]
				if palette[i].F > 0x0F {
					hasHighNib = true
				}
			}
			if !hasHighNib {
				return nil, nil, errors.New("ramesdk: wire payload unnecessary wideF flag")
			}
			off += palN
		} else {
			fBytes := (palN + 1) / 2
			if off+fBytes > len(raw) {
				return nil, nil, errors.New("ramesdk: wire payload truncated in palette")
			}
			for i := range palette {
				nib := raw[off+i/2] & 0x0F
				if i%2 == 1 {
					nib = raw[off+i/2] >> 4
				}
				palette[i].F = nib
			}
			if palN%2 == 1 && (raw[off+fBytes-1]&0xF0) != 0 {
				return nil, nil, errors.New("ramesdk: wire payload non-zero palette padding")
			}
			off += fBytes
		}
		for i := 1; i < palN; i++ {
			if palette[i].B < palette[i-1].B || (palette[i].B == palette[i-1].B && palette[i].F <= palette[i-1].F) {
				return nil, nil, errors.New("ramesdk: wire payload palette not strictly sorted")
			}
		}
	} else if dictN > 0 {
		for i := range dict {
			v, n, err := readWireVarint(raw, off)
			if err != nil {
				return nil, nil, err
			}
			dict[i].RawBitsX10 = v
			off += n
		}
		if wideF {
			if off+dictN > len(raw) {
				return nil, nil, errors.New("ramesdk: wire payload truncated in wide f-column")
			}
			hasHighNib := false
			for i := range dict {
				dict[i].ClassFlags = raw[off+i]
				if dict[i].ClassFlags > 0x0F {
					hasHighNib = true
				}
			}
			if !hasHighNib {
				return nil, nil, errors.New("ramesdk: wire payload unnecessary wideF flag")
			}
			off += dictN
		} else {
			fBytes := (dictN + 1) / 2
			if off+fBytes > len(raw) {
				return nil, nil, errors.New("ramesdk: wire payload truncated in f-column")
			}
			for i := range dict {
				nib := raw[off+i/2] & 0x0F
				if i%2 == 1 {
					nib = raw[off+i/2] >> 4
				}
				dict[i].ClassFlags = nib
			}
			if dictN%2 == 1 && (raw[off+fBytes-1]&0xF0) != 0 {
				return nil, nil, errors.New("ramesdk: wire payload non-zero f-column padding")
			}
			off += fBytes
		}
	}

	br := &wireBitReader{raw: raw[off:]}
	var prevH uint64
	maxUnary := uint64(1) << uint(64-grK+1)
	for i := range dict {
		var delta uint64
		if grK == 64 {
			v, err := br.read(64)
			if err != nil {
				return nil, nil, err
			}
			delta = v
		} else {
			var q uint64
			for {
				bit, err := br.read(1)
				if err != nil {
					return nil, nil, err
				}
				if bit == 0 {
					break
				}
				q++
				if q > maxUnary {
					return nil, nil, errors.New("ramesdk: wire payload unary overflow")
				}
			}
			rem, err := br.read(grK)
			if err != nil {
				return nil, nil, err
			}
			delta = (q << uint(grK)) | rem
		}
		if i > 0 && prevH+delta < prevH {
			return nil, nil, errors.New("ramesdk: wire payload delta overflow")
		}
		dict[i].H = prevH + delta
		prevH = dict[i].H
	}

	if palN > 0 {
		palBits := wireBitsForDict(palN)
		for i := range dict {
			pIdx := uint64(0)
			if palBits > 0 {
				var err error
				pIdx, err = br.read(palBits)
				if err != nil {
					return nil, nil, err
				}
				if pIdx >= uint64(palN) {
					return nil, nil, errors.New("ramesdk: wire payload palette index out of range")
				}
			}
			dict[i].RawBitsX10 = palette[pIdx].B
			dict[i].ClassFlags = palette[pIdx].F
		}
	}

	for i := 1; i < dictN; i++ {
		if !wireAtomLess(dict[i-1], dict[i]) {
			return nil, nil, errors.New("ramesdk: wire payload dictionary not sorted")
		}
	}

	index := make([]int, slotN)
	if identity {
		for i := range index {
			index[i] = i
		}
	} else if dictN == 1 {
		for i := range index {
			index[i] = 0
		}
	} else if dictN > 1 {
		remaining := make([]int, dictN)
		for i := range remaining {
			remaining[i] = i
		}
		seenList := make([]int, 0, dictN)
		for i := 0; i < slotN; i++ {
			if len(seenList) == 0 {
				remBits := wireBitsForDict(len(remaining))
				rPos, err := br.read(remBits)
				if err != nil {
					return nil, nil, err
				}
				if int(rPos) >= len(remaining) {
					return nil, nil, errors.New("ramesdk: wire payload slot index out of range")
				}
				dIdx := remaining[rPos]
				remaining = append(remaining[:rPos], remaining[rPos+1:]...)
				seenList = append(seenList, dIdx)
				index[i] = dIdx
				continue
			}
			isRepeat := uint64(1)
			if len(remaining) > 0 {
				var err error
				isRepeat, err = br.read(1)
				if err != nil {
					return nil, nil, err
				}
			}
			if isRepeat == 1 {
				sBits := wireBitsForDict(len(seenList))
				var sp uint64
				if sBits > 0 {
					var err error
					sp, err = br.read(sBits)
					if err != nil {
						return nil, nil, err
					}
					if int(sp) >= len(seenList) {
						return nil, nil, errors.New("ramesdk: wire payload seen index out of range")
					}
				}
				index[i] = seenList[sp]
			} else {
				remBits := wireBitsForDict(len(remaining))
				var rPos uint64
				if remBits > 0 {
					var err error
					rPos, err = br.read(remBits)
					if err != nil {
						return nil, nil, err
					}
					if int(rPos) >= len(remaining) {
						return nil, nil, errors.New("ramesdk: wire payload remaining index out of range")
					}
				}
				dIdx := remaining[rPos]
				remaining = append(remaining[:rPos], remaining[rPos+1:]...)
				seenList = append(seenList, dIdx)
				index[i] = dIdx
			}
		}
		if len(remaining) != 0 {
			return nil, nil, errors.New("ramesdk: wire payload unused dictionary entry")
		}
	}

	if err := br.verifyZeroPadding(); err != nil {
		return nil, nil, err
	}
	off += br.bytesRead()

	var sent []int
	if sentN > 0 {
		sent = make([]int, sentN)
		prevOff := 0
		for i := 0; i < sentN; i++ {
			deltaLen, n, err := readWireVarint32(raw, off)
			if err != nil {
				return nil, nil, err
			}
			off += n
			nextOff := prevOff + int(deltaLen) + 1
			if nextOff <= prevOff || nextOff > slotN {
				return nil, nil, errors.New("ramesdk: wire payload invalid sentence offset")
			}
			sent[i] = nextOff
			prevOff = nextOff
		}
	}
	if off != len(raw) {
		return nil, nil, errors.New("ramesdk: wire payload trailing bytes")
	}
	if err := checkWireOffsets(sent, slotN); err != nil {
		return nil, nil, err
	}

	atoms := make([]BlindTokenAtom, slotN)
	for i, p := range index {
		atoms[i] = dict[p]
	}
	var frames [][]BlindTokenAtom
	if len(sent) > 0 {
		frames = make([][]BlindTokenAtom, 0, len(sent))
		prev := 0
		for _, o := range sent {
			frames = append(frames, atoms[prev:o])
			prev = o
		}
	}
	return atoms, frames, nil
}

func checkWireOffsets(sent []int, slotN int) error {
	prev := 0
	for i, o := range sent {
		if o <= prev || o > slotN {
			return fmt.Errorf("ramesdk: invalid sentence offset[%d]=%d (prev=%d slots=%d)", i, o, prev, slotN)
		}
		prev = o
	}
	if len(sent) > 0 && prev != slotN {
		return fmt.Errorf("ramesdk: last sentence offset %d != slots %d", prev, slotN)
	}
	return nil
}

func wireVarintLen(v uint16) int {
	switch {
	case v < 0x80:
		return 1
	case v < 0x4000:
		return 2
	default:
		return 3
	}
}

func appendWireVarint(dst []byte, v uint16) []byte {
	for v >= 0x80 {
		dst = append(dst, byte(v)|0x80)
		v >>= 7
	}
	return append(dst, byte(v))
}

func readWireVarint(raw []byte, off int) (uint16, int, error) {
	var out uint32
	shift := uint(0)
	for n := 1; ; n++ {
		if off+n-1 >= len(raw) {
			return 0, 0, errors.New("ramesdk: wire varint truncated")
		}
		if n > 3 {
			return 0, 0, errors.New("ramesdk: wire varint overflow")
		}
		c := raw[off+n-1]
		out |= uint32(c&0x7F) << shift
		if c&0x80 == 0 {
			if out > 0xFFFF {
				return 0, 0, errors.New("ramesdk: wire varint u16 overflow")
			}
			v := uint16(out)
			if wireVarintLen(v) != n {
				return 0, 0, errors.New("ramesdk: wire varint non-canonical")
			}
			return v, n, nil
		}
		shift += 7
	}
}

func wireVarint32Len(v uint32) int {
	switch {
	case v < (1 << 7):
		return 1
	case v < (1 << 14):
		return 2
	case v < (1 << 21):
		return 3
	case v < (1 << 28):
		return 4
	default:
		return 5
	}
}

func appendWireVarint32(dst []byte, v uint32) []byte {
	for v >= 0x80 {
		dst = append(dst, byte(v)|0x80)
		v >>= 7
	}
	return append(dst, byte(v))
}

func readWireVarint32(raw []byte, off int) (uint32, int, error) {
	var out uint64
	shift := uint(0)
	for n := 1; ; n++ {
		if off+n-1 >= len(raw) {
			return 0, 0, errors.New("ramesdk: wire varint32 truncated")
		}
		if n > 5 {
			return 0, 0, errors.New("ramesdk: wire varint32 overflow")
		}
		c := raw[off+n-1]
		out |= uint64(c&0x7F) << shift
		if c&0x80 == 0 {
			if out > 0xFFFFFFFF {
				return 0, 0, errors.New("ramesdk: wire varint32 u32 overflow")
			}
			v := uint32(out)
			if wireVarint32Len(v) != n {
				return 0, 0, errors.New("ramesdk: wire varint32 non-canonical")
			}
			return v, n, nil
		}
		shift += 7
	}
}

type wireBitWriter struct {
	buf []byte
	cur byte
	n   uint
}

func (w *wireBitWriter) write(v uint64, width int) {
	for i := width - 1; i >= 0; i-- {
		w.cur = w.cur<<1 | byte((v>>uint(i))&1)
		w.n++
		if w.n == 8 {
			w.buf = append(w.buf, w.cur)
			w.cur, w.n = 0, 0
		}
	}
}

func (w *wireBitWriter) flush() {
	if w.n > 0 {
		w.buf = append(w.buf, w.cur<<(8-w.n))
		w.cur, w.n = 0, 0
	}
}

type wireBitReader struct {
	raw []byte
	bit int
}

func (r *wireBitReader) read(width int) (uint64, error) {
	var out uint64
	for i := 0; i < width; i++ {
		if r.bit >= len(r.raw)*8 {
			return 0, errors.New("ramesdk: wire bitstream truncated")
		}
		byteIdx := r.bit / 8
		bitIdx := 7 - uint(r.bit%8)
		out = out<<1 | uint64((r.raw[byteIdx]>>bitIdx)&1)
		r.bit++
	}
	return out, nil
}

func (r *wireBitReader) bytesRead() int {
	return (r.bit + 7) / 8
}

func (r *wireBitReader) verifyZeroPadding() error {
	rem := r.bit % 8
	if rem == 0 {
		return nil
	}
	byteIdx := r.bit / 8
	mask := byte((1 << uint(8-rem)) - 1)
	if (r.raw[byteIdx] & mask) != 0 {
		return errors.New("ramesdk: wire bitstream non-zero padding")
	}
	return nil
}

// EvaluatePulseSignal computes a 100% plaintext-free 5-dimension segmentation quality report from a BlindPulsePacket.
func EvaluatePulseSignal(pkt BlindPulsePacket) PulseSignalReport {
	slots := pkt.Slots
	frames := pkt.SentenceFrames
	if len(slots) == 0 && len(frames) > 0 {
		for _, fr := range frames {
			slots = append(slots, fr...)
		}
	}
	n := len(slots)
	if n == 0 {
		return PulseSignalReport{Score: 100, Grade: "EMPTY"}
	}
	if len(frames) == 0 {
		frames = [][]BlindTokenAtom{slots}
	}
	m := len(frames)

	inHarmonicSpan := make([]bool, n)
	foldedSpans, compoundGroups, companionSlots := markHarmonicConservationSpans(slots, inHarmonicSpan)
	effectiveUnits := n - companionSlots
	if effectiveUnits < 1 {
		effectiveUnits = 1
	}

	uniqueMap := make(map[uint64]int, n)
	var sumB int
	minB, maxB := int(slots[0].RawBitsX10), int(slots[0].RawBitsX10)
	gluedCnt, dustCnt := 0, 0
	for i, s := range slots {
		uniqueMap[s.H]++
		b := int(s.RawBitsX10)
		sumB += b
		if b < minB {
			minB = b
		}
		if b > maxB {
			maxB = b
		}
		if b > 320 && !inHarmonicSpan[i] {
			gluedCnt++
		}
		if b <= 20 && s.ClassFlags == 0 && !inHarmonicSpan[i] {
			dustCnt++
		}
	}

	isTandem := make([]bool, n)
	for p := 1; p <= 3; p++ {
		repRun := 1
		for i := p; i < n; i++ {
			if slots[i].H != 0 && slots[i].H == slots[i-p].H {
				repRun++
				if repRun >= 2 {
					isTandem[i] = true
				}
			} else {
				repRun = 1
			}
		}
	}
	tandemCnt := 0
	for _, td := range isTandem {
		if td {
			tandemCnt++
		}
	}

	singleSlotFrames := 0
	for _, fr := range frames {
		if len(fr) == 1 {
			singleSlotFrames++
		}
	}

	u := len(uniqueMap)
	meanB := sumB / n
	gluedPermille := (gluedCnt * 1000) / n
	dustPermille := (dustCnt * 1000) / n
	tandemPermille := (tandemCnt * 1000) / n
	singleFramePermille := (singleSlotFrames * 1000) / m

	spreadPermille := 0
	if maxB+minB > 0 {
		spreadPermille = ((maxB - minB) * 1000) / (maxB + minB)
	}

	coCovered, coTrunc, coCoveragePermille := u, 0, 1000
	if u > blindCooccurHorizonCap {
		coCovered = blindCooccurHorizonCap
		coTrunc = u - blindCooccurHorizonCap
		coCoveragePermille = (blindCooccurHorizonCap * 1000) / u
	}

	carrierScore := 100
	if gluedPermille > 50 {
		penalty := (gluedPermille - 50) / 8
		if meanB > 400 {
			penalty += (meanB - 400) / 20
		}
		carrierScore -= penalty
	}
	if dustPermille > 350 {
		carrierScore -= (dustPermille - 350) / 7
	}
	carrierScore = clampScore100(carrierScore)

	modScore := 100
	if n >= 4 {
		if spreadPermille < 120 && u <= 2 {
			modScore -= (120 - spreadPermille) / 2
		} else if spreadPermille == 0 && n >= 6 && dustPermille > 500 {
			modScore -= 45
		}
	}
	if n == 1 && meanB > 320 {
		modScore = 35
	}
	modScore = clampScore100(modScore)

	framingScore := 100
	if effectiveUnits >= 48 && m == 1 {
		framingScore -= minInt(65, (effectiveUnits-32)/2)
	}
	if m >= 6 && singleFramePermille > 600 {
		framingScore -= (singleFramePermille - 500) / 8
	}
	framingScore = clampScore100(framingScore)

	divScore := 100
	if tandemPermille > 120 {
		divScore -= (tandemPermille - 120) / 8
	}
	uniquePermille := (u * 1000) / n
	if n >= 12 && uniquePermille < 200 {
		divScore -= (200 - uniquePermille) / 3
	}
	divScore = clampScore100(divScore)

	coScore := 100
	if coCoveragePermille < 1000 {
		coScore = 40 + (coCoveragePermille*60)/1000
	}
	coScore = clampScore100(coScore)

	composite := clampScore100((carrierScore*25 + modScore*20 + framingScore*20 + divScore*20 + coScore*15) / 100)

	var warnings []PulseSignalWarning
	if gluedPermille >= 150 || (n == 1 && meanB > 320) {
		warnings = append(warnings, PulseSignalWarning{
			Code:    WarnCoarseUnsegmentedCarrier,
			Message: "Coarse segmentation detected: high share of glued slots (>32 bits) triggers rate-distortion soft saturation and degrades partial-query recall; enable 2-gram sliding windows or subword splitting.",
		})
	}
	if dustPermille >= 500 {
		warnings = append(warnings, PulseSignalWarning{
			Code:    WarnOverFragmentedDust,
			Message: "Over-fragmented segmentation detected: excessive single-character dust slots dilute document frequency (DF) and collapse waveform modulation contrast; emit word-level or harmonic parent atoms.",
		})
	}
	if modScore < 65 {
		warnings = append(warnings, PulseSignalWarning{
			Code:    WarnFlatCarrierModulation,
			Message: "Flat carrier modulation detected: uniform or single-slot pulse width reduces waveform peak-to-floor contrast and weakens cell addressing.",
		})
	}
	if (effectiveUnits >= 48 && m == 1) || (m >= 6 && singleFramePermille > 600) {
		warnings = append(warnings, PulseSignalWarning{
			Code:    WarnDegenerateSentenceFrames,
			Message: "Degenerate sentence framing detected: missing sentence boundaries or single-word frames impair cloud fractal dry-sentence distillation and evidence span delivery.",
		})
	}
	if tandemPermille >= 200 {
		warnings = append(warnings, PulseSignalWarning{
			Code:    WarnHighTandemRedundancy,
			Message: "High tandem pulse repetition detected: consecutive repeated token hashes trigger periodic damping; strip formatting separators before packing.",
		})
	}
	if coTrunc > 32 {
		warnings = append(warnings, PulseSignalWarning{
			Code:    WarnCooccurHorizonTruncated,
			Message: "Co-occurrence horizon truncation detected: unique atoms per unit substantially exceed the 64-atom co-occurrence window; split long documents into paragraph units or use harmonic parent grouping.",
		})
	}

	grade := "F"
	switch {
	case composite >= 90:
		grade = "S"
	case composite >= 80:
		grade = "A"
	case composite >= 70:
		grade = "B"
	case composite >= 55:
		grade = "C"
	}

	return PulseSignalReport{
		Score:                    composite,
		Grade:                    grade,
		NeedsAttention:           composite < 70 || len(warnings) > 0,
		TotalSlots:               n,
		UniqueSlotCount:          u,
		SentenceFrameCount:       m,
		MeanCarrierBitsX10:       meanB,
		GluedSlotCount:           gluedCnt,
		GluedSlotPermille:        gluedPermille,
		DustSlotCount:            dustCnt,
		DustSlotPermille:         dustPermille,
		TandemRepeatCount:        tandemCnt,
		TandemRepeatPermille:     tandemPermille,
		SingleSlotFrameCount:     singleSlotFrames,
		SingleSlotFramePermille:  singleFramePermille,
		WidthSpreadPermille:      spreadPermille,
		CooccurCoveredUnique:     coCovered,
		CooccurTruncatedUnique:   coTrunc,
		CooccurCoveragePermille:  coCoveragePermille,
		FoldedBiUniSpans:         foldedSpans,
		CompoundHarmonicGroups:   compoundGroups,
		CarrierHealthSubscore:    carrierScore,
		ModulationHealthSubscore: modScore,
		FramingHealthSubscore:    framingScore,
		DiversityHealthSubscore:  divScore,
		CooccurHealthSubscore:    coScore,
		Warnings:                 warnings,
	}
}

func markHarmonicConservationSpans(slots []BlindTokenAtom, inSpan []bool) (foldedSpans int, compoundGroups int, companionSlots int) {
	n := len(slots)
	for i := 0; i < n; {
		if slots[i].ClassFlags != 0 || slots[i].RawBitsX10 < 70 {
			i++
			continue
		}
		bestM := 0
		maxM := (n - i + 1) / 2
		if maxM > 64 {
			maxM = 64
		}
		for m := maxM; m >= 2; m-- {
			biCnt := m - 1
			uniStart := i + biCnt
			uniBase := int(slots[uniStart].RawBitsX10)
			if uniBase < 35 || uniBase > 150 {
				continue
			}
			valid := true
			for k := 0; k < m; k++ {
				uSlot := slots[uniStart+k]
				if uSlot.ClassFlags != 0 {
					valid = false
					break
				}
				diffU := int(uSlot.RawBitsX10) - uniBase
				if diffU < -20 || diffU > 20 {
					valid = false
					break
				}
			}
			if !valid {
				continue
			}
			for k := 0; k < biCnt; k++ {
				bSlot := slots[i+k]
				if bSlot.ClassFlags != 0 {
					valid = false
					break
				}
				expectedBi := int(slots[uniStart+k].RawBitsX10) + int(slots[uniStart+k+1].RawBitsX10)
				diffB := int(bSlot.RawBitsX10) - expectedBi
				if diffB < -25 || diffB > 25 {
					valid = false
					break
				}
			}
			if valid {
				bestM = m
				break
			}
		}
		if bestM >= 2 {
			foldedSpans++
			companionSlots += bestM - 1
			spanLen := 2*bestM - 1
			for k := 0; k < spanLen; k++ {
				inSpan[i+k] = true
			}
			i += spanLen
			continue
		}
		i++
	}

	isSub := make([]bool, n)
	for i := 0; i < n; i++ {
		if isSub[i] {
			continue
		}
		harmEnd := i
		for j := i + 1; j < n && j <= i+2; j++ {
			cur := slots[j]
			isParityHarm := (cur.ClassFlags&FlagHasConn) != 0 && (cur.ClassFlags&FlagHasDigit) == 0 &&
				slots[i].RawBitsX10 >= 20 && (slots[i].RawBitsX10&1) == 0 &&
				(cur.RawBitsX10 == slots[i].RawBitsX10-1 || (slots[i].RawBitsX10 >= 22 && cur.RawBitsX10 == slots[i].RawBitsX10-3))
			if isParityHarm || (cur.ClassFlags&FlagIsHarmonicCompanion) != 0 {
				harmEnd = j
			} else {
				break
			}
		}
		if harmEnd > i {
			compoundGroups++
			companionSlots += harmEnd - i
			inSpan[i] = true
			for j := i + 1; j <= harmEnd; j++ {
				isSub[j] = true
				inSpan[j] = true
			}
			i = harmEnd
			continue
		}

		parentB := int(slots[i].RawBitsX10)
		hasCompoundFlag := (slots[i].ClassFlags & (FlagHasConn | FlagHasCamel | FlagHasDigit)) != 0
		if !hasCompoundFlag || parentB < 60 {
			continue
		}
		matchedEnd := -1
		sumSubB, maxSubB := 0, 0
		distinctSubH := false
		firstSubH := uint64(0)
		maxSub := minInt(n-1, i+8)
		for j := i + 1; j <= maxSub; j++ {
			subB := int(slots[j].RawBitsX10)
			if subB <= 0 || subB >= parentB || slots[j].H == slots[i].H {
				break
			}
			if j == i+1 {
				firstSubH = slots[j].H
			} else if slots[j].H != firstSubH {
				distinctSubH = true
			}
			if subB > maxSubB {
				maxSubB = subB
			}
			sumSubB += subB
			subCnt := j - i
			if subCnt >= 2 && maxSubB >= 33 && distinctSubH {
				diff := parentB - sumSubB
				if diff >= -10 && diff <= 15*subCnt {
					matchedEnd = j
					break
				}
			}
			if sumSubB > parentB+10 {
				break
			}
		}
		if matchedEnd > i+1 {
			compoundGroups++
			companionSlots += matchedEnd - i
			inSpan[i] = true
			for j := i + 1; j <= matchedEnd; j++ {
				isSub[j] = true
				inSpan[j] = true
			}
			i = matchedEnd
		}
	}
	return foldedSpans, compoundGroups, companionSlots
}

func clampScore100(v int) int {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// iso3166GeoHarmonicEntries encodes the opt-in merged closed-set harmonic table: ISO 3166 L0-L3
// multilingual geography at the head, followed by the kinship / social-role / calendar / biology /
// object-venue lexicons and the SI/IEC/ISO 4217 unit & statistical dimension axes (@unit:* / @dim:*).
// One token may own several (tag1,tag2) axes; entry order defines the first-seen axis priority
// collected by ensureISO3166GeoMap.
var iso3166GeoHarmonicEntries = []string{
	"@geo:cont:eu|@dim:geo:europe,european,europeans,europa,européen,européenne,europeo,europea,欧洲,歐洲,ヨーロッパ,欧州,유럽,구주,европа,ยุโรป,scandinavia,scandinavian,nordic,北欧,北歐,스칸디나비아,balkans,balkan,巴尔干,mediterranean,地中海",
	"@geo:cont:as|@dim:geo:asia,asian,asians,asien,asie,asiatico,asiatique,亚洲,亞洲,アジア,亜州,아시아,азия,เอเชีย,eastasia,东亚,東亞,southeastasia,东南亚,東南亞,동남아시아,東南アジア,southasia,南亚,centralasia,中亚",
	"@geo:cont:me|@geo:cont:as:middleeast,middle-east,mideast,nahost,moyen-orient,中东,中東,중동,ตะวันออกกลาง",
	"@geo:cont:am|@dim:geo:america,americas,american,americans,northamerica,north-america,northamerican,southamerica,south-america,latinamerica,centralamerica,amerika,amérique,美洲,北美,北美洲,南美,南美洲,拉美,拉丁美洲,アメリカ州,北米,南米,북미,남미,미주,아메리카,ทวีปอเมริกา",
	"@geo:cont:af|@dim:geo:africa,african,africans,afrika,afrique,africano,非洲,アフリカ,아프리카,африка,แอฟริกา,northafrica,北非,sub-saharan",
	"@geo:cont:oc|@dim:geo:oceania,oceanian,australasia,ozeanien,océanie,大洋洲,オセアニア,오세아니아,โอเชียเนีย",
	"@geo:cc:gb|@geo:cont:eu:uk,u.k.,gb,gbr,britain,greatbritain,unitedkingdom,british,england,english,scotland,scottish,wales,welsh,northernireland,英国,英國,英格兰,英格蘭,苏格兰,蘇格蘭,威尔士,威爾士,北爱尔兰,イギリス,英国人,イングランド,スコットランド,ウェールズ,영국,잉글랜드,스코틀랜드,웨일스,großbritannien,vereinigtenkönigreich,royaume-uni,angleterre,écosse,reinounido,inglaterra,escocia,regnounito,สหราชอาณาจักร,อังกฤษ,london,greaterlondon,westminster,soho,hillingdon,heathrow,gatwick,stansted,cambridge,cambridgeshire,peterborough,petersfield,oxford,oxfordshire,manchester,birmingham,liverpool,leeds,sheffield,bristol,newcastle,nottingham,southampton,portsmouth,brighton,leicester,coventry,york,yorkshire,warwick,somerset,canterbury,kent,windsor,surrey,sussex,essex,norfolk,norwich,suffolk,devon,exeter,cornwall,edinburgh,glasgow,aberdeen,dundee,inverness,stirling,standrews,highlands,cardiff,swansea,newport,belfast,derry,伦敦,倫敦,剑桥,劍橋,牛津,曼彻斯特,伯明翰,利物浦,爱丁堡,愛丁堡,格拉斯哥,卡迪夫,贝尔法斯特,巴斯,约克,ロンドン,ケンブリッジ,オックスフォード,マンチェスター,エジンバラ,グラスゴー,런던,케임브리지,옥스퍼드,맨체스터,에든버러,글래스고,marazion,penzance,ives,scilly,glastonbury,crawley,willowbrae,tyne,northumberland,cumbria,lancashire,derbyshire,nottinghamshire,leicestershire,warwickshire,gloucestershire,dorset,hampshire,康沃尔,苏塞克斯,约克郡,牛津郡,诺福克,威斯敏斯特,コーンウォール,サセックス,ヨークシャー,ノーフォーク,ウェストミンスター,콘월,서식스,요크셔,노퍽,웨스트민스터,cornouailles,คอร์นวอลล์",
	"@geo:cc:fr|@geo:cont:eu:france,french,metropolitanfrance,frankreich,francia,frança,法国,法國,法兰西,フランス,프랑스,франция,ฝรั่งเศส,ile-de-france,île-de-france,provence,provence-alpes-côte,côted'azur,azur,vaucluse,bouches-du-rhône,rhône,var,alpes-maritimes,normandy,normandie,brittany,bretagne,alsace,lorraine,burgundy,bourgogne,aquitaine,occitanie,auvergne,loire,corsica,corse,savoie,chamonix,paris,marseille,lyon,toulouse,nantes,strasbourg,montpellier,bordeaux,lille,rennes,reims,toulon,saint-étienne,leharve,grenoble,dijon,angers,nîmes,villeurbanne,aix-en-provence,cannes,antibes,avignon,cassis,marignane,istres,puyvert,pertuis,la-seyne-sur-mer,seyne-sur-mer,sablettes,saint-elme,pourquier,annecy,versailles,rouen,caen,nancy,metz,orléans,clermont-ferrand,limoges,perpignan,besançon,brest,biarritz,巴黎,马赛,馬賽,里昂,图卢兹,尼斯,南特,斯特拉斯堡,波尔多,里尔,土伦,戛纳,阿维尼翁,普罗旺斯,凡尔赛,パリ,マルセイユ,リヨン,ニース,ボルドー,カンヌ,プロヴァンス,파리,마르세유,리옹,니스,보르도,칸,프로방스,rhone,alpes,aix,montmartre,dunkerque,蔚蓝海岸,阿尔卑斯,霞慕尼,诺曼底,布列塔尼,蒙马特,コートダジュール,アルプス,シャモニー,ノルマンディー,ブルターニュ,ストラスブール,トゥールーズ,アヴィニョン,ヴェルサイユ,モンマルトル,코트다쥐르,알프스,샤모니,노르망디,브르타뉴,스트라스부르,툴루즈,아비뇽,베르사유,몽마르트르,provenza,normandía,bretaña,versalles,โพรวองซ์,แอลป์,นอร์มังดี,แวร์ซาย",
	"@geo:cc:de|@geo:cont:eu:germany,german,deutschland,deutsch,allemagne,alemania,alemanha,germania,德国,德國,德意志,ドイツ,독일,германия,เยอรมนี,bavaria,bayern,baden-württemberg,hessen,nrw,nordrhein-westfalen,sachsen,saxony,berlin,munich,münchen,hamburg,frankfurt,cologne,köln,stuttgart,düsseldorf,dortmund,essen,leipzig,bremen,dresden,hanover,hannover,nuremberg,nürnberg,duisburg,bochum,wuppertal,bielefeld,bonn,münster,karlsruhe,mannheim,augsburg,wiesbaden,heidelberg,freiburg,aachen,kiel,lübeck,rostock,potsdam,erfurt,mainz,saarbrücken,regensburg,würzburg,ulm,göttingen,tübingen,darmstadt,weimar,柏林,慕尼黑,汉堡,漢堡,法兰克福,法蘭克福,科隆,斯图加特,杜塞尔多夫,莱比锡,德累斯顿,汉诺威,纽伦堡,波恩,海德堡,巴伐利亚,ベルリン,ミュンヘン,ハンブルク,フランクフルト,ケルン,シュトゥットガルト,ハイデルベルク,베를린,뮌헨,함부르크,프랑크푸르트,쾰른,슈투트가르트,하이델베르크",
	"@geo:cc:it|@geo:cont:eu:italy,italian,italia,italien,italie,意大利,義大利,イタリア,이탈리아,италия,อิตาลี,lombardy,lombardia,tuscany,toscana,veneto,lazio,piedmont,piemonte,sicily,sicilia,sardinia,sardegna,emilia-romagna,campania,liguria,puglia,rome,roma,milan,milano,portello,naples,napoli,turin,torino,palermo,genoa,genova,bologna,florence,firenze,bari,catania,venice,venezia,verona,messina,padua,padova,trieste,brescia,parma,modena,pisa,siena,perugia,ravenna,rimini,salerno,bergamo,como,amalfi,capri,pompeii,罗马,羅馬,米兰,米蘭,那不勒斯,都灵,佛罗伦萨,佛羅倫斯,威尼斯,博洛尼亚,热那亚,维罗纳,比萨,西西里,托斯卡纳,ローマ,ミラノ,ナポリ,トリノ,フィレンツェ,ヴェネツィア,ベネチア,ボローニャ,ピサ,로마,밀라노,나폴리,토리노,피렌체,베네치아,베니스,볼로냐,피사,positano,伦巴第,撒丁岛,科莫,贝尔加莫,帕多瓦,锡耶纳,阿马尔菲,卡普里,庞贝,巴勒莫,ロンバルディア,トスカーナ,シチリア,サルデーニャ,コモ,ベルガモ,ヴェローナ,シエナ,ジェノヴァ,アマルフィ,カプリ,ポンペイ,パレルモ,롬바르디아,토스카나,시칠리아,사르데냐,코모,베르가모,베로나,시에나,제노바,아말피,카프리,폼페이,팔레르모,lombardei,toskana,sizilien,sardinien,lombardie,toscane,sicile,sardaigne,ลอมบาร์เดีย,ทัสคานี,ซิซิลี",
	"@geo:cc:es|@geo:cont:eu:spain,spanish,españa,spanien,espagne,spagna,espanha,西班牙,スペイン,스페인,испания,สเปน,catalonia,cataluña,catalunya,andalusia,andalucía,galicia,basque,euskadi,valencia,castile,aragon,balearic,canary,madrid,barcelona,seville,sevilla,zaragoza,málaga,malaga,murcia,palma,mallorca,ibiza,tenerife,laspalmas,bilbao,alicante,córdoba,cordoba,valladolid,vigo,gijón,granada,vitoria,coruña,acoruña,santiagodecompostela,compostela,toledo,salamanca,san-sebastián,donostia,pamplona,santander,tarragona,girona,marbella,马德里,馬德里,巴塞罗那,巴塞隆納,塞维利亚,瓦伦西亚,毕尔巴鄂,格拉纳达,马拉加,圣地亚哥-德孔波斯特拉,加利西亚,加泰罗尼亚,安达卢西亚,マドリード,バルセロナ,セビリア,バレンシア,ビルバオ,グラナダ,サンティアゴ・デ・コンポステーラ,마드리드,바르셀로나,세비야,발렌시아,빌바오,그라나다,산티아고데콤포스텔라,santiago,coruna,ensanche,圣地亚哥,孔波斯特拉,萨拉戈萨,科尔多瓦,托莱多,马略卡,サンティアゴ,コンポステーラ,ガリシア,カタルーニャ,アンダルシア,コルドバ,トレド,マヨルカ,산티아고,콤포스텔라,갈리시아,카탈루냐,안달루시아,코르도바,톨레도,마요르카,galicien,katalonien,andalusien,galice,catalogne,andalousie,ซานติอาโก,กาลิเซีย,กาตาลุญญา",
	"@geo:cc:pt|@geo:cont:eu:portugal,portuguese,portogallo,葡萄牙,ポルトガル,포르투갈,португалия,โปรตุเกส,lisbon,lisboa,porto,oporto,gaia,vilaisabel,vilanovadegaia,ribeira,cedofeita,braga,coimbra,faro,algarve,aveiro,funchal,madeira,azores,sintra,cascais,évora,guimarães,里斯本,波尔图,波爾圖,科英布拉,法鲁,阿爾加維,马德拉,辛特拉,リスボン,ポルト,コインブラ,리스본,포르투,코임브라,miragaia,marinha,加亚新城,布拉加,卡斯凯什,阿尔加维,亚速尔,ガイア,ブラガ,シントラ,アルガルヴェ,マデイラ,가이아,브라가,신트라,알가르브,마데이라,ปอร์โต,ลิสบอน,มาเดรา",
	"@geo:cc:nl|@geo:cont:eu:netherlands,dutch,holland,nederland,niederlande,pays-bas,paísesbajos,荷兰,荷蘭,オランダ,네덜란드,เนเธอร์แลนด์,amsterdam,rotterdam,thehague,denhaag,hague,utrecht,eindhoven,groningen,tilburg,almere,breda,nijmegen,delft,leiden,haarlem,maastricht,arnhem,schiphol,阿姆斯特丹,鹿特丹,海牙,乌得勒支,埃因霍温,代尔夫特,莱顿,马斯特里赫特,アムステルダム,ロッテルダム,ハーグ,ユトレヒト,암스테르담,로테르담,헤이그,위트레흐트",
	"@geo:cc:be|@geo:cont:eu:belgium,belgian,belgië,belgique,belgien,bélgica,比利时,比利時,ベルギー,벨기에,เบลเยียม,brussels,bruxelles,brussel,antwerp,antwerpen,ghent,gent,charleroi,liège,liege,bruges,brugge,leuven,namur,布鲁塞尔,布魯塞爾,安特卫普,根特,布鲁日,鲁汶,ブリュッセル,アントワープ,ブルージュ,브뤼셀,앤트워프,브뤼헤",
	"@geo:cc:lu|@geo:cont:eu:luxembourg,luxemburg,卢森堡,盧森堡,ルクセンブルク,룩셈부르크",
	"@geo:cc:ch|@geo:cont:eu:switzerland,swiss,schweiz,suisse,svizzera,suiza,helvetica,瑞士,スイス,스위스,สวิตเซอร์แลนด์,zurich,zürich,geneva,genève,genf,basel,lausanne,bern,berne,winterthur,lucerne,luzern,st.gallen,lugano,interlaken,zermatt,davos,montreux,zug,苏黎世,蘇黎世,日内瓦,日內瓦,巴塞尔,洛桑,伯尔尼,卢塞恩,琉森,因特拉肯,采尔马特,达沃斯,チューリッヒ,ジュネーブ,バーゼル,ベルン,ルツェルン,취리히,제네바,바젤,베른,루체른",
	"@geo:cc:at|@geo:cont:eu:austria,austrian,österreich,autriche,奥地利,奧地利,オーストリア,오스트리아,ออสเตรีย,vienna,wien,graz,linz,salzburg,innsbruck,klagenfurt,villach,hallstatt,tyrol,tirol,维也纳,維也納,萨尔茨堡,薩爾茨堡,因斯布鲁克,格拉茨,林茨,哈尔施塔特,ウィーン,ザルツブルク,インスブルック,빈,비엔나,잘츠부르크,인스브루크",
	"@geo:cc:ie|@geo:cont:eu:ireland,irish,éire,irland,irlande,irlanda,爱尔兰,愛爾蘭,アイルランド,아일랜드,ไอร์แลนด์,dublin,cork,limerick,galway,waterford,kilkenny,都柏林,科克,高威,ダブリン,더블린",
	"@geo:cc:se|@geo:cont:eu:sweden,swedish,sverige,schweden,suède,suecia,瑞典,スウェーデン,스웨덴,สวีเดน,stockholm,gothenburg,göteborg,malmö,malmo,uppsala,lund,linköping,kiruna,斯德哥尔摩,哥德堡,马尔默,乌普萨拉,ストックホルム,스톡홀름",
	"@geo:cc:no|@geo:cont:eu:norway,norwegian,norge,norwegen,norvège,noruega,挪威,ノルウェー,노르웨이,นอร์เวย์,oslo,bergen,trondheim,stavanger,tromsø,tromso,ålesund,lofoten,奥斯陆,奧斯陸,卑尔根,特罗姆瑟,オスロ,오슬로",
	"@geo:cc:dk|@geo:cont:eu:denmark,danish,danmark,dänemark,danemark,dinamarca,丹麦,丹麥,デンマーク,덴마크,เดนมาร์ก,copenhagen,københavn,aarhus,odense,aalborg,roskilde,billund,哥本哈根,奥胡斯,欧登塞,コペンハーゲン,코펜하겐",
	"@geo:cc:fi|@geo:cont:eu:finland,finnish,suomi,finnland,finlande,finlandia,芬兰,芬蘭,フィンランド,핀란드,ฟินแลนด์,helsinki,espoo,tampere,vantaa,oulu,turku,rovaniemi,lapland,赫尔辛基,赫爾辛基,坦佩雷,图尔库,罗瓦涅米,ヘルシンキ,헬싱키",
	"@geo:cc:is|@geo:cont:eu:iceland,icelandic,ísland,island,islande,islandia,冰岛,冰島,アイスランド,아이슬란드,ไอซ์แลนด์,reykjavik,reykjavík,akureyri,keflavik,雷克雅未克,レイキャビク,레이캬비크",
	"@geo:cc:pl|@geo:cont:eu:poland,polish,polska,polen,pologne,polonia,波兰,波蘭,ポーランド,폴란드,โปแลนด์,warsaw,warszawa,kraków,krakow,cracow,łódź,wrocław,wroclaw,poznań,poznan,gdańsk,gdansk,katowice,lublin,华沙,華沙,克拉科夫,格但斯克,弗罗茨瓦夫,波兹南,ワルシャワ,クラクフ,바르샤바,크라쿠프",
	"@geo:cc:cz|@geo:cont:eu:czechia,czech,czechrepublic,česko,tschechien,tchèque,捷克,チェコ,체코,เช็ก,prague,praha,brno,ostrava,plzeň,karlovyvary,českýkrumlov,bohemia,moravia,布拉格,布尔诺,プラハ,프라하",
	"@geo:cc:hu|@geo:cont:eu:hungary,hungarian,magyarország,ungarn,hongrie,hungría,ungheria,匈牙利,ハンガリー,헝가리,ฮังการี,budapest,ferihegy,debrecen,szeged,miskolc,pécs,győr,balaton,布达佩斯,布達佩斯,ブダペスト,부다페스트",
	"@geo:cc:sk|@geo:cont:eu:slovakia,slovak,slovensko,slowakei,slovaquie,斯洛伐克,スロバキア,슬로바키아,bratislava,košice,布拉迪斯拉发,ブラチスラバ,브라티슬라바",
	"@geo:cc:si|@geo:cont:eu:slovenia,slovenian,slovenija,slowenien,slovénie,斯洛文尼亚,スロベニア,슬로베니아,ljubljana,maribor,bled,卢布尔雅那,リュブリャナ,류블랴나",
	"@geo:cc:hr|@geo:cont:eu:croatia,croatian,hrvatska,kroatien,croatie,croazia,克罗地亚,克羅埃西亞,クロアチア,크로아티아,zagreb,rijeka,dubrovnik,zadar,pula,istria,dalmatia,萨格勒布,杜布罗夫尼克,斯普利特,ザグレブ,ドブロブニク,자그레브,두브로브니크",
	"@geo:cc:rs|@geo:cont:eu:serbia,serbian,srbija,serbien,serbie,塞尔维亚,塞爾維亞,セルビア,세르비아,belgrade,beograd,novisad,niš,贝尔格莱德,ベオグラード,베오그라드",
	"@geo:cc:ro|@geo:cont:eu:romania,romanian,românia,rumänien,roumanie,罗马尼亚,羅馬尼亞,ルーマニア,루마니아,bucharest,bucurești,cluj,cluj-napoca,timișoara,iași,constanța,brașov,transylvania,布加勒斯特,克卢日,ブカレスト,부쿠레슈티",
	"@geo:cc:bg|@geo:cont:eu:bulgaria,bulgarian,bulgarien,bulgarie,保加利亚,保加利亞,ブルガリア,불가리아,sofia,plovdiv,varna,burgas,索非亚,ソフィア,소피아",
	"@geo:cc:gr|@geo:cont:eu:greece,greek,hellas,griechenland,grèce,grecia,希腊,希臘,ギリシャ,그리스,กรีซ,athens,athina,thessaloniki,patras,heraklion,crete,santorini,mykonos,rhodes,corfu,delphi,雅典,塞萨洛尼基,克里特,圣托里尼,米科诺斯,アテネ,サントリーニ,아테네,산토리니",
	"@geo:cc:ua|@geo:cont:eu:ukraine,ukrainian,ukraina,乌克兰,烏克蘭,ウクライナ,우크라이나,kyiv,kiev,kharkiv,odesa,odessa,dnipro,lviv,基辅,利沃夫,敖德萨,キーウ,키이우",
	"@geo:cc:ee|@geo:cont:eu:estonia,estonian,eesti,estland,爱沙尼亚,エストニア,에스토니아,tallinn,tartu,塔林,タリン,탈린",
	"@geo:cc:lv|@geo:cont:eu:latvia,latvian,latvija,lettland,拉脱维亚,ラトビア,라트비아,riga,里加,リガ,리가",
	"@geo:cc:lt|@geo:cont:eu:lithuania,lithuanian,lietuva,litauen,立陶宛,リトアニア,리투아니아,vilnius,kaunas,维尔纽斯,ビリニュス,빌뉴스",
	"@geo:cc:mt|@geo:cont:eu:malta,maltese,马耳他,マルタ,몰타,valletta,瓦莱塔",
	"@geo:cc:cy|@geo:cont:eu:cyprus,cypriot,塞浦路斯,キプロス,키프로스,nicosia,limassol,larnaca,尼科西亚",
	"@geo:cc:mc|@geo:cont:eu:monaco,摩纳哥,モナコ,모나코,montecarlo,蒙特卡洛",
	"@geo:cc:tr|@geo:cont:eu:turkey,turkish,türkiye,türkei,turquie,turquía,土耳其,トルコ,터키,튀르키예,ตุรกี,istanbul,ankara,izmir,antalya,bursa,adana,gaziantep,konya,cappadocia,bodrum,伊斯坦布尔,伊斯坦堡,安卡拉,安塔利亚,卡帕多奇亚,イスタンブール,アンカラ,이스탄불,앙카라",
	"@geo:cc:ru|@geo:cont:eu:russia,russian,россия,russland,russie,rusia,俄罗斯,俄羅斯,俄国,ロシア,러시아,รัสเซีย,moscow,moskva,st.petersburg,saintpetersburg,petersburg,novosibirsk,yekaterinburg,kazan,vladivostok,sochi,siberia,莫斯科,圣彼得堡,海参崴,西伯利亚,モスクワ,サンクトペテルブルク,모스크바,상트페테르부르크",
	"@geo:cc:cn|@geo:cont:as:china,chinese,prc,chine,cina,中国,中國,中华,中華,中国大陆,內地,中国語,チャイナ,중국,จีน,beijing,peking,shanghai,tianjin,chongqing,guangdong,canton,zhejiang,jiangsu,shandong,sichuan,hubei,hunan,fujian,anhui,henan,hebei,shanxi,shaanxi,liaoning,jilin,heilongjiang,jiangxi,guangxi,yunnan,guizhou,hainan,gansu,qinghai,ningxia,xinjiang,tibet,xizang,innermongolia,guangzhou,shenzhen,zhuhai,foshan,dongguan,zhongshan,huizhou,shantou,hangzhou,ningbo,wenzhou,shaoxing,jiaxing,suzhou,nanjing,wuxi,changzhou,xuzhou,nantong,jinan,qingdao,yantai,weihai,chengdu,mianyang,leshan,jiuzhaigou,wuhan,yichang,changsha,zhangjiajie,xiamen,fuzhou,quanzhou,hefei,huangshan,zhengzhou,luoyang,kaifeng,shijiazhuang,taiyuan,xi'an,xian,shenyang,dalian,changchun,harbin,nanchang,guilin,nanning,beihai,kunming,dali,lijiang,shangri-la,xishuangbanna,wuhua,guiyang,haikou,sanya,lanzhou,xining,yinchuan,urumqi,lhasa,hohhot,北京,上海,天津,重庆,重慶,广东,廣東,浙江,江苏,江蘇,山东,山東,四川,湖北,湖南,福建,安徽,河南,河北,山西,陕西,陝西,辽宁,遼寧,吉林,黑龙江,黑龍江,江西,广西,廣西,云南,雲南,贵州,貴州,海南,甘肃,青海,宁夏,新疆,西藏,内蒙古,广州,廣州,深圳,珠海,佛山,东莞,杭州,宁波,温州,苏州,蘇州,南京,无锡,济南,青岛,青島,武汉,武漢,长沙,長沙,厦门,廈門,福州,合肥,郑州,洛阳,西安,沈阳,大连,长春,哈尔滨,哈爾濱,南昌,桂林,南宁,昆明,大理,丽江,西双版纳,贵阳,海口,三亚,三亞,兰州,西宁,银川,乌鲁木齐,拉萨,ペキン,シャンハイ,広州,深セン,베이징,상하이,광저우,선전,항저우,청두,우한,시안,쿤밍,윈난",
	"@geo:cc:hk|@geo:cont:as:hongkong,hk,hkg,kowloon,tsimshatsui,causewaybay,lantau,香港,九龙,九龍,尖沙咀,铜锣湾,中环,香港島,ホンコン,홍콩",
	"@geo:cc:mo|@geo:cont:as:macau,macao,taipa,cotai,澳门,澳門,マカオ,마카오",
	"@geo:cc:tw|@geo:cont:as:taiwan,taiwanese,taipei,kaohsiung,taichung,tainan,hsinchu,hualien,台湾,台灣,台北,臺北,高雄,台中,台南,新竹,花莲,タイワン,대만,타이베이",
	"@geo:cc:jp|@geo:cont:as:japan,japanese,nippon,nihon,japon,giappone,日本,日本国,ジャパン,일본,ญี่ปุ่น,hokkaido,honshu,kyushu,shikoku,okinawa,kantō,kansai,tokyo,shinjuku,shibuya,ginza,akihabara,roppongi,asakusa,ueno,haneda,narita,yokohama,kanagawa,osaka,umeda,namba,nagoya,aichi,sapporo,otaru,hakodate,niseko,fukuoka,hakata,kyoto,arashiyama,gion,kobe,hyogo,kawasaki,saitama,hiroshima,sendai,miyagi,chiba,kitakyushu,sakai,niigata,hamamatsu,shizuoka,kumamoto,okayama,agano,nara,kanazawa,ishikawa,nagano,karuizawa,hakone,nikko,kamakura,fuji,naha,东京,東京,横滨,橫濱,大阪,名古屋,札幌,福冈,福岡,京都,神户,神戶,广岛,廣島,仙台,千叶,奈良,金泽,冲绳,沖繩,北海道,箱根,镰仓,富士山,トウキョウ,ヨコハマ,オオサカ,ナゴヤ,サッポロ,フクオカ,キョウト,コウベ,ヒロシマ,センダイ,オキナワ,도쿄,요코하마,오사카,나고야,삿포로,후쿠오카,교토,고베,히로시마,센다이,오키나와,홋카이도",
	"@geo:cc:kr|@geo:cont:as:korea,southkorea,korean,rok,corea,corée,südkorea,韩国,韓國,南韩,大韩民国,韓国,한국,대한민국,เกาหลีใต้,seoul,gangnam,hongdae,myeongdong,itaewon,busan,pusan,haeundae,incheon,daegu,daejeon,gwangju,suwon,ulsan,gyeonggi,jeju,jejudo,gyeongju,jeonju,pangyo,首尔,首爾,汉城,釜山,仁川,大邱,大田,光州,水原,蔚山,济州,濟州,济州岛,庆州,江南,明洞,弘大,ソウル,プサン,インチョン,チェジュ,済州島,서울,강남,명동,홍대,부산,해운대,인천,대구,대전,광주,수원,울산,제주,제주도,경주",
	"@geo:cc:sg|@geo:cont:as:singapore,singaporean,sgp,changi,sentosa,marinabay,新加坡,狮城,星洲,シンガポール,싱가포르,สิงคโปร์",
	"@geo:cc:my|@geo:cont:as:malaysia,malaysian,kualalumpur,penang,georgetown,johor,johorbahru,malacca,melaka,sabah,kotakinabalu,sarawak,kuching,langkawi,马来西亚,馬來西亞,大马,吉隆坡,槟城,檳城,新山,马六甲,沙巴,亚庇,兰卡威,マレーシア,クアラルンプール,말레이시아,쿠알라룸푸르,มาเลเซีย",
	"@geo:cc:th|@geo:cont:as:thailand,thai,siam,bangkok,suvarnabhumi,chiangmai,chiangrai,phuket,pattaya,krabi,samui,kohsamui,ayutthaya,huahin,泰国,泰國,曼谷,清迈,清邁,普吉,普吉岛,芭提雅,甲米,苏梅岛,大城,タイ,バンコク,チェンマイ,プーケット,태국,방콕,치앙마이,푸켓,ประเทศไทย,กรุงเทพ,เชียงใหม่,ภูเก็ต,pathum,sukhumvit,silom,chatuchak,巴吞旺,暹罗,素坤逸,是隆,乍都乍,サイアム,スクンビット,シーロム,チャトゥチャック,アユタヤ,クラビ,サムイ,パタヤ,시암,수쿰빗,실롬,짜뚜짝,아유타야,끄라비,사무이,파타야,ปทุมวัน,สยาม,สุขุมวิท,สีลม,จตุจักร,อยุธยา,กระบี่,สมุย,พัทยา",
	"@geo:cc:vn|@geo:cont:as:vietnam,vietnamese,hanoi,hochiminh,saigon,danang,nha-trang,nhatrang,hoian,halong,phuquoc,dalat,hue,越南,河内,河內,胡志明,胡志明市,西贡,岘港,芽庄,会安,下龙湾,富国岛,ベトナム,ハノイ,ホーチミン,ダナン,베트남,하노이,호치민,다낭,เวียดนาม",
	"@geo:cc:id|@geo:cont:as:indonesia,indonesian,jakarta,bali,denpasar,ubud,semiyak,kuta,surabaya,bandung,medan,yogyakarta,lombok,java,sumatra,印度尼西亚,印尼,雅加达,巴厘岛,峇里島,乌布,泗水,万隆,日惹,龙目岛,インドネシア,ジャカルタ,バリ島,인도네시아,자카르타,발리,อินโดนีเซีย",
	"@geo:cc:ph|@geo:cont:as:philippines,filipino,philippine,manila,makati,cebu,davao,boracay,palawan,bohol,luzon,菲律宾,菲律賓,马尼拉,馬尼拉,宿务,长滩岛,巴拉望,薄荷岛,フィリピン,マニラ,セブ,필리핀,마닐라,세부,ฟิลิปปินส์",
	"@geo:cc:kh|@geo:cont:as:cambodia,cambodian,khmer,phnompenh,siemreap,angkor,angkorwat,sihanoukville,柬埔寨,金边,金邊,暹粒,吴哥窟,西哈努克,カンボジア,プノンペン,アンコールワット,캄보디아,프놈펜,앙코르와트,กัมพูชา,កម្ពុជា,ភ្នំពេញ",
	"@geo:cc:la|@geo:cont:as:laos,lao,laotian,vientiane,luangprabang,vangvieng,老挝,寮國,万象,永珍,琅勃拉邦,万荣,ラオス,ビエンチャン,라오스,비엔티안,ลาว,ລາວ,ວຽງຈັນ",
	"@geo:cc:mm|@geo:cont:as:myanmar,burma,burmese,yangon,rangoon,mandalay,bagan,naypyidaw,缅甸,緬甸,仰光,曼德勒,蒲甘,内比都,ミャンマー,ヤンゴン,미얀마,양곤,พม่า",
	"@geo:cc:in|@geo:cont:as:india,indian,bharat,newdelhi,delhi,mumbai,bombay,bengaluru,bangalore,hyderabad,chennai,madras,kolkata,calcutta,pune,ahmedabad,jaipur,agra,goa,kerala,varanasi,印度,新德里,德里,孟买,孟買,班加罗尔,海得拉巴,金奈,加尔各答,浦那,斋浦尔,阿格拉,果阿,インド,ニューデリー,ムンバイ,バンガロール,인도,뉴델리,뭄바이,방갈로르,อินเดีย",
	"@geo:cc:pk|@geo:cont:as:pakistan,pakistani,islamabad,karachi,lahore,巴基斯坦,伊斯兰堡,卡拉奇,拉合尔,パキスタン,파키스탄",
	"@geo:cc:lk|@geo:cont:as:srilanka,ceylon,colombo,kandy,galle,斯里兰卡,科伦坡,康提,スリランカ,스리랑카",
	"@geo:cc:np|@geo:cont:as:nepal,nepali,kathmandu,pokhara,尼泊尔,加德满都,博卡拉,ネパール,네팔",
	"@geo:cc:ae|@geo:cont:as:uae,emirates,emirati,dubai,abudhabi,sharjah,阿联酋,阿聯酋,迪拜,杜拜,阿布扎比,阿布達比,沙迦,ドバイ,アブダビ,두바이,아부다비",
	"@geo:cc:sa|@geo:cont:as:saudiarabia,saudi,riyadh,jeddah,mecca,makkah,medina,dammam,沙特,沙特阿拉伯,沙烏地阿拉伯,利雅得,吉达,麦加,麦地那,サウジアラビア,リヤド,사우디아라비아,리야드",
	"@geo:cc:il|@geo:cont:as:israel,israeli,telaviv,jerusalem,haifa,eilat,以色列,特拉维夫,耶路撒冷,海法,イスラエル,テルアビブ,エルサレム,이스라엘,텔아비브,예루살렘",
	"@geo:cc:qa|@geo:cont:as:qatar,qatari,doha,卡塔尔,卡達,多哈,カタール,ドーハ,카타르,도하",
	"@geo:cc:jo|@geo:cont:as:jordan,jordanian,amman,petra,aqaba,deadsea,wadirum,约旦,約旦,安曼,佩特拉,阿卡巴,死海,瓦迪拉姆,ヨルダン,アンマン,ペトラ,요르단,암만,페트라",
	"@geo:cc:us|@geo:cont:am:usa,u.s.,u.s.a.,unitedstates,unitedstatesofamerica,美国,美國,美利坚,アメリカ合衆国,米国,미국,미합중국,สหรัฐอเมริกา,california,texas,florida,newyork,pennsylvania,illinois,ohio,georgia,northcarolina,michigan,newjersey,virginia,washington,arizona,massachusetts,tennessee,indiana,maryland,missouri,wisconsin,colorado,minnesota,southcarolina,alabama,louisiana,kentucky,oregon,oklahoma,connecticut,utah,iowa,nevada,arkansas,mississippi,kansas,newmexico,nebraska,idaho,hawaii,maine,newhampshire,rhodeisland,montana,delaware,southdakota,northdakota,alaska,vermont,wyoming,nyc,manhattan,brooklyn,queens,bronx,losangeles,hollywood,santamonica,beverlyhills,pasadena,irvine,chicago,houston,harriscounty,phoenix,scottsdale,tucson,philadelphia,pittsburgh,sanantonio,sandiego,la-jolla,dallas,fortworth,austin,sanjose,siliconvalley,sanfrancisco,berkeley,oakland,paloalto,mountainview,cupertino,sunnyvale,santaclara,stanford,jacksonville,columbus,charlotte,indianapolis,seattle,bellevue,redmond,tacoma,denver,boulder,washingtondc,d.c.,baltimore,boston,eastboston,suffolkcounty,midlesexcounty,middlesexcounty,harvard,mit,elpaso,nashville,memphis,detroit,annarbor,portland,lasvegas,reno,oklahomacity,louisville,milwaukee,madison,albuquerque,santafe,fresno,sacramento,kansascity,mesa,atlanta,omaha,coloradosprings,raleigh,durham,miami,orlando,tampa,keywest,minneapolis,st.paul,tulsa,cleveland,cincinnati,wichita,neworleans,orleansparish,storyville,kenner,jeffersonparish,batonrouge,honolulu,waikiki,maui,anchorage,saltlakecity,providence,newhaven,princeton,ithaca,加利福尼亚,加州,德克萨斯,德州,佛罗里达,佛州,纽约,紐約,宾夕法尼亚,伊利诺伊,俄亥俄,佐治亚,密歇根,新泽西,弗吉尼亚,华盛顿州,亚利桑那,马萨诸塞,麻省,路易斯安那,俄勒冈,科罗拉多,犹他,内华达,夏威夷,阿拉斯加,纽约市,曼哈顿,布鲁克林,洛杉矶,洛杉磯,好莱坞,芝加哥,休斯顿,休士頓,菲尼克斯,凤凰城,费城,圣安东尼奥,圣地亚哥,圣迭戈,达拉斯,奥斯汀,圣何塞,硅谷,旧金山,舊金山,三藩市,伯克利,帕罗奥图,西雅图,西雅圖,丹佛,华盛顿,華盛頓,巴尔的摩,波士顿,波士頓,底特律,波特兰,拉斯维加斯,迈阿密,邁阿密,奥兰多,亚特兰大,明尼阿波利斯,新奥尔良,紐奧良,檀香山,盐湖城,ニューヨーク,ロサンゼルス,シカゴ,ヒューストン,サンフランシスコ,シアトル,ボストン,ワシントン,ラスベガス,マイアミ,ニューオーリンズ,ハワイ,ホノルル,뉴욕,로스앤젤레스,시카고,휴스턴,샌프란시스코,시애틀,보스턴,워싱턴,라스베이거스,마이애미,뉴올리언스,하와이,호놀룰루,orleans,francisco,angeles,vegas,diego,jose,antonio,得克萨斯,ルイジアナ,カリフォルニア,テキサス,フロリダ,루이지애나,캘리포니아,텍사스,플로리다,kalifornien,californie,floride,นิวออร์ลีนส์,แคลิฟอร์เนีย,ฮาวาย",
	"@geo:cc:ca|@geo:cont:am:canada,canadian,kanada,canadá,加拿大,カナダ,캐나다,แคนาดา,ontario,quebec,québec,britishcolumbia,alberta,manitoba,saskatchewan,novascotia,toronto,montreal,montréal,vancouver,calgary,edmonton,ottawa,winnipeg,quebeccity,waterloo,banff,whistler,halifax,多伦多,多倫多,蒙特利尔,蒙特婁,温哥华,溫哥華,卡尔加里,埃德蒙顿,渥太华,渥太華,温尼伯,魁北克,班夫,滑铁卢,トロント,モントリオール,バンクーバー,オタワ,カルガリー,토론토,몬트리올,밴쿠버,오타와,캘거리",
	"@geo:cc:mx|@geo:cont:am:mexico,mexican,méxico,mexiko,mexique,墨西哥,メキシコ,멕시코,เม็กซิโก,mexicocity,cdmx,guadalajara,monterrey,puebla,tijuana,cancun,cancún,tulum,oaxaca,墨西哥城,坎昆,瓜达拉哈拉,蒙特雷,メキシコシティ,カンクン,멕시코시티,칸쿤",
	"@geo:cc:br|@geo:cont:am:brazil,brazilian,brasil,brasilien,brésil,巴西,ブラジル,브라질,บราซิล,saopaulo,sãopaulo,riodejaneiro,brasilia,brasília,salvador,fortaleza,belohorizonte,manaus,curitiba,recife,圣保罗,聖保羅,里约热内卢,里約熱內盧,巴西利亚,サンパウロ,リオデジャネイロ,상파울루,리우데자네이루",
	"@geo:cc:ar|@geo:cont:am:argentina,argentinian,argentine,argentinien,阿根廷,アルゼンチン,아르헨티나,buenosaires,rosario,mendoza,bariloche,patagonia,ushuaia,布宜诺斯艾利斯,ブエノスアイレス,부에노스아이레스",
	"@geo:cc:cl|@geo:cont:am:chile,chilean,chili,智利,チリ,칠레,valparaíso,atacama,智利圣地亚哥",
	"@geo:cc:co|@geo:cont:am:colombia,colombian,哥伦比亚,コロンビア,콜롬비아,bogota,bogotá,medellin,medellín,cartagena,波哥大,麦德林",
	"@geo:cc:pe|@geo:cont:am:peru,peruvian,perú,秘鲁,祕魯,ペルー,페루,lima,cusco,cuzco,machupicchu,利马,库斯科,马丘比丘",
	"@geo:cc:eg|@geo:cont:af:egypt,egyptian,ägypten,égypte,egipto,egitto,misr,埃及,エジプト,이집트,египет,อียิปต์,cairo,greatercairo,giza,alexandria,luxor,aswan,sharmelsheikh,hurghada,sinai,nile,karnak,pyramids,heliopolis,zamalek,maadi,开罗,開羅,吉萨,吉薩,亚历山大,卢克索,阿斯旺,沙姆沙伊赫,赫尔格达,尼罗河,金字塔,カイロ,ギザ,アレクサンドリア,ルクソール,アスワン,ナイル川,카이로,기자,알렉산드리아,룩소르,아스완,나일강,sharm,cataract,西奈,卡纳克,シナイ,ナイル,カルナック,시나이,나일,카르나크,kairo,nil,louxor,assouan,lúxor,asuán,nilo,ไคโร,กีซา,ลักซอร์,อัสวาน,แม่น้ำไนล์",
	"@geo:cc:za|@geo:cont:af:southafrica,southafrican,南非,南アフリカ,남아프리카,남아공,johannesburg,capetown,durban,pretoria,kruger,约翰内斯堡,开普敦,德班,比勒陀利亚,ヨハネスブルグ,ケープタウン,요하네스버그,케이프타운",
	"@geo:cc:ma|@geo:cont:af:morocco,moroccan,maroc,marokko,marruecos,摩洛哥,モロッコ,모로코,casablanca,marrakesh,marrakech,rabat,fez,fes,tangier,chefchaouen,卡萨布兰卡,马拉喀什,拉巴特,非斯,丹吉尔",
	"@geo:cc:ke|@geo:cont:af:kenya,kenyan,肯尼亚,肯亞,ケニア,케냐,nairobi,mombasa,masaimara,内罗毕,蒙巴萨,ナイロビ,나이로비",
	"@geo:cc:ng|@geo:cont:af:nigeria,nigerian,尼日利亚,ナイジェリア,나이지리아,lagos,abuja,拉各斯,阿布贾",
	"@geo:cc:et|@geo:cont:af:ethiopia,ethiopian,埃塞俄比亚,エチオピア,에티오피아,addisababa,亚的斯亚贝巴",
	"@geo:cc:tz|@geo:cont:af:tanzania,tanzanian,坦桑尼亚,タンザニア,탄자니아,daressalaam,zanzibar,serengeti,kilimanjaro,桑给巴尔,乞力马扎罗",
	"@geo:cc:au|@geo:cont:oc:australia,australian,aussie,australien,australie,澳大利亚,澳大利亞,澳洲,オーストラリア,豪州,호주,오스트레일리아,ออสเตรเลีย,nsw,newsouthwales,victoria,queensland,westernaustralia,southaustralia,tasmania,sydney,melbourne,brisbane,perth,adelaide,goldcoast,canberra,wollongong,hobart,darwin,cairns,悉尼,雪梨,墨尔本,墨爾本,布里斯班,珀斯,伯斯,阿德莱德,黄金海岸,堪培拉,坎培拉,霍巴特,达尔文,凯恩斯,塔斯马尼亚,シドニー,メルボルン,ブリスベン,パース,アデレード,キャンベラ,ゴールドコースト,시드니,멜버른,브리즈번,퍼스,애들레이드,캔버라,골드코스트",
	"@geo:cc:nz|@geo:cont:oc:newzealand,kiwi,aotearoa,neuseeland,nouvelle-zélande,新西兰,紐西蘭,ニュージーランド,뉴질랜드,นิวซีแลนด์,auckland,wellington,christchurch,queenstown,hamilton,dunedin,rotorua,奥克兰,奧克蘭,惠灵顿,威靈頓,基督城,皇后镇,罗托鲁瓦,オークランド,ウェリントン,クライストチャーチ,クイーンズタウン,오클랜드,웰링턴,크라이스트처치,퀸스타운",
	"@geo:cc:al|@geo:cont:eu:albania,albanian,shqipëri,tirana,tiranë,durrës,阿尔巴尼亚,地拉那,アルバニア,알바니아",
	"@geo:cc:ba|@geo:cont:eu:bosnia,herzegovina,bosnian,sarajevo,mostar,波黑,萨拉热窝,ボスニア,보스니아",
	"@geo:cc:mk|@geo:cont:eu:macedonia,northmacedonia,skopje,ohrid,北马其顿,斯科普里",
	"@geo:cc:me|@geo:cont:eu:montenegro,podgorica,kotor,budva,黑山,波德戈里察,科托尔",
	"@geo:cc:by|@geo:cont:eu:belarus,belarusian,minsk,gomel,白俄罗斯,明斯克,ベラルーシ,벨라루스",
	"@geo:cc:md|@geo:cont:eu:moldova,chișinău,chisinau,摩尔多瓦,基希讷乌",
	"@geo:cc:ad|@geo:cont:eu:andorra,安道尔,アンドラ,안도라",
	"@geo:cc:li|@geo:cont:eu:liechtenstein,vaduz,列支敦士登,瓦杜兹",
	"@geo:cc:sm|@geo:cont:eu:sanmarino,圣马力诺",
	"@geo:cc:va|@geo:cont:eu:vatican,vaticano,holysee,梵蒂冈,バチカン,바티칸",
	"@geo:cc:ge|@geo:cont:as:tbilisi,batumi,kutaisi,格鲁吉亚,第比利斯,ジョージア,조지아",
	"@geo:cc:am|@geo:cont:as:armenia,armenian,yerevan,亚美尼亚,埃里温,アルメニア,아르메니아",
	"@geo:cc:az|@geo:cont:as:azerbaijan,baku,阿塞拜疆,巴库,アゼルバイジャン,아제르바이잔",
	"@geo:cc:kz|@geo:cont:as:kazakhstan,kazakh,astana,almaty,shymkent,哈萨克斯坦,阿斯塔纳,阿拉木图,カザフスタン,카자흐스탄",
	"@geo:cc:uz|@geo:cont:as:uzbekistan,uzbek,tashkent,samarkand,bukhara,乌兹别克斯坦,塔什干,撒马尔罕,ウズベキスタン,우즈베키스탄",
	"@geo:cc:kg|@geo:cont:as:kyrgyzstan,bishkek,osh,吉尔吉斯斯坦,比什凯克",
	"@geo:cc:tj|@geo:cont:as:tajikistan,dushanbe,塔吉克斯坦,杜尚别",
	"@geo:cc:tm|@geo:cont:as:turkmenistan,ashgabat,土库曼斯坦,阿什哈巴德",
	"@geo:cc:mn|@geo:cont:as:mongolia,mongolian,ulaanbaatar,ulanbator,蒙古,蒙古国,乌兰巴托,モンゴル,ウランバートル,몽골,울란바토르",
	"@geo:cc:bd|@geo:cont:as:bangladesh,bangladeshi,dhaka,chittagong,孟加拉,孟加拉国,达卡,バングラデシュ,방글라데시",
	"@geo:cc:bt|@geo:cont:as:bhutan,thimphu,paro,不丹,廷布",
	"@geo:cc:mv|@geo:cont:as:maldives,maldivian,malé,马尔代夫,馬爾地夫,马累,モルディブ,몰디브",
	"@geo:cc:bn|@geo:cont:as:brunei,bandarseribegawan,文莱,汶萊,斯里巴加湾,ブルネイ,브루나이",
	"@geo:cc:kw|@geo:cont:as:kuwait,kuwaitcity,科威特,科威特城,クウェート,쿠웨이트",
	"@geo:cc:om|@geo:cont:as:oman,omani,muscat,salalah,阿曼,马斯喀特,オマーン,오만",
	"@geo:cc:bh|@geo:cont:as:bahrain,manama,巴林,麦纳麦,バーレーン,바레인",
	"@geo:cc:lb|@geo:cont:as:lebanon,lebanese,beirut,byblos,黎巴嫩,贝鲁特,レバノン,ベイルート,레바논,베이루트",
	"@geo:cc:ir|@geo:cont:as:iran,iranian,persia,persian,tehran,isfahan,shiraz,tabriz,伊朗,德黑兰,伊斯法罕,设拉子,イラン,テヘラン,이란,테헤란",
	"@geo:cc:iq|@geo:cont:as:iraq,iraqi,baghdad,basra,erbil,伊拉克,巴格达,イラク,バグダッド,이라크,바그다드",
	"@geo:cc:sy|@geo:cont:as:syria,syrian,damascus,aleppo,叙利亚,大马士革,シリア,시리아",
	"@geo:cc:af|@geo:cont:as:afghanistan,afghan,kabul,herat,阿富汗,喀布尔,アフガニスタン,아프가니스탄",
	"@geo:cc:cu|@geo:cont:am:cuba,cuban,havana,habana,varadero,古巴,哈瓦那,キューバ,ハバナ,쿠바,아바나",
	"@geo:cc:jm|@geo:cont:am:jamaica,jamaican,kingston,montegobay,牙买加,金斯敦,ジャマイカ,자메이카",
	"@geo:cc:cr|@geo:cont:am:costarica,sanjosé,哥斯达黎加,コスタリカ,코스타리카",
	"@geo:cc:pa|@geo:cont:am:panama,panamacity,巴拿马,巴拿马城,パナマ,파나마",
	"@geo:cc:uy|@geo:cont:am:uruguay,montevideo,puntadeleste,乌拉圭,蒙得维的亚,ウルグアイ,우루과이",
	"@geo:cc:ec|@geo:cont:am:ecuador,quito,guayaquil,galapagos,galápagos,厄瓜多尔,基多,加拉帕戈斯,エクアドル,에콰도르",
	"@geo:cc:bo|@geo:cont:am:bolivia,lapaz,sucre,uyuni,玻利维亚,拉巴斯,乌尤尼,ボリビア,볼리비아",
	"@geo:cc:ve|@geo:cont:am:venezuela,caracas,委内瑞拉,加拉加斯,ベネズエラ,베네수엘라",
	"@geo:cc:do|@geo:cont:am:dominicanrepublic,santodomingo,puntacana,多米尼加,圣多明各",
	"@geo:cc:pr|@geo:cont:am:puertorico,sanjuan,波多黎各,圣胡安",
	"@geo:cc:tn|@geo:cont:af:tunisia,tunisian,tunis,carthage,sousse,突尼斯,迦太基,チュニジア,튀니지",
	"@geo:cc:dz|@geo:cont:af:algeria,algerian,algiers,oran,阿尔及利亚,阿尔及尔,アルジェリア,알제리",
	"@geo:cc:gh|@geo:cont:af:ghana,ghanaian,accra,kumasi,加纳,阿克拉,ガーナ,가나",
	"@geo:cc:sn|@geo:cont:af:senegal,dakar,塞内加尔,达喀尔,セネガル,세네갈",
	"@geo:cc:rw|@geo:cont:af:rwanda,kigali,卢旺达,基加利,ルワンダ,르완다",
	"@geo:cc:mu|@geo:cont:af:mauritius,portlouis,毛里求斯,路易港,モーリシャス,모리셔스",
	"@geo:cc:ug|@geo:cont:af:uganda,kampala,entebbe,乌干达,坎帕拉",
	"@geo:cc:zw|@geo:cont:af:zimbabwe,harare,victoriafalls,津巴布韦,哈拉雷,维多利亚瀑布",
	"@geo:cc:na|@geo:cont:af:namibia,windhoek,纳米比亚,温得和克",
	"@geo:cc:bw|@geo:cont:af:botswana,gaborone,okavango,博茨瓦纳,哈博罗内",
	"@geo:cc:fj|@geo:cont:oc:fiji,fijian,suva,nadi,斐济,斐濟,苏瓦,楠迪,フィジー,피지",
	"@geo:cc:pg|@geo:cont:oc:papuanewguinea,portmoresby,巴布亚新几内亚,莫尔兹比港",
	"@geo:cc:pf|@geo:cont:oc:tahiti,borabora,frenchpolynesia,大溪地,塔希提,波拉波拉",
	"@kin:parent:mother|@kin:family:mom,mum,mommy,mummy,mama,mother,mothers,maternal,妈妈,媽媽,母亲,母親,老妈,老媽,娘亲,母上,岳母,婆婆,丈母娘,母,お母さん,ママ,母親,엄마,어머니,모친,mutter,mutti,maman,mere,mère,madre,mamá,แม่,มารดา",
	"@kin:parent:father|@kin:family:dad,daddy,papa,father,fathers,paternal,爸爸,父亲,父親,老爸,爹,父上,岳父,公公,老丈人,父,お父さん,パパ,父親,아빠,아버지,부친,vater,vati,père,padre,papá,พ่อ,บิดา",
	"@kin:parent|@kin:family:parent,parents,folks,双亲,雙親,父母,爸妈,爸媽,爹娘,両親,부모,부모님,eltern,padres",
	"@kin:spouse|@kin:family:wife,wives,husband,husbands,spouse,spouses,fiance,fiancé,fiancee,fiancée,newlywed,bride,groom,妻子,老婆,太太,夫人,媳妇,爱人,愛人,丈夫,老公,先生,伴侣,伴侶,配偶,未婚夫,未婚妻,新娘,新郎,妻,夫,嫁,旦那,奥さん,主人,配偶者,아내,남편,배우자,와이프,신부,신랑,ehefrau,ehemann,gattin,épouse,époux,mari,femme,esposo,esposa,ภรรยา,สามี",
	"@kin:sibling|@kin:family:brother,brothers,bro,sister,sisters,sis,sibling,siblings,twin,twins,哥哥,弟弟,姐姐,妹妹,兄长,兄弟,姐妹,姊妹,姐弟,兄妹,胞兄,胞弟,双胞胎,雙胞胎,兄,弟,姉,妹,お兄さん,お姉さん,兄弟,姉妹,형,오빠,동생,누나,언니,남동생,여동생,형제,자매,bruder,schwester,geschwister,frère,sœur,hermano,hermana,พี่ชาย,น้องชาย,พี่สาว,น้องสาว",
	"@kin:child|@kin:family:son,sons,daughter,daughters,child,children,kid,kids,toddler,infant,newborn,offspring,儿子,兒子,女儿,女兒,孩子,小孩,子女,儿女,兒女,宝宝,寶寶,婴儿,嬰兒,闺女,小子,息子,娘,子供,子ども,赤ちゃん,아들,딸,아이,자녀,아기,sohn,tochter,kinder,fils,fille,enfant,hijo,hija,niño,ลูกชาย,ลูกสาว,ลูก",
	"@kin:grandparent|@kin:family:grandpa,grandfather,granddad,grandma,grandmother,granny,nana,grandparent,grandparents,grandchild,grandchildren,grandson,granddaughter,爷爷,爺爺,奶奶,外公,外婆,姥爷,姥姥,祖父,祖母,外祖父,外祖母,孙子,孫子,孙女,外孙,外孙女,祖父,祖母,おじいさん,おばあさん,孫,할아버지,할머니,외할아버지,외할머니,손자,손녀,großvater,großmutter,oma,opa,grand-père,grand-mère,abuelo,abuela,ปู่,ย่า,ตา,ยาย",
	"@kin:extended|@kin:family:uncle,uncles,aunt,aunts,auntie,cousin,cousins,nephew,nephews,niece,nieces,in-law,in-laws,relative,relatives,kin,clan,叔叔,伯伯,伯父,叔父,舅舅,姑姑,姑妈,姨妈,阿姨,表哥,表姐,表弟,表妹,堂哥,堂姐,堂弟,堂妹,侄子,侄女,外甥,外甥女,亲戚,親戚,家人,家属,家庭,叔父,伯父,叔母,伯母,おじ,おば,いとこ,甥,姪,親戚,家族,삼촌,이모,고모,숙모,사촌,조카,친척,가족,onkel,tante,cousine,neffe,nichte,familie",
	"@role:colleague|@dim:social:colleague,colleagues,coworker,coworkers,co-worker,teammate,teammates,workmate,collaborator,coauthor,co-author,boss,manager,supervisor,intern,同事,同僚,队友,隊友,搭档,搭檔,合作者,合著者,上司,主管,经理,老板,老闆,实习生,同僚,上司,部下,先輩,後輩,동료,선배,후배,상사,팀원,kollege,kollegin,mitarbeiter,chef,collègue,compañero,เพื่อนร่วมงาน",
	"@role:academic|@dim:social:professor,prof,phd,postdoc,researcher,scientist,scholar,advisor,supervisor,mentor,student,classmate,schoolmate,alumni,dean,lecturer,教授,导师,導師,博导,博士,博士后,研究员,科学家,学者,学生,學生,同学,同學,校友,师兄,师姐,师弟,师妹,讲师,院长,教授,先生,研究者,学生,同級生,교수,박사,연구원,학생,동창,동문,선생님,professorin,dozent,étudiant,profesor,ศาสตราจารย์,นักศึกษา",
	"@role:medical|@dim:social:doctor,doctors,physician,surgeon,dentist,orthodontist,nurse,pharmacist,therapist,psychiatrist,psychologist,pediatrician,optometrist,vet,veterinarian,clinic,hospital,pharmacy,医生,醫生,大夫,医师,外科医生,牙医,牙醫,护士,護士,药剂师,心理医生,兽医,獸醫,诊所,診所,医院,醫院,药房,药店,医者,医師,歯医者,歯科医,看護師,病院,クリニック,薬局,의사,치과의사,간호사,병원,치과,약국,arzt,ärztin,zahnarzt,krankenhaus,apotheke,médecin,dentiste,hôpital,médico,หมอ,แพทย์,ทันตแพทย์,โรงพยาบาล,dental,tooth,teeth,periodontal,orthodontic,cavity,radiograph,surgery,surgical,checkup,牙科,牙齿,洗牙,洁牙,补牙,拔牙,牙周,正畸,手术,体检,歯科,歯,歯周病,手術,健診,치아,치주,수술,검진,zahnmedizin,zahn,zähne,zahnärztlich,dentaire,dent,dents,chirurgie,diente,dientes,cirugía,ทันตกรรม,ฟัน,ผ่าตัด,ตรวจสุขภาพ",
	"@role:friend|@dim:social:friend,friends,bestie,buddy,pal,roommate,flatmate,housemate,neighbor,neighbour,boyfriend,girlfriend,朋友,好友,闺蜜,閨蜜,死党,发小,室友,舍友,邻居,鄰居,男朋友,男友,女朋友,女友,友達,友人,親友,ルームメイト,隣人,彼氏,彼女,친구,절친,룸메이트,이웃,남자친구,여자친구,freund,freundin,mitbewohner,nachbar,ami,amie,amigo,เพื่อน,รูมเมท",
	"@cal:month:01|@cal:season:winter:january,jan,一月,1月,正月,元月,1월,일월,januar,janvier,enero,มกราคม",
	"@cal:month:02|@cal:season:winter:february,feb,二月,2月,2월,이월,februar,février,febrero,กุมภาพันธ์",
	"@cal:month:03|@cal:season:spring:march,mar,三月,3月,3월,삼월,märz,mars,marzo,มีนาคม",
	"@cal:month:04|@cal:season:spring:april,apr,四月,4月,4월,사월,avril,abril,เมษายน",
	"@cal:month:05|@cal:season:spring:五月,5月,5월,오월,mai,mayo,พฤษภาคม",
	"@cal:month:06|@cal:season:summer:june,jun,六月,6月,6월,유월,juni,juin,junio,มิถุนายน",
	"@cal:month:07|@cal:season:summer:july,jul,七月,7月,7월,칠월,juli,juillet,julio,กรกฎาคม",
	"@cal:month:08|@cal:season:summer:august,aug,八月,8月,8월,팔월,août,agosto,สิงหาคม",
	"@cal:month:09|@cal:season:autumn:september,sep,sept,九月,9月,9월,구월,septembre,septiembre,กันยายน",
	"@cal:month:10|@cal:season:autumn:october,oct,十月,10月,10월,시월,oktober,octobre,octubre,ตุลาคม",
	"@cal:month:11|@cal:season:autumn:november,nov,十一月,11月,11월,십일월,novembre,noviembre,พฤศจิกายน",
	"@cal:month:12|@cal:season:winter:december,dec,十二月,12月,腊月,12월,십이월,dezember,décembre,diciembre,ธันวาคม",
	"@cal:season:spring|@dim:time:spring,springtime,春天,春季,春日,开春,春,봄,frühling,printemps,primavera,ฤดูใบไม้ผลิ",
	"@cal:season:summer|@dim:time:summer,summertime,midsummer,夏天,夏季,夏日,盛夏,夏,여름,sommer,été,verano,ฤดูร้อน",
	"@cal:season:autumn|@dim:time:autumn,autumnal,秋天,秋季,秋日,金秋,秋,가을,herbst,automne,otoño,ฤดูใบไม้ร่วง",
	"@cal:season:winter|@dim:time:winter,wintertime,midwinter,冬天,冬季,冬日,寒冬,冬,겨울,hiver,invierno,ฤดูหนาว",
	"@cal:dow:weekday|@dim:time:monday,mon,tuesday,tue,tues,wednesday,wed,thursday,thu,thur,thurs,friday,fri,weekday,weekdays,周一,周二,周三,周四,周五,星期一,星期二,星期三,星期四,星期五,礼拜一,礼拜二,礼拜三,礼拜四,礼拜五,工作日,平日,月曜日,火曜日,水曜日,木曜日,金曜日,月曜,火曜,水曜,木曜,金曜,월요일,화요일,수요일,목요일,금요일,평일,montag,dienstag,mittwoch,donnerstag,freitag,lundi,mardi,mercredi,jeudi,vendredi,lunes,martes,miércoles,jueves,viernes",
	"@cal:dow:weekend|@dim:time:saturday,sat,sunday,sun,weekend,weekends,周六,周日,周天,星期六,星期日,星期天,礼拜六,礼拜日,礼拜天,周末,週末,双休日,土曜日,日曜日,土曜,日曜,週末,토요일,일요일,주말,samstag,sonntag,wochenende,samedi,dimanche,sábado,domingo,สุดสัปดาห์",
	"@cal:holiday|@dim:time:christmas,xmas,christmasday,christmaseve,easter,halloween,thanksgiving,newyear,newyears,valentine,valentines,圣诞,圣诞节,聖誕節,平安夜,复活节,万圣节,感恩节,元旦,新年,春节,春節,除夕,中秋,中秋节,端午,端午节,清明,国庆,国庆节,情人节,クリスマス,正月,大晦日,バレンタイン,ハロウィン,크리스마스,설날,추석,신정,weihnachten,ostern,silvester,noël,navidad",
	"@bio:animal:cat|@bio:pet:cat,cats,kitten,kittens,kitty,feline,tabby,shorthair,longhair,persian,siamese,ragdoll,maine-coon,calico,catgrass,猫,貓,猫咪,貓咪,小猫,幼猫,喵星人,橘猫,英短,美短,布偶猫,暹罗猫,猫草,猫粮,猫砂,ネコ,猫ちゃん,子猫,고양이,새끼고양이,냥이,katze,kätzchen,kater,chat,chaton,gato,gatito,แมว,ลูกแมว",
	"@bio:animal:dog|@bio:pet:dog,dogs,puppy,puppies,pup,canine,hound,retriever,goldenretriever,labrador,corgi,poodle,husky,bulldog,beagle,shepherd,chihuahua,shiba,dachshund,pug,terrier,collie,spaniel,狗,狗狗,小狗,幼犬,犬,汪星人,金毛,拉布拉多,柯基,泰迪,贵宾犬,哈士奇,柴犬,牧羊犬,德牧,边牧,法斗,比熊,博美,雪纳瑞,狗粮,遛狗,イヌ,犬,子犬,柴犬,개,강아지,멍멍이,반려견,시바견,골든리트리버,웰시코기,푸들,hund,welpe,chien,chiot,perro,cachorro,สุนัข,หมา,ลูกหมา",
	"@bio:pet|@dim:bio:pet,pets,hamster,guineapig,ferret,bunny,rabbit,rabbits,parrot,parakeets,goldfish,aquarium,leash,collar,litterbox,宠物,寵物,毛孩子,仓鼠,倉鼠,豚鼠,荷兰猪,兔子,小兔,鹦鹉,鸚鵡,金鱼,水族箱,鱼缸,牵引绳,猫窝,狗窝,ペット,うさぎ,ハムスター,インコ,金魚,반려동물,애완동물,토끼,햄스터,앵무새,금붕어,haustier,kaninchen,hamster,mascota,สัตว์เลี้ยง,กระต่าย",
	"@bio:animal:bovine|@dim:bio:cow,cows,cattle,calf,calves,bull,bulls,ox,oxen,bison,buffalo,yak,herd,herded,herding,pasture,grazing,dairy,牛,奶牛,黄牛,水牛,牦牛,小牛,牛群,放牛,牧场,牧場,草场,ウシ,牛,乳牛,和牛,소,젖소,한우,송아지,kuh,kühe,rind,rinder,kalb,stier,vache,bœuf,troupeau,vaca,toro,ganado,วัว,โค,ควาย",
	"@bio:animal:equine|@dim:bio:horse,horses,pony,ponies,stallion,mare,foal,donkey,mule,camel,camels,alpaca,llama,sheep,lamb,flock,goat,goats,pig,pigs,boar,马,馬,小马,骏马,骑马,驴,骆驼,駱駝,羊驼,羊,绵羊,山羊,小羊,羊群,猪,豬,野猪,ウマ,馬,羊,ヒツジ,ラクダ,ブタ,말,양,낙타,염소,돼지,pferd,schaf,ziege,kamel,schwein,cheval,mouton,chameau,caballo,oveja,ម้า,แกะ,อูฐ",
	"@bio:animal:wildlife|@dim:bio:bird,birds,pigeon,seagull,duck,ducks,goose,geese,swan,swans,eagle,owl,penguin,penguins,peacock,flamingo,chicken,rooster,hen,deer,stag,reindeer,squirrel,fox,wolf,bear,panda,koala,kangaroo,elephant,lion,tiger,monkey,ape,giraffe,zebra,hippo,rhino,whale,dolphin,shark,seal,turtle,tortoise,frog,snake,butterfly,bee,鸟,鳥,小鸟,鸽子,海鸥,鸭,鸭子,鹅,天鹅,老鹰,猫头鹰,企鹅,孔雀,火烈鸟,鸡,公鸡,母鸡,鹿,梅花鹿,驯鹿,松鼠,狐狸,狼,熊,熊猫,大熊猫,考拉,袋鼠,大象,狮子,老虎,猴子,长颈鹿,斑马,河马,犀牛,鲸鱼,海豚,鲨鱼,海豹,乌龟,海龟,青蛙,蝴蝶,蜜蜂,鳥,ハト,カモ,白鳥,ペンギン,シカ,リス,パンダ,ゾウ,ライオン,トラ,サル,クジラ,イルカ,새,비둘기,오리,백조,펭귄,사슴,다람쥐,판다,코끼리,사자,호랑이,원숭이,고래,돌고래,vogel,hirsch,bär,löwe,와일드라이프,นก,ช้าง,เสือ,ลิง",
	"@bio:plant|@dim:bio:tree,trees,forest,woods,pine,oak,maple,palm,cherryblossom,sakura,bamboo,flower,flowers,rose,roses,tulip,sunflower,lotus,orchid,lavender,grass,lawn,moss,mushroom,mushrooms,fungi,truffle,cactus,succulent,garden,botanical,树,樹,树木,森林,树林,松树,橡树,枫树,枫叶,棕榈树,樱花,櫻花,竹,竹子,花,鲜花,玫瑰,郁金香,向日葵,荷花,兰花,薰衣草,草,草坪,草地,蘑菇,菌子,野生菌,松露,仙人掌,多肉,花园,植物园,木,樹木,森,桜,さくら,竹,花,バラ,チューリップ,ひまわり,芝生,きのこ,キノコ,庭園,植物園,나무,숲,벚꽃,대나무,꽃,장미,튤립,해바라기,잔디,버섯,정원,식물원,baum,bäume,wald,blume,pilz,pilze,garten,arbre,fleur,champignon,jardin,árbol,flor,ต้นไม้,ดอกไม้,เห็ด",
	"@obj:transit:air|@dim:travel:flight,flights,airplane,aeroplane,aircraft,airliner,plane,planes,jet,aviation,airline,airlines,airways,airport,airports,terminal,runway,tarmac,boarding,boardingpass,gate,baggage,luggage,carousel,layover,stopover,cockpit,cabin,inflight,cathay,lufthansa,ryanair,easyjet,delta,unitedairlines,britishairways,airfrance,emirates,航班,飞机,飛機,客机,航空,航空公司,机场,機場,航站楼,跑道,登机,登机牌,登机口,行李,行李转盘,转机,经停,机舱,头等舱,商务舱,经济舱,机票,飛行機,航空機,フライト,空港,ターミナル,滑走路,搭乗,搭乗券,手荷物,항공기,비행기,항공편,공항,터미널,활주로,탑승,탑승권,수하물,flug,flugzeug,flughafen,fluggesellschaft,gepäck,vol,avion,aéroport,bagage,vuelo,avión,aeropuerto,เที่ยวบิน,เครื่องบิน,สนามบิน",
	"@obj:transit:rail|@dim:travel:train,trains,railway,railroad,rail,locomotive,carriage,coach,platform,station,trainstation,highspeedrail,shinkansen,eurostar,tgv,amtrak,subway,underground,tube,metro,tram,streetcar,trolley,lightrail,火车,火車,列车,列車,高铁,高鐵,动车,動車,新干线,铁路,鐵路,火车站,站台,月台,车厢,车票,火车票,有轨电车,电车,轻轨,電車,列車,鉄道,新幹線,駅,ホーム,地下鉄,路面電車,기차,열차,고속열차,KTX,철도,기차역,지하철,전철,트램,zug,bahn,bahnhof,straßenbahn,u-bahn,s-bahn,gare,métro,tramway,tren,estación,รถไฟ,สถานีรถไฟ,รถไฟฟ้า",
	"@obj:transit:road|@dim:travel:taxi,taxis,cab,cabs,uber,lyft,didi,grab,bolt,rideshare,car,cars,automobile,sedan,suv,van,minivan,truck,lorry,bus,buses,coach,shuttle,motorcoach,driver,chauffeur,parking,garage,gasstation,petrol,toll,highway,freeway,motorway,traffic,出租车,出租車,的士,计程车,計程車,网约车,打车,打的,滴滴,优步,汽车,汽車,轿车,小车,越野车,面包车,卡车,货车,公交,公交车,巴士,大巴,班车,摆渡车,司机,停车场,加油站,油泵,高速,高速公路,堵车,タクシー,車,自動車,バス,駐車場,ガソリンスタンド,高速道路,택시,자동차,승용차,버스,셔틀,주차장,주유소,고속도로,auto,wagen,parkplatz,tankstelle,autobahn,voiture,station-service,autoroute,coche,autobús,รถแท็กซี่,รถยนต์,รถบัส",
	"@obj:transit:bike|@dim:travel:bicycle,bicycles,bike,bikes,cycling,cyclist,scooter,e-bike,ebike,motorbike,motorcycle,moped,helmet,handlebar,pedal,tire,tyre,wheel,自行车,自行車,单车,單車,脚踏车,共享单车,骑行,骑车,滑板车,电动车,电瓶车,摩托车,摩天轮,车轮,轮胎,头盔,修车,自転車,サイクリング,バイク,スクーター,자전거,사이클,오토바이,스쿠터,fahrrad,rad,motorrad,vélo,bicyclette,moto,bicicleta,จักรยาน,มอเตอร์ไซค์",
	"@obj:transit:marine|@dim:travel:ship,ships,boat,boats,ferry,cruise,cruiseship,yacht,sailboat,vessel,harbor,harbour,port,pier,dock,wharf,marina,deck,voyage,船,轮船,游轮,邮轮,郵輪,渡轮,游艇,帆船,小船,港口,码头,碼頭,甲板,游船,船,フェリー,クルーズ,ヨット,港,배,선박,유람선,크루즈,페리,요트,항구,부두,schiff,boot,fähre,kreuzfahrt,hafen,bateau,croisière,port,barco,crucero,เรือ,ท่าเรือ",
	"@venue:lodging|@dim:travel:hotel,hotels,inn,resort,resorts,hostel,motel,lodge,chalet,villa,guesthouse,b&b,airbnb,accommodation,accommodations,lodging,booking,reservation,check-in,checkin,checkout,check-out,suite,bedroom,lobby,reception,concierge,balcony,terrace,hilton,marriott,sheraton,hyatt,intercontinental,novotel,sofitel,ibis,westin,ritz,four-seasons,holidayinn,crowne,酒店,宾馆,賓館,旅馆,旅館,旅店,度假村,民宿,青旅,客栈,住宿,下榻,预订,預訂,入住,退房,套房,客房,房间,大堂,前台,阳台,陽台,露台,希尔顿,万豪,喜来登,凯悦,洲际,诺富特,索菲特,ホテル,旅館,宿,宿泊,予約,チェックイン,客室,バルコニー,호텔,숙소,숙박,리조트,펜션,민박,예약,체크인,객실,발코니,unterkunft,reservierung,zimmer,balkon,hôtel,hébergement,chambre,alojamiento,habitación,โรงแรม,ที่พัก,รีสอร์ท",
	"@venue:dining|@dim:food:restaurant,restaurants,bistro,brasserie,trattoria,izakaya,diner,eatery,canteen,cafeteria,foodcourt,cafe,café,coffeehouse,coffeeshop,teahouse,bakery,patisserie,pizzeria,steakhouse,pub,tavern,lounge,nightclub,餐厅,餐廳,饭店,飯店,餐馆,餐館,酒楼,食堂,茶餐厅,居酒屋,小馆,咖啡馆,咖啡厅,咖啡店,茶馆,奶茶店,面包店,烘焙店,甜品店,披萨店,牛排馆,酒吧,酒馆,夜市,レストラン,食堂,居酒屋,カフェ,喫茶店,パン屋,バー,식당,레스토랑,음식점,카페,커피숍,베이커리,빵집,술집,바,gaststätte,kneipe,bäckerei,boulangerie,restaurante,cafetería,ร้านอาหาร,คาเฟ่",
	"@obj:meal|@dim:food:breakfast,brunch,lunch,dinner,supper,banquet,feast,buffet,picnic,barbecue,bbq,takeout,takeaway,delivery,menu,appetizer,starter,entree,maincourse,side-dish,beverage,drink,drinks,早餐,早饭,早茶,早午餐,午餐,午饭,中餐,晚餐,晚饭,晚宴,聚餐,宵夜,夜宵,自助餐,野餐,烧烤,外卖,菜单,菜單,点菜,饮料,饮品,朝食,朝ご飯,昼食,昼ご飯,ランチ,夕食,晩ご飯,ディナー,ビュッフェ,メニュー,아침식사,점심,점심식사,저녁,저녁식사,만찬,뷔페,메뉴,배달,frühstück,mittagessen,abendessen,speisekarte,petit-déjeuner,déjeuner,dîner,desayuno,almuerzo,cena,อาหารเช้า,อาหารกลางวัน,อาหารเย็น",
	"@obj:food:dish|@dim:food:pizza,pizzas,pasta,spaghetti,ramen,noodle,noodles,udon,soba,sushi,sashimi,tempura,rice,friedrice,risotto,curry,steak,beef,pork,lamb,chicken,fish,seafood,shrimp,prawn,crab,lobster,octopus,squid,oyster,salmon,tuna,soup,hotpot,steampot,dumpling,dumplings,burger,hamburger,sandwich,toast,bread,croissant,salad,cheese,halloumi,egg,omelette,tofu,jam,tomatojam,dessert,desserts,cake,cakes,pastry,pastries,pie,tart,cookie,biscuit,chocolate,icecream,gelato,pudding,mousse,mochi,cream,waffle,pancake,coffee,espresso,latte,cappuccino,tea,milktea,juice,wine,beer,cocktail,whiskey,披萨,比萨,意面,拉面,面条,乌冬面,寿司,壽司,刺身,生鱼片,天妇罗,米饭,炒饭,咖喱,牛排,牛肉,猪肉,羊肉,鸡肉,鱼,海鲜,虾,螃蟹,龙虾,章鱼,鱿鱼,三文鱼,金枪鱼,汤,火锅,火้อ,汽锅,汽锅鸡,菌菇锅,饺子,汉堡,三明治,吐司,面包,牛角包,沙拉,奶酪,芝士,鸡蛋,豆腐,果酱,甜品,甜点,甜點,蛋糕,糕点,饼干,巧克力,冰淇淋,雪糕,布丁,慕斯,麻薯,糯米糍,奶油,华夫饼,松饼,咖啡,拿铁,茶,奶茶,果汁,红酒,葡萄酒,啤酒,鸡尾酒,ピザ,パスタ,ラーメン,うどん,寿司,刺身,ご飯,カレー,ステーキ,海鮮,鍋,餃子,パン,サラダ,チーズ,デザート,ケーキ,アイスクリーム,餅,モチ,コーヒー,お茶,ビール,ワイン,피자,파스타,라면,국수,우동,초밥,스시,사시미,회,밥,카레,스테이크,해산물,전골,훠궈,만두,햄버거,샌드위치,빵,샐러드,치즈,디저트,케이크,아이스크림,모찌,떡,커피,차,맥주,와인,mussel,mussels,scallop,scallops,clam,clams,crepe,crepes,danish,fennel,stew,stewed,charcuterie,chips,fries,kibble,青口,贻贝,扇贝,带子,蛤蜊,可丽饼,丹麦酥,炖菜,薯条,ムール貝,ホタテ,アサリ,クレープ,デニッシュ,シチュー,フライドポテト,홍합,가리비,조개,크레페,스튜,감자튀김,muscheln,jakobsmuscheln,eintopf,pommes,moules,coquilles,crêpe,ragoût,frites,mejillones,vieiras,estofado,papas,หอยแมลงภู่,หอยเชลล์,สตูว์,มันฝรั่งทอด",
	"@obj:doc:receipt|@dim:money:invoice,invoices,receipt,receipts,bill,bills,folio,statement,voucher,coupon,ticket,tickets,fare,fee,tuition,tax,vat,tip,gratuity,payment,paid,pay,charge,charged,cost,costs,price,prices,total,subtotal,amount,balance,refund,reimbursement,reimburse,expense,expenses,budget,salary,wage,rent,mortgage,deposit,transfer,transaction,creditcard,debitcard,visa,mastercard,amex,cash,atm,wallet,bank,发票,發票,收据,收據,账单,賬單,水单,结账单,小票,凭证,代金券,优惠券,票,门票,车票,机票,费用,学费,税,小费,付款,支付,结账,买单,收费,扣款,价格,价钱,花费,花销,开销,总计,总额,合计,余额,退款,报销,報銷,差旅费,预算,工资,薪水,房租,租金,押金,转账,交易,信用卡,刷卡,银行卡,现金,钱包,银行,領収書,レシート,請求書,明細,チケット,料金,代金,支払い,決済,費用,合計,返金,精算,クレジットカード,現金,銀行,영수증,청구서,명세서,계산서,티켓,요금,비용,결제,지불,가격,합계,총액,환불,정산,신용카드,현금,은행,rechnung,quittung,beleg,zahlung,preis,kosten,gesamt,kreditkarte,bargeld,facture,reçu,paiement,prix,factura,recibo,pago,precio,ใบเสร็จ,ใบแจ้งหนี้,ชำระเงิน,ราคา,spend,spends,spent,spending,reimbursing,消费,支出,花钱,出費,経費,지출,소비,경비,ausgeben,ausgaben,erstattung,dépenser,dépense,dépenses,remboursement,gastar,gasto,gastos,reembolso,ใช้จ่าย,ค่าใช้จ่าย,เบิกจ่าย",
	"@unit:curr:usd|@dim:money:usd,us$,$,dollar,dollars,buck,bucks,美元,美金,米ドル,ドル,미국달러,달러,us-dollar,dólar,ดอลลาร์,ดอลลาร์สหรัฐ",
	"@unit:curr:eur|@dim:money:eur,€,euro,euros,欧元,歐元,ユーロ,유로,ยูโร",
	"@unit:curr:gbp|@dim:money:gbp,£,pound,pounds,sterling,英镑,英鎊,ポンド,영국파운드,파운드,pfund,livre,libra,ปอนด์",
	"@unit:curr:chf|@dim:money:chf,franc,francs,瑞郎,瑞士法郎,スイスフラン,스위스프랑,schweizerfranken,francsuisse,francosuizo,ฟรังก์สวิส",
	"@unit:curr:cny|@dim:money:cny,rmb,¥,￥,yuan,人民币,人民幣,人民元,위안,런민비,renminbi,หยวน",
	"@unit:curr:jpy|@dim:money:jpy,yen,日元,円,엔,日圓,엔화,yens,เยน",
	"@unit:curr:hkd|@dim:money:hkd,港币,港元,港幣,香港ドル,홍콩달러,ดอลลาร์ฮ่องกง",
	"@unit:curr:aud|@dim:money:aud,a$",
	"@unit:curr:cad|@dim:money:cad,c$",
	"@unit:curr:sgd|@dim:money:sgd,s$",
	"@unit:curr:nzd|@dim:money:nzd",
	"@unit:curr:krw|@dim:money:krw,₩,won,韩元,韓元,韩币,ウォン,韓国ウォン,원,원화,วอน",
	"@unit:curr:inr|@dim:money:inr,₹",
	"@unit:curr:brl|@dim:money:brl,r$",
	"@unit:curr:rub|@dim:money:₽",
	"@unit:curr:mxn|@dim:money:mxn",
	"@unit:curr:sek|@dim:money:sek",
	"@unit:curr:nok|@dim:money:nok",
	"@unit:curr:dkk|@dim:money:dkk",
	"@unit:curr:pln|@dim:money:pln",
	"@unit:curr:czk|@dim:money:czk",
	"@unit:curr:huf|@dim:money:huf",
	"@unit:curr:try|@dim:money:₺",
	"@unit:curr:zar|@dim:money:zar",
	"@unit:curr:aed|@dim:money:aed",
	"@unit:curr:sar|@dim:money:sar",
	"@unit:curr:ils|@dim:money:ils,₪",
	"@unit:curr:twd|@dim:money:twd,nt$",
	"@unit:curr:thb|@dim:money:thb,฿,บาท,baht,泰铢,泰銖,バーツ,タイバーツ,바트,태국바트",
	"@unit:curr:idr|@dim:money:idr",
	"@unit:curr:myr|@dim:money:myr",
	"@unit:curr:php|@dim:money:php,₱",
	"@unit:curr:vnd|@dim:money:vnd,₫",
	"@unit:curr:lak|@dim:money:lak,ກີບ",
	"@unit:curr:khr|@dim:money:khr",
	"@unit:time:ns|@dim:duration:ns,nanosecond,nanoseconds,纳秒,奈秒,ナノ秒,나노초,nanosekunde,nanoseconde,nanosegundo,นาโนวินาที",
	"@unit:time:us|@dim:duration:µs,μs,microsecond,microseconds,微秒,マイクロ秒,마이크로초,mikrosekunde,microseconde,microsegundo,ไมโครวินาที",
	"@unit:time:ms|@dim:duration:ms,msec,millisecond,milliseconds,毫秒,ミリ秒,밀리초,millisekunde,milliseconde,milisegundo,มิลลิวินาที",
	"@unit:time:s|@dim:duration:s,sec,secs,second,seconds,秒,秒钟,초,sekunde,sekunden,วินาที,ວິນາທີ,វិនាទី,秒鐘,seconde,secondes,segundo,segundos",
	"@unit:time:min|@dim:duration:min,mins,minute,minutes,分钟,分,분,minuten,นาที,ນາທີ,នាទី,分鐘,minuto,minutos",
	"@unit:time:h|@dim:duration:h,hr,hrs,hour,hours,小时,钟头,時間,시간,stunde,stunden,ชั่วโมง,ຊົ່ວໂມງ,ម៉ោង,小時,鐘頭,heure,heures,hora,horas",
	"@unit:time:week|@dim:duration:wk,wks,week,weeks,星期,礼拜,週間,주간,woche,wochen,สัปดาห์,ອາທິດ,សប្តាហ៍,周,禮拜,주,semaine,semaines,semana,semanas",
	"@unit:time:day|@dim:duration:day,days,天,日,日間,일,tag,tage,tagen,วัน,ມື້,ថ្ងៃ,jour,jours,journée,día,días",
	"@unit:time:month|@dim:duration:month,months,月,月份,ヶ月,か月,월,monat,monate,monaten,เดือน,ເດືອນ,ខែ,개월,mois,mes,meses",
	"@unit:time:year|@dim:duration:year,years,yr,yrs,年,年份,年間,년,jahr,jahre,jahren,ปี,ປີ,ឆ្នាំ,an,ans,année,années,año,años",
	"@unit:time:quarter|@dim:duration:quarter,quarters,qtr,季度",
	"@unit:time:decade|@dim:duration:decade,decades,十年,年代",
	"@unit:time:fortnight|@dim:duration:fortnight,fortnights",
	"@unit:mass:mg|@dim:mass:mg,milligram,milligrams,毫克,ミリグラム,밀리그램,milligramm,milligramme,miligramo,มิลลิกรัม",
	"@unit:mass:g|@dim:mass:g,gram,grams,克,グラム,그램,gramm,กรัม,ກຣາມ,ក្រាម,gramme,gramo,gramos",
	"@unit:mass:kg|@dim:mass:kg,kilo,kilos,kilogram,kilograms,千克,公斤,キロ,キログラム,킬로그램,kilogramm,กิโลกรัม,ກິໂລກຣາມ,គីឡូក្រាម,킬로,kilogramme,kilogramo,กิโล",
	"@unit:mass:ton|@dim:mass:t,ton,tons,tonne,tonnes,吨,噸,公吨,トン,톤,tonnen,tonelada,toneladas,ตัน",
	"@unit:mass:lb|@dim:mass:lb,lbs,磅,ポンド,파운드,pfund,livre,libra,libras,ปอนด์",
	"@unit:mass:oz|@dim:mass:oz,ounce,ounces,盎司,オンス,온스,unze,unzen,once,onza,onzas,ออนซ์",
	"@unit:len:mm|@dim:length:mm,millimeter,millimeters,millimetre,毫米,公厘,ミリ,ミリメートル,밀리미터,millimètre,milímetro,มิลลิเมตร",
	"@unit:len:cm|@dim:length:cm,centimeter,centimeters,centimetre,厘米,公分,เซนติเมตร,センチ,センチメートル,센티미터,zentimeter,centimètre,centímetro",
	"@unit:len:m|@dim:length:m,meter,meters,metre,metres,米,公尺,เมตร,ແມັດ,ម៉ែត្រ,メートル,미터,mètre,metro,metros",
	"@unit:len:km|@dim:length:km,kilometer,kilometers,kilometre,kilometres,千米,公里,キロメートル,킬로미터,kilometern,กิโลเมตร,ກິໂລແມັດ,គីឡូម៉ែត្រ,kilomètre,kilomètres,kilómetro,kilómetros",
	"@unit:len:ft|@dim:length:ft,foot,feet,英尺,呎,フィート,피트,fuß,pied,pieds,pie,pies,ฟุต",
	"@unit:len:mile|@dim:length:mi,mile,miles,英里,哩,マイル,마일,meile,meilen,mille,milla,millas,ไมล์",
	"@unit:vol:ml|@dim:volume:ml,milliliter,milliliters,millilitre,毫升,มิลลิลิตร,ミリリットル,밀리리터,mililitro",
	"@unit:vol:l|@dim:volume:l,liter,liters,litre,litres,升,公升,ลิตร,ລິດ,លីត្រ,リットル,리터,litro,litros",
	"@unit:vol:gal|@dim:volume:gal,gallon,gallons,加仑,加侖,ガロン,갤런,gallone,galón,galones,แกลลอน",
	"@unit:vol:pint|@dim:volume:pint,pints,品脱",
	"@unit:vol:quart|@dim:volume:quart,quarts,夸脱",
	"@unit:vol:tbsp|@dim:volume:tbsp,tablespoon,tablespoons,汤匙,大匙",
	"@unit:vol:tsp|@dim:volume:tsp,teaspoon,teaspoons,茶匙,小匙",
	"@unit:vol:floz|@dim:volume:floz",
	"@unit:data:kb|@dim:data:kb,kib,kilobyte,kilobytes,千字节",
	"@unit:data:mb|@dim:data:mb,mib,megabyte,megabytes,兆字节",
	"@unit:data:gb|@dim:data:gb,gib,gigabyte,gigabytes,吉字节",
	"@unit:data:tb|@dim:data:tb,tib,terabyte,terabytes,太字节",
	"@unit:data:pb|@dim:data:pb,pib,petabyte,petabytes,拍字节",
	"@unit:data:byte|@dim:data:byte,bytes,字节",
	"@unit:rate:bps|@dim:rate:bps",
	"@unit:rate:kbps|@dim:rate:kbps",
	"@unit:rate:mbps|@dim:rate:mbps",
	"@unit:rate:gbps|@dim:rate:gbps",
	"@unit:rate:qps|@dim:rate:qps",
	"@unit:rate:rps|@dim:rate:rps",
	"@unit:rate:tps|@dim:rate:tps",
	"@unit:freq:hz|@dim:freq:hz,khz,mhz,ghz,赫兹",
	"@unit:freq:rpm|@dim:freq:rpm",
	"@unit:temp:deg|@dim:temp:degc,degf,kelvin,℃,celsius,fahrenheit,摄氏度,华氏度,攝氏度,華氏度,摂氏,華氏,섭씨,화씨,grad,degré,degrés,grado,grados,องศาเซลเซียส,องศาฟาเรนไฮต์",
	"@unit:stat:pct|@dim:stat:%,pct,percent,percentage,percentages,百分比,百分点,prozent,เปอร์เซ็นต์,ເປີເຊັນ,ភាគរយ",
	"@unit:stat:bp|@dim:stat:bp,基点",
	"@unit:stat:quantile|@dim:stat:p50,p90,p95,p99,p999,percentile,quantile,median,中位数,分位数",
	"@unit:stat:mean|@dim:stat:avg,average,mean,平均值,均值",
	"@unit:stat:std|@dim:stat:std,stddev,stdev,variance,sigma,标准差,方差",
	"@unit:stat:ratio|@dim:stat:ppm,ppb",
	"@unit:stat:extreme|@dim:stat:max,maximum,minimum,最大值,最小值",
	"@unit:stat:total|@dim:stat:sum,total,总和,合计",
	"@unit:energy:j|@dim:energy:joule,joules,焦耳",
	"@unit:energy:kj|@dim:energy:kj,mj,kilojoule,kilojoules,千焦",
	"@unit:energy:cal|@dim:energy:cal,calorie,calories,卡路里,カロリー,칼로리,kalorie,kalorien,caloría,calorías,แคลอรี",
	"@unit:energy:kcal|@dim:energy:kcal,kilocalorie,kilocalories,千卡,大卡,キロカロリー,킬로칼로리,kilokalorie,kilocaloría,กิโลแคลอรี",
	"@unit:energy:wh|@dim:energy:wh",
	"@unit:energy:kwh|@dim:energy:kwh,mwh,watt-hour,watt-hours,kilowatt-hour,kilowatt-hours,瓦时,千瓦时,度电,千瓦時,度電,キロワット時,킬로와트시,kilowattheure,kilovatio-hora,กิโลวัตต์ชั่วโมง",
	"@unit:energy:ev|@dim:energy:ev",
	"@unit:power:w|@dim:power:watt,watts,瓦,瓦特,ワット,와트,vatios,วัตต์",
	"@unit:power:kw|@dim:power:kw,mw,gw,kilowatt,kilowatts,megawatt,megawatts,gigawatt,gigawatts,千瓦,兆瓦,kilowattstunde,kilowattstunden,กิโลวัตต์,キロワット,メガワット,킬로와트,메가와트,kilovatios",
	"@unit:power:hp|@dim:power:hp,horsepower,马力",
	"@unit:press:pa|@dim:pressure:kpa,mpa,hpa,pascal,pascals,kilopascal,kilopascals,帕,帕斯卡,千帕,兆帕",
	"@unit:press:psi|@dim:pressure:psi,mmhg,torr,mbar,millibar,millibars",
	"@unit:speed:mph|@dim:speed:mph",
	"@unit:speed:kmh|@dim:speed:kmh",
	"@unit:speed:kph|@dim:speed:kph",
	"@unit:speed:mps|@dim:speed:mps",
	"@unit:speed:kn|@dim:speed:knot,knots",
	"@unit:area:m2|@dim:area:m2,m²,sqm,平方米,平米,平方公尺,平方メートル,제곱미터,평방미터,quadratmeter,mètrecarré,metrocuadrado,ตารางเมตร",
	"@unit:area:km2|@dim:area:km2,km²,平方公里,平方千米,平方キロメートル,제곱킬로미터,quadratkilometer,kilomètrecarré,kilómetrocuadrado,ตารางกิโลเมตร",
	"@unit:area:cm2|@dim:area:cm2,cm²",
	"@unit:area:sqft|@dim:area:sqft,ft2,ft²,平方英尺",
	"@unit:area:ha|@dim:area:hectare,hectares,公顷,公頃,ヘクタール,헥타르,hektar,hectárea,hectáreas,เฮกตาร์",
	"@unit:area:acre|@dim:area:acre,acres,英亩",
	"@unit:volt:v|@dim:electric:volt,volts,mv,kv,kilovolt,kilovolts,伏,伏特,千伏",
	"@unit:amp:a|@dim:electric:amp,amps,ampere,amperes,milliampere,milliamperes,安培,毫安",
	"@unit:charge:ah|@dim:electric:mah",
	"@unit:res:ohm|@dim:electric:ohm,ohms,ω,欧姆",
	"@unit:power:va|@dim:electric:kva",
	"@unit:angle:deg|@dim:angle:deg,rad,radian,radians,องศา",
	"@unit:len:inch|@dim:length:inch,inches,英寸,吋,インチ,인치,zoll,pouce,pouces,pulgada,pulgadas,นิ้ว",
	"@bio:weather|@dim:nature:weather,forecast,meteorology,downpour,drizzle,blizzard,hail,hailing,hailstorm,hailstone,sleet,storm,storms,stormy,thunderstorm,thunder,lightning,typhoon,hurricane,tornado,天气,气象,天气预报,暴雨,暴风雪,冰雹,下冰雹,雷雨,雷暴,暴风雨,风暴,打雷,闪电,台风,飓风,龙卷风,天気,天候,気象,豪雨,吹雪,雹,ひょう,霰,嵐,暴風雨,稲妻,台風,ハリケーン,竜巻,날씨,기상,일기예보,폭우,폭설,눈보라,우박,진눈깨비,폭풍,폭풍우,천둥,번개,뇌우,태풍,허리케인,토네이도,wetter,wettervorhersage,wolkenbruch,schneesturm,hagel,hageln,hagelsturm,graupel,sturm,stürmisch,unwetter,gewitter,donner,blitz,taifun,hurrikan,météo,averse,grêle,grêler,giboulée,orage,orageux,tempête,tonnerre,éclair,foudre,typhon,ouragan,clima,pronóstico,aguacero,ventisca,granizo,granizar,granizada,aguanieve,tormenta,tormentoso,trueno,relámpago,tifón,huracán,สภาพอากาศ,พยากรณ์อากาศ,พายุฝน,พายุหิมะ,ลูกเห็บ,พายุลูกเห็บ,พายุ,พายุฝนฟ้าคะนอง,ฟ้าร้อง,ฟ้าผ่า,ไต้ฝุ่น,เฮอริเคน,ทอร์นาโด",
}

var (
	iso3166GeoOnce sync.Once
	iso3166GeoMap  map[string][][2]string
)

// ensureISO3166GeoMap collects, per token, every (tag1,tag2) axis the merged closed-set table
// owns in entry order with exact-pair de-duplication. A token shared by several axes (e.g. "gb":
// geography then the gigabyte unit, "rad": the transit lexicon then the angle unit) therefore
// keeps one ordered axis list instead of a single overwritten mapping.
func ensureISO3166GeoMap() {
	iso3166GeoOnce.Do(func() {
		iso3166GeoMap = make(map[string][][2]string, 6100)
		for _, entry := range iso3166GeoHarmonicEntries {
			colon := strings.LastIndexByte(entry, ':')
			if colon <= 0 {
				continue
			}
			head := entry[:colon]
			pipe := strings.IndexByte(head, '|')
			if pipe <= 0 {
				continue
			}
			pair := [2]string{head[:pipe], head[pipe+1:]}
			for _, tok := range strings.Split(entry[colon+1:], ",") {
				if tok == "" {
					continue
				}
				axes := iso3166GeoMap[tok]
				dup := false
				for _, prev := range axes {
					if prev == pair {
						dup = true
						break
					}
				}
				if !dup {
					iso3166GeoMap[tok] = append(axes, pair)
				}
			}
		}
	})
}

// LookupISO3166GeoHarmonicPair resolves a token into its first-seen axis pair: the 1st-order
// (@geo:cc:<iso2> or @geo:cont:<cont>) and 2nd-order (@geo:cont:<cont> or @dim:geo) ISO 3166
// geographic harmonic tags for purely geographic tokens. The merged closed-set table collects
// every axis a token owns in entry order and the geographic entries stand at the head of the
// table, so toponyms keep their original mapping; tokens shared with a later axis (e.g. "gb",
// "rad", "total") return their first-seen pair here and expose every axis via
// LookupClosedSetHarmonics and their unit view via LookupUnitDimensionHarmonicPair.
func LookupISO3166GeoHarmonicPair(token string) (string, string) {
	ensureISO3166GeoMap()
	l := strings.ToLower(strings.TrimSpace(token))
	if axes := iso3166GeoMap[l]; len(axes) > 0 {
		return axes[0][0], axes[0][1]
	}
	return "", ""
}

// LookupISO3166GeoHarmonics is an opt-in HarmonicExpander that maps multilingual L0-L3
// ISO 3166 toponyms (continents, countries, states/provinces, major cities) to canonical
// 2-tier geographic harmonic tags.
func LookupISO3166GeoHarmonics(token string) []string {
	t1, t2 := LookupISO3166GeoHarmonicPair(token)
	if t1 == "" {
		return nil
	}
	if t2 == "" || t2 == t1 {
		return []string{t1}
	}
	return []string{t1, t2}
}

// LookupUnitDimensionHarmonicPair resolves a token into the units axis view of the merged
// closed-set table: the first-seen (@unit:*, @dim:*) pair the token owns, or ("","") when it
// carries no unit/dimension axis. The `langpack` companion suite delegates its universal
// SI/IEC/ISO 4217/statistical lookups here so the four language SDKs and the Go langpack
// share a single maintenance source.
func LookupUnitDimensionHarmonicPair(token string) (string, string) {
	ensureISO3166GeoMap()
	l := strings.ToLower(strings.TrimSpace(token))
	for _, pair := range iso3166GeoMap[l] {
		if strings.HasPrefix(pair[0], "@unit:") && strings.HasPrefix(pair[1], "@dim:") {
			return pair[0], pair[1]
		}
	}
	return "", ""
}

// LookupClosedSetHarmonics is an opt-in HarmonicExpander over the merged closed-set harmonic
// table shipped with this SDK (geo/kin/role/cal/bio/obj/venue/units): ISO 3166 geography plus
// kinship, social-role, calendar, biology, object, venue and SI/IEC/ISO 4217 unit / statistical
// dimension axes (@unit:* / @dim:*). A token owned by several axes emits every axis's companion
// tags in table order with first-seen de-duplication (e.g. "gb" -> @geo:cc:gb, @geo:cont:eu,
// @unit:data:gb, @dim:data; "rad" -> @obj:transit:bike, @dim:travel, @unit:angle:deg, @dim:angle).
func LookupClosedSetHarmonics(token string) []string {
	ensureISO3166GeoMap()
	axes := iso3166GeoMap[strings.ToLower(strings.TrimSpace(token))]
	if len(axes) == 0 {
		return nil
	}
	out := make([]string, 0, 2*len(axes))
	seen := make(map[string]bool, 2*len(axes))
	for _, pair := range axes {
		for _, tag := range pair {
			if tag == "" || seen[tag] {
				continue
			}
			seen[tag] = true
			out = append(out, tag)
		}
	}
	return out
}

// CombineHarmonicExpanders merges several HarmonicExpanders into a single expander that queries
// each non-nil expander in order and concatenates their companion tags with first-seen
// de-duplication. It returns nil when no non-nil expander is supplied, so the result can be
// passed straight into SetCanonicalHarmonics.
func CombineHarmonicExpanders(es ...HarmonicExpander) HarmonicExpander {
	active := make([]HarmonicExpander, 0, len(es))
	for _, e := range es {
		if e != nil {
			active = append(active, e)
		}
	}
	if len(active) == 0 {
		return nil
	}
	return func(token string) []string {
		var out []string
		seen := make(map[string]bool, 4)
		for _, e := range active {
			for _, tag := range e(token) {
				if tag == "" || seen[tag] {
					continue
				}
				seen[tag] = true
				out = append(out, tag)
			}
		}
		return out
	}
}

// ExtractInlineSymbolUnits scans a sentence for currency/percentage/temperature symbols attached
// to digits (such as "$480", "€9.10", "£655.59", "¥100", "95%", "25°C") that standard alphanumeric
// word splitters skip, returning their canonical common-core symbols in order of appearance.
func ExtractInlineSymbolUnits(sentence string) []string {
	runes := []rune(sentence)
	n := len(runes)
	if n < 2 {
		return nil
	}
	var out []string
	seen := make(map[string]bool, 2)
	add := func(tok string) {
		if !seen[tok] {
			seen[tok] = true
			out = append(out, tok)
		}
	}
	for i, r := range runes {
		prevDigit := i > 0 && unicode.IsDigit(runes[i-1])
		nextDigit := i+1 < n && unicode.IsDigit(runes[i+1])
		if !prevDigit && !nextDigit {
			continue
		}
		switch r {
		case '$':
			add("usd")
		case '€':
			add("eur")
		case '£':
			add("gbp")
		case '¥', '￥':
			add("cny")
		case '₩':
			add("krw")
		case '₹':
			add("inr")
		case '₽':
			add("rub")
		case '₪':
			add("ils")
		case '₺':
			add("try")
		case '฿':
			add("thb")
		case '₱':
			add("php")
		case '₫':
			add("vnd")
		case '%', '％':
			add("pct")
		case '‰':
			add("ppm")
		case '℃':
			add("degc")
		case '°':
			if i+1 < n && (runes[i+1] == 'C' || runes[i+1] == 'c' || runes[i+1] == 'F' || runes[i+1] == 'f') {
				add("degc")
			}
		}
	}
	return out
}

var seAsianUnitWords = []string{
	"กิโลกรัม", "กรัม", "กิโลเมตร", "เซนติเมตร", "เมตร", "มิลลิลิตร", "ลิตร",
	"ชั่วโมง", "สัปดาห์", "วินาที", "นาที", "เดือน", "วัน", "ปี", "บาท", "องศา", "เปอร์เซ็นต์", "กิโลวัตต์",
	"ກິໂລກຣາມ", "ກຣາມ", "ກິໂລແມັດ", "ແມັດ", "ລິດ", "ຊົ່ວໂມງ", "ອາທິດ", "ວິນາທີ", "ນາທີ", "ເດືອນ", "ມື້", "ປີ", "ກີບ", "ເປີເຊັນ",
	"គីឡូក្រាម", "ក្រាម", "គីឡូម៉ែត្រ", "ម៉ែត្រ", "លីត្រ", "ម៉ោង", "សប្តាហ៍", "វិនាទី", "នាទី", "ខែ", "ថ្ងៃ", "ឆ្នាំ", "ភាគរយ",
}

// ExtractSEAsianUnitWords scans unspaced SEA text for companion unit words (longest-first).
func ExtractSEAsianUnitWords(sentence string) []string {
	if strings.TrimSpace(sentence) == "" {
		return nil
	}
	var out []string
	for _, w := range seAsianUnitWords {
		if !strings.Contains(sentence, w) {
			continue
		}
		absorbed := false
		for _, prev := range out {
			if strings.Contains(prev, w) {
				absorbed = true
				break
			}
		}
		if !absorbed {
			out = append(out, w)
		}
	}
	return out
}
