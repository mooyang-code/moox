package httpclient

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/dnsproxy"
	"trpc.group/trpc-go/trpc-go/log"
)

// HTTPClient 通用 HTTP 客户端（支持 DNS 优选 + TLS SNI）
type HTTPClient struct {
	httpClient *http.Client
}

const defaultRequestTimeout = 8 * time.Second

// NewHTTPClient 创建通用 HTTP 客户端
func NewHTTPClient() *HTTPClient {
	return &HTTPClient{
		httpClient: &http.Client{
			Timeout: defaultRequestTimeout,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{
					// 跳过证书验证，提升性能
					InsecureSkipVerify: true,
				},
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 10,
				IdleConnTimeout:     90 * time.Second,
			},
		},
	}
}

// Get 发送 GET 请求（自动获取最优 IP）
func (c *HTTPClient) Get(ctx context.Context, domain, path string, query url.Values, result interface{}) error {
	// 尝试获取最优 IP
	bestIP := dnsproxy.GetBestIP(domain)
	return c.GetWithIP(ctx, domain, path, query, result, bestIP)
}

// GetWithIP 发送 GET 请求（使用指定的 IP）
// specifiedIP: 指定使用的 IP 地址，如果为空则使用域名直接访问
func (c *HTTPClient) GetWithIP(ctx context.Context, domain, path string, query url.Values, result interface{}, specifiedIP string) error {
	// 构建完整 URL（使用域名，保证 TLS SNI 正确）
	fullURL := fmt.Sprintf("https://%s%s", domain, path)
	if len(query) > 0 {
		fullURL += "?" + query.Encode()
	}

	// 创建自定义 Dialer（将域名解析到指定 IP）
	var dialer *net.Dialer
	if specifiedIP != "" {
		log.DebugContextf(ctx, "使用指定 IP 访问 %s: %s", domain, specifiedIP)
		dialer = &net.Dialer{
			Timeout: 10 * time.Second,
		}

		// 设置自定义 Transport，将域名解析到指定 IP
		transport := c.httpClient.Transport.(*http.Transport).Clone()
		transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			// 提取端口号
			_, port, err := net.SplitHostPort(addr)
			if err != nil {
				port = "443" // 默认 HTTPS 端口
			}

			// 使用指定 IP 进行连接
			targetAddr := net.JoinHostPort(specifiedIP, port)
			log.DebugContextf(ctx, "DialContext: 将 %s 解析到 %s", addr, targetAddr)
			return dialer.DialContext(ctx, network, targetAddr)
		}

		// 为这次请求创建临时客户端
		tempClient := &http.Client{
			Timeout:   c.httpClient.Timeout,
			Transport: transport,
		}
		return c.doRequest(ctx, tempClient, fullURL, domain, result)
	}

	// 降级：直接使用域名（标准 DNS 解析）
	log.DebugContextf(ctx, "未找到指定 IP，直接使用域名: %s", domain)
	return c.doRequest(ctx, c.httpClient, fullURL, domain, result)
}

// doRequest 执行 HTTP 请求并解析 JSON 响应
func (c *HTTPClient) doRequest(ctx context.Context, httpClient *http.Client, fullURL, domain string, result interface{}) error {
	req, err := http.NewRequestWithContext(ctx, "GET", fullURL, nil)
	if err != nil {
		return fmt.Errorf("创建请求失败: %w", err)
	}

	// 设置必要的请求头
	req.Header.Set("User-Agent", "data-collector/1.0")

	// 发送请求
	start := time.Now()
	log.DebugContextf(ctx, "[collector-http] GET start domain=%s url=%s timeout=%s", domain, fullURL, httpClient.Timeout)
	resp, err := httpClient.Do(req)
	if err != nil {
		log.WarnContextf(ctx, "[collector-http] GET error domain=%s duration=%s error=%v", domain, time.Since(start), err)
		return fmt.Errorf("请求 %s 失败: %w", domain, err)
	}
	defer resp.Body.Close()
	log.DebugContextf(ctx, "[collector-http] GET response domain=%s status=%d duration=%s", domain, resp.StatusCode, time.Since(start))

	// 检查状态码
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP 错误 %d", resp.StatusCode)
	}

	// 解析 JSON
	if result != nil {
		decoder := json.NewDecoder(resp.Body)
		if err := decoder.Decode(result); err != nil {
			return fmt.Errorf("JSON 解析失败: %w", err)
		}
	}

	return nil
}
