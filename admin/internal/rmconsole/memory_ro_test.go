// plugin: rolling-memory v0.0.58
package rollingmemory

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

// TestSQLiteQueryJSONReadOnly 钉住进程内只读访问：整型/实数/文本取值与旧
// sqlite3 -json 口径一致，且写语句必然失败（插件永不改引擎库）。
func TestSQLiteQueryJSONReadOnly(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "memory.db")
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	stmts := []string{
		`CREATE TABLE demo (id INTEGER PRIMARY KEY, label TEXT, score REAL, note TEXT);`,
		`INSERT INTO demo (id, label, score, note) VALUES (1, '小白', 0.95, NULL);`,
		`INSERT INTO demo (id, label, score, note) VALUES (2, '老张', 0.80, 'x');`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("exec %q: %v", stmt, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	rows, err := sqliteQueryJSON(dbPath, `SELECT id, label, score, note FROM demo ORDER BY id;`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	if asInt(rows[0]["id"]) != 1 || asString(rows[0]["label"]) != "小白" || asFloat(rows[0]["score"]) != 0.95 {
		t.Fatalf("unexpected row 0: %#v", rows[0])
	}
	if rows[0]["note"] != nil {
		t.Fatalf("NULL should stay nil, got %#v", rows[0]["note"])
	}
	if asString(rows[1]["note"]) != "x" || asInt(rows[1]["id"]) != 2 {
		t.Fatalf("unexpected row 1: %#v", rows[1])
	}

	if _, err := sqliteQueryJSON(dbPath, `INSERT INTO demo (id) VALUES (3);`); err == nil {
		t.Fatal("expected write to fail on read-only connection")
	}
	if _, err := sqliteQueryJSON(filepath.Join(t.TempDir(), "missing.db"), `SELECT 1;`); err == nil {
		t.Fatal("expected missing db to fail")
	}
}

func TestBuildMemoryGraphWaveCellAndLayers(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "memory.db")
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	stmts := []string{
		`CREATE TABLE thin_vault_docs (
			doc_id TEXT PRIMARY KEY,
			layer TEXT NOT NULL DEFAULT 'episodic',
			occurred_at INTEGER NOT NULL DEFAULT 0,
			raw_text TEXT NOT NULL,
			sentences_json TEXT NOT NULL,
			address_uri TEXT NOT NULL DEFAULT '',
			coord64 INTEGER NOT NULL DEFAULT 0,
			kept_indices_json TEXT NOT NULL DEFAULT '[]'
		);`,
		`CREATE TABLE thin_vault_noetic (
			noetic_id TEXT PRIMARY KEY,
			occurred_at INTEGER NOT NULL DEFAULT 0,
			status TEXT NOT NULL DEFAULT 'active',
			query_text TEXT NOT NULL,
			answer_text TEXT NOT NULL,
			cited_unit_ids_json TEXT NOT NULL DEFAULT '[]',
			cited_sentences_json TEXT NOT NULL DEFAULT '[]',
			address_uri TEXT NOT NULL DEFAULT ''
		);`,
		`INSERT INTO thin_vault_docs VALUES ('doc-1', 'episodic', 1730800100, 'the memory engine episodic unit 1', '["the memory engine episodic unit 1"]', 'C6:L3:B001/C6:L2:B001/C6:L1:B001#00', 1001, '[0]');`,
		`INSERT INTO thin_vault_docs VALUES ('doc-2', 'background', 1730800200, 'Background architecture spec', '["Background architecture spec"]', 'C6:L3:B001/C6:L2:B002/C6:L1:B002#00', 1002, '[0]');`,
		`INSERT INTO thin_vault_noetic VALUES ('noetic-1', 1730800300, 'active', 'How does the memory engine work?', 'Via blind pulses and dynamic cells.', '["doc-1"]', '["the memory engine episodic unit 1"]', '');`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("exec %q: %v", stmt, err)
		}
	}
	_ = db.Close()

	resp := buildMemoryGraphWithLayers(dbPath, "mem:self", 180, "", []string{"background", "episodic", "noetic"}, map[string]float64{
		"background": 1.0,
		"episodic":   1.0,
		"noetic":     1.2,
	})
	var hasCell, hasDoc1, hasNoetic1 bool
	for _, n := range resp.Nodes {
		if n.Kind == "cell" && n.NodeKey == "cell:C6:L1:B001" {
			hasCell = true
		}
		if n.NodeKey == "unit:doc-1" {
			hasDoc1 = true
		}
		if n.NodeKey == "unit:noetic-1" && n.Layer == "noetic" {
			hasNoetic1 = true
		}
	}
	if !hasCell || !hasDoc1 || !hasNoetic1 {
		t.Fatalf("expected cell C6:L1:B001, unit:doc-1, and unit:noetic-1 in the memory-engine graph, got nodes=%+v", resp.Nodes)
	}
	if resp.Stats.AtomTotal <= 0 || resp.Stats.MoleculeTotal <= 0 || len(resp.Stats.TopAtoms) == 0 || len(resp.Stats.TopMolecules) == 0 {
		t.Fatalf("expected positive atom_total/molecule_total and top lists in graph stats, got %+v", resp.Stats)
	}
	for _, n := range resp.Nodes {
		if n.NodeKey == "unit:doc-1" && (len(n.Atoms) == 0 || len(n.Molecules) == 0) {
			t.Fatalf("expected unit:doc-1 to carry readable atoms and molecules, got %+v", n)
		}
		if n.NodeKey == "cell:C6:L1:B001" && len(n.Atoms) == 0 {
			t.Fatalf("expected cell:C6:L1:B001 to aggregate member atoms, got %+v", n)
		}
	}

	expanded := expandMemoryGraph(dbPath, "mem:self", []string{"unit:noetic-1"}, 1, 50)
	if len(expanded.Nodes) < 2 {
		t.Fatalf("expected ego expansion from unit:noetic-1 to include cited unit:doc-1, got %+v", expanded.Nodes)
	}
}

func TestQueryMemoriesFromDBFormKindAndCategories(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "memory.db")
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	stmts := []string{
		`CREATE TABLE thin_vault_docs (
			doc_id TEXT PRIMARY KEY,
			layer TEXT NOT NULL DEFAULT 'episodic',
			occurred_at INTEGER NOT NULL DEFAULT 0,
			raw_text TEXT NOT NULL,
			sentences_json TEXT NOT NULL DEFAULT '[]',
			address_uri TEXT NOT NULL DEFAULT '',
			coord64 INTEGER NOT NULL DEFAULT 0,
			kept_indices_json TEXT NOT NULL DEFAULT '[]'
		);`,
		`CREATE TABLE thin_vault_noetic (
			noetic_id TEXT PRIMARY KEY,
			occurred_at INTEGER NOT NULL DEFAULT 0,
			status TEXT NOT NULL DEFAULT 'active',
			query_text TEXT NOT NULL,
			answer_text TEXT NOT NULL,
			cited_unit_ids_json TEXT NOT NULL DEFAULT '[]',
			cited_sentences_json TEXT NOT NULL DEFAULT '[]',
			address_uri TEXT NOT NULL DEFAULT ''
		);`,
		`INSERT INTO thin_vault_docs(doc_id, layer, occurred_at, raw_text, address_uri) VALUES
			('doc-bg-1', 'background', 1003, 'Albert leads Rolling-Memory architecture.', 'C6:L3:B001/C6:L2:B001/C6:L1:B001#00'),
			('doc-ep-1', 'episodic',   1002, 'We discussed the three-layer memory model.', 'C6:L3:B001/C6:L2:B001/C6:L1:B001#01');`,
		`INSERT INTO thin_vault_noetic(noetic_id, occurred_at, status, query_text, answer_text, cited_unit_ids_json) VALUES
			('noetic-1', 1004, 'active', 'Who leads Rolling-Memory?', 'Albert leads Rolling-Memory.', '["doc-bg-1"]');`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("exec %q: %v", stmt, err)
		}
	}
	_ = db.Close()

	allResp, err := queryMemoriesFromDB(dbPath, MemoriesQueryRequest{Scope: "mem:self", Status: "active"})
	if err != nil {
		t.Fatalf("query all: %v", err)
	}
	if allResp.Total != 3 || len(allResp.Items) != 3 {
		t.Fatalf("total=%d len=%d, want 3", allResp.Total, len(allResp.Items))
	}
	if allResp.Summary.AnchorFullCount != 3 {
		t.Fatalf("anchor_full_count=%d, want 3", allResp.Summary.AnchorFullCount)
	}
}

func TestMoleculeWavepacketQualityOnMarkdownTableAndCodeRefs(t *testing.T) {
	rawTableLine := "| **1. 容器凝聚与分裂余弦阈值** | `EndogenousL2MergeCosineThreshold` · `EndogenousWeakEdgeCosineBounds` · `EndogenousCandidateScanWindows` | `containers.go:568` · 容器扫窗写死 `20/16` | 忽略租户球面余弦分布 $(\\mu_{\\cos}, \\sigma_{\\cos})$ 与容器规模 $N$ |"
	nodesByKey := map[string]*GraphNodeItem{
		"unit:u1": {
			ID:      1,
			NodeKey: "unit:u1",
			Kind:    "unit",
			Layer:   "background",
			Span:    rawTableLine,
		},
		"unit:u2": {
			ID:      2,
			NodeKey: "unit:u2",
			Kind:    "unit",
			Layer:   "background",
			Span:    "容器规模自适应与球面余弦分布内生标定法则，消除写死阈值。",
		},
	}
	keyOrder := []string{"unit:u1", "unit:u2"}
	cellMembers := map[string][]*GraphNodeItem{
		"cell:C6:L1:B001": {nodesByKey["unit:u1"], nodesByKey["unit:u2"]},
	}
	unitCells := map[string][]string{
		"unit:u1": {"cell:C6:L1:B001"},
		"unit:u2": {"cell:C6:L1:B001"},
	}

	_, molTotal, _, _, topMols := enrichGraphWithAtomsAndMolecules(nodesByKey, keyOrder, cellMembers, unitCells)
	if molTotal == 0 || len(topMols) == 0 {
		t.Fatalf("expected non-empty molecules, got molTotal=%d", molTotal)
	}
	u1Mols := nodesByKey["unit:u1"].Molecules
	if len(u1Mols) == 0 {
		t.Fatalf("expected unit:u1 to have extracted molecule wavepacket")
	}
	mol := u1Mols[0]
	t.Logf("[MoleculeView Refactored]\n  MolKey   : %s\n  Text     : %s\n  CoreAtoms: %v",
		mol.MolKey, mol.Text, mol.CoreAtoms)

	if strings.HasPrefix(mol.Text, "68`") || strings.Contains(mol.Text, "|") {
		t.Fatalf("molecule text still contains broken line-number fragment or raw pipe: %q", mol.Text)
	}
	for _, ca := range mol.CoreAtoms {
		if ca == "与容器规" || len([]rune(ca)) == 4 && strings.HasPrefix(ca, "与") {
			t.Fatalf("CoreAtoms still contains 4-char hard-sliced CJK garbage %q in %v", ca, mol.CoreAtoms)
		}
	}
	hasCodeSym := false
	hasConcept := false
	for _, ca := range mol.CoreAtoms {
		if strings.HasPrefix(ca, "Endogenous") {
			hasCodeSym = true
		} else {
			hasConcept = true
		}
	}
	if !hasCodeSym || !hasConcept {
		t.Fatalf("expected CoreAtoms to balance code symbols and natural-language concepts, got %v", mol.CoreAtoms)
	}
}

// TestSQLiteFileURIWindowsDriveRoot 钉住 Windows 盘符路径的 file: URI 形态：
// 盘符必须留在路径内；若被渲染成 file://C:%5C...，盘符会变成 URI authority，
// 驱动直接以 invalid uri authority 拒开。
func TestSQLiteFileURIWindowsDriveRoot(t *testing.T) {
	cases := map[string]string{
		`C:\Users\a\mem.db`:   "file:///C:/Users/a/mem.db",
		`C:\a#b\mem.db`:       "file:///C:/a%23b/mem.db",
		`c:\users\a\mem.db`:   "file:///c:/users/a/mem.db",
		`\\server\share\m.db`: "file:////server/share/m.db",
		"/home/a#b/mem.db":    "file:///home/a%23b/mem.db",
		"/tmp/plain.db":       "file:/tmp/plain.db",
	}
	for path, want := range cases {
		if got := sqliteFileURI(path); got != want {
			t.Errorf("sqliteFileURI(%q) = %q, want %q", path, got, want)
		}
	}
}
