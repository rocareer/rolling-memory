// plugin: kb v1.0.0
package kb

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-go-hub/ai-go-admin/internal/dto"
	"github.com/ai-go-hub/ai-go-admin/internal/infra/config"
	"github.com/ai-go-hub/ai-go-admin/internal/infra/database"
	"github.com/ai-go-hub/ai-go-admin/internal/middleware"
	hostmodel "github.com/ai-go-hub/ai-go-admin/internal/model"
	"github.com/ai-go-hub/ai-go-admin/plugins/kb/model"
	svcKB "github.com/ai-go-hub/ai-go-admin/plugins/kb/service/kb"
	"github.com/ai-go-hub/ai-go-admin/plugins/kb/service/kb/chunk"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// 检索探针/逐字核对 HTTP 面连库用例：
// 覆盖响应形状（list/total/mode/top_k/limit/degraded/readable_libraries）、上限截断回显、
// 逐字核对逐字一致、拒绝同型（deniedMessage）、未登录 fail-closed。
// 门控：PLUGIN_KB_DB=1（同前）；跑法：PLUGIN_KB_DB=1 go test -count=1 -timeout=120s ./plugins/kb/handler/admin/kb/
const kbHandlerTestDBEnv = "PLUGIN_KB_DB"

func chdirRepoRootHandler(t *testing.T) {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			if err := os.Chdir(dir); err != nil {
				t.Fatal(err)
			}
			return
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("未找到 go.mod（仓库根）")
		}
		dir = parent
	}
}

// probeResp 探针响应（显式字段面；断言用）
type probeResp struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    struct {
		List []struct {
			ChunkID   uint     `json:"chunk_id"`
			LibraryID uint     `json:"library_id"`
			Title     string   `json:"title"`
			Snippet   string   `json:"snippet"`
			Highlight [2]int   `json:"highlight"`
			Score     float64  `json:"score"`
			Legs      []string `json:"legs"`
			Tags      []string `json:"tags"`
			Locator   struct {
				Anchor    string `json:"anchor"`
				Kind      string `json:"kind"`
				CharStart int    `json:"char_start"`
				CharEnd   int    `json:"char_end"`
			} `json:"locator"`
		} `json:"list"`
		Total             int      `json:"total"`
		Mode              string   `json:"mode"`
		TopK              int      `json:"top_k"`
		Limit             int      `json:"limit"`
		Degraded          []string `json:"degraded"`
		ReadableLibraries int      `json:"readable_libraries"`
	} `json:"data"`
}

// chunkResp 逐字核对响应
type chunkResp struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    struct {
		Row struct {
			ChunkID  uint   `json:"chunk_id"`
			Text     string `json:"text"`
			TextHash string `json:"text_hash"`
			Locator  struct {
				Anchor string `json:"anchor"`
				Kind   string `json:"kind"`
			} `json:"locator"`
		} `json:"row"`
	} `json:"data"`
}

func TestSearchProbeHTTPDB(t *testing.T) {
	if os.Getenv(kbHandlerTestDBEnv) != "1" {
		t.Skipf("需要数据库：设 %s=1 启用（库需已应用本插件的自管迁移）", kbHandlerTestDBEnv)
	}
	chdirRepoRootHandler(t)
	if err := config.Init(); err != nil {
		t.Fatalf("初始化配置: %v", err)
	}
	if err := database.Init(); err != nil {
		t.Fatalf("初始化数据库: %v", err)
	}
	requireKBTestDatabase(t, database.DB())
	applyPluginMigrations(t)
	db := database.DB()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	token := "smokekbsearch" + suffix + "httpprobe"
	text := "冒烟检索样本 " + token + " 尾注"

	// 夹具：库 + 就绪文档 + 切片（tsv 按写侧单点同款生成）
	lib := &model.KBLibrary{OwnerKind: model.KBLibraryOwnerKindSystem, Name: "smoke-kb-search-http-" + suffix}
	if err := gorm.G[model.KBLibrary](db).Create(t.Context(), lib); err != nil {
		t.Fatalf("建库失败: %v", err)
	}
	build := chunk.TextHash("http-build-" + suffix)
	doc := &model.KBDocument{
		LibraryID: lib.ID, Title: "smoke-kb-search-http-doc-" + suffix, SourceKind: model.KBDocumentSourceText,
		ContentHash: chunk.TextHash("http-doc-" + suffix), BuildState: model.KBDocumentBuildReady, ActiveBuild: build,
		CreatedByKind: model.KBActorKindAdmin, CreatedByID: 990001,
	}
	if err := gorm.G[model.KBDocument](db).Create(t.Context(), doc); err != nil {
		t.Fatalf("建文档失败: %v", err)
	}
	ck := &model.KBChunk{
		LibraryID: lib.ID, DocumentID: doc.ID, BuildHash: build, Chunker: "fixed", ChunkerVersion: "v1",
		Locator: fmt.Sprintf("kb:doc:%d#p1", doc.ID), CharStart: 0, CharEnd: len([]rune(text)),
		Text: text, TextHash: chunk.TextHash(text),
	}
	if err := gorm.G[model.KBChunk](db).Create(t.Context(), ck); err != nil {
		t.Fatalf("建切片失败: %v", err)
	}
	// 匹配面列 match_text（词面腿匹配 match_text，tsv 为 DB 生成列自其派生；
	// 本夹具无标题/表头派生前缀 ⇒ match_text = verbatim 文本）
	if err := db.Exec("UPDATE kb_chunk SET match_text = text WHERE id = ?", ck.ID).Error; err != nil {
		t.Fatalf("回填 match_text 失败: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Exec("DELETE FROM kb_chunk WHERE id = ?", ck.ID).Error
		_ = db.Exec("DELETE FROM kb_search_log WHERE query_text LIKE ?", "%"+suffix+"%").Error
		_ = db.Exec("DELETE FROM kb_document WHERE id = ?", doc.ID).Error
		_ = db.Exec("DELETE FROM kb_library WHERE id = ?", lib.ID).Error
	})

	engine := newProbeEngine(990001)
	srv := httptest.NewServer(engine)
	defer srv.Close()

	post := func(path, body string) (*httptest.ResponseRecorder, *probeResp) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPost, srv.URL+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, req)
		var out probeResp
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("响应非 JSON: %v (%s)", err, rec.Body.String())
		}
		return rec, &out
	}

	// ① 命中 + 响应形状
	rec, out := post("/admin/kb/search", fmt.Sprintf(`{"query":%q,"mode":"lexical"}`, token))
	if rec.Code != http.StatusOK || out.Code != 0 {
		t.Fatalf("探针未成功: http=%d code=%d msg=%s", rec.Code, out.Code, out.Message)
	}
	if out.Data.Total != 1 || len(out.Data.List) != 1 {
		t.Fatalf("命中数不符：total=%d list=%d", out.Data.Total, len(out.Data.List))
	}
	item := out.Data.List[0]
	if item.ChunkID != ck.ID || item.LibraryID != lib.ID {
		t.Fatalf("命中块不符：%+v", item)
	}
	if out.Data.Mode != "lexical" || out.Data.TopK != 8 || out.Data.Limit != 8 {
		t.Fatalf("档位回显不符：mode=%s top_k=%d limit=%d", out.Data.Mode, out.Data.TopK, out.Data.Limit)
	}
	if out.Data.Degraded == nil || len(out.Data.Degraded) != 0 {
		t.Fatalf("degraded 应为空数组：%v", out.Data.Degraded)
	}
	if out.Data.ReadableLibraries < 1 {
		t.Fatalf("可读库数回显缺失：%d", out.Data.ReadableLibraries)
	}
	if item.Tags == nil || len(item.Tags) != 0 {
		t.Fatalf("tags 应为空数组：%v", item.Tags)
	}
	if len(item.Legs) == 0 || item.Score <= 0 {
		t.Fatalf("贡献腿/融合分缺失：legs=%v score=%v", item.Legs, item.Score)
	}
	if item.Locator.Anchor != ck.Locator || item.Locator.Kind != "file" || item.Locator.CharEnd <= item.Locator.CharStart {
		t.Fatalf("定位结构不合规：%+v", item.Locator)
	}
	if !strings.Contains(text, item.Snippet) || !strings.Contains(item.Snippet, token) {
		t.Fatalf("snippet 非原文逐字子串：%q", item.Snippet)
	}
	if item.Highlight[0] < 0 || item.Highlight[1] <= item.Highlight[0] {
		t.Fatalf("高亮区间缺失：%v", item.Highlight)
	}

	// ② 上限截断回显
	_, out = post("/admin/kb/search", fmt.Sprintf(`{"query":%q,"mode":"lexical","top_k":500,"limit":5000}`, token))
	if out.Code != 0 || out.Data.TopK != 50 || out.Data.Limit != 100 {
		t.Fatalf("上限回显不符：code=%d top_k=%d limit=%d", out.Code, out.Data.TopK, out.Data.Limit)
	}

	// ③ 入参拒绝（缺检索词 / 模式闭集外）
	if _, out = post("/admin/kb/search", `{"mode":"lexical"}`); out.Code == 0 {
		t.Fatal("缺 query 未拒")
	}
	if _, out = post("/admin/kb/search", fmt.Sprintf(`{"query":%q,"mode":"full"}`, token)); out.Code == 0 {
		t.Fatal("闭集外 mode 未拒")
	}

	// ④ 逐字核对（GET；逐字一致 + 同型拒）
	getReq, _ := http.NewRequest(http.MethodGet, srv.URL+fmt.Sprintf("/admin/kb/chunk/get/%d", ck.ID), nil)
	getRec := httptest.NewRecorder()
	engine.ServeHTTP(getRec, getReq)
	var got chunkResp
	if err := json.Unmarshal(getRec.Body.Bytes(), &got); err != nil {
		t.Fatalf("核对响应非 JSON: %v", err)
	}
	if got.Code != 0 || got.Data.Row.Text != text || got.Data.Row.TextHash != chunk.TextHash(text) {
		t.Fatalf("逐字核对不平：code=%d text=%q hash=%s", got.Code, got.Data.Row.Text, got.Data.Row.TextHash)
	}
	if got.Data.Row.Locator.Anchor != ck.Locator {
		t.Fatalf("核对定位锚不符：%s", got.Data.Row.Locator.Anchor)
	}
	getReq, _ = http.NewRequest(http.MethodGet, srv.URL+"/admin/kb/chunk/get/987654321", nil)
	getRec = httptest.NewRecorder()
	engine.ServeHTTP(getRec, getReq)
	_ = json.Unmarshal(getRec.Body.Bytes(), &got)
	if got.Code == 0 || got.Message != deniedMessage {
		t.Fatalf("不存在切片应为同型拒（%s）：code=%d msg=%s", deniedMessage, got.Code, got.Message)
	}

	// ⑤ 未登录（无会话注入）fail-closed：同型拒
	noAuth := newProbeEngine(0)
	srv2 := httptest.NewServer(noAuth)
	defer srv2.Close()
	req, _ := http.NewRequest(http.MethodPost, srv2.URL+"/admin/kb/search", strings.NewReader(fmt.Sprintf(`{"query":%q}`, token)))
	req.Header.Set("Content-Type", "application/json")
	rec2 := httptest.NewRecorder()
	noAuth.ServeHTTP(rec2, req)
	var out2 probeResp
	if err := json.Unmarshal(rec2.Body.Bytes(), &out2); err != nil {
		t.Fatalf("未登录响应非 JSON: %v", err)
	}
	if out2.Code == 0 || out2.Message != deniedMessage {
		t.Fatalf("未登录应为同型拒：code=%d msg=%s", out2.Code, out2.Message)
	}
}

// newProbeEngine 构造仅含检索两路由的测试引擎（会话注入：adminID>0 注入合成会话；0 = 不注入）
func newProbeEngine(adminID uint) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		if adminID > 0 {
			c.Set(middleware.CtxAdminKey, &dto.AdminSession{Admin: &hostmodel.Admin{ID: adminID}, Token: "test"})
		}
		c.Next()
	})
	h := NewHandler(svcKB.NewService())
	group := r.Group("/admin/kb")
	group.POST("/search", h.SearchProbe)
	group.GET("/chunk/get/:pk", h.ChunkGet)
	return r
}
