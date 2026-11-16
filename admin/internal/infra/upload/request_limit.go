package upload

import (
	"errors"
	"net/http"
)

// MaxUploadOverhead multipart 解析开销余量（boundary、表单字段、分段头部）：
// 请求体硬上限 = max_size + 本余量，避免「文件本体刚好卡在上限、整体 body 略超」被误伤。
const MaxUploadOverhead = 1 << 20

// LimitRequestBody 给上传请求套上请求体硬上限（http.MaxBytesReader）。
//
// 语义：
//   - 上限 = max_size + MaxUploadOverhead；max_size <= 0（配置为不限制）时不设 cap；
//   - 按 body 实际字节数封顶、不依赖 Content-Length：chunked（无 Content-Length）同样受控；
//   - 必须在任何表单 / multipart 解析之前调用（FormFile / PostForm 一旦触发解析即整体读 body）。
//
// 超限时 body 读取返回 "http: request body too large"，由解析调用方按普通错误上报；
// 本函数不返回错误：请求体上限是粗筛兜底，文件级 max_size 精校仍在服务层做。
func LimitRequestBody(w http.ResponseWriter, r *http.Request, maxSize int) {
	if maxSize <= 0 || r == nil || r.Body == nil {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, int64(maxSize)+MaxUploadOverhead)
}

// IsRequestTooLarge 判断解析错误是否由请求体超过 LimitRequestBody 上限引起。
// 上层据此返回友好文案，避免把 "http: request body too large" 直接暴露给调用方。
func IsRequestTooLarge(err error) bool {
	var maxErr *http.MaxBytesError
	return errors.As(err, &maxErr)
}
