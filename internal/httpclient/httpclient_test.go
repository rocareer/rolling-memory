// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package httpclient

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rocareer/rolling-memory/internal/version"
)

// These tests pin, in one place, the behavior the four private copies shared
// before they were unified here: extractor/http.go,
// sync/http.go, llm/llm.go and the soft embeddings leg of store/embed.go. The
// retry set and backoff, the request headers/body encoding, the response
// shapes, the error text and the typed status are what every leg relied on.

func TestUserAgentAndResponseCapMatchVersion(t *testing.T) {
	if UserAgent != "rolling-memory/"+version.Version {
		t.Fatalf("UserAgent = %q", UserAgent)
	}
	if MaxResponseBytes != 32*1024*1024 {
		t.Fatalf("MaxResponseBytes = %d", MaxResponseBytes)
	}
}

func TestPostJSONSendsPythonStyleBody(t *testing.T) {
	var (
		gotMethod string
		gotBody   string
		gotHeader http.Header
	)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		gotMethod = request.Method
		gotHeader = request.Header.Clone()
		raw, _ := io.ReadAll(request.Body)
		gotBody = string(raw)
		_, _ = writer.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	response, err := PostJSON(server.URL, map[string]any{"input": []any{"猫", "a<b", 7}}, map[string]string{
		"Authorization": "Bearer t",
		"Content-Type":  "application/x-custom",
	}, time.Second, 0)
	if err != nil {
		t.Fatalf("PostJSON: %v", err)
	}
	if response["ok"] != true {
		t.Fatalf("response = %v", response)
	}
	if gotMethod != http.MethodPost {
		t.Fatalf("method = %q", gotMethod)
	}
	// json.dumps(payload, ensure_ascii=False): default separators, raw non-ASCII
	// and no HTML escaping.
	if gotBody != `{"input": ["猫", "a<b", 7]}` {
		t.Fatalf("body = %q", gotBody)
	}
	if gotHeader.Get("Accept") != "application/json" || gotHeader.Get("User-Agent") != UserAgent {
		t.Fatalf("headers = %v", gotHeader)
	}
	if gotHeader.Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatalf("content type = %q", gotHeader.Get("Content-Type"))
	}
	if gotHeader.Get("Authorization") != "Bearer t" {
		t.Fatalf("authorization = %q", gotHeader.Get("Authorization"))
	}
}

func TestGetJSONSendsNoBody(t *testing.T) {
	var (
		gotMethod string
		gotLength int64
		gotType   string
		gotAuth   string
	)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		gotMethod = request.Method
		gotLength = request.ContentLength
		gotType = request.Header.Get("Content-Type")
		gotAuth = request.Header.Get("Authorization")
		_, _ = writer.Write([]byte(`[1, 2]`))
	}))
	defer server.Close()

	response, err := GetJSON(server.URL, Bearer("tok"), time.Second, 0)
	if err != nil {
		t.Fatalf("GetJSON: %v", err)
	}
	if gotMethod != http.MethodGet || gotLength != 0 || gotType != "" {
		t.Fatalf("request = %s %d %q", gotMethod, gotLength, gotType)
	}
	if gotAuth != "Bearer tok" {
		t.Fatalf("authorization = %q", gotAuth)
	}
	data, ok := response["data"].([]any)
	if !ok || len(data) != 2 || data[0] != float64(1) {
		t.Fatalf("response = %v", response)
	}
}

func TestPostJSONRetriesRetryableStatuses(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		if requests == 1 {
			writer.WriteHeader(http.StatusServiceUnavailable)
			_, _ = writer.Write([]byte(`{"error":"busy"}`))
			return
		}
		_, _ = writer.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	response, err := PostJSON(server.URL, map[string]any{"a": 1}, nil, time.Second, 1)
	if err != nil {
		t.Fatalf("PostJSON: %v", err)
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
	if response["ok"] != true {
		t.Fatalf("response = %v", response)
	}
}

func TestPostJSONAttemptsHonourRetries(t *testing.T) {
	for _, testCase := range []struct {
		retries int
		want    int
	}{{0, 1}, {1, 2}} {
		requests := 0
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			requests++
			writer.WriteHeader(http.StatusInternalServerError)
			_, _ = writer.Write([]byte(`{"error":"boom"}`))
		}))
		_, err := PostJSON(server.URL, map[string]any{"a": 1}, nil, time.Second, testCase.retries)
		server.Close()
		if requests != testCase.want {
			t.Fatalf("retries = %d: requests = %d, want %d", testCase.retries, requests, testCase.want)
		}
		var httpErr *HTTPError
		if !errors.As(err, &httpErr) || httpErr.Status != http.StatusInternalServerError {
			t.Fatalf("error = %v (%T), want HTTPError 500", err, err)
		}
		if httpErr.Msg != fmt.Sprintf("HTTP 500 from %s", URLHostSafe(server.URL)) {
			t.Fatalf("message = %q", httpErr.Msg)
		}
	}
}

func TestPostJSONDoesNotRetryOtherClientErrors(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		writer.WriteHeader(http.StatusBadRequest)
		_, _ = writer.Write([]byte(`{"error":"response_format unsupported","key":"sk-ABCDEFGH12345"}`))
	}))
	defer server.Close()

	_, err := PostJSON(server.URL, map[string]any{"a": 1}, nil, time.Second, 1)
	if requests != 1 {
		t.Fatalf("requests = %d, want 1 (400 is not retryable)", requests)
	}
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr.Status != http.StatusBadRequest {
		t.Fatalf("error = %v (%T), want HTTPError 400", err, err)
	}
	// The llm and extractor legs read Status/Body to decide their one
	// response_format fallback; the body is redacted.
	if !strings.Contains(strings.ToLower(httpErr.Body), "response_format") {
		t.Fatalf("body = %q, want the fallback marker", httpErr.Body)
	}
	if strings.Contains(httpErr.Body, "sk-ABCDEFGH12345") || !strings.Contains(httpErr.Body, "***") {
		t.Fatalf("body = %q, want redacted secrets", httpErr.Body)
	}
	if httpErr.Error() != fmt.Sprintf("HTTP 400 from %s", URLHostSafe(server.URL)) {
		t.Fatalf("Error() = %q", httpErr.Error())
	}
}

func TestErrorBodyIsCappedAt4096Bytes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusBadGateway)
		_, _ = writer.Write([]byte(strings.Repeat("x", 5000) + "TAIL"))
	}))
	defer server.Close()

	_, err := PostJSON(server.URL, map[string]any{"a": 1}, nil, time.Second, 0)
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("error = %v (%T), want HTTPError", err, err)
	}
	if len(httpErr.Body) != 4096 || strings.Contains(httpErr.Body, "TAIL") {
		t.Fatalf("body length = %d, want the first 4096 bytes", len(httpErr.Body))
	}
}

func TestBackoffMirrorsPython(t *testing.T) {
	want := []time.Duration{500 * time.Millisecond, time.Second, 2 * time.Second, 2 * time.Second, 2 * time.Second}
	for attempt, expected := range want {
		if got := backoff(attempt); got != expected {
			t.Fatalf("backoff(%d) = %v, want %v", attempt, got, expected)
		}
	}
}

func TestResponseShapes(t *testing.T) {
	for _, testCase := range []struct {
		name string
		body string
		want map[string]any
	}{
		{"empty body", "", map[string]any{}},
		{"blank body", "  \n\t ", map[string]any{}},
		{"json object", `{"a":1}`, map[string]any{"a": float64(1)}},
		{"non-dict list", `[1]`, map[string]any{"data": []any{float64(1)}}},
		{"non-dict null", `null`, map[string]any{"data": nil}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				_, _ = writer.Write([]byte(testCase.body))
			}))
			defer server.Close()

			response, err := GetJSON(server.URL, nil, time.Second, 0)
			if err != nil {
				t.Fatalf("GetJSON: %v", err)
			}
			raw, err := json.Marshal(response)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			expected, err := json.Marshal(testCase.want)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if string(raw) != string(expected) {
				t.Fatalf("response = %s, want %s", raw, expected)
			}
		})
	}
}

func TestResponseTooLarge(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write([]byte(strings.Repeat("a", MaxResponseBytes+1)))
	}))
	defer server.Close()

	_, err := GetJSON(server.URL, nil, 30*time.Second, 0)
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr.Msg != "response too large" || httpErr.Status != http.StatusOK {
		t.Fatalf("error = %v (%T), want HTTPError 'response too large'", err, err)
	}
}

func TestInvalidJSONResponse(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"plain text", "not json", "not json"},
		{"runes capped at 400", strings.Repeat("无", 500), strings.Repeat("无", 400)},
		{"invalid utf-8 becomes the replacement rune", "\xff", "\uFFFD"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				_, _ = writer.Write([]byte(testCase.body))
			}))
			defer server.Close()

			_, err := GetJSON(server.URL, nil, 30*time.Second, 0)
			var httpErr *HTTPError
			if !errors.As(err, &httpErr) || httpErr.Msg != "response is not JSON" {
				t.Fatalf("error = %v (%T), want HTTPError 'response is not JSON'", err, err)
			}
			if httpErr.Body != testCase.want {
				t.Fatalf("body = %q, want %q", httpErr.Body, testCase.want)
			}
		})
	}
}

func TestNetworkErrorIsRetriedAndWrapped(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		connection, _, err := writer.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		connection.Close()
	}))
	defer server.Close()

	_, err := PostJSON(server.URL, map[string]any{"a": 1}, nil, time.Second, 1)
	if got := requests.Load(); got != 2 {
		t.Fatalf("requests = %d, want 2", got)
	}
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("error = %v (%T), want HTTPError", err, err)
	}
	if !strings.HasPrefix(httpErr.Msg, fmt.Sprintf("network error reaching %s: ", URLHostSafe(server.URL))) {
		t.Fatalf("message = %q", httpErr.Msg)
	}
	if httpErr.Status != 0 {
		t.Fatalf("status = %d, want 0", httpErr.Status)
	}
}

func TestEncodingFailureIsReturnedAsIs(t *testing.T) {
	_, err := PostJSON("http://127.0.0.1:1/", map[string]any{"bad": make(chan int)}, nil, time.Second, 0)
	var httpErr *HTTPError
	if err == nil || errors.As(err, &httpErr) {
		t.Fatalf("error = %v (%T), want the encoder error", err, err)
	}
	if !strings.Contains(err.Error(), "not JSON serializable") {
		t.Fatalf("error = %q", err.Error())
	}
}

func TestBodyReadFailureIsNotRetried(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		connection, _, err := writer.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		// Promise 100 bytes and send 5: the read fails mid-body and must not
		// be retried.
		_, _ = connection.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 100\r\n\r\nshort"))
		connection.Close()
	}))
	defer server.Close()

	_, err := PostJSON(server.URL, map[string]any{"a": 1}, nil, time.Second, 1)
	if requests != 1 {
		t.Fatalf("requests = %d, want 1 (body read failures are not retried)", requests)
	}
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr.Status != http.StatusOK {
		t.Fatalf("error = %v (%T), want HTTPError 200", err, err)
	}
	if !strings.HasPrefix(httpErr.Msg, fmt.Sprintf("network error reaching %s: ", URLHostSafe(server.URL))) {
		t.Fatalf("message = %q", httpErr.Msg)
	}
}

func TestBearerAndURLHostSafe(t *testing.T) {
	if headers := Bearer(""); len(headers) != 0 {
		t.Fatalf("Bearer(\"\") = %v", headers)
	}
	if headers := Bearer("tok"); headers["Authorization"] != "Bearer tok" {
		t.Fatalf("Bearer(\"tok\") = %v", headers)
	}
	for _, testCase := range []struct {
		raw  string
		want string
	}{
		{"https://EXAMPLE.com:8443/v1/memories", "example.com"},
		{"http://127.0.0.1:8080/", "127.0.0.1"},
		{"http://[::1]:11434/api/chat", "::1"},
		{"not a url", "remote"},
		{"", "remote"},
	} {
		if got := URLHostSafe(testCase.raw); got != testCase.want {
			t.Fatalf("URLHostSafe(%q) = %q, want %q", testCase.raw, got, testCase.want)
		}
	}
}
