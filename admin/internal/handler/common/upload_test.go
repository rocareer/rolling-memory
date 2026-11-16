package common

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// 未认证上传不得触发 multipart 解析：鉴权判定必须先于任何表单 / multipart 读取。
// 路由挂了 AdminAuthOptional（未登录也进控制器），故控制器内的身份判定是唯一闸门；
// 若解析先发生，未认证请求同样会让服务端落临时文件、按字段上限占用内存与磁盘。

func multipartUploadBody(t *testing.T) (*bytes.Buffer, string) {
	t.Helper()

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, err := w.CreateFormFile("file", "probe.txt")
	if err != nil {
		t.Fatalf("构造 multipart 文件段: %v", err)
	}
	if _, err := fw.Write(bytes.Repeat([]byte("p"), 4096)); err != nil {
		t.Fatalf("写入文件段: %v", err)
	}
	if err := w.WriteField("topic", "avatar"); err != nil {
		t.Fatalf("写入表单字段: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("关闭 multipart: %v", err)
	}
	return &buf, w.FormDataContentType()
}

func TestUploadUnauthenticatedSkipsMultipartParse(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// 未认证路径不会用到上传服务，注入 nil 即可（一旦用到会 panic，正好暴露顺序问题）
	engine := gin.New()
	engine.POST("/common/upload", NewUploadHandler(nil).Upload)

	body, contentType := multipartUploadBody(t)
	req := httptest.NewRequest(http.MethodPost, "/common/upload", body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if req.MultipartForm != nil {
		t.Fatal("未认证请求触发了 multipart 解析（身份判定未先于 body 读取）")
	}
	if rec.Body.Len() == 0 {
		t.Fatal("未认证请求应有响应体（业务码 + 文案）")
	}
}
