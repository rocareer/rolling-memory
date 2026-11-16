// plugin: kb v1.0.0
package kb

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-go-hub/ai-go-admin/plugins/kb/model"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// ==================== ② 过滤下推证据（SQL 捕获） ====================
// TestACLFilterPushedToSQLDB 过滤必须进 SQL（禁应用层后过滤）的可证伪面：
// 捕获真实执行 SQL，断言授权反查 / owner 过滤 / EXISTS 前缀 / 显式列 / LIMIT 截断均在 SQL 中。
func TestACLFilterPushedToSQLDB(t *testing.T) {
	f := newKBFixture(t)
	ctx := f.ctx
	capture := &sqlCaptureLogger{}
	svc := newServiceWithDB(f.db.Session(&gorm.Session{Logger: capture}))

	g := f.group("kb-acl-sql-"+f.suffix, 1)
	uA := f.user("kb-acl-sql-a-"+f.suffix, "enable", g.ID)
	uB := f.user("kb-acl-sql-b-"+f.suffix, "enable", g.ID)
	libA := f.library(model.KBLibraryOwnerKindUser, uA.ID, "kb-acl-sqllib-a-"+f.suffix, 1)
	libB := f.library(model.KBLibraryOwnerKindUser, uB.ID, "kb-acl-sqllib-b-"+f.suffix, 1)
	libC := f.library(model.KBLibraryOwnerKindUser, uB.ID, "kb-acl-sqllib-c-"+f.suffix, 1)
	f.grant(libC.ID, model.KBGrantSubjectUser, uA.ID, model.KBGrantPermRead) // 反查集非空 → 主查询带 id IN (...)
	f.document(libA.ID, "kb-acl-sqldoc-"+f.suffix)

	viewA, err := svc.UserView(ctx, uA.ID)
	if err != nil {
		t.Fatalf("视图解析失败: %v", err)
	}
	if _, total, err := svc.ListLibraries(ctx, viewA, LibraryListOptions{Limit: 5000}); err != nil || total != 2 {
		t.Fatalf("列表失败：total=%d err=%v", total, err) // 自建库 + 受授库
	}
	if _, err := svc.GetLibrary(ctx, viewA, libB.ID); !errors.Is(err, ErrDenied) {
		t.Fatalf("跨用户应拒，得到: %v", err)
	}
	if _, _, err := svc.ListDocuments(ctx, viewA, libA.ID, DocumentListOptions{Limit: 5000}); err != nil {
		t.Fatalf("文档列表失败: %v", err)
	}

	sqls := capture.all()
	joined := strings.Join(sqls, "\n")
	t.Logf("捕获 SQL（%d 条）:\n%s", len(sqls), joined)

	// ① 步骤②反查在 SQL：DISTINCT library_id FROM kb_grant（授权过滤不在应用层）
	if !sqlContains(sqls, "SELECT DISTINCT g.library_id FROM") {
		t.Fatalf("未见授权反查 SQL（两步反查步骤②）")
	}
	// ② 主查询过滤含 owner 与反查集（owner 集 ∪ ② 的 id IN (...)，同一构造器）
	if !sqlContains(sqls, "kb_library.owner_kind = ") || !sqlContains(sqls, "kb_library.owner_id = ") {
		t.Fatalf("主查询未含 owner 过滤")
	}
	if !sqlContains(sqls, "kb_library.id IN (") {
		t.Fatalf("主查询未含反查集 IN 过滤")
	}
	// ③ 单资源 EXISTS（命中 uk_kb_grant_library_subject_perm 前缀：library_id + perm + subject）
	if !sqlContains(sqls, "EXISTS (SELECT 1 FROM") || !sqlContains(sqls, "g.library_id = kb_library.id") {
		t.Fatalf("单资源校验未走 EXISTS 前缀路径")
	}
	if !sqlContains(sqls, "g.perm = ") || !sqlContains(sqls, "g.subject_kind = ") {
		t.Fatalf("EXISTS 未含 perm/subject 前缀条件")
	}
	// ④ 显式列（禁 SELECT *）；大字段不进列表
	if sqlContains(sqls, "SELECT *") {
		t.Fatalf("出现 SELECT *（禁）")
	}
	listSQL := ""
	for _, s := range sqls {
		if strings.Contains(s, "FROM \"kb_library\"") && strings.Contains(s, "LIMIT 100") {
			listSQL = s
		}
	}
	if listSQL == "" || !strings.HasPrefix(listSQL, "SELECT id, owner_kind") || strings.Contains(listSQL, "chunk_config") {
		t.Fatalf("库列表列面不合规（应为显式列、不含大字段）：%s", listSQL)
	}
	if strings.Contains(listSQL, "id DESC,id DESC") {
		t.Fatalf("排序出现重复子句：%s", listSQL)
	}
	// ⑤ LIMIT 截断（浏览面 ≤100：请求 5000 应见 LIMIT 100）
	if !sqlContains(sqls, "LIMIT 100") {
		t.Fatalf("未见浏览面上限截断（LIMIT 100）")
	}
	// ⑥ Count 与 List 同一构造器（Count 亦带 owner 过滤，不是全表计数）
	if !sqlContains(sqls, `COUNT("id") FROM "kb_library" WHERE (kb_library.deleted_at IS NULL`) {
		t.Fatalf("未见带隔离谓词的计数 SQL")
	}
}

// sqlCaptureLogger 捕获 GORM 实际执行 SQL 的测试 logger（无第三方依赖的最小实现）
type sqlCaptureLogger struct {
	mu   sync.Mutex
	sqls []string
}

func (l *sqlCaptureLogger) LogMode(logger.LogLevel) logger.Interface { return l }
func (l *sqlCaptureLogger) Info(context.Context, string, ...any)     {}
func (l *sqlCaptureLogger) Warn(context.Context, string, ...any)     {}
func (l *sqlCaptureLogger) Error(context.Context, string, ...any)    {}
func (l *sqlCaptureLogger) Trace(_ context.Context, _ time.Time, fc func() (string, int64), _ error) {
	sql, _ := fc()
	l.mu.Lock()
	l.sqls = append(l.sqls, sql)
	l.mu.Unlock()
}

func (l *sqlCaptureLogger) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.sqls...)
}

// sqlContains 任一捕获 SQL 含子串
func sqlContains(sqls []string, substr string) bool {
	for _, s := range sqls {
		if strings.Contains(s, substr) {
			return true
		}
	}
	return false
}
