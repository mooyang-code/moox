package httpclient

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"trpc.group/trpc-go/trpc-go/log"
)

// HTTPClient issues HTTPS requests using the platform DNS resolver and TLS SNI.
type HTTPClient struct {
	// ResponseObserver 在每次收到响应（含非 200）后同步调用，用于读取限流相关的响应头。
	ResponseObserver func(domain string, status int, header http.Header)
	httpClient       *http.Client
}

const defaultRequestTimeout = 5 * time.Second

// StatusError reports a non-success HTTP response.
type StatusError struct {
	StatusCode int
	// RetryAfter 是响应头 Retry-After 给出的等待时间，没有则为 0。
	RetryAfter time.Duration
}

func (e *StatusError) Error() string { return fmt.Sprintf("HTTP status %d", e.StatusCode) }

// retryAfterOf 解析 Retry-After（秒数）。
func retryAfterOf(header http.Header) time.Duration {
	seconds, err := strconv.Atoi(strings.TrimSpace(header.Get("Retry-After")))
	if err != nil || seconds <= 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

func NewHTTPClient(base ...*http.Client) *HTTPClient {
	if len(base) > 0 && base[0] != nil {
		return &HTTPClient{httpClient: base[0]}
	}
	return &HTTPClient{httpClient: &http.Client{
		Timeout: defaultRequestTimeout,
		Transport: &http.Transport{
			TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12},
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 10,
			IdleConnTimeout:     90 * time.Second,
		},
	}}
}

func (c *HTTPClient) Get(ctx context.Context, domain, path string, query url.Values, result interface{}) error {
	return c.getWithClient(ctx, c.httpClient, domain, path, query, func(reader io.Reader) error {
		if result == nil {
			return nil
		}
		return json.NewDecoder(reader).Decode(result)
	})
}

// GetStream lets a caller decode a bounded response without an intermediate copy.
func (c *HTTPClient) GetStream(ctx context.Context, domain, path string, query url.Values, consume func(io.Reader) error) error {
	if consume == nil {
		return fmt.Errorf("response consumer is required")
	}
	return c.getWithClient(ctx, c.httpClient, domain, path, query, consume)
}

func (c *HTTPClient) getWithClient(ctx context.Context, client *http.Client, domain, path string, query url.Values, consume func(io.Reader) error) (retErr error) {
	if client == nil {
		return fmt.Errorf("HTTP client is not initialized")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	fullURL := fmt.Sprintf("https://%s%s", domain, path)
	if len(query) > 0 {
		fullURL += "?" + query.Encode()
	}
	// 调用方通过 WithTrace 挂了 Trace 时，记录这次请求的 DNS、建连、TLS 与首字节耗时。
	status := 0
	var rt *requestTrace
	if trace := traceFrom(ctx); trace != nil {
		ctx, rt = trace.begin(ctx, domain)
		defer func() { rt.done(status, retErr) }()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fullURL, nil)
	if err != nil {
		return fmt.Errorf("创建请求失败: %w", err)
	}
	req.Header.Set("User-Agent", "moox-collector/1.0")
	applyRequestHeaders(ctx, req)
	started := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		log.WarnContextf(ctx, "collector_http_failed domain=%s duration_ms=%d error=%q", domain, time.Since(started).Milliseconds(), err)
		return fmt.Errorf("请求 %s 失败: %w", domain, err)
	}
	defer resp.Body.Close()
	status = resp.StatusCode
	rt.response(resp.Header)
	if c.ResponseObserver != nil {
		c.ResponseObserver(domain, resp.StatusCode, resp.Header)
	}
	if resp.StatusCode != http.StatusOK {
		if rt != nil {
			hint, _ := io.ReadAll(io.LimitReader(resp.Body, 100))
			rt.body(hint)
		}
		log.WarnContextf(ctx, "collector_http_failed domain=%s status=%d duration_ms=%d", domain, resp.StatusCode, time.Since(started).Milliseconds())
		return &StatusError{StatusCode: resp.StatusCode, RetryAfter: retryAfterOf(resp.Header)}
	}
	if err := consume(resp.Body); err != nil {
		return fmt.Errorf("JSON 解析失败: %w", err)
	}
	// 正常请求只有几十毫秒，逐条记录只会淹没日志；只记录偏慢的请求，用于定位行情源的延迟尖峰。
	if elapsed := time.Since(started); elapsed >= slowRequestLogThreshold {
		log.InfoContextf(ctx, "collector_http_completed domain=%s status=%d duration_ms=%d", domain, resp.StatusCode, elapsed.Milliseconds())
	}
	return nil
}

// slowRequestLogThreshold 是需要记录成功请求耗时的下限。
const slowRequestLogThreshold = 500 * time.Millisecond
