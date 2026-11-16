package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ai-go-hub/ai-go-admin/internal/dto"
	"github.com/ai-go-hub/ai-go-admin/internal/middleware"
	"github.com/ai-go-hub/ai-go-admin/internal/model"
	pluginCli "github.com/ai-go-hub/ai-go-admin/internal/plugin/cli"
	pluginSource "github.com/ai-go-hub/ai-go-admin/internal/plugin/source"

	"github.com/gin-gonic/gin"
)

type fakeChecker struct {
	super bool
	err   error
}

func (f fakeChecker) IsSuperAdmin(context.Context, uint) (bool, error) {
	return f.super, f.err
}

type handlerStore struct {
	sources   map[uint]pluginSource.Source
	nextID    uint
	entries   []pluginSource.CatalogEntry
	installed map[string]pluginSource.InstalledPlugin

	// 清单列表的入参透传断言（TestCatalogListQueryPropagation / TestCatalogListVersionNotInContract）：
	// 记录 ListEntries 被调用的次数与最后一次查询体。零值 = 未被调用。
	entriesCalls     int
	lastEntriesQuery pluginSource.ListEntriesQuery
}

func newHandlerStore() *handlerStore {
	return &handlerStore{sources: map[uint]pluginSource.Source{}, nextID: 1}
}

func (s *handlerStore) ListSources(_ context.Context, q pluginSource.ListSourcesQuery) ([]pluginSource.Source, int64, error) {
	list := make([]pluginSource.Source, 0, len(s.sources))
	for _, row := range s.sources {
		list = append(list, row)
	}
	return list, int64(len(list)), nil
}
func (s *handlerStore) GetSource(_ context.Context, id uint) (*pluginSource.Source, error) {
	row, ok := s.sources[id]
	if !ok {
		return nil, pluginSource.ErrNotFound
	}
	return &row, nil
}
func (s *handlerStore) GetSourceByName(_ context.Context, name string) (*pluginSource.Source, error) {
	for _, row := range s.sources {
		if row.Name == name {
			copy := row
			return &copy, nil
		}
	}
	return nil, pluginSource.ErrNotFound
}
func (s *handlerStore) CreateSource(_ context.Context, src *pluginSource.Source) error {
	for _, row := range s.sources {
		if row.Name == src.Name {
			return pluginSource.ErrNameConflict
		}
	}
	src.ID = s.nextID
	s.nextID++
	s.sources[src.ID] = *src
	return nil
}
func (s *handlerStore) UpdateSource(_ context.Context, id uint, patch pluginSource.SourcePatch) (bool, error) {
	row, ok := s.sources[id]
	if !ok {
		return false, pluginSource.ErrNotFound
	}
	if patch.Title != nil {
		row.Title = *patch.Title
	}
	if patch.Type != nil {
		row.Type = *patch.Type
	}
	if patch.Location != nil {
		row.Location = *patch.Location
	}
	if patch.Enabled != nil {
		row.Enabled = *patch.Enabled
	}
	if patch.Weigh != nil {
		row.Weigh = *patch.Weigh
	}
	if patch.Remark != nil {
		row.Remark = *patch.Remark
	}
	if patch.AllowInsecureHTTP != nil {
		row.AllowInsecureHTTP = *patch.AllowInsecureHTTP
	}
	if patch.AllowPrivateHosts != nil {
		row.AllowPrivateHosts = *patch.AllowPrivateHosts
	}
	if patch.AllowAbsolutePaths != nil {
		row.AllowAbsolutePaths = *patch.AllowAbsolutePaths
	}
	if patch.ClearSnapshot {
		row.LastSyncStatus = ""
		row.LastSyncError = ""
		row.LastCatalogSHA256 = ""
	}
	s.sources[id] = row
	return patch.ClearSnapshot, nil
}
func (s *handlerStore) DeleteSources(_ context.Context, ids []uint) (int64, error) {
	var deleted int64
	for _, id := range ids {
		if _, ok := s.sources[id]; ok {
			delete(s.sources, id)
			deleted++
		}
	}
	return deleted, nil
}
func (s *handlerStore) ReplaceCatalog(context.Context, *pluginSource.Source, *pluginSource.Catalog, string, time.Time) error {
	return nil
}
func (s *handlerStore) MarkSyncFailure(context.Context, uint, string) error { return nil }
func (s *handlerStore) ListEntries(_ context.Context, q pluginSource.ListEntriesQuery) ([]pluginSource.CatalogEntry, int64, error) {
	s.entriesCalls++
	s.lastEntriesQuery = q
	return s.entries, int64(len(s.entries)), nil
}
func (s *handlerStore) ListEntryPeers(context.Context, []pluginSource.UIDVersion) ([]pluginSource.CatalogEntry, error) {
	return s.entries, nil
}
func (s *handlerStore) ListSourcesSummary(context.Context) ([]pluginSource.SourceSummary, error) {
	out := make([]pluginSource.SourceSummary, 0, len(s.sources))
	for _, row := range s.sources {
		out = append(out, pluginSource.SourceSummary{Source: row})
	}
	return out, nil
}
func (s *handlerStore) InstalledVersions(context.Context, []string) (map[string]pluginSource.InstalledPlugin, error) {
	if s.installed == nil {
		return map[string]pluginSource.InstalledPlugin{}, nil
	}
	return s.installed, nil
}

func testHandler(store pluginSource.Store, checker SuperAdminChecker, admin *dto.AdminSession) *adminSourceHandler {
	return &adminSourceHandler{
		store:   store,
		checker: checker,
		newEnv: func() (*pluginCli.Env, error) {
			return &pluginCli.Env{Root: os.TempDir()}, nil
		},
		// 环境判定默认给 dev（源码检出内运行的口径）；「生产环境禁装」用例自行替换为 prod。
		env: func() pluginCli.Environment {
			return pluginCli.Environment{Name: pluginCli.EnvDev, Source: pluginCli.EnvSourceAuto, LifecycleAllowed: true}
		},
	}
}

// prodEnvironment 生产环境判定（与 cli.classifyEnvironment("prod", true) 同形；判定规则本身的
// 逐格用例在 internal/plugin/cli/environment_test.go，这里只验证 handler 是否照它行事）。
func prodEnvironment() pluginCli.Environment {
	return pluginCli.Environment{Name: pluginCli.EnvProd, Source: pluginCli.EnvSourceConfig, Configured: "prod"}
}

type apiResponse struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func invoke(t *testing.T, handler gin.HandlerFunc, method, path, body string, admin *dto.AdminSession) apiResponse {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	route := "/x"
	if strings.HasPrefix(path, "/x/") {
		route = "/x/:pk"
	}
	router.Handle(method, route, func(c *gin.Context) {
		if admin != nil {
			c.Set(middleware.CtxAdminKey, admin)
		}
		handler(c)
	})
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("传输层必须是 200（错误码在 JSON code）：got %d", recorder.Code)
	}
	var response apiResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("解析响应失败: %v（%s）", err, recorder.Body.String())
	}
	return response
}

func adminSession() *dto.AdminSession {
	return &dto.AdminSession{Admin: &model.Admin{ID: 1, Username: "alice"}}
}

func TestSourceHandlerAuth(t *testing.T) {
	store := newHandlerStore()

	response := invoke(t, testHandler(store, fakeChecker{super: true}, nil).sourceList, http.MethodPost, "/x", "{}", nil)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("未登录应 401: %+v", response)
	}

	response = invoke(t, testHandler(store, fakeChecker{super: false}, adminSession()).sourceList, http.MethodPost, "/x", "{}", adminSession())
	if response.Code != http.StatusForbidden {
		t.Fatalf("非超管应 403: %+v", response)
	}

	response = invoke(t, testHandler(store, fakeChecker{err: errors.New("db down")}, adminSession()).sourceList, http.MethodPost, "/x", "{}", adminSession())
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("DB 故障应 503: %+v", response)
	}

	response = invoke(t, testHandler(store, fakeChecker{super: true}, adminSession()).sourceList, http.MethodPost, "/x", "{}", adminSession())
	if response.Code != 0 {
		t.Fatalf("超管应成功: %+v", response)
	}
}

func TestSourceHandlerCRUD(t *testing.T) {
	store := newHandlerStore()
	handler := testHandler(store, fakeChecker{super: true}, adminSession())
	admin := adminSession()

	response := invoke(t, handler.sourceCreate, http.MethodPost, "/x",
		`{"name":"local","title":"Local","type":"local","location":"catalog/catalog.yaml","enabled":1}`, admin)
	if response.Code != 0 {
		t.Fatalf("新增来源失败: %+v", response)
	}

	response = invoke(t, handler.sourceCreate, http.MethodPost, "/x",
		`{"name":"local","title":"Local","type":"local","location":"catalog/catalog.yaml"}`, admin)
	if response.Code != http.StatusConflict {
		t.Fatalf("重复名称应 409: %+v", response)
	}

	response = invoke(t, handler.sourceUpdate, http.MethodPost, "/x/1",
		`{"name":"renamed","title":"New"}`, admin)
	if response.Code != http.StatusConflict {
		t.Fatalf("改名应 409: %+v", response)
	}

	response = invoke(t, handler.sourceUpdate, http.MethodPost, "/x/1",
		`{"title":"New","type":"http","location":"https://example.com/catalog.yaml"}`, admin)
	if response.Code != 0 {
		t.Fatalf("更新来源失败: %+v", response)
	}
	var updateData struct {
		SnapshotCleared bool `json:"snapshot_cleared"`
	}
	if err := json.Unmarshal(response.Data, &updateData); err != nil || !updateData.SnapshotCleared {
		t.Fatalf("type/location 变化应清空快照: %+v %v", updateData, err)
	}

	response = invoke(t, handler.sourceGet, http.MethodGet, "/x/99", "", admin)
	if response.Code != http.StatusNotFound {
		t.Fatalf("不存在应 404: %+v", response)
	}

	response = invoke(t, handler.sourceList, http.MethodPost, "/x", `{"page":1,"limit":10}`, admin)
	if response.Code != 0 {
		t.Fatalf("列表失败: %+v", response)
	}
	var listData struct {
		Total int64 `json:"total"`
	}
	if err := json.Unmarshal(response.Data, &listData); err != nil || listData.Total != 1 {
		t.Fatalf("列表 total 不符: %+v %v", listData, err)
	}

	response = invoke(t, handler.sourceDelete, http.MethodPost, "/x", `{"pks":["1"]}`, admin)
	if response.Code != 0 {
		t.Fatalf("删除失败: %+v", response)
	}
	var deleteData struct {
		Deleted int64 `json:"deleted"`
	}
	if err := json.Unmarshal(response.Data, &deleteData); err != nil || deleteData.Deleted != 1 {
		t.Fatalf("删除数量不符: %+v %v", deleteData, err)
	}
}

func TestSourceHandlerCreateValidation(t *testing.T) {
	handler := testHandler(newHandlerStore(), fakeChecker{super: true}, adminSession())
	response := invoke(t, handler.sourceCreate, http.MethodPost, "/x",
		`{"name":"Bad Name","location":"x"}`, adminSession())
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("非法 name 应 422: %+v", response)
	}
}

func TestSourceTestEndpoint(t *testing.T) {
	root := t.TempDir()
	catalogPath := root + "/catalog.yaml"
	if err := os.WriteFile(catalogPath, []byte("schema: ai-go-plugin-catalog/v1\nplugins: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	handler := testHandler(newHandlerStore(), fakeChecker{super: true}, adminSession())
	handler.newEnv = func() (*pluginCli.Env, error) { return &pluginCli.Env{Root: root}, nil }

	response := invoke(t, handler.sourceTest, http.MethodPost, "/x", `{"type":"local","location":"catalog.yaml"}`, adminSession())
	if response.Code != 0 {
		t.Fatalf("来源测试应成功: %+v", response)
	}
	var data struct {
		OK      bool   `json:"ok"`
		Entries int    `json:"entries"`
		Schema  string `json:"schema"`
	}
	if err := json.Unmarshal(response.Data, &data); err != nil || !data.OK || data.Schema != "ai-go-plugin-catalog/v1" {
		t.Fatalf("来源测试回执不符: %+v %v", data, err)
	}

	response = invoke(t, handler.sourceTest, http.MethodPost, "/x", `{"type":"local","location":"missing.yaml"}`, adminSession())
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("缺失清单应 422: %+v", response)
	}
}

func TestCatalogListFlags(t *testing.T) {
	store := newHandlerStore()
	store.sources[1] = pluginSource.Source{ID: 1, Name: "a", Enabled: 1}
	store.sources[2] = pluginSource.Source{ID: 2, Name: "b", Enabled: 1}
	shaA := strings.Repeat("a", 64)
	shaB := strings.Repeat("b", 64)
	shaC := strings.Repeat("c", 64)
	store.entries = []pluginSource.CatalogEntry{
		{ID: 1, SourceID: 1, SourceName: "a", UID: "demo", Version: "1.0.0", PackageSHA256: shaA},
		{ID: 2, SourceID: 2, SourceName: "b", UID: "demo", Version: "1.0.0", PackageSHA256: shaA},
		{ID: 3, SourceID: 1, SourceName: "a", UID: "other", Version: "2.0.0", PackageSHA256: shaB},
		{ID: 4, SourceID: 2, SourceName: "b", UID: "other", Version: "2.0.0", PackageSHA256: shaC},
	}
	store.installed = map[string]pluginSource.InstalledPlugin{"demo": {Version: "1.0.0", Status: "installed"}}
	handler := testHandler(store, fakeChecker{super: true}, adminSession())

	response := invoke(t, handler.catalogList, http.MethodPost, "/x", `{"page":1,"limit":40}`, adminSession())
	if response.Code != 0 {
		t.Fatalf("清单列表失败: %+v", response)
	}
	var data struct {
		Total int64 `json:"total"`
		List  []struct {
			ID               uint   `json:"id"`
			InstalledVersion string `json:"installed_version"`
			SameContent      bool   `json:"same_content"`
			SourceConflict   bool   `json:"source_conflict"`
		} `json:"list"`
		Sources []pluginSource.SourceSummary `json:"sources"`
	}
	if err := json.Unmarshal(response.Data, &data); err != nil {
		t.Fatalf("解析列表失败: %v", err)
	}
	if data.Total != 4 || len(data.List) != 4 || len(data.Sources) != 2 {
		t.Fatalf("列表读数不符: %+v", data)
	}
	for _, row := range data.List {
		switch row.ID {
		case 1, 2:
			if !row.SameContent || row.SourceConflict || row.InstalledVersion != "1.0.0" {
				t.Fatalf("同内容多来源标记不符: %+v", row)
			}
		case 3, 4:
			if !row.SourceConflict || row.SameContent {
				t.Fatalf("来源冲突标记不符: %+v", row)
			}
		}
	}
}

// TestCatalogListQueryPropagation 清单列表的 HTTP 入参（source_id / uid / keyword / page / limit）
// 必须原样落进 ListEntriesQuery——「过滤看着不生效」的另一半成因是请求体字段没被 handler 传下去。
//
// 本用例只钉**入参没被丢**（替身按固定读数应答、不做过滤）；「SQL 里真带 WHERE」由 internal/plugin/source
// 的库级用例承担（store_db_test.go 的 TestStoreListEntriesFiltersDB，临时库 DSN 门控、默认 SKIP）。
func TestCatalogListQueryPropagation(t *testing.T) {
	demoRow := pluginSource.CatalogEntry{ID: 1, SourceID: 2, UID: "demo", Version: "1.0.0"}
	cases := []struct {
		name      string
		body      string
		entries   []pluginSource.CatalogEntry
		want      pluginSource.ListEntriesQuery
		wantTotal int64
	}{
		{
			name:      "过滤命中（四字段全传）",
			body:      `{"source_id":2,"uid":"demo","keyword":"d","page":3,"limit":7}`,
			entries:   []pluginSource.CatalogEntry{demoRow, {ID: 2, SourceID: 2, UID: "demo", Version: "1.0.0"}},
			want:      pluginSource.ListEntriesQuery{SourceID: 2, UID: "demo", Keyword: "d", Page: 3, Limit: 7},
			wantTotal: 2,
		},
		{
			name:      "过滤无命中",
			body:      `{"uid":"no-such","page":1,"limit":40}`,
			entries:   nil,
			want:      pluginSource.ListEntriesQuery{UID: "no-such", Page: 1, Limit: 40},
			wantTotal: 0,
		},
		{
			name:      "过滤+分页组合",
			body:      `{"source_id":2,"uid":"demo","page":2,"limit":1}`,
			entries:   []pluginSource.CatalogEntry{demoRow},
			want:      pluginSource.ListEntriesQuery{SourceID: 2, UID: "demo", Page: 2, Limit: 1},
			wantTotal: 1,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			store := newHandlerStore()
			store.entries = testCase.entries
			handler := testHandler(store, fakeChecker{super: true}, adminSession())
			response := invoke(t, handler.catalogList, http.MethodPost, "/x", testCase.body, adminSession())
			if response.Code != 0 {
				t.Fatalf("清单列表失败: %+v", response)
			}
			if store.entriesCalls != 1 {
				t.Fatalf("ListEntries 调用次数应为 1，实际 %d（handler 没查即失败）", store.entriesCalls)
			}
			if store.lastEntriesQuery != testCase.want {
				t.Fatalf("清单过滤入参被丢弃或被改写：got %+v want %+v", store.lastEntriesQuery, testCase.want)
			}
			var data struct {
				Total int64 `json:"total"`
			}
			if err := json.Unmarshal(response.Data, &data); err != nil {
				t.Fatalf("解析 total 失败: %v", err)
			}
			if data.Total != testCase.wantTotal {
				t.Fatalf("回执 total 应原样取自 store：got %d want %d", data.Total, testCase.wantTotal)
			}
		})
	}
}

// TestCatalogListVersionNotInContract HTTP 清单接口**没有** version 过滤字段：请求体带了 version 也不进查询
// （版本过滤只在 CLI 侧：`aigo plugin catalog list --version`）。
//
// 接口表就是 {source_id?, uid?, keyword?, page, limit}——「不加 version」是明确定下的接口决定（加了要同步
// 设计稿与前端）。旧形态的「HTTP 过滤不生效」观测里，「探针把 CLI 习惯带进 body 塞了 version」正是两个候选
// 解释之一；本用例把该行为落成明文契约：多传字段被忽略，查询体只由那四个字段决定，且仍按 uid 正常过滤。
func TestCatalogListVersionNotInContract(t *testing.T) {
	store := newHandlerStore()
	store.entries = []pluginSource.CatalogEntry{{ID: 1, SourceID: 2, UID: "demo", Version: "1.0.0"}}
	handler := testHandler(store, fakeChecker{super: true}, adminSession())

	response := invoke(t, handler.catalogList, http.MethodPost, "/x",
		`{"source_id":2,"uid":"demo","version":"9.9.9","page":1,"limit":40}`, adminSession())
	if response.Code != 0 {
		t.Fatalf("清单列表失败: %+v", response)
	}
	want := pluginSource.ListEntriesQuery{SourceID: 2, UID: "demo", Page: 1, Limit: 40}
	if store.lastEntriesQuery != want {
		t.Fatalf("body 里的 version 不应进入查询体：got %+v want %+v", store.lastEntriesQuery, want)
	}
	var data struct {
		Total int64 `json:"total"`
	}
	if err := json.Unmarshal(response.Data, &data); err != nil || data.Total != 1 {
		t.Fatalf("带 version 的请求仍应按 uid 过滤并回报替身读数: %+v %v", data, err)
	}
}

func TestCatalogActionErrorMapping(t *testing.T) {
	handler := testHandler(newHandlerStore(), fakeChecker{super: true}, adminSession())
	handler.preflight = func(context.Context, *pluginCli.Env, pluginSource.Store, uint, string, string, string) (*pluginCli.CatalogPreflightResult, error) {
		return nil, &pluginCli.CatalogError{Code: "catalog_stale", Status: http.StatusConflict, Message: "清单已变化"}
	}
	response := invoke(t, handler.catalogPreflight, http.MethodPost, "/x",
		`{"source_id":1,"uid":"demo","version":"1.0.0"}`, adminSession())
	if response.Code != http.StatusConflict {
		t.Fatalf("预演错误码映射失败: %+v", response)
	}
	var data struct {
		ErrorCode string `json:"error_code"`
	}
	if err := json.Unmarshal(response.Data, &data); err != nil || data.ErrorCode != "catalog_stale" {
		t.Fatalf("error_code 不符: %+v %v", data, err)
	}

	response = invoke(t, handler.catalogInstall, http.MethodPost, "/x",
		`{"source_id":1,"uid":"demo","version":"1.0.0"}`, adminSession())
	if response.Code != http.StatusBadRequest {
		t.Fatalf("缺 confirm_sha256 应 400: %+v", response)
	}
}

func TestCatalogInstallSuccessMapping(t *testing.T) {
	handler := testHandler(newHandlerStore(), fakeChecker{super: true}, adminSession())
	handler.install = func(context.Context, *pluginCli.Env, pluginSource.Store, pluginCli.CatalogInstallRequest) (*pluginCli.CatalogInstallResult, error) {
		return &pluginCli.CatalogInstallResult{Name: "demo", Version: "1.0.0", Status: "installed", Effect: "rebuild_required"}, nil
	}
	response := invoke(t, handler.catalogInstall, http.MethodPost, "/x",
		`{"source_id":1,"uid":"demo","version":"1.0.0","confirm_sha256":"`+strings.Repeat("a", 64)+`"}`, adminSession())
	if response.Code != 0 {
		t.Fatalf("安装回执失败: %+v", response)
	}
	var result pluginCli.CatalogInstallResult
	if err := json.Unmarshal(response.Data, &result); err != nil || result.Effect != "rebuild_required" {
		t.Fatalf("安装回执不符: %+v %v", result, err)
	}
}

// TestCatalogInstallForbiddenInProduction：生产环境禁止安装——handler 第一道就拒（403 +
// error_code=lifecycle_forbidden），且**不进安装实现**（不下载、不暂存；写路径另有同款门禁兜底）。
func TestCatalogInstallForbiddenInProduction(t *testing.T) {
	store := newHandlerStore()
	store.sources[1] = pluginSource.Source{ID: 1, Name: "a", Enabled: 1}
	handler := testHandler(store, fakeChecker{super: true}, adminSession())
	handler.env = prodEnvironment
	installCalled := false
	handler.install = func(context.Context, *pluginCli.Env, pluginSource.Store, pluginCli.CatalogInstallRequest) (*pluginCli.CatalogInstallResult, error) {
		installCalled = true
		return nil, nil
	}

	response := invoke(t, handler.catalogInstall, http.MethodPost, "/x",
		`{"source_id":1,"uid":"demo","version":"1.0.0","confirm_sha256":"`+strings.Repeat("a", 64)+`"}`, adminSession())
	if response.Code != http.StatusForbidden {
		t.Fatalf("生产环境应 403: %+v", response)
	}
	var data struct {
		ErrorCode string `json:"error_code"`
	}
	if err := json.Unmarshal(response.Data, &data); err != nil || data.ErrorCode != "lifecycle_forbidden" {
		t.Fatalf("error_code 不符: %+v %v", data, err)
	}
	if installCalled {
		t.Fatalf("生产环境不得进入安装实现（应在参数校验/取包之前拒绝）")
	}

	// dev 环境同一请求照旧放行到安装实现（门禁不是无条件拒绝）
	handler.env = func() pluginCli.Environment {
		return pluginCli.Environment{Name: pluginCli.EnvDev, Source: pluginCli.EnvSourceAuto, LifecycleAllowed: true}
	}
	installCalled = false
	response = invoke(t, handler.catalogInstall, http.MethodPost, "/x",
		`{"source_id":1,"uid":"demo","version":"1.0.0","confirm_sha256":"`+strings.Repeat("a", 64)+`"}`, adminSession())
	if response.Code != 0 || !installCalled {
		t.Fatalf("dev 环境应放行到安装实现: %+v（installCalled=%v）", response, installCalled)
	}
}

// TestCatalogListCarriesEnv：清单列表响应带 env 段（插件页面据此渲染环境标识、生产环境禁用安装入口）。
// 判定的取值规则由 cli 侧用例覆盖，这里只验证 handler 把注入的判定原样发出。
func TestCatalogListCarriesEnv(t *testing.T) {
	handler := testHandler(newHandlerStore(), fakeChecker{super: true}, adminSession())
	var data struct {
		Env struct {
			Name             string `json:"name"`
			Source           string `json:"source"`
			LifecycleAllowed bool   `json:"lifecycle_allowed"`
		} `json:"env"`
	}

	response := invoke(t, handler.catalogList, http.MethodPost, "/x", `{"page":1,"limit":10}`, adminSession())
	if response.Code != 0 {
		t.Fatalf("清单列表失败: %+v", response)
	}
	if err := json.Unmarshal(response.Data, &data); err != nil {
		t.Fatalf("解析 env 段失败: %v", err)
	}
	if data.Env.Name != pluginCli.EnvDev || data.Env.Source != pluginCli.EnvSourceAuto || !data.Env.LifecycleAllowed {
		t.Fatalf("dev 环境 env 段不符: %+v", data.Env)
	}

	handler.env = prodEnvironment
	response = invoke(t, handler.catalogList, http.MethodPost, "/x", `{"page":1,"limit":10}`, adminSession())
	if err := json.Unmarshal(response.Data, &data); err != nil {
		t.Fatalf("解析 env 段失败: %v", err)
	}
	if data.Env.Name != pluginCli.EnvProd || data.Env.Source != pluginCli.EnvSourceConfig || data.Env.LifecycleAllowed {
		t.Fatalf("生产环境 env 段不符: %+v", data.Env)
	}
}

// TestSourceListCarriesEnv：来源配置页的列表响应也带 env 段（与 catalog/list、plugin/list 同口径）——
// 插件频道三页（管理 / 清单 / 来源）共用 usePluginEnv 的派生：生产环境据此弹整频道门禁。
func TestSourceListCarriesEnv(t *testing.T) {
	handler := testHandler(newHandlerStore(), fakeChecker{super: true}, adminSession())
	var data struct {
		Env struct {
			Name             string `json:"name"`
			LifecycleAllowed bool   `json:"lifecycle_allowed"`
		} `json:"env"`
	}

	response := invoke(t, handler.sourceList, http.MethodPost, "/x", `{"page":1,"limit":10}`, adminSession())
	if response.Code != 0 {
		t.Fatalf("来源列表失败: %+v", response)
	}
	if err := json.Unmarshal(response.Data, &data); err != nil {
		t.Fatalf("解析 env 段失败: %v", err)
	}
	if data.Env.Name != pluginCli.EnvDev || !data.Env.LifecycleAllowed {
		t.Fatalf("dev 环境 env 段不符: %+v", data.Env)
	}

	handler.env = prodEnvironment
	response = invoke(t, handler.sourceList, http.MethodPost, "/x", `{"page":1,"limit":10}`, adminSession())
	if err := json.Unmarshal(response.Data, &data); err != nil {
		t.Fatalf("解析 env 段失败: %v", err)
	}
	if data.Env.Name != pluginCli.EnvProd || data.Env.LifecycleAllowed {
		t.Fatalf("生产环境 env 段不符: %+v", data.Env)
	}
}

// TestCatalogSyncPartial：部分来源失败时仍 code=0，逐来源结果在 results，partial=true；
// 全部失败时 partial=false（前端据此决定要不要弹全局成功提示）。
func TestCatalogSyncPartial(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(root+"/good.yaml", []byte("schema: ai-go-plugin-catalog/v1\nplugins: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := newHandlerStore()
	store.sources[1] = pluginSource.Source{ID: 1, Name: "good", Type: pluginSource.TypeLocal, Location: "good.yaml", Enabled: 1}
	store.sources[2] = pluginSource.Source{ID: 2, Name: "broken", Type: pluginSource.TypeLocal, Location: "missing.yaml", Enabled: 1}
	handler := testHandler(store, fakeChecker{super: true}, adminSession())
	handler.newEnv = func() (*pluginCli.Env, error) { return &pluginCli.Env{Root: root}, nil }

	var data struct {
		Results []pluginSource.SyncResult `json:"results"`
		Partial bool                      `json:"partial"`
	}
	response := invoke(t, handler.catalogSync, http.MethodPost, "/x", `{}`, adminSession())
	if response.Code != 0 {
		t.Fatalf("同步应 code=0（逐来源结果在 results）: %+v", response)
	}
	if err := json.Unmarshal(response.Data, &data); err != nil {
		t.Fatalf("解析同步回执失败: %v", err)
	}
	if len(data.Results) != 2 || !data.Partial {
		t.Fatalf("部分失败应 partial=true 且两条结果: %+v", data)
	}
	statusByName := map[string]string{}
	for _, item := range data.Results {
		statusByName[item.Name] = item.Status
		if item.Name == "broken" && item.Error == "" {
			t.Fatalf("失败来源应带错误原因: %+v", item)
		}
		// 失败原因不得回显绝对路径（脱敏口径与 source/test 一致）
		if strings.Contains(item.Error, root) {
			t.Fatalf("错误原因泄露绝对路径: %s", item.Error)
		}
	}
	if statusByName["good"] != "ok" || statusByName["broken"] != "error" {
		t.Fatalf("逐来源状态不符: %+v", statusByName)
	}

	// 全部失败：partial 必须为 false（没有成功来源）
	store.sources[1] = pluginSource.Source{ID: 1, Name: "good", Type: pluginSource.TypeLocal, Location: "missing.yaml", Enabled: 1}
	response = invoke(t, handler.catalogSync, http.MethodPost, "/x", `{}`, adminSession())
	if err := json.Unmarshal(response.Data, &data); err != nil {
		t.Fatalf("解析同步回执失败: %v", err)
	}
	if data.Partial {
		t.Fatalf("无成功来源时 partial 应为 false: %+v", data)
	}
}
