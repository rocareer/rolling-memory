// plugin: kb v1.0.0
// 知识库文档导入端点（**本插件新增**：宿主核心没有入库入口，没有它装完就是空库、核心闭环不成立）。
//
// 路由与节点（仅 POST；节点名 = 去后台入口前缀的路径，末段非 list/get ⇒ 无 read 回落）：
//
//	POST /admin/plugin/kb/document/import → plugin/kb/document/import
//
// 入参两种形态**合一**（同一段下游逻辑，见 service/kb/import.go）：
//   - `application/json`：{library_id, name?, text}（粘贴文本入库）；
//   - `multipart/form-data`：file 字段 + library_id / name（文件入库；格式不在提取闭集 ⇒ 入口即拒）。
//
// 回执：{document_id, created, job_id, job_key, created_job, build_state}
//   - created=false ⇒ 同库同内容已存在（幂等命中既有文档，不新建、不重复投递）；
//   - created_job=false ⇒ 同键活跃作业已在飞（单飞语义）。
//
// 隔离：写谓词门在服务层（RequireWrite 单点）；「不存在」与「无权」同型（failService 压成唯一文案）。
package kb

import (
	"io"
	"mime/multipart"
	"strconv"
	"strings"

	"github.com/ai-go-hub/ai-go-admin/internal/kit/httpx"
	"github.com/ai-go-hub/ai-go-admin/internal/middleware"
	svcKB "github.com/ai-go-hub/ai-go-admin/plugins/kb/service/kb"

	"github.com/gin-gonic/gin"
)

// maxImportBytes 单次导入的字节上限（与提取层的来源帽同源；超限响亮拒绝，不做截断入库）
const maxImportBytes = 64 << 20

// ImportHandler 文档导入端点控制器（复用主控制器的面/轴助手与同一服务实例）
type ImportHandler struct {
	base *Handler
}

// NewImportHandler 创建导入端点控制器
func NewImportHandler(svc *svcKB.Service) *ImportHandler {
	return &ImportHandler{base: NewHandler(svc)}
}

// RegisterRoutes 注册导入端点（挂 AdminAuth+AdminPermission）
func (h *ImportHandler) RegisterRoutes(group *gin.RouterGroup) {
	group.POST("/import", middleware.AdminAuth(), middleware.AdminPermission(), h.Import)
}

// Import 文档导入（JSON 文本身 / multipart 文件身）
func (h *ImportHandler) Import(c *gin.Context) {
	actor, ok := h.base.adminActor(c)
	if !ok {
		return
	}

	in, failMessage := parseImportRequest(c)
	if failMessage != "" {
		httpx.Fail(c, httpx.WithMessage(failMessage))
		return
	}

	res, err := h.base.svc.ImportDocument(c.Request.Context(), actor, in)
	if err != nil {
		failService(c, "导入文档失败", err)
		return
	}
	httpx.Success(c, httpx.WithData(gin.H{
		"document_id": res.DocumentID,
		"created":     res.Created,
		"job_id":      res.JobID,
		"job_key":     res.JobKey,
		"created_job": res.CreatedJob,
		"build_state": res.BuildState,
	}))
}

// parseImportRequest 解析入参（两种形态合一）；返回 (入参, 失败文案)；失败文案非空 = 参数不合法
func parseImportRequest(c *gin.Context) (svcKB.ImportInput, string) {
	var in svcKB.ImportInput
	contentType := c.ContentType()

	if strings.Contains(contentType, "multipart/form-data") {
		libraryID, err := strconv.ParseUint(strings.TrimSpace(c.PostForm("library_id")), 10, 64)
		if err != nil || libraryID == 0 {
			return in, "缺少或非法的 library_id"
		}
		in.LibraryID = uint(libraryID)
		in.Name = strings.TrimSpace(c.PostForm("name"))

		fileHeader, err := c.FormFile("file")
		if err != nil {
			return in, "缺少文件（file 字段）: " + err.Error()
		}
		if fileHeader.Size > maxImportBytes {
			return in, "文件超过单次导入上限（" + strconv.FormatInt(maxImportBytes, 10) + " 字节）"
		}
		data, err := readFormFile(fileHeader)
		if err != nil {
			return in, "读取上传文件失败: " + err.Error()
		}
		in.Data = data
		in.FileName = strings.TrimSpace(fileHeader.Filename)
		in.MIME = strings.TrimSpace(fileHeader.Header.Get("Content-Type"))
		return in, ""
	}

	body, err := decodeRawBody(c)
	if err != nil {
		return in, "参数错误: " + err.Error()
	}
	libraryID, err := rawUint(body, "library_id")
	if err != nil {
		return in, "library_id 字段类型错误: " + err.Error()
	}
	if libraryID == 0 {
		return in, "缺少 library_id（导入必须指定目标库）"
	}
	name, err := rawTrimmedString(body, "name")
	if err != nil {
		return in, "name 字段类型错误: " + err.Error()
	}
	text, err := rawTrimmedString(body, "text")
	if err != nil {
		return in, "text 字段类型错误: " + err.Error()
	}
	in.LibraryID, in.Name, in.Text = libraryID, name, text
	return in, ""
}

// readFormFile 读取上传文件字节（带上限；超限即错，不截断）
func readFormFile(header *multipart.FileHeader) ([]byte, error) {
	f, err := header.Open()
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, maxImportBytes+1))
}
