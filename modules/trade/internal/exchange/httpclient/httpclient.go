// Package httpclient 提供交易所适配层共用的 HTTP 工具：带超时的请求执行与 JSON 解析。
package httpclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultTimeout 默认请求超时。
const DefaultTimeout = 15 * time.Second

// Client 封装一个可复用的 *http.Client。
type Client struct {
	HTTP    *http.Client
	BaseURL string
}

// New 创建客户端。
func New(baseURL string) *Client {
	return &Client{HTTP: &http.Client{Timeout: DefaultTimeout}, BaseURL: strings.TrimRight(baseURL, "/")}
}

// Request 描述一次 REST 调用。
type Request struct {
	Method  string
	Path    string
	Query   url.Values
	Body    []byte
	Headers map[string]string
	Timeout time.Duration
}

// Do 执行请求，返回响应体。非 2xx 视为错误并返回交易所原始 body 便于诊断。
func (c *Client) Do(ctx context.Context, req *Request) ([]byte, error) {
	if req.Method == "" {
		req.Method = http.MethodGet
	}
	full := c.BaseURL + req.Path
	if len(req.Query) > 0 {
		full += "?" + req.Query.Encode()
	}
	var bodyReader io.Reader
	if len(req.Body) > 0 {
		bodyReader = bytes.NewReader(req.Body)
	}
	httpReq, err := http.NewRequestWithContext(ctx, req.Method, full, bodyReader)
	if err != nil {
		return nil, err
	}
	for k, v := range req.Headers {
		httpReq.Header.Set(k, v)
	}
	if len(req.Body) > 0 && httpReq.Header.Get("Content-Type") == "" {
		httpReq.Header.Set("Content-Type", "application/json")
	}
	timeout := req.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	httpReq = httpReq.WithContext(cctx)

	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return raw, fmt.Errorf("http %d: %s", resp.StatusCode, truncate(raw, 512))
	}
	return raw, nil
}

// DecodeJSON 把 raw 解析到 v。
func DecodeJSON(raw []byte, v interface{}) error {
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, v)
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "..."
}
