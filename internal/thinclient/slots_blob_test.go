// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package thinclient

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// TestAtomSlotsPackedRoundtrip pins the packed slot format: every atom field
// survives, and a legacy JSON row is still read (rolling restart tolerance).
func TestAtomSlotsPackedRoundtrip(t *testing.T) {
	atoms := []BlindTokenAtom{
		{H: 1, RawBitsX10: 2, ClassFlags: 3},
		{H: ^uint64(0), RawBitsX10: 65535, ClassFlags: 255},
		{H: 0x00ff00ff00ff00ff, RawBitsX10: 0, ClassFlags: 1},
	}
	blob := encodeAtomSlots(atoms)
	if string(blob[:4]) != atomSlotsMagic {
		t.Fatalf("magic = %q", blob[:4])
	}
	decoded, ok := decodeAtomSlots(blob)
	if !ok || len(decoded) != len(atoms) {
		t.Fatalf("decode: ok=%v len=%d", ok, len(decoded))
	}
	for i := range atoms {
		if decoded[i] != atoms[i] {
			t.Fatalf("atom %d = %+v, want %+v", i, decoded[i], atoms[i])
		}
	}
	legacy, ok := decodeAtomSlots([]byte(`[{"h":1,"b":2,"f":3},{"h":4,"b":5,"f":6}]`))
	if !ok || len(legacy) != 2 || legacy[1].H != 4 || legacy[1].RawBitsX10 != 5 || legacy[1].ClassFlags != 6 {
		t.Fatalf("legacy decode: ok=%v %+v", ok, legacy)
	}
	if empty, ok := decodeAtomSlots(nil); !ok || len(empty) != 0 {
		t.Fatalf("empty decode: ok=%v %+v", ok, empty)
	}
	if _, ok := decodeAtomSlots([]byte("garbage")); ok {
		t.Fatalf("garbage must be rejected")
	}
}

// TestMigrateBackgroundSlotBlobs rewrites legacy JSON rows in place and leaves
// already-packed rows untouched.
func TestMigrateBackgroundSlotBlobs(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "vault.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE thin_vault_bg_chunks (unit_id TEXT PRIMARY KEY, slots_json TEXT NOT NULL DEFAULT '[]')`); err != nil {
		t.Fatalf("ddl: %v", err)
	}
	packed := encodeAtomSlots([]BlindTokenAtom{{H: 7, RawBitsX10: 8, ClassFlags: 9}})
	if _, err := db.Exec(`INSERT INTO thin_vault_bg_chunks(unit_id, slots_json) VALUES ('legacy', ?), ('packed', ?)`,
		[]byte(`[{"h":7,"b":8,"f":9}]`), packed); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := migrateBackgroundSlotBlobs(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	var legacyRaw, packedRaw []byte
	if err := db.QueryRow(`SELECT slots_json FROM thin_vault_bg_chunks WHERE unit_id='legacy'`).Scan(&legacyRaw); err != nil {
		t.Fatalf("read legacy: %v", err)
	}
	if err := db.QueryRow(`SELECT slots_json FROM thin_vault_bg_chunks WHERE unit_id='packed'`).Scan(&packedRaw); err != nil {
		t.Fatalf("read packed: %v", err)
	}
	if len(legacyRaw) == 0 || legacyRaw[0] == '[' {
		t.Fatalf("legacy row not migrated: %q", legacyRaw)
	}
	atoms, ok := decodeAtomSlots(legacyRaw)
	if !ok || len(atoms) != 1 || atoms[0].H != 7 {
		t.Fatalf("migrated atoms: ok=%v %+v raw=%x", ok, atoms, legacyRaw)
	}
	if string(packedRaw) != string(packed) {
		t.Fatalf("packed row must stay byte-identical")
	}
	if err := migrateBackgroundSlotBlobs(db); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
}
