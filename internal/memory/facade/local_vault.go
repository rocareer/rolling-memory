// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package facade

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rocareer/rolling-memory/internal/clock"
	"github.com/rocareer/rolling-memory/internal/config"
	"github.com/rocareer/rolling-memory/internal/evalns"
	"github.com/rocareer/rolling-memory/internal/ids"
	"github.com/rocareer/rolling-memory/internal/memory/localvault"
	"github.com/rocareer/rolling-memory/internal/memory/recall"
	"github.com/rocareer/rolling-memory/internal/text"
	"github.com/rocareer/rolling-memory/sdk/go/public/tokenzr"
	plain "github.com/rocareer/rolling-memory/sdk/go/pulse/plain"
)

// LocalVault returns an initialized Rolling Autonoetic Memory Engine Local Vault Client
// bound to the local SQLite database and its in-process local vault transport (zero network egress).
// localPersonaIdentity resolves the persona coordinate stamped onto every pulse: the
// answering assistant (`ROLLING_MEMORY_ASSISTANT_ID`, else "default"), the concrete
// incarnation (`ROLLING_MEMORY_INCARNATION_ID`, empty until a caller supplies its session),
// and the workspace locus (`ROLLING_MEMORY_LOCUS`, else the process directory name).
func localPersonaIdentity() (assistantID, incarnationID, locus string) {
	assistantID = strings.TrimSpace(os.Getenv("ROLLING_MEMORY_ASSISTANT_ID"))
	if assistantID == "" {
		assistantID = "default"
	}
	incarnationID = strings.TrimSpace(os.Getenv("ROLLING_MEMORY_INCARNATION_ID"))
	locus = strings.TrimSpace(os.Getenv("ROLLING_MEMORY_LOCUS"))
	if locus == "" {
		if wd, err := os.Getwd(); err == nil {
			locus = ResolveWorkspaceLocus(wd)
		}
	}
	return assistantID, incarnationID, locus
}

// ResolveWorkspaceLocus resolves a directory path to its canonical workspace
// identity, delegating to the shared primitive in internal/text: the enclosing
// git repository root's basename, or "" outside any repository. Subdirectories
// keep the repository's identity; the home directory, runtime state/data
// directories and temporary directories are never stamped as workspaces.
func ResolveWorkspaceLocus(dir string) string {
	return text.DetectGitWorkspaceName(dir)
}

// LocalVaultScopePreflight reports the stored binding and whether unbound vault
// data exists. It only reads the open database and never assigns ownership.
func (m *Memory) LocalVaultScopePreflight() (string, bool, error) {
	if m == nil || m.Store == nil || m.Store.DB() == nil {
		return "", false, fmt.Errorf("local_plain vault preflight requires an open store")
	}
	var metaExists int
	if err := m.Store.DB().QueryRow(`SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = 'rm_meta')`).Scan(&metaExists); err != nil {
		return "", false, err
	}
	var bound string
	if metaExists != 0 {
		var storageType string
		err := m.Store.DB().QueryRow(`SELECT v, typeof(v) FROM rm_meta WHERE k = 'local_plain_scope'`).Scan(&bound, &storageType)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return "", false, err
		}
		if err == nil && (storageType != "text" || strings.TrimSpace(bound) == "") {
			return "", false, fmt.Errorf("local_plain vault scope binding is invalid; preserve the database and inspect the binding")
		}
	}
	for _, table := range []string{"rm_docs", "rm_noetic", "rm_bg_dirs", "rm_bg_files", "rm_bg_chunks", "rm_episodic_lineage", "rm_episodic_index"} {
		var exists int
		if err := m.Store.DB().QueryRow(`SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = ?)`, table).Scan(&exists); err != nil {
			return "", false, err
		}
		if exists == 0 {
			continue
		}
		var populated int
		if err := m.Store.DB().QueryRow(`SELECT EXISTS(SELECT 1 FROM ` + table + `)`).Scan(&populated); err != nil {
			return "", false, err
		}
		if populated != 0 {
			return bound, true, nil
		}
	}
	return bound, false, nil
}

// bindLocalVaultScope claims only an empty vault. Existing unbound rows have no
// trustworthy scope and must remain untouched until their ownership is resolved.
func (m *Memory) bindLocalVaultScope() error {
	scope := strings.TrimSpace(m.Config.Scope)
	if scope == "" {
		return fmt.Errorf("local_plain vault requires a scope")
	}
	var metaExists int
	if err := m.Store.DB().QueryRow(`SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = 'rm_meta')`).Scan(&metaExists); err == nil && metaExists != 0 {
		var bound, storageType string
		if err := m.Store.DB().QueryRow(`SELECT v, typeof(v) FROM rm_meta WHERE k = 'local_plain_scope'`).Scan(&bound, &storageType); err == nil {
			if storageType != "text" || bound != scope {
				return fmt.Errorf("local_plain vault scope does not match its binding")
			}
			return nil
		}
	}
	tx, err := m.Store.DB().Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS rm_meta (k TEXT PRIMARY KEY, v BLOB NOT NULL)`); err != nil {
		return err
	}
	var bound, storageType string
	err = tx.QueryRow(`SELECT v, typeof(v) FROM rm_meta WHERE k = 'local_plain_scope'`).Scan(&bound, &storageType)
	if errors.Is(err, sql.ErrNoRows) {
		for _, table := range []string{"rm_docs", "rm_noetic", "rm_bg_dirs", "rm_bg_files", "rm_bg_chunks", "rm_episodic_lineage", "rm_episodic_index"} {
			var exists int
			if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = ?)`, table).Scan(&exists); err != nil {
				return err
			}
			if exists == 0 {
				continue
			}
			var populated int
			if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM ` + table + `)`).Scan(&populated); err != nil {
				return err
			}
			if populated != 0 {
				return fmt.Errorf("local_plain vault scope is unbound; existing data is preserved and cannot be assigned automatically. Back up the database and referenced sources, independently verify the ownership of all records, and obtain explicit authorization before a non-destructive scope binding")
			}
		}
		if _, err := tx.Exec(`INSERT INTO rm_meta(k, v) VALUES ('local_plain_scope', ?)`, scope); err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else if storageType != "text" || bound != scope {
		return fmt.Errorf("local_plain vault scope does not match its binding")
	}
	return tx.Commit()
}

func (m *Memory) checkLocalVaultScope(scope string) error {
	if m.Config == nil {
		return fmt.Errorf("vault requires a configured scope")
	}
	if strings.TrimSpace(scope) != strings.TrimSpace(m.Config.Scope) {
		return fmt.Errorf("local_plain requested scope does not match the configured scope")
	}
	return nil
}

func (m *Memory) Warmup(ctx context.Context) error {
	tc, err := m.LocalVault()
	if err != nil {
		return err
	}
	if tc == nil || tc.Vault == nil {
		return nil
	}
	return tc.Vault.WarmupCorpusSnapshot(ctx)
}

// PublishedSnapshotReady reports whether a durable disk cluster exists.
func (m *Memory) PublishedSnapshotReady() bool {
	tc, err := m.LocalVault()
	if err != nil || tc == nil || tc.Vault == nil {
		return false
	}
	return tc.Vault.PublishedSnapshotReady()
}

// SnapshotBuildGap reports the corpus writes accumulated since the last build.
func (m *Memory) SnapshotBuildGap(ctx context.Context) (int64, bool) {
	tc, err := m.LocalVault()
	if err != nil || tc == nil || tc.Vault == nil {
		return 0, false
	}
	return tc.Vault.SnapshotBuildGap(ctx)
}

// SnapshotChangeGap reports the change-log rows accumulated since the last
// published cluster (the precise builder trigger).
func (m *Memory) SnapshotChangeGap(ctx context.Context) (int64, bool) {
	tc, err := m.LocalVault()
	if err != nil || tc == nil || tc.Vault == nil {
		return 0, false
	}
	return tc.Vault.SnapshotChangeGap(ctx)
}

// RebuildSnapshot runs the full-corpus build and publishes the disk cluster.
// It returns the corpus generation captured before the build; writes that
// arrive during the build stay in the delta overlay. It is only called by the
// background builder, never by a recall surface.
func (m *Memory) RebuildSnapshot(ctx context.Context) (int64, error) {
	tc, err := m.LocalVault()
	if err != nil {
		return 0, err
	}
	if tc == nil || tc.Vault == nil {
		return 0, fmt.Errorf("snapshot build requires an open local vault")
	}
	return tc.Vault.RebuildCorpusSnapshot(ctx)
}

func (m *Memory) LocalVault() (*localvault.Client, error) {
	if m == nil {
		return nil, fmt.Errorf("localvault requires an open local SQLite store")
	}
	m.localVaultMu.Lock()
	defer m.localVaultMu.Unlock()
	if m.Store == nil || m.Store.DB() == nil {
		return nil, fmt.Errorf("localvault requires an open local SQLite store")
	}
	if m.Config == nil {
		return nil, fmt.Errorf("localvault requires a configured scope")
	}
	if err := m.bindLocalVaultScope(); err != nil {
		return nil, err
	}
	m.localVaultOnce.Do(func() {
		vault, vaultErr := localvault.NewVault(m.Store.DB(), nil)
		if vaultErr == nil {
			vault.SetColdBuildMemoryLimit(m.coldBuildMemoryLimit.Load())
			vault.SetBuildAllowed(!m.snapshotOnly.Load())
		}
		if vaultErr == nil && !strings.EqualFold(strings.TrimSpace(os.Getenv("ROLLING_MEMORY_HARMONICS")), "off") {
			vault.SetCanonicalHarmonics(plain.CanonicalHarmonicOptions{
				EnableStructuredDates: true,
				Expander:              plain.CanonicalCompanionTags,
			})
			vault.SetSegmenterOptions(localvault.SegmenterPackOptions{HarmonicOverlay: true})
		}
		m.localVault = vault
		m.localVaultErr = vaultErr
	})
	if m.localVaultErr != nil {
		return nil, m.localVaultErr
	}
	// Reuse the expensive blind-slot index, but fork request-local persona state
	// so concurrent incarnations can never overwrite each other's identity.
	vault := m.localVault.CloneWithSharedSlotCache()
	client := localvault.NewLocalClient(vault)
	client.SetIdentity(localPersonaIdentity())
	return client, nil
}

func (m *Memory) localVaultRemember(req RememberRequest) (map[string]any, error) {
	started := clock.MonotonicMS()
	plan, err := m.prepareRemember(req)
	if err != nil {
		return nil, err
	}
	if req.Dry {
		return plan.dryReceipt(), nil
	}
	tc, err := m.LocalVault()
	if err != nil {
		return nil, err
	}
	written := make([]any, 0, len(plan.Units))
	var first localvault.IngestResponse
	for _, unit := range plan.Units {
		resp, err := tc.Remember(context.Background(), unit.DocID, unit.Plaintext, plan.OccurredAt)
		if err != nil {
			return nil, fmt.Errorf("localvault remember chunk %d/%d: %w", unit.ChunkIndex+1, plan.ChunkCount, err)
		}
		if len(written) == 0 {
			first = resp
		}
		written = append(written, map[string]any{
			"id":          unit.DocID,
			"address_uri": resp.AddressURI,
			"waveform":    resp.WaveformBar,
			"reason":      "vault_ingested",
			"chunk_index": unit.ChunkIndex,
		})
	}
	if len(written) == 0 {
		return nil, fmt.Errorf("localvault remember produced no unit")
	}
	firstID := written[0].(map[string]any)["id"].(string)
	return map[string]any{
		"ok":            true,
		"dry":           false,
		"scope":         plan.Scope,
		"extractor":     localvault.EngineName,
		"written":       written,
		"skipped":       []any{},
		"superseded":    []any{},
		"supplemented":  []any{},
		"conflicts":     0,
		"anchor_failed": 0,
		"trace": map[string]any{
			"hash":     firstID,
			"session":  plan.Session,
			"inserted": true,
			"chunks":   len(written),
		},
		"meta": map[string]any{
			"engine":      localvault.EngineName,
			"address_uri": first.AddressURI,
			"coord64":     first.AddressCoord64,
			"waveform":    first.WaveformBar,
			"chunks":      len(written),
			"elapsed_ms":  int(clock.MonotonicMS() - started),
		},
	}, nil
}

// `anchor`/`confidence`/`status`), so both paths agree on one source of truth.
func (m *Memory) recentVaultItems(limit int) ([]map[string]any, error) {
	tc, err := m.LocalVault()
	if err != nil {
		return nil, err
	}
	entries, err := tc.Vault.RecentDocs(limit)
	if err != nil {
		return nil, err
	}
	// The same derived-vs-source honesty rule the `recent` recall primitive follows: a
	// burst of Noetic handles must not occupy the whole window and hide the conversation
	// they cite, so swap the oldest derived slot for the newest source entry (time order
	// preserved). Without this, the flat receipt view and the recall view would disagree
	// about the very same window.
	if limit >= 2 && len(entries) > 0 && allNoetic(entries) {
		if source, sErr := tc.Vault.RecentSourceDoc(limit * 2); sErr == nil && source != nil {
			entries[len(entries)-1] = *source
			sort.SliceStable(entries, func(i, j int) bool {
				if entries[i].OccurredAt != entries[j].OccurredAt {
					return entries[i].OccurredAt > entries[j].OccurredAt
				}
				return entries[i].ID < entries[j].ID
			})
		}
	}
	items := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		layer := strings.TrimSpace(entry.Layer)
		if layer == "" {
			layer = "episodic"
		}
		timeLabel := ""
		if entry.OccurredAt > 0 {
			timeLabel = ids.FormatTimeSeconds(entry.OccurredAt, "")
		}
		items = append(items, map[string]any{
			"time":    timeLabel,
			"content": "[" + layer + "] " + strings.TrimSpace(entry.Text),
			"anchor":  entry.ID,
			"status":  "active",
			"layer":   layer,
		})
	}
	return items, nil
}

// maxRecallCandidateDepth bounds how deep one product recall may reach. The
// delivered window sits far below it (≤64 bands, deepest pool 3× the requested
// depth), so the bound only stops an unbounded offset/limit from turning into a
// multi-GB allocation. The eval arm calls RecallWithOptions directly and is not
// affected by this clamp.
const maxRecallCandidateDepth = 2048

// boundedRecallDepth folds raw_limit, caps.bands and offset into one candidate
// depth, clamped to the supported maximum.
func boundedRecallDepth(rawLimit int, capsBands *int, offset int) int {
	depth := rawLimit
	if depth <= 0 {
		depth = 10
	}
	if capsBands != nil && *capsBands > depth {
		depth = *capsBands
	}
	if depth > maxRecallCandidateDepth {
		depth = maxRecallCandidateDepth
	}
	if offset > 0 {
		if offset >= maxRecallCandidateDepth-depth {
			return maxRecallCandidateDepth
		}
		depth += offset
	}
	return depth
}

// recentVaultRecall serves the time-ordered `recent` recall primitive: the
// newest local vault entries (L1/L2 documents plus L3 Noetic records) with
// layer and time labels. Pure time ordering — zero semantic matching and zero
// transport round trips, so it answers "what changed just now" directly.
func (m *Memory) recentVaultRecall(req RecallRequest) (*recall.Result, error) {
	started := clock.MonotonicMS()
	scope := m.Config.Scope
	if req.Scope != nil && strings.TrimSpace(*req.Scope) != "" {
		scope = strings.TrimSpace(*req.Scope)
	}
	limit := boundedRecallDepth(req.RawLimit, req.CapsBands, req.Offset)
	tc, err := m.LocalVault()
	if err != nil {
		return nil, err
	}
	query := strings.TrimSpace(req.Cue)
	anchor := recall.LocusAnchor(query)
	fetchLimit := limit * 8
	if fetchLimit < 64 {
		fetchLimit = 64
	}
	if fetchLimit > maxRecallCandidateDepth {
		fetchLimit = maxRecallCandidateDepth
	}
	entries, err := tc.Vault.RecentDocs(fetchLimit)
	if err != nil {
		return nil, err
	}
	entries = foldRecentNoeticDerivatives(entries, limit, anchor)
	// A Noetic row is a derived handle produced by the agent's own recalls, so a burst
	// of them can occupy the whole window and hide the very conversation they cite.
	// When the window is exclusively derived while source memories exist, swap its
	// oldest derived slot for the newest source entry: the view must never claim
	// "nothing happened" about the layers it was derived from.
	if limit >= 2 && len(entries) > 0 && allNoetic(entries) {
		if source, sErr := tc.Vault.RecentSourceDoc(limit * 2); sErr == nil && source != nil {
			entries[len(entries)-1] = *source
			sort.SliceStable(entries, func(i, j int) bool {
				if entries[i].OccurredAt != entries[j].OccurredAt {
					return entries[i].OccurredAt > entries[j].OccurredAt
				}
				return entries[i].ID < entries[j].ID
			})
		}
	}
	bands := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		layer := strings.TrimSpace(entry.Layer)
		if layer == "" {
			layer = "episodic"
		}
		timeLabel := ""
		if entry.OccurredAt > 0 {
			timeLabel = ids.FormatTimeSeconds(entry.OccurredAt, "")
		}
		cleanedText := stripAssistantRoutingPreamble(strings.TrimSpace(entry.Text))
		if cleanedText == "" {
			cleanedText = strings.TrimSpace(entry.Text)
		}
		title := ids.Truncate(cleanedText, 80, "…")
		title = strings.NewReplacer("\n", " ", "\r", " ", "\t", " ").Replace(title)
		locus := strings.TrimSpace(entry.Locus)
		spaceLabel := locus
		if spaceLabel != "" && layer != "" && layer != "episodic" {
			spaceLabel = spaceLabel + " · " + layer
		} else if spaceLabel == "" {
			spaceLabel = layer
		}
		bands = append(bands, map[string]any{
			"kind":        "cell_band",
			"title":       "[" + layer + "] " + title,
			"time_label":  timeLabel,
			"space_label": spaceLabel,
			"nodes": []map[string]any{
				{
					"id":      entry.ID,
					"content": cleanedText,
					"locus":   locus,
					"time":    timeLabel,
					"anchor":  entry.ID,
					"status":  "active",
					"layer":   layer,
				},
			},
		})
	}
	reader := "deliverable"
	if req.Reader != nil && strings.TrimSpace(*req.Reader) != "" {
		reader = strings.TrimSpace(*req.Reader)
	}
	result := &recall.Result{
		Scope:  scope,
		Bands:  bands,
		Absent: len(bands) == 0,
		Gaps:   map[string]int{},
		Meta: map[string]any{
			"mode":       "recent",
			"reader":     reader,
			"entries":    len(bands),
			"elapsed_ms": int(clock.MonotonicMS() - started),
		},
	}
	if anchor != "" {
		recall.PromoteBandsByLocusAnchor(result, query)
	}
	applyThinDeliveryCaps(result, req)
	return result, nil
}

// allNoetic reports whether every entry in the window is a derived L3 handle.
func allNoetic(entries []localvault.RecentDoc) bool {
	for _, entry := range entries {
		if !strings.EqualFold(strings.TrimSpace(entry.Layer), "noetic") {
			return false
		}
	}
	return len(entries) > 0
}

// deliveredNoeticActivations returns the Noetic handles that actually took part
// in one delivery: the handle's own band survived the caps, or at least one of
// its cited units did. Matching alone does not count, so repeatedly asking an
// unhelpful question cannot ratchet a recollection forever.
func deliveredNoeticActivations(records []localvault.LocalNoeticRecord, bands []map[string]any, excludeTraceKey string) []string {
	evidence := make(map[string]bool, 64)
	handles := make(map[string]bool, 8)
	for _, band := range bands {
		kind, _ := band["kind"].(string)
		nodes, _ := band["nodes"].([]map[string]any)
		for _, node := range nodes {
			id, _ := node["id"].(string)
			id = strings.TrimSpace(id)
			if id == "" {
				continue
			}
			if kind == "noetic_band" {
				handles[id] = true
			} else {
				evidence[id] = true
			}
		}
	}
	out := make([]string, 0, 4)
	for _, rec := range records {
		if excludeTraceKey != "" && rec.TraceKey == excludeTraceKey {
			continue
		}
		used := handles[rec.NoeticID]
		if !used {
			for _, cid := range rec.CitedUnitIDs {
				if evidence[strings.TrimSpace(cid)] {
					used = true
					break
				}
			}
		}
		if used {
			out = append(out, rec.NoeticID)
		}
		if len(out) >= 8 {
			break
		}
	}
	return out
}

// parseRecentNoeticText extracts the cue line and cited unit IDs from a rendered
// RecentDoc noetic text ("Cue: ...\nCited units: u1, u2").
func parseRecentNoeticText(text string) (cueLine string, citedIDs []string) {
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "Cue:") {
			cueLine = strings.TrimSpace(strings.TrimPrefix(trimmed, "Cue:"))
		} else if strings.HasPrefix(trimmed, "Cited units:") {
			raw := strings.TrimSpace(strings.TrimPrefix(trimmed, "Cited units:"))
			for _, part := range strings.Split(raw, ",") {
				if id := strings.TrimSpace(part); id != "" {
					citedIDs = append(citedIDs, id)
				}
			}
		}
	}
	return cueLine, citedIDs
}

// foldRecentNoeticDerivatives enriches derived L3 Noetic entries with their inherited
// workspace locus (from `[<workspace>]` in the cue or from cited source units), applies
// optional `[<workspace>]` locus promotion, and folds redundant Noetic handles whose cited
// source unit is already present in the delivered window behind unshown source memories.
func foldRecentNoeticDerivatives(entries []localvault.RecentDoc, limit int, anchor string) []localvault.RecentDoc {
	if len(entries) == 0 || limit <= 0 {
		return entries
	}
	sourceLocus := make(map[string]string, len(entries))
	sourceInPool := make(map[string]bool, len(entries))
	for _, e := range entries {
		if !strings.EqualFold(strings.TrimSpace(e.Layer), "noetic") {
			sourceInPool[e.ID] = true
			if loc := strings.TrimSpace(e.Locus); loc != "" {
				sourceLocus[e.ID] = loc
			}
		}
	}
	citedByNoetic := make(map[string][]string, len(entries))
	for i := range entries {
		if !strings.EqualFold(strings.TrimSpace(entries[i].Layer), "noetic") {
			continue
		}
		cueLine, cited := parseRecentNoeticText(entries[i].Text)
		citedByNoetic[entries[i].ID] = cited
		if strings.TrimSpace(entries[i].Locus) == "" {
			if cueAnchor := recall.LocusAnchor(cueLine); cueAnchor != "" {
				entries[i].Locus = cueAnchor
			} else {
				for _, cid := range cited {
					if loc := sourceLocus[cid]; loc != "" {
						entries[i].Locus = loc
						break
					}
				}
			}
		}
	}
	if anchor != "" && len(entries) > 1 {
		matched := make([]localvault.RecentDoc, 0, len(entries))
		others := make([]localvault.RecentDoc, 0, len(entries))
		for _, e := range entries {
			if strings.EqualFold(strings.TrimSpace(e.Locus), anchor) {
				matched = append(matched, e)
			} else {
				others = append(others, e)
			}
		}
		if len(matched) > 0 {
			entries = append(matched, others...)
		}
	}
	coveredUnits := make(map[string]bool, len(sourceInPool)*2)
	for id := range sourceInPool {
		coveredUnits[id] = true
	}
	primary := make([]localvault.RecentDoc, 0, len(entries))
	folded := make([]localvault.RecentDoc, 0, len(entries))
	for _, e := range entries {
		if strings.EqualFold(strings.TrimSpace(e.Layer), "noetic") {
			cited := citedByNoetic[e.ID]
			allCovered := len(cited) > 0
			citesVisibleSource := false
			for _, cid := range cited {
				if sourceInPool[cid] {
					citesVisibleSource = true
				}
				if !coveredUnits[cid] {
					allCovered = false
				}
			}
			if citesVisibleSource || allCovered {
				folded = append(folded, e)
				continue
			}
			for _, cid := range cited {
				coveredUnits[cid] = true
			}
		}
		primary = append(primary, e)
	}
	out := append(primary, folded...)
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

var (
	foregroundRecallActive   atomic.Int64
	foregroundRecallLastNano atomic.Int64
	backgroundCancelMu       sync.Mutex
	backgroundCancelFn       context.CancelFunc
)

// RegisterBackgroundWorkCancel registers the cancel function of an in-flight background
// auto-watch pass so an incoming foreground Recall can immediately preempt it.
func RegisterBackgroundWorkCancel(cancel context.CancelFunc) {
	backgroundCancelMu.Lock()
	backgroundCancelFn = cancel
	backgroundCancelMu.Unlock()
}

// ShouldYieldToForegroundRecall reports whether a background auto-watch pass should defer
// because a foreground Recall is currently in flight in this process.
func ShouldYieldToForegroundRecall() bool {
	return foregroundRecallActive.Load() > 0
}

func beginForegroundRecall() func() {
	foregroundRecallActive.Add(1)
	foregroundRecallLastNano.Store(time.Now().UnixNano())
	backgroundCancelMu.Lock()
	cancel := backgroundCancelFn
	backgroundCancelMu.Unlock()
	if cancel != nil {
		cancel()
	}
	return func() {
		foregroundRecallLastNano.Store(time.Now().UnixNano())
		foregroundRecallActive.Add(-1)
	}
}

func (m *Memory) localVaultRecall(req RecallRequest) (*recall.Result, error) {
	doneFG := beginForegroundRecall()
	defer doneFG()
	scope := m.Config.Scope
	if req.Scope != nil && strings.TrimSpace(*req.Scope) != "" {
		scope = strings.TrimSpace(*req.Scope)
	}
	if err := m.checkLocalVaultScope(scope); err != nil {
		return nil, err
	}
	started := clock.MonotonicMS()
	if strings.EqualFold(strings.TrimSpace(req.Mode), "recent") {
		return m.recentVaultRecall(req)
	}
	cfgWindow := "0-16"
	if m.Config != nil && strings.TrimSpace(m.Config.Recall.DeliveryWindow) != "" {
		cfgWindow = m.Config.Recall.DeliveryWindow
	}
	deliveryWinLabel, deliveryWinCap, okWin := config.NormalizeDeliveryWindow(req.DeliveryWindow, cfgWindow)
	if !okWin {
		deliveryWinLabel, deliveryWinCap = "0-16", 16
	}
	topK := boundedRecallDepth(req.RawLimit, req.CapsBands, req.Offset)
	adaptiveEnergyWindow := req.CapsBands == nil && strings.TrimSpace(req.DeliveryWindow) != ""
	if adaptiveEnergyWindow && deliveryWinCap > topK {
		topK = deliveryWinCap
	}
	// Retrieval always runs on the explicit cue: the s/x/o hint slots that once
	// synthesized a query were removed with the structured memory assertion contract (2026-11-06).
	// Strip any leading `[<workspace>]` locus anchor before passing `query` to the lexical
	// engine so the workspace tag acts strictly as a post-retrieval locus preference rather
	// than polluting query token denominators.
	rawCue := strings.TrimSpace(req.Cue)
	query := recall.StripLocusAnchor(rawCue)
	tc, err := m.LocalVault()
	if err != nil {
		return nil, err
	}
	// The calling incarnation IS the concrete lifetime instance: stamp it so the L3 Noetic
	// record this recall crystallizes stays attributable, without narrowing the search scope.
	if strings.TrimSpace(req.IncarnationID) != "" {
		tc.SetIdentity(tc.AssistantID, strings.TrimSpace(req.IncarnationID), tc.Locus)
	}
	queryCtx := context.Background()
	budgetMS := 10000
	if req.BudgetMS != nil {
		budgetMS = *req.BudgetMS
	} else if m.Config != nil && m.Config.Recall.BudgetMS > 0 && m.Config.Recall.BudgetMS != 500 {
		budgetMS = m.Config.Recall.BudgetMS
	}
	if tc.Vault != nil && tc.Vault.CorpusSize() == 0 && budgetMS < 35000 {
		budgetMS = 35000
	}
	if budgetMS > 0 {
		var cancel context.CancelFunc
		queryCtx, cancel = context.WithTimeout(queryCtx, time.Duration(budgetMS)*time.Millisecond)
		defer cancel()
	}
	// 交付余量窗口：按请求深度的 3× 取候选（最少 20），再按交付 caps 截断。
	// 这是产品交付策略（L3 引用水合 / 轮次组装需要比交付条数更宽的候选池）；
	// 评测臂不经过本函数：rmem eval 直接以 --top-k 为检索深度调 RecallWithOptions。
	recallFetchK := topK * 3
	if recallFetchK < 20 {
		recallFetchK = 20
	}
	if tc.Vault != nil && tc.Vault.CorpusSize() >= 2048 {
		recallFetchK = topK + 4
		if recallFetchK < 12 {
			recallFetchK = 12
		}
	}
	now := clock.Unix()
	timeRef := ids.ParseTime(req.TimeRef, ids.ParseTimeOptions{TimeRef: &now, Default: now})
	// Noetic handles are matched by every cue variant of a trace; activation
	// counts real use, so no clock enters this decision.
	noeticHits, _ := tc.RecallNoetic(query, topK)
	hits, err := tc.RecallWithOptions(queryCtx, query, localvault.BlindQueryOptions{
		TopK:    recallFetchK,
		AsOf:    0,
		TimeRef: timeRef,
	})
	if err != nil {
		return nil, err
	}
	// L3 is a retrieval handle, not evidence. Keep it out of the evidence stream
	// so local fallback cannot self-amplify an old Noetic into the top delivery.
	evidenceHits := hits[:0]
	for _, h := range hits {
		if h.Layer == localvault.LayerNoetic || strings.HasPrefix(h.UnitID, "nt_") || strings.HasPrefix(h.UnitID, "noetic_") {
			continue
		}
		evidenceHits = append(evidenceHits, h)
	}
	hits = evidenceHits

	// L3 -> L1/L2 Citation Resonance Hydrate: SearchClientSDKDefault has already
	// applied the cited-set coverage floor inside the candidate pool; here we
	// only hydrate cited units that were outside the initial topK at the same
	// coverage-derived weight so proven evidence from past sessions is never
	// dropped.
	if len(noeticHits) > 0 && tc.Vault != nil {
		hitIdxByID := make(map[string]int, len(hits))
		maxW := 1.0
		for idx, h := range hits {
			hitIdxByID[h.UnitID] = idx
			if h.FinalWeight > maxW {
				maxW = h.FinalWeight
			}
		}
		for nRank, nh := range noeticHits {
			if nRank >= 3 || nh.MatchScore <= 0 {
				break
			}
			coverage := tc.Vault.NoeticEvidenceCoverage(nh.CitedUnitIDs, query)
			target := maxW * nh.MatchScore * coverage
			if target <= 0 {
				continue
			}
			for _, cid := range nh.CitedUnitIDs {
				cid = strings.TrimSpace(cid)
				if cid == "" || strings.HasPrefix(cid, "nt_") || strings.HasPrefix(cid, "noetic_") {
					continue
				}
				if _, ok := hitIdxByID[cid]; ok {
					continue
				}
				if len(hits) < topK*2 {
					rawTxt, distTxt, keptSents, lErr := tc.Vault.LoadLocalDocument(cid)
					if lErr != nil {
						hitIdxByID[cid] = len(hits)
						hits = append(hits, localvault.LocalResolvedHit{QueryHit: localvault.QueryHit{UnitID: cid}, SourceUnavailable: true})
					} else if strings.TrimSpace(rawTxt) != "" {
						layer := tc.Vault.LoadLocalDocumentLayer(cid)
						hitIdxByID[cid] = len(hits)
						hits = append(hits, localvault.LocalResolvedHit{
							QueryHit: localvault.QueryHit{
								UnitID:      cid,
								Layer:       layer,
								AddressURI:  nh.AddressURI,
								FinalWeight: target,
							},
							RawPlaintext:       rawTxt,
							DistilledPlaintext: distTxt,
							KeptSentences:      keptSents,
							Authors:            localvault.RoleTaggedAuthors(rawTxt),
						})
					}
				}
			}
		}
	}

	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].FinalWeight != hits[j].FinalWeight {
			return hits[i].FinalWeight > hits[j].FinalWeight
		}
		return hits[i].UnitID < hits[j].UnitID
	})
	// 端侧检索范围锚（cue 首部 `[<工作区>]`，语义正本 internal/recall/locus_scope.go）：命中锚
	// （= 入库 locus，仓库目录名）的候选稳定置前、其余候选原序压后，不删除任何候选
	// （fail-open）；未写锚或锚无人命中时，候选序与不带锚完全一致。节点恒带真实 locus，
	// 与图存管线同形（便于调用方按出处甄别）。
	anchor := recall.LocusAnchor(rawCue)
	locusByDoc := map[string]string{}
	timeByDoc := map[string]int64{}
	if tc.Vault != nil {
		ids := make([]string, 0, len(hits))
		for _, h := range hits {
			ids = append(ids, h.UnitID)
		}
		locusByDoc = tc.Vault.DocLoci(ids)
		timeByDoc = tc.Vault.DocTimes(ids)
	}
	for _, h := range hits {
		if timeByDoc[h.UnitID] <= 0 && h.OccurredAt > 0 {
			timeByDoc[h.UnitID] = h.OccurredAt
		}
	}
	hits = orderHitsByLocusAnchor(hits, anchor, locusByDoc)
	// Preserve independent source identities, even when their content is similar.
	var deduplicated int
	hits, deduplicated = dedupeResolvedHitsByContent(hits)
	diag := tc.RecallDiagnostics()
	peakWeight := 0.0
	for _, h := range hits {
		if h.FinalWeight > peakWeight {
			peakWeight = h.FinalWeight
		}
	}
	bands := make([]map[string]any, 0, topK)
	rawHits := make([]map[string]any, 0, len(hits))
	sourceUnavailable := make(map[string]bool)
	// turnAuthors caches the speaker tags named by a turn's opening segment, so a
	// long message whose later chunks carry no inline role tag still attributes
	// every hit of that message to its speaker without re-reading the source.
	turnAuthors := make(map[string][]string, len(hits))

	// Group hits by turn, but deliver only units independently verified against
	// their indexed source identity; neighboring content has no implicit authority.
	contentTruncated := false
	groupOrder := make([]string, 0, len(hits))
	groups := make(map[string]*episodicTurnGroup, len(hits))
	standalone := make([]localvault.LocalResolvedHit, 0, len(hits))
	for i := range hits {
		h := &hits[i]
		pointer := ""
		if tc.Vault != nil {
			pointer = tc.Vault.EpisodicPointerFor(h.UnitID)
		}
		if pointer == "" {
			if h.SourceUnavailable {
				sourceUnavailable[h.UnitID] = true
			} else {
				standalone = append(standalone, *h)
			}
			continue
		}
		resolved, actualRef, verifyErr := tc.Vault.LoadVerifiedEpisodicPointerSource(h.UnitID, pointer)
		primaryTurn, _ := splitTurnRefSegment(pointer)
		ref, segmentIndex := splitTurnRefSegment(actualRef)
		// A different segment in the same turn shares the primary's provenance.
		// Another turn has no authenticated audience or source-specific metadata.
		if verifyErr != nil || primaryTurn == strings.TrimSpace(pointer) || ref == strings.TrimSpace(actualRef) || ref != primaryTurn || strings.TrimSpace(resolved) == "" || strings.HasPrefix(strings.TrimSpace(resolved), "@ref:") {
			h.SourceUnavailable = true
			sourceUnavailable[h.UnitID] = true
			continue
		}
		h.SourceUnavailable = false
		h.RawPlaintext = resolved
		h.DistilledPlaintext = resolved
		h.Authors = localvault.RoleTaggedAuthors(resolved)
		if len(h.Authors) == 0 {
			// A message longer than one chunk carries its role tag only in the
			// opening chunk; the segment's own source text names the speaker, and
			// that tag is read from verified source, never inferred from content.
			key := fmt.Sprintf("%s#%d", ref, segmentIndex)
			if cached, ok := turnAuthors[key]; ok {
				h.Authors = cached
			} else if tags := tc.Vault.SegmentSpeakerTags(ref, segmentIndex); len(tags) > 0 {
				h.Authors = tags
				turnAuthors[key] = tags
			}
		}
		group := groups[ref]
		if group == nil {
			group = &episodicTurnGroup{ref: ref, rawPointer: actualRef, hitIndex: segmentIndex, score: h.FinalWeight, direct: h.DirectWeight, unitID: h.UnitID, authors: h.Authors}
			groups[ref] = group
			groupOrder = append(groupOrder, ref)
		}
		group.hits = append(group.hits, map[string]any{
			"unit": h.UnitID, "pointer": actualRef, "segment": segmentIndex, "verified": resolved,
		})
		if h.FinalWeight > group.score {
			group.score = h.FinalWeight
			group.direct = h.DirectWeight
			group.rawPointer = actualRef
			group.hitIndex = segmentIndex
			group.unitID = h.UnitID
			group.authors = h.Authors
		}
	}
	sort.SliceStable(groupOrder, func(i, j int) bool {
		return groups[groupOrder[i]].score > groups[groupOrder[j]].score
	})
	deliveryChars := 0
	if m.Config != nil {
		deliveryChars = m.Config.Recall.Chars
	}
	if req.CapsChars != nil && *req.CapsChars > 0 {
		deliveryChars = *req.CapsChars
	}
	candidates := make([]deliveredCandidate, 0, len(groupOrder)+len(standalone))
	nextOrder := 0
	for _, ref := range groupOrder {
		group := groups[ref]
		// Lineage establishes association, not authorization to deliver another hit:
		// only segments matched by retrieval and verified against their source are delivered.
		sort.SliceStable(group.hits, func(i, j int) bool {
			left := group.hits[i]["segment"].(int)
			right := group.hits[j]["segment"].(int)
			if left != right {
				return left < right
			}
			return group.hits[i]["pointer"].(string) < group.hits[j]["pointer"].(string)
		})
		var verified []string
		low, high := 0, 0
		var turnOccurredAt int64
		for _, hit := range group.hits {
			unit := hit["unit"].(string)
			resolved, _ := hit["verified"].(string)
			if ts := timeByDoc[unit]; ts > turnOccurredAt {
				turnOccurredAt = ts
			}
			index := hit["segment"].(int)
			if len(verified) == 0 {
				low = index
			}
			high = index
			verified = append(verified, resolved)
		}
		// The turn's opening user message is its provenance head; fetch it from the turn's
		// own verified index when retrieval matched only later segments, so the delivered
		// block always shows what the user asked as well as what the assistant answered.
		if len(verified) > 0 && low > 0 && tc.Vault != nil {
			if prompt, ok := tc.Vault.VerifiedTurnFirstSegment(ref); ok {
				verified = append([]string{prompt}, verified...)
			}
		}
		if len(verified) == 0 {
			continue
		}
		if turnOccurredAt <= 0 {
			turnOccurredAt = timeByDoc[group.unitID]
		}
		turnTimeLabel := ""
		if turnOccurredAt > 0 {
			turnTimeLabel = ids.FormatTimeSeconds(turnOccurredAt, "")
		}
		text := strings.Join(verified, "\n")
		if cleaned := stripAssistantRoutingPreamble(text); cleaned != "" {
			text = cleaned
		}
		if strings.TrimSpace(text) == "" || strings.HasPrefix(strings.TrimSpace(text), "@ref:") {
			continue
		}
		locus := locusByDoc[group.unitID]
		nodeMap := map[string]any{
			"id":                group.unitID,
			"kind":              "memory_unit",
			"locus":             locus,
			"content":           text,
			"authors":           group.authors,
			"anchor":            group.unitID,
			"time":              turnTimeLabel,
			"occurred_at":       turnOccurredAt,
			"relative_strength": relativeStrength(group.score, peakWeight),
			"status":            "active",
			"score":             group.score,
			"segment_window":    []int{low, high},
			"turn_segments":     len(verified),
		}
		bandEntry := map[string]any{
			"id":         group.unitID,
			"kind":       "episodic_turn_band",
			"title":      "轮·" + group.unitID,
			"subject":    group.unitID,
			"time_label": turnTimeLabel,
			"score":      group.score,
			"nodes":      []map[string]any{nodeMap},
		}
		rawEntry := map[string]any{
			"trace":          group.unitID,
			"session":        group.unitID,
			"author":         strings.Join(group.authors, ","),
			"occurred_at":    turnOccurredAt,
			"text":           text,
			"score":          group.score,
			"segment_window": []int{low, high},
			"turn_segments":  len(verified),
			"in_band":        true,
		}
		candidates = append(candidates, deliveredCandidate{
			band:       bandEntry,
			rawHit:     rawEntry,
			text:       text,
			score:      group.score,
			direct:     group.direct,
			locusMatch: anchor != "" && strings.EqualFold(strings.TrimSpace(locus), anchor),
			sessionKey: episodicTurnSessionKey(ref),
			occurredAt: turnOccurredAt,
			order:      nextOrder,
		})
		nextOrder++
	}
	// One batched coordinate lookup for the delivered set: a per-hit lookup would turn a
	// single recall into O(hits) statements, which is exactly what the small-vault path did
	// before the cache threshold kicked in.
	var backgroundLocations map[string]localvault.BackgroundLocation
	if tc.Vault != nil && len(standalone) > 0 {
		ids := make([]string, 0, len(standalone))
		for _, h := range standalone {
			ids = append(ids, h.UnitID)
		}
		backgroundLocations = tc.Vault.BackgroundChunkLocations(ids)
	}
	for _, h := range standalone {
		delivered := h.DistilledPlaintext
		if strings.TrimSpace(delivered) == "" {
			delivered = h.RawPlaintext
		}
		if cleaned := strings.TrimSpace(ids.StripInjected(delivered)); cleaned != "" {
			delivered = cleaned
		}
		delivered = stripAssistantRoutingPreamble(delivered)
		spaceLabel := ""
		if location, ok := backgroundLocations[h.UnitID]; ok {
			if location.Anchor != "" {
				spaceLabel = location.RelPath + "#" + location.Anchor
			} else {
				spaceLabel = location.RelPath
			}
		}
		locus := locusByDoc[h.UnitID]
		hitOccurredAt := timeByDoc[h.UnitID]
		if hitOccurredAt <= 0 && h.OccurredAt > 0 {
			hitOccurredAt = h.OccurredAt
		}
		hitTimeLabel := ""
		if hitOccurredAt > 0 {
			hitTimeLabel = ids.FormatTimeSeconds(hitOccurredAt, "")
		}
		// The reader-facing title prefers the source label (file coordinates) or the
		// workspace locus; the internal ladder address is only the last resort.
		bandTitle := "带·" + strings.TrimSpace(spaceLabel)
		if strings.TrimSpace(spaceLabel) == "" {
			if strings.TrimSpace(locus) != "" {
				bandTitle = "带·" + strings.TrimSpace(locus)
			} else {
				bandTitle = "带·" + strings.TrimSpace(h.AddressURI)
			}
		}
		nodeMap := map[string]any{
			"id":                h.UnitID,
			"kind":              "memory_unit",
			"label":             spaceLabel,
			"locus":             locus,
			"content":           delivered,
			"authors":           h.Authors,
			"anchor":            h.UnitID,
			"time":              hitTimeLabel,
			"occurred_at":       hitOccurredAt,
			"relative_strength": relativeStrength(h.FinalWeight, peakWeight),
			"status":            "active",
			"score":             h.FinalWeight,
		}
		bandEntry := map[string]any{
			"id":         h.AddressURI,
			"kind":       "cell_band",
			"title":      bandTitle,
			"subject":    h.AddressURI,
			"time_label": hitTimeLabel,
			"score":      h.FinalWeight,
			"nodes":      []map[string]any{nodeMap},
		}
		if spaceLabel != "" {
			bandEntry["space_label"] = spaceLabel
		}
		rawEntry := map[string]any{
			"trace":       h.UnitID,
			"session":     h.AddressURI,
			"author":      strings.Join(h.Authors, ","),
			"occurred_at": hitOccurredAt,
			"text":        delivered,
			"score":       h.FinalWeight,
			"in_band":     true,
		}
		candidates = append(candidates, deliveredCandidate{
			band:       bandEntry,
			rawHit:     rawEntry,
			text:       delivered,
			standalone: true,
			score:      h.FinalWeight,
			direct:     h.DirectWeight,
			locusMatch: anchor != "" && strings.EqualFold(strings.TrimSpace(locus), anchor),
			order:      nextOrder,
		})
		nextOrder++
	}
	type sessionStat struct {
		maxScore  float64
		newestTS  int64
		newestIdx int
	}
	sessionStats := make(map[string]sessionStat)
	for idx, c := range candidates {
		if c.sessionKey == "" {
			continue
		}
		st := sessionStats[c.sessionKey]
		if c.score > st.maxScore {
			st.maxScore = c.score
		}
		if c.occurredAt > st.newestTS || (st.newestIdx == 0 && idx == 0) {
			st.newestTS = c.occurredAt
			st.newestIdx = idx + 1
		}
		sessionStats[c.sessionKey] = st
	}
	for sessionKey, st := range sessionStats {
		if st.newestIdx <= 0 {
			continue
		}
		idx := st.newestIdx - 1
		if candidates[idx].sessionKey == sessionKey && candidates[idx].score*math.E >= st.maxScore {
			candidates[idx].score = st.maxScore
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].locusMatch != candidates[j].locusMatch {
			return candidates[i].locusMatch
		}
		if candidates[i].score != candidates[j].score {
			return candidates[i].score > candidates[j].score
		}
		if candidates[i].sessionKey != "" && candidates[i].sessionKey == candidates[j].sessionKey && candidates[i].occurredAt != candidates[j].occurredAt {
			return candidates[i].occurredAt > candidates[j].occurredAt
		}
		return candidates[i].order < candidates[j].order
	})
	candPeakScore, candPeakRef := peakDecayReference(candidates)
	selectedCount := 0
	for selectedCount < len(candidates) && selectedCount < topK {
		cand := &candidates[selectedCount]
		if adaptiveEnergyWindow && selectedCount > 0 && candPeakScore > 0 && !cand.locusMatch && cand.score < candPeakRef/math.E {
			break
		}
		selectedCount++
	}
	selected := candidates[:selectedCount]
	for i := range selected {
		cand := &selected[i]
		cand.rawHit["turn"] = int64(len(rawHits) + 1)
		bands = append(bands, cand.band)
		rawHits = append(rawHits, cand.rawHit)
	}
	if adaptiveEnergyWindow && req.RawLimit <= 10 && len(bands) > req.RawLimit {
		req.RawLimit = len(bands)
	}
	noeticPeak := 0.0
	for _, nh := range noeticHits {
		if nh.MatchScore > noeticPeak {
			noeticPeak = nh.MatchScore
		}
	}
	for _, nh := range noeticHits {
		if len(bands) >= topK {
			break
		}
		// L3 is the agent's own recollection, and the matched cue is its recall
		// handle. The handle names the phrasing that actually fired; it is not a
		// claim about the conversation, which is L2.
		if len(nh.CitedUnitIDs) > 0 {
			allInvalid := true
			for _, uid := range nh.CitedUnitIDs {
				if !sourceUnavailable[uid] {
					allInvalid = false
					break
				}
			}
			if allInvalid {
				continue
			}
		}
		matchedCue := strings.TrimSpace(nh.MatchedCue)
		if matchedCue == "" {
			matchedCue = strings.TrimSpace(nh.QueryText)
		}
		rendered := "Cue: " + matchedCue
		if len(nh.CitedUnitIDs) > 0 {
			rendered += "\nCited units: " + strings.Join(nh.CitedUnitIDs, ", ")
		}
		noeticTimeLabel := ""
		if nh.OccurredAt > 0 {
			noeticTimeLabel = ids.FormatTimeSeconds(nh.OccurredAt, "")
		}
		nodeMap := map[string]any{
			"id":                nh.NoeticID,
			"kind":              "noetic_qa",
			"label":             matchedCue,
			"content":           rendered,
			"anchor":            nh.NoeticID,
			"time":              noeticTimeLabel,
			"occurred_at":       nh.OccurredAt,
			"cited_unit_ids":    nh.CitedUnitIDs,
			"relative_strength": relativeStrength(nh.MatchScore, noeticPeak),
			"status":            nh.Status,
			"score":             nh.MatchScore,
		}
		bands = append(bands, map[string]any{
			"id":         nh.NoeticID,
			"kind":       "noetic_band",
			"title":      matchedCue,
			"subject":    matchedCue,
			"time_label": noeticTimeLabel,
			"score":      nh.MatchScore,
			"nodes":      []map[string]any{nodeMap},
		})
	}
	benchmarkScope := evalns.IsBenchmarkScope(scope)
	if m.Config != nil && evalns.IsBenchmarkScope(m.Config.Scope) {
		benchmarkScope = true
	}
	crystallizedTraceKey := ""
	if query != "" && !isInternalWarmupProbeCue(query) && len(hits) > 0 && req.Mode != "probe" && !req.NoCrystallize && !benchmarkScope {
		var citedIDs []string
		var citedSents []string
		seenCite := make(map[string]bool)
		for _, h := range selectNoeticCitationHits(tc.Vault, query, hits) {
			uid := strings.TrimSpace(h.UnitID)
			if uid == "" {
				continue
			}
			sent := noeticCitationEvidence(h)
			if sent == "" || sourceUnavailable[uid] {
				continue
			}
			if h.Layer == localvault.LayerEpisodic {
				// Faithful recording, zero correctness judgment (highest philosophy: the memory
				// system is NOT the arbiter of truth). Every role block — the role-tagged line
				// plus the continuation lines that follow it — is kept verbatim, so the downstream
				// model can weigh who said what and an excerpt can never collapse to a bare label
				// line while the content beneath it is left behind; nothing is dropped,
				// preferred, or promoted as "trusted", and old assistant replies are never
				// misattributed as the answer to the current query.
				blocks := roleTaggedBlocks(sent, 4)
				if len(blocks) > 0 {
					if len(citedSents) < 3 {
						citedSents = append(citedSents, strings.Join(blocks, "\n"))
					}
				} else if sent != "" && len(citedSents) < 3 {
					citedSents = append(citedSents, sent)
				}
			} else if sent != "" && len(citedSents) < 3 {
				citedSents = append(citedSents, sent)
			}
			if !seenCite[uid] {
				seenCite[uid] = true
				citedIDs = append(citedIDs, uid)
			}
			if len(citedIDs) >= 4 {
				break
			}
		}
		if len(citedIDs) > 0 {
			ansSummary := strings.Join(citedSents, " | ")
			// The trace is the evidence set, not the phrasing: re-asking the same
			// question in different words resolves to the same recollection, while
			// a changed evidence set is a new trace that supersedes the old one.
			traceKey := localvault.NoeticTraceKey(citedIDs)
			noeticID := localvault.NoeticTraceID(traceKey)
			crystallizedTraceKey = traceKey
			for _, nh := range noeticHits {
				nhID := strings.TrimSpace(nh.NoeticID)
				if nhID == "" || nhID == noeticID || nh.TraceKey == traceKey || seenCite[nhID] || nh.MatchScore < 1.0/math.E {
					continue
				}
				seenCite[nhID] = true
				citedIDs = append(citedIDs, nhID)
				if len(citedIDs) >= 6 {
					break
				}
			}
			now := timeRef
			if !m.snapshotOnly.Load() {
				if tc.Vault != nil && tc.Vault.CorpusSize() >= 2048 {
					citedIDsCopy := append([]string(nil), citedIDs...)
					citedSentsCopy := append([]string(nil), citedSents...)
					rememberNoeticAsync(tc, noeticID, query, citedIDsCopy, citedSentsCopy, ansSummary, now)
				} else {
					_ = tc.RememberNoetic(noeticID, query, citedIDs, citedSents, ansSummary, now)
				}
			}
			if !diag.Degraded {
				m.pushNoeticPulseAsync(tc, noeticID, query, ansSummary, citedIDs, citedSents, now)
			}
		}
	}

	meta := map[string]any{
		"engine":          localvault.EngineName,
		"count":           len(hits),
		"noetic_hits":     len(noeticHits),
		"delivery_window": deliveryWinLabel,
		"elapsed_ms":      int(clock.MonotonicMS() - started),
	}
	if deduplicated > 0 {
		meta["deduplicated"] = deduplicated
	}
	// 被评测空间拦下的结晶要可观测：调用方据此区分「没有可结晶证据」与「评测空间禁止结晶」。
	if benchmarkScope && query != "" && len(hits) > 0 && req.Mode != "probe" && !req.NoCrystallize {
		meta["crystallization"] = "disabled_eval_scope"
	}
	// Degradation is reported, never folded into Absent: "the engine was unavailable" and
	// "memory holds nothing" are different facts and must not share one signal.
	if diag.Degraded {
		meta["degraded"] = true
		meta["degraded_reason"] = diag.Reason
	}
	evidence := ""
	topHitID := ""
	if len(hits) > 0 {
		topHitID = hits[0].UnitID
		evidence = hits[0].DistilledPlaintext
		if strings.TrimSpace(evidence) == "" {
			evidence = hits[0].RawPlaintext
		}
	}
	var coverage float64
	if topHitID != "" && tc.Vault != nil {
		rawCov := tc.Vault.DocAtomCoverageWithCueHashes(topHitID, evidence, uniqueCueAtomHashes(tc.Vault, query))
		coverage = float64(int(rawCov*100+0.5)) / 100
	} else {
		coverage = cueCoverage(tc.Vault, query, evidence)
	}
	if coverage > 0 {
		meta["cue_coverage"] = coverage
		// A third of the cue atoms not landing on the delivered evidence means the cue reached
		// memory through a different surface form (script variant / synonym / inflection), which
		// the caller must not mistake for absence.
		if coverage < 1.0/3.0 {
			meta["low_coverage"] = true
		}
	}

	// 全档倒排零记载凭证与完备度：整个档案（非候选集抽样）的确定性事实，
	// 供调用方在证据不足时据实拒答。只作事实陈述，不触发截断或排序改变。
	if tc.Vault != nil && query != "" {
		if certs, report, ok := tc.Vault.ArchiveVoidProbe(query); ok && (len(certs) > 0 || report.FullMass > 0) {
			meta["void_certificates"] = voidCertificatePayload(certs)
			meta["coverage"] = coverageReportPayload(report)
		}
	}

	indexIncomplete := 0
	if _, local := tc.Transport.(*localvault.LocalVaultTransport); local && tc.Vault != nil {
		index, indexErr := tc.Vault.EpisodicIndexFaultDiagnostics()
		if indexErr != nil {
			// Diagnostics are observability over the legacy payload copies; a
			// broken diagnostics schema is not evidence that the canonical
			// retrieval corpus is incomplete.
			meta["episodic_index_error"] = "diagnostics_unavailable"
		} else {
			meta["episodic_index"] = map[string]int{
				"compatible": index.Compatible, "missing": index.Missing,
				"incompatible": index.Incompatible, "corrupt": index.Corrupt,
				"needs_rebuild": index.NeedsRebuild,
			}
			indexIncomplete = index.Missing + index.Incompatible + index.Corrupt + index.NeedsRebuild
		}
		// Mounted directories carry their own ingestion clock: a pass that failed leaves the
		// rows at the last successful sync, so the caller needs the receipt to know whether
		// "absent" reflects a fresh look or a stalled mount.
		if states, bgErr := tc.Vault.BackgroundSyncStates(); bgErr == nil && len(states) > 0 {
			meta["background"] = backgroundFreshnessPayload(states, clock.Unix())
		}
	}
	gaps := map[string]int{"source_unavailable": len(sourceUnavailable)}
	if indexIncomplete > 0 {
		gaps["index_incomplete"] = indexIncomplete
	}
	absent := len(bands) == 0 && len(hits) == 0 && !diag.Degraded && len(sourceUnavailable) == 0 && indexIncomplete == 0
	if absent && m.SnapshotOnly() && tc.Vault != nil && !tc.Vault.PublishedSnapshotReady() {
		gaps["snapshot_unavailable"] = 1
		absent = false
	}
	result := &recall.Result{
		Scope:   scope,
		Bands:   bands,
		RawHits: rawHits,
		Gaps:    gaps,
		Absent:  absent,
		Meta:    meta,
	}
	// 交付面再次保证「带锚者排最前」（同图存管线口径，fail-open）。
	recall.PromoteBandsByLocusAnchor(result, rawCue)
	applyThinDeliveryCaps(result, req)
	// The character budget is decided on the delivered page, after offset and
	// caps.bands: a candidate left off the page must not shrink the window of a
	// band that is shown, and the page's own set is kept whole when it fits.
	delivered := deliveryCharBudgetCandidates(result.Bands, selected)
	applyDeliveryCharBudget(delivered, query, deliveryChars)
	for i := range delivered {
		if delivered[i].truncated {
			contentTruncated = true
		}
	}
	// Recall is not a pure read: a recollection that actually took part in this
	// delivery is reinforced, while a matched but unused one is not. The write
	// happens after the caps so only delivered handles count, and it is skipped
	// on read-only surfaces (no-crystallize / snapshot-only) and in benchmark
	// scope, where the vault must stay frozen.
	if !req.NoCrystallize && !benchmarkScope && !m.snapshotOnly.Load() && tc.Vault != nil && len(noeticHits) > 0 {
		if activated := deliveredNoeticActivations(noeticHits, result.Bands, crystallizedTraceKey); len(activated) > 0 {
			_, _ = tc.Vault.ActivateNoetics(activated, timeRef)
		}
	}
	if contentTruncated {
		truncated, _ := result.Meta["truncated"].(map[string]any)
		if truncated == nil {
			truncated = map[string]any{}
			result.Meta["truncated"] = truncated
		}
		truncated["chars"] = true
	}
	return result, nil
}

// backgroundFreshnessPayload projects per-mount sync receipts for delivery: ages are
// measured against the recall clock, and a mount without a successful pass or with a
// recorded error is named instead of silently looking fresh. Counts only — no threshold
// and no gate: the caller decides what a stale mount means for its answer.
func backgroundFreshnessPayload(states []localvault.BackgroundSyncState, now int64) map[string]any {
	mounts := make([]map[string]any, 0, len(states))
	neverSynced, failed := 0, 0
	lastSuccess := int64(0)
	for _, st := range states {
		entry := map[string]any{
			"root":          st.RootPath,
			"tracked_files": st.TrackedFiles,
		}
		switch {
		case st.LastError != "":
			entry["status"] = "error"
			entry["last_error"] = st.LastError
			failed++
		case st.LastSuccessAt == 0:
			entry["status"] = "never_synced"
			neverSynced++
		default:
			entry["status"] = "ok"
		}
		if st.LastSuccessAt > 0 {
			entry["last_success_at"] = st.LastSuccessAt
			entry["age_seconds"] = max(now-st.LastSuccessAt, int64(0))
			if st.LastSuccessAt > lastSuccess {
				lastSuccess = st.LastSuccessAt
			}
		}
		mounts = append(mounts, entry)
	}
	out := map[string]any{"mounts": mounts}
	if lastSuccess > 0 {
		out["last_success_at"] = lastSuccess
		out["age_seconds"] = max(now-lastSuccess, int64(0))
	}
	if neverSynced > 0 {
		out["never_synced_mounts"] = neverSynced
	}
	if failed > 0 {
		out["error_mounts"] = failed
	}
	return out
}

// voidCertificatePayload projects SDK certificate structs into the plain JSON
// shapes the delivery surfaces encode. The MCP probe receipt is rendered by the
// Python-compatible dumper, which accepts maps and slices only, so a struct
// value would fail the whole probe call.
func voidCertificatePayload(certs []plain.VoidCertificate) []map[string]any {
	if certs == nil {
		return nil
	}
	out := make([]map[string]any, 0, len(certs))
	for _, cert := range certs {
		out = append(out, map[string]any{
			"kind":  cert.Kind,
			"terms": cert.Terms,
			"dfs":   cert.DFs,
		})
	}
	return out
}

// coverageReportPayload projects the SDK coverage struct into the same plain
// JSON shape its marshal form carries, keeping probe and full deliveries
// byte-identical for the same report.
func coverageReportPayload(report plain.CoverageReport) map[string]any {
	return map[string]any{
		"full_mass":    report.FullMass,
		"oov_mass":     report.OOVMass,
		"covered_mass": report.CoveredMass,
		"completeness": report.Completeness,
		"active_terms": report.ActiveTerms,
	}
}

// orderHitsByLocusAnchor 把命中锚（= 入库 locus，仓库目录名）的候选稳定置前、其余候选// 原序压后，不删除任何候选（fail-open）；与图存管线 orderCandidatesByLocusAnchor 同口径
// （见 internal/recall/locus_scope.go）。
func orderHitsByLocusAnchor(hits []localvault.LocalResolvedHit, anchor string, locusByDoc map[string]string) []localvault.LocalResolvedHit {
	if strings.TrimSpace(anchor) == "" || len(hits) < 2 {
		return hits
	}
	same := make([]localvault.LocalResolvedHit, 0, len(hits))
	other := make([]localvault.LocalResolvedHit, 0, len(hits))
	for _, h := range hits {
		if strings.EqualFold(strings.TrimSpace(locusByDoc[h.UnitID]), anchor) {
			same = append(same, h)
		} else {
			other = append(other, h)
		}
	}
	if len(same) == 0 || len(other) == 0 {
		return hits
	}
	return append(same, other...)
}

func uniqueCueAtomHashes(vault *localvault.Vault, cue string) []uint64 {
	if vault == nil || strings.TrimSpace(cue) == "" {
		return nil
	}
	cuePkt, _ := vault.PackBlindPacket("Q-COVERAGE", cue, 0)
	if len(cuePkt.Slots) == 0 {
		return nil
	}
	seen := make(map[uint64]bool, len(cuePkt.Slots))
	out := make([]uint64, 0, len(cuePkt.Slots))
	for _, atom := range cuePkt.Slots {
		if !seen[atom.H] {
			seen[atom.H] = true
			out = append(out, atom.H)
		}
	}
	return out
}

// selectNoeticCitationHits keeps only the strongest mechanical query-coverage
// plateau. Multi-unit citations must explain the cue equally well; when no unit
// has literal overlap, only the strongest engine hit is retained instead of
// concatenating several merely adjacent memories into one synthetic answer.
func selectNoeticCitationHits(vault *localvault.Vault, query string, hits []localvault.LocalResolvedHit) []localvault.LocalResolvedHit {
	cueHashes := uniqueCueAtomHashes(vault, query)
	bestCoverage := -1.0
	selected := make([]localvault.LocalResolvedHit, 0, 4)
	inspected := 0
	for _, h := range hits {
		if h.FinalWeight <= 0 || h.Layer == localvault.LayerNoetic || strings.HasPrefix(h.UnitID, "nt_") || strings.HasPrefix(h.UnitID, "noetic_") {
			continue
		}
		text := h.DistilledPlaintext
		if strings.TrimSpace(text) == "" {
			text = h.RawPlaintext
		}
		coverage := vault.DocAtomCoverageWithCueHashes(h.UnitID, text, cueHashes)
		if coverage > bestCoverage {
			bestCoverage = coverage
			selected = []localvault.LocalResolvedHit{h}
		} else if coverage == bestCoverage && len(selected) < 4 {
			selected = append(selected, h)
		}
		inspected++
		if inspected >= 8 {
			break
		}
	}
	if bestCoverage <= 0 && len(selected) > 1 {
		selected = selected[:1]
	}
	if len(selected) > 4 {
		selected = selected[:4]
	}
	return selected
}

// noeticEvidenceTokenBudget is the tokenz-r 512 rung. Each cited unit keeps a
// bounded, BPE-aligned evidence prefix: empty input stays empty, long input
// scales to at most 512 tokens in every language, and no rune/byte slicing or
// language-specific word rules are involved.
const noeticEvidenceTokenBudget = 512

func truncateNoeticEvidence(text string) string {
	if strings.TrimSpace(text) == "" || tokenzr.Count(text) <= noeticEvidenceTokenBudget {
		return strings.TrimSpace(text)
	}
	pieces := tokenzr.SplitSubwords(text)
	if len(pieces) > noeticEvidenceTokenBudget {
		pieces = pieces[:noeticEvidenceTokenBudget]
	}
	return strings.TrimSpace(strings.Join(pieces, ""))
}

// noeticCitationEvidence 取一条召回命中进入 L3 引证的证据文本：蒸馏优先、原文回落，
// 并沿用交付渲染的同一清洗谓词（stripAssistantRoutingPreamble）——交付侧已剥掉的
// 路由标签与裸旁白标记行（如 `[assistant] 思考过程：`）不得在引证里复活，否则引证
// 会坍缩成一枚零信息标签行；最后收敛到 noeticEvidenceTokenBudget。
func noeticCitationEvidence(h localvault.LocalResolvedHit) string {
	sent := strings.TrimSpace(h.DistilledPlaintext)
	if sent == "" {
		sent = strings.TrimSpace(h.RawPlaintext)
	}
	return truncateNoeticEvidence(stripAssistantRoutingPreamble(sent))
}

// relativeStrength maps a raw retrieval weight onto [0,1] against the strongest hit of the same
// family in this result set. 1.00 means "strongest in this delivery" — never "trustworthy".
//
// It is deliberately NOT named confidence: the top hit reaches 1.00 by construction (it *is* the
// denominator), so the number carries no meaning outside its own result set and must not be read as
// how relevant or how true a memory is. Ranking lives in score; whether the cue actually matched
// lives in cueCoverage. A constant 1.0 everywhere was the earlier shape of this same mistake (every
// recall looked equally authoritative); dividing by the set peak fixed the constant but kept the
// authority claim, which is what this rename removes.
func relativeStrength(weight, peak float64) float64 {
	if peak <= 0 || weight <= 0 {
		return 0
	}
	ratio := weight / peak
	if ratio > 1 {
		ratio = 1
	}
	return float64(int(ratio*100+0.5)) / 100
}

// cueCoverage reports the fraction of the query's blind atoms that the delivered evidence also
// carries. It is a purely mechanical overlap measure: a low value means the cue reached the vault
// through a different surface form (script variant, synonym, inflection) rather than not existing,
// so callers must not read it as "memory holds nothing".
func cueCoverage(vault *localvault.Vault, cue, evidence string) float64 {
	ratio := cueAtomCoverage(vault, cue, evidence)
	return float64(int(ratio*100+0.5)) / 100
}

func cueAtomCoverage(vault *localvault.Vault, cue, evidence string) float64 {
	if vault == nil || strings.TrimSpace(cue) == "" || strings.TrimSpace(evidence) == "" {
		return 0
	}
	cueHashes := uniqueCueAtomHashes(vault, cue)
	if len(cueHashes) == 0 {
		return 0
	}
	evPkt, _ := vault.PackBlindPacket("E-COVERAGE", evidence, 0)
	seen := make(map[uint64]bool, len(evPkt.Slots))
	for _, atom := range evPkt.Slots {
		seen[atom.H] = true
	}
	matched := 0
	for _, h := range cueHashes {
		if seen[h] {
			matched++
		}
	}
	return float64(matched) / float64(len(cueHashes))
}

// roleTaggedBlocks returns up to maxBlocks role blocks verbatim, prefix included: each
// block is one role-tagged line plus the continuation lines that follow it (until the next
// role-tagged line). Keeping the continuation with its speaker is what keeps the stored
// evidence whole in any language — a block can never collapse to a bare label line while
// the content underneath (which carries no role tag) is left behind. The memory system
// records who said what; it never rules on what is right.
func roleTaggedBlocks(text string, maxBlocks int) []string {
	var blocks, current []string
	flush := func() {
		if len(current) > 0 {
			blocks = append(blocks, strings.Join(current, "\n"))
			current = nil
		}
	}
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "[") {
			flush()
			if maxBlocks > 0 && len(blocks) >= maxBlocks {
				break
			}
			if end := strings.Index(trimmed, "]"); end <= 1 || strings.TrimSpace(trimmed[end+1:]) == "" {
				continue
			}
			current = append(current, trimmed)
			continue
		}
		if len(current) > 0 {
			current = append(current, trimmed)
		}
	}
	flush()
	return blocks
}

// RememberNoetic persists an L3 Noetic memory record (Query + Retrieved Sentences + Answer)
// directly in the downstream client's own local SQLite table (`rm_noetic`).
//
// 评测命名空间 fail-closed：评测 scope 内的任何 Noetic 记录都会让语料的一一
// 相等校验失效，因此这里显式拒绝（与自动结晶的评测豁免同一条判据）。
func (m *Memory) RememberNoetic(scope, session, queryText string, citedUnitIDs, citedSentences []string, answerText string) (string, error) {
	if m == nil || m.Store == nil || m.Store.DB() == nil {
		return "", fmt.Errorf("RememberNoetic requires an open local SQLite store")
	}
	if strings.TrimSpace(scope) == "" && m.Config != nil {
		scope = m.Config.Scope
	}
	if evalns.IsBenchmarkScope(scope) || (m.Config != nil && evalns.IsBenchmarkScope(m.Config.Scope)) {
		return "", fmt.Errorf("RememberNoetic refuses the benchmark namespace scope %q: evaluation scopes stay corpus-only (no Noetic rows)", scope)
	}
	if err := m.checkLocalVaultScope(scope); err != nil {
		return "", err
	}
	if strings.TrimSpace(session) == "" {
		session = "default"
	}
	now := clock.Unix()
	noeticID := "nt_" + ids.SHA256Hex(scope + "\x00" + queryText + "\x00" + answerText)[:20]

	tc, err := m.LocalVault()
	if err != nil {
		return "", err
	}
	actualID, err := tc.RememberNoeticTrace(noeticID, queryText, citedUnitIDs, citedSentences, answerText, now)
	if err != nil {
		return "", err
	}
	pushNoeticPulse(tc, actualID, queryText, answerText, citedUnitIDs, now)
	return actualID, nil
}

// NoeticPulseBudget bounds one L3 crystallization pulse. It is exported because a
// one-shot caller (the `recall` CLI command, which exits right after printing) must
// wait at most this long for a scheduled pulse before the process goes away.
const NoeticPulseBudget = 1500 * time.Millisecond

// noeticPulseMaxInFlight bounds scheduled crystallization pulses: a recall is a read
// and must never accumulate goroutines behind a slow writer. On saturation the pulse is
// dropped on purpose — the local `rm_noetic` handle already exists and the next
// reconcile pulses it, so a dropped pulse costs freshness, never data.
const noeticPulseMaxInFlight = 4

var (
	noeticPulseSem = make(chan struct{}, noeticPulseMaxInFlight)
	noeticPulseWG  sync.WaitGroup
)

// rememberNoeticAsync persists the local L3 plaintext handle off the recall path for
// large corpora. It is tracked by the same WaitGroup as the crystallization pulses, so
// a one-shot recall (or the daemon's drain) cannot exit while the owner row is
// mid-write and silently lose it.
func rememberNoeticAsync(tc *localvault.Client, noeticID, query string, citedUnitIDs, citedSentences []string, answer string, occurredAt int64) {
	noeticPulseWG.Add(1)
	go func() {
		defer noeticPulseWG.Done()
		_ = tc.RememberNoetic(noeticID, query, citedUnitIDs, citedSentences, answer, occurredAt)
	}()
}

// pushNoeticPulseAsync schedules the crystallization pulse off the recall path. The read
// leg is a warm in-process query, while the pulse runs a full local ingest (measured
// 200-1200ms); serialising the write behind the read made every recall pay ingest latency
// for a result the caller never uses inline. A read-only recall surface enqueues the
// request instead, so the worker performs the ingest.
func (m *Memory) pushNoeticPulseAsync(tc *localvault.Client, noeticID, query, answer string, citedUnitIDs, citedSentences []string, occurredAt int64) {
	if m != nil && m.snapshotOnly.Load() {
		_ = m.EnqueueCrystallization(CrystallizationRequest{
			NoeticID:       noeticID,
			Query:          query,
			Answer:         answer,
			CitedUnitIDs:   append([]string(nil), citedUnitIDs...),
			CitedSentences: append([]string(nil), citedSentences...),
			OccurredAt:     occurredAt,
		})
		return
	}
	select {
	case noeticPulseSem <- struct{}{}:
	default:
		return
	}
	noeticPulseWG.Add(1)
	go func() {
		defer noeticPulseWG.Done()
		defer func() { <-noeticPulseSem }()
		pushNoeticPulse(tc, noeticID, query, answer, citedUnitIDs, occurredAt)
	}()
}

// WaitForPendingNoeticPulses blocks until every scheduled crystallization pulse has
// finished, or the budget elapses. It reports whether the set drained, so a caller can
// distinguish "pulses are late" from "pulses are done" without sleeping blindly.
func WaitForPendingNoeticPulses(budget time.Duration) bool {
	done := make(chan struct{})
	go func() {
		noeticPulseWG.Wait()
		close(done)
	}()
	if budget <= 0 {
		<-done
		return true
	}
	timer := time.NewTimer(budget)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}

// pushNoeticPulse writes the `layer="noetic"` record (query + answer + cited unit ids)
// into the local vault while the retrieval is still hot, so the L3 node and its `cites`
// edges materialise immediately; the `rm_noetic` row stays the plaintext owner and
// records the assigned memory address (`address_uri`, `coord64`, `waveform_bar`).
func pushNoeticPulse(tc *localvault.Client, noeticID, query, answer string, citedUnitIDs []string, occurredAt int64) {
	if tc == nil || tc.Transport == nil || tc.Vault == nil {
		return
	}
	pkt, _ := tc.Vault.PackBlindPacket(noeticID, query+"\n"+answer, occurredAt)
	pkt.Layer = localvault.LayerNoetic
	pkt.CitedUnitIDs = citedUnitIDs
	ctx, cancel := context.WithTimeout(context.Background(), NoeticPulseBudget)
	defer cancel()
	if resp, err := tc.Transport.IngestBlindPulse(ctx, pkt); err == nil && strings.TrimSpace(resp.AddressURI) != "" {
		_ = tc.Vault.UpdateLocalNoeticManifold(noeticID, resp.AddressURI, int64(resp.AddressCoord64), resp.WaveformBar)
	}
}

// episodicTurnSessionKey extracts the session stem (`@ref:zcode:<session>`,
// `@ref:opencode:<session>` or `@ref:jsonl:<path>`) from a turn reference so
// consecutive turns inside the same conversation stream can be ordered with
// recency supersession.
func episodicTurnSessionKey(ref string) string {
	trimmed := strings.TrimSpace(ref)
	if strings.HasPrefix(trimmed, "@ref:zcode:") {
		rest := strings.TrimPrefix(trimmed, "@ref:zcode:")
		if colon := strings.IndexByte(rest, ':'); colon > 0 {
			return "@ref:zcode:" + rest[:colon]
		}
	}
	if strings.HasPrefix(trimmed, "@ref:opencode:") {
		rest := strings.TrimPrefix(trimmed, "@ref:opencode:")
		if colon := strings.IndexByte(rest, ':'); colon > 0 {
			return "@ref:opencode:" + rest[:colon]
		}
	}
	if strings.HasPrefix(trimmed, "@ref:jsonl:") {
		if colon := strings.LastIndexByte(trimmed, ':'); colon > len("@ref:jsonl:") {
			return trimmed[:colon]
		}
	}
	return ""
}

// isInternalWarmupProbeCue reports whether a recall query is an internal daemon
// warmup probe that must never be crystallized into rm_noetic.
func isInternalWarmupProbeCue(query string) bool {
	q := strings.ToLower(strings.TrimSpace(query))
	return strings.HasPrefix(q, "daemon warmup") || strings.HasPrefix(q, "daemon-warmup")
}
