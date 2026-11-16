// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package api

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
	"github.com/rocareer/rolling-memory/internal/cloudcred"
	"github.com/rocareer/rolling-memory/internal/config"
	"github.com/rocareer/rolling-memory/internal/evalns"
	"github.com/rocareer/rolling-memory/internal/ids"
	"github.com/rocareer/rolling-memory/internal/langpack"
	"github.com/rocareer/rolling-memory/internal/recall"
	"github.com/rocareer/rolling-memory/internal/text"
	"github.com/rocareer/rolling-memory/internal/thinclient"
	"github.com/rocareer/rolling-memory/internal/tokenzr"
)

// IsThinBrainProxy reports whether this Memory instance operates via the
// Rolling Autonoetic Memory Engine Ultra-Thin Client (`internal/thinclient`: local SQLite plaintext
// vault + zero-plaintext `{h, b, f}` integer frames to `/open/memory/v1/{ingest,noetic,query,distill}`).
func (m *Memory) IsThinBrainProxy() bool {
	if m == nil || m.Config == nil || !strings.EqualFold(strings.TrimSpace(m.Config.Mode), "cloud") {
		return false
	}
	if strings.TrimSpace(os.Getenv("ROLLING_MEMORY_BRAIN_URL")) != "" {
		return true
	}
	return m.Config.Mode == "cloud" && m.Config.Cloud.Enabled &&
		(strings.Contains(m.Config.Cloud.BaseURL, "/open/memory/v1") || strings.Contains(m.Config.Cloud.BaseURL, "/brain/v1"))
}

// brainEndpointAndKey resolves the memory-plane endpoint and credential, in order of explicitness:
// environment (injected by the dashboard plugin) → explicit configuration → the machine account state.
// An explicitly pinned endpoint is never rewritten: the account state only supplies the missing
// credential for it. The only legal client-side credential is a data-plane signed key (`mem2.*`:
// issued by the control plane, verified offline by the data plane, renewed locally before expiry);
// the retired control-plane proxied path (`mem_*` user keys) is gone and no key material is compiled in.
func (m *Memory) brainEndpointAndKey() (baseURL, apiKey string) {
	baseURL = strings.TrimSpace(os.Getenv("ROLLING_MEMORY_BRAIN_URL"))
	apiKey = strings.TrimSpace(os.Getenv("ROLLING_MEMORY_BRAIN_KEY"))
	if m.Config != nil {
		if baseURL == "" {
			baseURL = strings.TrimRight(strings.TrimSpace(m.Config.Cloud.BaseURL), "/")
			baseURL = strings.TrimSuffix(baseURL, "/brain/v1")
		}
		if apiKey == "" {
			apiKey, _ = m.Config.SyncKey()
		}
	}
	if baseURL != "" && apiKey != "" {
		return baseURL, apiKey
	}
	accountURL, accountKey, err := cloudcred.Ensure(context.Background())
	if err != nil || accountURL == "" || accountKey == "" {
		return baseURL, apiKey
	}
	if baseURL != "" {
		return baseURL, accountKey
	}
	return accountURL, accountKey
}

// ThinClient returns an initialized Rolling Autonoetic Memory Engine Ultra-Thin Client
// bound to the local SQLite database and either the remote `/open/memory/v1` transport or local vault fallback.
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
	if err := m.Store.DB().QueryRow(`SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = 'thin_vault_meta')`).Scan(&metaExists); err != nil {
		return "", false, err
	}
	var bound string
	if metaExists != 0 {
		var storageType string
		err := m.Store.DB().QueryRow(`SELECT v, typeof(v) FROM thin_vault_meta WHERE k = 'local_plain_scope'`).Scan(&bound, &storageType)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return "", false, err
		}
		if err == nil && (storageType != "text" || strings.TrimSpace(bound) == "") {
			return "", false, fmt.Errorf("local_plain vault scope binding is invalid; preserve the database and inspect the binding")
		}
	}
	for _, table := range []string{"thin_vault_docs", "thin_vault_noetic", "thin_vault_bg_dirs", "thin_vault_bg_files", "thin_vault_bg_chunks", "thin_vault_episodic_lineage", "thin_vault_episodic_index"} {
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
	if err := m.Store.DB().QueryRow(`SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = 'thin_vault_meta')`).Scan(&metaExists); err == nil && metaExists != 0 {
		var bound, storageType string
		if err := m.Store.DB().QueryRow(`SELECT v, typeof(v) FROM thin_vault_meta WHERE k = 'local_plain_scope'`).Scan(&bound, &storageType); err == nil {
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
	if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS thin_vault_meta (k TEXT PRIMARY KEY, v BLOB NOT NULL)`); err != nil {
		return err
	}
	var bound, storageType string
	err = tx.QueryRow(`SELECT v, typeof(v) FROM thin_vault_meta WHERE k = 'local_plain_scope'`).Scan(&bound, &storageType)
	if errors.Is(err, sql.ErrNoRows) {
		for _, table := range []string{"thin_vault_docs", "thin_vault_noetic", "thin_vault_bg_dirs", "thin_vault_bg_files", "thin_vault_bg_chunks", "thin_vault_episodic_lineage", "thin_vault_episodic_index"} {
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
		if _, err := tx.Exec(`INSERT INTO thin_vault_meta(k, v) VALUES ('local_plain_scope', ?)`, scope); err != nil {
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
	if m.IsThinBrainProxy() && evalns.IsBenchmarkScope(scope) {
		return nil
	}
	if strings.TrimSpace(scope) != strings.TrimSpace(m.Config.Scope) {
		return fmt.Errorf("local_plain requested scope does not match the configured scope")
	}
	return nil
}

func (m *Memory) Warmup(ctx context.Context) error {
	tc, err := m.ThinClient()
	if err != nil {
		return err
	}
	if tc == nil || tc.Vault == nil {
		return nil
	}
	return tc.Vault.WarmupCorpusSnapshot(ctx)
}

func (m *Memory) ThinClient() (*thinclient.Client, error) {
	if m == nil || m.Store == nil || m.Store.DB() == nil {
		return nil, fmt.Errorf("thinclient requires an open local SQLite store")
	}
	if m.Config == nil {
		return nil, fmt.Errorf("thinclient requires a configured scope")
	}
	if err := m.bindLocalVaultScope(); err != nil {
		return nil, err
	}
	m.thinVaultOnce.Do(func() {
		if m.IsThinBrainProxy() {
			thinclient.PurgeLegacyTables(m.Store.DB())
		}
		m.thinVault, m.thinVaultErr = thinclient.NewVault(m.Store.DB(), nil)
		if m.thinVaultErr == nil {
			// 唯一 Vault 装配点（ingest 与 query 共用）：默认即最强装配
			// （max-capability packer：128k BPE 先验自信息 + 词干/词根泛音 + 结构化日期信标 +
			// 内联符号单位），与评测产品面（internal/eval 的 LocalIndexSurfaceProduct）同源同配。
			// 依据：Hard-31 同库单变量实测 max-capability 36.72%→46.12%（+9.40pp）、
			// Main-1013 73.58%→77.13%（+3.55pp）。
			// `ROLLING_MEMORY_LANGPACK=off` 显式退回纯平脉冲；显式指定语言包仅作诊断覆盖。
			if err := langpack.ConfigureDefault(m.thinVault, os.Getenv("ROLLING_MEMORY_LANGPACK")); err != nil {
				m.thinVaultErr = fmt.Errorf("langpack default assembly: %w", err)
			}
		}
	})
	if m.thinVaultErr != nil {
		return nil, m.thinVaultErr
	}
	// Reuse the expensive blind-slot index, but fork request-local persona state
	// so concurrent incarnations can never overwrite each other's identity.
	vault := m.thinVault.CloneWithSharedSlotCache()
	baseURL, key := "", ""
	// Local mode never resolves remote endpoints or credentials, including explicit overrides.
	if m.Config != nil && strings.EqualFold(strings.TrimSpace(m.Config.Mode), "cloud") {
		baseURL, key = m.brainEndpointAndKey()
	}
	if baseURL != "" && key != "" {
		tr := thinclient.NewHTTPBrainTransport(baseURL, key)
		// The pulse payload is the single on-wire encoding (no version negotiation), so the
		// transport needs no wire-proto knobs. Request-body gzip stays auto-negotiated: compress
		// first (measured -26~52%), fall back to plain once on rejection, remember in the vault.
		// Wire gzip 同样默认自动协商：先压缩请求体（实测 -26~52%），旧服务端拒绝一次后
		// 回退未压缩并记入本地金库，24h 后自动重探。
		switch strings.ToLower(strings.TrimSpace(os.Getenv("ROLLING_MEMORY_WIRE_GZIP"))) {
		case "0", "off", "no":
			tr.WireGzip = thinclient.WireGzipOff
		case "1", "on", "gzip":
			tr.WireGzip = thinclient.WireGzipForce
		default:
			if vault.ServerWireGzipUnsupported() {
				tr.WireGzip = thinclient.WireGzipOff
			}
		}
		tr.WireGzipFallback = func() { _ = vault.MarkServerWireGzipUnsupported() }
		userID := strings.TrimSpace(os.Getenv("ROLLING_MEMORY_OPEN_MEMORY_USER"))
		if userID == "" {
			userID = strings.TrimSpace(os.Getenv("ROLLING_MEMORY_USER_ID"))
		}
		if userID == "" {
			userID = thinclient.DefaultUpstreamDevUserID
		}
		tr.UserID = userID
		if m.Config != nil && !config.IsDefaultScope(m.Config.Scope) {
			tr.Scope = strings.ReplaceAll(strings.TrimSpace(m.Config.Scope), ":", "-")
		}
		client := thinclient.NewClient(vault, tr)
		client.SetIdentity(localPersonaIdentity())
		return client, nil
	}
	client := thinclient.NewLocalClient(vault)
	client.SetIdentity(localPersonaIdentity())
	return client, nil
}

func (m *Memory) thinClientRemember(req RememberRequest) (map[string]any, error) {
	started := clock.MonotonicMS()
	scope := m.Config.Scope
	if req.Scope != nil && strings.TrimSpace(*req.Scope) != "" {
		scope = strings.TrimSpace(*req.Scope)
	}
	if err := m.checkLocalVaultScope(scope); err != nil {
		return nil, err
	}
	session := strings.TrimSpace(req.Session)
	if session == "" {
		session = "default"
	}
	var textParts []string
	if req.Content != nil && strings.TrimSpace(*req.Content) != "" {
		textParts = append(textParts, ids.StripInjected(strings.TrimSpace(*req.Content)))
	} else if req.SourceText != nil && strings.TrimSpace(*req.SourceText) != "" {
		textParts = append(textParts, ids.StripInjected(strings.TrimSpace(*req.SourceText)))
	}
	if len(textParts) == 0 && req.Assertion != nil {
		textParts = append(textParts, ids.StableJSON(req.Assertion))
	}
	if len(textParts) == 0 && len(req.Assertions) > 0 {
		textParts = append(textParts, ids.StableJSON(req.Assertions))
	}
	text := strings.Join(textParts, "\n")
	if strings.TrimSpace(text) == "" {
		return nil, fmt.Errorf("thinclient remember requires non-empty content")
	}
	docID := "rm_" + ids.SHA256Hex(scope + "\x00" + session + "\x00" + text)[:24]
	if req.Dry {
		return map[string]any{
			"ok":        true,
			"dry":       true,
			"scope":     scope,
			"extractor": thinclient.EngineName,
		}, nil
	}
	tc, err := m.ThinClient()
	if err != nil {
		return nil, err
	}
	now := clock.Unix()
	occ := ids.ParseTime(req.OccurredAt, ids.ParseTimeOptions{TimeRef: &now, Default: now})
	resp, err := tc.Remember(context.Background(), docID, text, occ)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"ok":        true,
		"dry":       false,
		"scope":     scope,
		"extractor": thinclient.EngineName,
		"written": []any{
			map[string]any{
				"id":          docID,
				"address_uri": resp.AddressURI,
				"waveform":    resp.WaveformBar,
				"reason":      "thin_vault_ingested",
			},
		},
		"skipped":       []any{},
		"superseded":    []any{},
		"supplemented":  []any{},
		"conflicts":     0,
		"anchor_failed": 0,
		"trace": map[string]any{
			"hash":     docID,
			"session":  session,
			"inserted": true,
		},
		"meta": map[string]any{
			"engine":      thinclient.EngineName,
			"address_uri": resp.AddressURI,
			"coord64":     resp.AddressCoord64,
			"waveform":    resp.WaveformBar,
			"elapsed_ms":  int(clock.MonotonicMS() - started),
		},
	}, nil
}

// MirrorSessionTracesToVault mirrors this session's not-yet-vaulted traces into the
// local plaintext vault. Hook-captured payload-only turns (hosts that hand the hook
// a prompt/last_assistant_message but no transcript file) land in `trace` as their
// only copy; without this session-scoped mirror they stay stranded there forever —
// the transcript scanners never see them and the full mirror only runs on CLI
// `import`. Session scoping keeps the hook-time cost bounded to the turn's own
// session instead of sweeping the whole trace table.
func (m *Memory) MirrorSessionTracesToVault(session string) error {
	if m == nil || m.Config == nil {
		return nil
	}
	session = strings.TrimSpace(session)
	if session == "" {
		return nil
	}
	return m.rememberImportedTracesToVault(m.Config.Scope, &session)
}

// rememberImportedTracesToVault mirrors freshly imported transcript traces into the
// local plaintext vault so local-mode recall has a real lexical index without cloud.
// It is intentionally local-only: cloud modes keep their existing ingest path.
func (m *Memory) rememberImportedTracesToVault(scope string, session *string) error {
	if m == nil || m.Store == nil || m.Config == nil || !strings.EqualFold(strings.TrimSpace(m.Config.Mode), "local") {
		return nil
	}
	if err := m.checkLocalVaultScope(scope); err != nil {
		return err
	}
	tc, err := m.ThinClient()
	if err != nil {
		return err
	}
	traces, err := m.Store.PendingVaultTraces(scope, session)
	if err != nil {
		return err
	}
	for _, tr := range traces {
		// The vault cannot preserve the trace audience. Never mirror private or
		// unknown-audience imports into a store that would lose that restriction.
		if tr.Audience != "shared" && tr.Audience != "public" {
			continue
		}
		text := strings.TrimSpace(ids.StripInjected(tr.Text))
		if text == "" {
			continue
		}
		occurred := tr.OccurredAt
		if occurred == 0 {
			occurred = tr.RecordedAt
		}
		if _, err := tc.RememberEpisodic(context.Background(), "ep_"+tr.Hash, text, occurred); err != nil {
			return err
		}
	}
	return nil
}

// recentVaultItems renders the time-ordered vault view in the flat receipt shape the
// legacy `recent` command and the boot pulse already consume (`time`/`rendered`/
// `anchor`/`confidence`/`status`), so both paths agree on one source of truth.
func (m *Memory) recentVaultItems(limit int) ([]map[string]any, error) {
	tc, err := m.ThinClient()
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
	if offset > 0 {
		depth += offset
	}
	if depth > maxRecallCandidateDepth {
		depth = maxRecallCandidateDepth
	}
	return depth
}

// recentVaultRecall serves the time-ordered `recent` recall primitive: the
// newest local vault entries (L1/L2 documents plus L3 Noetic records) with
// layer and time labels. Pure time ordering — zero semantic matching and zero
// cloud traffic, so it answers "what changed just now" directly.
func (m *Memory) recentVaultRecall(req RecallRequest) (*recall.Result, error) {
	started := clock.MonotonicMS()
	scope := m.Config.Scope
	if req.Scope != nil && strings.TrimSpace(*req.Scope) != "" {
		scope = strings.TrimSpace(*req.Scope)
	}
	limit := boundedRecallDepth(req.RawLimit, req.CapsBands, req.Offset)
	tc, err := m.ThinClient()
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
func allNoetic(entries []thinclient.RecentDoc) bool {
	for _, entry := range entries {
		if !strings.EqualFold(strings.TrimSpace(entry.Layer), "noetic") {
			return false
		}
	}
	return len(entries) > 0
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
func foldRecentNoeticDerivatives(entries []thinclient.RecentDoc, limit int, anchor string) []thinclient.RecentDoc {
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
		matched := make([]thinclient.RecentDoc, 0, len(entries))
		others := make([]thinclient.RecentDoc, 0, len(entries))
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
	primary := make([]thinclient.RecentDoc, 0, len(entries))
	folded := make([]thinclient.RecentDoc, 0, len(entries))
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

func (m *Memory) thinClientRecall(req RecallRequest) (*recall.Result, error) {
	doneFG := beginForegroundRecall()
	defer doneFG()
	scope := m.Config.Scope
	if req.Scope != nil && strings.TrimSpace(*req.Scope) != "" {
		scope = strings.TrimSpace(*req.Scope)
	}
	if err := m.checkLocalVaultScope(scope); err != nil {
		return nil, err
	}
	// The local vault cannot enforce these restrictions across all delivered sources.
	if !m.IsThinBrainProxy() && (req.Session != nil || req.AsOf != nil || req.WindowStart != nil || req.WindowEnd != nil) {
		return nil, fmt.Errorf("local_plain recall does not support session, asof or window filters")
	}
	started := clock.MonotonicMS()
	if strings.EqualFold(strings.TrimSpace(req.Mode), "recent") {
		return m.recentVaultRecall(req)
	}
	topK := boundedRecallDepth(req.RawLimit, req.CapsBands, req.Offset)
	// Retrieval always runs on the explicit cue: the s/x/o hint slots that once
	// synthesized a query were removed with the structured memory assertion contract (2026-11-06).
	query := strings.TrimSpace(req.Cue)
	tc, err := m.ThinClient()
	if err != nil {
		return nil, err
	}
	// The calling incarnation IS the concrete lifetime instance: stamp it so the L3 Noetic
	// record this recall crystallizes stays attributable, without narrowing the search scope.
	if strings.TrimSpace(req.IncarnationID) != "" {
		tc.SetIdentity(tc.AssistantID, strings.TrimSpace(req.IncarnationID), tc.Locus)
	}
	noeticHits, _ := tc.RecallNoetic(query, topK)
	queryCtx := context.Background()
	budgetMS := 10000
	if req.BudgetMS != nil {
		budgetMS = *req.BudgetMS
	} else if m.Config != nil && m.Config.Recall.BudgetMS > 0 && m.Config.Recall.BudgetMS != 500 {
		budgetMS = m.Config.Recall.BudgetMS
	}
	if !m.IsThinBrainProxy() && tc.Vault != nil && tc.Vault.CorpusSize() == 0 && budgetMS < 35000 {
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
	cloudFetchK := topK * 3
	if cloudFetchK < 20 {
		cloudFetchK = 20
	}
	if !m.IsThinBrainProxy() && tc.Vault != nil && tc.Vault.CorpusSize() >= 2048 {
		cloudFetchK = topK + 4
		if cloudFetchK < 12 {
			cloudFetchK = 12
		}
	}
	now := clock.Unix()
	timeRef := ids.ParseTime(req.TimeRef, ids.ParseTimeOptions{TimeRef: &now, Default: now})
	asOf := ids.ParseTime(req.AsOf, ids.ParseTimeOptions{TimeRef: &timeRef, Default: 0})
	hits, err := tc.RecallWithOptions(queryCtx, query, thinclient.BlindQueryOptions{
		TopK:    cloudFetchK,
		AsOf:    asOf,
		TimeRef: timeRef,
		// Cognitive-cluster delivery view (protocol noetic-cluster-delivery-v1,
		// slice S4b): HTTP proxy arm only — the engine regroups its ranked hits
		// read-only and rides the ordinal day annotation along; ranking and the
		// hits themselves are untouched.
		ClusterGroups: m.IsThinBrainProxy(),
	})
	if err != nil {
		return nil, err
	}
	// L3 is a retrieval handle, not evidence. Keep it out of the evidence stream
	// so local fallback cannot self-amplify an old Noetic into the top delivery.
	evidenceHits := hits[:0]
	for _, h := range hits {
		if h.Layer == thinclient.LayerNoetic || strings.HasPrefix(h.UnitID, "nt_") || strings.HasPrefix(h.UnitID, "noetic_") {
			continue
		}
		evidenceHits = append(evidenceHits, h)
	}
	hits = evidenceHits

	// L3 -> L1/L2 Citation Resonance Gain: when a past Noetic handle matches the current
	// cue, boost its cited evidence units (and hydrate any cited unit missing from the
	// initial topK) so proven evidence from past sessions bubbles to the top of delivery.
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
			boostMult := 1.0 + 0.35*nh.MatchScore
			for _, cid := range nh.CitedUnitIDs {
				cid = strings.TrimSpace(cid)
				if cid == "" || strings.HasPrefix(cid, "nt_") || strings.HasPrefix(cid, "noetic_") {
					continue
				}
				if idx, ok := hitIdxByID[cid]; ok {
					hits[idx].FinalWeight *= boostMult
				} else if len(hits) < topK*2 {
					rawTxt, distTxt, keptSents, lErr := tc.Vault.LoadLocalDocument(cid)
					if lErr != nil {
						hitIdxByID[cid] = len(hits)
						hits = append(hits, thinclient.LocalResolvedHit{CloudQueryHit: thinclient.CloudQueryHit{UnitID: cid}, SourceUnavailable: true})
					} else if strings.TrimSpace(rawTxt) != "" {
						layer := tc.Vault.LoadLocalDocumentLayer(cid)
						hitIdxByID[cid] = len(hits)
						hits = append(hits, thinclient.LocalResolvedHit{
							CloudQueryHit: thinclient.CloudQueryHit{
								UnitID:      cid,
								Layer:       layer,
								AddressURI:  nh.AddressURI,
								FinalWeight: maxW * (0.72 * nh.MatchScore),
							},
							RawPlaintext:       rawTxt,
							DistilledPlaintext: distTxt,
							KeptSentences:      keptSents,
							Authors:            thinclient.RoleTaggedAuthors(rawTxt),
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
	anchor := recall.LocusAnchor(query)
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

	// Group hits by turn, but deliver only units independently verified against
	// their indexed source identity; neighboring content has no implicit authority.
	contentTruncated := false
	groupOrder := make([]string, 0, len(hits))
	groups := make(map[string]*episodicTurnGroup, len(hits))
	standalone := make([]thinclient.LocalResolvedHit, 0, len(hits))
	for _, h := range hits {
		if h.SourceUnavailable {
			sourceUnavailable[h.UnitID] = true
			continue
		}
		pointer := ""
		if tc.Vault != nil {
			pointer = tc.Vault.EpisodicPointerFor(h.UnitID)
		}
		if pointer == "" {
			standalone = append(standalone, h)
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(h.RawPlaintext), "@ref:") {
			sourceUnavailable[h.UnitID] = true
		}
		ref, segmentIndex := splitTurnRefSegment(pointer)
		if ref == strings.TrimSpace(pointer) {
			// A whole-turn pointer cannot prove authority over its constituent segments.
			sourceUnavailable[h.UnitID] = true
			continue
		}
		group := groups[ref]
		if group == nil {
			group = &episodicTurnGroup{ref: ref, rawPointer: pointer, hitIndex: segmentIndex, score: h.FinalWeight, unitID: h.UnitID, authors: h.Authors}
			groups[ref] = group
			groupOrder = append(groupOrder, ref)
		}
		group.hits = append(group.hits, map[string]any{
			"unit": h.UnitID, "pointer": pointer, "segment": segmentIndex, "verified": h.RawPlaintext,
		})
		if h.FinalWeight > group.score {
			group.score = h.FinalWeight
			group.rawPointer = pointer
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
	competingBands := len(groupOrder) + len(standalone)
	if competingBands > topK {
		competingBands = topK
	}
	effectiveChars := deliveryChars
	if competingBands > 1 && effectiveChars > competingBands*160 {
		effectiveChars -= competingBands * 96
	}
	windowBudget := turnWindowBudget(effectiveChars, competingBands)
	type deliveredCandidate struct {
		band       map[string]any
		rawHit     map[string]any
		score      float64
		locusMatch bool
		sessionKey string
		occurredAt int64
		order      int
		truncated  bool
	}
	candidates := make([]deliveredCandidate, 0, len(groupOrder)+len(standalone))
	nextOrder := 0
	for _, ref := range groupOrder {
		group := groups[ref]
		// Lineage establishes association, not authorization to deliver another hit:
		// only segments actually matched by retrieval and verified via LoadVerifiedEpisodicPointer are delivered.
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
			if strings.TrimSpace(resolved) == "" || strings.HasPrefix(strings.TrimSpace(resolved), "@ref:") {
				var verifyErr error
				resolved, verifyErr = tc.Vault.LoadVerifiedEpisodicPointer(unit, hit["pointer"].(string))
				if verifyErr != nil {
					sourceUnavailable[unit] = true
					continue
				}
			}
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
		original := text
		text = focusEpisodicDeliveryText(text, query, windowBudget)
		wasTruncated := text != original
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
			score:      group.score,
			locusMatch: anchor != "" && strings.EqualFold(strings.TrimSpace(locus), anchor),
			sessionKey: episodicTurnSessionKey(ref),
			occurredAt: turnOccurredAt,
			order:      nextOrder,
			truncated:  wasTruncated,
		})
		nextOrder++
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
		maxNodeChars := windowBudget - 160
		if maxNodeChars < 240 && windowBudget > 0 {
			maxNodeChars = windowBudget
		}
		wasTruncated := false
		if maxNodeChars > 0 {
			original := delivered
			delivered = focusEpisodicDeliveryText(delivered, query, maxNodeChars)
			if delivered != original {
				wasTruncated = true
			}
		}
		spaceLabel := ""
		if tc.Vault != nil {
			if relPath, _, locAnchor, ok := tc.Vault.BackgroundChunkLocation(h.UnitID); ok {
				if locAnchor != "" {
					spaceLabel = relPath + "#" + locAnchor
				} else {
					spaceLabel = relPath
				}
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
			score:      h.FinalWeight,
			locusMatch: anchor != "" && strings.EqualFold(strings.TrimSpace(locus), anchor),
			order:      nextOrder,
			truncated:  wasTruncated,
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
	for _, cand := range candidates {
		if len(bands) >= topK {
			break
		}
		if cand.truncated {
			contentTruncated = true
		}
		cand.rawHit["turn"] = int64(len(rawHits) + 1)
		bands = append(bands, cand.band)
		rawHits = append(rawHits, cand.rawHit)
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
		rendered := "Cue: " + nh.QueryText
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
			"label":             nh.QueryText,
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
			"title":      "带·" + strings.TrimSpace(nh.QueryText),
			"subject":    nh.QueryText,
			"time_label": noeticTimeLabel,
			"score":      nh.MatchScore,
			"nodes":      []map[string]any{nodeMap},
		})
	}
	benchmarkScope := evalns.IsBenchmarkScope(scope)
	if m.Config != nil && evalns.IsBenchmarkScope(m.Config.Scope) {
		benchmarkScope = true
	}
	if query != "" && !isInternalWarmupProbeCue(query) && len(hits) > 0 && req.Mode != "probe" && !req.NoCrystallize && !benchmarkScope {
		var citedIDs []string
		var citedSents []string
		seenCite := make(map[string]bool)
		for _, h := range selectNoeticCitationHits(tc.Vault, query, hits) {
			uid := strings.TrimSpace(h.UnitID)
			if uid == "" {
				continue
			}
			sent := strings.TrimSpace(h.DistilledPlaintext)
			if sent == "" {
				sent = strings.TrimSpace(h.RawPlaintext)
			}
			sent = truncateNoeticEvidence(sent)
			if sent == "" || sourceUnavailable[uid] {
				continue
			}
			if h.Layer == thinclient.LayerEpisodic {
				// Faithful recording, zero correctness judgment (highest philosophy: the memory
				// system is NOT the arbiter of truth). Every role-tagged span is kept WITH its role
				// prefix intact, so the downstream model can weigh who said what; nothing is
				// dropped, preferred, or promoted as "trusted", and old assistant replies are
				// never misattributed as the answer to the current query.
				spans := roleTaggedSpans(sent, 4)
				if len(spans) > 0 {
					if len(citedSents) < 3 {
						citedSents = append(citedSents, strings.Join(spans, "\n"))
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
			noeticID := "nt_" + ids.SHA256Hex(scope + "\x00" + query + "\x00" + strings.Join(citedIDs, ","))[:16]
			now := clock.Unix()
			if tc.Vault != nil && tc.Vault.CorpusSize() >= 2048 {
				citedIDsCopy := append([]string(nil), citedIDs...)
				citedSentsCopy := append([]string(nil), citedSents...)
				rememberNoeticAsync(tc, noeticID, query, citedIDsCopy, citedSentsCopy, ansSummary, now)
			} else {
				_ = tc.RememberNoetic(noeticID, query, citedIDs, citedSents, ansSummary, now)
			}
			if !diag.Degraded {
				pushNoeticPulseAsync(tc, noeticID, query, ansSummary, citedIDs, now)
			}
		}
	}

	meta := map[string]any{
		"engine":      thinclient.EngineName,
		"count":       len(hits),
		"noetic_hits": len(noeticHits),
		"elapsed_ms":  int(clock.MonotonicMS() - started),
	}
	// Cognitive-cluster delivery view (slice S4b): surface the engine's read-only
	// regrouping and the ordinal day annotation to the LLM via recall meta. The
	// view only references unit IDs already present in the delivery, so no new
	// evidence enters through this channel.
	if httpTr, ok := tc.Transport.(*thinclient.HTTPBrainTransport); ok {
		if view := httpTr.DeliveryView(); len(view.ClusterGroups) > 0 {
			meta["cluster_groups"] = view.ClusterGroups
			if view.OrdinalAnnotation != nil {
				meta["ordinal_annotation"] = view.OrdinalAnnotation
			}
		}
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

	indexIncomplete := 0
	if _, local := tc.Transport.(*thinclient.LocalVaultTransport); local && tc.Vault != nil {
		index, indexErr := tc.Vault.EpisodicIndexFaultDiagnostics()
		if indexErr != nil {
			meta["episodic_index_error"] = "diagnostics_unavailable"
			indexIncomplete = 1
		} else {
			meta["episodic_index"] = map[string]int{
				"compatible": index.Compatible, "missing": index.Missing,
				"incompatible": index.Incompatible, "corrupt": index.Corrupt,
				"needs_rebuild": index.NeedsRebuild,
			}
			indexIncomplete = index.Missing + index.Incompatible + index.Corrupt + index.NeedsRebuild
		}
	}
	gaps := map[string]int{"source_unavailable": len(sourceUnavailable)}
	if indexIncomplete > 0 {
		gaps["index_incomplete"] = indexIncomplete
	}
	result := &recall.Result{
		Scope:   scope,
		Bands:   bands,
		RawHits: rawHits,
		Gaps:    gaps,
		Absent:  len(bands) == 0 && len(hits) == 0 && !diag.Degraded && len(sourceUnavailable) == 0 && indexIncomplete == 0,
		Meta:    meta,
	}
	// 交付面再次保证「带锚者排最前」（同图存管线口径，fail-open）。
	recall.PromoteBandsByLocusAnchor(result, query)
	applyThinDeliveryCaps(result, req)
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

// orderHitsByLocusAnchor 把命中锚（= 入库 locus，仓库目录名）的候选稳定置前、其余候选
// 原序压后，不删除任何候选（fail-open）；与图存管线 orderCandidatesByLocusAnchor 同口径
// （见 internal/recall/locus_scope.go）。
func orderHitsByLocusAnchor(hits []thinclient.LocalResolvedHit, anchor string, locusByDoc map[string]string) []thinclient.LocalResolvedHit {
	if strings.TrimSpace(anchor) == "" || len(hits) < 2 {
		return hits
	}
	same := make([]thinclient.LocalResolvedHit, 0, len(hits))
	other := make([]thinclient.LocalResolvedHit, 0, len(hits))
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

func uniqueCueAtomHashes(vault *thinclient.Vault, cue string) []uint64 {
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
func selectNoeticCitationHits(vault *thinclient.Vault, query string, hits []thinclient.LocalResolvedHit) []thinclient.LocalResolvedHit {
	cueHashes := uniqueCueAtomHashes(vault, query)
	bestCoverage := -1.0
	selected := make([]thinclient.LocalResolvedHit, 0, 4)
	inspected := 0
	for _, h := range hits {
		if h.FinalWeight <= 0 || h.Layer == thinclient.LayerNoetic || strings.HasPrefix(h.UnitID, "nt_") || strings.HasPrefix(h.UnitID, "noetic_") {
			continue
		}
		text := h.DistilledPlaintext
		if strings.TrimSpace(text) == "" {
			text = h.RawPlaintext
		}
		coverage := vault.DocAtomCoverageWithCueHashes(h.UnitID, text, cueHashes)
		if coverage > bestCoverage {
			bestCoverage = coverage
			selected = []thinclient.LocalResolvedHit{h}
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
func cueCoverage(vault *thinclient.Vault, cue, evidence string) float64 {
	ratio := cueAtomCoverage(vault, cue, evidence)
	return float64(int(ratio*100+0.5)) / 100
}

func cueAtomCoverage(vault *thinclient.Vault, cue, evidence string) float64 {
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

// roleTaggedSpans returns role-tagged lines verbatim, prefix included, so provenance survives into
// the stored evidence. The memory system records who said what; it never rules on what is right.
func roleTaggedSpans(text string, maxLines int) []string {
	var kept []string
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "[") {
			continue
		}
		end := strings.Index(trimmed, "]")
		if end <= 1 || strings.TrimSpace(trimmed[end+1:]) == "" {
			continue
		}
		kept = append(kept, trimmed)
		if maxLines > 0 && len(kept) >= maxLines {
			break
		}
	}
	return kept
}

// RememberNoetic persists an L3 Noetic memory record (Query + Retrieved Sentences + Answer)
// directly in the downstream client's own local SQLite table (`thin_vault_noetic`).
//
// 评测命名空间 fail-closed：评测 scope 内的任何 Noetic 记录都会让 `/units` 与语料的一一
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

	tc, err := m.ThinClient()
	if err != nil {
		return "", err
	}
	if err := tc.RememberNoetic(noeticID, queryText, citedUnitIDs, citedSentences, answerText, now); err != nil {
		return "", err
	}
	pushNoeticPulse(tc, noeticID, queryText, answerText, citedUnitIDs, now)
	return noeticID, nil
}

// NoeticPulseBudget bounds one L3 crystallization push. It is exported because a
// one-shot caller (the `recall` CLI command, which exits right after printing) must
// wait at most this long for a scheduled push before the process goes away.
const NoeticPulseBudget = 1500 * time.Millisecond

// noeticPulseMaxInFlight bounds scheduled crystallization pushes: a recall is a read
// and must never accumulate goroutines behind a slow engine. On saturation the pulse is
// dropped on purpose — the local `thin_vault_noetic` handle already exists and the next
// reconcile pushes it, so a dropped push costs freshness, never data.
const noeticPulseMaxInFlight = 4

var (
	noeticPulseSem = make(chan struct{}, noeticPulseMaxInFlight)
	noeticPulseWG  sync.WaitGroup
)

// rememberNoeticAsync persists the local L3 plaintext handle off the recall path for
// large corpora. It is tracked by the same WaitGroup as the engine pushes, so a
// one-shot recall (or the daemon's drain) cannot exit while the owner row is
// mid-write and silently lose it.
func rememberNoeticAsync(tc *thinclient.Client, noeticID, query string, citedUnitIDs, citedSentences []string, answer string, occurredAt int64) {
	noeticPulseWG.Add(1)
	go func() {
		defer noeticPulseWG.Done()
		_ = tc.RememberNoetic(noeticID, query, citedUnitIDs, citedSentences, answer, occurredAt)
	}()
}

// pushNoeticPulseAsync schedules the crystallization push off the recall path. The read
// leg costs one engine query (measured 0.6ms transport on loopback), while the pulse
// costs a full memory-engine ingest (measured 200-1200ms); serialising the write behind the
// read made every recall pay ingest latency for a result the caller never uses inline.
func pushNoeticPulseAsync(tc *thinclient.Client, noeticID, query, answer string, citedUnitIDs []string, occurredAt int64) {
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

// WaitForPendingNoeticPulses blocks until every scheduled crystallization push has
// finished, or the budget elapses. It reports whether the set drained, so a caller can
// distinguish "pushes are late" from "pushes are done" without sleeping blindly.
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

// pushNoeticPulse sends the blind `layer="noetic"` pulse (query + answer + cited unit ids) to
// `/open/memory/v1/ingest` so the engine materialises the L3 node and its `cites` edges while
// the retrieval is still hot; the local `thin_vault_noetic` row stays the plaintext owner and
// records the engine-assigned C6 cell address (`address_uri`, `coord64`, `waveform_bar`).
func pushNoeticPulse(tc *thinclient.Client, noeticID, query, answer string, citedUnitIDs []string, occurredAt int64) {
	if tc == nil || tc.Transport == nil || tc.Vault == nil {
		return
	}
	pkt, _ := tc.Vault.PackBlindPacket(noeticID, query+"\n"+answer, occurredAt)
	pkt.Layer = thinclient.LayerNoetic
	pkt.CitedUnitIDs = citedUnitIDs
	ctx, cancel := context.WithTimeout(context.Background(), NoeticPulseBudget)
	defer cancel()
	if resp, err := tc.Transport.IngestBlindPulse(ctx, pkt); err == nil && strings.TrimSpace(resp.AddressURI) != "" {
		_ = tc.Vault.UpdateLocalNoeticManifold(noeticID, resp.AddressURI, int64(resp.AddressCoord64), resp.WaveformBar)
	}
}

// episodicTurnSessionKey extracts the session stem (`@ref:zcode:<session>` or
// `@ref:jsonl:<path>`) from a turn reference so consecutive turns inside the same
// conversation stream can be ordered with recency supersession.
func episodicTurnSessionKey(ref string) string {
	trimmed := strings.TrimSpace(ref)
	if strings.HasPrefix(trimmed, "@ref:zcode:") {
		rest := strings.TrimPrefix(trimmed, "@ref:zcode:")
		if colon := strings.IndexByte(rest, ':'); colon > 0 {
			return "@ref:zcode:" + rest[:colon]
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
// warmup probe that must never be crystallized into thin_vault_noetic.
func isInternalWarmupProbeCue(query string) bool {
	q := strings.ToLower(strings.TrimSpace(query))
	return strings.HasPrefix(q, "daemon warmup") || strings.HasPrefix(q, "daemon-warmup")
}
