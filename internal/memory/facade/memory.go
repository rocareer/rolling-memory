// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

// Package facade is the in-process memory surface used by the CLI, MCP server and
// hooks. It wires config -> store -> remember/recall and exposes the governed
// read/write surface without adding policy of its own.
package facade

import (
	"bufio"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rocareer/rolling-memory/internal/clock"
	"github.com/rocareer/rolling-memory/internal/config"
	"github.com/rocareer/rolling-memory/internal/db"
	"github.com/rocareer/rolling-memory/internal/efficacy"
	"github.com/rocareer/rolling-memory/internal/episodic"
	"github.com/rocareer/rolling-memory/internal/episodicunit"
	"github.com/rocareer/rolling-memory/internal/memory/localvault"
	"github.com/rocareer/rolling-memory/internal/memory/recall"
	"github.com/rocareer/rolling-memory/internal/store"
	"github.com/rocareer/rolling-memory/internal/text"
	"github.com/rocareer/rolling-memory/internal/version"
)

// Memory is one open scope-bound facade.
type Memory struct {
	Config *config.Config
	Store  *store.Store

	localVaultOnce sync.Once
	localVaultMu   sync.RWMutex
	localVault     *localvault.Vault
	localVaultErr  error
	// coldBuildMemoryLimit is the soft memory limit the local vault may apply
	// while a cold full-corpus build runs (0 keeps the runtime limit as is).
	coldBuildMemoryLimit atomic.Int64
	// snapshotOnly marks a long-lived recall surface: it may open a published
	// disk cluster but must never run the full-corpus build on the query path.
	snapshotOnly atomic.Bool
}

// SetColdBuildMemoryLimit forwards the caller's cold-build memory window to the
// local vault, so a missing .zcsr disk cluster cannot degrade a daemon start
// into GC thrash under its steady-state cap. It must run before the vault is
// first opened (the daemon sets it before serving); LocalVault applies the
// stored window when it constructs the vault.
func (m *Memory) SetColdBuildMemoryLimit(bytes int64) {
	if m == nil {
		return
	}
	if bytes < 0 {
		bytes = 0
	}
	m.coldBuildMemoryLimit.Store(bytes)
}

// SetSnapshotOnly marks this memory as a read-only recall surface. It must be
// applied before LocalVault opens the vault so the build policy is in place
// for the first query.
func (m *Memory) SetSnapshotOnly(only bool) {
	if m == nil {
		return
	}
	m.snapshotOnly.Store(only)
}

// SnapshotOnly reports whether this memory refuses on-demand full builds.
func (m *Memory) SnapshotOnly() bool {
	if m == nil {
		return false
	}
	return m.snapshotOnly.Load()
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
	return &Memory{Config: cfg, Store: database}, nil
}

// Close closes the store (the usb profile checkpoints the WAL first).
func (m *Memory) Close() error {
	if m == nil {
		return nil
	}
	m.localVaultMu.Lock()
	defer m.localVaultMu.Unlock()
	if m.localVault != nil {
		m.localVault.CloseRuntimeSnapshots()
	}
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

// Remember writes one memory unit to the local vault.
func (m *Memory) Remember(req RememberRequest) (map[string]any, error) {
	return m.localVaultRemember(req)
}

// RecallRequest carries the read-side keyword arguments.
type RecallRequest struct {
	Cue            string
	Scope          *string
	Reader         *string
	Mode           string
	CapsBands      *int
	CapsNodes      *int
	CapsChars      *int
	DeliveryWindow string
	Offset         int
	IncludeRaw     *bool
	BudgetMS       *int
	RawLimit       int
	// IncarnationID stamps the calling lifetime instance onto the L3 Noetic record this
	// recall crystallizes. Identity-only: it never narrows the search scope.
	IncarnationID string
	// NoCrystallize suppresses L3 Noetic auto-crystallization for passive or read-only
	// callers (such as harness hooks) where the query is an unverified user prompt rather
	// than an agent-initiated retrieval cue.
	NoCrystallize bool
	TimeRef       any
	PresenceLocus string
	Presence      *text.PresenceCoordinates
}

// Recall assembles one recall result from the local vault.
func (m *Memory) Recall(req RecallRequest) (*recall.Result, error) {
	if m.Config == nil {
		return nil, fmt.Errorf("recall requires a configured scope")
	}
	scope := pickString(req.Scope, m.Config.Scope)
	if err := m.checkLocalVaultScope(scope); err != nil {
		return nil, err
	}
	return m.localVaultRecall(req)
}

// Recent lists the newest local-vault entries in time order.
func (m *Memory) Recent(scope, reader *string, limit int) ([]map[string]any, error) {
	if limit == 0 {
		limit = 10
	}
	effectiveScope := pickString(scope, m.Config.Scope)
	if err := m.checkLocalVaultScope(effectiveScope); err != nil {
		return nil, err
	}
	return m.recentVaultItems(limit)
}

// Status returns the canonical corpus counters plus version and config.
func (m *Memory) Status(scope string) (map[string]any, error) {
	result, err := m.Store.Status(scope)
	if err != nil {
		return nil, err
	}
	result["version"] = version.Version
	result["config"] = m.Config.PublicDict()
	result["snapshot"] = m.snapshotStatus()
	// Mounted directories carry their own ingestion clock, and a stalled mount is invisible
	// in corpus counts alone; status is where an operator looks before asking why a file is
	// not recalled.
	if states, err := localvault.ReadBackgroundSyncStates(m.Store.DB()); err == nil && len(states) > 0 {
		result["background"] = backgroundFreshnessPayload(states, clock.Unix())
	}
	return result, nil
}

// snapshotStatus reports the published cluster and the build gap without
// opening a vault, so a status probe never materialises the corpus.
func (m *Memory) snapshotStatus() map[string]any {
	status := map[string]any{
		"ready":             false,
		"docs_rev":          int64(0),
		"docs_cnt":          int64(0),
		"change_seq":        int64(0),
		"purge_epoch":       int64(0),
		"built_docs_rev":    int64(0),
		"built_change_seq":  int64(0),
		"built_purge_epoch": int64(0),
		"gap":               int64(0),
		"gap_known":         false,
		"change_gap":        int64(0),
		"change_gap_known":  false,
		"purge_blocked":     false,
	}
	if m == nil || m.Store == nil || m.Store.DB() == nil {
		return status
	}
	var docsRev, docsCnt, changeSeq, purgeEpoch int64
	if err := m.Store.DB().QueryRow(`SELECT docs_rev, docs_cnt, change_seq, purge_epoch FROM rm_revision WHERE id = 1`).Scan(&docsRev, &docsCnt, &changeSeq, &purgeEpoch); err == nil {
		status["docs_rev"] = docsRev
		status["docs_cnt"] = docsCnt
		status["change_seq"] = changeSeq
		status["purge_epoch"] = purgeEpoch
	}
	builtPurgeEpoch := int64(0)
	builtPurgeKnown := false
	var raw []byte
	if err := m.Store.DB().QueryRow(`SELECT v FROM rm_meta WHERE k = ?`, "snapshot_built_purge_epoch").Scan(&raw); err == nil {
		if built, parseErr := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64); parseErr == nil {
			builtPurgeEpoch = built
			builtPurgeKnown = true
		}
	}
	status["built_purge_epoch"] = builtPurgeEpoch
	status["purge_blocked"] = purgeEpoch > 0 && (!builtPurgeKnown || builtPurgeEpoch != purgeEpoch)
	if err := m.Store.DB().QueryRow(`SELECT v FROM rm_meta WHERE k = ?`, "snapshot_built_docs_rev").Scan(&raw); err == nil {
		if built, parseErr := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64); parseErr == nil {
			status["built_docs_rev"] = built
			status["gap_known"] = true
			gap := docsRev - built
			if gap < 0 {
				gap = 0
			}
			status["gap"] = gap
		}
	}
	if err := m.Store.DB().QueryRow(`SELECT v FROM rm_meta WHERE k = ?`, "snapshot_built_change_seq").Scan(&raw); err == nil {
		if built, parseErr := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64); parseErr == nil {
			status["built_change_seq"] = built
			status["change_gap_known"] = true
			gap := changeSeq - built
			if gap < 0 {
				gap = 0
			}
			status["change_gap"] = gap
		}
	}
	if docsCnt == 0 {
		status["ready"] = true
		return status
	}
	status["ready"] = localvault.PublishedSnapshotExists(m.Config.DB)
	return status
}

// snapshotBuilderRecent reports whether the background builder completed a run
// without failures inside a window that covers a few publication cadences.
func snapshotBuilderRecent(connection *sql.DB, cfg *config.Config) bool {
	if connection == nil {
		return false
	}
	var raw []byte
	if err := connection.QueryRow(`SELECT v FROM rm_meta WHERE k = ?`, "snapshot_build_status").Scan(&raw); err != nil {
		return false
	}
	var status struct {
		At       int64 `json:"at"`
		Failures int   `json:"failures"`
		Blocked  bool  `json:"blocked"`
	}
	if json.Unmarshal(raw, &status) != nil || status.Blocked || status.Failures != 0 || status.At <= 0 {
		return false
	}
	window := 3 * config.DefaultSnapshotInterval
	if cfg != nil {
		if interval := cfg.Daemon.EffectiveSnapshotInterval(); interval > 0 {
			window = 3 * interval
		}
	}
	if window < 2*time.Minute {
		window = 2 * time.Minute
	}
	return time.Since(time.Unix(status.At, 0)) <= window
}

// Doctor runs the fixed environment and integrity report.
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
	check("config", true, m.Config.Path)
	check("json1", db.JSONExtractAvailable(), "required by transcript inspection queries")
	corpus, err := db.ReadCorpusCounts(m.Store.DB())
	if err != nil {
		return nil, err
	}
	terms, err := db.CountCorpusTerms(m.Store.DB())
	if err != nil {
		return nil, err
	}
	check("corpus", true, fmt.Sprintf("%d memory units · %d term postings", corpus.MemoryUnits(), terms))
	// The revision counters are the O(1) freshness identity behind the snapshot
	// signature and the delta gate; drift means a writer bypassed the delete side
	// (for example a REPLACE rewrite that no longer compensates the counter).
	var counterDocs, counterTerms int64
	if err := m.Store.DB().QueryRow(`SELECT docs_cnt, terms_cnt FROM rm_revision WHERE id = 1`).Scan(&counterDocs, &counterTerms); err == nil {
		liveDocs := int64(corpus.Episodic + corpus.Background)
		liveTerms := int64(terms)
		check("corpus_counters", counterDocs == liveDocs && counterTerms == liveTerms,
			fmt.Sprintf("docs %d/%d · terms %d/%d", counterDocs, liveDocs, counterTerms, liveTerms))
		if counterDocs != liveDocs || counterTerms != liveTerms {
			warnings = append(warnings, "corpus counters drifted from the live row counts; the snapshot signature and delta gate will keep missing")
		}
	}
	// A collapsed event timeline is not a correctness problem, but it multiplies
	// storage: the cluster pipeline pairs units that share a spacetime bucket.
	if concentration, err := db.ReadTimeConcentration(m.Store.DB()); err == nil && concentration.Docs > 0 {
		check("event_time", !concentration.Collapsed(), fmt.Sprintf(
			"%d units · %d distinct instants · largest group %d · %.0f same-instant peers per unit",
			concentration.Docs, concentration.DistinctInstants, concentration.LargestGroup, concentration.PairsPerDoc))
		if concentration.Collapsed() {
			warnings = append(warnings, "most units share their occurred_at second; the cluster pipeline pairs units inside a bucket, so the snapshot grows with that multiplier — ingest with real event times")
		}
	}
	var docsCnt, changeSeq int64
	_ = m.Store.DB().QueryRow(`SELECT docs_cnt, change_seq FROM rm_revision WHERE id = 1`).Scan(&docsCnt, &changeSeq)
	snapshotOK := docsCnt == 0 || localvault.PublishedSnapshotUsable(m.Store.DB(), m.Config.DB)
	check("snapshot_cluster", snapshotOK, m.Config.DB+".snapshots")
	if docsCnt > 0 {
		if docsPath, ok := localvault.PublishedSnapshotDocsPath(m.Config.DB); ok {
			if _, statErr := os.Stat(docsPath); statErr != nil {
				warnings = append(warnings, "snapshot docs sidecar missing; recall falls back to the canonical scan")
			} else if !snapshotOK {
				warnings = append(warnings, "the published snapshot cluster cannot be opened by this build; the background builder will republish it")
			}
		} else {
			warnings = append(warnings, "no published snapshot cluster; recall serves warming until the builder runs")
		}
	}
	var changeGap int64
	changeGapKnown := false
	var rawGap []byte
	if err := m.Store.DB().QueryRow(`SELECT v FROM rm_meta WHERE k = ?`, "snapshot_built_change_seq").Scan(&rawGap); err == nil {
		if built, parseErr := strconv.ParseInt(strings.TrimSpace(string(rawGap)), 10, 64); parseErr == nil {
			changeGap = changeSeq - built
			if changeGap < 0 {
				changeGap = 0
			}
			changeGapKnown = true
		}
	}
	changeOK := docsCnt == 0 || (changeGapKnown && changeGap < 512)
	if !changeOK && changeGapKnown && snapshotBuilderRecent(m.Store.DB(), m.Config) {
		// A burst of writes (a background mount pass, a transcript backfill) can
		// outrun the one-minute publication cadence without stalling the builder.
		// A recent successful build means the gap is backlog, not a dead loop.
		changeOK = true
	}
	check("snapshot_change_gap", changeOK, fmt.Sprintf("change_seq=%d gap=%d known=%v", changeSeq, changeGap, changeGapKnown))
	// The deployment face only speaks when it has something to say: the fixed check
	// list is a published contract, while an image/deployment mismatch is an alarm.
	if deployOK, deployDetail := deploymentConsistency(); !deployOK {
		warnings = append(warnings, deployDetail)
	}
	backgroundStates, bgErr := localvault.ReadBackgroundSyncStates(m.Store.DB())
	if bgErr == nil {
		neverSynced, failed := 0, 0
		for _, st := range backgroundStates {
			if st.LastError != "" {
				failed++
			}
			if st.LastSuccessAt == 0 {
				neverSynced++
			}
		}
		detail := "no mounted directories"
		if len(backgroundStates) > 0 {
			detail = fmt.Sprintf("%d mount(s) · %d never synced · %d last pass failed", len(backgroundStates), neverSynced, failed)
		}
		// A failed pass is an alarm; a mount that has not synced yet is expected on the
		// first run and only stated in the detail.
		check("background_freshness", failed == 0, detail)
		if failed > 0 {
			warnings = append(warnings, fmt.Sprintf("%d background mount(s) failed their last sync; recall serves rows from the last successful pass", failed))
		}
	}
	integrity, err := m.Store.Check()
	if err != nil {
		return nil, err
	}
	sqliteVersion, err := db.SQLiteVersion(m.Store.DB())
	if err != nil {
		return nil, err
	}
	result := map[string]any{
		"version":   version.Version,
		"python":    "",
		"sqlite":    sqliteVersion,
		"checks":    checks,
		"warnings":  warnings,
		"integrity": integrity,
		"config":    m.Config.PublicDict(),
	}
	if bgErr == nil && len(backgroundStates) > 0 {
		result["background"] = backgroundStates
	}
	return result, nil
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

// ImportFile imports a JSONL transcript through the canonical pipeline shared
// with the background scanner. Other formats are refused.
func (m *Memory) ImportFile(path string, scope, session *string, audience string, replay bool) (map[string]any, error) {
	if audience != "" && audience != "shared" && audience != "public" {
		return nil, fmt.Errorf("restricted or unknown audience file import is unavailable: background transcript scanning cannot enforce file privacy; keep sensitive files outside scanned directories until source-level access controls are supported")
	}
	effectiveScope := pickString(scope, m.Config.Scope)
	if err := m.checkLocalVaultScope(effectiveScope); err != nil {
		return nil, err
	}
	target := config.ExpandUserPath(path)
	if m.Config != nil && strings.EqualFold(strings.TrimSpace(m.Config.Mode), "local") {
		if err := m.bindLocalVaultScope(); err != nil {
			return nil, err
		}
	}
	if !canonicalJSONLTranscript(target) {
		return nil, fmt.Errorf("import supports JSONL transcripts only: %s", filepath.Base(target))
	}
	return m.importCanonicalJSONL(target, effectiveScope, session, replay)
}

// canonicalJSONLTranscript reports whether the path carries a JSONL transcript
// extension the canonical (scanner-identical) ingest pipeline owns: a plain
// transcript, a zstd-compressed one, or a bare zstd container of one. JSON
// exports and plain-text transcripts keep their own readers.
func canonicalJSONLTranscript(path string) bool {
	lower := strings.ToLower(strings.TrimSpace(path))
	for _, suffix := range []string{".jsonl", ".jsonl.zst", ".jsonl.zstd", ".zst", ".zstd"} {
		if strings.HasSuffix(lower, suffix) {
			return true
		}
	}
	return false
}

// countJSONLLines counts the physical lines of one transcript (decompressed when
// zstd-compressed) for the receipt's offset/total_lines fields.
func countJSONLLines(path string) (int, error) {
	file, err := episodic.OpenJSONL(path)
	if err != nil {
		return 0, err
	}
	defer file.Close()
	reader := bufio.NewReader(file)
	lines := 0
	lastByte := byte('\n')
	buffer := make([]byte, 32*1024)
	for {
		n, readErr := reader.Read(buffer)
		for _, value := range buffer[:n] {
			if value == '\n' {
				lines++
			}
		}
		if n > 0 {
			lastByte = buffer[n-1]
		}
		if readErr == nil {
			continue
		}
		if !errors.Is(readErr, io.EOF) {
			return 0, readErr
		}
		break
	}
	if lastByte != '\n' {
		lines++
	}
	return lines, nil
}

// importCanonicalJSONL ingests one JSONL transcript through the same readers and
// unit derivation the background scanner uses: completed turns only (the same
// explicit completion evidence), one `@ref:jsonl:<abs>:<idx>#<k>` pointer per
// segment, and the same content-addressed document ids — so a hand-imported file
// and the same file captured by the scanner converge on one set of documents
// instead of two identities. No trace rows are queued for the JSONL path and the
// trace mirror is skipped; file imports establish no process persona, so identity
// and locus stay empty here (a later scan stamps the harness coordinate).
//
// One frozen parse of the file drives the whole import: every unit's pointer is
// verified against it in O(1) instead of re-reading and re-decompressing the
// transcript per unit. A parse that fails (oversize, corrupt) keeps the streaming
// reader unchanged; a source that changes while the import runs is reported at the
// end, with the units written so far (one transaction per unit) left in place.
func (m *Memory) importCanonicalJSONL(target, scope string, session *string, replay bool) (map[string]any, error) {
	tc, err := m.LocalVault()
	if err != nil {
		return nil, err
	}
	if tc == nil || tc.Vault == nil {
		return nil, fmt.Errorf("jsonl import requires a local vault")
	}
	vault := tc.Vault
	vault.SetIdentity(localvault.VaultIdentity{})
	snapshot, snapshotErr := episodic.NewSourceSnapshotContext(context.Background(), target)
	if snapshotErr != nil {
		snapshot = nil
	}
	messages, inserted, skippedIncomplete := 0, 0, 0
	saveTurnUnits := func(turnIndex int, segments []episodic.Segment, completed bool) error {
		if !completed {
			skippedIncomplete++
			return nil
		}
		prefix := fmt.Sprintf("@ref:jsonl:%s:%d", target, turnIndex)
		for _, segment := range segments {
			occurredAt := segment.OccurredAt
			if occurredAt > 1e11 {
				occurredAt /= 1000
			}
			if occurredAt < 0 {
				occurredAt = 0
			}
			for _, unit := range episodicunit.SegmentUnits(scope, prefix, segment, occurredAt, true) {
				var exists int
				if err := m.Store.DB().QueryRow(`SELECT EXISTS(SELECT 1 FROM rm_docs WHERE unit_id = ?)`, unit.DocID).Scan(&exists); err != nil {
					return err
				}
				if err := vault.SaveLocalEpisodicPointerIndexedVerified(scope, snapshot, unit.DocID, occurredAt, unit.RefPointer, unit.TurnRef, unit.Text, localvault.IngestResponse{}); err != nil {
					if errors.Is(err, localvault.ErrEpisodicUnitDeleted) {
						// A tombstoned unit was deleted by user intent: a manual
						// import must not resurrect it.
						continue
					}
					return fmt.Errorf("jsonl import %s: %w", unit.DocID, err)
				}
				messages++
				if exists == 0 {
					inserted++
				}
			}
		}
		return nil
	}
	if snapshot != nil {
		defer snapshot.Close()
		for turn := 0; turn < snapshot.Turns(); turn++ {
			segments, ok := snapshot.Segments(turn)
			if !ok {
				continue
			}
			if err := saveTurnUnits(turn, segments, snapshot.Completed(turn)); err != nil {
				return nil, err
			}
		}
		if snapshot.Stale() {
			return nil, fmt.Errorf("jsonl import %s: transcript changed during import; retry the import", target)
		}
	} else {
		err = episodic.VisitJSONLTurns(context.Background(), target, func(turn episodic.JSONLTurn, completed bool) error {
			return saveTurnUnits(turn.Index, turn.Segments, completed)
		})
		if err != nil {
			return nil, err
		}
	}
	totalLines, err := countJSONLLines(target)
	if err != nil {
		return nil, err
	}
	size := int64(0)
	if info, statErr := os.Stat(target); statErr == nil {
		size = info.Size()
	}
	sessionName := sessionStem(target)
	if session != nil && *session != "" {
		sessionName = *session
	}
	receipt := map[string]any{
		"ok": true, "path": target, "session": sessionName,
		"messages": messages, "inserted": inserted,
		"offset": totalLines, "total_lines": totalLines, "size": size,
		"skipped_incomplete_tail": skippedIncomplete,
	}
	if replay {
		// The canonical pipeline queues no traces, so replay has nothing of its
		// own to drain; the block keeps its shape at zero counts.
		receipt["replay"] = map[string]any{
			"ok": true, "scope": scope, "processed": 0, "written": 0, "skipped": 0,
			"anchor_failed": 0, "resolve_failed": 0, "llm_calls": 0, "warnings": []any{},
		}
	}
	return receipt, nil
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
	// Evaluation aggregates from the .efficacy.sqlite fact ledger plus vault
	// corpus counters; no retired trace/assertion replay is involved.
	return efficacy.Evaluate(m.Store.DB(), m.Config, efficacy.Options{
		Scope:         effectiveScope,
		Harness:       harnessID,
		Limit:         limit,
		BudgetSeconds: budgetSeconds,
	})
}

// ------------------------------------------------------------------ helpers

func pickString(value *string, fallback string) string {
	if value != nil && *value != "" {
		return *value
	}
	return fallback
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

// sessionStem mirrors the transcript file stem used as the default import
// session: the last suffix is dropped, dotfiles keep their name.
func sessionStem(path string) string {
	base := filepath.Base(path)
	if index := strings.LastIndex(base, "."); index > 0 {
		base = base[:index]
	}
	return base
}
