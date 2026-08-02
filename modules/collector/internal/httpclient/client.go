package httpclient

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/jobcontext"
	"trpc.group/trpc-go/trpc-go/log"
)

// HTTPClient issues HTTPS requests using the platform DNS resolver and TLS SNI.
type HTTPClient struct{ httpClient *http.Client }

const defaultRequestTimeout = 5 * time.Second

// StatusError reports a non-success HTTP response.
type StatusError struct{ StatusCode int }

func (e *StatusError) Error() string { return fmt.Sprintf("HTTP status %d", e.StatusCode) }

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
	return c.get(ctx, domain, path, query, func(reader io.Reader) error {
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
	return c.get(ctx, domain, path, query, consume)
}

func (c *HTTPClient) get(ctx context.Context, domain, path string, query url.Values, consume func(io.Reader) error) error {
	fullURL := fmt.Sprintf("https://%s%s", domain, path)
	if len(query) > 0 {
		fullURL += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fullURL, nil)
	if err != nil {
		return fmt.Errorf("创建请求失败: %w", err)
	}
	req.Header.Set("User-Agent", "moox-collector/1.0")
	started := time.Now()
	resp, err := c.httpClient.Do(req)
	if err != nil {
		log.WarnContextf(ctx, "collector_http_failed domain=%s duration_ms=%d error=%q", domain, time.Since(started).Milliseconds(), err)
		return fmt.Errorf("请求 %s 失败: %w", domain, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log.WarnContextf(ctx, "collector_http_failed domain=%s status=%d duration_ms=%d job_item_id=%q", domain, resp.StatusCode, time.Since(started).Milliseconds(), jobcontext.JobItemID(ctx))
		return &StatusError{StatusCode: resp.StatusCode}
	}
	if err := consume(resp.Body); err != nil {
		return fmt.Errorf("JSON 解析失败: %w", err)
	}
	log.InfoContextf(ctx, "collector_http_completed domain=%s status=%d duration_ms=%d job_item_id=%q", domain, resp.StatusCode, time.Since(started).Milliseconds(), jobcontext.JobItemID(ctx))
	return nil
}
