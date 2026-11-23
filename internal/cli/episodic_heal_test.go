// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rocareer/rolling-memory/internal/config"
	"github.com/rocareer/rolling-memory/internal/episodicunit"
	"github.com/rocareer/rolling-memory/internal/memory/facade"
	"github.com/rocareer/rolling-memory/internal/memory/localvault"
)

// TestHealDriftedEpisodicPointersReanchorsWithoutDeleting pins the regular-tick heal
// contract: drifted pointers are re-pointed and re-sealed, while provably-absent rows
// and unreadable sources are always kept — retiring them stays with `sync prune --apply`.
func TestHealDriftedEpisodicPointersReanchorsWithoutDeleting(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	source := filepath.Join(home, "transcript.jsonl")
	original := `{"type":"user","message":{"role":"user","content":"开始。"}}` + "\n" +
		`{"type":"assistant","message":{"role":"assistant","content":"回答一。"}}` + "\n" +
		`{"type":"assistant","message":{"role":"assistant","content":"回答二。"}}` + "\n"
	if err := os.WriteFile(source, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	goneSource := filepath.Join(home, "gone.jsonl")
	if err := os.WriteFile(goneSource, []byte(`{"type":"user","message":{"role":"user","content":"稍后删除。"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	memory, err := facade.New(&config.Config{DB: filepath.Join(home, "memory.db"), Scope: "mem:heal", Mode: "local"})
	if err != nil {
		t.Fatal(err)
	}
	defer memory.Close()
	tc, err := memory.LocalVault()
	if err != nil || tc == nil || tc.Vault == nil {
		t.Fatalf("local vault: %v", err)
	}
	turnRef := "@ref:jsonl:" + source + ":0"

	type unit struct{ id, ref, text string }
	units := []unit{
		{"", turnRef + "#2", ""},
		{"", turnRef + "#1", ""},
		{"", "@ref:jsonl:" + goneSource + ":0#0", ""},
	}
	for index := range units {
		text, rErr := localvault.ResolveEpisodicPointerLive(units[index].ref)
		if rErr != nil || strings.TrimSpace(text) == "" {
			t.Fatalf("resolve %s: %q %v", units[index].ref, text, rErr)
		}
		units[index].text = text
		// 真实导入的行身份是内容派生 DocID；同源重锚的双证以它为第二道门。
		units[index].id = episodicunit.DocID(memory.Config.Scope, text)
		unitTurnRef := units[index].ref[:strings.LastIndex(units[index].ref, "#")]
		if _, rErr := tc.RememberEpisodicByRef(context.Background(), units[index].id, units[index].ref, unitTurnRef, text, 1); rErr != nil {
			t.Fatal(rErr)
		}
	}
	driftedID, absentID, unreadableID := units[0].id, units[1].id, units[2].id
	// The turn re-enumerates without 回答一: the drifted row's content lives at #1 now
	// and the absent row's content is gone, while the third source disappears entirely.
	if err := os.WriteFile(source, []byte(`{"type":"user","message":{"role":"user","content":"开始。"}}`+"\n"+
		`{"type":"assistant","message":{"role":"assistant","content":"回答二。"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(goneSource); err != nil {
		t.Fatal(err)
	}

	scanned, repaired, err := healDriftedEpisodicPointers(context.Background(), memory, 100)
	if err != nil {
		t.Fatalf("heal pass: %v", err)
	}
	if scanned != 3 || repaired != 1 {
		t.Fatalf("heal pass = scanned %d repaired %d, want 3/1", scanned, repaired)
	}
	var healedPtr string
	if err := memory.Store.DB().QueryRow(`SELECT raw_text FROM rm_units WHERE doc_id=?`, driftedID).Scan(&healedPtr); err != nil || healedPtr != turnRef+"#1" {
		t.Fatalf("drifted pointer was not re-anchored in place: got %q err=%v", healedPtr, err)
	}
	var count int
	for _, id := range []string{driftedID, absentID, unreadableID} {
		if err := memory.Store.DB().QueryRow(`SELECT count(*) FROM rm_units WHERE doc_id=?`, id).Scan(&count); err != nil || count != 1 {
			t.Fatalf("heal must never delete rows: %s count=%d err=%v", id, count, err)
		}
	}

	// A second pass finds nothing left to repair and still deletes nothing.
	scanned, repaired, err = healDriftedEpisodicPointers(context.Background(), memory, 100)
	if err != nil {
		t.Fatalf("second heal pass: %v", err)
	}
	if scanned != 3 || repaired != 0 {
		t.Fatalf("second heal pass = scanned %d repaired %d, want 3/0", scanned, repaired)
	}

	// The cursor wraps and persists per scope: a bounded page still covers every row.
	scanned, repaired, err = healDriftedEpisodicPointers(context.Background(), memory, 2)
	if err != nil {
		t.Fatalf("bounded heal pass: %v", err)
	}
	if scanned > 2 {
		t.Fatalf("bounded heal pass scanned %d rows, want at most 2", scanned)
	}
	_ = repaired
}

// TestHealKeepsUnattributedAlternatesUntouched pins the automatic pass to same-source repair:
// when a row's primary session is gone but an identical-content alternate session exists, the
// sweep must NOT re-point the row to the unattributed alternate — that substitution stays
// rejected on the delivery side, and automation must not soften it.
func TestHealKeepsUnattributedAlternatesUntouched(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	memory, err := facade.New(&config.Config{DB: filepath.Join(home, "memory.db"), Scope: "mem:heal-alt", Mode: "local"})
	if err != nil {
		t.Fatal(err)
	}
	defer memory.Close()
	client, err := memory.LocalVault()
	if err != nil || client == nil || client.Vault == nil {
		t.Fatalf("local vault: %v", err)
	}
	dir := t.TempDir()
	primary := filepath.Join(dir, "primary.jsonl")
	alternate := filepath.Join(dir, "alternate.jsonl")
	body := `{"type":"user","message":{"role":"user","content":"quartz provenance witness"}}` + "\n"
	for _, path := range []string{primary, alternate} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	primaryTurn := "@ref:jsonl:" + primary + ":0"
	alternateTurn := "@ref:jsonl:" + alternate + ":0"
	text := "[user] quartz provenance witness"
	docID := episodicunit.DocID(memory.Config.Scope, text)
	packet, _ := client.Vault.PackBlindPacket(docID, text, 1)
	if err := client.Vault.SaveLocalEpisodicPointerIndexed(docID, 1, primaryTurn+"#0", primaryTurn, text, packet, localvault.IngestResponse{Layer: localvault.LayerEpisodic}); err != nil {
		t.Fatal(err)
	}
	if err := client.Vault.SaveLocalEpisodicPointerIndexed(docID, 2, alternateTurn+"#0", alternateTurn, text, packet, localvault.IngestResponse{Layer: localvault.LayerEpisodic}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(primary); err != nil {
		t.Fatal(err)
	}

	scanned, repaired, err := healDriftedEpisodicPointers(context.Background(), memory, 100)
	if err != nil {
		t.Fatalf("heal pass: %v", err)
	}
	if scanned != 1 || repaired != 0 {
		t.Fatalf("heal pass = scanned %d repaired %d, want 1/0 (cross-source alternate must stay untouched)", scanned, repaired)
	}
	var keptPtr string
	if err := memory.Store.DB().QueryRow(`SELECT raw_text FROM rm_units WHERE doc_id = ?`, docID).Scan(&keptPtr); err != nil || keptPtr != primaryTurn+"#0" {
		t.Fatalf("row was re-pointed to an unattributed alternate: got %q err=%v", keptPtr, err)
	}
}

// discoverHealJSONLRef 按内容找出 jsonl 源里承载 want 的那条指针（轮次/段号由实现枚举，
// 测试不假设编号约定）。
func discoverHealJSONLRef(t *testing.T, source, want string) string {
	t.Helper()
	for turn := 0; turn < 4; turn++ {
		for seg := 0; seg < 6; seg++ {
			ref := fmt.Sprintf("@ref:jsonl:%s:%d#%d", source, turn, seg)
			if text, err := localvault.ResolveEpisodicPointerLive(ref); err == nil && strings.Contains(text, want) {
				return ref
			}
		}
	}
	t.Fatalf("no jsonl pointer under %s resolves to %q", source, want)
	return ""
}

func healTestConfig(t *testing.T, home, scope string) *config.Config {
	t.Helper()
	configPath := filepath.Join(home, "config.toml")
	dbPath := filepath.Join(home, "memory.db")
	mode := "local"
	reader := "deliverable"
	cfg, err := config.Load(config.LoadOptions{
		Path: &configPath, DB: &dbPath, Mode: &mode, Scope: &scope, Reader: &reader,
		Env: map[string]string{},
	})
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	cfg.Log = filepath.Join(home, "state", "rolling-memory.log")
	return cfg
}

// TestHealReanchorsSameFileTurnDriftAndRecallRecovers 覆盖真实缺口的成因类别：同一文件里
// 前序轮次被删/被过滤后，后续轮次的序号整体位移一格。位移后交付侧按「未归属替代」拒绝
// （source_unavailable）；常规巡检把行重锚到封条内容现所在坐标（同文件），同一条 cue 恢复交付。
func TestHealReanchorsSameFileTurnDriftAndRecallRecovers(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	source := filepath.Join(home, "turns.jsonl")
	first := `{"type":"user","message":{"role":"user","content":"第一问：office hours"}}` + "\n" +
		`{"type":"assistant","message":{"role":"assistant","content":"回答一：ambient noise"}}` + "\n"
	second := `{"type":"user","message":{"role":"user","content":"第二问：quilted nebula checkpoint"}}` + "\n" +
		`{"type":"assistant","message":{"role":"assistant","content":"回答二：quilted nebula checkpoint sealed"}}` + "\n"
	if err := os.WriteFile(source, []byte(first+second), 0o600); err != nil {
		t.Fatal(err)
	}
	memory, err := facade.New(healTestConfig(t, home, "mem:heal-e2e"))
	if err != nil {
		t.Fatal(err)
	}
	defer memory.Close()
	client, err := memory.LocalVault()
	if err != nil || client == nil || client.Vault == nil {
		t.Fatalf("local vault: %v", err)
	}
	originalRef := discoverHealJSONLRef(t, source, "quilted nebula checkpoint sealed")
	text, err := localvault.ResolveEpisodicPointerLive(originalRef)
	if err != nil || !strings.Contains(text, "sealed") {
		t.Fatalf("resolve %s: %q %v", originalRef, text, err)
	}
	docID := episodicunit.DocID(memory.Config.Scope, text)
	packet, _ := client.Vault.PackBlindPacket(docID, text, 1)
	turnRef := originalRef[:strings.LastIndex(originalRef, "#")]
	if err := client.Vault.SaveLocalEpisodicPointerIndexed(docID, 1, originalRef, turnRef, text, packet, localvault.IngestResponse{Layer: localvault.LayerEpisodic}); err != nil {
		t.Fatal(err)
	}

	req := facade.RecallRequest{Cue: "quilted nebula checkpoint", NoCrystallize: true}
	fresh, err := memory.Recall(req)
	if err != nil || len(fresh.Bands) == 0 {
		t.Fatalf("fresh recall should deliver: bands=%d gaps=%v err=%v", len(fresh.Bands), fresh.Gaps, err)
	}

	// 同一文件内的前序轮次消失 ⇒ 序号整体位移一格。
	if err := os.WriteFile(source, []byte(second), 0o600); err != nil {
		t.Fatal(err)
	}
	drifted, err := memory.Recall(req)
	if err != nil || len(drifted.Bands) != 0 || drifted.Gaps["source_unavailable"] != 1 {
		t.Fatalf("drifted recall should be an honest gap: bands=%d gaps=%v err=%v", len(drifted.Bands), drifted.Gaps, err)
	}

	scanned, repaired, err := healDriftedEpisodicPointers(context.Background(), memory, 100)
	if err != nil || scanned != 1 || repaired != 1 {
		t.Fatalf("heal pass = scanned %d repaired %d err %v, want 1/1", scanned, repaired, err)
	}
	var healedRef string
	if err := memory.Store.DB().QueryRow(`SELECT raw_text FROM rm_units WHERE doc_id = ?`, docID).Scan(&healedRef); err != nil {
		t.Fatal(err)
	}
	if healedRef == originalRef {
		t.Fatalf("row was not re-anchored: %s", healedRef)
	}
	if got, _ := localvault.ResolveEpisodicPointerLive(healedRef); !strings.Contains(got, "sealed") {
		t.Fatalf("healed pointer does not address the sealed content: %s -> %q", healedRef, got)
	}
	recovered, err := memory.Recall(req)
	if err != nil || len(recovered.Bands) == 0 || recovered.Gaps["source_unavailable"] != 0 {
		t.Fatalf("recall after heal should deliver: bands=%d gaps=%v err=%v", len(recovered.Bands), recovered.Gaps, err)
	}
}

// TestHealSkipsTamperedIndexesWithoutTouchingRows：索引封条被篡改（MAC 不符）时分类为不可读，
// 巡检既不改也不删——宁漏勿错。
func TestHealSkipsTamperedIndexesWithoutTouchingRows(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	source := filepath.Join(home, "single.jsonl")
	body := `{"type":"user","message":{"role":"user","content":"tamper witness"}}` + "\n" +
		`{"type":"assistant","message":{"role":"assistant","content":"sealed tamper witness answer"}}` + "\n"
	if err := os.WriteFile(source, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	memory, err := facade.New(&config.Config{DB: filepath.Join(home, "memory.db"), Scope: "mem:heal-tamper", Mode: "local"})
	if err != nil {
		t.Fatal(err)
	}
	defer memory.Close()
	client, err := memory.LocalVault()
	if err != nil || client == nil || client.Vault == nil {
		t.Fatalf("local vault: %v", err)
	}
	ref := discoverHealJSONLRef(t, source, "sealed tamper witness answer")
	text, err := localvault.ResolveEpisodicPointerLive(ref)
	if err != nil {
		t.Fatal(err)
	}
	docID := episodicunit.DocID(memory.Config.Scope, text)
	packet, _ := client.Vault.PackBlindPacket(docID, text, 1)
	turnRef := ref[:strings.LastIndex(ref, "#")]
	if err := client.Vault.SaveLocalEpisodicPointerIndexed(docID, 1, ref, turnRef, text, packet, localvault.IngestResponse{Layer: localvault.LayerEpisodic}); err != nil {
		t.Fatal(err)
	}
	if _, err := memory.Store.DB().Exec(`UPDATE rm_episodic_index SET payload_mac = x'00' WHERE doc_id = ?`, docID); err != nil {
		t.Fatal(err)
	}

	scanned, repaired, err := healDriftedEpisodicPointers(context.Background(), memory, 100)
	if err != nil || scanned != 1 || repaired != 0 {
		t.Fatalf("heal pass = scanned %d repaired %d err %v, want 1/0", scanned, repaired, err)
	}
	var keptPtr string
	if err := memory.Store.DB().QueryRow(`SELECT raw_text FROM rm_units WHERE doc_id = ?`, docID).Scan(&keptPtr); err != nil || keptPtr != ref {
		t.Fatalf("tampered row was modified: %q err=%v", keptPtr, err)
	}
	var indexRows int
	if err := memory.Store.DB().QueryRow(`SELECT COUNT(*) FROM rm_episodic_index WHERE doc_id = ?`, docID).Scan(&indexRows); err != nil || indexRows != 1 {
		t.Fatalf("tampered index row was removed: %d err=%v", indexRows, err)
	}
}

// TestHealSweepCursorWrapsAndEventuallyRevisits：游标越过后才漂移的行在回卷后仍会被修到
// （无插入洪流时不会永久漏掉），证明分页游标不会造成饥饿。
func TestHealSweepCursorWrapsAndEventuallyRevisits(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	memory, err := facade.New(&config.Config{DB: filepath.Join(home, "memory.db"), Scope: "mem:heal-wrap", Mode: "local"})
	if err != nil {
		t.Fatal(err)
	}
	defer memory.Close()
	client, err := memory.LocalVault()
	if err != nil || client == nil || client.Vault == nil {
		t.Fatalf("local vault: %v", err)
	}
	segmented := `{"type":"user","message":{"role":"user","content":"wrap probe %s"}}` + "\n" +
		`{"type":"assistant","message":{"role":"assistant","content":"wrap keep alpha %s"}}` + "\n" +
		`{"type":"assistant","message":{"role":"assistant","content":"wrap keep beta %s"}}` + "\n"
	wrap1ID := ""
	for _, name := range []string{"wrap1", "wrap2", "wrap3"} {
		source := filepath.Join(home, name+".jsonl")
		body := fmt.Sprintf(segmented, name, name, name)
		if err := os.WriteFile(source, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		ref := discoverHealJSONLRef(t, source, "wrap keep beta "+name)
		text, rErr := localvault.ResolveEpisodicPointerLive(ref)
		if rErr != nil {
			t.Fatal(rErr)
		}
		docID := episodicunit.DocID(memory.Config.Scope, text)
		if name == "wrap1" {
			wrap1ID = docID
		}
		packet, _ := client.Vault.PackBlindPacket(docID, text, 1)
		turnRef := ref[:strings.LastIndex(ref, "#")]
		if err := client.Vault.SaveLocalEpisodicPointerIndexed(docID, 1, ref, turnRef, text, packet, localvault.IngestResponse{Layer: localvault.LayerEpisodic}); err != nil {
			t.Fatal(err)
		}
	}
	// 第一页（limit=2）越过按序最前的行，之后才制造漂移：只有回卷才能再访到它。
	if _, _, err := healDriftedEpisodicPointers(context.Background(), memory, 2); err != nil {
		t.Fatal(err)
	}
	driftedSource := filepath.Join(home, "wrap1.jsonl")
	reduced := `{"type":"user","message":{"role":"user","content":"wrap probe wrap1"}}` + "\n" +
		`{"type":"assistant","message":{"role":"assistant","content":"wrap keep beta wrap1"}}` + "\n"
	if err := os.WriteFile(driftedSource, []byte(reduced), 0o600); err != nil {
		t.Fatal(err)
	}

	totalRepaired := 0
	for pass := 0; pass < 4 && totalRepaired == 0; pass++ {
		if _, repaired, err := healDriftedEpisodicPointers(context.Background(), memory, 2); err != nil {
			t.Fatal(err)
		} else {
			totalRepaired += repaired
		}
	}
	if totalRepaired == 0 {
		t.Fatal("wrapped sweep never revisited the drifted row")
	}
	var healedRef string
	if err := memory.Store.DB().QueryRow(`SELECT raw_text FROM rm_units WHERE doc_id = ?`, wrap1ID).Scan(&healedRef); err != nil {
		t.Fatal(err)
	}
	if got, _ := localvault.ResolveEpisodicPointerLive(healedRef); !strings.Contains(got, "wrap keep beta") {
		t.Fatalf("wrapped sweep healed the wrong coordinate: %s -> %q", healedRef, got)
	}
}

// TestSameEpisodicSourceSessionPredicate 钉住同源判据本身：jsonl 认文件、zcode 认会话，
// 跨类与畸形一律 false。
func TestSameEpisodicSourceSessionPredicate(t *testing.T) {
	cases := []struct {
		name string
		a, b string
		want bool
	}{
		{"jsonl same file different turn", "@ref:jsonl:/tmp/a.jsonl:3#0", "@ref:jsonl:/tmp/a.jsonl:2#1", true},
		{"jsonl different file", "@ref:jsonl:/tmp/a.jsonl:3#0", "@ref:jsonl:/tmp/b.jsonl:3#0", false},
		{"zcode same session", "@ref:zcode:sess_x:msg_a#1", "@ref:zcode:sess_x:msg_b#0", true},
		{"zcode different session", "@ref:zcode:sess_x:msg_a#1", "@ref:zcode:sess_y:msg_b#0", false},
		{"opencode same session", "@ref:opencode:sess_x:msg_a#1", "@ref:opencode:sess_x:msg_b#0", true},
		{"opencode different session", "@ref:opencode:sess_x:msg_a#1", "@ref:opencode:sess_y:msg_b#0", false},
		{"cross kind", "@ref:zcode:sess_x:msg_a#1", "@ref:jsonl:/tmp/a.jsonl:0#0", false},
		{"cross kind sqlite", "@ref:opencode:sess_x:msg_a#1", "@ref:zcode:sess_x:msg_b#0", false},
		{"malformed second", "@ref:jsonl:/tmp/a.jsonl:3#0", "junk", false},
	}
	for _, tc := range cases {
		if got := sameEpisodicSourceSession(tc.a, tc.b); got != tc.want {
			t.Fatalf("%s: sameEpisodicSourceSession(%q, %q) = %v, want %v", tc.name, tc.a, tc.b, got, tc.want)
		}
	}
}

// TestPruneReanchorsRenumberedUnitsInsteadOfDeletingThem 是删除侧的反例护栏：前插一轮使存量
// 轮次序号整体后移后，分类器会把该行看成 absent；prune 在回收前必须先用同源摘要重扫找回真实
// 坐标——内容仍活在同一文件里的行绝不允许被删，只有重扫也找不到的才算确证不存在。
func TestPruneReanchorsRenumberedUnitsInsteadOfDeletingThem(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	source := filepath.Join(home, "turns.jsonl")
	oldFirst := `{"type":"user","message":{"role":"user","content":"第一问：office hours"}}` + "\n" +
		`{"type":"assistant","message":{"role":"assistant","content":"回答一：ambient noise"}}` + "\n"
	second := `{"type":"user","message":{"role":"user","content":"第二问：quilted nebula checkpoint"}}` + "\n" +
		`{"type":"assistant","message":{"role":"assistant","content":"回答二：quilted nebula checkpoint sealed"}}` + "\n"
	if err := os.WriteFile(source, []byte(oldFirst+second), 0o600); err != nil {
		t.Fatal(err)
	}
	memory, err := facade.New(&config.Config{DB: filepath.Join(home, "memory.db"), Scope: "mem:prune-guard", Mode: "local"})
	if err != nil {
		t.Fatal(err)
	}
	defer memory.Close()
	client, err := memory.LocalVault()
	if err != nil || client == nil || client.Vault == nil {
		t.Fatalf("local vault: %v", err)
	}
	originalRef := discoverHealJSONLRef(t, source, "quilted nebula checkpoint sealed")
	text, err := localvault.ResolveEpisodicPointerLive(originalRef)
	if err != nil {
		t.Fatal(err)
	}
	docID := episodicunit.DocID(memory.Config.Scope, text)
	packet, _ := client.Vault.PackBlindPacket(docID, text, 1)
	turnRef := originalRef[:strings.LastIndex(originalRef, "#")]
	if err := client.Vault.SaveLocalEpisodicPointerIndexed(docID, 1, originalRef, turnRef, text, packet, localvault.IngestResponse{Layer: localvault.LayerEpisodic}); err != nil {
		t.Fatal(err)
	}
	prepended := `{"type":"user","message":{"role":"user","content":"第零问：inserted"}}` + "\n" +
		`{"type":"assistant","message":{"role":"assistant","content":"回答零：inserted filler"}}` + "\n"
	if err := os.WriteFile(source, []byte(prepended+oldFirst+second), 0o600); err != nil {
		t.Fatal(err)
	}

	dry, err := pruneEpisodicUnits(context.Background(), memory, 100, false, false)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if dry.Absent != 0 || dry.Removed != 0 || dry.Reanchorable != 1 || len(dry.Candidates) != 0 {
		t.Fatalf("dry run = %+v, want the renumbered row re-anchorable and not a candidate", dry)
	}
	var dryCount int
	if err := memory.Store.DB().QueryRow(`SELECT COUNT(*) FROM rm_units WHERE doc_id = ?`, docID).Scan(&dryCount); err != nil || dryCount != 1 {
		t.Fatalf("dry run must not delete: count=%d err=%v", dryCount, err)
	}

	applied, err := pruneEpisodicUnits(context.Background(), memory, 100, true, true)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if applied.Removed != 0 || applied.Reanchored != 1 || applied.Absent != 0 {
		t.Fatalf("apply = %+v, want zero removals and one re-anchor", applied)
	}
	var keptRef string
	if err := memory.Store.DB().QueryRow(`SELECT raw_text FROM rm_units WHERE doc_id = ?`, docID).Scan(&keptRef); err != nil {
		t.Fatalf("row was deleted: %v", err)
	}
	if keptRef == originalRef {
		t.Fatalf("row was not re-anchored: %s", keptRef)
	}
	if got, _ := localvault.ResolveEpisodicPointerLive(keptRef); !strings.Contains(got, "sealed") {
		t.Fatalf("re-anchored pointer does not address the sealed content: %s -> %q", keptRef, got)
	}
}

// TestPruneRefusesCrossSourceReanchorWithoutExplicitOptIn：主源消失、备用内容在**另一会话**时，
// prune 默认只统计跳过、绝不把行指向未归属替代；只有显式 ReanchorCrossSource 才允许跨源重指。
func TestPruneRefusesCrossSourceReanchorWithoutExplicitOptIn(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	memory, err := facade.New(&config.Config{DB: filepath.Join(home, "memory.db"), Scope: "mem:prune-cross", Mode: "local"})
	if err != nil {
		t.Fatal(err)
	}
	defer memory.Close()
	client, err := memory.LocalVault()
	if err != nil || client == nil || client.Vault == nil {
		t.Fatalf("local vault: %v", err)
	}
	dir := t.TempDir()
	primary := filepath.Join(dir, "primary.jsonl")
	alternate := filepath.Join(dir, "alternate.jsonl")
	body := `{"type":"user","message":{"role":"user","content":"cross-source witness quartz"}}` + "\n"
	for _, path := range []string{primary, alternate} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	primaryTurn := "@ref:jsonl:" + primary + ":0"
	alternateTurn := "@ref:jsonl:" + alternate + ":0"
	text := "[user] cross-source witness quartz"
	docID := episodicunit.DocID(memory.Config.Scope, text)
	packet, _ := client.Vault.PackBlindPacket(docID, text, 1)
	if err := client.Vault.SaveLocalEpisodicPointerIndexed(docID, 1, primaryTurn+"#0", primaryTurn, text, packet, localvault.IngestResponse{Layer: localvault.LayerEpisodic}); err != nil {
		t.Fatal(err)
	}
	if err := client.Vault.SaveLocalEpisodicPointerIndexed(docID, 2, alternateTurn+"#0", alternateTurn, text, packet, localvault.IngestResponse{Layer: localvault.LayerEpisodic}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(primary); err != nil {
		t.Fatal(err)
	}

	dry, err := pruneEpisodicUnits(context.Background(), memory, 100, false, false)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if dry.Relocatable != 1 || dry.Reanchorable != 0 || dry.CrossSourceSkipped != 1 || dry.Absent != 0 || len(dry.Candidates) != 0 {
		t.Fatalf("dry run = %+v, want the cross-source alternate skipped, not a candidate", dry)
	}
	applied, err := pruneEpisodicUnits(context.Background(), memory, 100, true, true)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if applied.Reanchored != 0 || applied.CrossSourceSkipped != 1 || applied.Removed != 0 {
		t.Fatalf("apply = %+v, want nothing re-anchored or removed by default", applied)
	}
	var keptPtr string
	if err := memory.Store.DB().QueryRow(`SELECT raw_text FROM rm_units WHERE doc_id = ?`, docID).Scan(&keptPtr); err != nil || keptPtr != primaryTurn+"#0" {
		t.Fatalf("row was re-pointed to an unattributed alternate: %q err=%v", keptPtr, err)
	}

	// The explicit opt-in is the only path that crosses sessions, and it re-anchors the
	// row onto the verified alternate coordinate.
	optIn, err := pruneEpisodicUnitsWithOptions(context.Background(), memory, EpisodicPruneOptions{
		Limit: 100, Apply: true, Confirmed: true, ReanchorCrossSource: true,
	})
	if err != nil {
		t.Fatalf("opt-in apply: %v", err)
	}
	if optIn.Reanchored != 1 || optIn.Removed != 0 || optIn.CrossSourceSkipped != 0 {
		t.Fatalf("opt-in apply = %+v, want exactly one cross-source re-anchor", optIn)
	}
	if err := memory.Store.DB().QueryRow(`SELECT raw_text FROM rm_units WHERE doc_id = ?`, docID).Scan(&keptPtr); err != nil || keptPtr != alternateTurn+"#0" {
		t.Fatalf("opt-in did not re-anchor onto the alternate: %q err=%v", keptPtr, err)
	}
}

// TestEpisodicHealNextCursorLeavesUnprocessedRows pins the cursor contract: a
// pass that yields or is cancelled before finishing its page must not record
// the rows it did not reach as inspected.
func TestEpisodicHealNextCursorLeavesUnprocessedRows(t *testing.T) {
	rows := []episodicHealRow{{docID: "a"}, {docID: "b"}, {docID: "c"}}
	cases := []struct {
		name      string
		cursor    string
		rows      []episodicHealRow
		processed int
		limit     int
		want      string
	}{
		{"yield after one row", "", rows, 1, 3, "a"},
		{"yield before any row keeps the cursor", "prev", rows, 0, 3, "prev"},
		{"full page advances to the last row", "", rows, 3, 3, "c"},
		{"short page wraps to the beginning", "", rows[:2], 2, 3, ""},
	}
	for _, testCase := range cases {
		if got := episodicHealNextCursor(testCase.cursor, testCase.rows, testCase.processed, testCase.limit); got != testCase.want {
			t.Fatalf("%s: cursor = %q, want %q", testCase.name, got, testCase.want)
		}
	}
}
