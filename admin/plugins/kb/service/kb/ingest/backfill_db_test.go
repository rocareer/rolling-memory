// plugin: kb v1.0.0
// 匹配面回填连库用例：ReindexMatchText（aigo kb reindex 的核心）幂等 /
// DryRun / 复算单点（chunk.RowTextOf）/ 响亮失败。
//
// 门控与跑法（同 ingest 包先例）：PLUGIN_KB_DB=1 go test -count=1 -timeout=120s ./plugins/kb/service/kb/ingest/
package ingest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ai-go-hub/ai-go-admin/internal/infra/config"
	"github.com/ai-go-hub/ai-go-admin/internal/infra/database"
	"github.com/ai-go-hub/ai-go-admin/plugins/kb/model"

	"gorm.io/gorm"
)

// backfillDBEnv 连库门控（同 kb 包先例；未设时 DB 用例一律 SKIP——不得当作通过）。
const backfillDBEnv = "PLUGIN_KB_DB"

// backfillChdirRepoRoot 切到仓库根（config/database 初始化依赖相对路径）。
// 用 t.Chdir（Go 1.24+，用例收尾自动还原 cwd）——同包其他用例有相对路径夹具（testdata/），
// 裸 os.Chdir 会让后续用例找不到文件。
func backfillChdirRepoRoot(t *testing.T) {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			t.Chdir(dir)
			return
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("未找到 go.mod（仓库根）")
		}
		dir = parent
	}
}

// TestReindexMatchTextDB 回填主链：DryRun 只读 → 实跑回填（派生文本 = 标题路径 + verbatim 正文，
// 与 chunk.RowTextOf 单点一致；生成 tsv 随列重算）→ 复跑幂等（零写入）→ 快照非法行响亮失败点名。
func TestReindexMatchTextDB(t *testing.T) {
	if os.Getenv(backfillDBEnv) != "1" {
		t.Skipf("需要数据库：设 %s=1 启用（库需已应用本插件的自管迁移）", backfillDBEnv)
	}
	backfillChdirRepoRoot(t)
	if err := config.Init(); err != nil {
		t.Fatalf("初始化配置: %v", err)
	}
	if err := database.Init(); err != nil {
		t.Fatalf("初始化数据库: %v", err)
	}
	requireKBTestDatabase(t, database.DB())
	applyPluginMigrations(t)
	db := database.DB()
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())

	var libID, docID uint
	if err := db.Raw("INSERT INTO kb_library (owner_kind, owner_id, name, status, created_at, updated_at)"+
		" VALUES ('system', 0, ?, 1, now(), now()) RETURNING id", "kb-mtbackfill-"+suffix).Scan(&libID).Error; err != nil {
		t.Fatalf("建库失败: %v", err)
	}
	if err := db.Raw("INSERT INTO kb_document (library_id, title, source_kind, source_ref, content_hash, mime, size, build_state, active_build, created_by_kind, created_by_id, created_at, updated_at)"+
		" VALUES (?, 'mt-backfill', 'text', 'fixture.md', ?, 'text/markdown', 1, 'ready', ?, 'admin', 0, now(), now()) RETURNING id",
		int64(libID), fmt.Sprintf("%040d", len(suffix)), fmt.Sprintf("%040s", "mtbackfill")).Scan(&docID).Error; err != nil {
		t.Fatalf("建文档失败: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Exec("DELETE FROM kb_chunk WHERE document_id = ?", docID).Error
		_ = db.Exec("DELETE FROM kb_document WHERE id = ?", docID).Error
		_ = db.Exec("DELETE FROM kb_library WHERE id = ?", libID).Error
	})

	insertChunk := func(seq int, text, headingJSON, snapshot, matchText string) uint {
		t.Helper()
		var id uint
		var heading, snap any
		if headingJSON != "" {
			heading = headingJSON
		}
		if snapshot != "" {
			snap = snapshot
		}
		if err := db.Raw("INSERT INTO kb_chunk (library_id, document_id, build_hash, chunker, chunker_version, seq, locator,"+
			" char_start, char_end, heading_path, text, text_hash, config_snapshot, match_text, created_at)"+
			" VALUES (?, ?, ?, 'heading', 'v1', ?, 'kb:doc:' || ? || '#p' || ?, 0, ?, ?::jsonb, ?, ?, ?::jsonb, ?, now()) RETURNING id",
			int64(libID), int64(docID), fmt.Sprintf("%040s", "mtbackfill"), seq, fmt.Sprintf("%d", docID), fmt.Sprintf("%d", seq),
			len([]rune(text)), heading, text, fmt.Sprintf("%040d", seq), snap, matchText).Scan(&id).Error; err != nil {
			t.Fatalf("建切片失败: %v", err)
		}
		return id
	}

	body := "总价为人民币壹万元整。"
	derived := "合同标题 > 第二条 价款\n" + body
	// ① 未回填行（迁移后存量行态）；② 已对齐行；③ 缺快照行（按代码缺省档复算——heading_prefix 缺省 true）
	idPending := insertChunk(1, body, `["合同标题","第二条 价款"]`, `{"heading_prefix":true}`, "")
	idAligned := insertChunk(2, body, `["合同标题","第二条 价款"]`, `{"heading_prefix":true}`, derived)
	idNoSnap := insertChunk(3, body, `["合同标题","第二条 价款"]`, "", "")

	// 库收窄回填（LibraryID 作用域）：读数只含本夹具库——同库并发跑其他包的连库用例时读数仍确定
	scope := MatchTextOptions{LibraryID: libID}

	readChunk := func(id uint) model.KBChunk {
		t.Helper()
		row, err := gorm.G[model.KBChunk](db).Where("id = ?", id).First(ctx)
		if err != nil {
			t.Fatalf("读切片 %d 失败: %v", id, err)
		}
		return row
	}

	// DryRun：只读（不写库）
	dry, err := ReindexMatchText(ctx, db, MatchTextOptions{LibraryID: libID, DryRun: true})
	if err != nil {
		t.Fatalf("DryRun 失败: %v", err)
	}
	if dry.Scanned != 3 || dry.Pending != 2 || dry.Updated != 0 || dry.Unchanged != 1 || dry.Failed != 0 {
		t.Fatalf("DryRun 读数不符: %+v", dry)
	}
	if got := readChunk(idPending).MatchText; got != "" {
		t.Fatalf("DryRun 不得写库（match_text=%q）", got)
	}

	// 实跑：回填两行——派生文本 = ChunkTextOf 口径（含标题路径前缀；缺快照行按缺省档同口径）
	got, err := ReindexMatchText(ctx, db, scope)
	if err != nil {
		t.Fatalf("回填失败: %v", err)
	}
	if got.Pending != 2 || got.Updated != 2 || got.Unchanged != 1 || got.SnapshotMissing != 1 || got.Failed != 0 {
		t.Fatalf("回填读数不符: %+v", got)
	}
	if m := readChunk(idPending).MatchText; m != derived {
		t.Fatalf("回填值应为 ChunkTextOf 派生文本：got=%q want=%q", m, derived)
	}
	if m := readChunk(idNoSnap).MatchText; m != derived {
		t.Fatalf("缺快照行应按代码缺省档复算（heading_prefix 缺省 true）：got=%q want=%q", m, derived)
	}
	if m := readChunk(idAligned).MatchText; m != derived {
		t.Fatalf("已对齐行不应变化：%q", m)
	}

	// 生成 tsv 随 match_text 自动重算（DB 生成列）：标题词经 FTS 词面可达
	var n int64
	if err := db.Raw("SELECT count(*) FROM kb_chunk WHERE id = ? AND tsv @@ websearch_to_tsquery('simple', ?)",
		idPending, "合同标题").Scan(&n).Error; err != nil {
		t.Fatalf("tsv 探询失败: %v", err)
	}
	if n != 1 {
		t.Fatalf("回填后生成 tsv 应命中标题词（n=%d）", n)
	}

	// 复跑：幂等（零写入）
	again, err := ReindexMatchText(ctx, db, scope)
	if err != nil {
		t.Fatalf("复跑失败: %v", err)
	}
	if again.Pending != 0 || again.Updated != 0 || again.Unchanged != 3 {
		t.Fatalf("复跑应幂等（零写入）: %+v", again)
	}

	// 快照非法行：响亮失败 + 点名（读数如实带回）
	insertChunk(4, "坏快照行", "", `{"target":"abc"}`, "")
	bad, err := ReindexMatchText(ctx, db, scope)
	if err == nil || bad.Failed != 1 {
		t.Fatalf("快照非法行应响亮失败: err=%v stats=%+v", err, bad)
	}
	if len(bad.Errors) == 0 {
		t.Fatalf("失败应点名样例: %+v", bad)
	}
}
