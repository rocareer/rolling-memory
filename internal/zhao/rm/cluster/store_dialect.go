// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package cluster

import (
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
)

// StoreDialect identifies the underlying SQL database engine dialect and isolation tier:
//   - DialectSQLite         ("sqlite"):          single-user client SQLite vault (Rolling-Memory default);
//   - DialectPostgresCipher ("postgres_cipher"): multi-tenant server PostgreSQL database for `pulse_cipher` (physically ZERO `raw_text` column);
//   - DialectPostgresPlain  ("postgres_plain"):  multi-tenant server PostgreSQL database for `pulse_plain` (physically isolated database with `raw_text` column);
//   - DialectPostgres       ("postgres"):        alias for DialectPostgresCipher.
type StoreDialect string

const (
	DialectSQLite         StoreDialect = "sqlite"
	DialectPostgres       StoreDialect = "postgres"
	DialectPostgresCipher StoreDialect = "postgres_cipher"
	DialectPostgresPlain  StoreDialect = "postgres_plain"
)

var (
	// ErrCipherStoreRejectsPlaintext is returned when a caller attempts to ingest non-empty RawText into the CipherDB.
	ErrCipherStoreRejectsPlaintext = errors.New("rm/store: pulse_cipher database physically forbids non-empty RawText")
	// ErrPlainStoreRequiresPlaintext is returned when a caller attempts to ingest empty RawText into the PlainDB.
	ErrPlainStoreRequiresPlaintext = errors.New("rm/store: pulse_plain database requires non-empty RawText")
	// ErrDualStoreModeConflict is returned when a (tenant_id, scope) partition attempts to cross between CipherDB and PlainDB.
	ErrDualStoreModeConflict = errors.New("rm/store: tenant scope pulse_mode conflict across isolated databases")
)

// PostgresCipherSchema is the multi-tenant PostgreSQL schema for the `pulse_cipher` database (CipherDB).
// All 11 core document columns, 7 term columns, and the citation-edge table match client SQLite
// (`SQLiteSchema`) 1:1, prefixed by `(tenant_id, scope)`, and physically omit any `raw_text` column
// so plaintext can never be stored.
const PostgresCipherSchema = `
CREATE TABLE IF NOT EXISTS rm_docs_pg (
    tenant_id   TEXT NOT NULL,
    scope       TEXT NOT NULL,
    unit_id     TEXT NOT NULL,
    occurred_at BIGINT NOT NULL DEFAULT 0,
    narrated_at BIGINT NOT NULL DEFAULT 0,
    ingested_at BIGINT NOT NULL DEFAULT 0,
    author_hash BIGINT NOT NULL DEFAULT 0,
    doc_hash    BIGINT NOT NULL DEFAULT 0,
    block_hash  BIGINT NOT NULL DEFAULT 0,
    line_start  INTEGER NOT NULL DEFAULT 0,
    line_end    INTEGER NOT NULL DEFAULT 0,
    layer       TEXT NOT NULL DEFAULT '',
    term_count  INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (tenant_id, scope, unit_id)
);
CREATE TABLE IF NOT EXISTS rm_terms_pg (
    tenant_id   TEXT NOT NULL,
    scope       TEXT NOT NULL,
    term_hash   BIGINT NOT NULL,
    unit_id     TEXT NOT NULL,
    bits_x10    INTEGER NOT NULL,
    class_flags INTEGER NOT NULL DEFAULT 0,
    group_hash  BIGINT NOT NULL DEFAULT 0,
    occurrences INTEGER NOT NULL DEFAULT 1,
    pos_blob    BYTEA,
    PRIMARY KEY (tenant_id, scope, term_hash, unit_id)
);
CREATE INDEX IF NOT EXISTS rm_terms_pg_unit_idx ON rm_terms_pg(tenant_id, scope, unit_id);
CREATE TABLE IF NOT EXISTS rm_citations_pg (
    tenant_id TEXT NOT NULL,
    scope     TEXT NOT NULL,
    unit_id   TEXT NOT NULL,
    cited_id  TEXT NOT NULL,
    ord       INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (tenant_id, scope, unit_id, cited_id)
);
`

// PostgresSchema is retained as a backward-compatible alias for PostgresCipherSchema.
const PostgresSchema = PostgresCipherSchema

// PostgresPlainSchema is the multi-tenant PostgreSQL schema for the `pulse_plain` database (PlainDB).
// It is physically isolated from CipherDB and shares the exact same 12 columns as client SQLite (`rm_docs` + `raw_text`)
// plus the multi-tenant `(tenant_id, scope)` primary key prefix.
const PostgresPlainSchema = `
CREATE TABLE IF NOT EXISTS rm_docs_plain_pg (
    tenant_id   TEXT NOT NULL,
    scope       TEXT NOT NULL,
    unit_id     TEXT NOT NULL,
    occurred_at BIGINT NOT NULL DEFAULT 0,
    narrated_at BIGINT NOT NULL DEFAULT 0,
    ingested_at BIGINT NOT NULL DEFAULT 0,
    author_hash BIGINT NOT NULL DEFAULT 0,
    doc_hash    BIGINT NOT NULL DEFAULT 0,
    block_hash  BIGINT NOT NULL DEFAULT 0,
    line_start  INTEGER NOT NULL DEFAULT 0,
    line_end    INTEGER NOT NULL DEFAULT 0,
    layer       TEXT NOT NULL DEFAULT '',
    term_count  INTEGER NOT NULL DEFAULT 0,
    raw_text    TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (tenant_id, scope, unit_id)
);
CREATE TABLE IF NOT EXISTS rm_terms_plain_pg (
    tenant_id   TEXT NOT NULL,
    scope       TEXT NOT NULL,
    term_hash   BIGINT NOT NULL,
    unit_id     TEXT NOT NULL,
    bits_x10    INTEGER NOT NULL,
    class_flags INTEGER NOT NULL DEFAULT 0,
    group_hash  BIGINT NOT NULL DEFAULT 0,
    occurrences INTEGER NOT NULL DEFAULT 1,
    pos_blob    BYTEA,
    PRIMARY KEY (tenant_id, scope, term_hash, unit_id)
);
CREATE INDEX IF NOT EXISTS rm_terms_plain_pg_unit_idx ON rm_terms_plain_pg(tenant_id, scope, unit_id);
CREATE TABLE IF NOT EXISTS rm_citations_plain_pg (
    tenant_id TEXT NOT NULL,
    scope     TEXT NOT NULL,
    unit_id   TEXT NOT NULL,
    cited_id  TEXT NOT NULL,
    ord       INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (tenant_id, scope, unit_id, cited_id)
);
`

// StoreOptions configures an adaptive single-user SQLite or multi-tenant PostgreSQL Store.
type StoreOptions struct {
	Dialect    StoreDialect
	TenantID   string
	Scope      string
	OwnsDB     bool
	SkipSchema bool
}

// OpenStoreWithOptions wraps an existing *sql.DB connection pool for single-user SQLite
// (`DialectSQLite`), multi-tenant Cipher PostgreSQL (`DialectPostgresCipher` / `DialectPostgres`),
// or multi-tenant Plain PostgreSQL (`DialectPostgresPlain`).
func OpenStoreWithOptions(db *sql.DB, opts StoreOptions) (*Store, error) {
	if db == nil {
		return nil, errors.New("rm/store: nil *sql.DB")
	}
	dialect := opts.Dialect
	if dialect == "" {
		dialect = DialectSQLite
	}
	switch dialect {
	case DialectSQLite, DialectPostgres, DialectPostgresCipher, DialectPostgresPlain:
	default:
		return nil, fmt.Errorf("rm/store: unsupported dialect %q", dialect)
	}
	tenantID := strings.TrimSpace(opts.TenantID)
	if tenantID == "" {
		if dialect != DialectSQLite {
			return nil, errors.New("rm/store: PostgreSQL multi-tenant store requires a non-empty TenantID")
		}
		tenantID = "local"
	}
	scope := strings.TrimSpace(opts.Scope)
	if scope == "" {
		scope = "default"
	}
	if !opts.SkipSchema {
		schema := SQLiteSchema
		switch dialect {
		case DialectPostgres, DialectPostgresCipher:
			schema = PostgresCipherSchema
		case DialectPostgresPlain:
			schema = PostgresPlainSchema
		}
		if _, err := db.Exec(schema); err != nil {
			return nil, fmt.Errorf("rm/store: apply %s schema: %w", dialect, err)
		}
	}
	return &Store{
		db:       db,
		dialect:  dialect,
		tenantID: tenantID,
		scope:    scope,
		ownsDB:   opts.OwnsDB,
	}, nil
}

// DualClusterStore manages cloud engine's dual-database physical isolation architecture:
//   - CipherDB (`pulse_cipher`): multi-tenant PostgreSQL database with ZERO plaintext columns;
//   - PlainDB  (`pulse_plain`):  separate multi-tenant PostgreSQL database storing pulse + plaintext.
type DualClusterStore struct {
	cipherDB  *sql.DB
	plainDB   *sql.DB
	mu        sync.RWMutex
	boundMode map[string]string
}

// OpenDualClusterStore initializes the schema on both physical database pools (`cipherDB` and `plainDB`)
// and returns a router that enforces strict physical isolation between `pulse_cipher` and `pulse_plain`.
func OpenDualClusterStore(cipherDB, plainDB *sql.DB) (*DualClusterStore, error) {
	if cipherDB == nil && plainDB == nil {
		return nil, errors.New("rm/store: OpenDualClusterStore requires at least one non-nil database pool")
	}
	if cipherDB != nil {
		if _, err := cipherDB.Exec(PostgresCipherSchema); err != nil {
			return nil, fmt.Errorf("rm/store: init CipherDB schema: %w", err)
		}
	}
	if plainDB != nil {
		if _, err := plainDB.Exec(PostgresPlainSchema); err != nil {
			return nil, fmt.Errorf("rm/store: init PlainDB schema: %w", err)
		}
	}
	return &DualClusterStore{
		cipherDB:  cipherDB,
		plainDB:   plainDB,
		boundMode: make(map[string]string, 64),
	}, nil
}

// StoreForTenant resolves the physically isolated multi-tenant Store for `(tenantID, scope, pulseMode)`.
// Mixing `pulse_cipher` and `pulse_plain` on the same `(tenantID, scope)` is rejected fail-closed.
func (d *DualClusterStore) StoreForTenant(tenantID, scope, pulseMode string) (*Store, error) {
	if d == nil {
		return nil, errors.New("rm/store: nil DualClusterStore")
	}
	tid := strings.TrimSpace(tenantID)
	sc := strings.TrimSpace(scope)
	pm := strings.ToLower(strings.TrimSpace(pulseMode))
	if tid == "" || sc == "" {
		return nil, errors.New("rm/store: tenantID and scope are required")
	}
	if pm == "" {
		pm = "pulse_cipher"
	}
	if pm != "pulse_cipher" && pm != "pulse_plain" {
		return nil, fmt.Errorf("rm/store: invalid pulse_mode %q (must be pulse_cipher or pulse_plain)", pm)
	}
	partKey := tid + "\x00" + sc
	d.mu.RLock()
	prev, exists := d.boundMode[partKey]
	d.mu.RUnlock()
	if exists && prev != pm {
		return nil, fmt.Errorf("%w: (%s, %s) bound to %s, got %s", ErrDualStoreModeConflict, tid, sc, prev, pm)
	}
	d.mu.Lock()
	if prev, exists = d.boundMode[partKey]; exists && prev != pm {
		d.mu.Unlock()
		return nil, fmt.Errorf("%w: (%s, %s) bound to %s, got %s", ErrDualStoreModeConflict, tid, sc, prev, pm)
	}
	d.boundMode[partKey] = pm
	d.mu.Unlock()

	if pm == "pulse_cipher" {
		if d.cipherDB == nil {
			return nil, errors.New("rm/store: CipherDB pool is not configured")
		}
		return OpenStoreWithOptions(d.cipherDB, StoreOptions{
			Dialect:    DialectPostgresCipher,
			TenantID:   tid,
			Scope:      sc,
			SkipSchema: true,
		})
	}
	if d.plainDB == nil {
		return nil, errors.New("rm/store: PlainDB pool is not configured")
	}
	return OpenStoreWithOptions(d.plainDB, StoreOptions{
		Dialect:    DialectPostgresPlain,
		TenantID:   tid,
		Scope:      sc,
		SkipSchema: true,
	})
}

// Dialect returns the store's active database dialect.
func (s *Store) Dialect() StoreDialect {
	if s == nil || s.dialect == "" {
		return DialectSQLite
	}
	return s.dialect
}

// TenantScope returns the store's bound `(tenantID, scope)` partition coordinate.
func (s *Store) TenantScope() (string, string) {
	if s == nil {
		return "", ""
	}
	return s.tenantID, s.scope
}

func (s *Store) pgTables() (docsTable, termsTable, citesTable string, isPlain bool) {
	if s.dialect == DialectPostgresPlain {
		return "rm_docs_plain_pg", "rm_terms_plain_pg", "rm_citations_plain_pg", true
	}
	return "rm_docs_pg", "rm_terms_pg", "rm_citations_pg", false
}

// LoadRawText returns the stored plaintext for a unit (available on single-user SQLite and multi-tenant PlainDB;
// always returns empty on CipherDB since CipherDB physically has no raw_text column).
func (s *Store) LoadRawText(unitID string) (string, error) {
	if s == nil || s.db == nil || strings.TrimSpace(unitID) == "" {
		return "", nil
	}
	if s.dialect == DialectSQLite {
		var raw string
		err := s.db.QueryRow(`SELECT raw_text FROM rm_docs WHERE unit_id = ?`, unitID).Scan(&raw)
		return raw, err
	}
	if s.dialect == DialectPostgresPlain {
		var raw string
		err := s.db.QueryRow(`SELECT raw_text FROM rm_docs_plain_pg WHERE tenant_id = $1 AND scope = $2 AND unit_id = $3`, s.tenantID, s.scope, unitID).Scan(&raw)
		return raw, err
	}
	return "", nil
}

func (s *Store) deleteDocsPostgres(unitIDs ...string) error {
	docsTbl, atomsTbl, citesTbl, _ := s.pgTables()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	delTerms, err := tx.Prepare(`DELETE FROM ` + atomsTbl + ` WHERE tenant_id = $1 AND scope = $2 AND unit_id = $3`)
	if err != nil {
		return err
	}
	defer delTerms.Close()
	delCites, err := tx.Prepare(`DELETE FROM ` + citesTbl + ` WHERE tenant_id = $1 AND scope = $2 AND unit_id = $3`)
	if err != nil {
		return err
	}
	defer delCites.Close()
	delDocs, err := tx.Prepare(`DELETE FROM ` + docsTbl + ` WHERE tenant_id = $1 AND scope = $2 AND unit_id = $3`)
	if err != nil {
		return err
	}
	defer delDocs.Close()
	for _, uid := range unitIDs {
		if uid == "" {
			continue
		}
		if _, err := delTerms.Exec(s.tenantID, s.scope, uid); err != nil {
			return err
		}
		if _, err := delCites.Exec(s.tenantID, s.scope, uid); err != nil {
			return err
		}
		if _, err := delDocs.Exec(s.tenantID, s.scope, uid); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) ingestPostgres(docs []Doc) error {
	docsTbl, atomsTbl, citesTbl, isPlain := s.pgTables()
	for _, d := range docs {
		if d.UnitID == "" {
			continue
		}
		if !isPlain && strings.TrimSpace(d.RawText) != "" {
			return ErrCipherStoreRejectsPlaintext
		}
		if isPlain && strings.TrimSpace(d.RawText) == "" {
			return ErrPlainStoreRequiresPlaintext
		}
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var docSQL string
	if isPlain {
		docSQL = `
		INSERT INTO ` + docsTbl + `(
			tenant_id, scope, unit_id, occurred_at, narrated_at, ingested_at,
			author_hash, doc_hash, block_hash, line_start, line_end, layer, term_count, raw_text
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
		ON CONFLICT (tenant_id, scope, unit_id) DO UPDATE SET
			occurred_at = EXCLUDED.occurred_at,
			narrated_at = EXCLUDED.narrated_at,
			ingested_at = EXCLUDED.ingested_at,
			author_hash = EXCLUDED.author_hash,
			doc_hash    = EXCLUDED.doc_hash,
			block_hash  = EXCLUDED.block_hash,
			line_start  = EXCLUDED.line_start,
			line_end    = EXCLUDED.line_end,
			layer       = EXCLUDED.layer,
			term_count  = EXCLUDED.term_count,
			raw_text    = EXCLUDED.raw_text`
	} else {
		docSQL = `
		INSERT INTO ` + docsTbl + `(
			tenant_id, scope, unit_id, occurred_at, narrated_at, ingested_at,
			author_hash, doc_hash, block_hash, line_start, line_end, layer, term_count
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
		ON CONFLICT (tenant_id, scope, unit_id) DO UPDATE SET
			occurred_at = EXCLUDED.occurred_at,
			narrated_at = EXCLUDED.narrated_at,
			ingested_at = EXCLUDED.ingested_at,
			author_hash = EXCLUDED.author_hash,
			doc_hash    = EXCLUDED.doc_hash,
			block_hash  = EXCLUDED.block_hash,
			line_start  = EXCLUDED.line_start,
			line_end    = EXCLUDED.line_end,
			layer       = EXCLUDED.layer,
			term_count  = EXCLUDED.term_count`
	}
	docStmt, err := tx.Prepare(docSQL)
	if err != nil {
		return err
	}
	defer docStmt.Close()

	deleteTermsStmt, err := tx.Prepare(`DELETE FROM ` + atomsTbl + ` WHERE tenant_id = $1 AND scope = $2 AND unit_id = $3`)
	if err != nil {
		return err
	}
	defer deleteTermsStmt.Close()

	deleteCitesStmt, err := tx.Prepare(`DELETE FROM ` + citesTbl + ` WHERE tenant_id = $1 AND scope = $2 AND unit_id = $3`)
	if err != nil {
		return err
	}
	defer deleteCitesStmt.Close()

	citeStmt, err := tx.Prepare(`
		INSERT INTO ` + citesTbl + `(tenant_id, scope, unit_id, cited_id, ord)
		VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (tenant_id, scope, unit_id, cited_id) DO UPDATE SET ord = EXCLUDED.ord`)
	if err != nil {
		return err
	}
	defer citeStmt.Close()

	atomStmt, err := tx.Prepare(`
		INSERT INTO ` + atomsTbl + `(
			tenant_id, scope, term_hash, unit_id, bits_x10, class_flags, group_hash, occurrences, pos_blob
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		ON CONFLICT (tenant_id, scope, term_hash, unit_id) DO UPDATE SET
			bits_x10    = EXCLUDED.bits_x10,
			class_flags = EXCLUDED.class_flags,
			group_hash  = EXCLUDED.group_hash,
			occurrences = EXCLUDED.occurrences,
			pos_blob    = EXCLUDED.pos_blob`)
	if err != nil {
		return err
	}
	defer atomStmt.Close()

	for _, d := range docs {
		if d.UnitID == "" {
			continue
		}
		if _, err := deleteTermsStmt.Exec(s.tenantID, s.scope, d.UnitID); err != nil {
			return err
		}
		if _, err := deleteCitesStmt.Exec(s.tenantID, s.scope, d.UnitID); err != nil {
			return err
		}
		if isPlain {
			if _, err := docStmt.Exec(
				s.tenantID, s.scope, d.UnitID, d.OccurredAt, d.NarratedAt, d.IngestedAt,
				int64(d.AuthorHash), int64(d.DocHash), int64(d.BlockHash),
				d.LineStart, d.LineEnd, d.Layer, len(d.Terms), d.RawText,
			); err != nil {
				return err
			}
		} else {
			if _, err := docStmt.Exec(
				s.tenantID, s.scope, d.UnitID, d.OccurredAt, d.NarratedAt, d.IngestedAt,
				int64(d.AuthorHash), int64(d.DocHash), int64(d.BlockHash),
				d.LineStart, d.LineEnd, d.Layer, len(d.Terms),
			); err != nil {
				return err
			}
		}
		for ord, cid := range d.CitedIDs {
			if cid == "" || cid == d.UnitID {
				continue
			}
			if _, err := citeStmt.Exec(s.tenantID, s.scope, d.UnitID, cid, ord); err != nil {
				return err
			}
		}
		for hash, atom := range d.Terms {
			occ := d.Counts[hash]
			if occ < 1 {
				occ = 1
			}
			var posBlob []byte
			if len(d.Positions) > 0 {
				posBlob = encodePositionsBlob(d.Positions[hash])
			} else if atom.Sent > 0 && atom.Slot > 0 {
				posBlob = encodePositionsBlob([]TermPosition{{Sentence: int(atom.Sent - 1), Slot: int(atom.Slot - 1)}})
			}
			packedFlags := int64(atom.Flags) | (int64(packTermRoleExt(atom.Role, atom.ExtFlags)) << 8)
			if _, err := atomStmt.Exec(s.tenantID, s.scope, int64(hash), d.UnitID, int64(atom.Bits), packedFlags, int64(atom.Group), int64(occ), posBlob); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func (s *Store) docCountPostgres() (int, error) {
	docsTbl, _, _, _ := s.pgTables()
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM `+docsTbl+` WHERE tenant_id = $1 AND scope = $2`, s.tenantID, s.scope).Scan(&n)
	return n, err
}

func (s *Store) loadIndexPostgres() (*Index, error) {
	docsTbl, atomsTbl, citesTbl, _ := s.pgTables()
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	idx := &Index{
		UnitOf:    make(map[string]int32, 1024),
		Mass:      make(map[uint64]float64, 4096),
		DF:        make(map[uint64]int, 4096),
		Postings:  make(map[uint64][]Posting, 4096),
		TermBits:  make(map[uint64]uint16, 4096),
		TermFlags: make(map[uint64]uint8, 4096),
	}

	docRows, err := tx.Query(`
		SELECT unit_id, occurred_at, narrated_at, ingested_at, author_hash, doc_hash, block_hash, line_start, line_end, layer, term_count
		FROM `+docsTbl+`
		WHERE tenant_id = $1 AND scope = $2
		ORDER BY unit_id`, s.tenantID, s.scope)
	if err != nil {
		return nil, err
	}
	var metas []DocMeta
	var termCounts []int
	var totalTermPostings int
	for docRows.Next() {
		var unitID, layer string
		var occurredAt, narratedAt, ingestedAt, authorHash, docHash, blockHash int64
		var lineStart, lineEnd, termCount int64
		if err := docRows.Scan(&unitID, &occurredAt, &narratedAt, &ingestedAt, &authorHash, &docHash, &blockHash, &lineStart, &lineEnd, &layer, &termCount); err != nil {
			docRows.Close()
			return nil, err
		}
		idx.UnitOf[unitID] = int32(len(idx.UnitIDs))
		idx.UnitIDs = append(idx.UnitIDs, unitID)
		metas = append(metas, DocMeta{
			OccurredAt: occurredAt,
			NarratedAt: narratedAt,
			IngestedAt: ingestedAt,
			AuthorHash: uint64(authorHash),
			DocHash:    uint64(docHash),
			BlockHash:  uint64(blockHash),
			LineStart:  int(lineStart),
			LineEnd:    int(lineEnd),
			Layer:      layer,
		})
		tc := int(termCount)
		if tc < 4 {
			tc = 4
		}
		termCounts = append(termCounts, tc)
		totalTermPostings += tc
	}
	docRows.Close()
	if err := docRows.Err(); err != nil {
		return nil, err
	}
	idx.N = len(idx.UnitIDs)
	idx.DocMass = make([]float64, idx.N)
	idx.DocTerms = make([][]uint64, idx.N)
	idx.UnitMeta = make([]DocMeta, idx.N)
	idx.UnitPositions = make([]map[uint64][]TermPosition, idx.N)
	idx.UnitBits = make([]map[uint64]uint16, idx.N)
	idx.UnitFlags = make([]map[uint64]uint8, idx.N)
	idx.UnitRoleExt = make([]map[uint64]uint8, idx.N)
	idx.UnitGroups = make([]map[uint64]uint64, idx.N)
	for at, stored := range metas {
		idx.UnitMeta[at] = stored
	}

	citeRows, err := tx.Query(`
		SELECT unit_id, cited_id FROM `+citesTbl+`
		WHERE tenant_id = $1 AND scope = $2
		ORDER BY unit_id, ord, cited_id`, s.tenantID, s.scope)
	if err != nil {
		return nil, err
	}
	for citeRows.Next() {
		var unitID, citedID string
		if err := citeRows.Scan(&unitID, &citedID); err != nil {
			citeRows.Close()
			return nil, err
		}
		if u, ok := idx.UnitOf[unitID]; ok && citedID != "" {
			idx.UnitMeta[u].CitedIDs = append(idx.UnitMeta[u].CitedIDs, citedID)
		}
	}
	citeRows.Close()
	if err := citeRows.Err(); err != nil {
		return nil, err
	}

	if totalTermPostings < 256 {
		totalTermPostings = 256
	}
	postingArena := make([]Posting, 0, totalTermPostings)
	posArena := make([]TermPosition, 0, totalTermPostings*2)
	var curHash uint64
	var hasCurHash bool
	var runStart int

	termRows, err := tx.Query(`
		SELECT term_hash, unit_id, bits_x10, class_flags, group_hash, occurrences, pos_blob
		FROM `+atomsTbl+`
		WHERE tenant_id = $1 AND scope = $2
		ORDER BY term_hash, unit_id`, s.tenantID, s.scope)
	if err != nil {
		return nil, err
	}
	for termRows.Next() {
		var hash int64
		var unitID string
		var bitsX10, classFlags, groupHash, occ int64
		var posBlob []byte
		if err := termRows.Scan(&hash, &unitID, &bitsX10, &classFlags, &groupHash, &occ, &posBlob); err != nil {
			termRows.Close()
			return nil, err
		}
		unit, ok := idx.UnitOf[unitID]
		if !ok {
			termRows.Close()
			return idx, fmt.Errorf("rm/store: orphan term row for unknown unit %q", unitID)
		}
		h := uint64(hash)
		if !hasCurHash {
			curHash = h
			hasCurHash = true
			runStart = 0
		} else if h != curHash {
			runEnd := len(postingArena)
			idx.Postings[curHash] = postingArena[runStart:runEnd:runEnd]
			idx.DF[curHash] = runEnd - runStart
			curHash = h
			runStart = runEnd
		}
		if occ < 1 {
			occ = 1
		} else if occ > math.MaxInt32 {
			occ = math.MaxInt32
		}
		postingArena = append(postingArena, Posting{Unit: unit, Occ: int32(occ)})
		hint := termCounts[unit]
		if groupHash != 0 {
			if idx.UnitGroups[unit] == nil {
				idx.UnitGroups[unit] = make(map[uint64]uint64, hint)
			}
			idx.UnitGroups[unit][h] = uint64(groupHash)
		}
		if idx.UnitBits[unit] == nil {
			idx.UnitBits[unit] = make(map[uint64]uint16, hint)
		}
		if bitsX10 > 0 {
			b16 := uint16(bitsX10)
			if b16 > idx.TermBits[h] {
				idx.TermBits[h] = b16
			}
			if b16 > idx.UnitBits[unit][h] {
				idx.UnitBits[unit][h] = b16
			}
		} else if _, exists := idx.UnitBits[unit][h]; !exists {
			idx.UnitBits[unit][h] = 0
		}
		if idx.UnitFlags[unit] == nil {
			idx.UnitFlags[unit] = make(map[uint64]uint8, hint/2+1)
		}
		fl8 := uint8(classFlags)
		idx.UnitFlags[unit][h] |= fl8
		if fl8 != 0 {
			idx.TermFlags[h] |= fl8
		}
		if re8 := uint8(classFlags >> 8); re8 != 0 {
			if idx.UnitRoleExt[unit] == nil {
				idx.UnitRoleExt[unit] = make(map[uint64]uint8, hint/2+1)
			}
			idx.UnitRoleExt[unit][h] = re8
		}
		if len(posBlob) > 0 {
			if idx.UnitPositions[unit] == nil {
				idx.UnitPositions[unit] = make(map[uint64][]TermPosition, hint)
			}
			var posSlice []TermPosition
			var decErr error
			posSlice, posArena, decErr = decodePositionsIntoArena(posBlob, posArena)
			if decErr != nil {
				termRows.Close()
				return idx, decErr
			}
			idx.UnitPositions[unit][h] = posSlice
		}
	}
	if hasCurHash {
		runEnd := len(postingArena)
		idx.Postings[curHash] = postingArena[runStart:runEnd:runEnd]
		idx.DF[curHash] = runEnd - runStart
	}
	termRows.Close()
	if err := termRows.Err(); err != nil {
		return nil, err
	}
	idx.finalizeMasses()
	return idx, nil
}
