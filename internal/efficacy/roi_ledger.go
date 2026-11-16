// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package efficacy

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/rocareer/rolling-memory/internal/config"
	"github.com/rocareer/rolling-memory/internal/ids"
	"github.com/rocareer/rolling-memory/internal/store"
)

const roiLedgerSnapshotVersion = 3

// SourceWatermark captures the monotonic state of the memory database and hook log
// so read requests never re-run full evaluation when no new turns or assertions arrived.
type SourceWatermark struct {
	TraceCount     int   `json:"trace_count"`
	MaxTraceAt     int64 `json:"max_trace_at"`
	AssertionCount int   `json:"assertion_count"`
	MaxAssertionAt int64 `json:"max_assertion_at"`
	LogSize        int64 `json:"log_size"`
	Settled        bool  `json:"settled"`
}

// ROILedgerSnapshot persists the incrementally maintained ROI ledger report and all
// evaluated turn records on disk so CLI and Admin reads complete in milliseconds.
type ROILedgerSnapshot struct {
	Version       int                 `json:"version"`
	Scope         string              `json:"scope"`
	DBFingerprint string              `json:"db_fingerprint"`
	UpdatedAt     string              `json:"updated_at"`
	UpdatedUnix   int64               `json:"updated_unix"`
	Watermark     SourceWatermark     `json:"watermark"`
	FullSessions  []SessionEvaluation `json:"full_sessions"`
	Report        Report              `json:"report"`
}

var scopeSlugRe = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)

// ComputeDBFingerprint computes a read-only identity fingerprint for a memory database
// without writing to or mutating the database (preserving golden seals). The scope is
// not part of the fingerprint: one database carries many scopes, and the ledger holds
// events for all of them. The formula is shared with the admin console reader.
func ComputeDBFingerprint(db *sql.DB, scope string) string {
	if db == nil {
		return ""
	}
	var createdAt, engine string
	_ = db.QueryRow(`SELECT coalesce(value, '') FROM db_meta WHERE key = 'created_at'`).Scan(&createdAt)
	_ = db.QueryRow(`SELECT coalesce(value, '') FROM db_meta WHERE key = 'engine'`).Scan(&engine)
	var firstDocID string
	_ = db.QueryRow(`SELECT coalesce(doc_id, '') FROM thin_vault_docs ORDER BY occurred_at ASC, rowid ASC LIMIT 1`).Scan(&firstDocID)
	if strings.TrimSpace(firstDocID) == "" {
		// A vault that has recalled nothing yet still needs a stable identity, so
		// the oldest crystallized unit anchors the fingerprint instead.
		_ = db.QueryRow(`SELECT coalesce(noetic_id, '') FROM thin_vault_noetic ORDER BY occurred_at ASC, rowid ASC LIMIT 1`).Scan(&firstDocID)
	}
	if strings.TrimSpace(firstDocID) == "" {
		// Vaults written before the thin-vault era anchor on their oldest trace or
		// node. The queries fail harmlessly when those tables are absent.
		_ = db.QueryRow(`SELECT coalesce(hash, '') FROM trace ORDER BY recorded_at ASC, rowid ASC LIMIT 1`).Scan(&firstDocID)
		if strings.TrimSpace(firstDocID) == "" {
			_ = db.QueryRow(`SELECT coalesce(id, '') FROM node ORDER BY created_at ASC, rowid ASC LIMIT 1`).Scan(&firstDocID)
		}
	}
	if strings.TrimSpace(createdAt) == "" || strings.TrimSpace(firstDocID) == "" {
		return ""
	}
	raw := strings.Join([]string{
		strings.TrimSpace(createdAt),
		strings.TrimSpace(engine),
		strings.TrimSpace(firstDocID),
	}, "|")
	return ids.SHA256Hex(raw)[:24]
}

// QuerySourceWatermark performs lightweight indexed queries (<1ms) to inspect the current
// database and log watermark for a given scope.
func QuerySourceWatermark(st *store.Store, scope, logPath string) SourceWatermark {
	var wm SourceWatermark
	if st != nil && st.DB() != nil {
		_ = st.DB().QueryRow(
			`SELECT COUNT(*), COALESCE(MAX(recorded_at), 0) FROM trace WHERE scope = ?`,
			scope,
		).Scan(&wm.TraceCount, &wm.MaxTraceAt)
		var totalAss, activeAss int
		_ = st.DB().QueryRow(
			`SELECT COUNT(*), COALESCE(SUM(CASE WHEN status = 'active' THEN 1 ELSE 0 END), 0), COALESCE(MAX(created_at), 0) FROM assertion WHERE scope = ?`,
			scope,
		).Scan(&totalAss, &activeAss, &wm.MaxAssertionAt)
		wm.AssertionCount = totalAss*100000 + activeAss
	}
	if logPath != "" {
		if info, err := os.Stat(logPath); err == nil {
			wm.LogSize = info.Size()
		}
	}
	nowUnix := time.Now().Unix()
	wm.Settled = wm.MaxTraceAt == 0 || (nowUnix-wm.MaxTraceAt > 30)
	return wm
}

// ROILedgerSnapshotPath returns the persistent ROI ledger snapshot file path for a scope.
func ROILedgerSnapshotPath(logPath, home, scope string) string {
	stateDir := ""
	if logPath != "" {
		stateDir = filepath.Dir(logPath)
	} else if home != "" {
		stateDir = filepath.Join(home, ".local", "state", "rolling-memory")
	}
	if stateDir == "" {
		return ""
	}
	slug := strings.Trim(scopeSlugRe.ReplaceAllString(strings.TrimSpace(scope), "_"), "_")
	if slug == "" {
		slug = "default"
	}
	return filepath.Join(stateDir, "roi_ledger_v1_"+slug+".json")
}

// TryLoadROILedger checks whether the persistent ROI ledger snapshot on disk is already
// up-to-date with the current database watermark. When matched, it returns the projected
// Report in <2ms without re-running telemetry scans or graph recall.
func TryLoadROILedger(st *store.Store, cfg *config.Config, opts Options, scope, logPath, home string, limit int) (*Report, bool) {
	if strings.TrimSpace(opts.Harness) != "" || st == nil || st.DB() == nil {
		return nil, false
	}
	ledgerPath := ROILedgerSnapshotPath(logPath, home, scope)
	if ledgerPath == "" {
		return nil, false
	}
	raw, err := os.ReadFile(ledgerPath)
	if err != nil || len(raw) == 0 {
		return nil, false
	}
	var snap ROILedgerSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return nil, false
	}
	if snap.Version != roiLedgerSnapshotVersion || snap.Scope != scope {
		return nil, false
	}
	// Never short-circuit on an unfinished partial snapshot: let the next run continue
	// settling the deferred turns until Partial == false.
	if snap.Report.Partial || snap.Report.DeferredTurns > 0 {
		return nil, false
	}
	fp := ComputeDBFingerprint(st.DB(), scope)
	if fp == "" || snap.DBFingerprint != fp {
		return nil, false
	}
	curWM := QuerySourceWatermark(st, scope, logPath)
	if !curWM.Settled || !snap.Watermark.Settled ||
		curWM.TraceCount != snap.Watermark.TraceCount ||
		curWM.MaxTraceAt != snap.Watermark.MaxTraceAt ||
		curWM.AssertionCount != snap.Watermark.AssertionCount ||
		curWM.MaxAssertionAt != snap.Watermark.MaxAssertionAt {
		return nil, false
	}

	rep := snap.Report
	if opts.BudgetSeconds > 0 {
		rep.BudgetSeconds = opts.BudgetSeconds
	}
	fullSessions := snap.FullSessions
	if len(fullSessions) == 0 && len(rep.Sessions) > 0 {
		fullSessions = rep.Sessions
	}
	// Always recompute rolling time windows (1h / 24h / 7d / 30d), HourlySavings, and Today*
	// against the current wall clock so a snapshot read hours later or across midnight
	// reflects the exact live rolling windows without re-running recall.
	nowLocal := time.Now()
	if len(fullSessions) > 0 {
		recomputeRollingTimeWindows(&rep, fullSessions, nowLocal)
	}
	computeEngineeringDiagnostics(&rep, fullSessions, logPath, home, nil)
	// Refresh lightweight real-time 3D continuity & hook log counters if log grew.
	rep.Continuity = collectSessionContinuity(st, scope, opts.WorkspaceDir)
	if curWM.LogSize != snap.Watermark.LogSize && logPath != "" {
		refreshReportHookLogCounters(&rep, logPath)
	}
	// Persist the refreshed view (rolling windows recomputed above) when it aged out,
	// crossed a day boundary, or still needs the telemetry upgrade. The served report
	// keeps the evaluation stamp of the persisted ledger — a ledger-served read must be
	// a pure function of (snapshot, now), like the verbatim re-read contract in
	// TestEvaluatePersistsWindowsAndServesThemFromTheLedger; the snapshot's own write
	// time (UpdatedAt/UpdatedUnix) advances instead, so the daily refresh still fires.
	needsDiagUpgrade := snap.Report.HookTelemetry.L1DirectInjectedTurns == 0 && len(fullSessions) > 0
	if needsDiagUpgrade || nowLocal.Unix()-snap.UpdatedUnix >= 60 || !strings.HasPrefix(strings.TrimSpace(snap.UpdatedAt), nowLocal.Format("2006-01-02")) {
		SaveROILedger(st, scope, logPath, home, fullSessions, &rep)
	}
	if len(fullSessions) > limit {
		rep.Sessions = append([]SessionEvaluation(nil), fullSessions[:limit]...)
	} else {
		rep.Sessions = append([]SessionEvaluation(nil), fullSessions...)
	}
	return &rep, true
}

// SaveROILedger persists the full evaluated session ledger and aggregated report atomically.
func SaveROILedger(st *store.Store, scope, logPath, home string, fullSessions []SessionEvaluation, rep *Report) {
	if st == nil || st.DB() == nil || rep == nil {
		return
	}
	ledgerPath := ROILedgerSnapshotPath(logPath, home, scope)
	if ledgerPath == "" {
		return
	}
	fp := ComputeDBFingerprint(st.DB(), scope)
	if fp == "" {
		return
	}
	wm := QuerySourceWatermark(st, scope, logPath)
	cappedSessions := fullSessions
	if len(cappedSessions) > 2500 {
		cappedSessions = cappedSessions[:2500]
	}
	snap := ROILedgerSnapshot{
		Version:       roiLedgerSnapshotVersion,
		Scope:         scope,
		DBFingerprint: fp,
		// UpdatedAt is the snapshot's own write time (the served report's EvaluatedAt
		// stays the evaluation stamp of the report it carries).
		UpdatedAt:    time.Now().Format("2006-01-02 15:04:05"),
		UpdatedUnix:  time.Now().Unix(),
		Watermark:    wm,
		FullSessions: append([]SessionEvaluation(nil), cappedSessions...),
		Report:       *rep,
	}
	snap.Report.Sessions = append([]SessionEvaluation(nil), cappedSessions...)
	raw, err := json.Marshal(snap)
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(ledgerPath), 0o755)
	writeAtomicFile(ledgerPath, raw)
	if config.IsDefaultScope(scope) {
		latestPath := filepath.Join(filepath.Dir(ledgerPath), "roi_ledger_v1.json")
		writeAtomicFile(latestPath, raw)
	}
}

func writeAtomicFile(targetPath string, data []byte) {
	if targetPath == "" || len(data) == 0 {
		return
	}
	tmpPath := fmt.Sprintf("%s.tmp.%d.%d", targetPath, os.Getpid(), time.Now().UnixNano())
	if err := os.WriteFile(tmpPath, data, 0o600); err == nil {
		if os.Rename(tmpPath, targetPath) != nil {
			_ = os.Remove(tmpPath)
		}
	}
}

func refreshReportHookLogCounters(report *Report, logPath string) {
	f, err := os.Open(logPath)
	if err != nil {
		return
	}
	defer f.Close()
	var latencies []int
	totalRuns := 0
	logDedupCount := 0
	logAbsentCount := 0
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var m map[string]any
		if json.Unmarshal([]byte(line), &m) != nil {
			continue
		}
		if asString(m["event"]) != "recall" {
			continue
		}
		totalRuns++
		if ms := asInt(m["ms"]); ms > 0 {
			latencies = append(latencies, ms)
		}
		if asInt(m["dedup"]) > 0 {
			logDedupCount++
		} else if asInt(m["absent"]) > 0 {
			logAbsentCount++
		}
	}
	if totalRuns > report.TotalHookRuns {
		report.TotalHookRuns = totalRuns
	}
	report.LogDedupTotal = logDedupCount
	report.LogAbsentTotal = logAbsentCount
	if len(latencies) > 0 {
		sum := 0
		for _, v := range latencies {
			sum += v
		}
		report.AvgLatencyMS = sum / len(latencies)
		sortedLat := append([]int(nil), latencies...)
		sort.Ints(sortedLat)
		report.P50LatencyMS = sortedLat[len(sortedLat)/2]
		p95Idx := int(float64(len(sortedLat)-1) * 0.95)
		report.P95LatencyMS = sortedLat[p95Idx]
	}
}

type persistedNativeTelemetryCache struct {
	Version              int                            `json:"version"`
	ZCodeTurnCount       int                            `json:"zcode_turn_count"`
	ZCodeMaxStartedAt    int64                          `json:"zcode_max_started_at"`
	ZCodeMaxCompletedAt  int64                          `json:"zcode_max_completed_at"`
	EvaluatedSessionHash string                         `json:"evaluated_session_hash"`
	UpdatedUnix          int64                          `json:"updated_unix"`
	Sessions             map[string]*nativeSessionStats `json:"sessions"`
	EmpiricalBaseline    float64                        `json:"empirical_baseline"`
	AvgDynamicRoundTok   int                            `json:"avg_dynamic_round_tok"`
	AvgFreshTok          int                            `json:"avg_fresh_tok"`
	AvgCachedTok         int                            `json:"avg_cached_tok"`
}

func queryZCodeTelemetryWatermark(home string, evaluatedSessions map[string]bool) (int, int64, int64, string) {
	var sessList []string
	for s := range evaluatedSessions {
		sessList = append(sessList, s)
	}
	sort.Strings(sessList)
	sessHash := ids.SHA256Hex(strings.Join(sessList, "\x00"))[:16]
	if home == "" {
		return 0, 0, 0, sessHash
	}
	zdbPath := filepath.Join(home, ".zcode", "cli", "db", "db.sqlite")
	if _, err := os.Stat(zdbPath); err != nil {
		return 0, 0, 0, sessHash
	}
	db, err := sql.Open("sqlite", zdbPath+"?mode=ro&_pragma=busy_timeout(500)")
	if err != nil {
		return 0, 0, 0, sessHash
	}
	defer db.Close()
	var cnt int
	var maxStarted, maxCompleted int64
	_ = db.QueryRow(`SELECT COUNT(*), COALESCE(MAX(started_at), 0), COALESCE(MAX(completed_at), 0) FROM turn_usage`).Scan(&cnt, &maxStarted, &maxCompleted)
	return cnt, maxStarted, maxCompleted, sessHash
}
