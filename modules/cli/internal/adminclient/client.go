package adminclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/servicegateway"
)

// Client calls the MooX control HTTP API used by CLI workflows.
type Client struct {
	BaseURL     string
	AccessToken string
	SpaceID     string
	// ServiceAuth 后台服务签名鉴权配置。设置后请求走 /api/service/{service}/{method}
	// 路由并使用 HMAC Auth 头，不再依赖用户登录态 X-Access-Token。
	ServiceAuth *ServiceAuthConfig
	HTTPClient  *http.Client
}

// New creates a Control API client. baseURL should point at the Control service root.
func New(baseURL string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
	}
}

type retInfo struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
}

func isRetInfoSuccess(code int) bool {
	return code == 0 || code == 200
}
func (c *Client) postJSON(ctx context.Context, method, path string, body any) ([]byte, error) {
	if c.BaseURL == "" {
		return nil, fmt.Errorf("control url is required")
	}
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(data)
	}
	// 若配置了后台服务签名鉴权，则改走 /api/service/{service}/{method} 路由，
	// 并对原始请求体做 HMAC 签名放进 Auth 头，不再依赖用户登录态。
	finalPath := path
	var authHeader string
	if c.ServiceAuth != nil {
		finalPath = rewriteToServiceRoute(path)
		rawBody, _ := io.ReadAll(reader)
		reader = bytes.NewReader(rawBody)
		signedHeaders := map[string]string{"X-Space-Id": c.SpaceID}
		header, err := c.ServiceAuth.BuildAuthHeader(method, finalPath, rawBody, signedHeaders, time.Now())
		if err != nil {
			return nil, err
		}
		authHeader = header
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+finalPath, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if authHeader != "" {
		req.Header.Set("Auth", authHeader)
	}
	if c.AccessToken != "" {
		req.Header.Set("X-Access-Token", c.AccessToken)
	}
	if c.SpaceID != "" {
		req.Header.Set("X-Space-Id", c.SpaceID)
	}
	client := c.HTTPClient
	if client == nil {
		if c.ServiceAuth != nil {
			client, err = servicegateway.NewClient(30 * time.Second)
			if err != nil {
				return nil, err
			}
		} else {
			client = http.DefaultClient
		}
	}
	httpResp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer httpResp.Body.Close()
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		return nil, fmt.Errorf("control returned HTTP %s", httpResp.Status)
	}
	raw, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, err
	}
	if trpcRet := httpResp.Header.Get("trpc-ret"); trpcRet != "" && trpcRet != "0" {
		msg := httpResp.Header.Get("trpc-func-ret")
		if msg == "" {
			msg = string(raw)
		}
		return nil, fmt.Errorf("control returned trpc-ret=%s: %s", trpcRet, msg)
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("control returned empty response body")
	}
	return raw, nil
}

// rewriteToServiceRoute 将 /api/admin/{service}/{method} 改写为 /api/service/{service}/{method}。
// 仅识别 /api/admin/ 前缀；非该前缀的路径原样返回。
func rewriteToServiceRoute(path string) string {
	const controlPrefix = "/api/admin/"
	if strings.HasPrefix(path, controlPrefix) {
		return "/api/service/" + strings.TrimPrefix(path, controlPrefix)
	}
	return path
}
