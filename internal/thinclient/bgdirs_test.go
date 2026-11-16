// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package thinclient_test

import (
	"archive/zip"
	"bytes"
	"compress/zlib"
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rocareer/rolling-memory/internal/thinclient"
	"modernc.org/sqlite"
)

func writeSyntheticDocx(t *testing.T, path string, paragraphs []string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir docx dir: %v", err)
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("word/document.xml")
	if err != nil {
		t.Fatalf("create word/document.xml: %v", err)
	}
	var xmlBody strings.Builder
	xmlBody.WriteString(`<?xml version="1.0" encoding="UTF-8"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>`)
	for _, p := range paragraphs {
		xmlBody.WriteString(`<w:p><w:r><w:t>`)
		xmlBody.WriteString(p)
		xmlBody.WriteString(`</w:t></w:r></w:p>`)
	}
	xmlBody.WriteString(`</w:body></w:document>`)
	if _, err := w.Write([]byte(xmlBody.String())); err != nil {
		t.Fatalf("write word/document.xml: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close docx zip: %v", err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatalf("write docx file: %v", err)
	}
}

func writeSyntheticXlsx(t *testing.T, path string, sheetName string, rows [][]string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir xlsx dir: %v", err)
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	// 1. xl/workbook.xml
	wb, _ := zw.Create("xl/workbook.xml")
	_, _ = wb.Write([]byte(fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?><workbook><sheets><sheet name=%q sheetId="1"/></sheets></workbook>`, sheetName)))

	// 2. xl/sharedStrings.xml & xl/worksheets/sheet1.xml
	var shared []string
	sharedIdx := map[string]int{}
	addShared := func(s string) int {
		if idx, ok := sharedIdx[s]; ok {
			return idx
		}
		idx := len(shared)
		shared = append(shared, s)
		sharedIdx[s] = idx
		return idx
	}

	var sheetXML strings.Builder
	sheetXML.WriteString(`<?xml version="1.0" encoding="UTF-8"?><worksheet><sheetData>`)
	for _, r := range rows {
		sheetXML.WriteString(`<row>`)
		for _, cell := range r {
			sIdx := addShared(cell)
			sheetXML.WriteString(fmt.Sprintf(`<c t="s"><v>%d</v></c>`, sIdx))
		}
		sheetXML.WriteString(`</row>`)
	}
	sheetXML.WriteString(`</sheetData></worksheet>`)

	var ssXML strings.Builder
	ssXML.WriteString(`<?xml version="1.0" encoding="UTF-8"?><sst>`)
	for _, s := range shared {
		ssXML.WriteString(`<si><t>` + s + `</t></si>`)
	}
	ssXML.WriteString(`</sst>`)

	ssWriter, _ := zw.Create("xl/sharedStrings.xml")
	_, _ = ssWriter.Write([]byte(ssXML.String()))

	shWriter, _ := zw.Create("xl/worksheets/sheet1.xml")
	_, _ = shWriter.Write([]byte(sheetXML.String()))

	if err := zw.Close(); err != nil {
		t.Fatalf("close xlsx zip: %v", err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatalf("write xlsx file: %v", err)
	}
}

func writeSyntheticPDF(t *testing.T, path string, lines []string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir pdf dir: %v", err)
	}
	var content strings.Builder
	content.WriteString("BT\n/F1 12 Tf\n")
	for _, l := range lines {
		content.WriteString(fmt.Sprintf("(%s) Tj\n0 -14 Td\n", l))
	}
	content.WriteString("ET\n")

	var compressed bytes.Buffer
	zw := zlib.NewWriter(&compressed)
	_, _ = zw.Write([]byte(content.String()))
	_ = zw.Close()

	var pdf bytes.Buffer
	pdf.WriteString("%PDF-1.4\n1 0 obj\n<< /Length ")
	pdf.WriteString(fmt.Sprintf("%d /Filter /FlateDecode >>\nstream\n", compressed.Len()))
	pdf.Write(compressed.Bytes())
	pdf.WriteString("\nendstream\nendobj\n%%EOF\n")

	if err := os.WriteFile(path, pdf.Bytes(), 0o600); err != nil {
		t.Fatalf("write pdf file: %v", err)
	}
}

// TestBackgroundMultiDirs_NestedSubdirsAndSevenDocumentFormats verifies:
//  1. Mounting multiple local directories, including deeply nested subdirectories (`a/b/c/d`)
//     and overlapping parent/child mounted directories (verifying zero duplicate ingestion);
//  2. Native zero-CGO streaming extraction across 7 formats:
//     Word (`.docx`), Excel (`.xlsx`), PDF (`.pdf`), Markdown (`.md`), JSON (`.json`), CSV (`.csv`), and Go source (`.go`);
//  3. Source-in-place live on-demand reading (`LoadLocalDocument` reads directly from the disk file
//     without duplicating file contents into `thin_vault_docs`);
//  4. Millisecond `os.Stat` delta sync (unchanged files skipped in 0 I/O; modified `.xlsx` & `.docx`
//     re-ingested and cascading `stale` invalidation to local `noetic` records; deleted files purged).
func TestBackgroundMultiDirs_NestedSubdirsAndSevenDocumentFormats(t *testing.T) {
	rootDir1 := filepath.Join(t.TempDir(), "knowledge_hub")
	nestedDeepDir := filepath.Join(rootDir1, "dept_ai", "project_aurora", "specs", "2026_q4")
	rootDir2 := filepath.Join(t.TempDir(), "code_workspace")

	// 1. Word (.docx) inside 4-level nested subdirectory
	docxPath := filepath.Join(nestedDeepDir, "aurora_architecture.docx")
	writeSyntheticDocx(t, docxPath, []string{
		"Project Aurora uses the Hyperion-9optical interconnect for inter-node tensor routing.",
		"Maximum switching latency is 420 nanoseconds under full torus load.",
	})

	// 2. Excel (.xlsx) inside nested subdirectory
	xlsxPath := filepath.Join(rootDir1, "finance", "2026", "cloud_budget.xlsx")
	writeSyntheticXlsx(t, xlsxPath, "Q4Budget", [][]string{
		{"Vendor", "ClusterCode", "QuarterlyCostUSD"},
		{"NebulaCompute", "NC-A100-West", "184500"},
		{"QuantumStorage", "QS-NVMe-East", "62300"},
	})

	// 3. PDF (.pdf) with /FlateDecode compressed stream
	pdfPath := filepath.Join(rootDir1, "papers", "optical_switching_whitepaper.pdf")
	writeSyntheticPDF(t, pdfPath, []string{
		"Whitepaper: Zirconium-doped waveguide modulators achieve 99.4 percent phase fidelity.",
		" calibration wavelength is locked at 1550 nanometers.",
	})

	// 4. Markdown (.md)
	mdPath := filepath.Join(rootDir1, "runbook.md")
	if err := os.WriteFile(mdPath, []byte("# Emergency Failover Runbook\nExecute command `aurora-ctl drain --graceful` before replacing optical transceivers.\n"), 0o600); err != nil {
		t.Fatalf("write md: %v", err)
	}

	// 5. JSON (.json) inside code_workspace
	jsonPath := filepath.Join(rootDir2, "config", "endpoints.json")
	if err := os.MkdirAll(filepath.Dir(jsonPath), 0o755); err != nil {
		t.Fatalf("mkdir json: %v", err)
	}
	if err := os.WriteFile(jsonPath, []byte(`{"telemetry":{"collector_host":"telemetry-v9.internal.aurora","port":9411}}`), 0o600); err != nil {
		t.Fatalf("write json: %v", err)
	}

	// 6. CSV (.csv)
	csvPath := filepath.Join(rootDir2, "inventory", "racks.csv")
	if err := os.MkdirAll(filepath.Dir(csvPath), 0o755); err != nil {
		t.Fatalf("mkdir csv: %v", err)
	}
	if err := os.WriteFile(csvPath, []byte("RackID,Datacenter,SwitchModel\nRACK-ZURICH-07,Zurich-DC2,Arista-7808R3\n"), 0o600); err != nil {
		t.Fatalf("write csv: %v", err)
	}

	// 7. Go source code (.go) + ignored node_modules noise
	goPath := filepath.Join(rootDir2, "pkg", "router", "torus_router.go")
	if err := os.MkdirAll(filepath.Dir(goPath), 0o755); err != nil {
		t.Fatalf("mkdir go: %v", err)
	}
	if err := os.WriteFile(goPath, []byte("package router\n// ComputeTorusShortestHop calculates minimal hop count on a 3D torus.\nfunc ComputeTorusShortestHop(srcNode, dstNode int) int { return 0 }\n"), 0o600); err != nil {
		t.Fatalf("write go: %v", err)
	}
	ignoredNoise := filepath.Join(rootDir2, "node_modules", "bad_pkg", "noise.js")
	_ = os.MkdirAll(filepath.Dir(ignoredNoise), 0o755)
	_ = os.WriteFile(ignoredNoise, []byte("should_never_be_indexed_node_modules"), 0o600)

	// Open SQLite Vault & Client
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "bg_multi.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	vault, err := thinclient.NewVault(db, []byte("bg-multi-dir-secret-2026"))
	if err != nil {
		t.Fatalf("NewVault: %v", err)
	}
	client := thinclient.NewClient(vault, thinclient.NewLocalVaultTransport(vault))
	ctx := context.Background()

	// Mount rootDir1, rootDir2, AND the nested child directory `nestedDeepDir` (testing overlapping parent+child mounts!)
	if _, err := client.AddBackgroundDir(rootDir1); err != nil {
		t.Fatalf("AddBackgroundDir(rootDir1): %v", err)
	}
	if _, err := client.AddBackgroundDir(nestedDeepDir); err != nil {
		t.Fatalf("AddBackgroundDir(nestedDeepDir): %v", err)
	}
	if _, err := client.AddBackgroundDir(rootDir2); err != nil {
		t.Fatalf("AddBackgroundDir(rootDir2): %v", err)
	}

	// Pass 1: Initial Sync across all 3 mounts (7 supported files across nested dirs; 0 duplicates!)
	rep1, err := client.SyncBackgroundDirs(ctx)
	if err != nil {
		t.Fatalf("SyncBackgroundDirs pass 1: %v", err)
	}
	t.Logf("  [Pass 1 Sync] MountedDirs=%d ScannedFiles=%d IngestedFiles=%d IngestedChunks=%d",
		rep1.MountedDirs, rep1.ScannedFiles, rep1.IngestedFiles, rep1.IngestedChunks)
	if rep1.ScannedFiles != 7 || rep1.IngestedFiles != 7 {
		t.Fatalf("expected exactly 7 unique supported files across nested & overlapping mounts, got Scanned=%d Ingested=%d",
			rep1.ScannedFiles, rep1.IngestedFiles)
	}

	// Pass 2: Immediate re-sync without touching files -> 100% UnchangedFiles (0 file reads!)
	rep2, err := client.SyncBackgroundDirs(ctx)
	if err != nil {
		t.Fatalf("SyncBackgroundDirs pass 2: %v", err)
	}
	if rep2.UnchangedFiles != 7 || rep2.IngestedFiles != 0 {
		t.Fatalf("expected 7 unchanged files and 0 re-ingested on Pass 2, got %+v", rep2)
	}

	// Verify Recall + Live On-Demand Reading across all 7 formats!
	queries := []struct {
		format string
		query  string
		expect string
	}{
		{"Word (.docx)", "Hyperion-9optical interconnect switching latency", "420 nanoseconds"},
		{"Excel (.xlsx)", "NebulaCompute NC-A100-West QuarterlyCostUSD", "184500"},
		{"PDF (.pdf)", "Zirconium-doped waveguide modulators wavelength", "1550 nanometers"},
		{"Markdown (.md)", "Emergency Failover aurora-ctl drain", "transceivers"},
		{"JSON (.json)", "telemetry collector_host port", "telemetry-v9.internal.aurora"},
		{"CSV (.csv)", "RACK-ZURICH-07 SwitchModel", "Arista-7808R3"},
		{"Go Code (.go)", "ComputeTorusShortestHop 3D torus", "srcNode"},
	}

	var excelUnitID string
	for _, tc := range queries {
		hits, err := client.Recall(ctx, tc.query, 3)
		if err != nil || len(hits) == 0 {
			t.Fatalf("[%s] Recall(%q) failed or empty: hits=%+v err=%v", tc.format, tc.query, hits, err)
		}
		top := hits[0]
		if !strings.Contains(top.RawPlaintext, tc.expect) {
			t.Fatalf("[%s] expected top hit plaintext to contain %q, got %q", tc.format, tc.expect, top.RawPlaintext)
		}
		if tc.format == "Excel (.xlsx)" {
			excelUnitID = top.UnitID
		}
		t.Logf("  ✓ [%s] Live On-Demand Hit: UnitID=%s Layer=%s Snippet=%q",
			tc.format, top.UnitID, top.Layer, strings.ReplaceAll(top.RawPlaintext, "\n", " "))
	}

	// Save a local L3 Noetic record citing the Excel budget unitID
	if err := client.RememberNoetic(
		"NOETIC-BUDGET-Q4",
		"How much is the NebulaCompute NC-A100-West quarterly budget?",
		[]string{excelUnitID},
		nil,
		"The NebulaCompute NC-A100-West quarterly cost is 184,500 USD.",
		time.Now().Unix(),
	); err != nil {
		t.Fatalf("RememberNoetic: %v", err)
	}

	// Modify the Excel (.xlsx) file on disk (change 184500 -> 219000) and delete the CSV file!
	time.Sleep(15 * time.Millisecond)
	writeSyntheticXlsx(t, xlsxPath, "Q4Budget", [][]string{
		{"Vendor", "ClusterCode", "QuarterlyCostUSD"},
		{"NebulaCompute", "NC-A100-West", "219000"},
		{"QuantumStorage", "QS-NVMe-East", "62300"},
	})
	_ = os.Remove(csvPath)

	// Pass 3: Delta Sync detects 1 modified file (.xlsx), 5 unchanged files, 1 deleted file (.csv),
	// and cascades `stale` invalidation to `NOETIC-BUDGET-Q4`!
	rep3, err := client.SyncBackgroundDirs(ctx)
	if err != nil {
		t.Fatalf("SyncBackgroundDirs pass 3: %v", err)
	}
	t.Logf("  [Pass 3 Delta Sync] Unchanged=%d Ingested=%d Deleted=%d InvalidatedNoetics=%v",
		rep3.UnchangedFiles, rep3.IngestedFiles, rep3.DeletedFiles, rep3.InvalidatedNoeticIDs)
	if rep3.IngestedFiles != 1 || rep3.DeletedFiles != 1 || len(rep3.InvalidatedNoeticIDs) != 1 || rep3.InvalidatedNoeticIDs[0] != "NOETIC-BUDGET-Q4" {
		t.Fatalf("unexpected Pass 3 delta sync report: %+v", rep3)
	}

	// Verify live on-demand read now returns the updated Excel value "219000"!
	updatedHits, err := client.Recall(ctx, "NebulaCompute NC-A100-West QuarterlyCostUSD", 3)
	if err != nil || len(updatedHits) == 0 || !strings.Contains(updatedHits[0].RawPlaintext, "219000") {
		t.Fatalf("expected updated Excel live read to contain 219000, got %+v (err=%v)", updatedHits, err)
	}
	t.Logf("  ✓ Live On-Demand Read after Excel edit returned updated value: %q",
		strings.ReplaceAll(updatedHits[0].RawPlaintext, "\n", " "))
}

type backgroundTransportStub struct {
	ingest      func(context.Context, thinclient.BlindPulsePacket) (thinclient.CloudIngestResponse, error)
	deleteUnits func(context.Context, []string) error
}

func (s *backgroundTransportStub) IngestBlindPulse(ctx context.Context, pkt thinclient.BlindPulsePacket) (thinclient.CloudIngestResponse, error) {
	if s.ingest != nil {
		return s.ingest(ctx, pkt)
	}
	return thinclient.CloudIngestResponse{UnitID: pkt.UnitID, Layer: pkt.Layer}, nil
}

func (s *backgroundTransportStub) QueryBlindManifold(_ context.Context, _ thinclient.BlindPulsePacket, _ int) ([]thinclient.CloudQueryHit, error) {
	return nil, nil
}

func (s *backgroundTransportStub) DeleteUnits(ctx context.Context, unitIDs []string) error {
	if s.deleteUnits != nil {
		return s.deleteUnits(ctx, unitIDs)
	}
	return nil
}

func newBackgroundTestDir(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("canonicalize test directory: %v", err)
	}
	return filepath.Clean(resolved)
}

func newBackgroundTestClient(t *testing.T, transport thinclient.BrainTransport) (*thinclient.Client, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "background_test.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	vault, err := thinclient.NewVault(db, []byte("background-test-secret"))
	if err != nil {
		t.Fatalf("NewVault: %v", err)
	}
	return thinclient.NewClient(vault, transport), db
}

func countBackgroundRows(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var count int
	if err := db.QueryRow(query, args...).Scan(&count); err != nil {
		t.Fatalf("count background rows: %v", err)
	}
	return count
}

func loadBackgroundUnitIDs(t *testing.T, db *sql.DB, absPath string) []string {
	t.Helper()
	var unitIDsJSON string
	if err := db.QueryRow(`SELECT unit_ids_json FROM thin_vault_bg_files WHERE abs_path = ?`, absPath).Scan(&unitIDsJSON); err != nil {
		t.Fatalf("load background unit IDs: %v", err)
	}
	var unitIDs []string
	if err := json.Unmarshal([]byte(unitIDsJSON), &unitIDs); err != nil {
		t.Fatalf("decode background unit IDs: %v", err)
	}
	return unitIDs
}

func TestBackgroundSyncIngestFailureDoesNotWriteSyncedMetadata(t *testing.T) {
	root := newBackgroundTestDir(t)
	path := filepath.Join(root, "retry.md")
	if err := os.WriteFile(path, []byte("retry this background document after cloud recovery"), 0o600); err != nil {
		t.Fatalf("write retry document: %v", err)
	}

	// The upload pool invokes this stub from worker goroutines, so the call counter is atomic.
	var ingestCalls atomic.Int64
	transport := &backgroundTransportStub{}
	transport.ingest = func(_ context.Context, pkt thinclient.BlindPulsePacket) (thinclient.CloudIngestResponse, error) {
		if ingestCalls.Add(1) == 1 {
			return thinclient.CloudIngestResponse{}, errors.New("cloud ingest unavailable")
		}
		return thinclient.CloudIngestResponse{UnitID: pkt.UnitID, Layer: pkt.Layer}, nil
	}
	client, db := newBackgroundTestClient(t, transport)
	if _, err := client.AddBackgroundDir(root); err != nil {
		t.Fatalf("AddBackgroundDir: %v", err)
	}

	report, err := client.SyncBackgroundDirs(context.Background())
	if err == nil || !strings.Contains(err.Error(), "cloud ingest unavailable") {
		t.Fatalf("expected ingest error, got report=%+v err=%v", report, err)
	}
	if report.IngestedFiles != 0 || report.IngestedChunks != 0 {
		t.Fatalf("failed ingest must not be reported as synced: %+v", report)
	}
	if got := countBackgroundRows(t, db, `SELECT COUNT(*) FROM thin_vault_bg_files WHERE abs_path = ?`, path); got != 0 {
		t.Fatalf("failed ingest wrote synced file metadata: %d", got)
	}
	if got := countBackgroundRows(t, db, `SELECT COUNT(*) FROM thin_vault_bg_chunks WHERE abs_path = ?`, path); got != 0 {
		t.Fatalf("failed ingest wrote chunk metadata: %d", got)
	}

	report, err = client.SyncBackgroundDirs(context.Background())
	if err != nil {
		t.Fatalf("retry after ingest recovery: %v", err)
	}
	if report.IngestedFiles != 1 || ingestCalls.Load() != 2 {
		t.Fatalf("expected one retry ingest, got report=%+v calls=%d", report, ingestCalls.Load())
	}
	if got := countBackgroundRows(t, db, `SELECT COUNT(*) FROM thin_vault_bg_files WHERE abs_path = ?`, path); got != 1 {
		t.Fatalf("successful retry did not write synced metadata: %d", got)
	}

	report, err = client.SyncBackgroundDirs(context.Background())
	if err != nil || report.UnchangedFiles != 1 || ingestCalls.Load() != 2 {
		t.Fatalf("synced retry cursor was not reusable: report=%+v calls=%d err=%v", report, ingestCalls.Load(), err)
	}
}

func TestBackgroundSyncRequiresTransport(t *testing.T) {
	client, _ := newBackgroundTestClient(t, nil)
	if _, err := client.SyncBackgroundDirs(context.Background()); err == nil || !strings.Contains(err.Error(), "non-nil Transport") {
		t.Fatalf("expected explicit nil Transport error, got %v", err)
	}
}

func TestBackgroundSyncHTTPIngestUsesBlindBackgroundUser10(t *testing.T) {
	type capturedRequest struct {
		body   string
		packet thinclient.BlindPulsePacket
		auth   string
		user   string
		scope  string
	}
	captured := make(chan capturedRequest, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/open/memory/v1/ingest" {
			http.NotFound(w, r)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var pkt thinclient.BlindPulsePacket
		if err := json.Unmarshal(body, &pkt); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		captured <- capturedRequest{
			body:   string(body),
			packet: pkt,
			auth:   r.Header.Get("Authorization"),
			user:   r.Header.Get("X-Rolling-Memory-User"),
			scope:  r.Header.Get("X-Rolling-Memory-Scope"),
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(thinclient.CloudIngestResponse{UnitID: pkt.UnitID, Layer: pkt.Layer})
	}))
	defer srv.Close()

	root := newBackgroundTestDir(t)
	plaintext := "AsterQuixoticPlaintext do-not-leak-background remains local"
	if err := os.WriteFile(filepath.Join(root, "private.md"), []byte(plaintext), 0o600); err != nil {
		t.Fatalf("write private document: %v", err)
	}
	transport := thinclient.NewHTTPBrainTransport(srv.URL, "test-token")
	transport.Scope = "background-scope"
	client, _ := newBackgroundTestClient(t, transport)
	if _, err := client.AddBackgroundDir(root); err != nil {
		t.Fatalf("AddBackgroundDir: %v", err)
	}
	if _, err := client.SyncBackgroundDirs(context.Background()); err != nil {
		t.Fatalf("SyncBackgroundDirs: %v", err)
	}

	req := <-captured
	for _, secret := range []string{"asterquixoticplaintext", "do-not-leak-background", "remains local"} {
		if strings.Contains(strings.ToLower(req.body), secret) {
			t.Fatalf("plaintext leaked in background ingest %q: %s", secret, req.body)
		}
	}
	if req.packet.Layer != thinclient.LayerBackground || req.packet.UserID != thinclient.DefaultUpstreamDevUserID {
		t.Fatalf("unexpected background ingest identity: %+v", req.packet)
	}
	if req.packet.Scope != "background-scope" || req.scope != "background-scope" {
		t.Fatalf("scope missing from body or headers: packet=%q header=%q", req.packet.Scope, req.scope)
	}
	if req.auth != "Bearer test-token" || req.user != "10" {
		t.Fatalf("unexpected ingest headers: auth=%q user=%q", req.auth, req.user)
	}
}

func TestClientRemoveBackgroundDirPurgeFailureKeepsMountAndRetries(t *testing.T) {
	root := newBackgroundTestDir(t)
	path := filepath.Join(root, "mounted.md")
	if err := os.WriteFile(path, []byte("mounted unit survives a failed cloud purge"), 0o600); err != nil {
		t.Fatalf("write mounted document: %v", err)
	}

	purgeCalls := 0
	transport := &backgroundTransportStub{}
	transport.deleteUnits = func(_ context.Context, unitIDs []string) error {
		purgeCalls++
		if len(unitIDs) != 1 {
			t.Fatalf("expected one unit per purge, got %v", unitIDs)
		}
		if purgeCalls == 1 {
			return errors.New("cloud purge unavailable")
		}
		return nil
	}
	client, db := newBackgroundTestClient(t, transport)
	if _, err := client.AddBackgroundDir(root); err != nil {
		t.Fatalf("AddBackgroundDir: %v", err)
	}
	if _, err := client.SyncBackgroundDirs(context.Background()); err != nil {
		t.Fatalf("initial SyncBackgroundDirs: %v", err)
	}
	oldUnitIDs := loadBackgroundUnitIDs(t, db, path)

	if _, err := client.RemoveBackgroundDir(context.Background(), root); err == nil || !strings.Contains(err.Error(), "cloud purge unavailable") {
		t.Fatalf("expected purge failure, got %v", err)
	}
	mounts, err := client.Vault.ListBackgroundDirs()
	if err != nil {
		t.Fatalf("ListBackgroundDirs after failed purge: %v", err)
	}
	if len(mounts) != 1 || mounts[0].FileCount != 1 {
		t.Fatalf("failed purge removed mount metadata: %+v", mounts)
	}
	if got := loadBackgroundUnitIDs(t, db, path); len(got) != 1 || got[0] != oldUnitIDs[0] {
		t.Fatalf("failed purge changed retry metadata: got=%v want=%v", got, oldUnitIDs)
	}

	removedIDs, err := client.RemoveBackgroundDir(context.Background(), root)
	if err != nil {
		t.Fatalf("RemoveBackgroundDir retry: %v", err)
	}
	if purgeCalls != 2 || len(removedIDs) != 1 || removedIDs[0] != oldUnitIDs[0] {
		t.Fatalf("unexpected retry purge: calls=%d removed=%v want=%v", purgeCalls, removedIDs, oldUnitIDs)
	}
	mounts, err = client.Vault.ListBackgroundDirs()
	if err != nil || len(mounts) != 0 {
		t.Fatalf("mount still present after successful purge: mounts=%+v err=%v", mounts, err)
	}
	if got := countBackgroundRows(t, db, `SELECT COUNT(*) FROM thin_vault_bg_files WHERE abs_path = ?`, path); got != 0 {
		t.Fatalf("local file metadata retained after successful remove: %d", got)
	}
}

// countingBeginDriver wraps the sqlite driver so tests can observe how many transactions a code
// path opens on one vault.
type countingBeginDriver struct {
	inner  driver.Driver
	begins *atomic.Int64
}

func (d countingBeginDriver) Open(name string) (driver.Conn, error) {
	conn, err := d.inner.Open(name)
	if err != nil {
		return nil, err
	}
	return &countingBeginConn{Conn: conn, begins: d.begins}, nil
}

type countingBeginConn struct {
	driver.Conn
	begins *atomic.Int64
}

func (c *countingBeginConn) Begin() (driver.Tx, error) {
	c.begins.Add(1)
	return c.Conn.Begin()
}

func (c *countingBeginConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	c.begins.Add(1)
	if beginTx, ok := c.Conn.(driver.ConnBeginTx); ok {
		return beginTx.BeginTx(ctx, opts)
	}
	return c.Conn.Begin()
}

var (
	countingDriverOnce   sync.Once
	countingDriverBegins atomic.Int64
)

// openCountingVaultDB opens a vault database behind the transaction-counting driver.
func openCountingVaultDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	countingDriverOnce.Do(func() {
		sql.Register("sqlite_bg_remove_counting", countingBeginDriver{inner: &sqlite.Driver{}, begins: &countingDriverBegins})
	})
	db, err := sql.Open("sqlite_bg_remove_counting", path)
	if err != nil {
		t.Fatalf("open counting sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// TestRemoveBackgroundDirCommitsPerTrackedFile 验证挂载移除按文件逐个提交有界事务：移除一度在单个
// 巨型写事务里删除整个挂载，把 SQLite 写锁焊死数分钟，并发写入方 5s busy_timeout 到期即报
// SQLITE_BUSY（曾把并发 L1 同步打成 "database is locked" 警告）。
func TestRemoveBackgroundDirCommitsPerTrackedFile(t *testing.T) {
	root := newBackgroundTestDir(t)
	files := []string{"alpha.md", "bravo.md", "charlie.md"}
	for _, name := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte("bounded removal transaction "+name), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	db := openCountingVaultDB(t, filepath.Join(t.TempDir(), "counting_remove.db"))
	vault, err := thinclient.NewVault(db, []byte("counting-remove-secret"))
	if err != nil {
		t.Fatalf("NewVault: %v", err)
	}
	client := thinclient.NewClient(vault, thinclient.NewLocalVaultTransport(vault))
	ctx := context.Background()
	if _, err := client.AddBackgroundDir(root); err != nil {
		t.Fatalf("AddBackgroundDir: %v", err)
	}
	if _, err := client.SyncBackgroundDirs(ctx); err != nil {
		t.Fatalf("initial SyncBackgroundDirs: %v", err)
	}

	before := countingDriverBegins.Load()
	removed, err := client.RemoveBackgroundDir(ctx, root)
	if err != nil {
		t.Fatalf("RemoveBackgroundDir: %v", err)
	}
	if len(removed) == 0 {
		t.Fatalf("removal returned no tracked units")
	}
	if committed := countingDriverBegins.Load() - before; committed < int64(len(files)+1) {
		t.Fatalf("removal began %d transactions, want one bounded commit per tracked file (%d files + 1 mount commit)", committed, len(files))
	}
}

// TestRemoveBackgroundDirSplitsMonsterFileIntoBoundedBatches 验证单个「巨兽文件」（证据报告类大
// JSONL，一块挂载里实测有 9,799 块的单文件）的移除也按单位数分批提交：一度按文件提交时，这样一个
// 文件就是一个远超过 5s busy_timeout 的写事务，足以把并发写入方饿成 SQLITE_BUSY。
func TestRemoveBackgroundDirSplitsMonsterFileIntoBoundedBatches(t *testing.T) {
	root := newBackgroundTestDir(t)
	db := openCountingVaultDB(t, filepath.Join(t.TempDir(), "batched_remove.db"))
	vault, err := thinclient.NewVault(db, []byte("batched-remove-secret"))
	if err != nil {
		t.Fatalf("NewVault: %v", err)
	}
	client := thinclient.NewClient(vault, thinclient.NewLocalVaultTransport(vault))
	ctx := context.Background()
	if _, err := client.AddBackgroundDir(root); err != nil {
		t.Fatalf("AddBackgroundDir: %v", err)
	}
	var dirID string
	if err := db.QueryRow(`SELECT dir_id FROM thin_vault_bg_dirs LIMIT 1`).Scan(&dirID); err != nil {
		t.Fatalf("load dir_id: %v", err)
	}
	const units = 1200 // > 2 x the 512-unit batch cap
	absPath := filepath.Join(root, "monster.jsonl")
	unitIDs := make([]string, units)
	for i := range unitIDs {
		unitIDs[i] = fmt.Sprintf("bg:monster#%d", i)
	}
	unitJSON, err := json.Marshal(unitIDs)
	if err != nil {
		t.Fatalf("encode units: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO thin_vault_bg_files(
			abs_path, dir_id, rel_path, mtime_ns, file_size, content_sha256, chunk_count, unit_ids_json, chunk_hashes_json
		) VALUES (?, ?, 'monster.jsonl', 1, 1, 'sha', ?, ?, '[]')`, absPath, dirID, units, string(unitJSON)); err != nil {
		t.Fatalf("insert file row: %v", err)
	}
	for _, unitID := range unitIDs {
		if _, err := db.Exec(`INSERT INTO thin_vault_bg_chunks(unit_id, abs_path, rel_path, chunk_index) VALUES (?, ?, 'monster.jsonl', 0)`, unitID, absPath); err != nil {
			t.Fatalf("insert chunk row %s: %v", unitID, err)
		}
	}

	before := countingDriverBegins.Load()
	removed, err := client.RemoveBackgroundDir(ctx, root)
	if err != nil {
		t.Fatalf("RemoveBackgroundDir: %v", err)
	}
	if len(removed) != units {
		t.Fatalf("removed %d units, want %d", len(removed), units)
	}
	if committed := countingDriverBegins.Load() - before; committed < 3 {
		t.Fatalf("monster file removal began %d transactions, want one bounded batch per 512 units", committed)
	}
	if got := countBackgroundRows(t, db, `SELECT COUNT(*) FROM thin_vault_bg_chunks WHERE abs_path = ?`, absPath); got != 0 {
		t.Fatalf("chunk rows retained: %d", got)
	}
	if got := countBackgroundRows(t, db, `SELECT COUNT(*) FROM thin_vault_bg_files WHERE abs_path = ?`, absPath); got != 0 {
		t.Fatalf("file row retained: %d", got)
	}
}

// TestSyncBackgroundDirsSplitsMonsterFileCommitAndPurge 验证 SyncBackgroundDirs 在入库与删除多块大文件时
// 同样拆分为有界批次事务（<= 64 块/50ms），避免 commitBackgroundFile 或 purgeTrackedBackgroundFile 单文件
// 整块大事务焊死写锁。
func TestSyncBackgroundDirsSplitsMonsterFileCommitAndPurge(t *testing.T) {
	root := newBackgroundTestDir(t)
	db := openCountingVaultDB(t, filepath.Join(t.TempDir(), "batched_sync.db"))
	vault, err := thinclient.NewVault(db, []byte("batched-sync-secret"))
	if err != nil {
		t.Fatalf("NewVault: %v", err)
	}
	client := thinclient.NewClient(vault, thinclient.NewLocalVaultTransport(vault))
	ctx := context.Background()
	if _, err := client.AddBackgroundDir(root); err != nil {
		t.Fatalf("AddBackgroundDir: %v", err)
	}

	var b strings.Builder
	// The 1024-token budget packs several sections per chunk, so the fixture carries enough
	// sections to exceed the 256-chunk commit batch size.
	for sec := 0; sec < 700; sec++ {
		fmt.Fprintf(&b, "# Section %d\n", sec)
		for rep := 0; rep < 12; rep++ {
			fmt.Fprintf(&b, "Section %d paragraph %d carries distinct lexical tokens for bounded batch commit verification.\n", sec, rep)
		}
		b.WriteString("\n")
	}
	absPath := filepath.Join(root, "large_doc.md")
	if err := os.WriteFile(absPath, []byte(b.String()), 0o600); err != nil {
		t.Fatalf("write large_doc.md: %v", err)
	}

	beforeCommit := countingDriverBegins.Load()
	rep, err := client.SyncBackgroundDirs(ctx)
	if err != nil {
		t.Fatalf("SyncBackgroundDirs ingest: %v", err)
	}
	if rep.IngestedChunks <= 256 {
		t.Fatalf("expected > 256 chunks to exercise multi-batch commit, got %d", rep.IngestedChunks)
	}
	commitTxns := countingDriverBegins.Load() - beforeCommit
	wantMinTxns := int64((rep.IngestedChunks + 255) / 256)
	if commitTxns < wantMinTxns {
		t.Fatalf("large file ingest began %d transactions for %d chunks, want >= %d", commitTxns, rep.IngestedChunks, wantMinTxns)
	}

	if err := os.Remove(absPath); err != nil {
		t.Fatalf("remove large_doc.md: %v", err)
	}
	beforePurge := countingDriverBegins.Load()
	delRep, err := client.SyncBackgroundDirs(ctx)
	if err != nil {
		t.Fatalf("SyncBackgroundDirs delete: %v", err)
	}
	if delRep.DeletedChunks != rep.IngestedChunks {
		t.Fatalf("DeletedChunks = %d, want %d", delRep.DeletedChunks, rep.IngestedChunks)
	}
	purgeTxns := countingDriverBegins.Load() - beforePurge
	if purgeTxns < wantMinTxns {
		t.Fatalf("large file delete began %d transactions for %d chunks, want >= %d", purgeTxns, delRep.DeletedChunks, wantMinTxns)
	}
}

// TestRemoveBackgroundDirCascadesFTSAndNoetic 验证有界事务移除与旧整体事务语义一致：文件/分块/FTS
// 行全部清除，引用被删单元的活动 Noetic 记录转 stale，未引用者保持不变，挂载点整体消失。
func TestRemoveBackgroundDirCascadesFTSAndNoetic(t *testing.T) {
	root := newBackgroundTestDir(t)
	pathA := filepath.Join(root, "a.md")
	pathB := filepath.Join(root, "b.md")
	if err := os.WriteFile(pathA, []byte("first tracked document feeds the citation cascade"), 0o600); err != nil {
		t.Fatalf("write a.md: %v", err)
	}
	if err := os.WriteFile(pathB, []byte("second tracked document stays uncited"), 0o600); err != nil {
		t.Fatalf("write b.md: %v", err)
	}
	client, db := newBackgroundTestClient(t, &backgroundTransportStub{})
	ctx := context.Background()
	if _, err := client.AddBackgroundDir(root); err != nil {
		t.Fatalf("AddBackgroundDir: %v", err)
	}
	if _, err := client.SyncBackgroundDirs(ctx); err != nil {
		t.Fatalf("initial SyncBackgroundDirs: %v", err)
	}
	unitsA := loadBackgroundUnitIDs(t, db, pathA)
	unitsB := loadBackgroundUnitIDs(t, db, pathB)
	if len(unitsA) == 0 || len(unitsB) == 0 {
		t.Fatalf("expected tracked units for both files, got a=%v b=%v", unitsA, unitsB)
	}
	citedJSON, err := json.Marshal(unitsA)
	if err != nil {
		t.Fatalf("encode cited units: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO thin_vault_noetic(
			noetic_id, occurred_at, status, query_text, answer_text, cited_unit_ids_json, cited_sentences_json
		) VALUES ('NOETIC-CITED', 1, 'active', 'q', 'a', ?, '[]')`, string(citedJSON)); err != nil {
		t.Fatalf("insert cited Noetic: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO thin_vault_noetic(
			noetic_id, occurred_at, status, query_text, answer_text, cited_unit_ids_json, cited_sentences_json
		) VALUES ('NOETIC-UNRELATED', 1, 'active', 'q', 'a', '["bg:elsewhere#0"]', '[]')`); err != nil {
		t.Fatalf("insert unrelated Noetic: %v", err)
	}

	removed, err := client.RemoveBackgroundDir(ctx, root)
	if err != nil {
		t.Fatalf("RemoveBackgroundDir: %v", err)
	}
	if len(removed) != len(unitsA)+len(unitsB) {
		t.Fatalf("removed %d units, want %d", len(removed), len(unitsA)+len(unitsB))
	}

	mounts, err := client.Vault.ListBackgroundDirs()
	if err != nil || len(mounts) != 0 {
		t.Fatalf("mount still present after removal: mounts=%+v err=%v", mounts, err)
	}
	for _, path := range []string{pathA, pathB} {
		if got := countBackgroundRows(t, db, `SELECT COUNT(*) FROM thin_vault_bg_files WHERE abs_path = ?`, path); got != 0 {
			t.Fatalf("file metadata retained for %s: %d", path, got)
		}
		if got := countBackgroundRows(t, db, `SELECT COUNT(*) FROM thin_vault_bg_chunks WHERE abs_path = ?`, path); got != 0 {
			t.Fatalf("chunk metadata retained for %s: %d", path, got)
		}
	}
	for _, unitID := range append(append([]string(nil), unitsA...), unitsB...) {
		if got := countBackgroundRows(t, db, `SELECT COUNT(*) FROM thin_vault_fts_map WHERE doc_id = ?`, unitID); got != 0 {
			t.Fatalf("FTS row retained for %s: %d", unitID, got)
		}
	}
	if got := loadNoeticStatus(t, db, "NOETIC-CITED"); got != "stale" {
		t.Fatalf("cited Noetic status = %q, want stale", got)
	}
	if got := loadNoeticStatus(t, db, "NOETIC-UNRELATED"); got != "active" {
		t.Fatalf("unrelated Noetic status = %q, want active", got)
	}
}

func loadNoeticStatus(t *testing.T, db *sql.DB, noeticID string) string {
	t.Helper()
	var status string
	if err := db.QueryRow(`SELECT status FROM thin_vault_noetic WHERE noetic_id = ?`, noeticID).Scan(&status); err != nil {
		t.Fatalf("load noetic status for %s: %v", noeticID, err)
	}
	return status
}

func TestBackgroundSyncDeletedFilePurgesBeforeLocalDeleteAndRetries(t *testing.T) {
	root := newBackgroundTestDir(t)
	path := filepath.Join(root, "deleted.md")
	if err := os.WriteFile(path, []byte("delete this unit only after cloud purge succeeds"), 0o600); err != nil {
		t.Fatalf("write deleted document: %v", err)
	}

	purgeCalls := 0
	var metadataAtPurge []int
	transport := &backgroundTransportStub{}
	client, db := newBackgroundTestClient(t, transport)
	transport.deleteUnits = func(_ context.Context, unitIDs []string) error {
		purgeCalls++
		if len(unitIDs) != 1 {
			t.Fatalf("expected one deleted unit, got %v", unitIDs)
		}
		metadataAtPurge = append(metadataAtPurge, countBackgroundRows(t, db, `SELECT COUNT(*) FROM thin_vault_bg_files WHERE abs_path = ?`, path))
		if purgeCalls == 1 {
			return errors.New("deleted file purge failed")
		}
		return nil
	}
	if _, err := client.AddBackgroundDir(root); err != nil {
		t.Fatalf("AddBackgroundDir: %v", err)
	}
	if _, err := client.SyncBackgroundDirs(context.Background()); err != nil {
		t.Fatalf("initial SyncBackgroundDirs: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove source document: %v", err)
	}

	if _, err := client.SyncBackgroundDirs(context.Background()); err == nil || !strings.Contains(err.Error(), "deleted file purge failed") {
		t.Fatalf("expected deleted file purge error, got %v", err)
	}
	if got := countBackgroundRows(t, db, `SELECT COUNT(*) FROM thin_vault_bg_files WHERE abs_path = ?`, path); got != 1 {
		t.Fatalf("failed deletion purge removed retry metadata: %d", got)
	}
	report, err := client.SyncBackgroundDirs(context.Background())
	if err != nil {
		t.Fatalf("deleted file purge retry: %v", err)
	}
	if report.DeletedFiles != 1 || report.DeletedChunks != 1 || purgeCalls != 2 {
		t.Fatalf("unexpected delete retry report=%+v purgeCalls=%d", report, purgeCalls)
	}
	if len(metadataAtPurge) != 2 || metadataAtPurge[0] != 1 || metadataAtPurge[1] != 1 {
		t.Fatalf("local metadata was deleted before cloud purge: %v", metadataAtPurge)
	}
	if got := countBackgroundRows(t, db, `SELECT COUNT(*) FROM thin_vault_bg_files WHERE abs_path = ?`, path); got != 0 {
		t.Fatalf("deleted file metadata remains after successful purge: %d", got)
	}
}

func TestBackgroundSyncShrinkPurgesTrailingChunkBeforeLocalDelete(t *testing.T) {
	root := newBackgroundTestDir(t)
	path := filepath.Join(root, "shrink.txt")
	// Each line must carry more than half the 1024-token chunk budget so the document splits
	// into exactly two chunks and the shrink path has a trailing chunk to purge.
	longLine := strings.Repeat("本地记忆检索预算验证单元 ", 140)
	if err := os.WriteFile(path, []byte(longLine+"\n"+longLine+"\n"), 0o600); err != nil {
		t.Fatalf("write two-chunk document: %v", err)
	}

	purgeCalls := 0
	var chunksAtPurge []int
	var purgedUnitIDs []string
	transport := &backgroundTransportStub{}
	client, db := newBackgroundTestClient(t, transport)
	transport.deleteUnits = func(_ context.Context, unitIDs []string) error {
		purgeCalls++
		purgedUnitIDs = append(purgedUnitIDs, unitIDs...)
		chunksAtPurge = append(chunksAtPurge, countBackgroundRows(t, db, `SELECT COUNT(*) FROM thin_vault_bg_chunks WHERE abs_path = ?`, path))
		if purgeCalls == 1 {
			return errors.New("shrink purge failed")
		}
		return nil
	}
	if _, err := client.AddBackgroundDir(root); err != nil {
		t.Fatalf("AddBackgroundDir: %v", err)
	}
	if _, err := client.SyncBackgroundDirs(context.Background()); err != nil {
		t.Fatalf("initial SyncBackgroundDirs: %v", err)
	}
	oldUnitIDs := loadBackgroundUnitIDs(t, db, path)
	if len(oldUnitIDs) != 2 {
		t.Fatalf("test setup expected two chunks, got %v", oldUnitIDs)
	}
	if err := os.WriteFile(path, []byte("one short chunk\n"), 0o600); err != nil {
		t.Fatalf("shrink document: %v", err)
	}

	if _, err := client.SyncBackgroundDirs(context.Background()); err == nil || !strings.Contains(err.Error(), "shrink purge failed") {
		t.Fatalf("expected shrink purge error, got %v", err)
	}
	if got := loadBackgroundUnitIDs(t, db, path); len(got) != 2 {
		t.Fatalf("failed shrink purge advanced synced metadata: %v", got)
	}
	report, err := client.SyncBackgroundDirs(context.Background())
	if err != nil {
		t.Fatalf("shrink purge retry: %v", err)
	}
	if report.IngestedFiles != 1 || purgeCalls != 2 {
		t.Fatalf("unexpected shrink retry report=%+v purgeCalls=%d", report, purgeCalls)
	}
	if len(chunksAtPurge) != 2 || chunksAtPurge[0] != 2 || chunksAtPurge[1] != 2 {
		t.Fatalf("trailing local chunk was deleted before purge: %v", chunksAtPurge)
	}
	if len(purgedUnitIDs) != 2 || purgedUnitIDs[0] != oldUnitIDs[1] || purgedUnitIDs[1] != oldUnitIDs[1] {
		t.Fatalf("wrong trailing units purged: got=%v want=%v", purgedUnitIDs, oldUnitIDs[1:])
	}
	newUnitIDs := loadBackgroundUnitIDs(t, db, path)
	if len(newUnitIDs) != 1 || newUnitIDs[0] != oldUnitIDs[0] {
		t.Fatalf("unexpected shrunk synced units: %v", newUnitIDs)
	}
	if got := countBackgroundRows(t, db, `SELECT COUNT(*) FROM thin_vault_bg_chunks WHERE abs_path = ?`, path); got != 1 {
		t.Fatalf("trailing chunk metadata remains after successful purge: %d", got)
	}
}

func TestBackgroundSyncMountStatFailureSkipsDeletion(t *testing.T) {
	root := newBackgroundTestDir(t)
	path := filepath.Join(root, "stat.md")
	if err := os.WriteFile(path, []byte("mount stat failure must not trigger deletion"), 0o600); err != nil {
		t.Fatalf("write stat document: %v", err)
	}
	purgeCalls := 0
	transport := &backgroundTransportStub{deleteUnits: func(_ context.Context, _ []string) error {
		purgeCalls++
		return nil
	}}
	client, db := newBackgroundTestClient(t, transport)
	if _, err := client.AddBackgroundDir(root); err != nil {
		t.Fatalf("AddBackgroundDir: %v", err)
	}
	if _, err := client.SyncBackgroundDirs(context.Background()); err != nil {
		t.Fatalf("initial SyncBackgroundDirs: %v", err)
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatalf("remove mount root: %v", err)
	}
	if _, err := client.SyncBackgroundDirs(context.Background()); err == nil || !strings.Contains(err.Error(), "stat background mount") {
		t.Fatalf("expected mount stat error, got %v", err)
	}
	if purgeCalls != 0 {
		t.Fatalf("scan failure entered deletion phase: purgeCalls=%d", purgeCalls)
	}
	if got := countBackgroundRows(t, db, `SELECT COUNT(*) FROM thin_vault_bg_files WHERE abs_path = ?`, path); got != 1 {
		t.Fatalf("scan failure deleted synced metadata: %d", got)
	}
}

func TestInvalidateLocalNoeticByCitedDocReturnsJSONError(t *testing.T) {
	client, db := newBackgroundTestClient(t, &backgroundTransportStub{})
	if _, err := db.Exec(`
		INSERT INTO thin_vault_noetic(
			noetic_id, occurred_at, status, query_text, answer_text, cited_unit_ids_json, cited_sentences_json
		) VALUES ('BROKEN-NOETIC', 1, 'active', 'q', 'a', '{broken', '[]')`); err != nil {
		t.Fatalf("insert malformed Noetic citation: %v", err)
	}
	if _, err := client.Vault.InvalidateLocalNoeticByCitedDoc("bg:broken#0"); err == nil || !strings.Contains(err.Error(), "decode cited units") {
		t.Fatalf("expected citation JSON error, got %v", err)
	}
}

// TestBackgroundSyncChunkLevelDelta 验证文件级修改不再整文件重发：
// 只改中间一个段落时，仅变化块上行，未变块沿用原 unit 与云端回执。
func TestBackgroundSyncChunkLevelDelta(t *testing.T) {
	root := filepath.Join(t.TempDir(), "chunk_delta")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}
	section := func(seed string) string {
		base := seed + "记忆引擎在本地完成盲化编码云端只负责计算波形与胞体地址任何明文都不会离开本机"
		var b strings.Builder
		// A section must span a full chunk on its own so the middle-section edit re-ingests a
		// strict subset of the file's chunks.
		for b.Len() < 3200 {
			b.WriteString(base)
		}
		return b.String()
	}
	path := filepath.Join(root, "large.md")
	original := "# 第一章\n" + section("甲") + "\n\n# 第二章\n" + section("乙") + "\n\n# 第三章\n" + section("丙") + "\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatalf("write original: %v", err)
	}

	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "chunk_delta.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	vault, err := thinclient.NewVault(db, []byte("chunk-delta-secret-2026"))
	if err != nil {
		t.Fatalf("NewVault: %v", err)
	}
	client := thinclient.NewClient(vault, thinclient.NewLocalVaultTransport(vault))
	ctx := context.Background()
	if _, err := client.AddBackgroundDir(root); err != nil {
		t.Fatalf("AddBackgroundDir: %v", err)
	}

	first, err := client.SyncBackgroundDirs(ctx)
	if err != nil {
		t.Fatalf("initial sync: %v", err)
	}
	total := first.IngestedChunks
	if total < 3 {
		t.Fatalf("expected multiple chunks, got %d", total)
	}

	// Replace only the middle section with same-length different text.
	time.Sleep(10 * time.Millisecond)
	edited := "# 第一章\n" + section("甲") + "\n\n# 第二章\n" + section("丁") + "\n\n# 第三章\n" + section("丙") + "\n"
	if err := os.WriteFile(path, []byte(edited), 0o600); err != nil {
		t.Fatalf("write edited: %v", err)
	}

	second, err := client.SyncBackgroundDirs(ctx)
	if err != nil {
		t.Fatalf("delta sync: %v", err)
	}
	if second.IngestedFiles != 1 {
		t.Fatalf("delta sync should touch 1 file, got %+v", second)
	}
	if second.IngestedChunks <= 0 || second.IngestedChunks >= total {
		t.Fatalf("chunk delta failed: re-ingested %d of %d chunks (want 0 < n < total)", second.IngestedChunks, total)
	}

	// Third sync with no changes must send nothing.
	third, err := client.SyncBackgroundDirs(ctx)
	if err != nil {
		t.Fatalf("no-op sync: %v", err)
	}
	if third.IngestedChunks != 0 || third.UnchangedFiles != 1 {
		t.Fatalf("no-op sync should send 0 chunks, got %+v", third)
	}
}

// TestBackgroundSyncRechunksWhenChunkerGenerationChanges 验证切块规则代次是统计快路径的一部分：
// 由旧规则写下的清单在下一次同步必须整文件重新切块，而不是被 mtime/size 快路径继续信任。
func TestBackgroundSyncRechunksWhenChunkerGenerationChanges(t *testing.T) {
	root := newBackgroundTestDir(t)
	path := filepath.Join(root, "rule.md")
	var doc strings.Builder
	// Enough distinct text to span several chunks, so re-chunking under a different rule really
	// changes the boundary set (a single-chunk file would re-chunk to the identical chunk).
	for i := 0; i < 300; i++ {
		fmt.Fprintf(&doc, "第%d行 切块规则代次变化必须触发重新分块，而不是沿用旧边界继续信任。\n", i)
	}
	if err := os.WriteFile(path, []byte(doc.String()), 0o600); err != nil {
		t.Fatalf("write document: %v", err)
	}
	client, db := newBackgroundTestClient(t, &backgroundTransportStub{})
	ctx := context.Background()
	if _, err := client.AddBackgroundDir(root); err != nil {
		t.Fatalf("AddBackgroundDir: %v", err)
	}
	first, err := client.SyncBackgroundDirs(ctx)
	if err != nil {
		t.Fatalf("initial sync: %v", err)
	}
	if first.IngestedFiles != 1 || first.IngestedChunks < 2 {
		t.Fatalf("initial sync expected a multi-chunk file, got %+v", first)
	}
	second, err := client.SyncBackgroundDirs(ctx)
	if err != nil {
		t.Fatalf("no-op sync: %v", err)
	}
	if second.UnchangedFiles != 1 || second.IngestedChunks != 0 {
		t.Fatalf("unchanged file must stay on the fast path, got %+v", second)
	}

	// Simulate an inventory written by an older chunking rule: the chunk count still lines up, but
	// the file-level and per-chunk hashes describe other boundaries than the current rule's.
	var chunkCount int
	if err := db.QueryRow(`SELECT chunk_count FROM thin_vault_bg_files WHERE abs_path = ?`, path).Scan(&chunkCount); err != nil {
		t.Fatalf("read chunk count: %v", err)
	}
	staleHashes := make([]string, chunkCount)
	for i := range staleHashes {
		staleHashes[i] = fmt.Sprintf("stale-rule-%d", i)
	}
	staleJSON, err := json.Marshal(staleHashes)
	if err != nil {
		t.Fatalf("encode stale hashes: %v", err)
	}
	if _, err := db.Exec(
		`UPDATE thin_vault_bg_files SET chunker_generation = 'carrier/16000', content_sha256 = 'stale-rule', chunk_hashes_json = ? WHERE abs_path = ?`,
		string(staleJSON), path,
	); err != nil {
		t.Fatalf("age chunker generation: %v", err)
	}
	third, err := client.SyncBackgroundDirs(ctx)
	if err != nil {
		t.Fatalf("re-chunk sync: %v", err)
	}
	if third.IngestedFiles != 1 || third.IngestedChunks != first.IngestedChunks {
		t.Fatalf("stale generation must re-chunk the file, got %+v", third)
	}
	var stamped string
	if err := db.QueryRow(`SELECT chunker_generation FROM thin_vault_bg_files WHERE abs_path = ?`, path).Scan(&stamped); err != nil {
		t.Fatalf("read chunker generation: %v", err)
	}
	if stamped != "" && stamped == "carrier/16000" {
		t.Fatalf("stored chunker generation was not refreshed: %q", stamped)
	}
	fourth, err := client.SyncBackgroundDirs(ctx)
	if err != nil {
		t.Fatalf("post re-chunk sync: %v", err)
	}
	if fourth.UnchangedFiles != 1 || fourth.IngestedChunks != 0 {
		t.Fatalf("current generation must return to the fast path, got %+v", fourth)
	}
}

// TestBackgroundSyncUploadsChangedFilesConcurrently 验证首挂上行并发化：多个文件的块上行必须真正重叠，
// 否则闸门超时返回错误、同步判失败（并发池退化为串行时即命中该失败路径）。
func TestBackgroundSyncUploadsChangedFilesConcurrently(t *testing.T) {
	root := newBackgroundTestDir(t)
	const fileCount = 4
	for i := 0; i < fileCount; i++ {
		path := filepath.Join(root, fmt.Sprintf("concurrent_%d.md", i))
		if err := os.WriteFile(path, []byte(fmt.Sprintf("concurrent upload document %d", i)), 0o600); err != nil {
			t.Fatalf("write concurrent document %d: %v", i, err)
		}
	}

	var inFlight, peakInFlight atomic.Int64
	release := make(chan struct{})
	var releaseOnce sync.Once
	transport := &backgroundTransportStub{}
	transport.ingest = func(_ context.Context, pkt thinclient.BlindPulsePacket) (thinclient.CloudIngestResponse, error) {
		current := inFlight.Add(1)
		defer inFlight.Add(-1)
		for {
			peak := peakInFlight.Load()
			if current <= peak || peakInFlight.CompareAndSwap(peak, current) {
				break
			}
		}
		if current >= 2 {
			releaseOnce.Do(func() { close(release) })
		}
		select {
		case <-release:
		case <-time.After(10 * time.Second):
			return thinclient.CloudIngestResponse{}, errors.New("background chunk uploads stayed serial")
		}
		return thinclient.CloudIngestResponse{UnitID: pkt.UnitID, Layer: pkt.Layer}, nil
	}
	client, _ := newBackgroundTestClient(t, transport)
	if _, err := client.AddBackgroundDir(root); err != nil {
		t.Fatalf("AddBackgroundDir: %v", err)
	}

	report, err := client.SyncBackgroundDirs(context.Background())
	if err != nil {
		t.Fatalf("concurrent SyncBackgroundDirs: %v", err)
	}
	if peakInFlight.Load() < 2 {
		t.Fatalf("expected overlapping chunk uploads, peak in flight=%d", peakInFlight.Load())
	}
	if report.IngestedFiles != fileCount || report.IngestedChunks != fileCount {
		t.Fatalf("unexpected concurrent sync report: %+v", report)
	}
}

// backgroundUnitIDForPath mirrors the path+index unit ID shape of a background chunk ingest, so a
// transport stub can single out one file without inspecting the blind payload.
func backgroundUnitIDForPath(t *testing.T, absPath string, chunkIndex int) string {
	t.Helper()
	sum := sha256.Sum256([]byte(filepath.Clean(absPath)))
	return fmt.Sprintf("bg:%s#%d", hex.EncodeToString(sum[:6]), chunkIndex)
}

// TestBackgroundSyncFirstFailureByWalkOrderCommitsEarlierFiles 验证并发上行不改变串行错误语义：
// 按扫描序取首个失败文件报错、其前文件照常提交、失败文件整文件原子丢弃（零元数据、重试收敛）。
// 云侧不变量：失败中止时，失败点之后「已上行但未提交」的尾部单元必须被回滚清除——桩让失败请求
// 延迟返回，使后继文件确定性地先被派发并上行，因此去掉回滚的回归会让该判据变红。
func TestBackgroundSyncFirstFailureByWalkOrderCommitsEarlierFiles(t *testing.T) {
	root := newBackgroundTestDir(t)
	const files = 8
	const failIndex = 1
	paths := make([]string, 0, files)
	unitOwners := map[string]int{}
	for i := 0; i < files; i++ {
		p := filepath.Join(root, fmt.Sprintf("%02d-doc.md", i+1))
		if err := os.WriteFile(p, []byte(fmt.Sprintf("staged document %02d for the walk-order failure test", i+1)), 0o600); err != nil {
			t.Fatalf("write document %d: %v", i+1, err)
		}
		paths = append(paths, p)
		unitOwners[backgroundUnitIDForPath(t, p, 0)] = i
	}
	failPath := paths[failIndex]
	failUnitID := backgroundUnitIDForPath(t, failPath, 0)

	var rejectFail atomic.Bool
	rejectFail.Store(true)
	var cloud sync.Map // file index → struct{}: units the stub actually accepted
	var purged sync.Map
	transport := &backgroundTransportStub{}
	transport.ingest = func(_ context.Context, pkt thinclient.BlindPulsePacket) (thinclient.CloudIngestResponse, error) {
		owner, known := unitOwners[pkt.UnitID]
		if rejectFail.Load() && pkt.UnitID == failUnitID {
			// Delay the rejection so the other workers finish their files (and pull the next ones)
			// before the failure is observed: the rollback then has a tail to purge, and a
			// regression that drops the rollback leaves those units live in the cloud.
			time.Sleep(300 * time.Millisecond)
			return thinclient.CloudIngestResponse{}, errors.New("beta ingest rejected")
		}
		if known {
			cloud.Store(owner, struct{}{})
		}
		return thinclient.CloudIngestResponse{UnitID: pkt.UnitID, Layer: pkt.Layer}, nil
	}
	transport.deleteUnits = func(_ context.Context, unitIDs []string) error {
		for _, id := range unitIDs {
			if owner, ok := unitOwners[id]; ok {
				purged.Store(owner, struct{}{})
			}
		}
		return nil
	}
	client, db := newBackgroundTestClient(t, transport)
	if _, err := client.AddBackgroundDir(root); err != nil {
		t.Fatalf("AddBackgroundDir: %v", err)
	}

	report, err := client.SyncBackgroundDirs(context.Background())
	if err == nil || !strings.Contains(err.Error(), "beta ingest rejected") || !strings.Contains(err.Error(), failUnitID) {
		t.Fatalf("expected the first failing file to be reported, got report=%+v err=%v", report, err)
	}
	if report.IngestedFiles != failIndex || report.IngestedChunks != failIndex {
		t.Fatalf("files before the failure must commit and the failed file must not: %+v", report)
	}
	for i, p := range paths {
		want := 0
		if i < failIndex {
			want = 1
		}
		if got := countBackgroundRows(t, db, `SELECT COUNT(*) FROM thin_vault_bg_files WHERE abs_path = ?`, p); got != want {
			t.Fatalf("file %d synced rows = %d, want %d", i, got, want)
		}
	}
	// Cloud-side invariant: after the failed sync the accepted units are exactly the committed
	// prefix — every uploaded-but-uncommitted tail file was purged.
	liveOrphans := []int{}
	cloud.Range(func(key, _ any) bool {
		owner := key.(int)
		if owner >= failIndex {
			if _, wasPurged := purged.Load(owner); !wasPurged {
				liveOrphans = append(liveOrphans, owner)
			}
		}
		return true
	})
	if len(liveOrphans) != 0 {
		t.Fatalf("failed sync left uploaded-but-uncommitted files live in the cloud: %v", liveOrphans)
	}

	rejectFail.Store(false)
	report, err = client.SyncBackgroundDirs(context.Background())
	if err != nil || report.IngestedFiles != files-failIndex || report.UnchangedFiles != failIndex {
		t.Fatalf("retry after recovery: report=%+v err=%v", report, err)
	}
	for i, p := range paths {
		if got := countBackgroundRows(t, db, `SELECT COUNT(*) FROM thin_vault_bg_files WHERE abs_path = ?`, p); got != 1 {
			t.Fatalf("file %d did not converge after retry: rows=%d", i, got)
		}
	}
}

// TestBackgroundSyncSkipsOversizedFileAndKeepsMountSyncable 验证超限文件按文件降级：整棵挂载照常
// 完成，超限文件既不进入索引也不触发任何上行，报告如实列出跳过文件名与原因；下一拍重复上报且不再
// 触网。回归：单文件「exceeds cap」曾整树 abort，而该文件永不缩小时，挂载会永久卡死。
func TestBackgroundSyncSkipsOversizedFileAndKeepsMountSyncable(t *testing.T) {
	root := newBackgroundTestDir(t)
	kept := filepath.Join(root, "kept.md")
	if err := os.WriteFile(kept, []byte("kept background document survives an oversized sibling"), 0o600); err != nil {
		t.Fatalf("write kept document: %v", err)
	}
	huge := filepath.Join(root, "huge.json")
	f, err := os.Create(huge)
	if err != nil {
		t.Fatalf("create oversized document: %v", err)
	}
	if err := f.Truncate(thinclient.MaxTextFileSizeBytes + 1); err != nil {
		t.Fatalf("grow oversized document: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close oversized document: %v", err)
	}

	var ingestCalls atomic.Int64
	transport := &backgroundTransportStub{}
	transport.ingest = func(_ context.Context, pkt thinclient.BlindPulsePacket) (thinclient.CloudIngestResponse, error) {
		ingestCalls.Add(1)
		return thinclient.CloudIngestResponse{UnitID: pkt.UnitID, Layer: pkt.Layer}, nil
	}
	client, db := newBackgroundTestClient(t, transport)
	if _, err := client.AddBackgroundDir(root); err != nil {
		t.Fatalf("AddBackgroundDir: %v", err)
	}

	report, err := client.SyncBackgroundDirs(context.Background())
	if err != nil {
		t.Fatalf("oversized file must not fail the mount sync: %v", err)
	}
	if report.IngestedFiles != 1 || len(report.SkippedFiles) != 1 {
		t.Fatalf("expected one ingested file and one skip, got %+v", report)
	}
	skip := report.SkippedFiles[0]
	if skip.AbsPath != huge || !strings.Contains(skip.Reason, "exceeds") {
		t.Fatalf("unexpected skip record: %+v", skip)
	}
	if got := countBackgroundRows(t, db, `SELECT COUNT(*) FROM thin_vault_bg_files WHERE abs_path = ?`, huge); got != 0 {
		t.Fatalf("oversized file entered the index: %d", got)
	}
	if got := countBackgroundRows(t, db, `SELECT COUNT(*) FROM thin_vault_bg_files WHERE abs_path = ?`, kept); got != 1 {
		t.Fatalf("kept file missing from the index: %d", got)
	}
	callsAfterFirst := ingestCalls.Load()
	if callsAfterFirst == 0 {
		t.Fatalf("kept document never reached the transport")
	}

	// The skip is steady state: re-reported every pass, without any new upload or local row.
	report, err = client.SyncBackgroundDirs(context.Background())
	if err != nil || len(report.SkippedFiles) != 1 || report.UnchangedFiles != 1 {
		t.Fatalf("steady-state sync: report=%+v err=%v", report, err)
	}
	if ingestCalls.Load() != callsAfterFirst {
		t.Fatalf("steady-state sync re-uploaded: calls=%d want=%d", ingestCalls.Load(), callsAfterFirst)
	}
}

// TestBackgroundSyncSkipsEmptyFileAndKeepsMountSyncable 验证 0 字节受支持文件按文件跳过，不再以
// 「returned no chunks」中止整树。回归：任何挂载只要含一个空 .txt/.md（例如 testdata 里成片的
// 空期望文件），同步就永远完不成。
func TestBackgroundSyncSkipsEmptyFileAndKeepsMountSyncable(t *testing.T) {
	root := newBackgroundTestDir(t)
	empty := filepath.Join(root, "empty.txt")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatalf("write empty document: %v", err)
	}
	kept := filepath.Join(root, "kept.md")
	if err := os.WriteFile(kept, []byte("kept background document beside an empty sibling"), 0o600); err != nil {
		t.Fatalf("write kept document: %v", err)
	}
	client, db := newBackgroundTestClient(t, &backgroundTransportStub{})
	if _, err := client.AddBackgroundDir(root); err != nil {
		t.Fatalf("AddBackgroundDir: %v", err)
	}
	report, err := client.SyncBackgroundDirs(context.Background())
	if err != nil {
		t.Fatalf("empty file must not fail the mount sync: %v", err)
	}
	if report.IngestedFiles != 1 || len(report.SkippedFiles) != 1 || report.SkippedFiles[0].Reason != "no extractable text" {
		t.Fatalf("expected one ingest and one no-text skip, got %+v", report)
	}
	if got := countBackgroundRows(t, db, `SELECT COUNT(*) FROM thin_vault_bg_files WHERE abs_path = ?`, empty); got != 0 {
		t.Fatalf("empty file entered the index: %d", got)
	}
}

// TestBackgroundSyncPurgesTrackedFileThatBecomesOversized 验证超限清理语义：文件在上一拍已索引、
// 本拍涨过上限后，旧的行与云端单元经同一个删除式清理移除（不再继续服务本拍无法核实的内容行），
// 且同步整体成功、不计入 DeletedFiles（文件并未删除，只是不再索引）。
func TestBackgroundSyncPurgesTrackedFileThatBecomesOversized(t *testing.T) {
	root := newBackgroundTestDir(t)
	path := filepath.Join(root, "grow.txt")
	if err := os.WriteFile(path, []byte("small enough to index on the first pass"), 0o600); err != nil {
		t.Fatalf("write growable document: %v", err)
	}
	transport := &backgroundTransportStub{}
	client, db := newBackgroundTestClient(t, transport)
	if _, err := client.AddBackgroundDir(root); err != nil {
		t.Fatalf("AddBackgroundDir: %v", err)
	}
	if _, err := client.SyncBackgroundDirs(context.Background()); err != nil {
		t.Fatalf("initial SyncBackgroundDirs: %v", err)
	}
	unitIDs := loadBackgroundUnitIDs(t, db, path)
	if len(unitIDs) == 0 {
		t.Fatalf("initial pass indexed no units")
	}

	var purgedMu sync.Mutex
	var purged []string
	transport.deleteUnits = func(_ context.Context, ids []string) error {
		purgedMu.Lock()
		purged = append(purged, ids...)
		purgedMu.Unlock()
		return nil
	}

	f, err := os.OpenFile(path, os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open growable document: %v", err)
	}
	if err := f.Truncate(thinclient.MaxTextFileSizeBytes + 1); err != nil {
		t.Fatalf("grow document: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close growable document: %v", err)
	}

	report, err := client.SyncBackgroundDirs(context.Background())
	if err != nil {
		t.Fatalf("oversized rewrite must not fail the mount sync: %v", err)
	}
	if len(report.SkippedFiles) != 1 {
		t.Fatalf("expected one skip, got %+v", report)
	}
	if report.DeletedFiles != 0 || report.DeletedChunks != 0 {
		t.Fatalf("skip purge must not be reported as a deletion: %+v", report)
	}
	if got := countBackgroundRows(t, db, `SELECT COUNT(*) FROM thin_vault_bg_files WHERE abs_path = ?`, path); got != 0 {
		t.Fatalf("stale file row survived the skip purge: %d", got)
	}
	if got := countBackgroundRows(t, db, `SELECT COUNT(*) FROM thin_vault_bg_chunks WHERE abs_path = ?`, path); got != 0 {
		t.Fatalf("stale chunks survived the skip purge: %d", got)
	}
	purgedMu.Lock()
	defer purgedMu.Unlock()
	if len(purged) != len(unitIDs) {
		t.Fatalf("purged units = %v, want %v", purged, unitIDs)
	}
	for i, id := range unitIDs {
		if purged[i] != id {
			t.Fatalf("purged units = %v, want %v", purged, unitIDs)
		}
	}
}

// TestBackgroundSyncUnreadableDirKeepsTrackedRows 验证「目录不可枚举」既不让整树失败、也不把其中
// 已索引文件误当删除：行保留、零云端清理，等目录恢复可读后同一拍自然收敛（UnreadableDirs 清空）。
func TestBackgroundSyncUnreadableDirKeepsTrackedRows(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permission bits")
	}
	root := newBackgroundTestDir(t)
	locked := filepath.Join(root, "locked")
	if err := os.MkdirAll(locked, 0o755); err != nil {
		t.Fatalf("mkdir locked dir: %v", err)
	}
	deep := filepath.Join(locked, "deep.md")
	if err := os.WriteFile(deep, []byte("deep document inside a directory that later turns unreadable"), 0o600); err != nil {
		t.Fatalf("write deep document: %v", err)
	}
	top := filepath.Join(root, "top.md")
	if err := os.WriteFile(top, []byte("top-level document beside the locked directory"), 0o600); err != nil {
		t.Fatalf("write top document: %v", err)
	}

	purgeCalls := 0
	transport := &backgroundTransportStub{deleteUnits: func(_ context.Context, _ []string) error {
		purgeCalls++
		return nil
	}}
	client, db := newBackgroundTestClient(t, transport)
	if _, err := client.AddBackgroundDir(root); err != nil {
		t.Fatalf("AddBackgroundDir: %v", err)
	}
	if _, err := client.SyncBackgroundDirs(context.Background()); err != nil {
		t.Fatalf("initial SyncBackgroundDirs: %v", err)
	}

	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatalf("lock directory: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })

	report, err := client.SyncBackgroundDirs(context.Background())
	if err != nil {
		t.Fatalf("unreadable nested dir must not fail the mount sync: %v", err)
	}
	if len(report.UnreadableDirs) != 1 || report.UnreadableDirs[0] != locked {
		t.Fatalf("expected the locked dir to be reported, got %+v", report)
	}
	if report.DeletedFiles != 0 || purgeCalls != 0 {
		t.Fatalf("locked directory's absence was treated as deletion: report=%+v purgeCalls=%d", report, purgeCalls)
	}
	if got := countBackgroundRows(t, db, `SELECT COUNT(*) FROM thin_vault_bg_files WHERE abs_path = ?`, deep); got != 1 {
		t.Fatalf("tracked row under the locked dir was purged: %d", got)
	}

	if err := os.Chmod(locked, 0o755); err != nil {
		t.Fatalf("unlock directory: %v", err)
	}
	report, err = client.SyncBackgroundDirs(context.Background())
	if err != nil || len(report.UnreadableDirs) != 0 || report.UnchangedFiles != 2 {
		t.Fatalf("post-unlock sync: report=%+v err=%v", report, err)
	}
}

// TestBackgroundSyncDanglingSymlinkDoesNotFailMount 验证受支持扩展名的悬空软链（或扫描间隙消失
// 的文件）按缺席处理：不失败整树、不进入索引，其余文件照常完成。
func TestBackgroundSyncDanglingSymlinkDoesNotFailMount(t *testing.T) {
	root := newBackgroundTestDir(t)
	kept := filepath.Join(root, "kept.md")
	if err := os.WriteFile(kept, []byte("kept document beside a dangling symlink"), 0o600); err != nil {
		t.Fatalf("write kept document: %v", err)
	}
	ghost := filepath.Join(root, "ghost.md")
	if err := os.Symlink(filepath.Join(root, "missing-target.md"), ghost); err != nil {
		t.Fatalf("create dangling symlink: %v", err)
	}
	client, db := newBackgroundTestClient(t, &backgroundTransportStub{})
	if _, err := client.AddBackgroundDir(root); err != nil {
		t.Fatalf("AddBackgroundDir: %v", err)
	}
	report, err := client.SyncBackgroundDirs(context.Background())
	if err != nil {
		t.Fatalf("dangling symlink must not fail the mount sync: %v", err)
	}
	if report.IngestedFiles != 1 {
		t.Fatalf("expected the real document to ingest, got %+v", report)
	}
	if got := countBackgroundRows(t, db, `SELECT COUNT(*) FROM thin_vault_bg_files WHERE abs_path = ?`, ghost); got != 0 {
		t.Fatalf("dangling symlink entered the index: %d", got)
	}
}

// backgroundMountByRoot returns the mounted directory whose canonical root equals root.
func backgroundMountByRoot(t *testing.T, client *thinclient.Client, root string) thinclient.BackgroundDirMount {
	t.Helper()
	mounts, err := client.Vault.ListBackgroundDirs()
	if err != nil {
		t.Fatalf("ListBackgroundDirs: %v", err)
	}
	for _, m := range mounts {
		if m.RootPath == root {
			return m
		}
	}
	t.Fatalf("mount %s not found in %+v", root, mounts)
	return thinclient.BackgroundDirMount{}
}

func seedActiveNoeticCitation(t *testing.T, db *sql.DB, noeticID string, unitIDs []string) {
	t.Helper()
	payload, err := json.Marshal(unitIDs)
	if err != nil {
		t.Fatalf("encode citation payload: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO thin_vault_noetic(noetic_id, occurred_at, inserted_at, status, query_text, answer_text, cited_unit_ids_json)
		VALUES (?, strftime('%s','now'), strftime('%s','now'), 'active', 'covered file question', 'covered file answer', ?)`,
		noeticID, string(payload)); err != nil {
		t.Fatalf("insert noetic citation: %v", err)
	}
}

func noeticStatusByID(t *testing.T, db *sql.DB, noeticID string) string {
	t.Helper()
	var status string
	if err := db.QueryRow(`SELECT status FROM thin_vault_noetic WHERE noetic_id = ?`, noeticID).Scan(&status); err != nil {
		t.Fatalf("load noetic status: %v", err)
	}
	return status
}

// TestBackgroundUnmountChildRekeepsFilesCoveredByParent 验证移除嵌套子挂载点不再误清父挂载仍覆盖
// 的文件：文件重归属到父挂载（dir_id 与展示路径更新），云单元、本地分块与 Noetic 引证原封不动，
// 下一次同步零重传。
func TestBackgroundUnmountChildRekeepsFilesCoveredByParent(t *testing.T) {
	parent := newBackgroundTestDir(t)
	child := filepath.Join(parent, "child")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatalf("mkdir child: %v", err)
	}
	parentDoc := filepath.Join(parent, "parent.md")
	childDoc := filepath.Join(child, "child.md")
	if err := os.WriteFile(parentDoc, []byte("parent note stays with the parent mount"), 0o600); err != nil {
		t.Fatalf("write parent doc: %v", err)
	}
	if err := os.WriteFile(childDoc, []byte("child note is covered by the parent mount too"), 0o600); err != nil {
		t.Fatalf("write child doc: %v", err)
	}

	var ingestCalls atomic.Int64
	var purgedUnits atomic.Int64
	transport := &backgroundTransportStub{}
	transport.ingest = func(_ context.Context, pkt thinclient.BlindPulsePacket) (thinclient.CloudIngestResponse, error) {
		ingestCalls.Add(1)
		return thinclient.CloudIngestResponse{UnitID: pkt.UnitID, Layer: pkt.Layer}, nil
	}
	transport.deleteUnits = func(_ context.Context, unitIDs []string) error {
		purgedUnits.Add(int64(len(unitIDs)))
		return nil
	}
	client, db := newBackgroundTestClient(t, transport)
	ctx := context.Background()
	if _, err := client.AddBackgroundDir(parent); err != nil {
		t.Fatalf("AddBackgroundDir(parent): %v", err)
	}
	if _, err := client.AddBackgroundDir(child); err != nil {
		t.Fatalf("AddBackgroundDir(child): %v", err)
	}
	if report, err := client.SyncBackgroundDirs(ctx); err != nil || report.IngestedFiles != 2 {
		t.Fatalf("initial sync: report=%+v err=%v", report, err)
	}
	parentMount := backgroundMountByRoot(t, client, parent)
	childMount := backgroundMountByRoot(t, client, child)
	var childOwner string
	if err := db.QueryRow(`SELECT dir_id FROM thin_vault_bg_files WHERE abs_path = ?`, childDoc).Scan(&childOwner); err != nil {
		t.Fatalf("load child owner: %v", err)
	}
	if childOwner != childMount.DirID {
		t.Fatalf("child file must bind to the more specific mount, got %s want %s", childOwner, childMount.DirID)
	}
	childUnit := loadBackgroundUnitIDs(t, db, childDoc)[0]
	seedActiveNoeticCitation(t, db, "NOETIC-COVERED-CHILD", []string{childUnit})

	report, err := client.RemoveBackgroundDirReport(ctx, child)
	if err != nil {
		t.Fatalf("RemoveBackgroundDirReport(child): %v", err)
	}
	if len(report.RemovedUnitIDs) != 0 || report.ReboundFiles != 1 {
		t.Fatalf("expected zero purges and one re-bound file, got %+v", report)
	}
	if purgedUnits.Load() != 0 {
		t.Fatalf("covered file reached the cloud purge: %d", purgedUnits.Load())
	}
	var owner, relPath string
	if err := db.QueryRow(`SELECT dir_id, rel_path FROM thin_vault_bg_files WHERE abs_path = ?`, childDoc).Scan(&owner, &relPath); err != nil {
		t.Fatalf("child file must survive the child unmount: %v", err)
	}
	if owner != parentMount.DirID || relPath != "child/child.md" {
		t.Fatalf("child file not re-attributed to the parent mount: dir=%s rel=%s", owner, relPath)
	}
	var chunkRel string
	if err := db.QueryRow(`SELECT rel_path FROM thin_vault_bg_chunks WHERE abs_path = ? LIMIT 1`, childDoc).Scan(&chunkRel); err != nil {
		t.Fatalf("load chunk rel_path: %v", err)
	}
	if chunkRel != "child/child.md" {
		t.Fatalf("chunk display path not refreshed: %s", chunkRel)
	}
	if status := noeticStatusByID(t, db, "NOETIC-COVERED-CHILD"); status != "active" {
		t.Fatalf("citation of a covered file must stay active, got %s", status)
	}
	callsBefore := ingestCalls.Load()
	if report, err := client.SyncBackgroundDirs(ctx); err != nil || report.UnchangedFiles != 2 || report.IngestedFiles != 0 {
		t.Fatalf("post-unmount sync must be a no-op: report=%+v err=%v", report, err)
	}
	if ingestCalls.Load() != callsBefore {
		t.Fatalf("post-unmount sync re-uploaded covered files: calls=%d want=%d", ingestCalls.Load(), callsBefore)
	}
	mounts, err := client.Vault.ListBackgroundDirs()
	if err != nil || len(mounts) != 1 || mounts[0].DirID != parentMount.DirID || mounts[0].FileCount != 2 {
		t.Fatalf("mount list after child unmount: %+v err=%v", mounts, err)
	}
}

// TestBackgroundUnmountParentPurgesOnlyUncoveredFiles 验证移除父挂载点时只有不再被任何挂载覆盖的
// 文件被回收：子挂载仍覆盖的文件保留其归属与引证，父挂载独有的文件走完整回收并把引证转 stale。
func TestBackgroundUnmountParentPurgesOnlyUncoveredFiles(t *testing.T) {
	parent := newBackgroundTestDir(t)
	child := filepath.Join(parent, "child")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatalf("mkdir child: %v", err)
	}
	parentDoc := filepath.Join(parent, "parent.md")
	childDoc := filepath.Join(child, "child.md")
	if err := os.WriteFile(parentDoc, []byte("parent note is purged with its only mount"), 0o600); err != nil {
		t.Fatalf("write parent doc: %v", err)
	}
	if err := os.WriteFile(childDoc, []byte("child note stays under the child mount"), 0o600); err != nil {
		t.Fatalf("write child doc: %v", err)
	}

	transport := &backgroundTransportStub{}
	client, db := newBackgroundTestClient(t, transport)
	ctx := context.Background()
	if _, err := client.AddBackgroundDir(parent); err != nil {
		t.Fatalf("AddBackgroundDir(parent): %v", err)
	}
	if _, err := client.AddBackgroundDir(child); err != nil {
		t.Fatalf("AddBackgroundDir(child): %v", err)
	}
	if report, err := client.SyncBackgroundDirs(ctx); err != nil || report.IngestedFiles != 2 {
		t.Fatalf("initial sync: report=%+v err=%v", report, err)
	}
	childMount := backgroundMountByRoot(t, client, child)
	parentUnit := loadBackgroundUnitIDs(t, db, parentDoc)[0]
	childUnit := loadBackgroundUnitIDs(t, db, childDoc)[0]
	seedActiveNoeticCitation(t, db, "NOETIC-PARENT-ONLY", []string{parentUnit})
	seedActiveNoeticCitation(t, db, "NOETIC-CHILD-KEPT", []string{childUnit})

	removedIDs, err := client.RemoveBackgroundDir(ctx, parent)
	if err != nil {
		t.Fatalf("RemoveBackgroundDir(parent): %v", err)
	}
	if len(removedIDs) != 1 || removedIDs[0] != parentUnit {
		t.Fatalf("expected only the parent-owned unit purged, got %v", removedIDs)
	}
	if got := countBackgroundRows(t, db, `SELECT COUNT(*) FROM thin_vault_bg_files WHERE abs_path = ?`, parentDoc); got != 0 {
		t.Fatalf("uncovered parent file survived: %d", got)
	}
	var owner string
	if err := db.QueryRow(`SELECT dir_id FROM thin_vault_bg_files WHERE abs_path = ?`, childDoc).Scan(&owner); err != nil {
		t.Fatalf("covered child file must survive: %v", err)
	}
	if owner != childMount.DirID {
		t.Fatalf("child file owner changed: got %s want %s", owner, childMount.DirID)
	}
	if status := noeticStatusByID(t, db, "NOETIC-PARENT-ONLY"); status != "stale" {
		t.Fatalf("citation of the purged file must go stale, got %s", status)
	}
	if status := noeticStatusByID(t, db, "NOETIC-CHILD-KEPT"); status != "active" {
		t.Fatalf("citation of the covered file must stay active, got %s", status)
	}
}

// TestBackgroundUnmountLayersPurgeOnlyAfterLastCoverage 验证顺序解挂两层挂载：先删子层零回收、文件
// 重归属父层；删掉最后一层时才真正回收全部单元并把引证转 stale。
func TestBackgroundUnmountLayersPurgeOnlyAfterLastCoverage(t *testing.T) {
	parent := newBackgroundTestDir(t)
	child := filepath.Join(parent, "child")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatalf("mkdir child: %v", err)
	}
	childDoc := filepath.Join(child, "child.md")
	if err := os.WriteFile(childDoc, []byte("layered mount note purged only at the last unmount"), 0o600); err != nil {
		t.Fatalf("write child doc: %v", err)
	}

	transport := &backgroundTransportStub{}
	client, db := newBackgroundTestClient(t, transport)
	ctx := context.Background()
	if _, err := client.AddBackgroundDir(parent); err != nil {
		t.Fatalf("AddBackgroundDir(parent): %v", err)
	}
	if _, err := client.AddBackgroundDir(child); err != nil {
		t.Fatalf("AddBackgroundDir(child): %v", err)
	}
	if report, err := client.SyncBackgroundDirs(ctx); err != nil || report.IngestedFiles != 1 {
		t.Fatalf("initial sync: report=%+v err=%v", report, err)
	}
	childUnit := loadBackgroundUnitIDs(t, db, childDoc)[0]
	seedActiveNoeticCitation(t, db, "NOETIC-LAYERED", []string{childUnit})

	if report, err := client.RemoveBackgroundDirReport(ctx, child); err != nil || len(report.RemovedUnitIDs) != 0 || report.ReboundFiles != 1 {
		t.Fatalf("child unmount must only re-bind: report=%+v err=%v", report, err)
	}
	if status := noeticStatusByID(t, db, "NOETIC-LAYERED"); status != "active" {
		t.Fatalf("citation went stale while still covered: %s", status)
	}
	removedIDs, err := client.RemoveBackgroundDir(ctx, parent)
	if err != nil {
		t.Fatalf("RemoveBackgroundDir(parent): %v", err)
	}
	if len(removedIDs) != 1 || removedIDs[0] != childUnit {
		t.Fatalf("last unmount must purge the unit, got %v", removedIDs)
	}
	if status := noeticStatusByID(t, db, "NOETIC-LAYERED"); status != "stale" {
		t.Fatalf("citation must go stale once coverage is gone, got %s", status)
	}
	if got := countBackgroundRows(t, db, `SELECT COUNT(*) FROM thin_vault_bg_files WHERE abs_path = ?`, childDoc); got != 0 {
		t.Fatalf("file metadata survived the last unmount: %d", got)
	}
	if mounts, err := client.Vault.ListBackgroundDirs(); err != nil || len(mounts) != 0 {
		t.Fatalf("mounts remain after full unmount: %+v err=%v", mounts, err)
	}
}

// TestBackgroundAddIsIdempotentAndReportsCoverage 验证挂载幂等与覆盖提示：同一物理目录（含大小写
// 变体路径，仅大小写不敏感文件系统）复用原挂载点；嵌套挂载照常建立并提示覆盖关系。
func TestBackgroundAddIsIdempotentAndReportsCoverage(t *testing.T) {
	base := newBackgroundTestDir(t)
	root := filepath.Join(base, "Docs")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "doc.md"), []byte("mount identity note"), 0o600); err != nil {
		t.Fatalf("write doc: %v", err)
	}
	client, _ := newBackgroundTestClient(t, &backgroundTransportStub{})

	first, err := client.AddBackgroundDir(root)
	if err != nil {
		t.Fatalf("AddBackgroundDir(root): %v", err)
	}
	again, err := client.AddBackgroundDir(root)
	if err != nil {
		t.Fatalf("AddBackgroundDir(root) again: %v", err)
	}
	if again.DirID != first.DirID || !strings.Contains(again.Notice, "already mounted") {
		t.Fatalf("same-path remount must be idempotent with a notice: %+v", again)
	}
	mounts, err := client.Vault.ListBackgroundDirs()
	if err != nil || len(mounts) != 1 {
		t.Fatalf("idempotent remount changed the mount set: %+v err=%v", mounts, err)
	}

	// Case-variant path: on a case-insensitive filesystem this is the same physical directory
	// and must resolve to the existing mount instead of creating a duplicate.
	caseVariant := filepath.Join(base, "docs")
	if _, statErr := os.Stat(caseVariant); statErr == nil {
		variantMount, err := client.AddBackgroundDir(caseVariant)
		if err != nil {
			t.Fatalf("AddBackgroundDir(case variant): %v", err)
		}
		if variantMount.DirID != first.DirID || !strings.Contains(variantMount.Notice, "same physical directory") {
			t.Fatalf("case-variant remount must reuse the existing mount: %+v", variantMount)
		}
		if mounts, err := client.Vault.ListBackgroundDirs(); err != nil || len(mounts) != 1 {
			t.Fatalf("case-variant remount created a second mount: %+v err=%v", mounts, err)
		}
	} else {
		t.Logf("case-sensitive filesystem: skipping case-variant remount assertion")
	}

	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sub, "nested.md"), []byte("nested mount note"), 0o600); err != nil {
		t.Fatalf("write nested doc: %v", err)
	}
	nestedMount, err := client.AddBackgroundDir(sub)
	if err != nil {
		t.Fatalf("AddBackgroundDir(sub): %v", err)
	}
	if !strings.Contains(nestedMount.Notice, "covered by the existing mount") {
		t.Fatalf("nested mount must report coverage: %+v", nestedMount)
	}
	parentMount, err := client.AddBackgroundDir(base)
	if err != nil {
		t.Fatalf("AddBackgroundDir(base): %v", err)
	}
	if !strings.Contains(parentMount.Notice, "contains the existing mount") {
		t.Fatalf("ancestor mount must report containment: %+v", parentMount)
	}
	if mounts, err := client.Vault.ListBackgroundDirs(); err != nil || len(mounts) != 3 {
		t.Fatalf("overlapping mounts must coexist: %+v err=%v", mounts, err)
	}
}

func TestSyncBackgroundDirsHonorsRollingMemoryIgnoreAndLockfiles(t *testing.T) {
	root := newBackgroundTestDir(t)
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "bg_ignore.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	vault, err := thinclient.NewVault(db, []byte("bg-ignore-secret"))
	if err != nil {
		t.Fatalf("NewVault: %v", err)
	}
	client := thinclient.NewClient(vault, thinclient.NewLocalVaultTransport(vault))
	ctx := context.Background()

	docPath := filepath.Join(root, "docs", "guide.md")
	dumpPath := filepath.Join(root, "dev", "evidence", "dump.jsonl")
	lockPath := filepath.Join(root, "pnpm-lock.yaml")
	if err := os.MkdirAll(filepath.Dir(docPath), 0o755); err != nil {
		t.Fatalf("mkdir docs: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(dumpPath), 0o755); err != nil {
		t.Fatalf("mkdir evidence: %v", err)
	}
	if err := os.WriteFile(docPath, []byte("# Guide\n Keep this architecture guide indexed."), 0o600); err != nil {
		t.Fatalf("write guide.md: %v", err)
	}
	if err := os.WriteFile(dumpPath, []byte(`{"q":"synthetic benchmark row 1"}`+"\n"), 0o600); err != nil {
		t.Fatalf("write dump.jsonl: %v", err)
	}
	if err := os.WriteFile(lockPath, []byte("lockfileVersion: '9.0'\npackages:\n  foo@1.0.0: {}\n"), 0o600); err != nil {
		t.Fatalf("write pnpm-lock.yaml: %v", err)
	}

	if _, err := client.AddBackgroundDir(root); err != nil {
		t.Fatalf("AddBackgroundDir: %v", err)
	}
	rep1, err := client.SyncBackgroundDirs(ctx)
	if err != nil {
		t.Fatalf("SyncBackgroundDirs pass 1: %v", err)
	}
	if rep1.ScannedFiles != 2 || rep1.IngestedFiles != 2 {
		t.Fatalf("pass 1 expected lockfile skipped and 2 files ingested, got %+v", rep1)
	}

	// Add .rolling-memory-ignore excluding dev/evidence/ -> pass 2 must skip the directory and purge its previously indexed rows.
	if err := os.WriteFile(filepath.Join(root, ".rolling-memory-ignore"), []byte("# ignore benchmark evidence dumps\ndev/evidence/\n"), 0o600); err != nil {
		t.Fatalf("write .rolling-memory-ignore: %v", err)
	}
	rep2, err := client.SyncBackgroundDirs(ctx)
	if err != nil {
		t.Fatalf("SyncBackgroundDirs pass 2: %v", err)
	}
	if rep2.ScannedFiles != 1 || rep2.UnchangedFiles != 1 || rep2.DeletedFiles != 1 {
		t.Fatalf("pass 2 expected 1 unchanged and 1 deleted via .rolling-memory-ignore, got %+v", rep2)
	}
	if got := countBackgroundRows(t, db, `SELECT COUNT(*) FROM thin_vault_bg_files WHERE abs_path = ?`, dumpPath); got != 0 {
		t.Fatalf("ignored file row retained: %d", got)
	}
	if got := countBackgroundRows(t, db, `SELECT COUNT(*) FROM thin_vault_bg_chunks WHERE abs_path = ?`, dumpPath); got != 0 {
		t.Fatalf("ignored chunk rows retained: %d", got)
	}
}
