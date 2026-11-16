package routine

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/ai-go-hub/ai-go-admin/internal/dto"
	handlerAuth "github.com/ai-go-hub/ai-go-admin/internal/handler/admin/auth"
	"github.com/ai-go-hub/ai-go-admin/internal/infra/config"
	"github.com/ai-go-hub/ai-go-admin/internal/infra/database"
	"github.com/ai-go-hub/ai-go-admin/internal/middleware"
	"github.com/ai-go-hub/ai-go-admin/internal/model"
	repoAdmin "github.com/ai-go-hub/ai-go-admin/internal/repository/admin"
	svcAuth "github.com/ai-go-hub/ai-go-admin/internal/service/admin/auth"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
)

// 自助资料改密的凭证校验（真库用例）：
// 令牌只证明「当前会话」，不证明持钥人对该账号有处置权 ⇒ 自助改密必须校验当前密码。
//
//	运行：PROFILE_DB=1 DATABASE_WRITE_DBNAME=<独立测试库> \
//	      go test ./internal/handler/admin/routine/ -run CurrentPassword -v
//
// 未设 PROFILE_DB 时跳过（默认不触库）；用例只允许跑在库名含 test/probe 的独立测试库上，
// 探针行在用例结束时删除（可重复运行）。
const (
	probeAdminID   = 9901
	probeAdmin2ID  = 9902
	probeGroupID   = 9901
	probeUsername  = "zz-probe-admin"
	probeUsername2 = "zz-probe-admin2"
	probeOldPass   = "OldPass!123"
	probeNewPass   = "NewPass!456"
	probeWrong     = "WrongPass!789"
)

var probeOnce sync.Once

// requireTestDB 拒绝在非测试库上运行：本用例会删除/重建固定 id 的探针账号与分组。
func requireTestDB(t *testing.T) {
	t.Helper()
	name := strings.ToLower(config.Get().Database.Write.DBName)
	if name == "" || (!strings.Contains(name, "test") && !strings.Contains(name, "probe")) {
		t.Fatalf("拒绝在非测试库上运行（dbname=%q）：请用 DATABASE_WRITE_DBNAME 指向名字含 test/probe 的独立库", name)
	}
}

func probeSetup(t *testing.T) {
	t.Helper()
	if os.Getenv("PROFILE_DB") != "1" {
		t.Skip("真库用例：需 PROFILE_DB=1 且 DATABASE_WRITE_DBNAME 指向独立测试库")
	}
	probeOnce.Do(func() {
		if err := config.Init(); err != nil {
			t.Fatalf("初始化配置失败: %v", err)
		}
		requireTestDB(t)
		if err := database.Init(); err != nil {
			t.Fatalf("初始化数据库失败: %v", err)
		}
		if err := database.DB().AutoMigrate(&model.Admin{}, &model.AdminGroup{}, &model.AdminGroupAccess{}, &model.Token{}); err != nil {
			t.Fatalf("建表失败: %v", err)
		}
	})
}

// seedProbe 造探针账号（rules='*' 的分组 ⇒ 超管，走通可见范围校验）并把口令复位
func seedProbe(t *testing.T) {
	t.Helper()

	hash, err := bcrypt.GenerateFromPassword([]byte(probeOldPass), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("生成口令失败: %v", err)
	}

	db := database.DB()
	if err := db.Exec("DELETE FROM admin_group_access WHERE uid IN ?", []uint{probeAdminID, probeAdmin2ID}).Error; err != nil {
		t.Fatalf("清理旧探针分组关联失败: %v", err)
	}
	if err := db.Exec("DELETE FROM admin_groups WHERE id = ?", probeGroupID).Error; err != nil {
		t.Fatalf("清理旧探针分组失败: %v", err)
	}
	if err := db.Exec("DELETE FROM admins WHERE id IN ?", []uint{probeAdminID, probeAdmin2ID}).Error; err != nil {
		t.Fatalf("清理旧探针账号失败: %v", err)
	}
	t.Cleanup(func() {
		db.Exec("DELETE FROM admin_group_access WHERE uid IN ?", []uint{probeAdminID, probeAdmin2ID})
		db.Exec("DELETE FROM admin_groups WHERE id = ?", probeGroupID)
		db.Exec("DELETE FROM admins WHERE id IN ?", []uint{probeAdminID, probeAdmin2ID})
	})

	rules, status := "*", uint8(1)
	if err := db.Create(&model.Admin{ID: probeAdminID, Username: probeUsername, Nickname: "探针", Password: string(hash), Status: "enable"}).Error; err != nil {
		t.Fatalf("造探针账号失败: %v", err)
	}
	if err := db.Create(&model.Admin{ID: probeAdmin2ID, Username: probeUsername2, Nickname: "探针二", Password: string(hash), Status: "enable"}).Error; err != nil {
		t.Fatalf("造第二探针账号失败: %v", err)
	}
	if err := db.Create(&model.AdminGroup{ID: probeGroupID, Name: "zz-probe-super", Rules: &rules, Status: &status}).Error; err != nil {
		t.Fatalf("造探针分组失败: %v", err)
	}
	if err := db.Create(&model.AdminGroupAccess{UID: probeAdminID, GroupID: probeGroupID}).Error; err != nil {
		t.Fatalf("造探针分组关联失败: %v", err)
	}
	if err := db.Create(&model.AdminGroupAccess{UID: probeAdmin2ID, GroupID: probeGroupID}).Error; err != nil {
		t.Fatalf("造第二探针分组关联失败: %v", err)
	}
}

func passwordMatchesByID(t *testing.T, id uint, want string) bool {
	t.Helper()

	var admin model.Admin
	if err := database.DB().First(&admin, id).Error; err != nil {
		t.Fatalf("读探针账号失败: %v", err)
	}
	return bcrypt.CompareHashAndPassword([]byte(admin.Password), []byte(want)) == nil
}

func probePasswordMatches(t *testing.T, want string) bool {
	t.Helper()
	return passwordMatchesByID(t, probeAdminID, want)
}

func probeEngine() *gin.Engine {
	svc := svcAuth.NewAuthAdminService(repoAdmin.NewAdminRepository())
	h := NewAuthAdminProfileHandler(svc)

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.POST("/profile/update/:pk", func(c *gin.Context) {
		// 直接注入会话：本用例只验改密的凭证校验，不重复走认证/权限中间件
		c.Set(middleware.CtxAdminKey, &dto.AdminSession{
			Admin: &model.Admin{ID: probeAdminID, Username: probeUsername, Status: "enable"},
			Token: "zz-probe-token",
		})
	}, h.Update)
	return engine
}

func postProfileUpdate(t *testing.T, engine *gin.Engine, body string) int {
	t.Helper()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/profile/update/"+strconv.Itoa(probeAdminID), strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	engine.ServeHTTP(rec, req)

	var resp struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("响应解析失败: %v（body=%s）", err, rec.Body.String())
	}
	t.Logf("POST %s → code=%d message=%s", body, resp.Code, resp.Message)
	return resp.Code
}

func profileUpdateBody(extra string) string {
	base := `"username":"` + probeUsername + `","nickname":"探针改","status":"enable"`
	if extra != "" {
		base += "," + extra
	}
	return "{" + base + "}"
}

// TestProfileUpdatePasswordRequiresCurrentPassword 改密必须校验当前密码；只改资料不得被拦
func TestProfileUpdatePasswordRequiresCurrentPassword(t *testing.T) {
	probeSetup(t)
	seedProbe(t)
	engine := probeEngine()

	// ① 缺当前密码：必须拒绝，且库内口令不变
	if code := postProfileUpdate(t, engine, profileUpdateBody(`"password":"`+probeNewPass+`"`)); code == 0 {
		t.Fatal("缺当前密码的改密请求被放行（自助改密无凭证校验）")
	}
	if !probePasswordMatches(t, probeOldPass) {
		t.Fatal("被拒绝的请求仍然改掉了库内口令")
	}

	// ② 当前密码错误：必须拒绝
	if code := postProfileUpdate(t, engine, profileUpdateBody(`"password":"`+probeNewPass+`","old_password":"`+probeWrong+`"`)); code == 0 {
		t.Fatal("当前密码错误的改密请求被放行")
	}
	if !probePasswordMatches(t, probeOldPass) {
		t.Fatal("当前密码错误的请求仍然改掉了库内口令")
	}

	// ③ 当前密码正确：放行且口令更新
	if code := postProfileUpdate(t, engine, profileUpdateBody(`"password":"`+probeNewPass+`","old_password":"`+probeOldPass+`"`)); code != 0 {
		t.Fatal("当前密码正确的改密请求被拒")
	}
	if !probePasswordMatches(t, probeNewPass) {
		t.Fatal("改密成功后库内口令未更新")
	}

	// ④ 只改资料（不带 password）：不得要求当前密码
	if code := postProfileUpdate(t, engine, profileUpdateBody(`"email":""`)); code != 0 {
		t.Fatal("只改资料的请求被拦")
	}
}

// adminUpdateEngine 管理端更新引擎（注入会话，不重复走认证/权限中间件）
func adminUpdateEngine(sessionID uint, sessionUsername string) *gin.Engine {
	svc := svcAuth.NewAuthAdminService(repoAdmin.NewAdminRepository())
	h := handlerAuth.NewAuthAdminHandler(svc)

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.POST("/admin/auth/admin/update/:pk", func(c *gin.Context) {
		c.Set(middleware.CtxAdminKey, &dto.AdminSession{
			Admin: &model.Admin{ID: sessionID, Username: sessionUsername, Status: "enable"},
			Token: "zz-probe-token",
		})
	}, h.Update)
	return engine
}

func postAdminUpdate(t *testing.T, engine *gin.Engine, targetID uint, body string) int {
	t.Helper()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/admin/auth/admin/update/"+strconv.FormatUint(uint64(targetID), 10), strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	engine.ServeHTTP(rec, req)

	var resp struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("响应解析失败: %v（body=%s）", err, rec.Body.String())
	}
	t.Logf("POST update/%d %s → code=%d message=%s", targetID, body, resp.Code, resp.Message)
	return resp.Code
}

func adminUpdateBody(username, extra string) string {
	base := `"username":"` + username + `","nickname":"探针改","status":"enable"`
	if extra != "" {
		base += "," + extra
	}
	return "{" + base + "}"
}

// TestAdminUpdateSelfPasswordRequiresCurrentPassword 管理端编辑自己改密必须回验当前密码；
// 代改他人（操作者 != 目标）不要求当前密码。
func TestAdminUpdateSelfPasswordRequiresCurrentPassword(t *testing.T) {
	probeSetup(t)
	seedProbe(t)
	engine := adminUpdateEngine(probeAdminID, probeUsername)

	// ① 编辑自己改密缺当前密码：拒绝，且库内口令不变
	if code := postAdminUpdate(t, engine, probeAdminID, adminUpdateBody(probeUsername, `"password":"`+probeNewPass+`"`)); code == 0 {
		t.Fatal("管理端编辑自己改密缺当前密码被放行")
	}
	if !probePasswordMatches(t, probeOldPass) {
		t.Fatal("被拒绝的请求仍然改掉了库内口令")
	}

	// ② 当前密码错误：拒绝
	if code := postAdminUpdate(t, engine, probeAdminID, adminUpdateBody(probeUsername, `"password":"`+probeNewPass+`","old_password":"`+probeWrong+`"`)); code == 0 {
		t.Fatal("管理端编辑自己改密在当前密码错误时被放行")
	}
	if !probePasswordMatches(t, probeOldPass) {
		t.Fatal("当前密码错误的请求仍然改掉了库内口令")
	}

	// ③ 当前密码正确：放行且口令更新
	if code := postAdminUpdate(t, engine, probeAdminID, adminUpdateBody(probeUsername, `"password":"`+probeNewPass+`","old_password":"`+probeOldPass+`"`)); code != 0 {
		t.Fatal("管理端编辑自己改密在当前密码正确时被拒")
	}
	if !probePasswordMatches(t, probeNewPass) {
		t.Fatal("管理端编辑自己改密成功后库内口令未更新")
	}

	// ④ 编辑自己只改资料：不得要求当前密码
	if code := postAdminUpdate(t, engine, probeAdminID, adminUpdateBody(probeUsername, `"email":""`)); code != 0 {
		t.Fatal("管理端编辑自己只改资料被拦")
	}

	// ⑤ 代改他人（会话 9901 → 目标 9902）：不要求当前密码
	seedProbe(t)
	if code := postAdminUpdate(t, engine, probeAdmin2ID, adminUpdateBody(probeUsername2, `"password":"`+probeNewPass+`"`)); code != 0 {
		t.Fatal("代改他人口令被拦（操作者不掌握目标口令，不应要求当前密码）")
	}
	if !passwordMatchesByID(t, probeAdmin2ID, probeNewPass) {
		t.Fatal("代改他人口令成功后库内口令未更新")
	}
}
