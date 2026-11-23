// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package localvault

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	core "github.com/rocareer/rolling-memory/internal/core"
	"github.com/rocareer/rolling-memory/internal/graphmodel"
	plain "github.com/rocareer/rolling-memory/sdk/go/pulse/plain"
)

// WithCoreEngine exposes the immutable corpus snapshot's retrieval engine to a
// read-only graph projection. The callback must not mutate the engine or the
// vault; the engine is shared by every recall path in the process.
func (v *Vault) WithCoreEngine(ctx context.Context, fn func(*core.Engine) error) error {
	if v == nil || v.db == nil {
		return errors.New("localvault: nil vault")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	snap, release, err := v.acquireCorpusSnapshot(ctx)
	if err != nil {
		return err
	}
	if snap == nil || len(snap.docs) == 0 {
		return errors.New("localvault: empty corpus snapshot")
	}
	defer release()
	eng := snap.ensureSnapshotCoreEngine(v)
	if eng == nil || eng.Index == nil || eng.Index.N == 0 {
		return errors.New("localvault: core engine unavailable")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return fn(eng)
}

// AcquireCoreEngine leases the corpus snapshot's retrieval engine to a caller
// that keeps the pointer beyond one call (the graph projection caches it across
// view renders). The returned release func must be called exactly once when the
// caller stops using the engine; until then a replaced published generation
// stays mapped. Single-shot readers use WithCoreEngine instead.
func (v *Vault) AcquireCoreEngine(ctx context.Context) (*core.Engine, func(), error) {
	if v == nil || v.db == nil {
		return nil, nil, errors.New("localvault: nil vault")
	}
	snap, release, err := v.acquireCorpusSnapshot(ctx)
	if err != nil {
		return nil, nil, err
	}
	if snap == nil || len(snap.docs) == 0 {
		if release != nil {
			release()
		}
		return nil, nil, errors.New("localvault: empty corpus snapshot")
	}
	eng := snap.ensureSnapshotCoreEngine(v)
	if eng == nil || eng.Index == nil || eng.Index.N == 0 {
		release()
		return nil, nil, errors.New("localvault: core engine unavailable")
	}
	return eng, release, nil
}

// GraphUnits returns the plaintext-free unit metadata used to assemble the
// memory graph. Raw body text is read lazily per visible unit through
// LoadLocalDocument, so a full graph build never materializes the whole vault
// text in memory.
func (v *Vault) GraphUnits(ctx context.Context) ([]graphmodel.UnitRow, error) {
	if v == nil || v.db == nil {
		return nil, errors.New("localvault: nil vault")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	rows := make([]graphmodel.UnitRow, 0, 1024)
	seen := make(map[string]struct{}, 1024)

	docRows, err := v.db.QueryContext(ctx, `
		SELECT doc_id,
		       COALESCE(layer, 'episodic') AS layer,
		       occurred_at,
		       inserted_at,
		       CASE WHEN substr(trim(raw_text), 1, 5) = '@ref:' THEN trim(raw_text) ELSE '' END AS ref_pointer,
		       COALESCE(address_uri, ''),
		       COALESCE(locus, '')
		FROM rm_units`)
	if err != nil && !isMissingTableErr(err) {
		return nil, fmt.Errorf("localvault: graph docs: %w", err)
	}
	if err == nil {
		for docRows.Next() {
			var r graphmodel.UnitRow
			r.Source = graphmodel.SourceDoc
			if scanErr := docRows.Scan(&r.UnitID, &r.Layer, &r.OccurredAt, &r.InsertedAt, &r.RefPointer, &r.AddressURI, &r.Locus); scanErr != nil {
				_ = docRows.Close()
				return nil, fmt.Errorf("localvault: scan graph doc: %w", scanErr)
			}
			r.UnitID = strings.TrimSpace(r.UnitID)
			if r.UnitID == "" {
				continue
			}
			r.Layer = normalizeGraphLayer(r.Layer)
			if _, dup := seen[r.UnitID]; dup {
				continue
			}
			seen[r.UnitID] = struct{}{}
			rows = append(rows, r)
		}
		if scanErr := docRows.Err(); scanErr != nil {
			_ = docRows.Close()
			return nil, fmt.Errorf("localvault: iterate graph docs: %w", scanErr)
		}
		_ = docRows.Close()
	}

	bgRows, err := v.db.QueryContext(ctx, `
		SELECT unit_id, COALESCE(rel_path, ''), COALESCE(abs_path, ''), COALESCE(anchor, ''),
		       occurred_at, inserted_at, COALESCE(address_uri, '')
		FROM rm_bg_chunks`)
	if err != nil && !isMissingTableErr(err) {
		return nil, fmt.Errorf("localvault: graph background chunks: %w", err)
	}
	if err == nil {
		for bgRows.Next() {
			var r graphmodel.UnitRow
			r.Source = graphmodel.SourceBG
			r.Layer = LayerBackground
			if scanErr := bgRows.Scan(&r.UnitID, &r.RelPath, &r.AbsPath, &r.Anchor, &r.OccurredAt, &r.InsertedAt, &r.AddressURI); scanErr != nil {
				_ = bgRows.Close()
				return nil, fmt.Errorf("localvault: scan graph background chunk: %w", scanErr)
			}
			r.UnitID = strings.TrimSpace(r.UnitID)
			if r.UnitID == "" {
				continue
			}
			if _, dup := seen[r.UnitID]; dup {
				continue
			}
			seen[r.UnitID] = struct{}{}
			rows = append(rows, r)
		}
		if scanErr := bgRows.Err(); scanErr != nil {
			_ = bgRows.Close()
			return nil, fmt.Errorf("localvault: iterate graph background chunks: %w", scanErr)
		}
		_ = bgRows.Close()
	}

	noeticRows, err := v.db.QueryContext(ctx, `
		SELECT noetic_id, occurred_at, inserted_at,
		       COALESCE(query_text, ''), COALESCE(answer_text, ''),
		       COALESCE(cited_unit_ids_json, '[]'), COALESCE(address_uri, '')
		FROM rm_noetic
		WHERE COALESCE(NULLIF(status, ''), 'active') = 'active'`)
	if err != nil && !isMissingTableErr(err) {
		return nil, fmt.Errorf("localvault: graph noetic: %w", err)
	}
	if err == nil {
		for noeticRows.Next() {
			var r graphmodel.UnitRow
			var citedJSON string
			r.Source = graphmodel.SourceNoetic
			r.Layer = LayerNoetic
			r.Status = "active"
			if scanErr := noeticRows.Scan(&r.UnitID, &r.OccurredAt, &r.InsertedAt, &r.QueryText, &r.AnswerText, &citedJSON, &r.AddressURI); scanErr != nil {
				_ = noeticRows.Close()
				return nil, fmt.Errorf("localvault: scan graph noetic: %w", scanErr)
			}
			r.UnitID = strings.TrimSpace(r.UnitID)
			if r.UnitID == "" {
				continue
			}
			if _, dup := seen[r.UnitID]; dup {
				continue
			}
			seen[r.UnitID] = struct{}{}
			if unmarshalErr := json.Unmarshal([]byte(citedJSON), &r.CitedUnitIDs); unmarshalErr != nil {
				r.CitedUnitIDs = nil
			}
			rows = append(rows, r)
		}
		if scanErr := noeticRows.Err(); scanErr != nil {
			_ = noeticRows.Close()
			return nil, fmt.Errorf("localvault: iterate graph noetic: %w", scanErr)
		}
		_ = noeticRows.Close()
	}

	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].OccurredAt != rows[j].OccurredAt {
			return rows[i].OccurredAt > rows[j].OccurredAt
		}
		return rows[i].UnitID < rows[j].UnitID
	})
	return rows, nil
}

// SurfaceHashes hashes a batch of graph label candidates with one encoder
// instance. The tokenizer hashes the lowercased, trimmed surface, so callers
// must pass surfaces prepared the same way.
func (v *Vault) SurfaceHashes(surfaces []string) map[string]uint64 {
	out := make(map[string]uint64, len(surfaces))
	if v == nil || len(surfaces) == 0 {
		return out
	}
	enc := plain.NewBlindEncoderFromRawKey(v.secretKey)
	for _, surface := range surfaces {
		if _, seen := out[surface]; seen {
			continue
		}
		out[surface] = enc.KeyedHash64(surface)
	}
	return out
}

// GraphCitations returns the active L3 citation map (noetic unit -> cited unit
// ids). Citation edges live only in the vault tables; the local core engine
// deliberately does not ingest them.
func (v *Vault) GraphCitations(ctx context.Context) (map[string][]string, error) {
	if v == nil || v.db == nil {
		return nil, errors.New("localvault: nil vault")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rows, err := v.db.QueryContext(ctx, `
		SELECT noetic_id, COALESCE(cited_unit_ids_json, '[]')
		FROM rm_noetic
		WHERE COALESCE(NULLIF(status, ''), 'active') = 'active'`)
	if err != nil {
		if isMissingTableErr(err) {
			return map[string][]string{}, nil
		}
		return nil, fmt.Errorf("localvault: graph citations: %w", err)
	}
	defer rows.Close()
	out := make(map[string][]string, 64)
	for rows.Next() {
		var id, citedJSON string
		if scanErr := rows.Scan(&id, &citedJSON); scanErr != nil {
			return nil, fmt.Errorf("localvault: scan graph citation: %w", scanErr)
		}
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		var cited []string
		if unmarshalErr := json.Unmarshal([]byte(citedJSON), &cited); unmarshalErr != nil {
			continue
		}
		out[id] = cited
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("localvault: iterate graph citations: %w", err)
	}
	return out, nil
}

func normalizeGraphLayer(layer string) string {
	switch strings.ToLower(strings.TrimSpace(layer)) {
	case "background", "bg", "substrate", "document", "doc":
		return LayerBackground
	case "noetic", "qa", "insight":
		return LayerNoetic
	default:
		return LayerEpisodic
	}
}

func isMissingTableErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, sql.ErrNoRows) {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "no such table")
}
