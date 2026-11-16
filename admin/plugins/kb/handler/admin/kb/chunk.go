// plugin: kb v1.0.0
// 知识库切片逐字核对入口：GET /admin/kb/chunk/get/:pk。
//
// 节点：kb/chunk/get → 由 AdminPermission OR 回落 kb/chunk/read（read 节点在本插件清单登记）。
// 用途：按 chunk_id 取回**逐字原文**（防幻觉核对；text 为 kb_chunk.text verbatim 列，禁摘要改写），
// text_hash 供取材方复算核对（sha1(原文)）。
// 隔离：同一可读域条件（可读库集 + 文档就绪 + 当前 build）——不可见与不存在同型 ErrDenied（404 同型）。
package kb

import (
	"github.com/ai-go-hub/ai-go-admin/internal/kit/httpx"
	kbSearch "github.com/ai-go-hub/ai-go-admin/plugins/kb/service/kb/search"

	"github.com/gin-gonic/gin"
)

// ChunkGet 切片详情（逐字原文核对）
func (h *Handler) ChunkGet(c *gin.Context) {
	pk, ok := parsePK(c)
	if !ok {
		return
	}
	view, ok := h.adminView(c)
	if !ok {
		return
	}
	row, err := kbSearch.NewService(h.svc).GetChunk(c.Request.Context(), view, pk)
	if err != nil {
		failService(c, "查询切片失败", err)
		return
	}
	httpx.Success(c, httpx.WithData(gin.H{"row": row}))
}
