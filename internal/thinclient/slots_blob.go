// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package thinclient

import (
	"bytes"
	"database/sql"
	"encoding/binary"
	"encoding/json"
)

// ============================================================================
// Compact blind-slot storage (`thin_vault_bg_chunks.slots_json`)
//
// One background chunk's pre-packed atoms are a materialised scoring index: local
// recall sums hash frequencies per chunk without touching the source file. Storing
// them as a JSON array of `{"h":…,"b":…,"f":…}` objects costs ~26 bytes per atom
// (≈8.6 KiB per chunk); the packed form is 11 bytes per atom (≈3.6 KiB), with the
// exact same values — no field is dropped.
//
// The column name is kept (`slots_json`) so builds of the previous shape keep
// working during a rolling restart: they still write JSON (accepted below) and
// simply score zero for packed rows until they restart. New writes are always the
// packed form; `migrateBackgroundSlotBlobs` rewrites legacy JSON rows once.
// ============================================================================

// atomSlotsMagic marks the packed form so a reader can tell it from legacy JSON.
const atomSlotsMagic = "RMA1"

// atomSlotStride is the packed width of one atom: H(8) + RawBitsX10(2) + ClassFlags(1).
const atomSlotStride = 11

// encodeAtomSlots packs atoms into `RMA1` + count(uint32 LE) + fixed-width records.
func encodeAtomSlots(atoms []BlindTokenAtom) []byte {
	buf := make([]byte, 0, len(atomSlotsMagic)+4+len(atoms)*atomSlotStride)
	buf = append(buf, atomSlotsMagic...)
	var count [4]byte
	binary.LittleEndian.PutUint32(count[:], uint32(len(atoms)))
	buf = append(buf, count[:]...)
	var record [atomSlotStride]byte
	for _, atom := range atoms {
		binary.LittleEndian.PutUint64(record[0:8], atom.H)
		binary.LittleEndian.PutUint16(record[8:10], atom.RawBitsX10)
		record[10] = atom.ClassFlags
		buf = append(buf, record[:]...)
	}
	return buf
}

// decodeAtomSlots accepts both the packed form and legacy JSON rows. ok is false
// only for data that is neither (a corrupt row is skipped, never guessed). The
// packed form is binary: it is never trimmed (a flag byte may BE whitespace).
func decodeAtomSlots(raw []byte) (atoms []BlindTokenAtom, ok bool) {
	if len(raw) == 0 {
		return nil, true
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, true
	}
	if trimmed[0] == '[' {
		var legacy []BlindTokenAtom
		if err := json.Unmarshal(trimmed, &legacy); err != nil {
			return nil, false
		}
		return legacy, true
	}
	if len(raw) < len(atomSlotsMagic)+4 || !bytes.HasPrefix(raw, []byte(atomSlotsMagic)) {
		return nil, false
	}
	count := int(binary.LittleEndian.Uint32(raw[4:8]))
	if count < 0 || len(raw) != len(atomSlotsMagic)+4+count*atomSlotStride {
		return nil, false
	}
	atoms = make([]BlindTokenAtom, count)
	for i := 0; i < count; i++ {
		offset := len(atomSlotsMagic) + 4 + i*atomSlotStride
		atoms[i] = BlindTokenAtom{
			H:          binary.LittleEndian.Uint64(raw[offset : offset+8]),
			RawBitsX10: binary.LittleEndian.Uint16(raw[offset+8 : offset+10]),
			ClassFlags: raw[offset+10],
		}
	}
	return atoms, true
}

// decodeAtomSlotTermCounts extracts per-hash term counts and total slot length directly
// from a packed `RMBS` blob without allocating an intermediate `[]BlindTokenAtom` slice.
// Legacy JSON rows fall back to `decodeAtomSlots`.
func decodeAtomSlotTermCounts(raw []byte) (tCounts map[uint64]int, docLen int, ok bool) {
	if len(raw) >= len(atomSlotsMagic)+4 &&
		raw[0] == atomSlotsMagic[0] && raw[1] == atomSlotsMagic[1] &&
		raw[2] == atomSlotsMagic[2] && raw[3] == atomSlotsMagic[3] {
		count := int(binary.LittleEndian.Uint32(raw[4:8]))
		if count < 0 || len(raw) != len(atomSlotsMagic)+4+count*atomSlotStride {
			return nil, 0, false
		}
		hint := count/2 + 4
		if count < 8 {
			hint = count
		}
		tCounts = make(map[uint64]int, hint)
		offset := len(atomSlotsMagic) + 4
		for i := 0; i < count; i++ {
			h := binary.LittleEndian.Uint64(raw[offset : offset+8])
			tCounts[h]++
			offset += atomSlotStride
		}
		docLen = count
		if docLen < 1 {
			docLen = 1
		}
		return tCounts, docLen, true
	}
	atoms, ok := decodeAtomSlots(raw)
	if !ok {
		return nil, 0, false
	}
	tCounts = make(map[uint64]int, len(atoms)/2+4)
	for _, ds := range atoms {
		tCounts[ds.H]++
	}
	docLen = len(atoms)
	if docLen < 1 {
		docLen = 1
	}
	return tCounts, docLen, true
}

// atomSlotMatchWidth sums the query widths of the distinct atom hashes present in one
// stored chunk blob, so a background query can scan the resident packed bytes instead of
// decoding (and allocating) the whole slot list. `seen` is caller-provided scratch and is
// cleared on entry; legacy JSON rows fall back to decodeAtomSlots.
func atomSlotMatchWidth(raw []byte, qHashes map[uint64]uint16, seen map[uint64]struct{}) uint32 {
	if len(raw) == 0 || len(qHashes) == 0 {
		return 0
	}
	clear(seen)
	var width uint32
	if raw[0] != atomSlotsMagic[0] {
		atoms, ok := decodeAtomSlots(raw)
		if !ok {
			return 0
		}
		for _, atom := range atoms {
			if qw, hit := qHashes[atom.H]; hit {
				if _, dup := seen[atom.H]; dup {
					continue
				}
				seen[atom.H] = struct{}{}
				width += uint32(qw)
			}
		}
		return width
	}
	if !bytes.HasPrefix(raw, []byte(atomSlotsMagic)) || len(raw) < len(atomSlotsMagic)+4 {
		return 0
	}
	count := int(binary.LittleEndian.Uint32(raw[len(atomSlotsMagic) : len(atomSlotsMagic)+4]))
	if count < 0 || len(raw) != len(atomSlotsMagic)+4+count*atomSlotStride {
		return 0
	}
	for i := 0; i < count; i++ {
		offset := len(atomSlotsMagic) + 4 + i*atomSlotStride
		h := binary.LittleEndian.Uint64(raw[offset : offset+8])
		qw, hit := qHashes[h]
		if !hit {
			continue
		}
		if _, dup := seen[h]; dup {
			continue
		}
		seen[h] = struct{}{}
		width += uint32(qw)
	}
	return width
}

// migrateBackgroundSlotBlobs rewrites legacy JSON slot rows into the packed form.
// It is idempotent (a packed row never starts with '[') and tolerates a vault that
// has no background tables yet.
func migrateBackgroundSlotBlobs(db *sql.DB) error {
	if db == nil {
		return nil
	}
	// Fast O(1) boundary probe: check the earliest and latest rows by rowid before
	// scanning hundreds of megabytes of already-packed binary slot blobs.
	probeRows, err := db.Query(`
		SELECT substr(CAST(slots_json AS BLOB), 1, 4)
		FROM thin_vault_bg_chunks
		WHERE rowid IN (
			(SELECT MIN(rowid) FROM thin_vault_bg_chunks),
			(SELECT MAX(rowid) FROM thin_vault_bg_chunks)
		)`)
	if err != nil {
		return nil // background tables are created on first mount
	}
	needsScan := false
	seenProbe := 0
	for probeRows.Next() {
		var head []byte
		if err := probeRows.Scan(&head); err == nil {
			seenProbe++
			if len(head) > 0 && (head[0] == '[' || string(head) != atomSlotsMagic) {
				needsScan = true
			}
		}
	}
	probeRows.Close()
	if seenProbe == 0 || !needsScan {
		return nil
	}
	rows, err := db.Query(`SELECT unit_id, slots_json FROM thin_vault_bg_chunks WHERE length(slots_json) > 0 AND substr(CAST(slots_json AS TEXT), 1, 1) = '['`)
	if err != nil {
		return nil
	}
	type legacyRow struct {
		unitID string
		blob   []byte
	}
	pending := make([]legacyRow, 0, 64)
	for rows.Next() {
		var unitID string
		var raw []byte
		if err := rows.Scan(&unitID, &raw); err != nil {
			continue
		}
		atoms, ok := decodeAtomSlots(raw)
		if !ok {
			continue
		}
		pending = append(pending, legacyRow{unitID: unitID, blob: encodeAtomSlots(atoms)})
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if len(pending) == 0 {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, row := range pending {
		if _, err := tx.Exec(`UPDATE thin_vault_bg_chunks SET slots_json = ? WHERE unit_id = ?`, row.blob, row.unitID); err != nil {
			return err
		}
	}
	return tx.Commit()
}
