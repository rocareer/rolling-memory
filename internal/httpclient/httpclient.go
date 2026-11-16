// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

// Package httpclient is the one JSON-over-HTTP client used by the cloud legs
// (llm, extractor, sync, remote embeddings).
//
// It has zero third-party dependencies and no mode knowledge — every caller
// must run Config.CheckEgress before invoking it.
package httpclient

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/rocareer/rolling-memory/internal/ids"
	"github.com/rocareer/rolling-memory/internal/version"
)

// MaxResponseBytes caps one response body (32 MiB to accommodate the 30 MiB
// attach_files inline payload budget on /open/memory/search).
const MaxResponseBytes = 32 * 1024 * 1024

// UserAgent identifies the client to remote endpoints.
const UserAgent = "rolling-memory/" + version.Version

// defaultTimeout is the default HTTP client timeout.
const defaultTimeout = 30 * time.Second

// HTTPError is one transport or HTTP-status failure. The
// extractor and llm legs inspect Status/Body to decide the one response_format
// fallback.
type HTTPError struct {
	Status int
	Body   string
	Msg    string
}

// Error returns the transport message.
func (e *HTTPError) Error() string { return e.Msg }

// PostJSON posts one JSON body and decodes the JSON object reply, retrying
// transient transport failures up to the retry budget.
func PostJSON(url string, payload any, headers map[string]string, timeout time.Duration, retries int) (map[string]any, error) {
	return request(http.MethodPost, url, payload, headers, timeout, retries)
}

// GetJSON fetches one JSON object reply.
func GetJSON(url string, headers map[string]string, timeout time.Duration, retries int) (map[string]any, error) {
	return request(http.MethodGet, url, nil, headers, timeout, retries)
}

// Bearer returns the Authorization header for token; an empty token adds no
// header.
func Bearer(token string) map[string]string {
	headers := map[string]string{}
	if token != "" {
		headers["Authorization"] = "Bearer " + token
	}
	return headers
}

// URLHostSafe returns the host used in error messages, or "remote" when the
// URL carries no usable host.
func URLHostSafe(raw string) string {
	host, ok := ids.URLHost(raw)
	if !ok || host == "" {
		return "remote"
	}
	return host
}

// request mirrors http._request: retries+1 attempts, retrying only 429/500/502/
// 503/504 statuses and network errors, sleeping min(2.0, 0.5*2**attempt)
// seconds between attempts.
func request(method, url string, payload any, headers map[string]string, timeout time.Duration, retries int) (map[string]any, error) {
	var body []byte
	requestHeaders := map[string]string{
		"Accept":     "application/json",
		"User-Agent": UserAgent,
	}
	for name, value := range headers {
		requestHeaders[name] = value
	}
	if payload != nil {
		encoded, err := ids.PyJSONDump(payload, false, false, false)
		if err != nil {
			return nil, err
		}
		body = []byte(encoded)
		requestHeaders["Content-Type"] = "application/json; charset=utf-8"
	}
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	client := &http.Client{Timeout: timeout}

	attempts := retries
	if attempts < 0 {
		attempts = 0
	}
	attempts++
	var last error
	for attempt := 0; attempt < attempts; attempt++ {
		var reader io.Reader
		if body != nil {
			reader = bytes.NewReader(body)
		}
		request, err := http.NewRequest(method, url, reader)
		if err != nil {
			return nil, err
		}
		for name, value := range requestHeaders {
			request.Header.Set(name, value)
		}
		response, err := client.Do(request)
		if err != nil {
			last = &HTTPError{Msg: fmt.Sprintf("network error reaching %s: %v", URLHostSafe(url), err)}
			if attempt == attempts-1 {
				return nil, last
			}
			time.Sleep(backoff(attempt))
			continue
		}
		if response.StatusCode >= 400 {
			raw, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
			response.Body.Close()
			last = &HTTPError{
				Status: response.StatusCode,
				Body:   ids.Redact(string(raw)),
				Msg:    fmt.Sprintf("HTTP %d from %s", response.StatusCode, URLHostSafe(url)),
			}
			if !retryableStatus(response.StatusCode) || attempt == attempts-1 {
				return nil, last
			}
			time.Sleep(backoff(attempt))
			continue
		}
		raw, readErr := io.ReadAll(io.LimitReader(response.Body, MaxResponseBytes+1))
		response.Body.Close()
		if readErr != nil {
			return nil, &HTTPError{
				Status: response.StatusCode,
				Msg:    fmt.Sprintf("network error reaching %s: %v", URLHostSafe(url), readErr),
			}
		}
		if len(raw) > MaxResponseBytes {
			return nil, &HTTPError{Status: response.StatusCode, Msg: "response too large"}
		}
		text := strings.ToValidUTF8(string(raw), "\uFFFD")
		if strings.TrimSpace(text) == "" {
			return map[string]any{}, nil
		}
		var parsed any
		if err := json.Unmarshal([]byte(text), &parsed); err != nil {
			return nil, &HTTPError{
				Status: response.StatusCode,
				Body:   firstRunes(text, 400),
				Msg:    "response is not JSON",
			}
		}
		if object, ok := parsed.(map[string]any); ok {
			return object, nil
		}
		return map[string]any{"data": parsed}, nil
	}
	if last != nil {
		return nil, last
	}
	return nil, &HTTPError{Msg: "request failed"}
}

// retryableStatus reports the HTTP status codes eligible for automatic retry.
func retryableStatus(status int) bool {
	switch status {
	case 429, 500, 502, 503, 504:
		return true
	}
	return false
}

// backoff mirrors min(2.0, 0.5 * 2**attempt) seconds.
func backoff(attempt int) time.Duration {
	seconds := 0.5
	for index := 0; index < attempt; index++ {
		seconds *= 2
	}
	if seconds > 2.0 {
		seconds = 2.0
	}
	return time.Duration(seconds * float64(time.Second))
}

// firstRunes caps value at limit runes for error bodies.
func firstRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}
