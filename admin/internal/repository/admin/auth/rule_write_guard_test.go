package auth

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-go-hub/ai-go-admin/internal/model"
	"github.com/ai-go-hub/ai-go-admin/internal/repository"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// 本文件为不依赖 DB 的写侧防护断言（DryRun 会话渲染真实 SQL）：
// 归属列（plugin）防护已从 handler 构造选项下沉到 AdminRuleRepository.Create/Update
// （rule_write_guard.go），断言口径 = 「生效集合（上游 handler 名单 ∪ 仓储固定名单）逐字段不变」
// + 「渲染 SQL 丢弃 plugin」+ 「安装器直写路径不被本守卫挡住」。

// ruleGuardCaptureLogger 捕获 GORM 生成的 SQL（DryRun 会话用，其余日志方法委托默认实现）
type ruleGuardCaptureLogger struct {
	logger.Interface
	sqls []string
}

// Trace 记录每次生成的 SQL
func (l *ruleGuardCaptureLogger) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	sql, _ := fc()
	l.sqls = append(l.sqls, sql)
}

// ruleGuardDryRunDB 构造不连库的 DryRun 会话（sql.Open 惰性，无 I/O）+ SQL 捕获器。
// 必须 SkipDefaultTransaction：GORM 默认给 Create/Update 套事务，BEGIN 会触碰真实连接；
// DryRun 下跳过默认事务（与 GORM 自带 ToSQL 同口径），语句 SQL 本身不受影响。
func ruleGuardDryRunDB(t *testing.T) (*gorm.DB, *ruleGuardCaptureLogger) {
	t.Helper()

	cap := &ruleGuardCaptureLogger{Interface: logger.Default}
	db, err := gorm.Open(postgres.New(postgres.Config{DSN: "host=127.0.0.1 dbname=scratch sslmode=disable"}), &gorm.Config{
		DryRun:                 true,
		SkipDefaultTransaction: true,
		DisableAutomaticPing:   true,
		Logger:                 cap,
	})
	if err != nil {
		t.Fatalf("构造 DryRun 会话失败: %v", err)
	}
	return db, cap
}

// ruleGuardRepoOn 把仓储包装类型绑定到指定（DryRun）连接
func ruleGuardRepoOn(db *gorm.DB) *AdminRuleRepository {
	repo := NewAdminRuleRepository()
	repo.Repository = repo.Repository.WithDB(db)
	return repo
}

// ruleUpstreamCreateOmit / ruleUpstreamUpdateOmit：上游 handler 的原始名单（handler 已回归上游）
var (
	ruleUpstreamCreateOmit = []string{}
	ruleUpstreamUpdateOmit = []string{"id", "children"}
)

// rulePreSinkCreateOmit / rulePreSinkUpdateOmit：下沉前 handler 的生效名单（等价性基准）
var (
	rulePreSinkCreateOmit = []string{"plugin"}
	rulePreSinkUpdateOmit = []string{"id", "children", "plugin"}
)

// TestRuleWriteGuardKeepsEffectiveOmitSet 生效集合逐字段不变：
// 上游 handler 名单 ∪ 仓储固定名单 == 下沉前 handler 名单（Create / Update 双向相等）。
func TestRuleWriteGuardKeepsEffectiveOmitSet(t *testing.T) {
	for _, action := range []struct {
		name     string
		upstream []string
		preSink  []string
	}{
		{"Create", ruleUpstreamCreateOmit, rulePreSinkCreateOmit},
		{"Update", ruleUpstreamUpdateOmit, rulePreSinkUpdateOmit},
	} {
		merged := append(append([]string{}, action.upstream...), adminRuleWriteGuardOmit...)

		for _, field := range action.preSink {
			if !slices.Contains(merged, field) {
				t.Fatalf("%s 生效 Omit 缺字段 %q（union=%v）", action.name, field, merged)
			}
		}
		for _, field := range merged {
			if !slices.Contains(action.preSink, field) {
				t.Fatalf("%s 生效 Omit 多出字段 %q（下沉前后不等价，union=%v）", action.name, field, merged)
			}
		}
		if slices.Contains(merged, "name") || slices.Contains(merged, "status") {
			t.Fatalf("%s 生效 Omit 不应包含规则内容列 name/status（规则页需正常编辑）: %v", action.name, merged)
		}
	}
}

// TestRuleCreateDropsPluginColumn 仓储 Create 渲染的 INSERT 不含归属列
//
// 归属列是系统维护列（只由安装器按列名代写：internal/plugin/cli/dbwrite.go 建行后回填），
// 请求驱动的写入面必须把它列进固定排除名单（rule_write_guard.go 的 adminRuleWriteGuardOmit）。
// 攻击向量按宿主是否带归属字段注入：字段在（宿主加了该字段）用反射设非零值——归属列为零值时 GORM
// 因列有 `default:”` 而不渲染该列，断言会失去敏感度；字段不在（宿主没加）本列无从写起，
// 断言退化为「INSERT 形状不含该列」+ 排除名单断言。两种宿主下本用例都可编译、都有效。
func TestRuleCreateDropsPluginColumn(t *testing.T) {
	db, cap := ruleGuardDryRunDB(t)
	repo := ruleGuardRepoOn(db)

	if !slices.Contains(ruleGuardWriteOptions(repository.Options{OmitFields: ruleUpstreamCreateOmit}).OmitFields, "plugin") {
		t.Fatal("Create 的生效排除名单必须含 plugin：归属只由安装器代写，请求驱动面不得写入")
	}

	status := uint8(1)
	// Weigh 给非零值：AdminRule.AfterCreate 在 Weigh 为空/0 时会以主键回填 weigh，
	// 而 DryRun 下尚无主键（ID=0）⇒ 该钩子渲染出无 WHERE 的全局 UPDATE 而被 GORM 拒绝。
	// 非零 Weigh 让钩子早退，本次渲染只剩 INSERT 本身（断言对象正是这条 INSERT）。
	weigh := 5
	rule := &model.AdminRule{
		Type:   "menu",
		Title:  "测试菜单",
		Name:   "test/menu",
		Status: &status,
		Weigh:  &weigh,
	}
	if field := reflect.ValueOf(rule).Elem().FieldByName("Plugin"); field.IsValid() && field.CanSet() {
		field.SetString("evil-plugin")
	}
	if err := repo.Create(context.Background(), rule, repository.Options{OmitFields: ruleUpstreamCreateOmit}); err != nil {
		t.Fatalf("渲染仓储 INSERT 失败: %v", err)
	}

	sql := ruleGuardLastSQL(t, cap)
	if !strings.Contains(sql, `INSERT INTO "admin_rules"`) || !strings.Contains(sql, `"name"`) {
		t.Fatalf("INSERT 形状不符: %s", sql)
	}
	if strings.Contains(sql, `"plugin"`) {
		t.Fatalf("归属列不应出现在入库 SQL（归属只由安装器代写）：\n%s", sql)
	}
}

// TestRuleUpdateDropsPluginColumn 仓储 Update 渲染的 SET 段不含归属列
// （规则页整行回提交带回 plugin 时不得改写归属，否则「按归属清理」会误删/误标宿主规则）
func TestRuleUpdateDropsPluginColumn(t *testing.T) {
	db, cap := ruleGuardDryRunDB(t)
	repo := ruleGuardRepoOn(db)

	err := repo.Update(context.Background(), map[string]any{
		"title":  "改名后的菜单",
		"status": uint8(1),
		"plugin": "evil-plugin",
	}, repository.Options{PrimaryKeyValue: "7", OmitFields: ruleUpstreamUpdateOmit})
	if err != nil {
		t.Fatalf("渲染仓储 UPDATE 失败: %v", err)
	}

	sql := ruleGuardLastSQL(t, cap)
	setClause := ruleGuardSetClause(t, sql)
	for _, kept := range []string{`"title"`, `"status"`} {
		if !strings.Contains(setClause, kept) {
			t.Fatalf("非受护列 %s 应正常更新：\n%s", kept, sql)
		}
	}
	if strings.Contains(setClause, `"plugin"`) {
		t.Fatalf("归属列不应出现在更新 SET 段：\n%s", sql)
	}
}

// TestRuleUpdateWithoutHandlerOmitStillGuarded 调用方不传任何 Omit 时防护依然生效
// （防护的承载点是仓储层本身，新增调用方无需记得列字段）
func TestRuleUpdateWithoutHandlerOmitStillGuarded(t *testing.T) {
	db, cap := ruleGuardDryRunDB(t)
	repo := ruleGuardRepoOn(db)

	err := repo.Update(context.Background(), map[string]any{
		"title":  "改名后的菜单",
		"plugin": "evil-plugin",
	}, repository.Options{PrimaryKeyValue: "7"})
	if err != nil {
		t.Fatalf("渲染仓储 UPDATE 失败: %v", err)
	}

	setClause := ruleGuardSetClause(t, ruleGuardLastSQL(t, cap))
	if !strings.Contains(setClause, `"title"`) {
		t.Fatalf("非受护列应正常更新：\n%s", setClause)
	}
	if strings.Contains(setClause, `"plugin"`) {
		t.Fatalf("归属列不应出现在更新 SET 段（即使调用方未传 Omit）：\n%s", setClause)
	}
}

// TestRuleWriteGuardLeavesRawGenericsWritePathOpen 守卫只管通用写入面：
// 安装器（internal/plugin/cli 的 dbwrite / lifecycle / promote_run）不经本仓储，
// 直接用 gorm.G[model.AdminRule] 写库——该路径照常可写归属列（否则安装/启用/卸载整体失效，
// 这是防护下沉后最容易踩的坑）。归属列按**列名直写**（建行后回填），与安装器同口径。
func TestRuleWriteGuardLeavesRawGenericsWritePathOpen(t *testing.T) {
	db, cap := ruleGuardDryRunDB(t)

	status := uint8(0)
	weigh := 1 // 非零值让 AfterCreate 的 weigh 回填（需真实主键）早退，见上条测试的说明
	row := model.AdminRule{
		Type:   "menu",
		Title:  "插件实体页",
		Name:   "plugin/demo/items",
		Status: &status,
		Weigh:  &weigh,
	}
	if err := gorm.G[model.AdminRule](db).Create(context.Background(), &row); err != nil {
		t.Fatalf("渲染安装器直写 INSERT 失败: %v", err)
	}
	if _, err := gorm.G[model.AdminRule](db).Where("id = ?", row.ID).
		Update(context.Background(), "plugin", "demo"); err != nil {
		t.Fatalf("渲染安装器归属回填 UPDATE 失败: %v", err)
	}

	sql := ruleGuardLastSQL(t, cap)
	if !strings.Contains(sql, `"plugin"`) {
		t.Fatalf("安装器直写路径必须能写归属列（守卫不得拦到它）：\n%s", sql)
	}
}

// TestRuleWriteGuardDoesNotMutateCallerOptions 叠加防护不改变调用方传入的切片底层数组
func TestRuleWriteGuardDoesNotMutateCallerOptions(t *testing.T) {
	backing := make([]string, 1, 16)
	backing[0] = "id"

	opts := ruleGuardWriteOptions(repository.Options{OmitFields: backing})
	if len(opts.OmitFields) != 1+len(adminRuleWriteGuardOmit) {
		t.Fatalf("合并后字段数 = %d，期望 %d", len(opts.OmitFields), 1+len(adminRuleWriteGuardOmit))
	}
	if len(backing) != 1 || backing[0] != "id" {
		t.Fatalf("调用方切片被改写: %v", backing)
	}
}

// ruleGuardSetClause 截取 UPDATE 语句的 SET 段（WHERE 段不参与写侧断言）
func ruleGuardSetClause(t *testing.T, sql string) string {
	t.Helper()

	idx := strings.Index(sql, " WHERE ")
	if idx < 0 {
		t.Fatalf("UPDATE 语句缺 WHERE 段: %s", sql)
	}
	return sql[:idx]
}

// ruleGuardLastSQL 取 DryRun 会话捕获的最后一条 SQL
func ruleGuardLastSQL(t *testing.T, cap *ruleGuardCaptureLogger) string {
	t.Helper()

	if len(cap.sqls) == 0 {
		t.Fatal("未捕获到 SQL")
	}
	return cap.sqls[len(cap.sqls)-1]
}
