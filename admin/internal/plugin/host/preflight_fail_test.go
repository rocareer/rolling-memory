package host

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"gorm.io/gorm"
)

// Preflight 失败态用例（/：表缺失、归属列缺失与 spec 非法各自给出可读错误，且都不静默降级）。
//
// 每个失败分支除「错误可读」外还断言**不遗留半态**：预检只在校验全过后才替换内存快照
// （preflight.go 的 snapshot 赋值在函数末尾），失败时 Loaded() 必须仍是失败前的那一份
// （或仍是「未装载」）——绝不出现「部分实体已装载」的新快照。
//
// 门控：PLUGIN_DB=1 时连真实库跑（无该变量自动跳过，默认 `go test` 不连库）。
//
//	PLUGIN_DB=1 go test -count=1 -timeout=60s ./internal/plugin/host/
func TestPreflightDBFailureStates(t *testing.T) {
	t.Run("表缺失（迁移未跑）", func(t *testing.T) {
		pluginDBSetup(t) // 前置校验保证表在，随后注入「表不存在」判定覆盖失败分支

		original := tableExistsFn
		tableExistsFn = func(context.Context, *gorm.DB, string) (bool, error) { return false, nil }
		t.Cleanup(func() { tableExistsFn = original })

		before, had := Loaded()
		err := Preflight(context.Background())
		if err == nil {
			t.Fatal("表缺失应让预检失败（fail-fast，不静默零路由）")
		}
		msg := err.Error()
		if !strings.Contains(msg, physicalName("admin_plugins")) {
			t.Errorf("错误应点名缺失的物理表名: %v", err)
		}
		if !strings.Contains(msg, "不存在") || !strings.Contains(msg, "aigo migrate up") {
			t.Errorf("错误应给出可执行提示（`aigo migrate up`）: %v", err)
		}
		assertNoHalfLoadedSnapshot(t, before, had, "表缺失")
	})

	t.Run("表存在性检查报错", func(t *testing.T) {
		pluginDBSetup(t)

		original := tableExistsFn
		tableExistsFn = func(context.Context, *gorm.DB, string) (bool, error) {
			return false, context.Canceled
		}
		t.Cleanup(func() { tableExistsFn = original })

		before, had := Loaded()
		err := Preflight(context.Background())
		if err == nil || !strings.Contains(err.Error(), "检查数据表") {
			t.Fatalf("检查失败应包装成可读错误: %v", err)
		}
		assertNoHalfLoadedSnapshot(t, before, had, "表存在性检查报错")
	})

	t.Run("spec 非法（点名插件与实体）", func(t *testing.T) {
		db := pluginDBSetup(t)

		plugin := newPluginDBPlugin(t, db, StatusInstalled)
		// 字段名不是闭集标识符 ⇒ 预检必须失败并点名「插件 + 实体」
		entity := newPluginDBEntity(t, db, plugin.Name, "items",
			`{"entity":"items","fields":[{"name":"Bad-Name","type":"string"}]}`)

		before, had := Loaded()
		err := Preflight(context.Background())
		if err == nil {
			t.Fatal("非法 spec 应让预检失败")
		}
		msg := err.Error()
		if !strings.Contains(msg, plugin.Name) || !strings.Contains(msg, entity.Name) {
			t.Errorf("错误应点名插件与实体: %v", err)
		}
		if !strings.Contains(msg, "spec 非法") {
			t.Errorf("错误应说明是 spec 非法: %v", err)
		}
		// 半态的具体形态是「部分实体进了快照」：点名该实体不得出现在任何新快照里
		if after, ok := Loaded(); ok {
			for i := range after.Entities {
				if after.Entities[i].Plugin == plugin.Name {
					t.Fatalf("预检失败不得留下半装实体（快照里有 %s/%s）: %+v",
						after.Entities[i].Plugin, after.Entities[i].Name, after.Entities)
				}
			}
		}
		assertNoHalfLoadedSnapshot(t, before, had, "spec 非法")
	})
}

// assertNoHalfLoadedSnapshot 预检失败不得遗留半态：内存快照要么仍是失败前的那一份（同一指针），
// 要么仍是「未装载」；出现任何新快照都说明替换发生在校验全过之前。
func assertNoHalfLoadedSnapshot(t *testing.T, before *Snapshot, had bool, branch string) {
	t.Helper()
	after, ok := Loaded()
	if ok != had || after != before {
		t.Fatalf("%s：预检失败后不得替换/遗留半态快照（失败前 had=%v snapshot=%p；失败后 ok=%v snapshot=%p）",
			branch, had, before, ok, after)
	}
}

// TestPreflightOwnershipColumnMissing 归属列缺失（框架迁移未跑完 / 目标库结构被裁剪）⇒ fail-fast。
// 归属列是启停与卸载「按插件整片收口」的唯一依据：缺列时建行会失败、清理会空转，必须在装配前拦下。
func TestPreflightOwnershipColumnMissing(t *testing.T) {
	pluginDBSetup(t)

	original := columnExistsFn
	columnExistsFn = func(context.Context, *gorm.DB, string, string) (bool, error) { return false, nil }
	t.Cleanup(func() { columnExistsFn = original })

	before, had := Loaded()
	err := Preflight(context.Background())
	if err == nil {
		t.Fatal("归属列缺失应让预检失败（不静默丢归属）")
	}
	msg := err.Error()
	if !strings.Contains(msg, physicalName("admin_rules")+".plugin") {
		t.Errorf("错误应点名缺失的归属列: %v", err)
	}
	if !strings.Contains(msg, "aigo migrate up") {
		t.Errorf("错误应给出可执行提示（`aigo migrate up`）: %v", err)
	}
	assertNoHalfLoadedSnapshot(t, before, had, "归属列缺失")
}

// TestEnsureLoadedIdempotent 幂等装载：已装载后不得再跑一次预检（每次装配路由都多打一次库）。
// 断言方式 = 把查表判定换成「必失败」，已装载时该判定不该被调用到。
func TestEnsureLoadedIdempotent(t *testing.T) {
	pluginDBSetup(t)

	if err := Preflight(context.Background()); err != nil {
		t.Fatalf("前置装载应成功: %v", err)
	}

	original := tableExistsFn
	tableExistsFn = func(context.Context, *gorm.DB, string) (bool, error) {
		return false, errors.New("已装载后不应再查表")
	}
	t.Cleanup(func() { tableExistsFn = original })

	if err := EnsureLoaded(context.Background()); err != nil {
		t.Fatalf("已装载时 EnsureLoaded 必须直接返回: %v", err)
	}
}

// TestMustLoadPanicsOnLoadFailure 装载失败即启动中止（panic），不得静默继续：
// router.Setup 没有错误返回通道，「静默零路由」是否决项。
// 数据库未初始化时只记日志、不 panic 的分支由 internal/plugins/wiring_test.go 的零库装配覆盖。
func TestMustLoadPanicsOnLoadFailure(t *testing.T) {
	pluginDBSetup(t)

	// 清空快照：确保 EnsureLoaded 真去装载（同进程里前面的用例可能已装载过）
	snapshotMu.Lock()
	saved := snapshot
	snapshot = nil
	snapshotMu.Unlock()
	t.Cleanup(func() {
		snapshotMu.Lock()
		snapshot = saved
		snapshotMu.Unlock()
	})

	original := tableExistsFn
	tableExistsFn = func(context.Context, *gorm.DB, string) (bool, error) { return false, nil }
	t.Cleanup(func() { tableExistsFn = original })

	defer func() {
		recovered := recover()
		if recovered == nil {
			t.Fatal("装载失败必须 panic（启动中止），不得静默继续")
		}
		if !strings.Contains(fmt.Sprint(recovered), "启动中止") {
			t.Fatalf("panic 文案应说明启动中止: %v", recovered)
		}
	}()
	MustLoad(context.Background())
}
