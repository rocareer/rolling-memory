// plugin: kb v1.0.0
// 知识库检索探针（后台内测面）：POST /admin/kb/search。
//
// 节点：kb/search（末段非 list/get，无 read 回落——节点名即 checkPath；节点见本插件清单）。
// 轴：后台面按 **admin 轴** 解析读视图（admins.id；节点授权为 HTTP 侧前置），与其余后台端点同口径。
// 语义：mode auto|lexical|vector|hybrid；top_k 缺省 8 / 硬上限 50（超出截断并**回显生效值**）；
// limit（腿窗，浏览面）≤100 同回显；单腿失败只降该腿并在 degraded[] 点名；可读集为空 = 空集回执。
// 标签过滤：tags:[id…] 可选——服务层已支持（kbSearch.Options.TagIDs，每条腿 WHERE
// 内下推标签子查询，禁应用层后过滤）；本面严格透传（非数组/非正整数/超 50 项响亮拒绝，不静默降级）。
// 「不存在」与「无权」同型：failService 把 ErrDenied 全族压成唯一文案（不泄露存在性）。
package kb

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/ai-go-hub/ai-go-admin/internal/kit/httpx"
	kbSearch "github.com/ai-go-hub/ai-go-admin/plugins/kb/service/kb/search"

	"github.com/gin-gonic/gin"
)

// maxTagFilter 探针 tags 过滤项上限（与桥 knowledge.search.tags 同口径：正整数组 ≤50；防无界多值 IN；
// 超限显式报错，不截断不静默——严格解析 D2）
const maxTagFilter = 50

// rawTagIDs 裸值 → 标签过滤集（tags: [id…]；缺省 / null / 空数组 = nil = 不过滤）。
// 严格解析：非数组 / 元素非正整数 / 超上限一律响亮拒绝（静默回退不过滤会让过滤「看起来在工作」，属假行为）。
func rawTagIDs(body map[string]json.RawMessage, key string) ([]uint, error) {
	raw, ok := body[key]
	if !ok || isJSONNull(raw) {
		return nil, nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, errors.New("须为正整数数组")
	}
	if len(items) == 0 {
		return nil, nil
	}
	if len(items) > maxTagFilter {
		return nil, fmt.Errorf("标签项超上限（%d > %d）", len(items), maxTagFilter)
	}
	ids := make([]uint, 0, len(items))
	for _, item := range items {
		id, err := rawUint(map[string]json.RawMessage{"tag": item}, "tag")
		if err != nil || id == 0 {
			return nil, errors.New("数组项须为正整数")
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// SearchProbe 检索探针（请求体：{query, mode?, top_k?, limit?, library_id?, tags?}）
func (h *Handler) SearchProbe(c *gin.Context) {
	body, err := decodeRawBody(c)
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("参数错误: "+err.Error()))
		return
	}
	query, err := rawTrimmedString(body, "query")
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("query 字段类型错误: "+err.Error()))
		return
	}
	mode, err := rawTrimmedString(body, "mode")
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("mode 字段类型错误: "+err.Error()))
		return
	}
	topK, err := rawUint(body, "top_k")
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("top_k 字段类型错误: "+err.Error()))
		return
	}
	limit, err := rawUint(body, "limit")
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("limit 字段类型错误: "+err.Error()))
		return
	}
	libraryID, err := rawUint(body, "library_id")
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("library_id 字段类型错误: "+err.Error()))
		return
	}
	tagIDs, err := rawTagIDs(body, "tags")
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("tags 字段类型错误: "+err.Error()))
		return
	}
	if query == "" {
		httpx.Fail(c, httpx.WithMessage("缺少检索词（query）"))
		return
	}
	view, ok := h.adminView(c)
	if !ok {
		return
	}
	res, err := kbSearch.NewService(h.svc).Search(c.Request.Context(), view, kbSearch.Options{
		Query:     query,
		Mode:      mode,
		TopK:      int(topK),
		Limit:     int(limit),
		LibraryID: libraryID,
		TagIDs:    tagIDs, // 标签过滤下推（服务层单点：每条腿 WHERE 内子查询；nil = 不过滤）
	})
	if err != nil {
		failService(c, "检索失败", err)
		return
	}
	appliedTags := make([]uint, 0, len(tagIDs))
	appliedTags = append(appliedTags, tagIDs...)
	httpx.Success(c, httpx.WithData(gin.H{
		"list":               res.Items,
		"total":              len(res.Items),
		"mode":               res.Mode,
		"top_k":              res.TopK,
		"limit":              res.Limit,
		"degraded":           res.Degraded,
		"legs":               res.Legs,
		"readable_libraries": res.ReadableLibraries,
		"tag_ids":            appliedTags,
	}))
}
