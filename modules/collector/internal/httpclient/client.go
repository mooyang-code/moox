package httpclient

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
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

// GetWithIPs first dials the supplied addresses while keeping domain in the
// URL. This preserves the Host header and TLS SNI. Only after every supplied
// address fails does it make one normal hostname request.
func (c *HTTPClient) GetWithIPs(ctx context.Context, domain string, ips []string, path string, query url.Values, result interface{}) error {
	return c.getWithIPs(ctx, domain, ips, path, query, func(reader io.Reader) error {
		if result == nil {
			return nil
		}
		return json.NewDecoder(reader).Decode(result)
	})
}

// GetStreamWithIPs is the streaming counterpart of GetWithIPs.
func (c *HTTPClient) GetStreamWithIPs(ctx context.Context, domain string, ips []string, path string, query url.Values, consume func(io.Reader) error) error {
	if consume == nil {
		return fmt.Errorf("response consumer is required")
	}
	return c.getWithIPs(ctx, domain, ips, path, query, consume)
}

func (c *HTTPClient) getWithIPs(ctx context.Context, domain string, ips []string, path string, query url.Values, consume func(io.Reader) error) error {
	for _, ip := range uniqueIPs(ips) {
		client := c.clientForIP(ip)
		if client == nil {
			break
		}
		if err := c.getWithClient(ctx, client, domain, path, query, consume); err == nil {
			return nil
		} else {
			log.WarnContextf(ctx, "collector_http_resolved_ip_failed domain=%s ip=%s error=%v", domain, ip, err)
		}
	}
	log.InfoContextf(ctx, "collector_http_resolved_ip_fallback domain=%s ips=%d", domain, len(uniqueIPs(ips)))
	return c.getWithClient(ctx, c.httpClient, domain, path, query, consume)
}

func (c *HTTPClient) clientForIP(ip string) *http.Client {
	if c == nil || c.httpClient == nil {
		return nil
	}
	base, ok := c.httpClient.Transport.(*http.Transport)
	if !ok || base == nil {
		return nil
	}
	parsed := net.ParseIP(strings.TrimSpace(ip))
	if parsed == nil {
		return nil
	}
	transport := base.Clone()
	originalDial := transport.DialContext
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		_, port, err := net.SplitHostPort(address)
		if err != nil || port == "" {
			port = "443"
		}
		target := net.JoinHostPort(parsed.String(), port)
		if originalDial != nil {
			return originalDial(ctx, network, target)
		}
		return (&net.Dialer{}).DialContext(ctx, network, target)
	}
	client := *c.httpClient
	client.Transport = transport
	return &client
}

func (c *HTTPClient) getWithClient(ctx context.Context, client *http.Client, domain, path string, query url.Values, consume func(io.Reader) error) error {
	if client == nil {
		return fmt.Errorf("HTTP client is not initialized")
	}
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
	resp, err := client.Do(req)
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

func uniqueIPs(ips []string) []string {
	seen := make(map[string]struct{}, len(ips))
	result := make([]string, 0, len(ips))
	for _, value := range ips {
		ip := net.ParseIP(strings.TrimSpace(value))
		if ip == nil {
			continue
		}
		value = ip.String()
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
