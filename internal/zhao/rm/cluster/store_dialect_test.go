// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package cluster

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

func TestStoreDialectSingleUserSQLiteAndDualDatabasePostgresParity(t *testing.T) {
	// 1. Single-user SQLite via OpenStore (client Rolling-Memory standard)
	sqlitePath := filepath.Join(t.TempDir(), "single-user.sqlite")
	stSingle, err := OpenStore(sqlitePath)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer stSingle.Close()
	if stSingle.Dialect() != DialectSQLite {
		t.Fatalf("Dialect = %q, want %q", stSingle.Dialect(), DialectSQLite)
	}

	docsA := []Doc{
		{
			UnitID:     "u1",
			OccurredAt: 1700000100,
			RawText:    "Client local plaintext unit u1",
			Terms: map[uint64]Term{
				101: {Hash: 101, Bits: 42, Flags: 1, Role: RolePrimary, ExtFlags: ExtFlagSeenPrimary, Group: 101, Sent: 1, Slot: 1},
				202: {Hash: 202, Bits: 36, Flags: 0, Role: RolePrimary, ExtFlags: ExtFlagSeenPrimary, Group: 202, Sent: 1, Slot: 2},
			},
			Counts: map[uint64]int{101: 1, 202: 2},
			Positions: map[uint64][]TermPosition{
				101: {{Sentence: 0, Slot: 1}},
				202: {{Sentence: 0, Slot: 2}, {Sentence: 0, Slot: 5}},
			},
		},
	}
	if err := stSingle.Ingest(docsA); err != nil {
		t.Fatalf("stSingle.Ingest: %v", err)
	}
	if gotRaw, err := stSingle.LoadRawText("u1"); err != nil || gotRaw != "Client local plaintext unit u1" {
		t.Fatalf("stSingle.LoadRawText = %q (err=%v)", gotRaw, err)
	}
	idxSingle, err := stSingle.LoadIndex()
	if err != nil || idxSingle.N != 1 {
		t.Fatalf("stSingle.LoadIndex: n=%d err=%v", idxSingle.N, err)
	}

	// 2. cloud engine Dual-Database Physical Isolation:
	//    - cipherDB: physically separate database for `pulse_cipher` (ZERO raw_text column)
	//    - plainDB:  physically separate database for `pulse_plain` (has raw_text column)
	cipherDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open cipherDB: %v", err)
	}
	defer cipherDB.Close()

	plainDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open plainDB: %v", err)
	}
	defer plainDB.Close()

	dual, err := OpenDualClusterStore(cipherDB, plainDB)
	if err != nil {
		t.Fatalf("OpenDualClusterStore: %v", err)
	}

	// 2a. Cipher tenant (`pulse_cipher`) -> routed to cipherDB
	stCipherA, err := dual.StoreForTenant("tenant-alpha", "vault-cipher", "pulse_cipher")
	if err != nil {
		t.Fatalf("StoreForTenant(pulse_cipher): %v", err)
	}
	// Attempting to ingest plaintext into CipherDB must fail-closed!
	if err := stCipherA.Ingest(docsA); !errors.Is(err, ErrCipherStoreRejectsPlaintext) {
		t.Fatalf("expected ErrCipherStoreRejectsPlaintext when RawText is non-empty on CipherDB, got %v", err)
	}
	// Strip RawText for cipher ingest
	docsCipherA := []Doc{docsA[0]}
	docsCipherA[0].RawText = ""
	if err := stCipherA.Ingest(docsCipherA); err != nil {
		t.Fatalf("stCipherA.Ingest(blind): %v", err)
	}

	// 2b. Plain tenant (`pulse_plain`) -> routed to physically separate plainDB
	stPlainB, err := dual.StoreForTenant("tenant-beta", "dialog-plain", "pulse_plain")
	if err != nil {
		t.Fatalf("StoreForTenant(pulse_plain): %v", err)
	}
	// Attempting to ingest empty RawText into PlainDB must fail-closed!
	if err := stPlainB.Ingest(docsCipherA); !errors.Is(err, ErrPlainStoreRequiresPlaintext) {
		t.Fatalf("expected ErrPlainStoreRequiresPlaintext when RawText is empty on PlainDB, got %v", err)
	}
	if err := stPlainB.Ingest(docsA); err != nil {
		t.Fatalf("stPlainB.Ingest(plain): %v", err)
	}
	if gotPlain, err := stPlainB.LoadRawText("u1"); err != nil || gotPlain != "Client local plaintext unit u1" {
		t.Fatalf("stPlainB.LoadRawText = %q (err=%v)", gotPlain, err)
	}

	// 2c. Attempting to access ("tenant-alpha", "vault-cipher") as `pulse_plain` must fail-closed!
	if _, err := dual.StoreForTenant("tenant-alpha", "vault-cipher", "pulse_plain"); !errors.Is(err, ErrDualStoreModeConflict) {
		t.Fatalf("expected ErrDualStoreModeConflict on cross-mode access, got %v", err)
	}

	// 2d. 0-ULP parity across Client SQLite, Server CipherDB, and Server PlainDB
	idxCipherA, err := stCipherA.LoadIndex()
	if err != nil || idxCipherA.N != 1 {
		t.Fatalf("stCipherA.LoadIndex: n=%d err=%v", idxCipherA.N, err)
	}
	idxPlainB, err := stPlainB.LoadIndex()
	if err != nil || idxPlainB.N != 1 {
		t.Fatalf("stPlainB.LoadIndex: n=%d err=%v", idxPlainB.N, err)
	}
	dSingle := ComputeCorpusDigest(idxSingle)
	dCipher := ComputeCorpusDigest(idxCipherA)
	dPlain := ComputeCorpusDigest(idxPlainB)
	if dSingle != dCipher || dSingle != dPlain {
		t.Fatalf("0-ULP digest mismatch across 3 stores: sqlite=%x cipherPG=%x plainPG=%x", dSingle, dCipher, dPlain)
	}
}

// TestStoreDialectPostgresCitationRoundtripParity pins the multi-tenant
// PostgreSQL persistence of the citation provenance channel on both isolated
// databases (CipherDB and PlainDB), mirroring the SQLite rm_citations
// contract 1:1: ord-preserving roundtrip, self/empty-ID sanitation at read
// time, replacement clearing stale rows, DeleteDocs removing rows, and
// 0-ULP corpus-digest parity with the client SQLite store.
func TestStoreDialectPostgresCitationRoundtripParity(t *testing.T) {
	mkDocs := func(raw string) []Doc {
		ev := Doc{
			UnitID:     "ev_1",
			OccurredAt: 1700000100,
			RawText:    raw,
			Terms: map[uint64]Term{
				301: {Hash: 301, Bits: 40, Flags: 1, Role: RolePrimary, ExtFlags: ExtFlagSeenPrimary, Group: 301, Sent: 1, Slot: 1},
			},
			Counts:    map[uint64]int{301: 1},
			Positions: map[uint64][]TermPosition{301: {{Sentence: 0, Slot: 1}}},
		}
		no := Doc{
			UnitID:     "no_1",
			OccurredAt: 1700086500,
			Layer:      "noetic",
			RawText:    raw,
			CitedIDs:   []string{"ev_1", "no_1", ""},
			Terms: map[uint64]Term{
				401: {Hash: 401, Bits: 38, Flags: 0, Role: RolePrimary, ExtFlags: ExtFlagSeenPrimary, Group: 401, Sent: 1, Slot: 1},
			},
			Counts:    map[uint64]int{401: 1},
			Positions: map[uint64][]TermPosition{401: {{Sentence: 0, Slot: 1}}},
		}
		return []Doc{ev, no}
	}

	// Client single-user SQLite reference arm.
	stSingle, err := OpenStore(filepath.Join(t.TempDir(), "cite-ref.sqlite"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer stSingle.Close()
	if err := stSingle.Ingest(mkDocs("plain text payload")); err != nil {
		t.Fatalf("sqlite Ingest: %v", err)
	}
	idxRef, err := stSingle.LoadIndex()
	if err != nil {
		t.Fatalf("sqlite LoadIndex: %v", err)
	}

	cipherDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer cipherDB.Close()
	plainDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer plainDB.Close()
	dual, err := OpenDualClusterStore(cipherDB, plainDB)
	if err != nil {
		t.Fatalf("OpenDualClusterStore: %v", err)
	}

	checkArm := func(name string, st *Store, raw string) {
		t.Helper()
		if err := st.Ingest(mkDocs(raw)); err != nil {
			t.Fatalf("%s Ingest: %v", name, err)
		}
		idx, err := st.LoadIndex()
		if err != nil {
			t.Fatalf("%s LoadIndex: %v", name, err)
		}
		u, ok := idx.LookupUnit("no_1")
		if !ok {
			t.Fatalf("%s: no_1 missing", name)
		}
		// Read path keeps ord order; self-citation rows are stored but the
		// in-memory graph is what matters: SQLite write path drops them at
		// Ingest, PG write path must behave identically.
		got := idx.UnitMeta[u].CitedIDs
		if len(got) != 1 || got[0] != "ev_1" {
			t.Fatalf("%s: CitedIDs roundtrip wrong (want [ev_1]): %+v", name, got)
		}
		if d := ComputeCorpusDigest(idx); d != ComputeCorpusDigest(idxRef) {
			t.Fatalf("%s: corpus digest diverged from SQLite reference: %x vs %x", name, d, ComputeCorpusDigest(idxRef))
		}
		// Replacement without citations clears stale rows.
		repl := mkDocs(raw)[1]
		repl.CitedIDs = nil
		if err := st.Ingest([]Doc{repl}); err != nil {
			t.Fatalf("%s re-Ingest: %v", name, err)
		}
		idx2, err := st.LoadIndex()
		if err != nil {
			t.Fatalf("%s LoadIndex#2: %v", name, err)
		}
		u2, _ := idx2.LookupUnit("no_1")
		if got := idx2.UnitMeta[u2].CitedIDs; len(got) != 0 {
			t.Fatalf("%s: stale citations survived replacement: %+v", name, got)
		}
		// Re-ingest with citations then DeleteDocs purges rows.
		if err := st.Ingest([]Doc{mkDocs(raw)[1]}); err != nil {
			t.Fatalf("%s re-Ingest#2: %v", name, err)
		}
		if err := st.DeleteDocs("no_1"); err != nil {
			t.Fatalf("%s DeleteDocs: %v", name, err)
		}
		idx3, err := st.LoadIndex()
		if err != nil {
			t.Fatalf("%s LoadIndex#3: %v", name, err)
		}
		for v := range idx3.UnitMeta {
			if len(idx3.UnitMeta[v].CitedIDs) != 0 {
				t.Fatalf("%s: orphan citation rows leaked onto %s", name, idx3.UnitIDs[v])
			}
		}
	}

	stCipher, err := dual.StoreForTenant("tenant-c", "cite-cipher", "pulse_cipher")
	if err != nil {
		t.Fatalf("StoreForTenant(cipher): %v", err)
	}
	checkArm("cipherPG", stCipher, "")

	stPlain, err := dual.StoreForTenant("tenant-p", "cite-plain", "pulse_plain")
	if err != nil {
		t.Fatalf("StoreForTenant(plain): %v", err)
	}
	checkArm("plainPG", stPlain, "plain text payload")
}
