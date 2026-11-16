// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

// Package api is the embedded facade used by the CLI, MCP server and harness
// hooks. It wires config -> store -> remember/recall and exposes the governed
// read/write surface without adding policy of its own.
package api

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/rocareer/rolling-memory/internal/clock"
	"github.com/rocareer/rolling-memory/internal/config"
	"github.com/rocareer/rolling-memory/internal/db"
	"github.com/rocareer/rolling-memory/internal/efficacy"
	"github.com/rocareer/rolling-memory/internal/ids"
	"github.com/rocareer/rolling-memory/internal/llm"
	"github.com/rocareer/rolling-memory/internal/recall"
	"github.com/rocareer/rolling-memory/internal/store"
	"github.com/rocareer/rolling-memory/internal/text"
	"github.com/rocareer/rolling-memory/internal/thinclient"
	"github.com/rocareer/rolling-memory/internal/transcript"
	"github.com/rocareer/rolling-memory/internal/version"
)

// Memory is one open scope-bound facade.
type Memory struct {
	Config     *config.Config
	Store      *store.Store
	LLM        llm.Extractor
	LLMWarning *string

	thinVaultOnce sync.Once
	thinVault     *thinclient.Vault
	thinVaultErr  error
}

// New opens the database and builds the optional extractor.
func New(cfg *config.Config) (*Memory, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config is required")
	}
	database, err := store.Open(cfg)
	if err != nil {
		return nil, err
	}
	extractor, warning := llm.BuildLLM(cfg)
	return &Memory{Config: cfg, Store: database, LLM: extractor, LLMWarning: warning}, nil
}

// Close closes the store (the usb profile checkpoints the WAL first).
func (m *Memory) Close() error {
	if m.Store == nil {
		return nil
	}
	return m.Store.Close()
}

// Eject flushes and truncates the WAL journal and runs an integrity check so a
// removable USB/portable drive can be safely unmounted.
func (m *Memory) Eject() (map[string]any, error) {
	var busy, logFrames, checkpointed int
	row := m.Store.DB().QueryRow("PRAGMA wal_checkpoint(TRUNCATE)")
	_ = row.Scan(&busy, &logFrames, &checkpointed)
	integrity, err := m.Store.Check()
	if err != nil {
		return nil, err
	}
	ok, _ := integrity["ok"].(bool)
	return map[string]any{
		"ok":           ok && busy == 0,
		"db":           m.Config.DB,
		"profile":      m.Config.Storage.Profile,
		"checkpoint":   "TRUNCATE",
		"busy":         busy,
		"log_frames":   logFrames,
		"checkpointed": checkpointed,
		"integrity":    integrity,
	}, nil
}

// RememberRequest carries the write-side keyword arguments.
type RememberRequest struct {
	Content       *string
	SourceText    *string
	AnchorText    *string
	Assertion     map[string]any
	Assertions    []map[string]any
	Scope         *string
	Session       string
	Turn          any
	Author        string
	Audience      string
	OccurredAt    any
	Dry           bool
	TimeRef       any
	Aliases       []map[string]any
	AutoSync      *bool
	PresenceLocus string
	Presence      *text.PresenceCoordinates
}

func (req RememberRequest) effectivePresence() *text.PresenceCoordinates {
	if req.Presence != nil && !req.Presence.IsEmpty() {
		norm := req.Presence.Normalized()
		return &norm
	}
	if strings.TrimSpace(req.PresenceLocus) != "" {
		norm := text.PresenceCoordinates{Locus: req.PresenceLocus}.Normalized()
		return &norm
	}
	return nil
}

// Remember runs a thinclient remember or seeds structured test assertions locally.
func (m *Memory) Remember(req RememberRequest) (map[string]any, error) {
	if m.IsThinBrainProxy() {
		return m.thinClientRemember(req)
	}
	return m.seedStructuredAssertion(req)
}

func (m *Memory) seedStructuredAssertion(req RememberRequest) (map[string]any, error) {
	effScope := m.Config.Scope
	if req.Scope != nil && *req.Scope != "" {
		effScope = *req.Scope
	}
	session := req.Session
	if session == "" {
		session = "default"
	}
	audience := req.Audience
	if audience == "" {
		audience = "shared"
	}
	author := req.Author
	if author == "" {
		author = "user"
	}
	now := clock.Unix()
	timeRef := int64Ptr(req.TimeRef)
	if timeRef == nil {
		timeRef = &now
	}
	occurredAt := ids.ParseTime(req.OccurredAt, ids.ParseTimeOptions{TimeRef: timeRef, Default: now})
	content := stringValue(req.Content)
	if content == "" {
		content = stringValue(req.SourceText)
	}
	items := req.Assertions
	if items == nil && req.Assertion != nil {
		items = []map[string]any{req.Assertion}
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("assertions must be a non-empty list")
	}
	if req.Dry {
		return map[string]any{
			"ok": true, "dry": true, "scope": effScope, "extractor": "structured",
			"written": []any{}, "skipped": []any{}, "supplemented": []any{}, "superseded": []any{},
		}, nil
	}
	traceHash, traceInserted, err := m.Store.AddTrace(effScope, content, store.TraceOptions{
		Session: session, Turn: req.Turn, Kind: "remember", Author: author, Audience: audience, OccurredAt: &occurredAt,
	})
	if err != nil {
		return nil, err
	}
	resolveSlot := func(val any) (*string, *string, string, error) {
		switch v := val.(type) {
		case string:
			lbl := strings.TrimSpace(v)
			if lbl == "" {
				return nil, nil, "", nil
			}
			id, e := m.Store.EnsureNode(effScope, "referent", lbl, store.NodeOptions{Audience: audience})
			return &id, nil, lbl, e
		case map[string]any:
			if lbl := strings.TrimSpace(asString(v["label"])); lbl != "" {
				id, e := m.Store.EnsureNode(effScope, "referent", lbl, store.NodeOptions{Audience: audience})
				return &id, nil, lbl, e
			}
			if lit, ok := v["literal"]; ok {
				s := asString(lit)
				return nil, &s, s, nil
			}
		}
		return nil, nil, "", nil
	}
	presence := req.effectivePresence()
	written := []any{}
	skipped := []any{}
	totalConflicts := 0
	for _, item := range items {
		relLabel := strings.TrimSpace(asString(item["x"]))
		if relLabel == "" {
			relLabel = strings.TrimSpace(asString(item["rel"]))
		}
		relID, rErr := m.Store.EnsureNode(effScope, "relation_type", relLabel, store.NodeOptions{Audience: audience})
		if rErr != nil {
			return nil, rErr
		}
		subjID, subjLit, subjLabel, sErr := resolveSlot(item["s"])
		if sErr != nil {
			return nil, sErr
		}
		if subjID == nil && subjLit == nil && truthy(item["subject_absent"]) {
			empty := ""
			subjLit = &empty
		}
		objID, objLit, objLabel, oErr := resolveSlot(item["o"])
		if oErr != nil {
			return nil, oErr
		}
		if objID == nil && objLit == nil {
			empty := ""
			objLit = &empty
		}
		anchorText := strings.TrimSpace(asString(item["anchor_text"]))
		if anchorText == "" {
			anchorText = strings.TrimSpace(stringValue(req.AnchorText))
		}
		if anchorText == "" {
			anchorText = content
		}
		start, end := 0, utf8.RuneCountInString(content)
		if idx := strings.Index(content, anchorText); idx >= 0 && anchorText != "" {
			start = utf8.RuneCountInString(content[:idx])
			end = start + utf8.RuneCountInString(anchorText)
		}
		warrant := defaultString(asString(item["warrant"]), "referential")
		conf := 0.8
		if c, ok := item["confidence"].(float64); ok && c > 0 {
			conf = c
		}
		tFrom := ids.ParseTime(item["t_valid_from"], ids.ParseTimeOptions{TimeRef: &occurredAt, Default: 0})
		var world, carrier, locus *string
		if w := strings.TrimSpace(asString(item["world"])); w != "" {
			world = &w
		} else if presence != nil && presence.World != "" {
			w := presence.World
			world = &w
		}
		if c := strings.TrimSpace(asString(item["carrier"])); c != "" {
			carrier = &c
		} else if presence != nil && presence.Carrier != "" {
			c := presence.Carrier
			carrier = &c
		}
		if l := strings.TrimSpace(asString(item["locus"])); l != "" {
			locus = &l
		} else if presence != nil && presence.Locus != "" {
			l := presence.Locus
			locus = &l
		}
		var leafLocusID string
		if locus != nil && *locus != "" {
			leafLocusID, _, _ = m.Store.EnsureDualTrackLocus(effScope, *locus, audience)
		}
		appended, aErr := m.Store.AppendAssertion(store.AppendAssertionRequest{
			Scope: effScope, RelID: relID, AnchorTraceHash: traceHash,
			AnchorStart: start, AnchorEnd: end,
			SubjectID: subjID, SubjectLiteral: subjLit,
			ObjectID: objID, ObjectLiteral: objLit,
			Bare:    truthy(item["bare"]),
			Warrant: warrant, Audience: audience, Confidence: conf,
			TValidFrom: tFrom, TSource: &occurredAt,
			World: world, Carrier: carrier, Locus: locus, CreatedBy: author,
		})
		if aErr != nil {
			return nil, aErr
		}
		newAID := asString(appended["id"])
		if locus != nil && *locus != "" && newAID != "" {
			_ = leafLocusID
			m.Store.ObserveSpatialEpisode(effScope, newAID, stringValue(world), stringValue(carrier), *locus, session, tFrom, occurredAt)
		}
		if truthy(item["closes_prior"]) && subjID != nil && *subjID != "" {
			stop := now
			if tFrom > 0 {
				stop = tFrom
			}
			var oldIDs []string
			if rows, qErr := m.Store.DB().Query(
				`SELECT node_id FROM assertion WHERE scope = ? AND subject_id = ? AND rel_id = ? AND node_id <> ? AND status = 'active'`,
				effScope, *subjID, relID, newAID,
			); qErr == nil {
				for rows.Next() {
					var oid string
					if rows.Scan(&oid) == nil && oid != "" {
						oldIDs = append(oldIDs, oid)
					}
				}
				rows.Close()
			}
			for _, oldID := range oldIDs {
				_, _ = m.Store.DB().Exec(`UPDATE assertion SET status = 'superseded', t_valid_to = ? WHERE node_id = ?`, stop, oldID)
				_, _ = m.Store.DB().Exec(
					`INSERT INTO meta_edge(scope, meta_kind, lhs_id, rhs_id, status, evidence_anchor, created_at, created_by) VALUES (?, 'supersedes', ?, ?, 'active', 'closes_prior', ?, 'remember')`,
					effScope, newAID, oldID, now,
				)
				_, _ = m.Store.DB().Exec(
					`UPDATE meta_edge SET status = 'reverted' WHERE scope = ? AND meta_kind = 'conflicts-with' AND status = 'proposed' AND (lhs_id = ? OR rhs_id = ?)`,
					effScope, oldID, oldID,
				)
				_, _ = m.Store.Event(effScope, "assert.closes_prior", map[string]any{"new": newAID, "old": oldID, "valid_to": stop}, "closes_prior", "remember")
			}
		}
		if subjID != nil && subjLabel != "" {
			_ = m.Store.AddMention(effScope, traceHash, subjLabel, start, end, subjID)
		}
		if objID != nil && objLabel != "" {
			_ = m.Store.AddMention(effScope, traceHash, objLabel, start, end, objID)
		}
		totalConflicts += asInt(appended["conflicts"])
		entry := map[string]any{"id": newAID, "s": subjLabel, "x": relLabel, "o": objLabel, "anchor": appended["anchor"]}
		if asString(appended["action"]) == "written" {
			written = append(written, entry)
		} else {
			skipped = append(skipped, entry)
		}
	}
	_ = m.Store.MarkProcessed(traceHash)
	_ = m.Store.UpdateReplayState(effScope, occurredAt, traceHash)
	_ = m.Store.UpdateSessionCursor(effScope, session, int64(asInt(req.Turn)))
	recall.DecisionCacheBumpScope(effScope)
	return map[string]any{
		"ok": true, "dry": false, "scope": effScope, "extractor": "structured",
		"written": written, "skipped": skipped, "supplemented": []any{}, "superseded": []any{},
		"conflicts": totalConflicts,
		"trace":     map[string]any{"hash": traceHash, "session": session, "inserted": traceInserted},
	}, nil
}

func defaultString(val, fallback string) string {
	if strings.TrimSpace(val) != "" {
		return strings.TrimSpace(val)
	}
	return fallback
}

// RecallRequest carries the read-side keyword arguments.
type RecallRequest struct {
	Cue         string
	Scope       *string
	Reader      *string
	Mode        string
	AsOf        any
	WindowStart any
	WindowEnd   any
	CapsBands   *int
	CapsNodes   *int
	CapsChars   *int
	// CapsChains / CapsRows bound delivery breadth: chains per band and rows per
	// chain. The engine already accepts these as request-level overrides; the cloud
	// `/open/memory` protocol carries them as optional `caps_chains` / `caps_rows`
	// fields. Unset/0 keeps the remote default (QA-shaped folding), so only
	// enumeration-shaped callers raise them.
	CapsChains *int
	CapsRows   *int
	// ResultUnit selects the delivery unit on `/open/memory/search`: ""/"band" (default
	// subject-band folding) or "item" (source-item ranked delivery). Unset/"" omits the
	// wire field so default callers remain byte-identical.
	ResultUnit string
	// Rank controls item-mode grouping on `/open/memory/search` (distinct from LLM `rerank`):
	// nil/true = grouped delivery (sxo -> unit -> parse -> block); false = pure relevance order without grouping.
	Rank *bool
	// AttachFiles controls whether original multimodal/file parts (`memory_part_parse.file`) are attached
	// on delivery (`attach_files` body field + `X-Rolling-Memory-Attach-Files` header).
	AttachFiles *bool
	Offset      int
	Depth       int
	IncludeRaw  *bool
	BudgetMS    *int
	RawLimit    int
	Session     *string
	// IncarnationID stamps the calling lifetime instance onto the L3 Noetic record this
	// recall crystallizes. Identity-only: it never narrows the search scope.
	IncarnationID string
	// NoCrystallize suppresses L3 Noetic auto-crystallization for passive or read-only
	// callers (such as harness hooks) where the query is an unverified user prompt rather
	// than an agent-initiated retrieval cue.
	NoCrystallize   bool
	Deep            bool
	TimeRef         any
	TokenCorrect    *bool
	AliasExpand     *bool
	RelativeTag     *bool
	MentionBackfill *bool
	PresenceLocus   string
	Presence        *text.PresenceCoordinates
	ReaderSubject   string
	SearchEngine    string
}

// Recall assembles one recall result, including the optional deep hint leg.
func (m *Memory) Recall(req RecallRequest) (*recall.Result, error) {
	if m.Config == nil {
		return nil, fmt.Errorf("recall requires a configured scope")
	}
	if err := m.checkLocalVaultScope(pickString(req.Scope, m.Config.Scope)); err != nil {
		return nil, err
	}
	if m.IsThinBrainProxy() {
		if req.Session != nil || req.AsOf != nil || req.WindowStart != nil || req.WindowEnd != nil {
			// Noetic and locally hydrated sources cannot enforce these filters.
			return nil, fmt.Errorf("pulse_plain and pulse_cipher recall cannot enforce session, asof or window filters across all sources")
		}
		return m.thinClientRecall(req)
	}
	if m.Store != nil && m.Store.HasLegacyGraph() && (req.Session != nil || req.AsOf != nil || req.WindowStart != nil || req.WindowEnd != nil) {
		// Graph supplementary traces can bypass the requested filters.
		return nil, fmt.Errorf("graph recall cannot enforce session, asof or window filters across all sources")
	}
	// A RAME database keeps only the `thin_vault_*` schema (PurgeLegacyTables retired the
	// assertion graph), so the legacy recall body below has no tables to read: serve the
	// recall from the local plaintext vault (FTS5 + slot matching) instead.
	if m.Store != nil && !m.Store.HasLegacyGraph() {
		return m.thinClientRecall(req)
	}
	now := clock.Unix()
	// The legacy graph's cue hints come from the deep LLM rewrite alone: the
	// public s/x/o hint slots were removed with the structured memory assertion contract (2026-11-06)
	// and no caller populates them anymore.
	hints := map[string]string{}
	deepMeta := map[string]any{"requested": req.Deep, "used": false}
	if req.Deep && strings.TrimSpace(req.Cue) != "" {
		provider, ok := m.LLM.(llm.HintsProvider)
		if m.LLM != nil && ok {
			llmHints, err := provider.QueryHints(req.Cue)
			if err != nil {
				deepMeta["error"] = err.Error()
			} else {
				for key, value := range llmHints {
					if _, exists := hints[key]; !exists {
						hints[key] = value
					}
				}
				deepMeta["used"] = len(llmHints) > 0
				deepMeta["hints"] = llmHints
			}
		} else {
			deepMeta["error"] = "no LLM configured or remote egress is blocked"
		}
	}
	scope := m.Config.Scope
	if req.Scope != nil && *req.Scope != "" {
		scope = *req.Scope
	}
	reader := m.Config.Reader
	if req.Reader != nil && *req.Reader != "" {
		reader = *req.Reader
	}
	mode := req.Mode
	if mode == "" {
		mode = "full"
	}
	timeRef := now
	result, err := recall.Recall(m.Store, m.Config, recall.Options{
		Cue: req.Cue, Scope: scope, Reader: reader, Mode: mode,
		AsOf:        ids.ParseTime(req.AsOf, ids.ParseTimeOptions{TimeRef: &now, Default: 0}),
		WindowStart: ids.ParseTime(req.WindowStart, ids.ParseTimeOptions{TimeRef: &now, Default: 0}),
		WindowEnd:   ids.ParseTime(req.WindowEnd, ids.ParseTimeOptions{TimeRef: &now, Default: 0}),
		CapsBands:   req.CapsBands, CapsNodes: req.CapsNodes, CapsChars: req.CapsChars,
		CapsChains: req.CapsChains, CapsRows: req.CapsRows,
		Offset: req.Offset, Depth: req.Depth, IncludeRaw: req.IncludeRaw,
		BudgetMS: req.BudgetMS, RawLimit: req.RawLimit, Session: req.Session,
		Structured: hints, DeepMeta: deepMeta,
		TimeRef:      ids.ParseTime(req.TimeRef, ids.ParseTimeOptions{TimeRef: &timeRef, Default: 0}),
		TokenCorrect: req.TokenCorrect, AliasExpand: req.AliasExpand,
		RelativeTag: req.RelativeTag, MentionBackfill: req.MentionBackfill,
		PresenceLocus: req.PresenceLocus, Presence: req.Presence,
		ReaderSubject: req.ReaderSubject, SearchEngine: req.SearchEngine,
		ResultUnit: req.ResultUnit, Rank: req.Rank, AttachFiles: req.AttachFiles,
		VectorWarning: m.vectorWarning(),
	})
	if err != nil {
		return nil, err
	}
	// Local mode: the legacy assertion graph may be empty for transcript imports,
	// while the thin plaintext vault has the indexed source text. Fall back to the
	// local thinclient recall (FTS5 + slot matching) instead of returning empty —
	// but never for explicit session or time filters, which the vault path does not
	// reconstruct.
	noFilters := req.Session == nil && req.AsOf == nil && req.WindowStart == nil && req.WindowEnd == nil
	if noFilters && m.Config != nil && strings.EqualFold(strings.TrimSpace(m.Config.Mode), "local") && len(result.Bands) == 0 {
		local, localErr := m.thinClientRecall(req)
		if localErr != nil {
			return nil, localErr
		}
		if local != nil && (len(local.Bands) > 0 || len(local.RawHits) > 0 || local.Gaps["source_unavailable"] > 0 || local.Gaps["index_incomplete"] > 0) {
			return local, nil
		}
	}
	// The soft vector leg may set a warning while recall runs; standard reads the
	// property after assembly, so refresh it before the caller renders/marshals.
	if vector, ok := result.Meta["vector"].(map[string]any); ok {
		vector["warning"] = m.vectorWarning()
	}
	return result, nil
}

// Recent lists the newest memory entries: legacy assertions when the assertion graph
// still exists, otherwise the time-ordered local vault view (identical to `recent`).
func (m *Memory) Recent(scope, reader *string, limit int) ([]map[string]any, error) {
	if limit == 0 {
		limit = 10
	}
	effectiveScope := pickString(scope, m.Config.Scope)
	if err := m.checkLocalVaultScope(effectiveScope); err != nil {
		return nil, err
	}
	// A RAME database has no `assertion` table at all (PurgeLegacyTables retired it),
	// so the newest-entries view must be served from the local plaintext vault.
	if m.Store != nil && !m.Store.HasLegacyGraph() {
		return m.recentVaultItems(limit)
	}
	if m.IsThinBrainProxy() && !strings.HasPrefix(effectiveScope, "mem:golden:") {
		return m.recentVaultItems(limit)
	}
	items, err := m.Store.Recent(effectiveScope, pickString(reader, m.Config.Reader), limit)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 && m.Config != nil && strings.EqualFold(strings.TrimSpace(m.Config.Mode), "local") {
		vaultItems, vErr := m.recentVaultItems(limit)
		if vErr != nil {
			return nil, vErr
		}
		if len(vaultItems) > 0 {
			return vaultItems, nil
		}
	}
	return items, nil
}

// Status returns store counters plus version/config/runtime warnings.
func (m *Memory) Status(scope string) (map[string]any, error) {
	result, err := m.Store.Status(scope)
	if err != nil {
		return nil, err
	}
	result["version"] = version.Version
	result["config"] = m.Config.PublicDict()
	if m.LLMWarning != nil {
		result["llm_warning"] = *m.LLMWarning
	} else {
		result["llm_warning"] = nil
	}
	result["vector_warning"] = m.vectorWarning()
	return result, nil
}

// Doctor runs the fixed eight-check environment report.
func (m *Memory) Doctor() (map[string]any, error) {
	checks := []any{}
	warnings := []any{}
	check := func(name string, ok bool, detail string) {
		checks = append(checks, map[string]any{"name": name, "ok": ok, "detail": detail})
	}
	dbPath := m.Config.DB
	_, statErr := os.Stat(dbPath)
	check("db_exists", statErr == nil, dbPath)
	check("db_writable", writable(filepath.Dir(dbPath)), filepath.Dir(dbPath))
	check("fts5", db.FTS5Available(), "trigram tokenizer required for CJK lexical search")
	check("json1", db.JSONExtractAvailable(), "needed by some inspection queries")
	check("config", true, m.Config.Path)
	check("llm", m.Config.LLM.Provider == "none" || m.LLM != nil, m.Config.LLM.Provider)
	check("embedding", true, "hash")
	check("sync", !m.Config.Cloud.Enabled || m.Config.Mode == "cloud", "cloud.sync requires cloud mode")
	// The deployment face only speaks when it has something to say: the fixed check
	// list is a published contract, while an image/deployment mismatch is an alarm.
	if deployOK, deployDetail := deploymentConsistency(); !deployOK {
		warnings = append(warnings, deployDetail)
	}
	if m.Config.Mode == "local" {
		if m.Config.LLM.Provider != "none" && !loopback(m.Config.LLM.BaseURL) {
			warnings = append(warnings, "local mode: remote LLM configured but blocked; deterministic rule extractor will be used")
		}
	}
	if m.Config.Cloud.Enabled && m.Config.Mode != "cloud" {
		warnings = append(warnings, "cloud sync is enabled in config but mode is local; sync commands will refuse")
	}
	if m.LLMWarning != nil {
		warnings = append(warnings, *m.LLMWarning)
	}
	if warning := m.vectorWarning(); warning != nil {
		warnings = append(warnings, warning)
	}
	integrity, err := m.Store.Check()
	if err != nil {
		return nil, err
	}
	sqliteVersion, err := db.SQLiteVersion(m.Store.DB())
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"version":   version.Version,
		"python":    "",
		"sqlite":    sqliteVersion,
		"checks":    checks,
		"warnings":  warnings,
		"integrity": integrity,
		"config":    m.Config.PublicDict(),
	}, nil
}

// deploymentConsistency compares the running image with the deployed path. Writing
// a new build over a running executable in place (cp instead of an atomic rename)
// leaves already-started processes wedged in dyld with no error surfaced anywhere,
// so this face makes the mismatch visible before it looks like a hang.
func deploymentConsistency() (bool, string) {
	self, err := os.Executable()
	if err != nil {
		return true, "self path unavailable"
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return true, "home unavailable"
	}
	deployed := filepath.Join(home, ".local", "bin", "rolling-memory")
	if sameFile(self, deployed) {
		return true, deployed
	}
	if _, statErr := os.Stat(deployed); statErr != nil {
		return true, "no deployed copy"
	}
	selfSum, err := fileSHA256(self)
	if err != nil {
		return true, "cannot read running image"
	}
	deployedSum, err := fileSHA256(deployed)
	if err != nil {
		return true, "cannot read " + deployed
	}
	if selfSum != deployedSum {
		return false, "running image differs from " + deployed + " (deploy with an atomic rename)"
	}
	return true, deployed
}

func sameFile(a, b string) bool {
	left, errA := os.Stat(a)
	right, errB := os.Stat(b)
	if errA != nil || errB != nil {
		return false
	}
	return os.SameFile(left, right)
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", digest.Sum(nil)), nil
}

// ImportFile imports a JSON export, or falls back to the transcript adapter
// for plain text transcripts.
func (m *Memory) ImportFile(path string, scope, session *string, audience string, replay bool) (map[string]any, error) {
	effectiveScope := pickString(scope, m.Config.Scope)
	if err := m.checkLocalVaultScope(effectiveScope); err != nil {
		return nil, err
	}
	if m.Config != nil && strings.EqualFold(strings.TrimSpace(m.Config.Mode), "local") {
		if err := m.bindLocalVaultScope(); err != nil {
			return nil, err
		}
	}
	target := expandUser(path)
	drainPending := func(capCount int) (map[string]any, error) {
		limit := m.Config.Hooks.MaxTraces
		if capCount > 0 {
			limit = capCount
		}
		traces, err := m.Store.PendingTraces(effectiveScope, limit, session)
		if err != nil {
			return nil, err
		}
		for _, tr := range traces {
			_ = m.Store.MarkProcessed(tr.Hash)
			_ = m.Store.UpdateReplayState(effectiveScope, tr.RecordedAt, tr.Hash)
			_ = m.Store.UpdateSessionCursor(effectiveScope, tr.Session, tr.Turn)
		}
		return map[string]any{
			"ok": true, "scope": effectiveScope, "processed": len(traces),
			"written": 0, "skipped": 0, "anchor_failed": 0, "resolve_failed": 0,
			"llm_calls": 0, "warnings": []any{},
		}, nil
	}
	if strings.EqualFold(filepath.Ext(target), ".json") {
		raw, err := os.ReadFile(target)
		if err == nil {
			payload := map[string]any{}
			if err := json.Unmarshal(raw, &payload); err == nil {
				if asString(payload["format"]) == version.ExportFormat {
					imported, err := m.ImportData(payload, &effectiveScope)
					if err != nil {
						return nil, err
					}
					if err := m.rememberImportedTracesToVault(effectiveScope, session); err != nil {
						return nil, err
					}
					if replay {
						rep, err := drainPending(0)
						if err != nil {
							return nil, err
						}
						imported["replay"] = rep
					}
					return imported, nil
				}
			}
		}
	}
	result, err := transcript.ImportTranscript(m.Store, target, effectiveScope, session,
		audience, 0, transcript.DefaultImportLines)
	if err != nil {
		return nil, err
	}
	if err := m.rememberImportedTracesToVault(effectiveScope, nil); err != nil {
		return nil, err
	}
	if replay {
		rep, err := drainPending(asInt(result["messages"]))
		if err != nil {
			return nil, err
		}
		result["replay"] = rep
	}
	return result, nil
}

// ExportUnavailable reports why the legacy JSON export cannot serve as a backup.
func ExportUnavailable() error {
	return fmt.Errorf("JSON export is unavailable: it omits the local vault and referenced source files and is not a complete backup; existing exports remain available for import into a compatible database")
}

// Export refuses to produce an incomplete backup.
func (m *Memory) Export(path, scope *string) (map[string]any, error) {
	return nil, ExportUnavailable()
}

// ImportData imports one export payload into scope.
func (m *Memory) ImportData(payload map[string]any, scope *string) (map[string]any, error) {
	if asString(payload["format"]) != version.ExportFormat {
		return nil, fmt.Errorf("not a rolling-memory export")
	}
	effectiveScope := m.Config.Scope
	if scope != nil && *scope != "" {
		effectiveScope = *scope
	} else if value := asString(payload["scope"]); value != "" {
		effectiveScope = value
	}
	if err := m.checkLocalVaultScope(effectiveScope); err != nil {
		return nil, err
	}
	inserted, err := m.Store.ImportScope(effectiveScope, payload)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "scope": effectiveScope, "inserted": inserted}, nil
}

// Reindex rebuilds FTS and refreshes non-assertion vectors.
func (m *Memory) Reindex() (map[string]any, error) {
	fts, err := db.RebuildFTS(m.Store.DB())
	if err != nil {
		return nil, err
	}
	rows, err := m.Store.DB().Query(
		"SELECT id FROM node WHERE kind != 'assertion' AND status IN ('provisional','active') ORDER BY scope, kind, norm_label")
	if err != nil {
		return nil, err
	}
	nodeIDs := []string{}
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			rows.Close()
			return nil, err
		}
		nodeIDs = append(nodeIDs, value)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	return map[string]any{
		"ok": true, "fts": fts, "vectors": 0, "warning": m.vectorWarning(),
	}, nil
}

// Check runs the integrity/audit report.
func (m *Memory) Check() (map[string]any, error) {
	return m.Store.Check()
}

// Efficacy evaluates recall adoption rate, silence precision, round-trip savings,
// and net token ROI across all connected coding harnesses. budgetSeconds > 0 bounds the
// per-run settling cost so the evaluation always finalizes and persists a snapshot
// (partial ones are flagged) instead of being killed mid-flight by its caller.
func (m *Memory) Efficacy(scope *string, harnessID string, limit, budgetSeconds int) (*efficacy.Report, error) {
	effectiveScope := pickString(scope, m.Config.Scope)
	if m.Store == nil || m.Store.DB() == nil {
		return nil, fmt.Errorf("efficacy: no memory database to evaluate")
	}
	// Per-turn settlement formerly relied on retired trace/assertion tables.
	// Current evaluation (efficacy.Evaluate) now aggregates from the .efficacy.sqlite
	// fact ledger directly, which acts as the canonical source for recall provenance.
	return efficacy.Evaluate(m.Store, m.Config, efficacy.Options{
		Scope:         effectiveScope,
		Harness:       harnessID,
		Limit:         limit,
		BudgetSeconds: budgetSeconds,
	})
}

// ------------------------------------------------------------------ helpers

func (m *Memory) vectorWarning() any {
	if m == nil || m.Store == nil {
		return nil
	}
	return m.Store.Warning()
}

func pickString(value *string, fallback string) string {
	if value != nil && *value != "" {
		return *value
	}
	return fallback
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func int64Ptr(value any) *int64 {
	if value == nil {
		return nil
	}
	var result int64
	switch typed := value.(type) {
	case int:
		result = int64(typed)
	case int64:
		result = typed
	case float64:
		result = int64(typed)
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		if err != nil {
			return nil
		}
		result = parsed
	default:
		return nil
	}
	return &result
}

func writable(directory string) bool {
	file, err := os.CreateTemp(directory, ".rm-write-check-*")
	if err != nil {
		return false
	}
	name := file.Name()
	file.Close()
	_ = os.Remove(name)
	return true
}

func loopback(rawURL string) bool {
	host, ok := ids.URLHost(rawURL)
	return ok && ids.IsLoopbackHost(host)
}

func expandUser(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			if path == "~" {
				return home
			}
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

func collectionLength(value any) (int, bool) {
	switch typed := value.(type) {
	case []map[string]any:
		return len(typed), true
	case []any:
		return len(typed), true
	}
	return 0, false
}

func asSlice(value any) []any {
	switch typed := value.(type) {
	case []any:
		return typed
	case []map[string]any:
		result := make([]any, 0, len(typed))
		for _, item := range typed {
			result = append(result, item)
		}
		return result
	}
	return []any{}
}

func asInt(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case bool:
		if typed {
			return 1
		}
		return 0
	}
	return 0
}

func asString(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case bool:
		if typed {
			return "True"
		}
		return "False"
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case float64:
		return strconv.FormatFloat(typed, 'g', -1, 64)
	}
	return fmt.Sprint(value)
}

func truthy(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case string:
		return typed != ""
	case int:
		return typed != 0
	case int64:
		return typed != 0
	case float64:
		return typed != 0
	}
	return value != nil
}

func mergeMap(base, extra map[string]any) map[string]any {
	result := map[string]any{}
	for key, value := range base {
		result[key] = value
	}
	for key, value := range extra {
		result[key] = value
	}
	return result
}

func firstRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}
