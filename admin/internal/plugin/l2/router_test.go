package l2

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ai-go-hub/ai-go-admin/internal/plugin/host"
	"github.com/ai-go-hub/ai-go-admin/internal/plugin/manifest"

	"github.com/gin-gonic/gin"
)

// router_test.go：六动作契约的路由形状（不连库：快照注入即可断言路由表，闸的行为在 host 包用例）。
// 覆盖面：update 带 :pk、/get/:pk、/sort 仅 drag_sort、相对段挂在宿主前缀之下、非法 spec 启动即失败。

// routeSet 返回引擎上的路由集合（"METHOD path" 形态）
func routeSet(engine *gin.Engine) map[string]struct{} {
	routes := map[string]struct{}{}
	for _, route := range engine.Routes() {
		routes[route.Method+" "+route.Path] = struct{}{}
	}
	return routes
}

// TestMountRegistersSixActions 六动作契约：list/create/delete + get/:pk + update/:pk（+ drag_sort 的 sort）
func TestMountRegistersSixActions(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := New(nil)

	snapshot := &host.Snapshot{
		Plugins: []host.Plugin{{Name: "demo", Status: host.StatusEnabled}},
		Entities: []host.Entity{
			entityRow(t, "demo", "items", manifest.Entity{
				Entity:   "items",
				TitleZh:  "条目",
				TitleEn:  "Items",
				DragSort: true,
				Fields:   []manifest.Field{textField("title", "标题")},
			}),
			entityRow(t, "demo", "notes", manifest.Entity{
				Entity:  "notes",
				TitleZh: "笔记",
				TitleEn: "Notes",
				Fields:  []manifest.Field{textField("title", "标题")},
			}),
		},
	}
	SetSnapshotSource(func() (*host.Snapshot, bool) { return snapshot, true })
	t.Cleanup(func() { SetSnapshotSource(nil) })

	ginEngine := gin.New()
	engine.Mount(ginEngine.Group("/admin"))

	routes := routeSet(ginEngine)
	wants := []string{
		"POST /admin/plugin/demo/items/list",
		"POST /admin/plugin/demo/items/create",
		"POST /admin/plugin/demo/items/delete",
		"POST /admin/plugin/demo/items/sort",
		"GET /admin/plugin/demo/items/get/:pk",
		"POST /admin/plugin/demo/items/update/:pk",
	}
	for _, want := range wants {
		if _, ok := routes[want]; !ok {
			t.Fatalf("缺路由 %s（实际 %v）", want, routes)
		}
	}

	// update/get 必须带 :pk（前端 TableManagerAPI 拼 ${url}/${pk}，F9）
	if _, ok := routes["POST /admin/plugin/demo/items/update"]; ok {
		t.Fatal("update 必须带 :pk（不带 pk 的形态不得注册）")
	}

	// 未开 drag_sort 的实体不注册 /sort
	if _, ok := routes["POST /admin/plugin/demo/notes/sort"]; ok {
		t.Fatal("未声明 drag_sort 的实体不得注册 /sort")
	}
	if _, ok := routes["POST /admin/plugin/demo/notes/list"]; !ok {
		t.Fatal("实体 notes 的 list 路由应存在")
	}

	// 相对段之外的路径不存在（前缀由宿主提供，插件不写绝对前缀）
	for route := range routes {
		if strings.HasPrefix(route, "POST /plugin/") {
			t.Fatalf("路由不得落在宿主前缀之外：%s", route)
		}
	}
}

// TestMountPanicsOnInvalidSpec 非法 spec 启动即失败（不静默零路由）
func TestMountPanicsOnInvalidSpec(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := New(nil)

	snapshot := &host.Snapshot{
		Plugins: []host.Plugin{{Name: "demo", Status: host.StatusEnabled}},
		Entities: []host.Entity{
			entityRow(t, "demo", "items", manifest.Entity{
				Entity:  "items",
				TitleZh: "条目",
				TitleEn: "Items",
				// deleted_at 是引擎保留列名：装载期应拒
				Fields: []manifest.Field{textField("deleted_at", "软删")},
			}),
		},
	}
	SetSnapshotSource(func() (*host.Snapshot, bool) { return snapshot, true })
	t.Cleanup(func() { SetSnapshotSource(nil) })

	defer func() {
		recovered := recover()
		if recovered == nil {
			t.Fatal("非法 spec 必须 panic 使启动失败（不静默零路由）")
		}
		message := strings.TrimSpace(fmt.Sprint(recovered))
		if !strings.Contains(message, "demo") || !strings.Contains(message, "items") {
			t.Fatalf("panic 信息应点名插件与实体，实际 %v", recovered)
		}
	}()
	engine.Mount(gin.New().Group("/admin"))
}

// TestMountWithoutSnapshotIsNoop 快照未装载（host.Preflight 未执行）时不注册任何路由，只记日志
func TestMountWithoutSnapshotIsNoop(t *testing.T) {
	gin.SetMode(gin.TestMode)
	SetSnapshotSource(func() (*host.Snapshot, bool) { return nil, false })
	t.Cleanup(func() { SetSnapshotSource(nil) })

	ginEngine := gin.New()
	New(nil).Mount(ginEngine.Group("/admin"))
	if len(ginEngine.Routes()) != 0 {
		t.Fatalf("快照未装载时不得注册路由，实际 %v", ginEngine.Routes())
	}
}

// TestGateRejectsWhenDatabaseMissing 路由上的闸生效（fail-closed）：插件不在台账 / 未启用 ⇒ 404
// （闸的库级行为断言在 host 包的 DB 门禁用例；此处只钉「L2 路由确实挂了闸」——
// 插件名取不可能存在于台账的 gateprobe，故无论本机库是否已初始化都必然被闸拦下）
func TestGateRejectsWhenDatabaseMissing(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := New(nil)

	snapshot := &host.Snapshot{
		Plugins: []host.Plugin{{Name: "gateprobe", Status: host.StatusEnabled}},
		Entities: []host.Entity{
			entityRow(t, "gateprobe", "items", manifest.Entity{
				Entity:  "items",
				TitleZh: "条目",
				TitleEn: "Items",
				Fields:  []manifest.Field{textField("title", "标题")},
			}),
		},
	}
	SetSnapshotSource(func() (*host.Snapshot, bool) { return snapshot, true })
	t.Cleanup(func() { SetSnapshotSource(nil) })

	ginEngine := gin.New()
	engine.Mount(ginEngine.Group("/admin"))

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/admin/plugin/gateprobe/items/list", strings.NewReader(`{}`))
	ginEngine.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("台账无此插件/未启用时闸应 fail-closed（404），实际 %d", recorder.Code)
	}
}
