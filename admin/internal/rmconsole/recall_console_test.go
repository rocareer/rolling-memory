package rollingmemory

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestRecallQueryArgsReadOnlyByDefault 钉住检索调试台的两条契约：
// ① 默认只读（--no-crystallize），控制台试查不写 L3；显式勾选才按 Agent 同等路径结晶；
// ② 工作区锚用引擎现役 cue 首部 `[<工作区>]` 语法，不另造参数。
func TestRecallQueryArgsReadOnlyByDefault(t *testing.T) {
	args := recallQueryArgs("/tmp/m.db", "mem:self", "owner", "full", 8, 60, 12000, 2, 5, "登录页 预置账号", "", true, false)
	if !slices.Contains(args, "--no-crystallize") {
		t.Fatalf("console recall must be read-only by default: %v", args)
	}
	cue := args[slices.Index(args, "--q")+1]
	if cue != "登录页 预置账号" {
		t.Fatalf("cue without workspace = %q", cue)
	}

	args = recallQueryArgs("/tmp/m.db", "mem:self", "owner", "full", 8, 60, 12000, 2, 5, "登录页 预置账号", "Rolling-Memory", false, true)
	if slices.Contains(args, "--no-crystallize") {
		t.Fatalf("explicit crystallize must drop --no-crystallize: %v", args)
	}
	if cue := args[slices.Index(args, "--q")+1]; cue != "[Rolling-Memory] 登录页 预置账号" {
		t.Fatalf("workspace anchor cue = %q", cue)
	}
	if !slices.Contains(args, "--no-raw") {
		t.Fatalf("include_raw=false must pass --no-raw: %v", args)
	}
}

// TestRecallQueryArgsCarryConsoleBudget 钉住控制台试查的引擎预算：必须显式携带
// --budget-ms（consoleRecallBudgetMS = 1 分钟），不再落回引擎缺省 10s 在负载下误报超时失败。
func TestRecallQueryArgsCarryConsoleBudget(t *testing.T) {
	args := recallQueryArgs("/tmp/m.db", "mem:self", "owner", "full", 8, 60, 12000, 2, 5, "登录页 预置账号", "", false, false)
	i := slices.Index(args, "--budget-ms")
	if i < 0 || i+1 >= len(args) {
		t.Fatalf("console recall must pass --budget-ms: %v", args)
	}
	if want := strconv.Itoa(consoleRecallBudgetMS); args[i+1] != want {
		t.Fatalf("--budget-ms = %s, want %s", args[i+1], want)
	}
}

// TestTryConsoleDaemonRecallOverUnixSocket 钉住控制台直连本机守护进程 Unix Socket：
// 当 engine.sock 在线时，零 CLI 子进程直接通过 /v1/recall 拿到召回结果并透传工作区锚。
func TestTryConsoleDaemonRecallOverUnixSocket(t *testing.T) {
	sockPath := filepath.Join("/tmp", fmt.Sprintf("rm-console-test-%d.sock", time.Now().UnixNano()))
	_ = os.Remove(sockPath)
	t.Cleanup(func() { _ = os.Remove(sockPath) })
	t.Setenv("ROLLING_MEMORY_DAEMON_SOCKET", sockPath)

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen unix %s: %v", sockPath, err)
	}
	defer ln.Close()

	var gotPayload map[string]any
	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v1/recall" || r.Method != http.MethodPost {
				http.NotFound(w, r)
				return
			}
			_ = json.NewDecoder(r.Body).Decode(&gotPayload)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true,"result":{"absent":false,"meta":{"elapsed_ms":42},"bands":[{"id":"b1"}]}}`))
		}),
	}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	raw, handled, err := tryConsoleDaemonRecall(
		context.Background(), "/tmp", "/tmp/m.db", "mem:self", "owner", "full",
		8, 60, 12000, 2, 5, "检索性能", "Rolling-Memory", false, false, "", "", "",
	)
	if err != nil || !handled || raw == nil {
		t.Fatalf("tryConsoleDaemonRecall = (%v, %v, %v), want handled", raw, handled, err)
	}
	if gotCue, _ := gotPayload["cue"].(string); gotCue != "[Rolling-Memory] 检索性能" {
		t.Fatalf("daemon payload cue = %q, want anchored cue", gotCue)
	}
	if gotNoCryst, _ := gotPayload["no_crystallize"].(bool); !gotNoCryst {
		t.Fatalf("daemon payload no_crystallize = %v, want true", gotNoCryst)
	}
}

// TestRecentRecallCuesAndWorkspaceFacet 钉住「最近 Agent 检索」与工作区选项的数据口径：
// 线索取自 L3 Noetic（新→旧、同线索去重、拆出 `[<工作区>]` 锚），工作区取自入库 locus。
func TestRecentRecallCuesAndWorkspaceFacet(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "recall_console.db")
	db, err := openWritableVaultDB(dbPath)
	if err != nil {
		t.Fatalf("openWritableVaultDB: %v", err)
	}
	seeds := []string{
		`INSERT INTO thin_vault_docs(doc_id, layer, occurred_at, raw_text, locus) VALUES ('ep_a', 'episodic', 100, 'a', 'Rolling-Memory')`,
		`INSERT INTO thin_vault_docs(doc_id, layer, occurred_at, raw_text, locus) VALUES ('ep_b', 'episodic', 101, 'b', 'Rolling-Memory')`,
		`INSERT INTO thin_vault_docs(doc_id, layer, occurred_at, raw_text, locus) VALUES ('ep_c', 'episodic', 102, 'c', 'demo-app')`,
		`INSERT INTO thin_vault_docs(doc_id, layer, occurred_at, raw_text) VALUES ('ep_d', 'episodic', 103, 'd')`,
		`INSERT INTO thin_vault_noetic(noetic_id, occurred_at, status, query_text, answer_text, cited_unit_ids_json)
		 VALUES ('nt_old', 200, 'active', '[Rolling-Memory] 本地模式 recall 缓存', 'x', '["ep_a","ep_b"]')`,
		`INSERT INTO thin_vault_noetic(noetic_id, occurred_at, status, query_text, answer_text, cited_unit_ids_json)
		 VALUES ('nt_dup', 300, 'active', '[Rolling-Memory] 本地模式 recall 缓存', 'x', '["ep_a"]')`,
		`INSERT INTO thin_vault_noetic(noetic_id, occurred_at, status, query_text, answer_text, cited_unit_ids_json)
		 VALUES ('nt_new', 400, 'stale', '控制台 登录 验证码', 'x', '[]')`,
	}
	for _, seed := range seeds {
		if _, err := db.Exec(seed); err != nil {
			_ = db.Close()
			t.Fatalf("seed %q: %v", seed, err)
		}
	}
	_ = db.Close()

	cues := recentRecallCues(dbPath, 10)
	if len(cues) != 2 {
		t.Fatalf("recent cues = %+v, want 2 (duplicate cue collapsed)", cues)
	}
	if cues[0].ID != "nt_new" || cues[0].Cue != "控制台 登录 验证码" || cues[0].Workspace != "" || cues[0].Status != "stale" {
		t.Fatalf("newest cue = %+v", cues[0])
	}
	if cues[1].ID != "nt_dup" || cues[1].Cue != "本地模式 recall 缓存" || cues[1].Workspace != "Rolling-Memory" || cues[1].Cited != 1 {
		t.Fatalf("anchored cue = %+v", cues[1])
	}
	if got := recentRecallCues(dbPath, 1); len(got) != 1 || got[0].ID != "nt_new" {
		t.Fatalf("limit=1 cues = %+v", got)
	}

	want := []FacetCount{{Value: "Rolling-Memory", Count: 2}, {Value: "demo-app", Count: 1}}
	if got := queryFacets(dbPath, UserScope).Workspaces; !reflect.DeepEqual(got, want) {
		t.Fatalf("workspace facet = %+v, want %+v", got, want)
	}
}

// TestQueryFacetsMergesWorkspaceDuplicates 钉住 Top 工作区口径：会话记录以项目短名入库、
// 背景目录以绝对路径入库，同名工作区必须先按 basename 归一合并计数再取前 6，
// 否则同一工作区会以两条同名柱重复出现并挤掉真实名次。
func TestQueryFacetsMergesWorkspaceDuplicates(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "facets_merge.db")
	db, err := openWritableVaultDB(dbPath)
	if err != nil {
		t.Fatalf("openWritableVaultDB: %v", err)
	}
	seeds := []string{
		`INSERT INTO thin_vault_docs(doc_id, layer, occurred_at, raw_text, locus) VALUES ('ep_1', 'episodic', 100, 'a', 'Rolling-Memory')`,
		`INSERT INTO thin_vault_docs(doc_id, layer, occurred_at, raw_text, locus) VALUES ('ep_2', 'episodic', 101, 'b', 'Rolling-Memory')`,
		`INSERT INTO thin_vault_docs(doc_id, layer, occurred_at, raw_text, locus) VALUES ('ep_3', 'episodic', 102, 'c', 'demo-app')`,
		`INSERT INTO thin_vault_bg_dirs(dir_id, root_path, added_at) VALUES ('bg_rm', '/repos/Rolling-Memory', 1)`,
		`INSERT INTO thin_vault_bg_files(abs_path, dir_id, rel_path, chunk_count) VALUES ('/repos/Rolling-Memory/a.md', 'bg_rm', 'a.md', 4)`,
		`INSERT INTO thin_vault_bg_dirs(dir_id, root_path, added_at) VALUES ('bg_rb', '/repos/workspace-b', 2)`,
		`INSERT INTO thin_vault_bg_files(abs_path, dir_id, rel_path, chunk_count) VALUES ('/repos/workspace-b/b.md', 'bg_rb', 'b.md', 5)`,
	}
	for _, seed := range seeds {
		if _, err := db.Exec(seed); err != nil {
			_ = db.Close()
			t.Fatalf("seed %q: %v", seed, err)
		}
	}
	_ = db.Close()

	want := []FacetCount{
		{Value: "Rolling-Memory", Count: 6}, // 会话 2 + 背景分块 4 合并同名
		{Value: "workspace-b", Count: 5},
		{Value: "demo-app", Count: 1},
	}
	if got := queryFacets(dbPath, UserScope).TopPredicates; !reflect.DeepEqual(got, want) {
		t.Fatalf("top predicates = %+v, want %+v（同名工作区必须合并而非重复占位）", got, want)
	}
}

// TestRecallContextDeepLink protects context-only links from retaining an old cue.
func TestRecallContextDeepLink(t *testing.T) {
	root := filepath.Join("..", "..", "web", "src", "views", "admin")
	for _, tc := range []struct {
		path string
		want []string
	}{
		{filepath.Join(root, "plugin", "rolling-memory", "recall.vue"), []string{
			"['q', 'workspace', 'scope'].some((field) => Object.hasOwn(route.query, field))",
			"form.q = typeof route.query.q === 'string' ? route.query.q.trim() : ''",
			"form.workspace = typeof route.query.workspace === 'string' ? route.query.workspace.trim() : ''",
			"form.scope = typeof route.query.scope === 'string' && route.query.scope.trim() ? route.query.scope.trim() : 'mem:self'",
			"lastAppliedQuery.value = ''",
			"Local vault data is not isolated by scope.",
		}},
		{filepath.Join(root, "dashboard", "roi.vue"), []string{
			"scope: currentScope, workspace: selectedLocus === '(global / unscoped)' ? '' : selectedLocus",
			"Top cited units filter:",
		}},
	} {
		raw, err := os.ReadFile(tc.path)
		if err != nil {
			t.Fatalf("read %s: %v", tc.path, err)
		}
		for _, want := range tc.want {
			if !strings.Contains(string(raw), want) {
				t.Errorf("%s missing %q", tc.path, want)
			}
		}
	}
}

// TestRecallDebuggerScoreDisplay guards against treating raw scores or absent diagnostics as probabilities.
func TestRecallDebuggerScoreDisplay(t *testing.T) {
	path := filepath.Join("..", "..", "web", "src", "views", "admin", "plugin", "rolling-memory", "recall.vue")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	source := string(raw)
	for _, want := range []string{
		"sort_by: 'delivery'",
		"formatPercent(result.meta?.cue_coverage)",
		"typeof node.relative_strength === 'number'",
		"result.result_mode === 'probe' ? '—' : formatScore(row.score)",
	} {
		if !strings.Contains(source, want) {
			t.Errorf("recall debugger missing %q", want)
		}
	}
	for _, forbidden := range []string{
		"node.confidence || 0",
		"detailData.item.confidence",
		"Math.round((row.score || 0) * 100)",
		"result.meta?.cue_coverage ?? 0",
	} {
		if strings.Contains(source, forbidden) {
			t.Errorf("recall debugger must not contain %q", forbidden)
		}
	}
}
