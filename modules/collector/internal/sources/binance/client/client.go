package binance

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/mooyang-code/moox/modules/collector/internal/httpclient"
)

// 域名常量
const (
	SpotDomain = "api.binance.com"  // 现货域名
	SwapDomain = "fapi.binance.com" // U本位永续合约域名
)

// API 端点
const (
	SpotKlineEndpoint        = "/api/v3/klines"        // 现货K线
	SpotTradesEndpoint       = "/api/v3/aggTrades"     // 现货聚合成交（支持 fromId）
	SpotExchangeInfoEndpoint = "/api/v3/exchangeInfo"  // 现货交易规则和交易对
	SwapKlineEndpoint        = "/fapi/v1/klines"       // 永续合约K线
	SwapTradesEndpoint       = "/fapi/v1/aggTrades"    // 永续合约聚合成交（支持 fromId）
	SwapExchangeInfoEndpoint = "/fapi/v1/exchangeInfo" // 永续合约交易规则和交易对
)

// Client 币安客户端
type Client struct {
	*httpclient.HTTPClient
	spotDomains []string
	swapDomains []string
}

// NewClient 创建币安客户端
func NewClient() *Client {
	return &Client{
		HTTPClient:  httpclient.NewHTTPClient(),
		spotDomains: []string{SpotDomain},
		swapDomains: []string{SwapDomain},
	}
}

// SetSpotBaseURL 设置现货 API 基础地址。
func (c *Client) SetSpotBaseURL(rawURL string) error {
	return c.SetSpotBaseURLs([]string{rawURL})
}

// SetSpotBaseURLs configures the ordered public Spot endpoints. Binance
// exposes several official endpoints; keeping the order explicit lets a
// short-lived SCF invocation fail over within its bounded request budget.
func (c *Client) SetSpotBaseURLs(rawURLs []string) error {
	domains, err := domainsFromBaseURLs(rawURLs)
	if err != nil {
		return err
	}
	if len(domains) == 0 {
		return fmt.Errorf("Binance 现货 API 地址不能为空")
	}
	c.spotDomains = domains
	return nil
}

// SetSwapBaseURL 设置合约 API 基础地址。
func (c *Client) SetSwapBaseURL(rawURL string) error {
	return c.SetSwapBaseURLs([]string{rawURL})
}

func (c *Client) SetSwapBaseURLs(rawURLs []string) error {
	domains, err := domainsFromBaseURLs(rawURLs)
	if err != nil {
		return err
	}
	if len(domains) == 0 {
		return fmt.Errorf("Binance 合约 API 地址不能为空")
	}
	c.swapDomains = domains
	return nil
}

// SpotDomain 返回现货 API 域名。
func (c *Client) SpotDomain() string {
	if len(c.spotDomains) == 0 {
		return SpotDomain
	}
	return c.spotDomains[0]
}

func (c *Client) SpotDomains() []string {
	if len(c.spotDomains) == 0 {
		return []string{SpotDomain}
	}
	return append([]string(nil), c.spotDomains...)
}

// SwapDomain 返回合约 API 域名。
func (c *Client) SwapDomain() string {
	if len(c.swapDomains) == 0 {
		return SwapDomain
	}
	return c.swapDomains[0]
}

func (c *Client) SwapDomains() []string {
	if len(c.swapDomains) == 0 {
		return []string{SwapDomain}
	}
	return append([]string(nil), c.swapDomains...)
}

func domainsFromBaseURLs(rawURLs []string) ([]string, error) {
	domains := make([]string, 0, len(rawURLs))
	seen := make(map[string]struct{}, len(rawURLs))
	for _, rawURL := range rawURLs {
		domain, err := domainFromBaseURL(rawURL)
		if err != nil {
			return nil, err
		}
		if domain == "" {
			continue
		}
		if _, exists := seen[domain]; exists {
			continue
		}
		seen[domain] = struct{}{}
		domains = append(domains, domain)
	}
	return domains, nil
}

func domainFromBaseURL(rawURL string) (string, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return "", nil
	}
	if !strings.Contains(rawURL, "://") {
		rawURL = "https://" + rawURL
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("解析 Binance API 地址失败: %w", err)
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("Binance API 地址缺少 host: %s", rawURL)
	}
	return parsed.Host, nil
}

// GetDirect sends a normal HTTPS request using system DNS.
func (c *Client) GetDirect(ctx context.Context, domain, path string, query url.Values, result interface{}) error {
	return c.HTTPClient.Get(ctx, domain, path, query, result)
}

// GetDirectWithIPs tries the control-plane DNS snapshot before falling back
// to the normal hostname resolver. The URL remains the hostname so TLS SNI
// and the HTTP Host header are never replaced by an address literal.
func (c *Client) GetDirectWithIPs(ctx context.Context, domain string, ips []string, path string, query url.Values, result interface{}) error {
	return c.HTTPClient.GetWithIPs(ctx, domain, ips, path, query, result)
}

// GetDirectStream is the streaming counterpart of GetDirect.
func (c *Client) GetDirectStream(ctx context.Context, domain, path string, query url.Values, consume func(io.Reader) error) error {
	return c.HTTPClient.GetStream(ctx, domain, path, query, consume)
}

func (c *Client) GetDirectStreamWithIPs(ctx context.Context, domain string, ips []string, path string, query url.Values, consume func(io.Reader) error) error {
	return c.HTTPClient.GetStreamWithIPs(ctx, domain, ips, path, query, consume)
}
