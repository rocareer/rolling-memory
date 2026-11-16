// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package thinclient

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/rocareer/rolling-memory/internal/episodicunit"
	ramesdk "github.com/rocareer/rolling-memory/sdk/go"
	_ "modernc.org/sqlite"
)

func TestEpisodicPointerIndexJSONL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")
	body := `{"type":"user","message":{"role":"user","content":"quartz nebula lighthouse"}}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "vault.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	v, err := NewVault(db, []byte("episodic-index-test"))
	if err != nil {
		t.Fatal(err)
	}
	ref := "@ref:jsonl:" + path + ":0#0"
	live, err := ResolveEpisodicPointerLive(ref)
	if err != nil {
		t.Fatal(err)
	}
	c := NewClient(v, nil)
	if _, err := c.RememberEpisodicByRef(context.Background(), "doc", ref, "", live, 1); err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := db.QueryRow(`SELECT raw_text FROM thin_vault_docs WHERE doc_id='doc'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != ref || strings.Contains(stored, "quartz") {
		t.Fatalf("unexpected stored text %q", stored)
	}
	assertHits := func(query string, want bool) {
		t.Helper()
		hits, err := v.SearchClientSDKDefault(query, 10)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, hit := range hits {
			found = found || hit.UnitID == "doc"
		}
		if found != want {
			t.Errorf("default query %q: got %+v", query, hits)
		}
		pkt, _ := v.PackBlindPacket("query", query, 0)
		hits, err = NewLocalVaultTransport(v).QueryBlindManifold(context.Background(), pkt, 10)
		if err != nil {
			t.Fatal(err)
		}
		found = false
		for _, hit := range hits {
			found = found || hit.UnitID == "doc"
		}
		if found != want {
			t.Errorf("transport query %q: got %+v", query, hits)
		}
	}
	assertHits("quartz nebula", true)
	assertHits("unrelatedviolet", false)
	var payload []byte
	if err := db.QueryRow(`SELECT payload FROM thin_vault_episodic_index WHERE doc_id='doc'`).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(payload, []byte("quartz")) {
		t.Fatal("index contains plaintext")
	}
	slots, frames, err := ramesdk.DecodeWireBytes(payload)
	if err != nil {
		t.Fatal(err)
	}
	expected, _ := v.PackBlindPacket("doc", live, 1)
	if !reflect.DeepEqual(slots, expected.Slots) || !reflect.DeepEqual(frames, expected.SentenceFrames) {
		t.Fatal("canonical packet did not round trip")
	}
}

func TestEpisodicIndexEnsureAndCrossInstance(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vault.sqlite")
	db1, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db1.Close()
	db2, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	v1, err := NewVault(db1, []byte("shared-key"))
	if err != nil {
		t.Fatal(err)
	}
	v2, err := NewVault(db2, []byte("shared-key"))
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(dir, "source.jsonl")
	ref := "@ref:jsonl:" + source + ":0#0"
	if _, err := v2.SearchClientSDKDefault("quartz", 10); err != nil {
		t.Fatal(err)
	}
	for _, word := range []string{"quartz", "nebula", "marble", "violet"} {
		if _, err := db1.Exec(`DELETE FROM thin_vault_docs WHERE doc_id='doc'`); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(source, []byte(`{"type":"user","message":{"role":"user","content":"`+word+`"}}`+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		text, err := ResolveEpisodicPointerLive(ref)
		if err != nil {
			t.Fatal(err)
		}
		pkt, _ := v1.PackBlindPacket("doc", text, 1)
		if err := v1.SaveLocalEpisodicPointerIndexed("doc", 1, ref, "", text, pkt, CloudIngestResponse{}); err != nil {
			t.Fatal(err)
		}
		hits, err := v2.SearchClientSDKDefault(text, 10)
		if err != nil || len(hits) != 1 || hits[0].UnitID != "doc" {
			t.Fatalf("cross instance %q: %+v %v", text, hits, err)
		}
	}
	if _, err := db1.Exec(`DELETE FROM thin_vault_docs WHERE doc_id='doc'`); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte(`{"type":"user","message":{"role":"user","content":"quartz"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	text, err := ResolveEpisodicPointerLive(ref)
	if err != nil {
		t.Fatal(err)
	}
	const scope = "ensure-test"
	docID := episodicunit.DocID(scope, text)
	if err := v1.SaveLocalEpisodicPointer(docID, 1, ref, "", CloudIngestResponse{}); err != nil {
		t.Fatal(err)
	}
	created, err := v1.EnsureLocalEpisodicIndex(scope, docID, ref, text)
	if err != nil || !created {
		t.Fatalf("ensure: %v %v", created, err)
	}
	created, err = v1.EnsureLocalEpisodicIndex(scope, docID, ref, text)
	if err != nil || created {
		t.Fatalf("idempotent ensure: %v %v", created, err)
	}
	if _, err = v1.EnsureLocalEpisodicIndex(scope, docID, ref+"x", text); err == nil {
		t.Fatal("changed pointer accepted")
	}
	hits, err := v2.SearchClientSDKDefault("quartz", 10)
	if err != nil || len(hits) != 1 {
		t.Fatalf("cross-instance ensure: %+v %v", hits, err)
	}
	other, err := NewVault(db2, []byte("different-key"))
	if err != nil {
		t.Fatal(err)
	}
	compatible, incompatible, missing, err := other.EpisodicIndexDiagnostics()
	if err != nil || compatible != 0 || incompatible != 1 || missing != 0 {
		t.Fatalf("diagnostics %d %d %d %v", compatible, incompatible, missing, err)
	}
	if hits, err := other.SearchClientSDKDefault("quartz", 10); err != nil || len(hits) != 0 {
		t.Fatalf("incompatible index: %+v %v", hits, err)
	}
	// Cross-generation union read: a vault opened with a different encoder
	// configuration but the SAME secret must still read rows indexed by an
	// earlier codec generation — an SDK or language-pack bump must never orphan
	// previously indexed speech (payload is self-describing, MAC-authenticated
	// per row, and the current generation is merely preferred over older ones).
	// The secret remains the access boundary: the different-key vault above
	// still sees nothing.
	clone := v1.CloneWithSharedSlotCache()
	clone.SetCanonicalHarmonics(ramesdk.CanonicalHarmonicOptions{EnableStructuredDates: true})
	if hits, err := clone.SearchClientSDKDefault("quartz", 10); err != nil || len(hits) != 1 {
		t.Fatalf("clone config union read: %+v %v", hits, err)
	}
	if hits, err := v1.SearchClientSDKDefault("quartz", 10); err != nil || len(hits) != 1 {
		t.Fatalf("original config: %+v %v", hits, err)
	}
}

func TestEpisodicIndexContentIdentity(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "source.jsonl")
	writeSource := func(word string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(`{"type":"user","message":{"role":"user","content":"`+word+`"}}`+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeSource("quartz")
	ref := "@ref:jsonl:" + path + ":0#0"
	db, err := sql.Open("sqlite", filepath.Join(dir, "vault.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	v, err := NewVault(db, []byte("identity-key"))
	if err != nil {
		t.Fatal(err)
	}
	original, err := ResolveEpisodicPointerLive(ref)
	if err != nil {
		t.Fatal(err)
	}
	pkt, _ := v.PackBlindPacket("doc", original, 1)
	if err := v.SaveLocalEpisodicPointerIndexed("doc", 1, ref, "", original, pkt, CloudIngestResponse{}); err != nil {
		t.Fatal(err)
	}
	var digest, payload []byte
	if err := db.QueryRow(`SELECT content_digest, payload FROM thin_vault_episodic_index WHERE doc_id='doc'`).Scan(&digest, &payload); err != nil {
		t.Fatal(err)
	}
	if len(digest) == 0 || bytes.Contains(digest, []byte(original)) {
		t.Fatal("digest is missing or contains source text")
	}
	if got, _, _, err := v.LoadLocalDocument("doc"); err != nil || got != original {
		t.Fatalf("initial hydration %q: %v", got, err)
	}
	writeSource("nebula")
	if got, _, _, err := v.LoadLocalDocument("doc"); err == nil || got != "" {
		t.Fatalf("changed source delivered %q: %v", got, err)
	}
	changed, err := ResolveEpisodicPointerLive(ref)
	if err != nil {
		t.Fatal(err)
	}
	changedPkt, _ := v.PackBlindPacket("doc", changed, 1)
	if err := v.SaveLocalEpisodicPointerIndexed("doc", 1, ref, "", changed, changedPkt, CloudIngestResponse{}); err == nil {
		t.Fatal("changed source overwrote established identity")
	}
	if _, err := v.EnsureLocalEpisodicIndex("identity-test", "doc", ref, changed); err == nil {
		t.Fatal("backfill overwrote established identity")
	}
	wrongPkt, _ := v.PackBlindPacket("new", original, 1)
	if err := v.SaveLocalEpisodicPointerIndexed("new", 1, ref, "", original, wrongPkt, CloudIngestResponse{}); err == nil {
		t.Fatal("mismatched supplied text accepted")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if got, _, _, err := v.LoadLocalDocument("doc"); err == nil || got != "" {
		t.Fatalf("missing source delivered %q: %v", got, err)
	}
	if _, err := v.EnsureLocalEpisodicIndex("identity-test", episodicunit.DocID("identity-test", ref), ref, ref); err == nil {
		t.Fatal("unresolved pointer encoded as source")
	}
	var afterDigest, afterPayload []byte
	if err := db.QueryRow(`SELECT content_digest, payload FROM thin_vault_episodic_index WHERE doc_id='doc'`).Scan(&afterDigest, &afterPayload); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(digest, afterDigest) || !bytes.Equal(payload, afterPayload) {
		t.Fatal("failed operation changed existing index")
	}
}

func TestEpisodicEnsureAndRecentIdentity(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.jsonl")
	writeSource := func(text string) {
		t.Helper()
		body := `{"type":"user","message":{"role":"user","content":"` + text + `"}}` + "\n"
		if err := os.WriteFile(source, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeSource("原文 quartz")
	ref := "@ref:jsonl:" + source + ":0#0"
	original, err := ResolveEpisodicPointerLive(ref)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "vault.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	v, err := NewVault(db, []byte("recent-identity-key"))
	if err != nil {
		t.Fatal(err)
	}
	const scope = "mem:recent-identity"
	id := episodicunit.DocID(scope, original)
	if err := v.SaveLocalEpisodicPointer(id, 1, ref, "", CloudIngestResponse{}); err != nil {
		t.Fatal(err)
	}
	checkRecent := func(want string) {
		t.Helper()
		entries, err := v.RecentDocs(10)
		if err != nil || len(entries) != 1 || entries[0].ID != id || entries[0].Text != want {
			t.Fatalf("RecentDocs: %+v %v, want %q", entries, err, want)
		}
		entry, err := v.RecentSourceDoc(10)
		if err != nil || entry == nil || entry.ID != id || entry.Text != want {
			t.Fatalf("RecentSourceDoc: %+v %v, want %q", entry, err, want)
		}
	}
	// Gap delivery must not expose either the source pointer/path or its content.
	gap := "[来源缺口：指针身份未验证或内容不可用]"
	checkRecent(gap)
	for _, wrongScope := range []string{"", "mem:other"} {
		if _, err := v.EnsureLocalEpisodicIndex(wrongScope, id, ref, original); err == nil {
			t.Fatalf("scope %q accepted", wrongScope)
		}
	}
	writeSource("changed nebula")
	changed, err := ResolveEpisodicPointerLive(ref)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.EnsureLocalEpisodicIndex(scope, id, ref, changed); err == nil {
		t.Fatal("changed source indexed under old ID")
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM thin_vault_episodic_index WHERE doc_id = ?`, id).Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed ensure wrote index: %d %v", count, err)
	}
	checkRecent(gap)
	writeSource("原文 quartz")
	if created, err := v.EnsureLocalEpisodicIndex(scope, id, ref, original); err != nil || !created {
		t.Fatalf("ensure original: %v %v", created, err)
	}
	checkRecent(original)
	writeSource("changed nebula")
	checkRecent(gap)
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	checkRecent(gap)
	var pointer string
	if err := db.QueryRow(`SELECT raw_text FROM thin_vault_docs WHERE doc_id = ?`, id).Scan(&pointer); err != nil || pointer != ref {
		t.Fatalf("legacy source row changed: %q %v", pointer, err)
	}
}

func TestEpisodicIndexAtomicMismatch(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "vault.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	v, err := NewVault(db, []byte("key"))
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "source.jsonl")
	if err := os.WriteFile(source, []byte(`{"type":"user","message":{"role":"user","content":"quartz"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ref := "@ref:jsonl:" + source + ":0#0"
	text, err := ResolveEpisodicPointerLive(ref)
	if err != nil {
		t.Fatal(err)
	}
	pkt, _ := v.PackBlindPacket("doc", "different", 1)
	if err := v.SaveLocalEpisodicPointerIndexed("doc", 1, ref, "", text, pkt, CloudIngestResponse{}); err == nil {
		t.Fatal("mismatched packet accepted")
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM thin_vault_docs`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial write: %d %v", count, err)
	}
	if _, err := db.Exec(`CREATE TRIGGER fail_index BEFORE INSERT ON thin_vault_episodic_index BEGIN SELECT RAISE(ABORT, 'test failure'); END`); err != nil {
		t.Fatal(err)
	}
	pkt, _ = v.PackBlindPacket("doc", text, 1)
	if err := v.SaveLocalEpisodicPointerIndexed("doc", 1, ref, "", text, pkt, CloudIngestResponse{}); err == nil {
		t.Fatal("write failure ignored")
	}
	if err := db.QueryRow(`SELECT count(*) FROM thin_vault_docs`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial write: %d %v", count, err)
	}
}

// TestEpisodicPointerRepointSameContent 锁定「同一内容随重新定段平移 `#<k>`」时的自愈：
// docID 内容寻址，正文与记录摘要一致且仍属同一轮指针干时，允许把存储指针改指到当前枚举给出的
// 新下标（并重算绑定 ref_pointer 的 index MAC）；否则重新定段（例如排除过程旁白后下标整体前移）
// 会让整轮写入被永久拒绝，解析侧只剩「来源缺口」。跨轮改指必须拒绝。
func TestEpisodicPointerRepointSameContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")
	body := `{"type":"user","message":{"role":"user","content":"开始。"}}` + "\n" +
		`{"type":"assistant","message":{"role":"assistant","content":"同一句。"}}` + "\n" +
		`{"type":"assistant","message":{"role":"assistant","content":"同一句。"}}` + "\n" +
		`{"type":"user","message":{"role":"user","content":"另一轮。"}}` + "\n" +
		`{"type":"assistant","message":{"role":"assistant","content":"同一句。"}}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "vault.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	v, err := NewVault(db, []byte("repoint-test-secret"))
	if err != nil {
		t.Fatal(err)
	}
	client := NewClient(v, nil)
	turnRef := "@ref:jsonl:" + path + ":0"
	ref1 := turnRef + "#1"
	ref2 := turnRef + "#2"
	text, err := ResolveEpisodicPointerLive(ref1)
	if err != nil {
		t.Fatal(err)
	}
	if text != "[assistant] 同一句。" {
		t.Fatalf("unexpected resolved text %q", text)
	}
	if _, err := client.RememberEpisodicByRef(context.Background(), "doc", ref1, turnRef, text, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := client.RememberEpisodicByRef(context.Background(), "doc", ref2, turnRef, text, 1); err != nil {
		t.Fatalf("re-point same content: %v", err)
	}
	var storedRaw, storedRef string
	if err := db.QueryRow(`SELECT raw_text FROM thin_vault_docs WHERE doc_id='doc'`).Scan(&storedRaw); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT ref_pointer FROM thin_vault_episodic_index WHERE doc_id='doc'`).Scan(&storedRef); err != nil {
		t.Fatal(err)
	}
	if storedRaw != ref2 || storedRef != ref2 {
		t.Fatalf("pointer not re-pointed: doc=%q index=%q", storedRaw, storedRef)
	}
	if got, err := v.LoadVerifiedEpisodicPointer("doc", ref2); err != nil || got != text {
		t.Fatalf("re-pointed pointer fails verification: %q %v", got, err)
	}
	if _, err := v.LoadVerifiedEpisodicPointer("doc", ref1); err == nil {
		t.Fatal("stale segment pointer still resolves")
	}
	if _, err := client.RememberEpisodicByRef(context.Background(), "doc2", ref1, turnRef, text, 1); err != nil {
		t.Fatal(err)
	}
	refOtherTurn := "@ref:jsonl:" + path + ":1#1"
	if _, err := client.RememberEpisodicByRef(context.Background(), "doc2", refOtherTurn, "@ref:jsonl:"+path+":1", text, 2); err == nil || !strings.Contains(err.Error(), "different source") {
		t.Fatalf("cross-turn repoint must be refused, got %v", err)
	}
}

// TestEpisodicPointerRelocatesByContentDigest 锁定「定位下标漂移后按内容身份重定位」：
// 存储的 `#<k>` 是位置量，会随分段过滤演化或宿主追加步骤而平移；docID 与记录摘要都
// 绑定同一条正文，重定位只允许在同一轮内命中摘要完全一致的单元，找不到就 fail-closed。
func TestEpisodicPointerRelocatesByContentDigest(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")
	original := `{"type":"user","message":{"role":"user","content":"开始。"}}` + "\n" +
		`{"type":"assistant","message":{"role":"assistant","content":"回答一。"}}` + "\n" +
		`{"type":"assistant","message":{"role":"assistant","content":"回答二。"}}` + "\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "vault.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	v, err := NewVault(db, []byte("digest-relocate-secret"))
	if err != nil {
		t.Fatal(err)
	}
	client := NewClient(v, nil)
	turnRef := "@ref:jsonl:" + path + ":0"
	keptRef := turnRef + "#2"
	keptText, err := ResolveEpisodicPointerLive(keptRef)
	if err != nil || keptText != "[assistant] 回答二。" {
		t.Fatalf("kept text = %q err=%v", keptText, err)
	}
	droppedRef := turnRef + "#1"
	droppedText, err := ResolveEpisodicPointerLive(droppedRef)
	if err != nil || droppedText != "[assistant] 回答一。" {
		t.Fatalf("dropped text = %q err=%v", droppedText, err)
	}
	for id, ref := range map[string]string{"kept": keptRef, "dropped": droppedRef} {
		text := keptText
		if id == "dropped" {
			text = droppedText
		}
		if _, err := client.RememberEpisodicByRef(context.Background(), id, ref, turnRef, text, 1); err != nil {
			t.Fatal(err)
		}
	}
	// 分段过滤演化：同一轮里前一条旁白不再枚举，`#2` 越界、`#1` 指向他文本。
	if err := os.WriteFile(path, []byte(`{"type":"user","message":{"role":"user","content":"开始。"}}`+"\n"+
		`{"type":"assistant","message":{"role":"assistant","content":"回答二。"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := v.LoadVerifiedEpisodicPointer("kept", keptRef); err != nil || got != keptText {
		t.Fatalf("relocated pointer = %q err=%v, want %q", got, err, keptText)
	}
	if _, err := v.LoadVerifiedEpisodicPointer("dropped", droppedRef); err == nil {
		t.Fatal("pointer without a content match must fail closed")
	}
}
