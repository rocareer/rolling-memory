package thinclient

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func openIncrTestVault(t *testing.T, name string) (*Vault, *sql.DB, string) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), name)
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	v, err := NewVault(db, []byte("user-local-master-key-2026"))
	if err != nil {
		t.Fatalf("NewVault: %v", err)
	}
	return v, db, dbPath
}

// snapshotEquivalence asserts that the warm (generation-checked) snapshot of v
// delivers exactly the same recall ordering as a cold Vault rebuilt from the
// same SQLite file: the post-write snapshot path must be bitwise-equivalent to
// a from-scratch rebuild, never an approximation.
func snapshotEquivalence(t *testing.T, v *Vault, dbPath string, queries []string) {
	t.Helper()
	coldDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open cold db: %v", err)
	}
	defer coldDB.Close()
	coldV, err := NewVault(coldDB, []byte("user-local-master-key-2026"))
	if err != nil {
		t.Fatalf("cold NewVault: %v", err)
	}

	warmSnap, err := v.loadCorpusSnapshot(context.Background())
	if err != nil {
		t.Fatalf("warm snapshot: %v", err)
	}
	coldSnap, err := coldV.buildCorpusSnapshot(context.Background())
	if err != nil {
		t.Fatalf("cold snapshot: %v", err)
	}
	if warmSnap == nil || coldSnap == nil {
		t.Fatalf("nil snapshot: warm=%v cold=%v", warmSnap == nil, coldSnap == nil)
	}
	if len(warmSnap.docs) != len(coldSnap.docs) {
		t.Fatalf("docs count diverged: warm=%d cold=%d", len(warmSnap.docs), len(coldSnap.docs))
	}
	if warmSnap.totalDocLen != coldSnap.totalDocLen {
		t.Fatalf("totalDocLen diverged: warm=%d cold=%d", warmSnap.totalDocLen, coldSnap.totalDocLen)
	}
	if len(warmSnap.globalDF) != len(coldSnap.globalDF) {
		t.Fatalf("globalDF size diverged: warm=%d cold=%d", len(warmSnap.globalDF), len(coldSnap.globalDF))
	}
	for h, c := range coldSnap.globalDF {
		if warmSnap.globalDF[h] != c {
			t.Fatalf("globalDF[%d] diverged: warm=%d cold=%d", h, warmSnap.globalDF[h], c)
		}
	}
	for _, q := range queries {
		warmHits, wErr := v.SearchClientSDKDefault(q, 10)
		coldHits, cErr := coldV.SearchClientSDKDefault(q, 10)
		if (wErr == nil) != (cErr == nil) {
			t.Fatalf("query %q error diverged: warm=%v cold=%v", q, wErr, cErr)
		}
		if len(warmHits) != len(coldHits) {
			t.Fatalf("query %q hit count diverged: warm=%d cold=%d", q, len(warmHits), len(coldHits))
		}
		for i := range warmHits {
			if warmHits[i].UnitID != coldHits[i].UnitID {
				t.Fatalf("query %q rank %d diverged: warm=%s cold=%s", q, i, warmHits[i].UnitID, coldHits[i].UnitID)
			}
			if warmHits[i].FinalWeight != coldHits[i].FinalWeight {
				t.Fatalf("query %q rank %d weight diverged: warm=%v cold=%v", q, i, warmHits[i].FinalWeight, coldHits[i].FinalWeight)
			}
		}
	}
}

// TestSnapshotWriteGenerationEquivalence covers the post-write snapshot
// lifecycle: a plaintext save bumps the write generation instead of discarding
// the snapshot, and the next load must re-verify fingerprints and converge to
// the exact state of a cold rebuild.
func TestSnapshotWriteGenerationEquivalence(t *testing.T) {
	v, _, dbPath := openIncrTestVault(t, "gen_equiv.db")
	docs := []struct {
		id, text string
		occ      int64
	}{
		{"d1", "Albert visited the Porto lighthouse and photographed the Douro river sunset.", 1700000100},
		{"d2", "The Dongguan train arrived at noon; dinner was braised goose with friends.", 1700000200},
		{"d3", "Cambridge saw its first snow of late 2022 on December 11th around 22:40.", 1700000300},
		{"d4", "NeurIPS poster session: recruiting QR codes lined the convention hallway.", 1700000400},
	}
	for _, d := range docs {
		sents := SplitSentences(d.text)
		if err := v.SaveLocalPlaintext(d.id, d.occ, d.text, sents, CloudIngestResponse{AddressURI: "mem://" + d.id}); err != nil {
			t.Fatalf("save %s: %v", d.id, err)
		}
	}
	if _, err := v.loadCorpusSnapshot(context.Background()); err != nil {
		t.Fatalf("initial snapshot: %v", err)
	}
	// Write after a published snapshot: generation gap must route the next load
	// through the fingerprint re-check and land on the cold-equivalent state.
	extra := "A milk cow cat appeared in Oxford near the covered market."
	if err := v.SaveLocalPlaintext("d5", 1700000500, extra, SplitSentences(extra), CloudIngestResponse{AddressURI: "mem://d5"}); err != nil {
		t.Fatalf("save d5: %v", err)
	}
	snapshotEquivalence(t, v, dbPath, []string{
		"Porto lighthouse sunset",
		"first snow Cambridge 2022",
		"milk cow cat Oxford",
		"NeurIPS recruiting QR",
	})
}

// TestSnapshotWriteGenerationManyWrites exercises a write burst (debounced
// background refresh) followed by immediate foreground queries.
func TestSnapshotWriteGenerationManyWrites(t *testing.T) {
	v, _, dbPath := openIncrTestVault(t, "gen_burst.db")
	queries := []string{"storm shelter basement", "birthday cake lemon"}
	for i := 0; i < 24; i++ {
		text := fmt.Sprintf("Entry %02d: the storm shelter basement stocked lemon birthday cake and %d candles.", i, i)
		id := fmt.Sprintf("b%02d", i)
		if err := v.SaveLocalPlaintext(id, int64(1700001000+i*60), text, SplitSentences(text), CloudIngestResponse{AddressURI: "mem://" + id}); err != nil {
			t.Fatalf("save %s: %v", id, err)
		}
		if i%7 == 3 {
			// Interleave queries mid-burst: every read must already see every
			// prior write (read-your-writes), matching a cold rebuild.
			snapshotEquivalence(t, v, dbPath, queries)
		}
	}
	snapshotEquivalence(t, v, dbPath, queries)
}

// TestFullPostingsEquivalentToLazyScan pins the complete inverted index built
// at snapshot assembly to the lazy per-query scan it replaces: identical hash
// coverage and identical ascending doc-index lists.
func TestFullPostingsEquivalentToLazyScan(t *testing.T) {
	v, _, _ := openIncrTestVault(t, "postings_equiv.db")
	texts := []string{
		"Albert photographed the Porto lighthouse at golden hour.",
		"The lighthouse keeper in Porto served espresso at dawn.",
		"Dongguan braised goose dinner with college friends.",
		"Cambridge first snow fell late in December 2022.",
		"Porto repeated: lighthouse lighthouse lighthouse.",
	}
	for i, txt := range texts {
		id := fmt.Sprintf("p%02d", i)
		if err := v.SaveLocalPlaintext(id, int64(1700003000+i*60), txt, SplitSentences(txt), CloudIngestResponse{AddressURI: "mem://" + id}); err != nil {
			t.Fatalf("save %s: %v", id, err)
		}
	}
	snap, err := v.buildCorpusSnapshot(context.Background())
	if err != nil || snap == nil {
		t.Fatalf("snapshot: %v", err)
	}
	if !snap.postingsComplete {
		t.Fatalf("expected complete postings on a fully assembled snapshot")
	}
	// Lazy reference: force the scan path on a sibling snapshot sharing the
	// same docs and globalDF.
	lazy := &vaultCorpusSnapshot{docs: snap.docs, globalDF: snap.globalDF}
	qHashes := make(map[uint64]uint16, len(snap.globalDF))
	for h := range snap.globalDF {
		qHashes[h] = 32
	}
	lazyLists := lazy.queryPostings(qHashes)
	fullLists := snap.queryPostings(qHashes)
	if len(fullLists) != len(lazyLists) {
		t.Fatalf("hash coverage diverged: full=%d lazy=%d", len(fullLists), len(lazyLists))
	}
	for h, want := range lazyLists {
		got, ok := fullLists[h]
		if !ok {
			t.Fatalf("hash %d missing from full postings", h)
		}
		if len(got) != len(want) {
			t.Fatalf("hash %d list length diverged: full=%d lazy=%d", h, len(got), len(want))
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("hash %d entry %d diverged: full=%d lazy=%d", h, i, got[i], want[i])
			}
		}
	}
}

// TestCoherenceDecompositionBitwiseEquivalent pins the cached-mass
// decomposition (SalientInfoMass + SharedSalientInfoMass + CoherenceFromMasses)
// to the fused ComputeDimensionlessCoherence, including empty-map edges and the
// smaller-side iteration symmetry of the shared mass.
func TestCoherenceDecompositionBitwiseEquivalent(t *testing.T) {
	mk := func(hs ...uint64) map[uint64]int {
		m := make(map[uint64]int, len(hs))
		for _, h := range hs {
			m[h]++
		}
		return m
	}
	df := map[uint64]int{1: 2, 2: 3, 3: 50, 4: 1, 5: 7, 6: 2, 7: 9999}
	totalDocs := 300
	log2Total := fixedLog2Millibits(uint64(totalDocs + 4))
	cases := []struct {
		name   string
		da, di map[uint64]int
	}{
		{"disjoint", mk(1, 2), mk(5, 6)},
		{"overlap", mk(1, 2, 3, 5), mk(2, 5, 6)},
		{"identical", mk(1, 2, 5, 6), mk(1, 2, 5, 6)},
		{"da-larger", mk(1, 2, 3, 5, 6), mk(2)},
		{"di-larger", mk(2), mk(1, 2, 3, 5, 6)},
		{"salient-filtered", mk(4, 7), mk(4, 7)},
		{"empty-da", map[uint64]int{}, mk(1, 2)},
		{"empty-di", mk(1, 2), map[uint64]int{}},
	}
	for _, tc := range cases {
		wantDice, wantNu := ComputeDimensionlessCoherence(tc.da, tc.di, df, totalDocs, log2Total)
		gotDice, gotNu := CoherenceFromMasses(
			SalientInfoMass(tc.da, df, totalDocs, log2Total),
			SalientInfoMass(tc.di, df, totalDocs, log2Total),
			SharedSalientInfoMass(tc.da, tc.di, df, totalDocs, log2Total),
			log2Total,
		)
		if gotDice != wantDice || gotNu != wantNu {
			t.Fatalf("%s diverged: got (%v,%v) want (%v,%v)", tc.name, gotDice, gotNu, wantDice, wantNu)
		}
		// Shared mass must be argument-order symmetric.
		if SharedSalientInfoMass(tc.da, tc.di, df, totalDocs, log2Total) != SharedSalientInfoMass(tc.di, tc.da, df, totalDocs, log2Total) {
			t.Fatalf("%s shared mass asymmetric", tc.name)
		}
	}
}

// TestSnapshotRevisionGateCrossConnectionWrite proves the O(1) revision gate is
// safe against writers the in-process generation counter cannot see: the
// triggers live in the database file, so a second connection's INSERT, UPDATE
// and DELETE each bump docs_rev, and the next load must observe the change and
// converge to the cold-rebuild state instead of serving the stale snapshot.
func TestSnapshotRevisionGateCrossConnectionWrite(t *testing.T) {
	v, _, dbPath := openIncrTestVault(t, "gen_xproc.db")
	base := "The expedition cached supplies near the glacier moraine camp."
	if err := v.SaveLocalPlaintext("x1", 1700002000, base, SplitSentences(base), CloudIngestResponse{AddressURI: "mem://x1"}); err != nil {
		t.Fatalf("save x1: %v", err)
	}
	if _, err := v.loadCorpusSnapshot(context.Background()); err != nil {
		t.Fatalf("initial snapshot: %v", err)
	}

	// Foreign writer: a separate connection the Vault knows nothing about.
	ext, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open ext: %v", err)
	}
	defer ext.Close()
	if _, err := ext.Exec(`INSERT INTO thin_vault_docs(doc_id, layer, occurred_at, inserted_at, raw_text, sentences_json) VALUES ('x2', 'episodic', 1700002100, 1700002100, 'A hidden aurora photograph from the northern ridge.', '[]')`); err != nil {
		t.Fatalf("ext insert: %v", err)
	}
	snapshotEquivalence(t, v, dbPath, []string{"aurora northern ridge", "glacier moraine camp"})

	if _, err := ext.Exec(`UPDATE thin_vault_docs SET raw_text = 'A hidden aurora photograph from the SOUTHERN ridge.' WHERE doc_id = 'x2'`); err != nil {
		t.Fatalf("ext update: %v", err)
	}
	snapshotEquivalence(t, v, dbPath, []string{"aurora southern ridge"})

	if _, err := ext.Exec(`DELETE FROM thin_vault_docs WHERE doc_id = 'x2'`); err != nil {
		t.Fatalf("ext delete: %v", err)
	}
	snapshotEquivalence(t, v, dbPath, []string{"aurora ridge", "glacier moraine camp"})
}
