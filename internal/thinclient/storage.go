// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package thinclient

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	ramesdk "github.com/rocareer/rolling-memory/sdk/go"
)

// ============================================================================
// Bring Your Own Storage (`PlaintextStore` Interface & Built-in File/JSON Adapters)
//
// Core Philosophy:
//   "Your memory is yours — store it however you want."
//   The Rolling Autonoetic Memory Engine (`/open/memory/v1/*`) only exchanges
//   `UnitID` + irreversible blind integer pulses `{h, b, f}` and never cares where
//   or how the user stores their raw plaintext.
//   While the official plugin ships with a default SQLite `Vault`, any user or
//   third-party plugin developer can store plaintext in:
//     - Plain `.txt` / `.md` files (`DirTextStore`),
//     - Single or sharded `.json` / `.jsonl` files (`JSONFileStore`),
//     - SQLite (`Vault`), MySQL, PostgreSQL, Redis, IndexedDB, or custom KV stores
//   simply by implementing the 3-method `PlaintextStore` interface!
// ============================================================================

// PlaintextStore is the universal pluggable storage interface for user plaintext.
// Any custom storage engine (JSON file, TXT directory, SQLite, MySQL, PostgreSQL, etc.)
// only needs to implement these 3 methods to work seamlessly with `Client`.
type PlaintextStore interface {
	// PackBlindPacket mechanically splits raw plaintext into sentences and encodes
	// each word into an irreversible blind integer pulse `{h, b, f}` using the user's secret key.
	PackBlindPacket(docID string, text string, occurredAt int64) (BlindPulsePacket, []string)

	// SaveLocalPlaintext persists the user's raw plaintext alongside the engine-assigned
	// cell address, waveform bar, and kept dry-sentence indices.
	SaveLocalPlaintext(docID string, occurredAt int64, rawText string, sents []string, resp CloudIngestResponse) error

	// LoadLocalDocument loads the user's raw plaintext and reconstructs the distilled
	// dry text from `kept_sentence_indices` when the engine returns a hit for `docID`.
	LoadLocalDocument(docID string) (rawText string, distilledText string, keptSents []string, err error)
}

// Compile-time check that the default SQLite `*Vault` implements `PlaintextStore`.
var _ PlaintextStore = (*Vault)(nil)

// BlindEncoder provides standalone, storage-agnostic `{h, b, f}` pulse encoding
// using a 256-bit user secret key. Custom storage engines (JSON, TXT, MySQL, PG)
// can embed `BlindEncoder` so they only need to implement `Save` and `Load`!
// It is a thin adapter over the canonical SDK encoder (`sdk/go`); it holds no key
// material of its own, so the raw key never leaves the SDK's private state.
type BlindEncoder struct {
	sdk *ramesdk.BlindEncoder
}

// NewBlindEncoder creates a storage-agnostic pulse encoder backed by the canonical SDK encoder.
func NewBlindEncoder(secret []byte) *BlindEncoder {
	return &BlindEncoder{sdk: ramesdk.NewBlindEncoder(secret)}
}

// KeyedHash64 computes the 64-bit HMAC-SHA256 token commitment using the user's secret key via ramesdk.
func (e *BlindEncoder) KeyedHash64(token string) uint64 {
	return e.sdk.KeyedHash64(token)
}

// PackBlindPacket splits plaintext into sentences and encodes `{h, b, f}` blind integer pulses via ramesdk.
func (e *BlindEncoder) PackBlindPacket(docID string, text string, occurredAt int64) (BlindPulsePacket, []string) {
	sdkPkt, sents := e.sdk.PackBlindPacket(docID, text, occurredAt)
	return BlindPulsePacket{
		UnitID:         sdkPkt.UnitID,
		OccurredAt:     sdkPkt.OccurredAt,
		Slots:          sdkPkt.Slots,
		SentenceFrames: sdkPkt.SentenceFrames,
	}, sents
}

// ReconstructDistilled helper reconstructs distilled dry text from `allSents` and `keptIdx`.
func ReconstructDistilled(rawText string, allSents []string, keptIdx []int) (distilledText string, keptSents []string) {
	if len(keptIdx) == 0 || len(allSents) == 0 || len(keptIdx) >= len(allSents) {
		return rawText, allSents
	}
	for _, idx := range keptIdx {
		if idx >= 0 && idx < len(allSents) {
			keptSents = append(keptSents, allSents[idx])
		}
	}
	if len(keptSents) == 0 || len(keptSents) >= len(allSents) {
		return rawText, allSents
	}
	sep := "\n"
	if !strings.Contains(rawText, "\n") {
		sep = ". "
	}
	return strings.Join(keptSents, sep), keptSents
}

// ============================================================================
// Adapter 1: `JSONFileStore` — Store Memories in a Plain `.json` File
// ============================================================================

type jsonDocRecord struct {
	DocID       string   `json:"doc_id"`
	OccurredAt  int64    `json:"occurred_at"`
	RawText     string   `json:"raw_text"`
	Sentences   []string `json:"sentences"`
	AddressURI  string   `json:"address_uri"`
	Coord64     uint64   `json:"coord64"`
	WaveformBar string   `json:"waveform_bar"`
	KeptIndices []int    `json:"kept_indices"`
}

// JSONFileStore implements `PlaintextStore` backed by a human-readable `.json` file on disk.
type JSONFileStore struct {
	*BlindEncoder
	mu       sync.RWMutex
	filePath string
	docs     map[string]jsonDocRecord
}

// NewJSONFileStore opens or creates a JSON-file-backed plaintext memory store.
func NewJSONFileStore(filePath string, secret []byte) (*JSONFileStore, error) {
	if strings.TrimSpace(filePath) == "" {
		return nil, errors.New("thinclient: empty json file path")
	}
	s := &JSONFileStore{
		BlindEncoder: NewBlindEncoder(secret),
		filePath:     filePath,
		docs:         make(map[string]jsonDocRecord),
	}
	if raw, err := os.ReadFile(filePath); err == nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, &s.docs); err != nil {
			// Refusing to open is the only safe answer: silently starting from an
			// empty map would overwrite every stored memory on the next save.
			return nil, fmt.Errorf("thinclient: json store %s is corrupt: %w (move it aside to start fresh)", filePath, err)
		}
	}
	return s, nil
}

func (s *JSONFileStore) SaveLocalPlaintext(docID string, occurredAt int64, rawText string, sents []string, resp CloudIngestResponse) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.docs[docID] = jsonDocRecord{
		DocID:       docID,
		OccurredAt:  occurredAt,
		RawText:     rawText,
		Sentences:   sents,
		AddressURI:  resp.AddressURI,
		Coord64:     resp.AddressCoord64,
		WaveformBar: resp.WaveformBar,
		KeptIndices: resp.KeptSentenceIndices,
	}
	if err := os.MkdirAll(filepath.Dir(s.filePath), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s.docs, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(s.filePath, data, 0o600)
}

func (s *JSONFileStore) LoadLocalDocument(docID string) (rawText string, distilledText string, keptSents []string, err error) {
	s.mu.RLock()
	rec, ok := s.docs[docID]
	s.mu.RUnlock()
	if !ok {
		return "", "", nil, errors.New("thinclient: document not found in JSON store: " + docID)
	}
	distilled, kept := ReconstructDistilled(rec.RawText, rec.Sentences, rec.KeptIndices)
	return rec.RawText, distilled, kept, nil
}

// ============================================================================
// Adapter 2: `DirTextStore` — Store Each Memory as a Plain `.txt` / `.md` File
// ============================================================================

// DirTextStore implements `PlaintextStore` by saving each memory as a plain `<docID>.txt`
// file in a directory (with an optional `<docID>.meta.json` sidecar for kept sentence indices).
type DirTextStore struct {
	*BlindEncoder
	dirPath string
}

// NewDirTextStore creates a directory-backed plain `.txt` file memory store.
func NewDirTextStore(dirPath string, secret []byte) (*DirTextStore, error) {
	if strings.TrimSpace(dirPath) == "" {
		return nil, errors.New("thinclient: empty directory path")
	}
	if err := os.MkdirAll(dirPath, 0o755); err != nil {
		return nil, err
	}
	return &DirTextStore{
		BlindEncoder: NewBlindEncoder(secret),
		dirPath:      dirPath,
	}, nil
}

func safeFileSlug(docID string) string {
	var b strings.Builder
	for _, r := range docID {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "doc"
	}
	return b.String()
}

func (d *DirTextStore) SaveLocalPlaintext(docID string, occurredAt int64, rawText string, sents []string, resp CloudIngestResponse) error {
	slug := safeFileSlug(docID)
	txtPath := filepath.Join(d.dirPath, slug+".txt")
	if err := writeFileAtomic(txtPath, []byte(rawText), 0o600); err != nil {
		return err
	}
	meta := jsonDocRecord{
		DocID:       docID,
		OccurredAt:  occurredAt,
		Sentences:   sents,
		AddressURI:  resp.AddressURI,
		Coord64:     resp.AddressCoord64,
		WaveformBar: resp.WaveformBar,
		KeptIndices: resp.KeptSentenceIndices,
	}
	metaBytes, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(d.dirPath, slug+".meta.json"), metaBytes, 0o600)
}

func (d *DirTextStore) LoadLocalDocument(docID string) (rawText string, distilledText string, keptSents []string, err error) {
	slug := safeFileSlug(docID)
	rawBytes, err := os.ReadFile(filepath.Join(d.dirPath, slug+".txt"))
	if err != nil {
		return "", "", nil, err
	}
	rawText = string(rawBytes)
	var meta jsonDocRecord
	if mBytes, mErr := os.ReadFile(filepath.Join(d.dirPath, slug+".meta.json")); mErr == nil {
		_ = json.Unmarshal(mBytes, &meta)
	}
	sents := meta.Sentences
	if len(sents) == 0 {
		sents = SplitSentences(rawText)
	}
	distilled, kept := ReconstructDistilled(rawText, sents, meta.KeptIndices)
	return rawText, distilled, kept, nil
}

// writeFileAtomic replaces path through a same-directory temp file so a crash,
// kill, or full disk can never truncate previously stored memories.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp := fmt.Sprintf("%s.tmp.%d", path, os.Getpid())
	handle, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := handle.Write(data); err != nil {
		handle.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := handle.Sync(); err != nil {
		handle.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := handle.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	_ = os.Chmod(tmp, perm)
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
