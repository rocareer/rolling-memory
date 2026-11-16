// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package cli

import (
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/rocareer/rolling-memory/internal/harness"
)

// TestResolveActiveHarnessPicksStrongestEvidence 锁定活跃宿主裁决的唯一规则：
// 最新证据获胜（同刻保留更早、更具体的来源）；越界 id、非正时间戳不算证据；
// 零证据 = 未知（空 id + source none），绝不硬编码造一个默认宿主。
func TestResolveActiveHarnessPicksStrongestEvidence(t *testing.T) {
	items := []HarnessEnvItem{
		{ID: "zcode", Label: "ZCode"},
		{ID: "claude-code", Label: "Claude Code"},
		{ID: "workbuddy", Label: "WorkBuddy"},
	}
	cases := []struct {
		name    string
		claims  []activeHarnessClaim
		wantID  string
		wantSrc string
	}{
		{name: "no claims stays unknown", wantID: "", wantSrc: "none"},
		{
			name:    "non-positive timestamp is not evidence",
			claims:  []activeHarnessClaim{{"zcode", 0, "active_workspace.json"}},
			wantID:  "",
			wantSrc: "none",
		},
		{
			name:    "claim outside the inventory is dropped",
			claims:  []activeHarnessClaim{{"ghost", 900, "active_workspace.json"}},
			wantID:  "",
			wantSrc: "none",
		},
		{
			name: "newest claim wins",
			claims: []activeHarnessClaim{
				{"zcode", 100, "active_workspace.json"},
				{"claude-code", 150, "recent_activity"},
				{"workbuddy", 200, "recent_activity"},
			},
			wantID:  "workbuddy",
			wantSrc: "recent_activity",
		},
		{
			name: "equal timestamps keep the earlier more specific claim",
			claims: []activeHarnessClaim{
				{"zcode", 300, "active_workspace.json"},
				{"zcode", 300, "recent_activity"},
			},
			wantID:  "zcode",
			wantSrc: "active_workspace.json",
		},
		{
			name: "stale presence loses to fresher transcript",
			claims: []activeHarnessClaim{
				{"zcode", 100, "active_workspace.json"},
				{"claude-code", 200, "recent_activity"},
			},
			wantID:  "claude-code",
			wantSrc: "recent_activity",
		},
		{
			name: "unknown newest claim does not shadow a known older one",
			claims: []activeHarnessClaim{
				{"ghost", 900, "active_workspace.json"},
				{"zcode", 100, "recent_activity"},
			},
			wantID:  "zcode",
			wantSrc: "recent_activity",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id, src := resolveActiveHarness(items, tc.claims)
			if id != tc.wantID || src != tc.wantSrc {
				t.Fatalf("resolveActiveHarness = (%q, %q), want (%q, %q)", id, src, tc.wantID, tc.wantSrc)
			}
		})
	}
}

// TestHarnessClaimsSources 锁定证据拼装：env 来源记「现在」，presence 文件带自己的
// 写入时间，转录按最近活跃时间；缺失时间戳的 presence 不产生主张。
func TestHarnessClaimsSources(t *testing.T) {
	items := []HarnessEnvItem{
		{ID: "zcode", LastActiveAt: 400},
		{ID: "workbuddy"},
	}
	cases := []struct {
		name       string
		signalID   string
		detectSrc  string
		presenceAt int64
		want       []activeHarnessClaim
	}{
		{
			name:     "presence keeps its own write time",
			signalID: "workbuddy", detectSrc: "active_workspace.json", presenceAt: 500,
			want: []activeHarnessClaim{
				{"workbuddy", 500, "active_workspace.json"},
				{"zcode", 400, "recent_activity"},
			},
		},
		{
			name:     "env signal is timestamped now",
			signalID: "zcode", detectSrc: "env:ZCODE_PROJECT_DIR", presenceAt: 0,
			want: []activeHarnessClaim{
				{"zcode", 999, "env:ZCODE_PROJECT_DIR"},
				{"zcode", 400, "recent_activity"},
			},
		},
		{
			name:     "timestamp-less presence produces no claim",
			signalID: "workbuddy", detectSrc: "active_workspace.json", presenceAt: 0,
			want: []activeHarnessClaim{
				{"zcode", 400, "recent_activity"},
			},
		},
		{
			name:     "no signal",
			signalID: "", detectSrc: "none", presenceAt: 0,
			want: []activeHarnessClaim{
				{"zcode", 400, "recent_activity"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := harnessClaims(items, tc.signalID, tc.detectSrc, tc.presenceAt, 999)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("harnessClaims = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestHarnessEnvDefinitionsExtendsOnFootprintOrActive 锁定环境清单的组成：
// 五个核心宿主恒在；扩展宿主仅在有本机足迹（检测目录存在）或本身就是当前活跃
// 宿主时进入清单 —— 既不失明用户真正所在的环境，也不给无关宿主占版面。
func TestHarnessEnvDefinitionsExtendsOnFootprintOrActive(t *testing.T) {
	home := t.TempDir()
	core := len(harness.BuildHarnesses(home, ""))

	ids := func(defs []harness.Harness) map[string]bool {
		out := map[string]bool{}
		for _, d := range defs {
			out[d.ID] = true
		}
		return out
	}

	defs := harnessEnvDefinitions(home, "")
	if got := len(defs); got != core {
		t.Fatalf("bare home: %d definitions, want the %d core harnesses", got, core)
	}

	for _, dir := range []string{".workbuddy", ".cursor"} {
		if err := os.MkdirAll(filepath.Join(home, dir), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	defs = harnessEnvDefinitions(home, "")
	got := ids(defs)
	if !got["workbuddy"] || !got["cursor"] {
		t.Fatalf("footprint harnesses missing from inventory: %+v", got)
	}
	if got["doubao"] {
		t.Fatalf("doubao has no footprint here and is not active; it must stay out of the inventory")
	}

	// 活跃但目录已消失的扩展宿主仍须在清单里（presence 文件可能指名一个刚被卸载/移动的宿主）。
	defs = harnessEnvDefinitions(home, "doubao")
	if !ids(defs)["doubao"] {
		t.Fatalf("active extended harness absent from inventory: %+v", ids(defs))
	}
}

// TestHarnessCountCacheFoldsPendingImportDeltaWithinTTL 锁定计数缓存折叠契约：
// 在 TTL 窗口内，当后台自动同步或手动 reconcile 导入了未同步积压（0 < delta <= Unsynced）时，
// loadHarnessCountCache 直接原地折叠 Synced += delta / Unsynced -= delta 并返回命中，
// 避免每轮自动导入都作废缓存触发 GB 级 WAL 重扫；而超出 Unsynced 或单元减少时如实失效。
func TestHarnessCountCacheFoldsPendingImportDeltaWithinTTL(t *testing.T) {
	home := t.TempDir()
	zdbDir := filepath.Join(home, ".zcode", "cli", "db")
	if err := os.MkdirAll(zdbDir, 0o755); err != nil {
		t.Fatalf("mkdir zdb: %v", err)
	}
	if err := os.WriteFile(filepath.Join(zdbDir, "db.sqlite"), []byte("stub"), 0o600); err != nil {
		t.Fatalf("write stub zdb: %v", err)
	}
	vdb, err := sql.Open("sqlite", filepath.Join(home, "vault.db"))
	if err != nil {
		t.Fatalf("open vault: %v", err)
	}
	defer vdb.Close()
	if _, err := vdb.Exec(`CREATE TABLE thin_vault_meta (k TEXT PRIMARY KEY, v BLOB NOT NULL)`); err != nil {
		t.Fatalf("create meta: %v", err)
	}

	storeHarnessCountCache(vdb, "zcode", home, 100, 12, 80, 20, 100, 1700000000)

	// 1. 后台导入了 5 条积压（vaultUnits 100 → 105，delta=5 <= Unsynced=20）→ 命中并折叠为 Synced=85, Unsynced=15
	c1, ok := loadHarnessCountCache(vdb, "zcode", home, 105)
	if !ok {
		t.Fatalf("expected cache hit when folding pending import delta")
	}
	if c1.Synced != 85 || c1.Unsynced != 15 || c1.LocalTurns != 100 || c1.VaultUnits != 105 {
		t.Fatalf("unexpected folded cache: %+v", c1)
	}

	// 2. 超出已知积压的增长（vaultUnits 105 → 130，delta=25 > Unsynced=15）→ 必须失效重扫
	if _, ok := loadHarnessCountCache(vdb, "zcode", home, 130); ok {
		t.Fatalf("expected cache miss when vault growth exceeds unsynced backlog")
	}

	// 3. 单元减少（删除记忆，vaultUnits 105 → 100）→ 必须失效重扫
	if _, ok := loadHarnessCountCache(vdb, "zcode", home, 100); ok {
		t.Fatalf("expected cache miss when vault shrinks")
	}
}

func TestHarnessTranscriptChangedSinceDetectsNestedJSONLAndDSH(t *testing.T) {
	home := t.TempDir()
	dbConn, err := sql.Open("sqlite", filepath.Join(home, "meta.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer dbConn.Close()
	if _, err := dbConn.Exec(`CREATE TABLE IF NOT EXISTS thin_vault_meta (k TEXT PRIMARY KEY, v BLOB NOT NULL)`); err != nil {
		t.Fatalf("create meta: %v", err)
	}

	nestedDir := filepath.Join(home, ".claude", "projects", "proj-a")
	// The DeepSeek Harness transcript root is <DSH_HOME>/sessions when the
	// variable is set, otherwise <home>/.dsh/sessions; sessions live one level
	// deeper (--<cwd>--/<session-id>/session.v<N>.jsonl.zstd).
	dshHome := filepath.Join(home, "dsh-home")
	t.Setenv("DSH_HOME", dshHome)
	dshSessions := filepath.Join(dshHome, "sessions")
	dshDir := filepath.Join(dshSessions, "--demo-repo--", "session-abc")
	if err := os.MkdirAll(nestedDir, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	if err := os.MkdirAll(dshDir, 0o755); err != nil {
		t.Fatalf("mkdir dsh: %v", err)
	}
	nestedFile := filepath.Join(nestedDir, "s1.jsonl")
	if err := os.WriteFile(nestedFile, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write nested: %v", err)
	}
	baseTime := time.Unix(1700000000, 0)
	_ = os.Chtimes(filepath.Join(home, ".claude", "projects"), baseTime, baseTime)
	_ = os.Chtimes(nestedDir, baseTime, baseTime)
	_ = os.Chtimes(nestedFile, baseTime, baseTime)
	// Every directory level the change detector inspects is pinned to the base
	// time, so the case only measures the file mtime it advances.
	_ = os.Chtimes(dshSessions, baseTime, baseTime)
	_ = os.Chtimes(filepath.Dir(dshDir), baseTime, baseTime)
	_ = os.Chtimes(dshDir, baseTime, baseTime)

	markHarnessTranscriptSynced(dbConn, home)
	if harnessTranscriptChangedSince(dbConn, home) {
		t.Fatal("expected unchanged immediately after markHarnessTranscriptSynced")
	}

	// Appending to an existing nested JSONL file updates only the file's mtime, not the top-level directory.
	newerTime := baseTime.Add(10 * time.Second)
	_ = os.Chtimes(nestedFile, newerTime, newerTime)
	if !harnessTranscriptChangedSince(dbConn, home) {
		t.Fatal("expected change detection when nested JSONL file mtime advances while top-level dir mtime stays fixed")
	}
	markHarnessTranscriptSynced(dbConn, home)

	// A DSH transcript that advances must NOT arm the gate: this build inventories
	// DSH sessions but cannot read their turn content yet, so DSH's constant writes
	// would otherwise keep an auto-sync pass dirty that has nothing to import.
	dshFile := filepath.Join(dshDir, "session.v4.jsonl.zstd")
	if err := os.WriteFile(dshFile, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write dsh: %v", err)
	}
	dshTime := newerTime.Add(10 * time.Second)
	_ = os.Chtimes(dshFile, dshTime, dshTime)
	if harnessTranscriptChangedSince(dbConn, home) {
		t.Fatal("expected an unimportable DSH transcript not to arm the episodic gate")
	}
}
