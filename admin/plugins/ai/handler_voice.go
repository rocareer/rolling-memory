// plugin: ai v0.2.0
// handler_voice.go 是语音（TTS）的管理面与公开面端点：
//
//	POST /voice/list          列表（分页 + 快搜 + 顶层 status/model + wheres 高级检索）
//	GET  /voice/get/:pk       单行回填
//	POST /voice/create        新增（name/model/voice 必填、format 闭集收敛）
//	POST /voice/update/:pk    保存（只带出现过的字段）
//	POST /voice/delete        批量删除
//	POST /voice/preview       试听：读音色行 → 真实合成 → **音频流直出**（服务端不落盘）
//	POST /ai/v1/audio/speech  公开面语音合成（OpenAI 形状；同一鉴权与限流链）
//
// 【装配面】接线清单（本包不改 plugin.go / plugin.yaml）：
//   - adminRoutes 增 6 条，节点逐字为：/voice/list 与 /voice/get/:pk → plugin/ai/voice/read、
//     /voice/create → plugin/ai/voice/create、/voice/update/:pk → plugin/ai/voice/update、
//     /voice/delete → plugin/ai/voice/delete、/voice/preview → plugin/ai/voice/preview
//     （menu 节点清单：plugin/ai/voice/{read,create,update,delete,preview}）；
//   - publicRoutes 增 {http.MethodPost, "/ai/v1/audio/speech", d.publicSpeech}（经同一 bearerAuth 链）；
//   - 装配期把 dashscope_tts 注册进驱动注册表：名 DashScopeTTSDriverName、
//     能力 ["speech"]、工厂 NewDashScopeTTSDriver(set.AllowPrivateHosts)；注册表对象还需实现
//     service_tts.go 的 SpeechDriver（按 ch.Driver 分派），否则语音路由会以「驱动不支持语音合成」失败。
//
// 出口纪律：
//   - 音频只在内存与响应里，**服务端不落盘**（试听与公开面共用 writeSpeechAudio）；
//   - 公开面不把上游音频地址（OSS 链接）暴露给调用方：driver 已把 URL 取回成字节，统一按字节出口；
//   - source 口径：公开面经 keySourceOf(c) 取计量归因（未命中 DB Key 时即缺省 publicSource
//     'key:gateway'），与 chat/embeddings 一致——接线时已落地；
//   - 白名单口径：公开面经 modelInKeyScope(c, model) 做准入（未设置 scope ⇒ 恒放行；
//     设置了 ⇒ 白名单外回 404 model_not_found），与 chat/embeddings 同一判定与同一回执形状。
package ai

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/ai-go-hub/ai-go-admin/internal/kit/httpx"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// voiceWhereFields 是音色表的 wheres 字段白名单（列名是已引用常量；音色行没有密钥类字段，全列可筛）。
var voiceWhereFields = map[string]whereField{
	"id":          {Column: `"id"`, Kind: whereInt},
	"name":        {Column: `"name"`, Kind: whereText},
	"model":       {Column: `"model"`, Kind: whereText},
	"voice":       {Column: `"voice"`, Kind: whereText},
	"format":      {Column: `"format"`, Kind: whereText},
	"status":      {Column: `"status"`, Kind: whereBool01},
	"remark":      {Column: `"remark"`, Kind: whereText},
	"create_time": {Column: `"create_time"`, Kind: whereInt},
	"update_time": {Column: `"update_time"`, Kind: whereInt},
}

// voicePreviewDefaultText 是试听的默认文本（请求里没带 text 时用）。
//
// 源侧这一版没有音色管理页可对照（radmin_ai_voice 迁移只做闭集扩展），默认文本属本插件自定。
const voicePreviewDefaultText = "你好，这是语音合成试听。"

// 试听的调用上下文（usage 的 biz_type/source 维度；只判空、不做闭集校验）。
const (
	voicePreviewBizType = "preview"
	voicePreviewSource  = "admin:voice"
)

// ===== 列表 =====

// voiceListRequest 音色列表入参：宿主分页契约 + 顶层等值筛选 status/model。
type voiceListRequest struct {
	listRequest
	Status optionalStatus `json:"status"`
	Model  string         `json:"model"`
}

// filter 把请求收敛成 store 的筛选结构 + 已校验的 wheres 组。
func (r voiceListRequest) filter() (VoiceFilter, []whereGroup, error) {
	if r.Status.Value != nil && !validStatus(*r.Status.Value) {
		return VoiceFilter{}, nil, errors.New("status 只能是 0（停用）或 1（启用）")
	}
	if err := validateSort(r.Sort, voiceSortColumns, "音色"); err != nil {
		return VoiceFilter{}, nil, err
	}
	groups, err := validateWhereGroups(voiceWhereFields, r.Wheres)
	if err != nil {
		return VoiceFilter{}, nil, err
	}
	return VoiceFilter{
		Page:    r.Page,
		Limit:   r.Limit,
		Sort:    r.Sort,
		Order:   r.Order,
		Keyword: r.keyword(),
		Status:  r.Status.Value,
		Model:   r.Model,
	}, groups, nil
}

// voiceList 音色分页列表。
func (d deps) voiceList(c *gin.Context) {
	store := d.storeOrFail(c)
	if store == nil {
		return
	}
	var req voiceListRequest
	if err := bindJSON(c, &req); err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	filter, groups, err := req.filter()
	if err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	ctx := c.Request.Context()
	var result VoiceListResult
	if len(groups) == 0 {
		result, err = store.ListVoices(ctx, filter)
	} else {
		result, err = store.listVoicesWithWheres(ctx, filter, groups)
	}
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("读取音色列表失败: "+err.Error()))
		return
	}
	httpx.Success(c, httpx.WithData(gin.H{"list": result.Rows, "total": result.Total}))
}

// listVoicesWithWheres 与 Store.ListVoices 同口径（同一基础筛选/排序/分页），只多一层 wheres。
// 条件由接口层拼装（store 的列表原语不接受 wheres），列名与操作符都已过白名单、值全参数化。
func (s *Store) listVoicesWithWheres(ctx context.Context, f VoiceFilter, groups []whereGroup) (VoiceListResult, error) {
	result := VoiceListResult{Rows: []Voice{}}
	if err := s.ready(); err != nil {
		return result, err
	}
	countQuery, err := applyWhereGroups(s.voiceQuery(ctx, f), voiceWhereFields, groups)
	if err != nil {
		return result, err
	}
	if err := countQuery.Count(&result.Total).Error; err != nil {
		return result, err
	}
	listQuery, err := applyWhereGroups(s.voiceQuery(ctx, f), voiceWhereFields, groups)
	if err != nil {
		return result, err
	}
	offset, limit := normalizePage(f.Page, f.Limit, DefaultVoiceListLimit)
	order := orderClause(f.Sort, f.Order, voiceSortColumns, `"update_time" DESC, "id" DESC`)
	if err := listQuery.Order(order).Offset(offset).Limit(limit).Find(&result.Rows).Error; err != nil {
		return result, err
	}
	return result, nil
}

// ===== 单行读取 =====

// voiceGet 单行回填。
func (d deps) voiceGet(c *gin.Context) {
	store := d.storeOrFail(c)
	if store == nil {
		return
	}
	id, err := parsePK(c, "音色")
	if err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	row, err := store.GetVoice(c.Request.Context(), id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		httpx.Fail(c, httpx.WithMessage(fmt.Sprintf("音色 %d 不存在", id)))
		return
	}
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("读取音色失败: "+err.Error()))
		return
	}
	httpx.Success(c, httpx.WithData(gin.H{"row": row}))
}

// ===== 新增 / 保存 =====

// voiceSaveRequest 是音色新增/编辑的入参；指针字段把「缺省」与「显式零值」分开。
type voiceSaveRequest struct {
	Name   *string `json:"name"`
	Model  *string `json:"model"`
	Voice  *string `json:"voice"`
	Format *string `json:"format"`
	Status *int16  `json:"status"`
	Remark *string `json:"remark"`
}

// voiceCreate 新增音色。
//
// 不在此校验「model 指向的逻辑模型必须存在且 type=tts」：与渠道/模型的引用口径一致（表结构不用外键），
// 由调用期的 requireModel 能力门收口（模型不存在 ⇒ 404 model_not_found、类型不符 ⇒ 400 model_type_mismatch）。
func (d deps) voiceCreate(c *gin.Context) {
	store := d.storeOrFail(c)
	if store == nil {
		return
	}
	var req voiceSaveRequest
	if err := bindJSON(c, &req); err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	row, err := req.voice()
	if err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	if err := store.CreateVoice(c.Request.Context(), row); err != nil {
		if isUniqueViolation(err) {
			httpx.Fail(c, httpx.WithMessage(fmt.Sprintf("音色名 %q 已存在（name 唯一）", row.Name)))
			return
		}
		httpx.Fail(c, httpx.WithMessage("新增音色失败: "+err.Error()))
		return
	}
	httpx.Success(c, httpx.WithData(gin.H{"id": row.ID, "name": row.Name}))
}

// voiceUpdate 保存音色：只写出现过的字段。
func (d deps) voiceUpdate(c *gin.Context) {
	store := d.storeOrFail(c)
	if store == nil {
		return
	}
	id, err := parsePK(c, "音色")
	if err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	var req voiceSaveRequest
	if err := bindJSON(c, &req); err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	patch, err := req.patch()
	if err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	ctx := c.Request.Context()
	affected, err := store.UpdateVoice(ctx, id, patch)
	if err != nil {
		if isUniqueViolation(err) {
			httpx.Fail(c, httpx.WithMessage("音色名已存在（name 唯一）"))
			return
		}
		httpx.Fail(c, httpx.WithMessage("保存音色失败: "+err.Error()))
		return
	}
	if affected == 0 && !voiceExists(ctx, store, id) {
		httpx.Fail(c, httpx.WithMessage(fmt.Sprintf("音色 %d 不存在", id)))
		return
	}
	httpx.Success(c, httpx.WithData(gin.H{"id": id, "affected": affected}))
}

// ===== 删除 =====

// voiceDelete 批量删除音色（音色行没有下游引用，直接删）。
func (d deps) voiceDelete(c *gin.Context) {
	store := d.storeOrFail(c)
	if store == nil {
		return
	}
	var req pksRequest
	if err := bindJSON(c, &req); err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	ids, err := req.ids("音色")
	if err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	if err := store.DeleteVoices(c.Request.Context(), ids); err != nil {
		httpx.Fail(c, httpx.WithMessage("删除音色失败: "+err.Error()))
		return
	}
	httpx.Success(c, httpx.WithData(gin.H{"deleted": len(ids)}))
}

// ===== 试听（音频流直出，不落盘）=====

// voicePreviewRequest 试听入参：id 必填；text/format 可覆盖音色行的默认（便于对比试听）。
type voicePreviewRequest struct {
	ID     int64   `json:"id"`
	Text   *string `json:"text"`
	Format *string `json:"format"`
}

// voicePreview 试听：读音色行 → 真实合成 → 音频流直出。
//
// 三条纪律：
//  1. 一次试听 = 一次真实上游尝试（**恰好命中上游一次**，不做本地缓存/预取），因此照常记账
//     （usage 一行 call_type='tts'、biz_type='preview'、source='admin:voice'）；
//  2. 响应就是音频字节（Content-Type 按格式），不落盘、不写临时文件、不返回上游 OSS 链接；
//  3. 失败走后台面回执形状（HTTP 200 + code=1），文案取 *APIError.Detail()（驱动侧已脱敏）。
func (d deps) voicePreview(c *gin.Context) {
	store := d.storeOrFail(c)
	if store == nil {
		return
	}
	var req voicePreviewRequest
	if err := bindJSON(c, &req); err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	if req.ID <= 0 {
		httpx.Fail(c, httpx.WithMessage("音色 ID 必填"))
		return
	}
	ctx := c.Request.Context()
	row, err := store.GetVoice(ctx, req.ID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		httpx.Fail(c, httpx.WithMessage(fmt.Sprintf("音色 %d 不存在", req.ID)))
		return
	}
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("读取音色失败: "+err.Error()))
		return
	}

	// 格式：缺省沿用音色行自己的 format（请求里的 format 只是覆盖）。
	format := row.Format
	if req.Format != nil {
		override, formatErr := normalizeVoiceFormatValue(*req.Format)
		if formatErr != nil {
			httpx.Fail(c, httpx.WithMessage(formatErr.Error()))
			return
		}
		format = override
	}

	service, ok := d.voiceServiceOrFail(c)
	if !ok {
		return
	}
	res, err := service.Speech(ctx, SpeechInput{
		Model:   row.Model,
		Text:    voicePreviewText(req.Text),
		Voice:   row.Voice,
		Format:  format,
		BizType: voicePreviewBizType,
		Source:  voicePreviewSource,
	})
	if err != nil {
		httpx.Fail(c, httpx.WithMessage(previewErrorMessage(err)))
		return
	}
	writeSpeechAudio(c, res)
}

// voicePreviewText 取试听文本：显式 text 优先（空串=未填，按默认），否则用默认文本。
func voicePreviewText(text *string) string {
	if value := strings.TrimSpace(derefString(text)); value != "" {
		return value
	}
	return voicePreviewDefaultText
}

// normalizeVoiceFormatValue 试听/公开面的格式覆盖：空 ⇒ 空（沿用音色行/默认），闭集外响亮拒绝。
func normalizeVoiceFormatValue(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", nil
	}
	format, err := NormalizeVoiceFormat(value)
	if err != nil {
		return "", err
	}
	return format, nil
}

// previewErrorMessage 试听的失败文案：*APIError 带 Detail（含内部原因，管理面可读），其余原样。
func previewErrorMessage(err error) string {
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr != nil {
		return truncateRunes("试听失败: "+apiErr.Detail(), 500)
	}
	return truncateRunes("试听失败: "+err.Error(), 500)
}

// voiceServiceOrFail 取编排单例（后台面形状 fail-closed：HTTP 200 + code=1）。
func (d deps) voiceServiceOrFail(c *gin.Context) (*Service, bool) {
	ctx := c.Request.Context()
	set, err := d.loadSettings(ctx)
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("读取插件配置失败: "+err.Error()))
		return nil, false
	}
	service, err := d.loadService(ctx, set)
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("服务不可用: "+err.Error()))
		return nil, false
	}
	return service, true
}

// ===== 公开面：POST /ai/v1/audio/speech（OpenAI 形状）=====

// publicSpeech 对外语音合成。
//
// 请求（OpenAI 兼容形状，未知键忽略）：
//
//	{"model":"tts-model","input":"要合成的文本","voice":"longxiaochun_v2","response_format":"mp3"}
//
// 回执：**音频字节** + Content-Type（audio/mpeg 等）；错误按公开面 OpenAI 形状（真实 HTTP 状态码）。
// input 与 voice 必填（百炼 cosyvoice 的 voice 必填；缺了与其让上游 400，不如本地给可读错误）。
// 未实现/忽略的字段：speed（OpenAI 的语速参数，本驱动与源都不支持，如实登记在 README 差异节）。
func (d deps) publicSpeech(c *gin.Context) {
	body, apiErr := readPublicJSONObject(c)
	if apiErr != nil {
		writePublicAPIError(c, apiErr)
		return
	}
	model := strings.TrimSpace(asStringValue(body["model"]))
	if model == "" {
		writePublicAPIError(c, invalidRequest("model 不能为空"))
		return
	}
	text := strings.TrimSpace(asStringValue(body["input"]))
	if text == "" {
		writePublicAPIError(c, invalidRequest("input（待合成文本）不能为空"))
		return
	}
	voice := strings.TrimSpace(asStringValue(body["voice"]))
	if voice == "" {
		writePublicAPIError(c, invalidRequest("voice（音色）不能为空"))
		return
	}
	formatValue := asStringValue(body["response_format"])
	if strings.TrimSpace(formatValue) == "" {
		formatValue = asStringValue(body["format"]) // 兼容短键写法
	}
	if _, err := normalizeVoiceFormatValue(formatValue); err != nil {
		writePublicAPIError(c, publicAPIError(err))
		return
	}
	// 白名单准入（口径与 publicChat / publicEmbeddings 逐字一致）：未设置 model_scope ⇒ 恒放行；
	// 设置了 ⇒ 白名单外回 404 + code=model_not_found（与「模型不存在」同形，不向调用方区分二者）。
	// 缺了这一行，带 model_scope 的 Key 就能在语音面使用白名单外的模型（chat/embeddings/models 三条已拦）。
	if !modelInKeyScope(c, model) {
		writePublicAPIError(c, keyScopeModelNotFound(model))
		return
	}

	service, _, ok := d.serviceOrFail(c)
	if !ok {
		return
	}
	res, err := service.Speech(c.Request.Context(), SpeechInput{
		Model:      model,
		Text:       text,
		Voice:      voice,
		Format:     formatValue,
		SampleRate: intValue(body["sample_rate"]),
		BizType:    bizTypeFromBody(body),
		// 计量归因走 source 接缝（未命中 DB Key 时即缺省 publicSource='key:gateway'）：
		// 与 chat/embeddings 同一口径，语音面的用量不再恒记在网关全局 Key 上。
		Source: keySourceOf(c),
	})
	if err != nil {
		writePublicAPIError(c, publicAPIError(err))
		return
	}
	writeSpeechAudio(c, res)
}

// writeSpeechAudio 音频出口（试听与公开面共用）：只写响应，不落盘。
func writeSpeechAudio(c *gin.Context, res *SpeechResult) {
	if res == nil || len(res.Audio) == 0 {
		// 走到这里说明驱动违反了「成功必有音频」的契约：按 503 响亮失败，绝不回一个空音频。
		writePublicAPIError(c, &APIError{
			HTTPStatus: http.StatusServiceUnavailable,
			Type:       "upstream_unavailable",
			Code:       "empty_audio",
			Message:    "上游未返回音频内容",
			Class:      ErrorClassUpstream,
		})
		return
	}
	contentType := strings.TrimSpace(res.ContentType)
	if contentType == "" {
		contentType = speechContentType(res.Format)
	}
	// 试听/合成结果一律不缓存（同一次文本每次都是真实上游调用与真实计费）。
	c.Header("Cache-Control", "no-store")
	c.Data(http.StatusOK, contentType, res.Audio)
}

// intValue 宽容地取整数（题外字段；JSON 数字、数字字符串都接受，其余回落 0）。
func intValue(value any) int {
	switch v := value.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case string:
		number, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return 0
		}
		return number
	default:
		return 0
	}
}

// ===== 入参收敛（纯函数，可单测）=====

// voice 组装新行：必填校验 + 闭集收敛 + 长度截断（create）。
func (r voiceSaveRequest) voice() (*Voice, error) {
	name, err := requiredText(r.Name, "音色名", 50)
	if err != nil {
		return nil, err
	}
	model, err := requiredText(r.Model, "逻辑模型名", 100)
	if err != nil {
		return nil, err
	}
	voice, err := requiredText(r.Voice, "上游音色 ID", 60)
	if err != nil {
		return nil, err
	}
	format, err := NormalizeVoiceFormat(derefString(r.Format))
	if err != nil {
		return nil, err
	}
	status := StatusEnabled
	if r.Status != nil {
		if !validStatus(*r.Status) {
			return nil, errors.New("status 只能是 0（停用）或 1（启用）")
		}
		status = *r.Status
	}
	return &Voice{
		Name:   name,
		Model:  model,
		Voice:  voice,
		Format: format,
		Status: status,
		Remark: truncateRunes(strings.TrimSpace(derefString(r.Remark)), 255),
	}, nil
}

// patch 组装编辑补丁：只带出现过的字段。
func (r voiceSaveRequest) patch() (VoicePatch, error) {
	patch := VoicePatch{}
	if r.Name != nil {
		name, err := requiredText(r.Name, "音色名", 50)
		if err != nil {
			return patch, err
		}
		patch.Name = &name
	}
	if r.Model != nil {
		model, err := requiredText(r.Model, "逻辑模型名", 100)
		if err != nil {
			return patch, err
		}
		patch.Model = &model
	}
	if r.Voice != nil {
		voice, err := requiredText(r.Voice, "上游音色 ID", 60)
		if err != nil {
			return patch, err
		}
		patch.Voice = &voice
	}
	if r.Format != nil {
		format, err := NormalizeVoiceFormat(*r.Format)
		if err != nil {
			return patch, err
		}
		patch.Format = &format
	}
	if r.Status != nil {
		if !validStatus(*r.Status) {
			return patch, errors.New("status 只能是 0（停用）或 1（启用）")
		}
		status := *r.Status
		patch.Status = &status
	}
	if r.Remark != nil {
		remark := truncateRunes(strings.TrimSpace(*r.Remark), 255)
		patch.Remark = &remark
	}
	return patch, nil
}
