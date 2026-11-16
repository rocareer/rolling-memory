package upload

import (
	"bytes"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 请求体上限断言（http.MaxBytesReader 行为，不依赖 DB）：
// 超限截断（含 chunked 无 Content-Length）/ 限内放行 / max_size=0 不设 cap / 余量容忍轻度超限。

// multipartBody 构造单文件 multipart 请求体（附 topic 表单字段，贴近真实上传请求）
func multipartBody(t *testing.T, filename string, content []byte) (*bytes.Buffer, string) {
	t.Helper()

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, err := w.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("构造 multipart 文件段: %v", err)
	}
	if _, err := fw.Write(content); err != nil {
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

// newUploadRequest 构造上传请求（chunked=true 时置 ContentLength=-1 模拟无 Content-Length）
func newUploadRequest(t *testing.T, content []byte, chunked bool) *http.Request {
	t.Helper()

	body, contentType := multipartBody(t, "big.png", content)
	req := httptest.NewRequest(http.MethodPost, "/common/upload", body)
	req.Header.Set("Content-Type", contentType)
	if chunked {
		req.ContentLength = -1
	}
	return req
}

// TestLimitRequestBodyRejectsOversizedBody 超限请求体：解析即失败（不依赖 Content-Length，chunked 同样受控）
func TestLimitRequestBodyRejectsOversizedBody(t *testing.T) {
	for _, chunked := range []bool{false, true} {
		req := newUploadRequest(t, bytes.Repeat([]byte("a"), 2<<20), chunked) // 2MiB ≫ 1KiB + 1MiB 余量
		LimitRequestBody(httptest.NewRecorder(), req, 1024)

		_, _, err := req.FormFile("file")
		if err == nil {
			t.Fatalf("chunked=%v: 超出请求体上限应解析失败", chunked)
		}
		if !strings.Contains(err.Error(), "request body too large") {
			t.Fatalf("chunked=%v: 错误应指出请求体超限，实际: %v", chunked, err)
		}
		if !IsRequestTooLarge(err) {
			t.Fatalf("chunked=%v: IsRequestTooLarge 应识别超限错误，实际: %v", chunked, err)
		}
	}
}

// TestIsRequestTooLarge 只把 MaxBytesReader 超限错误判为 true。
func TestIsRequestTooLarge(t *testing.T) {
	if IsRequestTooLarge(nil) {
		t.Fatal("nil 不应判为超限")
	}
	if IsRequestTooLarge(errors.New("other")) {
		t.Fatal("普通错误不应判为超限")
	}
	if !IsRequestTooLarge(&http.MaxBytesError{Limit: 1}) {
		t.Fatal("MaxBytesError 应判为超限")
	}
}

// TestLimitRequestBodyAllowsWithinCap 限内请求体：解析放行、文件内容完整
func TestLimitRequestBodyAllowsWithinCap(t *testing.T) {
	content := bytes.Repeat([]byte("b"), 4096)
	req := newUploadRequest(t, content, false)
	LimitRequestBody(httptest.NewRecorder(), req, 1024)

	f, fh, err := req.FormFile("file")
	if err != nil {
		t.Fatalf("限内请求体应解析成功: %v", err)
	}
	defer f.Close()
	if fh.Size != int64(len(content)) {
		t.Fatalf("文件大小 = %d，期望 %d", fh.Size, len(content))
	}
	got, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("读取文件内容: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Fatal("解析出的文件内容与原始内容不一致")
	}
}

// TestLimitRequestBodyOverheadAllowance 余量语义：文件本体略超 max_size（在 multipart 开销余量内）仍可解析，
// 文件级精校交由服务层（避免「刚好卡在上限」被请求体粗筛误伤）
func TestLimitRequestBodyOverheadAllowance(t *testing.T) {
	req := newUploadRequest(t, bytes.Repeat([]byte("c"), 1024+512), false)
	LimitRequestBody(httptest.NewRecorder(), req, 1024)

	_, fh, err := req.FormFile("file")
	if err != nil {
		t.Fatalf("余量内（文件本体超 max_size 1024）应可解析: %v", err)
	}
	if fh.Size <= 1024 {
		t.Fatalf("用例构造有误：文件大小 %d 应超过 max_size", fh.Size)
	}
}

// TestLimitRequestBodyDisabledWhenMaxSizeZero max_size=0（配置为不限制）：不设 cap，请求体原样透传
func TestLimitRequestBodyDisabledWhenMaxSizeZero(t *testing.T) {
	content := bytes.Repeat([]byte("d"), 2<<20)
	req := newUploadRequest(t, content, true)
	original := req.Body

	LimitRequestBody(httptest.NewRecorder(), req, 0)

	if req.Body != original {
		t.Fatal("max_size=0 不应包裹请求体（不设 cap）")
	}
	f, fh, err := req.FormFile("file")
	if err != nil {
		t.Fatalf("不设 cap 时应能完整解析: %v", err)
	}
	defer f.Close()
	if fh.Size != int64(len(content)) {
		t.Fatalf("文件大小 = %d，期望 %d", fh.Size, len(content))
	}
}

// TestLimitRequestBodyWrapsWhenCapped max_size>0：请求体被包裹（cap 生效的形态断言）
func TestLimitRequestBodyWrapsWhenCapped(t *testing.T) {
	req := newUploadRequest(t, []byte("small"), false)
	original := req.Body

	LimitRequestBody(httptest.NewRecorder(), req, 1024)

	if req.Body == original {
		t.Fatal("max_size>0 应包裹请求体（http.MaxBytesReader）")
	}
}
