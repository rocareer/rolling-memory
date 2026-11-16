// plugin: kb v1.0.0
// 知识库运维端点（本插件新增：宿主 CLI 子命令是**固定闭集**，插件没有注册 CLI 命令的扩展点，
// 且插件不得改宿主 cmd/ 下的文件 ⇒ 原来的三个运维子命令整体迁为后台端点）。
//
// 三个端点都是 POST（仓内纪律：只用 GET/POST），各自挂 AdminAuth + AdminPermission，节点同名：
//
//	POST /admin/plugin/kb/maintenance/check   → plugin/kb/maintenance/check
//	POST /admin/plugin/kb/maintenance/reindex → plugin/kb/maintenance/reindex
//	POST /admin/plugin/kb/maintenance/reembed → plugin/kb/maintenance/reembed
//
// 语义对照（迁移前 → 迁移后）：
//   - check   ：团队绑定巡检 + 向量身份点名 + 匹配面回填探测 → 三段读数收成一份 JSON 回执；
//   - reindex ：按 ChunkTextOf 复算并回填 match_text（幂等；支持 dry_run 预演与 library_id 收窄）；
//   - reembed ：按当前嵌入身份投递 kb:rebuild(mode=reembed)（逐库一份作业）。**身份不可用即响亮失败**
//     （无密钥/未配模型的环境下这是正确行为：不静默忽略、不假装投递成功）。
//
// 回执即原 CLI 打印的读数（JSON 化），失败仍是失败（HTTP 层同型错误），不做「成功一半」的静默。
package kb

import (
	"github.com/ai-go-hub/ai-go-admin/internal/kit/httpx"
	"github.com/ai-go-hub/ai-go-admin/internal/middleware"
	svcKB "github.com/ai-go-hub/ai-go-admin/plugins/kb/service/kb"
	kbembed "github.com/ai-go-hub/ai-go-admin/plugins/kb/service/kb/embed"
	kbingest "github.com/ai-go-hub/ai-go-admin/plugins/kb/service/kb/ingest"

	"github.com/gin-gonic/gin"
)

// MaintenanceHandler 运维端点控制器（复用主控制器的面/轴助手与同一服务实例）
type MaintenanceHandler struct {
	base *Handler
}

// NewMaintenanceHandler 创建运维端点控制器
func NewMaintenanceHandler(svc *svcKB.Service) *MaintenanceHandler {
	return &MaintenanceHandler{base: NewHandler(svc)}
}

// RegisterRoutes 注册运维端点（挂 AdminAuth+AdminPermission）
func (h *MaintenanceHandler) RegisterRoutes(group *gin.RouterGroup) {
	group.POST("/check", middleware.AdminAuth(), middleware.AdminPermission(), h.Check)
	group.POST("/reindex", middleware.AdminAuth(), middleware.AdminPermission(), h.Reindex)
	group.POST("/reembed", middleware.AdminAuth(), middleware.AdminPermission(), h.Reembed)
}

// Check 运维巡检（只读）：团队绑定点名 + 向量身份点名 + 匹配面回填探测。
//
// 请求体：{}
// 回执：{teams, vector, match_text} —— teams 段 = CheckTeams 报告（行级点名 + 待处置计数），
// vector 段 = 向量身份报告（身份不可用时 identity_error 非空），match_text 段 = 未回填行数。
func (h *MaintenanceHandler) Check(c *gin.Context) {
	ctx := c.Request.Context()
	if _, ok := h.base.adminView(c); !ok {
		return
	}

	teams, err := h.base.svc.CheckTeams(ctx)
	if err != nil {
		failService(c, "巡检失败（团队绑定）", err)
		return
	}
	vector, err := kbembed.CheckVectorIdentities(ctx, h.base.svc.DB())
	if err != nil {
		failService(c, "巡检失败（向量身份）", err)
		return
	}
	pending, err := kbingest.MatchTextPendingProbe(ctx, h.base.svc.DB())
	if err != nil {
		failService(c, "巡检失败（匹配面回填探测）", err)
		return
	}

	httpx.Success(c, httpx.WithData(gin.H{
		"teams":      teams,
		"vector":     vector,
		"match_text": gin.H{"pending": pending},
	}))
}

// reindexRequest 词面索引回填入参
type reindexRequest struct {
	DryRun    bool `json:"dry_run"`
	LibraryID uint `json:"library_id"`
}

// Reindex 词面索引回填（按行复算 match_text；幂等可复跑；dry_run = 只统计不写库）。
//
// 请求体：{dry_run?, library_id?}
// 回执：{scanned, pending, updated, unchanged, snapshot_missing, failed, errors[]}
func (h *MaintenanceHandler) Reindex(c *gin.Context) {
	var req reindexRequest
	if err := bindJSON(c, &req); err != nil {
		httpx.Fail(c, httpx.WithMessage("参数错误: "+err.Error()))
		return
	}
	if _, ok := h.base.adminView(c); !ok {
		return
	}
	stat, err := kbingest.ReindexMatchText(c.Request.Context(), h.base.svc.DB(), kbingest.MatchTextOptions{
		LibraryID: req.LibraryID,
		DryRun:    req.DryRun,
	})
	if err != nil {
		failService(c, "词面索引回填失败", err)
		return
	}
	httpx.Success(c, httpx.WithData(gin.H{
		"dry_run":          req.DryRun,
		"scanned":          stat.Scanned,
		"pending":          stat.Pending,
		"updated":          stat.Updated,
		"unchanged":        stat.Unchanged,
		"snapshot_missing": stat.SnapshotMissing,
		"failed":           stat.Failed,
		"errors":           stat.Errors,
	}))
}

// reembedRequest 重投嵌入入参
type reembedRequest struct {
	LibraryID uint `json:"library_id"` // 0 = 全部未删库（逐库一份作业）
}

// Reembed 按当前嵌入身份投递重嵌作业（键 kb:rebuild:<library_id>:auto；同键单飞）。
//
// 请求体：{library_id?}
// 回执：{identity, jobs:[{library_id, job_id, job_key, created}], count}
//
// 身份不可用（Provider 未注册/模型未配置）⇒ 响亮失败：没有身份就无法重产向量，
// 静默返回成功会让运维以为已经重嵌。
func (h *MaintenanceHandler) Reembed(c *gin.Context) {
	var req reembedRequest
	if err := bindJSON(c, &req); err != nil {
		httpx.Fail(c, httpx.WithMessage("参数错误: "+err.Error()))
		return
	}
	if _, ok := h.base.adminView(c); !ok {
		return
	}
	out, err := h.base.svc.EnqueueReembed(c.Request.Context(), req.LibraryID)
	if err != nil {
		failService(c, "投递重嵌作业失败", err)
		return
	}
	httpx.Success(c, httpx.WithData(out))
}
