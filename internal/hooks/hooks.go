// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

// Package hooks implements the fail-open harness hooks.
//
// Events map to the three useful points of a coding session:
//
//   - start  -- import new transcript turns, replay them locally, optional boot line.
//   - recall -- read-side injection for the current prompt (<rolling-memory-recall>).
//   - close  -- import the remaining turns, replay them locally and close the
//     memory; closing is the stop checkpoint (the usb profile truncates the WAL
//     in store.Close).
//
// No hook ever raises into the harness. All failures are logged and exit 0.
package hooks

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/rocareer/rolling-memory/internal/api"
	"github.com/rocareer/rolling-memory/internal/config"
	"github.com/rocareer/rolling-memory/internal/ids"
	"github.com/rocareer/rolling-memory/internal/recall"
	"github.com/rocareer/rolling-memory/internal/store"
	"github.com/rocareer/rolling-memory/internal/text"
	"github.com/rocareer/rolling-memory/internal/thinclient"
	"github.com/rocareer/rolling-memory/internal/transcript"
)

const (
	maxHookInput  = 2 * 1024 * 1024
	maxLogBytes   = 2 * 1024 * 1024
	maxCloseTrace = 120
	maxImportLine = 200
)

// Options carries the parsed hook flags.
type Options struct {
	JSONOutput bool
	BudgetMS   *int
}

// RunHook runs one hook event and returns the text to print (empty means the
// harness gets no injection for this event).
func RunHook(event string, payload map[string]any, o Options) (string, error) {
	cfg, err := config.Load(config.LoadOptions{})
	if err != nil {
		return "", err
	}
	return runHook(cfg, event, payload, o)
}

func runHook(cfg *config.Config, event string, payload map[string]any, o Options) (string, error) {
	memory, err := api.New(cfg)
	if err != nil {
		return "", err
	}
	defer memory.Close()
	switch event {
	case "start", "close":
		return importReplayEvent(memory, cfg, event, payload, o)
	case "recall":
		return recallHook(memory, cfg, payload, o)
	}
	return "", fmt.Errorf("unknown hook event '%s'", event)
}

// importReplayEvent runs the start/close sweep: import transcript turns, replay
// the pending traces, log the receipt, and (for start) render the boot line.
func importReplayEvent(
	memory *api.Memory, cfg *config.Config, event string, payload map[string]any, o Options,
) (string, error) {
	presence := workspacePresence(payload)
	project := workspaceProject(payload)
	session := "default"
	if value := firstTruthy(payload["session_id"], payload["sessionId"]); value != nil {
		session = pyStr(value)
	}
	if presence != nil {
		saveActiveWorkspacePresence(cfg, *presence, session)
	}
	if memory != nil && memory.IsThinBrainProxy() {
		// RAME mode (`/open/memory/v1/*`): legacy tables (`trace`/`node`/`assertion`)
		// are purged and retired; the closed turn is read directly from the transcript
		// or harness session and ingested into the engine's episodic layer in real time.
		if memory.Store != nil && memory.Store.DB() != nil {
			thinclient.PurgeLegacyTables(memory.Store.DB())
		}
		data := map[string]any{
			"imported": 0,
			"messages": 0,
			"replayed": 0,
			"written":  0,
		}
		upReceipt := uploadCurrentEpisodicTurn(memory, cfg, session, payload)
		if dm := asIntDefault(upReceipt["direct_messages"], 0); dm > 0 {
			data["messages"] = dm
			data["imported"] = dm
		}
		delete(upReceipt, "direct_messages")
		for key, value := range upReceipt {
			data[key] = value
		}
		logHook(cfg, event, data)
		if event == "start" && (cfg.Hooks.BootContext || project != "") {
			return bootContext(memory, cfg, payload, o)
		}
		return "", nil
	}
	imported, err := importTurns(memory, payload)
	if err != nil {
		return "", err
	}
	// 本地模式金库镜像（fail-open · 会话有界）：payload-only 轮次（宿主只递
	// prompt/last_assistant_message、无转写文件）在 trace 里是唯一副本，转写扫描器
	// 永远看不到它；不在这里按会话镜像进金库，这轮话就对 recall 永久不可见。
	if asIntDefault(imported["inserted"], 0) > 0 {
		if mirrorErr := memory.MirrorSessionTracesToVault(session); mirrorErr != nil {
			logHook(cfg, event, map[string]any{"vault_mirror_error": ids.Redact(mirrorErr.Error())})
		}
	}
	replayedCount := 0
	if event == "close" || asIntDefault(imported["inserted"], 0) > 0 {
		maxTraces := maxCloseTrace
		if event == "start" {
			maxTraces = cfg.Hooks.MaxTraces
		}
		if traces, tErr := memory.Store.PendingTraces(cfg.Scope, maxTraces, nil); tErr == nil {
			for _, tr := range traces {
				_ = memory.Store.MarkProcessed(tr.Hash)
				_ = memory.Store.UpdateReplayState(cfg.Scope, tr.RecordedAt, tr.Hash)
				_ = memory.Store.UpdateSessionCursor(cfg.Scope, tr.Session, tr.Turn)
			}
			replayedCount = len(traces)
		}
	}
	data := map[string]any{
		"imported": asIntDefault(imported["inserted"], 0),
		"messages": asIntDefault(imported["messages"], 0),
		"replayed": replayedCount,
		"written":  0,
	}
	logHook(cfg, event, data)
	if event == "start" && (cfg.Hooks.BootContext || project != "") {
		return bootContext(memory, cfg, payload, o)
	}
	return "", nil
}

// bootContext renders the SessionStart pulse:
//   - In standalone / golden-test mode (project == ""), it renders the classic
//     status line plus up to 3 recent assertions for 100% golden contract parity.
//   - In live coding workspace mode (project != ""), it strips the legacy
//     heavy blind memory dump (zero blind Recall(project), zero unrequested hot bands,
//     zero redundant git subprocess forks) and emits ONLY a <60-token lightweight
//     pulse: 1 status line + 1-line episodic tail ("What Just Happened") + 1-line
//     anti-hallucination rule, deferring all specific memory retrieval to on-demand
//     UserPromptSubmit (`recallHook`).
func bootContext(memory *api.Memory, cfg *config.Config, payload map[string]any, o Options) (string, error) {
	status, err := memory.Status(cfg.Scope)
	if err != nil {
		return "", err
	}
	text := fmt.Sprintf(
		"Rolling-Memory (%s): %d active memory units · %d pending traces · %d conflicts · scope %s",
		cfg.Mode,
		asIntDefault(status["active_assertions"], 0),
		asIntDefault(status["pending_traces"], 0),
		asIntDefault(status["conflicts_proposed"], 0),
		cfg.Scope,
	)
	project := workspaceProject(payload)
	if project == "" {
		recent, err := memory.Recent(nil, nil, 3)
		if err != nil {
			return "", err
		}
		if len(recent) > 0 {
			rendered := make([]string, 0, len(recent))
			for _, item := range recent {
				rendered = append(rendered, pyStr(item["content"]))
			}
			text += "\n最近: " + strings.Join(rendered, "；")
		}
		return additionalContext(text, o.JSONOutput, "SessionStart")
	}

	session := "default"
	if value := firstTruthy(payload["session_id"], payload["sessionId"]); value != nil {
		session = pyStr(value)
	}
	if skel := buildLiveWorkspaceSkeleton(payload); skel != "" {
		text += "\n[活体目录骨骼 · 实时磁盘 (免盲目 ls/find)] " + skel
	}
	if atlas := queryDiskVerifiedWorkspaceAtlas(memory, cfg.Scope, project, payload, 5); atlas != "" {
		text += "\n[已验活体路标 · os.Stat 实时核验]:\n" + atlas
	}
	if tailLine := queryWorkspaceEpisodicTail(memory, cfg.Scope, project, session); tailLine != "" {
		text += "\n[刚干了什么 · 历史会话留痕（仅作背景，不代表当前任务已完成）] " + tailLine
	}
	text += "\n[记忆代理四问闭环与防幻觉准则] 1. 历史记忆≠本轮已做（寻址直达但动作必亲为：上方路标与过往记忆仅为历史基线，绝不代表当前会话已完成本轮任务；代码实现以实时 Read/git status 为准，凡本轮要求开发/优化/核验必须在本会话亲手执行，严禁拿历史记忆或存量代码当本轮完工交差）；2. 遇阻主动 recall；3. 终态验证后写入（严禁在任务中途或未验证前抢跑 remember；须等全部动作与测试/实测验证完成后，在收尾最后一步写入）；4. 五记五不记与过时必自愈：仅沉淀「①非显然意图→入口寻址路标、②跨模块联动契约、③带坐标的反直觉深坑与实战经验、④团队共识/认知倾向/用户偏好与架构边界、⑤外部环境配方」，严禁记完工战报/完成态谓词（如「落地并验证于/…闭环」）/瞬时卷号读数/行级代码细节；主语 s 必为无日期持久概念名——当共识/倾向/认知变化或发现所读记忆（坐标/参数/结论）显而易见过时，收尾必须复用同一 (s, x) 调 remember(closes_prior:true) 补记最新并使旧记忆自动 superseded 退场（Hook 读侧自动剥离 superseded，彻底杜绝新旧冲突污染）。"
	return additionalContext(text, o.JSONOutput, "SessionStart")
}

// buildLiveWorkspaceSkeleton scans the current workspace directory on live disk
// (<0.3ms via os.ReadDir, 0 subprocesses) and returns a compact 1-line module
// skeleton (e.g. "cmd/{rolling-memory} internal/{api,cli,hooks,...} skills/{...}")
// so a newly opened session never needs blind `ls` / `find` exploration while
// being 100% immune to stale memory.
func buildLiveWorkspaceSkeleton(payload map[string]any) string {
	raw := firstTruthy(payload["cwd"], payload["workspace"], os.Getenv("ROLLING_MEMORY_WORKSPACE"))
	if raw == nil {
		return ""
	}
	dir := filepath.Clean(expandUser(pyStrip(pyStr(raw))))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	skipDir := func(name string) bool {
		if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
			return true
		}
		switch name {
		case "node_modules", "vendor", "dist", "build", "target", "bin", "out", "coverage", "tmp", "logs", "testdata":
			return true
		}
		return false
	}
	var parts []string
	for _, e := range entries {
		if !e.IsDir() || skipDir(e.Name()) {
			continue
		}
		topName := e.Name()
		subEntries, subErr := os.ReadDir(filepath.Join(dir, topName))
		if subErr != nil {
			parts = append(parts, topName+"/")
			continue
		}
		var subs []string
		for _, se := range subEntries {
			if se.IsDir() && !skipDir(se.Name()) {
				subs = append(subs, se.Name())
				if len(subs) >= 28 {
					break
				}
			}
		}
		if len(subs) > 0 && len(subs) <= 28 {
			parts = append(parts, topName+"/{"+strings.Join(subs, ",")+"}")
		} else {
			parts = append(parts, topName+"/")
		}
		if len(parts) >= 10 {
			break
		}
	}
	return strings.Join(parts, " ")
}

// queryDiskVerifiedWorkspaceAtlas queries active assertions for this workspace,
// extracts any module file paths, and verifies each path on the LIVE filesystem
// via os.Stat(filepath.Join(cwd, path)). Only assertions whose referenced file
// paths 100% exist on disk right now are emitted as compact 1-line landmarks!
func queryDiskVerifiedWorkspaceAtlas(memory *api.Memory, scope, project string, payload map[string]any, maxItems int) string {
	if memory == nil || memory.Store == nil || memory.Store.DB() == nil || project == "" || maxItems <= 0 {
		return ""
	}
	raw := firstTruthy(payload["cwd"], payload["workspace"], os.Getenv("ROLLING_MEMORY_WORKSPACE"))
	if raw == nil {
		return ""
	}
	cwd := filepath.Clean(expandUser(pyStrip(pyStr(raw))))
	if info, err := os.Stat(cwd); err != nil || info == nil || !info.IsDir() {
		return ""
	}
	rows, err := memory.Store.DB().Query(
		`SELECT COALESCE(sn.label, a.subject_literal, '') AS s,
		        COALESCE(rn.label, '') AS x,
		        COALESCE(onode.label, a.object_literal, '') AS o
		 FROM assertion a
		 LEFT JOIN node sn ON a.subject_id = sn.id
		 LEFT JOIN node rn ON a.rel_id = rn.id
		 LEFT JOIN node onode ON a.object_id = onode.id
		 WHERE a.scope = ? AND a.status = 'active' AND a.bare = 0 AND a.confidence >= 0.75
		   AND (a.locus LIKE ? OR COALESCE(sn.label, a.subject_literal, '') LIKE ?)
		 ORDER BY (a.warrant = 'performative') DESC, a.created_at DESC LIMIT 36`,
		scope, "%"+project+"%", "%"+project+"%",
	)
	if err != nil {
		return ""
	}
	defer rows.Close()

	var lines []string
	seenSubj := map[string]bool{}
	for rows.Next() {
		var s, x, o string
		if rows.Scan(&s, &x, &o) != nil {
			continue
		}
		s = strings.TrimSpace(s)
		if s == "" || seenSubj[s] {
			continue
		}
		candPaths := extractStructuralPathsFromText(s+" "+o, 16)
		var verifiedPaths []string
		for _, p := range candPaths {
			if !strings.Contains(p, "/") || strings.Contains(p, "*") || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, "/") {
				continue
			}
			cleanRel := filepath.Clean(strings.TrimPrefix(p, "./"))
			if strings.HasPrefix(cleanRel, "..") {
				continue
			}
			fullPath := filepath.Join(cwd, cleanRel)
			if _, statErr := os.Stat(fullPath); statErr == nil {
				verifiedPaths = append(verifiedPaths, cleanRel)
			}
		}
		if len(verifiedPaths) == 0 {
			continue
		}
		seenSubj[s] = true
		lines = append(lines, "  • "+s+" ("+x+") → "+strings.Join(verifiedPaths, " , "))
		if len(lines) >= maxItems {
			break
		}
	}
	if len(lines) == 0 {
		nRows, nErr := memory.Store.DB().Query(
			`SELECT COALESCE(query_text, ''), COALESCE(answer_text, '')
				 FROM thin_vault_noetic
				 WHERE status = 'active'
				 ORDER BY occurred_at DESC LIMIT 36`,
		)
		if nErr == nil {
			defer nRows.Close()
			for nRows.Next() {
				var qText, aText string
				if nRows.Scan(&qText, &aText) != nil {
					continue
				}
				qText = strings.TrimSpace(strings.ReplaceAll(qText, "\n", " "))
				if qText == "" || seenSubj[qText] {
					continue
				}
				candPaths := extractStructuralPathsFromText(qText+" "+aText, 16)
				var verifiedPaths []string
				for _, p := range candPaths {
					if !strings.Contains(p, "/") || strings.Contains(p, "*") || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, "/") {
						continue
					}
					cleanRel := filepath.Clean(strings.TrimPrefix(p, "./"))
					if strings.HasPrefix(cleanRel, "..") {
						continue
					}
					if _, statErr := os.Stat(filepath.Join(cwd, cleanRel)); statErr == nil {
						verifiedPaths = append(verifiedPaths, cleanRel)
					}
				}
				if len(verifiedPaths) == 0 {
					continue
				}
				seenSubj[qText] = true
				lines = append(lines, "  • "+qText+" → "+strings.Join(verifiedPaths, " , "))
				if len(lines) >= maxItems {
					break
				}
			}
		}
	}
	return strings.Join(lines, "\n")
}

// recallHook implements the UserPromptSubmit surface. Memory retrieval is 100%
// agent-driven through the MCP `recall` tool: this hook never runs automatic
// recall on the user's raw prompt and never injects recalled material, so a
// prompt can neither self-echo as a hit nor crystallize a question the agent
// did not ask for. What remains is purely structural, language-agnostic and
// zero closed-set keyword tables: the capture side (Presence coordinates,
// background-dir sync, raw prompt into `trace`) and, on the four-turn cadence,
// a one-line reminder that `recall` exists.
func recallHook(memory *api.Memory, cfg *config.Config, payload map[string]any, o Options) (string, error) {
	prompt := ""
	if value := firstTruthy(payload["prompt"], payload["user_prompt"]); value != nil {
		prompt = pyStrip(pyStr(value))
	}
	if prompt == "" {
		return "", nil
	}
	session := "default"
	if value := firstTruthy(payload["session_id"], payload["sessionId"]); value != nil {
		session = pyStr(value)
	}
	project := workspaceProject(payload)
	if project != "" {
		if stripped := pyStrip(ids.StripInjected(prompt)); stripped != "" {
			prompt = stripped
		}
	}
	presence := workspacePresence(payload)
	if presence != nil {
		saveActiveWorkspacePresence(cfg, *presence, session)
	}
	maybeSyncBackgroundOnHook(memory)
	// In RAME (ThinBrainProxy) mode the capture side belongs to the background
	// sync as well: the hook is fully silent.
	if memory != nil && memory.IsThinBrainProxy() {
		logHook(cfg, "recall", map[string]any{"injected": 0, "absent": 1})
		return "", nil
	}
	if project != "" {
		// Capture only: the raw prompt enters `trace` for episodic history and
		// anchor verification, never as an extracted fact.
		captureUserPrompt(memory, cfg, session, prompt, presence)
		if isCadenceReminderDue(memory, cfg.Scope, session) {
			cadenceText := "[Rolling-Memory 4轮节律锚点] 盘中遇报错/盲搜无果或需核对前序决策与文件入口时，请直接带具体 cue 调 MCP `recall`（回忆即记忆，命中证据后自动巩固结晶 L3 Noetic，无需手动写记忆）。"
			logHook(cfg, "recall", map[string]any{"injected": 0, "absent": 1, "cadence": 1})
			return additionalContext(cadenceText, o.JSONOutput, "UserPromptSubmit")
		}
	}
	logHook(cfg, "recall", map[string]any{"injected": 0, "absent": 1})
	return "", nil
}

// additionalContext wraps text for --json output.
func additionalContext(text string, jsonOutput bool, eventName string) (string, error) {
	if !jsonOutput {
		return text, nil
	}
	payload := ids.OrderedMap{
		{Key: "hookSpecificOutput", Value: ids.OrderedMap{
			{Key: "hookEventName", Value: eventName},
			{Key: "additionalContext", Value: text},
		}},
	}
	return ids.PyJSONDump(payload, false, false, false)
}

// importTurns resolves the payload's transcript path and
// imports the next slice at the stored cursor. A missing file keeps the
// missing-file receipt shape instead of failing the hook.
func importTurns(memory *api.Memory, payload map[string]any) (map[string]any, error) {
	rawPath := firstTruthy(payload["transcript_path"], payload["transcriptPath"])
	if rawPath == nil {
		return importFallbackAssistantTurn(memory, payload, map[string]any{"messages": 0, "inserted": 0})
	}
	path := expandUser(pyStr(rawPath))
	if !filepath.IsAbs(path) {
		base := ""
		if cwd := firstTruthy(payload["cwd"]); cwd != nil {
			base = expandUser(pyStr(cwd))
		}
		if base == "" {
			directory, err := os.Getwd()
			if err != nil {
				return nil, err
			}
			base = directory
		}
		path = pyJoin(base, path)
	}
	if info, err := os.Stat(path); err != nil || info == nil {
		return importFallbackAssistantTurn(memory, payload, map[string]any{"messages": 0, "inserted": 0, "missing": path})
	}
	session := pyStr(firstTruthy(payload["session_id"], payload["sessionId"], pathStem(path)))
	offset, err := transcript.CurrentOffset(memory.Store, path, session)
	if err != nil {
		return nil, err
	}
	result, err := transcript.ImportTranscript(
		memory.Store, path, memory.Config.Scope, &session, "shared", offset, maxImportLine)
	if err != nil {
		return nil, err
	}
	if asIntDefault(result["inserted"], 0) == 0 {
		return importFallbackAssistantTurn(memory, payload, result)
	}
	// Fix W2 (Stop Ephemeral Transcript Turn Collapse & Missing Presence):
	// When a harness passes a single-turn temporary transcript file (e.g. zcode-hook-*/transcript.jsonl
	// with offset==0 and total_lines<=2), ImportTranscript numbers that isolated line as turn=1.
	// Re-align the newly inserted row's turn to the session's monotonic turn counter and stamp
	// workspace 4D Presence into meta_json so session timeline & locus filtering stay intact.
	if offset == 0 && asIntDefault(result["total_lines"], 0) <= 2 && session != "" && session != "default" {
		realignImportedEphemeralTurn(memory, session, path, workspacePresence(payload))
	}
	return result, nil
}

func realignImportedEphemeralTurn(memory *api.Memory, session, path string, presence *text.PresenceCoordinates) {
	if memory == nil || memory.Store == nil || memory.Store.DB() == nil {
		return
	}
	var latestHash, metaRaw, textRaw string
	var currentTurn int64
	err := memory.Store.DB().QueryRow(
		`SELECT hash, turn, meta_json, text FROM trace
		 WHERE scope = ? AND session = ?
		 ORDER BY recorded_at DESC, rowid DESC LIMIT 1`,
		memory.Config.Scope, session,
	).Scan(&latestHash, &currentTurn, &metaRaw, &textRaw)
	if err != nil || latestHash == "" {
		return
	}
	var maxPriorTurn sql.NullInt64
	_ = memory.Store.DB().QueryRow(
		`SELECT MAX(turn) FROM trace WHERE scope = ? AND session = ? AND hash != ?`,
		memory.Config.Scope, session, latestHash,
	).Scan(&maxPriorTurn)
	newTurn := currentTurn
	if maxPriorTurn.Valid && maxPriorTurn.Int64 >= currentTurn {
		newTurn = maxPriorTurn.Int64 + 1
	}
	meta := map[string]any{"source": path}
	_ = json.Unmarshal([]byte(metaRaw), &meta)
	if presence != nil && !presence.IsEmpty() {
		meta["world"] = presence.World
		meta["carrier"] = presence.Carrier
		meta["locus"] = presence.Locus
		meta["incarnation"] = presence.Incarnation
	}
	cleanedText := stripMemoryAttributionLine(stripDegenerateTail(textRaw))
	if encoded, mErr := json.Marshal(meta); mErr == nil {
		_, _ = memory.Store.DB().Exec(
			`UPDATE trace SET turn = ?, meta_json = ?, text = ? WHERE hash = ?`,
			newTurn, string(encoded), cleanedText, latestHash,
		)
	}
}

func importFallbackAssistantTurn(memory *api.Memory, payload map[string]any, base map[string]any) (map[string]any, error) {
	lastMsg := firstTruthy(payload["last_assistant_message"], payload["lastAssistantMessage"])
	if lastMsg == nil {
		return base, nil
	}
	text := stripMemoryAttributionLine(stripDegenerateTail(ids.StripInjected(pyStrip(pyStr(lastMsg)))))
	if utf8.RuneCountInString(text) < 4 {
		return base, nil
	}
	session := "default"
	if value := firstTruthy(payload["session_id"], payload["sessionId"]); value != nil {
		session = pyStr(value)
	}
	now := ids.UTCNow()
	meta := map[string]any{"source": "stop_payload"}
	if presence := workspacePresence(payload); presence != nil {
		meta["world"] = presence.World
		meta["carrier"] = presence.Carrier
		meta["locus"] = presence.Locus
		meta["incarnation"] = presence.Incarnation
	}
	_, created, err := memory.Store.AddTrace(memory.Config.Scope, text, store.TraceOptions{
		Session:    session,
		Turn:       nextSessionTurn(memory, memory.Config.Scope, session),
		Kind:       "message",
		Author:     "assistant",
		Audience:   "shared",
		OccurredAt: &now,
		Meta:       meta,
	})
	if err != nil {
		return base, nil
	}
	base["messages"] = asIntDefault(base["messages"], 0) + 1
	if created {
		base["inserted"] = asIntDefault(base["inserted"], 0) + 1
	}
	return base, nil
}

// renderIntError writes the ValueError text the way cli.main does.
func renderIntError(err error, jsonOutput bool, stdout, stderr io.Writer) {
	message := ids.Redact(err.Error())
	if jsonOutput {
		encoded, dumpErr := ids.PyJSONDump(ids.OrderedMap{
			{Key: "ok", Value: false},
			{Key: "error", Value: message},
		}, false, false, false)
		if dumpErr == nil {
			fmt.Fprintln(stdout, encoded)
		}
		return
	}
	fmt.Fprintf(stderr, "error: %s\n", message)
}

// HookMain parses argv, reads one JSON payload from stdin and prints the hook
// output. Every failure is logged and exits 0.
func HookMain(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	event := "recall"
	options := Options{}
	remaining := append([]string(nil), args...)
	for len(remaining) > 0 {
		value := remaining[0]
		remaining = remaining[1:]
		switch value {
		case "start", "close", "recall":
			event = value
		case "--json":
			options.JSONOutput = true
		case "--budget-ms":
			options.BudgetMS = nil
			if len(remaining) == 0 {
				continue
			}
			parsed, err := pyInt(remaining[0])
			remaining = remaining[1:]
			if err != nil {
				// Invalid integer flag renders an error (JSON on --json, else stderr)
				// and exits 1.
				renderIntError(err, options.JSONOutput, stdout, stderr)
				return 1
			}
			options.BudgetMS = &parsed
		}
	}
	payload, inputError := readPayload(stdin)
	if inputError != "" {
		fallbackLog("hook input error: " + inputError)
		return 0
	}
	cfg, err := config.Load(config.LoadOptions{})
	if err != nil {
		fallbackLog(err.Error())
		return 0
	}
	output, err := runHook(cfg, event, payload, options)
	if err != nil {
		// Log the error and stay fail-open.
		fallbackLog(err.Error())
		recordHookDecision(cfg, event, payload, "error")
		return 0
	}
	if output != "" {

		if !strings.HasSuffix(output, "\n") {
			output += "\n"
		}
		written, writeErr := io.WriteString(stdout, output)
		var flushErr error
		if flusher, ok := stdout.(interface{ Flush() error }); ok {
			flushErr = flusher.Flush()
		}
		recordHookOutput(cfg, event, payload, output, written, writeErr, flushErr)
	} else {
		recordHookDecision(cfg, event, payload, "empty")
	}
	return 0
}

// readPayload reads the payload, stopping one rune past maxHookInput so
// oversized input is detectable. The returned string is the
// fallback-log message; empty means the payload is usable.
func readPayload(stdin io.Reader) (map[string]any, string) {
	reader := bufio.NewReader(stdin)
	var builder strings.Builder
	count := 0
	for count <= maxHookInput {
		char, size, err := reader.ReadRune()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return map[string]any{}, err.Error()
		}
		if char == utf8.RuneError && size == 1 {
			return map[string]any{}, "invalid UTF-8 in hook input"
		}
		builder.WriteRune(char)
		count++
	}
	if count > maxHookInput {
		return map[string]any{}, "hook input too large"
	}
	raw := pyStrip(builder.String())
	if raw == "" {
		return map[string]any{}, ""
	}
	payload, err := parsePayload(raw)
	if err != nil {
		return map[string]any{}, "invalid JSON: " + ids.PyJSONDecodeMessage(raw, err)
	}
	return payload, ""
}

// parsePayload preserves int/float split via json.Number and drops
// non-object payloads.
func parsePayload(raw string) (map[string]any, error) {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("trailing data after payload")
	}
	payload, ok := value.(map[string]any)
	if !ok {
		return map[string]any{}, nil
	}
	return payload, nil
}

// logHook appends one sorted, redacted JSON line to config.log.
func logHook(cfg *config.Config, event string, data map[string]any) {
	if cfg == nil || cfg.Log == "" {
		return
	}
	if err := ensureHookStateDir(filepath.Dir(cfg.Log)); err != nil {
		return
	}
	rotate(cfg.Log)
	line := map[string]any{"ts": ids.UTCNow(), "event": event}
	if !strings.HasPrefix(cfg.Scope, "mem:golden:") && strings.TrimSpace(cfg.Scope) != "" {
		line["scope"] = cfg.Scope
	}
	for key, value := range data {
		line[key] = value
	}
	encoded, err := ids.PyJSONDump(line, false, true, false)
	if err != nil {
		return
	}
	appendLine(cfg.Log, ids.Redact(encoded))
}

// fallbackLog writes to default_state_dir()/hook.log when the hook cannot use
// the main log.
func fallbackLog(message string) {
	path := filepath.Join(config.DefaultStateDir(), "hook.log")
	if err := ensureHookStateDir(filepath.Dir(path)); err != nil {
		return
	}
	rotate(path)
	appendLine(path, ids.Redact(fmt.Sprintf("%d %s", ids.UTCNow(), message)))
}

// rotate renames an oversized log to <name>.1.
func rotate(path string) {
	info, err := os.Stat(path)
	if err != nil || info.Size() <= maxLogBytes {
		return
	}
	handle, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return
	}
	err = handle.Chmod(0o600)
	closeErr := handle.Close()
	if err != nil || closeErr != nil {
		return
	}
	_ = os.Rename(path, path+".1")
}

// ensureHookStateDir creates the hook state/log directory for first use with
// owner-only access. An existing directory keeps whatever mode its owner chose
// (the permission contract test pins this), so no chmod is applied.
func ensureHookStateDir(dir string) error {
	return os.MkdirAll(dir, 0o700)
}

func appendLine(path, line string) {
	handle, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer handle.Close()
	if err := handle.Chmod(0o600); err != nil {
		return
	}
	_, _ = handle.WriteString(line + "\n")
}

// HookStats reports the main log's line count and last parsed line; small
// helper for status/doctor, not used on the hot path.
func HookStats(cfg *config.Config) (map[string]any, error) {
	if cfg == nil {
		loaded, err := config.Load(config.LoadOptions{})
		if err != nil {
			return nil, err
		}
		cfg = loaded
	}
	info, err := os.Stat(cfg.Log)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[string]any{"exists": false, "lines": 0}, nil
		}
		return nil, err
	}
	handle, err := os.Open(cfg.Log)
	if err != nil {
		return nil, err
	}
	defer handle.Close()
	lines := 0
	var last any
	reader := bufio.NewReader(handle)
	for {
		chunk, readErr := reader.ReadString('\n')
		if chunk != "" {
			lines++
			parsed := map[string]any{}
			if json.Unmarshal([]byte(chunk), &parsed) == nil {
				last = parsed
			}
		}
		if readErr != nil {
			break
		}
	}
	return map[string]any{"exists": true, "lines": lines, "last": last, "size": info.Size()}, nil
}

// firstTruthy returns the first non-empty, non-zero value in values.
func firstTruthy(values ...any) any {
	for _, value := range values {
		if pyTruthy(value) {
			return value
		}
	}
	return nil
}

func pyTruthy(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case string:
		return typed != ""
	case json.Number:
		if number, err := strconv.ParseFloat(typed.String(), 64); err == nil {
			return number != 0
		}
		return true
	case float64:
		return typed != 0
	case float32:
		return typed != 0
	case int:
		return typed != 0
	case int64:
		return typed != 0
	case []any:
		return len(typed) > 0
	case map[string]any:
		return len(typed) > 0
	default:
		return true
	}
}

// pyStr formats a JSON-shaped value for human display.
func pyStr(value any) string {
	switch typed := value.(type) {
	case nil:
		return "None"
	case string:
		return typed
	case bool:
		if typed {
			return "True"
		}
		return "False"
	case json.Number:
		raw := typed.String()
		if !strings.ContainsAny(raw, ".eE") {
			return raw
		}
		number, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return raw
		}
		return pyFloatStr(number)
	case float64:
		return pyFloatStr(typed)
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	default:
		return fmt.Sprint(value)
	}
}

// pyFloatStr formats a float64 using shortest round-trip representation (or nan/inf/-inf).
func pyFloatStr(value float64) string {
	switch {
	case math.IsNaN(value):
		return "nan"
	case math.IsInf(value, 1):
		return "inf"
	case math.IsInf(value, -1):
		return "-inf"
	}
	encoded, err := ids.PyJSONDump(value, false, false, true)
	if err != nil {
		return strconv.FormatFloat(value, 'g', -1, 64)
	}
	return encoded
}

// pyStrip trims Unicode whitespace and ASCII file/group/record/unit separators (0x1c–0x1f).
func pyStrip(value string) string {
	return strings.TrimFunc(value, func(char rune) bool {
		return unicode.IsSpace(char) || (char >= 0x1c && char <= 0x1f)
	})
}

// pyInt mirrors int(str): surrounding whitespace, an optional sign and single
// underscores between digits.
func pyInt(raw string) (int, error) {
	value := pyStrip(raw)
	digits := value
	sign := 1
	if strings.HasPrefix(digits, "+") {
		digits = digits[1:]
	} else if strings.HasPrefix(digits, "-") {
		sign = -1
		digits = digits[1:]
	}
	cleaned := strings.Builder{}
	previousUnderscore := false
	for index, char := range digits {
		if char == '_' {
			if index == 0 || previousUnderscore {
				return 0, invalidIntLiteral(value)
			}
			previousUnderscore = true
			continue
		}
		if char < '0' || char > '9' {
			return 0, invalidIntLiteral(value)
		}
		previousUnderscore = false
		cleaned.WriteRune(char)
	}
	if cleaned.Len() == 0 || previousUnderscore {
		return 0, invalidIntLiteral(value)
	}
	parsed, err := strconv.ParseInt(cleaned.String(), 10, 64)
	if err != nil {
		return 0, invalidIntLiteral(value)
	}
	return sign * int(parsed), nil
}

func invalidIntLiteral(value string) error {
	return fmt.Errorf("invalid literal for int() with base 10: '%s'", value)
}

// pyJoin mirrors pathlib's "base / path": "." components vanish while ".."
// and repeated separators survive.
func pyJoin(base, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	prefix := ""
	if strings.HasPrefix(base, "/") {
		prefix = "/"
	}
	parts := []string{}
	for _, part := range strings.Split(base, "/") {
		if part == "" || part == "." {
			continue
		}
		parts = append(parts, part)
	}
	for _, part := range strings.Split(path, "/") {
		if part == "" || part == "." {
			continue
		}
		parts = append(parts, part)
	}
	return prefix + strings.Join(parts, "/")
}

// pathStem mirrors pathlib's Path.stem (a dotfile keeps its leading dot).
func pathStem(path string) string {
	base := filepath.Base(path)
	return base[:len(base)-len(pyPathSuffix(base))]
}

func pyPathSuffix(name string) string {
	index := strings.LastIndex(name, ".")
	if index <= 0 || index == len(name)-1 {
		return ""
	}
	return name[index:]
}

// expandUser mirrors Path.expanduser for the current user.
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

func asIntDefault(value any, fallback int) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case json.Number:
		if parsed, err := typed.Int64(); err == nil {
			return int(parsed)
		}
	}
	return fallback
}

func workspaceProject(payload map[string]any) string {
	raw := firstTruthy(payload["cwd"], payload["workspace"], os.Getenv("ROLLING_MEMORY_WORKSPACE"))
	if raw == nil {
		return ""
	}
	cleaned := filepath.Clean(expandUser(pyStrip(pyStr(raw))))
	if cleaned == "" || cleaned == "." || cleaned == "/" {
		return ""
	}
	base := filepath.Base(cleaned)
	if base == "" || base == "." || base == "/" {
		return ""
	}
	return base
}

func workspacePresence(payload map[string]any) *text.PresenceCoordinates {
	raw := firstTruthy(payload["cwd"], payload["workspace"], os.Getenv("ROLLING_MEMORY_WORKSPACE"))
	if raw == nil {
		return nil
	}
	cleaned := filepath.Clean(expandUser(pyStrip(pyStr(raw))))
	if cleaned == "" || cleaned == "." || cleaned == "/" {
		return nil
	}
	harnessName := ""
	if h := firstTruthy(payload["harness"], payload["client"]); h != nil {
		harnessName = pyStrip(pyStr(h))
	}
	role := ""
	if r := firstTruthy(payload["agent_role"], payload["subagent_type"]); r != nil {
		role = pyStrip(pyStr(r))
	}
	coords := text.DetectHarnessPresence(cleaned, harnessName, role)
	if coords.IsEmpty() {
		return nil
	}
	return &coords
}

func isEphemeralProbeSession(session string) bool {
	ls := strings.ToLower(strings.TrimSpace(session))
	if ls == "" || ls == "default" {
		return true
	}
	for _, p := range []string{"probe", "test", "verify", "eval", "bench", "seed", "locomo", "conv-", "dashboard-", "console-"} {
		if strings.HasPrefix(ls, p) {
			return true
		}
	}
	return false
}

func saveActiveWorkspacePresence(cfg *config.Config, coords text.PresenceCoordinates, session string) {
	if cfg == nil || cfg.Log == "" || coords.IsEmpty() || isEphemeralProbeSession(session) {
		return
	}
	stateDir := filepath.Dir(cfg.Log)
	if err := ensureHookStateDir(stateDir); err != nil {
		return
	}
	statePath := filepath.Join(stateDir, "active_workspace.json")
	lockPath := statePath + ".lock"
	for attempt := 0; attempt < 8; attempt++ {
		if info, sErr := os.Stat(lockPath); sErr == nil && time.Since(info.ModTime()) > 2*time.Second {
			_ = os.Remove(lockPath)
		}
		lf, lErr := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if lErr == nil {
			_ = lf.Close()
			defer os.Remove(lockPath)
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	// The presence file holds only the latest writer's coordinates: the locus is the
	// canonical workspace name (repository root basename) or "", and no per-locus
	// session map is accumulated (it had no reader and preserved stale workspace keys).
	data, err := json.Marshal(map[string]any{
		"world":       coords.World,
		"carrier":     coords.Carrier,
		"locus":       coords.Locus,
		"incarnation": coords.Incarnation,
		"session_id":  session,
		"updated_at":  ids.UTCNow(),
	})
	if err != nil {
		return
	}
	tmpPath := fmt.Sprintf("%s.tmp.%d.%d", statePath, os.Getpid(), time.Now().UnixNano())
	if os.WriteFile(tmpPath, data, 0o600) == nil {
		if os.Rename(tmpPath, statePath) != nil {
			_ = os.Remove(tmpPath)
		}
	}
}

func nextSessionTurn(memory *api.Memory, scope, session string) int64 {
	var maxTurn sql.NullInt64
	_ = memory.Store.DB().QueryRow(
		"SELECT MAX(turn) FROM trace WHERE scope = ? AND session = ?",
		scope, session,
	).Scan(&maxTurn)
	if maxTurn.Valid && maxTurn.Int64 >= 0 {
		return maxTurn.Int64 + 1
	}
	return 1
}

// captureUserPrompt records the user's raw prompt into trace for episodic
// continuity, but intentionally does NOT run Replay/extraction at prompt-submit
// time so unexecuted requests, questions, or hypotheses are never prematurely
// materialized into factual assertions before work is actually performed.
// Moreover, we set Processed: true on the trace row so Stop/Start replay sweeps
// do not turn raw user questions/commands into fake factual assertions either.
func captureUserPrompt(memory *api.Memory, cfg *config.Config, session, prompt string, presence *text.PresenceCoordinates) int {
	if memory != nil && memory.IsThinBrainProxy() {
		return 0
	}
	cleaned := ids.StripInjected(pyStrip(prompt))
	if cleaned == "" {
		return 0
	}
	if memory == nil || memory.Store == nil || memory.Store.DB() == nil {
		return 0
	}
	if !memory.Store.HasLegacyGraph() {
		if session != "" && session != "default" {
			db := memory.Store.DB()
			_, _ = db.Exec(`CREATE TABLE IF NOT EXISTS thin_vault_meta (k TEXT PRIMARY KEY, v BLOB NOT NULL)`)
			key := "hook_cadence_turns:" + cfg.Scope + ":" + session
			var raw []byte
			turns := 0
			if err := db.QueryRow(`SELECT v FROM thin_vault_meta WHERE k = ?`, key).Scan(&raw); err == nil {
				if n, pErr := strconv.Atoi(strings.TrimSpace(string(raw))); pErr == nil && n > 0 {
					turns = n
				}
			}
			turns++
			_, _ = db.Exec(`INSERT OR REPLACE INTO thin_vault_meta(k, v) VALUES (?, ?)`, key, []byte(strconv.Itoa(turns)))
		}
		return 0
	}
	now := ids.UTCNow()
	meta := map[string]any{"source": "user_prompt_submit"}
	if presence != nil {
		meta["world"] = presence.World
		meta["carrier"] = presence.Carrier
		meta["locus"] = presence.Locus
		meta["incarnation"] = presence.Incarnation
	}
	traceHash, created, err := memory.Store.AddTrace(cfg.Scope, cleaned, store.TraceOptions{
		Session:    session,
		Turn:       nextSessionTurn(memory, cfg.Scope, session),
		Kind:       "message",
		Author:     "user",
		Audience:   "shared",
		OccurredAt: &now,
		Meta:       meta,
	})
	if err != nil || !created {
		return 0
	}
	// Mark raw prompt capture as processed for rule-v1 replay so unverified user
	// prompts are kept in trace for episodic history & anchor verification without
	// being auto-extracted as settled facts before the agent verifies them.
	_, _ = memory.Store.DB().Exec(`UPDATE trace SET processed = 1 WHERE hash = ?`, traceHash)
	return 0
}

func extractStructuralPathsFromText(text string, maxPaths int) []string {
	return recall.ExtractStructuralPaths(text, maxPaths)
}

// queryWorkspaceEpisodicTail returns a concise 1-line summary of the most recent
// user/assistant interaction in the current workspace locus, answering "What did
// I just do right before this session?" using purely temporal/structural SQL.
// It strictly locks onto a single target session belonging to this workspace in
// two steps so interleaved parallel sessions never push the target session's
// user prompt out of the query window, and skips synthetic remember anchor rows.
func queryWorkspaceEpisodicTail(memory *api.Memory, scope, project, currentSession string) string {
	if memory == nil || memory.Store == nil || memory.Store.DB() == nil || project == "" {
		return ""
	}
	targetSession := ""
	if currentSession != "" && currentSession != "default" {
		_ = memory.Store.DB().QueryRow(
			`SELECT session FROM trace
			 WHERE scope = ? AND session = ? AND length(text) >= 8
			   AND meta_json NOT LIKE '%"synthetic_anchor"%'
			 LIMIT 1`,
			scope, currentSession,
		).Scan(&targetSession)
	}
	if targetSession == "" {
		_ = memory.Store.DB().QueryRow(
			`SELECT session FROM trace
			 WHERE scope = ? AND length(text) >= 8
			   AND session != '' AND session != 'default'
			   AND meta_json LIKE ?
			   AND meta_json NOT LIKE '%"synthetic_anchor"%'
			 ORDER BY recorded_at DESC, turn DESC LIMIT 1`,
			scope, "%"+project+"%",
		).Scan(&targetSession)
	}
	if targetSession == "" {
		return queryVaultWorkspaceEpisodicTail(memory, project)
	}
	rows, err := memory.Store.DB().Query(
		`SELECT author, text FROM trace
		 WHERE scope = ? AND session = ? AND length(text) >= 6
		   AND meta_json NOT LIKE '%"synthetic_anchor"%'
		 ORDER BY recorded_at DESC, turn DESC LIMIT 10`,
		scope, targetSession,
	)
	if err != nil {
		return ""
	}
	defer rows.Close()
	var lastUser, lastAssistant string
	for rows.Next() {
		var author, text string
		if rows.Scan(&author, &text) != nil {
			continue
		}
		rawCleaned := stripDegenerateTail(pyStrip(ids.StripInjected(text)))
		if author == "assistant" || author == "agent" {
			lines := strings.Split(rawCleaned, "\n")
			if len(lines) > 1 && strings.HasPrefix(strings.TrimSpace(lines[0]), "技能判定：") {
				rawCleaned = pyStrip(strings.Join(lines[1:], "\n"))
			}
		}
		cleaned := strings.ReplaceAll(rawCleaned, "\n", " ")
		if utf8.RuneCountInString(cleaned) < 6 {
			continue
		}
		if (author == "user" || author == "albert") && lastUser == "" {
			if r := []rune(cleaned); len(r) > 60 {
				cleaned = string(r[:60]) + "…"
			}
			lastUser = cleaned
		} else if (author == "assistant" || author == "agent") && lastAssistant == "" {
			if r := []rune(cleaned); len(r) > 80 {
				cleaned = string(r[:80]) + "…"
			}
			lastAssistant = cleaned
		}
		if lastUser != "" && lastAssistant != "" {
			break
		}
	}
	if lastUser != "" && lastAssistant != "" {
		return "上轮提问「" + lastUser + "」→ 进展「" + lastAssistant + "」"
	}
	if lastUser != "" {
		return "上轮提问「" + lastUser + "」"
	}
	return lastAssistant
}

func queryVaultWorkspaceEpisodicTail(memory *api.Memory, project string) string {
	if memory == nil || memory.Store == nil || memory.Store.DB() == nil || strings.TrimSpace(project) == "" {
		return ""
	}
	tc, err := memory.ThinClient()
	if err != nil || tc == nil || tc.Vault == nil {
		return ""
	}
	rows, err := memory.Store.DB().Query(
		`SELECT doc_id FROM thin_vault_docs
		 WHERE (layer = 'episodic' OR layer = '')
		   AND lower(trim(COALESCE(locus, ''))) = lower(trim(?))
		 ORDER BY occurred_at DESC LIMIT 8`,
		project,
	)
	if err != nil {
		return ""
	}
	var docIDs []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil && id != "" {
			docIDs = append(docIDs, id)
		}
	}
	rows.Close()
	var lastUser, lastAssistant string
	for _, docID := range docIDs {
		rawTxt, _, _, lErr := tc.Vault.LoadLocalDocument(docID)
		if lErr != nil || strings.TrimSpace(rawTxt) == "" {
			continue
		}
		for _, line := range strings.Split(rawTxt, "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "[user]") && lastUser == "" {
				body := pyStrip(ids.StripInjected(strings.TrimSpace(strings.TrimPrefix(trimmed, "[user]"))))
				if body != "" {
					lastUser = ids.Truncate(strings.ReplaceAll(body, "\n", " "), 60, "…")
				}
			} else if strings.HasPrefix(trimmed, "[assistant]") && lastAssistant == "" {
				body := pyStrip(ids.StripInjected(strings.TrimSpace(strings.TrimPrefix(trimmed, "[assistant]"))))
				if body != "" && !strings.HasPrefix(body, "技能判定：") {
					lastAssistant = ids.Truncate(strings.ReplaceAll(body, "\n", " "), 80, "…")
				}
			}
		}
		if lastUser != "" && lastAssistant != "" {
			break
		}
	}
	if lastUser != "" && lastAssistant != "" {
		return "上轮提问「" + lastUser + "」→ 进展「" + lastAssistant + "」"
	}
	if lastUser != "" {
		return "上轮提问「" + lastUser + "」"
	}
	return lastAssistant
}

// stripDegenerateTail strips LLM streaming degeneration tails where a single
// non-space character repeats >= 12 times consecutively (e.g. "]]]]]]]]]]]]...").
func stripDegenerateTail(s string) string {
	runes := []rune(s)
	if len(runes) < 16 {
		return s
	}
	// Scan for any run of >= 12 identical non-space characters and truncate from the start of that run
	runStart := 0
	for i := 1; i <= len(runes); i++ {
		if i < len(runes) && runes[i] == runes[runStart] && !unicode.IsSpace(runes[i]) {
			continue
		}
		if i-runStart >= 12 {
			return strings.TrimSpace(string(runes[:runStart]))
		}
		runStart = i
	}
	return s
}

// isCadenceReminderDue reports whether the current session has reached a 4-turn
// cadence boundary (turn 4, 8, 12, ...) so a lightweight (~40-token) attention
// anchor can be emitted on otherwise silent turns.
func isCadenceReminderDue(memory *api.Memory, scope, session string) bool {
	if memory == nil || memory.IsThinBrainProxy() || memory.Store == nil || memory.Store.DB() == nil || session == "" || session == "default" {
		return false
	}
	var userTurns int
	if !memory.Store.HasLegacyGraph() {
		key := "hook_cadence_turns:" + scope + ":" + session
		var raw []byte
		if err := memory.Store.DB().QueryRow(`SELECT v FROM thin_vault_meta WHERE k = ?`, key).Scan(&raw); err != nil {
			return false
		}
		n, err := strconv.Atoi(strings.TrimSpace(string(raw)))
		if err != nil || n < 4 {
			return false
		}
		return n%4 == 0
	}
	err := memory.Store.DB().QueryRow(
		`SELECT COUNT(*) FROM trace
		 WHERE scope = ? AND session = ? AND author IN ('user', 'albert')
		   AND meta_json NOT LIKE '%"synthetic_anchor"%'`,
		scope, session,
	).Scan(&userTurns)
	if err != nil || userTurns < 4 {
		return false
	}
	return userTurns%4 == 0
}

// stripMemoryAttributionLine removes the single-line natural memory impact
// attribution sentence (containing "Rolling-Memory 记忆") from the opening of an
// assistant reply before persisting to `trace`, preventing self-referential
// attribution lines from polluting episodic history or future assertions.
func stripMemoryAttributionLine(s string) string {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return s
	}
	lines := strings.Split(trimmed, "\n")
	checkIdx := 0
	if len(lines) > 1 && strings.HasPrefix(strings.TrimSpace(lines[0]), "技能判定：") {
		checkIdx = 1
		for checkIdx < len(lines) && strings.TrimSpace(lines[checkIdx]) == "" {
			checkIdx++
		}
	}
	if checkIdx < len(lines) {
		cand := strings.TrimSpace(lines[checkIdx])
		if strings.Contains(cand, "Rolling-Memory 记忆") && utf8.RuneCountInString(cand) <= 180 {
			remaining := append([]string{}, lines[:checkIdx]...)
			remaining = append(remaining, lines[checkIdx+1:]...)
			return strings.TrimSpace(strings.Join(remaining, "\n"))
		}
	}
	return s
}
